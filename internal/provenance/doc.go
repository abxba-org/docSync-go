// Package provenance is the Go port of packages/core/src/provenance
// (P9-T01..T07; ARCH §12.5; PLAN §10.3/§10.4; principle 10 — "provenance or it
// didn't happen"). Every automated documentation change carries its evidence
// record: the actually-routed models, the policy version, and the verification
// result, never fabricated afterwards.
//
// The D5 storage precedence is the package's spine (three surfaces, one
// record):
//
//   - CANONICAL  — the PR-body summary rendered by pr-body.go
//     (`renderProvenanceSummary`): reviewers read the record there, like any
//     other reviewable text.
//   - ARCHIVAL   — the content-addressed, APPEND-ONLY store in store.go
//     (`state/runs/prov-<run_id>.json`, OD-R44: no new state subdirs, CAS on
//     content hash, re-running a run is a byte-identical NO-OP, a history
//     rewrite is a typed refusal).
//   - EPHEMERAL  — the check-run projection in check-view.go
//     (`projectCheckRunMetadata`): a view over the record that can be deleted
//     without losing anything, because the other two surfaces own the truth.
//
// Markdown document bodies are NEVER modified for provenance: this text only
// ever lands in a PR body (D5 rule 5).
//
// The module files, per plan.md §2 traceability:
//
//   - record.go      <- provenance/record.ts      (pure builder + validation)
//   - store.go       <- provenance/store.ts        (append-only CAS archival store)
//   - check-view.go  <- provenance/check-view.ts   (ephemeral check-run view)
//   - pr-body.go     <- provenance/pr-body.ts      (canonical PR-body renderer)
//   - secret-scan.go <- provenance/secret-scan.ts  (write-path secret gate, INV-8)
//
// The TypeScript barrel (provenance/index.ts) is orchestrator-owned per
// CONVENTIONS.md, so this file records the exports it would wire:
//
//	record.go:      BuildProvenanceRecord, BuildProvenanceInput, RoutedModels,
//	                ProvenanceBuildError
//	store.go:       ProvenanceStore, NewProvenanceStore, ProvenanceStoreOptions,
//	                ProvenanceWriteResult, ProvenanceAppendError,
//	                NewProvenanceAppendError, ProvenanceRecordRelPath,
//	                ProvenanceRecordPrefix, ProvenanceStore.RecordFSPath
//	check-view.go:  ProjectCheckRunMetadata, CheckRunOutputPayload,
//	                CheckViewEphemeralMarker
//	pr-body.go:     RenderProvenanceSummary, PrBodyBanner
//	secret-scan.go: ScanProvenanceRecord, AssertNoSecrets, ProvenanceSecretError,
//	                ProvenanceSecretScan (value shape)
//
// Every file also exports the closest registered docutil code for its refusal
// class (CodeProvenanceBuild, CodeProvenanceSecret, CodeProvenanceRewrite), each
// a value already present in docutil — no new code was invented without
// orchestrator approval, as CONVENTIONS.md §Errors requires.
//
// Porting notes (deviations, listed in the unit report):
//
//   - The TypeScript builder throws a class per refusal and the store throws
//     `TypeError` for a non-absolute stateDir; Go returns errors instead. The
//     TS error CODES (`EDOCSYNC_PROVENANCE_BUILD`, `EDOCSYNC_PROVENANCE_SECRET`,
//     `EDOCSYNC_PROVENANCE_REWRITE`) are not in docutil's code list, so per
//     CONVENTIONS.md §Errors the closest registered codes carry them:
//     build/secret → EDOCSYNC_VALIDATION / EDOCSYNC_GUARD, rewrite →
//     EDOCSYNC_STATE. Class identity is preserved by the Go error TYPES.
//   - `PR_BODY_BANNER` is copied verbatim from gitpr/pr-body.ts (single banner
//     source there; internal/gitpr is not ported yet) — replace the local
//     constant with the gitpr export once that package lands.
//   - `CheckRunOutputPayload` is likewise a local mirror of
//     gitpr/check-runs.ts's interface; Phase 8's check-run client owns the
//     real type once ported.
//
// Import DAG (plan.md §3): this is an L5 package importing only docutil,
// schemas (L0), state and boundaries (L1). No import cycles.
package provenance
