package precedence

// Internal seam test (the TS suite reached into the module with vi.spyOn on
// `enforceFloors`, so this file is deliberately NOT black-box): the F3-3 block
// of packages/core/test/org-floors-p12-t13.test.ts proves that
// resolveEffectiveConfig converts org-policy floors into core floors and hands
// them to the floor engine with the `reviewed` flag preserved EXACTLY as the
// org declared it. Dropping it would make a declared review still nag
// (fail-closed the wrong way) whenever an advisory surfaces from the
// precedence path; inventing it would fail open.

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/modes"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seamOrgPolicy(floors []schemas.OrgFloor) *schemas.OrgPolicy {
	defaultMode := schemas.LearningModePropose
	return &schemas.OrgPolicy{
		Version:   1,
		Floors:    floors,
		Learning:  &schemas.OrgLearning{DefaultMode: &defaultMode},
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}
}

func TestOrgPolicyFloorConversionPassesReviewedThrough(t *testing.T) {
	reviewed := true
	explicitFalse := false

	cases := []struct {
		name             string
		reviewed         *bool
		expectAdvisories bool
	}{
		{"reviewed true stops the advisory", &reviewed, false},
		{"absent reviewed keeps the advisory (absence is not a review)", nil, true},
		{"reviewed false keeps the advisory", &explicitFalse, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := seamOrgPolicy([]schemas.OrgFloor{{
				Kind:              schemas.FloorKindReportOnlyUntilReviewed,
				Pattern:           "docs/**",
				RequiredReviewers: []string{"security-team"},
				Reviewed:          tc.reviewed,
			}})

			// The floors the precedence layer hands to the floor engine.
			handed := orgPolicyFloors(policy)
			require.Len(t, handed, 1)
			assert.Equal(t, schemas.FloorKindReportOnlyUntilReviewed, handed[0].Kind)
			assert.Equal(t, "docs/**", handed[0].Pattern)
			assert.Equal(t, []string{"security-team"}, handed[0].RequiredReviewers)
			assert.Equal(t, tc.reviewed, handed[0].Reviewed)

			// The full resolve path runs and agrees with checkFloors on the
			// flag: reviewed → no advisory, absent/false → advisory.
			config, err := ResolveEffectiveConfig(PrecedenceInput{
				DocPath:   "docs/readme.md",
				Floors:    []modes.OrgFloor{},
				OrgPolicy: policy,
			})
			require.NoError(t, err)
			assert.Equal(t, schemas.DocModeSuggest, config.Mode)
			assert.True(t, config.ReportOnly)

			check, err := modes.CheckFloors(modes.FloorCheckParams{
				Path: "docs/readme.md", Mode: &config.Mode, Floors: handed,
			})
			require.NoError(t, err)
			assert.True(t, check.Ok)
			if tc.expectAdvisories {
				require.Len(t, check.Advisories, 1)
				assert.Contains(t, check.Advisories[0].Message, "manifest not yet reviewed")
			} else {
				assert.Empty(t, check.Advisories)
			}

			// Advisories never block: the engine never errors on a report-only
			// floor, whatever the declared review state.
			assert.NoError(t, modes.EnforceFloors("docs/readme.md", config.Mode, handed))
		})
	}
}

func TestOrgPolicyFloorConversionPassesModeCeilingsThrough(t *testing.T) {
	maxMode := schemas.DocModeSuggest
	policy := seamOrgPolicy([]schemas.OrgFloor{{
		Kind: schemas.FloorKindModeCeiling, Pattern: "docs/security/**", MaxMode: &maxMode,
	}})

	handed := orgPolicyFloors(policy)
	require.Len(t, handed, 1)
	assert.Equal(t, modes.OrgFloor{
		Kind: schemas.FloorKindModeCeiling, Pattern: "docs/security/**", MaxMode: schemas.DocModeSuggest,
	}, handed[0])

	// The composition order is org-policy floors first, then manifest floors:
	// ../src/precedence/resolve.ts appends `input.floors` after the converted
	// org-policy floors.
	_, err := ResolveEffectiveConfig(PrecedenceInput{
		DocPath:   "docs/security/auth.md",
		EntryMode: modePointer(schemas.DocModeAuto),
		Floors:    []modes.OrgFloor{{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/**", MaxMode: schemas.DocModeAuto}},
		OrgPolicy: policy,
	})
	require.Error(t, err)

	var floorErr *modes.ModeFloorViolationError
	require.ErrorAs(t, err, &floorErr)
	assert.Equal(t, schemas.DocModeSuggest, floorErr.FloorMode)
}

func TestOrgPolicyFloorConversionNilPolicy(t *testing.T) {
	assert.Nil(t, orgPolicyFloors(nil))
}

func modePointer(mode schemas.DocMode) *schemas.DocMode { return &mode }
