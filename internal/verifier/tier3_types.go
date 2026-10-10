package verifier

// Ported from packages/verifier/src/tier3/types.ts (P4-T15..T17, T21; PLAN
// §5.4; ARCH §4.8 Tier-3): the inputs, check records, and result of the
// independent-judge tier.
//
// D13 (binding here): the judge is an independent model INSTANCE — it is never
// the patch-writer instance. The verifier never creates model clients: the
// `JudgeRole` port (internal/openrouter) is INJECTED at the orchestrator edge,
// and this tier stays pure except for that single injected call — no fs, no
// git writes, no network, no clock of its own (the injected `now` is carried
// for provenance symmetry with the other tiers, never read here).
//
// Identity provenance: `WriterIdentity` / `JudgeIdentity` are session records
// the ORCHESTRATOR owns (the application decides whether the patch is
// permitted; models never hold credentials or session authority). Session ids
// are telemetry-hygiene data — the tier redacts them to their first 8
// characters in every emitted detail.
//
// Type provenance: `DocPatch`, `VerificationCheck`, `VerificationJudge`,
// `VerificationJudgeAbstain` come from internal/schemas (v1/doc-patch.ts,
// v1/verification-report.ts); `JudgeRole` and `RoleInvocation` come from
// internal/openrouter (roles.ts — the stable ARCH §10.2/§10.3 role seam).

import (
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Session-identity role values (TS: SessionIdentity['role']).
const (
	// SessionRolePatchWriter is the patch-writer session that produced the patch.
	SessionRolePatchWriter = "patch-writer"
	// SessionRoleJudge is the judge session identity for the run under verification.
	SessionRoleJudge = "judge"
)

// bindingConstraintPatchWriterInstance is the models.yml D13 binding
// constraint value (TS: Tier3Input['bindingConstraint'] — the empty string
// means the policy declares no constraint). File-local on purpose:
// pipeline_verify_patch.go exports the same wire value under its own name;
// the orchestrator dedupes the two declarations at merge time.
const bindingConstraintPatchWriterInstance = "must_not_equal_patch-writer_instance"

// SessionIdentity is one model-session identity (D13 instance separation
// evidence). `SessionID` is the orchestrator's session-instance id — the value
// whose EQUALITY between writer and judge means the same instance judged its
// own work.
type SessionIdentity struct {
	// Role is the ARCH §10.2 role name: SessionRolePatchWriter or SessionRoleJudge.
	Role string
	// Model is the role-bound model id.
	Model string
	// SessionID is the session-instance id (telemetry-hygiene data).
	SessionID string
}

// Tier3Input holds the inputs ONE Tier-3 run needs (application-owned; see the
// module doc). Pointer string fields mirror the TS `string | null` / optional
// fields (nil == null / absent).
type Tier3Input struct {
	// Patch is the patch under verification (schema-validated upstream).
	Patch *schemas.DocPatch
	// DocContent is the pre-patch document content; nil = the doc is absent.
	DocContent *string
	// PatchedContent is the content after the Tier-1 simulated apply — nil ⇒
	// the second pass cannot run.
	PatchedContent *string
	// TriggeringDiff is the triggering diff text — UNTRUSTED repository
	// content (escaped before prompting); nil = not provided.
	TriggeringDiff *string
	// Judge is the INJECTED independent judge port — the verifier never
	// imports a client.
	Judge openrouter.JudgeRole
	// WriterIdentity is the writer session that produced the patch (D13
	// comparison input).
	WriterIdentity SessionIdentity
	// JudgeIdentity is the judge session identity for THIS run (D13
	// comparison input).
	JudgeIdentity SessionIdentity
	// BindingConstraint is the models.yml D13 binding constraint, when the
	// policy declares one ("" == undeclared).
	BindingConstraint string
	// PolicyVersion is the policy version this verification serves
	// (provenance symmetry).
	PolicyVersion int
	// RunID is the run id this verification belongs to (provenance symmetry).
	RunID string
	// DocPath is the repo-relative path of the document under verification.
	DocPath string
	// Now is the INJECTED clock (provenance symmetry; never a local Date).
	Now func() string
}

// Tier3Context is what later stages / the pipeline consume from the Tier-3 run.
type Tier3Context struct {
	// Judge is the judge outcome block (D3 full model-binding pin) — nil on
	// abstain/skip.
	Judge *schemas.VerificationJudge
	// JudgeAbstain is the first-class judge abstention (invariant 9) —
	// mutually exclusive with Judge (both nil on a plain skip).
	JudgeAbstain *schemas.VerificationJudgeAbstain
}

// Tier3Result is the Tier-3 result: one record per Tier-3 check, BOTH always
// emitted.
type Tier3Result struct {
	Tier    int
	Status  string
	Checks  []schemas.VerificationCheck
	Context Tier3Context
}
