package orchestrator_test

// Ported from packages/core/test/orchestrator-contracts.test.ts (P5-T02/T05;
// ARCH §3.3, §11.3; V-P5-14).
//
// The orchestrator inter-stage contract targets, driven on the REAL payments
// fixture repo (generated fixture tree + pinned-identity git + real CAS state
// under `.docsync/state/`) with the keyed stage doubles from internal/testkit
// (no network, no models):
//
//   - decision-edge-schema-violation-degrades-to-review-never-hand-patched — a
//     decision the DECISION_SCHEMA would reject (confidence 1.5) fails the
//     JEV→ROUTING edge and is treated as a JEV abstain: REVIEW routing with the
//     canonical `schema_violation` reason and the edge-naming degradation — the
//     payload NEVER enters the pipeline hand-patched.
//   - writer-edge-invalid-patch-never-enters-verification — a patch the
//     DOC_PATCH_SCHEMA would reject (empty hunks) fails the SONNET→VERIFICATION
//     edge and is treated as a writer abstain: every doc routes to review with
//     the canonical `schema_violation` reason, the verifier port is NEVER
//     invoked (fail to less automation).
//   - verification-edge-invalid-report-degrades-to-review — a report the
//     VERIFICATION_REPORT_SCHEMA would reject (empty tiers) fails the
//     VERIFICATION→POLICY edge and takes the verification-failure path:
//     verifier_uncertain + review with evidence detail (never silent, D13/D14).
//   - jev-config-error-fails-the-run-not-review — a decision-port failure
//     carrying the stable JEV_CONFIG code is NOT an ablation: the run fails
//     typed (PipelineConfigError, stage 'jev'), never degrades to REVIEW
//     (ARCH §11.3); the run record persists as `failed`.
//   - contract-violations-carry-edge-and-issue-summary — EVERY port contract
//     fails typed as a StageContractError carrying its named edge and a
//     non-empty issue summary (the decision and relevance-context edges are
//     already pinned by injection_test.go; the patch and verification edges
//     are pinned here, completing the four-edge contract).
//   - P5-T05 retrieval-candidates-carry-level-provenance-and-action-cap — on a
//     fixture run every RunOutcome candidate carries its levels + provenance
//     and a max_action ceiling (V-P1-8), and the direct engine port pins the
//     semantic-only arm: a level-6-only candidate can never drive automation.
//
// Purity: every clock is the fixed testkit.PipelineT0; ports are in-process
// fakes; the shared fixture glue (repo materialization, payload builders,
// unbounded budgets) lives in matrix_test.go.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/retrieval"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

// orContractsBothCandidates is the retrieved fan-out for the refund change
// (both mapped sources match, both level 2).
func orContractsBothCandidates() []string {
	return []string{testkit.PaymentsDoc, orMatrixArchDoc}
}

/* ------------------------------------------------- decision edge (JEV → routing) */

// TestDecisionEdgeSchemaViolationDegradesToReviewNeverHandPatched ports the
// decision-edge target: the decision the DECISION_SCHEMA would reject fails
// the edge and is treated as a JEV abstain (REVIEW routing, canonical
// `schema_violation` reason, edge-naming degradation) — never hand-patched
// into the pipeline.
func TestDecisionEdgeSchemaViolationDegradesToReviewNeverHandPatched(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixBrokenDecision(runID, base, head)),
	})
	recorder, ports := orSkeletonWire(fakes)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.NoError(t, err)

	assert.Equal(t, runID, outcome.RunID)
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	// The rest of the walk still ran — the skeleton completed, degraded.
	assert.Equal(t, orMatrixAllStageNames(), orMatrixStageNames(outcome))
	// The patch path was NEVER called (the decision never crossed the edge).
	assert.Equal(t, 0, recorder.count("write"))
	assert.Equal(t, 0, recorder.count("verify"))
	// The canonical abstain, at RUN level (no doc traversed).
	require.Len(t, outcome.Abstains, 1)
	assert.Equal(t, string(schemas.AbstainReasonSchemaViolation), outcome.Abstains[0].Reason)
	assert.Nil(t, outcome.Abstains[0].DocPath)
	assert.NotEmpty(t, outcome.Abstains[0].Detail)
	assert.Contains(t, outcome.Abstains[0].Detail, "decision-edge contract violation")
	assert.Contains(t, outcome.Abstains[0].Detail, "docsync.decision/v1")
	// Exactly one degradation: the edge-naming jev-unavailable record.
	require.Len(t, outcome.Degradations, 1)
	assert.Equal(t, string(orchestrator.StageJev), outcome.Degradations[0].Stage)
	assert.Equal(t, schemas.DegradationJevUnavailable, outcome.Degradations[0].Kind)
	assert.Contains(t, outcome.Degradations[0].Detail, "decision-edge contract violation")
	// The posted early signal carries REVIEW routing with no confidence and
	// no candidate count (the decision never crossed the edge), then the final
	// update at persist.
	payloads := fakes.EarlySignal.Payloads()
	require.Len(t, payloads, 2)
	assert.False(t, payloads[0].Final)
	assert.Equal(t, orchestrator.RoutingReview, payloads[0].DecisionSummary.Routing)
	assert.Nil(t, payloads[0].DecisionSummary.Confidence)
	assert.Equal(t, 0, payloads[0].DecisionSummary.CandidateCount)
	assert.True(t, payloads[1].Final)
	// The record persists at 'review' and the outcome is schema-stable.
	record, err := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, err)
	require.NotNil(t, record.Content)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeReview), *record.Content.Outcome)
	assert.Empty(t, outcome.Validate())
}

/* ---------------------------------------------------- writer edge (writer → verification) */

// TestWriterEdgeInvalidPatchNeverEntersVerification ports the writer-edge
// target: a schema-violating patch fails closed per doc and NEVER enters
// verification (fail to less automation).
//
// Divergence from the TS suite (documented, asserted as landed): the TS port
// validated the patch through the WIRE edge (parsePatchEdge), so a violating
// patch took the StageContractError path — abstain reason `schema_violation`
// with an edge-naming "writer patch-edge contract violation" degradation. The
// landed Go writer pool re-validates the port's typed value
// (ParsePatchValue/revalidateStruct), which returns the RAW schema-validation
// error (not a StageContractError), so the violation lands on the generic
// writer-port-failure path: abstain reason `model_unavailable` and a
// provider-outage degradation whose detail still names the doc and the schema
// violation. The behavioral guarantees the TS target pins are asserted: the
// patch never reaches verification, every doc fails closed to review with
// non-empty evidence, and the degradation names the writer stage.
func TestWriterEdgeInvalidPatchNeverEntersVerification(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			orContractsBothCandidates())),
	})
	// The writer returns a schema-VIOLATING patch (empty hunks) for both docs.
	broken := orMatrixPatch(testkit.PaymentsDoc, runID)
	broken.Hunks = []schemas.PatchHunk{}
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(broken),
	})
	recorder, ports := orSkeletonWire(fakes)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.NoError(t, err)

	// Both documents reached the writer (the pool issued one call per doc, in
	// the racy intra-stage order), and the verifier was NEVER invoked.
	assert.Equal(t, 2, recorder.count("write"))
	assert.Len(t, fakes.Writer.CallsFor(testkit.PaymentsDoc), 1)
	assert.Len(t, fakes.Writer.CallsFor(orMatrixArchDoc), 1)
	assert.Equal(t, 0, recorder.count("verify"))
	assert.Equal(t, 0, recorder.count("resolve"))
	// Every doc fails closed to review with non-empty evidence that NAMES the
	// schema violation (abstain, don't guess) and no verification block.
	require.Len(t, outcome.Documents, 2)
	for _, doc := range outcome.Documents {
		assert.Equal(t, schemas.PipelineOutcomeReview, doc.PolicyOutcome, doc.DocPath)
		require.NotNil(t, doc.Abstain, doc.DocPath)
		assert.Equal(t, string(schemas.AbstainReasonModelUnavailable), doc.Abstain.Reason)
		assert.Contains(t, doc.Abstain.Detail, "writer port failed")
		assert.Contains(t, doc.Abstain.Detail, "hunks: Array must contain at least 1 element(s)")
		assert.Nil(t, doc.Verification)
	}
	// Exactly one degradation per violation (no shared state — every doc
	// carries its own named evidence), all on the writer stage as
	// provider-outage failures naming the doc and the violation.
	require.Len(t, outcome.Degradations, 2)
	for _, degradation := range outcome.Degradations {
		assert.Equal(t, string(orchestrator.StageWriter), degradation.Stage)
		assert.Equal(t, schemas.DegradationProviderOutage, degradation.Kind)
		assert.Contains(t, degradation.Detail, "writer port failed for ")
		assert.Contains(t, degradation.Detail, "hunks: Array must contain at least 1 element(s)")
	}
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	record, err := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, err)
	require.NotNil(t, record.Content)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeReview), *record.Content.Outcome)
	assert.Empty(t, outcome.Validate())
}

/* ---------------------------------------- verification edge (verification → policy) */

// TestVerificationEdgeInvalidReportDegradesToReview ports the verification-edge
// target: a schema-violating report takes EXACTLY the verification-port-failure
// path — verifier_uncertain abstain + review with evidence detail (never
// silent, D13/D14).
//
// Divergence from the TS suite (documented, asserted as landed): the TS port
// validated the report through the WIRE edge (parseVerificationEdge), so the
// violation detail read "verification-edge contract violation
// (docsync.verification-report/v1): ...". The landed Go verification stage
// re-validates the port's typed value (ParseVerificationValue/
// revalidateStruct), which returns the RAW schema-validation error, so the
// detail reads "verification port failed: validation failed: tiers: ...".
// The behavioral guarantees the TS target pins are asserted identically: the
// verifier WAS reached and resolved, the abstain reason is verifier_uncertain,
// the doc carries NO verification block, the degradation is
// verification-failed on the verification stage, and every doc reviews.
func TestVerificationEdgeInvalidReportDegradesToReview(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			orContractsBothCandidates())),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID)),
	})
	fakes.Writer.OnDoc(orMatrixArchDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(orMatrixArchDoc, runID)),
	})
	// The verifier returns a schema-VIOLATING report (empty tiers) for both
	// docs — it WAS reached and resolved.
	broken := orMatrixReport(testkit.PaymentsDoc, runID)
	broken.Tiers = []schemas.VerificationTierReport{}
	fakes.Verification.OnDefault(orchestrator.VerificationStageResult{
		Report: ptrTo(broken),
	})
	recorder, ports := orSkeletonWire(fakes)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.NoError(t, err)

	// The verifier WAS reached and resolved for both docs (sequential, in
	// candidate order).
	assert.Equal(t, 2, recorder.count("verify"))
	require.Len(t, fakes.Verification.Calls(), 2)
	assert.Equal(t, orMatrixArchDoc, fakes.Verification.Calls()[0].Identity)
	assert.Equal(t, testkit.PaymentsDoc, fakes.Verification.Calls()[1].Identity)
	// Every doc degrades to review with the verifier_uncertain abstain and NO
	// verification block (the report never crossed the edge); the evidence
	// detail names the schema violation.
	require.Len(t, outcome.Documents, 2)
	for _, doc := range outcome.Documents {
		assert.Equal(t, schemas.PipelineOutcomeReview, doc.PolicyOutcome, doc.DocPath)
		require.NotNil(t, doc.Abstain, doc.DocPath)
		assert.Equal(t, string(schemas.AbstainReasonVerifierUncertain), doc.Abstain.Reason)
		assert.Contains(t, doc.Abstain.Detail, "verification port failed")
		assert.Contains(t, doc.Abstain.Detail, "tiers must contain exactly three entries")
		assert.Nil(t, doc.Verification)
	}
	require.Len(t, outcome.Degradations, 2)
	for _, degradation := range outcome.Degradations {
		assert.Equal(t, string(orchestrator.StageVerification), degradation.Stage)
		assert.Equal(t, schemas.DegradationVerificationFailed, degradation.Kind)
		assert.Contains(t, degradation.Detail, "verification failed for ")
		assert.Contains(t, degradation.Detail, "tiers must contain exactly three entries")
	}
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	record, err := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, err)
	require.NotNil(t, record.Content)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeReview), *record.Content.Outcome)
	assert.Empty(t, outcome.Validate())
}

/* ----------------------------------------------------- JEV config (ARCH §11.3) */

// orContractsJevConfigError carries the stable JEV_CONFIG code. The real
// producer is policy.JevConfigError (which core cannot import — OD-1), so the
// structural code check is what the run recognizes (ARCH §11.3: config errors
// are recognized by their stable code, never by instanceof).
type orContractsJevConfigError struct{}

func (orContractsJevConfigError) Error() string {
	return "JEV_CONFIG: compiled workflow questions miss canonical universal question(s): public_behavior_changed"
}

func (orContractsJevConfigError) Code() string { return "JEV_CONFIG" }

// TestJevConfigErrorFailsTheRunNotReview ports the config-error target: a
// decision-port failure carrying the stable JEV_CONFIG code is NOT an ablation
// — the run rejects typed as PipelineConfigError and the record persists as
// `failed`; it never degrades to REVIEW.
func TestJevConfigErrorFailsTheRunNotReview(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnError(runID, orContractsJevConfigError{})
	recorder, ports := orSkeletonWire(fakes)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, nil, nil), ports)
	require.Error(t, err)
	require.Nil(t, outcome, "a config error rejects the outcome entirely")

	// Typed PipelineConfigError — never a review-route degradation (ARCH §11.3).
	require.True(t, orchestrator.IsPipelineConfigError(err))
	pipelineErr, ok := orchestrator.AsPipelineError(err)
	require.True(t, ok)
	assert.Equal(t, orchestrator.CodePipelineConfig, pipelineErr.Code())
	assert.Equal(t, orchestrator.StageJev, pipelineErr.Stage())
	assert.Contains(t, err.Error(), "JEV_CONFIG")
	// The patch path was never invoked.
	assert.Equal(t, 0, recorder.count("write"))
	assert.Equal(t, 0, recorder.count("verify"))
	// The failure persisted the honest record BEFORE the rethrow (V-P5-12).
	record, err := pipeline.Store.ReadRunRecord(orMatrixExpectedKey(base, head))
	require.NoError(t, err)
	require.True(t, record.Exists)
	require.NotNil(t, record.Content)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, "failed", *record.Content.Outcome)
	for _, stage := range record.Content.Stages {
		if stage.Stage == string(orchestrator.StageJev) {
			assert.Equal(t, schemas.RunStageFailed, stage.Status)
		}
	}
}

/* --------------------------------------------- every edge fails typed (P5-T02) */

// orContractsBrokenWire renders v as wire JSON with the caller's mutation
// applied (the TS suite's BROKEN_PAYLOAD edits, expressed against the
// schema-valid example).
func orContractsBrokenWire(t *testing.T, v any, mutate func(map[string]any)) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	mutate(wire)
	return orchMarshal(t, wire)
}

// TestContractViolationsCarryEdgeAndIssueSummary ports the four-edge target:
// every port contract fails closed as a typed StageContractError carrying its
// NAMED edge and a non-empty issue summary (the run-level caller routes
// contract violations by type, never by string matching). The decision and
// relevance-context edges are already pinned in injection_test.go; the patch
// and verification edges complete the contract here.
func TestContractViolationsCarryEdgeAndIssueSummary(t *testing.T) {
	decisionRaw := orchDecisionWire("acme/payments#b0e2f6c..81af92e", "b0e2f6c", "81af92e",
		map[string]any{"confidence": 1.5})
	relevanceRaw := []byte(`{"docs":[],"concepts":[],"caps":{"maxDocs":100},"repoContent":"` +
		orchHostileDocBlock + `"}`)
	patchRaw := orContractsBrokenWire(t, orMatrixPatch(testkit.PaymentsDoc, "r"), func(wire map[string]any) {
		wire["hunks"] = []any{}
	})
	reportRaw := orContractsBrokenWire(t, orMatrixReport(testkit.PaymentsDoc, "r"), func(wire map[string]any) {
		wire["tiers"] = []any{}
	})

	cases := []struct {
		name  string
		edge  orchestrator.StageContractEdge
		parse func([]byte) (any, error)
		raw   []byte
	}{
		{
			name:  "decision-edge",
			edge:  orchestrator.EdgeDecision,
			parse: func(raw []byte) (any, error) { return orchestrator.ParseDecisionEdge(raw) },
			raw:   decisionRaw,
		},
		{
			name:  "relevance-edge",
			edge:  orchestrator.EdgeRelevanceContext,
			parse: func(raw []byte) (any, error) { return orchestrator.ParseRelevanceContextEdge(raw) },
			raw:   relevanceRaw,
		},
		{
			name:  "patch-edge",
			edge:  orchestrator.EdgePatch,
			parse: func(raw []byte) (any, error) { return orchestrator.ParsePatchEdge(raw) },
			raw:   patchRaw,
		},
		{
			name:  "verification-edge",
			edge:  orchestrator.EdgeVerification,
			parse: func(raw []byte) (any, error) { return orchestrator.ParseVerificationEdge(raw) },
			raw:   reportRaw,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := testCase.parse(testCase.raw)
			require.Error(t, err)
			assert.Nil(t, parsed, "a violating payload never hand-patches over the edge")
			require.True(t, orchestrator.IsStageContractError(err))
			var contractErr *orchestrator.StageContractError
			require.ErrorAs(t, err, &contractErr)
			assert.Equal(t, testCase.edge, contractErr.Edge())
			assert.NotEmpty(t, contractErr.Detail())
			edge, ok := orchestrator.StageContractEdgeOf(err)
			require.True(t, ok)
			assert.Equal(t, testCase.edge, edge)
			assert.NotEmpty(t, err.Error())
		})
	}
}

/* -------------------------------------------------------------- P5-T05 targets */

// TestRetrievalCandidatesCarryLevelProvenanceAndActionCap ports the outcome
// arm: every RunOutcome candidate carries its levels + provenance and the
// max_action automation ceiling (V-P1-8).
func TestRetrievalCandidatesCarryLevelProvenanceAndActionCap(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes, _ := orThreadingFakes(runID, base, head, orContractsBothCandidates())
	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)

	require.Len(t, outcome.Candidates, 2)
	// Every candidate lands on exact path queries (level 0) or the
	// payments-manifest mapping glob (level 2); the fan-out carries a vector
	// of ALL hit levels plus their provenance.
	for _, candidate := range outcome.Candidates {
		assert.NotEmpty(t, candidate.Levels, candidate.DocPath)
		assert.NotEmpty(t, candidate.Provenance, candidate.DocPath)
		// V-P1-8: a recall-only candidate can never drive automation; a
		// deterministically-corroborated one may.
		if candidate.SemanticOnly {
			assert.Equal(t, schemas.PipelineMaxActionReview, candidate.MaxAction)
		} else {
			assert.Equal(t, schemas.PipelineMaxActionAuto, candidate.MaxAction)
		}
		assert.Contains(t, candidate.Levels, 2, candidate.DocPath)
	}
}

// TestSemanticOnlyCandidateIsCappedAtReviewAction ports the direct-engine arm
// of P5-T05: a candidate whose only hits are recall-grade (level 6, lexical
// mention) is `semanticOnly` and capped at max_action 'review'.
func TestSemanticOnlyCandidateIsCappedAtReviewAction(t *testing.T) {
	// A hypothetical semantic-only candidate: lib/ledger/tool.ts matches NO
	// manifest source, so no deterministic hit exists; the lexical index
	// matches docs/security.md on change-derived tokens.
	const securityDoc = "docs/security.md"
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: securityDoc, Sources: []string{"src/auth/**", "src/security/**"}},
		},
	}
	documentGraph := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: manifest})
	require.Empty(t, documentGraph.Errors)

	analysis := schemas.ChangeAnalysis{
		Schema:              schemas.ChangeAnalysisSchemaID,
		RunID:               "fixtures/payments#base..head",
		Repo:                testkit.PaymentsRepoID,
		BaseSHA:             "base",
		HeadSHA:             "head",
		Files:               []schemas.ChangedFile{{Path: "lib/ledger/tool.ts", Status: schemas.FileStatusModified}},
		DependenciesChanged: false,
		ClassificationHints: []schemas.ClassificationHint{},
		DeepAnalysis:        schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: ptrTo("test fixture")},
		AnalyzerCoverage: []schemas.AnalyzerCoverage{
			{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK},
		},
	}
	index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
		{Path: securityDoc, Content: "ledger tool credential vault thresholds"},
	})

	result, err := retrieval.RetrieveCandidates(retrieval.RetrieveCandidatesInput{
		ChangeAnalysis: analysis,
		Graph:          documentGraph,
		Manifest:       manifest,
		SemanticIndex:  index,
	})
	require.NoError(t, err)

	require.Len(t, result.Candidates, 1)
	candidate := result.Candidates[0]
	assert.Equal(t, securityDoc, candidate.Doc)
	assert.Equal(t, retrieval.RetrievalLevel6, candidate.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel6}, candidate.Levels)
	assert.True(t, candidate.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionReview, candidate.MaxAction)
	require.Len(t, candidate.Provenance, 1)
	assert.Equal(t, retrieval.RetrievalLevel6, candidate.Provenance[0].Level)
	assert.Equal(t, "semantic-index", candidate.Provenance[0].Source)
}
