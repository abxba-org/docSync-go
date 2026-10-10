// Ported from packages/policy/src/question-composition.ts.

package policy

import "fmt"

// QuestionSource is a policy (or any object carrying its question list) usable
// as a composition input (QuestionSource = Pick<Policy, 'questions'>).
type QuestionSource struct {
	Questions []Question `json:"questions"`
}

// CompositionConflictKindValues lists the documented conflict kinds
// (COMPOSITION_CONFLICT_KIND_VALUES).
var CompositionConflictKindValues = []string{"additive_refines_universal"}

// CompositionConflictKind names which documented composition rule fired
// (CompositionConflictKind).
type CompositionConflictKind string

// ConflictKindAdditiveRefinesUniversal is the single documented rule: a key
// present in both policies means the additive policy REFINES the universal
// question, and the additive definition wins (recorded in conflicts, keeping
// the universal question's position).
const ConflictKindAdditiveRefinesUniversal CompositionConflictKind = "additive_refines_universal"

// CompositionConflict is one key collision between the universal and additive
// question sets (CompositionConflict).
type CompositionConflict struct {
	// Key is the colliding question key.
	Key string `json:"key"`
	// Kind is which documented rule fired.
	Kind CompositionConflictKind `json:"kind"`
	// Message is the compiler-facing diagnostic text.
	Message string `json:"message"`
}

// CompositionResult is the composed question list plus the typed conflicts
// (CompositionResult).
type CompositionResult struct {
	// Questions is the composed list: universal order preserved, additive
	// appended in order.
	Questions []Question `json:"questions"`
	// Conflicts is non-empty iff at least one additive question refines a
	// universal question.
	Conflicts []CompositionConflict `json:"conflicts"`
}

// ComposeQuestionsInput carries the composition inputs
// (composeQuestions(input)).
type ComposeQuestionsInput struct {
	UniversalPolicy QuestionSource  `json:"universalPolicy"`
	AdditivePolicy  *QuestionSource `json:"additivePolicy,omitempty"`
}

// ComposeQuestions composes the universal question set with an optional
// additive policy's questions (composeQuestions). Without an additive policy
// the universal list is returned as-is (same order, no conflicts).
// Deterministic: the same inputs always yield the same output; first
// occurrence wins if a single list contains duplicate keys (schema validation
// rejects duplicate keys for promotable policies).
//
// Key collisions are never thrown — they are typed conflicts returned to the
// compiler, which decides whether the composed policy is promotable. A key
// present in both policies means the additive policy REFINES the universal
// question, and the ADDITIVE definition wins (recorded in conflicts, keeping
// the universal question's position).
func ComposeQuestions(input ComposeQuestionsInput) CompositionResult {
	universalQuestions := input.UniversalPolicy.Questions
	conflicts := make([]CompositionConflict, 0)

	if input.AdditivePolicy == nil {
		return CompositionResult{
			Questions: append(make([]Question, 0, len(universalQuestions)), universalQuestions...),
			Conflicts: conflicts,
		}
	}

	universalIndexByKey := make(map[string]int, len(universalQuestions))
	for i, question := range universalQuestions {
		if _, exists := universalIndexByKey[question.Key]; !exists {
			universalIndexByKey[question.Key] = i
		}
	}

	composed := append(make([]Question, 0, len(universalQuestions)+len(input.AdditivePolicy.Questions)), universalQuestions...)
	for _, question := range input.AdditivePolicy.Questions {
		universalIndex, ok := universalIndexByKey[question.Key]
		if !ok {
			composed = append(composed, question)
			continue
		}
		composed[universalIndex] = question
		conflicts = append(conflicts, CompositionConflict{
			Key:     question.Key,
			Kind:    ConflictKindAdditiveRefinesUniversal,
			Message: fmt.Sprintf("additive refines universal question '%s': the additive definition wins (ARCH §8.3.1)", question.Key),
		})
	}

	return CompositionResult{Questions: composed, Conflicts: conflicts}
}
