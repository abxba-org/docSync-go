package openrouter

// openrouter_internal_coverage_test.go — WHITE-BOX (package openrouter) coverage
// tests for the internal helpers of this package that have no exported entry
// point: the client's numeric-shape reader and redactor, the models-config
// source-order reader, the JSON-Schema `required` projection, the prompt/JSON
// marshalers, the replay-key identity function, and the defensive (fail-closed)
// arms of the writer-result mapping.
//
// Every case here is deterministic and network-free (OD-2): the doubles are the
// package's own recorded transports and plain Go values. Tests that can be
// expressed through the PUBLIC surface live in openrouter_coverage_test.go
// (package openrouter_test); only the unexported branches live here.

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// TestCOV_OR_101_realSleep_awaits_the_bounded_default_budget pins the default
// Sleep seam (TS setTimeout): the client installs realSleep when a test does not
// inject one, so the bounded retry backoff is a real (bounded) wait.
func TestCOV_OR_101_realSleep_awaits_the_bounded_default_budget(t *testing.T) {
	start := time.Now()
	realSleep(5)
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed.Milliseconds(), int64(4))
}

// TestCOV_OR_102_asNumber_reads_every_transport_numeric_shape pins the numeric
// reader the telemetry builder runs over `usage`: float64 (JSON), Go ints and
// json.Number (strict decoding) are numbers; NaN/Inf and unparsable values are
// NOT (fail closed — no fabricated token counts).
func TestCOV_OR_102_asNumber_reads_every_transport_numeric_shape(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  float64
		ok    bool
	}{
		{"json float64", 3.5, 3.5, true},
		{"go int", 7, 7, true},
		{"go int64", int64(9), 9, true},
		{"json.Number", json.Number("11.25"), 11.25, true},
		{"NaN is not a number", math.NaN(), 0, false},
		{"+Inf is not a number", math.Inf(1), 0, false},
		{"-Inf is not a number", math.Inf(-1), 0, false},
		{"unparsable json.Number", json.Number("not-a-number"), 0, false},
		{"NaN json.Number", json.Number("NaN"), 0, false},
		{"string is not a number", "3", 0, false},
		{"nil is not a number", nil, 0, false},
		{"bool is not a number", true, 0, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := asNumber(testCase.value)
			assert.Equal(t, testCase.ok, ok)
			if testCase.ok {
				assert.InDelta(t, testCase.want, got, 1e-12)
			} else {
				assert.Zero(t, got)
			}
		})
	}
}

// TestCOV_OR_103_orderedRoleKeys_reads_source_order pins the divergence-order
// reader: role keys come straight from the raw JSON token stream (Go maps carry
// no insertion order), and every parse hiccup fails closed to nil (validation
// has already rejected the document at that point).
func TestCOV_OR_103_orderedRoleKeys_reads_source_order(t *testing.T) {
	document := []byte(`{"schema":"docsync.models/v1","version":1,"roles":{` +
		`"decision-engine":{"model":"z-ai/glm-4.7"},"judge":{"model":"openai/gpt-4.1"},` +
		`"writer":{"model":"anthropic/claude-sonnet-4.5"}}}`)
	assert.Equal(t, []string{"decision-engine", "judge", "writer"}, orderedRoleKeys(document))

	// A `roles` value that is not an object (null / string / array) has no
	// source order — nil, never a fabricated key list.
	for _, raw := range []string{`{"roles":null}`, `{"roles":"nope"}`, `{"roles":[]}`} {
		assert.Nil(t, orderedRoleKeys([]byte(raw)), raw)
	}
	// No `roles` key at all.
	assert.Nil(t, orderedRoleKeys([]byte(`{"schema":"docsync.models/v1","version":1}`)))
}

// TestCOV_OR_104_orderedRoleKeys_fails_closed_on_malformed_documents pins the
// defensive nil returns of the token-stream reader.
func TestCOV_OR_104_orderedRoleKeys_fails_closed_on_malformed_documents(t *testing.T) {
	for name, raw := range map[string]string{
		"not json at all":       "not json",
		"not a JSON object":     "[]",
		"truncated roles value": `{"roles": {"a"`,
		"truncated key":         `{"a"`,
		"truncated other value": `{"schema":"x","ver`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, orderedRoleKeys([]byte(raw)))
		})
	}
}

// TestCOV_OR_105_schemaStringSlice_reads_both_required_shapes pins the JSON
// Schema `required` reader: the registry exporter emits []string, a decoded
// export arrives as []any; anything else has no required list at all.
func TestCOV_OR_105_schemaStringSlice_reads_both_required_shapes(t *testing.T) {
	strings := []string{"change_kind", "abstain"}
	assert.Equal(t, strings, schemaStringSlice(strings))

	decoded := []any{"change_kind", 7, nil, "abstain"}
	assert.Equal(t, []string{"change_kind", "abstain"}, schemaStringSlice(decoded))

	assert.Nil(t, schemaStringSlice(nil))
	assert.Nil(t, schemaStringSlice("change_kind"))
	// An empty (or all-non-string) decoded list carries no required fields.
	assert.Empty(t, schemaStringSlice([]any{}))
	assert.Empty(t, schemaStringSlice([]any{7, nil}))
}

// TestCOV_OR_106_cachePrefixOrTrue_defaults_the_deterministic_prefix_on pins the
// TS `input.cachePrefix ?? true` default: absent (nil) means the deterministic
// system prefix IS cache-hinted; an explicit false is passed through.
func TestCOV_OR_106_cachePrefixOrTrue_defaults_the_deterministic_prefix_on(t *testing.T) {
	require.NotNil(t, cachePrefixOrTrue(nil))
	assert.True(t, *cachePrefixOrTrue(nil))

	off := false
	got := cachePrefixOrTrue(&off)
	require.NotNil(t, got)
	assert.False(t, *got)

	on := true
	assert.True(t, *cachePrefixOrTrue(&on))
}

// TestCOV_OR_107_redactAttempts_scrubs_key_material_from_every_attempt_field
// pins the invariant-8 defense in depth: the walk-exhaustion failure must never
// carry API-key material — not in the model target, not in the error text, and
// not in the provider-order pins.
func TestCOV_OR_107_redactAttempts_scrubs_key_material_from_every_attempt_field(t *testing.T) {
	key := "sk-or-v1-test-key-abc123def456"
	order := []string{"z-ai", "openai" + key}
	attempts := []ModelAttempt{
		{Target: key + "-model", ProviderOrder: order, Attempt: 1, Error: "boom " + key},
		{Target: "clean-model", Attempt: 2},
	}

	redacted := redactAttempts(attempts, key)
	require.Len(t, redacted, 2)
	assert.NotContains(t, redacted[0].Target, key)
	assert.Contains(t, redacted[0].Target, REDACTED)
	assert.NotContains(t, redacted[0].Error, key)
	assert.Contains(t, redacted[0].Error, REDACTED)
	require.Len(t, redacted[0].ProviderOrder, 2)
	assert.Equal(t, "z-ai", redacted[0].ProviderOrder[0])
	assert.Equal(t, "openai"+REDACTED, redacted[0].ProviderOrder[1])
	// The input records are never mutated (the failure leaves the gateway a
	// redacted COPY).
	assert.Contains(t, attempts[0].Error, key)
	// An attempt without a provider pin stays nil.
	assert.Nil(t, redacted[1].ProviderOrder)

	// An empty key has nothing to scrub — the records return unchanged.
	same := redactAttempts(attempts, "")
	assert.Equal(t, attempts, same)
}

// TestCOV_OR_108_decodeTelemetry_fails_closed_on_unrenderable_records pins the
// defensive arm of the telemetry decoder: a record the encoder cannot render
// degrades to the zero RoleTelemetry instead of panicking.
func TestCOV_OR_108_decodeTelemetry_fails_closed_on_unrenderable_records(t *testing.T) {
	assert.Equal(t, RoleTelemetry{}, decodeTelemetry(math.NaN()))

	telemetry := decodeTelemetry(map[string]any{
		"role":            "judge",
		"requested_model": "z-ai/glm-4.7",
		"routed_model":    "z-ai/glm-4.7-fallback",
		"latency_ms":      11,
		"attempt":         1,
		"fallback_used":   true,
	})
	assert.Equal(t, "judge", telemetry.Role)
	assert.Equal(t, "z-ai/glm-4.7-fallback", telemetry.RoutedModel)
	assert.Equal(t, 11, telemetry.LatencyMS)
	assert.True(t, telemetry.FallbackUsed)
}

// TestCOV_OR_109_jsonString_and_marshalSeamInput_report_serialization_failures
// pins the seam serializers' fail-closed fallbacks (the TS JSON.stringify
// throw becoming a visible, non-silent value).
func TestCOV_OR_109_jsonString_and_marshalSeamInput_report_serialization_failures(t *testing.T) {
	assert.Equal(t, `"docsync.decision"`, jsonString("docsync.decision"))
	assert.Equal(t, "2", jsonString(2))
	// A value JSON cannot represent renders through fmt instead of panicking.
	assert.Contains(t, jsonString(math.NaN()), "NaN")

	compact, err := marshalSeamInput(map[string]any{"a": "<b>"})
	require.NoError(t, err)
	assert.Equal(t, `{"a":"<b>"}`, string(compact))

	if _, err := marshalSeamInput(map[string]any{"bad": make(chan int)}); assert.Error(t, err) {
		assert.Contains(t, err.Error(), "unsupported type")
	}
}

// TestCOV_OR_110_formatSchemaIssues_renders_the_root_path pins the issue
// renderer's `(root)` arm: an issue with no path (the whole-document
// violations the strict decoders raise) renders as `(root): message`.
func TestCOV_OR_110_formatSchemaIssues_renders_the_root_path(t *testing.T) {
	rendered := formatSchemaIssues([]schemas.Issue{
		{Message: "unexpected trailing content after JSON value"},
		{Path: []string{"abstain", "reason"}, Message: "abstain_reason must name a reason"},
	})
	assert.Equal(t, "(root): unexpected trailing content after JSON value; abstain.reason: abstain_reason must name a reason", rendered)
	assert.Empty(t, formatSchemaIssues(nil))
}

// TestCOV_OR_111_prompt_marshalers_report_serialization_failures pins the
// prompt-byte renderers' error arms (JSON.stringify throwing is a programming
// error, never a silently degraded prompt).
func TestCOV_OR_111_prompt_marshalers_report_serialization_failures(t *testing.T) {
	questions := []CompiledQuestion{{Key: "impact", Type: "choice", Prompt: "impact?", Choices: []string{"none", "definite"}}}
	compact, err := marshalPromptJSONCompact(questions)
	require.NoError(t, err)
	assert.Contains(t, compact, `"key":"impact"`)
	assert.NotContains(t, compact, "\n")

	indented, err := marshalPromptJSONIndent(questions)
	require.NoError(t, err)
	assert.Contains(t, indented, "\n    \"key\": \"impact\"")

	if _, err := marshalPromptJSONCompact(make(chan int)); assert.Error(t, err) {
		assert.Contains(t, err.Error(), "unsupported type")
	}
	if _, err := marshalPromptJSONIndent(make(chan int)); assert.Error(t, err) {
		assert.Contains(t, err.Error(), "unsupported type")
	}
}

// TestCOV_OR_112_projectPatchBodyJsonSchema_projects_every_required_shape pins
// the native response_format projection over both `required` shapes and the
// empty-required case (the key is dropped, never emitted empty).
func TestCOV_OR_112_projectPatchBodyJsonSchema_projects_every_required_shape(t *testing.T) {
	identity := func() []string {
		return []string{"schema", "run_id", "base_content_hash", "provenance"}
	}

	exported := map[string]any{
		"type":       "object",
		"properties": map[string]any{"schema": map[string]any{}, "doc_path": map[string]any{}},
	}
	projected := projectPatchBodyJsonSchema(exported)
	properties, ok := projected["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "doc_path")
	assert.NotContains(t, properties, "schema")
	assert.NotContains(t, projected, "required")

	// A decoded export carries `required` as []any (JSON), not []string.
	decoded := map[string]any{
		"properties": map[string]any{"schema": map[string]any{}, "doc_path": map[string]any{}},
		"required":   []any{"schema", "doc_path", 7},
	}
	decodedProjected := projectPatchBodyJsonSchema(decoded)
	required, ok := decodedProjected["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"doc_path"}, required)

	// The registry exporter emits []string.
	strings := map[string]any{
		"properties": map[string]any{"run_id": map[string]any{}, "hunks": map[string]any{}},
		"required":   []string{"run_id", "hunks"},
	}
	stringProjected := projectPatchBodyJsonSchema(strings)
	required, ok = stringProjected["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"hunks"}, required)

	// Every required field is an adapter-owned identity field → the required
	// list becomes empty and the key is dropped entirely.
	allIdentity := map[string]any{
		"properties": map[string]any{"schema": map[string]any{}},
		"required":   append(identity(), "doc_path")[:len(identity())],
	}
	emptyRequired := projectPatchBodyJsonSchema(allIdentity)
	properties, ok = emptyRequired["properties"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, properties, "schema")
	assert.NotContains(t, emptyRequired, "required")
}

// TestCOV_OR_113_marshalJSONStringify_renders_like_JSONStringify pins the
// two-space, HTML-unescaped rendering (and its error arm).
func TestCOV_OR_113_marshalJSONStringify_renders_like_JSONStringify(t *testing.T) {
	rendered, err := marshalJSONStringify([]map[string]string{{"path": "<docs/a.md>"}})
	require.NoError(t, err)
	assert.Contains(t, rendered, "\"path\": \"<docs/a.md>\"")
	assert.False(t, len(rendered) == 0 || rendered[len(rendered)-1] == '\n')

	if _, err := marshalJSONStringify(make(chan int)); assert.Error(t, err) {
		assert.Contains(t, err.Error(), "unsupported type")
	}
}

// TestCOV_OR_114_copyHashes_never_aliases_or_fabricates pins the
// orchestrator-authoritative hash table copy: a nil map stays nil (nothing
// fabricated), and the copy is a deep-enough copy that mutating the request's
// map can never reach the caller's.
func TestCOV_OR_114_copyHashes_never_aliases_or_fabricates(t *testing.T) {
	assert.Nil(t, copyHashes(nil))
	source := map[string]string{"docs/payments.md": "abc"}
	copied := copyHashes(source)
	assert.Equal(t, source, copied)
	copied["docs/payments.md"] = "mutated"
	assert.Equal(t, "abc", source["docs/payments.md"])
}

// TestCOV_OR_115_defaultReplayKey_is_content_addressed pins the replay
// identity rule: identical input → identical key (never positional FIFO),
// namespaced by URL, with a visible fallback when a body is not canonicalizable.
func TestCOV_OR_115_defaultReplayKey_is_content_addressed(t *testing.T) {
	request := RecordedRequest{
		URL:     "https://openrouter.ai/api/v1/chat/completions",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    map[string]any{"model": "z-ai/glm-4.7", "temperature": float64(0)},
	}
	again := RecordedRequest{
		URL:     request.URL,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    map[string]any{"model": "z-ai/glm-4.7", "temperature": float64(0)},
	}
	first := defaultReplayKey(request)
	assert.Equal(t, first, defaultReplayKey(again), "identical input hashes to the same identity")
	assert.NotEqual(t, first, defaultReplayKey(RecordedRequest{
		URL:  "https://openrouter.ai/api/v1/other",
		Body: request.Body,
	}), "the identity is namespaced by URL")
	assert.NotEqual(t, first, defaultReplayKey(RecordedRequest{
		URL:  request.URL,
		Body: map[string]any{"model": "anthropic/claude-sonnet-4.5"},
	}), "a different body is a different identity")

	// A body the canonicalizer cannot render still yields a key (the recorded
	// double never fails silent on an un-keyable request) — the fallback, not
	// a panic.
	assert.NotEmpty(t, defaultReplayKey(RecordedRequest{
		URL:  request.URL,
		Body: map[string]any{"bad": make(chan int)},
	}))
}

// TestCOV_OR_116_mapWriterResult_rejects_unmapped_arms pins the exhaustive
// union mapping's typed internal errors: a finding arm without a finding and an
// unknown arm both fail closed instead of being silently swallowed.
func TestCOV_OR_116_mapWriterResult_rejects_unmapped_arms(t *testing.T) {
	_, err := mapWriterResult(PatchWriterWriteResult{Kind: PatchWriterResultFinding}, "docs/payments.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unreachable writer-port result arm: finding-without-patch carried no finding")
	assert.Equal(t, writerStageInternalCode, docutil.CodeOf(err))

	_, err = mapWriterResult(PatchWriterWriteResult{Kind: PatchWriterResultKind("quantum")}, "docs/payments.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unreachable writer-port result arm: quantum")

	// The mapped arms stay observable through the same function.
	patch, err := mapWriterResult(PatchWriterWriteResult{
		Kind:  PatchWriterResultPatch,
		Patch: &schemas.DocPatch{DocPath: "docs/payments.md"},
	}, "docs/payments.md")
	require.NoError(t, err)
	require.NotNil(t, patch.Patch)
	assert.Equal(t, "docs/payments.md", patch.Patch.DocPath)

	abstain, err := mapWriterResult(PatchWriterWriteResult{
		Kind:   PatchWriterResultAbstain,
		Reason: schemas.AbstainReasonSchemaViolation,
	}, "docs/payments.md")
	require.NoError(t, err)
	assert.Equal(t, WriterStageResultAbstain, abstain.Kind)
	assert.Equal(t, "patch-writer abstained (reason 'schema_violation')", abstain.Detail)
}
