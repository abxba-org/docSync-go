package gitpr_test

// Black-box port of test/gitpr-post-merge.test.ts (P6-T17; PLAN §7.3 "Merge-order
// conflicts & PR lifecycle"; ARCH §12.3; OD-R19): the post-merge re-run trigger
// primitive is PURE — no webhook transport, no scheduler, no side effects.
//
// Gate: a push event qualifies ONLY when it lands on the DEFAULT branch (the ref
// is compared after normalizing `refs/heads/`) AND there are OPEN docsync PRs
// based on it. Fails closed on malformed input — a non-push kind, an empty
// ref/branch or a malformed PR tuple is a typed error, never a silent "no
// rerun".

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/stretchr/testify/require"
)

func TestP6T17_PostMergeGateDecisionMatrix(t *testing.T) {
	testCases := []struct {
		name          string
		ref           string
		defaultBranch string
		prs           []gitpr.OpenDocsyncPr
		wantRerun     bool
		wantContains  []string
		wantAbsent    []string
	}{
		{
			name: "qualified push to the default branch names every affected PR",
			ref:  "refs/heads/main", defaultBranch: "main",
			prs:          []gitpr.OpenDocsyncPr{{Number: 7, Base: "main"}, {Number: 9, Base: "main"}},
			wantRerun:    true,
			wantContains: []string{"#7, #9", "push to default branch \"main\"", "superseded rather than conflicting"},
		},
		{
			name: "a bare branch name is accepted (no refs/heads prefix)",
			ref:  "main", defaultBranch: "main",
			prs:          []gitpr.OpenDocsyncPr{{Number: 3, Base: "main"}},
			wantRerun:    true,
			wantContains: []string{"#3"},
		},
		{
			name: "a docsync PR based on something else is untouched",
			ref:  "main", defaultBranch: "main",
			prs:          []gitpr.OpenDocsyncPr{{Number: 7, Base: "release"}},
			wantRerun:    false,
			wantContains: []string{"no open docsync PRs based on it", "nothing to re-run"},
		},
		{
			name: "no open PRs at all",
			ref:  "refs/heads/main", defaultBranch: "main",
			prs:          nil,
			wantRerun:    false,
			wantContains: []string{"nothing to re-run"},
		},
		{
			name: "a feature-branch push never re-runs",
			ref:  "refs/heads/feature/x", defaultBranch: "main",
			prs:          []gitpr.OpenDocsyncPr{{Number: 7, Base: "main"}},
			wantRerun:    false,
			wantContains: []string{"does not touch the default branch"},
		},
		{
			name: "a default-branch push with a differently-based PR is filtered",
			ref:  "refs/heads/main", defaultBranch: "main",
			prs:          []gitpr.OpenDocsyncPr{{Number: 7, Base: "main"}, {Number: 11, Base: "release"}},
			wantRerun:    true,
			wantContains: []string{"#7"},
			wantAbsent:   []string{"#11"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
				Event:          gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: tc.ref},
				DefaultBranch:  tc.defaultBranch,
				OpenDocsyncPrs: tc.prs,
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantRerun, decision.ShouldRerun)
			for _, want := range tc.wantContains {
				require.Contains(t, decision.Reason, want)
			}
			for _, absent := range tc.wantAbsent {
				require.NotContains(t, decision.Reason, absent)
			}
			if !tc.wantRerun {
				require.NotContains(t, decision.Reason, "re-run so a doc PR")
			}
		})
	}
}

func TestP6T17_PostMergeAffectedPrsAreExactlyTheDefaultBasedOnes(t *testing.T) {
	decision, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "refs/heads/main"},
		DefaultBranch: "main",
		OpenDocsyncPrs: []gitpr.OpenDocsyncPr{
			{Number: 7, Base: "main"},
			{Number: 11, Base: "release"},
			{Number: 12, Base: "main"},
		},
	})
	require.NoError(t, err)
	require.True(t, decision.ShouldRerun)
	require.Contains(t, decision.Reason, "#7, #12")
	require.True(t, strings.HasPrefix(decision.Reason, "push to default branch \"main\""))
}

func TestP6T17_PostMergeInputIsFailClosed(t *testing.T) {
	valid := []gitpr.OpenDocsyncPr{{Number: 7, Base: "main"}}

	_, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: "pull_request", Ref: "refs/heads/main"},
		DefaultBranch: "main", OpenDocsyncPrs: valid,
	})
	require.ErrorContains(t, err, `event.kind must be "push"`)
	require.Equal(t, gitpr.EDOCSYNCPostMerge, docutil.CodeOf(err))

	_, err = gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: ""},
		DefaultBranch: "main", OpenDocsyncPrs: valid,
	})
	require.ErrorContains(t, err, "event.ref must be a nonempty string")

	_, err = gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "main"},
		DefaultBranch: "", OpenDocsyncPrs: valid,
	})
	require.ErrorContains(t, err, "defaultBranch must be a nonempty string")

	_, err = gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "main"},
		DefaultBranch: "main", OpenDocsyncPrs: []gitpr.OpenDocsyncPr{{Number: 0, Base: "main"}},
	})
	require.ErrorContains(t, err, "openDocsyncPrs entries must have a positive integer number (got 0)")

	_, err = gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "main"},
		DefaultBranch: "main", OpenDocsyncPrs: []gitpr.OpenDocsyncPr{{Number: 7, Base: ""}},
	})
	require.ErrorContains(t, err, "openDocsyncPrs #7 has a malformed base (nonempty string required)")
}
