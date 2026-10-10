// Ported from packages/policy/test/metrics-health.test.ts
// (metrics/health.ts assertions).
package policy_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
)

// healthCounts builds the TS fixture: policy v17 with 7 abstains, 12
// overrides over 100 decisions.
func healthCounts(overrides map[string]float64) policy.HealthCounts {
	counts := policy.HealthCounts{
		PolicyVersion: 17,
		Abstains:      7,
		Decisions:     100,
		Overrides:     12,
	}
	for key, value := range overrides {
		switch key {
		case "policyVersion":
			counts.PolicyVersion = value
		case "abstains":
			counts.Abstains = value
		case "decisions":
			counts.Decisions = value
		case "overrides":
			counts.Overrides = value
		}
	}
	return counts
}

func mustRecordHealth(t *testing.T, counts policy.HealthCounts) policy.HealthMetrics {
	t.Helper()
	metrics, err := policy.RecordHealthMetrics(counts)
	require.NoError(t, err)
	return metrics
}

// TestHealthMetricsRecordsRatesPerPolicyVersion ports "records abstain_rate
// and override_rate as counts/total with the policy version key".
func TestHealthMetricsRecordsRatesPerPolicyVersion(t *testing.T) {
	metrics := mustRecordHealth(t, healthCounts(nil))

	assert.Equal(t, policy.HealthMetrics{
		PolicyVersion: 17,
		AbstainRate:   0.07,
		OverrideRate:  0.12,
		Total:         100,
	}, metrics)
}

// TestHealthMetricsZeroDivisionSafe ports "is zero-division safe: an empty
// cohort yields rates of exactly 0, never NaN".
func TestHealthMetricsZeroDivisionSafe(t *testing.T) {
	metrics := mustRecordHealth(t, healthCounts(map[string]float64{
		"abstains": 0, "decisions": 0, "overrides": 0,
	}))

	assert.Equal(t, 0, metrics.Total)
	assert.Equal(t, float64(0), metrics.AbstainRate)
	assert.Equal(t, float64(0), metrics.OverrideRate)
	assert.False(t, math.IsNaN(metrics.AbstainRate))
	assert.False(t, math.IsNaN(metrics.OverrideRate))
}

// TestHealthMetricsRejectsMalformedCounts ports "rejects malformed counts
// (fail-closed, deterministic programmer errors)".
func TestHealthMetricsRejectsMalformedCounts(t *testing.T) {
	for name, counts := range map[string]policy.HealthCounts{
		"negative abstains":         healthCounts(map[string]float64{"abstains": -1}),
		"fractional decisions":      healthCounts(map[string]float64{"decisions": 1.5}),
		"zero policy version":       healthCounts(map[string]float64{"policyVersion": 0}),
		"negative policy version":   healthCounts(map[string]float64{"policyVersion": -3}),
		"fractional policy version": healthCounts(map[string]float64{"policyVersion": 1.5}),
		"NaN overrides":             healthCounts(map[string]float64{"overrides": math.NaN()}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := policy.RecordHealthMetrics(counts)
			require.Error(t, err)
			assert.Equal(t, "EDOCSYNC_VALIDATION", docutil.CodeOf(err))
		})
	}
}

// TestHealthBoundsLabeledBootstrapDefaults ports "exports labeled bootstrap
// defaults (documented, not load-bearing)".
func TestHealthBoundsLabeledBootstrapDefaults(t *testing.T) {
	require.NotNil(t, policy.AbstainRateHealthDefaultBounds.MaxAbstainRate)
	require.NotNil(t, policy.AbstainRateHealthDefaultBounds.MinOverrideRate)
	assert.InDelta(t, 0.35, *policy.AbstainRateHealthDefaultBounds.MaxAbstainRate, 1e-12)
	assert.InDelta(t, 0, *policy.AbstainRateHealthDefaultBounds.MinOverrideRate, 1e-12)
}

// TestHealthBoundsHealthyVersionPassesDefaults ports "passes a healthy version
// against the bootstrap defaults".
func TestHealthBoundsHealthyVersionPassesDefaults(t *testing.T) {
	evaluation, err := policy.EvaluateHealthBounds(mustRecordHealth(t, healthCounts(nil)), nil)
	require.NoError(t, err)
	assert.True(t, evaluation.Pass)
	assert.Empty(t, evaluation.Violations)
}

// TestHealthBoundsGapRiddenAbstainRateNamesTheBound ports "fails a gap-ridden
// abstain rate against the bootstrap max and names the bound".
func TestHealthBoundsGapRiddenAbstainRateNamesTheBound(t *testing.T) {
	metrics := mustRecordHealth(t, healthCounts(map[string]float64{"abstains": 50}))
	evaluation, err := policy.EvaluateHealthBounds(metrics, nil)
	require.NoError(t, err)

	assert.False(t, evaluation.Pass)
	require.Len(t, evaluation.Violations, 1)
	assert.Contains(t, evaluation.Violations[0], "abstain_rate")
	assert.Contains(t, evaluation.Violations[0], "0.5")
	assert.Contains(t, evaluation.Violations[0], "v17")
}

// TestHealthBoundsHonorsCustomBounds ports "honors explicit custom bounds over
// the defaults (bounds are per-version calibrated data)".
func TestHealthBoundsHonorsCustomBounds(t *testing.T) {
	metrics := mustRecordHealth(t, healthCounts(map[string]float64{"abstains": 10, "overrides": 12}))

	tight, err := policy.EvaluateHealthBounds(metrics, &policy.HealthBounds{MaxAbstainRate: evalFloatPtr(0.05)})
	require.NoError(t, err)
	assert.False(t, tight.Pass)

	exact, err := policy.EvaluateHealthBounds(metrics, &policy.HealthBounds{MaxAbstainRate: evalFloatPtr(0.1)})
	require.NoError(t, err)
	assert.True(t, exact.Pass)

	highFloor, err := policy.EvaluateHealthBounds(metrics, &policy.HealthBounds{MinOverrideRate: evalFloatPtr(0.2)})
	require.NoError(t, err)
	assert.False(t, highFloor.Pass)

	exactFloor, err := policy.EvaluateHealthBounds(metrics, &policy.HealthBounds{MinOverrideRate: evalFloatPtr(0.12)})
	require.NoError(t, err)
	assert.True(t, exactFloor.Pass)
}

// TestHealthBoundsBoundariesAreInclusive ports "boundaries are inclusive (a
// rate exactly at the bound passes)".
func TestHealthBoundsBoundariesAreInclusive(t *testing.T) {
	metrics := mustRecordHealth(t, healthCounts(map[string]float64{"abstains": 35, "overrides": 0}))
	evaluation, err := policy.EvaluateHealthBounds(metrics, nil)
	require.NoError(t, err)
	assert.True(t, evaluation.Pass)
}

// TestHealthBoundsRejectsMalformedBounds ports "rejects malformed bounds
// (fail-closed)".
func TestHealthBoundsRejectsMalformedBounds(t *testing.T) {
	metrics := mustRecordHealth(t, healthCounts(nil))

	for name, bounds := range map[string]*policy.HealthBounds{
		"maxAbstainRate above 1":   {MaxAbstainRate: evalFloatPtr(1.5)},
		"maxAbstainRate negative":  {MaxAbstainRate: evalFloatPtr(-0.1)},
		"maxAbstainRate NaN":       {MaxAbstainRate: healthNanPtr()},
		"maxAbstainRate infinite":  {MaxAbstainRate: healthInfPtr()},
		"minOverrideRate negative": {MinOverrideRate: evalFloatPtr(-0.1)},
		"minOverrideRate above 1":  {MinOverrideRate: evalFloatPtr(2)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := policy.EvaluateHealthBounds(metrics, bounds)
			require.Error(t, err)
			assert.Equal(t, "EDOCSYNC_VALIDATION", docutil.CodeOf(err))
		})
	}
}

// TestHealthViolationNamesOverrideRate pins the symmetric violation record for
// the override-rate bound (the TS module's second violation text).
func TestHealthViolationNamesOverrideRate(t *testing.T) {
	metrics := mustRecordHealth(t, healthCounts(map[string]float64{"overrides": 1}))
	evaluation, err := policy.EvaluateHealthBounds(metrics, &policy.HealthBounds{
		MinOverrideRate: evalFloatPtr(0.05),
	})
	require.NoError(t, err)
	assert.False(t, evaluation.Pass)
	require.Len(t, evaluation.Violations, 1)
	assert.Contains(t, evaluation.Violations[0], "override_rate 0.01 below minOverrideRate 0.05")
	assert.Contains(t, evaluation.Violations[0], "overconfidence signal (§5.2, R11)")
	assert.Contains(t, evaluation.Violations[0], "v17")
}

// TestHealthEvaluationViolationsAreEmptySliceWhenPassing pins the
// toStrictEqual([]) shape of the TS suite.
func TestHealthEvaluationViolationsAreEmptySliceWhenPassing(t *testing.T) {
	evaluation, err := policy.EvaluateHealthBounds(mustRecordHealth(t, healthCounts(nil)), nil)
	require.NoError(t, err)
	assert.Equal(t, policy.HealthEvaluation{Pass: true, Violations: nil}, evaluation)
}

func healthNanPtr() *float64 { v := math.NaN(); return &v }
func healthInfPtr() *float64 { v := math.Inf(1); return &v }
