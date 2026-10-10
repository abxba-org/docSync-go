package orchestrator_test

// Ported from packages/core/test/orchestrator-skeleton.test.ts (P5-T01;
// V-P5-1/5-8/5-11/5-12/5-16 groundwork).
//
// The orchestrator skeleton targets, driven end-to-end on the REAL payments
// fixture repo (generated fixture tree + pinned-identity git + real CAS state
// under `.docsync/state/`) with the keyed stage doubles from internal/testkit
// wrapped by a cross-stage call-order recorder (no network, no models):
//
//   - continue-route-executes-stages-in-canonical-order — the happy path walks
//     analyze→map→invalidate→jev→retrieval→writer→verification→policy→persist
//     in that port-call order, persists the run record under the resolved-SHA
//     5-tuple key, pins the concurrency group + early-signal lifecycle, and the
//     returned outcome is schema-stable (V-P5-16 self-check).
//   - ignore-routed-run-never-invokes-writer (V-P5-1) — the jev→persist
//     shortcut: writer/verification/policy ports are NEVER called and no
//     candidate doc is traversed.
//   - review-route-records-abstain-with-named-reason — an abstained JEV result
//     routes REVIEW, records the canonical abstain reason
//     (`insufficient_confidence`) at run level, and never invokes the writer.
//   - inconclusive-analysis-degrades-never-reports-no-docs-affected (V-P5-8/
//     D14) — git failing mid-analysis (missing tree object) is
//     `analyzer-inconclusive`, the run continues DEGRADED (never "no docs
//     affected", never a failed run).
//   - cap-overflow-marks-partial-coverage (V-P5-11) — patches beyond
//     `maxPatchesPerRun` leave the remaining docs at outcome `review` with
//     `partial_coverage: true` + a `cap-overflow` degradation (V-P5-16 rule 3).
//   - run-record-upsert-is-idempotent-for-the-same-key (V-P5-12 groundwork) —
//     the in-process queue serializes writes, so a true cross-writer CAS
//     conflict is not deterministically reachable through the public API here
//     (the store-level conflict/retry semantics are covered by the state
//     suite); the reachable guarantee is verified instead: a pre-seeded run
//     record for the SAME key is upserted (never duplicated, never corrupting)
//     and the persisted record reads back clean.
//   - concurrency-group-format — the ARCH §12.2 surface string.
//
// Shared fixture glue (the payments repo materialization, the two-doc
// manifest, the golden payload builders and the unbounded-budget record) lives
// in matrix_test.go — this suite adds only the recorder and the per-target
// scenarios, mirroring how the TS files each carried their own local copy.
//
// ALREADY COVERED (no duplicate port): the frozen 10-edge stage list itself is
// pinned by run_test.go's TestDeclareStageGraphIsTheFrozenCanonicalEdgeList,
// and the TS `Object.isFrozen(edges)` assertion has no Go equivalent (a Go
// slice is not frozen; DeclareStageGraph returns a fresh list per call).

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

/* ------------------------------------------- cross-stage call-order recorder */

// orSkeletonPorts wraps the keyed stage doubles with a cross-stage call-order
// recorder (the TS makePorts `counts.seq`), delegating every call to the
// double so the scripted responses stay the single source of truth.
type orSkeletonPorts struct {
	inner *testkit.PipelineFakes
	mu    sync.Mutex
	seq   []string
}

func (p *orSkeletonPorts) record(event string) {
	p.mu.Lock()
	p.seq = append(p.seq, event)
	p.mu.Unlock()
}

// order returns the recorded call sequence.
func (p *orSkeletonPorts) order() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.seq))
	copy(out, p.seq)
	return out
}

func (p *orSkeletonPorts) count(event string) int {
	total := 0
	for _, entry := range p.order() {
		if entry == event {
			total++
		}
	}
	return total
}

func (p *orSkeletonPorts) Decide(input orchestrator.DecisionStageInput) (orchestrator.DecisionStageResult, error) {
	p.record("decide")
	return p.inner.Decision.Decide(input)
}

func (p *orSkeletonPorts) Write(input orchestrator.WriterStageInput) (orchestrator.WriterStageResult, error) {
	p.record("write")
	return p.inner.Writer.Write(input)
}

func (p *orSkeletonPorts) Verify(input orchestrator.VerificationStageInput) (orchestrator.VerificationStageResult, error) {
	p.record("verify")
	return p.inner.Verification.Verify(input)
}

func (p *orSkeletonPorts) Resolve(input orchestrator.PolicyStageInput) (orchestrator.PolicyStageResult, error) {
	p.record("resolve")
	return p.inner.Policy.Resolve(input)
}

func (p *orSkeletonPorts) Post(payload orchestrator.EarlySignalPayload) error {
	p.record("post")
	return p.inner.EarlySignal.Post(payload)
}

func (p *orSkeletonPorts) Update(payload orchestrator.EarlySignalPayload) error {
	p.record("update")
	return p.inner.EarlySignal.Update(payload)
}

// orSkeletonWire wraps the fakes with the recorder on every port.
func orSkeletonWire(fakes *testkit.PipelineFakes) (*orSkeletonPorts, orchestrator.PipelinePorts) {
	recorder := &orSkeletonPorts{inner: fakes}
	return recorder, orchestrator.PipelinePorts{
		Decision:     recorder,
		Writer:       recorder,
		Verification: recorder,
		Policy:       recorder,
		EarlySignal:  recorder,
	}
}

// orSkeletonPhases collapses consecutive duplicate events. The intra-stage
// call order is not part of the data walk (the writer pool issues its calls
// concurrently, so the two `write` events may land in either order), so the
// pin is the stage-phase sequence the TS `counts.seq` proved.
func orSkeletonPhases(seq []string) []string {
	phases := []string{}
	for _, event := range seq {
		if len(phases) == 0 || phases[len(phases)-1] != event {
			phases = append(phases, event)
		}
	}
	return phases
}

/* ------------------------------------------------------- scenario wiring */

// orSkeletonPatchDecision scripts the happy-path patch decision for both
// candidates.
func orSkeletonPatchDecision(fakes *testkit.PipelineFakes, runID, base, head string) {
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			[]string{testkit.PaymentsDoc})),
	})
}

// orSkeletonPatchWriters scripts one schema-valid patch per candidate doc.
func orSkeletonPatchWriters(fakes *testkit.PipelineFakes, runID string) {
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID)),
	})
	fakes.Writer.OnDoc(orMatrixArchDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(orMatrixArchDoc, runID)),
	})
}

// orSkeletonPassingVerifications scripts one passing report per candidate doc.
func orSkeletonPassingVerifications(fakes *testkit.PipelineFakes, runID string) {
	fakes.Verification.OnDoc(testkit.PaymentsDoc, orchestrator.VerificationStageResult{
		Report: ptrTo(orMatrixReport(testkit.PaymentsDoc, runID)),
	})
	fakes.Verification.OnDoc(orMatrixArchDoc, orchestrator.VerificationStageResult{
		Report: ptrTo(orMatrixReport(orMatrixArchDoc, runID)),
	})
	fakes.Policy.OnDefault(orchestrator.PolicyStageResult{Outcome: schemas.PipelineOutcomeAuto})
}

/* -------------------------------------------------------------------- tests */

// TestContinueRouteExecutesStagesInCanonicalOrder ports the happy path: every
// stage walked in PIPELINE_STAGES order with the canonical port-call order,
// the resolved-SHA identity, the PR concurrency group, the early-signal
// lifecycle and the persisted run record.
func TestContinueRouteExecutesStagesInCanonicalOrder(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	orSkeletonPatchDecision(fakes, runID, base, head)
	orSkeletonPatchWriters(fakes, runID)
	orSkeletonPassingVerifications(fakes, runID)
	recorder, ports := orSkeletonWire(fakes)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(),
			&orchestrator.PRContext{Number: 481}), ports)
	require.NoError(t, err)

	// Canonical stage walk: every stage ok, in PIPELINE_STAGES order.
	assert.Equal(t, orMatrixAllStageNames(), orMatrixStageNames(outcome))
	for _, stage := range outcome.Stages {
		assert.Equal(t, schemas.RunStageOk, stage.Status, "stage %s must be ok", stage.Stage)
	}
	// Port-call order proves the DATA walk: decide → post (early signal) →
	// write (candidate fan-out) → verify → resolve → update (final early
	// signal at persist).
	assert.Equal(t, []string{"decide", "post", "write", "verify", "resolve", "update"},
		orSkeletonPhases(recorder.order()))
	assert.Equal(t, 1, recorder.count("decide"))
	assert.Equal(t, 2, recorder.count("write"))
	assert.Equal(t, 2, recorder.count("verify"))
	assert.Equal(t, 2, recorder.count("resolve"))
	assert.Equal(t, 1, recorder.count("post"))
	assert.Equal(t, 1, recorder.count("update"))
	// Both candidates are level 2 (glob sources; no literal source equals the
	// changed path), so the deterministic ranking falls to path ascending:
	// docs/architecture.md sorts before docs/payments.md.
	assert.Equal(t, []string{orMatrixArchDoc, testkit.PaymentsDoc}, orMatrixDocPaths(outcome))
	// Identity: resolved-SHA 5-tuple key + run id (ARCH §4.1 convention).
	assert.Equal(t, orMatrixExpectedKey(base, head), outcome.Key)
	assert.Equal(t, runID, outcome.RunID)
	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	// ARCH §12.2 concurrency group.
	require.NotNil(t, outcome.ConcurrencyGroup)
	assert.Equal(t, orchestrator.ConcurrencyGroupFor(testkit.PaymentsRepoID, 481), *outcome.ConcurrencyGroup)
	// Early-signal lifecycle pinned (posted once, updated once, final).
	require.NotNil(t, outcome.EarlySignal.PostedAt)
	assert.Equal(t, testkit.PipelineT0, *outcome.EarlySignal.PostedAt)
	require.NotNil(t, outcome.EarlySignal.UpdatedAt)
	assert.Equal(t, testkit.PipelineT0, *outcome.EarlySignal.UpdatedAt)
	// Run record persisted under the same key, readable back (CAS store).
	record, err := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, err)
	require.True(t, record.Exists)
	require.NotNil(t, record.Content)
	assert.Equal(t, runID, record.Content.RunID)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeAuto), *record.Content.Outcome)
	assert.Len(t, record.Content.Stages, 9)
	// V-P5-16 self-check: the outcome is schema-stable (runPipeline already
	// rejects an invalid outcome before returning it, so the reachable
	// assertion is the validation itself).
	assert.Empty(t, outcome.Validate())
}

// TestIgnoreRoutedRunNeverInvokesWriter ports V-P5-1: the jev→persist shortcut
// never reaches the patch path.
func TestIgnoreRoutedRunNeverInvokesWriter(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionIgnore, []string{})),
	})
	recorder, ports := orSkeletonWire(fakes)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.NoError(t, err)

	// V-P5-1: the ignore-routed majority never reaches the patch path. The
	// decide/post/update calls still happen — the decision ran and routed
	// IGNORE, and the early signal still posted its final update at persist.
	assert.Equal(t, 0, recorder.count("write"))
	assert.Equal(t, 0, recorder.count("verify"))
	assert.Equal(t, 0, recorder.count("resolve"))
	assert.Equal(t, 1, recorder.count("decide"))
	// Retrieval never ran on the shortcut, so no candidate is traversed.
	assert.Empty(t, outcome.Candidates)
	assert.Empty(t, outcome.Documents)
	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	record, err := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, err)
	require.NotNil(t, record.Content)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeIgnore), *record.Content.Outcome)
	// The routed-around stages are HONEST skipped records.
	for _, stage := range outcome.Stages {
		switch orchestrator.StageName(stage.Stage) {
		case orchestrator.StageRetrieval, orchestrator.StageWriter,
			orchestrator.StageVerification, orchestrator.StagePolicy:
			assert.Equal(t, schemas.RunStageSkipped, stage.Status, "stage %s must be skipped", stage.Stage)
		}
	}
}

// TestReviewRouteRecordsAbstainWithNamedReason ports the review target: an
// abstained JEV result routes REVIEW, records the canonical abstain reason
// (`insufficient_confidence`) at run level, and never invokes the writer.
func TestReviewRouteRecordsAbstainWithNamedReason(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision:      nil,
		Abstained:     true,
		AbstainReason: ptrTo(schemas.AbstainReasonInsufficientConfidence),
	})
	recorder, ports := orSkeletonWire(fakes)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.NoError(t, err)

	assert.Equal(t, 0, recorder.count("write"))
	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	require.Len(t, outcome.Abstains, 1)
	assert.Equal(t, string(schemas.AbstainReasonInsufficientConfidence), outcome.Abstains[0].Reason)
	assert.Nil(t, outcome.Abstains[0].DocPath)
	assert.Regexp(t, regexp.MustCompile(`^ab-[0-9a-f]{16}$`), outcome.Abstains[0].AbstainID)
	writerStage := orMatrixStage(outcome, orchestrator.StageWriter)
	require.NotNil(t, writerStage)
	assert.Equal(t, schemas.RunStageSkipped, writerStage.Status)
	record, err := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, err)
	require.NotNil(t, record.Content)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeReview), *record.Content.Outcome)
}

// TestInconclusiveAnalysisDegradesNeverReportsNoDocsAffected ports V-P5-8/D14:
// the BASE commit's top tree object is removed after both SHAs resolve, so the
// analysis git diff hits a missing object and BuildChangeAnalysis returns the
// D14 inconclusive verdict — the run continues DEGRADED with an honest empty
// candidate set.
func TestInconclusiveAnalysisDegradesNeverReportsNoDocsAffected(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision:      nil,
		Abstained:     true,
		AbstainReason: ptrTo(schemas.AbstainReasonInsufficientConfidence),
	})
	recorder, ports := orSkeletonWire(fakes)

	// Deterministic mid-analysis git failure: remove the BASE commit's top
	// tree object after both SHAs resolve, so diffFiles hits a missing object.
	treeOutput, err := testkit.GitCommand(pipeline.Root, "rev-parse", base+"^{tree}").Output()
	require.NoError(t, err)
	treeSha := strings.TrimSpace(string(treeOutput))
	require.Len(t, treeSha, 40)
	objectPath := filepath.Join(pipeline.Root, ".git", "objects", treeSha[:2], treeSha[2:])
	require.NoError(t, os.Remove(objectPath))

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, nil, nil), ports)
	require.NoError(t, err)

	// Degraded, never failed — the run continued with an honest empty set.
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	// The run CONTINUED past the inconclusive analysis: the decision port was
	// still invoked (and abstained into REVIEW).
	assert.Equal(t, 1, recorder.count("decide"))
	analyzer := orMatrixDegradation(outcome, string(orchestrator.StageAnalyze),
		schemas.DegradationAnalyzerInconclusive)
	require.NotNil(t, analyzer, "analyzer-inconclusive degradation expected")
	assert.Contains(t, analyzer.Detail, "git failure")
	analyzeStage := orMatrixStage(outcome, orchestrator.StageAnalyze)
	require.NotNil(t, analyzeStage)
	assert.Equal(t, schemas.RunStageInconclusive, analyzeStage.Status)
	invalidateStage := orMatrixStage(outcome, orchestrator.StageInvalidate)
	require.NotNil(t, invalidateStage)
	assert.Equal(t, schemas.RunStageSkipped, invalidateStage.Status)
	// No candidate traversed, no fabricated "no docs affected" claim — the
	// degradation record IS the honest signal (documents stay empty).
	assert.Empty(t, outcome.Documents)
	assert.Empty(t, outcome.Findings)
	record, err := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, err)
	require.True(t, record.Exists)
	require.NotNil(t, record.Content)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeReview), *record.Content.Outcome)
}

// TestCapOverflowMarksPartialCoverage ports V-P5-11: with maxPatchesPerRun=1
// the FIRST patch fills the budget and the second doc is routed to review with
// partial coverage plus the cap-overflow degradation.
func TestCapOverflowMarksPartialCoverage(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	orSkeletonPatchDecision(fakes, runID, base, head)
	orSkeletonPatchWriters(fakes, runID)
	orSkeletonPassingVerifications(fakes, runID)
	recorder, ports := orSkeletonWire(fakes)

	budgets := &orchestrator.PipelineBudgetOverrides{MaxPatchesPerRun: ptrTo(1)}
	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, budgets, nil), ports)
	require.NoError(t, err)

	// Two retrieval candidates for the refund change (both level 2): the
	// FIRST patch fills the budget, the second doc is routed to review with
	// partial coverage.
	assert.Equal(t, 1, recorder.count("write"))
	require.Len(t, fakes.Writer.Calls(), 1)
	assert.Equal(t, orMatrixArchDoc, fakes.Writer.Calls()[0].Identity)
	overflowDoc := orMatrixFindDoc(outcome, testkit.PaymentsDoc)
	require.NotNil(t, overflowDoc)
	assert.True(t, overflowDoc.PartialCoverage)
	assert.Equal(t, schemas.PipelineOutcomeReview, overflowDoc.PolicyOutcome)
	capDegradation := orMatrixDegradation(outcome, string(orchestrator.StageWriter),
		schemas.DegradationCapOverflow)
	require.NotNil(t, capDegradation, "cap-overflow degradation expected")
	assert.Contains(t, capDegradation.Detail, "maxPatchesPerRun=1")
}

// TestRunRecordUpsertIsIdempotentForTheSameKey ports the V-P5-12 groundwork: a
// pre-seeded record for the SAME key is upserted (replaced, not duplicated,
// not corrupted) and the churn honesty holds — the record snapshot is taken
// BEFORE its own upsert (a record can never count its own write), while the
// OUTCOME snapshot is taken after persist, so it carries the upsert as one
// state write. No doc state is seeded in this scenario, so the upsert IS the
// only write.
func TestRunRecordUpsertIsIdempotentForTheSameKey(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	key := orMatrixExpectedKey(base, head)

	preseeded := schemas.RunRecord{
		Schema:     schemas.RunRecordSchemaID,
		RunID:      "run_preseed_conflict",
		Key:        key,
		StartedAt:  testkit.PipelineT0,
		StateChurn: schemas.StateChurn{},
		Stages:     []schemas.RunStage{},
	}
	upsert, err := pipeline.Store.UpsertRunRecord(key, preseeded)
	require.NoError(t, err)
	require.True(t, upsert.OK, "pre-seeded run record must land")

	fakes := testkit.NewPipelineFakes()
	orSkeletonPatchDecision(fakes, orMatrixRunID(base, head), base, head)
	orSkeletonPatchWriters(fakes, orMatrixRunID(base, head))
	orSkeletonPassingVerifications(fakes, orMatrixRunID(base, head))
	_, ports := orSkeletonWire(fakes)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.NoError(t, err)

	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	read, err := pipeline.Store.ReadRunRecord(key)
	require.NoError(t, err)
	require.True(t, read.Exists)
	// Upserted (replaced, not duplicated, not corrupted): the pipeline's own
	// record body is what reads back, under the same single file.
	assert.Equal(t, orMatrixRunID(base, head), read.Content.RunID)
	assert.Len(t, read.Content.Stages, 9)
	assert.Equal(t, 1, outcome.StateChurn.Updated)
	assert.Equal(t, 0, outcome.StateChurn.Added)
}

// TestConcurrencyGroupFormat ports the ARCH §12.2 surface string.
func TestConcurrencyGroupFormat(t *testing.T) {
	assert.Equal(t, "docsync-acme/api-481", orchestrator.ConcurrencyGroupFor("acme/api", 481))
}
