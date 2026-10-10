package precedence_test

// Ported from packages/core/test/precedence.property.test.ts (V-P0-7,
// fast-check, 500 runs per property) and the P4 precedence property of
// packages/core/test/write-path-invariants-p12-t17.property.test.ts (V-P12-15).
//
// Random inputs over modes, floors, ownership, and flags must satisfy, for ALL
// cases:
//
//	P1. No invocation-flag combination loosens a MODE: the effective mode with
//	    flags is never more permissive than the same input with flags removed.
//	P2. Locked is never bypassed: ownership "locked" (or a locked entry) yields
//	    patchCandidate=false and autoApply=false, ALWAYS (D2 terminal).
//	P3. Floor dominance: when a floor with maxMode M matches the doc path, the
//	    effective mode is within M — unless the case errored with a floor
//	    violation, in which case the requested mode genuinely exceeded M
//	    (never clamped).
//	P4. Determinism: two calls with the same input deep-equal (no Date/Random).
//	P5. autoApply implies mode "auto" AND report-only off.
//	P6. (V-P12-15) with org-policy floors composed in: the effective mode never
//	    exceeds a matching floor, locked stays terminal, and reportOnly only
//	    ever tightens.
//
// fast-check's arbitraries are ported as deterministic table sweeps over the
// SAME domains (doc paths, floor patterns, modes, ownership, triggers/ignore,
// flag combinations, floor-array lengths) — every case is reproducible, seeded
// from the TS example inputs, and the sweeps are far larger than the 200-case
// floor the task sets.

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/modes"
	"github.com/DUDE616/docSync-go/internal/precedence"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TS generator domains (verbatim from precedence.property.test.ts).
var (
	propDocPaths = []string{
		"docs/security/auth.md",
		"docs/security/nested/deep.md",
		"docs/overview.md",
		"src/auth/login.ts",
		"README.md",
	}
	propFloorPatterns = []string{
		"docs/**",
		"docs/security/**",
		"docs",
		"src/**",
		"README.md",
		"**",
	}
	// patternArb = fc.oneof(fc.constantFrom(...FLOOR_PATTERNS), pathArb).
	propPatterns = append(append([]string{}, propFloorPatterns...), propDocPaths...)
	propModes    = []schemas.DocMode{
		schemas.DocModeAuto,
		schemas.DocModeSuggest,
		schemas.DocModeHuman,
		schemas.DocModeLocked,
		schemas.DocModeGenerated,
	}
	propOwnerships = []modes.OwnershipCategory{
		modes.OwnershipHumanAuthored,
		modes.OwnershipAIMaintained,
		modes.OwnershipGenerated,
		modes.OwnershipExternal,
		modes.OwnershipLocked,
	}
	propTriggers = []string{"api_change", "public_behavior"}
	propIgnores  = []string{"internal_refactor", "test_only"}
)

// optionalMode cycles through the modes plus "absent" (fc.option(modeArb)).
func optionalMode(index int) *schemas.DocMode {
	if index%6 == 5 {
		return nil
	}
	mode := propModes[index%5]
	return &mode
}

func optionalOwnership(index int) *modes.OwnershipCategory {
	if index%6 == 5 {
		return nil
	}
	owner := propOwnerships[index%5]
	return &owner
}

func optionalStrings(index int, values []string) []string {
	switch index % 4 {
	case 0:
		return nil
	case 1:
		return []string{values[index%len(values)]}
	case 2:
		return values
	default:
		return nil
	}
}

// flagCombos enumerates the 27 fc.record<InvocationFlags> combinations of
// reportOnly/dryRun/strict (each absent, false, or true), plus "no flags".
func flagCombos() []*precedence.InvocationFlags {
	bools := []*bool{nil, boolPtr(false), boolPtr(true)}
	combos := make([]*precedence.InvocationFlags, 0, 28)
	combos = append(combos, nil)
	for _, reportOnly := range bools {
		for _, dryRun := range bools {
			for _, strict := range bools {
				combos = append(combos, &precedence.InvocationFlags{
					ReportOnly: reportOnly,
					DryRun:     dryRun,
					Strict:     strict,
				})
			}
		}
	}
	return combos
}

// seededCases are the TS example inputs the property suites were seeded with
// (the worked examples from precedence.test.ts and the property file).
func seededCases() []precedence.PrecedenceInput {
	return []precedence.PrecedenceInput{
		{DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto), DefaultsMode: modePtr(schemas.DocModeSuggest)},
		{DocPath: "docs/a.md"},
		{DocPath: "docs/a.md", DefaultsMode: modePtr(schemas.DocModeHuman)},
		{DocPath: "docs/security/auth.md", EntryMode: modePtr(schemas.DocModeAuto), Floors: []modes.OrgFloor{securityFloor}},
		{DocPath: "docs/security/auth.md", EntryMode: modePtr(schemas.DocModeSuggest), Floors: []modes.OrgFloor{securityFloor}},
		{DocPath: "docs/security/auth.md", EntryMode: modePtr(schemas.DocModeLocked), Floors: []modes.OrgFloor{securityFloor}},
		{
			DocPath: "docs/security/auth.md", EntryMode: modePtr(schemas.DocModeAuto),
			Ownership: ownerPtr(modes.OwnershipLocked),
			Flags:     &precedence.InvocationFlags{ReportOnly: boolPtr(false)},
		},
		{DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto), Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false)}},
		{
			DocPath: "docs/a.md", EntryMode: modePtr(schemas.DocModeAuto),
			Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false), DryRun: boolPtr(true)},
		},
		{
			DocPath: "docs/security/auth.md", EntryMode: modePtr(schemas.DocModeSuggest),
			DefaultsMode:  modePtr(schemas.DocModeSuggest),
			EntryTriggers: []string{"api_change"}, EntryIgnore: []string{"internal_refactor"},
			Floors: []modes.OrgFloor{securityFloor},
			Flags:  &precedence.InvocationFlags{Strict: boolPtr(true)},
		},
		{
			DocPath: "docs/security/auth.md", EntryMode: modePtr(schemas.DocModeSuggest),
			DefaultsMode: modePtr(schemas.DocModeSuggest),
			Floors:       []modes.OrgFloor{securityFloor},
			Flags:        &precedence.InvocationFlags{DryRun: boolPtr(true)},
			Ownership:    ownerPtr(modes.OwnershipHumanAuthored),
		},
		{DocPath: "docs/overview.md", Floors: []modes.OrgFloor{securityFloor}},
		{DocPath: "docs/a.md", Flags: &precedence.InvocationFlags{ReportOnly: boolPtr(false)}},
	}
}

// modeOwnershipSweep enumerates docPath × entryMode × defaultsMode × ownership
// (5 × 6 × 6 × 6 = 1080 cases) with a fixed matching floor — the P1/P2/P4/P5
// backbone that the TS inputArb sampled randomly.
func modeOwnershipSweep() []precedence.PrecedenceInput {
	cases := make([]precedence.PrecedenceInput, 0, 1080)
	index := 0
	for _, docPath := range propDocPaths {
		for entryIndex := 0; entryIndex < 6; entryIndex++ {
			for defaultsIndex := 0; defaultsIndex < 6; defaultsIndex++ {
				for ownershipIndex := 0; ownershipIndex < 6; ownershipIndex++ {
					cases = append(cases, precedence.PrecedenceInput{
						DocPath:       docPath,
						EntryMode:     optionalMode(entryIndex),
						DefaultsMode:  optionalMode(defaultsIndex),
						EntryTriggers: optionalStrings(index, propTriggers),
						EntryIgnore:   optionalStrings(index, propIgnores),
						Floors:        []modes.OrgFloor{securityFloor},
						Ownership:     optionalOwnership(ownershipIndex),
					})
					index++
				}
			}
		}
	}
	return cases
}

// floorSweep enumerates pattern × maxMode × docPath (11 × 5 × 5 = 275 cases)
// with a cycling entry mode — the P3 floor-dominance sweep.
func floorSweep() []precedence.PrecedenceInput {
	cases := make([]precedence.PrecedenceInput, 0, 275)
	index := 0
	for _, pattern := range propPatterns {
		for _, maxMode := range propModes {
			for _, docPath := range propDocPaths {
				cases = append(cases, precedence.PrecedenceInput{
					DocPath:   docPath,
					EntryMode: optionalMode(index),
					Floors: []modes.OrgFloor{{
						Kind: schemas.FloorKindModeCeiling, Pattern: pattern, MaxMode: maxMode,
					}},
					Ownership: nil,
				})
				index++
			}
		}
	}
	return cases
}

// multiFloorSweep enumerates two-floor combinations (11 × 5 × 11 × 5 = 3025
// pairs, sampled deterministically) so overlapping-floor composition is
// exercised, not just single floors.
func multiFloorSweep() []precedence.PrecedenceInput {
	cases := make([]precedence.PrecedenceInput, 0, 605)
	index := 0
	for i := range propPatterns {
		for j := range propPatterns {
			for _, docPath := range propDocPaths {
				cases = append(cases, precedence.PrecedenceInput{
					DocPath:   docPath,
					EntryMode: optionalMode(index),
					Floors: []modes.OrgFloor{
						{Kind: schemas.FloorKindModeCeiling, Pattern: propPatterns[i], MaxMode: propModes[index%5]},
						{Kind: schemas.FloorKindModeCeiling, Pattern: propPatterns[j], MaxMode: propModes[(index+2)%5]},
						{Kind: schemas.FloorKindReportOnlyUntilReviewed, Pattern: propPatterns[(i+j)%len(propPatterns)]},
					},
				})
				index++
			}
		}
	}
	return cases
}

// flagsSweep enumerates the 27 flag records × 5 modes (135 cases), plus the
// TS worked examples — the P1/P5 tighten-only sweep.
func flagsSweep() []precedence.PrecedenceInput {
	cases := make([]precedence.PrecedenceInput, 0, 160)
	for _, flags := range flagCombos() {
		for _, mode := range propModes {
			cases = append(cases, precedence.PrecedenceInput{
				DocPath:   "docs/a.md",
				EntryMode: modePtr(mode),
				Floors:    noFloors,
				Flags:     flags,
			})
		}
	}
	return cases
}

func orgPolicyFor(floors []schemas.OrgFloor, defaultMode schemas.LearningMode) *schemas.OrgPolicy {
	mode := defaultMode
	return &schemas.OrgPolicy{
		Version:   1,
		Floors:    floors,
		Learning:  &schemas.OrgLearning{DefaultMode: &mode},
		CreatedAt: "2026-10-01T12:00:00Z",
		UpdatedAt: "2026-10-01T12:00:00Z",
	}
}

// orgPolicySweep enumerates org-policy floors × manifest floors × doc paths
// (6 × 5 × 5 = 150 cases) — the V-P12-15 P6 sweep.
func orgPolicySweep() []precedence.PrecedenceInput {
	canary := schemas.LearningModeCanary
	propose := schemas.LearningModePropose
	cases := make([]precedence.PrecedenceInput, 0, 150)
	index := 0
	for _, pattern := range propFloorPatterns {
		for _, maxMode := range propModes {
			for _, docPath := range propDocPaths {
				policy := orgPolicyFor([]schemas.OrgFloor{
					{Kind: schemas.FloorKindModeCeiling, Pattern: pattern, MaxMode: &maxMode},
				}, canary)
				manifestFloors := []modes.OrgFloor{securityFloor}
				if index%3 == 0 {
					manifestFloors = nil
				}
				if index%4 == 0 {
					policy = orgPolicyFor([]schemas.OrgFloor{{
						Kind:              schemas.FloorKindReportOnlyUntilReviewed,
						Pattern:           pattern,
						RequiredReviewers: []string{"security-team"},
					}}, propose)
				}
				cases = append(cases, precedence.PrecedenceInput{
					DocPath:   docPath,
					EntryMode: optionalMode(index),
					Floors:    manifestFloors,
					OrgPolicy: policy,
					Ownership: optionalOwnership(index),
					Flags:     flagCombos()[index%len(flagCombos())],
				})
				index++
			}
		}
	}
	return cases
}

type attemptResult struct {
	ok    bool
	value precedence.EffectiveConfig
	err   error
}

func attempt(input precedence.PrecedenceInput) attemptResult {
	value, err := precedence.ResolveEffectiveConfig(input)
	if err != nil {
		return attemptResult{err: err}
	}
	return attemptResult{ok: true, value: value}
}

// requestedModeFor mirrors the TS helper: ownership locked short-circuits to
// 'locked', else entry ?? defaults ?? 'suggest'.
func requestedModeFor(input precedence.PrecedenceInput) schemas.DocMode {
	if input.Ownership != nil && *input.Ownership == modes.OwnershipLocked {
		return schemas.DocModeLocked
	}
	if input.EntryMode != nil {
		return *input.EntryMode
	}
	if input.DefaultsMode != nil {
		return *input.DefaultsMode
	}
	return schemas.DocModeSuggest
}

// allFloors mirrors the composed floors of precedence/resolve.go (org-policy
// floors + manifest-provided floors) as core OrgFloors, exactly as the TS P4
// property did.
func allFloors(input precedence.PrecedenceInput) []modes.OrgFloor {
	converted := make([]modes.OrgFloor, 0, len(input.Floors)+4)
	if input.OrgPolicy != nil {
		for _, floor := range input.OrgPolicy.Floors {
			switch floor.Kind {
			case schemas.FloorKindModeCeiling:
				mode := schemas.DocModeSuggest
				if floor.MaxMode != nil {
					mode = *floor.MaxMode
				}
				converted = append(converted, modes.OrgFloor{
					Kind: schemas.FloorKindModeCeiling, Pattern: floor.Pattern, MaxMode: mode,
				})
			case schemas.FloorKindReportOnlyUntilReviewed:
				converted = append(converted, modes.OrgFloor{
					Kind:              schemas.FloorKindReportOnlyUntilReviewed,
					Pattern:           floor.Pattern,
					RequiredReviewers: floor.RequiredReviewers,
					Reviewed:          floor.Reviewed,
				})
			}
		}
	}
	converted = append(converted, input.Floors...)
	return converted
}

func TestPrecedencePropertyP1NoFlagCombinationLoosensTheMode(t *testing.T) {
	cases := append(seededCases(), flagsSweep()...)
	cases = append(cases, modeOwnershipSweep()...)
	runCount := 0
	for _, input := range cases {
		withFlags := attempt(input)
		withoutFlagsInput := input
		withoutFlagsInput.Flags = nil
		withoutFlags := attempt(withoutFlagsInput)
		require.Equal(t, withFlags.ok, withoutFlags.ok, "case %+v", input)
		if withFlags.ok && withoutFlags.ok {
			assert.Equal(t, withoutFlags.value.Mode, withFlags.value.Mode, "case %+v", input)
			assert.LessOrEqual(t,
				modes.ModeLadder[withFlags.value.Mode],
				modes.ModeLadder[withoutFlags.value.Mode], "case %+v", input)
		}
		runCount++
	}
	assert.GreaterOrEqual(t, runCount, 200)
	t.Logf("P1 ran %d deterministic cases", runCount)
}

func TestPrecedencePropertyP2LockedIsNeverBypassed(t *testing.T) {
	cases := append(seededCases(), modeOwnershipSweep()...)
	cases = append(cases, orgPolicySweep()...)
	runCount := 0
	for _, input := range cases {
		ownershipLockedInput := input
		ownershipLockedInput.Ownership = ownerPtr(modes.OwnershipLocked)
		ownershipLocked := attempt(ownershipLockedInput)
		require.True(t, ownershipLocked.ok, "case %+v must resolve", input)
		assert.Equal(t, schemas.DocModeLocked, ownershipLocked.value.Mode, "case %+v", input)
		assert.Equal(t, 0, modes.ModeLadder[ownershipLocked.value.Mode])
		assert.False(t, ownershipLocked.value.PatchCandidate, "case %+v", input)
		assert.False(t, ownershipLocked.value.AutoApply, "case %+v", input)

		entryLockedInput := input
		entryLockedInput.Ownership = nil
		entryLockedInput.EntryMode = modePtr(schemas.DocModeLocked)
		entryLocked := attempt(entryLockedInput)
		require.True(t, entryLocked.ok, "case %+v must resolve", input)
		assert.Equal(t, schemas.DocModeLocked, entryLocked.value.Mode, "case %+v", input)
		assert.False(t, entryLocked.value.PatchCandidate, "case %+v", input)
		assert.False(t, entryLocked.value.AutoApply, "case %+v", input)
		runCount++
	}
	assert.GreaterOrEqual(t, runCount, 200)
	t.Logf("P2 ran %d deterministic cases", runCount)
}

func TestPrecedencePropertyP3FloorsDominate(t *testing.T) {
	cases := append(seededCases(), floorSweep()...)
	cases = append(cases, multiFloorSweep()...)
	runCount := 0
	for _, input := range cases {
		max, found, err := modes.ComposedFloorMax(input.Floors, input.DocPath)
		require.NoError(t, err, "case %+v", input)
		requested := requestedModeFor(input)
		result := attempt(input)

		if !found {
			assert.True(t, result.ok, "no floor may match yet the case errored: %+v", input)
			runCount++
			continue
		}
		if modes.ModeLadder[requested] > modes.ModeLadder[max] {
			assert.False(t, result.ok, "case %+v must error, not clamp", input)
			if !result.ok {
				var floorErr *modes.ModeFloorViolationError
				require.True(t, errors.As(result.err, &floorErr))
				assert.Equal(t, requested, floorErr.RequestedMode, "case %+v", input)
				assert.Equal(t, max, floorErr.FloorMode, "case %+v", input)
			}
			runCount++
			continue
		}
		assert.True(t, result.ok, "case %+v must resolve: %v", input, result.err)
		if result.ok {
			assert.LessOrEqual(t, modes.ModeLadder[result.value.Mode], modes.ModeLadder[max],
				"case %+v", input)
		}
		runCount++
	}
	assert.GreaterOrEqual(t, runCount, 200)
	t.Logf("P3 ran %d deterministic cases", runCount)
}

func TestPrecedencePropertyP4Deterministic(t *testing.T) {
	cases := append(seededCases(), modeOwnershipSweep()...)
	cases = append(cases, floorSweep()...)
	cases = append(cases, flagsSweep()...)
	runCount := 0
	for _, input := range cases {
		first := attempt(input)
		second := attempt(input)
		require.Equal(t, first.ok, second.ok, "case %+v", input)
		if first.ok && second.ok {
			assert.Equal(t, first.value, second.value, "case %+v", input)
		} else {
			assert.Equal(t, first.err.Error(), second.err.Error(), "case %+v", input)
		}
		runCount++
	}
	assert.GreaterOrEqual(t, runCount, 200)
	t.Logf("P4 ran %d deterministic cases", runCount)
}

func TestPrecedencePropertyP5AutoApplyImpliesAutoAndReportOnlyOff(t *testing.T) {
	cases := append(seededCases(), flagsSweep()...)
	cases = append(cases, modeOwnershipSweep()...)
	cases = append(cases, orgPolicySweep()...)
	runCount := 0
	for _, input := range cases {
		result := attempt(input)
		if !result.ok {
			runCount++
			continue
		}
		if result.value.AutoApply {
			assert.Equal(t, schemas.DocModeAuto, result.value.Mode, "case %+v", input)
			assert.False(t, result.value.ReportOnly, "case %+v", input)
		}
		if input.Flags != nil &&
			((input.Flags.DryRun != nil && *input.Flags.DryRun) ||
				(input.Flags.ReportOnly != nil && *input.Flags.ReportOnly)) {
			assert.True(t, result.value.ReportOnly, "case %+v", input)
			assert.False(t, result.value.AutoApply, "case %+v", input)
		}
		runCount++
	}
	assert.GreaterOrEqual(t, runCount, 200)
	t.Logf("P5 ran %d deterministic cases", runCount)
}

// TestPrecedencePropertyP6WritePathInvariants ports the P4 precedence property
// of write-path-invariants-p12-t17.property.test.ts (V-P12-15) with the
// org-policy generator included.
func TestPrecedencePropertyP6WritePathInvariants(t *testing.T) {
	cases := append(seededCases(), orgPolicySweep()...)
	cases = append(cases, floorSweep()...)
	runCount := 0
	for _, input := range cases {
		floors := allFloors(input)
		result := attempt(input)

		// (a) never a mode ABOVE a matching floor (or a thrown, unclamped
		// violation).
		if result.ok {
			check, err := modes.CheckFloors(modes.FloorCheckParams{
				Path: input.DocPath, Mode: modePtr(result.value.Mode), Floors: floors,
			})
			require.NoError(t, err, "case %+v", input)
			assert.True(t, check.Ok, "case %+v resolves above its floor", input)
			if max, found, err := modes.ComposedFloorMax(floors, input.DocPath); assert.NoError(t, err) && found {
				assert.LessOrEqual(t, modes.ModeLadder[result.value.Mode], modes.ModeLadder[max],
					"case %+v", input)
			}
		} else {
			// A refusal only happens when the requested mode genuinely exceeds
			// the floor.
			max, found, err := modes.ComposedFloorMax(floors, input.DocPath)
			require.NoError(t, err, "case %+v", input)
			if found {
				assert.Greater(t, modes.ModeLadder[requestedModeFor(input)], modes.ModeLadder[max],
					"case %+v errored without exceeding a floor", input)
			}
		}

		// (b) locked stays TERMINAL — nothing above it, even an `auto` floor.
		lockedInput := input
		lockedInput.Ownership = ownerPtr(modes.OwnershipLocked)
		locked := attempt(lockedInput)
		require.True(t, locked.ok, "case %+v must resolve", input)
		assert.Equal(t, schemas.DocModeLocked, locked.value.Mode)
		assert.Equal(t, 0, modes.ModeLadder[locked.value.Mode])
		assert.False(t, locked.value.PatchCandidate)
		assert.False(t, locked.value.AutoApply)

		entryLockedInput := input
		entryLockedInput.Ownership = nil
		entryLockedInput.EntryMode = modePtr(schemas.DocModeLocked)
		entryLocked := attempt(entryLockedInput)
		require.True(t, entryLocked.ok, "case %+v must resolve", input)
		assert.Equal(t, schemas.DocModeLocked, entryLocked.value.Mode)
		assert.False(t, entryLocked.value.PatchCandidate)
		assert.False(t, entryLocked.value.AutoApply)

		// (c) reportOnly only ever TIGHTENS: autoApply implies auto + off.
		if result.ok && result.value.AutoApply {
			assert.Equal(t, schemas.DocModeAuto, result.value.Mode)
			assert.False(t, result.value.ReportOnly)
		}
		runCount++
	}
	assert.GreaterOrEqual(t, runCount, 200)
	t.Logf("P6 ran %d deterministic cases", runCount)
}

func TestPropertySweepSizes(t *testing.T) {
	// Traceability for the port: the TS suites sampled fast-check arbitrarily;
	// these are the deterministic sweeps that replaced them.
	assert.Len(t, seededCases(), 13)
	assert.Len(t, flagCombos(), 28)
	assert.Len(t, modeOwnershipSweep(), 1080)
	assert.Len(t, floorSweep(), 275)
	assert.Len(t, multiFloorSweep(), 605)
	assert.Len(t, orgPolicySweep(), 150)
}
