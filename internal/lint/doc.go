// Package lint ports the docSync manifest-lint module (P0-T11; ARCH §11.3
// warnings + V-P0-15; R14 manifest drift) from packages/core/src/lint/:
//
//   - lint.ts → lint.go   (lintManifest: warnings-only findings)
//   - walk.ts → walk.go   (walkRepoFiles fs adapter + lintManifestFromRepo)
//
// Every lint finding is a WARNING: the run proceeds, annotated — lint never
// halts it (D8 taxonomy: hard errors such as schema violations or escaping
// globs are the fail-closed validator's job). Two hard inputs still fail closed
// here by returning a typed error rather than a report:
//
//   - a schema-invalid manifest → *ManifestLintError (lint runs on valid
//     manifests only), and
//   - a guard-failing glob → the globs package's *GlobGuardError (never
//     downgraded to a warning), propagated verbatim from compileGlob.
//
// The manifest is an ALLOWLIST (D7): a document absent from the manifest is
// never scanned or patched. `unmapped_doc_dir` surfaces that unknown-coverage
// contract at lint level.
//
// The walk is git-agnostic (R14 documented simplification): every file under
// the repo root except node_modules/, .git/, dist/ and .docsync/ (pruned by
// directory NAME at any depth) is reported as a POSIX-relative path; hidden
// files ARE included; symbolic links are skipped (never followed); .gitignore is
// NOT honored (Phase 8 wires git ls-files). Directory entries are visited in
// sorted order for deterministic output; walk errors propagate (fail closed).
package lint
