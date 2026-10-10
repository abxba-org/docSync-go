package openrouter_test

// Ported from packages/openrouter/test/adapters/patch-writer.test.ts (P3-T01)
// — black-box (package openrouter_test) with testify, recorded client doubles
// keyed by request identity (no network, OD-2).
//
// The TS suite drove the REAL OpenRouterClient over recorded transports
// (createRecordingTransport + createReplayTransport + createFailingTransport).
// The client itself is being ported concurrently (client.go / transport.go /
// models.go / errors.go), so this Go suite drives the adapter over a recorded
// CLIENT double: it mirrors the consumed client seam
// (openrouter.PatchWriterClientPort — see the TODO(client.go) marker in
// adapter-patch-writer.go), records every exchange (role + the outgoing
// CompletionRequest, appended BEFORE the scripted outcome applies) and
// replays scripted completions/failures in order. Boundary contracts the real
// client owns (the API-key guard before I/O; the bounded corrective-retry
// walk) are mirrored by the double where a ported test observes them.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* --------------------------------------------------------------- doubles */

// recordedExchange is one recorded client call (request identity = the role +
// the outgoing completion request), appended after the double's boundary
// guard and BEFORE the scripted outcome applies.
type recordedExchange struct {
	Role    string
	Request openrouter.PatchWriterCompletionRequest
}

// scriptedOutcome is one replayed client result: either a completion or a
// typed failure. When Block is non-nil the call parks until it is closed (the
// hanging-transport case).
type scriptedOutcome struct {
	Completion openrouter.PatchWriterRoleCompletion
	Err        error
	Block      chan struct{}
}

// fakeClient is the recorded client double (TS: testClient over
// createRecordingTransport/createReplayTransport/createFailingTransport).
type fakeClient struct {
	binding    openrouter.PatchWriterRoleBinding
	bindingErr error
	// guardAPIKey mirrors the real client's pre-I/O payload guard: when set,
	// any outgoing payload carrying the key fails BEFORE the exchange is
	// recorded (zero exchanges recorded).
	guardAPIKey string

	mu        sync.Mutex
	script    []scriptedOutcome
	exchanges []recordedExchange
}

func (c *fakeClient) BindingFor(role string) (openrouter.PatchWriterRoleBinding, error) {
	if c.bindingErr != nil {
		return openrouter.PatchWriterRoleBinding{}, c.bindingErr
	}
	return c.binding, nil
}

func (c *fakeClient) Complete(role string, request openrouter.PatchWriterCompletionRequest) (openrouter.PatchWriterRoleCompletion, error) {
	if c.guardAPIKey != "" && payloadContains(request, c.guardAPIKey) {
		return openrouter.PatchWriterRoleCompletion{}, openrouter.NewPatchWriterGuardError(
			"api key material detected in the outgoing payload — request blocked before any I/O",
			"body")
	}
	c.mu.Lock()
	index := len(c.exchanges)
	c.exchanges = append(c.exchanges, recordedExchange{Role: role, Request: request})
	c.mu.Unlock()
	if index >= len(c.script) {
		return openrouter.PatchWriterRoleCompletion{}, fmt.Errorf(
			"replay exhausted: no scripted outcome for exchange %d", index+1)
	}
	outcome := c.script[index]
	if outcome.Block != nil {
		<-outcome.Block
	}
	return outcome.Completion, outcome.Err
}

func payloadContains(request openrouter.PatchWriterCompletionRequest, needle string) bool {
	if strings.Contains(request.System, needle) || strings.Contains(request.User, needle) {
		return true
	}
	for _, block := range request.UntrustedContent {
		if strings.Contains(block, needle) {
			return true
		}
	}
	return false
}

// exchangeCount is the recorded exchange count (mutex-guarded).
func (c *fakeClient) exchangeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.exchanges)
}

// exchange returns the 1-indexed recorded exchange.
func (c *fakeClient) exchange(t *testing.T, index int) recordedExchange {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Greater(t, len(c.exchanges), index-1, "recorded exchange %d missing", index)
	return c.exchanges[index-1]
}

// telemetryFor builds the telemetry the client derives for one routed model
// (TS: the client's actually-routed telemetry rule).
func telemetryFor(requestedModel, routedModel string, fallbackUsed bool) openrouter.PatchWriterRoleTelemetry {
	return openrouter.PatchWriterRoleTelemetry{
		Role:           "patch-writer",
		RequestedModel: requestedModel,
		RoutedModel:    routedModel,
		LatencyMS:      5,
		Attempt:        1,
		FallbackUsed:   fallbackUsed,
		PolicyVersion:  17,
	}
}

// completionOutcome scripts a successful structured completion.
func completionOutcome(routedModel string, structured any) scriptedOutcome {
	encoded, _ := json.Marshal(structured)
	return scriptedOutcome{Completion: openrouter.PatchWriterRoleCompletion{
		Kind:       "completion",
		Text:       string(encoded),
		Structured: structured,
		Telemetry:  telemetryFor("anthropic/claude-sonnet-4.5", routedModel, routedModel != "anthropic/claude-sonnet-4.5"),
	}}
}

// jsonCompletion mirrors the TS testing helper: a completion whose content is
// the JSON-stringified body.
func jsonCompletion(model string, value any) scriptedOutcome {
	return completionOutcome(model, value)
}

/* --------------------------------------------------------------- fixtures */

// testAPIKey is the distinct sentinel the guard detects (TS: TEST_API_KEY);
// never a real key.
const testAPIKey = "sk-or-v1-test-key-abc123def456"

// The model-proposed body: DOC_PATCH_EXAMPLE minus ALL adapter-owned identity
// fields (TS: patchBodyOf).
func patchBodyOf(patch schemas.DocPatch) map[string]any {
	encoded, err := json.Marshal(patch)
	if err != nil {
		panic(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		panic(err)
	}
	delete(body, "schema")
	delete(body, "run_id")
	delete(body, "base_content_hash")
	delete(body, "provenance")
	return body
}

// The anchor hash the adapter must DERIVE over the example's quoted span
// (OD-R42) and the empty-string hash (pure insertions).
var (
	derivedExampleSpanHash = docutil.ContentHash("Refunds require an idempotency key.")
	emptyStringHash        = docutil.ContentHash("")
)

const (
	bareHashA        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bareHashB        = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	wrongHostileHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	patchWriteClock  = "2026-10-06T00:00:00.000Z"
)

func pwIntPtr(v int) *int { return &v }

func strPtr(v string) *string { return &v }

// writeRequest builds the default adapter request (TS: writeRequest).
func writeRequest() openrouter.PatchWriterRequest {
	return openrouter.PatchWriterRequest{
		Workspace:                 "WORKSPACE RENDER\nsource_files + relevant_documents + manifest policy",
		PolicyVersion:             17,
		RunID:                     "run-123",
		DocTargets:                []openrouter.PatchWriterDocTarget{{Path: "docs/payments.md", Mode: "auto"}},
		Clock:                     func() string { return patchWriteClock },
		ExpectedBaseContentHashes: map[string]string{"docs/payments.md": schemas.DocPatchExample.BaseContentHash},
	}
}

// newAdapter wires the adapter over the recorded double (TS:
// createPatchWriterAdapter({ client: testClient(...) })).
func newAdapter(t *testing.T, client openrouter.PatchWriterClientPort, opts ...func(*openrouter.PatchWriterAdapterOptions)) openrouter.PatchWriterAdapter {
	t.Helper()
	options := openrouter.PatchWriterAdapterOptions{Client: client}
	for _, opt := range opts {
		opt(&options)
	}
	adapter, err := openrouter.CreatePatchWriterAdapter(options)
	require.NoError(t, err)
	return adapter
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var bareHexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

/* ------------------------------------------------------------------ tests */

// V-P3-9 groundwork: provenance names the ACTUALLY routed model (the FALLBACK
// the walk used — never the configured primary).
func TestPatchWriterAdapter_VP3_9_provenance_names_actually_routed_model(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("openai/gpt-4.1-mini", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)

	result, err := writer.Write(writeRequest())
	require.NoError(t, err)
	require.Equal(t, openrouter.PatchWriterResultPatch, result.Kind)
	require.NotNil(t, result.Patch)
	assert.Equal(t, "openai/gpt-4.1-mini", result.Patch.Provenance.WriterModel)
	assert.Equal(t, "openai/gpt-4.1-mini", result.Telemetry.RoutedModel)
	assert.Equal(t, "anthropic/claude-sonnet-4.5", result.Telemetry.RequestedModel)
	assert.True(t, result.Telemetry.FallbackUsed)
	// The assembled patch re-parses through the registered DOC_PATCH_SCHEMA.
	encoded, err := json.Marshal(result.Patch)
	require.NoError(t, err)
	_, err = schemas.ParseDocPatch(encoded)
	require.NoError(t, err)
}

// V-P3-8 groundwork: timeout → finding-without-patch, no partial patch
// (adapter budget race).
func TestPatchWriterAdapter_VP3_8_timeout_finding_adapter_budget_race(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	client := &fakeClient{script: []scriptedOutcome{{Block: release}}}
	writer := newAdapter(t, client, func(o *openrouter.PatchWriterAdapterOptions) {
		o.TimeoutMs = pwIntPtr(40)
	})

	result, err := writer.Write(writeRequest())
	require.NoError(t, err)
	require.Equal(t, openrouter.PatchWriterResultFinding, result.Kind)
	require.NotNil(t, result.Finding)
	assert.Equal(t, []string{"docs/payments.md"}, result.Finding.DocPaths)
	assert.Equal(t, openrouter.PatchWriterTimeoutReason, result.Finding.Reason)
	assert.Contains(t, result.Finding.Detail, "40ms")
	assert.Nil(t, result.Patch)
	// No telemetry rides a finding (V-P3-8: the write never completed).
	assert.Zero(t, result.Telemetry.RoutedModel)
}

// V-P3-8 groundwork: timeout → finding (timeout-classified walk — every
// attempt names explicit client timeout semantics with no HTTP status).
func TestPatchWriterAdapter_VP3_8_timeout_finding_timeout_classified_walk(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		{Err: openrouter.NewPatchWriterTotalInferenceFailure(
			"all model targets exhausted for role patch-writer", "patch-writer",
			[]openrouter.PatchWriterModelAttempt{
				{Target: "anthropic/claude-sonnet-4.5", Attempt: 1, Error: "request exceeded the 60000ms budget (timeout)", LatencyMS: 3},
				{Target: "openai/gpt-4.1-mini", Attempt: 1, Error: "request exceeded the 60000ms budget (timeout)", LatencyMS: 3},
			})},
	}}
	writer := newAdapter(t, client)

	result, err := writer.Write(writeRequest())
	require.NoError(t, err)
	require.Equal(t, openrouter.PatchWriterResultFinding, result.Kind)
	assert.Equal(t, []string{"docs/payments.md"}, result.Finding.DocPaths)
	assert.Equal(t, openrouter.PatchWriterTimeoutReason, result.Finding.Reason)
	assert.Nil(t, result.Patch)
}

// Adapter budget race provenance is the TAGGED error
// (PatchWriterBudgetTimeoutError), not a message sniff (D13 minor 3).
func TestPatchWriterAdapter_budget_timeout_error_is_tagged_provenance(t *testing.T) {
	tagged := &openrouter.PatchWriterBudgetTimeoutError{BudgetMS: 40}
	assert.Equal(t, "adapter_budget_timeout", tagged.Code())
	assert.Equal(t, 40, tagged.BudgetMS)
	assert.Contains(t, tagged.Error(), "40ms")
	assert.Contains(t, tagged.Error(), "no partial patch")
	var coded interface{ Code() string } = tagged
	assert.Equal(t, "adapter_budget_timeout", coded.Code())
}

// Explicit client timeout semantics classify as writer-timeout (timed out /
// ETIMEDOUT / socket hang up).
func TestPatchWriterAdapter_explicit_timeout_semantics_classify_as_writer_timeout(t *testing.T) {
	for _, message := range []string{
		"request timed out after 30000ms",
		"connect ETIMEDOUT 10.0.0.1:443",
		"socket hang up",
	} {
		t.Run(message, func(t *testing.T) {
			client := &fakeClient{script: []scriptedOutcome{
				{Err: openrouter.NewPatchWriterTransportError(message, nil)},
			}}
			writer := newAdapter(t, client)
			result, err := writer.Write(writeRequest())
			require.NoError(t, err)
			assert.Equal(t, openrouter.PatchWriterResultFinding, result.Kind)
			assert.Equal(t, openrouter.PatchWriterTimeoutReason, result.Finding.Reason)
			assert.Nil(t, result.Patch)
		})
	}
}

// A hostile generic 'abort' failure is NOT timeout provenance — rethrown typed
// (the caller owns D10).
func TestPatchWriterAdapter_hostile_abort_is_not_timeout_provenance(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		{Err: openrouter.NewPatchWriterTotalInferenceFailure(
			"all model targets exhausted for role patch-writer", "patch-writer",
			[]openrouter.PatchWriterModelAttempt{
				{Target: "anthropic/claude-sonnet-4.5", Attempt: 1, Error: "The operation was aborted", LatencyMS: 3},
			})},
	}}
	writer := newAdapter(t, client)

	result, err := writer.Write(writeRequest())
	require.Error(t, err)
	var failure *openrouter.PatchWriterTotalInferenceFailure
	require.True(t, errors.As(err, &failure))
	require.Len(t, failure.Attempts, 1)
	assert.Equal(t, "The operation was aborted", failure.Attempts[0].Error)
	assert.Equal(t, openrouter.PatchWriterResultKind(""), result.Kind)
}

// A walk mixing a timeout attempt with a non-timeout abort attempt keeps the
// typed throw (any non-timeout attempt wins).
func TestPatchWriterAdapter_mixed_timeout_walk_keeps_typed_throw(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		{Err: openrouter.NewPatchWriterTotalInferenceFailure(
			"all model targets exhausted for role patch-writer", "patch-writer",
			[]openrouter.PatchWriterModelAttempt{
				{Target: "anthropic/claude-sonnet-4.5", Attempt: 1, Error: "request exceeded the 60000ms budget (timeout)", LatencyMS: 3},
				{Target: "openai/gpt-4.1-mini", Attempt: 1, Error: "The operation was aborted", LatencyMS: 3},
			})},
	}}
	writer := newAdapter(t, client)
	_, err := writer.Write(writeRequest())
	require.Error(t, err)
	var failure *openrouter.PatchWriterTotalInferenceFailure
	assert.True(t, errors.As(err, &failure))
}

// Non-timeout total inference failure keeps its typed throw (the caller owns
// D10 degradation).
func TestPatchWriterAdapter_non_timeout_total_failure_keeps_typed_throw(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		{Err: openrouter.NewPatchWriterTotalInferenceFailure(
			"all model targets exhausted for role patch-writer", "patch-writer",
			[]openrouter.PatchWriterModelAttempt{
				{Target: "anthropic/claude-sonnet-4.5", Attempt: 1, Status: pwIntPtr(500), Error: "HTTP 500", LatencyMS: 3},
				{Target: "openai/gpt-4.1-mini", Attempt: 1, Status: pwIntPtr(500), Error: "HTTP 500", LatencyMS: 3},
			})},
	}}
	writer := newAdapter(t, client)
	_, err := writer.Write(writeRequest())
	require.Error(t, err)
	var failure *openrouter.PatchWriterTotalInferenceFailure
	assert.True(t, errors.As(err, &failure))
}

// Full identity fill: the assembled patch equals DOC_PATCH_EXAMPLE; run_id
// lands verbatim.
func TestPatchWriterAdapter_full_identity_fill_equals_example(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)
	request := writeRequest()
	request.RunID = schemas.DocPatchExample.RunID
	request.Clock = func() string { return schemas.DocPatchExample.Provenance.GeneratedAt }

	result, err := writer.Write(request)
	require.NoError(t, err)
	require.Equal(t, openrouter.PatchWriterResultPatch, result.Kind)
	require.NotNil(t, result.Patch)
	assert.Equal(t, schemas.DocPatchExample, *result.Patch)
	assert.Equal(t, schemas.DocPatchExample.RunID, result.Patch.RunID)
	assert.Equal(t, schemas.DocPatchExample.BaseContentHash, result.Patch.BaseContentHash)
	assert.Equal(t, 17, result.Patch.Provenance.PolicyVersion)
	assert.Equal(t, schemas.DocPatchExample.Provenance.GeneratedAt, result.Patch.Provenance.GeneratedAt)
	assert.Equal(t, 17, result.Telemetry.PolicyVersion)
}

// Missing base content hash for the proposed doc_path → typed assembly
// violation (fail closed).
func TestPatchWriterAdapter_missing_base_content_hash_fails_assembly(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)

	// Hash provided for a DIFFERENT path than the model proposed:
	request := writeRequest()
	request.ExpectedBaseContentHashes = map[string]string{"docs/other.md": "sha256:x"}
	_, err := writer.Write(request)
	require.Error(t, err)
	assert.Regexp(t, regexp.MustCompile(`base content hash`), err.Error())
	assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))

	// No expected hashes at all:
	request = writeRequest()
	request.ExpectedBaseContentHashes = nil
	_, err = writer.Write(request)
	require.Error(t, err)
	assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))
}

// OD-R41 native default: the outgoing request carries the PROJECTED body JSON
// schema (no identity fields).
func TestPatchWriterAdapter_OD_R41_native_default_projected_body_schema(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)

	result, err := writer.Write(writeRequest())
	require.NoError(t, err)
	require.Equal(t, openrouter.PatchWriterResultPatch, result.Kind)

	exchange := client.exchange(t, 1)
	assert.Equal(t, "patch-writer", exchange.Role)
	assert.Equal(t, openrouter.DocPatchBodySchemaName, exchange.Request.SchemaName)
	assert.Equal(t, float64(0), exchange.Request.TemperatureOverride)
	assert.True(t, exchange.Request.CachePrefix)
	assert.Equal(t, 17, exchange.Request.PolicyVersion)
	assert.True(t, exchange.Request.BodySchemaAttached)
	require.NotNil(t, exchange.Request.JSONSchema)

	properties, ok := exchange.Request.JSONSchema["properties"].(map[string]any)
	require.True(t, ok, "projected schema must carry properties")
	required, ok := exchange.Request.JSONSchema["required"].([]string)
	require.True(t, ok, "projected schema must carry a required list")
	for _, proposed := range []string{"doc_path", "hunks", "evidence_refs", "minimality"} {
		assert.Contains(t, properties, proposed)
		assert.True(t, containsString(required, proposed), "required must contain %s", proposed)
	}
	for _, identity := range []string{"schema", "run_id", "base_content_hash", "provenance"} {
		assert.NotContains(t, properties, identity)
		assert.False(t, containsString(required, identity), "required must not contain %s", identity)
	}
	assert.Equal(t, false, exchange.Request.JSONSchema["additionalProperties"])
}

// OD-R41 prompt downgrade (explicit binding param): no response_format,
// identical validation.
func TestPatchWriterAdapter_OD_R41_prompt_downgrade_no_response_format(t *testing.T) {
	client := &fakeClient{
		binding: openrouter.PatchWriterRoleBinding{StructuredOutput: "prompt"},
		script: []scriptedOutcome{
			jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
		},
	}
	writer := newAdapter(t, client)

	result, err := writer.Write(writeRequest())
	require.NoError(t, err)
	assert.Nil(t, client.exchange(t, 1).Request.JSONSchema)
	require.Equal(t, openrouter.PatchWriterResultPatch, result.Kind)
	expected := schemas.DocPatchExample
	expected.RunID = "run-123"
	expected.Provenance.GeneratedAt = patchWriteClock
	assert.Equal(t, expected, *result.Patch)
}

// Temperature override 0 always present in the outgoing request (wins over
// binding params).
func TestPatchWriterAdapter_temperature_override_zero_always(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)
	_, err := writer.Write(writeRequest())
	require.NoError(t, err)
	assert.Equal(t, float64(0), client.exchange(t, 1).Request.TemperatureOverride)
}

// Schema-abstain from the client passes through verbatim — never hand-patched
// into compliance. (The bounded corrective-retry mechanics live in the client,
// ported concurrently; the adapter's contract is the pass-through.)
func TestPatchWriterAdapter_schema_abstain_passthrough_never_hand_patched(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		{Completion: openrouter.PatchWriterRoleCompletion{
			Kind:   "schema-abstain",
			Reason: "schema_violation",
			Attempts: []openrouter.PatchWriterSchemaViolation{
				{Attempt: 1, Issues: []string{"doc_path: Required"}, RawText: "this is not json"},
				{Attempt: 2, Issues: []string{"doc_path: String must contain at least 1 character(s)"}, RawText: `{"doc_path": ""}`},
				{Attempt: 3, Issues: []string{"hunks: Expected array, received string"}, RawText: `{"doc_path": "docs/a.md", "hunks": "not-an-array"}`},
			},
			Telemetry: telemetryFor("anthropic/claude-sonnet-4.5", "anthropic/claude-sonnet-4.5", false),
		}},
	}}
	writer := newAdapter(t, client)

	result, err := writer.Write(writeRequest())
	require.NoError(t, err)
	require.Equal(t, openrouter.PatchWriterResultAbstain, result.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, result.Reason)
	assert.Equal(t, "anthropic/claude-sonnet-4.5", result.Telemetry.RoutedModel)
	assert.Nil(t, result.Patch)
	assert.Equal(t, 1, client.exchangeCount())
}

// Multi-target: two adapter calls in one session — same runId verbatim,
// different doc_paths.
func TestPatchWriterAdapter_multi_target_two_calls_one_session(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", withDocPath(schemas.DocPatchExample, "docs/a.md")),
		jsonCompletion("anthropic/claude-sonnet-4.5", withDocPath(schemas.DocPatchExample, "docs/b.md")),
	}}
	writer := newAdapter(t, client)

	firstReq := writeRequest()
	firstReq.RunID = "run-shared"
	firstReq.DocTargets = []openrouter.PatchWriterDocTarget{{Path: "docs/a.md", Mode: "auto"}}
	firstReq.ExpectedBaseContentHashes = map[string]string{"docs/a.md": bareHashA}
	first, err := writer.Write(firstReq)
	require.NoError(t, err)

	secondReq := writeRequest()
	secondReq.RunID = "run-shared"
	secondReq.DocTargets = []openrouter.PatchWriterDocTarget{{Path: "docs/b.md", Mode: "suggest"}}
	secondReq.ExpectedBaseContentHashes = map[string]string{"docs/b.md": bareHashB}
	second, err := writer.Write(secondReq)
	require.NoError(t, err)

	require.Equal(t, openrouter.PatchWriterResultPatch, first.Kind)
	require.Equal(t, openrouter.PatchWriterResultPatch, second.Kind)
	assert.Equal(t, "run-shared", first.Patch.RunID)
	assert.Equal(t, "run-shared", second.Patch.RunID)
	assert.Equal(t, "docs/a.md", first.Patch.DocPath)
	assert.Equal(t, "docs/b.md", second.Patch.DocPath)
}

// withDocPath copies the example body with a different proposed doc_path.
func withDocPath(patch schemas.DocPatch, docPath string) map[string]any {
	body := patchBodyOf(patch)
	body["doc_path"] = docPath
	return body
}

// The rendered workspace is transported as ONE untrusted block; the caller
// prompt is transported verbatim. (The delimiter wrapping +
// <untrusted-repository-content id="1"> composition of the user message is
// the client's composeUserMessage, ported concurrently — the adapter's
// contract is the ONE untrusted block plus the verbatim system prompt.)
func TestPatchWriterAdapter_workspace_is_one_untrusted_block_prompt_verbatim(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)
	request := writeRequest()
	request.SystemPrompt = strPtr("CUSTOM FIVE-DUTY PROMPT")
	_, err := writer.Write(request)
	require.NoError(t, err)

	exchange := client.exchange(t, 1)
	assert.Equal(t, "CUSTOM FIVE-DUTY PROMPT", exchange.Request.System)
	require.Len(t, exchange.Request.UntrustedContent, 1)
	assert.Equal(t, request.Workspace, exchange.Request.UntrustedContent[0])
	assert.Contains(t, exchange.Request.UntrustedContent[0], "WORKSPACE RENDER")
	assert.Contains(t, exchange.Request.User, "Candidate target documents")
}

// Fallback system prompt (no caller prompt) carries the output contract only —
// never duty text.
func TestPatchWriterAdapter_fallback_system_prompt_contract_only(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)
	request := writeRequest()
	request.SystemPrompt = nil
	_, err := writer.Write(request)
	require.NoError(t, err)

	system := client.exchange(t, 1).Request.System
	assert.Contains(t, system, "patch-writer")
	assert.Contains(t, system, "evidence_refs")
	assert.Contains(t, system, "symbol_diff | api_diff")
	// Duty/lock text is NEVER re-stated by the adapter (one canonical prompt
	// source — workspace.BuildWriterSystemPrompt).
	assert.NotContains(t, system, "Your five duties, in order:")
}

// Invalid requests → typed error BEFORE any request is issued.
func TestPatchWriterAdapter_invalid_requests_typed_error_before_any_request(t *testing.T) {
	client := &fakeClient{}
	writer := newAdapter(t, client)

	request := writeRequest()
	request.RunID = ""
	_, err := writer.Write(request)
	assert.Equal(t, "invalid_patch_writer_input", docutil.CodeOf(err))

	request = writeRequest()
	request.DocTargets = nil
	_, err = writer.Write(request)
	assert.Equal(t, "invalid_patch_writer_input", docutil.CodeOf(err))

	request = writeRequest()
	request.PolicyVersion = 0
	_, err = writer.Write(request)
	assert.Equal(t, "invalid_patch_writer_input", docutil.CodeOf(err))

	request = writeRequest()
	request.Clock = nil
	_, err = writer.Write(request)
	assert.Equal(t, "invalid_patch_writer_input", docutil.CodeOf(err))

	assert.Equal(t, 0, client.exchangeCount())
}

// Hostile workspace containing the API key → the gateway guard fires before
// I/O (the guard is the client boundary contract; the double mirrors it).
func TestPatchWriterAdapter_hostile_workspace_guard_fires_before_io(t *testing.T) {
	client := &fakeClient{
		guardAPIKey: testAPIKey,
		script: []scriptedOutcome{
			jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
		},
	}
	writer := newAdapter(t, client)
	request := writeRequest()
	request.Workspace = fmt.Sprintf("steal %s now", testAPIKey)

	_, err := writer.Write(request)
	require.Error(t, err)
	var guard *openrouter.PatchWriterGuardError
	require.True(t, errors.As(err, &guard))
	assert.Equal(t, "gateway_guard_violation", guard.Code())
	assert.Equal(t, 0, client.exchangeCount())
}

// Structural mirror pin: ValidateWriterOutputContract mirrors core duties.ts
// semantics (the shared fixture cases ported as Go literals).
func TestPatchWriterAdapter_structural_mirror_pin_writer_output_contract(t *testing.T) {
	ok := openrouter.ValidateWriterOutputContract(map[string]any{"doc_path": "docs/a.md", "hunks": []any{}})
	assert.True(t, ok.Ok)
	assert.Empty(t, ok.Violations)

	nonObject := openrouter.ValidateWriterOutputContract("nope")
	assert.False(t, nonObject.Ok)

	missing := openrouter.ValidateWriterOutputContract(map[string]any{"hunks": []any{}})
	assert.False(t, missing.Ok)
	assert.Contains(t, missing.Violations, "output is missing 'doc_path'")

	badHunks := openrouter.ValidateWriterOutputContract(map[string]any{"doc_path": "docs/a.md", "hunks": "x"})
	assert.False(t, badHunks.Ok)
	assert.Contains(t, badHunks.Violations, "'hunks' must be an array")
}

// Verdicts match the shared drift-guard fixture
// (writer-output-contract.fixture.json — core/openrouter mirror contract; the
// fixture is not on the Go tree, so its cases are ported as literals).
func TestPatchWriterAdapter_writer_output_contract_fixture_verdicts(t *testing.T) {
	fixtureCases := []struct {
		name       string
		output     any
		wantOK     bool
		violations []string
	}{
		{
			name: "valid patch object (doc_path + one hunk)",
			output: map[string]any{
				"doc_path": "docs/payments.md",
				"hunks": []any{map[string]any{
					"anchor": map[string]any{
						"start_line":   3,
						"end_line":     3,
						"content_hash": "67df5f2fc5a81b8885e723bab5e54435436ef2177c4144031511c7342c32b506",
					},
					"delete_text": "Refunds require an idempotency key.",
					"insert_text": "Refunds require an idempotency key above 10 USD.",
					"evidence_refs": []any{map[string]any{
						"kind": "symbol_diff", "ref": "src/payments/refund.ts:3", "note": "idempotencyKey became optional",
					}},
				}},
			},
			wantOK: true,
		},
		{
			name:       "missing hunks",
			output:     map[string]any{"doc_path": "docs/payments.md"},
			wantOK:     false,
			violations: []string{"output is missing 'hunks'"},
		},
		{
			name:       "missing doc_path",
			output:     map[string]any{"hunks": []any{}},
			wantOK:     false,
			violations: []string{"output is missing 'doc_path'"},
		},
		{
			name:       "non-object output (string)",
			output:     "nope",
			wantOK:     false,
			violations: []string{"output must be a JSON object (not a primitive, array, or null)"},
		},
	}
	for _, testCase := range fixtureCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := openrouter.ValidateWriterOutputContract(testCase.output)
			assert.Equal(t, testCase.wantOK, result.Ok)
			if !testCase.wantOK {
				assert.Equal(t, testCase.violations, result.Violations)
			}
		})
	}
}

// OD-R42: the adapter derives anchor content hashes mechanically (bare 64-hex
// over the quoted span).
func TestPatchWriterAdapter_OD_R42_derived_anchor_hashes(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)

	result, err := writer.Write(writeRequest())
	require.NoError(t, err)
	require.Equal(t, openrouter.PatchWriterResultPatch, result.Kind)
	require.NotEmpty(t, result.Patch.Hunks)
	for _, hunk := range result.Patch.Hunks {
		assert.Equal(t, docutil.ContentHash(hunk.DeleteText), hunk.Anchor.ContentHash)
		assert.Regexp(t, bareHexRE, hunk.Anchor.ContentHash)
	}
	// The example's own anchor hash equals the derived one (coherence).
	assert.Equal(t, derivedExampleSpanHash, result.Patch.Hunks[0].Anchor.ContentHash)
}

// OD-R42: a hostile model-proposed WRONG hash is overwritten, never trusted;
// pure insertions hash the empty span.
func TestPatchWriterAdapter_OD_R42_hostile_hash_overwritten_insertion_hashes_empty(t *testing.T) {
	hostile := patchBodyOf(schemas.DocPatchExample)
	exampleHunk := schemas.DocPatchExample.Hunks[0]
	hostile["hunks"] = []any{
		map[string]any{
			"anchor":        map[string]any{"start_line": exampleHunk.Anchor.StartLine, "end_line": exampleHunk.Anchor.EndLine, "content_hash": wrongHostileHash},
			"delete_text":   exampleHunk.DeleteText,
			"insert_text":   exampleHunk.InsertText,
			"evidence_refs": []any{map[string]any{"kind": "symbol_diff", "ref": "src/payments/refund.ts:42", "note": "idempotencyKey became optional"}},
		},
		map[string]any{
			"anchor":        map[string]any{"start_line": 100, "end_line": 100, "content_hash": wrongHostileHash},
			"delete_text":   "",
			"insert_text":   "Appendix: the review window change landed in this run.",
			"evidence_refs": []any{map[string]any{"kind": "other", "ref": "appendix", "note": "addition-only hunk"}},
		},
	}
	client := &fakeClient{script: []scriptedOutcome{jsonCompletion("anthropic/claude-sonnet-4.5", hostile)}}
	writer := newAdapter(t, client)

	result, err := writer.Write(writeRequest())
	require.NoError(t, err)
	require.Equal(t, openrouter.PatchWriterResultPatch, result.Kind)
	require.Len(t, result.Patch.Hunks, 2)
	// The hostile hash never survives assembly — the adapter owns the field.
	assert.NotEqual(t, wrongHostileHash, result.Patch.Hunks[0].Anchor.ContentHash)
	assert.Equal(t, derivedExampleSpanHash, result.Patch.Hunks[0].Anchor.ContentHash)
	// Pure insertion: the hash of the EMPTY string.
	assert.Equal(t, emptyStringHash, result.Patch.Hunks[1].Anchor.ContentHash)
	assert.NotEqual(t, wrongHostileHash, result.Patch.Hunks[1].Anchor.ContentHash)
	// base_content_hash stays CALLER-supplied (race gate untouched).
	assert.Equal(t, schemas.DocPatchExample.BaseContentHash, result.Patch.BaseContentHash)
}

// Proposed doc_path outside the caller docTargets → typed
// patch_assembly_violation (membership check).
func TestPatchWriterAdapter_doc_path_outside_targets_assembly_violation(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", withDocPath(schemas.DocPatchExample, "docs/rogue.md")),
	}}
	writer := newAdapter(t, client)

	_, err := writer.Write(writeRequest())
	require.Error(t, err)
	assert.Regexp(t, regexp.MustCompile(`is not among the caller's docTargets`), err.Error())
	assert.Contains(t, err.Error(), "docs/rogue.md")
	assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))
	// One attempt, zero further I/O — the check is cheap and pre-assembly.
	assert.Equal(t, 1, client.exchangeCount())
}

// The body schema omits identity fields (strict) and mirrors the
// non-overlap refinement (TS: DOC_PATCH_BODY_SCHEMA pins).
func TestDocPatchBodySchema_omits_identity_fields_strict(t *testing.T) {
	body, err := openrouter.ParseDocPatchBody(patchBodyOf(schemas.DocPatchExample))
	require.NoError(t, err)
	assert.Equal(t, "docs/payments.md", body.DocPath)
	require.Len(t, body.Hunks, 1)

	extraIdentity := patchBodyOf(schemas.DocPatchExample)
	extraIdentity["run_id"] = "model-must-not-propose"
	_, err = openrouter.ParseDocPatchBody(extraIdentity)
	require.Error(t, err)
	assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))

	overlapping := map[string]any{
		"doc_path": "docs/a.md",
		"hunks": []any{
			map[string]any{
				"anchor":        map[string]any{"start_line": 1, "end_line": 5, "content_hash": bareHashA},
				"delete_text":   "x",
				"insert_text":   "y",
				"evidence_refs": []any{map[string]any{"kind": "other", "ref": "r", "note": "n"}},
			},
			map[string]any{
				"anchor":        map[string]any{"start_line": 5, "end_line": 9, "content_hash": bareHashB},
				"delete_text":   "",
				"insert_text":   "z",
				"evidence_refs": []any{map[string]any{"kind": "other", "ref": "r", "note": "n"}},
			},
		},
		"evidence_refs": []any{map[string]any{"kind": "other", "ref": "r", "note": "n"}},
		"minimality":    map[string]any{"measured": false, "added_lines": 2, "removed_lines": 1},
	}
	_, err = openrouter.ParseDocPatchBody(overlapping)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlap")
}

// The generic Phase-2 role seam is a typed programming error (the adapter
// never fabricates identity — invariant 10).
func TestPatchWriterAdapter_generic_seam_is_typed_programming_error(t *testing.T) {
	client := &fakeClient{}
	writer := newAdapter(t, client)
	_, err := writer.WritePatch(openrouter.PatchWriterRoleInvocation{System: "s", User: "u"})
	require.Error(t, err)
	assert.Equal(t, "patch_writer_identity_required", docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "run_id/policy_version/clock/docTargets")
	assert.Equal(t, 0, client.exchangeCount())
}

// An unbound role fails construction closed (fail-closed typed error before
// any request can be issued).
func TestPatchWriterAdapter_unbound_role_fails_construction(t *testing.T) {
	client := &fakeClient{bindingErr: errors.New("role 'patch-writer' has no binding in the models config")}
	_, err := openrouter.CreatePatchWriterAdapter(openrouter.PatchWriterAdapterOptions{Client: client})
	require.Error(t, err)
}

/* ------------------------------------------------- coverage completions */

// All seam error accessors carry their stable codes + messages.
func TestPatchWriterAdapter_seam_error_accessors(t *testing.T) {
	transport := openrouter.NewPatchWriterTransportError("connect ETIMEDOUT 10.0.0.1:443", nil)
	assert.Equal(t, "transport_failure", transport.Code())
	assert.Equal(t, "connect ETIMEDOUT 10.0.0.1:443", transport.Error())
	assert.Nil(t, transport.Status)

	statusful := openrouter.NewPatchWriterTransportError("HTTP 500", pwIntPtr(500))
	assert.Equal(t, 500, *statusful.Status)

	total := openrouter.NewPatchWriterTotalInferenceFailure(
		"all model targets exhausted for role patch-writer", "patch-writer",
		[]openrouter.PatchWriterModelAttempt{{Target: "m", Attempt: 1}})
	assert.Equal(t, "total_inference_failure", total.Code())
	assert.Equal(t, "all model targets exhausted for role patch-writer", total.Error())
	assert.Equal(t, "patch-writer", total.Role)

	guard := openrouter.NewPatchWriterGuardError("api key material detected in the outgoing payload", "system")
	assert.Equal(t, "gateway_guard_violation", guard.Code())
	assert.Equal(t, "api key material detected in the outgoing payload", guard.Error())
	assert.Equal(t, "system", guard.Location)
}

// Every invalid-request branch is the typed error with the exact TS message.
func TestPatchWriterAdapter_validate_request_table(t *testing.T) {
	client := &fakeClient{}
	writer := newAdapter(t, client)
	cases := []struct {
		name    string
		mutate  func(*openrouter.PatchWriterRequest)
		message string
	}{
		{"empty workspace", func(r *openrouter.PatchWriterRequest) { r.Workspace = "" },
			"workspace must be a non-empty string (the rendered workspace)"},
		{"empty system prompt", func(r *openrouter.PatchWriterRequest) { r.SystemPrompt = strPtr("") },
			"systemPrompt must be a non-empty string when provided"},
		{"non-positive policy version", func(r *openrouter.PatchWriterRequest) { r.PolicyVersion = 0 },
			"policyVersion must be a positive integer"},
		{"empty run id", func(r *openrouter.PatchWriterRequest) { r.RunID = "" },
			"runId must be a non-empty string"},
		{"empty doc targets", func(r *openrouter.PatchWriterRequest) { r.DocTargets = nil },
			"docTargets must be a non-empty array of target documents"},
		{"target without path", func(r *openrouter.PatchWriterRequest) {
			r.DocTargets = []openrouter.PatchWriterDocTarget{{Path: "", Mode: "auto"}}
		}, "every docTarget must carry a non-empty path"},
		{"target without mode", func(r *openrouter.PatchWriterRequest) {
			r.DocTargets = []openrouter.PatchWriterDocTarget{{Path: "docs/a.md", Mode: ""}}
		}, "docTarget 'docs/a.md' must carry a non-empty mode"},
		{"target with empty current hash", func(r *openrouter.PatchWriterRequest) {
			r.DocTargets = []openrouter.PatchWriterDocTarget{{Path: "docs/a.md", Mode: "auto", CurrentContentHash: strPtr("")}}
		}, "docTarget 'docs/a.md' currentContentHash must be non-empty when provided"},
		{"missing clock", func(r *openrouter.PatchWriterRequest) { r.Clock = nil },
			"clock must be a () => string function (injected, never a local Date)"},
		{"offending hash entry", func(r *openrouter.PatchWriterRequest) {
			r.ExpectedBaseContentHashes = map[string]string{"": ""}
		}, "expectedBaseContentHashes must map non-empty doc paths to non-empty hashes (offending entry: '')"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := writeRequest()
			testCase.mutate(&request)
			_, err := writer.Write(request)
			require.Error(t, err)
			assert.Equal(t, "invalid_patch_writer_input", docutil.CodeOf(err))
			// The TS GatewayError `message` is the docutil Msg (its Error()
			// rendering prefixes the stable code — house style).
			var coded *docutil.Error
			require.True(t, errors.As(err, &coded))
			assert.Equal(t, testCase.message, coded.Msg)
		})
	}
	assert.Equal(t, 0, client.exchangeCount())
}

// A completion with no structured value is a typed internal-invariant failure.
func TestPatchWriterAdapter_completion_without_structured_value_fails_closed(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{{Completion: openrouter.PatchWriterRoleCompletion{
		Kind:      "completion",
		Text:      "",
		Telemetry: telemetryFor("anthropic/claude-sonnet-4.5", "anthropic/claude-sonnet-4.5", false),
	}}}}
	writer := newAdapter(t, client)
	_, err := writer.Write(writeRequest())
	require.Error(t, err)
	assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "carried no structured value")
}

// The light contract pre-check failure is a typed internal-invariant failure,
// never a repair path.
func TestPatchWriterAdapter_light_contract_check_failure_is_internal_invariant(t *testing.T) {
	writer := newAdapter(t, &fakeClient{script: []scriptedOutcome{
		completionOutcome("anthropic/claude-sonnet-4.5", "not-an-object"),
	}})
	_, err := writer.Write(writeRequest())
	require.Error(t, err)
	assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "light contract pre-check")

	writer = newAdapter(t, &fakeClient{script: []scriptedOutcome{
		completionOutcome("anthropic/claude-sonnet-4.5", map[string]any{"doc_path": "docs/a.md", "hunks": "x"}),
	}})
	_, err = writer.Write(writeRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "light contract pre-check")
}

// No expected hashes at all renders the none-marker in the deterministic
// user prompt (the write still fails assembly closed — there is no hash to
// carry base_content_hash from).
func TestPatchWriterAdapter_nil_hashes_prompt_marker(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)
	request := writeRequest()
	request.ExpectedBaseContentHashes = nil
	_, err := writer.Write(request)
	require.Error(t, err)
	assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))
	assert.Contains(t, client.exchange(t, 1).Request.User, "(none provided)")
}

// The cachePrefix default (true) and the explicit override ride the outgoing
// request.
func TestPatchWriterAdapter_cache_prefix_override(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		jsonCompletion("anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
	}}
	writer := newAdapter(t, client)
	request := writeRequest()
	request.CachePrefix = pwBoolPtr(false)
	_, err := writer.Write(request)
	require.NoError(t, err)
	assert.False(t, client.exchange(t, 1).Request.CachePrefix)
}

func pwBoolPtr(v bool) *bool { return &v }

// Construction without a client is fail-closed typed.
func TestPatchWriterAdapter_create_requires_a_client(t *testing.T) {
	_, err := openrouter.CreatePatchWriterAdapter(openrouter.PatchWriterAdapterOptions{})
	require.Error(t, err)
	assert.Equal(t, "invalid_patch_writer_input", docutil.CodeOf(err))
}

// The body schema enforces the DOC_PATCH_SCHEMA rules for the body's members.
func TestDocPatchBodySchema_invalid_bodies(t *testing.T) {
	validEvidence := []any{map[string]any{"kind": "other", "ref": "r", "note": "n"}}
	validMinimality := map[string]any{"measured": false, "added_lines": 0, "removed_lines": 0}
	hunk := map[string]any{
		"anchor":        map[string]any{"start_line": 1, "end_line": 1, "content_hash": bareHashA},
		"delete_text":   "x",
		"insert_text":   "y",
		"evidence_refs": validEvidence,
	}
	cases := []struct {
		name    string
		body    map[string]any
		message string
	}{
		{
			name:    "empty doc_path",
			body:    map[string]any{"doc_path": "", "hunks": []any{hunk}, "evidence_refs": validEvidence, "minimality": validMinimality},
			message: "doc_path",
		},
		{
			name:    "empty hunks",
			body:    map[string]any{"doc_path": "docs/a.md", "hunks": []any{}, "evidence_refs": validEvidence, "minimality": validMinimality},
			message: "hunks",
		},
		{
			name: "anchor end_line before start_line",
			body: map[string]any{
				"doc_path": "docs/a.md",
				"hunks": []any{map[string]any{
					"anchor":        map[string]any{"start_line": 5, "end_line": 2, "content_hash": bareHashA},
					"delete_text":   "x",
					"insert_text":   "y",
					"evidence_refs": validEvidence,
				}},
				"evidence_refs": validEvidence,
				"minimality":    validMinimality,
			},
			message: "anchor",
		},
		{
			name: "unknown evidence kind",
			body: map[string]any{
				"doc_path":      "docs/a.md",
				"hunks":         []any{hunk},
				"evidence_refs": []any{map[string]any{"kind": "not_a_kind", "ref": "r", "note": "n"}},
				"minimality":    validMinimality,
			},
			message: "evidence_refs",
		},
		{
			name: "negative minimality counts",
			body: map[string]any{
				"doc_path":      "docs/a.md",
				"hunks":         []any{hunk},
				"evidence_refs": validEvidence,
				"minimality":    map[string]any{"measured": false, "added_lines": -1, "removed_lines": 0},
			},
			message: "minimality",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := openrouter.ParseDocPatchBody(testCase.body)
			require.Error(t, err)
			assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))
			assert.Contains(t, err.Error(), testCase.message)
		})
	}
}

// A body that is not JSON-representable fails the body-schema check (fail
// closed, never a panic).
func TestDocPatchBodySchema_non_json_representable_body(t *testing.T) {
	_, err := openrouter.ParseDocPatchBody(map[string]any{"doc_path": make(chan int)})
	require.Error(t, err)
	assert.Equal(t, "patch_assembly_violation", docutil.CodeOf(err))
}

// A walk attempt with NO error text carries no timeout provenance (typed
// throw).
func TestPatchWriterAdapter_empty_attempt_error_keeps_typed_throw(t *testing.T) {
	client := &fakeClient{script: []scriptedOutcome{
		{Err: openrouter.NewPatchWriterTotalInferenceFailure(
			"all model targets exhausted for role patch-writer", "patch-writer",
			[]openrouter.PatchWriterModelAttempt{{Target: "anthropic/claude-sonnet-4.5", Attempt: 1, LatencyMS: 3}})},
	}}
	writer := newAdapter(t, client)
	result, err := writer.Write(writeRequest())
	require.Error(t, err)
	assert.Equal(t, openrouter.PatchWriterResultKind(""), result.Kind)
}
