// Package verifier is the Go port of packages/verifier (L4 in plan.md §3;
// ARCH §4.8: three-tier verification with judge independence — no patch is
// applied or shipped without passing all tiers, whole-document rewrites are
// rejected (minimal diffs only), and failures degrade to REVIEW, never to a
// silent pass).
//
// The unit files landed by the storage + audit-replay migration unit, per
// plan.md §2 traceability:
//
//   - replay.go          <- verifier/src/replay.ts            (audit replay)
//   - storage_records.go <- verifier/src/storage/records.ts   (append-only ledger)
//
// The remaining verifier modules (tier1, tier2, tier3, pipeline, consistency,
// orchestrator stage) are owned by other delegates; this file is the shared
// package doc and deliberately describes only the landed surface.
//
// storage_records.go (P4-T20; PLAN §5.3/§5.4; ARCH §6.3) is the append-only,
// content-addressed verification-record store over the D1 doc-state records:
// every emitted `docsync.verification-report/v1` is embedded whole in the
// doc-state ledger and content-addressed by `report_hash` (BARE 64-hex sha256
// over the CANONICAL JSON of the report — the OD-R42 bare-digest convention;
// the dedup key), plus the verifier-vs-human disagreement log (R5) that feeds
// Phase-10 policy recalibration. Entries live INSIDE the existing D1
// doc-state layout (OD-R44: no new `state/` subdirectory). Save/list/
// recordDisagreement is the entire surface — no delete, no update, no
// reorder (audit replay depends on the ledger being append-only). Dedup: a
// report already present under its `report_hash` is a no-op
// (`created:false`, NO write); a disagreement dedupes on EXACTLY the 4-tuple
// (run_id, verifier_verdict, human_outcome, reason) — `at` and `patch_ref`
// are EXCLUDED from the identity. Fail closed: a MISSING doc-state record is
// a typed `VerificationStorageError` (the orchestrator initializes document
// state first; state is never invented here), and a CAS conflict is the same
// typed error with the loser re-running, never overwriting (R15 posture).
//
// replay.go (Phase 9, P9-T20/P9-T21; PLAN §10.7) reconstructs why a
// documented claim changed for a run from a COMMITTED state ALONE (V1
// files-only, OD-3 — `.docsync/state/` is committed to Git): the fix-run
// (decision) record, the provenance record, the verification verdicts from
// the per-doc ledgers, and the safety incidents. No git, no network, no
// GitHub. Read posture: evidence is rendered VERBATIM — the replay indexes
// records by their `schema` tag and `run_id` field but never re-validates
// them (validation happened at write time, fail closed). Missing evidence is
// a DECLARED GAP in the trail, never fabricated; output is deterministic and
// bounded scans fail loud past AUDIT_SCAN_CAP.
//
// The TypeScript barrel (verifier/src/index.ts) is orchestrator-owned per
// CONVENTIONS.md, so this file records the exports it would wire for the two
// ported modules:
//
//	replay.go:          NewAuditReplay, AuditReplayOptions, AuditReplayInput,
//	                    AuditReplay, AuditReplay.ReplayRun, RenderAuditTrail,
//	                    AuditTrail, AuditTrailVerification, AuditTrailIncident,
//	                    AuditInputError, AuditScanCap
//	storage_records.go: NewVerificationRecordStore, VerificationRecordStore,
//	                    (VerificationRecordStore).Save / .List /
//	                    .RecordDisagreement, VerificationSaveOutcome,
//	                    DisagreementOutcome, DisagreementInput, DocStateStore,
//	                    VerificationStorageError, CodeVerificationStorage
//
// Error mapping (CONVENTIONS.md §Errors): the TS classes carried codes that
// are not docutil codes (AuditInputError ⇒ CLI config error, exit 2;
// VerificationStorageError ⇒ 'VERIFICATION_STORAGE'). Class identity is
// preserved by the Go error TYPES, the TS constructor message text is
// preserved VERBATIM in Error(), and each error unwraps to the closest
// registered docutil code for routing (AuditInputError ⇒ EDOCSYNC_CONFIG,
// VerificationStorageError ⇒ EDOCSYNC_VERIFY).
//
// Import DAG (plan.md §3): this is an L4 package importing only docutil and
// schemas (L0) and state (L1). No import cycles.
package verifier
