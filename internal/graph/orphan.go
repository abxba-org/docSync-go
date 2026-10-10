package graph

import "sort"

// Orphaned-edge detection (P1-T09; ARCH §4.2; V-P1-6 groundwork). When a
// mapping's LITERAL source (a pinned path — glob sources pin nothing and are
// NEVER orphaned) disappears — deleted, renamed away, or simply absent at HEAD —
// the code ↔ doc edge is marked orphaned and the affected docs are re-routed
// toward SUSPECT by the routing layer. Orphaned edges are REPORTED, never
// silently dropped (V-P1-6) and never silently repaired: re-anchoring a doc to a
// renamed file is a mapping edit, which is declarative + reviewable.
//
// Pure function: ExistsAtHead is injected (the caller pins the rev it asks about
// — usually HEAD). Deterministic and sorted (doc, source, kind, old_path);
// duplicate edges (same doc/source/kind/old_path from overlapping signals, e.g.
// a deletion that also fails ExistsAtHead) collapse to one.

// OrphanChangedFile is one file-change signal orphan detection consumes.
type OrphanChangedFile struct {
	Path    string
	Status  string // "deleted" | "renamed"
	OldPath string
}

// OrphanEdge is one orphaned mapping edge.
type OrphanEdge struct {
	// Doc is the affected document (routes toward SUSPECT, V-P1-6).
	Doc string
	// Source is the literal source entry (verbatim from the manifest).
	Source string
	// Kind is "source-deleted" | "source-renamed".
	Kind string
	// OldPath is where the source went, for renames (the rename's old_path is the source).
	OldPath string
}

// DetectOrphansInput bundles the inputs of DetectOrphans.
type DetectOrphansInput struct {
	ChangedFiles []OrphanChangedFile
	Graph        DocumentGraph
	ExistsAtHead func(path string) bool
}

// DetectOrphans detects orphaned edges for every literal mapping source that no
// longer resolves. Signals (per mapping source, first applicable wins per changed
// file; existence at HEAD is the fallback):
//   - changed file status "deleted" with path === source → source-deleted;
//   - changed file status "renamed" with oldPath === source (renamed AWAY from
//     the source) → source-renamed (renaming TO the source is not an orphan —
//     the pinned path exists again);
//   - !existsAtHead(source) → source-deleted (vanished without a diff signal —
//     e.g. the change came from outside the scanned range).
func DetectOrphans(input DetectOrphansInput) []OrphanEdge {
	byKey := make(map[string]OrphanEdge)
	set := func(edge OrphanEdge) {
		key := edge.Doc + "\x00" + edge.Source + "\x00" + edge.Kind + "\x00" + edge.OldPath
		if _, exists := byKey[key]; !exists {
			byKey[key] = edge
		}
	}
	for i := range input.Graph.Mappings {
		mapping := input.Graph.Mappings[i]
		for _, source := range mapping.Sources {
			if !IsLiteralSourcePattern(source) {
				continue
			}
			if change := findOrphanChange(input.ChangedFiles, source); change != nil {
				if change.Status == "renamed" {
					set(OrphanEdge{
						Doc:     mapping.Doc,
						Source:  source,
						Kind:    "source-renamed",
						OldPath: change.OldPath,
					})
				} else {
					set(OrphanEdge{Doc: mapping.Doc, Source: source, Kind: "source-deleted"})
				}
			} else if !input.ExistsAtHead(source) {
				set(OrphanEdge{Doc: mapping.Doc, Source: source, Kind: "source-deleted"})
			}
		}
	}
	result := make([]OrphanEdge, 0, len(byKey))
	for _, edge := range byKey {
		result = append(result, edge)
	}
	sort.Slice(result, func(i, j int) bool {
		return compareOrphanEdges(result[i], result[j]) < 0
	})
	return result
}

// findOrphanChange returns the first changed file that orphans source, or nil.
func findOrphanChange(files []OrphanChangedFile, source string) *OrphanChangedFile {
	for i := range files {
		f := files[i]
		if (f.Status == "deleted" && f.Path == source) ||
			(f.Status == "renamed" && f.OldPath == source) {
			return &files[i]
		}
	}
	return nil
}

func compareOrphanEdges(a, b OrphanEdge) int {
	if a.Doc != b.Doc {
		if a.Doc < b.Doc {
			return -1
		}
		return 1
	}
	if a.Source != b.Source {
		if a.Source < b.Source {
			return -1
		}
		return 1
	}
	if a.Kind != b.Kind {
		if a.Kind < b.Kind {
			return -1
		}
		return 1
	}
	if a.OldPath != b.OldPath {
		if a.OldPath < b.OldPath {
			return -1
		}
		return 1
	}
	return 0
}
