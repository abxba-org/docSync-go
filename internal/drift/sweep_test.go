package drift_test

// Black-box port of packages/core/test/drift-sweep.test.ts (P11-T17;
// ARCH §4.3; CTX §11; V-P11-7): the headline query returns exactly the
// expected lost-support claims on a fixture with known drift; the sweep is
// report-only by default; state flips happen ONLY on the explicit flag, ONLY
// through the claim state machine's CAS transitions, and every flip/conflict
// is recorded. Real system git in a deterministic temp fixture
// (createDriftFixture, differential_test.go); zero LLM.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/drift"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------ claim store */

// newClaimStore builds a claim store over a REAL state store in a temp dir
// (the TS suite used the CAS-parity InMemoryStateStore fake; the real store
// exercises the same registry-validated, CAS-guarded write path, so the
// conflict shapes asserted below come from the production machinery).
func newClaimStore(t *testing.T) *claims.ClaimStore {
	t.Helper()
	store, err := state.NewStateStore(state.StateStoreOptions{
		StateDir: filepath.Join(t.TempDir(), schemas.DocSyncDir, "state"),
	})
	require.NoError(t, err)
	return claims.NewClaimStore(store)
}

func claimStatus(t *testing.T, store *claims.ClaimStore, claimID string) string {
	t.Helper()
	claim, err := store.Get(claimID)
	require.NoError(t, err)
	require.NotNil(t, claim)
	return claim.Status
}

func claimHash(t *testing.T, store *claims.ClaimStore, claimID string) *string {
	t.Helper()
	hash, err := store.HashOf(claimID)
	require.NoError(t, err)
	require.NotNil(t, hash)
	return hash
}

func entryIDs(entries []schemas.ClaimSupportEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ClaimID)
	}
	return ids
}

func entriesWithVerdict(report *schemas.ClaimSupportReport, verdict schemas.ClaimSupportVerdict) []schemas.ClaimSupportEntry {
	out := make([]schemas.ClaimSupportEntry, 0, len(report.Claims))
	for _, entry := range report.Claims {
		if entry.Verdict == verdict {
			out = append(out, entry)
		}
	}
	return out
}

func sweepOf(t *testing.T, input drift.ClaimSupportSweepInput) *schemas.ClaimSupportReport {
	t.Helper()
	report, err := drift.RunClaimSupportSweep(t.Context(), input)
	require.NoError(t, err)
	return report
}

/* ------------------------------------------------------------------ tests */

func TestVP11_7_SweepReturnsExactlyTheLostSupportClaims(t *testing.T) {
	fixture := createDriftFixture(t)
	report := sweepOf(t, drift.ClaimSupportSweepInput{
		Claims: fixture.claims,
		Git:    fixture.client(t),
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})

	// The headline answer — exact set equality: exactly ONE claim (A) lost
	// support; B and C are supported. Zero LLM: the sweep input surface has
	// no model port — every verdict below is a deterministic git
	// blob-identity/anchor fact from the closed verdict vocabulary.
	lost := entriesWithVerdict(report, schemas.ClaimSupportLostSupport)
	require.Equal(t, []string{claimAID}, entryIDs(lost))
	require.Equal(t, []string{claimBID, claimCID},
		entryIDs(entriesWithVerdict(report, schemas.ClaimSupportSupported)))
	require.Equal(t, schemas.ClaimSupportSummary{
		Total: 3, Supported: 2, LostSupport: 1, UnresolvableAnchor: 0, Flipped: 0,
	}, report.Summary)

	// Evidence: claim id, anchor, commits, reason — deterministic templates.
	claimA := lost[0]
	require.Equal(t, schemas.ClaimStatusVerified, claimA.StatusBefore)
	require.Equal(t, fmt.Sprintf(
		"source blob moved at head (recorded %s, head %s) — the recorded backing bytes no longer exist",
		claimA.Evidence.SourceBlobSHA, *claimA.Evidence.SourceBlobSHAAtHead), claimA.Reason)
	require.Equal(t, docPath, claimA.Evidence.AnchorFile)
	require.Equal(t, schemas.ClaimSupportAnchorUnchanged, claimA.Evidence.AnchorResolution)
	require.Equal(t, refundPath, claimA.Evidence.SourceFile)
	require.Equal(t, []string{fixture.headSha}, claimA.Evidence.Commits)
	for _, supported := range entriesWithVerdict(report, schemas.ClaimSupportSupported) {
		require.Equal(t, "source blob identity intact at head", supported.Reason)
		require.Empty(t, supported.Evidence.Commits)
	}

	// Report-only default: nothing was written anywhere.
	require.False(t, report.Applied)
	require.Empty(t, report.Flips)
	require.Empty(t, report.Conflicts)
}

func TestP11T17_ReportOnlyByDefault_ApplyFlipsViaCASAndRecordsOutcomes(t *testing.T) {
	fixture := createDriftFixture(t)
	claimStore := newClaimStore(t)

	// Claims in the store: A verified (lost support at head), C pre-marked
	// suspect (lost support but NOT newly — must never flip again).
	claimCPreSuspect := fixture.claims[2]
	claimCPreSuspect.Status = schemas.ClaimStatusSuspect
	for _, claim := range []schemas.Claim{fixture.claims[0], claimCPreSuspect} {
		write, err := claimStore.Upsert(claim, &claims.ClaimUpsertOptions{})
		require.NoError(t, err)
		require.True(t, write.OK)
	}
	hashA := claimHash(t, claimStore, claimAID)

	baseInput := drift.ClaimSupportSweepInput{
		Claims: []schemas.Claim{fixture.claims[0], claimCPreSuspect},
		Git:    fixture.client(t),
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	}

	// 1. REPORT-ONLY by default: no store is even passed — zero writes.
	reportOnly := sweepOf(t, baseInput)
	require.False(t, reportOnly.Applied)
	require.Empty(t, reportOnly.Flips)
	require.Empty(t, reportOnly.Conflicts)
	require.Equal(t, schemas.ClaimStatusVerified, claimStatus(t, claimStore, claimAID))

	// 2. Stale CAS expectation → the machine's conflict shape, NOT a crash:
	//    the failed transition surfaces in the report, the claim is untouched.
	staleHash := "0000000000000000000000000000000000000000deadbeef"
	applyInput := baseInput
	applyInput.Apply = true
	applyInput.Store = claimStore
	applyInput.ExpectedHashByClaim = map[string]string{claimAID: staleHash}
	conflicted := sweepOf(t, applyInput)
	require.True(t, conflicted.Applied)
	require.Equal(t, []schemas.ClaimSupportConflict{{
		ClaimID:      claimAID,
		Transition:   schemas.ClaimSupportTransitionLoseSupport,
		ExpectedHash: strptr(staleHash),
		ActualHash:   hashA,
	}}, conflicted.Conflicts)
	require.Empty(t, conflicted.Flips)
	require.Equal(t, schemas.ClaimStatusVerified, claimStatus(t, claimStore, claimAID))

	// 3. Apply with a FRESH expectation (transition reads current bytes):
	//    the flip goes through `loseSupport` (CAS + history event) and is
	//    recorded; the pre-suspect claim C never flips again.
	freshInput := baseInput
	freshInput.Apply = true
	freshInput.Store = claimStore
	applied := sweepOf(t, freshInput)
	require.Len(t, applied.Flips, 1)
	flip := applied.Flips[0]
	require.Equal(t, claimAID, flip.ClaimID)
	require.Equal(t, schemas.ClaimStatusVerified, flip.From)
	require.Equal(t, schemas.ClaimStatusSuspect, flip.To)
	require.Equal(t, schemas.ClaimSupportTransitionLoseSupport, flip.Transition)
	require.True(t, strings.HasPrefix(flip.Event, "support lost: claim-support sweep at head "))
	require.Equal(t, 1, applied.Summary.Flipped)
	require.Equal(t, schemas.ClaimStatusSuspect, claimStatus(t, claimStore, claimAID))
	require.Equal(t, schemas.ClaimStatusSuspect, claimStatus(t, claimStore, claimCID))
	require.Empty(t, applied.Conflicts)
}

func TestP11T17_UnresolvableAnchorFlipsViaMarkUnanchored(t *testing.T) {
	fixture := createDriftFixture(t)
	// Edit claim A's doc span (HEAD is already at commit 2): the content
	// hash matches nowhere at head → `unresolvable-anchor`
	// (content-hash-first, ARCH §4.3).
	docFile := filepath.Join(fixture.root, docPath)
	content, err := os.ReadFile(docFile)
	require.NoError(t, err)
	edited := strings.Replace(string(content), spanRefunds,
		"Refunds require an idempotency KEY and a customer reference.", 1)
	require.NotEqual(t, string(content), edited)
	require.NoError(t, os.WriteFile(docFile, []byte(edited), 0o644))
	fixture.gitIdentity(t, "commit", "-aq", "-m", "doc edit")
	headSha := fixture.revParseHead(t)

	claimStore := newClaimStore(t)
	claimA := fixture.claims[0]
	write, err := claimStore.Upsert(claimA, &claims.ClaimUpsertOptions{})
	require.NoError(t, err)
	require.True(t, write.OK)

	report := sweepOf(t, drift.ClaimSupportSweepInput{
		Claims: []schemas.Claim{claimA},
		Git:    fixture.client(t),
		Head:   headSha,
		RunID:  driftRunID,
		At:     driftAt,
		Apply:  true,
		Store:  claimStore,
	})

	entry := report.Claims[0]
	require.Equal(t, schemas.ClaimSupportUnresolvableAnchor, entry.Verdict)
	require.Equal(t, schemas.ClaimSupportAnchorUnanchored, entry.Evidence.AnchorResolution)
	require.Equal(t, docPath, entry.Evidence.AnchorFile)
	require.Equal(t, 1, report.Summary.UnresolvableAnchor)
	require.Len(t, report.Flips, 1)
	flip := report.Flips[0]
	require.Equal(t, claimAID, flip.ClaimID)
	require.Equal(t, schemas.ClaimStatusVerified, flip.From)
	require.Equal(t, schemas.ClaimStatusUnanchored, flip.To)
	require.Equal(t, schemas.ClaimSupportTransitionMarkUnanchored, flip.Transition)
	require.NotEmpty(t, flip.Event)
	require.NotEmpty(t, flip.Hash)
	require.True(t, strings.HasPrefix(flip.Event, "unanchored: claim-support sweep at head "))
	require.Equal(t, schemas.ClaimStatusUnanchored, claimStatus(t, claimStore, claimAID))
}

func TestP11T17_MissingSourceAtHeadIsLostSupportWithCommitEvidence(t *testing.T) {
	fixture := createDriftFixture(t)
	// Delete claim A's recorded source after the baseline: the recorded
	// backing bytes are gone (ARCH §4.3 direct-invalidation posture).
	fixture.git(t, "rm", "-q", refundPath)
	fixture.gitIdentity(t, "commit", "-q", "-m", "delete recorded source")
	deletingSha := fixture.revParseHead(t)

	report := sweepOf(t, drift.ClaimSupportSweepInput{
		Claims: []schemas.Claim{fixture.claims[0]},
		Git:    fixture.client(t),
		Head:   deletingSha,
		RunID:  driftRunID,
		At:     driftAt,
	})

	require.Len(t, report.Claims, 1)
	entry := report.Claims[0]
	require.Equal(t, schemas.ClaimSupportLostSupport, entry.Verdict)
	require.Equal(t, fmt.Sprintf(`source file "%s" is missing at head — the recorded backing source is gone`, refundPath),
		entry.Reason)
	require.Nil(t, entry.Evidence.SourceBlobSHAAtHead)
	// The anchor still resolves (the doc survived): only the source is gone.
	require.Equal(t, schemas.ClaimSupportAnchorUnchanged, entry.Evidence.AnchorResolution)
	// Commit evidence is the bounded walk for the deleted path: the deleting
	// commit plus the drift commit that touched refund.ts (topo order).
	require.Equal(t, []string{deletingSha, fixture.headSha}, entry.Evidence.Commits)
	require.Equal(t, schemas.ClaimSupportSummary{
		Total: 1, Supported: 0, LostSupport: 1, UnresolvableAnchor: 0, Flipped: 0,
	}, report.Summary)
}

func TestP11T17_MissingAnchorDocAtHeadIsUnresolvable(t *testing.T) {
	fixture := createDriftFixture(t)
	// Delete the anchor doc after the baseline: the anchor cannot be resolved
	// at head (a DECLARED absence, not an error — invariant 13).
	fixture.git(t, "rm", "-q", docPath)
	fixture.gitIdentity(t, "commit", "-q", "-m", "delete anchor doc")
	headSha := fixture.revParseHead(t)

	report := sweepOf(t, drift.ClaimSupportSweepInput{
		Claims: []schemas.Claim{fixture.claims[0]},
		Git:    fixture.client(t),
		Head:   headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})

	require.Len(t, report.Claims, 1)
	entry := report.Claims[0]
	require.Equal(t, schemas.ClaimSupportUnresolvableAnchor, entry.Verdict)
	require.Equal(t, fmt.Sprintf(`anchor doc "%s" is not readable at head — anchor cannot be resolved`, docPath),
		entry.Reason)
	require.Equal(t, schemas.ClaimSupportAnchorUnanchored, entry.Evidence.AnchorResolution)
	require.Equal(t, docPath, entry.Evidence.AnchorFile)
	// The source blob itself is unchanged at head (its path is still readable);
	// the drift commit already moved it, which the missing doc masks here.
	require.NotNil(t, entry.Evidence.SourceBlobSHAAtHead)
	// Commit evidence is computed only for lost-support claims.
	require.Empty(t, entry.Evidence.Commits)
	require.Equal(t, schemas.ClaimSupportSummary{
		Total: 1, Supported: 0, LostSupport: 0, UnresolvableAnchor: 1, Flipped: 0,
	}, report.Summary)
}

func TestP11T17_ApplyTrueWithoutAStoreIsRefused(t *testing.T) {
	fixture := createDriftFixture(t)
	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: fixture.claims,
		Git:    fixture.client(t),
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
		Apply:  true,
	})
	require.EqualError(t, err,
		"EDOCSYNC_STATE_KEY: claim-support sweep: apply: true requires a claim store — state flips ONLY through the claim state machine, never a direct write")
	require.Equal(t, docutil.CodeStateKey, docutil.CodeOf(err))

	// A nil git client is refused the same way, before any git runs.
	_, err = drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: fixture.claims,
		Git:    nil,
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})
	require.EqualError(t, err,
		"EDOCSYNC_INTERNAL: runClaimSupportSweep: git client is nil — refusing to sweep without git (fail closed)")
}

func TestClampCommitWalkDepth_DefaultsAndBounds(t *testing.T) {
	require.Equal(t, drift.DefaultCommitWalkDepth, drift.ClampCommitWalkDepth(nil))
	require.Equal(t, drift.DefaultCommitWalkDepth, drift.ClampCommitWalkDepth(intptr(drift.DefaultCommitWalkDepth)))
	require.Equal(t, 1, drift.ClampCommitWalkDepth(intptr(0)))
	require.Equal(t, 1, drift.ClampCommitWalkDepth(intptr(-7)))
	require.Equal(t, 7, drift.ClampCommitWalkDepth(intptr(7)))
	require.Equal(t, drift.MaxCommitWalkDepth, drift.ClampCommitWalkDepth(intptr(5000)))
	require.Equal(t, drift.MaxCommitWalkDepth, drift.ClampCommitWalkDepth(intptr(drift.MaxCommitWalkDepth)))
}
