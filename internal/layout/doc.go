// Package layout ports packages/core/src/layout (P0-T03, V-P0-3): the
// canonical `.docsync/` layout validator (pure, no fs) and the fs adapter that
// snapshots the on-disk tree into it.
//
// The canonical tree is decision D1; the layout constants in
// internal/schemas are the single source of truth (TS re-exported them from
// @docsync/schemas). Layout violations are CONFIGURATION ERRORS (D8
// fail-closed) and map to exit code 2 semantics in the validate package; the
// typed errors here make them distinguishable and render them line-precise.
//
// The loader resolves the canonical tree only — it does NOT parse manifest
// contents (semantic manifest validation lives in internal/validate).
package layout
