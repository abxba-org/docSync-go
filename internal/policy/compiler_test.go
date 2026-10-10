// Ported from packages/policy/test/policy-compiler.test.ts — the deterministic
// policy compiler (P2-T16; ARCH §8.2): all-or-nothing compilation, named
// diagnostics with path precision, the trigger-taxonomy seam (R17), additive
// composition (§8.3.1) and semantic checks. Black-box: package policy_test.
package policy_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers (the TS suite's fixtures, in Go form)
// ---------------------------------------------------------------------------

// wireQuestions renders typed questions in wire form.
func wireQuestions(t *testing.T, questions []policy.Question) []any {
	t.Helper()
	raw, err := json.Marshal(questions)
	require.NoError(t, err)
	var wire []any
	require.NoError(t, json.Unmarshal(raw, &wire))
	return wire
}

// cloneExample is a fresh wire copy of POLICY_EXAMPLE (TS: cloneExample()).
func cloneExample(t *testing.T) map[string]any {
	t.Helper()
	return wireOf(t, policy.PolicyExample)
}

// bootstrapPolicy is a fresh wire copy of the bootstrap default policy v1
// (TS: BOOTSTRAP_POLICY_V1 handed straight to compilePolicy, which
// schema-validates the untrusted object either way).
func bootstrapPolicy(t *testing.T) map[string]any {
	t.Helper()
	return wireOf(t, policy.BootstrapPolicyV1)
}

// additivePolicy builds an additive repository policy (TS: additivePolicy).
func additivePolicy(t *testing.T, questions []policy.Question) map[string]any {
	t.Helper()
	additive := cloneExample(t)
	additive["policy_version"] = 99
	additive["questions"] = wireQuestions(t, questions)
	return additive
}

// triggerMapExample mirrors the canonical `docsync.trigger-map/v1` document
// (TS: TRIGGER_MAP_EXAMPLE from @docsync/schemas).
func triggerMapExample() *policy.TriggerTaxonomyMap {
	return &policy.TriggerTaxonomyMap{
		TaxonomyVersion: schemas.TriggerMapExample.TaxonomyVersion,
		Mappings:        schemas.TriggerMapExample.Mappings,
	}
}

// diagnosticsOf asserts compilation FAILED and returns the diagnostics
// (TS: diagnosticsOf — 'compiled' in result === false).
func diagnosticsOf(t *testing.T, result policy.CompilePolicyResult) []policy.PolicyDiagnostic {
	t.Helper()
	require.False(t, result.OK)
	require.Nil(t, result.Compiled, "a failed compilation must never carry a workflow (ARCH §8.2)")
	return result.Diagnostics
}

func questionKeys(questions []policy.Question) []string {
	keys := make([]string, 0, len(questions))
	for _, question := range questions {
		keys = append(keys, question.Key)
	}
	return keys
}

// ---------------------------------------------------------------------------
// V-P2-1: failing policies never compile — named diagnostics, all-or-nothing
// ---------------------------------------------------------------------------

func TestV_P2_1FailingPoliciesNeverCompile(t *testing.T) {
	t.Run("open choice set → choice_set_violation naming the closed sets with path precision", func(t *testing.T) {
		broken := cloneExample(t)
		questions := broken["questions"].([]any)
		impact := questions[3].(map[string]any)
		impact["choices"] = []any{"none", "maybe_freeform"}

		diagnostics := diagnosticsOf(t, policy.CompilePolicy(broken))
		assert.True(t, func() bool {
			for _, diagnostic := range diagnostics {
				if diagnostic.Kind == "choice_set_violation" &&
					diagnostic.Path == "questions.3.choices" &&
					assert.Contains(t, diagnostic.Message,
						"'maybe_freeform' is not a member of the closed policy choice sets") {
					return true
				}
			}
			return false
		}(), "expected a choice_set_violation at questions.3.choices, got %+v", diagnostics)
	})

	t.Run("bad threshold (> 1) → routing_threshold_violation with path precision", func(t *testing.T) {
		broken := cloneExample(t)
		broken["routing"] = map[string]any{"high_confidence": 1.2, "review": 0.72}

		diagnostics := diagnosticsOf(t, policy.CompilePolicy(broken))
		assert.True(t, func() bool {
			for _, diagnostic := range diagnostics {
				if diagnostic.Kind == "routing_threshold_violation" &&
					diagnostic.Path == "routing.high_confidence" {
					return true
				}
			}
			return false
		}(), "expected a routing_threshold_violation at routing.high_confidence, got %+v", diagnostics)
	})

	t.Run("a threshold of exactly 1.0 is legal calibration data (V-P2-1 ratified boundary)", func(t *testing.T) {
		// D13 MINOR-1: the ratified V-P2-1 boundary — the schema's threshold
		// range is the CLOSED unit interval [0, 1], so exactly 1.0 is legal
		// calibration data (the 'bad threshold (> 1)' case bounds the
		// exclusive side at 1.2). Risk with a 1.0 auto floor stays bounded
		// WITHOUT any threshold hard-coding: the D2 ownership floor resolves
		// locked/generated/external at ANY confidence, the fixed matrix shape
		// never yields `auto` outside low×high, and the calibration gate
		// abstains the open interval (review, calibration_floor).
		result := policy.CompilePolicy(withRouting(cloneExample(t), map[string]any{
			"high_confidence":   1,
			"review":            0.72,
			"calibration_floor": 1,
		}))
		require.True(t, result.OK)
		require.NotNil(t, result.Compiled)

		// Compiled routing data round-trips verbatim — D11 calibrated data is
		// consumed by the matrix, never rewritten or clamped by the compiler.
		assert.Equal(t, policy.Routing{
			HighConfidence:   1,
			Review:           0.72,
			CalibrationFloor: floatPointer(1),
		}, result.Compiled.Routing)
		// (The end-to-end matrix assertions of the TS test
		// — confidenceBucket/mapOutcome on the compiled data — are owned by the
		// engine unit's port of engine-matrix.test.ts, which consumes this
		// same compiled workflow.)
	})

	t.Run("unknown question type → question_type_violation with path precision", func(t *testing.T) {
		broken := cloneExample(t)
		broken["questions"] = []any{map[string]any{
			"key":    "public_behavior_changed",
			"type":   "essay",
			"prompt": "Did the change alter behavior?",
		}}

		diagnostics := diagnosticsOf(t, policy.CompilePolicy(broken))
		assert.True(t, func() bool {
			for _, diagnostic := range diagnostics {
				if diagnostic.Kind == "question_type_violation" &&
					diagnostic.Path == "questions.0.type" {
					return true
				}
			}
			return false
		}(), "expected a question_type_violation at questions.0.type, got %+v", diagnostics)
	})

	t.Run("never compiles partially: failures carry diagnostics, never a workflow", func(t *testing.T) {
		result := policy.CompilePolicy(map[string]any{"schema": "docsync.jev-policy/v1"})
		require.False(t, result.OK)
		assert.NotEmpty(t, result.Diagnostics)
		assert.Nil(t, result.Compiled)
	})

	t.Run("collects every diagnostic for one broken policy (never fail-fast)", func(t *testing.T) {
		broken := cloneExample(t)
		broken["routing"] = map[string]any{"high_confidence": 0.5, "review": 0.72}
		broken["questions"] = []any{
			map[string]any{"key": "public_behavior_changed", "type": "boolean", "prompt": "Behavior?"},
			map[string]any{"key": "public_behavior_changed", "type": "boolean", "prompt": "Behavior again?"},
		}

		diagnostics := diagnosticsOf(t, policy.CompilePolicy(broken))
		assert.NotNil(t, findDiagnostic(diagnostics, "routing_threshold_violation"))
		assert.NotNil(t, findDiagnostic(diagnostics, "duplicate_question_key"))
	})
}

// ---------------------------------------------------------------------------
// trigger-taxonomy seam (R17; V-P2-2 groundwork)
// ---------------------------------------------------------------------------

func TestCompilerTriggerTaxonomySeam(t *testing.T) {
	t.Run("rejects a taxonomy document that fails docsync.trigger-map/v1, naming the expected schema version", func(t *testing.T) {
		diagnostics := diagnosticsOf(t, policy.CompilePolicy(cloneExample(t), policy.WithTriggerTaxonomy(
			&policy.TriggerTaxonomyMap{
				TaxonomyVersion: 2,
				Mappings:        map[string]string{"public_behavior": "behavioral"},
			})))
		diagnostic := findDiagnostic(diagnostics, "taxonomy_invalid")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "docsync.trigger-map/v1", diagnostic.ExpectedSchemaVersion)
		assert.Contains(t, diagnostic.Message, "docsync.trigger-map/v1")
		assert.Contains(t, diagnostic.Message, policy.DecisionSchemaVersion)
	})

	t.Run("rejects a policy/taxonomy version mismatch, naming the expected schema version (R17)", func(t *testing.T) {
		mismatched := cloneExample(t)
		mismatched["taxonomy_version"] = 2
		diagnostics := diagnosticsOf(t, policy.CompilePolicy(mismatched,
			policy.WithTriggerTaxonomy(triggerMapExample())))
		diagnostic := findDiagnostic(diagnostics, "taxonomy_version_mismatch")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "docsync.trigger-map/v2", diagnostic.ExpectedSchemaVersion)
		assert.Contains(t, diagnostic.Message, "docsync.trigger-map/v2")
		assert.Contains(t, diagnostic.Message, policy.DecisionSchemaVersion)
	})

	t.Run("accepts the canonical taxonomy map and pins the effective taxonomy version", func(t *testing.T) {
		result := policy.CompilePolicy(bootstrapPolicy(t), policy.WithTriggerTaxonomy(triggerMapExample()))
		require.True(t, result.OK)
		require.NotNil(t, result.Compiled)
		assert.Equal(t, schemas.TriggerMapExample.TaxonomyVersion, result.Compiled.TaxonomyVersion)
		assert.Equal(t, "docsync.decision/v1", result.Compiled.DecisionSchemaVersion)
	})

	t.Run("unknown trigger vocabulary → unknown_trigger naming the taxonomy version and decision schema", func(t *testing.T) {
		validation := policy.ValidateTriggerVocabulary(
			[]string{"public_behavior", "webhook_contract_changed"},
			policy.ValidateTriggerVocabularyOptions{Direction: policy.TriggerDirectionTriggers})
		assert.Equal(t, []string{"public_behavior"}, validation.Mapped)
		assert.Equal(t, []string{"webhook_contract_changed"}, validation.UnknownValues)
		require.Len(t, validation.Diagnostics, 1)
		diagnostic := validation.Diagnostics[0]
		assert.Equal(t, "unknown_trigger", diagnostic.Kind)
		assert.Equal(t, "triggers", diagnostic.Path)
		assert.Equal(t, "docsync.trigger-map/v1", diagnostic.ExpectedSchemaVersion)
		assert.Contains(t, diagnostic.Message, `"webhook_contract_changed"`)
		assert.Contains(t, diagnostic.Message, "expected trigger-taxonomy version 1")
		assert.Contains(t, diagnostic.Message, "docsync.trigger-map/v1")
		assert.Contains(t, diagnostic.Message, "docsync.decision/v1")
		assert.Contains(t, diagnostic.Message, "supported values:")
	})

	t.Run("v1 policy schema declares no trigger field — the vocabulary seam is the documented skip", func(t *testing.T) {
		// The TS test asserts Object.keys(policy) carries no triggers/ignore;
		// the strict-schema Go equivalent is that a policy carrying either key
		// is rejected as an unknown key (OD-R01).
		withTriggers := cloneExample(t)
		withTriggers["triggers"] = []any{"public_behavior"}
		_, err := policy.ParsePolicy(withTriggers)
		require.Error(t, err)

		withIgnore := cloneExample(t)
		withIgnore["ignore"] = []any{"docs/**"}
		_, err = policy.ParsePolicy(withIgnore)
		require.Error(t, err)

		assert.Equal(t, policy.TriggerVocabularyValidation{
			Mapped:        []string{},
			UnknownValues: []string{},
			Diagnostics:   []policy.PolicyDiagnostic{},
		}, policy.ValidateTriggerVocabulary([]string{},
			policy.ValidateTriggerVocabularyOptions{Direction: policy.TriggerDirectionIgnore}))
	})
}

// ---------------------------------------------------------------------------
// additive composition (ARCH §8.3.1)
// ---------------------------------------------------------------------------

func TestCompilerAdditiveComposition(t *testing.T) {
	t.Run("appends distinct additive questions after the universal set", func(t *testing.T) {
		result := policy.CompilePolicy(bootstrapPolicy(t), policy.WithAdditivePolicies(
			additivePolicy(t, []policy.Question{
				{Key: "payment_flow_changed", Type: policy.QuestionTypeBoolean, Prompt: "Payment flow?"},
			})))
		require.True(t, result.OK)
		require.NotNil(t, result.Compiled)
		expected := append(append([]string{}, schemas.UniversalQuestions...), "payment_flow_changed")
		assert.Equal(t, expected, questionKeys(result.Compiled.Questions))
	})

	t.Run("a same-key additive refinement is a compile-blocking diagnostic (v1 conservative rule)", func(t *testing.T) {
		diagnostics := diagnosticsOf(t, policy.CompilePolicy(bootstrapPolicy(t),
			policy.WithAdditivePolicies(additivePolicy(t, []policy.Question{
				{Key: "public_behavior_changed", Type: policy.QuestionTypeBoolean, Prompt: "Repo-refined behavior?"},
			}))))
		diagnostic := findDiagnostic(diagnostics, "additive_refines_universal")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "questions.public_behavior_changed", diagnostic.Path)
	})

	t.Run("an invalid additive policy fails compilation with prefixed path diagnostics", func(t *testing.T) {
		diagnostics := diagnosticsOf(t, policy.CompilePolicy(bootstrapPolicy(t),
			policy.WithAdditivePolicies(map[string]any{"schema": "docsync.jev-policy/v1"})))
		assert.NotEmpty(t, diagnostics)
		for _, diagnostic := range diagnostics {
			assert.Contains(t, diagnostic.Path, "additivePolicies.0.",
				"every diagnostic must be prefixed by the additive input location, got %+v", diagnostic)
		}
	})
}

// ---------------------------------------------------------------------------
// semantic compile checks beyond the schema (ARCH §8.2)
// ---------------------------------------------------------------------------

func TestCompilerSemanticChecks(t *testing.T) {
	t.Run("requires the documentation_impact question (the §5.2 abstention seam)", func(t *testing.T) {
		missing := cloneExample(t)
		missing["universal"] = false
		questions := missing["questions"].([]any)
		filtered := make([]any, 0, len(questions))
		for _, item := range questions {
			question := item.(map[string]any)
			if question["key"] == "documentation_impact" {
				continue
			}
			filtered = append(filtered, item)
		}
		missing["questions"] = filtered

		diagnostics := diagnosticsOf(t, policy.CompilePolicy(missing))
		assert.NotNil(t, findDiagnostic(diagnostics, "documentation_impact_missing"))
	})

	t.Run("rejects a non-choice documentation_impact question (the seam keys an answer probability)", func(t *testing.T) {
		nonChoice := cloneExample(t)
		questions := nonChoice["questions"].([]any)
		for _, item := range questions {
			question := item.(map[string]any)
			if question["key"] == "documentation_impact" {
				question["type"] = "boolean"
				delete(question, "choices")
			}
		}

		diagnostics := diagnosticsOf(t, policy.CompilePolicy(nonChoice))
		diagnostic := findDiagnostic(diagnostics, "question_type_violation")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "questions.documentation_impact.type", diagnostic.Path)
	})

	t.Run("requires every canonical universal question when universal: true", func(t *testing.T) {
		incomplete := cloneExample(t)
		questions := incomplete["questions"].([]any)
		filtered := make([]any, 0, len(questions))
		for _, item := range questions {
			question := item.(map[string]any)
			if question["key"] == "api_contract_changed" {
				continue
			}
			filtered = append(filtered, item)
		}
		incomplete["questions"] = filtered

		diagnostics := diagnosticsOf(t, policy.CompilePolicy(incomplete))
		diagnostic := findDiagnostic(diagnostics, "universal_set_violation")
		require.NotNil(t, diagnostic)
		assert.Contains(t, diagnostic.Message, "api_contract_changed")
	})

	t.Run("rejects risk_class_overrides outside the closed risk set (matrix input data)", func(t *testing.T) {
		outside := cloneExample(t)
		outside["risk_class_overrides"] = map[string]any{"docs/security/**": "extreme"}

		diagnostics := diagnosticsOf(t, policy.CompilePolicy(outside))
		diagnostic := findDiagnostic(diagnostics, "risk_class_override_violation")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "risk_class_overrides.docs/security/**", diagnostic.Path)
	})
}

// ---------------------------------------------------------------------------
// compiler determinism (policyHash idempotence, ARCH §8.2)
// ---------------------------------------------------------------------------

func TestCompilerCarriesOptionalRefinements(t *testing.T) {
	// Optional policy fields survive compilation untouched and feed the
	// canonical hash: `extends` (the prior version pointer) and the
	// conservative-max risk_class_overrides matrix input data (D2/D11).
	refined := bootstrapPolicy(t)
	refined["extends"] = "policy-history/v16.yml"
	refined["risk_class_overrides"] = map[string]any{
		"docs/security/**":  "high",
		"docs/changelog.md": "low",
	}
	result := policy.CompilePolicy(refined)
	require.True(t, result.OK)
	require.NotNil(t, result.Compiled)
	require.NotNil(t, result.Compiled.Policy.Extends)
	assert.Equal(t, "policy-history/v16.yml", *result.Compiled.Policy.Extends)
	assert.Equal(t, map[string]schemas.RiskLevel{
		"docs/security/**":  schemas.RiskLevelHigh,
		"docs/changelog.md": schemas.RiskLevelLow,
	}, result.Compiled.Policy.RiskClassOverrides)

	hash, err := policy.PolicyHash(result.Compiled.Policy)
	require.NoError(t, err)
	assert.Equal(t, hash, result.Compiled.PolicyHash)
}

func TestCompilerDeterminism(t *testing.T) {
	t.Run("compiles the same input to the identical workflow — twice", func(t *testing.T) {
		first := policy.CompilePolicy(bootstrapPolicy(t), policy.WithTriggerTaxonomy(triggerMapExample()))
		second := policy.CompilePolicy(bootstrapPolicy(t), policy.WithTriggerTaxonomy(triggerMapExample()))
		require.True(t, first.OK)
		require.True(t, second.OK)
		assert.Equal(t, first.Compiled, second.Compiled)
		assert.Regexp(t, regexp.MustCompile(`^sha256:[0-9a-f]{64}$`), first.Compiled.PolicyHash)
	})

	t.Run("key insertion order does not change the policyHash (canonical serialization, sorted keys)", func(t *testing.T) {
		reordered := map[string]any{
			"routing": map[string]any{
				"review":                          0.72,
				"high_confidence":                 0.94,
				"calibration_floor":               0.94,
				"documentation_impact_auto_floor": 0.94,
			},
			"taxonomy_version":    1,
			"universal":           true,
			"unsafe_auto_updates": map[string]any{"enabled": false},
			"questions":           wireQuestions(t, policy.BootstrapPolicyV1.Questions),
			"created_at":          policy.BootstrapPolicyV1.CreatedAt,
			"reason":              policy.BootstrapPolicyV1.Reason,
			"created_by":          policy.BootstrapPolicyV1.CreatedBy,
			"policy_version":      policy.BootstrapPolicyV1.PolicyVersion,
			"schema":              policy.BootstrapPolicyV1.Schema,
		}
		straight := policy.CompilePolicy(bootstrapPolicy(t))
		flipped := policy.CompilePolicy(reordered)
		require.True(t, straight.OK)
		require.True(t, flipped.OK)
		assert.Equal(t, straight.Compiled.PolicyHash, flipped.Compiled.PolicyHash)
	})

	t.Run("hashes the parsed policy, not the raw input (all-or-nothing data)", func(t *testing.T) {
		result := policy.CompilePolicy(bootstrapPolicy(t))
		require.True(t, result.OK)
		require.NotNil(t, result.Compiled)
		hash, err := policy.PolicyHash(result.Compiled.Policy)
		require.NoError(t, err)
		assert.Equal(t, hash, result.Compiled.PolicyHash)
	})
}

func TestCompilerAdditiveCompositionWithMultipleAdditives(t *testing.T) {
	result := policy.CompilePolicy(bootstrapPolicy(t), policy.WithAdditivePolicies(
		additivePolicy(t, []policy.Question{
			{Key: "payment_flow_changed", Type: policy.QuestionTypeBoolean, Prompt: "Payment flow?"},
		}),
		additivePolicy(t, []policy.Question{
			{Key: "idempotency_changed", Type: policy.QuestionTypeBoolean, Prompt: "Idempotency?"},
		}),
	))
	require.True(t, result.OK)
	require.NotNil(t, result.Compiled)
	expected := append(append([]string{}, schemas.UniversalQuestions...),
		"payment_flow_changed", "idempotency_changed")
	assert.Equal(t, expected, questionKeys(result.Compiled.Questions))
}

func TestValidateTriggerVocabularyOptions(t *testing.T) {
	t.Run("honors an explicit taxonomy version and known-values list", func(t *testing.T) {
		validation := policy.ValidateTriggerVocabulary(
			[]string{"a", "z"},
			policy.ValidateTriggerVocabularyOptions{
				Direction:       policy.TriggerDirectionIgnore,
				TaxonomyVersion: intPointer(2),
				KnownValues:     []string{"a", "b"},
			})
		assert.Equal(t, []string{"a"}, validation.Mapped)
		assert.Equal(t, []string{"z"}, validation.UnknownValues)
		require.Len(t, validation.Diagnostics, 1)
		diagnostic := validation.Diagnostics[0]
		assert.Equal(t, "ignore", diagnostic.Path)
		assert.Equal(t, "docsync.trigger-map/v2", diagnostic.ExpectedSchemaVersion)
		assert.Contains(t, diagnostic.Message, "expected trigger-taxonomy version 2")
		assert.Contains(t, diagnostic.Message, "supported values: a, b")
	})

	t.Run("deduplicates unknown values, one diagnostic per value", func(t *testing.T) {
		validation := policy.ValidateTriggerVocabulary(
			[]string{"x", "x", "public_behavior"},
			policy.ValidateTriggerVocabularyOptions{Direction: policy.TriggerDirectionTriggers})
		assert.Equal(t, []string{"public_behavior"}, validation.Mapped)
		assert.Equal(t, []string{"x"}, validation.UnknownValues)
		require.Len(t, validation.Diagnostics, 1)
	})
}

func intPointer(value int) *int { return &value }

func TestCompilerReportsRootPathDiagnosticsForNonObjectInput(t *testing.T) {
	// A non-object policy aborts the base parse: the single diagnostic is
	// namespaced `(root)` (TS: toDiagnostic's path === '' branch).
	for _, input := range []any{"not-a-policy", nil, []any{1, 2}, true} {
		diagnostics := diagnosticsOf(t, policy.CompilePolicy(input))
		require.NotEmpty(t, diagnostics)
		assert.Equal(t, "(root)", diagnostics[0].Path)
	}
}
