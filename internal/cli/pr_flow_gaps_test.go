package cli_test

import (
	"context"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrFlowGapsRunPrFakeRunner(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()

	prev := cli.PrFlowRunnerImpl
	t.Cleanup(func() { cli.PrFlowRunnerImpl = prev })

	called := false
	cli.PrFlowRunnerImpl = func(ctx context.Context, input *cli.PrFlowInput) (*cli.PrPayload, error) {
		called = true
		require.NotNil(t, input)
		return &cli.PrPayload{PrOpened: false}, nil
	}

	captured := captureIo()
	args := cliArgs(t, "pr", "--base", "HEAD")
	exit := cli.RunPr(args, captured, cli.PrOptions{Cwd: repo.root, Env: prEnv()})
	assert.True(t, called)
	assert.Equal(t, cli.ExitOK, exit)
}

func TestPrFlowGapsRunPrFakeRunnerError(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()

	prev := cli.PrFlowRunnerImpl
	t.Cleanup(func() { cli.PrFlowRunnerImpl = prev })

	cli.PrFlowRunnerImpl = func(ctx context.Context, input *cli.PrFlowInput) (*cli.PrPayload, error) {
		return nil, assert.AnError
	}

	captured := captureIo()
	args := cliArgs(t, "pr", "--base", "HEAD")
	exit := cli.RunPr(args, captured, cli.PrOptions{Cwd: repo.root, Env: prEnv()})
	assert.Equal(t, cli.ExitRuntime, exit)
}

func TestPrFlowGapsRunPrFakeRunnerOpened(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()

	prev := cli.PrFlowRunnerImpl
	t.Cleanup(func() { cli.PrFlowRunnerImpl = prev })

	cli.PrFlowRunnerImpl = func(ctx context.Context, input *cli.PrFlowInput) (*cli.PrPayload, error) {
		n := 7
		return &cli.PrPayload{PrOpened: true, PrNumber: &n}, nil
	}

	captured := captureIo()
	args := cliArgs(t, "pr", "--base", "HEAD")
	exit := cli.RunPr(args, captured, cli.PrOptions{Cwd: repo.root, Env: prEnv()})
	assert.Equal(t, cli.ExitDrift, exit)
}
