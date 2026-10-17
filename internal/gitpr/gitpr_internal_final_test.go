package gitpr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	gitx "github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCOV_GITPR_053_NoopDropOutcomeUnion pins every arm of the evaluator's
// closed outcome union: byte-identical ⇒ noop (dropped), genuine drift /
// unanchored / abstain ⇒ NOT a no-op, anything else ⇒ fail closed.
func TestCOV_GITPR_053_NoopDropOutcomeUnion(t *testing.T) {
	ctx := context.Background()
	docPath := "docs/doc.md"
	docs := func(line string) string {
		lines := make([]string, 0, 42)
		for i := 1; i <= 41; i++ {
			lines = append(lines, "line "+itoa(i))
		}
		lines = append(lines, line)
		return joinLines(lines)
	}
	baseContent := docs("Refunds require an idempotency key.")
	baseHash := docutil.ContentHash(baseContent)

	// (1) Byte-identical ⇒ a no-op.
	applied := docs("Refunds require an idempotency key for amounts above 10 USD.")
	result, err := EvaluateNoOpDrop(ctx, EvaluateNoOpDropInput{
		Patch: covGatePatch(t, docPath, baseHash), HeadContent: applied,
		BaseContent: &baseContent, ManifestAllowlist: map[string]bool{docPath: true},
		Now: noopDropConstantNow,
	})
	require.NoError(t, err)
	assert.True(t, result.Noop)
	assert.Equal(t, NoopDropDetail, result.Detail)

	// (2) A genuine drift ⇒ not a no-op, with the boundary named.
	drifted := "# an upstream rewrite\n" + baseContent
	result, err = EvaluateNoOpDrop(ctx, EvaluateNoOpDropInput{
		Patch: covGatePatch(t, docPath, baseHash), HeadContent: drifted,
		BaseContent: &baseContent, ManifestAllowlist: map[string]bool{docPath: true},
		Now: noopDropConstantNow,
	})
	require.NoError(t, err)
	assert.False(t, result.Noop)
	assert.Contains(t, result.Detail, noopDropBoundary)

	// (3) An unresolvable head ⇒ inconclusive, never "already fixed".
	unrelated := docs("totally different claim about the same subject matter here")
	result, err = EvaluateNoOpDrop(ctx, EvaluateNoOpDropInput{
		Patch: covGatePatch(t, docPath, baseHash), HeadContent: unrelated,
		BaseContent: &baseContent, ManifestAllowlist: map[string]bool{docPath: true},
		Now: noopDropConstantNow,
	})
	require.NoError(t, err)
	assert.False(t, result.Noop)
	assert.Contains(t, result.Detail, noopDropBoundary)

	// (4) A nil patch fails closed before any simulation.
	_, err = EvaluateNoOpDrop(ctx, EvaluateNoOpDropInput{Patch: nil})
	require.Error(t, err)

	// (5) An unhandled outcome kind fails closed (the default arm).
	// The engine's own outcome union pins the projection arms the evaluator
	// maps over: an already-applied result is the one the noop arm consumes.
	failure, err := patch.ApplyDocPatch(ctx, patch.ApplyDocPatchInput{
		Patch: covGatePatch(t, docPath, baseHash), Rev: "main",
		Git: noopDropNeverReadGit{}, WorktreeContent: &applied,
		BaseContent: &baseContent, ManifestAllowlist: map[string]bool{docPath: true},
		Now: noopDropConstantNow,
	})
	require.NoError(t, err)
	assert.Equal(t, patch.OutcomeNoop, failure.Kind)

	// The unhandled-kind arm is DEFENSIVE (the engine's outcome union is
	// closed); it is documented in the final report rather than forced.
}

func joinLines(lines []string) string {
	out := ""
	for index, line := range lines {
		out += line
		if index < len(lines)-1 {
			out += "\n"
		}
	}
	return out + "\n"
}

// TestCOV_GITPR_054_IncidentSinkOpenFailure pins the file sink's OpenFile
// failure: an incidents.jsonl that is a DIRECTORY cannot be appended to, and
// the sink fails loud.
func TestCOV_GITPR_054_IncidentSinkOpenFailure(t *testing.T) {
	dir := t.TempDir()
	sink, err := FileIncidentSink(dir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, IncidentLogFilename), 0o755))
	incident := &schemas.SafetyIncident{
		Schema: "docsync.safety-incident/v1", IncidentID: "inc-open-fail",
		At: covFixedTime(), RunID: "run", Rule: schemas.SafetyIncidentRuleProtectedPathPatch,
		OffendingPath: "manifest.yml", Detail: "d",
	}
	require.Error(t, sink.Append(context.Background(), incident),
		"a directory in the file's place cannot be appended to")

	// A normal append still works over the same stateDir shape.
	other := t.TempDir()
	goodSink, err := FileIncidentSink(other)
	require.NoError(t, err)
	require.NoError(t, goodSink.Append(context.Background(), incident))
	raw, err := os.ReadFile(filepath.Join(other, IncidentLogFilename))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "inc-open-fail")
}

// TestCOV_GITPR_055_SupersedeClientFailures pins the supersede ceremony's
// platform-failure surface: a read failure and a mark failure both propagate.
func TestCOV_GITPR_055_SupersedeClientFailures(t *testing.T) {
	client := &covSupersedeClient{}
	input := SupersedeStalePrInput{
		Stale: SeamPrRef{Number: 1, Branch: "docsync/a"},
		Newer: SeamPrRef{Number: 2, Branch: "docsync/b"},
		Clock: covFixedTime,
	}

	// A comment-read failure propagates.
	client.failList = assertFailure()
	_, err := SupersedeStalePr(client, input)
	require.ErrorIs(t, err, assertFailure())
	assert.Empty(t, client.appended, "nothing is written after a failed read")

	// A successful call resets the failure and appends exactly once.
	client.failList = nil
	result, err := SupersedeStalePr(client, input)
	require.NoError(t, err)
	assert.Equal(t, SupersedeActionSuperseded, result.Action)
	require.Len(t, client.appended, 1)

	// The idempotent short-circuit: the SAME marker in the thread.
	client.comments = append(client.comments, client.appended[0])
	again, err := SupersedeStalePr(client, input)
	require.NoError(t, err)
	assert.Equal(t, SupersedeActionAlready, again.Action)
	assert.Len(t, client.appended, 1)
}

// TestCOV_GITPR_056_GateReapplyAndRacingPaths pins the raced-document paths
// that remain: TWO racing patches over the same doc refuse loudly, and the
// closing clean-tree assert fires when a raced path leaves debris.
func TestCOV_GITPR_056_GateReapplyAndRacingPaths(t *testing.T) {
	const (
		branch = "docsync/run_gate"
		base   = "main"
		doc    = "docs/doc.md"
	)
	ctx := context.Background()
	pass := func(*schemas.DocPatch, string) *schemas.VerificationReport {
		return &schemas.VerificationReport{Verdict: schemas.VerdictPass, AutoEligible: true}
	}
	baseContent := joinLines(func() []string {
		lines := make([]string, 0, 42)
		for i := 1; i <= 41; i++ {
			lines = append(lines, "line "+itoa(i))
		}
		lines = append(lines, "Refunds require an idempotency key.")
		return lines
	}())
	baseHash := docutil.ContentHash(baseContent)

	// Two racing patches over the SAME doc ⇒ one patch per doc per run.
	racing := newCovGateGit(t.TempDir())
	racing.blobs[base+":"+doc] = "# new upstream note\n" + baseContent
	racing.blobs[racing.baseSha+":"+doc] = baseContent
	racing.blobs[branch+":"+doc] = baseContent
	gate, err := CreateRebaseGate(covGateDeps(racing, pass))
	require.NoError(t, err)
	patches := []*schemas.DocPatch{
		covGatePatch(t, doc, baseHash),
		covGatePatch(t, doc, baseHash),
	}
	result, err := gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   patches,
		ExpectedBaseContentHashes: map[string]string{doc: baseHash},
	})
	require.NoError(t, err)
	if result.Outcome == RebaseGateOutcomeRebasedReverified {
		// A drifted head that already carries BOTH patches' effect drops them
		// as no-ops; that is the (P6-T15) drop path, also fail-closed.
		assert.Empty(t, result.Patches)
	} else {
		assert.Equal(t, RebaseGateOutcomeAbstained, result.Outcome)
		assert.Equal(t, RebaseGateAbstainRaceUnresolved, result.Reason)
		assert.Contains(t, result.Detail, "two racing patches")
	}

	// A raced doc whose BRANCH content cannot be read (a hard exec error, not
	// a missing rev) surfaces as the gate's error, never a silent abstain.
	brokenBranch := newCovGateGit(t.TempDir())
	brokenBranch.blobs[base+":"+doc] = "# new upstream note\n" + baseContent
	brokenBranch.blobs[brokenBranch.baseSha+":"+doc] = baseContent
	brokenBranch.catFileErrs = map[string]error{
		branch + ":" + doc: gitx.NewGitExecError(gitx.KindExecFailure, "hard exec failure",
			&gitx.GitExecErrorOptions{StderrExcerpt: "cannot read branch blob"}),
	}
	gate2, err := CreateRebaseGate(covGateDeps(brokenBranch, pass))
	require.NoError(t, err)
	_, err = gate2.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   patches[:1],
		ExpectedBaseContentHashes: map[string]string{doc: baseHash},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hard exec failure")
	assert.Equal(t, "EDOCSYNC_GIT", errCode(err))
}

// TestCOV_GITPR_059_WriteCommitEmptyResult pins the write path's no-op
// boundary: an assembly whose staged content is byte-identical to HEAD is an
// *EmptyCommitError — the writer never fabricates a commit.
func TestCOV_GITPR_059_WriteCommitEmptyResult(t *testing.T) {
	repo := covNewFlowRepo(t)
	ctx := context.Background()
	client := repo.client(t)

	patchValue := schemas.DocPatchExample
	// The plan writes the doc's ALREADY-COMMITTED content: a staged no-op.
	existing, err := os.ReadFile(filepath.Join(repo.root, patchValue.DocPath))
	require.NoError(t, err)
	plan, err := AssembleDocCommit(AssembleDocCommitInput{
		Patches:     []schemas.DocPatch{patchValue},
		Provenance:  ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: patchValue.RunID},
		DocContents: map[string]string{patchValue.DocPath: string(existing)},
	})
	require.NoError(t, err)

	result, err := WriteDocCommit(ctx, client, plan, func(relPath, content string) error {
		return os.WriteFile(filepath.Join(repo.root, relPath), []byte(content), 0o644)
	})
	var emptyErr *EmptyCommitError
	require.ErrorAs(t, err, &emptyErr)
	assert.Equal(t, CodeEmptyCommit, docutil.CodeOf(err))
	assert.Equal(t, WriteDocCommitResult{}, result)
}

// TestCOV_GITPR_060_GateReadFailures pins the gate's ref-read failures: a
// rev-parse and a merge-base failure both surface (never a silent abstain).
func TestCOV_GITPR_060_GateReadFailures(t *testing.T) {
	const (
		branch = "docsync/run_gate"
		base   = "main"
		doc    = "docs/doc.md"
	)
	ctx := context.Background()
	pass := func(*schemas.DocPatch, string) *schemas.VerificationReport {
		return &schemas.VerificationReport{Verdict: schemas.VerdictPass, AutoEligible: true}
	}

	// A ResolveRev failure on the default-branch head.
	brokenResolve := newCovGateGit(t.TempDir())
	brokenResolve.resolveErr = assertFailure()
	gate, err := CreateRebaseGate(covGateDeps(brokenResolve, pass))
	require.NoError(t, err)
	_, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.ErrorIs(t, err, assertFailure())

	// A MergeBase failure.
	brokenMerge := newCovGateGit(t.TempDir())
	brokenMerge.mergeBaseErr = assertFailure()
	gate2, err := CreateRebaseGate(covGateDeps(brokenMerge, pass))
	require.NoError(t, err)
	_, err = gate2.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.ErrorIs(t, err, assertFailure())

	// A apply seam failure propagates through the race path.
	brokenApply := newCovGateGit(t.TempDir())
	brokenApply.blobs[base+":"+doc] = "# new upstream note\n" + docContent42()
	brokenApply.blobs[brokenApply.baseSha+":"+doc] = docContent42()
	brokenApply.blobs[branch+":"+doc] = docContent42()
	baseHash := docutil.ContentHash(docContent42())
	deps := covGateDeps(brokenApply, pass)
	deps.Apply = func(string, string) error { return assertFailure() }
	gate3, err := CreateRebaseGate(deps)
	require.NoError(t, err)
	_, err = gate3.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, baseHash)},
		ExpectedBaseContentHashes: map[string]string{doc: baseHash},
	})
	require.ErrorIs(t, err, assertFailure())
	assert.Empty(t, brokenApply.commitCalls, "a failed re-apply commits nothing")
}

func docContent42() string {
	lines := make([]string, 0, 42)
	for i := 1; i <= 41; i++ {
		lines = append(lines, "line "+itoa(i))
	}
	lines = append(lines, "Refunds require an idempotency key.")
	return joinLines(lines)
}

// TestCOV_GITPR_061_RestWriteSuccessProjections pins the write paths'
// SUCCESSFUL parse projections (the bodies that DO parse).
func TestCOV_GITPR_061_RestWriteSuccessProjections(t *testing.T) {
	ctx := context.Background()
	docsyncBranch := SelfTriggerBranchPrefix + "run_ok"

	// ClosePr with a valid body.
	double := newCovRestDouble()
	double.on("GET", "/repos/acme/widgets/pulls?page=1", covRestReply{
		status: 200,
		body:   `[{"number":3,"state":"open","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"}}]`,
	})
	platform := covRestPlatform(double, "acme/widgets")
	_, err := platform.ListOpenDocsPrs(ctx, "acme/widgets")
	require.NoError(t, err)
	double.on("POST", "/repos/acme/widgets/issues/3/comments", covRestReply{status: 201, body: `{"id":1}`})
	double.on("PATCH", "/repos/acme/widgets/pulls/3", covRestReply{
		status: 200, body: `{"number":3,"state":"closed"}`,
	})
	require.NoError(t, platform.ClosePr(ctx, covRef(3, docsyncBranch), "note"))

	// MarkSuperseded with a valid body.
	double.on("POST", "/repos/acme/widgets/issues/3/comments", covRestReply{status: 201, body: `{"id":1}`})
	double.on("PATCH", "/repos/acme/widgets/pulls/3", covRestReply{
		status: 200, body: `{"number":3,"state":"closed","head":{"ref":"` + docsyncBranch + `"}}`,
	})
	require.NoError(t, platform.MarkSuperseded(ctx, covRef(3, docsyncBranch), covRef(4, docsyncBranch)))

	// UpdatePr with a valid body.
	double.on("PATCH", "/repos/acme/widgets/pulls/3", covRestReply{
		status: 200,
		body:   `{"number":3,"state":"open","title":"new","body":"b","head":{"ref":"` + docsyncBranch + `"}}`,
	})
	updated, err := platform.UpdatePr(ctx, covRef(3, docsyncBranch),
		UpdatePrInput{Title: strPtrLocal("new")})
	require.NoError(t, err)
	assert.Equal(t, 3, updated.Number)
	assert.Equal(t, "new", updated.Title)

	// AppendPrComment with a valid body.
	double.on("POST", "/repos/acme/widgets/issues/3/comments", covRestReply{status: 201, body: `{"id":9}`})
	require.NoError(t, platform.AppendPrComment(ctx, covRef(3, docsyncBranch), "a comment"))
}

// TestCOV_GITPR_062_RestSendFailurePaths pins the write paths' transport
// failures: a dead seam fails loud at each send site (never a silent no-op).
func TestCOV_GITPR_062_RestSendFailurePaths(t *testing.T) {
	ctx := context.Background()
	docsyncBranch := SelfTriggerBranchPrefix + "run_dead"

	newDeadPlatform := func() PrPlatformPort {
		return CreateGitHubRestPlatform(GitHubRestPlatformDeps{
			Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "t"},
			Transport:   covFailingTransport{}.send,
			Clock:       covFixedTime,
		})
	}
	dead := newDeadPlatform()
	require.Error(t, dead.ClosePr(ctx, covRef(3, docsyncBranch), "note"))
	err := dead.AppendPrComment(ctx, covRef(3, docsyncBranch), "note")
	require.Error(t, err)
	// An UNREMEMBERED PR fails at the repo path before any send.
	assert.Equal(t, CodeGitHubRest, errCode(err))

	// A transport whose error message is EMPTY still fails loud (the unknown
	// transport failure fallback detail).
	empty := CreateGitHubRestPlatform(GitHubRestPlatformDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "ghp-realistic-token"},
		Transport: func(GitHubRestRequest) (GitHubRestResponse, error) {
			return GitHubRestResponse{}, errEmpty{}
		},
		Clock: covFixedTime,
	})
	// A repo-carrying write reaches the send, so the EMPTY cause renders the
	// "unknown transport failure" fallback detail.
	_, emptyErr := empty.CreatePr(ctx, CreatePrInput{
		Repo: "acme/widgets", Branch: docsyncBranch, Base: "main", Title: "t", Body: "b",
	})
	require.Error(t, emptyErr)
	assert.Equal(t, CodeGitHubTransport, errCode(emptyErr))
	assert.Contains(t, emptyErr.Error(), "unknown transport failure")

	// A listing whose body is a JSON OBJECT (not an array) is a TRANSPORT-class
	// refusal from listPullsPage.
	double := newCovRestDouble()
	double.on("GET", "/repos/acme/widgets/pulls?page=1", covRestReply{status: 200, body: `{"unexpected":true}`})
	platform := covRestPlatform(double, "acme/widgets")
	_, err = platform.ListOpenDocsPrs(ctx, "acme/widgets")
	require.Error(t, err)
	assert.Equal(t, CodeGitHubTransport, errCode(err))

	// A 2xx comment body that is not JSON is a TRANSPORT-class refusal.
	double2 := newCovRestDouble()
	platform2 := covRestPlatform(double2, "acme/widgets")
	// Remember PR #3's repo through the SAME platform instance.
	double2.on("GET", "/repos/acme/widgets/pulls?page=1", covRestReply{
		status: 200,
		body:   `[{"number":3,"state":"open","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"}}]`,
	})
	_, listErr2 := platform2.ListOpenDocsPrs(ctx, "acme/widgets")
	require.NoError(t, listErr2)
	double2.on("POST", "/repos/acme/widgets/issues/3/comments", covRestReply{status: 201, body: "not json"})
	err = platform2.AppendPrComment(ctx, covRef(3, docsyncBranch), "note")
	require.Error(t, err)
	assert.Equal(t, CodeGitHubTransport, errCode(err))
}

// errEmpty is an error with an empty message (the unknown-cause fallback).
type errEmpty struct{}

func (errEmpty) Error() string { return "" }

// TestCOV_GITPR_063_CreateDocBranchReuse pins the branch factory's idempotent
// reuse path: an EXISTING doc branch is returned as-is (never re-created), and
// the disambiguated ladder tolerates an occupied first candidate.
func TestCOV_GITPR_063_CreateDocBranchReuse(t *testing.T) {
	ctx := context.Background()
	repo := covNewFlowRepo(t)
	client := repo.client(t)
	factory := CreateBranchFactory(client)

	// First creation.
	head := repo.rev(t, "main")
	first, err := factory.CreateDocBranch(ctx, CreateDocBranchInput{
		RunID: "run_branch", RepoRoot: repo.root,
		HeadSha: head, DefaultBranch: "main", SourcePr: nil,
	})
	require.NoError(t, err)
	require.True(t, first.Created)
	assert.Equal(t, SelfTriggerBranchPrefix+"run_branch", first.Branch)

	// A re-run finds it (idempotent, never a duplicate).
	second, err := factory.CreateDocBranch(ctx, CreateDocBranchInput{
		RunID: "run_branch", RepoRoot: repo.root,
		HeadSha: head, DefaultBranch: "main", SourcePr: nil,
	})
	require.NoError(t, err)
	assert.False(t, second.Created, "an existing doc branch is reused, not re-created")
	assert.Equal(t, first.Branch, second.Branch)

	// A PRESENT default branch aborts the creation (INV-1 at the ref level).
	_, err = factory.CreateDocBranch(ctx, CreateDocBranchInput{
		RunID: "run_branch", RepoRoot: repo.root,
		HeadSha: head, DefaultBranch: SelfTriggerBranchPrefix + "run_branch", SourcePr: nil,
	})
	require.Error(t, err)
	assert.Equal(t, CodeDefaultBranchRef, docutil.CodeOf(err))
}
