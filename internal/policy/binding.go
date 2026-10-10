// Ported from packages/policy/src/calibration/binding.ts
// (P12-T09; ARCH §10.8; D3).
//
// Per-binding calibration re-check (ARCH §10.8; D3): thresholds are calibrated
// PER MODEL BINDING — swapping the decision-engine model invalidates the
// previous calibration, and a binding change failing calibration on the
// evaluation split CANNOT promote. This module exports the typed verdict plus
// the gate-library-compatible rows the promotion runner consumes.
//
// WIRING (shipped, P12-T09): the `binding_calibration` gate IS wired. The
// promotion pipeline's enforcement point accepts the optional bindingCalibration
// evidence and forwards it into createPromotionGateLibrary(...).checkGates(...),
// which imports BindingCalibrationGateRows from this file and appends its rows
// ONLY when binding-calibration evidence is supplied. The gate is CONDITIONAL by
// design and is therefore NOT listed in MandatoryGateNames (gates.go): absent
// evidence ⇒ not evaluated (Phase-10/W2 candidates carry no binding context);
// present-but-empty ⇒ a single failing row (fail closed —
// BindingCalibrationGateRows); a binding CHANGE always supplies evidence through
// the composition layer. No existing gate is weakened — this remains a strictly
// additive check riding the promotion checklist.
//
// Confidence display pinning (D3; R18): PinnedConfidenceLabel and
// PinnedConfidenceDisplay pin EVERY displayed confidence to its producing
// `(policy_version, model binding)` — the same number means different things
// across versions and bindings. The Phase-7/8 CLI display surfaces
// (canonical-report render + check-run body, core/cli ownership) would call
// PinnedConfidenceDisplay at render time; the helper is the single format
// contract, tested here.
//
// OD-R29/31/32 posture (recorded data, note comments only): a binding's rolling
// re-check cadence and sample fraction are versioned policy data (OD-R29
// spirit); integrated-mode criteria (OD-R31) ride the same promotion gates and
// add nothing here; cross-repo calibration surfaces (OD-R32) would consume the
// verdicts read-only, advisory only.

package policy

import (
	"fmt"
	"math"
	"strconv"
)

// BindingCalibrationGateName is the additive promotion-gate name this check
// rides (TS: BINDING_CALIBRATION_GATE_NAME).
const BindingCalibrationGateName = "binding_calibration"

// BindingCalibrationFailureReason is why a binding change is not promotable
// (typed, named — INV-9 spirit) (TS: BindingCalibrationFailureReason).
type BindingCalibrationFailureReason string

const (
	// BindingCalibrationReasonFailed marks a binding whose evaluation-split
	// calibration (ECE and/or latency) failed.
	BindingCalibrationReasonFailed BindingCalibrationFailureReason = "binding_calibration_failed"
	// BindingCalibrationReasonNoEvidence marks a missing evidence set — the
	// fail-closed refusal to promote without calibration evidence.
	BindingCalibrationReasonNoEvidence BindingCalibrationFailureReason = "binding_calibration_no_evidence"
)

// BindingCalibrationVerdict is the typed verdict of the per-binding calibration
// re-check (TS: BindingCalibrationVerdict). The TS discriminated union becomes
// one struct: Reason is non-empty exactly when Promotable is false, so a
// promotable verdict has no failure path and vice versa.
type BindingCalibrationVerdict struct {
	Promotable bool
	// PolicyVersion is the (candidate) policy version the binding change rides
	// on (D3 key half).
	PolicyVersion int
	// BindingKey is the changed model binding (D3 key half).
	BindingKey string
	// Gate is always BindingCalibrationGateName.
	Gate string
	// Reason names the refusal — present iff Promotable is false.
	Reason BindingCalibrationFailureReason
	// PinnedConfidenceLabel is the D3-pinned display fragment —
	// `policy v<N> · model <key>`.
	PinnedConfidenceLabel string
	Detail                string
}

// EvaluateBindingCalibrationInput carries the re-check inputs (TS:
// EvaluateBindingCalibrationInput).
type EvaluateBindingCalibrationInput struct {
	// PolicyVersion is the (candidate) policy version the binding change rides
	// on (D3 key half).
	PolicyVersion int
	// Binding is the CHANGED model binding (D3 key half).
	Binding CalibrationBinding
	// Result is evaluation-split calibration evidence for exactly this binding
	// + policy version (RunCalibrationHarness output). OPTIONAL ONLY in shape:
	// absent evidence is a fail-closed `binding_calibration_no_evidence`
	// not-promotable verdict — promoting with zero calibration evidence would
	// be fail-open (ARCH §10.8).
	Result *CalibrationResult
}

// EvaluateBindingCalibration is the per-binding calibration re-check
// (ARCH §10.8): a binding whose evaluation-split calibration (ECE + optional
// latency legs, Calibrated) FAILS cannot promote — a typed not-promotable
// verdict with a named gate. Mismatched evidence (a result for a different
// policy version or a different binding key) fails closed rather than passing.
// Absent evidence is a not-promotable verdict, never a pass.
func EvaluateBindingCalibration(input EvaluateBindingCalibrationInput) (*BindingCalibrationVerdict, error) {
	if input.PolicyVersion < 1 {
		return nil, calibrationRangeError("policyVersion must be a positive integer (got %d)", input.PolicyVersion)
	}
	key, err := BindingKey(input.Binding)
	if err != nil {
		return nil, err
	}
	label, err := PinnedConfidenceLabel(input.PolicyVersion, input.Binding)
	if err != nil {
		return nil, err
	}
	if input.Result == nil {
		return &BindingCalibrationVerdict{
			Promotable:            false,
			PolicyVersion:         input.PolicyVersion,
			BindingKey:            key,
			Gate:                  BindingCalibrationGateName,
			Reason:                BindingCalibrationReasonNoEvidence,
			PinnedConfidenceLabel: label,
			Detail: fmt.Sprintf(
				"binding %s has NO evaluation-split calibration evidence under policy v%d — "+
					"a binding change without calibration evidence cannot promote (fail closed; ARCH §10.8)",
				key, input.PolicyVersion),
		}, nil
	}
	result := input.Result
	if result.PolicyVersion != input.PolicyVersion || result.BindingKey != key {
		return nil, calibrationRangeError(
			"calibration evidence mismatch: result covers (v%d, %s) but the check targets (v%d, %s) — "+
				"fail closed (D3: evidence is pinned to the exact (policy_version, binding) key)",
			result.PolicyVersion, result.BindingKey, input.PolicyVersion, key)
	}
	latencyClause := ""
	if result.LatencyOK != nil {
		latencyClause = ", latency_ok " + strconv.FormatBool(*result.LatencyOK)
	}
	failedClause := ""
	if result.LatencyOK != nil && !*result.LatencyOK {
		failedClause = ", latency over budget"
	}
	if result.Calibrated {
		return &BindingCalibrationVerdict{
			Promotable:            true,
			PolicyVersion:         input.PolicyVersion,
			BindingKey:            key,
			Gate:                  BindingCalibrationGateName,
			PinnedConfidenceLabel: label,
			Detail: fmt.Sprintf(
				"binding %s passed the evaluation-split calibration re-check under policy v%d "+
					"(ECE %s ≤ threshold %s%s) — promotable (ARCH §10.8)",
				key, input.PolicyVersion, jsToFixed(result.ECE, 4), jsToFixed(result.Threshold, 4), latencyClause),
		}, nil
	}
	return &BindingCalibrationVerdict{
		Promotable:            false,
		PolicyVersion:         input.PolicyVersion,
		BindingKey:            key,
		Gate:                  BindingCalibrationGateName,
		Reason:                BindingCalibrationReasonFailed,
		PinnedConfidenceLabel: label,
		Detail: fmt.Sprintf(
			"binding %s FAILED the evaluation-split calibration re-check under policy v%d "+
				"(ECE %s > threshold %s%s) — a binding change failing calibration cannot promote "+
				"(ARCH §10.8; P12-T09)",
			key, input.PolicyVersion, jsToFixed(result.ECE, 4), jsToFixed(result.Threshold, 4), failedClause),
	}, nil
}

// BindingCalibrationGateRows returns gate-library-compatible rows (the Go form
// of the TS `GateRow` seam) for the promotion checklist seam — one
// `binding_calibration:<key>` row per binding result. An EMPTY results list
// yields a single failing `binding_calibration` row (promoting with zero
// binding-calibration evidence would be fail-open).
func BindingCalibrationGateRows(results []CalibrationResult) []GateRow {
	if len(results) == 0 {
		return []GateRow{{
			Gate:   BindingCalibrationGateName,
			Passed: false,
			Detail: "no binding-calibration evidence supplied — fail closed (ARCH §10.8)",
		}}
	}
	rows := make([]GateRow, 0, len(results))
	for _, result := range results {
		outcome := "passed (promotable)"
		if !result.Calibrated {
			outcome = "FAILED (not promotable; ARCH §10.8)"
		}
		rows = append(rows, GateRow{
			Gate:   BindingCalibrationGateName + ":" + result.BindingKey,
			Passed: result.Calibrated,
			Detail: fmt.Sprintf(
				"binding %s under policy v%d: ECE %s vs threshold %s — %s",
				result.BindingKey, result.PolicyVersion,
				jsToFixed(result.ECE, 4), jsToFixed(result.Threshold, 4), outcome),
		})
	}
	return rows
}

// PinnedConfidenceLabel is the D3/R18 pin fragment: every displayed confidence
// is labeled with its producing policy version + model binding. Format contract
// (tested): `policy v<N> · model <model>#<provider ?? 'default'>`.
func PinnedConfidenceLabel(policyVersion int, binding CalibrationBinding) (string, error) {
	if policyVersion < 1 {
		return "", calibrationRangeError("policyVersion must be a positive integer (got %d)", policyVersion)
	}
	key, err := BindingKey(binding)
	if err != nil {
		return "", err
	}
	return "policy v" + strconv.Itoa(policyVersion) + " · model " + key, nil
}

// PinnedConfidenceDisplay is the D3/R18 full display projection: the confidence
// percentage + the pin fragment — the single format the Phase-7/8 CLI display
// surfaces (canonical report render, check-run body; core/cli ownership — see
// the wave report) wire at render time. Confidence is a unit-interval
// probability displayed as a whole percentage.
func PinnedConfidenceDisplay(confidence float64, policyVersion int, binding CalibrationBinding) (string, error) {
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		return "", calibrationRangeError("confidence must be a finite number in [0, 1] (got %s)", jsString(confidence))
	}
	label, err := PinnedConfidenceLabel(policyVersion, binding)
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(math.Round(confidence*100), 'f', 0, 64) + "% (" + label + ")", nil
}
