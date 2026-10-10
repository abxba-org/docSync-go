package patch

// Ported from core/src/patch/anchor.ts — content-hash-first hunk-anchor
// resolution (P3-T08/T09; ARCH §4.3, §4.9; invariant 13, V-P3-3). Mirrors
// claims/anchor.go conventions exactly:
//
//   - An anchor is (line range + CONTENT HASH of the quoted span); the hash is
//     the IDENTITY, line numbers are a LOCATOR, never the truth.
//   - Hash convention: claims.HashSpan (docutil.ContentHash, sha256 hex) over the
//     span's lines joined with "\n" — the exact bytes the writer hashed and the
//     exact bytes resolution compares. 1-based lines; a document's trailing
//     newline does not count as an extra line.
//   - Sanity-first: a recorded line range outside the document bounds is
//     unanchored BEFORE any hash scan — an out-of-bounds recorded range means
//     the writer misquoted the document, and the conservative posture (invariant
//     13) is abstain, never a cross-bounds re-anchor.
//   - Then content-hash FIRST: the delete_text span is located by hashing every
//     window of its height — 0 matches → unanchored ('content matches nowhere');
//     >1 → unanchored ('ambiguous — never re-anchor on ambiguity, invariant 13');
//     exactly 1 → RESOLVED at its ACTUAL lines. A unique match at lines other
//     than the recorded ones is a LEGITIMATE re-anchor (content wins over lines).
//   - Pure-insertion hunks (delete_text === "") have no hash (N/A): they resolve
//     at the recorded line, which addresses the gap BEFORE that line
//     (start_line === lines.length + 1 is the end-of-document gap — append). The
//     insertion point must be within those bounds, else unanchored.

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// HunkLineRange (TS: HunkLineRange) is the 1-based inclusive line range a
// resolved hunk applies to.
type HunkLineRange struct {
	StartLine int
	EndLine   int
}

// HunkAnchorResolution status tags (the TS union's discriminant).
const (
	AnchorResolved   = "resolved"
	AnchorUnanchored = "unanchored"
)

// HunkAnchorResolution (TS: HunkAnchorResolution) is the outcome of resolving
// one hunk's anchor: Status selects the variant. For 'resolved', LineRange is
// the located range and ReAnchored reports whether it moved from the recorded
// lines; for 'unanchored', Reason names the decisive defect.
type HunkAnchorResolution struct {
	Status     string
	LineRange  *HunkLineRange
	ReAnchored bool
	Reason     string
}

// ResolveHunkAnchor (TS: resolveHunkAnchor) resolves one hunk's anchor against
// document content, content-hash-first (see module doc). Pure: no I/O, no clock
// — the applier owns sequencing.
func ResolveHunkAnchor(hunk *schemas.PatchHunk, docContent string) HunkAnchorResolution {
	lines := claims.SplitDocLines(docContent)
	startLine := hunk.Anchor.StartLine
	endLine := hunk.Anchor.EndLine
	contentHash := hunk.Anchor.ContentHash

	// Recorded-range sanity (mirrors claims/anchor.go: an invalid range is a
	// misquote — abstain before any hash work). Go ints are always integers, so
	// the TS Number.isInteger checks collapse into the bound checks.
	if startLine < 1 || endLine < startLine {
		return HunkAnchorResolution{
			Status: AnchorUnanchored,
			Reason: fmt.Sprintf("invalid recorded anchor range %d-%d", startLine, endLine),
		}
	}

	if len(hunk.DeleteText) == 0 {
		// Pure insertion — hash N/A. The insertion point addresses the gap BEFORE
		// start_line; start_line === lines.length + 1 is the end-of-document gap.
		if endLine != startLine {
			return HunkAnchorResolution{
				Status: AnchorUnanchored,
				Reason: fmt.Sprintf(
					"insertion anchor must be zero-width (start_line == end_line), got %d-%d",
					startLine, endLine),
			}
		}
		maxPoint := len(lines) + 1
		if startLine > maxPoint {
			return HunkAnchorResolution{
				Status: AnchorUnanchored,
				Reason: fmt.Sprintf(
					"insertion point %d is outside the document bounds (1-%d)", startLine, maxPoint),
			}
		}
		return HunkAnchorResolution{
			Status:     AnchorResolved,
			LineRange:  &HunkLineRange{StartLine: startLine, EndLine: endLine},
			ReAnchored: false,
		}
	}

	// Replacement/deletion: recorded lines must address real document lines.
	if endLine > len(lines) {
		return HunkAnchorResolution{
			Status: AnchorUnanchored,
			Reason: fmt.Sprintf(
				"recorded anchor lines %d-%d are outside the document bounds (1-%d)",
				startLine, endLine, len(lines)),
		}
	}

	// Self-consistency: the recorded content_hash IS the identity of the span the
	// writer intends to delete, so it must hash delete_text itself. A mismatch
	// means the writer misquoted its own anchor (fail closed — the applier splices
	// by line range, so an anchor inconsistent with its delete_text could never
	// be trusted).
	if claims.HashSpan(hunk.DeleteText) != contentHash {
		return HunkAnchorResolution{
			Status: AnchorUnanchored,
			Reason: "recorded content_hash does not match delete_text — misquoted anchor",
		}
	}

	// The recorded range height must agree with delete_text's line count: the
	// scan below compares windows of the recorded height against a hash over the
	// delete_text lines, so a height mismatch could never match anyway — name the
	// real defect instead of reporting a misleading 'matches nowhere'.
	spanHeight := endLine - startLine + 1
	deleteTextHeight := len(strings.Split(hunk.DeleteText, "\n"))
	if spanHeight != deleteTextHeight {
		return HunkAnchorResolution{
			Status: AnchorUnanchored,
			Reason: fmt.Sprintf(
				"recorded anchor range height (%d lines) does not match delete_text (%d lines)",
				spanHeight, deleteTextHeight),
		}
	}

	// Content-hash FIRST (invariant 13): scan every window of the delete_text
	// height for the recorded hash. Identical content is byte-identical, so any
	// surviving occurrence has the same height. Ambiguity stops the scan early.
	matches := make([]HunkLineRange, 0, 2)
	for start := 1; start+spanHeight-1 <= len(lines); start++ {
		end := start + spanHeight - 1
		window := strings.Join(lines[start-1:end], "\n")
		if claims.HashSpan(window) == contentHash {
			matches = append(matches, HunkLineRange{StartLine: start, EndLine: end})
			if len(matches) > 1 {
				break // ambiguity is decisive; stop early
			}
		}
	}

	if len(matches) == 0 {
		return HunkAnchorResolution{
			Status: AnchorUnanchored,
			Reason: "content hash matches nowhere — doc edited or delete_text misquoted",
		}
	}
	if len(matches) > 1 {
		return HunkAnchorResolution{
			Status: AnchorUnanchored,
			Reason: fmt.Sprintf(
				"ambiguous — %d identical spans (never re-anchor on ambiguity, invariant 13)",
				len(matches)),
		}
	}
	match := matches[0]
	return HunkAnchorResolution{
		Status:     AnchorResolved,
		LineRange:  &HunkLineRange{StartLine: match.StartLine, EndLine: match.EndLine},
		ReAnchored: match.StartLine != startLine || match.EndLine != endLine,
	}
}
