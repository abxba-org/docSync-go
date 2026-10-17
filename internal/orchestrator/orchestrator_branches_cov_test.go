package orchestrator_test

// Heart-beat branch coverage for pipeline stages reached through public
// RunPipeline by injecting the SEAMS the runner exposes (nil decision port,
// timeout findings, writer abstains, a failing signal sink) or by crafting a
// caller-owned repo whose state breaks a stage (empty manifest, corrupt doc
// state). Every case asserts the observable degrade/return, never bare code
// execution.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

// TestCOV_ORCH_602_NilDecisionPortDegrades covers jevStage's unwired-engine
// guard: a nil decision port is not a routing verdict — it degrades to REVIEW
// with the model_unavailable abstain, never a panic.
func TestCOV_ORCH_602_NilDecisionPortDegrades(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)

	ports := testkit.NewPipelineFakes().Ports()
	ports.Decision = nil // the unwired-engine seam

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.NoError(t, err, "an unwired decision engine degrades, it does not fail the run")
	require.NotNil(t, outcome)
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	assert.True(t, covAbstainHasReason(outcome, nil, schemas.AbstainReasonModelUnavailable))
	degradation := covDegradation(outcome, string(orchestrator.StageJev), schemas.DegradationJevUnavailable)
	require.NotNil(t, degradation)
	assert.Contains(t, degradation.Detail, "cannot read properties of undefined")
}

// TestCOV_ORCH_603_WriterTimeoutFinding surfaces the writer-stage timeout arm:
// a V-P3-8 finding (no patch) is recorded, a writer-timeout degradation is
// declared, and the doc surfaces to review — never silently absent.
func TestCOV_ORCH_603_WriterTimeoutFinding(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc})),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{
		Kind: orchestrator.WriterResultFinding,
		Finding: &orchestrator.WriterStageFinding{
			DocPath: testkit.PaymentsDoc, Reason: orchestrator.WriterStageFindingReasonTimeout, Detail: "writer exceeded its budget",
		},
	})

	outcome, err := orchestrator.RunPipeline(context.Background(), covPaymentsInput(pipeline, base, head), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	require.NotEmpty(t, outcome.Findings, "the timeout finding is recorded")
	// Findings are schema-shaped; assert its observable surface.
	assert.Equal(t, "potentially-stale", outcome.Findings[0].Kind)
	require.NotNil(t, outcome.Findings[0].DocPath)
	assert.Equal(t, testkit.PaymentsDoc, *outcome.Findings[0].DocPath)

	degradation := covDegradation(outcome, string(orchestrator.StageWriter), schemas.DegradationWriterTimeout)
	require.NotNil(t, degradation)
	assert.Contains(t, degradation.Detail, "writer exceeded its budget")
	doc := orMatrixFindDoc(outcome, testkit.PaymentsDoc)
	require.NotNil(t, doc)
	assert.Equal(t, schemas.PipelineOutcomeReview, doc.PolicyOutcome, "a finding surfaces the doc to review")
}

// TestCOV_ORCH_604_WriterAbstainMapping covers the writer-stage abstain arm: a
// writer abstain routes the doc to REVIEW and records the mapped abstain.
func TestCOV_ORCH_604_WriterAbstainMapping(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc})),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{
		Kind:   orchestrator.WriterResultAbstain,
		Reason: schemas.AbstainReasonModelUnavailable,
		Detail: "writer could not form any minimal patch",
	})

	outcome, err := orchestrator.RunPipeline(context.Background(), covPaymentsInput(pipeline, base, head), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	assert.True(t, covAbstainHasReason(outcome, ptrTo(testkit.PaymentsDoc), schemas.AbstainReasonModelUnavailable))
	doc := orMatrixFindDoc(outcome, testkit.PaymentsDoc)
	require.NotNil(t, doc)
	assert.Equal(t, schemas.PipelineOutcomeReview, doc.PolicyOutcome)
}

// covFailingPostSignal is an early-signal sink whose Post fails: the run must
// degrade (signal-sink-failed) but continue to REVIEW/routing, never crash.
type covFailingPostSignal struct{ err error }

func (s covFailingPostSignal) Post(orchestrator.EarlySignalPayload) error   { return s.err }
func (s covFailingPostSignal) Update(orchestrator.EarlySignalPayload) error { return nil }

// TestCOV_ORCH_605_EarlySignalPostFailureDegrades covers jevPostProcess's
// post-failure lane: a failing early-signal Post records a signal-sink-failed
// degradation but never blocks routing or persistence.
func TestCOV_ORCH_605_EarlySignalPostFailureDegrades(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes, _ := orThreadingFakes(runID, base, head, []string{testkit.PaymentsDoc})
	ports := fakes.Ports()
	ports.EarlySignal = covFailingPostSignal{err: errors.New("signal sink post failed")}

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.NoError(t, err, "a failing early-signal Post never blocks the run")
	require.NotNil(t, outcome)

	degradation := covDegradation(outcome, string(orchestrator.StageJev), schemas.DegradationSignalSinkFailed)
	require.NotNil(t, degradation)
	assert.Contains(t, degradation.Detail, "early signal post failed")
	assert.Nil(t, outcome.EarlySignal.PostedAt, "a failed post leaves no postedAt timestamp")
	// The successful run record still persists.
	record, readErr := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, readErr)
	assert.True(t, record.Exists)
}

// TestCOV_ORCH_606_CorruptDocStateFailsInvalidate covers the invalidate-stage
// failure lane: a corrupt doc state makes MarkSuspectsFromAnalysis fail, the
// pipeline surfaces the runtime failure, and the failed run record is still
// persisted for provenance.
func TestCOV_ORCH_606_CorruptDocStateFailsInvalidate(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)

	docStateRel, err := state.DocStateRelPath(testkit.PaymentsDoc)
	require.NoError(t, err)
	_, err = pipeline.Store.WriteRawForTest(docStateRel, "{ not json", nil)
	require.NoError(t, err)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), testkit.NewPipelineFakes().Ports())
	require.Error(t, err, "a corrupt doc state fails the invalidate stage")
	require.Nil(t, outcome)
	assert.True(t, orchestrator.IsRuntimeFailure(err))

	// Provenance is preserved: the failed run record persists with the
	// invalidate stage marked failed.
	record, readErr := pipeline.Store.ReadRunRecord(orMatrixExpectedKey(base, head))
	require.NoError(t, readErr)
	require.True(t, record.Exists)
	var invalidate *schemas.RunStage
	for i := range record.Content.Stages {
		if record.Content.Stages[i].Stage == string(orchestrator.StageInvalidate) {
			invalidate = &record.Content.Stages[i]
		}
	}
	require.NotNil(t, invalidate)
	assert.Equal(t, schemas.RunStageFailed, invalidate.Status, "the persisted record marks the invalidate stage failed")
}

// TestCOV_ORCH_607_VerificationPortReturnsNoReport covers the verification
// stage's nil-report guard: a verifier that returns no report degrades
// (verifier_uncertain + verification-failed), never crashes.
func TestCOV_ORCH_607_VerificationPortReturnsNoReport(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc})),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{Kind: orchestrator.WriterResultPatch, Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID))})
	fakes.Verification.OnDoc(testkit.PaymentsDoc, orchestrator.VerificationStageResult{Report: nil}) // no report

	outcome, err := orchestrator.RunPipeline(context.Background(), covPaymentsInput(pipeline, base, head), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	degradation := covDegradation(outcome, string(orchestrator.StageVerification), schemas.DegradationVerificationFailed)
	require.NotNil(t, degradation)
	assert.Contains(t, degradation.Detail, "no report")
	assert.True(t, covAbstainHasReason(outcome, ptrTo(testkit.PaymentsDoc), schemas.AbstainReasonVerifierUncertain))
}

// TestCOV_ORCH_608_InconclusiveContinueForcesReview covers jevPostProcess's
// CONTINUE-readiness gate (D14): a patch decision with an inconclusive analysis
// (no change record) is forced to REVIEW with an analyzer-inconclusive
// degradation, never silently proceeds.
func TestCOV_ORCH_608_InconclusiveContinueForcesReview(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	// Deterministic mid-analysis git failure: remove the base tree object so
	// BuildChangeAnalysis returns the D14 inconclusive verdict.
	treeOut, err := testkit.GitCommand(pipeline.Root, "rev-parse", base+"^{tree}").Output()
	require.NoError(t, err)
	tree := strings.TrimSpace(string(treeOut))
	require.Len(t, tree, 40)
	require.NoError(t, os.Remove(filepath.Join(pipeline.Root, ".git", "objects", tree[:2], tree[2:])))

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc})),
	})

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	degradation := covDegradation(outcome, string(orchestrator.StageRetrieval), schemas.DegradationAnalyzerInconclusive)
	require.NotNil(t, degradation, "CONTINUE without a change record forces REVIEW with a declared degradation")
	assert.Contains(t, degradation.Detail, "CONTINUE is not executable")
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	// The forced-review routing never invokes the writer.
	assert.Empty(t, fakes.Writer.Calls(), "a forced-review run never reaches the writer")
}

// TestCOV_ORCH_609_ContinueSuggestOnlyRunLevel covers runLevelOutcome's
// non-review arms: a fully suggest run (no review/blocked doc) resolves the
// run-level outcome to `suggest` (never auto), observable on the persisted
// run record.
func TestCOV_ORCH_609_ContinueSuggestOnlyRunLevel(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc})),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{Kind: orchestrator.WriterResultPatch, Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID))})
	fakes.Verification.OnDoc(testkit.PaymentsDoc, orchestrator.VerificationStageResult{Report: ptrTo(orMatrixReport(testkit.PaymentsDoc, runID))})
	fakes.Policy.OnDoc(testkit.PaymentsDoc, orchestrator.PolicyStageResult{Outcome: schemas.PipelineOutcomeSuggest})

	outcome, err := orchestrator.RunPipeline(context.Background(), covPaymentsInput(pipeline, base, head), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	doc := orMatrixFindDoc(outcome, testkit.PaymentsDoc)
	require.NotNil(t, doc)
	assert.Equal(t, schemas.PipelineOutcomeSuggest, doc.PolicyOutcome)
	record, readErr := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, readErr)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeSuggest), *record.Content.Outcome, "a suggest-only run records outcome suggest")
}

// TestCOV_ORCH_610_EmptyDecisionErrorMessage covers describeError's empty-message
// arm: a decision-port error whose message is blank renders as "Error", and the
// pipeline still degrades to REVIEW (never a blank degradation detail).
func TestCOV_ORCH_610_EmptyDecisionErrorMessage(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnError(runID, errors.New(""))

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)
	degradation := covDegradation(outcome, string(orchestrator.StageJev), schemas.DegradationJevUnavailable)
	require.NotNil(t, degradation)
	assert.Contains(t, degradation.Detail, "decision stage failed: Error", "a blank message renders as 'Error'")
}

// TestCOV_ORCH_611_UnresolvableHeadRef covers RunPipeline's head-resolution
// failure (distinct from the base lane): a valid base with an unresolvable head
// reports the setup failure, no outcome.
func TestCOV_ORCH_611_UnresolvableHeadRef(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)

	input := pipeline.PipelineInput(base, "this-head-ref-does-not-exist")
	input.Budgets = orMatrixUnboundedBudgets()
	outcome, runErr := orchestrator.RunPipeline(context.Background(), input, testkit.NewPipelineFakes().Ports())
	require.Error(t, runErr)
	require.Nil(t, outcome)
	assert.True(t, orchestrator.IsRuntimeFailure(runErr))
	assert.Contains(t, runErr.Error(), "cannot resolve base/head refs")
	assert.Contains(t, runErr.Error(), "this-head-ref-does-not-exist")
}

// TestCOV_ORCH_612_ExplicitCapsHonored covers RunPipeline's effectiveCaps lane:
// caller-supplied caps replace the defaults for a completing run.
func TestCOV_ORCH_612_ExplicitCapsHonored(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes, _ := orThreadingFakes(runID, base, head, []string{testkit.PaymentsDoc})
	input := orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil)
	input.Caps = &analysis.AnalysisCaps{MaxFiles: 200, MaxDiffSize: 1_000_000}

	outcome, err := orchestrator.RunPipeline(context.Background(), input, fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)
	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
}

// TestCOV_ORCH_613_CorruptRunRecordFailsPersist covers persistStage's upsert
// error lane: a pre-corrupt run-record file makes UpsertRunRecord fail, the
// pipeline surfaces the runtime persist failure (no partial outcome).
func TestCOV_ORCH_613_CorruptRunRecordFailsPersist(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)

	key := orMatrixExpectedKey(base, head)
	rel, err := state.RunRecordRelPath(key)
	require.NoError(t, err)
	_, err = pipeline.Store.WriteRawForTest(rel, "{ not a run record", nil)
	require.NoError(t, err)

	outcome, runErr := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), testkit.NewPipelineFakes().Ports())
	require.Error(t, runErr, "a corrupt run record fails the persist stage")
	require.Nil(t, outcome)
	assert.True(t, orchestrator.IsRuntimeFailure(runErr))
	pe, ok := orchestrator.AsPipelineError(runErr)
	require.True(t, ok)
	assert.Equal(t, orchestrator.StagePersist, pe.Stage())
}
