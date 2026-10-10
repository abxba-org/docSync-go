package schemas

import (
	"bytes"
	"encoding/json"

	"gopkg.in/yaml.v3"
)

// decodeYAMLOrJSON decodes data into dst. YAML is a superset of JSON, so YAML
// parsing handles both; strictness (unknown keys) is enforced by each type's
// Validate() and by callers that need strict-length checks, since YAML has no
// DisallowUnknownFields. Unknown keys are checked via a key-set probe in
// decodeStrictYAMLOrJSON.
func decodeYAMLOrJSON(data []byte, dst any) error {
	if err := yaml.Unmarshal(data, dst); err != nil {
		return &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	return nil
}

// decodeStrictYAMLOrJSON decodes and rejects keys not present in the allowed
// set (zod strictObject semantics for YAML inputs).
func decodeStrictYAMLOrJSON(data []byte, dst any, allowedKeys []string) error {
	var probe map[string]any
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	if probe != nil {
		allowed := make(map[string]bool, len(allowedKeys))
		for _, k := range allowedKeys {
			allowed[k] = true
		}
		var issues []Issue
		for k := range probe {
			if !allowed[k] {
				issues = append(issues, Issue{Path: []string{k}, Message: "Unrecognized key"})
			}
		}
		if len(issues) > 0 {
			return NewValidationError(issues...)
		}
	}
	if err := yaml.Unmarshal(data, dst); err != nil {
		return &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	return nil
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
	return dec.Decode(dst)
}
