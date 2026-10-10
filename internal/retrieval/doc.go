// Package retrieval is the Go port of the docSync retrieval engine
// (P1-T13/T14; ARCH §4.5): level-hierarchy candidate retrieval over the
// strictly ordered level hierarchy — 1 literal manifest, 2 glob path mapping,
// 3 symbol mapping and 4 API/schema mapping (P11-T04: evidence-conditional
// activation, MANDATORY per-level structured provenance), 5 concept siblings,
// 6 lexical semantic fallback (OD-R37) — plus evidence-conditional declared
// gap rows for levels 3–4 (D14/OD-4, never silent) and the V-P1-8 guard that
// recall-only candidates can never drive an automatic action.
//
// Ported files (traceability):
//
//   - types.go    — packages/core/src/retrieval/types.ts (level/evidence/
//     provenance/candidate types, INACTIVE_LEVEL_REASON, INACTIVE_LEVELS,
//     canDriveAutomaticAction)
//   - engine.go   — packages/core/src/retrieval/engine.ts (retrieveCandidates,
//     RetrieveCandidatesInput, RetrievalConfigError)
//   - semantic.go — packages/core/src/retrieval/semantic.ts (SemanticIndex,
//     SemanticShard, SemanticHit, QueryIndexOptions, semanticTokens,
//     buildSemanticIndex, refreshShard, isStale, queryIndex)
//
// Purity/determinism: the semantic index is INJECTED and never mutated by the
// engine; retrieval performs no I/O and reads no wall clock. Identical inputs
// produce an identical result: manifest-order doc iteration, sorted concept
// expansion, integer lexical scores and a total-order ranking (level ascending,
// score descending, path ascending).
//
// Dependencies: internal/schemas (ChangeAnalysis, Claim, Manifest), internal/graph
// (DocumentGraph, MappingRecord, IsLiteralSourcePattern, SourceMatchesPath,
// GraphUnusableError) and internal/docutil (ContentHash for shard content
// hashes). The engine consumes the ANALYSIS evidence inputs the retrieval
// engine reads (the classified OpenAPI delta and the key-level config diffs,
// internal/analysis/* in the TypeScript reference) through local SEAM types
// declared in engine.go — the same documented seam pattern internal/contracts
// used — carrying only the fields retrieval reads (delta locations, config
// keys). When internal/analysis lands, the swap is mechanical.
package retrieval
