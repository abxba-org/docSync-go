// Package audit is the Go port of packages/core/src/audit (P11-T18..P11-T21,
// P12-T15; ARCH §7.2 Loop B — Background drift audit, §6.3 audit-cursor,
// §15.3 org reporting; CTX §42; OD-R32) — the READ-MOSTLY background audit
// engine.
//
// The audit engine computes drift findings over repository state and emits
// them as DATA. It performs NO document writes and NO patch application ever
// (ARCH §7.2: findings surface as advisory artifacts, never auto-writes;
// remediation routes ONLY via the SUGGEST path — the same verification +
// policy engine as Loop A, one code path, two triggers).
//
// Module map (one file per TypeScript source, ports cited verbatim):
//
//	types.go        <- core/src/audit/types.ts        (findings, ports, results)
//	dedup.go        <- core/src/audit/dedup.ts        (claim-ID dedup rule)
//	incremental.go  <- core/src/audit/incremental.ts  (chunked cursor-resumed engine)
//	scheduler.go    <- core/src/audit/scheduler.ts    (Loop B trigger contract)
//	artifacts.go    <- core/src/audit/artifacts.ts    (advisory markdown renderers)
//	store.go        <- core/src/audit/store.ts        (append-only findings store)
//	suggest.go      <- core/src/audit/suggest.ts      (findings -> SUGGEST path)
//	compose.go      <- core/src/audit/compose.ts      (composed Loop B runtime)
//	org-report.go   <- core/src/audit/org-report.ts   (org-wide drift report)
//	seams.go        <- unlanded-dependency seams (internal/orchestrator,
//	                   internal/evaluation): declared here, never imported.
//
// Ports are injectable by design: the engine owns chunking, the audit cursor,
// dedup, idempotency and trigger discipline; the manifest doc list, the
// current audit position and the per-document drift predicates are wired by
// the caller (the W5b2 wiring wave). Determinism: every clock value and every
// record id arrives from the caller (AuditRunOptions.Now, the caller-assigned
// run id) — nothing in this package reads the wall clock, and no ULID is
// minted here (docutil.NewULIDGeneratorWith exists for callers that need
// deterministic ids).
//
// Provenance or it didn't happen (D5): every run composes a
// `docsync.drift-audit-run-record/v1` and every finding is persisted as a
// `docsync.drift-finding/v1` row in the append-only store.
//
// Import DAG (plan.md §3): this is an L4 package importing only docutil (L0),
// schemas (L0), state (L1), gitx (L1), claims (L2), drift (L3), feedback (L3)
// and workspace (L3). It never imports policy or evaluation (OD-1 topology),
// and it never imports a higher layer.
package audit
