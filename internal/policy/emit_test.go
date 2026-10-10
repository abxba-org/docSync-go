// Ported from packages/policy/test/decision-emit.test.ts — decision-object
// emission (P2-T13; V-P2-6, V-P2-7, V-P3-10):
//   - round-trips DECISION_SCHEMA and carries the §5.4 truthfulness invariants
//     (policy_version, trace.policy_hash, trace.latency_ms, decision_id,
//     value+probability on every answer);
//   - manifest gate (CANDIDATE_NOT_IN_MANIFEST) rejects candidates outside
//     the manifest allowlist and never silently drops;
//   - evidence gate: machine-resolvable anchors only — per-kind accept/reject
//     tables, single-error aggregation over every offender, the
//     canonical-gate delegation checks and the BYTE-IDENTICAL wrapper message
//     pinned against the internal/schemas canonical error;
//   - fail-closed DECISION_SCHEMA drift on latency/confidence/abstain-pairing.
package policy_test

import (
	"math"

	"encoding/json"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"sort"
	"testing"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func emitCompiled() *policy.CompiledPolicyWorkflow {
	return abstainCompiled()
}

var emitManifestPaths = abstainManifestPaths

var emitChange = abstainChange

func emitAnswers() map[string]schemas.Answer {
	return map[string]schemas.Answer{
		"public_behavior_changed": {Value: json.RawMessage(`true`), Probability: 0.87},
		"api_contract_changed":    {Value: json.RawMessage(`false`), Probability: 0.12},
		"affected_doc_count":      {Value: json.RawMessage(`2`), Probability: 0.91},
		"documentation_impact":    {Value: json.RawMessage(`"possible"`), Probability: 0.91},
	}
}

func emitBody() policy.EmitDecisionBody {
	return policy.EmitDecisionBody{
		ChangeKind:          schemas.ChangeKindBehavioral,
		DocumentationImpact: schemas.DocumentationImpactPossible,
		Risk:                schemas.RiskLevelMedium,
		Confidence:          0.91,
		Action:              schemas.ActionReview,
		Abstain:             false,
		AbstainReason:       nil,
	}
}

func emitEvidence() []schemas.Evidence {
	return []schemas.Evidence{
		{Kind: schemas.EvidenceKindSymbolDiff, Ref: "src/payments/refund.ts:42", Note: "idempotencyKey became optional"},
	}
}

func emitBaseInput() policy.EmitDecisionInput {
	return policy.EmitDecisionInput{
		Compiled:         emitCompiled(),
		Change:           emitChange,
		Answers:          emitAnswers(),
		Body:             emitBody(),
		Candidates:       []string{"docs/payments.md"},
		ManifestDocPaths: emitManifestPaths,
		Evidence:         emitEvidence(),
		RoutedModel:      "provider/routed",
		LatencyMs:        740,
	}
}

// emitWith ports the TS suite's `emit(overrides)` — base input with a mutation
// applied, requiring a successful emission.
func emitWith(t *testing.T, mutate func(*policy.EmitDecisionInput)) *schemas.Decision {
	t.Helper()
	input := emitBaseInput()
	if mutate != nil {
		mutate(&input)
	}
	decision, err := policy.EmitDecision(input)
	require.NoError(t, err)
	return decision
}

func TestEmitDecision_VP26_RoundTripsTheWireSchemaAndCarriesProvenanceFields(t *testing.T) {
	compiled := emitCompiled()
	decision := emitWith(t, nil)

	require.Equal(t, schemas.DecisionSchemaID, decision.Schema)
	require.Empty(t, decision.Validate())
	raw, err := json.Marshal(decision)
	require.NoError(t, err)
	parsed, err := schemas.ParseDecision(raw)
	require.NoError(t, err)
	require.Equal(t, decision, parsed)

	// §5.4 / V-P2-6 truthfulness invariants.
	require.Equal(t, compiled.Policy.PolicyVersion, decision.PolicyVersion)
	require.Equal(t, compiled.PolicyHash, decision.Trace.PolicyHash)
	require.Equal(t, 740, decision.Trace.LatencyMS)
	require.Equal(t, "provider/routed", decision.Trace.Model)
	require.Equal(t, emitChange, decision.Change)
}

func TestEmitDecision_EveryAnswerCarriesValueAndProbabilityKeyedByTheQuestionSet(t *testing.T) {
	decision := emitWith(t, nil)
	keys := make([]string, 0, len(decision.Answers))
	for key := range decision.Answers {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// The composed question set is the canonical universal set (bootstrap v1
	// compiles exactly UNIVERSAL_QUESTIONS, in canonical order).
	composed := append([]string{}, schemas.UniversalQuestions...)
	sort.Strings(composed)
	require.Equal(t, composed, keys)

	for key, answer := range decision.Answers {
		require.NotEmpty(t, key)
		require.NotEmpty(t, answer.Value, "answer %q must carry a value", key)
		require.GreaterOrEqual(t, answer.Probability, 0.0)
		require.LessOrEqual(t, answer.Probability, 1.0)
	}
}

func TestEmitDecision_DefaultDecisionIDIsDeterministicAndStable(t *testing.T) {
	compiled := emitCompiled()
	first := emitWith(t, nil)
	second := emitWith(t, nil)
	require.Equal(t, first.DecisionID, second.DecisionID)
	require.Equal(t, policy.ResolveDecisionID(compiled, emitChange), first.DecisionID)
	require.Regexp(t, `^d_[0-9a-f]{16}$`, first.DecisionID)
}

func TestEmitDecision_DecisionIDVariesWithChangeRefsAndHonorsAnExplicitOverride(t *testing.T) {
	other := emitWith(t, func(input *policy.EmitDecisionInput) {
		input.Change = schemas.DecisionChange{Commit: "82bg93f", DiffRef: "git:81af92e", ChangeAnalysisRef: "analysis/01J9ZK6X8A"}
	})
	require.NotEqual(t, emitWith(t, nil).DecisionID, other.DecisionID)

	overridden := emitWith(t, func(input *policy.EmitDecisionInput) {
		id := "d_explicit_override_id"
		input.DecisionID = &id
	})
	require.Equal(t, "d_explicit_override_id", overridden.DecisionID)
}

func TestEmitDecision_VP27_RejectsCandidatesNotInTheManifest(t *testing.T) {
	input := emitBaseInput()
	input.Candidates = []string{"docs/payments.md", "docs/not-in-manifest.md"}
	_, err := policy.EmitDecision(input)
	var candidateErr *policy.CandidateNotInManifestError
	require.ErrorAs(t, err, &candidateErr)
	require.Equal(t, []string{"docs/not-in-manifest.md"}, candidateErr.Offending)
	require.Equal(t, "CANDIDATE_NOT_IN_MANIFEST", candidateErr.Code())
	require.Contains(t, candidateErr.Error(), "manifest-known paths only")
}

func TestEmitDecision_ManifestOKCandidateSetEmitsCleanly(t *testing.T) {
	decision := emitWith(t, func(input *policy.EmitDecisionInput) {
		input.Candidates = []string{"docs/payments.md", "docs/api.md"}
	})
	require.Equal(t, []string{"docs/payments.md", "docs/api.md"}, decision.Candidates)
}

func TestEmitDecision_ManifestGateCollectsEveryOffenderInInputOrder(t *testing.T) {
	err := policy.AssertCandidatesInManifest(
		[]string{"docs/a.md", "docs/payments.md", "docs/b.md", "docs/c.md"},
		emitManifestPaths,
	)
	var candidateErr *policy.CandidateNotInManifestError
	require.ErrorAs(t, err, &candidateErr)
	require.Equal(t, []string{"docs/a.md", "docs/b.md", "docs/c.md"}, candidateErr.Offending)
}

func TestAssertEvidenceResolvable_AcceptsValidResolvableRefs(t *testing.T) {
	for _, testCase := range []struct{ kind, ref string }{
		{"symbol_diff", "src/payments/refund.ts:42"},
		{"symbol_diff", "src/payments/refund.ts:refundOrder"},
		{"api_diff", "src/payments/refund.ts"},
		{"config_diff", "config/app.yml:server.port"},
		{"dependency_diff", "lodash"},
		{"dependency_diff", "@scope/pkg-name"},
		{"doc_anchor", "docs/payments.md:12"},
		{"other", "arbitrary free text anchor"},
	} {
		t.Run(testCase.kind+" | "+testCase.ref, func(t *testing.T) {
			require.NoError(t, policy.AssertEvidenceResolvable([]schemas.Evidence{
				{Kind: schemas.EvidenceKind(testCase.kind), Ref: testCase.ref, Note: ""},
			}))
		})
	}
}

func TestAssertEvidenceResolvable_RejectsUnresolvableRefs(t *testing.T) {
	for _, testCase := range []struct{ kind, ref, reason string }{
		{"symbol_diff", "idempotencyKey became optional", "symbol_diff ref must be 'path:line' or 'path:symbolName'"},
		{"symbol_diff", "src/a.ts:0", "neither a 1-based line number nor a symbol name"},
		{"symbol_diff", "/abs/path.ts:42", "not a repo-relative path"},
		{"doc_anchor", "docs/payments.md:sym", "not a 1-based line number"},
		{"doc_anchor", "docs/payments.md", "doc_anchor ref must be 'path:line'"},
		{"api_diff", "src/a.ts:42", "without an anchor"},
		{"api_diff", "../escape.ts", "must be a repo-relative path"},
		{"config_diff", "config/app.yml", "config_diff ref must be 'file:key'"},
		{"dependency_diff", "Lodash Thing", "must be an npm package name"},
		{"other", "", "must be non-empty"},
		{"diary", "x", "unknown evidence kind"},
	} {
		t.Run(testCase.kind+" | "+testCase.ref, func(t *testing.T) {
			err := policy.AssertEvidenceResolvable([]schemas.Evidence{
				{Kind: schemas.EvidenceKind(testCase.kind), Ref: testCase.ref, Note: ""},
			})
			var evidenceErr *policy.EvidenceNotResolvableError
			require.ErrorAs(t, err, &evidenceErr)
			require.Len(t, evidenceErr.Offending, 1)
			require.Contains(t, evidenceErr.Offending[0].Reason, testCase.reason)
		})
	}
}

func TestAssertEvidenceResolvable_NotesAreFreeText(t *testing.T) {
	require.NoError(t, policy.AssertEvidenceResolvable([]schemas.Evidence{
		{Kind: schemas.EvidenceKindSymbolDiff, Ref: "src/a.ts:42", Note: "any free prose explanation is allowed"},
	}))
}

func TestAssertEvidenceResolvable_CollectsEveryUnresolvableItemInOneError(t *testing.T) {
	err := policy.AssertEvidenceResolvable([]schemas.Evidence{
		{Kind: schemas.EvidenceKindSymbolDiff, Ref: "no anchor here", Note: ""},
		{Kind: schemas.EvidenceKindAPIDiff, Ref: "src/a.ts", Note: ""},
		{Kind: schemas.EvidenceKindDocAnchor, Ref: "docs/x.md:sym", Note: ""},
	})
	var evidenceErr *policy.EvidenceNotResolvableError
	require.ErrorAs(t, err, &evidenceErr)
	require.Len(t, evidenceErr.Offending, 2)
	require.Equal(t, 0, evidenceErr.Offending[0].Index)
	require.Equal(t, 2, evidenceErr.Offending[1].Index)
}

func TestEmitDecision_VP310_UnresolvableEvidenceRejectsEmission(t *testing.T) {
	input := emitBaseInput()
	input.Evidence = []schemas.Evidence{{Kind: schemas.EvidenceKindSymbolDiff, Ref: "free text no anchor", Note: ""}}
	_, err := policy.EmitDecision(input)
	var evidenceErr *policy.EvidenceNotResolvableError
	require.ErrorAs(t, err, &evidenceErr)
}

func TestAssertEvidenceResolvable_VP310_DelegatesToTheCanonicalSchemasGate(t *testing.T) {
	// OD-2 single source: the policy-level verdict must agree with the
	// canonical schemas gate on both sides.
	samples := []schemas.Evidence{
		{Kind: schemas.EvidenceKindSymbolDiff, Ref: "src/payments/refund.ts:3", Note: ""},
		{Kind: schemas.EvidenceKindAPIDiff, Ref: "src/payments/refund.ts", Note: ""},
		{Kind: schemas.EvidenceKindConfigDiff, Ref: ".docsync/manifest.yml:version", Note: ""},
		{Kind: schemas.EvidenceKindDependencyDiff, Ref: "pnpm", Note: ""},
		{Kind: schemas.EvidenceKindDocAnchor, Ref: "docs/payments.md:5", Note: ""},
		{Kind: schemas.EvidenceKindOther, Ref: "free-text anchor for other kind", Note: ""},
		{Kind: schemas.EvidenceKindSymbolDiff, Ref: "free text no anchor", Note: ""},
		{Kind: schemas.EvidenceKindDocAnchor, Ref: "docs/x.md", Note: ""},
	}
	for _, sample := range samples {
		err := policy.AssertEvidenceResolvable([]schemas.Evidence{sample})
		require.Equal(t, schemas.IsResolvableEvidence(string(sample.Kind), sample.Ref), err == nil,
			"policy/schemas gate disagreement for kind %q ref %q", sample.Kind, sample.Ref)
	}

	// Emission-level negative stays pinned to the canonical reason text.
	err := policy.AssertEvidenceResolvable([]schemas.Evidence{samples[6]})
	var evidenceErr *policy.EvidenceNotResolvableError
	require.ErrorAs(t, err, &evidenceErr)
	require.Regexp(t, `symbol_diff ref must be 'path:line' or 'path:symbolName'`, evidenceErr.Offending[0].Reason)
}

func TestAssertEvidenceResolvable_WrapperMessageIsByteIdenticalToTheCanonicalError(t *testing.T) {
	evidence := []schemas.Evidence{
		{Kind: schemas.EvidenceKindSymbolDiff, Ref: "free text no anchor"},
		{Kind: schemas.EvidenceKindAPIDiff, Ref: "src/a.ts:42"},
		{Kind: schemas.EvidenceKindDocAnchor, Ref: "docs/x.md"},
	}
	err := policy.AssertEvidenceResolvable(evidence)
	var policyErr *policy.EvidenceNotResolvableError
	require.ErrorAs(t, err, &policyErr)
	require.Equal(t, schemas.EvidenceNotResolvableErrorCode, policyErr.Code())

	const expectedMessage = "decision emission: evidence refs must be machine-resolvable anchors (ARCH §5.4; V-P3-10) — " +
		"[0] symbol_diff 'free text no anchor': symbol_diff ref must be 'path:line' or 'path:symbolName' (got 'free text no anchor'); " +
		"[1] api_diff 'src/a.ts:42': api_diff ref must be a repo-relative path without an anchor (got 'src/a.ts:42'); " +
		"[2] doc_anchor 'docs/x.md': doc_anchor ref must be 'path:line' (got 'docs/x.md')"
	require.Equal(t, expectedMessage, policyErr.Error())

	// The canonical @docsync/schemas error over the SAME offenders at the SAME
	// site renders the identical bytes (message parity is pinned by the TS
	// suite).
	canonical := &schemas.EvidenceNotResolvableError{
		Where: "decision emission",
		Offending: []schemas.UnresolvableEvidenceItem{
			{
				Index: 0, Kind: string(schemas.EvidenceKindSymbolDiff), Ref: "free text no anchor",
				Reason: "symbol_diff ref must be 'path:line' or 'path:symbolName' (got 'free text no anchor')",
			},
			{
				Index: 1, Kind: string(schemas.EvidenceKindAPIDiff), Ref: "src/a.ts:42",
				Reason: "api_diff ref must be a repo-relative path without an anchor (got 'src/a.ts:42')",
			},
			{
				Index: 2, Kind: string(schemas.EvidenceKindDocAnchor), Ref: "docs/x.md",
				Reason: "doc_anchor ref must be 'path:line' (got 'docs/x.md')",
			},
		},
	}
	require.Equal(t, canonical.Error(), policyErr.Error())
}

func TestAssertEvidenceResolvable_UnknownKindKeepsTheUnknownDisplayLabelWithTheCanonicalReason(t *testing.T) {
	err := policy.AssertEvidenceResolvable([]schemas.Evidence{
		{Kind: schemas.EvidenceKind("diary"), Ref: "x", Note: ""},
	})
	var evidenceErr *policy.EvidenceNotResolvableError
	require.ErrorAs(t, err, &evidenceErr)
	require.Len(t, evidenceErr.Offending, 1)
	require.Equal(t, "(unknown)", evidenceErr.Offending[0].Kind)
	// The per-item reason is the CANONICAL source's, verbatim (byte-equal).
	require.Equal(t, schemas.EvidenceRefIssue("diary", "x"), evidenceErr.Offending[0].Reason)
}

func TestEmitDecision_NonIntegerLatencyIsSchemaDrift(t *testing.T) {
	input := emitBaseInput()
	input.LatencyMs = 1.5
	_, err := policy.EmitDecision(input)
	var driftErr *policy.DecisionSchemaDriftError
	require.ErrorAs(t, err, &driftErr)
}

func TestEmitDecision_ConfidenceOutsideUnitIntervalIsSchemaDrift(t *testing.T) {
	input := emitBaseInput()
	input.Body.Confidence = 1.5
	_, err := policy.EmitDecision(input)
	var driftErr *policy.DecisionSchemaDriftError
	require.ErrorAs(t, err, &driftErr)
}

func TestEmitDecision_AbstainTrueWithNullReasonIsSchemaDrift(t *testing.T) {
	input := emitBaseInput()
	input.Body.Abstain = true
	input.Body.AbstainReason = nil
	_, err := policy.EmitDecision(input)
	var driftErr *policy.DecisionSchemaDriftError
	require.ErrorAs(t, err, &driftErr)
}

func TestEmitDecision_AbstainedEmissionCarriesTheDecisionLevelReason(t *testing.T) {
	reason := schemas.AbstainReasonInsufficientConfidence
	decision := emitWith(t, func(input *policy.EmitDecisionInput) {
		input.Body.Abstain = true
		input.Body.AbstainReason = &reason
	})
	require.True(t, decision.Abstain)
	require.Equal(t, reason, *decision.AbstainReason)

	raw, err := json.Marshal(decision)
	require.NoError(t, err)
	parsed, err := schemas.ParseDecision(raw)
	require.NoError(t, err)
	require.Equal(t, decision, parsed)
}

func TestEmitDecision_NonFiniteLatencyIsSchemaDrift(t *testing.T) {
	input := emitBaseInput()
	input.LatencyMs = math.NaN()
	_, err := policy.EmitDecision(input)
	var driftErr *policy.DecisionSchemaDriftError
	require.ErrorAs(t, err, &driftErr)
}

func TestEmitErrors_CodedErrorContract(t *testing.T) {
	t.Run("CandidateNotInManifestError", func(t *testing.T) {
		input := emitBaseInput()
		input.Candidates = []string{"docs/nope.md"}
		_, err := policy.EmitDecision(input)
		var candidateErr *policy.CandidateNotInManifestError
		require.ErrorAs(t, err, &candidateErr)
		require.Equal(t, "CANDIDATE_NOT_IN_MANIFEST", candidateErr.Code())
		require.Contains(t, candidateErr.Error(), "Emission rejects; filter upstream, never here")
		require.Equal(t, docutil.CodeManifest, docutil.CodeOf(candidateErr))
	})

	t.Run("EvidenceNotResolvableError", func(t *testing.T) {
		input := emitBaseInput()
		input.Evidence = []schemas.Evidence{{Kind: schemas.EvidenceKindDocAnchor, Ref: "docs/x.md"}}
		_, err := policy.EmitDecision(input)
		var evidenceErr *policy.EvidenceNotResolvableError
		require.ErrorAs(t, err, &evidenceErr)
		require.Equal(t, "EVIDENCE_NOT_RESOLVABLE", evidenceErr.Code())
		require.Contains(t, evidenceErr.Error(), "V-P3-10")
		require.Equal(t, docutil.CodeSchema, docutil.CodeOf(evidenceErr))
	})

	t.Run("DecisionSchemaDriftError", func(t *testing.T) {
		input := emitBaseInput()
		input.Body.Confidence = -0.1
		_, err := policy.EmitDecision(input)
		var driftErr *policy.DecisionSchemaDriftError
		require.ErrorAs(t, err, &driftErr)
		require.Equal(t, "SCHEMA_DRIFT", driftErr.Code())
		require.NotEmpty(t, driftErr.Issues)
		require.Contains(t, driftErr.Error(), "fail closed on drift")
		require.Equal(t, docutil.CodeSchema, docutil.CodeOf(driftErr))
	})
}

func TestEvidenceNotResolvableError_DefaultsTheWhereSite(t *testing.T) {
	// An error built without an explicit Where renders the documented
	// 'decision emission' site (TS class default).
	err := &policy.EvidenceNotResolvableError{
		Offending: []policy.UnresolvableEvidence{
			{Index: 0, Kind: "symbol_diff", Ref: "x", Reason: "symbol_diff ref must be 'path:line' or 'path:symbolName'"},
		},
	}
	require.Contains(t, err.Error(), "decision emission: evidence refs must be machine-resolvable anchors")
	require.Equal(t, "EVIDENCE_NOT_RESOLVABLE", err.Code())
}
