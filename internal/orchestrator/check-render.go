// Ported from packages/core/src/orchestrator/check-render.ts (P7-T02..T05;
// ARCH §12.1; PLAN §8.3): ONE renderer producing the `docsync check`
// transcript for the Phase 7 CLI AND the Phase 8 CI check-run surface — local
// and CI never disagree about the facts. Text and Markdown carry THE SAME
// facts; Markdown adds formatting only. The renderer consumes ONLY the
// canonical report (`docsync.canonical-report/v1`) — every displayed value is
// a field.
//
// D3 pinning (binding): every rendered percentage is adjacent to its
// producing policy version AND routed model — the row's `confidenceDisplay`
// pin, reused verbatim (`97% (policy v3, model acme/writer-x)`). Abstention
// rows that surface a confidence reuse the SAME pinned string (a bare
// percentage is never rendered).
//
// D7 (P7-T04): `unknownCoverage` paths render as their own `Unknown coverage:`
// section with the fixed wording `unknown coverage — changed but not mapped by
// the manifest`; when the set is empty/absent NOTHING is rendered for it
// (never silence with different wording, never an empty section).
//
// Abstention visibility (P7-T05; ARCH §5.2): abstention rows render with
// reason + detail/pointer, SPARSE by default — in quiet mode at most
// MaxAbstainRows rows are shown, followed by a single suppression line;
// non-quiet renders all. Document rows (locked ones included) are NEVER
// collapsed — only abstention rows are.
//
// Glyphs: unicode ✓/⚠/🔒 by default (review shares the ⚠ tier), ASCII fallback
// `[ok]`/`[!]`/`[locked]` (review → `[!]`) for constrained terminals — mapped
// at DISPLAY time, never inside the report.

package orchestrator

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// GlyphSet selects the §12.1 visual language (default unicode).
type GlyphSet string

// The glyph sets.
const (
	// GlyphsUnicode emits ✓/⚠/🔒.
	GlyphsUnicode GlyphSet = "unicode"
	// GlyphsFallback emits [ok]/[!]/[locked].
	GlyphsFallback GlyphSet = "fallback"
)

// CheckRenderOptions are the options of the shared check renderer (all
// defaulted, P7-T02).
type CheckRenderOptions struct {
	// Glyphs is `unicode` (default) or `fallback`.
	Glyphs GlyphSet
	// Quiet is the sparse-abstention-rows flag (default true).
	Quiet *bool
	// MaxAbstainRows is the number of abstention rows shown before the
	// suppression line (default 5; clamped to >= 0).
	MaxAbstainRows *int
	// IncludeUsage renders the usage attribution line (default false).
	IncludeUsage bool
}

var glyphUnicode = map[schemas.ReportGlyph]string{
	schemas.ReportGlyphOk:     "✓",
	schemas.ReportGlyphWarn:   "⚠",
	schemas.ReportGlyphLocked: "🔒",
	schemas.ReportGlyphReview: "⚠",
}

var glyphFallback = map[schemas.ReportGlyph]string{
	schemas.ReportGlyphOk:     "[ok]",
	schemas.ReportGlyphWarn:   "[!]",
	schemas.ReportGlyphLocked: "[locked]",
	schemas.ReportGlyphReview: "[!]",
}

// LockedNoteRender is the ownership-floor note locked rows carry (D2:
// surfaced, never touched) — the same fixed string the report renderer emits.
const LockedNoteRender = LockedNote

// formatFloat renders a usage cost the way the TS template literal did
// (shortest round-trip decimal form).
func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// UnknownCoverageNote is the D7 fixed wording (P7-T04) — never reworded,
// never an empty section.
const UnknownCoverageNote = "unknown coverage — changed but not mapped by the manifest"

// SuppressedSuffix is the quiet-mode abstention suppression line (P7-T05),
// fixed wording.
const SuppressedSuffix = "more abstained rows suppressed (run with --verbose-equivalent to show)"

// resolvedCheckOptions is the fully-defaulted option set (TS
// resolveOptions).
type resolvedCheckOptions struct {
	unicode        bool
	quiet          bool
	maxAbstainRows int
	includeUsage   bool
}

func resolveOptions(opts *CheckRenderOptions) resolvedCheckOptions {
	resolved := resolvedCheckOptions{unicode: true, quiet: true, maxAbstainRows: 5}
	if opts == nil {
		return resolved
	}
	resolved.unicode = opts.Glyphs != GlyphsFallback
	if opts.Quiet != nil {
		resolved.quiet = *opts.Quiet
	}
	if opts.MaxAbstainRows != nil {
		resolved.maxAbstainRows = *opts.MaxAbstainRows
		if resolved.maxAbstainRows < 0 {
			resolved.maxAbstainRows = 0
		}
	}
	resolved.includeUsage = opts.IncludeUsage
	return resolved
}

// rowBody is the shared row body — the IDENTICAL facts both surfaces expose.
func checkRowBody(row schemas.CanonicalReportRow) string {
	parts := []string{row.DocPath}
	if row.Detail != nil {
		parts = append(parts, *row.Detail)
	}
	if row.Glyph == schemas.ReportGlyphLocked {
		parts = append(parts, LockedNoteRender)
	}
	if row.ConfidenceDisplay != nil {
		parts = append(parts, *row.ConfidenceDisplay)
	}
	return strings.Join(parts, " — ")
}

// abstainBody is the abstention row body (P7-T05): reason + detail/pointer.
// When the abstention maps to a document row carrying a confidence, the
// D3-PINNED display string is reused — never a bare percentage.
func abstainBody(abstain schemas.RunAbstain, rows []schemas.CanonicalReportRow) string {
	docPath := "(run-scoped)"
	if abstain.DocPath != nil {
		docPath = *abstain.DocPath
	}
	parts := []string{
		docPath,
		// The abstain id IS the evidence pointer (the queue keys on it).
		"id:" + abstain.AbstainID,
		abstain.Reason,
		abstain.Detail,
	}
	if abstain.DocPath != nil {
		for i := range rows {
			if rows[i].DocPath == *abstain.DocPath {
				if rows[i].ConfidenceDisplay != nil {
					parts = append(parts, *rows[i].ConfidenceDisplay)
				}
				break
			}
		}
	}
	return strings.Join(parts, " — ")
}

func checkHeaderLines(report *schemas.CanonicalReport, markdown bool) []string {
	title := "Documentation consistency"
	meta := "run " + report.RunID + " · status " + string(report.Status) +
		" · policy v" + itoa(report.PolicyVersion)
	if markdown {
		title = "## Documentation consistency"
		meta = "run `" + report.RunID + "` · status `" + string(report.Status) + "`" +
			" · policy v" + itoa(report.PolicyVersion)
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

func checkRowLines(
	report *schemas.CanonicalReport,
	markdown bool,
	glyphOf func(glyph schemas.ReportGlyph) string,
) []string {
	lines := []string{}
	for i := range report.Rows {
		line := glyphOf(report.Rows[i].Glyph) + " " + checkRowBody(report.Rows[i])
		if markdown {
			line = "- " + line
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	return lines
}

func degradationLines(report *schemas.CanonicalReport, markdown bool) []string {
	if len(report.Degradations) == 0 {
		return nil
	}
	var lines []string
	if markdown {
		lines = []string{"### Degradations", ""}
	} else {
		lines = []string{"Degradations:", ""}
	}
	for i := range report.Degradations {
		degradation := report.Degradations[i]
		lines = append(lines, "- Degraded: "+degradation.Stage+"/"+string(degradation.Kind)+" — "+degradation.Detail)
	}
	lines = append(lines, "")
	return lines
}

// checkUnknownCoverageLines is the D7 section (P7-T04): own section, fixed wording;
// nothing at all when empty.
func checkUnknownCoverageLines(report *schemas.CanonicalReport, markdown bool) []string {
	if len(report.UnknownCoverage) == 0 {
		return nil
	}
	var lines []string
	if markdown {
		lines = []string{"### Unknown coverage", ""}
	} else {
		lines = []string{"Unknown coverage:", ""}
	}
	for _, path := range report.UnknownCoverage {
		lines = append(lines, "- "+path+" — "+UnknownCoverageNote)
	}
	lines = append(lines, "")
	return lines
}

// abstentionLines is the abstention visibility (P7-T05): sparse by default —
// at most maxAbstainRows rows, then ONE suppression line; non-quiet renders
// all. Document rows are never touched by this collapsing.
func abstentionLines(
	report *schemas.CanonicalReport,
	markdown bool,
	quiet bool,
	maxAbstainRows int,
) []string {
	if len(report.Abstains) == 0 {
		return nil
	}
	var lines []string
	if markdown {
		lines = []string{"### Abstentions", ""}
	} else {
		lines = []string{"Abstentions:", ""}
	}
	visible := report.Abstains
	if quiet && len(visible) > maxAbstainRows {
		visible = visible[:maxAbstainRows]
	}
	for i := range visible {
		lines = append(lines, "- "+abstainBody(visible[i], report.Rows))
	}
	suppressed := len(report.Abstains) - len(visible)
	if suppressed > 0 {
		lines = append(lines, "- "+itoa(suppressed)+" "+SuppressedSuffix)
	}
	lines = append(lines, "")
	return lines
}

func checkUsageLines(report *schemas.CanonicalReport, markdown bool) []string {
	if report.Usage == nil {
		return nil
	}
	usage := report.Usage
	line := "Usage: " + itoa(usage.PromptTokens) + " prompt / " + itoa(usage.CompletionTokens) +
		" completion tokens (policy v" + itoa(usage.PolicyVersion) + ")"
	if usage.Cost != nil {
		line += " · cost " + formatFloat(*usage.Cost)
	}
	if markdown {
		return []string{"### Usage attribution", "", line, ""}
	}
	return []string{line, ""}
}

func renderCheckLines(
	report *schemas.CanonicalReport,
	markdown bool,
	resolved resolvedCheckOptions,
) string {
	glyphOf := func(glyph schemas.ReportGlyph) string {
		if resolved.unicode {
			return glyphUnicode[glyph]
		}
		return glyphFallback[glyph]
	}
	lines := []string{}
	lines = append(lines, checkHeaderLines(report, markdown)...)
	lines = append(lines, checkRowLines(report, markdown, glyphOf)...)
	lines = append(lines, degradationLines(report, markdown)...)
	lines = append(lines, checkUnknownCoverageLines(report, markdown)...)
	lines = append(lines, abstentionLines(report, markdown, resolved.quiet, resolved.maxAbstainRows)...)
	if resolved.includeUsage {
		lines = append(lines, checkUsageLines(report, markdown)...)
	}
	joined := strings.Join(lines, "\n")
	return strings.TrimRightFunc(joined, unicode.IsSpace) + "\n"
}

// RenderCheckText renders the `docsync check` transcript (plain text) from
// ONLY the canonical report — the §12.1 transcript the Phase 7 CLI prints and
// the Phase 8 CI check-run mirrors (same facts, see RenderCheckMarkdown).
func RenderCheckText(report *schemas.CanonicalReport, opts *CheckRenderOptions) string {
	return renderCheckLines(report, false, resolveOptions(opts))
}

// RenderCheckMarkdown renders the SAME facts as GitHub-flavored markdown
// (Phase 8 check runs).
func RenderCheckMarkdown(report *schemas.CanonicalReport, opts *CheckRenderOptions) string {
	return renderCheckLines(report, true, resolveOptions(opts))
}
