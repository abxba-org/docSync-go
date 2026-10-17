package orchestrator_test

// Pure coverage for orchestrator helpers with no I/O: ResolveRouting (and its
// abstainReasonSeed / toAbstainReason helpers) plus PipelineError.Unwrap. Each
// case asserts the ROUTING and the mapped abstain reason/detail as public
// observable outcomes — never code execution alone.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// covDecision is a minimal schema-shaped Decision for routing tests.
func covDecision(action schemas.Action) *schemas.Decision {
	return &schemas.Decision{Schema: schemas.DecisionSchemaID, DecisionID: "d_cov", Action: action}
}

// TestCOV_ORCH_201_ResolveRoutingActionBranches covers ResolveRouting's action
// mapping: patch/investigate coerce to continue, ignore/review pass through,
// and an unknown action fails closed to review.
func TestCOV_ORCH_201_ResolveRoutingActionBranches(t *testing.T) {
	for _, action := range []schemas.Action{schemas.ActionPatch, schemas.ActionInvestigate} {
		res := orchestrator.ResolveRouting(orchestrator.DecisionStageResult{Decision: covDecision(action)})
		assert.Equal(t, schemas.PipelineRoutingContinue, res.Routing, "action %q routes continue", action)
		assert.Nil(t, res.Abstain, "a non-abstaining decision carries no abstain marker")
	}
	assert.Equal(t, schemas.PipelineRoutingIgnore,
		orchestrator.ResolveRouting(orchestrator.DecisionStageResult{Decision: covDecision(schemas.ActionIgnore)}).Routing)
	assert.Equal(t, schemas.PipelineRoutingReview,
		orchestrator.ResolveRouting(orchestrator.DecisionStageResult{Decision: covDecision(schemas.ActionReview)}).Routing)
	unknown := orchestrator.ResolveRouting(orchestrator.DecisionStageResult{Decision: covDecision(schemas.Action("teleport"))})
	assert.Equal(t, schemas.PipelineRoutingReview, unknown.Routing, "unknown action fails closed to review")
}

// TestCOV_ORCH_202_ResolveRoutingAbstainReasonSeeds covers abstainReasonSeed +
// toAbstainReason: the abstain flag, the decision.Abstain flag, the explicit
// valid reason passthrough, the decision-sourced seed, and the two fallbacks —
// nil seed → model_unavailable, unrecognized seed → other.
func TestCOV_ORCH_202_ResolveRoutingAbstainReasonSeeds(t *testing.T) {
	insufficient := schemas.AbstainReasonInsufficientConfidence
	policyGap := schemas.AbstainReasonPolicyGap
	badReason := schemas.AbstainReason("not-a-real-reason")

	// Explicit valid reason → routes review, passes the reason through verbatim.
	flag := orchestrator.ResolveRouting(orchestrator.DecisionStageResult{
		Abstained: true, AbstainReason: &insufficient, Decision: covDecision(schemas.ActionPatch),
	})
	require.NotNil(t, flag.Abstain)
	assert.Equal(t, schemas.PipelineRoutingReview, flag.Routing)
	assert.Equal(t, insufficient, flag.Abstain.Reason, "a schema-valid seed passes through")
	assert.Contains(t, flag.Abstain.Detail, "insufficient_confidence", "the detail embeds the seed reason")

	// decision.Abstain flag with NO explicit seed → seed taken from the decision.
	fromDecision := orchestrator.ResolveRouting(orchestrator.DecisionStageResult{
		Decision: &schemas.Decision{Schema: schemas.DecisionSchemaID, DecisionID: "d", Action: schemas.ActionPatch, Abstain: true, AbstainReason: &policyGap},
	})
	require.NotNil(t, fromDecision.Abstain)
	assert.Equal(t, schemas.PipelineRoutingReview, fromDecision.Routing)
	assert.Equal(t, policyGap, fromDecision.Abstain.Reason, "decision-sourced seed (abstainReasonSeed decision arm)")
	assert.NotContains(t, fromDecision.Abstain.Detail, ":", "no explicit reason → no reason prefix in detail")

	// nil decision + abstain flag → abstainReasonSeed returns nil → model_unavailable.
	nilSeed := orchestrator.ResolveRouting(orchestrator.DecisionStageResult{Abstained: true, Decision: nil})
	require.NotNil(t, nilSeed.Abstain)
	assert.Equal(t, schemas.PipelineRoutingReview, nilSeed.Routing)
	assert.Equal(t, schemas.AbstainReasonModelUnavailable, nilSeed.Abstain.Reason, "nil seed falls back to model_unavailable")

	// Unrecognized seed → toAbstainReason maps to other.
	badSeed := orchestrator.ResolveRouting(orchestrator.DecisionStageResult{
		Abstained: true, AbstainReason: &badReason, Decision: covDecision(schemas.ActionPatch),
	})
	require.NotNil(t, badSeed.Abstain)
	assert.Equal(t, schemas.AbstainReasonOther, badSeed.Abstain.Reason, "an unrecognized seed maps to other")
}

// TestCOV_ORCH_203_PipelineErrorUnwrap covers PipelineError.Unwrap (types.go):
// a wrapped cause unwraps to the sentinel (errors.Is/As traverse it), and a
// cause-less pipeline error unwraps to nil.
func TestCOV_ORCH_203_PipelineErrorUnwrap(t *testing.T) {
	root := errors.New("root cause")
	wrapped := orchestrator.NewPipelineRunError("decorated detail", orchestrator.StageWriter, root)

	require.Error(t, wrapped)
	assert.Equal(t, root, wrapped.Unwrap(), "Unwrap returns the wrapped cause")
	assert.ErrorIs(t, wrapped, root, "errors.Is traverses the cause via Unwrap")

	pipelineErr, ok := orchestrator.AsPipelineError(wrapped)
	require.True(t, ok, "the run error is a PipelineError")
	assert.Equal(t, root, pipelineErr.Unwrap())
	assert.Equal(t, orchestrator.StageWriter, pipelineErr.Stage())
	assert.Equal(t, "decorated detail", pipelineErr.Detail())

	// No cause → nil Unwrap (defensive arm).
	bare := orchestrator.NewPipelineRunError("no cause", orchestrator.StageAnalyze, nil)
	barePipeline, ok := orchestrator.AsPipelineError(bare)
	require.True(t, ok, "AsPipelineError resolves the bare error")
	assert.Nil(t, barePipeline.Unwrap(), "a cause-less pipeline error unwraps to nil")
}

// TestCOV_ORCH_204_ConfigPipelineErrorUnwrap covers NewPipelineConfigError's
// wrapped-cause unwrap, distinct from the run-error lane.
func TestCOV_ORCH_204_ConfigPipelineErrorUnwrap(t *testing.T) {
	root := errors.New("bad config")
	cfg := orchestrator.NewPipelineConfigError("misconfigured stage", orchestrator.StageAnalyze, root)
	require.Error(t, cfg)
	assert.True(t, orchestrator.IsConfigError(cfg), "config errors are detected")
	assert.Equal(t, root, cfg.Unwrap(), "config error unwraps to its cause")
}
