package policy_test

// Ported from packages/policy/test/orchestrator-policy-stage.test.ts (P5-T08)
// — outcome mapping across the full §5.2 outcome set plus review, first-class
// review-queue writes with evidence, and the verification-null passthrough
// (the engine's own verdict, the adapter adds nothing). The bootstrap policy
// v1 workflow is the fixture (calibrated data: high_confidence 0.94 / review
// 0.72 / floors 0.94, D11). Structural-compatibility pins mirror the
// decision-stage probe pattern — @docsync/policy must NOT import
// @docsync/core (OD-1). No network, no clocks: the queue is an in-memory
// double; nothing here reads time.

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* -------------------------------------------------------------- fixtures */

// policyStageManifest is the TS MANIFEST fixture.
func policyStageManifest() schemas.Manifest {
	return schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/payments.md", Mode: docModePtr(schemas.DocModeAuto), Sources: []string{"src/payments/**"}},
			{Path: "docs/locked.md", Mode: docModePtr(schemas.DocModeLocked), Sources: []string{"src/locked/**"}},
			{Path: "docs/suggest.md", Mode: docModePtr(schemas.DocModeSuggest), Sources: []string{"src/suggest/**"}},
		},
	}
}

// policyStageChange is the TS CHANGE fixture.
var policyStageChange = schemas.DecisionChange{
	Commit:            "81af92e",
	DiffRef:           "git:81af92e",
	ChangeAnalysisRef: "analysis/01J9ZK6X8A",
}

// policyStageEvidence is the TS EVIDENCE fixture.
var policyStageEvidence = []schemas.Evidence{
	{
		Kind: schemas.EvidenceKindSymbolDiff,
		Ref:  "src/payments/refund.ts:42",
		Note: "idempotencyKey became optional",
	},
}

// policyStageDecisionSpec is the TS DecisionSpec.
type policyStageDecisionSpec struct {
	confidence        *float64
	risk              *schemas.RiskLevel
	changeKind        *schemas.ChangeKind
	impact            *schemas.DocumentationImpact
	impactProbability *float64
	abstain           *bool
	abstainReason     *schemas.AbstainReason
	decisionID        *string
}

// policyStageDecision emits a schema-valid Stage-1 decision through the real
// emission gate (the TS decision() helper — no hand-built objects).
func policyStageDecision(t *testing.T, compiled *policy.CompiledPolicyWorkflow, spec policyStageDecisionSpec) *schemas.Decision {
	t.Helper()
	impact := schemas.DocumentationImpactPossible
	if spec.impact != nil {
		impact = *spec.impact
	}
	impactProbability := 0.96
	if spec.impactProbability != nil {
		impactProbability = *spec.impactProbability
	}
	changeKind := schemas.ChangeKindBehavioral
	if spec.changeKind != nil {
		changeKind = *spec.changeKind
	}
	risk := schemas.RiskLevelLow
	if spec.risk != nil {
		risk = *spec.risk
	}
	confidence := 0.96
	if spec.confidence != nil {
		confidence = *spec.confidence
	}
	abstain := false
	if spec.abstain != nil {
		abstain = *spec.abstain
	}

	answers := policy.JevAnswers{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.87},
		"api_contract_changed":    {Value: json.RawMessage("false"), Probability: 0.12},
		"affected_doc_count":      {Value: json.RawMessage("1"), Probability: 0.9},
		"documentation_impact": {
			Value:       json.RawMessage(`"` + string(impact) + `"`),
			Probability: impactProbability,
		},
	}

	decision, err := policy.EmitDecision(policy.EmitDecisionInput{
		Compiled: compiled,
		Change:   policyStageChange,
		Answers:  answers,
		Body: policy.EmitDecisionBody{
			ChangeKind:          changeKind,
			DocumentationImpact: impact,
			Risk:                risk,
			Confidence:          confidence,
			Action:              schemas.ActionReview,
			Abstain:             abstain,
			AbstainReason:       spec.abstainReason,
		},
		Candidates:       []string{"docs/payments.md"},
		ManifestDocPaths: []string{"docs/payments.md", "docs/locked.md", "docs/suggest.md"},
		Evidence:         policyStageEvidence,
		RoutedModel:      "z-ai/glm-4.7",
		LatencyMs:        120,
		DecisionID:       spec.decisionID,
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	return decision
}

// policyStageVerdictReport builds a schema-valid verification report with the
// given verdict (the TS reportWithVerdict helper: the canonical mapping is
// respected — the report is parsed through the real schema gate).
func policyStageVerdictReport(t *testing.T, verdict schemas.VerificationVerdict) *schemas.VerificationReport {
	t.Helper()
	tier3Status := "pass"
	secondPass := "pass"
	if verdict == schemas.VerdictReview {
		tier3Status = "skipped"
		secondPass = "skipped"
	}
	raw, err := json.Marshal(map[string]any{
		"schema":    "docsync.verification-report/v1",
		"run_id":    "acme/payments#base..head",
		"doc_path":  "docs/payments.md",
		"patch_ref": "state/docs/docs__payments.md#patch-0000000000000000",
		"tiers": []map[string]any{
			{"tier": 1, "status": "pass", "checks": []map[string]any{{"id": "patch-applies-cleanly", "status": "pass"}}},
			{"tier": 2, "status": "pass", "checks": []map[string]any{{"id": "claim-decomposition", "status": "pass"}}},
			{"tier": 3, "status": tier3Status, "checks": []map[string]any{
				{"id": "judge-independence", "status": "pass"},
				{"id": "judge-second-pass", "status": secondPass},
			}},
		},
		"claims":             []any{},
		"verdict":            string(verdict),
		"auto_eligible":      true,
		"auto_block_reasons": []string{},
		"policy_version":     1,
	})
	require.NoError(t, err)
	if verdict == schemas.VerdictReview {
		// The judge abstention that routes the verdict to review.
		var document map[string]any
		require.NoError(t, json.Unmarshal(raw, &document))
		document["judge_abstain"] = map[string]any{
			"reason": "other",
			"detail": "tier 3 skipped: no patched content (tier-1 apply did not produce text)",
		}
		raw, err = json.Marshal(document)
		require.NoError(t, err)
	}
	report, err := schemas.ParseVerificationReport(raw)
	require.NoError(t, err)
	require.NotNil(t, report)
	return report
}

// policyStageRecordingQueue is the TS recordingQueue double: a recording queue
// over the Phase-2 AbstainQueue seam (enqueue capture only) that mirrors the
// real queue's two-tier discipline — operational reasons are never queueable.
type policyStageRecordingQueue struct {
	mu     sync.Mutex
	inputs []policy.RecordAbstainInput
	ids    []string
}

var _ policy.AbstainQueuePick = (*policyStageRecordingQueue)(nil)

func (q *policyStageRecordingQueue) RecordAbstain(input policy.RecordAbstainInput) (policy.AbstainEvent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inputs = append(q.inputs, input)
	// The real queue's two-tier discipline, mirrored: operational reasons are
	// never queueable — fail closed, never stored.
	if !policyStageIsDecisionLevelReason(input.Reason) {
		return policy.AbstainEvent{}, errors.New("operational reason bypasses the queue: " + string(input.Reason))
	}
	event := policy.AbstainEvent{
		EventID:  "a_" + padStart4(strconv.Itoa(len(q.ids))),
		Decision: input.Decision,
		Reason:   input.Reason,
		Evidence: append([]schemas.Evidence(nil), input.Evidence...),
	}
	q.ids = append(q.ids, event.EventID)
	return event, nil
}

// padStart4 zero-pads to width 4 (the TS `String(inputs.length).padStart(4,
// '0')` deterministic event-id scheme).
func padStart4(value string) string {
	if len(value) >= 4 {
		return value
	}
	return strings.Repeat("0", 4-len(value)) + value
}

func (q *policyStageRecordingQueue) recordedInputs() []policy.RecordAbstainInput {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]policy.RecordAbstainInput(nil), q.inputs...)
}

func policyStageIsDecisionLevelReason(reason schemas.AbstainReason) bool {
	for _, candidate := range schemas.DecisionLevelAbstainReasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

func policyStageCtx(manifest schemas.Manifest) policy.PolicyStageRunContext {
	return policy.PolicyStageRunContext{Manifest: manifest}
}

/* ------------------------- risk-class overrides + fail-loud propagation */

// policyStageOverridesCompiled compiles the bootstrap policy with a
// per-doc-glob risk_class_overrides table (D11 data) — the adapter's single
// derivation point for the conservative-max override input.
func policyStageOverridesCompiled(t *testing.T) *policy.CompiledPolicyWorkflow {
	t.Helper()
	wire := stagePolicyWire(t, policy.BootstrapPolicyV1)
	wire["risk_class_overrides"] = map[string]any{"docs/zone/**": "high"}
	result := policy.CompilePolicy(wire)
	require.True(t, result.OK, "fixture drift: the overrides fixture must compile: %+v", result.Diagnostics)
	require.NotNil(t, result.Compiled)
	return result.Compiled
}

// policyStageGlobMatcher is the injected glob matcher double (the glob engine
// is @docsync/core's; OD-1): a prefix matcher over a `**` glob.
func policyStageGlobMatcher(path, glob string) bool {
	return strings.HasPrefix(path, strings.TrimSuffix(glob, "**"))
}

func TestPolicyStageRiskClassOverrides(t *testing.T) {
	compiled := stageBootstrapCompiled(t)
	overrides := policyStageOverridesCompiled(t)
	require.NotNil(t, overrides.Policy.RiskClassOverrides)

	// A low-risk, high-confidence decision resolves AUTO on a writable doc
	// without overrides (the matrix's single auto cell).
	decision := policyStageDecision(t, compiled, policyStageDecisionSpec{})
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/zone/deep.md", Mode: docModePtr(schemas.DocModeAuto), Sources: []string{"src/zone/**"}},
		},
	}

	autoResult, err := policy.CreatePolicyStagePort(policy.PolicyStageDeps{Workflow: *compiled}).Resolve(
		policy.PolicyStageResolveInput{
			Ctx:      policy.PolicyStageRunContext{Manifest: manifest},
			DocPath:  "docs/zone/deep.md",
			Decision: decision,
		})
	require.NoError(t, err)
	assert.Equal(t, schemas.PipelineOutcomeAuto, autoResult.Outcome)

	// The compiled policy's overrides flow through the INJECTED matcher (the
	// adapter's wire → typed conversion + doc-path wiring): a matched override
	// raises the risk bucket, and the high-risk row never autoes.
	overrideResult, err := policy.CreatePolicyStagePort(policy.PolicyStageDeps{
		Workflow:   *overrides,
		MatchGlobs: policyStageGlobMatcher,
	}).Resolve(policy.PolicyStageResolveInput{
		Ctx:      policy.PolicyStageRunContext{Manifest: manifest},
		DocPath:  "docs/zone/deep.md",
		Decision: decision,
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.PipelineOutcomeReview, overrideResult.Outcome)
	assert.True(t, overrideResult.ReviewRequired)

	// Overrides present WITHOUT a matcher fail loud: the engine's typed range
	// error propagates — override resolution is never silently skipped.
	_, err = policy.CreatePolicyStagePort(policy.PolicyStageDeps{Workflow: *overrides}).Resolve(
		policy.PolicyStageResolveInput{
			Ctx:      policy.PolicyStageRunContext{Manifest: manifest},
			DocPath:  "docs/zone/deep.md",
			Decision: decision,
		})
	require.Error(t, err)
	var rangeErr *policy.EngineRangeError
	require.ErrorAs(t, err, &rangeErr)
}

// policyStageRejectingQueue is a queue double that rejects the record — the
// adapter propagates a queue rejection (fail loud; the runner owns
// degradation).
type policyStageRejectingQueue struct{}

var _ policy.AbstainQueuePick = policyStageRejectingQueue{}

func (policyStageRejectingQueue) RecordAbstain(input policy.RecordAbstainInput) (policy.AbstainEvent, error) {
	return policy.AbstainEvent{}, errors.New("queue rejected the record (test double)")
}

func TestPolicyStageQueueRejectionPropagates(t *testing.T) {
	compiled := stageBootstrapCompiled(t)
	port := policy.CreatePolicyStagePort(policy.PolicyStageDeps{
		Workflow: *compiled,
		Queue:    policyStageRejectingQueue{},
	})
	abstained := policyStageDecision(t, compiled, policyStageDecisionSpec{
		confidence:        floatPtr(0.6),
		impact:            impactPtr(schemas.DocumentationImpactUnknown),
		impactProbability: floatPtr(0.6),
		abstain:           boolPtr(true),
		abstainReason:     abstainReasonPtr(schemas.AbstainReasonInsufficientConfidence),
	})

	_, err := port.Resolve(policy.PolicyStageResolveInput{
		Ctx:      policyStageCtx(policyStageManifest()),
		DocPath:  "docs/payments.md",
		Decision: abstained,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue rejected")
}

/* --------------------------------- suites: P5-T08 policy stage outcome mapping */

func TestPolicyStageOutcomeMapping(t *testing.T) {
	compiled := stageBootstrapCompiled(t)
	port := policy.CreatePolicyStagePort(policy.PolicyStageDeps{Workflow: *compiled})

	resolve := func(t *testing.T, docPath string, spec policyStageDecisionSpec) policy.PolicyStageResultWithQueue {
		t.Helper()
		result, err := port.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(policyStageManifest()),
			DocPath:      docPath,
			Decision:     policyStageDecision(t, compiled, spec),
			Verification: nil,
		})
		require.NoError(t, err)
		return result
	}

	t.Run("policy-stage-maps-outcome-four-cells-plus-review", func(t *testing.T) {
		// AUTO: high confidence (0.96 ≥ 0.94), low risk, manifest mode auto —
		// the matrix's single auto cell passes the mode-permission gate.
		auto := resolve(t, "docs/payments.md", policyStageDecisionSpec{})
		assert.Equal(t, schemas.PipelineOutcomeAuto, auto.Outcome)
		assert.False(t, auto.ReviewRequired)
		assert.Nil(t, auto.Abstain)

		// SUGGEST: the same engine cell under a non-auto writable mode — the
		// V-P2-8 groundwork mode-permission gate degrades auto to a suggestion.
		suggest := resolve(t, "docs/suggest.md", policyStageDecisionSpec{})
		assert.Equal(t, schemas.PipelineOutcomeSuggest, suggest.Outcome)
		assert.False(t, suggest.ReviewRequired)

		// BLOCKED: the D2 ownership floor — locked resolves blocked at ANY
		// confidence (the floor takes no confidence or threshold input).
		blocked := resolve(t, "docs/locked.md", policyStageDecisionSpec{})
		assert.Equal(t, schemas.PipelineOutcomeBlocked, blocked.Outcome)
		assert.False(t, blocked.ReviewRequired)

		// IGNORE: the deterministic short-circuit (formatting_only + impact
		// none) fires after every abstain check — a Phase-1 R10 semantic, kept
		// verbatim.
		ignore := resolve(t, "docs/payments.md", policyStageDecisionSpec{
			changeKind: changeKindPtr(schemas.ChangeKindFormattingOnly),
			impact:     impactPtr(schemas.DocumentationImpactNone),
		})
		assert.Equal(t, schemas.PipelineOutcomeIgnore, ignore.Outcome)
		assert.False(t, ignore.ReviewRequired)

		// REVIEW (matrix diagonal): deciding probability 0.7 ≤ review
		// threshold (0.72) → low confidence bucket → the low×low review cell —
		// review WITHOUT an abstain record (the cell, not the abstain path).
		review := resolve(t, "docs/payments.md", policyStageDecisionSpec{
			confidence:        floatPtr(0.7),
			impactProbability: floatPtr(0.7),
		})
		assert.Equal(t, schemas.PipelineOutcomeReview, review.Outcome)
		assert.True(t, review.ReviewRequired)
		assert.Nil(t, review.Abstain)

		// REVIEW + abstain: the Stage-1 decision-level abstain passes through
		// verbatim, named insufficient_confidence (never re-derived).
		abstained := resolve(t, "docs/payments.md", policyStageDecisionSpec{
			confidence:        floatPtr(0.6),
			impact:            impactPtr(schemas.DocumentationImpactUnknown),
			impactProbability: floatPtr(0.6),
			abstain:           boolPtr(true),
			abstainReason:     abstainReasonPtr(schemas.AbstainReasonInsufficientConfidence),
		})
		assert.Equal(t, schemas.PipelineOutcomeReview, abstained.Outcome)
		assert.True(t, abstained.ReviewRequired)
		require.NotNil(t, abstained.Abstain)
		assert.Equal(t, schemas.AbstainReasonInsufficientConfidence, abstained.Abstain.Reason)
		assert.Contains(t, abstained.Abstain.Detail, "stage-1 decision abstained")

		// The outcome labels are the closed five-value run-outcome vocabulary.
		for _, outcome := range []policy.PolicyStageResultWithQueue{auto, suggest, blocked, ignore, review, abstained} {
			assert.Contains(t, policy.PolicyOutcomeValues, policy.PolicyOutcomeLabel(outcome.Outcome))
		}
	})

	t.Run("abstain-lands-in-review-queue-with-evidence", func(t *testing.T) {
		queue := &policyStageRecordingQueue{}
		queuePort := policy.CreatePolicyStagePort(policy.PolicyStageDeps{
			Workflow: *compiled,
			Queue:    queue,
		})
		abstained := policyStageDecision(t, compiled, policyStageDecisionSpec{
			confidence:        floatPtr(0.6),
			impact:            impactPtr(schemas.DocumentationImpactUnknown),
			impactProbability: floatPtr(0.6),
			abstain:           boolPtr(true),
			abstainReason:     abstainReasonPtr(schemas.AbstainReasonInsufficientConfidence),
			decisionID:        stageStrPtr("d_abstain_fixture_1"),
		})

		result, err := queuePort.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(policyStageManifest()),
			DocPath:      "docs/payments.md",
			Decision:     abstained,
			Verification: nil,
		})
		require.NoError(t, err)

		// The abstained resolve carries the first-class abstain and the
		// adapter-level queue linkage (the run-level abstain record is where
		// the queue event id lands in the run outcome — core's fixed result
		// shape has no such member, so the adapter surfaces it as an extension
		// field).
		assert.Equal(t, schemas.PipelineOutcomeReview, result.Outcome)
		assert.True(t, result.ReviewRequired)
		require.NotNil(t, result.Abstain)
		assert.Equal(t, schemas.AbstainReasonInsufficientConfidence, result.Abstain.Reason)
		require.NotNil(t, result.QueueEventID)
		assert.Regexp(t, regexp.MustCompile(`^a_`), *result.QueueEventID)

		// EXACTLY one queue record, carrying policy_version, the decision id,
		// the reason, and a non-empty detail (the abstain detail as the note on
		// a machine-resolvable anchor — the doc path).
		inputs := queue.recordedInputs()
		require.Len(t, inputs, 1)
		record := inputs[0]
		require.NotNil(t, record.Decision)
		assert.Equal(t, 1, record.Decision.PolicyVersion)
		assert.Equal(t, "d_abstain_fixture_1", record.Decision.DecisionID)
		assert.Equal(t, schemas.AbstainReasonInsufficientConfidence, record.Reason)
		assert.True(t, record.Decision.Abstain)
		assert.NotEmpty(t, record.Evidence)
		assert.Equal(t, "docs/payments.md", record.Evidence[0].Ref)
		assert.NotEmpty(t, record.Evidence[0].Note)

		// Contrast: a NON-abstained resolve writes NOTHING to the queue.
		before := len(queue.recordedInputs())
		_, err = queuePort.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(policyStageManifest()),
			DocPath:      "docs/payments.md",
			Decision:     policyStageDecision(t, compiled, policyStageDecisionSpec{}),
			Verification: nil,
		})
		require.NoError(t, err)
		assert.Len(t, queue.recordedInputs(), before)

		// The two-tier bypass on the SAME guard: a decision-level abstain
		// carrying an OPERATIONAL reason never enqueues, even though the engine
		// abstains with the same reason (§5.2 — the queue stores Tier-1
		// decision-level reasons only).
		operational := policyStageDecision(t, compiled, policyStageDecisionSpec{
			confidence:        floatPtr(0.6),
			impact:            impactPtr(schemas.DocumentationImpactUnknown),
			impactProbability: floatPtr(0.6),
			abstain:           boolPtr(true),
			abstainReason:     abstainReasonPtr(schemas.AbstainReasonModelUnavailable),
		})
		bypassed, err := queuePort.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(policyStageManifest()),
			DocPath:      "docs/payments.md",
			Decision:     operational,
			Verification: nil,
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.PipelineOutcomeReview, bypassed.Outcome)
		require.NotNil(t, bypassed.Abstain)
		assert.Equal(t, schemas.AbstainReasonModelUnavailable, bypassed.Abstain.Reason)
		assert.Nil(t, bypassed.QueueEventID)
		assert.Len(t, queue.recordedInputs(), before)
	})

	t.Run("policy-stage-verification-null-passthrough", func(t *testing.T) {
		autoDecision := policyStageDecision(t, compiled, policyStageDecisionSpec{
			decisionID: stageStrPtr("d_auto_fixture_1"),
		})

		// verification nil → the flag's false equivalent reaches the engine per
		// PolicyOutcomeInput's real shape: the outcome is EXACTLY the engine's
		// own verdict over the derived pending decision — the adapter adds
		// nothing (no verifier_uncertain, no downgrade; the runner owns the
		// review-path downgrade for missing verification evidence).
		result, err := port.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(policyStageManifest()),
			DocPath:      "docs/payments.md",
			Decision:     autoDecision,
			Verification: nil,
		})
		require.NoError(t, err)

		direct, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: policy.PendingDecision{
				ChangeKind:          autoDecision.ChangeKind,
				DocumentationImpact: autoDecision.DocumentationImpact,
				Risk:                autoDecision.Risk,
				Confidence:          autoDecision.Confidence,
				Answers:             policyStagePendingAnswers(autoDecision),
				Abstain:             autoDecision.Abstain,
				AbstainReason:       autoDecision.AbstainReason,
			},
			Compiled: compiled,
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		assert.Equal(t, direct.Outcome, policy.PolicyOutcomeLabel(result.Outcome))
		assert.Equal(t, direct.ReviewRequired, result.ReviewRequired)
		assert.Equal(t, schemas.PipelineOutcomeAuto, result.Outcome)

		// Contrast: the same decision with a REVIEW verdict — the §5.2
		// verifier_uncertain seam fires and the outcome abstains to review.
		uncertain, err := port.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(policyStageManifest()),
			DocPath:      "docs/payments.md",
			Decision:     autoDecision,
			Verification: policyStageVerdictReport(t, schemas.VerdictReview),
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.PipelineOutcomeReview, uncertain.Outcome)
		assert.True(t, uncertain.ReviewRequired)
		require.NotNil(t, uncertain.Abstain)
		assert.Equal(t, schemas.AbstainReasonVerifierUncertain, uncertain.Abstain.Reason)
	})

	t.Run("null-decision-fails-closed-to-review-abstain-without-queue-write", func(t *testing.T) {
		queue := &policyStageRecordingQueue{}
		queuePort := policy.CreatePolicyStagePort(policy.PolicyStageDeps{
			Workflow: *compiled,
			Queue:    queue,
		})

		result, err := queuePort.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(schemas.Manifest{Version: 1}),
			DocPath:      "docs/payments.md",
			Decision:     nil,
			Verification: nil,
		})
		require.NoError(t, err)

		// No pending decision → the engine cannot run: fail closed to a
		// first-class abstain (never forced into update/ignore), operational
		// reason, factual detail.
		assert.Equal(t, schemas.PipelineOutcomeReview, result.Outcome)
		assert.True(t, result.ReviewRequired)
		require.NotNil(t, result.Abstain)
		assert.Equal(t, schemas.AbstainReasonOther, result.Abstain.Reason)
		assert.Contains(t, result.Abstain.Detail, "no Stage-1 decision")
		// Operational reasons bypass the review queue (§5.2 two-tier
		// discipline).
		assert.Empty(t, queue.recordedInputs())
		assert.Nil(t, result.QueueEventID)
	})

	t.Run("unlisted-documents-resolve-external-blocked-at-any-confidence", func(t *testing.T) {
		// D7: the manifest is an allowlist — an unlisted document is never
		// writable; the D2 floor blocks it at any confidence.
		manifest := schemas.Manifest{
			Version: 1,
			Documents: []schemas.ManifestEntry{
				{Path: "docs/other.md", Mode: docModePtr(schemas.DocModeAuto), Sources: []string{"src/other/**"}},
			},
		}
		result, err := port.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(manifest),
			DocPath:      "docs/missing.md",
			Decision:     policyStageDecision(t, compiled, policyStageDecisionSpec{}),
			Verification: nil,
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.PipelineOutcomeBlocked, result.Outcome)
		assert.False(t, result.ReviewRequired)
		assert.Nil(t, result.QueueEventID)
	})

	t.Run("doc-mode-falls-back-to-the-manifest-defaults", func(t *testing.T) {
		// §11.3 precedence: an entry without a mode resolves through the
		// manifest defaults.
		manifest := schemas.Manifest{
			Version:  1,
			Defaults: &schemas.ManifestDefaults{Mode: docModePtr(schemas.DocModeLocked)},
			Documents: []schemas.ManifestEntry{
				{Path: "docs/guides.md", Sources: []string{"src/guides/**"}},
			},
		}
		result, err := port.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(manifest),
			DocPath:      "docs/guides.md",
			Decision:     policyStageDecision(t, compiled, policyStageDecisionSpec{}),
			Verification: nil,
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.PipelineOutcomeBlocked, result.Outcome)

		// An entry without a mode AND without manifest defaults resolves the
		// built-in suggest mode (never a writable auto).
		bare := schemas.Manifest{
			Version: 1,
			Documents: []schemas.ManifestEntry{
				{Path: "docs/guides.md", Sources: []string{"src/guides/**"}},
			},
		}
		suggest, err := port.Resolve(policy.PolicyStageResolveInput{
			Ctx:          policyStageCtx(bare),
			DocPath:      "docs/guides.md",
			Decision:     policyStageDecision(t, compiled, policyStageDecisionSpec{}),
			Verification: nil,
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.PipelineOutcomeSuggest, suggest.Outcome)
	})
}

/* ------------------------- structural-compatibility pins (probe pattern) */

// corePolicyStageAbstainSketch is core's `PolicyStageResult['abstain']`
// duplicated as a structural literal. The TS sketch pins `reason: string`
// (the TS AbstainReason union is a string); Go's canonical rendering of that
// vocabulary is schemas.AbstainReason (OD-2), so the pin targets the
// canonical type.
type corePolicyStageAbstainSketch struct {
	Reason schemas.AbstainReason
	Detail string
}

// corePolicyStageResultSketch is core's `PolicyStageResult`
// (core/src/orchestrator/types.ts) duplicated as a structural literal —
// @docsync/policy must NOT import @docsync/core (OD-1). A drift in the core
// shape fails the reflect pins below at test time.
type corePolicyStageResultSketch struct {
	Outcome        schemas.PipelineOutcome
	ReviewRequired bool
	Abstain        *corePolicyStageAbstainSketch
}

// corePolicyRunContextSketch is a minimal sketch of core's RunContext (the
// members the seam names).
type corePolicyRunContextSketch struct {
	Manifest schemas.Manifest
}

// corePolicyStageResolveInputSketch is core's `PolicyStagePort` input with the
// ctx subset above.
type corePolicyStageResolveInputSketch struct {
	Ctx          corePolicyRunContextSketch
	DocPath      string
	Decision     *schemas.Decision
	Verification *schemas.VerificationReport
}

// corePolicyStagePortSketch is core's `PolicyStagePort`, result side pinned;
// ctx side sketched. (TS input-side contravariance — the core input is
// assignable to the policy input — has no Go interface equivalent, so the
// input side is pinned by the conversion expression in the adapter below.)
type corePolicyStagePortSketch interface {
	Resolve(input corePolicyStageResolveInputSketch) (corePolicyStageResultSketch, error)
}

// stageToCorePolicySketch renders a policy-stage result onto the core sketch
// (the adapter mapping the TS forward probe checks structurally).
func stageToCorePolicySketch(result policy.PolicyStageResultWithQueue) corePolicyStageResultSketch {
	sketch := corePolicyStageResultSketch{
		Outcome:        result.Outcome,
		ReviewRequired: result.ReviewRequired,
	}
	if result.Abstain != nil {
		sketch.Abstain = &corePolicyStageAbstainSketch{
			Reason: result.Abstain.Reason,
			Detail: result.Abstain.Detail,
		}
	}
	return sketch
}

// stageCorePolicyAdapter pins that the policy port satisfies the core sketch:
// the core input converts to the policy input (the TS contravariance pin) and
// the result converts onto the core sketch (the TS forward probe).
type stageCorePolicyAdapter struct {
	port policy.PolicyStagePort
}

var _ corePolicyStagePortSketch = stageCorePolicyAdapter{}

func (a stageCorePolicyAdapter) Resolve(input corePolicyStageResolveInputSketch) (corePolicyStageResultSketch, error) {
	result, err := a.port.Resolve(policy.PolicyStageResolveInput{
		Ctx:          policy.PolicyStageRunContext{Manifest: input.Ctx.Manifest},
		DocPath:      input.DocPath,
		Decision:     input.Decision,
		Verification: input.Verification,
	})
	if err != nil {
		return corePolicyStageResultSketch{}, err
	}
	return stageToCorePolicySketch(result), nil
}

func TestPolicyStageStructuralCompatibilityWithCorePort(t *testing.T) {
	compiled := stageBootstrapCompiled(t)

	t.Run("policy-stage-result-is-assignable-to-the-core-result-shape", func(t *testing.T) {
		// Forward pin: every core result member exists on the Jev shape with a
		// compatible type (PolicyStageResultWithQueue extends
		// CorePolicyStageResultSketch).
		stagePinnedFields(t, reflect.TypeOf(policy.PolicyStageResultWithQueue{}), reflect.TypeOf(corePolicyStageResultSketch{}))
	})

	t.Run("policy-stage-port-satisfies-the-core-port-sketch", func(t *testing.T) {
		compiledValue := *compiled
		adapter := stageCorePolicyAdapter{port: policy.CreatePolicyStagePort(policy.PolicyStageDeps{Workflow: compiledValue})}
		result, err := adapter.Resolve(corePolicyStageResolveInputSketch{
			Ctx:      corePolicyRunContextSketch{Manifest: policyStageManifest()},
			DocPath:  "docs/payments.md",
			Decision: policyStageDecision(t, compiled, policyStageDecisionSpec{}),
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.PipelineOutcomeAuto, result.Outcome)
		assert.False(t, result.ReviewRequired)
		assert.Nil(t, result.Abstain)
	})
}

/* ------------------------------------------------------------------ helpers */

// policyStagePendingAnswers renders the decision's answer block onto the
// engine's PendingDecisionAnswer seam (the same field-for-field conversion the
// adapter performs, pinned here for the direct-engine comparison).
func policyStagePendingAnswers(decision *schemas.Decision) map[string]policy.PendingDecisionAnswer {
	answers := make(map[string]policy.PendingDecisionAnswer, len(decision.Answers))
	for key, answer := range decision.Answers {
		answers[key] = policy.PendingDecisionAnswer{
			Value:       []byte(answer.Value),
			Probability: answer.Probability,
		}
	}
	return answers
}

func floatPtr(value float64) *float64 { return &value }
func boolPtr(value bool) *bool        { return &value }
func docModePtr(value schemas.DocMode) *schemas.DocMode {
	return &value
}
func changeKindPtr(value schemas.ChangeKind) *schemas.ChangeKind {
	return &value
}
func impactPtr(value schemas.DocumentationImpact) *schemas.DocumentationImpact {
	return &value
}
func abstainReasonPtr(value schemas.AbstainReason) *schemas.AbstainReason {
	return &value
}
