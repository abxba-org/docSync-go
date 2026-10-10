package cli_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const plantedKey = "sk-or-v1-planted-test-key-DO-NOT-PRINT"

func TestCheckBrokenManifestExits2BeforeAnyAIStage(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	// A schema-level manifest defect (`version` must be a positive int) is a
	// line-precise E_MANIFEST_SCHEMA hard error.
	// DEVIATION NOTE: the Go config core does not (yet) emit
	// E_MANIFEST_UNKNOWN_TRIGGER for an unknown trigger name (it only checks
	// triggers are non-empty), so this lane is proven with a schema defect the
	// Go validator DOES classify hard — the binding contract under test (a
	// broken manifest fails closed BEFORE any AI stage, V-P7-4) is identical.
	repo.write(".docsync/manifest.yml", `version: 0
documents:
  - path: docs/api.md
    mode: suggest
    sources:
      - "src/payments/**"
    triggers:
      - public_behavior
`)
	captured := captureIo()
	args := cliArgs(t, "check", "--base", "HEAD~1")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	assert.Equal(t, cli.ExitConfig, exit)
	stderr := captured.Err()
	assert.Contains(t, stderr, "E_MANIFEST_SCHEMA")
	assert.Regexp(t, regexp.MustCompile(`manifest\.yml:\d+`), stderr)
	// Ordering proof: the failure is the MANIFEST diagnostic, never a
	// credential message (the composition never got past config, V-P7-4).
	assert.NotContains(t, stderr, "OPENROUTER_API_KEY")
}

func TestCheckMissingApiKeyExits2AfterValidConfig(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	args := cliArgs(t, "check", "--base", "HEAD~1")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "OPENROUTER_API_KEY")
}

func TestCheckInvalidModelsExits2(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	repo.write(".docsync/models.yml", "schema: docsync.models/v1\nversion: 1\nroles:\n  decision-engine:\n    model: z-ai/glm-4.7\n")
	captured := captureIo()
	args := cliArgs(t, "check", "--base", "HEAD~1")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: repo.root, Env: map[string]string{"OPENROUTER_API_KEY": plantedKey}})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "models.yml")
}

func TestCheckInvalidPolicyExits2(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	repo.write(".docsync/policy.yml", "schema: docsync.jev-policy/v1\n")
	captured := captureIo()
	args := cliArgs(t, "check", "--base", "HEAD~1")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: repo.root, Env: map[string]string{"OPENROUTER_API_KEY": plantedKey}})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "policy.yml")
}

func TestCheckUnresolvableBaseExits2(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	args := cliArgs(t, "check")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: repo.root, Env: map[string]string{"OPENROUTER_API_KEY": plantedKey}})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "--base")
}

func TestCheckRedactionPlantedKeyNeverReachesOutput(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	_ = cli.RunCheck(cliArgs(t, "check", "--base", "HEAD~1"), captured, cli.CheckOptions{Cwd: repo.root, Env: map[string]string{"OPENROUTER_API_KEY": plantedKey}})
	_ = cli.RunCheck(cliArgs(t, "check"), captured, cli.CheckOptions{Cwd: repo.root, Env: map[string]string{"OPENROUTER_API_KEY": plantedKey}})
	everything := captured.Out() + "\n" + captured.Err()
	assert.NotContains(t, everything, plantedKey)
	assert.NotContains(t, everything, "sk-or-v1")
}

/* ----------------------------------------------------- injected offline */

func TestCheckOfflineMappedDriftExits1(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "check", "--offline", "--base", "HEAD")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{
		Cwd:     ".",
		Runtime: &fakeRuntime{outcome: degradedOfflineOutcome([]string{"docs/api.md", "docs/locked.md"}, nil)},
	})
	assert.Equal(t, cli.ExitDrift, exit)
	out := captured.Out()
	assert.Contains(t, out, "offline mode (OD-R16)")
	// D14: never a fabricated "no docs affected".
	assert.NotContains(t, strings.ToLower(out), "no docs affected")
}

func TestCheckOfflineEmptyWindowExits0(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "check", "--offline", "--base", "HEAD")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{
		Cwd:     ".",
		Runtime: &fakeRuntime{outcome: degradedOfflineOutcome(nil, nil)},
	})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "offline mode (OD-R16)")
	assert.NotContains(t, captured.Out(), "⚠")
}

func TestCheckOfflineUnknownCoverageExits1(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "check", "--offline", "--base", "HEAD")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{
		Cwd:     ".",
		Runtime: &fakeRuntime{outcome: degradedOfflineOutcome(nil, []string{"scratch/unmapped.txt"})},
	})
	assert.Equal(t, cli.ExitDrift, exit)
	assert.Contains(t, captured.Out(), "Unknown coverage")
	assert.Contains(t, captured.Out(), "scratch/unmapped.txt")
}

/* ------------------------------------------------ injected exit mapping (online) */

func TestCheckExitCleanIgnoreRoute0(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "check", "--base", "HEAD")
	outcome := driftOutcome([]reportRow{{docPath: "docs/api.md", outcome: "ignore", confidence: 0.9}})
	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: ".", Runtime: &fakeRuntime{outcome: outcome}})
	assert.Equal(t, cli.ExitOK, exit)
}

func TestCheckExitAdvisoryDriftStrictPromotes(t *testing.T) {
	outcome := func() *fakeRuntime {
		return &fakeRuntime{outcome: driftOutcome([]reportRow{{docPath: "docs/api.md", outcome: "suggest", confidence: 0.95}})}
	}

	without := captureIo()
	exit0 := cli.RunCheck(cliArgs(t, "check", "--base", "HEAD"), without, cli.CheckOptions{Cwd: ".", Runtime: outcome()})
	assert.Equal(t, cli.ExitOK, exit0)
	assert.Contains(t, without.Out(), "⚠ docs/api.md")

	withStrict := captureIo()
	exit1 := cli.RunCheck(cliArgs(t, "check", "--strict", "--base", "HEAD"), withStrict, cli.CheckOptions{Cwd: ".", Runtime: outcome()})
	assert.Equal(t, cli.ExitDrift, exit1)
}

func TestCheckExitOutageDegradesToAdvisory0EmptyRows(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "check", "--json", "--base", "HEAD")
	exit := cli.RunCheck(args, captured, cli.CheckOptions{
		Cwd:     ".",
		Runtime: &fakeRuntime{outcome: degradedOfflineOutcome(nil, nil)},
	})
	// V-P5-7 posture: the outage is an honest run-level abstain with NO rows.
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), `"rows":[]`)
}

func TestCheckUnresolvableWindowIsRuntime3(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "check", "--base", "definitely-not-a-ref")
	err := orchestrator.NewPipelineRunError("setup: cannot resolve base/head refs ('definitely-not-a-ref', 'HEAD'): unknown revision", "", errNew("unknown revision"))
	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: ".", Runtime: &fakeRuntime{err: err}})
	assert.Equal(t, cli.ExitRuntime, exit)
	assert.Contains(t, captured.Err(), "cannot resolve base/head refs")
}

/* ----------------------------------- real offline engine path (no injection) */

func TestCheckOfflineRealEngineMappedDriftExits1(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	// Capture the PRE-change HEAD, then commit a behavioral source change.
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	repo.write("src/payments/refund.ts", "export function refund(): void {}\n// changed\nexport const x = 1;\n")
	repo.commitPaths([]string{"src/payments/refund.ts"}, "feat: refund behavior change")

	captured := captureIo()
	args := cliArgs(t, "check", "--offline", "--base", base)
	exit := cli.RunCheck(args, captured, cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	assert.Equal(t, cli.ExitDrift, exit)
	out := captured.Out()
	assert.Contains(t, out, "offline mode (OD-R16)")
	// Both manifest docs map src/payments/** → honest per-doc abstains.
	assert.Contains(t, out, "docs/api.md")
	assert.Contains(t, out, "docs/locked.md")
	assert.Contains(t, out, cli.OFFLINE_ABSTAIN_DETAIL)
	assert.NotContains(t, strings.ToLower(out), "no docs affected")
}

/* --------------------------------------------------------------- helpers */

func cliArgs(t *testing.T, argv ...string) *cli.ParsedInvocation {
	t.Helper()
	result := cli.ParseCliArgs(argv)
	require.Empty(t, result.Error)
	return &result
}

func errNew(msg string) error { return &stringError{msg: msg} }
