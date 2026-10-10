package verifier

// Tier-2 behavioral-predicate abstention record (port of
// tier2/behavioral.ts; P4-T12; OD-R09; PLAN §5.4; ARCH §4.8 Tier-2):
// behavioral predicates are NOT deterministically verifiable in V1 — the check
// emits the EXPLICIT `behavioral-predicate-unverified` record (kebab-case form
// of OD-R09's literal `behavioral_predicate_unverified`) whenever ≥1 behavioral
// claim remains an UNVERIFIED predicate: a final tracker status of
// `behavioral-unverified` (corroborated ones became 'supported' through
// head-verify's contract port), or a final `unanchored` status — a behavioral
// claim that failed to BIND is also an unverified behavioral predicate (the
// auto gate blocks it via unanchored-claims, but this record must not overstate
// its coverage). The detail lists the two groups distinctly.
//
// Wave-1 schema decision: SKIPPED-NOT-FAIL — the record never rejects the
// patch; it clears auto-eligibility downstream (schemas.ResolveAutoEligibility
// derives `behavioral-predicate-unverified` from the claim statuses), routing
// the patch to suggest, never auto (OD-R09). Fail-closed on a missing snapshot
// entry: an ungraded behavioral claim cannot silently count as verified, so it
// counts as remaining.
//
// Pure and deterministic (module doc): no fs, no git, no network, no clock of
// its own — the same inputs always yield deep-equal records.

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ---------------------------------------------------------------- exports */

// CheckBehavioralPredicate emits the OD-R09 abstention record over the run's
// decomposed behavioral claims and their FINAL tracker statuses (`claims` = the
// tracker snapshot, in index order). Remaining = `behavioral-unverified` claims
// plus `unanchored` behavioral claims (both are unverified predicates — module
// doc). See the module doc for the skipped-not-fail decision. Pure and
// deterministic (module doc).
func CheckBehavioralPredicate(
	decomposed []DecomposedClaim,
	claims []schemas.VerificationClaim,
	corroborated map[int]bool,
) schemas.VerificationCheck {
	var behavioral []DecomposedClaim
	for _, claim := range tier2SortDecomposedByIndex(decomposed) {
		if claim.Kind == DecomposedClaimKindBehavioral {
			behavioral = append(behavioral, claim)
		}
	}
	var unverified, unanchored []DecomposedClaim
	for _, claim := range behavioral {
		if corroborated[claim.Index] {
			continue
		}
		// Fail-closed: a behavioral claim with no snapshot entry is ungraded —
		// it cannot silently count as verified, so it counts as remaining.
		status := schemas.ClaimVerdictBehavioralUnverified
		if claim.Index >= 0 && claim.Index < len(claims) {
			status = claims[claim.Index].Status
		}
		if status == schemas.ClaimVerdictBehavioralUnverified {
			unverified = append(unverified, claim)
		} else if status == schemas.ClaimVerdictUnanchored {
			unanchored = append(unanchored, claim)
		}
	}
	if len(unverified)+len(unanchored) > 0 {
		var parts []string
		if len(unverified) > 0 {
			ids := make([]string, 0, len(unverified))
			for _, claim := range unverified {
				ids = append(ids, tier2ClaimIDFor(claim))
			}
			parts = append(parts, fmt.Sprintf(
				"%d behavioral predicate(s) not deterministically verifiable in V1 (OD-R09): %s",
				len(unverified), strings.Join(ids, ", ")))
		}
		if len(unanchored) > 0 {
			ids := make([]string, 0, len(unanchored))
			for _, claim := range unanchored {
				ids = append(ids, tier2ClaimIDFor(claim))
			}
			parts = append(parts, fmt.Sprintf(
				"%d unanchored behavioral claim(s) — unbound predicates route suggest, never auto "+
					"(PLAN §5.6/ARCH §4.8): %s",
				len(unanchored), strings.Join(ids, ", ")))
		}
		return schemas.VerificationCheck{
			ID:     "behavioral-predicate-unverified",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr(fmt.Sprintf("%s — routed to suggest, never auto",
				strings.Join(parts, "; "))),
		}
	}
	return schemas.VerificationCheck{
		ID:     "behavioral-predicate-unverified",
		Status: schemas.CheckPass,
		Detail: tier2StringPtr(fmt.Sprintf(
			"no unverified behavioral predicates (%d behavioral claim(s) checked)", len(behavioral))),
	}
}
