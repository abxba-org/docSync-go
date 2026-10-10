package policy_test

// Ported from packages/policy/test/canary-lifecycle-p12-t05.test.ts — canary
// lifecycle (P12-T05; ARCH §8.6, CTX §21; INV-9): time-box canaries,
// AUTO-EXPIRY of stale shadows (typed `expired` status, never consulted
// again), canary status tracked in `.docsync/policy-history/` under the same
// append-only record convention as the promotion event log, and expired
// canaries are NEVER promotable. Black-box, testify, injected clock + id
// generator, t.TempDir() history dir.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// promoShadowHash is the 40-hex recorded input hash of every fixture record.
const promoShadowHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// promoT0 is the TS T0 instant.
var promoT0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// promoISO renders an instant the way JS `toISOString()` does (millisecond
// precision, Z suffix).
func promoISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// promoPlusDays is the TS `new Date(Date.parse(T0) + n * DAY_MS).toISOString()`.
func promoPlusDays(days int) string { return promoISO(promoT0.AddDate(0, 0, days)) }

// promoShadowRecord is the TS shadowRecord fixture: a valid
// `docsync.shadow-run-record/v1` (sampled ⇒ decision + input hashes, no skip
// reason) with the given time-box.
func promoShadowRecord(recordID, runID, createdAt, expiresAt string) schemas.ShadowRunRecord {
	return schemas.ShadowRunRecord{
		Schema:     "docsync.shadow-run-record/v1",
		RecordID:   recordID,
		RunID:      runID,
		PrID:       "8a73f2c",
		Repo:       "acme/widgets",
		ShadowOnly: true,
		Sampled:    true,
		Eligible:   true,
		Sampling: schemas.ShadowSampling{
			Sampler:      schemas.ShadowSamplerSHA256HexThreshold,
			Fraction:     0.1,
			DrawHex:      "00000001",
			ThresholdHex: "1999999a",
		},
		Policy:   schemas.ShadowRecordedPolicy{ProductionVersion: 17, CandidateVersion: 18},
		Decision: &schemas.ShadowDecision{Action: schemas.ActionReview, Abstained: false},
		InputHashes: &schemas.ShadowRecordedInputHashes{
			ChangeAnalysis: promoShadowHash,
			CandidateSet:   promoShadowHash,
			DiffSubset:     promoShadowHash,
		},
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}
}

// promoNewCanaryStore builds a canary store over a fresh policy-history dir.
func promoNewCanaryStore(t *testing.T) *policy.CanaryLifecycleStore {
	t.Helper()
	store, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: promoNewHistoryDir(t),
		Clock:      func() string { return "2026-10-09T12:00:00.000Z" },
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	return store
}

// promoVerdictPairs projects verdicts onto the [recordId, status] pairs the TS
// suite asserted.
func promoVerdictPairs(result policy.CanaryLifecycleResult) [][2]string {
	pairs := make([][2]string, 0, len(result.Verdicts))
	for _, verdict := range result.Verdicts {
		pairs = append(pairs, [2]string{verdict.RecordID, string(verdict.Status)})
	}
	return pairs
}

// P12-T05: stale shadows auto-expire and expired canaries are never
// promotable.
func TestP12_T05_StaleShadowsAutoExpire(t *testing.T) {
	t.Parallel()

	active := promoShadowRecord("01JBSHAD0WBEHESTXAMP000001", "shadow-run-active",
		promoISO(promoT0), promoPlusDays(28))
	stale := promoShadowRecord("01JBSHAD0WBEHESTXAMP000002", "shadow-run-stale",
		promoISO(promoT0), promoPlusDays(14))
	oneDayIn := promoPlusDays(1)
	fifteenDaysIn := promoPlusDays(15)

	// Time-boxing: expiry is decided by expires_at vs the INJECTED now.
	beforeExpiry, err := policy.EvaluateCanaryLifecycle([]schemas.ShadowRunRecord{active, stale}, oneDayIn)
	require.NoError(t, err)
	require.Equal(t, [][2]string{
		{"01JBSHAD0WBEHESTXAMP000001", "active"},
		{"01JBSHAD0WBEHESTXAMP000002", "active"},
	}, promoVerdictPairs(beforeExpiry))
	require.Equal(t, 2, beforeExpiry.ActiveCount)
	require.Equal(t, 0, beforeExpiry.ExpiredCount)
	require.Equal(t, oneDayIn, beforeExpiry.Now)

	afterExpiry, err := policy.EvaluateCanaryLifecycle([]schemas.ShadowRunRecord{active, stale}, fifteenDaysIn)
	require.NoError(t, err)
	require.Equal(t, [][2]string{
		{"01JBSHAD0WBEHESTXAMP000001", "active"},
		{"01JBSHAD0WBEHESTXAMP000002", "expired"},
	}, promoVerdictPairs(afterExpiry))
	require.Equal(t, 1, afterExpiry.ExpiredCount)
	require.Equal(t, 1, afterExpiry.ActiveCount)

	// AUTO-EXPIRY: the stale shadow is dropped from the consultable set —
	// expired records are never consulted again (ARCH §8.6).
	consultable, err := policy.FilterConsultableRecords([]schemas.ShadowRunRecord{active, stale}, fifteenDaysIn)
	require.NoError(t, err)
	require.Equal(t, []schemas.ShadowRunRecord{active}, consultable)

	// Canary status tracked in `.docsync/policy-history/` (canaries.jsonl —
	// the promotion-store convention: append-only validated rows).
	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: historyDir,
		Clock:      func() string { return "2026-10-09T12:00:00.000Z" },
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	activeCanary, err := store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-active",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 17,
		CreatedAt:               active.CreatedAt,
		ExpiresAt:               active.ExpiresAt,
	})
	require.NoError(t, err)
	staleCanary, err := store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-stale",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  19,
		ProductionPolicyVersion: 17,
		CreatedAt:               stale.CreatedAt,
		ExpiresAt:               stale.ExpiresAt,
	})
	require.NoError(t, err)
	require.Equal(t, schemas.CanaryStatusActive, activeCanary.Status)
	require.Equal(t, schemas.CanaryStatusActive, staleCanary.Status)
	// the default open reason (TS `input.reason ?? default`)
	require.Equal(t, "canary opened: shadow run active within its time-box (ARCH §8.6)",
		staleCanary.Reason)

	expired, err := store.ExpireStale(fifteenDaysIn)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	require.Equal(t, staleCanary.CanaryID, expired[0].CanaryID)
	require.Equal(t, schemas.CanaryStatusExpired, expired[0].Status)
	require.Contains(t, expired[0].Reason, "auto-expired")
	require.Contains(t, expired[0].Reason, fifteenDaysIn)
	require.Contains(t, expired[0].Reason, stale.ExpiresAt)
	require.Contains(t, expired[0].Reason, "never consulted again, never promotable")

	// expireStale is idempotent: an already-expired canary is not re-appended.
	again, err := store.ExpireStale(fifteenDaysIn)
	require.NoError(t, err)
	require.Empty(t, again)

	// An EXPIRED canary is NEVER promotable (fail closed, typed refusal)…
	_, err = store.Promote(staleCanary.CanaryID, "too late")
	var canaryErr *policy.CanaryLifecycleError
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "invalid_transition", canaryErr.Code)
	require.Contains(t, err.Error(), "EXPIRED")
	require.Contains(t, err.Error(), "never consulted again and never promotable")

	// …and never re-openable into the consultable set.
	stillGone, err := policy.FilterConsultableRecords([]schemas.ShadowRunRecord{stale}, fifteenDaysIn)
	require.NoError(t, err)
	require.Empty(t, stillGone)

	// An ACTIVE canary within its time-box concludes normally.
	promotedCanary, err := store.Promote(activeCanary.CanaryID, "canary concluded with a gate-passing win")
	require.NoError(t, err)
	require.Equal(t, schemas.CanaryStatusPromoted, promotedCanary.Status)
	require.Equal(t, "canary concluded with a gate-passing win", promotedCanary.Reason)

	log, err := store.ReadAll()
	require.NoError(t, err)
	for _, row := range log {
		require.Equal(t, "docsync.canary-record/v1", row.Schema)
	}
	require.Equal(t, []schemas.CanaryStatus{
		schemas.CanaryStatusActive,
		schemas.CanaryStatusActive,
		schemas.CanaryStatusExpired,
		schemas.CanaryStatusPromoted,
	}, []schemas.CanaryStatus{
		log[0].Status, log[1].Status, log[2].Status, log[3].Status,
	})
	require.Equal(t, "canaries.jsonl", policy.CanaryLogFilename)
	require.Equal(t, filepath.Join(historyDir, "canaries.jsonl"), store.LogPath())
}

// Malformed lifecycle inputs fail closed: unparseable now, unknown canary,
// empty reason.
func TestP12_T05_MalformedInputsFailClosed(t *testing.T) {
	t.Parallel()
	store := promoNewCanaryStore(t)

	_, err := policy.EvaluateCanaryLifecycle(nil, "not-an-instant")
	var canaryErr *policy.CanaryLifecycleError
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "schema", canaryErr.Code)
	require.Contains(t, err.Error(), "evaluateCanaryLifecycle: now is not a parseable instant")

	_, err = store.ExpireStale("not-an-instant")
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "schema", canaryErr.Code)
	require.Contains(t, err.Error(), "expireStale: now is not a parseable instant")

	_, err = store.Promote("01JBSHAD0WBEHESTXAMP000003", "r")
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "not_found", canaryErr.Code)
	require.Contains(t, err.Error(), "canary 01JBSHAD0WBEHESTXAMP000003 not found")

	_, err = store.Reject("01JBSHAD0WBEHESTXAMP000004", "r")
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "not_found", canaryErr.Code)
}

// The expiry boundary is inclusive: `now` at exactly `expires_at` is stale
// (ARCH §8.6 "at or after").
func TestP12_T05_ExpiryBoundaryIsInclusive(t *testing.T) {
	t.Parallel()
	record := promoShadowRecord("01JBSHAD0WBEHESTXAMP000005", "shadow-run-boundary",
		promoISO(promoT0), promoPlusDays(14))
	justBefore, err := policy.EvaluateCanaryLifecycle([]schemas.ShadowRunRecord{record}, promoPlusDays(13))
	require.NoError(t, err)
	require.Equal(t, policy.CanaryLifecycleActive, justBefore.Verdicts[0].Status)

	exactly, err := policy.EvaluateCanaryLifecycle([]schemas.ShadowRunRecord{record}, promoPlusDays(14))
	require.NoError(t, err)
	require.Equal(t, policy.CanaryLifecycleExpired, exactly.Verdicts[0].Status)
	require.Equal(t, 1, exactly.ExpiredCount)
	require.Equal(t, 0, exactly.ActiveCount)
}

// An active canary concludes as rejected (e.g. its evaluation failed); a
// concluded canary never transitions again, and a conclusion needs a reason.
func TestP12_T05_RejectPathAndTerminalStatuses(t *testing.T) {
	t.Parallel()
	store := promoNewCanaryStore(t)
	canary, err := store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-reject",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 17,
		CreatedAt:               promoISO(promoT0),
		ExpiresAt:               promoPlusDays(14),
	})
	require.NoError(t, err)

	_, err = store.Reject(canary.CanaryID, "   ")
	var canaryErr *policy.CanaryLifecycleError
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "invalid_transition", canaryErr.Code)
	require.Contains(t, err.Error(), "concluding a canary requires a reason")

	rejected, err := store.Reject(canary.CanaryID, "evaluation regressed on the eval split")
	require.NoError(t, err)
	require.Equal(t, schemas.CanaryStatusRejected, rejected.Status)
	require.Equal(t, "evaluation regressed on the eval split", rejected.Reason)

	// a concluded canary never transitions again (only active can promote/reject)
	_, err = store.Promote(canary.CanaryID, "too late")
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "invalid_transition", canaryErr.Code)
	require.Contains(t, err.Error(), "canary "+canary.CanaryID+" is rejected — only an active canary can be promoted")
	_, err = store.Reject(canary.CanaryID, "again")
	require.ErrorAs(t, err, &canaryErr)
	require.Contains(t, err.Error(), "only an active canary can be rejected")

	// the log carries the append-only transitions
	log, err := store.ReadAll()
	require.NoError(t, err)
	require.Len(t, log, 2)
	require.Equal(t, schemas.CanaryStatusActive, log[0].Status)
	require.Equal(t, schemas.CanaryStatusRejected, log[1].Status)
}

// An expired canary cannot be re-rejected either: `expired` is terminal for
// every transition out of it.
func TestP12_T05_ExpiredIsTerminal(t *testing.T) {
	t.Parallel()
	store := promoNewCanaryStore(t)
	canary, err := store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-terminal",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 17,
		CreatedAt:               promoISO(promoT0),
		ExpiresAt:               promoPlusDays(7),
	})
	require.NoError(t, err)

	expired, err := store.ExpireStale(promoPlusDays(8))
	require.NoError(t, err)
	require.Len(t, expired, 1)

	_, err = store.Reject(canary.CanaryID, "but it looked fine")
	var canaryErr *policy.CanaryLifecycleError
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "invalid_transition", canaryErr.Code)
	require.Contains(t, err.Error(), "never consulted again and never promotable")

	latest, err := store.ByID(canary.CanaryID)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, schemas.CanaryStatusExpired, latest.Status)
}

// An opened row is schema-validated BEFORE it is appended — candidate and
// production versions must differ (a canary compares two policies), and the
// time-box must be strictly after created_at.
func TestCanary_OpenValidationFailsClosed(t *testing.T) {
	t.Parallel()
	store := promoNewCanaryStore(t)

	_, err := store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-same",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 18,
		CreatedAt:               promoISO(promoT0),
		ExpiresAt:               promoPlusDays(7),
	})
	var canaryErr *policy.CanaryLifecycleError
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "schema", canaryErr.Code)
	require.Contains(t, err.Error(), "canary row failed schema validation")

	_, err = store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-inverted",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 17,
		CreatedAt:               promoISO(promoT0),
		ExpiresAt:               promoISO(promoT0), // not strictly after created_at
	})
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "schema", canaryErr.Code)

	rows, err := store.ReadAll()
	require.NoError(t, err)
	require.Empty(t, rows)
}

// An explicit open reason overrides the default one.
func TestCanary_OpenWithExplicitReason(t *testing.T) {
	t.Parallel()
	store := promoNewCanaryStore(t)
	reason := "opened manually: W1 shadow regression watch"
	canary, err := store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-explicit",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 17,
		CreatedAt:               promoISO(promoT0),
		ExpiresAt:               promoPlusDays(7),
		Reason:                  &reason,
	})
	require.NoError(t, err)
	require.Equal(t, reason, canary.Reason)

	byID, err := store.ByID(canary.CanaryID)
	require.NoError(t, err)
	require.NotNil(t, byID)
	require.Equal(t, reason, byID.Reason)
	require.Equal(t, "2026-10-09T12:00:00.000Z", byID.At)
}

// The canary store refuses to construct without its injections or with a
// relative history dir (fail closed, determinism).
func TestCanary_ConstructionGuards(t *testing.T) {
	t.Parallel()
	_, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: "relative/policy-history",
		Clock:      func() string { return "2026-10-09T12:00:00.000Z" },
		NewID:      promoNewID,
	})
	require.Error(t, err)

	_, err = policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: promoNewHistoryDir(t),
		NewID:      promoNewID,
	})
	require.Error(t, err)

	_, err = policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: promoNewHistoryDir(t),
		Clock:      func() string { return "2026-10-09T12:00:00.000Z" },
	})
	require.Error(t, err)

	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: historyDir,
		Clock:      func() string { return "2026-10-09T12:00:00.000Z" },
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(historyDir, policy.CanaryLogFilename), store.LogPath())
}

// A canary log read fails loud on non-ENOENT I/O errors — the lifecycle
// surface never silently vanishes.
func TestCanary_LogReadFailsLoudOnNonENOENTIO(t *testing.T) {
	t.Parallel()
	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: historyDir,
		Clock:      func() string { return "2026-10-09T12:00:00.000Z" },
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	_, err = store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-io",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 17,
		CreatedAt:               promoISO(promoT0),
		ExpiresAt:               promoPlusDays(7),
	})
	require.NoError(t, err)

	logPath := filepath.Join(historyDir, policy.CanaryLogFilename)
	require.NoError(t, os.Remove(logPath))
	require.NoError(t, os.Mkdir(logPath, 0o755))
	t.Cleanup(func() { _ = os.Remove(logPath) })

	_, err = store.ReadAll()
	var canaryErr *policy.CanaryLifecycleError
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "io", canaryErr.Code)
	require.Contains(t, err.Error(), "never silently vanish")

	_, err = store.ExpireStale(promoPlusDays(30))
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "io", canaryErr.Code)
}

// An append whose write fails fails closed with a typed io error.
func TestCanary_AppendWriteFailureFailsClosed(t *testing.T) {
	t.Parallel()
	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: historyDir,
		Clock:      func() string { return "2026-10-09T12:00:00.000Z" },
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

	_, err = store.Open(policy.CanaryOpenInput{
		RunID:                   "shadow-run-nowrite",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 17,
		CreatedAt:               promoISO(promoT0),
		ExpiresAt:               promoPlusDays(7),
	})
	var canaryErr *policy.CanaryLifecycleError
	require.ErrorAs(t, err, &canaryErr)
	require.Equal(t, "io", canaryErr.Code)
	require.Contains(t, err.Error(), "could not be appended")
}
