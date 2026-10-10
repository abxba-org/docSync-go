package policy_test

// Ported from packages/policy/test/calibration-harness.test.ts
// (P2-T21; ARCH §10.8; R4; the A1 promotion gate + ECE math).

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
)

// --- optional-input helpers (TS `bins?: number` / `threshold?: number`) ---

func calBins(value int) *int                  { return &value }
func calThreshold(threshold float64) *float64 { return &threshold }
func calBudget(budgetMs float64) *float64     { return &budgetMs }
func calProvider(provider string) *string     { return &provider }

func calSample(probability float64, correct bool) policy.CalibrationSample {
	return policy.CalibrationSample{Probability: probability, Correct: correct}
}

// TestCalibrationBindingKey covers 'the (policy_version, model binding)
// calibration key (D3, §10.8)'.
func TestCalibrationBindingKey(t *testing.T) {
	t.Run("formats the binding key as model#provider-or-default", func(t *testing.T) {
		key, err := policy.BindingKey(policy.CalibrationBinding{Model: "jev"})
		require.NoError(t, err)
		assert.Equal(t, "jev#default", key)

		key, err = policy.BindingKey(policy.CalibrationBinding{Model: "jev", Provider: calProvider("openrouter")})
		require.NoError(t, err)
		assert.Equal(t, "jev#openrouter", key)
	})

	t.Run("rejects an empty model id", func(t *testing.T) {
		_, err := policy.BindingKey(policy.CalibrationBinding{Model: ""})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binding.model must be a non-empty string")
		assert.Equal(t, "EDOCSYNC_VALIDATION", docutil.CodeOf(err))
	})
}

// TestCalibrationECEHandComputed covers 'ECE math — hand-computed 2-bin example
// (exact value)'.
func TestCalibrationECEHandComputed(t *testing.T) {
	// bins = 2 → bin0 = [0, 0.5), bin1 = [0.5, 1].
	// bin0: three samples at 0.25, one correct  → accuracy 1/3, avgConfidence 0.25.
	// bin1: three samples at 0.75, two correct  → accuracy 2/3, avgConfidence 0.75.
	// ECE = (3/6)·|1/3 − 1/4| + (3/6)·|2/3 − 3/4| = 1/12 + … = 1/12.
	twoBinSamples := []policy.CalibrationSample{
		calSample(0.25, true),
		calSample(0.25, false),
		calSample(0.25, false),
		calSample(0.75, true),
		calSample(0.75, true),
		calSample(0.75, false),
	}

	t.Run("reproduces ECE = 1/12 for a hand-computable two-bin split (bins = 2)", func(t *testing.T) {
		result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       twoBinSamples,
			Bins:          calBins(2),
		})
		require.NoError(t, err)
		assert.InDelta(t, 1.0/12, result.ECE, 1e-12)
		require.Len(t, result.PerBin, 2)
		assert.Equal(t, 0.0, result.PerBin[0].BinLower)
		assert.Equal(t, 0.5, result.PerBin[0].BinUpper)
		assert.Equal(t, 3, result.PerBin[0].Count)
		assert.Equal(t, 0.25, result.PerBin[0].AvgConfidence)
		assert.InDelta(t, 1.0/3, result.PerBin[0].Accuracy, 1e-12)
		assert.Equal(t, 0.5, result.PerBin[1].BinLower)
		assert.Equal(t, 1.0, result.PerBin[1].BinUpper)
		assert.Equal(t, 3, result.PerBin[1].Count)
		assert.Equal(t, 0.75, result.PerBin[1].AvgConfidence)
		assert.InDelta(t, 2.0/3, result.PerBin[1].Accuracy, 1e-12)
	})

	t.Run("thresholds and bins are DATA params with labeled bootstrap defaults", func(t *testing.T) {
		assert.Equal(t, 10, policy.DefaultCalibrationBins)
		assert.Equal(t, 0.1, policy.DefaultCalibrationECEThreshold)
		// The 2-bin split above (ECE = 1/12 ≈ 0.0833) passes the default 0.1
		// gate inclusively and fails a stricter 0.05 gate — the threshold is data.
		result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       twoBinSamples,
			Bins:          calBins(2),
		})
		require.NoError(t, err)
		assert.Equal(t, 0.1, result.Threshold)
		assert.True(t, result.Calibrated)

		stricter, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       twoBinSamples,
			Bins:          calBins(2),
			Threshold:     calThreshold(0.05),
		})
		require.NoError(t, err)
		assert.False(t, stricter.Calibrated)
	})

	t.Run("pins the result to (policy_version, binding) and reports the exact threshold used", func(t *testing.T) {
		result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 18,
			Binding:       policy.CalibrationBinding{Model: "jev", Provider: calProvider("openrouter")},
			Samples:       []policy.CalibrationSample{calSample(0.9, true)},
			Threshold:     calThreshold(0.05),
		})
		require.NoError(t, err)
		assert.Equal(t, 18, result.PolicyVersion)
		assert.Equal(t, "jev#openrouter", result.BindingKey)
		assert.Equal(t, 0.05, result.Threshold)
	})
}

// Miscalibrated fixture: probabilities 0.90–0.99, 50% correctness → single bin,
// ECE = |0.5 − 0.945| = 0.445.
func miscalibratedSamples() []policy.CalibrationSample {
	samples := []policy.CalibrationSample{}
	for i := 0; i < 10; i++ {
		probability := 0.9 + 0.01*float64(i)
		for j := 0; j < 10; j++ {
			samples = append(samples, calSample(probability, j < 5))
		}
	}
	return samples
}

// Well-calibrated fixture: per bin, accuracy sits exactly 0.05 above
// avgConfidence → ECE = 0.05.
func wellCalibratedSamples() []policy.CalibrationSample {
	samples := []policy.CalibrationSample{}
	for i := 0; i < 10; i++ {
		probability := (float64(i) + 0.5) / 10
		correctCount := i + 1
		for j := 0; j < 10; j++ {
			samples = append(samples, calSample(probability, j < correctCount))
		}
	}
	return samples
}

// TestMiscalibratedFixtureGatesPromotion covers 'V-P2-10: miscalibrated synthetic
// fixture reports ECE above threshold and gates promotion (A1 covers ECE +
// latency)'.
func TestMiscalibratedFixtureGatesPromotion(t *testing.T) {
	t.Run("the miscalibrated fixture reports ECE ~0.445 above the threshold and sets calibrated false", func(t *testing.T) {
		result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       miscalibratedSamples(),
		})
		require.NoError(t, err)
		assert.Equal(t, 10, result.BinCount)
		assert.InDelta(t, 0.445, result.ECE, 1e-3)
		assert.Greater(t, result.ECE, result.Threshold)
		assert.False(t, result.Calibrated)
		highBin := result.PerBin[9]
		assert.Equal(t, 100, highBin.Count)
		assert.InDelta(t, 0.5, highBin.Accuracy, 1e-10)
		assert.InDelta(t, 0.945, highBin.AvgConfidence, 1e-10)
		// Empty bins are reported with zeros and contribute nothing to ECE.
		assert.Len(t, result.PerBin, 10)
		assert.Equal(t, 0, result.PerBin[0].Count)
		assert.Equal(t, 0.0, result.PerBin[0].Accuracy)
		assert.Equal(t, 0.0, result.PerBin[0].AvgConfidence)
	})

	t.Run("the well-calibrated fixture reports ECE ~0.05 within the threshold and sets calibrated true", func(t *testing.T) {
		result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev-canary"},
			Samples:       wellCalibratedSamples(),
		})
		require.NoError(t, err)
		assert.InDelta(t, 0.05, result.ECE, 1e-5)
		assert.True(t, result.Calibrated)
	})

	t.Run("A1 gatePromotion fails the miscalibrated binding and passes the calibrated one", func(t *testing.T) {
		miscalibrated, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       miscalibratedSamples(),
		})
		require.NoError(t, err)
		calibrated, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev-canary", Provider: calProvider("openrouter")},
			Samples:       wellCalibratedSamples(),
		})
		require.NoError(t, err)

		mixed := policy.GatePromotion([]policy.CalibrationResult{*miscalibrated, *calibrated})
		assert.False(t, mixed.Promotable)
		assert.Equal(t, []string{"jev#default"}, mixed.FailingKeys)
		assert.Equal(t, policy.PromotionGate{Promotable: true, FailingKeys: []string{}}, policy.GatePromotion([]policy.CalibrationResult{*calibrated}))
	})

	t.Run("fail-closed: a promotion with NO calibration evidence is not promotable", func(t *testing.T) {
		empty := policy.GatePromotion([]policy.CalibrationResult{})
		assert.False(t, empty.Promotable)
		assert.Empty(t, empty.FailingKeys)
		assert.Equal(t, []string{}, empty.FailingKeys)
	})

	t.Run("the A1 gate covers ECE AND latency: an ECE-passing binding that blows the latency budget is not promotable", func(t *testing.T) {
		// Well-calibrated ECE fixture, but the binding's p95 latency (5000ms) is
		// far above the declared 1000ms budget → calibrated false → the gate
		// fails the binding even though its ECE passes.
		latencySamples := make([]float64, 10)
		for i := range latencySamples {
			latencySamples[i] = 5000
		}
		ecePassesLatencyBlows, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:   17,
			Binding:         policy.CalibrationBinding{Model: "jev-slow"},
			Samples:         wellCalibratedSamples(),
			LatencySamples:  latencySamples,
			LatencyBudgetMs: calBudget(1000),
		})
		require.NoError(t, err)
		assert.LessOrEqual(t, ecePassesLatencyBlows.ECE, ecePassesLatencyBlows.Threshold)
		require.NotNil(t, ecePassesLatencyBlows.LatencyOK)
		assert.False(t, *ecePassesLatencyBlows.LatencyOK)
		assert.False(t, ecePassesLatencyBlows.Calibrated)

		gate := policy.GatePromotion([]policy.CalibrationResult{*ecePassesLatencyBlows})
		assert.False(t, gate.Promotable)
		assert.Equal(t, []string{"jev-slow#default"}, gate.FailingKeys)
	})
}

// TestLatencyBudgetGate covers 'A1 latency-budget gate: p95 (nearest-rank) gates
// `calibrated` alongside ECE'.
func TestLatencyBudgetGate(t *testing.T) {
	t.Run("p95 math pinned by hand: nearest-rank over sorted samples (n=20 → rank 19, NOT the max, NOT interpolated)", func(t *testing.T) {
		// n = 20 → rank = ceil(0.95 × 20) = 19 → the 19th smallest sample.
		// Eighteen samples at 10ms, the 19th at 20ms, the 20th at 999ms:
		// p95 = 20 (the observed rank-19 sample) — neither the maximum (999)
		// nor an interpolation between the top two values.
		twenty := make([]float64, 0, 20)
		for i := 0; i < 18; i++ {
			twenty = append(twenty, 10)
		}
		twenty = append(twenty, 20, 999)

		value, err := policy.P95LatencyMs(twenty)
		require.NoError(t, err)
		assert.Equal(t, 20.0, value)

		// Unsorted input is sorted first (order-independent).
		reversed := make([]float64, 0, len(twenty))
		for i := len(twenty) - 1; i >= 0; i-- {
			reversed = append(reversed, twenty[i])
		}
		value, err = policy.P95LatencyMs(reversed)
		require.NoError(t, err)
		assert.Equal(t, 20.0, value)

		// n = 4 → rank = ceil(3.8) = 4 → the maximum.
		value, err = policy.P95LatencyMs([]float64{400, 100, 300, 200})
		require.NoError(t, err)
		assert.Equal(t, 400.0, value)

		// n = 1 → rank 1 → the single sample.
		value, err = policy.P95LatencyMs([]float64{42})
		require.NoError(t, err)
		assert.Equal(t, 42.0, value)
	})

	t.Run("a fixture passing ECE but blowing the latency budget sets calibrated false with provenance fields", func(t *testing.T) {
		result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:   17,
			Binding:         policy.CalibrationBinding{Model: "jev"},
			Samples:         wellCalibratedSamples(),
			LatencySamples:  []float64{900, 1200, 1500, 1800, 2200, 2600, 3000, 3400, 3800, 4200},
			LatencyBudgetMs: calBudget(2000),
		})
		require.NoError(t, err)
		// ECE leg passes (≈ 0.05 ≤ 0.1)…
		assert.InDelta(t, 0.05, result.ECE, 1e-5)
		// …but the latency leg fails: p95 (nearest-rank, n=10 → rank 10) = 4200 > 2000.
		require.NotNil(t, result.P95LatencyMs)
		assert.Equal(t, 4200.0, *result.P95LatencyMs)
		require.NotNil(t, result.LatencyBudgetMs)
		assert.Equal(t, 2000.0, *result.LatencyBudgetMs)
		require.NotNil(t, result.LatencyOK)
		assert.False(t, *result.LatencyOK)
		assert.False(t, result.Calibrated)
	})

	t.Run("a fixture passing BOTH legs sets calibrated true; samples without a budget are reported but gate nothing", func(t *testing.T) {
		gated, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:   17,
			Binding:         policy.CalibrationBinding{Model: "jev"},
			Samples:         wellCalibratedSamples(),
			LatencySamples:  []float64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000},
			LatencyBudgetMs: calBudget(2000),
		})
		require.NoError(t, err)
		require.NotNil(t, gated.P95LatencyMs)
		assert.Equal(t, 1000.0, *gated.P95LatencyMs)
		require.NotNil(t, gated.LatencyBudgetMs)
		assert.Equal(t, 2000.0, *gated.LatencyBudgetMs)
		require.NotNil(t, gated.LatencyOK)
		assert.True(t, *gated.LatencyOK)
		assert.True(t, gated.Calibrated)

		reported, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:  17,
			Binding:        policy.CalibrationBinding{Model: "jev"},
			Samples:        wellCalibratedSamples(),
			LatencySamples: []float64{100, 200, 300, 400, 500, 600, 700, 800, 900, 5000},
		})
		require.NoError(t, err)
		// No budget → the p95 is reported for provenance, `latency_ok` is
		// absent, and the verdict is the ECE check alone (nothing to gate
		// latency against).
		require.NotNil(t, reported.P95LatencyMs)
		assert.Equal(t, 5000.0, *reported.P95LatencyMs)
		assert.Nil(t, reported.LatencyOK)
		assert.Nil(t, reported.LatencyBudgetMs)
		assert.True(t, reported.Calibrated)
	})

	t.Run("fail-closed: a budget without latency evidence, an empty latency set, and malformed inputs error", func(t *testing.T) {
		samples := wellCalibratedSamples()

		_, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:   17,
			Binding:         policy.CalibrationBinding{Model: "jev"},
			Samples:         samples,
			LatencyBudgetMs: calBudget(1000),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "latencyBudgetMs requires latencySamples")

		_, err = policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:  17,
			Binding:        policy.CalibrationBinding{Model: "jev"},
			Samples:        samples,
			LatencySamples: []float64{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty set has no p95")

		_, err = policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:  17,
			Binding:        policy.CalibrationBinding{Model: "jev"},
			Samples:        samples,
			LatencySamples: []float64{100, -5},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be finite numbers ≥ 0 (got -5)")

		_, err = policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:  17,
			Binding:        policy.CalibrationBinding{Model: "jev"},
			Samples:        samples,
			LatencySamples: []float64{100, math.NaN()},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be finite numbers ≥ 0 (got NaN)")

		_, err = policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:   17,
			Binding:         policy.CalibrationBinding{Model: "jev"},
			Samples:         samples,
			LatencySamples:  []float64{100},
			LatencyBudgetMs: calBudget(-1),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "latencyBudgetMs must be a finite non-negative number (got -1)")

		_, err = policy.P95LatencyMs([]float64{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "latency p95 requires at least one sample")
	})
}

// TestCalibrationFailClosedValidation covers 'fail-closed validation (a silent
// pass would be fail-open for the promotion gate)'.
func TestCalibrationFailClosedValidation(t *testing.T) {
	t.Run("rejects an empty sample set", func(t *testing.T) {
		_, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       []policy.CalibrationSample{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one sample — an empty split must not pass the promotion gate")
	})

	t.Run("rejects out-of-range probabilities", func(t *testing.T) {
		_, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       []policy.CalibrationSample{calSample(1.5, true)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sample.probability must be a finite number in [0, 1] (got 1.5)")

		_, err = policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       []policy.CalibrationSample{calSample(-0.25, true)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sample.probability must be a finite number in [0, 1] (got -0.25)")

		// TS: `{ probability: 0.5, correct: 'yes' as unknown as boolean }` throws.
		// A non-boolean `correct` is unrepresentable in Go's typed
		// CalibrationSample, so the type system carries that guard.
	})

	t.Run("rejects malformed bins, thresholds, policy versions and binding keys", func(t *testing.T) {
		samples := []policy.CalibrationSample{calSample(0.5, true)}
		base := policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       samples,
		}

		_, err := policy.RunCalibrationHarness(withBins(base, 0))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bins must be a positive integer (got 0)")

		_, err = policy.RunCalibrationHarness(withThreshold(base, 1.5))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "threshold must be a finite number in [0, 1] (got 1.5)")

		_, err = policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 0,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       samples,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "policyVersion must be a positive integer (got 0)")

		// The binding key is required before any sample is binned.
		_, err = policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: ""},
			Samples:       samples,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binding.model must be a non-empty string")
	})

	t.Run("the boundary probability 1.0 lands in the last bin (equal-width over [0, 1])", func(t *testing.T) {
		result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       []policy.CalibrationSample{calSample(1, true)},
			Bins:          calBins(10),
		})
		require.NoError(t, err)
		assert.Equal(t, 1, result.PerBin[9].Count)
		assert.Equal(t, 1.0, result.PerBin[9].Accuracy)
		assert.Equal(t, 1.0, result.PerBin[9].AvgConfidence)
	})
}

// TestCalibrationGatePromotionRows pins the A1 gate's dedup and per-binding row
// fan-out over several bindings.
func TestCalibrationGatePromotionRows(t *testing.T) {
	t.Run("the failing key is listed once even when several results fail for the same binding", func(t *testing.T) {
		miscalibrated, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       miscalibratedSamples(),
		})
		require.NoError(t, err)
		gate := policy.GatePromotion([]policy.CalibrationResult{*miscalibrated, *miscalibrated})
		assert.False(t, gate.Promotable)
		assert.Equal(t, []string{"jev#default"}, gate.FailingKeys)
	})

	t.Run("one row per binding, each gated on its own calibration", func(t *testing.T) {
		failing, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion: 17,
			Binding:       policy.CalibrationBinding{Model: "jev"},
			Samples:       miscalibratedSamples(),
		})
		require.NoError(t, err)
		passing, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:   17,
			Binding:         policy.CalibrationBinding{Model: "jev-canary", Provider: calProvider("openrouter")},
			Samples:         wellCalibratedSamples(),
			LatencySamples:  []float64{120, 140},
			LatencyBudgetMs: calBudget(200),
		})
		require.NoError(t, err)

		rows := policy.BindingCalibrationGateRows([]policy.CalibrationResult{*failing, *passing})
		require.Len(t, rows, 2)
		assert.Equal(t, "binding_calibration:jev#default", rows[0].Gate)
		assert.False(t, rows[0].Passed)
		assert.Equal(t, "binding_calibration:jev-canary#openrouter", rows[1].Gate)
		assert.True(t, rows[1].Passed)
		assert.Equal(t, "EDOCSYNC_VALIDATION", docutil.CodeOf(calibrationEvidenceMismatchErr()))
	})
}

// calibrationEvidenceMismatchErr returns the coded error the harness uses for a
// malformed input, so the error-code contract is pinned.
func calibrationEvidenceMismatchErr() error {
	_, err := policy.BindingKey(policy.CalibrationBinding{Model: ""})
	return err
}

func withBins(input policy.RunCalibrationHarnessInput, bins int) policy.RunCalibrationHarnessInput {
	input.Bins = &bins
	return input
}

func withThreshold(input policy.RunCalibrationHarnessInput, threshold float64) policy.RunCalibrationHarnessInput {
	input.Threshold = &threshold
	return input
}
