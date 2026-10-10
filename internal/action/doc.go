// Package action is the Go port of @docsync/action (Phase 8) — the GitHub
// Action wrapper that runs the docSync check in CI, reports check-run results,
// and degrades fail-closed (exit 2/3 must never be misreported as "no drift").
//
// Ported from packages/action/src/{index,event,runner,main}.ts. SURFACES, in
// compose order (P8-T15..T17): action.yml inputs (P8-T15) are mapped onto the
// runner env by Main; typed event parsing (event.go) plus the runner
// (runner.go) carry exit-code/conclusion mapping; Main (main.go) is the
// executable entry that reads the event payload, composes the default
// CheckRunPort, writes the outputs protocol, and sets the process exit code.
//
// Ports (injectable seams — no ambient network/clock/fs in logic):
//
//   - Runner (runner.go): the injected CheckRunPort seam over env. Pure
//     orchestration. A port that throws, or a runtime-inconclusive result,
//     fails closed to exit 3 / neutral (D14). Malformed events/payloads fail
//     closed to exit 2 / failure (D8). Fork PRs force report-only (A7).
//   - Event (event.go): pure typed parsing of the GitHub Actions event payload
//     plus the GITHUB_* environment into an ActionContext. Fail-closed: a
//     malformed payload/environment raises ActionEventError, never "no drift".
//   - Main (main.go): the executable entry. Env (INPUT_*), the event-payload
//     read, the outputs append, stderr, and the process exit code are all
//     injectable deps so tests are network-free (OD-2). The default port
//     composition binds the runner seam to the composed pipeline runtime
//     (port.go) — a SEAM over internal/cli, which is being written
//     concurrently (TODO(docsync-action/cli)); the runner and event surfaces
//     carry no internal/cli dependency (import DAG, plan.md §3).
//
// Exit-code table (internal/orchestrator/exit-codes.go; ARCH §12.1):
// 0 no drift · 1 drift · 2 config error (fail-closed before any AI stage, D8)
// · 3 runtime failure (also fail-closed, D14). Check-run conclusion mapping
// lives in internal/orchestrator/check-conclusion.go (D10 neutral-default
// advisory posture; conclusion advisory-neutral by default).
package action
