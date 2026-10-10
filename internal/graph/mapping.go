package graph

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Mapping composition (P1-T09; ARCH §4.2; OD-R36). BuildDocumentGraph composes
// one MappingRecord per manifest document (manifest order, deterministic) from
// the manifest allowlist + the optional concepts.yml declarations.
//
// Fail-closed rules (each violation is a typed GraphConfigError; concepts EXTEND
// manifest docs, never invent them):
//   - a concepts doc entry whose path is not in the manifest contributes
//     NOTHING to the mappings (never invents a doc);
//   - a concept referencing an unknown (non-manifest) doc is reported; its
//     membership for KNOWN docs still stands;
//   - duplicate concept names, duplicate claim ids across docs, and duplicate
//     contract names on one doc are reported; the FIRST declaration wins so
//     the composition stays deterministic;
//   - contractKinds values outside the V1 kind set are reported and dropped
//     (schema-level CONTRACT_KIND rejects them too — this runtime check
//     defends callers that bypass schema validation).
//
// Line precision: when ConceptsRaw (the raw concepts.yml text) is supplied,
// errors carry best-effort 1-based YAML lines via a deterministic textual scan of
// the fixed concepts.yml shape; without it, errors still carry
// concepts.yml-relative paths.
type BuildDocumentGraphInput struct {
	Manifest    schemas.Manifest
	Concepts    *schemas.ConceptsConfig
	ConceptsRaw string
}

// conceptsLineIndex is a best-effort line index into the raw concepts.yml
// (1-based; empty when unavailable).
type conceptsLineIndex struct {
	docPaths          map[string]int
	conceptNames      map[string]int
	contractKindNames map[string]int
}

func emptyLineIndex() conceptsLineIndex {
	return conceptsLineIndex{
		docPaths:          map[string]int{},
		conceptNames:      map[string]int{},
		contractKindNames: map[string]int{},
	}
}

var (
	docEntryLineRE     = regexp.MustCompile(`^\s*-\s+path:\s*(.+?)\s*$`)
	conceptEntryLineRE = regexp.MustCompile(`^\s*-\s+name:\s*(.+?)\s*$`)
	kindLineRE         = regexp.MustCompile(`^\s*([^\s#][^:#]*?):\s*(api_schema|config|behavior|content)\s*(?:#.*)?$`)
)

func unquote(value string) string {
	n := len(value)
	if n >= 2 && (value[0] == '"' || value[0] == '\'') && value[n-1] == value[0] {
		return value[1 : n-1]
	}
	return value
}

// buildLineIndex scans the raw YAML (first occurrence wins per name). The
// concepts.yml shape is fixed (OD-R36), so docs[].path, concepts[].name and
// contractKinds keys are reliably locatable without a full AST walk; anything
// unusual simply yields no line.
func buildLineIndex(raw string) conceptsLineIndex {
	index := emptyLineIndex()
	if len(raw) == 0 {
		return index
	}
	for offset, line := range strings.Split(raw, "\n") {
		lineNumber := offset + 1
		if m := docEntryLineRE.FindStringSubmatch(line); m != nil {
			name := unquote(m[1])
			if len(name) > 0 {
				if _, exists := index.docPaths[name]; !exists {
					index.docPaths[name] = lineNumber
				}
			}
		}
		if m := conceptEntryLineRE.FindStringSubmatch(line); m != nil {
			name := unquote(m[1])
			if len(name) > 0 {
				if _, exists := index.conceptNames[name]; !exists {
					index.conceptNames[name] = lineNumber
				}
			}
		}
		if m := kindLineRE.FindStringSubmatch(line); m != nil {
			name := unquote(m[1])
			if len(name) > 0 {
				if _, exists := index.contractKindNames[name]; !exists {
					index.contractKindNames[name] = lineNumber
				}
			}
		}
	}
	return index
}

// BuildDocumentGraph composes the document graph. Deterministic: same inputs →
// same mappings, contractKinds and error list (declaration order preserved).
func BuildDocumentGraph(input BuildDocumentGraphInput) DocumentGraph {
	// errors is a non-nil empty slice when there are no errors so the graph
	// round-trips and compares deterministically (matches the TS `errors: []`).
	errors := []GraphConfigError{}
	lineIndex := buildLineIndex(input.ConceptsRaw)

	manifestDocSet := make(map[string]struct{}, len(input.Manifest.Documents))
	for _, entry := range input.Manifest.Documents {
		manifestDocSet[entry.Path] = struct{}{}
	}

	// Per-doc contracts data (first declaration wins; duplicates reported).
	contractsByDoc := map[string][]string{}

	if input.Concepts != nil {
		concepts := input.Concepts

		for docIndex := range concepts.Docs {
			entry := concepts.Docs[docIndex]
			pointer := fmt.Sprintf("docs[%d]", docIndex)
			line, hasLine := lineIndex.docPaths[entry.Path]
			if _, ok := manifestDocSet[entry.Path]; !ok {
				err := GraphConfigError{
					Kind:    GraphConfigErrConceptsDocNotInManifest,
					Path:    pointer + ".path",
					Message: fmt.Sprintf("concepts.yml declares doc %q which is not in the manifest — concepts extend manifest docs, never invent them (fail closed)", entry.Path),
				}
				if hasLine {
					err.Line = &line
				}
				errors = append(errors, err)
				continue
			}
			if entry.Contracts != nil {
				seen := map[string]struct{}{}
				accepted := contractsByDoc[entry.Path]
				for contractIndex, contract := range entry.Contracts {
					if _, dup := seen[contract]; dup {
						err := GraphConfigError{
							Kind:    GraphConfigErrDuplicateContractDecl,
							Path:    fmt.Sprintf("%s.contracts[%d]", pointer, contractIndex),
							Message: fmt.Sprintf("contract %q is declared more than once on doc %q — first declaration wins", contract, entry.Path),
						}
						if hasLine {
							err.Line = &line
						}
						errors = append(errors, err)
						continue
					}
					seen[contract] = struct{}{}
					accepted = append(accepted, contract)
				}
				contractsByDoc[entry.Path] = accepted
			}
		}

		// Duplicate claim ids ACROSS docs (every declaration participates, even
		// those on entries already flagged as non-manifest docs — fail closed).
		claimOwner := map[string]string{}
		for docIndex := range concepts.Docs {
			entry := concepts.Docs[docIndex]
			if entry.Claims == nil {
				continue
			}
			for _, declaration := range entry.Claims {
				if owner, dup := claimOwner[declaration.ID]; dup {
					errors = append(errors, GraphConfigError{
						Kind:    GraphConfigErrDuplicateClaimID,
						Path:    fmt.Sprintf("docs[%d].claims[id=%s]", docIndex, declaration.ID),
						Message: fmt.Sprintf("claim id %q is declared on docs %q and %q — claim ids are unique across concepts.yml", declaration.ID, owner, entry.Path),
					})
					continue
				}
				claimOwner[declaration.ID] = entry.Path
			}
		}
	}

	// Concept membership (first definition wins; duplicates + unknown refs reported).
	var conceptNames []string
	conceptDocs := map[string][]string{}
	if input.Concepts != nil {
		for conceptIndex := range input.Concepts.Concepts {
			entry := input.Concepts.Concepts[conceptIndex]
			pointer := fmt.Sprintf("concepts[%d]", conceptIndex)
			line, hasLine := lineIndex.conceptNames[entry.Name]
			if _, dup := conceptDocs[entry.Name]; dup {
				err := GraphConfigError{
					Kind:    GraphConfigErrDuplicateConceptName,
					Path:    pointer + ".name",
					Message: fmt.Sprintf("concept %q is declared more than once — first declaration wins", entry.Name),
				}
				if hasLine {
					err.Line = &line
				}
				errors = append(errors, err)
				continue
			}
			conceptNames = append(conceptNames, entry.Name)
			knownDocs := []string{}
			for docRefIndex, docRef := range entry.Docs {
				if _, ok := manifestDocSet[docRef]; !ok {
					err := GraphConfigError{
						Kind:    GraphConfigErrConceptReferencesUnknownDoc,
						Path:    fmt.Sprintf("%s.docs[%d]", pointer, docRefIndex),
						Message: fmt.Sprintf("concept %q references doc %q which is not in the manifest (fail closed)", entry.Name, docRef),
					}
					if hasLine {
						err.Line = &line
					}
					errors = append(errors, err)
					continue
				}
				knownDocs = append(knownDocs, docRef)
			}
			conceptDocs[entry.Name] = knownDocs
		}
	}

	// Contract kinds (validated; unknown values reported + dropped).
	contractKinds := map[string]schemas.ContractKind{}
	if input.Concepts != nil && input.Concepts.ContractKinds != nil {
		// Iterate contractKinds in sorted key order for deterministic output: a
		// Go map has no insertion order, so the TS Object.entries order cannot be
		// recovered from the decoded map. Sorting guarantees same inputs → same
		// error list (only affects the order of multiple unknown-kind errors).
		names := make([]string, 0, len(input.Concepts.ContractKinds))
		for name := range input.Concepts.ContractKinds {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			kind := input.Concepts.ContractKinds[name]
			if !schemas.IsValidContractKind(string(kind)) {
				line, hasLine := lineIndex.contractKindNames[name]
				err := GraphConfigError{
					Kind:    GraphConfigErrUnknownContractKind,
					Path:    fmt.Sprintf("contractKinds[%s]", name),
					Message: fmt.Sprintf("contract %q declares unknown kind %q — V1 kinds: %s", name, string(kind), joinContractKindValues()),
				}
				if hasLine {
					err.Line = &line
				}
				errors = append(errors, err)
				continue
			}
			contractKinds[name] = kind
		}
	}

	// Mappings: one per manifest entry, manifest order, deterministic.
	mappings := make([]MappingRecord, len(input.Manifest.Documents))
	for i, entry := range input.Manifest.Documents {
		docConcepts := []string{}
		for _, name := range conceptNames {
			for _, doc := range conceptDocs[name] {
				if doc == entry.Path {
					docConcepts = append(docConcepts, name)
					break
				}
			}
		}
		contracts := []string{}
		if c, ok := contractsByDoc[entry.Path]; ok {
			contracts = append(contracts, c...)
		}
		mappings[i] = MappingRecord{
			Doc:       entry.Path,
			Sources:   append([]string{}, entry.Sources...),
			Concepts:  docConcepts,
			Contracts: contracts,
		}
	}

	return DocumentGraph{Mappings: mappings, ContractKinds: contractKinds, Errors: errors}
}

// joinContractKindValues renders ContractKindValues as the comma-separated list
// used in error messages (CONTRACT_KIND_VALUES.join(', ')).
func joinContractKindValues() string {
	parts := make([]string, len(schemas.ContractKindValues))
	for i, k := range schemas.ContractKindValues {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}
