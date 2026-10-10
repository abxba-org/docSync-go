// Ported from packages/policy/test/policy-schema.test.ts — the Jev policy
// document schema (`docsync.jev-policy/v1`; P2-T15; ARCH §8.3, §8.3.1, §8.4;
// D11; OD-R40). Black-box: package policy_test exercises only the exported API.
package policy_test

import (
	"encoding/json"
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

// ---------------------------------------------------------------------------
// helpers (the TS suite's fixtures, in Go form)
// ---------------------------------------------------------------------------

// wireOf renders a typed value in its untrusted wire form (the schema and the
// compiler validate decoded YAML/JSON objects) — the Go form of the TS
// structuredClone for schema tests.
func wireOf(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	return wire
}

// basePolicy is a fresh copy of POLICY_EXAMPLE in wire form
// (TS: basePolicy = structuredClone(POLICY_EXAMPLE)).
func basePolicy(t *testing.T) map[string]any {
	t.Helper()
	return wireOf(t, policy.PolicyExample)
}

// withoutKeys clones value minus the named keys (TS: withoutKeys).
func withoutKeys(value map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	for _, key := range keys {
		delete(out, key)
	}
	return out
}

// withRouting clones value with `routing` replaced.
func withRouting(base map[string]any, routing map[string]any) map[string]any {
	out := withoutKeys(base)
	out["routing"] = routing
	return out
}

// issuesFor is the TS issuesFor helper: the parse MUST fail and the collected
// zod-equivalent issues are returned.
func issuesFor(t *testing.T, source any) []schemas.Issue {
	t.Helper()
	_, err := policy.ParsePolicy(source)
	require.Error(t, err)
	var validationErr *schemas.ValidationError
	require.ErrorAs(t, err, &validationErr)
	return validationErr.Issues
}

// someIssue reports whether any issue matches the predicate
// (TS: issues.some(...)).
func someIssue(issues []schemas.Issue, matches func(schemas.Issue) bool) bool {
	for _, issue := range issues {
		if matches(issue) {
			return true
		}
	}
	return false
}

// stringValues converts a typed enum slice to its plain string view (the TS
// readonly string tuples).
func stringValues[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

func floatPointer(value float64) *float64 { return &value }

// ---------------------------------------------------------------------------
// OD-R40 — Jev policy document id and canonical alignment
// ---------------------------------------------------------------------------

func TestPolicySchemaNamesTheJevPolicyDocumentId(t *testing.T) {
	assert.Equal(t, "docsync.jev-policy/v1", policy.PolicySchemaID)
	assert.Equal(t, "docsync.jev-policy/v1", policy.PolicyExample.Schema)

	parsed, err := policy.ParsePolicy(basePolicy(t))
	require.NoError(t, err)
	require.NotNil(t, parsed)

	// The rename is binding: the old id no longer parses as a Jev policy
	// document…
	renamed := basePolicy(t)
	renamed["schema"] = "docsync.policy/v1"
	_, err = policy.ParsePolicy(renamed)
	require.Error(t, err)

	// …and the internal/schemas-registered docsync.policy/v1 artifact stays
	// the policy.yml live POINTER (OD-R04) — a different object, different id.
	assert.Equal(t, "docsync.policy/v1", policy.PolicyPointerSchemaID)
}

func TestPolicySchemaAlignsExampleQuestionsWithUniversalQuestions(t *testing.T) {
	keys := make([]string, 0, len(policy.PolicyExample.Questions))
	for _, question := range policy.PolicyExample.Questions {
		keys = append(keys, question.Key)
	}
	assert.Equal(t, schemas.UniversalQuestions, keys)

	behavior, apiContract, docCount, impact :=
		policy.PolicyExample.Questions[0], policy.PolicyExample.Questions[1],
		policy.PolicyExample.Questions[2], policy.PolicyExample.Questions[3]
	assert.Equal(t, policy.QuestionTypeBoolean, behavior.Type)
	assert.Equal(t, policy.QuestionTypeBoolean, apiContract.Type)
	assert.Equal(t, policy.QuestionTypeScore, docCount.Type)
	assert.Equal(t, policy.QuestionTypeChoice, impact.Type)
	assert.Equal(t, stringValues(schemas.DocumentationImpactValues), impact.Choices)
}

func TestPolicySchemaCarriesCalibratedThresholds(t *testing.T) {
	assert.Equal(t, policy.Routing{
		HighConfidence:               0.94,
		Review:                       0.72,
		CalibrationFloor:             floatPointer(0.94),
		DocumentationImpactAutoFloor: floatPointer(0.94),
	}, policy.PolicyExample.Routing)
}

// ---------------------------------------------------------------------------
// policy schema (ARCH §8.3, §8.3.1, §8.4)
// ---------------------------------------------------------------------------

func TestPolicySchemaValidatesTheBootstrapUniversalPolicyExample(t *testing.T) {
	parsed, err := policy.ParsePolicy(basePolicy(t))
	require.NoError(t, err)
	require.NotNil(t, parsed)
}

func TestPolicySchemaRoundTripsTheExampleWithoutMutation(t *testing.T) {
	parsed, err := policy.ParsePolicy(basePolicy(t))
	require.NoError(t, err)
	// No defaults injected: the parsed document equals the example verbatim.
	assert.Equal(t, policy.PolicyExample, *parsed)

	// Golden canonical serialization (sorted keys, arrays in order): the
	// policy hash is a sha256 over exactly these bytes, so Go-built hashes
	// match the TypeScript implementation byte-for-byte.
	goldenCanonical := `{"created_at":"2026-01-15T00:00:00.000Z",` +
		`"created_by":"claude-sonnet (policy-architect)",` +
		`"evaluation":{"metrics":{"precision":0.96,"recall":0.93,"unsafe_auto_updates":0},` +
		`"split":"evaluation/universal-bootstrap-v17"},` +
		`"policy_version":17,` +
		`"questions":[` +
		`{"key":"public_behavior_changed","prompt":"Did the change alter runtime behavior visible to users of this repository?","type":"boolean"},` +
		`{"key":"api_contract_changed","prompt":"Did the change alter the public contract (exported API surface)?","type":"boolean"},` +
		`{"key":"affected_doc_count","prompt":"How many known documentation candidates does this change affect?","scoring":{"max":50,"min":0},"type":"score"},` +
		`{"choices":["none","possible","definite","unknown"],"key":"documentation_impact","prompt":"What is the documentation impact of this change?","type":"choice"}` +
		`],` +
		`"reason":"Bootstrap universal policy v17: canonical universal questions with conservative calibrated routing (high_confidence 0.94, review 0.72) and a calibration no-man’s zone; abstain-safe defaults.",` +
		`"routing":{"calibration_floor":0.94,"documentation_impact_auto_floor":0.94,"high_confidence":0.94,"review":0.72},` +
		`"schema":"docsync.jev-policy/v1",` +
		`"taxonomy_version":1,` +
		`"universal":true,` +
		`"unsafe_auto_updates":{"enabled":false}}`
	// The canonical serializer itself produces exactly the golden bytes.
	canonical, err := policy.CanonicalJSON(wireOf(t, policy.PolicyExample))
	require.NoError(t, err)
	assert.Equal(t, goldenCanonical, canonical)

	hash, err := policy.PolicyHash(policy.PolicyExample)
	require.NoError(t, err)
	assert.Equal(t, "sha256:"+docutil.ContentHash(goldenCanonical), hash)
}

func TestPolicySchemaRejectsUnknownTopLevelKeys(t *testing.T) {
	unknown := basePolicy(t)
	unknown["matrix"] = []any{[]any{[]any{[]any{"auto"}}}}
	issues := issuesFor(t, unknown)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "Unrecognized key(s) in object") &&
			strings.Contains(issue.Message, "'matrix'")
	}), "expected an unrecognized-keys issue naming 'matrix', got %+v", issues)
}

func TestPolicySchemaRejectsUnknownNestedKeys(t *testing.T) {
	nestedRouting := basePolicy(t)
	nestedRouting["routing"] = map[string]any{
		"high_confidence":                 0.94,
		"review":                          0.72,
		"calibration_floor":               0.94,
		"documentation_impact_auto_floor": 0.94,
		"confidence_bucket":               "high",
	}
	issues := issuesFor(t, nestedRouting)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "Unrecognized key(s) in object: 'confidence_bucket'") &&
			assert.ObjectsAreEqual([]string{"routing"}, issue.Path)
	}), "expected an unrecognized-keys issue at routing naming 'confidence_bucket', got %+v", issues)

	nestedQuestion := basePolicy(t)
	questions := nestedQuestion["questions"].([]any)
	question := questions[1].(map[string]any)
	question["extra"] = true
	issues = issuesFor(t, nestedQuestion)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "Unrecognized key(s) in object: 'extra'") &&
			assert.ObjectsAreEqual([]string{"questions", "1"}, issue.Path)
	}), "expected an unrecognized-keys issue at questions.1 naming 'extra', got %+v", issues)
}

func TestPolicySchemaRejectsNonPositiveOrNonIntegerPolicyVersion(t *testing.T) {
	zero := basePolicy(t)
	zero["policy_version"] = 0
	assert.Greater(t, len(issuesFor(t, zero)), 0)

	fractional := basePolicy(t)
	fractional["policy_version"] = 1.5
	assert.Greater(t, len(issuesFor(t, fractional)), 0)
}

func TestPolicySchemaRequiresCreatedByReasonAndCreatedAt(t *testing.T) {
	assert.Greater(t, len(issuesFor(t, withoutKeys(basePolicy(t), "created_by"))), 0)
	assert.Greater(t, len(issuesFor(t, withoutKeys(basePolicy(t), "reason"))), 0)
	assert.Greater(t, len(issuesFor(t, withoutKeys(basePolicy(t), "created_at"))), 0)
}

// ---------------------------------------------------------------------------
// routing thresholds (D11 — calibrated data, ordered)
// ---------------------------------------------------------------------------

func TestPolicyRoutingRejectsReviewAboveHighConfidence(t *testing.T) {
	issues := issuesFor(t, withRouting(basePolicy(t), map[string]any{
		"high_confidence": 0.5,
		"review":          0.72,
	}))
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "review ≤ high_confidence") &&
			assert.ObjectsAreEqual([]string{"routing", "review"}, issue.Path)
	}), "expected a named review ≤ high_confidence issue at routing.review, got %+v", issues)
}

func TestPolicyRoutingAcceptsReviewEqualToHighConfidence(t *testing.T) {
	parsed, err := policy.ParsePolicy(withRouting(basePolicy(t), map[string]any{
		"high_confidence": 0.9,
		"review":          0.9,
	}))
	require.NoError(t, err)
	require.NotNil(t, parsed)
}

func TestPolicyRoutingRejectsCalibrationFloorOutsideTheBand(t *testing.T) {
	below := withRouting(basePolicy(t), map[string]any{
		"high_confidence":   0.94,
		"review":            0.72,
		"calibration_floor": 0.5,
	})
	_, err := policy.ParsePolicy(below)
	require.Error(t, err)

	above := withRouting(basePolicy(t), map[string]any{
		"high_confidence":   0.94,
		"review":            0.72,
		"calibration_floor": 0.96,
	})
	_, err = policy.ParsePolicy(above)
	require.Error(t, err)
}

func TestPolicyRoutingRejectsDocumentationImpactAutoFloorBelowReview(t *testing.T) {
	_, err := policy.ParsePolicy(withRouting(basePolicy(t), map[string]any{
		"high_confidence":                 0.94,
		"review":                          0.72,
		"documentation_impact_auto_floor": 0.6,
	}))
	require.Error(t, err)
}

func TestPolicyRoutingRejectsThresholdsOutsideTheUnitInterval(t *testing.T) {
	_, err := policy.ParsePolicy(withRouting(basePolicy(t), map[string]any{
		"high_confidence": 1.2,
		"review":          0.72,
	}))
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// typed question definitions (boolean / choice / score)
// ---------------------------------------------------------------------------

func TestPolicySchemaAcceptsBooleanQuestionWithoutChoicesOrScoring(t *testing.T) {
	withQuestions := withoutKeys(basePolicy(t))
	withQuestions["questions"] = []any{map[string]any{
		"key":    "q",
		"type":   "boolean",
		"prompt": "Question?",
	}}
	withQuestions["routing"] = map[string]any{"high_confidence": 0.94, "review": 0.72}
	parsed, err := policy.ParsePolicy(withQuestions)
	require.NoError(t, err)
	require.NotNil(t, parsed)
}

func TestPolicySchemaRejectsChoiceQuestionWithoutChoices(t *testing.T) {
	withQuestions := withoutKeys(basePolicy(t))
	withQuestions["questions"] = []any{map[string]any{
		"key":    "q",
		"type":   "choice",
		"prompt": "Question?",
	}}
	issues := issuesFor(t, withQuestions)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "must declare choices")
	}), "expected a 'must declare choices' issue, got %+v", issues)
}

func TestPolicySchemaRejectsScoreQuestionWithoutScoring(t *testing.T) {
	withQuestions := withoutKeys(basePolicy(t))
	withQuestions["questions"] = []any{map[string]any{
		"key":    "q",
		"type":   "score",
		"prompt": "Question?",
	}}
	issues := issuesFor(t, withQuestions)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "must declare scoring")
	}), "expected a 'must declare scoring' issue, got %+v", issues)
}

func TestPolicySchemaRejectsChoicesOnNonChoiceAndScoringOnNonScore(t *testing.T) {
	booleanWithChoices := withoutKeys(basePolicy(t))
	booleanWithChoices["questions"] = []any{map[string]any{
		"key":     "q",
		"type":    "boolean",
		"prompt":  "Question?",
		"choices": []any{"low", "medium"},
	}}
	issues := issuesFor(t, booleanWithChoices)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "only allowed on questions with type 'choice'")
	}), "expected an 'only allowed on questions with type 'choice'' issue, got %+v", issues)

	booleanWithScoring := withoutKeys(basePolicy(t))
	booleanWithScoring["questions"] = []any{map[string]any{
		"key":     "q",
		"type":    "boolean",
		"prompt":  "Question?",
		"scoring": map[string]any{"min": 0, "max": 1},
	}}
	issues = issuesFor(t, booleanWithScoring)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "only allowed on questions with type 'score'")
	}), "expected an 'only allowed on questions with type 'score'' issue, got %+v", issues)
}

func TestPolicySchemaRejectsShortOrDuplicateChoiceSets(t *testing.T) {
	single := withoutKeys(basePolicy(t))
	single["questions"] = []any{map[string]any{
		"key":     "q",
		"type":    "choice",
		"prompt":  "Question?",
		"choices": []any{"low"},
	}}
	assert.Greater(t, len(issuesFor(t, single)), 0)

	duplicate := withoutKeys(basePolicy(t))
	duplicate["questions"] = []any{map[string]any{
		"key":     "q",
		"type":    "choice",
		"prompt":  "Question?",
		"choices": []any{"low", "low"},
	}}
	issues := issuesFor(t, duplicate)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "choices must be unique")
	}), "expected a 'choices must be unique' issue, got %+v", issues)
}

func TestPolicySchemaRejectsChoiceStringsOutsideTheClosedSets(t *testing.T) {
	open := withoutKeys(basePolicy(t))
	open["questions"] = []any{map[string]any{
		"key":     "q",
		"type":    "choice",
		"prompt":  "Question?",
		"choices": []any{"low", "freeform"},
	}}
	issues := issuesFor(t, open)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "'freeform' is not a member of the closed policy choice sets")
	}), "expected a closed-sets closure issue for 'freeform', got %+v", issues)
}

func TestPolicySchemaRejectsScoringMaxBelowMin(t *testing.T) {
	inverted := withoutKeys(basePolicy(t))
	inverted["questions"] = []any{map[string]any{
		"key":     "q",
		"type":    "score",
		"prompt":  "Question?",
		"scoring": map[string]any{"min": 5, "max": 1},
	}}
	issues := issuesFor(t, inverted)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "scoring.max must be ≥ scoring.min")
	}), "expected a 'scoring.max must be ≥ scoring.min' issue, got %+v", issues)
}

func TestPolicySchemaRejectsDuplicateQuestionKeys(t *testing.T) {
	duplicated := withoutKeys(basePolicy(t))
	duplicated["questions"] = []any{
		map[string]any{"key": "dup", "type": "boolean", "prompt": "One?"},
		map[string]any{"key": "dup", "type": "boolean", "prompt": "Two?"},
	}
	issues := issuesFor(t, duplicated)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return strings.Contains(issue.Message, "duplicate key 'dup'")
	}), "expected a duplicate-key issue, got %+v", issues)
}

// ---------------------------------------------------------------------------
// gate structs and optional refinements (ARCH §8.4)
// ---------------------------------------------------------------------------

func TestPolicySchemaAcceptsUnsafeAutoUpdatesOnlyWhenDisabled(t *testing.T) {
	disabled := basePolicy(t)
	disabled["unsafe_auto_updates"] = map[string]any{"enabled": false}
	parsed, err := policy.ParsePolicy(disabled)
	require.NoError(t, err)
	require.NotNil(t, parsed)

	enabled := basePolicy(t)
	enabled["unsafe_auto_updates"] = map[string]any{"enabled": true}
	_, err = policy.ParsePolicy(enabled)
	require.Error(t, err)
}

func TestPolicySchemaRejectsRiskClassOverridesOutsideTheRiskSet(t *testing.T) {
	outside := basePolicy(t)
	outside["risk_class_overrides"] = map[string]any{"docs/security/**": "extreme"}
	_, err := policy.ParsePolicy(outside)
	require.Error(t, err)
}

func TestPolicySchemaAcceptsRiskClassOverridesWithGlobs(t *testing.T) {
	inside := basePolicy(t)
	inside["risk_class_overrides"] = map[string]any{
		"docs/security/**":  "high",
		"docs/changelog.md": "low",
	}
	parsed, err := policy.ParsePolicy(inside)
	require.NoError(t, err)
	require.NotNil(t, parsed)
}

// ---------------------------------------------------------------------------
// universal + additive question composition (ARCH §8.3.1)
// ---------------------------------------------------------------------------

func TestPolicyQuestionComposition(t *testing.T) {
	universal := policy.QuestionSource{Questions: []policy.Question{
		{Key: "behavior_changed", Type: policy.QuestionTypeBoolean, Prompt: "Universal behavior?"},
		{
			Key: "risk_level", Type: policy.QuestionTypeChoice, Prompt: "Universal risk?",
			Choices: []string{"low", "high"},
		},
	}}

	t.Run("returns the universal list unchanged without an additive policy", func(t *testing.T) {
		result := policy.ComposeQuestions(policy.ComposeQuestionsInput{UniversalPolicy: universal})
		assert.Equal(t, universal.Questions, result.Questions)
		assert.Empty(t, result.Conflicts)
	})

	t.Run("appends additive questions after the universal set, preserving order", func(t *testing.T) {
		additive := policy.QuestionSource{Questions: []policy.Question{
			{Key: "payment_flow_changed", Type: policy.QuestionTypeBoolean, Prompt: "Payment flow?"},
			{Key: "idempotency_changed", Type: policy.QuestionTypeBoolean, Prompt: "Idempotency?"},
		}}
		result := policy.ComposeQuestions(policy.ComposeQuestionsInput{
			UniversalPolicy: universal,
			AdditivePolicy:  &additive,
		})
		assert.Empty(t, result.Conflicts)
		keys := make([]string, 0, len(result.Questions))
		for _, question := range result.Questions {
			keys = append(keys, question.Key)
		}
		assert.Equal(t, []string{
			"behavior_changed", "risk_level", "payment_flow_changed", "idempotency_changed",
		}, keys)
	})

	t.Run("records same-key refinement as a conflict and lets the additive definition win", func(t *testing.T) {
		additive := policy.QuestionSource{Questions: []policy.Question{
			{
				Key: "risk_level", Type: policy.QuestionTypeChoice, Prompt: "Repo-specific risk?",
				Choices: []string{"low", "medium"},
			},
			{Key: "webhook_contract_changed", Type: policy.QuestionTypeBoolean, Prompt: "Webhook contract?"},
		}}
		result := policy.ComposeQuestions(policy.ComposeQuestionsInput{
			UniversalPolicy: universal,
			AdditivePolicy:  &additive,
		})
		require.Len(t, result.Conflicts, 1)
		conflict := result.Conflicts[0]
		assert.Equal(t, "risk_level", conflict.Key)
		assert.Equal(t, "additive_refines_universal", string(conflict.Kind))
		assert.Contains(t, conflict.Message, "additive refines universal question 'risk_level'")

		keys := make([]string, 0, len(result.Questions))
		for _, question := range result.Questions {
			keys = append(keys, question.Key)
		}
		assert.Equal(t, []string{"behavior_changed", "risk_level", "webhook_contract_changed"}, keys)
		refined := result.Questions[1]
		assert.Equal(t, "Repo-specific risk?", refined.Prompt)
		assert.Equal(t, []string{"low", "medium"}, refined.Choices)
	})

	t.Run("is deterministic across repeated runs", func(t *testing.T) {
		additive := policy.QuestionSource{Questions: []policy.Question{
			{Key: "extra", Type: policy.QuestionTypeBoolean, Prompt: "Extra?"},
		}}
		first := policy.ComposeQuestions(policy.ComposeQuestionsInput{
			UniversalPolicy: universal,
			AdditivePolicy:  &additive,
		})
		second := policy.ComposeQuestions(policy.ComposeQuestionsInput{
			UniversalPolicy: universal,
			AdditivePolicy:  &additive,
		})
		assert.Equal(t, first, second)
	})
}

// ---------------------------------------------------------------------------
// closed choice sets are views over @docsync/schemas (OD-2 drift guard)
// ---------------------------------------------------------------------------

func TestPolicyClosedChoiceSets(t *testing.T) {
	t.Run("mirrors the canonical value arrays exactly", func(t *testing.T) {
		assert.Equal(t, stringValues(schemas.ChangeKindValues),
			policy.PolicyChoiceSets[policy.PolicyChoiceSetChangeKind])
		assert.Equal(t, stringValues(schemas.DocumentationImpactValues),
			policy.PolicyChoiceSets[policy.PolicyChoiceSetDocumentationImpact])
		assert.Equal(t, stringValues(schemas.RiskLevelValues),
			policy.PolicyChoiceSets[policy.PolicyChoiceSetRisk])
		assert.Equal(t, stringValues(schemas.ActionValues),
			policy.PolicyChoiceSets[policy.PolicyChoiceSetAction])
	})

	t.Run("exposes the closed union used by the question refinement", func(t *testing.T) {
		union := make(map[string]struct{}, len(policy.ClosedPolicyChoices))
		for _, choice := range policy.ClosedPolicyChoices {
			union[choice] = struct{}{}
		}
		assert.Len(t, union, len(policy.ClosedPolicyChoices))
		for _, choice := range []string{"behavioral", "possible", "high", "patch"} {
			assert.Contains(t, policy.ClosedPolicyChoices, choice)
		}
	})
}

// ---------------------------------------------------------------------------
// defensive type-mismatch branches — zod's invalid_type / Required /
// unrecognized_keys surface, pinned by exact message text and path (Go tests,
// beyond the TS assertions, to lock the parse surface)
// ---------------------------------------------------------------------------

func TestPolicySchemaRejectsTypeMismatches(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(policy map[string]any)
		wantPath    []string
		wantMessage string
	}{
		{
			name:        "questions not an array",
			mutate:      func(p map[string]any) { p["questions"] = "none" },
			wantPath:    []string{"questions"},
			wantMessage: "Expected array, received string",
		},
		{
			name: "question element not an object",
			mutate: func(p map[string]any) {
				p["questions"] = []any{"not-an-object"}
			},
			wantPath:    []string{"questions", "0"},
			wantMessage: "Expected object, received string",
		},
		{
			name: "choices not an array",
			mutate: func(p map[string]any) {
				p["questions"].([]any)[3].(map[string]any)["choices"] = "none"
			},
			wantPath:    []string{"questions", "3", "choices"},
			wantMessage: "Expected array, received string",
		},
		{
			name: "choice element not a string",
			mutate: func(p map[string]any) {
				p["questions"].([]any)[3].(map[string]any)["choices"] = []any{5, "possible"}
			},
			wantPath:    []string{"questions", "3", "choices", "0"},
			wantMessage: "Expected string, received number",
		},
		{
			name: "choice element empty",
			mutate: func(p map[string]any) {
				p["questions"].([]any)[3].(map[string]any)["choices"] = []any{"", "possible"}
			},
			wantPath:    []string{"questions", "3", "choices", "0"},
			wantMessage: "String must contain at least 1 character(s)",
		},
		{
			name: "scoring not an object",
			mutate: func(p map[string]any) {
				p["questions"].([]any)[2].(map[string]any)["scoring"] = "0..50"
			},
			wantPath:    []string{"questions", "2", "scoring"},
			wantMessage: "Expected object, received string",
		},
		{
			name: "scoring min missing",
			mutate: func(p map[string]any) {
				p["questions"].([]any)[2].(map[string]any)["scoring"] = map[string]any{"max": 50}
			},
			wantPath:    []string{"questions", "2", "scoring", "min"},
			wantMessage: "Required",
		},
		{
			name:        "routing not an object",
			mutate:      func(p map[string]any) { p["routing"] = 0.94 },
			wantPath:    []string{"routing"},
			wantMessage: "Expected object, received number",
		},
		{
			name: "routing high_confidence missing",
			mutate: func(p map[string]any) {
				p["routing"] = map[string]any{"review": 0.72}
			},
			wantPath:    []string{"routing", "high_confidence"},
			wantMessage: "Required",
		},
		{
			name:        "risk_class_overrides not an object",
			mutate:      func(p map[string]any) { p["risk_class_overrides"] = []any{"high"} },
			wantPath:    []string{"risk_class_overrides"},
			wantMessage: "Expected object, received array",
		},
		{
			name: "risk_class_overrides value not a string",
			mutate: func(p map[string]any) {
				p["risk_class_overrides"] = map[string]any{"docs/**": 7}
			},
			wantPath:    []string{"risk_class_overrides", "docs/**"},
			wantMessage: "Expected 'low' | 'medium' | 'high', received number",
		},
		{
			name:        "evaluation not an object",
			mutate:      func(p map[string]any) { p["evaluation"] = "none" },
			wantPath:    []string{"evaluation"},
			wantMessage: "Expected object, received string",
		},
		{
			name: "evaluation unknown key",
			mutate: func(p map[string]any) {
				p["evaluation"].(map[string]any)["unknown"] = true
			},
			wantPath:    []string{"evaluation"},
			wantMessage: "Unrecognized key(s) in object: 'unknown'",
		},
		{
			name: "evaluation metrics not an object",
			mutate: func(p map[string]any) {
				p["evaluation"].(map[string]any)["metrics"] = []any{0.9}
			},
			wantPath:    []string{"evaluation", "metrics"},
			wantMessage: "Expected object, received array",
		},
		{
			name: "evaluation metric not a number",
			mutate: func(p map[string]any) {
				p["evaluation"].(map[string]any)["metrics"] = map[string]any{"precision": "high"}
			},
			wantPath:    []string{"evaluation", "metrics", "precision"},
			wantMessage: "Expected number, received string",
		},
		{
			name: "evaluation metric not finite",
			mutate: func(p map[string]any) {
				p["evaluation"].(map[string]any)["metrics"] = map[string]any{"precision": math.Inf(1)}
			},
			wantPath:    []string{"evaluation", "metrics", "precision"},
			wantMessage: "Number must be finite",
		},
		{
			name: "evaluation metrics missing",
			mutate: func(p map[string]any) {
				delete(p["evaluation"].(map[string]any), "metrics")
			},
			wantPath:    []string{"evaluation", "metrics"},
			wantMessage: "Required",
		},
		{
			name:        "unsafe_auto_updates not an object",
			mutate:      func(p map[string]any) { p["unsafe_auto_updates"] = false },
			wantPath:    []string{"unsafe_auto_updates"},
			wantMessage: "Expected object, received boolean",
		},
		{
			name: "unsafe_auto_updates enabled missing",
			mutate: func(p map[string]any) {
				p["unsafe_auto_updates"] = map[string]any{}
			},
			wantPath:    []string{"unsafe_auto_updates", "enabled"},
			wantMessage: "Required",
		},
		{
			name: "unsafe_auto_updates enabled not a boolean",
			mutate: func(p map[string]any) {
				p["unsafe_auto_updates"] = map[string]any{"enabled": "false"}
			},
			wantPath:    []string{"unsafe_auto_updates", "enabled"},
			wantMessage: "Expected boolean, received string",
		},
		{
			name:        "schema not a string",
			mutate:      func(p map[string]any) { p["schema"] = 17 },
			wantPath:    nil,
			wantMessage: "Expected string, received number",
		},
		{
			name:        "policy_version not a number",
			mutate:      func(p map[string]any) { p["policy_version"] = "17" },
			wantPath:    []string{"policy_version"},
			wantMessage: "Expected number, received string",
		},
		{
			name:        "policy_version NaN",
			mutate:      func(p map[string]any) { p["policy_version"] = math.NaN() },
			wantPath:    []string{"policy_version"},
			wantMessage: "Expected number, received nan",
		},
		{
			name: "high_confidence NaN",
			mutate: func(p map[string]any) {
				routing := p["routing"].(map[string]any)
				routing["high_confidence"] = math.NaN()
			},
			wantPath:    []string{"routing", "high_confidence"},
			wantMessage: "Expected number, received nan",
		},
		{
			name:        "created_by not a string",
			mutate:      func(p map[string]any) { p["created_by"] = 1 },
			wantPath:    []string{"created_by"},
			wantMessage: "Expected string, received number",
		},
		{
			name:        "created_by empty",
			mutate:      func(p map[string]any) { p["created_by"] = "" },
			wantPath:    []string{"created_by"},
			wantMessage: "String must contain at least 1 character(s)",
		},
		{
			name:        "universal not a boolean",
			mutate:      func(p map[string]any) { p["universal"] = "true" },
			wantPath:    []string{"universal"},
			wantMessage: "Expected boolean, received string",
		},
		{
			name:        "taxonomy_version not positive",
			mutate:      func(p map[string]any) { p["taxonomy_version"] = 0 },
			wantPath:    []string{"taxonomy_version"},
			wantMessage: "Number must be greater than 0",
		},
		{
			name:        "extends empty",
			mutate:      func(p map[string]any) { p["extends"] = "" },
			wantPath:    []string{"extends"},
			wantMessage: "String must contain at least 1 character(s)",
		},
		{
			name: "calibration_floor not a number",
			mutate: func(p map[string]any) {
				routing := p["routing"].(map[string]any)
				routing["calibration_floor"] = false
			},
			wantPath:    []string{"routing", "calibration_floor"},
			wantMessage: "Expected number, received boolean",
		},
		{
			name:        "schema missing",
			mutate:      func(p map[string]any) { delete(p, "schema") },
			wantPath:    nil,
			wantMessage: "Required",
		},
		{
			name:        "questions empty",
			mutate:      func(p map[string]any) { p["questions"] = []any{} },
			wantPath:    []string{"questions"},
			wantMessage: "Array must contain at least 1 element(s)",
		},
		{
			name: "evaluation metric empty key",
			mutate: func(p map[string]any) {
				p["evaluation"].(map[string]any)["metrics"] = map[string]any{"": 1}
			},
			wantPath:    []string{"evaluation", "metrics", ""},
			wantMessage: "String must contain at least 1 character(s)",
		},
		{
			name: "unsafe_auto_updates unknown key",
			mutate: func(p map[string]any) {
				p["unsafe_auto_updates"] = map[string]any{"enabled": false, "extra": 1}
			},
			wantPath:    []string{"unsafe_auto_updates"},
			wantMessage: "Unrecognized key(s) in object: 'extra'",
		},
		{
			name: "risk_class_overrides empty pattern",
			mutate: func(p map[string]any) {
				p["risk_class_overrides"] = map[string]any{"": "high"}
			},
			wantPath:    []string{"risk_class_overrides", ""},
			wantMessage: "String must contain at least 1 character(s)",
		},
		{
			name:        "extends not a string",
			mutate:      func(p map[string]any) { p["extends"] = 5 },
			wantPath:    []string{"extends"},
			wantMessage: "Expected string, received number",
		},
		{
			name: "high_confidence not a number",
			mutate: func(p map[string]any) {
				routing := p["routing"].(map[string]any)
				routing["high_confidence"] = "0.94"
			},
			wantPath:    []string{"routing", "high_confidence"},
			wantMessage: "Expected number, received string",
		},
		{
			name: "high_confidence negative",
			mutate: func(p map[string]any) {
				routing := p["routing"].(map[string]any)
				routing["high_confidence"] = -0.5
			},
			wantPath:    []string{"routing", "high_confidence"},
			wantMessage: "Number must be greater than or equal to 0",
		},
		{
			name: "documentation_impact_auto_floor not a number",
			mutate: func(p map[string]any) {
				routing := p["routing"].(map[string]any)
				routing["documentation_impact_auto_floor"] = "0.94"
			},
			wantPath:    []string{"routing", "documentation_impact_auto_floor"},
			wantMessage: "Expected number, received string",
		},
		{
			name: "scoring empty object",
			mutate: func(p map[string]any) {
				p["questions"].([]any)[2].(map[string]any)["scoring"] = map[string]any{}
			},
			wantPath:    []string{"questions", "2", "scoring", "min"},
			wantMessage: "Required",
		},
		{
			name:        "policy_version a malformed json.Number",
			mutate:      func(p map[string]any) { p["policy_version"] = json.Number("abc") },
			wantPath:    []string{"policy_version"},
			wantMessage: "Expected number, received number",
		},
		{
			name: "multiple unknown keys sorted deterministically",
			mutate: func(p map[string]any) {
				p["zebra"] = 1
				p["apple"] = 2
			},
			wantPath:    nil,
			wantMessage: "Unrecognized key(s) in object: 'apple', 'zebra'",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broken := basePolicy(t)
			testCase.mutate(broken)
			issues := issuesFor(t, broken)
			matched := someIssue(issues, func(issue schemas.Issue) bool {
				return issue.Message == testCase.wantMessage &&
					assert.ObjectsAreEqual(testCase.wantPath, issue.Path)
			})
			assert.True(t, matched,
				"expected %+v issue %q, got %+v", testCase.wantPath, testCase.wantMessage, issues)
		})
	}
}

func TestPolicySchemaAcceptsYAMLStyleAndJSONNumberInput(t *testing.T) {
	// YAML decoders hand the parser map[any]any objects; JSON decoders with
	// UseNumber hand json.Number scalars — both are valid untrusted input.
	t.Run("map[any]any objects (yaml.v3 shape)", func(t *testing.T) {
		parsed, err := policy.ParsePolicy(toYAMLShape(basePolicy(t)))
		require.NoError(t, err)
		require.NotNil(t, parsed)
	})

	t.Run("json.Number scalars", func(t *testing.T) {
		parsed, err := policy.ParsePolicy(toJSONNumberShape(basePolicy(t)))
		require.NoError(t, err)
		require.NotNil(t, parsed)
	})
}

// toYAMLShape converts decoded JSON value shapes into the map[any]any shape
// yaml.v3 produces for interface targets.
func toYAMLShape(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[any]any, len(typed))
		for key, item := range typed {
			out[key] = toYAMLShape(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, toYAMLShape(item))
		}
		return out
	default:
		return value
	}
}

// toJSONNumberShape converts every float64 into a json.Number (the shape a
// json.Decoder with UseNumber produces).
func toJSONNumberShape(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = toJSONNumberShape(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, toJSONNumberShape(item))
		}
		return out
	case float64:
		return json.Number(strconv.FormatFloat(typed, 'g', -1, 64))
	default:
		return value
	}
}

func TestPolicySchemaAcceptsIntegerNumericForms(t *testing.T) {
	// Decoders hand integers as int/int64 (not only float64) — every numeric
	// field accepts all forms.
	withInts := basePolicy(t)
	withInts["policy_version"] = 17
	withInts["taxonomy_version"] = int64(1)
	evaluation := withInts["evaluation"].(map[string]any)
	evaluation["metrics"] = map[string]any{"precision": float32(0.5)}
	parsed, err := policy.ParsePolicy(withInts)
	require.NoError(t, err)
	require.NotNil(t, parsed)

	// A non-string YAML mapping key is normalized to its string form and then
	// rejected as an unknown key (never silently dropped).
	nonStringKey := toYAMLShape(basePolicy(t)).(map[any]any)
	nonStringKey[17] = "x"
	issues := issuesFor(t, nonStringKey)
	assert.True(t, someIssue(issues, func(issue schemas.Issue) bool {
		return issue.Message == "Unrecognized key(s) in object: '17'"
	}), "expected the normalized key '17' to be reported, got %+v", issues)
}
