package cli_test

// Coverage-completion suite, part 4 — the EXPORTED CLI surfaces (package
// cli_test): argv parsing lanes (parseLoose / parseCliArgs validators),
// RunCli dispatch, and the pr / audit / check verb lanes the existing suites
// do not reach. Deterministic throughout: local git fixtures, replay
// transports and injected flow runners — no network, no wall clock.

import (
	"context"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covParseErr(t *testing.T, argv ...string) string {
	t.Helper()
	// ParseCliArgs parses the argv as-is (RunCli strips the program name).
	return cli.ParseCliArgs(argv).Error
}

/* --------------------------------------------------------------- argv parsing */

func TestCOV_CLI_301_ParseLooseInlineValueForm(t *testing.T) {
	// --name=value (the inline equals form; parseLoose's hasInline lane).
	args := cliArgs(t, "check", "--base=HEAD", "--run-id=01J9ZK7Q2M8P4X6T2W5Y3C0A9E")
	require.NotNil(t, args.Base)
	assert.Equal(t, "HEAD", *args.Base)
	require.NotNil(t, args.RunID)
	assert.Equal(t, "01J9ZK7Q2M8P4X6T2W5Y3C0A9E", *args.RunID)
}

func TestCOV_CLI_302_ParseLooseUnknownFlagsBecomePositionals(t *testing.T) {
	// A non-strict parse never errors at the tokenizer: the unknown flag and
	// its value land in positionals, reported by the verb-position check.
	msg := covParseErr(t, "check", "--frobnicate", "value")
	require.NotEmpty(t, msg)
	assert.Contains(t, msg, "unexpected argument(s)")
	assert.Contains(t, msg, `"--frobnicate"`)

	// An unknown flag WITHOUT a following value is still a positional.
	msg = covParseErr(t, "check", "--frobnicate")
	require.NotEmpty(t, msg)
	assert.Contains(t, msg, "unexpected argument(s)")
}

func TestCOV_CLI_303_ParseLooseUnknownShortClusterBecomesPositionals(t *testing.T) {
	// -x value → an unknown short cluster plus its value, both positionals.
	msg := covParseErr(t, "check", "-x", "value")
	require.NotEmpty(t, msg)
	assert.Contains(t, msg, "unexpected argument(s)")
	assert.Contains(t, msg, `"-x"`)
}

func TestCOV_CLI_304_ParseCliArgsFlagValidatorsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		argv    []string
		wantSub string
	}{
		{"unknown glyphs", []string{"check", "--glyphs", "emoji"}, "unknown --glyphs value: emoji"},
		{"non-numeric source-pr", []string{"pr", "--source-pr", "abc"}, "invalid --source-pr value: abc"},
		{"zero source-pr", []string{"pr", "--source-pr", "0"}, "invalid --source-pr value: 0"},
		{"overflow source-pr", []string{"pr", "--source-pr", "9999999999"}, "invalid --source-pr value: 9999999999"},
		{"PR trigger refused", []string{"audit", "--trigger", "push"}, "unknown --trigger value: push"},
		{"blank branch", []string{"audit", "--branch", "   "}, "invalid --branch value"},
		{"zero concurrency", []string{"audit", "--concurrency", "0"}, "invalid --concurrency value: 0"},
		{"fractional concurrency", []string{"audit", "--concurrency", "1.5"}, "invalid --concurrency value: 1.5"},
		{"zero commit-walk", []string{"audit", "--commit-walk", "0"}, "invalid --commit-walk value: 0"},
		{"unknown verb", []string{"frobnicate"}, "unknown verb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := covParseErr(t, tc.argv...)
			require.NotEmpty(t, msg, "the validator fails closed")
			assert.Contains(t, msg, tc.wantSub)
		})
	}
}

func TestCOV_CLI_305_ParseCliArgsDefaultBranchAndConcurrencyClamp(t *testing.T) {
	args := cliArgs(t, "pr", "--default-branch", "main")
	require.NotNil(t, args.DefaultBranch)
	assert.Equal(t, "main", *args.DefaultBranch)

	args = cliArgs(t, "audit", "--concurrency", "2147483647")
	require.NotNil(t, args.AuditConcurrency)
	assert.Equal(t, "64", *args.AuditConcurrency, "the concurrency is clamped to the engine ceiling")

	// The commit-walk is NOT clamped by the parser (the audit verb owns it).
	args = cliArgs(t, "audit", "--commit-walk", "1000000")
	require.NotNil(t, args.AuditCommitWalk)
	assert.Equal(t, "1000000", *args.AuditCommitWalk)
}

func TestCOV_CLI_306_ParseCliArgsMultipleDocsAndHelpShortFlag(t *testing.T) {
	args := cliArgs(t, "fix", "--doc", "docs/api.md", "--doc", "docs/other.md")
	assert.Equal(t, []string{"docs/api.md", "docs/other.md"}, args.DocPaths)

	args = cliArgs(t, "check", "-h")
	assert.True(t, args.Help)

	args = cliArgs(t, "check", "--no-quiet")
	assert.False(t, args.Quiet)
}

/* -------------------------------------------------------------------- RunCli */

func TestCOV_CLI_310_RunCliDispatchesInitWithJSONLane(t *testing.T) {
	captured := captureIo()

	exit := cli.RunCli([]string{"init", "--json"}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})

	assert.Equal(t, cli.ExitOK, exit)
	assert.JSONEq(t, `{"ok":true,"messages":[
		"docsync init: wrote .docsync/manifest.yml",
		"docsync init: wrote .docsync/models.yml",
		"docsync init: wrote .docsync/instructions.md",
		"docsync init: skeleton validates cleanly (placeholder models: openai/gpt-4o-mini — replace them)"]}`,
		captured.Out())
	assert.Empty(t, captured.Err())
}

func TestCOV_CLI_311_RunCliRejectsAMissingVerb(t *testing.T) {
	captured := captureIo()

	exit := cli.RunCli([]string{}, cli.RunCliOptions{Io: captured})

	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "no verb given")
	assert.Contains(t, captured.Err(), "Usage: docsync <verb> [flags]")
}

/* ---------------------------------------------------------------------- RunPr */

func TestCOV_CLI_320_RunPrSourcePrUnresolvableIsConfig(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	captured := captureIo()

	// pull/42/head does not exist in the local fixture → the pr verb refuses
	// BEFORE the flow runner (usage defect, exit 2).
	exit := cli.RunPr(cliArgs(t, "pr", "--source-pr", "42", "--base", "HEAD"), captured, cli.PrOptions{
		Cwd: repo.root,
		Env: prEnv(),
		Flow: func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
			t.Fatal("the pr flow must never be entered when the source PR cannot be resolved")
			return nil, nil
		},
	})

	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "could not resolve pull/42/head:")
	assert.Empty(t, captured.Out())
}

func TestCOV_CLI_321_RunPrDefaultBranchResolutionReachesTheFlow(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	captured := captureIo()
	var seen *cli.PrFlowInput

	exit := cli.RunPr(cliArgs(t, "pr", "--default-branch", "main", "--base", "HEAD"), captured, cli.PrOptions{
		Cwd: repo.root,
		Env: prEnv(),
		Flow: func(_ context.Context, in *cli.PrFlowInput) (*cli.PrPayload, error) {
			seen = in
			return &cli.PrPayload{
				Applied:         []cli.FixFlowApplied{{DocPath: "docs/api.md", PatchRef: "state/docs/docs__api.md#patch-aaaaaaaaaaaaaaaa"}},
				FixSummaryLines: []string{"[ok] applied docs/api.md (patch-aaaaaaaaaaaaaaaa)"},
				Ignored:         []cli.PrIssueRef{{DocPath: "docs/locked.md", Reason: "locked-or-protected", Detail: "locked resolves at ANY confidence"}},
				PrOpened:        true,
				PrNumber:        pr42(12),
				RecordedReplies: 2,
			}, nil
		},
	})

	require.NotNil(t, seen, "the flow was entered after resolving the default branch")
	assert.NotEmpty(t, seen.ChangePr, "the resolved main sha is threaded to the flow")
	assert.NotEqual(t, "main", seen.ChangePr)
	assert.Equal(t, "HEAD", seen.Base, "the explicit --base rides through verbatim")
	assert.Equal(t, cli.ExitDrift, exit)
	text := captured.Out()
	assert.Contains(t, text, "Applied 1 verified minimal patch(es)")
	assert.Contains(t, text, "⏭ docs/locked.md (locked-or-protected — locked resolves at ANY confidence)")
	assert.Contains(t, text, "[ok] applied docs/api.md")
	assert.Contains(t, text, "Opened documentation PR #12 (2 reviewer repl")
}

func TestCOV_CLI_322_RunPrFlowErrorMapsToRuntimeExit(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	captured := captureIo()

	exit := cli.RunPr(cliArgs(t, "pr", "--base", "HEAD"), captured, cli.PrOptions{
		Cwd: repo.root,
		Env: prEnv(),
		Flow: func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
			return nil, orchestrator.NewPipelineRunError("doc PR flow: upstream refused", "", errNew("upstream 500"))
		},
	})

	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Contains(t, captured.Err(), "doc PR flow: upstream refused")
}

func TestCOV_CLI_323_RunPrFlowConfigErrorMapsToConfigExit(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	captured := captureIo()

	exit := cli.RunPr(cliArgs(t, "pr", "--base", "HEAD"), captured, cli.PrOptions{
		Cwd: repo.root,
		Env: prEnv(),
		Flow: func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
			return nil, orchestrator.NewPipelineConfigError("no policy version pinned", "", nil)
		},
	})

	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "docsync pr:")
}

func TestCOV_CLI_324_RunPrJSONPayloadLane(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	captured := captureIo()

	exit := cli.RunPr(cliArgs(t, "pr", "--base", "HEAD", "--json"), captured, cli.PrOptions{
		Cwd: repo.root,
		Env: prEnv(),
		Flow: func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
			return &cli.PrPayload{
				Applied:         []cli.FixFlowApplied{{DocPath: "docs/api.md", PatchRef: "state/docs/docs__api.md#patch-aaaaaaaaaaaaaaaa"}},
				FixSummaryLines: []string{"[ok] applied docs/api.md"},
				Ignored:         []cli.PrIssueRef{{DocPath: "docs/locked.md", Reason: "locked-or-protected", Detail: "d2"}},
				PrOpened:        true,
				PrNumber:        pr42(7),
				RecordedReplies: 1,
			}, nil
		},
	})

	assert.Equal(t, cli.ExitDrift, exit)
	var payload map[string]any
	require.NoError(t, jsonUnmarshalString(captured.Out(), &payload))
	assert.Equal(t, true, payload["ok"])
	assert.Equal(t, true, payload["pr_opened"])
	assert.Equal(t, float64(7), payload["pr_number"])
	assert.Equal(t, float64(1), payload["recorded_replies"])
	assert.Equal(t, []any{"[ok] applied docs/api.md"}, payload["fix_summary"])
	ignoredRows, ok := payload["ignored"].([]any)
	require.True(t, ok)
	require.Len(t, ignoredRows, 1)
	row, _ := ignoredRows[0].(map[string]any)
	assert.Equal(t, "docs/locked.md", row["docPath"])
	appliedFix, ok := payload["fix_outcome"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"docs/api.md"}, appliedFix["applied"])
}

func TestCOV_CLI_325_RunPrWithoutABoundFlowRunnerFailsClosed(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	captured := captureIo()

	previous := cli.PrFlowRunnerImpl
	cli.PrFlowRunnerImpl = nil
	t.Cleanup(func() { cli.PrFlowRunnerImpl = previous })

	exit := cli.RunPr(cliArgs(t, "pr", "--base", "HEAD"), captured, cli.PrOptions{Cwd: repo.root, Env: prEnv()})

	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Contains(t, captured.Err(), "the core doc-PR flow is not bound")
}

func TestCOV_CLI_326_RunPrNonAbsoluteCwdIsARuntimeComposeFailure(t *testing.T) {
	captured := captureIo()

	// A relative cwd cannot anchor a git client (D12: absolute roots only) →
	// the compose failure renders on the runtime lane (exit 3).
	exit := cli.RunPr(cliArgs(t, "pr", "--base", "HEAD"), captured, cli.PrOptions{Cwd: "relative/path"})

	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Contains(t, captured.Err(), "docsync pr:")
	assert.Contains(t, captured.Err(), "repoRoot")
}

func TestCOV_CLI_327_RunPrRendersAdvisoriesAsData(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	captured := captureIo()

	exit := cli.RunPr(cliArgs(t, "pr", "--repo", cliComposedRepo, "--base", "HEAD"), captured, cli.PrOptions{
		Cwd:       repo.root,
		Env:       map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-test-run-key-9f2c"},
		Transport: openrouter.CreateReplayTransport(nil).Transport,
		Flow: func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
			return &cli.PrPayload{PrOpened: true, PrNumber: pr42(4)}, nil
		},
	})

	assert.Equal(t, cli.ExitDrift, exit)
	lines := cliAdvisoryLines(captured.Err(), "docsync pr:")
	require.Len(t, lines, 2, "the org-policy floor renders as advisory data lines")
	for _, line := range lines {
		assert.Contains(t, line, cliAdvisoryMarker)
	}
	assert.Contains(t, captured.Out(), "Opened documentation PR #4")
}

func TestCOV_CLI_328_RunPrOriginHeadUnresolvablePassesTheEmptyPr(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	captured := captureIo()
	var seen *cli.PrFlowInput

	exit := cli.RunPr(cliArgs(t, "pr", "--base", "HEAD"), captured, cli.PrOptions{
		Cwd: repo.root,
		Env: prEnv(),
		Flow: func(_ context.Context, in *cli.PrFlowInput) (*cli.PrPayload, error) {
			seen = in
			return &cli.PrPayload{
				PrOpened:  true,
				PrNumber:  pr42(1),
				PrSkipped: "no proposed changes",
			}, nil
		},
	})

	require.NotNil(t, seen)
	assert.Nil(t, seen.SourcePr, "the local fixture has no pull request to resolve")
	assert.Equal(t, "", seen.ChangePr)
	assert.Equal(t, cli.ExitDrift, exit)
}

func TestCOV_CLI_329_RunPrRunErrorAndPushFailedRender(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	captured := captureIo()
	runError := "fix flow ended runtime-error"
	push := "rejected: non-fast-forward"

	exit := cli.RunPr(cliArgs(t, "pr", "--base", "HEAD"), captured, cli.PrOptions{
		Cwd: repo.root,
		Env: prEnv(),
		Flow: func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
			return &cli.PrPayload{
				Applied:    []cli.FixFlowApplied{{DocPath: "docs/api.md", PatchRef: "state/docs/docs__api.md#patch-bbbbbbbbbbbbbbbb"}},
				PrOpened:   true,
				RunError:   &runError,
				PushFailed: &push,
			}, nil
		},
	})

	assert.Equal(t, cli.ExitDrift, exit)
	text := captured.Out()
	assert.Contains(t, text, "Pipeline run error (P7-T13 message): "+runError)
	assert.Contains(t, text, "Local branch push failed (fix the credential / repo grants and re-run; local docs already patched): reason=rejected: non-fast-forward")
}

/* -------------------------------------------------------------------- RunAudit */

func TestCOV_CLI_340_RunAuditThreadsTriggerBranchRepoAndPostMerge(t *testing.T) {
	repo := repoWithManifest(t)
	captured := captureIo()
	var seen cli.AuditRunnerInput

	withAuditRunner(t, func(_ context.Context, input cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		seen = input
		return cli.AuditRunResult{Kind: cli.AuditRunNoop, Reason: "not-default-branch"}, nil
	})

	exit := cli.RunAudit(cliArgs(t, "audit", "--trigger", "post-merge", "--branch", "release/next",
		"--repo", "acme/widgets", "--concurrency", "3", "--commit-walk", "25", "--post-merge"),
		captured, cli.AuditOptions{Cwd: repo})

	assert.Equal(t, cli.ExitOK, exit)
	assert.Equal(t, "post-merge", seen.Trigger)
	assert.Equal(t, "release/next", seen.Branch)
	assert.Equal(t, "acme/widgets", seen.Repo)
	assert.True(t, seen.PostMerge)
	assert.Equal(t, 3, seen.Concurrency, "the explicit --concurrency threads into the runner input")
	assert.Equal(t, 25, seen.CommitWalk)
	assert.Contains(t, captured.Out(), "trigger not eligible")
}

func TestCOV_CLI_341_RunAuditJSONAbstainedLane(t *testing.T) {
	repo := repoWithManifest(t)
	captured := captureIo()
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunAbstained, Reason: "org-policy failed re-validation"}, nil
	})

	exit := cli.RunAudit(cliArgs(t, "audit", "--json"), captured, cli.AuditOptions{Cwd: repo})

	assert.Equal(t, cli.ExitRuntime, exit)
	assert.JSONEq(t, `{"kind":"abstained","detail":"org-policy failed re-validation"}`, captured.Out())
	assert.Contains(t, captured.Err(), "abstained (fail closed)")
}

func TestCOV_CLI_342_RunAuditVerboseIssueMarkdownLane(t *testing.T) {
	repo := repoWithManifest(t)
	captured := captureIo()
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{
			Kind: cli.AuditRunRan, Status: "completed", Trigger: "nightly", RunID: "nightly:acme/widgets",
			FindingCount: 2, AppendedCount: 1, IssueMarkdown: "## Drift findings\nFINDING BODY\n",
			OrgMarkdown: "ORG MARKETPLACE REPORT",
		}, nil
	})

	exit := cli.RunAudit(cliArgs(t, "audit", "--verbose"), captured, cli.AuditOptions{Cwd: repo})

	assert.Equal(t, cli.ExitDrift, exit)
	assert.Contains(t, captured.Out(), "FINDING BODY")
	assert.Contains(t, captured.Out(), "ORG MARKETPLACE REPORT")
}

func TestCOV_CLI_343_RunAuditDegradedRunWithoutDetailLane(t *testing.T) {
	repo := repoWithManifest(t)
	captured := captureIo()
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunRan, Status: "failed", RunID: "nightly:acme/widgets"}, nil
	})

	exit := cli.RunAudit(cliArgs(t, "audit"), captured, cli.AuditOptions{Cwd: repo})

	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Contains(t, captured.Err(), "no detail")
}

/* -------------------------------------------------------------------- RunCheck */

func covDriftOptions() cli.CheckOptions {
	return cli.CheckOptions{Cwd: ".", Env: map[string]string{}}
}

func TestCOV_CLI_350_RunCheckFallbackGlyphSet(t *testing.T) {
	captured := captureIo()

	exit := cli.RunCheck(cliArgs(t, "check", "--glyphs", "fallback", "--no-quiet", "--base", "HEAD"), captured,
		cli.CheckOptions{Cwd: ".", Runtime: &fakeRuntime{outcome: driftOutcome([]reportRow{
			{docPath: "docs/api.md", outcome: "suggest", confidence: 0.95},
		})}})

	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "[!] docs/api.md")
	assert.NotContains(t, captured.Out(), "⚠ docs/api.md")
}

func TestCOV_CLI_351_RunCheckVerboseStageLatencyRenders(t *testing.T) {
	outcome := driftOutcome([]reportRow{{docPath: "docs/api.md", outcome: "suggest", confidence: 0.95}})
	latency := 42
	for i := range outcome.Stages {
		outcome.Stages[i].LatencyMS = &latency
	}
	captured := captureIo()

	exit := cli.RunCheck(cliArgs(t, "check", "--verbose", "--no-quiet", "--base", "HEAD"), captured,
		cli.CheckOptions{Cwd: ".", Runtime: &fakeRuntime{outcome: outcome}})

	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "stage=analyze status=ok latency_ms=42")
}

func TestCOV_CLI_352_RunCheckRelativeCwdFailsClosedAtTheBaseResolution(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "check")

	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: "relative/path", Env: map[string]string{}})

	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "configuration failed validation")
}

func TestCOV_CLI_353_RunCheckReportFailureLaneFailsClosed(t *testing.T) {
	// The runtime produces a report the canonical schema rejects → the verb
	// fails closed (exit 3) and emits nothing on stdout (D14).
	broken := driftOutcome([]reportRow{{docPath: "docs/api.md", outcome: "suggest", confidence: 0.95}})
	broken.Documents[0].PolicyOutcome = "not-a-valid-outcome"
	captured := captureIo()

	exit := cli.RunCheck(cliArgs(t, "check", "--no-quiet", "--base", "HEAD"), captured,
		cli.CheckOptions{Cwd: ".", Runtime: &fakeRuntime{outcome: broken}})

	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Empty(t, captured.Out())
	assert.Contains(t, captured.Err(), "canonical report failed schema validation")
}

func TestCOV_CLI_354_RunCheckRunErrorConfigClassLane(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "check", "--base", "HEAD")

	exit := cli.RunCheck(args, captured, cli.CheckOptions{
		Cwd:     ".",
		Runtime: &fakeRuntime{err: orchestrator.NewPipelineConfigError("no policy version pinned", "", nil)},
	})

	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "no policy version pinned")
}

/* --------------------------------------------------------- composed model lanes */

func TestCOV_CLI_360_CheckComposedRejectedJudgeVerdictLane(t *testing.T) {
	repo, base := cliDriftFixture(t)
	captured := captureIo()

	exit := cliComposedCheck(t, cliComposedArgs(t, "--base", base, "--no-quiet"), repo, captured, "reject",
		"sk-or-v1-test-run-key-9f2c")

	// A rejected verdict routes both candidates to blocked/review — the policy
	// stage resolves the pair, and the run still renders its rows.
	assert.Equal(t, cli.ExitDrift, exit)
	assert.Contains(t, captured.Out(), "docs/api.md")
	assert.Contains(t, captured.Out(), "Documentation consistency")
}

func TestCOV_CLI_361_CheckComposedDecisionContractViolationLane(t *testing.T) {
	repo, base := cliDriftFixture(t)
	captured := captureIo()

	// The decision adapter receives a body that fails DECISION_SCHEMA → the
	// untrusted edge is refused (the run degrades, never a guessed route).
	exit := cli.RunCheck(cliComposedArgs(t, "--base", base, "--no-quiet"), captured, cli.CheckOptions{
		Cwd:     repo.root,
		Env:     map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-test-run-key-9f2c"},
		Budgets: cliSequentialBudgets(),
		Transport: openrouter.CreateReplayTransport([]openrouter.ReplayFixture{
			cliBareCompletion(cliComposedDecisionModel, map[string]any{
				"change_kind": "behavioral", "action": "frobnicate", "abstain": false,
			}),
		}).Transport,
	})

	// The run completes HONESTLY degraded: the decision is refused as a schema
	// violation (abstain, don't guess), the run carries a run-scoped abstain,
	// NO document rows are fabricated, and the outage posture exits 0.
	assert.Equal(t, cli.ExitOK, exit)
	text := captured.Out()
	assert.Contains(t, text, "Documentation consistency")
	assert.Contains(t, text, "schema_violation")
	assert.NotContains(t, text, "docs/api.md")
}

func TestCOV_CLI_362_CheckComposedFailingTransportDegrades(t *testing.T) {
	repo, base := cliDriftFixture(t)
	captured := captureIo()

	exit := cli.RunCheck(cliComposedArgs(t, "--base", base), captured, cli.CheckOptions{
		Cwd:     repo.root,
		Env:     map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-test-run-key-9f2c"},
		Budgets: cliSequentialBudgets(),
		Transport: openrouter.CreateFailingTransport([]openrouter.TransportFailure{{
			Kind: openrouter.TransportFailureNetwork, Message: "transport refused the exchange",
		}}).Transport,
	})

	// A transport failure is a DECLINED run — the JEV port failed, the run
	// records the run-scoped abstain, no document rows are fabricated, and the
	// outage posture exits 0 (honest degradation, never a fabricated clean).
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "Documentation consistency")
}

func TestCOV_CLI_363_FixComposedSameRunIdSkipsBeforeAnyStage(t *testing.T) {
	// The run-id CAS lane: a fresh fix after a recorded run id skips BEFORE any
	// model stage (the failing transport proves the pipeline never runs).
	repo, base := cliFixComposedRepo(t)
	runID := genULID(t)
	first := captureIo()
	exit1 := cli.RunFix(cliFixComposedArgs(t, "--base", base, "--run-id", runID), first, cli.FixOptions{
		Cwd:       repo.root,
		Env:       map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateReplayTransport(cliDriftReplay("pass")).Transport,
		Budgets:   cliSequentialBudgets(),
	})
	require.Equal(t, cli.ExitDrift, exit1, "run 1 applies the verified patch")

	second := captureIo()
	exit2 := cli.RunFix(cliFixComposedArgs(t, "--base", base, "--run-id", runID), second, cli.FixOptions{
		Cwd: repo.root,
		Env: map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateFailingTransport([]openrouter.TransportFailure{{
			Kind: openrouter.TransportFailureNetwork, Message: "pipeline must not run",
		}}).Transport,
		Budgets: cliSequentialBudgets(),
	})

	assert.Equal(t, cli.ExitOK, exit2)
	assert.Contains(t, second.Out(), "no-op: run-id "+runID+" already recorded")
}

/* ------------------------------------------------------------------ helpers */

var _ = schemas.RunOutcomeSchemaID

/* ------------------------------------------------------------ RunCheck --base lane */

func TestCOV_CLI_370_RunCheckExplicitBaseSkipsResolution(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCheck(cliArgs(t, "check", "--base", "HEAD"), captured, cli.CheckOptions{
		Cwd:     ".",
		Runtime: &fakeRuntime{outcome: driftOutcome([]reportRow{})},
	})
	assert.Equal(t, cli.ExitOK, exit)
}

/* ------------------------------------------------------------ RunFix offline refusal */

func TestCOV_CLI_371_RunFixOfflineRefused(t *testing.T) {
	captured := captureIo()
	exit := cli.RunFix(cliArgs(t, "fix", "--offline"), captured, cli.FixOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "offline is not supported for fix")
}

/* ------------------------------------------------------------ RunFix invalid run-id */

func TestCOV_CLI_372_RunFixInvalidRunID(t *testing.T) {
	captured := captureIo()
	exit := cli.RunFix(cliArgs(t, "fix", "--run-id", "not-a-ulid"), captured, cli.FixOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "invalid --run-id")
}

/* ------------------------------------------------------------ RunFix FlowDeps injection */

func TestCOV_CLI_373_RunFixWithFlowDepsNoop(t *testing.T) {
	repo, _ := cliDriftFixture(t)
	stateDir := repo.root + "/.docsync/state"
	store := cli.NewFixRunRecordStore(stateDir)
	runID := genULID(t)
	require.NoError(t, store.Write(cli.FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: runID}))
	captured := captureIo()
	exit := cli.RunFix(cliArgs(t, "fix", "--base", "HEAD", "--run-id", runID), captured, cli.FixOptions{
		Cwd: repo.root,
		Env: map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-test-run-key"},
		FlowDeps: &cli.FixFlowDeps{
			Store: store,
			Clock: func() string { return "2026-01-01T00:00:00Z" },
		},
	})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "no-op: run-id "+runID+" already recorded")
}

/* ------------------------------------------------------------ RunPr offline refusal */

func TestCOV_CLI_374_RunPrOfflineRefused(t *testing.T) {
	captured := captureIo()
	exit := cli.RunPr(cliArgs(t, "pr", "--offline"), captured, cli.PrOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "offline is not supported for pr")
}

/* ------------------------------------------------------------ RunAudit trigger event mapping */

func TestCOV_CLI_375_RunAuditNonMappedTriggerDefaultsToEmpty(t *testing.T) {
	repo := repoWithManifest(t)
	captured := captureIo()
	withAuditRunner(t, func(_ context.Context, _ cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunNoop, Reason: "not-eligible"}, nil
	})
	exit := cli.RunAudit(cliArgs(t, "audit", "--trigger", "nightly"), captured, cli.AuditOptions{Cwd: repo})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "trigger not eligible")
}

/* ------------------------------------------------------------ fixGlyphSet */
