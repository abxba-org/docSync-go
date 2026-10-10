package gitx_test

// Parser-surface hardening for the git exec/client layer (P1-T01; V-P1-10
// failure semantics). The TypeScript suite pins the parser error paths through
// the same public surface; these cases drive malformed `--raw -z` /
// `ls-tree -z` / `--numstat -z` output and the log walk through a scripted
// executor so every failure the parsers fail closed on is covered: a
// malformed record is a typed GitExecError, never a silent empty result.
// Also covers the SIGKILL classification of a signal-killed git process.

import (
	"context"
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawPatchScript is the (raw, text) scripted pair for a malformed-raw test:
// the raw half is the case under test, the text half is one matching section.
func rawPatchScript(raw string) []gitx.GitRunResult {
	return []gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte(raw)}),
		capturedResult(&gitx.GitRunResult{
			ExitCode: intPtr(0),
			Stdout:   []byte("diff --git docs/a.md docs/a.md\n--- a/docs/a.md\n+++ b/docs/a.md\n"),
		}),
	}
}

func TestDiffFilesParser_FailsClosedOnMalformedRawRecords(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	base, head := "a000000000000000000000000000000000000000", "b000000000000000000000000000000000000000"
	ctx := context.Background()

	cases := []struct {
		name     string
		raw      string
		contains string
	}{
		{
			name:     "a record not starting with ':' is malformed",
			raw:      "garbage-record\x00docs/a.md\x00",
			contains: "malformed --raw -z record: \"garbage-record\"",
		},
		{
			name:     "a record without a status field is malformed",
			raw:      ":100644 100644 " + sha + " " + sha[:40] + "\x00docs/a.md\x00",
			contains: "malformed --raw -z record (no status)",
		},
		{
			name:     "a record without a path is malformed",
			raw:      ":100644 100644 " + sha + " " + sha + " M\x00\x00",
			contains: "malformed --raw -z record: missing path",
		},
		{
			name:     "a rename without a new path is malformed",
			raw:      ":100644 100644 " + sha + " " + sha + " R100\x00docs/old.md\x00\x00",
			contains: "malformed rename record: missing new path",
		},
		{
			name:     "a rename with a non-numeric score is malformed",
			raw:      ":100644 100644 " + sha + " " + sha + " Rabc\x00docs/old.md\x00docs/new.md\x00",
			contains: "malformed rename score: \"Rabc\"",
		},
		{
			name:     "an unmapped status letter fails closed (never a guessed status)",
			raw:      ":100644 100644 " + sha + " " + sha + " X\x00docs/a.md\x00",
			contains: "unmapped diff status letter: \"X\"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := clientWithScript(rawPatchScript(tc.raw))
			records, err := client.DiffFiles(ctx, base, head)
			require.Error(t, err)
			assert.Nil(t, records)
			var gitErr *gitx.GitExecError
			require.ErrorAs(t, err, &gitErr)
			assert.Equal(t, gitx.KindExecFailure, gitErr.Kind)
			assert.Contains(t, gitErr.Msg, tc.contains)
		})
	}
}

func TestDiffFilesParser_SectionOrderTripwireFailsClosed(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	base, head := "a000000000000000000000000000000000000000", "b000000000000000000000000000000000000000"
	// The raw record says docs/a.md, the text section header says otherwise —
	// the tripwire must fail closed rather than attach a mismatched patch.
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{
			ExitCode: intPtr(0),
			Stdout:   []byte(":100644 100644 " + sha + " " + sha + " M\x00docs/a.md\x00"),
		}),
		capturedResult(&gitx.GitRunResult{
			ExitCode: intPtr(0),
			Stdout:   []byte("diff --git docs/other.md docs/other.md\n--- a/docs/other.md\n+++ b/docs/other.md\n"),
		}),
	})
	records, err := client.DiffFiles(context.Background(), base, head)
	require.Error(t, err)
	assert.Nil(t, records)
	assert.Contains(t, err.Error(), "does not mention raw record path")
}

func TestLsTreeParser_FailsClosedOnMalformedRecords(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	ctx := context.Background()

	t.Run("a record without a tab is malformed", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("100644 blob " + sha + "\x00")}),
		})
		entries, err := client.LsTree(ctx, "HEAD", nil)
		require.Error(t, err)
		assert.Nil(t, entries)
		assert.Contains(t, err.Error(), "malformed ls-tree record:")
	})

	t.Run("a record with incomplete metadata is malformed", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("100644 blob\tdocs/a.md\x00")}),
		})
		entries, err := client.LsTree(ctx, "HEAD", nil)
		require.Error(t, err)
		assert.Nil(t, entries)
		assert.Contains(t, err.Error(), "malformed ls-tree meta:")
	})
}

func TestDiffStatParser_NumstatRecordsAndMalformed(t *testing.T) {
	base, head := "a000000000000000000000000000000000000000", "b000000000000000000000000000000000000000"
	ctx := context.Background()

	t.Run("a rename numstat record carries both paths", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			capturedResult(&gitx.GitRunResult{
				ExitCode: intPtr(0),
				Stdout:   []byte("3\t2\t\x00docs/old.md\x00docs/new.md\x00"),
			}),
		})
		stats, err := client.DiffStat(ctx, base, head)
		require.NoError(t, err)
		require.Len(t, stats, 1)
		assert.Equal(t, "docs/new.md", stats[0].Path)
		require.NotNil(t, stats[0].OldPath)
		assert.Equal(t, "docs/old.md", *stats[0].OldPath)
		require.NotNil(t, stats[0].Additions)
		require.NotNil(t, stats[0].Deletions)
		assert.Equal(t, 3, *stats[0].Additions)
		assert.Equal(t, 2, *stats[0].Deletions)
		assert.False(t, stats[0].Binary)
	})

	t.Run("a rename record missing its path pair is malformed", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			capturedResult(&gitx.GitRunResult{
				ExitCode: intPtr(0),
				Stdout:   []byte("3\t2\t\x00docs/old.md\x00"),
			}),
		})
		stats, err := client.DiffStat(ctx, base, head)
		require.Error(t, err)
		assert.Nil(t, stats)
		assert.Contains(t, err.Error(), "malformed numstat rename record: missing path pair")
	})

	t.Run("a record with too few fields is malformed", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("1\t2\x00")}),
		})
		stats, err := client.DiffStat(ctx, base, head)
		require.Error(t, err)
		assert.Nil(t, stats)
		assert.Contains(t, err.Error(), "malformed numstat record:")
	})

	t.Run("non-numeric counts fail closed with the TS JSON-stringified pair", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("x\ty\tdocs/a.md\x00")}),
		})
		stats, err := client.DiffStat(ctx, base, head)
		require.Error(t, err)
		assert.Nil(t, stats)
		assert.Contains(t, err.Error(), `malformed numstat counts: ["x","y"]`)
	})
}

func TestLogCommitsParser_FailsClosedOnMalformedOutput(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	ctx := context.Background()

	t.Run("maxCount must be a positive integer (pre-exec guard)", func(t *testing.T) {
		client, _ := clientWithScript(nil)
		commits, err := client.LogCommits(ctx, "HEAD", 0)
		require.Error(t, err)
		assert.Nil(t, commits)
		var gitErr *gitx.GitExecError
		require.ErrorAs(t, err, &gitErr)
		assert.Equal(t, gitx.KindInvalidArg, gitErr.Kind)
		assert.Contains(t, gitErr.Msg, "git log maxCount must be a positive integer (got 0)")
	})

	t.Run("a non-sha commit token is rejected", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte("not-a-sha\n")}),
		})
		commits, err := client.LogCommits(ctx, "HEAD", 10)
		require.Error(t, err)
		assert.Nil(t, commits)
		assert.Contains(t, err.Error(), "git log produced a non-sha token: not-a-sha")
	})

	t.Run("a non-sha parent token is rejected", func(t *testing.T) {
		client, _ := clientWithScript([]gitx.GitRunResult{
			capturedResult(&gitx.GitRunResult{ExitCode: intPtr(0), Stdout: []byte(sha + " not-a-parent\n")}),
		})
		commits, err := client.LogCommits(ctx, "HEAD", 10)
		require.Error(t, err)
		assert.Nil(t, commits)
		assert.Contains(t, err.Error(), "git log produced a non-sha parent: not-a-parent")
	})
}

func TestRebaseBranch_FailsClosedOnANonConflictFailure(t *testing.T) {
	// A rebase failure that is NOT a conflict is a typed failure — never an
	// improvised recovery and never a silently "clean" result.
	client, _ := clientWithScript([]gitx.GitRunResult{
		capturedResult(&gitx.GitRunResult{
			ExitCode: intPtr(128),
			Stderr:   []byte("fatal: unable to start rebase\n"),
		}),
	})
	result, err := client.RebaseBranch(context.Background(), "feature", "main")
	require.Error(t, err)
	assert.False(t, result.Ok)
	assert.False(t, result.Conflicted)
	var gitErr *gitx.GitExecError
	require.ErrorAs(t, err, &gitErr)
	assert.Equal(t, gitx.KindExecFailure, gitErr.Kind)
	assert.Contains(t, err.Error(), "unable to start rebase")
}

// TestSystemGitExecutor_SignalKilledProcessIsReportedWithNamedSignal covers
// the signal-name vocabulary beyond the timeout SIGTERM: a git process killed
// by SIGKILL must come back as ExitCode null + the signal name.
func TestSystemGitExecutor_SignalKilledProcessIsReportedWithNamedSignal(t *testing.T) {
	repo := newRepo(t)
	repo.git(t, "init", "-q", "-b", "main")
	repo.git(t, "config", "core.autocrlf", "false")
	repo.git(t, "config", "core.eol", "lf")

	executor := gitx.CreateSystemGitExecutor(nil)
	// The editor runs as git's child; killing $PPID kills git itself.
	result, err := executor.Run(context.Background(), []string{
		"-C", repo.root,
		"-c", "user.name=docsync-test",
		"-c", "user.email=docsync-test@invalid",
		"-c", "core.editor=kill -9 $PPID #",
		"commit",
		"--allow-empty",
	}, &gitx.GitRunOptions{TimeoutMs: 10_000})
	require.NoError(t, err)

	assert.Nil(t, result.ExitCode)
	assert.Equal(t, "SIGKILL", result.Signal)
	assert.False(t, result.TimedOut)
	assert.True(t, result.Failed())
}

// TestGitExecError_Surface covers the typed error's accessor and extractor:
// the docutil code, the exact TS message text, and AsGitExecError's negative
// path (a plain error is never a git failure).
func TestGitExecError_Surface(t *testing.T) {
	err := gitx.ClassifyGitFailure("diff", capturedResult(&gitx.GitRunResult{
		ExitCode: intPtr(128),
		Stderr:   []byte("fatal: bad revision\n"),
	}))
	assert.Equal(t, docutil.CodeGit, err.Code())
	assert.Equal(t, gitx.EDOCSYNCGitExec, docutil.CodeOf(err))
	assert.Equal(t, "git diff failed (kind inference: exit=128): fatal: bad revision", err.Msg)

	extracted, ok := gitx.AsGitExecError(err)
	require.True(t, ok)
	assert.Equal(t, err.Kind, extracted.Kind)

	_, ok = gitx.AsGitExecError(errors.New("some other failure"))
	assert.False(t, ok, "a non-git error must never extract as a git failure")
}
