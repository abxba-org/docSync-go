// Package contracts ports the docSync contract module (P1-T12; ARCH §4.4) from
// TypeScript to Go: per-contract classification vectors over the ChangeAnalysis
// (bootstrap signal policy + review threshold, D11), zero-claims coverage defect
// flags, and reviewable mapping-proposal automation (P11-T11; INV-3).
//
// Source files ported from packages/core/src/contracts/:
//
//   - classify.ts          → classify.go   (classifyContracts + policy data)
//   - coverage.ts          → coverage.go   (contractCoverage)
//   - mapping-proposals.ts → mapping-proposals.go (deriveMappingProposals)
//
// SEAM — the graph dependency (see "SEAM" below) and the analysis dependency
// (OpenAPI delta classification, key-level config diffs) are not on disk yet.
// This package intentionally does NOT import internal/graph or internal/analysis
// so it builds stand-alone; instead it declares the minimal input shapes it
// consumes as LOCAL SEAM types:
//
//   - graph.go declares Graph, MappingRecord and GraphConfigError plus the two
//     graph lookups (`ForwardLookup`, `SourceMatchesPath`) the classifier needs.
//     These mirror internal/graph's DocumentGraph/MappingRecord/GraphConfigError
//     and its forwardLookup/sourceMatchesPath. When internal/graph lands, this
//     package should migrate to graph.DocumentGraph/graph.MappingRecord and the
//     graph package's lookups — the field names and semantics are kept identical
//     so the swap is mechanical (a thin adapter or an alias).
//   - classify.go declares OpenApiTier and the ApiDelta/ConfigKeyDiff input
//     shapes consumed from the OpenAPI delta classifier and the config-diff
//     module (internal/analysis/*). When those land, replace these with the
//     analysis types; the consumed fields (summary counts + worstTier, delta
//     locations, key + change) are the only ones read here.
//
// ChangeAnalysis and its ChangedFile / SymbolsDetail come from internal/schemas
// (landed) and are consumed directly — no seam needed there.
//
// Errors: MappingProposalInputError preserves the observable TS code
// EDOCSYNC_MAPPING_PROPOSAL_INPUT (see conventions) through a docutil coded
// error. Classify/coverage are total over valid inputs; ClassifyContracts
// returns an error only to propagate a hard glob-guard failure (fail closed, D8)
// exactly where the TS compileGlob would have thrown.
package contracts
