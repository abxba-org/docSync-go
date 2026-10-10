package verifier

// Tier-2 coverage-defect check (port of tier2/coverage.ts; P4-T14; PLAN §5.4
// item 14; ARCH §4.4): a document that DECLARES contracts but holds zero
// checkable claims has no verification material for those contracts — a
// coverage defect surfaced through the check surface (Phase 7) and run records
// (Phase 5), never silent.
//
// The determination is contracts.ContractCoverage (the Go port of core's
// contractCoverage, P1-T12) over the injected graph snapshot (D7) and the
// store-claim snapshot: a doc entry with `ZeroClaims: true` skips the check
// WITH the defect detail — a coverage defect is missing evidence, not a wrong
// patch, so it never fails the tier; it is surfaced so the lint surface and
// reviewers see it. A doc with declared contracts and ≥1 checkable claim
// passes.
//
// Pure and deterministic (module doc): no fs, no git, no network, no clock of
// its own — the same inputs always yield deep-equal records.

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// tier2ContractsGraph adapts the injected internal/graph DocumentGraph to the
// contracts package's coverage entry point (the SEAM contracts/doc.go
// declares: contracts intentionally does not import graph, so the shapes are
// field-identical and this thin adapter performs the swap). Errors are copied
// verbatim (kind values are identical strings) so the adapter drops nothing.
func tier2ContractsGraph(g *graph.DocumentGraph) *contracts.Graph {
	if g == nil {
		return nil
	}
	mappings := make([]contracts.MappingRecord, 0, len(g.Mappings))
	for i := range g.Mappings {
		m := &g.Mappings[i]
		mappings = append(mappings, contracts.MappingRecord{
			Doc:       m.Doc,
			Sources:   m.Sources,
			Concepts:  m.Concepts,
			Contracts: m.Contracts,
			Apis:      m.APIs,
			Configs:   m.Configs,
			Workflows: m.Workflows,
		})
	}
	errors := make([]contracts.GraphConfigError, 0, len(g.Errors))
	for i := range g.Errors {
		e := &g.Errors[i]
		errors = append(errors, contracts.GraphConfigError{
			Kind:    contracts.GraphConfigErrorKind(e.Kind),
			Path:    e.Path,
			Message: e.Message,
			Line:    e.Line,
		})
	}
	return &contracts.Graph{
		Mappings:      mappings,
		ContractKinds: g.ContractKinds,
		Errors:        errors,
	}
}

/* ---------------------------------------------------------------- exports */

// CheckCoverageDefect runs the coverage-defect check for the patched document.
// All outcomes emit one record; inapplicable inputs skip WITH detail — never
// silent. Pure and deterministic (module doc).
func CheckCoverageDefect(input Tier2Input) schemas.VerificationCheck {
	if input.Graph == nil {
		return schemas.VerificationCheck{
			ID:     "coverage-defect",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr("no graph supplied"),
		}
	}
	if len(input.ContractNames) == 0 {
		return schemas.VerificationCheck{
			ID:     "coverage-defect",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr("no declared contracts for this document"),
		}
	}

	doc := ""
	if input.Patch != nil {
		doc = input.Patch.DocPath
	}
	coverageEntries := contracts.ContractCoverage(tier2ContractsGraph(input.Graph), input.Claims)
	var entry *contracts.ContractCoverageEntry
	for i := range coverageEntries {
		if coverageEntries[i].Doc == doc {
			entry = &coverageEntries[i]
			break
		}
	}
	if entry == nil {
		return schemas.VerificationCheck{
			ID:     "coverage-defect",
			Status: schemas.CheckPass,
			Detail: tier2StringPtr(fmt.Sprintf(
				"no contract coverage entry for %s (the graph mapping declares no contracts)", doc)),
		}
	}
	if entry.ZeroClaims {
		return schemas.VerificationCheck{
			ID:     "coverage-defect",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr(fmt.Sprintf(
				"coverage defect: %s declares %d contracts but has zero checkable claims "+
					"(surfaced to the lint surface; never silent)", doc, len(entry.Contracts))),
		}
	}
	return schemas.VerificationCheck{
		ID:     "coverage-defect",
		Status: schemas.CheckPass,
		Detail: tier2StringPtr(fmt.Sprintf(
			"contract coverage satisfied: %s declares %d contract(s) with at least one checkable claim",
			doc, len(entry.Contracts))),
	}
}
