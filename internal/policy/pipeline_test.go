package policy_test

// Ported from packages/policy/test/promotion-pipeline.test.ts,
// packages/policy/test/promotion-full-gates-p12-t06.test.ts and
// packages/policy/test/promotion-auto-p12-t07.test.ts (the promotion-pipeline
// halves of the latter two; the pure gate-library assertions of
// promotion-gates-p12-t13-regression.test.ts and evaluation-gates.test.ts
// belong to the gate-library unit) — black-box, testify, injected clock + id
// generator, t.TempDir() for the policy-history directory.

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------- test doubles */

// promoReportOverrides carries the metric overrides the TS `report(overrides)`
// helper applied (nil = the TS default, i.e. the key absent).
type promoReportOverrides struct {
	Precision         *float64
	DetectionRecall   *float64
	PatchCorrectness  *float64
	FalseUpdateRate   *float64
	AbstentionQuality *float64
}

// promoReport builds the TS `report()` fixture: the frozen-42-1 evaluation
// report with the given metric overrides, sample size and optional objective
// score.
func promoReport(sampleSize int, objectiveScore *float64, overrides promoReportOverrides) schemas.EvaluationReport {
	metrics := schemas.EvalReportMetrics{
		DetectionRecall:   0.8,
		Precision:         0.9,
		PatchCorrectness:  0.85,
		Minimality:        schemas.EvalMinimality{MeanEditDistance: 12, MeanHunkCount: 1.4},
		FalseUpdateRate:   0.02,
		AbstentionQuality: 0.7,
	}
	if overrides.Precision != nil {
		metrics.Precision = *overrides.Precision
	}
	if overrides.DetectionRecall != nil {
		metrics.DetectionRecall = *overrides.DetectionRecall
	}
	if overrides.PatchCorrectness != nil {
		metrics.PatchCorrectness = *overrides.PatchCorrectness
	}
	if overrides.FalseUpdateRate != nil {
		metrics.FalseUpdateRate = *overrides.FalseUpdateRate
	}
	if overrides.AbstentionQuality != nil {
		metrics.AbstentionQuality = *overrides.AbstentionQuality
	}
	report := schemas.EvaluationReport{
		Schema:        "docsync.evaluation-report/v1",
		PolicyVersion: 1,
		SplitID:       "frozen-42-1",
		SampleSize:    sampleSize,
		Metrics:       metrics,
		AbstainHealth: schemas.EvalReportAbstainHealth{AbstainRate: 0.2, OverrideRate: 0.1},
		ComputedAt:    "2026-10-08T12:00:00.000Z",
	}
	if objectiveScore != nil {
		report.ObjectiveScore = objectiveScore
	}
	return report
}

// promoEval is the TS EVAL fixture (the embedded promotion evaluation
// evidence).
var promoEval = policy.PromotionEvaluationEvidence{
	Report:            map[string]any{"policy_version": 2, "baseline_version": 1},
	UnsafeAutoUpdates: 0,
	SampleSize:        140,
}

var (
	promoCounterMu sync.Mutex
	promoCounter   int
)

// promoNewID is the TS newId(): a deterministic, ULID-shaped sequence. The
// counter is the TS module-level counter — guarded because the ported suite
// runs its cases in parallel (t.Parallel), unlike vitest's sequential files.
func promoNewID() string {
	promoCounterMu.Lock()
	defer promoCounterMu.Unlock()
	promoCounter++
	id := "01HTXSCHEMA1EXAMP1ES" + pad6(promoCounter) + "0"
	return id[:26]
}

func pad6(value int) string {
	digits := ""
	for _, digit := range []byte{byte('0' + value/100000%10), byte('0' + value/10000%10), byte('0' + value/1000%10), byte('0' + value/100%10), byte('0' + value/10%10), byte('0' + value%10)} {
		digits += string(digit)
	}
	return digits
}

// promoClock is the TS CLOCK — one fixed instant, never the wall clock.
func promoClock() string { return "2026-10-08T12:00:00.000Z" }

func promoFloatPtr(value float64) *float64 { return &value }
func promoIntPtr(value int) *int           { return &value }
func promoBoolPtr(value bool) *bool        { return &value }

// promoNewHistoryDir is the TS newHistoryDir(): a fresh policy-history dir.
func promoNewHistoryDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "policy-history")
}

// promoNewPipeline builds a pipeline over a fresh history dir.
func promoNewPipeline(t *testing.T) *policy.PromotionPipeline {
	t.Helper()
	pipeline, err := policy.NewPromotionPipeline(policy.PromotionPipelineOptions{
		HistoryDir: promoNewHistoryDir(t),
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	return pipeline
}

// promoNewStore builds a store over a fresh history dir.
func promoNewStore(t *testing.T) *policy.PromotionStore {
	t.Helper()
	store, err := policy.NewPromotionStore(policy.PromotionStoreOptions{
		HistoryDir: promoNewHistoryDir(t),
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	return store
}

// promoWinningCandidate is the TS WINNING_CANDIDATE fixture.
func promoWinningCandidate(sampleSize int) schemas.EvaluationReport {
	return promoReport(sampleSize, promoFloatPtr(0.93), promoReportOverrides{
		Precision:         promoFloatPtr(0.95),
		FalseUpdateRate:   promoFloatPtr(0.01),
		AbstentionQuality: promoFloatPtr(0.75),
	})
}

/* ------------------------------------------------------------------- V-P10-12 */

// V-P10-12: the promotion run is versioned, human-approved, logged, and
// rollbackable.
func TestVP10_12_FullPromotionRun(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)
	gateResult, err := pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "offline eval wins precision +0.06 with no regression and unsafe_auto_updates=0",
		Evaluation:      promoEval,
		BaselineReport:  promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: promoReport(140, nil, promoReportOverrides{
			Precision:         promoFloatPtr(0.95),
			FalseUpdateRate:   promoFloatPtr(0.01),
			AbstentionQuality: promoFloatPtr(0.75),
		}),
		CandidateWins:  90,
		CandidateTotal: 120,
		BaselineWins:   60,
		BaselineTotal:  120,
	})
	require.NoError(t, err)
	require.True(t, gateResult.Passed)

	logAfterPropose, err := pipeline.Log()
	require.NoError(t, err)
	require.Len(t, logAfterPropose, 1)
	proposed := logAfterPropose[len(logAfterPropose)-1]
	require.Equal(t, schemas.PromotionStatusProposed, proposed.Status)

	_, err = pipeline.Approve(proposed.PromotionID, "repo-owner:@acme/maintainers")
	require.NoError(t, err)
	_, err = pipeline.Promote(proposed.PromotionID)
	require.NoError(t, err)
	_, err = pipeline.Rollback(proposed.PromotionID)
	require.NoError(t, err)

	log, err := pipeline.Log()
	require.NoError(t, err)
	require.Len(t, log, 4)
	statuses := make([]schemas.PromotionStatus, 0, len(log))
	for _, row := range log {
		statuses = append(statuses, row.Status)
		// the log carries the evaluation evidence
		require.Equal(t, 140, row.Evaluation.SampleSize)
		require.Equal(t, 0, row.Evaluation.UnsafeAutoUpdates)
	}
	require.Equal(t, []schemas.PromotionStatus{
		schemas.PromotionStatusProposed,
		schemas.PromotionStatusApproved,
		schemas.PromotionStatusPromoted,
		schemas.PromotionStatusPromoted,
	}, statuses)

	rolled := log[len(log)-1]
	require.Equal(t, 1, rolled.PolicyVersion)
	require.NotNil(t, rolled.PreviousVersion)
	require.Equal(t, 2, *rolled.PreviousVersion)

	promoted := log[2]
	require.NotNil(t, promoted.ApprovedBy)
	require.Equal(t, "repo-owner:@acme/maintainers", *promoted.ApprovedBy)
}

// V-P10-13: the pipeline rejects a failing gate with the gate NAMED — before
// any approval.
func TestVP10_13_FailingGateNamedBeforeApproval(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)
	unsafeOne := 1
	_, err := pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "should fail",
		Evaluation: policy.PromotionEvaluationEvidence{
			Report:            promoEval.Report,
			UnsafeAutoUpdates: unsafeOne,
			SampleSize:        140,
		},
		BaselineReport: promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: promoReport(40, nil, promoReportOverrides{
			FalseUpdateRate: promoFloatPtr(0.05),
		}),
		CandidateWins:  10,
		CandidateTotal: 40,
		BaselineWins:   10,
		BaselineTotal:  40,
	})
	var gateFailure *policy.PromotionGateFailureError
	require.ErrorAs(t, err, &gateFailure)
	require.Contains(t, err.Error(), "failing gates:")
	// nothing was approved or promoted — the log has zero rows
	log, logErr := pipeline.Log()
	require.NoError(t, logErr)
	require.Empty(t, log)
}

/* ------------------------------------------------------------------- P12-T06 */

// P12-T06: bucket gates are inherited by the composed pipeline — propose
// refuses a bucket regression with the bucket NAMED.
func TestP12_T06_BucketGatesInheritedByComposedPipeline(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)
	_, err := pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "aggregate win hiding a bucket regression",
		Evaluation: policy.PromotionEvaluationEvidence{
			Report:            map[string]any{},
			UnsafeAutoUpdates: 0,
			SampleSize:        140,
		},
		BaselineReport:  promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: promoWinningCandidate(140),
		CandidateWins:   90,
		CandidateTotal:  120,
		BaselineWins:    60,
		BaselineTotal:   120,
		Buckets: []policy.PromotionBucketComparison{
			{
				Bucket:    "v17-review/v18-ignore",
				Baseline:  policy.PromotionBucketMetrics{FalseUpdateRate: 0.02, AbstentionQuality: 0.7},
				Candidate: policy.PromotionBucketMetrics{FalseUpdateRate: 0.02, AbstentionQuality: 0.4},
			},
		},
	})
	var gateFailure *policy.PromotionGateFailureError
	require.ErrorAs(t, err, &gateFailure)
	require.Contains(t, gateFailure.FailedGates, "bucket_no_regression:v17-review/v18-ignore")
	// nothing was proposed — the log stays empty (fail closed)
	log, logErr := pipeline.Log()
	require.NoError(t, logErr)
	require.Empty(t, log)
}

/* ----------------------------------------------------------- V-P12-4/5/6/8/9 */

// V-P12-4: a candidate with unsafe_auto_updates = 1 is rejected regardless of
// aggregate accuracy.
func TestVP12_4_UnsafeAutoUpdatesRejected(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)
	unsafeOne := 1
	_, err := pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "accuracy win with an unsafe auto-update",
		Evaluation: policy.PromotionEvaluationEvidence{
			Report:            promoEval.Report,
			UnsafeAutoUpdates: unsafeOne,
			SampleSize:        140,
		},
		BaselineReport: promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: promoReport(140, promoFloatPtr(0.97), promoReportOverrides{
			DetectionRecall:  promoFloatPtr(0.95),
			Precision:        promoFloatPtr(0.98),
			PatchCorrectness: promoFloatPtr(0.92),
		}),
		CandidateWins:  110,
		CandidateTotal: 120,
		BaselineWins:   60,
		BaselineTotal:  120,
	})
	var gateFailure *policy.PromotionGateFailureError
	require.ErrorAs(t, err, &gateFailure)

	// The failing gate is NAMED…
	gates, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{})
	require.NoError(t, err)
	result, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   promoReport(140, nil, promoReportOverrides{}),
		Candidate:                  promoReport(140, nil, promoReportOverrides{Precision: promoFloatPtr(0.98)}),
		CandidateUnsafeAutoUpdates: 1,
		CandidateWins:              110,
		CandidateTotal:             120,
		BaselineWins:               60,
		BaselineTotal:              120,
	})
	require.NoError(t, err)
	require.Contains(t, result.FailedGates, "unsafe_zero")

	// …and nothing was proposed or promoted.
	log, logErr := pipeline.Log()
	require.NoError(t, logErr)
	require.Empty(t, log)

	// Defense in depth on the store path: a proposal CARRYING unsafe = 1 in
	// its evaluation evidence cannot be promoted even with a passed checklist.
	store := promoNewStore(t)
	proposed, err := store.Propose(policy.PromotionProposal{
		PolicyVersion: 2,
		Reason:        "r",
		Evaluation: policy.PromotionEvaluationEvidence{
			Report:            promoEval.Report,
			UnsafeAutoUpdates: unsafeOne,
			SampleSize:        140,
		},
	})
	require.NoError(t, err)
	_, err = store.Approve(proposed.PromotionID, "repo-owner:@acme/maintainers")
	require.NoError(t, err)
	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{Passed: true})
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "invalid_transition", storeErr.Code)
	require.Contains(t, err.Error(), "not promotable away")
}

// V-P12-5: lucky-streak test — a candidate winning below the
// min-sample/significance floor is not promotable.
func TestVP12_5_LuckyStreakNotPromotable(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)
	_, err := pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "lucky streak",
		Evaluation: policy.PromotionEvaluationEvidence{
			Report:            promoEval.Report,
			UnsafeAutoUpdates: 0,
			SampleSize:        8,
		},
		BaselineReport: promoReport(8, nil, promoReportOverrides{}),
		CandidateReport: promoReport(8, promoFloatPtr(0.93), promoReportOverrides{
			Precision: promoFloatPtr(0.95),
		}),
		CandidateWins:  6,
		CandidateTotal: 8,
		BaselineWins:   3,
		BaselineTotal:  8,
	})
	var gateFailure *policy.PromotionGateFailureError
	require.ErrorAs(t, err, &gateFailure)

	// The named failures are exactly the floors — min_sample_size and
	// significance_floor (the ladder a lucky streak cannot climb).
	gates, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{})
	require.NoError(t, err)
	result, err := gates.CheckGates(policy.PromotionGateInputs{
		Baseline:                   promoReport(8, nil, promoReportOverrides{}),
		Candidate:                  promoReport(8, promoFloatPtr(0.93), promoReportOverrides{Precision: promoFloatPtr(0.95)}),
		CandidateUnsafeAutoUpdates: 0,
		CandidateWins:              6,
		CandidateTotal:             8,
		BaselineWins:               3,
		BaselineTotal:              8,
	})
	require.NoError(t, err)
	require.Contains(t, result.FailedGates, "min_sample_size")
	require.Contains(t, result.FailedGates, "significance_floor")

	// The floors are upward-clamped: repo config cannot lower them (OD-R24).
	strict, err := policy.NewPromotionGateLibrary(policy.PromotionGateLibraryOptions{
		MinComparisonSize: promoIntPtr(200),
	})
	require.NoError(t, err)
	require.Equal(t, 200, strict.MinComparisonSize)

	// A large-sample but insignificant delta: aggregate wins, floor blocks.
	insignificant := promoNewPipeline(t)
	_, err = insignificant.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "insignificant delta",
		Evaluation:      promoEval,
		BaselineReport:  promoReport(120, promoFloatPtr(0.8), promoReportOverrides{}),
		CandidateReport: promoReport(120, promoFloatPtr(0.81), promoReportOverrides{Precision: promoFloatPtr(0.91)}),
		CandidateWins:   103,
		CandidateTotal:  120,
		BaselineWins:    100,
		BaselineTotal:   120,
	})
	var gateFailure2 *policy.PromotionGateFailureError
	require.ErrorAs(t, err, &gateFailure2)

	log, logErr := pipeline.Log()
	require.NoError(t, logErr)
	require.Empty(t, log)
}

// V-P12-6: a candidate regressing abstention quality or false-update rate is
// rejected even with an accuracy win (anti-gaming).
func TestVP12_6_AbstentionOrFalseUpdateRegressionRejected(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)

	// Route-everything-to-ABSTAIN gaming: accuracy win, abstention quality falls.
	_, err := pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "abstention regression behind an accuracy win",
		Evaluation:      promoEval,
		BaselineReport:  promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: promoReport(140, promoFloatPtr(0.93), promoReportOverrides{
			Precision:         promoFloatPtr(0.95),
			AbstentionQuality: promoFloatPtr(0.6),
		}),
		CandidateWins:  90,
		CandidateTotal: 120,
		BaselineWins:   60,
		BaselineTotal:  120,
	})
	var gateFailure *policy.PromotionGateFailureError
	require.ErrorAs(t, err, &gateFailure)

	// Inflated-confidence gaming: accuracy win, false-update rate rises.
	_, err = pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   3,
		PreviousVersion: promoIntPtr(1),
		Reason:          "false-update regression behind an accuracy win",
		Evaluation:      promoEval,
		BaselineReport:  promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: promoReport(140, promoFloatPtr(0.93), promoReportOverrides{
			Precision:       promoFloatPtr(0.95),
			FalseUpdateRate: promoFloatPtr(0.05),
		}),
		CandidateWins:  90,
		CandidateTotal: 120,
		BaselineWins:   60,
		BaselineTotal:  120,
	})
	var gateFailure2 *policy.PromotionGateFailureError
	require.ErrorAs(t, err, &gateFailure2)

	log, logErr := pipeline.Log()
	require.NoError(t, logErr)
	require.Empty(t, log)
}

// V-P12-8: with all gates passing and learning.mode=auto, promotion occurs
// automatically; the event log contains full evaluation evidence; rollback
// restores the prior version in one command.
func TestVP12_8_AutoPromotion(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)
	promoted, err := pipeline.AutoPromote(policy.AutoPromotionInput{
		LearningMode: policy.LearningModeAuto,
		PromotionProposalInput: policy.PromotionProposalInput{
			PolicyVersion:   2,
			PreviousVersion: promoIntPtr(1),
			Reason:          "auto: all gates passed on the frozen split",
			Evaluation:      promoEval,
			BaselineReport:  promoReport(140, nil, promoReportOverrides{}),
			CandidateReport: promoWinningCandidate(140),
			CandidateWins:   90,
			CandidateTotal:  120,
			BaselineWins:    60,
			BaselineTotal:   120,
		},
	})
	require.NoError(t, err)
	require.Equal(t, schemas.PromotionStatusPromoted, promoted.Status)
	require.Equal(t, 2, promoted.PolicyVersion)

	// ONE pipeline, no separate fast path: the log is the promotion event log
	// and every row carries the full evaluation evidence (ARCH §8.9).
	log, err := pipeline.Log()
	require.NoError(t, err)
	statuses := make([]schemas.PromotionStatus, 0, len(log))
	for _, row := range log {
		statuses = append(statuses, row.Status)
		require.Equal(t, 140, row.Evaluation.SampleSize)
		require.Equal(t, 0, row.Evaluation.UnsafeAutoUpdates)
		require.Equal(t, "auto: all gates passed on the frozen split", row.Reason)
	}
	require.Equal(t, []schemas.PromotionStatus{
		schemas.PromotionStatusProposed,
		schemas.PromotionStatusApproved,
		schemas.PromotionStatusPromoted,
	}, statuses)

	// Application-owned: the approval attribution is the module-constant app
	// actor — never caller-supplied, never a model identity (INV-7).
	require.NotNil(t, log[1].ApprovedBy)
	require.Equal(t, policy.AutoPromotionActor, *log[1].ApprovedBy)

	// One-op rollback (ARCH §8.4/§8.9) restores the prior version.
	_, err = pipeline.Rollback(promoted.PromotionID)
	require.NoError(t, err)
	afterRollback, err := pipeline.Log()
	require.NoError(t, err)
	require.Len(t, afterRollback, 4)
	rolled := afterRollback[len(afterRollback)-1]
	require.Equal(t, 1, rolled.PolicyVersion)
	require.NotNil(t, rolled.PreviousVersion)
	require.Equal(t, 2, *rolled.PreviousVersion)
	require.Equal(t, schemas.PromotionStatusPromoted, rolled.Status)
}

// V-P12-9: bypass attempts — config forcing promotion with a failing gate;
// model output writing policy — are rejected by veto tests.
func TestVP12_9_BypassAttemptsRejected(t *testing.T) {
	t.Parallel()

	// (1) Config cannot force the auto path: a non-auto learning.mode gets a
	// typed veto, and config demanding auto without gate evidence is a
	// validation error.
	pipeline := promoNewPipeline(t)
	autoInput := func(mode policy.LearningMode) policy.AutoPromotionInput {
		return policy.AutoPromotionInput{
			LearningMode: mode,
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
		}
	}
	_, err := pipeline.AutoPromote(autoInput(policy.LearningModePropose))
	var veto *policy.PromotionBypassVetoError
	require.ErrorAs(t, err, &veto)

	_, err = pipeline.AutoPromote(autoInput(policy.LearningModeCanary))
	require.ErrorAs(t, err, &veto)
	require.Equal(t, policy.PromotionVetoLearningModeNotAuto, veto.Veto)

	gatesPassed := false
	_, err = policy.ResolveLearningMode(policy.LearningModeResolutionInput{
		RepoConfig:                 map[string]any{"learning": map[string]any{"mode": "auto"}},
		LatestPromotionGatesPassed: &gatesPassed,
	})
	var modeErr *policy.LearningModeValidationError
	require.ErrorAs(t, err, &modeErr)

	// (2) Config cannot force promotion across a FAILING gate: the gates
	// re-run in-pipeline unconditionally; a failing gate leaves nothing
	// proposed and nothing promotable.
	unsafeInput := autoInput(policy.LearningModeAuto)
	unsafeInput.Evaluation = policy.PromotionEvaluationEvidence{
		Report:            promoEval.Report,
		UnsafeAutoUpdates: 1,
		SampleSize:        140,
	}
	unsafeInput.CandidateReport = promoReport(140, promoFloatPtr(0.97), promoReportOverrides{
		Precision: promoFloatPtr(0.98),
	})
	unsafeInput.CandidateWins = 110
	_, err = pipeline.AutoPromote(unsafeInput)
	var gateFailure *policy.PromotionGateFailureError
	require.ErrorAs(t, err, &gateFailure)
	log, logErr := pipeline.Log()
	require.NoError(t, logErr)
	require.Empty(t, log)

	// (3) A fabricated checklist cannot promote: when the checklist carries
	// gate ROWS, the store cross-checks the claim against the rows and the
	// mandatory gate coverage.
	store := promoNewStore(t)
	proposed, err := store.Propose(policy.PromotionProposal{PolicyVersion: 2, Reason: "r", Evaluation: promoEval})
	require.NoError(t, err)
	_, err = store.Approve(proposed.PromotionID, "repo-owner:@acme/maintainers")
	require.NoError(t, err)
	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{
		Passed:      true,
		FailedGates: []string{},
		Rows:        []policy.PromotionGateChecklistRow{{Gate: "unsafe_zero", Passed: false}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "fabricated checklist")
	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{
		Passed:      true,
		FailedGates: []string{},
		Rows:        []policy.PromotionGateChecklistRow{{Gate: "made_up_gate", Passed: true}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mandatory gate")
	_, err = store.Promote(proposed.PromotionID, policy.PromotionGateChecklist{
		Passed:      false,
		FailedGates: []string{"unsafe_zero"},
	})
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "invalid_transition", storeErr.Code)

	// (4) Model output cannot write live policy: the ONLY promotion ids that
	// exist are ones created by a gate-passed propose; a model-authored record
	// (no propose, no human approval) has no promotable row — promote of an
	// unknown id fails closed, and an unapproved row refuses promotion (INV-9).
	freshPipeline := promoNewPipeline(t)
	_, err = freshPipeline.Promote("01JNOGATESNOEV1DENCE00000000")
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "invalid_transition", storeErr.Code)
	unapproved, err := store.Propose(policy.PromotionProposal{PolicyVersion: 3, Reason: "r", Evaluation: promoEval})
	require.NoError(t, err)
	_, err = store.Promote(unapproved.PromotionID, policy.PromotionGateChecklist{Passed: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "only approved promotes")
}

// TestAutoPromotionVetoMessages pins the verbatim TS veto message text.
func TestAutoPromotionVetoMessages(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)
	_, err := pipeline.AutoPromote(policy.AutoPromotionInput{
		LearningMode: policy.LearningModeOff,
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), `bypass vetoed: only learning.mode=auto automates promotion (got "off")`)
	assert.Contains(t, err.Error(), "config cannot force the auto path")
	assert.Contains(t, err.Error(), "(ARCH §8.9; V-P12-9)")
	var veto *policy.PromotionBypassVetoError
	require.True(t, errors.As(err, &veto))
	require.Equal(t, policy.PromotionVetoLearningModeNotAuto, veto.Veto)
}

// The pipeline refuses to construct without its injections or with invalid
// gate options (fail closed before any file is touched).
func TestPipeline_ConstructionGuards(t *testing.T) {
	t.Parallel()
	_, err := policy.NewPromotionPipeline(policy.PromotionPipelineOptions{
		HistoryDir: promoNewHistoryDir(t),
		Clock:      promoClock,
		NewID:      promoNewID,
		Alpha:      promoFloatPtr(1.5),
	})
	require.Error(t, err)

	_, err = policy.NewPromotionPipeline(policy.PromotionPipelineOptions{
		HistoryDir:        promoNewHistoryDir(t),
		Clock:             promoClock,
		NewID:             promoNewID,
		MinComparisonSize: func() *int { negative := -1; return &negative }(),
	})
	require.Error(t, err)

	_, err = policy.NewPromotionPipeline(policy.PromotionPipelineOptions{
		HistoryDir: promoNewHistoryDir(t),
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
}

// A malformed evaluation report fails closed at the gate BEFORE any row is
// proposed (the gates cannot be bypassed).
func TestPipeline_MalformedReportFailsClosed(t *testing.T) {
	t.Parallel()
	pipeline := promoNewPipeline(t)
	malformed := promoReport(140, nil, promoReportOverrides{})
	malformed.Metrics.Precision = 1.5 // outside the unit interval
	_, err := pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "out-of-range precision",
		Evaluation:      promoEval,
		BaselineReport:  promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: malformed,
		CandidateWins:   90,
		CandidateTotal:  120,
		BaselineWins:    60,
		BaselineTotal:   120,
	})
	require.Error(t, err)
	var gateFailure *policy.PromotionGateFailureError
	require.False(t, errors.As(err, &gateFailure))
	log, logErr := pipeline.Log()
	require.NoError(t, logErr)
	require.Empty(t, log)
}

// A gate-passed propose whose store write fails surfaces the typed store error
// (the pipeline never swallows a persistence failure).
func TestPipeline_StoreFailurePropagates(t *testing.T) {
	t.Parallel()
	historyDir := promoNewHistoryDir(t)
	pipeline, err := policy.NewPromotionPipeline(policy.PromotionPipelineOptions{
		HistoryDir: historyDir,
		Clock:      promoClock,
		NewID:      promoNewID,
	})
	require.NoError(t, err)
	// A read-only history dir makes the atomic append fail.
	require.NoError(t, os.MkdirAll(historyDir, 0o755))
	// A permission-denied write needs a non-root test process.
	if os.Geteuid() == 0 {
		t.Skip("permission-based io failure is not reachable as root")
	}
	require.NoError(t, os.Chmod(historyDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(historyDir, 0o755) })

	_, err = pipeline.Propose(policy.PromotionProposalInput{
		PolicyVersion:   2,
		PreviousVersion: promoIntPtr(1),
		Reason:          "cannot be persisted",
		Evaluation:      promoEval,
		BaselineReport:  promoReport(140, nil, promoReportOverrides{}),
		CandidateReport: promoWinningCandidate(140),
		CandidateWins:   90,
		CandidateTotal:  120,
		BaselineWins:    60,
		BaselineTotal:   120,
	})
	var storeErr *policy.PromotionStoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, "io", storeErr.Code)
}
