// Ported from packages/policy/test/policy-schema.test.ts — the "universal +
// additive question composition (ARCH §8.3.1)" describe block
// (question-composition.ts assertions).
package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DUDE616/docSync-go/internal/policy"
)

// compositionUniversal is the TS test's universal set.
var compositionUniversal = policy.QuestionSource{
	Questions: []policy.Question{
		{Key: "behavior_changed", Type: policy.QuestionTypeBoolean, Prompt: "Universal behavior?"},
		{Key: "risk_level", Type: policy.QuestionTypeChoice, Prompt: "Universal risk?", Choices: []string{"low", "high"}},
	},
}

func compositionQuestionKeys(source []policy.Question) []string {
	keys := make([]string, 0, len(source))
	for _, question := range source {
		keys = append(keys, question.Key)
	}
	return keys
}

// TestComposeQuestionsReturnsUniversalListWithoutAdditive ports "returns the
// universal list unchanged without an additive policy".
func TestComposeQuestionsReturnsUniversalListWithoutAdditive(t *testing.T) {
	result := policy.ComposeQuestions(policy.ComposeQuestionsInput{UniversalPolicy: compositionUniversal})

	assert.Equal(t, compositionUniversal.Questions, result.Questions)
	assert.Empty(t, result.Conflicts)
}

// TestComposeQuestionsAppendsAdditiveQuestionsInOrder ports "appends additive
// questions after the universal set, preserving order".
func TestComposeQuestionsAppendsAdditiveQuestionsInOrder(t *testing.T) {
	additive := policy.QuestionSource{
		Questions: []policy.Question{
			{Key: "payment_flow_changed", Type: policy.QuestionTypeBoolean, Prompt: "Payment flow?"},
			{Key: "idempotency_changed", Type: policy.QuestionTypeBoolean, Prompt: "Idempotency?"},
		},
	}

	result := policy.ComposeQuestions(policy.ComposeQuestionsInput{
		UniversalPolicy: compositionUniversal,
		AdditivePolicy:  &additive,
	})

	assert.Empty(t, result.Conflicts)
	assert.Equal(t, []string{
		"behavior_changed",
		"risk_level",
		"payment_flow_changed",
		"idempotency_changed",
	}, compositionQuestionKeys(result.Questions))
}

// TestComposeQuestionsRecordsSameKeyRefinement ports "records same-key
// refinement as a conflict and lets the additive definition win".
func TestComposeQuestionsRecordsSameKeyRefinement(t *testing.T) {
	additive := policy.QuestionSource{
		Questions: []policy.Question{
			{
				Key:     "risk_level",
				Type:    policy.QuestionTypeChoice,
				Prompt:  "Repo-specific risk?",
				Choices: []string{"low", "medium"},
			},
			{Key: "webhook_contract_changed", Type: policy.QuestionTypeBoolean, Prompt: "Webhook contract?"},
		},
	}

	result := policy.ComposeQuestions(policy.ComposeQuestionsInput{
		UniversalPolicy: compositionUniversal,
		AdditivePolicy:  &additive,
	})

	assert.Len(t, result.Conflicts, 1)
	assert.Equal(t, "risk_level", result.Conflicts[0].Key)
	assert.Equal(t, policy.CompositionConflictKind("additive_refines_universal"), result.Conflicts[0].Kind)
	assert.Contains(t, result.Conflicts[0].Message, "additive refines universal question 'risk_level'")
	assert.Equal(t, "additive refines universal question 'risk_level': the additive definition wins (ARCH §8.3.1)",
		result.Conflicts[0].Message)

	assert.Equal(t, []string{"behavior_changed", "risk_level", "webhook_contract_changed"}, compositionQuestionKeys(result.Questions))
	refined := result.Questions[1]
	assert.Equal(t, "Repo-specific risk?", refined.Prompt)
	assert.Equal(t, []string{"low", "medium"}, refined.Choices)
}

// TestComposeQuestionsIsDeterministic ports "is deterministic across repeated
// runs".
func TestComposeQuestionsIsDeterministic(t *testing.T) {
	additive := policy.QuestionSource{
		Questions: []policy.Question{{Key: "extra", Type: policy.QuestionTypeBoolean, Prompt: "Extra?"}},
	}

	first := policy.ComposeQuestions(policy.ComposeQuestionsInput{
		UniversalPolicy: compositionUniversal,
		AdditivePolicy:  &additive,
	})
	second := policy.ComposeQuestions(policy.ComposeQuestionsInput{
		UniversalPolicy: compositionUniversal,
		AdditivePolicy:  &additive,
	})

	assert.Equal(t, first, second)
}

// TestComposeQuestionsFirstUniversalOccurrenceWins pins the documented
// duplicate-key rule: the universal index keeps the FIRST occurrence.
func TestComposeQuestionsFirstUniversalOccurrenceWins(t *testing.T) {
	universal := policy.QuestionSource{
		Questions: []policy.Question{
			{Key: "dup", Type: policy.QuestionTypeBoolean, Prompt: "first"},
			{Key: "dup", Type: policy.QuestionTypeBoolean, Prompt: "second"},
		},
	}
	additive := policy.QuestionSource{
		Questions: []policy.Question{{Key: "dup", Type: policy.QuestionTypeBoolean, Prompt: "additive"}},
	}

	result := policy.ComposeQuestions(policy.ComposeQuestionsInput{
		UniversalPolicy: universal,
		AdditivePolicy:  &additive,
	})

	// The refining additive question replaces the FIRST universal slot,
	// leaving the later duplicate untouched.
	assert.Equal(t, "additive", result.Questions[0].Prompt)
	assert.Equal(t, "second", result.Questions[1].Prompt)
	assert.Len(t, result.Conflicts, 1)
}

// TestCompositionConflictKindValues pins COMPOSITION_CONFLICT_KIND_VALUES.
func TestCompositionConflictKindValues(t *testing.T) {
	assert.Equal(t, []string{"additive_refines_universal"}, policy.CompositionConflictKindValues)
}
