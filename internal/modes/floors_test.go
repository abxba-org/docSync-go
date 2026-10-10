package modes_test

// Ported from the floor-heavy parts of packages/core/test/modes.test.ts
// (floorMatchesPath, checkFloors, floorSummary, enforceFloors — V-P0-6),
// packages/core/test/org-floors-p12-t13.test.ts (P12-T13 org floors: ceiling
// violations, report-only advisories, reviewed declarations, composed maxima)
// and packages/core/test/testing-kit.property.test.ts (floor patterns are
// well-formed and match themselves).

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/modes"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ceiling(pattern string, maxMode schemas.DocMode) modes.OrgFloor {
	return modes.OrgFloor{Kind: schemas.FloorKindModeCeiling, Pattern: pattern, MaxMode: maxMode}
}

func reportOnly(pattern string) modes.OrgFloor {
	return modes.OrgFloor{Kind: schemas.FloorKindReportOnlyUntilReviewed, Pattern: pattern}
}

func reportOnlyReviewed(pattern string, requiredReviewers []string, reviewed *bool) modes.OrgFloor {
	return modes.OrgFloor{
		Kind:              schemas.FloorKindReportOnlyUntilReviewed,
		Pattern:           pattern,
		RequiredReviewers: requiredReviewers,
		Reviewed:          reviewed,
	}
}

func boolPtr(value bool) *bool { return &value }

func TestFloorMatchesPath(t *testing.T) {
	t.Run("matches exact paths", func(t *testing.T) {
		matches, err := modes.FloorMatchesPath("docs/security/auth.md", "docs/security/auth.md")
		require.NoError(t, err)
		assert.True(t, matches)
		matches, err = modes.FloorMatchesPath("docs/security/auth.md", "docs/security/auth.md.bak")
		require.NoError(t, err)
		assert.False(t, matches)
	})

	t.Run("matches trailing dir/** including the directory itself", func(t *testing.T) {
		for _, path := range []string{"docs/security/auth.md", "docs/security/a/b.md", "docs/security"} {
			matches, err := modes.FloorMatchesPath("docs/security/**", path)
			require.NoError(t, err)
			assert.True(t, matches, path)
		}
		matches, err := modes.FloorMatchesPath("docs/security/**", "docs/overview.md")
		require.NoError(t, err)
		assert.False(t, matches)
	})

	t.Run("treats a bare directory as dir/**", func(t *testing.T) {
		for _, path := range []string{"docs/overview.md", "docs"} {
			matches, err := modes.FloorMatchesPath("docs", path)
			require.NoError(t, err)
			assert.True(t, matches, path)
		}
		matches, err := modes.FloorMatchesPath("docs", "docsfoo/overview.md")
		require.NoError(t, err)
		assert.False(t, matches)
	})

	t.Run("supports bare ** as match-everything", func(t *testing.T) {
		for _, path := range []string{"any/file.md", "file.md"} {
			matches, err := modes.FloorMatchesPath("**", path)
			require.NoError(t, err)
			assert.True(t, matches, path)
		}
	})

	t.Run("does not match sibling prefixes", func(t *testing.T) {
		matches, err := modes.FloorMatchesPath("docs/**", "docsfoo/a.md")
		require.NoError(t, err)
		assert.False(t, matches)
		matches, err = modes.FloorMatchesPath("docs/**", "docs")
		require.NoError(t, err)
		assert.True(t, matches)
	})

	t.Run("rejects malformed patterns with a coded error (fail closed)", func(t *testing.T) {
		for _, pattern := range []string{
			"",
			"/abs/path",
			"a/../b",
			"docs/*.md",
			"a/**/b",
			"docs/",
			".",
			`docs\a.md`,
			"{a,b}",
			"docs/?.md",
			"docs/[ab].md",
		} {
			_, err := modes.FloorMatchesPath(pattern, "docs/a.md")
			require.Error(t, err, "pattern %q", pattern)
			var conflict *modes.ModeConflictError
			require.ErrorAs(t, err, &conflict)
			assert.Equal(t, modes.EDOCSYNC_MODE_CONFLICT, conflict.Code())
			assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
			assert.Contains(t, conflict.Message, `malformed floor pattern "`+pattern+`"`)
		}
	})
}

func TestAssertValidFloorPatternMessages(t *testing.T) {
	cases := []struct {
		pattern string
		message string
	}{
		{"", `malformed floor pattern "": must not be empty`},
		{"/abs/path", `malformed floor pattern "/abs/path": must be relative, not absolute`},
		{`docs\a.md`, `malformed floor pattern "docs\a.md": must use POSIX '/' separators`},
		{"a/**/b", `malformed floor pattern "a/**/b": '**' is only supported as the final segment`},
		{"docs//a.md", `malformed floor pattern "docs//a.md": must not contain empty segments (double slashes or trailing '/')`},
		{"a/../b", `malformed floor pattern "a/../b": must not contain '.' or '..' segments`},
		{"docs/*.md", `malformed floor pattern "docs/*.md": only exact paths, bare directories, and trailing 'dir/**' are supported`},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			err := modes.AssertValidFloorPattern(tc.pattern)
			require.Error(t, err)
			var conflict *modes.ModeConflictError
			require.ErrorAs(t, err, &conflict)
			assert.Equal(t, tc.message, conflict.Message)
		})
	}
	assert.NoError(t, modes.AssertValidFloorPattern("docs/security/**"))
	assert.NoError(t, modes.AssertValidFloorPattern("docs/security"))
	assert.NoError(t, modes.AssertValidFloorPattern("docs/security/auth.md"))
	assert.NoError(t, modes.AssertValidFloorPattern("**"))
}

func TestCheckedFloorsAllowModeWithinCeiling(t *testing.T) {
	result, err := modes.CheckFloors(modes.FloorCheckParams{
		Path:   "docs/security/readme.md",
		Mode:   modePtr(schemas.DocModeSuggest),
		Floors: []modes.OrgFloor{ceiling("docs/security/**", schemas.DocModeSuggest)},
	})
	require.NoError(t, err)
	assert.True(t, result.Ok)
}

func TestCheckFloorsRejectModeExceedingCeiling(t *testing.T) {
	securityFloor := ceiling("docs/security/**", schemas.DocModeSuggest)
	result, err := modes.CheckFloors(modes.FloorCheckParams{
		Path:   "docs/security/auth.md",
		Mode:   modePtr(schemas.DocModeAuto),
		Floors: []modes.OrgFloor{securityFloor},
	})
	require.NoError(t, err)
	assert.False(t, result.Ok)
	require.Len(t, result.Errors, 1)
	assert.Equal(t, "docs/security/auth.md", result.Errors[0].Path)
	assert.Equal(t, schemas.DocModeAuto, result.Errors[0].RequestedMode)
	assert.Equal(t, securityFloor, result.Errors[0].Floor)
	assert.Contains(t, result.Errors[0].Message, "docs/security/auth.md")
	assert.Contains(t, result.Errors[0].Message, `"auto"`)
	assert.Contains(t, result.Errors[0].Message, `"suggest"`)
	assert.Contains(t, result.Errors[0].Message, "org floor maximum")
}

func TestCheckFloorsPassAtCeilingAndWithoutAMode(t *testing.T) {
	floors := []modes.OrgFloor{ceiling("docs/security/**", schemas.DocModeSuggest)}

	atCeiling, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/security/auth.md", Mode: modePtr(schemas.DocModeSuggest), Floors: floors,
	})
	require.NoError(t, err)
	assert.Equal(t, modes.FloorCheckResult{Ok: true}, atCeiling)

	noMode, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/security/auth.md", Floors: floors,
	})
	require.NoError(t, err)
	assert.Equal(t, modes.FloorCheckResult{Ok: true}, noMode)
}

func TestCheckFloorsOverlappingFloorsComposeToTheMostRestrictiveMaximum(t *testing.T) {
	floors := []modes.OrgFloor{
		ceiling("docs/**", schemas.DocModeAuto),
		ceiling("docs/security/**", schemas.DocModeSuggest),
	}
	authResult, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/security/auth.md", Mode: modePtr(schemas.DocModeAuto), Floors: floors,
	})
	require.NoError(t, err)
	assert.False(t, authResult.Ok)

	overviewResult, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/overview.md", Mode: modePtr(schemas.DocModeAuto), Floors: floors,
	})
	require.NoError(t, err)
	assert.Equal(t, modes.FloorCheckResult{Ok: true}, overviewResult)

	max, ok, err := modes.ComposedFloorMax(floors, "docs/security/auth.md")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, schemas.DocModeSuggest, max)
	max, ok, err = modes.ComposedFloorMax(floors, "docs/overview.md")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, schemas.DocModeAuto, max)
}

func TestCheckFloorsLockedAndGeneratedMaxModesAreLegalAndTightenFurther(t *testing.T) {
	lockedFloors := []modes.OrgFloor{ceiling("docs/**", schemas.DocModeLocked)}
	suggestResult, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/a.md", Mode: modePtr(schemas.DocModeSuggest), Floors: lockedFloors,
	})
	require.NoError(t, err)
	assert.False(t, suggestResult.Ok)

	lockedResult, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/a.md", Mode: modePtr(schemas.DocModeLocked), Floors: lockedFloors,
	})
	require.NoError(t, err)
	assert.Equal(t, modes.FloorCheckResult{Ok: true}, lockedResult)

	generatedResult, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/a.md", Mode: modePtr(schemas.DocModeSuggest),
		Floors: []modes.OrgFloor{ceiling("docs/**", schemas.DocModeGenerated)},
	})
	require.NoError(t, err)
	assert.False(t, generatedResult.Ok)
}

func TestCheckFloorsReportsEveryViolatedPairSortedDeterministically(t *testing.T) {
	floors := []modes.OrgFloor{
		ceiling("docs/security/**", schemas.DocModeLocked),
		ceiling("docs", schemas.DocModeSuggest),
	}
	result, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/security/auth.md", Mode: modePtr(schemas.DocModeAuto), Floors: floors,
	})
	require.NoError(t, err)
	assert.False(t, result.Ok)
	patterns := make([]string, 0, len(result.Errors))
	for _, violation := range result.Errors {
		patterns = append(patterns, violation.Floor.Pattern)
	}
	assert.Equal(t, []string{"docs", "docs/security/**"}, patterns)
}

func TestCheckFloorsMalformedFloorsThrowEvenWhenTheyDoNotMatch(t *testing.T) {
	_, err := modes.CheckFloors(modes.FloorCheckParams{
		Path:   "other/file.md",
		Mode:   modePtr(schemas.DocModeAuto),
		Floors: []modes.OrgFloor{ceiling("a/../b", schemas.DocModeSuggest)},
	})
	require.Error(t, err)
	var conflict *modes.ModeConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Contains(t, conflict.Message, `malformed floor pattern "a/../b"`)

	_, err = modes.CheckFloors(modes.FloorCheckParams{
		Path:   "other/file.md",
		Mode:   modePtr(schemas.DocModeAuto),
		Floors: []modes.OrgFloor{ceiling("docs/**", schemas.DocMode("auto-magic"))},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t,
		`malformed floor: maxMode "auto-magic" is not one of auto, suggest, locked, generated, human`,
		conflict.Message)
}

func TestFloorSummaryListsApplicableFloorsSortedByPattern(t *testing.T) {
	floors := []modes.OrgFloor{
		ceiling("src/**", schemas.DocModeAuto),
		ceiling("docs/security/**", schemas.DocModeSuggest),
		ceiling("docs/**", schemas.DocModeAuto),
	}
	auth, err := modes.FloorSummary(floors, "docs/security/auth.md")
	require.NoError(t, err)
	authPatterns := make([]string, 0, len(auth))
	for _, floor := range auth {
		authPatterns = append(authPatterns, floor.Pattern)
	}
	assert.Equal(t, []string{"docs/**", "docs/security/**"}, authPatterns)

	src, err := modes.FloorSummary(floors, "src/index.ts")
	require.NoError(t, err)
	require.Len(t, src, 1)
	assert.Equal(t, "src/**", src[0].Pattern)

	none, err := modes.FloorSummary(floors, "README.md")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestFloorSummarySkipsReportOnlyFloorsFromTheComposedMaximum(t *testing.T) {
	floors := []modes.OrgFloor{
		ceiling("docs/**", schemas.DocModeAuto),
		reportOnly("docs/**"),
	}
	max, ok, err := modes.ComposedFloorMax(floors, "docs/readme.md")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, schemas.DocModeAuto, max)
}

func TestComposedFloorMaxReturnsNotFoundForNonMatchingPaths(t *testing.T) {
	_, ok, err := modes.ComposedFloorMax(
		[]modes.OrgFloor{ceiling("docs/security/**", schemas.DocModeSuggest)}, "README.md")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEnforceFloors(t *testing.T) {
	t.Run("throws a coded error naming path and both modes", func(t *testing.T) {
		err := modes.EnforceFloors("docs/security/auth.md", schemas.DocModeAuto,
			[]modes.OrgFloor{ceiling("docs/security/**", schemas.DocModeSuggest)})
		require.Error(t, err)
		var floorErr *modes.ModeFloorViolationError
		require.ErrorAs(t, err, &floorErr)
		assert.Equal(t, modes.EDOCSYNC_FLOOR, floorErr.Code())
		assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
		assert.Equal(t, "docs/security/auth.md", floorErr.Path)
		assert.Equal(t, schemas.DocModeAuto, floorErr.RequestedMode)
		assert.Equal(t, schemas.DocModeSuggest, floorErr.FloorMode)
		assert.Contains(t, floorErr.Message, "docs/security/auth.md")
		assert.Contains(t, floorErr.Message, "auto")
		assert.Contains(t, floorErr.Message, "suggest")
		assert.Contains(t, floorErr.Message, "org floor maximum")
	})

	t.Run("passes silently when no floor is violated", func(t *testing.T) {
		assert.NoError(t, modes.EnforceFloors("docs/a.md", schemas.DocModeAuto,
			[]modes.OrgFloor{ceiling("docs/**", schemas.DocModeAuto)}))
	})

	t.Run("reports the most restrictive violated floor", func(t *testing.T) {
		err := modes.EnforceFloors("docs/security/auth.md", schemas.DocModeAuto, []modes.OrgFloor{
			ceiling("docs/**", schemas.DocModeSuggest),
			ceiling("docs/security/**", schemas.DocModeLocked),
		})
		require.Error(t, err)
		var floorErr *modes.ModeFloorViolationError
		require.ErrorAs(t, err, &floorErr)
		assert.Equal(t, schemas.DocModeLocked, floorErr.FloorMode)
		assert.Contains(t, floorErr.Message, `docs/security/**`)
	})

	t.Run("propagates malformed floor defects instead of passing", func(t *testing.T) {
		err := modes.EnforceFloors("docs/a.md", schemas.DocModeAuto,
			[]modes.OrgFloor{ceiling("docs/*.md", schemas.DocModeSuggest)})
		require.Error(t, err)
		var conflict *modes.ModeConflictError
		require.ErrorAs(t, err, &conflict)
		assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
	})
}

// TestOrgFloorsP12T13 ports the mode-resolution assertions of
// packages/core/test/org-floors-p12-t13.test.ts (P12-T13; ARCH §11.2, §11.3;
// V-P12-12): mode ceiling violations are exit-2 errors, report-only floors are
// advisories, `locked` is terminal, and overlapping floors compose.
func TestOrgFloorsP12T13(t *testing.T) {
	modeCeilingFloor := ceiling("docs/security/**", schemas.DocModeSuggest)
	reportOnlyFloor := reportOnlyReviewed("docs/**", []string{"security-team"}, nil)
	lockedFloor := ceiling("docs/internal/**", schemas.DocModeLocked)

	t.Run("checkFloors: locked terminal — never allows any mode above locked", func(t *testing.T) {
		result, err := modes.CheckFloors(modes.FloorCheckParams{
			Path: "docs/internal/secret.md", Mode: modePtr(schemas.DocModeSuggest), Floors: []modes.OrgFloor{lockedFloor},
		})
		require.NoError(t, err)
		assert.False(t, result.Ok)
		assert.Greater(t, len(result.Errors), 0)
	})

	t.Run("checkFloors: report_only_until_reviewed produces advisory, not error", func(t *testing.T) {
		result, err := modes.CheckFloors(modes.FloorCheckParams{
			Path: "docs/readme.md", Mode: modePtr(schemas.DocModeAuto), Floors: []modes.OrgFloor{reportOnlyFloor},
		})
		require.NoError(t, err)
		assert.True(t, result.Ok)
		require.Len(t, result.Advisories, 1)
		assert.Contains(t, result.Advisories[0].Message, "manifest not yet reviewed")
		assert.Equal(t, "docs/**", result.Advisories[0].Floor.Pattern)
		assert.Equal(t, "docs/readme.md", result.Advisories[0].Path)
	})

	t.Run("checkFloors: report_only floor does not block a non-matching path", func(t *testing.T) {
		result, err := modes.CheckFloors(modes.FloorCheckParams{
			Path: "README.md", Mode: modePtr(schemas.DocModeAuto), Floors: []modes.OrgFloor{reportOnlyFloor},
		})
		require.NoError(t, err)
		assert.True(t, result.Ok)
		assert.Empty(t, result.Advisories)
	})

	t.Run("checkFloors: multiple floors — ceiling violation wins (blocking)", func(t *testing.T) {
		result, err := modes.CheckFloors(modes.FloorCheckParams{
			Path:   "docs/security/readme.md",
			Mode:   modePtr(schemas.DocModeAuto),
			Floors: []modes.OrgFloor{modeCeilingFloor, reportOnlyFloor},
		})
		require.NoError(t, err)
		assert.False(t, result.Ok)
		require.Len(t, result.Errors, 1)
		require.Len(t, result.Advisories, 1)
	})

	t.Run("enforceFloors: throws on a mode ceiling violation, not on a report-only floor", func(t *testing.T) {
		assert.Error(t, modes.EnforceFloors("docs/security/readme.md", schemas.DocModeAuto,
			[]modes.OrgFloor{modeCeilingFloor}))
		assert.NoError(t, modes.EnforceFloors("docs/readme.md", schemas.DocModeAuto,
			[]modes.OrgFloor{reportOnlyFloor}))
	})

	t.Run("composedFloorMax: returns the most restrictive matching floor maxMode", func(t *testing.T) {
		max, ok, err := modes.ComposedFloorMax([]modes.OrgFloor{
			ceiling("docs/**", schemas.DocModeAuto),
			ceiling("docs/security/**", schemas.DocModeSuggest),
		}, "docs/security/readme.md")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, schemas.DocModeSuggest, max)
	})

	t.Run("composedFloorMax: returns not-found for non-matching paths", func(t *testing.T) {
		_, ok, err := modes.ComposedFloorMax([]modes.OrgFloor{modeCeilingFloor}, "README.md")
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

// TestReviewedReportOnlyFloorsStopTheAdvisory ports the P12-T13 regression
// assertions about the human-owned `reviewed` declaration (absence is NOT a
// review — fail closed).
func TestReviewedReportOnlyFloorsStopTheAdvisory(t *testing.T) {
	reviewed := reportOnlyReviewed("docs/**", []string{"security-team"}, boolPtr(true))
	explicitlyUnreviewed := reportOnlyReviewed("docs/**", []string{"security-team"}, boolPtr(false))
	unreviewed := reportOnlyReviewed("docs/**", []string{"security-team"}, nil)

	stopped, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/readme.md", Mode: modePtr(schemas.DocModeAuto), Floors: []modes.OrgFloor{reviewed},
	})
	require.NoError(t, err)
	assert.True(t, stopped.Ok)
	assert.Empty(t, stopped.Advisories)

	persisted, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/readme.md", Mode: modePtr(schemas.DocModeAuto), Floors: []modes.OrgFloor{unreviewed},
	})
	require.NoError(t, err)
	assert.True(t, persisted.Ok)
	require.Len(t, persisted.Advisories, 1)
	assert.Contains(t, persisted.Advisories[0].Message, "manifest not yet reviewed")

	explicitFalse, err := modes.CheckFloors(modes.FloorCheckParams{
		Path: "docs/readme.md", Mode: modePtr(schemas.DocModeAuto), Floors: []modes.OrgFloor{explicitlyUnreviewed},
	})
	require.NoError(t, err)
	assert.True(t, explicitFalse.Ok)
	assert.Len(t, explicitFalse.Advisories, 1)
}

func TestEnforceFloorsNeverThrowsForAdvisories(t *testing.T) {
	assert.NoError(t, modes.EnforceFloors("docs/readme.md", schemas.DocModeAuto,
		[]modes.OrgFloor{reportOnlyReviewed("docs/**", []string{"security-team"}, boolPtr(true))}))
	assert.NoError(t, modes.EnforceFloors("docs/readme.md", schemas.DocModeAuto,
		[]modes.OrgFloor{reportOnlyReviewed("docs/**", []string{"security-team"}, boolPtr(false))}))
}

// TestFloorPatternsAreWellFormedAndMatchThemselves ports the testing-kit
// property: every generated floor pattern is valid per
// AssertValidFloorPattern and matches itself (deterministic enumeration over
// the same pattern domain the TS fast-check floorArb drew from).
func TestFloorPatternsAreWellFormedAndMatchThemselves(t *testing.T) {
	patterns := []string{
		"docs/**",
		"docs/security/**",
		"docs",
		"src/**",
		"README.md",
		"**",
		"docs/security/auth.md",
		"docs/security/nested/deep.md",
		"docs/overview.md",
		"src/auth/login.ts",
		"docs/security",
		"docs/internal/**",
		"evaluation/**",
		"memory/**",
		"policy-history/**",
		"state/**",
		"docs/p12t17/**",
	}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			require.NoError(t, modes.AssertValidFloorPattern(pattern))
			matches, err := modes.FloorMatchesPath(pattern, pattern)
			require.NoError(t, err)
			assert.True(t, matches)
		})
	}
}
