package cli_test

// Ported from packages/cli/test/bin.test.ts (runCli surface, P7-T01,
// P7-T06/T07) — the dispatcher lanes not already pinned by cmd_test.go:
//   - `--help` exits 0 with usage listing every verb and all four exit codes;
//   - V-P7-4 partial: config-class usage errors exit 2; check, fix and pr are
//     all wired (exit 2 at the manifest gate in an unconfigured cwd, before
//     any AI stage, D8);
//   - `check --json --offline` in an unconfigured cwd exits 2 with NO report
//     emitted (config failure → stderr diagnostics only).
import (
	"regexp"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
)

func TestRunCliHelpListsEveryVerbAndAllFourExitCodes(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"--help"}, cli.RunCliOptions{Io: captured})

	assert.Equal(t, cli.ExitOK, exit)
	text := captured.Out()
	// Every verb on the Go surface (the TS pinned four; audit is the fifth).
	for _, verb := range cli.CLI_VERBS {
		assert.Contains(t, text, verb)
	}
	assert.Regexp(t, regexp.MustCompile(`0\s+no drift detected`), text)
	assert.Regexp(t, regexp.MustCompile(`1\s+drift detected`), text)
	assert.Regexp(t, regexp.MustCompile(`2\s+configuration/manifest error`), text)
	assert.Regexp(t, regexp.MustCompile(`3\s+runtime failure`), text)
}

func TestRunCliConfigClassUsageErrorsExit2WithVerbsWired(t *testing.T) {
	// Unknown verb → usage error on stderr, exit 2 (D8 config lane).
	unknown := captureIo()
	exit := cli.RunCli([]string{"frobnicate"}, cli.RunCliOptions{Io: unknown})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Regexp(t, regexp.MustCompile(`unknown verb`), unknown.Err())
	assert.Contains(t, unknown.Err(), "Exit codes:")

	// No verb at all is also a config-class usage error.
	bare := captureIo()
	assert.Equal(t, cli.ExitConfig, cli.RunCli([]string{}, cli.RunCliOptions{Io: bare}))

	// check / fix / pr are all wired: in an unconfigured cwd each fails
	// closed at the manifest gate — exit 2 BEFORE any AI stage, never 3 or 0.
	for _, verb := range []string{"check", "fix", "pr"} {
		captured := captureIo()
		code := cli.RunCli([]string{verb, "--base", "HEAD"}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
		assert.Equal(t, cli.ExitConfig, code, "verb %s must be wired and fail closed", verb)
		assert.Contains(t, captured.Err(), "manifest", "verb %s must fail at the manifest gate", verb)
	}
}

func TestRunCliCheckJSONOfflineUnconfiguredCwdExits2WithNoReport(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"check", "--json", "--offline", "--base", "HEAD"},
		cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})

	assert.Equal(t, cli.ExitConfig, exit)
	// No report emitted — config failure produces stderr diagnostics only.
	assert.Empty(t, captured.Out())
}
