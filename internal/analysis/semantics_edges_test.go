package analysis_test

// Continue the YAML/env port hardening: the remaining resolution edges the
// eemeli core-schema rules define (JS exponent-form number rendering, the
// radix fallback, alias keys, export-prefixed env lines, empty env keys) plus
// the path-family edge for the empty path. All assertions go through the
// public surface (ConfigDiffFor / parseEnv via ConfigDiffFor / ClassifyPathFamily).

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DUDE616/docSync-go/internal/analysis"
)

func TestConfigDiffFor_YAMLNumberRenderingEdges_P1T05(t *testing.T) {
	t.Run("JS exponent form: 1e-7 renders 1e-7 and 1e22 renders 1e+22", func(t *testing.T) {
		// formatJSNumber switches to exponent form below 1e-6 and at/above
		// 1e21, then normalizes Go's zero-padded exponent (e-07 → e-7) to the
		// JS form. JSON.stringify prints 1e+22 with the plus sign.
		requireYAMLChanged(t, "small: 0.0000001\nlarge: 1e22\n", "small: 0.0000002\nlarge: 2e22\n",
			`config keys changed (2): large: 1e+22 → 2e+22; small: 1e-7 → 2e-7`)
	})

	t.Run("a hex value beyond int64 range stays the parser's string (conservative)", func(t *testing.T) {
		// Beyond int64, the Go parser cannot tag the value as a core type, so
		// the port keeps the parser's string rather than resolving it (the
		// radix fallback stays the defensive path it is).
		requireYAMLChanged(t, "x: 0xFFFFFFFFFFFFFFFFFFFFFFFF\n", "x: 1\n",
			`config keys changed (1): x: "0xFFFFFFFFFFFFFFFFFFFFFFFF" → 1`)
	})

	t.Run("an alias key stringifies through its target scalar", func(t *testing.T) {
		// The key `*key` resolves through the anchor to the scalar "mykey";
		// eemeli stringifies it like any other scalar key.
		requireYAMLChanged(t, "k: &key mykey\n*key: 1\n", "k: &key mykey\n*key: 2\n",
			`config keys changed (1): mykey: 1 → 2`)
	})
}

func TestConfigDiffFor_EnvEdgeLines_P1T05(t *testing.T) {
	t.Run("an export-prefixed line with a tab still parses", func(t *testing.T) {
		requireChanged(t, analysis.ConfigDiffFor(".env", "A=1\n", "export\tA=2\n"),
			`config keys changed (1): A: 1 → 2`)
	})

	t.Run("an empty env key is a parse failure (declared gap, never a guess)", func(t *testing.T) {
		requireGap(t, analysis.ConfigDiffFor(".env", "=value\n", "A=1\n"), yamlGapNote)
	})
}

// TestClassifyPathFamilyEmptyPath pins the node basename edge: the empty path
// classifies as "other" (node's posix basename("") is "", not ".").
func TestClassifyPathFamilyEmptyPath(t *testing.T) {
	assert.Equal(t, analysis.PathFamilyOther, analysis.ClassifyPathFamily(""))
	assert.Equal(t, analysis.PathFamilyConfig, analysis.ClassifyPathFamily(".env"))
	assert.Equal(t, analysis.PathFamilyConfig, analysis.ClassifyPathFamily("deep/nested/dir/.env.production"))
}
