package graph

// Internal coverage for the deterministic orphan edge ordering (the sort
// comparator's ordering branches are only reachable with mixed inputs both
// ways around).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCOV_INTERNAL_GRAPH_compareOrphanEdgesAllBranches(t *testing.T) {
	base := OrphanEdge{Doc: "a", Source: "b", Kind: "source-deleted", OldPath: ""}

	less := OrphanEdge{Doc: "a", Source: "b", Kind: "source-deleted", OldPath: ""}
	require.Equal(t, 0, compareOrphanEdges(base, less))

	assert.Equal(t, -1, compareOrphanEdges(OrphanEdge{Doc: "a"}, OrphanEdge{Doc: "b"}))
	assert.Equal(t, 1, compareOrphanEdges(OrphanEdge{Doc: "b"}, OrphanEdge{Doc: "a"}))
	assert.Equal(t, -1, compareOrphanEdges(OrphanEdge{Doc: "a", Source: "a"}, OrphanEdge{Doc: "a", Source: "b"}))
	assert.Equal(t, 1, compareOrphanEdges(OrphanEdge{Doc: "a", Source: "b"}, OrphanEdge{Doc: "a", Source: "a"}))
	assert.Equal(t, -1, compareOrphanEdges(
		OrphanEdge{Doc: "a", Kind: "source-deleted"},
		OrphanEdge{Doc: "a", Kind: "source-renamed"}))
	assert.Equal(t, 1, compareOrphanEdges(
		OrphanEdge{Doc: "a", Kind: "source-renamed"},
		OrphanEdge{Doc: "a", Kind: "source-deleted"}))
	assert.Equal(t, -1, compareOrphanEdges(
		OrphanEdge{Doc: "a", OldPath: ""},
		OrphanEdge{Doc: "a", OldPath: "old.ts"}))
	assert.Equal(t, 1, compareOrphanEdges(
		OrphanEdge{Doc: "a", OldPath: "old.ts"},
		OrphanEdge{Doc: "a", OldPath: ""}))
	require.Equal(t, 0, compareOrphanEdges(
		OrphanEdge{Doc: "a", OldPath: "x"},
		OrphanEdge{Doc: "a", OldPath: "x"}))
}

func TestCOV_INTERNAL_GRAPH_detectOrphansFullSort(t *testing.T) {
	entries := DetectOrphans(DetectOrphansInput{
		ChangedFiles: []OrphanChangedFile{
			{Path: "old.ts", Status: "renamed", OldPath: "old.ts"},
		},
		Graph: DocumentGraph{Mappings: []MappingRecord{
			{Doc: "z.md", Sources: []string{"old.ts"}},
			{Doc: "a.md", Sources: []string{"old.ts"}},
			{Doc: "m.md", Sources: []string{"gone.ts"}},
		}},
		ExistsAtHead: func(string) bool { return false },
	})
	require.Len(t, entries, 3)
	assert.Equal(t, "a.md", entries[0].Doc)
	assert.Equal(t, "m.md", entries[1].Doc)
	assert.Equal(t, "z.md", entries[2].Doc)
}
