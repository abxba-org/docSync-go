package baselines

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// Ported from core/src/baselines/store.ts: a baseline records WHAT a document
// was verified against (source files/symbols/claims + the commit + the doc's
// content hash at verification time) and bridges Loop A and Loop B: records
// land here in Phase 1; Loop B sweeps them in Phase 11.
//
// §6.1 CAS contract (R15): a baseline update persists ONLY if the STORED
// `doc_content_hash` AND `baseline_commit` still match the run-start values —
// either mismatch means the world moved under the run, the upsert reports the
// typed conflict shape, and the loser RE-RUNS its evaluation (never
// overwrites). The byte-level CAS underneath is the state store's (V-P0-11);
// the guards below add the §6.1 semantic layer on top:
//
//   - absent record  → create (`Created: true`);
//   - stored values mismatch EITHER guard → ConflictDocContentChanged /
//     ConflictBaselineCommitMoved (loser re-runs claim evaluation);
//   - values match but the bytes changed between read and write → the CAS
//     write conflicts (ConflictCasConflict) — the winner's bytes survive.
//
// There is NO delete: baselines are replaced by upsert, never removed
// (ARCH §6.3). Reads validate against the registry (OD-R03) — a corrupt or
// drifted baseline file fails closed, it is never "no baseline".

// BaselineStoreBackend is the structural backend: exactly the state store
// surface baselines need. *state.StateStore satisfies it as-is.
type BaselineStoreBackend interface {
	ReadBaseline(docPath string) (state.StateRead[*schemas.Baseline], error)
	WriteBaseline(docPath string, baseline schemas.Baseline, expectedHash *string) (state.CasWriteResult, error)
}

// BaselineGuards is the §6.1 run-start guard pair: the values the caller
// observed when its run started. The upsert persists only when the STORED
// record still carries both.
type BaselineGuards struct {
	ExpectedDocContentHash string
	ExpectedBaselineCommit string
}

// BaselineUpsertConflict is the typed §6.1 conflict kind (the TS union's `kind`
// discriminator); empty on a successful upsert.
type BaselineUpsertConflict string

// The §6.1 conflict kinds.
const (
	// ConflictDocContentChanged: the stored doc_content_hash moved since run start.
	ConflictDocContentChanged BaselineUpsertConflict = "doc-content-changed"
	// ConflictBaselineCommitMoved: the stored baseline_commit moved since run start.
	ConflictBaselineCommitMoved BaselineUpsertConflict = "baseline-commit-moved"
	// ConflictCasConflict: the bytes changed between the guard read and the write.
	ConflictCasConflict BaselineUpsertConflict = "cas-conflict"
)

// BaselineUpsert is the upsert outcome. OK is true for a landed write (Hash is
// the hash of the written bytes, Created reports create-vs-update); otherwise
// Kind carries the typed conflict plus a compact Detail — the loser re-runs and
// the winner's bytes are never overwritten.
type BaselineUpsert struct {
	OK      bool
	Hash    string
	Created bool
	Kind    BaselineUpsertConflict
	Detail  string
}

// hashOrAbsent renders the TS `expectedHash ?? 'absent'` / `actualHash ??
// 'absent'` fallback used by the CAS-conflict detail.
func hashOrAbsent(hash *string) string {
	if hash == nil {
		return "absent"
	}
	return *hash
}

// describeWrite ports the TS describeWrite: the compact CAS-conflict detail.
func describeWrite(write state.CasWriteResult) string {
	return fmt.Sprintf(
		"CAS conflict: expected %s, on disk %s — loser re-runs, winner's bytes survive (R15)",
		hashOrAbsent(write.ExpectedHash), hashOrAbsent(write.ActualHash))
}

// BaselineStore is the §6.1-guarded baseline store over a structural backend.
type BaselineStore struct {
	backend BaselineStoreBackend
}

// NewBaselineStore binds the store to a backend (a *state.StateStore, or a
// fake with the identical CAS/registry contract).
func NewBaselineStore(backend BaselineStoreBackend) *BaselineStore {
	return &BaselineStore{backend: backend}
}

// Get returns the stored baseline, or nil when no baseline exists for the doc.
func (s *BaselineStore) Get(docPath string) (*schemas.Baseline, error) {
	read, err := s.backend.ReadBaseline(docPath)
	if err != nil {
		return nil, err
	}
	if read.Exists && read.Content != nil {
		return read.Content, nil
	}
	return nil, nil
}

// Upsert is the §6.1 CAS upsert. The record is keyed by its own `doc_path`
// field; a stored record under the same key that names a DIFFERENT doc is a
// corrupt identity and fails closed (EDOCSYNC_STATE_KEY) instead of being
// overwritten.
func (s *BaselineStore) Upsert(baseline schemas.Baseline, guards BaselineGuards) (BaselineUpsert, error) {
	docPath := baseline.DocPath
	current, err := s.backend.ReadBaseline(docPath)
	if err != nil {
		return BaselineUpsert{}, err
	}
	if !current.Exists || current.Content == nil {
		write, err := s.backend.WriteBaseline(docPath, baseline, nil)
		if err != nil {
			return BaselineUpsert{}, err
		}
		if write.OK {
			return BaselineUpsert{OK: true, Hash: write.Hash, Created: true}, nil
		}
		return BaselineUpsert{OK: false, Kind: ConflictCasConflict, Detail: describeWrite(write)}, nil
	}
	if current.Content.DocPath != docPath {
		return BaselineUpsert{}, state.StateKeyError(
			`baseline file for %q stores a record for %q — refusing to overwrite a different doc (fail closed)`,
			docPath, current.Content.DocPath)
	}
	if current.Content.DocContentHash != guards.ExpectedDocContentHash {
		return BaselineUpsert{
			OK:   false,
			Kind: ConflictDocContentChanged,
			Detail: fmt.Sprintf("stored doc_content_hash %s != run-start %s — re-run claim evaluation (R15)",
				current.Content.DocContentHash, guards.ExpectedDocContentHash),
		}, nil
	}
	if current.Content.BaselineCommit != guards.ExpectedBaselineCommit {
		return BaselineUpsert{
			OK:   false,
			Kind: ConflictBaselineCommitMoved,
			Detail: fmt.Sprintf("stored baseline_commit %s != run-start %s",
				current.Content.BaselineCommit, guards.ExpectedBaselineCommit),
		}, nil
	}
	write, err := s.backend.WriteBaseline(docPath, baseline, &current.Hash)
	if err != nil {
		return BaselineUpsert{}, err
	}
	if write.OK {
		return BaselineUpsert{OK: true, Hash: write.Hash}, nil
	}
	return BaselineUpsert{OK: false, Kind: ConflictCasConflict, Detail: describeWrite(write)}, nil
}
