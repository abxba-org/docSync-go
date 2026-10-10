package schemas

// Ported from packages/schemas/src/v1/canonical-report.ts
// (CANONICAL_REPORT_SCHEMA + CANONICAL_REPORT_ROW_SCHEMA + REPORT_GLYPH +
// CANONICAL_REPORT_EXAMPLE).
//
// Canonical report — `docsync.canonical-report/v1` (P7-T01; ARCH §12.1 parity
// contract; D3/D7/D2). The shape was MOVED here verbatim from the core
// orchestrator (`@docsync/core` report.ts): the Phase 7 CLI and Phase 8 CI
// surfaces share ONE pure derivation from the frozen `RunOutcome`
// (`docsync.run-outcome/v1`) into this report, and both render it — same
// facts, two surfaces. The report is a derived DISPLAY artifact (never
// persisted as a decision record), so its vocabulary reuses the run-outcome
// schemas (OD-2: import, never duplicate): `key` (RunKey), `status`
// (RunStatus), `modelBindings`/`abstains`/`findings`/`degradations`/`usage`
// verbatim (run-outcome.go), and the per-row `outcome` pins the five-value
// PipelineOutcome vocabulary.
//
// Closed enums where the underlying values are closed: `glyph` (ok | warn |
// locked | review — the SEMANTIC display glyph contract; renderers map it to
// the §12.1 unicode visual language ✓/⚠/🔒 or the `[ok]`/`[!]`/`[locked]`
// fallback at DISPLAY time), `status` (RunStatus), `outcome`
// (PipelineOutcome).
//
// D3 (P7-T03 pinning): every row's `confidenceDisplay` is the pin string
// `<pct>% (policy v<n>, model <model>)` or null — a bare percentage is never
// carried. The additive OPTIONAL `modelBinding` pins the routed
// decision-engine model that produced the confidence (absent when unpinnable),
// so the renderer can pin abstention rows too. All optional — absent = valid,
// existing producers unaffected. The additive OPTIONAL `advisories` carries
// the Phase-12 report-only advisory surface (P12-T13/F5-1; OD-R30; V-P12-12):
// the non-blocking `report_only_until_reviewed` org-floor messages a run
// rendered, emitted by `docsync check --json` alongside the report itself.

// CanonicalReportSchemaID is the wire schema literal (TS:
// CANONICAL_REPORT_SCHEMA_ID, 'docsync.canonical-report/v1').
const CanonicalReportSchemaID = "docsync.canonical-report/v1"

// ReportGlyph is the semantic display glyph of one report row — the pinned
// renderer contract (TS: REPORT_GLYPH_VALUES, z.infer CanonicalReportGlyph).
type ReportGlyph string

// ReportGlyphValues lists all glyphs in canonical order (TS:
// REPORT_GLYPH_VALUES).
const (
	ReportGlyphOk     ReportGlyph = "ok"
	ReportGlyphWarn   ReportGlyph = "warn"
	ReportGlyphLocked ReportGlyph = "locked"
	ReportGlyphReview ReportGlyph = "review"
)

// ReportGlyphValues lists all glyphs in canonical order.
var ReportGlyphValues = []ReportGlyph{
	ReportGlyphOk, ReportGlyphWarn, ReportGlyphLocked, ReportGlyphReview,
}

// IsValidReportGlyph reports membership in REPORT_GLYPH.
func IsValidReportGlyph(s string) bool {
	for _, v := range ReportGlyphValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// CanonicalReportRow is one per-document row of the canonical report
// (documents order; TS: CANONICAL_REPORT_ROW_SCHEMA, z.infer
// CanonicalReportRow). `confidence`/`confidenceDisplay`/`detail` are nullable
// (required keys, null allowed); `modelBinding` is optional (absent = valid).
type CanonicalReportRow struct {
	DocPath           string          `json:"docPath"`
	Glyph             ReportGlyph     `json:"glyph"`
	Outcome           PipelineOutcome `json:"outcome"`
	Confidence        *float64        `json:"confidence"`
	ConfidenceDisplay *string         `json:"confidenceDisplay"`
	ModelBinding      *string         `json:"modelBinding,omitempty"`
	Detail            *string         `json:"detail"`
	Evidence          []string        `json:"evidence"`
}

// Validate checks the row against CANONICAL_REPORT_ROW_SCHEMA (strict).
func (r *CanonicalReportRow) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(r.DocPath, 1, []string{"docPath"}))
	Collect(&issues, CheckEnum(string(r.Glyph), stringsOf(ReportGlyphValues), []string{"glyph"}))
	Collect(&issues, CheckEnum(string(r.Outcome), stringsOf(PipelineOutcomeValues), []string{"outcome"}))
	if r.Confidence != nil {
		if *r.Confidence < 0 {
			Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be greater than or equal to 0"})
		}
		if *r.Confidence > 1 {
			Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be less than or equal to 1"})
		}
	}
	if r.ConfidenceDisplay != nil {
		Collect(&issues, RequiredString(*r.ConfidenceDisplay, 1, []string{"confidenceDisplay"}))
	}
	if r.ModelBinding != nil {
		Collect(&issues, RequiredString(*r.ModelBinding, 1, []string{"modelBinding"}))
	}
	if r.Detail != nil {
		Collect(&issues, RequiredString(*r.Detail, 1, []string{"detail"}))
	}
	for i, e := range r.Evidence {
		Collect(&issues, RequiredString(e, 1, []string{"evidence", itoa(i)}))
	}
	return issues
}

// CanonicalReport is the canonical report object — the ARCH §12.1 parity
// contract (TS: CANONICAL_REPORT_SCHEMA, z.infer CanonicalReport). Field order
// matches the TS declaration order so serialized bytes stay byte-stable with
// the TypeScript emitter.
//
// `advisories` is OPTIONAL and ADDITIVE (P12-T13/F5-1; OD-R30; V-P12-12): a
// repository whose org policy has an unreviewed report-only floor carries the
// key; every other repository (no org policy, or a floor declared
// `reviewed: true`) omits it and validates unchanged, so existing producers
// and consumers are unaffected. The CLI validates the EMITTED object (report
// + advisories) through this schema, so a consumer re-parsing `check --json`
// stdout with this schema never crashes. Element strings are free-form
// (z.array(z.string()) — no min length), matching the TS surface.
type CanonicalReport struct {
	RunID           string               `json:"runId"`
	Key             RunKey               `json:"key"`
	Status          RunStatus            `json:"status"`
	GeneratedAt     *string              `json:"generatedAt"`
	PolicyVersion   int                  `json:"policyVersion"`
	ModelBindings   []RunModelBinding    `json:"modelBindings"`
	Rows            []CanonicalReportRow `json:"rows"`
	Abstains        []RunAbstain         `json:"abstains"`
	Findings        []RunFinding         `json:"findings"`
	Degradations    []Degradation        `json:"degradations"`
	UnknownCoverage []string             `json:"unknownCoverage"`
	Usage           *RunUsage            `json:"usage,omitempty"`
	Advisories      *[]string            `json:"advisories,omitempty"`
}

// Validate checks the report against CANONICAL_REPORT_SCHEMA (strict),
// reproducing the zod issue paths.
func (r *CanonicalReport) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(r.RunID, 1, []string{"runId"}))
	for _, iss := range r.Key.Validate() {
		iss.Path = append([]string{"key"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, CheckEnum(string(r.Status), stringsOf(RunStatusValues), []string{"status"}))
	// `early_signal.updated_at ?? finished_at ?? started_at` — never generated;
	// z.string().datetime().nullable() → optionalDatetimeIssue.
	Collect(&issues, optionalDatetimeIssue(r.GeneratedAt, []string{"generatedAt"}))
	if r.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policyVersion"}, Message: "Number must be greater than 0"})
	}
	// Provenance model bindings verbatim (D3/DX-P2-2 routed halves).
	for i := range r.ModelBindings {
		for _, iss := range r.ModelBindings[i].Validate() {
			iss.Path = append([]string{"modelBindings", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Rows {
		for _, iss := range r.Rows[i].Validate() {
			iss.Path = append([]string{"rows", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Abstains {
		for _, iss := range r.Abstains[i].Validate() {
			iss.Path = append([]string{"abstains", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Findings {
		for _, iss := range r.Findings[i].Validate() {
			iss.Path = append([]string{"findings", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Degradations {
		for _, iss := range r.Degradations[i].Validate() {
			iss.Path = append([]string{"degradations", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	// D7: changed paths matched by no manifest mapping (sorted, deduped).
	for i, p := range r.UnknownCoverage {
		Collect(&issues, RequiredString(p, 1, []string{"unknownCoverage", itoa(i)}))
	}
	if r.Usage != nil {
		for _, iss := range r.Usage.Validate() {
			iss.Path = append([]string{"usage"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	// The additive optional advisory surface needs no per-element check:
	// z.array(z.string()) constrains only the element TYPE (enforced at
	// StrictDecode), and message strings are free-form. Absent = valid.
	return issues
}

// ParseCanonicalReport strictly decodes and validates
// `docsync.canonical-report/v1` bytes (unknown JSON keys are rejected,
// mirroring z.strictObject).
func ParseCanonicalReport(data []byte) (*CanonicalReport, error) {
	var r CanonicalReport
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// CanonicalReportExample mirrors CANONICAL_REPORT_EXAMPLE (validated display
// example mirroring the run-outcome example rows; golden data for
// byte-for-byte serializer checks). The example deliberately carries no
// `advisories` (mirrors the absent optional `usage`): the field is optional
// by contract, so a minimal example stays valid.
func CanonicalReportExample() CanonicalReport {
	generatedAt := "2026-10-04T22:30:47Z"
	paymentsConfidence := 0.96
	paymentsBinding := "z-ai/glm-4.7"
	apiConfidence := 0.31
	apiBinding := "z-ai/glm-4.7"
	cliConfidence := 0.66
	cliBinding := "z-ai/glm-4.7"
	cliDetail := "documentation_impact probability 0.66 sits below the review threshold (0.72)"
	decisionID := "d_01J9ZK7Q2N"
	queueEventID := "q_01J9ZK8A0P"
	return CanonicalReport{
		RunID:         "run_01J9ZK6X8A",
		Key:           RunKeyExample(),
		Status:        RunStatusCompleted,
		GeneratedAt:   &generatedAt,
		PolicyVersion: 17,
		ModelBindings: []RunModelBinding{
			{Role: "decision-engine", Model: "z-ai/glm-4.7", Provider: strPtr("z-ai")},
			{Role: "judge", Model: "z-ai/glm-4.7", Provider: strPtr("z-ai")},
			{Role: "writer", Model: "anthropic/claude-sonnet-4.5", Provider: strPtr("anthropic")},
		},
		Rows: []CanonicalReportRow{
			{
				DocPath:           "docs/payments.md",
				Glyph:             ReportGlyphWarn,
				Outcome:           PipelineOutcomeAuto,
				Confidence:        &paymentsConfidence,
				ConfidenceDisplay: strPtr("96% (policy v17, model z-ai/glm-4.7)"),
				ModelBinding:      &paymentsBinding,
				Detail:            nil,
				Evidence: []string{
					"routing:CONTINUE",
					"decision:d_01J9ZK7Q2M",
					"baseline:exact-sha",
					"patch:state/docs/payments.md#proposal-d_01J9ZK7Q2M",
				},
			},
			{
				DocPath:           "docs/api.md",
				Glyph:             ReportGlyphOk,
				Outcome:           PipelineOutcomeIgnore,
				Confidence:        &apiConfidence,
				ConfidenceDisplay: strPtr("31% (policy v17, model z-ai/glm-4.7)"),
				ModelBinding:      &apiBinding,
				Detail:            nil,
				Evidence:          []string{"routing:IGNORE", "baseline:same-tree"},
			},
			{
				DocPath:           "docs/cli.md",
				Glyph:             ReportGlyphReview,
				Outcome:           PipelineOutcomeReview,
				Confidence:        &cliConfidence,
				ConfidenceDisplay: strPtr("66% (policy v17, model z-ai/glm-4.7)"),
				ModelBinding:      &cliBinding,
				Detail:            &cliDetail,
				Evidence:          []string{"routing:REVIEW", "decision:d_01J9ZK7Q2N", "baseline:patch-id-match"},
			},
		},
		Abstains: []RunAbstain{
			{
				AbstainID:    "ab_01J9ZK7Q2N",
				DocPath:      strPtr("docs/cli.md"),
				Reason:       "insufficient_confidence",
				Detail:       "routed to the human-review queue with evidence attached (ARCH §5.2)",
				DecisionID:   &decisionID,
				QueueEventID: &queueEventID,
			},
		},
		Findings:        []RunFinding{},
		Degradations:    []Degradation{},
		UnknownCoverage: []string{},
	}
}
