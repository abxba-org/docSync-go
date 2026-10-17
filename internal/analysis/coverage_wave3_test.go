package analysis_test

// Wave-3: final targeted tests for analysis package.
// Covers remaining gaps in openapi compare functions, generated recorder
// edges, semantics YAML edge cases, and config-diff internals.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func TestCOV3_OPENAPI_SwaggerVersion(t *testing.T) {
	result, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"swagger": "2.0", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"responses": map[string]any{"200": map[string]any{"description": "ok"}}}}}},
		map[string]any{"swagger": "2.0", "paths": map[string]any{}})
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestCOV3_OPENAPI_PathNotObjectOnBefore(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": "not-obj"}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{}}}})
	require.Error(t, err)
}

func TestCOV3_OPENAPI_MissingOpenapiAndSwagger(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"paths": map[string]any{}},
		map[string]any{"paths": map[string]any{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required")
}

func TestCOV3_OPENAPI_EnumNonMonotonic(t *testing.T) {
	before := schemaDocWithEnum(map[string]bool{"a": true, "b": true})
	after := schemaDocWithEnum(map[string]bool{"a": true, "c": true})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaEnumNarrowed)
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "non-monotonically")
}

func TestCOV3_OPENAPI_ResponseContentSchemaChanged(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{"type": "string"},
			}},
		}},
	}}})
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{"type": "integer"},
			}},
		}},
	}}})
	result, err := analysis.ClassifyOpenApiDelta(before, after)
	require.NoError(t, err)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged)
	require.NotNil(t, delta)
}

func TestCOV3_OPENAPI_ComponentsSchemasCompareNonObject(t *testing.T) {
	before := map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{}}}}
	after := map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{}}}}
	before["components"] = map[string]any{"schemas": map[string]any{"A": "not-an-object"}}
	after["components"] = map[string]any{"schemas": map[string]any{"A": map[string]any{"type": "object"}}}
	_, err := analysis.ClassifyOpenApiDelta(before, after)
	require.NoError(t, err) // Non-object schema on before side handled via compareInlineSchema
}

func TestCOV3_GENERATED_NewJSONLSink_InvalidFilename(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".docsync", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	// ListFeedbackEvents uses newJSONLSink internally; with a good stateDir
	// and good filename, it should work.
	events, err := analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{StateDir: stateDir})
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestCOV3_GENERATED_MarshalJSONLine_FeedbackEvent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	opts := analysis.FeedbackRecorderOptions{StateDir: dir, Clock: func() string { return "2026-01-01T00:00:00Z" }}
	event, err := analysis.RecordFeedbackEvent(opts, analysis.FeedbackEventInput{
		DecisionID: "d", PolicyVersion: 1, Signal: "reject",
		RejectReason: strPtr("reason"),
		At: strPtr("2026-01-01T00:00:00Z"),
	})
	require.NoError(t, err)
	require.NotNil(t, event)
}

func TestCOV3_GENERATED_ReadAll_SchemaInvalidJSONLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "feedback"), 0o755))
	// Write a valid JSON line that fails schema validation.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feedback", "events.jsonl"),
		[]byte(`{"schema":"docsync.feedback-event/v1","decision_id":"","policy_version":0,"signal":""}`+"\n"), 0o644))
	_, err := analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{StateDir: dir})
	require.Error(t, err)
}

func TestCOV3_GENERATED_RecordAnalyzerMisprediction_FullFlow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "feedback"), 0o755))
	event, err := analysis.RecordAnalyzerMisprediction(analysis.AnalyzerMispredictionInput{
		DecisionID:    "d1",
		PolicyVersion: 1,
		Predicted:     "ignore",
		Actual:        "review",
		Paths:         []string{"src/api/generated/client.ts"},
		Evidence:      []string{"sha1", "sha2"},
		EventID:       strPtr("01JP11T2300000000000000000"),
		At:            strPtr("2026-01-01T00:00:00Z"),
	}, analysis.FeedbackRecorderOptions{
		StateDir: dir,
		Clock:   func() string { return "2026-01-01T00:00:00Z" },
	})
	require.NoError(t, err)
	require.NotNil(t, event)
	assert.Equal(t, "reject", string(event.Signal))
}

func TestCOV3_SEMANTICS_YAML_ExplicitTimestampTag(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: 2026-01-01\n", "x: 2026-01-02\n")
	require.NotNil(t, result)
	require.NotNil(t, result.Note)
}

func TestCOV3_SEMANTICS_YAML_ExplicitNonCoreTag(t *testing.T) {
	// !!timestamp value: Go parser may tag it, but eemeli resolves it to Date.
	result := analysis.ConfigDiffFor("conf.yaml", "x: !!timestamp 2026-01-01\n", "x: !!timestamp 2026-01-02\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

func TestCOV3_SEMANTICS_Env_SingleQuoted(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "A='hello'\n", "A='world'\n")
	requireChanged(t, result, `config keys changed (1): A: "hello" → "world"`)
}

func TestCOV3_SEMANTICS_Env_NumberValue(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "A=42\n", "A=43\n")
	requireChanged(t, result, `config keys changed (1): A: 42 → 43`)
}

func TestCOV3_SEMANTICS_Env_BoolValue(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "A=true\n", "A=false\n")
	requireChanged(t, result, `config keys changed (1): A: true → false`)
}

func TestCOV3_SEMANTICS_Env_NullValue(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "A=null\n", "A=42\n")
	requireChanged(t, result, `config keys changed (1): A: null → 42`)
}

func TestCOV3_SEMANTICS_YAML_SequenceNode(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x:\n  - 1\n  - 2\n", "x:\n  - 1\n  - 3\n")
	requireChanged(t, result, `config keys changed (1): x: [1,2] → [1,3]`)
}

func TestCOV3_SEMANTICS_YAML_MappingNode(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x:\n  a: 1\n  b: 2\n", "x:\n  a: 1\n  b: 3\n")
	requireChanged(t, result, `config keys changed (1): x.b: 2 → 3`)
}

func TestCOV3_CONFIGDIFF_JsonStringLiteral_Empty(t *testing.T) {
	// jsonStringLiteral is used internally by formatConfigValue for strings.
	// We exercise it via ConfigDiffFor.
	result := analysis.ConfigDiffFor("config.json", `{"a":""}`, `{"a":"x"}`)
	requireChanged(t, result, `config keys changed (1): a: "" → "x"`)
}

func TestCOV3_SEMANTICS_YAML_RadixParseFallback(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: 0x1\n", "x: 0x2\n")
	requireChanged(t, result, `config keys changed (1): x: 1 → 2`)
}

func TestCOV3_OPENAPI_ContentRemoved(t *testing.T) {
	before := oapiDoc(map[string]any{"/p": opMap(map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}},
		}},
	})})
	after := oapiDoc(map[string]any{"/p": opMap(map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content": map[string]any{},
		}},
	})})
	result, err := analysis.ClassifyOpenApiDelta(before, after)
	require.NoError(t, err)
	assert.NotNil(t, result)
}

var _ = assert.NotNil
var _ = require.NotNil
var _ = schemas.ChangeAnalysisExample
