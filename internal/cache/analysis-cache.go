package cache

import (
	"fmt"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Blob-SHA analysis cache (P1-T19; OD-R05). An in-memory bounded LRU over
// ChangeAnalysis records, keyed by a canonical composite key so any differing
// input (repo/base/head SHAs, plugin lineup ORDER, deep-analysis caps,
// content-fetch flag) yields a different key. The ChangeAnalysis builder is
// pure given pinned revs (git I/O only through the injected client), so a
// completed record is fully determined by (repo, baseSha, headSha) + the
// analyzer composition + caps + flags — exactly what the key encodes.
//
// Ephemeral by default (OD-R05, A8): NO file persistence in Phase 1 —
// `state/` stays lean and no cache invalidation problem is created. The opt-in
// per-blob-SHA FILE cache under `state/` (including the symbol-index cache)
// lands in Phase 11 (P11-T02); this module only fixes the key convention and
// the eviction policy.
//
// Inconclusive outcomes are NOT cacheable: they are environment-dependent (git
// failures, dead analyzers) and caching them could pin a transient failure —
// fail closed means re-running, never remembering a failure as an analysis
// result.

// DefaultAnalysisCacheMaxEntries is the TS constructor default (128).
const DefaultAnalysisCacheMaxEntries = 128

// CodeAnalysisCache is the coded-error code carried by AnalysisCacheConfigError.
// The TS constructor threw a plain RangeError (no EDOCSYNC code), so per the
// migration conventions the closest listed code is used: docutil.CodeConfig
// (EDOCSYNC_CONFIG — a configuration problem, fail closed).
const CodeAnalysisCache = docutil.CodeConfig

// AnalysisCacheConfigError mirrors the TS `RangeError` thrown for a malformed
// `maxEntries`.
type AnalysisCacheConfigError struct {
	// Message is the TS message text, preserved exactly.
	Message string
	inner   *docutil.Error
}

// NewAnalysisCacheConfigError builds the coded error.
func NewAnalysisCacheConfigError(message string) *AnalysisCacheConfigError {
	return &AnalysisCacheConfigError{Message: message, inner: docutil.New(CodeAnalysisCache, message)}
}

// Error renders the coded error (`EDOCSYNC_CONFIG: <message>`).
func (e *AnalysisCacheConfigError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_CONFIG.
func (e *AnalysisCacheConfigError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *AnalysisCacheConfigError) Unwrap() error { return e.inner }

// AnalysisCaps holds the deep-analysis budget caps (P1-T08). SEAM: mirrors the
// internal/analysis AnalysisCaps (the only two budget fields the cache key
// encodes are declared here so this package builds stand-alone).
type AnalysisCaps struct {
	// MaxFiles is the maximum number of changed files analyzed with deep analysis.
	MaxFiles int `json:"maxFiles"`
	// MaxDiffSize is the maximum diff-size budget.
	MaxDiffSize int `json:"maxDiffSize"`
}

// AnalysisCacheFlags carries the builder flags that are key-sensitive.
type AnalysisCacheFlags struct {
	// FetchContentForGeneratedHints is the builder flag: fetch head blob content
	// for generated-content hints.
	FetchContentForGeneratedHints bool `json:"fetchContentForGeneratedHints"`
}

// AnalysisCacheKeyInput is the canonical key input: every field is
// key-sensitive, plugin order included.
type AnalysisCacheKeyInput struct {
	// Repo is the repository identity, as used by the analysis builder (`run_id` context).
	Repo string `json:"repo"`
	// BaseSHA is the resolved base commit sha.
	BaseSHA string `json:"baseSha"`
	// HeadSHA is the resolved head commit sha.
	HeadSHA string `json:"headSha"`
	// PluginNames are the analyzer plugin names in composition order (OD-4).
	PluginNames []string `json:"pluginNames"`
	// Caps are the deep-analysis caps; nil = unlimited (the builder's default).
	Caps *AnalysisCaps `json:"caps,omitempty"`
	// Flags are the builder flags.
	Flags AnalysisCacheFlags `json:"flags"`
}

// AnalysisCacheKey derives the canonical cache key: sha256 hex over
// canonicalJson of the key input (object keys sorted recursively, arrays
// order-preserving — so plugin ORDER is key-sensitive while field order is
// not; absent caps serialize as an absent field, never as `null`). Same
// convention as every other content hash in `state/` (bare sha256 hex).
// Ported from analysisCacheKey in analysis-cache.ts.
func AnalysisCacheKey(input AnalysisCacheKeyInput) (string, error) {
	pluginNames := make([]any, len(input.PluginNames))
	for i, name := range input.PluginNames {
		pluginNames[i] = name
	}
	payload := map[string]any{
		"repo":        input.Repo,
		"baseSha":     input.BaseSHA,
		"headSha":     input.HeadSHA,
		"pluginNames": pluginNames,
	}
	if input.Caps != nil {
		payload["caps"] = map[string]any{
			"maxFiles":    input.Caps.MaxFiles,
			"maxDiffSize": input.Caps.MaxDiffSize,
		}
	}
	payload["flags"] = map[string]any{
		"fetchContentForGeneratedHints": input.Flags.FetchContentForGeneratedHints,
	}
	canonical, err := docutil.CanonicalJSON(payload)
	if err != nil {
		return "", err
	}
	return docutil.ContentHash(canonical), nil
}

// AnalysisCache is the in-memory bounded LRU cache for ChangeAnalysis records
// (ephemeral, OD-R05). Safe for concurrent use.
type AnalysisCache struct {
	// MaxEntries is the maximum number of entries; the OLDEST (least recently
	// used) is evicted beyond it.
	MaxEntries int

	mu      sync.Mutex
	entries map[string]schemas.ChangeAnalysis
	// order holds the recency order, oldest (least recently used) first.
	order []string
}

// NewAnalysisCache builds a cache bounded to maxEntries (must be a positive
// integer; the TS constructor threw a RangeError, Go returns the coded error).
func NewAnalysisCache(maxEntries int) (*AnalysisCache, error) {
	if maxEntries < 1 {
		return nil, NewAnalysisCacheConfigError(fmt.Sprintf(
			"AnalysisCache maxEntries must be a positive integer, got %d", maxEntries))
	}
	return &AnalysisCache{MaxEntries: maxEntries, entries: map[string]schemas.ChangeAnalysis{}}, nil
}

// Size returns the number of cached records.
func (c *AnalysisCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Get returns the cached record for `key`, or nil on miss. A hit refreshes its
// recency (LRU) and returns the record by reference — records are deterministic
// and callers must treat them as immutable. Ported from AnalysisCache.get.
func (c *AnalysisCache) Get(key string) *schemas.ChangeAnalysis {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.touch(key)
	return &record
}

// Set caches `record` under `key` (insert-or-move), evicting the LRU tail
// beyond MaxEntries. Ported from AnalysisCache.set.
func (c *AnalysisCache) Set(key string, record schemas.ChangeAnalysis) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		// Move-to-back on insert keeps the recency order exact.
		c.entries[key] = record
		c.order = append(c.order, key)
	} else {
		c.entries[key] = record
		c.touch(key)
	}
	for len(c.entries) > c.MaxEntries && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

// touch moves key to the most-recently-used position. Callers must hold c.mu.
func (c *AnalysisCache) touch(key string) {
	for i, existing := range c.order {
		if existing != key {
			continue
		}
		c.order = append(c.order[:i], c.order[i+1:]...)
		c.order = append(c.order, key)
		return
	}
}
