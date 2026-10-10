// Ported from packages/analyzer-typescript/test/cache.test.ts (V-P11-2
// groundwork). Black-box over the exported generic cache.
package tstool_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
)

func TestSymbolIndexCache_KeyedByBlobShaOnly(t *testing.T) {
	type table struct {
		Symbols []string `json:"symbols"`
	}
	cache := tstool.CreateSymbolIndexCache[table]()
	buildCalls := 0
	build := func() (table, error) {
		buildCalls++
		return table{Symbols: []string{"table-for-" + strconv.Itoa(buildCalls)}}, nil
	}

	first, err := cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-a", FilePath: "src/a.ts"}, build)
	require.NoError(t, err)
	assert.Equal(t, 1, buildCalls)
	assert.Equal(t, tstool.SymbolIndexCacheStats{Builds: 1, CacheHits: 0}, cache.Stats())

	// Same SHA ⇒ cached, build NOT invoked (keyed by blobSha ONLY; filePath is
	// metadata).
	cachedSamePath, err := cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-a", FilePath: "src/a.ts"}, build)
	require.NoError(t, err)
	cachedOtherPath, err := cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-a", FilePath: "src/b.ts"}, build)
	require.NoError(t, err)
	assert.Equal(t, 1, buildCalls)
	assert.Equal(t, first, cachedSamePath)
	assert.Equal(t, first, cachedOtherPath)
	assert.Equal(t, tstool.SymbolIndexCacheStats{Builds: 1, CacheHits: 2}, cache.Stats())

	// Different SHA ⇒ rebuild.
	second, err := cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-b", FilePath: "src/a.ts"}, build)
	require.NoError(t, err)
	assert.Equal(t, 2, buildCalls)
	assert.NotEqual(t, first, second)
	assert.Equal(t, tstool.SymbolIndexCacheStats{Builds: 2, CacheHits: 2}, cache.Stats())
}

// stubFailureError is the failing-build error.
type stubFailureError struct{}

func (*stubFailureError) Error() string { return "build failed (test injection)" }

func TestSymbolIndexCache_BuildErrorIsNotCached(t *testing.T) {
	cache := tstool.CreateSymbolIndexCache[int]()
	calls := 0
	failing := func() (int, error) {
		calls++
		return 0, &stubFailureError{}
	}
	_, err := cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-a"}, failing)
	require.Error(t, err)
	_, err = cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-a"}, failing)
	require.Error(t, err)
	assert.Equal(t, 2, calls) // a failed build leaves the sha absent
	assert.Equal(t, tstool.SymbolIndexCacheStats{Builds: 2, CacheHits: 0}, cache.Stats())
}

// TestSymbolIndexCache_ZeroValueUsable pins the lazily-initialized map: a
// zero-value cache behaves exactly like a created one.
func TestSymbolIndexCache_ZeroValueUsable(t *testing.T) {
	var cache tstool.SymbolIndexCache[int]
	value, err := cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-a"}, func() (int, error) { return 7, nil })
	require.NoError(t, err)
	assert.Equal(t, 7, value)
	assert.Equal(t, tstool.SymbolIndexCacheStats{Builds: 1}, cache.Stats())
	again, err := cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-a"}, func() (int, error) { return 9, nil })
	require.NoError(t, err)
	assert.Equal(t, 7, again)
	assert.Equal(t, tstool.SymbolIndexCacheStats{Builds: 1, CacheHits: 1}, cache.Stats())
}

func TestSymbolIndexCache_ConcurrentSameShaIsSingleFlight(t *testing.T) {
	cache := tstool.CreateSymbolIndexCache[int]()
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	build := func() (int, error) {
		calls++
		close(started)
		<-release
		return calls, nil
	}

	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			value, err := cache.GetOrBuild(tstool.SymbolIndexCacheRequest{BlobSha: "sha-a"}, build)
			if err != nil {
				panic(err)
			}
			results <- value
		}()
	}
	<-started
	close(release)
	for i := 0; i < 2; i++ {
		assert.Equal(t, 1, <-results)
	}
	assert.Equal(t, 1, calls, "same-sha concurrent requests must build exactly once")
}
