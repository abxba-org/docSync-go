package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func covBlockingStoreDir(t *testing.T, logFilename string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "policy-history")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, logFilename), 0o755))
	return dir
}

func covShadowRecord(recordID, runID, createdAt, expiresAt string) schemas.ShadowRunRecord {
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

func TestCOV_POLICY_018_PromotionStoreReadFailuresFailLoud(t *testing.T) {
	historyDir := covBlockingStoreDir(t, policy.PromotionLogFilename)
	store, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)

	t.Run("ReadAll/ByID refuse to let the audit surface silently vanish", func(t *testing.T) {
		_, err = store.ReadAll()
		require.Error(t, err)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "could not be read")
		row, err := store.ByID("01HTXSCHEMA1EXAMP1ES000010")
		require.Error(t, err)
		require.Nil(t, row)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
	})

	t.Run("Approve/Rollback/LatestStatusFor propagate the read failure fail-closed", func(t *testing.T) {
		_, err = store.Approve("01HTXSCHEMA1EXAMP1ES000010", "octocat")
		require.Error(t, err)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
		_, err = store.Rollback("01HTXSCHEMA1EXAMP1ES000010")
		require.Error(t, err)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
		_, _, err = store.LatestStatusFor(18)
		require.Error(t, err)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
		_, err = store.Promote("01HTXSCHEMA1EXAMP1ES000010", policy.PromotionGateChecklist{Passed: true})
		require.Error(t, err)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
	})

	t.Run("a schema-drifted promotion row fails loud instead of vanishing", func(t *testing.T) {
		driftDir := promoNewHistoryDir(t)
		driftStore, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
			HistoryDir: driftDir,
			Clock:      promoClock,
			NewID:      promoNewID,
		})
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(driftDir, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(driftDir, policy.PromotionLogFilename),
			[]byte("{\"bogus\":true}\n"), 0o644))
		_, err = driftStore.ByID("01HTXSCHEMA1EXAMP1ES000010")
		require.Error(t, err)
		require.Equal(t, docutil.CodeSchema, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "failed schema validation")
	})
}

func TestCOV_POLICY_019_PromotionPipelineConstructionFailsClosedOnBadStoreOptions(t *testing.T) {
	_, err := policy.NewPromotionPipeline(policy.PromotionPipelineOptions{
		HistoryDir: "relative/policy-history",
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.Error(t, err)
	require.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
	require.Contains(t, err.Error(), "historyDir must be an absolute path")
}

func TestCOV_POLICY_020_CanaryStoreReadFailuresFailLoud(t *testing.T) {
	historyDir := covBlockingStoreDir(t, policy.CanaryLogFilename)
	store, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)

	t.Run("ByID refuses to let the lifecycle surface silently vanish", func(t *testing.T) {
		row, err := store.ByID("01JBSHAD0WBEHESTXAMP000001")
		require.Error(t, err)
		require.Nil(t, row)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "could not be read")
	})

	t.Run("Promote cannot conclude a canary when the log cannot be read", func(t *testing.T) {
		_, err = store.Promote("01JBSHAD0WBEHESTXAMP000001", "evidence reviewed")
		require.Error(t, err)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
	})

	t.Run("Open fails closed when the append cannot read the current log", func(t *testing.T) {
		_, err = store.Open(policy.CanaryOpenInput{
			RunID:                   "shadow-run-blocked",
			Repo:                    "acme/widgets",
			CandidatePolicyVersion:  18,
			ProductionPolicyVersion: 17,
			CreatedAt:               promoISO(promoT0),
			ExpiresAt:               promoPlusDays(14),
		})
		require.Error(t, err)
		require.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "could not be read for append")
	})
}

func TestCOV_POLICY_021_CanaryStoreSchemaDriftFailsLoud(t *testing.T) {
	historyDir := promoNewHistoryDir(t)
	store, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(historyDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(historyDir, policy.CanaryLogFilename),
		[]byte("{\"bogus\":true}\n"), 0o644))
	_, err = store.ByID("01JBSHAD0WBEHESTXAMP000001")
	require.Error(t, err)
	require.Equal(t, docutil.CodeSchema, docutil.CodeOf(err))
	require.Contains(t, err.Error(), "failed schema validation")
}

func TestCOV_POLICY_022_CanaryLifecycleEvaluationFailsClosedOnBadInput(t *testing.T) {
	t.Run("an unparseable now instants is a schema-coded refusal", func(t *testing.T) {
		_, err := policy.FilterConsultableRecords(nil, "not-an-instant")
		require.Error(t, err)
		require.Contains(t, err.Error(), "now is not a parseable instant")
	})

	t.Run("a schema-invalid shadow record is refused before any verdict", func(t *testing.T) {
		broken := covShadowRecord("not-a-ulid", "shadow-run-broken",
			promoISO(promoT0), promoPlusDays(14))
		_, err := policy.EvaluateCanaryLifecycle([]schemas.ShadowRunRecord{broken}, promoPlusDays(15))
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed docsync.shadow-run-record/v1 validation")
	})
}

func TestCOV_POLICY_023_CanaryExpireStaleSortsEveryExpiredCanaryAndIsIdempotent(t *testing.T) {
	historyDir := promoNewHistoryDir(t)
	ids := []string{
		"01JBSHAD0WBEHESTXAMP000009",
		"01JBSHAD0WBEHESTXAMP000001",
	}
	next := 0
	store, err := policy.NewCanaryLifecycleStore(policy.CanaryLifecycleStoreOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID: func() string {
			id := ids[next]
			next++
			return id
		},
	})
	require.NoError(t, err)
	for index := 0; index < 2; index++ {
		opened, openErr := store.Open(policy.CanaryOpenInput{
			RunID:                   "shadow-run-stale",
			Repo:                    "acme/widgets",
			CandidatePolicyVersion:  18 + index,
			ProductionPolicyVersion: 17,
			CreatedAt:               promoISO(promoT0),
			ExpiresAt:               promoPlusDays(1),
		})
		require.NoError(t, openErr)
		require.Equal(t, schemas.CanaryStatusActive, opened.Status)
	}
	expired, err := store.ExpireStale(promoPlusDays(15))
	require.NoError(t, err)
	require.Len(t, expired, 2)
	require.Equal(t, "01JBSHAD0WBEHESTXAMP000001", expired[0].CanaryID,
		"expired canaries are reported in canary-id order")
	require.Equal(t, "01JBSHAD0WBEHESTXAMP000009", expired[1].CanaryID)
	for _, row := range expired {
		require.Equal(t, schemas.CanaryStatusExpired, row.Status)
		require.Contains(t, row.Reason, "auto-expired")
	}
	again, err := store.ExpireStale(promoPlusDays(15))
	require.NoError(t, err)
	require.Empty(t, again, "the auto-expiry pass is idempotent")
	expiredCanary, err := store.ByID("01JBSHAD0WBEHESTXAMP000001")
	require.NoError(t, err)
	require.NotNil(t, expiredCanary)
	require.Equal(t, schemas.CanaryStatusExpired, expiredCanary.Status)
	_, err = store.Promote("01JBSHAD0WBEHESTXAMP000001", "late promotion attempt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "EXPIRED")
}

func TestCOV_POLICY_024_ShadowRecordExpiryProbeMirrorsTheTSComparison(t *testing.T) {
	valid := covShadowRecord("01JBSHAD0WBEHESTXAMP000005", "shadow-run-boundary",
		promoISO(promoT0), promoPlusDays(14))
	expired, err := policy.IsShadowRecordExpired(valid, promoPlusDays(14))
	require.NoError(t, err)
	require.True(t, expired, "now at expires_at means the shadow is stale")
	active, err := policy.IsShadowRecordExpired(valid, promoPlusDays(13))
	require.NoError(t, err)
	require.False(t, active)
	unparseable := covShadowRecord("01JBSHAD0WBEHESTXAMP000005", "shadow-run-boundary",
		promoISO(promoT0), "not-an-instant")
	stale, err := policy.IsShadowRecordExpired(unparseable, promoPlusDays(15))
	require.NoError(t, err)
	require.False(t, stale, "an unparseable expires_at compares false, exactly as the TS NaN comparison does")
}
