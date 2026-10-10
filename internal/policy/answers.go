// Ported from packages/policy/src/jev/answers.ts (P2-T10; ARCH §4.6, §5.4).
//
// Answer aggregation + typed access helpers. The tripartite split (§2.2)
// makes branching and policy evaluation two DIFFERENT reads of one answer
// object: branching uses the answer VALUE, the policy engine's thresholds use
// the answer PROBABILITY. BranchOn returns the value, ScoreOn returns the
// probability — a caller cannot branch on a probability or score a policy
// threshold with a value without an explicit conversion at the call site.
//
// Overall decision confidence (the §5.4 `confidence` field) is derived by a
// DOCUMENTED deterministic rule (see AggregateAnswers): the probability of the
// single DECIDING answer (documentation_impact — the §5.2 abstention seam)
// when present, else the minimum probability across all answers, else the 0
// floor for an empty answer set.
//
// Pure module: deterministic, no clocks, no I/O. Inputs are the
// already-validated answer records of docsync.decision/v1 (ANSWER_SCHEMA);
// shape violations fail closed with typed errors.
package policy

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DecidingAnswerKey is the §5.2/§5.4 deciding answer key — the single answer
// whose probability IS the overall confidence (TS DECIDING_ANSWER_KEY).
const DecidingAnswerKey = "documentation_impact"

// JevAnswers is a validated answer record keyed by policy question key
// (docsync.decision/v1.answers; TS JevAnswers = Readonly<Record<string,
// Answer>>).
type JevAnswers map[string]schemas.Answer

// ConfidenceAggregationRule names which documented AggregateAnswers rule
// produced the confidence — provenance for the number.
type ConfidenceAggregationRule string

// ConfidenceAggregationRule values (TS ConfidenceAggregationRule).
const (
	ConfidenceRuleDecidingAnswerProbability ConfidenceAggregationRule = "deciding_answer_probability"
	ConfidenceRuleMinAnswerProbability      ConfidenceAggregationRule = "min_answer_probability"
	ConfidenceRuleEmptyAnswersFloor         ConfidenceAggregationRule = "empty_answers_floor"
)

// AggregatedAnswers is the aggregation result (TS AggregatedAnswers).
type AggregatedAnswers struct {
	// Confidence is the overall decision confidence in [0, 1] (ARCH §5.4).
	Confidence float64
	// Rule is the rule that produced the number.
	Rule ConfidenceAggregationRule
}

/* ------------------------------------------------------------------ errors */

// AnswerKeyMissingErrorCode is the stable machine-readable failure code
// (TS AnswerKeyMissingError.code = 'ANSWER_KEY_MISSING').
const AnswerKeyMissingErrorCode = "ANSWER_KEY_MISSING"

// AnswerKeyMissingError is returned when a validated answer record is missing
// the requested question key (TS AnswerKeyMissingError). It is a returned
// value in Go, never a panic.
type AnswerKeyMissingError struct {
	// Key is the question key that was asked for.
	Key string
	// Message preserves the TS message text exactly.
	Message string
}

func newAnswerKeyMissingError(key string) *AnswerKeyMissingError {
	return &AnswerKeyMissingError{
		Key:     key,
		Message: fmt.Sprintf("no answer for question key '%s' — branching/scoring requires the asked key", key),
	}
}

func (e *AnswerKeyMissingError) Error() string { return e.Message }

// Code returns the stable failure code (TS class code).
func (e *AnswerKeyMissingError) Code() string { return AnswerKeyMissingErrorCode }

// Unwrap maps the class onto the docutil coded-error family so docutil.CodeOf
// works uniformly (a missing answer key is a validation failure).
func (e *AnswerKeyMissingError) Unwrap() error {
	return docutil.New(docutil.CodeValidation, e.Message)
}

// MalformedAnswerErrorCode is the stable machine-readable failure code
// (TS MalformedAnswerError.code = 'MALFORMED_ANSWER').
const MalformedAnswerErrorCode = "MALFORMED_ANSWER"

// MalformedAnswerError is returned when an answer record carries a probability
// outside [0, 1] or non-finite, or a value that cannot be read as the
// requested type (TS MalformedAnswerError).
type MalformedAnswerError struct {
	// Message preserves the TS message text exactly.
	Message string
}

func (e *MalformedAnswerError) Error() string { return e.Message }

// Code returns the stable failure code (TS class code).
func (e *MalformedAnswerError) Code() string { return MalformedAnswerErrorCode }

// Unwrap maps the class onto the docutil coded-error family (a malformed
// answer is a validation failure).
func (e *MalformedAnswerError) Unwrap() error {
	return docutil.New(docutil.CodeValidation, e.Message)
}

// assertProbability is the TS assertProbability: finite number within [0, 1].
func assertProbability(key string, probability float64) error {
	if math.IsNaN(probability) || math.IsInf(probability, 0) {
		return &MalformedAnswerError{
			Message: fmt.Sprintf("answer '%s' probability must be a finite number (got %s)", key, formatJSNumber(probability)),
		}
	}
	if probability < 0 || probability > 1 {
		return &MalformedAnswerError{
			Message: fmt.Sprintf("answer '%s' probability must lie in [0, 1] (got %s)", key, formatJSNumber(probability)),
		}
	}
	return nil
}

/* ------------------------------------------------------------- aggregation */

// AggregateAnswers aggregates answers into the overall decision confidence.
//
// Documented rule (binding for the engine wave's displayed confidence):
//  1. documentation_impact present → confidence = its probability (the
//     deciding answer; §5.2/§5.4 abstention keys on exactly this answer).
//  2. Otherwise → confidence = the MINIMUM probability across all answers
//     (conservative: the weakest link gates the decision).
//  3. Empty answer set → confidence 0 (fail closed; an answerless decision
//     carries zero confidence, never a fabricated one).
//
// Deterministic; returns a typed MalformedAnswerError on malformed
// probabilities.
func AggregateAnswers(answers JevAnswers) (AggregatedAnswers, error) {
	if len(answers) == 0 {
		return AggregatedAnswers{Confidence: 0, Rule: ConfidenceRuleEmptyAnswersFloor}, nil
	}
	if deciding, ok := answers[DecidingAnswerKey]; ok {
		if err := assertProbability(DecidingAnswerKey, deciding.Probability); err != nil {
			return AggregatedAnswers{}, err
		}
		return AggregatedAnswers{
			Confidence: deciding.Probability,
			Rule:       ConfidenceRuleDecidingAnswerProbability,
		}, nil
	}
	min := math.Inf(1)
	// TS iterates Object.keys(answers) (wire order); Go maps are unordered, so
	// iterate the sorted keys — deterministic violation order.
	for _, key := range jevSortedKeys(answers) {
		answer := answers[key]
		if err := assertProbability(key, answer.Probability); err != nil {
			return AggregatedAnswers{}, err
		}
		if answer.Probability < min {
			min = answer.Probability
		}
	}
	// Unreachable for a non-empty record — kept fail-closed over silent NaN.
	if math.IsInf(min, 1) {
		return AggregatedAnswers{Confidence: 0, Rule: ConfidenceRuleEmptyAnswersFloor}, nil
	}
	return AggregatedAnswers{Confidence: min, Rule: ConfidenceRuleMinAnswerProbability}, nil
}

/* ---------------------------------------------------------- typed accessors */

// BranchOn is the branching read: returns the answer's VALUE (ARCH §5.4 —
// "branching uses the value"). The caller states the expected value type at
// the call site (BranchOn[bool](answers, "public_behavior_changed")). A
// missing key returns a typed AnswerKeyMissingError (fail closed, never a
// silently zero value); a value that cannot be read as T returns a typed
// MalformedAnswerError.
//
// Deviation from TS: branchOn<T> is an unchecked cast, while the Go generic
// decodes the JSON value into T (fail closed). The TS shape an answer value
// must match is enforced by ANSWER_SCHEMA and the runtime's answer-contract
// gate; the decode only ever rejects shapes those layers already forbid.
func BranchOn[T any](answers JevAnswers, key string) (T, error) {
	var zero T
	answer, ok := answers[key]
	if !ok {
		return zero, newAnswerKeyMissingError(key)
	}
	if len(answer.Value) == 0 {
		return zero, &MalformedAnswerError{
			Message: fmt.Sprintf("answer '%s' must carry a value (ARCH §5.4)", key),
		}
	}
	var value T
	if err := json.Unmarshal(answer.Value, &value); err != nil {
		return zero, &MalformedAnswerError{
			Message: fmt.Sprintf("answer '%s' value cannot be read as the requested type (got %s)", key, string(answer.Value)),
		}
	}
	return value, nil
}

// ScoreOn is the policy read: returns the answer's PROBABILITY (ARCH §5.4 —
// "policy uses the probability"). A missing key returns a typed
// AnswerKeyMissingError; a malformed probability a typed MalformedAnswerError.
func ScoreOn(answers JevAnswers, key string) (float64, error) {
	answer, ok := answers[key]
	if !ok {
		return 0, newAnswerKeyMissingError(key)
	}
	if err := assertProbability(key, answer.Probability); err != nil {
		return 0, err
	}
	return answer.Probability, nil
}

/* ------------------------------------------------------- shared JS renderers */

// jevSortedKeys returns the answer keys in lexicographic order (deterministic
// iteration over a Go map, which has no insertion order).
func jevSortedKeys(answers JevAnswers) []string {
	keys := make([]string, 0, len(answers))
	for key := range answers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// formatJSNumber renders a float the way JS String(number) (used by the TS
// message templates) renders it: "NaN"/"Infinity"/"-Infinity" for the
// non-finite cases, integers without a decimal point, else the shortest
// round-trip form.
func formatJSNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	if f == float64(int64(f)) && f < 1e15 && f > -1e15 {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
