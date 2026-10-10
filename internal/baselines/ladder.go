package baselines

import (
	"context"
	"fmt"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Ported from core/src/baselines/ladder.ts: the D12 baseline-identity ladder
// (P1-T16; ARCH §6.1/D12). Resolves whether a stored baseline still refers to
// a commit that is meaningfully present at HEAD, in the binding resolution
// order:
//
//  1. RunExactSHA — the baseline commit IS head (identity trivially intact).
//  2. RunSameTree — identical tree hash (force-push/amend re-committing the
//     same content; content identity, not commit identity).
//  3. RunAncestor — the baseline commit is head's merge-base (identity intact
//     IN HISTORY; content drift since then is invalidate.go's question, not the
//     ladder's).
//  4. RunPatchIDMatch — the baseline commit's own change (diff from its first
//     parent) re-landed as one of head's recent commits (squash/cherry-pick
//     re-lands): the SAME patch text found within SearchDepth of head.
//  5. RunOrphanedSuspect — no rung matched: treat the baseline as INVALIDATED
//     (D12: baselines NEVER silently survive history rewrites — squash merges,
//     force-pushes, merge queues).
//
// Cost (documented): rungs 1–3 are O(1) git reads. Rung 4 is the capped walk:
// one `log --topo-order` call, then one diff + one `patch-id` subprocess per
// walked commit until the first match — bounded by SearchDepth (default 100,
// hard cap 500). The walk is deterministic: `--topo-order` fixes the order by
// graph shape (no commit-date dependence), and matching short-circuits at the
// first hit in that fixed order.
//
// Failure semantics (D14): every git failure propagates as a typed
// *gitx.GitExecError — the ladder NEVER swallows a git error into a resolution
// (the caller routes it as inconclusive). The single documented exception: the
// baseline commit's first parent cannot resolve (unknown-ref — a root commit or
// a shallow/grafted clone boundary); rung 4 then has no base for the baseline's
// patch and falls through to RunOrphanedSuspect, which is the conservative D12
// direction, not a silent error.

// LadderRun is one of the five ladder outcomes.
type LadderRun string

// The ladder rungs.
const (
	RunExactSHA        LadderRun = "exact-sha"
	RunSameTree        LadderRun = "same-tree"
	RunAncestor        LadderRun = "ancestor"
	RunPatchIDMatch    LadderRun = "patch-id-match"
	RunOrphanedSuspect LadderRun = "orphaned-suspect"
)

// LadderResolution is a resolution with its evidence string (BaselineValid is
// false exactly for RunOrphanedSuspect).
type LadderResolution struct {
	Run           LadderRun
	Evidence      string
	BaselineValid bool
}

// ResolveBaselineInput is the input of ResolveBaseline.
type ResolveBaselineInput struct {
	// Baseline is the stored baseline whose identity is resolved.
	Baseline *schemas.Baseline
	// Head is the head revision (a ref or sha).
	Head string
	// Git is the typed git client (the only git surface used here).
	Git *gitx.GitClient
	// SearchDepth is the maximum commits walked for the patch-id rung
	// (default 100, hard cap 500); nil means "unset" (the default).
	SearchDepth *int
}

// Search-depth bounds (TS DEFAULT_SEARCH_DEPTH / MAX_SEARCH_DEPTH).
const (
	DefaultSearchDepth = 100
	MaxSearchDepth     = 500
)

// ClampSearchDepth clamps the caller's walk depth into
// [1, MaxSearchDepth] (deterministic); nil keeps DefaultSearchDepth.
func ClampSearchDepth(searchDepth *int) int {
	requested := DefaultSearchDepth
	if searchDepth != nil {
		requested = *searchDepth
	}
	if requested < 1 {
		return 1
	}
	if requested > MaxSearchDepth {
		return MaxSearchDepth
	}
	return requested
}

// shortSha is the TS `sha.slice(0, 12)`.
func shortSha(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ResolveBaseline runs the D12 ladder for one baseline against head.
func ResolveBaseline(ctx context.Context, input ResolveBaselineInput) (LadderResolution, error) {
	if input.Baseline == nil {
		return LadderResolution{}, docutil.New(docutil.CodeInternal,
			"resolveBaseline: baseline is nil — refusing to resolve an absent record (fail closed)")
	}
	if input.Git == nil {
		return LadderResolution{}, docutil.New(docutil.CodeInternal,
			"resolveBaseline: git client is nil — refusing to resolve without git (fail closed)")
	}
	git := input.Git
	depth := ClampSearchDepth(input.SearchDepth)

	baselineSha, err := git.ResolveRev(ctx, input.Baseline.BaselineCommit)
	if err != nil {
		return LadderResolution{}, err
	}
	headSha, err := git.ResolveRev(ctx, input.Head)
	if err != nil {
		return LadderResolution{}, err
	}

	// Rung 1 — exact commit identity.
	if baselineSha == headSha {
		return LadderResolution{
			Run:           RunExactSHA,
			Evidence:      fmt.Sprintf("baseline commit %s is exactly HEAD", baselineSha),
			BaselineValid: true,
		}, nil
	}

	// Rung 2 — same content, possibly re-committed (amend/force-push, identical tree).
	baselineTree, err := git.TreeSha(ctx, baselineSha)
	if err != nil {
		return LadderResolution{}, err
	}
	headTree, err := git.TreeSha(ctx, headSha)
	if err != nil {
		return LadderResolution{}, err
	}
	if baselineTree == headTree {
		return LadderResolution{
			Run: RunSameTree,
			Evidence: fmt.Sprintf("tree %s identical for baseline %s and head %s",
				baselineTree, shortSha(baselineSha), shortSha(headSha)),
			BaselineValid: true,
		}, nil
	}

	// Rung 3a — identity intact in history (empty merge-base = unrelated histories).
	mergeBase, err := git.MergeBase(ctx, baselineSha, headSha)
	if err != nil {
		return LadderResolution{}, err
	}
	if mergeBase != "" && mergeBase == baselineSha {
		return LadderResolution{
			Run: RunAncestor,
			Evidence: fmt.Sprintf("merge-base %s equals the baseline commit %s",
				mergeBase, shortSha(baselineSha)),
			BaselineValid: true,
		}, nil
	}

	// Rung 3b — the baseline commit's own change re-landed somewhere in head's
	// recent history (squash/cherry-pick shape). The baseline's own patch is
	// the diff from its first parent; an unresolvable parent (root commit or
	// shallow clone boundary) leaves no comparable patch → fall through to
	// orphaned-suspect (conservative; documented exception, never a swallowed
	// environmental error — every other GitExecError kind propagates).
	var baselinePatchId string
	parents, err := git.ParentShas(ctx, baselineSha)
	if err != nil {
		gitErr, ok := gitx.AsGitExecError(err)
		if !ok || gitErr.Kind != gitx.KindUnknownRef {
			return LadderResolution{}, err
		}
		parents = nil
	}
	if parents != nil && len(parents) > 0 {
		baselinePatchId, err = git.PatchId(ctx, parents[0], baselineSha, nil)
		if err != nil {
			return LadderResolution{}, err
		}
	}

	if baselinePatchId != "" {
		commits, err := git.LogCommits(ctx, headSha, depth)
		if err != nil {
			return LadderResolution{}, err
		}
		for _, commit := range commits {
			if len(commit.Parents) == 0 {
				continue // root commit: no own patch
			}
			candidate, err := git.PatchId(ctx, commit.Parents[0], commit.Sha, nil)
			if err != nil {
				return LadderResolution{}, err
			}
			if candidate != "" && candidate == baselinePatchId {
				return LadderResolution{
					Run: RunPatchIDMatch,
					Evidence: fmt.Sprintf(
						"patch-id %s re-landed at head-history commit %s (walked %d commits, depth %d)",
						baselinePatchId, shortSha(commit.Sha), len(commits), depth),
					BaselineValid: true,
				}, nil
			}
		}
	}

	orphanCause := "its own patch is not computable (no parent or empty diff)"
	if baselinePatchId != "" {
		orphanCause = fmt.Sprintf("no matching patch-id within depth %d", depth)
	}
	return LadderResolution{
		Run: RunOrphanedSuspect,
		Evidence: fmt.Sprintf(
			"no ladder rung matched: baseline %s is not HEAD, tree differs, not an ancestor of %s, and %s",
			shortSha(baselineSha), shortSha(headSha), orphanCause),
		BaselineValid: false,
	}, nil
}
