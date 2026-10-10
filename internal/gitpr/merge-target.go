package gitpr

// Ported from packages/core/src/gitpr/merge-target.ts (P6-T16; PLAN §7.3
// "Merge-order conflicts & PR lifecycle"; ARCH §12.3): merge-target re-check
// decision — "Doc PR checks re-run against the merge target (merge queues,
// squash merges) — triggering baseline re-resolution per D12".
//
// PURE name-level primitive over the four inputs the caller already knows — no
// git, no wall clock (P19). It decides WHETHER/HOW a doc PR's check must re-run;
// the D12 baseline re-resolution itself is NOT performed here — it delegates to
// Phase 1's resolveBaseline ladder (internal/baselines), which owns rung
// semantics (exact-sha, same-tree, ancestor, patch-id-match, orphaned-suspect).
// This function only kicks it off (`baselineReresolve: true`).
//
// Trigger precedence and the SHA-level heuristic (documented DECISION):
//
//  1. mergeQueueDetected → merge-queue — the queue subsumes any movement: the
//     re-check must evaluate the queue's projected merge result, not the branch
//     tip.
//  2. else defaultBranchMoved:
//     - docPrBaseSha === defaultBranchHeadSha → squash-merge: the doc PR's base
//     pin already sits ON the head that moved — the movement is the PR's own
//     changes landing as a squash commit (no external commits to catch up with).
//     - otherwise → default-branch-advanced: the default branch moved past the
//     doc PR's base with other commits.
//  3. else → no re-check (base content unchanged as far as name-level inputs can
//     tell).
//
// `baselineReresolve` is true exactly when `recheck` is true — every recheck
// kicks off D12 (baselines never silently survive history rewrites: squash
// merges, force-pushes, merge queues).

import "github.com/DUDE616/docSync-go/internal/docutil"

// CodeMergeTarget preserves the TS EDOCSYNC_MERGE_TARGET code verbatim
// (CONVENTIONS.md §Errors: the code is not in internal/docutil, so it rides the
// error text through docutil.New instead of inventing a new docutil code).
const CodeMergeTarget = "EDOCSYNC_MERGE_TARGET"

// mergeTargetError builds the MergeTargetError equivalent (the TS message text
// is preserved verbatim).
func mergeTargetError(message string) error {
	return docutil.New(CodeMergeTarget, message)
}

// MergeTargetTrigger is the closed union of the three re-check triggers
// (ARCH §12.3).
type MergeTargetTrigger string

// The three re-check triggers.
const (
	MergeTargetTriggerMergeQueue            MergeTargetTrigger = "merge-queue"
	MergeTargetTriggerSquashMerge           MergeTargetTrigger = "squash-merge"
	MergeTargetTriggerDefaultBranchAdvanced MergeTargetTrigger = "default-branch-advanced"
)

// MergeTargetRecheckInput is the decision input (TS
// MergeTargetRecheckInput).
type MergeTargetRecheckInput struct {
	// DocPrBaseSha is the commit the doc PR is currently based on.
	DocPrBaseSha string
	// DefaultBranchHeadSha is the default branch's current head commit.
	DefaultBranchHeadSha string
	// DefaultBranchMoved reports whether the default branch moved since the doc
	// PR's check last ran. (The TS also rejected non-boolean flags; Go's bool
	// type makes that unrepresentable.)
	DefaultBranchMoved bool
	// MergeQueueDetected reports whether a merge queue is orchestrating the
	// source PR's merge. (Same type-level note as DefaultBranchMoved.)
	MergeQueueDetected bool
}

// MergeTargetRecheckDecision is the re-check decision (PLAN §7.3; D12 kickoff).
type MergeTargetRecheckDecision struct {
	Recheck bool
	// Trigger is the trigger name; nil is the TS `null` (no re-check).
	Trigger *MergeTargetTrigger
	// BaselineReresolve is true ⟺ the caller must kick off Phase 1's
	// resolveBaseline (D12).
	BaselineReresolve bool
}

// EvaluateMergeTargetRecheck is the pure decision (P6-T16): default branch
// moved OR merge queue detected → recheck with the documented trigger and D12
// baseline re-resolution; unchanged → no recheck. Fails closed on malformed
// input (empty SHAs) — never a guessed decision.
func EvaluateMergeTargetRecheck(input MergeTargetRecheckInput) (MergeTargetRecheckDecision, error) {
	if input.DocPrBaseSha == "" {
		return MergeTargetRecheckDecision{}, mergeTargetError(
			"evaluateMergeTargetRecheck: docPrBaseSha must be a nonempty string")
	}
	if input.DefaultBranchHeadSha == "" {
		return MergeTargetRecheckDecision{}, mergeTargetError(
			"evaluateMergeTargetRecheck: defaultBranchHeadSha must be a nonempty string")
	}

	if input.MergeQueueDetected {
		trigger := MergeTargetTriggerMergeQueue
		return MergeTargetRecheckDecision{
			Recheck:           true,
			Trigger:           &trigger,
			BaselineReresolve: true,
		}, nil
	}
	if input.DefaultBranchMoved {
		trigger := MergeTargetTriggerDefaultBranchAdvanced
		if input.DocPrBaseSha == input.DefaultBranchHeadSha {
			trigger = MergeTargetTriggerSquashMerge
		}
		return MergeTargetRecheckDecision{
			Recheck:           true,
			Trigger:           &trigger,
			BaselineReresolve: true,
		}, nil
	}
	return MergeTargetRecheckDecision{Recheck: false, Trigger: nil, BaselineReresolve: false}, nil
}
