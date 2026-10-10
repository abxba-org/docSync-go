// Ported from packages/action/test/{event,runner,main,smoke}.test.ts. Black-box
// (package action_test); network-free (OD-2): the event payload/outputs are
// served by an in-memory fs double, the io seam is captured, and the check
// port is an injected double so NO pipeline/REST/model surface is reached.
package action_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/action"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ---------------------------------------------------------- report fixtures */

func reportRow(docPath string, glyph schemas.ReportGlyph, outcome schemas.PipelineOutcome) schemas.CanonicalReportRow {
	confidence := 0.5
	confidenceDisplay := "50% (policy v17, model z-ai/glm-4.7)"
	return schemas.CanonicalReportRow{
		DocPath:           docPath,
		Glyph:             glyph,
		Outcome:           outcome,
		Confidence:        &confidence,
		ConfidenceDisplay: &confidenceDisplay,
		Evidence:          []string{},
	}
}

func makeReport(rows []schemas.CanonicalReportRow) *schemas.CanonicalReport {
	generatedAt := "2026-10-07T00:00:00Z"
	return &schemas.CanonicalReport{
		RunID: "run_test",
		Key: schemas.RunKey{
			Repo: "acme/payments", BaseSHA: "8a73f2c", HeadSHA: "81af92e",
			ManifestVersion: 1, PolicyVersion: 17,
		},
		Status:          schemas.RunStatusCompleted,
		GeneratedAt:     &generatedAt,
		PolicyVersion:   17,
		ModelBindings:   []schemas.RunModelBinding{},
		Rows:            rows,
		Abstains:        []schemas.RunAbstain{},
		Findings:        []schemas.RunFinding{},
		Degradations:    []schemas.Degradation{},
		UnknownCoverage: []string{},
	}
}

var (
	cleanReport       = makeReport([]schemas.CanonicalReportRow{reportRow("docs/a.md", schemas.ReportGlyphOk, schemas.PipelineOutcomeIgnore)})
	advisoryReport    = makeReport([]schemas.CanonicalReportRow{reportRow("docs/a.md", schemas.ReportGlyphWarn, schemas.PipelineOutcomeSuggest)})
	strictDriftReport = makeReport([]schemas.CanonicalReportRow{reportRow("docs/a.md", schemas.ReportGlyphReview, schemas.PipelineOutcomeReview)})
	blockingReport    = makeReport([]schemas.CanonicalReportRow{reportRow("docs/a.md", schemas.ReportGlyphWarn, schemas.PipelineOutcomeAuto)})
)

func stringPtr(s string) *string { return &s }

/* ---------------------------------------------------------- event fixtures */

func sameRepoPayload() map[string]any {
	return map[string]any{
		"repository": map[string]any{"full_name": "acme/payments"},
		"pull_request": map[string]any{
			"number": 7,
			"head": map[string]any{
				"sha":  "abc123",
				"ref":  "feature/x",
				"repo": map[string]any{"full_name": "acme/payments"},
			},
			"base": map[string]any{"ref": "main"},
		},
	}
}

func forkPayload() map[string]any {
	return map[string]any{
		"repository": map[string]any{"full_name": "acme/payments"},
		"pull_request": map[string]any{
			"number": 9,
			"head": map[string]any{
				"sha":  "def456",
				"ref":  "patch",
				"repo": map[string]any{"full_name": "other/payments"},
			},
			"base": map[string]any{"ref": "main"},
		},
	}
}

func eventEnv() *action.RunnerEnv {
	return &action.RunnerEnv{GithubEventName: "pull_request"}
}

/* ================================================================== event */

func TestParseActionEvent(t *testing.T) {
	t.Run("parses a same-repo pull_request event (isFork: false)", func(t *testing.T) {
		ctx, err := action.ParseActionEvent(sameRepoPayload(), eventEnv())
		require.NoError(t, err)
		assert.Equal(t, "pull_request", ctx.EventName)
		require.NotNil(t, ctx.Repo)
		assert.Equal(t, "acme/payments", *ctx.Repo)
		require.NotNil(t, ctx.PR)
		assert.Equal(t, 7, ctx.PR.Number)
		assert.Equal(t, "abc123", ctx.PR.HeadSha)
		assert.Equal(t, "feature/x", ctx.PR.HeadBranch)
		assert.Equal(t, "main", ctx.PR.BaseBranch)
		assert.False(t, ctx.PR.IsFork)
	})

	t.Run("parses a fork pull_request event (isFork: true)", func(t *testing.T) {
		ctx, err := action.ParseActionEvent(forkPayload(), eventEnv())
		require.NoError(t, err)
		require.NotNil(t, ctx.PR)
		assert.True(t, ctx.PR.IsFork)
		assert.Equal(t, 9, ctx.PR.Number)
	})

	t.Run("parses fork detection from a JSON string payload both ways", func(t *testing.T) {
		forkJSON, err := json.Marshal(forkPayload())
		require.NoError(t, err)
		sameJSON, err := json.Marshal(sameRepoPayload())
		require.NoError(t, err)

		forkCtx, err := action.ParseActionEvent(string(forkJSON), eventEnv())
		require.NoError(t, err)
		require.NotNil(t, forkCtx.PR)
		assert.True(t, forkCtx.PR.IsFork)

		sameCtx, err := action.ParseActionEvent(string(sameJSON), eventEnv())
		require.NoError(t, err)
		require.NotNil(t, sameCtx.PR)
		assert.False(t, sameCtx.PR.IsFork)
	})

	t.Run("prefers GITHUB_HEAD_REF/GITHUB_BASE_REF env over payload refs", func(t *testing.T) {
		env := &action.RunnerEnv{
			GithubEventName: "pull_request",
			GithubHeadRef:   "env-head",
			GithubBaseRef:   "env-base",
		}
		ctx, err := action.ParseActionEvent(sameRepoPayload(), env)
		require.NoError(t, err)
		require.NotNil(t, ctx.PR)
		assert.Equal(t, "env-head", ctx.PR.HeadBranch)
		assert.Equal(t, "env-base", ctx.PR.BaseBranch)
	})

	t.Run("resolves an indeterminate fork relationship to isFork: true (fail-closed to report-only)", func(t *testing.T) {
		payload := map[string]any{
			"pull_request": map[string]any{
				"number": 3,
				"head":   map[string]any{"sha": "aaa", "ref": "h", "repo": nil},
				"base":   map[string]any{"ref": "main"},
			},
		}
		ctx, err := action.ParseActionEvent(payload, eventEnv())
		require.NoError(t, err)
		require.NotNil(t, ctx.PR)
		assert.True(t, ctx.PR.IsFork)
	})

	t.Run("parses a push event (branch + after)", func(t *testing.T) {
		env := &action.RunnerEnv{GithubEventName: "push"}
		ctx, err := action.ParseActionEvent(map[string]any{"ref": "refs/heads/main", "after": "cafe01"}, env)
		require.NoError(t, err)
		assert.Equal(t, "push", ctx.EventName)
		require.NotNil(t, ctx.Push)
		assert.Equal(t, "main", ctx.Push.Branch)
		assert.Equal(t, "cafe01", ctx.Push.After)
	})

	t.Run("falls back to GITHUB_REF/GITHUB_SHA for push when the payload omits them", func(t *testing.T) {
		env := &action.RunnerEnv{GithubEventName: "push", GithubRef: "refs/heads/dev", GithubSha: "cafe02"}
		ctx, err := action.ParseActionEvent(map[string]any{}, env)
		require.NoError(t, err)
		require.NotNil(t, ctx.Push)
		assert.Equal(t, "dev", ctx.Push.Branch)
		assert.Equal(t, "cafe02", ctx.Push.After)
	})

	t.Run("maps a non-PR/non-push event to other", func(t *testing.T) {
		env := &action.RunnerEnv{GithubEventName: "release"}
		ctx, err := action.ParseActionEvent(map[string]any{"action": "published"}, env)
		require.NoError(t, err)
		assert.Equal(t, "other", ctx.EventName)
		assert.Nil(t, ctx.PR)
		assert.Nil(t, ctx.Push)
	})

	t.Run("throws a typed error on invalid JSON (fail-closed)", func(t *testing.T) {
		_, err := action.ParseActionEvent("{not json", eventEnv())
		var eventErr *action.ActionEventError
		require.ErrorAs(t, err, &eventErr)
	})

	t.Run("throws a typed error when a pull_request event carries no pull_request object", func(t *testing.T) {
		_, err := action.ParseActionEvent(map[string]any{"repository": map[string]any{"full_name": "acme/payments"}}, eventEnv())
		var eventErr *action.ActionEventError
		require.ErrorAs(t, err, &eventErr)
	})

	t.Run("throws a typed error when the pull_request object is malformed (no head sha)", func(t *testing.T) {
		_, err := action.ParseActionEvent(map[string]any{
			"pull_request": map[string]any{"number": 1, "head": map[string]any{}, "base": map[string]any{"ref": "main"}},
		}, eventEnv())
		var eventErr *action.ActionEventError
		require.ErrorAs(t, err, &eventErr)
	})

	t.Run("throws a typed error when GITHUB_EVENT_NAME is missing", func(t *testing.T) {
		_, err := action.ParseActionEvent(sameRepoPayload(), &action.RunnerEnv{})
		var eventErr *action.ActionEventError
		require.ErrorAs(t, err, &eventErr)
	})

	t.Run("throws a typed error on a JSON-array payload string", func(t *testing.T) {
		_, err := action.ParseActionEvent("[1,2,3]", eventEnv())
		var eventErr *action.ActionEventError
		require.ErrorAs(t, err, &eventErr)
	})
}

/* ================================================================= runner */

// portDouble records the runner's inputs and returns a fixed result kind (or
// one selected from the input).
type portDouble struct {
	port  action.CheckRunPort
	calls *[]action.CheckRunPortInput
}

func staticDouble(result action.CheckRunPortResult) portDouble {
	calls := &[]action.CheckRunPortInput{}
	port := func(in action.CheckRunPortInput) (action.CheckRunPortResult, error) {
		*calls = append(*calls, in)
		return result, nil
	}
	return portDouble{port: port, calls: calls}
}

func selectingDouble(fn func(in action.CheckRunPortInput) action.CheckRunPortResult) portDouble {
	calls := &[]action.CheckRunPortInput{}
	port := func(in action.CheckRunPortInput) (action.CheckRunPortResult, error) {
		*calls = append(*calls, in)
		return fn(in), nil
	}
	return portDouble{port: port, calls: calls}
}

func TestRunActionRunner_ForkReportOnly(t *testing.T) {
	t.Run("V-P8-16 partial: fork PR → report-only check; no writer invocation; no PR creation", func(t *testing.T) {
		double := selectingDouble(func(in action.CheckRunPortInput) action.CheckRunPortResult {
			return action.CheckRunPortResult{
				Kind:          action.CheckRunPortResultReport,
				Report:        advisoryReport,
				WriterInvoked: !in.ForkReportOnly,
				PRCreated:     !in.ForkReportOnly,
			}
		})

		result := action.RunActionRunner(action.RunActionRunnerDeps{
			Check: double.port, Env: eventEnv(), Payload: forkPayload(),
		})

		require.Len(t, *double.calls, 1)
		call := (*double.calls)[0]
		assert.True(t, call.ForkReportOnly)
		require.NotNil(t, call.PR)
		assert.Equal(t, 9, call.PR.Number)
		assert.Equal(t, "def456", call.Head)
		assert.Equal(t, "main", call.Base)
		assert.Equal(t, "acme/payments", call.Repo)

		conclusion := orchestrator.ConclusionNeutral
		assert.Equal(t, &conclusion, result.Conclusion)
		assert.Equal(t, orchestrator.ExitOK, result.ExitCode)
		assert.Contains(t, result.Summary, "advisory mode (D10 default)")

		// Fork + BLOCKING drift: the drift-only failure is downgraded to neutral
		// (advisory on forks, A7); exit-1 semantics are preserved for scripts.
		downgradeDouble := selectingDouble(func(in action.CheckRunPortInput) action.CheckRunPortResult {
			return action.CheckRunPortResult{
				Kind:          action.CheckRunPortResultReport,
				Report:        blockingReport,
				WriterInvoked: !in.ForkReportOnly,
				PRCreated:     !in.ForkReportOnly,
			}
		})
		downgraded := action.RunActionRunner(action.RunActionRunnerDeps{
			Check:   downgradeDouble.port,
			Env:     &action.RunnerEnv{GithubEventName: "pull_request", DocsyncStrict: "true"},
			Payload: forkPayload(),
		})
		neutral := orchestrator.ConclusionNeutral
		assert.Equal(t, &neutral, downgraded.Conclusion)
		assert.Equal(t, orchestrator.ExitDrift, downgraded.ExitCode)
		assert.Contains(t, downgraded.Summary, "advisory on fork PRs")

		// Same drift on a NON-fork PR with strict: no downgrade — real failure.
		strictDouble := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: blockingReport})
		strictNonFork := action.RunActionRunner(action.RunActionRunnerDeps{
			Check:   strictDouble.port,
			Env:     &action.RunnerEnv{GithubEventName: "pull_request", DocsyncStrict: "true"},
			Payload: sameRepoPayload(),
		})
		failure := orchestrator.ConclusionFailure
		assert.Equal(t, &failure, strictNonFork.Conclusion)
		assert.Equal(t, orchestrator.ExitDrift, strictNonFork.ExitCode)

		// Non-fork run through the SAME double: write path reachable (contrast).
		nonFork := action.RunActionRunner(action.RunActionRunnerDeps{
			Check: double.port, Env: eventEnv(), Payload: sameRepoPayload(),
		})
		require.Len(t, *double.calls, 2)
		assert.False(t, (*double.calls)[1].ForkReportOnly)
		assert.Equal(t, orchestrator.ExitOK, nonFork.ExitCode)
	})

	t.Run("V-P8-16 partial: fork PR with a contract-violating port (writer invoked) fails closed to inconclusive", func(t *testing.T) {
		double := staticDouble(action.CheckRunPortResult{
			Kind: action.CheckRunPortResultReport, Report: advisoryReport, WriterInvoked: true,
		})
		result := action.RunActionRunner(action.RunActionRunnerDeps{
			Check: double.port, Env: eventEnv(), Payload: forkPayload(),
		})
		assert.Equal(t, orchestrator.ExitRuntime, result.ExitCode)
		neutral := orchestrator.ConclusionNeutral
		assert.Equal(t, &neutral, result.Conclusion)
		assert.True(t, result.Inconclusive)
		assert.Contains(t, result.Summary, "contract violation")
	})

	t.Run("V-P8-16 partial: fork report carrying abstains WITHOUT the report-only marker fails closed", func(t *testing.T) {
		report := makeReport([]schemas.CanonicalReportRow{reportRow("docs/a.md", schemas.ReportGlyphWarn, schemas.PipelineOutcomeSuggest)})
		report.Abstains = []schemas.RunAbstain{{
			AbstainID: "ab_foreign0001",
			DocPath:   stringPtr("docs/a.md"),
			Reason:    "other",
			Detail:    "unrelated operational abstain",
		}}
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: report})
		result := action.RunActionRunner(action.RunActionRunnerDeps{
			Check: double.port, Env: eventEnv(), Payload: forkPayload(),
		})
		assert.Equal(t, orchestrator.ExitRuntime, result.ExitCode)
		neutral := orchestrator.ConclusionNeutral
		assert.Equal(t, &neutral, result.Conclusion)
		assert.True(t, result.Inconclusive)
		assert.Contains(t, result.Summary, "report-only downgrade marker")
	})

	t.Run("V-P8-16 partial: fork report carrying the report-only marker passes the guard", func(t *testing.T) {
		report := makeReport([]schemas.CanonicalReportRow{reportRow("docs/a.md", schemas.ReportGlyphWarn, schemas.PipelineOutcomeSuggest)})
		report.Abstains = []schemas.RunAbstain{{
			AbstainID: "ab_marker0001",
			DocPath:   stringPtr("docs/a.md"),
			Reason:    "other",
			Detail:    action.ReportOnlyAbstainDetail,
		}}
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: report})
		result := action.RunActionRunner(action.RunActionRunnerDeps{
			Check: double.port, Env: eventEnv(), Payload: forkPayload(),
		})
		assert.Equal(t, orchestrator.ExitOK, result.ExitCode)
		neutral := orchestrator.ConclusionNeutral
		assert.Equal(t, &neutral, result.Conclusion)
		assert.False(t, result.Inconclusive)
	})
}

func TestRunActionRunner_ExitConclusionMatrix(t *testing.T) {
	cases := []struct {
		name            string
		env             *action.RunnerEnv
		result          action.CheckRunPortResult
		exitCode        orchestrator.DocsyncExitCode
		conclusion      *orchestrator.CheckRunConclusion
		summaryIncludes []string
	}{
		{
			name: "0 → success (clean report)", env: eventEnv(),
			result:     action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport},
			exitCode:   orchestrator.ExitOK,
			conclusion: conclusion(orchestrator.ConclusionSuccess),
		},
		{
			name:     "advisory drift (suggest) → exit 0, neutral (D10 default, no enforcement opt-in)",
			env:      eventEnv(),
			result:   action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: advisoryReport},
			exitCode: orchestrator.ExitOK, conclusion: conclusion(orchestrator.ConclusionNeutral),
			summaryIncludes: []string{"advisory mode (D10 default)"},
		},
		{
			name:     "strict advisory drift (review) → exit 1, failure (D10 enforcement)",
			env:      &action.RunnerEnv{GithubEventName: "pull_request", DocsyncStrict: "true"},
			result:   action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: strictDriftReport},
			exitCode: orchestrator.ExitDrift, conclusion: conclusion(orchestrator.ConclusionFailure),
		},
		{
			name:     "blocking drift (auto) → exit 1 (D2 floor), neutral absent strict/opt-in",
			env:      eventEnv(),
			result:   action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: blockingReport},
			exitCode: orchestrator.ExitDrift, conclusion: conclusion(orchestrator.ConclusionNeutral),
		},
		{
			name:     "enforcement opt-in advisory → failure without strict",
			env:      &action.RunnerEnv{GithubEventName: "pull_request", DocsyncEnforcementOptIn: "true"},
			result:   action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: advisoryReport},
			exitCode: orchestrator.ExitOK, conclusion: conclusion(orchestrator.ConclusionFailure),
		},
		{
			name: "2 → failure, config error with offending lines (D8)",
			env:  eventEnv(),
			result: action.CheckRunPortResult{Kind: action.CheckRunPortResultConfigError, Diagnostics: []orchestrator.ConfigErrorDiagnostic{
				{File: ".docsync/manifest.yml", Message: "unknown mapping key", Line: intPtr(12)},
			}},
			exitCode: orchestrator.ExitConfig, conclusion: conclusion(orchestrator.ConclusionFailure),
			summaryIncludes: []string{"Configuration error (D8)"},
		},
		{
			name:     "3 → neutral inconclusive (D14)",
			env:      eventEnv(),
			result:   action.CheckRunPortResult{Kind: action.CheckRunPortResultRuntimeInconclusive, Reason: "analyzer outage"},
			exitCode: orchestrator.ExitRuntime, conclusion: conclusion(orchestrator.ConclusionNeutral),
			summaryIncludes: []string{"Inconclusive (D14)"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			double := staticDouble(c.result)
			result := action.RunActionRunner(action.RunActionRunnerDeps{
				Check: double.port, Env: c.env, Payload: sameRepoPayload(),
				Logger: &action.ActionLogger{Info: func(string) {}, Warn: func(string) {}},
			})
			assert.Equal(t, c.exitCode, result.ExitCode, c.name)
			assert.Equal(t, c.conclusion, result.Conclusion, c.name)
			for _, fragment := range c.summaryIncludes {
				assert.Contains(t, result.Summary, fragment, c.name)
			}
		})
	}

	t.Run("config-error details list the offending file:line entries", func(t *testing.T) {
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultConfigError, Diagnostics: []orchestrator.ConfigErrorDiagnostic{
			{File: ".docsync/manifest.yml", Message: "unknown mapping key", Line: intPtr(12)},
		}})
		cfg := action.RunActionRunner(action.RunActionRunnerDeps{Check: double.port, Env: eventEnv(), Payload: sameRepoPayload()})
		require.NotNil(t, cfg.Text)
		assert.Contains(t, *cfg.Text, "`.docsync/manifest.yml:12`: unknown mapping key")
	})
}

func TestRunActionRunner_InconclusiveAndLanes(t *testing.T) {
	noopLogger := &action.ActionLogger{Info: func(string) {}, Warn: func(string) {}}

	t.Run("V-P8-5 partial: runtime-inconclusive → neutral, never 'no docs affected'", func(t *testing.T) {
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultRuntimeInconclusive, Reason: "model stage failure"})
		result := action.RunActionRunner(action.RunActionRunnerDeps{Check: double.port, Env: eventEnv(), Payload: sameRepoPayload(), Logger: noopLogger})
		assert.Equal(t, orchestrator.ExitRuntime, result.ExitCode)
		neutral := orchestrator.ConclusionNeutral
		assert.Equal(t, &neutral, result.Conclusion)
		assert.True(t, result.Inconclusive)
		assert.Contains(t, result.Summary, "NOT a pass on documentation consistency")
		lower := strings.ToLower(result.Summary)
		assert.NotContains(t, lower, "no docs affected")
		assert.NotContains(t, lower, "no documentation drift")
	})

	t.Run("a throwing port fails closed to exit 3 / neutral inconclusive (D14)", func(t *testing.T) {
		port := func(action.CheckRunPortInput) (action.CheckRunPortResult, error) {
			return action.CheckRunPortResult{}, plainErr("boom")
		}
		result := action.RunActionRunner(action.RunActionRunnerDeps{Check: port, Env: eventEnv(), Payload: sameRepoPayload(), Logger: noopLogger})
		assert.Equal(t, orchestrator.ExitRuntime, result.ExitCode)
		neutral := orchestrator.ConclusionNeutral
		assert.Equal(t, &neutral, result.Conclusion)
		assert.True(t, result.Inconclusive)
		assert.Contains(t, result.Summary, "boom")
	})

	t.Run("a malformed event payload maps to the config-error lane (exit 2, failure, D8)", func(t *testing.T) {
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport})
		result := action.RunActionRunner(action.RunActionRunnerDeps{Check: double.port, Env: eventEnv(), Payload: `{"broken json`, Logger: noopLogger})
		assert.Len(t, *double.calls, 0)
		assert.Equal(t, orchestrator.ExitConfig, result.ExitCode)
		failure := orchestrator.ConclusionFailure
		assert.Equal(t, &failure, result.Conclusion)
		assert.Contains(t, result.Summary, "Configuration error (D8)")
		require.NotNil(t, result.Text)
		assert.Contains(t, *result.Text, "not valid JSON")
	})

	t.Run("report-only:true forces forkReportOnly even on same-repo PRs", func(t *testing.T) {
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport})
		_ = action.RunActionRunner(action.RunActionRunnerDeps{
			Check:   double.port,
			Env:     &action.RunnerEnv{GithubEventName: "pull_request", DocsyncReportOnly: "true"},
			Payload: sameRepoPayload(),
		})
		require.Len(t, *double.calls, 1)
		assert.True(t, (*double.calls)[0].ForkReportOnly)
	})

	t.Run("push events drive the port without a pr field", func(t *testing.T) {
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport})
		result := action.RunActionRunner(action.RunActionRunnerDeps{
			Check: double.port,
			Env:   &action.RunnerEnv{GithubEventName: "push"},
			Payload: map[string]any{
				"ref": "refs/heads/main", "after": "cafe01",
				"repository": map[string]any{"full_name": "acme/payments"},
			},
			Logger: noopLogger,
		})
		require.Len(t, *double.calls, 1)
		assert.Nil(t, (*double.calls)[0].PR)
		assert.Equal(t, "cafe01", (*double.calls)[0].Head)
		assert.False(t, (*double.calls)[0].ForkReportOnly)
		success := orchestrator.ConclusionSuccess
		assert.Equal(t, &success, result.Conclusion)
	})

	t.Run("an event with no determinable head commit is inconclusive, never 'no drift'", func(t *testing.T) {
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport})
		result := action.RunActionRunner(action.RunActionRunnerDeps{
			Check: double.port, Env: &action.RunnerEnv{GithubEventName: "release"}, Payload: map[string]any{}, Logger: noopLogger,
		})
		assert.Len(t, *double.calls, 0)
		assert.Equal(t, orchestrator.ExitRuntime, result.ExitCode)
		assert.True(t, result.Inconclusive)
	})

	t.Run("port input is derived from parseActionEvent context (pr identity round-trip)", func(t *testing.T) {
		double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport})
		ctx, err := action.ParseActionEvent(sameRepoPayload(), eventEnv())
		require.NoError(t, err)
		_ = action.RunActionRunner(action.RunActionRunnerDeps{Check: double.port, Env: eventEnv(), Payload: sameRepoPayload(), Logger: noopLogger})
		require.NotNil(t, ctx.PR)
		require.Len(t, *double.calls, 1)
		assert.Equal(t, ctx.PR.HeadSha, (*double.calls)[0].Head)
		assert.Equal(t, ctx.PR.BaseBranch, (*double.calls)[0].Base)
	})
}

func TestRunActionRunner_Redaction(t *testing.T) {
	const token = "ghs_planted_secret_token_0123456789"
	env := &action.RunnerEnv{
		GithubEventName: "pull_request", GithubToken: token, DocsyncStrict: "true",
	}
	lanes := []action.CheckRunPortResult{
		{Kind: action.CheckRunPortResultReport, Report: advisoryReport},
		{Kind: action.CheckRunPortResultConfigError, Diagnostics: []orchestrator.ConfigErrorDiagnostic{
			{File: ".docsync/manifest.yml", Message: "bad key", Line: intPtr(1)},
		}},
		{Kind: action.CheckRunPortResultRuntimeInconclusive, Reason: "outage"},
	}
	for _, lane := range lanes {
		double := staticDouble(lane)
		result := action.RunActionRunner(action.RunActionRunnerDeps{Check: double.port, Env: env, Payload: sameRepoPayload()})
		serialized, err := json.Marshal(result)
		require.NoError(t, err)
		assert.NotContains(t, string(serialized), token)
		assert.NotContains(t, string(serialized), "ghs_planted")
	}
}

/* =================================================================== main */

type runCapture struct {
	env        map[string]string
	files      map[string]string
	stderr     []string
	exitCodes  []int
	outputs    []struct{ path, data string }
	portInputs []action.CheckRunPortInput
	check      action.CheckRunPort
}

func makeCapture(env, files map[string]string) *runCapture {
	capture := &runCapture{env: env, files: files}
	capture.check = func(in action.CheckRunPortInput) (action.CheckRunPortResult, error) {
		capture.portInputs = append(capture.portInputs, in)
		return action.CheckRunPortResult{Kind: action.CheckRunPortResultRuntimeInconclusive, Reason: "injected test lane (never runs the pipeline)"}, nil
	}
	return capture
}

func (c *runCapture) deps() action.MainDeps {
	return action.MainDeps{
		Env:   c.env,
		Check: c.check,
		FS: &action.MainFS{
			ReadFile: func(path string) (string, error) {
				if content, ok := c.files[path]; ok {
					return content, nil
				}
				return "", plainErr("ENOENT (fixture): " + path)
			},
			AppendFile: func(path, data string) error {
				c.outputs = append(c.outputs, struct{ path, data string }{path, data})
				return nil
			},
		},
		IO: &action.MainIO{
			Stderr:      func(line string) { c.stderr = append(c.stderr, line) },
			SetExitCode: func(code int) { c.exitCodes = append(c.exitCodes, code) },
		},
	}
}

func TestMapActionInputsToRunnerEnv(t *testing.T) {
	t.Run("maps INPUT_STRICT=true and INPUT_REPORT_ONLY=true", func(t *testing.T) {
		env := action.MapActionInputsToRunnerEnv(map[string]string{"INPUT_STRICT": "true", "INPUT_REPORT_ONLY": "true"})
		assert.Equal(t, "true", env.DocsyncStrict)
		assert.Equal(t, "true", env.DocsyncReportOnly)
	})

	t.Run("maps the defaults honestly: absent/false inputs → explicit false flags", func(t *testing.T) {
		env := action.MapActionInputsToRunnerEnv(map[string]string{"INPUT_STRICT": "false", "INPUT_REPORT_ONLY": "auto"})
		assert.Equal(t, "false", env.DocsyncStrict)
		assert.Equal(t, "false", env.DocsyncReportOnly)
	})

	t.Run("flows INPUT_BASE to GITHUB_BASE_REF (the base ref the port diffs against)", func(t *testing.T) {
		env := action.MapActionInputsToRunnerEnv(map[string]string{
			"INPUT_BASE": "8a73f2c", "GITHUB_BASE_REF": "main", "GITHUB_EVENT_NAME": "pull_request",
		})
		assert.Equal(t, "8a73f2c", env.GithubBaseRef)
		assert.Equal(t, "pull_request", env.GithubEventName)
	})

	t.Run("passes the GITHUB_* runner/event keys through untouched", func(t *testing.T) {
		env := action.MapActionInputsToRunnerEnv(map[string]string{
			"GITHUB_EVENT_NAME": "push", "GITHUB_SHA": "cafe01", "GITHUB_REPOSITORY": "acme/payments",
			"GITHUB_TOKEN": "ghs_planted_entry_token", "UNRELATED": "dropped",
		})
		assert.Equal(t, "push", env.GithubEventName)
		assert.Equal(t, "cafe01", env.GithubSha)
		assert.Equal(t, "acme/payments", env.GithubRepository)
		assert.Equal(t, "ghs_planted_entry_token", env.GithubToken)
	})
}

func TestRunMain(t *testing.T) {
	eventJSON, err := json.Marshal(sameRepoPayload())
	require.NoError(t, err)
	eventJSONString := string(eventJSON)

	t.Run("maps the inputs onto the port input: INPUT_BASE → base, GITHUB_REPOSITORY → repo fallback", func(t *testing.T) {
		capture := makeCapture(
			map[string]string{
				"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json",
				"GITHUB_REPOSITORY": "acme/payments", "INPUT_BASE": "8a73f2c", "INPUT_STRICT": "true",
			},
			map[string]string{"event.json": eventJSONString},
		)
		result := action.RunMain(capture.deps())

		require.Len(t, capture.portInputs, 1)
		assert.Equal(t, "acme/payments", capture.portInputs[0].Repo)
		assert.Equal(t, "8a73f2c", capture.portInputs[0].Base)
		assert.Equal(t, "abc123", capture.portInputs[0].Head)
		require.NotNil(t, capture.portInputs[0].PR)
		assert.Equal(t, 7, capture.portInputs[0].PR.Number)
		assert.Equal(t, orchestrator.ExitRuntime, result.ExitCode)
		neutral := orchestrator.ConclusionNeutral
		assert.Equal(t, &neutral, result.Conclusion)
		assert.True(t, result.Inconclusive)
	})

	t.Run("writes the outputs protocol to the injected GITHUB_OUTPUT path and propagates the exit code", func(t *testing.T) {
		capture := makeCapture(
			map[string]string{
				"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json",
				"GITHUB_OUTPUT": "/tmp/kilo/github_output_fixture",
			},
			map[string]string{"event.json": eventJSONString},
		)
		result := action.RunMain(capture.deps())

		require.Len(t, capture.outputs, 1)
		assert.Equal(t, "/tmp/kilo/github_output_fixture", capture.outputs[0].path)
		assert.Equal(t, "exit-code=3\nconclusion=neutral\n", capture.outputs[0].data)
		assert.Equal(t, []int{3}, capture.exitCodes)
		assert.True(t, hasStderrPrefix(capture.stderr, "docsync action: exit 3, conclusion neutral"))
		assert.Equal(t, orchestrator.ExitRuntime, result.ExitCode)
	})

	t.Run("missing GITHUB_EVENT_PATH fails closed to the config lane (exit 2, failure) without calling the port", func(t *testing.T) {
		capture := makeCapture(
			map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_OUTPUT": "/tmp/kilo/github_output_fixture"},
			map[string]string{},
		)
		result := action.RunMain(capture.deps())

		assert.Len(t, capture.portInputs, 0)
		assert.Equal(t, orchestrator.ExitConfig, result.ExitCode)
		failure := orchestrator.ConclusionFailure
		assert.Equal(t, &failure, result.Conclusion)
		assert.Contains(t, result.Summary, "Configuration error")
		require.NotNil(t, result.Text)
		assert.Contains(t, *result.Text, "GITHUB_EVENT_PATH is missing")
		require.Len(t, capture.outputs, 1)
		assert.Equal(t, "exit-code=2\nconclusion=failure\n", capture.outputs[0].data)
		assert.Equal(t, []int{2}, capture.exitCodes)
	})

	t.Run("an unreadable GITHUB_EVENT_PATH fails closed to the config lane (exit 2, D8)", func(t *testing.T) {
		capture := makeCapture(
			map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "missing.json"},
			map[string]string{},
		)
		result := action.RunMain(capture.deps())

		assert.Len(t, capture.portInputs, 0)
		assert.Equal(t, orchestrator.ExitConfig, result.ExitCode)
		require.NotNil(t, result.Text)
		assert.Contains(t, *result.Text, "GITHUB_EVENT_PATH is unreadable")
		assert.Equal(t, []int{2}, capture.exitCodes)
	})

	t.Run("an invalid JSON payload maps to the runner config lane (exit 2) — fail-closed, never 'no drift'", func(t *testing.T) {
		capture := makeCapture(
			map[string]string{
				"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json",
				"GITHUB_OUTPUT": "/tmp/kilo/github_output_fixture",
			},
			map[string]string{"event.json": `{"broken json`},
		)
		result := action.RunMain(capture.deps())

		assert.Len(t, capture.portInputs, 0)
		assert.Equal(t, orchestrator.ExitConfig, result.ExitCode)
		failure := orchestrator.ConclusionFailure
		assert.Equal(t, &failure, result.Conclusion)
		require.NotNil(t, result.Text)
		assert.Contains(t, *result.Text, "not valid JSON")
		require.Len(t, capture.outputs, 1)
		assert.Equal(t, "exit-code=2\nconclusion=failure\n", capture.outputs[0].data)
		assert.Equal(t, []int{2}, capture.exitCodes)
	})
}

func TestSmoke(t *testing.T) {
	t.Run("exposes a version", func(t *testing.T) {
		assert.NotEmpty(t, action.ActionPackageVersion)
	})
}

/* ------------------------------------------------------------- test utils */

func conclusion(c orchestrator.CheckRunConclusion) *orchestrator.CheckRunConclusion { return &c }

func intPtr(i int) *int { return &i }

func hasStderrPrefix(lines []string, prefix string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func plainErr(msg string) error { return errors.New(msg) }
