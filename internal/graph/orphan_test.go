package graph_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// Orphaned-edge detection tests (P1-T09; ARCH §4.2; V-P1-6 groundwork):
// deleted/renamed literal source → orphaned edge + affected docs identified;
// glob sources do NOT orphan (they pin nothing); existsAtHead is injected
// (pure function, caller pins the rev); output deterministic and sorted.

func orphanManifest() schemas.Manifest {
	return schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/payments.md", Sources: []string{"src/payments/refund.ts"}},
			{Path: "docs/api.md", Sources: []string{"src/api/**"}},
			{Path: "docs/ops.md", Sources: []string{"src/payments/refund.ts", "src/ops/run.sh"}},
		},
	}
}

func orphanGraph(t *testing.T) graph.DocumentGraph {
	t.Helper()
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: orphanManifest()})
	require.Empty(t, g.Errors)
	return g
}

func alwaysTrue(string) bool { return true }

func TestDetectOrphans_DeletedLiteralSourceOrphansEveryMapping(t *testing.T) {
	edges := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{{Path: "src/payments/refund.ts", Status: "deleted"}},
		Graph:        orphanGraph(t),
		ExistsAtHead: alwaysTrue,
	})
	require.Equal(t, []graph.OrphanEdge{
		{Doc: "docs/ops.md", Source: "src/payments/refund.ts", Kind: "source-deleted"},
		{Doc: "docs/payments.md", Source: "src/payments/refund.ts", Kind: "source-deleted"},
	}, edges)
}

func TestDetectOrphans_RenameAwayOrphansWithOldPath(t *testing.T) {
	edges := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{
			{Path: "src/payments/relocated.ts", Status: "renamed", OldPath: "src/payments/refund.ts"},
		},
		Graph:        orphanGraph(t),
		ExistsAtHead: alwaysTrue,
	})
	require.Equal(t, []graph.OrphanEdge{
		{Doc: "docs/ops.md", Source: "src/payments/refund.ts", Kind: "source-renamed", OldPath: "src/payments/refund.ts"},
		{Doc: "docs/payments.md", Source: "src/payments/refund.ts", Kind: "source-renamed", OldPath: "src/payments/refund.ts"},
	}, edges)
}

func TestDetectOrphans_RenameToSourceIsNotAnOrphan(t *testing.T) {
	edges := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{
			{Path: "src/payments/refund.ts", Status: "renamed", OldPath: "src/payments/old.ts"},
		},
		Graph:        orphanGraph(t),
		ExistsAtHead: alwaysTrue,
	})
	require.Empty(t, edges)
}

func TestDetectOrphans_GlobSourcesNeverOrphan(t *testing.T) {
	edges := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{{Path: "src/api/server.ts", Status: "deleted"}},
		Graph:        orphanGraph(t),
		ExistsAtHead: func(path string) bool { return path != "src/api/server.ts" },
	})
	require.Empty(t, edges)
}

func TestDetectOrphans_LiteralAbsentAtHeadOrphansViaInjectedCheck(t *testing.T) {
	edges := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{},
		Graph:        orphanGraph(t),
		ExistsAtHead: func(path string) bool { return path != "src/ops/run.sh" },
	})
	require.Equal(t, []graph.OrphanEdge{
		{Doc: "docs/ops.md", Source: "src/ops/run.sh", Kind: "source-deleted"},
	}, edges)
}

func TestDetectOrphans_OverlappingSignalsCollapseToOneEdge(t *testing.T) {
	edges := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{{Path: "src/payments/refund.ts", Status: "deleted"}},
		Graph:        orphanGraph(t),
		ExistsAtHead: func(path string) bool { return path != "src/payments/refund.ts" },
	})
	require.Len(t, edges, 2) // one per affected doc, not two per doc
	payments := 0
	for _, e := range edges {
		if e.Doc == "docs/payments.md" {
			payments++
		}
	}
	require.Equal(t, 1, payments)
}

func TestDetectOrphans_HealthyGraphsYieldNoEdgesDeterministic(t *testing.T) {
	g := orphanGraph(t)

	none := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{},
		Graph:        g,
		ExistsAtHead: alwaysTrue,
	})
	require.Empty(t, none)

	twice := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{
			{Path: "src/ops/run.sh", Status: "deleted"},
			{Path: "src/payments/refund.ts", Status: "deleted"},
		},
		Graph:        g,
		ExistsAtHead: func(string) bool { return false },
	})
	twiceReordered := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{
			{Path: "src/payments/refund.ts", Status: "deleted"},
			{Path: "src/ops/run.sh", Status: "deleted"},
		},
		Graph:        g,
		ExistsAtHead: func(string) bool { return false },
	})
	require.Equal(t, twiceReordered, twice)

	pairs := make([][2]string, len(twice))
	for i, e := range twice {
		pairs[i] = [2]string{e.Doc, e.Source}
	}
	require.Equal(t, [][2]string{
		{"docs/ops.md", "src/ops/run.sh"},
		{"docs/ops.md", "src/payments/refund.ts"},
		{"docs/payments.md", "src/payments/refund.ts"},
	}, pairs)
}
