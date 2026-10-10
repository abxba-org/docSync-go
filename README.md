# docSync-go

A **Git-native documentation consistency engine** — not an AI docs generator.
This repository is the **Go implementation**, migrated 1:1 from the
[TypeScript reference implementation](https://github.com/abxba-org/docSync)
(the original repo is untouched; this is a new repository).

> docSync detects when code changes make documentation stale, identifies *only* the
> relevant documentation, proposes *minimal* evidence-backed patches, verifies them,
> and ships them through the normal Git/PR workflow. Each repository develops its own
> decision policy over time — a repository-specific, self-calibrating documentation
> policy engine.

```text
Code change
    ↓
Git diff                          deterministic analysis (no AI)
    ↓
Manifest allowlist                only mapped docs are candidates
    ↓
Jev (typed decision layer)        classify · score · route — IGNORE / REVIEW / CONTINUE
    ↓
Retrieval                         bounded candidate docs with provenance
    ↓
Patch-writer (Sonnet role)        minimal, content-anchored DocPatch + evidence
    ↓
Verification                      three tiers: validators → claim checking → independent judge
    ↓
Policy engine                     auto | suggest | human review | blocked (abstain, don't guess)
    ↓
Git diff / doc PR                 human review, exactly like any code change
```

**Core principle:** *Git owns the documentation. AI proposes changes to it. The
application controls whether AI is allowed to make those changes.*

## Repository layout

Single Go module (`github.com/DUDE616/docSync-go`) mirroring the TypeScript
package layout:

| Go package | Migrated from |
|---|---|
| `internal/schemas` | `packages/schemas` (zod → structs + strict parsers; the single schema source of truth) |
| `internal/docutil` | `core/src/util` + coded errors + canonical JSON + content hash + ULID |
| `internal/state` | `core/src/state` (CAS store, keys, paths, queue, churn) |
| `internal/layout`, `internal/validate` | `core/src/layout`, `core/src/validate` |
| `internal/globs` | `core/src/globs` (picomatch-compatible engine — see `plan.md` §6 DEV-2) |
| `internal/gitx` | `core/src/git` (typed exec wrapper + client, D12) |
| `internal/boundaries`, `internal/modes`, `internal/precedence` | same-named core modules |
| `internal/claims`, `internal/graph`, `internal/baselines`, `internal/statemachine`, `internal/contracts`, `internal/retrieval`, `internal/cache`, `internal/patch`, `internal/workspace` | same-named core modules |
| `internal/analysis`, `internal/analyzers/generic`, `internal/analyzers/tstool` | `core/src/analysis`, `packages/analyzer-*` (see `plan.md` §6 DEV-1) |
| `internal/policy` | `packages/policy` (Jev runtime, compiler, calibration, promotion, shadow) |
| `internal/verifier` | `packages/verifier` (tiers 1–3, pipeline, replay) |
| `internal/openrouter` | `packages/openrouter` (client, transport, role adapters) |
| `internal/orchestrator` | `core/src/orchestrator` (deterministic stage graph, FROZEN RunOutcome API) |
| `internal/gitpr` | `core/src/gitpr` (doc-PR write path) |
| `internal/cli` + `cmd/docsync` | `packages/cli` |
| `internal/action` + `cmd/docsync-action` | `packages/action` |
| `internal/githubapp` + `cmd/docsync-app` | `packages/github-app` |
| `internal/provenance`, `internal/feedback`, `internal/evaluation`, `internal/drift`, `internal/audit`, `internal/coverageaudit`, `internal/memory`, `internal/lint`, `internal/envlint`, `internal/triggers` | same-named core modules |
| `internal/testkit` | `core/src/testing` (fakes, golden, property helpers) |

## Quick commands

```bash
go build ./...          # compile every package + entrypoint
go vet ./...            # static analysis
gofmt -l internal/ cmd/ # formatting check (empty = clean)
go test ./... -count=1  # unit + integration tests (no network)
go test ./... -race     # with the race detector
go test ./... -cover    # coverage
go run ./cmd/docsync --help
```

Exit codes are binding: `0` no drift · `1` drift · `2` config error
(fail-closed before any AI stage) · `3` runtime failure (fail-closed).

## Documentation

| File | Contents |
|---|---|
| [`plan.md`](plan.md) | Migration plan: module mapping, waves, progress log, deviations |
| [`CONVENTIONS.md`](CONVENTIONS.md) | Binding migration conventions (errors, serialization, tests, gates) |
| [`AGENTS.md`](AGENTS.md) | Behavioral guide for agents working in this repo |
| [`AGENT-behavior.md`](AGENT-behavior.md) | Learned behavioral rules |
| `source/docs/ARCHITECTURE.md` | Reference architecture (D1–D14, 20 principles, 13 safety invariants) |
| `source/docs/PLAN.md` | Reference implementation plan (Phases 0–12, OD-R registry) |

The TypeScript reference lives in `source/` (a clone of the original repo used as the
behavioral reference during migration).

## Migration status

The migration is organized in waves (W0–W9) tracked in `plan.md` §5. Each wave gate is
`gofmt` + `go vet` + `go build` + `go test` + `go test -race` + per-package coverage.
Known deviations (tree-sitter substitution, picomatch replacement, YAML bridge
semantics) are documented in `plan.md` §6 with their conservative routing rules.
