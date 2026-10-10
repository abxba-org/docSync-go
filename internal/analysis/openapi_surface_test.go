package analysis_test

// Continues packages/core/test/openapi-classifier.test.ts (P11-T07; OD-R28
// three-tier) over the classifier's request-body / response / parameter
// surfaces: media-type add/remove, the required-flip on requestBody, the
// no-finer-rule fallbacks, parameter add/remove, path-level parameters, and
// the fail-closed typed errors for non-object operations/responses.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
)

// pathOp wraps one GET operation under a single path (the common shape of the
// TS fixtures).
func pathOp(operation map[string]any) map[string]any {
	return map[string]any{"/payments/{id}": map[string]any{"get": operation}}
}

// operationWith builds an operation carrying the given requestBody (the
// response half stays the standard getOperation()).
func operationWithRequestBody(requestBody map[string]any) map[string]any {
	operation := getOperation()
	operation["requestBody"] = requestBody
	return operation
}

// jsonBody builds a requestBody/response content block for one media type.
func jsonBody(schema map[string]any) map[string]any {
	return map[string]any{
		"required": true,
		"content": map[string]any{
			jsonCT: map[string]any{"schema": schema},
		},
	}
}

func TestClassifyOpenApiDelta_RequestBody_P11T07(t *testing.T) {
	paymentSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"amount":   map[string]any{"type": "number"},
			"currency": map[string]any{"type": "string"},
		},
	}

	t.Run("requestBody added and removed are request_schema_changed", func(t *testing.T) {
		added := classify(t, doc(pathOp(getOperation())),
			doc(pathOp(operationWithRequestBody(jsonBody(paymentSchema)))))
		addedDelta := findDeltaAt(added.Deltas, analysis.OpenApiDeltaRequestSchemaChanged,
			"/paths/~1payments~1{id}/get/requestBody")
		require.NotNil(t, addedDelta)
		assert.Equal(t, "requestBody added", addedDelta.Detail)
		assert.Equal(t, analysis.OpenApiTierBreaking, addedDelta.Tier)

		removed := classify(t, doc(pathOp(operationWithRequestBody(jsonBody(paymentSchema)))),
			doc(pathOp(getOperation())))
		removedDelta := findDeltaAt(removed.Deltas, analysis.OpenApiDeltaRequestSchemaChanged,
			"/paths/~1payments~1{id}/get/requestBody")
		require.NotNil(t, removedDelta)
		assert.Equal(t, "requestBody removed", removedDelta.Detail)
	})

	t.Run("requestBody flipping to required is parameter_required_added", func(t *testing.T) {
		optional := jsonBody(paymentSchema)
		optional["required"] = false
		required := jsonBody(paymentSchema)
		result := classify(t, doc(pathOp(operationWithRequestBody(optional))),
			doc(pathOp(operationWithRequestBody(required))))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterRequiredAdded,
			"/paths/~1payments~1{id}/get/requestBody")
		require.NotNil(t, delta)
		assert.Equal(t, "requestBody became required", delta.Detail)
		assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
	})

	t.Run("media types added and removed are reported at the media schema location", func(t *testing.T) {
		before := operationWithRequestBody(jsonBody(paymentSchema))
		after := operationWithRequestBody(jsonBody(paymentSchema))
		after["requestBody"].(map[string]any)["content"].(map[string]any)["application/xml"] =
			map[string]any{"schema": paymentSchema}
		result := classify(t, doc(pathOp(before)), doc(pathOp(after)))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaRequestSchemaChanged,
			"/paths/~1payments~1{id}/get/requestBody/content/application~1xml/schema")
		require.NotNil(t, delta)
		assert.Equal(t, "media type 'application/xml' added", delta.Detail)

		reverse := classify(t, doc(pathOp(after)), doc(pathOp(before)))
		reverseDelta := findDeltaAt(reverse.Deltas, analysis.OpenApiDeltaRequestSchemaChanged,
			"/paths/~1payments~1{id}/get/requestBody/content/application~1xml/schema")
		require.NotNil(t, reverseDelta)
		assert.Equal(t, "media type 'application/xml' removed", reverseDelta.Detail)
	})

	t.Run("a requestBody schema property change is classified by the finer rule", func(t *testing.T) {
		widened := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"amount":   map[string]any{"type": "number"},
				"currency": map[string]any{"type": "string"},
				"note":     map[string]any{"type": "string"},
			},
		}
		result := classify(t, doc(pathOp(operationWithRequestBody(jsonBody(paymentSchema)))),
			doc(pathOp(operationWithRequestBody(jsonBody(widened)))))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyAdded,
			"/paths/~1payments~1{id}/get/requestBody/content/application~1json/schema/properties/note")
		require.NotNil(t, delta)
		assert.Equal(t, analysis.OpenApiTierNonBreaking, delta.Tier)
	})

	t.Run("a metadata-only requestBody change falls back to the catch-all rule", func(t *testing.T) {
		before := jsonBody(paymentSchema)
		before["description"] = "one"
		after := jsonBody(paymentSchema)
		after["description"] = "two"
		result := classify(t, doc(pathOp(operationWithRequestBody(before))),
			doc(pathOp(operationWithRequestBody(after))))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaRequestSchemaChanged,
			"/paths/~1payments~1{id}/get/requestBody")
		require.NotNil(t, delta)
		assert.Equal(t, "requestBody changed (no finer rule applies)", delta.Detail)
	})
}

func TestClassifyOpenApiDelta_Responses_P11T07(t *testing.T) {
	t.Run("response statuses added and removed are response_schema_changed", func(t *testing.T) {
		before := getOperation()
		after := getOperation()
		after["responses"].(map[string]any)["404"] = map[string]any{"description": "not found"}
		result := classify(t, doc(pathOp(before)), doc(pathOp(after)))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged,
			"/paths/~1payments~1{id}/get/responses/404")
		require.NotNil(t, delta)
		assert.Equal(t, "response '404' added", delta.Detail)
		assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)

		reverse := classify(t, doc(pathOp(after)), doc(pathOp(before)))
		reverseDelta := findDeltaAt(reverse.Deltas, analysis.OpenApiDeltaResponseSchemaChanged,
			"/paths/~1payments~1{id}/get/responses/404")
		require.NotNil(t, reverseDelta)
		assert.Equal(t, "response '404' removed", reverseDelta.Detail)
	})

	t.Run("a response media type added is reported at the media schema location", func(t *testing.T) {
		before := getOperation()
		after := getOperation()
		after["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/xml"] =
			map[string]any{"schema": map[string]any{"type": "object"}}
		result := classify(t, doc(pathOp(before)), doc(pathOp(after)))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged,
			"/paths/~1payments~1{id}/get/responses/200/content/application~1xml/schema")
		require.NotNil(t, delta)
		assert.Equal(t, "media type 'application/xml' added", delta.Detail)
	})

	t.Run("a response change without a finer rule falls back to the catch-all", func(t *testing.T) {
		// A headers-only change has no finer rule: the whole response object
		// differs under canonical comparison.
		before := getOperation()
		after := getOperation()
		after["responses"].(map[string]any)["200"].(map[string]any)["headers"] =
			map[string]any{"X-Trace": map[string]any{"schema": map[string]any{"type": "string"}}}
		result := classify(t, doc(pathOp(before)), doc(pathOp(after)))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged,
			"/paths/~1payments~1{id}/get/responses/200")
		require.NotNil(t, delta)
		assert.Equal(t, "response '200' changed (no finer rule applies)", delta.Detail)
	})

	t.Run("responses added wholesale is one delta at the responses location", func(t *testing.T) {
		before := map[string]any{"operationId": "get"}
		after := getOperation()
		result := classify(t, doc(pathOp(before)), doc(pathOp(after)))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged,
			"/paths/~1payments~1{id}/get/responses")
		require.NotNil(t, delta)
		assert.Equal(t, "responses added", delta.Detail)
	})
}

func TestClassifyOpenApiDelta_Parameters_P11T07(t *testing.T) {
	t.Run("an operation parameter added and removed is parameter_added/_removed", func(t *testing.T) {
		before := getOperation()
		after := getOperation()
		after["parameters"] = []any{
			map[string]any{"name": "verbose", "in": "query", "schema": map[string]any{"type": "boolean"}},
		}
		result := classify(t, doc(pathOp(before)), doc(pathOp(after)))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterAdded,
			"/paths/~1payments~1{id}/get/parameters/verbose")
		require.NotNil(t, delta)
		assert.Equal(t, "parameter 'verbose' (in query) added", delta.Detail)
		// Only optional additions land here; required ones are
		// parameter_required_added (the rule table's rationale).
		assert.Equal(t, analysis.OpenApiTierNonBreaking, delta.Tier)

		reverse := classify(t, doc(pathOp(after)), doc(pathOp(before)))
		reverseDelta := findDeltaAt(reverse.Deltas, analysis.OpenApiDeltaParameterRemoved,
			"/paths/~1payments~1{id}/get/parameters/verbose")
		require.NotNil(t, reverseDelta)
		assert.Equal(t, "parameter 'verbose' (in query) removed", reverseDelta.Detail)
	})

	t.Run("path-level parameters are diffed at the path location", func(t *testing.T) {
		beforePath := map[string]any{
			"parameters": []any{
				map[string]any{"name": "tenant", "in": "path", "required": true,
					"schema": map[string]any{"type": "string"}},
			},
			"get": getOperation(),
		}
		afterPath := map[string]any{
			"parameters": []any{
				map[string]any{"name": "tenant", "in": "path", "required": true,
					"schema": map[string]any{"type": "string"}},
				map[string]any{"name": "locale", "in": "query",
					"schema": map[string]any{"type": "string"}},
			},
			"get": getOperation(),
		}
		result := classify(t, doc(map[string]any{"/payments/{id}": beforePath}),
			doc(map[string]any{"/payments/{id}": afterPath}))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterAdded,
			"/paths/~1payments~1{id}/parameters/locale")
		require.NotNil(t, delta)
		assert.Equal(t, "parameter 'locale' (in query) added", delta.Detail)
	})

	t.Run("a body-parameter schema change is request_schema_changed", func(t *testing.T) {
		before := getOperation()
		before["parameters"] = []any{
			map[string]any{"name": "payload", "in": "body",
				"schema": map[string]any{"type": "object"}},
		}
		after := getOperation()
		after["parameters"] = []any{
			map[string]any{"name": "payload", "in": "body",
				"schema": map[string]any{"type": "array"}},
		}
		result := classify(t, doc(pathOp(before)), doc(pathOp(after)))
		delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaRequestSchemaChanged,
			"/paths/~1payments~1{id}/get/parameters/payload/schema")
		require.NotNil(t, delta)
	})
}

func TestClassifyOpenApiDelta_FailClosedShapes_P11T07(t *testing.T) {
	t.Run("a non-object response entry fails closed with the typed classifier error", func(t *testing.T) {
		before := getOperation()
		after := getOperation()
		after["responses"].(map[string]any)["200"] = "not-an-object"
		_, err := analysis.ClassifyOpenApiDelta(doc(pathOp(before)), doc(pathOp(after)))
		require.Error(t, err)
		var classifierErr *analysis.OpenApiClassifierError
		require.ErrorAs(t, err, &classifierErr)
		assert.Contains(t, classifierErr.Error(),
			"/paths/~1payments~1{id}/get/responses/200: response objects must be objects on both sides")
	})

	t.Run("a non-array parameters list fails closed", func(t *testing.T) {
		before := getOperation()
		after := getOperation()
		after["parameters"] = "not-an-array"
		_, err := analysis.ClassifyOpenApiDelta(doc(pathOp(before)), doc(pathOp(after)))
		require.Error(t, err)
		var classifierErr *analysis.OpenApiClassifierError
		require.ErrorAs(t, err, &classifierErr)
		assert.Contains(t, classifierErr.Error(), "'parameters' must be an array")
	})
}
