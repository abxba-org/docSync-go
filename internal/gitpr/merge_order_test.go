package gitpr_test

// Black-box port of packages/core/test/gitpr-merge-order.test.ts (P6-T16/T17/
// T18): the per-source-key merge-order ledger (CAS upsert with the R15
// loser-re-runs contract; the closed transition table), the merge-target
// re-check decision (P6-T16, D12 kickoff) and the post-merge re-run gate
// primitive (P6-T17; webhook wiring is Phase 8, OD-R19).
//
// No network, no wall clock (P19): the TS temp-dir StateStore fixtures project
// onto the keyed in-memory double (internal/testkit — same CAS/registry/guard
// contract, no fs) and the clock is a constant.

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gpMergeSourceKey is the SOURCE_KEY of the TS suite.
const gpMergeSourceKey = "source-pr-42"

// gpClock / gpLaterClock are the two constant clocks of the TS suite.
func gpClock() string      { return "2026-10-07T00:00:00.000Z" }
func gpLaterClock() string { return "2026-10-07T00:30:00.000Z" }

// gpLedgerFixture is the TS makeFixture: a fresh store plus the merge-ledger
// store over it.
func gpLedgerFixture(t *testing.T) (*testkit.InMemoryStateStore, gitpr.MergeLedgerStore) {
	t.Helper()
	store := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	ledger, err := gitpr.NewMergeLedgerStore(gitpr.MergeLedgerStoreDeps{Store: store})
	require.NoError(t, err)
	return store, ledger
}

// gpLedgerEntryNumbers renders the doc-PR numbers of a ledger (the TS
// `entries.map((entry) => entry.doc_pr_number)`).
func gpLedgerEntryNumbers(ledger *schemas.MergeLedger) []int {
	numbers := make([]int, 0, len(ledger.Entries))
	for _, entry := range ledger.Entries {
		numbers = append(numbers, entry.DocPRNumber)
	}
	return numbers
}

// gpTriggerPtr is the optional-trigger pointer helper.
func gpTriggerPtr(trigger gitpr.MergeTargetTrigger) *gitpr.MergeTargetTrigger { return &trigger }

func TestP6T18_LedgerCasLoserReRuns(t *testing.T) {
	store, ledger := gpLedgerFixture(t)

	// First record: fresh internal read sees nothing → create, ok.
	first, err := ledger.Record(gpMergeSourceKey, gitpr.RecordLedgerEntryInput{
		DocPRNumber:    84,
		State:          schemas.MergeLedgerStateOpen,
		IdempotencyKey: "docsync-acme-84",
		Detail:         "doc PR opened",
	}, gpClock, nil)
	require.NoError(t, err)
	require.True(t, first.OK)

	// A concurrent winner advances the file behind the loser's back (the
	// store's own CAS-correct write stands in for the other process).
	winnerLedger := schemas.MergeLedgerExample()
	winnerLedger.SourceKey = gpMergeSourceKey
	winnerLedger.Entries = []schemas.LedgerEntry{{
		DocPRNumber:    83,
		State:          schemas.MergeLedgerStateOpen,
		IdempotencyKey: "docsync-acme-83",
		At:             gpLaterClock(),
		Detail:         "a concurrent run opened doc PR 83 first",
	}}
	winner, err := store.WriteMergeLedger(gpMergeSourceKey, winnerLedger, &first.Hash)
	require.NoError(t, err)
	require.True(t, winner.OK)

	// Second record asserting the now-STALE expected hash: the CAS loser
	// gets ok:false cas-conflict — it must re-run, never overwrite (R15).
	loser, err := ledger.Record(gpMergeSourceKey, gitpr.RecordLedgerEntryInput{
		DocPRNumber:    85,
		State:          schemas.MergeLedgerStateOpen,
		IdempotencyKey: "docsync-acme-85",
		Detail:         "loser still believes its read is current",
	}, gpClock, &gitpr.RecordLedgerOptions{ExpectedHash: &first.Hash})
	require.NoError(t, err)
	assert.False(t, loser.OK)
	assert.Contains(t, loser.Detail, "re-run record() against a fresh read")
	assert.Contains(t, loser.Detail, "R15")

	// The winner's bytes are intact — the loser wrote nothing.
	afterLoser, err := ledger.Load(gpMergeSourceKey)
	require.NoError(t, err)
	require.NotNil(t, afterLoser)
	assert.Equal(t, []int{83}, gpLedgerEntryNumbers(afterLoser))

	// Fresh read (no stale assertion): the re-run succeeds and merges the
	// entry into the winner's ledger.
	rerun, err := ledger.Record(gpMergeSourceKey, gitpr.RecordLedgerEntryInput{
		DocPRNumber:    85,
		State:          schemas.MergeLedgerStateOpen,
		IdempotencyKey: "docsync-acme-85",
		Detail:         "re-run after the CAS conflict",
	}, gpClock, nil)
	require.NoError(t, err)
	require.True(t, rerun.OK)
	finalLedger, err := ledger.Load(gpMergeSourceKey)
	require.NoError(t, err)
	require.NotNil(t, finalLedger)
	assert.Equal(t, gpMergeSourceKey, finalLedger.SourceKey)
	assert.Equal(t, []int{83, 85}, gpLedgerEntryNumbers(finalLedger))
	assert.Equal(t, gpClock(), finalLedger.UpdatedAt)
}

func TestP6T18_LedgerTransitionsAreClosed(t *testing.T) {
	// Legal edges (the green path and its re-runs).
	legal := [][2]schemas.MergeLedgerState{
		// Legal chain (green path): open → superseded → merged.
		{schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateSuperseded},
		{schemas.MergeLedgerStateSuperseded, schemas.MergeLedgerStateMerged},
		// Conflicted can re-open (a successful re-run) or resolve onward.
		{schemas.MergeLedgerStateConflicted, schemas.MergeLedgerStateOpen},
		{schemas.MergeLedgerStateSuperseded, schemas.MergeLedgerStateDropped},
		{schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateConflicted},
		{schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateDropped},
		{schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateMerged},
		{schemas.MergeLedgerStateConflicted, schemas.MergeLedgerStateSuperseded},
		{schemas.MergeLedgerStateConflicted, schemas.MergeLedgerStateDropped},
	}
	for _, edge := range legal {
		got, err := gitpr.LedgerTransition(edge[0], edge[1])
		require.NoError(t, err, "%s → %s must be legal", edge[0], edge[1])
		assert.Equal(t, edge[1], got)
	}

	// Terminal states refuse everything; unlisted edges and self-transitions
	// are illegal; unknown states fail closed (the union is closed).
	illegal := [][2]schemas.MergeLedgerState{
		{schemas.MergeLedgerStateMerged, schemas.MergeLedgerStateOpen},
		{schemas.MergeLedgerStateMerged, schemas.MergeLedgerStateDropped},
		{schemas.MergeLedgerStateDropped, schemas.MergeLedgerStateOpen},
		{schemas.MergeLedgerStateDropped, schemas.MergeLedgerStateMerged},
		{schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateOpen},
		{schemas.MergeLedgerStateSuperseded, schemas.MergeLedgerStateConflicted},
		{schemas.MergeLedgerState("unknown"), schemas.MergeLedgerStateOpen},
		{schemas.MergeLedgerStateOpen, schemas.MergeLedgerState("unknown")},
	}
	for _, edge := range illegal {
		_, err := gitpr.LedgerTransition(edge[0], edge[1])
		require.Error(t, err, "%s → %s must be illegal", edge[0], edge[1])
		assert.Equal(t, gitpr.CodeMergeLedgerTransition, docutil.CodeOf(err))
	}

	// The store factory exposes the same pure table.
	_, ledger := gpLedgerFixture(t)
	got, err := ledger.Transition(schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateMerged)
	require.NoError(t, err)
	assert.Equal(t, schemas.MergeLedgerStateMerged, got)
	_, err = ledger.Transition(schemas.MergeLedgerStateMerged, schemas.MergeLedgerStateOpen)
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeMergeLedgerTransition, docutil.CodeOf(err))
}

func TestP6T18_LedgerRecordFailsClosedOnDrift(t *testing.T) {
	_, ledger := gpLedgerFixture(t)
	entry := gitpr.RecordLedgerEntryInput{
		DocPRNumber:    84,
		State:          schemas.MergeLedgerStateOpen,
		IdempotencyKey: "k",
		Detail:         "d",
	}

	// Malformed entries are refused before any write (typed, fail loud).
	zeroNumber := entry
	zeroNumber.DocPRNumber = 0
	_, err := ledger.Record(gpMergeSourceKey, zeroNumber, gpClock, nil)
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeMergeLedger, docutil.CodeOf(err))

	zombieState := entry
	zombieState.State = schemas.MergeLedgerState("zombie")
	_, err = ledger.Record(gpMergeSourceKey, zombieState, gpClock, nil)
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeMergeLedger, docutil.CodeOf(err))

	emptyKey := entry
	emptyKey.IdempotencyKey = ""
	_, err = ledger.Record(gpMergeSourceKey, emptyKey, gpClock, nil)
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeMergeLedger, docutil.CodeOf(err))

	_, err = ledger.Record(gpMergeSourceKey, entry, func() string { return "not-an-iso-timestamp" }, nil)
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeMergeLedger, docutil.CodeOf(err))

	_, err = ledger.Record("", entry, gpClock, nil)
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeMergeLedger, docutil.CodeOf(err))

	// Nothing was written.
	loaded, err := ledger.Load(gpMergeSourceKey)
	require.NoError(t, err)
	assert.Nil(t, loaded)

	// A ledger whose source_key disagrees with the write key is refused by
	// the store (fail closed, same discipline as run-record keys).
	store, _ := gpLedgerFixture(t)
	mismatched := schemas.MergeLedgerExample()
	mismatched.SourceKey = gpMergeSourceKey
	_, err = store.WriteMergeLedger("other-key", mismatched, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match the write key")

	// The relPath helper lives under state/merge-ledger/ with the injective
	// escape.
	relPath, err := state.DocLedgerRelPath("source-pr-42")
	require.NoError(t, err)
	assert.Equal(t, "state/merge-ledger/source-pr-42.json", relPath)
	relPath, err = state.DocLedgerRelPath("run/abc")
	require.NoError(t, err)
	assert.Equal(t, "state/merge-ledger/run__abc.json", relPath)
	relPath, err = state.DocLedgerRelPath("a_b")
	require.NoError(t, err)
	assert.Equal(t, "state/merge-ledger/a_ub.json", relPath)
}

func TestP6T16_MergeTargetRecheckTriggersOnMovement(t *testing.T) {
	// Merge queue: subsumes movement (even with the branch also moved).
	queue, err := gitpr.EvaluateMergeTargetRecheck(gitpr.MergeTargetRecheckInput{
		DocPrBaseSha:         "aaaa",
		DefaultBranchHeadSha: "bbbb",
		DefaultBranchMoved:   true,
		MergeQueueDetected:   true,
	})
	require.NoError(t, err)
	assert.Equal(t, gitpr.MergeTargetRecheckDecision{
		Recheck:           true,
		Trigger:           gpTriggerPtr(gitpr.MergeTargetTriggerMergeQueue),
		BaselineReresolve: true,
	}, queue)

	// Squash merge: the branch moved and the doc PR's base pin already sits
	// on the new head (the PR's own changes landed as a squash commit).
	squash, err := gitpr.EvaluateMergeTargetRecheck(gitpr.MergeTargetRecheckInput{
		DocPrBaseSha:         "cccc",
		DefaultBranchHeadSha: "cccc",
		DefaultBranchMoved:   true,
		MergeQueueDetected:   false,
	})
	require.NoError(t, err)
	assert.Equal(t, gitpr.MergeTargetRecheckDecision{
		Recheck:           true,
		Trigger:           gpTriggerPtr(gitpr.MergeTargetTriggerSquashMerge),
		BaselineReresolve: true,
	}, squash)

	// Default branch advanced: the branch moved past the doc PR's base.
	advanced, err := gitpr.EvaluateMergeTargetRecheck(gitpr.MergeTargetRecheckInput{
		DocPrBaseSha:         "dddd",
		DefaultBranchHeadSha: "eeee",
		DefaultBranchMoved:   true,
		MergeQueueDetected:   false,
	})
	require.NoError(t, err)
	assert.Equal(t, gitpr.MergeTargetRecheckDecision{
		Recheck:           true,
		Trigger:           gpTriggerPtr(gitpr.MergeTargetTriggerDefaultBranchAdvanced),
		BaselineReresolve: true,
	}, advanced)

	// Unchanged: no recheck, no D12 kickoff.
	unchanged, err := gitpr.EvaluateMergeTargetRecheck(gitpr.MergeTargetRecheckInput{
		DocPrBaseSha:         "ffff",
		DefaultBranchHeadSha: "ffff",
		DefaultBranchMoved:   false,
		MergeQueueDetected:   false,
	})
	require.NoError(t, err)
	assert.Equal(t, gitpr.MergeTargetRecheckDecision{
		Recheck:           false,
		Trigger:           nil,
		BaselineReresolve: false,
	}, unchanged)

	// Fail closed on malformed input (empty SHAs).
	_, err = gitpr.EvaluateMergeTargetRecheck(gitpr.MergeTargetRecheckInput{
		DocPrBaseSha:         "",
		DefaultBranchHeadSha: "ffff",
		DefaultBranchMoved:   false,
		MergeQueueDetected:   false,
	})
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeMergeTarget, docutil.CodeOf(err))
	_, err = gitpr.EvaluateMergeTargetRecheck(gitpr.MergeTargetRecheckInput{
		DocPrBaseSha:         "ffff",
		DefaultBranchHeadSha: "",
		DefaultBranchMoved:   false,
		MergeQueueDetected:   false,
	})
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeMergeTarget, docutil.CodeOf(err))

	// Not ported: the TS non-boolean `defaultBranchMoved: 'yes'` case — Go's
	// bool type makes it unrepresentable (same note as the ported source).
}

func TestP6T17_PostMergePrimitiveGatesReRun(t *testing.T) {
	// Default-branch push with open docsync PRs based on it → re-run, naming
	// them.
	rerun, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "refs/heads/main"},
		DefaultBranch: "main",
		OpenDocsyncPrs: []gitpr.OpenDocsyncPr{
			{Number: 7, Base: "main"},
			{Number: 9, Base: "main"},
		},
	})
	require.NoError(t, err)
	assert.True(t, rerun.ShouldRerun)
	assert.Contains(t, rerun.Reason, "#7")
	assert.Contains(t, rerun.Reason, "#9")

	// A bare branch name is accepted too.
	bare, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "main"},
		DefaultBranch: "main",
		OpenDocsyncPrs: []gitpr.OpenDocsyncPr{
			{Number: 7, Base: "main"},
		},
	})
	require.NoError(t, err)
	assert.True(t, bare.ShouldRerun)

	// Feature-branch push → no re-run, even with open docsync PRs.
	feature, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "refs/heads/feature/payments"},
		DefaultBranch: "main",
		OpenDocsyncPrs: []gitpr.OpenDocsyncPr{
			{Number: 7, Base: "main"},
		},
	})
	require.NoError(t, err)
	assert.False(t, feature.ShouldRerun)

	// Default-branch push with NO open docsync PRs → nothing to re-run.
	noPrs, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:          gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "refs/heads/main"},
		DefaultBranch:  "main",
		OpenDocsyncPrs: nil,
	})
	require.NoError(t, err)
	assert.False(t, noPrs.ShouldRerun)

	// PRs not based on the default branch are unaffected by the push.
	otherBase, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "refs/heads/main"},
		DefaultBranch: "main",
		OpenDocsyncPrs: []gitpr.OpenDocsyncPr{
			{Number: 11, Base: "release/1.2"},
		},
	})
	require.NoError(t, err)
	assert.False(t, otherBase.ShouldRerun)

	// Fail closed on malformed input.
	_, err = gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: "pull_request", Ref: "refs/heads/main"},
		DefaultBranch: "main",
	})
	require.Error(t, err)
	assert.Equal(t, gitpr.EDOCSYNCPostMerge, docutil.CodeOf(err))
	_, err = gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: ""},
		DefaultBranch: "main",
	})
	require.Error(t, err)
	assert.Equal(t, gitpr.EDOCSYNCPostMerge, docutil.CodeOf(err))
	_, err = gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "refs/heads/main"},
		DefaultBranch: "main",
		OpenDocsyncPrs: []gitpr.OpenDocsyncPr{
			{Number: 0, Base: "main"},
		},
	})
	require.Error(t, err)
	assert.Equal(t, gitpr.EDOCSYNCPostMerge, docutil.CodeOf(err))
}
