package baselines_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/require"
)

// selectiveGitExecutor runs the real system git through a delegating executor,
// except for invocations the test opts into failing (matched by argv), letting
// a fixture repo stand while ONE git run fails — the D14 "no ladder rung
// swallows a git error" seam.
type selectiveGitExecutor struct {
	base    gitx.GitExecutor
	failRun func(args []string) *gitx.GitRunResult
}

func newSelectiveGitExecutor() *selectiveGitExecutor {
	return &selectiveGitExecutor{base: gitx.CreateSystemGitExecutor(nil)}
}

func (s *selectiveGitExecutor) Run(ctx context.Context, args []string, opts *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	if s.failRun != nil {
		if res := s.failRun(args); res != nil {
			return *res, nil
		}
	}
	return s.base.Run(ctx, args, opts)
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func joinedArgs(args []string) string { return strings.Join(args, "\x00") }

func gitFailure(code int, stderr string) *gitx.GitRunResult {
	return &gitx.GitRunResult{ExitCode: &code, Stderr: []byte(stderr)}
}

func requireGitExecError(t *testing.T, err error, wantKind ...gitx.GitExecErrorKind) *gitx.GitExecError {
	t.Helper()
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.True(t, errors.As(err, &gitErr), "want *GitExecError, got %T: %v", err, err)
	if len(wantKind) > 0 {
		require.Equal(t, wantKind[0], gitErr.Kind)
	}
	return gitErr
}

func TestLadderTreeShaError_BaselineTreeLookupFailsClosed(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\n")
	baseline := repo.commitAll(t, "baseline")
	repo.write(t, "src/a.ts", "two\n")
	head := repo.commitAll(t, "head")

	exec := newSelectiveGitExecutor()
	exec.failRun = func(args []string) *gitx.GitRunResult {
		if hasArg(args, "rev-parse") && strings.Contains(joinedArgs(args), "^{tree}") {
			return gitFailure(128, "fatal: injected tree failure\n")
		}
		return nil
	}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: exec})
	require.NoError(t, err)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baseline, "docs/payments.md")),
		Head:     head,
		Git:      client,
	})
	requireGitExecError(t, err, gitx.KindExecFailure)
}

func TestLadderTreeShaError_HeadTreeLookupFailsClosed(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\n")
	baseline := repo.commitAll(t, "baseline")
	repo.write(t, "src/a.ts", "two\n")
	head := repo.commitAll(t, "head")

	exec := newSelectiveGitExecutor()
	exec.failRun = func(args []string) *gitx.GitRunResult {
		if hasArg(args, "rev-parse") && strings.Contains(joinedArgs(args), head+"^{tree}") {
			return gitFailure(128, "fatal: injected head tree failure\n")
		}
		return nil
	}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: exec})
	require.NoError(t, err)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baseline, "docs/payments.md")),
		Head:     head,
		Git:      client,
	})
	requireGitExecError(t, err, gitx.KindExecFailure)
}

func TestLadderMergeBaseError_FailsClosed(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\n")
	baseline := repo.commitAll(t, "baseline")
	repo.write(t, "src/a.ts", "two\n")
	head := repo.commitAll(t, "head")

	exec := newSelectiveGitExecutor()
	exec.failRun = func(args []string) *gitx.GitRunResult {
		if hasArg(args, "merge-base") {
			return gitFailure(128, "fatal: injected merge-base failure\n")
		}
		return nil
	}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: exec})
	require.NoError(t, err)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baseline, "docs/payments.md")),
		Head:     head,
		Git:      client,
	})
	requireGitExecError(t, err, gitx.KindExecFailure)
}

// TestLadderParentShasError_NonUnknownRefPropagates covers the rung-3b branch
// where ParentShas fails with a NON-unknown-ref error: the failure must
// propagate (D14), not silently fall through to orphaned.
func TestLadderParentShasError_NonUnknownRefPropagates(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "main root")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feature")
	repo.git(t, "checkout", "-q", "--orphan", "isolated")
	repo.git(t, "rm", "-q", "-rf", ".")
	repo.write(t, "other.ts", "independent\n")
	head := repo.commitAll(t, "isolated root")

	exec := newSelectiveGitExecutor()
	exec.failRun = func(args []string) *gitx.GitRunResult {
		if hasArg(args, "rev-list") {
			return gitFailure(128, "fatal: injected rev-list failure\n")
		}
		return nil
	}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: exec})
	require.NoError(t, err)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      client,
	})
	requireGitExecError(t, err, gitx.KindExecFailure)
}

// TestLadderParentShasError_UnknownRefFallsToOrphan covers the documented
// exception (D12): an unresolvable first parent (unknown-ref) means "no
// comparable patch" and the baseline is treated as orphaned — never a swallowed
// environmental error.
func TestLadderParentShasError_UnknownRefFallsToOrphan(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "main root")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feature")
	repo.git(t, "checkout", "-q", "--orphan", "isolated")
	repo.git(t, "rm", "-q", "-rf", ".")
	repo.write(t, "other.ts", "independent\n")
	head := repo.commitAll(t, "isolated root")

	exec := newSelectiveGitExecutor()
	exec.failRun = func(args []string) *gitx.GitRunResult {
		if hasArg(args, "rev-list") {
			return gitFailure(128, "fatal: bad revision\n")
		}
		return nil
	}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: exec})
	require.NoError(t, err)

	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      client,
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunOrphanedSuspect, resolution.Run)
	require.False(t, resolution.BaselineValid)
	require.Contains(t, resolution.Evidence, "its own patch is not computable")
}

func TestLadderPatchIdError_BaselinePatchFailsClosed(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "main root")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feature")
	repo.git(t, "checkout", "-q", "--orphan", "isolated")
	repo.git(t, "rm", "-q", "-rf", ".")
	repo.write(t, "other.ts", "independent\n")
	head := repo.commitAll(t, "isolated root")

	exec := newSelectiveGitExecutor()
	exec.failRun = func(args []string) *gitx.GitRunResult {
		if hasArg(args, "diff") {
			return gitFailure(128, "fatal: injected diff failure\n")
		}
		return nil
	}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: exec})
	require.NoError(t, err)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      client,
	})
	requireGitExecError(t, err, gitx.KindExecFailure)
}

func TestLadderLogCommitsError_FailsClosed(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "main root")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feature")
	repo.git(t, "checkout", "-q", "--orphan", "isolated")
	repo.git(t, "rm", "-q", "-rf", ".")
	repo.write(t, "other.ts", "independent\n")
	head := repo.commitAll(t, "isolated root")

	exec := newSelectiveGitExecutor()
	exec.failRun = func(args []string) *gitx.GitRunResult {
		if hasArg(args, "log") {
			return gitFailure(128, "fatal: injected log failure\n")
		}
		return nil
	}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: exec})
	require.NoError(t, err)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      client,
	})
	requireGitExecError(t, err, gitx.KindExecFailure)
}

// TestLadderPatchIdError_CandidatePatchFailsClosed covers the walk-loop branch:
// the baseline's own patch computes fine, head's log walks fine, but a
// candidate commit's patch-id run fails → the failure propagates.
func TestLadderPatchIdError_CandidatePatchFailsClosed(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "base")
	repo.git(t, "checkout", "-q", "-b", "feature")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feat")
	repo.git(t, "checkout", "-q", "main")
	repo.write(t, "src/other.ts", "other\n")
	repo.commitAll(t, "unrelated work")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	head := repo.commitAll(t, "re-land")

	diffCalls := 0
	exec := newSelectiveGitExecutor()
	exec.failRun = func(args []string) *gitx.GitRunResult {
		if hasArg(args, "diff") {
			diffCalls++
			if diffCalls == 2 {
				return gitFailure(128, "fatal: injected candidate diff failure\n")
			}
		}
		return nil
	}
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: exec})
	require.NoError(t, err)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      client,
	})
	requireGitExecError(t, err, gitx.KindExecFailure)
}

// TestBaselineStore_UpdatePathWriteErrorPropagates covers the §6.1 update
// branch: the stored record passes BOTH guards, so the CAS write is attempted,
// and a backend write failure must propagate rather than degrade.
func TestBaselineStore_UpdatePathWriteErrorPropagates(t *testing.T) {
	stored := makeBaseline(func(b *schemas.Baseline) {
		b.DocContentHash = "sha256:stored-content"
		b.BaselineCommit = "abc"
	})
	boom := errors.New("backend write failed on update")
	store := baselines.NewBaselineStore(&erroringBackend{
		read: func(string) (state.StateRead[*schemas.Baseline], error) {
			return state.StateRead[*schemas.Baseline]{Exists: true, Content: &stored, Hash: "sha256:file"}, nil
		},
		write: func(string, schemas.Baseline, *string) (state.CasWriteResult, error) {
			return state.CasWriteResult{}, boom
		},
	})

	_, err := store.Upsert(stored, baselines.BaselineGuards{
		ExpectedDocContentHash: stored.DocContentHash,
		ExpectedBaselineCommit: stored.BaselineCommit,
	})
	require.ErrorIs(t, err, boom)
}
