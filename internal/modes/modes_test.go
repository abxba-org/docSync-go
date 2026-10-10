package modes_test

// Ported from packages/core/test/modes.test.ts (V-P0-5, V-P0-6, P0-T05/T06).
// Key contracts: D2 — classifier confidence NEVER overrides an ownership
// floor; locked/generated/external are never patch candidates; `auto` is the
// only auto-apply mode; a mode above an org floor is a validation error, not a
// clamp.

import (
	"math"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/modes"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modePtr(mode schemas.DocMode) *schemas.DocMode { return &mode }

func confidencePtr(value float64) *float64 { return &value }

func ownerPtr(owner modes.OwnershipCategory) *modes.OwnershipCategory { return &owner }

func TestModeLadderRanks(t *testing.T) {
	assert.Equal(t, 3, modes.ModeLadder[schemas.DocModeAuto])
	assert.Equal(t, 2, modes.ModeLadder[schemas.DocModeSuggest])
	assert.Equal(t, 2, modes.ModeLadder[schemas.DocModeHuman])
	assert.Equal(t, modes.ModeLadder[schemas.DocModeHuman], modes.ModeLadder[schemas.DocModeSuggest])
	assert.False(t, modes.IsMorePermissive(schemas.DocModeSuggest, schemas.DocModeHuman))
	assert.False(t, modes.IsMorePermissive(schemas.DocModeHuman, schemas.DocModeSuggest))
	assert.Equal(t, 0, modes.ModeLadder[schemas.DocModeLocked])
	assert.Equal(t, 0, modes.ModeLadder[schemas.DocModeGenerated])
	assert.False(t, modes.IsMorePermissive(schemas.DocModeGenerated, schemas.DocModeLocked))
}

func TestIsMorePermissiveIsAStrictOrderOnWritableRungs(t *testing.T) {
	assert.True(t, modes.IsMorePermissive(schemas.DocModeAuto, schemas.DocModeSuggest))
	assert.True(t, modes.IsMorePermissive(schemas.DocModeAuto, schemas.DocModeHuman))
	assert.False(t, modes.IsMorePermissive(schemas.DocModeSuggest, schemas.DocModeAuto))
	assert.False(t, modes.IsMorePermissive(schemas.DocModeAuto, schemas.DocModeAuto))
	assert.False(t, modes.IsMorePermissive(schemas.DocModeLocked, schemas.DocModeGenerated))
	assert.True(t, modes.IsMorePermissive(schemas.DocModeHuman, schemas.DocModeLocked))
}

// TestLadderMirrorInvariant ports the MODE_LADDER half of ladder.mirror.test.ts
// (the RESTRICTIVENESS half belongs to internal/globs, ported with that unit).
func TestLadderMirrorInvariant(t *testing.T) {
	t.Run("the ladder covers exactly the DocMode vocabulary", func(t *testing.T) {
		assert.Len(t, modes.ModeLadder, len(schemas.DocModeValues))
		for _, mode := range schemas.DocModeValues {
			_, ok := modes.ModeLadder[mode]
			assert.True(t, ok, "missing ladder rung for %s", mode)
		}
	})

	t.Run("isMorePermissive agrees with the ladder itself for ALL pairs", func(t *testing.T) {
		for _, m := range schemas.DocModeValues {
			for _, n := range schemas.DocModeValues {
				assert.Equal(t, modes.ModeLadder[m] > modes.ModeLadder[n],
					modes.IsMorePermissive(m, n), "pair (%s, %s)", m, n)
			}
		}
	})

	t.Run("documented ties: locked/generated at 0; suggest/human at 2; auto at 3", func(t *testing.T) {
		assert.Equal(t, 0, modes.ModeLadder[schemas.DocModeLocked])
		assert.Equal(t, 0, modes.ModeLadder[schemas.DocModeGenerated])
		assert.Equal(t, 2, modes.ModeLadder[schemas.DocModeSuggest])
		assert.Equal(t, 2, modes.ModeLadder[schemas.DocModeHuman])
		assert.Equal(t, 3, modes.ModeLadder[schemas.DocModeAuto])
	})
}

func TestResolveEffectiveModeLockedOwnershipForcesLockedAtAnyConfidence(t *testing.T) {
	for _, confidence := range []float64{0.01, 0.5, 0.99} {
		resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
			Ownership:            ownerPtr(modes.OwnershipLocked),
			ClassifierConfidence: confidencePtr(confidence),
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.DocModeLocked, resolution.Mode)
		assert.Equal(t, modes.EnforcementLockedFloor, resolution.Enforcement)
		assert.False(t, resolution.Writable)
		assert.False(t, resolution.PatchCandidate)
		assert.False(t, resolution.AutoApply)
		assert.Contains(t, resolution.BlockedReason, "D2")
		assert.Contains(t, resolution.BlockedReason, "manifest.yml")
	}
}

func TestResolveEffectiveModeIsDeepEqualForLowAndHighConfidence(t *testing.T) {
	low, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
		EntryMode:            modePtr(schemas.DocModeAuto),
		Ownership:            ownerPtr(modes.OwnershipLocked),
		ClassifierConfidence: confidencePtr(0.01),
	})
	require.NoError(t, err)
	high, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
		EntryMode:            modePtr(schemas.DocModeAuto),
		Ownership:            ownerPtr(modes.OwnershipLocked),
		ClassifierConfidence: confidencePtr(0.99),
	})
	require.NoError(t, err)
	assert.Equal(t, low, high)
	assert.Equal(t, modes.EnforcementLockedFloor, high.Enforcement)
	assert.False(t, low.PatchCandidate)
}

func TestResolveEffectiveModeGeneratedOwnershipIsExcluded(t *testing.T) {
	resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
		EntryMode: modePtr(schemas.DocModeAuto),
		Ownership: ownerPtr(modes.OwnershipGenerated),
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeAuto, resolution.Mode)
	assert.Equal(t, modes.EnforcementGeneratedExcluded, resolution.Enforcement)
	assert.False(t, resolution.Writable)
	assert.False(t, resolution.PatchCandidate)
	assert.False(t, resolution.AutoApply)
	assert.Contains(t, resolution.BlockedReason, "generator")
}

func TestResolveEffectiveModeExternalOwnershipIsExcluded(t *testing.T) {
	resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
		DefaultsMode: modePtr(schemas.DocModeSuggest),
		Ownership:    ownerPtr(modes.OwnershipExternal),
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeSuggest, resolution.Mode)
	assert.Equal(t, modes.EnforcementExternalExcluded, resolution.Enforcement)
	assert.False(t, resolution.PatchCandidate)
	assert.Contains(t, resolution.BlockedReason, "external")
}

func TestResolveEffectiveModeGeneratedAndExternalNeverCandidates(t *testing.T) {
	for _, ownership := range []modes.OwnershipCategory{modes.OwnershipGenerated, modes.OwnershipExternal} {
		for _, confidence := range []float64{0.01, 0.99} {
			resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
				EntryMode:            modePtr(schemas.DocModeAuto),
				Ownership:            ownerPtr(ownership),
				ClassifierConfidence: confidencePtr(confidence),
			})
			require.NoError(t, err)
			assert.False(t, resolution.PatchCandidate)
			assert.False(t, resolution.AutoApply)
		}
	}
}

func TestResolveEffectiveModeNonFloorOwnershipDoesNotOverrideModes(t *testing.T) {
	for _, ownership := range []modes.OwnershipCategory{modes.OwnershipHumanAuthored, modes.OwnershipAIMaintained} {
		resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
			EntryMode: modePtr(schemas.DocModeAuto),
			Ownership: ownerPtr(ownership),
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.DocModeAuto, resolution.Mode)
		assert.True(t, resolution.PatchCandidate)
		assert.Equal(t, modes.EnforcementMode, resolution.Enforcement)
		require.NotNil(t, resolution.Ownership)
		assert.Equal(t, ownership, *resolution.Ownership)
	}
}

func TestResolveEffectiveModeAutoIsWritableAndAutoApplies(t *testing.T) {
	resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{EntryMode: modePtr(schemas.DocModeAuto)})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeAuto, resolution.Mode)
	assert.True(t, resolution.Writable)
	assert.True(t, resolution.PatchCandidate)
	assert.True(t, resolution.AutoApply)
	assert.Empty(t, resolution.BlockedReason)
}

func TestResolveEffectiveModeSuggestAndHumanAreCandidatesButNeverAutoApply(t *testing.T) {
	for _, mode := range []schemas.DocMode{schemas.DocModeSuggest, schemas.DocModeHuman} {
		resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{EntryMode: modePtr(mode)})
		require.NoError(t, err)
		assert.Equal(t, mode, resolution.Mode)
		assert.True(t, resolution.Writable)
		assert.True(t, resolution.PatchCandidate)
		assert.False(t, resolution.AutoApply)
		assert.Empty(t, resolution.BlockedReason)
	}
}

func TestResolveEffectiveModeExplicitLockedIsTerminal(t *testing.T) {
	resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{EntryMode: modePtr(schemas.DocModeLocked)})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeLocked, resolution.Mode)
	assert.False(t, resolution.PatchCandidate)
	assert.False(t, resolution.AutoApply)
	assert.Equal(t, modes.EnforcementMode, resolution.Enforcement)
	assert.Contains(t, resolution.BlockedReason, "terminal")
}

func TestResolveEffectiveModeBuiltInDefaultIsSuggestWithNullEnforcement(t *testing.T) {
	resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeSuggest, resolution.Mode)
	assert.Nil(t, resolution.Ownership)
	assert.True(t, resolution.PatchCandidate)
	assert.False(t, resolution.AutoApply)
	assert.Empty(t, resolution.Enforcement)
}

func TestResolveEffectiveModeEntryOverridesDefaults(t *testing.T) {
	entrySuggest, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
		EntryMode: modePtr(schemas.DocModeSuggest), DefaultsMode: modePtr(schemas.DocModeAuto),
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeSuggest, entrySuggest.Mode)

	entryAuto, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
		EntryMode: modePtr(schemas.DocModeAuto), DefaultsMode: modePtr(schemas.DocModeSuggest),
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeAuto, entryAuto.Mode)
}

func TestResolveEffectiveModeDefaultsApplyWithoutEntryMode(t *testing.T) {
	resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{DefaultsMode: modePtr(schemas.DocModeHuman)})
	require.NoError(t, err)
	assert.Equal(t, schemas.DocModeHuman, resolution.Mode)
	assert.False(t, resolution.AutoApply)
	assert.Equal(t, modes.EnforcementMode, resolution.Enforcement)
}

func TestResolveEffectiveModeFailsClosedOnMalformedInputs(t *testing.T) {
	_, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{Ownership: ownerPtr(modes.OwnershipCategory("ai-owned"))})
	require.Error(t, err)
	var conflict *modes.ModeConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, modes.EDOCSYNC_MODE_CONFLICT, conflict.Code())
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
	assert.Equal(t, `unknown ownership category "ai-owned"`, conflict.Message)

	_, err = modes.ResolveEffectiveMode(modes.ModeResolutionInput{ClassifierConfidence: confidencePtr(1.5)})
	require.Error(t, err)
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "classifierConfidence must be a finite number in [0, 1], got 1.5", conflict.Message)

	_, err = modes.ResolveEffectiveMode(modes.ModeResolutionInput{ClassifierConfidence: confidencePtr(math.NaN())})
	require.Error(t, err)
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "classifierConfidence must be a finite number in [0, 1], got NaN", conflict.Message)

	_, err = modes.ResolveEffectiveMode(modes.ModeResolutionInput{ClassifierConfidence: confidencePtr(math.Inf(1))})
	require.Error(t, err)
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "classifierConfidence must be a finite number in [0, 1], got Infinity", conflict.Message)
}

// TestLockedLayersAdversarialModeResolution ports the mode-resolution half of
// lock-layers-adversarial.test.ts (V-P9-8, L1 candidate filtering).
func TestLockedLayersAdversarialModeResolution(t *testing.T) {
	for _, confidence := range []float64{0.01, 0.99} {
		modeLocked, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
			EntryMode:            modePtr(schemas.DocModeLocked),
			ClassifierConfidence: confidencePtr(confidence),
		})
		require.NoError(t, err)
		assert.False(t, modeLocked.PatchCandidate)
		assert.False(t, modeLocked.Writable)
		assert.False(t, modeLocked.AutoApply)
		assert.Equal(t, modes.EnforcementMode, modeLocked.Enforcement)

		floorLocked, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
			Ownership:            ownerPtr(modes.OwnershipLocked),
			ClassifierConfidence: confidencePtr(confidence),
		})
		require.NoError(t, err)
		assert.False(t, floorLocked.PatchCandidate)
		assert.Equal(t, schemas.DocModeLocked, floorLocked.Mode)
		assert.Equal(t, modes.EnforcementLockedFloor, floorLocked.Enforcement)

		generated, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
			EntryMode:            modePtr(schemas.DocModeAuto),
			Ownership:            ownerPtr(modes.OwnershipGenerated),
			ClassifierConfidence: confidencePtr(confidence),
		})
		require.NoError(t, err)
		assert.False(t, generated.PatchCandidate)
		assert.Equal(t, modes.EnforcementGeneratedExcluded, generated.Enforcement)

		external, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
			EntryMode:            modePtr(schemas.DocModeAuto),
			Ownership:            ownerPtr(modes.OwnershipExternal),
			ClassifierConfidence: confidencePtr(confidence),
		})
		require.NoError(t, err)
		assert.False(t, external.PatchCandidate)
		assert.Equal(t, modes.EnforcementExternalExcluded, external.Enforcement)
	}

	// Control: the same confidence on a writable mode admits the candidate.
	writable, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
		EntryMode: modePtr(schemas.DocModeAuto), ClassifierConfidence: confidencePtr(0.99),
	})
	require.NoError(t, err)
	assert.True(t, writable.PatchCandidate)
}

func TestIsDocModeAcceptsExactlyTheFiveModes(t *testing.T) {
	for _, mode := range []schemas.DocMode{
		schemas.DocModeAuto, schemas.DocModeSuggest, schemas.DocModeHuman,
		schemas.DocModeLocked, schemas.DocModeGenerated,
	} {
		assert.True(t, modes.IsDocMode(string(mode)), "%s should be a DocMode", mode)
	}
	assert.False(t, modes.IsDocMode("Auto"))
	assert.False(t, modes.IsDocMode("yolo"))
	assert.False(t, modes.IsDocMode(nil))
	assert.False(t, modes.IsDocMode(42))
	assert.False(t, modes.IsDocMode(schemas.DocMode("")))
}

func TestOwnershipCategoriesAreTheFiveCanonicalOnes(t *testing.T) {
	assert.Equal(t, []modes.OwnershipCategory{
		"human-authored", "ai-maintained", "generated", "external", "locked",
	}, modes.OwnershipCategories)
}
