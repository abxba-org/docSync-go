package gitpr

// Ported from core/src/gitpr/post-merge.ts (P6-T17; PLAN §7.3 "Merge-order
// conflicts & PR lifecycle"; ARCH §12.3): post-merge re-run trigger primitive.
//
// "Post-merge `push` handling (wired in Phase 8; trigger primitive built
// here): evaluation re-runs after the code PR merges so a doc PR that no longer
// applies is superseded rather than conflicting."
//
// PURE primitive — NO webhook transport, NO scheduler, NO side effects. The
// webhook wiring and the DEFAULT-OFF posture (nothing re-runs unless an
// operator enables post-merge handling) are Phase 8 concerns (OD-R19), which
// consumes this function from its event handler.
//
// Gate (documented): a push event qualifies ONLY when
//
//  1. it lands on the DEFAULT branch — the event ref is compared after
//     normalizing the `refs/heads/` prefix (both bare names and fully
//     qualified refs are accepted), and
//  2. there are OPEN docsync PRs whose base content the push may have
//     changed — i.e. PRs based directly on the default branch (a docsync PR
//     based on anything else is not touched by this push).
//
// The re-run itself then evaluates whether each affected doc PR still applies
// (supersede instead of conflict, PLAN §7.3); baseline identity on re-run
// delegates to Phase 1's resolveBaseline ladder (D12).

import (
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// EDOCSYNCPostMerge preserves the TS static `PostMergeError.code`
// (EDOCSYNC_POST_MERGE), which internal/docutil does not list. Per
// CONVENTIONS.md §Errors the code string is preserved on the returned error
// instead of inventing a new docutil code constant.
const EDOCSYNCPostMerge = "EDOCSYNC_POST_MERGE"

// PostMergePushEvent is the only event kind this primitive evaluates (other
// kinds are Phase 8 wiring, OD-R19).
type PostMergePushEvent struct {
	Kind string `json:"kind"` // 'push'
	// Ref is a branch name or a fully qualified ref (`refs/heads/<name>`).
	Ref string `json:"ref"`
}

// PostMergeEventKindPush is the sole accepted event kind.
const PostMergeEventKindPush = "push"

// OpenDocsyncPr is an open docsync PR known to the caller.
type OpenDocsyncPr struct {
	Number int    `json:"number"`
	Base   string `json:"base"` // the branch the PR targets
}

// PostMergeReRunInput is the input of EvaluatePostMergeReRun.
type PostMergeReRunInput struct {
	Event          PostMergePushEvent
	DefaultBranch  string
	OpenDocsyncPrs []OpenDocsyncPr
}

// PostMergeReRunDecision is the re-run gate decision (PLAN §7.3; wired in
// Phase 8, OD-R19).
type PostMergeReRunDecision struct {
	ShouldRerun bool   `json:"should_rerun"`
	Reason      string `json:"reason"`
}

// postMergeRefsHeadsPrefix is the `refs/heads/` prefix the normalizer strips.
const postMergeRefsHeadsPrefix = "refs/heads/"

// postMergeBranchFromRef normalizes a ref to a bare branch name.
func postMergeBranchFromRef(ref string) string {
	if strings.HasPrefix(ref, postMergeRefsHeadsPrefix) {
		return ref[len(postMergeRefsHeadsPrefix):]
	}
	return ref
}

// postMergeError is the typed fail-closed refusal (TS: PostMergeError).
func postMergeError(msg string) error {
	return docutil.New(EDOCSYNCPostMerge, msg)
}

// EvaluatePostMergeReRun is the pure gate (P6-T17): a push to the default
// branch with open docsync PRs based on it → `ShouldRerun: true` with a reason
// naming every affected PR (`#7, #9`); a feature-branch push or no affected PRs
// → `false`. Fails closed on malformed input — a non-push event kind, empty
// refs/branch names or malformed PR tuples are typed errors, never a silent
// "no rerun".
func EvaluatePostMergeReRun(input PostMergeReRunInput) (PostMergeReRunDecision, error) {
	event := input.Event
	if event.Kind != PostMergeEventKindPush {
		return PostMergeReRunDecision{}, postMergeError(
			`evaluatePostMergeReRun: event.kind must be "push" — other webhook kinds are Phase 8 wiring (OD-R19), not this primitive`)
	}
	if event.Ref == "" {
		return PostMergeReRunDecision{}, postMergeError(
			"evaluatePostMergeReRun: event.ref must be a nonempty string")
	}
	if input.DefaultBranch == "" {
		return PostMergeReRunDecision{}, postMergeError(
			"evaluatePostMergeReRun: defaultBranch must be a nonempty string")
	}
	for _, pr := range input.OpenDocsyncPrs {
		if pr.Number < 1 {
			return PostMergeReRunDecision{}, postMergeError(
				"evaluatePostMergeReRun: openDocsyncPrs entries must have a positive integer number (got " +
					strconv.Itoa(pr.Number) + ")")
		}
		if pr.Base == "" {
			return PostMergeReRunDecision{}, postMergeError(
				"evaluatePostMergeReRun: openDocsyncPrs #" + strconv.Itoa(pr.Number) +
					" has a malformed base (nonempty string required)")
		}
	}

	pushedBranch := postMergeBranchFromRef(event.Ref)
	if pushedBranch != input.DefaultBranch {
		return PostMergeReRunDecision{
			ShouldRerun: false,
			Reason: "push to \"" + event.Ref + "\" does not touch the default branch \"" +
				input.DefaultBranch + "\" — no docsync re-run",
		}, nil
	}
	var affected []OpenDocsyncPr
	for _, pr := range input.OpenDocsyncPrs {
		if pr.Base == input.DefaultBranch {
			affected = append(affected, pr)
		}
	}
	if len(affected) == 0 {
		return PostMergeReRunDecision{
			ShouldRerun: false,
			Reason:      "push to default branch \"" + input.DefaultBranch + "\" with no open docsync PRs based on it — nothing to re-run",
		}, nil
	}
	named := make([]string, 0, len(affected))
	for _, pr := range affected {
		named = append(named, "#"+strconv.Itoa(pr.Number))
	}
	return PostMergeReRunDecision{
		ShouldRerun: true,
		Reason: "push to default branch \"" + input.DefaultBranch + "\" may have changed base content for open docsync PR(s) " +
			strings.Join(named, ", ") + " — re-run so a doc PR that no longer applies is superseded rather than conflicting " +
			"(PLAN §7.3; baseline re-resolution delegates to Phase 1 resolveBaseline, D12)",
	}, nil
}
