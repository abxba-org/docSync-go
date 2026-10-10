package patch_test

// Black-box port of test/patch-minimality.test.ts (P3-T12; V-P3-6): measured
// counts on the actual content change (never the writer's self-report), pinned
// ratio math against the triggering diff, the oversized FLAG at the maxRatio
// boundary (flagged for the verifier here, never auto-rejected at the
// application layer), and the labeled-bootstrap default bound.

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func TestMeasureMinimality(t *testing.T) {
	t.Run("counts the actual added/removed non-empty lines (duplicates + blank lines handled)", func(t *testing.T) {
		current := "alpha\nbeta\n\nbeta\nalpha\n"
		patched := "gamma gamma\ngamma gamma\nbeta\nbeta\n"
		m, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent: current,
			PatchedContent: patched,
		})
		require.NoError(t, err)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 2, RemovedLines: 2}, m.Minimality)
		require.Nil(t, m.Ratio)
		require.False(t, m.Oversized)
	})

	t.Run("pins the ratio math against the triggering diff (churn / max(1, trigger churn))", func(t *testing.T) {
		current := "one\ntwo\nthree\nfour\nfive\n"
		patched := "one\nTWO\nthree\nfour\nfive\nsix\n"
		m, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent:      current,
			PatchedContent:      patched,
			TriggeringDiffStats: &patch.DiffStats{Added: 4, Removed: 2},
		})
		require.NoError(t, err)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 2, RemovedLines: 1}, m.Minimality)
		require.NotNil(t, m.Ratio)
		require.InDelta(t, 3.0/6.0, *m.Ratio, 1e-12)
		require.False(t, m.Oversized)
	})

	t.Run("flags oversized strictly beyond the bound; exactly AT the bound is not oversized (V-P3-6)", func(t *testing.T) {
		current := "a\nb\n"
		patched := "x\ny\nz\nw\n"                       // added 4, removed 2 → churn 6
		stats := &patch.DiffStats{Added: 1, Removed: 1} // trigger churn 2 → ratio 3
		at := 3.0
		atBound, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent: current, PatchedContent: patched, TriggeringDiffStats: stats, MaxRatio: &at,
		})
		require.NoError(t, err)
		require.False(t, atBound.Oversized)

		below := 2.999
		belowBound, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent: current, PatchedContent: patched, TriggeringDiffStats: stats, MaxRatio: &below,
		})
		require.NoError(t, err)
		require.True(t, belowBound.Oversized)
	})

	t.Run("uses DEFAULT_MINIMALITY_MAX_RATIO (labeled bootstrap data, 20) when maxRatio is omitted", func(t *testing.T) {
		require.Equal(t, 20.0, patch.DefaultMinimalityMaxRatio)
		current := "a\n"
		patched := strings.Repeat("x\n", 42)            // added 42, removed 1 → churn 43
		stats := &patch.DiffStats{Added: 1, Removed: 1} // ratio 43/2 = 21.5 > 20
		m, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent: current, PatchedContent: patched, TriggeringDiffStats: stats,
		})
		require.NoError(t, err)
		require.True(t, m.Oversized)
		require.NotNil(t, m.Ratio)
		require.InDelta(t, 21.5, *m.Ratio, 1e-12)
	})

	t.Run("rejects invalid bounds and invalid triggering stats (config errors, TS TypeError)", func(t *testing.T) {
		zero := 0.0
		_, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent: "a", PatchedContent: "b", MaxRatio: &zero,
		})
		require.Error(t, err)

		_, err = patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent: "a", PatchedContent: "b",
			TriggeringDiffStats: &patch.DiffStats{Added: -1, Removed: 0},
		})
		require.Error(t, err)
	})

	t.Run("whitespace-only churn measures zero (trimmed non-empty basis mirrors the rewrite detector)", func(t *testing.T) {
		m, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent: "a\n\nb\n",
			PatchedContent: "a\n   \nb\n",
		})
		require.NoError(t, err)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 0, RemovedLines: 0}, m.Minimality)
		require.Nil(t, m.Ratio)
		require.False(t, m.Oversized)

		// With triggering stats, zero churn pins ratio 0.
		withStats, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			CurrentContent:      "a\n\nb\n",
			PatchedContent:      "a\n   \nb\n",
			TriggeringDiffStats: &patch.DiffStats{Added: 3, Removed: 1},
		})
		require.NoError(t, err)
		require.NotNil(t, withStats.Ratio)
		require.Equal(t, 0.0, *withStats.Ratio)
	})
}
