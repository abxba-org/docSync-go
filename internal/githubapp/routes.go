// Ported as the Go "routes" surface for the GitHub App inbound HTTP seam.
//
// The TS @docsync/github-app package has NO routes/ directory — webhook
// routing/dispatch lives entirely in webhook/receiver.ts (verify → parse →
// route). The live server that fronts it (an Express/http listener) is NOT in
// that package either; this file is the Go inbound adapter that binds
// net/http to the receiver: extract the GitHub webhook headers
// (X-GitHub-Event / X-GitHub-Delivery / X-Hub-Signature-256) plus the raw body,
// hand them to the receiver, and mirror the result's HTTP status.
//
// No business logic lives here — it is the transport edge; the admission
// contract (verify → suppress → route → dispatch) is the receiver (webhooks.go).

package githubapp

import (
	"encoding/json"
	"io"
	"net/http"
)

// The GitHub App webhook header names (X-Hub-Signature-256 is the HMAC header).
const (
	headerGithubEvent    = "X-GitHub-Event"
	headerGithubDelivery = "X-GitHub-Delivery"
	headerHubSignature   = "X-Hub-Signature-256"
)

// webhookResponse is the JSON body ServeWebhook writes (the flattened receiver
// result).
type webhookResponse struct {
	Kind   string `json:"kind"`
	Event  string `json:"event,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ServeWebhook adapts the WebhookReceiver to an http.HandlerFunc: it reads the
// inbound webhook, dispatches through the fail-closed receiver, and mirrors the
// DeliveryResult.HTTPStatus. A handler error (a routed handler that throws)
// surfaces as 500.
func ServeWebhook(receiver *WebhookReceiver) http.HandlerFunc {
	return serveHandleWebhook(func(body string, headers WebhookHeaders) (WebhookResult, error) {
		return receiver.HandleWebhook(body, headers)
	})
}

// ServeAppWebhook adapts the wired AppWebhookHandler to an http.HandlerFunc
// (cmd/docsync-app mounts this).
func ServeAppWebhook(app *AppWebhookHandler) http.HandlerFunc {
	return serveHandleWebhook(func(body string, headers WebhookHeaders) (WebhookResult, error) {
		return app.HandleWebhook(body, headers)
	})
}

func serveHandleWebhook(
	handle func(body string, headers WebhookHeaders) (WebhookResult, error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "unable to read request body", http.StatusBadRequest)
			return
		}
		result, handlerErr := handle(string(body), WebhookHeaders{
			Event:     r.Header.Get(headerGithubEvent),
			Delivery:  r.Header.Get(headerGithubDelivery),
			Signature: r.Header.Get(headerHubSignature),
		})
		if handlerErr != nil {
			http.Error(w, "webhook handler failure", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(result.HTTPStatus)
		_ = json.NewEncoder(w).Encode(webhookResponse{
			Kind: string(result.Kind), Event: result.Event, Reason: result.Reason,
		})
	}
}
