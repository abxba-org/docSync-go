package globs_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
)

func modeOf(m schemas.DocMode) *schemas.DocMode { return &m }

func TestRestrictivenessLadder(t *testing.T) {
	// TS RESTRICTIVENESS — the local mirror of the modes/ MODE_LADDER:
	// locked(3) and generated(3) beat human(2) and suggest(2) beat auto(1).
	// D2: ownership is the floor, so a locked entry wins at any confidence.
	assert.Equal(t, 3, globs.RESTRICTIVENESS[schemas.DocModeLocked])
	assert.Equal(t, 3, globs.RESTRICTIVENESS[schemas.DocModeGenerated])
	assert.Equal(t, 2, globs.RESTRICTIVENESS[schemas.DocModeHuman])
	assert.Equal(t, 2, globs.RESTRICTIVENESS[schemas.DocModeSuggest])
	assert.Equal(t, 1, globs.RESTRICTIVENESS[schemas.DocModeAuto])
	assert.Greater(t, globs.RESTRICTIVENESS[schemas.DocModeLocked], globs.RESTRICTIVENESS[schemas.DocModeSuggest])
	assert.Greater(t, globs.RESTRICTIVENESS[schemas.DocModeLocked], globs.RESTRICTIVENESS[schemas.DocModeAuto])
	assert.Greater(t, globs.RESTRICTIVENESS[schemas.DocModeGenerated], globs.RESTRICTIVENESS[schemas.DocModeHuman])
	assert.Greater(t, globs.RESTRICTIVENESS[schemas.DocModeHuman], globs.RESTRICTIVENESS[schemas.DocModeAuto])
	assert.Len(t, globs.RESTRICTIVENESS, len(schemas.DocModeValues))
}

func TestResolveOverlap_MoreSpecificGlobWins(t *testing.T) {
	// TS V-P0-10: more specific glob wins for a changed file under the nested
	// source.
	matches := []globs.OverlapMatch{
		{EntryIndex: 0, Pattern: "src/payments/**"},
		{EntryIndex: 1, Pattern: "src/payments/refunds/**"},
	}
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path:    "src/payments/refunds/refund.ts",
		Matches: matches,
	})
	assert.Equal(t, globs.TieUnique, resolution.TieKind)
	assert.Equal(t, []globs.CandidateWinner{
		{EntryIndex: 1, Pattern: "src/payments/refunds/**", Mode: nil},
	}, resolution.Winners)
	assert.Equal(t, "src/payments/refunds/refund.ts", resolution.Path)
}

func TestResolveOverlap_EqualSpecificityEqualRestrictivenessBothCandidates(t *testing.T) {
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/a/x.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/a/**"},
			{EntryIndex: 1, Pattern: "src/b/**"},
		},
	})
	assert.Equal(t, globs.TieEqualTie, resolution.TieKind)
	assert.Equal(t, []globs.CandidateWinner{
		{EntryIndex: 0, Pattern: "src/a/**", Mode: nil},
		{EntryIndex: 1, Pattern: "src/b/**", Mode: nil},
	}, resolution.Winners)
}

func TestResolveOverlap_SpecificityTieMoreRestrictiveModeWins(t *testing.T) {
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/pay/billing.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/pay/**", Mode: modeOf(schemas.DocModeSuggest)},
			{EntryIndex: 1, Pattern: "src/pay/**", Mode: modeOf(schemas.DocModeAuto)},
		},
	})
	assert.Equal(t, globs.TieRestrictiveTie, resolution.TieKind)
	assert.Equal(t, []globs.CandidateWinner{
		{EntryIndex: 0, Pattern: "src/pay/**", Mode: modeOf(schemas.DocModeSuggest)},
	}, resolution.Winners)
}

func TestResolveOverlap_LockedBeatsSuggest(t *testing.T) {
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/x/a.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeSuggest)},
			{EntryIndex: 1, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeLocked)},
		},
	})
	assert.Equal(t, globs.TieRestrictiveTie, resolution.TieKind)
	assert.Equal(t, []globs.CandidateWinner{
		{EntryIndex: 1, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeLocked)},
	}, resolution.Winners)
}

func TestResolveOverlap_EquallyRestrictiveModesTieToBothCandidates(t *testing.T) {
	// locked(3) and generated(3) share the top rank → equal-tie → both.
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/x/a.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeGenerated)},
			{EntryIndex: 1, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeLocked)},
		},
	})
	assert.Equal(t, globs.TieEqualTie, resolution.TieKind)
	assert.Len(t, resolution.Winners, 2)
	// human(2) and suggest(2) share a rank too → both.
	resolution = globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/x/a.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeHuman)},
			{EntryIndex: 1, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeSuggest)},
		},
	})
	assert.Equal(t, globs.TieEqualTie, resolution.TieKind)
	assert.Len(t, resolution.Winners, 2)
}

func TestResolveOverlap_ModeOfSuppliesModes_ModelessLoses(t *testing.T) {
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/x/a.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/*/a.ts"},
			{EntryIndex: 1, Pattern: "src/*/a.ts"},
		},
		ModeOf: func(index int) *schemas.DocMode {
			if index == 0 {
				return nil
			}
			return modeOf(schemas.DocModeLocked)
		},
	})
	assert.Equal(t, globs.TieRestrictiveTie, resolution.TieKind)
	assert.Equal(t, []globs.CandidateWinner{
		{EntryIndex: 1, Pattern: "src/*/a.ts", Mode: modeOf(schemas.DocModeLocked)},
	}, resolution.Winners)
}

func TestResolveOverlap_TwoModelessCandidatesTieAsEqualTie(t *testing.T) {
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/x/a.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/*/a.ts"},
			{EntryIndex: 1, Pattern: "src/*/a.ts"},
		},
		ModeOf: func(int) *schemas.DocMode { return nil },
	})
	assert.Equal(t, globs.TieEqualTie, resolution.TieKind)
	winners := make([]int, 0, len(resolution.Winners))
	for _, w := range resolution.Winners {
		winners = append(winners, w.EntryIndex)
	}
	assert.Equal(t, []int{0, 1}, winners)
}

func TestResolveOverlap_NoMatchesNoWinners(t *testing.T) {
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path:    "src/a.ts",
		Matches: nil,
	})
	assert.Equal(t, globs.CandidateResolution{
		Path:    "src/a.ts",
		Winners: []globs.CandidateWinner{},
		TieKind: globs.TieUnique,
	}, resolution)
}

func TestResolveOverlap_WinnersOrderedByEntryIndex(t *testing.T) {
	// Equal-tie winners are ordered by ascending entryIndex (manifest order),
	// regardless of input order.
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/a/x.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 7, Pattern: "src/b/**"},
			{EntryIndex: 2, Pattern: "src/a/**"},
			{EntryIndex: 5, Pattern: "src/c/**"},
		},
	})
	assert.Equal(t, globs.TieEqualTie, resolution.TieKind)
	indices := make([]int, 0, len(resolution.Winners))
	for _, w := range resolution.Winners {
		indices = append(indices, w.EntryIndex)
	}
	assert.Equal(t, []int{2, 5, 7}, indices)
}

func TestResolveOverlap_PerMatchModeOverridesModeOf(t *testing.T) {
	// An explicit per-match mode wins over ModeOf (TS: match.mode ?? modeOf?.(...)).
	resolution := globs.ResolveOverlap(globs.ResolveOverlapParams{
		Path: "src/x/a.ts",
		Matches: []globs.OverlapMatch{
			{EntryIndex: 0, Pattern: "src/x/**"},
			{EntryIndex: 1, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeLocked)},
		},
		ModeOf: func(int) *schemas.DocMode { return modeOf(schemas.DocModeAuto) },
	})
	assert.Equal(t, globs.TieRestrictiveTie, resolution.TieKind)
	assert.Equal(t, []globs.CandidateWinner{
		{EntryIndex: 1, Pattern: "src/x/**", Mode: modeOf(schemas.DocModeLocked)},
	}, resolution.Winners)
}
