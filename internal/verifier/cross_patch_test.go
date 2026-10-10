// Ported from packages/verifier/test/cross-patch.test.ts (R16, V-P4-12;
// Phase 4 W5b): the run-level detection (checkCrossPatchConsistency) over
// sibling patches sharing a claim id with DIFFERENT values, the report
// fold-in (applyCrossPatchConflict) blocking BOTH involved patches to review
// with the `cross-patch-consistency` tier-2 record, the uninvolved third patch
// returned unchanged, and idempotent re-application without duplicate records.
//
// DEVIATION from the TS suite (forced by file ownership): the TS test sourced
// its reports from the real `verifyPatch` over the payments pipeline repo
// fixture (git-backed, peer-owned pipeline files). This port builds the two
// individually-consistent (all-tiers-pass, verdict pass) reports directly as
// schema-valid literals — the surface the assertions pin (tier-2 record shape,
// the R16 routing, idempotency, the unchanged-reference rule) is identical and
// the report fixtures are validated through ValidateReport before use.
package verifier_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- fixtures */

// claimIdempotency is the R16 fixture claim id (TS CLAIM_IDEMPOTENCY).
const claimIdempotency = "claim_refund_idempotency"

const (
	crossPaymentsDocPath     = "docs/payments.md"
	crossArchitectureDocPath = "docs/architecture.md"
)

// crossPaymentsDoc / crossArchitectureDoc are the two fixture documents (the
// R16 sibling pair); line 3 of each carries the claim's asserted value.
const crossPaymentsDoc = "# Payments\n\nRefunds require an idempotency key.\n" +
	"The capture amount defaults to the order total.\nDeclined refunds are logged."

const crossArchitectureDoc = "# Architecture\n\nRefund amounts are shown in USD.\n" +
	"Escalation contacts live in docs/oncall.md."

// claimedPaymentsPatch is the payments-side patch of the conflict pair: same
// claim id, value A (TS claimedPaymentsPatch).
func claimedPaymentsPatch(t *testing.T) *schemas.DocPatch {
	t.Helper()
	return t3BuildPatch(t, crossPaymentsDoc, t3LineEdits(t, crossPaymentsDoc, []t3LineEdit{{
		line:        "The capture amount defaults to the order total.",
		replacement: "The capture amount defaults to `amountMinor`.",
		claimIDs:    []string{claimIdempotency},
	}}), crossPaymentsDocPath)
}

// conflictingSiblingPatch is a patch with the SAME claim id but a DIFFERENT
// value (the R16 sibling; TS conflictingSiblingPatch).
func conflictingSiblingPatch(t *testing.T) *schemas.DocPatch {
	t.Helper()
	return t3BuildPatch(t, crossArchitectureDoc, t3LineEdits(t, crossArchitectureDoc, []t3LineEdit{{
		line:        "Refund amounts are shown in USD.",
		replacement: "Refund amounts are shown in minor units.",
		claimIDs:    []string{claimIdempotency},
	}}), crossArchitectureDocPath)
}

// uninvolvedCapturePatch carries no claim_ids ⇒ uninvolved in any conflict
// (TS patchC).
func uninvolvedCapturePatch(t *testing.T) *schemas.DocPatch {
	t.Helper()
	return t3BuildPatch(t, crossPaymentsDoc, t3LineEdits(t, crossPaymentsDoc, []t3LineEdit{{
		line:        "The capture amount defaults to the order total.",
		replacement: "The capture amount defaults to `amountMinor`.",
	}}), crossPaymentsDocPath)
}

// crossPatchCleanReport is one individually-consistent verification report:
// three tiers, every check 'pass', verdict 'pass' (the shape verifyPatch
// produces for a clean patch). Validated schema-wise before use.
func crossPatchCleanReport(t *testing.T, docPath string) *schemas.VerificationReport {
	t.Helper()
	report := &schemas.VerificationReport{
		Schema:   "docsync.verification-report/v1",
		RunID:    t3RunID,
		DocPath:  docPath,
		PatchRef: "state/" + docPath + "#proposal-01J9ZK7Q2M",
		Tiers: []schemas.VerificationTierReport{
			{
				Tier:   schemas.Tier1,
				Status: schemas.CheckPass,
				Checks: []schemas.VerificationCheck{
					{ID: "patch-applies-cleanly", Status: schemas.CheckPass},
					{ID: "manifest-allowlist-only", Status: schemas.CheckPass},
					{ID: "ownership-floor", Status: schemas.CheckPass},
					{ID: "minimality-bound", Status: schemas.CheckPass},
					{ID: "whole-document-rewrite", Status: schemas.CheckPass},
					{ID: "structural-integrity", Status: schemas.CheckPass},
					{ID: "link-resolution", Status: schemas.CheckPass},
					{ID: "examples-parse", Status: schemas.CheckPass},
				},
			},
			{
				Tier:   schemas.Tier2,
				Status: schemas.CheckPass,
				Checks: []schemas.VerificationCheck{
					{ID: "claim-decomposition", Status: schemas.CheckPass},
					{ID: "claim-anchor-binding", Status: schemas.CheckPass},
					{ID: "head-verification", Status: schemas.CheckPass},
					{ID: "over-deletion-guard", Status: schemas.CheckPass},
					{ID: "contract-aware-check", Status: schemas.CheckPass},
					{ID: "coverage-defect", Status: schemas.CheckPass},
				},
			},
			{
				Tier:   schemas.Tier3,
				Status: schemas.CheckPass,
				Checks: []schemas.VerificationCheck{
					{ID: "judge-independence", Status: schemas.CheckPass},
					{ID: "judge-second-pass", Status: schemas.CheckPass},
				},
			},
		},
		Claims:           []schemas.VerificationClaim{},
		Verdict:          schemas.VerdictPass,
		AutoEligible:     true,
		AutoBlockReasons: []string{},
		PolicyVersion:    t3PolicyVer,
	}
	require.Empty(t, report.ValidateReport(), "fixture report must be schema-valid")
	return report
}

// tier2Record returns the first `cross-patch-consistency` record of the
// tier-2 report (nil when absent).
func tier2Record(report *schemas.VerificationReport) *schemas.VerificationCheck {
	for _, tier := range report.Tiers {
		if tier.Tier != schemas.Tier2 {
			continue
		}
		for index := range tier.Checks {
			if tier.Checks[index].ID == "cross-patch-consistency" {
				return &tier.Checks[index]
			}
		}
	}
	return nil
}

// requireRoutedToReview asserts the R16 fold-in posture on one folded report.
func requireRoutedToReview(t *testing.T, report *schemas.VerificationReport) {
	t.Helper()
	require.Empty(t, report.ValidateReport(), "folded report must stay schema-valid")
	require.NotNil(t, report.CrossPatchConflict)
	assert.True(t, *report.CrossPatchConflict, "cross_patch_conflict must be pinned true")
	assert.Equal(t, schemas.VerdictReview, report.Verdict, "a conflict blocks to review, never auto")
	record := tier2Record(report)
	require.NotNil(t, record, "the tier-2 cross-patch-consistency record must be present")
	assert.Equal(t, schemas.CheckPass, record.Status)
	require.NotNil(t, record.Detail)
	assert.Contains(t, *record.Detail, "on claim "+claimIdempotency)
	assert.Contains(t, *record.Detail, "both patches routed to review (R16, V-P4-12)")
}

/* ------------------------------------------------------------------ tests */

func TestV_P4_12_SameClaimDifferentValueBlocksBothToReview(t *testing.T) {
	patchA := claimedPaymentsPatch(t)
	patchB := conflictingSiblingPatch(t)
	patchC := uninvolvedCapturePatch(t) // no claim_ids ⇒ uninvolved
	patches := []*schemas.DocPatch{patchA, patchB, patchC}

	// detection: one conflict (the shared claim id), patches 0 and 1 implicated
	result := verifier.CheckCrossPatchConsistency(patches)
	require.Len(t, result.Conflicts, 1)
	conflict := result.Conflicts[0]
	assert.Equal(t, claimIdempotency, conflict.ClaimID)
	require.Len(t, conflict.Conflicts, 2)
	assert.Equal(t, map[int]bool{0: true, 1: true}, result.ConflictedPatchIndexes)

	// the fold-in: BOTH involved reports route to review (never auto)
	reportA := crossPatchCleanReport(t, crossPaymentsDocPath)
	reportB := crossPatchCleanReport(t, crossArchitectureDocPath)
	require.Equal(t, schemas.VerdictPass, reportA.Verdict) // individually consistent…
	require.Equal(t, schemas.VerdictPass, reportB.Verdict) // …but siblings still conflict

	conflictedA, err := verifier.ApplyCrossPatchConflict(reportA, result.Conflicts, 0, patches)
	require.NoError(t, err)
	conflictedB, err := verifier.ApplyCrossPatchConflict(reportB, result.Conflicts, 1, patches)
	require.NoError(t, err)

	requireRoutedToReview(t, conflictedA)
	requireRoutedToReview(t, conflictedB)
	// each side's record names ITS value vs the sibling's value + sibling doc
	detailA := tier2Record(conflictedA).Detail
	require.NotNil(t, detailA)
	assert.Contains(t, *detailA, "\"The capture amount defaults to `amountMinor`.\"")
	assert.Contains(t, *detailA, `"Refund amounts are shown in minor units."`)
	assert.Contains(t, *detailA, "("+crossArchitectureDocPath+")")

	// the uninvolved patch is returned unchanged (same reference)
	reportC := crossPatchCleanReport(t, crossPaymentsDocPath)
	unchanged, err := verifier.ApplyCrossPatchConflict(reportC, result.Conflicts, 2, patches)
	require.NoError(t, err)
	require.Same(t, reportC, unchanged, "an uninvolved patch's report is returned as-is")

	// out-of-range patch indexes are a no-op, never a panic
	unchangedRange, err := verifier.ApplyCrossPatchConflict(reportC, result.Conflicts, len(patches), patches)
	require.NoError(t, err)
	require.Same(t, reportC, unchangedRange)
}

func TestCrossPatchConflictIdempotentReApplication(t *testing.T) {
	patchA := claimedPaymentsPatch(t)
	patchB := conflictingSiblingPatch(t)
	patches := []*schemas.DocPatch{patchA, patchB}
	result := verifier.CheckCrossPatchConsistency(patches)
	reportA := crossPatchCleanReport(t, crossPaymentsDocPath)

	once, err := verifier.ApplyCrossPatchConflict(reportA, result.Conflicts, 0, patches)
	require.NoError(t, err)
	twice, err := verifier.ApplyCrossPatchConflict(once, result.Conflicts, 0, patches)
	require.NoError(t, err)

	assert.Equal(t, once, twice, "re-application adds no duplicate records")
	records := 0
	for _, tier := range twice.Tiers {
		if tier.Tier != schemas.Tier2 {
			continue
		}
		for _, check := range tier.Checks {
			if check.ID == "cross-patch-consistency" {
				records++
			}
		}
	}
	assert.Equal(t, 1, records)
	require.NotNil(t, twice.CrossPatchConflict)
	assert.True(t, *twice.CrossPatchConflict)
	assert.Equal(t, schemas.VerdictReview, twice.Verdict)
}

func TestCrossPatchConsistencyNoConflictsNoRecords(t *testing.T) {
	patchA := claimedPaymentsPatch(t)
	// A lone patch asserting one value for a claim is consistent — no conflict.
	patches := []*schemas.DocPatch{patchA}
	result := verifier.CheckCrossPatchConsistency(patches)

	assert.Empty(t, result.Conflicts)
	assert.Empty(t, result.ConflictedPatchIndexes)

	report := crossPatchCleanReport(t, crossPaymentsDocPath)
	unchanged, err := verifier.ApplyCrossPatchConflict(report, result.Conflicts, 0, patches)
	require.NoError(t, err)
	require.Same(t, report, unchanged)
	assert.Nil(t, unchanged.CrossPatchConflict)
}
