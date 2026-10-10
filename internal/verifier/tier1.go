package verifier

// Ported from packages/verifier/src/tier1/tier1.ts (P4-T02..T07; PLAN §5.4
// items 2–7; ARCH §4.8): the Tier-1 orchestrator composes ALL EIGHT
// deterministic checks in canonical order — patch-applies-cleanly,
// manifest-allowlist-only, ownership-floor, minimality-bound,
// whole-document-rewrite, structural-integrity, link-resolution,
// examples-parse — one record each, ALWAYS all eight: a check that cannot run
// emits 'skipped' WITH detail, never silently omitted (invariant 10 —
// provenance or it didn't happen; ARCH §4.8 "all tiers always run").
//
// Tier status: 'fail' when ANY check failed, else 'pass' ('skipped' never
// fails a tier by itself — the failing simulation behind a skip already
// failed patch-applies-cleanly).
//
// Purity: RunTier1 is pure over its inputs — no fs, no git writes, no network
// (reachability is a caller port invoked through the link check), no clock of
// its own. The ONLY git interaction happens inside patch.ApplyDocPatch
// through the caller-injected ports. Async TS flow: the awaited checks that
// cannot fail run in line (links/examples only await caller ports); the
// engine simulation's runtime error propagates instead of a rejected promise.

import (
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// RunTier1 runs all eight Tier-1 deterministic checks in canonical order.
func RunTier1(input Tier1Input) (Tier1Result, error) {
	/* -- 1. the application simulation (needed by every content check) ------- */
	apply, err := CheckPatchAppliesCleanly(input)
	if err != nil {
		return Tier1Result{}, err
	}

	/* -- 2/3. permission re-checks (pure on input data; always run) ---------- */
	allowlist := CheckManifestAllowlistOnly(input)
	ownership := CheckOwnershipFloor(input)

	/* -- 4/5. minimality bound + whole-document rewrite (separate records) --- */
	minimality, err := CheckMinimalityAndRewrite(input, apply.PatchedContent)
	if err != nil {
		return Tier1Result{}, err
	}

	/* -- 6/7/8. patched-content checks (skipped when nothing was simulated) --- */
	structural := CheckStructuralIntegrity(input, apply.PatchedContent)
	links := CheckLinkResolution(input, apply.PatchedContent)
	examples := CheckExamplesParse(input, apply.PatchedContent)

	checks := []schemas.VerificationCheck{
		apply.Check,
		allowlist,
		ownership,
		minimality.Bound,
		minimality.Rewrite,
		structural,
		links,
		examples,
	}

	context := Tier1Context{ApplyOutcome: &apply.Outcome}
	if apply.PatchedContent != nil {
		context.PatchedContent = apply.PatchedContent
	}
	if minimality.Measurement != nil {
		context.Minimality = minimality.Measurement
	}

	status := schemas.CheckPass
	for _, check := range checks {
		if check.Status == schemas.CheckFail {
			status = schemas.CheckFail
			break
		}
	}

	return Tier1Result{
		Tier:    schemas.Tier1,
		Status:  status,
		Checks:  checks,
		Context: context,
	}, nil
}
