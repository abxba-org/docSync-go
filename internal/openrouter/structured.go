package openrouter

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Structured-output enforcement (P2-T04; ARCH §10.4): decision and verdict
// objects are schema-validated at the gateway boundary.
//
//   - mode 'native' — models with native structured-output support get a
//     `response_format` JSON-schema constraint built from a CALLER-SUPPLIED
//     `jsonSchema` (the gateway stays schema-agnostic: it never interprets the
//     schema, only constrains generation with it).
//   - mode 'prompt' — everything else gets constrained prompting
//     (ConstrainedPromptInstruction) plus STRICT validation of the
//     caller-supplied schema (the Go StructuredSchema, ported from the TS
//     zodSchema).
//
// Violations are recorded as issue lists (bounded corrective-retry input data);
// ValidateStructuredOutput is the throwing convenience variant. A violating
// output is retried bounded, then abstained — never hand-patched into
// compliance (§3.4).

// NativeResponseFormat is the OpenRouter `response_format` payload for native
// structured outputs. The `schema` is the caller-supplied JSON Schema, passed
// through verbatim (TS NativeResponseFormat).
type NativeResponseFormat struct {
	Type       string           `json:"type"`
	JSONSchema NativeJSONSchema `json:"json_schema"`
}

// NativeJSONSchema is the inner json_schema object (strict, named).
type NativeJSONSchema struct {
	Name   string `json:"name"`
	Strict bool   `json:"strict"`
	Schema any    `json:"schema"`
}

// BuildNativeResponseFormat builds the native `response_format` body field from
// a caller-supplied JSON Schema (TS buildNativeResponseFormat).
func BuildNativeResponseFormat(jsonSchema any, schemaName string) NativeResponseFormat {
	// TS `schemaName ?? 'response'`: Go cannot distinguish "" from absent, so an
	// empty schemaName falls back to 'response' (never observable in practice —
	// callers pass real schema names).
	name := schemaName
	if name == "" {
		name = "response"
	}
	return NativeResponseFormat{
		Type: "json_schema",
		JSONSchema: NativeJSONSchema{
			Name:   name,
			Strict: true,
			Schema: jsonSchema,
		},
	}
}

// ConstrainedPromptInstruction is the fixed constrained-prompting instruction
// (prompt mode): deterministic text appended to the task so the model knows the
// output contract exists even without a native `response_format` constraint.
const ConstrainedPromptInstruction = "Return ONLY a single JSON value that strictly conforms to the stated schema contract. " +
	"No prose, no markdown code fences, no trailing text."

// StructuredParseResult is the outcome of a structured parse/validation
// (TS StructuredParseResult). OK false carries the zod-style Issues.
type StructuredParseResult struct {
	// OK is true when the text parsed (and validated).
	OK bool
	// Value is the parsed JSON value (nil when no schema mode was active).
	Value any
	// Issues are the violation issue strings (only when !OK).
	Issues []string
}

// StructuredSchema validates a decoded JSON value against the caller-supplied
// schema — the Go form of the TS `zodSchema: z.ZodTypeAny`. It returns the
// zod-style issue strings (`path: message`, `(root)` for the root path); a nil
// or empty list means the value is valid.
//
// The gateway stays schema-agnostic: callers build their own validators
// (closures over internal/schemas types are the common case — see
// ParseStrictSchema).
type StructuredSchema func(value any) []string

// fencedJSONRe is the Go port of /```(?:json)?\s*([\s\S]*?)```/.
var fencedJSONRe = regexp.MustCompile("```(?:json)?\\s*([\\s\\S]*?)```")

// ExtractJsonCandidate extracts a JSON value from model text: direct parse,
// fenced block, or the outermost brace/bracket span (TS extractJsonCandidate).
func ExtractJsonCandidate(text string) StructuredParseResult {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return StructuredParseResult{
			OK:     false,
			Issues: []string{"response is empty — no JSON value found"},
		}
	}
	if direct, ok := tryParseJSON(trimmed); ok {
		return StructuredParseResult{OK: true, Value: direct}
	}
	if fenced := fencedJSONRe.FindStringSubmatch(trimmed); fenced != nil {
		if fromFence, ok := tryParseJSON(strings.TrimSpace(fenced[1])); ok {
			return StructuredParseResult{OK: true, Value: fromFence}
		}
	}
	if firstBrace, ok := firstOpenBraceIndex(trimmed); ok {
		if lastBrace, ok := lastCloseBraceIndex(trimmed); ok && firstBrace < lastBrace {
			if embedded, ok := tryParseJSON(trimmed[firstBrace : lastBrace+1]); ok {
				return StructuredParseResult{OK: true, Value: embedded}
			}
		}
	}
	return StructuredParseResult{
		OK:     false,
		Issues: []string{"response is not valid JSON: no parseable JSON value found in the model output"},
	}
}

// tryParseJSON parses one JSON value (TS tryParse; ok false == undefined).
func tryParseJSON(text string) (any, bool) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, false
	}
	return value, true
}

// firstOpenBraceIndex is Math.min of the present '{'/'[' indexes
// (TS ...filter(index => index >= 0); ok false == none present).
func firstOpenBraceIndex(text string) (int, bool) {
	first := -1
	for _, marker := range []string{"{", "["} {
		index := strings.Index(text, marker)
		if index < 0 {
			continue
		}
		if first == -1 || index < first {
			first = index
		}
	}
	return first, first >= 0
}

// lastCloseBraceIndex is Math.max of the present '}'/']' indexes.
func lastCloseBraceIndex(text string) (int, bool) {
	last := -1
	for _, marker := range []string{"}", "]"} {
		index := strings.LastIndex(text, marker)
		if index < 0 {
			continue
		}
		if index > last {
			last = index
		}
	}
	return last, last >= 0
}

// ValidateStructuredOptions carries one response text plus the caller-supplied
// schemas (TS validateStructured options).
type ValidateStructuredOptions struct {
	// Text is the raw model output.
	Text string
	// ZodSchema is the strict caller schema (prompt mode / trust-but-verify on
	// native); it takes precedence.
	ZodSchema StructuredSchema
	// JSONSchema selects JSON-parseability + object-shape validation only (the
	// gateway never interprets the JSON Schema itself — native enforcement is
	// the provider's response_format contract).
	JSONSchema any
}

// ValidateStructured strictly validates one response text against the
// caller-supplied schemas (TS validateStructured).
func ValidateStructured(options ValidateStructuredOptions) StructuredParseResult {
	if options.ZodSchema != nil {
		extracted := ExtractJsonCandidate(options.Text)
		if !extracted.OK {
			return extracted
		}
		if issues := options.ZodSchema(extracted.Value); len(issues) > 0 {
			return StructuredParseResult{OK: false, Issues: issues}
		}
		return StructuredParseResult{OK: true, Value: extracted.Value}
	}
	if options.JSONSchema != nil {
		extracted := ExtractJsonCandidate(options.Text)
		if !extracted.OK {
			return extracted
		}
		if !isJSONObject(extracted.Value) {
			return StructuredParseResult{
				OK:     false,
				Issues: []string{"native structured output must be a JSON object"},
			}
		}
		return StructuredParseResult{OK: true, Value: extracted.Value}
	}
	return StructuredParseResult{OK: true, Value: nil}
}

// isJSONObject is the TS check `value !== null && typeof value === 'object' &&
// !Array.isArray(value)` over decoded JSON.
func isJSONObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// ValidateStructuredOutputOptions is the input of ValidateStructuredOutput
// (TS validateStructuredOutput options).
type ValidateStructuredOutputOptions struct {
	Text       string
	Attempt    int
	SchemaName string
	ZodSchema  StructuredSchema
	JSONSchema any
}

// ValidateStructuredOutput is the throwing convenience around
// ValidateStructured — for callers that want exception-style enforcement
// outside the client's bounded-retry loop (TS validateStructuredOutput).
func ValidateStructuredOutput(options ValidateStructuredOutputOptions) (any, error) {
	result := ValidateStructured(ValidateStructuredOptions{
		Text:       options.Text,
		ZodSchema:  options.ZodSchema,
		JSONSchema: options.JSONSchema,
	})
	if result.OK {
		return result.Value, nil
	}
	return nil, NewStructuredOutputError(schemaNameOrDefault(options.SchemaName), options.Attempt, result.Issues)
}

// schemaNameOrDefault is `schemaName ?? 'response'` (see the BuildNativeResponseFormat
// note about "" vs absent).
func schemaNameOrDefault(schemaName string) string {
	if schemaName == "" {
		return "response"
	}
	return schemaName
}

/* ------------------------------------------------- schemas bridge (Go seam) */

// ParseStrictSchema adapts a strict internal/schemas parser (e.g.
// schemas.ParseJudgeVerdictBody, schemas.ParseDocPatch) into a StructuredSchema:
// the decoded value is canonicalized and re-parsed, and parse failures become
// zod-style issue strings. This is the Go replacement for the TS caller-supplied
// zod schemas — the schema source of truth stays internal/schemas.
func ParseStrictSchema(parse func(data []byte) (any, error)) StructuredSchema {
	return func(value any) []string {
		canonical, err := docutil.CanonicalJSON(value)
		if err != nil {
			return []string{"(root): " + err.Error()}
		}
		if _, err := parse([]byte(canonical)); err != nil {
			return IssuesFromValidationError(err)
		}
		return nil
	}
}

// IssuesFromValidationError converts an internal/schemas parse/validation error
// into the zod-style issue list ("path: message"; "(root): …" for root issues),
// matching the TS zodIssues rendering.
func IssuesFromValidationError(err error) []string {
	var validationErr *schemas.ValidationError
	if errors.As(err, &validationErr) {
		issues := make([]string, 0, len(validationErr.Issues))
		for _, issue := range validationErr.Issues {
			path := "(root)"
			if len(issue.Path) > 0 {
				path = strings.Join(issue.Path, ".")
			}
			issues = append(issues, path+": "+issue.Message)
		}
		return issues
	}
	return []string{"(root): " + err.Error()}
}
