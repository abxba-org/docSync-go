package patch

// Ported from core/src/patch/minimality.ts — minimality measurement (P3-T12;
// ARCH §4.9, V-P3-6): the ACTUAL line-level change between the current document
// and the patched result, MEASURED — never trusted from the writer's
// self-reported patch.minimality — and exposed to the verifier, which owns the
// hard rejection (Phase 4 Tier-1).
//
// Measurement basis mirrors the whole-document-rewrite detector
// (schemas/v1/doc-patch.go): trimmed NON-EMPTY lines as a multiset, so
// whitespace-only churn never inflates the counts and duplicated lines are
// handled symmetrically. added_lines = Σ max(0, patched−current) per line key;
// removed_lines = Σ max(0, current−patched).
//
// Ratio (vs the triggering diff): (added + removed) / max(1, triggerAdded +
// triggerRemoved). Oversized = ratio strictly beyond the bound — the
// DefaultMinimalityMaxRatio constant is LABELED BOOTSTRAP DATA (V-P3-6): an
// oversized patch is FLAGGED for verifier rejection here, never auto-rejected at
// the application layer; the hard rejection is Phase 4 Tier-1. The bound is
// never hard-coded at call sites — consume the exported constant.

import (
	"math"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DefaultMinimalityMaxRatio (TS: DEFAULT_MINIMALITY_MAX_RATIO) is the bootstrap
// bound on (patch churn / triggering-diff churn) beyond which a patch is flagged
// oversized for the verifier (V-P3-6). LABELED bootstrap data — calibration-
// class, not architecture; never hard-code it elsewhere.
const DefaultMinimalityMaxRatio = 20.0

// MinimalityMeasurement (TS: MinimalityMeasurement) is the measured minimality +
// verifier-facing ratio/flag (P3-T12, V-P3-6).
type MinimalityMeasurement struct {
	// Minimality is the schema-shaped measurement; Measured is always true here
	// (it IS measured).
	Minimality schemas.DocPatchMinimality
	// Ratio is patch churn / triggering-diff churn; nil when no triggeringDiffStats
	// were supplied (not measurable against a trigger — flagged neither way).
	Ratio *float64
	// Oversized is true iff Ratio !== nil and *Ratio > maxRatio — a FLAG for the
	// verifier.
	Oversized bool
}

// DiffStats is a triggering code-diff size for the ratio.
type DiffStats struct {
	Added   int
	Removed int
}

// MeasureMinimalityInput (TS: MeasureMinimalityInput) carries the measurement
// inputs. Patch is accepted for signature parity with the TS API but is NOT
// read (the application measures; the writer's self-report is ignored).
type MeasureMinimalityInput struct {
	Patch          *schemas.DocPatch
	CurrentContent string
	PatchedContent string
	// TriggeringCodeDiff, omitted (nil), yields a nil ratio and oversized false.
	TriggeringDiffStats *DiffStats
	// MaxRatio overrides the oversized bound; nil uses DefaultMinimalityMaxRatio.
	MaxRatio *float64
}

// lineCounts is the multiset of trimmed non-empty lines (mirrors the
// rewrite-detector basis).
func lineCounts(content string) map[string]int {
	counts := make(map[string]int)
	for _, line := range strings.Split(content, "\n") {
		key := strings.TrimSpace(line)
		if len(key) == 0 {
			continue
		}
		counts[key]++
	}
	return counts
}

// multisetDeltaCounts is the symmetric delta: per line key, the surplus on each
// side sums to removed (current) / added (patched).
func multisetDeltaCounts(current, patched map[string]int) (added, removed int) {
	for key, count := range current {
		if other := patched[key]; count > other {
			removed += count - other
		}
	}
	for key, count := range patched {
		if other := current[key]; count > other {
			added += count - other
		}
	}
	return added, removed
}

// MeasureMinimality (TS: measureMinimality) measures the actual minimality of a
// patch's content change (P3-T12). Pure: no I/O, no clock. Invalid bounds/stats
// are caller configuration errors and surface as a docutil coded error
// (CodeConfig) in place of the TS TypeError.
func MeasureMinimality(input MeasureMinimalityInput) (MinimalityMeasurement, error) {
	addedLines, removedLines := multisetDeltaCounts(
		lineCounts(input.CurrentContent),
		lineCounts(input.PatchedContent),
	)

	maxRatio := DefaultMinimalityMaxRatio
	if input.MaxRatio != nil {
		maxRatio = *input.MaxRatio
	}
	if math.IsNaN(maxRatio) || math.IsInf(maxRatio, 0) || maxRatio <= 0 {
		return MinimalityMeasurement{}, docutil.ConfigError(
			"maxRatio must be a positive finite number, got %s",
			strconv.FormatFloat(maxRatio, 'g', -1, 64))
	}

	stats := input.TriggeringDiffStats
	if stats != nil {
		if stats.Added < 0 || stats.Removed < 0 {
			return MinimalityMeasurement{}, docutil.ConfigError(
				`triggeringDiffStats must be non-negative integers, got {"added":%d,"removed":%d}`,
				stats.Added, stats.Removed)
		}
	}

	var ratio *float64
	if stats != nil {
		r := float64(addedLines+removedLines) / math.Max(1, float64(stats.Added+stats.Removed))
		ratio = &r
	}

	return MinimalityMeasurement{
		Minimality: schemas.DocPatchMinimality{
			Measured:     true,
			AddedLines:   addedLines,
			RemovedLines: removedLines,
		},
		Ratio:     ratio,
		Oversized: ratio != nil && *ratio > maxRatio,
	}, nil
}

// MeasuredMinimality (TS: measuredMinimality) is convenience for callers that
// only need the schema-shaped measurement of a patch whose application already
// produced patchedContent (e.g. recording into run records). Ratio/oversized
// require MeasureMinimality.
func MeasuredMinimality(currentContent, patchedContent string) schemas.DocPatchMinimality {
	addedLines, removedLines := multisetDeltaCounts(
		lineCounts(currentContent),
		lineCounts(patchedContent),
	)
	return schemas.DocPatchMinimality{
		Measured:     true,
		AddedLines:   addedLines,
		RemovedLines: removedLines,
	}
}
