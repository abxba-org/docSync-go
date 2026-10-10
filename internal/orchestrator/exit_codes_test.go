package orchestrator_test

// Ported from packages/core/test/exit-codes.test.ts (P7-T06/T07; ARCH §12.1
// exit table; D8/D14/D10/D2). Pure predicates over canonical reports and the
// typed error classes core owns — no process exit, no I/O.
//
// Exit-code contract (ARCH §12.1):
//
//	0 — no drift detected.
//	1 — drift detected (candidates found).
//	2 — configuration/manifest error (fail-closed, D8) — wins over everything.
//	3 — runtime failure (API/model/verifier error — also fail-closed, D14).
//
// Strictness (D10): `suggest`-only (advisory) runs exit 0 without `--strict`
// and exit 1 with it; `review`-family rows are promoted the same way.
// Blocking outcomes are NOT advisory: `auto` and `blocked`/locked rows (the
// ownership floor, D2) ALWAYS count as drift regardless of strict. Unknown
// coverage alone (D7) is surfaced, never a drift exit.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/layout"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

/* --------------------------------------------------------------- fixtures */

// orchExitBase mirrors RUN_OUTCOME_EXAMPLE: documents[0] is the `auto` drift
// row, documents[1] the `ignore` clean row and documents[2] the `review`
// row.
func orchExitBase() schemas.RunOutcome { return schemas.RunOutcomeExample() }

/** Clean run — only the ignore row, no abstains. */
func orchExitCleanOutcome() schemas.RunOutcome {
	outcome := orchExitBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[1]}
	outcome.Abstains = []schemas.RunAbstain{}
	return outcome
}

/** Drift run — the `auto` row (blocking drift). */
func orchExitDriftOutcome() schemas.RunOutcome {
	outcome := orchExitBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[0]}
	outcome.Abstains = []schemas.RunAbstain{}
	return outcome
}

/** Advisory-only run: suggest is NOT blocking drift without --strict. */
func orchExitSuggestOnlyOutcome() schemas.RunOutcome {
	confidence := 0.8
	routingContinue := schemas.PipelineRoutingContinue
	sameTree := schemas.BaselineRunSameTree
	return schemas.RunOutcome{
		Schema:     schemas.RunOutcomeSchemaID,
		RunID:      "run_01J9ZK6X8A",
		Key:        schemas.RunKeyExample(),
		StartedAt:  orchExitBase().StartedAt,
		FinishedAt: orchExitBase().FinishedAt,
		Status:     schemas.RunStatusDegraded,
		Stages:     orchExitBase().Stages,
		Documents: []schemas.RunDocument{
			{
				DocPath:       "docs/ops.md",
				PolicyOutcome: schemas.PipelineOutcomeSuggest,
				Confidence:    &confidence,
				Routing:       &routingContinue,
				Verification: &schemas.RunDocumentVerification{
					Verdict:          schemas.VerdictReject,
					AutoEligible:     false,
					AutoBlockReasons: []string{"unanchored-claims"},
				},
				PartialCoverage: false,
				BaselineRun:     &sameTree,
			},
		},
		Abstains:   []schemas.RunAbstain{},
		Candidates: []schemas.RunCandidate{},
		Provenance: orchExitBase().Provenance,
		Degradations: []schemas.Degradation{
			{
				Stage:  "decision",
				Kind:   schemas.DegradationCapOverflow,
				Detail: "candidate fan-out capped (fixture)",
			},
		},
		Findings: []schemas.RunFinding{
			{
				DocPath: orchPtr("docs/ops.md"),
				Kind:    "potentially-stale",
				Detail:  "suggest-only fixture finding",
			},
		},
		EarlySignal:      orchExitBase().EarlySignal,
		ConcurrencyGroup: orchExitBase().ConcurrencyGroup,
		StateChurn:       schemas.StateChurn{},
	}
}

/** The run refused to guess — review family, promoted only by --strict. */
func orchExitReviewOnlyOutcome() schemas.RunOutcome {
	outcome := orchExitBase()
	outcome.Documents = []schemas.RunDocument{outcome.Documents[2]}
	outcome.Abstains = []schemas.RunAbstain{}
	return outcome
}

/** A blocked row — the ownership floor (D2), glyph locked. */
func orchExitLockedOutcome() schemas.RunOutcome {
	confidence := 0.88
	routingContinue := schemas.PipelineRoutingContinue
	exactSHA := schemas.BaselineRunExactSHA
	outcome := orchExitBase()
	outcome.Documents = []schemas.RunDocument{
		{
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
		},
	}
	outcome.Abstains = []schemas.RunAbstain{}
	return outcome
}

/** Unknown-coverage-only run — surfaced (D7), never a drift exit. */
func orchExitUnknownCoverageOnlyOutcome() schemas.RunOutcome {
	outcome := orchExitCleanOutcome()
	outcome.UnknownCoverage = []string{"README.md"}
	return outcome
}

// Every fixture must stay a valid run outcome (the TS suite safeParses each).
func TestExitFixturesStaySchemaValidRunOutcomes(t *testing.T) {
	outcomes := []schemas.RunOutcome{
		orchExitCleanOutcome(),
		orchExitDriftOutcome(),
		orchExitSuggestOnlyOutcome(),
		orchExitReviewOnlyOutcome(),
		orchExitLockedOutcome(),
		orchExitUnknownCoverageOnlyOutcome(),
	}
	for i := range outcomes {
		outcome := outcomes[i]
		require.Empty(t, outcome.Validate(), "fixture %d must stay schema-valid", i)
	}
}

// orchExitReport builds the canonical report for one outcome.
func orchExitReport(t *testing.T, outcome schemas.RunOutcome) *schemas.CanonicalReport {
	t.Helper()
	report, err := orchestrator.BuildCanonicalReport(&outcome)
	require.NoError(t, err)
	return report
}

// orchExitCode asserts the resolved exit code (the TS `code` helper).
func orchExitCode(t *testing.T, actual, expected orchestrator.DocsyncExitCode) {
	t.Helper()
	assert.Equal(t, expected, actual)
}

/* ---------------------------------------------------------- exit matrix (V-P7-4) */

func TestV_P7_4_ExitCodeMatrix(t *testing.T) {
	t.Run("clean run exits 0", func(t *testing.T) {
		orchExitCode(t, orchestrator.ResolveCheckExitCode(orchExitReport(t, orchExitCleanOutcome()), nil), orchestrator.ExitOK)
	})

	t.Run("drift run exits 1", func(t *testing.T) {
		orchExitCode(t, orchestrator.ResolveCheckExitCode(orchExitReport(t, orchExitDriftOutcome()), nil), orchestrator.ExitDrift)
	})

	t.Run("configError exits 2 even when drift is present (D8)", func(t *testing.T) {
		report := orchExitReport(t, orchExitDriftOutcome())
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{ConfigError: true}), orchestrator.ExitConfig)
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{ConfigError: true, RuntimeError: true}), orchestrator.ExitConfig)
	})

	t.Run("runtimeError exits 3 even when drift is present (D14)", func(t *testing.T) {
		report := orchExitReport(t, orchExitDriftOutcome())
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{RuntimeError: true}), orchestrator.ExitRuntime)
	})

	t.Run("precedence: config > runtime > drift", func(t *testing.T) {
		report := orchExitReport(t, orchExitDriftOutcome())
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{ConfigError: true}), orchestrator.ExitConfig)
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{RuntimeError: true}), orchestrator.ExitRuntime)
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{}), orchestrator.ExitDrift)
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{Strict: true}), orchestrator.ExitDrift)
	})

	t.Run("error classification lanes", func(t *testing.T) {
		// Lane 2 (exit 2): the config family.
		assert.True(t, orchestrator.IsConfigError(orchestrator.NewPipelineConfigError("bad configuration", "", nil)))
		assert.True(t, orchestrator.IsConfigError(layout.NewDocsyncLayoutError([]layout.LayoutViolation{})))
		assert.True(t, orchestrator.IsConfigError(layout.NewLayoutIoError("unreadable .docsync/", nil)))
		assert.False(t, orchestrator.IsConfigError(errors.New("plain")))
		assert.False(t, orchestrator.IsConfigError(nil))

		// Lane 3 (exit 3): the runtime family.
		assert.True(t, orchestrator.IsRuntimeFailure(orchestrator.NewPipelineRunError("stage failed", "", nil)))
		assert.True(t, orchestrator.IsRuntimeFailure(docutil.New(state.CodeStateCorrupt, "corrupt state")))
		assert.True(t, orchestrator.IsRuntimeFailure(gitx.NewGitExecError(gitx.KindExecFailure, "git rev-parse failed", nil)))
		// The config subclass belongs to lane 2, never lane 3.
		assert.False(t, orchestrator.IsRuntimeFailure(orchestrator.NewPipelineConfigError("bad configuration", "", nil)))
		assert.False(t, orchestrator.IsRuntimeFailure(errors.New("plain")))
	})
}

/* ------------------------------------------------------- strictness (V-P7-5) */

func TestV_P7_5_StrictPromotesAdvisoryRunsBlockedRowsAreAlwaysDrift(t *testing.T) {
	t.Run("suggest-only run: exit 0 without --strict, exit 1 with --strict (D10)", func(t *testing.T) {
		report := orchExitReport(t, orchExitSuggestOnlyOutcome())
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, nil), orchestrator.ExitOK)
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{Strict: true}), orchestrator.ExitDrift)
		// driftDetected still SEES the advisory row (glyph ≠ ok); the exit
		// decision is where strictness is applied.
		assert.True(t, orchestrator.DriftDetected(report))
	})

	t.Run("review-family run: exit 0 without --strict, exit 1 with --strict (D10)", func(t *testing.T) {
		report := orchExitReport(t, orchExitReviewOnlyOutcome())
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, nil), orchestrator.ExitOK)
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{Strict: true}), orchestrator.ExitDrift)
	})

	t.Run("blocked/locked rows count as drift without --strict (D2 floor)", func(t *testing.T) {
		report := orchExitReport(t, orchExitLockedOutcome())
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, nil), orchestrator.ExitDrift)
		orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{Strict: false}), orchestrator.ExitDrift)
	})
}

/* --------------------------------------------- unknown-coverage-only (D7, exit 0) */

func TestDriftDetectedIgnoresUnknownCoverageOnlyRuns(t *testing.T) {
	report := orchExitReport(t, orchExitUnknownCoverageOnlyOutcome())
	assert.False(t, orchestrator.DriftDetected(report))
	orchExitCode(t, orchestrator.ResolveCheckExitCode(report, nil), orchestrator.ExitOK)
	orchExitCode(t, orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{Strict: true}), orchestrator.ExitOK)

	// Surfaced, never silenced (D7) — exit 0 does not mean silence.
	text := orchestrator.RenderCheckText(report, nil)
	assert.Contains(t, text, "Unknown coverage")
	assert.Contains(t, text, "unknown coverage — changed but not mapped by the manifest")
}

// The drift predicate is outcome-based: any row whose policy outcome is NOT
// `ignore` counts, and it never counts unknown coverage (which is not a row).
func TestDriftDetectedIsOutcomeBasedAndIgnoresUnknownCoverage(t *testing.T) {
	assert.False(t, orchestrator.DriftDetected(orchExitReport(t, orchExitCleanOutcome())))
	assert.True(t, orchestrator.DriftDetected(orchExitReport(t, orchExitDriftOutcome())))
	assert.True(t, orchestrator.DriftDetected(orchExitReport(t, orchExitSuggestOnlyOutcome())))
	assert.True(t, orchestrator.DriftDetected(orchExitReport(t, orchExitReviewOnlyOutcome())))
	assert.True(t, orchestrator.DriftDetected(orchExitReport(t, orchExitLockedOutcome())))
	assert.False(t, orchestrator.DriftDetected(orchExitReport(t, orchExitUnknownCoverageOnlyOutcome())))
}
