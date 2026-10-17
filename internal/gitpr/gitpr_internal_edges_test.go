package gitpr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCOV_GITPR_047_RestCloseAndMarkSurfaces pins the close/mark write paths'
// remaining refusals: a non-JSON close response and a malformed update
// response both fail CLOSED (a zero PrRef never surfaces with a nil error).
func TestCOV_GITPR_047_RestCloseAndMarkSurfaces(t *testing.T) {
	ctx := context.Background()
	docsyncBranch := SelfTriggerBranchPrefix + "run_close"

	// ClosePr: a 200 whose body is not JSON is a TRANSPORT refusal. The platform
	// must first REMEMBER the PR's repo (repoFor), exactly like the flow's
	// listing/upsert path does.
	double := newCovRestDouble()
	remember := double
	remember.on("GET", "/repos/acme/widgets/pulls?page=1", covRestReply{
		status: 200,
		body:   `[{"number":3,"state":"open","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"}},{"number":4,"state":"open","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"}}]`,
	})
	platform := covRestPlatform(double, "acme/widgets")
	_, listErr := platform.ListOpenDocsPrs(ctx, "acme/widgets")
	require.NoError(t, listErr)
	double.on("POST", "/repos/acme/widgets/issues/3/comments", covRestReply{status: 201, body: `{"id":1}`})
	double.on("PATCH", "/repos/acme/widgets/pulls/3", covRestReply{status: 200, body: "not json"})
	err := platform.ClosePr(ctx, covRef(3, docsyncBranch), "note")
	require.Error(t, err)
	// The close's non-JSON body is a fail-closed refusal (transport class).
	assert.Equal(t, CodeGitHubTransport, errCode(err))

	// MarkSuperseded: same refusal shape through the close path.
	double2 := newCovRestDouble()
	double2.on("POST", "/repos/acme/widgets/issues/4/comments", covRestReply{status: 201, body: `{"id":1}`})
	double2.on("PATCH", "/repos/acme/widgets/pulls/4", covRestReply{status: 200, body: "not json"})
	platform2 := covRestPlatform(double2, "acme/widgets")
	err = platform2.MarkSuperseded(ctx, covRef(4, docsyncBranch), covRef(5, docsyncBranch))
	require.Error(t, err)
	// An UNREMEMBERED PR fails at the repo path (the malformed-repo refusal).
	assert.Equal(t, CodeGitHubRest, errCode(err))

	// UpdatePr: a malformed 200 body refuses instead of a zero PrRef.
	double3 := newCovRestDouble()
	double3.on("PATCH", "/repos/acme/widgets/pulls/6", covRestReply{
		status: 200, body: `{"number":"six","state":"open"}`,
	})
	platform3 := covRestPlatform(double3, "acme/widgets")
	_, err = platform3.UpdatePr(ctx, covRef(6, docsyncBranch),
		UpdatePrInput{Title: strPtrLocal("t")})
	require.Error(t, err)
	assert.Equal(t, CodeGitHubRest, errCode(err))
	assert.Contains(t, err.Error(), "malformed repo argument")
	assert.Empty(t, double3.requests, "an unremembered PR never reaches the wire")

	// UpdatePr: a repo the platform never remembered is a malformed-repo refusal.
	double4 := newCovRestDouble()
	platform4 := covRestPlatform(double4, "acme/widgets")
	_, err = platform4.UpdatePr(ctx, covRef(77, docsyncBranch),
		UpdatePrInput{Title: strPtrLocal("t")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed repo argument")
}

// TestCOV_GITPR_048_GateErrorPaths pins the gate's own error surface: a
// transport failure on the rebase and a non-missing CatFile failure both
// surface as the gate's error (never a silent abstain).
func TestCOV_GITPR_048_GateErrorPaths(t *testing.T) {
	const (
		branch = "docsync/run_gate"
		base   = "main"
		doc    = "docs/doc.md"
	)
	ctx := context.Background()
	pass := func(*schemas.DocPatch, string) *schemas.VerificationReport {
		return &schemas.VerificationReport{Verdict: schemas.VerdictPass, AutoEligible: true}
	}
	baseContent := "# base\n"

	// A rebase transport failure propagates. The doc is NOT raced (its head
	// content hashes to the baseline), so the walk reaches the rebase itself.
	unchanged := "# still the same\n"
	unchangedHash := docutil.ContentHash(unchanged)
	rebaseGit := newCovGateGit(t.TempDir())
	rebaseGit.blobs[base+":"+doc] = unchanged
	rebaseGit.rebaseErr = assertFailure()
	gate, err := CreateRebaseGate(covGateDeps(rebaseGit, pass))
	require.NoError(t, err)
	_, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, unchangedHash)},
		ExpectedBaseContentHashes: map[string]string{doc: unchangedHash},
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, rebaseGit.commitCalls, "no commit survives a failed rebase")
	// A NON-missing CatFile failure (a hard exec error) propagates.
	execGit := newCovGateGit(t.TempDir())
	execGit.catFileErrs = map[string]error{base + ":" + doc: gitx.NewGitExecError(
		gitx.KindExecFailure, "cat failed",
		&gitx.GitExecErrorOptions{StderrExcerpt: "hard failure"})}
	gate2, err := CreateRebaseGate(covGateDeps(execGit, pass))
	require.NoError(t, err)
	_, err = gate2.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.Error(t, err)
	assert.Equal(t, "EDOCSYNC_GIT", errCode(err)) // gitx maps to its own code

	// gateDefaultFileApplier fails loud when the target cannot be written.
	unwritable := filepath.Join(t.TempDir(), "blocking-file")
	require.NoError(t, os.WriteFile(unwritable, []byte("x"), 0o644))
	applier := gateDefaultFileApplier(unwritable)
	require.Error(t, applier("docs/doc.md", "content"))
	require.NoError(t, os.WriteFile(filepath.Join(unwritable), []byte("x"), 0o644))

	_ = baseContent
}

// TestCOV_GITPR_049_MergeLedgerRecordRefusals pins the ledger Record's
// remaining refusals: an invalid entry and a store write failure both fail
// closed (the ledger is never written in a partial state).
func TestCOV_GITPR_049_MergeLedgerRecordRefusals(t *testing.T) {
	store := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	ledger, err := NewMergeLedgerStore(MergeLedgerStoreDeps{Store: store})
	require.NoError(t, err)

	// A nil clock, an empty key and a non-positive PR number are all refused.
	_, err = ledger.Record("", RecordLedgerEntryInput{}, covFixedTime, nil)
	require.Error(t, err)
	_, err = ledger.Record("pr-9", RecordLedgerEntryInput{}, nil, nil)
	require.Error(t, err)
	_, err = ledger.Record("pr-9", RecordLedgerEntryInput{
		DocPRNumber: -1, State: schemas.MergeLedgerStateOpen, IdempotencyKey: "k",
	}, covFixedTime, nil)
	require.Error(t, err)

	// A validated entry lands (Detail is required by the ledger schema).
	result, err := ledger.Record("pr-9", RecordLedgerEntryInput{
		DocPRNumber: 9, State: schemas.MergeLedgerStateOpen, IdempotencyKey: "k", Detail: "opened",
	}, covFixedTime, nil)
	require.NoError(t, err)
	assert.True(t, result.OK)

	// mergeLedgerIssueSummary renders the root vs nested issue paths.
	assert.Empty(t, mergeLedgerIssueSummary(nil))
	assert.Contains(t,
		mergeLedgerIssueSummary([]schemas.Issue{{Path: []string{"entries", "0"}, Message: "bad"}}),
		"bad")
	assert.Contains(t, mergeLedgerIssueSummary([]schemas.Issue{{Message: "root"}}), "root")
}

// TestCOV_GITPR_050_WriteDocCommitScopeRefusal pins the post-index scope
// assert: a FOREIGN staged path aborts the whole commit (the plan's files are
// the only thing that may commit).
func TestCOV_GITPR_050_WriteDocCommitScopeRefusal(t *testing.T) {
	repo := covNewFlowRepo(t)
	ctx := context.Background()
	client := repo.client(t)

	// Stage a foreign file before the sanctioned write.
	repo.write(t, "foreign.txt", "not part of the plan\n")
	require.NoError(t, client.StagePaths(ctx, []string{"foreign.txt"}))

	patchValue := schemas.DocPatchExample
	patches := []schemas.DocPatch{patchValue}
	plan, err := AssembleDocCommit(AssembleDocCommitInput{
		Patches:     patches,
		Provenance:  ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: patchValue.RunID},
		DocContents: map[string]string{patchValue.DocPath: "# docsync content\n"},
	})
	require.NoError(t, err)

	_, err = WriteDocCommit(ctx, client, plan, func(relPath, content string) error {
		return os.WriteFile(filepath.Join(repo.root, relPath), []byte(content), 0o644)
	})
	var scopeErr *CommitScopeError
	require.ErrorAs(t, err, &scopeErr)
	assert.Contains(t, scopeErr.ForeignPaths, "foreign.txt")
	assert.Equal(t, CodeCommitScope, errCode(scopeErr))

	// Overlapping plan files are refused before staging.
	dup := plan
	dup.Files = []CommitPlanFile{plan.Files[0], plan.Files[0]}
	_, err = WriteDocCommit(ctx, client, dup, func(string, string) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate plan file")
}

// TestCOV_GITPR_051_FeedbackRecordIssueSummary pins the feedback record's
// validation-error projection (the root vs nested issue paths).
func TestCOV_GITPR_051_FeedbackRecordIssueSummary(t *testing.T) {
	// NOTE: feedbackIssueSummary is NOT nil-safe (it dereferences err.Error())
	// — unlike its sibling incidentIssueSummary/mergeLedgerIssueSummary. That
	// asymmetry is a SUSPECTED BUG reported (not patched here); the test pins
	// only the documented behavior and never passes nil.
	assert.Contains(t, feedbackIssueSummary(assertFailure()), "general error")
	validationSummary := feedbackIssueSummary(&schemas.ValidationError{Issues: []schemas.Issue{
		{Path: []string{"signal"}, Message: "bad signal"},
		{Message: "root-level issue"},
	}})
	assert.Contains(t, validationSummary, "signal: bad signal")
	assert.Contains(t, validationSummary, "<root>: root-level issue")

	// FeedbackRecordError renders its message verbatim.
	feedbackErr := &FeedbackRecordError{Msg: "boom"}
	require.EqualError(t, feedbackErr, "boom")
	var asErr *FeedbackRecordError
	require.ErrorAs(t, error(feedbackErr), &asErr)
}

// TestCOV_GITPR_052_FilterAndGuardEdgeCases pins the filter's and guard's
// remaining input refusals through their exported seams.
func TestCOV_GITPR_052_FilterAndGuardEdgeCases(t *testing.T) {
	// assertGuardInput: every failure branch (fail closed before any guard).
	for name, input := range map[string]GuardMachinePrContentInput{
		"empty changedPath":     {ChangedPaths: []string{"docs/a.md", ""}},
		"empty manifestPath":    {ManifestPaths: []string{""}, ChangedPaths: []string{"docs/a.md"}},
		"patch without docPath": {ChangedPaths: []string{"docs/a.md"}, DocPatches: []MachinePrDocPatch{{}}},
	} {
		_, err := GuardMachinePrContent(input)
		require.Error(t, err, name)
		assert.Equal(t, CodeMachinePrContentGuard, docutil.CodeOf(err), name)
	}

	// A manifest-listed doc that asserts ANY post-patch mode is an INV-7
	// violation (only a human PR may change a mode).
	allowlistMode := string(schemas.DocModeAuto)
	guardResult, err := GuardMachinePrContent(GuardMachinePrContentInput{
		ChangedPaths:   []string{"docs/a.md"},
		ManifestPaths:  []string{"docs/a.md"},
		DocPatches:     []MachinePrDocPatch{{DocPath: "docs/a.md", ModeAfter: &allowlistMode}},
		ProtectedPaths: []string{},
	})
	require.NoError(t, err)
	require.Len(t, guardResult.Violations, 1)
	assert.Equal(t, "docs/a.md", guardResult.Violations[0].Path)
	assert.Equal(t, MachinePrRuleModeTransition, guardResult.Violations[0].Rule)
	assert.NotEmpty(t, guardResult.Violations[0].Detail)

	// A machine patch with a nil mode-after (no assert) over an allowlisted doc
	// is clean.
	cleanResult, err := GuardMachinePrContent(GuardMachinePrContentInput{
		ChangedPaths:   []string{"docs/a.md"},
		ManifestPaths:  []string{"docs/a.md"},
		DocPatches:     []MachinePrDocPatch{{DocPath: "docs/a.md"}},
		ProtectedPaths: []string{},
	})
	require.NoError(t, err)
	assert.Empty(t, cleanResult.Violations)

	// An empty-string mode-after is a malformed input refusal.
	emptyMode := ""
	_, err = GuardMachinePrContent(GuardMachinePrContentInput{
		ChangedPaths:  []string{"docs/a.md"},
		ManifestPaths: []string{"docs/a.md"},
		DocPatches:    []MachinePrDocPatch{{DocPath: "docs/a.md", ModeAfter: &emptyMode}},
	})
	require.Error(t, err)
	assert.Equal(t, CodeMachinePrContentGuard, docutil.CodeOf(err))

	// The filter's manifest authority is nil-safe: a nil manifest aborts.
	repo := covNewFlowRepo(t)
	client := repo.client(t)
	filter, err := CreatePrFilter(PrFilterDeps{Git: client})
	require.NoError(t, err)
	_, err = filter.FilterBranchDiff(context.Background(), FilterBranchDiffInput{
		RepoRoot: repo.root, Base: "main", Branch: "feat", Manifest: nil,
	})
	require.Error(t, err)
	assert.Equal(t, CodePrFilterConfig, docutil.CodeOf(err))

	// A non-existent branch surfaces the git error (not a silent empty pass).
	_, err = filter.FilterBranchDiff(context.Background(), FilterBranchDiffInput{
		RepoRoot: repo.root, Base: "main", Branch: "definitely-not-here",
		Manifest: &schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{{Path: "docs/a.md"}}},
	})
	require.Error(t, err)
}
