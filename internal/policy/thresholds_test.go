package policy_test

// Ported from packages/policy/test/threshold-calibration-p12-t08.test.ts
// (P12-T08; D11; ARCH §5.3/§10.8; R4; V-P12-10): an adaptive threshold change
// produces a NEW policy version only; the time-split report shows no historical
// overfit; rolling re-eval detects a synthetic drift fixture.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// --- optional-input helpers (TS `splitCount?: number`, …) ---

func calSplitCount(value int) *int          { return &value }
func calWindowSize(value int) *int          { return &value }
func calRollingStep(value int) *int         { return &value }
func calOverfit(value float64) *float64     { return &value }
func calDrift(value float64) *float64       { return &value }
func calProbability(value float64) *float64 { return &value }

// v17Thresholds is the bootstrap-era calibration (policy v17: review 0.72, auto
// floor 0.94 — D11 data).
func v17Thresholds() policy.CalibrationThresholds {
	return policy.CalibrationThresholds{
		HighConfidence:               0.94,
		Review:                       0.72,
		CalibrationFloor:             0.94,
		DocumentationImpactAutoFloor: 0.94,
	}
}

// calTimelineCaseAt renders an instant exactly as the TS fixture's
// `new Date(Date.UTC(2026, 0, 1, 0, index)).toISOString()` does.
func calTimelineCaseAt(index int) string {
	return time.Date(2026, 1, 1, 0, index, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z07:00")
}

// calTimelineOptions is the makeCalibrationTimeline fixture shape.
type calTimelineOptions struct {
	count           int
	correctRate     float64
	driftFrom       *int
	driftRate       *float64
	highProbability *float64
	lowProbability  *float64
}

// makeCalibrationTimeline is the deterministic timeline fixture: `correctRate`
// correct in [0, cut), `driftRate` from cut — the decade-slot pattern means
// exactly `rate × 10` correct per 10-case block, so any contiguous multiple of
// 10 has exactly the requested accuracy.
func makeCalibrationTimeline(options calTimelineOptions) []policy.CalibrationTimelineCase {
	cases := make([]policy.CalibrationTimelineCase, 0, options.count)
	for index := 0; index < options.count; index++ {
		drifted := options.driftFrom != nil && index >= *options.driftFrom
		effectiveRate := options.correctRate
		if drifted && options.driftRate != nil {
			effectiveRate = *options.driftRate
		}
		perDecade := int(math.Round(effectiveRate * 10))
		correct := index%10 < perDecade
		item := policy.CalibrationTimelineCase{
			CaseID:  fmt.Sprintf("case-%04d", index),
			At:      calTimelineCaseAt(index),
			Correct: correct,
		}
		if options.highProbability != nil {
			probability := *options.highProbability
			if !correct {
				probability = 0.1
				if options.lowProbability != nil {
					probability = *options.lowProbability
				}
			}
			item.Probability = calProbability(probability)
		}
		cases = append(cases, item)
	}
	return cases
}

// calStable120 / calDrift120 / calOverfit120 are the three timeline fixtures of
// the TS suite.
func calStable120() []policy.CalibrationTimelineCase {
	return makeCalibrationTimeline(calTimelineOptions{count: 120, correctRate: 0.9})
}

func calDrift120() []policy.CalibrationTimelineCase {
	driftFrom := 90
	driftRate := 0.3
	return makeCalibrationTimeline(calTimelineOptions{
		count: 120, correctRate: 0.9, driftFrom: &driftFrom, driftRate: &driftRate})
}

func calOverfit120() []policy.CalibrationTimelineCase {
	from := 60
	rate := 0.7
	return makeCalibrationTimeline(calTimelineOptions{
		count: 120, correctRate: 1.0, driftFrom: &from, driftRate: &rate})
}

// canonicalCalibrationArtifactJSON renders the artifact the way the TS test's
// `canonicalJson(artifact)` does: canonical (key-sorted) JSON bytes.
func canonicalCalibrationArtifactJSON(t *testing.T, artifact *schemas.ThresholdCalibration) string {
	t.Helper()
	wire, err := json.Marshal(artifact)
	require.NoError(t, err)
	canonical, err := docutil.CanonicalJSON(json.RawMessage(wire))
	require.NoError(t, err)
	return canonical
}

// parseCalibrationArtifact mirrors `THRESHOLD_CALIBRATION_SCHEMA.parse(artifact)`
// — the strict round-trip through the registered parser (throws on invalid).
func parseCalibrationArtifact(t *testing.T, artifact *schemas.ThresholdCalibration) *schemas.ThresholdCalibration {
	t.Helper()
	wire, err := json.Marshal(artifact)
	require.NoError(t, err)
	parsed, err := schemas.ParseThresholdCalibration(wire)
	require.NoError(t, err)
	return parsed
}

func calEvolveBase() policy.EvolveThresholdsInput {
	return policy.EvolveThresholdsInput{
		PreviousPolicyVersion: 17,
		PolicyVersion:         18,
		CreatedBy:             "app:docsync/threshold-calibration@v1",
		Reason:                "r",
		Now:                   "2026-10-09T00:00:00.000Z",
		PreviousThresholds:    v17Thresholds(),
		ProposedThresholds:    v17Thresholds(),
		EvaluationSplitID:     "split",
		EvidenceRefs:          []string{"ref"},
		Timeline:              calStable120(),
	}
}

// TestFixedMatrixShapePinned covers 'the fixed confidence × risk matrix shape is
// pinned architecture (D11, ARCH §5.3)'.
func TestFixedMatrixShapePinned(t *testing.T) {
	t.Run("the computed matrix shape hash matches the pinned constant", func(t *testing.T) {
		computed, err := policy.ComputeMatrixShapeHash()
		require.NoError(t, err)
		assert.Equal(t, policy.MatrixShapeHashV1, computed)
	})

	t.Run("shape invariants: auto in exactly one cell, diagonal review, high-risk rows never auto", func(t *testing.T) {
		// The canonical JSON of the FIXED 3×3 table pins every cell, so the
		// shape invariants the TS suite reads straight off MATRIX
		// (auto only in low×high, `review` on the diagonal, the whole high-risk
		// row never `auto`) are asserted here as a byte-exact table.
		const fixedTable = `{"high":{"high":"review","low":"block","medium":"block"},` +
			`"low":{"high":"auto","low":"review","medium":"suggest"},` +
			`"medium":{"high":"suggest","low":"block","medium":"review"}}`
		computed, err := policy.ComputeMatrixShapeHash()
		require.NoError(t, err)
		assert.Equal(t, "sha256:"+docutil.ContentHash(fixedTable), computed)

		var cells map[string]map[string]string
		require.NoError(t, json.Unmarshal([]byte(fixedTable), &cells))
		autoCells := []string{}
		for risk, row := range cells {
			for confidence, cell := range row {
				if cell == "auto" {
					autoCells = append(autoCells, risk+"x"+confidence)
				}
			}
		}
		assert.Equal(t, []string{"lowxhigh"}, autoCells)
		assert.Equal(t, "review", cells["medium"]["medium"])
		assert.Equal(t, "review", cells["high"]["high"])
		assert.Equal(t, "block", cells["high"]["low"])
		assert.Equal(t, "block", cells["high"]["medium"])
		assert.NotEqual(t, "auto", cells["high"]["high"])
	})

	t.Run("every produced artifact carries the pinned shape hash — a shape change can never ride calibration data", func(t *testing.T) {
		artifact, err := policy.EvolveThresholds(calStableV18Input("2026-10-09T00:00:00.000Z"))
		require.NoError(t, err)
		assert.Equal(t, "fixed-3x3-v1", artifact.MatrixShape)
		assert.Equal(t, policy.MatrixShapeHashV1, artifact.MatrixShapeHash)
		assert.Equal(t, policy.MatrixShapeV1, artifact.MatrixShape)
	})

	t.Run("the shape-violation error is a named stop-and-surface contract, never a silent ride-along", func(t *testing.T) {
		shapeErr := policy.NewMatrixShapeViolationError("sha256:"+strings.Repeat("0", 64), policy.MatrixShapeHashV1)
		require.Error(t, shapeErr)
		assert.Equal(t, "sha256:"+strings.Repeat("0", 64), shapeErr.ComputedHash)
		assert.Equal(t, policy.MatrixShapeHashV1, shapeErr.PinnedHash)
		assert.Contains(t, shapeErr.Error(), "stop-and-surface")
		assert.Contains(t, shapeErr.Error(), "never ride calibration data")
		var target *policy.MatrixShapeViolationError
		require.ErrorAs(t, shapeErr, &target)
		assert.Equal(t, "EDOCSYNC_INTERNAL", docutil.CodeOf(shapeErr))
		assert.Equal(t, "EDOCSYNC_INTERNAL", shapeErr.Code())
	})
}

// calStableV18Input is the TS suite's evolveStableV18 helper.
func calStableV18Input(now string) policy.EvolveThresholdsInput {
	input := calEvolveBase()
	input.PreviousPolicyVersion = 17
	input.PolicyVersion = 18
	input.CreatedBy = "app:docsync/threshold-calibration@v1"
	input.Reason = "adaptive recalibration from the frozen split (stable fixture)"
	input.Now = now
	input.ProposedThresholds = v17Thresholds()
	input.ProposedThresholds.Review = 0.7
	input.EvaluationSplitID = "frozen-2026-10"
	input.EvidenceRefs = []string{"policy-history/evaluation-report-v17.json#frozen-2026-10"}
	input.Timeline = calStable120()
	return input
}

// TestEvolveThresholdsInputValidation covers 'evolveThresholds input validation
// (fail closed)'.
func TestEvolveThresholdsInputValidation(t *testing.T) {
	t.Run("rejects a non-increasing version — adaptive calibration never mutates vN in place", func(t *testing.T) {
		input := calEvolveBase()
		input.PreviousPolicyVersion = 18
		input.PolicyVersion = 18
		_, err := policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "never mutates an existing version in place")
	})

	t.Run("rejects an empty timeline and evidence-free calibration", func(t *testing.T) {
		input := calEvolveBase()
		input.Timeline = []policy.CalibrationTimelineCase{}
		_, err := policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty split")

		input = calEvolveBase()
		input.EvidenceRefs = []string{}
		_, err = policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evidenceRefs")
	})

	t.Run("rejects threshold-ordering violations (Routing ordering invariant)", func(t *testing.T) {
		input := calEvolveBase()
		input.ProposedThresholds.Review = 0.95
		input.ProposedThresholds.HighConfidence = 0.94
		_, err := policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "review must not exceed high_confidence")
	})

	t.Run("rejects under-covered splits and non-assessable rolling windows", func(t *testing.T) {
		input := calEvolveBase()
		input.SplitCount = calSplitCount(1)
		_, err := policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "integer ≥ 2")

		input = calEvolveBase()
		input.WindowSize = calWindowSize(121)
		_, err = policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fail closed")

		// One single window (window ≥ half the span with a full-span step) →
		// drift is not assessable → fail, never silent pass.
		input = calEvolveBase()
		input.WindowSize = calWindowSize(100)
		input.RollingStep = calRollingStep(100)
		_, err = policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least two windows")
	})

	t.Run("rejects malformed provenance fields (createdBy, reason, now, split id, evidence refs, case fields)", func(t *testing.T) {
		cases := []struct {
			name    string
			mutate  func(*policy.EvolveThresholdsInput)
			expects string
		}{
			{"empty createdBy", func(i *policy.EvolveThresholdsInput) { i.CreatedBy = "" }, "createdBy must be a non-empty string"},
			{"empty reason", func(i *policy.EvolveThresholdsInput) { i.Reason = "" }, "reason must be a non-empty string"},
			{"bad now", func(i *policy.EvolveThresholdsInput) { i.Now = "not-a-timestamp" }, "now must be an ISO-8601 instant"},
			{"empty split id", func(i *policy.EvolveThresholdsInput) { i.EvaluationSplitID = "" }, "evaluationSplitId must be a non-empty string"},
			{"empty evidence ref", func(i *policy.EvolveThresholdsInput) { i.EvidenceRefs = []string{""} }, "each evidenceRef must be a non-empty string"},
			{"zero previous version", func(i *policy.EvolveThresholdsInput) { i.PreviousPolicyVersion = 0 }, "previousPolicyVersion must be a positive integer"},
			{"zero new version", func(i *policy.EvolveThresholdsInput) { i.PolicyVersion = 0 }, "policyVersion must be a positive integer"},
			{
				"empty caseId",
				func(i *policy.EvolveThresholdsInput) {
					timeline := calStable120()
					timeline[3].CaseID = ""
					i.Timeline = timeline
				},
				"timeline.3.caseId must be a non-empty string",
			},
			{
				"bad case at",
				func(i *policy.EvolveThresholdsInput) {
					timeline := calStable120()
					timeline[3].At = "not-a-timestamp"
					i.Timeline = timeline
				},
				"timeline.3.at must be an ISO-8601 instant",
			},
			{
				"out-of-range case probability",
				func(i *policy.EvolveThresholdsInput) {
					timeline := calStable120()
					timeline[3].Probability = calProbability(1.5)
					i.Timeline = timeline
				},
				"timeline.3.probability must be a finite number in [0, 1] (got 1.5)",
			},
			{
				"previous thresholds ordering",
				func(i *policy.EvolveThresholdsInput) {
					i.PreviousThresholds.CalibrationFloor = 0.5
				},
				"previousThresholds.calibration_floor bounds the no-man's zone",
			},
			{
				"proposed thresholds ordering",
				func(i *policy.EvolveThresholdsInput) {
					i.ProposedThresholds.HighConfidence = 0.5
					i.ProposedThresholds.CalibrationFloor = 0.94
				},
				"proposedThresholds.review must not exceed high_confidence",
			},
			{
				"overfit tolerance out of range",
				func(i *policy.EvolveThresholdsInput) { i.OverfitTolerance = calOverfit(1.5) },
				"overfitTolerance must be a finite number in [0, 1]",
			},
			{
				"drift tolerance out of range",
				func(i *policy.EvolveThresholdsInput) { i.DriftTolerance = calDrift(-0.5) },
				"driftTolerance must be a finite number in [0, 1]",
			},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				input := calEvolveBase()
				testCase.mutate(&input)
				_, err := policy.EvolveThresholds(input)
				require.Error(t, err)
				assert.Contains(t, err.Error(), testCase.expects)
			})
		}
	})
}

// TestTimeSplitAndRollingReports covers 'time-split + rolling reports (R4
// helpers)'.
func TestTimeSplitAndRollingReports(t *testing.T) {
	t.Run("time-split flags historical overfit when the benefit is concentrated in the past", func(t *testing.T) {
		report, err := policy.ComputeTimeSplitReport(calOverfit120(), v17Thresholds(), 2, 0.1)
		require.NoError(t, err)
		assert.True(t, report.HistoricalOverfitDetected)
		require.Len(t, report.Splits, 2)
		assert.Equal(t, 1.0, report.Splits[0].Accuracy)
		assert.InDelta(t, 0.7, report.Splits[1].Accuracy, 1e-10)
		assert.Contains(t, report.Detail, "HISTORICAL OVERFIT")
	})

	t.Run("rolling report flags drift only on a real late-window collapse", func(t *testing.T) {
		stable, err := policy.ComputeRollingReevalReport(calStable120(), v17Thresholds(), 30, 10, 0.1)
		require.NoError(t, err)
		assert.False(t, stable.DriftDetected)

		drifted, err := policy.ComputeRollingReevalReport(calDrift120(), v17Thresholds(), 30, 10, 0.1)
		require.NoError(t, err)
		assert.True(t, drifted.DriftDetected)
	})

	t.Run("reports carry the exact detail strings and case counts (deterministic windows)", func(t *testing.T) {
		stable, err := policy.ComputeRollingReevalReport(calStable120(), v17Thresholds(), 30, 10, 0.1)
		require.NoError(t, err)
		require.Len(t, stable.Windows, 10)
		assert.Equal(t, 30, stable.WindowSize)
		assert.Equal(t, 10, stable.Step)
		assert.Equal(t, 30, stable.Windows[0].CaseCount)
		assert.Equal(t, "w0", stable.Windows[0].WindowID)
		assert.Equal(t, "w9", stable.Windows[9].WindowID)
		assert.Equal(t, "latest w9 accuracy 0.9000 vs reference mean 0.9000 over 9 earlier window(s) — "+
			"drop 0.0000 within the 0.1 tolerance: no drift detected (R4)", stable.Detail)

		overfit, err := policy.ComputeTimeSplitReport(calOverfit120(), v17Thresholds(), 2, 0.1)
		require.NoError(t, err)
		assert.Equal(t, "earliest t1 accuracy 1.0000 vs latest t2 accuracy 0.7000 — benefit concentrated "+
			"in the past exceeds the 0.1 tolerance: HISTORICAL OVERFIT detected (R4)", overfit.Detail)
		assert.Equal(t, "t1", overfit.Splits[0].SplitID)
		assert.Equal(t, "t2", overfit.Splits[1].SplitID)
		assert.Equal(t, 60, overfit.Splits[0].CaseCount)
		assert.Equal(t, 60, overfit.Splits[1].CaseCount)
	})
}

// TestVP12_10NewVersionOnlyArtifact covers 'V-P12-10: an adaptive threshold change
// produces a new policy version only (vN is never mutated in place); the
// time-split report shows no historical overfit; rolling re-eval detects a
// synthetic drift fixture'.
func TestVP12_10NewVersionOnlyArtifact(t *testing.T) {
	t.Run("creates a NEW artifact, leaves the old version byte-identical, shows no overfit, and detects drift", func(t *testing.T) {
		// The OLD artifact: policy v17's calibration, produced once and then
		// treated as immutable committed data.
		v17Artifact, err := policy.EvolveThresholds(policy.EvolveThresholdsInput{
			PreviousPolicyVersion: 16,
			PolicyVersion:         17,
			CreatedBy:             "app:docsync/threshold-calibration@v1",
			Reason:                "bootstrap-era calibration of the 0.94/0.72 routing data (D11)",
			Now:                   "2026-09-01T00:00:00.000Z",
			PreviousThresholds:    v17Thresholds(),
			ProposedThresholds:    v17Thresholds(),
			EvaluationSplitID:     "frozen-2026-09",
			EvidenceRefs:          []string{"policy-history/evaluation-report-v16.json#frozen-2026-09"},
			Timeline:              calStable120(),
		})
		require.NoError(t, err)
		v17BytesBefore := canonicalCalibrationArtifactJSON(t, v17Artifact)
		v17Snapshot := *v17Artifact
		// The old thresholds object handed to the workflow — must never be mutated.
		previousThresholds := v17Thresholds()
		previousThresholdsSnapshot := previousThresholds
		// The shared timeline — must never be reordered or mutated in place.
		timeline := calStable120()
		timelineSnapshot := append([]policy.CalibrationTimelineCase(nil), timeline...)

		// THE ADAPTIVE CHANGE: v17 → v18 with a re-calibrated review floor.
		proposed := v17Thresholds()
		proposed.Review = 0.7
		v18Artifact, err := policy.EvolveThresholds(policy.EvolveThresholdsInput{
			PreviousPolicyVersion: 17,
			PolicyVersion:         18,
			CreatedBy:             "app:docsync/threshold-calibration@v1",
			Reason:                "adaptive recalibration: review floor 0.72 → 0.70 on split evidence",
			Now:                   "2026-10-09T00:00:00.000Z",
			PreviousThresholds:    previousThresholds,
			ProposedThresholds:    proposed,
			EvaluationSplitID:     "frozen-2026-10",
			EvidenceRefs:          []string{"policy-history/evaluation-report-v17.json#frozen-2026-10"},
			Timeline:              timeline,
		})
		require.NoError(t, err)

		// NEW artifact created with a strictly increasing version — never an
		// in-place mutation.
		assert.Equal(t, 18, v18Artifact.PolicyVersion)
		assert.Equal(t, 17, v18Artifact.PreviousPolicyVersion)
		assert.Equal(t, 0.7, v18Artifact.Thresholds.Review)
		require.NotPanics(t, func() { parseCalibrationArtifact(t, v18Artifact) })

		// The OLD version is never mutated: its canonical JSON bytes are
		// unchanged…
		assert.Equal(t, v17BytesBefore, canonicalCalibrationArtifactJSON(t, v17Artifact))
		// …its parsed object is deep-identical to the pre-call snapshot…
		assert.Equal(t, &v17Snapshot, v17Artifact)
		// …the previous thresholds object was only read, and the timeline was
		// never reordered.
		assert.Equal(t, previousThresholdsSnapshot, previousThresholds)
		assert.Equal(t, timelineSnapshot, timeline)

		// R4 evidence #1: the time-split report shows NO historical overfit
		// (stable fixture).
		assert.Len(t, v18Artifact.TimeSplitReport.Splits, 2)
		assert.False(t, v18Artifact.TimeSplitReport.HistoricalOverfitDetected)
		assert.Contains(t, v18Artifact.TimeSplitReport.Detail, "no historical overfit")

		// R4 evidence #2 (contrast): rolling re-eval on the SAME stable
		// fixture sees no drift…
		assert.False(t, v18Artifact.RollingReevalReport.DriftDetected)

		// …and DETECTS the synthetic drift fixture (accuracy collapse in the
		// last window).
		driftedArtifact, err := policy.EvolveThresholds(policy.EvolveThresholdsInput{
			PreviousPolicyVersion: 17,
			PolicyVersion:         19,
			CreatedBy:             "app:docsync/threshold-calibration@v1",
			Reason:                "drift-fixture probe: rolling re-eval must detect the synthetic collapse",
			Now:                   "2026-10-09T01:00:00.000Z",
			PreviousThresholds:    v17Thresholds(),
			ProposedThresholds:    v17Thresholds(),
			EvaluationSplitID:     "frozen-2026-10-drift",
			EvidenceRefs:          []string{"policy-history/evaluation-report-v17.json#frozen-2026-10-drift"},
			Timeline:              calDrift120(),
		})
		require.NoError(t, err)
		assert.True(t, driftedArtifact.RollingReevalReport.DriftDetected)
		assert.Contains(t, driftedArtifact.RollingReevalReport.Detail, "DRIFT detected")
		// The drift evidence lives in the ARTIFACT — committed data, not a
		// transient log line.
		assert.True(t, parseCalibrationArtifact(t, driftedArtifact).RollingReevalReport.DriftDetected)
	})

	t.Run("is deterministic: the same input produces a byte-identical artifact (injected clock)", func(t *testing.T) {
		a, err := policy.EvolveThresholds(calStableV18Input("2026-10-09T00:00:00.000Z"))
		require.NoError(t, err)
		b, err := policy.EvolveThresholds(calStableV18Input("2026-10-09T00:00:00.000Z"))
		require.NoError(t, err)
		assert.Equal(t, canonicalCalibrationArtifactJSON(t, a), canonicalCalibrationArtifactJSON(t, b))

		// And the clock is INJECTED — a different `now` stamps a different
		// `created_at`, nothing else.
		later, err := policy.EvolveThresholds(calStableV18Input("2026-10-10T00:00:00.000Z"))
		require.NoError(t, err)
		assert.Equal(t, "2026-10-10T00:00:00.000Z", later.CreatedAt)
		normalized := *later
		normalized.CreatedAt = a.CreatedAt
		assert.Equal(t, canonicalCalibrationArtifactJSON(t, a), canonicalCalibrationArtifactJSON(t, &normalized))
	})

	t.Run("emits the artifact wire shape of the registered schema (no unknown or missing keys)", func(t *testing.T) {
		artifact, err := policy.EvolveThresholds(calStableV18Input("2026-10-09T00:00:00.000Z"))
		require.NoError(t, err)
		wire, err := json.Marshal(artifact)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(wire, &decoded))
		for _, key := range []string{
			"schema", "policy_version", "previous_policy_version", "created_by", "reason",
			"created_at", "thresholds", "matrix_shape", "matrix_shape_hash", "evaluation",
			"time_split_report", "rolling_reeval_report",
		} {
			require.Contains(t, decoded, key)
		}
		assert.Equal(t, "docsync.threshold-calibration/v1", decoded["schema"])
		assert.Equal(t, "fixed-3x3-v1", decoded["matrix_shape"])
		assert.Len(t, decoded, 12)

		thresholds, ok := decoded["thresholds"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, map[string]any{
			"high_confidence":                 0.94,
			"review":                          0.7,
			"calibration_floor":               0.94,
			"documentation_impact_auto_floor": 0.94,
		}, thresholds)
	})
}

// TestCalibrationOrderingAndRangeGuards pins the guards and ordering the report
// helpers carry (each TS RangeError branch of the calibration module).
func TestCalibrationOrderingAndRangeGuards(t *testing.T) {
	t.Run("assertCalibrationThresholds rejects each out-of-range value by name", func(t *testing.T) {
		cases := []struct {
			name    string
			mutate  func(*policy.CalibrationThresholds)
			expects string
		}{
			{"high_confidence above 1", func(c *policy.CalibrationThresholds) { c.HighConfidence = 1.5 }, "Threshold_calibration.high_confidence"},
			{"review below 0", func(c *policy.CalibrationThresholds) { c.Review = -0.5 }, "Threshold_calibration.review"},
			{"calibration_floor above 1", func(c *policy.CalibrationThresholds) { c.CalibrationFloor = 2 }, "Threshold_calibration.calibration_floor"},
			{"auto floor above 1", func(c *policy.CalibrationThresholds) { c.DocumentationImpactAutoFloor = 3 }, "Threshold_calibration.documentation_impact_auto_floor"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				input := calEvolveBase()
				testCase.mutate(&input.PreviousThresholds)
				_, err := policy.EvolveThresholds(input)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "must be a finite number in [0, 1] (got ")
				assert.Contains(t, err.Error(), strings.Replace(testCase.expects, "Threshold_calibration", "previousThresholds", 1))
			})
		}
	})

	t.Run("the timeline is sorted chronologically with a caseId tie-break", func(t *testing.T) {
		// Deliberately out of order, with two cases sharing an instant so the
		// caseId tie-break decides. If the sort respected input order — or
		// caseId order alone — the split accuracies would differ.
		shuffled := []policy.CalibrationTimelineCase{
			{CaseID: "c-b", At: "2026-01-01T00:02:00.000Z", Correct: false},
			{CaseID: "c-a", At: "2026-01-01T00:00:00.000Z", Correct: true},
			{CaseID: "c-d", At: "2026-01-01T00:01:00.000Z", Correct: false},
			{CaseID: "c-c", At: "2026-01-01T00:01:00.000Z", Correct: true},
		}
		report, err := policy.ComputeTimeSplitReport(shuffled, v17Thresholds(), 2, 0.1)
		require.NoError(t, err)
		require.Len(t, report.Splits, 2)
		// t1 = [c-a (00:00), c-c (00:01, first by caseId)] → both correct.
		assert.Equal(t, 1.0, report.Splits[0].Accuracy)
		// t2 = [c-d (00:01, second by caseId), c-b (00:02)] → both wrong.
		assert.Equal(t, 0.0, report.Splits[1].Accuracy)
		assert.True(t, report.HistoricalOverfitDetected)
	})

	t.Run("case metrics report the high-confidence share only over probability-carrying cases", func(t *testing.T) {
		timeline := []policy.CalibrationTimelineCase{
			{CaseID: "c1", At: "2026-01-01T00:00:00.000Z", Correct: true, Probability: calProbability(0.95)},
			{CaseID: "c2", At: "2026-01-01T00:01:00.000Z", Correct: false, Probability: calProbability(0.5)},
			{CaseID: "c3", At: "2026-01-01T00:02:00.000Z", Correct: true},
			{CaseID: "c4", At: "2026-01-01T00:03:00.000Z", Correct: true, Probability: calProbability(0.94)},
		}
		report, err := policy.ComputeTimeSplitReport(timeline, v17Thresholds(), 2, 0.1)
		require.NoError(t, err)
		require.Len(t, report.Splits, 2)
		assert.Equal(t, 2, report.Splits[0].CaseCount)
		assert.Equal(t, 0.5, report.Splits[0].Accuracy)
		// 0.95 ≥ high_confidence 0.94, 0.5 < 0.94, c3 carries no probability.
		assert.Equal(t, 0.5, report.Splits[0].HighConfidenceShare)
		assert.Equal(t, 1.0, report.Splits[1].Accuracy)
		// Only c4 carries a probability in the second split → share 1/1.
		assert.Equal(t, 1.0, report.Splits[1].HighConfidenceShare)
		// Low-confidence (0.94 exactly) still counts as high (boundary is >=).
		assert.Equal(t, 1.0, report.Splits[1].HighConfidenceShare)
	})

	t.Run("rolling re-eval rejects malformed windows, steps and under-covered spans", func(t *testing.T) {
		_, err := policy.ComputeRollingReevalReport(calStable120(), v17Thresholds(), 0, 10, 0.1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "windowSize must be a positive integer (got 0)")

		_, err = policy.ComputeRollingReevalReport(calStable120(), v17Thresholds(), 30, 0, 0.1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "step must be a positive integer (got 0)")

		_, err = policy.ComputeRollingReevalReport(calStable120(), v17Thresholds(), 200, 10, 0.1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least windowSize=200 cases (got 120)")
	})

	t.Run("time-split rejects an under-covered split and a malformed split count", func(t *testing.T) {
		_, err := policy.ComputeTimeSplitReport(calStable120(), v17Thresholds(), 0, 0.1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "splitCount must be an integer ≥ 2 (got 0)")

		_, err = policy.ComputeTimeSplitReport(calOverfit120()[:1], v17Thresholds(), 2, 0.1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires at least 2 cases for 2 splits (got 1)")
	})

	t.Run("evolveThresholds propagates report-guard failures from both reports", func(t *testing.T) {
		input := calEvolveBase()
		input.WindowSize = calWindowSize(0)
		_, err := policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "windowSize must be a positive integer (got 0)")

		input = calEvolveBase()
		input.RollingStep = calRollingStep(0)
		_, err = policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "step must be a positive integer (got 0)")

		input = calEvolveBase()
		input.SplitCount = calSplitCount(1000)
		_, err = policy.EvolveThresholds(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires at least 1000 cases for 1000 splits")
	})
}

func TestCalibrationBootstrapDefaults(t *testing.T) {
	t.Run("defaults are exported constants, not magic numbers in code paths", func(t *testing.T) {
		assert.Equal(t, 2, policy.DefaultTemporalSplitCount)
		assert.Equal(t, 0.1, policy.DefaultOverfitTolerance)
		assert.Equal(t, 30, policy.DefaultRollingWindowSize)
		assert.Equal(t, 10, policy.DefaultRollingStep)
		assert.Equal(t, 0.1, policy.DefaultDriftTolerance)
	})

	t.Run("the constants are the labeled defaults the artifact records", func(t *testing.T) {
		artifact, err := policy.EvolveThresholds(calStableV18Input("2026-10-09T00:00:00.000Z"))
		require.NoError(t, err)
		assert.Equal(t, policy.DefaultTemporalSplitCount, artifact.TimeSplitReport.SplitCount)
		assert.Equal(t, policy.DefaultRollingWindowSize, artifact.RollingReevalReport.WindowSize)
		assert.Equal(t, policy.DefaultRollingStep, artifact.RollingReevalReport.Step)
		assert.Equal(t, policy.DefaultOverfitTolerance, artifact.TimeSplitReport.OverfitTolerance)
		assert.Equal(t, policy.DefaultDriftTolerance, artifact.RollingReevalReport.DriftTolerance)
	})
}
