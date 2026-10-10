package provenance

// Ported from core/src/provenance/store.ts (P9-T04; ARCH §6.3 + §12.5; D5
// storage precedence). Archival surface of the provenance record:
// content-addressed by run id, APPEND-ONLY, CAS-written under `.docsync/state/`.
//
// OD-R44 decision (no new state subdirs — `STATE_SUBDIRS` stays `baselines,
// claims, docs, runs`): the records live under the EXISTING `runs/` subdir with
// a distinct filename convention, `runs/prov-<run_id>.json` (layout
// `validate.ts` treats anything deeper than `state/<canonical-subdir>` as
// free-form system-written content, so no layout change is needed). Adding a
// `provenance/` subdir was rejected: it would widen the canonical tree and trip
// the `state/ must contain only the canonical state entries` invariant.
//
// Write semantics (fail closed at every step, in order):
//  1. run-id sanity (`StateKeyError`) + guard (`state/**` only, V-P0-13);
//  2. schema validation against the registered `docsync.provenance/v1`
//     (`StateSchemaError` on drift, OD-R03);
//  3. write-path secret scan (`AssertNoSecrets` — a record that is not a clean
//     evidence container never touches disk, ARCH §12.5);
//  4. canonical serialization (`canonicalJson` — stable bytes, stable CAS
//     hashes);
//  5. append-only arbitration under the per-target queue:
//     - absent → CAS create;
//     - present + byte-identical → NO-OP (re-running the same run is a no-op,
//       never a rewrite — V-P9-3);
//     - present + different → typed `ProvenanceAppendError`: a history-rewrite
//       attempt is a bug-class refusal, the original bytes stay intact.
//
// There is no delete/update API by design (ARCH §6.3: state is append/no-op
// only, nothing is deleted or rewritten).

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/boundaries"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// CodeProvenanceRewrite is the closest registered docutil code for an
// append-only violation (CONVENTIONS.md §Errors; the TS class code
// EDOCSYNC_PROVENANCE_REWRITE is not in the code list — class identity is
// preserved by the ProvenanceAppendError TYPE).
const CodeProvenanceRewrite = docutil.CodeState

// ProvenanceRecordPrefix is the filename convention prefix under the existing
// `runs/` subdir (OD-R44 note).
const ProvenanceRecordPrefix = "prov-"

const (
	stateRelPrefix     = "state/"
	runsSubdir         = "runs"
	fileSuffix         = ".json"
	provenanceSchemaID = "docsync.provenance"
)

// ProvenanceAppendError is the append-only violation class port: a rewrite
// attempt for a run that is already archived with different bytes.
type ProvenanceAppendError struct {
	// RunID is the archived run the caller tried to rewrite.
	RunID string
	// Detail is the same one-line reason the TS constructor received.
	Detail string
	coded  *docutil.Error
}

func (e *ProvenanceAppendError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *ProvenanceAppendError) Unwrap() error { return e.coded }

// Code returns the coded-error code for this refusal (EDOCSYNC_STATE).
func (e *ProvenanceAppendError) Code() string { return CodeProvenanceRewrite }

// NewProvenanceAppendError builds the typed append-only refusal (the TS
// constructor message text, preserved verbatim).
func NewProvenanceAppendError(runID, detail string) *ProvenanceAppendError {
	message := `provenance record for run "` + runID + `" already exists with DIFFERENT content — ` +
		`append-only archival refuses history rewrites (fail closed): ` + detail
	return &ProvenanceAppendError{RunID: runID, Detail: detail, coded: docutil.New(CodeProvenanceRewrite, message)}
}

// ProvenanceWriteResult is the value shape of the TS union
// `{ok:true, hash, created, replay} | CasWriteConflict`: OK is the union tag,
// Replay is the byte-identical replay no-op, and the conflict pair rides the
// same struct the way the TS union's second member did.
type ProvenanceWriteResult struct {
	OK           bool
	Hash         string
	Created      bool
	Replay       bool
	ExpectedHash *string
	ActualHash   *string
}

// ProvenanceStoreOptions configures a ProvenanceStore.
type ProvenanceStoreOptions struct {
	// StateDir is the ABSOLUTE path to `.docsync/state` (D1).
	StateDir string
}

// ProvenanceStore is the append-only, content-addressed provenance archive.
type ProvenanceStore struct {
	stateDir string
	queue    state.StateLockQueue
}

// NewProvenanceStore builds a store over an ABSOLUTE stateDir. The optional
// queue defaults to CreateInProcessQueue (the TS default parameter).
func NewProvenanceStore(opts ProvenanceStoreOptions, queue ...state.StateLockQueue) (*ProvenanceStore, error) {
	if opts.StateDir == "" || !filepath.IsAbs(opts.StateDir) {
		return nil, docutil.New(docutil.CodeInternal,
			"ProvenanceStore.stateDir must be an absolute path (got \""+opts.StateDir+"\")")
	}
	q := state.CreateInProcessQueue()
	if len(queue) > 0 && queue[0] != nil {
		q = queue[0]
	}
	return &ProvenanceStore{stateDir: opts.StateDir, queue: q}, nil
}

// ProvenanceRecordRelPath returns `state/runs/prov-<runId>.json` — the
// OD-R44-compliant archival location. The run id is validated filename-safe
// first, so a caller can never construct a traversal path through it.
func ProvenanceRecordRelPath(runID string) (string, error) {
	if err := assertProvenanceRunID(runID); err != nil {
		return "", err
	}
	return stateRelPrefix + runsSubdir + "/" + ProvenanceRecordPrefix + runID + fileSuffix, nil
}

// RecordFSPath returns the absolute archival path for a run id (validated
// filename-safe first). It is the Go twin of the TS suite reaching into the
// store's private `stateDir` to assert archival bytes.
func (s *ProvenanceStore) RecordFSPath(runID string) (string, error) {
	relPath, err := ProvenanceRecordRelPath(runID)
	if err != nil {
		return "", err
	}
	return s.toFsPath(relPath)
}

// assertProvenanceRunID is the run-id sanity check (StateKeyError).
func assertProvenanceRunID(runID string) error {
	if runID == "" {
		return state.StateKeyError("provenance run id is empty")
	}
	if !schemas.IsProvenanceRunID(runID) {
		return state.StateKeyError(
			`provenance run id "%s" is not filename-safe — refusing (fail closed)`, runID)
	}
	return nil
}

// assertGuarded guards the write path (`state/**` only, V-P0-13) through the
// ported boundary guard, whose refusal message matches the TS GuardedPathError.
func assertGuarded(relPath string) error {
	return boundaries.AssertStateWrite(relPath)
}

// Write is the append-only archival write (see module doc). The write key IS
// `record.RunID` (single-argument API): it is validated filename-safe
// (`StateKeyError` otherwise, same discipline as the run-record store) and keys
// the `prov-<run_id>.json` location directly, so key/record divergence is
// unconstructable by design.
func (s *ProvenanceStore) Write(record schemas.Provenance) (ProvenanceWriteResult, error) {
	if err := assertProvenanceRunID(record.RunID); err != nil {
		return ProvenanceWriteResult{}, err
	}
	relPath, err := ProvenanceRecordRelPath(record.RunID)
	if err != nil {
		return ProvenanceWriteResult{}, err
	}
	if err := assertGuarded(relPath); err != nil {
		return ProvenanceWriteResult{}, err
	}
	parsed, err := parseProvenanceForPath(record, relPath)
	if err != nil {
		return ProvenanceWriteResult{}, err
	}
	if err := AssertNoSecrets(*parsed); err != nil {
		return ProvenanceWriteResult{}, err
	}
	raw, err := canonicalRecordBytes(parsed)
	if err != nil {
		return ProvenanceWriteResult{}, err
	}
	fsPath, err := s.toFsPath(relPath)
	if err != nil {
		return ProvenanceWriteResult{}, err
	}

	var result ProvenanceWriteResult
	err = s.queue.Run(relPath, func() error {
		existing, err := state.ReadStateFile[any](fsPath)
		if err != nil {
			return err
		}
		if existing.Exists {
			if existing.Raw == raw && existing.Hash != "" {
				// Re-running the same run is a NO-OP, never a rewrite (V-P9-3).
				result = ProvenanceWriteResult{OK: true, Hash: existing.Hash, Created: false, Replay: true}
				return nil
			}
			// Validate-then-refuse: the existing record is still schema-checked
			// so a corrupt on-disk file reports as corrupt, not as a rewrite.
			if _, err := schemas.ParseProvenance([]byte(existing.Raw)); err != nil {
				return state.StateSchemaErrorForPath(relPath,
					errors.New("existing record fails docsync.provenance/v1"))
			}
			return NewProvenanceAppendError(record.RunID,
				"existing sha256 "+existing.Hash+" differs from the incoming bytes")
		}
		write, err := state.CasWriteFileDetailed(fsPath, raw, nil)
		if err != nil {
			return err
		}
		if !write.OK {
			result = ProvenanceWriteResult{
				OK:           false,
				ExpectedHash: write.ExpectedHash,
				ActualHash:   write.ActualHash,
			}
			return nil
		}
		result = ProvenanceWriteResult{OK: true, Hash: write.Hash, Created: true, Replay: false}
		return nil
	})
	if err != nil {
		return ProvenanceWriteResult{}, err
	}
	return result, nil
}

// Read reads one record; absent run ids read as `Exists: false` (soft).
func (s *ProvenanceStore) Read(runID string) (state.StateRead[*schemas.Provenance], error) {
	if err := assertProvenanceRunID(runID); err != nil {
		return state.StateRead[*schemas.Provenance]{}, err
	}
	relPath, err := ProvenanceRecordRelPath(runID)
	if err != nil {
		return state.StateRead[*schemas.Provenance]{}, err
	}
	fsPath, err := s.toFsPath(relPath)
	if err != nil {
		return state.StateRead[*schemas.Provenance]{}, err
	}
	read, err := state.ReadStateFile[any](fsPath)
	if err != nil {
		return state.StateRead[*schemas.Provenance]{}, err
	}
	if !read.Exists {
		return state.StateRead[*schemas.Provenance]{}, nil
	}
	parsed, err := schemas.ParseProvenance([]byte(read.Raw))
	if err != nil {
		return state.StateRead[*schemas.Provenance]{}, state.StateSchemaErrorForPath(relPath, err)
	}
	return state.StateRead[*schemas.Provenance]{
		Content: parsed,
		Raw:     read.Raw,
		Hash:    read.Hash,
		Exists:  true,
	}, nil
}

// List returns the run ids present (from `prov-<id>.json` filename stems),
// sorted deterministically. A missing `runs/` directory is the soft "nothing
// there" outcome; other fs errors propagate (fail closed).
func (s *ProvenanceStore) List() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.stateDir, runsSubdir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ProvenanceRecordPrefix) || !strings.HasSuffix(name, fileSuffix) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(name, ProvenanceRecordPrefix), fileSuffix))
	}
	sort.Strings(ids)
	return ids, nil
}

// toFsPath maps a state relPath into the state dir. It rejects anything that is
// not a `state/` relPath — the same internal invariant the state store enforces.
func (s *ProvenanceStore) toFsPath(relPath string) (string, error) {
	if !strings.HasPrefix(relPath, stateRelPrefix) {
		return "", state.StateKeyError(
			`internal invariant violated: "%s" is not a state/ relPath`, relPath)
	}
	return filepath.Join(s.stateDir, filepath.FromSlash(strings.TrimPrefix(relPath, stateRelPrefix))), nil
}

/* ------------------------------------------- registry validation + bytes */

// parseProvenanceForPath validates a record against the registered
// `docsync.provenance/v1` BEFORE any byte moves (OD-R03), returning the parsed
// value. The Go registry validates wire bytes, so the record is marshaled
// first and parsed strictly (unknown keys rejected — the zod strictObject
// semantics the TS safeParse had).
func parseProvenanceForPath(record schemas.Provenance, relPath string) (*schemas.Provenance, error) {
	entry := schemas.GetSchema(provenanceSchemaID)
	if entry == nil {
		return nil, state.StateSchemaErrorForPath(relPath, errors.New(
			`schema "`+provenanceSchemaID+`" is missing from the registry — refusing (fail closed)`))
	}
	wire, err := json.Marshal(record)
	if err != nil {
		return nil, state.StateSchemaErrorForPath(relPath, err)
	}
	parsed, err := entry.ParseTyped(wire)
	if err != nil {
		return nil, state.StateSchemaErrorForPath(relPath, err)
	}
	parsedRecord, ok := parsed.(*schemas.Provenance)
	if !ok {
		return nil, state.StateSchemaErrorForPath(relPath, errors.New(
			`registry parse for "`+relPath+`" returned the wrong record type (fail closed)`))
	}
	return parsedRecord, nil
}

// canonicalRecordBytes renders a validated record as canonical JSON — the
// exact bytes CAS hashes and state files carry. The value round-trips through
// generic JSON form first (docutil.CanonicalJSON operates on decoded values)
// with UseNumber so integers stay integral.
func canonicalRecordBytes(record *schemas.Provenance) (string, error) {
	wire, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(wire))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	return docutil.CanonicalJSON(generic)
}
