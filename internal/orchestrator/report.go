// Ported from packages/core/src/orchestrator/report.ts (P5-T15; P7-T01; ARCH
// §12.1 parity contract; D3/D7/D2).
//
// Canonical report object: the Phase 7 CLI and the Phase 8 CI surfaces must
// never disagree about the facts — ONE pure derivation from the frozen
// RunOutcome (`docsync.run-outcome/v1`) produces the CanonicalReport, and BOTH
// renderers (RenderReportText, RenderReportMarkdown) consume ONLY that report
// — same facts, two surfaces. Every displayed value is a FIELD of the outcome
// (`generatedAt` is `early_signal.updated_at ?? finished_at ?? started_at` —
// a field, never generated); no clock, no I/O anywhere in this module.
//
// The report SHAPE lives in internal/schemas as the registered
// `docsync.canonical-report/v1`; this module re-exports the type (backward
// compatible) and builds against it, with a cheap schema self-check on every
// build (the run outcome itself is already schema-validated by the
// orchestrator before it lands here).
//
// Glyph semantics (contract, pinned by the orchestrator-report tests):
//   - `ok` — policy_outcome `ignore`: consistent, nothing to do (✓).
//   - `warn` — policy_outcome `suggest`/`auto`: potentially stale, an action
//     was proposed (the §12.1 "potentially stale" visual tier, ⚠).
//   - `locked` — policy_outcome `blocked`: the ownership floor (D2) —
//     surfaced, never touched; rendered with an explicit no-writer/no-patch
//     note (🔒).
//   - `review` — policy_outcome `review`: the run refused to guess (abstain,
//     don't guess) — rendered in the same ⚠ human-triage tier, while the
//     SEMANTIC glyph stays distinct for the CLI.
//
// The report carries the SEMANTIC glyph; the renderers use the §12.1 unicode
// visual language ✓/⚠/🔒 only. Constrained terminals map glyph → fallback
// (`[ok]`, `[!]`, `[locked]`) at DISPLAY time (Phase 7 CLI) — never inside
// the report or the renderers.
//
// D3 display pin: every displayed confidence carries its producing policy
// version AND the routed decision-engine model — `96% (policy v17, model
// <model>)` — and ONLY when the document has a confidence AND provenance
// carries a decision-engine binding; otherwise confidenceDisplay is null
// (never a bare, unpinned percentage). The row additionally carries the
// additive optional modelBinding pin (P7-T03).
//
// D7 display pin: `unknown_coverage` paths (changed paths matched by NO
// manifest mapping) render as their own "Unknown coverage (D7)" section in
// both surfaces — surfaced, never silenced, sorted + deduped here as a second
// deterministic guard.
//
// P5-T17 display pin: when the outcome carries `usage`, both surfaces render
// the token counts with the producing `policy_version` — attribution stays
// attached to the policy that incurred it (ARCH §10.6).

package orchestrator

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------------- shapes */

// ReportGlyph is the semantic display glyph of one report row (the schema type,
// re-exported for the report API parity with the TS barrel).
type ReportGlyph = schemas.ReportGlyph

// ReportRow is one per-document row of the canonical report (documents order).
type ReportRow = schemas.CanonicalReportRow

// CanonicalReportUsage is the usage attribution surface (verbatim
// RunOutcome.usage shape, P5-T17).
type CanonicalReportUsage = schemas.RunUsage

/* ---------------------------------------------------------------- derivation */

// GlyphForOutcome is the pinned glyph contract (module doc): `ignore` → ok;
// `review` → review; `suggest`/`auto` → warn; `blocked` → locked (the
// ownership floor, D2 — surfaced, never touched). Total over PipelineOutcome.
func GlyphForOutcome(outcome schemas.PipelineOutcome) schemas.ReportGlyph {
	switch outcome {
	case schemas.PipelineOutcomeIgnore:
		return schemas.ReportGlyphOk
	case schemas.PipelineOutcomeReview:
		return schemas.ReportGlyphReview
	case schemas.PipelineOutcomeSuggest, schemas.PipelineOutcomeAuto:
		return schemas.ReportGlyphWarn
	case schemas.PipelineOutcomeBlocked:
		return schemas.ReportGlyphLocked
	default:
		// Unreachable for a schema-validated outcome; fail closed to the
		// most conservative (human-triage) glyph.
		return schemas.ReportGlyphReview
	}
}

// RoutedDecisionModel is the routed decision-engine model from provenance,
// when present (D3).
func RoutedDecisionModel(bindings []schemas.RunModelBinding) *string {
	for i := range bindings {
		if bindings[i].Role == "decision-engine" {
			model := bindings[i].Model
			return &model
		}
	}
	return nil
}

// ConfidenceDisplayOf is the D3 display pin: `<pct>% (policy v<n>, model
// <model>)` — rounded to the whole percent, pinned to the producing policy
// version and the ROUTED decision-engine model from provenance. Null when the
// document carries no confidence or provenance has no decision-engine binding
// (a bare percentage is never displayed — D3).
func ConfidenceDisplayOf(doc schemas.RunDocument, policyVersion int, routedModel *string) *string {
	if doc.Confidence == nil || routedModel == nil {
		return nil
	}
	display := fmt.Sprintf("%d%% (policy v%d, model %s)",
		int(math.Round(*doc.Confidence*100)), policyVersion, *routedModel)
	return &display
}

// rowFor builds one report row (TS rowFor). Row detail precedence
// (deterministic): the document's abstain detail (the refusal evidence), else
// the first run-level finding matching the doc (the "potentially stale"
// evidence), else null. Evidence tokens are derived purely from the outcome
// record (routing → decision → baseline → patch).
func rowFor(outcome *schemas.RunOutcome, doc schemas.RunDocument) schemas.CanonicalReportRow {
	var finding *schemas.RunFinding
	for i := range outcome.Findings {
		if outcome.Findings[i].DocPath != nil && *outcome.Findings[i].DocPath == doc.DocPath {
			finding = &outcome.Findings[i]
			break
		}
	}
	evidence := []string{}
	if doc.Routing != nil {
		evidence = append(evidence, "routing:"+string(*doc.Routing))
	}
	if doc.DecisionID != nil {
		evidence = append(evidence, "decision:"+*doc.DecisionID)
	}
	if doc.BaselineRun != nil {
		evidence = append(evidence, "baseline:"+string(*doc.BaselineRun))
	}
	if doc.Verification != nil && doc.Verification.PatchRef != nil {
		evidence = append(evidence, "patch:"+*doc.Verification.PatchRef)
	}
	routedModel := RoutedDecisionModel(outcome.Provenance.ModelBindings)
	row := schemas.CanonicalReportRow{
		DocPath:           doc.DocPath,
		Glyph:             GlyphForOutcome(doc.PolicyOutcome),
		Outcome:           doc.PolicyOutcome,
		Confidence:        doc.Confidence,
		ConfidenceDisplay: ConfidenceDisplayOf(doc, outcome.Provenance.PolicyVersion, routedModel),
		Detail:            nil,
		Evidence:          evidence,
	}
	// P7-T03: the routed decision-engine model pin, when one exists.
	if routedModel != nil {
		row.ModelBinding = routedModel
	}
	// detail: the abstain detail (refusal evidence), else the first matching
	// finding detail, else null.
	if doc.Abstain != nil {
		detail := doc.Abstain.Detail
		row.Detail = &detail
	} else if finding != nil {
		detail := finding.Detail
		row.Detail = &detail
	}
	return row
}

// NormalizeUnknownCoverage is the D7 second guard: deterministic
// unknown-coverage display set (deduped + sorted).
func NormalizeUnknownCoverage(paths []string) []string {
	if paths == nil {
		return []string{}
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// BuildCanonicalReport builds the canonical report from a validated RunOutcome
// — pure, total, deterministic: same outcome → same report. Rows keep the
// outcome's document order; every member is copied from the outcome (no
// derivation beyond the documented glyph/detail/evidence/D3 rules). The cheap
// CANONICAL_REPORT_SCHEMA self-check (P7-T01) asserts the built object against
// the registered schema — structural drift fails loudly.
func BuildCanonicalReport(outcome *schemas.RunOutcome) (*schemas.CanonicalReport, error) {
	rows := make([]schemas.CanonicalReportRow, 0, len(outcome.Documents))
	for i := range outcome.Documents {
		rows = append(rows, rowFor(outcome, outcome.Documents[i]))
	}
	modelBindings := make([]schemas.RunModelBinding, 0, len(outcome.Provenance.ModelBindings))
	for i := range outcome.Provenance.ModelBindings {
		binding := outcome.Provenance.ModelBindings[i]
		copied := schemas.RunModelBinding{Role: binding.Role, Model: binding.Model}
		if binding.Provider != nil {
			provider := *binding.Provider
			copied.Provider = &provider
		}
		modelBindings = append(modelBindings, copied)
	}
	abstains := make([]schemas.RunAbstain, 0, len(outcome.Abstains))
	for i := range outcome.Abstains {
		abstains = append(abstains, copyAbstain(outcome.Abstains[i]))
	}
	findings := make([]schemas.RunFinding, 0, len(outcome.Findings))
	for i := range outcome.Findings {
		findings = append(findings, copyFinding(outcome.Findings[i]))
	}
	degradations := make([]schemas.Degradation, 0, len(outcome.Degradations))
	for i := range outcome.Degradations {
		degradation := outcome.Degradations[i]
		degradations = append(degradations, schemas.Degradation{
			Stage: degradation.Stage, Kind: degradation.Kind, Detail: degradation.Detail,
		})
	}

	generatedAt := outcome.EarlySignal.UpdatedAt
	if generatedAt == nil {
		generatedAt = outcome.FinishedAt
	}
	if generatedAt == nil {
		generatedAt = outcome.StartedAt
	}

	report := &schemas.CanonicalReport{
		RunID:           outcome.RunID,
		Key:             outcome.Key,
		Status:          outcome.Status,
		GeneratedAt:     generatedAt,
		PolicyVersion:   outcome.Provenance.PolicyVersion,
		ModelBindings:   modelBindings,
		Rows:            rows,
		Abstains:        abstains,
		Findings:        findings,
		Degradations:    degradations,
		UnknownCoverage: NormalizeUnknownCoverage(outcome.UnknownCoverage),
	}
	if outcome.Usage != nil {
		usage := *outcome.Usage
		if usage.TotalTokens != nil {
			total := *usage.TotalTokens
			usage.TotalTokens = &total
		}
		if usage.Cost != nil {
			cost := *usage.Cost
			usage.Cost = &cost
		}
		report.Usage = &usage
	}

	// The cheap self-check: the built object must validate against the
	// registered canonical-report schema (structural drift fails loud).
	if issues := report.Validate(); len(issues) > 0 {
		return nil, reportDriftError(issues)
	}
	return report, nil
}

// copyAbstain deep-copies one run-level abstain record.
func copyAbstain(abstain schemas.RunAbstain) schemas.RunAbstain {
	copied := schemas.RunAbstain{
		AbstainID: abstain.AbstainID,
		Reason:    abstain.Reason,
		Detail:    abstain.Detail,
	}
	if abstain.DocPath != nil {
		docPath := *abstain.DocPath
		copied.DocPath = &docPath
	}
	if abstain.DecisionID != nil {
		decisionID := *abstain.DecisionID
		copied.DecisionID = &decisionID
	}
	if abstain.QueueEventID != nil {
		queueEventID := *abstain.QueueEventID
		copied.QueueEventID = &queueEventID
	}
	return copied
}

// copyFinding deep-copies one run-level finding.
func copyFinding(finding schemas.RunFinding) schemas.RunFinding {
	copied := schemas.RunFinding{Kind: finding.Kind, Detail: finding.Detail}
	if finding.DocPath != nil {
		docPath := *finding.DocPath
		copied.DocPath = &docPath
	}
	if finding.PatchRef != nil {
		patchRef := *finding.PatchRef
		copied.PatchRef = &patchRef
	}
	return copied
}

// reportDriftError renders a built-report schema failure as a loud coded
// error (the TS ZodError from CANONICAL_REPORT_SCHEMA.parse).
func reportDriftError(issues []schemas.Issue) error {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Error())
	}
	return docutil.SchemaError(
		"canonical report failed self-validation (CANONICAL_REPORT_SCHEMA) — refusing to build an invalid report: %s",
		strings.Join(parts, "; "))
}

/* ----------------------------------------------------------------- renderers */

// glyphChar is the §12.1 unicode visual language for the RENDERERS only.
// `review` shares the ⚠ human-triage tier (the §12.1 visual language has
// exactly three glyphs); the SEMANTIC glyph stays on the report row.
var glyphChar = map[schemas.ReportGlyph]string{
	schemas.ReportGlyphOk:     "✓",
	schemas.ReportGlyphWarn:   "⚠",
	schemas.ReportGlyphLocked: "🔒",
	schemas.ReportGlyphReview: "⚠",
}

// LockedNote is the ownership-floor note locked rows carry (D2: surfaced,
// never touched).
const LockedNote = "locked (surfaced, never touched)"

// rowBody is the shared row body — the IDENTICAL facts both surfaces expose:
// path, detail, the locked never-touched note, the D3-pinned confidence
// display. Evidence tokens stay data on the row (machine-readable), never
// rendered prose.
func rowBody(row schemas.CanonicalReportRow) string {
	parts := []string{row.DocPath}
	if row.Detail != nil {
		parts = append(parts, *row.Detail)
	}
	if row.Glyph == schemas.ReportGlyphLocked {
		parts = append(parts, LockedNote)
	}
	if row.ConfidenceDisplay != nil {
		parts = append(parts, *row.ConfidenceDisplay)
	}
	return strings.Join(parts, " — ")
}

func headerLines(report *schemas.CanonicalReport, markdown bool) []string {
	title := "Documentation consistency"
	meta := "run " + report.RunID + " · status " + string(report.Status) + fmt.Sprintf(" · policy v%d", report.PolicyVersion)
	if markdown {
		title = "## Documentation consistency"
		meta = "run `" + report.RunID + "` · status `" + string(report.Status) + "`" + fmt.Sprintf(" · policy v%d", report.PolicyVersion)
	}
	lines := []string{title, "", meta}
	if report.GeneratedAt != nil {
		if markdown {
			lines = append(lines, "as of `"+*report.GeneratedAt+"`")
		} else {
			lines = append(lines, "as of "+*report.GeneratedAt)
		}
	}
	lines = append(lines, "")
	return lines
}

func reportUnknownCoverageLines(report *schemas.CanonicalReport, markdown bool) []string {
	if len(report.UnknownCoverage) == 0 {
		return nil
	}
	heading := "Unknown coverage (D7 — changed paths matched by no manifest mapping):"
	if markdown {
		heading = "### Unknown coverage (D7)"
	}
	lines := []string{heading, ""}
	for _, path := range report.UnknownCoverage {
		lines = append(lines, "- "+path)
	}
	lines = append(lines, "")
	return lines
}

func usageLines(report *schemas.CanonicalReport, markdown bool) []string {
	if report.Usage == nil {
		return nil
	}
	usage := report.Usage
	line := fmt.Sprintf("Usage: %d prompt / %d completion tokens (policy v%d)",
		usage.PromptTokens, usage.CompletionTokens, usage.PolicyVersion)
	if usage.Cost != nil {
		line += " · cost " + strconv.FormatFloat(*usage.Cost, 'g', -1, 64)
	}
	if markdown {
		return []string{"### Usage attribution", "", line, ""}
	}
	return []string{line, ""}
}

// RenderReportText renders the §12.1 transcript (plain text) from ONLY the
// canonical report. Constrained terminals: the Phase 7 CLI maps ✓→`[ok]`,
// ⚠→`[!]`, 🔒→`[locked]` at DISPLAY time — the renderer always emits the
// unicode set. The check renderer (check-render.go) supersedes this for the
// CLI check transcript; this pair stays untouched for backward compatibility.
func RenderReportText(report *schemas.CanonicalReport) string {
	lines := headerLines(report, false)
	for i := range report.Rows {
		lines = append(lines, glyphChar[report.Rows[i].Glyph]+" "+rowBody(report.Rows[i]))
	}
	lines = append(lines, "")
	lines = append(lines, reportUnknownCoverageLines(report, false)...)
	lines = append(lines, usageLines(report, false)...)
	return trimEndNewline(lines)
}

// RenderReportMarkdown renders the same facts as GitHub-flavored markdown
// (PR/check surfaces).
func RenderReportMarkdown(report *schemas.CanonicalReport) string {
	lines := headerLines(report, true)
	for i := range report.Rows {
		lines = append(lines, "- "+glyphChar[report.Rows[i].Glyph]+" "+rowBody(report.Rows[i]))
	}
	lines = append(lines, "")
	lines = append(lines, reportUnknownCoverageLines(report, true)...)
	lines = append(lines, usageLines(report, true)...)
	return trimEndNewline(lines)
}

// trimEndNewline is the TS `${lines.join('\n').trimEnd()}\n`.
func trimEndNewline(lines []string) string {
	joined := strings.Join(lines, "\n")
	return strings.TrimRightFunc(joined, unicode.IsSpace) + "\n"
}
