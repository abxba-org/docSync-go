// Black-box port of the scheduler half of packages/core/test/
// audit-loop-b.test.ts (P11-T18; ARCH §7.2 Loop B — Background drift audit;
// PLAN §12.5 V-P11-8).
//
// Pins:
//   - V-P11-8 scheduler discipline: the nightly trigger runs only on the
//     default branch; PR-context triggers are refused STRUCTURALLY
//     (`pr-critical-path-forbidden`) — no config, branch or override can make
//     a PR context eligible.
//   - Fail-closed refusal of unconfigured schedules, unknown branches and
//     unknown events.

package audit_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/stretchr/testify/require"
)

// schedulerConfig is the fully configured schedule the suite resolves over.
func schedulerConfig() audit.LoopBScheduleConfig {
	return audit.LoopBScheduleConfig{Nightly: true, PostMerge: true, DefaultBranch: "main"}
}

func TestVP11_8_NightlyRunsOnlyOnTheDefaultBranch(t *testing.T) {
	config := schedulerConfig()

	// Nightly on the default branch: eligible.
	verdict, err := audit.ResolveLoopBTriggers(config, audit.LoopBTriggerContext{
		Event: audit.LoopBEventNightly, Branch: strPtr("main"), PRNumber: nil,
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: true, Trigger: audit.LoopBTriggerNightlyScheduled,
	}, verdict)

	// Nightly pinned to the default branch ONLY: feature branch refused;
	// unknown branch refused (fail closed).
	verdict, err = audit.ResolveLoopBTriggers(config, audit.LoopBTriggerContext{
		Event: audit.LoopBEventNightly, Branch: strPtr("feature/x"), PRNumber: nil,
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopNotDefaultBranch,
	}, verdict)

	verdict, err = audit.ResolveLoopBTriggers(config, audit.LoopBTriggerContext{
		Event: audit.LoopBEventNightly, Branch: nil, PRNumber: nil,
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopNotDefaultBranch,
	}, verdict)
}

func TestVP11_8_PostMergeEligibleOnlyWhenConfiguredAndOnDefaultBranch(t *testing.T) {
	config := schedulerConfig()

	verdict, err := audit.ResolveLoopBTriggers(config, audit.LoopBTriggerContext{
		Event: audit.LoopBEventPostMerge, Branch: strPtr("main"), PRNumber: nil,
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: true, Trigger: audit.LoopBTriggerPostMergeDefaultBranch,
	}, verdict)

	verdict, err = audit.ResolveLoopBTriggers(config, audit.LoopBTriggerContext{
		Event: audit.LoopBEventPostMerge, Branch: strPtr("feature/x"), PRNumber: nil,
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopNotDefaultBranch,
	}, verdict)

	verdict, err = audit.ResolveLoopBTriggers(
		audit.LoopBScheduleConfig{Nightly: true, PostMerge: false, DefaultBranch: "main"},
		audit.LoopBTriggerContext{
			Event: audit.LoopBEventPostMerge, Branch: strPtr("main"), PRNumber: nil,
		})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopPostMergeNotConfigured,
	}, verdict)
}

func TestVP11_8_PRContextsAreRefusedStructurally(t *testing.T) {
	config := schedulerConfig()

	// A PR event is never eligible — not on the default branch, not with
	// post-merge configured, not for any config combination.
	verdict, err := audit.ResolveLoopBTriggers(config, audit.LoopBTriggerContext{
		Event: audit.LoopBEventPR, Branch: strPtr("main"), PRNumber: intPtr(481),
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopPRCriticalPathForbidden,
	}, verdict)

	verdict, err = audit.ResolveLoopBTriggers(config, audit.LoopBTriggerContext{
		Event: audit.LoopBEventPR, Branch: strPtr("feature/x"), PRNumber: intPtr(481),
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopPRCriticalPathForbidden,
	}, verdict)

	// Belt-and-braces structural seam: ANY context carrying a PR number is
	// PR-critical-path, even if mis-declared as another event type.
	verdict, err = audit.ResolveLoopBTriggers(config, audit.LoopBTriggerContext{
		Event: audit.LoopBEventPostMerge, Branch: strPtr("main"), PRNumber: intPtr(481),
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopPRCriticalPathForbidden,
	}, verdict)
}

func TestVP11_8_UnconfiguredNightlyAndUnknownEventsRefuse(t *testing.T) {
	verdict, err := audit.ResolveLoopBTriggers(
		audit.LoopBScheduleConfig{Nightly: false, PostMerge: false, DefaultBranch: "main"},
		audit.LoopBTriggerContext{
			Event: audit.LoopBEventNightly, Branch: strPtr("main"), PRNumber: nil,
		})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopNightlyNotConfigured,
	}, verdict)

	// Unknown event (the TS `'mystery' as never`): refused, fail closed.
	verdict, err = audit.ResolveLoopBTriggers(schedulerConfig(), audit.LoopBTriggerContext{
		Event: audit.LoopBTriggerEvent("mystery"), Branch: strPtr("main"), PRNumber: nil,
	})
	require.NoError(t, err)
	require.Equal(t, audit.LoopBTriggerVerdict{
		Eligible: false, Reason: audit.NoopUnknownEvent,
	}, verdict)
}

func TestP11T18_NonemptyDefaultBranchIsRequired(t *testing.T) {
	// The TS module threw a TypeError; Go returns it (never panics across
	// package boundaries).
	_, err := audit.ResolveLoopBTriggers(
		audit.LoopBScheduleConfig{Nightly: true, DefaultBranch: ""},
		audit.LoopBTriggerContext{
			Event: audit.LoopBEventNightly, Branch: strPtr("main"), PRNumber: nil,
		})
	require.Error(t, err)
	require.Contains(t, err.Error(),
		"LoopBScheduleConfig.defaultBranch must be a nonempty branch name")
}
