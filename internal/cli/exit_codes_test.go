package cli_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
)

func TestDescriptionForExitCode(t *testing.T) {
	assert.Equal(t, "no drift detected", cli.DescriptionForExitCode(cli.ExitOK))
	assert.Equal(t, "drift detected (candidates found)", cli.DescriptionForExitCode(cli.ExitDrift))
	assert.Contains(t, cli.DescriptionForExitCode(cli.ExitConfig), "configuration")
	assert.Contains(t, cli.DescriptionForExitCode(cli.ExitRuntime), "runtime")
}

func TestExitCodeConstantsAreBinding(t *testing.T) {
	assert.Equal(t, 0, int(cli.ExitOK))
	assert.Equal(t, 1, int(cli.ExitDrift))
	assert.Equal(t, 2, int(cli.ExitConfig))
	assert.Equal(t, 3, int(cli.ExitRuntime))
}
