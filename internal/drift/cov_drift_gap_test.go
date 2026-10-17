package drift_test

// Coverage-gap tests for internal/drift: remaining reachable branches in
// AnalyzeDrift (rename paths, symbol sort ties, added-after-baseline scope)
// and in the sweep's state discipline (transition error propagation).

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/drift"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gapSharedSymbol = "sharedToken"

// TestCOV_DRIFT_RenamedChangedSourceCarriesSymbolsAndSourceChanged:
// a RENAMED recorded source (content unchanged) is still processed in the
// symbol loop through its baseline old path, and a claim anchored on the
// renamed file's blob (which survives the rename) reports source-changed.
func TestCOV_DRIFT_RenamedChangedSourceCarriesSymbolsAndSourceChanged(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "core.autocrlf", "false")
	runGit(t, root, "config", "core.eol", "lf")

	oldPath := "src/a.ts"
	newPath := "src/b.ts"
	content := "export const " + gapSharedSymbol + " = 1;\n"
	writeFileBytes(t, filepath.Join(root, oldPath), []byte(content))
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "baseline")
	baselineSha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	blobV1 := strings.TrimSpace(runGit(t, root, "rev-parse", baselineSha+":"+oldPath))

	runGit(t, root, "mv", oldPath, newPath)
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "rename")
	headSha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	baseline := schemas.Baseline{
		DocPath:        docPath,
		DocContentHash: driftDocHash,
		BaselineCommit: baselineSha,
		SourceFiles:    []string{newPath},
		SourceSymbols:  []string{gapSharedSymbol},
		ClaimIDs:       []string{"c_renamed"},
		VerifiedAt:     driftAt,
		VerifiedByRun:  driftRunID,
		PolicyVersion:  17,
	}
	claim := schemas.Claim{
		ID:     "c_renamed",
		Text:   "the token is a contract",
		Anchor: schemas.ClaimAnchor{File: docPath, StartLine: 1, EndLine: 1},
		Source: schemas.ClaimSource{File: newPath, BlobSHA: blobV1},
		Status: schemas.ClaimStatusVerified,
	}
	report, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &baseline,
		Head:     headSha,
		Git:      client,
		Claims:   []schemas.Claim{claim},
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.NoError(t, err)

	// The renamed changed source carries its old path AND the symbol fact
	// (evaluated against the baseline old-path blob).
	require.Len(t, report.Sources, 1)
	require.NotNil(t, report.Sources[0].OldPath)
	assert.Equal(t, oldPath, *report.Sources[0].OldPath)
	symbol := report.Symbols
	require.Len(t, symbol, 1)
	assert.Equal(t, schemas.DriftSymbolPresent, symbol[0].Kind)

	// The claim's backing blob STILL exists at head under the new path: the
	// file changed (moved) but the recorded bytes are intact → source-changed.
	require.Len(t, report.Claims, 1)
	assert.Equal(t, schemas.DriftClaimSourceChanged, report.Claims[0].Verdict)
	assert.Nil(t, report.Claims[0].ProposedStatus)
}

// TestCOV_DRIFT_SymbolSortTieBrokenByPath: the same recorded symbol appears in
// two CHANGED sources, so the deterministic sort must break the name tie by
// path (the comparator's second clause).
func TestCOV_DRIFT_SymbolSortTieBrokenByPath(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "core.autocrlf", "false")
	runGit(t, root, "config", "core.eol", "lf")

	writeFileBytes(t, filepath.Join(root, "src/x.ts"), []byte("export const "+gapSharedSymbol+" = 1;\n"))
	writeFileBytes(t, filepath.Join(root, "src/y.ts"), []byte("export const "+gapSharedSymbol+" = 1;\n"))
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "baseline")
	baselineSha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	writeFileBytes(t, filepath.Join(root, "src/x.ts"), []byte("export const "+gapSharedSymbol+" = 2;\n"))
	writeFileBytes(t, filepath.Join(root, "src/y.ts"), []byte("export const "+gapSharedSymbol+" = 3;\n"))
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "drift")
	headSha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	baseline := schemas.Baseline{
		DocPath:        docPath,
		DocContentHash: driftDocHash,
		BaselineCommit: baselineSha,
		SourceFiles:    []string{"src/x.ts", "src/y.ts"},
		SourceSymbols:  []string{gapSharedSymbol},
		ClaimIDs:       []string{},
		VerifiedAt:     driftAt,
		VerifiedByRun:  driftRunID,
		PolicyVersion:  17,
	}
	report, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &baseline,
		Head:     headSha,
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.NoError(t, err)

	require.Len(t, report.Symbols, 2)
	assert.Equal(t, "src/x.ts", report.Symbols[0].Path)
	assert.Equal(t, "src/y.ts", report.Symbols[1].Path)
	// Both files still contain the symbol text at head (present in both);
	// the assertion that matters is the deterministic name-then-path sort.
	assert.Equal(t, report.Symbols[0].Name, report.Symbols[1].Name)
	assert.Equal(t, schemas.DriftSymbolPresent, report.Symbols[0].Kind)
}

// TestCOV_DRIFT_RecordedSourceAddedAfterBaseline: a source listed in the
// baseline scope but absent at the baseline commit (added later) is reported
// as added and skipped by the symbol loop — the record-status Added guard.
func TestCOV_DRIFT_RecordedSourceAddedAfterBaseline(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "core.autocrlf", "false")
	runGit(t, root, "config", "core.eol", "lf")

	writeFileBytes(t, filepath.Join(root, "stable.txt"), []byte("keep\n"))
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "baseline")
	baselineSha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	writeFileBytes(t, filepath.Join(root, "src/added.ts"), []byte("export const fresh = true;\n"))
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "add src")
	headSha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	baseline := schemas.Baseline{
		DocPath:        docPath,
		DocContentHash: driftDocHash,
		BaselineCommit: baselineSha,
		SourceFiles:    []string{"src/added.ts"},
		SourceSymbols:  []string{"fresh"},
		ClaimIDs:       []string{},
		VerifiedAt:     driftAt,
		VerifiedByRun:  driftRunID,
		PolicyVersion:  17,
	}
	report, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &baseline,
		Head:     headSha,
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.NoError(t, err)
	require.Len(t, report.Sources, 1)
	assert.Equal(t, schemas.DriftSourceStatusAdded, report.Sources[0].Status)
	// An added file records no symbols by design.
	assert.Empty(t, report.Symbols)
}

// TestCOV_DRIFT_SweepApplyTransitionErrorPropagates: with Apply=true the
// sweep only flips through the claim state machine; a store that does not own
// the claim surfaces the machine's typed missing-claim error (fail closed,
// never swallowed into a flip/conflict).
func TestCOV_DRIFT_SweepApplyTransitionErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	emptyStore := newClaimStore(t)

	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: []schemas.Claim{fixture.claims[0]},
		Git:    fixture.client(t),
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
		Apply:  true,
		Store:  emptyStore,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "it does not exist")
}

var _ = claims.ClaimUpsert{}

// TestCOV_DRIFT_SweepUnknownHeadFailsClosed: an unresolvable head ref is a
// typed git failure propagated from the sweep (D14), never a guessed verdict.
func TestCOV_DRIFT_SweepUnknownHeadFailsClosed(t *testing.T) {
	fixture := createDriftFixture(t)
	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: fixture.claims,
		Git:    fixture.client(t),
		Head:   "nonexistent-ref",
		RunID:  driftRunID,
		At:     driftAt,
	})
	require.Error(t, err)
}
