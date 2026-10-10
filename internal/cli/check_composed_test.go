package cli_test

// Ported from packages/cli/test/check-composed.test.ts (V-P7-1; P7-T01/T02,
// wave-2 unit a) — `docsync check` end-to-end over the REAL composed stage
// adapters (decision chain, patch-writer, judge, policy engine) on recorded
// replay doubles (OD-2 — no network): the shared renderer spec (⚠ suggest
// row, 🔒 locked row, canonical header) and the --json surface.
//
// Replay-order honesty (the AGENT-behavior replay rule): the Go replay
// transport dispatches keyed fixtures by request identity and bare-response
// fixtures positionally. This suite uses BARE responses (the TS createReplay
// transport was positional FIFO) with the runner's writer pool capped at
// maxConcurrentDocs: 1 (writer calls run sequentially in candidate order)
// and a sequential verification walk — so the FIFO script is consumed in a
// deterministic order, exactly as the TS suite documented.
import (
	"encoding/json"

	"regexp"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cliComposedRepo          = "acme/cli-fixture"
	cliComposedDecisionModel = "z-ai/glm-4.7"
	cliComposedWriterModel   = "anthropic/claude-sonnet-4.5"
	cliComposedJudgeModel    = "openai/gpt-4.1"
	cliComposedApiInsert     = "The refund call defaults to `amountMinor` minor units."
	cliComposedLockedInsert  = "The refund ledger defaults to `amountMinor` minor units."
	cliComposedApiLine3      = "The refund call captures the payment."
	cliComposedLockedLine3   = "The refund ledger is append-only."
)

// cliSequentialBudgets is the TS SEQUENTIAL_POOL ({maxConcurrentDocs: 1}).
func cliSequentialBudgets() *orchestrator.PipelineBudgetOverrides {
	one := 1
	return &orchestrator.PipelineBudgetOverrides{MaxConcurrentDocs: &one}
}

// cliBareCompletion builds a bare (keyless) replay response for a model.
func cliBareCompletion(model string, value any) openrouter.ReplayFixture {
	m := model
	return openrouter.ResponseFixture(openrouter.JSONCompletion(&m, value))
}

// cliComposedDecisionBody is the recorded CONTINUE proposal (documentation_
// impact 'definite' — both manifest docs are candidates).
func cliComposedDecisionBody() map[string]any {
	return map[string]any{
		"change_kind":          "behavioral",
		"documentation_impact": "definite",
		"risk":                 "low",
		"confidence":           0.95,
		"answers": map[string]any{
			"public_behavior_changed": map[string]any{"value": true, "probability": 0.95},
			"api_contract_changed":    map[string]any{"value": false, "probability": 0.9},
			"affected_doc_count":      map[string]any{"value": 2, "probability": 0.95},
			"documentation_impact":    map[string]any{"value": "definite", "probability": 0.95},
		},
		"candidates":     []string{"docs/api.md", "docs/locked.md"},
		"action":         "patch",
		"abstain":        false,
		"abstain_reason": nil,
		"evidence": []map[string]any{{
			"kind": "symbol_diff", "ref": "src/payments/refund.ts:3", "note": "idempotencyKey became optional",
		}},
	}
}

// cliComposedWriterBody is the recorded writer PROPOSAL body: one anchored
// line-3 replacement (the adapter's identity-fill derives the anchor hash).
func cliComposedWriterBody(docPath, line3, insert string) map[string]any {
	return map[string]any{
		"doc_path": docPath,
		"hunks": []map[string]any{{
			"anchor": map[string]any{
				"start_line":   3,
				"end_line":     3,
				"content_hash": claims.HashSpan(line3),
			},
			"delete_text": line3,
			"insert_text": insert,
			"evidence_refs": []map[string]any{{
				"kind": "symbol_diff", "ref": "src/payments/refund.ts:3", "note": "idempotencyKey became optional",
			}},
		}},
		"evidence_refs": []map[string]any{},
		"minimality":    map[string]any{"measured": false, "added_lines": 1, "removed_lines": 1},
	}
}

func cliComposedJudgeBody(verdict string) map[string]any {
	return map[string]any{
		"verdict": verdict,
		"reasons": []string{"all tiers green; the drift is resolved by the patch"},
		"questions": map[string]any{
			"stale_before":              map[string]any{"value": "true", "probability": 0.97},
			"resolved_after":            map[string]any{"value": "true", "probability": 0.94},
			"minimal":                   map[string]any{"value": "true", "probability": 0.98},
			"introduced_new_inaccuracy": map[string]any{"value": "false", "probability": 0.96},
		},
	}
}

// cliDriftReplay is the full replay script for one composed drift run:
// decision → writer(api) → writer(locked) → judge → judge.
func cliDriftReplay(judgeVerdict string) []openrouter.ReplayFixture {
	return []openrouter.ReplayFixture{
		cliBareCompletion(cliComposedDecisionModel, cliComposedDecisionBody()),
		cliBareCompletion(cliComposedWriterModel, cliComposedWriterBody("docs/api.md", cliComposedApiLine3, cliComposedApiInsert)),
		cliBareCompletion(cliComposedWriterModel, cliComposedWriterBody("docs/locked.md", cliComposedLockedLine3, cliComposedLockedInsert)),
		cliBareCompletion(cliComposedJudgeModel, cliComposedJudgeBody(judgeVerdict)),
		cliBareCompletion(cliComposedJudgeModel, cliComposedJudgeBody(judgeVerdict)),
	}
}

// cliComposedModels binds the Phase-0 roles plus the `patch-writer` adapter
// role (the Go writer adapter resolves its binding under `patch-writer`).
const cliComposedModels = `schema: docsync.models/v1
version: 1
roles:
  decision-engine:
    model: z-ai/glm-4.7
  patch-writer:
    model: anthropic/claude-sonnet-4.5
  writer:
    model: anthropic/claude-sonnet-4.5
  judge:
    model: openai/gpt-4.1
`

// cliComposedApiDoc / cliComposedLockedDoc are the TS-shaped doc bodies (the
// line-3 anchor convention; long enough that a single-line replacement is not
// measured as a whole-document rewrite — Rule B counts non-empty lines).
const cliComposedApiDoc = "# API reference\n\nThe refund call captures the payment.\n\nSee the payments module for details.\n"
const cliComposedLockedDoc = "# Operations runbook (locked)\n\nThe refund ledger is append-only.\n\nOwned by the platform team.\n"

// cliDriftFixture commits a behavioral source change over the fixture base
// (TS parity: `idempotencyKey` becomes optional; the source carries the
// `amountMinor` token the patch inserts anchor to).
func cliDriftFixture(t *testing.T) (*gitFixture, string) {
	t.Helper()
	repo := newGitFixture(t)
	repo.seedBase()
	repo.write(".docsync/models.yml", cliComposedModels)
	repo.write("docs/api.md", cliComposedApiDoc)
	repo.write("docs/locked.md", cliComposedLockedDoc)
	repo.commitPaths([]string{".docsync/models.yml", "docs/api.md", "docs/locked.md"},
		"chore: composed model bindings and doc bodies")
	repo.write("src/payments/refund.ts",
		"export interface RefundInput {\n  amountMinor: number;\n  idempotencyKey?: string;\n}\n\n"+
			"export function refund(input: RefundInput): void {\n  void input;\n}\n")
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	repo.commitPaths([]string{"src/payments/refund.ts"}, "feat: refund idempotency key optional")
	return repo, base
}

func cliComposedArgs(t *testing.T, extra ...string) *cli.ParsedInvocation {
	t.Helper()
	argv := append([]string{"check", "--no-quiet", "--repo", cliComposedRepo}, extra...)
	return cliArgs(t, argv...)
}

func cliComposedCheck(t *testing.T, args *cli.ParsedInvocation, repo *gitFixture, captured *capturedIo, judgeVerdict, apiKey string) cli.DocsyncExitCode {
	t.Helper()
	return cli.RunCheck(args, captured, cli.CheckOptions{
		Cwd:       repo.root,
		Env:       map[string]string{"OPENROUTER_API_KEY": apiKey},
		Transport: openrouter.CreateReplayTransport(cliDriftReplay(judgeVerdict)).Transport,
		Budgets:   cliSequentialBudgets(),
	})
}

func TestCheckComposedRedactionPlantedKeyNeverReachesOutput(t *testing.T) {
	repo, base := cliDriftFixture(t)
	captured := captureIo()

	_ = cliComposedCheck(t, cliComposedArgs(t, "--json", "--base", base), repo, captured, "pass", plantedKey)

	everything := captured.Out() + "\n" + captured.Err()
	assert.NotContains(t, everything, plantedKey)
	assert.NotContains(t, everything, "sk-or-v1")
}

func TestCheckComposedTextModeRendersSuggestAndLockedRows(t *testing.T) {
	repo, base := cliDriftFixture(t)
	captured := captureIo()

	exit := cliComposedCheck(t, cliComposedArgs(t, "--base", base), repo, captured, "pass",
		"sk-or-v1-test-run-key-9f2c")

	// The composed walk produced advisory + blocking drift rows.
	text := captured.Out()
	assert.Contains(t, text, "⚠ docs/api.md")
	assert.Contains(t, text, "🔒 docs/locked.md")
	assert.Contains(t, text, "locked (surfaced, never touched)")
	// The canonical header pins run id + status + policy version.
	assert.Regexp(t, regexpMustCompile(`(?m)^Documentation consistency\n\nrun acme/cli-fixture#`), text)
	assert.Contains(t, text, "· status ")
	// No unknown-coverage section: the changed source is mapped by the manifest.
	assert.NotContains(t, text, "Unknown coverage")
	// The exit is drift regardless of strict (a locked row is blocking, D2).
	assert.Equal(t, cli.ExitDrift, exit)
}

func TestCheckComposedJSONEmitsSchemaValidCanonicalReport(t *testing.T) {
	repo, base := cliDriftFixture(t)
	captured := captureIo()

	exit := cliComposedCheck(t, cliComposedArgs(t, "--json", "--base", base), repo, captured, "pass",
		"sk-or-v1-test-run-key-9f2c")

	assert.Equal(t, cli.ExitDrift, exit)
	require.Len(t, captured.stdoutLines(), 1)
	// The emitted payload re-parses through the registered report schema.
	var report schemas.CanonicalReport
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &report))
	assert.Empty(t, report.Validate())
	require.Len(t, report.Rows, 2)
	assert.Equal(t, "docs/api.md", report.Rows[0].DocPath)
	assert.Equal(t, schemas.PipelineOutcomeSuggest, report.Rows[0].Outcome)
	assert.Equal(t, "docs/locked.md", report.Rows[1].DocPath)
	assert.Equal(t, schemas.PipelineOutcomeBlocked, report.Rows[1].Outcome)
	// The D3 confidence display is pinned to policy version + routed model.
	require.NotNil(t, report.Rows[0].ConfidenceDisplay)
	assert.Contains(t, *report.Rows[0].ConfidenceDisplay, "policy v1")
}

func TestCheckComposedChainConsumedExactlyTheScriptedExchanges(t *testing.T) {
	repo, base := cliDriftFixture(t)
	captured := captureIo()

	// 5 scripted responses; any extra model call would exhaust the replay and
	// degrade the run instead of producing the expected rows (fail loud).
	exit := cliComposedCheck(t, cliComposedArgs(t, "--json", "--base", base), repo, captured, "pass",
		"sk-or-v1-test-run-key-9f2c")

	assert.Equal(t, cli.ExitDrift, exit)
	var report schemas.CanonicalReport
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &report))
	// The judge verdicts were consumed: the locked row resolves blocked (D2).
	lockedRow := cliFindRow(report.Rows, "docs/locked.md")
	require.NotNil(t, lockedRow)
	assert.Equal(t, schemas.PipelineOutcomeBlocked, lockedRow.Outcome)
	// A degraded/abstained run would carry a model_unavailable abstain.
	for _, abstain := range report.Abstains {
		assert.NotEqual(t, string(schemas.AbstainReasonModelUnavailable), abstain.Reason)
	}
}

func TestCheckComposedDecisionTelemetryCarriesTheRoutedModel(t *testing.T) {
	repo, base := cliDriftFixture(t)
	captured := captureIo()

	_ = cliComposedCheck(t, cliComposedArgs(t, "--json", "--base", base), repo, captured, "pass",
		"sk-or-v1-test-run-key-9f2c")

	var report schemas.CanonicalReport
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &report))
	var decisionBinding *schemas.RunModelBinding
	for i := range report.ModelBindings {
		if report.ModelBindings[i].Role == "decision-engine" {
			decisionBinding = &report.ModelBindings[i]
		}
	}
	// D3, provenance truthfulness: the actually-routed model, not a default.
	require.NotNil(t, decisionBinding, "the decision-engine binding must ride the run provenance")
	assert.Equal(t, cliComposedDecisionModel, decisionBinding.Model)
}

func cliFindRow(rows []schemas.CanonicalReportRow, docPath string) *schemas.CanonicalReportRow {
	for i := range rows {
		if rows[i].DocPath == docPath {
			return &rows[i]
		}
	}
	return nil
}

// regexpMustCompile is a local alias to keep the import list minimal.
func regexpMustCompile(pattern string) *regexp.Regexp { return regexp.MustCompile(pattern) }
