package policy_test

// Ported from packages/policy/test/jev-runtime.test.ts (P2-T10, P2-T11;
// ARCH §4.6) — the happy path, the application-owned routing override, the
// answer-contract gate, the universal-coverage gate and the routing-table
// totality pins.
//
// Fixture note: the TS tests build decisions through emitDecision over the
// compiled bootstrap workflow; this port builds *schemas.Decision literals
// directly (same shapes, same values) so the suite does not depend on the
// compiler/emit units' seams. The question fixture mirrors the bootstrap
// policy v1 question set exactly (the canonical universal set).

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jevUniversalQuestions is the bootstrap policy v1 question set — the
// canonical UNIVERSAL_QUESTIONS in canonical order (TS compileBootstrapPolicyV1
// compiled.questions).
func jevUniversalQuestions() []policy.Question {
	return []policy.Question{
		{
			Key:    "public_behavior_changed",
			Type:   policy.QuestionTypeBoolean,
			Prompt: "Did the change alter runtime behavior visible to users of this repository?",
		},
		{
			Key:    "api_contract_changed",
			Type:   policy.QuestionTypeBoolean,
			Prompt: "Did the change alter the public contract (exported API surface)?",
		},
		{
			Key:     "affected_doc_count",
			Type:    policy.QuestionTypeScore,
			Prompt:  "How many known documentation candidates does this change affect?",
			Scoring: &policy.QuestionScoring{Min: 0, Max: 50},
		},
		{
			Key:     "documentation_impact",
			Type:    policy.QuestionTypeChoice,
			Prompt:  "What is the documentation impact of this change?",
			Choices: []string{"none", "possible", "definite", "unknown"},
		},
	}
}

// jevChange is the TS CHANGE fixture.
func jevChange() policy.JevChangeRefs {
	return policy.JevChangeRefs{
		Commit:            "81af92e",
		DiffRef:           "git:81af92e",
		ChangeAnalysisRef: "analysis/01J9ZK6X8A",
	}
}

// jevTelemetry is the TS TELEMETRY fixture (jev-runtime.test.ts).
func jevTelemetry() policy.JevTelemetry {
	return policy.JevTelemetry{
		Role:           "decision-engine",
		RequestedModel: "binding/primary",
		RoutedModel:    "provider/routed",
		LatencyMS:      120,
		Attempt:        1,
		FallbackUsed:   false,
	}
}

// jevAnalysisContext is the TS analysisContext() fixture.
func jevAnalysisContext() policy.JevAnalysisContext {
	return policy.JevAnalysisContext{
		ChangeKind:          schemas.ChangeKindBehavioral,
		DocumentationImpact: schemas.DocumentationImpactPossible,
		Risk:                schemas.RiskLevelMedium,
		Confidence:          0.9,
		Answers:             jevUniversalAnswers(),
		Candidates:          []string{"docs/payments.md"},
		Evidence: []schemas.Evidence{
			{Kind: schemas.EvidenceKindSymbolDiff, Ref: "src/payments/refund.ts:42", Note: "idempotencyKey became optional"},
		},
	}
}

// jevPortSpec is the TS PortSpec fixture (answers / abstain / proposedAction /
// rawDecision / portResult overrides).
type jevPortSpec struct {
	answers        map[string]schemas.Answer
	abstain        bool
	abstainReason  *schemas.AbstainReason
	proposedAction schemas.Action
	rawDecision    *schemas.Decision
	portResult     *policy.JevPortResult
}

// jevFakePort is the TS fakePort double — records every call so tests can pin
// "exactly one port call" / "port never called".
type jevFakePort struct {
	t     *testing.T
	spec  jevPortSpec
	calls []policy.JevPortInput
}

func (p *jevFakePort) Decide(input policy.JevPortInput) (policy.JevPortResult, error) {
	p.calls = append(p.calls, input)
	if p.spec.portResult != nil {
		return *p.spec.portResult, nil
	}
	if p.spec.rawDecision != nil {
		return policy.JevPortResult{
			Kind:      policy.JevPortResultKindDecision,
			Decision:  p.spec.rawDecision,
			Telemetry: jevTelemetry(),
		}, nil
	}
	context := input.AnalysisContext
	answers := p.spec.answers
	if answers == nil {
		answers = jevUniversalAnswers()
	}
	action := p.spec.proposedAction
	if action == "" {
		action = schemas.ActionReview
	}
	var abstainReason *schemas.AbstainReason
	if p.spec.abstain {
		reason := p.spec.abstainReason
		if reason == nil {
			insufficient := schemas.AbstainReasonInsufficientConfidence
			reason = &insufficient
		}
		abstainReason = reason
	}
	telemetry := jevTelemetry()
	decision := &schemas.Decision{
		Schema:              schemas.DecisionSchemaID,
		DecisionID:          "d_" + input.Change.Commit,
		PolicyVersion:       1,
		Change:              schemas.DecisionChange{Commit: input.Change.Commit, DiffRef: input.Change.DiffRef, ChangeAnalysisRef: input.Change.ChangeAnalysisRef},
		ChangeKind:          context.ChangeKind,
		DocumentationImpact: context.DocumentationImpact,
		Risk:                context.Risk,
		Confidence:          0.9,
		Answers:             answers,
		Candidates:          context.Candidates,
		Action:              action,
		Abstain:             p.spec.abstain,
		AbstainReason:       abstainReason,
		Evidence:            context.Evidence,
		Trace: schemas.DecisionTrace{
			Model:      telemetry.RoutedModel,
			PolicyHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			LatencyMS:  telemetry.LatencyMS,
		},
	}
	return policy.JevPortResult{
		Kind:      policy.JevPortResultKindDecision,
		Decision:  decision,
		Telemetry: telemetry,
	}, nil
}

// jevFakePortRejecting is the TS port-rejection double: Decide fails the way
// the openrouter adapter's promise rejection surfaces.
type jevFakePortRejecting struct {
	err   error
	calls []policy.JevPortInput
}

func (p *jevFakePortRejecting) Decide(input policy.JevPortInput) (policy.JevPortResult, error) {
	p.calls = append(p.calls, input)
	return policy.JevPortResult{}, p.err
}

// jevRunWith is the TS runWith helper: one port call with the given spec and
// the given analysis-context overrides.
func jevRunWith(t *testing.T, spec jevPortSpec, context func(*policy.JevAnalysisContext)) (policy.JevOutcome, *jevFakePort) {
	t.Helper()
	port := &jevFakePort{t: t, spec: spec}
	analysis := jevAnalysisContext()
	if context != nil {
		context(&analysis)
	}
	outcome, err := policy.RunJev(policy.RunJevInput{
		Compiled:        policy.CompiledPolicyWorkflow{Questions: jevUniversalQuestions()},
		Port:            port,
		Change:          jevChange(),
		AnalysisContext: analysis,
	})
	require.NoError(t, err)
	return outcome, port
}

// jevAnswersWithImpact overrides the documentation_impact answer value +
// probability pair (the TS `{...UNIVERSAL_ANSWERS, documentation_impact: …}`).
func jevAnswersWithImpact(value string, probability float64) map[string]schemas.Answer {
	answers := make(map[string]schemas.Answer, len(jevUniversalAnswers()))
	for key, answer := range jevUniversalAnswers() {
		answers[key] = answer
	}
	answers["documentation_impact"] = schemas.Answer{
		Value:       json.RawMessage(strconv.Quote(value)),
		Probability: probability,
	}
	return answers
}

func TestRunJevHappyPathRoutesWithTheTableVerdictOnExactlyOnePortCall(t *testing.T) {
	outcome, port := jevRunWith(t, jevPortSpec{}, nil)

	require.Equal(t, policy.JevOutcomeDecision, outcome.Kind)
	require.NotNil(t, outcome.Decision)
	assert.Equal(t, policy.JevRoutingOutcome{
		Verdict: policy.JevStage1VerdictReview,
		Rule:    "possible_review",
	}, *outcome.Routing)
	assert.Equal(t, schemas.ActionReview, outcome.Decision.Action)
	assert.False(t, outcome.Decision.Abstain)
	assert.Equal(t, schemas.ActionReview, outcome.ProposedAction)
	assert.Len(t, port.calls, 1)
	assert.Equal(t, jevTelemetry(), outcome.Telemetry)
	for _, key := range schemas.UniversalQuestions {
		answer, ok := outcome.Decision.Answers[key]
		require.True(t, ok, "expected answer for universal key '%s'", key)
		assert.NotEmpty(t, answer.Value, "answer '%s' must carry a value", key)
		assert.GreaterOrEqual(t, answer.Probability, 0.0)
		assert.LessOrEqual(t, answer.Probability, 1.0)
	}
}

func TestRunJevOverridesTheModelProposedActionWithTheDeterministicVerdict(t *testing.T) {
	// Model proposes 'ignore'; the table says 'possible' impact → review.
	outcome, _ := jevRunWith(t, jevPortSpec{proposedAction: schemas.ActionIgnore}, nil)

	require.Equal(t, policy.JevOutcomeDecision, outcome.Kind)
	require.NotNil(t, outcome.Decision)
	assert.Equal(t, schemas.ActionReview, outcome.Decision.Action)
	assert.Equal(t, schemas.ActionIgnore, outcome.ProposedAction)
}

func TestRunJevDecisionLevelAbstentionFlowsThroughForcedToReview(t *testing.T) {
	insufficient := schemas.AbstainReasonInsufficientConfidence
	outcome, _ := jevRunWith(t,
		jevPortSpec{abstain: true, abstainReason: &insufficient},
		func(ctx *policy.JevAnalysisContext) {
			ctx.ChangeKind = schemas.ChangeKindFormattingOnly
			ctx.DocumentationImpact = schemas.DocumentationImpactNone
			ctx.Risk = schemas.RiskLevelLow
		},
	)

	require.Equal(t, policy.JevOutcomeDecision, outcome.Kind)
	require.NotNil(t, outcome.Decision)
	assert.True(t, outcome.Decision.Abstain)
	require.NotNil(t, outcome.Decision.AbstainReason)
	assert.Equal(t, schemas.AbstainReasonInsufficientConfidence, *outcome.Decision.AbstainReason)
	assert.Equal(t, schemas.ActionReview, outcome.Decision.Action)
	assert.Equal(t, policy.JevRoutingOutcome{
		Verdict: policy.JevStage1VerdictReview,
		Rule:    "abstained_review",
	}, *outcome.Routing)
}

func TestRunJevPortOperationalAbstainPassesThroughWithoutRetry(t *testing.T) {
	outcome, port := jevRunWith(t, jevPortSpec{
		portResult: &policy.JevPortResult{
			Kind:      policy.JevPortResultKindAbstain,
			Reason:    schemas.AbstainReasonModelUnavailable,
			Telemetry: jevTelemetry(),
		},
	}, nil)

	require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonModelUnavailable, outcome.Reason)
	assert.Contains(t, outcome.Detail, "without retry")
	assert.Equal(t, jevTelemetry(), outcome.Telemetry)
	assert.Len(t, port.calls, 1)
}

func TestRunJevIsDeterministic(t *testing.T) {
	first, firstPort := jevRunWith(t, jevPortSpec{}, nil)
	second, secondPort := jevRunWith(t, jevPortSpec{}, nil)
	assert.Equal(t, first, second)
	assert.Len(t, firstPort.calls, 1)
	assert.Len(t, secondPort.calls, 1)
}

func TestStage1RoutingTableEntries(t *testing.T) {
	t.Run("none + low risk + formatting_only → ignore (none_safe_trivial)", func(t *testing.T) {
		outcome, _ := jevRunWith(t,
			jevPortSpec{answers: jevAnswersWithImpact("none", 0.95)},
			func(ctx *policy.JevAnalysisContext) {
				ctx.ChangeKind = schemas.ChangeKindFormattingOnly
				ctx.DocumentationImpact = schemas.DocumentationImpactNone
				ctx.Risk = schemas.RiskLevelLow
			},
		)
		require.Equal(t, policy.JevOutcomeDecision, outcome.Kind)
		require.NotNil(t, outcome.Decision)
		assert.Equal(t, schemas.ActionIgnore, outcome.Decision.Action)
		assert.Equal(t, policy.JevRoutingOutcome{
			Verdict: policy.JevStage1VerdictIgnore,
			Rule:    "none_safe_trivial",
		}, *outcome.Routing)
	})

	t.Run("definite impact → investigate (definite_investigate)", func(t *testing.T) {
		outcome, _ := jevRunWith(t,
			jevPortSpec{answers: jevAnswersWithImpact("definite", 0.93)},
			func(ctx *policy.JevAnalysisContext) {
				ctx.ChangeKind = schemas.ChangeKindBehavioral
				ctx.DocumentationImpact = schemas.DocumentationImpactDefinite
				ctx.Risk = schemas.RiskLevelMedium
			},
		)
		require.Equal(t, policy.JevOutcomeDecision, outcome.Kind)
		require.NotNil(t, outcome.Decision)
		assert.Equal(t, schemas.ActionInvestigate, outcome.Decision.Action)
		assert.Equal(t, policy.JevRoutingOutcome{
			Verdict: policy.JevStage1VerdictInvestigate,
			Rule:    "definite_investigate",
		}, *outcome.Routing)
	})

	t.Run("definite impact + high risk → review (definite_high_risk)", func(t *testing.T) {
		outcome, _ := jevRunWith(t,
			jevPortSpec{answers: jevAnswersWithImpact("definite", 0.93)},
			func(ctx *policy.JevAnalysisContext) {
				ctx.ChangeKind = schemas.ChangeKindBehavioral
				ctx.DocumentationImpact = schemas.DocumentationImpactDefinite
				ctx.Risk = schemas.RiskLevelHigh
			},
		)
		require.Equal(t, policy.JevOutcomeDecision, outcome.Kind)
		require.NotNil(t, outcome.Decision)
		assert.Equal(t, schemas.ActionReview, outcome.Decision.Action)
		assert.Equal(t, policy.JevRoutingOutcome{
			Verdict: policy.JevStage1VerdictReview,
			Rule:    "definite_high_risk",
		}, *outcome.Routing)
	})

	t.Run("answer disagrees with the deterministic analysis → conflicting_signals → review", func(t *testing.T) {
		outcome, _ := jevRunWith(t,
			jevPortSpec{answers: jevAnswersWithImpact("definite", 0.9)},
			func(ctx *policy.JevAnalysisContext) {
				ctx.ChangeKind = schemas.ChangeKindBehavioral
				ctx.DocumentationImpact = schemas.DocumentationImpactNone
				ctx.Risk = schemas.RiskLevelLow
			},
		)
		require.Equal(t, policy.JevOutcomeDecision, outcome.Kind)
		require.NotNil(t, outcome.Decision)
		assert.Equal(t, schemas.ActionReview, outcome.Decision.Action)
		assert.Equal(t, policy.JevRoutingOutcome{
			Verdict: policy.JevStage1VerdictReview,
			Rule:    "conflicting_signals",
		}, *outcome.Routing)
	})

	t.Run("none impact on a substantive change kind → none_conservative_default (fail-safe default)", func(t *testing.T) {
		outcome, _ := jevRunWith(t,
			jevPortSpec{answers: jevAnswersWithImpact("none", 0.95)},
			func(ctx *policy.JevAnalysisContext) {
				ctx.ChangeKind = schemas.ChangeKindBehavioral
				ctx.DocumentationImpact = schemas.DocumentationImpactNone
				ctx.Risk = schemas.RiskLevelLow
			},
		)
		require.Equal(t, policy.JevOutcomeDecision, outcome.Kind)
		require.NotNil(t, outcome.Decision)
		assert.Equal(t, schemas.ActionReview, outcome.Decision.Action)
		assert.Equal(t, policy.JevRoutingOutcome{
			Verdict: policy.JevStage1VerdictReview,
			Rule:    "none_conservative_default",
		}, *outcome.Routing)
	})

	t.Run("possible and unknown impacts map to their named review rules (pure resolver)", func(t *testing.T) {
		possibleOutcome, err := policy.ResolveStage1Verdict(policy.JevRoutingSignals{
			ChangeKind:     schemas.ChangeKindBehavioral,
			AnalysisImpact: schemas.DocumentationImpactPossible,
			AnswerImpact:   jevImpactPtr(schemas.DocumentationImpactPossible),
			Risk:           schemas.RiskLevelLow,
			Abstained:      false,
		})
		require.NoError(t, err)
		assert.Equal(t, policy.JevRoutingOutcome{
			Verdict: policy.JevStage1VerdictReview,
			Rule:    "possible_review",
		}, possibleOutcome)

		unknownOutcome, err := policy.ResolveStage1Verdict(policy.JevRoutingSignals{
			ChangeKind:     schemas.ChangeKindBehavioral,
			AnalysisImpact: schemas.DocumentationImpactUnknown,
			AnswerImpact:   jevImpactPtr(schemas.DocumentationImpactUnknown),
			Risk:           schemas.RiskLevelLow,
			Abstained:      false,
		})
		require.NoError(t, err)
		assert.Equal(t, policy.JevRoutingOutcome{
			Verdict: policy.JevStage1VerdictReview,
			Rule:    "unknown_review",
		}, unknownOutcome)
	})
}

func TestEveryRoutingTableEntryCarriesAVerdictFromTheClosedStage1Set(t *testing.T) {
	for _, id := range policy.JevRoutingRuleIDs {
		entry, ok := policy.JevRoutingTable[id]
		require.True(t, ok, "table entry '%s' must be defined", id)
		assert.Contains(t, policy.JevStage1VerdictValues, entry.Verdict)
		assert.Equal(t, id, entry.ID)
		assert.NotEmpty(t, entry.Guard)
	}
}

func TestRoutingTableIsTotalOverTheFullSignalCrossProductAndDeterministic(t *testing.T) {
	changeKinds := schemas.ChangeKindValues
	impacts := schemas.DocumentationImpactValues
	risks := schemas.RiskLevelValues
	count := 0
	for _, changeKind := range changeKinds {
		for _, analysisImpact := range impacts {
			for _, risk := range risks {
				// The TS loop iterates [...impacts, undefined] for the answer
				// impact; -1 is the undefined (nil pointer) case.
				for answerIndex := -1; answerIndex < len(impacts); answerIndex++ {
					var answerImpact *schemas.DocumentationImpact
					if answerIndex >= 0 {
						answerImpact = jevImpactPtr(impacts[answerIndex])
					}
					for _, abstained := range []bool{false, true} {
						signals := policy.JevRoutingSignals{
							ChangeKind:     changeKind,
							AnalysisImpact: analysisImpact,
							AnswerImpact:   answerImpact,
							Risk:           risk,
							Abstained:      abstained,
						}
						outcome, err := policy.ResolveStage1Verdict(signals)
						require.NoError(t, err)
						assert.Contains(t, policy.JevStage1VerdictValues, outcome.Verdict)
						assert.Contains(t, policy.JevRoutingRuleIDs, outcome.Rule)
						again, err := policy.ResolveStage1Verdict(signals)
						require.NoError(t, err)
						assert.Equal(t, outcome, again)
						count++
					}
				}
			}
		}
	}
	assert.Equal(t, 8*4*3*5*2, count)
}

func TestAnswerContractGateWrongValueTypeOnBooleanQuestionAbstains(t *testing.T) {
	answers := jevAnswersWithImpact("possible", 0.91)
	answers["public_behavior_changed"] = schemas.Answer{Value: json.RawMessage(`"yes"`), Probability: 0.87}
	outcome, port := jevRunWith(t, jevPortSpec{answers: answers}, nil)

	require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, outcome.Reason)
	assert.Contains(t, outcome.Detail, "'public_behavior_changed' is a boolean question")
	assert.Len(t, port.calls, 1)
}

func TestAnswerContractGateOutOfBoundsScoreAbstains(t *testing.T) {
	answers := jevAnswersWithImpact("possible", 0.91)
	answers["affected_doc_count"] = schemas.Answer{Value: json.RawMessage("120"), Probability: 0.91}
	outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

	require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, outcome.Reason)
	assert.Contains(t, outcome.Detail, "outside the question scoring bounds [0, 50]")
}

func TestAnswerContractGateHallucinatedAnswerKeyAbstains(t *testing.T) {
	answers := jevAnswersWithImpact("possible", 0.91)
	answers["weather"] = schemas.Answer{Value: json.RawMessage("true"), Probability: 0.9}
	outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

	require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, outcome.Reason)
	assert.Contains(t, outcome.Detail, "hallucinated answer key 'weather'")
}

func TestAnswerContractGateMissingAnswerForAskedKeyAbstains(t *testing.T) {
	answers := jevAnswersWithImpact("possible", 0.91)
	delete(answers, "api_contract_changed")
	outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

	require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, outcome.Reason)
	assert.Contains(t, outcome.Detail, "missing answer for asked question key 'api_contract_changed'")
}

func TestAnswerContractGateProbabilityOutsideRangeAbstains(t *testing.T) {
	answers := jevAnswersWithImpact("possible", 1.5)
	outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

	require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, outcome.Reason)
	assert.Contains(t, outcome.Detail, "answer 'documentation_impact' probability must lie in [0, 1]")
}

func TestAnswerContractGateSchemaDriftedDecisionAbstainsOnFinalParse(t *testing.T) {
	answers := jevAnswersWithImpact("possible", 0.91)
	drifted := &schemas.Decision{
		Schema:              schemas.DecisionSchemaID,
		DecisionID:          "d_81af92e",
		PolicyVersion:       1,
		Change:              schemas.DecisionChange{Commit: "81af92e", DiffRef: "git:81af92e", ChangeAnalysisRef: "analysis/01J9ZK6X8A"},
		ChangeKind:          schemas.ChangeKind("weather"),
		DocumentationImpact: schemas.DocumentationImpactPossible,
		Risk:                schemas.RiskLevelMedium,
		Confidence:          0.9,
		Answers:             answers,
		Candidates:          []string{"docs/payments.md"},
		Action:              schemas.ActionReview,
		Abstain:             false,
		AbstainReason:       nil,
		Evidence:            []schemas.Evidence{},
		Trace:               schemas.DecisionTrace{Model: "provider/routed", PolicyHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", LatencyMS: 120},
	}
	outcome, _ := jevRunWith(t, jevPortSpec{rawDecision: drifted}, nil)

	require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, outcome.Reason)
	assert.Contains(t, outcome.Detail, "DECISION_SCHEMA parse")
}

func TestAssertAnswerContractCollectsEveryViolationNeverFailFast(t *testing.T) {
	err := policy.AssertAnswerContract(jevUniversalQuestions(), map[string]schemas.Answer{
		"public_behavior_changed": {Value: json.RawMessage(`"yes"`), Probability: 0.5},
		"documentation_impact":    {Probability: 0.9},
		"weather":                 {Value: json.RawMessage("true"), Probability: 0.9},
	})
	require.Error(t, err)
	var contractErr *policy.AnswerContractError
	require.True(t, errors.As(err, &contractErr))
	assert.Equal(t, "ANSWER_CONTRACT", contractErr.Code())

	violations := contractErr.Violations
	assert.True(t, jevAnyViolationContains(violations, "boolean question"))
	assert.True(t, jevAnyViolationContains(violations, "missing answer for asked question key 'api_contract_changed'"))
	assert.True(t, jevAnyViolationContains(violations, "missing answer for asked question key 'affected_doc_count'"))
	assert.True(t, jevAnyViolationContains(violations, "must carry a value"))
	assert.True(t, jevAnyViolationContains(violations, "hallucinated answer key 'weather'"))
}

func TestUniversalCoverageGateFailsClosedBeforeAnyModelCall(t *testing.T) {
	// The TS fixture: a universal:false policy carrying only 3 of the 4
	// universal questions (api_contract_changed missing). The composed set
	// misses a canonical universal key → JevConfigError, port never called.
	questions := []policy.Question{
		jevUniversalQuestions()[0],
		jevUniversalQuestions()[2],
		jevUniversalQuestions()[3],
	}
	port := &jevFakePort{t: t}
	_, err := policy.RunJev(policy.RunJevInput{
		Compiled:        policy.CompiledPolicyWorkflow{Questions: questions},
		Port:            port,
		Change:          jevChange(),
		AnalysisContext: jevAnalysisContext(),
	})
	require.Error(t, err)
	var configErr *policy.JevConfigError
	require.True(t, errors.As(err, &configErr))
	assert.Equal(t, "JEV_CONFIG", configErr.Code())
	assert.Equal(t, []string{"api_contract_changed"}, configErr.MissingUniversalKeys)
	assert.Empty(t, port.calls)
	assert.Contains(t, configErr.Error(), "the universal set always runs (ARCH §8.3.1); fail closed before any model call")
}

func TestUniversalQuestionFixtureCoversTheFullCanonicalUniversalSet(t *testing.T) {
	questions := jevUniversalQuestions()
	keys := make([]string, len(questions))
	for i, question := range questions {
		keys[i] = question.Key
	}
	assert.Equal(t, schemas.UniversalQuestions, keys)
}

func TestTypedJevErrorsMapOntoTheDocutilCodedErrorFamily(t *testing.T) {
	// Double identity, mirroring the schemas.EvidenceNotResolvableError
	// precedent: Code() carries the stable TS class code; Unwrap carries the
	// docutil family code, discoverable through docutil.CodeOf.
	// JevConfigError → docutil.CodeConfig (exit-code 2 territory).
	port := &jevFakePort{t: t}
	_, err := policy.RunJev(policy.RunJevInput{
		Compiled: policy.CompiledPolicyWorkflow{Questions: []policy.Question{
			jevUniversalQuestions()[0],
			jevUniversalQuestions()[2],
			jevUniversalQuestions()[3],
		}},
		Port:            port,
		Change:          jevChange(),
		AnalysisContext: jevAnalysisContext(),
	})
	var configErr *policy.JevConfigError
	require.True(t, errors.As(err, &configErr))
	assert.Equal(t, "JEV_CONFIG", configErr.Code())
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))

	// AnswerContractError → docutil.CodeSchema (operational schema_violation).
	err = policy.AssertAnswerContract(jevUniversalQuestions(), map[string]schemas.Answer{})
	var contractErr *policy.AnswerContractError
	require.True(t, errors.As(err, &contractErr))
	assert.Equal(t, "ANSWER_CONTRACT", contractErr.Code())
	assert.Equal(t, docutil.CodeSchema, docutil.CodeOf(err))
}

func TestAnswerContractGateValueShapeViolationMessages(t *testing.T) {
	t.Run("boolean question with a number value", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["public_behavior_changed"] = schemas.Answer{Value: json.RawMessage("3"), Probability: 0.87}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'public_behavior_changed' is a boolean question — value must be true|false (got number)")
	})

	t.Run("boolean question with unparseable JSON value bytes", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["public_behavior_changed"] = schemas.Answer{Value: json.RawMessage("{oops"), Probability: 0.87}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'public_behavior_changed' is a boolean question — value must be true|false (got undefined)")
	})

	t.Run("choice question with a number value", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["documentation_impact"] = schemas.Answer{Value: json.RawMessage("7"), Probability: 0.5}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'documentation_impact' is a choice question — value must be one of [none, possible, definite, unknown] (got 7)")
	})

	t.Run("choice question with an array value renders like JS String()", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["documentation_impact"] = schemas.Answer{Value: json.RawMessage(`["none"]`), Probability: 0.5}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'documentation_impact' is a choice question — value must be one of [none, possible, definite, unknown] (got none)")
	})

	t.Run("choice question with an object value renders like JS String()", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["documentation_impact"] = schemas.Answer{Value: json.RawMessage(`{"impact":"none"}`), Probability: 0.5}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'documentation_impact' is a choice question — value must be one of [none, possible, definite, unknown] (got [object Object])")
	})

	t.Run("score question with a non-number value", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["affected_doc_count"] = schemas.Answer{Value: json.RawMessage("true"), Probability: 0.91}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'affected_doc_count' is a score question — value must be a finite number (got true)")
	})

	t.Run("choice question with a non-member string value", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["documentation_impact"] = schemas.Answer{Value: json.RawMessage(`"sunny"`), Probability: 0.5}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'documentation_impact' is a choice question — value must be one of [none, possible, definite, unknown] (got sunny)")
	})

	t.Run("choice question with a boolean value", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["documentation_impact"] = schemas.Answer{Value: json.RawMessage("true"), Probability: 0.5}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'documentation_impact' is a choice question — value must be one of [none, possible, definite, unknown] (got true)")
	})

	t.Run("choice question with a false value", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["documentation_impact"] = schemas.Answer{Value: json.RawMessage("false"), Probability: 0.5}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'documentation_impact' is a choice question — value must be one of [none, possible, definite, unknown] (got false)")
	})

	t.Run("choice question with a null value", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["documentation_impact"] = schemas.Answer{Value: json.RawMessage("null"), Probability: 0.5}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'documentation_impact' is a choice question — value must be one of [none, possible, definite, unknown] (got null)")
	})

	t.Run("boolean question with an array value", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["public_behavior_changed"] = schemas.Answer{Value: json.RawMessage("[true]"), Probability: 0.87}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'public_behavior_changed' is a boolean question — value must be true|false (got object)")
	})

	t.Run("choice question with unparseable JSON value bytes", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", 0.91)
		answers["documentation_impact"] = schemas.Answer{Value: json.RawMessage("{oops"), Probability: 0.5}
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"'documentation_impact' is a choice question — value must be one of [none, possible, definite, unknown] (got {oops)")
	})

	t.Run("non-finite probability", func(t *testing.T) {
		answers := jevAnswersWithImpact("possible", math.NaN())
		outcome, _ := jevRunWith(t, jevPortSpec{answers: answers}, nil)

		require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
		assert.Contains(t, outcome.Detail,
			"answer 'documentation_impact' probability must be a finite number (got NaN)")
	})
}

func TestRunJevPortRejectionPropagatesVerbatim(t *testing.T) {
	// The Go form of the TS promise rejection: RunJev propagates the port's
	// error (the decision-stage seam maps it to the operational abstain
	// model_unavailable, exactly like the TS orchestrator catch).
	port := &jevFakePortRejecting{err: docutil.ModelError("openrouter: route unavailable")}
	outcome, err := policy.RunJev(policy.RunJevInput{
		Compiled:        policy.CompiledPolicyWorkflow{Questions: jevUniversalQuestions()},
		Port:            port,
		Change:          jevChange(),
		AnalysisContext: jevAnalysisContext(),
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeModel, docutil.CodeOf(err))
	assert.Equal(t, "EDOCSYNC_MODEL: openrouter: route unavailable", err.Error())
	assert.Equal(t, policy.JevOutcome{}, outcome)
	assert.Len(t, port.calls, 1)
}

func TestRunJevPortReturningKindDecisionWithoutADecisionFailsClosed(t *testing.T) {
	// Go-only defensive guard: the TS union forbids kind 'decision' without a
	// decision object; a misbehaving port cannot smuggle a fabricated decision
	// through the runtime.
	outcome, port := jevRunWith(t, jevPortSpec{
		portResult: &policy.JevPortResult{
			Kind:      policy.JevPortResultKindDecision,
			Telemetry: jevTelemetry(),
		},
	}, nil)

	require.Equal(t, policy.JevOutcomeAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, outcome.Reason)
	assert.Len(t, port.calls, 1)
}

/* ------------------------------------------------------------- test helpers */

func jevImpactPtr(impact schemas.DocumentationImpact) *schemas.DocumentationImpact {
	return &impact
}

func jevAnyViolationContains(violations []string, substr string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, substr) {
			return true
		}
	}
	return false
}
