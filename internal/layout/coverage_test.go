package layout_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareViolations_SortedOrder(t *testing.T) {
	entries := []layout.LayoutEntry{
		{Path: "z", Kind: layout.LayoutEntryFile},
		{Path: "a", Kind: layout.LayoutEntryFile},
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state/rogue.json", Kind: layout.LayoutEntryFile},
	}
	result := layout.ValidateLayout(entries)
	paths := make([]string, len(result.Violations))
	for i, v := range result.Violations {
		paths[i] = v.Path
	}
	assert.Equal(t, []string{"a", "state/rogue.json", "z"}, paths)
}

func TestLoadDocsyncLayout_Symlink(t *testing.T) {
	root := t.TempDir()
	docsyncDir := filepath.Join(root, ".docsync")
	require.NoError(t, os.MkdirAll(docsyncDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(docsyncDir, "manifest.yml"), nil, 0o644))
	require.NoError(t, os.Symlink("manifest.yml", filepath.Join(docsyncDir, "link_to_manifest")))
	result, err := layout.LoadDocsyncLayout(root)
	require.NoError(t, err)
	assert.False(t, result.OK)
	kind, ok := violationKindByPath(result.Violations, "link_to_manifest")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationUnknownEntry, kind)
}

func TestIsWellFormedRelativePath_Empty(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "", Kind: layout.LayoutEntryFile},
	})
	assert.False(t, result.OK)
	kind, ok := violationKindByPath(result.Violations, "")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationUnknownEntry, kind)
}

func TestClassifyEntry_ConfigFileAsDir(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryDir},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "manifest.yml")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationFileExpected, val)
}

func TestClassifyEntry_TopLevelDirAsFile(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "memory", Kind: layout.LayoutEntryFile},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "memory")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationDirExpected, val)
}

func TestClassifyEntry_StateFileAsDir(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state", Kind: layout.LayoutEntryDir},
		{Path: "state/audit-cursor", Kind: layout.LayoutEntryDir},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "state/audit-cursor")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationFileExpected, val)
}

func TestClassifyEntry_StateSubdirAsFile(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state", Kind: layout.LayoutEntryDir},
		{Path: "state/baselines", Kind: layout.LayoutEntryFile},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "state/baselines")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationDirExpected, val)
}

func TestClassifyEntry_UnknownTopLevel(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "weird_thing", Kind: layout.LayoutEntryFile},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "weird_thing")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationUnknownEntry, val)
}

func TestClassifyEntry_MemoryFileAsDir(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "memory", Kind: layout.LayoutEntryDir},
		{Path: "memory/decisions.md", Kind: layout.LayoutEntryDir},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "memory/decisions.md")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationFileExpected, val)
}

func TestClassifyEntry_NestedStateUnknown(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "state", Kind: layout.LayoutEntryDir},
		{Path: "state/rogue_dir/custom.json", Kind: layout.LayoutEntryFile},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "state/rogue_dir/custom.json")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationStateUnknownEntry, val)
}

func TestClassifyEntry_MemoryNestedTopLevelDir(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "memory", Kind: layout.LayoutEntryDir},
		{Path: "memory/evaluation/cases/x.json", Kind: layout.LayoutEntryFile},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "memory/evaluation/cases/x.json")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationMisplacedFile, val)
}

func TestClassifyEntry_MemoryDeepNesting(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "memory", Kind: layout.LayoutEntryDir},
		{Path: "memory/sub/deep.txt", Kind: layout.LayoutEntryFile},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "memory/sub/deep.txt")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationMemoryExtraFile, val)
}

func TestClassifyEntry_ConfigFileInFreeFormAsDir(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "evaluation", Kind: layout.LayoutEntryDir},
		{Path: "evaluation/manifest.yml", Kind: layout.LayoutEntryDir},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "evaluation/manifest.yml")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationFileExpected, val)
}

func TestClassifyEntry_ConfigFileNameMisplaced(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "policy-history", Kind: layout.LayoutEntryDir},
		{Path: "policy-history/manifest.yml", Kind: layout.LayoutEntryFile},
	})
	assert.False(t, result.OK)
	val, ok := violationKindByPath(result.Violations, "policy-history/manifest.yml")
	assert.True(t, ok)
	assert.Equal(t, layout.ViolationMisplacedFile, val)
}

func TestClassifyEntry_FreeFormDirValid(t *testing.T) {
	result := layout.ValidateLayout([]layout.LayoutEntry{
		{Path: "manifest.yml", Kind: layout.LayoutEntryFile},
		{Path: "evaluation", Kind: layout.LayoutEntryDir},
		{Path: "evaluation/anything", Kind: layout.LayoutEntryDir},
	})
	assert.True(t, result.OK)
}

func TestLoadDocsyncLayout_ReadDirErrorInSubdir(t *testing.T) {
	root := t.TempDir()
	docsyncDir := filepath.Join(root, ".docsync")
	require.NoError(t, os.MkdirAll(docsyncDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(docsyncDir, "manifest.yml"), nil, 0o644))
	unreadable := filepath.Join(docsyncDir, "state")
	require.NoError(t, os.MkdirAll(unreadable, 0o755))
	require.NoError(t, os.Chmod(unreadable, 0o000))
	t.Cleanup(func() { os.Chmod(unreadable, 0o755) })
	_, err := layout.LoadDocsyncLayout(root)
	require.Error(t, err)
	var ioErr *layout.LayoutIoError
	require.ErrorAs(t, err, &ioErr)
}
