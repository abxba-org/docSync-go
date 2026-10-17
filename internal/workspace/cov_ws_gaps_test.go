package workspace

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intp(v int) *int { return &v }

func TestWsCov_kindRank(t *testing.T) {
	assert.Equal(t, 0, kindRank(kindSourceFile))
	assert.Equal(t, 1, kindRank(kindDocument))
	assert.Equal(t, 2, kindRank(entryKind("bogus")))
}

func TestWsCov_entryOrder(t *testing.T) {
	a := &capEntry{path: "docs/a.md", bytes: 10}
	b := &capEntry{path: "docs/b.md", bytes: 10}
	assert.Less(t, entryOrder(a, b), 0)
	assert.Greater(t, entryOrder(b, a), 0)

	a2 := &capEntry{path: "docs/a.md", bytes: 5}
	assert.Less(t, entryOrder(a2, a), 0)

	a3 := &capEntry{path: "docs/a.md", bytes: 10, kind: kindSourceFile}
	assert.Less(t, entryOrder(a3, a), 0)

	assert.Equal(t, entryOrder(a, a), 0)
}

func TestWsCov_WithOverrides(t *testing.T) {
	r := WithOverrides(&WorkspaceCapsOverrides{MaxDocs: intp(3)})
	assert.Equal(t, 3, r.MaxDocs)
	assert.Equal(t, 20, r.MaxSourceFiles)

	r2 := WithOverrides(nil)
	assert.Equal(t, 10, r2.MaxDocs)
}

func TestWsCov_NewBatchConfigError(t *testing.T) {
	err := NewBatchConfigError("test error")
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "test error")
}

func TestWsCov_findMapping(t *testing.T) {
	_, ok := findMapping([]graph.MappingRecord{}, "docs/a.md")
	assert.False(t, ok)
}

func TestWsCov_disjointSet(t *testing.T) {
	ds := newDisjointSet()
	ds.union(1, 2)
	ds.union(2, 3)
	assert.True(t, ds.has(1))
	assert.True(t, ds.has(3))
	assert.False(t, ds.has(4))
}

func TestWsCov_orderedHits(t *testing.T) {
	oh := newOrderedHits()
	oh.add("a", 0)
	oh.add("b", 1)
	oh.add("a", 2)
	assert.Len(t, oh.indexes("a"), 2)
	keys := oh.sortedKeys()
	assert.Equal(t, []string{"a", "b"}, keys)
}

func TestWsCov_assertPositiveInt(t *testing.T) {
	assert.NoError(t, assertPositiveInt(5, "x"))
	err := assertPositiveInt(0, "x")
	require.Error(t, err)
	err = assertPositiveInt(-1, "x")
	require.Error(t, err)
}
