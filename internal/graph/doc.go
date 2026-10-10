// Package graph ports the docSync document graph module (P1-T09; ARCH §4.2)
// from TypeScript to Go.
//
// It is a faithful port of the TypeScript sources under
// packages/core/src/graph/ in the docSync repository:
//
//   - types.ts    — MappingRecord / DocumentGraph / edge + config-error types
//   - mapping.ts  — buildDocumentGraph (manifest + concepts composition, OD-R36)
//   - edges.ts    — typed code↔concepts↔docs edges; toGraphEdge ingestion
//   - lookup.ts   — per-run reverse/forward lookups over the globs engine
//   - orphan.ts   — orphaned-edge detection (V-P1-6 groundwork)
//   - files.ts    — graph-as-files serialize/parse round-trip (canonical JSON)
//   - errors.ts   — GraphUnusable / GraphEdgeInvalid / GraphParse coded errors
//
// The tripartite code ↔ concepts ↔ docs graph is represented declaratively:
// one MappingRecord per manifest document (manifest order, deterministic),
// extended by concepts.yml. Composition, edge derivation, lookup and orphan
// detection are PURE and fail-closed (D8): a graph with configuration errors is
// unusable, edges with unknown kinds are refused, and serialized documents are
// parsed strictly. The reverse/forward lookup invariant — every edge resolves in
// BOTH directions, for ALL kinds — is exercised by graph_test.go.
//
// There is NO persistent full-graph index in V1: reverse indexes are built per
// run from changed-file sets (see buildReverseIndex). Graph matching goes
// through the shared glob engine (internal/globs), which enforces the ARCH
// §11.3 escape guards — a glob that fails its guard is a configuration error,
// fail closed per D8.
package graph
