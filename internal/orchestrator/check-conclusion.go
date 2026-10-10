// Ported from packages/core/src/orchestrator/check-conclusion.ts (P8-T08..T11
// prep; ARCH §11.3, §12.1; PLAN §8.3). PURE module — no I/O, no AI, no clocks.
//
// Exit-code sibling: exit-codes.go decides the CLI process code from the SAME
// canonical report; this module decides the GitHub check-run `conclusion`
// from that report plus call-site policy flags. Local and CI never disagree
// about the facts (one renderer, one report).
//
// Conclusion mapping table (binding precedence, top wins):
//
//	| # | condition (in precedence order)                       | conclusion | reason |
//	|---|-------------------------------------------------------|------------|--------|
//	| 1 | configuration error (invalid manifest/config)         | failure    | fail closed, D8: a config error is an error before any AI stage |
//	| 2 | runtime inconclusive (analyzer outage, stage failure) | neutral    | D14 fail-closed inconclusive: degrade to LESS automation; explicitly labeled inconclusive — NEVER "no docs affected" |
//	| 3 | enforcement mode AND warn-family rows present         | failure    | D10 enforcement: strict OR enforcementOptIn OR any row outcome=blocked / glyph=locked |
//	| 4 | warn-family rows present, advisory (default)          | neutral    | D10 default: advisory mode reports drift without failing CI |
//	| 5 | clean (no warn-family rows)                           | success    | no documentation drift detected |
//
// Warn-family rows: outcome `suggest`/`review` or glyph `warn`/`review`.
// Enforcement-signaling rows: outcome `blocked` or glyph `locked`.

package orchestrator

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CheckRunConclusion is a GitHub check-run conclusion this engine may emit.
type CheckRunConclusion string

// The check-run conclusions.
const (
	ConclusionSuccess CheckRunConclusion = "success"
	ConclusionNeutral CheckRunConclusion = "neutral"
	ConclusionFailure CheckRunConclusion = "failure"
)

// ResolveCheckConclusionOptions are the caller-owned policy flags of
// ResolveCheckConclusion.
type ResolveCheckConclusionOptions struct {
	// Strict is the strict enforcement flag (CLI `--strict` / policy
	// enforcement).
	Strict bool
	// ConfigError marks a configuration error detected before any AI stage
	// (D8).
	ConfigError bool
	// RuntimeInconclusive marks runtime inconclusiveness: analyzer outage /
	// stage failure (D14).
	RuntimeInconclusive bool
	// EnforcementOptIn marks a repository that opted into enforcement without
	// the strict flag (D10).
	EnforcementOptIn bool
}

// ResolvedCheckConclusion is the resolved conclusion plus the reason trace the
// payload summary cites.
type ResolvedCheckConclusion struct {
	Conclusion CheckRunConclusion
	// Reason is the human-readable reason naming the decision rule
	// (D8/D10/D14).
	Reason string
	// Inconclusive is set only for the D14 inconclusive branch.
	Inconclusive bool
}

// IsWarnFamilyRow reports a warn-family row: drift surfaced without
// enforcement teeth (D10).
func IsWarnFamilyRow(row schemas.CanonicalReportRow) bool {
	return row.Glyph == schemas.ReportGlyphWarn ||
		row.Glyph == schemas.ReportGlyphReview ||
		row.Outcome == schemas.PipelineOutcomeSuggest ||
		row.Outcome == schemas.PipelineOutcomeReview
}

// IsEnforcementRow reports an enforcement-signaling row: the ownership floor
// answered `blocked`.
func IsEnforcementRow(row schemas.CanonicalReportRow) bool {
	return row.Outcome == schemas.PipelineOutcomeBlocked || row.Glyph == schemas.ReportGlyphLocked
}

// HasWarnFamilyRows reports whether any row of the report is warn-family
// (suggest/review/warn glyph).
func HasWarnFamilyRows(report *schemas.CanonicalReport) bool {
	for i := range report.Rows {
		if IsWarnFamilyRow(report.Rows[i]) {
			return true
		}
	}
	return false
}

// IsEnforcementEngaged reports whether enforcement applies: strict flag,
// opt-in, or any blocked/locked row (D10).
func IsEnforcementEngaged(report *schemas.CanonicalReport, opts ResolveCheckConclusionOptions) bool {
	if opts.Strict || opts.EnforcementOptIn {
		return true
	}
	for i := range report.Rows {
		if IsEnforcementRow(report.Rows[i]) {
			return true
		}
	}
	return false
}

// ResolveCheckConclusion resolves the GitHub check-run conclusion from the
// canonical report (precedence table in the module doc). Pure, zero AI.
func ResolveCheckConclusion(
	report *schemas.CanonicalReport,
	opts *ResolveCheckConclusionOptions,
) ResolvedCheckConclusion {
	resolvedOpts := ResolveCheckConclusionOptions{}
	if opts != nil {
		resolvedOpts = *opts
	}
	// 1. D8: configuration error — fail closed BEFORE any AI stage ran.
	if resolvedOpts.ConfigError {
		return ResolvedCheckConclusion{
			Conclusion: ConclusionFailure,
			Reason:     "configuration error (D8): config error fails closed before any AI stage",
		}
	}
	// 2. D14: runtime inconclusive — neutral + explicit inconclusive label.
	if resolvedOpts.RuntimeInconclusive {
		return ResolvedCheckConclusion{
			Conclusion: ConclusionNeutral,
			Reason: "runtime inconclusive (D14): analyzer/stage failure degrades to less automation; " +
				"run reported as inconclusive, never as \"no docs affected\"",
			Inconclusive: true,
		}
	}
	warnFamily := HasWarnFamilyRows(report)
	// 3. D10 enforcement: strict / opt-in / any blocked-locked row + drift →
	// failure.
	if warnFamily && IsEnforcementEngaged(report, resolvedOpts) {
		return ResolvedCheckConclusion{
			Conclusion: ConclusionFailure,
			Reason: "enforcement mode (D10): documentation drift present under strict/opt-in/" +
				"blocked-locked policy — failing the check",
		}
	}
	// 4. D10 default: advisory — drift reported, check stays neutral.
	if warnFamily {
		return ResolvedCheckConclusion{
			Conclusion: ConclusionNeutral,
			Reason:     "advisory mode (D10 default): documentation drift reported without failing CI",
		}
	}
	// 5. Clean.
	return ResolvedCheckConclusion{
		Conclusion: ConclusionSuccess,
		Reason:     "clean: no documentation drift detected",
	}
}

/* --------------------------------------------------------- summary payloads */

// ConfigErrorDiagnostic is one manifest/config diagnostic (file + message +
// optional position).
type ConfigErrorDiagnostic struct {
	File    string
	Message string
	Line    *int
	Column  *int
}

// CheckSummary is the title+summary pair (BuildInconclusiveSummary carries no
// details text).
type CheckSummary struct {
	Title   string
	Summary string
}

// CheckSummaryPayload is the payload triple a check run accepts for a
// completed config-error run.
type CheckSummaryPayload struct {
	Title   string
	Summary string
	Text    string
}

// ConfigErrorTitle is the fixed config-error check title.
const ConfigErrorTitle = "docsync: configuration error"

// BuildConfigErrorSummary builds the config-error check payload (D8): pure,
// zero AI. Text lists every offending `file:line: message` line plus
// remediation — fix the manifest via a normal PR (ARCH §11.3); the check never
// patches config.
func BuildConfigErrorSummary(diagnostics []ConfigErrorDiagnostic) CheckSummaryPayload {
	lines := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		at := ""
		if diagnostic.Line != nil {
			at = fmt.Sprintf(":%d", *diagnostic.Line)
			if diagnostic.Column != nil {
				at += fmt.Sprintf(":%d", *diagnostic.Column)
			}
		}
		lines = append(lines, fmt.Sprintf("`%s%s`: %s", diagnostic.File, at, diagnostic.Message))
	}
	remediation := "Remediation: fix the docsync manifest/configuration via a normal pull request " +
		"(ARCH §11.3) — the check itself never modifies configuration."
	textLines := []string{"docsync could not start: the manifest/configuration is invalid."}
	if len(lines) > 0 {
		textLines = append(textLines, "", "Offending entries:")
		for _, line := range lines {
			textLines = append(textLines, "- "+line)
		}
	}
	textLines = append(textLines, "", remediation)
	entryWord := "ies"
	if len(diagnostics) == 1 {
		entryWord = "y"
	}
	return CheckSummaryPayload{
		Title: ConfigErrorTitle,
		Summary: "Configuration error (D8): docsync fails closed before any AI stage. " +
			fmt.Sprintf("%d offending entr%s listed in the details.", len(diagnostics), entryWord),
		Text: strings.Join(textLines, "\n"),
	}
}

// BuildInconclusiveSummary builds the explicit-inconclusive payload (D14): the
// run could not produce a trustworthy result (e.g. analyzer outage).
// SELF-ASSERT RULE: the summary must never read as "no docs affected" — that
// claim is unavailable when the run is inconclusive; it labels the run
// inconclusive instead.
func BuildInconclusiveSummary(reason string) CheckSummary {
	return CheckSummary{
		Title: "docsync: documentation check (inconclusive)",
		Summary: fmt.Sprintf(
			"Inconclusive (D14): %s — docsync could not produce a trustworthy result; "+
				"this is NOT a pass on documentation consistency.", reason),
	}
}

// BuildCheckPayloadOptions are the options of BuildCheckPayload.
type BuildCheckPayloadOptions struct {
	// Partial is the early-signal vs final seam (documented, caller-owned):
	// the orchestrator may publish an in-progress check with a PARTIAL
	// canonical report (`Partial: true` marks the title as an early signal);
	// the completed run later upserts the SAME check run with the final
	// report. This module stays agnostic — it renders whatever report it is
	// handed.
	Partial bool
}

// CheckTitle is the fixed documentation-check title.
const CheckTitle = "docsync: documentation check"

// BuildCheckPayload builds the check-run output payload from ONLY the
// canonical report, through the shared Phase 7 renderer in quiet mode
// (`quiet: true` — abstained rows collapse to the suppression line; see
// check-render.go P7-T05). Same facts as the local CLI transcript,
// GitHub-flavored markdown.
func BuildCheckPayload(
	report *schemas.CanonicalReport,
	opts *BuildCheckPayloadOptions,
) (CheckSummaryPayload, error) {
	if report == nil {
		return CheckSummaryPayload{}, docutil.InternalError(
			"buildCheckPayload: canonical report is nil — refusing to build a check payload without a report (fail closed)")
	}
	quiet := true
	markdown := RenderCheckMarkdown(report, &CheckRenderOptions{Quiet: &quiet})
	title := CheckTitle
	if opts != nil && opts.Partial {
		title = CheckTitle + " (early signal)"
	}
	return CheckSummaryPayload{Title: title, Summary: markdown, Text: markdown}, nil
}
