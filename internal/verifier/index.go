// Ported from packages/verifier/src/index.ts (131 lines).
//
// In Go there is no barrel: a doc-surface listing of the package API this
// unit owns (plus the concurrently-landed peer surfaces it composes). No
// re-exports — importers reference the symbols directly.
//
// This unit's API surface:
//
//	pipeline (pipeline_verify_patch.go — pipeline/verify-patch.ts):
//	  VerifyPatch(VerifyPatchInput) (*schemas.VerificationReport, error) — the
//	    three-tier composition (all three tiers always run, ARCH §4.8) behind
//	    the fail-closed input gate, the verified-claim-refs gate (P11-T15,
//	    OD-R26), the wave-4-flagged judge-abstain synthesis (invariant 9) and
//	    the report self-check; verdict and auto gate via the schema's
//	    canonical helpers (OD-2).
//	  ComputePatchRef(*schemas.DocPatch) (string, error) — the deterministic
//	    content-addressed patch ref.
//	  VerifyPatchInput, PatchNotVerifiableError (+ PatchNotVerifiableCode).
//	  All three tiers are called directly: RunTier1 (tier1.go), RunTier2
//	  (tier2.go), RunTier3 (tier3.go) — the landed tier entrypoints.
//
//	orchestrator stage (verification_stage.go — orchestrator/verification-stage.ts):
//	  CreateVerificationStagePort(VerificationStageDeps) (VerificationStagePort, error)
//	    — the deterministic verification stage: RunContext-driven input
//	    derivation, the composition-time D13 writer/judge session-collision
//	    assert, and optional append-only record persistence. Typed
//	    VerifyPatch failures propagate; the runner degrades them to review
//	    (`verifier_uncertain`).
//	  VerificationStageDeps, VerificationStagePort, VerificationStageInput,
//	  VerificationStageResult, VerificationRecordSaver, RunContext (the
//	  verifier-side run-context view), JudgeWriterSessionCollisionError (+
//	  JudgeWriterSessionCollisionCode).
//
//	bin (bin.go — src/bin.ts): the docsync-audit CLI entrypoint (Phase 9,
//	  P9-T20/P9-T21; PLAN §10.7). Kept minimal — it becomes
//	  cmd/docsync-verifier later (the orchestrator wires it).
//
// Peer surfaces landed by concurrent delegates in this package (NOT owned
// here): tier1* (RunTier1 + types), tier2* (RunTier2, DecomposePatchClaims +
// types), tier3* (RunTier3 + types), cross_patch (checkCrossPatchConsistency
// / applyCrossPatchConflict), replay (NewAuditReplay / RenderAuditTrail /
// AuditInputError), storage (VerificationRecordStore).
package verifier
