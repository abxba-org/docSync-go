package orchestrator_test

// Check-surface coverage (check-render.go / check-conclusion.go) driven
// through the PUBLIC renderers and conclusion resolver built from a canonical
// report the module adapters consume: degradationLines, the sparse-abstention
// suppression line, the admission-vs-unrelated sections, resolveOptions'
// glyph/quiet/cabinet flags, and the ResolveCheckConclusion precedence table.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// covCheckRichOutcome is a rich outcome: an auto warn row, a locked row, a
// review warn row, a degradation, three abstains (one row-matched + confidence-
// carrying, one run-scoped, one ghost), unknown coverage and usage.
func covCheckRichOutcome() schemas.RunOutcome {
	outcome := schemas.RunOutcomeExample()
	outcome.Documents = []schemas.RunDocument{
		outcome.Documents[0],  // auto, confidence 0.96, decision-engine binding
		orchRenderLockedDoc(), // locked
		outcome.Documents[2],  // review + abstain detail (confidence 0.66)
	}
	outcome.Degradations = []schemas.Degradation{
		{Stage: string(orchestrator.StageWriter), Kind: schemas.DegradationProviderOutage, Detail: "writer port failed for docs/payments.md: timeout"},
	}
	outcome.UnknownCoverage = []string{"src/orphan.ts"}
	outcome.Usage = &schemas.RunUsage{PromptTokens: 900, CompletionTokens: 340, Cost: ptrTo(0.0182), PolicyVersion: outcome.Provenance.PolicyVersion}
	outcome.Abstains = []schemas.RunAbstain{
		{AbstainID: "ab_matched", DocPath: ptrTo("docs/cli.md"), Reason: string(schemas.AbstainReasonInsufficientConfidence), Detail: "below threshold"},
		{AbstainID: "ab_run", DocPath: nil, Reason: string(schemas.AbstainReasonModelUnavailable), Detail: "decision down"},
		{AbstainID: "ab_ghost", DocPath: ptrTo("docs/ghost.md"), Reason: string(schemas.AbstainReasonCapExceeded), Detail: "fan-out"},
	}
	return outcome
}

func covCheckReport(t *testing.T, outcome schemas.RunOutcome) *schemas.CanonicalReport {
	t.Helper()
	report, err := orchestrator.BuildCanonicalReport(&outcome)
	require.NoError(t, err)
	return report
}

// TestCOV_ORCH_501_RenderCheckRichSections covers the check renderer's
// degradation, sparse-abstention (with suppression line + row-matched
// confidence), unknown-coverage and usage sections, and resolveOptions' custom
// glyph set + quiet + cap.
func TestCOV_ORCH_501_RenderCheckRichSections(t *testing.T) {
	report := covCheckReport(t, covCheckRichOutcome())

	opts := &orchestrator.CheckRenderOptions{
		Glyphs:         orchestrator.GlyphsFallback,
		Quiet:          ptrTo(true),
		MaxAbstainRows: ptrTo(1),
		IncludeUsage:   true,
	}

	text := orchestrator.RenderCheckText(report, opts)
	assert.Contains(t, text, "Degradations:")
	assert.Contains(t, text, "- Degraded: writer/provider-outage — writer port failed for docs/payments.md: timeout")
	assert.Contains(t, text, "Unknown coverage:")
	assert.Contains(t, text, orchestrator.UnknownCoverageNote)
	assert.Contains(t, text, "Usage: 900 prompt / 340 completion tokens (policy v17)")
	assert.Contains(t, text, "cost 0.0182")
	// Sparse abstentions: 1 shown, 2 suppressed (the fallback glyph, not ✓/⚠).
	assert.Contains(t, text, "[!]")
	assert.Contains(t, text, "2 "+orchestrator.SuppressedSuffix)
	assert.NotContains(t, text, "⚠", "the fallback glyph set is selected")
	assert.Contains(t, text, "docs/cli.md — id:ab_matched", "a row-matched abstain names its document")
	assert.Contains(t, text, "66% (policy v17, model z-ai/glm-4.7)", "a row-matched abstain reuses the D3-pinned confidence")

	markdown := orchestrator.RenderCheckMarkdown(report, opts)
	assert.Contains(t, markdown, "### Degradations")
	assert.Contains(t, markdown, "### Unknown coverage")
	assert.Contains(t, markdown, "### Usage attribution")
	assert.Contains(t, markdown, "- [!]")
}

// TestCOV_ORCH_502_RenderCheckOmitsAbsentSections covers the nil/empty arms of
// every check section: a single ignore row with no degradations, abstains,
// usage or unknown coverage renders NO section for them, in both glyph sets.
func TestCOV_ORCH_502_RenderCheckOmitsAbsentSections(t *testing.T) {
	outcome := schemas.RunOutcomeExample()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[1]} // ignore only
	outcome.Degradations = []schemas.Degradation{}
	outcome.Abstains = []schemas.RunAbstain{}
	outcome.UnknownCoverage = nil
	outcome.Usage = nil
	report := covCheckReport(t, outcome)

	include := true
	opts := &orchestrator.CheckRenderOptions{IncludeUsage: include}
	text := orchestrator.RenderCheckText(report, opts)
	assert.NotContains(t, text, "Degradations:")
	assert.NotContains(t, text, "Abstentions:")
	assert.NotContains(t, text, "Unknown coverage")
	assert.NotContains(t, text, "Usage:", "a usage-nil report renders no usage line even when asked")
	assert.Contains(t, text, "✓", "the default unicode glyph set is selected")

	// Default options (nil) still resolve deterministically.
	def := orchestrator.RenderCheckText(report, nil)
	assert.Contains(t, def, "Documentation consistency")
}

// TestCOV_ORCH_503_ResolveCheckConclusionPrecedence covers the full conclusion
// precedence table (config error → runtime inconclusive → enforcement → advisory
// → clean) and its row-classification helpers.
func TestCOV_ORCH_503_ResolveCheckConclusionPrecedence(t *testing.T) {
	warn := covCheckReport(t, covCheckRichOutcome()) // has warn-family + a locked row
	cleanOutcome := schemas.RunOutcomeExample()
	cleanOutcome.Documents = []schemas.RunDocument{cleanOutcome.Documents[1]} // ok(row? ignore→ok)
	clean := covCheckReport(t, cleanOutcome)

	// 1. config error → failure.
	cfg := orchestrator.ResolveCheckConclusion(warn, &orchestrator.ResolveCheckConclusionOptions{ConfigError: true})
	assert.Equal(t, orchestrator.ConclusionFailure, cfg.Conclusion)
	// 2. runtime inconclusive → neutral + labeled inconclusive.
	inc := orchestrator.ResolveCheckConclusion(warn, &orchestrator.ResolveCheckConclusionOptions{RuntimeInconclusive: true})
	assert.Equal(t, orchestrator.ConclusionNeutral, inc.Conclusion)
	assert.True(t, inc.Inconclusive)
	// 3. enforcement (strict) → failure.
	strict := orchestrator.ResolveCheckConclusion(warn, &orchestrator.ResolveCheckConclusionOptions{Strict: true})
	assert.Equal(t, orchestrator.ConclusionFailure, strict.Conclusion)
	// 3b. enforcement (opt-in) → failure.
	optIn := orchestrator.ResolveCheckConclusion(warn, &orchestrator.ResolveCheckConclusionOptions{EnforcementOptIn: true})
	assert.Equal(t, orchestrator.ConclusionFailure, optIn.Conclusion)
	// 3c. enforcement via a locked/blocked row → failure even in advisory flags.
	locked := orchestrator.ResolveCheckConclusion(warn, &orchestrator.ResolveCheckConclusionOptions{})
	assert.Equal(t, orchestrator.ConclusionFailure, locked.Conclusion, "a locked row engages enforcement")
	// 4. advisory default → neutral (a warn report with no enforcement row).
	advisoryOutcome := schemas.RunOutcomeExample()
	advisoryOutcome.Documents = []schemas.RunDocument{advisoryOutcome.Documents[2]} // review warn, not locked
	advisory := orchestrator.ResolveCheckConclusion(covCheckReport(t, advisoryOutcome), &orchestrator.ResolveCheckConclusionOptions{})
	assert.Equal(t, orchestrator.ConclusionNeutral, advisory.Conclusion)
	assert.False(t, advisory.Inconclusive)
	// 5. clean → success.
	success := orchestrator.ResolveCheckConclusion(clean, &orchestrator.ResolveCheckConclusionOptions{})
	assert.Equal(t, orchestrator.ConclusionSuccess, success.Conclusion)
	assert.False(t, success.Inconclusive)
}

// TestCOV_ORCH_504_ConfigErrorSummary covers BuildConfigErrorSummary: the
// entry-position branches (line only, line:column, none) and the fixed
// remediation text of the fail-closed config-error payload (D8).
func TestCOV_ORCH_504_ConfigErrorSummary(t *testing.T) {
	line := 12
	payload := orchestrator.BuildConfigErrorSummary([]orchestrator.ConfigErrorDiagnostic{
		{File: ".docsync/manifest.yml", Message: "unknown mode", Line: &line},
		{File: ".docsync/manifest.yml", Message: "no documents"},
	})
	assert.Equal(t, orchestrator.ConfigErrorTitle, payload.Title)
	assert.Contains(t, payload.Summary, "Configuration error (D8)")
	assert.Contains(t, payload.Summary, "2 offending entries")
	assert.Contains(t, payload.Text, "docsync could not start")
	assert.Contains(t, payload.Text, "`.docsync/manifest.yml:12`: unknown mode")
	assert.Contains(t, payload.Text, "`.docsync/manifest.yml`: no documents")
	assert.Contains(t, payload.Text, "never modifies configuration")

	// Single diagnostic → the singular "entry" wording.
	single := orchestrator.BuildConfigErrorSummary([]orchestrator.ConfigErrorDiagnostic{{File: "m.yml", Message: "x"}})
	assert.Contains(t, single.Summary, "1 offending entry listed")
}

// TestCOV_ORCH_505_InconclusiveSummary covers BuildInconclusiveSummary: the
// self-assert rule that the summary is never read as "no docs affected".
func TestCOV_ORCH_505_InconclusiveSummary(t *testing.T) {
	summary := orchestrator.BuildInconclusiveSummary("analyzer outage")
	assert.Equal(t, "docsync: documentation check (inconclusive)", summary.Title)
	assert.Contains(t, summary.Summary, "Inconclusive (D14)")
	assert.Contains(t, summary.Summary, "analyzer outage")
	assert.Contains(t, summary.Summary, "NOT a pass")
}

// TestCOV_ORCH_506_BuildCheckPayload covers BuildCheckPayload: the nil-report
// fail-closed arm and the default vs partial (early-signal) title.
func TestCOV_ORCH_506_BuildCheckPayload(t *testing.T) {
	report := covCheckReport(t, covCheckRichOutcome())

	_, err := orchestrator.BuildCheckPayload(nil, nil)
	require.Error(t, err, "a nil report fails closed")

	final, err := orchestrator.BuildCheckPayload(report, nil)
	require.NoError(t, err)
	assert.Equal(t, orchestrator.CheckTitle, final.Title)
	assert.Contains(t, final.Summary, "### Degradations")

	partial, err := orchestrator.BuildCheckPayload(report, &orchestrator.BuildCheckPayloadOptions{Partial: true})
	require.NoError(t, err)
	assert.Equal(t, orchestrator.CheckTitle+" (early signal)", partial.Title)
}
