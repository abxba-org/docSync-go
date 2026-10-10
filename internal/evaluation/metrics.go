// Ported from core/src/evaluation/metrics.ts (P10-T10..T13; ARCH §9; CTX
// §35/§36; V-P10-7/8): the six-metric evaluation computation.
//
// Computes the `docsync.evaluation-report/v1` metrics block from per-case
// evaluation rows — the honest input shape the evaluation loop can actually
// carry (each row records WHAT the policy did and what the ground truth said,
// per decision):
//
//   - `outcome`: 'updated' (a patch was produced/shipped), 'abstained' (the
//     policy chose not to update), 'missed' (ground truth required an update
//     and none was produced).
//   - `wasCorrectUpdate` / `wasCorrectAbstain`: ground-truth verdicts from the
//     hygiene labels — REQUIRED on their respective outcomes (fail closed: a
//     row without its verdict cannot be scored and refuses the whole run, it
//     is never silently guessed).
//   - `anchorMatched`: patch-correctness SOURCE-ANCHOR evidence (ARCH §5.2 —
//     the patch claimed an anchor; did the shipped patch actually match it?).
//     REQUIRED on 'updated' rows.
//   - Minimality is MEASURED, not eyeballed (F10-7): each 'updated' row
//     carries `shippedHunks`, `triggeringDiffLines` and `editDistance` from
//     the diff/patch records; the report carries the mean of the per-case
//     edit-distance RATIO (edit distance ÷ triggering diff lines — minimality
//     relative to the triggering change) and the mean shipped hunk count.
//   - `verifierCausedReject` (P10-T24/R5): rejects caused by the verifier (not
//     the doc) are DISCOUNTED from the metrics — they are verifier noise, not
//     policy evidence — and reported instead as the verifier-disagreement
//     metric (rate + affected case ids), its own tracked number.
//
// The abstention-health monitor (F10-8/R11) is the 2-D view (abstain_rate ×
// override_rate) per policy version; its alert thresholds are versioned data
// constants (D11-adjacent — thresholds are data, never silent hard-coding).
// `computed_at` is an injected instant supplied by the caller — never
// generated here (no `new Date()` anywhere). Fail closed: the assembled report
// is re-validated against EVALUATION_REPORT_SCHEMA before it is returned
// (out-of-range is refused, not clamped).
package evaluation

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CaseOutcome is what the policy DID on one evaluation case (closed
// three-value union).
type CaseOutcome string

// The case outcomes in canonical order.
const (
	CaseOutcomeUpdated   CaseOutcome = "updated"
	CaseOutcomeAbstained CaseOutcome = "abstained"
	CaseOutcomeMissed    CaseOutcome = "missed"
)

// CaseEvaluation is one per-case evaluation row — the honest scoring input.
// Optional fields are REQUIRED on the outcome they annotate; ComputeMetrics
// refuses rows that lack their verdicts (fail closed, never guessed).
type CaseEvaluation struct {
	// CaseID is the stable case id (echoed in the verifier-disagreement
	// affected list).
	CaseID string
	// Outcome is what the policy did / ground-truth verdict lane.
	Outcome CaseOutcome
	// WasCorrectUpdate is REQUIRED on 'updated': the shipped patch was
	// correct per ground truth.
	WasCorrectUpdate *bool
	// WasCorrectAbstain is REQUIRED on 'abstained': abstaining was the
	// correct decision.
	WasCorrectAbstain *bool
	// ShippedHunks is REQUIRED on 'updated': hunk count of the SHIPPED
	// patch (measured).
	ShippedHunks *float64
	// TriggeringDiffLines is REQUIRED on 'updated': line count of the
	// triggering code diff (measured).
	TriggeringDiffLines *float64
	// EditDistance is REQUIRED on 'updated': edit distance of the shipped
	// patch (measured).
	EditDistance *float64
	// AnchorMatched is REQUIRED on 'updated': the shipped patch matched its
	// source anchor.
	AnchorMatched *bool
	// VerifierCausedReject: the reject/abstain was caused by the verifier,
	// not the documentation.
	VerifierCausedReject *bool
}

// OverrideCounts carries the override aggregates from the feedback layer
// (F10-4/R11 2-D monitor input).
type OverrideCounts struct {
	// OverrideCount counts decisions a human overrode (any direction).
	OverrideCount int
	// DecisionCount is the total decisions in the window (the override-rate
	// denominator).
	DecisionCount int
}

// ComputeMetricsInput pins the policy version, split, rows, override
// aggregates, optional baseline and the injected instant.
type ComputeMetricsInput struct {
	// PolicyVersion is the pinned policy version under evaluation (report
	// `policy_version`).
	PolicyVersion int
	// BaselineVersion is the optional baseline policy version of the lineage
	// (first report: none).
	BaselineVersion *int
	// SplitID is the split the cases belong to (report `split_id`).
	SplitID string
	// Cases are the per-case evaluation rows.
	Cases []CaseEvaluation
	// Overrides carries the override aggregates for the abstain-health 2-D
	// monitor.
	Overrides OverrideCounts
	// ComputedAt is the injected ISO-UTC-Z instant stamped on the report —
	// never generated.
	ComputedAt string
}

// VerifierDisagreement is the verifier-vs-judge disagreement (P10-T24/R5) —
// its own tracked metric.
type VerifierDisagreement struct {
	// Rate is the discounted verifier-caused rows ÷ all input rows (0..1).
	Rate float64
	// AffectedCaseIDs lists the sorted case ids discounted because of
	// verifier-caused rejects.
	AffectedCaseIDs []string
}

// ComputeMetricsResult carries the schema-validated report plus the
// verifier-disagreement metric.
type ComputeMetricsResult struct {
	Report               schemas.EvaluationReport
	VerifierDisagreement VerifierDisagreement
}

// AbstainHealthAlert is the alert union produced by EvaluateAbstainHealth.
type AbstainHealthAlert string

// The abstain-health alerts in canonical order.
const (
	AbstainHealthPolicyGap      AbstainHealthAlert = "policy_gap"
	AbstainHealthOverconfidence AbstainHealthAlert = "overconfidence"
)

// AbstainHealthThresholds are the abstain-health alert thresholds (versioned
// data, D11-adjacent — F10-8/R11): abstain_rate >
// PolicyGapAbstainRate ⇒ `policy_gap` alert; abstain_rate <
// OverconfidenceAbstainRate AND override_rate >
// OverconfidenceOverrideRate ⇒ `overconfidence` alert. Exported so tests,
// monitors and the promotion pipeline read the same numbers (never silently
// hard-coded).
type AbstainHealthThresholds struct {
	PolicyGapAbstainRate       float64
	OverconfidenceAbstainRate  float64
	OverconfidenceOverrideRate float64
}

// ABSTAIN_HEALTH_THRESHOLDS is the exported threshold data.
var ABSTAIN_HEALTH_THRESHOLDS = AbstainHealthThresholds{
	PolicyGapAbstainRate:       0.5,
	OverconfidenceAbstainRate:  0.05,
	OverconfidenceOverrideRate: 0.3,
}

// EvaluateAbstainHealth classifies one (abstain_rate × override_rate)
// observation (F10-8/R11). `policy_gap` wins when both fire — an
// under-abstaining policy with heavy overrides is overconfidence FIRST (the
// overrides are the louder signal), and a policy cannot be both under- and
// over-abstaining at once. The second return value reports whether an alert
// fired at all (no third "none" state exists).
func EvaluateAbstainHealth(abstainRate, overrideRate float64) (AbstainHealthAlert, bool) {
	if abstainRate > ABSTAIN_HEALTH_THRESHOLDS.PolicyGapAbstainRate {
		return AbstainHealthPolicyGap, true
	}
	if abstainRate < ABSTAIN_HEALTH_THRESHOLDS.OverconfidenceAbstainRate &&
		overrideRate > ABSTAIN_HEALTH_THRESHOLDS.OverconfidenceOverrideRate {
		return AbstainHealthOverconfidence, true
	}
	return "", false
}

// jsNumber renders a float the way JavaScript's Number#toString / template
// interpolation does, so preserved error texts read identically (1.5 → "1.5",
// 1 → "1", NaN → "NaN").
func jsNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func requireInt(name string, value, min int) error {
	if value < min {
		return fmt.Errorf("computeMetrics: %s must be an integer >= %d, got %d", name, min, value)
	}
	return nil
}

func requireNumber(name string, value, min float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < min {
		return fmt.Errorf("computeMetrics: %s must be a finite number >= %s, got %s",
			name, jsNumber(min), jsNumber(value))
	}
	return nil
}

// mean returns 0 for an empty list.
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// share returns the share of hits in total; vacuously perfect when the
// denominator is 0.
func share(hits, total int) float64 {
	if total == 0 {
		return 1
	}
	return float64(hits) / float64(total)
}

// numOrNaN reads an optional measured number; an absent value reads as NaN so
// the requireNumber check refuses it exactly as the TS `?? Number.NaN` did.
func numOrNaN(v *float64) float64 {
	if v == nil {
		return math.NaN()
	}
	return *v
}

// ComputeMetrics computes the six-metric evaluation report (V-P10-7) plus the
// abstain-health 2-D monitor (V-P10-8) and the verifier-disagreement metric
// (P10-T24/R5) from per-case evaluation rows. Verifier-caused rows are
// discounted from every metric and reported separately. The assembled report
// is validated against EVALUATION_REPORT_SCHEMA before return — fail closed.
func ComputeMetrics(input ComputeMetricsInput) (ComputeMetricsResult, error) {
	if err := requireInt("policyVersion", input.PolicyVersion, 1); err != nil {
		return ComputeMetricsResult{}, err
	}
	if input.BaselineVersion != nil {
		if err := requireInt("baselineVersion", *input.BaselineVersion, 1); err != nil {
			return ComputeMetricsResult{}, err
		}
	}
	if input.SplitID == "" {
		return ComputeMetricsResult{}, fmt.Errorf("computeMetrics: splitId must be a non-empty string")
	}
	if err := requireInt("overrides.decisionCount", input.Overrides.DecisionCount, 1); err != nil {
		return ComputeMetricsResult{}, err
	}
	if err := requireInt("overrides.overrideCount", input.Overrides.OverrideCount, 0); err != nil {
		return ComputeMetricsResult{}, err
	}
	if input.Overrides.OverrideCount > input.Overrides.DecisionCount {
		return ComputeMetricsResult{}, fmt.Errorf(
			"computeMetrics: overrideCount (%d) cannot exceed decisionCount (%d)",
			input.Overrides.OverrideCount, input.Overrides.DecisionCount)
	}
	if len(input.Cases) == 0 {
		return ComputeMetricsResult{}, fmt.Errorf("computeMetrics: cases must be non-empty (sample_size >= 1)")
	}

	// P10-T24/R5: verifier-caused rejects are verifier noise, not policy
	// evidence — discounted from every metric, tracked as their own number.
	affectedCaseIDs := make([]string, 0, len(input.Cases))
	for _, row := range input.Cases {
		if row.VerifierCausedReject != nil && *row.VerifierCausedReject {
			affectedCaseIDs = append(affectedCaseIDs, row.CaseID)
		}
	}
	sort.SliceStable(affectedCaseIDs, func(i, j int) bool { return affectedCaseIDs[i] < affectedCaseIDs[j] })
	verifierCaused := map[string]bool{}
	for _, id := range affectedCaseIDs {
		verifierCaused[id] = true
	}
	scored := make([]CaseEvaluation, 0, len(input.Cases))
	for _, row := range input.Cases {
		if !verifierCaused[row.CaseID] {
			scored = append(scored, row)
		}
	}
	if len(scored) == 0 {
		return ComputeMetricsResult{}, fmt.Errorf(
			"computeMetrics: every case was discounted as verifier-caused — no policy evidence remains")
	}

	updated := 0
	correctUpdates := 0
	anchorMatchedUpdates := 0
	missed := 0
	abstained := 0
	correctAbstains := 0
	editDistanceRatios := []float64{}
	hunkCounts := []float64{}

	for _, row := range scored {
		switch row.Outcome {
		case CaseOutcomeUpdated:
			if row.WasCorrectUpdate == nil {
				return ComputeMetricsResult{}, fmt.Errorf(
					"computeMetrics: case %s is 'updated' but carries no wasCorrectUpdate verdict (fail closed: never guessed)",
					row.CaseID)
			}
			if row.AnchorMatched == nil {
				return ComputeMetricsResult{}, fmt.Errorf(
					"computeMetrics: case %s is 'updated' but carries no anchorMatched source-anchor evidence (fail closed: never guessed)",
					row.CaseID)
			}
			shippedHunks := numOrNaN(row.ShippedHunks)
			triggeringDiffLines := numOrNaN(row.TriggeringDiffLines)
			editDistance := numOrNaN(row.EditDistance)
			if err := requireNumber(fmt.Sprintf("case %s shippedHunks", row.CaseID), shippedHunks, 0); err != nil {
				return ComputeMetricsResult{}, err
			}
			if err := requireNumber(fmt.Sprintf("case %s triggeringDiffLines", row.CaseID), triggeringDiffLines, 1); err != nil {
				return ComputeMetricsResult{}, err
			}
			if err := requireNumber(fmt.Sprintf("case %s editDistance", row.CaseID), editDistance, 0); err != nil {
				return ComputeMetricsResult{}, err
			}
			updated++
			if *row.WasCorrectUpdate {
				correctUpdates++
			}
			if *row.AnchorMatched {
				anchorMatchedUpdates++
			}
			editDistanceRatios = append(editDistanceRatios, editDistance/triggeringDiffLines)
			hunkCounts = append(hunkCounts, shippedHunks)
		case CaseOutcomeAbstained:
			if row.WasCorrectAbstain == nil {
				return ComputeMetricsResult{}, fmt.Errorf(
					"computeMetrics: case %s is 'abstained' but carries no wasCorrectAbstain verdict (fail closed: never guessed)",
					row.CaseID)
			}
			abstained++
			if *row.WasCorrectAbstain {
				correctAbstains++
			}
		case CaseOutcomeMissed:
			missed++
		}
	}

	scoredTotal := len(scored)
	report := schemas.EvaluationReport{
		Schema:        "docsync.evaluation-report/v1",
		PolicyVersion: input.PolicyVersion,
		SplitID:       input.SplitID,
		SampleSize:    scoredTotal,
		Metrics: schemas.EvalReportMetrics{
			// Detection recall: share of ground-truth-needed updates produced.
			DetectionRecall: share(updated, updated+missed),
			// Precision: share of produced updates that were correct per label.
			Precision: share(correctUpdates, updated),
			// Patch correctness judged against SOURCE ANCHORS (ARCH §5.2).
			PatchCorrectness: share(anchorMatchedUpdates, updated),
			// Minimality MEASURED: mean edit-distance ratio vs the triggering
			// diff and mean shipped hunk count — numbers from diff records,
			// not prose.
			Minimality: schemas.EvalMinimality{
				MeanEditDistance: mean(editDistanceRatios),
				MeanHunkCount:    mean(hunkCounts),
			},
			// False updates (incorrect updates) as a share of ALL scored cases.
			FalseUpdateRate: float64(updated-correctUpdates) / float64(scoredTotal),
			// Share of abstains that were correct to abstain.
			AbstentionQuality: share(correctAbstains, abstained),
		},
		AbstainHealth: schemas.EvalReportAbstainHealth{
			AbstainRate:  float64(abstained) / float64(scoredTotal),
			OverrideRate: float64(input.Overrides.OverrideCount) / float64(input.Overrides.DecisionCount),
		},
		VerifierDisagreementRate: float64(len(affectedCaseIDs)) / float64(len(input.Cases)),
		ComputedAt:               input.ComputedAt,
	}
	if input.BaselineVersion != nil {
		report.BaselineVersion = input.BaselineVersion
	}

	// Fail closed: the assembled report must round-trip the v1 schema — an
	// out-of-range metric is refused here, not clamped and not shipped.
	if issues := report.Validate(); len(issues) > 0 {
		return ComputeMetricsResult{}, schemas.NewValidationError(issues...)
	}
	return ComputeMetricsResult{
		Report: report,
		VerifierDisagreement: VerifierDisagreement{
			Rate:            report.VerifierDisagreementRate,
			AffectedCaseIDs: affectedCaseIDs,
		},
	}, nil
}
