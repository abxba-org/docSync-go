// Package testkit is the Go port of packages/core/src/testing (P0-T17; the
// `@docsync/core/testing` V1-INTERNAL subpath) — the shared testing-helpers
// package of the docSync Go repository. It is NOT part of any published API
// surface: it exists so the Phase 1-5 Go suites share one implementation of
// golden-file comparison, property-based generators, the state/GitPlatform
// fakes and the pipeline fixture builders.
//
// Contents (one file per ported TS module):
//
//   - golden.go          — port of testing/golden.ts: canonical-JSON golden
//     snapshots (expectGolden / readGolden / goldenExists, deterministic
//     JSON-path diffs), reusing the SAME canonicalization as the state store
//     (docutil.CanonicalJSON) so goldens are byte-stable across runs.
//   - property.go        — port of testing/property.ts: the shared arbitraries
//     plus the Go form of the fast-check harness — a SEEDED splitmix64 PRNG,
//     shrink-free, with the run count asserted (DEFAULT_NUM_RUNS = 200).
//   - pipeline-fixtures.go — port of testing/pipeline-fixtures.ts (+ the
//     caller-owned-root parts of testing/fakes/fs-fixture.ts): payments and
//     monorepo-scale fixture TREES written under a caller-supplied root, with
//     git repo construction left to the caller (os/exec git helpers are
//     provided, never invoked implicitly).
//   - fakes.go           — port of testing/fakes/{state-fake,git-platform-fake,
//     types}.ts: the in-memory state store (CAS/registry/guard contract copy),
//     the GitPlatform recording double, and the keyed model-stage doubles
//     that wire onto the orchestrator port bundle.
//
// Port discipline (CONVENTIONS.md):
//   - every wire value keeps its TS JSON/YAML keys (snake_case where zod
//     declared them that way);
//   - TS error message text is preserved EXACTLY where tests or users observe
//     it (guard refusals, schema-drift refusals, golden mismatch summaries);
//   - no clock and no unseeded randomness anywhere in this package — the
//     fixture clock is the exported PipelineT0 constant and every property
//     stream derives from an explicit seed;
//   - testkit is a pure helper package: it imports internal/docutil,
//     internal/schemas, internal/state, internal/boundaries, internal/modes,
//     internal/graph, internal/baselines, internal/claims, internal/statemachine
//     and internal/orchestrator, and NOTHING imports testkit — so it can never
//     take part in an import cycle (internal/cli is deliberately not imported).
//
// Replay rule (AGENT-behavior.md; the phase-6 verifier flake fixed by 4c68f4b):
// every recording/replay double in this package keys its matches by REQUEST
// IDENTITY and dispatches per call — never positional FIFO — and fails loud
// on an unexpected identity, because completion-order consumption cross-wires
// fixtures under concurrent load. The TS doubles in this directory script
// FIFO queues; the Go ports dispatch by identity instead, and the package
// tests pin that behavior under -race.
package testkit
