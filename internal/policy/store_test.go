package policy_test

// Ported from packages/policy/test/promotion-pipeline.test.ts (the
// PromotionStore describe blocks) — black-box, testify, t.TempDir() history
// dir, injected clock + id generator. The pipeline-level blocks of that TS
// suite are ported in pipeline_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Approve of a non-proposed row fails closed; approve without approver fails
// closed (INV-7/INV-9).
func TestStore_ApproveOfNonProposedRowFailsClosed(t *testing.T) {
	t.Parallel()
	store := promoNewStore(t)
	proposed, err := store.Propose(policy.PromotionProposal{
		PolicyVersion: 2,
		Reason:        "r",
		Evaluation:    promoEval,
	})
	require.NoError(t, err)

	_, err = store.Approve(proposed.PromotionID, "  ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "INV-7")

	_, err = store.Approve(proposed.PromotionID, "owner")
	require.NoError(t, err)

	_, err = store.Approve(proposed.PromotionID, "owner-again")
	require.Error(t, err)
	require.Contains(t, err.Error(), "only proposed")

	promoted, err := store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{Passed: true})
	require.NoError(t, err)
	require.Equal(t, schemas.PromotionStatusPromoted, promoted.Status)

	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{Passed: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "only approved")
}

// The promotion log read fails loud on non-ENOENT I/O errors — the audit
// surface never silently vanishes.
func TestStore_LogReadFailsLoudOnNonENOENTIO(t *testing.T) {
	t.Parallel()
	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	_, err = store.Propose(policy.PromotionProposal{PolicyVersion: 2, Reason: "r", Evaluation: promoEval})
	require.NoError(t, err)

	logPath := filepath.Join(historyDir, policy.PromotionLogFilename)
	// Force a non-ENOENT error portably: replace the FILE with a DIRECTORY of
	// the same name — a read then gets EISDIR (not ENOENT).
	require.NoError(t, os.Remove(logPath))
	require.NoError(t, os.Mkdir(logPath, 0o755))
	t.Cleanup(func() { _ = os.Remove(logPath) })

	_, err = store.ReadAll()
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "io", storeErr.Code)
	require.Contains(t, err.Error(), "never silently vanish")
}

// Rollback of a merely-proposed row is refused — only promoted rows roll
// back, with the original approver attribution.
func TestStore_RollbackOfMerelyProposedRowIsRefused(t *testing.T) {
	t.Parallel()
	store := promoNewStore(t)
	// propose with NO approve — an unapproved row has no human attribution
	proposed, err := store.Propose(policy.PromotionProposal{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "r",
		Evaluation:      promoEval,
	})
	require.NoError(t, err)
	require.Equal(t, schemas.PromotionStatusProposed, proposed.Status)

	// An unapproved promoted row cannot be hand-crafted via the store API —
	// the proposed-row refusal is the enforceable refusal point.
	_, err = store.Rollback(proposed.PromotionID)
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "invalid_transition", storeErr.Code)
	require.Contains(t, err.Error(), "only a promoted row")

	rows, err := store.ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

// The exported store path is ALSO gate-enforced — promote without a passed
// checklist fails closed.
func TestStore_ExportedStorePathIsGateEnforced(t *testing.T) {
	t.Parallel()
	store := promoNewStore(t)
	proposed, err := store.Propose(policy.PromotionProposal{PolicyVersion: 2, Reason: "r", Evaluation: promoEval})
	require.NoError(t, err)
	_, err = store.Approve(proposed.PromotionID, "owner")
	require.NoError(t, err)

	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{Passed: false})
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "invalid_transition", storeErr.Code)
	require.Contains(t, err.Error(), "PASSED gate checklist")

	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{
		Passed:      false,
		FailedGates: []string{"unsafe_zero"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsafe_zero")

	promoted, err := store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{Passed: true})
	require.NoError(t, err)
	require.Equal(t, schemas.PromotionStatusPromoted, promoted.Status)

	log, err := store.ReadAll()
	require.NoError(t, err)
	require.Len(t, log, 3)
	statuses := make([]schemas.PromotionStatus, 0, len(log))
	for _, row := range log {
		statuses = append(statuses, row.Status)
	}
	require.Equal(t, []schemas.PromotionStatus{
		schemas.PromotionStatusProposed,
		schemas.PromotionStatusApproved,
		schemas.PromotionStatusPromoted,
	}, statuses)
}

// The store's not-found and identity paths fail closed (the TS byId/latest
// status surface).
func TestStore_ByIdAndLatestStatusFor(t *testing.T) {
	t.Parallel()
	store := promoNewStore(t)

	missing, err := store.ByID("01JNOSUCHPROMOTION0000001")
	require.NoError(t, err)
	require.Nil(t, missing)

	status, found, err := store.LatestStatusFor(2)
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, status)

	proposed, err := store.Propose(policy.PromotionProposal{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "r",
		Evaluation:      promoEval,
	})
	require.NoError(t, err)

	byID, err := store.ByID(proposed.PromotionID)
	require.NoError(t, err)
	require.NotNil(t, byID)
	require.Equal(t, proposed.PromotionID, byID.PromotionID)

	status, found, err = store.LatestStatusFor(2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, schemas.PromotionStatusProposed, status)

	_, err = store.Approve(proposed.PromotionID, "owner")
	require.NoError(t, err)
	status, found, err = store.LatestStatusFor(2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, schemas.PromotionStatusApproved, status)

	// not-found transitions
	_, err = store.Approve("01JNOSUCHPROMOTION0000002", "owner")
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "not_found", storeErr.Code)
	_, err = store.Promote("01JNOSUCHPROMOTION0000003", policy.PromotionGateChecklist{Passed: true})
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "not_found", storeErr.Code)
	_, err = store.Rollback("01JNOSUCHPROMOTION0000004")
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "not_found", storeErr.Code)
}

// A rollback restores the prior version in one operation, carrying the
// original human approver attribution (INV-7).
func TestStore_RollbackRestoresPriorVersion(t *testing.T) {
	t.Parallel()
	store := promoNewStore(t)
	proposed, err := store.Propose(policy.PromotionProposal{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "r",
		Evaluation:      promoEval,
	})
	require.NoError(t, err)
	_, err = store.Approve(proposed.PromotionID, "repo-owner:@acme/maintainers")
	require.NoError(t, err)
	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{Passed: true})
	require.NoError(t, err)

	rolled, err := store.Rollback(proposed.PromotionID)
	require.NoError(t, err)
	require.Equal(t, schemas.PromotionStatusPromoted, rolled.Status)
	require.Equal(t, 1, rolled.PolicyVersion)
	require.NotNil(t, rolled.PreviousVersion)
	require.Equal(t, 2, *rolled.PreviousVersion)
	require.NotNil(t, rolled.ApprovedBy)
	require.Equal(t, "repo-owner:@acme/maintainers", *rolled.ApprovedBy)
	require.Equal(t, "rollback of promotion "+proposed.PromotionID, rolled.Reason)

	// the rolled-back row is itself a promoted row — a second rollback is the
	// one-op restore of the version it names
	again, err := store.Rollback(rolled.PromotionID)
	require.NoError(t, err)
	require.Equal(t, 2, again.PolicyVersion)
	require.NotNil(t, again.PreviousVersion)
	require.Equal(t, 1, *again.PreviousVersion)

	rows, err := store.ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 5)
	require.Equal(t, []schemas.PromotionStatus{
		schemas.PromotionStatusProposed,
		schemas.PromotionStatusApproved,
		schemas.PromotionStatusPromoted,
		schemas.PromotionStatusPromoted,
		schemas.PromotionStatusPromoted,
	}, []schemas.PromotionStatus{
		rows[0].Status, rows[1].Status, rows[2].Status, rows[3].Status, rows[4].Status,
	})
}

// A rollback of a promoted row without a previous version fails closed.
func TestStore_RollbackWithoutPreviousVersionRefused(t *testing.T) {
	t.Parallel()
	store := promoNewStore(t)
	proposed, err := store.Propose(policy.PromotionProposal{PolicyVersion: 2, Reason: "r", Evaluation: promoEval})
	require.NoError(t, err)
	_, err = store.Approve(proposed.PromotionID, "owner")
	require.NoError(t, err)
	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{Passed: true})
	require.NoError(t, err)

	_, err = store.Rollback(proposed.PromotionID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has no previous version to roll back to")
}

// The log replays schema-validated rows — a corrupted row fails loud instead
// of silently vanishing from the audit surface.
func TestStore_LogRowSchemaDriftFailsClosed(t *testing.T) {
	t.Parallel()
	store := promoNewStore(t)
	proposed, err := store.Propose(policy.PromotionProposal{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "r",
		Evaluation:      promoEval,
	})
	require.NoError(t, err)
	require.NotEmpty(t, proposed.PromotionID)

	logPath := store.LogPath()
	require.NoError(t, os.WriteFile(logPath,
		[]byte(`{"schema":"docsync.promotion-record/v1","promotion_id":"not-a-ulid","policy_version":2,"reason":"r","evaluation":{"report":{},"unsafe_auto_updates":0,"sample_size":1},"status":"proposed","at":"2026-10-08T12:00:00.000Z","unexpected":true}`+"\n"),
		0o644))

	_, err = store.ReadAll()
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "schema", storeErr.Code)
	require.Contains(t, err.Error(), "promotion log row failed schema validation")
}

// The store refuses to construct without its injections or with a relative
// history dir (fail closed, determinism).
func TestStore_ConstructionGuards(t *testing.T) {
	t.Parallel()
	_, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: "relative/policy-history",
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.Error(t, err)

	_, err = policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: promoNewHistoryDir(t),
		NewID:      promoNewID,
	})
	require.Error(t, err)

	_, err = policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: promoNewHistoryDir(t),
		Clock:      promoClock,
	})
	require.Error(t, err)

	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(historyDir, policy.PromotionLogFilename), store.LogPath())
}

// A proposed row is schema-validated BEFORE it is appended — a non-ULID id
// (the injected newId contract) fails closed and nothing is logged.
func TestStore_ProposeValidationFailsClosed(t *testing.T) {
	t.Parallel()
	store, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: promoNewHistoryDir(t),
		Clock:      promoClock,
		NewID:      func() string { return "not-a-ulid" },
	})
	require.NoError(t, err)
	_, err = store.Propose(policy.PromotionProposal{PolicyVersion: 2, Reason: "r", Evaluation: promoEval})
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "schema", storeErr.Code)
	require.Contains(t, err.Error(), "promotion row failed schema validation")
	rows, err := store.ReadAll()
	require.NoError(t, err)
	require.Empty(t, rows)
}

// An append whose read fails loudly (EISDIR, not ENOENT) fails closed — the
// audit surface never silently vanishes.
func TestStore_AppendReadFailureFailsClosed(t *testing.T) {
	t.Parallel()
	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	// replace the (about to be created) log path with a DIRECTORY: the append's
	// read then fails with EISDIR
	require.NoError(t, os.MkdirAll(historyDir, 0o755))
	require.NoError(t, os.Mkdir(store.LogPath(), 0o755))
	t.Cleanup(func() { _ = os.Remove(store.LogPath()) })

	_, err = store.Propose(policy.PromotionProposal{PolicyVersion: 2, Reason: "r", Evaluation: promoEval})
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "io", storeErr.Code)
}

// An append whose write fails fails closed with a typed io error (the CAS
// write is the durability boundary).
func TestStore_AppendWriteFailureFailsClosed(t *testing.T) {
	t.Parallel()
	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	// A read-only history dir makes the atomic temp-file + rename fail.
	require.NoError(t, os.MkdirAll(historyDir, 0o755))
	// A permission-denied write needs a non-root test process.
	if os.Geteuid() == 0 {
		t.Skip("permission-based io failure is not reachable as root")
	}
	require.NoError(t, os.Chmod(historyDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(historyDir, 0o755) })

	_, err = store.Propose(policy.PromotionProposal{PolicyVersion: 2, Reason: "r", Evaluation: promoEval})
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "io", storeErr.Code)
	require.Contains(t, err.Error(), "could not be appended")
}

// The typed errors unwrap to their docutil coded errors, so docutil.CodeOf
// reports the mapped codes (CONVENTIONS error mapping).
func TestErrorCodeMapping(t *testing.T) {
	t.Parallel()
	store := promoNewStore(t)
	proposed, err := store.Propose(policy.PromotionProposal{PolicyVersion: 2, Reason: "r", Evaluation: promoEval})
	require.NoError(t, err)
	_, err = store.Approve(proposed.PromotionID, "  ")
	require.Equal(t, docutil.CodeState, docutil.CodeOf(err))

	_, err = store.Promote("01JNOSUCHPROMOTION0000005", policy.PromotionGateChecklist{Passed: true})
	require.Equal(t, docutil.CodeState, docutil.CodeOf(err))

	pipeline := promoNewPipeline(t)
	_, err = pipeline.AutoPromote(policy.AutoPromotionInput{
		LearningMode: policy.LearningModePropose,
		PromotionProposalInput: policy.PromotionProposalInput{
			PolicyVersion:  2,
			Reason:         "config forcing auto",
			Evaluation:     promoEval,
			BaselineReport: promoReport(140, nil, promoReportOverrides{}),
			CandidateReport: promoReport(140, promoFloatPtr(0.93), promoReportOverrides{
				Precision: promoFloatPtr(0.95),
			}),
			CandidateWins:  90,
			CandidateTotal: 120,
			BaselineWins:   60,
			BaselineTotal:  120,
		},
	})
	require.Equal(t, docutil.CodeGuard, docutil.CodeOf(err))

	unsafeOne := 1
	_, err = pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "unsafe",
		Evaluation: policy.PromotionEvaluationEvidence{
			Report:            promoEval.Report,
			UnsafeAutoUpdates: unsafeOne,
			SampleSize:        140,
		},
		BaselineReport:  promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: promoWinningCandidate(140),
		CandidateWins:   90,
		CandidateTotal:  120,
		BaselineWins:    60,
		BaselineTotal:   120,
	})
	require.Equal(t, docutil.CodeGuard, docutil.CodeOf(err))

	canaryStore := promoNewCanaryStore(t)
	_, err = canaryStore.Promote("01JBSHAD0WBEHESTXAMP000006", "r")
	require.Equal(t, docutil.CodeState, docutil.CodeOf(err))
}
