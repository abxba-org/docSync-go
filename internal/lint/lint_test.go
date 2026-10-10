package lint_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/lint"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

func codesOf(findings []lint.LintFinding) []lint.LintFindingCode {
	out := make([]lint.LintFindingCode, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Code)
	}
	return out
}

func TestLintManifest_ZeroMatchGlobAndNonexistentDocWarn(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/real.md", Sources: []string{"src/ghost/**"}},
			{Path: "docs/ghost.md", Sources: []string{"src/payments/**"}},
		},
	}
	report, err := lint.LintManifest(lint.LintManifestParams{
		Manifest:  manifest,
		RepoFiles: []string{"src/payments/pay.ts", "docs/real.md", "README.md"},
	})
	require.NoError(t, err)
	require.True(t, report.OK)
	require.Len(t, report.Findings, 2)
	require.Equal(t, []lint.LintFindingCode{lint.LintNonexistentDocPath, lint.LintZeroMatchGlob}, codesOf(report.Findings))
	for _, f := range report.Findings {
		require.Equal(t, lint.LintSeverityWarning, f.Severity)
	}
	// Exact message + target of the zero-match warning (V-P0-15).
	zm := report.Findings[1]
	require.Equal(t, lint.LintZeroMatchGlob, zm.Code)
	require.Equal(t, "docs/real.md", zm.Path)
	require.Equal(t, "src/ghost/**", zm.Target)
	require.Equal(t, `sources glob "src/ghost/**" matches zero repository files today (V-P0-15: warning — the run proceeds, annotated)`, zm.Detail)
}

func TestLintManifest_DeadEntryOnceWhenAllSourcesZeroMatchAndDocAbsent(t *testing.T) {
	manifest := schemas.Manifest{
		Version:   1,
		Documents: []schemas.ManifestEntry{{Path: "docs/gone.md", Sources: []string{"nowhere/**", "also-nowhere/**"}}},
	}
	report, err := lint.LintManifest(lint.LintManifestParams{Manifest: manifest, RepoFiles: []string{"src/a.ts"}})
	require.NoError(t, err)
	require.True(t, report.OK)

	deadCount, zeroCount := 0, 0
	var dead lint.LintFinding
	for _, f := range report.Findings {
		switch f.Code {
		case lint.LintDeadEntry:
			deadCount++
			dead = f
		case lint.LintZeroMatchGlob:
			zeroCount++
		}
	}
	require.Equal(t, 1, deadCount)
	require.Equal(t, 2, zeroCount)
	require.Equal(t, "manifest entry has zero signal: all 2 source glob(s) match nothing and the document path does not exist — a removal candidate (V-P0-15: warning — the run proceeds, annotated)", dead.Detail)
}

func TestLintManifest_UnmappedDocsUnderDocsDir(t *testing.T) {
	manifest := schemas.Manifest{
		Version:   1,
		Documents: []schemas.ManifestEntry{{Path: "docs/payments.md", Sources: []string{"src/**"}}},
	}
	docFiles := []string{"docs/payments.md", "docs/extra.md", "README.md"}
	report, err := lint.LintManifest(lint.LintManifestParams{
		Manifest:  manifest,
		RepoFiles: []string{"src/a.ts", "docs/payments.md", "docs/extra.md", "README.md"},
		DocFiles:  &docFiles,
	})
	require.NoError(t, err)
	require.Len(t, report.Findings, 1)
	require.Equal(t, lint.LintUnmappedDocDir, report.Findings[0].Code)
	require.Equal(t, "docs/extra.md", report.Findings[0].Path)
	require.Contains(t, report.Findings[0].Detail, "D7")
	require.Contains(t, report.Findings[0].Detail, "unknown coverage")
}

func TestLintManifest_SkipsCoverageScanWhenDocFilesAbsent(t *testing.T) {
	manifest := schemas.Manifest{
		Version:   1,
		Documents: []schemas.ManifestEntry{{Path: "docs/payments.md", Sources: []string{"src/**"}}},
	}
	report, err := lint.LintManifest(lint.LintManifestParams{
		Manifest:  manifest,
		RepoFiles: []string{"src/a.ts", "docs/payments.md", "docs/extra.md"},
	})
	require.NoError(t, err)
	require.Len(t, report.Findings, 0)
}

func TestLintManifest_ZeroFindingsForFullyMappedValidManifest(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/payments.md", Sources: []string{"src/payments/**"}},
			{Path: "docs/api.md", Sources: []string{"src/api/**"}},
		},
	}
	docFiles := []string{"docs/payments.md", "docs/api.md"}
	report, err := lint.LintManifest(lint.LintManifestParams{
		Manifest:  manifest,
		RepoFiles: []string{"src/payments/a.ts", "src/api/b.ts", "docs/payments.md", "docs/api.md"},
		DocFiles:  &docFiles,
	})
	require.NoError(t, err)
	require.Equal(t, lint.LintReport{Findings: []lint.LintFinding{}, OK: true}, report)
}

func TestLintManifest_SortsFindingsByPathThenCode(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/b.md", Sources: []string{"ghost-b/**"}},
			{Path: "docs/a.md", Sources: []string{"ghost-a/**"}},
		},
	}
	report, err := lint.LintManifest(lint.LintManifestParams{Manifest: manifest, RepoFiles: nil})
	require.NoError(t, err)
	type pair struct {
		path string
		code lint.LintFindingCode
	}
	got := make([]pair, 0, len(report.Findings))
	for _, f := range report.Findings {
		got = append(got, pair{f.Path, f.Code})
	}
	require.Equal(t, []pair{
		{"docs/a.md", lint.LintDeadEntry},
		{"docs/a.md", lint.LintNonexistentDocPath},
		{"docs/a.md", lint.LintZeroMatchGlob},
		{"docs/b.md", lint.LintDeadEntry},
		{"docs/b.md", lint.LintNonexistentDocPath},
		{"docs/b.md", lint.LintZeroMatchGlob},
	}, got)
}

func TestLintManifest_FailsClosedOnSchemaInvalidManifest(t *testing.T) {
	invalid := schemas.Manifest{Version: 0} // documents empty too
	_, err := lint.LintManifest(lint.LintManifestParams{Manifest: invalid, RepoFiles: nil})
	require.Error(t, err)
	var lintErr *lint.ManifestLintError
	require.ErrorAs(t, err, &lintErr)
	require.Equal(t, "EDOCSYNC_LINT_INVALID", lintErr.Code())
	require.GreaterOrEqual(t, len(lintErr.Issues), 1)
	require.Contains(t, err.Error(), "schema validation")
}

func TestLintManifest_UnknownKeysRejectedAtStrictDecode(t *testing.T) {
	// z.strictObject semantics: an UNKNOWN root key is a hard error at the STRICT
	// PARSE boundary (schemas.StrictDecode / json DisallowUnknownFields), never
	// inside lint — LintManifest receives an already-parsed struct and can only
	// re-check semantics (see TestLintManifest_FailsClosedOnSchemaInvalidManifest).
	dec := json.NewDecoder(strings.NewReader(`{"version":1,"documents":[{"path":"docs/x.md","sources":["src/**"]}],"extra":true}`))
	dec.DisallowUnknownFields()
	var m schemas.Manifest
	require.Error(t, dec.Decode(&m))
}

func TestLintManifest_PropagatesGlobGuardError(t *testing.T) {
	manifest := schemas.Manifest{
		Version:   1,
		Documents: []schemas.ManifestEntry{{Path: "docs/x.md", Sources: []string{"../escape/**"}}},
	}
	_, err := lint.LintManifest(lint.LintManifestParams{Manifest: manifest, RepoFiles: nil})
	require.Error(t, err)
	var guardErr *globs.GlobGuardError
	require.ErrorAs(t, err, &guardErr)
}

func TestLintManifestFromRepo_WalksExcludingVendoredDirsAndLints(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/payments/pay.ts", "export {};\n")
	write(t, root, "docs/payments.md", "# payments\n")
	write(t, root, "docs/unmapped.md", "# unmapped\n")
	write(t, root, "README.md", "# readme\n")
	write(t, root, "node_modules/pkg/index.js", "module.exports = 1;\n")
	write(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	write(t, root, "dist/bundle.js", "console.log(1);\n")
	write(t, root, ".docsync/state/runs/run-1.json", "{}\n")

	files, err := lint.WalkRepoFiles(root)
	require.NoError(t, err)
	require.Equal(t, []string{
		"README.md",
		"docs/payments.md",
		"docs/unmapped.md",
		"src/payments/pay.ts",
	}, files)

	manifest := schemas.Manifest{
		Version:   1,
		Documents: []schemas.ManifestEntry{{Path: "docs/payments.md", Sources: []string{"src/**/*.ts"}}},
	}
	report, err := lint.LintManifestFromRepo(manifest, root)
	require.NoError(t, err)
	require.True(t, report.OK)
	require.Len(t, report.Findings, 1)
	require.Equal(t, lint.LintUnmappedDocDir, report.Findings[0].Code)
	require.Equal(t, "docs/unmapped.md", report.Findings[0].Path)
	require.Contains(t, report.Findings[0].Detail, "D7")

	// The vendored dirs were pruned: every excluded-dir source glob matches zero
	// walked files (3 zero_match_glob warnings), the run proceeding (warnings).
	excluded := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{{
			Path:    "docs/payments.md",
			Sources: []string{"node_modules/**", "dist/**", ".git/**"},
		}},
	}
	excludedReport, err := lint.LintManifestFromRepo(excluded, root)
	require.NoError(t, err)
	zeroMatch := 0
	for _, f := range excludedReport.Findings {
		if f.Code == lint.LintZeroMatchGlob {
			zeroMatch++
		}
	}
	require.Equal(t, 3, zeroMatch)
}
