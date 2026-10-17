package gitx_test

// Coverage-focused tests for internal/gitx targeting the branches the
// existing suites do not reach: invalid-arg guards in NewGitClient and the
// client methods, scripted-executor failure paths per method, empty-diff
// and path-restricted variants, the write-path plumbing, and the exec
// layer's error surface.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCOV_GITX_NewGitClientInvalidRepoRoot(t *testing.T) {
	_, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: "relative/path"})
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.Equal(t, gitx.KindInvalidArg, gitErr.Kind)
	assert.Contains(t, err.Error(), "repoRoot must be an absolute path")
}

func TestCOV_GITX_NewGitClientNilExecutorUsesSystem(t *testing.T) {
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: os.TempDir()})
	require.NoError(t, err)
	assert.NotEmpty(t, client.Root())
	assert.NotNil(t, client)
}

func TestCOV_GITX_TreeShaInvalidRev(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(128), Stderr: []byte("fatal: bad revision\n")}),
	})
	_, err := client.TreeSha(context.Background(), "bogus")
	require.Error(t, err)
	requireGitExecErrorKind(t, err, gitx.KindUnknownRef)
}

func TestCOV_GITX_TreeShaNonShaOutput(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("not-a-sha\n")}),
	})
	_, err := client.TreeSha(context.Background(), "HEAD")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-sha value")
}

func TestCOV_GITX_MergeBaseUnrelatedHistoriesEmpty(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(1), Stdout: []byte{}, Stderr: []byte{}}),
	})
	sha, err := client.MergeBase(context.Background(), "main", "side")
	require.NoError(t, err)
	assert.Equal(t, "", sha)
}

func TestCOV_GITX_MergeBaseNonZeroNonOne(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(128), Stderr: []byte("fatal: bad\n")}),
	})
	_, err := client.MergeBase(context.Background(), "main", "side")
	require.Error(t, err)
	requireGitExecErrorKind(t, err, gitx.KindExecFailure)
}

func TestCOV_GITX_CatFileValidationAndMissingRev(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "x\n")
	repo.commitAll(t, "base")
	client := repo.client(t)
	ctx := context.Background()

	_, err := client.CatFile(ctx, "-oProxyCommand=x", "docs/a.md")
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)

	_, err = client.CatFile(ctx, "HEAD", "docs/a\x00b.md")
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)

	_, err = client.CatFile(ctx, "HEAD", "docs/missing.md")
	requireGitExecErrorKind(t, err, gitx.KindMissingRev)
}

func TestCOV_GITX_LsTreeWithPathPrefix(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "one\n")
	repo.write(t, "src/b.md", "two\n")
	repo.commitAll(t, "base")
	client := repo.client(t)
	ctx := context.Background()

	entries, err := client.LsTree(ctx, "HEAD", strptr("docs"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "docs/a.md", entries[0].Path)
}

func TestCOV_GITX_LsTreeUnknownRef(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(128), Stderr: []byte("fatal: unknown revision\n")}),
	})
	_, err := client.LsTree(context.Background(), "bogus", nil)
	requireGitExecErrorKind(t, err, gitx.KindUnknownRef)
}

func TestCOV_GITX_DiffFilesEmptyAndPathRestricted(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "one\n")
	base := repo.commitAll(t, "base")
	head := base
	client := repo.client(t)
	ctx := context.Background()

	records, err := client.DiffFiles(ctx, base, head)
	require.NoError(t, err)
	assert.Empty(t, records)

	id, err := client.PatchId(ctx, base, head, nil)
	require.NoError(t, err)
	assert.Equal(t, "", id)
}

func TestCOV_GITX_PatchIdPathRestricted(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "one\n")
	repo.write(t, "src/b.md", "one\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "docs/a.md", "two\n")
	head := repo.commitAll(t, "head")

	client := repo.client(t)
	ctx := context.Background()
	id, err := client.PatchId(ctx, base, head, strptr("docs/a.md"))
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{40}$`, id)
}

func TestCOV_GITX_ParentShasRootAndMultiple(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "one\n")
	sha := repo.commitAll(t, "root")
	client := repo.client(t)
	ctx := context.Background()

	parents, err := client.ParentShas(ctx, sha)
	require.NoError(t, err)
	assert.Empty(t, parents)

	repo.write(t, "docs/a.md", "two\n")
	second := repo.commitAll(t, "second")
	parents, err = client.ParentShas(ctx, second)
	require.NoError(t, err)
	assert.Equal(t, []string{sha}, parents)
}

func TestCOV_GITX_LogCommitsSingleAndGuard(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "one\n")
	sha := repo.commitAll(t, "root")
	client := repo.client(t)
	ctx := context.Background()

	commits, err := client.LogCommits(ctx, sha, 1)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	assert.Equal(t, sha, commits[0].Sha)
	assert.Empty(t, commits[0].Parents)

	_, err = client.LogCommits(ctx, sha, 0)
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)
}

func TestCOV_GITX_WritePathCreateBranchAndCommit(t *testing.T) {
	repo := newRepo(t)
	setIdentityCOV(t, repo)
	repo.write(t, "docs/base.md", "base\n")
	base := repo.commitAll(t, "base")
	client := repo.client(t)
	ctx := context.Background()

	require.NoError(t, client.CreateBranch(ctx, "docsync/at-sha", strptr(base)))
	assert.Equal(t, base, strings.TrimSpace(repo.gitOut(t, "rev-parse", "docsync/at-sha")))

	repo.write(t, "docs/new.md", "new\n")
	require.NoError(t, client.StagePaths(ctx, []string{"docs/new.md"}))
	cached, err := client.CachedFilePaths(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"docs/new.md"}, cached)

	newHead, err := client.Commit(ctx, "docs: add new.md")
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{40}$`, newHead)
	resolved, err := client.ResolveRev(ctx, newHead)
	require.NoError(t, err)
	assert.Equal(t, newHead, resolved)
}

func TestCOV_GITX_WritePathStatusAndRebaseClean(t *testing.T) {
	repo := newRepo(t)
	setIdentityCOV(t, repo)
	repo.write(t, "docs/c.md", "base\n")
	repo.commitAll(t, "base")
	client := repo.client(t)
	ctx := context.Background()

	require.NoError(t, client.CreateBranch(ctx, "feature", nil))
	repo.write(t, "docs/feature.md", "feature\n")
	repo.commitAll(t, "feature")
	repo.git(t, "checkout", "-q", "main")
	repo.write(t, "docs/main2.md", "main\n")
	repo.commitAll(t, "main advance")

	status, err := client.StatusPorcelain(ctx)
	require.NoError(t, err)
	assert.Empty(t, status)

	result, err := client.RebaseBranch(ctx, "feature", "main")
	require.NoError(t, err)
	assert.True(t, result.Ok)
	assert.False(t, result.Conflicted)
}

func TestCOV_GITX_PushBranchRemoteAndForce(t *testing.T) {
	repo := newRepo(t)
	setIdentityCOV(t, repo)
	repo.write(t, "docs/p.md", "p\n")
	first := repo.commitAll(t, "push base")
	repo.write(t, "docs/p.md", "p2\n")
	second := repo.commitAll(t, "push two")
	client := repo.client(t)
	ctx := context.Background()

	remoteDir := t.TempDir()
	runGitCOV(t, remoteDir, "init", "-q", "--bare", "-b", "main")
	repo.git(t, "remote", "add", "origin", remoteDir)

	require.NoError(t, client.CreateBranch(ctx, "docsync/other", strptr(second)))
	require.NoError(t, client.PushBranch(ctx, "docsync/other", "", false))

	runGitCOV(t, remoteDir, "update-ref", "refs/heads/docsync/pushed", second)
	require.NoError(t, client.CreateBranch(ctx, "docsync/pushed", strptr(first)))
	err := client.PushBranch(ctx, "docsync/pushed", "", false)
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.Equal(t, gitx.KindExecFailure, gitErr.Kind)

	require.NoError(t, client.PushBranch(ctx, "docsync/pushed", "", true))
}

func TestCOV_GITX_StagePathsEmptyAndCommitInvalid(t *testing.T) {
	repo := newRepo(t)
	setIdentityCOV(t, repo)
	client := repo.client(t)
	ctx := context.Background()

	err := client.StagePaths(ctx, nil)
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)
	assert.Contains(t, err.Error(), "git add requires at least one path")

	_, err = client.Commit(ctx, "")
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)

	_, err = client.Commit(ctx, "docs: \x00")
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)

	err = client.PushBranch(ctx, "--exec=evil", "", false)
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)
}

func TestCOV_GITX_ExecErrorSurfaceAndUnwrap(t *testing.T) {
	err := gitx.ClassifyGitFailure("diff", capturedResult(&gitx.GitRunResult{
		ExitCode: intPtr(128),
		Stderr:   []byte("fatal: bad revision\n"),
	}))
	assert.Equal(t, docutil.CodeGit, err.Code())
	assert.Equal(t, gitx.EDOCSYNCGitExec, docutil.CodeOf(err))

	extracted, ok := gitx.AsGitExecError(err)
	require.True(t, ok)
	assert.Equal(t, gitx.KindUnknownRef, extracted.Kind)

	_, ok = gitx.AsGitExecError(errors.New("plain"))
	assert.False(t, ok)

	unwrapped := err.Unwrap()
	require.Len(t, unwrapped, 2)
	assert.Contains(t, unwrapped[1].Error(), "git run failed")
}

func TestCOV_GITX_GitRunResultTimedOutAndFailed(t *testing.T) {
	result := gitx.GitRunResult{
		ExitCode: nil,
		Signal:   "SIGTERM",
		TimedOut: true,
		Stdout:   []byte{},
		Stderr:   []byte{},
	}
	assert.True(t, result.Failed())
	assert.True(t, result.TimedOut)
	assert.Nil(t, result.ExitCode)

	okResult := gitx.GitRunResult{ExitCode: intPtr(0)}
	assert.False(t, okResult.Failed())
	assert.Equal(t, "0", gitxFormatExitCode(okResult.ExitCode))
}

func gitxFormatExitCode(code *int) string {
	if code == nil {
		return "null"
	}
	return itoaCOV(*code)
}

func itoaCOV(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestCOV_GITX_StderrExcerptAndValidateHelpers(t *testing.T) {
	assert.Equal(t, "", gitx.StderrExcerpt(nil, 400))
	assert.Equal(t, "", gitx.StderrExcerpt([]byte{}, 400))
	excerpt := gitx.StderrExcerpt([]byte("fatal: bad revision\nsome more"), 10)
	assert.Equal(t, "fatal: bad…", excerpt)

	_, err := gitx.ValidateRepoRoot("relative")
	require.Error(t, err)

	got, err := gitx.ValidateRefArg("HEAD~1", "ref")
	require.NoError(t, err)
	assert.Equal(t, "HEAD~1", got)

	for _, bad := range []string{"-x", "a\x00b", "a\nb", "a\rb", ""} {
		_, err := gitx.ValidateRefArg(bad, "ref")
		require.Error(t, err)
	}
}

func setIdentityCOV(t *testing.T, repo *testGitRepo) {
	t.Helper()
	repo.git(t, "config", "user.name", "docsync-test")
	repo.git(t, "config", "user.email", "docsync-test@invalid")
}

func runGitCOV(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runGit(t, dir, args...)
}
