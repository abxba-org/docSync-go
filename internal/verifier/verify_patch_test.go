// Ported from packages/verifier/test/pipeline.test.ts (V-P4-1 full) and
// packages/verifier/test/pipeline-claim-refs.test.ts (V-P11-T15, OD-R26).
//
// The fixtures are the Go stand-ins for the TS payments pipeline repo
// (`@docsync/core/testing`): the real doc/source CONTENT minus the git
// materialization (RunTier1's apply simulation works from the injected
// docContent alone when no git port is supplied), the intake'd hand-anchored
// fixture claim, and the REAL RunTier1 / RunTier2 / RunTier3 tiers — the judge
// is the only double, exactly as in the TS suite (network-free, OD-2).
//
// Identifier namespacing: this file shares the external `verifier_test`
// package with the concurrent delegates' suites, so every top-level helper
// here carries the `vp` (verify-patch) prefix — no name may collide with a
// peer delegate's declarations (CONVENTIONS.md: disjoint file ownership
// extends to the shared test package's symbol space).
//
// Replay/recording double discipline (AGENT-behavior.md): the judge double is
// keyed by REQUEST IDENTITY (the triggering-diff marker the pipeline passes
// through as the judge invocation's untrusted content) and dispatches per
// call — never positional FIFO. An invocation without the identity marker, or
// a second unexpected identity, fails loud.
package verifier_test

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------- fixture pins */

const (
	vpRunID        = "acme/payments#verify-p4"
	vpPolicyVer    = 17
	vpWriterModel  = "anthropic/claude-sonnet-4.5"
	vpGeneratedAt  = "2026-10-06T12:00:00.000Z"
	vpRoutedModel  = "openai/gpt-4.1"
	vpClockValue   = "2026-10-06T12:30:00.000Z"
	vpDocPath      = "docs/payments.md"
	vpTypesSource  = "src/payments/types.ts"
	vpClaimID      = "claim_idempotency"
	vpUnresolvable = "claim_does_not_exist_anywhere"
)

// vpClock is the fixed injected clock — Tier provenance is caller-owned (no
// hidden Date).
func vpClock() string { return vpClockValue }

// vpFixtureDoc is the Go stand-in for the real docs/payments.md content.
const vpFixtureDoc = `# Payments

Refunds require an idempotency key for amounts above 10 USD.

The capture amount defaults to the order total.

Retry window: 4 hours after a declined charge.
`

// vpTypesSourceContent is the Go stand-in for the real src/payments/types.ts
// content (the workspace source claims bind against); `amountMinor` sits on
// line 3 — the head-verification evidence window.
const vpTypesSourceContent = `export interface CaptureRequest {
  orderId: string;
  amountMinor: number;
  currency: string;
}
`

const vpIdempotencyLine = "Refunds require an idempotency key for amounts above 10 USD."

// vpTriggeredLine is the capture-amount doc line the patch edits.
const vpTriggeredLine = "The capture amount defaults to the order total."

// vpCaptureReplacement is the patched capture-amount line (the claim text).
const vpCaptureReplacement = "The capture amount defaults to `amountMinor`."

// vpTriggerMarker is the request-identity marker the recorded judge double
// keys on (it rides the triggering diff as the judge invocation's UNTRUSTED
// content).
const vpTriggerMarker = "trigger: capture-amount doc-line drift (request identity: payments-doc-v1)"

// The tier check-id expectations (ARCH §4.8 canonical order).
var (
	vpTier1CheckIDs = []string{
		"patch-applies-cleanly",
		"manifest-allowlist-only",
		"ownership-floor",
		"minimality-bound",
		"whole-document-rewrite",
		"structural-integrity",
		"link-resolution",
		"examples-parse",
	}
	vpTier2CheckIDs = []string{
		"claim-decomposition",
		"claim-anchor-binding",
		"head-verification",
		"over-deletion-guard",
		"contract-aware-check",
		"coverage-defect",
		"behavioral-predicate-unverified",
	}
	vpTier3CheckIDs = []string{"judge-independence", "judge-second-pass"}
)

/* --------------------------------------------------------------- test doubles */

// vpJudgeCall is one recorded judge invocation.
type vpJudgeCall struct {
	Identity string
	System   string
	User     string
}

// vpRecordedJudge is the JudgeRole replay double: keyed by request identity,
// dispatch-per-call (AGENT-behavior.md replay rule — never positional FIFO).
// One scripted verdict is carried; the first invocation binds its identity and
// every later invocation must present the SAME identity or the double fails
// loud. Every invocation is recorded so tests can assert the judge was or was
// not reached.
type vpRecordedJudge struct {
	marker string
	result openrouter.JudgeResult

	mu       sync.Mutex
	identity string
	calls    []vpJudgeCall
}

func vpNewRecordedJudge() *vpRecordedJudge {
	return &vpRecordedJudge{
		marker: vpTriggerMarker,
		result: openrouter.JudgeResult{
			Kind:    openrouter.JudgeKindVerdict,
			Verdict: openrouter.JudgeVerdictPass,
			Reasons: []string{"all tiers green; the drift is resolved by the patch"},
			Questions: map[string]openrouter.JudgeQuestionAnswer{
				"stale_before":              {Value: "true", Probability: 0.97},
				"resolved_after":            {Value: "true", Probability: 0.94},
				"minimal":                   {Value: "true", Probability: 0.98},
				"introduced_new_inaccuracy": {Value: "false", Probability: 0.96},
			},
			Telemetry: openrouter.RoleTelemetry{
				Role:           "judge",
				RequestedModel: vpRoutedModel,
				RoutedModel:    vpRoutedModel,
			},
		},
	}
}

// Judge implements openrouter.JudgeRole.
func (j *vpRecordedJudge) Judge(invocation openrouter.RoleInvocation) (openrouter.JudgeResult, error) {
	// Request identity: the triggering-diff marker the pipeline passes through
	// as the judge invocation's escaped untrusted content (the tier-3
	// invocation carries it in the user payload; UntrustedContent is honored
	// when present too).
	haystack := invocation.User + "\x1f" + invocation.System
	for _, chunk := range invocation.UntrustedContent {
		haystack += "\x1f" + chunk
	}
	identity := ""
	if strings.Contains(haystack, j.marker) {
		identity = j.marker
	}
	if identity == "" {
		return openrouter.JudgeResult{}, fmt.Errorf(
			"recorded judge: invocation carries no request-identity marker %q (fail loud — never positional FIFO)",
			j.marker)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.identity != "" && j.identity != identity {
		return openrouter.JudgeResult{}, fmt.Errorf(
			"recorded judge: unexpected request identity %q (bound to %q; fail loud)", identity, j.identity)
	}
	j.identity = identity
	j.calls = append(j.calls, vpJudgeCall{
		Identity: identity,
		System:   invocation.System,
		User:     invocation.User,
	})
	return j.result, nil
}

// invocationCount is the recorded judge invocation count.
func (j *vpRecordedJudge) invocationCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.calls)
}

/* ------------------------------------------------------------ patch builder */

type vpLineEdit struct {
	line        string
	replacement string
	claimIDs    []string
}

// vpSplitDocLines mirrors the core convention: a trailing newline absorbed,
// 1-addressable lines.
func vpSplitDocLines(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// vpLineEdits builds hunks from line edits over baseContent — each edit
// anchors on the FIRST occurrence of its line (hashSpan convention,
// line-number-second), mirroring the TS patch-builder.
func vpLineEdits(t *testing.T, baseContent string, edits []vpLineEdit) []schemas.PatchHunk {
	t.Helper()
	lines := vpSplitDocLines(baseContent)
	hunks := make([]schemas.PatchHunk, 0, len(edits))
	for _, edit := range edits {
		lineIndex := -1
		for i, line := range lines {
			if line == edit.line {
				lineIndex = i
				break
			}
		}
		if lineIndex < 0 {
			t.Fatalf("patch builder: line not found in base content: %q", edit.line)
		}
		hunk := schemas.PatchHunk{
			Anchor: schemas.PatchAnchor{
				StartLine:   lineIndex + 1,
				EndLine:     lineIndex + 1,
				ContentHash: claims.HashSpan(edit.line),
			},
			DeleteText:   edit.line,
			InsertText:   edit.replacement,
			EvidenceRefs: []schemas.Evidence{vpEvidence()},
		}
		if edit.claimIDs != nil {
			hunk.ClaimIDs = append([]string(nil), edit.claimIDs...)
		}
		hunks = append(hunks, hunk)
	}
	return hunks
}

func vpEvidence() schemas.Evidence {
	return schemas.Evidence{
		Kind: schemas.EvidenceKindSymbolDiff,
		Ref:  "src/payments/refund.ts:3",
		Note: "idempotencyKey became optional for amounts under 10 USD",
	}
}

// vpBuildPatch assembles a COMPLETE DocPatch and runs the adapter's
// fail-closed assembly step: the candidate re-parses through
// DOC_PATCH_SCHEMA (Go: strict decode + Validate).
func vpBuildPatch(t *testing.T, baseContent string, hunks []schemas.PatchHunk, docPath string) *schemas.DocPatch {
	t.Helper()
	candidate := schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           vpRunID,
		DocPath:         docPath,
		BaseContentHash: docutil.ContentHash(baseContent),
		Hunks:           hunks,
		EvidenceRefs:    []schemas.Evidence{vpEvidence()},
		Minimality:      schemas.DocPatchMinimality{Measured: false, AddedLines: 0, RemovedLines: 0},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: vpPolicyVer,
			WriterModel:   vpWriterModel,
			GeneratedAt:   vpGeneratedAt,
		},
	}
	if issues := candidate.Validate(); len(issues) > 0 {
		t.Fatalf("patch builder: candidate patch failed the parse gate: %v", issues)
	}
	return &candidate
}

// vpValidCapturePatch is the canonical valid fixture patch: the
// capture-amount line edit carrying the intake'd claim id.
func vpValidCapturePatch(t *testing.T) *schemas.DocPatch {
	t.Helper()
	return vpBuildPatch(t, vpFixtureDoc, vpLineEdits(t, vpFixtureDoc, []vpLineEdit{{
		line:        vpTriggeredLine,
		replacement: vpCaptureReplacement,
		claimIDs:    []string{vpClaimID},
	}}), vpDocPath)
}

// vpPatchWithRefs carries the additive claim-edit fields (P11-T15, OD-R26).
func vpPatchWithRefs(t *testing.T, claimIDs []string) *schemas.DocPatch {
	t.Helper()
	patch := vpValidCapturePatch(t)
	patch.VerifiedClaims = append([]string(nil), claimIDs...)
	hunks := make([]schemas.PatchHunk, len(patch.Hunks))
	copy(hunks, patch.Hunks)
	hunks[0].VerifiedClaimRefs = append([]string(nil), claimIDs...)
	patch.Hunks = hunks
	return patch
}

// vpFixtureClaims is the intake'd hand-anchored claim (real anchor hash, real
// source pin).
func vpFixtureClaims() []schemas.Claim {
	line := 3
	return []schemas.Claim{{
		ID:   vpClaimID,
		Text: vpIdempotencyLine,
		Anchor: schemas.ClaimAnchor{
			File:        vpDocPath,
			StartLine:   3,
			EndLine:     3,
			ContentHash: claims.HashSpan(vpIdempotencyLine),
		},
		Source: schemas.ClaimSource{
			File:    vpTypesSource,
			Line:    &line,
			BlobSHA: docutil.ContentHash(vpTypesSourceContent),
		},
		Status:  schemas.ClaimStatusVerified,
		History: []schemas.ClaimHistoryEvent{},
	}}
}

// vpFixtureGraph is the D7 document graph snapshot for the fixture doc.
func vpFixtureGraph() *graph.DocumentGraph {
	return &graph.DocumentGraph{
		Mappings: []graph.MappingRecord{{
			Doc:       vpDocPath,
			Sources:   []string{"src/payments/**", "src/api/payments/**"},
			Concepts:  []string{},
			Contracts: []string{"refund-idempotency"},
		}},
		ContractKinds: map[string]schemas.ContractKind{"refund-idempotency": schemas.ContractKindBehavior},
		Errors:        []graph.GraphConfigError{},
	}
}

// vpFixtureContractVector is the tuned P1-T12 vector (hit, not abstaining).
func vpFixtureContractVector() *contracts.ContractVector {
	return &contracts.ContractVector{
		Doc: vpDocPath,
		Vectors: []contracts.ContractVectorEntry{{
			Contract:   "refund-idempotency",
			Kind:       schemas.ContractKindBehavior,
			Hit:        true,
			Confidence: 0.95,
			Evidence:   []string{"src/payments/refund.ts (behavior)"},
			Abstain:    false,
		}},
	}
}

// vpBindingConstraint is the models.yml D13 binding constraint value (TS
// 'must_not_equal_patch-writer_instance'; the tier-3 unit keeps the canonical
// seam unexported, so the test pins the wire literal).
const vpBindingConstraint = "must_not_equal_patch-writer_instance"

// vpPipelineInput is the canonical verifyPatch input for the fixture (the TS
// pipelineInput stand-in).
func vpPipelineInput(docPatch *schemas.DocPatch, judge openrouter.JudgeRole) verifier.VerifyPatchInput {
	return verifier.VerifyPatchInput{
		Patch:               docPatch,
		DocContent:          vpStrPtr(vpFixtureDoc),
		PolicyVersion:       vpPolicyVer,
		RunID:               vpRunID,
		DocPath:             docPatch.DocPath,
		Now:                 vpClock,
		ManifestAllowlist:   map[string]bool{vpDocPath: true},
		TargetMode:          schemas.DocModeAuto,
		TriggeringDiffStats: &patch.DiffStats{Added: 1, Removed: 0},
		Claims:              vpFixtureClaims(),
		WorkspaceSources:    map[string]string{vpTypesSource: vpTypesSourceContent},
		ContractNames:       []string{"refund-idempotency"},
		ContractVector:      vpFixtureContractVector(),
		ContractKinds:       map[string]schemas.ContractKind{"refund-idempotency": schemas.ContractKindBehavior},
		Graph:               vpFixtureGraph(),
		TriggeringDiff:      vpStrPtr(vpTriggerMarker),
		Judge:               judge,
		WriterIdentity: verifier.SessionIdentity{
			Role:      verifier.SessionRolePatchWriter,
			Model:     vpWriterModel,
			SessionID: "writer-session-1",
		},
		JudgeIdentity: verifier.SessionIdentity{
			Role:      verifier.SessionRoleJudge,
			Model:     vpRoutedModel,
			SessionID: "judge-session-1",
		},
		BindingConstraint: vpStrPtr(vpBindingConstraint),
	}
}

func vpStrPtr(s string) *string { return &s }

/* ------------------------------------------------------------------- tests */

// V-P4-1: a valid patch passes all three tiers with per-claim statuses and
// the D3-pinned judge block; the fail-closed self-check holds.
func TestVerifyPatch_V_P4_1_ValidPatchPassesAllThreeTiers(t *testing.T) {
	judge := vpNewRecordedJudge()
	input := vpPipelineInput(vpValidCapturePatch(t), judge)

	report, err := verifier.VerifyPatch(input)
	require.NoError(t, err)
	require.NotNil(t, report)

	// fail-closed self-check: the returned report validates against the schema
	require.Empty(t, report.ValidateReport())

	assert.Equal(t, schemas.VerdictPass, report.Verdict)
	assert.True(t, report.AutoEligible)
	assert.Empty(t, report.AutoBlockReasons)

	// all three tiers ran, each exactly once, every record pass
	require.Len(t, report.Tiers, 3)
	tierNumbers := make([]int, 0, 3)
	for _, tier := range report.Tiers {
		tierNumbers = append(tierNumbers, tier.Tier)
	}
	assert.Equal(t, []int{1, 2, 3}, tierNumbers)
	for _, tier := range report.Tiers {
		assert.Equal(t, schemas.CheckPass, tier.Status, "tier %d", tier.Tier)
		for _, check := range tier.Checks {
			assert.Equal(t, schemas.CheckPass, check.Status, "tier %d check %s", tier.Tier, check.ID)
		}
	}
	assert.Equal(t, vpTier1CheckIDs, vpCheckIDs(report.Tiers[0]))
	assert.Equal(t, vpTier2CheckIDs, vpCheckIDs(report.Tiers[1]))
	assert.Equal(t, vpTier3CheckIDs, vpCheckIDs(report.Tiers[2]))

	// per-claim statuses: the edited claim verified against the real source
	require.Len(t, report.Claims, 1)
	assert.Equal(t, vpClaimID, report.Claims[0].ClaimID)
	assert.Equal(t, schemas.ClaimVerdictSupported, report.Claims[0].Status)
	require.NotNil(t, report.Claims[0].Detail)
	assert.Contains(t, *report.Claims[0].Detail, vpTypesSource+":")

	// judge block: routed model + verdict; the four typed questions surfaced
	require.NotNil(t, report.Judge)
	assert.Equal(t, vpRoutedModel, report.Judge.Model)
	require.NotNil(t, report.Judge.RequestedModel)
	assert.Equal(t, vpRoutedModel, *report.Judge.RequestedModel)
	assert.Equal(t, schemas.VerdictPass, report.Judge.Verdict)
	secondPass := vpFindCheck(report.Tiers[2], "judge-second-pass")
	require.NotNil(t, secondPass)
	require.NotNil(t, secondPass.Detail)
	for _, question := range schemas.VerificationJudgeQuestionIds {
		assert.Contains(t, *secondPass.Detail, question)
	}
	assert.Nil(t, report.JudgeAbstain)

	// the judge was invoked exactly once (recorded double, request-identity keyed)
	assert.Equal(t, 1, judge.invocationCount())
}

// V-P4-1: the deterministic content-addressed patch_ref (state-store
// flattening + canonical hash).
func TestVerifyPatch_V_P4_1_PatchRefIsContentAddressedAndDeterministic(t *testing.T) {
	patch := vpValidCapturePatch(t)
	report, err := verifier.VerifyPatch(vpPipelineInput(patch, vpNewRecordedJudge()))
	require.NoError(t, err)

	ref, err := verifier.ComputePatchRef(patch)
	require.NoError(t, err)
	assert.Equal(t, ref, report.PatchRef)
	assert.Regexp(t, regexp.MustCompile(`^state/docs/docs__payments\.md#patch-[0-9a-f]{16}$`), report.PatchRef)

	// deterministic: the same patch yields the same ref across calls
	again, err := verifier.ComputePatchRef(patch)
	require.NoError(t, err)
	assert.Equal(t, ref, again)

	// a different doc flattens differently (D1 escape convention: `_` → `_u`)
	otherDoc := *patch
	otherDoc.DocPath = "docs/a_b.md"
	otherRef, err := verifier.ComputePatchRef(&otherDoc)
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^state/docs/docs__a_ub\.md#patch-[0-9a-f]{16}$`), otherRef)
}

// Fail-closed input gate: a schema-invalid patch NEVER reaches the tiers (and
// the judge port is never invoked).
func TestVerifyPatch_FailClosedInputGate_SchemaInvalidPatchNeverReachesTiers(t *testing.T) {
	patch := vpValidCapturePatch(t)
	// a hunk WITHOUT evidence_refs violates PATCH_HUNK_SCHEMA (min 1) — built
	// as a raw value so the builder's parse step cannot mask it.
	invalid := *patch
	invalid.Hunks = []schemas.PatchHunk{{
		Anchor:     patch.Hunks[0].Anchor,
		DeleteText: "x",
		InsertText: "y",
	}}
	judge := vpNewRecordedJudge()

	_, err := verifier.VerifyPatch(vpPipelineInput(&invalid, judge))

	require.Error(t, err)
	var notVerifiable *verifier.PatchNotVerifiableError
	require.True(t, errors.As(err, &notVerifiable), "expected *PatchNotVerifiableError, got %T", err)
	assert.Equal(t, verifier.PatchNotVerifiableCode, notVerifiable.Code())
	// the message carries the schema issues verbatim
	assert.Contains(t, notVerifiable.Error(), "evidence_refs")
	// the tiers (and the judge port) never ran
	assert.Equal(t, 0, judge.invocationCount())
}

// Wave-4-flagged refinement (invariant 9): a Tier-1 run-id skip (no patched
// content) degrades Tier 3 to a synthesized judge_abstain and the verdict to
// review — with the FACTUAL, idempotent-skip detail.
func TestVerifyPatch_Wave4_RunIDSkipSynthesizesJudgeAbstention(t *testing.T) {
	judge := vpNewRecordedJudge()
	input := vpPipelineInput(vpValidCapturePatch(t), judge)
	// the idempotency port reports this run already applied ⇒ the apply
	// simulation SKIPS (never a failure) with no patched content.
	input.IsRunApplied = func(runID, docPath string) bool { return true }

	report, err := verifier.VerifyPatch(input)
	require.NoError(t, err)
	require.NotNil(t, report)
	require.Empty(t, report.ValidateReport())

	// tier 1 passed (a run-id skip is not a failure) but attests nothing
	require.Len(t, report.Tiers, 3)
	assert.Equal(t, schemas.CheckPass, report.Tiers[0].Status)
	assert.Equal(t, schemas.CheckSkipped, vpFindCheck(report.Tiers[0], "patch-applies-cleanly").Status)

	// tier 2: decomposition + binding ran on patch/workspace-only data; the
	// bound claim carries 'unchecked' (head-verification fail-closed skip)
	assert.Equal(t, schemas.CheckPass, vpFindCheck(report.Tiers[1], "claim-decomposition").Status)
	assert.Equal(t, schemas.CheckPass, vpFindCheck(report.Tiers[1], "claim-anchor-binding").Status)
	assert.Equal(t, schemas.CheckSkipped, vpFindCheck(report.Tiers[1], "head-verification").Status)

	require.Len(t, report.Claims, 1)
	assert.Equal(t, schemas.ClaimVerdictUnchecked, report.Claims[0].Status)
	require.NotNil(t, report.Claims[0].Detail)
	assert.Contains(t, *report.Claims[0].Detail, "decomposed and bound but not verified")

	// tier 3: skipped with BOTH checks emitted; the judge was never invoked
	tier3 := report.Tiers[2]
	assert.Equal(t, schemas.CheckSkipped, tier3.Status)
	assert.Equal(t, vpTier3CheckIDs, vpCheckIDs(tier3))
	assert.Equal(t, schemas.CheckPass, vpFindCheck(tier3, "judge-independence").Status)
	assert.Equal(t, schemas.CheckSkipped, vpFindCheck(tier3, "judge-second-pass").Status)
	assert.Nil(t, report.Judge)

	// the synthesized detail is FACTUAL: tier 1 passed (run-id skip), it did
	// not fail — the abstain names the idempotent skip and the unchecked claims
	require.NotNil(t, report.JudgeAbstain)
	assert.Equal(t, schemas.VerificationJudgeAbstain{
		Reason: "other",
		Detail: "tier 3 skipped: patched content unavailable (run-id idempotent skip; claims carried unchecked)",
	}, *report.JudgeAbstain)
	assert.Equal(t, schemas.VerdictReview, report.Verdict)

	// 'unchecked' does NOT block auto (resolveAutoEligibility, OD-R09)
	assert.True(t, report.AutoEligible)
	assert.Empty(t, report.AutoBlockReasons)

	assert.Equal(t, 0, judge.invocationCount())
}

// Pipeline determinism (P5 seam): same inputs + same scripted judge ⇒
// deep-equal reports (replayable evidence).
func TestVerifyPatch_Determinism_SameInputsDeepEqualReports(t *testing.T) {
	patch := vpValidCapturePatch(t)

	first, err := verifier.VerifyPatch(vpPipelineInput(patch, vpNewRecordedJudge()))
	require.NoError(t, err)
	// a FRESH replay judge double for the second run (same scripted body)
	second, err := verifier.VerifyPatch(vpPipelineInput(patch, vpNewRecordedJudge()))
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

// V-P11-T15: legacy patches without claim-edit fields parse and verify
// byte-identically — NO check carries the refs surface.
func TestVerifyPatch_V_P11_T15_LegacyPatchUntouchedByTheRefsGate(t *testing.T) {
	patch := vpValidCapturePatch(t)
	// the legacy patch carries no claim-edit keys at all
	require.Empty(t, patch.VerifiedClaims)
	for _, hunk := range patch.Hunks {
		require.Empty(t, hunk.VerifiedClaimRefs)
	}

	report, err := verifier.VerifyPatch(vpPipelineInput(patch, vpNewRecordedJudge()))
	require.NoError(t, err)
	require.Empty(t, report.ValidateReport())
	assert.Equal(t, schemas.VerdictPass, report.Verdict)

	// byte-identical posture: the tier-2 claim-decomposition record has no inputs
	decomposition := vpFindCheck(report.Tiers[1], "claim-decomposition")
	require.NotNil(t, decomposition)
	assert.Nil(t, decomposition.Inputs)

	// determinism: an identical legacy run produces a deep-equal report
	again, err := verifier.VerifyPatch(vpPipelineInput(patch, vpNewRecordedJudge()))
	require.NoError(t, err)
	assert.Equal(t, report, again)
}

// V-P11-T15: an unresolvable verified_claim_ref fails closed at verification,
// naming the ref — the tiers never run.
func TestVerifyPatch_V_P11_T15_UnresolvableRefFailsClosedBeforeTheTiers(t *testing.T) {
	judge := vpNewRecordedJudge()
	patch := vpPatchWithRefs(t, []string{vpClaimID, vpUnresolvable})

	_, err := verifier.VerifyPatch(vpPipelineInput(patch, judge))

	require.Error(t, err)
	var notVerifiable *verifier.PatchNotVerifiableError
	require.True(t, errors.As(err, &notVerifiable), "expected *PatchNotVerifiableError, got %T", err)
	assert.Equal(t, verifier.PatchNotVerifiableCode, notVerifiable.Code())
	assert.Contains(t, notVerifiable.Error(), vpUnresolvable)
	assert.Contains(t, notVerifiable.Error(), "verified-claim-refs resolution gate")
	// the tiers (and the judge port) never ran — fail closed BEFORE tier 1
	assert.Equal(t, 0, judge.invocationCount())
}

// V-P11-T15: refs present with NO claims source available at all ⇒ the same
// fail-closed behavior (never a silent pass, never an auto-strip).
func TestVerifyPatch_V_P11_T15_RefsWithoutClaimSourceFailClosed(t *testing.T) {
	judge := vpNewRecordedJudge()
	patch := vpPatchWithRefs(t, []string{vpClaimID})
	input := vpPipelineInput(patch, judge)
	input.Claims = nil // no claim source available at all

	_, err := verifier.VerifyPatch(input)

	require.Error(t, err)
	var notVerifiable *verifier.PatchNotVerifiableError
	require.True(t, errors.As(err, &notVerifiable), "expected *PatchNotVerifiableError, got %T", err)
	assert.Equal(t, verifier.PatchNotVerifiableCode, notVerifiable.Code())
	assert.Contains(t, notVerifiable.Error(), vpClaimID)
	assert.Contains(t, notVerifiable.Error(), "no claim source is available")
	assert.Equal(t, 0, judge.invocationCount())
}

// V-P11-T15: resolvable refs pass verification and surface on the Tier-2
// claim-decomposition check's existing replayable inputs surface.
func TestVerifyPatch_V_P11_T15_ResolvedRefsSurfaceOnClaimDecompositionInputs(t *testing.T) {
	patch := vpPatchWithRefs(t, []string{vpClaimID})

	report, err := verifier.VerifyPatch(vpPipelineInput(patch, vpNewRecordedJudge()))
	require.NoError(t, err)
	require.Empty(t, report.ValidateReport())

	// all three tiers pass — the gate resolved and did not block
	for _, tier := range report.Tiers {
		assert.Equal(t, schemas.CheckPass, tier.Status, "tier %d", tier.Tier)
	}
	assert.Equal(t, schemas.VerdictPass, report.Verdict)

	// the resolved refs are visible on the minimal additive place: the tier-2
	// claim-decomposition check's existing `inputs` surface
	decomposition := vpFindCheck(report.Tiers[1], "claim-decomposition")
	require.NotNil(t, decomposition)
	assert.Equal(t, []string{vpClaimID}, decomposition.Inputs)

	// no new check id exists; no other tier-2 check carries the resolved refs
	assert.Equal(t, vpTier2CheckIDs, vpCheckIDs(report.Tiers[1]))
	for _, check := range report.Tiers[1].Checks {
		if check.ID == "claim-decomposition" {
			continue
		}
		for _, input := range check.Inputs {
			assert.NotEqual(t, vpClaimID, input, "check %s carries the resolved refs", check.ID)
		}
	}
}

/* ------------------------------------------------------------------ helpers */

func vpCheckIDs(tier schemas.VerificationTierReport) []string {
	ids := make([]string, 0, len(tier.Checks))
	for _, check := range tier.Checks {
		ids = append(ids, check.ID)
	}
	return ids
}

func vpFindCheck(tier schemas.VerificationTierReport, id string) *schemas.VerificationCheck {
	for i := range tier.Checks {
		if tier.Checks[i].ID == id {
			return &tier.Checks[i]
		}
	}
	return nil
}
