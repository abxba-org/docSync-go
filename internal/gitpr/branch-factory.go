package gitpr

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
)

// Ported from core/src/gitpr/branch-factory.ts — branch factory (P6-T01;
// ARCH §12.2 PR write path groundwork; INV-1).
//
// Deterministic doc-branch naming + the INV-1 hard default-branch guard:
//
//   - Source-PR runs name the branch after the reviewed change it documents —
//     `docsync/pr-<n>-<sha7>` where sha7 is the first 7 hex chars of the FULL
//     run head sha, lowercased (D12 fingerprint stability). Run-scoped flows
//     name the branch after the run — `docsync/<run-id>`. Same inputs always
//     produce the same name.
//   - INV-1: a computed branch name equal to the default branch (bare, or in
//     `refs/heads/<default>` form) is REFUSED with a typed
//     DefaultBranchRefError BEFORE any git write — the engine never writes doc
//     branches onto the default branch. AssertNotDefaultBranch is pure and
//     total; BranchFactory.CreateDocBranch re-runs it for every candidate it
//     considers.
//   - Idempotency (P6-T13 rule, shared with future supersede flows): scanning
//     candidates in the deterministic order `<name>`, `<name>-2`, `<name>-3`,
//     ... — an existing candidate whose tip equals the run's head sha means
//     the branch already exists exactly as required → created: false, no
//     error; an existing candidate at a DIFFERENT sha is skipped and the
//     FIRST FREE candidate is created at the run's head sha → created: true.
//     The scan is bounded (MAX_DISAMBIGUATOR_CANDIDATES); exhaustion is a
//     typed failure, never a silent fallback.
//
// All git access flows through the injected *gitx.GitClient (the exec
// wrapper's arg-validation and typed failures apply to every call here);
// input.RepoRoot is the run's declared repository root — validated and carried
// into typed error evidence, but never used to bypass the client.

/* -------------------------------------------------------------------- errors */

// CodeDefaultBranchRef preserves the TS EDOCSYNC_DEFAULT_BRANCH_REF code
// verbatim (INV-1 hard guard). docutil carries no such code; per the
// globs/statemachine precedent the TS code is kept through docutil.New
// instead of inventing a new one (CONVENTIONS.md §Errors).
const CodeDefaultBranchRef = "EDOCSYNC_DEFAULT_BRANCH_REF"

// CodeBranchName preserves the TS EDOCSYNC_BRANCH_NAME code verbatim.
const CodeBranchName = "EDOCSYNC_BRANCH_NAME"

// DefaultBranchRefError is the INV-1 hard guard violation: the proposed ref is
// the default branch (in any accepted form). The write path NEVER proceeds on
// this error.
type DefaultBranchRefError struct {
	// Ref is the offending ref, verbatim.
	Ref string
	// DefaultBranch is the default branch it collided with (normalized, bare).
	DefaultBranch string

	coded *docutil.Error
}

// Code reports the machine-readable code (EDOCSYNC_DEFAULT_BRANCH_REF).
func (e *DefaultBranchRefError) Code() string { return CodeDefaultBranchRef }

// Error renders the coded error in docutil house style.
func (e *DefaultBranchRefError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *DefaultBranchRefError) Unwrap() error { return e.coded }

// BranchNameError is a branch-name computation or validation failure (typed,
// fail closed).
type BranchNameError struct {
	coded *docutil.Error
}

// Code reports the machine-readable code (EDOCSYNC_BRANCH_NAME).
func (e *BranchNameError) Code() string { return CodeBranchName }

// Error renders the coded error in docutil house style.
func (e *BranchNameError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *BranchNameError) Unwrap() error { return e.coded }

// newDefaultBranchRefError builds the INV-1 refusal with the exact TS message.
func newDefaultBranchRefError(ref, defaultBranch string) *DefaultBranchRefError {
	return &DefaultBranchRefError{
		Ref:           ref,
		DefaultBranch: defaultBranch,
		coded: docutil.New(CodeDefaultBranchRef,
			"INV-1 hard guard: ref '"+ref+"' is the default branch '"+defaultBranch+
				"' — doc branches must never target the default branch (never proceed)"),
	}
}

// newBranchNameError builds a branch-name refusal.
func newBranchNameError(message string) *BranchNameError {
	return &BranchNameError{coded: docutil.New(CodeBranchName, message)}
}

/* ------------------------------------------------------------------- naming */

var (
	// fullShaHex matches a full 40-hex object name (TS FULL_SHA_HEX).
	fullShaHex = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	// runIDSafe is the safe branch-ref subset for run ids (git forbids spaces,
	// control chars, ~ ^ : ? * [ \ ..).
	runIDSafe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// refsHeadsPrefix is the fully-qualified local branch prefix.
const refsHeadsPrefix = "refs/heads/"

// refsRemotesPrefix is the remote-tracking prefix the TS guard also strips
// (with the TS's 11-character slice — see the quirk note in
// assertNotDefaultBranch).
const refsRemotesPrefix = "refs/remotes/"

// jsonStringExcerpt reproduces JSON.stringify for the bounded string excerpts
// the TS error messages embed (raw non-ASCII, \b\f\n\r\t escapes, \u00xx for
// the remaining control characters) — the same rendering gitx uses internally.
func branchNameJSONExcerpt(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				const hexDigits = "0123456789abcdef"
				b.WriteByte(hexDigits[(r>>4)&0xf])
				b.WriteByte(hexDigits[r&0xf])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// firstRunes64 slices s to at most 64 runes (the TS `slice(0, 64)` bound on
// echoed input, so a hostile value can never flood an error message).
func branchEchoBound(s string) string {
	runes := []rune(s)
	if len(runes) > 64 {
		return string(runes[:64])
	}
	return s
}

// normalizedFullSha validates + lowercases a FULL 40-hex sha; sha7 derivation
// always sees the lowercase form regardless of the caller's case.
func normalizedFullSha(headSha, label string) (string, error) {
	if !fullShaHex.MatchString(headSha) {
		return "", newBranchNameError(
			label + " must be a full 40-hex commit sha (got " +
				branchNameJSONExcerpt(branchEchoBound(headSha)) + ")")
	}
	return strings.ToLower(headSha), nil
}

// assertSafeRunId refuses a run id that is not a safe branch fragment.
func assertSafeRunId(runID string) error {
	if !runIDSafe.MatchString(runID) {
		return newBranchNameError(
			"runId must be a safe branch fragment (nonempty, starting alphanumeric, only " +
				"[A-Za-z0-9._-]; got " + branchNameJSONExcerpt(branchEchoBound(runID)) + ")")
	}
	return nil
}

// BranchNameForSourcePr is the deterministic source-PR branch name:
// `docsync/pr-<n>-<sha7>` — sha7 is the first 7 hex chars of the FULL 40-hex
// head sha, lowercased.
func BranchNameForSourcePr(sourcePr int, headSha string) (string, error) {
	if sourcePr < 1 {
		return "", newBranchNameError(
			"sourcePr must be a positive integer (got " + strconv.Itoa(sourcePr) + ")")
	}
	sha7, err := normalizedFullSha(headSha, "headSha")
	if err != nil {
		return "", err
	}
	return "docsync/pr-" + strconv.Itoa(sourcePr) + "-" + sha7[:7], nil
}

// BranchNameForRun is the deterministic run-scoped branch name:
// `docsync/<run-id>`.
func BranchNameForRun(runID string) (string, error) {
	if err := assertSafeRunId(runID); err != nil {
		return "", err
	}
	return "docsync/" + runID, nil
}

// AssertNotDefaultBranch is the INV-1 hard guard (pure, total): ref must not
// be the default branch — bare equality, or the `refs/heads/<default>` form
// (on either side; the default branch argument is normalized before
// comparison). Any collision returns a *DefaultBranchRefError; NEVER proceed
// past this guard.
//
// Fidelity note: the TS normalizer strips REFS_HEADS_PREFIX.length (11)
// characters from a `refs/remotes/` default too, yielding the TS's exact
// (quirky) `motes/<name>` normalization for that form. That behavior is
// reproduced verbatim — behavior preservation outranks the apparent typo, and
// no caller or test depends on the remote form.
func AssertNotDefaultBranch(ref, defaultBranch string) error {
	if len(ref) == 0 {
		return newBranchNameError("ref must be a nonempty string")
	}
	if len(defaultBranch) == 0 {
		return newBranchNameError("defaultBranch must be nonempty")
	}
	bare := defaultBranch
	if strings.HasPrefix(defaultBranch, refsHeadsPrefix) || strings.HasPrefix(defaultBranch, refsRemotesPrefix) {
		bare = defaultBranch[len(refsHeadsPrefix):]
	}
	if ref == bare || ref == refsHeadsPrefix+bare {
		return newDefaultBranchRefError(ref, bare)
	}
	return nil
}

/* ------------------------------------------------------------------ factory */

// MAX_DISAMBIGUATOR_CANDIDATES is the deterministic disambiguator scan bound
// (candidates `<name>`, `-2` … `-(n+1)`).
const MAX_DISAMBIGUATOR_CANDIDATES = 64

// CreateDocBranchInput is the input of BranchFactory.CreateDocBranch.
type CreateDocBranchInput struct {
	// RepoRoot is the run's declared repository root (validated; evidence in
	// typed errors).
	RepoRoot string
	// SourcePr is the source-PR number — when non-nil the PR-form name is used.
	SourcePr *int
	// RunID is the run id (required; names the branch when SourcePr is absent).
	RunID string
	// HeadSha is the run's FULL head commit sha — the branch is created at it.
	HeadSha string
	// DefaultBranch is the repository's default branch (INV-1 guard input, any
	// accepted form).
	DefaultBranch string
}

// CreateDocBranchResult is the result of BranchFactory.CreateDocBranch.
type CreateDocBranchResult struct {
	// Branch is the branch that now exists for this run (base name or `-2`…
	// disambiguator).
	Branch string
	// Created is false when an existing branch already pointed at the head sha.
	Created bool
}

// BranchFactory is the branch factory surface (P6-T01): naming, the INV-1
// guard, creation. The pure naming functions are package-level
// (BranchNameForSourcePr / BranchNameForRun / AssertNotDefaultBranch) and the
// factory methods delegate to them, so callers and tests share ONE
// implementation — the Go form of the TS factory object.
type BranchFactory struct {
	git *gitx.GitClient
}

// CreateBranchFactory builds the branch factory over an injected GitClient.
func CreateBranchFactory(git *gitx.GitClient) *BranchFactory {
	return &BranchFactory{git: git}
}

// Git is the injected git client the factory writes through.
func (f *BranchFactory) Git() *gitx.GitClient { return f.git }

// BranchNameForSourcePr delegates to the package-level pure function.
func (f *BranchFactory) BranchNameForSourcePr(sourcePr int, headSha string) (string, error) {
	return BranchNameForSourcePr(sourcePr, headSha)
}

// BranchNameForRun delegates to the package-level pure function.
func (f *BranchFactory) BranchNameForRun(runID string) (string, error) {
	return BranchNameForRun(runID)
}

// AssertNotDefaultBranch delegates to the package-level pure function.
func (f *BranchFactory) AssertNotDefaultBranch(ref, defaultBranch string) error {
	return AssertNotDefaultBranch(ref, defaultBranch)
}

// branchTip returns the tip sha of a local branch; the bool is false when the
// branch does not exist (a typed unknown-ref failure).
func branchTip(ctx context.Context, git *gitx.GitClient, branch string) (string, bool, error) {
	tip, err := git.ResolveRev(ctx, refsHeadsPrefix+branch)
	if err != nil {
		var execErr *gitx.GitExecError
		if errors.As(err, &execErr) && execErr.Kind == gitx.KindUnknownRef {
			return "", false, nil
		}
		return "", false, err
	}
	return tip, true, nil
}

// CreateDocBranch creates (or idempotently re-finds) the run's doc branch.
func (f *BranchFactory) CreateDocBranch(ctx context.Context, input CreateDocBranchInput) (CreateDocBranchResult, error) {
	if len(input.RepoRoot) == 0 || !filepath.IsAbs(input.RepoRoot) {
		return CreateDocBranchResult{}, newBranchNameError(
			"createDocBranch repoRoot must be an absolute path (got " +
				branchNameJSONExcerpt(branchEchoBound(input.RepoRoot)) + ")")
	}
	if err := assertSafeRunId(input.RunID); err != nil {
		return CreateDocBranchResult{}, err
	}
	head, err := normalizedFullSha(input.HeadSha, "headSha")
	if err != nil {
		return CreateDocBranchResult{}, err
	}
	var base string
	if input.SourcePr != nil {
		base, err = BranchNameForSourcePr(*input.SourcePr, head)
		if err != nil {
			return CreateDocBranchResult{}, err
		}
	} else {
		base, err = BranchNameForRun(input.RunID)
		if err != nil {
			return CreateDocBranchResult{}, err
		}
	}
	if err := AssertNotDefaultBranch(base, input.DefaultBranch); err != nil {
		return CreateDocBranchResult{}, err
	}

	// Deterministic candidate scan (P6-T13): base, then -2, -3, ... first free.
	// Idempotent: an existing candidate AT the run's head sha → created:false.
	for attempt := 0; attempt < MAX_DISAMBIGUATOR_CANDIDATES; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = base + "-" + strconv.Itoa(attempt+1)
		}
		if err := AssertNotDefaultBranch(candidate, input.DefaultBranch); err != nil {
			return CreateDocBranchResult{}, err
		}
		tip, exists, err := branchTip(ctx, f.git, candidate)
		if err != nil {
			return CreateDocBranchResult{}, err
		}
		if !exists {
			// Created AT the run's head sha (identical to current HEAD in the
			// normal orchestration flow, and idempotency-stable across re-runs).
			start := head
			if err := f.git.CreateBranch(ctx, candidate, &start); err != nil {
				return CreateDocBranchResult{}, err
			}
			return CreateDocBranchResult{Branch: candidate, Created: true}, nil
		}
		if tip == head {
			return CreateDocBranchResult{Branch: candidate, Created: false}, nil
		}
		// Existing candidate at a DIFFERENT sha → deterministic disambiguator.
	}
	return CreateDocBranchResult{}, newBranchNameError(
		"no free branch name for '" + base + "' after " +
			strconv.Itoa(MAX_DISAMBIGUATOR_CANDIDATES) +
			" deterministic disambiguator candidates")
}
