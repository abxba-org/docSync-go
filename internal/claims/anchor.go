package claims

// Ported from core/src/claims/anchor.ts: content-hash-first anchoring
// (P1-T10; ARCH §4.3; invariant 13, V-P1-7). An anchor is (file + line range +
// CONTENT HASH of the quoted span). The hash is the identity: line numbers are
// a locator, never the truth.
//
// ResolveAnchor finds ALL spans in the document whose joined line content
// hashes to the recorded content_hash:
//   - 0 matches → unanchored ("doc edited") — routes REVIEW;
//   - >1 match → unanchored ("ambiguous") — an ambiguous re-anchoring is
//     NEVER applied silently (invariant 13): no automated anchor move happens
//     without a unique content-hash match;
//   - exactly 1 match at the recorded lines → unchanged;
//   - exactly 1 match elsewhere → re-anchored with the NEW line range — a
//     legitimate re-anchor (content hash matches uniquely), which the caller
//     persists via ApplyReanchor (anchor update + history event + run_id).
//
// Hash convention: HashSpan is the core state hash (docutil.ContentHash,
// sha256 hex, state/hash.ts) over the span's lines joined with "\n" — the
// exact bytes intake hashes and the exact bytes resolution compares. Line
// numbers are 1-based; a document's trailing newline does not count as an
// extra line. Sanity-first: recorded lines outside the document bounds →
// unanchored (line-number-second check, never a crash).

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AnchorResolution status values (the TS discriminated union's tag).
const (
	// AnchorUnchanged: exactly one hash match, at the recorded lines.
	AnchorUnchanged = "unchanged"
	// AnchorReAnchored: exactly one hash match elsewhere (a legitimate move).
	AnchorReAnchored = "re-anchored"
	// AnchorUnanchored: zero or ambiguous matches — routes REVIEW, never a
	// silent anchor move (invariant 13).
	AnchorUnanchored = "unanchored"
)

// LineRange is a 1-based inclusive line range.
type LineRange struct {
	StartLine int
	EndLine   int
}

// AnchorResolution is the outcome of ResolveAnchor: the TS union rendered as
// one value. Status carries the variant tag; StartLine/EndLine are the located
// span (unchanged/re-anchored), Previous is the recorded range before a
// re-anchor (nil otherwise), Reason explains an unanchored outcome ("" when
// not unanchored).
type AnchorResolution struct {
	Status    string
	StartLine int
	EndLine   int
	Previous  *LineRange
	Reason    string
}

// HashSpan is the sha256 hex of the quoted span: its lines joined with "\n"
// (the core hash convention).
func HashSpan(spanText string) string {
	return docutil.ContentHash(spanText)
}

// SplitDocLines splits document content into 1-addressable lines (a trailing
// newline is absorbed).
func SplitDocLines(docContent string) []string {
	lines := strings.Split(docContent, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// spanTextAt is the span text a (start, end) 1-based line range addresses.
func spanTextAt(lines []string, startLine, endLine int) string {
	return strings.Join(lines[startLine-1:endLine], "\n")
}

// ResolveAnchor resolves a claim's anchor against document content,
// content-hash-first. Pure: no I/O, no clock, no store — persistence is the
// caller's job (ApplyReanchor / MarkUnanchored). The anchor is accepted
// directly (the TS signature took a Claim | Pick<Claim, 'anchor'>; the anchor
// is all resolution reads).
func ResolveAnchor(anchor schemas.ClaimAnchor, docContent string) AnchorResolution {
	lines := SplitDocLines(docContent)

	// Line-number-second sanity: recorded lines must be within doc bounds.
	inBounds := anchor.StartLine >= 1 &&
		anchor.EndLine >= anchor.StartLine &&
		anchor.EndLine <= len(lines)
	if !inBounds {
		return AnchorResolution{
			Status: AnchorUnanchored,
			Reason: fmt.Sprintf(
				"recorded anchor lines %d-%d are outside the document bounds (1-%d)",
				anchor.StartLine, anchor.EndLine, len(lines)),
		}
	}

	// The recorded span content had exactly (end - start + 1) lines — identical
	// content is byte-identical, so any surviving occurrence has the same
	// height. Scan every window of that height for a unique hash match.
	spanHeight := anchor.EndLine - anchor.StartLine + 1
	matches := make([]LineRange, 0, 2)
	for start := 1; start+spanHeight-1 <= len(lines); start++ {
		end := start + spanHeight - 1
		if HashSpan(spanTextAt(lines, start, end)) == anchor.ContentHash {
			matches = append(matches, LineRange{StartLine: start, EndLine: end})
			if len(matches) > 1 {
				break // ambiguity is decisive; stop early
			}
		}
	}

	if len(matches) == 0 {
		return AnchorResolution{Status: AnchorUnanchored, Reason: "content hash matches nowhere — doc edited"}
	}
	if len(matches) > 1 {
		return AnchorResolution{
			Status: AnchorUnanchored,
			Reason: fmt.Sprintf(
				"ambiguous — %d identical spans (never re-anchor on ambiguity, invariant 13)",
				len(matches)),
		}
	}
	match := matches[0]
	if match.StartLine == anchor.StartLine && match.EndLine == anchor.EndLine {
		return AnchorResolution{
			Status:    AnchorUnchanged,
			StartLine: match.StartLine,
			EndLine:   match.EndLine,
		}
	}
	return AnchorResolution{
		Status:    AnchorReAnchored,
		StartLine: match.StartLine,
		EndLine:   match.EndLine,
		Previous:  &LineRange{StartLine: anchor.StartLine, EndLine: anchor.EndLine},
	}
}
