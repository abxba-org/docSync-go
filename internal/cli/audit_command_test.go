package cli_test

// Ported from packages/cli/test/audit-command-p12-t16.test.ts — the
// `docsync audit` verb (P12-T16; ARCH §7.2 Loop B): the flag surface, the
// fail-closed cost-knob/branch/repo validation lanes, the exit mapping over
// the injected engine seam, and the `--org-report` render surface.
//
// DEVIATION NOTES (the TS suite drives the REAL loop-B engine; the Go engine
// is a DECLARED SEAM — AuditRunnerImpl — rebound by the orchestrator, so the
// engine-owned lanes are pinned through the seam):
//   - the TS trigger verdict name 'nightly-scheduled' is engine vocabulary;
//     the Go verb carries the trigger CONTEXT ('nightly'/'post-merge') and
//     renders it — the seam input is pinned instead;
//   - the advisory state files under .docsync/state (audit-runs.jsonl,
//     audit-findings.jsonl) are written by the ENGINE (internal/audit), never
//     by the verb — that lane is not portable at this seam;
//   - `--concurrency` clamps at the ARGS layer in Go (ceiling 64) where the TS
//     clamped inside the verb; `--commit-walk` clamps in the verb (ceiling
//     1000) exactly as the TS did.
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditVerbExistsInSurfaceAndUsageText(t *testing.T) {
	parsed := cli.ParseCliArgsWithEnv([]string{"audit"}, map[string]string{})
	assert.Equal(t, cli.VerbAudit, parsed.Verb)
	assert.Empty(t, parsed.Error)
	assert.Contains(t, cli.UsageText(), "audit")
}

func TestAuditCleanFixtureRendersAdvisoryNoopWithoutDocWrites(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	beforeAPI := cliReadDoc(t, repo.root, "docs/api.md")
	beforeLocked := cliReadDoc(t, repo.root, "docs/locked.md")
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{
			Kind: cli.AuditRunRan, Status: "completed", FindingCount: 0,
			RunID: "run_audit_test_01", Trigger: "nightly",
		}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit"), captured, cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitOK, exit)
	assert.Empty(t, captured.Err())
	// The run id + advisory posture are reported.
	assert.Contains(t, captured.Out(), "run_audit_test_01")
	assert.Contains(t, captured.Out(), "advisory only, no writes")
	// NO document was written: the fixture docs stay byte-identical.
	assert.Equal(t, beforeAPI, cliReadDoc(t, repo.root, "docs/api.md"))
	assert.Equal(t, beforeLocked, cliReadDoc(t, repo.root, "docs/locked.md"))
}

func TestAuditJSONEmitsMachineReadableAdvisoryRecord(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{
			Kind: cli.AuditRunRan, Status: "completed", FindingCount: 0,
			RunID: "run_audit_test_02", Trigger: "nightly",
		}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit", "--json"), captured, cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitOK, exit)
	require.Len(t, captured.stdoutLines(), 1)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &payload))
	assert.Equal(t, true, payload["advisory"])
	assert.Equal(t, "nightly", payload["trigger"])
	assert.Equal(t, "run_audit_test_02", payload["runId"])
	assert.Equal(t, float64(0), payload["findings"])
	assert.Equal(t, "completed", payload["status"])
	// No org object is attached unless --org-report was requested.
	_, hasOrgReport := payload["orgReport"]
	assert.False(t, hasOrgReport)
}

func TestAuditPRTriggerContextIsStructurallyRefused(t *testing.T) {
	// Loop B is forbidden on the PR critical path (V-P11-8; ARCH §7.2): the
	// CLI's --trigger accepts ONLY the two non-PR contexts.
	parsed := cli.ParseCliArgsWithEnv([]string{"audit", "--trigger", "pr"}, map[string]string{})
	require.Error(t, errFromString(parsed.Error))
	assert.Contains(t, parsed.Error, "unknown --trigger value")
	assert.Contains(t, parsed.Error, "structurally refused")
}

func TestAuditTriggerUnknownValueIsUsageErrorExit2(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"audit", "--trigger", "weekly"}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "unknown --trigger value")
}

func TestAuditCostKnobsRejectNonPositiveIntegers(t *testing.T) {
	badConcurrency := cli.ParseCliArgsWithEnv([]string{"audit", "--concurrency", "0"}, map[string]string{})
	assert.Contains(t, badConcurrency.Error, "invalid --concurrency")
	badWalk := cli.ParseCliArgsWithEnv([]string{"audit", "--commit-walk", "x"}, map[string]string{})
	assert.Contains(t, badWalk.Error, "invalid --commit-walk")
	assert.Empty(t, cli.ParseCliArgsWithEnv([]string{"audit", "--concurrency", "8"}, map[string]string{}).Error)
	assert.Empty(t, cli.ParseCliArgsWithEnv([]string{"audit", "--commit-walk", "25"}, map[string]string{}).Error)
}

func TestAuditInvalidManifestIsExit2BeforeAnythingRuns(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	// min(1) violation: a hard manifest defect (D8).
	repo.write(".docsync/manifest.yml", "version: 1\ndocuments: []\n")
	runnerCalls := 0
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		runnerCalls++
		return cli.AuditRunResult{Kind: cli.AuditRunNoop}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit"), captured, cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "docsync audit:")
	assert.Equal(t, 0, runnerCalls, "the engine must never run on a manifest defect")
}

func TestAuditPostMergeContextAcceptedWhenOptedIn(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	var seen cli.AuditRunnerInput
	withAuditRunner(t, func(_ context.Context, input cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		seen = input
		return cli.AuditRunResult{
			Kind: cli.AuditRunRan, Status: "completed", FindingCount: 0,
			RunID: "run_audit_test_03", Trigger: "post-merge",
		}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit", "--trigger", "post-merge", "--post-merge"), captured,
		cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitOK, exit)
	assert.True(t, seen.PostMerge)
	assert.Equal(t, "post-merge", seen.Trigger)
	assert.Contains(t, captured.Out(), "post-merge")
}

func TestAuditOrgReportRendersTheOrgShapedReport(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{
			Kind: cli.AuditRunRan, Status: "completed", FindingCount: 0,
			RunID: "run_audit_test_04", Trigger: "nightly",
			OrgMarkdown: "ADVISORY — Org-wide drift report — acme\n\nacme/widgets: 0 finding(s)\n",
		}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit", "--org-report", "--repo", "acme/widgets"), captured,
		cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitOK, exit)
	text := captured.Out()
	assert.Contains(t, text, "ADVISORY")
	assert.Contains(t, text, "Org-wide drift report — acme")
	assert.Contains(t, text, "acme/widgets")
}

func TestAuditOrgReportJSONIsOneLineCarryingTheOrgReportObject(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	orgMarkdown := "ADVISORY — Org-wide drift report — acme\nacme/widgets: 0 finding(s)\n"
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{
			Kind: cli.AuditRunRan, Status: "completed", FindingCount: 0,
			RunID: "run_audit_org_p12", Trigger: "nightly", OrgMarkdown: orgMarkdown,
		}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit", "--org-report", "--json", "--repo", "acme/widgets"), captured,
		cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitOK, exit)
	require.Len(t, captured.stdoutLines(), 1)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &payload))
	assert.Equal(t, orgMarkdown, payload["orgReport"])
}

func TestAuditAbstainedRunNamesTheStatusOnStderr(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	// F1-1: a fail-closed (abstained) audit exits 3 and names the status on
	// stderr — never a silent exit 0 derived from a zero findings count.
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunAbstained, Reason: "claim-store corpus is empty — failing closed (read-mostly invariant, V-P11-8)"}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit"), captured, cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Contains(t, captured.Err(), "docsync audit:")
	assert.Contains(t, captured.Err(), "abstained")
}

func TestAuditBlankBranchIsUsageErrorExit2(t *testing.T) {
	// F1-4: `--branch ""` is a usage error → exit 2 (never a silent main
	// fallback).
	parsed := cli.ParseCliArgsWithEnv([]string{"audit", "--branch", ""}, map[string]string{})
	require.Error(t, errFromString(parsed.Error))
	assert.Contains(t, parsed.Error, "--branch")

	captured := captureIo()
	exit := cli.RunCli([]string{"audit", "--branch", ""}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "--branch")
}

func TestAuditConcurrencyRejectsUnsafeIntegersAndClampsToTheCeiling(t *testing.T) {
	// F1-4: a value beyond the safe-integer range fails validation.
	unsafe := cli.ParseCliArgsWithEnv([]string{"audit", "--concurrency", "999999999999999999999"}, map[string]string{})
	require.Error(t, errFromString(unsafe.Error))
	assert.Contains(t, unsafe.Error, "invalid --concurrency")

	// A valid but absurdly large integer is clamped to the 64 ceiling (the Go
	// clamp lives at the args layer — see the file header).
	clamped := cli.ParseCliArgsWithEnv([]string{"audit", "--concurrency", "100"}, map[string]string{})
	require.Empty(t, clamped.Error)
	require.NotNil(t, clamped.AuditConcurrency)
	assert.Equal(t, "64", *clamped.AuditConcurrency)
}

func TestAuditOrgReportRefusesMalformedRepoBeforeTheRun(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	runnerCalls := 0
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		runnerCalls++
		return cli.AuditRunResult{Kind: cli.AuditRunNoop}, nil
	})
	captured := captureIo()

	// L1: `--repo /x` under --org-report is refused BEFORE the run (the
	// guarded aggregation is defense-in-depth).
	exit := cli.RunAudit(cliArgs(t, "audit", "--org-report", "--repo", "/x"), captured,
		cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "org/name")
	assert.Equal(t, 0, runnerCalls)
}

func TestAuditCommitWalkRejectsUnsafeIntegersAndClampsInTheVerb(t *testing.T) {
	// L2: the walk depth rejects an unsafe integer at the args layer.
	unsafe := cli.ParseCliArgsWithEnv([]string{"audit", "--commit-walk", "999999999999999999999"}, map[string]string{})
	require.Error(t, errFromString(unsafe.Error))
	assert.Contains(t, unsafe.Error, "invalid --commit-walk")

	captured := captureIo()
	exit := cli.RunCli([]string{"audit", "--commit-walk", "999999999999999999999"},
		cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)

	// L2: a SAFE-but-large --commit-walk is accepted by args and clamped by
	// the verb (ceiling 1000).
	repo := newGitFixture(t)
	repo.seedBase()
	var seen cli.AuditRunnerInput
	withAuditRunner(t, func(_ context.Context, input cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		seen = input
		return cli.AuditRunResult{Kind: cli.AuditRunNoop, Reason: "trigger-* is not checked out"}, nil
	})
	exit = cli.RunAudit(cliArgs(t, "audit", "--commit-walk", "5000"), captureIo(),
		cli.AuditOptions{Cwd: repo.root})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Equal(t, cli.AuditCommitWalkCeiling, seen.CommitWalk)
}

func TestAuditValidRepoOrgReportStillRenders(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{
			Kind: cli.AuditRunRan, Status: "completed", FindingCount: 1,
			RunID: "run_audit_valid_p12", Trigger: "nightly",
			OrgMarkdown: "ADVISORY — Org-wide drift report — acme\nacme/widgets: 1 finding(s)\n",
		}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit", "--org-report", "--repo", "acme/widgets"), captured,
		cli.AuditOptions{Cwd: repo.root})

	// A completed audit WITH findings maps to 1 (drift) — the same constant
	// check/fix use (single exit-code surface, ARCH §12.1).
	assert.Equal(t, cli.ExitDrift, exit)
	assert.Contains(t, captured.Out(), "Org-wide drift report — acme")
}

func cliReadDoc(t *testing.T, root, relPath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, relPath))
	require.NoError(t, err)
	return string(data)
}
