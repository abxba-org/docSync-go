// Ported from packages/verifier/src/pipeline/verify-patch.ts (451 lines).
//
// The verification pipeline (P5 seam, Phase 4 W5b; the composition seam of
// the three tiers — ARCH §4.8): VerifyPatch composes the three tiers in
// deterministic order — Tier 1 deterministic validators, Tier 2
// evidence-backed per-claim checking, Tier 3 independent judge — and
// assembles the schema-validated `docsync.verification-report/v1`. ALL THREE
// TIERS ALWAYS RUN: there is no fast-path skipping (ARCH §4.8/§5.6) — a
// Tier-1 apply failure degrades the later tiers to their fail-closed skip
// records instead of omitting them.
//
// Fail-closed input gate: the patch is re-parsed against the registered
// DOC_PATCH_SCHEMA BEFORE any tier runs — a schema-invalid patch is never
// verified, never scored, never routed (typed PatchNotVerifiableError carrying
// the schema issues). The tiers therefore operate on parsed data only.
//
// Claims-coverage constraint (defense in depth behind Tier-2's structural
// guarantee): after assembly, a claim-bearing patch (DecomposePatchClaims
// non-empty) whose report carries NO claim statuses is a verifiability bug —
// it returns typed PatchNotVerifiableError ('PATCH_NOT_VERIFIABLE'). This
// re-asserts the schema's VERIFICATION_CLAIMS_CONSTRAINT_NOTE at report
// emission; Tier-2's always-run decomposition/binding makes it unreachable
// in normal operation (tier2.ts data-shape posture).
//
// Verified-claim-refs gate (P11-T15, OD-R26): when the patch carries any
// verified_claims (patch level) or per-hunk verified_claim_refs, each ref
// MUST resolve against the claim source available to VerifyPatch — the SAME
// input.Claims store snapshot Tier-2's over-deletion guard consumes (the
// single claims port; no second resolution mechanism). An unresolvable ref,
// or refs present while no claims source is available at all (Claims == nil,
// the TS `undefined`), returns typed PatchNotVerifiableError naming the
// offending ref — never a silent pass, never an auto-strip. Patches WITHOUT
// these fields are untouched by this gate and verify byte-identically to the
// pre-gate pipeline. When the refs resolve, they are surfaced on the Tier-2
// `claim-decomposition` check's existing replayable Inputs surface (the
// minimal additive place — no new check id).
//
// Wave-4-flagged refinement (invariant 9): when Tier 3 returns status
// 'skipped' with an EMPTY context (the Tier-1 apply produced no patched
// content, so the judge never ran), the pipeline SYNTHESIZES the first-class
// judge-abstention record {reason: 'other', detail: ...}; when Tier 3 provides
// its own JudgeAbstain, that record is used verbatim. The synthesized detail
// is FACTUAL, derived from the Tier-1 outcome in context: a run-id idempotent
// skip names itself; every other no-patched-content path reads 'tier 3
// skipped: no patched content (tier-1 apply did not produce text)'.
//
// Assembly is fail-closed end to end: the assembled report is re-validated
// with VerificationReport.ValidateReport before it is returned — an assembly
// bug surfaces as a typed validation error at the seam, never as an invalid
// report in circulation. Verdict and auto gate come from the schema's
// canonical helpers (ResolveVerificationVerdict / ResolveAutoEligibility,
// OD-2), so the wire format and the mapping cannot drift.
//
// patch_ref (content addressing): `state/docs/<flattened>#patch-<hash16>`
// where <flattened> is the D1 doc-state filename flattening of the doc path
// (state.EncodeStateDocPath — the single implementation of the `_` → `_u`,
// `/` → `__` escape) and <hash16> is the first 16 hex chars of the sha256 over
// the CANONICAL JSON of the patch (docutil.CanonicalJSON). Deterministic: the
// same patch yields the same ref across runs and machines.
//
// Purity: the pipeline adds no I/O of its own — every effect flows through
// the tiers' injected ports; the only crypto is the patch-ref digest over
// caller-owned data.
package verifier

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// ------------------------------------------------------------------------
// Tier composition: all three tiers are called directly — RunTier1
// (tier1.go), RunTier2 (tier2.go) and RunTier3 (tier3.go) are the landed
// tier entrypoints (tier1_types.go / tier2_types.go / tier3_types.go carry the
// TS tier interfaces ported verbatim). RunTier1/RunTier2 return errors only
// for engine-configuration failures (fail closed, D8); RunTier3 never does
// (a thrown port error degrades to the `model_unavailable` abstention record
// there). ALL THREE TIERS ALWAYS RUN — there is no fast-path skipping
// (ARCH §4.8/§5.6): a Tier-1 apply failure degrades the later tiers to their
// fail-closed skip records instead of omitting them.
// ------------------------------------------------------------------------

/* ------------------------------------------------------------------ */
/* PatchNotVerifiableError (TS PatchNotVerifiableError)                */
/* ------------------------------------------------------------------ */

// PatchNotVerifiableCode is the machine-readable failure code (TS static/
// instance `code`) — a stable consumer switch.
const PatchNotVerifiableCode = "PATCH_NOT_VERIFIABLE"

// defaultDocPatchParseGate is the default fail-closed gate name (TS
// constructor default 'docsync.doc-patch/v1 parse failed').
const defaultDocPatchParseGate = "docsync.doc-patch/v1 parse failed"

// PatchNotVerifiableError is returned by VerifyPatch when the patch is not
// verifiable — the fail-closed gates BEFORE/AFTER the tiers: (a) the patch
// does not parse against the registered `docsync.doc-patch/v1` schema (an
// invalid patch NEVER reaches the tiers), (b) the verified-claim-refs
// resolution gate fails (P11-T15, OD-R26), or (c) the post-assembly
// claims-coverage check fails. Code() is a stable consumer switch; the
// message carries the gate's evidence verbatim (the parse gate enforces
// invariant 5 — no unparseable patch is ever verified — and the verbatim
// issues are the failure's provenance, invariant 10).
//
// Error-class mapping (CONVENTIONS.md "Errors"): the TS class has no entry in
// internal/docutil, so the underlying coded error maps to the closest code,
// docutil.CodeVerify (EDOCSYNC_VERIFY); the TS class code and message text
// are preserved verbatim on the type itself.
type PatchNotVerifiableError struct {
	// Msg is the exact TS message text: "patch is not verifiable (<gate>):
	// <detail>".
	Msg string
	// Gate names the fail-closed gate that rejected the patch.
	Gate string

	coded *docutil.Error
}

func newPatchNotVerifiableError(detail string) *PatchNotVerifiableError {
	return newPatchNotVerifiableErrorAtGate(defaultDocPatchParseGate, detail)
}

func newPatchNotVerifiableErrorAtGate(gate, detail string) *PatchNotVerifiableError {
	msg := "patch is not verifiable (" + gate + "): " + detail
	return &PatchNotVerifiableError{
		Msg:   msg,
		Gate:  gate,
		coded: docutil.New(docutil.CodeVerify, msg),
	}
}

// Code returns the machine-readable failure code (TS instance `code`).
func (e *PatchNotVerifiableError) Code() string { return PatchNotVerifiableCode }

// Error reproduces the TS message text exactly.
func (e *PatchNotVerifiableError) Error() string { return e.Msg }

// Unwrap exposes the underlying coded error so docutil.CodeOf resolves it.
func (e *PatchNotVerifiableError) Unwrap() error { return e.coded }

/* ------------------------------------------------------------------ */
/* verifyPatch (TS)                                                     */
/* ------------------------------------------------------------------ */

// VerifyPatchInput is the superset input ONE VerifyPatch run needs: the union
// of the three tier input types (application-owned data injected at the
// orchestrator edge). The verifier owns nothing about permissions,
// credentials, or clocks: the allowlist/modes come from the application, the
// judge port is injected (D13), the clock is injected.
//
// Optional members mirror the TS optional fields (nil == absent): DocContent
// nil is the TS `null` (the doc is absent); Claims nil is the TS `undefined`
// (no claim source supplied) — the distinction is load-bearing for the
// verified-claim-refs gate.
type VerifyPatchInput struct {
	/* -- shared identity ------------------------------------------------ */
	Patch         *schemas.DocPatch
	DocContent    *string
	PolicyVersion int
	RunID         string
	DocPath       string
	Now           func() string // INJECTED clock (never a local Date)

	/* -- tier 1 (application simulation + permission re-checks) -------- */
	ManifestAllowlist   map[string]bool // derived from the manifest BY THE APPLICATION
	TargetMode          schemas.DocMode
	TriggeringDiffStats *patch.DiffStats
	MinRatio            *float64
	LinkInputs          *LinkCheckInputs
	ExampleChecker      ExampleCheckerPort
	Git                 patch.CatFilePort
	Rev                 *string
	BaseContent         *string
	IsRunApplied        patch.IsRunAppliedPort

	/* -- tier 2 (evidence context) ------------------------------------- */
	Claims           []schemas.Claim
	WorkspaceSources map[string]string
	SourceHeadShas   map[string]string
	ContractVector   *contracts.ContractVector
	ContractNames    []string
	ContractKinds    map[string]schemas.ContractKind
	Graph            *graph.DocumentGraph

	/* -- tier 3 (independent judge, D13) ------------------------------- */
	TriggeringDiff *string
	Judge          openrouter.JudgeRole
	WriterIdentity SessionIdentity
	JudgeIdentity  SessionIdentity
	// BindingConstraint is the models.yml D13 binding constraint, when the
	// policy declares one (nil == TS undefined; Tier3Input renders it as "").
	BindingConstraint *string
}

// ComputePatchRef is the deterministic patch ref:
// `state/docs/<flattened>#patch-<hash16>` — the D1 doc-state filename
// flattening (state.EncodeStateDocPath) plus the first 16 hex chars of the
// sha256 over the CANONICAL JSON of the patch. Deterministic and
// collision-tight for verification-run scale (TS computePatchRef).
func ComputePatchRef(patch *schemas.DocPatch) (string, error) {
	flattened, err := state.EncodeStateDocPath(patch.DocPath)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return "", docutil.New(docutil.CodeVerify, "patch_ref: patch failed to marshal for canonical hashing: "+err.Error())
	}
	canonical, err := docutil.CanonicalJSON(json.RawMessage(data))
	if err != nil {
		return "", err
	}
	hash16 := docutil.ContentHash(canonical)
	if len(hash16) < 16 {
		return "", docutil.New(docutil.CodeInternal, "patch_ref: sha256 digest shorter than 16 hex chars")
	}
	return "state/docs/" + flattened + "#patch-" + hash16[:16], nil
}

// VerifyPatch runs ALL THREE tiers in deterministic order and assembles the
// schema-validated verification report (module doc for the flow, the
// wave-4-flagged abstain synthesis, and the fail-closed gates). Pure except
// the tiers' own injected ports.
func VerifyPatch(input VerifyPatchInput) (*schemas.VerificationReport, error) {
	// Go nil-interface guard (fail closed): a missing judge port can never
	// run the Tier-3 second pass. (The TS port is typed; in Go a nil
	// interface would otherwise panic inside RunTier3.)
	if input.Judge == nil {
		return nil, docutil.New(docutil.CodeVerify,
			"verifyPatch requires an injected judge port (D13) — none was supplied")
	}

	/* -- 1. fail-closed input gate: an invalid patch NEVER reaches the tiers -- */
	parsed, err := parseDocPatchGate(input.Patch)
	if err != nil {
		return nil, err
	}

	/* -- 1b. verified-claim-refs gate (P11-T15, OD-R26): fail-closed
	   resolution against the SAME claims port Tier-2's over-deletion guard
	   consumes — a ref-present patch with an unresolvable ref (or with no
	   claims source at all) never reaches the tiers; a legacy patch (no
	   refs) is untouched. */
	resolvedVerifiedClaimRefs, err := resolveVerifiedClaimRefs(
		declaredVerifiedClaimRefs(parsed),
		input.Claims,
	)
	if err != nil {
		return nil, err
	}

	/* -- 2. tier 1 (deterministic validators; all eight always emitted) ------- */
	tier1, err := RunTier1(Tier1Input{
		Patch:               parsed,
		DocContent:          input.DocContent,
		ManifestAllowlist:   input.ManifestAllowlist,
		TargetMode:          input.TargetMode,
		TriggeringDiffStats: input.TriggeringDiffStats,
		MinRatio:            input.MinRatio,
		LinkInputs:          input.LinkInputs,
		ExampleChecker:      input.ExampleChecker,
		Git:                 input.Git,
		Rev:                 input.Rev,
		BaseContent:         input.BaseContent,
		IsRunApplied:        input.IsRunApplied,
		Now:                 input.Now,
	})
	if err != nil {
		return nil, err
	}
	patchedContent := tier1.Context.PatchedContent

	/* -- 3. tier 2 (evidence-backed per-claim checking) ----------------------- */
	tier2, err := RunTier2(Tier2Input{
		Patch:            parsed,
		DocContent:       input.DocContent,
		PatchedContent:   patchedContent,
		Claims:           input.Claims,
		WorkspaceSources: input.WorkspaceSources,
		SourceHeadShas:   input.SourceHeadShas,
		ContractVector:   input.ContractVector,
		ContractNames:    input.ContractNames,
		ContractKinds:    input.ContractKinds,
		Graph:            input.Graph,
		Now:              input.Now,
	})
	if err != nil {
		return nil, err
	}

	/* -- 4. tier 3 (independent judge, D13) ----------------------------------- */
	tier3 := RunTier3(Tier3Input{
		Patch:             parsed,
		DocContent:        input.DocContent,
		PatchedContent:    patchedContent,
		TriggeringDiff:    input.TriggeringDiff,
		Judge:             input.Judge,
		WriterIdentity:    input.WriterIdentity,
		JudgeIdentity:     input.JudgeIdentity,
		BindingConstraint: bindingConstraintText(input.BindingConstraint),
		PolicyVersion:     input.PolicyVersion,
		RunID:             input.RunID,
		DocPath:           input.DocPath,
		Now:               input.Now,
	})

	/* -- wave-4-flagged refinement: a skipped judge tier requires an abstain -- */
	judge := tier3.Context.Judge
	judgeAbstain := tier3.Context.JudgeAbstain
	if judgeAbstain == nil && judge == nil && tier3.Status == schemas.CheckSkipped {
		synthesized := synthesizedTier3SkipAbstain(tier1.Context.ApplyOutcome)
		judgeAbstain = &synthesized
	}

	/* -- 5. assemble the report (tier entries are EXACTLY the schema shape) --- */
	tiers := []schemas.VerificationTierReport{
		{Tier: schemas.Tier1, Status: tier1.Status, Checks: copyChecks(tier1.Checks)},
		{
			Tier:   schemas.Tier2,
			Status: tier2.Status,
			Checks: attachVerifiedClaimRefs(tier2.Checks, resolvedVerifiedClaimRefs),
		},
		{Tier: schemas.Tier3, Status: tier3.Status, Checks: copyChecks(tier3.Checks)},
	}
	claims := copyClaims(tier2.Context.Claims)
	eligibility := schemas.ResolveAutoEligibility(claims)
	patchRef, err := ComputePatchRef(parsed)
	if err != nil {
		return nil, err
	}
	report := &schemas.VerificationReport{
		Schema:           verificationReportSchemaID,
		RunID:            input.RunID,
		DocPath:          input.DocPath,
		PatchRef:         patchRef,
		Tiers:            tiers,
		Claims:           claims,
		Verdict:          schemas.ResolveVerificationVerdict(tiers, judge, judgeAbstain, false),
		AutoEligible:     eligibility.AutoEligible,
		AutoBlockReasons: eligibility.AutoBlockReasons,
		PolicyVersion:    input.PolicyVersion,
		Judge:            judge,
		JudgeAbstain:     judgeAbstain,
	}

	/* -- 6. claims-coverage defense in depth (VERIFICATION_CLAIMS_CONSTRAINT_NOTE):
	   Tier-2's always-run decomposition/binding makes this unreachable in
	   normal operation; it is re-asserted here so an assembly bug can never
	   ship a claim-bearing patch with no claim statuses (fail closed, typed). */
	if len(report.Claims) == 0 {
		if len(DecomposePatchClaims(parsed)) > 0 {
			return nil, newPatchNotVerifiableErrorAtGate(
				"post-assembly claims-coverage check",
				"claims-coverage constraint violated: claim-bearing patch produced no claim statuses",
			)
		}
	}

	/* -- 7. fail-closed self-check: an assembly bug is a typed error ---------- */
	if issues := report.ValidateReport(); len(issues) > 0 {
		return nil, schemas.NewValidationError(issues...)
	}
	return report, nil
}

// verificationReportSchemaID is the wire schema literal (TS
// 'docsync.verification-report/v1').
const verificationReportSchemaID = "docsync.verification-report/v1"

// claimDecompositionCheckID is the check id the resolved verified-claim refs
// surface on (P11-T15, OD-R26 — NO new check id).
const claimDecompositionCheckID = "claim-decomposition"

// bindingConstraintText renders the optional D13 binding constraint for
// Tier3Input (nil == TS undefined → "" == undeclared).
func bindingConstraintText(constraint *string) string {
	if constraint == nil {
		return ""
	}
	return *constraint
}

/* ------------------------------------------------------------ input gate */

// parseDocPatchGate is the fail-closed re-parse of the patch against the
// registered DOC_PATCH_SCHEMA BEFORE any tier runs (TS
// DOC_PATCH_SCHEMA.safeParse): the patch is re-decoded strictly (unknown keys
// rejected) and validated; a failure is a typed PatchNotVerifiableError
// carrying the issues formatted verbatim (TS formatIssues).
func parseDocPatchGate(patch *schemas.DocPatch) (*schemas.DocPatch, error) {
	data, err := json.Marshal(patch)
	if err != nil {
		return nil, newPatchNotVerifiableError("patch failed to re-encode for the doc-patch parse gate: " + err.Error())
	}
	var parsed schemas.DocPatch
	if err := schemas.StrictDecode(data, &parsed); err != nil {
		var validation *schemas.ValidationError
		if errors.As(err, &validation) {
			return nil, newPatchNotVerifiableError(formatIssues(validation.Issues))
		}
		return nil, newPatchNotVerifiableError(err.Error())
	}
	if issues := parsed.Validate(); len(issues) > 0 {
		return nil, newPatchNotVerifiableError(formatIssues(issues))
	}
	return &parsed, nil
}

// formatIssues formats schema issues into the fail-closed gate error message
// (TS formatIssues): "path.to.field: message" joined by "; ", with "(root)"
// for the empty path.
func formatIssues(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		path := strings.Join(issue.Path, ".")
		if path == "" {
			path = "(root)"
		}
		parts = append(parts, path+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

/* --------------------------------------------- verified-claim-refs gate */

// verifiedClaimRefsGate is the fail-closed gate name for the
// verified-claim-refs resolution gate (TS VERIFIED_CLAIM_REFS_GATE).
const verifiedClaimRefsGate = "verified-claim-refs resolution gate (P11-T15, OD-R26)"

// declaredVerifiedClaimRefs is the patch's declared verified-claim refs
// (P11-T15, OD-R26): the patch-level verified_claims set plus every per-hunk
// verified_claim_refs entry, in first-seen order, deduplicated. Empty for a
// legacy patch (both fields OPTIONAL and absent) — the gate is then a no-op
// and the pipeline behaves identically to before.
func declaredVerifiedClaimRefs(patch *schemas.DocPatch) []string {
	var refs []string
	seen := make(map[string]bool)
	for _, ref := range patch.VerifiedClaims {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	for _, hunk := range patch.Hunks {
		for _, ref := range hunk.VerifiedClaimRefs {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs
}

// resolveVerifiedClaimRefs is the fail-closed resolution of the declared refs
// against the claims source available to VerifyPatch — the SAME Claims store
// snapshot Tier-2's over-deletion guard consumes (the single claims port).
// Every ref must resolve to a store claim id; an unresolvable ref, or any ref
// at all while no claims source was supplied (claims == nil, TS `undefined`),
// returns a typed PatchNotVerifiableError naming the offending ref — fail
// closed, never a silent pass, never an auto-strip.
func resolveVerifiedClaimRefs(refs []string, claims []schemas.Claim) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if claims == nil {
		return nil, newPatchNotVerifiableErrorAtGate(verifiedClaimRefsGate,
			"patch declares verified claim refs ("+strings.Join(refs, ", ")+") but no claim source is available "+
				"to resolve them against — failing closed (never silently passing, never auto-stripping)")
	}
	claimIDs := make(map[string]bool, len(claims))
	for _, claim := range claims {
		claimIDs[claim.ID] = true
	}
	for _, ref := range refs {
		if !claimIDs[ref] {
			return nil, newPatchNotVerifiableErrorAtGate(verifiedClaimRefsGate,
				"verified claim ref '"+ref+"' does not resolve against the available claim source "+
					"("+strconv.Itoa(len(claims))+" claim(s)) — failing closed (never silently passing, never auto-stripping)")
		}
	}
	out := make([]string, len(refs))
	copy(out, refs)
	return out, nil
}

// attachVerifiedClaimRefs surfaces the resolved verified-claim refs on the
// report's minimal additive place (P11-T15, OD-R26): the Tier-2
// `claim-decomposition` check's existing replayable Inputs surface — NO new
// check id, NO new field. A legacy patch (no refs) yields identical check
// records.
func attachVerifiedClaimRefs(checks []schemas.VerificationCheck, resolvedRefs []string) []schemas.VerificationCheck {
	if len(resolvedRefs) == 0 {
		return copyChecks(checks)
	}
	out := make([]schemas.VerificationCheck, 0, len(checks))
	for _, check := range checks {
		if check.ID == claimDecompositionCheckID {
			inputs := make([]string, len(resolvedRefs))
			copy(inputs, resolvedRefs)
			check.Inputs = inputs
		}
		out = append(out, check)
	}
	return out
}

/* ------------------------------------------------- abstain + copy helpers */

// synthesizedTier3SkipAbstain is the canonical judge-abstention record
// synthesized for a Tier-3 skip with empty context — the detail is DERIVED
// from the Tier-1 outcome present in context (factual, never "apply failed"
// when tier 1 passed): a run-id idempotent skip names itself; every other
// no-patched-content path names the missing text (TS
// synthesizedTier3SkipAbstain).
func synthesizedTier3SkipAbstain(applyOutcome *patch.ApplyOutcome) schemas.VerificationJudgeAbstain {
	runIDSkip := applyOutcome != nil &&
		applyOutcome.Kind == patch.OutcomeNoop &&
		applyOutcome.NoopReason == patch.NoopRunIDSkip
	detail := "tier 3 skipped: no patched content (tier-1 apply did not produce text)"
	if runIDSkip {
		detail = "tier 3 skipped: patched content unavailable (run-id idempotent skip; claims carried unchecked)"
	}
	return schemas.VerificationJudgeAbstain{Reason: "other", Detail: detail}
}

// copyChecks is the TS `[...checks]` spread: a defensive copy so the tiers'
// records cannot be mutated through the assembled report.
func copyChecks(checks []schemas.VerificationCheck) []schemas.VerificationCheck {
	out := make([]schemas.VerificationCheck, len(checks))
	copy(out, checks)
	return out
}

// copyClaims is the TS `[...tier2.context.claims]` spread (nil renders as an
// empty array — the schema's claims field is a required array).
func copyClaims(claims []schemas.VerificationClaim) []schemas.VerificationClaim {
	out := make([]schemas.VerificationClaim, len(claims))
	copy(out, claims)
	return out
}
