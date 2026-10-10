package patch_test

// Black-box port of test/patch-claims-export.test.ts (P3-T13; R16 cross-patch
// consistency): hunks with claim_ids export (claim_id, doc_path, hunk_index,
// value = inserted_text, evidence kinds); grouping by claim_id detects two
// patches asserting DIFFERENT values for the same claim id; single-value groups
// are consistent and never reported; ordering is deterministic.

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func TestExportPatchClaimValues(t *testing.T) {
	t.Run("exports one record per (hunk, claim_id) with the inserted value and evidence kinds", func(t *testing.T) {
		p := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{
					start:      3,
					deleteText: "Refunds require an idempotency key.",
					insert:     "Refunds require an idempotency key above 10 USD.",
					claimIDs:   []string{"claim_182"},
				}),
				hunk(hunkOpts{
					start:      4,
					deleteText: "Amounts above 10 USD are held for review.",
					insert:     "Amounts above 25 USD are held for review.",
					claimIDs:   []string{"claim_182", "claim_183"},
				}),
			},
		})
		require.Equal(t, []patch.ExportedClaimValue{
			{ClaimID: "claim_182", DocPath: "docs/payments.md", HunkIndex: 0, Value: "Refunds require an idempotency key above 10 USD.", EvidenceRefKinds: []string{"symbol_diff"}},
			{ClaimID: "claim_182", DocPath: "docs/payments.md", HunkIndex: 1, Value: "Amounts above 25 USD are held for review.", EvidenceRefKinds: []string{"symbol_diff"}},
			{ClaimID: "claim_183", DocPath: "docs/payments.md", HunkIndex: 1, Value: "Amounts above 25 USD are held for review.", EvidenceRefKinds: []string{"symbol_diff"}},
		}, patch.ExportPatchClaimValues(p))
	})

	t.Run("hunks without claim_ids export nothing; deletion-only hunks export an empty value", func(t *testing.T) {
		p := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{
					start:      3,
					deleteText: "Refunds require an idempotency key.",
					insert:     "Replacement.",
				}),
				hunk(hunkOpts{
					start:      4,
					deleteText: "Amounts above 10 USD are held for review.",
					claimIDs:   []string{"claim_184"},
				}),
			},
		})
		require.Equal(t, []patch.ExportedClaimValue{
			{ClaimID: "claim_184", DocPath: "docs/payments.md", HunkIndex: 1, Value: "", EvidenceRefKinds: []string{"symbol_diff"}},
		}, patch.ExportPatchClaimValues(p))
	})
}

func TestDetectClaimValueConflicts(t *testing.T) {
	t.Run("detects same claim_id, different values (across docs) and groups by value deterministically", func(t *testing.T) {
		exports := []patch.ExportedClaimValue{
			{ClaimID: "claim_182", DocPath: "docs/payments.md", HunkIndex: 0, Value: "Refunds require an idempotency key above 10 USD.", EvidenceRefKinds: []string{"symbol_diff"}},
			{ClaimID: "claim_182", DocPath: "docs/refunds.md", HunkIndex: 2, Value: "Refunds require an idempotency key above 25 USD.", EvidenceRefKinds: []string{"doc_anchor"}},
			{ClaimID: "claim_182", DocPath: "docs/architecture.md", HunkIndex: 1, Value: "Refunds require an idempotency key above 10 USD.", EvidenceRefKinds: []string{"symbol_diff"}},
		}
		require.Equal(t, []patch.ClaimValueConflict{
			{
				ClaimID: "claim_182",
				Conflicts: [][]patch.ClaimValueRecord{
					{
						{DocPath: "docs/architecture.md", Value: "Refunds require an idempotency key above 10 USD."},
						{DocPath: "docs/payments.md", Value: "Refunds require an idempotency key above 10 USD."},
					},
					{
						{DocPath: "docs/refunds.md", Value: "Refunds require an idempotency key above 25 USD."},
					},
				},
			},
		}, patch.DetectClaimValueConflicts(exports))
	})

	t.Run("single-value groups are consistent → no conflict; independent claims sort by id", func(t *testing.T) {
		exports := []patch.ExportedClaimValue{
			{ClaimID: "claim_200", DocPath: "docs/b.md", HunkIndex: 0, Value: "same", EvidenceRefKinds: []string{"other"}},
			{ClaimID: "claim_100", DocPath: "docs/a.md", HunkIndex: 3, Value: "also same", EvidenceRefKinds: []string{"other"}},
			{ClaimID: "claim_100", DocPath: "docs/c.md", HunkIndex: 1, Value: "also same", EvidenceRefKinds: []string{"other"}},
		}
		require.Empty(t, patch.DetectClaimValueConflicts(exports))
	})

	t.Run("empty exports → no conflicts (pure, no side effects)", func(t *testing.T) {
		require.Empty(t, patch.DetectClaimValueConflicts(nil))
	})
}
