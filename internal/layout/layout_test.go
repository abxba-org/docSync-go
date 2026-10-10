package layout_test

// Ported from packages/core/test/layout.test.ts (V-P0-3, P0-T17) — canonical
// `.docsync/` layout validation, typed errors, and the fs adapter. Black-box:
// only the exported API is exercised.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var canonicalPaths = []string{
	"manifest.yml",
	"policy.yml",
	"concepts.yml",
	"instructions.md",
	"models.yml",
	"evaluation/",
	"memory/",
	"memory/decisions.md",
	"memory/known-patterns.md",
	"memory/exceptions.md",
	"memory/failures.md",
	"policy-history/",
	"policy-history/v1.json",
	"state/",
	"state/baselines/",
	"state/claims/",
	"state/docs/",
	"state/runs/",
	"state/audit-cursor",
}

// seedDocsync seeds paths under `<repoRoot>/.docsync/`; paths ending with "/"
// seed directories, every other path seeds an empty file.
func seedDocsync(t *testing.T, repoRoot string, paths []string) {
	t.Helper()
	root := filepath.Join(repoRoot, ".docsync")
	for _, path := range paths {
		absolute := filepath.Join(root, filepath.Join(strings.Split(path, "/")...))
		if strings.HasSuffix(path, "/") {
			require.NoError(t, os.MkdirAll(absolute, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(absolute), 0o755))
		require.NoError(t, os.WriteFile(absolute, nil, 0o644))
	}
}

func expectErr(t *testing.T, result layout.LayoutResult) layout.LayoutResult {
	t.Helper()
	assert.False(t, result.OK, "expected layout validation to fail")
	return result
}

func violationKindByPath(violations []layout.LayoutViolation, path string) (layout.LayoutViolationKind, bool) {
	for _, v := range violations {
		if v.Path == path {
			return v.Kind, true
		}
	}
	return "", false
}

func TestLoadDocsyncLayoutCanonicalD1Tree(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, canonicalPaths)

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	require.True(t, result.OK)
	resolved, err := layout.AssertLayoutOk(result)
	require.NoError(t, err)

	assert.Equal(t, layout.CanonicalLayout{
		ManifestPath:     ".docsync/manifest.yml",
		PolicyPath:       ".docsync/policy.yml",
		ConceptsPath:     ".docsync/concepts.yml",
		InstructionsPath: ".docsync/instructions.md",
		ModelsPath:       ".docsync/models.yml",
		EvaluationDir:    ".docsync/evaluation",
		MemoryDir:        ".docsync/memory",
		MemoryFiles: []string{
			".docsync/memory/decisions.md",
			".docsync/memory/known-patterns.md",
			".docsync/memory/exceptions.md",
			".docsync/memory/failures.md",
		},
		PolicyHistoryDir: ".docsync/policy-history",
		StateDir:         ".docsync/state",
		StateSubdirs: []string{
			".docsync/state/baselines",
			".docsync/state/claims",
			".docsync/state/docs",
			".docsync/state/runs",
		},
		StateFiles: []string{".docsync/state/audit-cursor"},
	}, resolved)
}

func TestLoadDocsyncLayoutFlagsMisplacedPolicyHistoryUnderMemory(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, append(append([]string{}, canonicalPaths...), "memory/policy-history/v1.json"))

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	result = expectErr(t, result)

	var misplaced []layout.LayoutViolation
	for _, v := range result.Violations {
		if strings.HasPrefix(v.Path, "memory/policy-history") {
			misplaced = append(misplaced, v)
		}
	}
	require.GreaterOrEqual(t, len(misplaced), 1)
	for _, v := range misplaced {
		assert.Equal(t, layout.ViolationMisplacedFile, v.Kind)
		assert.True(t, strings.HasPrefix(v.Expected, ".docsync/policy-history/"),
			"expected %q to start with .docsync/policy-history/", v.Expected)
	}
	found := false
	for _, v := range misplaced {
		if v.Path == "memory/policy-history/v1.json" {
			found = true
		}
	}
	assert.True(t, found, "expected a violation at memory/policy-history/v1.json")
}

func TestDocsyncLayoutErrorRendersPathPrecise(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, append(append([]string{}, canonicalPaths...), "memory/policy-history/v1.json"))

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	result = expectErr(t, result)

	layoutErr := layout.NewDocsyncLayoutError(result.Violations)
	assert.Equal(t, layout.EDOCSYNC_LAYOUT, layoutErr.Code())
	assert.Contains(t, layoutErr.Render(),
		"memory/policy-history/v1.json: canonical top-level directory found in a nested location (expected .docsync/policy-history/")
	assert.Len(t, strings.Split(layoutErr.Render(), "\n"), len(result.Violations))
}

func TestLoadDocsyncLayoutDeterministicAcrossRepeatedLoads(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, canonicalPaths)

	first, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	second, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)

	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)
	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)
	assert.JSONEq(t, string(firstJSON), string(secondJSON))
	assert.Equal(t, first, second)
}

func TestLoadDocsyncLayoutMissingManifestIsMissingRequiredFile(t *testing.T) {
	repoRoot := t.TempDir()
	seeded := make([]string, 0, len(canonicalPaths))
	for _, path := range canonicalPaths {
		if path != "manifest.yml" {
			seeded = append(seeded, path)
		}
	}
	seedDocsync(t, repoRoot, seeded)

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	result = expectErr(t, result)

	var missing *layout.LayoutViolation
	for i := range result.Violations {
		if result.Violations[i].Kind == layout.ViolationMissingRequiredFile {
			missing = &result.Violations[i]
		}
	}
	require.NotNil(t, missing)
	assert.Equal(t, layout.LayoutViolation{
		Kind:     layout.ViolationMissingRequiredFile,
		Path:     "manifest.yml",
		Expected: ".docsync/manifest.yml",
		Message:  "manifest.yml is required — no manifest, no run (D7/D8)",
	}, *missing)
}

func TestLoadDocsyncLayoutFlagsStrayAndRogueEntries(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, []string{
		"manifest.yml",
		"random.txt",
		"memory/",
		"memory/extra.txt",
		"state/",
		"state/manifest.yml",
		"state/rogue.json",
	})

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	result = expectErr(t, result)

	byPath := map[string]layout.LayoutViolationKind{}
	for _, v := range result.Violations {
		byPath[v.Path] = v.Kind
	}
	assert.Equal(t, layout.ViolationUnknownEntry, byPath["random.txt"])
	assert.Equal(t, layout.ViolationMemoryExtraFile, byPath["memory/extra.txt"])
	assert.Equal(t, layout.ViolationMisplacedFile, byPath["state/manifest.yml"])
	for _, v := range result.Violations {
		if v.Path == "state/manifest.yml" {
			assert.Equal(t, ".docsync/manifest.yml", v.Expected)
		}
	}
	assert.Equal(t, layout.ViolationStateUnknownEntry, byPath["state/rogue.json"])
	_, hasMissing := violationKindByPath(result.Violations, "manifest.yml")
	assert.False(t, hasMissing, "no missing_required_file expected for the top-level manifest.yml")
}

func TestLoadDocsyncLayoutMissingDocsyncDir(t *testing.T) {
	repoRoot := t.TempDir()

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	result = expectErr(t, result)

	require.Len(t, result.Violations, 1)
	assert.Equal(t, layout.ViolationMissingDocsyncDir, result.Violations[0].Kind)
	assert.Equal(t, ".docsync", result.Violations[0].Path)
}

func TestLoadDocsyncLayoutDocsyncAsFileIsDirExpected(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".docsync"), nil, 0o644))

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	result = expectErr(t, result)

	require.Len(t, result.Violations, 1)
	assert.Equal(t, layout.ViolationDirExpected, result.Violations[0].Kind)
	assert.Equal(t, ".docsync", result.Violations[0].Path)
}

func TestLoadDocsyncLayoutBareManifestWithEmptyState(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, []string{"manifest.yml", "state/"})

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	require.True(t, result.OK)

	assert.Equal(t, layout.CanonicalLayout{
		ManifestPath: ".docsync/manifest.yml",
		StateDir:     ".docsync/state",
	}, result.Resolved)
}

func TestLoadDocsyncLayoutEvaluationIsFreeForm(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, []string{"manifest.yml", "evaluation/", "evaluation/cases/x.json"})

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	require.True(t, result.OK)
	assert.Equal(t, ".docsync/evaluation", result.Resolved.EvaluationDir)
}

func TestLoadDocsyncLayoutFreeFormUnderStateSubdirs(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, []string{"manifest.yml", "state/", "state/runs/", "state/runs/run-1/steps/0.json"})

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	assert.True(t, result.OK)
}

func TestLoadDocsyncLayoutSymlinkedDirResolvesWithoutFollowing(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, []string{"manifest.yml", "memory/decisions.md"})
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "assets", "random.txt"), nil, 0o644))
	require.NoError(t, os.RemoveAll(filepath.Join(repoRoot, ".docsync", "memory")))
	require.NoError(t, os.Symlink(filepath.Join("..", "assets"), filepath.Join(repoRoot, ".docsync", "memory")))

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	require.True(t, result.OK)
	resolved, err := layout.AssertLayoutOk(result)
	require.NoError(t, err)
	assert.Equal(t, ".docsync/memory", resolved.MemoryDir)
	assert.Empty(t, resolved.MemoryFiles)
}

func TestValidateLayoutViolationsSortedByPath(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "state/rogue.json", Kind: layout.LayoutEntryFile},
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "random.txt", Kind: layout.LayoutEntryFile},
		{Path: "memory/extra.txt", Kind: layout.LayoutEntryFile},
		{Path: "z stray.txt", Kind: layout.LayoutEntryFile},
	})

	result = expectErr(t, result)
	paths := make([]string, len(result.Violations))
	for i, v := range result.Violations {
		paths[i] = v.Path
	}
	assert.Equal(t, []string{"memory/extra.txt", "random.txt", "state/rogue.json", "z stray.txt"}, paths)
}

func TestValidateLayoutDeterministicForIdenticalSnapshots(t *testing.T) {
	entries := []layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state/rogue.json", Kind: layout.LayoutEntryFile},
		{Path: "memory/extra.txt", Kind: layout.LayoutEntryFile},
	}
	first, err := json.Marshal(layout.ValidateLayout(entries))
	require.NoError(t, err)
	second, err := json.Marshal(layout.ValidateLayout(entries))
	require.NoError(t, err)
	assert.JSONEq(t, string(first), string(second))
}

func TestValidateLayoutNestedDocsyncIsUnknownEntry(t *testing.T) {
	result := expectErr(t, layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: ".docsync/.docsync", Kind: layout.LayoutEntryDir},
	}))
	kind, ok := violationKindByPath(result.Violations, ".docsync/.docsync")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationUnknownEntry, kind)
}

func TestValidateLayoutCanonicalConfigNamesAtWrongLocations(t *testing.T) {
	result := expectErr(t, layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "evaluation", Kind: layout.LayoutEntryDir},
		{Path: "evaluation/manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "memory", Kind: layout.LayoutEntryDir},
		{Path: "memory/models.yml", Kind: layout.LayoutEntryFile},
	}))
	for _, v := range result.Violations {
		switch v.Path {
		case "evaluation/manifest.yml":
			assert.Equal(t, layout.ViolationMisplacedFile, v.Kind)
			assert.Equal(t, ".docsync/manifest.yml", v.Expected)
		case "memory/models.yml":
			assert.Equal(t, layout.ViolationMisplacedFile, v.Kind)
		}
	}
}

func TestValidateLayoutKindMismatches(t *testing.T) {
	asDir := expectErr(t, layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryDir},
	}))
	pairs := make([][2]string, len(asDir.Violations))
	for i, v := range asDir.Violations {
		pairs[i] = [2]string{v.Path, string(v.Kind)}
	}
	assert.Equal(t, [][2]string{
		{"manifest.yml", "file_expected"},
		{"manifest.yml", "missing_required_file"},
	}, pairs)

	stateDirAsFile := expectErr(t, layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state", Kind: layout.LayoutEntryFile},
	}))
	kinds := make([]layout.LayoutViolationKind, len(stateDirAsFile.Violations))
	for i, v := range stateDirAsFile.Violations {
		kinds[i] = v.Kind
	}
	assert.Equal(t, []layout.LayoutViolationKind{layout.ViolationDirExpected}, kinds)

	subdirAsFile := expectErr(t, layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state/baselines", Kind: layout.LayoutEntryFile},
	}))
	pairs = pairs[:0]
	for _, v := range subdirAsFile.Violations {
		pairs = append(pairs, [2]string{v.Path, string(v.Kind)})
	}
	assert.Equal(t, [][2]string{{"state/baselines", "dir_expected"}}, pairs)

	memoryFileAsDir := expectErr(t, layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "memory", Kind: layout.LayoutEntryDir},
		{Path: "memory/decisions.md", Kind: layout.LayoutEntryDir},
	}))
	kinds = kinds[:0]
	for _, v := range memoryFileAsDir.Violations {
		kinds = append(kinds, v.Kind)
	}
	assert.Equal(t, []layout.LayoutViolationKind{layout.ViolationFileExpected}, kinds)

	auditCursorAsDir := expectErr(t, layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state", Kind: layout.LayoutEntryDir},
		{Path: "state/audit-cursor", Kind: layout.LayoutEntryDir},
	}))
	kinds = kinds[:0]
	for _, v := range auditCursorAsDir.Violations {
		kinds = append(kinds, v.Kind)
	}
	assert.Equal(t, []layout.LayoutViolationKind{layout.ViolationFileExpected}, kinds)
}

func TestValidateLayoutDedupesConflictingEntriesPreferringDir(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state", Kind: layout.LayoutEntryFile},
		{Path: "state", Kind: layout.LayoutEntryDir},
	})
	assert.True(t, result.OK)
}

func TestAssertLayoutOkRendersLinePreciseError(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state/rogue.json", Kind: layout.LayoutEntryFile},
	})
	require.False(t, result.OK)

	_, err := layout.AssertLayoutOk(result)
	require.Error(t, err)
	var layoutErr *layout.DocsyncLayoutError
	require.ErrorAs(t, err, &layoutErr)
	assert.Equal(t, layout.EDOCSYNC_LAYOUT, layoutErr.Code())
	assert.Equal(t,
		"state/rogue.json: state/ must contain only the canonical state entries (expected .docsync/state/ containing only baselines/, claims/, docs/, runs/ (directories) and audit-cursor, fix-run-records.jsonl, incidents.jsonl (files))",
		layoutErr.Render())
}

func TestDocsyncLayoutErrorCodedErrorShape(t *testing.T) {
	violations := []layout.LayoutViolation{
		{Kind: layout.ViolationUnknownEntry, Path: "stray.txt", Expected: ".docsync/", Message: "entry is not part of the canonical .docsync/ tree"},
		{Kind: layout.ViolationStateUnknownEntry, Path: "state/rogue.json", Expected: ".docsync/state/", Message: "state/ must contain only the canonical state entries"},
	}
	layoutErr := layout.NewDocsyncLayoutError(violations)

	// The TS default message is preserved verbatim.
	assert.Equal(t, "invalid .docsync/ layout — 2 violation(s)", layoutErr.Message())
	assert.Contains(t, layoutErr.Error(), layout.EDOCSYNC_LAYOUT)
	assert.Contains(t, layoutErr.Error(), "invalid .docsync/ layout — 2 violation(s)")
	// Unwraps to a docutil coded error carrying the TS code.
	assert.Equal(t, layout.EDOCSYNC_LAYOUT, docutil.CodeOf(layoutErr))
	assert.Error(t, layoutErr.Unwrap())
	assert.Equal(t,
		"stray.txt: entry is not part of the canonical .docsync/ tree (expected .docsync/)\nstate/rogue.json: state/ must contain only the canonical state entries (expected .docsync/state/)",
		layoutErr.Render())
}

func TestLayoutIoErrorIsTypedAndCoded(t *testing.T) {
	repoRoot := t.TempDir()
	seedDocsync(t, repoRoot, []string{"manifest.yml"})
	require.NoError(t, os.Symlink(filepath.Join(repoRoot, "nowhere"), filepath.Join(repoRoot, ".docsync", "broken")))

	_, err := layout.LoadDocsyncLayout(repoRoot)
	require.Error(t, err)
	var ioErr *layout.LayoutIoError
	require.ErrorAs(t, err, &ioErr)
	assert.Equal(t, layout.EDOCSYNC_LAYOUT_IO, ioErr.Code())
	assert.Contains(t, ioErr.Message(), "broken or unreadable symlink at .docsync/broken")
	assert.Error(t, ioErr.Unwrap())
	assert.Contains(t, ioErr.Error(), "EDOCSYNC_LAYOUT_IO")
}

func TestLoadDocsyncLayoutNotDirStatIsMissingDocsyncDir(t *testing.T) {
	// A repoRoot that is itself a file makes `<repoRoot>/.docsync` an ENOTDIR
	// stat — the same "no .docsync/ here" verdict as ENOENT (fail-closed).
	dir := t.TempDir()
	fileRoot := filepath.Join(dir, "not-a-repo")
	require.NoError(t, os.WriteFile(fileRoot, nil, 0o644))

	result, err := layout.LoadDocsyncLayout(fileRoot)
	require.NoError(t, err)
	result = expectErr(t, result)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, layout.ViolationMissingDocsyncDir, result.Violations[0].Kind)
}

func TestValidateLayoutMalformedRelativePathsAreUnknownEntry(t *testing.T) {
	entries := []layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "/absolute", Kind: layout.LayoutEntryFile},
		{Path: "back\\slash", Kind: layout.LayoutEntryFile},
		{Path: "nul\x00byte", Kind: layout.LayoutEntryFile},
		{Path: "up/../down", Kind: layout.LayoutEntryFile},
		{Path: "./dot", Kind: layout.LayoutEntryFile},
		{Path: "double//slash", Kind: layout.LayoutEntryFile},
	}
	result := expectErr(t, layout.ValidateLayout(entries))
	for _, path := range []string{"/absolute", "back\\slash", "nul\x00byte", "up/../down", "./dot", "double//slash"} {
		kind, ok := violationKindByPath(result.Violations, path)
		assert.True(t, ok, "expected a violation for %q", path)
		assert.Equal(t, layout.ViolationUnknownEntry, kind, "path %q", path)
		for _, v := range result.Violations {
			if v.Path == path {
				assert.Equal(t,
					"a relative POSIX path without empty/'.'/'..' segments, backslashes or NUL",
					v.Expected)
			}
		}
	}
}

// TestDocsHeavyFixtureLoadsCanonical ports the temp-repo.test.ts fixture-copy
// cases (P0-T17) that exercise the layout loader: the checked-in docs-heavy
// tree copied to a fresh temp dir resolves ok:true.
func TestDocsHeavyFixtureLoadsCanonical(t *testing.T) {
	repoRoot := t.TempDir()
	copyTree(t, filepath.Join("testdata", "docs-heavy"), repoRoot)

	result, err := layout.LoadDocsyncLayout(repoRoot)
	require.NoError(t, err)
	require.True(t, result.OK)
	resolved, err := layout.AssertLayoutOk(result)
	require.NoError(t, err)
	assert.Equal(t, ".docsync/manifest.yml", resolved.ManifestPath)

	files := listRepoFiles(t, repoRoot)
	assert.Contains(t, files, "docs/overview.md")
	assert.Contains(t, files, "docs/legacy/old-guide.md")
	assert.Contains(t, files, "src/telemetry.ts")
}

// copyTree recursively copies src into dst (testdata fixtures are read-only
// inputs; the copy is a fresh temp dir, mirroring createTempRepo).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dst, 0o755))
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			copyTree(t, srcPath, dstPath)
			continue
		}
		data, err := os.ReadFile(srcPath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dstPath, data, 0o644))
	}
}

func listRepoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}))
	return files
}
