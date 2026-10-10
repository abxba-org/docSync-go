// Command docsync-app is the GitHub App entrypoint: a thin main that composes
// the App webhook handler and serves it over HTTP (the routes surface), applying
// the fail-closed webhook contract (verify → suppress → route → dispatch).
//
// PORT seam (internal/cli): the aggregated check runtime (and the escalation
// patch runtime) are internal/cli composition (TODO(githubapp/cli)). Until they
// land, the check runtime is a fail-closed stub — pull_request deliveries fail
// closed (500) rather than silently reporting a check with no pipeline; the
// signature verification, routing, self-trigger suppression, and escalation
// idempotency all work now.
package main

import (
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/DUDE616/docSync-go/internal/githubapp"
)

// errCheckRuntimeUnwired is the fail-closed stub error for the un-wired check
// runtime (internal/cli seam).
var errCheckRuntimeUnwired = errors.New(
	"app check runtime not wired (internal/cli seam, TODO(githubapp/cli)) — fail closed")

// failClosedCheckRuntime is the placeholder AppCheckRuntime until the composed
// pipeline runtime lands: it fails closed (never reports a false pass).
type failClosedCheckRuntime struct{}

func (failClosedCheckRuntime) HandlePullRequest(githubapp.AppCheckInput) (githubapp.AppCheckResult, error) {
	return githubapp.AppCheckResult{}, errCheckRuntimeUnwired
}

func main() {
	secret := os.Getenv("APP_WEBHOOK_SECRET")
	appIdentity := os.Getenv("APP_IDENTITY")
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
		AppCheckRuntime: failClosedCheckRuntime{},
		AppIdentity:     appIdentity,
		Secret:          secret,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", githubapp.ServeAppWebhook(app))

	log.Printf("docsync-app: webhook surface listening on :%s at /webhook (fail-closed until internal/cli lands)", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("docsync-app: server failed: %v", err)
	}
}
