package cli_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// repoWithOriginHead builds a fixture whose `origin/HEAD` resolves (a real
// remote with a symref), exercising resolveBaseRef's git-success branch.
func repoWithOriginHead(t *testing.T) *gitFixture {
	t.Helper()
	repo := newGitFixture(t)
	origin := t.TempDir()
	if _, err := gitIn(origin, "init", "-b", "main", "-q"); err != nil {
		t.Fatalf("init origin: %v", err)
	}
	if _, err := gitIn(origin, "config", "user.name", "o"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitIn(origin, "config", "user.email", "o@o"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitIn(origin, "commit", "-q", "--allow-empty", "-m", "origin seed"); err != nil {
		t.Fatal(err)
	}
	repo.git("remote", "add", "origin", origin)
	repo.git("fetch", "origin", "-q")
	if out, err := gitIn(repo.root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("symbolic-ref origin/HEAD: %v\n%s", err, out)
	}
	return repo
}

/* ------------------------------------------------------------- RunCli verbs */

func TestRunCliFixDispatchConfigError(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"fix"}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "configuration failed validation")
}

func TestRunCliPrDispatchConfigError(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"pr"}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "configuration failed validation")
}

func TestRunCliAuditDispatchConfigError(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"audit"}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "E_MANIFEST_MISSING")
}

/* ------------------------------------------------------- resolveBaseRef git */

func TestCheckBaseResolvableViaOriginHead(t *testing.T) {
	repo := repoWithOriginHead(t)
	repo.seedBase()
	captured := captureIo()
	// No --base: resolveBaseRef must resolve origin/HEAD and continue offline.
	exit := cli.RunCheck(cliArgs(t, "check", "--offline"), captured, cli.CheckOptions{
		Cwd: repo.root, Env: map[string]string{}})
	// Offline with a non-empty change window -> drift (never fabricated clean).
	assert.Equal(t, cli.ExitDrift, exit)
	assert.Contains(t, captured.Out(), cli.OFFLINE_ABSTAIN_DETAIL)
}

/* ----------------------------------------------------------- RunInit force */

func TestInitForceOverwritesValidConfig(t *testing.T) {
	root := t.TempDir()
	require.Equal(t, cli.ExitOK, cli.RunInit(cli.InitOptions{RepoRoot: root}).ExitCode)
	// Force overwrite of an already-valid config still re-validates and exits 0.
	result := cli.RunInit(cli.InitOptions{RepoRoot: root, Force: true})
	assert.Equal(t, cli.ExitOK, result.ExitCode)
	assert.Contains(t, joinMessages(result.Messages), "wrote .docsync/manifest.yml")
}

/* ------------------------------------------------------------ RunPr offline */

func TestPrOfflineRefused(t *testing.T) {
	captured := captureIo()
	exit := cli.RunPr(cliArgs(t, "pr", "--offline", "--base", "HEAD"), captured, cli.PrOptions{
		Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "--offline is not supported for pr")
}

/* ----------------------------------------------------- resolveChangePr default */

func TestPrDefaultBranchResolvedToSha(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	// default-branch names an existing branch -> resolved to its sha.
	captured := captureIo()
	var seen *cli.PrFlowInput
	exit := cli.RunPr(cliArgs(t, "pr", "--default-branch", "main", "--base", "HEAD"), captured, cli.PrOptions{
		Cwd: repo.root, Env: prEnv(),
		Flow: func(_ context.Context, in *cli.PrFlowInput) (*cli.PrPayload, error) {
			seen = in
			return &cli.PrPayload{PrOpened: true, PrNumber: pr42(3)}, nil
		},
	})
	require.NotNil(t, seen)
	assert.NotEqual(t, "", seen.ChangePr, "default branch resolved to a sha")
	assert.Equal(t, cli.ExitDrift, exit)
}

/* -------------------------------------------------------- RunAudit engine nil */

func TestAuditEngineNotBoundFailsClosed(t *testing.T) {
	previous := cli.AuditRunnerImpl
	cli.AuditRunnerImpl = nil
	t.Cleanup(func() { cli.AuditRunnerImpl = previous })

	captured := captureIo()
	exit := cli.RunAudit(cliArgs(t, "audit"), captured, cli.AuditOptions{Cwd: repoWithManifest(t)})
	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Contains(t, captured.Err(), "loop-B audit engine is not bound")
}

/* ----------------------------------- online run: model ports not bound lane */

func TestCheckOnlineModelPortsNotBoundFailsClosed(t *testing.T) {
	previous := cli.ModelPortsBuilderImpl
	cli.ModelPortsBuilderImpl = nil
	t.Cleanup(func() { cli.ModelPortsBuilderImpl = previous })

	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	exit := cli.RunCheck(cliArgs(t, "check", "--base", "HEAD"), captured, cli.CheckOptions{
		Cwd: repo.root,
		Env: map[string]string{"OPENROUTER_API_KEY": plantedKey}})
	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Contains(t, captured.Err(), "model adapter ports are not bound")
}

/* -------------------------------------------------------- RunCheck JSON + advisory */

func TestCheckJSONIncludesAdvisories(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCheck(cliArgs(t, "check", "--json", "--base", "HEAD"), captured, cli.CheckOptions{
		Cwd:     ".",
		Runtime: &fakeRuntime{outcome: driftOutcome(nil), advisories: []string{"floor advisory"}},
	})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "floor advisory")
	assert.Contains(t, captured.Out(), "\"advisories\"")
}

/* --------------------------------------------------------- context switch */

func TestRunCliUnknownVerbStillUsage(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"--verbose", "frobnicate"}, cli.RunCliOptions{Io: captured})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "unknown verb: \"frobnicate\"")
	assert.Contains(t, captured.Err(), "Usage: docsync")
}

var _ = context.Background

/* --------------------------------------------- config validation lanes */

func TestCheckMissingModelsConfigExits2(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	os.Remove(filepath.Join(repo.root, ".docsync", "models.yml"))
	captured := captureIo()
	exit := cli.RunCheck(cliArgs(t, "check", "--base", "HEAD"), captured, cli.CheckOptions{
		Cwd: repo.root,
		Env: map[string]string{"OPENROUTER_API_KEY": plantedKey}})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "models.yml")
}

func TestCheckInvalidModelsYAMLExits2(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	repo.write(".docsync/models.yml", "   : invalid yaml ::\n")
	captured := captureIo()
	exit := cli.RunCheck(cliArgs(t, "check", "--base", "HEAD"), captured, cli.CheckOptions{
		Cwd: repo.root,
		Env: map[string]string{"OPENROUTER_API_KEY": plantedKey}})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "models.yml")
}

func TestFixRunRecordStoreWriteAppends(t *testing.T) {
	dir := t.TempDir()
	store := cli.NewFixRunRecordStore(dir)
	runID := mustULID(t)
	require.NoError(t, store.Write(cli.FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: runID}))
	assert.True(t, store.Has(runID))
	assert.False(t, store.HasPatchRef("nope"))
}

func mustULID(t *testing.T) string { return genULID(t) }

/* --------------------------------------------------------------- RunCli help + io default */

func TestRunCliHelpRendersUsageAndExits0(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"--help"}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "Usage: docsync")
	assert.Contains(t, captured.Out(), "Verbs:")
	assert.Empty(t, captured.Err())
}

func TestRunCliNilIoDefaultsToRealIo(t *testing.T) {
	// Io nil -> DefaultIo() is used (writes to real streams). No verb -> exit 2.
	exit := cli.RunCli([]string{}, cli.RunCliOptions{Io: nil, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
}

/* ------------------------------------------------- offline base resolution */
