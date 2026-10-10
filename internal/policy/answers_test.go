package policy_test

// Ported from packages/policy/test/jev-runtime.test.ts — the "Answer
// aggregation + typed access helpers" and typed-error-code describe blocks
// (src/jev/answers.ts; P2-T10).

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jevUniversalAnswers is the TS UNIVERSAL_ANSWERS fixture of
// jev-runtime.test.ts (value+probability, all four universal keys).
func jevUniversalAnswers() policy.JevAnswers {
	return policy.JevAnswers{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.87},
		"api_contract_changed":    {Value: json.RawMessage("false"), Probability: 0.12},
		"affected_doc_count":      {Value: json.RawMessage("2"), Probability: 0.91},
		"documentation_impact":    {Value: json.RawMessage(`"possible"`), Probability: 0.91},
	}
}

func TestJevTypedErrorCodesAreMachineReadable(t *testing.T) {
	assert.Equal(t, "JEV_CONFIG", policy.JevConfigErrorCode)
	assert.Equal(t, "ANSWER_CONTRACT", policy.AnswerContractErrorCode)
	assert.Equal(t, "ANSWER_KEY_MISSING", policy.AnswerKeyMissingErrorCode)
	assert.Equal(t, "MALFORMED_ANSWER", policy.MalformedAnswerErrorCode)
}

func TestJevAnswerKeyMissingErrorInstanceCarriesTheCode(t *testing.T) {
	_, err := policy.BranchOn[bool](policy.JevAnswers{}, "weather")
	require.Error(t, err)
	var missing *policy.AnswerKeyMissingError
	require.True(t, errors.As(err, &missing))
	assert.Equal(t, "ANSWER_KEY_MISSING", missing.Code())
	assert.Equal(t, "weather", missing.Key)
	assert.Equal(t, "no answer for question key 'weather' — branching/scoring requires the asked key", missing.Error())
}

func TestAggregateAnswersDecidingAnswerProbabilityIsTheOverallConfidence(t *testing.T) {
	aggregated, err := policy.AggregateAnswers(jevUniversalAnswers())
	require.NoError(t, err)
	assert.Equal(t, policy.AggregatedAnswers{
		Confidence: 0.91,
		Rule:       policy.ConfidenceRuleDecidingAnswerProbability,
	}, aggregated)
}

func TestAggregateAnswersDecidingAnswerAbsentUsesMinimumProbability(t *testing.T) {
	answers := jevUniversalAnswers()
	delete(answers, policy.DecidingAnswerKey)
	aggregated, err := policy.AggregateAnswers(answers)
	require.NoError(t, err)
	assert.Equal(t, policy.AggregatedAnswers{
		Confidence: 0.12,
		Rule:       policy.ConfidenceRuleMinAnswerProbability,
	}, aggregated)
}

func TestAggregateAnswersEmptyAnswerSetIsTheClosedFloor(t *testing.T) {
	aggregated, err := policy.AggregateAnswers(policy.JevAnswers{})
	require.NoError(t, err)
	assert.Equal(t, policy.AggregatedAnswers{
		Confidence: 0,
		Rule:       policy.ConfidenceRuleEmptyAnswersFloor,
	}, aggregated)
}

func TestBranchOnReturnsTheValueAndScoreOnReturnsTheProbability(t *testing.T) {
	answers := jevUniversalAnswers()

	changed, err := policy.BranchOn[bool](answers, "public_behavior_changed")
	require.NoError(t, err)
	assert.True(t, changed)

	impact, err := policy.BranchOn[string](answers, "documentation_impact")
	require.NoError(t, err)
	assert.Equal(t, "possible", impact)

	count, err := policy.BranchOn[float64](answers, "affected_doc_count")
	require.NoError(t, err)
	assert.Equal(t, 2.0, count)

	apiContract, err := policy.ScoreOn(answers, "api_contract_changed")
	require.NoError(t, err)
	assert.Equal(t, 0.12, apiContract)

	deciding, err := policy.ScoreOn(answers, policy.DecidingAnswerKey)
	require.NoError(t, err)
	assert.Equal(t, 0.91, deciding)
}

func TestBranchOnAndScoreOnMissingKeyReturnTypedError(t *testing.T) {
	answers := jevUniversalAnswers()

	_, err := policy.BranchOn[any](answers, "weather")
	require.Error(t, err)
	var missing *policy.AnswerKeyMissingError
	require.True(t, errors.As(err, &missing))
	assert.Equal(t, "ANSWER_KEY_MISSING", missing.Code())

	_, err = policy.ScoreOn(answers, "weather")
	require.Error(t, err)
	require.True(t, errors.As(err, &missing))
	assert.Equal(t, "ANSWER_KEY_MISSING", missing.Code())
}

func TestMalformedProbabilityReturnsTypedMalformedAnswerError(t *testing.T) {
	_, err := policy.AggregateAnswers(policy.JevAnswers{
		"documentation_impact": {Value: json.RawMessage(`"none"`), Probability: 1.7},
	})
	require.Error(t, err)
	var malformed *policy.MalformedAnswerError
	require.True(t, errors.As(err, &malformed))
	assert.Equal(t, "MALFORMED_ANSWER", malformed.Code())
	assert.Equal(t, "answer 'documentation_impact' probability must lie in [0, 1] (got 1.7)", malformed.Error())

	_, err = policy.ScoreOn(policy.JevAnswers{
		"affected_doc_count": {Value: json.RawMessage("2"), Probability: math.NaN()},
	}, "affected_doc_count")
	require.Error(t, err)
	require.True(t, errors.As(err, &malformed))
	assert.Equal(t, "MALFORMED_ANSWER", malformed.Code())
	assert.Equal(t, "answer 'affected_doc_count' probability must be a finite number (got NaN)", malformed.Error())

	_, err = policy.ScoreOn(policy.JevAnswers{
		"affected_doc_count": {Value: json.RawMessage("2"), Probability: math.Inf(-1)},
	}, "affected_doc_count")
	require.Error(t, err)
	require.True(t, errors.As(err, &malformed))
	assert.Equal(t, "answer 'affected_doc_count' probability must be a finite number (got -Infinity)", malformed.Error())
}

func TestTypedAnswerErrorsMapOntoTheDocutilCodedErrorFamily(t *testing.T) {
	// CONVENTIONS.md §Errors: every typed error is discoverable through
	// docutil.CodeOf so exit-code routing is uniform.
	_, err := policy.BranchOn[bool](policy.JevAnswers{}, "weather")
	require.Error(t, err)
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))

	_, err = policy.ScoreOn(policy.JevAnswers{
		"affected_doc_count": {Value: json.RawMessage("2"), Probability: 2},
	}, "affected_doc_count")
	require.Error(t, err)
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
}

func TestBranchOnMissingValueFailsClosed(t *testing.T) {
	_, err := policy.BranchOn[string](policy.JevAnswers{
		"documentation_impact": {Probability: 0.9},
	}, "documentation_impact")
	require.Error(t, err)
	var malformed *policy.MalformedAnswerError
	require.True(t, errors.As(err, &malformed))
	assert.Equal(t, "answer 'documentation_impact' must carry a value (ARCH §5.4)", malformed.Error())
}

func TestBranchOnUnreadableValueFailsClosed(t *testing.T) {
	_, err := policy.BranchOn[bool](policy.JevAnswers{
		"public_behavior_changed": {Value: json.RawMessage(`"yes"`), Probability: 0.87},
	}, "public_behavior_changed")
	require.Error(t, err)
	var malformed *policy.MalformedAnswerError
	require.True(t, errors.As(err, &malformed))
	assert.Equal(t, `answer 'public_behavior_changed' value cannot be read as the requested type (got "yes")`, malformed.Error())
}

func TestAggregateAnswersMalformedProbabilityUnderTheMinRuleFailsClosed(t *testing.T) {
	// The deciding key is absent, so the min rule validates every answer —
	// a malformed probability anywhere fails closed with the typed error.
	_, err := policy.AggregateAnswers(policy.JevAnswers{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.87},
		"api_contract_changed":    {Value: json.RawMessage("false"), Probability: math.Inf(1)},
	})
	require.Error(t, err)
	var malformed *policy.MalformedAnswerError
	require.True(t, errors.As(err, &malformed))
	assert.Equal(t, "answer 'api_contract_changed' probability must be a finite number (got Infinity)", malformed.Error())
}
