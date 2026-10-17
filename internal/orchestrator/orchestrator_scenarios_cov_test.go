package orchestrator_test

// Pipeline-scenario coverage for internal/orchestrator driven through the
// public RunPipeline over the caller-owned payments fixture, plus direct
// EvaluateBaselineShortCircuit calls. Every run asserts observable outcomes
// (returned outcome/error, abstains, degradations, document rows, persisted
// run record) — never bare execution. The fixture clock (testkit.PipelineT0)
// is injected, so no test reads the wall clock.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/statemachine"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

// covAbstainHasReason reports whether the outcome carries a run-level abstain
// (scoped to docPath when non-nil) with the given canonical reason.
func covAbstainHasReason(outcome *schemas.RunOutcome, docPath *string, reason schemas.AbstainReason) bool {
	for _, abstain := range outcome.Abstains {
		if abstain.Reason != string(reason) {
			continue
		}
		if docPath == nil || (abstain.DocPath != nil && *abstain.DocPath == *docPath) {
			return true
		}
	}
	return false
}

func covDegradation(outcome *schemas.RunOutcome, stage string, kind schemas.DegradationKind) *schemas.Degradation {
	for i := range outcome.Degradations {
		if outcome.Degradations[i].Stage == stage && outcome.Degradations[i].Kind == kind {
			return &outcome.Degradations[i]
		}
	}
	return nil
}

func covAutoEntry() schemas.ManifestEntry {
	auto := schemas.DocModeAuto
	return schemas.ManifestEntry{Path: "docs/payments.md", Mode: &auto, Sources: []string{"src/**"}}
}

// covPaymentsInput is the payments-only fixture input (single mapped doc) with
// unbounded stage budgets — the one-candidate continue runs.
func covPaymentsInput(pipeline *testkit.PaymentsPipeline, base, head string) orchestrator.PipelineInput {
	input := pipeline.PipelineInput(base, head)
	input.Budgets = orMatrixUnboundedBudgets()
	return input
}

// TestCOV_ORCH_301_RunPipelineRejectsNonAbsoluteRepoRoot covers RunPipeline's
// git-client setup failure: a relative RepoRoot fails ValidateRepoRoot before
// any stage runs, so the pipeline returns the (runtime) failure, no outcome.
func TestCOV_ORCH_301_RunPipelineRejectsNonAbsoluteRepoRoot(t *testing.T) {
	input := orchestrator.PipelineInput{
		Repo:     "acme/payments",
		RepoRoot: "relative/not/absolute",
		Base:     "base",
		Head:     "head",
		Manifest: schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{covAutoEntry()}},
		Policy:   testkit.PipelinePolicy(1),
		Clock:    func() string { return testkit.PipelineT0 },
	}
	outcome, err := orchestrator.RunPipeline(context.Background(), input, testkit.NewPipelineFakes().Ports())
	require.Error(t, err)
	require.Nil(t, outcome)
	assert.True(t, orchestrator.IsRuntimeFailure(err), "git-client failure is a runtime failure, not a config error")
}

// TestCOV_ORCH_302_RunPipelineUnresolvableBaseRef covers RunPipeline's base/head
// resolution failure: a fresh repo has no commit, so resolving HEAD fails and
// the pipeline reports the setup failure.
func TestCOV_ORCH_302_RunPipelineUnresolvableBaseRef(t *testing.T) {
	orRequireGit(t)
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))
	input := orchestrator.PipelineInput{
		Repo:     "acme/payments",
		RepoRoot: root,
		Base:     "HEAD",
		Head:     "HEAD",
		Manifest: schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{covAutoEntry()}},
		Policy:   testkit.PipelinePolicy(1),
		Clock:    func() string { return testkit.PipelineT0 },
	}
	outcome, err := orchestrator.RunPipeline(context.Background(), input, testkit.NewPipelineFakes().Ports())
	require.Error(t, err)
	require.Nil(t, outcome)
	assert.True(t, orchestrator.IsRuntimeFailure(err))
	assert.Contains(t, err.Error(), "cannot resolve base/head refs")
}

// TestCOV_ORCH_303_ExtractPolicyVersionFailLanes covers extractPolicyVersion's
// fail-closed lanes threaded through RunPipeline: absent, zero, negative,
// non-integer, beyond-int32 and an unmarshalable payload each fail the run
// before any stage executes.
func TestCOV_ORCH_303_ExtractPolicyVersionFailLanes(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)

	cases := []struct {
		name   string
		policy any
	}{
		{"absent", map[string]any{"unrelated": true}},
		{"zero", map[string]any{"policy_version": 0}},
		{"negative", map[string]any{"policy_version": -5}},
		{"non-integer", map[string]any{"policy_version": 1.5}},
		{"beyond-int32", map[string]any{"policy_version": float64(9_000_000_000)}},
		{"unmarshalable", make(chan int)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil)
			input.Policy = tc.policy
			outcome, err := orchestrator.RunPipeline(context.Background(), input, testkit.NewPipelineFakes().Ports())
			require.Error(t, err, "%s policy must fail the run", tc.name)
			assert.Nil(t, outcome)
			pipelineErr, ok := orchestrator.AsPipelineError(err)
			require.True(t, ok, "the failure is a PipelineError")
			assert.Contains(t, pipelineErr.Detail(), "policy_version")
		})
	}
}

// TestCOV_ORCH_304_ExtractPolicyNestedVersionHonored covers extractPolicyVersion's
// nested-lane success through a completing run: policy.policy_version is read
// and routed onto the run key and provenance.
func TestCOV_ORCH_304_ExtractPolicyNestedVersionHonored(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes, _ := orThreadingFakes(runID, base, head, []string{testkit.PaymentsDoc})
	input := orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil)
	input.Policy = map[string]any{"policy": map[string]any{"policy_version": 7}}

	outcome, err := orchestrator.RunPipeline(context.Background(), input, fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)
	assert.Equal(t, 7, outcome.Provenance.PolicyVersion, "the nested policy_version is honored")
	assert.Equal(t, 7, outcome.Key.PolicyVersion)
	assert.Equal(t, base, outcome.Key.BaseSHA)
}

// TestCOV_ORCH_305_DecisionPortRuntimeFailureDegrades covers jevFailure's soft
// lane: a NON-config decision-port error is not a config failure — it abstains
// model_unavailable with a jev-unavailable degradation and routes REVIEW,
// never reaching the writer.
func TestCOV_ORCH_305_DecisionPortRuntimeFailureDegrades(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnError(runID, errors.New("decision port returned 503"))

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err, "a runtime decision failure degrades, it does not fail the run")
	require.NotNil(t, outcome)

	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	assert.True(t, covAbstainHasReason(outcome, nil, schemas.AbstainReasonModelUnavailable))
	degradation := covDegradation(outcome, string(orchestrator.StageJev), schemas.DegradationJevUnavailable)
	require.NotNil(t, degradation)
	assert.Contains(t, degradation.Detail, "decision port returned 503")
	assert.Empty(t, fakes.Writer.Calls(), "a soft jev failure never reaches the writer")
	assert.Empty(t, fakes.Verification.Calls(), "a soft jev failure never reaches verification")
}

// TestCOV_ORCH_306_RelevanceOverflowDegrades covers jevFailure's overflow lane:
// affected-doc fan-out beyond MaxRelevanceDocs abstains cap_exceeded with a
// cap-overflow degradation and routes REVIEW — the decision port is never
// consulted (the bound trips before Decide).
func TestCOV_ORCH_306_RelevanceOverflowDegrades(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)

	fakes := testkit.NewPipelineFakes()
	budgets := orMatrixUnboundedBudgets()
	budgets.MaxRelevanceDocs = ptrTo(1) // two affected docs → overflow

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, budgets, nil), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	assert.True(t, covAbstainHasReason(outcome, nil, schemas.AbstainReasonCapExceeded))
	degradation := covDegradation(outcome, string(orchestrator.StageJev), schemas.DegradationCapOverflow)
	require.NotNil(t, degradation)
	assert.Contains(t, degradation.Detail, "maxDocs=1")
	assert.Empty(t, fakes.Decision.Calls(), "the relevance bound trips before the decision port is consulted")
}

// TestCOV_ORCH_308_ReviewPolicyFiltersAndDowngrades covers the REVIEW policy
// path: manifestContains keeps only allowlisted decision candidates, and a
// policy `auto` outcome with no verification evidence is DOWNGRADED to review
// (policy_gap) while the queue-event id rides onto the abstain record.
func TestCOV_ORCH_308_ReviewPolicyFiltersAndDowngrades(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)
	unmapped := "docs/not-in-manifest.md"

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionReview,
			[]string{testkit.PaymentsDoc, unmapped})),
	})
	fakes.Policy.OnDoc(testkit.PaymentsDoc, orchestrator.PolicyStageResult{
		Outcome:      schemas.PipelineOutcomeAuto,
		QueueEventID: ptrTo("q_cov_123"),
	})

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	// Only the allowlisted candidate becomes a target.
	require.NotNil(t, orMatrixFindDoc(outcome, testkit.PaymentsDoc))
	assert.Equal(t, schemas.PipelineOutcomeReview, orMatrixFindDoc(outcome, testkit.PaymentsDoc).PolicyOutcome,
		"a review-path auto without verification is downgraded to review")
	assert.Nil(t, orMatrixFindDoc(outcome, unmapped), "a non-manifest candidate is filtered out (manifestContains false)")

	// The downgrade abstains policy_gap and carries the queue-event id.
	found := false
	for _, abstain := range outcome.Abstains {
		if abstain.Reason == string(schemas.AbstainReasonPolicyGap) && abstain.DocPath != nil && *abstain.DocPath == testkit.PaymentsDoc {
			found = true
			require.NotNil(t, abstain.QueueEventID)
			assert.Equal(t, "q_cov_123", *abstain.QueueEventID)
		}
	}
	assert.True(t, found, "the downgraded review carries a policy_gap abstain")
}

// TestCOV_ORCH_309_ReviewPolicyPortAbstainMapping covers resolvePolicyTarget's
// port-abstain mapping: a policy port that abstains projects its (mapped)
// reason + detail onto the document abstain verbatim.
func TestCOV_ORCH_309_ReviewPolicyPortAbstainMapping(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionReview, []string{testkit.PaymentsDoc})),
	})
	fakes.Policy.OnDoc(testkit.PaymentsDoc, orchestrator.PolicyStageResult{
		Outcome: schemas.PipelineOutcomeReview,
		Abstain: &orchestrator.AbstainMarker{Reason: schemas.AbstainReasonModelUnavailable, Detail: "policy engine refused to resolve"},
	})

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	found := false
	for _, abstain := range outcome.Abstains {
		if abstain.Reason == string(schemas.AbstainReasonModelUnavailable) && abstain.DocPath != nil && *abstain.DocPath == testkit.PaymentsDoc {
			found = true
			assert.Equal(t, "policy engine refused to resolve", abstain.Detail)
		}
	}
	assert.True(t, found, "the port abstain is mapped onto the run record")
}

// TestCOV_ORCH_310_PolicyPortFailureDegrades covers the policy-stage port
// failure lane: a resolution error abstains model_unavailable, records a
// provider-outage degradation, and marks the policy stage FAILED.
func TestCOV_ORCH_310_PolicyPortFailureDegrades(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionReview, []string{testkit.PaymentsDoc})),
	})
	fakes.Policy.OnError(testkit.PaymentsDoc, errors.New("policy engine timed out"))

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	deg := covDegradation(outcome, string(orchestrator.StagePolicy), schemas.DegradationProviderOutage)
	require.NotNil(t, deg)
	assert.Contains(t, deg.Detail, "policy engine timed out")
	assert.True(t, covAbstainHasReason(outcome, ptrTo(testkit.PaymentsDoc), schemas.AbstainReasonModelUnavailable))
	policy := orMatrixStage(outcome, orchestrator.StagePolicy)
	require.NotNil(t, policy)
	assert.Equal(t, schemas.RunStageFailed, policy.Status, "a policy resolution failure fails the policy stage")
}

// TestCOV_ORCH_311_WriterPatchEdgeContractViolation covers the writer StageContractError
// lane: an injected patch-edge StageContractError is treated as a writer
// abstain (schema_violation) — never entering verification — and the edge-naming
// detail is surfaced.
func TestCOV_ORCH_311_WriterPatchEdgeContractViolation(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	_, patchErr := orchestrator.ParsePatchEdge([]byte(`{"schema":"docsync.doc-patch/v1"}`))
	require.Error(t, patchErr)
	require.True(t, orchestrator.IsStageContractError(patchErr), "the edge parse yields a StageContractError")

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc})),
	})
	fakes.Writer.OnError(testkit.PaymentsDoc, patchErr)

	outcome, err := orchestrator.RunPipeline(context.Background(), covPaymentsInput(pipeline, base, head), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	assert.True(t, covAbstainHasReason(outcome, ptrTo(testkit.PaymentsDoc), schemas.AbstainReasonSchemaViolation))
	deg := covDegradation(outcome, string(orchestrator.StageWriter), schemas.DegradationProviderOutage)
	require.NotNil(t, deg)
	assert.Contains(t, deg.Detail, "patch-edge contract violation (docsync.doc-patch/v1)")
	assert.Empty(t, fakes.Verification.Calls(), "a contract-violating patch never enters verification")
}

// TestCOV_ORCH_312_VerificationEdgeContractViolation covers the verification
// StageContractError lane: an injected report-edge StageContractError abstains
// verifier_uncertain, records the edge-naming verification-failed degradation,
// and fails the verification stage.
func TestCOV_ORCH_312_VerificationEdgeContractViolation(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	_, verErr := orchestrator.ParseVerificationEdge([]byte(`{}`))
	require.Error(t, verErr)
	require.True(t, orchestrator.IsStageContractError(verErr))

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc})),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{Kind: orchestrator.WriterResultPatch, Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID))})
	fakes.Verification.OnError(testkit.PaymentsDoc, verErr)

	outcome, err := orchestrator.RunPipeline(context.Background(), covPaymentsInput(pipeline, base, head), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	deg := covDegradation(outcome, string(orchestrator.StageVerification), schemas.DegradationVerificationFailed)
	require.NotNil(t, deg)
	assert.Contains(t, deg.Detail, "verification-edge contract violation (docsync.verification-report/v1)")
	assert.True(t, covAbstainHasReason(outcome, ptrTo(testkit.PaymentsDoc), schemas.AbstainReasonVerifierUncertain))
	verification := orMatrixStage(outcome, orchestrator.StageVerification)
	require.NotNil(t, verification)
	assert.Equal(t, schemas.RunStageFailed, verification.Status)
}

// TestCOV_ORCH_313_RetrievalLevelCapOverflowDegrades covers the retrieval-stage
// per-level candidate cap: with two relevant docs and MaxDocsInvestigated=1 the
// engine drops the surplus in ranked order and records a cap-overflow
// degradation naming the level and budget (D14 — reported, never silent).
func TestCOV_ORCH_313_RetrievalLevelCapOverflowDegrades(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc, orMatrixArchDoc})),
	})
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{Kind: orchestrator.WriterResultNoop, Detail: "test"})

	budgets := orMatrixUnboundedBudgets()
	budgets.MaxDocsInvestigated = ptrTo(1) // two path-level relevant docs → one dropped

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, budgets, nil), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	deg := covDegradation(outcome, string(orchestrator.StageRetrieval), schemas.DegradationCapOverflow)
	require.NotNil(t, deg, "the retrieval stage records a cap-overflow degradation")
	assert.Contains(t, deg.Detail, "maxDocsInvestigated=1")
	assert.Contains(t, deg.Detail, "beyond the cap dropped")
}

// TestCOV_ORCH_315_AssembleOutcomeModelBindingsAndUsage covers assembleOutcome's
// provenance path: the decision telemetry (model + provider) routes a
// decision-engine binding, the usage seed a usage block, the first applied
// patch a writer binding, and the first verification report's judge a judge
// binding (D3/DX-P2-2 full run-level provenance).
func TestCOV_ORCH_315_AssembleOutcomeModelBindingsAndUsage(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	judgeReport := orMatrixReport(testkit.PaymentsDoc, runID)
	judgeReport.Judge = &schemas.VerificationJudge{Model: "fake-judge-model", Verdict: schemas.VerdictPass}

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.On(runID, orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch, []string{testkit.PaymentsDoc})),
		Telemetry: &orchestrator.StageTelemetry{
			Model:    ptrTo("fake-decision-model"),
			Provider: ptrTo("openrouter"),
			Usage:    &orchestrator.DecisionUsageSeed{PromptTokens: 1200, CompletionTokens: 340, Cost: ptrTo(0.0123)},
		},
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{Kind: orchestrator.WriterResultPatch, Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID))})
	fakes.Verification.OnDoc(testkit.PaymentsDoc, orchestrator.VerificationStageResult{Report: &judgeReport})
	fakes.Policy.OnDoc(testkit.PaymentsDoc, orchestrator.PolicyStageResult{Outcome: schemas.PipelineOutcomeAuto})

	outcome, err := orchestrator.RunPipeline(context.Background(), covPaymentsInput(pipeline, base, head), fakes.Ports())
	require.NoError(t, err)
	require.NotNil(t, outcome)

	roles := map[string]schemas.RunModelBinding{}
	for _, binding := range outcome.Provenance.ModelBindings {
		roles[binding.Role] = binding
	}
	require.Contains(t, roles, "decision-engine")
	assert.Equal(t, "fake-decision-model", roles["decision-engine"].Model)
	require.NotNil(t, roles["decision-engine"].Provider)
	assert.Equal(t, "openrouter", *roles["decision-engine"].Provider)
	require.Contains(t, roles, "writer")
	assert.Equal(t, "anthropic/claude-sonnet-4.5", roles["writer"].Model)
	require.Contains(t, roles, "judge")
	assert.Equal(t, "fake-judge-model", roles["judge"].Model)

	require.NotNil(t, outcome.Usage, "the decision telemetry seeds the usage attribution")
	assert.Equal(t, 1200, outcome.Usage.PromptTokens)
	require.NotNil(t, outcome.Usage.Cost)
	assert.Equal(t, 0.0123, *outcome.Usage.Cost)
	assert.Equal(t, testkit.PipelinePolicyVersion, outcome.Usage.PolicyVersion)
}

// covFailingUpdateSignal is an early-signal sink whose Post succeeds but whose
// Update fails — the signal stays partial while the persisted record is
// unchanged (P5-T08/T11).
type covFailingUpdateSignal struct {
	inner     *testkit.EarlySignalFake
	updateErr error
}

func (s *covFailingUpdateSignal) Post(payload orchestrator.EarlySignalPayload) error {
	return s.inner.Post(payload)
}
func (s *covFailingUpdateSignal) Update(payload orchestrator.EarlySignalPayload) error {
	_ = payload
	return s.updateErr
}

// TestCOV_ORCH_316_EarlySignalUpdateFailureIsProvenancePreserving exercises
// persistStage's signal-update-failure lane: the run record is persisted and
// the returned failure names the signal sink, so signal-sink output never
// silently corrupts the persisted record.
func TestCOV_ORCH_316_EarlySignalUpdateFailureIsProvenancePreserving(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes, _ := orThreadingFakes(runID, base, head, []string{testkit.PaymentsDoc})
	ports := fakes.Ports()
	ports.EarlySignal = &covFailingUpdateSignal{
		inner:     testkit.NewEarlySignalFake(),
		updateErr: errors.New("signal sink update failed"),
	}

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), ports)
	require.Error(t, err, "the signal-update failure is surfaced as a run failure")
	require.Nil(t, outcome)

	pipelineErr, ok := orchestrator.AsPipelineError(err)
	require.True(t, ok)
	assert.Contains(t, pipelineErr.Detail(), "signal sink update failed")
	assert.Equal(t, orchestrator.StagePersist, pipelineErr.Stage())

	// The run record is persisted BEFORE the signal update, so provenance for
	// the attempt is preserved regardless of the sink failure.
	record, readErr := pipeline.Store.ReadRunRecord(orMatrixExpectedKey(base, head))
	require.NoError(t, readErr)
	assert.True(t, record.Exists, "the run record is persisted despite the signal-sink failure")
}

// TestCOV_ORCH_317_EvaluateBaselineShortCircuitRunNoneAndInconclusive exercises
// EvaluateBaselineShortCircuit directly across its non-shipping arms: no state,
// a non-verified state, a verified state with no baseline all run `none`; an
// unreadable doc state is inconclusive (never a guessed short-circuit).
func TestCOV_ORCH_317_EvaluateBaselineShortCircuitRunNoneAndInconclusive(t *testing.T) {
	orRequireGit(t)
	ctx := context.Background()

	t.Run("no-doc-state", func(t *testing.T) {
		pipeline := orMatrixRepo(t)
		base, err := pipeline.CommitAll("base")
		require.NoError(t, err)
		result := orchestrator.EvaluateBaselineShortCircuit(ctx, testkit.PaymentsDoc, pipeline.Store, pipeline.Git, base)
		assert.Equal(t, schemas.BaselineRunNone, result.Run)
		assert.False(t, result.ShortCircuit)
	})

	t.Run("non-verified", func(t *testing.T) {
		pipeline := orMatrixRepo(t)
		base, err := pipeline.CommitAll("base")
		require.NoError(t, err)
		orMatrixSeedDocStateAndBaseline(t, pipeline, schemas.DocStateSuspect, base)
		result := orchestrator.EvaluateBaselineShortCircuit(ctx, testkit.PaymentsDoc, pipeline.Store, pipeline.Git, base)
		assert.Equal(t, schemas.BaselineRunNone, result.Run, "a SUSPECT doc never short-circuits")
		assert.False(t, result.ShortCircuit)
	})

	t.Run("verified-no-baseline", func(t *testing.T) {
		pipeline := orMatrixRepo(t)
		base, err := pipeline.CommitAll("base")
		require.NoError(t, err)
		initialized, err := statemachine.NewDocStateMachine(pipeline.Store).Initialize(statemachine.InitializeStateInput{
			DocPath: testkit.PaymentsDoc, To: schemas.DocStateVerified,
			RunID: testkit.PipelineInitRun, PolicyVersion: testkit.PipelinePolicyVersion, At: testkit.PipelineT0,
		})
		require.NoError(t, err)
		require.True(t, initialized.OK)
		result := orchestrator.EvaluateBaselineShortCircuit(ctx, testkit.PaymentsDoc, pipeline.Store, pipeline.Git, base)
		assert.Equal(t, schemas.BaselineRunNone, result.Run, "a verified doc with no baseline runs `none`")
		assert.False(t, result.ShortCircuit)
	})

	t.Run("corrupt-state-inconclusive", func(t *testing.T) {
		pipeline := orMatrixRepo(t)
		base, err := pipeline.CommitAll("base")
		require.NoError(t, err)
		docStateRel, err := state.DocStateRelPath(testkit.PaymentsDoc)
		require.NoError(t, err)
		_, err = pipeline.Store.WriteRawForTest(docStateRel, "{ corrupt", nil)
		require.NoError(t, err)
		result := orchestrator.EvaluateBaselineShortCircuit(ctx, testkit.PaymentsDoc, pipeline.Store, pipeline.Git, base)
		assert.True(t, result.Inconclusive, "an unreadable doc state is inconclusive, never a guess")
		assert.False(t, result.ShortCircuit)
	})
}
