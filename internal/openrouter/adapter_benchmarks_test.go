// Ported from packages/openrouter/test/adapters/benchmarks.test.ts.
//
// Historical-replay + self-scoring adapter (A2; ARCH §12, P11-T19): pins the
// deterministic scoring math (rewrite-tendency rate consuming the architectural
// constant; evidence coverage; determinism; the empty-set and hunk-less
// degenerate cases) and the Phase-10 forward-declared harness stub. Fixtures
// mirror the TS suite exactly (default hunk anchored 1..1 with a deletion;
// DOC_PATCH_EXAMPLE is the registered schemas example — deletion span 1, max
// anchored line 42).
package openrouter_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ev is the shared evidence fixture (TS EV).
var ev = schemas.Evidence{Kind: "symbol_diff", Ref: "src/payments/refund.ts:42", Note: "n"}

// benchmarkHunk is the TS `hunk()` fixture: anchored 1..1 with a deletion and
// one evidence ref.
func benchmarkHunk(overrides ...func(*schemas.PatchHunk)) schemas.PatchHunk {
	hunk := schemas.PatchHunk{
		Anchor:       schemas.PatchAnchor{StartLine: 1, EndLine: 1, ContentHash: "sha256:a"},
		DeleteText:   "old",
		InsertText:   "new",
		EvidenceRefs: []schemas.Evidence{ev},
	}
	for _, override := range overrides {
		override(&hunk)
	}
	return hunk
}

func benchmarkPatch(hunks []schemas.PatchHunk) schemas.DocPatch {
	return schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "r",
		DocPath:         "docs/a.md",
		BaseContentHash: "sha256:a",
		Hunks:           hunks,
		EvidenceRefs:    []schemas.Evidence{ev},
		Minimality:      schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 1},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: 1,
			WriterModel:   "m",
			GeneratedAt:   "2026-10-06T00:00:00.000Z",
		},
	}
}

func benchmarkRun(patch schemas.DocPatch, prRef string) openrouter.BenchmarkRun {
	return openrouter.BenchmarkRun{
		PRRef:          prRef,
		Patch:          patch,
		TriggeringDiff: "diff",
		WriterModel:    "m",
		LatencyMs:      10,
	}
}

// rewritePatch — whole-document-shaped: one deletion hunk spanning lines
// 1..10 → ratio 10/10 = 1.
func rewritePatch() schemas.DocPatch {
	return benchmarkPatch([]schemas.PatchHunk{benchmarkHunk(func(h *schemas.PatchHunk) {
		h.Anchor = schemas.PatchAnchor{StartLine: 1, EndLine: 10, ContentHash: "sha256:r"}
		h.DeleteText = "a\nb"
	})})
}

// boundary90Patch — deletion span 9 of max anchored line 10 → ratio 0.9
// (>= WHOLE_DOC_REWRITE_RATIO).
func boundary90Patch() schemas.DocPatch {
	return benchmarkPatch([]schemas.PatchHunk{
		benchmarkHunk(func(h *schemas.PatchHunk) {
			h.Anchor = schemas.PatchAnchor{StartLine: 1, EndLine: 9, ContentHash: "sha256:b9"}
		}),
		benchmarkHunk(func(h *schemas.PatchHunk) {
			h.Anchor = schemas.PatchAnchor{StartLine: 10, EndLine: 10, ContentHash: "sha256:b10"}
			h.DeleteText = ""
		}),
	})
}

// below90Patch — deletion span 8 of max anchored line 10 → ratio 0.8.
func below90Patch() schemas.DocPatch {
	return benchmarkPatch([]schemas.PatchHunk{
		benchmarkHunk(func(h *schemas.PatchHunk) {
			h.Anchor = schemas.PatchAnchor{StartLine: 1, EndLine: 8, ContentHash: "sha256:c8"}
		}),
		benchmarkHunk(func(h *schemas.PatchHunk) {
			h.Anchor = schemas.PatchAnchor{StartLine: 9, EndLine: 10, ContentHash: "sha256:c10"}
			h.DeleteText = ""
		}),
	})
}

func TestScoreRewriteTendencyMathPinned(t *testing.T) {
	report := openrouter.ScoreRewriteTendency([]openrouter.BenchmarkRun{
		benchmarkRun(rewritePatch(), "pr#1"),
		benchmarkRun(rewritePatch(), "pr#2"),
		benchmarkRun(schemas.DocPatchExample, "pr#3"),
	})
	assert.InDelta(t, 2.0/3.0, report.RewriteRate, 1e-12)
	require.Len(t, report.Runs, 3)
	assert.True(t, report.Runs[0].WasRewrite)
	assert.False(t, report.Runs[2].WasRewrite)
	// DOC_PATCH_EXAMPLE: deletion span 1, max anchored line 42.
	assert.InDelta(t, 1.0/42.0, report.Runs[2].MeasuredRatio, 1e-12)
	assert.Equal(t, openrouter.RunEvidenceQuality{HunksWithEvidence: 1, Hunks: 1}, report.Runs[2].EvidenceQuality)
}

func TestWasRewriteConsumesExportedConstant(t *testing.T) {
	require.InDelta(t, 0.9, schemas.WholeDocumentRewriteRatio, 1e-12)

	boundary := openrouter.ScoreRewriteTendency([]openrouter.BenchmarkRun{benchmarkRun(boundary90Patch(), "pr#4")})
	require.Len(t, boundary.Runs, 1)
	assert.True(t, boundary.Runs[0].WasRewrite, "the shared architectural threshold classifies the ratio as a rewrite")
	assert.InDelta(t, 0.9, boundary.Runs[0].MeasuredRatio, 1e-12)

	below := openrouter.ScoreRewriteTendency([]openrouter.BenchmarkRun{benchmarkRun(below90Patch(), "pr#5")})
	require.Len(t, below.Runs, 1)
	assert.False(t, below.Runs[0].WasRewrite)
	assert.InDelta(t, 0.8, below.Runs[0].MeasuredRatio, 1e-12)
}

func TestEmptyRunSetAndHunklessPatch(t *testing.T) {
	report := openrouter.ScoreRewriteTendency(nil)
	assert.InDelta(t, 0.0, report.RewriteRate, 1e-12)
	require.NotNil(t, report.Runs)
	assert.Empty(t, report.Runs)

	// Hunk-less: no anchors at all → 0 (no span signal — NOT an error).
	hunkless := openrouter.ScoreRewriteTendency([]openrouter.BenchmarkRun{
		benchmarkRun(benchmarkPatch(nil), "pr#6"),
	})
	require.Len(t, hunkless.Runs, 1)
	assert.False(t, hunkless.Runs[0].WasRewrite)
	assert.InDelta(t, 0.0, hunkless.Runs[0].MeasuredRatio, 1e-12)
	assert.Equal(t, openrouter.RunEvidenceQuality{HunksWithEvidence: 0, Hunks: 0}, hunkless.Runs[0].EvidenceQuality)

	// 0/0 carries no evidence signal — coverage is 0, not 100%.
	coverage := openrouter.ScoreEvidenceQuality([]openrouter.BenchmarkRun{benchmarkRun(benchmarkPatch(nil), "pr#6")})
	assert.InDelta(t, 0.0, coverage.EvidenceCoverage, 1e-12)
}

func TestScoreEvidenceQualityCoverage(t *testing.T) {
	full := openrouter.ScoreEvidenceQuality([]openrouter.BenchmarkRun{benchmarkRun(rewritePatch(), "pr#7")})
	assert.InDelta(t, 1.0, full.EvidenceCoverage, 1e-12)

	// Sparse: one hunk, no evidence refs → 0 (allowed but counts against).
	sparsePatch := benchmarkPatch([]schemas.PatchHunk{benchmarkHunk(func(h *schemas.PatchHunk) {
		h.EvidenceRefs = nil
	})})
	sparse := openrouter.ScoreEvidenceQuality([]openrouter.BenchmarkRun{benchmarkRun(sparsePatch, "pr#8")})
	assert.InDelta(t, 0.0, sparse.EvidenceCoverage, 1e-12)

	// Mixed: one of two hunks carries evidence → 0.5.
	mixedPatch := benchmarkPatch([]schemas.PatchHunk{
		benchmarkHunk(),
		benchmarkHunk(func(h *schemas.PatchHunk) {
			h.Anchor = schemas.PatchAnchor{StartLine: 5, EndLine: 5, ContentHash: "sha256:a"}
			h.EvidenceRefs = nil
		}),
	})
	mixed := openrouter.ScoreEvidenceQuality([]openrouter.BenchmarkRun{benchmarkRun(mixedPatch, "pr#9")})
	assert.InDelta(t, 0.5, mixed.EvidenceCoverage, 1e-12) // 1/2

	// Empty set → 0.
	empty := openrouter.ScoreEvidenceQuality(nil)
	assert.InDelta(t, 0.0, empty.EvidenceCoverage, 1e-12)
}

func TestBenchmarkScoringDeterministic(t *testing.T) {
	runs := []openrouter.BenchmarkRun{
		benchmarkRun(rewritePatch(), "pr#10"),
		benchmarkRun(schemas.DocPatchExample, "pr#11"),
	}
	first := openrouter.ScoreRewriteTendency(runs)
	second := openrouter.ScoreRewriteTendency(runs)
	assert.Equal(t, first.RewriteRate, second.RewriteRate)
	assert.Equal(t, first.Runs, second.Runs)

	firstCoverage := openrouter.ScoreEvidenceQuality(runs)
	secondCoverage := openrouter.ScoreEvidenceQuality(runs)
	assert.Equal(t, firstCoverage, secondCoverage)
}

func TestHarnessStubPhase10Only(t *testing.T) {
	_, err := openrouter.HistoricalReplayHarnessStub.Collect([]string{"acme/payments#812"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Phase 10")
	assert.Contains(t, err.Error(),
		"role 'historical-replay-harness (A2; full execution Phase 10)' is forward-declared for a later phase and has no implementation yet")
	var notImplemented *openrouter.NotYetImplementedRoleError
	require.ErrorAs(t, err, &notImplemented)
	assert.Equal(t, openrouter.CodeNotYetImplementedRole, notImplemented.Code)

	_, err = openrouter.HistoricalReplayHarnessStub.Run([]openrouter.BenchmarkRun{benchmarkRun(rewritePatch(), "acme/payments#812")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Phase 10")
}
