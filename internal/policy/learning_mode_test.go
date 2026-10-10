package policy_test

// Ported from packages/policy/test/learning-mode.test.ts — learning.mode
// governance (P10-T22; V-P10-15): default propose; canary/auto need gate
// evidence; gate bypass via config is rejected.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/policy"
)

// lmModePtr returns a pointer to a LearningMode (the optional org default).
func lmModePtr(mode string) *policy.LearningMode {
	resolved := policy.LearningMode(mode)
	return &resolved
}

// lmBoolPtr returns a pointer to a bool (the optional gate evidence).
func lmBoolPtr(value bool) *bool {
	return &value
}

// lmExpectValidationError asserts a fail-closed typed validation refusal.
func lmExpectValidationError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var validationErr *policy.LearningModeValidationError
	require.True(t, errors.As(err, &validationErr),
		"expected a *policy.LearningModeValidationError, got %T: %v", err, err)
}

func TestV_P10_15_LearningModeDefaultsAndEscalations(t *testing.T) {
	t.Run("V-P10-15: defaults to propose; canary/auto without gate evidence is a validation error; with evidence it resolves", func(t *testing.T) {
		// shipped default
		shipped, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{},
		})
		require.NoError(t, err)
		require.Equal(t, policy.LearningModePropose, shipped.Mode)
		require.Equal(t, policy.LearningModeSourceShippedDefault, shipped.Source)
		require.Empty(t, shipped.Escalations)

		// canary without gate evidence — validation error (V-P10-15)
		_, canaryErr := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": map[string]any{"mode": "canary"}},
		})
		lmExpectValidationError(t, canaryErr)

		// with gate evidence — resolves, with the bypass-proofing note
		canary, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig:                 map[string]any{"learning": map[string]any{"mode": "canary"}},
			LatestPromotionGatesPassed: lmBoolPtr(true),
		})
		require.NoError(t, err)
		require.Equal(t, policy.LearningModeCanary, canary.Mode)
		require.Equal(t, policy.LearningModeSourceRepoConfig, canary.Source)
		require.NotEmpty(t, canary.Escalations)
		require.Contains(t, canary.Escalations[0], "cannot bypass")

		// auto likewise
		auto, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig:                 map[string]any{"learning": map[string]any{"mode": "auto"}},
			LatestPromotionGatesPassed: lmBoolPtr(true),
		})
		require.NoError(t, err)
		require.Equal(t, policy.LearningModeAuto, auto.Mode)

		// off disables proposal processing
		off, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": map[string]any{"mode": "off"}},
		})
		require.NoError(t, err)
		require.Equal(t, policy.LearningModeOff, off.Mode)
		require.Empty(t, off.Escalations)
	})

	t.Run("precedence: explicit repo config > org default > shipped default; org canary/auto also need evidence", func(t *testing.T) {
		repoWins, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": map[string]any{"mode": "propose"}},
			OrgDefault: lmModePtr("off"),
		})
		require.NoError(t, err)
		require.Equal(t, policy.LearningModePropose, repoWins.Mode)
		require.Equal(t, policy.LearningModeSourceRepoConfig, repoWins.Source)

		orgLevel, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{},
			OrgDefault: lmModePtr("off"),
		})
		require.NoError(t, err)
		require.Equal(t, policy.LearningModeOff, orgLevel.Mode)
		require.Equal(t, policy.LearningModeSourceOrgDefault, orgLevel.Source)

		_, orgAutoErr := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig:                 map[string]any{},
			OrgDefault:                 lmModePtr("auto"),
			LatestPromotionGatesPassed: lmBoolPtr(false),
		})
		lmExpectValidationError(t, orgAutoErr)

		orgAuto, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig:                 map[string]any{},
			OrgDefault:                 lmModePtr("auto"),
			LatestPromotionGatesPassed: lmBoolPtr(true),
		})
		require.NoError(t, err)
		require.Equal(t, policy.LearningModeSourceOrgDefault, orgAuto.Source)
		require.Equal(t, policy.LearningModeAuto, orgAuto.Mode)
		require.NotEmpty(t, orgAuto.Escalations)
		require.Contains(t, orgAuto.Escalations[0], "gates re-run inside the pipeline")
	})

	t.Run("invalid config is a typed validation error (fail closed)", func(t *testing.T) {
		_, yoloErr := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": map[string]any{"mode": "yolo"}},
		})
		lmExpectValidationError(t, yoloErr)

		_, numberErr := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": map[string]any{"mode": 42}},
		})
		lmExpectValidationError(t, numberErr)
	})

	t.Run("strict config: unknown keys and a non-object learning block are rejected", func(t *testing.T) {
		_, unknownRootKeyErr := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": map[string]any{"mode": "off"}, "typo": true},
		})
		lmExpectValidationError(t, unknownRootKeyErr)

		_, unknownLearningKeyErr := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": map[string]any{"mode": "off", "extra": 1}},
		})
		lmExpectValidationError(t, unknownLearningKeyErr)

		_, nonObjectErr := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": "auto"},
		})
		lmExpectValidationError(t, nonObjectErr)

		// A nil repo config is not a valid strict object either (fail closed).
		_, nilConfigErr := policy.ResolveLearningMode(policy.LearningModeResolutionInput{})
		lmExpectValidationError(t, nilConfigErr)
	})

	t.Run("LEARNING_MODE_DEFAULT is propose (never auto — Phase 12 enables auto)", func(t *testing.T) {
		require.Equal(t, policy.LearningModePropose, policy.LearningModeDefault)
		require.Equal(t, "propose", string(policy.LearningModeDefault))
		require.Equal(t, []policy.LearningMode{
			policy.LearningModeOff, policy.LearningModePropose,
			policy.LearningModeCanary, policy.LearningModeAuto,
		}, policy.LearningModeValues)
		require.True(t, policy.IsValidLearningMode("canary"))
		require.False(t, policy.IsValidLearningMode("yolo"))
		require.True(t, policy.IsLearningModeEscalation(policy.LearningModeAuto))
		require.True(t, policy.IsLearningModeEscalation(policy.LearningModeCanary))
		require.False(t, policy.IsLearningModeEscalation(policy.LearningModePropose))
		require.False(t, policy.IsLearningModeEscalation(policy.LearningModeOff))
	})
}

func TestLearningModeConfigTypeCoverage(t *testing.T) {
	cases := []struct {
		name       string
		repoConfig any
	}{
		{"null learning block", map[string]any{"learning": nil}},
		{"array learning block", map[string]any{"learning": []any{"off"}}},
		{"boolean learning block", map[string]any{"learning": true}},
		{"scalar learning block", map[string]any{"learning": 7}},
		{"non-object repo config", "off"},
		{"nil repo config", nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name+" is a typed validation error", func(t *testing.T) {
			_, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
				RepoConfig: testCase.repoConfig,
			})
			lmExpectValidationError(t, err)
			require.Contains(t, err.Error(), "learning config invalid")
		})
	}

	t.Run("an explicitly empty learning block resolves the shipped default", func(t *testing.T) {
		resolution, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
			RepoConfig: map[string]any{"learning": map[string]any{}},
		})
		require.NoError(t, err)
		require.Equal(t, policy.LearningModeDefault, resolution.Mode)
		require.Equal(t, policy.LearningModeSourceShippedDefault, resolution.Source)
	})
}
