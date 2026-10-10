package gitpr_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/gitpr.test.ts — the "doc-commit assembly +
// write path (P6-T02)" describe block (INV-10 provenance gate, plan shape, the
// post-index scope assert). The write-path cases use REAL temp git
// repositories exactly like the TS suite; there is no network anywhere.

// caPatchFor builds a doc patch for path (the TS patchFor helper over
// DOC_PATCH_EXAMPLE).
func caPatchFor(path string) schemas.DocPatch {
	patch := schemas.DocPatchExample
	patch.DocPath = path
	return patch
}

// caProvenance is the OK_PROVENANCE fixture: a present record + a run id.
func caProvenance(runID string) gitpr.ProvenanceRef {
	record := map[string]any{"docsync": map[string]any{"verification": "passed"}}
	return gitpr.ProvenanceRef{Record: record, RunID: runID}
}

func TestAssembleDocCommit_inv10ProvenanceGateRefusals(t *testing.T) {
	patches := []schemas.DocPatch{caPatchFor("docs/payments.md")}
	stateUpdates := []gitpr.CommitStateUpdate{{RelPath: ".docsync/state/run.json", Content: "{}"}}
	docContents := map[string]string{"docs/payments.md": "content"}

	// null, undefined and {} are all missing records.
	for name, record := range map[string]any{
		"null":      nil,
		"empty map": map[string]any{},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
				Patches:      patches,
				StateUpdates: stateUpdates,
				Provenance:   gitpr.ProvenanceRef{Record: record, RunID: "run_01J"},
				DocContents:  docContents,
			})
			require.Error(t, err)
			var provenanceErr *gitpr.ProvenanceMissingError
			require.ErrorAs(t, err, &provenanceErr)
			assert.Equal(t, "EDOCSYNC_PROVENANCE_MISSING", provenanceErr.Code())
		})
	}

	// An empty run id is equally a missing provenance ref.
	_, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      patches,
		StateUpdates: stateUpdates,
		Provenance:   gitpr.ProvenanceRef{Record: caProvenance("run_01J").Record, RunID: ""},
		DocContents:  docContents,
	})
	require.Error(t, err)
	var provenanceErr *gitpr.ProvenanceMissingError
	require.ErrorAs(t, err, &provenanceErr)

	// A present record passes the gate and reaches plan emission.
	plan, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      patches,
		StateUpdates: stateUpdates,
		Provenance:   caProvenance("run_01J"),
		DocContents:  docContents,
	})
	require.NoError(t, err)
	assert.Contains(t, plan.Message, "Provenance: run_01J")
}

func TestAssembleDocCommit_planFilesAreExactlyDocsPlusState(t *testing.T) {
	plan, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches: []schemas.DocPatch{
			caPatchFor("docs/payments.md"),
			caPatchFor("docs/api.md"),
		},
		StateUpdates: []gitpr.CommitStateUpdate{
			{RelPath: ".docsync/state/zz.json", Content: "zz"},
			{RelPath: ".docsync/state/aa.json", Content: "aa"},
		},
		Provenance: caProvenance("run_01J"),
		DocContents: map[string]string{
			"docs/payments.md": "PAY",
			"docs/api.md":      "API",
		},
	})
	require.NoError(t, err)

	paths := make([]string, 0, len(plan.Files))
	for _, file := range plan.Files {
		paths = append(paths, file.Path)
	}
	// Sorted (path asc — '.' < 'd') deduped union of doc paths + state relPaths.
	assert.Equal(t, []string{
		".docsync/state/aa.json",
		".docsync/state/zz.json",
		"docs/api.md",
		"docs/payments.md",
	}, paths)

	contents := make([]string, 0, len(plan.Files))
	for _, file := range plan.Files {
		contents = append(contents, file.Content)
	}
	assert.Equal(t, []string{"aa", "zz", "API", "PAY"}, contents)

	assert.Contains(t, plan.Message, "docs: synchronize documentation")
	assert.Contains(t, plan.Message, "- docs/api.md")
	assert.Contains(t, plan.Message, "- .docsync/state/aa.json")
	assert.Contains(t, plan.Message, "Provenance: run_01J")
	assert.Equal(t, gitpr.CommitPlanStats{
		FileCount:      4,
		DocPathCount:   2,
		StatePathCount: 2,
		AddedLines:     schemas.DocPatchExample.Minimality.AddedLines * 2,
		RemovedLines:   schemas.DocPatchExample.Minimality.RemovedLines * 2,
	}, plan.Stats)

	// Dedup: the same doc path twice (identical content) collapses to one file.
	dedup, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches: []schemas.DocPatch{
			caPatchFor("docs/payments.md"),
			caPatchFor("docs/payments.md"),
		},
		StateUpdates: []gitpr.CommitStateUpdate{},
		Provenance:   caProvenance("run_01J"),
		DocContents:  map[string]string{"docs/payments.md": "PAY"},
	})
	require.NoError(t, err)
	require.Len(t, dedup.Files, 1)
	assert.Equal(t, "docs/payments.md", dedup.Files[0].Path)
	assert.Equal(t, 1, dedup.Stats.DocPathCount)

	// Conflicting content for one path is a fail-closed refusal (the same path
	// riding in BOTH channels with differing content).
	assemblyErr := &gitpr.CommitAssemblyError{}
	_, err = gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches: []schemas.DocPatch{caPatchFor(".docsync/state/run.json")},
		StateUpdates: []gitpr.CommitStateUpdate{
			{RelPath: ".docsync/state/run.json", Content: "OTHER"},
		},
		Provenance:  caProvenance("run_01J"),
		DocContents: map[string]string{".docsync/state/run.json": "PAY"},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)
	assert.Equal(t, "EDOCSYNC_COMMIT_PLAN", assemblyErr.Code())

	// Stray state path (outside .docsync/state/) → typed abort.
	_, err = gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{},
		StateUpdates: []gitpr.CommitStateUpdate{{RelPath: "state/run.json", Content: "x"}},
		Provenance:   caProvenance("run_01J"),
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)

	// Empty plan (no patches, no state updates) → typed abort.
	_, err = gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{},
		StateUpdates: []gitpr.CommitStateUpdate{},
		Provenance:   caProvenance("run_01J"),
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)

	// A patch without applied content → typed abort (hunks are not content).
	_, err = gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{caPatchFor("docs/payments.md")},
		StateUpdates: []gitpr.CommitStateUpdate{},
		Provenance:   caProvenance("run_01J"),
		DocContents:  map[string]string{},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)
}

func TestAssembleDocCommit_newlineCarryingInputsAreRefused(t *testing.T) {
	assemblyErr := &gitpr.CommitAssemblyError{}

	// A doc_path carrying a newline cannot ride in a commit message.
	newlinePatch := caPatchFor("docs/payments.md")
	newlinePatch.DocPath = "docs/payments\n.md"
	_, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{newlinePatch},
		StateUpdates: []gitpr.CommitStateUpdate{},
		Provenance:   caProvenance("run_01J"),
		DocContents:  map[string]string{"docs/payments\n.md": "PAY"},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)

	// A state relPath carrying a newline is refused the same way.
	_, err = gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches: []schemas.DocPatch{},
		StateUpdates: []gitpr.CommitStateUpdate{
			{RelPath: ".docsync/state/run\n.json", Content: "{}"},
		},
		Provenance: caProvenance("run_01J"),
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)

	// An empty doc_path / relPath is equally a commit-message hazard.
	_, err = gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{caPatchFor("")},
		StateUpdates: []gitpr.CommitStateUpdate{},
		Provenance:   caProvenance("run_01J"),
		DocContents:  map[string]string{"": "PAY"},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)

	// A run id carrying a newline is refused by buildCommitMessage.
	present := caProvenance("run_01J")
	_, err = gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{caPatchFor("docs/payments.md")},
		StateUpdates: []gitpr.CommitStateUpdate{},
		Provenance:   gitpr.ProvenanceRef{Record: present.Record, RunID: "run\n01J"},
		DocContents:  map[string]string{"docs/payments.md": "PAY"},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)
}

/* --------------------------------------------------------------- write path */

// caHead returns the fixture's current HEAD sha.
func caHead(t *testing.T, repo *bfRepo) string {
	t.Helper()
	sha, err := repo.client.ResolveRev(context.Background(), "HEAD")
	require.NoError(t, err)
	return sha
}

// TestWriteDocCommit_postIndexScopeAssert ports the write-path suite: the
// happy path (exactly the plan staged + committed), the all-no-op refusal, and
// the foreign-staged abort (the post-index scope assert).
func TestWriteDocCommit_postIndexScopeAssert(t *testing.T) {
	repo := bfNewRepo(t)
	bfWrite(t, repo, ".docsync/state/run.json", "{\"v\":1}\n")
	bfCommitAll(t, repo, "initial")

	plan, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{},
		StateUpdates: []gitpr.CommitStateUpdate{{RelPath: ".docsync/state/run.json", Content: "{\"v\":2}\n"}},
		Provenance:   caProvenance("run_01J"),
	})
	require.NoError(t, err)

	write := func(path, content string) error {
		return os.WriteFile(filepath.Join(repo.root, path), []byte(content), 0o644)
	}

	// Happy path: a compliant seam writes exactly the plan → one commit,
	// staged exactly the planned paths.
	result, err := gitpr.WriteDocCommit(context.Background(), repo.client, plan, write)
	require.NoError(t, err)
	assert.Equal(t, []string{".docsync/state/run.json"}, result.Staged)

	committedSha := caHead(t, repo)
	assert.Equal(t, committedSha, result.Sha)
	onDisk, err := os.ReadFile(filepath.Join(repo.root, ".docsync/state/run.json"))
	require.NoError(t, err)
	assert.Equal(t, "{\"v\":2}\n", string(onDisk))

	// All writes were no-ops → nothing staged → typed refusal, no commit.
	_, err = gitpr.WriteDocCommit(context.Background(), repo.client, plan, write)
	require.Error(t, err)
	var emptyErr *gitpr.EmptyCommitError
	require.ErrorAs(t, err, &emptyErr)
	assert.Equal(t, "EDOCSYNC_EMPTY_COMMIT", emptyErr.Code())
	assert.Equal(t, committedSha, caHead(t, repo))

	// Foreign staged content: the buggy seam writes beyond the plan AND its
	// stray write reaches the index (the leftover-index hazard the post-index
	// assert exists for) → typed abort BEFORE the commit; HEAD unchanged.
	plan2, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{},
		StateUpdates: []gitpr.CommitStateUpdate{{RelPath: ".docsync/state/run.json", Content: "{\"v\":3}\n"}},
		Provenance:   caProvenance("run_01J"),
	})
	require.NoError(t, err)
	buggyApply := func(path, content string) error {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo.root, path)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(repo.root, path), []byte(content), 0o644); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(repo.root, "docs"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(repo.root, "docs", "rogue.md"), []byte("foreign"), 0o644); err != nil {
			return err
		}
		cmd := exec.Command("git", "add", "docs/rogue.md")
		cmd.Dir = repo.root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo.root)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add docs/rogue.md: %v: %s", err, out)
		}
		return nil
	}

	_, err = gitpr.WriteDocCommit(context.Background(), repo.client, plan2, buggyApply)
	require.Error(t, err)
	var scopeErr *gitpr.CommitScopeError
	require.ErrorAs(t, err, &scopeErr)
	assert.Equal(t, "EDOCSYNC_COMMIT_SCOPE", scopeErr.Code())
	assert.Equal(t, []string{"docs/rogue.md"}, scopeErr.ForeignPaths)
	assert.Equal(t, committedSha, caHead(t, repo))

	// The assert is stable: the abort repeats on a second attempt.
	_, err = gitpr.WriteDocCommit(context.Background(), repo.client, plan2, buggyApply)
	require.Error(t, err)
	require.ErrorAs(t, err, &scopeErr)
	assert.Equal(t, []string{"docs/rogue.md"}, scopeErr.ForeignPaths)
	assert.Equal(t, committedSha, caHead(t, repo))
}

// TestWriteDocCommit_argumentRefusals ports the seam-level guards.
func TestWriteDocCommit_argumentRefusals(t *testing.T) {
	repo := bfNewRepo(t)
	bfWrite(t, repo, "README.md", "r")
	bfCommitAll(t, repo, "initial")

	plan, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{},
		StateUpdates: []gitpr.CommitStateUpdate{{RelPath: ".docsync/state/run.json", Content: "{}"}},
		Provenance:   caProvenance("run_01J"),
	})
	require.NoError(t, err)

	noop := func(string, string) error { return nil }

	// no apply seam → typed refusal
	_, err = gitpr.WriteDocCommit(context.Background(), repo.client, plan, nil)
	require.Error(t, err)
	var assemblyErr *gitpr.CommitAssemblyError
	require.ErrorAs(t, err, &assemblyErr)
	assert.Contains(t, err.Error(), "apply disk seam")

	// an empty plan files list → typed refusal
	_, err = gitpr.WriteDocCommit(context.Background(), repo.client, gitpr.CommitPlan{}, noop)
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)
	assert.Contains(t, err.Error(), "files list is empty")

	// a duplicated plan file → typed refusal
	duplicated := gitpr.CommitPlan{
		Message: "docs: synchronize documentation",
		Files: []gitpr.CommitPlanFile{
			{Path: ".docsync/state/run.json", Content: "{}"},
			{Path: ".docsync/state/run.json", Content: "{}"},
		},
	}
	_, err = gitpr.WriteDocCommit(context.Background(), repo.client, duplicated, noop)
	require.Error(t, err)
	require.ErrorAs(t, err, &assemblyErr)
	assert.Contains(t, err.Error(), "duplicate plan file")
}

// TestWriteDocCommit_seamFailuresPropagate: a failing disk seam aborts the
// write path (nothing is staged, nothing is committed).
func TestWriteDocCommit_seamFailuresPropagate(t *testing.T) {
	repo := bfNewRepo(t)
	bfWrite(t, repo, "README.md", "r")
	bfCommitAll(t, repo, "initial")

	plan, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{},
		StateUpdates: []gitpr.CommitStateUpdate{{RelPath: ".docsync/state/run.json", Content: "{}"}},
		Provenance:   caProvenance("run_01J"),
	})
	require.NoError(t, err)

	boom := errors.New("disk full")
	_, err = gitpr.WriteDocCommit(context.Background(), repo.client, plan, func(string, string) error {
		return boom
	})
	require.Error(t, err)
	assert.Same(t, boom, err, "seam failures propagate verbatim")

	head := caHead(t, repo)
	staged, err := repo.client.CachedFilePaths(context.Background())
	require.NoError(t, err)
	assert.Empty(t, staged)
	assert.Equal(t, head, caHead(t, repo))
}

// TestWriteDocCommit_carriesDocsFromPatches ports the doc-carrying write path
// (the state-channel case is covered above).
func TestWriteDocCommit_carriesDocsFromPatches(t *testing.T) {
	repo := bfNewRepo(t)
	bfWrite(t, repo, "README.md", "r")
	bfCommitAll(t, repo, "initial")

	plan, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{caPatchFor("docs/api.md")},
		StateUpdates: []gitpr.CommitStateUpdate{{RelPath: ".docsync/state/run.json", Content: "{}"}},
		Provenance:   caProvenance("run_01J"),
		DocContents:  map[string]string{"docs/api.md": "API v2\n"},
	})
	require.NoError(t, err)
	require.Len(t, plan.Files, 2)

	write := func(path, content string) error {
		full := filepath.Join(repo.root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(full, []byte(content), 0o644)
	}
	result, err := gitpr.WriteDocCommit(context.Background(), repo.client, plan, write)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"docs/api.md", ".docsync/state/run.json"}, result.Staged)

	api, err := os.ReadFile(filepath.Join(repo.root, "docs", "api.md"))
	require.NoError(t, err)
	assert.Equal(t, "API v2\n", string(api))

	// the commit message rides in the real commit
	log := bfRunGit(t, repo.root, "log", "-1", "--pretty=%B")
	assert.True(t, strings.Contains(log, "docs: synchronize documentation"))
	assert.True(t, strings.Contains(log, "Provenance: run_01J"))
	assert.True(t, strings.Contains(log, "- docs/api.md"))
}

// TestIsPresentRecord_shapes ports the TS isPresentRecord predicate through
// AssertProvenancePresent: only a non-nil, non-array object with keys counts.
func TestIsPresentRecord_shapes(t *testing.T) {
	present := []struct {
		name   string
		record any
		want   bool
	}{
		{"nil", nil, false},
		{"nil typed map", map[string]any(nil), false},
		{"empty map", map[string]any{}, false},
		{"empty struct", struct{}{}, false},
		{"empty slice", []any{}, false},
		{"string", "docsync", false},
		{"number", 7, false},
		{"bool", true, false},
		{"nonempty map", map[string]any{"verification": "passed"}, true},
		{"nested map", map[string]any{"docsync": map[string]any{"verification": "passed"}}, true},
		{"struct", struct{ Verification string }{"passed"}, true},
		{"pointer to struct", &struct{ Verification string }{"passed"}, true},
		{"nil pointer", (*struct{ Verification string })(nil), false},
	}
	for _, testCase := range present {
		t.Run(testCase.name, func(t *testing.T) {
			err := gitpr.AssertProvenancePresent(gitpr.ProvenanceRef{
				Record: testCase.record,
				RunID:  "run_01J",
			})
			if testCase.want {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var provenanceErr *gitpr.ProvenanceMissingError
			require.ErrorAs(t, err, &provenanceErr)
		})
	}
}

// TestTypedCommitErrors_errorSurface pins the coded error surface of every
// commit-assembly error type.
func TestTypedCommitErrors_errorSurface(t *testing.T) {
	repo := bfNewRepo(t)
	bfWrite(t, repo, "README.md", "r")
	bfCommitAll(t, repo, "initial")
	plan, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{},
		StateUpdates: []gitpr.CommitStateUpdate{{RelPath: ".docsync/state/run.json", Content: "{}"}},
		Provenance:   caProvenance("run_01J"),
	})
	require.NoError(t, err)

	buggyApply := func(path, content string) error {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo.root, path)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(repo.root, path), []byte(content), 0o644); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(repo.root, "docs"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(repo.root, "docs", "rogue.md"), []byte("foreign"), 0o644); err != nil {
			return err
		}
		cmd := exec.Command("git", "add", "docs/rogue.md")
		cmd.Dir = repo.root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo.root)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add docs/rogue.md: %v: %s", err, out)
		}
		return nil
	}
	compliantWrite := func(path, content string) error {
		full := filepath.Join(repo.root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(full, []byte(content), 0o644)
	}
	_, emptyErr := gitpr.WriteDocCommit(context.Background(), repo.client, plan, compliantWrite)
	require.NoError(t, emptyErr)

	surfaces := []struct {
		name string
		err  error
		code string
	}{
		{
			name: "provenance",
			err:  gitpr.AssertProvenancePresent(gitpr.ProvenanceRef{}),
			code: "EDOCSYNC_PROVENANCE_MISSING",
		},
		{
			name: "plan",
			err: func() error {
				_, err := gitpr.AssembleDocCommit(gitpr.AssembleDocCommitInput{
					Patches:      []schemas.DocPatch{},
					StateUpdates: []gitpr.CommitStateUpdate{},
					Provenance:   caProvenance("run_01J"),
				})
				return err
			}(),
			code: "EDOCSYNC_COMMIT_PLAN",
		},
		{
			name: "scope",
			err: func() error {
				_, err := gitpr.WriteDocCommit(context.Background(), repo.client, plan, buggyApply)
				return err
			}(),
			code: "EDOCSYNC_COMMIT_SCOPE",
		},
		{
			name: "empty",
			err: func() error {
				_, err := gitpr.WriteDocCommit(context.Background(), repo.client, plan, compliantWrite)
				return err
			}(),
			code: "EDOCSYNC_EMPTY_COMMIT",
		},
	}

	// The scope error carries its foreign-path evidence and the error surface.
	var scopeErr *gitpr.CommitScopeError
	require.ErrorAs(t, surfaces[2].err, &scopeErr)
	assert.Equal(t, []string{"docs/rogue.md"}, scopeErr.ForeignPaths)
	require.Error(t, scopeErr.Unwrap())
	assert.Equal(t, "EDOCSYNC_COMMIT_SCOPE", scopeErr.Code())
	assert.NotEmpty(t, scopeErr.Error())
}
