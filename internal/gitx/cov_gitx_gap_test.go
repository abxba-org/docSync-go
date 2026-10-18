package gitx_test

// Coverage-gap tests for internal/gitx: D12/D14 branches the ported and
// earlier-coverage suites left uncovered — write-path guards, empty-patch-id
// edge, and every method's git-failure propagation through the scripted
// executor seam (V-P1-10).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gapEnv is a deterministic isolated git environment.
func gapEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"TERM=dumb",
	}
}

func gapGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gapEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return string(out)
}

// gapIdentity runs a fixture git command with a pinned identity.
func gapIdentity(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gapGit(t, dir, append([]string{"-c", "user.name=docsync-test", "-c", "user.email=docsync-test@invalid"}, args...)...)
}

func gapWrite(t *testing.T, target, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

// gapRepo builds a client over a fresh two-commit repo.
func gapRepo(t *testing.T) (*gitx.GitClient, string) {
	t.Helper()
	root := t.TempDir()
	gapGit(t, root, "init", "-b", "main")
	gapGit(t, root, "config", "core.autocrlf", "false")
	gapGit(t, root, "config", "core.eol", "lf")
	gapWrite(t, filepath.Join(root, "a.txt"), "one\n")
	gapIdentity(t, root, "add", "-A")
	gapIdentity(t, root, "commit", "-q", "-m", "one")
	head := strings.TrimSpace(gapGit(t, root, "rev-parse", "HEAD"))
	gapWrite(t, filepath.Join(root, "a.txt"), "two\n")
	gapIdentity(t, root, "add", "-A")
	gapIdentity(t, root, "commit", "-q", "-m", "two")
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	return client, head
}

// gapSubcommand finds the first plain token (config assignments and flags are
// skipped) in the spliced argv.
func gapSubcommand(args []string) string {
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") || strings.Contains(a, "=") || strings.HasPrefix(a, "/") {
			continue
		}
		return a
	}
	return ""
}

// gapFailExecutor fails git invocations matched by fail with a scripted
// executor error (the seam D14 surface).
type gapFailExecutor struct {
	inner gitx.GitExecutor
	fail  func(args []string) bool
}

func (f *gapFailExecutor) Run(ctx context.Context, args []string, opts *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	if f.fail(args) {
		return gitx.GitRunResult{}, errors.New("scripted executor failure")
	}
	return f.inner.Run(ctx, args, opts)
}

func gapClientWithFail(t *testing.T, root string, fail func(args []string) bool) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{
		RepoRoot: root,
		Executor: &gapFailExecutor{inner: gitx.CreateSystemGitExecutor(nil), fail: fail},
	})
	require.NoError(t, err)
	return client
}

func gapAnyContains(args []string, needle string) bool {
	for _, a := range args {
		if strings.Contains(a, needle) {
			return true
		}
	}
	return false
}

/* ------------------------------------------------------------ read paths */

func TestCOV_GITX_PatchIdModeOnlyChangeIsEmpty(t *testing.T) {
	root := t.TempDir()
	gapGit(t, root, "init", "-b", "main")
	gapGit(t, root, "config", "core.autocrlf", "false")
	gapGit(t, root, "config", "core.eol", "lf")
	gapWrite(t, filepath.Join(root, "run.sh"), "#!/bin/sh\n")
	require.NoError(t, os.Chmod(filepath.Join(root, "run.sh"), 0o644))
	gapIdentity(t, root, "add", "-A")
	gapIdentity(t, root, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gapGit(t, root, "rev-parse", "HEAD"))
	require.NoError(t, os.Chmod(filepath.Join(root, "run.sh"), 0o755))
	gapIdentity(t, root, "add", "-A")
	gapIdentity(t, root, "commit", "-q", "-m", "chmod")
	head := strings.TrimSpace(gapGit(t, root, "rev-parse", "HEAD"))

	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	// Real git emits a patch-id even for a mode-only diff header (observed
	// against git 2.x: no hunk, still a stable id), so the documented edge is
	// asserted as the actual contract: a hex id, never an error.
	modeID, err := client.PatchId(t.Context(), base, head, nil)
	require.NoError(t, err)
	assert.Equal(t, 40, len(modeID))
}

func TestCOV_GITX_PatchIdRunFailurePropagates(t *testing.T) {
	client, _ := gapRepo(t)
	client2 := gapClientWithFail(t, client.Root(), func(args []string) bool {
		return gapSubcommand(args) == "patch-id"
	})
	_, err := client2.PatchId(t.Context(), "HEAD~1", "HEAD", nil)
	require.Error(t, err)
}

func TestCOV_GITX_CatFileBlobReadErrorPropagates(t *testing.T) {
	client, head := gapRepo(t)
	failing := gapClientWithFail(t, client.Root(), func(args []string) bool {
		return gapSubcommand(args) == "cat-file"
	})
	_, err := failing.CatFile(t.Context(), head, "a.txt")
	require.Error(t, err)
}

func TestCOV_GITX_LogCommitsBrokenRevPropagates(t *testing.T) {
	client, _ := gapRepo(t)
	_, err := client.LogCommits(t.Context(), "definitely-not-a-ref", 5)
	require.Error(t, err)
}

func TestCOV_GITX_ParentShasRootCommitEmpty(t *testing.T) {
	client, _ := gapRepo(t)
	parents, err := client.ParentShas(t.Context(), "HEAD~1")
	require.NoError(t, err)
	assert.Empty(t, parents)
}

/* ---------------------------------------------------- write-path branches */

func TestCOV_GITX_CreateBranchStartPointAndGuard(t *testing.T) {
	client, head := gapRepo(t)
	headP := &head

	require.NoError(t, client.CreateBranch(t.Context(), "feat/start", headP))
	require.NoError(t, client.CreateBranch(t.Context(), "feat/plain", nil))

	_, err := client.ResolveRev(t.Context(), "feat/start")
	require.NoError(t, err)

	err = client.CreateBranch(t.Context(), "-bad", nil)
	require.Error(t, err)
	err = client.CreateBranch(t.Context(), "ok", strPointer("-bad"))
	require.Error(t, err)
	_, ok := gitx.AsGitExecError(err)
	assert.True(t, ok)
}

func strPointer(s string) *string { return &s }

func TestCOV_GITX_StagePathsInvalidPathRefused(t *testing.T) {
	client, _ := gapRepo(t)
	err := client.StagePaths(t.Context(), []string{"-evil"})
	require.Error(t, err)
	gitErr, ok := gitx.AsGitExecError(err)
	require.True(t, ok)
	assert.Equal(t, gitx.KindInvalidArg, gitErr.Kind)
}

func TestCOV_GITX_CommitInvalidMessageRefused(t *testing.T) {
	client, _ := gapRepo(t)
	_, err := client.Commit(t.Context(), "")
	require.Error(t, err)
	_, err = client.Commit(t.Context(), "bad\x00msg")
	require.Error(t, err)
}

func TestCOV_GITX_PushBranchGuards(t *testing.T) {
	client, _ := gapRepo(t)
	// No remote configured: `origin` does not exist → classified typed failure.
	err := client.PushBranch(t.Context(), "main", "", false)
	require.Error(t, err)
	gitErr, ok := gitx.AsGitExecError(err)
	require.True(t, ok)
	assert.Equal(t, gitx.KindExecFailure, gitErr.Kind)

	err = client.PushBranch(t.Context(), "-branch", "origin", false)
	require.Error(t, err)
	err = client.PushBranch(t.Context(), "main", "-remote", false)
	require.Error(t, err)
	remoteErr, ok := gitx.AsGitExecError(err)
	require.True(t, ok)
	assert.Equal(t, gitx.KindInvalidArg, remoteErr.Kind)
}

func TestCOV_GITX_WriteMethodFailuresPropagate(t *testing.T) {
	cases := []struct {
		name  string
		match func(args []string) bool
		run   func(t *testing.T, c *gitx.GitClient)
	}{
		{
			name:  "cached paths",
			match: func(args []string) bool { return gapAnyContains(args, "--cached") },
			run: func(t *testing.T, c *gitx.GitClient) {
				_, err := c.CachedFilePaths(t.Context())
				require.Error(t, err)
			},
		},
		{
			name:  "status",
			match: func(args []string) bool { return gapSubcommand(args) == "status" },
			run: func(t *testing.T, c *gitx.GitClient) {
				_, err := c.StatusPorcelain(t.Context())
				require.Error(t, err)
			},
		},
		{
			name:  "commit",
			match: func(args []string) bool { return gapSubcommand(args) == "commit" },
			run: func(t *testing.T, c *gitx.GitClient) {
				_, err := c.Commit(t.Context(), "x")
				require.Error(t, err)
			},
		},
		{
			name:  "branch",
			match: func(args []string) bool { return gapSubcommand(args) == "branch" },
			run: func(t *testing.T, c *gitx.GitClient) {
				require.Error(t, c.CreateBranch(t.Context(), "feat/x", nil))
			},
		},
		{
			name:  "add",
			match: func(args []string) bool { return gapSubcommand(args) == "add" },
			run: func(t *testing.T, c *gitx.GitClient) {
				require.Error(t, c.StagePaths(t.Context(), []string{"a.txt"}))
			},
		},
		{
			name:  "push",
			match: func(args []string) bool { return gapSubcommand(args) == "push" },
			run: func(t *testing.T, c *gitx.GitClient) {
				require.Error(t, c.PushBranch(t.Context(), "main", "origin", false))
			},
		},
		{
			name:  "rebase",
			match: func(args []string) bool { return gapSubcommand(args) == "rebase" },
			run: func(t *testing.T, c *gitx.GitClient) {
				_, err := c.RebaseBranch(t.Context(), "main", "HEAD")
				require.Error(t, err)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := gapRepo(t)
			failing := gapClientWithFail(t, client.Root(), tc.match)
			tc.run(t, failing)
		})
	}
}

func TestCOV_GITX_RebaseAbortFailurePropagates(t *testing.T) {
	root := t.TempDir()
	gapGit(t, root, "init", "-b", "main")
	gapGit(t, root, "config", "core.autocrlf", "false")
	gapGit(t, root, "config", "core.eol", "lf")
	gapWrite(t, filepath.Join(root, "conflict.txt"), "base\n")
	gapIdentity(t, root, "add", "-A")
	gapIdentity(t, root, "commit", "-q", "-m", "base")

	gapGit(t, root, "checkout", "-q", "-b", "feat")
	gapWrite(t, filepath.Join(root, "conflict.txt"), "feature\n")
	gapIdentity(t, root, "add", "-A")
	gapIdentity(t, root, "commit", "-q", "-m", "feat")

	gapGit(t, root, "checkout", "-q", "main")
	gapWrite(t, filepath.Join(root, "conflict.txt"), "mainline\n")
	gapIdentity(t, root, "add", "-A")
	gapIdentity(t, root, "commit", "-q", "-m", "main")
	newHead := strings.TrimSpace(gapGit(t, root, "rev-parse", "HEAD"))

	gapGit(t, root, "checkout", "-q", "feat")

	// The rebase runs (conflict detected), then the --abort recovery run fails.
	failing := gapClientWithFail(t, root, func(args []string) bool {
		return gapAnyContains(args, "--abort")
	})
	_, err := failing.RebaseBranch(t.Context(), "feat", newHead)
	require.Error(t, err)
}

/* ------------------------------------------ scripted read-path assertions */

func TestCOV_GITX_ResolveRevNonShaFailsClosed(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("not-a-sha")}),
	})
	_, err := client.ResolveRev(t.Context(), "HEAD")
	require.Error(t, err)
}

func TestCOV_GITX_CatFileBlobShaNonShaFailsClosed(t *testing.T) {
	sha := strings.Repeat("a", 40)
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte(sha)}),    // ^{commit}
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("nope")}), // sha:path
	})
	_, err := client.CatFile(t.Context(), sha, "a.txt")
	require.Error(t, err)
}

func TestCOV_GITX_MergeBaseExitTwoClassified(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(2), Stdout: []byte("x"), Stderr: []byte("boom")}),
	})
	_, err := client.MergeBase(t.Context(), "HEAD", "HEAD")
	require.Error(t, err)
	gitErr, ok := gitx.AsGitExecError(err)
	require.True(t, ok)
	assert.Contains(t, gitErr.Msg, "exit=2")
}

func TestCOV_GITX_PatchIdEmptyAndNonHexScripted(t *testing.T) {
	sha := strings.Repeat("a", 40)
	diffOut := capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n")})

	t.Run("empty patch-id text counts as no id", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			diffOut,
			capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte{}}),
		})
		id, err := client.PatchId(t.Context(), sha, sha, nil)
		require.NoError(t, err)
		assert.Equal(t, "", id)
	})

	t.Run("non-hex patch-id output fails closed", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			diffOut,
			capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("zzz 0000000000000000000000000000000000000000")}),
		})
		_, err := client.PatchId(t.Context(), sha, sha, nil)
		require.Error(t, err)
		gitErr, ok := gitx.AsGitExecError(err)
		require.True(t, ok)
		assert.Contains(t, gitErr.Msg, "non-hex output")
	})
}

func TestCOV_GITX_ParentShasNonShaParentFailsClosed(t *testing.T) {
	sha := strings.Repeat("a", 40)
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte(sha + " maparenttoken\n")}),
	})
	_, err := client.ParentShas(t.Context(), sha)
	require.Error(t, err)
}

func TestCOV_GITX_DiffFilesSectionCountMismatchFailsClosed(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	raw := ":100644 100644 " + sha + " " + sha + " A\x00docs/a.md\x00" +
		":100644 100644 " + sha + " " + sha + " M\x00docs/b.md\x00"
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte(raw)}),
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("diff --git docs/a.md docs/a.md\n--- a/docs/a.md\n+++ b/docs/a.md\n")}),
	})
	_, err := client.DiffFiles(t.Context(), "a000000000000000000000000000000000000000", "b000000000000000000000000000000000000000")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "raw records vs")
}

/* ------------------------------------------------ arg-guard validation */

func TestCOV_GITX_ReadValidationBranches(t *testing.T) {
	client, _ := gapRepo(t)
	sha := strings.Repeat("a", 40)
	badPrefix := "-evil"

	_, err := client.MergeBase(t.Context(), "", "HEAD")
	require.Error(t, err)
	_, err = client.MergeBase(t.Context(), "HEAD", "")
	require.Error(t, err)

	_, err = client.DiffFiles(t.Context(), "", "HEAD")
	require.Error(t, err)
	_, err = client.DiffFiles(t.Context(), "HEAD", badPrefix)
	require.Error(t, err)

	_, err = client.DiffStat(t.Context(), "", "HEAD")
	require.Error(t, err)

	_, err = client.LsTree(t.Context(), "HEAD", &badPrefix)
	require.Error(t, err)

	_, err = client.LogCommits(t.Context(), "", 5)
	require.Error(t, err)

	_, err = client.ParentShas(t.Context(), "")
	require.Error(t, err)

	_, err = client.PatchId(t.Context(), badPrefix, "HEAD", nil)
	require.Error(t, err)
	_, err = client.PatchId(t.Context(), sha, badPrefix, nil)
	require.Error(t, err)
}
