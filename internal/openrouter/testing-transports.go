package openrouter

// Ported from packages/openrouter/src/testing/transports.ts (P2-T24 part 1) —
// the recorded/replay/failing transport DOUBLES: scripted HTTP exchanges with
// zero network (OD-2: no network-dependent unit tests). "The TS suite drove
// the REAL OpenRouterClient over recorded transports ... the Go suite drives
// the adapter over a recorded CLIENT double" holds here too — these doubles
// stand in for the wire, never for the client's retry/corrective logic.
//
// REplay rule (AGENT-behavior.md; the phase-6 verifier flake, fixed by commit
// 4c68f4b): doubles shared across CONCURRENT code paths MUST key matches by
// REQUEST IDENTITY and dispatch per call — never positional FIFO, because
// completion-order consumption cross-wires fixtures and manifests as a
// load-dependent flake. The TS createReplayTransport popped responses in
// order; this port dispatches by request identity instead:
//
//   - every full exchange fixture carries its RecordedRequest, whose identity
//     is (by default) the content hash of its canonical JSON body — i.e.
//     "identate input" → identical key (docutil.ContentHash), exactly what
//     the rule asks for;
//   - a request whose identity matches no fixture, with no bare-response
//     fixture left, fails LOUD (ReplayExhaustedError) instead of feeding the
//     caller the wrong record;
//   - the recording transport appends per call (guarded by a mutex), so
//     concurrent callers cannot lose an exchange.
//
// The handle exposes what was consumed: Requests returns the captured
// requests in completion order and Unmatched returns the fixtures no request
// ever claimed — a well-formed script uses every fixture, so an unexpected
// empty Unmatched is the loud signal the rule demands.

import (
	"context"
	"strconv"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// RecordedRequest is one recorded request: the URL, a COPY of the headers (TS
// `{...headers}` — the double must never alias the caller's map) and the JSON
// body.
type RecordedRequest struct {
	URL     string
	Headers map[string]string
	Body    any
}

// RecordedExchange is one recorded exchange: the request plus the response it
// received (TS RecordedExchange).
type RecordedExchange struct {
	Request  RecordedRequest
	Response TransportExchangeResult
}

// TransportFailure is the scripted failure union (TS TransportFailure): an
// HTTP failure that still returns (kind "status") or a network-level failure
// that throws (kind "network").
type TransportFailure struct {
	// Kind is "status" or "network".
	Kind string
	// Status is the HTTP status (kind "status"; the default body is
	// `{ error: { message: 'scripted HTTP <status> failure', code: <status> } }`).
	Status int
	// JSON is the custom response body (kind "status"; nil → the default body).
	JSON any
	// Message is the TransportError message (kind "network"; empty → the
	// default 'scripted network failure').
	Message string
}

// Transport failure kinds (TS TransportFailure union tags).
const (
	TransportFailureStatus  = "status"
	TransportFailureNetwork = "network"
)

// StatusFailure builds an HTTP-status failure with the default error body.
func StatusFailure(status int) TransportFailure {
	return TransportFailure{Kind: TransportFailureStatus, Status: status}
}

// StatusFailureWithJSON builds an HTTP-status failure with a custom body.
func StatusFailureWithJSON(status int, body any) TransportFailure {
	return TransportFailure{Kind: TransportFailureStatus, Status: status, JSON: body}
}

// NetworkFailure builds a network-level failure (thrown as TransportError;
// empty message → the default 'scripted network failure').
func NetworkFailure(message string) TransportFailure {
	return TransportFailure{Kind: TransportFailureNetwork, Message: message}
}

/* -------------------------------------------------------- scripted responses */

// ScriptedUsage mirrors the OpenRouter usage block (TS ScriptedUsage).
type ScriptedUsage struct {
	// Pointers mirror the optional TS fields (nil == absent).
	PromptTokens     *int
	CompletionTokens *int
	TotalTokens      *int
	Cost             *float64
	CachedTokens     *int
}

// ScriptedCompletion shapes one chat-completion response (TS
// ScriptedCompletion).
type ScriptedCompletion struct {
	// Model is the response-body `model` field — the ACTUALLY ROUTED id when a
	// routing lane served it (V-P2-11); nil == absent.
	Model *string
	// Content is the model text.
	Content string
	// Provider is the response-body `provider` field; nil == absent.
	Provider *string
	// Usage is the usage block; nil == absent.
	Usage *ScriptedUsage
}

// CompletionResponse fabricates a chat-completion response body (TS
// completionResponse).
func CompletionResponse(script ScriptedCompletion) TransportExchangeResult {
	body := map[string]any{
		"id":      "scripted-completion",
		"choices": []any{map[string]any{"index": float64(0), "message": map[string]any{"role": "assistant", "content": script.Content}, "finish_reason": "stop"}},
	}
	if script.Model != nil {
		body["model"] = *script.Model
	}
	if script.Provider != nil {
		body["provider"] = *script.Provider
	}
	if script.Usage != nil {
		// Numbers are emitted as JSON numbers (float64) — exactly what a real
		// response body decodes to, so downstream parsing sees identical types.
		usage := map[string]any{"prompt_tokens": float64(0), "completion_tokens": float64(0)}
		if script.Usage.PromptTokens != nil {
			usage["prompt_tokens"] = float64(*script.Usage.PromptTokens)
		}
		if script.Usage.CompletionTokens != nil {
			usage["completion_tokens"] = float64(*script.Usage.CompletionTokens)
		}
		if script.Usage.TotalTokens != nil {
			usage["total_tokens"] = float64(*script.Usage.TotalTokens)
		}
		if script.Usage.Cost != nil {
			usage["cost"] = *script.Usage.Cost
		}
		if script.Usage.CachedTokens != nil {
			usage["prompt_tokens_details"] = map[string]any{"cached_tokens": float64(*script.Usage.CachedTokens)}
		}
		body["usage"] = usage
	}
	return TransportExchangeResult{Status: 200, JSON: body}
}

// ErrorResponse fabricates an HTTP error response body (TS errorResponse).
func ErrorResponse(status int, message string) TransportExchangeResult {
	return TransportExchangeResult{Status: status, JSON: map[string]any{
		"error": map[string]any{"message": message, "code": status},
	}}
}

// JSONCompletion fabricates a completion whose content is a JSON-serialized
// value (TS jsonCompletion — how structured-output tests feed a response).
func JSONCompletion(model *string, value any) TransportExchangeResult {
	return CompletionResponse(ScriptedCompletion{Model: model, Content: jsonString(value)})
}

/* ---------------------------------------------------------- the recording one */

// RecordingTransportHandle wraps a transport and records every exchange (TS
// createRecordingTransport's handle).
type RecordingTransportHandle struct {
	// Transport is the wrapped double.
	Transport OpenRouterTransport

	mu        sync.Mutex
	exchanges []RecordedExchange
}

// CreateRecordingTransport wraps inner and records every exchange it makes
// (TS createRecordingTransport). The inner transport's outcome (response or
// throw) is passed through unchanged; the exchange is recorded after the
// response arrives.
func CreateRecordingTransport(inner OpenRouterTransport) *RecordingTransportHandle {
	handle := &RecordingTransportHandle{}
	handle.Transport = transportFunc(func(url string, headers map[string]string, body any) (TransportExchangeResult, error) {
		result, err := inner.Post(context.Background(), url, headers, body)
		if err == nil {
			handle.mu.Lock()
			handle.exchanges = append(handle.exchanges, RecordedExchange{
				Request:  RecordedRequest{URL: url, Headers: copyHeaders(headers), Body: body},
				Response: result,
			})
			handle.mu.Unlock()
		}
		return result, err
	})
	return handle
}

// Exchanges returns the recorded exchanges (TS `exchanges` — the append-only
// record; concurrent callers append under a mutex, so completion order is the
// only guarantee and callers must NOT depend on FIFO matching).
func (h *RecordingTransportHandle) Exchanges() []RecordedExchange {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RecordedExchange, len(h.exchanges))
	copy(out, h.exchanges)
	return out
}

// Requests returns just the recorded requests (TS `handle.requests` pattern).
func (h *RecordingTransportHandle) Requests() []RecordedRequest {
	exchanges := h.Exchanges()
	out := make([]RecordedRequest, 0, len(exchanges))
	for _, exchange := range exchanges {
		out = append(out, exchange.Request)
	}
	return out
}

/* ------------------------------------------------------------- the replay one */

// ReplayFixture is one replayed exchange (TS ReplayFixture = RecordedExchange |
// TransportExchangeResult): a full exchange fixture carries its request (the
// identity it matches by); a bare-response fixture carries none (a keyless
// fallback consumed by any request that matches no keyed fixture).
type ReplayFixture struct {
	Request  *RecordedRequest
	Response TransportExchangeResult
}

// RecordedExchangeFixture builds a keyed fixture from a recorded exchange.
func RecordedExchangeFixture(exchange RecordedExchange) ReplayFixture {
	request := exchange.Request
	return ReplayFixture{Request: &request, Response: exchange.Response}
}

// ResponseFixture builds a bare-response fixture (no request identity).
func ResponseFixture(response TransportExchangeResult) ReplayFixture {
	return ReplayFixture{Response: response}
}

// ReplayTransportHandle is the keyed replay double (TS createReplayTransport,
// dispatched by request identity per the AGENT-behavior replay rule).
type ReplayTransportHandle struct {
	// Transport is the double itself.
	Transport OpenRouterTransport

	key      func(request RecordedRequest) string
	mu       sync.Mutex
	keyed    map[string][]ReplayFixture
	bare     []ReplayFixture
	consumed int
	requests []RecordedRequest
}

// ReplayKeyFunc derives the dispatch key of one request. The default is the
// content hash of the canonical JSON body (identical input → identical key).
type ReplayKeyFunc func(request RecordedRequest) string

// CreateReplayTransport builds the keyed replay double. Requests are matched
// by identity; a request that matches nothing (with no bare-response fixture
// left) fails loud, and exhausting the script is a typed ReplayExhaustedError
// naming how many fixtures were consumed.
func CreateReplayTransport(fixtures []ReplayFixture, key ...ReplayKeyFunc) *ReplayTransportHandle {
	handle := &ReplayTransportHandle{
		keyed: make(map[string][]ReplayFixture, len(fixtures)),
	}
	if len(key) > 0 && key[0] != nil {
		handle.key = key[0]
	} else {
		handle.key = defaultReplayKey
	}
	for _, fixture := range fixtures {
		if fixture.Request == nil {
			handle.bare = append(handle.bare, fixture)
			continue
		}
		identity := handle.key(*fixture.Request)
		handle.keyed[identity] = append(handle.keyed[identity], fixture)
	}
	handle.Transport = transportFunc(func(url string, headers map[string]string, body any) (TransportExchangeResult, error) {
		request := RecordedRequest{URL: url, Headers: copyHeaders(headers), Body: body}
		handle.mu.Lock()
		defer handle.mu.Unlock()
		handle.requests = append(handle.requests, request)
		identity := handle.key(request)
		if queue, ok := handle.keyed[identity]; ok && len(queue) > 0 {
			handle.keyed[identity] = queue[1:]
			handle.consumed++
			return queue[0].Response, nil
		}
		if len(handle.bare) > 0 {
			fixture := handle.bare[0]
			handle.bare = handle.bare[1:]
			handle.consumed++
			return fixture.Response, nil
		}
		return TransportExchangeResult{}, NewReplayExhaustedError(
			"replay transport exhausted after " + strconv.Itoa(handle.consumed) +
				" recorded exchange(s) — no fixture matches request identity " + identity +
				" (keyed dispatch by request identity, never positional FIFO)")
	})
	return handle
}

// Requests returns the captured requests (completion order).
func (h *ReplayTransportHandle) Requests() []RecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RecordedRequest, len(h.requests))
	copy(out, h.requests)
	return out
}

// Unmatched returns the fixtures no request ever claimed. An empty result
// means the script was fully consumed — the loud signal the AGENT-behavior
// replay rule asks for (a leftover fixture means an expected call never
// happened).
func (h *ReplayTransportHandle) Unmatched() []ReplayFixture {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ReplayFixture, 0, len(h.bare))
	out = append(out, h.bare...)
	for _, queue := range h.keyed {
		out = append(out, queue...)
	}
	return out
}

// defaultReplayKey is the identity of a request: the content hash of its
// canonical JSON body, namespaced by URL (docutil.ContentHash — identical
// input hashes to the same key).
func defaultReplayKey(request RecordedRequest) string {
	canonical, err := docutil.CanonicalJSON(request.Body)
	if err != nil {
		canonical = jsonString(request.Body)
	}
	return docutil.ContentHash(request.URL + "\x00" + canonical)
}

/* ---------------------------------------------------------- the failing one */

// FailingTransportHandle throws transport-level failures from a scripted
// sequence (TS createFailingTransport's handle): each call consumes the next
// scripted failure (recorded BEFORE it applies), every scripted response is
// returned, and the script's exhaustion is a typed ReplayExhaustedError.
type FailingTransportHandle struct {
	// Transport is the double itself.
	Transport OpenRouterTransport

	mu       sync.Mutex
	index    int
	requests []RecordedRequest
}

// CreateFailingTransport builds the scripted-failure double (TS
// createFailingTransport).
func CreateFailingTransport(failures []TransportFailure) *FailingTransportHandle {
	handle := &FailingTransportHandle{}
	handle.Transport = transportFunc(func(url string, headers map[string]string, body any) (TransportExchangeResult, error) {
		handle.mu.Lock()
		defer handle.mu.Unlock()
		handle.requests = append(handle.requests, RecordedRequest{
			URL:     url,
			Headers: copyHeaders(headers),
			Body:    body,
		})
		if handle.index >= len(failures) {
			return TransportExchangeResult{}, NewReplayExhaustedError(
				"failing transport exhausted after " + strconv.Itoa(len(failures)) +
					" scripted failure(s)")
		}
		failure := failures[handle.index]
		handle.index++
		if failure.Kind == TransportFailureNetwork {
			message := failure.Message
			if message == "" {
				message = "scripted network failure"
			}
			return TransportExchangeResult{}, NewTransportError(message, 0, nil)
		}
		if failure.JSON != nil {
			return TransportExchangeResult{Status: failure.Status, JSON: failure.JSON}, nil
		}
		return ErrorResponse(
			failure.Status,
			"scripted HTTP "+strconv.Itoa(failure.Status)+" failure",
		), nil
	})
	return handle
}

// Requests returns the captured requests (recorded BEFORE the failure applies).
func (h *FailingTransportHandle) Requests() []RecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RecordedRequest, len(h.requests))
	copy(out, h.requests)
	return out
}

/* ------------------------------------------------------------------ plumbing */

// transportFunc adapts a function to the OpenRouterTransport seam (the landed
// transport.go interface). Post's context is the caller's: the doubles are
// synchronous scripts and never block on it.
type transportFunc func(url string, headers map[string]string, body any) (TransportExchangeResult, error)

// Post implements OpenRouterTransport.
func (f transportFunc) Post(_ context.Context, url string, headers map[string]string, body any) (TransportExchangeResult, error) {
	return f(url, headers, body)
}

// copyHeaders returns a defensive copy (TS `{...headers}`) so a double can
// never alias or observe later mutations of the caller's header map.
func copyHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		out[name] = value
	}
	return out
}
