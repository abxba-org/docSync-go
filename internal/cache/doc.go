// Package cache is the Go port of the docSync cache module (P1-T19; OD-R05):
// the in-memory bounded LRU over ChangeAnalysis records keyed by the canonical
// composite key (repo, base/head SHAs, plugin lineup ORDER, caps, flags), and
// the declared-unavailable per-blob-SHA symbol-index interface stub (the real
// cache lands Phase 11, P11-T02 — caches are ephemeral by default in Phase 1,
// no `state/` file persistence).
//
// Ported files (traceability):
//
//   - analysis-cache.go — packages/core/src/cache/analysis-cache.ts
//     (AnalysisCacheKeyInput, analysisCacheKey, AnalysisCache LRU)
//   - symbol-index.go   — packages/core/src/cache/symbol-index.ts
//     (SymbolIndex, SymbolIndexResult, UnavailableSymbolIndex,
//     SYMBOL_INDEX_UNAVAILABLE_REASON, createUnavailableSymbolIndex)
//
// Inconclusive outcomes are NOT cacheable (fail closed: re-run, never remember a
// transient failure). The symbol-index stub NEVER throws and NEVER fabricates:
// every probe degrades explicitly to the typed `unavailable` result and is
// counted, so consumption attempts are observable.
//
// Dependencies: internal/schemas (ChangeAnalysis) and internal/docutil
// (CanonicalJSON/ContentHash for the canonical key; coded errors). The deep
// analysis caps shape (internal/analysis AnalysisCaps in the TypeScript
// reference) is mirrored by the local SEAM type AnalysisCaps in
// analysis-cache.go — only the two budget fields the key encodes are read.
package cache
