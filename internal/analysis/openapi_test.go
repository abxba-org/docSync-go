package analysis_test

// Ported from packages/core/test/openapi-classifier.test.ts (traceability,
// plan.md §7): OpenAPI delta classifier tests (P11-T07; OD-R28 three-tier;
// V-P11-4) — the named contractual assertions over three fixtures, the
// supporting rule decisions (path removal, enum narrowing/widening,
// operationId, description tier, worstTier rollup, fail-closed typed errors),
// rule-table integrity, and determinism.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
)

const jsonCT = "application/json"

// doc mirrors the TS `doc(paths, schemas?)` fixture builder.
func doc(paths map[string]any, schemas ...map[string]any) map[string]any {
	result := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "test-api", "version": "1.0.0"},
		"paths":   paths,
	}
	if len(schemas) > 0 {
		result["components"] = map[string]any{"schemas": schemas[0]}
	}
	return result
}

// getOperation mirrors the TS `getOperation(extra?)` fixture builder.
func getOperation(extra ...map[string]any) map[string]any {
	operation := map[string]any{
		"responses": map[string]any{
			"200": map[string]any{
				"description": "ok",
				"content": map[string]any{
					jsonCT: map[string]any{
						"schema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":     map[string]any{"type": "string"},
								"amount": map[string]any{"type": "number"},
							},
						},
					},
				},
			},
		},
	}
	for _, entry := range extra {
		for key, value := range entry {
			operation[key] = value
		}
	}
	return operation
}

func classify(t *testing.T, before, after any) analysis.OpenApiDeltaClassification {
	t.Helper()
	result, err := analysis.ClassifyOpenApiDelta(before, after)
	require.NoError(t, err)
	return result
}

// findDelta mirrors the TS `deltas.find((d) => d.kind === …)` assertion
// (kind-only lookup); location assertions are made on the returned entry.
func findDelta(deltas []analysis.OpenApiDeltaEntry, kind analysis.OpenApiDeltaKind) *analysis.OpenApiDeltaEntry {
	for i := range deltas {
		if deltas[i].Kind == kind {
			return &deltas[i]
		}
	}
	return nil
}

// findDeltaAt mirrors the TS `deltas.find((d) => d.kind === ... &&
// d.location === ...)` assertions that pin an exact JSON-pointer location.
func findDeltaAt(deltas []analysis.OpenApiDeltaEntry, kind analysis.OpenApiDeltaKind, location string) *analysis.OpenApiDeltaEntry {
	for i := range deltas {
		if deltas[i].Kind == kind && deltas[i].Location == location {
			return &deltas[i]
		}
	}
	return nil
}

func TestClassifyOpenApiDelta_P11T07_OD_R28_V_P11_4(t *testing.T) {
	t.Run("V-P11-4: a breaking delta is classified breaking; a deprecation annotation is behavior-annotating; an optional property addition is non-breaking", func(t *testing.T) {
		// 1. response schema property removed → breaking
		beforeResponse := doc(map[string]any{"/payments/{id}": map[string]any{"get": getOperation()}})
		afterResponse := doc(map[string]any{
			"/payments/{id}": map[string]any{
				"get": map[string]any{
					"responses": map[string]any{
						"200": map[string]any{
							"description": "ok",
							"content": map[string]any{
								jsonCT: map[string]any{
									"schema": map[string]any{
										"type":       "object",
										"properties": map[string]any{"id": map[string]any{"type": "string"}},
									},
								},
							},
						},
					},
				},
			},
		})
		removal := classify(t, beforeResponse, afterResponse)
		removedProperty := findDeltaAt(removal.Deltas, analysis.OpenApiDeltaSchemaPropertyRemoved,
			"/paths/~1payments~1{id}/get/responses/200/content/application~1json/schema/properties/amount")
		require.NotNil(t, removedProperty, "amount property removal must be detected")
		assert.Equal(t, analysis.OpenApiTierBreaking, removedProperty.Tier)
		assert.GreaterOrEqual(t, removal.Summary.Breaking, 1)
		assert.Equal(t, analysis.OpenApiTierBreaking, removal.Summary.WorstTier)

		// 2. deprecation annotation → behavior-annotating
		plain := getOperation()
		deprecated := getOperation(map[string]any{"deprecated": true})
		annotation := classify(t,
			doc(map[string]any{"/payments/{id}": map[string]any{"get": plain}}),
			doc(map[string]any{"/payments/{id}": map[string]any{"get": deprecated}}))
		deprecatedDelta := findDelta(annotation.Deltas, analysis.OpenApiDeltaDeprecatedAdded)
		require.NotNil(t, deprecatedDelta, "deprecation must be detected")
		assert.Equal(t, analysis.OpenApiTierBehaviorAnnotating, deprecatedDelta.Tier)
		assert.Equal(t, 0, annotation.Summary.Breaking)
		assert.Equal(t, 1, annotation.Summary.BehaviorAnnotating)
		assert.Equal(t, analysis.OpenApiTierBehaviorAnnotating, annotation.Summary.WorstTier)

		// 3. optional property addition → non-breaking
		addition := classify(t,
			doc(map[string]any{}, map[string]any{
				"Payment": map[string]any{
					"type":       "object",
					"properties": map[string]any{"amount": map[string]any{"type": "number"}},
				},
			}),
			doc(map[string]any{}, map[string]any{
				"Payment": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"amount":   map[string]any{"type": "number"},
						"currency": map[string]any{"type": "string"},
					},
				},
			}))
		addedProperty := findDeltaAt(addition.Deltas, analysis.OpenApiDeltaSchemaPropertyAdded,
			"/components/schemas/Payment/properties/currency")
		require.NotNil(t, addedProperty, "currency property addition must be detected")
		assert.Equal(t, analysis.OpenApiTierNonBreaking, addedProperty.Tier)
		assert.Equal(t, 0, addition.Summary.Breaking)
		assert.GreaterOrEqual(t, addition.Summary.NonBreaking, 1)
		assert.Equal(t, analysis.OpenApiTierNonBreaking, addition.Summary.WorstTier)
	})

	t.Run("a removed path is breaking", func(t *testing.T) {
		result := classify(t,
			doc(map[string]any{"/v1/legacy": map[string]any{"get": getOperation()}}),
			doc(map[string]any{}))
		delta := findDelta(result.Deltas, analysis.OpenApiDeltaPathRemoved)
		require.NotNil(t, delta)
		assert.Equal(t, "/paths/~1v1~1legacy", delta.Location)
		assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
		assert.Equal(t, analysis.OpenApiTierBreaking, result.Summary.WorstTier)
	})

	t.Run("enum narrowing is breaking; enum widening is non-breaking", func(t *testing.T) {
		before := doc(map[string]any{}, map[string]any{
			"Status": map[string]any{"type": "string", "enum": []any{"active", "inactive"}},
		})
		narrowed := classify(t, before, doc(map[string]any{}, map[string]any{
			"Status": map[string]any{"type": "string", "enum": []any{"active"}},
		}))
		narrowedDelta := findDelta(narrowed.Deltas, analysis.OpenApiDeltaEnumNarrowed)
		require.NotNil(t, narrowedDelta)
		assert.Equal(t, analysis.OpenApiTierBreaking, narrowedDelta.Tier)
		assert.Equal(t, analysis.OpenApiTierBreaking, narrowed.Summary.WorstTier)

		widened := classify(t, before, doc(map[string]any{}, map[string]any{
			"Status": map[string]any{"type": "string", "enum": []any{"active", "inactive", "pending"}},
		}))
		widenedDelta := findDelta(widened.Deltas, analysis.OpenApiDeltaEnumWidened)
		require.NotNil(t, widenedDelta)
		assert.Equal(t, analysis.OpenApiTierNonBreaking, widenedDelta.Tier)
		assert.Equal(t, analysis.OpenApiTierNonBreaking, widened.Summary.WorstTier)
	})

	t.Run("an operationId change is behavior-annotating", func(t *testing.T) {
		result := classify(t,
			doc(map[string]any{"/payments/{id}": map[string]any{
				"get": getOperation(map[string]any{"operationId": "getPayment"}),
			}}),
			doc(map[string]any{"/payments/{id}": map[string]any{
				"get": getOperation(map[string]any{"operationId": "fetchPayment"}),
			}}))
		delta := findDelta(result.Deltas, analysis.OpenApiDeltaOperationIDChanged)
		require.NotNil(t, delta)
		assert.Equal(t, analysis.OpenApiTierBehaviorAnnotating, delta.Tier)
		assert.Equal(t, 0, result.Summary.Breaking)
		assert.Equal(t, analysis.OpenApiTierBehaviorAnnotating, result.Summary.WorstTier)
	})

	t.Run("a description-only change is non-breaking", func(t *testing.T) {
		result := classify(t,
			doc(map[string]any{"/payments/{id}": map[string]any{
				"get": getOperation(map[string]any{"description": "old"}),
			}}),
			doc(map[string]any{"/payments/{id}": map[string]any{
				"get": getOperation(map[string]any{"description": "new"}),
			}}))
		require.Len(t, result.Deltas, 1)
		assert.Equal(t, analysis.OpenApiDeltaDescriptionChanged, result.Deltas[0].Kind)
		assert.Equal(t, analysis.OpenApiTierNonBreaking, result.Deltas[0].Tier)
		assert.Equal(t, 1, result.Summary.NonBreaking)
		assert.Equal(t, analysis.OpenApiTierNonBreaking, result.Summary.WorstTier)
	})

	t.Run("a parameter flipping to required is breaking (parameter_required_added)", func(t *testing.T) {
		result := classify(t,
			doc(map[string]any{"/payments/{id}": map[string]any{
				"get": getOperation(map[string]any{
					"parameters": []any{
						map[string]any{"name": "verbose", "in": "query", "required": false,
							"schema": map[string]any{"type": "boolean"}},
					},
				}),
			}}),
			doc(map[string]any{"/payments/{id}": map[string]any{
				"get": getOperation(map[string]any{
					"parameters": []any{
						map[string]any{"name": "verbose", "in": "query", "required": true,
							"schema": map[string]any{"type": "boolean"}},
					},
				}),
			}}))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterRequiredAdded,
			"/paths/~1payments~1{id}/get/parameters/verbose")
		require.NotNil(t, delta)
		assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
	})

	t.Run("worstTier rolls up: zeros give non-breaking, tier-2-only gives behavior-annotating, any breaking gives breaking", func(t *testing.T) {
		empty := classify(t, doc(map[string]any{}), doc(map[string]any{}))
		assert.Empty(t, empty.Deltas)
		assert.Equal(t, analysis.OpenApiDeltaSummary{
			Breaking:           0,
			BehaviorAnnotating: 0,
			NonBreaking:        0,
			WorstTier:          analysis.OpenApiTierNonBreaking,
		}, empty.Summary)

		mixed := classify(t,
			doc(map[string]any{
				"/a":         map[string]any{"get": getOperation()},
				"/v1/legacy": map[string]any{"get": getOperation()},
			}),
			doc(map[string]any{"/a": map[string]any{"get": getOperation(map[string]any{"deprecated": true})}}))
		assert.GreaterOrEqual(t, mixed.Summary.Breaking, 1)
		assert.GreaterOrEqual(t, mixed.Summary.BehaviorAnnotating, 1)
		assert.Equal(t, analysis.OpenApiTierBreaking, mixed.Summary.WorstTier)
	})

	t.Run("invalid input fails closed with a typed OpenApiClassifierError", func(t *testing.T) {
		valid := doc(map[string]any{})
		cases := []struct {
			before      any
			after       any
			messagePart string
		}{
			{nil, valid, "before"},
			{valid, "not an object", "after"},
			{map[string]any{"openapi": 3}, valid, "missing required 'openapi' or 'swagger'"},
			{map[string]any{"openapi": "3.0.3", "paths": []any{}}, valid, "'paths' must be an object"},
		}
		for _, testCase := range cases {
			_, err := analysis.ClassifyOpenApiDelta(testCase.before, testCase.after)
			require.Error(t, err, "expected OpenApiClassifierError for %s", testCase.messagePart)
			var classifierErr *analysis.OpenApiClassifierError
			require.True(t, errors.As(err, &classifierErr), "expected *OpenApiClassifierError for %s", testCase.messagePart)
			assert.Contains(t, classifierErr.Error(), testCase.messagePart)
		}
	})

	t.Run("classification is deterministic (identical output across runs, stable ordering)", func(t *testing.T) {
		before := doc(
			map[string]any{
				"/payments/{id}": map[string]any{
					"get":  getOperation(map[string]any{"operationId": "getPayment", "deprecated": false}),
					"post": getOperation(),
				},
				"/v1/legacy": map[string]any{"get": getOperation()},
			},
			map[string]any{
				"Status":  map[string]any{"type": "string", "enum": []any{"active", "inactive"}},
				"Payment": map[string]any{"type": "object"},
			})
		after := doc(
			map[string]any{
				"/payments/{id}": map[string]any{
					"get": getOperation(map[string]any{"operationId": "fetchPayment", "deprecated": true}),
				},
				"/payments": map[string]any{"post": getOperation()},
			},
			map[string]any{
				"Status":  map[string]any{"type": "string", "enum": []any{"active"}},
				"Payment": map[string]any{"type": "object", "required": []any{}},
			})
		first, err := analysis.ClassifyOpenApiDelta(before, after)
		require.NoError(t, err)
		second, err := analysis.ClassifyOpenApiDelta(before, after)
		require.NoError(t, err)
		assert.Equal(t, first, second)

		ordered := make([]string, 0, len(first.Deltas))
		for _, delta := range first.Deltas {
			ordered = append(ordered, entryOrderOf(delta))
		}
		assert.IsIncreasing(t, ordered)
	})

	t.Run("the rule table is complete, versioned, and consistent", func(t *testing.T) {
		kinds := []analysis.OpenApiDeltaKind{
			analysis.OpenApiDeltaPathRemoved,
			analysis.OpenApiDeltaPathAdded,
			analysis.OpenApiDeltaOperationRemoved,
			analysis.OpenApiDeltaOperationAdded,
			analysis.OpenApiDeltaParameterRemoved,
			analysis.OpenApiDeltaParameterAdded,
			analysis.OpenApiDeltaParameterRequiredAdded,
			analysis.OpenApiDeltaRequestSchemaChanged,
			analysis.OpenApiDeltaResponseSchemaChanged,
			analysis.OpenApiDeltaDeprecatedAdded,
			analysis.OpenApiDeltaDescriptionChanged,
			analysis.OpenApiDeltaOperationIDChanged,
			analysis.OpenApiDeltaSchemaPropertyRemoved,
			analysis.OpenApiDeltaSchemaPropertyTypeChanged,
			analysis.OpenApiDeltaSchemaPropertyAdded,
			analysis.OpenApiDeltaEnumNarrowed,
			analysis.OpenApiDeltaEnumWidened,
		}
		assert.Equal(t, "1.0.0", analysis.OpenAPIDeltaRules.Version)
		byKind := map[analysis.OpenApiDeltaKind]analysis.OpenApiDeltaRule{}
		for _, rule := range analysis.OpenAPIDeltaRules.Rules {
			byKind[rule.Kind] = rule
		}
		for _, kind := range kinds {
			rule, ok := byKind[kind]
			require.True(t, ok, "rule table must cover kind '%s'", kind)
			assert.Contains(t, []analysis.OpenApiTier{
				analysis.OpenApiTierBreaking,
				analysis.OpenApiTierBehaviorAnnotating,
				analysis.OpenApiTierNonBreaking,
			}, rule.Tier)
			assert.Greater(t, len(rule.Rationale), 10)
		}
		assert.Len(t, analysis.OpenAPIDeltaRules.Rules, len(kinds))
	})
}

func entryOrderOf(entry analysis.OpenApiDeltaEntry) string {
	return entry.Location + "\u0000" + string(entry.Kind) + "\u0000" + entry.Detail
}
