// Package githubapp is the Go port of @docsync/github-app (Phase 8) — the
// production AI agent that listens for GitHub pull_request / check_run / push
// webhooks, verifies their signatures, renders a check-run lifecycle, and
// (disabled until the human escalation lands) patches docs behind an
// aggregated docsync/* PR. The check-run surface replaces a chat surface
// (ARCH §12.2, D10): models propose; humans promote.
//
// Ported from packages/github-app/src/{index,manifest,app-webhooks}.ts and the
// webhook/ + checks/ surfaces. SURFACES, in decompose order (CONVERSATIONS
// P8 CONVERSATION 05; FEATURIZATION P8 SURFACES):
//
//   - manifest (manifest.go): the GitHub App permission table + events with the
//     signup asserts (never request write hooks).
//   - webhooks (webhooks.go): the fail-closed webhook admission core — HMAC
//     SHA-256 signature verification (verify.ts) + typed routing + self-trigger
//     suppression (receiver.ts).
//   - checks (checks.go): the aggregated check-run surface — upsert service,
//     pipeline check handler, escalation handler, fallback guidance.
//   - routes (routes.go): the HTTP admission seam that binds net/http to the
//     receiver (the TS package has no routes/ dir — routing lives in the
//     receiver; this is the Go inbound adapter).
//   - app (app.go): the composition unit wiring the receiver to the injected
//     check/escalation runtimes.
//
// Ports (injectable seams — no ambient network/clock/fs/log in logic, P19):
// the REST/transport (internal/gitpr), the check-run client (upsert-service +
// pipeline-check-handler), the secrets resolver (credentialsFor), the logger,
// and the check/escalation RUNTIMES (app.go) are all injected so tests stay
// network-free (OD-2). The composed pipeline runtime behind the check/escalation
// seams is internal/cli (TODO(githubapp/cli)); this package declares the seams
// and does NOT import internal/cli (import DAG, plan.md §3).
//
// Fail-closed posture (D7/D8/D10/D16): invalid signature → 401-class reject and
// no handler; unexpected event → 200 README ACK and no handler; missing handler
// → 202 SKIP; everything degrades to less automation, never more.
package githubapp
