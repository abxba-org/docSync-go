package gitx_test

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
Git exec layer tests (P1-T01): SystemGitExecutor binary-safety/stdin piping,
argument validation, and failure classification (D14: every failure is a
typed GitExecError — timeout, unknown-ref, missing-rev, exec-failure —
never a silent "no changes").
*/

// runGit runs a raw git command outside the typed wrapper (test fixtures only).
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = fixtureEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return string(out)
}

// fixtureEnv is a deterministic, isolated git environment: no user or system
// config, fixed identity and dates (no wall-clock dependence beyond git).
func fixtureEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"TERM=dumb",
	}
}

// capturedResult mirrors the TS `runResult` helper: a scripted GitRunResult.
func capturedResult(patch *gitx.GitRunResult) gitx.GitRunResult {
	result := gitx.GitRunResult{
		ExitCode: intPtr(0),
		Signal:   "",
		TimedOut: false,
		Stdout:   []byte{},
		Stderr:   []byte{},
	}
	if patch != nil {
		result = *patch
	}
	return result
}

func intPtr(v int) *int { return &v }

// scriptedExecutor returns pre-scripted results in order, then fails
// (TS parity: "script exhausted").
type scriptedExecutor struct {
	results []gitx.GitRunResult
	calls   [][]string
}

func (s *scriptedExecutor) Run(_ context.Context, args []string, opts *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	s.calls = append(s.calls, append([]string(nil), args...))
	if len(s.results) == 0 {
		return gitx.GitRunResult{}, errScriptExhausted
	}
	next := s.results[0]
	s.results = s.results[1:]
	return next, nil
}

var errScriptExhausted = &scriptExhaustedError{}

type scriptExhaustedError struct{}

func (e *scriptExhaustedError) Error() string { return "script exhausted" }

// clientWithScript builds a client over a scripted executor (classification
// through the public surface).
func clientWithScript(script []gitx.GitRunResult) (*gitx.GitClient, []gitx.GitRunResult) {
	executor := &scriptedExecutor{results: append([]gitx.GitRunResult(nil), script...)}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{
		RepoRoot: t_TempDir(),
		Executor: executor,
	})
	if err != nil {
		panic(err)
	}
	return client, script
}

// t_TempDir stands in for the TS non-existent repo root (the client only
// validates absoluteness, and a scripted executor never touches git).
func t_TempDir() string { return os.TempDir() }

func TestSystemGitExecutor_CapturesSuccessfulRunsAsBinarySafeBuffers(t *testing.T) {
	executor := gitx.CreateSystemGitExecutor(nil)
	result, err := executor.Run(context.Background(), []string{"--version"}, nil)
	require.NoError(t, err)

	require.NotNil(t, result.ExitCode)
	assert.Equal(t, 0, *result.ExitCode)
	assert.Contains(t, string(result.Stdout), "git version")
	assert.Equal(t, "", string(result.Stderr))
}

func TestSystemGitExecutor_ReportsNonZeroExitsAsResultsNeverEmptySuccess(t *testing.T) {
	dir := t.TempDir()
	executor := gitx.CreateSystemGitExecutor(nil)
	result, err := executor.Run(context.Background(), []string{"-C", dir, "rev-parse", "--verify", "bogus-ref"}, nil)
	require.NoError(t, err)

	require.NotNil(t, result.ExitCode)
	assert.Equal(t, 128, *result.ExitCode)
	assert.Contains(t, string(result.Stderr), "fatal")
	assert.False(t, result.TimedOut)
}

func TestSystemGitExecutor_PipesStdinIntoPatchIDWithoutAShell(t *testing.T) {
	patch := "diff --git a/f.md b/f.md\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/f.md\n" +
		"+++ b/f.md\n" +
		"@@ -1 +1 @@\n" +
		"-a\n" +
		"+b\n"
	executor := gitx.CreateSystemGitExecutor(nil)
	result, err := executor.Run(context.Background(), []string{"patch-id", "--stable"}, &gitx.GitRunOptions{Stdin: []byte(patch)})
	require.NoError(t, err)

	require.NotNil(t, result.ExitCode)
	assert.Equal(t, 0, *result.ExitCode)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{40} 0{40}$`), strings.TrimSpace(string(result.Stdout)))
}

func TestSystemGitExecutor_TimesOutSlowInvocationsAndReportsThemAsTimedOut(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	// Platform port (DX-P11-3): repo-local line-ending policy so the system
	// gitconfig (core.autocrlf=true) cannot touch this temp repo.
	runGit(t, dir, "config", "core.autocrlf", "false")
	runGit(t, dir, "config", "core.eol", "lf")

	executor := gitx.CreateSystemGitExecutor(nil)
	started := time.Now()
	// Deterministic hang: commit without -m opens the configured editor, which
	// is a `sleep 5` — killed by the executor timeout.
	result, err := executor.Run(context.Background(), []string{
		"-C", dir,
		"-c", "user.name=docsync-test",
		"-c", "user.email=docsync-test@invalid",
		"-c", "core.editor=sleep 5 #",
		"commit",
		"--allow-empty",
	}, &gitx.GitRunOptions{TimeoutMs: 300})
	require.NoError(t, err)

	assert.True(t, result.TimedOut)
	assert.Nil(t, result.ExitCode)
	assert.Less(t, time.Since(started), 4*time.Second)
}

func TestSystemGitExecutor_MaxBufferOverflowIsReportedAsResultNeverSuccess(t *testing.T) {
	executor := gitx.NewSystemGitExecutor(&gitx.SystemGitExecutorOptions{MaxBufferBytes: 16, DefaultTimeoutMs: 30_000})
	result, err := executor.Run(context.Background(), []string{"hash-object", "--stdin-paths"}, &gitx.GitRunOptions{
		Stdin: []byte(strings.Repeat("docs/very/long/path/that/overflows.md\n", 8)),
	})
	// The run is never clean: an overflow is INCONCLUSIVE, not success (D14),
	// and the captured stdout is truncated at the cap.
	require.NoError(t, err)
	assert.Nil(t, result.ExitCode)
	assert.LessOrEqual(t, len(result.Stdout), 16)
	assert.True(t, result.Failed())
}

func TestValidateRefArg_ValidatePathArg_RejectsInvalidArgBeforeExec(t *testing.T) {
	invalid := []string{"-oProxyCommand=x", "--upload-pack=x", "HEAD\x00extra", "HEAD\n", "HEAD\r", ""}
	for _, value := range invalid {
		t.Run("rejects "+strings.TrimSpace(value), func(t *testing.T) {
			assertRejected(t, func() error {
				_, err := gitx.ValidateRefArg(value, "ref")
				return err
			})
			assertRejected(t, func() error {
				_, err := gitx.ValidatePathArg(value, "path")
				return err
			})
		})
	}

	accepted := []string{"HEAD", "main", "v1.2.3^{commit}", "docs/notes.md", "sk-ä táctus.md"}
	for _, value := range accepted {
		t.Run("accepts "+value, func(t *testing.T) {
			got, err := gitx.ValidateRefArg(value, "ref")
			require.NoError(t, err)
			assert.Equal(t, value, got)
			got, err = gitx.ValidatePathArg(value, "path")
			require.NoError(t, err)
			assert.Equal(t, value, got)
		})
	}
}

func assertRejected(t *testing.T, act func() error) {
	t.Helper()
	err := act()
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.Equal(t, gitx.KindInvalidArg, gitErr.Kind)
	assert.Contains(t, err.Error(), "rejected")
	assert.Equal(t, docutil.CodeGit, docutil.CodeOf(err))
}

func TestClassifyGitFailure_MapsStderrPatternsToKindsDeterministically(t *testing.T) {
	cases := []struct {
		text string
		kind gitx.GitExecErrorKind
	}{
		{text: "fatal: ambiguous argument 'bogus': unknown revision or path not in the working tree.", kind: gitx.KindUnknownRef},
		{text: "fatal: Needed a single revision", kind: gitx.KindUnknownRef},
		{text: "fatal: bad revision HEAD~999", kind: gitx.KindUnknownRef},
		{text: "fatal: path 'missing.md' does not exist in 'HEAD'", kind: gitx.KindMissingRev},
		{text: "fatal: Not a valid object name bogus", kind: gitx.KindUnknownRef},
		{text: "error: pathspec 'nope' did not match any file(s) known to git", kind: gitx.KindMissingRev},
		{text: "fatal: disk full while rewriting pack", kind: gitx.KindExecFailure},
		{text: "", kind: gitx.KindExecFailure},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind)+"/"+tc.text, func(t *testing.T) {
			err := gitx.ClassifyGitFailure("rev-parse", capturedResult(&gitx.GitRunResult{
				ExitCode: intPtr(128),
				Stderr:   []byte(tc.text),
			}))
			require.NotNil(t, err)
			assert.Equal(t, tc.kind, err.Kind)
		})
	}
}

func TestClassifyGitFailure_PrefersTimeoutOverPatternsAndHonorsKindHints(t *testing.T) {
	timeoutErr := gitx.ClassifyGitFailure("diff", capturedResult(&gitx.GitRunResult{
		ExitCode: nil,
		Signal:   "SIGTERM",
		TimedOut: true,
		Stderr:   []byte("unknown revision"),
	}))
	assert.Equal(t, gitx.KindTimeout, timeoutErr.Kind)

	hinted := gitx.ClassifyGitFailure("rev-parse", capturedResult(&gitx.GitRunResult{
		ExitCode: intPtr(128),
		Stderr:   []byte("fatal: Needed a single revision"),
	}), gitx.KindMissingRev)
	assert.Equal(t, gitx.KindMissingRev, hinted.Kind)
}

func TestClassifyGitFailure_ExposesBoundedStderrExcerptAndFailingCommand(t *testing.T) {
	err := gitx.ClassifyGitFailure("diff", capturedResult(&gitx.GitRunResult{
		ExitCode: intPtr(128),
		Stderr:   []byte("fatal: " + strings.Repeat("x", 1000)),
	}))
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.Equal(t, "diff", gitErr.Command)
	assert.LessOrEqual(t, len([]rune(gitErr.StderrExcerpt)), 401)
}

func TestGitClientOverScriptedExecutors_ClassifiesInjectedTimeoutAsTimeout(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: nil, Signal: "SIGTERM", TimedOut: true}),
	})
	_, err := client.ResolveRev(context.Background(), "HEAD")
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.Equal(t, gitx.KindTimeout, gitErr.Kind)
}

func TestGitClientOverScriptedExecutors_NeverReportsSpawnedButFailedGitAsEmptyDiff(t *testing.T) {
	client, _ := clientWithScript([]gitx.GitRunResult{
		// First scripted run: valid sha (resolveRev succeeds).
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("0123456789abcdef0123456789abcdef01234567\n")}),
		// Second scripted run: diff fails — diffStat must error, not return [].
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(128), Stderr: []byte("fatal: simulated outage\n")}),
	})

	sha, err := client.ResolveRev(context.Background(), "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", sha)

	stats, err := client.DiffStat(context.Background(), "HEAD", "HEAD")
	require.Error(t, err)
	assert.Nil(t, stats)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.Equal(t, gitx.KindExecFailure, gitErr.Kind)
}
