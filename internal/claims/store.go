package claims

// Ported from core/src/claims/store.ts: the claim store (P1-T10; ARCH §4.3).
// A thin, CAS-guarded wrapper around the StateStore's registry-enforced claim
// read/write API — claims are `state/` artifacts (D1), validated against
// `docsync.claim` on EVERY read and write (OD-R03). There is NO delete and NO
// status removal: nothing is ever deleted; updates APPEND history events and
// never rewrite them (append-only semantics, ARCH §6.3).
//
// The backend is captured structurally (ClaimStoreBackend) so both the real
// *state.StateStore and test fakes satisfy it; *state.StateStore already
// exposes exactly the three methods needed (ReadClaim, WriteClaim,
// ListClaimIds), so no state API was missing or wrapped. Concurrency: the
// read → merge → CAS-write sequence relies on the store's CAS check (the
// write fails with OK=false when the on-disk bytes changed underneath) — the
// loser re-runs, the winner's bytes survive (R15, V-P0-11 parity).

import (
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// ClaimStoreBackend is the structural backend: exactly the StateStore surface
// claims need.
type ClaimStoreBackend interface {
	ReadClaim(claimID string) (state.StateRead[*schemas.Claim], error)
	WriteClaim(claimID string, claim schemas.Claim, expectedHash *string) (state.CasWriteResult, error)
	ListClaimIds() ([]string, error)
}

// ClaimUpsertOptions carries the caller-owned CAS expectation. The TS shape
// had a three-valued field (`expectedHash?: string | null`), rendered here as
// a pointer-to-options so the three states stay distinguishable:
//   - nil options (Upsert(claim)): no expectation — merge on top of the
//     current on-disk hash (TS: the key omitted / undefined);
//   - &ClaimUpsertOptions{}: an EXPLICIT null — create-if-absent, conflict
//     when the claim already exists (TS: expectedHash: null);
//   - &ClaimUpsertOptions{ExpectedHash: &h}: compare h verbatim (stale hash →
//     conflict, caller re-runs).
type ClaimUpsertOptions struct {
	// ExpectedHash is the explicit CAS expectation; nil is the explicit null
	// ("expect the claim to be absent").
	ExpectedHash *string
}

// ClaimUpsert is the upsert outcome — the same CAS conflict shape as the
// run-record upsert: OK=false carries what the caller expected vs. what is on
// disk; the loser re-runs, never overwrites.
type ClaimUpsert struct {
	OK           bool
	Hash         string
	Created      bool
	ExpectedHash *string
	ActualHash   *string
}

// sameHistoryEvent is event identity for the append-only merge: identical
// at/run_id/event.
func sameHistoryEvent(a, b schemas.ClaimHistoryEvent) bool {
	return a.At == b.At && a.RunID == b.RunID && a.Event == b.Event
}

// MergeHistory merges incoming.History into existing.History append-only:
// existing events are kept verbatim in order; incoming events are appended
// only when not already present (identical at/run_id/event), so idempotent
// re-submissions stay byte-stable. History is never reordered or removed.
func MergeHistory(existing, incoming schemas.Claim) []schemas.ClaimHistoryEvent {
	merged := append([]schemas.ClaimHistoryEvent(nil), existing.History...)
	for _, event := range incoming.History {
		present := false
		for _, kept := range merged {
			if sameHistoryEvent(kept, event) {
				present = true
				break
			}
		}
		if !present {
			merged = append(merged, event)
		}
	}
	return merged
}

// ClaimStore is the CAS-guarded claim store.
type ClaimStore struct {
	backend ClaimStoreBackend
}

// NewClaimStore builds a claim store over a structural backend.
func NewClaimStore(backend ClaimStoreBackend) *ClaimStore {
	return &ClaimStore{backend: backend}
}

// Get returns the stored claim, or nil when no claim with this id exists.
func (s *ClaimStore) Get(id string) (*schemas.Claim, error) {
	read, err := s.backend.ReadClaim(id)
	if err != nil {
		return nil, err
	}
	if !read.Exists {
		return nil, nil
	}
	return read.Content, nil
}

// HashOf returns the current CAS hash of a stored claim (nil when absent).
func (s *ClaimStore) HashOf(id string) (*string, error) {
	read, err := s.backend.ReadClaim(id)
	if err != nil {
		return nil, err
	}
	if !read.Exists {
		return nil, nil
	}
	hash := read.Hash
	return &hash, nil
}

// List returns all stored claims, sorted by id (deterministic).
func (s *ClaimStore) List() ([]schemas.Claim, error) {
	ids, err := s.backend.ListClaimIds()
	if err != nil {
		return nil, err
	}
	claims := make([]schemas.Claim, 0, len(ids))
	for _, id := range ids {
		claim, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		if claim != nil {
			claims = append(claims, *claim)
		}
	}
	return claims, nil
}

// Upsert is the CAS-guarded upsert with append-only history merge. On an
// existing claim, the incoming body replaces the scalar fields (text/anchor/
// source/concept/status) while histories are MERGED (see MergeHistory) — no
// event is ever dropped or rewritten. Without an expectation, the merge
// applies on top of the current on-disk bytes; with an explicit stale
// ExpectedHash the upsert conflicts (OK=false) exactly like a direct store
// write. See ClaimUpsertOptions for the expectation's three states.
func (s *ClaimStore) Upsert(claim schemas.Claim, opts *ClaimUpsertOptions) (ClaimUpsert, error) {
	existing, err := s.backend.ReadClaim(claim.ID)
	if err != nil {
		return ClaimUpsert{}, err
	}
	if !existing.Exists || existing.Content == nil {
		var expected *string
		if opts != nil {
			expected = opts.ExpectedHash
		}
		write, err := s.backend.WriteClaim(claim.ID, claim, expected)
		if err != nil {
			return ClaimUpsert{}, err
		}
		if write.OK {
			return ClaimUpsert{OK: true, Hash: write.Hash, Created: true}, nil
		}
		return ClaimUpsert{OK: false, ExpectedHash: write.ExpectedHash, ActualHash: write.ActualHash}, nil
	}
	var expected *string
	if opts == nil {
		// No expectation: merge on top of the current on-disk bytes.
		onDisk := existing.Hash
		expected = &onDisk
	} else {
		// An explicit expectation (a hash, or the explicit null = create-only).
		expected = opts.ExpectedHash
	}
	merged := claim
	merged.History = MergeHistory(*existing.Content, claim)
	write, err := s.backend.WriteClaim(claim.ID, merged, expected)
	if err != nil {
		return ClaimUpsert{}, err
	}
	if write.OK {
		return ClaimUpsert{OK: true, Hash: write.Hash, Created: false}, nil
	}
	return ClaimUpsert{OK: false, ExpectedHash: write.ExpectedHash, ActualHash: write.ActualHash}, nil
}
