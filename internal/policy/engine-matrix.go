// Ported from packages/policy/src/engine/matrix.ts — the fixed confidence ×
// risk matrix (P2-T18; ARCH §5.3, §16.2 R11 context), the engine-side half of
// the D11 boundary. BINDING SPLIT: the matrix SHAPE is architecture and lives
// here as fixed code; bucket BOUNDARIES (`high_confidence` / `review` /
// `calibration_floor`) are per-policy-version calibrated DATA carried by the
// compiled Jev policy's Routing (schema.ts) and are only CONSUMED here, never
// defined. The matrix's shape is fixed; its calibration is evolvable per
// repository (ARCH §5.3).
//
// FILE NOTE: this file is named engine-matrix.go (not matrix.go) because the
// shadow unit's port of packages/policy/src/shadow/matrix.ts already owns
// internal/policy/matrix.go — one writer per file, both ports preserved.
//
// Shape invariants (ARCH §5.3, structural read):
//   - automation (`auto`) exists in exactly one cell (low risk × high
//     confidence);
//   - the diagonal is `review`;
//   - anything left of the diagonal is human-gated or `block`;
//   - high-risk rows never produce `auto` at any confidence level (V-P2-8).
//
// Risk is the conservative max (ARCH §5.3): the manifest-declared risk class
// (`risk_class_overrides`, glob → class, D2/D11 input data) versus Jev's `risk`
// choice — whichever is higher wins. The caller may either pass the
// pre-resolved winning override class (ManifestRisk) or the raw override table
// plus the doc path and an injected glob matcher; overlapping matching
// overrides compose to the HIGHEST class (most restrictive, §11.3 spirit).
//
// D2 ownership floor: `locked` / `generated` / `external` documents resolve to
// `blocked` at ANY confidence; the engine has no path to override ownership —
// the floor is applied AFTER matrix evaluation (ApplyOwnershipFloor) and takes
// no confidence, threshold or policy input by type.
package policy

import (
	"math"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// RiskBucket is the risk axis buckets — the closed risk set (ARCH §5.3 rows).
// TS: `export type RiskBucket = RiskLevel` (an alias, kept as a Go alias so
// schemas.RiskLevel values are directly usable).
type RiskBucket = schemas.RiskLevel

// ConfidenceBucket is the confidence axis bucket derived from the policy's
// calibrated thresholds (D11). TS: 'low' | 'medium' | 'high'.
type ConfidenceBucket string

// ConfidenceBucket values.
const (
	ConfidenceBucketLow    ConfidenceBucket = "low"
	ConfidenceBucketMedium ConfidenceBucket = "medium"
	ConfidenceBucketHigh   ConfidenceBucket = "high"
)

// MatrixCell is the §5.3 cell vocabulary — note `block` (the table) versus
// `blocked` (the outcome set of outcome.go): ApplyOwnershipFloor is the single
// normalization point, so downstream code never sees the raw table cell.
type MatrixCell string

// MatrixCell values.
const (
	MatrixCellAuto    MatrixCell = "auto"
	MatrixCellSuggest MatrixCell = "suggest"
	MatrixCellReview  MatrixCell = "review"
	MatrixCellBlock   MatrixCell = "block"
)

// ResolvedCell is the cell after the ownership floor: the §5.3 vocabulary with
// outcome-compatible `blocked`.
type ResolvedCell string

// ResolvedCell values.
const (
	ResolvedCellAuto    ResolvedCell = "auto"
	ResolvedCellSuggest ResolvedCell = "suggest"
	ResolvedCellReview  ResolvedCell = "review"
	ResolvedCellBlocked ResolvedCell = "blocked"
)

// MATRIX is the FIXED matrix (ARCH §5.3 verbatim): rows = risk buckets
// low/medium/high, columns = confidence buckets low/medium/high. This table is
// architecture code, NOT policy data — policies calibrate the bucket
// boundaries the columns hang off, never the table itself.
var MATRIX = map[RiskBucket]map[ConfidenceBucket]MatrixCell{
	schemas.RiskLevelLow: {
		ConfidenceBucketLow:    MatrixCellReview,
		ConfidenceBucketMedium: MatrixCellSuggest,
		ConfidenceBucketHigh:   MatrixCellAuto,
	},
	schemas.RiskLevelMedium: {
		ConfidenceBucketLow:    MatrixCellBlock,
		ConfidenceBucketMedium: MatrixCellReview,
		ConfidenceBucketHigh:   MatrixCellSuggest,
	},
	schemas.RiskLevelHigh: {
		ConfidenceBucketLow:    MatrixCellBlock,
		ConfidenceBucketMedium: MatrixCellBlock,
		ConfidenceBucketHigh:   MatrixCellReview,
	},
}

// OwnershipFloorModes is the D2 floor set: ownership-protected document kinds
// resolved `blocked` at any confidence.
//
// DRIFT GUARD: pinned to the shared committed fixture
// packages/schemas/test/fixtures/modes.fixture.json — the policy AND
// @docsync/core (PROTECTED_MODES/WRITABLE_MODES) test suites assert set
// equality with the fixture, so neither side can drift. The fixture lives
// outside the Go module, so the Go port asserts equality with the inlined
// fixture data in engine-matrix_test.go (the drift-guard contract is
// preserved; the file read is test-only in TS too).
var OwnershipFloorModes = []string{"locked", "generated", "external"}

// OwnershipFloorMode is the document mutability mode accepted by the engine
// (D2 floor input). The five manifest modes are internal/schemas' DocMode
// (auto | suggest | locked | generated | human, ARCH §11.2); `external` is the
// §11.2 ownership category, surfaced here as a floor-carrying pseudo-mode so
// all three ownership-protected kinds (locked / generated / external) share
// one floor. TS: `DocMode | 'external'`.
type OwnershipFloorMode string

// OwnershipFloorMode values — the five manifest modes plus the `external`
// ownership pseudo-mode.
const (
	OwnershipFloorAuto      OwnershipFloorMode = "auto"
	OwnershipFloorSuggest   OwnershipFloorMode = "suggest"
	OwnershipFloorLocked    OwnershipFloorMode = "locked"
	OwnershipFloorGenerated OwnershipFloorMode = "generated"
	OwnershipFloorHuman     OwnershipFloorMode = "human"
	OwnershipFloorExternal  OwnershipFloorMode = "external"
)

// EngineRangeErrorCode is the stable machine-readable failure code (TS
// `EngineRangeError.code` — 'RANGE'; the TS class extends RangeError, which
// Go models as this dedicated error type).
const EngineRangeErrorCode = "RANGE"

// EngineRangeError is the typed range/shape error of the engine layer
// (mirrors AbstainQueueError's machine-readable-code pattern): every
// fail-closed guard in the engine returns this instead of a bare range error.
type EngineRangeError struct {
	Message string
}

// Code is the stable failure code (TS class code).
func (e *EngineRangeError) Code() string { return EngineRangeErrorCode }

// Error renders the preserved TS message.
func (e *EngineRangeError) Error() string { return e.Message }

// Unwrap maps the range failure onto docutil's coded-error class so
// docutil.CodeOf reports EDOCSYNC_VALIDATION — the closest existing docutil
// code for a range/shape refusal (the TS class has no docutil counterpart;
// see CONVENTIONS.md §Errors).
func (e *EngineRangeError) Unwrap() error {
	return docutil.New(docutil.CodeValidation, e.Message)
}

func newEngineRangeError(message string) *EngineRangeError {
	return &EngineRangeError{Message: message}
}

// HigherRisk is the conservative max over the closed risk set — whichever is
// higher wins (ARCH §5.3). TS: `RISK_ORDER[a] >= RISK_ORDER[b] ? a : b`, where
// an unknown level orders as undefined (every comparison false), so an
// unknown level never wins against a known one.
func HigherRisk(a, b schemas.RiskLevel) schemas.RiskLevel {
	orderA, okA := riskOrder(a)
	orderB, okB := riskOrder(b)
	if okA && okB && orderA >= orderB {
		return a
	}
	return b
}

func riskOrder(level schemas.RiskLevel) (int, bool) {
	switch level {
	case schemas.RiskLevelLow:
		return 0, true
	case schemas.RiskLevelMedium:
		return 1, true
	case schemas.RiskLevelHigh:
		return 2, true
	}
	return 0, false
}

// ResolveConfidenceBucket maps overall confidence onto the confidence axis
// from the policy's calibrated DATA (D11; TS confidenceBucket — renamed in Go
// because ConfidenceBucket is the bucket TYPE name): `high` iff confidence ≥
// `high_confidence` (the auto floor — the `auto` cell exists only in the high
// column); `medium` iff ≥ `review`; else `low`. Note the
// [calibration_floor, high_confidence) region of §5.2 — not abstained, but
// structurally unable to select `auto` — falls out of this bucketing: any
// confidence below `high_confidence` lands in the medium column, which has no
// `auto` cell.
func ResolveConfidenceBucket(confidence float64, routing Routing) (ConfidenceBucket, error) {
	if err := assertUnitInterval(confidence, "confidence"); err != nil {
		return "", err
	}
	if confidence >= routing.HighConfidence {
		return ConfidenceBucketHigh, nil
	}
	if confidence >= routing.Review {
		return ConfidenceBucketMedium, nil
	}
	return ConfidenceBucketLow, nil
}

// RiskBucketInput is the input to ResolveRiskBucket — conservative-max
// manifest risk resolution (D2/D11). TS: RiskBucketInput.
type RiskBucketInput struct {
	// JevRisk is Jev's `risk` choice from the Stage-1 decision (ARCH §5.3).
	JevRisk schemas.RiskLevel
	// ManifestRisk is the pre-resolved manifest-declared risk class for the
	// document — the caller may resolve `risk_class_overrides` itself (glob
	// matching owned elsewhere) and pass the winning class here (nil when
	// absent).
	ManifestRisk *schemas.RiskLevel
	// RiskClassOverrides is the raw per-doc-glob risk classes (the policy's
	// `risk_class_overrides` record, D2/D11 data); nil when absent.
	RiskClassOverrides map[string]schemas.RiskLevel
	// DocPath is the document path the overrides are matched against
	// (required iff overrides present).
	DocPath *string
	// MatchGlobs is the injected glob matcher (path, glob) → match — the glob
	// engine is owned elsewhere (internal/globs); this module never
	// implements pattern matching. Required iff overrides present.
	MatchGlobs func(path, glob string) bool
}

// ResolveRiskBucket resolves the risk bucket = conservative max of the
// manifest-declared class and Jev's risk choice (ARCH §5.3; TS riskBucket —
// renamed in Go because RiskBucket is the bucket TYPE name). When raw
// RiskClassOverrides are supplied, the winning declared class is resolved
// through the INJECTED matcher; overlapping matches compose to the highest
// class (most restrictive — §11.3). Supplying overrides without
// DocPath/MatchGlobs is a programmer error and fails closed (EngineRangeError)
// rather than silently ignoring the declaration.
func ResolveRiskBucket(input RiskBucketInput) (schemas.RiskLevel, error) {
	var declared *schemas.RiskLevel
	if input.ManifestRisk != nil {
		declared = input.ManifestRisk
	}
	if input.RiskClassOverrides != nil {
		if input.DocPath == nil || input.MatchGlobs == nil {
			return "", newEngineRangeError(
				"riskBucket requires docPath and matchGlobs when riskClassOverrides are supplied " +
					"(override resolution is fail-closed, never skipped silently)",
			)
		}
		var matched *schemas.RiskLevel
		for glob, level := range input.RiskClassOverrides {
			if !input.MatchGlobs(*input.DocPath, glob) {
				continue
			}
			if matched == nil {
				winner := level
				matched = &winner
			} else {
				winner := HigherRisk(*matched, level)
				matched = &winner
			}
		}
		if matched != nil {
			if declared == nil {
				declared = matched
			} else {
				winner := HigherRisk(*declared, *matched)
				declared = &winner
			}
		}
	}
	if declared == nil {
		return input.JevRisk, nil
	}
	return HigherRisk(*declared, input.JevRisk), nil
}

// ApplyOwnershipFloor is the D2 ownership floor, applied AFTER matrix
// evaluation (ARCH §5.3, PLAN §3.3): a document whose mode is in the D2 floor
// set (`locked` / `generated` / `external`) resolves to `blocked` AT ANY
// CONFIDENCE; the engine has NO path to override ownership — the input is
// typed to carry no confidence, no thresholds and no policy, so no escape
// hatch exists. For all other modes the cell passes through, with the §5.3
// table vocabulary `block` normalized to the outcome vocabulary `blocked` (the
// single such normalization point).
func ApplyOwnershipFloor(cell MatrixCell, docMode OwnershipFloorMode) ResolvedCell {
	for _, mode := range OwnershipFloorModes {
		if docMode == OwnershipFloorMode(mode) {
			return ResolvedCellBlocked
		}
	}
	if cell == MatrixCellBlock {
		return ResolvedCellBlocked
	}
	return ResolvedCell(cell)
}

// LookupMatrixCell is the matrix lookup with fail-closed guards (TS
// matrixCell — renamed in Go because MatrixCell is the cell TYPE name) — the
// table is total over the closed bucket unions, so the guards are
// unreachable; they exist so a future shape change degrades to a returned
// error instead of an empty cell silently flowing downstream (D8).
func LookupMatrixCell(risk RiskBucket, confidence ConfidenceBucket) (MatrixCell, error) {
	row, ok := MATRIX[risk]
	if !ok {
		return "", newEngineRangeError("no matrix row for risk bucket '" + string(risk) + "'")
	}
	cell, ok := row[confidence]
	if !ok {
		return "", newEngineRangeError(
			"no matrix cell for risk bucket '" + string(risk) + "' × confidence bucket '" +
				string(confidence) + "'",
		)
	}
	return cell, nil
}

// assertUnitInterval is the deterministic range guard: unit-interval
// probabilities/confidences, fail-closed (TS assertUnitInterval).
func assertUnitInterval(value float64, name string) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return newEngineRangeError(
			name + " must be a finite number in [0, 1] (got " + formatJSNumber(value) + ")",
		)
	}
	return nil
}
