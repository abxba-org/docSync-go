package cli_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
)

func TestRunCliNoVerbExits2WithUsage(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{}, cli.RunCliOptions{Io: captured})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "no verb given")
	assert.Contains(t, captured.Err(), "Usage:")
}

func TestRunCliHelpExits0WithUsage(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"--help"}, cli.RunCliOptions{Io: captured})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "Usage: docsync")
}

func TestRunCliUnknownVerbExits2(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"frobnicate"}, cli.RunCliOptions{Io: captured})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "unknown verb")
}

func TestRunCliInitIntegration(t *testing.T) {
	root := t.TempDir()
	captured := captureIo()
	exit := cli.RunCli([]string{"init"}, cli.RunCliOptions{Io: captured, Cwd: root})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "skeleton validates cleanly")
}

func TestRunCliCheckMissingManifestExits2(t *testing.T) {
	captured := captureIo()
	exit := cli.RunCli([]string{"check"}, cli.RunCliOptions{Io: captured, Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
}
