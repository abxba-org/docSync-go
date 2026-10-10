package orchestrator_test

// Ported from packages/core/test/orchestrator-matrix.test.ts (P5-T09..T14;
// V-P5-6, V-P5-9; ARCH §3.4, §3.5, §6.1/§7.1).
//
// The orchestrator degradation-matrix + caching + baseline targets, driven
// end-to-end on the REAL payments fixture repo (generated fixture tree +
// pinned-identity git + real CAS state under `.docsync/state/`) with the keyed
// stage doubles from internal/testkit (no network, no models):
//
//   - V-P5-6 verification-reject-downgrades-to-review-with-evidence
//   - V-P5-6 auto-eligible-false-downgrades-auto-to-review-with-block-reasons
//   - V-P5-9 identical-run-key-produces-identical-outcome
//   - budget-exceeded-degradation-on-stage-overrun
//   - parallel-investigation-preserves-candidate-order
//   - baseline-short-circuit-skips-writer-when-verified-baseline-holds
//   - suspect-doc-does-not-short-circuit
//
// Purity: every clock is the fixed testkit.PipelineT0 (no wall clock inside
// logic under test); ports are the in-process keyed doubles, wrapped ONLY
// where the TS suite's inline fakes carried behavior the keyed doubles do not
// model (a decision-stage sleep for the budget overrun, a writer settle-order
// recorder for the concurrency proof); the only fs side effects live under the
// fixture repo's own `.docsync/state/`.

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/statemachine"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

/* ------------------------------------------------------- shared fixture glue */

// orMatrixArchDoc is the payments fixture's second mapped document (the TS
// fixture's `docs/architecture.md`, mapped via `src/**`). The generated Go
// fixture tree carries only docs/payments.md, so the file is written here and
// the run manifest declares it: the two-candidate fan-out the TS matrix drives
// (candidate ordering, sibling routing, cap overflow) needs both.
const orMatrixArchDoc = "docs/architecture.md"

// orMatrixRefundSource is the mapped payments source every scenario changes.
const orMatrixRefundSource = "src/payments/refund.ts"

// orMatrixArchDocContent is the sibling document body (committed in the base
// commit so the writer stage can read it at head).
const orMatrixArchDocContent = "# Payments architecture\n\nReference overview of the payments module.\n"

// orRequireGit skips the suite when no git binary exists (the caller-owned
// repo fixtures are constructed through it).
func orRequireGit(t *testing.T) {
	t.Helper()
	if _, err := lookPathGit(); err != nil {
		t.Skip("git binary unavailable: caller-owned repo fixtures are skipped")
	}
}

// orMatrixRepo materializes the payments fixture under a caller-owned git repo
// with BOTH mapped docs present (TS createPaymentsPipelineRepo + the checked-in
// architecture doc).
func orMatrixRepo(t *testing.T) *testkit.PaymentsPipeline {
	t.Helper()
	orRequireGit(t)
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))
	pipeline, err := testkit.BuildPaymentsPipeline(root)
	require.NoError(t, err)
	require.NoError(t, pipeline.Write(orMatrixArchDoc, orMatrixArchDocContent))
	return pipeline
}

// orMatrixManifest is the two-document allowlist the TS fixture carries:
// payments (auto) ← src/payments/** + src/api/payments/**, architecture
// (suggest) ← src/**.
func orMatrixManifest() schemas.Manifest {
	auto := schemas.DocModeAuto
	suggest := schemas.DocModeSuggest
	return schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{
				Path:    testkit.PaymentsDoc,
				Mode:    &auto,
				Sources: []string{"src/payments/**", "src/api/payments/**"},
			},
			{
				Path:    orMatrixArchDoc,
				Mode:    &suggest,
				Sources: []string{"src/**"},
			},
		},
	}
}

// orMatrixInput builds the run input over the fixture with the two-doc
// manifest, the fixed fixture clock and the caller's budgets/PR context (the
// runner merges the budgets over the defaults).
func orMatrixInput(
	pipeline *testkit.PaymentsPipeline,
	base, head string,
	budgets *orchestrator.PipelineBudgetOverrides,
	pr *orchestrator.PRContext,
) orchestrator.PipelineInput {
	input := pipeline.PipelineInput(base, head)
	input.Manifest = orMatrixManifest()
	input.Budgets = budgets
	input.PR = pr
	return input
}

// orMatrixMaterializeChange is the shared scenario: commit base, apply the
// idempotency-key change, commit head.
func orMatrixMaterializeChange(t *testing.T, pipeline *testkit.PaymentsPipeline) (string, string) {
	t.Helper()
	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)
	refundV1, err := pipeline.Read(orMatrixRefundSource)
	require.NoError(t, err)
	refundV2 := strings.Replace(refundV1, "\tidempotencyKey: string;", "\tidempotencyKey?: string;", 1)
	require.NotEqual(t, refundV1, refundV2, "payments fixture drifted — change did not apply")
	require.NoError(t, pipeline.Write(orMatrixRefundSource, refundV2))
	head, err := pipeline.CommitPaths([]string{orMatrixRefundSource}, "feat: optional idempotencyKey")
	require.NoError(t, err)
	return base, head
}

// orMatrixCommitFormattingSweep is the whitespace-only sweep (indent-bump one
// unique line — a trim-equal removed/added pair): the ChangeAnalysis carries
// the whole-change `formatting_only` hint, so the invalidate stage does NOT
// churn mapped docs into SUSPECT (R10/OD-R38) and a pre-seeded doc state
// survives to the writer stage unchanged.
func orMatrixCommitFormattingSweep(t *testing.T, pipeline *testkit.PaymentsPipeline) (string, string) {
	t.Helper()
	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)
	refundV1, err := pipeline.Read(orMatrixRefundSource)
	require.NoError(t, err)
	refundV2 := strings.Replace(refundV1,
		"\t\tthrow new Error('idempotencyKey is required');",
		"\t\t\tthrow new Error('idempotencyKey is required');", 1)
	require.NotEqual(t, refundV1, refundV2, "payments fixture drifted — sweep did not apply")
	require.NoError(t, pipeline.Write(orMatrixRefundSource, refundV2))
	head, err := pipeline.CommitPaths([]string{orMatrixRefundSource}, "style: whitespace-only sweep")
	require.NoError(t, err)
	return base, head
}

// orMatrixRunID is the ARCH §4.1 run id (`repo#base..head`).
func orMatrixRunID(base, head string) string {
	return testkit.PaymentsRepoID + "#" + base + ".." + head
}

// orMatrixExpectedKey is the resolved-SHA 5-tuple run key.
func orMatrixExpectedKey(base, head string) schemas.RunKey {
	return schemas.RunKey{
		Repo:            testkit.PaymentsRepoID,
		BaseSHA:         base,
		HeadSHA:         head,
		ManifestVersion: 1,
		PolicyVersion:   testkit.PipelinePolicyVersion,
	}
}

// orMatrixUnboundedBudgets mirrors the TS UNBOUNDED_STAGE_BUDGETS: every stage
// unbounded (these runs assert composition, never machine speed).
func orMatrixUnboundedBudgets() *orchestrator.PipelineBudgetOverrides {
	return &orchestrator.PipelineBudgetOverrides{
		StageBudgetMS: map[orchestrator.StageName]*int{
			orchestrator.StageAnalyze:      nil,
			orchestrator.StageMap:          nil,
			orchestrator.StageInvalidate:   nil,
			orchestrator.StageJev:          nil,
			orchestrator.StageRetrieval:    nil,
			orchestrator.StageWriter:       nil,
			orchestrator.StageVerification: nil,
			orchestrator.StagePolicy:       nil,
			orchestrator.StagePersist:      nil,
		},
	}
}

/* ------------------------------------------------------------- fake payloads */

// orMatrixDecision is the TS fakeDecision: the golden decision example with
// the run-scoped identity fields and the caller's action/candidates.
func orMatrixDecision(runID, base, head string, action schemas.Action, candidates []string) schemas.Decision {
	decision := schemas.DecisionExample
	decision.PolicyVersion = testkit.PipelinePolicyVersion
	decision.DecisionID = "d_fake_1"
	decision.Change = schemas.DecisionChange{
		Commit:            head,
		DiffRef:           "git:" + base + ".." + head,
		ChangeAnalysisRef: runID,
	}
	decision.DocumentationImpact = schemas.DocumentationImpactDefinite
	decision.Trace = schemas.DecisionTrace{
		Model:      "fake-decision-model",
		PolicyHash: "sha256:fake",
		LatencyMS:  1,
	}
	decision.Action = action
	decision.Candidates = candidates
	return decision
}

// orMatrixBrokenDecision is the golden decision with an out-of-range
// confidence (1.5): the DECISION_SCHEMA constraint violation the contracts
// suite drives through the decision edge.
func orMatrixBrokenDecision(runID, base, head string) schemas.Decision {
	decision := orMatrixDecision(runID, base, head, schemas.ActionPatch,
		[]string{testkit.PaymentsDoc, orMatrixArchDoc})
	decision.Confidence = 1.5
	return decision
}

// orMatrixPatch is the TS fakePatch: the golden patch example scoped to the
// run and doc.
func orMatrixPatch(docPath, runID string) schemas.DocPatch {
	patch := schemas.DocPatchExample
	patch.RunID = runID
	patch.DocPath = docPath
	patch.Provenance.PolicyVersion = testkit.PipelinePolicyVersion
	patch.Provenance.GeneratedAt = testkit.PipelineT0
	return patch
}

// orMatrixTier builds one all-green tier report over the closed check-id
// vocabulary.
func orMatrixTier(tierNumber int) schemas.VerificationTierReport {
	ids := []string{
		"patch-applies-cleanly", "manifest-allowlist-only", "ownership-floor",
		"minimality-bound", "whole-document-rewrite", "structural-integrity",
		"link-resolution", "examples-parse",
		"claim-decomposition", "claim-anchor-binding", "head-verification",
		"over-deletion-guard", "contract-aware-check", "coverage-defect",
		"judge-second-pass", "judge-independence",
	}
	checks := make([]schemas.VerificationCheck, 0, len(ids))
	for _, id := range ids {
		if schemas.CheckTier[id] != tierNumber {
			continue
		}
		checks = append(checks, schemas.VerificationCheck{ID: id, Status: schemas.CheckPass})
	}
	return schemas.VerificationTierReport{Tier: tierNumber, Status: schemas.CheckPass, Checks: checks}
}

// orMatrixReport is the TS fakeReport: a schema-valid passing report scoped to
// the run and doc.
func orMatrixReport(docPath, runID string) schemas.VerificationReport {
	return schemas.VerificationReport{
		Schema:   "docsync.verification-report/v1",
		RunID:    runID,
		DocPath:  docPath,
		PatchRef: "state/" + docPath + "#proposal-d_fake_1",
		Tiers: []schemas.VerificationTierReport{
			orMatrixTier(schemas.Tier1),
			orMatrixTier(schemas.Tier2),
			orMatrixTier(schemas.Tier3),
		},
		Claims:           []schemas.VerificationClaim{{ClaimID: "claim_182", Status: schemas.ClaimVerdictSupported}},
		Verdict:          schemas.VerdictPass,
		AutoEligible:     true,
		AutoBlockReasons: []string{},
		PolicyVersion:    testkit.PipelinePolicyVersion,
	}
}

// orMatrixRejectReport is the canonical rejecting report (V-P5-6 test 1): tier
// 2 fails with invariant-5 evidence detail on the anchor-binding check ⇒ the
// canonical verdict 'reject' (ResolveVerificationVerdict), auto-eligibility
// cleared with a real AUTO_BLOCK_REASON value (an unanchored claim derives the
// 'unanchored-claims' floor).
func orMatrixRejectReport(docPath, runID string) schemas.VerificationReport {
	report := orMatrixReport(docPath, runID)
	tiers := make([]schemas.VerificationTierReport, 0, len(report.Tiers))
	for _, tier := range report.Tiers {
		if tier.Tier == schemas.Tier2 {
			tier.Status = schemas.CheckFail
			checks := make([]schemas.VerificationCheck, 0, len(tier.Checks))
			for _, check := range tier.Checks {
				if check.ID == "claim-anchor-binding" {
					check.Status = schemas.CheckFail
					check.Detail = strPtr("claim anchor no longer binds at head")
				}
				checks = append(checks, check)
			}
			tier.Checks = checks
		}
		tiers = append(tiers, tier)
	}
	report.Tiers = tiers
	report.Claims = []schemas.VerificationClaim{{ClaimID: "claim_182", Status: schemas.ClaimVerdictUnanchored}}
	report.Verdict = schemas.VerdictReject
	report.AutoEligible = false
	report.AutoBlockReasons = []string{schemas.AutoBlockUnanchoredClaims}
	return report
}

// orMatrixBlockedButPassingReport is the passing but NOT auto-eligible report
// (V-P5-6 test 2): all tiers green ⇒ verdict 'pass' stays canonically valid,
// while the non-empty block-reason set clears auto-eligibility (OD-R09 gate).
func orMatrixBlockedButPassingReport(docPath, runID string) schemas.VerificationReport {
	report := orMatrixReport(docPath, runID)
	report.Claims = []schemas.VerificationClaim{{ClaimID: "claim_182", Status: schemas.ClaimVerdictUnanchored}}
	report.AutoEligible = false
	report.AutoBlockReasons = []string{schemas.AutoBlockUnanchoredClaims}
	return report
}

/* ------------------------------------------------- observing wrapper ports */

// orMatrixDelayedDecision delays every Decide call by delay before delegating
// to the keyed double (the TS fake's decisionDelayMs — the P5-T11 overrun
// trigger; the keyed double dispatches synchronously).
type orMatrixDelayedDecision struct {
	inner orchestrator.DecisionStagePort
	delay time.Duration
}

func (p orMatrixDelayedDecision) Decide(input orchestrator.DecisionStageInput) (orchestrator.DecisionStageResult, error) {
	time.Sleep(p.delay)
	return p.inner.Decide(input)
}

// orMatrixSettleRecorder records the writer-port COMPLETION order (with an
// inverse-to-doc settle delay: the second candidate settles first) and
// delegates to the keyed double for the scripted result.
type orMatrixSettleRecorder struct {
	inner   orchestrator.WriterStagePort
	mu      sync.Mutex
	settled []string
	delay   map[string]time.Duration
}

func (p *orMatrixSettleRecorder) Write(input orchestrator.WriterStageInput) (orchestrator.WriterStageResult, error) {
	if delay, ok := p.delay[input.Doc.DocPath]; ok {
		time.Sleep(delay)
	}
	result, err := p.inner.Write(input)
	p.mu.Lock()
	p.settled = append(p.settled, input.Doc.DocPath)
	p.mu.Unlock()
	return result, err
}

func (p *orMatrixSettleRecorder) order() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.settled))
	copy(out, p.settled)
	return out
}

/* ----------------------------------------------------------- seeding helpers */

// orMatrixSeedDocStateAndBaseline ports the TS seedDocStateAndBaseline: the doc
// state is initialized through the real DocStateMachine and the baseline
// recorded through the real BaselineStore, `baseline_commit` pinned to the
// run's resolved head SHA and `source_files` the literal files the graph's
// forward lookup resolves.
func orMatrixSeedDocStateAndBaseline(
	t *testing.T,
	pipeline *testkit.PaymentsPipeline,
	to schemas.DocStateName,
	baselineCommit string,
) {
	t.Helper()
	machine := statemachine.NewDocStateMachine(pipeline.Store)
	initialized, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       testkit.PaymentsDoc,
		To:            to,
		RunID:         testkit.PipelineInitRun,
		PolicyVersion: testkit.PipelinePolicyVersion,
		At:            testkit.PipelineT0,
	})
	require.NoError(t, err)
	require.True(t, initialized.OK, "doc state initialize failed: %+v", initialized)

	mapping := graph.ForwardLookup(pipeline.Graph, testkit.PaymentsDoc)
	require.NotNil(t, mapping, "payments fixture doc is not mapped")
	allFiles, err := pipeline.Files()
	require.NoError(t, err)
	sourceFiles := []string{}
	for _, file := range allFiles {
		for _, source := range mapping.Sources {
			matches, matchErr := graph.SourceMatchesPath(source, file)
			require.NoError(t, matchErr)
			if matches {
				sourceFiles = append(sourceFiles, file)
				break
			}
		}
	}
	require.NotEmpty(t, sourceFiles, "payments fixture resolved an empty source file set")

	docContent, err := pipeline.Read(testkit.PaymentsDoc)
	require.NoError(t, err)
	baseline := schemas.Baseline{
		DocPath:        testkit.PaymentsDoc,
		DocContentHash: docutil.ContentHash(docContent),
		BaselineCommit: baselineCommit,
		SourceFiles:    sourceFiles,
		SourceSymbols:  []string{},
		ClaimIDs:       []string{},
		VerifiedAt:     testkit.PipelineT0,
		VerifiedByRun:  testkit.PipelineInitRun,
		PolicyVersion:  testkit.PipelinePolicyVersion,
	}
	upsert, err := baselines.NewBaselineStore(pipeline.Store).Upsert(baseline, baselines.BaselineGuards{
		ExpectedDocContentHash: baseline.DocContentHash,
		ExpectedBaselineCommit: baselineCommit,
	})
	require.NoError(t, err)
	require.True(t, upsert.OK, "baseline upsert failed: %s (%s)", upsert.Kind, upsert.Detail)
}

/* --------------------------------------------------------- assertion helpers */

// orMatrixFindDoc returns the outcome document entry for docPath.
func orMatrixFindDoc(outcome *schemas.RunOutcome, docPath string) *schemas.RunDocument {
	for i := range outcome.Documents {
		if outcome.Documents[i].DocPath == docPath {
			return &outcome.Documents[i]
		}
	}
	return nil
}

// orMatrixStage returns the outcome stage trace for stage.
func orMatrixStage(outcome *schemas.RunOutcome, stage orchestrator.StageName) *schemas.PipelineStage {
	for i := range outcome.Stages {
		if outcome.Stages[i].Stage == string(stage) {
			return &outcome.Stages[i]
		}
	}
	return nil
}

// orMatrixDegradation returns the first degradation record for (stage, kind).
func orMatrixDegradation(
	outcome *schemas.RunOutcome, stage string, kind schemas.DegradationKind,
) *schemas.Degradation {
	for i := range outcome.Degradations {
		if outcome.Degradations[i].Stage == stage && outcome.Degradations[i].Kind == kind {
			return &outcome.Degradations[i]
		}
	}
	return nil
}

// orMatrixStageNames lists the outcome's stage names in order.
func orMatrixStageNames(outcome *schemas.RunOutcome) []string {
	names := make([]string, 0, len(outcome.Stages))
	for _, stage := range outcome.Stages {
		names = append(names, stage.Stage)
	}
	return names
}

// orMatrixDecisionTrailJSON renders the outcome with every stage latency_ms
// pinned to 0 (latency is the single wall-clock MEASUREMENT field, P5-T17 —
// the decision trail proper must be byte-identical).
func orMatrixDecisionTrailJSON(t *testing.T, outcome *schemas.RunOutcome) string {
	t.Helper()
	raw, err := json.Marshal(outcome)
	require.NoError(t, err)
	var generic any
	require.NoError(t, json.Unmarshal(raw, &generic))
	root, ok := generic.(map[string]any)
	require.True(t, ok, "outcome JSON is not an object")
	stages, ok := root["stages"].([]any)
	require.True(t, ok, "outcome JSON carries no stages array")
	for _, stage := range stages {
		stageObject, ok := stage.(map[string]any)
		require.True(t, ok, "stage entry is not an object")
		stageObject["latency_ms"] = float64(0)
	}
	trail, err := json.Marshal(root)
	require.NoError(t, err)
	return string(trail)
}

/* -------------------------------------------------------------------- tests */

// TestV_P5_6_VerificationRejectDowngradesToReviewWithEvidence ports the first
// V-P5-6 target: a schema-valid rejecting verification report downgrades the
// policy-resolved 'auto' to 'review' with the canonical verifier_uncertain
// abstain whose detail names the verdict and the block reasons, the
// verification block riding the doc record, and a verification-failed
// degradation — never a silent drop (D13/D14).
func TestV_P5_6_VerificationRejectDowngradesToReviewWithEvidence(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			[]string{testkit.PaymentsDoc, orMatrixArchDoc})),
	})
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID)),
	})
	// The writer result is keyed by doc path: each candidate gets its own
	// patch-shaped result.
	fakes.Writer.OnDoc(orMatrixArchDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(orMatrixArchDoc, runID)),
	})
	fakes.Verification.OnDoc(testkit.PaymentsDoc, orchestrator.VerificationStageResult{
		Report: ptrTo(orMatrixRejectReport(testkit.PaymentsDoc, runID)),
	})
	fakes.Verification.OnDoc(orMatrixArchDoc, orchestrator.VerificationStageResult{
		Report: ptrTo(orMatrixReport(orMatrixArchDoc, runID)),
	})
	fakes.Policy.OnDefault(orchestrator.PolicyStageResult{Outcome: schemas.PipelineOutcomeAuto})

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)

	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	doc := orMatrixFindDoc(outcome, testkit.PaymentsDoc)
	require.NotNil(t, doc)
	assert.Equal(t, schemas.PipelineOutcomeReview, doc.PolicyOutcome)
	require.NotNil(t, doc.Abstain)
	assert.Equal(t, string(schemas.AbstainReasonVerifierUncertain), doc.Abstain.Reason)
	assert.NotEmpty(t, doc.Abstain.Detail)
	assert.Contains(t, doc.Abstain.Detail, "verdict 'reject'")
	assert.Contains(t, doc.Abstain.Detail, "unanchored-claims")
	// The verification evidence rides the doc record — never a silent drop (D13).
	require.NotNil(t, doc.Verification)
	assert.Equal(t, schemas.VerdictReject, doc.Verification.Verdict)
	assert.False(t, doc.Verification.AutoEligible)
	assert.Equal(t, []string{schemas.AutoBlockUnanchoredClaims}, doc.Verification.AutoBlockReasons)
	// The policy-stage degradation names the verdict.
	gate := orMatrixDegradation(outcome, string(orchestrator.StagePolicy), schemas.DegradationVerificationFailed)
	require.NotNil(t, gate, "verification-failed degradation expected")
	assert.Contains(t, gate.Detail, "verdict 'reject'")
	// Run-level abstain evidence for the review routing.
	var abstained *schemas.RunAbstain
	for i := range outcome.Abstains {
		if outcome.Abstains[i].DocPath != nil && *outcome.Abstains[i].DocPath == testkit.PaymentsDoc &&
			outcome.Abstains[i].Reason == string(schemas.AbstainReasonVerifierUncertain) {
			abstained = &outcome.Abstains[i]
			break
		}
	}
	require.NotNil(t, abstained, "run-level verifier_uncertain abstain expected")
	// The rejection is doc-scoped: the eligible sibling keeps its resolved 'auto'.
	sibling := orMatrixFindDoc(outcome, orMatrixArchDoc)
	require.NotNil(t, sibling)
	assert.Equal(t, schemas.PipelineOutcomeAuto, sibling.PolicyOutcome)
}

// TestV_P5_6_AutoEligibleFalseDowngradesAutoToReviewWithBlockReasons ports the
// second V-P5-6 target: a PASSING report that is not auto-eligible downgrades
// the same way — the verdict passes, eligibility gates (OD-R09).
func TestV_P5_6_AutoEligibleFalseDowngradesAutoToReviewWithBlockReasons(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			[]string{testkit.PaymentsDoc, orMatrixArchDoc})),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID)),
	})
	fakes.Writer.OnDoc(orMatrixArchDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(orMatrixArchDoc, runID)),
	})
	fakes.Verification.OnDoc(testkit.PaymentsDoc, orchestrator.VerificationStageResult{
		Report: ptrTo(orMatrixBlockedButPassingReport(testkit.PaymentsDoc, runID)),
	})
	fakes.Verification.OnDoc(orMatrixArchDoc, orchestrator.VerificationStageResult{
		Report: ptrTo(orMatrixReport(orMatrixArchDoc, runID)),
	})
	fakes.Policy.OnDefault(orchestrator.PolicyStageResult{Outcome: schemas.PipelineOutcomeAuto})

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)

	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	doc := orMatrixFindDoc(outcome, testkit.PaymentsDoc)
	require.NotNil(t, doc)
	assert.Equal(t, schemas.PipelineOutcomeReview, doc.PolicyOutcome)
	require.NotNil(t, doc.Abstain)
	assert.Equal(t, string(schemas.AbstainReasonVerifierUncertain), doc.Abstain.Reason)
	assert.Contains(t, doc.Abstain.Detail, "unanchored-claims")
	// Verdict 'pass' but not auto-eligible — the OD-R09 gate alone downgrades.
	require.NotNil(t, doc.Verification)
	assert.Equal(t, schemas.VerdictPass, doc.Verification.Verdict)
	assert.False(t, doc.Verification.AutoEligible)
	assert.Equal(t, []string{schemas.AutoBlockUnanchoredClaims}, doc.Verification.AutoBlockReasons)
	gate := orMatrixDegradation(outcome, string(orchestrator.StagePolicy), schemas.DegradationVerificationFailed)
	require.NotNil(t, gate, "verification-failed degradation expected")
	assert.Contains(t, gate.Detail, "unanchored-claims")
	sibling := orMatrixFindDoc(outcome, orMatrixArchDoc)
	require.NotNil(t, sibling)
	assert.Equal(t, schemas.PipelineOutcomeAuto, sibling.PolicyOutcome)
}

// TestV_P5_9_IdenticalRunKeyProducesIdenticalOutcome ports the idempotency
// target: the same run key re-executed on fresh identical fixtures produces
// the identical decision trail (D6/ARCH §3.5). Every generated field comes
// from the injected clock; latency_ms is the single wall-clock MEASUREMENT
// field (P5-T17) and is pinned before the deep-equal.
func TestV_P5_9_IdenticalRunKeyProducesIdenticalOutcome(t *testing.T) {
	orRequireGit(t)
	repoA := orMatrixRepo(t)
	repoB := orMatrixRepo(t)
	changeABase, changeAHead := orMatrixMaterializeChange(t, repoA)
	changeBBase, changeBHead := orMatrixMaterializeChange(t, repoB)

	runOne := func(pipeline *testkit.PaymentsPipeline, base, head string) *schemas.RunOutcome {
		t.Helper()
		runID := orMatrixRunID(base, head)
		fakes := testkit.NewPipelineFakes()
		fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
			Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
				[]string{testkit.PaymentsDoc, orMatrixArchDoc})),
		})
		fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{
			Kind:  orchestrator.WriterResultPatch,
			Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID)),
		})
		fakes.Writer.OnDoc(orMatrixArchDoc, orchestrator.WriterStageResult{
			Kind:  orchestrator.WriterResultPatch,
			Patch: ptrTo(orMatrixPatch(orMatrixArchDoc, runID)),
		})
		fakes.Verification.OnDoc(testkit.PaymentsDoc, orchestrator.VerificationStageResult{
			Report: ptrTo(orMatrixReport(testkit.PaymentsDoc, runID)),
		})
		fakes.Verification.OnDoc(orMatrixArchDoc, orchestrator.VerificationStageResult{
			Report: ptrTo(orMatrixReport(orMatrixArchDoc, runID)),
		})
		fakes.Policy.OnDefault(orchestrator.PolicyStageResult{Outcome: schemas.PipelineOutcomeAuto})
		outcome, err := orchestrator.RunPipeline(context.Background(),
			orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
		require.NoError(t, err)
		return outcome
	}
	outcomeA := runOne(repoA, changeABase, changeAHead)
	outcomeB := runOne(repoB, changeBBase, changeBHead)

	// Same run key: the pinned fixture identity resolves identical SHAs.
	assert.Equal(t, outcomeA.Key, outcomeB.Key)
	assert.Equal(t, orMatrixExpectedKey(changeABase, changeAHead), outcomeB.Key)
	assert.Equal(t, outcomeA.RunID, outcomeB.RunID)
	// The decision trail proper must be byte-identical.
	assert.Equal(t, orMatrixDecisionTrailJSON(t, outcomeA), orMatrixDecisionTrailJSON(t, outcomeB))
}

// TestBudgetExceededDegradationOnStageOverrun ports the P5-T11 target: a 0ms
// jev budget with a genuinely slow decision port emits the budget-exceeded
// degradation, keeps the stage status 'ok' (over-budget-but-successful),
// degrades the run status, and the run COMPLETES (honest bookkeeping).
func TestBudgetExceededDegradationOnStageOverrun(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			[]string{testkit.PaymentsDoc, orMatrixArchDoc})),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(testkit.PaymentsDoc, runID)),
	})
	fakes.Writer.OnDoc(orMatrixArchDoc, orchestrator.WriterStageResult{
		Kind:  orchestrator.WriterResultPatch,
		Patch: ptrTo(orMatrixPatch(orMatrixArchDoc, runID)),
	})
	fakes.Verification.OnDoc(testkit.PaymentsDoc, orchestrator.VerificationStageResult{
		Report: ptrTo(orMatrixReport(testkit.PaymentsDoc, runID)),
	})
	fakes.Verification.OnDoc(orMatrixArchDoc, orchestrator.VerificationStageResult{
		Report: ptrTo(orMatrixReport(orMatrixArchDoc, runID)),
	})
	fakes.Policy.OnDefault(orchestrator.PolicyStageResult{Outcome: schemas.PipelineOutcomeAuto})

	ports := fakes.Ports()
	ports.Decision = orMatrixDelayedDecision{inner: ports.Decision, delay: 5 * time.Millisecond}

	// 0ms jev wall-time budget over the default record (P5-T11 overrun).
	budgets := &orchestrator.PipelineBudgetOverrides{
		StageBudgetMS: map[orchestrator.StageName]*int{orchestrator.StageJev: ptrTo(0)},
	}

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, budgets, nil), ports)
	require.NoError(t, err)

	// The decision double saw exactly one call, keyed by the run id.
	assert.Len(t, fakes.Decision.CallsFor(runID), 1)
	// The over-budget-but-SUCCESSFUL stage keeps its status; the visible
	// degradation record drives the run status (P5-T11 honest bookkeeping).
	budget := orMatrixDegradation(outcome, string(orchestrator.StageJev), schemas.DegradationBudgetExceeded)
	require.NotNil(t, budget, "budget-exceeded degradation expected")
	assert.Contains(t, budget.Detail, "budget 0ms")
	jev := orMatrixStage(outcome, orchestrator.StageJev)
	require.NotNil(t, jev)
	assert.Equal(t, schemas.RunStageOk, jev.Status)
	require.NotNil(t, jev.Note)
	assert.Contains(t, *jev.Note, "budget exceeded (P5-T11)")
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	// The run COMPLETES — the degradation is bookkeeping, never an abort.
	assert.Equal(t, orMatrixAllStageNames(), orMatrixStageNames(outcome))
	record, err := pipeline.Store.ReadRunRecord(outcome.Key)
	require.NoError(t, err)
	require.True(t, record.Exists)
	require.NotNil(t, record.Content)
	require.NotNil(t, record.Content.Outcome)
	assert.Equal(t, string(schemas.PipelineOutcomeAuto), *record.Content.Outcome)
}

// TestParallelInvestigationPreservesCandidateOrder ports the P5-T12/OD-R10
// target: with the writer pool at maxConcurrentDocs 2 and settle delays
// inverse to candidate index, the settled results apply strictly in candidate
// order, never completion order.
func TestParallelInvestigationPreservesCandidateOrder(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixMaterializeChange(t, pipeline)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			[]string{testkit.PaymentsDoc, orMatrixArchDoc})),
	})
	fakes.Writer.OnDoc(testkit.PaymentsDoc, orchestrator.WriterStageResult{
		Kind: orchestrator.WriterResultNoop, Detail: "noop-payments",
	})
	fakes.Writer.OnDoc(orMatrixArchDoc, orchestrator.WriterStageResult{
		Kind: orchestrator.WriterResultNoop, Detail: "noop-architecture",
	})

	// Inverse-to-index settle delay: the SECOND candidate settles first, so
	// any completion-order application would invert the documents order.
	recorder := &orMatrixSettleRecorder{
		inner: fakes.Writer,
		delay: map[string]time.Duration{
			orMatrixArchDoc:     30 * time.Millisecond,
			testkit.PaymentsDoc: 5 * time.Millisecond,
		},
	}
	ports := fakes.Ports()
	ports.Writer = recorder

	budgets := orMatrixUnboundedBudgets()
	budgets.MaxConcurrentDocs = ptrTo(2)

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, budgets, nil), ports)
	require.NoError(t, err)

	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	// Both candidates accepted a writer session (the keyed double recorded
	// one call per doc path).
	assert.Len(t, fakes.Writer.CallsFor(testkit.PaymentsDoc), 1)
	assert.Len(t, fakes.Writer.CallsFor(orMatrixArchDoc), 1)
	// The delays actually inverted completion (the second call settled
	// first) — making the ordering assertion below a real concurrency proof.
	assert.Equal(t, []string{testkit.PaymentsDoc, orMatrixArchDoc}, recorder.order())
	// Settled results apply in candidate order, never completion order (P5-T12).
	assert.Equal(t, []string{orMatrixArchDoc, testkit.PaymentsDoc}, orMatrixDocPaths(outcome))
	assert.Equal(t, []string{orMatrixArchDoc, testkit.PaymentsDoc}, orMatrixCandidatePaths(outcome))
}

// TestBaselineShortCircuitSkipsWriterWhenVerifiedBaselineHolds ports the §6.1
// short-circuit target: a doc seeded VERIFIED with its baseline pinned to the
// run's resolved head SHA short-circuits on the exact-sha ladder rung (D12) —
// no writer session for that doc, policy_outcome 'ignore', baseline_run
// 'exact-sha'.
func TestBaselineShortCircuitSkipsWriterWhenVerifiedBaselineHolds(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixCommitFormattingSweep(t, pipeline)
	// Doc state VERIFIED + baseline pinned to the run's resolved head SHA,
	// written through the real DocStateMachine/BaselineStore write APIs (§6.1).
	orMatrixSeedDocStateAndBaseline(t, pipeline, schemas.DocStateVerified, head)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			[]string{testkit.PaymentsDoc, orMatrixArchDoc})),
	})
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{
		Kind: orchestrator.WriterResultNoop, Detail: "test",
	})

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)

	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	// The R10 skip held: the VERIFIED state survived invalidate unchanged.
	stateAfter, err := statemachine.NewDocStateMachine(pipeline.Store).GetState(testkit.PaymentsDoc)
	require.NoError(t, err)
	require.NotNil(t, stateAfter)
	assert.Equal(t, schemas.DocStateVerified, stateAfter.State)
	// The mapped payments doc IS a retrieval candidate (its sources changed)
	// but its VERIFIED baseline still holds at head → NO writer session.
	assert.Empty(t, fakes.Writer.CallsFor(testkit.PaymentsDoc))
	// The stateless sibling still went through the writer (noop).
	assert.Len(t, fakes.Writer.CallsFor(orMatrixArchDoc), 1)
	doc := orMatrixFindDoc(outcome, testkit.PaymentsDoc)
	require.NotNil(t, doc)
	assert.Equal(t, schemas.PipelineOutcomeIgnore, doc.PolicyOutcome)
	require.NotNil(t, doc.BaselineRun)
	assert.Equal(t, schemas.BaselineRunExactSHA, *doc.BaselineRun)
}

// TestSuspectDocDoesNotShortCircuit ports the §6.2 target: the identical setup
// with doc state SUSPECT — the writer IS invoked (fail to more investigation);
// a baseline proves only a VERIFIED doc current.
func TestSuspectDocDoesNotShortCircuit(t *testing.T) {
	orRequireGit(t)
	pipeline := orMatrixRepo(t)
	base, head := orMatrixCommitFormattingSweep(t, pipeline)
	// Identical setup to the VERIFIED scenario except the doc state: SUSPECT
	// never short-circuits even with a baseline pinned to head.
	orMatrixSeedDocStateAndBaseline(t, pipeline, schemas.DocStateSuspect, head)
	runID := orMatrixRunID(base, head)

	fakes := testkit.NewPipelineFakes()
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{
		Decision: ptrTo(orMatrixDecision(runID, base, head, schemas.ActionPatch,
			[]string{testkit.PaymentsDoc, orMatrixArchDoc})),
	})
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{
		Kind: orchestrator.WriterResultNoop, Detail: "test",
	})

	outcome, err := orchestrator.RunPipeline(context.Background(),
		orMatrixInput(pipeline, base, head, orMatrixUnboundedBudgets(), nil), fakes.Ports())
	require.NoError(t, err)

	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	// The SUSPECT doc was investigated by the writer.
	assert.Len(t, fakes.Writer.CallsFor(testkit.PaymentsDoc), 1)
	doc := orMatrixFindDoc(outcome, testkit.PaymentsDoc)
	require.NotNil(t, doc)
	require.NotNil(t, doc.BaselineRun)
	assert.Equal(t, schemas.BaselineRunNone, *doc.BaselineRun)
	assert.Equal(t, schemas.PipelineOutcomeIgnore, doc.PolicyOutcome)
}

/* ------------------------------------------------------------ small helpers */

// lookPathGit resolves the git binary on PATH (the fixture repos are built
// through it).
func lookPathGit() (string, error) { return exec.LookPath("git") }

// ptrTo returns a pointer to v (optional members; a zero value cannot express
// "unset" in Go).
func ptrTo[T any](v T) *T { return &v }

// strPtr returns a pointer to s.
func strPtr(s string) *string { return &s }

// orMatrixAllStageNames is the frozen PipelineStages list.
func orMatrixAllStageNames() []string {
	names := make([]string, 0, len(orchestrator.PipelineStages))
	for _, stage := range orchestrator.PipelineStages {
		names = append(names, string(stage))
	}
	return names
}

// orMatrixDocPaths lists the outcome's document paths in order.
func orMatrixDocPaths(outcome *schemas.RunOutcome) []string {
	paths := make([]string, 0, len(outcome.Documents))
	for _, doc := range outcome.Documents {
		paths = append(paths, doc.DocPath)
	}
	return paths
}

// orMatrixCandidatePaths lists the outcome's candidate paths in order.
func orMatrixCandidatePaths(outcome *schemas.RunOutcome) []string {
	paths := make([]string, 0, len(outcome.Candidates))
	for _, candidate := range outcome.Candidates {
		paths = append(paths, candidate.DocPath)
	}
	return paths
}
