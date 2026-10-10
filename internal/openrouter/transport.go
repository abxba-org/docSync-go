package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Transport seam (ARCH §10.1): ALL network access of the model-access layer
// lives behind OpenRouterTransport. Unit tests inject doubles — the real
// fetch implementation (CreateFetchTransport) is the only place that touches
// the network (OD-2: no network-dependent unit tests).

// TransportExchangeResult is a completed HTTP exchange — status plus the parsed
// JSON body (whatever it is) (TS TransportExchangeResult).
type TransportExchangeResult struct {
	// Status is the HTTP status code of the exchange.
	Status int `json:"status"`
	// JSON is the parsed response body.
	JSON any `json:"json"`
}

// OpenRouterTransport is the injectable HTTP boundary. Post returns for every
// completed exchange (2xx-5xx alike) and returns a typed *TransportError for
// network-level failures (connection, timeout, non-JSON body).
type OpenRouterTransport interface {
	Post(ctx context.Context, url string, headers map[string]string, body any) (TransportExchangeResult, error)
}

// OpenRouterChatCompletionsURL is the single OpenRouter endpoint this layer
// talks to (chat completions).
const OpenRouterChatCompletionsURL = "https://openrouter.ai/api/v1/chat/completions"

// CreateFetchTransportOptions configures the real transport.
type CreateFetchTransportOptions struct {
	// TimeoutMs is the per-request budget; the request is aborted past it
	// (default DefaultTimeoutMs = 60_000).
	TimeoutMs int
}

// fetchTransport is the real transport over net/http. The per-request budget is
// enforced by the http.Client timeout (the Go form of the TS AbortController);
// abort and other network failures surface as *TransportError so the client can
// apply its bounded-retry policy uniformly.
type fetchTransport struct {
	client *http.Client
}

// CreateFetchTransport builds the real transport over net/http.
func CreateFetchTransport(options *CreateFetchTransportOptions) OpenRouterTransport {
	timeoutMs := DefaultTimeoutMs
	if options != nil && options.TimeoutMs > 0 {
		timeoutMs = options.TimeoutMs
	}
	return &fetchTransport{
		client: &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond},
	}
}

// Post performs one HTTP exchange (TS createFetchTransport's post).
func (t *fetchTransport) Post(
	ctx context.Context,
	url string,
	headers map[string]string,
	body any,
) (TransportExchangeResult, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return TransportExchangeResult{}, newFetchTransportError(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return TransportExchangeResult{}, newFetchTransportError(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := t.client.Do(request)
	if err != nil {
		return TransportExchangeResult{}, newFetchTransportError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return TransportExchangeResult{}, newFetchTransportError(err)
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// TS: `response body is not JSON (status ${response.status})`.
		return TransportExchangeResult{}, NewTransportError(
			fmt.Sprintf("response body is not JSON (status %d)", response.StatusCode),
			response.StatusCode,
			err,
		)
	}
	return TransportExchangeResult{Status: response.StatusCode, JSON: parsed}, nil
}

// newFetchTransportError wraps a network-level failure with the TS message
// format: `transport request failed: ${message}`.
func newFetchTransportError(cause error) *TransportError {
	return NewTransportError(fmt.Sprintf("transport request failed: %s", cause.Error()), 0, cause)
}
