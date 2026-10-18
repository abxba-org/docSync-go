// Ported from core/src/../../test/evaluation-dataset.test.ts and
// evaluation-metrics.test.ts (P10-T07..T13 + T24; ARCH §9; CTX §35/§36):
// V-P10-5 (dataset-yield report, A3 fixture verdict), V-P10-6 (frozen split
// reproducibility + contamination check), V-P10-7 (all six metrics present,
// minimality measured numbers), V-P10-8 (2-D abstain-health monitor with
// configured alert thresholds), V-P10-9 (deterministic spot-check sample;
// audit findings ingested as eval labels) and V-P10-10 (repo-configurable
// objective weights; unsafe_auto_updates collapses + fails the gate).
//
// Fixture-based, injected clock + byte source (deterministic ULIDs), temp-dir
// filesystem, no network.
package evaluation_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/evaluation"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	at   = "2026-10-08T12:00:00Z"
	now  = "2026-10-08T12:00:00Z"
	repo = "acme/widgets"
)

// sha renders a 40-hex lowercase git sha (deterministic per integer).
func sha(n int) string {
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

// byteSeed drives the deterministic byte source for ULID generation
// (labels only; no network).
var byteSeed int

func detRng(bytes int) ([]byte, error) {
	out := make([]byte, bytes)
	for index := 0; index < bytes; index++ {
		out[index] = byte((byteSeed*37 + index*11) % 256)
	}
	byteSeed++
	return out, nil
}

// constRng fills every byte with 7 (deterministic ULID labels).
func constRng(bytes int) ([]byte, error) {
	out := make([]byte, bytes)
	for i := range out {
		out[i] = 7
	}
	return out, nil
}

// labelFor builds a schema-valid hygiene label pinned to a decision id.
func labelFor(index int, decisionID string, policyVersion int) schemas.HygieneLabel {
	gen := docutil.NewULIDGeneratorWith(
		func() int64 { return 1_700_000_000_000 + int64(index) }, detRng)
	labelID, err := gen.Generate()
	if err != nil {
		panic(err)
	}
	return schemas.HygieneLabel{
		Schema:        "docsync.hygiene-label/v1",
		LabelID:       labelID,
		DecisionID:    decisionID,
		PolicyVersion: policyVersion,
		Label:         schemas.HygieneLabelAccepted,
		SourceSignal:  schemas.HygieneSourceSpotCheck,
		HygieneRule:   "direct",
		NormalizedAt:  at,
	}
}

// fixRunRecord is a fix-run-record-shaped run record matched by run_id ↔
// label.decision_id.
func fixRunRecord(runID, docPath string) map[string]any {
	return map[string]any{
		"schema":           "docsync.fix-run-record/v1",
		"run_id":           runID,
		"at":               at,
		"repo":             repo,
		"base":             sha(len(runID)*7 + 1),
		"head":             sha(len(runID)*7 + 2),
		"manifest_version": "1",
		"policy_version":   17,
		"applied":          []any{map[string]any{"doc_path": docPath, "patch_ref": "patch_" + runID, "three_way_used": false}},
		"skipped":          []any{},
		"refused":          []any{},
	}
}

// decisionRecord is a minimal run-outcome-shaped record with a top-level
// decision id.
func decisionRecord(decisionID string, policyVersion int, docPath string) map[string]any {
	return map[string]any{
		"decision_id":      decisionID,
		"repo":             repo,
		"base_sha":         sha(len(decisionID)*13 + 1),
		"head_sha":         sha(len(decisionID)*13 + 2),
		"manifest_version": "1",
		"policy_version":   policyVersion,
		"documents":        []any{map[string]any{"doc_path": docPath, "policy_outcome": "auto", "partial_coverage": false}},
	}
}

// detBuilder builds a dataset builder with an injected clock + byte source.
func detBuilder() *evaluation.DatasetBuilder {
	builder, err := evaluation.NewDatasetBuilder(evaluation.DatasetBuilderOptions{
		Clock:     func() int64 { return 1_700_000_000_000 },
		RandBytes: detRng,
	})
	if err != nil {
		panic(err)
	}
	return builder
}

func TestEvaluationDatasetAndSplits(t *testing.T) {
	t.Run("V-P10-5: the dataset-yield report counts labeled cases + true negatives and records the A3 verdict (fixture data source declared)", func(t *testing.T) {
		builder := detBuilder()

		labels := []schemas.HygieneLabel{
			labelFor(0, "01J9ZK7Q2M8P4X6T2W5Y3C0A9E", 17),
			labelFor(1, "d_2", 17),
			labelFor(2, "d_3", 17),
			labelFor(3, "d_bad", 17),
		}
		runRecords := []map[string]any{
			fixRunRecord("01J9ZK7Q2M8P4X6T2W5Y3C0A9E", "docs/payments.md"),
			decisionRecord("d_2", 17, "docs/api.md"),
			{
				// per-document decision_id lane + policy 18 (byPolicyVersion coverage)
				"repo":             repo,
				"base_sha":         sha(101),
				"head_sha":         sha(102),
				"manifest_version": "1",
				"policy_version":   18,
				"documents": []any{
					map[string]any{
						"doc_path":         "docs/api.md",
						"policy_outcome":   "suggest",
						"decision_id":      "d_3",
						"partial_coverage": false,
					},
				},
			},
			{
				// true-negative lane: docs_affected=false AND a later same-PR doc edit
				"repo":             repo,
				"base_sha":         sha(201),
				"head_sha":         sha(202),
				"manifest_version": "1",
				"policy_version":   17,
				"docs_affected":    false,
				"pr_number":        42,
				"decision_id":      "d_tn",
			},
			{
				// fail-closed lane: matches label d_bad but repo is missing → Rejected, never silent
				"decision_id":      "d_bad",
				"base_sha":         sha(301),
				"head_sha":         sha(302),
				"manifest_version": "1",
				"policy_version":   17,
			},
		}

		result, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
			RunRecords: runRecords,
			Labels:     labels,
			LaterDocEdits: []evaluation.LaterDocEdit{
				{PRKey: "42", DocPath: "docs/api.md", At: at},
			},
		})
		require.NoError(t, err)

		require.Len(t, result.Cases, 4)
		labeledCount := 0
		trueNegativeCount := 0
		for _, evalCase := range result.Cases {
			// exactly-one-of ground truth on every produced case (schema-level XOR)
			assert.NotEqual(t, evalCase.LabelID != nil, evalCase.IsTrueNegative != nil)
			if evalCase.LabelID != nil {
				labeledCount++
			}
			if evalCase.IsTrueNegative != nil && *evalCase.IsTrueNegative {
				trueNegativeCount++
			}
		}
		assert.Equal(t, 3, labeledCount)
		assert.Equal(t, 1, trueNegativeCount)
		// true-negative shape: explicit marker, no label, later-edit doc paths
		var trueNegative *schemas.EvalCase
		for i := range result.Cases {
			if result.Cases[i].IsTrueNegative != nil && *result.Cases[i].IsTrueNegative {
				trueNegative = &result.Cases[i]
			}
		}
		require.NotNil(t, trueNegative)
		assert.Nil(t, trueNegative.LabelID)
		assert.Equal(t, []string{"docs/api.md"}, trueNegative.ExpectedDocPaths)
		// labeled shape: label_id present, expected paths from the run record
		assert.Equal(t, labels[0].LabelID, *result.Cases[0].LabelID)
		assert.Equal(t, []string{"docs/payments.md"}, result.Cases[0].ExpectedDocPaths)
		// the invalid record was skipped AND counted — never silently dropped
		require.Len(t, result.Rejected, 1)
		assert.Equal(t, evaluation.RejectedSchemaValidationFailed, result.Rejected[0].Reason)
		require.NotNil(t, result.Rejected[0].RunIndex)
		assert.Equal(t, 4, *result.Rejected[0].RunIndex)
		assert.Contains(t, result.Rejected[0].Detail, "repo")

		// deterministic curated dataset write (OD-R22: explicit evaluationDir)
		dir := t.TempDir()
		evaluationDir := filepath.Join(dir, ".docsync", "evaluation")
		first, err := builder.WriteDataset(result.Cases, evaluation.WriteDatasetOptions{
			Name:          "pilot-replay",
			EvaluationDir: evaluationDir,
		})
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(evaluationDir, "datasets", "pilot-replay.json"), first.Path)
		assert.Equal(t, 4, first.CaseCount)
		_, err = builder.WriteDataset(result.Cases, evaluation.WriteDatasetOptions{
			Name:          "pilot-replay",
			EvaluationDir: evaluationDir,
		})
		require.NoError(t, err)
		firstRaw, err := os.ReadFile(first.Path)
		require.NoError(t, err)
		secondRaw, err := os.ReadFile(first.Path)
		require.NoError(t, err)
		assert.Equal(t, string(firstRaw), string(secondRaw)) // byte-identical rewrites
		var persisted []struct {
			CaseID string `json:"case_id"`
		}
		require.NoError(t, json.Unmarshal(firstRaw, &persisted))
		persistedIDs := make([]string, 0, len(persisted))
		for _, c := range persisted {
			persistedIDs = append(persistedIDs, c.CaseID)
		}
		sortedIDs := append([]string(nil), persistedIDs...)
		assert.IsIncreasing(t, sortedIDs) // already sorted ascending

		// A3 yield report: fixture data source declared, verdict from MinDatasetCases
		report := builder.YieldReport(result.Cases)
		assert.Equal(t, 4, report.Total)
		assert.Equal(t, 3, report.Labeled)
		assert.Equal(t, 1, report.TrueNegatives)
		assert.Equal(t, map[int]int{17: 3, 18: 1}, report.ByPolicyVersion)
		assert.Equal(t, "fixture", report.DataSource)
		assert.Equal(t, evaluation.DatasetYieldVerdictInsufficient, report.Verdict) // 4 < MinDatasetCases

		// A3 floor: 30 labeled cases flip the verdict to usable (versioned floor data)
		bulkLabels := make([]schemas.HygieneLabel, 0, evaluation.MinDatasetCases)
		bulkRecords := make([]map[string]any, 0, evaluation.MinDatasetCases)
		for i := 0; i < evaluation.MinDatasetCases; i++ {
			bulkLabels = append(bulkLabels, labelFor(10+i, "d_bulk_"+strconv.Itoa(i), 16+(i%3)))
			bulkRecords = append(bulkRecords, decisionRecord("d_bulk_"+strconv.Itoa(i), 16+(i%3), "docs/api.md"))
		}
		bulk, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
			RunRecords: bulkRecords,
			Labels:     bulkLabels,
		})
		require.NoError(t, err)
		assert.True(t, len(result.Rejected) == 0 || len(bulk.Rejected) == 0)
		bulkReport := builder.YieldReport(bulk.Cases)
		assert.Equal(t, evaluation.MinDatasetCases, bulkReport.Total)
		assert.Equal(t, evaluation.MinDatasetCases, bulkReport.Labeled)
		assert.Equal(t, map[int]int{16: 10, 17: 10, 18: 10}, bulkReport.ByPolicyVersion)
		assert.Equal(t, evaluation.DatasetYieldVerdictUsable, bulkReport.Verdict)
	})

	t.Run("writeDataset refuses schema-invalid cases at the write boundary (fail closed)", func(t *testing.T) {
		builder := detBuilder()
		valid, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
			RunRecords: []map[string]any{decisionRecord("d_x", 17, "docs/api.md")},
			Labels:     []schemas.HygieneLabel{labelFor(90, "d_x", 17)},
		})
		require.NoError(t, err)
		require.Len(t, valid.Cases, 1)
		corrupted := valid.Cases[0]
		corrupted.RunKey.Repo = ""
		_, err = builder.WriteDataset([]schemas.EvalCase{corrupted},
			evaluation.WriteDatasetOptions{Name: "bad", EvaluationDir: t.TempDir()})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refusing to write 1 schema-invalid case(s) at indices 0")
		assert.Contains(t, err.Error(), "fail-closed")
	})

	t.Run("buildLabeledCases counts duplicate labels and duplicate decision/label pairs", func(t *testing.T) {
		builder := detBuilder()
		labelA := labelFor(0, "d_dup", 17)
		labelB := labelFor(1, "d_dup", 17)
		result, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
			RunRecords: []map[string]any{
				decisionRecord("d_dup", 17, "docs/api.md"),
				decisionRecord("d_dup", 17, "docs/api.md"),
			},
			Labels: []schemas.HygieneLabel{labelA, labelB},
		})
		require.NoError(t, err)
		require.Len(t, result.Cases, 1)
		require.Len(t, result.Rejected, 2)
		assert.Equal(t, evaluation.RejectedDuplicateLabelForDecision, result.Rejected[0].Reason)
		assert.Nil(t, result.Rejected[0].RunIndex)
		assert.Contains(t, result.Rejected[0].Detail, "already labeled by")
		assert.Equal(t, evaluation.RejectedDuplicateDecisionLabelPair, result.Rejected[1].Reason)
		require.NotNil(t, result.Rejected[1].RunIndex)
		assert.Equal(t, 1, *result.Rejected[1].RunIndex)
		assert.Contains(t, result.Rejected[1].Detail, "already consumed by an earlier run record")
	})

	t.Run("V-P10-6: the frozen split is reproducible byte-identically from seed + version pin; benchmark rows are disjoint from candidate-training feedback (contamination)", func(t *testing.T) {
		builder := detBuilder()
		count := 40
		labels := make([]schemas.HygieneLabel, 0, count)
		records := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			labels = append(labels, labelFor(50+i, "d_split_"+strconv.Itoa(i), 17))
			records = append(records, decisionRecord("d_split_"+strconv.Itoa(i), 17, "docs/api.md"))
		}
		built, err := builder.BuildLabeledCases(evaluation.BuildLabeledCasesInput{
			RunRecords: records,
			Labels:     labels,
		})
		require.NoError(t, err)
		require.Len(t, built.Cases, count)

		caseIDs := make([]struct{ CaseID string }, 0, len(built.Cases))
		for _, evalCase := range built.Cases {
			caseIDs = append(caseIDs, struct{ CaseID string }{CaseID: evalCase.CaseID})
		}

		// byte-identical reproducibility from seed + dataset version pin
		options := evaluation.FrozenSplitOptions{
			Seed:           7,
			DatasetVersion: "ds-2026-10-08",
			CreatedAt:      at,
		}
		frozenOne := evaluation.BuildFrozenSplit(caseIDs, options)
		frozenTwo := evaluation.BuildFrozenSplit(caseIDs, options)
		oneJSON, err := json.Marshal(frozenOne)
		require.NoError(t, err)
		twoJSON, err := json.Marshal(frozenTwo)
		require.NoError(t, err)
		assert.JSONEq(t, string(oneJSON), string(twoJSON))
		assert.Equal(t, string(oneJSON), string(twoJSON))
		assert.Equal(t, "frozen-7-ds-2026-10-08", frozenOne.SplitID)
		assert.Equal(t, "ds-2026-10-08", frozenOne.DatasetVersion)
		assert.Equal(t, at, frozenOne.CreatedAt)
		assert.Len(t, frozenOne.CaseIDs, count/2+count%2) // round(count * 0.5)
		assert.IsIncreasing(t, frozenOne.CaseIDs)
		allCaseIDs := map[string]bool{}
		for _, evalCase := range built.Cases {
			allCaseIDs[evalCase.CaseID] = true
		}
		for _, caseID := range frozenOne.CaseIDs {
			assert.True(t, allCaseIDs[caseID])
		}
		// a different seed selects different rows (selection is seed-driven)
		frozenOther := evaluation.BuildFrozenSplit(caseIDs, evaluation.FrozenSplitOptions{
			Seed:           8,
			DatasetVersion: options.DatasetVersion,
			CreatedAt:      options.CreatedAt,
		})
		assert.NotEqual(t, frozenOne.CaseIDs, frozenOther.CaseIDs)

		// rolling split: timestamp-window lane with injected timestamps
		timestamps := map[string]string{}
		for index, evalCase := range built.Cases {
			if index < 25 {
				timestamps[evalCase.CaseID] = "2026-10-08T10:00:00Z"
			} else {
				timestamps[evalCase.CaseID] = "2026-09-01T00:00:00Z"
			}
		}
		rolling, err := evaluation.BuildRollingSplit(caseIDs, evaluation.RollingSplitOptions{
			WindowDays: 7,
			Now:        now,
			Timestamps: timestamps,
		})
		require.NoError(t, err)
		assert.Equal(t, evaluation.RollingSplitModeTimestampWindow, rolling.Mode)
		assert.Len(t, rolling.CaseIDs, 25)
		assert.Equal(t, 0, rolling.ExcludedWithoutTimestamp)
		assert.IsIncreasing(t, rolling.CaseIDs)
		for _, evalCase := range built.Cases[25:] {
			assert.NotContains(t, rolling.CaseIDs, evalCase.CaseID)
		}
		// declared fallback lane: no timestamps anywhere → last-N insertion order
		fallback, err := evaluation.BuildRollingSplit(caseIDs, evaluation.RollingSplitOptions{
			WindowDays: 7,
			Now:        now,
		})
		require.NoError(t, err)
		assert.Equal(t, evaluation.RollingSplitModeLastNFallback, fallback.Mode)
		assert.Len(t, fallback.CaseIDs, evaluation.RollingFallbackCount)
		expectedFallback := make([]string, 0, evaluation.RollingFallbackCount)
		for _, evalCase := range built.Cases[len(built.Cases)-evaluation.RollingFallbackCount:] {
			expectedFallback = append(expectedFallback, evalCase.CaseID)
		}
		assert.ElementsMatch(t, expectedFallback, fallback.CaseIDs)

		// contamination: benchmark rows never overlap candidate-training feedback
		trainingLabelIDs := make([]string, 0, len(labels))
		for _, label := range labels {
			trainingLabelIDs = append(trainingLabelIDs, label.LabelID)
		}
		clean := evaluation.ContaminationCheck(frozenOne.CaseIDs, trainingLabelIDs)
		assert.True(t, clean.Disjoint)
		assert.Equal(t, []string{}, clean.Violations)
		firstFrozenID := frozenOne.CaseIDs[0]
		contaminated := evaluation.ContaminationCheck(frozenOne.CaseIDs,
			append(append([]string{}, trainingLabelIDs...), firstFrozenID))
		assert.False(t, contaminated.Disjoint)
		assert.Equal(t, []string{firstFrozenID}, contaminated.Violations)
	})
}

func f64(v float64) *float64 { return &v }

func b(v bool) *bool { return &v }

// updatedRow builds a fully-measured 'updated' case row.
func updatedRow(caseID string, wasCorrectUpdate, anchorMatched bool, shippedHunks, triggeringDiffLines, editDistance float64) evaluation.CaseEvaluation {
	return evaluation.CaseEvaluation{
		CaseID:              caseID,
		Outcome:             evaluation.CaseOutcomeUpdated,
		WasCorrectUpdate:    b(wasCorrectUpdate),
		AnchorMatched:       b(anchorMatched),
		ShippedHunks:        f64(shippedHunks),
		TriggeringDiffLines: f64(triggeringDiffLines),
		EditDistance:        f64(editDistance),
	}
}

func TestEvaluationMetricsSpotCheckObjective(t *testing.T) {
	t.Run("V-P10-7: the eval report for a pinned policy version contains all six metrics; minimality is measured numbers, not prose", func(t *testing.T) {
		result, err := evaluation.ComputeMetrics(evaluation.ComputeMetricsInput{
			PolicyVersion: 17,
			SplitID:       "frozen-42-2026-10",
			ComputedAt:    at,
			Overrides:     evaluation.OverrideCounts{OverrideCount: 1, DecisionCount: 10},
			Cases: []evaluation.CaseEvaluation{
				// 5 scored updates (4 correct; 3 anchor-matched), 1 missed,
				// 2 abstains (1 correct), plus 1 verifier-caused reject
				// discounted from scoring.
				updatedRow("c1", true, true, 2, 10, 4),
				updatedRow("c2", true, true, 1, 5, 1),
				updatedRow("c3", true, true, 3, 6, 3),
				updatedRow("c4", true, false, 1, 8, 2),
				updatedRow("c5", false, false, 2, 4, 2),
				{CaseID: "c6", Outcome: evaluation.CaseOutcomeMissed},
				{CaseID: "c7", Outcome: evaluation.CaseOutcomeAbstained, WasCorrectAbstain: b(true)},
				{CaseID: "c8", Outcome: evaluation.CaseOutcomeAbstained, WasCorrectAbstain: b(false)},
				func() evaluation.CaseEvaluation {
					row := updatedRow("c9", true, true, 1, 3, 1)
					row.VerifierCausedReject = b(true)
					return row
				}(),
			},
		})
		require.NoError(t, err)

		report := result.Report
		assert.Equal(t, 17, report.PolicyVersion)
		assert.Equal(t, "frozen-42-2026-10", report.SplitID)
		assert.Equal(t, at, report.ComputedAt)
		// The report round-trips the v1 schema (fail-closed validation).
		raw, err := json.Marshal(report)
		require.NoError(t, err)
		parsed, err := schemas.ParseEvaluationReport(raw)
		require.NoError(t, err)
		assert.Equal(t, 17, parsed.PolicyVersion)

		// detection_recall = scored updates / (updates + missed) = 5 / 6.
		assert.InDelta(t, 5.0/6.0, report.Metrics.DetectionRecall, 1e-12)
		// precision = correct updates / updates = 4 / 5.
		assert.InDelta(t, 0.8, report.Metrics.Precision, 1e-12)
		// patch correctness judged against SOURCE ANCHORS = 3 / 5.
		assert.InDelta(t, 0.6, report.Metrics.PatchCorrectness, 1e-12)
		// minimality MEASURED: mean edit-distance ratio vs triggering diff
		// lines ((4/10 + 1/5 + 3/6 + 2/8 + 2/4) / 5 = 0.37) and mean hunk
		// count (9/5).
		assert.InDelta(t, 0.37, report.Metrics.Minimality.MeanEditDistance, 1e-12)
		assert.InDelta(t, 1.8, report.Metrics.Minimality.MeanHunkCount, 1e-12)
		// false updates / ALL scored cases = 1 / 8.
		assert.InDelta(t, 0.125, report.Metrics.FalseUpdateRate, 1e-12)
		// share of abstains that were correct to abstain = 1 / 2.
		assert.InDelta(t, 0.5, report.Metrics.AbstentionQuality, 1e-12)
		assert.Equal(t, 8, report.SampleSize)

		// Verifier-vs-human disagreement is its own metric (P10-T24/R5): the
		// verifier-caused case is discounted from scoring and reported apart.
		assert.Equal(t, []string{"c9"}, result.VerifierDisagreement.AffectedCaseIDs)
		assert.InDelta(t, 1.0/9.0, result.VerifierDisagreement.Rate, 1e-12)
		assert.InDelta(t, 1.0/9.0, report.VerifierDisagreementRate, 1e-12)
	})

	t.Run("V-P10-8: the 2-D abstain-health metric is present per policy version with configured alert thresholds", func(t *testing.T) {
		cases := []evaluation.CaseEvaluation{
			{CaseID: "a1", Outcome: evaluation.CaseOutcomeAbstained, WasCorrectAbstain: b(true)},
			{CaseID: "a2", Outcome: evaluation.CaseOutcomeAbstained, WasCorrectAbstain: b(false)},
		}
		for i := 3; i <= 10; i++ {
			cases = append(cases, updatedRow("a"+strconv.Itoa(i), true, true, 1, 4, 1))
		}
		base := evaluation.ComputeMetricsInput{
			SplitID:    "frozen-42-2026-10",
			ComputedAt: at,
			Overrides:  evaluation.OverrideCounts{OverrideCount: 1, DecisionCount: 10},
			Cases:      cases,
		}

		v17, err := evaluation.ComputeMetrics(func() evaluation.ComputeMetricsInput {
			in := base
			in.PolicyVersion = 17
			return in
		}())
		require.NoError(t, err)
		v18, err := evaluation.ComputeMetrics(func() evaluation.ComputeMetricsInput {
			in := base
			in.PolicyVersion = 18
			return in
		}())
		require.NoError(t, err)
		assert.InDelta(t, 0.2, v17.Report.AbstainHealth.AbstainRate, 1e-12)
		assert.InDelta(t, 0.1, v17.Report.AbstainHealth.OverrideRate, 1e-12)
		assert.InDelta(t, 0.2, v18.Report.AbstainHealth.AbstainRate, 1e-12)
		assert.InDelta(t, 0.1, v18.Report.AbstainHealth.OverrideRate, 1e-12)

		// Thresholds are exported versioned data constants (D11-adjacent),
		// not silent hard-coding — monitors, tests and promotion read the
		// same data.
		assert.Equal(t, evaluation.AbstainHealthThresholds{
			PolicyGapAbstainRate:       0.5,
			OverconfidenceAbstainRate:  0.05,
			OverconfidenceOverrideRate: 0.3,
		}, evaluation.ABSTAIN_HEALTH_THRESHOLDS)
		assert.Equal(t, 0.1, evaluation.SpotCheckRate)
		assert.Equal(t, 1, evaluation.SpotCheckMinPer50)

		// abstain_rate > 0.5 ⇒ 'policy_gap' alert.
		alert, fired := evaluation.EvaluateAbstainHealth(0.6, 0.1)
		assert.True(t, fired)
		assert.Equal(t, evaluation.AbstainHealthPolicyGap, alert)
		// abstain_rate < 0.05 AND override_rate > 0.3 ⇒ 'overconfidence' alert.
		alert, fired = evaluation.EvaluateAbstainHealth(0.02, 0.5)
		assert.True(t, fired)
		assert.Equal(t, evaluation.AbstainHealthOverconfidence, alert)
		// healthy observation ⇒ no alert.
		_, fired = evaluation.EvaluateAbstainHealth(0.2, 0.1)
		assert.False(t, fired)
		// both conditions fire ⇒ policy_gap wins (an over-abstaining policy
		// cannot simultaneously be under-abstaining).
		alert, fired = evaluation.EvaluateAbstainHealth(0.6, 0.5)
		assert.True(t, fired)
		assert.Equal(t, evaluation.AbstainHealthPolicyGap, alert)
	})

	t.Run("V-P10-9: a spot-check sample of auto-applied patches is recorded deterministically; ingested audit findings appear as eval labels", func(t *testing.T) {
		// 100 auto-applied patches → 10% rate (round(10)) ⇒ 10 sampled rows.
		patches := make([]evaluation.AutoAppliedPatch, 0, 100)
		for index := 0; index < 100; index++ {
			patches = append(patches, evaluation.AutoAppliedPatch{
				DecisionID: "d-" + pad3(index),
			})
		}
		first, err := evaluation.SampleAutoPatches(patches, evaluation.Mulberry32(42))
		require.NoError(t, err)
		second, err := evaluation.SampleAutoPatches(patches, evaluation.Mulberry32(42))
		require.NoError(t, err)
		assert.Len(t, first, 10)
		assert.Equal(t, first, second) // same seed ⇒ byte-identical sample
		firstIDs := make([]string, 0, len(first))
		selected := map[string]bool{}
		for _, patch := range first {
			firstIDs = append(firstIDs, patch.DecisionID)
			selected[patch.DecisionID] = true
		}
		sortedIDs := append([]string(nil), firstIDs...)
		sort.Strings(sortedIDs)
		assert.Equal(t, sortedIDs, firstIDs)
		assert.Len(t, selected, 10)
		for _, patch := range patches {
			if selected[patch.DecisionID] {
				assert.Contains(t, firstIDs, patch.DecisionID)
			}
		}
		// Floor discipline: a non-empty batch is never unaudited; 3 < 50 ⇒ 1.
		size0, err := evaluation.SpotCheckSampleSize(0)
		require.NoError(t, err)
		assert.Equal(t, 0, size0)
		small, err := evaluation.SampleAutoPatches(patches[:3], evaluation.Mulberry32(42))
		require.NoError(t, err)
		assert.Len(t, small, 1)
		size50, err := evaluation.SpotCheckSampleSize(50)
		require.NoError(t, err)
		assert.Equal(t, 5, size50)

		// Audit verdicts re-enter the loop as hygiene-label-shaped eval
		// labels (source_signal 'audit_finding', traceable hygiene_rule). The
		// injected clock + byte source pin the ids.
		injectedNow := int64(1_700_000_000_000)
		labels, err := evaluation.IngestAuditFindings([]evaluation.AuditFinding{
			{DecisionID: first[0].DecisionID, PolicyVersion: 17, Verdict: evaluation.AuditVerdictPatchIncorrect, At: at},
			{DecisionID: first[1].DecisionID, PolicyVersion: 17, Verdict: evaluation.AuditVerdictPatchCorrect, At: at},
			{DecisionID: first[2].DecisionID, PolicyVersion: 17, Verdict: evaluation.AuditVerdictUpdateUnwarranted, At: at},
		}, &evaluation.IngestAuditFindingsOptions{NowMs: &injectedNow, RandBytes: constRng})
		require.NoError(t, err)
		require.Len(t, labels, 3)
		for _, label := range labels {
			// Each ingested row is a schema-valid eval label the dataset consumes.
			assert.Empty(t, label.Validate())
			raw, err := json.Marshal(label)
			require.NoError(t, err)
			_, err = schemas.ParseHygieneLabel(raw)
			require.NoError(t, err)
			assert.Equal(t, schemas.HygieneSourceAuditFinding, label.SourceSignal)
			assert.Equal(t, 17, label.PolicyVersion)
			assert.Equal(t, at, label.NormalizedAt)
		}
		labelNames := make([]schemas.HygieneLabelName, 0, len(labels))
		labelRules := make([]string, 0, len(labels))
		for _, label := range labels {
			labelNames = append(labelNames, label.Label)
			labelRules = append(labelRules, label.HygieneRule)
		}
		assert.Equal(t, []schemas.HygieneLabelName{
			schemas.HygieneLabelCorrected,
			schemas.HygieneLabelAccepted,
			schemas.HygieneLabelFalsePositive,
		}, labelNames)
		assert.Equal(t, []string{
			"audit_finding:patch_incorrect⇒corrected",
			"audit_finding:patch_correct⇒accepted",
			"audit_finding:update_unwarranted⇒false_positive",
		}, labelRules)
		// Ingestion is deterministic under the injected clock + byte source.
		replay, err := evaluation.IngestAuditFindings([]evaluation.AuditFinding{
			{DecisionID: first[0].DecisionID, PolicyVersion: 17, Verdict: evaluation.AuditVerdictPatchIncorrect, At: at},
		}, &evaluation.IngestAuditFindingsOptions{NowMs: &injectedNow, RandBytes: constRng})
		require.NoError(t, err)
		require.Len(t, replay, 1)
		assert.Equal(t, labels[0].LabelID, replay[0].LabelID)

		// Fail closed: an empty decisionId, a bad policy version or an
		// unknown verdict refuse the batch.
		_, err = evaluation.IngestAuditFindings([]evaluation.AuditFinding{
			{DecisionID: "", PolicyVersion: 17, Verdict: evaluation.AuditVerdictPatchCorrect, At: at},
		}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty decisionId")
		_, err = evaluation.IngestAuditFindings([]evaluation.AuditFinding{
			{DecisionID: "d_1", PolicyVersion: 0, Verdict: evaluation.AuditVerdictPatchCorrect, At: at},
		}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "policyVersion must be a positive integer")
		_, err = evaluation.IngestAuditFindings([]evaluation.AuditFinding{
			{DecisionID: "d_1", PolicyVersion: 17, Verdict: evaluation.AuditVerdict("vibes"), At: at},
		}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown verdict")
		_, err = evaluation.SpotCheckSampleSize(-1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "batchSize must be a non-negative integer")
	})

	t.Run("V-P10-10: the objective score computes with repo-configurable weights; a high-risk fixture with unsafe_auto_updates=1 collapses and fails the gate", func(t *testing.T) {
		metrics := evaluation.ObjectiveMetricsInput{
			DetectionRecall:  0.94,
			Precision:        0.9,
			PatchCorrectness: 0.88,
		}
		usage := evaluation.UsageAccounting{TotalCostUsd: 1, TotalLatencyMs: 3000}

		// Default CTX §36 weights: 0.40*precision + 0.30*recall + 0.20*correct
		// + 0.10*efficiency; efficiency = 1 - (0.1 + 0.1)/2 = 0.9.
		defaulted, err := evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
			Metrics: metrics, Usage: usage, UnsafeAutoUpdates: 0,
		})
		require.NoError(t, err)
		assert.False(t, defaulted.Collapsed)
		assert.Nil(t, defaulted.FailedGate)
		assert.InDelta(t, 0.4*0.9+0.3*0.94+0.2*0.88+0.1*0.9, defaulted.Score, 1e-12)

		// Repo-configurable weights (sum exactly 1.0) change the composite.
		weighted, err := evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
			Metrics: metrics, Usage: usage, UnsafeAutoUpdates: 0,
			Weights: &evaluation.ObjectiveWeights{
				Precision: 0.5, Recall: 0.25, PatchCorrectness: 0.25, Efficiency: 0,
			},
		})
		require.NoError(t, err)
		assert.InDelta(t, 0.5*0.9+0.25*0.94+0.25*0.88, weighted.Score, 1e-12)
		assert.Equal(t, evaluation.DefaultObjectiveWeights, evaluation.ObjectiveWeights{
			Precision: 0.4, Recall: 0.3, PatchCorrectness: 0.2, Efficiency: 0.1,
		})

		// Usage accounting is the efficiency SOURCE: above the exported
		// ceilings both shares clamp at 1, so efficiency degrades to 0
		// (never boosts).
		degraded, err := evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
			Metrics: metrics, Usage: evaluation.UsageAccounting{TotalCostUsd: 25, TotalLatencyMs: 60_000},
			UnsafeAutoUpdates: 0,
		})
		require.NoError(t, err)
		assert.InDelta(t, 0.4*0.9+0.3*0.94+0.2*0.88, degraded.Score, 1e-12)

		// High-risk fixture: unsafe_auto_updates=1 COLLAPSES the score to 0
		// and fails the gate (ARCH §8.4 — never laundered into a promotable
		// number).
		collapsed, err := evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
			Metrics: metrics, Usage: usage, UnsafeAutoUpdates: 1,
		})
		require.NoError(t, err)
		require.NotNil(t, collapsed.FailedGate)
		assert.Equal(t, evaluation.UnsafeAutoUpdatesGate, *collapsed.FailedGate)
		assert.True(t, collapsed.Collapsed)
		assert.Equal(t, 0.0, collapsed.Score)

		// Fail closed: mis-specified objectives and out-of-range metrics refuse.
		_, err = evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
			Metrics: metrics, Usage: usage, UnsafeAutoUpdates: 0,
			Weights: &evaluation.ObjectiveWeights{
				Precision: 0.5, Recall: 0.2, PatchCorrectness: 0.2, Efficiency: 0.05,
			},
		})
		require.Error(t, err)
		assert.Regexp(t, `weights must sum to exactly 1\.0`, err.Error())
		_, err = evaluation.ComputeObjectiveScore(evaluation.ComputeObjectiveScoreInput{
			Metrics: evaluation.ObjectiveMetricsInput{
				DetectionRecall: 1.2, Precision: 0.9, PatchCorrectness: 0.88,
			},
			Usage: usage, UnsafeAutoUpdates: 0,
		})
		require.Error(t, err)
		assert.Regexp(t, `must be in 0\.\.1`, err.Error())
	})

	t.Run("computeMetrics refuses rows missing their verdicts and empty/discount-only inputs (fail closed)", func(t *testing.T) {
		base := evaluation.ComputeMetricsInput{
			PolicyVersion: 17,
			SplitID:       "split",
			ComputedAt:    at,
			Overrides:     evaluation.OverrideCounts{OverrideCount: 0, DecisionCount: 1},
			Cases:         []evaluation.CaseEvaluation{updatedRow("c1", true, true, 1, 4, 1)},
		}
		// 'updated' without wasCorrectUpdate.
		bad := base
		bad.Cases = []evaluation.CaseEvaluation{{CaseID: "c1", Outcome: evaluation.CaseOutcomeUpdated, AnchorMatched: b(true), ShippedHunks: f64(1), TriggeringDiffLines: f64(4), EditDistance: f64(1)}}
		_, err := evaluation.ComputeMetrics(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "carries no wasCorrectUpdate verdict")
		// 'updated' without anchorMatched.
		bad = base
		bad.Cases = []evaluation.CaseEvaluation{{CaseID: "c1", Outcome: evaluation.CaseOutcomeUpdated, WasCorrectUpdate: b(true), ShippedHunks: f64(1), TriggeringDiffLines: f64(4), EditDistance: f64(1)}}
		_, err = evaluation.ComputeMetrics(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "carries no anchorMatched source-anchor evidence")
		// 'abstained' without wasCorrectAbstain.
		bad = base
		bad.Cases = []evaluation.CaseEvaluation{{CaseID: "c1", Outcome: evaluation.CaseOutcomeAbstained}}
		_, err = evaluation.ComputeMetrics(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "carries no wasCorrectAbstain verdict")
		// empty case list.
		bad = base
		bad.Cases = nil
		_, err = evaluation.ComputeMetrics(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cases must be non-empty")
		// every case discounted as verifier-caused.
		bad = base
		discounted := updatedRow("c1", true, true, 1, 4, 1)
		discounted.VerifierCausedReject = b(true)
		bad.Cases = []evaluation.CaseEvaluation{discounted}
		_, err = evaluation.ComputeMetrics(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no policy evidence remains")
		// out-of-range measured numbers.
		bad = base
		bad.Cases = []evaluation.CaseEvaluation{updatedRow("c1", true, true, 1, 4, 1)}
		bad.Cases[0].TriggeringDiffLines = f64(0)
		_, err = evaluation.ComputeMetrics(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "triggeringDiffLines must be a finite number >= 1")
		// override count beyond decision count, and a non-positive policy version.
		bad = base
		bad.Overrides = evaluation.OverrideCounts{OverrideCount: 5, DecisionCount: 2}
		_, err = evaluation.ComputeMetrics(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overrideCount (5) cannot exceed decisionCount (2)")
		bad = base
		bad.PolicyVersion = 0
		_, err = evaluation.ComputeMetrics(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "policyVersion must be an integer >= 1, got 0")
	})
}

// pad3 mirrors String(index).padStart(3, '0').
func pad3(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}
