package globs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCOV_GLOBS_FastpathCreateAllShapes(t *testing.T) {
	// Every fastpathCreate switch case, final and non-final, plus the
	// default extWordSuffix branch and the final "not ok" return.
	for _, c := range []struct {
		pattern string
		final   bool
		want    string
		ok      bool
	}{
		{"*", true, starTail, true},
		{"*", false, starAny, true},
		{".*", true, `\.` + dotStarTail, true},
		{".*", false, `\.` + starAny, true},
		{"*.*", true, starAny + `\.` + dotStarTail, true},
		{"*.*", false, starAny + `\.` + starAny, true},
		{"*/*", true, starSlashStar + `/` + starTail, true},
		{"*/*", false, starSlashStar + `/` + starAny, true},
		{"**", true, globstarBody + `(?:/)?`, true},
		{"**", false, globstarBody, true},
		{"**/*", true, `(?:` + globstarBody + `/)?` + starTail, true},
		{"**/*", false, `(?:` + globstarBody + `/)?` + starAny, true},
		{"**/*.*", true, `(?:` + globstarBody + `/)?` + starAny + `\.` + dotStarTail, true},
		{"**/*.*", false, `(?:` + globstarBody + `/)?` + starAny + `\.` + starAny, true},
		{"**/.*", true, `(?:` + globstarBody + `/)?` + `\.` + dotStarTail, true},
		{"**/.*", false, `(?:` + globstarBody + `/)?` + `\.` + starAny, true},
		{"a.md", true, "", false}, // default: base "a" is not a fastpath shape
		{"a.md", false, "", false},
	} {
		got, ok := fastpathCreate(c.pattern, c.final)
		assert.Equalf(t, c.want, got, "fastpathCreate(%q, %v)", c.pattern, c.final)
		assert.Equalf(t, c.ok, ok, "fastpathCreate(%q, %v) ok", c.pattern, c.final)
	}
}

func TestCOV_GLOBS_BangOpensExtglob(t *testing.T) {
	assert.True(t, bangOpensExtglob("!(a|b)"))
	assert.True(t, bangOpensExtglob("!(!a|b)"))
	assert.True(t, bangOpensExtglob("!(:a|b)"))
	assert.True(t, bangOpensExtglob("!(?a|b)"))
	assert.False(t, bangOpensExtglob("!(?=a|b)"))
	assert.False(t, bangOpensExtglob("!(?<a|b)"))
	assert.False(t, bangOpensExtglob("!(?!a|b)"))
	assert.False(t, bangOpensExtglob("!(?:a|b)"))
	assert.True(t, bangOpensExtglob("!(a"))
	assert.False(t, bangOpensExtglob("a!"))
	assert.False(t, bangOpensExtglob("!"))
	assert.False(t, bangOpensExtglob("x"))
	assert.False(t, bangOpensExtglob(""))
}

func TestCOV_GLOBS_SplitTopLevel(t *testing.T) {
	require.Equal(t, []string{"a", "b"}, splitTopLevel("a,b", ','))
	require.Equal(t, []string{"a\\(b)"}, splitTopLevel("a\\(b)", ','))
	require.Equal(t, []string{"a", "[b,c]"}, splitTopLevel("a,[b,c]", ','))
	require.Equal(t, []string{"a", "(x|y)"}, splitTopLevel("a,(x|y)", ','))
	require.Equal(t, []string{"a", "{b,c}"}, splitTopLevel("a,{b,c}", ','))
	require.Equal(t, []string{"a\\\\", "b"}, splitTopLevel("a\\\\,b", ','))
	require.Equal(t, []string{"x"}, splitTopLevel("x", ','))
	require.Equal(t, []string{"", ""}, splitTopLevel(",", ','))
}

func TestCOV_GLOBS_DotRange(t *testing.T) {
	lo, hi, ok := dotRange("a..c")
	require.True(t, ok)
	assert.Equal(t, "a", lo)
	assert.Equal(t, "c", hi)
	_, _, ok = dotRange("..x")
	assert.False(t, ok)
	_, _, ok = dotRange("x..")
	assert.False(t, ok)
	_, _, ok = dotRange("a.b..c")
	assert.False(t, ok)
	_, _, ok = dotRange("10..12")
	assert.False(t, ok)
	_, _, ok = dotRange("z..a")
	assert.False(t, ok)
	_, _, ok = dotRange("no-dots")
	assert.False(t, ok)
}

func TestCOV_GLOBS_FindBraceClose(t *testing.T) {
	var ok bool
	_, ok = findBraceClose("{a,b}", 0)
	require.True(t, ok)
	_, ok = findBraceClose("{[a]}b}", 0)
	require.True(t, ok)
	_, ok = findBraceClose("no brace", 0)
	require.False(t, ok)
}

func TestCOV_GLOBS_FindParenClose(t *testing.T) {
	var ok bool
	_, ok = findParenClose("(a|b)", 0)
	require.True(t, ok)
	_, ok = findParenClose("no paren", 0)
	require.False(t, ok)
}

func TestCOV_GLOBS_SkipClass(t *testing.T) {
	// "]" as first content char is a literal member.
	assert.Equal(t, 3, skipClass("[]x]", 0))
	// "^"/"!" first char is negation.
	assert.Equal(t, 3, skipClass("[!a]", 0))
	assert.Equal(t, 3, skipClass("[^a]", 0))
	// escaped char inside is skipped.
	assert.Equal(t, 4, skipClass("[\\]a]", 0))
	// unterminated class returns end-1.
	assert.Equal(t, 1, skipClass("[a", 0))
}
