package testkit

// Ported from packages/core/src/testing/pipeline-fixtures.ts (P1-T20) plus the
// caller-owned-root half of testing/fakes/fs-fixture.ts (P0-T17).
//
// End-to-end pipeline fixture repositories: the payments fixture (manifest +
// concepts + graph + claims) and the monorepo-scale fixture (V-P1-4 cap-breach
// material). WHERE THE TS BUILT REAL GIT REPOS VIA NODE FS, the Go kit builds
// minimal fixture TREES under a caller-supplied root (a t.TempDir() in the
// suites): every helper takes the root path, and GIT REPO CONSTRUCTION IS THE
// CALLER'S CONCERN through the os/exec git helpers below (InitGitRepo,
// GitCommitAll, GitCommitPaths, GitBlobSHA). The fixture content is generated
// in-process instead of copied from checked-in `fixtures/<name>` trees — the
// Go repository carries no checked-in fixture trees, and a generated tree is
// byte-deterministic, so the fixture books below ARE the checked-in tree.
//
// What the payments fixture provides (D1 layout, ARCH §4.3):
//
//   - `.docsync/manifest.yml` mapping docs/payments.md in `auto` mode ←
//     `src/payments/**` + `src/api/payments/**`;
//   - `.docsync/concepts.yml` (`docsync.concepts/v1`, OD-R36) declaring two
//     hand-anchored claims: the idempotency claim (anchored at
//     docs/payments.md line 8, source `src/payments/refund.ts`) and the
//     retry-window claim (anchored at line 12, source
//     `src/api/payments/client.ts` — the UNCHANGED-file contrast partner);
//     claim `source.blob_sha` values are the REAL git blob shas of the fixture
//     sources (provenance or it didn't happen), computed with the caller's
//     git binary;
//   - `SeedVerifiedDoc` initializes docs/payments.md VERIFIED and records its
//     baseline against the given commit, with `source_files` RESOLVED through
//     the real graph forward lookup (the literal file identities verification
//     recorded — globs pin nothing).
//
// The monorepo-scale repo is the generated 8-package × 10-area × 5-file tree
// (400 TS files + 8 docs) plus git + manifest/graph; no concepts.yml —
// V-P1-4 exercises the CAP stage, not claims.
//
// Commit discipline (unchanged from the TS): CommitAll is for the INITIAL
// fixture commit only; after state exists under `.docsync/state/` use
// CommitPaths (targeted adds) so untracked state never enters a diff.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/statemachine"
)

/* ------------------------------------------------------- shared constants */

// PaymentsRepoID is the `repo` identity pipelines record for the payments
// fixture (TS PAYMENTS_REPO_ID).
const PaymentsRepoID = "fixtures/payments"

// MonorepoRepoID is the `repo` identity pipelines record for the monorepo-scale fixture
// (TS MONOREPO_REPO_ID).
const MonorepoRepoID = "fixtures/monorepo-scale"

// PaymentsDoc is the mapped payments document (the SUSPECT-routing target of
// the suites) (TS PAYMENTS_DOC).
const PaymentsDoc = "docs/payments.md"

// PaymentsTypesSource is the deleted-file orphan target of the V-P1-6
// scenario (TS PAYMENTS_TYPES_SOURCE).
const PaymentsTypesSource = "src/payments/types.ts"

// ClaimIdempotency / ClaimRetryWindow are the hand-anchored claim ids
// declared in the fixture concepts.yml (TS CLAIM_IDEMPOTENCY /
// CLAIM_RETRY_WINDOW).
const (
	ClaimIdempotency = "claim_refund_idempotency"
	ClaimRetryWindow = "claim_refund_retry_window"
)

// PipelineT0 is the fixed fixture clock (caller-owned timestamps everywhere
// — no hidden clock) (TS PIPELINE_T0).
const PipelineT0 = "2026-10-01T00:00:00Z"

// PipelineInitRun is the owning run id of fixture initialization (doc state,
// baseline, intake) (TS PIPELINE_INIT_RUN).
const PipelineInitRun = "run_fixture_init_p1t20"

// PipelinePolicyVersion is the fixture policy version (TS
// PIPELINE_POLICY_VERSION).
const PipelinePolicyVersion = 1

// ConceptsSchemaID is the concepts schema literal the fixture concepts.yml
// carries (TS CONCEPTS_SCHEMA id).
const ConceptsSchemaID = "docsync.concepts/v1"

// MonorepoPackageCount / MonorepoAreaCount / MonorepoFileCount are the
// generated monorepo-scale dimensions (TS the checked-in 8 × 10 × 5 tree):
// 400 TS files + 8 docs.
const (
	MonorepoPackageCount = 8
	MonorepoAreaCount    = 10
	MonorepoFileCount    = 5
)

// GitPinnedDate is the pinned author/committer date: stable SHAs across runs
// and machines (TS GIT_ENV).
const GitPinnedDate = "2026-01-01T00:00:00+00:00"

// gitIdentity is the pinned in-command identity (TS IDENTITY): with
// GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_NOSYSTEM=1 no machine-level
// gitconfig (a stray user.name, or core.autocrlf) can leak in.
var gitIdentity = []string{"-c", "user.name=docsync-test", "-c", "user.email=docsync-test@invalid"}

/* ------------------------------------------------------------ git plumbing */

// GitCommand builds the pinned `git -C <root> <identity> <args...>` command
// with the pinned environment. Callers own repo construction: this is the seam
// they drive, never an implicit fixture step.
func GitCommand(root string, args ...string) *exec.Cmd {
	full := make([]string, 0, 4+len(args)+len(gitIdentity))
	full = append(full, "-C", root)
	full = append(full, gitIdentity...)
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+GitPinnedDate,
		"GIT_COMMITTER_DATE="+GitPinnedDate,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	return cmd
}

// gitOutput runs one pinned git command and returns its trimmed stdout
// (stderr is surfaced in the error for diagnosability).
func gitOutput(root string, args ...string) (string, error) {
	out, err := GitCommand(root, args...).Output()
	if err != nil {
		return "", fmt.Errorf("git -C %s %s failed: %w", root, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// InitGitRepo initializes root as a git repo on branch `main` with the
// repo-local LF line-ending policy pinned BEFORE any file is written (the
// DX-P11-3 port: a machine-level core.autocrlf=true would rewrite fixture
// files to CRLF and surface them as ` M` in worktree assertions).
func InitGitRepo(root string) error {
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "core.autocrlf", "false"},
		{"config", "core.eol", "lf"},
	}
	for _, args := range steps {
		if err := GitCommand(root, args...).Run(); err != nil {
			return fmt.Errorf("git -C %s %s failed: %w", root, strings.Join(args, " "), err)
		}
	}
	return nil
}

// GitBlobSHA is the real git blob sha of a working-tree file — the
// deterministic content identity concepts.yml records (TS gitBlobSha).
// Content hashing only: no repository is required (and none is constructed).
func GitBlobSHA(root, relPath string) (string, error) {
	return gitOutput(root, "hash-object", relPath)
}

// GitCommitAll commits the whole working tree (`add -A`). INITIAL fixture
// commit only (TS the plumbing's commitAll).
func GitCommitAll(root, message string) (string, error) {
	if _, err := gitOutput(root, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := gitOutput(root, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	return gitOutput(root, "rev-parse", "HEAD")
}

// GitCommitPaths commits ONLY the given repo-relative paths
// (`git add -A -- <paths>`): works for modifications AND deletions/renames,
// and never sweeps untracked `.docsync/state/` into a diff (TS the plumbing's
// commitPaths).
func GitCommitPaths(root string, paths []string, message string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("commitPaths requires at least one path")
	}
	addArgs := make([]string, 0, 3+len(paths))
	addArgs = append(addArgs, "add", "-A", "--")
	addArgs = append(addArgs, paths...)
	if _, err := gitOutput(root, addArgs...); err != nil {
		return "", err
	}
	if _, err := gitOutput(root, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	return gitOutput(root, "rev-parse", "HEAD")
}

// GitHeadSHA resolves HEAD (the caller-owned revision bookkeeping helper).
func GitHeadSHA(root string) (string, error) {
	return gitOutput(root, "rev-parse", "HEAD")
}

// GitTrackedPaths lists the repo-relative paths tracked at HEAD.
func GitTrackedPaths(root string) ([]string, error) {
	out, err := gitOutput(root, "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return []string{}, nil
	}
	return strings.Split(out, "\n"), nil
}

/* ---------------------------------------------------------- tree utilities */

// WalkedEntry is one recursively collected entry under a root; Path is
// POSIX-relative (TS WalkedEntry).
type WalkedEntry struct {
	Path string
	Kind string
}

// Walk entry kinds (TS WalkedEntry['kind']).
const (
	WalkedFile = "file"
	WalkedDir  = "dir"
)

// ListRepoOptions configures ListRepoEntries (TS the options bag).
type ListRepoOptions struct {
	// IncludeDirs also reports directories (files only by default).
	IncludeDirs bool
	// Prefix restricts the walk to root/<prefix> (trailing slashes stripped).
	Prefix string
}

// ListRepoEntries is the deterministic file listing: walks root (or
// root/<prefix>) recursively. No symlink descent (fixture trees contain
// none); POSIX-relative paths relative to root; sorted byte-wise. A missing
// prefix directory fails closed instead of returning a silent empty list
// (TS listRepoEntries).
func ListRepoEntries(root string, options ListRepoOptions) ([]WalkedEntry, error) {
	prefix := strings.TrimRight(options.Prefix, "/")
	start := root
	relativeBase := ""
	if prefix != "" {
		start = filepath.Join(root, filepath.FromSlash(prefix))
		info, err := os.Stat(start)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("fixture prefix %q not found under %q", prefix, root)
			}
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("fixture prefix %q is not a directory under %q", prefix, root)
		}
		relativeBase = prefix
	}
	entries := []WalkedEntry{}
	if err := walkTree(start, relativeBase, options.IncludeDirs, &entries); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// walkTree is the recursive descent behind ListRepoEntries: symlinks are
// refused (fail loud) rather than guessed at (TS walk).
func walkTree(dir, relativeBase string, includeDirs bool, out *[]WalkedEntry) error {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, dirent := range dirents {
		// `.git` is repository plumbing the CALLER constructs (os/exec git),
		// never fixture content: skipping it keeps the listing about the
		// fixture tree alone (and assertions stable across git states).
		if dirent.Name() == ".git" && relativeBase == "" {
			continue
		}
		relativePath := dirent.Name()
		if relativeBase != "" {
			relativePath = relativeBase + "/" + dirent.Name()
		}
		if dirent.Type()&os.ModeSymlink != 0 {
			// Never descend into symlinks: fixtures must be plain files.
			return fmt.Errorf("fixture tree contains a symlink at %q — fixtures must be plain files", relativePath)
		}
		if dirent.IsDir() {
			if includeDirs {
				*out = append(*out, WalkedEntry{Path: relativePath, Kind: WalkedDir})
			}
			if err := walkTree(filepath.Join(dir, dirent.Name()), relativePath, includeDirs, out); err != nil {
				return err
			}
			continue
		}
		if dirent.Type().IsRegular() {
			*out = append(*out, WalkedEntry{Path: relativePath, Kind: WalkedFile})
		}
	}
	return nil
}

// ListRepoFiles is files only, POSIX-relative, sorted — the common case for
// fixture assertions. The optional prefix restricts the walk (TS
// listRepoFiles).
func ListRepoFiles(root string, prefix ...string) ([]string, error) {
	options := ListRepoOptions{}
	if len(prefix) > 0 {
		options.Prefix = prefix[0]
	}
	entries, err := ListRepoEntries(root, options)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind == WalkedFile {
			files = append(files, entry.Path)
		}
	}
	return files, nil
}

// RepoPath joins and normalizes a POSIX-relative path inside a repo root —
// helper for deterministic path construction in tests; rejects `..` traversal
// and absolute paths (TS repoPath).
func RepoPath(root, relativePath string) (string, error) {
	normalized := path.Clean(filepath.ToSlash(relativePath))
	if strings.HasPrefix(normalized, "..") || path.IsAbs(normalized) {
		return "", fmt.Errorf("fixture relative path must stay inside the root: %q", relativePath)
	}
	return filepath.Join(root, filepath.FromSlash(normalized)), nil
}

// WriteRepoFile writes content under root/relPath, creating directories (TS
// the plumbing's write).
func WriteRepoFile(root, relPath, content string) error {
	target, err := RepoPath(root, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

// RemoveRepoFile removes a file under the repo root (TS the plumbing's
// remove).
func RemoveRepoFile(root, relPath string) error {
	target, err := RepoPath(root, relPath)
	if err != nil {
		return err
	}
	return os.Remove(target)
}

// ReadRepoFile reads a file under the repo root as text.
func ReadRepoFile(root, relPath string) (string, error) {
	target, err := RepoPath(root, relPath)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

/* ------------------------------------------------------------- base shapes */

// FixtureRepo is the shared plumbing every pipeline fixture carries: the
// materialized root, the typed read-only git client over it, the real CAS
// state store over `<root>/.docsync/state` (D1), and the write/remove/commit
// helpers (TS PipelineRepoBase).
type FixtureRepo struct {
	// Root is the absolute root of the materialized fixture tree.
	Root string
	// Git is the typed git client over Root.
	Git *gitx.GitClient
	// Store is the real CAS state store over Root/.docsync/state.
	Store *state.StateStore
}

// NewFixtureRepo binds the git client and the state store over root (the
// state directory is created lazily by the first write — the real store never
// pre-creates empty directories).
func NewFixtureRepo(root string) (*FixtureRepo, error) {
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	if err != nil {
		return nil, err
	}
	store, err := state.NewStateStore(state.StateStoreOptions{
		StateDir: filepath.Join(root, schemas.DocSyncDir, "state"),
	})
	if err != nil {
		return nil, err
	}
	return &FixtureRepo{Root: root, Git: git, Store: store}, nil
}

// StateDir is the absolute `.docsync/state` directory of this fixture (D1).
func (r *FixtureRepo) StateDir() string {
	return filepath.Join(r.Root, schemas.DocSyncDir, "state")
}

// Write writes a file under the repo root (directories created).
func (r *FixtureRepo) Write(relPath, content string) error {
	return WriteRepoFile(r.Root, relPath, content)
}

// Remove removes a file under the repo root.
func (r *FixtureRepo) Remove(relPath string) error {
	return RemoveRepoFile(r.Root, relPath)
}

// Read reads a file under the repo root as text.
func (r *FixtureRepo) Read(relPath string) (string, error) {
	return ReadRepoFile(r.Root, relPath)
}

// CommitAll commits the whole working tree (INITIAL fixture commit only).
func (r *FixtureRepo) CommitAll(message string) (string, error) {
	return GitCommitAll(r.Root, message)
}

// CommitPaths commits ONLY the given repo-relative paths — never sweeps
// untracked `.docsync/state/` into a diff.
func (r *FixtureRepo) CommitPaths(paths []string, message string) (string, error) {
	return GitCommitPaths(r.Root, paths, message)
}

// Files lists the fixture's files (POSIX-relative, sorted).
func (r *FixtureRepo) Files(prefix ...string) ([]string, error) {
	return ListRepoFiles(r.Root, prefix...)
}

/* ------------------------------------------------- payments fixture content */

// PaymentsDocLines is the fixture document, one element per 1-addressable
// line. The hand-anchored claims pin line 8 (the idempotency claim) and line
// 12 (the retry-window claim); AssertPaymentsAnchors re-verifies both at
// materialization time, so a silent fixture shift fails loud.
var PaymentsDocLines = []string{
	"# Payments",
	"",
	"Payments API reference for the refund flow.",
	"",
	"## Refunds",
	"",
	"Refunds are asynchronous: the caller receives a refund id immediately.",
	"Refund requests require an `idempotencyKey`.",
	"",
	"The idempotency key is scoped per merchant account.",
	"",
	"- Retries are safe within a 24-hour idempotency window.",
	"",
	"## Webhooks",
	"",
	"Webhook deliveries are retried with exponential backoff.",
}

// PaymentsDocContent is the fixture document text (POSIX line endings, one
// trailing newline — absorbed by SplitDocLines).
var PaymentsDocContent = strings.Join(PaymentsDocLines, "\n") + "\n"

// paymentsRefundSource is the idempotency claim's source file; its line 10 is
// the claim's declared `source.line`.
var paymentsRefundSource = strings.Join([]string{
	"// Payments refund handler (fixture source of the idempotency claim).",
	"",
	"export interface RefundRequest {",
	"\torderId: string;",
	"\tidempotencyKey: string;",
	"}",
	"",
	"export function refundRequest(request: RefundRequest): string {",
	"\tif (!request.idempotencyKey) {",
	"\t\tthrow new Error('idempotencyKey is required');",
	"\t}",
	"\treturn `refund_${request.orderId}`;",
	"}",
	"",
}, "\n")

// paymentsClientSource is the retry-window claim's source file — the
// UNCHANGED-file contrast partner of the V-P1-* scenarios; its line 12 is the
// claim's declared `source.line`.
var paymentsClientSource = strings.Join([]string{
	"// Payments API client (fixture source of the retry-window claim).",
	"",
	"export interface RefundClientOptions {",
	"\tbaseUrl: string;",
	"\ttimeoutMs: number;",
	"}",
	"",
	"export class RefundClient {",
	"\tconstructor(private readonly options: RefundClientOptions) {}",
	"",
	"\tasync refund(orderId: string): Promise<string> {",
	"\t\t// Retries are safe within a 24-hour idempotency window.",
	"\t\treturn this.post(`/v1/refunds/${orderId}`);",
	"\t}",
	"}",
	"",
}, "\n")

// paymentsTypesSource is the deleted-file orphan target of the V-P1-6
// scenario.
var paymentsTypesSource = strings.Join([]string{
	"// Payments domain types (fixture source of the deleted-file orphan scenario).",
	"",
	"export type RefundStatus = 'pending' | 'settled' | 'failed';",
	"",
	"export interface RefundRecord {",
	"\tid: string;",
	"\torderId: string;",
	"\tstatus: RefundStatus;",
	"}",
	"",
}, "\n")

// paymentsManifestYML maps docs/payments.md in `auto` mode ← src/payments/**
// + src/api/payments/** (the fixture's D1 allowlist).
var paymentsManifestYML = strings.Join([]string{
	"# docSync manifest — payments fixture (auto-shippable payment docs).",
	"version: 1",
	"documents:",
	"  - path: docs/payments.md",
	"    mode: auto",
	"    sources:",
	"      - src/payments/**",
	"      - src/api/payments/**",
	"    triggers:",
	"      - public_behavior",
	"      - api_change",
	"    ignore:",
	"      - internal_refactor",
	"      - test_only",
	"",
}, "\n")

// paymentsIdempotencyClaimText is the idempotency claim's text — the SAME
// sentence docs/payments.md line 8 carries (the claim quotes its anchor).
const paymentsIdempotencyClaimText = "Refund requests require an `idempotencyKey`."

// paymentsRetryWindowClaimText is the retry-window claim's text — the SAME
// bullet docs/payments.md line 12 carries.
const paymentsRetryWindowClaimText = "- Retries are safe within a 24-hour idempotency window."

// PaymentsConceptsDocument renders the fixture concepts document for
// `.docsync/concepts.yml` with the REAL git blob shas of the two fixture
// sources (provenance or it didn't happen).
//
// DEVIATION — the TS fixture wrote concepts.yml as YAML; this renders the SAME
// document in the JSON tile (YAML 1.2 is a superset of JSON and
// schemas.ParseConcepts detects+decodes JSON first, so the document is
// identical either way). The cause is a gap in the Go port of
// internal/schemas, filed separately: the shared `docsync.claim/v1`
// `schemas.ClaimSource` carries JSON tags but NO yaml tags, so a YAML concepts
// file decodes `source.blob_sha` to the empty string — a value intake
// REQUIRES — and ParseConcepts then fails closed on a perfectly valid fixture.
// Flip this back to YAML the moment ClaimSource gains
// `yaml:"blob_sha"` / `yaml:"line"`.
func PaymentsConceptsDocument(refundBlobSHA, clientBlobSHA string) string {
	return fmt.Sprintf(`{
  "schema": "docsync.concepts/v1",
  "docs": [
    {
      "path": "docs/payments.md",
      "claims": [
        {
          "id": %q,
          "text": %q,
          "anchor": {"file": "docs/payments.md", "start_line": 8, "end_line": 8},
          "source": {"file": "src/payments/refund.ts", "line": 10, "blob_sha": %q}
        },
        {
          "id": %q,
          "text": %q,
          "anchor": {"file": "docs/payments.md", "start_line": 12, "end_line": 12},
          "source": {"file": "src/api/payments/client.ts", "line": 12, "blob_sha": %q}
        }
      ]
    }
  ]
}
`, ClaimIdempotency, paymentsIdempotencyClaimText, refundBlobSHA,
		ClaimRetryWindow, paymentsRetryWindowClaimText, clientBlobSHA)
}

// WritePaymentsFixtureTree writes the payments fixture files under root:
// the manifest, the document and the three sources. Concepts are NOT written
// here (they need the sources' real git blob shas — see
// WritePaymentsConceptsFile).
func WritePaymentsFixtureTree(root string) error {
	files := []struct {
		relPath string
		content string
	}{
		{".docsync/manifest.yml", paymentsManifestYML},
		{PaymentsDoc, PaymentsDocContent},
		{"src/payments/refund.ts", paymentsRefundSource},
		{"src/payments/types.ts", paymentsTypesSource},
		{"src/api/payments/client.ts", paymentsClientSource},
	}
	for _, file := range files {
		if err := WriteRepoFile(root, file.relPath, file.content); err != nil {
			return err
		}
	}
	return nil
}

// WritePaymentsConceptsFile computes the REAL git blob shas of the fixture
// sources (the git binary's content identity — the same values a committed
// repo would carry) and writes `.docsync/concepts.yml`. It performs no
// repository construction: `git hash-object` hashes content only.
func WritePaymentsConceptsFile(root string) error {
	if _, err := os.Stat(filepath.Join(root, PaymentsDoc)); err != nil {
		return fmt.Errorf("write payments concepts: fixture tree missing under %q: %w", root, err)
	}
	refundBlobSHA, err := GitBlobSHA(root, "src/payments/refund.ts")
	if err != nil {
		return err
	}
	clientBlobSHA, err := GitBlobSHA(root, "src/api/payments/client.ts")
	if err != nil {
		return err
	}
	relPath := filepath.Join(schemas.DocSyncDir, "concepts.yml")
	if err := WriteRepoFile(root, relPath, PaymentsConceptsDocument(refundBlobSHA, clientBlobSHA)); err != nil {
		return err
	}
	return nil
}

// AssertPaymentsAnchors asserts the anchor lines the fixture concepts.yml
// declares still carry the expected span text (fail loud on fixture drift —
// protects intake anchors and the committed goldens against silent fixture
// shifts) (TS assertPaymentAnchors).
func AssertPaymentsAnchors(root string) error {
	content, err := ReadRepoFile(root, PaymentsDoc)
	if err != nil {
		return err
	}
	lines := claims.SplitDocLines(content)
	line8 := lineAt(lines, 8)
	if line8 != "Refund requests require an `idempotencyKey`." {
		return fmt.Errorf(
			"payments fixture drifted: docs/payments.md line 8 is %s — update the concepts anchors",
			strconv.Quote(line8))
	}
	line12 := lineAt(lines, 12)
	if line12 != "- Retries are safe within a 24-hour idempotency window." {
		return fmt.Errorf(
			"payments fixture drifted: docs/payments.md line 12 is %s — update the concepts anchors",
			strconv.Quote(line12))
	}
	return nil
}

// lineAt is the 1-addressable line of a SplitDocLines result ("" when out of
// range — the caller's drift message then names the absent line).
func lineAt(lines []string, number int) string {
	if number < 1 || number > len(lines) {
		return ""
	}
	return lines[number-1]
}

/* ----------------------------------------------------- payments fixture */

// PaymentsPipeline is the materialized payments fixture: the root, the
// manifest, the parsed concepts, the composed graph and the claim store
// (TS PaymentsPipelineRepo).
type PaymentsPipeline struct {
	FixtureRepo
	// Manifest is the parsed fixture manifest.
	Manifest schemas.Manifest
	// Concepts is the parsed fixture concepts config.
	Concepts schemas.ConceptsConfig
	// ConceptsRaw is the raw concepts.yml text (graph line diagnostics).
	ConceptsRaw string
	// Graph is the document graph composed from the REAL manifest + concepts.
	Graph graph.DocumentGraph
	// Claims is the claim store over the fixture's state store.
	Claims *claims.ClaimStore
}

// BuildPaymentsPipeline materializes the payments fixture tree under root
// (which the caller owns — a temp dir — and has git-initialized if git
// plumbing is wanted), parses the manifest/concepts, composes the graph and
// binds the state/claim stores. The base commit is NOT made here: callers
// commit and then seed state (TS createPaymentsPipelineRepo).
func BuildPaymentsPipeline(root string) (*PaymentsPipeline, error) {
	if err := WritePaymentsFixtureTree(root); err != nil {
		return nil, err
	}
	if err := WritePaymentsConceptsFile(root); err != nil {
		return nil, err
	}
	if err := AssertPaymentsAnchors(root); err != nil {
		return nil, err
	}

	manifestRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "manifest.yml"))
	if err != nil {
		return nil, err
	}
	manifest, err := schemas.ParseManifest([]byte(manifestRaw))
	if err != nil {
		return nil, fmt.Errorf("payments fixture manifest is invalid: %w", err)
	}

	conceptsRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "concepts.yml"))
	if err != nil {
		return nil, err
	}
	concepts, err := schemas.ParseConcepts([]byte(conceptsRaw))
	if err != nil {
		return nil, fmt.Errorf("payments fixture concepts are invalid: %w", err)
	}

	documentGraph := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest:    *manifest,
		Concepts:    concepts,
		ConceptsRaw: conceptsRaw,
	})
	if len(documentGraph.Errors) > 0 {
		return nil, fmt.Errorf("payments fixture graph has configuration errors: %s",
			renderGraphErrors(documentGraph.Errors))
	}

	repo, err := NewFixtureRepo(root)
	if err != nil {
		return nil, err
	}
	return &PaymentsPipeline{
		FixtureRepo: *repo,
		Manifest:    *manifest,
		Concepts:    *concepts,
		ConceptsRaw: conceptsRaw,
		Graph:       documentGraph,
		Claims:      claims.NewClaimStore(repo.Store),
	}, nil
}

// IntakeHandAnchoredClaims intakes the two hand-anchored claims into the
// store under the fixed fixture clock (TS intakeHandAnchoredClaims).
func (p *PaymentsPipeline) IntakeHandAnchoredClaims() (claims.IntakeReport, error) {
	root := p.Root
	return claims.IntakeClaims(claims.IntakeClaimInput{
		Concepts: p.Concepts,
		DocContents: func(docPath string) *string {
			content, err := ReadRepoFile(root, docPath)
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				// An unreadable document is not "absent": surface it as an
				// empty read so intake reports the failure deterministically.
				empty := ""
				return &empty
			}
			return &content
		},
		RunID: PipelineInitRun,
		Store: p.Claims,
		Now:   func() string { return PipelineT0 },
	})
}

// SeedVerifiedDoc initializes PaymentsDoc VERIFIED and records its baseline
// against baselineCommit — `source_files` are the literal file identities the
// graph's forward lookup resolves at verification time (sorted, real files)
// (TS seedVerifiedPaymentsDoc).
func (p *PaymentsPipeline) SeedVerifiedDoc(baselineCommit string) error {
	machine := statemachine.NewDocStateMachine(p.Store)
	initialized, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       PaymentsDoc,
		To:            schemas.DocStateVerified,
		RunID:         PipelineInitRun,
		PolicyVersion: PipelinePolicyVersion,
		At:            PipelineT0,
	})
	if err != nil {
		return fmt.Errorf("doc state initialize failed: %w", err)
	}
	if !initialized.OK {
		return fmt.Errorf("doc state initialize failed: %+v", initialized)
	}

	mapping := graph.ForwardLookup(p.Graph, PaymentsDoc)
	if mapping == nil {
		return errors.New("payments fixture doc is not mapped")
	}
	allFiles, err := p.Files()
	if err != nil {
		return err
	}
	sourceFiles := []string{}
	for _, file := range allFiles {
		for _, source := range mapping.Sources {
			matches, err := graph.SourceMatchesPath(source, file)
			if err != nil {
				return err
			}
			if matches {
				sourceFiles = append(sourceFiles, file)
				break
			}
		}
	}
	if len(sourceFiles) == 0 {
		return errors.New("payments fixture resolved an empty source file set for docs/payments.md")
	}

	docContent, err := p.Read(PaymentsDoc)
	if err != nil {
		return err
	}
	baseline := schemas.Baseline{
		DocPath:        PaymentsDoc,
		DocContentHash: docutil.ContentHash(docContent),
		BaselineCommit: baselineCommit,
		SourceFiles:    sourceFiles,
		SourceSymbols:  []string{},
		ClaimIDs:       []string{ClaimIdempotency, ClaimRetryWindow},
		VerifiedAt:     PipelineT0,
		VerifiedByRun:  PipelineInitRun,
		PolicyVersion:  PipelinePolicyVersion,
	}
	upsert, err := baselines.NewBaselineStore(p.Store).Upsert(baseline, baselines.BaselineGuards{
		ExpectedDocContentHash: baseline.DocContentHash,
		ExpectedBaselineCommit: baselineCommit,
	})
	if err != nil {
		return fmt.Errorf("baseline upsert failed: %w", err)
	}
	if !upsert.OK {
		return fmt.Errorf("baseline upsert failed: %s (%s)", upsert.Kind, upsert.Detail)
	}
	return nil
}

// PipelinePolicy is the minimal CompiledPolicyWorkflow-shaped payload the
// policy stage requires (`policy_version`, read STRUCTURALLY by the runner).
// The policy engine itself is the caller's PolicyStagePort — core never
// interprets policy internals (D11, OD-1).
func PipelinePolicy(version int) map[string]any {
	return map[string]any{"policy_version": version}
}

// PipelineInput builds the orchestrator run input over this fixture repo:
// the fixture manifest, the minimal policy pointer, the FIXED fixture clock
// (no wall clock inside logic under test) and the caller's change window.
// Graph/Caps/Budgets stay nil — the runner composes and defaults them (TS the
// suites' hand-built runs).
func (p *PaymentsPipeline) PipelineInput(base, head string) orchestrator.PipelineInput {
	return orchestrator.PipelineInput{
		Repo:     PaymentsRepoID,
		RepoRoot: p.Root,
		Base:     base,
		Head:     head,
		Manifest: p.Manifest,
		Policy:   PipelinePolicy(PipelinePolicyVersion),
		Clock:    func() string { return PipelineT0 },
	}
}

// RunPipeline runs one orchestrator pipeline run over this fixture with the
// given keyed fakes wired onto the port interfaces (the Go fixture builder's
// job the TS suites did by hand: real repo, recorded stage doubles).
func (p *PaymentsPipeline) RunPipeline(ctx context.Context, fakes *PipelineFakes, base, head string) (*schemas.RunOutcome, error) {
	return orchestrator.RunPipeline(ctx, p.PipelineInput(base, head), fakes.Ports())
}

/* ------------------------------------------------ monorepo-scale fixture */

// MonorepoPipeline is the materialized monorepo-scale fixture (V-P1-4: cap
// breach material) (TS MonorepoPipelineRepo).
type MonorepoPipeline struct {
	FixtureRepo
	// Manifest is the parsed fixture manifest (one doc per package).
	Manifest schemas.Manifest
	// Graph is the composed document graph (no concepts — V-P1-4 exercises
	// the CAP stage, not claims).
	Graph graph.DocumentGraph
	// PackageCount / AreaCount / FileCount are the generated dimensions.
	PackageCount int
	AreaCount    int
	FileCount    int
}

// monorepoPackageName is the package letter of an index: a, b, … (TS the
// [a-h] fixture packages).
func monorepoPackageName(index int) string {
	return string(rune('a' + index))
}

// monorepoDocPath is the mapped document of one package (TS
// `docs/packages/<pkg>.md`).
func monorepoDocPath(pkg string) string {
	return "docs/packages/" + pkg + ".md"
}

// WriteMonorepoFixtureTree writes the generated monorepo-scale tree under
// root: packageCount packages × areaCount areas × fileCount TS files plus one
// doc per package and the manifest that maps them (TS the checked-in
// fixtures/monorepo-scale generated tree).
func WriteMonorepoFixtureTree(root string, packageCount, areaCount, fileCount int) error {
	if packageCount < 1 || packageCount > 26 || areaCount < 1 || fileCount < 1 {
		return fmt.Errorf(
			"monorepo dimensions out of range: packages=%d (1..26), areas=%d (>=1), files=%d (>=1)",
			packageCount, areaCount, fileCount)
	}
	manifestLines := []string{
		"# docSync manifest — monorepo-scale fixture (one doc per package).",
		"version: 1",
		"documents:",
	}
	for packageIndex := 0; packageIndex < packageCount; packageIndex++ {
		pkg := monorepoPackageName(packageIndex)
		manifestLines = append(manifestLines,
			"  - path: "+monorepoDocPath(pkg),
			"    mode: auto",
			"    sources:",
			"      - packages/"+pkg+"/src/**",
		)
		for area := 1; area <= areaCount; area++ {
			for file := 1; file <= fileCount; file++ {
				relPath := "packages/" + pkg + "/src/area" + strconv.Itoa(area) +
					"/file" + strconv.Itoa(file) + ".ts"
				content := "// Generated fixture source: " + relPath + "\n" +
					"export const area" + strconv.Itoa(area) + "_file" + strconv.Itoa(file) +
					" = \"" + pkg + "\";\n"
				if err := WriteRepoFile(root, relPath, content); err != nil {
					return err
				}
			}
		}
		docContent := "# Package " + pkg + "\n\n" +
			"Reference documentation for `packages/" + pkg + "`.\n\n" +
			"## Usage\n\n" +
			"Import from `packages/" + pkg + "/src`.\n"
		if err := WriteRepoFile(root, monorepoDocPath(pkg), docContent); err != nil {
			return err
		}
	}
	manifestLines = append(manifestLines, "")
	return WriteRepoFile(root, filepath.Join(schemas.DocSyncDir, "manifest.yml"),
		strings.Join(manifestLines, "\n"))
}

// BuildMonorepoPipeline materializes the generated monorepo-scale tree under
// root (gits, manifest and graph; no concepts.yml) with the default 8 × 10 × 5
// dimensions (TS createMonorepoPipelineRepo).
func BuildMonorepoPipeline(root string) (*MonorepoPipeline, error) {
	return BuildMonorepoPipelineSized(root, MonorepoPackageCount, MonorepoAreaCount, MonorepoFileCount)
}

// BuildMonorepoPipelineSized is BuildMonorepoPipeline with explicit
// dimensions (smaller trees keep tests fast; the default is the V-P1-4 scale).
func BuildMonorepoPipelineSized(root string, packageCount, areaCount, fileCount int) (*MonorepoPipeline, error) {
	if err := WriteMonorepoFixtureTree(root, packageCount, areaCount, fileCount); err != nil {
		return nil, err
	}
	manifestRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "manifest.yml"))
	if err != nil {
		return nil, err
	}
	manifest, err := schemas.ParseManifest([]byte(manifestRaw))
	if err != nil {
		return nil, fmt.Errorf("monorepo fixture manifest is invalid: %w", err)
	}
	documentGraph := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: *manifest})
	if len(documentGraph.Errors) > 0 {
		return nil, fmt.Errorf("monorepo fixture graph has configuration errors: %s",
			renderGraphErrors(documentGraph.Errors))
	}
	repo, err := NewFixtureRepo(root)
	if err != nil {
		return nil, err
	}
	return &MonorepoPipeline{
		FixtureRepo:  *repo,
		Manifest:     *manifest,
		Graph:        documentGraph,
		PackageCount: packageCount,
		AreaCount:    areaCount,
		FileCount:    fileCount,
	}, nil
}

// monorepoDocPathPattern is the fixture doc-path shape SeedVerifiedDoc
// accepts (TS the /^docs\/packages\/([a-h])\.md$/ check).
var monorepoDocPathPattern = regexp.MustCompile(`^docs/packages/([a-h])\.md$`)

// SeedVerifiedDoc initializes one `docs/packages/<pkg>.md` VERIFIED and
// records its baseline against baselineCommit; `source_files` are the
// package's generated source files (the forward-lookup resolution of
// `packages/<pkg>/src/**`) (TS seedVerifiedMonorepoDoc).
func (p *MonorepoPipeline) SeedVerifiedDoc(docPath, baselineCommit string) error {
	match := monorepoDocPathPattern.FindStringSubmatch(docPath)
	if match == nil || match[1] == "" {
		return fmt.Errorf("not a monorepo fixture doc path: %q", docPath)
	}
	pkg := match[1]
	machine := statemachine.NewDocStateMachine(p.Store)
	initialized, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       docPath,
		To:            schemas.DocStateVerified,
		RunID:         PipelineInitRun,
		PolicyVersion: PipelinePolicyVersion,
		At:            PipelineT0,
	})
	if err != nil {
		return fmt.Errorf("doc state initialize failed: %w", err)
	}
	if !initialized.OK {
		return fmt.Errorf("doc state initialize failed: %+v", initialized)
	}

	sourceFiles, err := p.Files("packages/" + pkg + "/src")
	if err != nil {
		return err
	}
	if len(sourceFiles) == 0 {
		return fmt.Errorf("monorepo fixture resolved an empty source file set for %s", docPath)
	}
	docContent, err := p.Read(docPath)
	if err != nil {
		return err
	}
	baseline := schemas.Baseline{
		DocPath:        docPath,
		DocContentHash: docutil.ContentHash(docContent),
		BaselineCommit: baselineCommit,
		SourceFiles:    sourceFiles,
		SourceSymbols:  []string{},
		ClaimIDs:       []string{},
		VerifiedAt:     PipelineT0,
		VerifiedByRun:  PipelineInitRun,
		PolicyVersion:  PipelinePolicyVersion,
	}
	upsert, err := baselines.NewBaselineStore(p.Store).Upsert(baseline, baselines.BaselineGuards{
		ExpectedDocContentHash: baseline.DocContentHash,
		ExpectedBaselineCommit: baselineCommit,
	})
	if err != nil {
		return fmt.Errorf("baseline upsert failed: %w", err)
	}
	if !upsert.OK {
		return fmt.Errorf("baseline upsert failed: %s (%s)", upsert.Kind, upsert.Detail)
	}
	return nil
}

// PipelineInput builds the orchestrator run input over this fixture repo with
// the fixed fixture clock and the minimal policy pointer.
func (p *MonorepoPipeline) PipelineInput(base, head string) orchestrator.PipelineInput {
	return orchestrator.PipelineInput{
		Repo:     MonorepoRepoID,
		RepoRoot: p.Root,
		Base:     base,
		Head:     head,
		Manifest: p.Manifest,
		Policy:   PipelinePolicy(PipelinePolicyVersion),
		Clock:    func() string { return PipelineT0 },
	}
}

// RunPipeline runs one orchestrator pipeline run over this fixture with the
// given keyed fakes wired onto the port interfaces.
func (p *MonorepoPipeline) RunPipeline(ctx context.Context, fakes *PipelineFakes, base, head string) (*schemas.RunOutcome, error) {
	return orchestrator.RunPipeline(ctx, p.PipelineInput(base, head), fakes.Ports())
}

/* ------------------------------------------------------------------ helpers */

// renderGraphErrors renders a graph configuration-error list deterministically
// (path: message, declaration order) for the fail-closed fixture messages (TS
// JSON.stringify(graph.errors)).
func renderGraphErrors(graphErrors []graph.GraphConfigError) string {
	parts := make([]string, 0, len(graphErrors))
	for _, graphError := range graphErrors {
		parts = append(parts, graphError.Path+": "+graphError.Message)
	}
	return "[" + strings.Join(parts, "; ") + "]"
}
