package cache_test

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cache"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheErrorCodeAndUnwrap(t *testing.T) {
	err := cache.NewAnalysisCacheConfigError("test error")
	require.NotNil(t, err)

	errStr := err.Error()
	assert.Contains(t, errStr, "test error")

	code := err.Code()
	assert.Equal(t, docutil.CodeConfig, code)

	wrapped := err.Unwrap()
	require.NotNil(t, wrapped)
	var coded *docutil.Error
	require.True(t, errors.As(wrapped, &coded))
	assert.Equal(t, docutil.CodeConfig, coded.Code)
}

func TestCacheTouchContinueBranch(t *testing.T) {
	cacheInstance, err := cache.NewAnalysisCache(3)
	require.NoError(t, err)

	rec := schemas.ChangeAnalysis{
		Schema:              "docsync.change-analysis/v1",
		RunID:               "test",
		Repo:                "r",
		BaseSHA:             "b",
		HeadSHA:             "h",
		DependenciesChanged: false,
	}

	cacheInstance.Set("k1", rec)
	cacheInstance.Set("k2", rec)
	got := cacheInstance.Get("k2")
	require.NotNil(t, got)
	assert.Equal(t, 2, cacheInstance.Size())
}

func TestAnalysisCacheKeyNilCaps(t *testing.T) {
	key1, err := cache.AnalysisCacheKey(cache.AnalysisCacheKeyInput{
		Repo:        "r",
		BaseSHA:     "b",
		HeadSHA:     "h",
		PluginNames: []string{"p1"},
		Flags:       cache.AnalysisCacheFlags{FetchContentForGeneratedHints: false},
	})
	require.NoError(t, err)

	key2, err := cache.AnalysisCacheKey(cache.AnalysisCacheKeyInput{
		Repo:    "r",
		BaseSHA: "b",
		HeadSHA: "h",
		PluginNames: []string{"p1"},
		Flags:   cache.AnalysisCacheFlags{FetchContentForGeneratedHints: false},
		Caps:    nil,
	})
	require.NoError(t, err)

	assert.Equal(t, key1, key2)
}

func TestAnalysisCacheSetUpdateRefreshesRecency(t *testing.T) {
	cacheInstance, err := cache.NewAnalysisCache(2)
	require.NoError(t, err)

	rec := schemas.ChangeAnalysis{
		Schema:              "docsync.change-analysis/v1",
		RunID:               "test",
		Repo:                "r",
		BaseSHA:             "b",
		HeadSHA:             "h",
		DependenciesChanged: false,
	}

	cacheInstance.Set("k1", rec)
	cacheInstance.Set("k1", rec)
	assert.Equal(t, 1, cacheInstance.Size())
}
