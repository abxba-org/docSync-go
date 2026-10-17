package main

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
)

func TestRunExitCodes(t *testing.T) {
	t.Run("no verb -> config error exit 2", func(t *testing.T) {
		assert.Equal(t, 2, run(nil))
	})

	t.Run("help -> exit 0", func(t *testing.T) {
		assert.Equal(t, 0, run([]string{"--help"}))
	})

	t.Run("unknown verb -> config error exit 2", func(t *testing.T) {
		assert.Equal(t, 2, run([]string{"frobnicate"}))
	})

	t.Run("check in empty dir -> config error exit 2", func(t *testing.T) {
		assert.Equal(t, 2, run([]string{"check", "--base", "HEAD"}))
	})

	t.Run("init scaffolds and exits 0", func(t *testing.T) {
		assert.Equal(t, 0, run([]string{"init"}))
	})
}

func TestMainPropagatesExitCode(t *testing.T) {
	var captured int
	previous := osExit
	osExit = func(code int) { captured = code }
	t.Cleanup(func() { osExit = previous })

	main()
	assert.Equal(t, 2, captured, "no verb -> main exits 2")
}

var _ = cli.ExitOK
