package patch

// Ported from core/src/patch/merge.ts — deterministic line-level 3-way merge
// (P3-T10; ARCH §4.9, §12.3, A9).
//
// Classic diff3 over base/ours/theirs: each side is diffed against the base with
// an LCS line diff (no dependencies — implemented here, deterministic), then the
// two change scripts are walked together over base coordinates:
//
//   - a base region changed by NEITHER side → base lines;
//   - changed by exactly ONE side → that side's lines;
//   - changed by BOTH sides with identical replacement content → taken once;
//   - changed by BOTH sides over OVERLAPPING base lines with different content →
//     CONFLICT — the merged result is untrustworthy, so the caller (the applier)
//     abstains to REVIEW instead of shipping it (ARCH §4.9: races abstain; a
//     3-way merge ships only on a re-verified pass).
//
// Overlap is STRICT: adjacent changes and changes touching only a boundary share
// no base line and merge independently (both sides inserting at the SAME point is
// the exception — the order would be ambiguous, so it is a conflict unless the
// inserted content is identical). Determinism: same three inputs → the same
// merged text or the same conflict records, always (no randomness, no locale, no
// wall clock; stable iteration orders; LCS ties broken deterministically toward
// base deletions). Pure: no I/O.
//
// Trailing-newline convention: the merged text ends with '\n' iff OURS (the live
// document) ends with one — the current document's convention wins. Line
// splitting mirrors claims/anchor.go (claims.SplitDocLines).

import (
	"strings"

	"github.com/DUDE616/docSync-go/internal/claims"
)

// MaxLcsCells (TS: MAX_LCS_CELLS) bounds the LCS diff table (O(n·m) cells);
// beyond the budget a side diff degrades deterministically to a whole-region
// replacement of the differing middle (fail closed). Injectable per call via
// ThreeWayMergeInput.MaxLcsCells (tests pass a small override to exercise the
// degradation path cheaply); the default is the architectural constant.
const MaxLcsCells = 10_000_000

// ThreeWayMergeInput (TS: ThreeWayMergeInput) carries base/ours/theirs content
// plus the injectable LCS cell-budget override (nil → MaxLcsCells).
type ThreeWayMergeInput struct {
	// Base is the content both sides started from (the writer's read snapshot).
	Base string
	// Ours is the live document content at apply time.
	Ours string
	// Theirs is the writer's intended result (base with the patch applied).
	Theirs string
	// MaxLcsCells overrides the cell budget (test seam for the degradation path).
	MaxLcsCells *int
}

// MergeConflict (TS: MergeConflict) is one conflicting region: the 1-based base
// line where it starts plus each side's view.
type MergeConflict struct {
	Line       int
	BaseText   string
	OursText   string
	TheirsText string
}

// ThreeWayMergeResult (TS: ThreeWayMergeResult) is the closed outcome union
// rendered as a tagged struct: Ok true → Merged; Ok false → Conflicts.
type ThreeWayMergeResult struct {
	Ok        bool
	Merged    string
	Conflicts []MergeConflict
}

// Edit replaces base[From, To) (0-based, To exclusive) with Lines; a pure
// insertion has From === To.
type Edit struct {
	From  int
	To    int
	Lines []string
}

// diffEdits is the LCS-based diff of side against base as an ordered, disjoint
// edit list. Indices are ABSOLUTE (into the full arrays); a pure insertion is an
// edit with From === To ('insert before line From'). Pure function.
func diffEdits(base, side []string, maxLcsCells int) []Edit {
	// Trim the common prefix/suffix first — huge practical win, zero ambiguity.
	prefix := 0
	for prefix < len(base) && prefix < len(side) && base[prefix] == side[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(base)-prefix && suffix < len(side)-prefix &&
		base[len(base)-1-suffix] == side[len(side)-1-suffix] {
		suffix++
	}
	midBase := base[prefix : len(base)-suffix]
	midSide := side[prefix : len(side)-suffix]
	if len(midBase) == 0 && len(midSide) == 0 {
		return nil
	}

	if len(midBase)*len(midSide) > maxLcsCells {
		if len(midBase) == 0 {
			return []Edit{{From: prefix, To: prefix, Lines: midSide}}
		}
		return []Edit{{From: prefix, To: len(base) - suffix, Lines: midSide}}
	}

	// LCS length table: table[i*cols + j] = LCS length of midBase[i..] / midSide[j..].
	rows := len(midBase) + 1
	cols := len(midSide) + 1
	table := make([]uint32, rows*cols)
	for i := len(midBase) - 1; i >= 0; i-- {
		for j := len(midSide) - 1; j >= 0; j-- {
			if midBase[i] == midSide[j] {
				table[i*cols+j] = table[(i+1)*cols+(j+1)] + 1
			} else {
				table[i*cols+j] = max(table[(i+1)*cols+j], table[i*cols+(j+1)])
			}
		}
	}

	var edits []Edit
	i, j := 0, 0
	runFrom := -1    // absolute base index where the run began; -1 = no open run
	runSideFrom := 0 // absolute side index where the run began
	closeRun := func(baseAbsTo, sideAbsTo int) {
		if runFrom == -1 {
			return
		}
		edits = append(edits, Edit{
			From:  runFrom,
			To:    baseAbsTo,
			Lines: side[runSideFrom:sideAbsTo],
		})
		runFrom = -1
	}
	for i < len(midBase) && j < len(midSide) {
		if midBase[i] == midSide[j] {
			closeRun(prefix+i, prefix+j)
			i++
			j++
			continue
		}
		if runFrom == -1 {
			runFrom = prefix + i
			runSideFrom = prefix + j
		}
		// Deterministic tie-break: prefer advancing the base cursor (deletion).
		if table[(i+1)*cols+j] >= table[i*cols+(j+1)] {
			i++
		} else {
			j++
		}
	}
	hadOpenRun := runFrom != -1
	closeRun(prefix+i, len(side)-suffix)
	// Exactly one tail can remain (the loop exited on that side's exhaustion).
	// NOTE: the final closeRun extends an open run's side range through any
	// unconsumed side lines (prefix + j .. len(side)-suffix) when the base was
	// exhausted first — otherwise one replacement would fragment into a deletion +
	// boundary insertion, which diff3 would treat as two change regions. A
	// run-less remaining side slice is a pure end insertion.
	if i < len(midBase) {
		// Unconsumed base lines with the side exhausted → pure deletion.
		edits = append(edits, Edit{From: prefix + i, To: prefix + len(midBase), Lines: nil})
	} else if j < len(midSide) && !hadOpenRun {
		// Unconsumed side lines with NO open run → pure insertion at the end of
		// the base (the run-open case was already folded in by the closeRun).
		edits = append(edits, Edit{
			From:  prefix + len(midBase),
			To:    prefix + len(midBase),
			Lines: side[prefix+j : len(side)-suffix],
		})
	}
	return edits
}

// sideContentForRegion returns one side's content for the base region
// [from, to): its edits inside the region applied, base lines elsewhere. `edits`
// MUST be the disjoint, ordered edits known to intersect the region (the merge
// walk's consumed range) — replacements satisfy from <= from <= to; pure
// insertions satisfy from === to with from inside [from, to].
func sideContentForRegion(edits []Edit, base []string, from, to int) []string {
	content := make([]string, 0, to-from)
	cursor := from
	for _, edit := range edits {
		if edit.From > to {
			break
		}
		for cursor < min(edit.From, to) {
			content = append(content, base[cursor])
			cursor++
		}
		content = append(content, edit.Lines...)
		cursor = max(cursor, edit.To)
	}
	for cursor < to {
		content = append(content, base[cursor])
		cursor++
	}
	return content
}

// ThreeWayMerge (TS: threeWayMerge) merges ours/theirs over base (see module
// doc). Pure and deterministic.
func ThreeWayMerge(input ThreeWayMergeInput) ThreeWayMergeResult {
	base := claims.SplitDocLines(input.Base)
	ours := claims.SplitDocLines(input.Ours)
	theirs := claims.SplitDocLines(input.Theirs)
	maxLcsCells := MaxLcsCells
	if input.MaxLcsCells != nil {
		maxLcsCells = *input.MaxLcsCells
	}

	oursEdits := diffEdits(base, ours, maxLcsCells)
	theirsEdits := diffEdits(base, theirs, maxLcsCells)

	merged := make([]string, 0, len(base))
	var conflicts []MergeConflict

	oi, ti, pos := 0, 0, 0
	for pos < len(base) || oi < len(oursEdits) || ti < len(theirsEdits) {
		nextOursFrom := len(base)
		if oi < len(oursEdits) {
			nextOursFrom = oursEdits[oi].From
		}
		nextTheirsFrom := len(base)
		if ti < len(theirsEdits) {
			nextTheirsFrom = theirsEdits[ti].From
		}
		nextEvent := min(min(nextOursFrom, nextTheirsFrom), len(base))

		// Unchanged gap — identical on both sides by construction of nextEvent.
		for pos < nextEvent {
			merged = append(merged, base[pos])
			pos++
		}

		// Start the change group. When BOTH sides have an edit beginning at pos
		// and exactly one of them is a pure insertion at that boundary while the
		// other is a replacement, the insertion forms its OWN group first — it
		// shares no base line with the replacement, so the two merge in order
		// (insertion output precedes the replaced region). Otherwise every edit
		// beginning at pos starts one group together. Remaining edits all have
		// From >= pos.
		var oursStart, theirsStart *Edit
		if oi < len(oursEdits) && oursEdits[oi].From <= pos {
			oursStart = &oursEdits[oi]
		}
		if ti < len(theirsEdits) && theirsEdits[ti].From <= pos {
			theirsStart = &theirsEdits[ti]
		}
		oursGroupStart := oi
		theirsGroupStart := ti
		groupEnd := pos
		oursStartIsBoundaryInsertion := oursStart != nil && oursStart.From == oursStart.To
		theirsStartIsBoundaryInsertion := theirsStart != nil && theirsStart.From == theirsStart.To
		oursGoesAlone := oursStart != nil &&
			oursStartIsBoundaryInsertion &&
			theirsStart != nil &&
			!theirsStartIsBoundaryInsertion
		theirsGoesAlone := theirsStart != nil &&
			theirsStartIsBoundaryInsertion &&
			oursStart != nil &&
			!oursStartIsBoundaryInsertion
		if oursStart != nil && (oursGoesAlone || !theirsGoesAlone) {
			groupEnd = max(groupEnd, oursStart.To)
			oi++
		}
		if theirsStart != nil && (theirsGoesAlone || !oursGoesAlone) {
			groupEnd = max(groupEnd, theirsStart.To)
			ti++
		}
		expanded := true
		for expanded {
			expanded = false
			if oi < len(oursEdits) {
				edit := &oursEdits[oi]
				joins := false
				if edit.From == edit.To {
					joins = pos < edit.From && edit.From < groupEnd
				} else {
					joins = edit.From < groupEnd && edit.To > pos
				}
				if joins {
					groupEnd = max(groupEnd, edit.To)
					oi++
					expanded = true
				}
			}
			if ti < len(theirsEdits) {
				edit := &theirsEdits[ti]
				joins := false
				if edit.From == edit.To {
					joins = pos < edit.From && edit.From < groupEnd
				} else {
					joins = edit.From < groupEnd && edit.To > pos
				}
				if joins {
					groupEnd = max(groupEnd, edit.To)
					ti++
					expanded = true
				}
			}
		}
		hasOurs := oi > oursGroupStart
		hasTheirs := ti > theirsGroupStart
		if !hasOurs && !hasTheirs {
			// Nothing starts at pos and the gap copy exhausted the base — done.
			break
		}

		if hasOurs && hasTheirs {
			oursContent := sideContentForRegion(oursEdits[oursGroupStart:oi], base, pos, groupEnd)
			theirsContent := sideContentForRegion(theirsEdits[theirsGroupStart:ti], base, pos, groupEnd)
			identical := len(oursContent) == len(theirsContent)
			if identical {
				for idx := range oursContent {
					if oursContent[idx] != theirsContent[idx] {
						identical = false
						break
					}
				}
			}
			if identical {
				merged = append(merged, oursContent...)
			} else {
				conflicts = append(conflicts, MergeConflict{
					Line:       pos + 1,
					BaseText:   strings.Join(base[pos:groupEnd], "\n"),
					OursText:   strings.Join(oursContent, "\n"),
					TheirsText: strings.Join(theirsContent, "\n"),
				})
			}
		} else if hasOurs {
			merged = append(merged, sideContentForRegion(oursEdits[oursGroupStart:oi], base, pos, groupEnd)...)
		} else {
			merged = append(merged, sideContentForRegion(theirsEdits[theirsGroupStart:ti], base, pos, groupEnd)...)
		}

		pos = groupEnd
	}

	if len(conflicts) > 0 {
		return ThreeWayMergeResult{Ok: false, Conflicts: conflicts}
	}

	// Empty MERGE (no lines at all) is '' — not the single-empty-line case
	// (merged = [''], joined === ''), which must round-trip through the
	// trailing-newline convention.
	if len(merged) == 0 {
		return ThreeWayMergeResult{Ok: true, Merged: ""}
	}
	joined := strings.Join(merged, "\n")
	if strings.HasSuffix(input.Ours, "\n") {
		joined += "\n"
	}
	return ThreeWayMergeResult{Ok: true, Merged: joined}
}
