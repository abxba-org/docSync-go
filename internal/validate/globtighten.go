package validate

import (
	"strconv"
	"strings"
)

// Validator-level glob tightening (P0-T08; ARCH §11.3 hard errors; D8),
// ported from packages/core/src/validate/globtighten.ts.
//
// The shared guard (core guardGlob → internal/globs when landed) catches
// literal `.docsync`/`.github`+workflows segments, `..` escapes, and absolute
// paths — but documents two residual holes that only a semantic reachability
// analysis can close, and this module is the AUTHORITATIVE tightening (the
// globs wave defers it here):
//
//   - an unanchored leading globstar (a `**`-prefixed pattern, e.g. one whose
//     middle segment is `state`) can reach `.docsync/…` without any pattern
//     segment containing the literal name;
//   - a `.github/<star>` prefix followed by a globstar can expand onto
//     `workflows/`.
//
// GlobCanMatchUnder answers, conservatively: CAN this pattern match ANY
// repo-relative path whose leading segments equal forbiddenPrefixSegments?
// Only a YES is reported; uncertainty answers YES (D8 — reject more, never
// less) and the caller's message says so.
//
// Model: pattern segments consume path segments in order. Path positions
// `0..m-1` are pinned to the forbidden prefix; positions `>= m` are a FREE
// tail the analysis may choose at will. A literal pattern segment must equal
// the pinned path segment it consumes; ANY wildcard-bearing segment
// (`*?[]{}()` anywhere in the segment, conservative about extglobs and brace
// sets — a `{a,b}` set is treated as able to match anything) consumes one
// arbitrary pinned segment (picomatch runs with `dot: true`, so wildcards DO
// match dotted segments such as `.docsync`/`.github`); `**` consumes zero or
// more segments. Once the cursor reaches the free tail, any remaining pattern
// is satisfiable by construction (the tail is unconstrained), so reachability
// of position `m` decides the question.
//
// Limits (accepted, documented): brace/expansion segments are over-approximated
// as fully wild, so patterns like `{src,docs}/**` are conservatively rejected;
// negated extglobs (`!(…)`) are not modeled as exclusions. Both directions
// fail closed per D8.

// wildcardChars are the characters that make a pattern segment
// conservative-wild in this analysis.
const wildcardChars = "*?[]{}()"

func isWildcardSegment(segment string) bool {
	return strings.ContainsAny(segment, wildcardChars)
}

// GlobCanMatchUnder reports whether pattern can match some path whose first
// len(forbiddenPrefixSegments) segments are exactly forbiddenPrefixSegments.
// Pure; deterministic; never throws.
func GlobCanMatchUnder(pattern string, forbiddenPrefixSegments []string) bool {
	segments := strings.Split(pattern, "/")
	m := len(forbiddenPrefixSegments)
	failed := make(map[string]bool)

	// Can segments[pi..] consume path positions [pos..] where positions < m
	// are pinned to the forbidden prefix and positions >= m are free?
	var canConsumeFrom func(pi, pos int) bool
	canConsumeFrom = func(pi, pos int) bool {
		if pos >= m {
			return true
		}
		stateKey := strconv.Itoa(pi) + ":" + strconv.Itoa(pos)
		if failed[stateKey] {
			return false
		}
		reachable := false
		switch {
		case pi >= len(segments):
			reachable = false
		case segments[pi] == "**":
			// Consume 0..(m-pos) pinned segments (landing at or past m succeeds).
			for k := 0; k <= m-pos; k++ {
				if canConsumeFrom(pi+1, pos+k) {
					reachable = true
					break
				}
			}
		case isWildcardSegment(segments[pi]) || segments[pi] == forbiddenPrefixSegments[pos]:
			reachable = canConsumeFrom(pi+1, pos+1)
		}
		if !reachable {
			failed[stateKey] = true
		}
		return reachable
	}

	return canConsumeFrom(0, 0)
}

// Forbidden prefixes the manifest validator enforces (ARCH §11.3).
var (
	ForbiddenDocsyncPrefix   = []string{".docsync"}
	ForbiddenWorkflowsPrefix = []string{".github", "workflows"}
)
