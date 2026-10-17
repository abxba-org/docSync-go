package evaluation

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func asNumberVal(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := asNumber(v)
	require.True(t, ok)
	return f
}

func TestCOV_EVAL_Internal_AsNumberAllTypes(t *testing.T) {
	assert.Equal(t, 1.5, asNumberVal(t, 1.5))
	assert.Equal(t, 2.25, asNumberVal(t, float32(2.25)))
	assert.Equal(t, 3.0, asNumberVal(t, 3))
	assert.Equal(t, 4.0, asNumberVal(t, int8(4)))
	assert.Equal(t, 5.0, asNumberVal(t, int16(5)))
	assert.Equal(t, 6.0, asNumberVal(t, int32(6)))
	assert.Equal(t, 7.0, asNumberVal(t, int64(7)))
	assert.Equal(t, 8.0, asNumberVal(t, uint(8)))
	assert.Equal(t, 9.0, asNumberVal(t, uint8(9)))
	assert.Equal(t, 10.0, asNumberVal(t, uint16(10)))
	assert.Equal(t, 11.0, asNumberVal(t, uint32(11)))
	assert.Equal(t, 12.0, asNumberVal(t, uint64(12)))
	assert.Equal(t, 13.5, asNumberVal(t, json.Number("13.5")))
	_, ok := asNumber(json.Number("not-a-number"))
	assert.False(t, ok)
	_, ok = asNumber(math.NaN())
	assert.False(t, ok)
	_, ok = asNumber(math.Inf(1))
	assert.False(t, ok)
	_, ok = asNumber(math.Inf(-1))
	assert.False(t, ok)
	_, ok = asNumber("not-a-number")
	assert.False(t, ok)
	_, ok = asNumber(nil)
	assert.False(t, ok)
}

func TestCOV_EVAL_Internal_NumberToString(t *testing.T) {
	assert.Equal(t, "7", numberToString(7))
	assert.Equal(t, "0", numberToString(0))
	assert.Equal(t, "-3", numberToString(-3))
	assert.Equal(t, "0.94", numberToString(0.94))
	assert.Equal(t, "1.5", numberToString(1.5))
	assert.Equal(t, "12345", numberToString(12345))
	assert.Equal(t, "1e+21", numberToString(1e21))
}

func TestCOV_EVAL_Internal_AsStringAndGetPath(t *testing.T) {
	_, ok := asString("")
	assert.False(t, ok)
	_, ok = asString(5)
	assert.False(t, ok)
	s, ok := asString("hello")
	require.True(t, ok)
	assert.Equal(t, "hello", s)

	record := map[string]any{"a": map[string]any{"b": "c"}}
	v, ok := getPath(record, "a", "b")
	require.True(t, ok)
	assert.Equal(t, "c", v)
	_, ok = getPath(record, "a", "missing")
	assert.False(t, ok)
	_, ok = getPath(record, "none", "b")
	assert.False(t, ok)
	bad := map[string]any{"a": "not-a-record"}
	_, ok = getPath(bad, "a", "b")
	assert.False(t, ok)
}

func TestCOV_EVAL_Internal_FirstExtractors(t *testing.T) {
	s, ok := firstString(map[string]any{"x": ""}, []string{"x"})
	assert.False(t, ok)
	s, ok = firstString(map[string]any{"x": 5}, []string{"x"})
	assert.False(t, ok)
	s, ok = firstString(map[string]any{"x": "hi"}, []string{"x"}, []string{"y"})
	require.True(t, ok)
	assert.Equal(t, "hi", s)

	n, ok := firstNumber(map[string]any{"x": "hi"}, []string{"x"})
	assert.False(t, ok)
	n, ok = firstNumber(map[string]any{"x": 4.5}, []string{"x"})
	require.True(t, ok)
	assert.Equal(t, 4.5, n)

	str, ok := firstNumberOrString(map[string]any{"m": 3}, []string{"m"})
	require.True(t, ok)
	assert.Equal(t, "3", str)
	str, ok = firstNumberOrString(map[string]any{"m": "v3"}, []string{"m"})
	require.True(t, ok)
	assert.Equal(t, "v3", str)
}

func TestCOV_EVAL_Internal_IsAllDigits(t *testing.T) {
	assert.True(t, isAllDigits("42"))
	assert.True(t, isAllDigits("0"))
	assert.False(t, isAllDigits(""))
	assert.False(t, isAllDigits("abc"))
	assert.False(t, isAllDigits("42a"))
	assert.False(t, isAllDigits("-1"))
}

func TestCOV_EVAL_Internal_IsAbstainedOnDocs(t *testing.T) {
	assert.True(t, isAbstainedOnDocs(map[string]any{"docs_affected": false}))
	assert.False(t, isAbstainedOnDocs(map[string]any{"docs_affected": true}))
	assert.False(t, isAbstainedOnDocs(map[string]any{}))
	assert.True(t, isAbstainedOnDocs(map[string]any{
		"documents": []any{
			map[string]any{"policy_outcome": "ignore"},
			map[string]any{"policy_outcome": "ignore"},
		},
	}))
	assert.False(t, isAbstainedOnDocs(map[string]any{
		"documents": []any{
			map[string]any{"policy_outcome": "ignore"},
			map[string]any{"policy_outcome": "auto"},
		},
	}))
	assert.True(t, isAbstainedOnDocs(map[string]any{
		"documents": []any{map[string]any{"policy_outcome": "ignore", "decision_id": "d"}},
	}))
	assert.False(t, isAbstainedOnDocs(map[string]any{
		"documents": []any{
			map[string]any{"policy_outcome": "ignore"},
			map[string]any{"policy_outcome": "abstain"},
		},
	}))
}

func TestCOV_EVAL_Internal_DerivePrKeys(t *testing.T) {
	assert.Equal(t, []string{"7"}, derivePrKeys(map[string]any{"pr_number": 7}))
	assert.Equal(t, []string{"src-main"}, derivePrKeys(map[string]any{"source": "src-main"}))
	assert.Equal(t, []string{"42"}, derivePrKeys(map[string]any{"concurrency_group": "docsync-acme-42"}))
	assert.Equal(t, []string{"42", "src-main"}, derivePrKeys(map[string]any{"pr_number": 42, "source": "src-main", "concurrency_group": "docsync-acme-42"}))
	assert.Empty(t, derivePrKeys(map[string]any{"concurrency_group": "docsync-acme-abc"}))
	assert.Empty(t, derivePrKeys(map[string]any{}))
}

func TestCOV_EVAL_Internal_SortedUniqueAndDedupe(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, sortedUnique([]string{"c", "a", "b", "a"}))
	assert.Equal(t, []string{}, sortedUnique(nil))
	assert.Equal(t, []string{"x", "y"}, dedupePreserveOrder([]string{"x", "y", "x"}))
	assert.Equal(t, []string{}, dedupePreserveOrder(nil))
}

func TestCOV_EVAL_Internal_MetricsHelpers(t *testing.T) {
	assert.Equal(t, "NaN", jsNumber(math.NaN()))
	assert.Equal(t, "Infinity", jsNumber(math.Inf(1)))
	assert.Equal(t, "-Infinity", jsNumber(math.Inf(-1)))
	assert.Equal(t, "1.5", jsNumber(1.5))
	assert.Equal(t, "1", jsNumber(1))

	err := requireInt("x", 0, 1)
	require.Error(t, err)
	assert.NoError(t, requireInt("x", 1, 1))

	err = requireNumber("n", math.NaN(), 0)
	require.Error(t, err)
	err = requireNumber("n", math.Inf(1), 0)
	require.Error(t, err)
	err = requireNumber("n", math.Inf(-1), 0)
	require.Error(t, err)
	err = requireNumber("n", -1, 0)
	require.Error(t, err)
	assert.NoError(t, requireNumber("n", 0.5, 0))

	assert.True(t, math.IsNaN(numOrNaN(nil)))
	assert.Equal(t, 4.2, numOrNaN(float64Ptr(4.2)))

	assert.Equal(t, 0.0, mean(nil))
	assert.Equal(t, 2.5, mean([]float64{1, 2, 3, 4}))

	assert.Equal(t, 1.0, share(0, 0))
	assert.Equal(t, 1.0, share(5, 5))
	assert.Equal(t, 0.5, share(1, 2))
}

func float64Ptr(v float64) *float64 { return &v }

func TestCOV_EVAL_Internal_ObjectiveHelpers(t *testing.T) {
	err := requireNonNegative("w", math.NaN())
	require.Error(t, err)
	err = requireNonNegative("w", math.Inf(1))
	require.Error(t, err)
	err = requireNonNegative("w", math.Inf(-1))
	require.Error(t, err)
	err = requireNonNegative("w", -0.1)
	require.Error(t, err)
	assert.NoError(t, requireNonNegative("w", 0))
	assert.NoError(t, requireNonNegative("w", 0.5))

	err = requireUnitInterval("m", math.NaN())
	require.Error(t, err)
	err = requireUnitInterval("m", math.Inf(1))
	require.Error(t, err)
	err = requireUnitInterval("m", -0.1)
	require.Error(t, err)
	err = requireUnitInterval("m", 1.5)
	require.Error(t, err)
	assert.NoError(t, requireUnitInterval("m", 0))
	assert.NoError(t, requireUnitInterval("m", 1))
}

func TestCOV_EVAL_Internal_ProviderOrEmpty(t *testing.T) {
	assert.Equal(t, "", providerOrEmpty(nil))
	p := ptrStr("anthropic")
	assert.Equal(t, "anthropic", providerOrEmpty(p))
}

func ptrStr(s string) *string { return &s }

func TestCOV_EVAL_Internal_BuilderDefaultsAndNilPaths(t *testing.T) {
	b, err := NewDatasetBuilder(DatasetBuilderOptions{Clock: func() int64 { return 1 }})
	require.NoError(t, err)
	assert.NotNil(t, b)

	rec := map[string]any{"n": nil, "m": "text"}
	_, ok := firstNumber(rec, []string{"n"})
	assert.False(t, ok)
	_, ok = firstNumberOrString(rec, []string{"n"})
	assert.False(t, ok)
	_, ok = firstNumberOrString(map[string]any{"x": 1}, []string{"missing"})
	assert.False(t, ok)
}

func TestCOV_EVAL_Internal_ExtractDocPathsBadRows(t *testing.T) {
	paths := extractDocPaths(map[string]any{
		"documents": []any{"not-a-record", map[string]any{"doc_path": "docs/a.md"}},
	})
	assert.Equal(t, []string{"docs/a.md"}, paths)
	paths = extractDocPaths(map[string]any{
		"applied": []any{map[string]any{"doc_path": 5}},
	})
	assert.Empty(t, paths)
}

func TestCOV_EVAL_Internal_IsAbstainedOnDocsBadRows(t *testing.T) {
	assert.False(t, isAbstainedOnDocs(map[string]any{
		"documents": []any{"not-a-record"},
	}))
	assert.False(t, isAbstainedOnDocs(map[string]any{
		"documents": []any{map[string]any{"decision_id": "d"}},
	}))
}

func TestCOV_EVAL_Internal_SchemaIssuesEmptyPath(t *testing.T) {
	out := schemaIssues([]schemas.Issue{{Message: "root issue"}})
	assert.Equal(t, "<root>: root issue", out)
	out = schemaIssues([]schemas.Issue{{Path: []string{"a"}, Message: "m"}})
	assert.Equal(t, "a: m", out)
}

func TestCOV_EVAL_Internal_MetricsValidationBranches(t *testing.T) {
	_, err := ComputeMetrics(ComputeMetricsInput{PolicyVersion: 17, SplitID: "", ComputedAt: "2026-10-08T12:00:00Z",
		Overrides: OverrideCounts{OverrideCount: 0, DecisionCount: 1},
		Cases: []CaseEvaluation{{CaseID: "c1", Outcome: CaseOutcomeUpdated, WasCorrectUpdate: boolPtr(true),
			AnchorMatched: boolPtr(true), ShippedHunks: float64Ptr(1), TriggeringDiffLines: float64Ptr(4), EditDistance: float64Ptr(1)}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "splitId must be a non-empty string")

	_, err = ComputeMetrics(ComputeMetricsInput{PolicyVersion: 17, SplitID: "s", ComputedAt: "2026-10-08T12:00:00Z",
		Overrides: OverrideCounts{OverrideCount: 0, DecisionCount: 0},
		Cases: []CaseEvaluation{{CaseID: "c1", Outcome: CaseOutcomeUpdated, WasCorrectUpdate: boolPtr(true),
			AnchorMatched: boolPtr(true), ShippedHunks: float64Ptr(1), TriggeringDiffLines: float64Ptr(4), EditDistance: float64Ptr(1)}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decisionCount")

	_, err = ComputeMetrics(ComputeMetricsInput{PolicyVersion: 17, SplitID: "s", ComputedAt: "2026-10-08T12:00:00Z",
		Overrides: OverrideCounts{OverrideCount: -1, DecisionCount: 1},
		Cases: []CaseEvaluation{{CaseID: "c1", Outcome: CaseOutcomeUpdated, WasCorrectUpdate: boolPtr(true),
			AnchorMatched: boolPtr(true), ShippedHunks: float64Ptr(1), TriggeringDiffLines: float64Ptr(4), EditDistance: float64Ptr(1)}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overrideCount")

	_, err = ComputeMetrics(ComputeMetricsInput{PolicyVersion: 17, BaselineVersion: intPtr(0), SplitID: "s", ComputedAt: "2026-10-08T12:00:00Z",
		Overrides: OverrideCounts{OverrideCount: 0, DecisionCount: 1},
		Cases: []CaseEvaluation{{CaseID: "c1", Outcome: CaseOutcomeUpdated, WasCorrectUpdate: boolPtr(true),
			AnchorMatched: boolPtr(true), ShippedHunks: float64Ptr(1), TriggeringDiffLines: float64Ptr(4), EditDistance: float64Ptr(1)}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baselineVersion")
}

func intPtr(v int) *int { return &v }

func boolPtr(b bool) *bool { return &b }

func constRng(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = 7
	}
	return out
}

func TestCOV_EVAL_Internal_MonitoringRequireHelpers(t *testing.T) {
	assert.Error(t, requireNonEmptyString("name", ""))
	assert.NoError(t, requireNonEmptyString("name", "x"))

	assert.Error(t, requireCount("n", -1))
	assert.NoError(t, requireCount("n", 0))

	assert.Error(t, requirePositiveInt("n", 0))
	assert.Error(t, requirePositiveInt("n", -3))
	assert.NoError(t, requirePositiveInt("n", 1))

	assert.Error(t, requireFiniteNonNegative("c", math.NaN()))
	assert.Error(t, requireFiniteNonNegative("c", math.Inf(1)))
	assert.Error(t, requireFiniteNonNegative("c", math.Inf(-1)))
	assert.Error(t, requireFiniteNonNegative("c", -0.5))
	assert.NoError(t, requireFiniteNonNegative("c", 0))
	assert.NoError(t, requireFiniteNonNegative("c", 1.5))

	assert.Equal(t, 0.0, shareOf(0, 0))
	assert.Equal(t, 0.0, shareOf(0, 5))
	assert.Equal(t, 0.5, shareOf(1, 2))
	assert.Equal(t, 1.0, shareOf(5, 5))
}

func TestCOV_EVAL_Internal_ObjectiveWeightsEachField(t *testing.T) {
	_, err := NormalizeObjectiveWeights(ObjectiveWeights{Precision: 0.2, Recall: math.NaN(), PatchCorrectness: 0.3, Efficiency: 0.1})
	require.Error(t, err)
	_, err = NormalizeObjectiveWeights(ObjectiveWeights{Precision: 0.2, Recall: 0.3, PatchCorrectness: -0.1, Efficiency: 0.1})
	require.Error(t, err)
	_, err = NormalizeObjectiveWeights(ObjectiveWeights{Precision: 0.2, Recall: 0.3, PatchCorrectness: 0.3, Efficiency: math.Inf(1)})
	require.Error(t, err)
}

func TestCOV_EVAL_Internal_DatasetBuilderNilRandBytesGeneratesULID(t *testing.T) {
	b, err := NewDatasetBuilder(DatasetBuilderOptions{Clock: func() int64 { return 1_700_000_000_000 }})
	require.NoError(t, err)
	id, err := b.newCaseID()
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestCOV_EVAL_Internal_ExtractDecisionIdsNonRecordRow(t *testing.T) {
	ids := extractDecisionIds(map[string]any{
		"documents": []any{42, map[string]any{"decision_id": "d"}},
	})
	assert.Equal(t, []string{"d"}, ids)
}

func TestCOV_EVAL_Internal_MetricsVerifierCausedSort(t *testing.T) {
	_, err := ComputeMetrics(ComputeMetricsInput{PolicyVersion: 17, SplitID: "s", ComputedAt: "2026-10-08T12:00:00Z",
		Overrides: OverrideCounts{OverrideCount: 0, DecisionCount: 2},
		Cases: []CaseEvaluation{
			{CaseID: "c1", Outcome: CaseOutcomeUpdated, WasCorrectUpdate: boolPtr(true),
				AnchorMatched: boolPtr(true), ShippedHunks: float64Ptr(1), TriggeringDiffLines: float64Ptr(4), EditDistance: float64Ptr(1),
				VerifierCausedReject: boolPtr(true)},
			{CaseID: "c0", Outcome: CaseOutcomeUpdated, WasCorrectUpdate: boolPtr(true),
				AnchorMatched: boolPtr(true), ShippedHunks: float64Ptr(1), TriggeringDiffLines: float64Ptr(4), EditDistance: float64Ptr(1),
				VerifierCausedReject: boolPtr(true)},
		}})
	require.Error(t, err)
}

func TestCOV_EVAL_Internal_CompareGroupsAllBranches(t *testing.T) {
	a := CostAttributionGroup{PolicyVersion: 1, Role: "writer", Model: "claude", Provider: nil}
	b := CostAttributionGroup{PolicyVersion: 2, Role: "writer", Model: "claude", Provider: nil}
	assert.Equal(t, -1, compareGroups(a, b))
	assert.Equal(t, 1, compareGroups(b, a))

	c := CostAttributionGroup{PolicyVersion: 1, Role: "judge", Model: "claude", Provider: nil}
	assert.Equal(t, 1, compareGroups(a, c))
	assert.Equal(t, -1, compareGroups(c, a))

	d := CostAttributionGroup{PolicyVersion: 1, Role: "writer", Model: "gpt4", Provider: nil}
	assert.Equal(t, -1, compareGroups(a, d))
	assert.Equal(t, 1, compareGroups(d, a))

	p1 := ptrStr("openai")
	p2 := ptrStr("anthropic")
	e := CostAttributionGroup{PolicyVersion: 1, Role: "writer", Model: "claude", Provider: p1}
	f := CostAttributionGroup{PolicyVersion: 1, Role: "writer", Model: "claude", Provider: p2}
	assert.Equal(t, 1, compareGroups(e, f))
	assert.Equal(t, -1, compareGroups(f, e))

	g := CostAttributionGroup{PolicyVersion: 1, Role: "writer", Model: "claude", Provider: nil}
	assert.Equal(t, -1, compareGroups(g, e))
	assert.Equal(t, 1, compareGroups(e, g))
}

func TestCOV_EVAL_Internal_ValidateUsageAllPaths(t *testing.T) {
	p := ptrStr("")
	err := validateUsage("run_1", 0, CostAttributionUsage{Role: "writer", Model: "m", Provider: p})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider")

	good := CostAttributionUsage{Role: "writer", Model: "m", PromptTokens: 100, CompletionTokens: 50,
		CostUsd: float64Ptr(0.01), LatencyMs: float64Ptr(200)}
	assert.NoError(t, validateUsage("run_1", 0, good))

	neg := float64Ptr(-1)
	assert.Error(t, validateUsage("run_1", 0, CostAttributionUsage{Role: "writer", Model: "m",
		PromptTokens: 100, CompletionTokens: 50, CostUsd: neg}))
}

func TestCOV_EVAL_Internal_WriteDatasetMkdirFailure(t *testing.T) {
	b, err := NewDatasetBuilder(DatasetBuilderOptions{Clock: func() int64 { return 1 }})
	require.NoError(t, err)
	_, err = b.WriteDataset(nil, WriteDatasetOptions{Name: "x", EvaluationDir: "/nonexistent/path"})
	require.Error(t, err)
}

func TestCOV_EVAL_Internal_NewDatasetBuilderCryptoFallback(t *testing.T) {
	b, err := NewDatasetBuilder(DatasetBuilderOptions{Clock: func() int64 { return 1 }})
	require.NoError(t, err)
	id, err := b.newCaseID()
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestCOV_EVAL_Internal_IngestAuditFindingsInjectedNowOnly(t *testing.T) {
	now := int64(1_700_000_000_000)
	labels, err := IngestAuditFindings([]AuditFinding{
		{DecisionID: "d_1", PolicyVersion: 17, Verdict: AuditVerdictPatchCorrect, At: "2026-10-08T12:00:00Z"},
	}, &IngestAuditFindingsOptions{NowMs: &now})
	require.NoError(t, err)
	require.Len(t, labels, 1)
	assert.Equal(t, schemas.HygieneLabelAccepted, labels[0].Label)
}

func TestCOV_EVAL_Internal_IngestAuditFindingsInjectedRandOnly(t *testing.T) {
	labels, err := IngestAuditFindings([]AuditFinding{
		{DecisionID: "d_1", PolicyVersion: 17, Verdict: AuditVerdictPatchCorrect, At: "2026-10-08T12:00:00Z"},
	}, &IngestAuditFindingsOptions{RandBytes: constRng})
	require.NoError(t, err)
	require.Len(t, labels, 1)
}

func TestCOV_EVAL_Internal_RenderDashboardCostEmptyAndOverflow(t *testing.T) {
	row := SteadyStateRow{
		Key: "repo@v1", Repo: "repo", PolicyVersion: 1, RunCount: 1, DecisionCount: 10,
		AbstainHealth: AbstainHealthSummary{AbstainRate: 0.2, OverrideRate: 0.1, Decisions: 10},
	}
	markdown := RenderSteadyStateDashboard([]*SteadyStateRow{&row}, nil)
	assert.Contains(t, markdown, "No usage was attributed")

	capped := make([]*SteadyStateRow, SteadyStateRenderCap+3)
	for i := 0; i < SteadyStateRenderCap+3; i++ {
		capped[i] = &SteadyStateRow{
			Key: fmt.Sprintf("repo-%d@v1", i), Repo: fmt.Sprintf("repo-%d", i), PolicyVersion: 1,
			RunCount: 1, DecisionCount: 10,
			AbstainHealth: AbstainHealthSummary{AbstainRate: 0.2, OverrideRate: 0.1, Decisions: 10},
		}
	}
	markdown = RenderSteadyStateDashboard(capped, nil)
	assert.Contains(t, markdown, "more row(s) omitted")
}

func TestCOV_EVAL_Internal_ComputeMetricsShareZeroUpdated(t *testing.T) {
	result, err := ComputeMetrics(ComputeMetricsInput{
		PolicyVersion: 17, SplitID: "s", ComputedAt: "2026-10-08T12:00:00Z",
		Overrides: OverrideCounts{OverrideCount: 0, DecisionCount: 2},
		Cases: []CaseEvaluation{
			{CaseID: "a1", Outcome: CaseOutcomeAbstained, WasCorrectAbstain: boolPtr(true)},
			{CaseID: "a2", Outcome: CaseOutcomeAbstained, WasCorrectAbstain: boolPtr(true)},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1.0, result.Report.Metrics.Precision)
	assert.Equal(t, 1.0, result.Report.Metrics.PatchCorrectness)
}

func TestCOV_EVAL_Internal_CompareStringsAllBranches(t *testing.T) {
	assert.Equal(t, -1, compareStrings("a", "b"))
	assert.Equal(t, 1, compareStrings("b", "a"))
	assert.Equal(t, 0, compareStrings("a", "a"))
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowEmptyRepo(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "", PolicyVersion: 1, Runs: []SteadyStateRunInput{{RunID: "r1", Repo: "", PolicyVersion: 1, Decisions: 1}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowPolicyVersionZero(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 0, Runs: []SteadyStateRunInput{{RunID: "r1", Repo: "r", PolicyVersion: 0, Decisions: 1}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policyVersion")
}

func TestCOV_EVAL_Internal_ComputeEfficiencyEdgeCases(t *testing.T) {
	v, err := ComputeEfficiency(UsageAccounting{TotalCostUsd: 10, TotalLatencyMs: 30000})
	require.NoError(t, err)
	assert.InDelta(t, 0.0, v, 1e-12)

	v, err = ComputeEfficiency(UsageAccounting{TotalCostUsd: 0, TotalLatencyMs: 0})
	require.NoError(t, err)
	assert.InDelta(t, 1.0, v, 1e-12)
}

func TestCOV_EVAL_Internal_SpotCheckSampleSizeFloorOnly(t *testing.T) {
	for _, batch := range []int{2, 3, 4} {
		size, err := SpotCheckSampleSize(batch)
		require.NoError(t, err)
		assert.Equal(t, 1, size)
	}
	size, err := SpotCheckSampleSize(100)
	require.NoError(t, err)
	assert.Equal(t, 10, size)
}

func TestCOV_EVAL_Internal_SampleAutoPatchesCountExceeds(t *testing.T) {
	patches := make([]AutoAppliedPatch, 5)
	for i := 0; i < 5; i++ {
		patches[i] = AutoAppliedPatch{DecisionID: fmt.Sprintf("d-%d", i)}
	}
	rng := Mulberry32(1)
	selected, err := SampleAutoPatches(patches, rng)
	require.NoError(t, err)
	assert.Len(t, selected, 1)
}

func TestCOV_EVAL_Internal_SteadyStateSummaryWithCostGroupOverflow(t *testing.T) {
	groups := make([]CostAttributionGroup, SteadyStateRenderCap+3)
	for i := 0; i < SteadyStateRenderCap+3; i++ {
		groups[i] = CostAttributionGroup{
			PolicyVersion: 1, Role: "writer", Model: fmt.Sprintf("model-%d", i),
			UsageCount: 1, RunIDs: []string{"r1"},
			PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
		}
	}
	row := &SteadyStateRow{
		Key: "r@v1", Repo: "r", PolicyVersion: 1, RunCount: 1, DecisionCount: 10, AbstainCount: 2,
		AbstainHealth: AbstainHealthSummary{AbstainRate: 0.2, OverrideRate: 0.1, Decisions: 10},
		CostAttribution: groups,
	}
	markdown := RenderSteadyStateDashboard([]*SteadyStateRow{row}, nil)
	assert.Contains(t, markdown, "more attribution row(s) omitted")
}

func TestCOV_EVAL_Internal_ComputeObjectiveScoreNegativeUnsafe(t *testing.T) {
	_, err := ComputeObjectiveScore(ComputeObjectiveScoreInput{
		Metrics:          ObjectiveMetricsInput{DetectionRecall: 0.5, Precision: 0.5, PatchCorrectness: 0.5},
		Usage:            UsageAccounting{TotalCostUsd: 1, TotalLatencyMs: 100},
		UnsafeAutoUpdates: -1,
	})
	require.Error(t, err)
}

func TestCOV_EVAL_Internal_RenderDashboardBreachedWithEmptyGroups(t *testing.T) {
	row := &SteadyStateRow{
		Key: "r@v1", Repo: "r", PolicyVersion: 1, RunCount: 1, DecisionCount: 10,
		AbstainCount: 5, OverrideCount: 3,
		UnsafeAutoUpdates: 1, Flags: []string{SteadyStateRowFlagUnsafeAutoUpdates},
		AbstainHealth: AbstainHealthSummary{
			AbstainRate: 0.5, OverrideRate: 0.3, Decisions: 10,
			Alert: abstainAlertPtr(AbstainHealthOverconfidence),
		},
		SpotCheck: SpotCheckSummary{
			AutoApplied: 4, Sampled: 1, Checked: 1, ExpectedSampleSize: 1,
		},
		FalsePositiveCount: 1, FalsePositiveRate: 0.1,
		FalseNegativeCount: 0, FalseNegativeRate: 0,
	}
	markdown := RenderSteadyStateDashboard([]*SteadyStateRow{row}, nil)
	assert.Contains(t, markdown, "FLOOR BREACHED")
	assert.Contains(t, markdown, "advisory")
}

func abstainAlertPtr(a AbstainHealthAlert) *AbstainHealthAlert {
	return &a
}

func TestCOV_EVAL_Internal_ParseInstantExtraFormats(t *testing.T) {
	ms, err := parseInstant("2026-10-08")
	require.NoError(t, err)
	assert.Equal(t, int64(1791417600000), ms)

	ms, err = parseInstant("not-a-date")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unparseable")
}

func TestCOV_EVAL_Internal_Mulberry32Determinism(t *testing.T) {
	rng := Mulberry32(42)
	v1 := rng()
	v2 := rng()
	v3 := Mulberry32(42)()
	assert.Equal(t, v1, v3, "same seed, same first value")
	_ = v2
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowRunKeyMismatchRepo(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "repo_a", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{
			{RunID: "r1", Repo: "repo_b", PolicyVersion: 1, Decisions: 10},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ONE repository at ONE policy version")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowRunKeyMismatchVersion(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "repo", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{
			{RunID: "r1", Repo: "repo", PolicyVersion: 2, Decisions: 10},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ONE repository at ONE policy version")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowContainmentMispredictions(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{
			{RunID: "r1", Repo: "r", PolicyVersion: 1, Decisions: 10, AutoApplied: 5, SpotCheckSampled: 2, SpotCheckChecked: 1, AuditMispredictions: 2},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auditMispredictions")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowContainmentFPFN(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{
			{RunID: "r1", Repo: "r", PolicyVersion: 1, Decisions: 10, FalsePositives: 6, FalseNegatives: 5},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "falsePositives + falseNegatives")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowContainmentOverrides(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{
			{RunID: "r1", Repo: "r", PolicyVersion: 1, Decisions: 10, Overrides: 11},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overrides")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowContainmentSampled(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{
			{RunID: "r1", Repo: "r", PolicyVersion: 1, Decisions: 10, AutoApplied: 5, SpotCheckSampled: 6},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spotCheckSampled")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowContainmentChecked(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{
			{RunID: "r1", Repo: "r", PolicyVersion: 1, Decisions: 10, AutoApplied: 5, SpotCheckSampled: 3, SpotCheckChecked: 4},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spotCheckChecked")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowContainmentAutoApplied(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{
			{RunID: "r1", Repo: "r", PolicyVersion: 1, Decisions: 10, AutoApplied: 15},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "autoApplied")
}

func TestCOV_EVAL_Internal_BuildSteadyStateSummaryNilRowBandMinOnly(t *testing.T) {
	row := &SteadyStateRow{
		Key: "r@v1", Repo: "r", PolicyVersion: 1, RunCount: 1, DecisionCount: 10, AbstainCount: 1,
		AbstainHealth: AbstainHealthSummary{AbstainRate: 0.1, OverrideRate: 0.05, Decisions: 10},
	}
	summary, err := BuildSteadyStateSummary(SteadyStateSummaryInput{
		Rows: []*SteadyStateRow{row},
		AbstainHealthBand: &AbstainHealthBand{MinAbstainRate: float64Ptr(0.2)},
	})
	require.NoError(t, err)
	require.Len(t, summary.AbstainHealth.DriftFlags, 1)
	assert.Equal(t, DriftDirectionBelowMin, summary.AbstainHealth.DriftFlags[0].Direction)

	summary, err = BuildSteadyStateSummary(SteadyStateSummaryInput{
		Rows: []*SteadyStateRow{row},
		AbstainHealthBand: &AbstainHealthBand{MaxAbstainRate: float64Ptr(0.05)},
	})
	require.NoError(t, err)
	require.Len(t, summary.AbstainHealth.DriftFlags, 1)
	assert.Equal(t, DriftDirectionAboveMax, summary.AbstainHealth.DriftFlags[0].Direction)
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowAutoApplyBatchZeroCausesNoSampleGap(t *testing.T) {
	row := &SteadyStateRow{
		Key: "r@v1", Repo: "r", PolicyVersion: 1, RunCount: 1, DecisionCount: 10,
		AbstainHealth: AbstainHealthSummary{AbstainRate: 0.1, OverrideRate: 0.05, Decisions: 10},
		SpotCheck: SpotCheckSummary{AutoApplied: 0, Sampled: 0, Checked: 0},
	}
	summary, err := BuildSteadyStateSummary(SteadyStateSummaryInput{Rows: []*SteadyStateRow{row}})
	require.NoError(t, err)
	require.Len(t, summary.SpotCheckCoverage.Gaps, 1)
	assert.Equal(t, SpotCheckGapNoAutoApplyBatch, summary.SpotCheckCoverage.Gaps[0].Reason)
}

func TestCOV_EVAL_Internal_RenderDashboardBandNotConfigured(t *testing.T) {
	row := &SteadyStateRow{
		Key: "r@v1", Repo: "r", PolicyVersion: 1, RunCount: 1, DecisionCount: 10,
		AbstainHealth: AbstainHealthSummary{AbstainRate: 0.1, OverrideRate: 0.05, Decisions: 10},
	}
	band := &AbstainHealthBand{}
	markdown := RenderSteadyStateDashboard([]*SteadyStateRow{row}, band)
	_ = markdown
}

func TestCOV_EVAL_Internal_BuildSteadyStateSummaryEmptyRowsList(t *testing.T) {
	_, err := BuildSteadyStateSummary(SteadyStateSummaryInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs at least one row")
}

func TestCOV_EVAL_Internal_BuildSteadyStateSummaryNilRow(t *testing.T) {
	validRow := &SteadyStateRow{
		Key: "r@v1", Repo: "r", PolicyVersion: 1, RunCount: 1, DecisionCount: 10,
		AbstainHealth: AbstainHealthSummary{AbstainRate: 0.1, OverrideRate: 0.05, Decisions: 10},
	}
	_, err := BuildSteadyStateSummary(SteadyStateSummaryInput{
		Rows: []*SteadyStateRow{validRow, nil},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index 1 is missing")
}

func TestCOV_EVAL_Internal_ComputeObjectiveScoreGuardClamps(t *testing.T) {
	result, err := ComputeObjectiveScore(ComputeObjectiveScoreInput{
		Metrics:          ObjectiveMetricsInput{DetectionRecall: 0, Precision: 0, PatchCorrectness: 0},
		Usage:            UsageAccounting{TotalCostUsd: 0, TotalLatencyMs: 0},
		UnsafeAutoUpdates: 0,
	})
	require.NoError(t, err)
	assert.InDelta(t, 0.1, result.Score, 1e-12)

	_, err = ComputeObjectiveScore(ComputeObjectiveScoreInput{
		Metrics: ObjectiveMetricsInput{DetectionRecall: 0.5, Precision: 0.5, PatchCorrectness: 0.5},
		Usage:   UsageAccounting{TotalCostUsd: -1, TotalLatencyMs: 100},
		UnsafeAutoUpdates: 0,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "totalCostUsd")
}

func TestCOV_EVAL_Internal_ValidateUsageLatencyNeg(t *testing.T) {
	neg := float64Ptr(-1)
	err := validateUsage("run_1", 0, CostAttributionUsage{Role: "writer", Model: "m",
		PromptTokens: 100, CompletionTokens: 50, LatencyMs: neg})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "latencyMs")
}

func TestCOV_EVAL_Internal_CostAttributionEmptyRole(t *testing.T) {
	runs := []CostAttributionRun{
		{RunID: "r1", PolicyVersion: 1, Usages: []CostAttributionUsage{{Role: "", Model: "m"}}},
	}
	_, err := CostAttributionByPolicyVersion(runs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role")
}

func TestCOV_EVAL_Internal_CostAttributionEmptyModel(t *testing.T) {
	runs := []CostAttributionRun{
		{RunID: "r1", PolicyVersion: 1, Usages: []CostAttributionUsage{{Role: "writer", Model: ""}}},
	}
	_, err := CostAttributionByPolicyVersion(runs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model")
}

func TestCOV_EVAL_Internal_SampleAutoPatchesCountGreaterThanLen(t *testing.T) {
	patches := make([]AutoAppliedPatch, 2)
	for i := 0; i < 2; i++ {
		patches[i] = AutoAppliedPatch{DecisionID: fmt.Sprintf("d-%d", i)}
	}
	rng := func() float64 { return 0.5 }
	selected, err := SampleAutoPatches(patches, rng)
	require.NoError(t, err)
	assert.Len(t, selected, 1)
}

func TestCOV_EVAL_Internal_ContaminationCheckEmptyTraining(t *testing.T) {
	result := ContaminationCheck([]string{"a", "b"}, nil)
	assert.True(t, result.Disjoint)
	assert.Empty(t, result.Violations)
}

func TestCOV_EVAL_Internal_SpotCheckSampleSizeRateExceeds(t *testing.T) {
	size, err := SpotCheckSampleSize(55)
	require.NoError(t, err)
	assert.Equal(t, 6, size)
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowEmptyRunID(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{{RunID: "", Repo: "r", PolicyVersion: 1, Decisions: 10}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runId")
}

func TestCOV_EVAL_Internal_BuildSteadyStateRowRunKeyMismatch(t *testing.T) {
	_, err := BuildSteadyStateRow(SteadyStateRowInput{
		Repo: "r", PolicyVersion: 1,
		Runs: []SteadyStateRunInput{{RunID: "r1", Repo: "other", PolicyVersion: 1, Decisions: 10}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ONE repository")
}
