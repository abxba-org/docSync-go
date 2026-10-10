// Package analysis is the Go port of the docSync deterministic analysis core
// (ARCH §4.1/§15.2): the analyzer plugin seam with D14-declared failure
// semantics, the pure change-shape detectors and classification hints
// (P1-T03/T04), the semantic differs (P1-T05), the deterministic
// ChangeAnalysis builder with caps/downgrade and inconclusive semantics
// (P1-T02/T08/T18), the V1 key-level config diffs, generated-file detection,
// and the three-tier OpenAPI delta classifier (P11-T07; OD-R28; V-P11-4).
//
// Ported files (traceability, plan.md §7 — one writer per file):
//
//   - doc.go            — this barrel (orchestrator-owned pointer file)
//   - semantics.go      <- core/src/analysis/semantics.ts  (path families,
//     dependencyChanged, apiDiffFor, configDiffFor — P1-T05; OD-R06)
//   - openapi.go        <- core/src/analysis/openapi.ts    (the versioned
//     three-tier OpenAPI delta rule table + classifyOpenApiDelta)
//   - semantics_test.go <- packages/core/test/analysis-semantics.test.ts
//   - openapi_test.go   <- packages/core/test/openapi-classifier.test.ts
//
// Sibling files in this package are owned by other delegates (detect.go,
// generated.go, build.go, plugin.go, config-diff.go) and are intentionally
// absent from this listing.
//
// The package is pure over its inputs — no fs/network/git/clock — and fails
// closed (D14): analyzer/assessment failure is inconclusive, never a silent
// negative.
package analysis
