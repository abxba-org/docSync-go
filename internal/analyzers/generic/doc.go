// Package generic is the Go port of `@docsync/analyzer-generic`
// (packages/analyzer-generic/src/index.ts; P1-T07; ARCH §3.7/§4.1; OD-4) — the
// ALWAYS-PRESENT path-level analyzer.
//
// Role in the OD-4 composition: the TypeScript analyzer (tree-sitter in TS;
// the lightweight parser port in internal/analyzers/tstool — plan.md §6 DEV-1)
// runs FIRST; this generic analyzer is ALWAYS present after it, so composition
// never runs empty and every changed file receives at least one declared
// finding. Its analysis is strictly path-level and deterministic: it consumes
// only the analyzer input (the purity contract of analysis.AnalyzerPlugin) and
// performs no symbol/AST extraction in V1.
//
// Declared coverage gap, never silent (ARCH §3.7, D14, V-P1-13): for EVERY input
// file the plugin emits `deep_declined: true` plus a stable explanatory note. It
// deliberately does NOT emit bare all-false rows — a row with all booleans false
// and no deep_declined would read as an assessed-negative result, which would
// misrepresent an un-analyzed file (D14: analyzer failure or coverage gap is
// inconclusive, never negative). The language-agnostic classification itself
// (test/formatting/generated detection, dependency and config differs) lives in
// internal/analysis and consumes these findings; this plugin stays minimal by
// design.
//
// `deepAllowed` is irrelevant in V1: there is no deep analysis to permit or
// cap, deep is declined for every file BY DESIGN, so the finding set is
// IDENTICAL whether deepAllowed is true or false.
//
// Ported surface (traceability, plan.md §7 — one writer per file):
//
//	analyzer.go     packages/analyzer-generic/src/index.ts      — the plugin
//	analyzer_test.go packages/analyzer-generic/test/*.test.ts   — both TS suites
//
// The plugin is pure by construction: no fs/network/git/clock access, and
// identical input yields deep-equal output with the input file order preserved.
// The returned plugin is a zero-size value type — frozen in exactly the way the
// TS `Object.freeze` intended: it cannot be mutated.
package generic
