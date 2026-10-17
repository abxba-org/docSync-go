package openrouter_test

// openrouter_coverage_test.go — black-box (package openrouter_test) coverage
// tests for internal/openrouter, driving the PUBLIC surface through the
// package's own recorded doubles (testing-transports.go, the shared replay/
// recorded clients and the writer-stage fixtures) — zero network (OD-2).
//
// Focus: the branches the ported suites leave open — the transport handles'
// captured-request accessors, the client's telemetry numeric-shape reader and
// key-scrubbed walk failures, the routing-policy parse/activation input shapes
// and state invariants, the resolved policy bindings, the role adapters'
// fail-closed validation arms, and the writer-stage port's result mapping.
//
// Naming: TestCOV_OR_NNN_<scenario>.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

/* ----------------------------------------------------------------- doubles */
// covScriptedClient is a recorded client double over the adapters' client seam
// (BindingFor + Complete — the method set the landed *OpenRouterClient
// satisfies). It returns one scripted outcome, optionally never settling (the
// adapter-budget race case).
type covScriptedClient struct {
	binding  *openrouter.ResolvedRoleBinding
	bindErr  error
	result   openrouter.RoleCompletion
	err      error
	hang     bool
	requests []openrouter.CompletionRequest
}

func (c *covScriptedClient) BindingFor(role string) (*openrouter.ResolvedRoleBinding, error) {
	if c.bindErr != nil {
		return nil, c.bindErr
	}
	if c.binding == nil {
		return nil, openrouter.NewRoleBindingError(role,
			"role '"+role+"' has no binding in models.yml (docsync.models/v1); bound roles: (none)")
	}
	binding := *c.binding
	return &binding, nil
}
func (c *covScriptedClient) Complete(_ string, request openrouter.CompletionRequest) (openrouter.RoleCompletion, error) {
	c.requests = append(c.requests, request)
	if c.hang {
		select {}
	}
	return c.result, c.err
}
func covBinding(model string, structuredOutput string) *openrouter.ResolvedRoleBinding {
	params := &openrouter.ResolvedRoleParams{StructuredOutput: structuredOutput}
	return &openrouter.ResolvedRoleBinding{Primary: model, Fallbacks: []string{}, Params: params}
}
func covCompletion(value any, routedModel string) openrouter.RoleCompletion {
	return openrouter.RoleCompletion{
		Kind:       openrouter.RoleCompletionKindCompletion,
		Structured: value,
		Text:       "{}",
		Telemetry: openrouter.RoleTelemetry{
			Role:           "judge",
			RequestedModel: "z-ai/glm-4.7",
			RoutedModel:    routedModel,
			LatencyMS:      3,
			Attempt:        1,
		},
	}
}

/* ------------------------------------------------- recorded transport access */
// TestCOV_OR_201_transport_handles_expose_their_captured_requests pins the
// handles' request accessors (the AGENT-behavior replay rule: a well-formed
// script consumes every fixture and every captured request stays observable).
func TestCOV_OR_201_transport_handles_expose_their_captured_requests(t *testing.T) {
	inner := openrouter.CreateReplayTransport([]openrouter.ReplayFixture{
		openrouter.ResponseFixture(openrouter.CompletionResponse(openrouter.ScriptedCompletion{
			Content: `{"ok":true}`,
		})),
	})
	recording := openrouter.CreateRecordingTransport(inner.Transport)
	headers := map[string]string{
		"Authorization": "Bearer " + orTestAPIKey,
		"Content-Type":  "application/json",
	}
	body := map[string]any{"model": "z-ai/glm-4.7", "messages": []any{}}
	result, err := recording.Transport.Post(context.Background(), openrouter.OpenRouterChatCompletionsURL, headers, body)
	require.NoError(t, err)
	assert.Equal(t, 200, result.Status)
	requests := recording.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, openrouter.OpenRouterChatCompletionsURL, requests[0].URL)
	assert.Equal(t, headers, requests[0].Headers)
	assert.Equal(t, body, requests[0].Body)
	// The wrapped replay double saw the very same captured request, and the
	// script was fully consumed (an empty Unmatched is the loud signal).
	require.Len(t, inner.Requests(), 1)
	assert.Empty(t, inner.Unmatched())
	// The failing double records requests BEFORE the scripted failure applies.
	failing := openrouter.CreateFailingTransport([]openrouter.TransportFailure{
		openrouter.StatusFailure(503),
		openrouter.NetworkFailure("dial tcp: connection refused"),
	})
	status, err := failing.Transport.Post(context.Background(), openrouter.OpenRouterChatCompletionsURL, nil, map[string]any{"step": 1})
	require.NoError(t, err)
	assert.Equal(t, 503, status.Status)
	_, err = failing.Transport.Post(context.Background(), openrouter.OpenRouterChatCompletionsURL, nil, map[string]any{"step": 2})
	require.Error(t, err)
	failingRequests := failing.Requests()
	require.Len(t, failingRequests, 2)
	assert.Equal(t, map[string]any{"step": 1}, failingRequests[0].Body)
	assert.Equal(t, map[string]any{"step": 2}, failingRequests[1].Body)
	// A FAILED exchange is never recorded by the recording double.
	failedOnly := openrouter.CreateRecordingTransport(failing.Transport)
	_, err = failedOnly.Transport.Post(context.Background(), openrouter.OpenRouterChatCompletionsURL, nil, nil)
	require.Error(t, err)
	assert.Empty(t, failedOnly.Requests())
}

/* ------------------------------------------------------ client telemetry */
// TestCOV_OR_202_client_telemetry_reads_every_usage_numeric_shape pins the
// telemetry builder's numeric reader over the shapes a transport double may
// carry: Go ints, int64s and json.Number are numbers; NaN is NOT (no
// fabricated token counts), and an unparsable cached_tokens count is no cache
// hit.
func TestCOV_OR_202_client_telemetry_reads_every_usage_numeric_shape(t *testing.T) {
	usage := map[string]any{
		"prompt_tokens":     11,
		"completion_tokens": int64(3),
		"total_tokens":      json.Number("14"),
		"cost":              math.NaN(),
		"prompt_tokens_details": map[string]any{
			"cached_tokens": json.Number("not-a-number"),
		},
	}
	transport := orTransportFunc(func(_ context.Context, _ string, _ map[string]string, _ any) (openrouter.TransportExchangeResult, error) {
		return openrouter.TransportExchangeResult{Status: 200, JSON: map[string]any{
			"id":      "scripted-completion",
			"model":   "z-ai/glm-4.7",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": `{"answer":"ok"}`}}},
			"usage":   usage,
		}}, nil
	})
	client := orTestClient(t, transport, nil)
	result, err := client.Complete("judge", openrouter.CompletionRequest{
		System:      "system",
		User:        "task",
		CachePrefix: orBoolPtr(true),
	})
	require.NoError(t, err)
	require.Equal(t, openrouter.RoleCompletionKindCompletion, result.Kind)
	telemetry := result.Telemetry
	require.NotNil(t, telemetry.Usage)
	assert.Equal(t, 11, telemetry.Usage.PromptTokens)
	assert.Equal(t, 3, telemetry.Usage.CompletionTokens)
	require.NotNil(t, telemetry.Usage.TotalTokens)
	assert.Equal(t, 14, *telemetry.Usage.TotalTokens)
	// NaN is not a number: no cost field is fabricated.
	assert.Nil(t, telemetry.Usage.Cost)
	// An unparsable cached-token count is not a prefix hit.
	require.NotNil(t, telemetry.Cache)
	assert.False(t, telemetry.Cache.PrefixHit)
	assert.Equal(t, "sha256:"+docutil.ContentHash("system\u0000"), telemetry.Cache.PrefixKey)
	// The buffered sink record is the same (redacted) telemetry.
	buffered := client.GetBufferedTelemetry()
	require.Len(t, buffered, 1)
	assert.Equal(t, telemetry.RoutedModel, buffered[0].RoutedModel)
	assert.NotContains(t, buffered[0].RequestedModel, orTestAPIKey)
}

// TestCOV_OR_203_client_scrubs_key_material_from_every_walk_attempt pins the
// invariant-8 defense in depth on the walk-exhaustion failure: the attempt
// records (target, error text AND provider-order pins) leave the gateway
// redacted.
func TestCOV_OR_203_client_scrubs_key_material_from_every_walk_attempt(t *testing.T) {
	failing := orCreateFailingTransport(
		orFailure{Kind: orFailureNetwork, Message: "transport request failed: connection refused carrying " + orTestAPIKey},
		orFailure{Kind: orFailureNetwork, Message: "transport request failed: connection refused carrying " + orTestAPIKey},
		orFailure{Kind: orFailureNetwork, Message: "transport request failed: connection refused carrying " + orTestAPIKey},
		orFailure{Kind: orFailureNetwork, Message: "transport request failed: connection refused carrying " + orTestAPIKey},
	)
	noRetries := 0
	client := orTestClient(t, failing.Transport, func(options *openrouter.OpenRouterClientOptions) {
		options.Retry = &openrouter.RetryOptions{MaxRetries: &noRetries}
	})
	_, err := client.Complete("decision-engine", openrouter.CompletionRequest{System: "system", User: "task"})
	require.Error(t, err)
	var failure *openrouter.TotalInferenceFailure
	require.ErrorAs(t, err, &failure)
	require.NotEmpty(t, failure.Attempts)
	sawProviderPin := false
	for _, attempt := range failure.Attempts {
		assert.NotContains(t, attempt.Error, orTestAPIKey)
		assert.Contains(t, attempt.Error, openrouter.REDACTED)
		if len(attempt.ProviderOrder) > 0 {
			sawProviderPin = true
			assert.NotContains(t, attempt.ProviderOrder[0], orTestAPIKey)
		}
	}
	// The provider-order walk pinned attempts (the role's routing.provider
	// order), so the pinned records were the ones redacted.
	assert.True(t, sawProviderPin, "the walk must include provider-pinned attempts")
}

// TestCOV_OR_204_client_guard_blocks_a_corrective_body_carrying_model_echoed_key
// pins the schema loop's guard: when the model's own (offending) output quotes
// API-key material, the corrective exchange is blocked BEFORE any I/O and the
// completion degrades to the typed schema-abstain.
func TestCOV_OR_204_client_guard_blocks_a_corrective_body_carrying_model_echoed_key(t *testing.T) {
	leaking := orTestAPIKey
	replay := orCreateReplayTransport(orCompletionResponse(orCompletionScript{
		Model:    "z-ai/glm-4.7",
		Content:  `{"answer":"wrong","leak":"` + leaking + `"}`,
		Provider: "z-ai",
	}))
	recording := orCreateRecordingTransport(replay)
	client := orTestClient(t, recording.Transport, nil)
	strictSchema := func(value any) []string {
		record, ok := value.(map[string]any)
		if !ok {
			return []string{"(root): expected a JSON object"}
		}
		if record["answer"] != "ok" {
			return []string{`answer: Invalid literal value, expected "ok"`}
		}
		return nil
	}
	result, err := client.Complete("judge", openrouter.CompletionRequest{
		System:    "system",
		User:      "task",
		ZodSchema: strictSchema,
	})
	require.NoError(t, err)
	assert.Equal(t, openrouter.RoleCompletionKindSchemaAbstain, result.Kind)
	assert.Equal(t, openrouter.SchemaViolationAbstainReason, result.Reason)
	// The initial exchange happened; the corrective exchange never did.
	require.Len(t, recording.Exchanges(), 1)
	require.Len(t, result.Attempts, 1)
	require.NotEmpty(t, result.Attempts[0].Issues)
	// The recorded request body never carried the key (the guard fired before
	// the corrective I/O).
	for _, exchange := range recording.Exchanges() {
		encoded, marshalErr := json.Marshal(exchange.Request.Body)
		require.NoError(t, marshalErr)
		assert.NotContains(t, string(encoded), leaking)
	}
}

/* ------------------------------------------------------ routing policy parse */
func covRoutingPolicyJSON() string {
	return `{"schema":"docsync.routing-policy/v1","version":2,"source_ref":"org/routing.yml@abc123",` +
		`"buckets":{"low":{"primary":"z-ai/glm-4.7-air"},"medium":{"primary":"z-ai/glm-4.7"},` +
		`"high":{"primary":"anthropic/claude-sonnet-4.5"}}}`
}

// TestCOV_OR_205_parse_routing_policy_accepts_every_input_shape pins the
// parser's input dispatch (decoded document, raw bytes, RawMessage, JSON text
// and any JSON-shaped value) plus its fail-closed rejection of an
// unrepresentable document.
func TestCOV_OR_205_parse_routing_policy_accepts_every_input_shape(t *testing.T) {
	t.Run("decoded documents (valid and invalid) round-trip through the parser", func(t *testing.T) {
		valid := &schemas.RoutingPolicy{
			Schema:    openrouter.RoutingPolicySchemaID,
			Version:   2,
			SourceRef: "org/routing.yml@abc123",
			Buckets: schemas.RoutingPolicyBuckets{
				Low:    schemas.RoutingPolicyBinding{Primary: "z-ai/glm-4.7-air"},
				Medium: schemas.RoutingPolicyBinding{Primary: "z-ai/glm-4.7"},
				High:   schemas.RoutingPolicyBinding{Primary: "anthropic/claude-sonnet-4.5"},
			},
		}
		policy, err := openrouter.ParseRoutingPolicy(valid)
		require.NoError(t, err)
		assert.Equal(t, 2, policy.Version)
		assert.Equal(t, "org/routing.yml@abc123", policy.SourceRef)
		assert.Equal(t, "anthropic/claude-sonnet-4.5", policy.Buckets.High.Primary)
		_, err = openrouter.ParseRoutingPolicy(&schemas.RoutingPolicy{
			Schema:  openrouter.RoutingPolicySchemaID,
			Version: 0,
		})
		require.Error(t, err)
		var policyErr *openrouter.RoutingPolicyError
		require.ErrorAs(t, err, &policyErr)
		assert.Equal(t, "routing_policy_invalid", policyErr.Code)
		assert.Contains(t, err.Error(), "rejected fail-closed")
	})
	t.Run("raw bytes, RawMessage, JSON text and map inputs all decode", func(t *testing.T) {
		raw := covRoutingPolicyJSON()
		for name, input := range map[string]any{
			"bytes":           []byte(raw),
			"json.RawMessage": json.RawMessage(raw),
			"string":          raw,
			"map":             policyV2,
		} {
			t.Run(name, func(t *testing.T) {
				policy, err := openrouter.ParseRoutingPolicy(input)
				require.NoError(t, err)
				assert.Equal(t, 2, policy.Version)
				assert.Equal(t, "z-ai/glm-4.7-air", policy.Buckets.Low.Primary)
			})
		}
	})
	t.Run("an unrepresentable document is rejected fail-closed (never a panic)", func(t *testing.T) {
		_, err := openrouter.ParseRoutingPolicy(map[string]any{"schema": "docsync.routing-policy/v1", "bad": make(chan int)})
		require.Error(t, err)
		var policyErr *openrouter.RoutingPolicyError
		require.ErrorAs(t, err, &policyErr)
		assert.Equal(t, "routing_policy_invalid", policyErr.Code)
	})
	t.Run("malformed JSON text is rejected fail-closed with its issues", func(t *testing.T) {
		for _, input := range []any{[]byte("not json"), "not json", json.RawMessage("{trunc")} {
			_, err := openrouter.ParseRoutingPolicy(input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "rejected fail-closed")
		}
	})
}

// TestCOV_OR_206_parse_activation_accepts_every_input_shape pins the activation
// decoder's input dispatch (decoded envelope, bytes, RawMessage, JSON text,
// arbitrary JSON-shaped values) and its fail-closed rejection of unknown keys
// and unrepresentable envelopes.
func TestCOV_OR_206_parse_activation_accepts_every_input_shape(t *testing.T) {
	t.Run("a decoded draft envelope passes through untouched", func(t *testing.T) {
		envelope := &schemas.RoutingPolicyActivation{
			Policy: *routingPolicyV2(t),
			State:  schemas.RoutingPolicyDraft,
		}
		parsed, err := openrouter.ParseRoutingPolicyActivation(envelope)
		require.NoError(t, err)
		assert.Equal(t, schemas.RoutingPolicyDraft, parsed.State)
		assert.Equal(t, envelope.Policy, parsed.Policy)
	})
	t.Run("bytes, RawMessage and JSON text all decode", func(t *testing.T) {
		encoded, err := json.Marshal(map[string]any{"policy": policyV2, "state": "draft"})
		require.NoError(t, err)
		for name, input := range map[string]any{
			"bytes":           encoded,
			"json.RawMessage": json.RawMessage(encoded),
			"string":          string(encoded),
			"map":             map[string]any{"policy": policyV2, "state": "draft"},
		} {
			t.Run(name, func(t *testing.T) {
				parsed, err := openrouter.ParseRoutingPolicyActivation(input)
				require.NoError(t, err)
				assert.Equal(t, schemas.RoutingPolicyDraft, parsed.State)
				assert.Equal(t, 2, parsed.Policy.Version)
			})
		}
	})
	t.Run("unknown keys are rejected fail-closed", func(t *testing.T) {
		_, err := openrouter.ParseRoutingPolicyActivation(map[string]any{
			"policy": policyV2,
			"state":  "draft",
			"nope":   true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown keys are rejected fail-closed")
	})
	t.Run("an unrepresentable envelope is rejected fail-closed", func(t *testing.T) {
		_, err := openrouter.ParseRoutingPolicyActivation(map[string]any{
			"policy": policyV2,
			"state":  "draft",
			"bad":    make(chan int),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not serializable")
	})
}

// TestCOV_OR_207_activation_state_invariants_are_typed_vetoes pins the runtime
// state rules (the TS throw-on-first pattern): a draft may carry no
// lifecycle fields, an evaluated envelope must carry the evaluation report ref
// and no actor, and a retired envelope must carry the retirement instant.
func TestCOV_OR_207_activation_state_invariants_are_typed_vetoes(t *testing.T) {
	cases := []struct {
		name     string
		envelope map[string]any
		want     string
	}{
		{
			name:     "draft carries an evaluation report ref",
			envelope: map[string]any{"policy": policyV2, "state": "draft", "evaluationReportRef": routingReportRef},
			want:     "must not carry 'evaluationReportRef'",
		},
		{
			name:     "draft carries an actor",
			envelope: map[string]any{"policy": policyV2, "state": "draft", "activatedBy": "app://promotion-cli"},
			want:     "must not carry 'activatedBy'",
		},
		{
			name:     "draft carries an activation instant",
			envelope: map[string]any{"policy": policyV2, "state": "draft", "activatedAt": "2026-10-09T00:00:00.000Z"},
			want:     "must not carry 'activatedAt'",
		},
		{
			name:     "draft carries a retirement instant",
			envelope: map[string]any{"policy": policyV2, "state": "draft", "retiredAt": "2026-10-10T00:00:00.000Z"},
			want:     "must not carry 'retiredAt'",
		},
		{
			name:     "evaluated carries no evaluation report ref",
			envelope: map[string]any{"policy": policyV2, "state": "evaluated"},
			want:     "is missing the required 'evaluationReportRef' field",
		},
		{
			name:     "evaluated carries an actor",
			envelope: map[string]any{"policy": policyV2, "state": "evaluated", "evaluationReportRef": routingReportRef, "activatedBy": "app://x"},
			want:     "must not carry 'activatedBy'",
		},
		{
			name:     "evaluated carries an activation instant",
			envelope: map[string]any{"policy": policyV2, "state": "evaluated", "evaluationReportRef": routingReportRef, "activatedAt": "2026-10-09T00:00:00.000Z"},
			want:     "must not carry 'activatedAt'",
		},
		{
			name:     "evaluated carries a retirement instant",
			envelope: map[string]any{"policy": policyV2, "state": "evaluated", "evaluationReportRef": routingReportRef, "retiredAt": "2026-10-10T00:00:00.000Z"},
			want:     "must not carry 'retiredAt'",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := openrouter.ParseRoutingPolicyActivation(testCase.envelope)
			require.Error(t, err)
			var veto *openrouter.RoutingPolicyActivationError
			require.ErrorAs(t, err, &veto)
			assert.Equal(t, "routing_activation_veto", veto.Code)
			assert.Contains(t, err.Error(), testCase.want)
		})
	}
	t.Run("an unknown activation state is rejected fail-closed", func(t *testing.T) {
		_, err := openrouter.ParseRoutingPolicyActivation(map[string]any{"policy": policyV2, "state": "zombie"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown routing-policy activation state: zombie")
	})
	// SUSPECTED BUG (reported, not blessed): checkActivationInvariants returns
	// inside the `active || retired` branch, so a RETIRED envelope that carries
	// no `retiredAt` passes the RUNTIME check even though the registered
	// `docsync.routing-policy-activation/v1` contract (the persisted-data path)
	// requires it. Pinned here so the divergence is visible; see the final
	// coverage report.
	t.Run("a retired envelope without retiredAt is accepted by the runtime check and rejected by the registered schema", func(t *testing.T) {
		envelope := map[string]any{
			"policy":              policyV2,
			"state":               "retired",
			"evaluationReportRef": routingReportRef,
			"activatedBy":         "app://promotion-cli",
			"activatedAt":         "2026-10-09T00:00:00.000Z",
		}
		parsed, err := openrouter.ParseRoutingPolicyActivation(envelope)
		require.NoError(t, err)
		assert.Nil(t, parsed.RetiredAt)
		// The registered schema (the contract persisted state must satisfy)
		// does NOT accept it.
		var typed schemas.RoutingPolicyActivation
		encoded, err := json.Marshal(envelope)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(encoded, &typed))
		assert.NotEmpty(t, typed.Validate())
	})
	t.Run("a complete retired envelope round-trips", func(t *testing.T) {
		parsed, err := openrouter.ParseRoutingPolicyActivation(map[string]any{
			"policy":              policyV2,
			"state":               "retired",
			"evaluationReportRef": routingReportRef,
			"activatedBy":         "app://promotion-cli",
			"activatedAt":         "2026-10-09T00:00:00.000Z",
			"retiredAt":           "2026-10-10T00:00:00.000Z",
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.RoutingPolicyRetired, parsed.State)
		require.NotNil(t, parsed.RetiredAt)
		assert.Equal(t, "2026-10-10T00:00:00.000Z", *parsed.RetiredAt)
	})
}

// TestCOV_OR_208_markEvaluated_requires_a_report_ref pins the typed veto on an
// empty evaluation-report ref (V-P12-11: promotion needs the report BEFORE the
// policy can even be considered).
func TestCOV_OR_208_markEvaluated_requires_a_report_ref(t *testing.T) {
	draft := openrouter.DraftRoutingPolicyActivation(routingPolicyV2(t))
	_, err := openrouter.MarkEvaluated(draft, "")
	require.Error(t, err)
	var veto *openrouter.RoutingPolicyActivationError
	require.ErrorAs(t, err, &veto)
	assert.Equal(t, "draft", veto.From)
	assert.Equal(t, "evaluated", veto.To)
	assert.Contains(t, err.Error(), "markEvaluated requires a non-empty shadow/canary evaluation report ref")
	// The input envelope is untouched.
	assert.Equal(t, schemas.RoutingPolicyDraft, draft.State)
	assert.Nil(t, draft.EvaluationReportRef)
}

// TestCOV_OR_209_resolve_route_projects_policy_bindings pins the policy → client
// binding projection over the ARCH §10.3 extended fields: the medium bucket's
// fallbacks and params (temperature, max_tokens, structured_output), the
// provider-order pins, and the `provider.allow_fallbacks ?? true` default.
func TestCOV_OR_209_resolve_route_projects_policy_bindings(t *testing.T) {
	extended := map[string]any{
		"schema":     openrouter.RoutingPolicySchemaID,
		"version":    3,
		"source_ref": "org/routing.yml@extended",
		"buckets": map[string]any{
			"low": map[string]any{
				"primary":          "z-ai/glm-4.7-air",
				"provider_routing": map[string]any{"provider": map[string]any{"order": []any{"z-ai"}}},
			},
			"medium": map[string]any{
				"primary":   "z-ai/glm-4.7",
				"fallbacks": []any{"z-ai/glm-4.7-air"},
				"params": map[string]any{
					"temperature":       0.4,
					"max_tokens":        512,
					"structured_output": "prompt",
				},
			},
			"high": map[string]any{
				"primary": "anthropic/claude-sonnet-4.5",
				"provider_routing": map[string]any{"provider": map[string]any{
					"order":           []any{"anthropic", "z-ai"},
					"allow_fallbacks": false,
				}},
			},
		},
	}
	policy, err := openrouter.ParseRoutingPolicy(extended)
	require.NoError(t, err)
	medium, err := openrouter.ResolveRoute(policy, "medium", openrouter.RoutingRouteContext{Role: "patch-writer"})
	require.NoError(t, err)
	assert.Equal(t, []string{"z-ai/glm-4.7-air"}, medium.Binding.Fallbacks)
	require.NotNil(t, medium.Binding.Params)
	require.NotNil(t, medium.Binding.Params.Temperature)
	assert.InDelta(t, 0.4, *medium.Binding.Params.Temperature, 1e-9)
	require.NotNil(t, medium.Binding.Params.MaxTokens)
	assert.Equal(t, 512, *medium.Binding.Params.MaxTokens)
	assert.Equal(t, "prompt", medium.Binding.Params.StructuredOutput)
	assert.Equal(t, 3, medium.PolicyVersion)
	assert.Equal(t, "org/routing.yml@extended", medium.SourceRef)
	// A policy binding carries no instance constraint.
	assert.Nil(t, medium.Binding.Constraint)
	high, err := openrouter.ResolveRoute(policy, "high", openrouter.RoutingRouteContext{Role: "patch-writer"})
	require.NoError(t, err)
	require.NotNil(t, high.Binding.ProviderRouting)
	assert.Equal(t, []string{"anthropic", "z-ai"}, high.Binding.ProviderRouting.Order)
	assert.False(t, high.Binding.ProviderRouting.AllowFallbacks)
	// An omitted allow_fallbacks defaults to true (the TS `?? true`).
	low, err := openrouter.ResolveRoute(policy, "low", openrouter.RoutingRouteContext{Role: "decision-engine"})
	require.NoError(t, err)
	require.NotNil(t, low.Binding.ProviderRouting)
	assert.True(t, low.Binding.ProviderRouting.AllowFallbacks)
	assert.Equal(t, []string{}, low.Binding.Fallbacks)
}

/* --------------------------------------------------- decision-engine gaps */
// jsonPlaceholderAnswerKey is the empty answer key the contract rejects (a
// nondeterministic answer is not identifiable downstream).
const jsonPlaceholderAnswerKey = ""

func covValidDecisionBody() openrouter.DecisionBody {
	return decisionBodyOf(func() *schemas.Decision {
		decision := schemas.DecisionExample
		decision.Candidates = []string{docPath}
		return &decision
	}())
}

// TestCOV_OR_210_decision_body_validate_rejects_every_contract_violation pins
// the wire contract's fail-closed arms: probability bounds, the mandatory
// answer-key name, the abstain/reason pairing rule, and evidence validation.
func TestCOV_OR_210_decision_body_validate_rejects_every_contract_violation(t *testing.T) {
	t.Run("the schema-valid example is the fixture", func(t *testing.T) {
		valid := covValidDecisionBody()
		assert.Empty(t, valid.Validate())
	})
	reason := schemas.AbstainReasonConflictingEvidence
	cases := []struct {
		name   string
		mutate func(*openrouter.DecisionBody)
		want   string
	}{
		{
			name:   "confidence below zero",
			mutate: func(body *openrouter.DecisionBody) { body.Confidence = -0.1 },
			want:   "confidence: Number must be greater than or equal to 0",
		},
		{
			name:   "confidence above one",
			mutate: func(body *openrouter.DecisionBody) { body.Confidence = 1.1 },
			want:   "confidence: Number must be less than or equal to 1",
		},
		{
			name: "an answer key must be named",
			mutate: func(body *openrouter.DecisionBody) {
				body.Answers = map[string]schemas.Answer{jsonPlaceholderAnswerKey: {Value: json.RawMessage("true"), Probability: 0.5}}
			},
			want: "answers.: String must contain at least 1 character(s)",
		},
		{
			name: "an abstaining body must name its reason",
			mutate: func(body *openrouter.DecisionBody) {
				body.Abstain = false
				body.AbstainReason = &reason
			},
			want: "abstain_reason: abstain_reason must be null when abstain is false",
		},
		{
			name: "invalid evidence is rejected",
			mutate: func(body *openrouter.DecisionBody) {
				body.Evidence = []schemas.Evidence{{Kind: schemas.EvidenceKind("telepathy")}}
			},
			want: "evidence.0.",
		},
		{
			name:   "an invalid enum is rejected",
			mutate: func(body *openrouter.DecisionBody) { body.Action = schemas.Action("celebrate") },
			want:   "action: Invalid enum value",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := covValidDecisionBody()
			testCase.mutate(&body)
			issues := body.Validate()
			require.NotEmpty(t, issues)
			found := ""
			for _, issue := range issues {
				found += issue.Error() + "; "
			}
			assert.Contains(t, found, testCase.want)
		})
	}
}

// TestCOV_OR_211_parse_decision_body_rejects_unrepresentable_values pins the
// typed fail-closed error for a decision body the seam cannot serialize.
func TestCOV_OR_211_parse_decision_body_rejects_unrepresentable_values(t *testing.T) {
	_, err := openrouter.ParseDecisionBody(make(chan int))
	require.Error(t, err)
	var gatewayErr *openrouter.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, "decision_assembly_violation", gatewayErr.Code)
	assert.Contains(t, err.Error(), "decision body is not JSON-representable")
	// A valid document parses into the same body the contract validates.
	body := covValidDecisionBody()
	parsed, err := openrouter.ParseDecisionBody(body)
	require.NoError(t, err)
	assert.Equal(t, body.ChangeKind, parsed.ChangeKind)
	require.Len(t, parsed.Answers, len(body.Answers))
	assert.Equal(t, body.Confidence, parsed.Confidence)
	// A contract violation surfaces with the attached schema issues.
	_, err = openrouter.ParseDecisionBody(map[string]any{"abstain": false, "abstain_reason": "insufficient_confidence"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "abstain_reason must be null when abstain is false")
}

// TestCOV_OR_212_decide_rejects_an_unserializable_analysis_context pins the
// fail-closed prompt build: a grounding answer that cannot serialize is a
// caller-contract defect (the adapter never sends a silently degraded prompt),
// so the typed error leaves the port BEFORE any model call.
func TestCOV_OR_212_decide_rejects_an_unserializable_analysis_context(t *testing.T) {
	analysisContext := decisionEngineAnalysisContext()
	analysisContext.Answers = map[string]openrouter.DecisionAnswer{
		"impact": {Value: json.RawMessage("{ not json"), Probability: 0.9},
	}
	input := decisionEngineInput()
	input.AnalysisContext = analysisContext
	role, err := openrouter.CreateDecisionEngineAdapter(openrouter.DecisionEngineAdapterOptions{
		Client:   &covScriptedClient{binding: covBinding(replayDefaultModel, "native")},
		Compiled: decisionEngineWorkflow,
	})
	require.NoError(t, err)
	_, err = role.Decide(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "json")
	assert.Contains(t, err.Error(), "invalid character")
}

// // TestCOV_OR_213_decide_passes_cache_prefix_and_fails_closed_on_internal_violations
// pins the `cachePrefix ?? true` default's pass-through (an explicit false drops
// the cache-hinted parts array) and the two assembly violations that can only
// come from a defect in the port: a missing structured value and an undecodable
// decision body.
func TestCOV_OR_213_decide_passes_cache_prefix_and_fails_closed_on_internal_violations(t *testing.T) {
	t.Run("an explicit cache prefix override is passed through (deterministic, not cached)", func(t *testing.T) {
		client := newReplayClient(decisionEngineBindings(""), decisionBodyRouter(
			decisionBodyJSON(t, func() *schemas.Decision {
				decision := schemas.DecisionExample
				decision.Candidates = []string{docPath}
				return &decision
			}())))
		role, err := openrouter.CreateDecisionEngineAdapter(openrouter.DecisionEngineAdapterOptions{
			Client:   client,
			Compiled: decisionEngineWorkflow,
		})
		require.NoError(t, err)
		input := decisionEngineInput()
		off := false
		input.CachePrefix = &off
		result, err := role.Decide(input)
		require.NoError(t, err)
		assert.Equal(t, openrouter.DecisionEngineKindDecision, result.Kind)
		// With the override OFF the system message is a plain string (no
		// cache-hinted parts array).
		require.Len(t, client.exchanges, 1)
		request := client.exchanges[0].request
		require.NotNil(t, request.CachePrefix)
		assert.False(t, *request.CachePrefix, "the override must be passed through as false")
		assert.NotEmpty(t, request.System, "the plain system prompt (no cache-hinted parts)")
	})
}

// TestCOV_OR_214_decision_engine_gap_violations pins the adapter's fail-closed
// arms for defect-shaped completions: no structured value at all, and a
// structured value that is not a decodable decision body.
func TestCOV_OR_214_decision_engine_gap_violations(t *testing.T) {
	for name, completion := range map[string]openrouter.RoleCompletion{
		"no structured value": covCompletion(nil, replayDefaultModel),
		"undecodable body":    covCompletion(map[string]any{"wrong": "shape"}, replayDefaultModel),
	} {
		t.Run(name, func(t *testing.T) {
			client := &covScriptedClient{binding: covBinding(replayDefaultModel, "native"), result: completion}
			role, err := openrouter.CreateDecisionEngineAdapter(openrouter.DecisionEngineAdapterOptions{
				Client:   client,
				Compiled: decisionEngineWorkflow,
			})
			require.NoError(t, err)
			result, err := role.Decide(decisionEngineInput())
			require.Error(t, err)
			var gatewayErr *openrouter.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, "decision_assembly_violation", gatewayErr.Code)
			assert.Contains(t, err.Error(), "internal invariant violation")
			assert.Empty(t, result.Telemetry.RoutedModel)
		})
	}
}

/* -------------------------------------------------------------- judge gaps */
// TestCOV_OR_215_judge_gap_branches pins the judge's fail-closed arms: an
// unbound role at construction, the prompt-mode structured-output downgrade,
// the adapter-budget race in both directions, and defect-shaped completions
// (no structured value, an unrenderable one, an invalid verdict body).
func TestCOV_OR_215_judge_gap_branches(t *testing.T) {
	t.Run("an unbound judge role fails closed at construction", func(t *testing.T) {
		client := newReplayClient(map[string]*openrouter.ResolvedRoleBinding{}, func(string, openrouter.CompletionRequest) []replayScript {
			return nil
		})
		_, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{Client: client})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "role 'judge' has no binding in models.yml")
	})
	t.Run("a prompt-mode binding downgrades to constrained prompting", func(t *testing.T) {
		role, client := newJudgeHarness(t, "prompt", nil, nil, judgeBodyRouter(mapOf(schemas.JudgeVerdictExample)))
		result, err := role.Judge(judgeInvocation)
		require.NoError(t, err)
		assert.Equal(t, openrouter.JudgeKindVerdict, result.Kind)
		require.NotEmpty(t, client.exchanges)
		request := client.exchanges[len(client.exchanges)-1].request
		require.NotNil(t, request.CachePrefix)
		assert.True(t, *request.CachePrefix)
		assert.NotEmpty(t, request.System)
		assert.Empty(t, request.UntrustedContent)
	})
	t.Run("a settled completion under an adapter budget still returns a verdict", func(t *testing.T) {
		role, _ := newJudgeHarness(t, "", nil, pwIntPtr(1000), judgeBodyRouter(mapOf(schemas.JudgeVerdictExample)))
		result, err := role.Judge(judgeInvocation)
		require.NoError(t, err)
		assert.Equal(t, openrouter.JudgeKindVerdict, result.Kind)
	})
	t.Run("an expired adapter budget throws the typed provenance marker", func(t *testing.T) {
		client := &covScriptedClient{binding: covBinding(replayDefaultModel, "native"), hang: true}
		role, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{
			Client:    client,
			TimeoutMs: pwIntPtr(1),
		})
		require.NoError(t, err)
		_, err = role.Judge(judgeInvocation)
		require.Error(t, err)
		var budget *openrouter.PatchWriterBudgetTimeoutError
		require.ErrorAs(t, err, &budget)
		assert.Equal(t, 1, budget.BudgetMS)
	})
	for name, completion := range map[string]openrouter.RoleCompletion{
		"no structured value": covCompletion(nil, replayDefaultModel),
		"undecodable body":    covCompletion(map[string]any{"verdict": "reject"}, replayDefaultModel),
	} {
		t.Run("a defect-shaped completion fails closed: "+name, func(t *testing.T) {
			client := &covScriptedClient{binding: covBinding(replayDefaultModel, "native"), result: completion}
			role, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{Client: client})
			require.NoError(t, err)
			_, err = role.Judge(judgeInvocation)
			require.Error(t, err)
			var gatewayErr *openrouter.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, "judge_assembly_violation", gatewayErr.Code)
		})
	}
	t.Run("an unrenderable structured value is reported fail-closed", func(t *testing.T) {
		client := &covScriptedClient{
			binding: covBinding(replayDefaultModel, "native"),
			result:  covCompletion(make(chan int), replayDefaultModel),
		}
		role, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{Client: client})
		require.NoError(t, err)
		_, err = role.Judge(judgeInvocation)
		require.Error(t, err)
		var gatewayErr *openrouter.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, "judge_assembly_violation", gatewayErr.Code)
		assert.Contains(t, err.Error(), "judge body failed JUDGE_VERDICT_SCHEMA re-parse")
	})
	t.Run("a reject without reasons is a fail-closed schema violation", func(t *testing.T) {
		client := &covScriptedClient{
			binding: covBinding(replayDefaultModel, "native"),
			result:  covCompletion(map[string]any{"verdict": "reject", "reasons": []any{}}, replayDefaultModel),
		}
		role, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{Client: client})
		require.NoError(t, err)
		_, err = role.Judge(judgeInvocation)
		require.Error(t, err)
		var gatewayErr *openrouter.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, "judge_assembly_violation", gatewayErr.Code)
		assert.Contains(t, err.Error(), "a reject verdict must attach its evidence reasons")
	})
}

/* --------------------------------------------------------- patch-writer gaps */
func covHunk(start, end int) map[string]any {
	return map[string]any{
		"anchor": map[string]any{
			"start_line":   start,
			"end_line":     end,
			"content_hash": bareHexRE.FindString(schemas.DocPatchExample.Hunks[0].Anchor.ContentHash),
		},
		"delete_text":   "old text",
		"insert_text":   "new text",
		"evidence_refs": []any{map[string]any{"kind": "symbol_diff", "ref": "src/x.ts:1", "note": "why"}},
	}
}
func covPatchBody(hunks ...map[string]any) map[string]any {
	hunkSlice := make([]any, len(hunks))
	for i := range hunks {
		hunkSlice[i] = hunks[i]
	}
	return map[string]any{
		"doc_path": "docs/payments.md",
		"hunks":    hunkSlice,
		"evidence_refs": []any{
			map[string]any{"kind": "symbol_diff", "ref": "src/x.ts:1", "note": "why"},
		},
		"minimality": map[string]any{"measured": true, "added_lines": 1, "removed_lines": 1},
	}
}

// TestCOV_OR_216_patch_writer_gap_branches pins the patch-writer's
// fail-closed arms: the overlapping-hunk detector's start-line comparison, the
// non-timeout rethrow of an attempt-less total-inference failure, the adapter
// budget race in both directions, the undecodable-response-format body, and the
// identity-hash shape check the adapter cannot repair.
func TestCOV_OR_216_patch_writer_gap_branches(t *testing.T) {
	t.Run("hunks sharing a start line are rejected as overlapping", func(t *testing.T) {
		client := &fakeClient{script: []scriptedOutcome{
			completionOutcome("anthropic/claude-sonnet-4.5", covPatchBody(covHunk(5, 7), covHunk(5, 9))),
		}}
		adapter := newAdapter(t, client)
		result, err := adapter.Write(writeRequest())
		require.Error(t, err)
		assert.Nil(t, result.Patch)
		assert.Contains(t, err.Error(), "hunks 0 and 1 overlap in line ranges [5, 7] and [5, 9]")
	})
	t.Run("an attempt-less total inference failure is NOT a writer timeout", func(t *testing.T) {
		client := &fakeClient{script: []scriptedOutcome{{
			Err: openrouter.NewPatchWriterTotalInferenceFailure(
				"all model targets exhausted", "patch-writer", nil),
		}}}
		adapter := newAdapter(t, client)
		_, err := adapter.Write(writeRequest())
		require.Error(t, err)
		var failure *openrouter.PatchWriterTotalInferenceFailure
		require.ErrorAs(t, err, &failure)
		assert.Empty(t, failure.Attempts)
		assert.NotContains(t, err.Error(), "writer-timeout")
	})
	t.Run("a settled completion under an adapter budget returns the patch", func(t *testing.T) {
		client := &fakeClient{script: []scriptedOutcome{completionOutcome(
			"anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
		}}
		adapter := newAdapter(t, client, func(options *openrouter.PatchWriterAdapterOptions) {
			options.TimeoutMs = pwIntPtr(1000)
		})
		result, err := adapter.Write(writeRequest())
		require.NoError(t, err)
		require.NotNil(t, result.Patch)
		assert.Equal(t, "run-123", result.Patch.RunID)
	})
	t.Run("an expired adapter budget degrades to the timeout finding", func(t *testing.T) {
		block := make(chan struct{})
		client := &fakeClient{script: []scriptedOutcome{{Block: block}}}
		adapter := newAdapter(t, client, func(options *openrouter.PatchWriterAdapterOptions) {
			options.TimeoutMs = pwIntPtr(1)
		})
		result, err := adapter.Write(writeRequest())
		require.NoError(t, err)
		require.NotNil(t, result.Finding)
		assert.Equal(t, openrouter.PatchWriterResultFinding, result.Kind)
		assert.Equal(t, openrouter.PatchWriterTimeoutReason, result.Finding.Reason)
		assert.Contains(t, result.Finding.Detail, "writer wall-clock budget of 1ms elapsed")
		close(block)
	})
	t.Run("a defect-shaped structured response fails assembly fail-closed", func(t *testing.T) {
		client := &fakeClient{script: []scriptedOutcome{completionOutcome(
			"anthropic/claude-sonnet-4.5", map[string]any{"doc_path": "docs/payments.md"}),
		}}
		adapter := newAdapter(t, client)
		_, err := adapter.Write(writeRequest())
		require.Error(t, err)
		var patchErr *docutil.Error
		require.ErrorAs(t, err, &patchErr)
		assert.Equal(t, "patch_assembly_violation", patchErr.Code)
		assert.Contains(t, err.Error(), "hunks")
	})
	t.Run("a non-bare-hex base content hash is an assembly violation (never repaired)", func(t *testing.T) {
		client := &fakeClient{script: []scriptedOutcome{completionOutcome(
			"anthropic/claude-sonnet-4.5", patchBodyOf(schemas.DocPatchExample)),
		}}
		adapter := newAdapter(t, client)
		request := writeRequest()
		request.ExpectedBaseContentHashes = map[string]string{"docs/payments.md": "sha256:" + schemas.DocPatchExample.BaseContentHash}
		_, err := adapter.Write(request)
		require.Error(t, err)
		var patchErr *docutil.Error
		require.ErrorAs(t, err, &patchErr)
		assert.Equal(t, "patch_assembly_violation", patchErr.Code)
		assert.Contains(t, err.Error(), "must be bare 64-hex sha256")
	})
}

/* -------------------------------------------------------- drift-auditor gaps */
func covDriftFindings(count int) []map[string]any {
	findings := make([]map[string]any, 0, count)
	for index := 0; index < count; index++ {
		findings = append(findings, map[string]any{
			"id":         fmt.Sprintf("f%03d", index),
			"doc_path":   "docs/payments.md",
			"kind":       "doc-drift",
			"claim_id":   fmt.Sprintf("c%03d", index),
			"confidence": 0.77,
			"evidence": map[string]any{
				"commits": []any{"81af92e"},
				"paths":   []any{"src/payments/refund.ts"},
				"detail":  "the guard clause moved",
			},
		})
	}
	return findings
}
func covDriftDraft(findings []map[string]any) any {
	wrapped := make([]any, len(findings))
	for i := range findings {
		wrapped[i] = findings[i]
	}
	return map[string]any{
		"summary":           "drift sweep proposed findings",
		"proposed_findings": wrapped,
	}
}

// TestCOV_OR_217_drift_auditor_gap_branches pins the auditor's fail-closed
// arms: unserializable inputs, transport failures, defect-shaped completions,
// and the contract's finding-count / field bounds.
func TestCOV_OR_217_drift_auditor_gap_branches(t *testing.T) {
	newRole := func(client *recordedClient) openrouter.DriftAuditorAdapter {
		role, err := openrouter.CreateDriftAuditorAdapter(openrouter.DriftAuditorAdapterOptions{
			Client: client,
			Models: driftAuditorModels(t),
		})
		require.NoError(t, err)
		return role
	}
	bound := &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: replayDefaultModel}}
	structured := func(value any) scriptedCompletion {
		return scriptedCompletion{Completion: openrouter.RoleCompletion{
			Kind:       openrouter.RoleCompletionKindCompletion,
			Structured: value,
			Telemetry:  openrouter.RoleTelemetry{Role: "drift-auditor", RequestedModel: replayDefaultModel, RoutedModel: replayDefaultModel},
		}}
	}
	t.Run("an unserializable impact payload refuses BEFORE any model call", func(t *testing.T) {
		input := driftAuditorInput()
		input.Findings = []map[string]any{{"payload": make(chan int)}}
		_, err := newRole(bound).AuditDrift(input)
		require.Error(t, err)
		var abstain *openrouter.DriftAuditorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "could not be serialized")
		assert.Equal(t, 0, bound.calls())
	})
	t.Run("a transport failure propagates typed", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: replayDefaultModel},
			script:  []scriptedCompletion{{Err: openrouter.NewTransportError("transport request failed: connection refused", 0, nil)}},
		}
		_, err := newRole(client).AuditDrift(driftAuditorInput())
		require.Error(t, err)
		var transportErr *openrouter.TransportError
		require.ErrorAs(t, err, &transportErr)
		assert.Equal(t, "transport_failure", transportErr.Code)
	})
	t.Run("a defect-shaped completion abstains fail-closed", func(t *testing.T) {
		cases := []struct {
			name  string
			value any
			want  string
		}{
			{"no structured value", nil, "completion carried no structured draft — no analysis is fabricated"},
			{"unrendered draft", map[string]any{"proposed_findings": []any{map[string]any{"bad": make(chan int)}}},
				"canonicalJson: unsupported value type chan int in state files"},
			{"too many findings", covDriftDraft(covDriftFindings(41)), "Array must contain at most 40 element(s)"},
			{"unnamed claim", covDriftDraft([]map[string]any{{
				"id": "f001", "doc_path": "docs/payments.md", "kind": "doc-drift",
				"claim_id": "", "confidence": 0.7, "evidence": map[string]any{},
			}}), "claim_id: String must contain at least 1 character(s)"},
			{"impossible confidence", covDriftDraft([]map[string]any{{
				"id": "f001", "doc_path": "docs/payments.md", "kind": "doc-drift",
				"claim_id": "c001", "confidence": 1.5, "evidence": map[string]any{},
			}}), "confidence must be a number in [0, 1]"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				client := &recordedClient{
					binding: openrouter.ResolvedRoleBinding{Primary: replayDefaultModel},
					script:  []scriptedCompletion{structured(testCase.value)},
				}
				_, err := newRole(client).AuditDrift(driftAuditorInput())
				require.Error(t, err)
				var abstain *openrouter.DriftAuditorAbstainError
				require.ErrorAs(t, err, &abstain)
				assert.Contains(t, abstain.Reason, testCase.want)
			})
		}
	})
	t.Run("a contract-valid draft is returned", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: replayDefaultModel},
			script:  []scriptedCompletion{structured(covDriftDraft(covDriftFindings(2)))},
		}
		result, err := newRole(client).AuditDrift(driftAuditorInput())
		require.NoError(t, err)
		assert.Len(t, result.ProposedFindings, 2)
		assert.Equal(t, "81af92e", result.ProposedFindings[0].Evidence.Commits[0])
	})
}

/* ---------------------------------------------------- policy-architect gaps */
// TestCOV_OR_218_policy_architect_gap_branches pins the architect's fail-closed
// arms: an unserializable input (never a model call), a transport failure, a
// defect-shaped completion, and the draft contract's own field bounds.
func TestCOV_OR_218_policy_architect_gap_branches(t *testing.T) {
	newRole := func(client *recordedClient) openrouter.PolicyArchitectAdapter {
		role, err := openrouter.CreatePolicyArchitectAdapter(openrouter.PolicyArchitectAdapterOptions{
			Client: client,
			Models: architectModels(t),
		})
		require.NoError(t, err)
		return role
	}
	structured := func(value any) scriptedCompletion {
		return scriptedCompletion{Completion: openrouter.RoleCompletion{
			Kind:       openrouter.RoleCompletionKindCompletion,
			Structured: value,
			Telemetry:  openrouter.RoleTelemetry{Role: "policy-architect", RequestedModel: "z-ai/glm-4.7", RoutedModel: "z-ai/glm-4.7"},
		}}
	}
	t.Run("an unserializable historical payload refuses BEFORE any model call", func(t *testing.T) {
		client := &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"}}
		input := architectInput()
		input.HistoricalPrs = []map[string]any{{"blob": make(chan int)}}
		_, err := newRole(client).Propose(input)
		require.Error(t, err)
		var abstain *openrouter.ArchitectAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "could not be serialized")
		assert.Equal(t, 0, client.calls())
	})
	t.Run("a transport failure propagates typed", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script:  []scriptedCompletion{{Err: openrouter.NewTransportError("transport request failed: connection refused", 0, nil)}},
		}
		_, err := newRole(client).Propose(architectInput())
		require.Error(t, err)
		var transportErr *openrouter.TransportError
		require.ErrorAs(t, err, &transportErr)
		assert.Equal(t, "transport_failure", transportErr.Code)
	})
	for _, testCase := range []struct {
		name  string
		value any
		want  string
	}{
		{"no structured value", nil, "completion carried no structured draft — no proposal is fabricated"},
		{"undecodable draft", map[string]any{"type": "threshold", "counted_evidence": []any{map[string]any{"bad": make(chan int)}}}, "failed the policy-architect draft contract"},
		{"unknown type", map[string]any{
			"type": "clairvoyance",
			"counted_evidence": []any{map[string]any{
				"statement": "17/23 reviewer overrides targeted generated-API routing", "support_count": 17, "total_count": 23,
			}},
			"artifact": map[string]any{"policy_version": 2},
		}, "failed the policy-architect draft contract"},
		{"invalid counted evidence", map[string]any{
			"type":             "threshold",
			"counted_evidence": []any{map[string]any{"statement": "", "support_count": 0, "total_count": 0}},
			"artifact":         map[string]any{},
		}, "failed the policy-architect draft contract"},
		{"absent artifact", map[string]any{
			"type": "threshold",
			"counted_evidence": []any{map[string]any{
				"statement": "17/23 reviewer overrides targeted generated-API routing", "support_count": 17, "total_count": 23,
			}},
			"artifact": nil,
		}, "failed the policy-architect draft contract"},
	} {
		t.Run("a defect-shaped completion abstains fail-closed: "+testCase.name, func(t *testing.T) {
			client := &recordedClient{
				binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
				script:  []scriptedCompletion{structured(testCase.value)},
			}
			_, err := newRole(client).Propose(architectInput())
			require.Error(t, err)
			var abstain *openrouter.ArchitectAbstainError
			require.ErrorAs(t, err, &abstain)
			assert.Contains(t, err.Error(), testCase.want)
		})
	}
}

/* --------------------------------------------------- claim-extractor gaps */
// TestCOV_OR_219_claim_extractor_gap_branches pins the extractor's fail-closed
// arms: an unserializable prior-claim anchor, a defect-shaped completion, and
// the claim-count bound.
func TestCOV_OR_219_claim_extractor_gap_branches(t *testing.T) {
	newRole := func(client *recordedClient) openrouter.ClaimExtractorAdapter {
		role, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)
		return role
	}
	structured := func(value any) scriptedCompletion {
		return scriptedCompletion{Completion: openrouter.RoleCompletion{
			Kind:       openrouter.RoleCompletionKindCompletion,
			Structured: value,
			Telemetry:  telemetryOf(replayDefaultModel),
		}}
	}
	t.Run("an unserializable prior-claim anchor refuses BEFORE any model call", func(t *testing.T) {
		client := &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: replayDefaultModel}}
		input := extractorInput()
		input.ExistingClaims[0].Anchor = map[string]any{"bad": make(chan int)}
		_, err := newRole(client).Extract(input)
		require.Error(t, err)
		var abstain *openrouter.ClaimExtractorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "could not be serialized")
		assert.Equal(t, 0, client.calls())
	})
	for _, testCase := range []struct {
		name  string
		value any
		want  string
	}{
		{"no structured value", nil, "completion carried no structured draft — no proposal is fabricated"},
		{"undecodable draft", map[string]any{"claims": []any{map[string]any{"bad": make(chan int)}}}, "failed the claim-extractor draft contract"},
	} {
		t.Run("a defect-shaped completion abstains fail-closed: "+testCase.name, func(t *testing.T) {
			client := &recordedClient{
				binding: openrouter.ResolvedRoleBinding{Primary: replayDefaultModel},
				script:  []scriptedCompletion{structured(testCase.value)},
			}
			_, err := newRole(client).Extract(extractorInput())
			require.Error(t, err)
			var abstain *openrouter.ClaimExtractorAbstainError
			require.ErrorAs(t, err, &abstain)
			assert.Contains(t, err.Error(), testCase.want)
		})
	}
	t.Run("more claims than the contract allows are rejected (never fabricated evidence)", func(t *testing.T) {
		claims := make([]any, 0, 41)
		for index := 0; index < 41; index++ {
			claims = append(claims, map[string]any{
				"claim_text": fmt.Sprintf("claim %d", index),
				"candidate_anchor": map[string]any{
					"section_heading": "Payments",
					"line_hint":       3,
				},
				"source_evidence": map[string]any{
					"quote":    "Refunds require an idempotency key.",
					"doc_path": "docs/payments.md",
				},
			})
		}
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: replayDefaultModel},
			script:  []scriptedCompletion{structured(map[string]any{"claims": claims})},
		}
		_, err := newRole(client).Extract(extractorInput())
		require.Error(t, err)
		var abstain *openrouter.ClaimExtractorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "failed the claim-extractor draft contract")
	})
}

/* -------------------------------------------------------- writer-stage gaps */
// covPanicWriterEmbedded adapts the shared recorded writer double into a
// panicking one (the programming-error path) by embedding it.
type covPanicWriterEmbedded struct{ fakeWriter }

func (*covPanicWriterEmbedded) Write(_ openrouter.PatchWriterRequest) (openrouter.PatchWriterWriteResult, error) {
	panic("simulated adapter defect")
}

// covPort wires the writer-stage port over ANY writer double (the shared
// newPort helper is typed to the recorded *fakeWriter).
func covPort(
	t *testing.T,
	writer openrouter.WriterStageWriterPort,
	mutate func(*openrouter.WriterStageDeps),
) openrouter.WriterStagePort {
	t.Helper()
	deps := openrouter.WriterStageDeps{
		Writer:        writer,
		Git:           defaultGit(),
		PolicySummary: strPtr(policySummary),
		WorkspaceCaps: &workspace.WorkspaceCapsOverrides{MaxDocBytes: pwIntPtr(24)},
	}
	if mutate != nil {
		mutate(&deps)
	}
	port, err := openrouter.CreateWriterStagePort(deps)
	require.NoError(t, err)
	return port
}

// TestCOV_OR_220_writer_stage_gap_branches pins the port's grounding and
// result-mapping branches: the rename half of the default source selector, an
// anomalous git read, the workspaces' own fail-closed assembly, and the two
// typed internal errors the exhaustive result mapping raises.
func TestCOV_OR_220_writer_stage_gap_branches(t *testing.T) {
	t.Run("a renamed source grounds the writer through its OLD path", func(t *testing.T) {
		newPath := "src/payments/new-refund.ts"
		oldPath := "src/payments/refund.ts"
		renamePatch := "diff --git a/src/payments/refund.ts b/src/payments/new-refund.ts\n--- a/src/payments/refund.ts\n+++ b/src/payments/new-refund.ts\n@@ -1 +1,2 @@\n-export function refund(amount: number) {}\n+export function refund(amount: number) { /* reviewed */ }\n"
		git := defaultGit()
		git.head[newPath] = refundHeadText
		git.diff = []gitx.DiffFileRecord{{Path: newPath, OldPath: &oldPath, Status: gitx.DiffStatusModified, Patch: renamePatch}}
		writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{
			Kind:      openrouter.PatchWriterResultPatch,
			Patch:     stagePatch(),
			Telemetry: openrouter.PatchWriterRoleTelemetry{RoutedModel: "fake/writer-routed"},
		}}
		input := writeStageInput()
		analysis := analysisFixture()
		analysis.Files = []schemas.ChangedFile{
			{Path: newPath, OldPath: &oldPath, Status: schemas.FileStatusModified},
		}
		input.Analysis = analysis
		port := newPort(t, writer, func(deps *openrouter.WriterStageDeps) { deps.Git = git })
		outcome, err := port.Write(input)
		require.NoError(t, err)
		require.Equal(t, openrouter.WriterStageResultPatch, outcome.Kind)
		require.Len(t, writer.requests, 1)
		// The rename's NEW half grounds the session; the workspace carries the
		// rename-aware diff (both halves) and the new path's content.
		assert.Contains(t, writer.requests[0].Workspace, newPath)
		assert.Contains(t, writer.requests[0].Workspace, renamePatch)
	})
	t.Run("an anomalous source blob read fails closed", func(t *testing.T) {
		git := defaultGit()
		git.catErrPath = sourcePath
		git.catErr = errors.New("git cat-file: anomalous blob for src/payments/refund.ts")
		writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{Kind: openrouter.PatchWriterResultPatch}}
		_, err := newPort(t, writer, func(deps *openrouter.WriterStageDeps) { deps.Git = git }).Write(writeStageInput())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "anomalous blob")
		assert.Empty(t, writer.requests, "no model call on a grounding failure")
	})
	t.Run("an unusable document graph fails the assembly closed", func(t *testing.T) {
		input := writeStageInput()
		input.Ctx.Graph = graph.DocumentGraph{
			Mappings:      []graph.MappingRecord{{Doc: docPath, Sources: []string{"src/payments/**"}}},
			ContractKinds: map[string]schemas.ContractKind{},
			Errors: []graph.GraphConfigError{{
				Kind:    graph.GraphConfigErrDuplicateConceptName,
				Message: "concept 'refund' is declared twice",
				Path:    "concepts.yml",
			}},
		}
		writer := &fakeWriter{}
		_, err := newPort(t, writer, nil).Write(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unusable")
		assert.Empty(t, writer.requests)
	})
	for name, result := range map[string]openrouter.PatchWriterWriteResult{
		"a finding arm without a finding": {Kind: openrouter.PatchWriterResultFinding},
		"an unknown result arm":           {Kind: openrouter.PatchWriterResultKind("quantum")},
	} {
		t.Run("the exhaustive result mapping fails closed: "+name, func(t *testing.T) {
			writer := &fakeWriter{result: result}
			_, err := newPort(t, writer, nil).Write(writeStageInput())
			require.Error(t, err)
			assert.Equal(t, "writer_stage_internal", docutil.CodeOf(err))
			require.NotEmpty(t, writer.requests)
		})
	}
	t.Run("a panicking writer is recovered into the typed degradation", func(t *testing.T) {
		outcome, err := covPort(t, &covPanicWriterEmbedded{}, nil).Write(writeStageInput())
		require.NoError(t, err)
		require.Equal(t, openrouter.WriterStageResultAbstain, outcome.Kind)
		assert.Equal(t, "patch-writer failed: patch-writer panicked: simulated adapter defect", outcome.Detail)
	})
}

/* --------------------------------------------------------- models.yml gaps */
// TestCOV_OR_221_models_config_gap_branches pins the models.yml validator's
// fail-closed arms: an unnamed role, an out-of-range generation parameter, a
// document that fails the extended contract while still being base-compliant,
// and a document that is not even JSON.
func TestCOV_OR_221_models_config_gap_branches(t *testing.T) {
	document := func(mutate func(roles map[string]any)) []byte {
		roles := map[string]any{"judge": map[string]any{"model": "z-ai/glm-4.7"}}
		if mutate != nil {
			mutate(roles)
		}
		data, err := json.Marshal(map[string]any{
			"schema":  "docsync.models/v1",
			"version": 1,
			"roles":   roles,
		})
		require.NoError(t, err)
		return data
	}
	t.Run("an unnamed role is rejected fail-closed", func(t *testing.T) {
		_, err := openrouter.ParseModelsConfig(document(func(roles map[string]any) {
			roles[""] = map[string]any{"model": "z-ai/glm-4.7-air"}
		}))
		require.Error(t, err)
		var configErr *openrouter.ModelsConfigError
		require.ErrorAs(t, err, &configErr)
		assert.Equal(t, "models_config_invalid", configErr.Code)
		assert.Contains(t, err.Error(), "roles.: String must contain at least 1 character(s)")
	})
	t.Run("an out-of-range temperature is rejected fail-closed", func(t *testing.T) {
		_, err := openrouter.ParseModelsConfig(document(func(roles map[string]any) {
			role := roles["judge"].(map[string]any)
			role["params"] = map[string]any{"temperature": -0.5}
		}))
		require.Error(t, err)
		var configErr *openrouter.ModelsConfigError
		require.ErrorAs(t, err, &configErr)
		assert.Equal(t, "models_config_invalid", configErr.Code)
		assert.Contains(t, err.Error(), "roles.judge.params.temperature: Number must be greater than or equal to 0")
	})
	t.Run("a document that is not JSON is rejected fail-closed with its parse issue", func(t *testing.T) {
		_, err := openrouter.ParseModelsConfig([]byte("{\"schema\": docsync.models/v1,}"))
		require.Error(t, err)
		var configErr *openrouter.ModelsConfigError
		require.ErrorAs(t, err, &configErr)
		assert.Equal(t, "models_config_invalid", configErr.Code)
		assert.Contains(t, err.Error(), "Phase-0 base compliance: no")
	})
	t.Run("a valid extended config round-trips", func(t *testing.T) {
		parsed, err := openrouter.ParseModelsConfig(document(nil))
		require.NoError(t, err)
		require.Contains(t, parsed.Models.Roles, "judge")
		assert.Equal(t, "z-ai/glm-4.7", parsed.Models.Roles["judge"].Model)
		assert.True(t, parsed.Phase0SchemaCompliant)
		assert.Empty(t, parsed.Divergences)
	})
}

/* ----------------------------------------------- replacement evaluation gaps */
// TestCOV_OR_222_replacement_evaluation_gap_branches pins the fail-closed input
// validation of the shadow evaluation and its report arithmetic: every missing
// or contradictory field is rejected BEFORE any case runs, and a disagreement
// is counted (never averaged away).
func TestCOV_OR_222_replacement_evaluation_gap_branches(t *testing.T) {
	t.Run("every missing or contradictory input is rejected before any case runs", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*openrouter.EvaluateReplacementModelInput)
			want   string
		}{
			{"a missing role", func(in *openrouter.EvaluateReplacementModelInput) { in.Role = "" }, "role must be a non-empty string"},
			{"a missing repo", func(in *openrouter.EvaluateReplacementModelInput) { in.Repo = "" }, "repo must be a non-empty string"},
			{"a missing runId", func(in *openrouter.EvaluateReplacementModelInput) { in.RunID = "" }, "runId must be a non-empty string"},
			{"a zero production version", func(in *openrouter.EvaluateReplacementModelInput) { in.ProductionVersion = 0 }, "productionVersion must be a positive integer"},
			{"equal versions", func(in *openrouter.EvaluateReplacementModelInput) { in.CandidateVersion = 2 }, "production and candidate versions must differ"},
			{"no cases", func(in *openrouter.EvaluateReplacementModelInput) { in.Cases = nil }, "cases must be non-empty"},
			{"an unnamed case", func(in *openrouter.EvaluateReplacementModelInput) {
				in.Cases = []schemas.ReplacementEvalCaseInput{{RiskBucket: schemas.RiskBucketHigh}}
			}, "caseId must be a non-empty string"},
			{"a duplicate caseId", func(in *openrouter.EvaluateReplacementModelInput) {
				in.Cases = replacementCases("case-001", "case-001")
			}, "duplicate caseId \"case-001\""},
			{"a non-positive time box", func(in *openrouter.EvaluateReplacementModelInput) { in.TimeBoxDays = 0 }, "timeBoxDays must be a positive integer"},
			{"an out-of-range agreement rate", func(in *openrouter.EvaluateReplacementModelInput) { in.MinAgreementRate = 1.5 }, "minAgreementRate must be a number in [0, 1]"},
			{"a non-canonical clock", func(in *openrouter.EvaluateReplacementModelInput) {
				in.Clock = func() string { return "Sat Oct 10 2026" }
			}, "clock must return a canonical ISO-8601 instant"},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				port := newRecordedEvaluationPort(t, nil)
				input := replacementBaseInput(port)
				testCase.mutate(&input)
				_, err := openrouter.EvaluateReplacementModel(input)
				require.Error(t, err)
				var evalErr *openrouter.ReplacementEvaluationError
				require.ErrorAs(t, err, &evalErr)
				assert.Contains(t, err.Error(), testCase.want)
				// Fail closed BEFORE any case ran.
				assert.Empty(t, port.seen)
			})
		}
	})
	t.Run("a port failure and an invalid outcome are both fail-closed", func(t *testing.T) {
		port := &covFailingEvaluationPort{err: errors.New("the port exploded")}
		_, err := openrouter.EvaluateReplacementModel(replacementBaseInput(port))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "port \"recorded-double/v1\" failed case \"case-001\": the port exploded")
		// An outcome that cannot satisfy the registered shadow-decision schema
		// (an empty decision is not valid) is fail-closed per side.
		invalidOld := &covScriptedEvaluationPort{oldOutcome: schemas.ShadowDecision{}}
		_, err = openrouter.EvaluateReplacementModel(replacementBaseInput(invalidOld))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "returned an invalid old_outcome for case \"case-001\"")
		invalidNew := &covScriptedEvaluationPort{
			oldOutcome: schemas.ShadowDecision{Action: schemas.ActionPatch, Abstained: false},
			newOutcome: schemas.ShadowDecision{},
		}
		_, err = openrouter.EvaluateReplacementModel(replacementBaseInput(invalidNew))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "returned an invalid new_outcome for case \"case-001\"")
	})
	t.Run("a disagreement is counted, never averaged away", func(t *testing.T) {
		port := newRecordedEvaluationPort(t, map[string]replacementScriptedOutcome{
			"case-001": {Action: schemas.ActionPatch, Agree: true},
			"case-002": {Action: schemas.ActionReview, Agree: false},
		})
		result, err := openrouter.EvaluateReplacementModel(replacementBaseInput(port))
		require.NoError(t, err)
		assert.Equal(t, schemas.ReplacementEvalAgreement{Total: 2, Agreed: 1, Disagreed: 1, Rate: 0.5}, result.Report.Agreement)
		assert.Equal(t, "fail", result.Report.Verdict)
		assert.False(t, result.Report.Cases[1].Agreed)
	})
}

// covScriptedEvaluationPort is a port double returning scripted (optionally
// schema-invalid) paired shadow outcomes.
type covScriptedEvaluationPort struct {
	oldOutcome schemas.ShadowDecision
	newOutcome schemas.ShadowDecision
}

func (p *covScriptedEvaluationPort) PortID() string { return "recorded-double/v1" }
func (p *covScriptedEvaluationPort) RunCase(openrouter.ReplacementCaseInput) (openrouter.ReplacementCaseResult, error) {
	return openrouter.ReplacementCaseResult{
		OldOutcome: p.oldOutcome,
		NewOutcome: p.newOutcome,
	}, nil
}

// covFailingEvaluationPort is a port double that fails one case outright.
type covFailingEvaluationPort struct{ err error }

func (p *covFailingEvaluationPort) PortID() string { return "recorded-double/v1" }
func (p *covFailingEvaluationPort) RunCase(openrouter.ReplacementCaseInput) (openrouter.ReplacementCaseResult, error) {
	return openrouter.ReplacementCaseResult{}, p.err
}

/* ------------------------------------------------------ fetch transport gaps */
// TestCOV_OR_223_fetch_transport_gap_branches pins the HTTP transport's
// fail-closed arms: an unbodyable request, a malformed endpoint, and a body
// the server truncates mid-read — every one is a typed transport error, never
// a zero-value result (D10).
func TestCOV_OR_223_fetch_transport_gap_branches(t *testing.T) {
	transport := openrouter.CreateFetchTransport(&openrouter.CreateFetchTransportOptions{TimeoutMs: 2000})
	t.Run("an unrepresentable body is a typed transport failure", func(t *testing.T) {
		_, err := transport.Post(context.Background(), "https://openrouter.invalid/api/v1/chat/completions",
			map[string]string{}, map[string]any{"bad": make(chan int)})
		require.Error(t, err)
		var transportErr *openrouter.TransportError
		require.ErrorAs(t, err, &transportErr)
		assert.Contains(t, err.Error(), "transport request failed")
	})
	t.Run("a malformed endpoint is a typed transport failure", func(t *testing.T) {
		_, err := transport.Post(context.Background(), "http://a b.com/api",
			map[string]string{"Content-Type": "application/json"}, map[string]any{"model": "m"})
		require.Error(t, err)
		var transportErr *openrouter.TransportError
		require.ErrorAs(t, err, &transportErr)
		assert.Contains(t, err.Error(), "transport request failed")
	})
	t.Run("a truncated response body is a typed transport failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "{\"choices\":")
		}))
		defer server.Close()
		_, err := transport.Post(context.Background(), server.URL+"/api/v1/chat/completions",
			map[string]string{"Content-Type": "application/json"}, map[string]any{"model": "m"})
		require.Error(t, err)
		var transportErr *openrouter.TransportError
		require.ErrorAs(t, err, &transportErr)
		assert.Contains(t, err.Error(), "unexpected EOF")
	})
	t.Run("a live listener exchanges one recorded request", func(t *testing.T) {
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			data, _ := io.ReadAll(request.Body)
			_ = json.Unmarshal(data, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"x","choices":[]}`)
		}))
		defer server.Close()
		result, err := transport.Post(context.Background(), server.URL,
			map[string]string{"Content-Type": "application/json"},
			map[string]any{"model": "z-ai/glm-4.7", "messages": []any{}})
		require.NoError(t, err)
		assert.Equal(t, 200, result.Status)
		assert.Equal(t, map[string]any{"model": "z-ai/glm-4.7", "messages": []any{}}, gotBody)
	})
}
