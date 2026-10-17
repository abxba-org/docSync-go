package drift

// Internal-package coverage for the tiny pure helpers whose edge branches the
// public suite cannot reach (hash-length edge cases, dedupe, history tails).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func TestCOV_INTERNAL_DRIFT_compareStringsEqual(t *testing.T) {
	assert.Equal(t, 0, compareStrings("same", "same"))
	assert.Equal(t, -1, compareStrings("a", "b"))
	assert.Equal(t, 1, compareStrings("b", "a"))
}

func TestCOV_INTERNAL_DRIFT_shortHeadShort(t *testing.T) {
	assert.Equal(t, "abc", shortHead("abc"))
	assert.Equal(t, "0123456789ab", shortHead("0123456789abcdef"))
}

func TestCOV_INTERNAL_DRIFT_sortedUnique(t *testing.T) {
	out := sortedUnique([]string{"b", "a", "b", "c", "a"})
	require.Equal(t, []string{"a", "b", "c"}, out)

	out = sortedUnique(nil)
	assert.Empty(t, out)
}

func TestCOV_INTERNAL_DRIFT_lastHistoryEventEdge(t *testing.T) {
	assert.Equal(t, "", lastHistoryEvent(nil))
	assert.Equal(t, "", lastHistoryEvent(&schemas.Claim{}))
	assert.Equal(t, "evt", lastHistoryEvent(&schemas.Claim{
		History: []schemas.ClaimHistoryEvent{{Event: "evt"}},
	}))
}

func TestCOV_INTERNAL_DRIFT_claimSupportDetail(t *testing.T) {
	assert.Equal(t, "backing blob identity matches the recorded source anchor (ARCH §4.3)",
		claimSupportDetail(schemas.ClaimSupportSupported))
	assert.Contains(t, claimSupportDetail(schemas.ClaimSupportLostSupport), "no longer exist")
	assert.Contains(t, claimSupportDetail(schemas.ClaimSupportUnresolvableAnchor), "REVIEW")
}

func TestCOV_INTERNAL_DRIFT_touchingCommitsErrorPropagates(t *testing.T) {
	runner, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: t.TempDir()})
	require.NoError(t, err)
	_, err = touchingCommits(t.Context(), runner, "abc", "src/x.ts", 5)
	require.Error(t, err)
}

func TestCOV_INTERNAL_DRIFT_indexDiffCoversOldPaths(t *testing.T) {
	oldPath := "a"
	newPath := "b"
	index := indexDiff(
		[]gitx.DiffFileRecord{{Path: newPath, OldPath: &oldPath}},
		[]gitx.DiffStatRecord{{Path: newPath, OldPath: &oldPath}})
	require.NotNil(t, index.byPath[newPath])
	require.NotNil(t, index.byPath[oldPath])
	require.NotNil(t, index.stats[newPath])
	require.NotNil(t, index.stats[oldPath])
}
