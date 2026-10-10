package cli_test

import (
	"regexp"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parsedOK(t *testing.T, argv []string) cli.ParsedInvocation {
	t.Helper()
	result := cli.ParseCliArgsWithEnv(argv, map[string]string{})
	require.Empty(t, result.Error, "expected no parse error for %v", argv)
	return result
}

func TestParseCliArgsDefaults(t *testing.T) {
	result := parsedOK(t, []string{"check"})
	assert.Equal(t, cli.VerbCheck, result.Verb)
	assert.False(t, result.JSON)
	assert.False(t, result.Strict)
	assert.False(t, result.Offline)
	assert.True(t, result.Quiet)
	assert.Equal(t, cli.GlyphsUnicode, result.Glyphs)
}

func TestParseCliArgsToleratesFlagsAnywhere(t *testing.T) {
	result := parsedOK(t, []string{"--json", "fix", "--strict", "--offline", "--no-quiet", "--verbose"})
	assert.Equal(t, cli.VerbFix, result.Verb)
	assert.True(t, result.JSON)
	assert.True(t, result.Strict)
	assert.True(t, result.Offline)
	assert.True(t, result.Verbose)
	assert.False(t, result.Quiet)
}

func TestParseCliArgsDocSelectors(t *testing.T) {
	result := parsedOK(t, []string{"fix", "--doc", "docs/a.md", "--doc", "docs/b.md"})
	assert.Equal(t, []string{"docs/a.md", "docs/b.md"}, result.DocPaths)
}

func TestParseCliArgsAllowDirty(t *testing.T) {
	assert.True(t, parsedOK(t, []string{"fix", "--allow-dirty"}).AllowDirty)
}

func TestParseCliArgsSourcePr(t *testing.T) {
	result := parsedOK(t, []string{"pr", "--source-pr", "42"})
	require.NotNil(t, result.SourcePr)
	assert.Equal(t, 42, *result.SourcePr)
}

func TestParseCliArgsRejectsNonNumericSourcePr(t *testing.T) {
	result := cli.ParseCliArgsWithEnv([]string{"pr", "--source-pr", "nope"}, map[string]string{})
	require.Error(t, errFromString(result.Error))
	assert.Regexp(t, regexp.MustCompile(`--source-pr`), result.Error)
	assert.Equal(t, cli.CliVerb(""), result.Verb)
}

func TestParseCliArgsInitForce(t *testing.T) {
	assert.True(t, parsedOK(t, []string{"init", "--force"}).Force)
}

func TestParseCliArgsGlyphs(t *testing.T) {
	assert.Equal(t, cli.GlyphsUnicode, parsedOK(t, []string{"check", "--glyphs", "unicode"}).Glyphs)
	assert.Equal(t, cli.GlyphsFallback, parsedOK(t, []string{"check", "--glyphs", "fallback"}).Glyphs)
}

func TestParseCliArgsRejectsUnknownGlyphs(t *testing.T) {
	result := cli.ParseCliArgsWithEnv([]string{"check", "--glyphs", "emoji"}, map[string]string{})
	assert.Regexp(t, regexp.MustCompile(`--glyphs`), result.Error)
}

func TestParseCliArgsGlyphsEnvDefault(t *testing.T) {
	result := cli.ParseCliArgsWithEnv([]string{"check"}, map[string]string{"DOC_SYNC_GLYPHS": "fallback"})
	require.Empty(t, result.Error)
	assert.Equal(t, cli.GlyphsFallback, result.Glyphs)
}

func TestParseCliArgsHelpAnywhere(t *testing.T) {
	assert.True(t, parsedOK(t, []string{"check", "--help"}).Help)
	assert.True(t, parsedOK(t, []string{"-h", "fix"}).Help)
}

func TestParseCliArgsUnknownVerb(t *testing.T) {
	result := cli.ParseCliArgsWithEnv([]string{"frobnicate"}, map[string]string{})
	assert.Equal(t, cli.CliVerb(""), result.Verb)
	assert.Regexp(t, regexp.MustCompile(`(?i)unknown verb`), result.Error)
}

func TestParseCliArgsUnexpectedPositional(t *testing.T) {
	result := cli.ParseCliArgsWithEnv([]string{"check", "extra"}, map[string]string{})
	assert.Regexp(t, regexp.MustCompile(`(?i)unexpected argument`), result.Error)
}

func TestParseCliArgsAuditTriggerValidation(t *testing.T) {
	nightly := parsedOK(t, []string{"audit", "--trigger", "nightly"})
	require.NotNil(t, nightly.AuditTrigger)
	assert.Equal(t, "nightly", *nightly.AuditTrigger)
	postMerge := parsedOK(t, []string{"audit", "--trigger", "post-merge"})
	require.NotNil(t, postMerge.AuditTrigger)
	assert.Equal(t, "post-merge", *postMerge.AuditTrigger)

	bad := cli.ParseCliArgsWithEnv([]string{"audit", "--trigger", "on-push"}, map[string]string{})
	assert.Regexp(t, regexp.MustCompile(`(?i)--trigger`), bad.Error)
}

func TestUsageTextListsVerbsAndExitCodes(t *testing.T) {
	text := cli.UsageText()
	for _, verb := range []string{"check", "fix", "pr", "init"} {
		assert.Contains(t, text, verb)
	}
	assert.Regexp(t, regexp.MustCompile(`0\s+no drift detected`), text)
	assert.Regexp(t, regexp.MustCompile(`1\s+drift detected`), text)
	assert.Regexp(t, regexp.MustCompile(`2\s+configuration/manifest error`), text)
	assert.Regexp(t, regexp.MustCompile(`3\s+runtime failure`), text)
}

// errFromString adapts the parse-error string into an error for require.Error.
func errFromString(msg string) error {
	if msg == "" {
		return nil
	}
	return &stringError{msg}
}

type stringError struct{ msg string }

func (e *stringError) Error() string { return e.msg }
