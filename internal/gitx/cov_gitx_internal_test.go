package gitx

// Internal-package coverage for the tiny exec-layer helpers and the
// SystemGitExecutor branches that external tests cannot name directly.

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func execGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	require.NoError(t, cmd.Run())
}

func TestCOV_INTERNAL_GITX_writeJSONStringEscapes(t *testing.T) {
	var b strings.Builder
	writeJSONString(&b, "q\"b\\f\x0c\b\n\r\t\x01plain\u00e9")
	assert.Equal(t, `"q\"b\\f\f\b\n\r\t\u0001plainé"`, b.String())
}

func TestCOV_INTERNAL_GITX_firstRunesTruncates(t *testing.T) {
	assert.Equal(t, "ab", firstRunes("abcdef", 2))
	assert.Equal(t, "a😀cd", firstRunes("a😀cdef", 4))
	assert.Equal(t, "short", firstRunes("short", 99))
}

func TestCOV_INTERNAL_GITX_signalNames(t *testing.T) {
	assert.Equal(t, "SIGHUP", signalName(syscall.SIGHUP))
	assert.Equal(t, "SIGINT", signalName(syscall.SIGINT))
	assert.Equal(t, "SIGTERM", signalName(syscall.SIGTERM))
	assert.Equal(t, "SIGKILL", signalName(syscall.SIGKILL))
	assert.Equal(t, syscall.SIGURG.String(), signalName(syscall.SIGURG))
}

func TestCOV_INTERNAL_GITX_terminatedByNilState(t *testing.T) {
	_, ok := terminatedBy(nil)
	assert.False(t, ok)
}

func TestCOV_INTERNAL_GITX_isolatedEnvFiltersHijackVars(t *testing.T) {
	t.Setenv("GIT_DIR", "hijacked")
	t.Setenv("GIT_WORK_TREE", "hijacked")
	t.Setenv("GIT_INDEX_FILE", "hijacked")
	t.Setenv("GIT_COMMON_DIR", "hijacked")
	t.Setenv("PERSIST_ME", "yes")
	env := isolatedEnv()
	for _, entry := range env {
		key := entry
		if i := strings.Index(entry, "="); i >= 0 {
			key = entry[:i]
		}
		for _, bad := range hijackEnvVars {
			assert.NotEqual(t, bad, key)
		}
	}
	found := false
	for _, entry := range env {
		if entry == "PERSIST_ME=yes" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestCOV_INTERNAL_GITX_executorNilCtxAndNegativeTimeout(t *testing.T) {
	executor := CreateSystemGitExecutor(nil)

	// NIL context → background (the guard branch).
	result, err := executor.Run(nil, []string{"--version"}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.ExitCode)
	assert.Equal(t, 0, *result.ExitCode)

	// Non-positive timeout → the default is applied (no immediate death).
	result, err = executor.Run(t.Context(), []string{"--version"}, &GitRunOptions{TimeoutMs: -5})
	require.NoError(t, err)
	assert.Equal(t, 0, *result.ExitCode)
}

func TestCOV_INTERNAL_GITX_executorStderrFabricatedOnOverflow(t *testing.T) {
	executor := NewSystemGitExecutor(&SystemGitExecutorOptions{MaxBufferBytes: 16})
	result, err := executor.Run(t.Context(), []string{"hash-object", "--stdin"}, &GitRunOptions{
		Stdin: []byte("some object content"),
	})
	require.NoError(t, err)
	// An overflow is INCONCLUSIVE, never a clean run: exit code is null and
	// the empty stderr is fabricated with the failure detail (D14).
	assert.Nil(t, result.ExitCode)
	assert.True(t, result.Failed())
	assert.True(t, strings.Contains(string(result.Stderr), "maxBuffer length exceeded"))
	assert.LessOrEqual(t, len(result.Stdout), 16)
}

func TestCOV_INTERNAL_GITX_executorTimesOutWithSignal(t *testing.T) {
	dir := t.TempDir()
	execGit(t, dir, "init", "-q", "-b", "main")
	executor := CreateSystemGitExecutor(nil)
	start := time.Now()
	// Deterministic hang: commit without -m opens the configured editor
	// (a sleep), which the executor kills with SIGTERM after the timeout.
	result, err := executor.Run(t.Context(), []string{
		"-C", dir,
		"-c", "user.name=docsync-test",
		"-c", "user.email=docsync-test@invalid",
		"-c", "core.editor=sleep 5 #",
		"commit",
		"--allow-empty",
	}, &GitRunOptions{TimeoutMs: 300})
	require.NoError(t, err)
	assert.True(t, result.TimedOut)
	assert.Nil(t, result.ExitCode)
	if result.Signal != "" {
		assert.Equal(t, "SIGTERM", result.Signal)
	}
	assert.Less(t, time.Since(start), 4*time.Second)
}

func TestCOV_INTERNAL_GITX_cappedBufferOverflowContinues(t *testing.T) {
	buf := &cappedBuffer{max: 4}
	_, _ = buf.Write([]byte("abcd"))
	_, _ = buf.Write([]byte("efgh"))
	assert.True(t, buf.overflow)
	data, overflow := buf.bytes()
	assert.Equal(t, "abcd", string(data))
	assert.True(t, overflow)
}

func TestCOV_INTERNAL_GITX_stderrExcerptLimitFallback(t *testing.T) {
	assert.Equal(t, "line", StderrExcerpt([]byte("line\n"), 0))
}

func TestCOV_INTERNAL_GITX_executorDefaultTimeoutFallback(t *testing.T) {
	executor := NewSystemGitExecutor(&SystemGitExecutorOptions{DefaultTimeoutMs: -1})
	result, err := executor.Run(t.Context(), []string{"--version"}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.ExitCode)
	assert.Equal(t, 0, *result.ExitCode)
}

func TestCOV_INTERNAL_GITX_mapStatusLetterCopyLetter(t *testing.T) {
	status, err := mapStatusLetter("C")
	require.NoError(t, err)
	assert.Equal(t, DiffStatusRenamed, status)
}

func TestCOV_INTERNAL_GITX_chunkAtOutOfRange(t *testing.T) {
	assert.Equal(t, "", chunkAt([]string{"a", "b"}, 5))
}
