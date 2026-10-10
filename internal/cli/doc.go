// Package cli is the Go port of the docSync command-line surface
// (TS packages/cli/src — the "composed-application library"; ARCH §7, PLAN
// Phase 7). It is the highest layer of the import DAG: it wires the landed
// deterministic core packages (orchestrator, analysis, graph, schemas,
// validate/layout, gitx, patch, gitpr, openrouter, policy, verifier, docutil)
// into the four verbs `check` / `fix` / `pr` / `init` (and the advisory
// `audit`), and maps every run onto the binding exit-code table:
//
//	0  no drift detected
//	1  drift detected (candidates found)
//	2  configuration/manifest error — fail-closed BEFORE any AI stage (D8)
//	3  runtime failure — fail-closed (D14)
//
// Exit codes are binding (AGENTS.md §5; ARCH §12.1). `docsync check` is
// read-only and produces ZERO patches when the manifest is unreadable
// (V-P0-7); `docsync fix` / `docsync pr` run the full engine; `docsync init`
// scaffolds a minimal valid `.docsync/`.
//
// File-by-file TS provenance (packages/cli/src → internal/cli):
//
//	args.ts               → args.go
//	exit-codes.ts         → exit_codes.go   (numbers re-exported from
//	                                          internal/orchestrator, ARCH §12.1)
//	io.ts                 → io.go
//	git-status.ts         → git_status.go
//	compose.ts            → compose.go
//	bin.ts                → cmd.go          (runCli dispatcher)
//	commands/check.ts     → commands_check.go
//	commands/fix.ts       → commands_fix.go (+ fix/flow.ts, fix/capture.ts,
//	                                          fix/record-store.ts folded in)
//	commands/pr.ts        → commands_pr.go  (+ pr/flow.ts folded in)
//	commands/init.ts      → commands_init.go
//	commands/audit.ts    → commands_audit.go
//
// Declared migration seams (orchestrator to rebind — see the unit report):
//   - The model-backed PipelinePorts (OpenRouter decision/writer adapters,
//     policy bridge, three-tier verifier port) are wired through the
//     ModelPortsBuilderImpl seam in compose.go; the audit loop-B engine through
//     AuditRunnerImpl; and the core doc-PR flow through PrFlowRunnerImpl. An
//     ONLINE run / audit / pr fails closed (D14) until the seam is bound; the
//     deterministic offline path and every config/exit lane work without it.
//     TODO(model-adapters), TODO(loop-b-engine), TODO(doc-pr-flow).
//
// The analyzer lineup (CLI_ANALYZER_LINEUP) is bound to the landed
// internal/analyzers/tstool + internal/analyzers/generic packages.
package cli
