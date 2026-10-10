// Package patch ports the docSync patch-application layer (Phase 3,
// P3-T08..T16; ARCH §4.3, §4.9, §11.4 layer 3, §12.1/§12.3, A9; invariants
// 2/3/12/13) from TypeScript to Go. It is the DETERMINISTIC, PURE decision
// core: content-hash-first anchor resolution, whole-patch abstention semantics
// (typed outcomes, never semantic exceptions), the doc-changed-race 3-way merge
// gated on an injected re-verification pass, idempotency, the manifest-derived
// application guard, minimality measurement, and the R16 per-patch claim-value
// export.
//
// Ported files (one TS source per Go file):
//
//	types.go         <- core/src/patch/types.ts         (ApplyOutcome union, AppliedHunk,
//	                                                     StaleFinding, PatchEngineError)
//	anchor.go        <- core/src/patch/anchor.ts        (ResolveHunkAnchor, content-hash-first)
//	minimality.go    <- core/src/patch/minimality.ts    (MeasureMinimality, MeasuredMinimality,
//	                                                     DEFAULT_MINIMALITY_MAX_RATIO)
//	merge.go         <- core/src/patch/merge.ts         (ThreeWayMerge diff3, MAX_LCS_CELLS)
//	apply.go         <- core/src/patch/apply.ts         (ApplyDocPatch engine, ports)
//	export-claims.go <- core/src/patch/export-claims.ts (ExportPatchClaimValues,
//	                                                     DetectClaimValueConflicts)
//
// Import DAG (plan.md §3): patch is layer 3 and imports only lower layers —
// docutil/schemas (L0), claims/gitx (L1/L2), never openrouter or a net/fs
// surface (mirroring the TS static purity audit: the engine is pure over
// injected inputs; persistence is Phase 5/6's job, and the git READ port is
// consumed only when worktreeContent is absent).
//
// Behavior preservation: the application engine NEVER throws for a semantic
// outcome — every decision is a typed ApplyOutcome value (a guard refusal IS an
// abstain-kind outcome at the engine layer, not an exception). Only
// ENGINE-CONFIGURATION errors surface as a *PatchEngineError (a missing/typed-
// wrong guard input; fail closed, D8). Anchor/merge/minimality/export are pure
// functions with no clock and no I/O.
package patch
