package verifier

// Ported from packages/verifier/src/consistency/cross-patch.ts (R16, V-P4-12;
// PLAN §5.4 item 18, P4-T18; ARCH §4.8): the run-level pass that detects
// SIBLING patches asserting DIFFERENT values for the SAME claim id and routes
// every involved patch to review — neither patch is individually wrong, so a
// conflict never REJECTS on its own; it blocks to review and never auto
// (invariant posture of the `cross_patch_conflict` report field and the closed
// `cross-patch-consistency` tier-2 check id).
//
// Detection is a pure function over the run's patches: per patch
// `exportPatchClaimValues` (internal/patch — the R16 data surface), the union
// grouped by `detectClaimValueConflicts`; a patch is conflicted iff any of its
// exports' claim_id appears in the conflict set (a patch asserting two values
// for one claim is conflicted against itself, too).
//
// ApplyCrossPatchConflict folds the detection outcome into ONE patch's
// verification report WITHOUT mutating it: the tier-2 checks array is EXTENDED
// with one `cross-patch-consistency` record (status 'pass' — detection
// succeeded ⇒ routed to review; a 'fail' here would reject, contradicting R16)
// per conflicting claim id in deterministic order, the `cross_patch_conflict`
// field is pinned true, and the verdict is RECOMPUTED through the canonical
// mapping (schemas.ResolveVerificationVerdict) — a reject trigger still
// dominates (the mapping handles precedence). Idempotent re-application must
// not double-add: claim ids whose record detail is already present are
// skipped. The result is re-parsed through the registered verification-report
// parser before returning — schema validity is preserved by construction (fail
// closed): a fold that would invalidate the report is a typed error, never a
// silently-shipped record.
//
// Purity: both exports are pure — no fs, no git, no network, no clock.

import (
	"encoding/json"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CROSS_PATCH_CHECK_ID — the closed tier-2 check id the consistency pass emits
// records under.
const crossPatchCheckID = "cross-patch-consistency"

// CrossPatchResult is the outcome of the run-level consistency pass (R16).
type CrossPatchResult struct {
	// Conflicts is every same-claim-different-value conflict, deterministic order.
	Conflicts []patch.ClaimValueConflict
	// ConflictedPatchIndexes holds the input-order indexes of the patches
	// implicated by any conflict (TS ReadonlySet<number>).
	ConflictedPatchIndexes map[int]bool
}

// CheckCrossPatchConsistency detects same-claim-different-value conflicts
// across the run's sibling patches (module doc). Pure: the input patches are
// never mutated, the union of the per-patch claim-value exports feeds the
// deterministic detector, and a patch is conflicted iff one of its exports'
// claim ids conflicts.
func CheckCrossPatchConsistency(patches []*schemas.DocPatch) CrossPatchResult {
	exported := make([][]patch.ExportedClaimValue, len(patches))
	var union []patch.ExportedClaimValue
	for index, targetPatch := range patches {
		records := patch.ExportPatchClaimValues(targetPatch)
		exported[index] = records
		union = append(union, records...)
	}
	conflicts := patch.DetectClaimValueConflicts(union)
	conflictIDs := make(map[string]bool, len(conflicts))
	for _, conflict := range conflicts {
		conflictIDs[conflict.ClaimID] = true
	}
	conflictedPatchIndexes := make(map[int]bool)
	for index, records := range exported {
		for _, record := range records {
			if conflictIDs[record.ClaimID] {
				conflictedPatchIndexes[index] = true
				break
			}
		}
	}
	return CrossPatchResult{Conflicts: conflicts, ConflictedPatchIndexes: conflictedPatchIndexes}
}

// conflictDetail is the deterministic `cross-patch-consistency` record detail
// for ONE conflicting claim id from THIS patch's perspective: this patch's
// asserted value (its first export record for the claim — deterministic)
// versus the first record of the sibling value group.
func conflictDetail(conflict patch.ClaimValueConflict, targetPatch *schemas.DocPatch) string {
	// This patch's asserted value: its FIRST export record for the claim
	// (deterministic; "" when the patch exports none — TS `own?.value ?? ''`).
	var ownValue string
	for _, record := range patch.ExportPatchClaimValues(targetPatch) {
		if record.ClaimID == conflict.ClaimID {
			ownValue = record.Value
			break
		}
	}
	// The value group THIS patch belongs to (first group carrying its value);
	// -1 when no group matches (mirrors TS `undefined`).
	ownGroup := -1
	for groupIndex, group := range conflict.Conflicts {
		for _, record := range group {
			if record.Value == ownValue {
				ownGroup = groupIndex
				break
			}
		}
		if ownGroup >= 0 {
			break
		}
	}
	// The first group that is not the own group — the sibling value group.
	siblingGroup := -1
	for groupIndex := range conflict.Conflicts {
		if groupIndex != ownGroup {
			siblingGroup = groupIndex
			break
		}
	}
	siblingValue := ""
	siblingDocPath := ""
	if siblingGroup >= 0 && len(conflict.Conflicts[siblingGroup]) > 0 {
		siblingValue = conflict.Conflicts[siblingGroup][0].Value
		siblingDocPath = conflict.Conflicts[siblingGroup][0].DocPath
	}
	return "same-claim-different-value conflict on claim " + conflict.ClaimID +
		": \"" + ownValue + "\" vs \"" + siblingValue + "\" (" + siblingDocPath +
		") — both patches routed to review (R16, V-P4-12)"
}

// ApplyCrossPatchConflict folds a detected cross-patch conflict into ONE
// patch's verification report (module doc for the record shape, the verdict
// recomputation, and the idempotency rule). Returns the input report UNCHANGED
// (same reference) when the patch at `patchIndex` exports no conflicting claim
// id. A fold that would break report schema validity returns a typed error
// (the TS zod-parse step, fail closed).
func ApplyCrossPatchConflict(
	report *schemas.VerificationReport,
	conflicts []patch.ClaimValueConflict,
	patchIndex int,
	patches []*schemas.DocPatch,
) (*schemas.VerificationReport, error) {
	if patchIndex < 0 || patchIndex >= len(patches) {
		return report, nil
	}
	targetPatch := patches[patchIndex]
	conflictIDs := make(map[string]bool, len(conflicts))
	for _, conflict := range conflicts {
		conflictIDs[conflict.ClaimID] = true
	}
	exported := patch.ExportPatchClaimValues(targetPatch)
	involved := false
	for _, record := range exported {
		if conflictIDs[record.ClaimID] {
			involved = true
			break
		}
	}
	if !involved {
		return report, nil
	}

	/* -- one record per conflicting claim id, deterministic order, no dupes -- */
	tier2 := -1
	for index := range report.Tiers {
		if report.Tiers[index].Tier == schemas.Tier2 {
			tier2 = index
			break
		}
	}
	existingDetails := make(map[string]bool)
	if tier2 >= 0 {
		for _, check := range report.Tiers[tier2].Checks {
			if check.ID == crossPatchCheckID {
				detail := ""
				if check.Detail != nil {
					detail = *check.Detail
				}
				existingDetails[detail] = true
			}
		}
	}
	var additions []schemas.VerificationCheck
	seenClaims := make(map[string]bool)
	for _, conflict := range conflicts {
		if !conflictIDs[conflict.ClaimID] {
			continue
		}
		if seenClaims[conflict.ClaimID] {
			continue
		}
		seenClaims[conflict.ClaimID] = true
		detail := conflictDetail(conflict, targetPatch)
		if existingDetails[detail] {
			continue
		}
		existingDetails[detail] = true
		additions = append(additions, schemas.VerificationCheck{
			ID:     crossPatchCheckID,
			Status: schemas.CheckPass,
			Detail: &detail,
		})
	}

	tiers := report.Tiers
	if tier2 >= 0 && len(additions) > 0 {
		extended := make([]schemas.VerificationTierReport, len(report.Tiers))
		for index := range report.Tiers {
			if index == tier2 {
				checks := make([]schemas.VerificationCheck, 0, len(report.Tiers[index].Checks)+len(additions))
				checks = append(checks, report.Tiers[index].Checks...)
				checks = append(checks, additions...)
				extended[index] = schemas.VerificationTierReport{
					Tier:   report.Tiers[index].Tier,
					Status: report.Tiers[index].Status,
					Checks: checks,
				}
				continue
			}
			extended[index] = report.Tiers[index]
		}
		tiers = extended
	}

	verdict := schemas.ResolveVerificationVerdict(tiers, report.Judge, report.JudgeAbstain, true)
	updated := *report
	updated.Tiers = tiers
	updated.Verdict = verdict
	crossPatchConflict := true
	updated.CrossPatchConflict = &crossPatchConflict

	// The schema re-parse (TS VERIFICATION_REPORT_SCHEMA.parse): the folded
	// report must stay schema-valid — a fold that would not is a typed error,
	// never a silently-shipped record (fail closed).
	wire, err := json.Marshal(&updated)
	if err != nil {
		return nil, docutil.InternalError("cross-patch fold produced an unserializable report: %s", err.Error())
	}
	parsed, err := schemas.ParseVerificationReport(wire)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}
