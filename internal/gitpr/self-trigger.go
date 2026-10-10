package gitpr

// Ported from packages/core/src/gitpr/self-trigger.ts (P6-T19; PLAN §7.3 "Rate,
// burst, idempotency, lifecycle — self-trigger suppression"; invariant 11; P19).
//
// Pure classifier: a doc PR whose AUTHOR or BRANCH is docsync's own output is a
// SELF-TRIGGER — the run that produced it must never consume its own artifact
// and fire another run. Exactly one rule: branch prefix docssync/ OR author ===
// the injected bot identity. Anything else is NOT self-trigger and runs
// normally (docsync/* branch + human author is explicitly fine).

// SelfTriggerBranchPrefix is the owned doc branch namespace — a source PR whose
// head branch starts with it is docsync's own output.
const SelfTriggerBranchPrefix = "docsync/"

// SelfTriggerCheckInput is the pure classifier input (TS SelfTriggerCheckInput).
type SelfTriggerCheckInput struct {
	// Branch is the source PR's head branch; nil means "unknown/absent" (the TS
	// `string | null`): an unknown branch can never match the prefix.
	Branch *string
	// AuthorIdentity is the source PR's author; nil means "unknown/absent": the
	// empty fallback can never equal a platform author identity (platform
	// authors are non-empty), which is why the TS union keeps null distinct.
	AuthorIdentity *string
	// BotIdentity is the identity whose authorship is indistinguishable from
	// the app's own commits (a selected configured identity; the classifier
	// still uses the branch prefix as a second, independent signal).
	BotIdentity string
	// DefaultBranch is the repository's default branch — a source PR against it
	// is NEVER a docflow analysis target. Carried by the TS contract; NOT used
	// by this classifier (a branch-prefix/author match suppresses even a PR
	// against the default branch, and vice versa).
	DefaultBranch string
}

// SelfTriggerResult is the pure classification (TS SelfTriggerResult).
type SelfTriggerResult struct {
	// Suppressed is true iff the input is docsync's own output.
	Suppressed bool
	// Reason names the rule for the abort record; empty when not suppressed.
	Reason string
}

// EvaluateSelfTrigger (TS evaluateSelfTrigger) applies the P6-T19 rule.
func EvaluateSelfTrigger(input SelfTriggerCheckInput) SelfTriggerResult {
	branchIsOurs := input.Branch != nil &&
		len(*input.Branch) >= len(SelfTriggerBranchPrefix) &&
		(*input.Branch)[:len(SelfTriggerBranchPrefix)] == SelfTriggerBranchPrefix
	if branchIsOurs {
		return SelfTriggerResult{
			Suppressed: true,
			Reason:     "self-trigger: docsync/* branch (invariant 11)",
		}
	}
	identityIsOurs := input.AuthorIdentity != nil && *input.AuthorIdentity == input.BotIdentity
	if identityIsOurs {
		return SelfTriggerResult{
			Suppressed: true,
			Reason:     "self-trigger: author is the configured bot identity (invariant 11)",
		}
	}
	return SelfTriggerResult{Suppressed: false, Reason: ""}
}
