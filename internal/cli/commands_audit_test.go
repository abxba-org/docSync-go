package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const auditManifest = `version: 1
documents:
  - path: docs/api.md
    mode: suggest
    sources:
      - "src/**"
    triggers:
      - public_behavior
`

func repoWithManifest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".docsync"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".docsync", "manifest.yml"), []byte(auditManifest), 0o644))
	return root
}

func TestAuditOrgReportRefusesMalformedRepo(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "audit", "--org-report", "--repo", "widgets")
	exit := cli.RunAudit(args, captured, cli.AuditOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "org/name")
}

func TestAuditMissingManifestExits2(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "audit")
	exit := cli.RunAudit(args, captured, cli.AuditOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
}

func withAuditRunner(t *testing.T, fn func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error)) {
	t.Helper()
	previous := cli.AuditRunnerImpl
	cli.AuditRunnerImpl = fn
	t.Cleanup(func() { cli.AuditRunnerImpl = previous })
}

func TestAuditNoopExits0(t *testing.T) {
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunNoop, Reason: "trigger-* is not checked out"}, nil
	})
	captured := captureIo()
	args := cliArgs(t, "audit")
	exit := cli.RunAudit(args, captured, cli.AuditOptions{Cwd: repoWithManifest(t)})
	assert.Equal(t, cli.ExitOK, exit)
}

func TestAuditAbstainedExits3(t *testing.T) {
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunAbstained, Reason: "claim-store corpus is empty — failing closed (read-mostly invariant, V-P11-8)"}, nil
	})
	captured := captureIo()
	args := cliArgs(t, "audit")
	exit := cli.RunAudit(args, captured, cli.AuditOptions{Cwd: repoWithManifest(t)})
	assert.Equal(t, cli.ExitRuntime, exit)
}

func TestAuditCompletedWithFindingsExits1(t *testing.T) {
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunRan, Status: "completed", FindingCount: 2, AppendedCount: 1, RunID: "nightly:local/repo", Trigger: "nightly"}, nil
	})
	captured := captureIo()
	args := cliArgs(t, "audit")
	exit := cli.RunAudit(args, captured, cli.AuditOptions{Cwd: repoWithManifest(t)})
	assert.Equal(t, cli.ExitDrift, exit)
}

func TestAuditCompletedCleanExits0(t *testing.T) {
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunRan, Status: "completed", FindingCount: 0, RunID: "nightly:local/repo", Trigger: "nightly"}, nil
	})
	captured := captureIo()
	args := cliArgs(t, "audit")
	exit := cli.RunAudit(args, captured, cli.AuditOptions{Cwd: repoWithManifest(t)})
	assert.Equal(t, cli.ExitOK, exit)
}

func TestAuditDegradedRunExits3(t *testing.T) {
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunRan, Status: "failed", Detail: "analyzer outage"}, nil
	})
	captured := captureIo()
	args := cliArgs(t, "audit")
	exit := cli.RunAudit(args, captured, cli.AuditOptions{Cwd: repoWithManifest(t)})
	assert.Equal(t, cli.ExitRuntime, exit)
}
