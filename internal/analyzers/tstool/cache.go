// Ported from packages/analyzer-typescript/src/index-cache.ts (F11-1 / P11;
// V-P11-2 groundwork; ARCH §3.7).
//
// Keying decision (ARCH §3.7): the cache is keyed by `blobSha` ONLY — the file
// path is descriptive metadata carried in the request, never part of the key. A
// blob SHA already pins the exact content, so the extracted table is a pure
// function of the key; file identity must not inflate the table. Consequence:
// two paths with the same content share one entry (V1-accepted; never observed
// to be a problem in practice).
//
// Eviction decision (V1, documented): NO eviction — the cache grows for the
// process lifetime. V1 workloads (single check/fix/pr run per process) bound
// the size naturally. A size-bounded variant is deferred to a later phase.
//
// Determinism / purity: `GetOrBuild` builds exactly once per distinct sha, in
// request order; concurrent same-sha requests are single-flight (one build, both
// waiters get the same result), exactly like the TS in-flight promise. Build
// failures are NOT cached — a failed build leaves the sha absent so the next
// request rebuilds.

package tstool

import "sync"

// SymbolIndexCacheRequest is one cache request (port of SymbolIndexCacheRequest).
type SymbolIndexCacheRequest struct {
	// BlobSha is the blob SHA of the source blob (the ONLY cache key).
	BlobSha string
	// FilePath is the descriptive file path — carried, never keyed.
	FilePath string
}

// SymbolIndexCacheStats is the cache observability snapshot (port of
// SymbolIndexCacheStats).
type SymbolIndexCacheStats struct {
	// Builds is the number of times a build actually ran (distinct shas plus
	// explicit rebuilds).
	Builds int
	// CacheHits is the number of requests served from the cache.
	CacheHits int
}

// cacheEntry is one in-flight-or-completed build (the port of the TS cached
// promise).
type cacheEntry[T any] struct {
	ready chan struct{}
	value T
	err   error
}

// SymbolIndexCache is the per-blob-SHA symbol index cache (port of
// SymbolIndexCache).
type SymbolIndexCache[T any] struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry[T]
	builds  int
	hits    int
}

// CreateSymbolIndexCache creates the per-blob-SHA symbol index cache.
//
// Same blob sha ⇒ the cached table is returned and build is NOT invoked. A
// different blob sha ⇒ rebuild; counters via Stats. No eviction in V1. Build
// errors are not cached.
func CreateSymbolIndexCache[T any]() *SymbolIndexCache[T] {
	return &SymbolIndexCache[T]{entries: make(map[string]*cacheEntry[T])}
}

// GetOrBuild returns the cached value for the request's blob sha, building it
// exactly once when absent (single-flight across concurrent callers).
func (c *SymbolIndexCache[T]) GetOrBuild(request SymbolIndexCacheRequest, build func() (T, error)) (T, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*cacheEntry[T])
	}
	if entry, ok := c.entries[request.BlobSha]; ok {
		c.hits++
		c.mu.Unlock()
		<-entry.ready // single-flight: wait for the in-flight build
		return entry.value, entry.err
	}
	entry := &cacheEntry[T]{ready: make(chan struct{})}
	c.entries[request.BlobSha] = entry
	c.builds++
	c.mu.Unlock()

	// The build runs OUTSIDE the lock so a slow build never blocks other shas.
	entry.value, entry.err = build()
	if entry.err != nil {
		c.mu.Lock()
		if c.entries[request.BlobSha] == entry {
			delete(c.entries, request.BlobSha)
		}
		c.mu.Unlock()
	}
	close(entry.ready)
	return entry.value, entry.err
}

// Stats is the cache observability snapshot over the instance lifetime.
func (c *SymbolIndexCache[T]) Stats() SymbolIndexCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return SymbolIndexCacheStats{Builds: c.builds, CacheHits: c.hits}
}
