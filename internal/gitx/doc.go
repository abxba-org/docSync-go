// Package gitx is the Go port of the docSync git layer.
//
// It mirrors packages/core/src/git in the TypeScript reference
// (/home/ubuntu/CODE/docSync-GO/source):
//
//   - exec.go       <- git/exec.ts    (typed exec wrapper around the git binary)
//   - client.go     <- git/client.ts  (changed files between refs, rename
//     detection per decision D12, blob SHAs, patch-id, merge-base, commit
//     metadata and the rest of the GitClient surface)
//
// Errors produced by this package are docutil coded errors carrying the code
// EDOCSYNC_GIT while preserving the exact messages used by the TypeScript
// implementation, so callers can assert on them without changes.
package gitx
