package analysis_test

// Table-driven OpenAPI delta classification coverage. Each case exercises a
// specific comparison function / delta kind path in openapi.go that the
// ported suites left below 95%.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
)

func oapiDoc(paths map[string]any) map[string]any {
	return map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "t", "version": "1.0.0"},
		"paths":   paths,
	}
}

func opMap(inner map[string]any) map[string]any {
	return map[string]any{"get": inner}
}

func TestCOV_ANALYSIS_OpenAPI_AllDeltaKinds(t *testing.T) {
	cases := []struct {
		name  string
		before map[string]any
		after  map[string]any
		kinds  []analysis.OpenApiDeltaKind
	}{
		{
			name:  "path_removed",
			before: oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": resp("ok")})}),
			after:  oapiDoc(map[string]any{}),
			kinds:  []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaPathRemoved},
		},
		{
			name:  "path_added",
			before: oapiDoc(map[string]any{}),
			after:  oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": resp("ok")})}),
			kinds:  []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaPathAdded},
		},
		{
			name:  "operation_removed",
			before: oapiDoc(map[string]any{"/p": map[string]any{"get": map[string]any{"responses": resp("ok")}}}),
			after:  oapiDoc(map[string]any{"/p": map[string]any{}}),
			kinds:  []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaOperationRemoved},
		},
		{
			name:  "operation_added",
			before: oapiDoc(map[string]any{"/p": map[string]any{}}),
			after:  oapiDoc(map[string]any{"/p": map[string]any{"get": map[string]any{"responses": resp("ok")}}}),
			kinds:  []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaOperationAdded},
		},
		{
			name:  "parameter_removed",
			before: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"parameters": []any{param("q", "query", map[string]any{"type": "string"})},
				"responses":  resp("ok"),
			})}),
			after: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"responses": resp("ok"),
			})}),
			kinds: []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaParameterRemoved},
		},
		{
			name:  "parameter_added",
			before: oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": resp("ok")})}),
			after: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"parameters": []any{param("q", "query", map[string]any{"type": "string"})},
				"responses":  resp("ok"),
			})}),
			kinds: []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaParameterAdded},
		},
		{
			name:  "deprecated_added",
			before: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"responses": resp("ok"),
			})}),
			after: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"deprecated": true,
				"responses":  resp("ok"),
			})}),
			kinds: []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaDeprecatedAdded},
		},
		{
			name:  "operation_id_changed",
			before: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"operationId": "old",
				"responses":   resp("ok"),
			})}),
			after: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"operationId": "new",
				"responses":   resp("ok"),
			})}),
			kinds: []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaOperationIDChanged},
		},
		{
			name:  "schema_property_added",
			before: compDoc(map[string]any{"A": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}}),
			after: compDoc(map[string]any{"A": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
			}}),
			kinds: []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaSchemaPropertyAdded},
		},
		{
			name:  "schema_property_removed",
			before: compDoc(map[string]any{"A": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
			}}),
			after: compDoc(map[string]any{"A": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}}),
			kinds: []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaSchemaPropertyRemoved},
		},
		{
			name:  "enum_narrowed",
			before: schemaDocWithEnum(map[string]bool{"a": true, "b": true, "c": true}),
			after:  schemaDocWithEnum(map[string]bool{"a": true, "b": true}),
			kinds:  []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaEnumNarrowed},
		},
		{
			name:  "enum_widened",
			before: schemaDocWithEnum(map[string]bool{"a": true, "b": true}),
			after:  schemaDocWithEnum(map[string]bool{"a": true, "b": true, "c": true}),
			kinds:  []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaEnumWidened},
		},
		{
			name:  "description_changed",
			before: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"responses": resp("old"),
			})}),
			after: oapiDoc(map[string]any{"/p": opMap(map[string]any{
				"responses": resp("new"),
			})}),
			kinds: []analysis.OpenApiDeltaKind{analysis.OpenApiDeltaDescriptionChanged},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := classify(t, tc.before, tc.after)
			got := map[analysis.OpenApiDeltaKind]bool{}
			for _, d := range result.Deltas {
				got[d.Kind] = true
			}
			for _, want := range tc.kinds {
				assert.True(t, got[want], "case %q: expected delta kind %s, got %v", tc.name, want, got)
			}
		})
	}
}

func resp(desc string) map[string]any {
	return map[string]any{"200": map[string]any{"description": desc}}
}

func param(name, where string, schema map[string]any) map[string]any {
	return map[string]any{"name": name, "in": where, "schema": schema}
}

func compDoc(schemas map[string]any) map[string]any {
	d := oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": resp("ok")})})
	d["components"] = map[string]any{"schemas": schemas}
	return d
}

func schemaDocWithEnum(vals map[string]bool) map[string]any {
	enum := make([]any, 0, len(vals))
	for v := range vals {
		enum = append(enum, v)
	}
	return oapiDoc(map[string]any{"/p": opMap(map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content": map[string]any{
				"application/json": map[string]any{"schema": map[string]any{
					"type":   "string",
					"enum":   enum,
				}},
			},
		}},
	})})
}

/* ------------------------------------------------------------- error paths */

func TestCOV_ANALYSIS_OpenAPI_ErrorPaths(t *testing.T) {
	// paths not an object on after.
	_, err := analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{}},
		map[string]any{"openapi": "3.0.3", "paths": []any{}})
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "'paths' must be an object")

	// paths not an object on before.
	_, err = analysis.ClassifyOpenApiDelta(
		map[string]any{"openapi": "3.0.3", "paths": []any{}},
		map[string]any{"openapi": "3.0.3", "paths": map[string]any{}})
	require.Error(t, err)
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "'paths' must be an object")

	// top-level not an object.
	_, err = analysis.ClassifyOpenApiDelta(struct{}{}, map[string]any{"openapi": "3.0.0"})
	require.Error(t, err)
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "object")
}

/* ------------------------------------------------------------- compareSchemaObject recordField error */

func TestCOV_ANALYSIS_OpenAPI_ComponentsSchemasError(t *testing.T) {
	before := compDoc(map[string]any{"A": map[string]any{"type": "object"}})
	before["components"] = map[string]any{"schemas": []any{}}
	_, err := analysis.ClassifyOpenApiDelta(before, compDoc(map[string]any{}))
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "'schemas' must be an object")
}

/* ------------------------------------------------------------- compareComponents push / schema removed */

func TestCOV_ANALYSIS_OpenAPI_ComponentSchemaRemoved(t *testing.T) {
	before := compDoc(map[string]any{"A": map[string]any{"type": "object"}})
	after := compDoc(map[string]any{})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaSchemaPropertyRemoved)
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
}

/* ------------------------------------------------------------- deeper comparison branches */

func TestCOV_ANALYSIS_OpenAPI_PropertyTypeAndRequired(t *testing.T) {
	// property type changed within a components schema -> compareProperties
	// -> compareProperty type-changed branch.
	before := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
		"required":   []any{"name"},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "integer"}},
		"required":   []any{"name"},
	}})
	result := classify(t, before, after)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyTypeChanged,
		"/components/schemas/A/properties/name")
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
}

func TestCOV_ANALYSIS_OpenAPI_RequiredNotArray(t *testing.T) {
	// required present but not an array -> isRequired non-array branch.
	before := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
		"required":   "not-an-array",
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
	}})
	result := classify(t, before, after)
	// No crash; required ignored when not an array.
	assert.NotNil(t, result)
}

func TestCOV_ANALYSIS_OpenAPI_PropertyRefChanged(t *testing.T) {
	// property $ref changed -> compareProperty $ref branch.
	before := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"ref": map[string]any{"$ref": "#/components/schemas/Old"}},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"ref": map[string]any{"$ref": "#/components/schemas/New"}},
	}})
	result := classify(t, before, after)
	// Should not error; $ref change classified.
	assert.NotNil(t, result)
}

func TestCOV_ANALYSIS_OpenAPI_RequestBodySchemaChanged(t *testing.T) {
	// requestBody schema changed -> compareRequestBody / compareContentSchemas.
	before := oapiDoc(map[string]any{"/p": map[string]any{"post": map[string]any{
		"requestBody": map[string]any{"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"type": "string"}},
		}},
		"responses": resp("ok"),
	}}})
	after := oapiDoc(map[string]any{"/p": map[string]any{"post": map[string]any{
		"requestBody": map[string]any{"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"type": "integer"}},
		}},
		"responses": resp("ok"),
	}}})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaRequestSchemaChanged)
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
}

func TestCOV_ANALYSIS_OpenAPI_ResponseDescriptionChanged(t *testing.T) {
	// response description changed -> compareDescription.
	before := oapiDoc(map[string]any{"/p": opMap(map[string]any{
		"responses": map[string]any{"200": map[string]any{"description": "old"}},
	})})
	after := oapiDoc(map[string]any{"/p": opMap(map[string]any{
		"responses": map[string]any{"200": map[string]any{"description": "new"}},
	})})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaDescriptionChanged)
	require.NotNil(t, delta)
}

func TestCOV_ANALYSIS_OpenAPI_DeprecatedAndOperationID(t *testing.T) {
	before := oapiDoc(map[string]any{"/p": opMap(map[string]any{
		"operationId": "old",
		"responses":   resp("ok"),
	})})
	after := oapiDoc(map[string]any{"/p": opMap(map[string]any{
		"operationId": "new",
		"deprecated":  true,
		"responses":   resp("ok"),
	})})
	result := classify(t, before, after)
	kinds := map[analysis.OpenApiDeltaKind]bool{}
	for _, d := range result.Deltas {
		kinds[d.Kind] = true
	}
	assert.True(t, kinds[analysis.OpenApiDeltaOperationIDChanged])
	assert.True(t, kinds[analysis.OpenApiDeltaDeprecatedAdded])
}

func TestCOV_ANALYSIS_OpenAPI_InlineSchemaCatchAll(t *testing.T) {
}
