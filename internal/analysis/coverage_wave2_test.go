package analysis_test

// Wave-2 coverage hardening for internal/analysis. Targets every remaining
// function/statement below 95%: semantics edges (YAML node values, env parsing,
// diff maps, formatConfigValue, canonicalOrNull), config-diff edges (isJSONSafe,
// configNumber, resolvedValueEquals), OpenAPI delta comparison branches
// (compareSchemaObject, compareProperty, compareParameters, compareContentSchemas,
// compareRequestBody, compareResponses, compareOperation, compareComponents,
// tierFor, push, descriptionChanged, compareDescription, isRequired),
// generated.go recorder edges (appendLine, marshalJSONLine, containsString,
// ListFeedbackEvents, newJSONLSink, RecordFeedbackEvent ULID generation),
// detect.go mustCompileGlobs, and build.go assessCaps edges.
// Black-box (package analysis_test), testify, deterministic.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------- semantics: config-diff edges */

func TestCOV2_SEMANTICS_ConfigDiff_TOML(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.toml", "[build]\na=1", "[build]\na=2")
	require.NotNil(t, result)
	assert.Nil(t, result.ConfigDiff)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, "toml")
}

func TestCOV2_SEMANTICS_ConfigDiff_INI(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.ini", "[section]\nkey=1", "[section]\nkey=2")
	require.NotNil(t, result)
	assert.Nil(t, result.ConfigDiff)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, "toml")
}

func TestCOV2_SEMANTICS_ParseJSON_NonObject(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `"string"`, `"other"`)
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

func TestCOV2_SEMANTICS_ParseJSON_Invalid(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", "{bad json}", `{"a":1}`)
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

func TestCOV2_SEMANTICS_JSONDiff_Changed(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":1,"b":2}`, `{"a":1,"b":3}`)
	requireChanged(t, result, `config keys changed (1): b: 2 → 3`)
}

func TestCOV2_SEMANTICS_JSONDiff_Added(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":1}`, `{"a":1,"b":2}`)
	requireChanged(t, result, `config keys changed (1): b: absent → 2`)
}

func TestCOV2_SEMANTICS_JSONDiff_Removed(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":1,"b":2}`, `{"a":1}`)
	requireChanged(t, result, `config keys changed (1): b: 2 → absent`)
}

func TestCOV2_SEMANTICS_JSONDiff_Nested(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":{"b":{"c":1}}}`, `{"a":{"b":{"c":2}}}`)
	requireChanged(t, result, `config keys changed (1): a.b.c: 1 → 2`)
}

func TestCOV2_SEMANTICS_JSONDiff_Array(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":[1,2]}`, `{"a":[1,2,3]}`)
	requireChanged(t, result, `config keys changed (1): a: [1,2] → [1,2,3]`)
}

func TestCOV2_SEMANTICS_JSONDiff_Bool(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":true}`, `{"a":false}`)
	requireChanged(t, result, `config keys changed (1): a: true → false`)
}

func TestCOV2_SEMANTICS_JSONDiff_Null(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json", `{"a":null}`, `{"a":1}`)
	requireChanged(t, result, `config keys changed (1): a: null → 1`)
}

func TestCOV2_SEMANTICS_YAML_DeepNested(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "a:\n  b:\n    c: 1\n", "a:\n  b:\n    c: 2\n")
	requireChanged(t, result, `config keys changed (1): a.b: {"c":1} → {"c":2}`)
}

func TestCOV2_SEMANTICS_YAML_DupKey(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "a: 1\na: 2\n", "a: 3\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

func TestCOV2_SEMANTICS_YAML_NonScalarKey(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "? {a: b}\n: 1\n", "? {a: b}\n: 2\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

func TestCOV2_SEMANTICS_YAML_AliasMiss(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "a: *missing\n", "a: 1\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

func TestCOV2_SEMANTICS_YAML_ExplicitStr(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: !!str 42\n", "x: !!str 99\n")
	requireChanged(t, result, `config keys changed (1): x: "42" → "99"`)
}

func TestCOV2_SEMANTICS_Env_Export(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "export A=1\n", "export A=2\n")
	requireChanged(t, result, `config keys changed (1): A: 1 → 2`)
}

func TestCOV2_SEMANTICS_Env_Quoted(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "A=\"hello\"\n", "A=\"world\"\n")
	requireChanged(t, result, `config keys changed (1): A: "hello" → "world"`)
}

func TestCOV2_SEMANTICS_Env_BothFail(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "BAD\n", "ALSO_BAD\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

func TestCOV2_SEMANTICS_FormatAbsent(t *testing.T) {
	result := analysis.ConfigDiffFor(".env", "A=1\n", "")
	requireChanged(t, result, `config keys changed (1): A: 1 → absent`)
}

func TestCOV2_SEMANTICS_DiffTruncate(t *testing.T) {
	var b1, b2 strings.Builder
	b1.WriteString("{")
	b2.WriteString("{")
	for i := 0; i < 22; i++ {
		if i > 0 {
			b1.WriteString(",")
			b2.WriteString(",")
		}
		b1.WriteString(fmt.Sprintf(`"k%d":%d`, i, i))
		b2.WriteString(fmt.Sprintf(`"k%d":%d`, i, i+1))
	}
	b1.WriteString("}")
	b2.WriteString("}")
	result := analysis.ConfigDiffFor("config.json", b1.String(), b2.String())
	require.NotNil(t, result)
	require.NotNil(t, result.ConfigDiff)
	assert.True(t, *result.ConfigDiff)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, "…")
}

/* ------------------------------------------------------------- config-diff: DiffConfigKeys */

func TestCOV2_CONFIGDIFF_Unsafe(t *testing.T) {
	_, err := analysis.DiffConfigKeys(
		map[string]any{"a": make(chan int)},
		map[string]any{"a": 1})
	require.Error(t, err)
}

func TestCOV2_CONFIGDIFF_ArrayRoot(t *testing.T) {
	diffs, err := analysis.DiffConfigKeys([]any{1, 2}, []any{1, 3})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	assert.Equal(t, "", diffs[0].Key)
}

func TestCOV2_CONFIGDIFF_ScalarRoot(t *testing.T) {
	_, err := analysis.DiffConfigKeys("string", map[string]any{"a": 1})
	require.Error(t, err)
}

func TestCOV2_CONFIGDIFF_NilRoot(t *testing.T) {
	_, err := analysis.DiffConfigKeys(nil, map[string]any{"a": 1})
	require.Error(t, err)
}

func TestCOV2_CONFIGDIFF_Num_F32(t *testing.T) {
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"a": float32(1.5)}, map[string]any{"a": float32(2.5)})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
}

func TestCOV2_CONFIGDIFF_Num_Uint(t *testing.T) {
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"a": uint(1)}, map[string]any{"a": uint(2)})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
}

func TestCOV2_CONFIGDIFF_Num_I8(t *testing.T) {
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"a": int8(1)}, map[string]any{"a": int8(2)})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
}

func TestCOV2_CONFIGDIFF_Secret(t *testing.T) {
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"password": "x", "normal": "v"},
		map[string]any{"password": "y", "normal": "v"})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	assert.True(t, diffs[0].SecretMasked)
}

func TestCOV2_CONFIGDIFF_Summary(t *testing.T) {
	// before: {a:1, b:2, password:x} after: {a:1, c:3}
	// diffs: b removed, password removed (2 removed), c added (1 added)
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"a": 1, "b": 2, "password": "x"},
		map[string]any{"a": 1, "c": 3})
	require.NoError(t, err)
	summary := analysis.SummarizeConfigDiff(diffs)
	assert.Equal(t, 1, summary.Added)
	assert.Equal(t, 2, summary.Removed)
	assert.Equal(t, 0, summary.Changed)
	assert.Equal(t, 1, summary.SecretMaskedCount)
}

func TestCOV2_CONFIGDIFF_EnvRef(t *testing.T) {
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"a": "$VAR", "b": "plain"},
		map[string]any{"a": "new", "b": "plain"})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	assert.True(t, diffs[0].EnvRef)
}

/* ------------------------------------------------------------- OpenAPI: error paths */

func TestCOV2_OPENAPI_DescNil(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(nil, map[string]any{"openapi": "3.0.0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null")
}

func TestCOV2_OPENAPI_DescArray(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta([]any{}, map[string]any{"openapi": "3.0.0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "array")
}

func TestCOV2_OPENAPI_MissingVer(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"paths": map[string]any{}},
		map[string]any{"openapi": "3.0.0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required")
}

func TestCOV2_OPENAPI_OpNotPlain(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": "string"}}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{}}}})
	require.Error(t, err)
}

func TestCOV2_OPENAPI_PathNotObj(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": "string"}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{}}})
	require.Error(t, err)
}

func TestCOV2_OPENAPI_RespNotObj(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"responses": map[string]any{"200": "string"},
		}}}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"responses": map[string]any{"200": map[string]any{"description": "ok"}},
		}}}})
	require.Error(t, err)
}

func TestCOV2_OPENAPI_ParamNoName(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"parameters": []any{map[string]any{"in": "query"}},
			"responses":  resp("ok"),
		}}}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"responses": resp("ok"),
		}}}})
	require.Error(t, err)
}

func TestCOV2_OPENAPI_ParamNotObj(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"parameters": []any{"not-obj"},
			"responses":  resp("ok"),
		}}}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"responses": resp("ok"),
		}}}})
	require.Error(t, err)
}

func TestCOV2_OPENAPI_ParamsNotArr(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"parameters": "not-arr",
			"responses":  resp("ok"),
		}}}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{"get": map[string]any{
			"responses": resp("ok"),
		}}}})
	require.Error(t, err)
}

func TestCOV2_OPENAPI_ReqBodyAdded(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"post": map[string]any{"responses": resp("ok")}}})
	after := covDocWith(map[string]any{"/p": map[string]any{"post": map[string]any{
		"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}},
		"responses":   resp("ok"),
	}}})
	result, err := analysis.ClassifyOpenApiDelta(before, after)
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestCOV2_OPENAPI_RespRemoved(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{"description": "ok"}},
	}}})
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	result, err := analysis.ClassifyOpenApiDelta(before, after)
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestCOV2_OPENAPI_CompNotObj(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	before["components"] = map[string]any{"schemas": []any{}}
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	_, err := analysis.ClassifyOpenApiDelta(before, after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'schemas' must be an object")
}

func TestCOV2_OPENAPI_CompRemoved(t *testing.T) {
	before := compDoc(map[string]any{"A": map[string]any{"type": "object"}})
	after := compDoc(map[string]any{})
	result, err := analysis.ClassifyOpenApiDelta(before, after)
	require.NoError(t, err)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaSchemaPropertyRemoved)
	require.NotNil(t, delta)
}

func TestCOV2_OPENAPI_ReqNotArr(t *testing.T) {
	before := compDoc(map[string]any{"A": map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "string"}}, "required": "not-arr"}})
	after := compDoc(map[string]any{"A": map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "string"}}}})
	result := classify(t, before, after)
	assert.NotNil(t, result)
}

/* ------------------------------------------------------------- generated */

func TestCOV2_GENERATED_Contains(t *testing.T) {
	input := analysis.GeneratedRoutingInput{
		RunID: "r1", BaseSha: "b1", HeadSha: "h1",
		Files: []analysis.GeneratedRoutingFile{
			{DetectorFile: analysis.DetectorFile{Path: "src/api/generated/client.ts"}},
		},
		Docs: []analysis.GeneratedRoutingDoc{
			{Path: "docs/api.md", Content: "The client module handles..."},
			{Path: "docs/other.md", Content: "No mention"},
		},
	}
	record := analysis.InspectGeneratedOutput(input)
	require.NotNil(t, record)
	assert.Equal(t, []string{"docs/api.md"}, record.AffectedDocs)
}

func TestCOV2_GENERATED_ListMiss(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "feedback"), 0o755))
	events, err := analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{StateDir: dir})
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestCOV2_GENERATED_RecordULID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	opts := analysis.FeedbackRecorderOptions{StateDir: dir, Clock: func() string { return "2026-01-01T00:00:00Z" }}
	_, err := analysis.RecordFeedbackEvent(opts, analysis.FeedbackEventInput{
		DecisionID: "d", PolicyVersion: 1, Signal: "reject",
		RejectReason: strPtr("r"), At: strPtr("2026-01-01T00:00:00Z"),
	})
	require.NoError(t, err)
}

func TestCOV2_GENERATED_RecordNoClock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	opts := analysis.FeedbackRecorderOptions{StateDir: dir}
	_, err := analysis.RecordFeedbackEvent(opts, analysis.FeedbackEventInput{
		DecisionID: "d", PolicyVersion: 1, Signal: "reject",
		RejectReason: strPtr("r"), EventID: strPtr("01JP11T2300000000000000000"),
		At: strPtr("2026-01-01T00:00:00Z"),
	})
	require.NoError(t, err)
}

func TestCOV2_GENERATED_ReadSchemaBad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "feedback"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feedback", "events.jsonl"),
		[]byte(`{"schema":"docsync.feedback-event/v1","decision_id":"d","policy_version":1,"signal":"invalid"}`+"\n"), 0o644))
	_, err := analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{
		StateDir: dir, Clock: func() string { return "2026-01-01T00:00:00Z" },
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
}

func TestCOV2_GENERATED_MispEmpty(t *testing.T) {
	_, err := analysis.RecordAnalyzerMisprediction(analysis.AnalyzerMispredictionInput{
		DecisionID: "d", PolicyVersion: 1,
	}, analysis.FeedbackRecorderOptions{})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
}

func TestCOV2_GENERATED_InspectNoGen(t *testing.T) {
	record := analysis.InspectGeneratedOutput(analysis.GeneratedRoutingInput{
		RunID: "r1", BaseSha: "b1", HeadSha: "h1",
		Files: []analysis.GeneratedRoutingFile{
			{DetectorFile: analysis.DetectorFile{Path: "src/app.ts"}},
		},
	})
	assert.Nil(t, record)
}

func TestCOV2_GENERATED_AppendRetry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	fb := filepath.Join(dir, "feedback")
	require.NoError(t, os.WriteFile(fb, []byte("x"), 0o444))
	_, err := analysis.RecordAnalyzerMisprediction(analysis.AnalyzerMispredictionInput{
		DecisionID: "d", PolicyVersion: 1,
		Predicted: "p", Actual: "a", Paths: []string{"x"},
		EventID: strPtr("01JP11T2300000000000000000"),
	}, analysis.FeedbackRecorderOptions{
		StateDir: dir, Clock: func() string { return "2026-01-01T00:00:00Z" },
	})
	require.Error(t, err)
}

/* ------------------------------------------------------------- build */

func TestCOV2_BUILD_AllUnavail(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "a.ts", "x\n")
	base := repo.commitAll(t, "b1")
	repo.write(t, "a.ts", "y\n")
	head := repo.commitAll(t, "b2")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/noan", Git: repo.gitClient(t), Base: base, Head: head,
		Plugins: []analysis.AnalyzerPlugin{
			failingAnalyzer(schemas.AnalyzerNameTypeScript, errors.New("down")),
			failingAnalyzer(schemas.AnalyzerNameGeneric, errors.New("down")),
		},
	})
	require.NoError(t, err)
	require.Equal(t, analysis.AnalysisOutcomeKindInconclusive, outcome.Kind)
	assert.Contains(t, outcome.Reason, "all analyzers unavailable")
}

func TestCOV2_BUILD_GitFail(t *testing.T) {
	repo := newBuilderRepo(t)
	client := repo.gitClient(t)
	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/bad", Git: client, Base: "NONEXISTENT", Head: "HEAD",
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
	})
	require.NoError(t, err)
	require.Equal(t, analysis.AnalysisOutcomeKindInconclusive, outcome.Kind)
	assert.Contains(t, outcome.Reason, "git failure")
}
