package analysis_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// openApiCanonical with NaN through YAML
func TestCOV4_OPENAPI_CanonicalInfinity(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: .nan\n", "x: .inf\n")
	require.NotNil(t, result)
}

// describeValue with number
func TestCOV4_OPENAPI_DescribeNumber(t *testing.T) {
	before := map[string]any{"openapi": float64(1.0)}
	after := map[string]any{"openapi": "3.0.0"}
	_, err := analysis.ClassifyOpenApiDelta(before, after)
	require.Error(t, err)
}

// renderKeyChange with absent
func TestCOV4_SEMANTICS_RenderKeyChange(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":1}`, `{}`)
	requireChanged(t, result, `config keys changed (1): a: 1 → absent`)
}

// canonicalOrNull with NaN
func TestCOV4_SEMANTICS_CanonicalNaN(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: .nan\n", "x: 5\n")
	require.NotNil(t, result)
}

// yamlMappingKey null key
func TestCOV4_SEMANTICS_YAML_NullKey(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "~: value\n", "~: other\n")
	require.NotNil(t, result)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, ":")
}

// yamlNodeValue sequence with non-seq tag
func TestCOV4_SEMANTICS_YAML_SeqNonSeq(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: !!set {a}\n", "x: !!set {b}\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

// yamlScalarValue merge key
func TestCOV4_SEMANTICS_YAML_MergeKey(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "<<: merge\n", "<<: other\n")
	require.NotNil(t, result)
}

// yamlScalarValue non-core explicit tag
func TestCOV4_SEMANTICS_YAML_NonCoreTag(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: !!timestamp 2026-01-01\n", "x: !!timestamp 2026-01-02\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

// yamlScalarValue empty tag with resolved
func TestCOV4_SEMANTICS_YAML_EmptyTag(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: 42\n", "x: 43\n")
	requireChanged(t, result, `config keys changed (1): x: 42 → 43`)
}

// yamlScalarValue plain core tag mismatch
func TestCOV4_SEMANTICS_YAML_PlainCoreTag(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: 0x1A\n", "x: 0x2B\n")
	requireChanged(t, result, `config keys changed (1): x: 26 → 43`)
}

// parseEnv tab prefix export
func TestCOV4_SEMANTICS_Env_TabExport(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "export\tA=1\n", "export\tA=2\n")
	requireChanged(t, result, `config keys changed (1): A: 1 → 2`)
}

// parseEnv empty key after export
func TestCOV4_SEMANTICS_Env_EmptyKey(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "export =1\n", "export =2\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

// formatConfigValue bool false
func TestCOV4_SEMANTICS_FormatBoolFalse(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":true}`, `{"a":false}`)
	requireChanged(t, result, `config keys changed (1): a: true → false`)
}

// jsonStringLiteral special chars
func TestCOV4_SEMANTICS_JsonStringLiteral(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":"hello\nworld"}`, `{"a":"goodbye\nworld"}`)
	requireChanged(t, result, `config keys changed (1): a: "hello\nworld" → "goodbye\nworld"`)
}

// diffConfigMaps both absent
func TestCOV4_SEMANTICS_DiffBothAbsent(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":1}`, `{"a":1}`)
	requireUnchanged(t, result)
}

// indexOfAnalyzerName via OD-4 violation
func TestCOV4_PLUGIN_IndexOfAnalyzer(t *testing.T) {
	ts := fixedOutputPlugin{name: schemas.AnalyzerNameTypeScript, output: tsStubAnalyzerOutput()}
	generic := fixedOutputPlugin{name: schemas.AnalyzerNameGeneric, output: genericAnalyzerOutput()}
	_, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{ts, generic}, &analysis.ComposeAnalyzersOptions{
		Order: []schemas.AnalyzerName{schemas.AnalyzerNameGeneric, schemas.AnalyzerNameTypeScript},
	})
	require.Error(t, err)
}

// containsString via duplicate avoidance
func TestCOV4_GENERATED_ContainsString_DupAvoid(t *testing.T) {
	input := analysis.GeneratedRoutingInput{
		RunID: "r1", BaseSha: "b1", HeadSha: "h1",
		Files: []analysis.GeneratedRoutingFile{
			{DetectorFile: analysis.DetectorFile{Path: "src/api/generated/client.ts"}},
		},
		Docs: []analysis.GeneratedRoutingDoc{
			{Path: "docs/api.md", Content: "The client module handles..."},
			{Path: "docs/api.md", Content: "The client module handles again..."},
		},
	}
	record := analysis.InspectGeneratedOutput(input)
	require.NotNil(t, record)
	assert.Len(t, record.AffectedDocs, 1)
}

// newJSONLSink with relative stateDir
func TestCOV4_GENERATED_NewJSONLSink_Relative(t *testing.T) {
	_, err := analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{StateDir: "relative/path"})
	require.Error(t, err)
}

// appendLine success
func TestCOV4_GENERATED_AppendLine_Success(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "feedback"), 0o755))
	opts := analysis.FeedbackRecorderOptions{StateDir: dir, Clock: func() string { return "2026-01-01T00:00:00Z" }}
	event, err := analysis.RecordFeedbackEvent(opts, analysis.FeedbackEventInput{
		DecisionID: "d", PolicyVersion: 1, Signal: "reject",
		RejectReason: strPtr("reason"), At: strPtr("2026-01-01T00:00:00Z"),
	})
	require.NoError(t, err)
	require.NotNil(t, event)
}

// isJSONSafe nested map unsafe
func TestCOV4_CONFIGDIFF_IsJSONSafe_NestedMap(t *testing.T) {
	_, err := analysis.DiffConfigKeys(
		map[string]any{"a": map[string]any{"b": make(chan int)}},
		map[string]any{"a": 1})
	require.Error(t, err)
}

// configNumber int64
func TestCOV4_CONFIGDIFF_ConfigNumber_Int64(t *testing.T) {
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"a": int64(1 << 40)},
		map[string]any{"a": int64(1 << 41)})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
}

// configNumber uint64
func TestCOV4_CONFIGDIFF_ConfigNumber_Uint64(t *testing.T) {
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"a": uint64(100)},
		map[string]any{"a": uint64(200)})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
}

// isJSONSafe array unsafe
func TestCOV4_CONFIGDIFF_IsJSONSafe_ArrayUnsafe(t *testing.T) {
	_, err := analysis.DiffConfigKeys(
		map[string]any{"a": []any{make(chan int)}},
		map[string]any{"a": 1})
	require.Error(t, err)
}

// mustCompileGlobs via test-only
func TestCOV4_DETECT_MustCompileGlobs(t *testing.T) {
	assert.True(t, analysis.IsTestOnlyFile("src/x.test.ts"))
	assert.True(t, analysis.IsTestOnlyFile("tests/x.ts"))
}
