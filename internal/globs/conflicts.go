package globs

import (
	"sort"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// §11.3 overlap resolution (TS conflicts.ts; P0-T10): when several manifest
// entries' `sources` match the same changed file, the most specific glob wins;
// on a specificity tie the entry with the MORE RESTRICTIVE mode wins
// (fail-safe); between equally-restrictive entries the document routes to BOTH
// candidates.
//
// RESTRICTIVENESS is the local mirror of the modes/ MODE_LADDER owned by the
// P0-T05 wave — intentionally NOT imported from there (module boundary); keep
// the two in sync by review. Higher = more restrictive:
// locked(3) and generated(3) beat human(2) and suggest(2) beat auto(1).
// This is D2 in action: `locked` (and `generated`) resolve `blocked` at ANY
// confidence, so a locked entry can never lose an overlap to a permissive one.
//
// ARCH §11.2 also names an `external` ownership, but the manifest schema (the
// single source of truth, @docsync/schemas) has no `external` DocMode value —
// externally regenerated docs are modeled as `generated`; hence no external
// rank here (n/a).

// RESTRICTIVENESS is the restrictiveness rank per DocMode; higher wins ties
// (TS RESTRICTIVENESS).
var RESTRICTIVENESS = map[schemas.DocMode]int{
	schemas.DocModeAuto:      1,
	schemas.DocModeSuggest:   2,
	schemas.DocModeLocked:    3,
	schemas.DocModeGenerated: 3,
	schemas.DocModeHuman:     2,
}

// unranked is the rank of a match whose mode cannot be resolved — it loses to
// any defined mode (TS UNRANKED).
const unranked = -1

// TieKind classifies how an overlap resolved (TS
// CandidateResolution['tieKind']).
type TieKind string

const (
	// TieUnique: no tie — a single top-specificity candidate wins.
	TieUnique TieKind = "unique"
	// TieRestrictiveTie: specificity tie broken by the more restrictive mode.
	TieRestrictiveTie TieKind = "restrictive-tie"
	// TieEqualTie: equally-restrictive candidates — the document routes to
	// BOTH (§11.3).
	TieEqualTie TieKind = "equal-tie"
)

// OverlapMatch is one manifest (entryIndex, pattern) hit with an optional
// mode override (the TS inline type of resolveOverlap's matches parameter).
type OverlapMatch struct {
	EntryIndex int
	Pattern    string
	// Mode, when non-nil, overrides ModeOf for this match.
	Mode *schemas.DocMode
}

// CandidateWinner is one winning candidate (TS
// CandidateResolution['winners'][number]).
type CandidateWinner struct {
	EntryIndex int              `json:"entryIndex"`
	Pattern    string           `json:"pattern"`
	Mode       *schemas.DocMode `json:"mode,omitempty"`
}

// CandidateResolution is the routing decision for one changed path (TS
// CandidateResolution).
type CandidateResolution struct {
	Path    string            `json:"path"`
	Winners []CandidateWinner `json:"winners"`
	TieKind TieKind           `json:"tieKind"`
}

// ResolveOverlapParams carries the inputs of ResolveOverlap (the TS inline
// params object).
type ResolveOverlapParams struct {
	Path    string
	Matches []OverlapMatch
	// ModeOf resolves the mode of a manifest entry index (the TS modeOf
	// callback); nil means no mode is ever resolvable.
	ModeOf func(entryIndex int) *schemas.DocMode
}

// ResolveOverlap resolves which documents a changed path routes to.
//
//   - No matches → empty winners, tieKind "unique".
//   - Single top-specificity candidate → tieKind "unique".
//   - Specificity tie → the candidate with the highest restrictiveness rank
//     wins (tieKind "restrictive-tie"); a candidate without a resolvable mode
//     (nil) loses to any defined mode.
//   - Specificity tie with equally-restrictive candidates (or ≥2 candidates
//     all without a resolvable mode) → tieKind "equal-tie" and BOTH candidates
//     are returned as winners.
//
// Mode resolution: an explicit per-match Mode wins; otherwise ModeOf is
// consulted with the entry index; nil stays unranked. Winners are ordered
// deterministically by ascending entryIndex (manifest order). This function is
// pure — guard enforcement stays with CompileGlob; it scores patterns as
// written via ScoreGlob (metric ties stay ties per §11.3).
func ResolveOverlap(params ResolveOverlapParams) CandidateResolution {
	if len(params.Matches) == 0 {
		return CandidateResolution{Path: params.Path, Winners: []CandidateWinner{}, TieKind: TieUnique}
	}
	type candidate struct {
		entryIndex  int
		pattern     string
		mode        *schemas.DocMode
		rank        int
		specificity GlobSpecificity
	}
	candidates := make([]candidate, 0, len(params.Matches))
	for _, match := range params.Matches {
		mode := match.Mode
		if mode == nil && params.ModeOf != nil {
			mode = params.ModeOf(match.EntryIndex)
		}
		rank := unranked
		if mode != nil {
			rank = RESTRICTIVENESS[*mode]
		}
		candidates = append(candidates, candidate{
			entryIndex:  match.EntryIndex,
			pattern:     match.Pattern,
			mode:        mode,
			rank:        rank,
			specificity: ScoreGlob(match.Pattern),
		})
	}

	top := candidates[0]
	for _, c := range candidates[1:] {
		if CompareSpecificity(c.specificity, top.specificity) > 0 {
			top = c
		}
	}

	var topGroup []candidate
	for _, c := range candidates {
		if CompareSpecificity(c.specificity, top.specificity) == 0 {
			topGroup = append(topGroup, c)
		}
	}

	maxRank := unranked
	for _, c := range topGroup {
		if c.rank > maxRank {
			maxRank = c.rank
		}
	}
	var rankWinners []candidate
	for _, c := range topGroup {
		if c.rank == maxRank {
			rankWinners = append(rankWinners, c)
		}
	}

	tieKind := TieEqualTie
	switch {
	case len(topGroup) == 1:
		tieKind = TieUnique
	case len(rankWinners) == 1:
		tieKind = TieRestrictiveTie
	}

	sort.SliceStable(rankWinners, func(i, j int) bool {
		return rankWinners[i].entryIndex < rankWinners[j].entryIndex
	})
	winners := make([]CandidateWinner, 0, len(rankWinners))
	for _, c := range rankWinners {
		winners = append(winners, CandidateWinner{
			EntryIndex: c.entryIndex,
			Pattern:    c.pattern,
			Mode:       c.mode,
		})
	}
	return CandidateResolution{Path: params.Path, Winners: winners, TieKind: tieKind}
}
