package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitScaffoldsValidConfig(t *testing.T) {
	root := t.TempDir()
	result := cli.RunInit(cli.InitOptions{RepoRoot: root})
	assert.Equal(t, cli.ExitOK, result.ExitCode)
	for _, name := range []string{"manifest.yml", "models.yml", "instructions.md"} {
		_, err := os.Stat(filepath.Join(root, ".docsync", name))
		require.NoError(t, err, "init must scaffold %s", name)
	}
	// policy.yml / concepts.yml are optional and NOT scaffolded.
	_, err := os.Stat(filepath.Join(root, ".docsync", "policy.yml"))
	assert.True(t, os.IsNotExist(err))
}

func TestInitIdempotentNoOpOnValidConfig(t *testing.T) {
	root := t.TempDir()
	first := cli.RunInit(cli.InitOptions{RepoRoot: root})
	require.Equal(t, cli.ExitOK, first.ExitCode)
	// Re-running with a valid config is a no-op (exit 0), never a silent drop.
	second := cli.RunInit(cli.InitOptions{RepoRoot: root})
	assert.Equal(t, cli.ExitOK, second.ExitCode)
	assert.Contains(t, joinMessages(second.Messages), "nothing to do")
}

func TestInitForceOverwritesAndStaysValid(t *testing.T) {
	root := t.TempDir()
	require.Equal(t, cli.ExitOK, cli.RunInit(cli.InitOptions{RepoRoot: root}).ExitCode)
	// Corrupt an existing file.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".docsync", "manifest.yml"), []byte("version: 1\ndocuments: []\n"), 0o644))
	result := cli.RunInit(cli.InitOptions{RepoRoot: root, Force: true})
	assert.Equal(t, cli.ExitOK, result.ExitCode)
}

func TestInitInvalidWithoutForceFailsClosed(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".docsync"), 0o755))
	// An existing (invalid) manifest without --force: do not drop, do not proceed.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".docsync", "manifest.yml"), []byte("version: 0\n"), 0o644))
	result := cli.RunInit(cli.InitOptions{RepoRoot: root})
	assert.Equal(t, cli.ExitConfig, result.ExitCode)
}

func joinMessages(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}
