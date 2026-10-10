// Ported from packages/schemas/src/v1/evaluation-report.ts
// (EVALUATION_REPORT_SCHEMA_ID + UNIT_INTERVAL + EVALUATION_REPORT_SCHEMA +
// EvaluationReport + EVALUATION_REPORT_EXAMPLE).
package schemas

// Evaluation report — `docsync.evaluation-report/v1` (P10-T01; ARCH §9
// metrics; CTX §35). The scored outcome of evaluating a policy version over
// a sample of evaluation cases: detection recall/precision, patch
// correctness, patch minimality, false update rate, abstention quality,
// verifier judge disagreement (P10-T24 R5) and the composite objective
// score. Thresholds (0.94/0.72) are per-policy-version calibrated data
// (D11) — they are decided elsewhere; this record only carries the measured
// numbers that calibration consumes.
//
// Shape notes: flat envelope with a literal `schema` tag (OD-R01 strict,
// OD-2 vocabulary reuse); every rate/metric is bounded to the unit interval
// 0..1 while minimality components are non-negative unbounded magnitudes
// (edit distance, hunk count) — out-of-range is refused, not clamped (fail
// closed); baseline_version is optional (the first report of a policy
// lineage has no baseline to compare against); computed_at is an injected
// instant supplied by the emitter — never generated here.

// EvaluationReportSchemaID is the wire literal carried by the `schema` field
// (EVALUATION_REPORT_SCHEMA_ID).
const EvaluationReportSchemaID = "docsync.evaluation-report/v1"

// unitIntervalIssue renders the zod failures of UNIT_INTERVAL
// (z.number().min(0).max(1)) — out-of-range is refused, never clamped.
func unitIntervalIssue(v float64, path []string) *Issue {
	if v < 0 {
		return &Issue{Path: path, Message: "Number must be greater than or equal to 0"}
	}
	if v > 1 {
		return &Issue{Path: path, Message: "Number must be less than or equal to 1"}
	}
	return nil
}

// EvalMinimality is the minimality block: non-negative unbounded magnitudes
// (mean edit distance, mean hunk count).
type EvalMinimality struct {
	MeanEditDistance float64 `json:"mean_edit_distance"`
	MeanHunkCount    float64 `json:"mean_hunk_count"`
}

// Validate checks the minimality block against z.number().min(0).
func (m *EvalMinimality) Validate() []Issue {
	var issues []Issue
	if m.MeanEditDistance < 0 {
		Collect(&issues, &Issue{Path: []string{"mean_edit_distance"}, Message: "Number must be greater than or equal to 0"})
	}
	if m.MeanHunkCount < 0 {
		Collect(&issues, &Issue{Path: []string{"mean_hunk_count"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// EvalReportMetrics is the metrics block: detection recall/precision, patch
// correctness, minimality, false update rate, abstention quality.
type EvalReportMetrics struct {
	DetectionRecall   float64        `json:"detection_recall"`
	Precision         float64        `json:"precision"`
	PatchCorrectness  float64        `json:"patch_correctness"`
	Minimality        EvalMinimality `json:"minimality"`
	FalseUpdateRate   float64        `json:"false_update_rate"`
	AbstentionQuality float64        `json:"abstention_quality"`
}

// Validate checks the metrics block, reproducing the zod issue paths.
func (m *EvalReportMetrics) Validate() []Issue {
	var issues []Issue
	Collect(&issues, unitIntervalIssue(m.DetectionRecall, []string{"detection_recall"}))
	Collect(&issues, unitIntervalIssue(m.Precision, []string{"precision"}))
	Collect(&issues, unitIntervalIssue(m.PatchCorrectness, []string{"patch_correctness"}))
	for _, iss := range m.Minimality.Validate() {
		iss.Path = append([]string{"minimality"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, unitIntervalIssue(m.FalseUpdateRate, []string{"false_update_rate"}))
	Collect(&issues, unitIntervalIssue(m.AbstentionQuality, []string{"abstention_quality"}))
	return issues
}

// EvalReportAbstainHealth is the abstain-health block: abstention and
// override rates.
type EvalReportAbstainHealth struct {
	AbstainRate  float64 `json:"abstain_rate"`
	OverrideRate float64 `json:"override_rate"`
}

// Validate checks the abstain-health block, reproducing the zod issue paths.
func (h *EvalReportAbstainHealth) Validate() []Issue {
	var issues []Issue
	Collect(&issues, unitIntervalIssue(h.AbstainRate, []string{"abstain_rate"}))
	Collect(&issues, unitIntervalIssue(h.OverrideRate, []string{"override_rate"}))
	return issues
}

// EvaluationReport is one policy-version evaluation report — the
// calibration-loop scoring unit (EVALUATION_REPORT_SCHEMA).
type EvaluationReport struct {
	Schema                   string                  `json:"schema"`
	PolicyVersion            int                     `json:"policy_version"`
	BaselineVersion          *int                    `json:"baseline_version,omitempty"`
	SplitID                  string                  `json:"split_id"`
	SampleSize               int                     `json:"sample_size"`
	Metrics                  EvalReportMetrics       `json:"metrics"`
	AbstainHealth            EvalReportAbstainHealth `json:"abstain_health"`
	VerifierDisagreementRate float64                 `json:"verifier_disagreement_rate"`
	ObjectiveScore           *float64                `json:"objective_score,omitempty"`
	ComputedAt               string                  `json:"computed_at"`
}

// Validate checks the report against EVALUATION_REPORT_SCHEMA (strict),
// reproducing the zod issue paths.
func (r *EvaluationReport) Validate() []Issue {
	var issues []Issue
	if r.Schema != EvaluationReportSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.evaluation-report/v1"`})
	}
	if r.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	if r.BaselineVersion != nil && *r.BaselineVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"baseline_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, RequiredString(r.SplitID, 1, []string{"split_id"}))
	if r.SampleSize < 1 {
		Collect(&issues, &Issue{Path: []string{"sample_size"}, Message: "Number must be greater than 0"})
	}
	for _, iss := range r.Metrics.Validate() {
		iss.Path = append([]string{"metrics"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range r.AbstainHealth.Validate() {
		iss.Path = append([]string{"abstain_health"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, unitIntervalIssue(r.VerifierDisagreementRate, []string{"verifier_disagreement_rate"}))
	if r.ObjectiveScore != nil {
		Collect(&issues, unitIntervalIssue(*r.ObjectiveScore, []string{"objective_score"}))
	}
	Collect(&issues, datetimeIssue(r.ComputedAt, []string{"computed_at"}))
	return issues
}

// ParseEvaluationReport strictly decodes and validates a
// `docsync.evaluation-report/v1` document (unknown JSON keys are rejected,
// mirroring z.strictObject).
func ParseEvaluationReport(data []byte) (*EvaluationReport, error) {
	var r EvaluationReport
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// EvaluationReportExample mirrors EVALUATION_REPORT_EXAMPLE (validated
// example: a policy-version report over the frozen split; golden data for
// byte-for-byte serializer checks).
func EvaluationReportExample() EvaluationReport {
	return EvaluationReport{
		Schema:          EvaluationReportSchemaID,
		PolicyVersion:   17,
		BaselineVersion: intPtr(16),
		SplitID:         "frozen-2026-10",
		SampleSize:      120,
		Metrics: EvalReportMetrics{
			DetectionRecall:   0.94,
			Precision:         0.91,
			PatchCorrectness:  0.88,
			Minimality:        EvalMinimality{MeanEditDistance: 42.5, MeanHunkCount: 1.3},
			FalseUpdateRate:   0.02,
			AbstentionQuality: 0.86,
		},
		AbstainHealth:            EvalReportAbstainHealth{AbstainRate: 0.12, OverrideRate: 0.03},
		VerifierDisagreementRate: 0.05,
		ObjectiveScore:           floatPtr(0.9),
		ComputedAt:               "2026-10-05T12:00:00Z",
	}
}
