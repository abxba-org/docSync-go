// Package validate ports packages/core/src/validate (P0-T08, D8): the
// fail-closed configuration validator — diagnostic taxonomy, semantic
// manifest validation with line-precise YAML position mapping, validator-
// level glob tightening, and the composition entry point
// ValidateRepositoryConfig (layout + manifest + mode resolution + org
// floors — zero AI invocations by construction).
//
// Fail-closed contract (D8): the run halts BEFORE any AI stage with the FULL
// diagnostic list (never first-error-only); hard errors map to exit code 2
// (config error) semantics, warnings annotate and never halt (V-P0-15).
//
// Not yet ported (upstream waves, tracked in plan.md): the environment
// policy lint (E_ENV_POLICY), manifest lint warnings
// (W_ZERO_MATCH_GLOB/W_NONEXISTENT_DOC_PATH/W_COVERAGE_LINT), and the
// trigger-taxonomy mapping (E_MANIFEST_UNKNOWN_TRIGGER) — those live in
// core/src/envlint, core/src/lint and core/src/triggers and have no Go
// counterpart package yet.
package validate
