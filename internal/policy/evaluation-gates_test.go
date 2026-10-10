// Ported from packages/policy/test/evaluation-gates.test.ts
// (evaluation/gates.ts + evaluation/harness.ts assertions).
package policy_test

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// gateReportFixture builds the TS test's evaluation-report fixture: the
// six-metric block
// with optional metric overrides, the abstain-health block, and an optional
// objective score.
func gateReportFixture(sampleSize int, objectiveScore *float64, metricOverrides map[string]float64) schemas.EvaluationReport {
	metrics := schemas.EvalReportMetrics{
		DetectionRecall:   0.8,
		Precision:         0.9,
		PatchCorrectness:  0.85,
		Minimality:        schemas.EvalMinimality{MeanEditDistance: 12, MeanHunkCount: 1.4},
		FalseUpdateRate:   0.02,
		AbstentionQuality: 0.7,
	}
	for key, value := range metricOverrides {
		switch key {
		case "detection_recall":
			metrics.DetectionRecall = value
		case "precision":
			metrics.Precision = value
		case "patch_correctness":
			metrics.PatchCorrectness = value
		case "false_update_rate":
			metrics.FalseUpdateRate = value
		case "abstention_quality":
			metrics.AbstentionQuality = value
		}
	}
	return schemas.EvaluationReport{
		Schema:                   "docsync.evaluation-report/v1",
		PolicyVersion:            1,
		SplitID:                  "frozen-42-1",
		SampleSize:               sampleSize,
		Metrics:                  metrics,
		AbstainHealth:            schemas.EvalReportAbstainHealth{AbstainRate: 0.2, OverrideRate: 0.1},
		VerifierDisagreementRate: 0.03,
		ObjectiveScore:           objectiveScore,
		ComputedAt:               "2026-10-08T12:00:00.000Z",
	}
}

func baselineReport() schemas.EvaluationReport { return gateReportFixture(140, nil, nil) }

// winningCandidate is the strict-win candidate the significance assertions
// replay (detection_recall 0.9 over the 0.8 baseline, everything else equal).
func winningCandidate(objectiveScore *float64) schemas.EvaluationReport {
	return gateReportFixture(140, objectiveScore, map[string]float64{"detection_recall": 0.9})
}

func evalFloatPtr(value float64) *float64 { return &value }

func findGate(result policy.PromotionGateResult, gate string) *policy.GateRow {
	for i := range result.Rows {
		if result.Rows[i].Gate == gate {
			return &result.Rows[i]
		}
	}
	return nil
}

func newGateLibrary(t *testing.T, options policy.PromotionGateLibraryOptions) *policy.PromotionGateLibrary {
	t.Helper()
	library, err := policy.NewPromotionGateLibrary(options)
	require.NoError(t, err)
	return library
}

// TestPromotionGatesNameEveryFailingGate ports "V-P10-13: a promotion attempt
// failing any gate is rejected with the failing gate named".
func TestPromotionGatesNameEveryFailingGate(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})
	result, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  gateReportFixture(40, nil, map[string]float64{"false_update_rate": 0.05}),
		CandidateUnsafeAutoUpdates: 1,
		CandidateWins:              10,
		CandidateTotal:             40,
		BaselineWins:               10,
		BaselineTotal:              40,
	})
	require.NoError(t, err)

	assert.False(t, result.Passed)
	assert.Contains(t, result.FailedGates, "no_regression_false_update")
	assert.Contains(t, result.FailedGates, "unsafe_zero")
	assert.Contains(t, result.FailedGates, "min_sample_size")
	assert.Contains(t, result.FailedGates, "significance_floor")
}

// TestPromotionGatesMinSampleFloorClampsUpwardOnly ports "the min-sample floor
// clamps UPWARD only (OD-R24)".
func TestPromotionGatesMinSampleFloorClampsUpwardOnly(t *testing.T) {
	raised, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{
		MinComparisonSize: evalIntPtr(150),
	})
	require.NoError(t, err)
	assert.Equal(t, 150, raised.MinComparisonSize)

	lowered, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{
		MinComparisonSize: evalIntPtr(50),
	})
	require.NoError(t, err)
	assert.Equal(t, policy.MinComparisonSizeDefault, lowered.MinComparisonSize)

	_, err = policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{
		MinComparisonSize: evalIntPtr(-1),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion gate: minComparisonSize must be a non-negative integer")
}

// TestPromotionGatesSignificanceIsARealTwoProportionTest ports "the
// significance floor is a real two-proportion test: same delta, different n".
func TestPromotionGatesSignificanceIsARealTwoProportionTest(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})

	strong, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
	})
	require.NoError(t, err)
	strongRow := findGate(strong, "significance_floor")
	require.NotNil(t, strongRow)
	assert.True(t, strongRow.Passed)

	weak, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              4,
		CandidateTotal:             8,
		BaselineWins:               2,
		BaselineTotal:              8,
	})
	require.NoError(t, err)
	weakRow := findGate(weak, "significance_floor")
	require.NotNil(t, weakRow)
	assert.False(t, weakRow.Passed)
	assert.Contains(t, weakRow.Detail, "below the floor")

	// Strong z but totals below the comparison floor: significance is NOT
	// assessable — the gate fails with the totals requirement named (abstain,
	// don't guess; the two-proportion result alone is not promotable evidence).
	belowFloor, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              4,
		CandidateTotal:             5,
		BaselineWins:               1,
		BaselineTotal:              5,
	})
	require.NoError(t, err)
	belowFloorRow := findGate(belowFloor, "significance_floor")
	require.NotNil(t, belowFloorRow)
	assert.False(t, belowFloorRow.Passed)
	assert.Contains(t, belowFloorRow.Detail, "below the floor")
	assert.Contains(t, belowFloorRow.Detail, "not assessable")
}

// TestOfflineEvalHarnessEmitsGateChecklistAndAgreementSummary ports "the
// harness runs the injected candidate replay and emits the gate checklist +
// agreement summary".
func TestOfflineEvalHarnessEmitsGateChecklistAndAgreementSummary(t *testing.T) {
	candidateCases := []policy.HarnessCaseEvaluation{
		{CaseID: "c1", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(true)},
		{CaseID: "c2", Outcome: policy.HarnessOutcomeAbstained, Correct: evalBoolPtr(true)},
		{CaseID: "c3", Outcome: policy.HarnessOutcomeMissed, Correct: evalBoolPtr(false)},
	}
	harness, err := policy.NewOfflineEvalHarness(policy.OfflineEvalHarnessDeps{
		Aggregate: func(policy.CaseEvaluations) schemas.EvaluationReport {
			return winningCandidate(evalFloatPtr(0.88))
		},
	})
	require.NoError(t, err)

	comparison, err := harness.Run(policy.OfflineEvalHarnessInput{
		BaselineReport: gateReportFixture(140, evalFloatPtr(0.8), nil),
		BaselineCases: []policy.HarnessCaseEvaluation{
			{CaseID: "c1", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(true)},
			{CaseID: "c2", Outcome: policy.HarnessOutcomeAbstained, Correct: evalBoolPtr(false)},
			{CaseID: "c3", Outcome: policy.HarnessOutcomeMissed, Correct: evalBoolPtr(false)},
		},
		CandidateCases:             candidateCases,
		CandidatePolicyVersion:     2,
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
	})
	require.NoError(t, err)

	assert.Equal(t, policy.VerdictPromotable, comparison.Verdict)
	assert.True(t, comparison.GateChecklist.Passed)
	assert.Equal(t, 1, comparison.AgreementSummary.BothCorrect)
	assert.Equal(t, 1, comparison.AgreementSummary.CandidateOnlyCorrect)
	assert.InDelta(t, 0.1, comparison.Deltas.DetectionRecall, 1e-9)
}

// TestTwoProportionZIsNegativeInfinityForEmptyTotals pins the guard the TS
// module documented (candidateTotal/baselineTotal ≤ 0 ⇒ -Infinity).
func TestTwoProportionZIsNegativeInfinityForEmptyTotals(t *testing.T) {
	assert.True(t, math.IsInf(policy.TwoProportionZ(0, 0, 0, 0), -1))
	assert.True(t, math.IsInf(policy.TwoProportionZ(5, -2, 1, 10), -1))

	// se === 0 with equal proportions is -Infinity (pooled 0 or 1 forces the
	// proportions equal, so the +Infinity branch is unreachable in practice);
	// a strictly better proportion with a non-degenerate pooled share is the
	// finite statistic.
	assert.True(t, math.IsInf(policy.TwoProportionZ(0, 10, 0, 10), -1))
	assert.True(t, math.IsInf(policy.TwoProportionZ(10, 10, 10, 10), -1))
	assert.InDelta(t, 1/math.Sqrt(0.05), policy.TwoProportionZ(10, 10, 0, 10), 1e-12)
}

// TestCompositeScoreUsesCTX36Weights pins the composite fallback weights.
func TestCompositeScoreUsesCTX36Weights(t *testing.T) {
	got := policy.CompositeScore(gateReportFixture(140, nil, map[string]float64{
		"detection_recall":  0.8,
		"precision":         0.9,
		"patch_correctness": 0.85,
		"false_update_rate": 0.02,
	}))
	assert.InDelta(t, 0.4*0.9+0.3*0.8+0.2*0.85+0.1*0.98, got, 1e-12)
}

// TestMandatoryGateNamesCarriesTheSixPhase10Gates pins MANDATORY_GATE_NAMES.
func TestMandatoryGateNamesCarriesTheSixPhase10Gates(t *testing.T) {
	assert.Equal(t, []string{
		"aggregate_correctness_win",
		"no_regression_false_update",
		"no_regression_abstention",
		"unsafe_zero",
		"min_sample_size",
		"significance_floor",
	}, policy.MandatoryGateNames)
	assert.Equal(t, 100, policy.MinComparisonSizeDefault)
	assert.InDelta(t, 0.05, policy.SignificanceAlphaDefault, 1e-12)
	assert.InDelta(t, 1.6448536269514722, policy.ZCritical005, 1e-15)
}

// TestSignificanceRowDetailNamesTheZAndCritical pins the detail spelling of
// the significance row (finite z ⇒ toFixed(4); JavaScript String() numbers).
func TestSignificanceRowDetailNamesTheZAndCritical(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})
	result, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
	})
	require.NoError(t, err)
	row := findGate(result, "significance_floor")
	require.NotNil(t, row)
	assert.Regexp(t, regexp.MustCompile(`one-sided two-proportion z = \d+\.\d{4} vs critical 1\.6448536269514722 at α=0\.05`), row.Detail)

	unsafeRow := findGate(result, "unsafe_zero")
	require.NotNil(t, unsafeRow)
	assert.Equal(t, "unsafe_auto_updates = 0 — must be exactly 0 (ARCH §8.4)", unsafeRow.Detail)
}

// TestPromotionGatesBindingCalibrationIsConditional ports the P12-T09
// wiring (V-P12-11): absent evidence ⇒ the `binding_calibration` gate is NOT
// evaluated at all (not in MandatoryGateNames); present-but-empty evidence ⇒
// fail closed with a failing binding-calibration row; passing evidence rides
// the checklist as an additive row.
func TestPromotionGatesBindingCalibrationIsConditional(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})
	inputs := policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(evalFloatPtr(0.9)),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
	}

	absent, err := gates.CheckGates(inputs)
	require.NoError(t, err)
	for _, row := range absent.Rows {
		assert.NotContains(t, row.Gate, "binding_calibration")
	}

	empty := inputs
	empty.BindingCalibration = []policy.CalibrationResult{}
	present, err := gates.CheckGates(empty)
	require.NoError(t, err)
	assert.False(t, present.Passed)
	var sawFailingBindingGate bool
	for _, gate := range present.FailedGates {
		if strings.HasPrefix(gate, "binding_calibration") {
			sawFailingBindingGate = true
		}
	}
	assert.True(t, sawFailingBindingGate, "present-but-empty evidence fails closed")

	passing := inputs
	passing.BindingCalibration = []policy.CalibrationResult{{BindingKey: "jev#openrouter", Calibrated: true}}
	ok, err := gates.CheckGates(passing)
	require.NoError(t, err)
	assert.True(t, ok.Passed)
	var sawPassingBindingRow bool
	for _, row := range ok.Rows {
		if strings.HasPrefix(row.Gate, "binding_calibration") && row.Passed {
			sawPassingBindingRow = true
		}
	}
	assert.True(t, sawPassingBindingRow, "passing evidence rides the checklist additively")
}

// TestPromotionGateLibraryAlphaOptions pins the α handling: validated range,
// tabulated critical values and the fail-closed refusal for an untabulated α.
func TestPromotionGateLibraryAlphaOptions(t *testing.T) {
	for _, alpha := range []float64{0, 1, 1.5, -0.05} {
		_, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{Alpha: evalFloatPtr(alpha)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "promotion gate: alpha must be in (0, 1)")
	}

	for _, tabulated := range []struct {
		alpha    float64
		critical string
	}{
		{0.05, "1.6448536269514722"},
		{0.025, "1.959963984540054"},
		{0.01, "2.3263478740408408"},
	} {
		library, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{Alpha: evalFloatPtr(tabulated.alpha)})
		require.NoError(t, err)
		assert.InDelta(t, tabulated.alpha, library.Alpha, 1e-12)

		result, err := library.CheckGates(policy.PromotionGateInputs{
			Baseline:                   baselineReport(),
			Candidate:                  winningCandidate(nil),
			CandidateUnsafeAutoUpdates: 0,
			CandidateWins:              70,
			CandidateTotal:             140,
			BaselineWins:               40,
			BaselineTotal:              140,
		})
		require.NoError(t, err)
		row := findGate(result, "significance_floor")
		require.NotNil(t, row)
		assert.Contains(t, row.Detail, "at α="+trimFloat(tabulated.alpha))
		assert.Contains(t, row.Detail, "vs critical "+tabulated.critical)
	}

	_, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{Alpha: evalFloatPtr(0.1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"promotion gate: no z critical value tabulated for alpha 0.1 (use 0.05/0.025/0.01 or extend the table — never approximate silently)")
}

// trimFloat renders an alpha the way JavaScript String(alpha) does for the
// detail assertion (0.025/0.01/0.05).
func trimFloat(v float64) string {
	switch v {
	case 0.05:
		return "0.05"
	case 0.025:
		return "0.025"
	case 0.01:
		return "0.01"
	}
	return ""
}

// TestCheckGatesRejectsMalformedGateInputs pins the fail-closed refusals:
// negative gate metrics, a negative repo floor, and reports that fail the
// evaluation-report schema.
func TestCheckGatesRejectsMalformedGateInputs(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})
	base := policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
	}

	negativeUnsafe := base
	negativeUnsafe.CandidateUnsafeAutoUpdates = -1
	_, err := gates.CheckGates(negativeUnsafe)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion gate: candidateUnsafeAutoUpdates must be a non-negative integer")

	negativeRepoFloor := base
	negativeRepoFloor.RepoMinComparisonSize = evalIntPtr(-1)
	_, err = gates.CheckGates(negativeRepoFloor)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion gate: repoMinComparisonSize must be a non-negative integer")

	invalidBaseline := base
	badBaseline := baselineReport()
	badBaseline.SampleSize = 0
	invalidBaseline.Baseline = badBaseline
	_, err = gates.CheckGates(invalidBaseline)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion gate: invalid baseline evaluation report")
	assert.Contains(t, err.Error(), "sample_size")

	invalidCandidate := base
	badCandidate := winningCandidate(nil)
	badCandidate.Schema = "docsync.other/v1"
	invalidCandidate.Candidate = badCandidate
	_, err = gates.CheckGates(invalidCandidate)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion gate: invalid candidate evaluation report")
}

// TestCheckGatesRejectsMalformedBuckets pins the P12-T06 bucket validation
// (fail closed on malformed bucket data before any row is built).
func TestCheckGatesRejectsMalformedBuckets(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})
	base := policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
	}
	cleanMetrics := policy.PromotionBucketMetrics{FalseUpdateRate: 0.02, AbstentionQuality: 0.7}

	for name, buckets := range map[string][]policy.PromotionBucketComparison{
		"empty name": {{Bucket: "   ", Baseline: cleanMetrics, Candidate: cleanMetrics}},
		"duplicate name": {
			{Bucket: "both-ignore", Baseline: cleanMetrics, Candidate: cleanMetrics},
			{Bucket: "both-ignore", Baseline: cleanMetrics, Candidate: cleanMetrics},
		},
		"false_update_rate above 1": {{
			Bucket:    "b",
			Baseline:  policy.PromotionBucketMetrics{FalseUpdateRate: 1.5, AbstentionQuality: 0.7},
			Candidate: cleanMetrics,
		}},
		"abstention_quality negative": {{
			Bucket:    "b",
			Baseline:  cleanMetrics,
			Candidate: policy.PromotionBucketMetrics{FalseUpdateRate: 0.01, AbstentionQuality: -0.1},
		}},
		"abstention_quality NaN": {{
			Bucket:    "b",
			Baseline:  cleanMetrics,
			Candidate: policy.PromotionBucketMetrics{FalseUpdateRate: 0.01, AbstentionQuality: math.NaN()},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			inputs := base
			inputs.Buckets = buckets
			_, err := gates.CheckGates(inputs)
			require.Error(t, err)
			assert.Equal(t, "EDOCSYNC_VALIDATION", docutil.CodeOf(err))
		})
	}
}

// TestCheckGatesBucketRowsAreAdditive pins the per-bucket no-regression row:
// a bucket regression is named in failedGates even when every aggregate gate
// passes, and the row's detail names both bucket metrics.
func TestCheckGatesBucketRowsAreAdditive(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})
	clean := policy.PromotionBucketMetrics{FalseUpdateRate: 0.02, AbstentionQuality: 0.7}

	result, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
		Buckets: []policy.PromotionBucketComparison{
			{Bucket: "both-review", Baseline: clean, Candidate: clean},
			{
				Bucket:    "both-ignore",
				Baseline:  policy.PromotionBucketMetrics{FalseUpdateRate: 0.03, AbstentionQuality: 0.9},
				Candidate: policy.PromotionBucketMetrics{FalseUpdateRate: 0.09, AbstentionQuality: 0.8},
			},
		},
	})
	require.NoError(t, err)

	assert.False(t, result.Passed)
	assert.Equal(t, []string{"bucket_no_regression:both-ignore"}, result.FailedGates)
	row := findGate(result, "bucket_no_regression:both-ignore")
	require.NotNil(t, row)
	assert.Contains(t, row.Detail, "both-ignore")
	assert.Contains(t, row.Detail, "false_update_rate 0.09 vs baseline 0.03 (must not rise)")
	assert.Contains(t, row.Detail, "abstention_quality 0.8 vs baseline 0.9 (must not fall)")
}

// TestCheckGatesSignificanceDetailSpellings pins the finite-z and ±Infinity
// spellings of the significance row detail.
func TestCheckGatesSignificanceDetailSpellings(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})

	// Totals above the floor but a tiny delta: the row fails with the finite
	// z spelled to four decimals.
	tiny, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              55,
		CandidateTotal:             150,
		BaselineWins:               50,
		BaselineTotal:              150,
	})
	require.NoError(t, err)
	tinyRow := findGate(tiny, "significance_floor")
	require.NotNil(t, tinyRow)
	assert.False(t, tinyRow.Passed)
	assert.Contains(t, tinyRow.Detail,
		"z = "+strconv.FormatFloat(policy.TwoProportionZ(55, 150, 50, 150), 'f', 4, 64)+" vs critical")

	// Equal win rates at full totals: pooled = 1 ⇒ se = 0 ⇒ -Infinity, which
	// JavaScript spells exactly this way (and never passes the strict test).
	tied, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              150,
		CandidateTotal:             150,
		BaselineWins:               150,
		BaselineTotal:              150,
	})
	require.NoError(t, err)
	tiedRow := findGate(tied, "significance_floor")
	require.NotNil(t, tiedRow)
	assert.False(t, tiedRow.Passed)
	assert.Contains(t, tiedRow.Detail, "z = -Infinity vs critical 1.6448536269514722")
}

// TestOfflineEvalHarnessAgreementMatrixPinsEveryCell ports the agreement-matrix
// computation: every (baselineCorrect, candidateCorrect) cell is counted, and a
// baseline case with no candidate replay is skipped (TS: `if (!c) continue`).
func TestOfflineEvalHarnessAgreementMatrixPinsEveryCell(t *testing.T) {
	harness, err := policy.NewOfflineEvalHarness(policy.OfflineEvalHarnessDeps{
		Aggregate: func(policy.CaseEvaluations) schemas.EvaluationReport { return winningCandidate(nil) },
	})
	require.NoError(t, err)

	comparison, err := harness.Run(policy.OfflineEvalHarnessInput{
		BaselineReport: baselineReport(),
		BaselineCases: []policy.HarnessCaseEvaluation{
			{CaseID: "both-correct", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(true)},
			{CaseID: "both-wrong", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(false)},
			{CaseID: "candidate-only", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(false)},
			{CaseID: "baseline-only", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(true)},
			{CaseID: "no-replay", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(true)},
		},
		CandidateCases: []policy.HarnessCaseEvaluation{
			{CaseID: "both-correct", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(true)},
			{CaseID: "both-wrong", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(false)},
			{CaseID: "candidate-only", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(true)},
			{CaseID: "baseline-only", Outcome: policy.HarnessOutcomeUpdated, Correct: evalBoolPtr(false)},
		},
		CandidatePolicyVersion:     2,
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
	})
	require.NoError(t, err)

	assert.Equal(t, policy.AgreementSummary{
		BothCorrect:          1,
		BothWrong:            1,
		CandidateOnlyCorrect: 1,
		BaselineOnlyCorrect:  1,
	}, comparison.AgreementSummary)
}

// TestOfflineEvalHarnessFailClosed pins the harness construction and run
// error paths (malformed gate options; an aggregate output that fails the
// evaluation-report schema).
func TestOfflineEvalHarnessFailClosed(t *testing.T) {
	_, err := policy.NewOfflineEvalHarness(policy.OfflineEvalHarnessDeps{
		Aggregate: func(policy.CaseEvaluations) schemas.EvaluationReport { return baselineReport() },
		Alpha:     evalFloatPtr(1.5),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion gate: alpha must be in (0, 1)")

	harness, err := policy.NewOfflineEvalHarness(policy.OfflineEvalHarnessDeps{
		Aggregate: func(policy.CaseEvaluations) schemas.EvaluationReport {
			broken := winningCandidate(nil)
			broken.SampleSize = 0
			return broken
		},
	})
	require.NoError(t, err)
	_, err = harness.Run(policy.OfflineEvalHarnessInput{
		BaselineReport:             baselineReport(),
		CandidateCases:             []policy.HarnessCaseEvaluation{{CaseID: "c1", Outcome: policy.HarnessOutcomeUpdated}},
		CandidatePolicyVersion:     2,
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              70,
		CandidateTotal:             140,
		BaselineWins:               40,
		BaselineTotal:              140,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "promotion gate: invalid candidate evaluation report")
}

// TestCheckGatesRepoFloorRaisesTheEffectiveFloor pins the per-call repository
// floor clamp (P12-T13; OD-R24): effectiveMax = max(library floor, repo floor)
// names the effective floor in both comparison rows.
func TestCheckGatesRepoFloorRaisesTheEffectiveFloor(t *testing.T) {
	gates := newGateLibrary(t, policy.PromotionGateLibraryOptions{})
	result, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baselineReport(),
		Candidate:                  winningCandidate(nil),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              110,
		CandidateTotal:             150,
		BaselineWins:               80,
		BaselineTotal:              150,
		RepoMinComparisonSize:      evalIntPtr(200),
	})
	require.NoError(t, err)

	assert.False(t, result.Passed)
	assert.Equal(t, []string{"min_sample_size", "significance_floor"}, result.FailedGates)
	assert.Contains(t, findGate(result, "min_sample_size").Detail, "sample_size 140 vs floor 200 (upward-clamped, OD-R24)")
	sigRow := findGate(result, "significance_floor")
	require.NotNil(t, sigRow)
	assert.Contains(t, sigRow.Detail, "candidate 150, baseline 150 vs floor 200")
	assert.Contains(t, sigRow.Detail, "not assessable")
}

func evalIntPtr(value int) *int { return &value }

func evalBoolPtr(value bool) *bool { return &value }
