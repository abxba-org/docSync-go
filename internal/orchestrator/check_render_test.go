package orchestrator_test

// Ported from packages/core/test/check-render.test.ts (P7-T02..T05; ARCH
// §12.1 transcript contract). Report-side tests derive from the schema
// examples — pure, no git, no clock, no network.
//
// The shared check renderer consumes ONLY the canonical report
// (`docsync.canonical-report/v1`): text and Markdown carry THE SAME facts,
// Markdown adds formatting only. Every displayed value is a FIELD
// (`generatedAt` is `early_signal.updated_at ?? finished_at ?? started_at`),
// and every rendered percentage is adjacent to its producing policy version
// AND the routed decision-engine model (D3 — a bare percentage is never
// displayed). Abstention rows are SPARSE by default (at most 5, then one
// suppression line); document rows — locked ones included — are never
// collapsed. Unknown coverage (D7) renders as its own section with fixed
// wording, or not at all when the set is empty.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* --------------------------------------------------------------- fixtures */

// orchRenderBase mirrors RUN_OUTCOME_EXAMPLE: documents[0] is the `auto`
// (warn) row, documents[1] the `ignore` (ok) row.
func orchRenderBase() schemas.RunOutcome { return schemas.RunOutcomeExample() }

// orchRenderLockedDoc is a blocked row — the ownership floor (D2): surfaced,
// never touched.
func orchRenderLockedDoc() schemas.RunDocument { return orchConclusionLockedDoc() }

/** [warn(auto), ok(ignore), locked(blocked)] — all accounted for (V-P5-16). */
func orchRenderMixedOutcome() schemas.RunOutcome {
	outcome := orchRenderBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[0], outcome.Documents[1], orchRenderLockedDoc()}
	outcome.Abstains = []schemas.RunAbstain{}
	return outcome
}

func orchRenderMixedReport(t *testing.T) *schemas.CanonicalReport {
	t.Helper()
	outcome := orchRenderMixedOutcome()
	require.Empty(t, outcome.Validate(), "the mixed outcome stays schema-valid")
	return orchRenderReport(t, outcome)
}

// orchRenderRunScopedAbstains builds N run-scoped abstention records
// (P7-T05 sparse-visibility fixture).
func orchRenderRunScopedAbstains(count int) []schemas.RunAbstain {
	abstains := make([]schemas.RunAbstain, 0, count)
	for index := 0; index < count; index++ {
		abstains = append(abstains, schemas.RunAbstain{
			AbstainID: fmt.Sprintf("ab_fixture_%d", index),
			DocPath:   nil,
			Reason:    string(schemas.AbstainReasonInsufficientConfidence),
			Detail:    fmt.Sprintf("abstained: evidence below the confidence floor (fixture %d)", index),
		})
	}
	return abstains
}

// orchRenderOutcomeWithAbstains is the ignore row plus 8 run-scoped abstains.
func orchRenderOutcomeWithAbstains() schemas.RunOutcome {
	outcome := orchRenderBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[1]}
	outcome.Abstains = orchRenderRunScopedAbstains(8)
	return outcome
}

// orchRenderOutcomeLockedWithAbstains is the locked row plus 8 run-scoped
// abstains (locked rows are never collapsed).
func orchRenderOutcomeLockedWithAbstains() schemas.RunOutcome {
	outcome := orchRenderBase()
	outcome.Documents = []schemas.RunDocument{orchRenderLockedDoc()}
	outcome.Abstains = orchRenderRunScopedAbstains(8)
	return outcome
}

func orchRenderReport(t *testing.T, outcome schemas.RunOutcome) *schemas.CanonicalReport {
	t.Helper()
	report, err := orchestrator.BuildCanonicalReport(&outcome)
	require.NoError(t, err)
	return report
}

/* ------------------------------------------------------- glyph rows (V-P7-1) */

func TestV_P7_1_CheckTextOutputRendersGlyphRowsIncludingLockedRowAndFallback(t *testing.T) {
	report := orchRenderMixedReport(t)

	text := orchestrator.RenderCheckText(report, nil)
	assert.Contains(t, text, "✓ docs/api.md")
	assert.Contains(t, text, "⚠ docs/payments.md")
	assert.Contains(t, text, "🔒 docs/security.md")
	// The locked row carries the ownership-floor note in BOTH surfaces.
	assert.Contains(t, text, "locked (surfaced, never touched)")
	// The header fields (never generated values).
	assert.Contains(t, text, "Documentation consistency")
	assert.Contains(t, text, "run run_01J9ZK6X8A · status completed · policy v17")
	assert.Contains(t, text, "as of 2026-10-04T22:30:47Z")

	fallback := orchestrator.RenderCheckText(report, &orchestrator.CheckRenderOptions{Glyphs: orchestrator.GlyphsFallback})
	assert.Contains(t, fallback, "[ok] docs/api.md")
	assert.Contains(t, fallback, "[!] docs/payments.md")
	assert.Contains(t, fallback, "[locked] docs/security.md")
	assert.Contains(t, fallback, "locked (surfaced, never touched)")

	markdown := orchestrator.RenderCheckMarkdown(report, &orchestrator.CheckRenderOptions{Glyphs: orchestrator.GlyphsFallback})
	assert.Contains(t, markdown, "- [locked] docs/security.md")
	assert.Contains(t, markdown, "## Documentation consistency")
	assert.Contains(t, markdown, "as of `2026-10-04T22:30:47Z`")
}

/* ------------------------------------------------ D3 pinning (V-P7-2, P7-T03) */

// orchPinnedPercentage is the D3 pin: `<pct>% (policy v<n>, model <model>)`.
var (
	orchPercentageRe = regexp.MustCompile(`\d+%`)
	orchPinnedRe     = regexp.MustCompile(`\d+% \(policy v\d+, model [^)]+\)`)
	orchPinShapeRe   = regexp.MustCompile(`^\d+% \(policy v\d+, model \S+\)$`)
)

// orchAssertEveryPercentagePinned fails on a percentage without the adjacent
// pin (a bare percentage is never rendered).
func orchAssertEveryPercentagePinned(t *testing.T, render string) {
	t.Helper()
	percentages := orchPercentageRe.FindAllString(render, -1)
	pinned := orchPinnedRe.FindAllString(render, -1)
	assert.NotEmpty(t, pinned, "a confidence-bearing render must pin at least one percentage")
	assert.Equal(t, len(percentages), len(pinned), "every rendered percentage must carry the D3 pin")
	for _, pin := range pinned {
		assert.Regexp(t, orchPinShapeRe, pin)
	}
}

func TestV_P7_2_D3EveryRenderedConfidenceCarriesPolicyVersionAndModelBinding(t *testing.T) {
	outcome := orchRenderMixedOutcome()
	outcome.Abstains = []schemas.RunAbstain{
		{
			AbstainID: "ab_pin",
			DocPath:   orchPtr("docs/payments.md"),
			Reason:    string(schemas.AbstainReasonInsufficientConfidence),
			Detail:    "abstained with a confidence-bearing document attached",
		},
	}
	report := orchRenderReport(t, outcome)

	orchAssertEveryPercentagePinned(t, orchestrator.RenderCheckText(report, nil))
	orchAssertEveryPercentagePinned(t, orchestrator.RenderCheckMarkdown(report, nil))
	orchAssertEveryPercentagePinned(t, orchestrator.RenderCheckText(report, &orchestrator.CheckRenderOptions{Glyphs: orchestrator.GlyphsFallback}))

	// Abstention rows showing a confidence reuse the SAME pinned string.
	text := orchestrator.RenderCheckText(report, nil)
	assert.Contains(t, text, "Abstentions:")
	assert.Contains(t, text, fmt.Sprintf("96%% (policy v%d, model z-ai/glm-4.7)", report.PolicyVersion))
}

// D3 display derivation pinned directly: routed decision-engine model +
// producing policy version, and NULL when either half is missing (a bare
// percentage is never displayed).
func TestConfidenceDisplayOfPinsPolicyVersionAndRoutedModel(t *testing.T) {
	confidence := 0.96
	doc := schemas.RunDocument{DocPath: "docs/payments.md", PolicyOutcome: schemas.PipelineOutcomeAuto, Confidence: &confidence}
	model := "z-ai/glm-4.7"

	display := orchestrator.ConfidenceDisplayOf(doc, 17, &model)
	require.NotNil(t, display)
	assert.Equal(t, "96% (policy v17, model z-ai/glm-4.7)", *display)

	assert.Nil(t, orchestrator.ConfidenceDisplayOf(doc, 17, nil), "no routed binding → no display")
	noConfidence := doc
	noConfidence.Confidence = nil
	assert.Nil(t, orchestrator.ConfidenceDisplayOf(noConfidence, 17, &model), "no confidence → no display")
}

// RoutedDecisionModel reads the actually-routed decision-engine binding out
// of provenance (D3/DX-P2-2), never a configured default.
func TestRoutedDecisionModelReadsTheDecisionEngineBinding(t *testing.T) {
	bindings := []schemas.RunModelBinding{
		{Role: "writer", Model: "anthropic/claude-sonnet-4.5"},
		{Role: "decision-engine", Model: "z-ai/glm-4.7", Provider: orchPtr("z-ai")},
	}
	model := orchestrator.RoutedDecisionModel(bindings)
	require.NotNil(t, model)
	assert.Equal(t, "z-ai/glm-4.7", *model)
	assert.Nil(t, orchestrator.RoutedDecisionModel(nil))
	assert.Nil(t, orchestrator.RoutedDecisionModel([]schemas.RunModelBinding{{Role: "judge", Model: "z-ai/glm-4.7"}}))
}

// The glyph contract is total over PipelineOutcome (review keeps its SEMANTIC
// glyph while the renderers share the ⚠ human-triage tier).
func TestGlyphForOutcomeIsThePinnedContract(t *testing.T) {
	expected := map[schemas.PipelineOutcome]schemas.ReportGlyph{
		schemas.PipelineOutcomeIgnore:  schemas.ReportGlyphOk,
		schemas.PipelineOutcomeSuggest: schemas.ReportGlyphWarn,
		schemas.PipelineOutcomeAuto:    schemas.ReportGlyphWarn,
		schemas.PipelineOutcomeBlocked: schemas.ReportGlyphLocked,
		schemas.PipelineOutcomeReview:  schemas.ReportGlyphReview,
	}
	for outcome, glyph := range expected {
		assert.Equal(t, glyph, orchestrator.GlyphForOutcome(outcome), "outcome %q must map to glyph %q", outcome, glyph)
	}
}

/* --------------------------------------------------- D7 unknown coverage (V-P7-3) */

func TestV_P7_3_D7UnknownCoverageSurfacesAsItsOwnSectionNeverSilence(t *testing.T) {
	outcome := orchRenderBase()
	outcome.UnknownCoverage = []string{"scripts/deploy.sh", "README.md", "README.md"}
	require.Empty(t, outcome.Validate(), "the unknown-coverage outcome stays schema-valid")
	report := orchRenderReport(t, outcome)

	// Sorted + deduped here as a second deterministic guard.
	assert.Equal(t, []string{"README.md", "scripts/deploy.sh"}, report.UnknownCoverage)

	for _, render := range []string{
		orchestrator.RenderCheckText(report, nil),
		orchestrator.RenderCheckMarkdown(report, nil),
	} {
		assert.Contains(t, render, "Unknown coverage")
		assert.Contains(t, render, "unknown coverage — changed but not mapped by the manifest")
		assert.Contains(t, render, "README.md")
		assert.Contains(t, render, "scripts/deploy.sh")
	}

	// Absent: NOTHING is rendered for it — never silence with different
	// wording, never an empty section.
	empty := orchestrator.RenderCheckText(orchRenderMixedReport(t), nil)
	assert.NotContains(t, empty, "Unknown coverage")
	assert.NotContains(t, empty, "unknown coverage —")
	emptyMarkdown := orchestrator.RenderCheckMarkdown(orchRenderMixedReport(t), nil)
	assert.NotContains(t, emptyMarkdown, "Unknown coverage")

	// A nil set normalizes to the empty list (never a null section).
	assert.Equal(t, []string{}, orchestrator.NormalizeUnknownCoverage(nil))
	assert.Equal(t, []string{"a.md", "b.md"}, orchestrator.NormalizeUnknownCoverage([]string{"b.md", "a.md", "b.md"}),
		"deduped and sorted")
}

/* -------------------------------------------------- abstention visibility (P7-T05) */

func TestAbstentionRowsAreSparseUnderQuietModeAndFullyVisibleWithoutIt(t *testing.T) {
	report := orchRenderReport(t, orchRenderOutcomeWithAbstains())

	t.Run("quiet mode collapses to maxAbstainRows + one suppression line", func(t *testing.T) {
		text := orchestrator.RenderCheckText(report, nil)
		rows := orchLinesContaining(text, "ab_fixture_")
		assert.Len(t, rows, 5)
		assert.Contains(t, text, "3 more abstained rows suppressed (run with --verbose-equivalent to show)")
	})

	t.Run("non-quiet renders all abstain rows and no suppression line", func(t *testing.T) {
		text := orchestrator.RenderCheckText(report, &orchestrator.CheckRenderOptions{Quiet: orchPtr(false)})
		rows := orchLinesContaining(text, "ab_fixture_")
		assert.Len(t, rows, 8)
		assert.NotContains(t, text, "more abstained rows suppressed")
	})

	t.Run("a clamped maxAbstainRows of zero shows only the suppression line", func(t *testing.T) {
		text := orchestrator.RenderCheckText(report, &orchestrator.CheckRenderOptions{MaxAbstainRows: orchPtr(0)})
		assert.Empty(t, orchLinesContaining(text, "ab_fixture_"))
		assert.Contains(t, text, "8 more abstained rows suppressed")
	})

	t.Run("locked rows are never collapsed, even with many abstain rows", func(t *testing.T) {
		lockedText := orchestrator.RenderCheckText(orchRenderReport(t, orchRenderOutcomeLockedWithAbstains()), nil)
		assert.Contains(t, lockedText, "🔒 docs/security.md")
		assert.Contains(t, lockedText, "locked (surfaced, never touched)")
		assert.Contains(t, lockedText, "more abstained rows suppressed")
	})
}

// orchLinesContaining returns the render lines that carry the marker.
func orchLinesContaining(render, marker string) []string {
	lines := []string{}
	for _, line := range strings.Split(render, "\n") {
		if strings.Contains(line, marker) {
			lines = append(lines, line)
		}
	}
	return lines
}

/* ------------------------------------------------------------- fact parity */

func TestTextAndMarkdownRenderTheSameFacts(t *testing.T) {
	outcome := orchRenderOutcomeWithAbstains()
	outcome.UnknownCoverage = []string{"README.md"}
	report := orchRenderReport(t, outcome)

	text := orchestrator.RenderCheckText(report, &orchestrator.CheckRenderOptions{Quiet: orchPtr(false)})
	markdown := orchestrator.RenderCheckMarkdown(report, &orchestrator.CheckRenderOptions{Quiet: orchPtr(false)})

	pathsOf := func(render string) []string {
		matches := regexp.MustCompile(`docs\/[a-z-]+\.md`).FindAllString(render, -1)
		sort.Strings(matches)
		return matches
	}
	assert.Equal(t, pathsOf(text), pathsOf(markdown))

	percentagesOf := func(render string) []string {
		matches := orchPinnedRe.FindAllString(render, -1)
		sort.Strings(matches)
		return matches
	}
	assert.Equal(t, percentagesOf(text), percentagesOf(markdown))

	assert.Contains(t, text, "status completed")
	assert.Contains(t, markdown, "status `completed`")
	assert.Contains(t, text, "README.md")
	assert.Contains(t, markdown, "README.md")
}

/* ------------------------------------------------------------- usage surface */

// P5-T17: when the outcome carries `usage`, both surfaces render the token
// counts with the producing `policy_version` (attribution stays attached to
// the policy that incurred it); `includeUsage` gates the line.
func TestUsageLineRendersOnlyWhenRequested(t *testing.T) {
	totalTokens := 1240
	cost := 0.0182
	outcome := orchRenderMixedOutcome()
	outcome.Usage = &schemas.RunUsage{
		PromptTokens:     900,
		CompletionTokens: 340,
		TotalTokens:      &totalTokens,
		Cost:             &cost,
		PolicyVersion:    outcome.Provenance.PolicyVersion,
	}
	report := orchRenderReport(t, outcome)

	defaultRender := orchestrator.RenderCheckText(report, nil)
	assert.NotContains(t, defaultRender, "Usage:")

	withUsage := orchestrator.RenderCheckText(report, &orchestrator.CheckRenderOptions{IncludeUsage: true})
	assert.Contains(t, withUsage, "Usage: 900 prompt / 340 completion tokens (policy v17)")
	assert.Contains(t, withUsage, "cost 0.0182")

	markdownUsage := orchestrator.RenderCheckMarkdown(report, &orchestrator.CheckRenderOptions{IncludeUsage: true})
	assert.Contains(t, markdownUsage, "### Usage attribution")
	assert.Contains(t, markdownUsage, "Usage: 900 prompt / 340 completion tokens (policy v17)")

	// The report carries the usage block verbatim.
	require.NotNil(t, report.Usage)
	assert.Equal(t, 900, report.Usage.PromptTokens)
	assert.Equal(t, 340, report.Usage.CompletionTokens)
}

/* --------------------------------------------------------------- --json shape */

func TestBuildCanonicalReportRoundTripsThroughTheRegisteredSchema(t *testing.T) {
	report := orchRenderMixedReport(t)

	data := orchMarshal(t, report)
	assert.Equal(t, "docsync.canonical-report/v1", schemas.CanonicalReportSchemaID)
	assert.Contains(t, string(data), `"runId":"run_01J9ZK6X8A"`)

	parsed, err := schemas.ParseCanonicalReport(data)
	require.NoError(t, err)
	assert.Equal(t, *report, *parsed, "the built report must round-trip the registered schema unchanged")

	// The renderers stay pure consumers of that same report object.
	assert.Equal(t, *report, *parsed)
}
