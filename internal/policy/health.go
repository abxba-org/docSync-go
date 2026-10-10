// Ported from packages/policy/src/metrics/health.ts.

package policy

import (
	"fmt"
	"math"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// HealthCounts are the inputs to health recording — raw counts from the
// review/override feedback path (HealthCounts; ARCH §8.7).
//
// The counts are float64, mirroring the TS `number` fields: the TS module
// rejects non-integer and NaN counts fail-closed (Number.isInteger), and that
// validation matrix is only expressible in Go with a numeric input type. The
// emitted HealthMetrics carries int counts.
type HealthCounts struct {
	// PolicyVersion is the policy version these counts belong to (§8.4
	// attribution).
	PolicyVersion float64 `json:"policyVersion"`
	// Abstains counts decisions that abstained (any §5.2 decision-level or
	// operational reason).
	Abstains float64 `json:"abstains"`
	// Decisions is the total decided cases in the cohort (abstains and
	// overrides are subsets).
	Decisions float64 `json:"decisions"`
	// Overrides counts decisions a human overrode (review-queue resolutions
	// that changed the outcome).
	Overrides float64 `json:"overrides"`
}

// HealthMetrics is the 2-D health metric struct per policy version
// (HealthMetrics; ARCH §16.2 R11).
type HealthMetrics struct {
	PolicyVersion int `json:"policy_version"`
	// AbstainRate is abstains / decisions — 0 when the cohort is empty
	// (zero-division safe).
	AbstainRate float64 `json:"abstain_rate"`
	// OverrideRate is overrides / decisions — 0 when the cohort is empty
	// (zero-division safe).
	OverrideRate float64 `json:"override_rate"`
	// Total is the cohort size the rates were computed over (0 on empty —
	// never NaN).
	Total int `json:"total"`
}

// AbstainRateHealthDefaultBounds is BOOTSTRAP DATA (not architecture — D11
// spirit): default health bounds, deliberately conservative placeholders until
// a repository calibrates real bounds on its evaluation split (§8.4).
// `maxAbstainRate: 0.35` — above a third abstaining, the policy is gap-ridden
// and needs the architect loop (§8.1), not more traffic.
// `minOverrideRate: 0` — no floor until real data exists; the overconfidence
// pathology (low abstain + high override, §5.2) is a 2-D read Phase 10 owns.
// These values are DATA: pass explicit bounds in production; never treat
// these defaults as load-bearing thresholds
// (ABSTAIN_RATE_HEALTH_DEFAULT_BOUNDS).
var AbstainRateHealthDefaultBounds = HealthBounds{
	MaxAbstainRate:  healthFloat(0.35),
	MinOverrideRate: healthFloat(0),
}

// HealthBounds are per-dimension health bounds — DATA parameters, never
// hard-coded (HealthBounds; D11 spirit).
type HealthBounds struct {
	// MaxAbstainRate is the abstain rate at or below which the policy version
	// is healthy on this dimension (nil ⇒ the bound is not evaluated).
	MaxAbstainRate *float64 `json:"maxAbstainRate,omitempty"`
	// MinOverrideRate is the override rate at or above which the policy
	// version is healthy on this dimension (nil ⇒ not evaluated).
	MinOverrideRate *float64 `json:"minOverrideRate,omitempty"`
}

// HealthEvaluation is the per-dimension bound evaluation (HealthEvaluation).
type HealthEvaluation struct {
	// Pass is true iff no configured bound is violated.
	Pass bool `json:"pass"`
	// Violations carries the machine-readable violation records, one per
	// failed bound (provenance, §5.2).
	Violations []string `json:"violations"`
}

// healthFloat returns a pointer to v (the package-local pointer helper — the
// shared pointer helpers of other packages are unexported and not importable).
func healthFloat(v float64) *float64 { return &v }

// assertCount reproduces the TS assertCount: a non-integer or negative count
// is a deterministic programmer error and fails closed.
func assertCount(value float64, name string) error {
	if !isJSInteger(value) || value < 0 {
		return docutil.Newf(docutil.CodeValidation,
			"health counts require a non-negative integer %s (got %s)", name, jsString(value))
	}
	return nil
}

// isJSInteger reports Number.isInteger semantics (false for NaN, ±Inf and
// fractional values).
func isJSInteger(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v == math.Trunc(v)
}

// RecordHealthMetrics records the 2-D health metrics for one policy version
// from raw counts (recordHealthMetrics). Rates are counts/total; an empty
// cohort yields rates of exactly 0 (never NaN — a division by zero would
// poison every downstream comparison).
func RecordHealthMetrics(counts HealthCounts) (HealthMetrics, error) {
	if err := assertCount(counts.Abstains, "abstains count"); err != nil {
		return HealthMetrics{}, err
	}
	if err := assertCount(counts.Decisions, "decisions count"); err != nil {
		return HealthMetrics{}, err
	}
	if err := assertCount(counts.Overrides, "overrides count"); err != nil {
		return HealthMetrics{}, err
	}
	if !isJSInteger(counts.PolicyVersion) || counts.PolicyVersion <= 0 {
		return HealthMetrics{}, docutil.Newf(docutil.CodeValidation,
			"health metrics require a positive-integer policy_version (got %s)",
			jsString(counts.PolicyVersion))
	}
	total := int(counts.Decisions)
	return HealthMetrics{
		PolicyVersion: int(counts.PolicyVersion),
		AbstainRate:   rateOrZero(counts.Abstains, total),
		OverrideRate:  rateOrZero(counts.Overrides, total),
		Total:         total,
	}, nil
}

// rateOrZero is the zero-division-safe rate: 0 when the cohort is empty.
func rateOrZero(count float64, total int) float64 {
	if total == 0 {
		return 0
	}
	return count / float64(total)
}

// EvaluateHealthBounds evaluates a policy version's health against DATA bounds
// (evaluateHealthBounds; default: AbstainRateHealthDefaultBounds). Per-dimension
// only — the 2-D pathology read (§5.2: low abstain rate combined with high
// override rate) is Phase 10's combination analysis. Bounds are inclusive: a
// rate exactly at maxAbstainRate or minOverrideRate passes. A nil bounds
// pointer selects the documented bootstrap defaults.
func EvaluateHealthBounds(metrics HealthMetrics, bounds *HealthBounds) (HealthEvaluation, error) {
	if bounds == nil {
		bounds = &AbstainRateHealthDefaultBounds
	}
	var violations []string
	if bounds.MaxAbstainRate != nil {
		maxAbstainRate := *bounds.MaxAbstainRate
		if !isFiniteNumber(maxAbstainRate) || maxAbstainRate < 0 || maxAbstainRate > 1 {
			return HealthEvaluation{}, docutil.Newf(docutil.CodeValidation,
				"maxAbstainRate must be a finite number in [0, 1] (got %s)", jsString(maxAbstainRate))
		}
		if metrics.AbstainRate > maxAbstainRate {
			violations = append(violations, fmt.Sprintf(
				"abstain_rate %s exceeds maxAbstainRate %s for policy v%d — policy gap signal (§5.2, R11)",
				jsString(metrics.AbstainRate), jsString(maxAbstainRate), metrics.PolicyVersion))
		}
	}
	if bounds.MinOverrideRate != nil {
		minOverrideRate := *bounds.MinOverrideRate
		if !isFiniteNumber(minOverrideRate) || minOverrideRate < 0 || minOverrideRate > 1 {
			return HealthEvaluation{}, docutil.Newf(docutil.CodeValidation,
				"minOverrideRate must be a finite number in [0, 1] (got %s)", jsString(minOverrideRate))
		}
		if metrics.OverrideRate < minOverrideRate {
			violations = append(violations, fmt.Sprintf(
				"override_rate %s below minOverrideRate %s for policy v%d — overconfidence signal (§5.2, R11)",
				jsString(metrics.OverrideRate), jsString(minOverrideRate), metrics.PolicyVersion))
		}
	}
	return HealthEvaluation{Pass: len(violations) == 0, Violations: violations}, nil
}

// isFiniteNumber reports Number.isFinite semantics.
func isFiniteNumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
