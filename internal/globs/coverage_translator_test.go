package globs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Translators-level coverage for "stay total" defensive branches the public
// CompileGlob cannot reach (guards reject backslashes and empty patterns), and
// for fastpath/create edge shapes. Internal-only, deterministic.

func TestCOV_GLOBS_TranslatorEscapesAndLiteralBang(t *testing.T) {
	// A literal "!" that is NOT an extglob opener is quoted literal text.
	require.Equal(t, "a!b", (&translator{pattern: "a!b"}).translateAll())

	// An escaped char mid-pattern is quoted and consumed as a literal.
	require.Equal(t, "ab", (&translator{pattern: `a\b`}).translateAll())

	// A trailing backslash is emitted as a literal backslash (total).
	require.Equal(t, `\\`, (&translator{pattern: `\`}).translateAll())
}

func TestCOV_GLOBS_TranslatorFragmentAndFastpathEdges(t *testing.T) {
	// Fragment-final star: no trailing-slash tolerance inside a fragment.
	require.Equal(t, starAny, (&translator{pattern: "*", fragment: true}).translateAll())

	// fastpathEligible refuses the empty pattern (len<1).
	assert.False(t, fastpathEligible(""))

	// extWordSuffix non-final form returns base + `\.` + word.
	got, ok := fastpathCreate("*.md", false)
	require.True(t, ok)
	require.Equal(t, starAny+`\.`+`md`, got)
}

func TestCOV_GLOBS_TranslatorBracketEscapeAndInvalidRange(t *testing.T) {
	// A "\" inside a bracket content is copied verbatim (stay total).
	require.Equal(t, `[\]a]`, (&translator{pattern: `[\]a]`, fastpath: true}).translateAll())

	// A bracket that fails RE2 compilation falls back to the QUOTED RAW text
	// (never an uncompilable body).
	require.Equal(t, `\[z-a\]`, (&translator{pattern: "[z-a]", fastpath: true}).translateAll())
}

func TestCOV_GLOBS_FindCloseEscapes(t *testing.T) {
	// findBraceClose / findParenClose: a "\\" inside skips the next byte.
	_, ok := findBraceClose(`{\}`, 0)
	assert.False(t, ok)
	_, ok = findParenClose(`(\`, 0)
	assert.False(t, ok)
}
