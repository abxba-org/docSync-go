package orchestrator_test

// Ported from packages/core/test/check-conclusion.test.ts (V-P8-4..V-P8-6;
// P8-T08..T11 prep; ARCH §11.3, §12.1; D8/D10/D14). Fixtures reuse the
// check-render test pattern: schema examples → buildCanonicalReport. Pure, no
// git, no clock, no network.
//
// Conclusion mapping table (binding precedence, top wins):
//
//	1. configuration error (invalid manifest/config)         → failure (D8)
//	2. runtime inconclusive (analyzer outage, stage failure) → neutral (D14)
//	3. enforcement mode AND warn-family rows present         → failure (D10)
//	4. warn-family rows present, advisory (default)          → neutral (D10)
//	5. clean (no warn-family rows)                           → success
//
// Warn-family rows: outcome `suggest`/`review` or glyph `warn`/`review`.
// Enforcement-signaling rows: outcome `blocked` or glyph `locked`.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* --------------------------------------------------------------- fixtures */

// orchConclusionBase mirrors RUN_OUTCOME_EXAMPLE: documents[0] is the `auto`
// (warn) row, documents[1] the `ignore` (ok) row, documents[2] the `review`
// row.
func orchConclusionBase() schemas.RunOutcome { return schemas.RunOutcomeExample() }

// orchConclusionLockedDoc is a blocked row — the ownership floor (D2).
func orchConclusionLockedDoc() schemas.RunDocument {
	confidence := 0.88
	routingContinue := schemas.PipelineRoutingContinue
	exactSHA := schemas.BaselineRunExactSHA
	return schemas.RunDocument{
		DocPath:       "docs/security.md",
		PolicyOutcome: schemas.PipelineOutcomeBlocked,
		Confidence:    &confidence,
		Routing:       &routingContinue,
		Verification: &schemas.RunDocumentVerification{
			Verdict:          schemas.VerdictReject,
			AutoEligible:     false,
			AutoBlockReasons: []string{"unanchored-claims"},
			PatchRef:         orchPtr("state/docs/security.md#proposal-d_sec"),
		},
		PartialCoverage: false,
		BaselineRun:     &exactSHA,
	}
}

/** [warn(auto), ok(ignore)] — one drift row, one clean row. */
func orchConclusionWarnReport(t *testing.T) *schemas.CanonicalReport {
	t.Helper()
	outcome := orchConclusionBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[0], outcome.Documents[1]}
	outcome.Abstains = []schemas.RunAbstain{}
	return orchConclusionReport(t, outcome)
}

/** Only ignore rows — clean run. */
func orchConclusionCleanReport(t *testing.T) *schemas.CanonicalReport {
	t.Helper()
	outcome := orchConclusionBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[1]}
	outcome.Abstains = []schemas.RunAbstain{}
	return orchConclusionReport(t, outcome)
}

/** A blocked row (the ownership floor, D2) plus the warn row → locked. */
func orchConclusionLockedReport(t *testing.T) *schemas.CanonicalReport {
	t.Helper()
	outcome := orchConclusionBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[0], orchConclusionLockedDoc()}
	outcome.Abstains = []schemas.RunAbstain{}
	return orchConclusionReport(t, outcome)
}

func orchConclusionReport(t *testing.T, outcome schemas.RunOutcome) *schemas.CanonicalReport {
	t.Helper()
	report, err := orchestrator.BuildCanonicalReport(&outcome)
	require.NoError(t, err)
	return report
}

/* ---------------------------------------------- V-P8-4: conclusion mapping */

func TestV_P8_4_ConclusionMapping(t *testing.T) {
	clean := orchConclusionCleanReport(t)
	warn := orchConclusionWarnReport(t)
	locked := orchConclusionLockedReport(t)

	t.Run("V-P8-4a: clean report → success", func(t *testing.T) {
		cleanOutcome := orchConclusionCleanOutcome(t)
		require.Empty(t, cleanOutcome.Validate(), "the clean outcome stays schema-valid")
		resolved := orchestrator.ResolveCheckConclusion(clean, nil)
		assert.Equal(t, orchestrator.ConclusionSuccess, resolved.Conclusion)
		assert.False(t, resolved.Inconclusive)
	})

	t.Run("V-P8-4b: advisory warn-family rows → neutral by default (D10 default)", func(t *testing.T) {
		assert.Equal(t, orchestrator.ConclusionNeutral, orchestrator.ResolveCheckConclusion(warn, nil).Conclusion)
		// blocked/locked rows engage enforcement WITHOUT any flag (D10 floor).
		assert.Equal(t, orchestrator.ConclusionFailure, orchestrator.ResolveCheckConclusion(locked, nil).Conclusion)
	})

	t.Run("V-P8-4c: strict / enforcementOptIn turns advisory drift into failure (D10)", func(t *testing.T) {
		strict := &orchestrator.ResolveCheckConclusionOptions{Strict: true}
		assert.Equal(t, orchestrator.ConclusionFailure, orchestrator.ResolveCheckConclusion(warn, strict).Conclusion)
		optIn := &orchestrator.ResolveCheckConclusionOptions{EnforcementOptIn: true}
		assert.Equal(t, orchestrator.ConclusionFailure, orchestrator.ResolveCheckConclusion(warn, optIn).Conclusion)
		// Strict with NO drift stays success (enforcement needs warn-family rows).
		assert.Equal(t, orchestrator.ConclusionSuccess, orchestrator.ResolveCheckConclusion(clean, strict).Conclusion)
	})
}

func orchConclusionCleanOutcome(t *testing.T) schemas.RunOutcome {
	t.Helper()
	outcome := orchConclusionBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[1]}
	outcome.Abstains = []schemas.RunAbstain{}
	return outcome
}

/* ------------------------------------ V-P8-5: configuration error (D8) */

func TestV_P8_5_ConfigurationError(t *testing.T) {
	t.Run("config error fails closed at top precedence", func(t *testing.T) {
		clean := orchConclusionCleanReport(t)
		resolved := orchestrator.ResolveCheckConclusion(clean, &orchestrator.ResolveCheckConclusionOptions{
			ConfigError: true,
			Strict:      true,
		})
		assert.Equal(t, orchestrator.ConclusionFailure, resolved.Conclusion)
		assert.Contains(t, resolved.Reason, "D8")
	})

	t.Run("config-error summary lists offending file:line entries + remediation", func(t *testing.T) {
		payload := orchestrator.BuildConfigErrorSummary([]orchestrator.ConfigErrorDiagnostic{
			{
				File:    ".docsync/manifest.yml",
				Message: `unknown mapping key "docs_to"`,
				Line:    orchPtr(4),
				Column:  orchPtr(3),
			},
			{File: "docsync.config.json", Message: "policy_version must be positive"},
		})
		assert.Equal(t, orchestrator.ConfigErrorTitle, payload.Title)
		assert.Equal(t, "docsync: configuration error", payload.Title)
		assert.Contains(t, payload.Summary, "D8")
		assert.Contains(t, payload.Summary, "2 offending entries listed in the details")
		assert.Contains(t, payload.Text, "`.docsync/manifest.yml:4:3`: unknown mapping key \"docs_to\"")
		assert.Contains(t, payload.Text, "`docsync.config.json`: policy_version must be positive")
		assert.Contains(t, payload.Text, "pull request")
		assert.Contains(t, payload.Text, "ARCH §11.3")
	})

	t.Run("single diagnostic renders the singular entry word", func(t *testing.T) {
		payload := orchestrator.BuildConfigErrorSummary([]orchestrator.ConfigErrorDiagnostic{
			{File: ".docsync/manifest.yml", Message: "unknown mapping key \"docs_to\""},
		})
		assert.Contains(t, payload.Summary, "1 offending entry listed in the details")
	})
}

/* ---------------------------------- V-P8-6: inconclusive runtime (D14) */

func TestV_P8_6_InconclusiveRuntime(t *testing.T) {
	clean := orchConclusionCleanReport(t)
	warn := orchConclusionWarnReport(t)

	t.Run("runtime inconclusive → neutral + inconclusive, never \"no docs affected\"", func(t *testing.T) {
		resolved := orchestrator.ResolveCheckConclusion(clean, &orchestrator.ResolveCheckConclusionOptions{
			RuntimeInconclusive: true,
		})
		assert.Equal(t, orchestrator.ConclusionNeutral, resolved.Conclusion)
		assert.True(t, resolved.Inconclusive)
		assert.Contains(t, resolved.Reason, "D14")

		payload := orchestrator.BuildInconclusiveSummary("analyzer stage failed (timeout)")
		assert.Contains(t, payload.Title, "inconclusive")
		assert.NotContains(t, strings.ToLower(payload.Summary), "no docs affected")
		assert.NotContains(t, payload.Summary, "no documentation drift")
		// D14 precedence beats advisory/enforcement evaluation of a partial report.
		precedence := orchestrator.ResolveCheckConclusion(warn, &orchestrator.ResolveCheckConclusionOptions{
			RuntimeInconclusive: true,
			Strict:              true,
		})
		assert.Equal(t, orchestrator.ConclusionNeutral, precedence.Conclusion)
		assert.True(t, precedence.Inconclusive)
	})
}

/* --------------------------- quiet-mode abstention collapse in the payload */

func TestBuildCheckPayloadQuietModeAbstainedRowCollapse(t *testing.T) {
	outcome := orchConclusionBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[1]}
	outcome.Abstains = make([]schemas.RunAbstain, 0, 8)
	for index := 0; index < 8; index++ {
		outcome.Abstains = append(outcome.Abstains, schemas.RunAbstain{
			AbstainID: fmt.Sprintf("ab_v8_%d", index),
			DocPath:   nil,
			Reason:    string(schemas.AbstainReasonInsufficientConfidence),
			Detail:    fmt.Sprintf("abstained: evidence below the confidence floor (fixture %d)", index),
		})
	}
	report := orchConclusionReport(t, outcome)

	payload, err := orchestrator.BuildCheckPayload(report, nil)
	require.NoError(t, err)
	assert.Equal(t, orchestrator.CheckTitle, payload.Title)
	assert.Equal(t, "docsync: documentation check", payload.Title)
	// Quiet mode (default maxAbstainRows 5): exactly 5 rows + 1 suppression line.
	assert.Contains(t, payload.Summary, "3 more abstained rows suppressed")
	assert.Equal(t, 5, strings.Count(payload.Summary, "ab_v8_"))

	// Early-signal seam: the caller marks a partial report.
	partial, err := orchestrator.BuildCheckPayload(report, &orchestrator.BuildCheckPayloadOptions{Partial: true})
	require.NoError(t, err)
	assert.Equal(t, "docsync: documentation check (early signal)", partial.Title)

	// Fail-closed: a nil report is refused, not rendered.
	_, err = orchestrator.BuildCheckPayload(nil, nil)
	require.Error(t, err)
}

/* ------------------------------------------------- row predicates (D10) */

// The row predicates are the D10 vocabulary both the conclusion mapping and
// the exit-code table read — pinned here so the two surfaces cannot drift.
func TestWarnFamilyAndEnforcementRowPredicates(t *testing.T) {
	warnRows := []schemas.CanonicalReportRow{
		{DocPath: "docs/a.md", Glyph: schemas.ReportGlyphWarn, Outcome: schemas.PipelineOutcomeAuto},
		{DocPath: "docs/b.md", Glyph: schemas.ReportGlyphReview, Outcome: schemas.PipelineOutcomeReview},
		{DocPath: "docs/c.md", Glyph: schemas.ReportGlyphWarn, Outcome: schemas.PipelineOutcomeSuggest},
		{DocPath: "docs/d.md", Glyph: schemas.ReportGlyphReview, Outcome: schemas.PipelineOutcomeSuggest},
	}
	for _, row := range warnRows {
		assert.True(t, orchestrator.IsWarnFamilyRow(row), "%s must be warn-family", row.DocPath)
	}

	enforcementRows := []schemas.CanonicalReportRow{
		{DocPath: "docs/locked.md", Glyph: schemas.ReportGlyphLocked, Outcome: schemas.PipelineOutcomeBlocked},
		{DocPath: "docs/blocked.md", Glyph: schemas.ReportGlyphWarn, Outcome: schemas.PipelineOutcomeBlocked},
	}
	for _, row := range enforcementRows {
		assert.True(t, orchestrator.IsEnforcementRow(row), "%s must be enforcement-signaling", row.DocPath)
	}

	cleanRow := schemas.CanonicalReportRow{DocPath: "docs/ok.md", Glyph: schemas.ReportGlyphOk, Outcome: schemas.PipelineOutcomeIgnore}
	assert.False(t, orchestrator.IsWarnFamilyRow(cleanRow))
	assert.False(t, orchestrator.IsEnforcementRow(cleanRow))

	// HasWarnFamilyRows / IsEnforcementEngaged over a whole report.
	warn := orchConclusionWarnReport(t)
	locked := orchConclusionLockedReport(t)
	clean := orchConclusionCleanReport(t)
	assert.True(t, orchestrator.HasWarnFamilyRows(warn))
	assert.False(t, orchestrator.HasWarnFamilyRows(clean))
	assert.True(t, orchestrator.HasWarnFamilyRows(locked))
	assert.True(t, orchestrator.IsEnforcementEngaged(locked, orchestrator.ResolveCheckConclusionOptions{}))
	assert.False(t, orchestrator.IsEnforcementEngaged(warn, orchestrator.ResolveCheckConclusionOptions{}))
	assert.True(t, orchestrator.IsEnforcementEngaged(warn, orchestrator.ResolveCheckConclusionOptions{Strict: true}))
	assert.True(t, orchestrator.IsEnforcementEngaged(warn, orchestrator.ResolveCheckConclusionOptions{EnforcementOptIn: true}))
	assert.True(t, orchestrator.IsEnforcementEngaged(clean, orchestrator.ResolveCheckConclusionOptions{Strict: true}),
		"engagement itself is flag-driven; the warn-family requirement is applied separately")
	assert.False(t, orchestrator.IsEnforcementEngaged(clean, orchestrator.ResolveCheckConclusionOptions{}))
	// …and that separation is why strict on a CLEAN report still resolves
	// success (D10: enforcement needs warn-family rows).
	assert.Equal(t, orchestrator.ConclusionSuccess, orchestrator.ResolveCheckConclusion(clean,
		&orchestrator.ResolveCheckConclusionOptions{Strict: true}).Conclusion)
}
