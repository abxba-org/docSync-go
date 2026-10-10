package gitx

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// GitClient is typed access to the system git binary — rev resolution, tree
// hashes, blob reads, tree listing, rename-aware diffs with per-file patch
// text, numstat counts, patch-id, and the history-walk primitives. The core
// API stays READ-ONLY; the only write surface is the narrow Phase-6 PR-write
// plumbing (branch creation, exact-path staging, staged-path listing, plain
// commit, one-branch push, status, rebase) — each scoped and fail-closed.
//
// D12 fidelity: TreeSha and PatchId are the ladder rungs "same tree hash" and
// "same patch text" — both computed from pinned, deterministic git invocations
// (see exec.go) so identical trees / identical textual diffs hash identically
// across squash-like re-commits.
//
// Per-file patch text strategy: DiffFiles runs ONE raw status pass
// (`diff --raw -z --find-renames`) whose records are authoritative for
// status/paths/similarity, and ONE full textual diff (`diff --find-renames
// --no-color --no-ext-diff`) parsed into per-file sections. Sections attach to
// records BY ORDER with strict length equality; any mismatch throws (fail
// closed, D14) instead of attaching a wrong patch.
//
// Every failure is a typed *GitExecError — no method can return an "empty
// success" for a broken git run (D14 groundwork, V-P1-10).
type GitClient struct {
	repoRoot  string
	executor  GitExecutor
	timeoutMs int
}

// GitClientOptions configures a GitClient.
type GitClientOptions struct {
	// RepoRoot is the absolute repository root (passed to git as `-C <root>`).
	RepoRoot string
	// Executor is the injection seam (nil = the system git binary).
	Executor GitExecutor
	// TimeoutMs is the per-invocation timeout forwarded to the executor.
	TimeoutMs int
}

// NewGitClient validates the repo root and binds the client to it.
func NewGitClient(opts GitClientOptions) (*GitClient, error) {
	root, err := ValidateRepoRoot(opts.RepoRoot)
	if err != nil {
		return nil, err
	}
	executor := opts.Executor
	if executor == nil {
		executor = CreateSystemGitExecutor(nil)
	}
	return &GitClient{repoRoot: root, executor: executor, timeoutMs: opts.TimeoutMs}, nil
}

// Root is the validated absolute repository root this client is bound to.
func (c *GitClient) Root() string { return c.repoRoot }

// CatFileResult is a blob read: its sha plus its content.
type CatFileResult struct {
	BlobSha string `json:"blob_sha"`
	Content string `json:"content"`
}

// LsTreeEntry is one `ls-tree -z -r` record.
type LsTreeEntry struct {
	Mode string `json:"mode"`
	Type string `json:"type"`
	Sha  string `json:"sha"`
	Path string `json:"path"`
}

// DiffFileStatus is the routing-relevant status letter mapping.
type DiffFileStatus string

// Diff file statuses (TS DiffFileStatus).
const (
	DiffStatusAdded    DiffFileStatus = "added"
	DiffStatusModified DiffFileStatus = "modified"
	DiffStatusDeleted  DiffFileStatus = "deleted"
	DiffStatusRenamed  DiffFileStatus = "renamed"
)

// DiffFileRecord is one rename-aware diff record with its raw patch text.
type DiffFileRecord struct {
	Path string `json:"path"`
	// OldPath is the original path for renames (nil otherwise).
	OldPath *string        `json:"old_path,omitempty"`
	Status  DiffFileStatus `json:"status"`
	// Similarity is the 0-100 similarity score for renames (nil otherwise).
	Similarity *int   `json:"similarity,omitempty"`
	Patch      string `json:"patch"`
}

// DiffStatRecord is one per-file addition/deletion count record.
type DiffStatRecord struct {
	Path string `json:"path"`
	// OldPath is the original path for renames (nil otherwise).
	OldPath *string `json:"old_path,omitempty"`
	// Additions / Deletions are nil for binary files.
	Additions *int `json:"additions"`
	Deletions *int `json:"deletions"`
	Binary    bool `json:"binary"`
}

// LogCommit is one commit of a history walk: its sha plus its parents (first
// parent first).
type LogCommit struct {
	Sha     string   `json:"sha"`
	Parents []string `json:"parents"`
}

// RebaseBranchResult is a clean replay, or a conflict that was aborted.
type RebaseBranchResult struct {
	// Ok is true when the replay was clean.
	Ok bool `json:"ok"`
	// Conflicted is true when the replay conflicted and was aborted.
	Conflicted bool `json:"conflicted"`
}

// RebaseOK builds the clean-replay result.
func RebaseOK() RebaseBranchResult { return RebaseBranchResult{Ok: true} }

// RebaseConflicted builds the aborted-conflict result.
func RebaseConflicted() RebaseBranchResult { return RebaseBranchResult{Conflicted: true} }

// hex40 matches a full 40-hex object name (TS HEX40); hexDigits matches the
// nonempty hex token `git patch-id` must emit.
var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

var hexDigits = regexp.MustCompile(`^[0-9a-f]+$`)

/* ------------------------------------------------------------- public API */

// ResolveRev returns the full 40-hex commit sha; unknown ref → unknown-ref.
func (c *GitClient) ResolveRev(ctx context.Context, ref string) (string, error) {
	safe, err := ValidateRefArg(ref, "ref")
	if err != nil {
		return "", err
	}
	out, err := c.runOk(ctx, []string{"rev-parse", "--verify", safe + "^{commit}"}, "rev-parse", KindUnknownRef, nil)
	if err != nil {
		return "", err
	}
	sha := trimText(out.StdoutText())
	if err := c.assertSha(sha, "rev-parse"); err != nil {
		return "", err
	}
	return sha, nil
}

// TreeSha returns the tree hash of a commit-ish (D12 ladder rung: "same tree
// hash").
func (c *GitClient) TreeSha(ctx context.Context, rev string) (string, error) {
	safe, err := ValidateRefArg(rev, "rev")
	if err != nil {
		return "", err
	}
	out, err := c.runOk(ctx, []string{"rev-parse", "--verify", safe + "^{tree}"}, "rev-parse", "", nil)
	if err != nil {
		return "", err
	}
	sha := trimText(out.StdoutText())
	if err := c.assertSha(sha, "rev-parse"); err != nil {
		return "", err
	}
	return sha, nil
}

// MergeBase returns the merge base sha, or "" ONLY for genuinely unrelated
// histories (git `merge-base` exits 1 with empty output when there is no common
// ancestor). Any other failure (bad revision, exec failure) still errors.
func (c *GitClient) MergeBase(ctx context.Context, a, b string) (string, error) {
	safeA, err := ValidateRefArg(a, "ref a")
	if err != nil {
		return "", err
	}
	safeB, err := ValidateRefArg(b, "ref b")
	if err != nil {
		return "", err
	}
	result, err := c.runRaw(ctx, []string{"merge-base", safeA, safeB}, nil)
	if err != nil {
		return "", err
	}
	if result.ExitCode != nil && *result.ExitCode == 0 {
		sha := trimText(result.StdoutText())
		if err := c.assertSha(sha, "merge-base"); err != nil {
			return "", err
		}
		return sha, nil
	}
	if result.ExitCode != nil && *result.ExitCode == 1 && len(result.Stdout) == 0 && len(result.Stderr) == 0 {
		return "", nil
	}
	return "", ClassifyGitFailure("merge-base", result)
}

// CatFile returns the blob at `<rev>:<path>`: sha via rev-parse, content via
// cat-file.
func (c *GitClient) CatFile(ctx context.Context, rev, path string) (CatFileResult, error) {
	safeRev, err := ValidateRefArg(rev, "rev")
	if err != nil {
		return CatFileResult{}, err
	}
	safePath, err := ValidatePathArg(path, "path")
	if err != nil {
		return CatFileResult{}, err
	}
	// Two-step keeps error routing precise: a bad rev is unknown-ref
	// (ResolveRev), a missing path is missing-rev (rev-parse <sha>:<path>).
	commitSha, err := c.ResolveRev(ctx, safeRev)
	if err != nil {
		return CatFileResult{}, err
	}
	shaOut, err := c.runOk(ctx, []string{"rev-parse", "--verify", commitSha + ":" + safePath}, "rev-parse", KindMissingRev, nil)
	if err != nil {
		return CatFileResult{}, err
	}
	blobSha := trimText(shaOut.StdoutText())
	if err := c.assertSha(blobSha, "rev-parse"); err != nil {
		return CatFileResult{}, err
	}
	content, err := c.runOk(ctx, []string{"cat-file", "blob", blobSha}, "cat-file", KindMissingRev, nil)
	if err != nil {
		return CatFileResult{}, err
	}
	// Binary-safe capture; UTF-8 decoded here at the API boundary.
	return CatFileResult{BlobSha: blobSha, Content: content.StdoutText()}, nil
}

// LsTree lists the tree recursively (`ls-tree -z -r`) in git's canonical tree
// order. pathPrefix, when non-nil, restricts the listing to that path.
func (c *GitClient) LsTree(ctx context.Context, rev string, pathPrefix *string) ([]LsTreeEntry, error) {
	safeRev, err := ValidateRefArg(rev, "rev")
	if err != nil {
		return nil, err
	}
	var subArgs []string
	if pathPrefix == nil {
		subArgs = []string{"ls-tree", "-z", "-r", safeRev}
	} else {
		safePrefix, err := ValidatePathArg(*pathPrefix, "pathPrefix")
		if err != nil {
			return nil, err
		}
		subArgs = []string{"ls-tree", "-z", "-r", safeRev, "--", safePrefix}
	}
	out, err := c.runOk(ctx, subArgs, "ls-tree", "", nil)
	if err != nil {
		return nil, err
	}
	return parseLsTree(out.Stdout)
}

// DiffFiles returns rename-aware per-file diff records: authoritative statuses
// from `diff --raw -z --find-renames`, per-file patch text from one full
// `diff --find-renames` run parsed into sections.
func (c *GitClient) DiffFiles(ctx context.Context, base, head string) ([]DiffFileRecord, error) {
	safeBase, err := ValidateRefArg(base, "base")
	if err != nil {
		return nil, err
	}
	safeHead, err := ValidateRefArg(head, "head")
	if err != nil {
		return nil, err
	}
	rawOut, err := c.runOk(ctx, diffArgs(safeBase, safeHead, "--raw", "-z", "--find-renames"), "diff", "", nil)
	if err != nil {
		return nil, err
	}
	textOut, err := c.runOk(ctx, diffArgs(safeBase, safeHead, "--find-renames"), "diff", "", nil)
	if err != nil {
		return nil, err
	}
	records, err := parseDiffRaw(rawOut.Stdout)
	if err != nil {
		return nil, err
	}
	sections := splitDiffSections(textOut.StdoutText())
	if len(records) != len(sections) {
		return nil, NewGitExecError(KindExecFailure, fmt.Sprintf(
			"diff parse mismatch: %d raw records vs %d text sections for %s..%s",
			len(records), len(sections), safeBase, safeHead), &GitExecErrorOptions{Command: "diff"})
	}
	out := make([]DiffFileRecord, 0, len(records))
	for i, record := range records {
		attached, err := attachPatch(record, sections[i], i)
		if err != nil {
			return nil, err
		}
		out = append(out, attached)
	}
	return out, nil
}

// DiffStat returns per-file addition/deletion counts (`--numstat -z`); nulls
// for binaries.
func (c *GitClient) DiffStat(ctx context.Context, base, head string) ([]DiffStatRecord, error) {
	safeBase, err := ValidateRefArg(base, "base")
	if err != nil {
		return nil, err
	}
	safeHead, err := ValidateRefArg(head, "head")
	if err != nil {
		return nil, err
	}
	out, err := c.runOk(ctx, diffArgs(safeBase, safeHead, "--numstat", "-z", "--find-renames"), "diff", "", nil)
	if err != nil {
		return nil, err
	}
	return parseNumstat(out.Stdout)
}

// PatchId returns the `git patch-id --stable` id of the textual diff — the D12
// ladder rung "same patch text" — or "" ONLY when the diff is empty (no patch →
// no id); a failing diff run errors, it never degrades to "". The diff is piped
// into `patch-id` via executor stdin (args array, no shell, no temp files).
// path, when non-nil, restricts the diff to that path.
func (c *GitClient) PatchId(ctx context.Context, base, head string, path *string) (string, error) {
	safeBase, err := ValidateRefArg(base, "base")
	if err != nil {
		return "", err
	}
	safeHead, err := ValidateRefArg(head, "head")
	if err != nil {
		return "", err
	}
	args := diffArgs(safeBase, safeHead, "--find-renames")
	if path != nil {
		safePath, err := ValidatePathArg(*path, "path")
		if err != nil {
			return "", err
		}
		args = append(args, "--", safePath)
	}
	diffOut, err := c.runOk(ctx, args, "diff", "", nil)
	if err != nil {
		return "", err
	}
	if len(diffOut.Stdout) == 0 {
		return "", nil
	}
	pidOut, err := c.runOk(ctx, []string{"patch-id", "--stable"}, "patch-id", "", &GitRunOptions{Stdin: diffOut.Stdout})
	if err != nil {
		return "", err
	}
	text := trimText(pidOut.StdoutText())
	if len(text) == 0 {
		return "", nil // e.g. mode-only changes: no hunk, no id
	}
	id := strings.Fields(text)[0]
	if !hexDigits.MatchString(id) {
		return "", NewGitExecError(KindExecFailure, fmt.Sprintf("git patch-id produced non-hex output: %s", id),
			&GitExecErrorOptions{Command: "patch-id", StderrExcerpt: id})
	}
	return id, nil
}

// ParentShas returns all parent shas of a commit, first parent first
// (`rev-list --parents -n 1`). A root commit has no parents → an empty slice.
func (c *GitClient) ParentShas(ctx context.Context, rev string) ([]string, error) {
	safe, err := ValidateRefArg(rev, "rev")
	if err != nil {
		return nil, err
	}
	out, err := c.runOk(ctx, []string{"rev-list", "--parents", "-n", "1", safe}, "rev-list", "", nil)
	if err != nil {
		return nil, err
	}
	tokens := strings.Fields(trimText(out.StdoutText()))
	// tokens[0] is the commit itself; every further token is a parent sha.
	var parents []string
	if len(tokens) > 1 {
		parents = append([]string(nil), tokens[1:]...)
	}
	for _, sha := range parents {
		if err := c.assertSha(sha, "rev-list"); err != nil {
			return nil, err
		}
	}
	return parents, nil
}

// LogCommits is a deterministic capped commit walk (`log --topo-order
// --format=%H %P -n`): children before parents in a fixed graph order
// independent of commit dates, capped at maxCount (positive integer).
func (c *GitClient) LogCommits(ctx context.Context, rev string, maxCount int) ([]LogCommit, error) {
	safe, err := ValidateRefArg(rev, "rev")
	if err != nil {
		return nil, err
	}
	if maxCount < 1 {
		return nil, NewGitExecError(KindInvalidArg,
			fmt.Sprintf("git log maxCount must be a positive integer (got %d)", maxCount),
			&GitExecErrorOptions{Command: "log"})
	}
	out, err := c.runOk(ctx, []string{"log", "--topo-order", "--format=%H %P", fmt.Sprintf("--max-count=%d", maxCount), safe}, "log", "", nil)
	if err != nil {
		return nil, err
	}
	return parseLogCommits(out.Stdout)
}

/* -------------------------------------------- write-path plumbing (P6-T01/T02)
The ONLY mutating surface: the Phase-6 PR write path (branch factory +
doc-commit assembly). Every method is scoped and fail-closed — no sweeping
adds, no hook bypasses, no ref deletion, never anything beyond the explicitly
passed arguments (invariants 1-3 hold at the git layer). */

// CreateBranch creates the branch <name> at <startPoint> (default: current
// HEAD). A pre-existing branch name is a typed failure (`git branch` refuses
// it) — idempotency is decided by the caller, never here.
func (c *GitClient) CreateBranch(ctx context.Context, name string, startPoint *string) error {
	safeName, err := ValidateRefArg(name, "branch name")
	if err != nil {
		return err
	}
	var args []string
	if startPoint == nil {
		args = []string{"branch", safeName}
	} else {
		safeStart, err := ValidateRefArg(*startPoint, "start point")
		if err != nil {
			return err
		}
		args = []string{"branch", safeName, safeStart}
	}
	_, err = c.runOk(ctx, args, "branch", "", nil)
	return err
}

// StagePaths stages EXACT paths (`git add -- <paths...>`) — never sweeps the
// worktree.
func (c *GitClient) StagePaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return NewGitExecError(KindInvalidArg, "git add requires at least one path",
			&GitExecErrorOptions{Command: "add"})
	}
	args := make([]string, 0, len(paths)+3)
	args = append(args, "add", "--")
	for _, p := range paths {
		safe, err := ValidatePathArg(p, "path")
		if err != nil {
			return err
		}
		args = append(args, safe)
	}
	_, err := c.runOk(ctx, args, "add", "", nil)
	return err
}

// CachedFilePaths lists staged paths vs HEAD (`git diff --cached --name-only
// -z`), NUL-safe parse.
func (c *GitClient) CachedFilePaths(ctx context.Context) ([]string, error) {
	out, err := c.runOk(ctx, []string{"diff", "--cached", "--name-only", "-z"}, "diff", "", nil)
	if err != nil {
		return nil, err
	}
	return splitNonEmpty(out.StdoutText(), "\x00"), nil
}

// Commit runs a plain `git commit -m <message>` — NO hook bypass (hooks run in
// CI too; the machine never opts out of repository policy). The message must be
// nonempty and NUL-free (argv constraint). Returns the new HEAD sha.
func (c *GitClient) Commit(ctx context.Context, message string) (string, error) {
	if len(message) == 0 || strings.Contains(message, "\x00") {
		return "", NewGitExecError(KindInvalidArg, "git commit message must be nonempty and NUL-free",
			&GitExecErrorOptions{Command: "commit"})
	}
	if _, err := c.runOk(ctx, []string{"commit", "-m", message}, "commit", "", nil); err != nil {
		return "", err
	}
	return c.ResolveRev(ctx, "HEAD")
}

// PushBranch pushes ONE local branch to ONE remote (`git push <remote>
// <branch>`, optional `--force`). Scoped like every write here: exactly one
// refspec of the form `<branch>` (a bare branch name — never a wildcard, never
// `--all`/`--mirror`, never an explicit `<src>:<dst>` rename), the remote name
// validated by the same ref-arg guard. Default remote: `origin`. A rejected
// non-fast-forward is a typed error — the caller decides supersede/rebase
// semantics; this method never improvises.
func (c *GitClient) PushBranch(ctx context.Context, branch, remote string, force bool) error {
	safeBranch, err := ValidateRefArg(branch, "branch name")
	if err != nil {
		return err
	}
	if remote == "" {
		remote = "origin"
	}
	safeRemote, err := ValidateRefArg(remote, "remote")
	if err != nil {
		return err
	}
	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, safeRemote, safeBranch)
	_, err = c.runOk(ctx, args, "push", "", nil)
	return err
}

// StatusPorcelain returns working-tree/index status entries (`git status
// --porcelain`), one string per nonempty porcelain line — the rebase gate's
// clean-tree pre/post conditions. READ-ONLY.
func (c *GitClient) StatusPorcelain(ctx context.Context) ([]string, error) {
	out, err := c.runOk(ctx, []string{"status", "--porcelain"}, "status", "", nil)
	if err != nil {
		return nil, err
	}
	return splitNonEmpty(out.StdoutText(), "\n"), nil
}

// RebaseBranch rebases ONE branch onto ONE target ref (`git rebase <onto>
// <branch>`) — scoped like every write here: two validated ref names, nothing
// else (no `--force`, no ref surgery).
//
// A rebase CONFLICT is a semantic outcome, not a broken run: the method runs
// `git rebase --abort` — the ONLY undo it performs, restoring the branch to its
// exact pre-rebase state — and returns a conflicted result for the caller to
// abstain (A9). Any OTHER failure is a classified typed error (fail closed,
// D14) — never an improvised recovery.
func (c *GitClient) RebaseBranch(ctx context.Context, branch, onto string) (RebaseBranchResult, error) {
	safeBranch, err := ValidateRefArg(branch, "branch name")
	if err != nil {
		return RebaseBranchResult{}, err
	}
	safeOnto, err := ValidateRefArg(onto, "onto")
	if err != nil {
		return RebaseBranchResult{}, err
	}
	result, err := c.runRaw(ctx, []string{"rebase", safeOnto, safeBranch}, nil)
	if err != nil {
		return RebaseBranchResult{}, err
	}
	if result.ExitCode != nil && *result.ExitCode == 0 {
		return RebaseOK(), nil
	}
	output := result.StdoutText() + "\n" + result.StderrText()
	if strings.Contains(output, "CONFLICT") || strings.Contains(output, "could not apply") {
		if _, err := c.runOk(ctx, []string{"rebase", "--abort"}, "rebase --abort", "", nil); err != nil {
			return RebaseBranchResult{}, err
		}
		return RebaseConflicted(), nil
	}
	return RebaseBranchResult{}, ClassifyGitFailure("rebase", result)
}

/* ---------------------------------------------------------------- plumbing */

// diffArgs assembles the determinism-pinned argv for a diff invocation:
// config pins first, then the subcommand flags, then the output flags and the
// two revs (later `-c` args win, so pins always prevail over the environment).
func diffArgs(base, head string, flags ...string) []string {
	args := make([]string, 0, len(GITDiffConfigPins)+len(flags)+len(GITDiffOutputArgs)+4)
	args = append(args, GITDiffConfigPins...)
	args = append(args, "diff")
	args = append(args, flags...)
	args = append(args, GITDiffOutputArgs...)
	args = append(args, base, head)
	return args
}

// runRaw runs git with `-C <absRoot>` + the global determinism flag leading the
// argv.
func (c *GitClient) runRaw(ctx context.Context, subArgs []string, opts *GitRunOptions) (GitRunResult, error) {
	args := make([]string, 0, len(subArgs)+6)
	args = append(args, "-C", c.repoRoot)
	args = append(args, GITQuotePathArgs...)
	args = append(args, subArgs...)

	effective := &GitRunOptions{TimeoutMs: c.timeoutMs}
	if opts != nil {
		effective.Stdin = opts.Stdin
		if opts.TimeoutMs != 0 {
			effective.TimeoutMs = opts.TimeoutMs
		}
	}
	return c.executor.Run(ctx, args, effective)
}

// runOk runs git and classifies any failure; a non-zero exit never passes as an
// empty success.
func (c *GitClient) runOk(ctx context.Context, subArgs []string, command string, kindHint GitExecErrorKind, opts *GitRunOptions) (GitRunResult, error) {
	result, err := c.runRaw(ctx, subArgs, opts)
	if err != nil {
		return GitRunResult{}, err
	}
	if result.Failed() {
		return GitRunResult{}, ClassifyGitFailure(command, result, kindHint)
	}
	return result, nil
}

func (c *GitClient) assertSha(value, command string) error {
	if !hex40.MatchString(value) {
		return NewGitExecError(KindExecFailure,
			fmt.Sprintf("git %s returned a non-sha value: %s", command, value),
			&GitExecErrorOptions{Command: command, StderrExcerpt: firstRunes(value, 64)})
	}
	return nil
}

func attachPatch(record rawDiffRecord, section diffSection, index int) (DiffFileRecord, error) {
	if section.text == "" {
		return DiffFileRecord{}, NewGitExecError(KindExecFailure,
			fmt.Sprintf("diff section #%d missing or empty for path %s (raw/text mismatch)", index, tsJSONString(record.path)),
			&GitExecErrorOptions{Command: "diff"})
	}
	if err := assertSectionMatchesRecord(section, record, index); err != nil {
		return DiffFileRecord{}, err
	}
	out := DiffFileRecord{Path: record.path, Status: record.status, Patch: section.text}
	if record.oldPath != nil {
		out.OldPath = record.oldPath
	}
	if record.similarity != nil {
		out.Similarity = record.similarity
	}
	return out, nil
}

/* --------------------------------------------------------------- raw parser */

type rawDiffRecord struct {
	path       string
	oldPath    *string
	status     DiffFileStatus
	similarity *int
}

// parseDiffRaw parses `diff --raw -z --find-renames` output. Format (verified
// against git 2.53): with `-z`, EVERY field terminator is NUL — each record is
// `<:modes> <shas> <STATUS><score> NUL <path> NUL`, and rename/copy records
// carry TWO path chunks — ORIGINAL path first, then NEW path. Paths are raw
// (unquoted) thanks to `core.quotepath=false` and `-z`.
func parseDiffRaw(stdout []byte) ([]rawDiffRecord, error) {
	chunks := strings.Split(string(stdout), "\x00")
	records := make([]rawDiffRecord, 0, 8)
	index := 0
	for index < len(chunks) {
		metaChunk := chunks[index]
		index++
		if len(metaChunk) == 0 {
			continue
		}
		if !strings.HasPrefix(metaChunk, ":") {
			return nil, malformedRawRecord(metaChunk)
		}
		meta := strings.Split(metaChunk[1:], " ")
		statusField := fieldAt(meta, 4)
		if len(statusField) == 0 {
			return nil, malformedRawRecordNoStatus(metaChunk)
		}
		letter := statusField[:1]
		score := statusField[1:]
		firstPath := chunkAt(chunks, index)
		index++
		if len(firstPath) == 0 {
			return nil, malformedRawRecordPlain("missing path")
		}
		if letter == "R" || letter == "C" {
			newPath := chunkAt(chunks, index)
			index++
			if len(newPath) == 0 {
				return nil, malformedRenameRecord()
			}
			if !renameScore.MatchString(score) {
				return nil, malformedRenameScore(statusField)
			}
			similarity, err := strconv.Atoi(score)
			if err != nil {
				return nil, malformedRenameScore(statusField)
			}
			records = append(records, rawDiffRecord{
				path:       newPath,
				oldPath:    strPtr(firstPath),
				status:     DiffStatusRenamed,
				similarity: intPtr(similarity),
			})
			continue
		}
		status, err := mapStatusLetter(letter)
		if err != nil {
			return nil, err
		}
		records = append(records, rawDiffRecord{path: firstPath, status: status})
	}
	return records, nil
}

var renameScore = regexp.MustCompile(`^\d{1,3}$`)

func mapStatusLetter(letter string) (DiffFileStatus, error) {
	switch letter {
	case "A":
		return DiffStatusAdded, nil
	case "M", "T":
		return DiffStatusModified, nil
	case "D":
		return DiffStatusDeleted, nil
	case "R", "C":
		return DiffStatusRenamed, nil
	default:
		// Fail closed: an unmapped status letter must never pass as a known one.
		return "", NewGitExecError(KindExecFailure,
			fmt.Sprintf("unmapped diff status letter: %s", tsJSONString(letter)),
			&GitExecErrorOptions{Command: "diff"})
	}
}

/* ---------------------------------------------------------- text diff parser */

type diffSection struct {
	headerLine string
	text       string
}

// splitDiffSections splits one full textual diff into per-file sections. Each
// section starts at a `diff --git ` line and extends to (not including) the
// next one.
func splitDiffSections(diffText string) []diffSection {
	var sections []diffSection
	current := newSection()
	started := false
	for _, line := range strings.Split(diffText, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			if started {
				sections = append(sections, current.finish())
			}
			current = newSection()
			started = true
			current.lines = append(current.lines, line)
			current.headerLine = line
		} else if started {
			current.lines = append(current.lines, line)
		}
	}
	if started {
		sections = append(sections, current.finish())
	}
	return sections
}

type sectionBuilder struct {
	lines      []string
	headerLine string
}

func newSection() sectionBuilder { return sectionBuilder{} }

func (s sectionBuilder) finish() diffSection {
	text := strings.Join(s.lines, "\n")
	text = strings.TrimRight(text, "\n")
	return diffSection{headerLine: s.headerLine, text: text}
}

// assertSectionMatchesRecord is a tripwire for order mismatch (raw record ↔
// text section): the header line must corroborate the raw record's path(s).
// Presence-based (not equality) so pathological-but-legit paths (spaces,
// ` b/` substrings) don't false-alarm; C-quoted headers (`"..."` escaped paths)
// are skipped — order is the only signal there, and both passes share it.
func assertSectionMatchesRecord(section diffSection, record rawDiffRecord, index int) error {
	if strings.Contains(section.headerLine, `"`) {
		return nil
	}
	paths := []string{record.path}
	if record.oldPath != nil {
		paths = []string{*record.oldPath, record.path}
	}
	for _, candidate := range paths {
		if !strings.Contains(section.headerLine, candidate) {
			return NewGitExecError(KindExecFailure, fmt.Sprintf(
				"diff section #%d header %s does not mention raw record path %s -> %s",
				index, tsJSONString(section.headerLine), tsJSONString(record.pathForError()), tsJSONString(record.path)),
				&GitExecErrorOptions{Command: "diff"})
		}
	}
	return nil
}

// pathForError renders the record's reported path for error messages.
func (r rawDiffRecord) pathForError() string {
	if r.oldPath != nil {
		return *r.oldPath
	}
	return r.path
}

/* ---------------------------------------------------------------- ls-tree */

// parseLsTree parses `ls-tree -z -r` output: `<mode> SP <type> SP <sha> TAB
// <path>` records, NUL-terminated; paths are raw (unquoted). Order is git's
// canonical tree order (deterministic for a given tree).
func parseLsTree(stdout []byte) ([]LsTreeEntry, error) {
	entries := make([]LsTreeEntry, 0, 8)
	for _, chunk := range strings.Split(string(stdout), "\x00") {
		if len(chunk) == 0 {
			continue
		}
		tabIndex := strings.Index(chunk, "\t")
		if tabIndex < 0 {
			return nil, malformedLsTreeRecord(chunk)
		}
		meta := strings.Split(chunk[:tabIndex], " ")
		mode, typ, sha := fieldAt(meta, 0), fieldAt(meta, 1), fieldAt(meta, 2)
		if mode == "" || typ == "" || sha == "" {
			return nil, malformedLsTreeMeta(chunk)
		}
		entries = append(entries, LsTreeEntry{Mode: mode, Type: typ, Sha: sha, Path: chunk[tabIndex+1:]})
	}
	return entries, nil
}

/* ---------------------------------------------------------------- log-commits */

// parseLogCommits parses `log --topo-order --format=%H %P -n` output: one
// commit per line, `<sha>[ SP <parent> [<SP> <parent> ...]]` (a root commit
// line has no parents). Shas are validated (fail closed on malformed output).
func parseLogCommits(stdout []byte) ([]LogCommit, error) {
	text := string(stdout)
	if len(text) == 0 {
		return []LogCommit{}, nil
	}
	commits := make([]LogCommit, 0, 8)
	for _, line := range strings.Split(text, "\n") {
		if len(line) == 0 {
			continue
		}
		separator := strings.Index(line, " ")
		sha := line
		if separator >= 0 {
			sha = line[:separator]
		}
		if !hex40.MatchString(sha) {
			return nil, NewGitExecError(KindExecFailure,
				fmt.Sprintf("git log produced a non-sha token: %s", sha),
				&GitExecErrorOptions{Command: "log", StderrExcerpt: firstRunes(line, 64)})
		}
		var parents []string
		if separator >= 0 {
			parents = splitNonEmpty(line[separator+1:], " ")
		}
		for _, parent := range parents {
			if !hex40.MatchString(parent) {
				return nil, NewGitExecError(KindExecFailure,
					fmt.Sprintf("git log produced a non-sha parent: %s", parent),
					&GitExecErrorOptions{Command: "log", StderrExcerpt: firstRunes(line, 64)})
			}
		}
		commits = append(commits, LogCommit{Sha: sha, Parents: parents})
	}
	return commits, nil
}

/* ---------------------------------------------------------------- numstat */

// parseNumstat parses `diff --numstat -z --find-renames` output (format
// verified against git 2.53): records are `<adds> TAB <dels> TAB <path>`
// NUL-terminated; rename records have an empty third field followed by two
// NUL-terminated chunks — ORIGINAL path first, then NEW path. `-` counts mean
// binary.
func parseNumstat(stdout []byte) ([]DiffStatRecord, error) {
	chunks := strings.Split(string(stdout), "\x00")
	records := make([]DiffStatRecord, 0, 8)
	index := 0
	for index < len(chunks) {
		chunk := chunks[index]
		index++
		if len(chunk) == 0 {
			continue
		}
		fields := strings.Split(chunk, "\t")
		if len(fields) < 3 {
			return nil, malformedNumstat(chunk)
		}
		adds, dels, path := fields[0], fields[1], fields[2]
		if len(path) == 0 {
			// Rename record: next two chunks are (original, new) — both required.
			oldPath := chunkAt(chunks, index)
			newPath := chunkAt(chunks, index+1)
			index += 2
			if len(oldPath) == 0 || len(newPath) == 0 {
				return nil, NewGitExecError(KindExecFailure,
					"malformed numstat rename record: missing path pair",
					&GitExecErrorOptions{Command: "diff"})
			}
			count, err := countPair(adds, dels)
			if err != nil {
				return nil, err
			}
			records = append(records, DiffStatRecord{
				Path:      newPath,
				OldPath:   strPtr(oldPath),
				Additions: count.additions,
				Deletions: count.deletions,
				Binary:    count.binary,
			})
		} else {
			count, err := countPair(adds, dels)
			if err != nil {
				return nil, err
			}
			records = append(records, DiffStatRecord{
				Path:      path,
				Additions: count.additions,
				Deletions: count.deletions,
				Binary:    count.binary,
			})
		}
	}
	return records, nil
}

type numstatCount struct {
	additions *int
	deletions *int
	binary    bool
}

// countPair maps the two numstat count fields; `-` means binary.
func countPair(adds, dels string) (numstatCount, error) {
	if adds == "-" || dels == "-" {
		return numstatCount{binary: true}, nil
	}
	if !digitsOnly.MatchString(adds) || !digitsOnly.MatchString(dels) {
		return numstatCount{}, NewGitExecError(KindExecFailure,
			fmt.Sprintf("malformed numstat counts: %s", tsJSONStringArray([]string{adds, dels})),
			&GitExecErrorOptions{Command: "diff"})
	}
	additions, _ := strconv.Atoi(adds)
	deletions, _ := strconv.Atoi(dels)
	return numstatCount{additions: intPtr(additions), deletions: intPtr(deletions)}, nil
}

var digitsOnly = regexp.MustCompile(`^\d+$`)

/* --------------------------------------------------------------- utilities */

func trimText(s string) string { return strings.TrimSpace(s) }

func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			out = append(out, part)
		}
	}
	return out
}

func fieldAt(fields []string, i int) string {
	if i < 0 || i >= len(fields) {
		return ""
	}
	return fields[i]
}

func chunkAt(chunks []string, i int) string {
	if i < 0 || i >= len(chunks) {
		return ""
	}
	return chunks[i]
}

func strPtr(s string) *string { return &s }

func intPtr(i int) *int { return &i }

// The malformed-record error constructors below preserve the exact TS parser
// messages (each `JSON.stringify` excerpt is reproduced by tsJSONString).

func malformedRawRecord(metaChunk string) *GitExecError {
	return NewGitExecError(KindExecFailure,
		"malformed --raw -z record: "+tsJSONString(firstRunes(metaChunk, 64)),
		&GitExecErrorOptions{Command: "diff"})
}

func malformedRawRecordPlain(detail string) *GitExecError {
	return NewGitExecError(KindExecFailure,
		"malformed --raw -z record: "+detail,
		&GitExecErrorOptions{Command: "diff"})
}

func malformedRawRecordNoStatus(metaChunk string) *GitExecError {
	return NewGitExecError(KindExecFailure,
		"malformed --raw -z record (no status): "+tsJSONString(metaChunk),
		&GitExecErrorOptions{Command: "diff"})
}

func malformedRenameRecord() *GitExecError {
	return NewGitExecError(KindExecFailure,
		"malformed rename record: missing new path",
		&GitExecErrorOptions{Command: "diff"})
}

func malformedRenameScore(statusField string) *GitExecError {
	return NewGitExecError(KindExecFailure,
		"malformed rename score: "+tsJSONString(statusField),
		&GitExecErrorOptions{Command: "diff"})
}

func malformedLsTreeRecord(chunk string) *GitExecError {
	return NewGitExecError(KindExecFailure,
		"malformed ls-tree record: "+tsJSONString(firstRunes(chunk, 64)),
		&GitExecErrorOptions{Command: "ls-tree"})
}

func malformedLsTreeMeta(chunk string) *GitExecError {
	return NewGitExecError(KindExecFailure,
		"malformed ls-tree meta: "+tsJSONString(firstRunes(chunk, 64)),
		&GitExecErrorOptions{Command: "ls-tree"})
}

func malformedNumstat(chunk string) *GitExecError {
	return NewGitExecError(KindExecFailure,
		"malformed numstat record: "+tsJSONString(firstRunes(chunk, 64)),
		&GitExecErrorOptions{Command: "diff"})
}
