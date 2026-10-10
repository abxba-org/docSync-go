// Ported from packages/policy/src/evaluation/gates.ts.

package policy

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// MinComparisonSizeDefault is the OD-R24 adopted default: minimum 100 labeled
// disagreements per comparison (MIN_COMPARISON_SIZE_DEFAULT).
const MinComparisonSizeDefault = 100

// SignificanceAlphaDefault is the OD-R24 adopted default: two-proportion test
// at α=0.05, one-sided, candidate better (SIGNIFICANCE_ALPHA_DEFAULT).
const SignificanceAlphaDefault = 0.05

// ZCritical005 is the one-sided z critical value for α = 0.05
// (Z_CRITICAL_0_05).
const ZCritical005 = 1.6448536269514722

// MandatoryGateNames lists the gate names every promotion checklist must carry
// when rows are recorded (MANDATORY_GATE_NAMES). The conditional
// `binding_calibration` gate (P12-T09) is deliberately absent: absent evidence
// means it is not evaluated at all.
var MandatoryGateNames = []string{
	"aggregate_correctness_win",
	"no_regression_false_update",
	"no_regression_abstention",
	"unsafe_zero",
	"min_sample_size",
	"significance_floor",
}

// PromotionGateInputs is the per-call gate evidence
// (PromotionGateInputs).
type PromotionGateInputs struct {
	Baseline                   schemas.EvaluationReport `json:"baseline"`
	Candidate                  schemas.EvaluationReport `json:"candidate"`
	CandidateUnsafeAutoUpdates int                      `json:"candidateUnsafeAutoUpdates"`
	// CandidateWins and CandidateTotal are the labeled-disagreement win
	// counts for the significance floor.
	CandidateWins         int  `json:"candidateWins"`
	CandidateTotal        int  `json:"candidateTotal"`
	BaselineWins          int  `json:"baselineWins"`
	BaselineTotal         int  `json:"baselineTotal"`
	RepoMinComparisonSize *int `json:"repoMinComparisonSize,omitempty"`
	// Buckets are the per-decision-bucket comparisons (P12-T06; R3
	// per-bucket metrics) — each bucket gets its own no-regression gate on
	// false-update rate and abstention quality.
	Buckets []PromotionBucketComparison `json:"buckets,omitempty"`
	// BindingCalibration is the binding-calibration evidence (P12-T09; D3)
	// for the changed model bindings. OPTIONAL: a nil slice means the
	// `binding_calibration` gate is NOT evaluated (Phase-10/W2 candidates
	// carry no binding context); a non-nil but empty slice fails closed.
	BindingCalibration []CalibrationResult `json:"bindingCalibration,omitempty"`
}

// GateRow is one checklist row (GateRow).
type GateRow struct {
	Gate   string `json:"gate"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// PromotionGateResult is the gate checklist verdict (PromotionGateResult).
type PromotionGateResult struct {
	Passed      bool      `json:"passed"`
	FailedGates []string  `json:"failedGates"`
	Rows        []GateRow `json:"rows"`
}

// PromotionGateLibraryOptions configures the gate library
// (PromotionGateLibraryOptions).
type PromotionGateLibraryOptions struct {
	MinComparisonSize *int     `json:"minComparisonSize,omitempty"`
	Alpha             *float64 `json:"alpha,omitempty"`
}

// PromotionBucketMetrics is one side of a per-bucket no-regression comparison
// (PromotionBucketMetrics).
type PromotionBucketMetrics struct {
	FalseUpdateRate   float64 `json:"false_update_rate"`
	AbstentionQuality float64 `json:"abstention_quality"`
}

// PromotionBucketComparison is one decision-bucket (agreement-matrix cell)
// comparison, e.g. `both-ignore` (PromotionBucketComparison).
type PromotionBucketComparison struct {
	// Bucket is the bucket name — the (candidate, production) decision pair,
	// human-readable.
	Bucket    string                 `json:"bucket"`
	Baseline  PromotionBucketMetrics `json:"baseline"`
	Candidate PromotionBucketMetrics `json:"candidate"`
}

// PromotionGateLibrary is the reusable gate-check library
// (PromotionGateLibrary).
type PromotionGateLibrary struct {
	MinComparisonSize int     `json:"minComparisonSize"`
	Alpha             float64 `json:"alpha"`

	zCritical float64
}

// CompositeScore is the composite fallback when objective scores are absent
// (CTX §36 weights).
func CompositeScore(report schemas.EvaluationReport) float64 {
	return 0.4*report.Metrics.Precision +
		0.3*report.Metrics.DetectionRecall +
		0.2*report.Metrics.PatchCorrectness +
		0.1*(1-report.Metrics.FalseUpdateRate)
}

// TwoProportionZ is the two-proportion one-sided z statistic (candidate
// better than baseline). A zero denominator returns -Inf (and +Inf when the
// candidate proportion already exceeds the baseline's).
func TwoProportionZ(candidateWins, candidateTotal, baselineWins, baselineTotal int) float64 {
	if candidateTotal <= 0 || baselineTotal <= 0 {
		return math.Inf(-1)
	}
	pC := float64(candidateWins) / float64(candidateTotal)
	pB := float64(baselineWins) / float64(baselineTotal)
	pooled := float64(candidateWins+baselineWins) / float64(candidateTotal+baselineTotal)
	se := math.Sqrt(pooled * (1 - pooled) * (1/float64(candidateTotal) + 1/float64(baselineTotal)))
	if se == 0 {
		if pC > pB {
			return math.Inf(1)
		}
		return math.Inf(-1)
	}
	return (pC - pB) / se
}

// NewPromotionGateLibrary builds the gate library. The TS factory threw on
// malformed options; Go returns the coded error (message preserved verbatim).
// The floor clamps UPWARD-ONLY (OD-R24): it can never be disabled or lowered.
func NewPromotionGateLibrary(options PromotionGateLibraryOptions) (*PromotionGateLibrary, error) {
	requested := 0
	if options.MinComparisonSize != nil {
		requested = *options.MinComparisonSize
	}
	if requested < 0 {
		return nil, docutil.New(docutil.CodeValidation,
			"promotion gate: minComparisonSize must be a non-negative integer")
	}
	alpha := SignificanceAlphaDefault
	if options.Alpha != nil {
		alpha = *options.Alpha
	}
	if !(alpha > 0 && alpha < 1) {
		return nil, docutil.New(docutil.CodeValidation,
			"promotion gate: alpha must be in (0, 1)")
	}
	// UPWARD-ONLY clamp: the floor cannot be disabled or lowered (OD-R24).
	minComparisonSize := requested
	if MinComparisonSizeDefault > minComparisonSize {
		minComparisonSize = MinComparisonSizeDefault
	}
	zCritical := ZCritical005
	if alpha != SignificanceAlphaDefault {
		var err error
		zCritical, err = inverseNormalUpperTail(alpha)
		if err != nil {
			return nil, err
		}
	}
	return &PromotionGateLibrary{
		MinComparisonSize: minComparisonSize,
		Alpha:             alpha,
		zCritical:         zCritical,
	}, nil
}

// CheckGates evaluates the promotion checklist (checkGates). Every failing
// gate is NAMED (INV-9 / V-P10-13). Fail-closed: malformed evidence returns a
// coded error instead of a checklist.
func (g *PromotionGateLibrary) CheckGates(inputs PromotionGateInputs) (PromotionGateResult, error) {
	baseline, err := parseGateReport(inputs.Baseline, "baseline")
	if err != nil {
		return PromotionGateResult{}, err
	}
	candidate, err := parseGateReport(inputs.Candidate, "candidate")
	if err != nil {
		return PromotionGateResult{}, err
	}
	if inputs.CandidateUnsafeAutoUpdates < 0 {
		return PromotionGateResult{}, docutil.New(docutil.CodeValidation,
			"promotion gate: candidateUnsafeAutoUpdates must be a non-negative integer")
	}
	// Per-call repository floor (P12-T10/T20 fix): the caller may RAISE the
	// comparison floor for this promotion; the clamp stays UPWARD-ONLY so a
	// repo can never lower it below the library's own floor (OD-R24).
	repoFloor := 0
	if inputs.RepoMinComparisonSize != nil {
		repoFloor = *inputs.RepoMinComparisonSize
		if repoFloor < 0 {
			return PromotionGateResult{}, docutil.New(docutil.CodeValidation,
				"promotion gate: repoMinComparisonSize must be a non-negative integer")
		}
	}
	effectiveMin := g.MinComparisonSize
	if repoFloor > effectiveMin {
		effectiveMin = repoFloor
	}
	rows := make([]GateRow, 0, len(MandatoryGateNames)+len(inputs.Buckets))

	row := func(gate string, passed bool, detail string) GateRow {
		return GateRow{Gate: gate, Passed: passed, Detail: detail}
	}

	bScore := compositeScoreOf(baseline)
	cScore := compositeScoreOf(candidate)
	rows = append(rows, row(
		"aggregate_correctness_win",
		cScore > bScore,
		fmt.Sprintf("composite candidate %s vs baseline %s — strict win required",
			jsToFixed(cScore, 4), jsToFixed(bScore, 4)),
	))
	rows = append(rows, row(
		"no_regression_false_update",
		candidate.Metrics.FalseUpdateRate <= baseline.Metrics.FalseUpdateRate,
		fmt.Sprintf("false_update_rate candidate %s vs baseline %s",
			jsString(candidate.Metrics.FalseUpdateRate), jsString(baseline.Metrics.FalseUpdateRate)),
	))
	rows = append(rows, row(
		"no_regression_abstention",
		candidate.Metrics.AbstentionQuality >= baseline.Metrics.AbstentionQuality,
		fmt.Sprintf("abstention_quality candidate %s vs baseline %s",
			jsString(candidate.Metrics.AbstentionQuality), jsString(baseline.Metrics.AbstentionQuality)),
	))
	rows = append(rows, row(
		"unsafe_zero",
		inputs.CandidateUnsafeAutoUpdates == 0,
		fmt.Sprintf("unsafe_auto_updates = %d — must be exactly 0 (ARCH §8.4)",
			inputs.CandidateUnsafeAutoUpdates),
	))
	rows = append(rows, row(
		"min_sample_size",
		candidate.SampleSize >= effectiveMin,
		fmt.Sprintf("sample_size %d vs floor %d (upward-clamped, OD-R24)",
			candidate.SampleSize, effectiveMin),
	))
	totalsOK := inputs.CandidateTotal >= effectiveMin && inputs.BaselineTotal >= effectiveMin
	z := TwoProportionZ(
		inputs.CandidateWins,
		inputs.CandidateTotal,
		inputs.BaselineWins,
		inputs.BaselineTotal,
	)
	var significanceDetail string
	if totalsOK {
		significanceDetail = fmt.Sprintf(
			"one-sided two-proportion z = %s vs critical %s at α=%s",
			jsZ(z), jsString(g.zCritical), jsString(g.Alpha))
	} else {
		significanceDetail = fmt.Sprintf(
			"comparison totals below the floor (candidate %d, baseline %d vs floor %d) — significance is not assessable (abstain, don't guess)",
			inputs.CandidateTotal, inputs.BaselineTotal, effectiveMin)
	}
	rows = append(rows, row(
		"significance_floor",
		totalsOK && z > g.zCritical,
		significanceDetail,
	))

	failedGates := make([]string, 0, len(rows))
	for _, r := range rows {
		if !r.Passed {
			failedGates = append(failedGates, r.Gate)
		}
	}

	// P12-T06 per-bucket gates: validated FIRST (fail closed on malformed
	// bucket data), then one no-regression row per bucket. A bucket-level
	// regression rejects even when the aggregate wins (R3).
	seenBuckets := make(map[string]struct{}, len(inputs.Buckets))
	for _, bucket := range inputs.Buckets {
		if strings.TrimSpace(bucket.Bucket) == "" {
			return PromotionGateResult{}, docutil.New(docutil.CodeValidation,
				"promotion gate: bucket name must be a non-empty string")
		}
		if _, dup := seenBuckets[bucket.Bucket]; dup {
			return PromotionGateResult{}, docutil.Newf(docutil.CodeValidation,
				"promotion gate: duplicate bucket %q — each bucket is compared once", bucket.Bucket)
		}
		seenBuckets[bucket.Bucket] = struct{}{}
		for _, side := range []struct {
			name    string
			metrics PromotionBucketMetrics
		}{
			{"baseline", bucket.Baseline},
			{"candidate", bucket.Candidate},
		} {
			for _, metric := range []struct {
				name  string
				value float64
			}{
				{"false_update_rate", side.metrics.FalseUpdateRate},
				{"abstention_quality", side.metrics.AbstentionQuality},
			} {
				if math.IsNaN(metric.value) || metric.value < 0 || metric.value > 1 {
					return PromotionGateResult{}, docutil.Newf(docutil.CodeValidation,
						"promotion gate: bucket %q %s.%s must be a number in [0, 1]",
						bucket.Bucket, side.name, metric.name)
				}
			}
		}
		fuOK := bucket.Candidate.FalseUpdateRate <= bucket.Baseline.FalseUpdateRate
		abOK := bucket.Candidate.AbstentionQuality >= bucket.Baseline.AbstentionQuality
		gate := "bucket_no_regression:" + bucket.Bucket
		passed := fuOK && abOK
		rows = append(rows, row(gate, passed, fmt.Sprintf(
			"bucket %q per-bucket no-regression (ARCH §8.6; R3): "+
				"false_update_rate %s vs baseline %s (must not rise), "+
				"abstention_quality %s vs baseline %s (must not fall)",
			bucket.Bucket,
			jsString(bucket.Candidate.FalseUpdateRate), jsString(bucket.Baseline.FalseUpdateRate),
			jsString(bucket.Candidate.AbstentionQuality), jsString(bucket.Baseline.AbstentionQuality),
		)))
		if !passed {
			failedGates = append(failedGates, gate)
		}
	}

	// P12-T09 (V-P12-11): binding_calibration is CONDITIONAL — rows are
	// appended ONLY when the candidate carries binding-calibration evidence
	// (absent ⇒ not evaluated, NOT in MANDATORY_GATE_NAMES; present-but-empty
	// fails closed), and binding-CHANGE enforcement rides the composition
	// layer, which always supplies evidence when models.yml changed.
	if inputs.BindingCalibration != nil {
		for _, gateRow := range BindingCalibrationGateRows(inputs.BindingCalibration) {
			rows = append(rows, gateRow)
			if !gateRow.Passed {
				failedGates = append(failedGates, gateRow.Gate)
			}
		}
	}

	return PromotionGateResult{
		Passed:      len(failedGates) == 0,
		FailedGates: failedGates,
		Rows:        rows,
	}, nil
}

// parseGateReport reproduces EVALUATION_REPORT_SCHEMA.parse on the gate
// inputs: the report is validated (strict) and the parsed copy is what the
// rows read, so a malformed report fails closed before any row is built.
func parseGateReport(report schemas.EvaluationReport, side string) (schemas.EvaluationReport, error) {
	if issues := report.Validate(); len(issues) > 0 {
		parts := make([]string, 0, len(issues))
		for _, issue := range issues {
			parts = append(parts, fmt.Sprintf("%s: %s", strings.Join(issue.Path, "."), issue.Message))
		}
		return report, docutil.Newf(docutil.CodeValidation,
			"promotion gate: invalid %s evaluation report (%s)", side, strings.Join(parts, "; "))
	}
	return report, nil
}

// compositeScoreOf mirrors `baseline.objective_score ?? compositeScore(...)`
// (CTX §36 fallback when the objective score is absent).
func compositeScoreOf(report schemas.EvaluationReport) float64 {
	if report.ObjectiveScore != nil {
		return *report.ObjectiveScore
	}
	return CompositeScore(report)
}

// inverseNormalUpperTail is the acklam-free inverse normal (upper tail) via a
// closed-form table — adequate for gate constants; an untabulated α fails
// closed rather than being approximated silently.
func inverseNormalUpperTail(alpha float64) (float64, error) {
	// Closed-form constants for the common α values; else fail closed.
	table := map[string]float64{
		"0.05":  ZCritical005,
		"0.025": 1.959963984540054,
		"0.01":  2.3263478740408408,
	}
	key := jsString(alpha)
	hit, ok := table[key]
	if !ok {
		return 0, docutil.Newf(docutil.CodeValidation,
			"promotion gate: no z critical value tabulated for alpha %s (use 0.05/0.025/0.01 or extend the table — never approximate silently)",
			key)
	}
	return hit, nil
}

// jsString renders a float the way JavaScript's String(number) does for the
// values gate details carry (shortest round-trip decimal, `NaN` for the
// not-a-number case). Infinities never reach the gate detail numbers; the
// significance statistic formats them through jsZ.
func jsString(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// jsToFixed renders a float the way JavaScript's number.toFixed(digits) does
// (fixed notation, exactly `digits` fraction digits).
func jsToFixed(v float64, digits int) string {
	return strconv.FormatFloat(v, 'f', digits, 64)
}

// jsZ renders the significance statistic: toFixed(4) when finite, the
// JavaScript String() spelling of ±Infinity otherwise.
func jsZ(z float64) string {
	if math.IsInf(z, 1) {
		return "Infinity"
	}
	if math.IsInf(z, -1) {
		return "-Infinity"
	}
	return jsToFixed(z, 4)
}
