package analysis_test

// Coverage-gap tests for internal/analysis (mission: >= 95% package
// coverage). These target the branches the ported + earlier-coverage suites
// left uncovered in openapi.go, semantics.go, config-diff.go and detect.go.
// All tests are deterministic public-API calls (pure functions).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
)

/* --------------------------------------------------------- openapi enum paths */

func TestCOV_OpenApi_EnumDuplicatesDedup(t *testing.T) {
	// Duplicate enum entries must dedup before comparison — before/after carry
	// the same logical set, so NO enum delta is emitted.
	before := compDoc(map[string]any{"A": map[string]any{
		"type": "string",
		"enum": []any{"a", "a", "b"},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type": "string",
		"enum": []any{"a", "b"},
	}})
	result := classify(t, before, after)
	for _, d := range result.Deltas {
		assert.NotEqual(t, analysis.OpenApiDeltaEnumNarrowed, d.Kind, "duplicate-only changes are not enum changes")
		assert.NotEqual(t, analysis.OpenApiDeltaEnumWidened, d.Kind)
	}
}

func TestCOV_OpenApi_EnumNonMonotonicFailsClosed(t *testing.T) {
	// Added AND removed values → purelyNarrowed/purelyWidened both false →
	// fail closed toward the breaking enum_narrowed kind.
	before := compDoc(map[string]any{"A": map[string]any{
		"type": "string",
		"enum": []any{"a", "b"},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type": "string",
		"enum": []any{"b", "c"},
	}})
	result := classify(t, before, after)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaEnumNarrowed, "/components/schemas/A")
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "non-monotonically")
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
}

/* ----------------------------------------------------- property comparison */

func TestCOV_OpenApi_PropertyBecameRequiredViaSchemaRequiredList(t *testing.T) {
	before := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
		"required":   []any{"a"},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
		"required":   []any{"a", "b"},
	}})
	result := classify(t, before, after)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterRequiredAdded, "/components/schemas/A/properties/b")
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)

	// Non-string entries in the required list are skipped without crashing.
	// (both sides list 42; the string entries drive the diff)
	before = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"b": map[string]any{"type": "string"}},
		"required":   []any{42},
	}})
	after = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"b": map[string]any{"type": "string"}},
		"required":   []any{42, "b"},
	}})
	classify(t, before, after)
}

func TestCOV_OpenApi_ScalarPropertyTypeChanged(t *testing.T) {
	// A property flipping between object-schema and scalar → canonical compare
	// emits schema_property_type_changed.
	before := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string"}},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": "plain-string"},
	}})
	result := classify(t, before, after)
	require.Equal(t, analysis.OpenApiTierBreaking, result.Summary.WorstTier)
	found := false
	for _, d := range result.Deltas {
		if d.Kind == analysis.OpenApiDeltaSchemaPropertyTypeChanged {
			found = true
		}
	}
	assert.True(t, found, "scalar-vs-object property change must be classified")

	// Equal scalar canonical → no delta.
	before = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": "same"},
	}})
	after = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": "same"},
	}})
	result = classify(t, before, after)
	assert.Empty(t, result.Deltas)
}

func TestCOV_OpenApi_PropertyNestedNoFinerRule(t *testing.T) {
	// Two plain-object schemas with equal type but a changed field with no
	// finer-grained rule (format/maxLength) → coarse catch at the property.
	before := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string", "format": "date"}},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string", "format": "date-time"}},
	}})
	result := classify(t, before, after)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyTypeChanged, "/components/schemas/A/properties/x")
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "no finer-grained rule")
	assert.Equal(t, analysis.OpenApiTierBreaking, delta.Tier)
}

func TestCOV_OpenApi_ComponentSchemaNoFinerRuleCatch(t *testing.T) {
	// compareInlineSchema with equal canonical → no delta; changed with no fine
	// rule → the coarse catch kind fires at the component level too.
	before := compDoc(map[string]any{"A": map[string]any{"type": "string", "format": "date"}})
	after := compDoc(map[string]any{"A": map[string]any{"type": "string", "format": "date-time"}})
	result := classify(t, before, after)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyTypeChanged, "/components/schemas/A")
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "no finer-grained rule")
}

func TestCOV_OpenApi_NonObjectSchemaCatch(t *testing.T) {
	// A component schema turning from object into scalar → non-object catch.
	before := compDoc(map[string]any{"A": map[string]any{"type": "object"}})
	after := compDoc(map[string]any{"A": "scalar"})
	result := classify(t, before, after)
	delta := findDelta(result.Deltas, analysis.OpenApiDeltaSchemaPropertyTypeChanged)
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "non-object schema")
}

func TestCOV_OpenApi_NestedSchemaDepthCap(t *testing.T) {
	// At the documented depth cap the fine-grained recursive compare stops and
	// the coarse canonical fallback classifies the deepest change.
	nested := func(leaf map[string]any) map[string]any {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{"d": leaf},
		}
	}
	beforeLeaf := map[string]any{"type": "string", "format": "date"}
	afterLeaf := map[string]any{"type": "string", "format": "date-time"}
	beforeSchema := nested(nested(nested(nested(beforeLeaf))))
	afterSchema := nested(nested(nested(nested(afterLeaf))))
	result := classify(t, compDoc(map[string]any{"A": beforeSchema}), compDoc(map[string]any{"A": afterSchema}))
	assert.NotEmpty(t, result.Deltas)
	assert.Equal(t, analysis.OpenApiTierBreaking, result.Summary.WorstTier)
}

/* ------------------------------------------------------- request body paths */

func TestCOV_OpenApi_RequestBodyAddedRemoved(t *testing.T) {
	base := func() map[string]any {
		return oapiDoc(map[string]any{"/p": map[string]any{"post": map[string]any{"responses": resp("ok")}}})
	}
	withBody := func() map[string]any {
		d := base()
		post := d["paths"].(map[string]any)["/p"].(map[string]any)["post"].(map[string]any)
		post["requestBody"] = map[string]any{"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"type": "string"}},
		}}
		return d
	}

	// added.
	result := classify(t, base(), withBody())
	added := findDeltaAt(result.Deltas, analysis.OpenApiDeltaRequestSchemaChanged, "/paths/~1p/post/requestBody")
	require.NotNil(t, added)
	assert.Contains(t, added.Detail, "added")

	// removed.
	result = classify(t, withBody(), base())
	removed := findDeltaAt(result.Deltas, analysis.OpenApiDeltaRequestSchemaChanged, "/paths/~1p/post/requestBody")
	require.NotNil(t, removed)
	assert.Contains(t, removed.Detail, "removed")

	// required flip false -> true.
	withBodyRequired := func() map[string]any {
		d := withBody()
		post := d["paths"].(map[string]any)["/p"].(map[string]any)["post"].(map[string]any)
		post["requestBody"].(map[string]any)["required"] = true
		return d
	}
	result = classify(t, withBody(), withBodyRequired())
	require.NotNil(t, findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterRequiredAdded, "/paths/~1p/post/requestBody"))

	// requestBody changed with a fine rule (schema type change).
	withBodyInteger := func() map[string]any {
		d := withBody()
		post := d["paths"].(map[string]any)["/p"].(map[string]any)["post"].(map[string]any)
		post["requestBody"] = map[string]any{"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"type": "integer"}},
		}}
		return d
	}
	result = classify(t, withBody(), withBodyInteger())
	require.NotNil(t, findDelta(result.Deltas, analysis.OpenApiDeltaRequestSchemaChanged))
}

/* -------------------------------------------------- content/media-type paths */

func TestCOV_OpenApi_MediaTypeAddedRemoved(t *testing.T) {
	response := func(mediaTypes ...string) map[string]any {
		content := map[string]any{}
		for _, mt := range mediaTypes {
			content[mt] = map[string]any{"schema": map[string]any{"type": "string"}}
		}
		return map[string]any{"200": map[string]any{"description": "ok", "content": content}}
	}
	jsonOnly := oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": response("application/json")})})
	both := oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": response("application/json", "text/plain")})})

	// media type added.
	result := classify(t, jsonOnly, both)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged,
		"/paths/~1p/get/responses/200/content/text~1plain/schema")
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "added")

	// media type removed.
	result = classify(t, both, jsonOnly)
	delta = findDeltaAt(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged,
		"/paths/~1p/get/responses/200/content/text~1plain/schema")
	require.NotNil(t, delta)
	assert.Contains(t, delta.Detail, "removed")

	// media-type entry is a non-object on the after side — classified via the
	// catch path without crashing.
	weird := oapiDoc(map[string]any{"/p": opMap(map[string]any{
		"responses": map[string]any{"200": map[string]any{
			"description": "ok",
			"content":     map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "string"}}, "text/plain": "not-an-object"},
		}},
	})})
	result = classify(t, jsonOnly, weird)
	assert.NotEmpty(t, result.Deltas)
}

/* ----------------------------------------------------------- response paths */

func TestCOV_OpenApi_ResponseAddedRemovedAndInvalid(t *testing.T) {
	oneResponse := func() map[string]any {
		return oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": map[string]any{"200": map[string]any{"description": "ok"}}})})
	}
	twoResponses := func() map[string]any {
		return oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": map[string]any{
			"200": map[string]any{"description": "ok"},
			"404": map[string]any{"description": "missing"},
		}})})
	}

	// response added.
	result := classify(t, oneResponse(), twoResponses())
	added := findDelta(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged)
	require.NotNil(t, added)
	assert.Contains(t, added.Detail, "added")

	// response removed.
	result = classify(t, twoResponses(), oneResponse())
	removed := findDelta(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged)
	require.NotNil(t, removed)
	assert.Contains(t, removed.Detail, "removed")

	// responses object missing entirely on both sides → responses added.
	noResponses := oapiDoc(map[string]any{"/p": opMap(map[string]any{})})
	result = classify(t, noResponses, oneResponse())
	require.NotNil(t, findDelta(result.Deltas, analysis.OpenApiDeltaResponseSchemaChanged))

	// response not an object on the after side → typed error.
	bad := oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": map[string]any{"200": "nope"}})})
	_, err := analysis.ClassifyOpenApiDelta(oneResponse(), bad)
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "must be objects")

	// Identical response objects → no delta.
	identical := classify(t, oneResponse(), oneResponse())
	assert.Empty(t, identical.Deltas)
}

/* ----------------------------------------------------------- parameter paths */

func TestCOV_OpenApi_ParametersFailClosed(t *testing.T) {
	op := func(params any) map[string]any {
		return oapiDoc(map[string]any{"/p": opMap(map[string]any{"parameters": params, "responses": resp("ok")})})
	}
	validOp := op([]any{param("q", "query", map[string]any{"type": "string"})})

	// parameters present but not an array.
	_, err := analysis.ClassifyOpenApiDelta(validOp, op(map[string]any{"nope": true}))
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "must be an array")

	// parameter entry not an object.
	_, err = analysis.ClassifyOpenApiDelta(validOp, op([]any{"scalar"}))
	require.Error(t, err)
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "every parameter must be an object")

	// parameter with non-string in.
	_, err = analysis.ClassifyOpenApiDelta(validOp, op([]any{map[string]any{"name": "q", "in": 42}}))
	require.Error(t, err)
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "string 'name' and 'in'")
}

func TestCOV_OpenApi_PathLevelParameters(t *testing.T) {
	pathParams := func(params any) map[string]any {
		return oapiDoc(map[string]any{"/p": map[string]any{
			"get":        map[string]any{"responses": resp("ok")},
			"parameters": params,
		}})
	}
	before := pathParams([]any{param("q", "query", map[string]any{"type": "string"})})
	after := pathParams([]any{})
	result := classify(t, before, after)
	require.NotNil(t, findDelta(result.Deltas, analysis.OpenApiDeltaParameterRemoved))
}

func TestCOV_OpenApi_BodyParameterSchemaChange(t *testing.T) {
	body := func(schema map[string]any) map[string]any {
		return oapiDoc(map[string]any{"/p": opMap(map[string]any{
			"parameters": []any{map[string]any{"name": "body", "in": "body", "schema": schema}},
			"responses":  resp("ok"),
		})})
	}
	result := classify(t, body(map[string]any{"type": "string"}), body(map[string]any{"type": "integer"}))
	require.NotNil(t, findDelta(result.Deltas, analysis.OpenApiDeltaRequestSchemaChanged))
}

/* --------------------------------------------------------- operation/path item */

func TestCOV_OpenApi_OperationNonObjectFailsClosed(t *testing.T) {
	before := oapiDoc(map[string]any{"/p": map[string]any{"get": map[string]any{"responses": resp("ok")}}})
	after := oapiDoc(map[string]any{"/p": map[string]any{"get": "scalar"}})
	_, err := analysis.ClassifyOpenApiDelta(before, after)
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "operation objects must be objects")
}

func TestCOV_OpenApi_OperationAddedRemoved(t *testing.T) {
	onlyGet := oapiDoc(map[string]any{"/p": map[string]any{"get": map[string]any{"responses": resp("ok")}}})
	both := oapiDoc(map[string]any{"/p": map[string]any{
		"get":  map[string]any{"responses": resp("ok")},
		"post": map[string]any{"responses": resp("ok")},
	}})

	result := classify(t, onlyGet, both)
	added := findDelta(result.Deltas, analysis.OpenApiDeltaOperationAdded)
	require.NotNil(t, added)
	assert.Contains(t, added.Detail, "POST")

	result = classify(t, both, onlyGet)
	removed := findDelta(result.Deltas, analysis.OpenApiDeltaOperationRemoved)
	require.NotNil(t, removed)
	assert.Contains(t, removed.Detail, "POST")
}

func TestCOV_OpenApi_PathItemNonObjectFailsClosed(t *testing.T) {
	before := oapiDoc(map[string]any{"/p": map[string]any{"get": map[string]any{"responses": resp("ok")}}})
	after := oapiDoc(map[string]any{"/p": "scalar"})
	_, err := analysis.ClassifyOpenApiDelta(before, after)
	require.Error(t, err)
	var ce *analysis.OpenApiClassifierError
	require.ErrorAs(t, err, &ce)
	assert.Contains(t, ce.Error(), "path item must be an object")
}

/* ------------------------------------------------------------- components */

func TestCOV_OpenApi_ComponentSchemaAdded(t *testing.T) {
	empty := compDoc(map[string]any{})
	withA := compDoc(map[string]any{"A": map[string]any{"type": "object"}})

	result := classify(t, empty, withA)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyAdded, "/components/schemas/A")
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierNonBreaking, delta.Tier)

	result = classify(t, withA, empty)
	require.NotNil(t, findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyRemoved, "/components/schemas/A"))

	// components present on one side only.
	docWrap := func(components any) map[string]any {
		d := oapiDoc(map[string]any{"/p": opMap(map[string]any{"responses": resp("ok")})})
		d["components"] = components
		return d
	}
	result = classify(t, docWrap(map[string]any{"schemas": map[string]any{"A": map[string]any{"type": "object"}}}), docWrap(map[string]any{}))
	removed := findDelta(result.Deltas, analysis.OpenApiDeltaSchemaPropertyRemoved)
	require.NotNil(t, removed)
	assert.Contains(t, removed.Detail, "removed")
}

/* --------------------------------------------------------- classifier inputs */

func TestCOV_OpenApi_DescribeValueBranches(t *testing.T) {
	cases := []struct {
		name      string
		value     any
		wantInMsg string
	}{
		{name: "boolean", value: true, wantInMsg: "boolean"},
		{name: "number", value: 42, wantInMsg: "number"},
		{name: "array", value: []any{1}, wantInMsg: "array"},
		{name: "string", value: "plain", wantInMsg: "string"},
	}
	valid := map[string]any{"openapi": "3.0.0", "paths": map[string]any{}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := analysis.ClassifyOpenApiDelta(tc.value, valid)
			require.Error(t, err)
			var ce *analysis.OpenApiClassifierError
			require.ErrorAs(t, err, &ce)
			assert.Contains(t, ce.Error(), tc.wantInMsg)
		})
	}
}

/* ----------------------------------------------------- config-diff (key-level) */

func TestCOV_DiffConfigKeys_FloatAndUnsafeLeaf(t *testing.T) {
	// float64 keys exercise the numeric normalization branch (JS number).
	diffs, err := analysis.DiffConfigKeys(
		map[string]any{"port": float64(8080), "rate": float64(1.5)},
		map[string]any{"port": float64(9090), "rate": float64(1.5)})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	assert.Equal(t, "port", diffs[0].Key)
	assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)

	// A non-JSON-safe leaf fails closed with the typed config-diff error.
	_, err = analysis.DiffConfigKeys(map[string]any{"x": make(chan int)}, map[string]any{"x": 1})
	require.Error(t, err)
}

/* ---------------------------------------------------------- semantics gaps */

func TestCOV_ConfigDiffFor_TomlIniDeclaredGap(t *testing.T) {
	for _, p := range []string{"app.toml", "server.ini"} {
		result := analysis.ConfigDiffFor(p, "a=1", "a=2")
		require.NotNil(t, result, "path %q must return a declared-gap result", p)
		assert.Nil(t, result.ConfigDiff)
		require.NotNil(t, result.Note)
		assert.Contains(t, *result.Note, "unavailable")
	}
}

func TestCOV_ConfigDiff_Env_ExportAndQuotes(t *testing.T) {
	result := analysis.ConfigDiffFor(".env",
		"# comment\n\nexport COUNT=2\nURL=\"https://example.com\"\nSYM='quoted'\n",
		"export COUNT=3\nURL=\"https://example.org\"\nSYM='quoted'\n")
	require.NotNil(t, result)
	require.NotNil(t, result.ConfigDiff)
	assert.True(t, *result.ConfigDiff)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, "COUNT: 2")
	assert.Contains(t, *result.Note, "URL: \"https://example.com\"")

	// A non-`=` line is a parse failure → declared gap, never a false negative.
	gap := analysis.ConfigDiffFor(".env", "KEY=1\ngarbage-line\n", "KEY=1")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)
	require.NotNil(t, gap.Note)
	assert.Contains(t, *gap.Note, "unparseable")
}

func TestCOV_ConfigDiff_YAML_NonFiniteFloats(t *testing.T) {
	result := analysis.ConfigDiffFor("config.yaml",
		"a: .inf\nb: .nan\nc: -2\n",
		"a: 1\nb: 2\nc: 2\n")
	require.NotNil(t, result)
	require.NotNil(t, result.ConfigDiff)
	assert.True(t, *result.ConfigDiff)
	require.NotNil(t, result.Note)
	// Non-finite YAML floats render via JS String() semantics in the note.
	assert.Contains(t, *result.Note, "a: Infinity → 1")
	assert.Contains(t, *result.Note, "b: NaN → 2")
	assert.Contains(t, *result.Note, "c: -2 → 2")

	// Exponent rendering triggers the exponent cleanup path.
	result = analysis.ConfigDiffFor("config.yaml",
		"x: 0.000000001\n",
		"x: 0.000000002\n")
	require.NotNil(t, result)
	assert.NotNil(t, result.ConfigDiff)
}

func TestCOV_ConfigDiff_YAML_TaggedScalars(t *testing.T) {
	// Explicit core tags resolve when the text passes the tag's test.
	result := analysis.ConfigDiffFor("config.yaml",
		"a: !!str 123\nb: !!int '42'\n",
		"a: !!str 456\nb: !!int '43'\n")
	require.NotNil(t, result)
	require.NotNil(t, result.ConfigDiff)
	assert.True(t, *result.ConfigDiff)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, "a: \"123\" → \"456\"")

	// An explicit non-core tag (!!timestamp) is a declared gap, never a guess.
	gap := analysis.ConfigDiffFor("config.yaml",
		"c: !!timestamp 2020-01-01\n",
		"c: !!timestamp 2021-01-01\n")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)
}

func TestCOV_ConfigDiff_YAML_EmptyMultiDocumentGap(t *testing.T) {
	// Multiple YAML documents → declared gap (eemeli parse error port).
	gap := analysis.ConfigDiffFor("config.yaml", "a: 1\n---\nb: 2\n", "a: 1\n")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)

	// Empty document → declared gap.
	gap = analysis.ConfigDiffFor("config.yaml", "", "a: 1\n")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)

	// Root not a mapping → declared gap.
	gap = analysis.ConfigDiffFor("config.yaml", "- 1\n- 2\n", "a: 1\n")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)

	// Invalid YAML → declared gap.
	gap = analysis.ConfigDiffFor("config.yaml", "a: [unclosed\n", "a: 1\n")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)
}

func TestCOV_ConfigDiff_YAML_DuplicateKeysAndAnchors(t *testing.T) {
	// Duplicate mapping keys → declared gap (eemeli "Map keys must be unique").
	gap := analysis.ConfigDiffFor("config.yaml", "a: 1\na: 2\n", "a: 1\n")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)

	// Anchored + aliased scalars resolve.
	result := analysis.ConfigDiffFor("config.yaml",
		"base: &b 1\ncopy: *b\n",
		"base: &b 2\ncopy: *b\n")
	require.NotNil(t, result)
	require.NotNil(t, result.ConfigDiff)
}

func TestCOV_ConfigDiff_JSON_DeepNestedAndGap(t *testing.T) {
	result := analysis.ConfigDiffFor("config.json",
		`{"a":{"b":{"c":1}},"arr":[1,2,3]}`,
		`{"a":{"b":{"c":2}},"arr":[1,2,3]}`)
	require.NotNil(t, result)
	require.NotNil(t, result.ConfigDiff)
	assert.True(t, *result.ConfigDiff)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, "a.b.c: 1 → 2")

	// Invalid JSON → declared gap.
	gap := analysis.ConfigDiffFor("config.json", "{not json", "{}")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)

	// Root not an object → declared gap.
	gap = analysis.ConfigDiffFor("config.json", "[1,2]", "{}")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)
}

/* ---------------------------------------------------------- formatting detect */

func TestCOV_IsFormattingOnly_MultiHunkFailFast(t *testing.T) {
	// The FIRST hunk of a multi-hunk patch has unbalanced removed/added lines
	// → IsFormattingOnlyFile fails fast at the hunk boundary.
	patch := "@@ -1 +1 @@\n-a\n@@ -5 +5 @@\n-x\n+y\n"
	assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{Path: "f", Patch: patch}))

	// First hunk formatting-only, second changes content → false (end check).
	patch = "@@ -1 +1 @@\n- old\n+ old\n@@ -5 +5 @@\n-x\n+y\n"
	assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{Path: "f", Patch: patch}))

	// Binary diff markers → not formatting.
	assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{Path: "f", Patch: "Binary files a and b differ"}))
	assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{Path: "f", Patch: "GIT binary patch\n@@"}))
}

/* ------------------------------------------- remaining property paths */

func TestCOV_OpenApi_PropertyAddedRequiredAndOptional(t *testing.T) {
	// Added property whose NAME appears in a literal "required" property key
	// of the after-side properties map → added & required (the isRequired
	// helper inspects the properties map shape as written).
	before := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a":        map[string]any{"type": "string"},
			"required": []any{"q"},
			"q":        map[string]any{"type": "string"},
		},
	}})
	result := classify(t, before, after)
	require.NotNil(t, findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterRequiredAdded, "/components/schemas/A/properties/q"))

	// New optional property → schema_property_added (non-breaking).
	after = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
	}})
	result = classify(t, before, after)
	delta := findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyAdded, "/components/schemas/A/properties/b")
	require.NotNil(t, delta)
	assert.Equal(t, analysis.OpenApiTierNonBreaking, delta.Tier)

	// Removed property → schema_property_removed.
	before = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}},
	}})
	after = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
	}})
	result = classify(t, before, after)
	require.NotNil(t, findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyRemoved, "/components/schemas/A/properties/b"))
}

func TestCOV_OpenApi_PropertyBecameRequiredInCompareProperties(t *testing.T) {
	// Both sides have the property; the after-side properties.required flips →
	// compareProperties' became-required branch.
	before := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{},
	}})
	after := compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
		"required":   []any{"a"},
	}})
	result := classify(t, before, after)
	require.NotNil(t, findDeltaAt(result.Deltas, analysis.OpenApiDeltaParameterRequiredAdded, "/components/schemas/A/properties/a"))

	// Non-plain property values that are canonically equal emit nothing,
	// while a sibling change still classifies (the return-nil path).
	before = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"s": "same", "n": 1},
	}})
	after = compDoc(map[string]any{"A": map[string]any{
		"type":       "object",
		"properties": map[string]any{"s": "same", "n": 2},
	}})
	result = classify(t, before, after)
	require.NotNil(t, findDelta(result.Deltas, analysis.OpenApiDeltaSchemaPropertyTypeChanged))
	assert.Nil(t, findDeltaAt(result.Deltas, analysis.OpenApiDeltaSchemaPropertyTypeChanged, "/components/schemas/A/properties/s"))
}

/* -------------------------------------------------------- YAML extra edges */

func TestCOV_ConfigDiff_YAML_SequenceAliasRevisit(t *testing.T) {
	// An aliased sequence is reached twice through yamlNodeValue → declared
	// gap (cycle/shared-reference guard) — never a guessed diff.
	gap := analysis.ConfigDiffFor("config.yaml",
		"a: &s [1]\nb: *s\n",
		"a: [1]\nb: [1]\n")
	require.NotNil(t, gap)
	assert.Nil(t, gap.ConfigDiff)
}

func TestCOV_ConfigDiff_YAML_BoolFalseKey(t *testing.T) {
	result := analysis.ConfigDiffFor("config.yaml", "false: 1\n", "false: 2\n")
	require.NotNil(t, result)
	require.NotNil(t, result.ConfigDiff)
	assert.True(t, *result.ConfigDiff)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, "false: 1")
}

func TestCOV_ConfigDiff_YAML_HugeExponentFloat(t *testing.T) {
	// A float whose exponent overflows float64 fails ParseFloat → the text is
	// kept as the string value (eemeli resolves a JS number/double, and the
	// port honours the text when Go's ParseFloat diverges).
	result := analysis.ConfigDiffFor("config.yaml",
		"x: 1e9999\n",
		"x: 2e9999\n")
	require.NotNil(t, result)
	require.NotNil(t, result.ConfigDiff)
	assert.True(t, *result.ConfigDiff)
	require.NotNil(t, result.Note)
	assert.Contains(t, *result.Note, `x: "1e9999" → "2e9999"`)
}
