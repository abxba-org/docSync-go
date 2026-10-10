package cli_test

// Ported from packages/cli/test/check-verbose.test.ts — `--verbose` stage
// timings (P7-T02 cross-cutting; P5-T17 telemetry): after the report, one
// line per recorded pipeline stage —
// `stage=<name> status=<status> latency_ms=<n|null>`.
//
// Runs the REAL offline engine (OD-R16 — no models.yml or key required) over
// a real fixture repo: no injection seam is involved here.
import (
	"regexp"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
)

// cliPipelineStageNames is the frozen pipeline stage list the TS test pins.
var cliPipelineStageNames = []string{
	"analyze", "map", "invalidate", "jev", "retrieval", "writer", "verification", "policy", "persist",
}

func TestCheckVerboseOfflineRunEmitsOneLinePerStage(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	repo.write("src/payments/refund.ts", "export function refund(): void {}\n// changed\nexport const x = 1;\n")
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	repo.commitPaths([]string{"src/payments/refund.ts"}, "feat: refund idempotency key optional")

	captured := captureIo()
	exit := cli.RunCheck(cliArgs(t, "check", "--offline", "--verbose", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	assert.Equal(t, cli.ExitDrift, exit)
	lines := captured.stdoutLines()
	analyze := cliFindStageLine(lines, "analyze")
	assert.Regexp(t, regexp.MustCompile(`^stage=analyze status=ok latency_ms=(\d+|null)$`), analyze)
	jev := cliFindStageLine(lines, "jev")
	assert.Equal(t, "stage=jev status=skipped latency_ms=null", jev)
	// Every frozen pipeline stage is represented exactly once.
	for _, stage := range cliPipelineStageNames {
		assert.NotEmpty(t, cliFindStageLine(lines, stage), "stage %s must render exactly one line", stage)
	}
	stageLines := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "stage=") {
			stageLines++
		}
	}
	assert.Equal(t, len(cliPipelineStageNames), stageLines)
}

func TestCheckVerboseNonVerboseRunsEmitNoStageLines(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))

	captured := captureIo()
	_ = cli.RunCheck(cliArgs(t, "check", "--offline", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	for _, line := range captured.stdoutLines() {
		assert.False(t, strings.HasPrefix(line, "stage="), "non-verbose runs emit no stage lines")
	}
}

// cliFindStageLine returns the single stage line for name ("" when absent).
func cliFindStageLine(lines []string, name string) string {
	prefix := "stage=" + name + " "
	found := ""
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			if found != "" {
				return found + "|" + line // two matches → the caller's NotEmpty still catches shape drift
			}
			found = line
		}
	}
	return found
}
