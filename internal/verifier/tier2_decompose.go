package verifier

// Tier-2 claim decomposition (port of tier2/decompose.ts; P4-T08; PLAN
// §5.3/§5.4; ARCH §4.8 Tier-2): deterministic regex-based decomposition of the
// patched region — every hunk's `insert_text`, line by line — into per-claim
// units with a closed-vocabulary kind (tier2_types.go) that drives which Tier-2
// check applies.
//
// A4 fallback posture: the V1 extractors are DETERMINISTIC — NO model calls at
// this stage. Decomposition must be exactly replayable evidence (invariant 10 —
// provenance or it didn't happen), so a regex miss routes a line to kind
// 'other', never to a guessed kind, and markdown furniture (headings, bare list
// markers, table separators, fence markers, empty lines) is skipped rather than
// claimed (abstain, don't guess — §3 rule 6). Downstream, status.ts keys
// per-claim statuses by `index`, and the head-verify module reuses
// ExtractClaimedTokens for deterministic token re-extraction over a claim's
// `text`.
//
// Purity: pure and deterministic over the patch alone — no fs, no git, no
// network, no clock of its own, no Date/random. `line` is derived with the
// applier splice convention (schemas doc-patch.go applyHunksStructurally); a
// patch-only input cannot re-verify anchors against the document (that is
// Tier-1's applied check), so positions are COMPUTED per the convention, never
// silently guessed.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DecompositionStats is the aggregate decomposition shape for the W3c check
// record (composed in tier2.go): raw insert lines examined (furniture
// included), claims emitted, and per-kind counts. The type is pinned here so
// the composing check and its tests share one shape.
type DecompositionStats struct {
	// LinesScanned is the raw insert lines examined across all hunks,
	// including skipped furniture.
	LinesScanned int `json:"lines_scanned"`
	// Claims is the number of claims actually emitted (the decomposition
	// array length).
	Claims int `json:"claims"`
	// ByKind holds the per-kind claim counts; every vocabulary kind is
	// present (0 when none).
	ByKind map[DecomposedClaimKind]int `json:"by_kind"`
}

/* --------------------------------------------- deterministic extractors */

// Backticked `…` spans of a line, in appearance order (contents only).
var tier2BacktickSpanRE = regexp.MustCompile("`([^`]+)`")

// Backticked error-code token: an HTTP status (1xx–5xx) or `E_<UPPER_SNAKE>`.
var tier2ErrorCodeTokenRE = regexp.MustCompile("^(?:[1-5][0-9]{2}|E_[A-Z0-9_]+)$")

// Error-code phrasing: bare 3-digit `returns … on`, status-code/raise talk.
var tier2ErrorCodeLineRE = regexp.MustCompile("(?i)returns?\\s+`?\\d{3}`?\\s+on|status code|raises?|error code")

// Call/signature-shaped span: a dotted identifier path + parenthesized args.
var tier2SignatureSpanRE = regexp.MustCompile("^[A-Za-z_$][\\w$]*(?:\\.[\\w$]+)*\\(.*\\)$")

// Value-shaped phrasing, paired with at least one backticked claimed value.
var tier2ValueLineRE = regexp.MustCompile("(?i)defaults? to|is set to|returns?|by default|environment variable")

// Behavioral-predicate phrasing (idempotence, retries, ordering, delivery).
var tier2BehavioralLineRE = regexp.MustCompile("(?i)\\b(?:idempoten\\w*|retr\\w*|order\\w*|exactly once|at least once|at most once|concurrent\\w*|sequen\\w*)")

/* ------------------------------------------- markdown furniture (no claims) */

// Whitespace-only line.
var tier2EmptyLineRE = regexp.MustCompile("^\\s*$")

// Fence marker line (opening or closing, tagged or not) — skipped entirely.
var tier2FenceMarkerLineRE = regexp.MustCompile("^\\s*```")

// ATX heading (hashes then whitespace or end of line).
var tier2HeadingLineRE = regexp.MustCompile("^\\s*#{1,6}(?:\\s|$)")

// A line that is ONLY a list marker (`-`, `*`, digits with `.` or `)`).
var tier2BareListMarkerLineRE = regexp.MustCompile("^\\s*(?:[-*]|\\d+[.)])\\s*$")

// Table separator / rule row: only pipes, dashes, colons, spaces.
var tier2TableSeparatorLineRE = regexp.MustCompile("^[|\\s:-]+$")

// Leading list marker on a CONTENT line (stripped for matching only).
var tier2LeadingListMarkerRE = regexp.MustCompile("^\\s*(?:[-*]|\\d+[.)])\\s+")

/* ------------------------------------------------------------------ helpers */

// splitInsertLines is the insert lines of one hunk, line by line (the core
// document-line convention: a single trailing newline is absorbed, matching
// patch/apply.go and the test patch builder's splitDocLines).
func tier2SplitInsertLines(insertText string) []string {
	lines := strings.Split(insertText, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// tier2BacktickSpans is the `…` span contents of a line, in appearance order.
func tier2BacktickSpans(line string) []string {
	matches := tier2BacktickSpanRE.FindAllStringSubmatch(line, -1)
	spans := make([]string, 0, len(matches))
	for _, match := range matches {
		spans = append(spans, match[1])
	}
	return spans
}

// tier2PatchedLine is the claim's 1-based line in the PATCHED document:
// `start_line + offset`, the applier splice convention — replacement and
// pure-insertion hunks both land inserted line `offset` at `start_line +
// offset` (zero-width anchors insert BEFORE the anchor line).
// Replacement/insertion hunks — the only hunks that yield lines — always
// resolve; a shape without a computable position returns nil rather than
// guessing (fail-closed). Per-hunk only: cross-hunk shifts of later anchors
// are not composed in V1 (the patch-only signature cannot see the document;
// each hunk is anchored independently).
func tier2PatchedLine(hunk *schemas.PatchHunk, offset int) *int {
	if hunk.InsertText == "" {
		return nil
	}
	line := hunk.Anchor.StartLine + offset
	return &line
}

// tier2IsFurniture is markdown furniture / emptiness — lines that NEVER become
// claims (skipped, not classified): empty lines, fence markers, headings, bare
// list markers, table separators. Content lines INSIDE a fenced block are
// still claims — only the marker lines are furniture.
func tier2IsFurniture(line string) bool {
	return tier2EmptyLineRE.MatchString(line) ||
		tier2FenceMarkerLineRE.MatchString(line) ||
		tier2HeadingLineRE.MatchString(line) ||
		tier2BareListMarkerLineRE.MatchString(line) ||
		tier2TableSeparatorLineRE.MatchString(line)
}

// tier2ClassifyLine is the closed-vocabulary kind for one non-furniture line —
// first match wins, in the precedence order error_code → signature → value →
// behavioral → other (anything unmatched is 'other', never a guessed kind —
// §3 rule 6).
func tier2ClassifyLine(line string) DecomposedClaimKind {
	spans := tier2BacktickSpans(line)
	for _, span := range spans {
		if tier2ErrorCodeTokenRE.MatchString(span) {
			return DecomposedClaimKindErrorCode
		}
	}
	if tier2ErrorCodeLineRE.MatchString(line) {
		return DecomposedClaimKindErrorCode
	}
	for _, span := range spans {
		if tier2SignatureSpanRE.MatchString(span) {
			return DecomposedClaimKindSignature
		}
	}
	if tier2ValueLineRE.MatchString(line) && len(spans) > 0 {
		return DecomposedClaimKindValue
	}
	if tier2BehavioralLineRE.MatchString(tier2LeadingListMarkerRE.ReplaceAllString(line, "")) {
		return DecomposedClaimKindBehavioral
	}
	return DecomposedClaimKindOther
}

/* --------------------------------------------------------------- exports */

// DecomposePatchClaims decomposes the patched region — every hunk's
// `insert_text`, line by line, in `patch.hunks` array order — into per-claim
// units. Claims are dense 0-based over ALL hunks (`index` increments across
// hunks, hunk order = array order); `existing_claim_id` links a hunk's claims
// to its FIRST `claim_ids` entry when the claim store covers the anchor;
// furniture lines produce no claims. Deterministic and pure (module doc): the
// same patch always yields deep-equal output. A nil patch (a caller contract
// violation the schema makes unreachable) yields no claims rather than
// panicking; RunTier2 refuses a nil patch outright.
func DecomposePatchClaims(patch *schemas.DocPatch) []DecomposedClaim {
	claims := make([]DecomposedClaim, 0)
	if patch == nil {
		return claims
	}
	for hunkIndex := range patch.Hunks {
		hunk := &patch.Hunks[hunkIndex]
		var existingClaimID string
		if len(hunk.ClaimIDs) > 0 {
			existingClaimID = hunk.ClaimIDs[0]
		}
		lines := tier2SplitInsertLines(hunk.InsertText)
		for offset, text := range lines {
			if tier2IsFurniture(text) {
				continue
			}
			claim := DecomposedClaim{
				Index:     len(claims),
				Text:      text,
				Kind:      tier2ClassifyLine(text),
				HunkIndex: hunkIndex,
			}
			if existingClaimID != "" {
				claim.ExistingClaimID = existingClaimID
			}
			if line := tier2PatchedLine(hunk, offset); line != nil {
				claim.Line = line
			}
			claims = append(claims, claim)
		}
	}
	return claims
}

// ExtractClaimedTokens is deterministic token re-extraction over a decomposed
// claim's `text` — the head-verify module's reuse surface (the claimed value is
// extracted again, never re-inferred): for kind 'signature' the FULL backticked
// call/signature span(s); for 'value'/'error_code' the backticked tokens, in
// appearance order without dedup. 'behavioral' and 'other' carry no
// deterministic token surface (empty array). Error-code claims raised WITHOUT
// a backticked token (bare `returns 404 on …` phrasing) also yield an empty
// array — the bare number stays in `text` for the head-verify check to
// consume.
func ExtractClaimedTokens(text string, kind DecomposedClaimKind) []string {
	spans := tier2BacktickSpans(text)
	switch kind {
	case DecomposedClaimKindSignature:
		signatureSpans := make([]string, 0, len(spans))
		for _, span := range spans {
			if tier2SignatureSpanRE.MatchString(span) {
				signatureSpans = append(signatureSpans, span)
			}
		}
		return signatureSpans
	case DecomposedClaimKindValue, DecomposedClaimKindErrorCode:
		return spans
	default:
		return []string{}
	}
}

// tier2SortDecomposedByIndex returns a copy of the claims sorted by index, so
// unordered input stays deterministic (TS: `[...decomposed].sort((a, b) =>
// a.index - b.index)` — the copies are never mutated, so a stable sort is
// unnecessary; Go's sort.Slice is fine).
func tier2SortDecomposedByIndex(decomposed []DecomposedClaim) []DecomposedClaim {
	sorted := make([]DecomposedClaim, len(decomposed))
	copy(sorted, decomposed)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Index < sorted[j].Index })
	return sorted
}
