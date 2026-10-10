package schemas_test

// Ported from packages/schemas/test/safety-incident.test.ts and
// safety-incident-odr50.test.ts — docsync.safety-incident/v1 (P6-T12) and the
// OD-R50 additive rule-union extension (P9-T19; PLAN §10.3 six-class
// taxonomy; ARCH §11.4).

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustJSONTyped[T any](t *testing.T, v T) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

func TestSafetyIncidentExampleValidates(t *testing.T) {
	example := schemas.SafetyIncidentExample()
	require.Empty(t, example.Validate())
	_, err := schemas.ParseSafetyIncident(mustJSONTyped(t, example))
	require.NoError(t, err)
}

func TestSafetyIncidentRoundTripsByteStable(t *testing.T) {
	first := mustJSONTyped(t, schemas.SafetyIncidentExample())

	var decoded schemas.SafetyIncident
	require.NoError(t, schemas.StrictDecode(first, &decoded))
	second := mustJSONTyped(t, decoded)

	assert.Equal(t, string(first), string(second))
	require.Empty(t, decoded.Validate())
}

func TestSafetyIncidentRejectsUnknownTopLevelKeys(t *testing.T) {
	base := mustJSONTyped(t, schemas.SafetyIncidentExample())
	mutated := mutateJSON(t, base, func(tree any) {
		tree.(map[string]any)["extra_field"] = true
	})
	_, err := schemas.ParseSafetyIncident(mutated)
	require.Error(t, err)
}

func TestSafetyIncidentRejectsBadFieldValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*schemas.SafetyIncident)
	}{
		{"incident_id", func(s *schemas.SafetyIncident) { s.IncidentID = "" }},
		{"at", func(s *schemas.SafetyIncident) { s.At = "not-a-timestamp" }},
		{"run_id", func(s *schemas.SafetyIncident) { s.RunID = "" }},
		{"offending_path", func(s *schemas.SafetyIncident) { s.OffendingPath = "" }},
		{"detail", func(s *schemas.SafetyIncident) { s.Detail = "" }},
		{"source_pr_number", func(s *schemas.SafetyIncident) { s.SourcePRNumber = new(0) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			incident := schemas.SafetyIncidentExample()
			tc.mutate(&incident)
			require.NotEmpty(t, incident.Validate(), "%s must be rejected", tc.name)

			_, err := schemas.ParseSafetyIncident(mustJSONTyped(t, incident))
			require.Error(t, err, "%s must be rejected at parse", tc.name)
		})
	}
}

func TestSafetyIncidentOptionalFieldsNeverGenerated(t *testing.T) {
	minimal := schemas.SafetyIncidentExample()
	minimal.DocPath = nil
	minimal.SourcePRNumber = nil

	require.Empty(t, minimal.Validate())
	_, err := schemas.ParseSafetyIncident(mustJSONTyped(t, minimal))
	require.NoError(t, err)
}

func TestSafetyIncidentRuleValuesIsTheFrozenEightRuleTaxonomy(t *testing.T) {
	assert.Equal(t, []schemas.SafetyIncidentRule{
		"out_of_allowlist_path",
		"protected_path_patch",
		"machine_mode_transition",
		"manifest_or_policy_edit",
		"default_branch_ref",
		"provenance_missing",
		"model_influence_on_authority",
		"injection_contained",
	}, schemas.SafetyIncidentRuleValues)
	assert.Len(t, schemas.SafetyIncidentRuleValues, 8)

	// The union stays CLOSED.
	rejected := schemas.SafetyIncidentExample()
	rejected.Rule = "other_rule"
	require.NotEmpty(t, rejected.Validate())
}

func TestSafetyIncidentEveryRuleValueParses(t *testing.T) {
	for _, rule := range schemas.SafetyIncidentRuleValues {
		incident := schemas.SafetyIncidentExample()
		incident.Rule = rule
		require.Empty(t, incident.Validate(), "rule %s must validate", rule)

		_, err := schemas.ParseSafetyIncident(mustJSONTyped(t, incident))
		require.NoError(t, err, "rule %s must parse end to end", rule)
	}
}

func TestSafetyIncidentODR50PlanClassMapping(t *testing.T) {
	planClassToRule := map[string]schemas.SafetyIncidentRule{
		"lock_violation":               "protected_path_patch",
		"out_of_allowlist_write":       "out_of_allowlist_path",
		"policy_path_violation":        "manifest_or_policy_edit",
		"provenance_missing":           "provenance_missing",
		"model_influence_on_authority": "model_influence_on_authority",
		"injection_contained":          "injection_contained",
	}
	rules := map[schemas.SafetyIncidentRule]bool{}
	for _, rule := range schemas.SafetyIncidentRuleValues {
		rules[rule] = true
	}
	for planClass, rule := range planClassToRule {
		assert.True(t, rules[rule], "%s → %s", planClass, rule)
		assert.True(t, schemas.IsValidSafetyIncidentRule(string(rule)))
	}
}

func TestSafetyIncidentODR50NewRulesValidateAsEvidence(t *testing.T) {
	for _, rule := range []schemas.SafetyIncidentRule{
		"model_influence_on_authority",
		"injection_contained",
	} {
		assert.True(t, schemas.IsValidSafetyIncidentRule(string(rule)))
		incident := schemas.SafetyIncidentExample()
		incident.Rule = rule
		require.Empty(t, incident.Validate())
	}
}

func TestSafetyIncidentODR50ExampleUnchanged(t *testing.T) {
	assert.Equal(t, schemas.SafetyIncidentRule("out_of_allowlist_path"), schemas.SafetyIncidentExample().Rule)
}
