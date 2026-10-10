package openrouter_test

// testing-transports_test.go — Go port of packages/openrouter/src/testing/
// transports.ts (P2-T24 part 1): the recorded/replay/failing transport
// doubles. Every double is scripted and synchronous — zero network (OD-2).
//
// The point of this suite is the AGENT-behavior replay rule: replay dispatch
// keys by REQUEST IDENTITY and fails loud on unexpected identities, never a
// positional FIFO pop (the phase-6 verifier flake, fixed by 4c68f4b). The
// Go port of createReplayTransport therefore keys fixtures by the canonical
// content hash of each request body and proves it here by replaying fixtures
// whose identity order differs from the call order.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
)

// replyTransport returns a fixed response for every call (the scripted inner
// transport of the recording double).
type replyTransport struct {
	status int
	body   any
}

func (t *replyTransport) Post(context.Context, string, map[string]string, any) (openrouter.TransportExchangeResult, error) {
	return openrouter.TransportExchangeResult{Status: t.status, JSON: t.body}, nil
}

// ackTransport returns the request's own body, so the caller can prove what
// the double actually observed.
type ackTransport struct{ seen []any }

func (t *ackTransport) Post(_ context.Context, _ string, _ map[string]string, body any) (openrouter.TransportExchangeResult, error) {
	t.seen = append(t.seen, body)
	return openrouter.TransportExchangeResult{Status: 200, JSON: body}, nil
}

func TestCreateRecordingTransport(t *testing.T) {
	t.Run("records every exchange after the response arrives", func(t *testing.T) {
		inner := &replyTransport{status: 200, body: map[string]any{"ok": true}}
		handle := openrouter.CreateRecordingTransport(inner)

		result, err := handle.Transport.Post(context.Background(), "https://openrouter.ai/api/v1/chat/completions",
			map[string]string{"authorization": "Bearer sk-or-v1-test"}, map[string]any{"model": "z-ai/glm-4.7"})
		require.NoError(t, err)
		assert.Equal(t, 200, result.Status)

		exchanges := handle.Exchanges()
		require.Len(t, exchanges, 1)
		assert.Equal(t, "https://openrouter.ai/api/v1/chat/completions", exchanges[0].Request.URL)
		assert.Equal(t, map[string]any{"model": "z-ai/glm-4.7"}, exchanges[0].Request.Body)
		assert.Equal(t, 200, exchanges[0].Response.Status)
		assert.Equal(t, map[string]any{"ok": true}, exchanges[0].Response.JSON)
	})

	t.Run("the recorded headers are a COPY — the caller's map can mutate freely", func(t *testing.T) {
		inner := &replyTransport{status: 200, body: map[string]any{"ok": true}}
		handle := openrouter.CreateRecordingTransport(inner)
		headers := map[string]string{"x-one": "1"}
		_, err := handle.Transport.Post(context.Background(), "https://openrouter.ai", headers, "body")
		require.NoError(t, err)
		// Mutating the caller's map afterwards must not rewrite the record.
		headers["x-one"] = "mutated"
		headers["x-two"] = "2"
		exchanges := handle.Exchanges()
		require.Len(t, exchanges, 1)
		assert.Equal(t, map[string]string{"x-one": "1"}, exchanges[0].Request.Headers)
	})

	t.Run("an inner failure passes through and records nothing", func(t *testing.T) {
		failing := openrouter.CreateFailingTransport([]openrouter.TransportFailure{
			openrouter.NetworkFailure(""),
		})
		handle := openrouter.CreateRecordingTransport(failing.Transport)
		_, err := handle.Transport.Post(context.Background(), "https://openrouter.ai", nil, "body")
		require.Error(t, err)
		var transportErr *openrouter.TransportError
		require.ErrorAs(t, err, &transportErr)
		assert.Equal(t, openrouter.CodeTransportFailure, transportErr.Code)
		// Message carries the TS text verbatim (Error() renders the coded
		// wrapper, EDOCSYNC_MODEL: <message>, per errors.go).
		assert.Equal(t, "scripted network failure", transportErr.Message)
		assert.Empty(t, handle.Exchanges())
	})
}

func TestCreateReplayTransport(t *testing.T) {
	t.Run("dispatches by REQUEST IDENTITY, never positional order", func(t *testing.T) {
		first := openrouter.CompletionResponse(openrouter.ScriptedCompletion{
			Model:   scriptedModel("z-ai/glm-4.7"),
			Content: "first",
		})
		second := openrouter.CompletionResponse(openrouter.ScriptedCompletion{
			Model:   scriptedModel("anthropic/claude-sonnet-4.5"),
			Content: "second",
		})
		firstRequest := openrouter.RecordedRequest{URL: url, Body: map[string]any{"case": "one"}}
		secondRequest := openrouter.RecordedRequest{URL: url, Body: map[string]any{"case": "two"}}

		handle := openrouter.CreateReplayTransport([]openrouter.ReplayFixture{
			openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{Request: firstRequest, Response: first}),
			openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{Request: secondRequest, Response: second}),
		})

		// Call them in the OPPOSITE order of the fixtures: identity dispatch
		// (not FIFO) must still pair each response with its own request.
		gotSecond, err := handle.Transport.Post(context.Background(), url, nil, secondRequest.Body)
		require.NoError(t, err)
		assert.Equal(t, "second", completionContent(t, gotSecond))
		gotFirst, err := handle.Transport.Post(context.Background(), url, nil, firstRequest.Body)
		require.NoError(t, err)
		assert.Equal(t, "first", completionContent(t, gotFirst))

		// The script is fully consumed.
		assert.Empty(t, handle.Unmatched())
		require.Len(t, handle.Requests(), 2)

		// Any further call fails loud instead of re-serving a record.
		_, err = handle.Transport.Post(context.Background(), url, nil, secondRequest.Body)
		require.Error(t, err)
		var exhausted *openrouter.ReplayExhaustedError
		require.ErrorAs(t, err, &exhausted)
		assert.Equal(t, openrouter.CodeReplayExhausted, exhausted.Code)
		assert.Contains(t, err.Error(), "exhausted after 2 recorded exchange(s)")
	})

	t.Run("a repeated identity consumes the next queued fixture (keyed FIFO per identity)", func(t *testing.T) {
		retryA := openrouter.JSONCompletion(scriptedModel("z-ai/glm-4.7"), map[string]any{"attempt": 1})
		retryB := openrouter.JSONCompletion(scriptedModel("z-ai/glm-4.7"), map[string]any{"attempt": 2})
		body := map[string]any{"same": "identity"}
		request := openrouter.RecordedRequest{URL: url, Body: body}
		handle := openrouter.CreateReplayTransport([]openrouter.ReplayFixture{
			openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{Request: request, Response: retryA}),
			openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{Request: request, Response: retryB}),
			// A bare-response fixture: no request identity, consumed by any
			// request that matches no keyed fixture.
			openrouter.ResponseFixture(openrouter.JSONCompletion(scriptedModel("z-ai/glm-4.7"), map[string]any{"wildcard": true})),
		})

		first, err := handle.Transport.Post(context.Background(), url, nil, body)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"attempt": float64(1)}, jsonCompletionValue(t, first))
		second, err := handle.Transport.Post(context.Background(), url, nil, body)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"attempt": float64(2)}, jsonCompletionValue(t, second))
		wildcard, err := handle.Transport.Post(context.Background(), url, nil, map[string]any{"unkeyed": true})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"wildcard": true}, jsonCompletionValue(t, wildcard))
		assert.Empty(t, handle.Unmatched())
	})

	t.Run("an unexpected identity fails loud and is never fed another fixture", func(t *testing.T) {
		recorded := openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{
			Request:  openrouter.RecordedRequest{URL: url, Body: map[string]any{"expected": true}},
			Response: openrouter.CompletionResponse(openrouter.ScriptedCompletion{Content: "expected"}),
		})
		unmatched := openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{
			Request:  openrouter.RecordedRequest{URL: url, Body: map[string]any{"never-called": true}},
			Response: openrouter.CompletionResponse(openrouter.ScriptedCompletion{Content: "never"}),
		})
		handle := openrouter.CreateReplayTransport([]openrouter.ReplayFixture{recorded, unmatched})

		_, err := handle.Transport.Post(context.Background(), url, nil, map[string]any{"different": true})
		require.Error(t, err)
		var exhausted *openrouter.ReplayExhaustedError
		require.ErrorAs(t, err, &exhausted)
		assert.Contains(t, err.Error(), "no fixture matches request identity")
		assert.Contains(t, err.Error(), "never positional FIFO")

		// The loud failure kept every fixture unconsumed, and the request that
		// triggered it is still captured (recording happens before dispatch).
		assert.Len(t, handle.Unmatched(), 2)
		require.Len(t, handle.Requests(), 1)
		assert.Equal(t, map[string]any{"different": true}, handle.Requests()[0].Body)
	})

	t.Run("a caller-supplied key overrides the default identity derivation", func(t *testing.T) {
		keyedA := openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{
			Request:  openrouter.RecordedRequest{URL: url, Body: map[string]any{"id": "a"}},
			Response: openrouter.CompletionResponse(openrouter.ScriptedCompletion{Content: "A"}),
		})
		keyedB := openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{
			Request:  openrouter.RecordedRequest{URL: url, Body: map[string]any{"id": "b"}},
			Response: openrouter.CompletionResponse(openrouter.ScriptedCompletion{Content: "B"}),
		})
		handle := openrouter.CreateReplayTransport([]openrouter.ReplayFixture{keyedA, keyedB},
			func(request openrouter.RecordedRequest) string {
				id, _ := request.Body.(map[string]any)
				value, _ := id["id"].(string)
				return value
			})

		got, err := handle.Transport.Post(context.Background(), url, nil, map[string]any{"id": "b"})
		require.NoError(t, err)
		assert.Equal(t, "B", completionContent(t, got))
		// The key I did not ask for is still recorded as unmatched.
		unmatched := handle.Unmatched()
		require.Len(t, unmatched, 1)
		assert.Equal(t, "A", completionContent(t, unmatched[0].Response))
	})

	t.Run("the default identity is the canonical content hash of the body", func(t *testing.T) {
		body := map[string]any{"model": "z-ai/glm-4.7", "messages": []any{"hi"}}
		// Two structurally IDENTICAL requests (different key order) hash to the
		// same identity, so one recorded fixture serves them both — the
		// derivation is docutil.ContentHash(url ⊕ NUL ⊕ canonical body).
		fixture := openrouter.RecordedExchangeFixture(openrouter.RecordedExchange{
			Request:  openrouter.RecordedRequest{URL: url, Body: body},
			Response: openrouter.CompletionResponse(openrouter.ScriptedCompletion{Content: "identical"}),
		})
		handle := openrouter.CreateReplayTransport([]openrouter.ReplayFixture{fixture})
		got, err := handle.Transport.Post(context.Background(), url, nil, map[string]any{
			"messages": []any{"hi"},
			"model":    "z-ai/glm-4.7",
		})
		require.NoError(t, err)
		assert.Equal(t, "identical", completionContent(t, got))
		assert.Empty(t, handle.Unmatched())
		// A different URL namespaces the identity: the same body under another
		// endpoint matches nothing.
		_, err = handle.Transport.Post(context.Background(), "https://other.example/v1/chat", nil, body)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no fixture matches request identity")
	})
}

func TestCreateFailingTransport(t *testing.T) {
	t.Run("a network failure throws a TransportError carrying the TS message", func(t *testing.T) {
		handle := openrouter.CreateFailingTransport([]openrouter.TransportFailure{
			openrouter.NetworkFailure("connection refused"),
			openrouter.NetworkFailure(""),
		})
		_, err := handle.Transport.Post(context.Background(), url, nil, "body")
		require.Error(t, err)
		var transportErr *openrouter.TransportError
		require.ErrorAs(t, err, &transportErr)
		assert.Equal(t, "transport_failure", transportErr.Code)
		// Message carries the TS text verbatim (Error() renders the coded
		// wrapper, EDOCSYNC_MODEL: <message>, per errors.go).
		assert.Equal(t, "connection refused", transportErr.Message)

		// The default network message is preserved verbatim.
		_, err = handle.Transport.Post(context.Background(), url, nil, "body")
		require.Error(t, err)
		require.ErrorAs(t, err, &transportErr)
		assert.Equal(t, "scripted network failure", transportErr.Message)
	})

	t.Run("an HTTP failure RETURNS (with the default error body)", func(t *testing.T) {
		handle := openrouter.CreateFailingTransport([]openrouter.TransportFailure{
			openrouter.StatusFailure(429),
			openrouter.StatusFailureWithJSON(500, map[string]any{"custom": true}),
		})
		result, err := handle.Transport.Post(context.Background(), url, nil, "body")
		require.NoError(t, err)
		assert.Equal(t, 429, result.Status)
		assert.Equal(t, map[string]any{
			"error": map[string]any{"message": "scripted HTTP 429 failure", "code": 429},
		}, result.JSON)

		result, err = handle.Transport.Post(context.Background(), url, nil, "body")
		require.NoError(t, err)
		assert.Equal(t, 500, result.Status)
		assert.Equal(t, map[string]any{"custom": true}, result.JSON)
	})

	t.Run("the request is captured BEFORE the failure applies", func(t *testing.T) {
		handle := openrouter.CreateFailingTransport([]openrouter.TransportFailure{openrouter.NetworkFailure("boom")})
		_, err := handle.Transport.Post(context.Background(), "https://openrouter.ai/api/v1/chat/completions",
			map[string]string{"x-test": "1"}, map[string]any{"payload": true})
		require.Error(t, err)
		requests := handle.Requests()
		require.Len(t, requests, 1)
		assert.Equal(t, map[string]any{"payload": true}, requests[0].Body)
		assert.Equal(t, map[string]string{"x-test": "1"}, requests[0].Headers)
	})

	t.Run("running out of scripted failures fails loud with a typed error", func(t *testing.T) {
		handle := openrouter.CreateFailingTransport([]openrouter.TransportFailure{openrouter.StatusFailure(500)})
		_, err := handle.Transport.Post(context.Background(), url, nil, "body")
		require.NoError(t, err)
		_, err = handle.Transport.Post(context.Background(), url, nil, "body")
		require.Error(t, err)
		var exhausted *openrouter.ReplayExhaustedError
		require.ErrorAs(t, err, &exhausted)
		assert.Contains(t, err.Error(), "failing transport exhausted after 1 scripted failure(s)")
	})
}

func TestScriptedResponses(t *testing.T) {
	t.Run("completionResponse shapes an OpenRouter chat-completion body", func(t *testing.T) {
		prompt, completion, total, cost, cached := 11, 7, 18, 0.0021, 5
		result := openrouter.CompletionResponse(openrouter.ScriptedCompletion{
			Model:    scriptedModel("z-ai/glm-4.7-fallback"),
			Provider: scriptedModel("z-ai"),
			Content:  "hello",
			Usage: &openrouter.ScriptedUsage{
				PromptTokens:     &prompt,
				CompletionTokens: &completion,
				TotalTokens:      &total,
				Cost:             &cost,
				CachedTokens:     &cached,
			},
		})
		assert.Equal(t, 200, result.Status)
		body, ok := result.JSON.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "scripted-completion", body["id"])
		assert.Equal(t, "z-ai/glm-4.7-fallback", body["model"])
		assert.Equal(t, "z-ai", body["provider"])
		choices, ok := body["choices"].([]any)
		require.True(t, ok)
		require.Len(t, choices, 1)
		choice := choices[0].(map[string]any)
		assert.Equal(t, float64(0), choice["index"])
		assert.Equal(t, "stop", choice["finish_reason"])
		message := choice["message"].(map[string]any)
		assert.Equal(t, "assistant", message["role"])
		assert.Equal(t, "hello", message["content"])
		usage := body["usage"].(map[string]any)
		assert.Equal(t, float64(11), usage["prompt_tokens"])
		assert.Equal(t, float64(7), usage["completion_tokens"])
		assert.Equal(t, float64(18), usage["total_tokens"])
		assert.InDelta(t, 0.0021, usage["cost"], 1e-12)
		details := usage["prompt_tokens_details"].(map[string]any)
		assert.Equal(t, float64(5), details["cached_tokens"])
	})

	t.Run("completionResponse omits absent optional fields", func(t *testing.T) {
		result := openrouter.CompletionResponse(openrouter.ScriptedCompletion{Content: "bare"})
		body := result.JSON.(map[string]any)
		assert.NotContains(t, body, "model")
		assert.NotContains(t, body, "provider")
		assert.NotContains(t, body, "usage")
	})

	t.Run("errorResponse shapes an OpenRouter error body", func(t *testing.T) {
		result := openrouter.ErrorResponse(418, "teapot")
		assert.Equal(t, 418, result.Status)
		assert.Equal(t, map[string]any{"error": map[string]any{"message": "teapot", "code": 418}}, result.JSON)
	})

	t.Run("jsonCompletion feeds structured-output responses", func(t *testing.T) {
		result := openrouter.JSONCompletion(scriptedModel("z-ai/glm-4.7"), map[string]any{"claims": []any{}})
		body := result.JSON.(map[string]any)
		assert.Equal(t, "z-ai/glm-4.7", body["model"])
		choices := body["choices"].([]any)
		message := choices[0].(map[string]any)["message"].(map[string]any)
		assert.Equal(t, `{"claims":[]}`, message["content"])
	})
}

/* ------------------------------------------------------------------ helpers */

const url = "https://openrouter.ai/api/v1/chat/completions"

func scriptedModel(value string) *string { return &value }

// completionContent extracts the scripted assistant text from a completion
// response body.
func completionContent(t *testing.T, result openrouter.TransportExchangeResult) string {
	t.Helper()
	body, ok := result.JSON.(map[string]any)
	require.True(t, ok)
	choices, ok := body["choices"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, choices)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := message["content"].(string)
	return content
}

// jsonCompletionValue decodes a jsonCompletion content value.
func jsonCompletionValue(t *testing.T, result openrouter.TransportExchangeResult) map[string]any {
	t.Helper()
	body, ok := result.JSON.(map[string]any)
	require.True(t, ok)
	choices, ok := body["choices"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, choices)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	content, ok := message["content"].(string)
	require.True(t, ok)
	var value map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &value))
	return value
}

// compile-time proof that the doubles satisfy the landed transport seam.
var (
	_ openrouter.OpenRouterTransport = (*replyTransport)(nil)
	_ openrouter.OpenRouterTransport = (*ackTransport)(nil)
)
