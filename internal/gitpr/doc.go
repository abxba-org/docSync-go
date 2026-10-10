// Package gitpr is the Go port of packages/core/src/gitpr — the Phase 6 doc-PR
// write path's deterministic core (P6-T01..T28; PLAN §7.3; ARCH §12.2-§12.5;
// binding invariants 1/2/3/4/7/10/11/12; postcondition 19 — NO model inference
// anywhere in this package).
//
// The barrel (TS index.ts) re-exported every gitpr module; Go needs no barrel,
// so each TS module lands as one file of this package:
//
//	branch-factory.ts   → branch-factory.go    (concurrent delegate: NOT this unit)
//	commit-assembly.ts  → commit-assembly.go   (concurrent delegate: NOT this unit)
//	content-guard.ts    → content-guard.go     (concurrent delegate: NOT this unit)
//	client.ts           → client.go            (concurrent delegate: NOT this unit)
//	backoff.ts          → backoff.go           (concurrent delegate: NOT this unit)
//	check-runs.ts       → check-runs.go        (concurrent delegate: NOT this unit)
//	github-rest-platform.ts → github-rest-platform.go (THIS unit)
//	governors.ts        → governors.go         (THIS unit)
//	idempotency.ts      → idempotency.go       (THIS unit)
//	incident-log.ts     → (seam in doc-pr-flow.go; canonical file unowned this wave)
//	merge-ledger.ts     → merge-ledger.go      (THIS unit)
//	merge-target.ts     → merge-target.go      (THIS unit)
//	noop-drop.ts        → (seam in doc-pr-flow.go; canonical file unowned this wave)
//	partial-pass.ts     → (seam in doc-pr-flow.go; canonical file unowned this wave)
//	pr-body.ts          → pr-body.go           (THIS unit)
//	pr-filter.ts        → pr-filter.go         (THIS unit)
//	pr-platform.ts      → pr-platform.go       (THIS unit — the canonical home of
//	                                            the platform interface)
//	rebase-gate.ts      → (FlowReverifyPort seam in doc-pr-flow.go; canonical
//	                       file unowned this wave)
//	self-trigger.ts     → self-trigger.go      (THIS unit)
//	supersede.ts        → (seam in doc-pr-flow.go; canonical file unowned this wave)
//	doc-pr-flow.ts      → doc-pr-flow.go       (THIS unit — the heart of the
//	                                            Phase 6 write path)
//
// PORTS AND SEAMS (the wiring discipline this package is built on):
//
//   - The platform is an INJECTABLE INTERFACE: PrPlatformPort (pr-platform.go) is
//     the canonical home of the seam every flow consumes. Tests inject fakes —
//     InMemoryPrPlatform (the port of the TS test double, keyed by repo + PR
//     number with mutable state) and github-rest-platform.go's scripted
//     GitHubRestTransport (a function seam keyed by request identity). Planned
//     platforms (D1-D5: GitLab, Bitbucket, Azure DevOps) implement the same
//     seven methods; the flow never learns the platform's identity.
//   - Git access flows through the injected narrow port PrFlowGitPort, which the
//     exec wrapper's *gitx.GitClient satisfies directly.
//   - State flows through the injected MergeLedgerStateStore (the two merge-ledger
//     methods of *state.StateStore) — never through a store the flow constructs.
//   - Safety incidents flow through the injected IncidentRecorder seam.
//   - The clock is injected (Clock func() string); the burst delay is injected
//     (BurstDelay); the RNG is injected (deps.RNG). Nothing in this package reads
//     a wall clock, a random source, or the network on its own.
//
// LOCAL SEAMS (CONVENTIONS.md "Ported files needing new Go seams ..."):
// doc-pr-flow.go declares small `seam`-prefixed unexported helpers for the
// symbols whose canonical files are written by the CONCURRENT delegate
// (branch-factory.go, commit-assembly.go, content-guard.go, client.go) or are
// unowned this wave (partial-pass, noop-drop, supersede, incident-log,
// authority-surface, rebase-gate). Every seam carries a TODO naming its
// canonical file; when those files land, delete the seam and wire the canonical
// symbol. The seam names are deliberately unexported and `seam`-prefixed so they
// can NEVER collide with the canonical exports.
//
// The determinism laws this package enforces end-to-end:
//
//	rule 1   writes only to docsync/* branches (INV-1 guard, never the default
//	         branch — assertNotDefaultBranch + the factory scan)
//	rule 4   one doc PR per source PR (idempotency-keyed upsert, D4 per-document
//	         partial pass — a failing doc is dropped and REPORTED, never
//	         silently)
//	D5       the PR body summary is the canonical provenance record
//	rule 11  no flood — rate-limited (hourly burst), idempotent (marker-keyed
//	         upsert), self-trigger-suppressed (docsync/* author/branch)
//	rule 12  whole-document rewrites rejected (the patch engine refuses them;
//	         the no-op simulation runs THROUGH the engine, so the guard applies)
//	D12      baselines never silently survive history rewrites (merge-target
//	         resolution on merge — merge-target.go)
package gitpr
