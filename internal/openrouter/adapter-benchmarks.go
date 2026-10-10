// Ported from packages/openrouter/src/adapters/benchmarks.ts.
//
// Historical-replay + self-scoring adapter seam (A2; ARCH §12, P11-T19): the
// two deterministic self-scoring functions the harness computes over recorded
// historical-replay runs, plus the forward-declared harness seam that runs
// them. Benchmarks NEVER mutate policy by themselves (ARCH §12) — the
// promotion pipeline consumes these scores.
//
// This module deliberately contains NO dynamic imports and NO Zod — the Go
// port imports only internal/schemas for the DocPatch/Evidence types and the
// exported architectural constants (WHOLE_DOCUMENT_REWRITE_RATIO). The
// scoring math is intentionally simple deterministic aggregation.
package openrouter

import (
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// One recorded historical replay run (PR/merge ref → proposed doc patch +
// triggering diff + writer model + measured latency). Latency feeds the P95
// probes (the ported CalibrationResult's `p95_latency_ms`), NOT the rewrite
// scorers below.
type BenchmarkRun struct {
	// PRRef is the historical PR/merge ref.
	PRRef string `json:"pr_ref"`
	// Patch is the proposed doc patch.
	Patch schemas.DocPatch `json:"patch"`
	// TriggeringDiff is the triggering diff text.
	TriggeringDiff string `json:"triggering_diff"`
	// WriterModel is the model that produced the patch.
	WriterModel string `json:"writer_model"`
	// LatencyMs is the measured latency in milliseconds.
	LatencyMs int `json:"latency_ms"`
}

// RunEvidenceQuality is the evidence-coverage cardinalities of one run (TS
// RunEvidenceQuality): hunks carrying at least one evidence ref over total
// hunks. 0/0 is NOT 100% — it carries no evidence signal.
type RunEvidenceQuality struct {
	// HunksWithEvidence is the count of hunks with at least one evidence ref.
	HunksWithEvidence int `json:"hunks_with_evidence"`
	// Hunks is the total hunk count.
	Hunks int `json:"hunks"`
}

// RewriteTendencyScored scores ONE historical run for whole-document-rewrite
// tendency (TS RewriteTendencyScored). `was_rewrite` CONSUMES the architectural
// constant WHOLE_DOCUMENT_REWRITE_RATIO (0.9 — shared with the patch schema's
// guard) rather than re-declaring the threshold, so the scorer and the guard
// can never drift. `measured_ratio` is the deleted-line span ratio over the
// patch's max anchored end line (best available proxy without the pre-image
// file).
type RewriteTendencyScored struct {
	// Run is the scored run.
	Run BenchmarkRun `json:"run"`
	// WasRewrite reports whether the patch structurally rewrote the document.
	WasRewrite bool `json:"was_rewrite"`
	// MeasuredRatio is the deleted-line span ratio.
	MeasuredRatio float64 `json:"measured_ratio"`
	// EvidenceQuality is the run's evidence-coverage cardinalities.
	EvidenceQuality RunEvidenceQuality `json:"evidence_quality"`
}

// RewriteTendencyReport aggregates the rewrite rate over the scored set (TS
// RewriteTendencyReport). `runs` is the per-run detail; an empty set yields
// rate 0.
type RewriteTendencyReport struct {
	// RewriteRate is the fraction of scored runs that were rewrites.
	RewriteRate float64 `json:"rewrite_rate"`
	// Runs is the per-run scored detail.
	Runs []RewriteTendencyScored `json:"runs"`
}

// EvidenceQualityReport aggregates evidence coverage over the run set (TS
// EvidenceQualityReport): hunks-with-evidence / hunks summed over all runs.
// An empty set (and a hunk-less set) yields 0.
type EvidenceQualityReport struct {
	// EvidenceCoverage is the aggregate evidence coverage.
	EvidenceCoverage float64 `json:"evidence_coverage"`
}

// ReplayBenchmarkReport is the combined historical-replay report the harness
// seam returns (TS ReplayBenchmarkReport).
type ReplayBenchmarkReport struct {
	// RewriteRate is the aggregate rewrite rate.
	RewriteRate float64 `json:"rewrite_rate"`
	// EvidenceCoverage is the aggregate evidence coverage.
	EvidenceCoverage float64 `json:"evidence_coverage"`
	// Runs is the per-run scored detail.
	Runs []RewriteTendencyScored `json:"runs"`
}

// ScoreRewriteTendency scores each run deterministically and aggregates the
// rewrite rate (TS scoreRewriteTendency). Pure and synchronous. Per-run
// ratios aggregate by BOOLEAN rewrite classification over the run count — a
// patched threshold change anywhere in the set moves the aggregate, not just
// one run's row (V-P11-19).
func ScoreRewriteTendency(runs []BenchmarkRun) RewriteTendencyReport {
	scored := make([]RewriteTendencyScored, 0, len(runs))
	rewrites := 0
	for _, run := range runs {
		measuredRatio := rewriteTendencyRatio(run.Patch)
		wasRewrite := measuredRatio >= schemas.WholeDocumentRewriteRatio
		if wasRewrite {
			rewrites++
		}
		scored = append(scored, RewriteTendencyScored{
			Run:             run,
			WasRewrite:      wasRewrite,
			MeasuredRatio:   measuredRatio,
			EvidenceQuality: runEvidenceQuality(run.Patch),
		})
	}
	rate := 0.0
	if len(scored) > 0 {
		rate = float64(rewrites) / float64(len(scored))
	}
	return RewriteTendencyReport{RewriteRate: rate, Runs: scored}
}

// ScoreEvidenceQuality aggregates evidence coverage over the run set (TS
// scoreEvidenceQuality): hunks-with-evidence / hunks summed over all runs.
// Pure and synchronous.
func ScoreEvidenceQuality(runs []BenchmarkRun) EvidenceQualityReport {
	hunksWithEvidence := 0
	totalHunks := 0
	for _, run := range runs {
		quality := runEvidenceQuality(run.Patch)
		hunksWithEvidence += quality.HunksWithEvidence
		totalHunks += quality.Hunks
	}
	coverage := 0.0
	if totalHunks > 0 {
		coverage = float64(hunksWithEvidence) / float64(totalHunks)
	}
	return EvidenceQualityReport{EvidenceCoverage: coverage}
}

// rewriteTendencyRatio computes the deleted-line span ratio for one patch
// (TS rewriteTendencyRatio): the sum of each deleting hunk's anchored line
// span over the patch's max anchored end line. 0 for a hunk-less patch or a
// patch anchored at line 0 (both carry no span signal).
func rewriteTendencyRatio(patch schemas.DocPatch) float64 {
	maxEndLine := 0
	deletedLines := 0
	for _, hunk := range patch.Hunks {
		if hunk.Anchor.EndLine > maxEndLine {
			maxEndLine = hunk.Anchor.EndLine
		}
		if hunk.DeleteText != "" {
			deletedLines += hunk.Anchor.EndLine - hunk.Anchor.StartLine + 1
		}
	}
	if maxEndLine == 0 {
		return 0
	}
	return float64(deletedLines) / float64(maxEndLine)
}

// runEvidenceQuality counts each hunk's evidence refs (TS
// runEvidenceQuality). 0/0 is NOT 100% — it carries no evidence signal.
func runEvidenceQuality(patch schemas.DocPatch) RunEvidenceQuality {
	hunksWithEvidence := 0
	for _, hunk := range patch.Hunks {
		if len(hunk.EvidenceRefs) > 0 {
			hunksWithEvidence++
		}
	}
	return RunEvidenceQuality{HunksWithEvidence: hunksWithEvidence, Hunks: len(patch.Hunks)}
}

// HistoricalReplayHarness is the historical-replay harness seam (TS
// HistoricalReplayHarness): collect recorded PR runs, then run the
// self-scoring functions over them. Go cannot declare a package-level const
// of interface type, so the Phase-10 seam instance is the
// HistoricalReplayHarnessStub var below (the Go form of the TS
// HISTORICAL_REPLAY_HARNESS const; same single instance).
type HistoricalReplayHarness interface {
	Collect(prRefs []string) ([]BenchmarkRun, error)
	Run(runs []BenchmarkRun) (ReplayBenchmarkReport, error)
}

// historicalReplayHarness is the not-yet-implemented harness: collection is
// the Phase-10 job of the orchestrator/history index (the harness reads
// recorded merge history), and run() (the A2 loop: collect → score → propose
// to policy-architect) also belongs to Phase 10. Every method fails closed
// with the typed not-yet-implemented error (TS NotYetImplementedRoleError
// from errors.ts — a 2-phase not-implemented class with the SAME base as the
// gateway errors) — this adapter is a SEAM, not a live path.
type historicalReplayHarness struct{}

func (historicalReplayHarness) Collect(prRefs []string) ([]BenchmarkRun, error) {
	return nil, NewNotYetImplementedRoleError(historicalReplayRoleLabel)
}

func (historicalReplayHarness) Run(runs []BenchmarkRun) (ReplayBenchmarkReport, error) {
	return ReplayBenchmarkReport{}, NewNotYetImplementedRoleError(historicalReplayRoleLabel)
}

// historicalReplayRoleLabel is the forward-declared role label the typed
// not-yet-implemented error names (verbatim from the TS seam).
const historicalReplayRoleLabel = "historical-replay-harness (A2; full execution Phase 10)"

// HistoricalReplayHarnessStub is the Phase-10 seam instance (TS
// HISTORICAL_REPLAY_HARNESS): every method fails closed with the typed
// not-yet-implemented error. This adapter is a SEAM, not a live path — see the
// module doc.
var HistoricalReplayHarnessStub HistoricalReplayHarness = historicalReplayHarness{}
