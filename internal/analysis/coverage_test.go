package analysis_test

// Ported coverage-hardening suite for internal/analysis. Targets the
// statements/branches the ported suites leave below 95%: YAML/env semantic
// edges, OpenAPI classifier error/fallback paths, the generated-sdk recorder
// I/O edges, cap accounting for binary files. Black-box (package
// analysis_test), testify, deterministic (injected clocks/ULIDs, temp dirs,
// real git for the builder slices that need it).

import (
	"context"
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

/* ------------------------------------------------------------- semantics: YAML edges */

// parseYAMLDocument: multiple documents -> gap (the err == nil decode branch).
func TestCOV_ANALYSIS_Semantics_YAMLMultipleDocumentsGap(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "a: 1\n---\nb: 2\n", "a: 1\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

// yamlScalarValue: !!str explicit tag keeps the text.
func TestCOV_ANALYSIS_Semantics_YAMLStrTag(t *testing.T) {
	requireYAMLChanged(t, "x: !!str 123\ny: !!str 456\n", "x: !!str 123\ny: !!str 789\n",
		`config keys changed (1): y: "456" → "789"`)
}

// yamlMappingKey: float key -> "1.5".
func TestCOV_ANALYSIS_Semantics_YAMLMappingKeyFloat(t *testing.T) {
	requireYAMLChanged(t, "1.5: one\n", "1.5: two\n",
		`config keys changed (1): 1.5: "one" → "two"`)
}

// yamlTagTestPasses: explicit non-core tag (!!omap) fails closed.
func TestCOV_ANALYSIS_Semantics_YAMLUnknownTagGap(t *testing.T) {
	result := analysis.ConfigDiffFor("conf.yaml", "x: !!omap {a: 1}\n", "x: !!omap {a: 2}\n")
	requireGap(t, result, "unparseable — semantic config diff unavailable")
}

// resolveYAMLScalarText: .inf / .nan non-finite -> null canonicalization.
func TestCOV_ANALYSIS_Semantics_YAMLNonFinite(t *testing.T) {
	requireYAMLChanged(t, "x: .inf\n", "x: 5\n",
		`config keys changed (1): x: Infinity → 5`)
}

// yamlParseRadix: out-of-int64-range radix fallback to float64.
func TestCOV_ANALYSIS_Semantics_YAMLRadixFallback(t *testing.T) {
	requireYAMLChanged(t, "x: 0o7777777777777777777777\n", "x: 0o10\n",
		`config keys changed (1): x: "0o7777777777777777777777" → 8`)
}

// parseEnv: env parse failure on the new side -> declared gap.
func TestCOV_ANALYSIS_Semantics_EnvParseFailureNewSide(t *testing.T) {
	requireGap(t, analysis.ConfigDiffFor(".env", "A=1\n", "A=1\nNO_EQUALS"),
		"unparseable — semantic config diff unavailable")
}

/* ------------------------------------------------------------- openapi: describeValue object case */

func TestCOV_ANALYSIS_OpenAPI_DescribeValueObject(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(struct{}{}, map[string]any{"openapi": "3.0.0"})
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "object")
}

/* ------------------------------------------------------------- openapi: truncate long description */

func TestCOV_ANALYSIS_OpenAPI_DescriptionTruncate(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": covOpWith("description", strings.Repeat("x", 200))}})
	after := covDocWith(map[string]any{"/p": map[string]any{"get": covOpWith("description", strings.Repeat("y", 200))}})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaDescriptionChanged)
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "…")
}

func covDocWith(paths map[string]any) map[string]any {
	return map[string]any{"openapi": "3.0.3", "info": map[string]any{"title": "t", "version": "1.0.0"}, "paths": paths}
}

func covOpWith(k, v string) map[string]any {
	return map[string]any{k: v, "responses": map[string]any{"200": map[string]any{"description": "ok"}}}
}

/* ------------------------------------------------------------- openapi: recordField errors */

func TestCOV_ANALYSIS_OpenAPI_RecordFieldNonObject(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": []any{}},
		map[string]any{"openapi": "3.0.3"})
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "'paths' must be an object")
}

func TestCOV_ANALYSIS_OpenAPI_ComponentsSchemasNonObject(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	before["components"] = map[string]any{"schemas": []any{}}
	_, err := analysis.ClassifyOpenApiDelta(before, covDocWith(map[string]any{}))
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "'schemas' must be an object")
}

/* ------------------------------------------------------------- openapi: description absent branches */

func TestCOV_ANALYSIS_OpenAPI_DescriptionAbsent(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{"description": "ok"}}}}})
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{}}}}})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaDescriptionChanged)
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "(absent)")
}

/* ------------------------------------------------------------- openapi: compareProperty type changed */

func TestCOV_ANALYSIS_OpenAPI_ComparePropertyTypeChanged(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	before["components"] = map[string]any{"schemas": map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
	}}}
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	after["components"] = map[string]any{"schemas": map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "integer"}},
	}}}
	result := classify(t, before, after)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyTypeChanged,
		"/components/schemas/A/properties/name")
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
}

func covSchemaDoc(schema map[string]any) map[string]any {
	return covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content": map[string]any{
				"application/json": map[string]any{"schema": schema},
			},
		}},
	}}})
}

/* ------------------------------------------------------------- openapi: compareRequiredList */

func TestCOV_ANALYSIS_OpenAPI_CompareRequiredList(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"parameters": []any{map[string]any{"name": "q", "in": "query",
			"schema": map[string]any{"type": "string"}}}}}})
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"parameters": []any{map[string]any{"name": "q", "in": "query", "required": true,
			"schema": map[string]any{"type": "string"}}}}}})
	result := classify(t, before, after)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterRequiredAdded,
		"/paths/~1p/get/parameters/q")
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
}

/* ------------------------------------------------------------- openapi: compareInlineSchema catch-all */

func TestCOV_ANALYSIS_OpenAPI_CompareInlineSchemaCatchAll(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content": map[string]any{
				"application/json": map[string]any{"schema": map[string]any{"type": "object"}},
			},
		}},
	}}})
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content": map[string]any{
				"application/json": map[string]any{"schema": map[string]any{"type": "integer"}},
			},
		}},
	}}})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged)
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
}

/* ------------------------------------------------------------- openapi: parameterIndex errors */

func TestCOV_ANALYSIS_OpenAPI_ParameterIndexErrors(t *testing.T) {
	beforePI := map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{
		"get": map[string]any{"parameters": map[string]any{}}}}}
	afterPI := map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{
		"get": map[string]any{"parameters": []any{}}}}}
	_, err := analysis.ClassifyOpenApiDelta(beforePI, afterPI)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'parameters' must be an array")

	_, err = analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{
			"get": map[string]any{"parameters": []any{"not-an-object"}}}}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{
			"get": map[string]any{"parameters": []any{}}}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "every parameter must be an object")
}

/* ------------------------------------------------------------- openapi: response not object / responses removed / requestBody required */

func TestCOV_ANALYSIS_OpenAPI_ResponseNotObject(t *testing.T) {
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{"description": "ok"}}}}})
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": "not-an-object"}}}})
	_, err := analysis.ClassifyOpenApiDelta(before, after)
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "response objects must be objects on both sides")
}

func TestCOV_ANALYSIS_OpenAPI_ResponsesRemovedAndRequestBodyRequired(t *testing.T) {
	before := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{
		"responses": map[string]any{"200": map[string]any{"description": "ok"}}}}})
	after := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged)
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)

	beforeB := covDocWith(map[string]any{"/p": map[string]any{"post": map[string]any{
		"requestBody": map[string]any{"content": map[string]any{}}}}})
	afterB := covDocWith(map[string]any{"/p": map[string]any{"post": map[string]any{
		"requestBody": map[string]any{"required": true, "content": map[string]any{}}}}})
	resultB := classify(t, beforeB, afterB)
	deltaB := findDelta(resultB.Deltas, analysis.OpenApiDeltaParameterRequiredAdded)
	require.NotNil(t, deltaB)
	assert.Equal(t, analysis.OpenApiTierBreaking, deltaB.Tier)
}

/* ------------------------------------------------------------- openapi: operation / path item / components errors */

func TestCOV_ANALYSIS_OpenAPI_OperationPathItemComponentsErrors(t *testing.T) {
	opBad := map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{
		"get": "not-an-object"}}}
	_, err := analysis.ClassifyOpenApiDelta(opBad, opBad)
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "operation objects must be objects on both sides")

	_, err = analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": "not-an-object"}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/p": map[string]any{
			"get": map[string]any{}}}})
	require.Error(t, err)
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "path item must be an object on both sides")

	beforeC := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	beforeC["components"] = map[string]any{"schemas": map[string]any{"A": map[string]any{"type": "object"}}}
	afterC := covDocWith(map[string]any{"/p": map[string]any{"get": map[string]any{}}})
	resultC := classify(t, beforeC, afterC)
	deltaC := findDelta(resultC.Deltas, analysis.OpenApiDeltaSchemaPropertyRemoved)
	require.NotNil(t, deltaC)
	assert.Equal(t, analysis.OpenApiTierBreaking, deltaC.Tier)
}

/* ------------------------------------------------------------- openapi: ClassifyOpenApiDelta paths recordField error */

func TestCOV_ANALYSIS_OpenAPI_PathsRecordFieldError(t *testing.T) {
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{}},
		map[string]any{"openapi": "3.0.3", "paths": []any{}})
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "'paths' must be an object")
}

/* ------------------------------------------------------------- generated: PathStem edges */

func TestCOV_ANALYSIS_Generated_PathStem(t *testing.T) {
	assert.Equal(t, "client", analysis.PathStem("src\\api\\generated\\client.ts"))
	assert.Equal(t, "Makefile", analysis.PathStem("src/Makefile"))
	assert.Equal(t, ".env", analysis.PathStem("config/.env"))
	assert.Equal(t, "client", analysis.PathStem("src/api/generated/client.ts"))
}

/* ------------------------------------------------------------- generated: AnalyzerMispredictionReason */

func TestCOV_ANALYSIS_Generated_MispredictionReasonPatternAndEvidence(t *testing.T) {
	reason := analysis.AnalyzerMispredictionReason(analysis.AnalyzerMispredictionInput{
		Predicted: "p", Actual: "a", Paths: []string{"x"}, Pattern: strPtr("custom-pattern")})
	assert.Contains(t, reason, "custom-pattern")
	reason2 := analysis.AnalyzerMispredictionReason(analysis.AnalyzerMispredictionInput{
		Predicted: "p", Actual: "a", Paths: []string{"x"}, Evidence: []string{"e1", "e2"}})
	assert.Contains(t, reason2, "evidence=e1|e2")
}

/* ------------------------------------------------------------- generated: recorder edges */

func TestCOV_ANALYSIS_Generated_ListFeedbackEventsReadError(t *testing.T) {
	blocker := t.TempDir()
	file := filepath.Join(blocker, "feedback")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	_, err := analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{StateDir: blocker})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
}

func TestCOV_ANALYSIS_Generated_NewJSONLSinkViaRecorderRelativeStateDir(t *testing.T) {
	facts := analysis.AnalyzerMispredictionInput{
		DecisionID: "d", PolicyVersion: 1,
		Predicted: "p", Actual: "a", Paths: []string{"x"},
	}
	_, err := analysis.RecordAnalyzerMisprediction(facts, analysis.FeedbackRecorderOptions{
		StateDir: "relative/state", Clock: func() string { return "2026-01-01T00:00:00Z" },
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "stateDir must be an absolute path")
}

func TestCOV_ANALYSIS_Generated_AppendRetryBlocker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	blocker := filepath.Join(dir, "feedback")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	_, err := analysis.RecordAnalyzerMisprediction(covFacts(t), analysis.FeedbackRecorderOptions{
		StateDir: dir, Clock: func() string { return "2026-01-01T00:00:00Z" },
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
}

func TestCOV_ANALYSIS_Generated_ReadAllCorruptJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".docsync", "state")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "feedback"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feedback", "events.jsonl"),
		[]byte("{not valid json\n"), 0o644))
	_, err := analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{
		StateDir: dir, Clock: func() string { return "2026-01-01T00:00:00Z" },
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
}

func TestCOV_ANALYSIS_Generated_TruncateForMessageLongStateDir(t *testing.T) {
	longDir := strings.Repeat("a", 100)
	_, err := analysis.RecordAnalyzerMisprediction(covFacts(t), analysis.FeedbackRecorderOptions{
		StateDir: longDir, Clock: func() string { return "2026-01-01T00:00:00Z" },
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), longDir[:64])
}

func covFacts(t *testing.T) analysis.AnalyzerMispredictionInput {
	t.Helper()
	return analysis.AnalyzerMispredictionInput{
		DecisionID: "d", PolicyVersion: 1,
		Predicted: "p", Actual: "a", Paths: []string{"x"},
		EventID: strPtr("01JP11T2300000000000000000"),
	}
}

/* ------------------------------------------------------------- build: assessCaps binary patch bytes */

func TestCOV_ANALYSIS_Build_AssessCapsBinaryPatch(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "img.bin", "\x00\x01\x02")
	base := repo.commitAll(t, "base")
	repo.write(t, "img.bin", "\x00\x01\x02\x03")
	head := repo.commitAll(t, "head")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/binary", Git: repo.gitClient(t), Base: base, Head: head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
		Caps:    &analysis.AnalysisCaps{MaxFiles: 100, MaxDiffSize: 0},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	assert.Contains(t, *record.DeepAnalysis.Reason, "cap breach")
}

/* ------------------------------------------------------------- detect: binary markers */

func TestCOV_ANALYSIS_Detect_FormattingBinaryMarker(t *testing.T) {
	assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
		Patch: "diff --git a/img.bin b/img.bin\nBinary files a/img.bin and b/img.bin differ\n",
	}))
	assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
		Patch: "diff --git a/img.bin b/img.bin\nGIT binary patch\n",
	}))
}

func strPtr(s string) *string { return &s }

var _ = schemas.AnalyzerNameTypeScript
var _ = context.Background
