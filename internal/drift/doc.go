// Package drift is the Go port of packages/core/src/drift (P11-T16/P11-T17;
// ARCH §4.3, §6.1; CTX §11; PLAN §12.3 F11-9) — the deterministic, zero-LLM
// Loop B historical-drift analysis surface.
//
// Two modules, one package:
//
//   - differential.go <- core/src/drift/differential.ts (P11-T16)
//     AnalyzeDrift computes the baseline differential ARCH §6.1 binds as the
//     evidence set that must be re-evaluated:
//
//     diff baseline_commit..head  ∩  baseline scope
//
//     INTERSECT, never blanket (PRIN-2): only baseline-recorded source files,
//     only baseline-recorded symbol names, only baseline-recorded claim ids
//     are evaluated. Files/symbols/claims outside the recorded scope are
//     invisible to this module's output by construction — the report cannot
//     leak them.
//
//   - sweep.go <- core/src/drift/sweep.ts (P11-T17; V-P11-7)
//     RunClaimSupportSweep answers the headline query per claim: "which
//     documented claims lost support in the source of truth?" Anchor
//     resolution reuses claims.ResolveAnchor (content-hash-first, invariant
//     13 — never a silent re-anchor) and source support reuses the ARCH §4.3
//     blob-identity predicate. The sweep is a QUERY: it writes NOTHING by
//     default (report-only); on explicit opt-in a newly-lost-support claim
//     flips ONLY through the claim state machine's own CAS transition
//     functions (claims.LoseSupport / claims.MarkUnanchored) with the
//     caller's runId, and every flip and every CAS conflict (R15) is recorded
//     in the report.
//
// The TypeScript barrel (drift/index.ts) is orchestrator-owned per
// CONVENTIONS.md, so this file records the exports it would wire:
//
//	differential.go: AnalyzeDrift, AnalyzeDriftInput
//	sweep.go:        RunClaimSupportSweep, ClaimSupportSweepInput,
//	                 ClampCommitWalkDepth, DefaultCommitWalkDepth,
//	                 MaxCommitWalkDepth
//
// Output types (schemas.DriftReport, schemas.ClaimSupportReport) are the
// already-ported wire schemas in internal/schemas/drift.go; both reports are
// validated against their registry schema before they are returned (fail
// closed on any producer bug), exactly as the TS `SCHEMA.parse(report)` did.
// Advisory data, never a write instruction.
//
// The import DAG in plan.md §3 is honored: this is an L2/L3 package importing
// only docutil (L0), schemas (L0), state (L1), gitx (L1), claims (L2) and
// baselines (L2). No state bytes are written here directly — the sweep's
// flips go through claims.LoseSupport / claims.MarkUnanchored, which own the
// CAS-guarded write.
package drift
