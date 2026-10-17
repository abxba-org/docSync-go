package policy_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/policy"
)

func covBootstrapQuestions(t *testing.T, policy map[string]any) []any {
	t.Helper()
	questions, ok := policy["questions"].([]any)
	require.True(t, ok, "the bootstrap wire carries a questions array")
	return questions
}

func TestCOV_POLICY_025_PolicySchemaFieldHelpersRejectMalformedValues(t *testing.T) {
	t.Run("a non-number scoring bound is a type failure naming the received kind", func(t *testing.T) {
		wire := bootstrapPolicy(t)
		questions := covBootstrapQuestions(t, wire)
		score := questions[2].(map[string]any)
		score["scoring"] = map[string]any{"min": "zero", "max": 50}
		_, err := policy.ParsePolicy(wire)
		require.Error(t, err)
		require.Contains(t, err.Error(), "questions.2.scoring.min: Expected number, received string")
	})

	t.Run("an infinite scoring bound is a finite-number failure", func(t *testing.T) {
		wire := bootstrapPolicy(t)
		questions := covBootstrapQuestions(t, wire)
		score := questions[2].(map[string]any)
		score["scoring"] = map[string]any{"min": math.Inf(1), "max": 50}
		_, err := policy.ParsePolicy(wire)
		require.Error(t, err)
		require.Contains(t, err.Error(), "questions.2.scoring.min: Number must be finite")
	})

	t.Run("a NaN scoring bound is a type failure naming nan (float64 and float32)", func(t *testing.T) {
		wire := bootstrapPolicy(t)
		questions := covBootstrapQuestions(t, wire)
		score := questions[2].(map[string]any)
		score["scoring"] = map[string]any{"min": math.NaN(), "max": 50}
		_, err := policy.ParsePolicy(wire)
		require.Error(t, err)
		require.Contains(t, err.Error(), "questions.2.scoring.min: Expected number, received nan")

		wire32 := bootstrapPolicy(t)
		questions32 := covBootstrapQuestions(t, wire32)
		score32 := questions32[2].(map[string]any)
		score32["scoring"] = map[string]any{"min": float32(math.NaN()), "max": 50}
		_, err = policy.ParsePolicy(wire32)
		require.Error(t, err)
		require.Contains(t, err.Error(), "questions.2.scoring.min: Expected number, received nan")
	})

	t.Run("an absent question type is Required; a non-string type names the enum", func(t *testing.T) {
		wire := bootstrapPolicy(t)
		questions := covBootstrapQuestions(t, wire)
		boolean := questions[0].(map[string]any)
		delete(boolean, "type")
		_, err := policy.ParsePolicy(wire)
		require.Error(t, err)
		require.Contains(t, err.Error(), "questions.0.type: Required")

		wireTyped := bootstrapPolicy(t)
		questionsTyped := covBootstrapQuestions(t, wireTyped)
		booleanTyped := questionsTyped[0].(map[string]any)
		booleanTyped["type"] = 42
		_, err = policy.ParsePolicy(wireTyped)
		require.Error(t, err)
		require.Contains(t, err.Error(), "questions.0.type: Expected 'boolean' | 'choice' | 'score', received number")
	})

	t.Run("a non-string policy field names the received object kind", func(t *testing.T) {
		wire := bootstrapPolicy(t)
		wire["created_by"] = map[string]any{"model": "claude-sonnet"}
		_, err := policy.ParsePolicy(wire)
		require.Error(t, err)
		require.Contains(t, err.Error(), "created_by: Expected string, received object")
	})

	t.Run("a non-object policy document is refused with the unknown received kind", func(t *testing.T) {
		_, err := policy.ParsePolicy(struct{ Name string }{Name: "not-a-policy"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "Expected object, received unknown")
	})
}

func TestCOV_POLICY_026_CompilerAdditiveDiagnosticsCarryTheInputPathPrefix(t *testing.T) {
	t.Run("a root-level additive issue is namespaced under the additive input path", func(t *testing.T) {
		additive := bootstrapPolicy(t)
		additive["policy_version"] = 99
		additive["bogus_root_key"] = true
		result := policy.CompilePolicy(bootstrapPolicy(t), policy.WithAdditivePolicies(additive))
		require.False(t, result.OK)
		require.Nil(t, result.Compiled)
		require.NotEmpty(t, result.Diagnostics)
		require.Equal(t, "additivePolicies.0.(root)", result.Diagnostics[0].Path)
		require.Contains(t, result.Diagnostics[0].Message, `Unrecognized key(s) in object: 'bogus_root_key'`)
	})

	t.Run("a field-level additive issue is namespaced under the additive field path", func(t *testing.T) {
		additive := bootstrapPolicy(t)
		additive["policy_version"] = 99
		additive["created_by"] = ""
		result := policy.CompilePolicy(bootstrapPolicy(t), policy.WithAdditivePolicies(additive))
		require.False(t, result.OK)
		require.Nil(t, result.Compiled)
		require.NotEmpty(t, result.Diagnostics)
		require.Equal(t, "additivePolicies.0.created_by", result.Diagnostics[0].Path)
		require.Contains(t, result.Diagnostics[0].Message, "String must contain at least 1 character(s)")
	})
}

func TestCOV_POLICY_027_TwoProportionZGuards(t *testing.T) {
	require.True(t, math.IsInf(policy.TwoProportionZ(1, 0, 1, 1), -1),
		"a zero candidate total fails closed with -Inf")
	require.True(t, math.IsInf(policy.TwoProportionZ(1, 1, 1, 0), -1),
		"a zero baseline total fails closed with -Inf")
	noWins := policy.TwoProportionZ(0, 10, 0, 10)
	require.True(t, math.IsInf(noWins, -1),
		"a zero pooled proportion has no standard error and fails closed with -Inf")
	allWins := policy.TwoProportionZ(10, 10, 10, 10)
	require.True(t, math.IsInf(allWins, -1),
		"a saturated pooled proportion fails closed with -Inf")
	better := policy.TwoProportionZ(9, 10, 5, 10)
	require.Greater(t, better, 0.0)
}

func TestCOV_POLICY_028_P95LatencyMsNearestRank(t *testing.T) {
	p95, err := policy.P95LatencyMs([]float64{10, 20, 30, 40})
	require.NoError(t, err)
	require.Equal(t, 40.0, p95, "n=4 → rank 4 (the maximum)")
	p95Twenty, err := policy.P95LatencyMs(covLatencySamples(20))
	require.NoError(t, err)
	require.Equal(t, 19.0, p95Twenty, "n=20 → rank 19 (the second-largest)")
	_, err = policy.P95LatencyMs(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires at least one sample")
	_, err = policy.P95LatencyMs([]float64{10, math.NaN()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be finite numbers")
}

func covLatencySamples(count int) []float64 {
	samples := make([]float64, 0, count)
	for index := 1; index <= count; index++ {
		samples = append(samples, float64(index))
	}
	return samples
}

func TestCOV_POLICY_029_CompilePolicyReportsBasePolicyParseDiagnostics(t *testing.T) {
	result := policy.CompilePolicy(map[string]any{"schema": "docsync.jev-policy/v1"})
	require.False(t, result.OK)
	require.Nil(t, result.Compiled, "a failed compilation must never carry a workflow (ARCH §8.2)")
	require.NotEmpty(t, result.Diagnostics)
	require.Equal(t, "policy_version", result.Diagnostics[0].Path)
	require.Equal(t, policy.PolicyDiagnosticKindSchemaViolation, result.Diagnostics[0].Kind)
	require.Contains(t, result.Diagnostics[0].Message, "Required")
	paths := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		paths = append(paths, diagnostic.Path)
	}
	require.Contains(t, paths, "questions")
	require.Contains(t, paths, "routing")
	require.Contains(t, paths, "created_by")
}

func TestCOV_POLICY_030_TwoProportionZSaturationAndBoundaryCells(t *testing.T) {
	saturated := policy.TwoProportionZ(10, 10, 4, 10)
	require.Greater(t, saturated, 1.0)
	symmetric := policy.TwoProportionZ(5, 10, 5, 10)
	require.Equal(t, 0.0, symmetric)
	negative := policy.TwoProportionZ(4, 10, 10, 10)
	require.Less(t, negative, 0.0)
}
