package gitx_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
GitClient tests (P1-T01, ARCH §4.1, D12/D14 groundwork). Real system git in
fresh t.TempDir() repos — no network, no shared state. The named tests cover
the V-P1-9 ladder groundwork (tree-hash + patch-id stability across
squash-like re-commits) and the V-P1-10 failure semantics (injected git failure
→ typed error, never an empty success).
*/

// identityArgs pins the commit identity per invocation (the fixture env points
// GIT_CONFIG_GLOBAL at /dev/null, so no user config exists).
var identityArgs = []string{"-c", "user.name=docsync-test", "-c", "user.email=docsync-test@invalid"}

// testGitRepo is a throwaway real repository (TS TestGitRepo).
type testGitRepo struct {
	root string
}

func newRepo(t *testing.T) *testGitRepo {
	t.Helper()
	repo := &testGitRepo{root: t.TempDir()}
	repo.git(t, "init", "-b", "main")
	// Platform port (DX-P11-3): repo-local line-ending policy — the machine
	// level system gitconfig carries core.autocrlf=true, which would rewrite
	// fresh fixture/checkout files to CRLF and surface them as ` M` in
	// worktree assertions. Pin LF in-repo before any file is written.
	repo.git(t, "config", "core.autocrlf", "false")
	repo.git(t, "config", "core.eol", "lf")
	return repo
}

func (r *testGitRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	return runGit(t, r.root, args...)
}

func (r *testGitRepo) gitOut(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	cmd.Env = fixtureEnv()
	out, err := cmd.Output()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return string(out)
}

func (r *testGitRepo) write(t *testing.T, relPath, content string) {
	t.Helper()
	target := filepath.Join(r.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func (r *testGitRepo) writeBinary(t *testing.T, relPath string, data []byte) {
	t.Helper()
	target := filepath.Join(r.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, data, 0o644))
}

func (r *testGitRepo) read(t *testing.T, relPath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, relPath))
	require.NoError(t, err)
	return string(data)
}

func (r *testGitRepo) remove(t *testing.T, relPath string) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(r.root, relPath)))
}

func (r *testGitRepo) rename(t *testing.T, fromRel, toRel string) {
	t.Helper()
	require.NoError(t, os.Rename(filepath.Join(r.root, fromRel), filepath.Join(r.root, toRel)))
}

func (r *testGitRepo) commitAll(t *testing.T, message string) string {
	t.Helper()
	r.git(t, append(append([]string(nil), identityArgs...), "add", "-A")...)
	r.git(t, append(append([]string(nil), identityArgs...), "commit", "-q", "-m", message)...)
	return strings.TrimSpace(r.gitOut(t, "rev-parse", "HEAD"))
}

func (r *testGitRepo) amendAll(t *testing.T, message string) string {
	t.Helper()
	r.git(t, append(append([]string(nil), identityArgs...), "commit", "-q", "--amend", "-m", message)...)
	return strings.TrimSpace(r.gitOut(t, "rev-parse", "HEAD"))
}

func (r *testGitRepo) client(t *testing.T) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: r.root})
	require.NoError(t, err)
	return client
}

// isGitExecErrorWithKind mirrors the TS predicate.
func isGitExecErrorWithKind(err error, kind gitx.GitExecErrorKind) bool {
	var gitErr *gitx.GitExecError
	if !errors.As(err, &gitErr) {
		return false
	}
	return gitErr.Kind == kind
}

func byPath(records []gitx.DiffFileRecord, path string) (gitx.DiffFileRecord, bool) {
	for _, candidate := range records {
		if candidate.Path == path {
			return candidate, true
		}
	}
	paths := make([]string, 0, len(records))
	for _, record := range records {
		paths = append(paths, record.Path)
	}
	panic(fmt.Sprintf("no diff record for %s; got: %s", path, strings.Join(paths, ", ")))
}

func requireGitExecErrorKind(t *testing.T, err error, kind gitx.GitExecErrorKind) {
	t.Helper()
	require.Error(t, err)
	assert.True(t, isGitExecErrorWithKind(err, kind),
		"expected GitExecError kind %q, got %v", kind, err)
	assert.Equal(t, docutil.CodeGit, docutil.CodeOf(err))
}

func TestGitClientDiffFiles_StatusesAndOldPathsForAddModifyDeleteRename(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "keep.md", "unchanged\n")
	repo.write(t, "modify.md", "base\n")
	repo.write(t, "delete.md", "del\n")
	repo.write(t, "src/old-name.md", joinLines(20, "")+"\n")
	base := repo.commitAll(t, "base")

	repo.write(t, "modify.md", "changed\n")
	repo.remove(t, "delete.md")
	repo.write(t, "docs/added.md", "added\n")
	repo.rename(t, "src/old-name.md", "src/new-name.md")
	repo.write(t, "src/new-name.md", joinLines(20, " EDITED", 3)+"\n")
	head := repo.commitAll(t, "head")
	assert.NotEqual(t, base, head)

	records, err := repo.client(t).DiffFiles(context.Background(), base, head)
	require.NoError(t, err)

	// keep.md is untouched; the four changed files appear exactly once each.
	paths := make([]string, 0, len(records))
	for _, record := range records {
		paths = append(paths, record.Path)
	}
	sort.Strings(paths)
	assert.Equal(t, []string{"delete.md", "docs/added.md", "modify.md", "src/new-name.md"}, paths)

	added, ok := byPath(records, "docs/added.md")
	require.True(t, ok)
	assert.Equal(t, gitx.DiffStatusAdded, added.Status)
	assert.Nil(t, added.OldPath)
	assert.Contains(t, added.Patch, "+++ b/docs/added.md")
	assert.Contains(t, added.Patch, "+added")

	modified, ok := byPath(records, "modify.md")
	require.True(t, ok)
	assert.Equal(t, gitx.DiffStatusModified, modified.Status)
	assert.Contains(t, modified.Patch, "-base")
	assert.Contains(t, modified.Patch, "+changed")

	deleted, ok := byPath(records, "delete.md")
	require.True(t, ok)
	assert.Equal(t, gitx.DiffStatusDeleted, deleted.Status)
	assert.Nil(t, deleted.OldPath)
	assert.Contains(t, deleted.Patch, "--- a/delete.md")
	assert.Contains(t, deleted.Patch, "+++ /dev/null")

	// Rename via content-similarity move (no `git mv` bookkeeping) with -M.
	renamed, ok := byPath(records, "src/new-name.md")
	require.True(t, ok)
	assert.Equal(t, gitx.DiffStatusRenamed, renamed.Status)
	require.NotNil(t, renamed.OldPath)
	assert.Equal(t, "src/old-name.md", *renamed.OldPath)
	require.NotNil(t, renamed.Similarity)
	assert.Greater(t, *renamed.Similarity, 50)
	assert.Contains(t, renamed.Patch, "rename from src/old-name.md")
	assert.Contains(t, renamed.Patch, "rename to src/new-name.md")
	assert.Contains(t, renamed.Patch, "-line 3")
}

func TestGitClientTreeSha_SameTreeHashAcrossSquashLikeRecommits(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/x.md", "A\n")
	c1 := repo.commitAll(t, "c1")

	repo.write(t, "docs/x.md", "B\n")
	squashHead := repo.commitAll(t, "c2 (squash of feature)")

	// Re-commit the same final content on a separate branch: identical tree,
	// different commit object (and different history shape).
	repo.git(t, "checkout", "-q", "-b", "squashed", c1)
	repo.write(t, "docs/x.md", "B\n")
	recommit := repo.commitAll(t, "c3 (same change, separate commit)")

	assert.NotEqual(t, squashHead, recommit)
	client := repo.client(t)
	squashTree, err := client.TreeSha(context.Background(), squashHead)
	require.NoError(t, err)
	recommitTree, err := client.TreeSha(context.Background(), recommit)
	require.NoError(t, err)
	assert.Equal(t, squashTree, recommitTree)
}

func TestGitClientPatchId_StableAcrossRecommitsDiffersAcrossContent(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/g.md", "one\nalpha\nthree\n")
	base := repo.commitAll(t, "base")

	repo.write(t, "docs/g.md", "one\nbeta\nthree\n")
	feat := repo.commitAll(t, "feat: alpha -> beta")

	// Re-commit the same textual change from the same base (squash shape).
	repo.git(t, "checkout", "-q", "-b", "same-diff", base)
	repo.write(t, "docs/g.md", "one\nbeta\nthree\n")
	recommit := repo.commitAll(t, "same change re-committed")

	// Amend (history rewrite) preserving the identical textual diff.
	amended := repo.amendAll(t, "same change, amended")

	// A different content change from the same base.
	repo.write(t, "docs/g.md", "one\ngamma\nthree\n")
	other := repo.commitAll(t, "different content")

	client := repo.client(t)
	ctx := context.Background()
	featID, err := client.PatchId(ctx, base, feat, nil)
	require.NoError(t, err)
	recommitID, err := client.PatchId(ctx, base, recommit, nil)
	require.NoError(t, err)
	amendedID, err := client.PatchId(ctx, base, amended, nil)
	require.NoError(t, err)
	otherID, err := client.PatchId(ctx, base, other, nil)
	require.NoError(t, err)

	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40}$`), featID)
	assert.Equal(t, featID, recommitID)
	assert.Equal(t, featID, amendedID)
	assert.NotEqual(t, featID, otherID)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40}$`), otherID)
}

func TestGitClientPatchId_ReturnsNullOnlyForEmptyDiffNeverForAFailingRun(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "x\n")
	only := repo.commitAll(t, "only")

	client := repo.client(t)
	ctx := context.Background()
	emptyID, err := client.PatchId(ctx, only, only, nil)
	require.NoError(t, err)
	assert.Equal(t, "", emptyID)

	_, err = client.PatchId(ctx, only, only+"~1", nil)
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.True(t, gitErr.Kind == gitx.KindUnknownRef || gitErr.Kind == gitx.KindExecFailure,
		"unexpected kind %q", gitErr.Kind)
}

func TestGitClientPatchId_StableUnderHostileDiffGitConfig(t *testing.T) {
	repo := newRepo(t)
	// Two changes far apart: diff.context=8 (vs the pinned 3) would widen
	// both hunks; noprefix/srcPrefix hostility would reshape the headers —
	// every one of them changes the PATCH TEXT a customized machine would
	// produce for the same change.
	repo.write(t, "docs/h.md", joinLines(40, "")+"\n")
	base := repo.commitAll(t, "base")
	original := repo.read(t, "docs/h.md")
	repo.write(t, "docs/h.md",
		strings.ReplaceAll(strings.ReplaceAll(original, "line 3\n", "line 3 CHANGED\n"), "line 30\n", "line 30 CHANGED\n"))
	head := repo.commitAll(t, "two separate hunks")

	// Hostile config file (the env of a customized machine): fixed values,
	// each of which alters unpinned patch text.
	hostileDir := t.TempDir()
	hostileConfigPath := filepath.Join(hostileDir, "gitconfig")
	require.NoError(t, os.WriteFile(hostileConfigPath, []byte(strings.Join([]string{
		"[diff]",
		"\tcontext = 8",
		"\tinterHunkContext = 10",
		"\talgorithm = histogram",
		"\tindentHeuristic = false",
		"\tsrcPrefix = old/",
		"\tdstPrefix = new/",
		"",
	}, "\n")), 0o644))

	// GIT_CONFIG_GLOBAL routes the child git's config; the executor copies
	// the process env per run, so the test drives it explicitly per call.
	rawDiffText := func(t *testing.T, globalConfig string) string {
		t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
		// Raw diff WITHOUT the pins (only the global quotepath flag) — this
		// is the text an unpinned machine would produce.
		result, err := gitx.CreateSystemGitExecutor(nil).Run(context.Background(), []string{
			"-C", repo.root,
			"-c", "core.quotepath=false",
			"diff", "--find-renames", "--no-color", "--no-ext-diff",
			base, head,
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, result.ExitCode)
		require.Equal(t, 0, *result.ExitCode)
		return string(result.Stdout)
	}

	// Hostility is REAL: the unpinned diff text differs under the hostile
	// config (a customized machine would compute a different patch-id).
	cleanText := rawDiffText(t, "/dev/null")
	hostileText := rawDiffText(t, hostileConfigPath)
	assert.NotEqual(t, cleanText, hostileText)
	assert.Contains(t, cleanText, "--- a/docs/h.md")
	// srcPrefix hostility reshapes the headers; context=8 + interHunk
	// hostility reshapes the hunks (the two far-apart edits merge into one).
	assert.Contains(t, hostileText, "--- old/docs/h.md")
	assert.Contains(t, hostileText, "@@ -1,38 +1,38 @@")

	// (1) Hostile config FILE vs the pinned client: identical patch-id —
	// the pins neutralize the whole hostile config surface.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	pinnedClient := repo.client(t)
	pinnedID, err := pinnedClient.PatchId(context.Background(), base, head, nil)
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40}$`), pinnedID)

	t.Setenv("GIT_CONFIG_GLOBAL", hostileConfigPath)
	hostileClient, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	hostileID, err := hostileClient.PatchId(context.Background(), base, head, nil)
	require.NoError(t, err)
	assert.Equal(t, pinnedID, hostileID)

	// (2) Hostile -c args ON THE RAW EXECUTOR (no pins): they DO change the
	// patch-id — the negative control proving the pins are load-bearing
	// (patch-id hashes the whole patch text shape: context width, hunk
	// merging, prefixes).
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	executor := gitx.CreateSystemGitExecutor(nil)
	diff, err := executor.Run(context.Background(), []string{
		"-c", "diff.context=8",
		"-c", "diff.interHunkContext=10",
		"-c", "diff.algorithm=histogram",
		"-c", "diff.indentHeuristic=false",
		"-C", repo.root,
		"-c", "core.quotepath=false",
		"diff", "--find-renames", "--no-color", "--no-ext-diff",
		base, head,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, diff.ExitCode)
	require.Equal(t, 0, *diff.ExitCode)
	idOut, err := executor.Run(context.Background(), []string{"patch-id", "--stable"}, &gitx.GitRunOptions{Stdin: diff.Stdout})
	require.NoError(t, err)
	require.NotNil(t, idOut.ExitCode)
	require.Equal(t, 0, *idOut.ExitCode)
	rawHostileID := strings.Fields(string(idOut.Stdout))[0]
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40}$`), rawHostileID)
	assert.NotEqual(t, pinnedID, rawHostileID)

	// (3) Hostile -c args THROUGH the pinned client: an environment layer
	// that PREPENDS hostile config pairs (ahead of GitClient's own args)
	// cannot change the patch-id — the pins run later and `-c` options are
	// last-writer-wins (and always beat config files), so the pinned values
	// prevail over anything the environment layered beneath.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	hostilePrefix := []string{
		"-c", "diff.context=8",
		"-c", "diff.interHunkContext=10",
		"-c", "diff.algorithm=histogram",
		"-c", "diff.indentHeuristic=false",
		"-c", "diff.noprefix=true",
		"-c", "diff.srcPrefix=old/",
		"-c", "diff.dstPrefix=new/",
	}
	injectedClient, err := gitx.NewGitClient(gitx.GitClientOptions{
		RepoRoot: repo.root,
		Executor: &prefixingExecutor{prefix: hostilePrefix, inner: gitx.CreateSystemGitExecutor(nil)},
	})
	require.NoError(t, err)
	injectedID, err := injectedClient.PatchId(context.Background(), base, head, nil)
	require.NoError(t, err)
	assert.Equal(t, pinnedID, injectedID)
}

func TestGitClientCatFile_BlobShaEqualsHashObjectOfContent(t *testing.T) {
	repo := newRepo(t)
	content := "héllo — résumé 💥\nsecond line\n"
	repo.write(t, "docs/uni.md", content)
	repo.commitAll(t, "add unicode doc")

	result, err := repo.client(t).CatFile(context.Background(), "HEAD", "docs/uni.md")
	require.NoError(t, err)
	assert.Equal(t, content, result.Content)

	expectedSha := strings.TrimSpace(repo.gitOut(t, "hash-object", filepath.Join(repo.root, "docs/uni.md")))
	assert.Equal(t, expectedSha, result.BlobSha)
}

func TestGitClientFailureSemantics_InjectedGitFailureThrowsTypedErrorNeverEmptyDiff(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "x\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "docs/a.md", "y\n")
	head := repo.commitAll(t, "head")

	client, err := gitx.NewGitClient(gitx.GitClientOptions{
		RepoRoot: repo.root,
		Executor: &staticExecutor{result: gitx.GitRunResult{
			ExitCode: intPtr(128),
			Stdout:   []byte{},
			Stderr:   []byte("fatal: simulated git outage\n"),
		}},
	})
	require.NoError(t, err)

	ctx := context.Background()
	// Lazily invoked thunks: a failure aborts the loop without leaving eager
	// unhandled failures behind.
	expectations := []struct {
		call func() error
		kind gitx.GitExecErrorKind
	}{
		// resolveRev's only failure mode is a ref that cannot be verified.
		{call: func() error { _, err := client.ResolveRev(ctx, "HEAD"); return err }, kind: gitx.KindUnknownRef},
		{call: func() error { _, err := client.TreeSha(ctx, "HEAD"); return err }, kind: gitx.KindExecFailure},
		// catFile resolves the rev first: resolveRev's only failure mode is a
		// ref that cannot be verified → unknown-ref, even under a broken git.
		{call: func() error { _, err := client.CatFile(ctx, "HEAD", "docs/a.md"); return err }, kind: gitx.KindUnknownRef},
		{call: func() error { _, err := client.LsTree(ctx, "HEAD", nil); return err }, kind: gitx.KindExecFailure},
		{call: func() error { _, err := client.DiffFiles(ctx, base, head); return err }, kind: gitx.KindExecFailure},
		{call: func() error { _, err := client.DiffStat(ctx, base, head); return err }, kind: gitx.KindExecFailure},
		{call: func() error { _, err := client.PatchId(ctx, base, head, nil); return err }, kind: gitx.KindExecFailure},
		{call: func() error { _, err := client.MergeBase(ctx, base, head); return err }, kind: gitx.KindExecFailure},
	}
	for _, expectation := range expectations {
		requireGitExecErrorKind(t, expectation.call(), expectation.kind)
	}

	// patchId never degrades a broken run to "", and the stderr excerpt is
	// preserved for run records.
	_, err = client.PatchId(ctx, base, head, nil)
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.Equal(t, gitx.KindExecFailure, gitErr.Kind)
	assert.Contains(t, gitErr.StderrExcerpt, "simulated git outage")
}

func TestGitClientOptionInjectionGuard_RejectsDashPrefixedArgs(t *testing.T) {
	repo := newRepo(t)
	recording := &recordingExecutor{result: gitx.GitRunResult{ExitCode: intPtr(0)}}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: recording})
	require.NoError(t, err)

	ctx := context.Background()
	injections := []func() error{
		func() error { _, err := client.ResolveRev(ctx, "-oProxyCommand=x"); return err },
		func() error { _, err := client.ResolveRev(ctx, "HEAD\x00extra"); return err },
		func() error { _, err := client.ResolveRev(ctx, "HEAD\n"); return err },
		func() error { _, err := client.TreeSha(ctx, "--upload-pack=x"); return err },
		func() error { _, err := client.MergeBase(ctx, "HEAD", "--exec=evil"); return err },
		func() error { _, err := client.CatFile(ctx, "HEAD", "-foo.md"); return err },
		func() error { _, err := client.CatFile(ctx, "HEAD", "docs/a\x00b.md"); return err },
		func() error { _, err := client.LsTree(ctx, "HEAD", strptr("-zapping")); return err },
		func() error { _, err := client.PatchId(ctx, "HEAD", "HEAD", strptr("--output=/tmp/evil")); return err },
		func() error { _, err := client.DiffFiles(ctx, "-uAll", "HEAD"); return err },
		func() error { _, err := client.DiffStat(ctx, "--exec=evil", "HEAD"); return err },
	}
	for _, injection := range injections {
		requireGitExecErrorKind(t, injection(), gitx.KindInvalidArg)
	}
	// The guard fires BEFORE exec — the binary was never invoked.
	assert.Equal(t, 0, recording.calls)
}

func TestGitClientMergeBase_NullOnlyForUnrelatedHistories(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/root.md", "root\n")
	root := repo.commitAll(t, "root")

	repo.git(t, "checkout", "-q", "-b", "side")
	repo.write(t, "docs/side.md", "side\n")
	repo.commitAll(t, "side work")
	repo.git(t, "checkout", "-q", "main")
	repo.write(t, "docs/main.md", "main\n")
	repo.commitAll(t, "main work")

	client := repo.client(t)
	ctx := context.Background()
	merged, err := client.MergeBase(ctx, "main", "side")
	require.NoError(t, err)
	assert.Equal(t, root, merged)

	// Genuinely unrelated history: orphan branch with an independent root.
	repo.git(t, "checkout", "-q", "--orphan", "isolated")
	repo.git(t, "rm", "-q", "-rf", ".")
	repo.write(t, "other.md", "independent\n")
	repo.commitAll(t, "isolated root")

	unrelated, err := client.MergeBase(ctx, "main", "isolated")
	require.NoError(t, err)
	assert.Equal(t, "", unrelated)

	// "only for unrelated histories": an unknown ref is still a typed error.
	_, err = client.MergeBase(ctx, "main", "nonexistent-ref")
	requireGitExecErrorKind(t, err, gitx.KindUnknownRef)
}

func TestGitClientReadAPIs_RoundTripCanonicalValues(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "alpha\n")
	repo.write(t, "docs/b.md", "beta\n")
	sha := repo.commitAll(t, "two docs")

	client := repo.client(t)
	ctx := context.Background()
	resolved, err := client.ResolveRev(ctx, "main")
	require.NoError(t, err)
	assert.Equal(t, sha, resolved)
	resolved, err = client.ResolveRev(ctx, sha)
	require.NoError(t, err)
	assert.Equal(t, sha, resolved)

	tree, err := client.TreeSha(ctx, sha)
	require.NoError(t, err)
	mainTree, err := client.TreeSha(ctx, "main")
	require.NoError(t, err)
	assert.Equal(t, tree, mainTree)
	assert.Equal(t, strings.TrimSpace(repo.gitOut(t, "rev-parse", sha+"^{tree}")), tree)

	entries, err := client.LsTree(ctx, sha, nil)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "100644", entries[0].Mode)
	assert.Equal(t, "blob", entries[0].Type)
	assert.Equal(t, "docs/a.md", entries[0].Path)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40}$`), entries[0].Sha)
	assert.Equal(t, "100644", entries[1].Mode)
	assert.Equal(t, "blob", entries[1].Type)
	assert.Equal(t, "docs/b.md", entries[1].Path)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40}$`), entries[1].Sha)

	blobSha := entries[0].Sha
	cat, err := client.CatFile(ctx, sha, "docs/a.md")
	require.NoError(t, err)
	assert.Equal(t, blobSha, cat.BlobSha)
	assert.Equal(t, "alpha\n", cat.Content)

	// Unknown refs are typed unknown-ref (not empty results, not exec-failure).
	_, err = client.ResolveRev(ctx, "nonexistent-ref")
	requireGitExecErrorKind(t, err, gitx.KindUnknownRef)
	_, err = client.CatFile(ctx, sha, "docs/missing.md")
	requireGitExecErrorKind(t, err, gitx.KindMissingRev)

	// History-walk primitives (P1-T16 groundwork).
	parents, err := client.ParentShas(ctx, sha)
	require.NoError(t, err)
	assert.Empty(t, parents)
	repo.write(t, "docs/a.md", "alpha2\n")
	second := repo.commitAll(t, "second")
	parents, err = client.ParentShas(ctx, second)
	require.NoError(t, err)
	assert.Equal(t, []string{sha}, parents)
	commits, err := client.LogCommits(ctx, second, 10)
	require.NoError(t, err)
	require.Len(t, commits, 2)
	assert.Equal(t, second, commits[0].Sha)
	assert.Equal(t, []string{sha}, commits[0].Parents)
	assert.Empty(t, commits[1].Parents)
}

func TestGitClientDiffStat_ReportsPerFileCountsAndBinaryFiles(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/text.md", "a\nb\nc\n")
	base := repo.commitAll(t, "base")

	repo.write(t, "docs/text.md", "a\nB\nc\nd\n")
	repo.writeBinary(t, "assets/logo.bin", []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01})
	head := repo.commitAll(t, "edit text, add binary")

	stats, err := repo.client(t).DiffStat(context.Background(), base, head)
	require.NoError(t, err)

	text, ok := statByPath(stats, "docs/text.md")
	require.True(t, ok)
	require.NotNil(t, text.Additions)
	require.NotNil(t, text.Deletions)
	assert.Equal(t, 2, *text.Additions)
	assert.Equal(t, 1, *text.Deletions)
	assert.False(t, text.Binary)

	binary, ok := statByPath(stats, "assets/logo.bin")
	require.True(t, ok)
	assert.Nil(t, binary.Additions)
	assert.Nil(t, binary.Deletions)
	assert.True(t, binary.Binary)
}

func TestGitPackageSurface_PublicAPISurface(t *testing.T) {
	// TS kept the exec internals module-internal behind the git barrel; in Go
	// the package IS the module, so the surface is asserted directly here.
	var _ func(gitx.GitClientOptions) (*gitx.GitClient, error) = gitx.NewGitClient
	var _ func(*gitx.SystemGitExecutorOptions) *gitx.SystemGitExecutor = gitx.CreateSystemGitExecutor
	var _ gitx.GitExecutor = (*gitx.SystemGitExecutor)(nil)

	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: t.TempDir()})
	require.NoError(t, err)
	assert.NotEmpty(t, client.Root())

	// The determinism constants are part of the published D12 contract.
	assert.Equal(t, []string{"-c", "core.quotepath=false"}, gitx.GITQuotePathArgs)
	assert.Contains(t, gitx.GITDiffConfigPins, "diff.algorithm=myers")
	assert.Equal(t, []string{"--no-color", "--no-ext-diff"}, gitx.GITDiffOutputArgs)
}

func TestGitClientScopedQueries_PathPrefixAndPatchIDPath(t *testing.T) {
	repo := newRepo(t)
	repo.write(t, "docs/a.md", "one\n")
	repo.write(t, "src/b.md", "one\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "docs/a.md", "two\n")
	repo.write(t, "src/b.md", "two\n")
	head := repo.commitAll(t, "head")

	client := repo.client(t)
	ctx := context.Background()

	entries, err := client.LsTree(ctx, head, strptr("docs"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "docs/a.md", entries[0].Path)

	id, err := client.PatchId(ctx, base, head, strptr("docs/a.md"))
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40}$`), id)

	// A path with no change still yields an empty diff → no patch-id (null).
	unchanged, err := client.PatchId(ctx, base, head, strptr("docs/unmentioned.md"))
	require.NoError(t, err)
	assert.Equal(t, "", unchanged)
}

func TestGitClientWritePath_BranchStageCommitAndStatus(t *testing.T) {
	repo := newRepo(t)
	setIdentity(t, repo)
	repo.write(t, "docs/base.md", "base\n")
	base := repo.commitAll(t, "base")
	client := repo.client(t)
	ctx := context.Background()

	// Branch creation (P6-T01): no start point → current HEAD.
	require.NoError(t, client.CreateBranch(ctx, "docsync/write-path", nil))
	assert.Equal(t, base, strings.TrimSpace(repo.gitOut(t, "rev-parse", "docsync/write-path")))

	// Branch creation at an explicit start point.
	require.NoError(t, client.CreateBranch(ctx, "docsync/write-path-2", strptr(base)))
	assert.Equal(t, base, strings.TrimSpace(repo.gitOut(t, "rev-parse", "docsync/write-path-2")))

	// A pre-existing branch name is a typed failure (idempotency is the
	// caller's decision, never the client's).
	err := client.CreateBranch(ctx, "docsync/write-path", nil)
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)

	// Exact-path staging (never sweeps the worktree).
	repo.write(t, "docs/new.md", "new\n")
	repo.write(t, "other.md", "unstaged\n")
	require.NoError(t, client.StagePaths(ctx, []string{"docs/new.md"}))
	cached, err := client.CachedFilePaths(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"docs/new.md"}, cached)

	status, err := client.StatusPorcelain(ctx)
	require.NoError(t, err)
	assert.Len(t, status, 2)
	assert.Contains(t, status, "A  docs/new.md")

	// Plain commit — no hook bypass — returns the new HEAD sha.
	newHead, err := client.Commit(ctx, "docs: add new.md")
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40}$`), newHead)
	resolved, err := client.ResolveRev(ctx, newHead)
	require.NoError(t, err)
	assert.Equal(t, newHead, resolved)

	status, err = client.StatusPorcelain(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"?? other.md"}, status)

	// Fail-closed write guards.
	err = client.StagePaths(ctx, nil)
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)
	assert.Contains(t, err.Error(), "git add requires at least one path")
	_, err = client.Commit(ctx, "")
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)
	_, err = client.Commit(ctx, "docs: \x00")
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)
	err = client.PushBranch(ctx, "--exec=evil", "", false)
	requireGitExecErrorKind(t, err, gitx.KindInvalidArg)
}

func TestGitClientWritePath_PushToConfiguredRemote(t *testing.T) {
	repo := newRepo(t)
	setIdentity(t, repo)
	repo.write(t, "docs/p.md", "p\n")
	first := repo.commitAll(t, "push base")
	repo.write(t, "docs/p.md", "p2\n")
	second := repo.commitAll(t, "push two")
	client := repo.client(t)
	ctx := context.Background()

	// A local file remote (no network, no auth): the scoped push of exactly
	// one branch.
	remoteDir := t.TempDir()
	runGit(t, remoteDir, "init", "-q", "--bare", "-b", "main")
	// The client's default remote is `origin` (P6-T04).
	repo.git(t, "remote", "add", "origin", remoteDir)

	require.NoError(t, client.CreateBranch(ctx, "docsync/other", strptr(second)))
	require.NoError(t, client.PushBranch(ctx, "docsync/other", "", false))
	assert.Contains(t, strings.TrimSpace(repo.gitOut(t, "ls-remote", "origin", "refs/heads/docsync/other")),
		"refs/heads/docsync/other")

	// The remote already holds a divergent history: a non-forced push of one
	// branch is a typed failure, never an improvised recovery (the PR layer
	// decides supersede/rebase).
	runGit(t, remoteDir, "update-ref", "refs/heads/docsync/pushed", second)
	require.NoError(t, client.CreateBranch(ctx, "docsync/pushed", strptr(first)))
	err := client.PushBranch(ctx, "docsync/pushed", "", false)
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)

	// --force is the caller's decision (supersede), still one branch only.
	require.NoError(t, client.PushBranch(ctx, "docsync/pushed", "", true))
}

func TestGitClientRebaseBranch_CleanReplayAndAbortedConflict(t *testing.T) {
	repo := newRepo(t)
	setIdentity(t, repo)
	repo.write(t, "docs/c.md", "base\n")
	repo.commitAll(t, "base")

	repo.git(t, "checkout", "-q", "-b", "feature")
	repo.write(t, "docs/feature.md", "feature\n")
	featureHead := repo.commitAll(t, "feature")

	repo.git(t, "checkout", "-q", "main")
	repo.write(t, "docs/main2.md", "main\n")
	repo.commitAll(t, "main advance")

	client := repo.client(t)
	ctx := context.Background()
	before, err := os.ReadFile(filepath.Join(repo.root, "docs/c.md"))
	require.NoError(t, err)

	// Clean replay.
	result, err := client.RebaseBranch(ctx, "feature", "main")
	require.NoError(t, err)
	assert.True(t, result.Ok)
	assert.False(t, result.Conflicted)
	replayed, err := client.ParentShas(ctx, "feature")
	require.NoError(t, err)
	mainSha, err := client.ResolveRev(ctx, "main")
	require.NoError(t, err)
	assert.Equal(t, []string{mainSha}, replayed)
	assert.NotEqual(t, featureHead, mustResolve(t, client, "feature"))

	// Conflicting replay: conflict is a semantic outcome, aborted and reported.
	repo.git(t, "checkout", "-q", "main")
	repo.write(t, "docs/c.md", "main side\n")
	repo.commitAll(t, "main conflict side")
	repo.git(t, "checkout", "-q", "feature")
	repo.write(t, "docs/c.md", "feature side\n")
	conflicted := repo.commitAll(t, "feature conflict side")

	result, err = client.RebaseBranch(ctx, "feature", "main")
	require.NoError(t, err)
	assert.False(t, result.Ok)
	assert.True(t, result.Conflicted)

	// The abort restored the exact pre-rebase state (the client stays
	// mechanical; the gate layer re-verifies the tree).
	assert.Equal(t, conflicted, mustResolve(t, client, "feature"))
	after, err := os.ReadFile(filepath.Join(repo.root, "docs/c.md"))
	require.NoError(t, err)
	assert.Equal(t, "feature side\n", string(after))
	_ = before
}

func mustResolve(t *testing.T, client *gitx.GitClient, ref string) string {
	t.Helper()
	sha, err := client.ResolveRev(context.Background(), ref)
	require.NoError(t, err)
	return sha
}

// setIdentity pins a repo-local commit identity (the fixture env has no user
// config), which the replaying write paths need.
func setIdentity(t *testing.T, repo *testGitRepo) {
	t.Helper()
	repo.git(t, "config", "user.name", "docsync-test")
	repo.git(t, "config", "user.email", "docsync-test@invalid")
}

/* ------------------------------------------------------------- helpers */

// staticExecutor always fails with the same scripted result.
type staticExecutor struct {
	result gitx.GitRunResult
}

func (s *staticExecutor) Run(_ context.Context, _ []string, _ *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	return s.result, nil
}

// recordingExecutor counts invocations (the guard must fire before exec).
type recordingExecutor struct {
	result gitx.GitRunResult
	calls  int
}

func (r *recordingExecutor) Run(_ context.Context, _ []string, _ *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	r.calls++
	return r.result, nil
}

// prefixingExecutor prepends hostile `-c` pairs to every argv (the pins must
// still win, being later writers).
type prefixingExecutor struct {
	prefix []string
	inner  gitx.GitExecutor
}

func (p *prefixingExecutor) Run(ctx context.Context, args []string, opts *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	return p.inner.Run(ctx, append(append([]string(nil), p.prefix...), args...), opts)
}

func statByPath(records []gitx.DiffStatRecord, path string) (gitx.DiffStatRecord, bool) {
	for _, record := range records {
		if record.Path == path {
			return record, true
		}
	}
	return gitx.DiffStatRecord{}, false
}

func strptr(s string) *string { return &s }

func joinLines(count int, editedSuffix string, editedLines ...int) string {
	edited := map[int]bool{}
	for _, line := range editedLines {
		edited[line] = true
	}
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		suffix := editedSuffix
		if !edited[i] {
			suffix = ""
		}
		lines = append(lines, "line "+strconv.Itoa(i)+suffix)
	}
	return strings.Join(lines, "\n")
}
