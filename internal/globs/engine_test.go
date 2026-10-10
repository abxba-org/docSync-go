package globs_test

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matchCase is one (pattern, path) expectation. Every expectation in this file
// was verified against the real picomatch with the pinned { dot: true,
// windows: false } options (differential run over 167k combinations; see the
// package doc for the two documented deviations).
type matchCase struct {
	pattern string
	path    string
	want    bool
}

func runMatchCases(t *testing.T, cases []matchCase) {
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

func TestCompileGlob_GuardEnforcementPoint(t *testing.T) {
	// TS: compileGlob('../escape/**') throws GlobGuardError with the
	// EDOCSYNC_GLOB code and the guard kind (D8: the guard is enforced here).
	compiled, err := globs.CompileGlob("../escape/**")
	require.Error(t, err)
	assert.Nil(t, compiled)

	var guardErr *globs.GlobGuardError
	require.True(t, errors.As(err, &guardErr), "error must be a *globs.GlobGuardError")
	assert.Equal(t, "EDOCSYNC_GLOB", guardErr.Code())
	assert.Equal(t, docutil.CodeOf(err), globs.CodeGlob)
	assert.Equal(t, globs.GuardRepoEscape, guardErr.Kind)
	assert.Equal(t, "../escape/**", guardErr.Pattern)
	assert.Equal(t,
		`EDOCSYNC_GLOB: glob pattern "../escape/**" rejected (repo_escape): glob pattern "../escape/**" contains a ".." segment and escapes the repository (hard error per ARCH §11.3)`,
		guardErr.Error())

	// The run must not proceed with an unsafe glob: no compiled matcher
	// escapes the guard, whatever the pattern.
	for _, pattern := range []string{
		"/abs/**", "C:/repo/**", `src\x/**`, ".docsync/state/**",
		"**/.docsync/**", "docs/.docsync-backup/**", ".github/workflows/ci.yml",
		".github/**", "**/.github/workflows/**", "",
	} {
		_, err := globs.CompileGlob(pattern)
		var g *globs.GlobGuardError
		assert.Truef(t, errors.As(err, &g), "pattern %q must be rejected", pattern)
		assert.Equalf(t, "EDOCSYNC_GLOB", docutil.CodeOf(err), "pattern %q", pattern)
	}
}

func TestCompileGlob_SafePatternsWithSpecificity(t *testing.T) {
	compiled, err := globs.CompileGlob("src/**/*.ts")
	require.NoError(t, err)
	assert.Equal(t, "src/**/*.ts", compiled.Pattern)
	assert.Equal(t, globs.ScoreGlob("src/**/*.ts"), compiled.Specificity)
	assert.True(t, compiled.Match("src/a/b.ts"))
	assert.False(t, compiled.Match("docx/a.ts"))
}

func TestCompileGlob_DotTrueHiddenFiles(t *testing.T) {
	// { dot: true }: hidden files and dotted directories match wildcards.
	compiled, err := globs.CompileGlob("**/*.yml")
	require.NoError(t, err)
	assert.True(t, compiled.Match(".hidden/config.yml"))
	assert.True(t, compiled.Match("a/b/c.yml"))
	assert.True(t, compiled.Match("config.yml"))
}

func TestMatch_PicomatchQuirksPreserved(t *testing.T) {
	// picomatch.test rejects an empty path for every pattern.
	for _, pattern := range []string{"**", "*", "**/*", "!*", "!.ts"} {
		compiled, err := globs.CompileGlob(pattern)
		require.NoError(t, err)
		assert.Falsef(t, compiled.Match(""), "pattern %q must not match the empty path", pattern)
	}
	// picomatch.test short-circuits on input === glob, before (and regardless
	// of) negation — so a brace pattern also matches its own literal text.
	for _, c := range []matchCase{
		{"{a,b}", "{a,b}", true},
		{"a{b}", "a{b}", true},
		{"**/x", "**/x", true},
		{"!*.ts", "!*.ts", true},
		{"[abc]", "[abc]", true},
		{"[!abc]", "[!abc]", true},
	} {
		runMatchCases(t, []matchCase{c})
	}
}

func TestGlobMatching_Globstar(t *testing.T) {
	runMatchCases(t, []matchCase{
		// `**` spans zero or more segments, across "/".
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "src/a/b.ts", true},
		{"src/**/*.ts", "src/a/b/c.ts", true},
		{"src/**/*.ts", "docx/a.ts", false},
		{"src/**/*.ts", "src/.hidden.ts", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a//b", true},
		{"a/**/b", "ab", false},
		{"a/**/b", "a/../b", false},
		{"**/x", "x", true},
		{"**/x", "a/x", true},
		{"**/x", "a/b/x", true},
		{"**/x", "x/", false},
		{"**", "a/b", true},
		{"**", "a/", true},
		{"**", ".hidden", true},
		{"**", "a/.", false},
		{"docs/**", "docs", true},
		{"docs/**", "docs/a/b.md", true},
		{"docs/**", "docs/.hidden/a", true},
		{"docs/**", "src/a", false},
		{"src/payments/**", "src/payments", true},
		{"src/payments/**", "src/payments/", true},
		{"src/payments/**", "src/payments/refunds/refund.ts", true},
		{"src/payments/**", "srcx/a", false},
		{"src/**/", "src/", true},
		{"src/**/", "src/a/", true},
		{"src/**/", "src", false},
		// consecutive globstars collapse (`**/**` ≡ `**`).
		{"**/**", "a/b", true},
		{"**/**/x", "x", true},
		{"**/**/x", "a/x", true},
		{"src/**/**/*.ts", "src/a/b.ts", true},
		// a globstar after a star segment keeps a required separator.
		{"*/**", "a", false},
		{"*/**", "a/b", true},
		{"*/**", "a/b/c", true},
		// brace + trailing globstar still matches the parent directory.
		{"{a,b}/**", "a", true},
	})
}

func TestGlobMatching_Star(t *testing.T) {
	runMatchCases(t, []matchCase{
		{"*", "a", true},
		{"*", "a/", true},
		{"*", "/", true},
		{"*", ".hidden", true},
		{"*", ".", false},
		{"*", "..", false},
		{"*", "./x", false},
		{"*.ts", "a.ts", true},
		{"*.ts", ".ts", true},
		{"*.ts", ".a.ts", true},
		{"*.ts", "a/b.ts", false},
		{"src/*.ts", "src/a.ts", true},
		{"src/*.ts", "src/.hidden.ts", true},
		{"src/*.ts", "src/a.ts/", false},
		{"src/*", "src/a", true},
		{"src/*", "src//", true},
		{"src/*", "src/.hidden", true},
		{"src/*", "src/", false},
		{"src/*", "src/a/b", false},
		{"a*", "a", true},
		{"a*", "a/", false},
		{"a*", "/", false},
		{"*.*", "a.b", true},
		{"*.*", ".b", true},
		{"*.*", "x.y.z", true},
		{"*.*", "a.", false},
		// trailing-slash tolerance: `*.md` tolerates it, `src/*.md` does not.
		{"*.md", "a.md/", true},
		{"x/*", "x/a/", true},
	})
}

func TestGlobMatching_Question(t *testing.T) {
	runMatchCases(t, []matchCase{
		{"?", "a", true},
		{"?", ".", true},
		{"?", "ab", false},
		{"?", "/", false},
		{"a?b", "axb", true},
		{"a?b", "a/b", false},
		{"a?", "ab", true},
		{"a?", "ab/", false},
		{"src/?", "src/a", true},
		{"src/?", "src//", false},
		{"src/?", "src/", false},
	})
}

func TestGlobMatching_CharacterClasses(t *testing.T) {
	runMatchCases(t, []matchCase{
		{"[abc]", "a", true},
		{"[abc]", "d", false},
		{"[abc]", "a/", true},
		{"[abc]", "/", false},
		{"[a-z]", "m", true},
		{"[a-z]", "-", false},
		{"[a-z]", "5", false},
		{"[!abc]", "a", true},
		{"[!abc]", "!", true},
		{"[!abc]", "d", false},
		{"[^abc]", "d", true},
		{"[^abc]", "a", false},
		{"[^abc]", "/", false},
		{"[^abc]", "^", true},
		{"[[:alpha:]]", "b", true},
		{"[[:alpha:]]", "5", false},
		{"[[:digit:]]", "5", true},
		{"[^[:digit:]]", "x", true},
		{"[^[:digit:]]", "5", false},
		{"[]a]", "a", true},
		{"[]a]", "]", true},
		{"[]]", "]", true},
		{"[a-]", "-", true},
		{"[-a]", "-", true},
	})
}

func TestGlobMatching_Braces(t *testing.T) {
	runMatchCases(t, []matchCase{
		{"{a,b}", "a", true},
		{"{a,b}", "b", true},
		{"{a,b}", "c", false},
		{"src/{a,b}.ts", "src/a.ts", true},
		{"src/{a,b}.ts", "src/b.ts", true},
		{"src/{a,b}.ts", "src/c.ts", false},
		{"{a,{b,c}}", "a", true},
		{"{a,{b,c}}", "b", true},
		{"{a,{b,c}}", "c", true},
		{"{a,{b,c}}", "d", false},
		{"{src,docs}/*.md", "src/a.md", true},
		{"{src,docs}/*.md", "docs/a.md", true},
		{"{src,docs}/*.md", "x/src/a.md", false},
		{"{a..c}", "a", true},
		{"{a..c}", "b", true},
		{"{a..c}", "d", false},
		// a brace without a comma or range is literal (braces stay braces).
		{"a{b}", "a{b}", true},
		{"a{b}", "ab", false},
		{"a{b,c}d", "abd", true},
		{"a{b,c}d", "acd", true},
		{"a{b,c}d", "a{b,c}d", true},
		// braces nest and compose with wildcards.
		{"{a,{b,c}d}", "bd", true},
		{"x{a,y}b", "xab", true},
		{"x{a,y}b", "xyb", true},
		{"{a,b}*", "a", true},
		{"{a,b}*", "ab", true},
		{"**/{a,b}", "a", true},
		{"**/{a,b}", "b", true},
		{"src/**/{a,b}", "src/x/a", true},
		// classes inside braces/extglobs translate recursively.
		{"{[abc],d}", "a", true},
		{"{[abc],d}", "d", true},
		{"{[abc],d}", "e", false},
		{"@([a-c]|x)", "b", true},
		{"@([a-c]|x)", "x", true},
		{"@([a-c]|x)", "y", false},
		{"@(a|b)*", "ab", true},
		// unterminated braces/brackets are literal text.
		{"{a,b", "{a,b", true},
		{"[a", "[a", true},
		// multi-rune brace ranges: picomatch emits a degenerate character
		// class (so it matches the single characters "0"/"1"/"2"); the port
		// keeps the brace literal instead. Documented deviation.
		{"{10..12}", "{10..12}", true},
	})
}

func TestGlobMatching_MultiRuneBraceRangeDeviation(t *testing.T) {
	// picomatch compiles `{10..12}` to the character class [10-12], which
	// matches the single characters {0,1,2}; the port keeps the brace literal
	// (conservative: matches nothing real). Documented deviation — docSync
	// manifest globs do not use brace ranges.
	compiled, err := globs.CompileGlob("{10..12}")
	require.NoError(t, err)
	assert.True(t, compiled.Match("{10..12}"))
	assert.False(t, compiled.Match("0"), "DEVIATION: picomatch matches '0' here")
	assert.False(t, compiled.Match("1"))
}

func TestGlobMatching_Negation(t *testing.T) {
	runMatchCases(t, []matchCase{
		{"!*.ts", "a.ts", false},
		{"!*.ts", "a.js", true},
		{"!*.ts", "a.ts/", true},
		{"!docs/**", "docs/a.md", false},
		{"!docs/**", "src/a.ts", true},
		{"!src/**", "src", false},
		{"!src/**", "src/a/b", false},
		// an even number of leading "!" cancels the negation.
		{"!!a", "a", true},
		{"!!a", "!a", false},
		{"!!!a", "a", false},
	})
}

func TestGlobMatching_Dotfiles(t *testing.T) {
	runMatchCases(t, []matchCase{
		// { dot: true }: wildcards reach dotfiles and dotted directories
		// without the pattern itself starting with a dot.
		{"**/*.yml", ".hidden/config.yml", true},
		{"**/*", ".hidden/a", true},
		{"docs/**", "docs/.hidden/a", true},
		{"src/*", "src/.hidden", true},
		{"src/*.ts", "src/.hidden.ts", true},
		{"**/*.ts", "src/.hidden.ts", true},
		// a literal leading dot still anchors the pattern.
		{".hidden/**", ".hidden/file", true},
		{".hidden/**", ".hidden/a/b", true},
		{".hidden/**", "hidden/file", false},
		{".*", ".hidden", true},
		{".*", ".a", true},
		{".*", ".", false},
		{".*", "./a", false},
	})
}

func TestMatchPaths_PerPathMatchesInInputOrder(t *testing.T) {
	// TS: matchPaths reports per-path matches in input order.
	matches, err := globs.MatchPaths(
		[]string{"src/a.ts", "docs/x.md"},
		[]globs.PatternEntry{
			{Index: 0, Pattern: "src/**/*.ts"},
			{Index: 1, Pattern: "docs/*.md"},
			{Index: 2, Pattern: "src/payments/**"},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []globs.GlobMatch{
		{Pattern: "src/**/*.ts", EntryIndex: 0, Path: "src/a.ts"},
		{Pattern: "docs/*.md", EntryIndex: 1, Path: "docs/x.md"},
	}, matches)
}

func TestMatchPaths_MatchesHiddenPaths(t *testing.T) {
	matches, err := globs.MatchPaths(
		[]string{".hidden/file"},
		[]globs.PatternEntry{{Index: 0, Pattern: ".hidden/**"}},
	)
	require.NoError(t, err)
	assert.Equal(t, []globs.GlobMatch{
		{Pattern: ".hidden/**", EntryIndex: 0, Path: ".hidden/file"},
	}, matches)
}

func TestMatchPaths_FailsClosedBeforeAnyMatching(t *testing.T) {
	// One unsafe pattern aborts the whole call with a GlobGuardError before
	// any path is matched (D8).
	matches, err := globs.MatchPaths(
		[]string{"src/a.ts"},
		[]globs.PatternEntry{{Index: 0, Pattern: "../*.ts"}},
	)
	require.Error(t, err)
	assert.Empty(t, matches)
	var guardErr *globs.GlobGuardError
	require.True(t, errors.As(err, &guardErr))
	assert.Equal(t, globs.GuardRepoEscape, guardErr.Kind)
	assert.Equal(t, "EDOCSYNC_GLOB", guardErr.Code())
}

func TestMatchPaths_EmptyInputsYieldEmptyResults(t *testing.T) {
	matches, err := globs.MatchPaths(nil, nil)
	require.NoError(t, err)
	assert.Empty(t, matches)

	matches, err = globs.MatchPaths([]string{"src/a.ts"}, nil)
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestMatchPaths_GroupedPerChangedPath(t *testing.T) {
	matches, err := globs.MatchPaths(
		[]string{"src/a.ts", "src/a.ts", "docs/x.md"},
		[]globs.PatternEntry{
			{Index: 0, Pattern: "**/*.ts"},
			{Index: 1, Pattern: "src/**"},
			{Index: 2, Pattern: "docs/**"},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []globs.GlobMatch{
		{Pattern: "**/*.ts", EntryIndex: 0, Path: "src/a.ts"},
		{Pattern: "src/**", EntryIndex: 1, Path: "src/a.ts"},
		{Pattern: "**/*.ts", EntryIndex: 0, Path: "src/a.ts"},
		{Pattern: "src/**", EntryIndex: 1, Path: "src/a.ts"},
		{Pattern: "docs/**", EntryIndex: 2, Path: "docs/x.md"},
	}, matches)
}

func TestGlobMatching_Extglobs(t *testing.T) {
	// picomatch extglob groups (verified against the real picomatch).
	runMatchCases(t, []matchCase{
		{"@(a|b)", "a", true},
		{"@(a|b)", "b", true},
		{"@(a|b)", "ab", false},
		{"@(a|b)", "c", false},
		{"+(a|b)", "b", true},
		{"+(a|b)", "", false},
		{"*(a)", "aaa", true},
		{"*(a)", "b", false},
		{"?(a)", "a", true},
		{"?(a)", "b", false},
		{"?(a)", "", false},
		{"x@(a|b)y", "xay", true},
		{"x@(a|b)y", "xby", true},
		{"x@(a|b)y", "xcy", false},
		{"src/@(a|b)/*.ts", "src/a/x.ts", true},
		{"src/@(a|b)/*.ts", "src/c/x.ts", false},
		{"@(src|docs)/*.md", "src/a.md", true},
	})
}

func TestGlobMatching_NegatedExtglobApproximation(t *testing.T) {
	// The negated extglob `!(...)` compiles to a negative lookahead in
	// picomatch, which RE2 cannot express; the port matches any slash-free
	// run (`negExtglob`), which agrees with picomatch except that it ALSO
	// matches the excluded alternatives. Documented deviation — docSync
	// manifest globs never use extglobs.
	compiled, err := globs.CompileGlob("*.!(js)")
	require.NoError(t, err)
	assert.True(t, compiled.Match("a.ts"), "non-excluded extension matches")
	assert.True(t, compiled.Match("a.js"), "DEVIATION: the excluded extension also matches")

	// The still-safe direction: `!.github/**` is guard-rejected before any
	// matching, so the approximation never reaches guard-rejected patterns
	// through CompileGlob.
	_, err = globs.CompileGlob("!.github/**")
	require.Error(t, err)
}

func TestGlobMatching_LiteralFallbacks(t *testing.T) {
	// Extglob openers that are not followed by "(" are ordinary text, as are
	// bare parens.
	runMatchCases(t, []matchCase{
		{"a@b", "a@b", true},
		{"a@b", "ab", false},
		{"a@", "a@", true},
		{"a+b", "a+b", true},
		{"x+y", "x+y", true},
		{"a(b)c", "a(b)c", true},
		{"a)b", "a)b", true},
	})
}

func TestGlobMatching_FastpathShapes(t *testing.T) {
	// picomatch's makeRe-level fastpath switch shapes and their recursive
	// ".word" forms (verified against the real picomatch).
	runMatchCases(t, []matchCase{
		{"**/*.*", "a.b", true},
		{"**/*.*", "a.", false},
		{"**/.*", ".a", true},
		{"*/*", "a/b", true},
		{"*/*", "/b", true},
		{"./*.md", "a.md", true},
		{"!./*.md", "a.md", false},
		{"!./*.md", "b.ts", true},
	})
}

func TestCompileGlob_PosixAndDotSegments(t *testing.T) {
	// A literal "." segment is preserved mid-pattern, and POSIX separators are
	// the only separators (windows paths never compile — see guards).
	compiled, err := globs.CompileGlob("a/./b")
	require.NoError(t, err)
	assert.True(t, compiled.Match("a/./b"))
	assert.False(t, compiled.Match("a/b"))

	compiled, err = globs.CompileGlob("./src/**")
	require.NoError(t, err)
	assert.True(t, compiled.Match("src/a.ts"))
}
