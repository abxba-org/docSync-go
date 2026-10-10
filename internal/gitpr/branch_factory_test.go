package gitpr_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/gitpr.test.ts — the "branch factory (P6-T01)"
// describe block (INV-1 guard, deterministic naming, the P6-T13 disambiguator
// scan). The disambiguator cases use REAL temp git repositories exactly like
// the TS suite; there is no network anywhere.

const bfFullSha = "0123456789abcdef0123456789abcdef01234567"

// bfRepo is a temp repository fixture.
type bfRepo struct {
	root   string
	client *gitx.GitClient
}

// bfRunGit runs a pinned git command in dir (identity/date pinned so every
// fixture is reproducible).
func bfRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+dir,
		"GIT_AUTHOR_NAME=docsync-test",
		"GIT_AUTHOR_EMAIL=docsync-test@invalid",
		"GIT_COMMITTER_NAME=docsync-test",
		"GIT_COMMITTER_EMAIL=docsync-test@invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// bfWrite writes a file in the fixture repo.
func bfWrite(t *testing.T, repo *bfRepo, relPath, content string) {
	t.Helper()
	full := filepath.Join(repo.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

// bfNewRepo makes an initialized temp repo (no commits — each test commits its
// own changes, exactly like the TS makeRepo fixture).
func bfNewRepo(t *testing.T) *bfRepo {
	t.Helper()
	root := t.TempDir()
	bfRunGit(t, root, "init", "-b", "main")
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	return &bfRepo{root: root, client: client}
}

// bfCommitAll commits everything currently on disk as one change.
func bfCommitAll(t *testing.T, repo *bfRepo, message string) string {
	t.Helper()
	bfRunGit(t, repo.root, "add", "-A")
	bfRunGit(t, repo.root, "commit", "-m", message)
	sha, err := repo.client.ResolveRev(context.Background(), "HEAD")
	require.NoError(t, err)
	return sha
}

// bfSeed makes the fixture's two-commit history: README 'r' then
// docs/payments.md (returning both shas).
func bfSeed(t *testing.T, repo *bfRepo) (firstSha, headSha string) {
	t.Helper()
	bfWrite(t, repo, "README.md", "r")
	firstSha = bfCommitAll(t, repo, "initial")
	bfWrite(t, repo, "docs/payments.md", "# payments\n")
	headSha = bfCommitAll(t, repo, "docs update")
	require.NotEqual(t, firstSha, headSha)
	return firstSha, headSha
}

func TestBranchNameForSourcePr_isDeterministic(t *testing.T) {
	expected := "docsync/pr-42-0123456"
	name, err := gitpr.BranchNameForSourcePr(42, bfFullSha)
	require.NoError(t, err)
	assert.Equal(t, expected, name)

	// Same inputs → same name, always (regardless of case).
	lower, err := gitpr.BranchNameForSourcePr(42, strings.ToUpper(bfFullSha))
	require.NoError(t, err)
	assert.Equal(t, expected, lower)

	again, err := gitpr.BranchNameForSourcePr(42, bfFullSha)
	require.NoError(t, err)
	assert.Equal(t, expected, again)

	// Invalid inputs are typed failures, never silent names.
	for _, call := range []func() error{
		func() error { _, err := gitpr.BranchNameForSourcePr(0, bfFullSha); return err },
		func() error { _, err := gitpr.BranchNameForSourcePr(42, "81af92e"); return err },
		func() error { _, err := gitpr.BranchNameForRun("bad run id"); return err },
		func() error { _, err := gitpr.BranchNameForRun(""); return err },
	} {
		err := call()
		require.Error(t, err)
		var branchErr *gitpr.BranchNameError
		require.ErrorAs(t, err, &branchErr)
		assert.Equal(t, "EDOCSYNC_BRANCH_NAME", branchErr.Code())
	}

	name, err = gitpr.BranchNameForRun("run_01J9ZK6X8A")
	require.NoError(t, err)
	assert.Equal(t, "docsync/run_01J9ZK6X8A", name)
}

func TestAssertNotDefaultBranch_inv1HardFails(t *testing.T) {
	for _, c := range []struct{ ref, defaultBranch string }{
		{"main", "refs/heads/main"},
		{"refs/heads/main", "main"},
		{"refs/heads/main", "refs/heads/main"},
	} {
		err := gitpr.AssertNotDefaultBranch(c.ref, c.defaultBranch)
		require.Error(t, err)
		var guard *gitpr.DefaultBranchRefError
		require.ErrorAs(t, err, &guard)
		assert.Equal(t, "EDOCSYNC_DEFAULT_BRANCH_REF", guard.Code())
		assert.Equal(t, c.ref, guard.Ref)
		assert.Contains(t, err.Error(), "INV-1 hard guard")
		assert.Contains(t, err.Error(), "never proceed")
	}
	// Non-default refs pass.
	assert.NoError(t, gitpr.AssertNotDefaultBranch("docsync/pr-42-0123456", "main"))
	assert.NoError(t, gitpr.AssertNotDefaultBranch("develop", "main"))
	assert.NoError(t, gitpr.AssertNotDefaultBranch("feature/docs", "refs/heads/main"))

	require.Error(t, gitpr.AssertNotDefaultBranch("", "main"))
	require.Error(t, gitpr.AssertNotDefaultBranch("main", ""))
}

// TestAssertNotDefaultBranch_refsRemotesNormalizationQuirk pins the exact TS
// normalizer behavior: a `refs/remotes/` default branch is stripped with the
// 11-character REFS_HEADS_PREFIX length (not its own 13) — a TS quirk
// reproduced verbatim for behavior preservation; no caller or test depends on
// the remote form.
func TestAssertNotDefaultBranch_refsRemotesNormalizationQuirk(t *testing.T) {
	// 'refs/remotes/origin/main'.slice(11) === 's/origin/main'
	err := gitpr.AssertNotDefaultBranch("refs/heads/main", "refs/remotes/origin/main")
	require.NoError(t, err, "the TS 11-char strip makes this ref not the default")
	err = gitpr.AssertNotDefaultBranch("s/origin/main", "refs/remotes/origin/main")
	require.Error(t, err)
	var guard *gitpr.DefaultBranchRefError
	require.ErrorAs(t, err, &guard)
	assert.Equal(t, "s/origin/main", guard.DefaultBranch)
}

// TestBranchFactory_inv1HardFailsBeforeAnyGitWrite ports the wired-guard case:
// the INV-1 refusal fires BEFORE any git write, so the branch is absent.
func TestBranchFactory_inv1HardFailsBeforeAnyGitWrite(t *testing.T) {
	repo := bfNewRepo(t)
	bfWrite(t, repo, "README.md", "r")
	bfCommitAll(t, repo, "initial")
	factory := gitpr.CreateBranchFactory(repo.client)

	_, err := factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      repo.root,
		RunID:         "run_x",
		HeadSha:       bfFullSha,
		DefaultBranch: "docsync/run_x",
	})
	require.Error(t, err)
	var guard *gitpr.DefaultBranchRefError
	require.ErrorAs(t, err, &guard)
	assert.Equal(t, "docsync/run_x", guard.Ref)
	assert.Equal(t, "docsync/run_x", guard.DefaultBranch)
	// the ref is defaulted against the bare form too
	_, err = factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      repo.root,
		RunID:         "run_x",
		HeadSha:       bfFullSha,
		DefaultBranch: "refs/heads/docsync/run_x",
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &guard)

	// NO write happened: the branch does not exist.
	_, err = repo.client.ResolveRev(context.Background(), "refs/heads/docsync/run_x")
	require.Error(t, err)
}

func TestBranchFactory_rejectsInvalidInputs(t *testing.T) {
	repo := bfNewRepo(t)
	factory := gitpr.CreateBranchFactory(repo.client)

	_, err := factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      "relative/repo",
		RunID:         "run_x",
		HeadSha:       bfFullSha,
		DefaultBranch: "main",
	})
	require.Error(t, err)
	var branchErr *gitpr.BranchNameError
	require.ErrorAs(t, err, &branchErr)
	assert.Contains(t, err.Error(), "absolute path")

	for _, runID := range []string{"bad run id", "", ".hidden", "../escape", "a~b", "probe/../escape"} {
		_, err := factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
			RepoRoot:      repo.root,
			RunID:         runID,
			HeadSha:       bfFullSha,
			DefaultBranch: "main",
		})
		require.Error(t, err, "runId %q must be refused", runID)
		require.ErrorAs(t, err, &branchErr)
	}

	for _, headSha := range []string{"81af92e", "not-a-sha", strings.Repeat("z", 40), ""} {
		_, err := factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
			RepoRoot:      repo.root,
			RunID:         "run_x",
			HeadSha:       headSha,
			DefaultBranch: "main",
		})
		require.Error(t, err, "headSha %q must be refused", headSha)
		require.ErrorAs(t, err, &branchErr)
	}
}

// TestBranchFactory_existingBranchDifferentShaGetsDisambiguator ports the
// P6-T13 case (clean create, idempotent re-run, deterministic -2 collision).
func TestBranchFactory_existingBranchDifferentShaGetsDisambiguator(t *testing.T) {
	repo := bfNewRepo(t)
	_, headSha := bfSeed(t, repo)

	name, err := gitpr.BranchNameForSourcePr(42, headSha)
	require.NoError(t, err)
	assert.Equal(t, "main", bfRunGit(t, repo.root, "branch", "--show-current"))

	factory := gitpr.CreateBranchFactory(repo.client)
	sourcePr := 42
	input := gitpr.CreateDocBranchInput{
		RepoRoot:      repo.root,
		SourcePr:      &sourcePr,
		RunID:         "run_x",
		HeadSha:       headSha,
		DefaultBranch: "main",
	}

	// Clean create: branch at the run's head sha.
	result, err := factory.CreateDocBranch(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, name, result.Branch)
	assert.True(t, result.Created)
	tip, err := repo.client.ResolveRev(context.Background(), "refs/heads/"+name)
	require.NoError(t, err)
	assert.Equal(t, headSha, tip)

	// Idempotent re-run: same inputs, branch already at the head sha.
	result, err = factory.CreateDocBranch(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, name, result.Branch)
	assert.False(t, result.Created)

	// Collision (P6-T13): the same name exists at a DIFFERENT sha → the
	// deterministic disambiguator -2, first free.
	other := bfNewRepo(t)
	otherFirst, otherHead := bfSeed(t, other)
	otherName, err := gitpr.BranchNameForSourcePr(42, otherHead)
	require.NoError(t, err)
	bfRunGit(t, other.root, "branch", otherName, otherFirst)

	otherFactory := gitpr.CreateBranchFactory(other.client)
	otherInput := gitpr.CreateDocBranchInput{
		RepoRoot:      other.root,
		SourcePr:      &sourcePr,
		RunID:         "run_x",
		HeadSha:       otherHead,
		DefaultBranch: "main",
	}
	result, err = otherFactory.CreateDocBranch(context.Background(), otherInput)
	require.NoError(t, err)
	assert.Equal(t, otherName+"-2", result.Branch)
	assert.True(t, result.Created)
	tip, err = other.client.ResolveRev(context.Background(), "refs/heads/"+otherName+"-2")
	require.NoError(t, err)
	assert.Equal(t, otherHead, tip)

	// The disambiguated branch is idempotent on re-run too.
	result, err = otherFactory.CreateDocBranch(context.Background(), otherInput)
	require.NoError(t, err)
	assert.Equal(t, otherName+"-2", result.Branch)
	assert.False(t, result.Created)
}

// TestBranchFactory_runScopedNameWithoutSourcePr covers the run-form name.
func TestBranchFactory_runScopedNameWithoutSourcePr(t *testing.T) {
	repo := bfNewRepo(t)
	bfWrite(t, repo, "README.md", "r")
	headSha := bfCommitAll(t, repo, "initial")
	factory := gitpr.CreateBranchFactory(repo.client)

	result, err := factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      repo.root,
		RunID:         "run_2d3f",
		HeadSha:       headSha,
		DefaultBranch: "main",
	})
	require.NoError(t, err)
	assert.Equal(t, "docsync/run_2d3f", result.Branch)
	assert.True(t, result.Created)
}

// TestBranchFactory_exhaustingTheDisambiguatorScan is fail-closed: every
// deterministic candidate occupied at differing shas produces a typed
// exhaustion error (never a silent fallback).
func TestBranchFactory_exhaustingTheDisambiguatorScan(t *testing.T) {
	repo := bfNewRepo(t)
	firstSha, headSha := bfSeed(t, repo)

	name, err := gitpr.BranchNameForSourcePr(42, headSha)
	require.NoError(t, err)
	// Occupy the base name and every disambiguator (name-2 … name-64) at a
	// different sha.
	candidates := []string{name}
	for i := 2; i <= gitpr.MAX_DISAMBIGUATOR_CANDIDATES; i++ {
		candidates = append(candidates, name+"-"+strconv.Itoa(i))
	}
	for _, candidate := range candidates {
		bfRunGit(t, repo.root, "branch", candidate, firstSha)
	}

	factory := gitpr.CreateBranchFactory(repo.client)
	sourcePr := 42
	_, err = factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      repo.root,
		SourcePr:      &sourcePr,
		RunID:         "run_x",
		HeadSha:       headSha,
		DefaultBranch: "main",
	})
	require.Error(t, err)
	var branchErr *gitpr.BranchNameError
	require.ErrorAs(t, err, &branchErr)
	assert.Contains(t, err.Error(), "no free branch name")
	assert.Contains(t, err.Error(), name)
}

// TestBranchFactory_delegatingMethods pins the factory surface parity (the
// pure naming functions are the package-level ones; the factory delegates).
func TestBranchFactory_delegatingMethods(t *testing.T) {
	repo := bfNewRepo(t)
	factory := gitpr.CreateBranchFactory(repo.client)

	name, err := factory.BranchNameForSourcePr(7, bfFullSha)
	require.NoError(t, err)
	assert.Equal(t, "docsync/pr-7-0123456", name)

	runName, err := factory.BranchNameForRun("run_x")
	require.NoError(t, err)
	assert.Equal(t, "docsync/run_x", runName)

	require.NoError(t, factory.AssertNotDefaultBranch("docsync/run_x", "main"))
	require.Error(t, factory.AssertNotDefaultBranch("main", "refs/heads/main"))

	assert.Same(t, repo.client, factory.Git())
}

// TestBranchFactory_longInputsAreEchoedBounded pins the 64-character echo
// bound on hostile inputs (a giant ref can never flood an error message).
func TestBranchFactory_longInputsAreEchoedBounded(t *testing.T) {
	repo := bfNewRepo(t)
	factory := gitpr.CreateBranchFactory(repo.client)
	longRepoRoot := "relative/" + strings.Repeat("a", 200)

	_, err := factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      longRepoRoot,
		RunID:         "run_x",
		HeadSha:       bfFullSha,
		DefaultBranch: "main",
	})
	require.Error(t, err)
	var branchErr *gitpr.BranchNameError
	require.ErrorAs(t, err, &branchErr)
	assert.Contains(t, err.Error(), "absolute path")
	assert.Less(t, len(err.Error()), 300, "the echoed excerpt is bounded")
	assert.NotContains(t, err.Error(), strings.Repeat("a", 200))

	// a hostile run id carrying control characters renders through the JSON
	// string escaping
	_, err = factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      repo.root,
		RunID:         "bad\nid\t\x00here",
		HeadSha:       bfFullSha,
		DefaultBranch: "main",
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &branchErr)
	assert.Contains(t, err.Error(), `\n`)
	assert.Contains(t, err.Error(), `\t`)
	assert.Contains(t, err.Error(), `\u0000`)

	// quotes and backslashes are escaped exactly like JSON.stringify
	_, err = factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      repo.root,
		RunID:         `quote"back\slash`,
		HeadSha:       bfFullSha,
		DefaultBranch: "main",
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &branchErr)
	assert.Contains(t, err.Error(), `\"`)
	assert.Contains(t, err.Error(), `\\`)

	// the head-sha echo is bounded too
	_, err = factory.CreateDocBranch(context.Background(), gitpr.CreateDocBranchInput{
		RepoRoot:      repo.root,
		RunID:         "run_x",
		HeadSha:       strings.Repeat("z", 300),
		DefaultBranch: "main",
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &branchErr)
	assert.Less(t, len(err.Error()), 400)
}

// TestTypedBranchErrors_errorSurface pins the coded error surface of both
// branch-factory error types.
func TestTypedBranchErrors_errorSurface(t *testing.T) {
	_, err := gitpr.BranchNameForSourcePr(0, bfFullSha)
	require.Error(t, err)
	var branchErr *gitpr.BranchNameError
	require.ErrorAs(t, err, &branchErr)
	assert.Equal(t, "EDOCSYNC_BRANCH_NAME", branchErr.Code())
	require.Error(t, branchErr.Unwrap())
	assert.Contains(t, err.Error(), "EDOCSYNC_BRANCH_NAME")

	err = gitpr.AssertNotDefaultBranch("main", "refs/heads/main")
	require.Error(t, err)
	var guard *gitpr.DefaultBranchRefError
	require.ErrorAs(t, err, &guard)
	assert.Equal(t, "EDOCSYNC_DEFAULT_BRANCH_REF", guard.Code())
	require.Error(t, guard.Unwrap())
	assert.Contains(t, err.Error(), "doc branches must never target the default branch")
}
