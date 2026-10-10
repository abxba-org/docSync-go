package policy_test

// Ported from packages/policy/test/binding-calibration-p12-t09.test.ts
// (P12-T09; ARCH §10.8; D3/R18): a binding change failing calibration on the
// evaluation split cannot promote; displayed confidence is pinned to
// (policy_version, model binding).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/policy"
)

var bindingCalibrationBinding = policy.CalibrationBinding{
	Model:    "jev",
	Provider: calProvider("openrouter"),
}

// Evaluation-split samples: asserted 0.95 confidence, only 50% correct → ECE 0.45
// (badly calibrated).
func bindingCalibrationFailingSamples() []policy.CalibrationSample {
	samples := make([]policy.CalibrationSample, 0, 20)
	for i := 0; i < 20; i++ {
		samples = append(samples, calSample(0.95, i < 10))
	}
	return samples
}

// Evaluation-split samples: asserted 0.95 confidence, 100% correct → ECE 0
// (calibrated).
func bindingCalibrationPassingSamples() []policy.CalibrationSample {
	samples := make([]policy.CalibrationSample, 0, 20)
	for i := 0; i < 20; i++ {
		samples = append(samples, calSample(0.95, true))
	}
	return samples
}

func bindingCalibrationFailingResult(policyVersion int) *policy.CalibrationResult {
	result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
		PolicyVersion: policyVersion,
		Binding:       bindingCalibrationBinding,
		Samples:       bindingCalibrationFailingSamples(),
	})
	if err != nil {
		panic(err)
	}
	return result
}

func bindingCalibrationPassingResult(policyVersion int) *policy.CalibrationResult {
	result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
		PolicyVersion:   policyVersion,
		Binding:         bindingCalibrationBinding,
		Samples:         bindingCalibrationPassingSamples(),
		LatencySamples:  []float64{120, 140},
		LatencyBudgetMs: calBudget(200),
	})
	if err != nil {
		panic(err)
	}
	return result
}

// TestP12T09BindingChangeFailingCalibration covers 'P12-T09: a binding change
// failing calibration on the eval split cannot promote (typed not-promotable
// verdict)'.
func TestP12T09BindingChangeFailingCalibration(t *testing.T) {
	t.Run("returns a typed not-promotable verdict naming the binding, the gate and the ECE failure", func(t *testing.T) {
		verdict, err := policy.EvaluateBindingCalibration(policy.EvaluateBindingCalibrationInput{
			PolicyVersion: 18,
			Binding:       bindingCalibrationBinding,
			Result:        bindingCalibrationFailingResult(18),
		})
		require.NoError(t, err)
		require.NotNil(t, verdict)
		assert.False(t, verdict.Promotable)
		assert.Equal(t, policy.BindingCalibrationReasonFailed, verdict.Reason)
		assert.Equal(t, policy.BindingCalibrationGateName, verdict.Gate)
		assert.Equal(t, "jev#openrouter", verdict.BindingKey)
		assert.Contains(t, verdict.Detail, "jev#openrouter")
		assert.Contains(t, verdict.Detail, "cannot promote")
		assert.Contains(t, verdict.Detail, "FAILED")
		// Typed discriminant: the failing verdict carries no promotable path.
		assert.Equal(t, "policy v18 · model jev#openrouter", verdict.PinnedConfidenceLabel)
	})

	t.Run("fails closed on MISSING evidence — no calibration evidence means not promotable, never a pass", func(t *testing.T) {
		verdict, err := policy.EvaluateBindingCalibration(policy.EvaluateBindingCalibrationInput{
			PolicyVersion: 18,
			Binding:       bindingCalibrationBinding,
		})
		require.NoError(t, err)
		require.NotNil(t, verdict)
		assert.False(t, verdict.Promotable)
		assert.Equal(t, policy.BindingCalibrationReasonNoEvidence, verdict.Reason)
		assert.Contains(t, verdict.Detail, "NO evaluation-split calibration evidence")
	})

	t.Run("rejects evidence pinned to a different (policy_version, binding) key (D3 fail closed)", func(t *testing.T) {
		_, err := policy.EvaluateBindingCalibration(policy.EvaluateBindingCalibrationInput{
			PolicyVersion: 18,
			Binding:       bindingCalibrationBinding,
			Result:        bindingCalibrationFailingResult(17),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evidence mismatch")

		_, err = policy.EvaluateBindingCalibration(policy.EvaluateBindingCalibrationInput{
			PolicyVersion: 18,
			Binding:       bindingCalibrationBinding,
			Result: func() *policy.CalibrationResult {
				result, runErr := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
					PolicyVersion: 18,
					Binding:       policy.CalibrationBinding{Model: "jev"},
					Samples:       bindingCalibrationPassingSamples(),
				})
				if runErr != nil {
					panic(runErr)
				}
				return result
			}(),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evidence mismatch")
	})

	t.Run("produces failing gate rows for the promotion checklist seam (structurally GateRow)", func(t *testing.T) {
		rows := policy.BindingCalibrationGateRows([]policy.CalibrationResult{*bindingCalibrationFailingResult(18)})
		require.Len(t, rows, 1)
		assert.Equal(t, "binding_calibration:jev#openrouter", rows[0].Gate)
		assert.False(t, rows[0].Passed)
		assert.Contains(t, rows[0].Detail, "not promotable")
	})

	t.Run("an empty evidence list yields a single failing binding_calibration row (fail closed)", func(t *testing.T) {
		rows := policy.BindingCalibrationGateRows([]policy.CalibrationResult{})
		require.Len(t, rows, 1)
		assert.Equal(t, policy.BindingCalibrationGateName, rows[0].Gate)
		assert.False(t, rows[0].Passed)
	})

	t.Run("names both failed legs in the verdict detail when ECE and latency both fail", func(t *testing.T) {
		result, err := policy.RunCalibrationHarness(policy.RunCalibrationHarnessInput{
			PolicyVersion:   18,
			Binding:         bindingCalibrationBinding,
			Samples:         bindingCalibrationFailingSamples(),
			LatencySamples:  []float64{120, 140},
			LatencyBudgetMs: calBudget(100),
		})
		require.NoError(t, err)
		require.NotNil(t, result.LatencyOK)
		assert.False(t, *result.LatencyOK)
		assert.False(t, result.Calibrated)

		verdict, err := policy.EvaluateBindingCalibration(policy.EvaluateBindingCalibrationInput{
			PolicyVersion: 18,
			Binding:       bindingCalibrationBinding,
			Result:        result,
		})
		require.NoError(t, err)
		assert.False(t, verdict.Promotable)
		assert.Equal(t, policy.BindingCalibrationReasonFailed, verdict.Reason)
		assert.Contains(t, verdict.Detail, "latency over budget")
		assert.Contains(t, verdict.Detail, "ECE")
	})

	t.Run("rejects a non-positive policy version", func(t *testing.T) {
		_, err := policy.EvaluateBindingCalibration(policy.EvaluateBindingCalibrationInput{
			PolicyVersion: 0,
			Binding:       bindingCalibrationBinding,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "policyVersion must be a positive integer (got 0)")
	})

	t.Run("rejects an empty model binding by name (fail closed, never a silent key)", func(t *testing.T) {
		_, err := policy.EvaluateBindingCalibration(policy.EvaluateBindingCalibrationInput{
			PolicyVersion: 18,
			Binding:       policy.CalibrationBinding{Model: ""},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binding.model must be a non-empty string")

		_, err = policy.PinnedConfidenceLabel(18, policy.CalibrationBinding{Model: ""})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binding.model must be a non-empty string")

		_, err = policy.PinnedConfidenceLabel(0, bindingCalibrationBinding)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "policyVersion must be a positive integer (got 0)")
	})
}

// TestP12T09BindingChangePassingCalibration covers 'P12-T09: a passing binding
// change yields a promotable verdict with (policy_version, model
// binding)-pinned confidence'.
func TestP12T09BindingChangePassingCalibration(t *testing.T) {
	t.Run("returns a promotable verdict whose confidence label is pinned to policy version + binding (D3)", func(t *testing.T) {
		verdict, err := policy.EvaluateBindingCalibration(policy.EvaluateBindingCalibrationInput{
			PolicyVersion: 18,
			Binding:       bindingCalibrationBinding,
			Result:        bindingCalibrationPassingResult(18),
		})
		require.NoError(t, err)
		require.NotNil(t, verdict)
		assert.True(t, verdict.Promotable)
		assert.Empty(t, verdict.Reason)
		assert.Equal(t, policy.BindingCalibrationGateName, verdict.Gate)
		assert.Equal(t, "jev#openrouter", verdict.BindingKey)
		assert.Equal(t, "policy v18 · model jev#openrouter", verdict.PinnedConfidenceLabel)
		assert.Contains(t, verdict.Detail, "ECE")
		assert.Contains(t, verdict.Detail, "latency_ok true")
		assert.Contains(t, verdict.Detail, "promotable")
	})

	t.Run("the pin fragment is a pure format contract over (policy_version, bindingKey)", func(t *testing.T) {
		label, err := policy.PinnedConfidenceLabel(17, policy.CalibrationBinding{Model: "jev"})
		require.NoError(t, err)
		assert.Equal(t, "policy v17 · model jev#default", label)

		label, err = policy.PinnedConfidenceLabel(18, bindingCalibrationBinding)
		require.NoError(t, err)
		assert.Equal(t, "policy v18 · model jev#openrouter", label)

		key, err := policy.BindingKey(bindingCalibrationBinding)
		require.NoError(t, err)
		assert.Equal(t, "jev#openrouter", key)
	})

	t.Run("the full display projection pins the confidence percentage to version + binding", func(t *testing.T) {
		display, err := policy.PinnedConfidenceDisplay(0.916, 18, bindingCalibrationBinding)
		require.NoError(t, err)
		assert.Equal(t, "92% (policy v18 · model jev#openrouter)", display)

		display, err = policy.PinnedConfidenceDisplay(0, 1, policy.CalibrationBinding{Model: "jev"})
		require.NoError(t, err)
		assert.Equal(t, "0% (policy v1 · model jev#default)", display)

		display, err = policy.PinnedConfidenceDisplay(1, 17, policy.CalibrationBinding{Model: "jev"})
		require.NoError(t, err)
		assert.Equal(t, "100% (policy v17 · model jev#default)", display)

		_, err = policy.PinnedConfidenceDisplay(1.5, 18, bindingCalibrationBinding)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "confidence must be a finite number in [0, 1] (got 1.5)")

		_, err = policy.PinnedConfidenceDisplay(0.5, 0, bindingCalibrationBinding)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "policyVersion must be a positive integer (got 0)")
	})

	t.Run("passing evidence yields a passing gate row for the checklist seam", func(t *testing.T) {
		rows := policy.BindingCalibrationGateRows([]policy.CalibrationResult{*bindingCalibrationPassingResult(18)})
		require.Len(t, rows, 1)
		assert.Equal(t, "binding_calibration:jev#openrouter", rows[0].Gate)
		assert.True(t, rows[0].Passed)
	})
}
