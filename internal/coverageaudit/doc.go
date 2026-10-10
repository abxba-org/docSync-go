// Package coverageaudit is the Go port of the Phase-11 coverage module
// (P11-T22; ARCH §16.1 A5; D14) from packages/core/src/coverage-audit/:
//
//	audit.go     core/src/coverage-audit/audit.ts      — the A5
//	                                               behavioral-proxy coverage
//	                                               audit engine
//	run-audit.go core/src/coverage-audit/run-audit.ts — the coverage-corpus
//	                                               directory loader
//
// The A5 audit MEASURES the deterministic approximation quality of the
// "externally observable behavior changed" proxies (signature / type /
// visibility / api-surface / config — ARCH §16.1 A5) over a labeled fixture
// corpus whose ground truth each case DECLARES (no model judgment). Per
// language it computes recall, the false-positive rate, the DECLARED recall
// gap (a behavioral case no proxy detected — recorded in the report, never
// silent, D14) and REVIEW routing for uncertain cases (D14 — ambiguity
// degrades toward less automation, never more).
//
// Unknown coverage at the D7 manifest level (a changed-but-unmapped doc) is a
// DIFFERENT surface: internal/lint reports it as the `unmapped_doc_dir`
// warning. This package is the A5 measurement study and is deliberately
// dependency-light: the real-analyzer study runner lives in the
// analyzer-typescript package's tests because the dependency direction
// (analyzer-typescript → core) forbids this engine from running the real
// analyzer itself — the proxy measurements are an injectable narrow port
// (BehavioralProxyCoverageCaseInput).
//
// The TS barrel (core/src/coverage-audit/index.ts) and its wiring into
// packages/core/src/index.ts are NOT ported here: barrels are
// orchestrator-owned. Consumers import this package directly.
//
// Deterministic, offline, zero LLM. No clock: `at` is caller-owned. Output is
// validated against docsync.behavioral-proxy-coverage-report/v1 before return
// — a shape bug fails closed rather than emitting an invalid report.
//
// Fail closed (D14): a missing/unparsable/invalid truth.json, an unknown truth
// label, an unknown proxy kind, or a missing before/after pair refuses the
// whole corpus load — never a silently skipped case (a skipped behavioral
// case would fake recall).
package coverageaudit
