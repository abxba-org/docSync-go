// Package envlint ports the docSync environment-variable policy lint (P0-T16;
// ARCH §11.3; invariant 8; D9) from packages/core/src/envlint/envlint.ts.
//
// Environment variables carry CREDENTIALS AND ENDPOINTS ONLY — never policy
// (ARCH §11.3): policy must be reviewable in Git. LintEnvironment is PURE — the
// environment is passed in; the real process.env is never read here.
// lintProcessEnvironment is the thin adapter that snapshots the calling
// process's environment for the orchestrator/CLI. Violations are reported (the
// validate layer converts them into fail-closed configuration errors, D8).
//
// Violation rules (first match wins per variable):
//
//   - any variable in a POLICY_ENV_PREFIXES namespace → policy via env;
//   - any other DOCSYNC_* variable outside the allowlist → policy-via-env
//     attempt (unknown docSync namespaces fail closed rather than leak in);
//   - everything else (including unrelated vars such as PATH) is not docSync's
//     business and is ignored.
//
// Variables are linted in sorted (variable-name) order for deterministic output.
package envlint
