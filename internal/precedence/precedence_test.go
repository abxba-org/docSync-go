package precedence_test

// Ported from packages/core/test/precedence.test.ts (P0-T07, V-P0-6 path) plus
// the resolveEffectiveConfig assertions of
// packages/core/test/org-floors-p12-t13.test.ts (P12-T13: org-policy floors
// compose, learning.defaultMode flows, org floor violations throw) and the
// F5-3 block of packages/core/test/validator-org-advisories-p12-t16.test.ts
// (the org learning default never changes the posture flags).
//
// Layer order and binding rules: built-in defaults < defaults block < entry <
// org floors (dominant — violation errors, never clamps) < invocation flags
// (tighten-only, applied last). D2 stays embedded via resolveEffectiveMode.

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/modes"
	"github.com/DUDE616/docSync-go/internal/precedence"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var noFloors []modes.OrgFloor

var securityFloor = modes.OrgFloor{
	Kind: schemas.FloorKindModeCeiling, Pattern: "docs/security/**", MaxMode: schemas.DocModeSuggest,
}

func modePtr(mode schemas.DocMode) *schemas.DocMode { return &mode }

func boolPtr(value bool) *bool { return &value }

func ownerPtr(owner modes.OwnershipCategory) *modes.OwnershipCategory { return &owner }

func layers(applied []precedence.AppliedSource) []string {
	out := make([]string, 0, len(applied))
	for _, source := range applied {
		out = append(out, source.Layer+":"+source.Field)
	}
	return out
}

func TestResolveEffectiveConfigEntryModeOverridesDefaults(t *testing.T) {
	config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
		DocPath:      "docs/a.md",
		EntryMode:    modePtr(schemas.DocModeAuto),
		DefaultsMode: modePtr(schemas.DocModeSuggest),
		Floors:       noFloors,
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeAuto, config.Mode)
	assert.True(t, config.PatchCandidate)
}

func TestResolveEffectiveConfigBuiltInDefaultIsSuggest(t *testing.T) {
	config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
		DocPath: "docs/a.md",
		Floors:  noFloors,
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeSuggest, config.Mode)
	assert.True(t, config.PatchCandidate)
	assert.False(t, config.AutoApply)
	assert.True(t, config.ReportOnly)
	assert.Equal(t, schemas.LearningModePropose, config.LearningMode)
	assert.Equal(t, schemas.LearningModePropose, config.LearningModeDefault)
	assert.True(t, config.AbstainOnUncertainty)
}

func TestResolveEffectiveConfigDefaultsApplyWithoutEntryMode(t *testing.T) {
	config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
		DocPath:      "docs/a.md",
		DefaultsMode: modePtr(schemas.DocModeHuman),
		Floors:       noFloors,
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeHuman, config.Mode)
	assert.True(t, config.PatchCandidate)
	assert.False(t, config.AutoApply)
}

func TestResolveEffectiveConfigFloorsDominateAndError(t *testing.T) {
	t.Run("auto above a suggest floor errors (NOT a clamp)", func(t *testing.T) {
		_, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath:   "docs/security/auth.md",
			EntryMode: modePtr(schemas.DocModeAuto),
			Floors:    []modes.OrgFloor{securityFloor},
		})
		require.Error(t, err)
		var floorErr *modes.ModeFloorViolationError
		require.True(t, errors.As(err, &floorErr))
		assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
		assert.Equal(t, "docs/security/auth.md", floorErr.Path)
		assert.Equal(t, schemas.DocModeAuto, floorErr.RequestedMode)
		assert.Equal(t, schemas.DocModeSuggest, floorErr.FloorMode)
		assert.Contains(t, floorErr.Message, "docs/security/auth.md")
		assert.Contains(t, floorErr.Message, `"auto"`)
		assert.Contains(t, floorErr.Message, `"suggest"`)
	})

	t.Run("same document at the floor maximum resolves normally", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath:   "docs/security/auth.md",
			EntryMode: modePtr(schemas.DocModeSuggest),
			Floors:    []modes.OrgFloor{securityFloor},
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.DocModeSuggest, config.Mode)
		assert.True(t, config.PatchCandidate)
		assert.False(t, config.AutoApply)
	})

	t.Run("a locked entry never violates any floor and never becomes a candidate", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath:   "docs/security/auth.md",
			EntryMode: modePtr(schemas.DocModeLocked),
			Floors:    []modes.OrgFloor{securityFloor},
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.DocModeLocked, config.Mode)
		assert.False(t, config.PatchCandidate)
		assert.False(t, config.AutoApply)
	})

	t.Run("D2 embedded: locked ownership wins over an auto entry and over flags", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath:   "docs/security/auth.md",
			EntryMode: modePtr(schemas.DocModeAuto),
			Ownership: ownerPtr(modes.OwnershipLocked),
			Floors:    noFloors,
			Flags:     &precedence.InvocationFlags{ReportOnly: boolPtr(false)},
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.DocModeLocked, config.Mode)
		assert.False(t, config.PatchCandidate)
		assert.False(t, config.AutoApply)
	})
}

func TestResolveEffectiveConfigReportOnlyPostureAndTightenOnlyFlags(t *testing.T) {
	t.Run("reportOnly defaults to true, so auto never auto-applies", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto), Floors: noFloors,
		})
		require.NoError(t, err)
		assert.True(t, config.ReportOnly)
		assert.False(t, config.AutoApply)
	})

	t.Run("flags.reportOnly=false is the only route to autoApply for an auto doc", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto), Floors: noFloors,
			Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false)},
		})
		require.NoError(t, err)
		assert.False(t, config.ReportOnly)
		assert.True(t, config.AutoApply)
	})

	t.Run("reportOnly=true flips autoApply off", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto), Floors: noFloors,
			Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false)},
		})
		require.NoError(t, err)
		tightened, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto), Floors: noFloors,
			Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false), DryRun: boolPtr(true)},
		})
		require.NoError(t, err)
		assert.True(t, tightened.ReportOnly)
		assert.False(t, tightened.AutoApply)
		assert.True(t, config.AutoApply)
	})

	t.Run("tightening flags provably never flip autoApply on (mode untouched)", func(t *testing.T) {
		flagless, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto), Floors: noFloors,
		})
		require.NoError(t, err)
		assert.False(t, flagless.AutoApply)
		flagSets := []*precedence.InvocationFlags{
			{ReportOnly: boolPtr(true)},
			{DryRun: boolPtr(true)},
			{Strict: boolPtr(true)},
			{ReportOnly: boolPtr(true), Strict: boolPtr(true)},
			{DryRun: boolPtr(true), Strict: boolPtr(true)},
		}
		for _, flags := range flagSets {
			tightened, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
				DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto), Floors: noFloors, Flags: flags,
			})
			require.NoError(t, err)
			assert.Equal(t, flagless.Mode, tightened.Mode)
			assert.False(t, tightened.AutoApply)
			assert.True(t, tightened.ReportOnly)
		}
	})

	t.Run("autoApply implies mode auto AND report-only off (invariant spot check)", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeSuggest), Floors: noFloors,
			Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false)},
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.DocModeSuggest, config.Mode)
		assert.False(t, config.AutoApply)
	})
}

func TestResolveEffectiveConfigAppliedSourcesProvenance(t *testing.T) {
	t.Run("records every layer's contribution in application order", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath:       "docs/security/auth.md",
			EntryMode:     modePtr(schemas.DocModeSuggest),
			DefaultsMode:  modePtr(schemas.DocModeSuggest),
			EntryTriggers: []string{"api_change"},
			EntryIgnore:   []string{"internal_refactor"},
			Floors:        []modes.OrgFloor{securityFloor},
			Flags:         &precedence.InvocationFlags{Strict: boolPtr(true)},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{
			"builtin:mode",
			"builtin:learningMode",
			"builtin:learningModeDefault",
			"builtin:abstainOnUncertainty",
			"builtin:reportOnly",
			"defaults:mode",
			"entry:mode",
			"entry:triggers",
			"entry:ignore",
			"floor:maxMode",
			"flags:strict",
		}, layers(config.AppliedSources))

		var floorSource, defaultsSource *precedence.AppliedSource
		for i := range config.AppliedSources {
			source := &config.AppliedSources[i]
			if source.Layer == precedence.LayerFloor {
				floorSource = source
			}
			if source.Layer == precedence.LayerDefaults {
				defaultsSource = source
			}
		}
		require.NotNil(t, floorSource)
		assert.Equal(t, schemas.DocModeSuggest, floorSource.Value)
		require.NotNil(t, defaultsSource)
		assert.Equal(t, schemas.DocModeSuggest, defaultsSource.Value)
	})

	t.Run("records the floor layer only when a floor actually applies", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/overview.md",
			Floors:  []modes.OrgFloor{securityFloor},
		})
		require.NoError(t, err)
		for _, source := range config.AppliedSources {
			assert.NotEqual(t, precedence.LayerFloor, source.Layer)
		}
	})

	t.Run("records flag contributions only for flags that are present", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/a.md", Floors: noFloors,
			Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false)},
		})
		require.NoError(t, err)
		flagSources := make([]precedence.AppliedSource, 0, 2)
		for _, source := range config.AppliedSources {
			if source.Layer == precedence.LayerFlags {
				flagSources = append(flagSources, source)
			}
		}
		assert.Equal(t, []precedence.AppliedSource{
			{Layer: precedence.LayerFlags, Field: precedence.FieldReportOnly, Value: false},
		}, flagSources)
	})

	t.Run("is pure: two calls with the same input deep-equal", func(t *testing.T) {
		input := precedence.PrecedenceInput{
			DocPath:      "docs/security/auth.md",
			EntryMode:    modePtr(schemas.DocModeSuggest),
			DefaultsMode: modePtr(schemas.DocModeSuggest),
			Floors:       []modes.OrgFloor{securityFloor},
			Flags:        &precedence.InvocationFlags{DryRun: boolPtr(true)},
			Ownership:    ownerPtr(modes.OwnershipHumanAuthored),
		}
		first, err := precedence.ResolveEffectiveConfig(input)
		require.NoError(t, err)
		second, err := precedence.ResolveEffectiveConfig(input)
		require.NoError(t, err)
		assert.Equal(t, first, second)
	})

	t.Run("records dryRun and strict in that order after reportOnly", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/a.md", Floors: noFloors,
			Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false), DryRun: boolPtr(true), Strict: boolPtr(true)},
		})
		require.NoError(t, err)
		flagLayers := make([]string, 0, 3)
		for _, source := range config.AppliedSources {
			if source.Layer == precedence.LayerFlags {
				flagLayers = append(flagLayers, source.Field)
			}
		}
		assert.Equal(t, []string{"reportOnly", "dryRun", "strict"}, flagLayers)
	})
}

func TestResolveEffectiveConfigMalformedInputsFailClosed(t *testing.T) {
	_, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
		DocPath:   "docs/a.md",
		Floors:    noFloors,
		Ownership: ownerPtr(modes.OwnershipCategory("ai-owned")),
	})
	require.Error(t, err)
	var conflict *modes.ModeConflictError
	require.True(t, errors.As(err, &conflict))
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))

	_, err = precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
		DocPath: "docs/a.md",
		Floors:  []modes.OrgFloor{{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/*.md", MaxMode: schemas.DocModeSuggest}},
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &conflict))
	assert.Contains(t, conflict.Message, `malformed floor pattern "docs/*.md"`)
}

// TestResolveEffectiveConfigWithOrgPolicy ports the orgPolicy assertions of
// packages/core/test/org-floors-p12-t13.test.ts.
func TestResolveEffectiveConfigWithOrgPolicy(t *testing.T) {
	orgPolicy := func(floors []schemas.OrgFloor, defaultMode schemas.LearningMode) *schemas.OrgPolicy {
		return &schemas.OrgPolicy{
			Version:   1,
			Floors:    floors,
			Learning:  &schemas.OrgLearning{DefaultMode: &defaultMode},
			CreatedAt: "2026-01-01T00:00:00Z",
			UpdatedAt: "2026-01-01T00:00:00Z",
		}
	}
	canary := schemas.LearningModeCanary
	propose := schemas.LearningModePropose
	securityMax := schemas.DocModeSuggest

	t.Run("org learning.defaultMode flows to the effective config", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath:   "docs/readme.md",
			Floors:    []modes.OrgFloor{},
			OrgPolicy: orgPolicy(nil, canary),
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.LearningModeCanary, config.LearningModeDefault)
	})

	t.Run("org floors compose with manifest floors (org is the floor)", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/security/readme.md",
			Floors: []modes.OrgFloor{
				{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/**", MaxMode: schemas.DocModeAuto},
			},
			OrgPolicy: orgPolicy([]schemas.OrgFloor{
				{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/security/**", MaxMode: &securityMax},
			}, propose),
		})
		require.NoError(t, err)
		// The org floor is more restrictive: suggest < auto.
		assert.Equal(t, schemas.DocModeSuggest, config.Mode)
	})

	t.Run("org floor violation errors (V-P12-12)", func(t *testing.T) {
		_, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath: "docs/security/readme.md",
			Floors:  []modes.OrgFloor{},
			OrgPolicy: orgPolicy([]schemas.OrgFloor{
				{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/security/**", MaxMode: &securityMax},
			}, propose),
			EntryMode: modePtr(schemas.DocModeAuto),
		})
		require.Error(t, err)
		var floorErr *modes.ModeFloorViolationError
		require.True(t, errors.As(err, &floorErr))
		assert.Equal(t, schemas.DocModeSuggest, floorErr.FloorMode)
	})

	t.Run("an org report-only floor never blocks the run", func(t *testing.T) {
		config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
			DocPath:   "docs/readme.md",
			Floors:    []modes.OrgFloor{},
			EntryMode: modePtr(schemas.DocModeAuto),
			OrgPolicy: orgPolicy([]schemas.OrgFloor{
				{
					Kind:              schemas.FloorKindReportOnlyUntilReviewed,
					Pattern:           "docs/**",
					RequiredReviewers: []string{"security-team"},
				},
			}, canary),
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.DocModeAuto, config.Mode)
		assert.True(t, config.PatchCandidate)
	})
}

// TestOrgLearningDefaultNeverChangesPosture ports the F5-3 block of
// packages/core/test/validator-org-advisories-p12-t16.test.ts: the org
// learning default resolves without touching the posture flags.
func TestOrgLearningDefaultNeverChangesPosture(t *testing.T) {
	canary := schemas.LearningModeCanary
	policy := &schemas.OrgPolicy{
		Version:   1,
		Floors:    []schemas.OrgFloor{},
		Learning:  &schemas.OrgLearning{DefaultMode: &canary},
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}

	config, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
		DocPath: "docs/readme.md", Floors: []modes.OrgFloor{}, OrgPolicy: policy,
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.LearningModeCanary, config.LearningModeDefault)
	assert.True(t, config.ReportOnly)
	assert.Equal(t, schemas.LearningModePropose, config.LearningMode)
	assert.True(t, config.AbstainOnUncertainty)

	baseline, err := precedence.ResolveEffectiveConfig(precedence.PrecedenceInput{
		DocPath: "docs/readme.md", Floors: []modes.OrgFloor{},
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.LearningModePropose, baseline.LearningModeDefault)
	assert.True(t, baseline.ReportOnly)
	assert.Equal(t, schemas.LearningModePropose, baseline.LearningMode)
}

func TestAppliedSourceConstantValues(t *testing.T) {
	assert.Equal(t, "flags", precedence.LayerFlags)
	assert.Equal(t, "entry", precedence.LayerEntry)
	assert.Equal(t, "defaults", precedence.LayerDefaults)
	assert.Equal(t, "floor", precedence.LayerFloor)
	assert.Equal(t, "builtin", precedence.LayerBuiltin)
	assert.Equal(t, "mode", precedence.FieldMode)
	assert.Equal(t, "learningMode", precedence.FieldLearningMode)
	assert.Equal(t, "learningModeDefault", precedence.FieldLearningModeDefault)
	assert.Equal(t, "abstainOnUncertainty", precedence.FieldAbstainOnUncertainty)
	assert.Equal(t, "reportOnly", precedence.FieldReportOnly)
	assert.Equal(t, "maxMode", precedence.FieldMaxMode)
	assert.Equal(t, "triggers", precedence.FieldTriggers)
	assert.Equal(t, "ignore", precedence.FieldIgnore)
	assert.Equal(t, "dryRun", precedence.FieldDryRun)
	assert.Equal(t, "strict", precedence.FieldStrict)
}
