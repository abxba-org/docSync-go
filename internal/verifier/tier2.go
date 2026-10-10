package verifier

// The Tier-2 orchestrator (port of tier2/tier2.ts; P4-T08..T14; PLAN §5.4;
// ARCH §4.8 Tier-2): composes ALL SEVEN evidence-backed checks in canonical
// order — claim-decomposition, claim-anchor-binding, head-verification,
// over-deletion-guard, contract-aware-check, coverage-defect,
// behavioral-predicate-unverified — one record each, ALWAYS all seven: a check
// that cannot run emits 'skipped' WITH detail, never silently omitted
// (invariant 10 — provenance or it didn't happen; ARCH §4.8 "all tiers always
// run").
//
// Composition order vs record order: contract corroboration is COMPUTED FIRST
// (the contract-aware check's corroboration map is head-verify's
// `ContractCorroborated` port input), but the emitted `checks` array stays in
// the canonical order above; the over-deletion/contract/coverage/behavioral
// records follow. Unbound claims are routed `unanchored` on the tracker BEFORE
// head-verification (unanchored ⇒ suggest, never auto — PLAN §5.6 / ARCH §4.8;
// a guessed anchor is never fabricated).
//
// Data-shape posture: decomposition and binding are PATCH/WORKSPACE-ONLY
// computations — they run ALWAYS, including when the Tier-1 apply produced no
// patched content (decomposition never needed it). Their records read 'pass'
// with the usual detail when they ran; only a patch with no inserted lines
// legitimately skips them — they are never 'skipped' merely because patched
// content is absent. The consequence: a claim-bearing patch can NEVER lose its
// claim tracker, so Context.Claims is non-empty whenever
// DecomposePatchClaims(patch) is (the schema's
// VERIFICATION_CLAIMS_CONSTRAINT_NOTE holds by construction).
//
// Fail-closed posture on `patchedContent === nil` (the Tier-1 apply produced
// no text): head-verification skips WITH detail — nothing can be verified
// against absent text — and every BOUND claim is pinned 'unchecked' with the
// declared-gap detail ("patched content unavailable — decomposed and bound but
// not verified (fail-closed declared gap)"); unbound claims stay
// 'unanchored'. The patch/doc-shaped checks (over-deletion, contract,
// coverage, behavioral) keep their nil-content behavior.
//
// Tier status: 'fail' when ANY check failed, else 'pass' — 'skipped' never
// fails the tier by itself (skips clear auto-eligibility downstream or surface
// defects, they do not reject the patch).
//
// Purity: RunTier2 is pure over its inputs — no fs, no git writes, no network,
// no clock of its own (the injected `now` is carried for provenance symmetry
// with the other tiers, never read here). The TS signature is async only for
// symmetry with runTier1; the body is synchronous, so the Go port is a plain
// function. Its error return carries the tier's fail-closed throws (the
// tracker's validation and a nil patch, which the schema makes unreachable) —
// never a semantic outcome.

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// patchedContentUnavailableDetail is the fail-closed skip detail when the
// Tier-1 apply produced no patched text.
const patchedContentUnavailableDetail = "patched content unavailable — tier-1 apply did not produce text (fail-closed)"

// unverifiedBoundClaimDetail is the per-claim declared-gap detail pinned on
// BOUND claims in that skip.
const unverifiedBoundClaimDetail = "patched content unavailable — decomposed and bound but not verified (fail-closed declared gap)"

// noInsertedLinesDetail is the canonical skip detail for a patch with no
// inserted lines.
const noInsertedLinesDetail = "patch proposes no inserted lines"

/* ------------------------------------------------------------------ helpers */

// tier2InsertLineCount is the insert lines of one hunk (the core document-line
// convention: a single trailing newline is absorbed); an empty `insert_text`
// contributes 0 lines.
func tier2InsertLineCount(insertText string) int {
	if insertText == "" {
		return 0
	}
	return len(tier2SplitInsertLines(insertText))
}

// tier2DecompositionStats is the decomposition aggregate for the
// claim-decomposition record.
func tier2DecompositionStats(decomposed []DecomposedClaim, linesScanned int) DecompositionStats {
	byKind := map[DecomposedClaimKind]int{
		DecomposedClaimKindSignature:  0,
		DecomposedClaimKindValue:      0,
		DecomposedClaimKindErrorCode:  0,
		DecomposedClaimKindBehavioral: 0,
		DecomposedClaimKindOther:      0,
	}
	for _, claim := range decomposed {
		byKind[claim.Kind]++
	}
	return DecompositionStats{
		LinesScanned: linesScanned,
		Claims:       len(decomposed),
		ByKind:       byKind,
	}
}

// tier2DecompositionDetail is the stats detail of the claim-decomposition
// record (replayable evidence).
func tier2DecompositionDetail(stats DecompositionStats) string {
	return fmt.Sprintf(
		"decomposed %d claim(s) from %d inserted line(s): signature %d, value %d, error_code %d, "+
			"behavioral %d, other %d",
		stats.Claims, stats.LinesScanned,
		stats.ByKind[DecomposedClaimKindSignature],
		stats.ByKind[DecomposedClaimKindValue],
		stats.ByKind[DecomposedClaimKindErrorCode],
		stats.ByKind[DecomposedClaimKindBehavioral],
		stats.ByKind[DecomposedClaimKindOther])
}

// tier2BindingDetail is the `n bound (m claim-store, k workspace-token), j
// unanchored` detail.
func tier2BindingDetail(bindings []ClaimBinding) string {
	bound := 0
	claimStore := 0
	workspaceToken := 0
	for _, binding := range bindings {
		if !binding.Bound {
			continue
		}
		bound++
		switch binding.Via {
		case BindingViaClaimStore:
			claimStore++
		case BindingViaWorkspaceToken:
			workspaceToken++
		}
	}
	unanchored := len(bindings) - bound
	summary := fmt.Sprintf("%d bound (%d claim-store, %d workspace-token), %d unanchored",
		bound, claimStore, workspaceToken, unanchored)
	if workspaceToken == 0 {
		return summary
	}
	return summary + " — anchor surfaces bound at token level; OpenAPI/config-resolved-value/" +
		"test-name semantics are a declared V1 gap (A4 posture)"
}

/* ---------------------------------------------------------------- exports */

// RunTier2 runs ALL SEVEN Tier-2 checks in canonical order and returns the
// tier report with the per-claim context (statuses, decomposition, bindings).
// See the module doc for the composition order, the data-shape posture
// (decomposition and binding run ALWAYS), the fail-closed head-verification
// skip that pins bound claims 'unchecked', and the skip-never-fails rule.
// Pure over its inputs (module doc).
func RunTier2(input Tier2Input) (Tier2Result, error) {
	if input.Patch == nil {
		return Tier2Result{}, docutil.New(docutil.CodeValidation,
			"tier2: patch is required (a schema-valid DocPatch; fail-closed)")
	}

	hasPatchedContent := input.PatchedContent != nil
	decomposed := DecomposePatchClaims(input.Patch)
	tracker, err := CreateClaimStatusTracker(decomposed)
	if err != nil {
		return Tier2Result{}, err
	}
	bindings := BindClaims(decomposed, input)

	linesScanned := 0
	for hunkIndex := range input.Patch.Hunks {
		linesScanned += tier2InsertLineCount(input.Patch.Hunks[hunkIndex].InsertText)
	}
	noInsertions := linesScanned == 0

	/* -- 1. claim decomposition (patch-only data — runs ALWAYS) -------------- */
	var decomposition schemas.VerificationCheck
	if noInsertions {
		decomposition = schemas.VerificationCheck{
			ID:     "claim-decomposition",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr(noInsertedLinesDetail),
		}
	} else {
		decomposition = schemas.VerificationCheck{
			ID:     "claim-decomposition",
			Status: schemas.CheckPass,
			Detail: tier2StringPtr(tier2DecompositionDetail(
				tier2DecompositionStats(decomposed, linesScanned))),
		}
	}

	/* -- 2. claim anchor binding (patch/workspace-only — runs ALWAYS; --------- */
	/*      unbound ⇒ tracker 'unanchored' first) ------------------------------ */
	for _, binding := range bindings {
		if !binding.Bound {
			if err := tracker.Set(binding.Index, schemas.ClaimVerdictUnanchored, binding.Detail); err != nil {
				return Tier2Result{}, err
			}
		}
	}
	var anchorBinding schemas.VerificationCheck
	if noInsertions {
		anchorBinding = schemas.VerificationCheck{
			ID:     "claim-anchor-binding",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr(noInsertedLinesDetail),
		}
	} else {
		anchorBinding = schemas.VerificationCheck{
			ID:     "claim-anchor-binding",
			Status: schemas.CheckPass,
			Detail: tier2StringPtr(tier2BindingDetail(bindings)),
		}
	}

	/* -- 5 FIRST (computation): contract corroboration for head-verify ------- */
	contractAware := CheckContractAware(input, tracker)

	/* -- 3. head verification (bound claims only; unbound routed above) ------ */
	var headVerification schemas.VerificationCheck
	if hasPatchedContent {
		headVerification, err = CheckHeadVerification(input, decomposed, bindings, tracker, &HeadVerifyDeps{
			ContractCorroborated: func(claim DecomposedClaim) bool {
				return contractAware.Corroborated[claim.Index]
			},
		})
		if err != nil {
			return Tier2Result{}, err
		}
	} else {
		// Fail-closed skip: nothing can be verified against absent text. Every
		// BOUND claim is pinned 'unchecked' with the declared-gap detail —
		// decomposed and bound but verified against nothing (never a fabricated
		// support); unbound claims stay 'unanchored' (routed above).
		for _, binding := range bindings {
			if !binding.Bound {
				continue
			}
			if err := tracker.Set(binding.Index, schemas.ClaimVerdictUnchecked, unverifiedBoundClaimDetail); err != nil {
				return Tier2Result{}, err
			}
		}
		headVerification = schemas.VerificationCheck{
			ID:     "head-verification",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr(patchedContentUnavailableDetail),
		}
	}

	/* -- 4. over-deletion guard ---------------------------------------------- */
	overDeletion, err := CheckOverDeletionGuard(input, tracker)
	if err != nil {
		return Tier2Result{}, err
	}

	/* -- 6. coverage defect --------------------------------------------------- */
	coverage := CheckCoverageDefect(input)

	/* -- 7. behavioral predicate (OD-R09 explicit abstention record) ---------- */
	behavioral := CheckBehavioralPredicate(decomposed, tracker.Snapshot(), contractAware.Corroborated)

	checks := []schemas.VerificationCheck{
		decomposition,
		anchorBinding,
		headVerification,
		overDeletion,
		contractAware.Check,
		coverage,
		behavioral,
	}

	status := schemas.CheckPass
	for _, check := range checks {
		if check.Status == schemas.CheckFail {
			status = schemas.CheckFail
			break
		}
	}

	return Tier2Result{
		Tier:   schemas.Tier2,
		Status: status,
		Checks: checks,
		Context: Tier2Context{
			Claims:     tracker.Snapshot(),
			Decomposed: decomposed,
			Bindings:   bindings,
		},
	}, nil
}
