package patch_test

// Black-box port of test/patch-anchor.test.ts (P3-T08/T09; ARCH §4.3; invariant
// 13, V-P3-3): hash is the identity, lines are the locator — a unique hash match
// re-anchors legitimately (even at moved lines), a misquoted span or an
// ambiguous span is UNANCHORED (never a silent re-anchor), out-of-bounds
// recorded lines are unanchored, and pure insertions resolve at their in-bounds
// point only.

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func TestResolveHunkAnchorContentHashFirst(t *testing.T) {
	t.Run("resolves in place when the recorded lines still hold the hashed span", func(t *testing.T) {
		h := hunk(hunkOpts{
			start:      3,
			deleteText: docLines[2],
			insert:     "Refunds require an idempotency key above 10 USD.",
		})
		res := patch.ResolveHunkAnchor(&h, doc)
		require.Equal(t, patch.AnchorResolved, res.Status)
		require.NotNil(t, res.LineRange)
		require.Equal(t, 3, res.LineRange.StartLine)
		require.Equal(t, 3, res.LineRange.EndLine)
		require.False(t, res.ReAnchored)
	})

	t.Run("content-hash-first: a unique match at SHIFTED lines resolves at the actual lines", func(t *testing.T) {
		// The span text moved from line 4 to line 7 (lines inserted above it):
		// the recorded lines are stale but the hash is unique — legitimate
		// re-anchor (content wins over lines).
		shifted := strings.Join([]string{
			"# Payments",
			"",
			"NEW HEADER NOTE",
			"ANOTHER NEW LINE",
			"",
			"Refunds require an idempotency key.",
			"",
		}, "\n") + "\n"
		h := hunk(hunkOpts{start: 4, deleteText: "Refunds require an idempotency key."})
		res := patch.ResolveHunkAnchor(&h, shifted)
		require.Equal(t, patch.AnchorResolved, res.Status)
		require.NotNil(t, res.LineRange)
		require.Equal(t, 6, res.LineRange.StartLine)
		require.Equal(t, 6, res.LineRange.EndLine)
		require.True(t, res.ReAnchored)
	})

	t.Run("content wins over lines: misquoted delete_text is unanchored even though the recorded lines exist", func(t *testing.T) {
		h := hunk(hunkOpts{start: 4, deleteText: "Amounts above 25 USD are held for review."})
		res := patch.ResolveHunkAnchor(&h, "# Payments\n\nTotally different body line.\nAnother one.\n\nFooter.\n")
		require.Equal(t, patch.AnchorUnanchored, res.Status)
		require.Equal(t, "content hash matches nowhere — doc edited or delete_text misquoted", res.Reason)
	})

	t.Run("duplicated span text is ambiguous → unanchored (invariant 13, never silent re-anchor)", func(t *testing.T) {
		duplicated := strings.Repeat("# Payments\n\nRefunds require an idempotency key.\n", 2)
		h := hunk(hunkOpts{start: 3, deleteText: "Refunds require an idempotency key."})
		res := patch.ResolveHunkAnchor(&h, duplicated)
		require.Equal(t, patch.AnchorUnanchored, res.Status)
		require.Contains(t, res.Reason, "ambiguous — 2 identical spans")
		require.Contains(t, res.Reason, "invariant 13")
	})

	t.Run("recorded lines out of bounds → unanchored (bounds sanity first, claims/anchor pattern)", func(t *testing.T) {
		h := hunk(hunkOpts{start: 99, deleteText: docLines[2]})
		res := patch.ResolveHunkAnchor(&h, doc)
		require.Equal(t, patch.AnchorUnanchored, res.Status)
		require.Contains(t, res.Reason, "outside the document bounds (1-6)")
	})

	t.Run("a recorded content_hash inconsistent with delete_text is a misquoted anchor → unanchored", func(t *testing.T) {
		h := hunk(hunkOpts{start: 3, deleteText: docLines[2], contentHashOverride: "deadbeef"})
		res := patch.ResolveHunkAnchor(&h, doc)
		require.Equal(t, patch.AnchorUnanchored, res.Status)
		require.Equal(t, "recorded content_hash does not match delete_text — misquoted anchor", res.Reason)
	})

	t.Run("a recorded range height inconsistent with delete_text is unanchored", func(t *testing.T) {
		// The recorded hash IS consistent with delete_text, but the recorded
		// range claims 1 line for a 3-line delete_text — the applier could never
		// splice that honestly.
		h := schemas.PatchHunk{
			Anchor: schemas.PatchAnchor{
				StartLine:   3,
				EndLine:     3,
				ContentHash: docutil.ContentHash("one\ntwo\nthree"),
			},
			DeleteText:   "one\ntwo\nthree",
			InsertText:   "",
			EvidenceRefs: defaultDocPatchEvidence(),
		}
		res := patch.ResolveHunkAnchor(&h, doc)
		require.Equal(t, patch.AnchorUnanchored, res.Status)
		require.Contains(t, res.Reason, "range height (1 lines) does not match delete_text (3 lines)")
	})

	t.Run("invalid recorded ranges (zero, reversed, sub-line) are unanchored", func(t *testing.T) {
		zero := hunk(hunkOpts{start: 0, deleteText: "x"})
		require.Equal(t, patch.AnchorUnanchored, patch.ResolveHunkAnchor(&zero, doc).Status)

		reversed := schemas.PatchHunk{
			Anchor:       schemas.PatchAnchor{StartLine: 5, EndLine: 3, ContentHash: "h"},
			DeleteText:   "x",
			InsertText:   "",
			EvidenceRefs: nil,
		}
		require.Equal(t, patch.AnchorUnanchored, patch.ResolveHunkAnchor(&reversed, doc).Status)
	})

	t.Run("pure insertions resolve at the recorded point (hash N/A), including the end-of-document gap", func(t *testing.T) {
		mid := hunk(hunkOpts{start: 2, deleteText: "", insert: "Inserted note."})
		midRes := patch.ResolveHunkAnchor(&mid, doc)
		require.Equal(t, patch.AnchorResolved, midRes.Status)
		require.NotNil(t, midRes.LineRange)
		require.Equal(t, 2, midRes.LineRange.StartLine)
		require.Equal(t, 2, midRes.LineRange.EndLine)
		require.False(t, midRes.ReAnchored)

		// start_line === lines.length + 1 is the end-of-document gap (append).
		appendH := hunk(hunkOpts{start: 7, deleteText: "", insert: "Appended."})
		appendRes := patch.ResolveHunkAnchor(&appendH, doc)
		require.Equal(t, patch.AnchorResolved, appendRes.Status)
		require.NotNil(t, appendRes.LineRange)
		require.Equal(t, 7, appendRes.LineRange.StartLine)
		require.Equal(t, 7, appendRes.LineRange.EndLine)
		require.False(t, appendRes.ReAnchored)
	})

	t.Run("insertion points out of bounds are unanchored; non-zero-width insertion anchors are unanchored", func(t *testing.T) {
		beyond := hunk(hunkOpts{start: 8, deleteText: "", insert: "x"})
		beyondRes := patch.ResolveHunkAnchor(&beyond, doc)
		require.Equal(t, patch.AnchorUnanchored, beyondRes.Status)
		require.Contains(t, beyondRes.Reason, "insertion point 8 is outside the document bounds (1-7)")

		wide := schemas.PatchHunk{
			Anchor:       schemas.PatchAnchor{StartLine: 2, EndLine: 4, ContentHash: "insertion-point"},
			DeleteText:   "",
			InsertText:   "x",
			EvidenceRefs: nil,
		}
		wideRes := patch.ResolveHunkAnchor(&wide, doc)
		require.Equal(t, patch.AnchorUnanchored, wideRes.Status)
		require.Contains(t, wideRes.Reason, "insertion anchor must be zero-width")
	})
}
