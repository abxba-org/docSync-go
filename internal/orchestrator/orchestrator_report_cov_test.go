package orchestrator_test

// Coverage extension for internal/orchestrator (report.go renderers + the
// report self-validation guard). Pure: no git, no clock, no network — every
// payload is a schema-example-derived RunOutcome handed to the public
// BuildCanonicalReport, and every display value is asserted against the
// §12.1 transcript contract the module doc pins (glyphs ✓/⚠/🔒, the D3-pinned
// confidence string, the D7 unknown-coverage section, the P5-T17 usage line).

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// covReportOutcome builds a rich RunOutcome exercising every report member the
// renderers read: an `auto` row (confidence + routed decision-engine binding →
// D3 display), an `ignore` row, a `locked` row (ownership floor), a `review`
// row carrying an abstain detail, a PatchRef-carrying finding (copyFinding's
// optional member), unknown coverage, and the usage attribution block.
func covReportOutcome() schemas.RunOutcome {
	outcome := schemas.RunOutcomeExample()
	outcome.Documents = []schemas.RunDocument{
		outcome.Documents[0],  // auto, confidence 0.96, decision, routing, verification, baseline
		outcome.Documents[1],  // ignore
		orchRenderLockedDoc(), // blocked (locked)
		outcome.Documents[2],  // review + abstain detail
	}
	outcome.Findings = []schemas.RunFinding{
		// A doc NOT among the rendered rows so rowFor leaves every row's
		// detail on the abstain/null path while copyFinding still copies the
		// optional DocPath + PatchRef members.
		{DocPath: ptrTo("docs/orphan.md"), Kind: "potentially-stale", Detail: "orphan finding", PatchRef: ptrTo("state/docs/orphan.md#proposal-x")},
	}
	outcome.UnknownCoverage = []string{"zeta.md", "alpha.md", "zeta.md"}
	outcome.Usage = &schemas.RunUsage{
		PromptTokens:     900,
		CompletionTokens: 340,
		TotalTokens:      ptrTo(1240),
		Cost:             ptrTo(0.0182),
		PolicyVersion:    outcome.Provenance.PolicyVersion,
	}
	return outcome
}

func covReport(t *testing.T, outcome schemas.RunOutcome) *schemas.CanonicalReport {
	t.Helper()
	report, err := orchestrator.BuildCanonicalReport(&outcome)
	require.NoError(t, err)
	return report
}

// TestCOV_ORCH_101_RenderReportText pins the plain-text transcript from ONLY
// the canonical report: the header, every glyph row, the D3-pinned confidence,
// the locked ownership-floor note, the D7 unknown-coverage section and the
// P5-T17 usage line — and the trimEndNewline trailing-newline contract.
func TestCOV_ORCH_101_RenderReportText(t *testing.T) {
	report := covReport(t, covReportOutcome())

	text := orchestrator.RenderReportText(report)

	// Header fields (never generated values).
	assert.Contains(t, text, "Documentation consistency")
	assert.Contains(t, text, "run run_01J9ZK6X8A · status completed · policy v17")
	assert.Contains(t, text, "as of 2026-10-04T22:30:47Z")
	// Row bodies: the auto row carries the D3 pin, the locked row the floor
	// note, the review row its abstain detail.
	assert.Contains(t, text, "⚠ docs/payments.md — 96% (policy v17, model z-ai/glm-4.7)")
	assert.Contains(t, text, "✓ docs/api.md")
	assert.Contains(t, text, "🔒 docs/security.md — locked (surfaced, never touched) — 88% (policy v17, model z-ai/glm-4.7)")
	assert.Contains(t, text, "⚠ docs/cli.md — documentation_impact probability 0.66 sits below the review threshold (0.72)")
	// D7 unknown coverage: deduped + sorted here, its own section.
	assert.Contains(t, text, "Unknown coverage (D7")
	assert.Contains(t, text, "- alpha.md")
	assert.Contains(t, text, "- zeta.md")
	// P5-T17 usage attribution with cost.
	assert.Contains(t, text, "Usage: 900 prompt / 340 completion tokens (policy v17)")
	assert.Contains(t, text, "cost 0.0182")

	// trimEndNewline: exactly one trailing newline, no trailing blank line.
	assert.True(t, strings.HasSuffix(text, "\n"), "render must end with a single newline")
	assert.False(t, strings.HasSuffix(text, "\n\n"), "render must not end with a blank line")
	assert.False(t, strings.HasSuffix(strings.TrimRight(text, "\n"), " "), "no trailing space")
}

// TestCOV_ORCH_102_RenderReportMarkdown pins the GitHub-flavored markdown of
// the same facts: markdown header/row/section formatting only.
func TestCOV_ORCH_102_RenderReportMarkdown(t *testing.T) {
	report := covReport(t, covReportOutcome())

	markdown := orchestrator.RenderReportMarkdown(report)

	assert.Contains(t, markdown, "## Documentation consistency")
	assert.Contains(t, markdown, "run `run_01J9ZK6X8A` · status `completed` · policy v17")
	assert.Contains(t, markdown, "as of `2026-10-04T22:30:47Z`")
	// Rows are markdown list items with the shared row body.
	assert.Contains(t, markdown, "- ⚠ docs/payments.md — 96% (policy v17, model z-ai/glm-4.7)")
	assert.Contains(t, markdown, "- 🔒 docs/security.md — locked (surfaced, never touched) — 88% (policy v17, model z-ai/glm-4.7)")
	assert.Contains(t, markdown, "### Unknown coverage (D7)")
	assert.Contains(t, markdown, "### Usage attribution")
	assert.Contains(t, markdown, "Usage: 900 prompt / 340 completion tokens (policy v17)")
	assert.True(t, strings.HasSuffix(markdown, "\n"))
	assert.False(t, strings.HasSuffix(markdown, "\n\n"))
}

// TestCOV_ORCH_103_RenderReportOmitsAbsentMembers covers the nil/empty guards
// of the header, unknown-coverage and usage lines: an outcome with no
// generatedAt, no usage and no unknown coverage renders NO section for them.
func TestCOV_ORCH_103_RenderReportOmitsAbsentMembers(t *testing.T) {
	outcome := schemas.RunOutcomeExample()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[1]}
	// generatedAt absent: nil the whole timestamp chain.
	outcome.EarlySignal = schemas.EarlySignal{}
	outcome.StartedAt = nil
	outcome.FinishedAt = nil
	outcome.Usage = nil
	outcome.UnknownCoverage = nil

	report := covReport(t, outcome)

	require.Nil(t, report.GeneratedAt, "no timestamp chain → generatedAt is nil")
	text := orchestrator.RenderReportText(report)
	assert.NotContains(t, text, "as of", "no generatedAt → no 'as of' header line")
	assert.NotContains(t, text, "Usage:", "no usage block → no usage line")
	assert.NotContains(t, text, "Unknown coverage", "no unknown-coverage set → no section")

	markdown := orchestrator.RenderReportMarkdown(report)
	assert.NotContains(t, markdown, "as of")
	assert.NotContains(t, markdown, "### Usage attribution")
	assert.NotContains(t, markdown, "### Unknown coverage")
}

// TestCOV_ORCH_104_UsageLineWithoutCost covers the usageLines no-cost arm.
func TestCOV_ORCH_104_UsageLineWithoutCost(t *testing.T) {
	outcome := schemas.RunOutcomeExample()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[1]}
	outcome.Usage = &schemas.RunUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		PolicyVersion:    outcome.Provenance.PolicyVersion,
	}
	report := covReport(t, outcome)

	text := orchestrator.RenderReportText(report)
	assert.Contains(t, text, "Usage: 10 prompt / 5 completion tokens (policy v17)")
	assert.NotContains(t, text, "cost ", "no cost member → no cost fragment")
}

// TestCOV_ORCH_105_BuildCanonicalReportDriftFailsClosed covers reportDriftError:
// an outcome whose Status the CANONICAL_REPORT_SCHEMA rejects makes the built
// report fail self-validation → BuildCanonicalReport refuses (fail closed)
// with the loud coded error, never an invalid report.
func TestCOV_ORCH_105_BuildCanonicalReportDriftFailsClosed(t *testing.T) {
	outcome := schemas.RunOutcomeExample()
	// A status string no schema in the run-outcome/report family accepts: the
	// report copies Status verbatim, so report.Validate() rejects it.
	outcome.Status = schemas.RunStatus("bogus-not-a-run-status")

	report, err := orchestrator.BuildCanonicalReport(&outcome)
	require.Error(t, err)
	require.Nil(t, report, "a self-validation failure never returns a report")
	assert.Contains(t, err.Error(), "canonical report failed self-validation")
	// It is a coded schema error, not a pipeline/config error the caller might
	// misinterpret into an exit lane.
	assert.False(t, orchestrator.IsConfigError(err))
	assert.False(t, orchestrator.IsStageContractError(err))
}
