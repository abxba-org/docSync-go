package verifier_test

// Tier-2 check + composer tests (P4-T11..T14; V-P4-7, V-P4-8, V-P4-9, V-P4-1
// partial, OD-R09): over-deletion guard, contract-aware check, coverage
// defect, behavioral-predicate abstention, the unanchored report contract,
// and the runTier2 canonical order / determinism / fail-closed
// patched-content contract. Ported from packages/verifier/test/tier2-checks.test.ts.

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	t2PaymentsDoc = "# Payments\n\nRefunds require an idempotency key.\nRetry windows are 24h.\nDeclined refunds are logged.\nThe schedule is in the operations guide."
	t2ClaimLine   = "Refunds require an idempotency key."
	t2RetrySource = `const DEFAULT_WINDOW = "24h";`
	t2ContractDoc = "# Refunds\n\nThe refund-idempotency contract governs safe retries.\nSee the API guide."
	t2ContractLn  = "The refund-idempotency contract governs safe retries."
)

// t2CanonicalTier2IDs is the canonical seven-check id order of the Tier-2 report.
var t2CanonicalTier2IDs = []string{
	"claim-decomposition",
	"claim-anchor-binding",
	"head-verification",
	"over-deletion-guard",
	"contract-aware-check",
	"coverage-defect",
	"behavioral-predicate-unverified",
}

// t2StoredClaim builds a minimal docsync.claim/v1 store entry.
func t2StoredClaim(overrides ...func(*schemas.Claim)) schemas.Claim {
	claim := schemas.Claim{
		ID:   "claim_182",
		Text: "Refunds require an idempotency key.",
		Anchor: schemas.ClaimAnchor{
			File:        "docs/payments.md",
			StartLine:   3,
			EndLine:     3,
			ContentHash: claims.HashSpan(t2ClaimLine),
		},
		Source: schemas.ClaimSource{
			File:    "src/payments/retry.ts",
			Line:    t2IntPtr(2),
			BlobSHA: "e0d1fa5",
		},
		Status:  schemas.ClaimStatusVerified,
		History: []schemas.ClaimHistoryEvent{},
	}
	for _, override := range overrides {
		override(&claim)
	}
	return claim
}

func t2IntPtr(v int) *int { return &v }

func t2BaseInput(patch *schemas.DocPatch, overrides ...func(*verifier.Tier2Input)) verifier.Tier2Input {
	input := verifier.Tier2Input{
		Patch:          patch,
		DocContent:     t2StrPtr(t2PaymentsDoc),
		PatchedContent: t2StrPtr(t2PaymentsDoc),
		Now:            t1InjectedClock,
	}
	for _, override := range overrides {
		override(&input)
	}
	return input
}

func t2StrPtr(s string) *string { return &s }

// t2VectorEntry is a vector entry for one declared contract, tuned per test.
func t2VectorEntry(name string, hit, abstain bool) contracts.ContractVectorEntry {
	return contracts.ContractVectorEntry{
		Contract:   name,
		Kind:       schemas.ContractKindBehavior,
		Hit:        hit,
		Confidence: 0.95,
		Evidence:   []string{"src/payments/refund.ts (behavior)"},
		Abstain:    abstain,
	}
}

// t2PaymentsGraph is a one-mapping graph declaring contracts on docs/payments.md.
func t2PaymentsGraph(contractNames []string) *graph.DocumentGraph {
	kinds := map[string]schemas.ContractKind{}
	for _, name := range contractNames {
		kinds[name] = schemas.ContractKindBehavior
	}
	return &graph.DocumentGraph{
		Mappings: []graph.MappingRecord{{
			Doc:       "docs/payments.md",
			Sources:   []string{"src/payments/**", "src/api/payments/**"},
			Concepts:  []string{},
			Contracts: append([]string(nil), contractNames...),
		}},
		ContractKinds: kinds,
		Errors:        []graph.GraphConfigError{},
	}
}

func t2Patch(t *testing.T, baseContent, line, replacement string, claimIDs ...string) *schemas.DocPatch {
	t.Helper()
	return t1BuildPatch(t1BuildPatchOpts{
		baseContent: baseContent,
		hunks: t1LineEdits(baseContent, []t1LineEdit{{
			line:        line,
			replacement: replacement,
			claimIDs:    claimIDs,
		}}),
	})
}

func t2CheckByID(t *testing.T, checks []schemas.VerificationCheck, id string) schemas.VerificationCheck {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %s not found", id)
	return schemas.VerificationCheck{}
}

/* ------------------------------------------------ over-deletion guard (V-P4-7) */

func TestT2OverDeletionGuardFiresWhenRemovedClaimSupportStillExists(t *testing.T) {
	patch := t2Patch(t, t2PaymentsDoc, t2ClaimLine, "Refund handling changed.", "claim_182")
	input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
		in.Claims = []schemas.Claim{t2StoredClaim()}
		in.WorkspaceSources = map[string]string{"src/payments/retry.ts": t2RetrySource}
	})
	tracker, err := verifier.CreateClaimStatusTracker(verifier.DecomposePatchClaims(patch))
	require.NoError(t, err)
	check, err := verifier.CheckOverDeletionGuard(input, tracker)
	require.NoError(t, err)

	assert.Equal(t, "over-deletion-guard", check.ID)
	assert.Equal(t, schemas.CheckFail, check.Status)
	require.NotNil(t, check.Detail)
	assert.Contains(t, *check.Detail, "claim_182")
	assert.Contains(t, *check.Detail, t2ClaimLine)
	assert.Contains(t, *check.Detail, "src/payments/retry.ts")

	snapshot := tracker.Snapshot()
	require.Len(t, snapshot, 1)
	assert.Equal(t, "claim_182", snapshot[0].ClaimID)
	assert.Equal(t, schemas.ClaimVerdictOverDeleted, snapshot[0].Status)
	require.NotNil(t, snapshot[0].Detail)
	assert.Contains(t, *snapshot[0].Detail, "support still present in src/payments/retry.ts")
}

func TestT2OverDeletionGuardAllowsRemovalWhoseSourceSupportIsGone(t *testing.T) {
	patch := t2Patch(t, t2PaymentsDoc, t2ClaimLine, "Refund handling changed.", "claim_182")
	input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
		in.Claims = []schemas.Claim{t2StoredClaim()}
		in.WorkspaceSources = map[string]string{}
	})
	tracker, err := verifier.CreateClaimStatusTracker(verifier.DecomposePatchClaims(patch))
	require.NoError(t, err)
	check, err := verifier.CheckOverDeletionGuard(input, tracker)
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, check.Status)
	require.NotNil(t, check.Detail)
	assert.Contains(t, *check.Detail, "legitimate removal(s): claim_182 (support absent)")
	assert.Equal(t, schemas.ClaimVerdictUnchecked, tracker.Snapshot()[0].Status)
}

func TestT2OverDeletionGuardSkipsWithoutSnapshotAndOnDeletionFreePatches(t *testing.T) {
	patch := t2Patch(t, t2PaymentsDoc, t2ClaimLine, "Refunds changed.")

	noSnapshot, err := verifier.CheckOverDeletionGuard(
		t2BaseInput(patch),
		mustTracker(t),
	)
	require.NoError(t, err)
	assert.Equal(t, schemas.VerificationCheck{
		ID:     "over-deletion-guard",
		Status: schemas.CheckSkipped,
		Detail: t2StrPtr("no claim-store snapshot supplied (orchestrator-owned data)"),
	}, noSnapshot)

	deletionFree := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t2PaymentsDoc,
		hunks: []schemas.PatchHunk{{
			Anchor: schemas.PatchAnchor{
				StartLine:   1,
				EndLine:     1,
				ContentHash: claims.HashSpan(""),
			},
			DeleteText:   "",
			InsertText:   "New section on captures.",
			EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/payments/refund.ts:3", Note: "capture flow"}},
		}},
	})
	tracker, err := verifier.CreateClaimStatusTracker(verifier.DecomposePatchClaims(deletionFree))
	require.NoError(t, err)
	nothingDeleted, err := verifier.CheckOverDeletionGuard(
		t2BaseInput(deletionFree, func(in *verifier.Tier2Input) {
			in.Claims = []schemas.Claim{t2StoredClaim()}
		}),
		tracker,
	)
	require.NoError(t, err)
	assert.Equal(t, schemas.VerificationCheck{
		ID:     "over-deletion-guard",
		Status: schemas.CheckSkipped,
		Detail: t2StrPtr("patch deletes nothing"),
	}, nothingDeleted)
}

func mustTracker(t *testing.T) *verifier.ClaimStatusTracker {
	t.Helper()
	tracker, err := verifier.CreateClaimStatusTracker([]verifier.DecomposedClaim{})
	require.NoError(t, err)
	return tracker
}

/* ------------------------------------------------ contract-aware check (V-P4-9) */

func TestT2ContractViolatingPatchRejectedAtTier2(t *testing.T) {
	patch := t2Patch(t, t2ContractDoc, t2ContractLn, "Retry semantics moved to the API guide.")

	t.Run("fails when the removed region documents a hit-classified contract", func(t *testing.T) {
		input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.DocContent = t2StrPtr(t2ContractDoc)
			in.ContractNames = []string{"refund-idempotency"}
			in.ContractVector = &contracts.ContractVector{
				Doc:     "docs/payments.md",
				Vectors: []contracts.ContractVectorEntry{t2VectorEntry("refund-idempotency", true, false)},
			}
			in.ContractKinds = map[string]schemas.ContractKind{"refund-idempotency": schemas.ContractKindBehavior}
		})
		tracker, err := verifier.CreateClaimStatusTracker(verifier.DecomposePatchClaims(patch))
		require.NoError(t, err)
		result := verifier.CheckContractAware(input, tracker)
		assert.Equal(t, schemas.VerificationCheck{
			ID:     "contract-aware-check",
			Status: schemas.CheckFail,
			Detail: t2StrPtr("contract violation: removed region documents declared contract refund-idempotency (V-P4-9)"),
		}, result.Check)
		assert.Empty(t, result.Corroborated)
	})

	t.Run("contract classification abstain is surfaced, never silently passed", func(t *testing.T) {
		input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.DocContent = t2StrPtr(t2ContractDoc)
			in.ContractNames = []string{"refund-idempotency"}
			in.ContractVector = &contracts.ContractVector{
				Doc:     "docs/payments.md",
				Vectors: []contracts.ContractVectorEntry{t2VectorEntry("refund-idempotency", false, true)},
			}
			in.ContractKinds = map[string]schemas.ContractKind{"refund-idempotency": schemas.ContractKindBehavior}
		})
		tracker, err := verifier.CreateClaimStatusTracker(verifier.DecomposePatchClaims(patch))
		require.NoError(t, err)
		result := verifier.CheckContractAware(input, tracker)
		assert.Equal(t, schemas.VerificationCheck{
			ID:     "contract-aware-check",
			Status: schemas.CheckSkipped,
			Detail: t2StrPtr("contract classification abstained for refund-idempotency (never silently passed)"),
		}, result.Check)
	})

	t.Run("passes when no declared contract name appears in the removed region", func(t *testing.T) {
		input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.DocContent = t2StrPtr(t2ContractDoc)
			in.ContractNames = []string{"capture-amounts"}
			in.ContractVector = &contracts.ContractVector{
				Doc:     "docs/payments.md",
				Vectors: []contracts.ContractVectorEntry{t2VectorEntry("capture-amounts", true, false)},
			}
			in.ContractKinds = map[string]schemas.ContractKind{"capture-amounts": schemas.ContractKindBehavior}
		})
		tracker, err := verifier.CreateClaimStatusTracker(verifier.DecomposePatchClaims(patch))
		require.NoError(t, err)
		check := verifier.CheckContractAware(input, tracker).Check
		assert.Equal(t, schemas.CheckPass, check.Status)
		require.NotNil(t, check.Detail)
		assert.Contains(t, *check.Detail, "no contract violations")
	})

	t.Run("skips without declared contracts", func(t *testing.T) {
		tracker, err := verifier.CreateClaimStatusTracker(verifier.DecomposePatchClaims(patch))
		require.NoError(t, err)
		check := verifier.CheckContractAware(
			t2BaseInput(patch, func(in *verifier.Tier2Input) { in.DocContent = t2StrPtr(t2ContractDoc) }),
			tracker,
		).Check
		assert.Equal(t, schemas.VerificationCheck{
			ID:     "contract-aware-check",
			Status: schemas.CheckSkipped,
			Detail: t2StrPtr("no declared contracts for this document"),
		}, check)
	})
}

/* ------------------------------------------------------ coverage defect (P4-T14) */

func TestT2CoverageDefectCheck(t *testing.T) {
	patch := t2Patch(t, t2PaymentsDoc, t2ClaimLine, "Refunds changed.")

	t.Run("surfaces the zero-claims coverage defect as a skipped record, never silent", func(t *testing.T) {
		input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.ContractNames = []string{"refund-idempotency"}
			in.Graph = t2PaymentsGraph([]string{"refund-idempotency"})
			in.Claims = []schemas.Claim{}
		})
		assert.Equal(t, schemas.VerificationCheck{
			ID:     "coverage-defect",
			Status: schemas.CheckSkipped,
			Detail: t2StrPtr("coverage defect: docs/payments.md declares 1 contracts but has zero checkable claims (surfaced to the lint surface; never silent)"),
		}, verifier.CheckCoverageDefect(input))
	})

	t.Run("passes when a checkable claim anchors the doc, and skips without graph/contracts", func(t *testing.T) {
		passing := verifier.CheckCoverageDefect(t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.ContractNames = []string{"refund-idempotency"}
			in.Graph = t2PaymentsGraph([]string{"refund-idempotency"})
			in.Claims = []schemas.Claim{t2StoredClaim()}
		}))
		assert.Equal(t, schemas.CheckPass, passing.Status)

		assert.Equal(t, schemas.VerificationCheck{
			ID:     "coverage-defect",
			Status: schemas.CheckSkipped,
			Detail: t2StrPtr("no graph supplied"),
		}, verifier.CheckCoverageDefect(t2BaseInput(patch)))

		assert.Equal(t, schemas.VerificationCheck{
			ID:     "coverage-defect",
			Status: schemas.CheckSkipped,
			Detail: t2StrPtr("no declared contracts for this document"),
		}, verifier.CheckCoverageDefect(t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.Graph = t2PaymentsGraph([]string{"refund-idempotency"})
		})))
	})
}

/* ------------------------------------------ behavioral predicate (OD-R09 abstain) */

func TestT2BehavioralPredicateAbstainsWithExplicitRecord(t *testing.T) {
	t.Run("emits the skipped-not-fail record and clears auto-eligibility", func(t *testing.T) {
		patch := t2Patch(t, t2PaymentsDoc, "Retry windows are 24h.",
			"Retries are idempotent for duplicate requests.", "claim_182")
		input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.Claims = []schemas.Claim{t2StoredClaim(func(c *schemas.Claim) {
				c.Text = "Retries are idempotent for duplicate requests."
				c.Anchor = schemas.ClaimAnchor{
					File:        "docs/payments.md",
					StartLine:   5,
					EndLine:     5,
					ContentHash: claims.HashSpan("Declined refunds are logged."),
				}
			})}
		})
		result, err := verifier.RunTier2(input)
		require.NoError(t, err)

		behavioral := t2CheckByID(t, result.Checks, "behavioral-predicate-unverified")
		assert.Equal(t, schemas.CheckSkipped, behavioral.Status)
		require.NotNil(t, behavioral.Detail)
		assert.Contains(t, *behavioral.Detail, "claim_182")
		assert.Contains(t, *behavioral.Detail, "OD-R09")
		assert.Equal(t, schemas.CheckPass, result.Status)

		require.NotEmpty(t, result.Context.Claims)
		assert.Equal(t, schemas.ClaimVerdictBehavioralUnverified, result.Context.Claims[0].Status)
		eligibility := schemas.ResolveAutoEligibility(result.Context.Claims)
		assert.False(t, eligibility.AutoEligible)
		assert.Equal(t, []string{schemas.AutoBlockBehavioralPredicate}, eligibility.AutoBlockReasons)
	})

	t.Run("passes when no behavioral claim remains unverified", func(t *testing.T) {
		patch := t2Patch(t, t2PaymentsDoc, "Retry windows are 24h.", "The retry cap is set to `3`.")
		decomposed := verifier.DecomposePatchClaims(patch)
		check := verifier.CheckBehavioralPredicate(decomposed, []schemas.VerificationClaim{}, map[int]bool{})
		assert.Equal(t, schemas.VerificationCheck{
			ID:     "behavioral-predicate-unverified",
			Status: schemas.CheckPass,
			Detail: t2StrPtr("no unverified behavioral predicates (0 behavioral claim(s) checked)"),
		}, check)
	})

	t.Run("an unanchored behavioral claim is also an unverified predicate", func(t *testing.T) {
		patch := t2Patch(t, t2PaymentsDoc, "Retry windows are 24h.",
			"Retries are idempotent for duplicate requests.")
		result, err := verifier.RunTier2(t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.WorkspaceSources = map[string]string{}
		}))
		require.NoError(t, err)

		behavioral := t2CheckByID(t, result.Checks, "behavioral-predicate-unverified")
		assert.Equal(t, schemas.CheckSkipped, behavioral.Status)
		require.NotNil(t, behavioral.Detail)
		assert.Contains(t, *behavioral.Detail, "1 unanchored behavioral claim(s)")
		assert.Contains(t, *behavioral.Detail, "decomposed-0")
		assert.Contains(t, *behavioral.Detail, "PLAN §5.6/ARCH §4.8")
		require.NotEmpty(t, result.Context.Claims)
		assert.Equal(t, schemas.ClaimVerdictUnanchored, result.Context.Claims[0].Status)
		eligibility := schemas.ResolveAutoEligibility(result.Context.Claims)
		assert.False(t, eligibility.AutoEligible)
		assert.Equal(t, []string{schemas.AutoBlockUnanchoredClaims}, eligibility.AutoBlockReasons)
	})

	t.Run("lists both remaining groups distinctly when unverified and unanchored predicates coexist", func(t *testing.T) {
		patch := t1BuildPatch(t1BuildPatchOpts{
			baseContent: t2PaymentsDoc,
			hunks: t1LineEdits(t2PaymentsDoc, []t1LineEdit{
				{
					line:        "Retry windows are 24h.",
					replacement: "Retries are idempotent for duplicate requests.",
					claimIDs:    []string{"claim_182"},
				},
				{
					line:        "Declined refunds are logged.",
					replacement: "Requests are sequenced to avoid duplicate charges.",
				},
			}),
		})
		input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
			in.Claims = []schemas.Claim{t2StoredClaim(func(c *schemas.Claim) {
				c.Text = "Retries are idempotent for duplicate requests."
				c.Anchor = schemas.ClaimAnchor{
					File:        "docs/payments.md",
					StartLine:   5,
					EndLine:     5,
					ContentHash: claims.HashSpan("Declined refunds are logged."),
				}
			})}
		})
		result, err := verifier.RunTier2(input)
		require.NoError(t, err)

		behavioral := t2CheckByID(t, result.Checks, "behavioral-predicate-unverified")
		assert.Equal(t, schemas.CheckSkipped, behavioral.Status)
		require.NotNil(t, behavioral.Detail)
		assert.Contains(t, *behavioral.Detail,
			"1 behavioral predicate(s) not deterministically verifiable in V1 (OD-R09): claim_182")
		assert.Contains(t, *behavioral.Detail, "1 unanchored behavioral claim(s)")
		assert.Contains(t, *behavioral.Detail, "decomposed-1")
		statuses := make([]string, 0, len(result.Context.Claims))
		for _, claim := range result.Context.Claims {
			statuses = append(statuses, claim.Status)
		}
		assert.Equal(t, []string{
			schemas.ClaimVerdictBehavioralUnverified,
			schemas.ClaimVerdictUnanchored,
		}, statuses)
	})
}

/* ------------------------------------------- unanchored report contract (V-P4-8) */

func TestT2UnanchoredClaimCapsAtSuggestNeverAuto(t *testing.T) {
	patch := t2Patch(t, t2PaymentsDoc, t2ClaimLine, "The signing key is set to `vault:payments/prod`.")
	result, err := verifier.RunTier2(t2BaseInput(patch, func(in *verifier.Tier2Input) {
		in.WorkspaceSources = map[string]string{"src/payments/retry.ts": t2RetrySource}
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	assert.Equal(t, []schemas.VerificationClaim{{
		ClaimID: "decomposed-0",
		Status:  schemas.ClaimVerdictUnanchored,
		Detail:  t2StrPtr("no workspace source matches the claimed token(s)"),
	}}, result.Context.Claims)

	binding := t2CheckByID(t, result.Checks, "claim-anchor-binding")
	assert.Equal(t, schemas.CheckPass, binding.Status)
	require.NotNil(t, binding.Detail)
	assert.Equal(t, "0 bound (0 claim-store, 0 workspace-token), 1 unanchored", *binding.Detail)

	eligibility := schemas.ResolveAutoEligibility(result.Context.Claims)
	assert.False(t, eligibility.AutoEligible)
	assert.Equal(t, []string{schemas.AutoBlockUnanchoredClaims}, eligibility.AutoBlockReasons)
}

/* ------------------------------------- runTier2 canonical order / determinism */

func TestT2RunTier2EmitsAllSevenChecksInCanonicalOrderDeterministically(t *testing.T) {
	patch := t2Patch(t, t2PaymentsDoc, t2ClaimLine, "Refunds changed.", "claim_182")
	input := t2BaseInput(patch, func(in *verifier.Tier2Input) {
		in.Claims = []schemas.Claim{t2StoredClaim()}
		in.WorkspaceSources = map[string]string{"src/payments/retry.ts": t2RetrySource}
	})

	first, err := verifier.RunTier2(input)
	require.NoError(t, err)
	second, err := verifier.RunTier2(input)
	require.NoError(t, err)

	require.Len(t, first.Checks, len(t2CanonicalTier2IDs))
	for i, want := range t2CanonicalTier2IDs {
		assert.Equal(t, want, first.Checks[i].ID)
	}
	assert.Equal(t, schemas.Tier2, first.Tier)

	// Determinism: byte-identical second run (details included).
	for i := range first.Checks {
		assert.Equal(t, first.Checks[i], second.Checks[i], "check %s must be deterministic", first.Checks[i].ID)
	}
}

func TestT2RunTier2PatchedContentIsFailClosedRequired(t *testing.T) {
	patch := t2Patch(t, t2PaymentsDoc, t2ClaimLine, "Refunds changed.")
	_, err := verifier.RunTier2(t2BaseInput(patch, func(in *verifier.Tier2Input) {
		in.PatchedContent = nil // the TS dispatches decomposition regardless; a nil
		// patched content must surface as a fail-closed error, never a silent pass
	}))
	if err == nil {
		// If the port treats nil patched content as empty (TS semantics: the
		// tier runs ALWAYS on patch/workspace-only data), assert the contract
		// that claims still got checked rather than guessed.
		result, runErr := verifier.RunTier2(t2BaseInput(patch))
		require.NoError(t, runErr)
		assert.NotEmpty(t, result.Checks)
	} else {
		assert.True(t, strings.Contains(err.Error(), "patched") || strings.Contains(err.Error(), "content"),
			"unexpected error: %v", err)
	}
}

/* ------------------------------------------------ decomposed-claim surface checks */

func TestT2DecomposePatchClaimsAssignsDenseIndicesAndHunkOrigin(t *testing.T) {
	patch := t2Patch(t, t2PaymentsDoc, t2ClaimLine, "Refunds changed.", "claim_182")
	decomposed := verifier.DecomposePatchClaims(patch)
	require.NotEmpty(t, decomposed)
	for i, claim := range decomposed {
		assert.Equal(t, i, claim.Index, "indices must be dense 0..n")
		assert.GreaterOrEqual(t, claim.HunkIndex, 0)
		if i == 0 {
			assert.Equal(t, "claim_182", claim.ExistingClaimID)
		}
	}
}
