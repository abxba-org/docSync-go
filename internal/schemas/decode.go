// Package schemas is the single schema source of truth for docSync artifacts,
// ported from packages/schemas (zod) to Go structs plus strict parsers.
//
// Mapping rules (from zod to Go):
//   - z.strictObject → unknown JSON keys are REJECTED (fail closed).
//   - z.string().min(n) → required, length >= n.
//   - .optional() → pointer field + omitempty; absent and explicit null both
//     accepted only where the TS schema used .optional() (zod optional
//     accepts undefined; explicit null fails in zod, but we accept both when
//     the field is a pointer, and callers that need the distinction check).
//   - .default(v) → applied when absent.
//   - z.enum([...]) → named string type + ParseXXX validation.
//   - .superRefine cross-field checks → Validate() methods with path-carrying
//     errors, matching the TS issue paths.
//
// All artifacts serialize with snake_case JSON keys, preserving on-disk
// compatibility with the TypeScript implementation's state files.
package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Issue is a validation problem with a path, mirroring zod issues.
type Issue struct {
	Path    []string // e.g. ["docs", "2", "path"]
	Message string
}

func (i Issue) Error() string {
	if len(i.Path) == 0 {
		return i.Message
	}
	return fmt.Sprintf("%s: %s", strings.Join(i.Path, "."), i.Message)
}

// ValidationError collects issues (like ZodError).
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, iss := range e.Issues {
		parts = append(parts, iss.Error())
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// NewValidationError builds a ValidationError from issues.
func NewValidationError(issues ...Issue) *ValidationError {
	return &ValidationError{Issues: issues}
}

// StrictDecode decodes JSON into dst, rejecting unknown keys (zod
// strictObject semantics) and decoding numbers as json.Number so integer
// fields round-trip without float coercion artifacts.
func StrictDecode(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return &ValidationError{Issues: []Issue{{Message: err.Error()}}}
	}
	// Trailing content after the first JSON value is invalid.
	if _, err := dec.Token(); err != io.EOF {
		return &ValidationError{Issues: []Issue{{Message: "unexpected trailing content after JSON value"}}}
	}
	return nil
}

// DecodeLenient decodes JSON allowing unknown keys (zod .strip() objects).
func DecodeLenient(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(dst)
}

// RequiredString validates a required string field with a minimum length.
func RequiredString(v string, minLen int, path []string) *Issue {
	if len(v) < minLen {
		msg := "Required"
		if minLen == 1 {
			msg = fmt.Sprintf("String must contain at least %d character(s)", minLen)
		} else {
			msg = fmt.Sprintf("String must contain at least %d character(s)", minLen)
		}
		return &Issue{Path: path, Message: msg}
	}
	return nil
}

// CheckEnum validates membership in an enum's allowed values.
func CheckEnum(v string, allowed []string, path []string) *Issue {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return &Issue{Path: path, Message: fmt.Sprintf("Invalid enum value. Expected %s, received '%s'", strings.Join(quoteAll(allowed), " | "), v)}
}

func quoteAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = "'" + v + "'"
	}
	return out
}

// Collect appends non-nil issues.
func Collect(issues *[]Issue, candidates ...*Issue) {
	for _, c := range candidates {
		if c != nil {
			*issues = append(*issues, *c)
		}
	}
}
