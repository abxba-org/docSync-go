package schemas

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// covScalarNode decodes a single YAML scalar document node for direct
// yamlScalarToGeneric coverage (unexported helper).
func covScalarNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
	require.NotEmpty(t, doc.Content)
	return doc.Content[0]
}

// TestCOV_SCHEMA_001_NormalizeYAMLValueCovertsNonStringMapKeys covers the
// unexported normalizeYAMLValue bridge (map[any]any, map[string]any, []any and
// scalar fallback), which reaches the JSON-marshaling path otherwise
// unreachable because production decoding goes through yamlNodeToGeneric.
func TestCOV_SCHEMA_001_NormalizeYAMLValueCovertsNonStringMapKeys(t *testing.T) {
	out := normalizeYAMLValue(map[any]any{
		1: "one",
		2: "two",
		"nested": map[string]any{
			"deep": []any{"x", map[any]any{3: "three"}},
		},
		"scalar": 42,
	})
	require.IsType(t, map[string]any{}, out)
	converted, ok := out.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "one", converted["1"])
	assert.Equal(t, "two", converted["2"])
	assert.Equal(t, 42, converted["scalar"])
	nested, ok := converted["nested"].(map[string]any)
	require.True(t, ok)
	list, ok := nested["deep"].([]any)
	require.True(t, ok)
	require.Len(t, list, 2)
	assert.Equal(t, "x", list[0])
	inner, ok := list[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "three", inner["3"])

	assert.Equal(t, "plain", normalizeYAMLValue("plain"))
	assert.Equal(t, 7, normalizeYAMLValue(7))
}

// TestCOV_SCHEMA_002_YAMLScalarToGenericResolution covers the tag-driven
// scalar resolution branches, including the uint64 fallback used when an
// integer literal overflows int64.
func TestCOV_SCHEMA_002_YAMLScalarToGenericResolution(t *testing.T) {
	intNode := covScalarNode(t, "42")
	value, err := yamlScalarToGeneric(intNode)
	require.NoError(t, err)
	assert.Equal(t, int64(42), value)

	bigNode := covScalarNode(t, "18446744073709551615")
	value, err = yamlScalarToGeneric(bigNode)
	require.NoError(t, err)
	assert.Equal(t, uint64(18446744073709551615), value)

	floatNode := covScalarNode(t, "1.5")
	value, err = yamlScalarToGeneric(floatNode)
	require.NoError(t, err)
	assert.Equal(t, 1.5, value)

	boolNode := covScalarNode(t, "true")
	value, err = yamlScalarToGeneric(boolNode)
	require.NoError(t, err)
	assert.Equal(t, true, value)

	nullNode := covScalarNode(t, "null")
	value, err = yamlScalarToGeneric(nullNode)
	require.NoError(t, err)
	assert.Nil(t, value)

	timestampNode := covScalarNode(t, "2026-10-08T12:00:00.000Z")
	value, err = yamlScalarToGeneric(timestampNode)
	require.NoError(t, err)
	assert.Equal(t, "2026-10-08T12:00:00.000Z", value)
}

// TestCOV_SCHEMA_003_AbsIntNegative covers the negative branch of absInt,
// consumed by multisetDelta when a patched document carries FEWER copies of a
// line than the original.
func TestCOV_SCHEMA_003_AbsIntNegative(t *testing.T) {
	assert.Equal(t, 5, absInt(-5))
	assert.Equal(t, 0, absInt(0))
	assert.Equal(t, 5, absInt(5))
}

// TestCOV_SCHEMA_004_EvidenceShapePredicates covers the unexported
// machine-resolvable-anchor predicates used by EvidenceRefIssue.
func TestCOV_SCHEMA_004_EvidenceShapePredicates(t *testing.T) {
	assert.False(t, isPathish(""))
	assert.False(t, isPathish("/absolute.md"))
	assert.False(t, isPathish("docs\\payments.md"))
	assert.False(t, isPathish("docs//payments.md"))
	assert.False(t, isPathish("./payments.md"))
	assert.False(t, isPathish("docs/../payments.md"))
	assert.False(t, isPathish("docs/pay ments.md"))
	assert.False(t, isPathish("docs:payments.md"))
	assert.True(t, isPathish("docs/payments.md"))

	assert.False(t, isDependencyName("."))
	assert.False(t, isDependencyName(".."))
	assert.True(t, isDependencyName("lodash"))
	assert.True(t, isDependencyName("@scope/pkg"))
}

// TestCOV_SCHEMA_005_ItoaNegativeAndLarge covers the itoa negative-format
// branch, unreachable through the validators (they only index non-negative
// slices), plus the multi-digit path.
func TestCOV_SCHEMA_005_ItoaNegativeAndLarge(t *testing.T) {
	assert.Equal(t, "-7", itoa(-7))
	assert.Equal(t, "0", itoa(0))
	assert.Equal(t, "12345", itoa(12345))
	assert.Equal(t, "-1", itoa(-1))
}

// TestCOV_SCHEMA_006_DenumberJSONValues covers the nested json.Number
// conversion consumed by MarshalJSONKeysYAML.
func TestCOV_SCHEMA_006_DenumberJSONValues(t *testing.T) {
	raw := []byte(`{"outer":{"list":[1,2.5],"flag":true}}`)
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&generic))

	out := denumberJSONValues(generic)
	outer, ok := out.(map[string]any)
	require.True(t, ok)
	inner, ok := outer["outer"].(map[string]any)
	require.True(t, ok)
	list, ok := inner["list"].([]any)
	require.True(t, ok)
	assert.Equal(t, int64(1), list[0])
	assert.Equal(t, 2.5, list[1])
	assert.Equal(t, true, inner["flag"])
	assert.Equal(t, int64(9), denumberJSONValues(json.Number("9")))
	assert.Equal(t, "literal", denumberJSONValues("literal"))
}

// TestCOV_SCHEMA_007_JSONSchemaOfUnconstrainedValues covers the reflection
// exporter's nil-type and unsupported-kind branches, plus the pointer
// dereference loop.
func TestCOV_SCHEMA_007_JSONSchemaOfUnconstrainedValues(t *testing.T) {
	nilSchema := JSONSchemaOf(nil)
	assert.Equal(t, map[string]any{}, nilSchema)

	chanSchema := JSONSchemaOf(make(chan int))
	assert.Equal(t, map[string]any{}, chanSchema)

	funcSchema := JSONSchemaOf(func() {})
	assert.Equal(t, map[string]any{}, funcSchema)

	ptrChainSchema := JSONSchemaOf(new(new(new(""))))
	assert.Equal(t, map[string]any{"type": "string"}, ptrChainSchema)

	sliceSchema := JSONSchemaOf([]int{})
	assert.Equal(t, map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, sliceSchema)

	mapSchema := JSONSchemaOf(map[string]float64{})
	assert.Equal(t, map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "number"}}, mapSchema)
}

// covTagMix is a local struct exercising every jsonFieldName branch the
// shipped wire types never reach (missing tag, json:"-", options-only tag,
// pointer-to-interface nullable anyOf, unexported field skipped).
type covTagMix struct {
	Bare       string `json:"bare"`
	NoTag      string
	Hidden     string `json:"-"`
	Opts       string `json:",omitempty"`
	AnyRef     *any   `json:"any_ref,omitempty"`
	unexported string
}

// TestCOV_SCHEMA_008_ObjectSchemaReflectionEdgeCases covers jsonFieldName's
// no-tag / skip / options-only branches and asNullable's anyOf fallback.
func TestCOV_SCHEMA_008_ObjectSchemaReflectionEdgeCases(t *testing.T) {
	schema := JSONSchemaOf(covTagMix{})
	assert.Equal(t, "object", schema["type"])
	assert.Equal(t, false, schema["additionalProperties"])

	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "bare")
	assert.Contains(t, props, "NoTag")
	assert.Contains(t, props, "Opts")
	assert.Contains(t, props, "any_ref")
	assert.NotContains(t, props, "Hidden")
	assert.NotContains(t, props, "unexported")

	anyRef, ok := props["any_ref"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, anyRef, "anyOf")

	required, ok := schema["required"].([]string)
	require.True(t, ok)
	assert.NotContains(t, required, "Opts")
	assert.Contains(t, required, "bare")
}

// TestCOV_SCHEMA_009_RegisterDuplicatePanics covers the closed-set registry
// guard: a duplicate id fails closed with a panic (never silently shadowing).
func TestCOV_SCHEMA_009_RegisterDuplicatePanics(t *testing.T) {
	assert.Panics(t, func() {
		Register(RegisteredSchema{ID: "docsync.manifest", Version: 1})
	})
}
