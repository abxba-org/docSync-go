// Ported from core test suites (P10-T07..T13 + T24, P12-T19): coverage tests
// targeting every statement and branch in internal/evaluation not exercised by
// the existing evaluation_test.go / monitoring_test.go. Black-box, exported
// surface only, deterministic, no network.
package evaluation_test

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/DUDE616/docSync-go/internal/evaluation"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	covAt   = "2026-10-08T12:00:00Z"
	covNow  = "2026-10-08T12:00:00Z"
	covRepo = "acme/widgets"
)

func covSha(n int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 40)
	for i := range out {
		out[i] = '0'
	}
	value := uint64(n)
	for i := 39; i >= 0 && value > 0; i-- {
		out[i] = hex[value&0xf]
		value >>= 4
	}
	return string(out)
}

func covF64(v float64) *float64 { return &v }

func covB(v bool) *bool { return &v }

func covLabel(index int, decisionID string, policyVersion int) schemas.HygieneLabel {
	// LabelID must be a valid ULID (EvalCase.Validate enforces it); a fixed
	// valid ULID is sufficient because each coverage test uses at most one label.
	return schemas.HygieneLabel{
		Schema:        "docsync.hygiene-label/v1",
		LabelID:       "01J9ZM6T0CZA6D8E0A2C4D6E8F",
		DecisionID:    decisionID,
		PolicyVersion: policyVersion,
		Label:         schemas.HygieneLabelAccepted,
		SourceSignal:  schemas.HygieneSourceSpotCheck,
		HygieneRule:   "direct",
		NormalizedAt:  covAt,
	}
}

func covFixRunRecord(runID, docPath string) map[string]any {
	return map[string]any{
		"schema":           "docsync.fix-run-record/v1",
		"run_id":           runID,
		"at":               covAt,
		"repo":             covRepo,
		"base":             covSha(len(runID)*7 + 1),
		"head":             covSha(len(runID)*7 + 2),
		"manifest_version": "1",
		"policy_version":   17,
		"applied":          []any{map[string]any{"doc_path": docPath, "patch_ref": "patch_" + runID, "three_way_used": false}},
		"skipped":          []any{},
		"refused":          []any{},
	}
}

func covDecisionRecord(decisionID string, policyVersion int, docPath string) map[string]any {
	return map[string]any{
		"decision_id":      decisionID,
		"repo":             covRepo,
		"base_sha":         covSha(len(decisionID)*13 + 1),
		"head_sha":         covSha(len(decisionID)*13 + 2),
		"manifest_version": "1",
		"policy_version":   policyVersion,
		"documents":        []any{map[string]any{"doc_path": docPath, "policy_outcome": "auto", "decision_id": decisionID, "partial_coverage": false}},
	}
}

func covDetBuilder() *evaluation.DatasetBuilder {
	builder, err := evaluation.NewDatasetBuilder(evaluation.DatasetBuilderOptions{
		Clock:     func() int64 { return 1_700_000_000_000 },
		RandBytes: func(n int) ([]byte, error) { return make([]byte, n), nil },
	})
	if err != nil {
		panic(err)
	}
	return builder
}

var _ = strconv.Itoa

func TestCOV_EVAL_DatasetBuilderNilClock(t *testing.T) {
	_, err := evaluation.NewDatasetBuilder(evaluation.DatasetBuilderOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clock must be non-nil")
}

func TestCOV_EVAL_BuildLabeledCasesEmptyInputs(t *testing.T) {
	builder := covDetBuilder()
	result, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{})
	require.NoError(t, err)
	assert.Empty(t, result.Cases)
	assert.Empty(t, result.Rejected)

	report := builder.YieldReport(nil)
	assert.Equal(t, 0, report.Total)
	assert.Equal(t, evaluation.DatasetYieldVerdictInsufficient, report.Verdict)
	assert.Equal(t, "fixture", report.DataSource)

	report = builder.YieldReport([]schemas.EvalCase{})
	assert.Equal(t, 0, report.Total)
	assert.Equal(t, evaluation.DatasetYieldVerdictInsufficient, report.Verdict)
}

func TestCOV_EVAL_BuildLabeledCasesTrueNegativeWithoutEdits(t *testing.T) {
	builder := covDetBuilder()
	result, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
		RunRecords: []map[string]any{
			{
				"repo":             covRepo,
				"base_sha":         covSha(1),
				"head_sha":         covSha(2),
				"manifest_version": "1",
				"policy_version":   17,
				"docs_affected":    false,
				"pr_number":        42,
			},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result.Cases)
	assert.Empty(t, result.Rejected)
}

func TestCOV_EVAL_BuildLabeledCasesConcurrencyGroupSuffixDigits(t *testing.T) {
	builder := covDetBuilder()
	result, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
		RunRecords: []map[string]any{
			{
				"repo":              covRepo,
				"base_sha":          covSha(1),
				"head_sha":          covSha(2),
				"manifest_version":  "1",
				"policy_version":    17,
				"docs_affected":     false,
				"concurrency_group": "docsync-acme-42",
			},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result.Cases)
	assert.Empty(t, result.Rejected)

	result, err = builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
		RunRecords: []map[string]any{
			{
				"repo":              covRepo,
				"base_sha":          covSha(1),
				"head_sha":          covSha(2),
				"manifest_version":  "1",
				"policy_version":    17,
				"docs_affected":     false,
				"concurrency_group": "docsync-acme-abc",
			},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result.Cases)
	assert.Empty(t, result.Rejected)
}

func TestCOV_EVAL_BuildLabeledCasesNumericManifestVersion(t *testing.T) {
	builder := covDetBuilder()
	result, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
		RunRecords: []map[string]any{
			{
				"repo":             covRepo,
				"base_sha":         covSha(1),
				"head_sha":         covSha(2),
				"manifest_version": 3,
				"policy_version":   17,
				"documents": []any{
					map[string]any{"doc_path": "docs/a.md", "policy_outcome": "auto", "decision_id": "d_1"},
				},
			},
		},
		Labels: []schemas.HygieneLabel{covLabel(0, "d_1", 17)},
	})
	require.NoError(t, err)
	require.Len(t, result.Cases, 1)
	assert.Equal(t, "3", result.Cases[0].RunKey.ManifestVersion)
}

func TestCOV_EVAL_BuildLabeledCasesKeyNotAMap(t *testing.T) {
	builder := covDetBuilder()
	result, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
		RunRecords: []map[string]any{
			{
				"repo":             covRepo,
				"base_sha":         covSha(1),
				"head_sha":         covSha(2),
				"manifest_version": "1",
				"policy_version":   17,
				"key":              "not-a-map",
				"documents": []any{
					map[string]any{"doc_path": "docs/a.md", "policy_outcome": "auto", "decision_id": "d_1"},
				},
			},
		},
		Labels: []schemas.HygieneLabel{covLabel(0, "d_1", 17)},
	})
	require.NoError(t, err)
	require.Len(t, result.Cases, 1)
}

func TestCOV_EVAL_BuildLabeledCasesTrueNegativeInvalid(t *testing.T) {
	builder := covDetBuilder()
	result, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
		RunRecords: []map[string]any{
			{
				"repo":             covRepo,
				"base_sha":         covSha(1),
				"head_sha":         covSha(2),
				"manifest_version": "1",
				"policy_version":   17,
				"docs_affected":    false,
				"pr_number":        42,
			},
		},
		LaterDocEdits: []evaluation.LaterDocEdit{{PRKey: "42", DocPath: "", At: covAt}},
	})
	require.NoError(t, err)
	assert.Empty(t, result.Cases)
	require.Len(t, result.Rejected, 1)
	assert.Equal(t, evaluation.RejectedSchemaValidationFailed, result.Rejected[0].Reason)
	require.NotNil(t, result.Rejected[0].RunIndex)
	assert.Equal(t, 0, *result.Rejected[0].RunIndex)
}

func TestCOV_EVAL_WriteDatasetByteIdenticalAndInvalid(t *testing.T) {
	builder := covDetBuilder()
	built, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
		RunRecords: []map[string]any{covFixRunRecord("r1", "docs/payments.md")},
		Labels:     []schemas.HygieneLabel{covLabel(0, "r1", 17)},
	})
	require.NoError(t, err)
	require.Len(t, built.Cases, 1)

	dir := t.TempDir()
	evaluationDir := filepath.Join(dir, ".docsync", "evaluation")
	first, err := builder.WriteDataset(built.Cases, evaluation.WriteDatasetOptions{
		Name:          "ds",
		EvaluationDir: evaluationDir,
	})
	require.NoError(t, err)
	second, err := builder.WriteDataset(built.Cases, evaluation.WriteDatasetOptions{
		Name:          "ds",
		EvaluationDir: evaluationDir,
	})
	require.NoError(t, err)
	assert.Equal(t, first.Bytes, second.Bytes)
	assert.Equal(t, first.Path, second.Path)

	bad := built.Cases[0]
	bad.RunKey.Repo = ""
	_, err = builder.WriteDataset([]schemas.EvalCase{bad}, evaluation.WriteDatasetOptions{
		Name: "bad", EvaluationDir: t.TempDir(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail-closed")
}

func TestCOV_EVAL_YieldReportAllTrueNegatives(t *testing.T) {
	builder := covDetBuilder()
	built, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
		RunRecords: []map[string]any{
			covDecisionRecord("d_labeled", 17, "docs/a.md"),
			{
				"repo":             covRepo,
				"base_sha":         covSha(1),
				"head_sha":         covSha(2),
				"manifest_version": "1",
				"policy_version":   17,
				"docs_affected":    false,
				"pr_number":        7,
				"decision_id":      "d_tn",
			},
		},
		Labels:        []schemas.HygieneLabel{covLabel(0, "d_labeled", 17)},
		LaterDocEdits: []evaluation.LaterDocEdit{{PRKey: "7", DocPath: "docs/a.md", At: covAt}},
	})
	require.NoError(t, err)
	require.Len(t, built.Cases, 2)

	report := builder.YieldReport(built.Cases)
	assert.Equal(t, 2, report.Total)
	assert.Equal(t, 1, report.Labeled)
	assert.Equal(t, 1, report.TrueNegatives)
	assert.Equal(t, map[int]int{17: 2}, report.ByPolicyVersion)
	assert.Equal(t, evaluation.DatasetYieldVerdictInsufficient, report.Verdict)
}

func TestCOV_EVAL_FrozenSplitEmptyAndRounding(t *testing.T) {
	empty := evaluation.BuildFrozenSplit(nil, evaluation.FrozenSplitOptions{
		Seed:           7,
		DatasetVersion: "v1",
		CreatedAt:      covAt,
	})
	assert.Empty(t, empty.CaseIDs)
	assert.Equal(t, "frozen-7-v1", empty.SplitID)

	one := evaluation.BuildFrozenSplit([]struct{ CaseID string }{{CaseID: "c1"}}, evaluation.FrozenSplitOptions{
		Seed:           7,
		DatasetVersion: "v1",
		CreatedAt:      covAt,
	})
	assert.Len(t, one.CaseIDs, 1)
	assert.Equal(t, []string{"c1"}, one.CaseIDs)

	two := evaluation.BuildFrozenSplit([]struct{ CaseID string }{{CaseID: "a"}, {CaseID: "b"}}, evaluation.FrozenSplitOptions{
		Seed:           7,
		DatasetVersion: "v1",
		CreatedAt:      covAt,
	})
	assert.Len(t, two.CaseIDs, 1)
	assert.IsIncreasing(t, two.CaseIDs)
}

func TestCOV_EVAL_RollingSplitUnparseableAndEmpty(t *testing.T) {
	_, err := evaluation.BuildRollingSplit(nil, evaluation.RollingSplitOptions{
		WindowDays: 7,
		Now:        "not-a-date",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a parseable ISO-8601 instant")

	fallback, err := evaluation.BuildRollingSplit(nil, evaluation.RollingSplitOptions{
		WindowDays: 7,
		Now:        covNow,
	})
	require.NoError(t, err)
	assert.Equal(t, evaluation.RollingSplitModeLastNFallback, fallback.Mode)
	assert.Empty(t, fallback.CaseIDs)

	cases := []struct{ CaseID string }{{CaseID: "c1"}, {CaseID: "c2"}}
	ts := map[string]string{"c1": "not-a-date", "c2": "also-not-a-date"}
	rolled, err := evaluation.BuildRollingSplit(cases, evaluation.RollingSplitOptions{
		WindowDays: 7,
		Now:        covNow,
		Timestamps: ts,
	})
	require.NoError(t, err)
	// Timestamps present but all unparseable → timestamp-window lane with an
	// empty selection (unparseable timestamps are skipped, not counted).
	assert.Equal(t, evaluation.RollingSplitModeTimestampWindow, rolled.Mode)
	assert.Equal(t, 0, rolled.ExcludedWithoutTimestamp)
	assert.Empty(t, rolled.CaseIDs)

	// Non-nil but empty timestamps map → every case is without timestamp →
	// declared fallback lane.
	emptyTs := map[string]string{}
	rolled, err = evaluation.BuildRollingSplit(cases, evaluation.RollingSplitOptions{
		WindowDays: 7,
		Now:        covNow,
		Timestamps: emptyTs,
	})
	require.NoError(t, err)
	assert.Equal(t, evaluation.RollingSplitModeLastNFallback, rolled.Mode)
	assert.Equal(t, 0, rolled.ExcludedWithoutTimestamp)
	assert.Len(t, rolled.CaseIDs, 2)
	assert.IsIncreasing(t, rolled.CaseIDs)

	cases = []struct{ CaseID string }{{CaseID: "c1"}, {CaseID: "c2"}}
	ts = map[string]string{"c1": "2026-10-08", "c2": "not-a-date"}
	rolled, err = evaluation.BuildRollingSplit(cases, evaluation.RollingSplitOptions{
		WindowDays: 7,
		Now:        covNow,
		Timestamps: ts,
	})
	require.NoError(t, err)
	assert.Equal(t, evaluation.RollingSplitModeTimestampWindow, rolled.Mode)
	assert.Equal(t, 1, len(rolled.CaseIDs))
	assert.Equal(t, "c1", rolled.CaseIDs[0])
	assert.Equal(t, 0, rolled.ExcludedWithoutTimestamp)
}

func TestCOV_EVAL_ContaminationCheckDuplicates(t *testing.T) {
	result := evaluation.ContaminationCheck([]string{"a", "b", "a"}, []string{"a", "a"})
	assert.False(t, result.Disjoint)
	assert.Equal(t, []string{"a"}, result.Violations)
}

func TestCOV_EVAL_SpotCheckSampleSizeBoundaries(t *testing.T) {
	size, err := evaluation.SpotCheckSampleSize(1)
	require.NoError(t, err)
	assert.Equal(t, 1, size)

	size, err = evaluation.SpotCheckSampleSize(4)
	require.NoError(t, err)
	assert.Equal(t, 1, size)

	size, err = evaluation.SpotCheckSampleSize(49)
	require.NoError(t, err)
	assert.Equal(t, 5, size)

	size, err = evaluation.SpotCheckSampleSize(50)
	require.NoError(t, err)
	assert.Equal(t, 5, size)

	size, err = evaluation.SpotCheckSampleSize(100)
	require.NoError(t, err)
	assert.Equal(t, 10, size)

	_, err = evaluation.SpotCheckSampleSize(-1)
	require.Error(t, err)
}

func TestCOV_EVAL_SampleAutoPatchesEmptyDecisionID(t *testing.T) {
	_, err := evaluation.SampleAutoPatches([]evaluation.AutoAppliedPatch{{DecisionID: ""}}, evaluation.Mulberry32(42))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-empty decisionId")
}

func TestCOV_EVAL_SampleAutoPatchesRngFloor(t *testing.T) {
	patches := []evaluation.AutoAppliedPatch{{DecisionID: "d1"}, {DecisionID: "d2"}}
	selected, err := evaluation.SampleAutoPatches(patches, evaluation.Mulberry32(1))
	require.NoError(t, err)
	assert.Len(t, selected, 1)

	zero := func() float64 { return 0.0 }
	selected, err = evaluation.SampleAutoPatches(patches, zero)
	require.NoError(t, err)
	assert.Len(t, selected, 1)
}

func TestCOV_EVAL_IngestAuditFindingsNilOpts(t *testing.T) {
	labels, err := evaluation.IngestAuditFindings([]evaluation.AuditFinding{
		{DecisionID: "d_1", PolicyVersion: 17, Verdict: evaluation.AuditVerdictPatchCorrect, At: covAt},
	}, nil)
	require.NoError(t, err)
	require.Len(t, labels, 1)
	assert.Empty(t, labels[0].Validate())
	assert.Equal(t, schemas.HygieneSourceAuditFinding, labels[0].SourceSignal)
	assert.Equal(t, schemas.HygieneLabelAccepted, labels[0].Label)
	assert.Equal(t, "audit_finding:patch_correct⇒accepted", labels[0].HygieneRule)
}

func TestCOV_EVAL_IngestAuditFindingsAllVerdicts(t *testing.T) {
	labels, err := evaluation.IngestAuditFindings([]evaluation.AuditFinding{
		{DecisionID: "d_1", PolicyVersion: 17, Verdict: evaluation.AuditVerdictPatchIncorrect, At: covAt},
		{DecisionID: "d_2", PolicyVersion: 17, Verdict: evaluation.AuditVerdictUpdateUnwarranted, At: covAt},
	}, &evaluation.IngestAuditFindingsOptions{NowMs: covInt64(), RandBytes: covConstRng})
	require.NoError(t, err)
	require.Len(t, labels, 2)
	assert.Equal(t, schemas.HygieneLabelCorrected, labels[0].Label)
	assert.Equal(t, schemas.HygieneLabelFalsePositive, labels[1].Label)
}

func covInt64() *int64 { v := int64(1_700_000_000_000); return &v }

func covConstRng(n int) ([]byte, error) {
	out := make([]byte, n)
	for i := range out {
		out[i] = 7
	}
	return out, nil
}

func TestCOV_EVAL_ComputeMetricsNoUpdatesShareZero(t *testing.T) {
	result, err := evaluation.ComputeMetrics(evaluation.ComputeMetricsInput{
		PolicyVersion: 17,
		SplitID:       "split",
		ComputedAt:    covAt,
		Overrides:     evaluation.OverrideCounts{OverrideCount: 0, DecisionCount: 1},
		Cases: []evaluation.CaseEvaluation{
			{CaseID: "a1", Outcome: evaluation.CaseOutcomeAbstained, WasCorrectAbstain: covB(true)},
			{CaseID: "m1", Outcome: evaluation.CaseOutcomeMissed},
		},
	})
	require.NoError(t, err)
	assert.InDelta(t, 1.0, result.Report.Metrics.Precision, 1e-12)
	assert.InDelta(t, 1.0, result.Report.Metrics.PatchCorrectness, 1e-12)
	assert.InDelta(t, 0.0, result.Report.Metrics.DetectionRecall, 1e-12)
	assert.InDelta(t, 1.0, result.Report.Metrics.AbstentionQuality, 1e-12)
	assert.Equal(t, 2, result.Report.SampleSize)
}

func TestCOV_EVAL_ComputeMetricsNilMeasuredFieldNaN(t *testing.T) {
	bad := evaluation.CaseEvaluation{
		CaseID:              "c1",
		Outcome:             evaluation.CaseOutcomeUpdated,
		WasCorrectUpdate:    covB(true),
		AnchorMatched:       covB(true),
		ShippedHunks:        nil,
		TriggeringDiffLines: covF64(4),
		EditDistance:        covF64(1),
	}
	_, err := evaluation.ComputeMetrics(evaluation.ComputeMetricsInput{
		PolicyVersion: 17, SplitID: "split", ComputedAt: covAt,
		Overrides: evaluation.OverrideCounts{OverrideCount: 0, DecisionCount: 1},
		Cases:     []evaluation.CaseEvaluation{bad},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shippedHunks")
}

func TestCOV_EVAL_ComputeMetricsInfMeasuredField(t *testing.T) {
	bad := evaluation.CaseEvaluation{
		CaseID:              "c1",
		Outcome:             evaluation.CaseOutcomeUpdated,
		WasCorrectUpdate:    covB(true),
		AnchorMatched:       covB(true),
		ShippedHunks:        covF64(1),
		TriggeringDiffLines: covF64(math.Inf(1)),
		EditDistance:        covF64(1),
	}
	_, err := evaluation.ComputeMetrics(evaluation.ComputeMetricsInput{
		PolicyVersion: 17, SplitID: "split", ComputedAt: covAt,
		Overrides: evaluation.OverrideCounts{OverrideCount: 0, DecisionCount: 1},
		Cases:     []evaluation.CaseEvaluation{bad},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Infinity")

	bad2 := evaluation.CaseEvaluation{
		CaseID:              "c1",
		Outcome:             evaluation.CaseOutcomeUpdated,
		WasCorrectUpdate:    covB(true),
		AnchorMatched:       covB(true),
		ShippedHunks:        covF64(1),
		TriggeringDiffLines: covF64(math.Inf(-1)),
		EditDistance:        covF64(1),
	}
	_, err = evaluation.ComputeMetrics(evaluation.ComputeMetricsInput{
		PolicyVersion: 17, SplitID: "split", ComputedAt: covAt,
		Overrides: evaluation.OverrideCounts{OverrideCount: 0, DecisionCount: 1},
		Cases:     []evaluation.CaseEvaluation{bad2},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-Infinity")
}

func TestCOV_EVAL_ComputeMetricsEditDistanceRequireError(t *testing.T) {
	bad := evaluation.CaseEvaluation{
		CaseID:              "c1",
		Outcome:             evaluation.CaseOutcomeUpdated,
		WasCorrectUpdate:    covB(true),
		AnchorMatched:       covB(true),
		ShippedHunks:        covF64(1),
		TriggeringDiffLines: covF64(4),
		EditDistance:        covF64(-1),
	}
	_, err := evaluation.ComputeMetrics(evaluation.ComputeMetricsInput{
		PolicyVersion: 17, SplitID: "split", ComputedAt: covAt,
		Overrides: evaluation.OverrideCounts{OverrideCount: 0, DecisionCount: 1},
		Cases:     []evaluation.CaseEvaluation{bad},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "editDistance")
}

func TestCOV_EVAL_ComputeMetricsBaselineVersionAndValidation(t *testing.T) {
	result, err := evaluation.ComputeMetrics(evaluation.ComputeMetricsInput{
		PolicyVersion:   17,
		BaselineVersion: covIntPtr(16),
		SplitID:         "split",
		ComputedAt:      covAt,
		Overrides:       evaluation.OverrideCounts{OverrideCount: 0, DecisionCount: 1},
		Cases: []evaluation.CaseEvaluation{
			{CaseID: "c1", Outcome: evaluation.CaseOutcomeUpdated, WasCorrectUpdate: covB(true),
				AnchorMatched: covB(true), ShippedHunks: covF64(1), TriggeringDiffLines: covF64(4), EditDistance: covF64(1)},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Report.BaselineVersion)
	assert.Equal(t, 16, *result.Report.BaselineVersion)

	raw, err := json.Marshal(result.Report)
	require.NoError(t, err)
	parsed, err := schemas.ParseEvaluationReport(raw)
	require.NoError(t, err)
	assert.Equal(t, 17, parsed.PolicyVersion)
}

func covIntPtr(v int) *int { return &v }

func TestCOV_EVAL_EvaluateAbstainHealthBoundaries(t *testing.T) {
	alert, fired := evaluation.EvaluateAbstainHealth(0.5, 0.1)
	assert.False(t, fired)
	assert.Equal(t, evaluation.AbstainHealthAlert(""), alert)

	alert, fired = evaluation.EvaluateAbstainHealth(0.05, 0.9)
	assert.False(t, fired)

	alert, fired = evaluation.EvaluateAbstainHealth(0.02, 0.3)
	assert.False(t, fired)

	alert, fired = evaluation.EvaluateAbstainHealth(math.NaN(), math.NaN())
	assert.False(t, fired)
}

func TestCOV_EVAL_NormalizeObjectiveWeightsEdgeCases(t *testing.T) {
	_, err := evaluation.NormalizeObjectiveWeights(evaluation.ObjectiveWeights{
		Precision: math.NaN(), Recall: 0.3, PatchCorrectness: 0.2, Efficiency: 0.1,
	})
	require.Error(t, err)

	_, err = evaluation.NormalizeObjectiveWeights(evaluation.ObjectiveWeights{
		Precision: math.Inf(1), Recall: 0.3, PatchCorrectness: 0.2, Efficiency: 0.1,
	})
	require.Error(t, err)

	_, err = evaluation.NormalizeObjectiveWeights(evaluation.ObjectiveWeights{
		Precision: -0.1, Recall: 0.3, PatchCorrectness: 0.2, Efficiency: 0.1,
	})
	require.Error(t, err)

	_, err = evaluation.NormalizeObjectiveWeights(evaluation.ObjectiveWeights{
		Precision: 0.5, Recall: 0.5, PatchCorrectness: 0.5, Efficiency: 0.5,
	})
	require.Error(t, err)

	w, err := evaluation.NormalizeObjectiveWeights(evaluation.ObjectiveWeights{
		Precision: 0.5, Recall: 0.5, PatchCorrectness: 0, Efficiency: 0,
	})
	require.NoError(t, err)
	assert.Equal(t, 0.5, w.Precision)
	assert.Equal(t, 0.5, w.Recall)
}

func TestCOV_EVAL_ComputeEfficiencyInvalidUsage(t *testing.T) {
	_, err := evaluation.ComputeEfficiency(evaluation.UsageAccounting{TotalCostUsd: -1, TotalLatencyMs: 100})
	require.Error(t, err)

	_, err = evaluation.ComputeEfficiency(evaluation.UsageAccounting{TotalCostUsd: 1, TotalLatencyMs: math.NaN()})
	require.Error(t, err)

	_, err = evaluation.ComputeEfficiency(evaluation.UsageAccounting{TotalCostUsd: 1, TotalLatencyMs: -5})
	require.Error(t, err)
}

func TestCOV_EVAL_ComputeObjectiveScoreMetricValidation(t *testing.T) {
	metrics := evaluation.ObjectiveMetricsInput{
		DetectionRecall:  0.94,
		Precision:        0.9,
		PatchCorrectness: 0.88,
	}
	usage := evaluation.UsageAccounting{TotalCostUsd: 1, TotalLatencyMs: 3000}

	bad := metrics
	bad.DetectionRecall = math.NaN()
	_, err := evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
		Metrics: bad, Usage: usage, UnsafeAutoUpdates: 0,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detection_recall")

	bad = metrics
	bad.Precision = 1.5
	_, err = evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
		Metrics: bad, Usage: usage, UnsafeAutoUpdates: 0,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "precision")

	_, err = evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
		Metrics: metrics, Usage: evaluation.UsageAccounting{TotalCostUsd: 1000, TotalLatencyMs: 100000},
		UnsafeAutoUpdates: 0,
	})
	require.NoError(t, err)
}

func TestCOV_EVAL_CostAttributionProviderNonNil(t *testing.T) {
	runs := []evaluation.SteadyStateRunInput{
		{
			RunID:            "run_p",
			Repo:             covRepo,
			PolicyVersion:    17,
			Decisions:        10,
			Abstains:         1,
			Overrides:        1,
			AutoApplied:      1,
			SpotCheckSampled: 1,
			SpotCheckChecked: 1,
			Usages: []evaluation.CostAttributionUsage{
				{Role: "writer", Model: "m", Provider: covStrPtr("anthropic"), PromptTokens: 10, CompletionTokens: 5, CostUsd: covF64(0.01)},
			},
		},
	}
	groups, err := evaluation.CostAttributionByPolicyVersion(evaluation.CostAttributionRunsFrom(runs))
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.NotNil(t, groups[0].Provider)
	assert.Equal(t, "anthropic", *groups[0].Provider)

	badRuns := []evaluation.SteadyStateRunInput{
		{
			RunID:         "run_bad",
			Repo:          covRepo,
			PolicyVersion: 17,
			Decisions:     10,
			Usages:        []evaluation.CostAttributionUsage{{Role: "writer", Model: "m", Provider: covStrPtr("")}},
		},
	}
	_, err = evaluation.CostAttributionByPolicyVersion(evaluation.CostAttributionRunsFrom(badRuns))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider")
}

func covStrPtr(s string) *string { return &s }

func TestCOV_EVAL_SteadyStateSummaryBandNaN(t *testing.T) {
	row := makeSteadyRow(t, covRepo, 17, []evaluation.SteadyStateRunInput{
		{RunID: "r1", Repo: covRepo, PolicyVersion: 17, Decisions: 10, Abstains: 5},
	})

	_, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
		Rows:              []*evaluation.SteadyStateRow{row},
		AbstainHealthBand: &evaluation.AbstainHealthBand{MinAbstainRate: covF64(math.NaN())},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minAbstainRate")

	_, err = evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
		Rows:              []*evaluation.SteadyStateRow{row},
		AbstainHealthBand: &evaluation.AbstainHealthBand{MaxAbstainRate: covF64(math.Inf(1))},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maxAbstainRate")
}

func TestCOV_EVAL_RenderDashboardErrorPath(t *testing.T) {
	row := makeSteadyRow(t, covRepo, 17, []evaluation.SteadyStateRunInput{
		{RunID: "r1", Repo: covRepo, PolicyVersion: 17, Decisions: 10, Abstains: 5},
	})
	markdown := evaluation.RenderSteadyStateDashboard(
		[]*evaluation.SteadyStateRow{row},
		&evaluation.AbstainHealthBand{MinAbstainRate: covF64(0.8), MaxAbstainRate: covF64(0.2)},
	)
	assert.Contains(t, markdown, "refused to summarize")
	assert.Contains(t, markdown, "min (0.8) exceeds max (0.2)")
}

func makeSteadyRow(t *testing.T, repo string, policyVersion int, runs []evaluation.SteadyStateRunInput) *evaluation.SteadyStateRow {
	t.Helper()
	row, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
		Repo:          repo,
		PolicyVersion: policyVersion,
		Runs:          runs,
	})
	require.NoError(t, err)
	return row
}

func TestCOV_EVAL_Mulberry32Determinism(t *testing.T) {
	a := evaluation.Mulberry32(42)
	b := evaluation.Mulberry32(42)
	assert.Equal(t, a(), b())
	c := evaluation.Mulberry32(43)
	assert.NotEqual(t, a(), c())
}
