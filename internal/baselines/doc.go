// Package baselines is the Go port of packages/core/src/baselines
// (P1-T15/T16; ARCH §6.1, D12): the baseline store with the §6.1 CAS guard
// semantics (R15), the pure invalidation engine, and the D12
// baseline-identity ladder (exact SHA → same tree hash → merge-base /
// patch-id → orphaned-suspect).
//
// The module files, per plan.md §7 traceability:
//
//   - store.go      <- baselines/store.ts      (§6.1 CAS-guarded baseline store)
//   - invalidate.go <- baselines/invalidate.ts (the pure invalidation engine)
//   - ladder.go     <- baselines/ladder.ts     (the D12 resolution ladder)
//
// The TypeScript barrel (baselines/index.ts) is orchestrator-owned per
// CONVENTIONS.md, so this file records the exports it would wire:
//
//	store.go:      BaselineStore, BaselineGuards, BaselineStoreBackend,
//	               BaselineUpsert (+ BaselineUpsertConflict values)
//	invalidate.go: EvaluateInvalidation, EvaluateSourceIntersection,
//	               BaselineOrphanSignals (+ the Invalidation* /
//	               SourceOrphanSignal types)
//	ladder.go:     ResolveBaseline, ClampSearchDepth, DefaultSearchDepth,
//	               MaxSearchDepth (+ LadderResolution, LadderRun,
//	               ResolveBaselineInput)
//
// Package-local ports of graph/lookup.ts's isLiteralSourcePattern /
// sourceMatchesPath live in invalidate.go: internal/graph is not ported yet,
// and the invalidation engine is the only current consumer.
//
// The import DAG in plan.md §3 is honored: this is an L2 package importing
// only docutil + schemas (L0) and state, globs, gitx (L1). No state bytes
// are written here directly — every read/write goes through the state
// store's baseline APIs (state.StateStore already satisfies
// BaselineStoreBackend structurally), which own the byte-level CAS.
package baselines
