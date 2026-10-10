package contracts

import (
	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// GraphConfigErrorKind enumerates the typed configuration-error kinds surfaced
// by graph composition (SEAM: mirrors internal/graph GraphConfigErrorKind /
// GRAPH_CONFIG_ERROR_KIND_VALUES). Used verbatim in mapping-proposal errors.
type GraphConfigErrorKind string

const (
	GraphErrorConceptsDocNotInManifest     GraphConfigErrorKind = "concepts_doc_not_in_manifest"
	GraphErrorDuplicateConceptName         GraphConfigErrorKind = "duplicate_concept_name"
	GraphErrorConceptReferencesUnknownDoc  GraphConfigErrorKind = "concept_references_unknown_doc"
	GraphErrorDuplicateClaimID             GraphConfigErrorKind = "duplicate_claim_id"
	GraphErrorDuplicateContractDeclaration GraphConfigErrorKind = "duplicate_contract_declaration"
	GraphErrorUnknownContractKind          GraphConfigErrorKind = "unknown_contract_kind"
)

// GraphConfigError is a concepts.yml configuration error (fail closed, D8).
// SEAM: mirrors internal/graph GraphConfigError.
type GraphConfigError struct {
	Kind    GraphConfigErrorKind `json:"kind"`
	Path    string               `json:"path"`
	Message string               `json:"message"`
	Line    *int                 `json:"line,omitempty"`
}

// MappingRecord is one document's declarative mapping (verbatim manifest globs
// + concepts data). SEAM: mirrors internal/graph MappingRecord. The rich
// Phase-11 edge fields (Apis/Configs/Workflows) are OPTIONAL; an absent field
// (nil) means no edges of that kind, keeping Phase-1 records byte-compatible.
type MappingRecord struct {
	Doc       string   `json:"doc"`
	Sources   []string `json:"sources"`
	Concepts  []string `json:"concepts"`
	Contracts []string `json:"contracts"`
	Apis      []string `json:"apis,omitempty"`
	Configs   []string `json:"configs,omitempty"`
	Workflows []string `json:"workflows,omitempty"`
}

// Graph is the composed document graph. SEAM: mirrors internal/graph
// DocumentGraph. A graph with non-empty Errors is UNUSABLE (fail closed);
// consumers must refuse to route on it. ContractKinds is the validated
// concepts.yml contractKinds map (unknown-kind entries are dropped by
// composition; undeclared names classify as 'content' downstream).
type Graph struct {
	Mappings      []MappingRecord                 `json:"mappings"`
	ContractKinds map[string]schemas.ContractKind `json:"contractKinds"`
	Errors        []GraphConfigError              `json:"errors"`
}

// globMetaChars is the set of glob metacharacters recognized by the docSync glob
// engine (picomatch syntax). A source entry containing NONE of these is a
// LITERAL path (pins one file); conservative by design (SEAM: mirrors
// internal/graph GLOB_META_CHARS / isLiteralSourcePattern).
const globMetaChars = "*?[]{}!()"

// isLiteralSourcePattern reports whether pattern contains no glob
// metacharacters (a pinned literal path).
func isLiteralSourcePattern(pattern string) bool {
	if len(pattern) == 0 {
		return false
	}
	for i := 0; i < len(pattern); i++ {
		for j := 0; j < len(globMetaChars); j++ {
			if pattern[i] == globMetaChars[j] {
				return false
			}
		}
	}
	return true
}

// SourceMatchesPath reports whether a mapping source entry matches the changed
// path (literal or glob). SEAM: mirrors internal/graph sourceMatchesPath. Literal
// entries match by exact path equality (picomatch is never consulted, so a
// literal path containing glob-significant characters still matches itself);
// glob entries compile through the core glob engine (which enforces the §11.3
// escape guards). A guard-failing glob returns the globs package's *GlobGuardError
// rather than guessing (fail closed, D8) — the Go form of the TS thrown error.
func SourceMatchesPath(source, changedPath string) (bool, error) {
	if isLiteralSourcePattern(source) {
		return source == changedPath, nil
	}
	matcher, err := globs.CompileGlob(source)
	if err != nil {
		return false, err
	}
	return matcher.Match(changedPath), nil
}

// ForwardLookup returns the mapping record for doc, or nil when the doc is
// unmapped (SEAM: mirrors internal/graph forwardLookup). First match wins —
// mapping order is manifest order.
func ForwardLookup(graph *Graph, doc string) *MappingRecord {
	for i := range graph.Mappings {
		if graph.Mappings[i].Doc == doc {
			return &graph.Mappings[i]
		}
	}
	return nil
}
