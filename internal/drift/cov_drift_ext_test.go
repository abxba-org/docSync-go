package drift_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/drift"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCOV_DRIFT_RenamedSourceCarriesOldPath(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "core.autocrlf", "false")
	runGit(t, root, "config", "core.eol", "lf")

	oldPath := "src/old_name.ts"
	newPath := "src/new_name.ts"
	writeFileBytes(t, filepath.Join(root, oldPath), []byte("export function x() {}\n"))
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "baseline")
	baselineSha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

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
		SourceSymbols:  []string{},
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
	require.NotNil(t, report.Sources[0].OldPath)
	assert.Equal(t, oldPath, *report.Sources[0].OldPath)
	assert.Equal(t, newPath, report.Sources[0].Path)
}

func TestCOV_DRIFT_ClampDepthBounds(t *testing.T) {
	require.Equal(t, drift.DefaultCommitWalkDepth, drift.ClampCommitWalkDepth(nil))
	require.Equal(t, 1, drift.ClampCommitWalkDepth(intptr(0)))
	require.Equal(t, 1, drift.ClampCommitWalkDepth(intptr(-7)))
	require.Equal(t, drift.MaxCommitWalkDepth, drift.ClampCommitWalkDepth(intptr(5000)))
	require.Equal(t, drift.MaxCommitWalkDepth, drift.ClampCommitWalkDepth(intptr(drift.MaxCommitWalkDepth)))
}

func TestCOV_DRIFT_AnalyzeDriftUnknownHeadRef(t *testing.T) {
	fixture := createDriftFixture(t)
	client := fixture.client(t)
	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &fixture.baseline,
		Head:     "nonexistent-ref",
		Git:      client,
		Claims:   fixture.claims,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.Error(t, err)
}

func TestCOV_DRIFT_AnalyzeDriftUnknownBaselineCommit(t *testing.T) {
	fixture := createDriftFixture(t)
	client := fixture.client(t)
	bad := schemas.Baseline{BaselineCommit: "nonexistent-ref"}
	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &bad,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   fixture.claims,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.Error(t, err)
}

func TestCOV_DRIFT_AnalyzeDriftEmptyScope(t *testing.T) {
	fixture := createDriftFixture(t)
	client := fixture.client(t)
	emptyScope := schemas.Baseline{
		DocPath:        docPath,
		DocContentHash: driftDocHash,
		BaselineCommit: fixture.baselineSha,
		SourceFiles:    []string{},
		SourceSymbols:  []string{},
		ClaimIDs:       []string{},
		VerifiedAt:     driftAt,
		VerifiedByRun:  driftRunID,
		PolicyVersion:  17,
	}
	report, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &emptyScope,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.DriftReportSummary{}, report.Summary)
}

