package globs

import (
	"fmt"
	"strings"
)

// Glob specificity scoring and ordering (TS specificity.ts; P0-T10;
// ARCH §11.3 "glob overlap" rule; OD-R02: picomatch behind a thin wrapper).
// Specificity is a deterministic heuristic used to decide which manifest
// entry wins when several `sources` globs match the same changed file:
//
//	(1) more "/" segments wins;
//	(2) tie → more literal (non-wildcard) characters wins;
//	(3) tie → fewer "**" (globstar) occurrences wins;
//	(4) tie → fewer single "*" occurrences wins;
//	(5) tie → lexicographically greater pattern wins (final tie-break for
//	    stable sorting only — see SpecificityKey).
//
// Rules (1)–(4) are implemented by CompareSpecificity. Rule (5) deliberately
// lives in SpecificityKey, NOT in CompareSpecificity: the GlobSpecificity
// shape carries no pattern string, and patterns with equal metrics are
// exactly the §11.3 "equal specificity" ties that must route a document to
// BOTH candidates, so winner selection must observe them as ties. Folding the
// pattern tie-break into CompareSpecificity would silently turn equal ties
// into unique winners. SpecificityKey encodes rules (1)–(5) into one canonical
// string so that sorting keys with a descending codepoint comparator yields a
// total, deterministic order over patterns.

// metaChars are treated as glob syntax (not literal) when counting
// literalChars (TS META_CHARS).
var metaChars = map[rune]bool{
	'*': true, '?': true, '[': true, ']': true,
	'{': true, '}': true, '(': true, ')': true, '!': true,
}

// GlobSpecificity is the pinned specificity metrics of a glob (TS
// GlobSpecificity).
type GlobSpecificity struct {
	// Segments is the number of "/"-separated components (a "**" counts as a
	// segment, so pattern depth decides rule (1)).
	Segments int `json:"segments"`
	// LiteralChars counts characters that are NOT glob metacharacters —
	// everything else, including "/" separators, dots and dashes, is literal.
	LiteralChars int `json:"literalChars"`
	// DoubleStarCount counts runs of ≥2 consecutive "*" (each run counts once).
	DoubleStarCount int `json:"doubleStarCount"`
	// WildcardStars counts single-"*" runs.
	WildcardStars int `json:"wildcardStars"`
}

// ScoreGlob scores a glob pattern. Pure and deterministic; accepts ANY string
// (escape guards are a separate concern — see GuardGlob).
//
//   - `segments`: components when split on "/" — a `**` segment counts as a
//     segment, so pattern depth decides rule (1) (`src/payments/refunds/**`
//     beats `src/payments/**`).
//   - `literalChars`: characters that are NOT glob metacharacters (`*`, `?`,
//     `[`, `]`, `{`, `}`, `(`, `)`, `!`); everything else — including "/"
//     separators, dots and dashes — counts as literal. Scoring is a stable
//     heuristic for ordering, not a parse of picomatch's grammar.
//   - `doubleStarCount`: runs of ≥2 consecutive `*` (each run counts once —
//     `***` is at least as coarse as `**`).
//   - `wildcardStars`: single-`*` runs.
func ScoreGlob(pattern string) GlobSpecificity {
	if len(pattern) == 0 {
		return GlobSpecificity{}
	}
	var (
		literalChars    int
		doubleStarCount int
		wildcardStars   int
		starRun         int
	)
	flushStarRun := func() {
		switch {
		case starRun == 1:
			wildcardStars++
		case starRun >= 2:
			doubleStarCount++
		}
		starRun = 0
	}
	for _, ch := range pattern {
		if ch == '*' {
			starRun++
			continue
		}
		flushStarRun()
		if !metaChars[ch] {
			literalChars++
		}
	}
	flushStarRun()
	return GlobSpecificity{
		Segments:        strings.Count(pattern, "/") + 1,
		LiteralChars:    literalChars,
		DoubleStarCount: doubleStarCount,
		WildcardStars:   wildcardStars,
	}
}

// CompareSpecificity compares two specificity scores. Returns > 0 when a is
// MORE SPECIFIC than b, < 0 when less specific, 0 when their metrics tie
// (rules (1)–(4)).
//
// A 0 return means "equal specificity" per §11.3: such ties are resolved by
// restrictiveness or routed to BOTH candidates — they are deliberately NOT
// broken here; the rule-(5) total order for stable sorting lives in
// SpecificityKey.
func CompareSpecificity(a, b GlobSpecificity) int {
	if a.Segments != b.Segments {
		return boolToOrder(a.Segments > b.Segments)
	}
	if a.LiteralChars != b.LiteralChars {
		return boolToOrder(a.LiteralChars > b.LiteralChars)
	}
	if a.DoubleStarCount != b.DoubleStarCount {
		return boolToOrder(a.DoubleStarCount < b.DoubleStarCount)
	}
	if a.WildcardStars != b.WildcardStars {
		return boolToOrder(a.WildcardStars < b.WildcardStars)
	}
	return 0
}

func boolToOrder(more bool) int {
	if more {
		return 1
	}
	return -1
}

const (
	// keyFieldWidth is the zero-padded width of every numeric key field.
	keyFieldWidth = 10
	// keyCountCeiling is the room for inverted counts: fewer wildcards must
	// encode LARGER.
	keyCountCeiling = 999_999_999
)

func direct(value int) string {
	return fmt.Sprintf("%0*d", keyFieldWidth, min(value, keyCountCeiling))
}

func inverted(value int) string {
	return fmt.Sprintf("%0*d", keyFieldWidth, keyCountCeiling-min(value, keyCountCeiling))
}

// SpecificityKey is the canonical string encoding of a pattern's specificity
// for STABLE SORTING. Shape: `<segments>|<literalChars>|<inverted **count>|
// <inverted *count>|<pattern>` with zero-padded fixed-width numeric fields;
// the `**`/`*` counts are inverted so that FEWER wildcards encode larger.
//
// Sort keys with a DESCENDING codepoint comparator, e.g.
// `sort.Slice(keys, func(i, j int) bool { return keys[i] > keys[j] })`:
// descending key order is exactly rules (1)–(4) followed by rule (5) — among
// metric ties the lexicographically greater pattern sorts first (the raw
// pattern is the last field). Two identical patterns produce identical keys;
// distinct metric-tied patterns differ in the trailing pattern field, so the
// order is total.
func SpecificityKey(pattern string) string {
	score := ScoreGlob(pattern)
	return strings.Join([]string{
		direct(score.Segments),
		direct(score.LiteralChars),
		inverted(score.DoubleStarCount),
		inverted(score.WildcardStars),
		pattern,
	}, "|")
}
