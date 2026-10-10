package openrouter

// Ported from packages/openrouter/src/routing/replacement.ts (P12-T11; ARCH
// §10.3; R2; D9) — the openrouter-side half of replacement-model shadow
// evaluation. ARCH §10.3: "`models.yml` changes are treated like policy
// changes: any model swap is shadow/canary evaluated (same machinery as
// §8.6) before production".
//
// PROMOTION (mirrored from the TS side, P12-T20): the report shape is the
// REGISTERED `docsync.routing-replacement-report/v1` artifact in
// internal/schemas (replacement-evaluation.go) — one definition only. The
// emitted report is validated AT CONSTRUCTION, exactly like the canary
// records it rides with.
//
// Composition seam (recorded; dependency direction): the canary lifecycle
// machinery lives in internal/policy (canary.go — EvaluateCanaryLifecycle /
// FilterConsultableRecords). An import would be LEGAL (openrouter already
// declares the workspace dependency on the TS side) but the runtime modules
// here deliberately import ONLY schemas and stay policy-free: the composition
// point is the RECORD FORMAT, not an import. This module emits validated
// `docsync.shadow-run-record/v1` rows (ReplacementEvaluationResult.CanaryRecords)
// — exactly the type the policy-side canary lifecycle consumes. The policy
// side needs zero awareness of openrouter.
//
// Sampling honesty: the evaluation workload IS the sample (deliberately
// selected cases), so the emitted records carry the SAME deterministic sampler
// id (`sha256-hex-threshold/v1`) with `fraction: 1` — the fraction-derived
// threshold is 2^32 → '100000000', which every 32-bit deterministic draw
// (`sha256(run_id ⊕ NUL ⊕ case_id)` first 8 hex chars, identical formula to
// the policy-side sampler) falls below. No randomness, no fabricated draws.
//
// No clocks, no randomness, no network in this module: the evaluation port is
// an injected seam (recorded doubles in tests, OD-2), clock and record ids are
// injected, the agreement threshold is caller-supplied versioned data (D11 —
// never hard-coded here).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// shadowRunRecordWireSchemaID is the literal schema tag every emitted canary
// record carries (TS SHADOW_RUN_RECORD_SCHEMA_ID). Declared here because
// internal/schemas keeps the identifier unexported; the same one-definition
// precedent as internal/policy's claimProposalWireSchemaID.
const shadowRunRecordWireSchemaID = "docsync.shadow-run-record/v1"

// replacementEvaluationReportWireSchemaID is the registry id of the emitted
// report (TS REPLACEMENT_EVALUATION_REPORT_SCHEMA_ID, unexported in
// internal/schemas).
const replacementEvaluationReportWireSchemaID = "docsync.routing-replacement-report/v1"

// replacementEvaluationReportWireKind is the report kind literal
// (TS: kind: 'replacement_model_shadow_evaluation').
const replacementEvaluationReportWireKind = "replacement_model_shadow_evaluation"

// replacementShadowThresholdHex is the fraction=1 draw threshold: 2^32 in hex
// is '100000000' (9 chars) — the TS derivation is
// `Math.min(Math.round(1 * 2 ** 32), 2 ** 32).toString(16).padStart(8, '0')`.
// Every 32-bit draw falls below it, so the workload is truthfully "the sample".
var replacementShadowThresholdHex = strconv.FormatUint(uint64(1)<<32, 16)

// dayMS is one day in milliseconds (TS DAY_MS).
const dayMS = 24 * 60 * 60 * 1000

// ReplacementEvaluationError ports the TS ReplacementEvaluationError class
// (src/routing/errors.ts; GatewayError code 'replacement_evaluation_invalid'):
// a replacement-model shadow-evaluation request was rejected before it ran.
// The type + the NewReplacementEvaluationError constructor are declared in
// routing-errors.go (the port of src/routing/errors.ts, owned there) — this
// module only constructs them, exactly like the TS runtime modules import the
// class from the sibling errors module.

/* ------------------------------------------------------------- the port seam */

// ReplacementCaseInput is ONE case run through BOTH bindings on the SHADOW
// lane (never production; recorded doubles in tests, OD-2).
type ReplacementCaseInput struct {
	CaseID     string
	Role       string
	RiskBucket schemas.RiskBucket
	// OldModel is what was asked for before the swap (old binding primary).
	OldModel string
	// NewModel is what was asked for after the swap (new binding primary).
	NewModel string
}

// ReplacementCaseResult is the paired outcome plus the V-P12-2 input-hash
// lineage of the case inputs.
type ReplacementCaseResult struct {
	OldOutcome  schemas.ShadowDecision
	NewOutcome  schemas.ShadowDecision
	InputHashes schemas.ShadowRecordedInputHashes
}

// ReplacementEvaluationPort is the injected evaluation port (TS
// ReplacementEvaluationPort): the Go signature returns an error where the TS
// port rejected, so the evaluator fails closed instead of fabricating a case.
type ReplacementEvaluationPort interface {
	// PortID names the double (recorded in the report's port_id).
	PortID() string
	// RunCase runs one case under BOTH bindings.
	RunCase(input ReplacementCaseInput) (ReplacementCaseResult, error)
}

/* ------------------------------------------------------------ evaluation API */

// EvaluateReplacementModelInput is the evaluation request (TS
// EvaluateReplacementModelInput). Clock and record-id factory are injected
// (no wall clock, no randomness); the agreement threshold and the time-box are
// caller-supplied versioned data (D11).
type EvaluateReplacementModelInput struct {
	// Role is the logical role the swap targets (ARCH §10.2, e.g. patch-writer).
	Role string
	// Repo is the repo the evaluation runs against (shadow record field).
	Repo string
	// RunID is the shadow run id (sampling draw seed; shadow record field).
	RunID string
	// ProductionVersion is the current (production) binding version —
	// models.yml versions are versioned data.
	ProductionVersion int
	// CandidateVersion is the candidate binding version.
	CandidateVersion int
	OldBinding       schemas.ReplacementModelBinding
	NewBinding       schemas.ReplacementModelBinding
	// Cases are the evaluation cases the port must run under BOTH bindings.
	Cases []schemas.ReplacementEvalCaseInput
	Port  ReplacementEvaluationPort
	// Clock returns a canonical ISO-8601 instant (injected — no real time).
	Clock func() string
	// NewRecordID is the injected ULID-shaped record-id factory (seeded per
	// case: `<runId> ⊕ NUL ⊕ <caseId>`).
	NewRecordID func(seed string) string
	// TimeBoxDays is the time-box in days for the emitted canary records.
	TimeBoxDays int
	// MinAgreementRate is the minimum agreement rate for a `pass` verdict —
	// versioned data (D11), supplied by the caller.
	MinAgreementRate float64
}

// ReplacementEvaluationResult is the (registered, schema-validated) report
// plus the canary-shaped records the policy-side lifecycle consumes.
type ReplacementEvaluationResult struct {
	Report        schemas.ReplacementEvaluationReport
	CanaryRecords []schemas.ShadowRunRecord
}

// EvaluateReplacementModel runs the shadow evaluation (TS
// evaluateReplacementModel): it validates all inputs fail-closed BEFORE any
// case runs (nothing to evaluate → typed error), executes every case through
// the injected port, builds the report, and emits the canary-shaped records
// (schema-validated at construction — a record that could not satisfy
// `docsync.shadow-run-record/v1` aborts the evaluation).
func EvaluateReplacementModel(
	input EvaluateReplacementModelInput,
) (ReplacementEvaluationResult, error) {
	problems := make([]string, 0, 8)
	for _, required := range []struct {
		label string
		value string
	}{
		{"role", input.Role},
		{"repo", input.Repo},
		{"runId", input.RunID},
	} {
		if required.value == "" {
			problems = append(problems, required.label+" must be a non-empty string")
		}
	}
	for _, version := range []struct {
		label string
		value int
	}{
		{"productionVersion", input.ProductionVersion},
		{"candidateVersion", input.CandidateVersion},
	} {
		if version.value <= 0 {
			problems = append(problems, version.label+" must be a positive integer")
		}
	}
	if input.ProductionVersion == input.CandidateVersion {
		problems = append(problems,
			"production and candidate versions must differ — a swap compares two bindings")
	}
	if input.OldBinding.Primary == input.NewBinding.Primary {
		problems = append(problems,
			fmt.Sprintf("nothing to evaluate: old and new bindings have the same primary %s",
				jsonString(input.OldBinding.Primary)))
	}
	if len(input.Cases) == 0 {
		problems = append(problems,
			"cases must be non-empty — an evaluation with no cases evaluates nothing")
	}
	seenCaseIDs := make(map[string]struct{}, len(input.Cases))
	for _, evaluationCase := range input.Cases {
		if evaluationCase.CaseID == "" {
			problems = append(problems, "caseId must be a non-empty string")
		}
		if _, duplicate := seenCaseIDs[evaluationCase.CaseID]; duplicate {
			problems = append(problems,
				fmt.Sprintf("duplicate caseId %s", jsonString(evaluationCase.CaseID)))
		}
		seenCaseIDs[evaluationCase.CaseID] = struct{}{}
	}
	if input.TimeBoxDays <= 0 {
		problems = append(problems, "timeBoxDays must be a positive integer")
	}
	if math.IsNaN(input.MinAgreementRate) ||
		input.MinAgreementRate < 0 || input.MinAgreementRate > 1 {
		problems = append(problems, "minAgreementRate must be a number in [0, 1] (versioned data — D11)")
	}
	now := input.Clock()
	if !isCanonicalISOInstant(now) {
		problems = append(problems,
			fmt.Sprintf("clock must return a canonical ISO-8601 instant (got %s)", jsonString(now)))
	}
	if len(problems) > 0 {
		return ReplacementEvaluationResult{}, NewReplacementEvaluationError(
			"evaluateReplacementModel: rejected fail-closed — " + strings.Join(problems, "; "))
	}

	createdAt := now
	createdAtTime, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		// Unreachable: isCanonicalISOInstant already accepted the instant.
		return ReplacementEvaluationResult{}, NewReplacementEvaluationError(
			"evaluateReplacementModel: rejected fail-closed — clock must return a canonical " +
				"ISO-8601 instant (got " + jsonString(now) + ")")
	}
	expiresAt := createdAtTime.Add(time.Duration(input.TimeBoxDays) * dayMS * time.Millisecond).
		UTC().Format("2006-01-02T15:04:05.000Z")

	cases := make([]schemas.ReplacementEvalCase, 0, len(input.Cases))
	canaryRecords := make([]schemas.ShadowRunRecord, 0, len(input.Cases))
	for _, evaluationCase := range input.Cases {
		run, err := input.Port.RunCase(ReplacementCaseInput{
			CaseID:     evaluationCase.CaseID,
			Role:       input.Role,
			RiskBucket: evaluationCase.RiskBucket,
			OldModel:   input.OldBinding.Primary,
			NewModel:   input.NewBinding.Primary,
		})
		if err != nil {
			return ReplacementEvaluationResult{}, NewReplacementEvaluationError(
				"evaluateReplacementModel: port " + jsonString(input.Port.PortID()) +
					" failed case " + jsonString(evaluationCase.CaseID) + ": " + err.Error())
		}
		// SHADOW_DECISION_SCHEMA.parse on both outcomes (fail closed).
		if issues := run.OldOutcome.Validate(); len(issues) > 0 {
			return ReplacementEvaluationResult{}, NewReplacementEvaluationError(
				fmt.Sprintf("evaluateReplacementModel: port %s returned an invalid old_outcome for case %s: %s",
					jsonString(input.Port.PortID()), jsonString(evaluationCase.CaseID),
					schemas.NewValidationError(issues...).Error()))
		}
		if issues := run.NewOutcome.Validate(); len(issues) > 0 {
			return ReplacementEvaluationResult{}, NewReplacementEvaluationError(
				fmt.Sprintf("evaluateReplacementModel: port %s returned an invalid new_outcome for case %s: %s",
					jsonString(input.Port.PortID()), jsonString(evaluationCase.CaseID),
					schemas.NewValidationError(issues...).Error()))
		}
		oldOutcome := run.OldOutcome
		newOutcome := run.NewOutcome
		cases = append(cases, schemas.ReplacementEvalCase{
			CaseID:      evaluationCase.CaseID,
			RiskBucket:  evaluationCase.RiskBucket,
			OldOutcome:  oldOutcome,
			NewOutcome:  newOutcome,
			Agreed:      sameOutcome(oldOutcome, newOutcome),
			InputHashes: run.InputHashes,
		})

		recordID := input.NewRecordID(input.RunID + "\x00" + evaluationCase.CaseID)
		if !docutil.IsValidULID(recordID) {
			return ReplacementEvaluationResult{}, NewReplacementEvaluationError(
				fmt.Sprintf("evaluateReplacementModel: newRecordId produced a non-ULID id for case %s: %s",
					jsonString(evaluationCase.CaseID), jsonString(recordID)))
		}
		drawSum := sha256.Sum256([]byte(input.RunID + "\x00" + evaluationCase.CaseID))
		record := schemas.ShadowRunRecord{
			Schema:     shadowRunRecordWireSchemaID,
			RecordID:   recordID,
			RunID:      input.RunID,
			PrID:       evaluationCase.CaseID,
			Repo:       input.Repo,
			ShadowOnly: true,
			Sampled:    true,
			// A replacement-model evaluation runs on an opted-in canary lane
			// by construction (R2: the swap rides the canary machinery).
			Eligible: true,
			Sampling: schemas.ShadowSampling{
				Sampler:      schemas.ShadowSamplerSHA256HexThreshold,
				Fraction:     1,
				DrawHex:      hex.EncodeToString(drawSum[:])[:8],
				ThresholdHex: replacementShadowThresholdHex,
			},
			Policy: schemas.ShadowRecordedPolicy{
				ProductionVersion: input.ProductionVersion,
				CandidateVersion:  input.CandidateVersion,
			},
			Decision:    &newOutcome,
			InputHashes: &run.InputHashes,
			CreatedAt:   createdAt,
			ExpiresAt:   expiresAt,
		}
		if issues := record.Validate(); len(issues) > 0 {
			return ReplacementEvaluationResult{}, NewReplacementEvaluationError(
				fmt.Sprintf("evaluateReplacementModel: emitted canary record for case %s could not satisfy %s: %s",
					jsonString(evaluationCase.CaseID), shadowRunRecordWireSchemaID,
					schemas.NewValidationError(issues...).Error()))
		}
		canaryRecords = append(canaryRecords, record)
	}

	total := len(cases)
	agreed := 0
	for _, entry := range cases {
		if entry.Agreed {
			agreed++
		}
	}
	rate := 0.0
	if total > 0 {
		rate = float64(agreed) / float64(total)
	}
	verdict := "fail"
	if rate >= input.MinAgreementRate {
		verdict = "pass"
	}
	// The report is validated AT CONSTRUCTION against the registered
	// `docsync.routing-replacement-report/v1` schema (P12-T20 promotion) —
	// the same fail-closed posture as the canary records it rides with.
	report := schemas.ReplacementEvaluationReport{
		Schema: replacementEvaluationReportWireSchemaID,
		Kind:   replacementEvaluationReportWireKind,
		Role:   input.Role,
		Repo:   input.Repo,
		RunID:  input.RunID,
		Bindings: schemas.ReplacementEvalBindings{
			Old: schemas.ReplacementEvaluationBinding{
				Primary:   input.OldBinding.Primary,
				Fallbacks: orEmptyFallbacks(input.OldBinding.Fallbacks),
			},
			New: schemas.ReplacementEvaluationBinding{
				Primary:   input.NewBinding.Primary,
				Fallbacks: orEmptyFallbacks(input.NewBinding.Fallbacks),
			},
		},
		Policy: schemas.ReplacementEvalPolicy{
			ProductionVersion: input.ProductionVersion,
			CandidateVersion:  input.CandidateVersion,
		},
		Cases: cases,
		Agreement: schemas.ReplacementEvalAgreement{
			Total:     total,
			Agreed:    agreed,
			Disagreed: total - agreed,
			Rate:      rate,
		},
		MinAgreementRate: input.MinAgreementRate,
		Verdict:          verdict,
		PortID:           input.Port.PortID(),
		CreatedAt:        createdAt,
		ExpiresAt:        expiresAt,
	}
	if issues := report.Validate(); len(issues) > 0 {
		return ReplacementEvaluationResult{}, NewReplacementEvaluationError(
			"evaluateReplacementModel: the emitted report could not satisfy " +
				replacementEvaluationReportWireSchemaID + ": " +
				schemas.NewValidationError(issues...).Error())
	}
	return ReplacementEvaluationResult{Report: report, CanaryRecords: canaryRecords}, nil
}

// orEmptyFallbacks mirrors the TS `fallbacks ?? []`.
func orEmptyFallbacks(fallbacks []string) []string {
	if fallbacks == nil {
		return []string{}
	}
	return fallbacks
}

// sameOutcome mirrors the TS sameOutcome: same action, same abstained flag,
// and the same abstain reason (nil-normalized — TS `a.abstain_reason ?? undefined`).
func sameOutcome(a, b schemas.ShadowDecision) bool {
	if a.Action != b.Action || a.Abstained != b.Abstained {
		return false
	}
	return abstainReasonOf(a) == abstainReasonOf(b)
}

func abstainReasonOf(decision schemas.ShadowDecision) string {
	if decision.AbstainReason == nil {
		return ""
	}
	return string(*decision.AbstainReason)
}

// isCanonicalISOInstant mirrors the TS clock guard: the instant must parse AND
// re-render identically to the input (TS `now !== new Date(nowMs).toISOString()`
// rejection), so only canonical `YYYY-MM-DDTHH:mm:ss.sssZ` instants pass.
func isCanonicalISOInstant(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return false
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z") == value
}
