package globs_test

// COVERAGE tests targeting engine statements/branches not already exercised by
// the ported glob suites (fastpath shapes, extglob openers, bracket/brace
// edge cases, negation, guard kinds). Black-box, deterministic.

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runMatchCases2(t *testing.T, cases []matchCase) {
	t.Helper()
	for _, c := range cases {
		compiled, err := globs.CompileGlob(c.pattern)
		if err != nil {
			t.Errorf("CompileGlob(%q): unexpected error: %v", c.pattern, err)
			continue
		}
		assert.Equalf(t, c.want, compiled.Match(c.path),
			"CompileGlob(%q).Match(%q)", c.pattern, c.path)
	}
}

/* ----------------------------------------------------------- fastpath shapes */

func TestCOV_GLOBS_FastpathShapes(t *testing.T) {
	// Exact fastpath switch shapes (makeRe-level), final form.
	runMatchCases2(t, []matchCase{
		{"*", "a", true},
		{"*", "a/", true},
		{".*", ".hidden", true},
		{".*", "hidden", false},
		{"*.*", "a.b", true},
		{"*.*", "a.", false},
		{"*/*", "a/b", true},
		{"*/*", "/b", true},
		{"*/*", "a", false},
		{"**", "a/b", true},
		{"**", "a/", true},
		{"**", ".hidden", true},
		{"**/*", "a/b", true},
		{"**/*", "b", true},
		{"**/*", "a/b/c", true},
		{"**/*.*", "a.b", true},
		{"**/*.*", "a.", false},
		{"**/.*", ".a", true},
		{"**/.*", "a", false},
		// recursive ".word" fastpath forms.
		{"src.md", "src.md", true},
		{"src.md", "src.md/", false},
		{"*.md", "a.md", true},
		{"*.md", "a.md/", true},
	})
}

// TestCOV_GLOBS_FastpathEligible covers ineligible patterns that still compile
// through the translator (empty, leading '*'/'!', special chars).
func TestCOV_GLOBS_FastpathEligible(t *testing.T) {
	for _, c := range []matchCase{
		{"*x", "ax", true},
		{"!x", "x", false},
		{"a/b", "a/b", true},
		{"a(b)", "a(b)", true},
		{"a[b]", "a[b]", true},
	} {
		compiled, err := globs.CompileGlob(c.pattern)
		require.NoError(t, err, c.pattern)
		assert.Equalf(t, c.want, compiled.Match(c.path), "pattern %q path %q", c.pattern, c.path)
	}
}

/* ----------------------------------------------------------- extglob openers */

func TestCOV_GLOBS_ExtglobOpeners(t *testing.T) {
	runMatchCases2(t, []matchCase{
		// !(...) negated extglob approximation (matches any slash-free run).
		{"!(js)", "a.ts", true},
		{"!(js)", "a.js", true},
		{"!(js)", "a/b.js", false},
		// !(?...) / !(=...) / !(<...) / !(:...) are NOT extglobs.
		{"!(?a)", "a", true},
		{"!(?a)", "b", true},
		{"!(=a)", "a", true},
		// @( ) one-of, *( ) zero-or-more, +( ) one-or-more, ?( ) zero-or-one.
		{"@(a|b)", "ab", false},
		{"*(a|b)", "", false},
		{"+(a|b)", "", false},
		{"?(a|b)", "", false},
		{"?(a|b)", "ab", false},
		// Unterminated extgroup opener is literal text.
		{"@(a|b", "@(a|b", true},
		{"@(a|b", "a", false},
	})
}

/* ----------------------------------------------------------- brackets & braces */

func TestCOV_GLOBS_BracketsAndBraces(t *testing.T) {
	runMatchCases2(t, []matchCase{
		// negated class whose content contains "/" gets no extra "/".
		{"[^/]", "a", true},
		{"[^/]", "/", false},
		// maybe_slash: pattern ending in a bracket tolerates a trailing slash.
		{"[abc]", "a/", true},
		// multi-rune brace range kept literal (documented deviation).
		{"{10..12}", "{10..12}", true},
		{"{10..12}", "1", false},
		// degenerate / out-of-order ranges kept literal.
		{"{..x}", "{..x}", true},
		{"{x..}", "{x..}", true},
		{"{a.b..c}", "{a.b..c}", true},
		{"{z..a}", "{z..a}", true},
		// nested braces compose with wildcards.
		{"{a,{b,c}}", "c", true},
		{"{{a,b}}", "{a}", true},
		{"{{a,b}}", "{b}", true},
		{"{{a,b}}", "a", false},
		// brace without comma or range is literal.
		{"a{b}", "a{b}", true},
		{"a{b}", "ab", false},
		// unterminated brace is literal.
		{"{a,b", "{a,b", true},
		{"{a,b", "a", false},
	})
}

/* ----------------------------------------------------------- star / globstar / qmark */

func TestCOV_GLOBS_StarGlobstarQmark(t *testing.T) {
	runMatchCases2(t, []matchCase{
		// segment-final star before "/" (starSlashStar).
		{"src/*", "src/a", true},
		{"src/*", "src/", false},
		// pattern-final star (starTail) tolerates trailing slash.
		{"*", "a/", true},
		// non-segment-initial star at end (non-fastpath) tolerates "/".
		{"(a)*", "(a)", true},
		{"(a)*", "(a)/", true},
		{"(a)*", "a", false},
		// qmark directly after "(" is escaped.
		{"(?a)", "(?a)", true},
		{"(?a)", "?a", false},
		// globstar after a star segment keeps required separator.
		{"*/**", "a", false},
		{"*/**", "a/b", true},
		// consecutive globstars collapse.
		{"a/**/**", "a/x", true},
		{"a/**/**", "a", true},
		// brace + trailing globstar matches parent dir.
		{"{a,b}/**", "b", true},
	})
}

/* ----------------------------------------------------------- negation */

func TestCOV_GLOBS_NegationEdgeCases(t *testing.T) {
	runMatchCases2(t, []matchCase{
		// odd/even negation runs.
		{"!", "!", true},
		{"!", "a", true},
		{"!!*", "a", true},
		{"!!*", "!", true},
		{"***", "a", true},
		{"***", "a/b", true},
		// negated globstar across dotfiles.
		{"!**/*.yml", ".hidden/config.yml", false},
		{"!**/*.yml", "a/b.yml", false},
		{"!**/*.yml", "a/b.ts", true},
	})
}

/* ----------------------------------------------------------- MatchPaths */

func TestCOV_GLOBS_MatchPaths_GuardAbortsWholeCall(t *testing.T) {
	// One unsafe pattern aborts the whole call before any matching.
	matches, err := globs.MatchPaths(
		[]string{"src/a.ts"},
		[]globs.PatternEntry{
			{Index: 0, Pattern: "src/**"},
			{Index: 1, Pattern: "/abs/**"},
		},
	)
	require.Error(t, err)
	assert.Empty(t, matches)

	// Empty paths list yields empty results even with patterns.
	matches, err = globs.MatchPaths(
		nil,
		[]globs.PatternEntry{{Index: 0, Pattern: "src/**"}},
	)
	require.NoError(t, err)
	assert.Empty(t, matches)
}

// TestCOV_GLOBS_CompileGlob_AllCompile covers patterns exercising every
// translator branch compile to valid RE2.
func TestCOV_GLOBS_CompileGlob_AllCompile(t *testing.T) {
	for _, pattern := range []string{
		"src/**", "*.ts", "[a-z]", "{a,b}", "@(a|b)", "!(a|b)",
		"**/.*", "a/./b", "./src/**", "src/**/*.ts",
	} {
		compiled, err := globs.CompileGlob(pattern)
		require.NoError(t, err, "pattern %q", pattern)
		assert.NotNil(t, compiled)
		assert.Equal(t, pattern, compiled.Pattern)
	}
}

// TestCOV_GLOBS_GuardResultKinds covers each guard kind via GuardGlob.
func TestCOV_GLOBS_GuardResultKinds(t *testing.T) {
	assert.False(t, globs.GuardGlob("").OK)
	assert.Equal(t, globs.GuardEmpty, globs.GuardGlob("").Kind)

	assert.False(t, globs.GuardGlob(`src\x/**`).OK)
	assert.Equal(t, globs.GuardAbsoluteOrWindows, globs.GuardGlob(`src\x/**`).Kind)

	assert.False(t, globs.GuardGlob("../x/**").OK)
	assert.Equal(t, globs.GuardRepoEscape, globs.GuardGlob("../x/**").Kind)

	assert.False(t, globs.GuardGlob(".docsync/x").OK)
	assert.Equal(t, globs.GuardDocsyncInternal, globs.GuardGlob(".docsync/x").Kind)

	assert.False(t, globs.GuardGlob(".github/workflows/x").OK)
	assert.Equal(t, globs.GuardWorkflows, globs.GuardGlob(".github/workflows/x").Kind)

	assert.True(t, globs.GuardGlob("src/**").OK)
}

// TestCOV_GLOBS_SpecificityAndConflicts pins scoring/ordering/restrictiveness
// branches under the COV naming convention.
func TestCOV_GLOBS_SpecificityAndConflicts(t *testing.T) {
	assert.Equal(t, globs.GlobSpecificity{Segments: 3, LiteralChars: 13, DoubleStarCount: 1, WildcardStars: 0},
		globs.ScoreGlob("src/payments/**"))
	assert.Greater(t, globs.CompareSpecificity(
		globs.ScoreGlob("src/payments/refunds/**"), globs.ScoreGlob("src/payments/**")), 0)
	assert.Equal(t, 0, globs.CompareSpecificity(
		globs.ScoreGlob("src/a/**"), globs.ScoreGlob("src/b/**")))
	assert.Equal(t, globs.SpecificityKey("src/b/**"), globs.SpecificityKey("src/b/**"))

	modeOf := func(m schemas.DocMode) *schemas.DocMode { return &m }
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/a/x.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/a/**", Mode: modeOf(schemas.DocModeAuto)},
			{EntryIndex: 1, Pattern: "src/b/**", Mode: modeOf(schemas.DocModeLocked)},
		},
	})
	assert.Equal(t, globs.TieRestrictiveTie, resolution.TieKind)
	assert.Len(t, resolution.Winners, 1)
	assert.Equal(t, 1, resolution.Winners[0].EntryIndex)

	// equal-tie, both candidates, ordered by entryIndex.
	resolution = globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/a/x.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 5, Pattern: "src/b/**"},
			{EntryIndex: 2, Pattern: "src/a/**"},
		},
	})
	assert.Equal(t, globs.TieEqualTie, resolution.TieKind)
	assert.Equal(t, []int{2, 5}, winnerIndices(resolution))

	// no matches.
	resolution = globs.ResolveOverlap(globs.ResolveOverlapParams{Path: "src/a.ts"})
	assert.Equal(t, globs.TieUnique, resolution.TieKind)
	assert.Empty(t, resolution.Winners)
}

func winnerIndices(r globs.CandidateResolution) []int {
	out := make([]int, 0, len(r.Winners))
	for _, w := range r.Winners {
		out = append(out, w.EntryIndex)
	}
	return out
}
