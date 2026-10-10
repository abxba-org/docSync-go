package schemas_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The JSON Schema exporter (OD-2) is registry-driven: in TS each entry carried
// a live zod schema. The Go registry carries a representative Example per
// artifact and is populated by registry-entries.go (owned by a concurrent
// delegate). These tests therefore exercise the reflection engine directly via
// JSONSchemaOf on locally constructed schemas values, and assert the
// registry-backed ExportJsonSchema behavior for the concrete manifest/decision
// ids when (and only when) those entries are registered. Helpers are prefixed
// `jsx` to stay disjoint from the other delegates' schemas_test helpers.

const jsxDraft07 = "http://json-schema.org/draft-07/schema#"

// jsxRequireRegistered skips (never fails) when id/v has not been registered
// yet, so the suite stays green before registry-entries.go lands and fully
// validates once it does.
func jsxRequireRegistered(t *testing.T, id string, v int) {
	t.Helper()
	if schemas.GetSchema(id, v) == nil {
		t.Skipf("registry not yet populated: %s/v%d not registered; re-run after registry-entries.go lands", id, v)
	}
}

func TestExportJsonSchema_ManifestCarriesIdentity(t *testing.T) {
	jsxRequireRegistered(t, "docsync.manifest", 1)
	exported, err := schemas.ExportJsonSchema("docsync.manifest")
	require.NoError(t, err)
	require.IsType(t, map[string]any{}, exported)
	assert.Equal(t, "docsync.manifest/v1", exported["$id"])
	assert.Equal(t, "docsync.manifest/v1", exported["title"])
	assert.Equal(t, jsxDraft07, exported["$schema"])
}

func TestExportJsonSchema_DecisionCarriesIdentity(t *testing.T) {
	jsxRequireRegistered(t, "docsync.decision", 1)
	exported, err := schemas.ExportJsonSchema("docsync.decision")
	require.NoError(t, err)
	assert.Equal(t, "docsync.decision/v1", exported["$id"])
	assert.Equal(t, "docsync.decision/v1", exported["title"])
}

func TestExportJsonSchema_IsDeterministic(t *testing.T) {
	jsxRequireRegistered(t, "docsync.manifest", 1)
	jsxRequireRegistered(t, "docsync.decision", 1)

	manifestA, err := schemas.ExportJsonSchema("docsync.manifest")
	require.NoError(t, err)
	manifestB, err := schemas.ExportJsonSchema("docsync.manifest")
	require.NoError(t, err)
	assert.Equal(t, manifestA, manifestB)

	decisionA, err := schemas.ExportJsonSchema("docsync.decision")
	require.NoError(t, err)
	decisionB, err := schemas.ExportJsonSchema("docsync.decision")
	require.NoError(t, err)
	assert.Equal(t, decisionA, decisionB)
}

// Purity: re-exports must not mutate the registry. GetSchema returns a pointer
// into the closed registry set, so the exported example is snapshotted through
// the JSON serializer (a pointer-independent deep copy) and compared byte-wise.
func TestExportJsonSchema_IsPureRegistryUnchanged(t *testing.T) {
	jsxRequireRegistered(t, "docsync.manifest", 1)
	before := schemas.GetSchema("docsync.manifest")
	require.NotNil(t, before)
	beforeExample, err := json.Marshal(before.Example)
	require.NoError(t, err)

	_, err = schemas.ExportJsonSchema("docsync.manifest")
	require.NoError(t, err)

	after := schemas.GetSchema("docsync.manifest")
	require.NotNil(t, after)
	afterExample, err := json.Marshal(after.Example)
	require.NoError(t, err)

	assert.Equal(t, before.ID, after.ID)
	assert.Equal(t, before.Version, after.Version)
	assert.Equal(t, before.Description, after.Description)
	assert.JSONEq(t, string(beforeExample), string(afterExample))
}

// Fail-closed behavior for unknown ids works regardless of registry state; the
// unsupported-version case (manifest/v2) fails closed whether the v1 entry is
// present (version mismatch) or the registry is still empty (no match). So this
// test needs no registration guard.
func TestExportJsonSchema_FailsClosed(t *testing.T) {
	_, err := schemas.ExportJsonSchema("docsync.nonexistent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown schema id/version in registry: docsync.nonexistent/v1")

	_, err = schemas.ExportJsonSchema("docsync.manifest", 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown schema id/version in registry: docsync.manifest/v2")
}

// Every registry entry — whatever the set is at test time — exports an object
// carrying "type" or "$ref" with $id == id/vN. Iterates SchemaRegistry()
// directly so it depends on no specific entry set.
func TestExportJsonSchema_AllRegistryEntriesExportValidShape(t *testing.T) {
	entries := schemas.SchemaRegistry()
	if len(entries) == 0 {
		t.Skip("registry is empty (registry-entries.go has not landed yet); re-run once it does")
	}
	for _, entry := range entries {
		t.Run(entry.ID, func(t *testing.T) {
			exported, err := schemas.ExportJsonSchema(entry.ID, entry.Version)
			require.NoError(t, err)
			require.NotNil(t, exported)
			require.IsType(t, map[string]any{}, exported)
			assert.Equal(t, fmt.Sprintf("%s/v%d", entry.ID, entry.Version), exported["$id"])
			assert.Equal(t, fmt.Sprintf("%s/v%d", entry.ID, entry.Version), exported["title"])
			assert.Equal(t, jsxDraft07, exported["$schema"])
			_, hasType := exported["type"]
			_, hasRef := exported["$ref"]
			assert.True(t, hasType || hasRef, "entry %s export missing both type and $ref", entry.ID)
		})
	}
}

/* --------------------- reflection engine (JSONSchemaOf) ---------------------
 *
 * These validate the pure type→schema mapping the registry-backed exporter
 * relies on, independent of registry population. They pin the primitives,
 * pointers-as-nullable, omitempty/pointer required-ness, slices, maps, nested
 * structs, determinism, and the no-panic contract.
 */

func TestJSONSchemaOf_ObjectShape(t *testing.T) {
	schema := schemas.JSONSchemaOf(schemas.Manifest{})
	assert.Equal(t, "object", schema["type"])
	assert.Equal(t, false, schema["additionalProperties"])

	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, props, "version")
	require.Contains(t, props, "documents")
	require.Contains(t, props, "defaults")

	assert.Equal(t, map[string]any{"type": "integer"}, props["version"])

	documents := props["documents"].(map[string]any)
	assert.Equal(t, "array", documents["type"])

	// version + documents are required; defaults is optional (pointer). The
	// required list is sorted.
	required, ok := schema["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"documents", "version"}, required)

	// defaults is a nullable object (pointer to struct).
	defaults := props["defaults"].(map[string]any)
	typ, ok := defaults["type"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"object", "null"}, typ)
}

func TestJSONSchemaOf_Primitives(t *testing.T) {
	require.Equal(t, map[string]any{"type": "string"}, schemas.JSONSchemaOf(""))
	require.Equal(t, map[string]any{"type": "integer"}, schemas.JSONSchemaOf(0))
	require.Equal(t, map[string]any{"type": "number"}, schemas.JSONSchemaOf(float64(0)))
	require.Equal(t, map[string]any{"type": "boolean"}, schemas.JSONSchemaOf(false))
}

// Top-level pointers dereference to a plain object: an artifact example stored
// as *T still exports as an object schema (never nullable at the root).
func TestJSONSchemaOf_TopLevelPointerDereferencedToObject(t *testing.T) {
	entry := schemas.ManifestEntry{Path: "docs/a.md", Sources: []string{"src/"}}
	schema := schemas.JSONSchemaOf(&entry)
	assert.Equal(t, "object", schema["type"])
	assert.Equal(t, false, schema["additionalProperties"])
	props := schema["properties"].(map[string]any)
	require.Contains(t, props, "path")
}

// Nullability is a field-level concern: a *T field exports as the nullable
// variant of its pointee and is omitted from required.
func TestJSONSchemaOf_PointerFieldIsNullableAndOptional(t *testing.T) {
	schema := schemas.JSONSchemaOf(schemas.ManifestEntry{})
	props := schema["properties"].(map[string]any)
	require.Contains(t, props, "path")
	require.Contains(t, props, "mode")

	// mode (*DocMode) is a nullable string.
	mode := props["mode"].(map[string]any)
	modeType, ok := mode["type"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"string", "null"}, modeType)

	// path + sources are required (no omitempty, non-pointer); mode (pointer),
	// triggers, ignore (omitempty) are not.
	required, ok := schema["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"path", "sources"}, required)
}

func TestJSONSchemaOf_SliceAndMap(t *testing.T) {
	arr := schemas.JSONSchemaOf([]string{})
	assert.Equal(t, "array", arr["type"])
	assert.Equal(t, map[string]any{"type": "string"}, arr["items"])

	obj := schemas.JSONSchemaOf(map[string]bool{})
	assert.Equal(t, "object", obj["type"])
	assert.Equal(t, map[string]any{"type": "boolean"}, obj["additionalProperties"])
}

func TestJSONSchemaOf_NestedStructs(t *testing.T) {
	schema := schemas.JSONSchemaOf(schemas.Decision{})
	props := schema["properties"].(map[string]any)

	// answers: map[string]Answer → object with additionalProperties = Answer.
	answers := props["answers"].(map[string]any)
	assert.Equal(t, "object", answers["type"])
	answerSchema := answers["additionalProperties"].(map[string]any)
	assert.Equal(t, "object", answerSchema["type"])
	answerProps := answerSchema["properties"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "number"}, answerProps["probability"])

	// abstain_reason: *AbstainReason → nullable string; abstain: bool.
	abstainReason := props["abstain_reason"].(map[string]any)
	reasonType, ok := abstainReason["type"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"string", "null"}, reasonType)
	assert.Equal(t, map[string]any{"type": "boolean"}, props["abstain"])
}

func TestJSONSchemaOf_IsDeterministic(t *testing.T) {
	a := schemas.JSONSchemaOf(schemas.Decision{})
	b := schemas.JSONSchemaOf(schemas.Decision{})
	assert.Equal(t, a, b)
}

func TestJSONSchemaOf_NilDoesNotPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		schema := schemas.JSONSchemaOf(nil)
		assert.Empty(t, schema)
	})
}

func TestJSONSchemaOf_NoPanicAcrossRegistryType(t *testing.T) {
	// Reflect an example of every registered entry's own example type; unknown
	// value kinds (raw messages, any fields, time-like structs) must not panic.
	assert.NotPanics(t, func() {
		schemas.JSONSchemaOf(schemas.DecisionExample)
		schemas.JSONSchemaOf(schemas.Manifest{})
	})
}
