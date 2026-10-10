// Ported from packages/policy/src/calibration/harness.ts
// (P2-T21; ARCH §10.8, §16.1; PLAN §3.3 line 549).
//
// Calibration harness — expected calibration error (ECE) and reliability per
// `(policy_version, model binding)` on the evaluation split, with the A1
// promotion gate: a binding must pass calibration BEFORE any policy using it is
// promotable (§10.8: swapping the decision-engine model invalidates the previous
// calibration; the new binding must re-pass). The A1 gate covers BOTH gate
// inputs when supplied: calibration ECE AND the binding's latency (p95,
// nearest-rank, against an optional `latencyBudgetMs` — a budget with no latency
// evidence fails closed). Displayed confidence is pinned to
// `(policy_version, model binding)` (D3), which is exactly the key
// RunCalibrationHarness reports: `PolicyVersion` + BindingKey.
//
// Pure and deterministic: equal-width binning over [0, 1], no randomness, no
// I/O, no clocks; the same samples always produce the same ECE. Thresholds and
// bin counts are DATA parameters (D11 spirit) with labeled bootstrap defaults;
// NO storage — Phase 5 wires persistence and the promotion pipeline.
//
// Error mapping (CONVENTIONS.md §Errors): every TS RangeError guard becomes a
// coded docutil error (EDOCSYNC_VALIDATION — the closest existing code for
// fail-closed input validation) with the message text preserved EXACTLY, so the
// ported assertions on message content still hold.

package policy

import (
	"math"
	"sort"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// DefaultCalibrationBins is the bootstrap bin count for the reliability
// diagram (equal-width over [0, 1]) (TS: DEFAULT_CALIBRATION_BINS).
const DefaultCalibrationBins = 10

// DefaultCalibrationECEThreshold is the bootstrap default ECE promotion
// threshold (TS: DEFAULT_CALIBRATION_ECE_THRESHOLD). Deliberately lenient until
// a repository calibrates a real bound on its evaluation split (§8.4); pass an
// explicit threshold in production and never treat this default as a
// load-bearing quality bar.
const DefaultCalibrationECEThreshold = 0.1

// CalibrationSample is one calibration sample: the model's asserted probability
// and whether the outcome was correct (TS: CalibrationSample).
type CalibrationSample struct {
	// Probability is the asserted probability in [0, 1] — the confidence the
	// model displayed.
	Probability float64 `json:"probability"`
	// Correct reports whether the model's call was ultimately correct
	// (labeled data, §8.7).
	Correct bool `json:"correct"`
}

// CalibrationBinding is the (policy_version, model binding) half of the
// calibration key (D3/§10.8) (TS: CalibrationBinding).
type CalibrationBinding struct {
	// Model is the model identifier as bound in `models.yml` (ARCH §10.3).
	Model string `json:"model"`
	// Provider is the optional provider routing segment; omitted → `default`
	// in the key (TS `provider?: string` ⇒ *string + omitempty).
	Provider *string `json:"provider,omitempty"`
}

// CalibrationBin is one reliability bin (equal-width slice of [0, 1]). Empty
// bins report zero accuracy/confidence (TS: CalibrationBin).
type CalibrationBin struct {
	BinLower      float64 `json:"binLower"`
	BinUpper      float64 `json:"binUpper"`
	Count         int     `json:"count"`
	Accuracy      float64 `json:"accuracy"`
	AvgConfidence float64 `json:"avgConfidence"`
}

// CalibrationResult is the full per-(policy_version, binding) calibration
// result (A1 gate input) (TS: CalibrationResult). The optional legs are pointers
// so "absent" stays distinguishable from a zero value:
// P95LatencyMs/LatencyBudgetMs/LatencyOK are nil iff no latency samples/budget
// were supplied.
type CalibrationResult struct {
	PolicyVersion int                `json:"policyVersion"`
	BindingKey    string             `json:"bindingKey"`
	Binding       CalibrationBinding `json:"binding"`
	// ECE = Σ (binCount/total × |accuracy − avgConfidence|) over non-empty bins.
	ECE float64 `json:"ece"`
	// BinCount is the equal-width bin count the reliability diagram used.
	BinCount int `json:"binCount"`
	// PerBin carries one row per bin, including empties (fixed-length
	// reliability diagram).
	PerBin []CalibrationBin `json:"perBin"`
	// Calibrated is the A1 gate verdict over BOTH gate inputs:
	// `ece ≤ threshold` AND — when a latency budget was supplied —
	// `p95_latency_ms ≤ latency_budget_ms`. No budget input → the verdict is
	// the ECE check alone (nothing to gate against).
	Calibrated bool `json:"calibrated"`
	// Threshold is the DATA threshold this result was gated against
	// (provenance).
	Threshold float64 `json:"threshold"`
	// P95LatencyMs is the p95 of the latency samples (ms), nearest-rank —
	// present iff samples were supplied.
	P95LatencyMs *float64 `json:"p95_latency_ms,omitempty"`
	// LatencyBudgetMs is the latency budget this result was gated against
	// (ms) — present iff a budget was supplied.
	LatencyBudgetMs *float64 `json:"latency_budget_ms,omitempty"`
	// LatencyOK is `p95_latency_ms ≤ latency_budget_ms` — present iff a budget
	// was supplied (provenance for `calibrated`).
	LatencyOK *bool `json:"latency_ok,omitempty"`
}

// calibrationRangeError builds the coded error every TS RangeError guard of the
// calibration module returns: EDOCSYNC_VALIDATION with the TS message text
// preserved verbatim.
func calibrationRangeError(format string, args ...any) error {
	return docutil.Newf(docutil.CodeValidation, format, args...)
}

// calibrationUnitInterval ports the module-private assertUnitInterval helper:
// a finite number in [0, 1] or fail closed.
func calibrationUnitInterval(value float64, name string) error {
	if math.IsNaN(value) || value < 0 || value > 1 {
		return calibrationRangeError("%s must be a finite number in [0, 1] (got %s)", name, jsString(value))
	}
	return nil
}

// BindingKey returns the binding half of the D3 calibration key:
// `${model}#${provider ?? 'default'}` (TS: bindingKey).
func BindingKey(binding CalibrationBinding) (string, error) {
	if binding.Model == "" {
		return "", calibrationRangeError("binding.model must be a non-empty string (got %s)", binding.Model)
	}
	provider := "default"
	if binding.Provider != nil {
		provider = *binding.Provider
	}
	return binding.Model + "#" + provider, nil
}

// P95LatencyMs returns the p95 latency by the NEAREST-RANK method (documented,
// deterministic): sort the samples ascending and take the value at 1-based rank
// `ceil(0.95 × n)` — no interpolation, so the reported p95 is always an
// observed sample. Examples: n = 4 → rank 4 (the maximum); n = 20 → rank 19 (the
// second-largest). A non-positive or non-finite sample fails closed; an empty
// sample set has no p95 and fails closed (a silent pass would be fail-open for
// the A1 gate) (TS: p95LatencyMs).
func P95LatencyMs(samples []float64) (float64, error) {
	if len(samples) == 0 {
		return 0, calibrationRangeError("latency p95 requires at least one sample — an empty set has no p95")
	}
	for _, sample := range samples {
		if math.IsNaN(sample) || math.IsInf(sample, 0) || sample < 0 {
			return 0, calibrationRangeError("latency samples must be finite numbers ≥ 0 (got %s)", jsString(sample))
		}
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(0.95 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		// Unreachable for a non-empty list (rank ∈ [1, n]) — kept fail-closed.
		return 0, calibrationRangeError("p95 rank %d out of bounds for %d sample(s)", rank, len(sorted))
	}
	return sorted[rank-1], nil
}

// RunCalibrationHarnessInput carries the harness inputs (TS:
// RunCalibrationHarnessInput). All numeric knobs are pointers/optional so
// "absent" (apply the bootstrap default) stays distinct from an explicit value
// such as `threshold: 0`.
type RunCalibrationHarnessInput struct {
	// PolicyVersion is the policy version the samples were collected under
	// (D3 key half).
	PolicyVersion int
	Binding       CalibrationBinding
	// Samples are the labeled samples from the evaluation split; a run with no
	// samples is a programmer error.
	Samples []CalibrationSample
	// Bins is the equal-width bin count; nil applies DefaultCalibrationBins
	// (data param).
	Bins *int
	// Threshold is the ECE pass threshold; nil applies
	// DefaultCalibrationECEThreshold (data param).
	Threshold *float64
	// LatencySamples are the latency samples (ms) from the evaluation split —
	// the second A1 gate input. Optional: nil → no latency fields on the
	// result and the gate is ECE-only. Every sample must be a finite number
	// ≥ 0; an empty (non-nil, zero-length) slice is a programmer error
	// (fail closed).
	LatencySamples []float64
	// LatencyBudgetMs is the latency budget (ms) the p95 must not exceed.
	// Optional. When present it REQUIRES LatencySamples (a declared budget
	// with no latency evidence must not silently pass the gate — fail closed)
	// and the A1 verdict becomes `ece ≤ threshold AND p95 ≤ budget`.
	LatencyBudgetMs *float64
}

// RunCalibrationHarness computes ECE + the reliability diagram for one
// (policy_version, model binding) key (ARCH §10.8). Equal-width binning over
// [0, 1]: sample p lands in bin `min(bins − 1, floor(p × bins))` — 1.0 lands
// in the last bin. Empty bins carry count/accuracy/avgConfidence 0 and
// contribute nothing to ECE. Fail-closed: an empty sample set, an out-of-range
// probability, a non-positive bin count or an out-of-range threshold fails
// closed (a silent pass would be fail-open for the promotion gate).
func RunCalibrationHarness(input RunCalibrationHarnessInput) (*CalibrationResult, error) {
	if input.PolicyVersion < 1 {
		return nil, calibrationRangeError("policyVersion must be a positive integer (got %d)", input.PolicyVersion)
	}
	key, err := BindingKey(input.Binding)
	if err != nil {
		return nil, err
	}
	if len(input.Samples) == 0 {
		return nil, calibrationRangeError("calibration requires at least one sample — an empty split must not pass the promotion gate")
	}
	bins := DefaultCalibrationBins
	if input.Bins != nil {
		bins = *input.Bins
	}
	if bins < 1 {
		return nil, calibrationRangeError("bins must be a positive integer (got %d)", bins)
	}
	threshold := DefaultCalibrationECEThreshold
	if input.Threshold != nil {
		threshold = *input.Threshold
	}
	if err := calibrationUnitInterval(threshold, "threshold"); err != nil {
		return nil, err
	}
	if input.LatencyBudgetMs != nil {
		if math.IsNaN(*input.LatencyBudgetMs) || math.IsInf(*input.LatencyBudgetMs, 0) || *input.LatencyBudgetMs < 0 {
			return nil, calibrationRangeError("latencyBudgetMs must be a finite non-negative number (got %s)", jsString(*input.LatencyBudgetMs))
		}
	}
	if input.LatencyBudgetMs != nil && input.LatencySamples == nil {
		return nil, calibrationRangeError(
			"latencyBudgetMs requires latencySamples — a declared budget with no latency evidence " +
				"must not silently pass the promotion gate (fail closed)")
	}

	counts := make([]int, bins)
	correctCounts := make([]int, bins)
	confidenceSums := make([]float64, bins)
	for _, sample := range input.Samples {
		if err := calibrationUnitInterval(sample.Probability, "sample.probability"); err != nil {
			return nil, err
		}
		// TS also guards `typeof sample.correct !== 'boolean'`; Go's type
		// system makes a non-boolean `correct` unrepresentable, so the guard
		// has no port (documented deviation).
		binIndex := int(math.Min(float64(bins-1), math.Floor(sample.Probability*float64(bins))))
		counts[binIndex]++
		if sample.Correct {
			correctCounts[binIndex]++
		}
		confidenceSums[binIndex] += sample.Probability
	}

	total := len(input.Samples)
	perBin := make([]CalibrationBin, 0, bins)
	ece := 0.0
	binWidth := 1.0 / float64(bins)
	for index := 0; index < bins; index++ {
		count := counts[index]
		accuracy := 0.0
		avgConfidence := 0.0
		if count != 0 {
			accuracy = float64(correctCounts[index]) / float64(count)
			avgConfidence = confidenceSums[index] / float64(count)
		}
		perBin = append(perBin, CalibrationBin{
			BinLower:      float64(index) * binWidth,
			BinUpper:      float64(index+1) * binWidth,
			Count:         count,
			Accuracy:      accuracy,
			AvgConfidence: avgConfidence,
		})
		if count > 0 {
			ece += (float64(count) / float64(total)) * math.Abs(accuracy-avgConfidence)
		}
	}

	// A1 latency leg (optional): p95 over the supplied samples, gated against
	// the budget when one is declared. A budget without latency evidence fails
	// closed above; samples without a budget are REPORTED but gate nothing
	// (provenance for the next calibration round), so `calibrated` stays
	// ECE-only there.
	var p95Latency *float64
	var latencyOK *bool
	if input.LatencySamples != nil {
		value, err := P95LatencyMs(input.LatencySamples)
		if err != nil {
			return nil, err
		}
		p95Latency = &value
		if input.LatencyBudgetMs != nil {
			ok := value <= *input.LatencyBudgetMs
			latencyOK = &ok
		}
	}
	calibrated := ece <= threshold && (latencyOK == nil || *latencyOK)

	result := &CalibrationResult{
		PolicyVersion: input.PolicyVersion,
		BindingKey:    key,
		Binding:       input.Binding,
		ECE:           ece,
		BinCount:      bins,
		PerBin:        perBin,
		Calibrated:    calibrated,
		Threshold:     threshold,
	}
	if p95Latency != nil {
		result.P95LatencyMs = p95Latency
	}
	if input.LatencyBudgetMs != nil {
		budget := *input.LatencyBudgetMs
		result.LatencyBudgetMs = &budget
	}
	if latencyOK != nil {
		result.LatencyOK = latencyOK
	}
	return result, nil
}

// PromotionGate is an A1 promotion gate result (§10.8): every binding must pass
// calibration before promotion (TS: PromotionGate).
type PromotionGate struct {
	Promotable bool
	// FailingKeys lists the binding keys whose calibration failed, in input
	// order, deduplicated.
	FailingKeys []string
}

// GatePromotion is the A1 gate (ARCH §10.8, §16.1): a binding must pass
// calibration before promotion — one miscalibrated binding fails the whole
// candidate set. Fail-closed on missing evidence: an EMPTY result set is not
// promotable (promoting with zero calibration evidence would be fail-open); the
// failingKeys list is empty because the failure is "no evidence", not a named
// binding.
func GatePromotion(results []CalibrationResult) PromotionGate {
	if len(results) == 0 {
		return PromotionGate{Promotable: false, FailingKeys: []string{}}
	}
	failingKeys := []string{}
	for _, result := range results {
		if result.Calibrated {
			continue
		}
		seen := false
		for _, key := range failingKeys {
			if key == result.BindingKey {
				seen = true
				break
			}
		}
		if !seen {
			failingKeys = append(failingKeys, result.BindingKey)
		}
	}
	return PromotionGate{Promotable: len(failingKeys) == 0, FailingKeys: failingKeys}
}
