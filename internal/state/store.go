package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Ported from core/src/state/store.ts: the transaction boundary for all
// `state/` access. One owner of every state file (baselines, claims, docs,
// merge ledgers, run records, audit cursor), each behind a compare-and-swap
// on content hash, each write validated against the closed schema registry
// BEFORE any byte moves, and each idempotent per key (re-runs upsert, never
// duplicate).

// TransitionEntry is an append-only transition entry: a validated
// DOC_STATE_TRANSITION_SCHEMA value (state, at, run_id, reason).
type TransitionEntry = schemas.DocStateTransition

// StateChurnHook is the optional A8 churn instrumentation (P0-T15): the store
// reports every successful state write (conflicts are not written), and every
// observable in the report is a direct emission from this hook — never a
// derived placeholder.
type StateChurnHook interface {
	RecordCreated()
	RecordUpdated()
}

// StateStoreOptions configures a StateStore.
type StateStoreOptions struct {
	StateDir string
	Churn    StateChurnHook
}

// RunRecordUpsert is the keyed-upsert outcome (created/conflict).
type RunRecordUpsert struct {
	OK           bool
	Hash         string
	Created      bool
	ExpectedHash *string
	ActualHash   *string
}

// StateStore is the state transaction boundary.
type StateStore struct {
	stateDir string
	queue    StateLockQueue
	churn    StateChurnHook
}

// NewStateStore builds a store over an ABSOLUTE stateDir. The optional queue
// defaults to CreateInProcessQueue (the TS default parameter).
func NewStateStore(opts StateStoreOptions, queue ...StateLockQueue) (*StateStore, error) {
	if opts.StateDir == "" || !filepath.IsAbs(opts.StateDir) {
		return nil, docutil.New(docutil.CodeInternal,
			fmt.Sprintf(`StateStore.stateDir must be an absolute path (got "%s")`, opts.StateDir))
	}
	q := StateLockQueue(CreateInProcessQueue())
	if len(queue) > 0 && queue[0] != nil {
		q = queue[0]
	}
	return &StateStore{stateDir: opts.StateDir, queue: q, churn: opts.Churn}, nil
}

/* -------------------------------------------------------------- read APIs */

// ReadBaseline reads the verified baseline for a doc (missing ⇒ Exists false).
func (s *StateStore) ReadBaseline(docPath string) (StateRead[*schemas.Baseline], error) {
	relPath, err := BaselineRelPath(docPath)
	if err != nil {
		return StateRead[*schemas.Baseline]{}, err
	}
	return readTyped[*schemas.Baseline](s, relPath, "docsync.baseline")
}

// ReadClaim reads a claim record (missing ⇒ Exists false).
func (s *StateStore) ReadClaim(claimID string) (StateRead[*schemas.Claim], error) {
	relPath, err := ClaimRelPath(claimID)
	if err != nil {
		return StateRead[*schemas.Claim]{}, err
	}
	return readTyped[*schemas.Claim](s, relPath, "docsync.claim")
}

// ListClaimIds returns the claim ids present under state/claims ([] when the
// directory does not exist yet).
func (s *StateStore) ListClaimIds() ([]string, error) {
	stems, err := listStateStems(filepath.Join(s.stateDir, "claims"))
	if err != nil {
		return nil, err
	}
	sort.Strings(stems)
	return stems, nil
}

// ReadDocState reads the lifecycle record for a doc (missing ⇒ Exists false).
func (s *StateStore) ReadDocState(docPath string) (StateRead[*schemas.DocState], error) {
	relPath, err := DocStateRelPath(docPath)
	if err != nil {
		return StateRead[*schemas.DocState]{}, err
	}
	return readTyped[*schemas.DocState](s, relPath, "docsync.doc-state")
}

// ListDocStatePaths returns the doc paths present under state/docs ([] when
// the directory does not exist yet); each stem is decoded back to its
// repo-relative doc path.
func (s *StateStore) ListDocStatePaths() ([]string, error) {
	stems, err := listStateStems(filepath.Join(s.stateDir, "docs"))
	if err != nil {
		return nil, err
	}
	docPaths := make([]string, 0, len(stems))
	for _, stem := range stems {
		docPath, err := DecodeStateDocPath(stem)
		if err != nil {
			return nil, err
		}
		docPaths = append(docPaths, docPath)
	}
	sort.Strings(docPaths)
	return docPaths, nil
}

// ReadMergeLedger reads the merge ledger for a source key (missing ⇒ Exists false).
func (s *StateStore) ReadMergeLedger(sourceKey string) (StateRead[*schemas.MergeLedger], error) {
	relPath, err := DocLedgerRelPath(sourceKey)
	if err != nil {
		return StateRead[*schemas.MergeLedger]{}, err
	}
	return readTyped[*schemas.MergeLedger](s, relPath, "docsync.merge-ledger")
}

// ReadRunRecord reads the run record for a key (missing ⇒ Exists false).
func (s *StateStore) ReadRunRecord(key RunKey) (StateRead[*schemas.RunRecord], error) {
	relPath, err := RunRecordRelPath(key)
	if err != nil {
		return StateRead[*schemas.RunRecord]{}, err
	}
	return readTyped[*schemas.RunRecord](s, relPath, "docsync.run-record")
}

// ReadAuditCursor reads the Loop-B audit cursor (missing ⇒ Exists false).
func (s *StateStore) ReadAuditCursor() (StateRead[*schemas.AuditCursor], error) {
	return readTyped[*schemas.AuditCursor](s, AuditCursorRelPath(), "docsync.audit-cursor")
}

/* ------------------------------------------------------------- write APIs */

// WriteBaseline writes the baseline for one doc (create or CAS update).
func (s *StateStore) WriteBaseline(docPath string, baseline schemas.Baseline, expectedHash *string) (CasWriteResult, error) {
	relPath, err := BaselineRelPath(docPath)
	if err != nil {
		return CasWriteResult{}, err
	}
	return s.writeTyped(relPath, "docsync.baseline", baseline, expectedHash)
}

// WriteClaim writes one claim record (create or CAS update).
func (s *StateStore) WriteClaim(claimID string, claim schemas.Claim, expectedHash *string) (CasWriteResult, error) {
	relPath, err := ClaimRelPath(claimID)
	if err != nil {
		return CasWriteResult{}, err
	}
	return s.writeTyped(relPath, "docsync.claim", claim, expectedHash)
}

// WriteDocState writes the lifecycle record for one doc (create or CAS update).
func (s *StateStore) WriteDocState(docPath string, docState schemas.DocState, expectedHash *string) (CasWriteResult, error) {
	relPath, err := DocStateRelPath(docPath)
	if err != nil {
		return CasWriteResult{}, err
	}
	return s.writeTyped(relPath, "docsync.doc-state", docState, expectedHash)
}

// WriteAuditCursor writes the audit cursor (create or CAS update).
func (s *StateStore) WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (CasWriteResult, error) {
	return s.writeTyped(AuditCursorRelPath(), "docsync.audit-cursor", cursor, expectedHash)
}

// WriteMergeLedger writes the merge ledger for a source key (create or CAS
// update); the record's own source_key must match the write key.
func (s *StateStore) WriteMergeLedger(sourceKey string, ledger schemas.MergeLedger, expectedHash *string) (CasWriteResult, error) {
	if ledger.SourceKey != sourceKey {
		return CasWriteResult{}, StateKeyError(
			`merge ledger source_key %s does not match the write key %s (fail closed)`,
			jsonString(ledger.SourceKey), jsonString(sourceKey))
	}
	relPath, err := DocLedgerRelPath(sourceKey)
	if err != nil {
		return CasWriteResult{}, err
	}
	return s.writeTyped(relPath, "docsync.merge-ledger", ledger, expectedHash)
}

// UpsertRunRecord writes the run record for key: same key ⇒ idempotent upsert
// (the caller-supplied run_id/outcome win), same filename with a DIFFERENT run
// payload ⇒ refused (fail closed).
func (s *StateStore) UpsertRunRecord(key RunKey, record schemas.RunRecord) (RunRecordUpsert, error) {
	relPath, err := RunRecordRelPath(key)
	if err != nil {
		return RunRecordUpsert{}, err
	}
	if err := guardStateWrite(relPath); err != nil {
		return RunRecordUpsert{}, err
	}
	if !SameRunKey(record.Key, key) {
		return RunRecordUpsert{}, StateKeyError(
			`run record key %s does not match the upsert key %s`,
			jsonString(record.Key), jsonString(key))
	}
	schema, err := requireSchema("docsync.run-record")
	if err != nil {
		return RunRecordUpsert{}, err
	}
	parsed, err := parseRegistered(schema, record, relPath)
	if err != nil {
		return RunRecordUpsert{}, err
	}
	raw, err := canonicalBytes(parsed)
	if err != nil {
		return RunRecordUpsert{}, err
	}
	fsPath, err := s.toFsPath(relPath)
	if err != nil {
		return RunRecordUpsert{}, err
	}
	var upsert RunRecordUpsert
	err = s.queue.Run(relPath, func() error {
		existing, err := ReadStateFile[any](fsPath)
		if err != nil {
			return err
		}
		var expectedHash *string
		if existing.Exists {
			existingParsed, err := parseRegistered(schema, existing.Content, relPath)
			if err != nil {
				return err
			}
			existingRecord, ok := existingParsed.(*schemas.RunRecord)
			if !ok {
				return newStateSchemaError(
					fmt.Sprintf(`registry parse for "%s" returned %T, want *schemas.RunRecord`, relPath, existingParsed),
					"type mismatch")
			}
			if !SameRunKey(existingRecord.Key, key) {
				return StateCorruptError(
					`run record "%s" holds key %s — refusing to overwrite a different run (fail closed)`,
					relPath, jsonString(existingRecord.Key))
			}
			hash := existing.Hash
			expectedHash = &hash
		}
		write, err := CasWriteFileDetailed(fsPath, raw, expectedHash)
		if err != nil {
			return err
		}
		if !write.OK {
			upsert = RunRecordUpsert{OK: false, ExpectedHash: write.ExpectedHash, ActualHash: write.ActualHash}
			return nil
		}
		s.recordChurn(write.Created)
		upsert = RunRecordUpsert{OK: true, Hash: write.Hash, Created: write.Created}
		return nil
	})
	if err != nil {
		return RunRecordUpsert{}, err
	}
	return upsert, nil
}

// AppendTransition appends to the existing doc state's transition history
// (append-only: one entry, one CAS update; the doc must exist — a missing
// target refuses with EDOCSYNC_STATE_MISSING).
func (s *StateStore) AppendTransition(docPath string, transition TransitionEntry, expectedHash *string) (CasWriteResult, error) {
	relPath, err := DocStateRelPath(docPath)
	if err != nil {
		return CasWriteResult{}, err
	}
	if err := guardStateWrite(relPath); err != nil {
		return CasWriteResult{}, err
	}
	schema, err := requireSchema("docsync.doc-state")
	if err != nil {
		return CasWriteResult{}, err
	}
	fsPath, err := s.toFsPath(relPath)
	if err != nil {
		return CasWriteResult{}, err
	}
	var write CasWriteDetailed
	err = s.queue.Run(relPath, func() error {
		read, err := ReadStateFile[any](fsPath)
		if err != nil {
			return err
		}
		if !read.Exists {
			return StateMissingError(
				`cannot append transition: doc state "%s" does not exist (append-only log requires an existing record)`,
				relPath)
		}
		currentParsed, err := parseRegistered(schema, read.Content, relPath)
		if err != nil {
			return err
		}
		current, ok := currentParsed.(*schemas.DocState)
		if !ok {
			return newStateSchemaError(
				fmt.Sprintf(`registry parse for "%s" returned %T, want *schemas.DocState`, relPath, currentParsed),
				"type mismatch")
		}
		next := *current
		next.Transitions = append(append([]schemas.DocStateTransition{}, current.Transitions...), transition)
		// The whole next state re-validates against the registry — the new
		// transition included — BEFORE any byte moves.
		if _, err := parseRegistered(schema, &next, relPath); err != nil {
			return err
		}
		raw, err := canonicalBytes(&next)
		if err != nil {
			return err
		}
		written, err := CasWriteFileDetailed(fsPath, raw, expectedHash)
		if err != nil {
			return err
		}
		write = written
		return nil
	})
	if err != nil {
		return CasWriteResult{}, err
	}
	if !write.OK {
		return write.Result(), nil
	}
	s.recordChurn(write.Created)
	return write.Result(), nil
}

// WriteRawForTest writes raw bytes to an arbitrary STATE-OWNED path behind the
// same boundary guard and CAS (the TS test-only interior; the boundary guard
// IS the test subject).
func (s *StateStore) WriteRawForTest(relPath, raw string, expectedHash *string) (CasWriteResult, error) {
	if err := guardStateWrite(relPath); err != nil {
		return CasWriteResult{}, err
	}
	fsPath, err := s.toFsPath(relPath)
	if err != nil {
		return CasWriteResult{}, err
	}
	var write CasWriteDetailed
	err = s.queue.Run(relPath, func() error {
		written, err := CasWriteFileDetailed(fsPath, raw, expectedHash)
		if err != nil {
			return err
		}
		write = written
		return nil
	})
	if err != nil {
		return CasWriteResult{}, err
	}
	return write.Result(), nil
}

/* ------------------------------------------------------- typed read/write */

// readTyped reads and registry-validates one state artifact. Every write is
// validated BEFORE the CAS write; a corrupt or schema-drifted file on disk
// FLOODS the caller — no zero-value record ever comes back as "empty".
// (A generic function, not a method: Go methods cannot declare type
// parameters.)
func readTyped[T any](s *StateStore, relPath, schemaID string) (StateRead[T], error) {
	var empty StateRead[T]
	schema, err := requireSchema(schemaID)
	if err != nil {
		return empty, err
	}
	fsPath, err := s.toFsPath(relPath)
	if err != nil {
		return empty, err
	}
	read, err := ReadStateFile[any](fsPath)
	if err != nil {
		return empty, err
	}
	if !read.Exists {
		return empty, nil
	}
	parsed, err := parseRegistered(schema, read.Content, relPath)
	if err != nil {
		return empty, err
	}
	content, ok := parsed.(T)
	if !ok {
		return empty, newStateSchemaError(
			fmt.Sprintf(`registry parse for "%s" returned %T, want %T`, relPath, parsed, *new(T)),
			"type mismatch")
	}
	return StateRead[T]{Content: content, Raw: read.Raw, Hash: read.Hash, Exists: true}, nil
}

// writeTyped validates a value against the registry, serializes it to
// canonical JSON (the exact bytes CAS hashes and the file carries) and CAS
// writes it, all serialized per relPath.
func (s *StateStore) writeTyped(relPath, schemaID string, value any, expectedHash *string) (CasWriteResult, error) {
	if err := guardStateWrite(relPath); err != nil {
		return CasWriteResult{}, err
	}
	schema, err := requireSchema(schemaID)
	if err != nil {
		return CasWriteResult{}, err
	}
	parsed, err := parseRegistered(schema, value, relPath)
	if err != nil {
		return CasWriteResult{}, err
	}
	raw, err := canonicalBytes(parsed)
	if err != nil {
		return CasWriteResult{}, err
	}
	fsPath, err := s.toFsPath(relPath)
	if err != nil {
		return CasWriteResult{}, err
	}
	var write CasWriteDetailed
	err = s.queue.Run(relPath, func() error {
		written, err := CasWriteFileDetailed(fsPath, raw, expectedHash)
		if err != nil {
			return err
		}
		write = written
		return nil
	})
	if err != nil {
		return CasWriteResult{}, err
	}
	if !write.OK {
		return write.Result(), nil
	}
	s.recordChurn(write.Created)
	return write.Result(), nil
}

// recordChurn feeds a landed write to the hook: created vs updated (a
// conflict is not written, so it never lands here).
func (s *StateStore) recordChurn(created bool) {
	if s.churn == nil {
		return
	}
	if created {
		s.churn.RecordCreated()
		return
	}
	s.churn.RecordUpdated()
}

// toFsPath maps a state relPath into the state dir WITHOUT decoding: the
// relPath is the filename (escapes intact).
func (s *StateStore) toFsPath(relPath string) (string, error) {
	if !strings.HasPrefix(relPath, stateRelPrefix) {
		return "", StateKeyError(`internal invariant violated: "%s" is not a state/ relPath`, relPath)
	}
	return filepath.Join(s.stateDir, filepath.FromSlash(strings.TrimPrefix(relPath, stateRelPrefix))), nil
}

// listStateStems returns the .json stems under a state subdir ([] when the
// directory does not exist yet — the "empty and that is fine" outcome).
func listStateStems(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if isENOENT(err) {
			return nil, nil
		}
		return nil, err
	}
	stems := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, STATE_FILE_SUFFIX) {
			continue
		}
		stems = append(stems, strings.TrimSuffix(name, STATE_FILE_SUFFIX))
	}
	return stems, nil
}

/* ------------------------------------------- registry validation + bytes */

// requireSchema looks one registry entry up (fail closed on a miss).
func requireSchema(id string) (*schemas.RegisteredSchema, error) {
	entry := schemas.GetSchema(id)
	if entry == nil {
		return nil, newStateSchemaError(
			fmt.Sprintf(`schema "%s" is missing from the registry — refusing (fail closed)`, id),
			"registry miss")
	}
	return entry, nil
}

// parseRegistered validates a value against a registry entry, returning the
// parsed value. The Go registry validates wire bytes, so the value is
// marshaled first and parsed strictly (unknown keys rejected — the zod
// strictObject semantics the TS safeParse had).
func parseRegistered(entry *schemas.RegisteredSchema, value any, relPath string) (any, error) {
	wire, err := json.Marshal(value)
	if err != nil {
		return nil, StateSchemaErrorForPath(relPath, err)
	}
	parsed, err := entry.ParseTyped(wire)
	if err != nil {
		return nil, StateSchemaErrorForPath(relPath, err)
	}
	return parsed, nil
}

// canonicalBytes renders a validated value as canonical JSON — the exact
// bytes CAS hashes and state files carry. The value round-trips through
// generic JSON form first (docutil.CanonicalJSON operates on decoded values)
// with UseNumber so integers stay integral.
func canonicalBytes(value any) (string, error) {
	wire, err := json.Marshal(value)
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

// jsonString mirrors the JSON.stringify used by the TS mismatch/corruption
// messages (keys are ASCII-safe there).
func jsonString(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(raw)
}

/* ------------------------------------------------------- write boundary */

// CodeBoundary preserves the TS EDOCSYNC_BOUNDARY code (guards.ts).
const CodeBoundary = "EDOCSYNC_BOUNDARY"

// GuardedPathError is a refused write path (ported from the TS
// GuardedPathError): it carries the code and the refused relPath.
type GuardedPathError struct {
	RelPath string
	coded   *docutil.Error
}

func (e *GuardedPathError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf reports EDOCSYNC_BOUNDARY.
func (e *GuardedPathError) Unwrap() error { return e.coded }

// windowsAbsPath matches the drive-letter absolute form `/^[A-Za-z]:($|\/)/`.
var windowsAbsPath = regexp.MustCompile(`^[A-Za-z]:($|/)`)

// docSyncPrefix is the optional leading `.docsync/` the guard strips.
const docSyncPrefix = schemas.DocSyncDir + "/"

// normalizedPath is the guard's normalization outcome; a non-empty reason
// is a refusal.
type normalizedPath struct {
	path   string
	reason string
}

// normalizeDocsyncPath ports the TS normalizer: the leading `.docsync/`
// prefix is optional and stripped; `..`, absolute paths, backslashes, NUL
// bytes, empty and `.` segments are REFUSED with a reason — never resolved
// away.
func normalizeDocsyncPath(input string) normalizedPath {
	if strings.Contains(input, "\\") {
		return normalizedPath{reason: fmt.Sprintf(
			`backslashes are not allowed in docsync paths ("%s") — use POSIX '/' separators`, input)}
	}
	if strings.HasPrefix(input, "/") || windowsAbsPath.MatchString(input) {
		return normalizedPath{reason: fmt.Sprintf(
			`absolute paths are not allowed ("%s") — use a path relative to .docsync/`, input)}
	}
	if strings.Contains(input, "\x00") {
		return normalizedPath{reason: fmt.Sprintf(
			`NUL bytes are not allowed in docsync paths ("%s")`, input)}
	}
	stripped := input
	if strings.HasPrefix(stripped, docSyncPrefix) {
		stripped = stripped[len(docSyncPrefix):]
	}
	if len(stripped) == 0 {
		return normalizedPath{reason: fmt.Sprintf(
			`path is empty ("%s") — expected a file path inside .docsync/`, input)}
	}
	segments := strings.Split(stripped, "/")
	for _, segment := range segments {
		if segment == ".." {
			return normalizedPath{reason: fmt.Sprintf(`path traversal ('..') is not allowed ("%s")`, input)}
		}
	}
	for _, segment := range segments {
		if len(segment) == 0 {
			return normalizedPath{reason: fmt.Sprintf(`empty path segments are not allowed ("%s")`, input)}
		}
	}
	for _, segment := range segments {
		if segment == "." {
			return normalizedPath{reason: fmt.Sprintf(
				`'.' segments are not allowed ("%s") — use a direct path inside .docsync/`, input)}
		}
	}
	return normalizedPath{path: stripped}
}

// docsyncPathClass classifies a normalized path.
type docsyncPathClass int

const (
	pathClassUnknown docsyncPathClass = iota
	pathClassConfig
	pathClassState
)

// classifyDocsyncPath mirrors the TS classifier: config files and
// non-state top-level dirs (evaluation/, memory/, policy-history/) are
// config; `state/**` is state; everything else is unknown.
func classifyDocsyncPath(path string) docsyncPathClass {
	for _, file := range schemas.ConfigFiles {
		if path == file {
			return pathClassConfig
		}
	}
	for _, dir := range schemas.TopLevelDirs {
		if dir == "state" {
			continue
		}
		if strings.HasPrefix(path, dir+"/") {
			return pathClassConfig
		}
	}
	if strings.HasPrefix(path, stateRelPrefix) {
		return pathClassState
	}
	return pathClassUnknown
}

// guardStateWrite refuses anything that is not `state/**` (fail closed,
// invariant 3, V-P0-13).
//
// NOTE: this is a MINIMAL LOCAL port of internal/boundaries' guardStateWrite
// (normalization + classification, same refusal messages and the same
// EDOCSYNC_BOUNDARY code). internal/boundaries is not ported yet; when it
// lands, replace this guard with it and delete GuardedPathError here.
func guardStateWrite(relPath string) error {
	refuse := func(reason string) error {
		return &GuardedPathError{
			RelPath: relPath,
			coded: docutil.New(CodeBoundary,
				fmt.Sprintf(`state write refused for "%s": %s`, relPath, reason)),
		}
	}
	normalized := normalizeDocsyncPath(relPath)
	if normalized.reason != "" {
		return refuse(normalized.reason)
	}
	switch classifyDocsyncPath(normalized.path) {
	case pathClassState:
		return nil
	case pathClassConfig:
		return refuse(fmt.Sprintf(
			`state writers must not touch human-owned config; "%s" (from "%s") is config-owned — edit via a reviewable PR`,
			normalized.path, relPath))
	default:
		return refuse(fmt.Sprintf(
			`"%s" is outside the .docsync/ ownership model (config files, evaluation/, memory/, policy-history/, state/); refusing both config and state writes for "%s" (fail closed)`,
			normalized.path, relPath))
	}
}
