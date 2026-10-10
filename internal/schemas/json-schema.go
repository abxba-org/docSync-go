// JSON Schema export (OD-2). Ported from packages/schemas/src/json-schema.ts.
//
// In TS every registry entry carried a live zod schema and the exporter ran it
// through `zod-to-json-schema` to obtain a stable draft-07 JSON Schema with a
// `$id`/`title`. The Go registry carries no zod type — it carries a
// representative validated `Example` value per artifact (OD-R03) — so this
// exporter derives the schema BY REFLECTION from the Go struct type of that
// example. ExportJsonSchema IS the zod-to-json-schema equivalent here: it is
// pure (it only reads the registry), deterministic (no clocks, no randomness,
// sorted required lists), and fails closed on an unknown id or version exactly
// as the TS `throw` did.
package schemas

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

const draft07SchemaURL = "http://json-schema.org/draft-07/schema#"

// JSONSchemaOf derives a draft-07 JSON Schema from the Go type of v by
// reflection. It is the direct-use form of the exporter and shares its engine
// with ExportJsonSchema (which additionally stamps $schema/$id/title). v is
// never mutated and a nil v yields an empty (unconstrained) schema — no panics.
func JSONSchemaOf(v any) map[string]any {
	return schemaForType(reflect.TypeOf(v))
}

// ExportJsonSchema looks up the registry entry for id (and version when given)
// and returns its draft-07 JSON Schema stamped with a stable `$id`/`title`.
//
// It fails closed — returning an error rather than panicking — when no entry
// matches the id/version, preserving the TS `throw new Error(...)` behavior.
func ExportJsonSchema(id string, version ...int) (map[string]any, error) {
	entry := GetSchema(id, version...)
	if entry == nil {
		resolved := 1
		if len(version) > 0 {
			resolved = version[0]
		}
		return nil, docutil.SchemaError("unknown schema id/version in registry: %s/v%d", id, resolved)
	}

	base := JSONSchemaOf(entry.Example)
	stamp := fmt.Sprintf("%s/v%d", entry.ID, entry.Version)

	out := make(map[string]any, len(base)+3)
	for k, v := range base {
		out[k] = v
	}
	out["$schema"] = draft07SchemaURL
	out["$id"] = stamp
	out["title"] = stamp
	return out, nil
}

// schemaForType returns the value schema for t (never nullable and never
// identity-stamped): objects nest, pointers are dereferenced to their pointee
// (callers apply nullability at the field boundary), and slices/maps map to
// array/object-with-additionalProperties respectively.
func schemaForType(t reflect.Type) map[string]any {
	if t == nil {
		return map[string]any{}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return objectSchema(t)
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaForType(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaForType(t.Elem())}
	default:
		// interface{}, unknown kinds, etc. — unconstrained value.
		return map[string]any{}
	}
}

// objectSchema builds the strict-object schema for a struct type: emitted
// properties keyed by their json tag names, a deterministic (sorted) required
// list, and additionalProperties:false matching z.strictObject semantics.
func objectSchema(t reflect.Type) map[string]any {
	properties := make(map[string]any)
	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// Skip unexported, non-embedded fields (unmarshalled-invisible).
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}
		name, omitempty, skip := jsonFieldName(f)
		if skip {
			continue
		}

		isPointer := f.Type.Kind() == reflect.Pointer
		propSchema := schemaForType(f.Type)
		if isPointer {
			propSchema = asNullable(propSchema)
		}
		properties[name] = propSchema

		// Required iff the field is neither optional (omitempty) nor a
		// pointer (which is inherently nullable/optional).
		if !omitempty && !isPointer {
			required = append(required, name)
		}
	}

	out := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		sort.Strings(required)
		out["required"] = required
	}
	return out
}

// jsonFieldName resolves the wire property name for a struct field, honoring
// json-tag options. It reports omitempty and skip (json:"-").
func jsonFieldName(f reflect.StructField) (name string, omitempty, skip bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return f.Name, false, false
	}
	if tag == "-" {
		return "", false, true
	}
	name = tag
	if idx := strings.IndexByte(tag, ','); idx >= 0 {
		name = tag[:idx]
		for _, opt := range strings.Split(tag[idx+1:], ",") {
			if opt == "omitempty" {
				omitempty = true
			}
		}
	}
	if name == "" {
		name = f.Name
	}
	return name, omitempty, false
}

// asNullable wraps schema so null also validates (TS's .nullable()). A simple
// string "type" becomes a two-member ["<type>","null"] array; anything else is
// expressed as an anyOf with the null branch.
func asNullable(schema map[string]any) map[string]any {
	if t, ok := schema["type"].(string); ok {
		out := make(map[string]any, len(schema))
		for k, v := range schema {
			out[k] = v
		}
		out["type"] = []any{t, "null"}
		return out
	}
	return map[string]any{
		"anyOf": []any{schema, map[string]any{"type": "null"}},
	}
}
