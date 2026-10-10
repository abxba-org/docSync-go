// Ported from core/src/evaluation/objective.ts (P10-T12; ARCH §9/§5.2; CTX
// §36; V-P10-10): the improvement-objective scorer.
//
// The composite objective the policy-improvement loop maximizes:
//
//	score = 0.40*precision + 0.30*recall + 0.20*patch_correctness
//	      + 0.10*efficiency
//
// The default weights are versioned data (D11-adjacent) and the weights are
// REPO-CONFIGURABLE — but only within a closed contract: every weight is a
// finite non-negative number and the four MUST sum to exactly 1.0 (within a
// declared float tolerance). Anything else refuses the computation (fail
// closed: a mis-specified objective must never silently skew promotion).
//
// Efficiency comes from OpenRouter usage accounting (P5-T17/ARCH §10.6 — the
// usage numbers are the SOURCE, never estimated): the normalized penalty is
// the mean of the cost share and the latency share against exported ceiling
// constants, so `efficiency = 1 - (costShare + latencyShare) / 2`. Both shares
// clamp at 1 — a runaway cost degrades efficiency to 0, never below, and never
// boosts another term.
//
// High-risk gate (ARCH §8.4): `unsafe_auto_updates > 0` collapses the score to
// 0 and fails the gate — the improvement objective is never allowed to
// launder unsafe auto-update behavior into a promotable number.
package evaluation

import (
	"fmt"
	"math"
)

// ObjectiveWeights is the closed weight set of the improvement objective
// (CTX §36).
type ObjectiveWeights struct {
	// Precision weights report `metrics.precision`.
	Precision float64
	// Recall weights report `metrics.detection_recall`.
	Recall float64
	// PatchCorrectness weights report `metrics.patch_correctness`.
	PatchCorrectness float64
	// Efficiency weights the usage-accounting efficiency term.
	Efficiency float64
}

// DefaultObjectiveWeights are the default CTX §36 weights — versioned data,
// overridable per repository.
var DefaultObjectiveWeights = ObjectiveWeights{
	Precision:        0.4,
	Recall:           0.3,
	PatchCorrectness: 0.2,
	Efficiency:       0.1,
}

// ObjectiveWeightSumTolerance is the float tolerance for the
// weights-sum-to-1 contract (declared, exported).
const ObjectiveWeightSumTolerance = 1e-9

// EfficiencyCostCeilingUSD is the cost ceiling (USD) for the efficiency
// normalization — versioned data (D11-adjacent): total run cost at or above
// this clamps the cost share to 1.
const EfficiencyCostCeilingUSD = 10

// EfficiencyLatencyCeilingMs is the latency ceiling (ms) for the efficiency
// normalization — versioned data: total run latency at or above this clamps
// the latency share to 1.
const EfficiencyLatencyCeilingMs = 30_000

// UnsafeAutoUpdatesGate is the declared gate-metric name (ARCH §8.4
// unsafe-auto-updates promotion gate).
const UnsafeAutoUpdatesGate = "unsafe_auto_updates"

// UsageAccounting is the OpenRouter usage-accounting aggregate for one
// evaluation window.
type UsageAccounting struct {
	// TotalCostUsd is the total billed USD from the gateway usage records
	// (the source, never estimated).
	TotalCostUsd float64
	// TotalLatencyMs is the total measured latency in ms across the window.
	TotalLatencyMs float64
}

// ObjectiveMetricsInput is the report metrics the objective consumes (CTX §36
// terms).
type ObjectiveMetricsInput struct {
	// DetectionRecall is report `metrics.detection_recall` (0..1).
	DetectionRecall float64
	// Precision is report `metrics.precision` (0..1).
	Precision float64
	// PatchCorrectness is report `metrics.patch_correctness` (0..1).
	PatchCorrectness float64
}

// ComputeObjectiveScoreInput carries the metrics, repo-configurable weights
// (nil ⇒ DefaultObjectiveWeights), the usage accounting and the unsafe
// auto-update gate count.
type ComputeObjectiveScoreInput struct {
	// Metrics is the six-metric block (the three scored terms are consumed).
	Metrics ObjectiveMetricsInput
	// Weights are the repo-configurable weights; nil ⇒ DefaultObjectiveWeights.
	Weights *ObjectiveWeights
	// Usage is the OpenRouter usage accounting for the window (efficiency
	// SOURCE).
	Usage UsageAccounting
	// UnsafeAutoUpdates counts unsafe auto updates observed in the window
	// (gate metric).
	UnsafeAutoUpdates int
}

// ObjectiveScoreResult carries the composite score, the collapse flag and the
// failed-gate name (present, and UnsafeAutoUpdatesGate, only when the gate
// failed).
type ObjectiveScoreResult struct {
	// Score is the composite score in 0..1 (0 when the high-risk gate
	// collapsed it).
	Score float64
	// Collapsed is true when the unsafe-auto-updates gate collapsed the score.
	Collapsed bool
	// FailedGate is present (and 'unsafe_auto_updates') only when the gate
	// failed.
	FailedGate *string
}

func requireUnitInterval(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("computeObjectiveScore: %s must be in 0..1, got %s", name, jsNumber(value))
	}
	return nil
}

func requireNonNegative(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return fmt.Errorf("computeObjectiveScore: %s must be a finite number >= 0, got %s",
			name, jsNumber(value))
	}
	return nil
}

// NormalizeObjectiveWeights validates repo-configurable weights (fail closed):
// every weight finite and non-negative, the sum exactly 1.0 within
// ObjectiveWeightSumTolerance — a mis-specified objective refuses to compute,
// it never silently skews policy promotion.
func NormalizeObjectiveWeights(weights ObjectiveWeights) (ObjectiveWeights, error) {
	precision := weights.Precision
	recall := weights.Recall
	patchCorrectness := weights.PatchCorrectness
	efficiency := weights.Efficiency
	if err := requireNonNegative("weights.precision", precision); err != nil {
		return ObjectiveWeights{}, err
	}
	if err := requireNonNegative("weights.recall", recall); err != nil {
		return ObjectiveWeights{}, err
	}
	if err := requireNonNegative("weights.patch_correctness", patchCorrectness); err != nil {
		return ObjectiveWeights{}, err
	}
	if err := requireNonNegative("weights.efficiency", efficiency); err != nil {
		return ObjectiveWeights{}, err
	}
	sum := precision + recall + patchCorrectness + efficiency
	if math.Abs(sum-1) > ObjectiveWeightSumTolerance {
		return ObjectiveWeights{}, fmt.Errorf(
			"computeObjectiveScore: weights must sum to exactly 1.0, got %s "+
				"(fail closed: a mis-specified objective is refused, never silently skewed)",
			jsNumber(sum))
	}
	return ObjectiveWeights{
		Precision:        precision,
		Recall:           recall,
		PatchCorrectness: patchCorrectness,
		Efficiency:       efficiency,
	}, nil
}

// ComputeEfficiency computes usage-accounting efficiency (F10-10):
// `1 - (costShare + latencyShare) / 2` with both shares clamped to 0..1
// against the exported ceiling constants — the usage numbers are the source,
// never estimated.
func ComputeEfficiency(usage UsageAccounting) (float64, error) {
	cost := usage.TotalCostUsd
	latency := usage.TotalLatencyMs
	if err := requireNonNegative("usage.totalCostUsd", cost); err != nil {
		return 0, err
	}
	if err := requireNonNegative("usage.totalLatencyMs", latency); err != nil {
		return 0, err
	}
	costShare := math.Min(1, cost/EfficiencyCostCeilingUSD)
	latencyShare := math.Min(1, latency/EfficiencyLatencyCeilingMs)
	return 1 - (costShare+latencyShare)/2, nil
}

// ComputeObjectiveScore computes the improvement-objective score (V-P10-10).
// With UnsafeAutoUpdates > 0 the score COLLAPSES to 0 and the result fails the
// gate — the objective never launders unsafe auto-update behavior into a
// promotable number (ARCH §8.4).
func ComputeObjectiveScore(input ComputeObjectiveScoreInput) (ObjectiveScoreResult, error) {
	if err := requireNonNegative("unsafeAutoUpdates", float64(input.UnsafeAutoUpdates)); err != nil {
		return ObjectiveScoreResult{}, err
	}
	if input.UnsafeAutoUpdates > 0 {
		gate := UnsafeAutoUpdatesGate
		return ObjectiveScoreResult{
			Score:      0,
			Collapsed:  true,
			FailedGate: &gate,
		}, nil
	}
	weights := DefaultObjectiveWeights
	if input.Weights != nil {
		weights = *input.Weights
	}
	normalized, err := NormalizeObjectiveWeights(weights)
	if err != nil {
		return ObjectiveScoreResult{}, err
	}
	recall := input.Metrics.DetectionRecall
	precision := input.Metrics.Precision
	patchCorrectness := input.Metrics.PatchCorrectness
	if err := requireUnitInterval("metrics.detection_recall", recall); err != nil {
		return ObjectiveScoreResult{}, err
	}
	if err := requireUnitInterval("metrics.precision", precision); err != nil {
		return ObjectiveScoreResult{}, err
	}
	if err := requireUnitInterval("metrics.patch_correctness", patchCorrectness); err != nil {
		return ObjectiveScoreResult{}, err
	}
	efficiency, err := ComputeEfficiency(input.Usage)
	if err != nil {
		return ObjectiveScoreResult{}, err
	}
	if err := requireUnitInterval("efficiency", efficiency); err != nil {
		return ObjectiveScoreResult{}, err
	}
	score := normalized.Precision*precision +
		normalized.Recall*recall +
		normalized.PatchCorrectness*patchCorrectness +
		normalized.Efficiency*efficiency
	// Guard the composite against float drift beyond the unit interval.
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return ObjectiveScoreResult{Score: score, Collapsed: false}, nil
}
