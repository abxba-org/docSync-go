package cli_test

// Ported from packages/cli/test/fix-output.test.ts (P7-T08..T13, wave-3 unit b)
// — the fix output surfaces: `--json` emits
// {report, applied, skipped, refused, run_id} with the report re-pinned before
// emit; same-run-skip `--json` emits the honest null-report shape; text mode
// renders the canonical report plus the fix summary lines; redaction scan.
//
// DEVIATION NOTE: the TS `--verbose` lane renders the pipeline stage lines from
// the COMPOSED outcome (the flow result carries the run's stages). The Go
// FixFlowResult carries no stage records and RunFix's render has no verbose
// lane — verbose is a no-op on the fix surface. The BEHAVIOR available in Go
// is pinned here (no `stage=` lines, the report still renders); the composed
// stage-line rendering itself is pinned for check in check_verbose_test.go.
import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliFixOutputOutcome is the composed drift run the TS replay script produces:
// api.md verified-suggest, locked.md blocked (D2 floor).
func cliFixOutputOutcome() *schemas.RunOutcome {
	api := cliVerifiedDoc("docs/api.md", schemas.PipelineOutcomeSuggest)
	locked := cliVerifiedDoc("docs/locked.md", schemas.PipelineOutcomeBlocked)
	return cliFlowOutcome([]schemas.RunDocument{api, locked})
}

func cliFixOutputDeps(t *testing.T, repoRoot string) cli.FixFlowDeps {
	t.Helper()
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repoRoot})
	require.NoError(t, err)
	return cli.FixFlowDeps{
		Runtime: &fakeRuntime{outcome: cliFixOutputOutcome()},
		Git:     git,
		Store:   cli.NewFixRunRecordStore(filepath.Join(repoRoot, ".docsync", "state")),
		Clock:   func() string { return cliFlowNow },
		Apply: cliApplyFn(cli.FixApplyOutcome{
			Kind:          "applied",
			ResultContent: "# API reference\n\nThe refund call captures and voids the payment.\n",
		}),
		PatchSource: func(_ context.Context, docPath string, _ *string) (*schemas.DocPatch, error) {
			content := "# API reference\n\nThe refund call captures the payment.\n"
			if docPath == "docs/locked.md" {
				content = "# Operations runbook (locked)\n\nThe refund ledger is append-only.\n"
			}
			return cliMakePatch(docPath, content, false), nil
		},
	}
}

func TestFixOutputJSONEmitsPayloadWithSchemaValidReport(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	runID := genULID(t)
	captured := captureIo()
	exit := cli.RunFix(cliArgs(t, "fix", "--json", "--no-quiet", "--repo", cliFlowRepo,
		"--base", "main", "--run-id", runID), captured, cli.FixOptions{
		Cwd:      repo.root,
		FlowDeps: cliFixOutputDepsPtr(t, repo.root),
	})

	assert.Equal(t, cli.ExitDrift, exit)
	require.Len(t, captured.stdoutLines(), 1)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &payload))
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	assert.ElementsMatch(t, []string{"report", "applied", "skipped", "refused", "run_id"}, keys)
	assert.Equal(t, runID, payload["run_id"])

	// The report re-pins its schema before emit (D14) — it validates.
	report, ok := payload["report"].(map[string]any)
	require.True(t, ok, "report must be an object")
	assert.Equal(t, "run_01J9ZK6X8A", report["runId"])
	assert.Equal(t, "completed", report["status"])
	assert.Equal(t, float64(1), report["policyVersion"])
	rows, ok := report["rows"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 2)
	first, _ := rows[0].(map[string]any)
	second, _ := rows[1].(map[string]any)
	assert.Equal(t, "docs/api.md", first["docPath"])
	assert.Equal(t, "suggest", first["outcome"])
	assert.Equal(t, "docs/locked.md", second["docPath"])
	assert.Equal(t, "blocked", second["outcome"])

	applied, ok := payload["applied"].([]any)
	require.True(t, ok)
	require.Len(t, applied, 1)
	appliedRow, _ := applied[0].(map[string]any)
	assert.Equal(t, "docs/api.md", appliedRow["docPath"])
	assert.Equal(t, false, appliedRow["threeWayUsed"])
	assert.Regexp(t, regexp.MustCompile(`^state/docs/.+#patch-[0-9a-f]{16}$`), appliedRow["patchRef"].(string))

	skipped, ok := payload["skipped"].([]any)
	require.True(t, ok)
	require.Len(t, skipped, 1)
	skippedRow, _ := skipped[0].(map[string]any)
	assert.Equal(t, "docs/locked.md", skippedRow["docPath"])
	assert.Equal(t, "locked-or-protected", skippedRow["reason"])

	assert.Equal(t, []any{}, payload["refused"])
}

func TestFixOutputSameRunSkipJSONHonestNullReport(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	runID := genULID(t)
	store := cli.NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state"))
	require.NoError(t, store.Write(cli.FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: runID}))

	captured := captureIo()
	exit := cli.RunFix(cliArgs(t, "fix", "--json", "--repo", cliFlowRepo, "--run-id", runID), captured,
		cli.FixOptions{Cwd: repo.root, FlowDeps: cliFixOutputDepsPtr(t, repo.root)})

	assert.Equal(t, cli.ExitOK, exit)
	require.Len(t, captured.stdoutLines(), 1)
	assert.JSONEq(t, `{"report":null,"applied":[],"skipped":[],"refused":[],"run_id":"`+runID+`"}`,
		captured.stdoutLines()[0])
}

func TestFixOutputVerboseRendersNoStageLinesOnTheFixSurface(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	exit := cli.RunFix(cliArgs(t, "fix", "--verbose", "--no-quiet", "--repo", cliFlowRepo, "--base", "main"),
		captured, cli.FixOptions{Cwd: repo.root, FlowDeps: cliFixOutputDepsPtr(t, repo.root)})

	assert.Equal(t, cli.ExitDrift, exit)
	for _, line := range captured.stdoutLines() {
		assert.False(t, strings.HasPrefix(line, "stage="),
			"the Go fix surface has no stage-line lane (deviation noted in the file header)")
	}
	// The report itself still renders.
	assert.Contains(t, captured.Out(), "Documentation consistency")
}

func TestFixOutputTextRendersReportPlusSummaryLines(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	exit := cli.RunFix(cliArgs(t, "fix", "--no-quiet", "--repo", cliFlowRepo, "--base", "main"),
		captured, cli.FixOptions{Cwd: repo.root, FlowDeps: cliFixOutputDepsPtr(t, repo.root)})

	assert.Equal(t, cli.ExitDrift, exit)
	text := captured.Out()
	// The canonical report header pins run id + status (shared renderer).
	assert.Regexp(t, regexp.MustCompile(`(?m)^Documentation consistency\n\nrun \S+ · status completed · policy v1`), text)
	// Fix summary lines (§12.1 visual language).
	assert.Contains(t, text, "✓ applied docs/api.md (state/docs/")
	assert.Contains(t, text, "⏭ skipped docs/locked.md (locked-or-protected — ")
	// No refused lines in this run.
	assert.NotContains(t, text, "⚠ refused")
}

func TestFixOutputRedactionPlantedKeyNeverReachesOutput(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	// The Go fix deps carry NO credential surface at all; the planted key
	// exists nowhere in the run — scan every captured line anyway (§3 rule 8).
	_ = cli.RunFix(cliArgs(t, "fix", "--no-quiet", "--repo", cliFlowRepo, "--base", "main"),
		captured, cli.FixOptions{Cwd: repo.root, FlowDeps: cliFixOutputDepsPtr(t, repo.root)})

	everything := captured.Out() + "\n" + captured.Err()
	assert.NotContains(t, everything, plantedKey)
	assert.NotContains(t, everything, "sk-or-v1")
}

/* ------------------------------------------------------------- helpers */

// cliFixOutputDepsPtr builds the injected flow deps (pointer for FixOptions).
func cliFixOutputDepsPtr(t *testing.T, repoRoot string) *cli.FixFlowDeps {
	t.Helper()
	deps := cliFixOutputDeps(t, repoRoot)
	return &deps
}

// stdoutLines splits the captured stdout into lines (the TS captureIo array).
func (c *capturedIo) stdoutLines() []string {
	out := strings.TrimSuffix(c.Out(), "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
