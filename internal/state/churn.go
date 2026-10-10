package state

import "github.com/DUDE616/docSync-go/internal/schemas"

// Ported from core/src/state/churn.ts: the A8 state-churn instrumentation
// (P0-T15). Called from ONE place — the state store's post-write path, never
// from the write itself — so the counters can never include a speculative or
// failed write, and every observable there is a pure fold over recorded
// outcomes.
//
// The store reports created/updated only after a CAS write lands and its
// hash is known; a failed compare-and-swap is NOT a write and is NOT counted.
// "Reads" are deliberately NOT instrumented here — that counter stays absent
// from every run record until it is defined; a derived placeholder would be
// fabricated data.

// StateChurnCounter is the counter view (TS StateChurnCounter interface):
// state files created, state files updated, state writes skipped as
// byte-identical. Read/parse failures and corrupt-state reports never enter
// it.
type StateChurnCounter struct {
	Added     int
	Updated   int
	Unchanged int
}

// StateChurnChange is one write outcome folded by DeriveStateChurn.
type StateChurnChange struct {
	Written bool
	Created bool
}

// ChurnCounter is the mutable counter plus its recording controls.
type ChurnCounter struct {
	counters StateChurnCounter
}

// CreateStateChurnCounter returns a zeroed counter.
func CreateStateChurnCounter() *ChurnCounter {
	return &ChurnCounter{}
}

// RecordCreated records a created state file.
func (c *ChurnCounter) RecordCreated() { c.counters.Added++ }

// RecordUpdated records a CAS update that landed.
func (c *ChurnCounter) RecordUpdated() { c.counters.Updated++ }

// RecordUnchanged records a write skipped as byte-identical.
func (c *ChurnCounter) RecordUnchanged() { c.counters.Unchanged++ }

// Snapshot returns the current counters.
func (c *ChurnCounter) Snapshot() StateChurnCounter { return c.counters }

// ChurnToRecordFields shapes the snapshot for the `state_churn` record field.
func ChurnToRecordFields(counter StateChurnCounter) schemas.StateChurn {
	return schemas.StateChurn{
		Added:     counter.Added,
		Updated:   counter.Updated,
		Unchanged: counter.Unchanged,
	}
}

// DeriveStateChurn folds one write outcome into the counters: a written file
// counts as created or updated (by whether it existed), a skipped write as
// unchanged.
func DeriveStateChurn(before schemas.StateChurn, change StateChurnChange) schemas.StateChurn {
	if !change.Written {
		return schemas.StateChurn{
			Added:     before.Added,
			Updated:   before.Updated,
			Unchanged: before.Unchanged + 1,
		}
	}
	if change.Created {
		return schemas.StateChurn{
			Added:     before.Added + 1,
			Updated:   before.Updated,
			Unchanged: before.Unchanged,
		}
	}
	return schemas.StateChurn{
		Added:     before.Added,
		Updated:   before.Updated + 1,
		Unchanged: before.Unchanged,
	}
}
