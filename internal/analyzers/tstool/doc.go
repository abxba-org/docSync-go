// Package tstool is the Go port of `@docsync/analyzer-typescript`
// (packages/analyzer-typescript/src/*; F11-1/F11-3, E1b + E2 / P11-T03; ARCH
// §3.7, §4.1, §15.2) — the TypeScript analyzer plugin, FIRST in the OD-4 plugin
// order.
//
// It exports the plugin itself ({@link CreateTypeScriptAnalyzer}, plus the
// declared-degradation fallback {@link CreateTypeScriptAnalyzerStub}) under the
// unchanged registry name 'typescript', so swapping the stub for the real
// analyzer is transparent to analysis.ComposeAnalyzers (OD-4 order unchanged, no
// call-site changes). Since E2 it derives a FULL per-file SymbolDiff for every
// patch-bearing file by reconstructing BOTH sides of the unified diff
// ({@link BeforeTextFromPatch} / {@link AfterTextFromPatch}), projects it onto
// the schema-level `symbols_detail` that survives composition merging, and
// declares its symbol capability in the coverage note (P11-T03/P11-T05). It
// degrades to the declared stub coverage shape on parser/extraction failure and
// when the run forbids deep analysis (D14, never silent).
//
// PURITY (plugin contract): no fs/network/git/clock access; deterministic for
// identical input — the same analyzer input yields a deep-equal, byte-stable
// output (every note is a frozen constant).
//
// DEVIATION (plan.md §6 DEV-1, planned): the TypeScript implementation extracts
// symbols with web-tree-sitter (WASM grammar). Go has no tree-sitter binding
// without CGO, so this package implements an EQUIVALENT lightweight
// TypeScript/TSX parser (see symbols.go — lexer plus a statement/member
// scanner covering exports, signatures, comments and symbol declarations) that
// reproduces the tree-sitter query's matches: top-level declarations and
// class/interface members, in source order, with the same name/kind/line/
// signature/exported/typeAnnotations shape. The classification output
// (ChangeClassification: added/removed/changed symbols, symbols_changed,
// api_diff, symbols_detail) is identical for the ported corpus; the declared
// parser-coverage gaps are documented on the extractor (declared, not silent —
// D14). Nothing is silently approximated: a construct the lightweight parser
// cannot classify simply yields NO symbol (it never invents one), and the
// extractor's own coverage model is the same as tree-sitter's (top-level plus
// class/interface members only).
//
// Ported surface (traceability, plan.md §7 — one writer per file):
//
//	analyzer.go      packages/analyzer-typescript/src/{index,analyzer,diff}.ts
//	                 — the plugin, the stub, the unified-diff reconstruction and
//	                   the pure symbol-table diff
//	symbols.go       packages/analyzer-typescript/src/{parser,symbols}.ts
//	                 — the lightweight parser and the symbol extractor (E1a)
//	cache.go         packages/analyzer-typescript/src/index-cache.ts
//	                 — the per-blob-SHA symbol index cache (V-P11-2)
//	stats.go         packages/analyzer-typescript/src/proxy-measure.ts
//	                 — the A5 behavioral-proxy measurement (P11-T22)
//	*_test.go        packages/analyzer-typescript/test/*.test.ts
//
// The TS barrel (index.ts re-exports) and the parser bootstrap (parser.ts:
// WASM asset resolution, `Parser.init`, `Language.load`) are NOT ported:
// barrels are orchestrator-owned and the WASM runtime is what DEV-1 replaces.
// The TS export `contentSha256` maps to docutil.ContentHash (same sha256 hex
// digest), so the analyzer has no crypto import of its own.
//
// The dependency direction follows the TS package: this package imports
// internal/analysis (the plugin seam), internal/schemas and internal/docutil —
// never the reverse.
package tstool
