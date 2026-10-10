package orchestrator_test

// Ported from packages/core/test/orchestrator-writer-threading.test.ts
// (P5-T06 groundwork).
//
// The writer-stage threading targets, driven on the REAL payments fixture repo
// (generated fixture tree + pinned-identity git + real CAS state under
// `.docsync/state/`) with the keyed stage doubles from internal/testkit (no
// network, no models):
//
//   - writer-port-receives-analysis-decision-relevance-and-expected-hashes —
//     on the CONTINUE route the writer port receives the §3.3 workspace
//     ingredients VERBATIM: the resolved P1 ChangeAnalysis record
//     (`docsync.change-analysis/v1`), the Stage-1 Jev decision it emitted, the
//     SAME bounded RelevanceContext Jev saw (deep-equal), and the
//     orchestrator-computed `expectedBaseContentHashes` — the bare-hex
//     `contentHash` of the doc's head content, never model-supplied (V-P5-14:
//     workspace assembly is application-owned).
//   - unreadable-doc-content-skips-writer-fails-to-review — when the mapping
//     source changed but the mapped doc path is ABSENT from the head tree
//     (declared in the manifest, never committed), the writer port is never
//     invoked; the doc fails closed to `review` with the canonical `other`
//     abstain reason and non-empty evidence detail, plus a
//     `content-unavailable` degradation record (abstain, don't guess — D14).
//
// The keyed doubles RECORD their inputs (KeyedStageCall.Input), so the
// capturing ports of the TS suite map directly onto the recorded call log:
// the writer-input capture is `fakes.Writer.Calls()`, and the relevance the
// decision port saw is `fakes.Decision.Calls()[0].Input.Relevance`.
//
// Purity: every clock is the fixed testkit.PipelineT0; ports are in-process
// fakes; the only fs side effects live under the fixture repo's own
// `.docsync/state/`. The shared fixture glue (repo materialization, payload
// builders, unbounded budgets) lives in matrix_test.go.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

// orThreadingFakes wires the CONTINUE-routed threading scenario: the decision
// port is scripted with the patched decision (the double records the relevance
// context it saw), the writer port answers `noop` (nothing downstream runs),
// and the verification/policy doubles stay UNSHELLED — the keyed dispatch fails
// loud if they are ever reached, which is exactly the TS suite's "unreachable"
// guard (a noop writer never produces a patch to verify or resolve). The
// emitted decision is returned so callers can pin the threaded identity.
func orThreadingFakes(runID, base, head string, candidates []string) (*testkit.PipelineFakes, schemas.Decision) {
	decision := orMatrixDecision(runID, base, head, schemas.ActionPatch, candidates)
	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{Decision: &decision})
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{
		Kind: orchestrator.WriterResultNoop, Detail: "test",
	})
	return fakes, decision
}

// TestWriterPortReceivesAnalysisDecisionRelevanceAndExpectedHashes ports the
// threading closure: the writer port's §3.3 workspace ingredients are the
// application-assembled values, never model-supplied ones.
func TestWriterPortReceivesAnalysisDecisionRelevanceAndExpectedHashes(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes, emitted := orThreadingFakes(runID, base, head, []string{testkit.PaymentsDoc})
	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)

	// The run completes (noop writer → no patches, no degradations); the
	// assertions below target the CAPTURED writer input, not the outcome.
	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	var writerCall *testkit.KeyedStageCall[orchestrator.WriterStageInput]
	for i, call := range fakes.Writer.Calls() {
		if call.Identity == testkit.PaymentsDoc {
			writerCall = &fakes.Writer.Calls()[i]
			break
		}
	}
	require.NotNil(t, writerCall, "a writer call for docs/payments.md is expected")
	captured := writerCall.Input

	// analysis: the resolved P1 ChangeAnalysis record, schema-pinned.
	require.NotNil(t, captured.Analysis)
	assert.Equal(t, schemas.ChangeAnalysisSchemaID, captured.Analysis.Schema)
	// decision: exactly the decision the fake decision port emitted.
	require.NotNil(t, captured.Decision)
	assert.Equal(t, "d_fake_1", emitted.DecisionID)
	assert.Equal(t, emitted.DecisionID, captured.Decision.DecisionID)
	// relevance: deep-equals the bounded context the decision port received.
	require.Len(t, fakes.Decision.Calls(), 1)
	decidedRelevance := fakes.Decision.Calls()[0].Input.Relevance
	assert.Equal(t, decidedRelevance, captured.Relevance)
	// expectedBaseContentHashes: orchestrator-computed head-content hash —
	// recomputed here over the SAME head content via contentHash.
	headBlob, err := pipeline.Git.CatFile(context.Background(), head, testkit.PaymentsDoc)
	require.NoError(t, err)
	require.NotEmpty(t, captured.ExpectedBaseContentHashes[testkit.PaymentsDoc])
	assert.Equal(t, docutil.ContentHash(headBlob.Content), captured.ExpectedBaseContentHashes[testkit.PaymentsDoc])
}

// TestUnreadableDocContentSkipsWriterFailsToReview ports the fail-closed
// content gap: the ghost doc is declared in the run manifest but was NEVER
// committed, so its mapping source changed while the doc is absent from the
// head tree.
func TestUnreadableDocContentSkipsWriterFailsToReview(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	// The mapped doc path is declared in the run manifest but was NEVER
	// committed: the mapping source (src/payments/**) changed while the doc
	// is absent from the head tree.
	const ghostDoc = "docs/ghost.md"
	auto := schemas.DocModeAuto
	input := orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil)
	input.Manifest = schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: ghostDoc, Mode: &auto, Sources: []string{"src/payments/**"}},
		},
	}

	fakes, _ := orThreadingFakes(runID, base, head, []string{ghostDoc})
	outcome, err := orchestrator.RunPipeline(context.Background(), input, fakes.Ports())
	require.NoError(t, err)

	// The writer port was NEVER invoked — unreadable content skips the writer.
	assert.Empty(t, fakes.Writer.Calls())
	// The doc fails closed to review with canonical evidence (abstain, don't
	// guess): reason 'other', non-empty detail.
	doc := orMatrixFindDoc(outcome, ghostDoc)
	require.NotNil(t, doc)
	assert.Equal(t, schemas.PipelineOutcomeReview, doc.PolicyOutcome)
	require.NotNil(t, doc.Abstain)
	assert.Equal(t, string(schemas.AbstainReasonOther), doc.Abstain.Reason)
	assert.NotEmpty(t, doc.Abstain.Detail)
	// The honest degradation record names the writer-stage content gap.
	gap := orMatrixDegradation(outcome, string(orchestrator.StageWriter),
		schemas.DegradationContentUnavailable)
	require.NotNil(t, gap, "content-unavailable degradation expected")
	assert.Contains(t, gap.Detail, "unreadable at head")
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
}
