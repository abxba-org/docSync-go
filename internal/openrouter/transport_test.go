package openrouter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Port of packages/openrouter/test — transport seam (transport.ts): the real
// net/http implementation against httptest servers (loopback only — no external
// network, OD-2).

// orStaticDouble is an injected transport double proving the interface is
// satisfiable outside the package (compile-time + runtime check).
type orStaticDouble struct {
	result openrouter.TransportExchangeResult
	err    error
	calls  int
}

func (d *orStaticDouble) Post(_ context.Context, _ string, _ map[string]string, _ any) (openrouter.TransportExchangeResult, error) {
	d.calls++
	return d.result, d.err
}

func TestOpenRouterChatCompletionsURLConstant(t *testing.T) {
	assert.Equal(t, "https://openrouter.ai/api/v1/chat/completions", openrouter.OpenRouterChatCompletionsURL)
}

func TestTransportInterfaceIsSatisfiedByDoubles(t *testing.T) {
	var transport openrouter.OpenRouterTransport = &orStaticDouble{
		result: openrouter.TransportExchangeResult{Status: 200, JSON: map[string]any{"ok": true}},
	}
	result, err := transport.Post(context.Background(), "url", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 200, result.Status)
}

func TestFetchTransportPostsJSONAndParsesResponse(t *testing.T) {
	var gotMethod, gotContentType, gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":3}}`))
	}))
	defer server.Close()

	transport := openrouter.CreateFetchTransport(nil)
	body := map[string]any{"model": "z-ai/glm-4.7", "messages": []openrouter.ChatMessage{{Role: "user", Content: "u"}}, "temperature": float64(0)}
	result, err := transport.Post(context.Background(), server.URL, map[string]string{
		"Authorization": "Bearer sk-or-v1-test-key-abc123def456",
		"Content-Type":  "application/json",
	}, body)
	require.NoError(t, err)
	assert.Equal(t, 200, result.Status)
	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "Bearer sk-or-v1-test-key-abc123def456", gotAuth)
	assert.JSONEq(t, `{"model":"z-ai/glm-4.7","messages":[{"role":"user","content":"u"}],"temperature":0}`, gotBody)
	parsed, ok := result.JSON.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "x", parsed["id"])
	choices, ok := parsed["choices"].([]any)
	require.True(t, ok)
	require.Len(t, choices, 1)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	assert.Equal(t, "hi", message["content"])
}

func TestFetchTransportReturnsErrorStatusesWithoutThrowing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","code":429}}`))
	}))
	defer server.Close()

	transport := openrouter.CreateFetchTransport(&openrouter.CreateFetchTransportOptions{TimeoutMs: 5_000})
	result, err := transport.Post(context.Background(), server.URL, nil, map[string]any{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, 429, result.Status)
	errorObj := result.JSON.(map[string]any)["error"].(map[string]any)
	assert.Equal(t, "rate limited", errorObj["message"])
}

func TestFetchTransportNonJSONBodyIsTypedTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer server.Close()

	transport := openrouter.CreateFetchTransport(nil)
	_, err := transport.Post(context.Background(), server.URL, nil, map[string]any{})
	require.Error(t, err)
	var transportErr *openrouter.TransportError
	require.True(t, errors.As(err, &transportErr))
	assert.Equal(t, "response body is not JSON (status 200)", transportErr.Message)
	assert.Equal(t, 200, transportErr.Status)
	assert.Equal(t, openrouter.CodeTransportFailure, transportErr.Code)
}

func TestFetchTransportNetworkFailureIsTypedTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening anymore

	transport := openrouter.CreateFetchTransport(nil)
	_, err := transport.Post(context.Background(), url, nil, map[string]any{})
	require.Error(t, err)
	var transportErr *openrouter.TransportError
	require.True(t, errors.As(err, &transportErr))
	assert.Contains(t, transportErr.Message, "transport request failed: ")
	assert.Equal(t, 0, transportErr.Status)
}

func TestFetchTransportTimeoutIsTypedTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transport := openrouter.CreateFetchTransport(&openrouter.CreateFetchTransportOptions{TimeoutMs: 30})
	_, err := transport.Post(context.Background(), server.URL, nil, map[string]any{})
	require.Error(t, err)
	var transportErr *openrouter.TransportError
	require.True(t, errors.As(err, &transportErr))
	assert.Contains(t, transportErr.Message, "transport request failed: ")
}

func TestCreateFetchTransportDefaultsToSixtySecondBudget(t *testing.T) {
	// Default construction must serve a request fine (the 60s default budget is
	// never hit by a loopback double).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	transport := openrouter.CreateFetchTransport(nil)
	result, err := transport.Post(context.Background(), server.URL, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"ok": true}, result.JSON)
}
