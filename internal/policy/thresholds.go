// Ported from packages/policy/src/calibration/thresholds.ts
// (P12-T08; D11; ARCH §5.3/§10.8; R4).
//
// Adaptive-threshold calibration workflow: turns an evaluation split + rolling
// windows into a NEW versioned `docsync.threshold-calibration/v1` policy
// artifact — the calibrated routing thresholds (the 0.94/0.72 class of data)
// evolve ONLY as new versioned artifacts; the OLD version is never mutated in
// place (V-P12-10; PLAN §13.6).
//
// Binding rules:
//   - MATRIX SHAPE IS FIXED (ARCH §5.3): the calibration recomputes the content
//     hash of the fixed 3×3 table and refuses to emit an artifact whose shape
//     hash differs from the pinned MatrixShapeHashV1 — a matrix-shape change must
//     stop-and-surface as an architecture change, never ride calibration data.
//     Calibration data only re-sets the bucket boundaries the columns hang off
//     (D11).
//   - R4 anti-overfitting evidence is PART of the artifact: a time-split report
//     (temporal segments of the split with per-split metrics; a benefit
//     concentrated in the earliest split is flagged as historical overfit) and a
//     rolling re-evaluation report (sliding windows over the timeline tail; a
//     late-window accuracy collapse is flagged as drift — the synthetic drift
//     fixture in V-P12-10 is detected here).
//   - Pure and deterministic: injected clock (`now`), no I/O, no randomness; the
//     same input always produces a byte-identical artifact (canonical JSON).
//   - Fail closed: an empty timeline, fewer than two temporal splits/windows, a
//     thresholds-ordering violation or a version that does not strictly increase
//     throws — a silent empty report would be fail-open evidence.
//
// OD-R29/31/32 posture (recorded data, note comments only): the rolling
// window/step sizes are versioned policy DATA passed as inputs (OD-R29 spirit —
// fractions and cadences are versioned policy data, never code); integrated-mode
// criteria (OD-R31) are orthogonal and untouched here; a cross-repo calibration
// report surface (OD-R32) would consume these artifacts read-only, advisory only.
//
// Error mapping (CONVENTIONS.md §Errors): every TS RangeError guard becomes a
// coded docutil error (EDOCSYNC_VALIDATION) with the message text preserved
// EXACTLY; MatrixShapeViolationError keeps its TS class identity (named fields
// ComputedHash/PinnedHash) over an EDOCSYNC_INTERNAL coded error, because a
// matrix-shape mismatch is a broken code invariant rather than bad caller data.

package policy

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// MatrixShapeV1 is the fixed matrix shape tag this calibration era pins
// (ARCH §5.3) (TS: MATRIX_SHAPE_V1).
const MatrixShapeV1 = "fixed-3x3-v1"

// MatrixShapeHashV1 is the PINNED content hash of the FIXED confidence × risk
// matrix (ARCH §5.3) — sha256 over canonicalJson(MATRIX) (TS:
// MATRIX_SHAPE_HASH_V1). If the matrix ever changes shape, ComputeMatrixShapeHash
// stops matching this constant and EvolveThresholds fails: a shape change is an
// ARCHITECTURE change that must stop-and-surface, never flow through calibration
// data.
const MatrixShapeHashV1 = "sha256:82b30f0886a7a8558d2bf448fcdd056c9d3e04e9a09c4aae64f443db5484a418"

// thresholdCalibrationSchemaID is the registered artifact tag the calibration
// emits (THRESHOLD_CALIBRATION_SCHEMA_ID). It is kept unexported because
// internal/schemas owns the literal (schemas.ThresholdCalibration.Validate
// compares against it) and this package only ever writes it.
const thresholdCalibrationSchemaID = "docsync.threshold-calibration/v1"

// CalibrationThresholds is the fixed four-key routing-threshold vocabulary
// (schema.ts Routing; D11): the calibration surface of the adaptive workflow.
// Wire keys stay snake_case exactly as zod declared them.
type CalibrationThresholds struct {
	// HighConfidence is the auto cell floor of the confidence axis (e.g. the
	// 0.94 class of data).
	HighConfidence float64 `json:"high_confidence"`
	// Review is the lower confidence-axis threshold (e.g. the 0.72 class of
	// data).
	Review float64 `json:"review"`
	// CalibrationFloor is the upper bound of the §5.2 no-man's zone; ≥ review.
	CalibrationFloor float64 `json:"calibration_floor"`
	// DocumentationImpactAutoFloor is the documentation-impact auto floor.
	DocumentationImpactAutoFloor float64 `json:"documentation_impact_auto_floor"`
}

// CalibrationTimelineCase is one labeled evaluation case on the calibration
// timeline (R4 input data) (TS: CalibrationTimelineCase).
type CalibrationTimelineCase struct {
	// CaseID identifies the case.
	CaseID string `json:"caseId"`
	// At is the ISO-8601 instant the case resolved — the temporal axis for
	// splitting.
	At string `json:"at"`
	// Correct is the labeled outcome: was the policy's call ultimately correct.
	Correct bool `json:"correct"`
	// Probability is the asserted probability in [0, 1] (optional; drives
	// highConfidenceShare).
	Probability *float64 `json:"probability,omitempty"`
}

// matrixShapeTable is the FIXED 3×3 confidence × risk matrix (ARCH §5.3) mirrored
// for the shape-hash pin: the calibration pins the shape of the SAME table the
// decision engine consumes, so the hash must be taken over those exact cells.
//
// Engineering-matrix.ts (the engine-side half of the D11 boundary) owns the live
// table once it lands in this package; when it does, both must be reconciled to
// ONE definition — MatrixShapeHashV1 is the guard that makes any shape drift
// fail closed instead of silently riding calibration data.
var matrixShapeTable = map[string]any{
	"low":    map[string]any{"low": "review", "medium": "suggest", "high": "auto"},
	"medium": map[string]any{"low": "block", "medium": "review", "high": "suggest"},
	"high":   map[string]any{"low": "block", "medium": "block", "high": "review"},
}

// ComputeMatrixShapeHash returns sha256 over the canonical JSON of the FIXED
// matrix table, prefixed `sha256:` (TS: computeMatrixShapeHash).
func ComputeMatrixShapeHash() (string, error) {
	canonical, err := docutil.CanonicalJSON(matrixShapeTable)
	if err != nil {
		// Unreachable: the table is a static literal of JSON-safe scalars.
		return "", docutil.InternalError("computeMatrixShapeHash: canonical matrix JSON: %v", err)
	}
	return "sha256:" + docutil.ContentHash(canonical), nil
}

// MatrixShapeViolationError is thrown when the fixed matrix shape no longer
// matches the pinned hash — stop-and-surface (never calibrate over a shape
// change) (TS: MatrixShapeViolationError). Go returns values, so the class
// identity is preserved as a named error type callers match with errors.As;
// CodeOf reports EDOCSYNC_INTERNAL (a broken invariant, not caller data).
type MatrixShapeViolationError struct {
	// ComputedHash is the shape hash computed from the table on disk.
	ComputedHash string
	// PinnedHash is the hash the calibration era pins (MatrixShapeHashV1).
	PinnedHash string

	coded *docutil.Error
}

// Error renders the coded message (preserving the TS message text verbatim).
func (e *MatrixShapeViolationError) Error() string { return e.coded.Error() }

// Code exposes the machine-readable failure code.
func (e *MatrixShapeViolationError) Code() string { return e.coded.Code }

// Unwrap exposes the coded error so docutil.CodeOf reports EDOCSYNC_INTERNAL.
func (e *MatrixShapeViolationError) Unwrap() error { return e.coded }

// NewMatrixShapeViolationError builds the stop-and-surface error (TS: `new
// MatrixShapeViolationError(computedHash, pinnedHash)`).
func NewMatrixShapeViolationError(computedHash, pinnedHash string) *MatrixShapeViolationError {
	message := fmt.Sprintf(
		"matrix shape violation (P12-T08 stop-and-surface): the confidence × risk matrix SHAPE is "+
			"fixed architecture (ARCH §5.3); computed shape hash %s does not match the pinned %s — "+
			"a shape change must never ride calibration data (D11; V-P12-10)",
		computedHash, pinnedHash)
	return &MatrixShapeViolationError{
		ComputedHash: computedHash,
		PinnedHash:   pinnedHash,
		coded:        docutil.New(docutil.CodeInternal, message),
	}
}

// EvolveThresholdsInput carries the adaptive-calibration inputs (TS:
// EvolveThresholdsInput). The version numbers must move FORWARD — the old
// artifact is only ever referenced as `previous`.
type EvolveThresholdsInput struct {
	// PreviousPolicyVersion is the OLD policy version (never mutated — only
	// referenced as `previous`).
	PreviousPolicyVersion int
	// PolicyVersion is the NEW policy version the produced artifact belongs to
	// (strictly greater).
	PolicyVersion int
	CreatedBy     string
	Reason        string
	// Now is the injected clock instant (ISO-8601) stamped as `created_at`.
	Now string
	// PreviousThresholds is the old version's calibrated thresholds (the shape
	// reference; never mutated).
	PreviousThresholds CalibrationThresholds
	// ProposedThresholds is the proposed NEW threshold values (data — models
	// propose, evidence gates, humans promote).
	ProposedThresholds CalibrationThresholds
	// EvaluationSplitID is the evaluation split the calibration consumed.
	EvaluationSplitID string
	// EvidenceRefs are the evidence refs (report/record pointers) the
	// calibration consumed — at least one is required.
	EvidenceRefs []string
	// Timeline is the evaluation split's timeline cases (the R4 temporal axis).
	Timeline []CalibrationTimelineCase
	// SplitCount is the temporal split count; nil applies
	// DefaultTemporalSplitCount.
	SplitCount *int
	// OverfitTolerance is the historical-overfit tolerance (absolute accuracy
	// drop); nil applies DefaultOverfitTolerance.
	OverfitTolerance *float64
	// WindowSize is the rolling window size (cases); nil applies
	// DefaultRollingWindowSize.
	WindowSize *int
	// RollingStep is the rolling step (cases); nil applies
	// DefaultRollingStep.
	RollingStep *int
	// DriftTolerance is the drift tolerance (absolute accuracy drop); nil
	// applies DefaultDriftTolerance.
	DriftTolerance *float64
}

// ThresholdSplitMetrics is one temporal split's metrics (TS:
// ThresholdSplitMetrics).
type ThresholdSplitMetrics struct {
	SplitID             string  `json:"splitId"`
	CaseCount           int     `json:"caseCount"`
	Accuracy            float64 `json:"accuracy"`
	HighConfidenceShare float64 `json:"highConfidenceShare"`
}

// ThresholdTimeSplitReport is the time-split report — the R4 anti-overfitting
// evidence over temporal segments of the split (TS: ThresholdTimeSplitReport).
type ThresholdTimeSplitReport struct {
	SplitCount                int
	Splits                    []ThresholdSplitMetrics
	OverfitTolerance          float64
	HistoricalOverfitDetected bool
	Detail                    string
}

// ThresholdWindowMetrics is one rolling window's metrics (TS:
// ThresholdWindowMetrics).
type ThresholdWindowMetrics struct {
	WindowID            string  `json:"windowId"`
	CaseCount           int     `json:"caseCount"`
	Accuracy            float64 `json:"accuracy"`
	HighConfidenceShare float64 `json:"highConfidenceShare"`
}

// ThresholdRollingReevalReport is the rolling re-evaluation report — the windows
// where a synthetic drift fixture is detected (TS: ThresholdRollingReevalReport).
type ThresholdRollingReevalReport struct {
	WindowSize     int
	Step           int
	Windows        []ThresholdWindowMetrics
	DriftTolerance float64
	DriftDetected  bool
	Detail         string
}

// DefaultTemporalSplitCount is the bootstrap default number of temporal splits
// (TS: DEFAULT_TEMPORAL_SPLIT_COUNT).
const DefaultTemporalSplitCount = 2

// DefaultOverfitTolerance is the bootstrap default overfit tolerance — absolute
// accuracy drop, earliest vs latest split (TS: DEFAULT_OVERFIT_TOLERANCE).
const DefaultOverfitTolerance = 0.1

// DefaultRollingWindowSize is the bootstrap default rolling window size (cases)
// (TS: DEFAULT_ROLLING_WINDOW_SIZE).
const DefaultRollingWindowSize = 30

// DefaultRollingStep is the bootstrap default rolling step (cases) (TS:
// DEFAULT_ROLLING_STEP).
const DefaultRollingStep = 10

// DefaultDriftTolerance is the bootstrap default drift tolerance — absolute
// accuracy drop, latest window vs reference mean (TS: DEFAULT_DRIFT_TOLERANCE).
const DefaultDriftTolerance = 0.1

// assertCalibrationThresholds ports the module-private assertCalibrationThresholds
// guard: all four values live in the unit interval and the §5.3/§5.2 ordering
// invariants hold.
func assertCalibrationThresholds(thresholds CalibrationThresholds, label string) error {
	if err := calibrationUnitInterval(thresholds.HighConfidence, label+".high_confidence"); err != nil {
		return err
	}
	if err := calibrationUnitInterval(thresholds.Review, label+".review"); err != nil {
		return err
	}
	if err := calibrationUnitInterval(thresholds.CalibrationFloor, label+".calibration_floor"); err != nil {
		return err
	}
	if err := calibrationUnitInterval(thresholds.DocumentationImpactAutoFloor, label+".documentation_impact_auto_floor"); err != nil {
		return err
	}
	if thresholds.Review > thresholds.HighConfidence {
		return calibrationRangeError(
			"%s.review must not exceed high_confidence (Routing ordering invariant; ARCH §5.3)", label)
	}
	if thresholds.CalibrationFloor < thresholds.Review {
		return calibrationRangeError(
			"%s.calibration_floor bounds the no-man's zone from above and must be ≥ review (ARCH §5.2)", label)
	}
	return nil
}

// parseCalibrationInstant reports whether s is an ISO-8601 instant, mirroring
// the TS guard `Number.isNaN(Date.parse(value))` for the instants this workflow
// consumes (RFC 3339 with or without fractional seconds).
func parseCalibrationInstant(value string) (time.Time, bool) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

// sortedCalibrationTimeline ports sortedTimeline: chronological case order by
// `at`, ties broken by caseId — deterministic (TS: sortedTimeline).
func sortedCalibrationTimeline(timeline []CalibrationTimelineCase) []CalibrationTimelineCase {
	sorted := append([]CalibrationTimelineCase(nil), timeline...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		at, aOK := parseCalibrationInstant(a.At)
		bt, bOK := parseCalibrationInstant(b.At)
		if aOK && bOK && !at.Equal(bt) {
			return at.Before(bt)
		}
		// Equal instants — or (unreachable through EvolveThresholds, which
		// pre-validates every `at`) an unparseable instant — fall back to the
		// caseId order so the partition stays deterministic.
		return a.CaseID < b.CaseID
	})
	return sorted
}

// calibrationSegment is one segment's metrics (caseSegmentMetrics' return shape).
type calibrationSegment struct {
	caseCount           int
	accuracy            float64
	highConfidenceShare float64
}

// caseSegmentMetrics ports caseSegmentMetrics: accuracy over the segment and the
// share of probability-carrying cases at or above the proposed high_confidence.
func caseSegmentMetrics(cases []CalibrationTimelineCase, highConfidence float64) calibrationSegment {
	total := len(cases)
	correct := 0
	high := 0
	withProbability := 0
	for _, item := range cases {
		if item.Correct {
			correct++
		}
		if item.Probability != nil {
			withProbability++
			if *item.Probability >= highConfidence {
				high++
			}
		}
	}
	segment := calibrationSegment{caseCount: total}
	if total > 0 {
		segment.accuracy = float64(correct) / float64(total)
	}
	if withProbability > 0 {
		segment.highConfidenceShare = float64(high) / float64(withProbability)
	}
	return segment
}

// ComputeTimeSplitReport is the time-split evaluation (R4): partitions the
// timeline into `splitCount` contiguous TEMPORAL segments (earliest → latest)
// and computes per-split metrics. Historical overfit heuristic (R4 — documented,
// conservative): the calibration's benefit must not be concentrated in the
// training past — an accuracy drop from the EARLIEST to the LATEST split
// exceeding `overfitTolerance` sets HistoricalOverfitDetected: true on the
// artifact.
func ComputeTimeSplitReport(
	timeline []CalibrationTimelineCase,
	proposed CalibrationThresholds,
	splitCount int,
	overfitTolerance float64,
) (*ThresholdTimeSplitReport, error) {
	sorted := sortedCalibrationTimeline(timeline)
	if splitCount < 2 {
		return nil, calibrationRangeError("splitCount must be an integer ≥ 2 (got %d)", splitCount)
	}
	if len(sorted) < splitCount {
		return nil, calibrationRangeError(
			"time-split evaluation requires at least %d cases for %d splits (got %d) — "+
				"an under-covered split must not pass as evidence (fail closed)",
			splitCount, splitCount, len(sorted))
	}
	perSplit := len(sorted) / splitCount
	splits := make([]ThresholdSplitMetrics, 0, splitCount)
	for index := 0; index < splitCount; index++ {
		start := index * perSplit
		end := start + perSplit
		if index == splitCount-1 {
			end = len(sorted)
		}
		segment := caseSegmentMetrics(sorted[start:end], proposed.HighConfidence)
		splits = append(splits, ThresholdSplitMetrics{
			SplitID:             fmt.Sprintf("t%d", index+1),
			CaseCount:           segment.caseCount,
			Accuracy:            segment.accuracy,
			HighConfidenceShare: segment.highConfidenceShare,
		})
	}
	first := splits[0]
	last := splits[len(splits)-1]
	drop := first.Accuracy - last.Accuracy
	historicalOverfitDetected := drop > overfitTolerance
	detail := fmt.Sprintf(
		"earliest %s accuracy %s vs latest %s accuracy %s — ",
		first.SplitID, jsToFixed(first.Accuracy, 4), last.SplitID, jsToFixed(last.Accuracy, 4))
	if historicalOverfitDetected {
		detail += fmt.Sprintf(
			"benefit concentrated in the past exceeds the %s tolerance: HISTORICAL OVERFIT detected (R4)",
			jsString(overfitTolerance))
	} else {
		detail += fmt.Sprintf(
			"drop %s within the %s tolerance: no historical overfit (R4)",
			jsToFixed(drop, 4), jsString(overfitTolerance))
	}
	return &ThresholdTimeSplitReport{
		SplitCount:                splitCount,
		Splits:                    splits,
		OverfitTolerance:          overfitTolerance,
		HistoricalOverfitDetected: historicalOverfitDetected,
		Detail:                    detail,
	}, nil
}

// ComputeRollingReevalReport is the rolling re-evaluation (R4): sliding windows
// of `windowSize` cases stepped by `step` over the timeline tail. Drift
// heuristic (documented, conservative): the reference is the mean accuracy of
// all windows EXCEPT the last; a last-window accuracy below
// `reference − driftTolerance` sets DriftDetected: true — the V-P12-10 synthetic
// drift fixture is detected here. Fewer than two windows is a caller error
// (drift is not assessable — fail closed, never a silent "no drift").
func ComputeRollingReevalReport(
	timeline []CalibrationTimelineCase,
	proposed CalibrationThresholds,
	windowSize int,
	step int,
	driftTolerance float64,
) (*ThresholdRollingReevalReport, error) {
	sorted := sortedCalibrationTimeline(timeline)
	if windowSize < 1 {
		return nil, calibrationRangeError("windowSize must be a positive integer (got %d)", windowSize)
	}
	if step < 1 {
		return nil, calibrationRangeError("step must be a positive integer (got %d)", step)
	}
	if len(sorted) < windowSize {
		return nil, calibrationRangeError(
			"rolling re-evaluation requires at least windowSize=%d cases (got %d) — fail closed",
			windowSize, len(sorted))
	}
	windows := []ThresholdWindowMetrics{}
	for start := 0; start+windowSize <= len(sorted); start += step {
		segment := caseSegmentMetrics(sorted[start:start+windowSize], proposed.HighConfidence)
		windows = append(windows, ThresholdWindowMetrics{
			WindowID:            fmt.Sprintf("w%d", len(windows)),
			CaseCount:           segment.caseCount,
			Accuracy:            segment.accuracy,
			HighConfidenceShare: segment.highConfidenceShare,
		})
	}
	if len(windows) < 2 {
		return nil, calibrationRangeError(
			"rolling re-evaluation requires at least two windows (windowSize %d, step %d over %d cases "+
				"yielded %d) — drift is not assessable from a single window and must not silently pass "+
				"(fail closed)",
			windowSize, step, len(sorted), len(windows))
	}
	last := windows[len(windows)-1]
	referenceSum := 0.0
	for index := 0; index < len(windows)-1; index++ {
		referenceSum += windows[index].Accuracy
	}
	reference := referenceSum / float64(len(windows)-1)
	driftDetected := last.Accuracy < reference-driftTolerance
	detail := fmt.Sprintf(
		"latest %s accuracy %s vs reference mean %s over %d earlier window(s) — ",
		last.WindowID, jsToFixed(last.Accuracy, 4), jsToFixed(reference, 4), len(windows)-1)
	if driftDetected {
		detail += fmt.Sprintf(
			"drop exceeds the %s tolerance: DRIFT detected (R4)", jsString(driftTolerance))
	} else {
		detail += fmt.Sprintf(
			"drop %s within the %s tolerance: no drift detected (R4)",
			jsToFixed(math.Max(0, reference-last.Accuracy), 4), jsString(driftTolerance))
	}
	return &ThresholdRollingReevalReport{
		WindowSize:     windowSize,
		Step:           step,
		Windows:        windows,
		DriftTolerance: driftTolerance,
		DriftDetected:  driftDetected,
		Detail:         detail,
	}, nil
}

// EvolveThresholds produces a NEW `docsync.threshold-calibration/v1` artifact
// (P12-T08; V-P12-10): the old version is NEVER mutated — the input's previous
// thresholds and timeline are only read (sorted copies), and the output is a
// fresh artifact bumping `policy_version`. The fixed matrix shape is asserted
// against the pinned hash BEFORE any evidence is computed (stop-and-surface),
// the time-split report and the rolling re-evaluation report are computed and
// EMBEDDED (R4), and the result is validated against the registered schema.
func EvolveThresholds(input EvolveThresholdsInput) (*schemas.ThresholdCalibration, error) {
	if input.PreviousPolicyVersion < 1 {
		return nil, calibrationRangeError(
			"previousPolicyVersion must be a positive integer (got %d)", input.PreviousPolicyVersion)
	}
	if input.PolicyVersion < 1 {
		return nil, calibrationRangeError("policyVersion must be a positive integer (got %d)", input.PolicyVersion)
	}
	if input.PolicyVersion <= input.PreviousPolicyVersion {
		return nil, calibrationRangeError(
			"policyVersion %d must strictly increase over previousPolicyVersion %d — "+
				"adaptive calibration never mutates an existing version in place (V-P12-10; D11)",
			input.PolicyVersion, input.PreviousPolicyVersion)
	}
	if input.CreatedBy == "" {
		return nil, calibrationRangeError("createdBy must be a non-empty string")
	}
	if input.Reason == "" {
		return nil, calibrationRangeError("reason must be a non-empty string")
	}
	if _, ok := parseCalibrationInstant(input.Now); !ok {
		return nil, calibrationRangeError("now must be an ISO-8601 instant (got %s)", input.Now)
	}
	if input.EvaluationSplitID == "" {
		return nil, calibrationRangeError("evaluationSplitId must be a non-empty string")
	}
	if len(input.EvidenceRefs) == 0 {
		return nil, calibrationRangeError(
			"evidenceRefs must carry at least one ref — calibration without recorded evidence refs " +
				"is \"provenance or it didn't happen\" (fail closed)")
	}
	for _, ref := range input.EvidenceRefs {
		if ref == "" {
			return nil, calibrationRangeError("each evidenceRef must be a non-empty string")
		}
	}
	if len(input.Timeline) == 0 {
		return nil, calibrationRangeError(
			"timeline must carry at least one case — an empty split must not produce calibration evidence")
	}
	for index, item := range input.Timeline {
		if item.CaseID == "" {
			return nil, calibrationRangeError("timeline.%d.caseId must be a non-empty string", index)
		}
		if _, ok := parseCalibrationInstant(item.At); !ok {
			return nil, calibrationRangeError("timeline.%d.at must be an ISO-8601 instant (got %s)", index, item.At)
		}
		// TS also guards `typeof item.correct !== 'boolean'`; Go's type system
		// makes a non-boolean `correct` unrepresentable, so that guard has no
		// port (documented deviation).
		if item.Probability != nil {
			if err := calibrationUnitInterval(*item.Probability, fmt.Sprintf("timeline.%d.probability", index)); err != nil {
				return nil, err
			}
		}
	}

	if err := assertCalibrationThresholds(input.PreviousThresholds, "previousThresholds"); err != nil {
		return nil, err
	}
	if err := assertCalibrationThresholds(input.ProposedThresholds, "proposedThresholds"); err != nil {
		return nil, err
	}

	// Matrix SHAPE assertion FIRST (stop-and-surface before any evidence): the
	// computed hash of the fixed table must match the pinned constant.
	computedShapeHash, err := ComputeMatrixShapeHash()
	if err != nil {
		return nil, err
	}
	if computedShapeHash != MatrixShapeHashV1 {
		return nil, NewMatrixShapeViolationError(computedShapeHash, MatrixShapeHashV1)
	}

	splitCount := DefaultTemporalSplitCount
	if input.SplitCount != nil {
		splitCount = *input.SplitCount
	}
	overfitTolerance := DefaultOverfitTolerance
	if input.OverfitTolerance != nil {
		overfitTolerance = *input.OverfitTolerance
	}
	if err := calibrationUnitInterval(overfitTolerance, "overfitTolerance"); err != nil {
		return nil, err
	}
	windowSize := DefaultRollingWindowSize
	if input.WindowSize != nil {
		windowSize = *input.WindowSize
	}
	rollingStep := DefaultRollingStep
	if input.RollingStep != nil {
		rollingStep = *input.RollingStep
	}
	driftTolerance := DefaultDriftTolerance
	if input.DriftTolerance != nil {
		driftTolerance = *input.DriftTolerance
	}
	if err := calibrationUnitInterval(driftTolerance, "driftTolerance"); err != nil {
		return nil, err
	}

	timeSplitReport, err := ComputeTimeSplitReport(input.Timeline, input.ProposedThresholds, splitCount, overfitTolerance)
	if err != nil {
		return nil, err
	}
	rollingReport, err := ComputeRollingReevalReport(input.Timeline, input.ProposedThresholds, windowSize, rollingStep, driftTolerance)
	if err != nil {
		return nil, err
	}

	artifact := schemas.ThresholdCalibration{
		Schema:                thresholdCalibrationSchemaID,
		PolicyVersion:         input.PolicyVersion,
		PreviousPolicyVersion: input.PreviousPolicyVersion,
		CreatedBy:             input.CreatedBy,
		Reason:                input.Reason,
		CreatedAt:             input.Now,
		Thresholds: schemas.ThresholdRouting{
			HighConfidence:               input.ProposedThresholds.HighConfidence,
			Review:                       input.ProposedThresholds.Review,
			CalibrationFloor:             input.ProposedThresholds.CalibrationFloor,
			DocumentationImpactAutoFloor: input.ProposedThresholds.DocumentationImpactAutoFloor,
		},
		MatrixShape:     MatrixShapeV1,
		MatrixShapeHash: computedShapeHash,
		Evaluation: schemas.ThresholdCalibrationEvaluation{
			SplitID:      input.EvaluationSplitID,
			EvidenceRefs: append([]string(nil), input.EvidenceRefs...),
		},
		TimeSplitReport: schemas.ThresholdCalibrationTimeSplitReport{
			SplitCount:                timeSplitReport.SplitCount,
			Splits:                    make([]schemas.TimeSplitRow, 0, len(timeSplitReport.Splits)),
			OverfitTolerance:          timeSplitReport.OverfitTolerance,
			HistoricalOverfitDetected: timeSplitReport.HistoricalOverfitDetected,
			Detail:                    timeSplitReport.Detail,
		},
		RollingReevalReport: schemas.ThresholdCalibrationRollingReport{
			WindowSize:     rollingReport.WindowSize,
			Step:           rollingReport.Step,
			Windows:        make([]schemas.RollingWindowRow, 0, len(rollingReport.Windows)),
			DriftTolerance: rollingReport.DriftTolerance,
			DriftDetected:  rollingReport.DriftDetected,
			Detail:         rollingReport.Detail,
		},
	}
	for _, split := range timeSplitReport.Splits {
		artifact.TimeSplitReport.Splits = append(artifact.TimeSplitReport.Splits, schemas.TimeSplitRow{
			SplitID:             split.SplitID,
			CaseCount:           split.CaseCount,
			Accuracy:            split.Accuracy,
			HighConfidenceShare: split.HighConfidenceShare,
		})
	}
	for _, window := range rollingReport.Windows {
		artifact.RollingReevalReport.Windows = append(artifact.RollingReevalReport.Windows, schemas.RollingWindowRow{
			WindowID:            window.WindowID,
			CaseCount:           window.CaseCount,
			Accuracy:            window.Accuracy,
			HighConfidenceShare: window.HighConfidenceShare,
		})
	}

	// THRESHOLD_CALIBRATION_SCHEMA.parse(…): the produced artifact is decoded
	// back through the registered strict parser so a shape the schema rejects
	// fails closed here rather than downstream.
	wire, err := json.Marshal(artifact)
	if err != nil {
		return nil, docutil.InternalError("evolveThresholds: marshal artifact: %v", err)
	}
	parsed, err := schemas.ParseThresholdCalibration(wire)
	if err != nil {
		return nil, docutil.Newf(docutil.CodeValidation, "evolveThresholds: produced an invalid %s artifact: %v",
			thresholdCalibrationSchemaID, strings.TrimPrefix(err.Error(), "validation failed: "))
	}
	return parsed, nil
}
