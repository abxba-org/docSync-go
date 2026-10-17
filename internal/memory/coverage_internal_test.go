package memory

// Internal coverage for the YAML emitter helpers: several flat branches
// (document-marker root handling, newline-run indenters, the backslash /
// carriage-return JSON escapes) are not exercised through the public
// serialize→parse surface because the top-level emit context always carries a
// non-empty indent. Exercising them directly pins the emitter's flat behavior.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCOV_INTERNAL_MEMORY_yamlNewlineRunIndenters(t *testing.T) {
	assert.Equal(t, "a\n\n  b\n  c", yamlIndentNewlineRuns("a\n\nb\nc", "  "))
	assert.Equal(t, "a\nb", yamlIndentNewlineRuns("a\nb", ""))
	assert.Equal(t, "no-newline", yamlIndentNewlineRuns("no-newline", "  "))

	assert.Equal(t, "a\n\n\n  b\n\n  c", yamlIndentNewlineRunsOpen("a\n\nb\nc", "  "))
	assert.Equal(t, "single\n\ndigit", yamlIndentNewlineRunsOpen("single\ndigit", ""))
	assert.Equal(t, "plain", yamlIndentNewlineRunsOpen("plain", "  "))

	assert.Equal(t, "x\n\n  y", yamlIndentTrailingNewlineRuns("x\n\ny", "  "))
	assert.Equal(t, "a\n  b", yamlIndentTrailingNewlineRuns("a\nb", "  "))
	assert.Equal(t, "x\n\n\ny", yamlIndentTrailingNewlineRuns("x\n\n\ny", ""))
	assert.Equal(t, "none", yamlIndentTrailingNewlineRuns("none", "  "))
}

func TestCOV_INTERNAL_MEMORY_yamlPlainAndQuotedDocumentMarkers(t *testing.T) {
	// A single-line document marker at the root (empty indent) forces the
	// indent step so the emitted document stays single-document.
	plain := yamlPlainString("---x", yamlScalarCtx{indent: ""})
	assert.Contains(t, plain, "|")
	assert.Contains(t, plain, "  ---x")

	// Same for the single- and double-quoted styles (multi-line values show
	// the applied indent step on the continuation lines).
	single := yamlSingleQuoted("---x\ny", yamlScalarCtx{indent: ""})
	assert.Equal(t, "'---x\n\n  y'", single)
	double := yamlDoubleQuoted("---x\nabcdefghijklmnopqrstuvwxyz0123456789", yamlScalarCtx{indent: ""})
	assert.Equal(t, `"---x`+"\n\n"+`  abcdefghijklmnopqrstuvwxyz0123456789"`, double)
}

func TestCOV_INTERNAL_MEMORY_jsonStringifyBackslashAndCR(t *testing.T) {
	assert.Equal(t, `"a\\b"`, jsonStringifyString("a\\b"))
	assert.Equal(t, `"a\rb"`, jsonStringifyString("a\rb"))
	assert.Equal(t, `"a\tb"`, jsonStringifyString("a\tb"))
	assert.Equal(t, `"a\bb"`, jsonStringifyString("a\bb"))
	assert.Equal(t, `"a\fb"`, jsonStringifyString("a\fb"))
	assert.Equal(t, `"a\u0002b"`, jsonStringifyString("a\x02b"))
}

func TestCOV_INTERNAL_MEMORY_yamlQuotedPreference(t *testing.T) {
	// Quote-preference: double quote wins when both present; a value with
	// only double quotes prefers single quotes; only single quotes → double.
	ctx := yamlScalarCtx{indent: "  "}
	assert.True(t, strings.HasPrefix(yamlQuotedString(`a"b"c`, ctx), "'"))
	assert.True(t, strings.HasPrefix(yamlQuotedString(`it's`, ctx), "\""))
	assert.True(t, strings.HasPrefix(yamlQuotedString(`both " and '`, ctx), "\""))
}

func TestCOV_INTERNAL_MEMORY_yamlBlockChompingAndIndent(t *testing.T) {
	// Chomp semantics: strip (-), keep (+), clip (default) plus the
	// leading-indent indicator path.
	ctx := yamlScalarCtx{indent: "  "}
	block := yamlBlockString("line1\nline2", ctx)
	assert.Contains(t, block, "|-")
	block = yamlBlockString("line1\n\n", ctx)
	assert.Contains(t, block, "|+")
	block = yamlBlockString("line1\n", ctx)
	assert.Contains(t, block, "|\n")

	// A value that cannot be a block (trailing whitespace) falls back to a
	// quoted scalar.
	quoted := yamlBlockString("\"dq\"\n ", ctx)
	assert.True(t, strings.HasPrefix(quoted, "\"\\\"dq"))
}

func TestCOV_INTERNAL_MEMORY_emitterRemainingFlatBranches(t *testing.T) {
	// Block-scalar document marker at the root (empty indent) forces the
	// indent step onto the scalar header.
	block := yamlBlockString("---x\ny", yamlScalarCtx{indent: ""})
	assert.Equal(t, "|-\n  ---x\n  y", block)

	// implicitKey with a newline always quotes (double style).
	quoted := yamlPlainString("with line\nbreak", yamlScalarCtx{indent: "  ", implicitKey: true})
	assert.Equal(t, `"with line\nbreak"`, quoted)
}
