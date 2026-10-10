# Migration Conventions (binding for all work in this repo)

This file is the contract every migration unit (human or delegate) follows.
It complements `plan.md` (scope, waves, gates). When in doubt: fail closed,
preserve behavior, persist immediately, verify on disk.

## Module & imports

- Module path: `github.com/DUDE616/docSync-go`. Go 1.26.
- Package layout per `plan.md` §2. Never import a higher layer (the import
  DAG in `plan.md` §3 is binding). No import cycles.
- One writer per file. Delegates own disjoint file sets; barrels/index files
  and `go.mod` are orchestrator-owned.

## Errors

- Code errors via `internal/docutil` (`docutil.ConfigError`, `docutil.New(code, msg)`, …).
  Preserve the TS error message text EXACTLY where tests or users observe it.
- Map each TS error class to one code (see `docutil/errors.go`); when the TS
  code throws a class not yet listed, pick the closest code and note it in
  the unit report — do not invent a new code without orchestrator approval.
- Go idiom: return `error` values; never panic across package boundaries.

## Types & serialization

- Wire formats are law: JSON keys stay snake_case exactly as zod declared
  them; YAML config keys stay identical. Go struct tags carry the TS names.
- `z.strictObject` ⇒ unknown JSON keys are rejected at parse (see
  `schemas.StrictDecode`; for YAML see `decodeStrictYAMLOrJSON`).
- `.optional()` ⇒ pointer field + `omitempty`. `.default(v)` ⇒ applied in
  the parser when the key is absent.
- Zod validation messages (e.g. "String must contain at least 1 character(s)")
  are reproduced in `Validate()` methods via `schemas.Issue` paths.
- Numbers: integers use `int`; floats only where the TS schema used
  `z.number()` non-int. Canonical state hashing goes through
  `docutil.ContentHash`/`docutil.CanonicalJSON` — never ad-hoc marshaling.

## Canonical JSON & state

- `canonicalJson` semantics are ported in `docutil.CanonicalJSON`: keys
  sorted, arrays in order, JSON.stringify-compatible escaping, fail-closed
  on non-JSON values. State files are written as canonical bytes so hashes
  match the TS implementation.

## Tests

- Port the corresponding TS test files to Go `_test.go`; use
  `github.com/stretchr/testify`. Black-box (`package foo_test`) unless the
  TS test reached into internals.
- Keep the same test names/IDs where useful (e.g. `TestP0T12_CASConflict`)
  for traceability to `BACKLOG.md`/PLAN ids.
- Golden data: TS `*_EXAMPLE` values become Go literals and are asserted
  byte-for-byte through the Go serializer.
- Determinism: inject clocks/ULID generators (`docutil.NewULIDGeneratorWith`),
  never read the wall clock inside logic under test.

## Gates (scoped, per unit)

Each unit runs, from the repo root:

```
gofmt -l -w <written files>        # formatting is mandatory
go build ./<affected packages>     # compile
go test ./<affected packages>      # unit tests
```

The orchestrator runs the full gate per wave (`plan.md` §3). Delegates never
run repo-wide builds concurrently (AGENT-behavior.md).

## Reporting (mandatory format)

Every unit ends with:

```
FILES: <paths written>
TESTS: <go test command + result summary>
COVERAGE: <percent or n/a for the package>
DEVIATIONS: <behavior differences vs TS, or NONE>
BLOCKERS: <none or exact blocker>
```

An empty or truncated report is a failed unit (AGENT-behavior.md).

## Behavior preservation

- No silent behavior changes. Any unavoidable difference (platform, library
  substitution like picomatch/tree-sitter) goes to `plan.md` §6 with
  rationale and conservative routing.
- Concurrency: TS async flows become goroutines + channels or plain
  sequential code; preserve ordering, cancellation, and rate limiting.
  Shared mutable state gets mutexes; run tests with `-race`.
- Time: inject `now` functions; preserve timezone/ISO-8601 formats.
