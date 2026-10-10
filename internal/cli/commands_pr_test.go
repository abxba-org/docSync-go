package cli_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
)

func pr42(n int) *int { return &n }

func TestRenderPrOutcomeShipped(t *testing.T) {
	payload := cli.PrPayload{
		Applied:         []cli.FixFlowApplied{{DocPath: "docs/api.md", PatchRef: "sha256:abc"}},
		FixSummaryLines: nil,
		RunError:        nil,
		PrOpened:        true,
		PrNumber:        pr42(42),
		RecordedReplies: 1,
		PrResult:        nil,
	}
	lines := cli.RenderPrOutcome(payload, cli.GlyphsUnicode)
	joined := joinLines(lines)
	assert.Regexp(t, regexp.MustCompile(`Opened documentation PR #42 \(1 reviewer repl`), joined)
	assert.Regexp(t, regexp.MustCompile(`✓ docs/api\.md \(sha256:abc\)`), joined)
	assert.Regexp(t, regexp.MustCompile(`(?m)^ok: true$`), joined)
}

func TestRenderPrOutcomeRunError(t *testing.T) {
	msg := "boom"
	payload := cli.PrPayload{
		RunError: &msg,
		PrResult: nil,
	}
	lines := cli.RenderPrOutcome(payload, cli.GlyphsUnicode)
	joined := joinLines(lines)
	assert.Contains(t, joined, "Pipeline run error (P7-T13 message): boom")
	assert.Contains(t, joined, "ok: false")
}

func TestJSONResultOfFields(t *testing.T) {
	msg := "boom"
	out := cli.JSONResultOf(cli.PrPayload{RunError: &msg, PrOpened: false})
	assert.Equal(t, false, out["ok"])
	assert.Equal(t, false, out["pr_opened"])

	opened := cli.JSONResultOf(cli.PrPayload{
		Applied:         []cli.FixFlowApplied{{DocPath: "docs/api.md"}},
		PrOpened:        true,
		PrNumber:        pr42(7),
		RecordedReplies: 2,
		PrResult:        nil,
	})
	assert.Equal(t, true, opened["ok"])
	assert.Equal(t, true, opened["pr_opened"])
	assert.Equal(t, 7, opened["pr_number"])
	assert.Equal(t, 2, opened["recorded_replies"])

	unknown, ok := out["unknown"].([]any)
	_ = ok
	assert.NotEmpty(t, unknown)
}

func withPrFlow(t *testing.T, fn func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error)) {
	t.Helper()
	previous := cli.PrFlowRunnerImpl
	cli.PrFlowRunnerImpl = fn
	t.Cleanup(func() { cli.PrFlowRunnerImpl = previous })
}

func TestPrInjectedFlowShippedExits1(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	flow := func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
		return &cli.PrPayload{PrOpened: true, PrNumber: pr42(9)}, nil
	}
	captured := captureIo()
	args := cliArgs(t, "pr", "--base", "HEAD")
	exit := cli.RunPr(args, captured, cli.PrOptions{Cwd: repo.root, Env: prEnv(), Flow: flow})
	assert.Equal(t, cli.ExitDrift, exit)
}

func TestPrInjectedFlowIgnoredOnlyExits0(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	flow := func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
		return &cli.PrPayload{Ignored: []cli.PrIssueRef{{DocPath: "docs/x.md", Reason: "not-a-candidate", Detail: "review"}}}, nil
	}
	captured := captureIo()
	args := cliArgs(t, "pr", "--base", "HEAD")
	exit := cli.RunPr(args, captured, cli.PrOptions{Cwd: repo.root, Env: prEnv(), Flow: flow})
	assert.Equal(t, cli.ExitOK, exit)
}

func prEnv() map[string]string {
	return map[string]string{"OPENROUTER_API_KEY": "sk-or-v1-test-run-key-9f2c"}
}

func TestPrOfflineRefusedExits2(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "pr", "--offline")
	exit := cli.RunPr(args, captured, cli.PrOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "--offline")
}

func TestPrSourcePullRefusedExits2(t *testing.T) {
	withPrFlow(t, func(context.Context, *cli.PrFlowInput) (*cli.PrPayload, error) {
		return nil, cli.ErrPrSourcePull
	})
	captured := captureIo()
	args := cliArgs(t, "pr", "--source-pr", "42")
	exit := cli.RunPr(args, captured, cli.PrOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
