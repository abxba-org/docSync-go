# docSync-go — Migration Plan (TypeScript → Go)

> Companion to the meta-prompt "TypeScript-to-Go Migration Orchestrator".
> Source of truth for behavior: the TypeScript implementation in
> `source/` (clone of abxba-org/docSync) plus its docs
> (`AGENTS.md`, `AGENT-behavior.md`, `docs/ARCHITECTURE.md`, `docs/PLAN.md`).

## 1. Migration scope & constraints

- **Goal:** idiomatic Go implementation of the docSync engine preserving
  observable behavior, public contracts (JSON/YAML wire formats, exit codes,
  state file layout under `.docsync/`), safety invariants (ARCH §14), and
  the 14 binding decisions D1–D14.
- **Repo:** this repository (`docSync-go`) is a NEW repository; no pushes or
  comments go to the original `abxba-org/docSync`. All migration work lands
  on branch `migration/typescript-to-go`; a single PR to `main` is opened
  when the migration reaches its documented stopping point.
- **Behavior over syntax:** semantics are translated, not transcribed. Zod
  schemas become Go structs + strict parsers with identical validation
  messages; TS error classes become coded errors (`internal/docutil`) with
  preserved messages; `canonicalJson` is ported byte-for-byte so CAS hashes
  match the TS implementation.
- **Unavoidable differences are documented** in §6 (Deviations), never
  silently absorbed.

## 2. Module & package mapping (TS package → Go package)

| TypeScript source | Go package | Notes |
|---|---|---|
| packages/schemas (zod, v1) | `internal/schemas` | single schema source of truth; strict parsers; registry |
| core/src/util/ulid.ts | `internal/docutil` | ULID + content hash + canonical JSON + coded errors |
| core/src/state/* | `internal/state` | CAS store, keys, paths, queue, canonical, churn |
| core/src/layout/* | `internal/layout` | `.docsync/` layout + manifest/config loaders |
| core/src/boundaries/* | `internal/boundaries` | guarded-path write rules |
| core/src/modes/* | `internal/modes` | mode resolution + floors (D2) |
| core/src/precedence/* | `internal/precedence` | precedence resolution |
| core/src/globs/* | `internal/globs` | picomatch-compatible engine, specificity, conflicts |
| core/src/git/* | `internal/gitx` | typed exec wrapper + diff/patch-id client (D12) |
| core/src/validate/* | `internal/validate` | manifest validation, diagnostics, glob tightening |
| core/src/lint/* | `internal/lint` | repository lints |
| core/src/envlint/* | `internal/envlint` | env lint |
| core/src/triggers/* | `internal/triggers` | trigger map |
| core/src/analysis/* | `internal/analysis` | ChangeAnalysis, semantics, OpenAPI, generated, config-diff |
| core/src/graph/* | `internal/graph` | bidirectional code ↔ concepts ↔ docs graph |
| core/src/claims/* | `internal/claims` | anchor, intake, status, store |
| core/src/contracts/* | `internal/contracts` | classify, coverage, mapping proposals |
| core/src/retrieval/* | `internal/retrieval` | bounded retrieval engine |
| core/src/cache/* | `internal/cache` | analysis cache + symbol index |
| core/src/baselines/* | `internal/baselines` | store, invalidation ladder (D12) |
| core/src/statemachine/* | `internal/statemachine` | doc lifecycle state machine |
| core/src/workspace/* | `internal/workspace` | workspace assembly/batch/duties/render |
| core/src/patch/* | `internal/patch` | anchor, apply, merge, minimality, export-claims |
| core/src/orchestrator/* | `internal/orchestrator` | deterministic stage graph, RunOutcome (frozen API) |
| core/src/provenance/* | `internal/provenance` | record, store, check-view, PR body, secret scan |
| core/src/gitpr/* | `internal/gitpr` | doc-PR flow, governors, idempotency, self-trigger, merge ledger |
| core/src/feedback/* | `internal/feedback` | recorder, sink, hygiene, overrides, fn/fp detection |
| core/src/evaluation/* | `internal/evaluation` | dataset, metrics, monitoring, splits, spotcheck |
| core/src/memory/* | `internal/memory` | decisions/patterns/exceptions/failures records |
| core/src/drift/* | `internal/drift` | differential sweep (Loop B) |
| core/src/audit/* | `internal/audit` | audit engine, scheduler, store, org report |
| core/src/coverage-audit/* | `internal/coverageaudit` | unknown-coverage audit |
| core/src/testing/* | `internal/testkit` | fakes, golden/property helpers |
| packages/openrouter | `internal/openrouter` | client, transport, structured output, adapters, routing |
| packages/policy | `internal/policy` | compiler, engine matrix, Jev runtime, calibration, promotion, shadow |
| packages/verifier | `internal/verifier` | tier1/2/3, pipeline, replay, records |
| packages/cli | `internal/cli` + `cmd/docsync` | check/fix/pr/init/audit; exit codes 0/1/2/3 |
| packages/action | `internal/action` + `cmd/docsync-action` | GitHub Action entrypoint |
| packages/github-app | `internal/githubapp` + `cmd/docsync-app` | GitHub App webhooks + check runs |
| packages/analyzer-generic | `internal/analyzers/generic` | path-level analyzer |
| packages/analyzer-typescript | `internal/analyzers/tstool` | TS symbol analyzer (see §6 deviation DEV-1) |

Module path: `github.com/DUDE616/docSync-go`.

## 3. Phases & dependencies

Dependency order (Go package import DAG; a package only imports
lower-numbered layers):

```
L0 docutil, schemas
L1 state, boundaries, modes, precedence, triggers, globs, gitx, layout, validate
L2 claims, graph, baselines, statemachine, contracts, retrieval, cache, lint, envlint
L3 analysis, patch, workspace, memory, feedback, drift, coverageaudit, openrouter
L4 policy, verifier, evaluation, audit, analyzers
L5 orchestrator, provenance, gitpr, cli, action, githubapp, testkit
```

Execution waves (each wave = disjoint file ownership, parallel-safe):

- **W0 (done):** skeleton, docutil, schemas foundation (manifest, state,
  verification-report, decode/yaml/hex helpers).
- **W1:** remaining `internal/schemas` files + their tests; `internal/state`;
  `internal/boundaries`, `internal/modes`, `internal/precedence`.
- **W2:** `internal/globs`, `internal/gitx`, `internal/layout`,
  `internal/validate`, `internal/triggers`.
- **W3:** `internal/claims`, `internal/graph`, `internal/baselines`,
  `internal/statemachine`, `internal/contracts`.
- **W4:** `internal/analysis`, `internal/patch`, `internal/retrieval`,
  `internal/cache`.
- **W5:** `internal/openrouter`, `internal/policy`, `internal/verifier`.
- **W6:** `internal/orchestrator`, `internal/provenance`, `internal/gitpr`,
  `internal/workspace`, `internal/memory`.
- **W7:** `internal/cli` (+`cmd/docsync`), `internal/action`,
  `internal/githubapp`, `internal/analyzers/*`.
- **W8:** `internal/feedback`, `internal/evaluation`, `internal/drift`,
  `internal/audit`, `internal/coverageaudit`, `internal/lint`,
  `internal/envlint`, `internal/testkit`.
- **W9:** full verification: `go build ./...`, `go vet ./...`,
  `gofmt -l`, `go test ./... -race`, coverage report; deviations review;
  trackers; PR.

Per-wave gate (run by the orchestrator after each wave):
`gofmt -l .` · `go vet ./...` · `go build ./...` · `go test ./...` ·
coverage per touched package.

## 4. Test strategy (mirrors TS suite)

- Every behavior-bearing TS test file is ported to a Go `_test.go`
  (black-box `<pkg>_test` package where possible; in-package where internals
  are exercised). testify `require`/`assert` mirrors vitest expectations.
- Golden tests: serialized artifacts compared against the TS examples
  (`*_EXAMPLE` values) so the wire format cannot drift.
- Property tests: the TS `fast-check` suites are ported with Go
  `testing/quick` or table-driven equivalents over the same generators.
- Fakes/doubles: TS testing doubles become Go interfaces + in-memory fakes
  in `internal/testkit` (or package-local fakes), keyed by request identity
  per AGENT-behavior.md replay rule.
- Coverage target: ≥90% statement/branch per migrated package, reported
  honestly in §5 as waves land.

## 5. Progress & verification log

| Wave | Packages | Build | Vet | Tests | Coverage | Gate |
|---|---|---|---|---|---|---|
| W0 | docutil, schemas(foundation) | green | green | — | — | green |

(Updated by the orchestrator after each wave; claims here are backed by
executed commands, never by delegate status messages.)

## 6. Deviations from the TypeScript implementation

- **DEV-1 (planned):** `analyzer-typescript` uses tree-sitter in TS. Go has
  no tree-sitter binding without CGO; the Go port implements an equivalent
  lightweight TypeScript/TSX parser (exports, signatures, comments) with the
  same change-classification output. If any classification cannot be
  reproduced exactly it is documented here and routed conservatively
  (fail-closed → REVIEW) per D14.
- **DEV-2 (planned):** `picomatch` is replaced by `internal/globs`, a
  Go glob engine implementing the picomatch semantics used by docSync
  manifests (`**`, `*`, `?`, braces, character classes, negation). The
  manifest test corpus is ported to keep behavior pinned.
- Additional deviations are appended here with rationale as waves land.

## 7. Requirement traceability

Each migrated Go symbol carries the TS file path it came from in its doc
comment (e.g. `// Ported from core/src/state/cas.ts`), so any behavior can
be traced back to the reference implementation. The mapping table in §2 is
the reverse index.

## 8. Completion criteria (from the meta-prompt)

1. Every in-scope TS feature has a mapped Go implementation or an explicitly
   documented exclusion in §6.
2. Wire contracts preserved: `.docsync/` layout, state files, run records,
   exit codes 0/1/2/3, PR body formats.
3. `go build ./...`, `go vet ./...`, `gofmt -l .` clean; `go test ./... -race`
   green.
4. Coverage reported per package; gaps documented.
5. Safety invariants (ARCH §14) verified as executable tests.
6. README/docs updated for the Go workspace.
7. Reviewed against `source/docs/ARCHITECTURE.md` and `source/docs/PLAN.md`.
8. Final verification agent pass; findings resolved or documented.
9. Delegates terminated; unfinished work listed in §5 with exact next steps.
