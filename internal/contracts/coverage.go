package contracts

import "github.com/DUDE616/docSync-go/internal/schemas"

// ContractCoverageEntry is one document's contract coverage report.
type ContractCoverageEntry struct {
	Doc string `json:"doc"`
	// Contracts are the declared contract names (mapping order).
	Contracts []string `json:"contracts"`
	// ZeroClaims is true when the doc declares contracts but holds zero
	// checkable claims (a coverage defect — flagged, never invented/dropped).
	ZeroClaims bool `json:"zeroClaims"`
}

// ContractCoverage reports contract coverage over every document that DECLARES
// contracts (manifest + concepts.yml composition). Deterministic: mappings in
// graph order.
//
// "Checkable claim" = a claim anchored to the doc that is not `closed`
// (verified/suspect/unanchored/stale all remain checkable — closed claims are
// retired). The input claims are the store's live claim set.
func ContractCoverage(graph *Graph, claims []schemas.Claim) []ContractCoverageEntry {
	entries := []ContractCoverageEntry{}
	for i := range graph.Mappings {
		mapping := &graph.Mappings[i]
		if len(mapping.Contracts) == 0 {
			continue
		}
		checkable := false
		for _, claim := range claims {
			if claim.Anchor.File == mapping.Doc && claim.Status != schemas.ClaimStatusClosed {
				checkable = true
				break
			}
		}
		entries = append(entries, ContractCoverageEntry{
			Doc:        mapping.Doc,
			Contracts:  append([]string{}, mapping.Contracts...),
			ZeroClaims: !checkable,
		})
	}
	return entries
}
