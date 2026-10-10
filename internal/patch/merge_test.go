package patch_test

// Black-box port of test/patch-merge.test.ts (P3-T10; ARCH §4.9, §12.3, A9):
// diff3 semantics — one-side changes taken, identical changes taken once,
// overlapping non-identical changes conflict; adversarial Markdown fixtures
// (front-matter, fenced code blocks, nested lists) merge cleanly when the
// changes are disjoint and deterministically, always.

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/stretchr/testify/require"
)

var (
	frontMatterBase = strings.Join([]string{
		"---",
		"title: Payments",
		"---",
		"",
		"Refunds require an idempotency key.",
		"",
		"Amounts above 10 USD are held for review.",
	}, "\n")

	fencedBase = strings.Join([]string{
		"# Runbook",
		"",
		"```bash",
		"docsync check --strict",
		"```",
		"",
		"Escalate to the on-call rotation.",
	}, "\n")

	nestedListBase = strings.Join([]string{
		"- Top item A",
		"  - Nested item 1",
		"  - Nested item 2",
		"- Top item B",
	}, "\n")
)

func withTrailing(text string) string { return text + "\n" }

// rep is the TS String.replace(old, new) single-replacement equivalent.
func rep(s, old, replacement string) string { return strings.Replace(s, old, replacement, 1) }

func TestThreeWayMerge(t *testing.T) {
	t.Run("unchanged sides reproduce the base byte-for-byte (round-trip)", func(t *testing.T) {
		result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{
			Base: withTrailing(frontMatterBase), Ours: withTrailing(frontMatterBase), Theirs: withTrailing(frontMatterBase),
		})
		require.True(t, result.Ok)
		require.Equal(t, withTrailing(frontMatterBase), result.Merged)
	})

	t.Run("an ours-only change wins; a theirs-only change wins", func(t *testing.T) {
		base := withTrailing(frontMatterBase)
		oursOnly := rep(base, "title: Payments", "title: Payments v2")
		theirsOnly := rep(base, "Refunds require an idempotency key.", "Refunds require an idempotency key above 10 USD.")
		ours := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: oursOnly, Theirs: base})
		require.True(t, ours.Ok)
		require.Equal(t, oursOnly, ours.Merged)
		theirs := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: base, Theirs: theirsOnly})
		require.True(t, theirs.Ok)
		require.Equal(t, theirsOnly, theirs.Merged)
	})

	t.Run("disjoint changes from both sides combine deterministically", func(t *testing.T) {
		base := withTrailing(frontMatterBase)
		ours := rep(base, "title: Payments", "title: Payments v2")
		theirs := rep(base, "Refunds require an idempotency key.", "Refunds require an idempotency key above 10 USD.")
		merged := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		require.True(t, merged.Ok)
		expected := withTrailing(
			rep(
				rep(frontMatterBase, "title: Payments", "title: Payments v2"),
				"Refunds require an idempotency key.",
				"Refunds require an idempotency key above 10 USD.",
			),
		)
		require.Equal(t, expected, merged.Merged)
	})

	t.Run("identical changes from both sides are taken once", func(t *testing.T) {
		base := withTrailing(fencedBase)
		changed := rep(base, "docsync check --strict", "docsync check --strict --json")
		result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: changed, Theirs: changed})
		require.True(t, result.Ok)
		require.Equal(t, changed, result.Merged)
	})

	t.Run("overlapping non-identical changes conflict with each side's view recorded (A9)", func(t *testing.T) {
		base := withTrailing(nestedListBase)
		ours := rep(base, "- Top item B", "- Top item B (urgent)")
		theirs := rep(base, "- Top item B", "- Top item C")
		result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		require.False(t, result.Ok)
		require.Len(t, result.Conflicts, 1)
		conflict := result.Conflicts[0]
		require.Equal(t, 4, conflict.Line)
		require.Equal(t, "- Top item B", conflict.BaseText)
		require.Equal(t, "- Top item B (urgent)", conflict.OursText)
		require.Equal(t, "- Top item C", conflict.TheirsText)
	})

	t.Run("A9 adversarial fixture: front-matter document merges cleanly", func(t *testing.T) {
		base := withTrailing(frontMatterBase)
		ours := rep(base, "title: Payments", "title: Payments v2")
		theirs := rep(base, "Amounts above 10 USD are held for review.", "Amounts above 25 USD are held for review.")
		result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		require.True(t, result.Ok)
		require.Equal(t, withTrailing(
			rep(
				rep(frontMatterBase, "title: Payments", "title: Payments v2"),
				"Amounts above 10 USD are held for review.",
				"Amounts above 25 USD are held for review.",
			),
		), result.Merged)
	})

	t.Run("A9 adversarial fixture: fenced code block merges cleanly", func(t *testing.T) {
		base := withTrailing(fencedBase)
		ours := rep(base, "docsync check --strict", "docsync check --strict --json")
		theirs := rep(base, "Escalate to the on-call rotation.", "Escalate to the secondary rotation.")
		result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		require.True(t, result.Ok)
		require.Equal(t, withTrailing(
			rep(
				rep(fencedBase, "docsync check --strict", "docsync check --strict --json"),
				"Escalate to the on-call rotation.",
				"Escalate to the secondary rotation.",
			),
		), result.Merged)
	})

	t.Run("A9 adversarial fixture: nested lists — sibling edits merge, same-item edits conflict", func(t *testing.T) {
		base := withTrailing(nestedListBase)
		ours := rep(base, "  - Nested item 1", "  - Nested item 1 (clarified)")
		theirs := rep(base, "  - Nested item 2", "  - Nested item 2 (renamed)")
		result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		require.True(t, result.Ok)
		require.Equal(t, withTrailing(
			rep(
				rep(nestedListBase, "  - Nested item 1", "  - Nested item 1 (clarified)"),
				"  - Nested item 2",
				"  - Nested item 2 (renamed)",
			),
		), result.Merged)

		conflicting := patch.ThreeWayMerge(patch.ThreeWayMergeInput{
			Base:   base,
			Ours:   rep(base, "  - Nested item 2", "  - Nested item 2 (urgent)"),
			Theirs: rep(base, "  - Nested item 2", "  - Nested item 2 (renamed)"),
		})
		require.False(t, conflicting.Ok)
	})

	t.Run("pure insertions on both sides at different points merge; at the same point they conflict", func(t *testing.T) {
		base := withTrailing("# Title\n\nBody line.\n")
		ours := base + "Ours appended.\n"
		theirs := base + "Theirs appended.\n"
		// Same insertion point with different content → conflict (ambiguous order).
		result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		require.False(t, result.Ok)

		compatible := patch.ThreeWayMerge(patch.ThreeWayMergeInput{
			Base:   base,
			Ours:   base + "Ours appended.\n",
			Theirs: "Intro line.\n\n# Title\n\nBody line.\n",
		})
		require.True(t, compatible.Ok)
		require.Equal(t, "Intro line.\n\n# Title\n\nBody line.\nOurs appended.\n", compatible.Merged)
	})

	t.Run("trailing-newline convention follows OURS; empty merges stay empty", func(t *testing.T) {
		noTrailing := fencedBase
		withTrailingBase := withTrailing(fencedBase)
		// Ours has no trailing newline, theirs does → merged follows ours.
		oursNoTrailing := rep(noTrailing, "docsync check --strict", "docsync check --json")
		theirsTrailing := rep(withTrailingBase, "docsync check --strict", "docsync check --json")
		result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: withTrailingBase, Ours: oursNoTrailing, Theirs: theirsTrailing})
		require.True(t, result.Ok)
		require.Equal(t, oursNoTrailing, result.Merged)

		// Both sides delete everything → empty merge.
		empty := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: withTrailingBase, Ours: "", Theirs: ""})
		require.True(t, empty.Ok)
		require.Equal(t, "", empty.Merged)
	})

	t.Run("is deterministic: the same inputs produce the same conflicts every time", func(t *testing.T) {
		base := withTrailing(nestedListBase)
		ours := rep(base, "- Top item A", "- Top item A1")
		theirs := rep(base, "- Top item A", "- Top item A2")
		first := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		second := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		require.Equal(t, first, second)
	})
}

func TestLCSCellBudgetDegradation(t *testing.T) {
	degradeBudget := 1

	t.Run("LCS cell-budget degradation produces a conflict region deterministically", func(t *testing.T) {
		base := withTrailing("# Title\n\nKeep me.\nMiddle one.\nMiddle two.\nTail line.\n")
		ours := rep(base, "Middle one.\nMiddle two.", "Ours one.\nOurs two.")
		theirs := rep(base, "Middle one.\nMiddle two.", "Theirs one.\nTheirs two.")

		// Budget 1 cell: the differing middle (2×2 = 4 cells > 1) exceeds the
		// budget on BOTH sides → each side degrades to one whole-region
		// replacement of the middle → overlapping different replacements ⇒ one
		// conflict region covering the differing middle.
		degraded := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs, MaxLcsCells: &degradeBudget})
		require.False(t, degraded.Ok)
		require.Len(t, degraded.Conflicts, 1)
		conflict := degraded.Conflicts[0]
		require.Equal(t, 4, conflict.Line) // 1-based base line of the differing middle
		require.Equal(t, "Middle one.\nMiddle two.", conflict.BaseText)
		require.Equal(t, "Ours one.\nOurs two.", conflict.OursText)
		require.Equal(t, "Theirs one.\nTheirs two.", conflict.TheirsText)

		// Deterministic: the degraded outcome is identical across runs.
		require.Equal(t, degraded, patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs, MaxLcsCells: &degradeBudget}))

		// Degradation is fail-closed ONLY when both sides actually differ over
		// the region: one unchanged side still merges cleanly under the same
		// tiny budget (the degraded edit of a side identical to base covers the
		// region alone).
		oneSideChanged := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: base, MaxLcsCells: &degradeBudget})
		require.True(t, oneSideChanged.Ok)
		require.Equal(t, ours, oneSideChanged.Merged)

		// The default budget (10M cells) handles the same inputs normally: the
		// LCS diff resolves the disjoint-enough edits without degradation — here
		// both sides replace the SAME region with different content, so the
		// conflict is semantic (identical to the degraded region view here, but
		// reached without the budget guard).
		normal := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
		require.False(t, normal.Ok)
		require.Len(t, normal.Conflicts, 1)
		require.Equal(t, 4, normal.Conflicts[0].Line)
	})
}
