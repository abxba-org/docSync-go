package claims

// Ported from core/src/claims/status.ts: claim status transitions (P1-T10;
// ARCH §4.3). Claim status is a small machine over
// `verified | suspect | unanchored | stale | closed` (schemas.ClaimStatus*);
// every transition APPENDS a history event (never rewrites history) and is
// CAS-guarded: without an explicit ExpectedHash the write is conditioned on
// the bytes read at transition time — a concurrent writer wins and the
// transition reports the conflict shape instead of overwriting (R15).
//
// Timestamps are caller-owned (At, ISO-8601): core modules keep no hidden
// clocks — the orchestrator (Phase 5) stamps runs deterministically.

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// ClaimTransitionOptions carries the owning run id, the human- or
// system-readable reason embedded in the history event, the caller-owned
// ISO-8601 timestamp, and the optional explicit CAS expectation (a stale hash
// yields the conflict result instead of a write).
type ClaimTransitionOptions struct {
	RunID        string
	Reason       string
	At           string
	ExpectedHash *string
}

// ClaimTransitionResult is the TS union rendered as one value: OK=true with
// the written claim and its CAS hash, or OK=false with the expected-vs-actual
// hash pair.
type ClaimTransitionResult struct {
	OK           bool
	Claim        *schemas.Claim
	Hash         string
	ExpectedHash *string
	ActualHash   *string
}

// upsertOptions projects the transition's optional expectation onto the
// upsert options: a nil ExpectedHash means "no expectation" (merge on top of
// the current on-disk bytes), never an explicit null.
func (o ClaimTransitionOptions) upsertOptions() *ClaimUpsertOptions {
	if o.ExpectedHash == nil {
		return nil
	}
	return &ClaimUpsertOptions{ExpectedHash: o.ExpectedHash}
}

// transition appends one history event and flips the status, CAS-guarded.
func transition(
	store *ClaimStore,
	claimID string,
	event string,
	status string,
	opts ClaimTransitionOptions,
) (ClaimTransitionResult, error) {
	current, err := store.Get(claimID)
	if err != nil {
		return ClaimTransitionResult{}, err
	}
	if current == nil {
		return ClaimTransitionResult{}, state.StateMissingError(
			`cannot transition claim "%s": it does not exist`, claimID)
	}
	historyEvent := schemas.ClaimHistoryEvent{At: opts.At, RunID: opts.RunID, Event: event}
	next := *current
	next.Status = status
	next.History = appendHistory(current.History, historyEvent)
	write, err := store.Upsert(next, opts.upsertOptions())
	if err != nil {
		return ClaimTransitionResult{}, err
	}
	if write.OK {
		return ClaimTransitionResult{OK: true, Claim: &next, Hash: write.Hash}, nil
	}
	return ClaimTransitionResult{
		OK:           false,
		ExpectedHash: write.ExpectedHash,
		ActualHash:   write.ActualHash,
	}, nil
}

// appendHistory appends one event onto a copy of the existing history (the
// existing slice is never aliased or mutated).
func appendHistory(existing []schemas.ClaimHistoryEvent, event schemas.ClaimHistoryEvent) []schemas.ClaimHistoryEvent {
	next := append([]schemas.ClaimHistoryEvent(nil), existing...)
	return append(next, event)
}

// LoseSupport: code changed under the claim's support → status `suspect` +
// history event.
func LoseSupport(store *ClaimStore, claimID string, opts ClaimTransitionOptions) (ClaimTransitionResult, error) {
	return transition(store, claimID, fmt.Sprintf("support lost: %s", opts.Reason),
		schemas.ClaimStatusSuspect, opts)
}

// MarkUnanchored: content-hash resolution failed → status `unanchored` +
// history event (routes REVIEW).
func MarkUnanchored(store *ClaimStore, claimID string, opts ClaimTransitionOptions) (ClaimTransitionResult, error) {
	return transition(store, claimID, fmt.Sprintf("unanchored: %s", opts.Reason),
		schemas.ClaimStatusUnanchored, opts)
}

// MarkStale: support drifted without a direct refutation → status `stale` +
// history event.
func MarkStale(store *ClaimStore, claimID string, opts ClaimTransitionOptions) (ClaimTransitionResult, error) {
	return transition(store, claimID, fmt.Sprintf("stale: %s", opts.Reason),
		schemas.ClaimStatusStale, opts)
}

// CloseClaim: claim retired → status `closed` + history event (terminal;
// nothing is deleted).
func CloseClaim(store *ClaimStore, claimID string, opts ClaimTransitionOptions) (ClaimTransitionResult, error) {
	return transition(store, claimID, fmt.Sprintf("closed: %s", opts.Reason),
		schemas.ClaimStatusClosed, opts)
}

// ApplyReanchorOptions carries the owning run id, the caller-owned
// ISO-8601 timestamp, and the optional explicit CAS expectation.
type ApplyReanchorOptions struct {
	RunID        string
	At           string
	ExpectedHash *string
}

// ApplyReanchor persists a LEGITIMATE re-anchor (unique content-hash match,
// ResolveAnchor → re-anchored): it updates the anchor's line range (the hash
// is unchanged — it IS the identity) and appends the history event
// `re-anchored (content-hash match)` with the owning run_id.
// Ambiguous/nowhere resolutions are rejected — they route REVIEW via
// MarkUnanchored, never a silent anchor move (invariant 13).
func ApplyReanchor(
	store *ClaimStore,
	claimID string,
	resolution AnchorResolution,
	opts ApplyReanchorOptions,
) (ClaimTransitionResult, error) {
	if resolution.Status != AnchorReAnchored {
		return ClaimTransitionResult{}, state.StateKeyError(
			`applyReanchor requires a 're-anchored' resolution (got '%s') — ambiguous or missing spans route REVIEW via markUnanchored`,
			resolution.Status)
	}
	current, err := store.Get(claimID)
	if err != nil {
		return ClaimTransitionResult{}, err
	}
	if current == nil {
		return ClaimTransitionResult{}, state.StateMissingError(
			`cannot re-anchor claim "%s": it does not exist`, claimID)
	}
	// The content hash is unchanged (it matched uniquely); only the locator moves.
	historyEvent := schemas.ClaimHistoryEvent{
		At:    opts.At,
		RunID: opts.RunID,
		Event: "re-anchored (content-hash match)",
	}
	next := *current
	next.Anchor.StartLine = resolution.StartLine
	next.Anchor.EndLine = resolution.EndLine
	next.History = appendHistory(current.History, historyEvent)
	var upsertOpts *ClaimUpsertOptions
	if opts.ExpectedHash != nil {
		upsertOpts = &ClaimUpsertOptions{ExpectedHash: opts.ExpectedHash}
	}
	write, err := store.Upsert(next, upsertOpts)
	if err != nil {
		return ClaimTransitionResult{}, err
	}
	if write.OK {
		return ClaimTransitionResult{OK: true, Claim: &next, Hash: write.Hash}, nil
	}
	return ClaimTransitionResult{
		OK:           false,
		ExpectedHash: write.ExpectedHash,
		ActualHash:   write.ActualHash,
	}, nil
}
