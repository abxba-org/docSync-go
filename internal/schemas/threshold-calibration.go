package schemas

// Ported from packages/schemas/src/v1/threshold-calibration.ts
// (THRESHOLD_CALIBRATION_SCHEMA_ID + MATRIX_SHAPE_V1 + SHA256_HASH_REGEX +
// TIME_SPLIT_ROW_SCHEMA + ROLLING_WINDOW_ROW_SCHEMA +
// THRESHOLD_CALIBRATION_SCHEMA + THRESHOLD_CALIBRATION_EXAMPLE).
//
// Threshold calibration artifact — `docsync.threshold-calibration/v1`
// (P12-T08; D11; ARCH §5.3, §10.8; R4): the VERSIONED policy-data artifact an
// adaptive-threshold calibration run produces. The 0.94/0.72 routing
// thresholds are per-policy-version calibrated data — NEVER hard-coded (D11) —
// and they live here as committed, reviewable data: an adaptive change NEVER
// mutates an existing artifact in place; it can only CREATE a new artifact
// that bumps `policy_version` (V-P12-10; PLAN §13.6).
//
// Shape notes (binding): the artifact carries the fixed confidence × risk
// matrix shape's content hash (`matrix_shape_hash`, shape tag `fixed-3x3-v1`)
// so a matrix-shape change can never ride a calibration-data change;
// `thresholds` carries exactly the fixed four-key routing vocabulary of the
// Jev policy's Routing block; the time-split report and the rolling
// re-evaluation report are PART of the artifact (R4 anti-overfitting
// evidence); `created_at` is an injected instant supplied by the caller —
// never generated here.
//
// Wire keys stay snake_case exactly as zod declared them; z.strictObject ⇒
// unknown JSON keys are rejected at parse (StrictDecode), and every zod
// validation message is reproduced in Validate() via Issue paths (see
// decode.go for the zod→Go mapping rules).

// ThresholdCalibrationSchemaID is the literal schema tag
// (THRESHOLD_CALIBRATION_SCHEMA_ID).
const ThresholdCalibrationSchemaID = "docsync.threshold-calibration/v1"

// MatrixShapeV1 is the fixed confidence × risk matrix shape tag
// (MATRIX_SHAPE_V1 — ARCH §5.3: 3×3, fixed code).
const MatrixShapeV1 = "fixed-3x3-v1"

// IsSHA256HashDigest reports the `sha256:<hex>` digest format
// (SHA256_HASH_REGEX /^sha256:[0-9a-f]{64}$/ — canonical-JSON hashes, mirrors
// policyHash).
func IsSHA256HashDigest(s string) bool {
	const prefix = "sha256:"
	if len(s) != len(prefix)+64 {
		return false
	}
	if s[:len(prefix)] != prefix {
		return false
	}
	return IsBareSHA256Hex(s[len(prefix):])
}

// thresholdUnitIntervalIssue renders zod's UNIT_INTERVAL failures
// (z.number().min(0).max(1) — evaluation-report.ts).
func thresholdUnitIntervalIssue(v float64, path []string) *Issue {
	if v < 0 {
		return &Issue{Path: path, Message: "Number must be greater than or equal to 0"}
	}
	if v > 1 {
		return &Issue{Path: path, Message: "Number must be less than or equal to 1"}
	}
	return nil
}

// TimeSplitRow is one temporal split of the evaluation timeline —
// split-wise metrics (TIME_SPLIT_ROW_SCHEMA, R4).
type TimeSplitRow struct {
	SplitID             string  `json:"split_id"`
	CaseCount           int     `json:"case_count"`
	Accuracy            float64 `json:"accuracy"`
	HighConfidenceShare float64 `json:"high_confidence_share"`
}

// Validate checks the row (strict).
func (r *TimeSplitRow) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(r.SplitID, 1, []string{"split_id"}))
	if r.CaseCount < 1 {
		Collect(&issues, &Issue{Path: []string{"case_count"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, thresholdUnitIntervalIssue(r.Accuracy, []string{"accuracy"}))
	Collect(&issues, thresholdUnitIntervalIssue(r.HighConfidenceShare, []string{"high_confidence_share"}))
	return issues
}

// RollingWindowRow is one rolling re-evaluation window — window-wise metrics
// (ROLLING_WINDOW_ROW_SCHEMA, R4).
type RollingWindowRow struct {
	WindowID            string  `json:"window_id"`
	CaseCount           int     `json:"case_count"`
	Accuracy            float64 `json:"accuracy"`
	HighConfidenceShare float64 `json:"high_confidence_share"`
}

// Validate checks the row (strict).
func (r *RollingWindowRow) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(r.WindowID, 1, []string{"window_id"}))
	if r.CaseCount < 1 {
		Collect(&issues, &Issue{Path: []string{"case_count"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, thresholdUnitIntervalIssue(r.Accuracy, []string{"accuracy"}))
	Collect(&issues, thresholdUnitIntervalIssue(r.HighConfidenceShare, []string{"high_confidence_share"}))
	return issues
}

// ThresholdRouting carries exactly the fixed four-key routing vocabulary of
// the Jev policy's Routing block — the fixed calibration surface; adding or
// removing keys would be a schema change (version bump), never a data edit.
type ThresholdRouting struct {
	HighConfidence               float64 `json:"high_confidence"`
	Review                       float64 `json:"review"`
	CalibrationFloor             float64 `json:"calibration_floor"`
	DocumentationImpactAutoFloor float64 `json:"documentation_impact_auto_floor"`
}

// Validate checks the routing block (strict).
func (t *ThresholdRouting) Validate() []Issue {
	var issues []Issue
	Collect(&issues, thresholdUnitIntervalIssue(t.HighConfidence, []string{"high_confidence"}))
	Collect(&issues, thresholdUnitIntervalIssue(t.Review, []string{"review"}))
	Collect(&issues, thresholdUnitIntervalIssue(t.CalibrationFloor, []string{"calibration_floor"}))
	Collect(&issues, thresholdUnitIntervalIssue(t.DocumentationImpactAutoFloor, []string{"documentation_impact_auto_floor"}))
	return issues
}

// ThresholdCalibrationEvaluation names the evaluation-report/harness records
// the calibration consumed (provenance or it didn't happen).
type ThresholdCalibrationEvaluation struct {
	SplitID      string   `json:"split_id"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// Validate checks the evaluation block (strict).
func (e *ThresholdCalibrationEvaluation) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(e.SplitID, 1, []string{"split_id"}))
	if len(e.EvidenceRefs) < 1 {
		Collect(&issues, &Issue{Path: []string{"evidence_refs"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i, ref := range e.EvidenceRefs {
		Collect(&issues, RequiredString(ref, 1, []string{"evidence_refs", itoa(i)}))
	}
	return issues
}

// ThresholdCalibrationTimeSplitReport is the time-split report — split-wise
// metrics must show no historical overfit (R4).
type ThresholdCalibrationTimeSplitReport struct {
	SplitCount                int            `json:"split_count"`
	Splits                    []TimeSplitRow `json:"splits"`
	OverfitTolerance          float64        `json:"overfit_tolerance"`
	HistoricalOverfitDetected bool           `json:"historical_overfit_detected"`
	Detail                    string         `json:"detail"`
}

// Validate checks the time-split report (strict).
func (r *ThresholdCalibrationTimeSplitReport) Validate() []Issue {
	var issues []Issue
	if r.SplitCount < 1 {
		Collect(&issues, &Issue{Path: []string{"split_count"}, Message: "Number must be greater than 0"})
	}
	if len(r.Splits) < 2 {
		Collect(&issues, &Issue{Path: []string{"splits"}, Message: "Array must contain at least 2 element(s)"})
	}
	for i := range r.Splits {
		for _, iss := range r.Splits[i].Validate() {
			iss.Path = append([]string{"splits", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	Collect(&issues, thresholdUnitIntervalIssue(r.OverfitTolerance, []string{"overfit_tolerance"}))
	Collect(&issues, RequiredString(r.Detail, 1, []string{"detail"}))
	return issues
}

// ThresholdCalibrationRollingReport is the rolling re-evaluation report — the
// windows where a synthetic drift fixture is detected (V-P12-10).
type ThresholdCalibrationRollingReport struct {
	WindowSize     int                `json:"window_size"`
	Step           int                `json:"step"`
	Windows        []RollingWindowRow `json:"windows"`
	DriftTolerance float64            `json:"drift_tolerance"`
	DriftDetected  bool               `json:"drift_detected"`
	Detail         string             `json:"detail"`
}

// Validate checks the rolling report (strict).
func (r *ThresholdCalibrationRollingReport) Validate() []Issue {
	var issues []Issue
	if r.WindowSize < 1 {
		Collect(&issues, &Issue{Path: []string{"window_size"}, Message: "Number must be greater than 0"})
	}
	if r.Step < 1 {
		Collect(&issues, &Issue{Path: []string{"step"}, Message: "Number must be greater than 0"})
	}
	if len(r.Windows) < 2 {
		Collect(&issues, &Issue{Path: []string{"windows"}, Message: "Array must contain at least 2 element(s)"})
	}
	for i := range r.Windows {
		for _, iss := range r.Windows[i].Validate() {
			iss.Path = append([]string{"windows", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	Collect(&issues, thresholdUnitIntervalIssue(r.DriftTolerance, []string{"drift_tolerance"}))
	Collect(&issues, RequiredString(r.Detail, 1, []string{"detail"}))
	return issues
}

// ThresholdCalibration is the `docsync.threshold-calibration/v1` artifact
// (THRESHOLD_CALIBRATION_SCHEMA). `previous_policy_version` exists so the
// artifact proves it moves FORWARD: an adaptive change only ever CREATES a
// new artifact, it never rewrites the old one (V-P12-10).
type ThresholdCalibration struct {
	Schema                string                              `json:"schema"`
	PolicyVersion         int                                 `json:"policy_version"`
	PreviousPolicyVersion int                                 `json:"previous_policy_version"`
	CreatedBy             string                              `json:"created_by"`
	Reason                string                              `json:"reason"`
	CreatedAt             string                              `json:"created_at"`
	Thresholds            ThresholdRouting                    `json:"thresholds"`
	MatrixShape           string                              `json:"matrix_shape"`
	MatrixShapeHash       string                              `json:"matrix_shape_hash"`
	Evaluation            ThresholdCalibrationEvaluation      `json:"evaluation"`
	TimeSplitReport       ThresholdCalibrationTimeSplitReport `json:"time_split_report"`
	RollingReevalReport   ThresholdCalibrationRollingReport   `json:"rolling_reeval_report"`
}

// Validate checks the artifact against THRESHOLD_CALIBRATION_SCHEMA (strict),
// reproducing the zod issue paths and the three superRefine invariants.
func (c *ThresholdCalibration) Validate() []Issue {
	var issues []Issue
	if c.Schema != ThresholdCalibrationSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.threshold-calibration/v1"`})
	}
	if c.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	if c.PreviousPolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"previous_policy_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, RequiredString(c.CreatedBy, 1, []string{"created_by"}))
	Collect(&issues, RequiredString(c.Reason, 1, []string{"reason"}))
	if !isOffsetDateTime(c.CreatedAt) {
		Collect(&issues, &Issue{Path: []string{"created_at"}, Message: "Invalid datetime"})
	}
	for _, iss := range c.Thresholds.Validate() {
		iss.Path = append([]string{"thresholds"}, iss.Path...)
		issues = append(issues, iss)
	}
	if c.MatrixShape != MatrixShapeV1 {
		Collect(&issues, &Issue{Path: []string{"matrix_shape"}, Message: `Invalid literal value, expected "fixed-3x3-v1"`})
	}
	if !IsSHA256HashDigest(c.MatrixShapeHash) {
		Collect(&issues, &Issue{Path: []string{"matrix_shape_hash"}, Message: "Invalid"})
	}
	for _, iss := range c.Evaluation.Validate() {
		iss.Path = append([]string{"evaluation"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range c.TimeSplitReport.Validate() {
		iss.Path = append([]string{"time_split_report"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range c.RollingReevalReport.Validate() {
		iss.Path = append([]string{"rolling_reeval_report"}, iss.Path...)
		issues = append(issues, iss)
	}

	// Routing ordering invariants (schema.ts Routing semantics): review is the
	// lower confidence-axis threshold, the calibration floor is the upper
	// bound of the §5.2 no-man's zone — it must sit at or above review.
	if c.Thresholds.Review > c.Thresholds.HighConfidence {
		Collect(&issues, &Issue{
			Path:    []string{"thresholds", "review"},
			Message: "review must not exceed high_confidence (Routing ordering invariant; ARCH §5.3)",
		})
	}
	if c.Thresholds.CalibrationFloor < c.Thresholds.Review {
		Collect(&issues, &Issue{
			Path:    []string{"thresholds", "calibration_floor"},
			Message: "calibration_floor bounds the no-man's zone from above and must be ≥ review (ARCH §5.2)",
		})
	}
	// Version discipline (V-P12-10): a calibration artifact can only move
	// FORWARD — the old version is never rewritten, so the new version must
	// strictly increase.
	if c.PolicyVersion <= c.PreviousPolicyVersion {
		Collect(&issues, &Issue{
			Path: []string{"policy_version"},
			Message: "policy_version must strictly increase over previous_policy_version — adaptive " +
				"calibration never mutates an existing version in place (V-P12-10; D11)",
		})
	}
	return issues
}

// ParseThresholdCalibration strictly decodes and validates
// `docsync.threshold-calibration/v1` bytes (unknown JSON keys are rejected,
// mirroring z.strictObject).
func ParseThresholdCalibration(data []byte) (*ThresholdCalibration, error) {
	var c ThresholdCalibration
	if err := StrictDecode(data, &c); err != nil {
		return nil, err
	}
	if issues := c.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &c, nil
}

// ThresholdCalibrationExample mirrors THRESHOLD_CALIBRATION_EXAMPLE (golden
// data for byte-for-byte serializer checks).
func ThresholdCalibrationExample() ThresholdCalibration {
	return ThresholdCalibration{
		Schema:                "docsync.threshold-calibration/v1",
		PolicyVersion:         18,
		PreviousPolicyVersion: 17,
		CreatedBy:             "app:docsync/threshold-calibration@v1",
		Reason: "Adaptive recalibration from the frozen evaluation split: review floor lowered 0.72 → 0.70 " +
			"after split-wise and rolling-window evidence showed stable accuracy (R4).",
		CreatedAt: "2026-10-09T00:00:00.000Z",
		Thresholds: ThresholdRouting{
			HighConfidence:               0.94,
			Review:                       0.7,
			CalibrationFloor:             0.94,
			DocumentationImpactAutoFloor: 0.94,
		},
		MatrixShape:     MatrixShapeV1,
		MatrixShapeHash: "sha256:82b30f0886a7a8558d2bf448fcdd056c9d3e04e9a09c4aae64f443db5484a418",
		Evaluation: ThresholdCalibrationEvaluation{
			SplitID:      "frozen-2026-10",
			EvidenceRefs: []string{"policy-history/evaluation-report-v17.json#frozen-2026-10"},
		},
		TimeSplitReport: ThresholdCalibrationTimeSplitReport{
			SplitCount: 2,
			Splits: []TimeSplitRow{
				{SplitID: "t1", CaseCount: 60, Accuracy: 0.9, HighConfidenceShare: 0.8},
				{SplitID: "t2", CaseCount: 60, Accuracy: 0.9, HighConfidenceShare: 0.8},
			},
			OverfitTolerance:          0.1,
			HistoricalOverfitDetected: false,
			Detail:                    "earliest t1 accuracy 0.90 vs latest t2 accuracy 0.90 — no historical overfit (R4)",
		},
		RollingReevalReport: ThresholdCalibrationRollingReport{
			WindowSize: 30,
			Step:       10,
			Windows: []RollingWindowRow{
				{WindowID: "w0", CaseCount: 30, Accuracy: 0.9, HighConfidenceShare: 0.8},
				{WindowID: "w1", CaseCount: 30, Accuracy: 0.9, HighConfidenceShare: 0.8},
			},
			DriftTolerance: 0.1,
			DriftDetected:  false,
			Detail:         "latest w1 accuracy 0.90 vs reference mean 0.90 — no drift detected (R4)",
		},
	}
}
