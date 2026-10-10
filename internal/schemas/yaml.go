package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// The YAML decode path funnels every YAML document through a JSON bridge:
// YAML → generic value → JSON bytes → struct decode driven by the json tags.
//
// This makes the json tags the SINGLE source of truth for key names across
// both wire formats (CONVENTIONS §Types & serialization) and lets the strict
// decoder reject unknown keys in YAML exactly as it does for JSON (zod
// strictObject semantics) — without requiring duplicate yaml tags on every
// schema struct. The yaml.v3 tags some structs carry are redundant fallbacks.

// decodeYAMLOrJSON decodes YAML (or JSON) bytes into dst through the JSON
// bridge, rejecting unknown keys (strict object semantics).
func decodeYAMLOrJSON(data []byte, dst any) error {
	generic, err := yamlToGeneric(data)
	if err != nil {
		return err
	}
	jsonBytes, err := json.Marshal(generic)
	if err != nil {
		return &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	return decodeJSONStrictValue(jsonBytes, dst, true)
}

// decodeStrictYAMLOrJSON decodes YAML/JSON bytes into dst with the allowedKeys
// probe kept for API compatibility; the actual strictness comes from the JSON
// bridge's DisallowUnknownFields.
func decodeStrictYAMLOrJSON(data []byte, dst any, allowedKeys []string) error {
	generic, err := yamlToGeneric(data)
	if err != nil {
		return err
	}
	jsonBytes, err := json.Marshal(generic)
	if err != nil {
		return &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	return decodeJSONStrictValue(jsonBytes, dst, true)
}

func decodeJSONStrictValue(data []byte, dst any, strict bool) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		return &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	return nil
}

// yamlToGeneric parses YAML/JSON into a generic value with JSON-compatible
// shapes (map[string]any, []any, scalars). A NODE walk (not a plain
// Unmarshal into any) is required so timestamp-looking scalars keep their
// exact wire text — yaml.v3's resolver re-types `2026-10-08T12:00:00.000Z`
// as a time.Time, which JSON round-trips as `2026-10-08T12:00:00Z` (the
// `.000` is lost and the schema's datetime literal no longer matches).
func yamlToGeneric(data []byte) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, nil
	}
	value, err := yamlNodeToGeneric(doc.Content[0])
	if err != nil {
		return nil, err
	}
	return value, nil
}

// yamlNodeToGeneric converts one YAML node into a JSON-compatible value.
func yamlNodeToGeneric(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			return nil, nil
		}
		return yamlNodeToGeneric(node.Content[0])
	case yaml.AliasNode:
		return yamlNodeToGeneric(node.Alias)
	case yaml.MappingNode:
		out := make(map[string]any, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, err := yamlNodeToGeneric(node.Content[i])
			if err != nil {
				return nil, err
			}
			value, err := yamlNodeToGeneric(node.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[fmt.Sprintf("%v", key)] = value
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			value, err := yamlNodeToGeneric(item)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case yaml.ScalarNode:
		return yamlScalarToGeneric(node)
	default:
		return nil, &ValidationError{Issues: []Issue{
			{Message: fmt.Sprintf("unsupported YAML node kind %d", node.Kind)},
		}}
	}
}

// yamlScalarToGeneric resolves a scalar by its announced tag: numerics and
// booleans become native values, everything else (including timestamps)
// keeps the exact source text.
func yamlScalarToGeneric(node *yaml.Node) (any, error) {
	switch node.Tag {
	case "!!int":
		var i int64
		if err := node.Decode(&i); err == nil {
			return i, nil
		}
		var u uint64
		if err := node.Decode(&u); err == nil {
			return u, nil
		}
		return node.Value, nil
	case "!!float":
		var f float64
		if err := node.Decode(&f); err == nil {
			return f, nil
		}
		return node.Value, nil
	case "!!bool":
		var b bool
		if err := node.Decode(&b); err == nil {
			return b, nil
		}
		return node.Value, nil
	case "!!null":
		return nil, nil
	default:
		return node.Value, nil
	}
}

// normalizeYAMLValue converts yaml.v3 decoded values to JSON-compatible
// forms (non-string map keys become strings via the JSON bridge).
func normalizeYAMLValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = normalizeYAMLValue(value)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[fmt.Sprintf("%v", key)] = normalizeYAMLValue(value)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, normalizeYAMLValue(item))
		}
		return out
	default:
		return v
	}
}

// MarshalJSONKeysYAML serializes a value to YAML using the struct JSON tags
// as the key names (through the same bridge as decoding). Used where the TS
// wrote YAML files from schema structs (e.g. concepts.yml fixtures) so the
// on-disk key names stay identical to the TypeScript writer's.
func MarshalJSONKeysYAML(v any) ([]byte, error) {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(jsonBytes))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	// json.Number values must become native numbers before YAML marshal,
	// otherwise yaml.v3 renders them as quoted strings.
	return yaml.Marshal(denumberJSONValues(generic))
}

// denumberJSONValues converts json.Number values into int64/float64 so YAML
// marshals them as numeric literals.
func denumberJSONValues(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = denumberJSONValues(value)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, denumberJSONValues(item))
		}
		return out
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return i
		}
		if f, err := typed.Float64(); err == nil {
			return f
		}
		return typed.String()
	default:
		return v
	}
}

// isJSONish reports whether data looks like JSON (starts with { or [).
func isJSONish(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// decodeJSONStrict decodes JSON rejecting unknown fields.
func decodeJSONStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	return nil
}
