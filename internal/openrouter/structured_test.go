package openrouter_test

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Port of packages/openrouter/test/structured.test.ts — structured-output
// enforcement (P2-T04): native response_format building, constrained-prompting
// extraction, strict validation, typed StructuredOutputError with attempt +
// issues.

func TestBuildNativeResponseFormatWrapsCallerSchemaVerbatim(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"answer": map[string]any{"type": "string"}},
		"required":   []any{"answer"},
	}
	named := openrouter.BuildNativeResponseFormat(schema, "contract")
	assert.Equal(t, "json_schema", named.Type)
	assert.Equal(t, "contract", named.JSONSchema.Name)
	assert.True(t, named.JSONSchema.Strict)
	assert.Equal(t, schema, named.JSONSchema.Schema)

	defaulted := openrouter.BuildNativeResponseFormat(schema, "")
	assert.Equal(t, "response", defaulted.JSONSchema.Name)
}

func TestExtractJsonCandidateDirectFencedAndEmbedded(t *testing.T) {
	direct := openrouter.ExtractJsonCandidate(`{"a": 1}`)
	require.True(t, direct.OK)
	assert.Equal(t, map[string]any{"a": float64(1)}, direct.Value)

	fenced := openrouter.ExtractJsonCandidate("```json\n{\"a\": 1}\n```")
	require.True(t, fenced.OK)
	assert.Equal(t, map[string]any{"a": float64(1)}, fenced.Value)

	fencedBare := openrouter.ExtractJsonCandidate("``` \n{\"a\": 1}\n```")
	require.True(t, fencedBare.OK)
	assert.Equal(t, map[string]any{"a": float64(1)}, fencedBare.Value)

	embedded := openrouter.ExtractJsonCandidate("Sure: {\"a\": {\"b\": [1,2]}} — done")
	require.True(t, embedded.OK)
	assert.Equal(t, map[string]any{"a": map[string]any{"b": []any{float64(1), float64(2)}}}, embedded.Value)

	array := openrouter.ExtractJsonCandidate("[1,2,3]")
	require.True(t, array.OK)
	assert.Equal(t, []any{float64(1), float64(2), float64(3)}, array.Value)

	assert.False(t, openrouter.ExtractJsonCandidate("no json here").OK)
	assert.False(t, openrouter.ExtractJsonCandidate("").OK)
	empty := openrouter.ExtractJsonCandidate("")
	assert.Equal(t, []string{"response is empty — no JSON value found"}, empty.Issues)
	unparsable := openrouter.ExtractJsonCandidate("{not json")
	assert.Equal(t, []string{"response is not valid JSON: no parseable JSON value found in the model output"}, unparsable.Issues)
}

// orSimpleSchema is the Go port of the TS test's SIMPLE_SCHEMA
// (z.strictObject({ answer: z.string() })): a strict object with a required
// string `answer`, rendering zod-style issue strings.
func orSimpleSchema(value any) []string {
	object, ok := value.(map[string]any)
	if !ok {
		return []string{"(root): Expected object, received " + orJSTypeOf(value)}
	}
	var issues []string
	for key := range object {
		if key != "answer" {
			issues = append(issues, key+": Unrecognized key(s) in object: '"+key+"'")
		}
	}
	answer, present := object["answer"]
	if !present {
		issues = append(issues, "answer: Required")
	} else if _, ok := answer.(string); !ok {
		issues = append(issues, "answer: Expected string, received "+orJSTypeOf(answer))
	}
	return issues
}

// orJSTypeOf renders the JS typeof of a decoded JSON value.
func orJSTypeOf(value any) string {
	switch value.(type) {
	case nil:
		return "undefined"
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	case string:
		return "string"
	case []any:
		return "object"
	case map[string]any:
		return "object"
	default:
		return "object"
	}
}

func TestValidateStructuredOutputThrowsTypedErrorWithAttemptAndIssues(t *testing.T) {
	value, err := openrouter.ValidateStructuredOutput(openrouter.ValidateStructuredOutputOptions{
		Text:      `{"answer":"ok"}`,
		Attempt:   1,
		ZodSchema: orSimpleSchema,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"answer": "ok"}, value)

	_, err = openrouter.ValidateStructuredOutput(openrouter.ValidateStructuredOutputOptions{
		Text:       `{"answer": 42}`,
		Attempt:    3,
		SchemaName: "contract",
		ZodSchema:  orSimpleSchema,
	})
	require.Error(t, err)
	var violation *openrouter.StructuredOutputError
	require.ErrorAs(t, err, &violation)
	assert.Equal(t, 3, violation.Attempt)
	assert.NotEmpty(t, violation.Issues)
	assert.Contains(t, joinIssues(violation.Issues), "answer")
	assert.Contains(t, violation.Message, "contract")
	// The message never embeds the raw text.
	assert.NotContains(t, violation.Message, `{"answer": 42}`)
}

// joinIssues renders issues like the TS `issues.join('; ')`.
func joinIssues(issues []string) string {
	out := ""
	for i, issue := range issues {
		if i > 0 {
			out += "; "
		}
		out += issue
	}
	return out
}

func TestValidateStructuredJsonSchemaOnlyModeChecksObjectShape(t *testing.T) {
	jsonSchema := map[string]any{"type": "object"}
	value, err := openrouter.ValidateStructuredOutput(openrouter.ValidateStructuredOutputOptions{
		Text:       `{"any": true}`,
		Attempt:    1,
		JSONSchema: jsonSchema,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"any": true}, value)

	_, err = openrouter.ValidateStructuredOutput(openrouter.ValidateStructuredOutputOptions{
		Text:       `[1,2,3]`,
		Attempt:    1,
		JSONSchema: jsonSchema,
	})
	require.Error(t, err)
	var violation *openrouter.StructuredOutputError
	require.ErrorAs(t, err, &violation)
	assert.Equal(t, []string{"native structured output must be a JSON object"}, violation.Issues)

	_, err = openrouter.ValidateStructuredOutput(openrouter.ValidateStructuredOutputOptions{
		Text:       `not json`,
		Attempt:    1,
		JSONSchema: jsonSchema,
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &violation)
}

func TestValidateStructuredNoSchemaModePassesThrough(t *testing.T) {
	// Neither schema: any text is "valid" with an undefined structured value
	// (TS validateStructured returns { ok: true, value: undefined }).
	result := openrouter.ValidateStructured(openrouter.ValidateStructuredOptions{Text: "plain text"})
	assert.True(t, result.OK)
	assert.Nil(t, result.Value)
}

func TestConstrainedPromptInstructionConstant(t *testing.T) {
	assert.Equal(t,
		"Return ONLY a single JSON value that strictly conforms to the stated schema contract. "+
			"No prose, no markdown code fences, no trailing text.",
		openrouter.ConstrainedPromptInstruction,
	)
}

/* ------------------------------------- internal/schemas bridge (Go seam) */

func TestParseStrictSchemaValidatesThroughInternalSchemas(t *testing.T) {
	// The Go form of the TS caller-supplied zod schema: an internal/schemas
	// strict parser wrapped into a StructuredSchema.
	schema := openrouter.ParseStrictSchema(func(data []byte) (any, error) {
		return schemas.ParseJudgeVerdictBody(data)
	})
	require.NotNil(t, schema)

	// The registered example passes.
	valid := orJSONRoundTrip(t, schemas.JudgeVerdictExample)
	assert.Empty(t, schema(valid))

	// A violating body yields zod-style issue strings (path: message).
	broken := orJSONRoundTrip(t, map[string]any{
		"verdict": "not-a-verdict",
	})
	issues := schema(broken)
	assert.NotEmpty(t, issues)
	for _, issue := range issues {
		assert.NotEmpty(t, issue)
	}
	// A body with an extra key fails strict parsing (schema-agnostic guard).
	extra := orJSONRoundTrip(t, map[string]any{"verdict": "pass", "abstain": true})
	assert.NotEmpty(t, schema(extra))
}

func TestIssuesFromValidationErrorRendersZodStyle(t *testing.T) {
	_, err := schemas.ParseJudgeVerdictBody([]byte(`{`))
	require.Error(t, err)
	issues := openrouter.IssuesFromValidationError(err)
	require.NotEmpty(t, issues)
	for _, issue := range issues {
		assert.NotEmpty(t, issue)
	}
	// A non-validation error still renders as a root issue.
	plain := openrouter.IssuesFromValidationError(assert.AnError)
	require.Len(t, plain, 1)
	assert.Contains(t, plain[0], "(root): ")
}

// orJSONRoundTrip marshals a value and decodes it back into generic JSON form
// (what the transport doubles and ExtractJsonCandidate produce).
func orJSONRoundTrip(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded any
	require.NoError(t, json.Unmarshal(data, &decoded))
	return decoded
}

func TestValidateStructuredZodSchemaWithUnparsableTextPropagatesExtractionIssues(t *testing.T) {
	result := openrouter.ValidateStructured(openrouter.ValidateStructuredOptions{
		Text:      "no json at all",
		ZodSchema: orSimpleSchema,
	})
	assert.False(t, result.OK)
	assert.Equal(t,
		[]string{"response is not valid JSON: no parseable JSON value found in the model output"},
		result.Issues,
	)

	_, err := openrouter.ValidateStructuredOutput(openrouter.ValidateStructuredOutputOptions{
		Text:      "",
		Attempt:   2,
		ZodSchema: orSimpleSchema,
	})
	require.Error(t, err)
	var violation *openrouter.StructuredOutputError
	require.ErrorAs(t, err, &violation)
	assert.Equal(t, []string{"response is empty — no JSON value found"}, violation.Issues)
}

func TestParseStrictSchemaReportsUncanonicalizableValues(t *testing.T) {
	schema := openrouter.ParseStrictSchema(func(data []byte) (any, error) {
		return schemas.ParseJudgeVerdictBody(data)
	})
	// A value CanonicalJSON cannot render (a non-JSON Go struct) fails closed
	// with a root issue — the gateway never guesses how to validate it.
	issues := schema(struct{ Verdict string }{Verdict: "pass"})
	require.Len(t, issues, 1)
	assert.Contains(t, issues[0], "(root): ")
	assert.Contains(t, issues[0], "canonicalJson")
}
