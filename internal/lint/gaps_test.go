package lint_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/lint"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

func TestManifestLintErrorUnwrap(t *testing.T) {
	invalid := schemas.Manifest{Version: 0}
	_, err := lint.LintManifest(lint.LintManifestParams{Manifest: invalid, RepoFiles: nil})
	require.Error(t, err)

	var lintErr *lint.ManifestLintError
	require.True(t, errors.As(err, &lintErr))

	unwrapped := lintErr.Unwrap()
	require.NotNil(t, unwrapped)
	var coded *docutil.Error
	require.True(t, errors.As(unwrapped, &coded))
	assert.Equal(t, lint.CodeLintInvalid, coded.Code)
}

func TestWalkRepoFilesErrorPropagates(t *testing.T) {
	_, err := lint.WalkRepoFiles("/nonexistent/path/that/does/not/exist")
	require.Error(t, err)
}

func TestWalkRepoFilesVisitsFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "real.txt", "hello")
	files, err := lint.WalkRepoFiles(root)
	require.NoError(t, err)
	assert.Contains(t, files, "real.txt")
}

func TestLintManifestFromRepoWalkError(t *testing.T) {
	manifest := schemas.Manifest{
		Version:   1,
		Documents: []schemas.ManifestEntry{},
	}
	_, err := lint.LintManifestFromRepo(manifest, "/nonexistent/path")
	require.Error(t, err)
}

func TestLintManifestFromRepoWithDocFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/a.ts", "")
	writeFile(t, root, "docs/a.md", "# A\n")
	writeFile(t, root, "docs/extra.md", "# Extra\n")

	manifest := schemas.Manifest{
		Version:   1,
		Documents: []schemas.ManifestEntry{{Path: "docs/a.md", Sources: []string{"src/**"}}},
	}
	report, err := lint.LintManifestFromRepo(manifest, root)
	require.NoError(t, err)
	assert.True(t, report.OK)
	require.Len(t, report.Findings, 1)
	assert.Equal(t, lint.LintUnmappedDocDir, report.Findings[0].Code)
	assert.Equal(t, "docs/extra.md", report.Findings[0].Path)
}

func TestLintManifestSortOrder(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/b.md", Sources: []string{"ghost-b/**"}},
			{Path: "docs/a.md", Sources: []string{"ghost-a/**"}},
		},
	}
	report, err := lint.LintManifest(lint.LintManifestParams{
		Manifest:  manifest,
		RepoFiles: nil,
	})
	require.NoError(t, err)
	assert.Equal(t, lint.LintDeadEntry, report.Findings[0].Code)
	assert.Equal(t, lint.LintNonexistentDocPath, report.Findings[1].Code)
	assert.Equal(t, lint.LintZeroMatchGlob, report.Findings[2].Code)
	assert.Equal(t, "docs/a.md", report.Findings[0].Path)
}

func TestLintManifestInvalidManifestMessage(t *testing.T) {
	invalid := schemas.Manifest{Version: 0}
	_, err := lint.LintManifest(lint.LintManifestParams{Manifest: invalid, RepoFiles: nil})
	require.Error(t, err)
	var lintErr *lint.ManifestLintError
	require.ErrorAs(t, err, &lintErr)
	assert.NotEmpty(t, lintErr.Issues)
	assert.Contains(t, err.Error(), "lint runs on valid manifests only")
}
