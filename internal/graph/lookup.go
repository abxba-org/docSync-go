package graph

import (
	"strings"

	"github.com/DUDE616/docSync-go/internal/globs"
)

// Per-run graph lookups (P1-T09; ARCH §4.2; Phase-11 P11-T09). Reverse indexes
// are built per run from the changed-file sets — there is NO persistent
// full-graph index in V1. Matching goes through the core glob engine
// (CompileGlob, which enforces the §11.3 escape guards — a manifest glob that
// fails its guard is a GlobGuardError, a configuration error, fail closed per D8).
//
// Literal source entries (no glob metacharacters, see IsLiteralSourcePattern)
// match by exact path equality — the glob engine is never consulted for them, so
// a literal path containing glob-significant characters still matches itself
// deterministically.
//
// Lookup coverage (the invariant): BOTH directions resolve for ALL edge kinds,
// incl. the Phase-11 api/config/workflow edges —
//   - changed path → docs: BuildReverseIndex over the PATH-ref edge kinds
//     (`code` sources + the rich `api`/`config`/`workflow` refs);
//   - ref → docs (exact, ALL kinds incl. name refs): ReverseLookup;
//   - doc → edges (ALL kinds): ForwardEdges / ForwardLookup.
//
// The invariant is that every edge resolves in BOTH directions and the two
// directions agree — tested exhaustively in the invariants suite.

// ReverseIndex is the reverse-lookup result for one run.
type ReverseIndex struct {
	// DocsByChangedPath maps a changed path → mappings whose sources match it (graph order).
	DocsByChangedPath map[string][]MappingRecord
	// AffectedDocs is the set of distinct docs affected by the changed set (deterministic).
	AffectedDocs map[string]struct{}
}

// globMetaChars are the glob metacharacters recognized by the docSync glob
// engine (picomatch syntax): wildcard `*`/`?`, character classes `[...]`, brace
// expansion `{...}`, negation `!`, and extglobs `(...)`. A source entry
// containing NONE of these is a LITERAL path: it pins one file, which is what
// orphan detection keys on. Conservative by design — when in doubt a pattern is
// treated as a glob (glob sources pin nothing, so they never orphan).
const globMetaChars = "*?[]{}!()"

// IsLiteralSourcePattern reports whether pattern contains no glob metacharacters
// (a pinned literal path).
func IsLiteralSourcePattern(pattern string) bool {
	if strings.ContainsAny(pattern, globMetaChars) {
		return false
	}
	return len(pattern) > 0
}

// SourceMatchesPath reports whether a mapping's source entry matches the changed
// path (literal or glob). A non-literal source is compiled through the glob
// engine; a guard failure is returned as an error (fail closed).
func SourceMatchesPath(source, changedPath string) (bool, error) {
	if IsLiteralSourcePattern(source) {
		return source == changedPath, nil
	}
	matcher, err := globs.CompileGlob(source)
	if err != nil {
		return false, err
	}
	return matcher.Match(changedPath), nil
}

// BuildReverseIndex builds the per-run reverse index: for each changed path, the
// mappings with at least one PATH-ref edge matching it (`code` sources verbatim,
// plus the Phase-11 rich api/config/workflow refs). Mappings appear at most ONCE
// per changed path even when several of their refs match. graph.Mappings order is
// preserved (manifest order → deterministic downstream routing); mappings without
// rich fields behave EXACTLY as in Phase 1.
//
// A glob-ref guard failure aborts the whole call with a GlobGuardError
// (fail closed) before any routing.
func BuildReverseIndex(graph DocumentGraph, changedPaths []string) (ReverseIndex, error) {
	docsByChangedPath := make(map[string][]MappingRecord, len(changedPaths))
	affectedDocs := make(map[string]struct{})
	for _, changedPath := range changedPaths {
		matching := []MappingRecord{}
		for i := range graph.Mappings {
			mapping := graph.Mappings[i]
			refs := PathRefsOf(mapping)
			matched := false
			for _, ref := range refs {
				ok, err := SourceMatchesPath(ref, changedPath)
				if err != nil {
					return ReverseIndex{}, err
				}
				if ok {
					matched = true
					break
				}
			}
			if matched {
				matching = append(matching, mapping)
				affectedDocs[mapping.Doc] = struct{}{}
			}
		}
		docsByChangedPath[changedPath] = matching
	}
	return ReverseIndex{DocsByChangedPath: docsByChangedPath, AffectedDocs: affectedDocs}, nil
}

// ReverseLookup is the exact-ref reverse lookup over ALL edge kinds (incl. the
// name refs `concept` and `doc` that are never path-matched): the mappings with
// at least one edge whose Ref equals ref. graph.Mappings order, each mapping at
// most once — deterministic, no persistent index (derived per call, per the V1
// model).
func ReverseLookup(graph DocumentGraph, ref string) []MappingRecord {
	var matching []MappingRecord
	seen := make(map[string]struct{})
	for _, edge := range GraphEdges(graph) {
		if edge.Ref != ref {
			continue
		}
		if _, dup := seen[edge.Doc]; dup {
			continue
		}
		seen[edge.Doc] = struct{}{}
		for i := range graph.Mappings {
			if graph.Mappings[i].Doc == edge.Doc {
				matching = append(matching, graph.Mappings[i])
				break
			}
		}
	}
	return matching
}

// ForwardLookup returns doc → its mapping record (nil when the doc is unmapped).
func ForwardLookup(graph DocumentGraph, doc string) *MappingRecord {
	for i := range graph.Mappings {
		if graph.Mappings[i].Doc == doc {
			return &graph.Mappings[i]
		}
	}
	return nil
}

// ForwardEdges is the forward edge lookup: doc → its typed edges, ALL kinds
// (derivation order: vocabulary kind order, declaration order within a kind —
// deterministic). The forward half of the bidirectional invariant.
func ForwardEdges(graph DocumentGraph, doc string) []DocumentGraphEdge {
	var result []DocumentGraphEdge
	for _, edge := range GraphEdges(graph) {
		if edge.Doc == doc {
			result = append(result, edge)
		}
	}
	return result
}
