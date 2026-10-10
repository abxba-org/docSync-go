// Package boundaries ports packages/core/src/boundaries: the bidirectional
// write-boundary guards for the `.docsync/` tree (P0-T14, V-P0-13; ARCH §6.3).
//
// ARCH §6.3 ownership model: config (manifest/policy/concepts/models +
// instructions, `evaluation/**`, `memory/**`, `policy-history/**`) is
// HUMAN-owned and PR-reviewed, while `state/` is SYSTEM-written. Unknown paths
// are refused BOTH ways — fail closed (invariant 3, D8): the guards never touch
// files outside the allowlist/ownership model.
//
// The guards decide the write BOUNDARY only. Whether a path is part of the
// canonical D1 layout is `validateLayout`'s job (internal/layout) — e.g.
// `state/rogue.json` is state-class (refused to config writers) yet still a
// layout violation.
//
// Export surface (mirrors the TS boundaries/index.ts barrel):
//
//	ClassifyDocsyncPath, GuardStateWrite, GuardConfigWrite  (guards.go)
//	GuardedPathError, AssertStateWrite, AssertConfigWrite    (errors.go)
//	EDOCSYNC_BOUNDARY, DocsyncPathClass, WriteDecision       (this package)
package boundaries
