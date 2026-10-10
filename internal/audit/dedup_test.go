// Black-box port of the dedup assertions in packages/core/test/
// audit-loop-b.test.ts, audit-store-artifacts.test.ts and
// org-drift-report-p12-t15.test.ts (P11-T19/P11-T20/P12-T15; ARCH §7.2
// "findings deduplicated by claim ID so nightly runs don't spam").
//
// Pins the pinned tie-break rule: a claim-ID finding dedupes on
// `claim:<claim_id>` (the identity regardless of document), a claimless
// finding on `doc:<doc_path>|kind:<kind>`; the LAST occurrence in scan order
// wins the scalar body while evidence commits/paths merge as sorted unions.

package audit_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/stretchr/testify/require"
)

// loopBFinding is the deterministic finding factory of the Loop B suites
// (TS `finding(claimId, docPath, commit?)`): pinned dates, claim-scoped
// evidence, confidence 0.9.
func loopBFinding(claimID, docPath, commit string) audit.DriftFinding {
	finding := audit.DriftFinding{
		ID:      "f-" + claimID + "-" + docPath,
		DocPath: docPath,
		Kind:    "claim-lost-support",
		Evidence: audit.DriftFindingEvidence{
			Commits: []string{commit},
			Paths:   []string{"src/x.ts"},
			Detail:  claimID + " lost support (audited via " + docPath + ")",
		},
		Confidence: floatPtr(0.9),
		CreatedAt:  auditNow,
	}
	if claimID != "" {
		id := claimID
		finding.ClaimID = &id
	}
	return finding
}

func TestP11T19_DedupeCollapsesClaimOccurrencesWithUnionedEvidence(t *testing.T) {
	// Same claim reported from two docs collapses to ONE finding whose
	// evidence commits merge (union) — the pinned tie-break rule.
	merged := audit.DedupeFindings([]audit.DriftFinding{
		loopBFinding("c1", "docs/a.md", "commit-1"),
		loopBFinding("c1", "docs/b.md", "commit-2"),
	})
	require.Len(t, merged, 1)
	require.Equal(t, []string{"commit-1", "commit-2"}, merged[0].Evidence.Commits)
	require.Equal(t, []string{"src/x.ts"}, merged[0].Evidence.Paths)
}

func TestP11T19_DedupeIdentityIsClaimFirstThenDocKind(t *testing.T) {
	withClaim := loopBFinding("claim_1", "docs/a.md", "c1")
	require.Equal(t, "claim:claim_1", audit.FindingDedupKey(withClaim))

	claimless := loopBFinding("", "docs/a.md", "c1")
	claimless.Kind = "doc-drift"
	require.Equal(t, "doc:docs/a.md|kind:doc-drift", audit.FindingDedupKey(claimless))

	// A claim identity dedupes across documents (same key, different docs).
	require.Equal(t,
		audit.FindingDedupKey(loopBFinding("claim_1", "docs/a.md", "c1")),
		audit.FindingDedupKey(loopBFinding("claim_1", "docs/b.md", "c2")))

	// A claimless identity does NOT dedupe across kinds.
	other := loopBFinding("", "docs/a.md", "c1")
	other.Kind = "symbol-absent"
	require.NotEqual(t,
		audit.FindingDedupKey(claimless), audit.FindingDedupKey(other))
}

func TestP11T19_DedupeIsLatestWinsBodyWithMergedEvidence(t *testing.T) {
	first := loopBFinding("claim_9", "docs/a.md", "c1")
	second := loopBFinding("claim_9", "docs/b.md", "c2")
	second.ID = "f-claim_9-docs/b.md"
	second.Evidence.Detail = "claim_9 lost support (re-detected)"
	second.Evidence.Paths = []string{"src/y.ts"}
	confidence := 0.5
	second.Confidence = &confidence

	merged := audit.DedupeFindings([]audit.DriftFinding{first, second})
	require.Len(t, merged, 1)
	// Latest-wins scalars.
	require.Equal(t, "f-claim_9-docs/b.md", merged[0].ID)
	require.Equal(t, "docs/b.md", merged[0].DocPath)
	require.Equal(t, "claim_9 lost support (re-detected)", merged[0].Evidence.Detail)
	require.NotNil(t, merged[0].Confidence)
	require.InDelta(t, 0.5, *merged[0].Confidence, 1e-9)
	// Merged evidence unions (sorted, de-duplicated).
	require.Equal(t, []string{"c1", "c2"}, merged[0].Evidence.Commits)
	require.Equal(t, []string{"src/x.ts", "src/y.ts"}, merged[0].Evidence.Paths)
}

func TestP11T19_DedupeSortsByDocPathKindClaimID(t *testing.T) {
	claim190 := loopBFinding("claim_190", "docs/api.md", "c2")
	claimless := loopBFinding("", "docs/legacy.md", "c3")
	claimless.Kind = "doc-drift"
	claim182 := loopBFinding("claim_182", "docs/payments.md", "c1")

	view := audit.DedupeFindings([]audit.DriftFinding{claim182, claimless, claim190})
	require.Len(t, view, 3)
	requireClaimOrder(t, view, []*string{claim190.ClaimID, claimless.ClaimID, claim182.ClaimID})
}

func TestP11T19_DedupeIsDeterministicUnderInputOrder(t *testing.T) {
	a := loopBFinding("claim_1", "docs/a.md", "c1")
	b := loopBFinding("claim_2", "docs/b.md", "c2")
	c := loopBFinding("", "docs/c.md", "c3")
	require.Equal(t,
		audit.DedupeFindings([]audit.DriftFinding{a, b, c}),
		audit.DedupeFindings([]audit.DriftFinding{c, b, a}))
}

func TestP12T15_FindingsAreDeduplicatedBeforeAggregation(t *testing.T) {
	// The org-report suite's claim-id dedup pin: two occurrences of one claim
	// over the same doc collapse to a single finding.
	first := audit.DriftFinding{
		ID:      "f-1",
		DocPath: "docs/a.md",
		Kind:    "support-moved",
		Evidence: audit.DriftFindingEvidence{
			Commits: []string{"81af92e"}, Paths: []string{"src/x.ts"}, Detail: "fixture evidence",
		},
		CreatedAt: "2026-10-08T03:00:00Z",
	}
	claim := "claim_9"
	first.ClaimID = &claim
	second := first
	second.ID = "f-2"

	view := audit.DedupeFindings([]audit.DriftFinding{first, second})
	require.Len(t, view, 1)
}

func TestP11T19_DedupeSortsByDocPathThenKind(t *testing.T) {
	// The engine's per-port schema-check assertion: the deduplicated view
	// orders by doc_path, then kind (docs/a.md/claim-lost-support before
	// docs/a.md/doc-drift).
	f1 := loopBFinding("claim_1", "docs/a.md", "c1")
	f2 := loopBFinding("claim_2", "docs/a.md", "c2")
	f2.Kind = "doc-drift"
	f3 := loopBFinding("claim_3", "docs/b.md", "c3")

	view := audit.DedupeFindings([]audit.DriftFinding{f3, f1, f2})
	require.Len(t, view, 3)
	require.Equal(t, []string{"docs/a.md", "docs/a.md", "docs/b.md"}, []string{
		view[0].DocPath, view[1].DocPath, view[2].DocPath})
	require.Equal(t, []string{"claim-lost-support", "doc-drift", "claim-lost-support"}, []string{
		view[0].Kind, view[1].Kind, view[2].Kind})
}

// requireClaimOrder asserts the claim ids (nil for claimless findings) in
// order.
func requireClaimOrder(t *testing.T, findings []audit.DriftFinding, want []*string) {
	t.Helper()
	got := make([]*string, 0, len(findings))
	for _, finding := range findings {
		got = append(got, finding.ClaimID)
	}
	require.Equal(t, want, got)
}
