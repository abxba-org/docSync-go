// Ported from packages/verifier/src/orchestrator/verification-stage.ts (216
// lines).
//
// Verification-stage port (P5-T07; PLAN §6.4 `verification` stage; ARCH §3.5,
// §13) — the adapter that wires the three-tier pipeline (VerifyPatch) into
// the core `VerificationStagePort` shape. The orchestrator owns control flow
// and the persistence boundary; this adapter owns ONLY the input derivation
// the port cannot carry (the port receives `{ ctx, patch, docContent }` — the
// VerifyPatchInput members are derived application-side, here) plus the
// composition-time D13 session-collision assert.
//
// Input derivation (all application-owned, never invented):
//   - RunID ← ctx.RunID; PolicyVersion ← ctx.Key.PolicyVersion (the RunKey is
//     the run-record identity, ARCH §6.3); DocPath ← patch.DocPath;
//     Now ← ctx.Clock (injected — never a local Date).
//   - ManifestAllowlist ← the manifest document paths (D7: the manifest IS
//     the allowlist; tier-1 re-checks it — invariant 3).
//   - TargetMode ← the doc's effective mode per §11.3 precedence (entry mode
//     → defaults mode → built-in `suggest`); a doc path ABSENT from the
//     manifest resolves `external` (D7 allowlist semantics — unlisted is never
//     writable; the tier-1 ownership-floor check then fails closed).
//   - Graph / ContractNames / ContractKinds ← ctx.Graph (the doc's mapping
//     record carries its declared contracts; P1-T09).
//   - everything else (claims, workspace sources, link inputs, judge, …) is
//     injected verbatim through VerificationStageDeps — the adapter adds no
//     analysis data of its own.
//
// D13 (binding): the judge separation is enforced INSIDE VerifyPatch (tier-3
// independence check first). This adapter passes the writer/judge session
// identities through verbatim and adds ONE composition-time assert: when both
// identities are visible and share a SessionID, the same instance would judge
// its own work — JudgeWriterSessionCollisionError is returned when the port
// is CREATED (fail loud, before any VerifyPatch call).
//
// Error posture: VerifyPatch's typed failures (PatchNotVerifiableError and
// everything the tiers raise) propagate UNCHANGED — they are never swallowed
// into a fabricated report. The runner (P5-T02) already degrades a
// verification-port failure to review (`verifier_uncertain`); inventing a
// report here would fake evidence. Store persistence: when
// VerificationStageDeps.Store is present the report is saved AFTER a
// successful verify (append-only; dedup semantics are the store's own
// report_hash contract) and a store failure also propagates (fail loud; the
// R15 loser-re-runs posture stays the store's).
//
// Purity: no fs, no git, no network, no clock of its own — the only effects
// are the injected tiers' ports and the optional injected store.
//
// Layering note: the TS imports RunContext / VerificationStagePort from
// @docsync/core. In Go the CRITICAL direction is inverted — internal/verifier
// is L4 and the orchestrator is L5 (plan.md §3), so the verifier CANNOT import
// the orchestrator package. This file therefore declares the port and the
// run-context VIEW below; the wave-end composition layer binds the concrete
// port to the orchestrator's identically-shaped seam (internal/orchestrator
// types.go, VerificationStagePort/VerificationStageInput) with a field-copy
// adapter.
package verifier

import (
	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------------ */
/* JudgeWriterSessionCollisionError (TS JudgeWriterSessionCollisionError) */
/* ------------------------------------------------------------------ */

// JudgeWriterSessionCollisionCode is the machine-readable failure code (TS
// static/instance `code`).
const JudgeWriterSessionCollisionCode = "JUDGE_WRITER_SESSION_COLLISION"

// JudgeWriterSessionCollisionError is returned when the port is composed
// with a judge session whose id equals the writer session's — the same model
// INSTANCE would judge its own patch (D13 violation). Code() is a stable
// consumer switch; the composition NEVER reaches VerifyPatch (fail loud at
// the seam, before any tier runs).
//
// Error-class mapping (CONVENTIONS.md "Errors"): the TS class has no entry in
// internal/docutil, so the underlying coded error maps to the closest code,
// docutil.CodeVerify; the TS class code and message text are preserved
// verbatim on the type itself.
type JudgeWriterSessionCollisionError struct {
	// Msg is the exact TS message text.
	Msg string

	coded *docutil.Error
}

func newJudgeWriterSessionCollisionError(sessionID string) *JudgeWriterSessionCollisionError {
	msg := "judge session '" + sessionID + "' equals the writer session id — " +
		"the same model instance must never judge its own patch (D13); wire distinct " +
		"session identities for the writer and judge roles"
	return &JudgeWriterSessionCollisionError{Msg: msg, coded: docutil.New(docutil.CodeVerify, msg)}
}

// Code returns the machine-readable failure code (TS instance `code`).
func (e *JudgeWriterSessionCollisionError) Code() string { return JudgeWriterSessionCollisionCode }

// Error reproduces the TS message text exactly.
func (e *JudgeWriterSessionCollisionError) Error() string { return e.Msg }

// Unwrap exposes the underlying coded error so docutil.CodeOf resolves it.
func (e *JudgeWriterSessionCollisionError) Unwrap() error { return e.coded }

/* ------------------------------------------------------------------ */
/* The stage port shapes                                              */
/* ------------------------------------------------------------------ */

// VerificationRecordSaver is the persistence seam the stage consumes (TS
// VerificationStageStore): append-only, deduped by the store's own
// report_hash contract. *VerificationRecordStore (storage_records.go)
// satisfies it structurally; tests inject a fake.
type VerificationRecordSaver interface {
	Save(report schemas.VerificationReport, now func() string) (VerificationSaveOutcome, error)
}

// VerificationStageDeps is the adapter's injected dependencies: the required
// D13 identities and judge port, plus every optional VerifyPatchInput member
// the composition can supply. Per-run identity members (runId, policyVersion,
// now, allowlist, graph) are NOT here — the adapter derives them from the
// port's RunContext (the runner owns them). Per-document budget/snapshot data
// (triggeringDiffStats, baseContent) is injected as provider functions of the
// doc path; shared context is injected verbatim.
type VerificationStageDeps struct {
	/* -- tier 3: the injected judge seam (D13; the verifier never imports a client) -- */
	Judge          openrouter.JudgeRole
	WriterIdentity SessionIdentity
	JudgeIdentity  SessionIdentity
	// BindingConstraint is the models.yml D13 binding constraint, when the
	// policy declares one (nil == TS undefined).
	BindingConstraint *string

	/* -- persistence (optional; the runner may also own it) ------------------- */
	Store VerificationRecordSaver

	/* -- tier 1 (shared context, injected verbatim) --------------------------- */
	LinkInputs     *LinkCheckInputs
	ExampleChecker ExampleCheckerPort
	MinRatio       *float64
	TriggeringDiff *string
	Git            patch.CatFilePort
	Rev            *string
	IsRunApplied   patch.IsRunAppliedPort
	// TriggeringDiffStats is the per-doc triggering code-diff size for the
	// minimality ratio (P3-T12); nil = none supplied.
	TriggeringDiffStats func(docPath string) *patch.DiffStats
	// BaseContent is the per-doc writer base snapshot (enables the race 3-way
	// merge path); nil = none supplied.
	BaseContent func(docPath string) *string

	/* -- tier 2 (evidence context, injected verbatim) ------------------------- */
	Claims           []schemas.Claim
	WorkspaceSources map[string]string
	SourceHeadShas   map[string]string
	ContractVector   *contracts.ContractVector
}

// RunContext is the verifier-side view of core's RunContext — exactly the
// members the verification stage derives from (the seam the composition
// layer field-copies the orchestrator's full RunContext into).
//
// TODO(orchestrator): bind this view to internal/orchestrator's RunContext
// (same member names; that type adds caps/budgets/PR the verifier never
// reads). The verifier never imports the orchestrator package (L4 < L5).
type RunContext struct {
	// Repo is the repository identity (e.g. `org/name`).
	Repo string
	// RepoRoot is the absolute repository root.
	RepoRoot string
	// Base is the base ref/sha of the change window.
	Base string
	// Head is the head ref/sha of the change window.
	Head string
	// RunID is the human-readable run id (ARCH §4.1).
	RunID string
	// Key is the run-record identity (state-store key, ARCH §6.3).
	Key schemas.RunKey
	// Manifest is the manifest allowlist for the run (D7).
	Manifest schemas.Manifest
	// Graph is the composed document graph (P1-T09).
	Graph graph.DocumentGraph
	// Clock is the INJECTED clock: returns the current ISO-8601 instant.
	Clock func() string
}

// VerificationStageInput is the verification-stage port input (mirrors the
// orchestrator seam; Patch is a pointer — the pipeline re-parses it).
type VerificationStageInput struct {
	Ctx        RunContext
	Patch      *schemas.DocPatch
	DocContent *string
}

// VerificationStageResult carries the verified report (Report is nil only
// alongside an error).
type VerificationStageResult struct {
	Report *schemas.VerificationReport
}

// VerificationStagePort is the verification-side stage port (ARCH §3.5).
type VerificationStagePort interface {
	Verify(input VerificationStageInput) (VerificationStageResult, error)
}

// docModeExternal is the D7 allowlist semantics for a doc path ABSENT from
// the manifest: unlisted is never writable (TS WorkspaceMode 'external'). It
// renders through schemas.DocMode because the manifest mode union has no
// 'external' member — the tier-1 ownership floor treats it as read-only.
const docModeExternal schemas.DocMode = "external"

// docModeOf is the effective §11.3 mode precedence: entry mode → defaults
// mode → `suggest` (TS docModeOf). A doc path absent from the manifest
// resolves `external` (D7).
func docModeOf(manifest *schemas.Manifest, docPath string) schemas.DocMode {
	for _, entry := range manifest.Documents {
		if entry.Path == docPath {
			if entry.Mode != nil {
				return *entry.Mode
			}
			if manifest.Defaults != nil && manifest.Defaults.Mode != nil {
				return *manifest.Defaults.Mode
			}
			return schemas.DocModeSuggest
		}
	}
	return docModeExternal
}

// verificationStagePort is the concrete VerificationStagePort.
type verificationStagePort struct {
	deps VerificationStageDeps
}

// CreateVerificationStagePort creates the verification-stage port (P5-T07).
// The D13 session-collision assert runs HERE — at composition time, before
// any verify call — so a mis-wired port cannot exist, let alone verify its
// own writer's patch.
func CreateVerificationStagePort(deps VerificationStageDeps) (VerificationStagePort, error) {
	// D13 composition-time assert: the judge instance is never the writer
	// instance. Session ids are the instance identity (SessionIdentity doc);
	// equality means the same session would judge its own work — fail loud.
	if deps.JudgeIdentity.SessionID == deps.WriterIdentity.SessionID {
		return nil, newJudgeWriterSessionCollisionError(deps.JudgeIdentity.SessionID)
	}
	return &verificationStagePort{deps: deps}, nil
}

// Verify derives the application-owned inputs from the run context, runs the
// three-tier pipeline, and — when a store is wired — appends the report.
// Typed errors PROPAGATE (fail loud): the runner degrades verification
// failures to review (verifier_uncertain) — this adapter never swallows them
// into a fabricated report.
func (p *verificationStagePort) Verify(input VerificationStageInput) (VerificationStageResult, error) {
	ctx := input.Ctx
	docPatch := input.Patch
	docPath := docPatch.DocPath

	var triggeringDiffStats *patch.DiffStats
	if p.deps.TriggeringDiffStats != nil {
		triggeringDiffStats = p.deps.TriggeringDiffStats(docPath)
	}
	var baseContent *string
	if p.deps.BaseContent != nil {
		baseContent = p.deps.BaseContent(docPath)
	}
	manifestAllowlist := make(map[string]bool, len(ctx.Manifest.Documents))
	for _, entry := range ctx.Manifest.Documents {
		manifestAllowlist[entry.Path] = true
	}
	// The doc's mapping record carries its declared contracts (P1-T09); a
	// doc absent from the graph declares none.
	var contractNames []string
	for _, record := range ctx.Graph.Mappings {
		if record.Doc == docPath {
			contractNames = append(make([]string, 0, len(record.Contracts)), record.Contracts...)
			break
		}
	}
	graphSnapshot := ctx.Graph

	report, err := VerifyPatch(VerifyPatchInput{
		Patch:               docPatch,
		DocContent:          input.DocContent,
		PolicyVersion:       ctx.Key.PolicyVersion,
		RunID:               ctx.RunID,
		DocPath:             docPath,
		Now:                 ctx.Clock,
		ManifestAllowlist:   manifestAllowlist,
		TargetMode:          docModeOf(&ctx.Manifest, docPath),
		TriggeringDiffStats: triggeringDiffStats,
		MinRatio:            p.deps.MinRatio,
		LinkInputs:          p.deps.LinkInputs,
		ExampleChecker:      p.deps.ExampleChecker,
		Git:                 p.deps.Git,
		Rev:                 p.deps.Rev,
		BaseContent:         baseContent,
		IsRunApplied:        p.deps.IsRunApplied,
		Claims:              p.deps.Claims,
		WorkspaceSources:    p.deps.WorkspaceSources,
		SourceHeadShas:      p.deps.SourceHeadShas,
		ContractVector:      p.deps.ContractVector,
		ContractNames:       contractNames,
		ContractKinds:       ctx.Graph.ContractKinds,
		Graph:               &graphSnapshot,
		TriggeringDiff:      p.deps.TriggeringDiff,
		Judge:               p.deps.Judge,
		WriterIdentity:      p.deps.WriterIdentity,
		JudgeIdentity:       p.deps.JudgeIdentity,
		BindingConstraint:   p.deps.BindingConstraint,
	})
	if err != nil {
		return VerificationStageResult{}, err
	}

	// Append-only persistence when a store is wired; the store's dedup
	// (report_hash) and CAS semantics are its own (P4-T20). A store failure
	// propagates — never a silently unpersisted report.
	if p.deps.Store != nil {
		if _, err := p.deps.Store.Save(*report, ctx.Clock); err != nil {
			return VerificationStageResult{}, err
		}
	}
	return VerificationStageResult{Report: report}, nil
}
