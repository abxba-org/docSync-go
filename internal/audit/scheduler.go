// Ported from core/src/audit/scheduler.ts (P11-T18; ARCH §7.2 Loop B —
// Background drift audit): PURE trigger-eligibility resolution for the
// background audit — scheduled (nightly) or post-merge on the default branch,
// and NEVER on the developer's PR critical path.
//
// STRUCTURAL PR REFUSAL (the hard assertion seam, V-P11-8): any context that
// IS a PR event — or that carries a PR number at all, regardless of the
// declared event — is refused with the typed verdict
// `pr-critical-path-forbidden`. The refusal is structural: there is NO
// configuration flag, branch value or caller override that can make a
// PR-context trigger eligible through this module. The workflow YAML itself
// is wired in W5b2/T18-final; this module is the decision seam its trigger
// conditions must consult.
//
// Event rules (P11-T18):
//   - `nightly` — eligible when the schedule is configured AND the trigger
//     runs on the default branch (the V-P11-8 pinning: the nightly trigger
//     runs ONLY on the default branch). An unknown/absent branch is refused
//     (fail closed); the W5b2 workflow pins the default branch explicitly.
//   - `post-merge` — eligible ONLY when explicitly configured AND the event
//     is on the default branch.
//   - Anything else — refused as `unknown-event` (fail closed).
//
// No IO, no clock, no environment reads: the module is a pure decision
// function over its inputs.
package audit

import "fmt"

// LoopBScheduleConfig is the Loop B schedule configuration.
type LoopBScheduleConfig struct {
	// Nightly reports whether the nightly schedule is configured (e.g. the
	// scheduled workflow exists).
	Nightly bool
	// PostMerge reports whether the optional post-merge default-branch
	// trigger is explicitly configured.
	PostMerge bool
	// DefaultBranch is the repository's default branch (exact name
	// comparison).
	DefaultBranch string
}

// LoopBTriggerEvent is the trigger event vocabulary (TS LoopBTriggerEvent).
type LoopBTriggerEvent string

// The trigger events.
const (
	LoopBEventNightly   LoopBTriggerEvent = "nightly"
	LoopBEventPostMerge LoopBTriggerEvent = "post-merge"
	LoopBEventPR        LoopBTriggerEvent = "pr"
)

// LoopBTriggerContext is the trigger context the verdict is resolved over.
type LoopBTriggerContext struct {
	// Event is the declared event. An unrecognized value fails closed as
	// `unknown-event`.
	Event LoopBTriggerEvent
	// Branch is the branch the event runs on (nil = unknown — refused, fail
	// closed).
	Branch *string
	// PRNumber is the PR number when the context is PR-related (ANY number
	// ⇒ PR-critical path).
	PRNumber *int
}

// LoopBScheduledTrigger names an eligible trigger (the TS verdict's
// `trigger` union).
type LoopBScheduledTrigger string

// The eligible triggers.
const (
	LoopBTriggerNightlyScheduled       LoopBScheduledTrigger = "nightly-scheduled"
	LoopBTriggerPostMergeDefaultBranch LoopBScheduledTrigger = "post-merge-default-branch"
)

// LoopBNoopReason is the typed no-op refusal vocabulary (the TS verdict's
// `reason` union, verbatim).
type LoopBNoopReason string

// The no-op reasons in canonical order.
const (
	NoopPRCriticalPathForbidden LoopBNoopReason = "pr-critical-path-forbidden"
	NoopNightlyNotConfigured    LoopBNoopReason = "nightly-not-configured"
	NoopPostMergeNotConfigured  LoopBNoopReason = "post-merge-not-configured"
	NoopNotDefaultBranch        LoopBNoopReason = "not-default-branch"
	NoopUnknownEvent            LoopBNoopReason = "unknown-event"
)

// LoopBNoopReasonValues lists every no-op reason in canonical order.
var LoopBNoopReasonValues = []LoopBNoopReason{
	NoopPRCriticalPathForbidden, NoopNightlyNotConfigured, NoopPostMergeNotConfigured,
	NoopNotDefaultBranch, NoopUnknownEvent,
}

// LoopBTriggerVerdict is the eligibility verdict: exactly one of Trigger /
// Reason is set (the TS discriminated union).
type LoopBTriggerVerdict struct {
	Eligible bool
	Trigger  LoopBScheduledTrigger
	Reason   LoopBNoopReason
}

// SchedulerConfigError is the typed programming error the TS module threw as
// a TypeError for a nonempty default branch (never a configuration the
// workflow can produce). The message preserves the TS text exactly.
type SchedulerConfigError struct {
	Message string
}

func (e *SchedulerConfigError) Error() string { return e.Message }

// ResolveLoopBTriggers resolves whether a Loop B trigger context is eligible
// to run the background audit. PR contexts are refused structurally (see the
// module doc). A nonempty defaultBranch is required — the TS module's
// TypeError is returned as an error (Go idiom: returned values, never panics
// across package boundaries).
func ResolveLoopBTriggers(config LoopBScheduleConfig, context LoopBTriggerContext) (LoopBTriggerVerdict, error) {
	if config.DefaultBranch == "" {
		return LoopBTriggerVerdict{}, &SchedulerConfigError{Message: fmt.Sprintf(
			"LoopBScheduleConfig.defaultBranch must be a nonempty branch name (got %q)",
			config.DefaultBranch)}
	}
	// Structural PR refusal FIRST — a PR event, or ANY PR number in the
	// context, is the developer's PR critical path: never eligible (ARCH
	// §7.2, V-P11-8).
	if context.Event == LoopBEventPR || context.PRNumber != nil {
		return LoopBTriggerVerdict{Eligible: false, Reason: NoopPRCriticalPathForbidden}, nil
	}
	if context.Event == LoopBEventNightly {
		if !config.Nightly {
			return LoopBTriggerVerdict{Eligible: false, Reason: NoopNightlyNotConfigured}, nil
		}
		if context.Branch == nil || *context.Branch != config.DefaultBranch {
			return LoopBTriggerVerdict{Eligible: false, Reason: NoopNotDefaultBranch}, nil
		}
		return LoopBTriggerVerdict{Eligible: true, Trigger: LoopBTriggerNightlyScheduled}, nil
	}
	if context.Event == LoopBEventPostMerge {
		if !config.PostMerge {
			return LoopBTriggerVerdict{Eligible: false, Reason: NoopPostMergeNotConfigured}, nil
		}
		if context.Branch == nil || *context.Branch != config.DefaultBranch {
			return LoopBTriggerVerdict{Eligible: false, Reason: NoopNotDefaultBranch}, nil
		}
		return LoopBTriggerVerdict{Eligible: true, Trigger: LoopBTriggerPostMergeDefaultBranch}, nil
	}
	return LoopBTriggerVerdict{Eligible: false, Reason: NoopUnknownEvent}, nil
}
