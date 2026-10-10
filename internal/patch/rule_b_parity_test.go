package patch_test

// Black-box port of test/patch-rule-b-parity.test.ts — rule-B parity: the
// @docsync/schemas structural simulation (schemas.MeasureStructuralDiffRatio —
// the rewrite-detector's rule-B quantity) must equal the REFERENCE application
// engine's measurement (ApplyDocPatch splice → MeasureMinimality added+removed,
// divided by the original's non-empty line count) on identical fixtures.
//
// Fixtures are insertion-heavy patches near the WHOLE_DOC_REWRITE_RATIO (0.9)
// boundary — the case class that motivated the fix: zero-width anchors (pure
// insertions) previously mis-simulated with removeCount 1 (the anchor line was
// wrongly removed), inflating the simulated delta by 1 per insertion hunk. A
// 10-line document with an 8-line insertion must measure 0.8 (not a rewrite) —
// 9/10 only for a NINE-line insertion.
//
// Patches here are anchor-exact (hash + recorded lines agree with the document),
// so the engine resolves every hunk at its recorded lines and the two splices
// apply identical edits.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func docOfLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "line-" + strconv.Itoa(i)
	}
	return strings.Join(lines, "\n") + "\n"
}

func nonEmptyLineCount(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// seq is the TS `Array.from({length: n}, (_, i) => `${prefix}-${i}`)`.
func seq(prefix string, from, count int) []string {
	out := make([]string, count)
	for i := range out {
		out[i] = prefix + "-" + strconv.Itoa(from+i)
	}
	return out
}

func parityEvidence() []schemas.Evidence {
	return []schemas.Evidence{{
		Kind: schemas.EvidenceKindSymbolDiff,
		Ref:  "src/payments/refund.ts:3",
		Note: "parity fixture",
	}}
}

// insertionPatch is a pure-insertion patch: count fresh lines inserted before
// line at (TS: insertionPatch).
func insertionPatch(n, at, count int, tag string) *schemas.DocPatch {
	d := docOfLines(n)
	inserted := strings.Join(seq(tag, 0, count), "\n")
	return &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "parity-insert-" + tag,
		DocPath:         "docs/payments.md",
		BaseContentHash: docutil.ContentHash(d),
		Hunks: []schemas.PatchHunk{{
			Anchor:       schemas.PatchAnchor{StartLine: at, EndLine: at, ContentHash: docutil.ContentHash("")},
			DeleteText:   "",
			InsertText:   inserted,
			EvidenceRefs: parityEvidence(),
		}},
		EvidenceRefs: parityEvidence(),
		Minimality:   schemas.DocPatchMinimality{Measured: true, AddedLines: count, RemovedLines: 0},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: 17, WriterModel: "anthropic/claude-sonnet-4.5", GeneratedAt: "2026-10-06T12:00:00.000Z",
		},
	}
}

// replacementPatch replaces span lines by count fresh lines (TS: replacementPatch).
func replacementPatch(n, at, span, count int) *schemas.DocPatch {
	d := docOfLines(n)
	deleted := strings.Join(seq("line", at-1, span), "\n")
	inserted := strings.Join(seq("edited", 0, count), "\n")
	return &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "parity-replace",
		DocPath:         "docs/payments.md",
		BaseContentHash: docutil.ContentHash(d),
		Hunks: []schemas.PatchHunk{{
			Anchor:       schemas.PatchAnchor{StartLine: at, EndLine: at + span - 1, ContentHash: docutil.ContentHash(deleted)},
			DeleteText:   deleted,
			InsertText:   inserted,
			EvidenceRefs: parityEvidence(),
		}},
		EvidenceRefs: parityEvidence(),
		Minimality:   schemas.DocPatchMinimality{Measured: true, AddedLines: count, RemovedLines: span},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: 17, WriterModel: "anthropic/claude-sonnet-4.5", GeneratedAt: "2026-10-06T12:00:00.000Z",
		},
	}
}

func parityApplyInput(t *testing.T, p *schemas.DocPatch, d string) patch.ApplyDocPatchInput {
	t.Helper()
	return patch.ApplyDocPatchInput{
		Patch:             p,
		Git:               newCatFileDouble(d, nil),
		Rev:               "HEAD",
		WorktreeContent:   strp(d),
		ManifestAllowlist: map[string]bool{"docs/payments.md": true},
		Now:               NOW,
	}
}

func TestRuleBSimulationMatchesEngineSplice(t *testing.T) {
	t.Run("reviewer case: 10-line doc + 8-line pure insertion measures 0.8 — not a rewrite — and simulation ≡ engine", func(t *testing.T) {
		d := docOfLines(10)
		p := insertionPatch(10, 1, 8, "fresh")

		// The corrected simulation: 8 added non-empty lines / 10 non-empty = 0.8
		// (the buggy simulation removed the anchor line: 9/10 = 0.9 ⇒ false
		// rewrite). The reviewer's exact boundary case stays NOT a rewrite.
		require.InDelta(t, 0.8, schemas.MeasureStructuralDiffRatio(p, d), 1e-12)
		require.False(t, schemas.IsWholeDocumentRewrite(p, d))
		require.NoError(t, schemas.AssertNotWholeDocumentRewrite(p, d))

		// Engine reference: apply the patch, measure the real content delta.
		outcome, err := patch.ApplyDocPatch(context.Background(), parityApplyInput(t, p, d))
		require.NoError(t, err)
		require.Equal(t, patch.OutcomeApplied, outcome.Kind)
		engineMeasurement, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			Patch: p, CurrentContent: d, PatchedContent: outcome.ResultContent,
		})
		require.NoError(t, err)
		engineRatio := float64(engineMeasurement.Minimality.AddedLines+engineMeasurement.Minimality.RemovedLines) /
			float64(nonEmptyLineCount(d))

		require.Equal(t, 8, engineMeasurement.Minimality.AddedLines)
		require.Equal(t, 0, engineMeasurement.Minimality.RemovedLines)
		require.InDelta(t, schemas.MeasureStructuralDiffRatio(p, d), engineRatio, 1e-12)
		require.InDelta(t, 0.8, engineRatio, 1e-12)
	})

	t.Run("insertion-heavy mixed patches (double insertion + replacement) — simulation ≡ engine", func(t *testing.T) {
		d := docOfLines(10)
		basePatch := replacementPatch(10, 4, 2, 1)
		mixed := *basePatch
		mixed.RunID = "parity-mixed"
		mixed.Hunks = []schemas.PatchHunk{
			{
				Anchor:       schemas.PatchAnchor{StartLine: 1, EndLine: 1, ContentHash: docutil.ContentHash("")},
				DeleteText:   "",
				InsertText:   "head-0\nhead-1",
				EvidenceRefs: parityEvidence(),
			},
			basePatch.Hunks[0],
			{
				Anchor:       schemas.PatchAnchor{StartLine: 11, EndLine: 11, ContentHash: docutil.ContentHash("")},
				DeleteText:   "",
				InsertText:   "tail-0\ntail-1",
				EvidenceRefs: parityEvidence(),
			},
		}
		require.Empty(t, mixed.Validate())

		// Simulated delta: 2 head insertions + 1 added + 2 removed (replacement)
		// + 2 tail insertions = 7 over 10 non-empty lines.
		schemaRatio := schemas.MeasureStructuralDiffRatio(&mixed, d)
		require.InDelta(t, 0.7, schemaRatio, 1e-12)

		outcome, err := patch.ApplyDocPatch(context.Background(), parityApplyInput(t, &mixed, d))
		require.NoError(t, err)
		require.Equal(t, patch.OutcomeApplied, outcome.Kind)
		engineMeasurement, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			Patch: &mixed, CurrentContent: d, PatchedContent: outcome.ResultContent,
		})
		require.NoError(t, err)
		engineRatio := float64(engineMeasurement.Minimality.AddedLines+engineMeasurement.Minimality.RemovedLines) /
			float64(nonEmptyLineCount(d))
		require.InDelta(t, schemaRatio, engineRatio, 1e-12)
	})

	t.Run("replacement patch (2→2 lines over 10) — simulation ≡ engine (0.4, comfortably below the ratio)", func(t *testing.T) {
		d := docOfLines(10)
		p := replacementPatch(10, 3, 2, 2)
		schemaRatio := schemas.MeasureStructuralDiffRatio(p, d)
		require.InDelta(t, 0.4, schemaRatio, 1e-12)

		outcome, err := patch.ApplyDocPatch(context.Background(), parityApplyInput(t, p, d))
		require.NoError(t, err)
		require.Equal(t, patch.OutcomeApplied, outcome.Kind)
		engineMeasurement, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
			Patch: p, CurrentContent: d, PatchedContent: outcome.ResultContent,
		})
		require.NoError(t, err)
		engineRatio := float64(engineMeasurement.Minimality.AddedLines+engineMeasurement.Minimality.RemovedLines) /
			float64(nonEmptyLineCount(d))
		require.InDelta(t, schemaRatio, engineRatio, 1e-12)
	})

	t.Run("the 0.9 boundary still classifies: a NINE-line insertion into a 10-line doc IS a rule-B rewrite", func(t *testing.T) {
		d := docOfLines(10)
		p := insertionPatch(10, 1, 9, "flood")
		require.InDelta(t, 0.9, schemas.MeasureStructuralDiffRatio(p, d), 1e-12)
		// The 0.9 bound itself is pinned behaviorally: 8 → not a rewrite, 9 → a
		// rewrite (the Go WholeDocumentRewriteRatio constant is package-private
		// in internal/schemas, so the direct `=== 0.9` assertion from the TS suite
		// is not reproducible from this black-box test; the boundary fixtures pin
		// the value).
		require.True(t, schemas.IsWholeDocumentRewrite(p, d))
	})
}
