package graph

import "github.com/DUDE616/docSync-go/internal/schemas"

// MappingRecord is one document's declarative mapping (verbatim manifest globs
// + concepts data). Ported from the TS MappingRecord interface.
type MappingRecord struct {
	// Doc is the manifest document path (also the record identity within the graph).
	Doc string `json:"doc"`
	// Sources holds manifest `sources` entries VERBATIM (globs and literal paths unmodified).
	Sources []string `json:"sources"`
	// Concepts holds concept names (concepts.yml declaration order) that include this doc.
	Concepts []string `json:"concepts"`
	// Contracts holds contract names declared for this doc (concepts.yml declaration order).
	Contracts []string `json:"contracts"`
	// APIs are API references this doc documents (Phase-11 rich edge). Declaration
	// order; OPTIONAL — nil means no api edges, which keeps Phase-1 records and
	// composition backward-compatible.
	APIs []string `json:"apis,omitempty"`
	// Configs are config references this doc documents (Phase-11 rich edge; optional).
	Configs []string `json:"configs,omitempty"`
	// Workflows are workflow references this doc documents (Phase-11 rich edge; optional).
	Workflows []string `json:"workflows,omitempty"`
}

// GraphEdgeKind is one of the versioned, additive edge-kind vocabulary values
// (TS GraphEdgeKind).
type GraphEdgeKind string

// The edge-kind vocabulary in canonical (additive) order: the three Phase-1
// implicit kinds keep their positions; the Phase-11 kinds are appended.
const (
	GraphEdgeKindCode     GraphEdgeKind = "code"
	GraphEdgeKindConcept  GraphEdgeKind = "concept"
	GraphEdgeKindDoc      GraphEdgeKind = "doc"
	GraphEdgeKindAPI      GraphEdgeKind = "api"
	GraphEdgeKindConfig   GraphEdgeKind = "config"
	GraphEdgeKindWorkflow GraphEdgeKind = "workflow"
)

// GraphEdgeKindValues lists the edge-kind vocabulary in canonical order
// (GRAPH_EDGE_KIND_VALUES).
var GraphEdgeKindValues = []GraphEdgeKind{
	GraphEdgeKindCode,
	GraphEdgeKindConcept,
	GraphEdgeKindDoc,
	GraphEdgeKindAPI,
	GraphEdgeKindConfig,
	GraphEdgeKindWorkflow,
}

// GraphPathEdgeKind is one of the PATH-ref edge kinds: refs are repository paths
// or globs and therefore participate in the changed-path reverse index.
// Remaining kinds (concept, doc) are NAME refs resolved only through exact-ref
// lookup.
type GraphPathEdgeKind string

// The PATH-ref edge kinds, in canonical order (GRAPH_PATH_EDGE_KIND_VALUES).
const (
	GraphPathEdgeKindCode     GraphPathEdgeKind = "code"
	GraphPathEdgeKindAPI      GraphPathEdgeKind = "api"
	GraphPathEdgeKindConfig   GraphPathEdgeKind = "config"
	GraphPathEdgeKindWorkflow GraphPathEdgeKind = "workflow"
)

// GraphPathEdgeKindValues lists the PATH-ref edge kinds in canonical order.
var GraphPathEdgeKindValues = []GraphPathEdgeKind{
	GraphPathEdgeKindCode,
	GraphPathEdgeKindAPI,
	GraphPathEdgeKindConfig,
	GraphPathEdgeKindWorkflow,
}

// DocumentGraphEdge is one explicit graph edge: `doc` ↔ `ref` under `kind`
// (the edge identity triple). Ported from the TS DocumentGraphEdge interface.
type DocumentGraphEdge struct {
	Kind GraphEdgeKind `json:"kind"`
	// Doc is the document side of the edge (mapping identity).
	Doc string `json:"doc"`
	// Ref is the counterpart reference (source path/glob, concept name, api/config/workflow ref).
	Ref string `json:"ref"`
}

// GraphConfigErrorKind is one of the typed configuration-error kinds surfaced by
// buildDocumentGraph — versioned data consumed by parseGraph file-format
// validation (TS GraphConfigErrorKind).
type GraphConfigErrorKind string

// The configuration-error kinds, in canonical order
// (GRAPH_CONFIG_ERROR_KIND_VALUES).
const (
	GraphConfigErrConceptsDocNotInManifest    GraphConfigErrorKind = "concepts_doc_not_in_manifest"
	GraphConfigErrDuplicateConceptName        GraphConfigErrorKind = "duplicate_concept_name"
	GraphConfigErrConceptReferencesUnknownDoc GraphConfigErrorKind = "concept_references_unknown_doc"
	GraphConfigErrDuplicateClaimID            GraphConfigErrorKind = "duplicate_claim_id"
	GraphConfigErrDuplicateContractDecl       GraphConfigErrorKind = "duplicate_contract_declaration"
	GraphConfigErrUnknownContractKind         GraphConfigErrorKind = "unknown_contract_kind"
)

// GraphConfigErrorKindValues lists the configuration-error kinds in canonical order.
var GraphConfigErrorKindValues = []GraphConfigErrorKind{
	GraphConfigErrConceptsDocNotInManifest,
	GraphConfigErrDuplicateConceptName,
	GraphConfigErrConceptReferencesUnknownDoc,
	GraphConfigErrDuplicateClaimID,
	GraphConfigErrDuplicateContractDecl,
	GraphConfigErrUnknownContractKind,
}

// GraphConfigError is a concepts.yml configuration error (fail closed, D8). Path
// is a concepts.yml-relative pointer (e.g. `docs[2].path`); Line is best-effort
// 1-based YAML line precision (present only when raw YAML was supplied).
type GraphConfigError struct {
	Kind    GraphConfigErrorKind `json:"kind"`
	Path    string               `json:"path"`
	Message string               `json:"message"`
	Line    *int                 `json:"line,omitempty"`
}

// DocumentGraph is the composed document graph. A graph with a non-empty Errors
// slice is UNUSABLE (fail closed): consumers must refuse to route on it — the
// mappings are still returned for diagnostics only. ContractKinds is the
// validated concepts.yml contractKinds map (entries with unknown kinds are
// dropped and reported; undeclared names classify as 'content' downstream).
type DocumentGraph struct {
	Mappings      []MappingRecord                 `json:"mappings"`
	ContractKinds map[string]schemas.ContractKind `json:"contractKinds"`
	Errors        []GraphConfigError              `json:"errors"`
}
