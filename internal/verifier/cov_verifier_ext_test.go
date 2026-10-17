package verifier_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ---------------------------------------------------------------- bin.go */

func TestCOV_VERIFIER_BinHelpAndConfigErrors(t *testing.T) {
	var out, errOut strings.Builder
	io := verifier.AuditReplayCLIIo{
		Stdout: func(line string) { out.WriteString(line + "\n") },
		Stderr: func(line string) { errOut.WriteString(line + "\n") },
	}
	code := verifier.RunAuditReplayCLI([]string{"--help"}, io)
	assert.Equal(t, verifier.AuditCLIExitOK, code)
	assert.Contains(t, out.String(), "docsync-audit")

	out.Reset(); errOut.Reset()
	code = verifier.RunAuditReplayCLI([]string{"--bogus"}, io)
	assert.Equal(t, verifier.AuditCLIExitConfig, code)
	assert.Contains(t, errOut.String(), "unknown argument")

	out.Reset(); errOut.Reset()
	code = verifier.RunAuditReplayCLI([]string{"--state-dir", t.TempDir()}, io)
	assert.Equal(t, verifier.AuditCLIExitConfig, code)

	out.Reset(); errOut.Reset()
	code = verifier.RunAuditReplayCLI([]string{"--run", "x"}, io)
	assert.Equal(t, verifier.AuditCLIExitConfig, code)

	out.Reset(); errOut.Reset()
	code = verifier.RunAuditReplayCLI([]string{"--state-dir", filepath.Join(t.TempDir(), "nope"), "--run", "x"}, io)
	assert.Equal(t, verifier.AuditCLIExitConfig, code)
	assert.Contains(t, errOut.String(), "state dir does not exist")
}

func TestCOV_VERIFIER_BinReplaySuccess(t *testing.T) {
	root := seedStateBinCov(t)
	var out, errOut strings.Builder
	io := verifier.AuditReplayCLIIo{
		Stdout: func(line string) { out.WriteString(line + "\n") },
		Stderr: func(line string) { errOut.WriteString(line + "\n") },
	}
	code := verifier.RunAuditReplayCLI([]string{"--state-dir", root, "--run", auditRunID}, io)
	assert.Equal(t, verifier.AuditCLIExitOK, code)
	assert.Contains(t, out.String(), "# docsync audit trail")
	assert.Empty(t, errOut.String())
}

func seedStateBinCov(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runs := filepath.Join(root, "state", "runs")
	docs := filepath.Join(root, "state", "docs")
	require.NoError(t, os.MkdirAll(runs, 0o755))
	require.NoError(t, os.MkdirAll(docs, 0o755))
	writeJSON(t, filepath.Join(runs, auditRunID+".json"), map[string]any{
		"schema": "docsync.fix-run-record/v1",
		"run_id": auditRunID,
		"at":     auditNow,
		"status": "applied",
	})
	writeJSON(t, filepath.Join(runs, "prov-"+auditRunID+".json"), map[string]any{
		"schema":        "docsync.provenance/v1",
		"run_id":        auditRunID,
		"source_commit": strings.Repeat("a", 40),
	})
	writeJSON(t, filepath.Join(docs, "docs__payments.md.json"), map[string]any{
		"schema": "docsync.doc-state/v1",
		"path":   "docs/payments.md",
		"verifications": []any{
			map[string]any{"report_hash": strings.Repeat("b", 64), "report": map[string]any{"verdict": "pass"}, "at": auditNow, "run_id": auditRunID},
		},
	})
	require.NoError(t, os.WriteFile(filepath.Join(root, "state", "incidents.jsonl"), []byte("{\"schema\":\"x\",\"run_id\":\""+auditRunID+"\",\"rule\":\"r\",\"offending_path\":\"p\",\"detail\":\"d\"}\n"), 0o644))
	return root
}

/* ---------------------------------------------------------------- pipeline */

func TestCOV_VERIFIER_PipelineNilJudgeFailsClosed(t *testing.T) {
	input := vpPipelineInput(vpValidCapturePatch(t), nil)
	_, err := verifier.VerifyPatch(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge port")
}

func TestCOV_VERIFIER_PipelinePatchNotVerifiableUnwrap(t *testing.T) {
	patch := vpValidCapturePatch(t)
	invalid := *patch
	invalid.Hunks = []schemas.PatchHunk{{Anchor: patch.Hunks[0].Anchor, DeleteText: "x", InsertText: "y"}}
	judge := vpNewRecordedJudge()
	_, err := verifier.VerifyPatch(vpPipelineInput(&invalid, judge))
	require.Error(t, err)
	var pnve *verifier.PatchNotVerifiableError
	require.ErrorAs(t, err, &pnve)
	assert.Equal(t, verifier.PatchNotVerifiableCode, pnve.Code())
	unwrapped := pnve.Unwrap()
	require.NotNil(t, unwrapped)
	assert.Equal(t, docutil.CodeVerify, docutil.CodeOf(unwrapped))
}

func TestCOV_VERIFIER_PipelineThrownPortErrorDetail(t *testing.T) {
	input := vpPipelineInput(vpValidCapturePatch(t), t3ErroringJudge{err: t3ErrBoom})
	report, err := verifier.VerifyPatch(input)
	require.NoError(t, err)
	require.NotNil(t, report)
	require.NotNil(t, report.JudgeAbstain)
	assert.Contains(t, report.JudgeAbstain.Detail, "judge invocation failed: Error: boom")
}

/* ---------------------------------------------------------------- tier1 */

func TestCOV_VERIFIER_Tier1ContentOnlyGitSatisfiesPort(t *testing.T) {
	var _ patch.CatFilePort = contentOnlyGitCov{}
}

type contentOnlyGitCov struct{}

func (contentOnlyGitCov) CatFile(_ context.Context, _, _ string) (gitx.CatFileResult, error) {
	return gitx.CatFileResult{}, &structDefectCov{msg: "x"}
}


type structDefectCov struct{ msg string }

func (e *structDefectCov) Error() string { return e.msg }

func TestCOV_VERIFIER_Tier1LinkReachabilityError(t *testing.T) {
	doc := t1LinksDoc("See [the spec](https://example.com/spec).")
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc), t1Override{
		t1LinkInputs: &verifier.LinkCheckInputs{
			KnownDocs:   map[string]string{},
			IsReachable: func(url string) (bool, error) { return false, errors.New("dns failure") },
		},
	}))
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckFail, result.Status)
	t1AssertDetailContains(t, t1CheckOf(t, result, "link-resolution"), "reachability port rejected")
}

/* ---------------------------------------------------------------- tier2 */

func TestCOV_VERIFIER_Tier2NilPatchError(t *testing.T) {
	_, err := verifier.RunTier2(verifier.Tier2Input{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "patch is required")
}

func TestCOV_VERIFIER_Tier2DuplicateIndexError(t *testing.T) {
	dup := []verifier.DecomposedClaim{
		{Index: 0, Text: "a", Kind: verifier.DecomposedClaimKindOther},
		{Index: 0, Text: "b", Kind: verifier.DecomposedClaimKindOther},
	}
	_, err := verifier.CreateClaimStatusTracker(dup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate decomposed-claim index")
}

func TestCOV_VERIFIER_Tier2TrackerInvalidStatus(t *testing.T) {
	tracker, err := verifier.CreateClaimStatusTracker([]verifier.DecomposedClaim{
		{Index: 0, Text: "a", Kind: verifier.DecomposedClaimKindOther},
	})
	require.NoError(t, err)
	err = tracker.Set(0, "not-a-status")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid verification claim status")
}

func TestCOV_VERIFIER_Tier2TrackerUnknownIndex(t *testing.T) {
	tracker, err := verifier.CreateClaimStatusTracker([]verifier.DecomposedClaim{
		{Index: 0, Text: "a", Kind: verifier.DecomposedClaimKindOther},
	})
	require.NoError(t, err)
	err = tracker.Set(99, schemas.ClaimVerdictSupported)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown decomposed-claim index")
}

func TestCOV_VERIFIER_Tier2BindingUnboundNoTokens(t *testing.T) {
	claims := []verifier.DecomposedClaim{
		{Index: 0, Text: "retries are idempotent", Kind: verifier.DecomposedClaimKindBehavioral},
	}
	input := verifier.Tier2Input{
		Patch:            vpValidCapturePatch(t),
		WorkspaceSources: map[string]string{},
	}
	bindings := verifier.BindClaims(claims, input)
	require.Len(t, bindings, 1)
	assert.False(t, bindings[0].Bound)
	assert.Equal(t, verifier.BindingViaNone, bindings[0].Via)
	assert.Equal(t, "no workspace source matches the claimed token(s)", bindings[0].Detail)
}

func TestCOV_VERIFIER_Tier2BindingClaimStoreWithLine(t *testing.T) {
	claims := []verifier.DecomposedClaim{
		{Index: 0, Text: "x", Kind: verifier.DecomposedClaimKindOther, ExistingClaimID: "claim_182"},
	}
	stored := schemas.Claim{
		ID:     "claim_182",
		Text:   "x",
		Source: schemas.ClaimSource{File: "src/x.ts", Line: t2IntPtr(5)},
	}
	input := verifier.Tier2Input{Claims: []schemas.Claim{stored}}
	bindings := verifier.BindClaims(claims, input)
	require.Len(t, bindings, 1)
	assert.True(t, bindings[0].Bound)
	assert.Equal(t, verifier.BindingViaClaimStore, bindings[0].Via)
	require.NotNil(t, bindings[0].Line)
	assert.Equal(t, 5, *bindings[0].Line)
}

func TestCOV_VERIFIER_Tier2BindingExistingClaimIDNotFound(t *testing.T) {
	claims := []verifier.DecomposedClaim{
		{Index: 0, Text: "x", Kind: verifier.DecomposedClaimKindOther, ExistingClaimID: "missing_id"},
	}
	input := verifier.Tier2Input{
		Patch:            vpValidCapturePatch(t),
		WorkspaceSources: map[string]string{"src/x.ts": "x"},
	}
	bindings := verifier.BindClaims(claims, input)
	require.Len(t, bindings, 1)
	assert.False(t, bindings[0].Bound)
}

func TestCOV_VERIFIER_Tier2SignatureBindingReformatted(t *testing.T) {
	claims := []verifier.DecomposedClaim{
		{Index: 0, Text: "calls `foo.bar()`", Kind: verifier.DecomposedClaimKindSignature},
	}
	input := verifier.Tier2Input{
		Patch:            vpValidCapturePatch(t),
		WorkspaceSources: map[string]string{"src/x.ts": "function foo.bar() {}"},
	}
	bindings := verifier.BindClaims(claims, input)
	require.Len(t, bindings, 1)
	assert.True(t, bindings[0].Bound)
}

func TestCOV_VERIFIER_Tier2CoverageEntryNotFound(t *testing.T) {
	input := verifier.Tier2Input{
		Patch:          vpValidCapturePatch(t),
		Graph:          &graph.DocumentGraph{Mappings: []graph.MappingRecord{{Doc: "docs/other.md", Contracts: []string{"refund-idempotency"}}}, ContractKinds: map[string]schemas.ContractKind{"refund-idempotency": schemas.ContractKindBehavior}},
		ContractNames:  []string{"refund-idempotency"},
		ContractKinds:  map[string]schemas.ContractKind{"refund-idempotency": schemas.ContractKindBehavior},
		Claims:         []schemas.Claim{},
	}
	check := verifier.CheckCoverageDefect(input)
	assert.Equal(t, schemas.CheckPass, check.Status)
	require.NotNil(t, check.Detail)
	assert.Contains(t, *check.Detail, "no contract coverage entry")
}

func TestCOV_VERIFIER_Tier2HeadVerifySkipEmptyDecomposed(t *testing.T) {
	check, err := verifier.CheckHeadVerification(verifier.Tier2Input{}, nil, nil, mustTracker(t), nil)
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckSkipped, check.Status)
	assert.Contains(t, *check.Detail, "no claims")
}

func TestCOV_VERIFIER_Tier2HeadVerifyNoTokens(t *testing.T) {
	claims2 := []verifier.DecomposedClaim{
		{Index: 0, Text: "returns 404 on not found", Kind: verifier.DecomposedClaimKindErrorCode},
	}
	tracker, err := verifier.CreateClaimStatusTracker(claims2)
	require.NoError(t, err)
	input := verifier.Tier2Input{
		Patch:          vpValidCapturePatch(t),
		DocContent:     t1StrPtr(t1ProseDoc),
		PatchedContent: t1StrPtr(t1ProseDoc),
		WorkspaceSources: map[string]string{"src/x.ts": "returns 404 on not found"},
	}
	bindings := []verifier.ClaimBinding{
		{Index: 0, Bound: true, Via: verifier.BindingViaClaimStore, File: "src/x.ts", Line: t2IntPtr(1)},
	}
	check, err := verifier.CheckHeadVerification(input, claims2, bindings, tracker, nil)
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckPass, check.Status)
}

func TestCOV_VERIFIER_Tier2HeadVerifySignatureMismatch(t *testing.T) {
	claims2 := []verifier.DecomposedClaim{
		{Index: 0, Text: "calls `foo.bar()`", Kind: verifier.DecomposedClaimKindSignature},
	}
	tracker, err := verifier.CreateClaimStatusTracker(claims2)
	require.NoError(t, err)
	input := verifier.Tier2Input{
		Patch:          vpValidCapturePatch(t),
		DocContent:     t1StrPtr(t1ProseDoc),
		PatchedContent: t1StrPtr(t1ProseDoc),
		WorkspaceSources: map[string]string{"src/x.ts": "completely different"},
	}
	bindings := []verifier.ClaimBinding{
		{Index: 0, Bound: true, Via: verifier.BindingViaClaimStore, File: "src/x.ts", Line: t2IntPtr(1)},
	}
	check, err := verifier.CheckHeadVerification(input, claims2, bindings, tracker, nil)
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckFail, check.Status)
}

func TestCOV_VERIFIER_Tier2OverDeletionUnanchoredNote(t *testing.T) {
	claim := schemas.Claim{
		ID:   "claim_x",
		Text: "x",
		Anchor: schemas.ClaimAnchor{
			File:        docsPaymentsMDCov,
			StartLine:   1,
			EndLine:     1,
			ContentHash: strings.Repeat("0", 64),
		},
		Source: schemas.ClaimSource{File: "src/x.ts"},
		Status: schemas.ClaimStatusVerified,
	}
	patch := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1ProseDoc,
		hunks: t1LineEdits(t1ProseDoc, []t1LineEdit{{line: "## Refunds", replacement: "## Refunds (revised)"}}),
	})
	input := verifier.Tier2Input{
		Patch:      patch,
		DocContent: t1StrPtr(t1ProseDoc),
		Claims:     []schemas.Claim{claim},
	}
	tracker, err := verifier.CreateClaimStatusTracker(verifier.DecomposePatchClaims(patch))
	require.NoError(t, err)
	check, err := verifier.CheckOverDeletionGuard(input, tracker)
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckPass, check.Status)
	require.NotNil(t, check.Detail)
	assert.Contains(t, *check.Detail, "unresolvable anchor note")
}

const docsPaymentsMDCov = "docs/payments.md"

/* ---------------------------------------------------------------- tier3 */

func TestCOV_VERIFIER_Tier3IndependenceSameModelWarning(t *testing.T) {
	input := t3BaseInput(t, t3ReplayJudge(t))
	input.JudgeIdentity = verifier.SessionIdentity{
		Role:      verifier.SessionRoleJudge,
		Model:     t3WriterModel,
		SessionID: "judge-session-2",
	}
	input.BindingConstraint = "must_not_equal_patch-writer_instance"
	check := verifier.CheckJudgeIndependence(input)
	assert.Equal(t, schemas.CheckPass, check.Status)
	require.NotNil(t, check.Detail)
	assert.Contains(t, *check.Detail, "warning-grade")
}

func TestCOV_VERIFIER_Tier3SkipPatchedContentNil(t *testing.T) {
	input := t3BaseInput(t, t3ReplayJudge(t))
	input.PatchedContent = nil
	result := verifier.RunTier3(input)
	assert.Equal(t, schemas.CheckSkipped, result.Status)
	assert.Equal(t, schemas.CheckSkipped, result.Checks[1].Status)
	require.NotNil(t, result.Checks[1].Detail)
	assert.Equal(t, verifier.PatchedContentSkipDetail, *result.Checks[1].Detail)
}

func TestCOV_VERIFIER_Tier3JudgeAbstainOther(t *testing.T) {
	result := verifier.RunTier3(t3BaseInput(t, t3ScriptedJudge{result: openrouter.JudgeResult{
		Kind:      openrouter.JudgeKindAbstain,
		Reason:    t3ReasonPtr(schemas.AbstainReasonOther),
		Telemetry: t3ScriptedTelemetry,
	}}))
	assert.Equal(t, schemas.CheckSkipped, result.Status)
	assert.Equal(t, schemas.CheckSkipped, result.Checks[1].Status)
	require.NotNil(t, result.Context.JudgeAbstain)
	assert.Equal(t, string(schemas.AbstainReasonOther), result.Context.JudgeAbstain.Reason)
}

/* ---------------------------------------------------------------- verification stage */

func TestCOV_VERIFIER_StageTriggeringDiffStatsNil(t *testing.T) {
	judge := vpNewRecordedJudge()
	port, err := verifier.CreateVerificationStagePort(vsDeps(judge))
	require.NoError(t, err)
	ctx := vsCtx(vsManifest(vpModePtr(schemas.DocModeAuto)), *vpFixtureGraph())
	result, err := port.Verify(vsStageInput(ctx, vpValidCapturePatch(t), vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)
	require.NotNil(t, result.Report)
}

var _ = context.Background
var _ = errors.New
var _ = os.MkdirAll

/* ---------------------------------------------------------------- final gap closers */

func TestCOV_VERIFIER_Tier1ContentOnlyGitCatFileError(t *testing.T) {
	g := contentOnlyGitCov{}
	_, err := g.CatFile(context.Background(), "rev", "path")
	require.Error(t, err)
	assert.Equal(t, "x", err.Error())
}

func TestCOV_VERIFIER_Tier2SignaturePrefixMatchOnly(t *testing.T) {
	// Token "add(a)" matches only via identifier prefix "add" in source line.
	claims2 := []verifier.DecomposedClaim{
		{Index: 0, Text: "calls `add(a)`", Kind: verifier.DecomposedClaimKindSignature},
	}
	input := verifier.Tier2Input{
		Patch:            vpValidCapturePatch(t),
		WorkspaceSources: map[string]string{"src/x.ts": "const add = 1;"},
	}
	bindings := verifier.BindClaims(claims2, input)
	require.Len(t, bindings, 1)
	assert.True(t, bindings[0].Bound)
	assert.Equal(t, verifier.BindingViaWorkspaceToken, bindings[0].Via)
}

func TestCOV_VERIFIER_StageCollisionUnwrap(t *testing.T) {
	same := verifier.SessionIdentity{Role: verifier.SessionRoleJudge, Model: "m", SessionID: "sess-1"}
	_, err := verifier.CreateVerificationStagePort(verifier.VerificationStageDeps{
		Judge:          nil,
		WriterIdentity: same,
		JudgeIdentity:  same,
	})
	require.Error(t, err)
	var collision *verifier.JudgeWriterSessionCollisionError
	require.ErrorAs(t, err, &collision)
	unwrapped := collision.Unwrap()
	require.NotNil(t, unwrapped)
	assert.Equal(t, docutil.CodeVerify, docutil.CodeOf(unwrapped))
}

/* ---------------------------------------------------------------- replay edge cases */

func TestCOV_VERIFIER_ReplayUndecodableRunRecord(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "state", "runs")
	docs := filepath.Join(root, "state", "docs")
	require.NoError(t, os.MkdirAll(runs, 0o755))
	require.NoError(t, os.MkdirAll(docs, 0o755))
	// Undecodable JSON in runs dir — skipped (declared gap, not error).
	require.NoError(t, os.WriteFile(filepath.Join(runs, "bad.json"), []byte("{not-json"), 0o644))
	writeJSON(t, filepath.Join(runs, auditRunID+".json"), map[string]any{
		"schema": "docsync.fix-run-record/v1", "run_id": auditRunID, "at": auditNow, "status": "applied",
	})
	writeJSON(t, filepath.Join(runs, "prov-"+auditRunID+".json"), map[string]any{
		"schema": "docsync.provenance/v1", "run_id": auditRunID, "source_commit": strings.Repeat("a", 40),
	})
	var out, errOut strings.Builder
	io := verifier.AuditReplayCLIIo{
		Stdout: func(line string) { out.WriteString(line + "\n") },
		Stderr: func(line string) { errOut.WriteString(line + "\n") },
	}
	code := verifier.RunAuditReplayCLI([]string{"--state-dir", root, "--run", auditRunID}, io)
	assert.Equal(t, verifier.AuditCLIExitOK, code)
}

func TestCOV_VERIFIER_ReplayProvenanceFallbackLoop(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "state", "runs")
	docs := filepath.Join(root, "state", "docs")
	require.NoError(t, os.MkdirAll(runs, 0o755))
	require.NoError(t, os.MkdirAll(docs, 0o755))
	// No prov-<run>.json, but another run's provenance file matches run_id.
	writeJSON(t, filepath.Join(runs, auditRunID+".json"), map[string]any{
		"schema": "docsync.fix-run-record/v1", "run_id": auditRunID, "at": auditNow, "status": "applied",
	})
	writeJSON(t, filepath.Join(runs, "prov-other.json"), map[string]any{
		"schema": "docsync.provenance/v1", "run_id": auditRunID, "source_commit": strings.Repeat("b", 40),
	})
	var out, errOut strings.Builder
	io := verifier.AuditReplayCLIIo{
		Stdout: func(line string) { out.WriteString(line + "\n") },
		Stderr: func(line string) { errOut.WriteString(line + "\n") },
	}
	code := verifier.RunAuditReplayCLI([]string{"--state-dir", root, "--run", auditRunID}, io)
	assert.Equal(t, verifier.AuditCLIExitOK, code)
}

func TestCOV_VERIFIER_ReplayDocLedgerNonArray(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "state", "runs")
	docs := filepath.Join(root, "state", "docs")
	require.NoError(t, os.MkdirAll(runs, 0o755))
	require.NoError(t, os.MkdirAll(docs, 0o755))
	writeJSON(t, filepath.Join(runs, auditRunID+".json"), map[string]any{
		"schema": "docsync.fix-run-record/v1", "run_id": auditRunID, "at": auditNow, "status": "applied",
	})
	writeJSON(t, filepath.Join(runs, "prov-"+auditRunID+".json"), map[string]any{
		"schema": "docsync.provenance/v1", "run_id": auditRunID, "source_commit": strings.Repeat("a", 40),
	})
	// verifications is not an array → skipped.
	writeJSON(t, filepath.Join(docs, "docs__payments.md.json"), map[string]any{
		"schema": "docsync.doc-state/v1", "path": "docs/payments.md",
		"verifications": "not-an-array",
	})
	var out, errOut strings.Builder
	io := verifier.AuditReplayCLIIo{
		Stdout: func(line string) { out.WriteString(line + "\n") },
		Stderr: func(line string) { errOut.WriteString(line + "\n") },
	}
	code := verifier.RunAuditReplayCLI([]string{"--state-dir", root, "--run", auditRunID}, io)
	assert.Equal(t, verifier.AuditCLIExitOK, code)
}

func TestCOV_VERIFIER_ReplayIncidentsPresent(t *testing.T) {
	root := seedStateBinCov(t)
	// Add a second incident for a different run → should not appear.
	require.NoError(t, os.WriteFile(filepath.Join(root, "state", "incidents.jsonl"), []byte(
		"{\"schema\":\"x\",\"run_id\":\"other-run\",\"rule\":\"r\",\"offending_path\":\"p\",\"detail\":\"d\"}\n"+
			"{\"schema\":\"x\",\"run_id\":\""+auditRunID+"\",\"rule\":\"r2\",\"offending_path\":\"p2\",\"detail\":\"d2\"}\n",
	), 0o644))
	var out, errOut strings.Builder
	io := verifier.AuditReplayCLIIo{
		Stdout: func(line string) { out.WriteString(line + "\n") },
		Stderr: func(line string) { errOut.WriteString(line + "\n") },
	}
	code := verifier.RunAuditReplayCLI([]string{"--state-dir", root, "--run", auditRunID}, io)
	assert.Equal(t, verifier.AuditCLIExitOK, code)
	assert.Contains(t, out.String(), "r2")
	assert.Contains(t, out.String(), "p2")
	assert.NotContains(t, out.String(), "other-run")
}

/* ---------------------------------------------------------------- pipeline claim refs */

func TestCOV_VERIFIER_PipelineRefsResolvedSurface(t *testing.T) {
	// Verified claim refs that resolve → surface on decomposition.
	patch := vpPatchWithRefs(t, []string{vpClaimID})
	report, err := verifier.VerifyPatch(vpPipelineInput(patch, vpNewRecordedJudge()))
	require.NoError(t, err)
	require.NotNil(t, report.Tiers)
	decomp := vpFindCheck(report.Tiers[1], "claim-decomposition")
	require.NotNil(t, decomp)
	assert.Equal(t, []string{vpClaimID}, decomp.Inputs)
}

var _ = filepath.Join
var _ = os.WriteFile
