package openrouter_test

// models_test.go — Go port of packages/openrouter/test/models.test.ts +
// models-constraint.test.ts (P2-T03 / P4-T16; D9, D13; ARCH §10.3). Black-box
// (package openrouter_test), testify. The TS `testModels`/`testModelsDivergences`
// helpers (helpers.ts) become JSON literals parsed through openrouter.ParseModelsConfig.

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
)

// testModelsJSON is the helpers.ts TEST_MODELS_INPUT fixture verbatim: an
// extended docsync.models/v1 document where decision-engine carries params +
// provider routing, and patch-writer carries params.
const testModelsJSON = `{
  "schema": "docsync.models/v1",
  "version": 1,
  "roles": {
    "decision-engine": {
      "model": "z-ai/glm-4.7",
      "fallbacks": ["openai/gpt-4.1-mini"],
      "params": { "temperature": 0, "max_tokens": 2000 },
      "routing": { "provider": { "order": ["z-ai", "openai"], "allow_fallbacks": true } }
    },
    "judge": { "model": "openai/gpt-4.1" },
    "writer": { "model": "anthropic/claude-sonnet-4.5" },
    "patch-writer": { "model": "anthropic/claude-sonnet-4.5", "params": { "temperature": 0 } }
  }
}`

// judgeConstraintDoc is models-constraint.test.ts's JUDGE_CONSTRAINT_DOC.
const judgeConstraintDoc = `{
  "schema": "docsync.models/v1",
  "version": 1,
  "roles": {
    "judge": {
      "model": "openai/gpt-4.1",
      "constraint": "must_not_equal_patch-writer_instance"
    },
    "patch-writer": { "model": "anthropic/claude-sonnet-4.5" }
  }
}`

func parseModels(t *testing.T, raw string) *openrouter.ModelsConfigParse {
	t.Helper()
	parsed, err := openrouter.ParseModelsConfig([]byte(raw))
	require.NoError(t, err)
	require.NotNil(t, parsed)
	return parsed
}

func testModelsConfig(t *testing.T) *openrouter.ExtendedModelsConfig {
	t.Helper()
	parsed := parseModels(t, testModelsJSON)
	return &parsed.Models
}

func TestP2T03_ModelsBindings_SupersetValidationAndRoleResolution(t *testing.T) {
	t.Run("validates an extended docsync.models/v1 document and resolves the binding shape", func(t *testing.T) {
		models := testModelsConfig(t)

		binding, err := openrouter.ResolveRoleBinding(models, "decision-engine")
		require.NoError(t, err)
		assert.Equal(t, "z-ai/glm-4.7", binding.Primary)
		assert.Equal(t, []string{"openai/gpt-4.1-mini"}, binding.Fallbacks)
		require.NotNil(t, binding.Params)
		require.NotNil(t, binding.Params.Temperature)
		assert.Equal(t, 0.0, *binding.Params.Temperature)
		require.NotNil(t, binding.Params.MaxTokens)
		assert.Equal(t, 2000, *binding.Params.MaxTokens)
		assert.Equal(t, "", binding.Params.StructuredOutput)
		require.NotNil(t, binding.ProviderRouting)
		assert.Equal(t, []string{"z-ai", "openai"}, binding.ProviderRouting.Order)
		assert.True(t, binding.ProviderRouting.AllowFallbacks)
		assert.Nil(t, binding.Constraint)

		judge, err := openrouter.ResolveRoleBinding(models, "judge")
		require.NoError(t, err)
		assert.Equal(t, "openai/gpt-4.1", judge.Primary)
		assert.Equal(t, []string{}, judge.Fallbacks)
		assert.Nil(t, judge.Params)
		assert.Nil(t, judge.ProviderRouting)
		assert.Nil(t, judge.Constraint)
	})

	t.Run("unknown role -> typed RoleBindingError naming the bound roles", func(t *testing.T) {
		_, err := openrouter.ResolveRoleBinding(testModelsConfig(t), "nonsense-role")
		require.Error(t, err)
		var rbe *openrouter.RoleBindingError
		require.ErrorAs(t, err, &rbe)
		assert.Equal(t, "nonsense-role", rbe.Role)
		assert.Contains(t, err.Error(), "bound roles")
		assert.Contains(t, err.Error(), "decision-engine")
	})

	t.Run("rejects documents that are not valid docsync.models/v1 at the base schema", func(t *testing.T) {
		_, err := openrouter.ParseModelsConfig([]byte(`{"schema":"docsync.models/v1","version":0,"roles":{}}`))
		require.Error(t, err)
		var mce *openrouter.ModelsConfigError
		require.ErrorAs(t, err, &mce)

		_, err = openrouter.ParseModelsConfig([]byte(`"nope"`))
		require.Error(t, err)
		require.ErrorAs(t, err, &mce)

		_, err = openrouter.ParseModelsConfig([]byte(`{"schema":"docsync.models/v2","version":1,"roles":{}}`))
		require.Error(t, err)
		require.ErrorAs(t, err, &mce)
		assert.Contains(t, err.Error(), "docsync.models/v1")
	})

	t.Run("superset refinement: base-valid params/routing violations are rejected fail-closed", func(t *testing.T) {
		_, err := openrouter.ParseModelsConfig([]byte(
			`{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","params":{"temperature":"hot"}}}}`,
		))
		require.Error(t, err)
		var mce *openrouter.ModelsConfigError
		require.ErrorAs(t, err, &mce)
		assert.Contains(t, err.Error(), "extended binding shape")
	})

	t.Run("strict unknown keys stay rejected through the refinement", func(t *testing.T) {
		_, err := openrouter.ParseModelsConfig([]byte(
			`{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","bogus":true}}}`,
		))
		require.Error(t, err)
		var mce *openrouter.ModelsConfigError
		require.ErrorAs(t, err, &mce)
	})

	t.Run("missingRequiredRoles reports Phase-0 REQUIRED_MODEL_ROLES gaps", func(t *testing.T) {
		assert.Equal(t, []string{}, openrouter.MissingRequiredRoles(testModelsConfig(t)))

		partial := parseModels(t, `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m"}}}`)
		missing := openrouter.MissingRequiredRoles(&partial.Models)
		sort.Strings(missing)
		assert.Equal(t, []string{"judge", "writer"}, missing)
	})

	t.Run("divergence surfacing: extended fields accepted with phase0SchemaCompliant=false + named divergences", func(t *testing.T) {
		parsed := parseModels(t, testModelsJSON)
		assert.False(t, parsed.Phase0SchemaCompliant)
		assert.Equal(t, []string{
			"roles.decision-engine.params — accepted by the gateway extended binding (ARCH §10.3), unknown to Phase-0 MODELS_SCHEMA (strict)",
			"roles.decision-engine.routing — accepted by the gateway extended binding (ARCH §10.3), unknown to Phase-0 MODELS_SCHEMA (strict)",
			"roles.patch-writer.params — accepted by the gateway extended binding (ARCH §10.3), unknown to Phase-0 MODELS_SCHEMA (strict)",
		}, parsed.Divergences)

		plain := parseModels(t, `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m"}}}`)
		assert.True(t, plain.Phase0SchemaCompliant)
		assert.Equal(t, []string{}, plain.Divergences)
	})
}

func TestP4T16_RoleBindingConstraint_ClosedVocabulary(t *testing.T) {
	t.Run("constraint vocabulary is exactly the one instance constraint", func(t *testing.T) {
		assert.Equal(t, []openrouter.RoleBindingConstraint{
			openrouter.RoleBindingConstraintMustNotEqualPatchWriter,
		}, openrouter.JudgeConstraintValues)
	})

	t.Run("models.yml with judge constraint parses; divergence surfaced; Phase-0 compliance false", func(t *testing.T) {
		parsed := parseModels(t, judgeConstraintDoc)
		assert.False(t, parsed.Phase0SchemaCompliant)
		assert.Equal(t, []string{
			"roles.judge.constraint — accepted by the gateway extended binding (D13/ARCH §10.3), unknown to Phase-0 MODELS_SCHEMA (strict)",
		}, parsed.Divergences)
	})

	t.Run("resolveRoleBinding surfaces the constraint verbatim", func(t *testing.T) {
		parsed := parseModels(t, judgeConstraintDoc)
		binding, err := openrouter.ResolveRoleBinding(&parsed.Models, "judge")
		require.NoError(t, err)
		assert.Equal(t, "openai/gpt-4.1", binding.Primary)
		require.NotNil(t, binding.Constraint)
		assert.Equal(t, openrouter.RoleBindingConstraintMustNotEqualPatchWriter, string(*binding.Constraint))
	})

	t.Run("no-constraint binding resolves constraint undefined (decision-engine-style doc)", func(t *testing.T) {
		parsed := parseModels(t, `{"schema":"docsync.models/v1","version":1,"roles":{"judge":{"model":"openai/gpt-4.1"}}}`)
		binding, err := openrouter.ResolveRoleBinding(&parsed.Models, "judge")
		require.NoError(t, err)
		assert.Nil(t, binding.Constraint)
	})

	t.Run("unknown constraint value fails the parse fail-closed with the field named", func(t *testing.T) {
		_, err := openrouter.ParseModelsConfig([]byte(
			`{"schema":"docsync.models/v1","version":1,"roles":{"judge":{"model":"openai/gpt-4.1","constraint":"must_equal_writer"}}}`,
		))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "roles.judge.constraint")
		assert.Contains(t, err.Error(), "must_not_equal_patch-writer_instance")
	})

	t.Run("empty-string constraint fails the parse fail-closed too", func(t *testing.T) {
		_, err := openrouter.ParseModelsConfig([]byte(
			`{"schema":"docsync.models/v1","version":1,"roles":{"judge":{"model":"openai/gpt-4.1","constraint":""}}}`,
		))
		require.Error(t, err)
	})

	t.Run("constraint on a non-judge role is equally parseable data (enforcement is the verifier's)", func(t *testing.T) {
		parsed := parseModels(t,
			`{"schema":"docsync.models/v1","version":1,"roles":{"writer":{"model":"anthropic/claude-sonnet-4.5","constraint":"must_not_equal_patch-writer_instance"}}}`,
		)
		binding, err := openrouter.ResolveRoleBinding(&parsed.Models, "writer")
		require.NoError(t, err)
		require.NotNil(t, binding.Constraint)
		assert.Equal(t, openrouter.RoleBindingConstraintMustNotEqualPatchWriter, string(*binding.Constraint))
	})
}

func TestP2T03_SupersetRefinementRejectsEachExtendedViolation(t *testing.T) {
	// Each ARCH §10.3 extended field is a strict superset over the Phase-0 base:
	// a malformed extended value is REJECTED fail-closed (never silently
	// accepted), and the rejection names the "extended binding shape".
	cases := []struct {
		name    string
		raw     string
		expects string // a substring the failure message must carry (field path or shared prefix)
	}{
		{"params.max_tokens must be positive", `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","params":{"max_tokens":0}}}}`, "roles.decision-engine.params.max_tokens"},
		{"params.temperature within [0,2]", `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","params":{"temperature":3}}}}`, "roles.decision-engine.params.temperature"},
		{"params.structured_output enum", `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","params":{"structured_output":"freeform"}}}}`, "roles.decision-engine.params.structured_output"},
		{"routing.provider.order min one", `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","routing":{"provider":{"order":[]}}}}}`, "roles.decision-engine.routing.provider.order"},
		{"routing.provider.order non-empty entries", `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","routing":{"provider":{"order":[""]}}}}}`, "roles.decision-engine.routing.provider.order"},
		{"binding model required", `{"schema":"docsync.models/v1","version":1,"roles":{"judge":{"model":""}}}`, "roles.judge.model"},
		{"fallback entries required", `{"schema":"docsync.models/v1","version":1,"roles":{"judge":{"model":"m","fallbacks":[""]}}}`, "roles.judge.fallbacks.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := openrouter.ParseModelsConfig([]byte(tc.raw))
			require.Error(t, err)
			var mce *openrouter.ModelsConfigError
			require.ErrorAs(t, err, &mce)
			assert.Contains(t, err.Error(), "extended binding shape")
			assert.Contains(t, err.Error(), tc.expects)
		})
	}
}

func TestP2T03_StructuredOutputOmittedCarriesThroughResolvedBinding(t *testing.T) {
	// OD-R41: structured_output omitted in models.yml stays omitted (empty) in
	// the resolved binding — the consumer selects the mode ('native') itself.
	parsed := parseModels(t, `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","params":{"temperature":0,"max_tokens":10}}}}`)
	binding, err := openrouter.ResolveRoleBinding(&parsed.Models, "decision-engine")
	require.NoError(t, err)
	require.NotNil(t, binding.Params)
	assert.Equal(t, "", binding.Params.StructuredOutput)

	// An explicit mode surfaces verbatim.
	parsed2 := parseModels(t, `{"schema":"docsync.models/v1","version":1,"roles":{"decision-engine":{"model":"m","params":{"structured_output":"prompt"}}}}`)
	binding2, err := openrouter.ResolveRoleBinding(&parsed2.Models, "decision-engine")
	require.NoError(t, err)
	require.NotNil(t, binding2.Params)
	assert.Equal(t, "prompt", binding2.Params.StructuredOutput)
}
