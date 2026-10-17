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

// osExit is the process-exit seam; main() routes through it so the entrypoint
// logic can be exercised in-process without terminating the test binary.
var osExit = os.Exit

// buildWebhookMux assembles the webhook mux (signature verification, routing,
// fail-closed check runtime). Extracted so it can be tested without starting an
// HTTP server.
func buildWebhookMux() *http.ServeMux {
	secret := os.Getenv("APP_WEBHOOK_SECRET")
	appIdentity := os.Getenv("APP_IDENTITY")

	app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
		AppCheckRuntime: failClosedCheckRuntime{},
		AppIdentity:     appIdentity,
		Secret:          secret,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", githubapp.ServeAppWebhook(app))
	return mux
}

// serve starts srv and blocks until it is closed; returns 0 on graceful close,
// 1 on a real listener failure.
func serve(srv *http.Server) int {
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("docsync-app: server failed: %v", err)
		return 1
	}
	return 0
}

// run executes the app entrypoint logic and returns the process exit code.
// main() exists only to translate that into os.Exit so the logic can be tested
// in-process without terminating the test binary.
func run() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	mux := buildWebhookMux()
	srv := &http.Server{Addr: ":" + port, Handler: mux}
	return serve(srv)
}

func main() {
	osExit(run())
}
