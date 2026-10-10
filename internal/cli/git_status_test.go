package cli_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePorcelainLineModified(t *testing.T) {
	entry := cli.ParsePorcelainLine(" M docs/api.md")
	require.NotNil(t, entry)
	assert.Equal(t, " M", entry.Status)
	assert.Equal(t, "docs/api.md", entry.Path)
}

func TestParsePorcelainLineUntracked(t *testing.T) {
	entry := cli.ParsePorcelainLine("?? scratch/new.txt")
	require.NotNil(t, entry)
	assert.Equal(t, "??", entry.Status)
	assert.Equal(t, "scratch/new.txt", entry.Path)
}

func TestParsePorcelainLineRenameUsesNewPath(t *testing.T) {
	entry := cli.ParsePorcelainLine("R  old/path.md -> new/path.md")
	require.NotNil(t, entry)
	assert.Equal(t, "R ", entry.Status)
	assert.Equal(t, "new/path.md", entry.Path)
}

func TestParsePorcelainLineTooShortIsNil(t *testing.T) {
	assert.Nil(t, cli.ParsePorcelainLine(" M"))
}
