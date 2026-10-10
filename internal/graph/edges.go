package graph

import (
	"fmt"
	"strings"
)

// Typed edge records over the document graph (Phase-11, P11-T09; ARCH §4.2).
// Phase 1 kept edges implicit in the MappingRecord fields; Phase 11 makes them
// first-class (DocumentGraphEdge) so the reverse/forward lookup invariant can be
// stated — and tested — over an EXPLICIT edge set:
//
//	for every edge (kind, doc, ref): forward(doc) sees the edge AND
//	reverse(ref) sees the doc — in BOTH directions, for ALL kinds.
//
// Derivation is deterministic (manifest order, then vocabulary kind order, then
// declaration order within a kind) and PURE: graphEdges never mutates the graph
// and never invents refs. The edge vocabulary is versioned data
// (GraphEdgeKindValues, additive); toGraphEdge is the single typed ingestion
// point for edge DATA and refuses unknown kinds (fail closed, D8): an edge whose
// kind is outside the vocabulary can never enter the system through it.

// IsGraphEdgeKind reports whether value is in the (versioned, additive) edge-kind
// vocabulary.
func IsGraphEdgeKind(value string) bool {
	for _, k := range GraphEdgeKindValues {
		if string(k) == value {
			return true
		}
	}
	return false
}

// ToGraphEdge normalizes raw edge data into a typed DocumentGraphEdge — the
// typed ingestion point for edge records (programmatically composed graphs,
// future file formats, proposals). Shape violations and UNKNOWN KINDS are
// refused with a *GraphEdgeInvalidError (fail closed): never coerced, never
// dropped.
func ToGraphEdge(raw any) (DocumentGraphEdge, error) {
	record, ok := raw.(map[string]any)
	if !ok {
		return DocumentGraphEdge{}, NewGraphEdgeInvalidError("graph edge must be an object", GraphEdgeReasonShape, "")
	}
	for _, field := range []string{"kind", "doc", "ref"} {
		v, ok := record[field].(string)
		if !ok || len(v) == 0 {
			return DocumentGraphEdge{}, NewGraphEdgeInvalidError(
				fmt.Sprintf("graph edge %q must be a non-empty string", field), GraphEdgeReasonShape, "")
		}
	}
	kind := record["kind"].(string) // safe: validated above
	if !IsGraphEdgeKind(kind) {
		return DocumentGraphEdge{}, NewGraphEdgeInvalidError(
			fmt.Sprintf("unknown graph edge kind %q — edge kinds: %s", kind, joinGraphEdgeKindValues()),
			GraphEdgeReasonUnknownKind, kind)
	}
	return DocumentGraphEdge{
		Kind: GraphEdgeKind(kind),
		Doc:  record["doc"].(string),
		Ref:  record["ref"].(string),
	}, nil
}

// PathRefsOf returns the path-ref sources of one mapping, in declaration order
// (sources, then apis, configs, workflows).
func PathRefsOf(mapping MappingRecord) []string {
	out := make([]string, 0, len(mapping.Sources)+len(mapping.APIs)+len(mapping.Configs)+len(mapping.Workflows))
	out = append(out, mapping.Sources...)
	out = append(out, mapping.APIs...)
	out = append(out, mapping.Configs...)
	out = append(out, mapping.Workflows...)
	return out
}

// GraphEdges derives the explicit edge set of a graph (deterministic, pure): per
// mapping in graph order, per kind in vocabulary order (`code`, `concept`,
// `doc`, `api`, `config`, `workflow`), refs in declaration order. Edge records
// are deduplicated on the identity triple (first occurrence wins — mirrors the
// reverse index's "at most once" rule). Mappings without the rich fields
// contribute exactly their Phase-1 edges — composition is backward-compatible.
//
// Insertion order matches the TS Map-based derivation (first occurrence wins).
func GraphEdges(graph DocumentGraph) []DocumentGraphEdge {
	byIdentity := make(map[string]DocumentGraphEdge)
	order := make([]string, 0)
	add := func(kind GraphEdgeKind, doc, ref string) {
		key := string(kind) + "\x00" + doc + "\x00" + ref
		if _, exists := byIdentity[key]; !exists {
			byIdentity[key] = DocumentGraphEdge{Kind: kind, Doc: doc, Ref: ref}
			order = append(order, key)
		}
	}
	for i := range graph.Mappings {
		mapping := graph.Mappings[i]
		// NAME_REF_FIELDS in vocabulary kind order: concepts → concept, contracts → doc.
		for _, ref := range mapping.Sources {
			add(GraphEdgeKindCode, mapping.Doc, ref)
		}
		for _, ref := range mapping.Concepts {
			add(GraphEdgeKindConcept, mapping.Doc, ref)
		}
		for _, ref := range mapping.Contracts {
			add(GraphEdgeKindDoc, mapping.Doc, ref)
		}
		// RICH_FIELDS in vocabulary kind order: apis → api, configs → config, workflows → workflow.
		for _, ref := range mapping.APIs {
			add(GraphEdgeKindAPI, mapping.Doc, ref)
		}
		for _, ref := range mapping.Configs {
			add(GraphEdgeKindConfig, mapping.Doc, ref)
		}
		for _, ref := range mapping.Workflows {
			add(GraphEdgeKindWorkflow, mapping.Doc, ref)
		}
	}
	result := make([]DocumentGraphEdge, 0, len(order))
	for _, key := range order {
		result = append(result, byIdentity[key])
	}
	return result
}

// joinGraphEdgeKindValues renders the vocabulary as the comma-separated list used
// in error messages (GRAPH_EDGE_KIND_VALUES.join(', ')).
func joinGraphEdgeKindValues() string {
	parts := make([]string, len(GraphEdgeKindValues))
	for i, k := range GraphEdgeKindValues {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}
