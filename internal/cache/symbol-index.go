package cache

import "sync"

// Symbol-index cache INTERFACE STUB (P1-T19; OD-R05). The real per-blob-SHA
// symbol cache — incremental, persisted under `state/` — lands in Phase 11
// (P11-T02). Phase 1 ships ONLY the interface and a declared-unavailable stub,
// so orchestrators can wire consumption now and every probe degrades EXPLICITLY
// instead of crashing or pretending to have data (fail closed: stubs resolve a
// typed `unavailable` result, never throw, never fabricate).
//
// Keying is by BLOB SHA (content address): identical content shares one entry
// regardless of path or commit, which is what makes the future cache
// incremental across runs.

// SymbolIndexStatus is the tagged-union status of a SymbolIndexResult
// (TS 'hit' | 'miss' | 'unavailable').
type SymbolIndexStatus string

// The result statuses (TS SymbolIndexResult.status values).
const (
	SymbolIndexStatusHit         SymbolIndexStatus = "hit"
	SymbolIndexStatusMiss        SymbolIndexStatus = "miss"
	SymbolIndexStatusUnavailable SymbolIndexStatus = "unavailable"
)

// SymbolIndexResult is the result of a symbol-index `get` (TS discriminated
// union). Status selects the active payload: `hit` carries Symbols, `miss`
// carries nothing, `unavailable` carries Reason.
type SymbolIndexResult struct {
	Status  SymbolIndexStatus `json:"status"`
	Symbols []string          `json:"symbols,omitempty"`
	Reason  string            `json:"reason,omitempty"`
}

// SymbolIndex is the per-blob-SHA symbol index contract. `Get` may also resolve
// nil (treated like a miss by consumers); the unavailable stub never does.
// The TS async surface becomes plain Go returns (goroutines only when callers
// want them).
type SymbolIndex interface {
	Get(blobSha string) (*SymbolIndexResult, error)
	Store(blobSha string, symbols []string) error
}

// SymbolIndexUnavailableReason is the fixed declared-unavailable reason surfaced
// by every stub probe (TS SYMBOL_INDEX_UNAVAILABLE_REASON).
const SymbolIndexUnavailableReason = "symbol index cache lands Phase 11 (P11-T02) — declared interface stub"

// UnavailableSymbolIndex is the stub handle: it counts every
// declared-unavailable probe (get + store) so callers/tests can observe that
// consumption ATTEMPTS happened and were degraded, not swallowed silently. It
// implements SymbolIndex and is safe for concurrent use.
type UnavailableSymbolIndex struct {
	mu     sync.Mutex
	probes int
}

// CreateUnavailableSymbolIndex creates the declared-unavailable symbol index.
// Every `get` resolves `{status: 'unavailable', reason:
// SymbolIndexUnavailableReason}`; every `store` is a no-op that only records the
// probe. No throw, no I/O, no fabricated data — stubs degrade, never crash.
// Ported from createUnavailableSymbolIndex in symbol-index.ts.
func CreateUnavailableSymbolIndex() *UnavailableSymbolIndex {
	return &UnavailableSymbolIndex{}
}

// Probes returns the number of get/store calls serviced by this stub.
func (s *UnavailableSymbolIndex) Probes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probes
}

// Get resolves the typed unavailable result (TS stub get).
func (s *UnavailableSymbolIndex) Get(blobSha string) (*SymbolIndexResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes++
	return &SymbolIndexResult{Status: SymbolIndexStatusUnavailable, Reason: SymbolIndexUnavailableReason}, nil
}

// Store is a no-op that records the probe (TS stub store).
func (s *UnavailableSymbolIndex) Store(blobSha string, symbols []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes++
	return nil
}
