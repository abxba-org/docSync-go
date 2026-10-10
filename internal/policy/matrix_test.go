// Ported from packages/policy/test/shadow-matrix.test.ts (P12-T04; V-P12-3; V-P12-2; ARCH §8.6):
// the decision-level agreement/disagreement matrix, its closed vocabulary and
// its end-to-end role as the Phase-10 entry-gate deliverable (a resolved
// disagreement comparison feeding the promotion gate library).
//
// Determinism note (mirrors the TS): no clock is injected — the fixture's
// `now` (shadowReplayNow) is passed into runShadowReplay, so the matrix
// computed_at is pinned.
package policy_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- fixture */

func shadowMatrixAnalysisFor(caseID string) schemas.ChangeAnalysis {
	return schemas.ChangeAnalysis{
		Schema:              "docsync.change-analysis/v1",
		RunID:               "acme/widgets#" + caseID,
		Repo:                "acme/widgets",
		BaseSHA:             "base-" + caseID,
		HeadSHA:             "head-" + caseID,
		Files:               []schemas.ChangedFile{{Path: "src/" + caseID + ".ts", Status: schemas.FileStatusModified}},
		DependenciesChanged: false,
		ClassificationHints: []schemas.ClassificationHint{},
		DeepAnalysis:        schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: shadowStrPtr("fixture promotion case")},
		AnalyzerCoverage:    []schemas.AnalyzerCoverage{{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK}},
	}
}

func shadowMatrixInputsFor(caseID string) policy.ShadowReplayInputs {
	return policy.ShadowReplayInputs{
		ChangeAnalysis: shadowMatrixAnalysisFor(caseID),
		CandidateSet:   []string{"docs/guide.md", "docs/api.md"},
		DiffSubsetHash: docutil.ContentHash(caseID),
	}
}

func shadowMatrixCases() []policy.ShadowReplayCase {
	cases := make([]policy.ShadowReplayCase, 0, len(shadowSplit.CaseIDs))
	for _, caseID := range shadowSplit.CaseIDs {
		cases = append(cases, policy.ShadowReplayCase{CaseID: caseID, Inputs: shadowMatrixInputsFor(caseID)})
	}
	return cases
}

// shadowAllOutcomes are the reviewer outcomes resolving every fixture
// disagreement.
var shadowAllOutcomes = []policy.ShadowReviewerOutcome{
	{CaseID: "case-05", Resolution: schemas.ShadowResolutionCandidateCorrect},
	{CaseID: "case-07", Resolution: schemas.ShadowResolutionCandidateCorrect},
	{CaseID: "case-12", Resolution: schemas.ShadowResolutionCandidateCorrect},
}

func shadowMatrixFixtureReplay(t *testing.T) policy.ShadowReplayResult {
	t.Helper()
	result, err := policy.RunShadowReplay(policy.ShadowReplayInput{
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		Split:                   shadowSplit,
		Cases:                   shadowMatrixCases(),
		ProductionReplay:        shadowRecordedPort(t, shadowProductionActions, nil),
		CandidateReplay:         shadowRecordedPort(t, shadowCandidateActions, nil),
		Now:                     shadowReplayNow,
	})
	require.NoError(t, err)
	return result
}

func shadowMatrixDisagreementByCase(t *testing.T, matrix schemas.ShadowAgreementMatrix, caseID string) schemas.ShadowMatrixDisagreement {
	t.Helper()
	for _, row := range matrix.Disagreements {
		if row.CaseID == caseID {
			return row
		}
	}
	t.Fatalf("no disagreement row for case %s", caseID)
	return schemas.ShadowMatrixDisagreement{}
}

// shadowMatrixEvaluationReports are the fixture promotion's baseline (v17) and
// candidate (v18) evaluation reports — the entry-gate evidence a promotion
// pipeline assembles from a split.
func shadowMatrixBaselineReport() schemas.EvaluationReport {
	return schemas.EvaluationReport{
		Schema:        schemas.EvaluationReportSchemaID,
		PolicyVersion: 17,
		SplitID:       shadowSplit.SplitID,
		SampleSize:    8,
		Metrics: schemas.EvalReportMetrics{
			DetectionRecall:   0.5,
			Precision:         2.0 / 3.0,
			PatchCorrectness:  0.9,
			Minimality:        schemas.EvalMinimality{MeanEditDistance: 12, MeanHunkCount: 1},
			FalseUpdateRate:   1.0 / 3.0,
			AbstentionQuality: 5.0 / 8.0,
		},
		AbstainHealth:            schemas.EvalReportAbstainHealth{AbstainRate: 0.625, OverrideRate: 0},
		VerifierDisagreementRate: 0,
		ComputedAt:               shadowReplayNow,
	}
}

/* ----------------------------------------------------------------- tests */

// TS: 'V-P12-3: the shadow matrix produces a decision-level
// agreement/disagreement matrix for the fixture promotion'.
func TestShadowMatrix_VP12_3_DisagreementMatrixForFixturePromotion(t *testing.T) {
	result := shadowMatrixFixtureReplay(t)

	assert.Equal(t, shadowSplit.SplitID, result.Matrix.SplitID)
	assert.Equal(t, 17, result.Matrix.ProductionPolicyVersion)
	assert.Equal(t, 18, result.Matrix.CandidatePolicyVersion)

	// Cases get classified into agreement or disagreement buckets (each case
	// exactly once, deterministic order).
	var classified []string
	for _, cell := range result.Matrix.Cells {
		classified = append(classified, cell.CaseIDs...)
	}
	sort.Strings(classified)
	expectedCases := append([]string(nil), shadowSplit.CaseIDs...)
	sort.Strings(expectedCases)
	assert.Equal(t, expectedCases, classified)
	total := 0
	for _, cell := range result.Matrix.Cells {
		total += cell.Count
	}
	assert.Equal(t, len(shadowSplit.CaseIDs), total)

	assert.Equal(t, []schemas.ShadowMatrixCell{
		{CandidateDecision: schemas.ActionIgnore, ProductionDecision: schemas.ActionIgnore, Count: 3, CaseIDs: []string{"case-02", "case-08", "case-10"}},
		{CandidateDecision: schemas.ActionIgnore, ProductionDecision: schemas.ActionReview, Count: 1, CaseIDs: []string{"case-12"}},
		{CandidateDecision: schemas.ActionReview, ProductionDecision: schemas.ActionIgnore, Count: 2, CaseIDs: []string{"case-05", "case-07"}},
		{CandidateDecision: schemas.ActionReview, ProductionDecision: schemas.ActionReview, Count: 2, CaseIDs: []string{"case-01", "case-04"}},
	}, result.Matrix.Cells)
	assert.Equal(t, 5, result.Matrix.AgreementCount)
	assert.Equal(t, 3, result.Matrix.DisagreementCount)

	// Every disagreement starts unresolved ('pending' so explicitly-unresolved
	// rows survive strict artifact validation, ARCH §8.6).
	for _, row := range result.Matrix.Disagreements {
		assert.Equal(t, schemas.ShadowResolutionPending, row.Resolution)
	}

	// END-TO-END (ARCH §8.6): the resolved labels feed the promotion gate
	// library over the fixture promotion.

	// Partial resolution: only the labelled disagreements resolve; unresolved
	// ones KEEP 'pending'.
	partial, err := policy.ResolveDisagreementLabels(result.Matrix, shadowAllOutcomes[:2])
	require.NoError(t, err)
	assert.Equal(t, schemas.ShadowResolutionCandidateCorrect, shadowMatrixDisagreementByCase(t, partial, "case-05").Resolution)
	assert.Equal(t, schemas.ShadowResolutionPending, shadowMatrixDisagreementByCase(t, partial, "case-12").Resolution)

	// Full resolution: every disagreement labelled; agreement cells and the
	// input-hash lineage are untouched (the resolution is a label-only pass).
	resolved, err := policy.ResolveDisagreementLabels(result.Matrix, shadowAllOutcomes)
	require.NoError(t, err)
	labels := make(map[string]schemas.ShadowDisagreementResolution, len(resolved.Disagreements))
	for _, row := range resolved.Disagreements {
		assert.NotEqual(t, schemas.ShadowResolutionPending, row.Resolution)
		labels[row.CaseID] = row.Resolution
	}
	assert.Equal(t, map[string]schemas.ShadowDisagreementResolution{
		"case-05": schemas.ShadowResolutionCandidateCorrect,
		"case-07": schemas.ShadowResolutionCandidateCorrect,
		"case-12": schemas.ShadowResolutionCandidateCorrect,
	}, labels)
	assert.Equal(t, result.Matrix.Cells, resolved.Cells)
	assert.Equal(t, result.Matrix.InputHashes, resolved.InputHashes)

	// The promotion gate library over the fixture promotion: this candidate WINS
	// the aggregate and regression gates but fails the sample and significance
	// floors — 8 labeled cases cannot clear the OD-R24 100-case floor.
	baseline := shadowMatrixBaselineReport()
	candidate := baseline
	candidate.PolicyVersion = 18
	candidate.BaselineVersion = shadowIntPtr(17)
	candidate.Metrics = schemas.EvalReportMetrics{
		DetectionRecall:   1,
		Precision:         1,
		PatchCorrectness:  baseline.Metrics.PatchCorrectness,
		Minimality:        baseline.Metrics.Minimality,
		FalseUpdateRate:   0,
		AbstentionQuality: 1,
	}
	gates, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{})
	require.NoError(t, err)
	checklist, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   baseline,
		Candidate:                  candidate,
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              7,
		CandidateTotal:             8,
		BaselineWins:               5,
		BaselineTotal:              8,
	})
	require.NoError(t, err)
	rowGates := make([]string, len(checklist.Rows))
	for index, row := range checklist.Rows {
		rowGates[index] = row.Gate
	}
	assert.Equal(t, policy.MandatoryGateNames, rowGates)
	assert.False(t, checklist.Passed)
	assert.Contains(t, checklist.FailedGates, "min_sample_size")
	assert.Contains(t, checklist.FailedGates, "significance_floor")
}

// TS: 'P12-T04: the closed decision vocabulary is enforced and
// additive-safe'.
func TestShadowMatrix_P12_T04_ClosedVocabularyAdditiveSafe(t *testing.T) {
	assert.Equal(t, []schemas.Action{
		schemas.ActionIgnore,
		schemas.ActionReview,
		schemas.ActionInvestigate,
		schemas.ActionPatch,
	}, schemas.ActionValues)

	// An off-vocabulary candidate decision fails closed.
	_, err := policy.ComputeAgreementMatrix(policy.ComputeAgreementMatrixInput{
		Pairs: []policy.ShadowDecisionPair{{
			CaseID:           "case-01",
			ProductionAction: schemas.ActionIgnore,
			CandidateAction:  schemas.Action("refactor"),
			InputHash:        strings.Repeat("a", 64),
		}},
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		SplitID:                 shadowSplit.SplitID,
		Now:                     shadowReplayNow,
	})
	require.Error(t, err)
	var vocabulary *policy.ShadowMatrixVocabularyError
	require.ErrorAs(t, err, &vocabulary)
	assert.Equal(t, "case-01", vocabulary.CaseID)
	assert.Equal(t, "candidateAction", vocabulary.Field)
	assert.Equal(t, "refactor", vocabulary.Value)
	assert.Equal(t, docutil.CodeValidation, vocabulary.Code())
	assert.Contains(t, vocabulary.Error(), "vocabulary violation")
	assert.Contains(t, vocabulary.Error(), "refactor")

	// An off-vocabulary production decision fails closed too.
	_, err = policy.ComputeAgreementMatrix(policy.ComputeAgreementMatrixInput{
		Pairs: []policy.ShadowDecisionPair{{
			CaseID:           "case-01",
			ProductionAction: schemas.Action("merge"),
			CandidateAction:  schemas.ActionIgnore,
			InputHash:        strings.Repeat("a", 64),
		}},
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		SplitID:                 shadowSplit.SplitID,
		Now:                     shadowReplayNow,
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &vocabulary)
	assert.Equal(t, "productionAction", vocabulary.Field)
	assert.Equal(t, "merge", vocabulary.Value)

	// The matrix artifact is a data row set: a future decision vocabulary
	// extension is additive-safe for v1 readers (every cell carries the
	// candidate decision on the wire).
	result := shadowMatrixFixtureReplay(t)
	for _, cell := range result.Matrix.Cells {
		wire, err := json.Marshal(cell)
		require.NoError(t, err)
		var row map[string]any
		require.NoError(t, json.Unmarshal(wire, &row))
		assert.Contains(t, row, "candidate_decision")
	}

	// An unknown reviewer outcome resolution fails closed.
	_, err = policy.ResolveDisagreementLabels(result.Matrix, []policy.ShadowReviewerOutcome{
		{CaseID: "case-05", Resolution: schemas.ShadowDisagreementResolution("candidate_won")},
	})
	require.Error(t, err)
	var outcomeErr *policy.ShadowReviewerOutcomeError
	require.ErrorAs(t, err, &outcomeErr)
	assert.Equal(t, docutil.CodeValidation, outcomeErr.Code())

	// A stray case (never disagreed) fails closed.
	_, err = policy.ResolveDisagreementLabels(result.Matrix, []policy.ShadowReviewerOutcome{
		{CaseID: "case-01", Resolution: schemas.ShadowResolutionCandidateCorrect},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &outcomeErr)
	assert.Contains(t, outcomeErr.Error(), "not a disagreement")

	// A duplicate case outcome fails closed.
	_, err = policy.ResolveDisagreementLabels(result.Matrix, []policy.ShadowReviewerOutcome{
		{CaseID: "case-05", Resolution: schemas.ShadowResolutionCandidateCorrect},
		{CaseID: "case-05", Resolution: schemas.ShadowResolutionCandidateCorrect},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &outcomeErr)
	assert.Contains(t, outcomeErr.Error(), "duplicate reviewer outcome")
}

// TS: 'fails closed on a duplicate pair (a case must contribute exactly one
// decision pair)'.
func TestShadowMatrix_FailsClosedOnDuplicatePairs(t *testing.T) {
	pair := policy.ShadowDecisionPair{
		CaseID:           "case-01",
		ProductionAction: schemas.ActionIgnore,
		CandidateAction:  schemas.ActionReview,
		InputHash:        strings.Repeat("a", 64),
	}
	_, err := policy.ComputeAgreementMatrix(policy.ComputeAgreementMatrixInput{
		Pairs:                   []policy.ShadowDecisionPair{pair, pair},
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		SplitID:                 shadowSplit.SplitID,
		Now:                     shadowReplayNow,
	})
	require.Error(t, err)
	var shapeErr *policy.ShadowMatrixShapeError
	require.ErrorAs(t, err, &shapeErr)
	assert.Contains(t, shapeErr.Error(), "duplicate pair")
}

// Fail-closed edge branches of the matrix engine (the TS test file pins the
// vocabulary/duplicate/shape failures; these complete the port): the artifact
// self-check is the fail-closed boundary — a drift-ridden matrix (here: a
// non-instant computed_at) is rejected by docsync.shadow-agreement-matrix/v1
// validation, both on compute and on label resolution.
func TestShadowMatrix_FailClosedEdgeBranches(t *testing.T) {
	valid := policy.ComputeAgreementMatrixInput{
		Pairs: []policy.ShadowDecisionPair{{
			CaseID:           "case-01",
			ProductionAction: schemas.ActionIgnore,
			CandidateAction:  schemas.ActionReview,
			InputHash:        strings.Repeat("a", 64),
		}},
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		SplitID:                 shadowSplit.SplitID,
		Now:                     shadowReplayNow,
	}
	_, err := policy.ComputeAgreementMatrix(valid)
	require.NoError(t, err)

	// A non-instant computed_at never reaches the wire (fail closed on drift).
	drift := valid
	drift.Now = "2026-10-09"
	_, err = policy.ComputeAgreementMatrix(drift)
	require.Error(t, err)

	// The same self-check guards label resolution.
	result := shadowMatrixFixtureReplay(t)
	tampered := result.Matrix
	tampered.ComputedAt = "not-an-instant"
	_, err = policy.ResolveDisagreementLabels(tampered, nil)
	require.Error(t, err)

	// Typed-error accessors carry the coded error.
	duplicate := valid
	duplicate.Pairs = []policy.ShadowDecisionPair{valid.Pairs[0], valid.Pairs[0]}
	_, err = policy.ComputeAgreementMatrix(duplicate)
	require.Error(t, err)
	var shapeErr *policy.ShadowMatrixShapeError
	require.ErrorAs(t, err, &shapeErr)
	assert.Equal(t, docutil.CodeValidation, shapeErr.Code())
	assertShadowErrorCoded(t, err, docutil.CodeValidation)

	_, err = policy.ComputeAgreementMatrix(policy.ComputeAgreementMatrixInput{
		Pairs: []policy.ShadowDecisionPair{{
			CaseID:           "case-01",
			ProductionAction: schemas.Action("refactor"),
			CandidateAction:  schemas.ActionIgnore,
			InputHash:        strings.Repeat("a", 64),
		}},
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		SplitID:                 shadowSplit.SplitID,
		Now:                     shadowReplayNow,
	})
	require.Error(t, err)
	var vocabulary *policy.ShadowMatrixVocabularyError
	require.ErrorAs(t, err, &vocabulary)
	assert.Equal(t, docutil.CodeValidation, vocabulary.Code())
	assertShadowErrorCoded(t, err, docutil.CodeValidation)

	_, err = policy.ResolveDisagreementLabels(result.Matrix, []policy.ShadowReviewerOutcome{
		{CaseID: "case-05", Resolution: schemas.ShadowDisagreementResolution("candidate_won")},
	})
	require.Error(t, err)
	var outcomeErr *policy.ShadowReviewerOutcomeError
	require.ErrorAs(t, err, &outcomeErr)
	assert.Equal(t, docutil.CodeValidation, outcomeErr.Code())
	assertShadowErrorCoded(t, err, docutil.CodeValidation)
}
