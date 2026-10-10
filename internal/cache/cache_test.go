package cache_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/cache"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Cache tests (P1-T19): canonical composite-key sensitivity (repo/base/head
// SHAs, plugin ORDER, caps, flags), in-memory bounded LRU eviction + recency,
// get/set round-trip, and the declared-unavailable symbol-index stub (degrades
// explicitly, never throws, never fabricates; OD-R05 — ephemeral by default, no
// file persistence in Phase 1).
//
// Ported from packages/core/test/cache.test.ts (black-box, testify).

// record is the RECORD fixture of cache.test.ts (fresh copy per call).
func record() schemas.ChangeAnalysis {
	return schemas.ChangeAnalysis{
		Schema:              "docsync.change-analysis/v1",
		RunID:               "acme/payments#base..head",
		Repo:                "acme/payments",
		BaseSHA:             "base",
		HeadSHA:             "head",
		Files:               []schemas.ChangedFile{{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified, SymbolsChanged: boolp(true)}},
		DependenciesChanged: false,
		ClassificationHints: []schemas.ClassificationHint{},
		DeepAnalysis:        schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: strp("fixture")},
		AnalyzerCoverage:    []schemas.AnalyzerCoverage{{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK}},
	}
}

func strp(value string) *string { return &value }
func boolp(value bool) *bool    { return &value }

// withBaseSHA overrides the key input's base sha (the TS `keyInput({...})` overrides).
func withBaseSHA(input cache.AnalysisCacheKeyInput, baseSHA string) cache.AnalysisCacheKeyInput {
	input.BaseSHA = baseSHA
	return input
}

func keyInput() cache.AnalysisCacheKeyInput {
	return cache.AnalysisCacheKeyInput{
		Repo:        "acme/payments",
		BaseSHA:     "1111",
		HeadSHA:     "2222",
		PluginNames: []string{"typescript", "generic"},
		Flags:       cache.AnalysisCacheFlags{FetchContentForGeneratedHints: false},
	}
}

// mustKey derives a cache key, failing the test on the (unexpected) canonical
// serialization error.
func mustKey(t *testing.T, input cache.AnalysisCacheKeyInput) string {
	t.Helper()
	key, err := cache.AnalysisCacheKey(input)
	require.NoError(t, err)
	return key
}

func TestAnalysisCacheKey_CanonicalCompositeKey(t *testing.T) {
	t.Run("is deterministic for identical inputs (sha256 hex over canonicalJson)", func(t *testing.T) {
		key := mustKey(t, keyInput())
		assert.Equal(t, key, mustKey(t, keyInput()))
		assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), key)
	})

	t.Run("is sensitive to every field, including plugin ORDER (OD-4 lineup)", func(t *testing.T) {
		base := mustKey(t, keyInput())

		otherRepo := keyInput()
		otherRepo.Repo = "other/repo"
		assert.NotEqual(t, base, mustKey(t, otherRepo))

		otherBase := keyInput()
		otherBase.BaseSHA = "9999"
		assert.NotEqual(t, base, mustKey(t, otherBase))

		otherHead := keyInput()
		otherHead.HeadSHA = "9999"
		assert.NotEqual(t, base, mustKey(t, otherHead))

		reorderedPlugins := keyInput()
		reorderedPlugins.PluginNames = []string{"generic", "typescript"}
		assert.NotEqual(t, base, mustKey(t, reorderedPlugins))

		fewerPlugins := keyInput()
		fewerPlugins.PluginNames = []string{"typescript"}
		assert.NotEqual(t, base, mustKey(t, fewerPlugins))

		withCaps := keyInput()
		withCaps.Caps = &cache.AnalysisCaps{MaxFiles: 10, MaxDiffSize: 1000}
		assert.NotEqual(t, base, mustKey(t, withCaps))

		otherCaps := keyInput()
		otherCaps.Caps = &cache.AnalysisCaps{MaxFiles: 11, MaxDiffSize: 1000}
		assert.NotEqual(t, mustKey(t, withCaps), mustKey(t, otherCaps))

		withFlag := keyInput()
		withFlag.Flags = cache.AnalysisCacheFlags{FetchContentForGeneratedHints: true}
		assert.NotEqual(t, base, mustKey(t, withFlag))
	})
}

func TestAnalysisCache_BoundedLRU(t *testing.T) {
	t.Run("round-trips get/set; misses resolve nil", func(t *testing.T) {
		cacheInstance, err := cache.NewAnalysisCache(cache.DefaultAnalysisCacheMaxEntries)
		require.NoError(t, err)
		key := mustKey(t, keyInput())
		assert.Nil(t, cacheInstance.Get(key))
		assert.Equal(t, 0, cacheInstance.Size())
		cacheInstance.Set(key, record())
		assert.Equal(t, 1, cacheInstance.Size())
		assert.Equal(t, record(), *cacheInstance.Get(key))
	})

	t.Run("defaults to 128 entries; invalid bounds are refused", func(t *testing.T) {
		cacheInstance, err := cache.NewAnalysisCache(cache.DefaultAnalysisCacheMaxEntries)
		require.NoError(t, err)
		assert.Equal(t, cache.DefaultAnalysisCacheMaxEntries, cacheInstance.MaxEntries)
		assert.Equal(t, 128, cacheInstance.MaxEntries)

		// The TS RangeError for a non-positive maxEntries: 0 and a negative
		// bound are both refused with the preserved message. A NON-INTEGER
		// maxEntries is not expressible through the typed Go constructor
		// (NewAnalysisCache takes an int) — the refusal is pinned here at 0.
		for _, invalid := range []int{0, -1, -128} {
			_, err = cache.NewAnalysisCache(invalid)
			var configErr *cache.AnalysisCacheConfigError
			require.Error(t, err)
			assert.ErrorAs(t, err, &configErr)
			assert.Contains(t, err.Error(), "AnalysisCache maxEntries must be a positive integer")
		}
	})

	t.Run("evicts the least recently used beyond maxEntries", func(t *testing.T) {
		cacheInstance, err := cache.NewAnalysisCache(2)
		require.NoError(t, err)
		k1 := mustKey(t, withBaseSHA(keyInput(), "1"))
		k2 := mustKey(t, withBaseSHA(keyInput(), "2"))
		k3 := mustKey(t, withBaseSHA(keyInput(), "3"))
		cacheInstance.Set(k1, record())
		cacheInstance.Set(k2, record())
		cacheInstance.Set(k3, record()) // k1 is oldest → evicted
		assert.Equal(t, 2, cacheInstance.Size())
		assert.Nil(t, cacheInstance.Get(k1))
		assert.Equal(t, record(), *cacheInstance.Get(k2))
		assert.Equal(t, record(), *cacheInstance.Get(k3))
	})

	t.Run("a get refreshes recency (LRU, not FIFO)", func(t *testing.T) {
		cacheInstance, err := cache.NewAnalysisCache(2)
		require.NoError(t, err)
		k1 := mustKey(t, withBaseSHA(keyInput(), "1"))
		k2 := mustKey(t, withBaseSHA(keyInput(), "2"))
		k3 := mustKey(t, withBaseSHA(keyInput(), "3"))
		cacheInstance.Set(k1, record())
		cacheInstance.Set(k2, record())
		require.NotNil(t, cacheInstance.Get(k1)) // touch k1 → k2 becomes LRU
		cacheInstance.Set(k3, record())
		assert.Equal(t, record(), *cacheInstance.Get(k1))
		assert.Nil(t, cacheInstance.Get(k2))
		assert.Equal(t, record(), *cacheInstance.Get(k3))
	})

	t.Run("set over an existing key updates in place without evicting", func(t *testing.T) {
		cacheInstance, err := cache.NewAnalysisCache(2)
		require.NoError(t, err)
		k1 := mustKey(t, withBaseSHA(keyInput(), "1"))
		k2 := mustKey(t, withBaseSHA(keyInput(), "2"))
		cacheInstance.Set(k1, record())
		updated := record()
		updated.RunID = "updated"
		cacheInstance.Set(k1, updated)
		assert.Equal(t, 1, cacheInstance.Size())
		cacheInstance.Set(k2, record())
		got := cacheInstance.Get(k1)
		require.NotNil(t, got)
		assert.Equal(t, "updated", got.RunID)
	})
}

func TestCreateUnavailableSymbolIndex_DeclaredInterfaceStub(t *testing.T) {
	t.Run("every get resolves the typed unavailable result with the fixed reason", func(t *testing.T) {
		stub := cache.CreateUnavailableSymbolIndex()
		result, err := stub.Get("blobsha")
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, &cache.SymbolIndexResult{
			Status: cache.SymbolIndexStatusUnavailable,
			Reason: cache.SymbolIndexUnavailableReason,
		}, result)
		assert.Contains(t, cache.SymbolIndexUnavailableReason, "Phase 11 (P11-T02)")
	})

	t.Run("store is a no-op that records the probe; probes count every degraded call", func(t *testing.T) {
		stub := cache.CreateUnavailableSymbolIndex()
		assert.Equal(t, 0, stub.Probes())
		require.NoError(t, stub.Store("blobsha", []string{"refundRequest"}))
		_, err := stub.Get("blobsha")
		require.NoError(t, err)
		assert.Equal(t, 2, stub.Probes())
	})

	t.Run("never fabricates a hit or a miss", func(t *testing.T) {
		stub := cache.CreateUnavailableSymbolIndex()
		result, err := stub.Get("blobsha")
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, cache.SymbolIndexStatusUnavailable, result.Status)
	})
}
