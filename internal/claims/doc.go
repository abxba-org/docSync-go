// Package claims ports packages/core/src/claims (P1-T10/T11; ARCH §4.3):
// the CAS-guarded claim store over the StateStore's registry-enforced claim
// API, content-hash-first anchor resolution (invariant 13 — never a silent
// re-anchor), hand-anchored/manifest-declared claim intake from the canonical
// concepts.yml (OD-R36), and typed status transitions with append-only
// history. It also carries the claim-invalidation wiring (ported from
// packages/core/src/analysis/claim-invalidation.ts, P11-T06; V-P11-1) because
// that module is pure wiring OVER the claim state machine — the deterministic
// zero-LLM entry gate routes symbol evidence through THIS package's own
// transition functions, with no side-door write.
//
// Ported files (one TS source per Go file):
//
//	anchor.go        <- core/src/claims/anchor.ts         (HashSpan, SplitDocLines, ResolveAnchor)
//	store.go         <- core/src/claims/store.ts          (ClaimStore, MergeHistory)
//	status.go        <- core/src/claims/status.ts         (LoseSupport, MarkUnanchored, MarkStale, CloseClaim, ApplyReanchor)
//	intake.go        <- core/src/claims/intake.ts         (IntakeClaims)
//	invalidation.go  <- core/src/analysis/claim-invalidation.ts
//	                                                     (ClaimInvalidationRules, DeriveClaimInvalidations, ApplyClaimInvalidations)
//	claims_test.go   <- packages/core/test/claims.test.ts + claim-invalidation.test.ts
//
// Behavioral invariants preserved from ARCH §4.3/§6.3: the content hash is the
// anchor's IDENTITY (line numbers are a locator, never the truth); an
// ambiguous re-anchoring is NEVER applied silently (0 matches or >1 matches →
// unanchored → REVIEW); there is no delete and history is append-only and
// never rewritten; every write is CAS-guarded so a concurrent writer wins and
// the loser re-runs (R15).
package claims
