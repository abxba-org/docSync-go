package testkit

// Ported from packages/core/src/testing/fakes/{state-fake,git-platform-fake,types}.ts
// (P0-T17) plus the keyed model-stage doubles the Phase 5+ Go suites need
// (the TS suites bound recorded replay transports at the OpenRouter seam —
// internal/openrouter/testing-transports.go already owns the wire-level
// double — so this file owns the STAGE-level doubles instead).
//
// Three double families, all deterministic and clock-free:
//
//   - InMemoryStateStore — the CAS/registry/guard CONTRACT copy of the real
//     state store (no fs, no directories, no I/O), structurally substitutable
//     for *state.StateStore everywhere a backend is accepted;
//   - GitPlatformFake — the recording GitPlatform stub (ARCH §12.2), whose
//     script is KEYED BY REQUEST IDENTITY instead of the TS per-method FIFO
//     queues: completion-order consumption cross-wires fixtures under
//     concurrent load (AGENT-behavior.md replay rule), so every scripted
//     response names the request identity it answers and an unexpected
//     identity fails loud;
//   - the keyed model-stage doubles (decision / writer / verification /
//     policy / early-signal) that wire onto the orchestrator port bundle via
//     PipelineFakes.Ports().
//
// Every call is recorded with a monotonic, process-local counter (`at`) —
// deliberately NOT a clock value, so assertions are deterministic.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/boundaries"
	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/statemachine"
)

/* ------------------------------------------- shared registry + byte helpers */

// requireSchemaEntry looks one registry entry up (fail closed on a miss —
// OD-R03). A miss is a closed-set programming error: the ids below are the
// registry's fixed state-artifact set, so the TS "schema missing" message is
// unreachable in Go (state.newStateSchemaError is package-private); the miss
// still fails closed as a *state.StateSchemaError.
func requireSchemaEntry(schemaID string) (*schemas.RegisteredSchema, error) {
	entry := schemas.GetSchema(schemaID)
	if entry == nil {
		return nil, state.StateSchemaErrorForPath(schemaID,
			fmt.Errorf(`schema %q is missing from the registry — refusing (fail closed)`, schemaID))
	}
	return entry, nil
}

// parseRegisteredEntry validates a value against a registry entry, returning
// the parsed value (TS parseRegistered). The Go registry validates wire
// bytes, so the value is marshaled first and parsed strictly (unknown keys
// rejected — the zod strictObject semantics the TS safeParse had).
func parseRegisteredEntry(entry *schemas.RegisteredSchema, value any, relPath string) (any, error) {
	wire, err := json.Marshal(value)
	if err != nil {
		return nil, state.StateSchemaErrorForPath(relPath, err)
	}
	parsed, err := entry.ParseTyped(wire)
	if err != nil {
		return nil, state.StateSchemaErrorForPath(relPath, err)
	}
	return parsed, nil
}

// canonicalStateBytes renders a validated value as canonical JSON — the exact
// bytes CAS hashes and state files carry (TS canonicalJson on the parsed
// record). The value round-trips through generic JSON form with UseNumber so
// integers stay integral.
func canonicalStateBytes(value any) (string, error) {
	wire, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return canonicalizeGeneric(wire)
}

// canonicalizeGeneric canonicalizes already-marshaled JSON bytes: decode into
// generic form (json.Number for numbers) then apply the ONE canonical
// serializer, exactly like state.store.go's canonicalBytes.
func canonicalizeGeneric(wire []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(wire))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	return docutil.CanonicalJSON(generic)
}

// parseRawStateValue reads raw state bytes; invalid JSON fails closed
// (TS parseRaw — StateCorruptError, never "treated as empty").
func parseRawStateValue(raw, relPath string) (any, error) {
	generic, err := parseJSONValue(raw)
	if err != nil {
		return nil, state.StateCorruptError(
			`state record "%s" is not valid JSON — refusing to read (fail closed)`, relPath)
	}
	return generic, nil
}

/* ------------------------------------------------- the in-memory state fake */

// State store schema ids (the closed state-artifact set, OD-R03).
const (
	schemaBaseline    = "docsync.baseline"
	schemaClaim       = "docsync.claim"
	schemaDocState    = "docsync.doc-state"
	schemaRunRecord   = "docsync.run-record"
	schemaAuditCursor = "docsync.audit-cursor"
	schemaMergeLedger = "docsync.merge-ledger"
)

// InMemoryStateStoreOptions configures the fake (TS
// InMemoryStateStoreOptions).
type InMemoryStateStoreOptions struct {
	// Churn is the optional A8 churn hook (mirrors StateStoreOptions.Churn).
	Churn state.StateChurnHook
}

// StateStoreSurface is the public surface of the real StateStore, captured so
// fakes and real stores stay type-compatible (TS StateStoreSurface; the
// compile-time parity check lives in testkit_test.go). Structural typing, no
// inheritance — the real store is NOT imported for its behavior.
type StateStoreSurface interface {
	ReadBaseline(docPath string) (state.StateRead[*schemas.Baseline], error)
	ReadClaim(claimID string) (state.StateRead[*schemas.Claim], error)
	ListClaimIds() ([]string, error)
	ReadDocState(docPath string) (state.StateRead[*schemas.DocState], error)
	ListDocStatePaths() ([]string, error)
	ReadMergeLedger(sourceKey string) (state.StateRead[*schemas.MergeLedger], error)
	ReadRunRecord(key schemas.RunKey) (state.StateRead[*schemas.RunRecord], error)
	ReadAuditCursor() (state.StateRead[*schemas.AuditCursor], error)
	WriteBaseline(docPath string, baseline schemas.Baseline, expectedHash *string) (state.CasWriteResult, error)
	WriteClaim(claimID string, claim schemas.Claim, expectedHash *string) (state.CasWriteResult, error)
	WriteDocState(docPath string, docState schemas.DocState, expectedHash *string) (state.CasWriteResult, error)
	WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error)
	WriteMergeLedger(sourceKey string, ledger schemas.MergeLedger, expectedHash *string) (state.CasWriteResult, error)
	UpsertRunRecord(key schemas.RunKey, record schemas.RunRecord) (state.RunRecordUpsert, error)
	AppendTransition(docPath string, transition schemas.DocStateTransition, expectedHash *string) (state.CasWriteResult, error)
	WriteRawForTest(relPath, raw string, expectedHash *string) (state.CasWriteResult, error)
}

// Compile-time substitutability: the fake satisfies every backend the real
// store satisfies (baselines, claims, statemachine) plus the surface itself.
var (
	_ StateStoreSurface                   = (*InMemoryStateStore)(nil)
	_ baselines.BaselineStoreBackend      = (*InMemoryStateStore)(nil)
	_ claims.ClaimStoreBackend            = (*InMemoryStateStore)(nil)
	_ statemachine.DocStateMachineBackend = (*InMemoryStateStore)(nil)
)

// InMemoryStateStore is the in-memory implementation of the state store
// contract (TS InMemoryStateStore): identical method names and signatures,
// identical CAS semantics, identical registry validation and identical
// canonical bytes. The map keys are `state/`-prefixed relPaths — exactly the
// relPaths the real store maps onto `<stateDir>/**` (the fake drops the fs
// layer, not the path discipline).
//
// Read-modify-write sequences run synchronously inside each method under one
// mutex, so they are atomic within a process exactly like the real store's
// per-target queue.
type InMemoryStateStore struct {
	mu    sync.Mutex
	files map[string]string
	churn state.StateChurnHook
}

// NewInMemoryStateStore builds the fake (TS createInMemoryStateStore).
func NewInMemoryStateStore(options InMemoryStateStoreOptions) *InMemoryStateStore {
	return &InMemoryStateStore{
		files: map[string]string{},
		churn: options.Churn,
	}
}

// CreateInMemoryStateStore is the TS-named factory alias.
func CreateInMemoryStateStore(options InMemoryStateStoreOptions) *InMemoryStateStore {
	return NewInMemoryStateStore(options)
}

/* ------------------------------------------------------------------- reads */

// ReadBaseline reads the verified baseline for a doc (missing ⇒ Exists false).
func (s *InMemoryStateStore) ReadBaseline(docPath string) (state.StateRead[*schemas.Baseline], error) {
	relPath, err := state.BaselineRelPath(docPath)
	if err != nil {
		return state.StateRead[*schemas.Baseline]{}, err
	}
	return readStateTyped[*schemas.Baseline](s, relPath, schemaBaseline)
}

// ReadClaim reads a claim record (missing ⇒ Exists false).
func (s *InMemoryStateStore) ReadClaim(claimID string) (state.StateRead[*schemas.Claim], error) {
	relPath, err := state.ClaimRelPath(claimID)
	if err != nil {
		return state.StateRead[*schemas.Claim]{}, err
	}
	return readStateTyped[*schemas.Claim](s, relPath, schemaClaim)
}

// ListClaimIds mirrors the real store's read-only `state/claims/` scan.
func (s *InMemoryStateStore) ListClaimIds() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listStateStemIDs(s.files, "state/claims/", state.STATE_FILE_SUFFIX, nil)
}

// ReadDocState reads the lifecycle record for a doc (missing ⇒ Exists false).
func (s *InMemoryStateStore) ReadDocState(docPath string) (state.StateRead[*schemas.DocState], error) {
	relPath, err := state.DocStateRelPath(docPath)
	if err != nil {
		return state.StateRead[*schemas.DocState]{}, err
	}
	return readStateTyped[*schemas.DocState](s, relPath, schemaDocState)
}

// ListDocStatePaths mirrors the real store's read-only `state/docs/` scan
// (each stem decoded back to its repo-relative doc path).
func (s *InMemoryStateStore) ListDocStatePaths() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listStateStemIDs(s.files, "state/docs/", state.STATE_FILE_SUFFIX, state.DecodeStateDocPath)
}

// ReadMergeLedger reads the merge ledger for a source key (missing ⇒ Exists false).
func (s *InMemoryStateStore) ReadMergeLedger(sourceKey string) (state.StateRead[*schemas.MergeLedger], error) {
	relPath, err := state.DocLedgerRelPath(sourceKey)
	if err != nil {
		return state.StateRead[*schemas.MergeLedger]{}, err
	}
	return readStateTyped[*schemas.MergeLedger](s, relPath, schemaMergeLedger)
}

// ReadRunRecord reads the run record for a key (missing ⇒ Exists false).
func (s *InMemoryStateStore) ReadRunRecord(key schemas.RunKey) (state.StateRead[*schemas.RunRecord], error) {
	relPath, err := state.RunRecordRelPath(key)
	if err != nil {
		return state.StateRead[*schemas.RunRecord]{}, err
	}
	return readStateTyped[*schemas.RunRecord](s, relPath, schemaRunRecord)
}

// ReadAuditCursor reads the Loop-B audit cursor (missing ⇒ Exists false).
func (s *InMemoryStateStore) ReadAuditCursor() (state.StateRead[*schemas.AuditCursor], error) {
	return readStateTyped[*schemas.AuditCursor](s, state.AuditCursorRelPath(), schemaAuditCursor)
}

/* ------------------------------------------------------------------ writes */

// WriteBaseline writes the baseline for one doc (create or CAS update).
func (s *InMemoryStateStore) WriteBaseline(docPath string, baseline schemas.Baseline, expectedHash *string) (state.CasWriteResult, error) {
	relPath, err := state.BaselineRelPath(docPath)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	return s.writeTyped(relPath, schemaBaseline, baseline, expectedHash)
}

// WriteClaim writes one claim record (create or CAS update).
func (s *InMemoryStateStore) WriteClaim(claimID string, claim schemas.Claim, expectedHash *string) (state.CasWriteResult, error) {
	relPath, err := state.ClaimRelPath(claimID)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	return s.writeTyped(relPath, schemaClaim, claim, expectedHash)
}

// WriteDocState writes the lifecycle record for one doc (create or CAS update).
func (s *InMemoryStateStore) WriteDocState(docPath string, docState schemas.DocState, expectedHash *string) (state.CasWriteResult, error) {
	relPath, err := state.DocStateRelPath(docPath)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	return s.writeTyped(relPath, schemaDocState, docState, expectedHash)
}

// WriteAuditCursor writes the audit cursor (create or CAS update).
func (s *InMemoryStateStore) WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error) {
	return s.writeTyped(state.AuditCursorRelPath(), schemaAuditCursor, cursor, expectedHash)
}

// WriteMergeLedger writes the merge ledger for a source key (create or CAS
// update); the record's own source_key must match the write key.
func (s *InMemoryStateStore) WriteMergeLedger(sourceKey string, ledger schemas.MergeLedger, expectedHash *string) (state.CasWriteResult, error) {
	if ledger.SourceKey != sourceKey {
		return state.CasWriteResult{}, state.StateKeyError(
			`merge ledger source_key %s does not match the write key %s (fail closed)`,
			jsonString(ledger.SourceKey), jsonString(sourceKey))
	}
	relPath, err := state.DocLedgerRelPath(sourceKey)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	return s.writeTyped(relPath, schemaMergeLedger, ledger, expectedHash)
}

// UpsertRunRecord writes the run record for key: same key ⇒ idempotent upsert
// (the caller-supplied body wins), same filename with a DIFFERENT run payload
// ⇒ refused (fail closed).
func (s *InMemoryStateStore) UpsertRunRecord(key schemas.RunKey, record schemas.RunRecord) (state.RunRecordUpsert, error) {
	relPath, err := state.RunRecordRelPath(key)
	if err != nil {
		return state.RunRecordUpsert{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := assertStateWriteGuarded(relPath); err != nil {
		return state.RunRecordUpsert{}, err
	}
	if err := assertStateRelPath(relPath); err != nil {
		return state.RunRecordUpsert{}, err
	}
	if !state.SameRunKey(record.Key, key) {
		return state.RunRecordUpsert{}, state.StateKeyError(
			`run record key %s does not match the upsert key %s`,
			jsonString(record.Key), jsonString(key))
	}
	entry, err := requireSchemaEntry(schemaRunRecord)
	if err != nil {
		return state.RunRecordUpsert{}, err
	}
	parsed, err := parseRegisteredEntry(entry, record, relPath)
	if err != nil {
		return state.RunRecordUpsert{}, err
	}
	raw, err := canonicalStateBytes(parsed)
	if err != nil {
		return state.RunRecordUpsert{}, err
	}
	var expectedHash *string
	if existing, ok := s.files[relPath]; ok {
		existingValue, parseErr := parseRawStateValue(existing, relPath)
		if parseErr != nil {
			return state.RunRecordUpsert{}, parseErr
		}
		existingParsed, err := parseRegisteredEntry(entry, existingValue, relPath)
		if err != nil {
			return state.RunRecordUpsert{}, err
		}
		existingRecord, typeOK := existingParsed.(*schemas.RunRecord)
		if !typeOK {
			return state.RunRecordUpsert{}, newFakeSchemaError(relPath, existingParsed)
		}
		if !state.SameRunKey(existingRecord.Key, key) {
			return state.RunRecordUpsert{}, state.StateCorruptError(
				`run record "%s" holds key %s — refusing to overwrite a different run (fail closed)`,
				relPath, jsonString(existingRecord.Key))
		}
		hash := docutil.ContentHash(existing)
		expectedHash = &hash
	}
	write := s.casWrite(relPath, raw, expectedHash)
	if !write.OK {
		return state.RunRecordUpsert{OK: false, ExpectedHash: write.ExpectedHash, ActualHash: write.ActualHash}, nil
	}
	return state.RunRecordUpsert{OK: true, Hash: write.Hash, Created: write.Created}, nil
}

// AppendTransition appends to the existing doc state's transition history
// (append-only: one entry, one CAS update; the doc must exist — a missing
// target refuses with EDOCSYNC_STATE_MISSING).
func (s *InMemoryStateStore) AppendTransition(docPath string, transition schemas.DocStateTransition, expectedHash *string) (state.CasWriteResult, error) {
	relPath, err := state.DocStateRelPath(docPath)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := assertStateWriteGuarded(relPath); err != nil {
		return state.CasWriteResult{}, err
	}
	if err := assertStateRelPath(relPath); err != nil {
		return state.CasWriteResult{}, err
	}
	entry, err := requireSchemaEntry(schemaDocState)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	// The transition itself is validated as part of the whole next state
	// below — the doc-state registry entry validates one doc-state record, not
	// a bare transition (the same order as the real store).
	existing, ok := s.files[relPath]
	if !ok {
		return state.CasWriteResult{}, state.StateMissingError(
			`cannot append transition: doc state "%s" does not exist (append-only log requires an existing record)`,
			relPath)
	}
	currentValue, parseErr := parseRawStateValue(existing, relPath)
	if parseErr != nil {
		return state.CasWriteResult{}, parseErr
	}
	parsed, err := parseRegisteredEntry(entry, currentValue, relPath)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	current, typeOK := parsed.(*schemas.DocState)
	if !typeOK {
		return state.CasWriteResult{}, newFakeSchemaError(relPath, parsed)
	}
	next := *current
	next.Transitions = append(append([]schemas.DocStateTransition{}, current.Transitions...), transition)
	// The whole next state re-validates — the new transition included —
	// BEFORE any byte moves.
	if _, err := parseRegisteredEntry(entry, &next, relPath); err != nil {
		return state.CasWriteResult{}, err
	}
	raw, err := canonicalStateBytes(&next)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	write := s.casWrite(relPath, raw, expectedHash)
	if !write.OK {
		return write.Result(), nil
	}
	return state.CasWriteResult{OK: true, Hash: write.Hash}, nil
}

// WriteRawForTest is the test-only raw write hook: no schema validation
// (arbitrary bytes) but the same guard + CAS + path discipline as the real
// store's raw path.
func (s *InMemoryStateStore) WriteRawForTest(relPath, raw string, expectedHash *string) (state.CasWriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Same order as the real store: the guard decides FIRST (fail closed on
	// config/unknown), then the state/ relPath invariant.
	if err := assertStateWriteGuarded(relPath); err != nil {
		return state.CasWriteResult{}, err
	}
	if err := assertStateRelPath(relPath); err != nil {
		return state.CasWriteResult{}, err
	}
	write := s.casWrite(relPath, raw, expectedHash)
	if !write.OK {
		return write.Result(), nil
	}
	return state.CasWriteResult{OK: true, Hash: write.Hash}, nil
}

/* ---------------------------------------------------------------- internal */

// readStateTyped reads and registry-validates one state artifact. Real reads
// validate the relPath invariant (toFsPath) but never guard (TS readTyped).
// It is a free function because Go methods cannot carry type parameters.
func readStateTyped[T any](s *InMemoryStateStore, relPath, schemaID string) (state.StateRead[T], error) {
	var empty state.StateRead[T]
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := assertStateRelPath(relPath); err != nil {
		return empty, err
	}
	entry, err := requireSchemaEntry(schemaID)
	if err != nil {
		return empty, err
	}
	raw, ok := s.files[relPath]
	if !ok {
		return empty, nil
	}
	existingValue, parseErr := parseRawStateValue(raw, relPath)
	if parseErr != nil {
		return empty, parseErr
	}
	parsed, err := parseRegisteredEntry(entry, existingValue, relPath)
	if err != nil {
		return empty, err
	}
	content, typeOK := parsed.(T)
	if !typeOK {
		return empty, newFakeSchemaError(relPath, parsed)
	}
	return state.StateRead[T]{Content: content, Raw: raw, Hash: docutil.ContentHash(raw), Exists: true}, nil
}

// writeTyped validates a value against the registry, serializes it to
// canonical JSON and CAS writes it (TS writeTyped). Same order as the real
// store: guard first, relPath invariant later.
func (s *InMemoryStateStore) writeTyped(relPath, schemaID string, value any, expectedHash *string) (state.CasWriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := assertStateWriteGuarded(relPath); err != nil {
		return state.CasWriteResult{}, err
	}
	if err := assertStateRelPath(relPath); err != nil {
		return state.CasWriteResult{}, err
	}
	entry, err := requireSchemaEntry(schemaID)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	parsed, err := parseRegisteredEntry(entry, value, relPath)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	raw, err := canonicalStateBytes(parsed)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	write := s.casWrite(relPath, raw, expectedHash)
	if !write.OK {
		return write.Result(), nil
	}
	return state.CasWriteResult{OK: true, Hash: write.Hash}, nil
}

// casWrite is the CAS semantics copied from cas.ts: a nil expectedHash means
// "expect absent" (create), a hash means "expect exactly that content"
// (update), anything else is ok:false with the expected-vs-actual pair
// (actual is nil when the file was absent). Callers hold s.mu.
func (s *InMemoryStateStore) casWrite(relPath, nextRaw string, expectedHash *string) state.CasWriteDetailed {
	currentRaw, exists := s.files[relPath]
	var currentHash *string
	if exists {
		hash := docutil.ContentHash(currentRaw)
		currentHash = &hash
		if expectedHash == nil || *currentHash != *expectedHash {
			return state.CasWriteDetailed{OK: false, ExpectedHash: expectedHash, ActualHash: currentHash}
		}
	} else if expectedHash != nil {
		return state.CasWriteDetailed{OK: false, ExpectedHash: expectedHash, ActualHash: nil}
	}
	s.files[relPath] = nextRaw
	s.recordChurn(!exists)
	return state.CasWriteDetailed{OK: true, Hash: docutil.ContentHash(nextRaw), Created: !exists}
}

// recordChurn feeds a landed write to the hook (a conflict is not written, so
// it never lands here).
func (s *InMemoryStateStore) recordChurn(created bool) {
	if s.churn == nil {
		return
	}
	if created {
		s.churn.RecordCreated()
		return
	}
	s.churn.RecordUpdated()
}

// Files exposes the raw backing bytes keyed by state relPath — the
// byte-level inspection surface the golden/CAS tests use (no TS counterpart:
// the TS fake had no escape hatch beyond the typed reads).
func (s *InMemoryStateStore) Files() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.files))
	for relPath, raw := range s.files {
		out[relPath] = raw
	}
	return out
}

// RecordingStateChurnHook is the observable A8 churn hook: it counts the
// created/updated ops a fake accepted (TS InMemoryChurnCounter state, made
// inspectable so the hook's exactly-one-op-per-write invariant is testable).
type RecordingStateChurnHook struct {
	mu      sync.Mutex
	created int
	updated int
}

// RecordCreated counts one created write (state.StateChurnHook).
func (h *RecordingStateChurnHook) RecordCreated() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.created++
}

// RecordUpdated counts one updated write (state.StateChurnHook).
func (h *RecordingStateChurnHook) RecordUpdated() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updated++
}

// Added is the number of created writes recorded.
func (h *RecordingStateChurnHook) Added() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.created
}

// Updated is the number of update writes recorded.
func (h *RecordingStateChurnHook) Updated() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.updated
}

// newFakeSchemaError builds the registry type-mismatch refusal (the TS
// newStateSchemaError pair): the parse returned an unexpected Go type.
func newFakeSchemaError(relPath string, parsed any) error {
	return state.StateSchemaErrorForPath(relPath,
		fmt.Errorf(`registry parse for "%s" returned %T — type mismatch`, relPath, parsed))
}

// assertStateRelPath refuses anything but a `state/` relPath (TS
// assertStateRelPath).
func assertStateRelPath(relPath string) error {
	if !strings.HasPrefix(relPath, "state/") {
		return state.StateKeyError(`internal invariant violated: "%s" is not a state/ relPath`, relPath)
	}
	return nil
}

// assertStateWriteGuarded runs the real boundary guard (internal/boundaries
// — the TS fake imported guardStateWrite from boundaries/guards.ts) and
// returns its typed refusal.
func assertStateWriteGuarded(relPath string) error {
	return boundaries.AssertStateWrite(relPath)
}

// listStateStemIDs collects the stems under a state subdir directory,
// optionally decoded (TS the fake's listClaimIds / listDocStatePaths scans).
// Callers hold s.mu.
func listStateStemIDs(files map[string]string, prefix, suffix string, decode func(string) (string, error)) ([]string, error) {
	ids := []string{}
	for relPath := range files {
		if !strings.HasPrefix(relPath, prefix) || !strings.HasSuffix(relPath, suffix) {
			continue
		}
		stem := relPath[len(prefix) : len(relPath)-len(suffix)]
		if decode == nil {
			ids = append(ids, stem)
			continue
		}
		decoded, err := decode(stem)
		if err != nil {
			return nil, err
		}
		ids = append(ids, decoded)
	}
	sort.Strings(ids)
	return ids, nil
}

// jsonString mirrors the JSON.stringify the TS mismatch/corruption messages
// use (keys are ASCII-safe there).
func jsonString(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(raw)
}

/* ============================================ the GitPlatform recording fake */

// Git platform payload shapes (TS testing/fakes/types.ts). PROVISIONAL
// boundary shapes, frozen enough for orchestration tests; ASCII, deterministic
// and credential-free.

// GitPlatformMethod is one method name of the platform boundary (TS
// GitPlatformMethodName).
type GitPlatformMethod string

// The frozen method names, in the TS GIT_PLATFORM_METHODS order.
const (
	GitPlatformMethodListPullRequests  GitPlatformMethod = "listPullRequests"
	GitPlatformMethodCreatePullRequest GitPlatformMethod = "createPullRequest"
	GitPlatformMethodCreateCheckRun    GitPlatformMethod = "createCheckRun"
	GitPlatformMethodUpdateCheckRun    GitPlatformMethod = "updateCheckRun"
	GitPlatformMethodGetFileContent    GitPlatformMethod = "getFileContent"
	GitPlatformMethodListChangedFiles  GitPlatformMethod = "listChangedFiles"
)

// GitPlatformMethods is the frozen method-name list (TS GIT_PLATFORM_METHODS).
var GitPlatformMethods = []GitPlatformMethod{
	GitPlatformMethodListPullRequests,
	GitPlatformMethodCreatePullRequest,
	GitPlatformMethodCreateCheckRun,
	GitPlatformMethodUpdateCheckRun,
	GitPlatformMethodGetFileContent,
	GitPlatformMethodListChangedFiles,
}

// PullRequestState is the PR lifecycle state (TS PullRequestRef['state']).
type PullRequestState string

// The two PR states.
const (
	PRStateOpen   PullRequestState = "open"
	PRStateClosed PullRequestState = "closed"
)

// CheckRunStatus is the check-run status (TS CheckRunRef['status']).
type CheckRunStatus string

// The check-run statuses.
const (
	CheckRunStatusQueued     CheckRunStatus = "queued"
	CheckRunStatusInProgress CheckRunStatus = "in_progress"
	CheckRunStatusCompleted  CheckRunStatus = "completed"
)

// CheckRunConclusion is the check-run conclusion (TS
// CheckRunRef['conclusion']).
type CheckRunConclusion string

// The check-run conclusions.
const (
	CheckRunConclusionSuccess   CheckRunConclusion = "success"
	CheckRunConclusionFailure   CheckRunConclusion = "failure"
	CheckRunConclusionNeutral   CheckRunConclusion = "neutral"
	CheckRunConclusionCancelled CheckRunConclusion = "cancelled"
	CheckRunConclusionSkipped   CheckRunConclusion = "skipped"
	CheckRunConclusionTimedOut  CheckRunConclusion = "timed_out"
)

// ContentEncoding is the file-content encoding (TS FileContent['encoding']).
type ContentEncoding string

// ContentEncodingUTF8 is the one supported encoding.
const ContentEncodingUTF8 ContentEncoding = "utf8"

// ChangedFileStatus is the changed-file status (TS ChangedFileStatus).
type ChangedFileStatus string

// The changed-file statuses.
const (
	ChangedFileStatusAdded    ChangedFileStatus = "added"
	ChangedFileStatusModified ChangedFileStatus = "modified"
	ChangedFileStatusRemoved  ChangedFileStatus = "removed"
	ChangedFileStatusRenamed  ChangedFileStatus = "renamed"
)

// PullRequestRef is one pull request (TS PullRequestRef).
type PullRequestRef struct {
	Number int
	URL    string
	Title  string
	Head   string
	Base   string
	State  PullRequestState
}

// ListPullRequestsInput is the list filter (TS ListPullRequestsInput).
type ListPullRequestsInput struct {
	Head  *string
	Base  *string
	State *string
}

// CreatePullRequestInput is the PR-creation payload (TS
// CreatePullRequestInput).
type CreatePullRequestInput struct {
	Title string
	Body  string
	Head  string
	Base  string
	Draft *bool
}

// CheckRunOutput is the check-run output block (TS CheckRunOutput).
type CheckRunOutput struct {
	Title   *string
	Summary string
}

// CheckRunRef is one check run (TS CheckRunRef).
type CheckRunRef struct {
	ID         int
	Name       string
	HeadSHA    string
	Status     CheckRunStatus
	Conclusion *CheckRunConclusion
	Output     *CheckRunOutput
}

// CreateCheckRunInput is the check-run creation payload (TS
// CreateCheckRunInput).
type CreateCheckRunInput struct {
	Name       string
	HeadSHA    string
	Status     CheckRunStatus
	Conclusion *CheckRunConclusion
	Output     *CheckRunOutput
}

// UpdateCheckRunPatch is the check-run update patch (TS
// UpdateCheckRunPatch).
type UpdateCheckRunPatch struct {
	Status     *CheckRunStatus
	Conclusion *CheckRunConclusion
	Output     *CheckRunOutput
}

// FileContent is one file read (TS FileContent; Content is null when the
// file does not exist at ref).
type FileContent struct {
	Ref      string
	Path     string
	Content  *string
	Encoding ContentEncoding
}

// ChangedFile is one changed file (TS ChangedFile; PreviousPath carries the
// rename source, D12 fidelity at the platform boundary).
type ChangedFile struct {
	Path         string
	Status       ChangedFileStatus
	PreviousPath *string
}

// RecordedCall is one recorded invocation with a monotonic counter — never a
// clock value (TS RecordedCall).
type RecordedCall struct {
	Method GitPlatformMethod
	Args   []any
	At     int
}

// GitPlatform is the platform boundary Phase 6/8 consumers code against (TS
// GitPlatform). The fake below is the only implementation in this package.
type GitPlatform interface {
	ListPullRequests(input ListPullRequestsInput) ([]PullRequestRef, error)
	CreatePullRequest(input CreatePullRequestInput) (PullRequestRef, error)
	CreateCheckRun(input CreateCheckRunInput) (CheckRunRef, error)
	UpdateCheckRun(id int, patch UpdateCheckRunPatch) (CheckRunRef, error)
	GetFileContent(ref, path string) (FileContent, error)
	ListChangedFiles(base, head string) ([]ChangedFile, error)
}

// Compile-time satisfaction: the fake implements the boundary.
var _ GitPlatform = (*GitPlatformFake)(nil)

const (
	defaultPullRequestNumberStart = 101
	defaultCheckRunIDStart        = 9001
)

// GitPlatformScriptedResponse scripts ONE response for ONE request identity
// of one method (TS GitPlatformScript, re-keyed per the AGENT-behavior
// replay rule).
type GitPlatformScriptedResponse struct {
	Method   GitPlatformMethod
	Identity string
	Value    any
}

// GitPlatformOptions configures the fake (TS GitPlatformFakeOptions).
type GitPlatformOptions struct {
	// Script is the keyed response script: exhausted scripted identities
	// throw (fail closed) instead of silently returning wrong data.
	Script []GitPlatformScriptedResponse
	// PullRequestNumberStart is the first synthesized PR number (default 101).
	PullRequestNumberStart int
	// CheckRunIDStart is the first synthesized check-run id (default 9001).
	CheckRunIDStart int
}

// GitPlatformFake is the recording fake for the GitPlatform boundary (ARCH
// §12.2). NEVER a real client: no network, no git binary, no credentials —
// every method returns scripted (identity-keyed) or deterministic default
// data, and every call is recorded with a monotonic counter.
type GitPlatformFake struct {
	mu        sync.Mutex
	queues    map[GitPlatformMethod]map[string][]any
	scripted  map[GitPlatformMethod]bool
	calls     []RecordedCall
	counter   int
	prSeq     int
	checkSeq  int
	checkRuns map[int]*CheckRunRef
}

// NewGitPlatformFake builds the fake (TS createGitPlatformFake).
func NewGitPlatformFake(options GitPlatformOptions) *GitPlatformFake {
	fake := &GitPlatformFake{
		queues:    map[GitPlatformMethod]map[string][]any{},
		scripted:  map[GitPlatformMethod]bool{},
		prSeq:     options.PullRequestNumberStart,
		checkSeq:  options.CheckRunIDStart,
		checkRuns: map[int]*CheckRunRef{},
	}
	if fake.prSeq == 0 {
		fake.prSeq = defaultPullRequestNumberStart
	}
	if fake.checkSeq == 0 {
		fake.checkSeq = defaultCheckRunIDStart
	}
	for _, scripted := range options.Script {
		fake.script(scripted.Method, scripted.Identity, scripted.Value)
	}
	return fake
}

// script registers one identity-keyed response (fluent internals).
func (f *GitPlatformFake) script(method GitPlatformMethod, identity string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queues[method] == nil {
		f.queues[method] = map[string][]any{}
	}
	f.queues[method][identity] = append(f.queues[method][identity], value)
	f.scripted[method] = true
}

// On scripts the response for one explicit request identity (repeat calls
// queue: the Nth call with that identity consumes the Nth value).
func (f *GitPlatformFake) On(method GitPlatformMethod, identity string, value any) *GitPlatformFake {
	f.script(method, identity, value)
	return f
}

// OnListPullRequests scripts the response for one filter identity.
func (f *GitPlatformFake) OnListPullRequests(input ListPullRequestsInput, value []PullRequestRef) *GitPlatformFake {
	return f.On(GitPlatformMethodListPullRequests, GitPlatformListIdentity(input), value)
}

// OnCreatePullRequest scripts the response for one creation payload identity.
func (f *GitPlatformFake) OnCreatePullRequest(input CreatePullRequestInput, value PullRequestRef) *GitPlatformFake {
	return f.On(GitPlatformMethodCreatePullRequest, GitPlatformCreatePRIdentity(input), value)
}

// OnCreateCheckRun scripts the response for one creation payload identity.
func (f *GitPlatformFake) OnCreateCheckRun(input CreateCheckRunInput, value CheckRunRef) *GitPlatformFake {
	return f.On(GitPlatformMethodCreateCheckRun, GitPlatformCreateCheckIdentity(input), value)
}

// OnUpdateCheckRun scripts the response for one (id, patch) identity.
func (f *GitPlatformFake) OnUpdateCheckRun(id int, patch UpdateCheckRunPatch, value CheckRunRef) *GitPlatformFake {
	return f.On(GitPlatformMethodUpdateCheckRun, GitPlatformUpdateCheckIdentity(id, patch), value)
}

// OnGetFileContent scripts the response for one (ref, path) identity.
func (f *GitPlatformFake) OnGetFileContent(ref, path string, value FileContent) *GitPlatformFake {
	return f.On(GitPlatformMethodGetFileContent, GitPlatformFileIdentity(ref, path), value)
}

// OnListChangedFiles scripts the response for one (base, head) identity.
func (f *GitPlatformFake) OnListChangedFiles(base, head string, value []ChangedFile) *GitPlatformFake {
	return f.On(GitPlatformMethodListChangedFiles, GitPlatformChangedFilesIdentity(base, head), value)
}

// GitPlatformListIdentity is the request identity of listPullRequests: the
// canonical JSON of the filter (identical filter ⇒ identical identity).
func GitPlatformListIdentity(input ListPullRequestsInput) string { return canonicalIdentity(input) }

// GitPlatformCreatePRIdentity is the request identity of createPullRequest.
func GitPlatformCreatePRIdentity(input CreatePullRequestInput) string {
	return canonicalIdentity(input)
}

// GitPlatformCreateCheckIdentity is the request identity of createCheckRun.
func GitPlatformCreateCheckIdentity(input CreateCheckRunInput) string {
	return canonicalIdentity(input)
}

// GitPlatformUpdateCheckIdentity is the request identity of updateCheckRun:
// the check-run id plus the canonical patch.
func GitPlatformUpdateCheckIdentity(id int, patch UpdateCheckRunPatch) string {
	return strconv.Itoa(id) + "\x00" + canonicalIdentity(patch)
}

// GitPlatformFileIdentity is the request identity of getFileContent.
func GitPlatformFileIdentity(ref, path string) string { return ref + "\x00" + path }

// GitPlatformChangedFilesIdentity is the request identity of listChangedFiles.
func GitPlatformChangedFilesIdentity(base, head string) string { return base + ".." + head }

// canonicalIdentity renders a payload as its canonical JSON identity
// (identical input ⇒ identical key; docutil.ContentHash keeps the key short).
func canonicalIdentity(value any) string {
	canonical, err := canonicalize(value)
	if err != nil {
		canonical = jsonString(value)
	}
	return docutil.ContentHash(canonical)
}

/* ------------------------------------------------------------- recording */

// Calls returns every call so far, in invocation order, with monotonic `at`
// counters (TS `calls`).
func (f *GitPlatformFake) Calls() []RecordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RecordedCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// CallsFor returns the recorded calls for one method, in order (TS
// callsFor).
func (f *GitPlatformFake) CallsFor(method GitPlatformMethod) []RecordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []RecordedCall{}
	for _, call := range f.calls {
		if call.Method == method {
			out = append(out, call)
		}
	}
	return out
}

// ResetCalls drops the recording log (script queues are NOT reset).
func (f *GitPlatformFake) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

/* ---------------------------------------------------------------- methods */

// ListPullRequests returns the scripted response for the filter's identity,
// or an empty list when the method is unscripted.
func (f *GitPlatformFake) ListPullRequests(input ListPullRequestsInput) ([]PullRequestRef, error) {
	identity := GitPlatformListIdentity(input)
	f.record(GitPlatformMethodListPullRequests, []any{input})
	value, err := f.next(GitPlatformMethodListPullRequests, identity, func() any {
		return []PullRequestRef{}
	})
	if err != nil {
		return nil, err
	}
	return value.([]PullRequestRef), nil
}

// CreatePullRequest returns the scripted response for the payload identity,
// or a deterministic synthesized PR (number 101, 102, …) when unscripted.
func (f *GitPlatformFake) CreatePullRequest(input CreatePullRequestInput) (PullRequestRef, error) {
	identity := GitPlatformCreatePRIdentity(input)
	f.record(GitPlatformMethodCreatePullRequest, []any{input})
	value, err := f.next(GitPlatformMethodCreatePullRequest, identity, func() any {
		number := f.prSeq
		f.prSeq++
		return PullRequestRef{
			Number: number,
			URL:    fmt.Sprintf("https://gitplatform.fake/pulls/%d", number),
			Title:  input.Title,
			Head:   input.Head,
			Base:   input.Base,
			State:  PRStateOpen,
		}
	})
	if err != nil {
		return PullRequestRef{}, err
	}
	return value.(PullRequestRef), nil
}

// CreateCheckRun returns the scripted response for the payload identity, or a
// deterministic synthesized check run (id 9001, 9002, …) when unscripted.
func (f *GitPlatformFake) CreateCheckRun(input CreateCheckRunInput) (CheckRunRef, error) {
	identity := GitPlatformCreateCheckIdentity(input)
	f.record(GitPlatformMethodCreateCheckRun, []any{input})
	value, err := f.next(GitPlatformMethodCreateCheckRun, identity, func() any {
		id := f.checkSeq
		f.checkSeq++
		run := CheckRunRef{
			ID:         id,
			Name:       input.Name,
			HeadSHA:    input.HeadSHA,
			Status:     input.Status,
			Conclusion: input.Conclusion,
			Output:     input.Output,
		}
		stored := run
		f.checkRuns[id] = &stored
		return run
	})
	if err != nil {
		return CheckRunRef{}, err
	}
	return value.(CheckRunRef), nil
}

// UpdateCheckRun returns the scripted response for the (id, patch) identity,
// or the field-level merge over the created check run when unscripted.
func (f *GitPlatformFake) UpdateCheckRun(id int, patch UpdateCheckRunPatch) (CheckRunRef, error) {
	identity := GitPlatformUpdateCheckIdentity(id, patch)
	f.record(GitPlatformMethodUpdateCheckRun, []any{id, patch})
	value, err := f.next(GitPlatformMethodUpdateCheckRun, identity, func() any {
		existing := f.checkRuns[id]
		run := CheckRunRef{
			ID:         id,
			Name:       "check-run",
			HeadSHA:    "HEAD",
			Status:     CheckRunStatusCompleted,
			Conclusion: nil,
			Output:     nil,
		}
		if existing != nil {
			run.Name = existing.Name
			run.HeadSHA = existing.HeadSHA
			run.Status = existing.Status
			run.Conclusion = existing.Conclusion
			run.Output = existing.Output
		}
		if patch.Status != nil {
			run.Status = *patch.Status
		}
		if patch.Conclusion != nil {
			run.Conclusion = patch.Conclusion
		}
		if patch.Output != nil {
			run.Output = patch.Output
		}
		stored := run
		f.checkRuns[id] = &stored
		return run
	})
	if err != nil {
		return CheckRunRef{}, err
	}
	return value.(CheckRunRef), nil
}

// GetFileContent returns the scripted response for the (ref, path) identity,
// or a null-content read when unscripted (callers assert absence).
func (f *GitPlatformFake) GetFileContent(ref, path string) (FileContent, error) {
	identity := GitPlatformFileIdentity(ref, path)
	f.record(GitPlatformMethodGetFileContent, []any{ref, path})
	value, err := f.next(GitPlatformMethodGetFileContent, identity, func() any {
		return FileContent{Ref: ref, Path: path, Content: nil, Encoding: ContentEncodingUTF8}
	})
	if err != nil {
		return FileContent{}, err
	}
	return value.(FileContent), nil
}

// ListChangedFiles returns the scripted response for the (base, head)
// identity, or an empty list when unscripted.
func (f *GitPlatformFake) ListChangedFiles(base, head string) ([]ChangedFile, error) {
	identity := GitPlatformChangedFilesIdentity(base, head)
	f.record(GitPlatformMethodListChangedFiles, []any{base, head})
	value, err := f.next(GitPlatformMethodListChangedFiles, identity, func() any {
		return []ChangedFile{}
	})
	if err != nil {
		return nil, err
	}
	return value.([]ChangedFile), nil
}

/* -------------------------------------------------------------- internal */

// record appends one invocation with the monotonic counter.
func (f *GitPlatformFake) record(method GitPlatformMethod, args []any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, RecordedCall{Method: method, Args: args, At: f.counter})
	f.counter++
}

// next dispatches by request identity: a scripted method resolves its
// per-identity queue (FIFO WITHIN one identity — identical requests are
// interchangeable, so no cross-wiring is possible), and a scripted method
// with no matching entry FAILS LOUD. An unscripted method falls back to its
// deterministic default (TS `next`).
func (f *GitPlatformFake) next(method GitPlatformMethod, identity string, fallback func() any) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.scripted[method] {
		return fallback(), nil
	}
	queue := f.queues[method][identity]
	if len(queue) == 0 {
		return nil, fmt.Errorf(
			"GitPlatformFake: script exhausted for %q (request identity %q) — extend the script or stop calling (fail closed stub)",
			method, identity)
	}
	value := queue[0]
	f.queues[method][identity] = queue[1:]
	return value, nil
}

/* ================================================ the keyed model-stage doubles */

// KeyedStageCall is one recorded stage invocation with its monotonic index
// (never a clock value).
type KeyedStageCall[In any] struct {
	At       int
	Identity string
	Input    In
}

// KeyedStageFake is the recording stage double every model-stage fake is
// built on: responses are keyed by REQUEST IDENTITY and dispatched per call,
// per the AGENT-behavior replay rule — never positional FIFO, because
// completion-order consumption cross-wires fixtures and manifests as a
// load-dependent flake. An unscripted identity fails loud (or falls back to
// the explicit default when one was set).
type KeyedStageFake[In any, Out any] struct {
	label     string
	key       func(In) string
	mu        sync.Mutex
	responses map[string]Out
	failures  map[string]error
	fallback  *Out
	next      int
	calls     []KeyedStageCall[In]
}

// NewKeyedStageFake builds the double for one stage (label names it in
// failure messages) with the identity function that dispatches its calls.
func NewKeyedStageFake[In any, Out any](label string, key func(In) string) *KeyedStageFake[In, Out] {
	return &KeyedStageFake[In, Out]{
		label:     label,
		key:       key,
		responses: map[string]Out{},
		failures:  map[string]error{},
	}
}

// On scripts the response for one request identity.
func (f *KeyedStageFake[In, Out]) On(identity string, result Out) *KeyedStageFake[In, Out] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[identity] = result
	return f
}

// OnError scripts the failure for one request identity (the stage reports the
// error to its caller; the orchestrator degrades per its contract).
func (f *KeyedStageFake[In, Out]) OnError(identity string, err error) *KeyedStageFake[In, Out] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[identity] = err
	return f
}

// OnDefault scripts the fallback response for UNSCRIPTED identities.
func (f *KeyedStageFake[In, Out]) OnDefault(result Out) *KeyedStageFake[In, Out] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback = &result
	return f
}

// Dispatch is the keyed call: record FIRST, then resolve by identity, then
// fail loud on a miss with no default (a refused call is still a call that
// happened — it stays observable).
func (f *KeyedStageFake[In, Out]) Dispatch(input In) (Out, error) {
	identity := f.key(input)
	f.mu.Lock()
	f.calls = append(f.calls, KeyedStageCall[In]{At: f.next, Identity: identity, Input: input})
	f.next++
	defer f.mu.Unlock()

	if result, ok := f.responses[identity]; ok {
		return result, nil
	}
	if failure, ok := f.failures[identity]; ok {
		var zero Out
		return zero, failure
	}
	if f.fallback != nil {
		return *f.fallback, nil
	}
	var zero Out
	return zero, fmt.Errorf(
		"%s fake: no scripted response for request identity %q — keyed dispatch by request identity, never positional FIFO (fail loud)",
		f.label, identity)
}

// Calls returns the recorded invocations in call order.
func (f *KeyedStageFake[In, Out]) Calls() []KeyedStageCall[In] {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]KeyedStageCall[In], len(f.calls))
	copy(out, f.calls)
	return out
}

// CallsFor returns the recorded invocations with one request identity.
func (f *KeyedStageFake[In, Out]) CallsFor(identity string) []KeyedStageCall[In] {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []KeyedStageCall[In]{}
	for _, call := range f.calls {
		if call.Identity == identity {
			out = append(out, call)
		}
	}
	return out
}

// ResetCalls drops the recording log (scripted responses are NOT reset).
func (f *KeyedStageFake[In, Out]) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// Identities returns the request identities recorded so far (call order).
func (f *KeyedStageFake[In, Out]) Identities() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, call.Identity)
	}
	return out
}

/* ----------------------------------------------------- decision stage fake */

// DefaultDecisionStageIdentity is the decision-stage request identity: one
// run → one decision call (the ARCH §4.1 run id).
func DefaultDecisionStageIdentity(input orchestrator.DecisionStageInput) string {
	return input.Ctx.RunID
}

// DecisionStageFake is the keyed decision-stage double
// (orchestrator.DecisionStagePort; the TS suites bound a recorded replay at
// the OpenRouter seam — this is the STAGE-level equivalent for Go suites).
type DecisionStageFake struct {
	*KeyedStageFake[orchestrator.DecisionStageInput, orchestrator.DecisionStageResult]
}

// Compile-time satisfaction of the port.
var _ orchestrator.DecisionStagePort = (*DecisionStageFake)(nil)

// NewDecisionStageFake builds the double keyed by the run id.
func NewDecisionStageFake() *DecisionStageFake {
	return &DecisionStageFake{
		KeyedStageFake: NewKeyedStageFake[orchestrator.DecisionStageInput, orchestrator.DecisionStageResult]("decision", DefaultDecisionStageIdentity),
	}
}

// Decide dispatches by request identity.
func (f *DecisionStageFake) Decide(input orchestrator.DecisionStageInput) (orchestrator.DecisionStageResult, error) {
	return f.Dispatch(input)
}

// OnRun scripts the decision result for one run id.
func (f *DecisionStageFake) OnRun(runID string, result orchestrator.DecisionStageResult) *DecisionStageFake {
	return &DecisionStageFake{KeyedStageFake: f.KeyedStageFake.On(runID, result)}
}

/* -------------------------------------------------------- writer stage fake */

// DefaultWriterStageIdentity is the writer-stage request identity: the doc
// path (the phase-6 per-doc routing precedent — the writer pool issues its
// calls concurrently, so the double MUST be dispatch-by-input).
func DefaultWriterStageIdentity(input orchestrator.WriterStageInput) string {
	return input.Doc.DocPath
}

// WriterStageFake is the keyed writer-stage double
// (orchestrator.WriterStagePort).
type WriterStageFake struct {
	*KeyedStageFake[orchestrator.WriterStageInput, orchestrator.WriterStageResult]
}

// Compile-time satisfaction of the port.
var _ orchestrator.WriterStagePort = (*WriterStageFake)(nil)

// NewWriterStageFake builds the double keyed by doc path.
func NewWriterStageFake() *WriterStageFake {
	return &WriterStageFake{
		KeyedStageFake: NewKeyedStageFake[orchestrator.WriterStageInput, orchestrator.WriterStageResult]("writer", DefaultWriterStageIdentity),
	}
}

// Write dispatches by request identity.
func (f *WriterStageFake) Write(input orchestrator.WriterStageInput) (orchestrator.WriterStageResult, error) {
	return f.Dispatch(input)
}

// OnDoc scripts the writer result for one doc path.
func (f *WriterStageFake) OnDoc(docPath string, result orchestrator.WriterStageResult) *WriterStageFake {
	return &WriterStageFake{KeyedStageFake: f.KeyedStageFake.On(docPath, result)}
}

/* -------------------------------------------------- verification stage fake */

// DefaultVerificationStageIdentity is the verification-stage request
// identity: the patch's doc path (judge independence per ARCH §13).
func DefaultVerificationStageIdentity(input orchestrator.VerificationStageInput) string {
	return input.Patch.DocPath
}

// VerificationStageFake is the keyed verification-stage double
// (orchestrator.VerificationStagePort).
type VerificationStageFake struct {
	*KeyedStageFake[orchestrator.VerificationStageInput, orchestrator.VerificationStageResult]
}

// Compile-time satisfaction of the port.
var _ orchestrator.VerificationStagePort = (*VerificationStageFake)(nil)

// NewVerificationStageFake builds the double keyed by doc path.
func NewVerificationStageFake() *VerificationStageFake {
	return &VerificationStageFake{
		KeyedStageFake: NewKeyedStageFake[orchestrator.VerificationStageInput, orchestrator.VerificationStageResult]("verification", DefaultVerificationStageIdentity),
	}
}

// Verify dispatches by request identity.
func (f *VerificationStageFake) Verify(input orchestrator.VerificationStageInput) (orchestrator.VerificationStageResult, error) {
	return f.Dispatch(input)
}

// OnDoc scripts the verification result for one doc path.
func (f *VerificationStageFake) OnDoc(docPath string, result orchestrator.VerificationStageResult) *VerificationStageFake {
	return &VerificationStageFake{KeyedStageFake: f.KeyedStageFake.On(docPath, result)}
}

/* -------------------------------------------------------- policy stage fake */

// DefaultPolicyStageIdentity is the policy-stage request identity: the doc
// path the outcome is resolved for.
func DefaultPolicyStageIdentity(input orchestrator.PolicyStageInput) string {
	return input.DocPath
}

// PolicyStageFake is the keyed policy-stage double
// (orchestrator.PolicyStagePort).
type PolicyStageFake struct {
	*KeyedStageFake[orchestrator.PolicyStageInput, orchestrator.PolicyStageResult]
}

// Compile-time satisfaction of the port.
var _ orchestrator.PolicyStagePort = (*PolicyStageFake)(nil)

// NewPolicyStageFake builds the double keyed by doc path.
func NewPolicyStageFake() *PolicyStageFake {
	return &PolicyStageFake{
		KeyedStageFake: NewKeyedStageFake[orchestrator.PolicyStageInput, orchestrator.PolicyStageResult]("policy", DefaultPolicyStageIdentity),
	}
}

// Resolve dispatches by request identity.
func (f *PolicyStageFake) Resolve(input orchestrator.PolicyStageInput) (orchestrator.PolicyStageResult, error) {
	return f.Dispatch(input)
}

// OnDoc scripts the policy outcome for one doc path.
func (f *PolicyStageFake) OnDoc(docPath string, result orchestrator.PolicyStageResult) *PolicyStageFake {
	return &PolicyStageFake{KeyedStageFake: f.KeyedStageFake.On(docPath, result)}
}

/* ------------------------------------------------------- early-signal fake */

// EarlySignalFake records every posted/updated early-signal payload (no
// scripted responses: the sink is observe-only, TS the suites' inline sink).
type EarlySignalFake struct {
	mu       sync.Mutex
	payloads []orchestrator.EarlySignalPayload
}

// Compile-time satisfaction of the port.
var _ orchestrator.EarlySignalPort = (*EarlySignalFake)(nil)

// NewEarlySignalFake builds the recording sink.
func NewEarlySignalFake() *EarlySignalFake { return &EarlySignalFake{} }

// Post records the initial payload.
func (f *EarlySignalFake) Post(payload orchestrator.EarlySignalPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, payload)
	return nil
}

// Update records an updated payload.
func (f *EarlySignalFake) Update(payload orchestrator.EarlySignalPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, payload)
	return nil
}

// Payloads returns the recorded payloads in order.
func (f *EarlySignalFake) Payloads() []orchestrator.EarlySignalPayload {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]orchestrator.EarlySignalPayload, len(f.payloads))
	copy(out, f.payloads)
	return out
}

// Reset drops the recording.
func (f *EarlySignalFake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = nil
}

/* --------------------------------------------------- the pipeline ports bundle */

// PipelineFakes bundles the keyed model-stage doubles for one pipeline run.
// Start from NewPipelineFakes, script each stage by its request identity, then
// hand Ports() to orchestrator.RunPipeline.
type PipelineFakes struct {
	Decision     *DecisionStageFake
	Writer       *WriterStageFake
	Verification *VerificationStageFake
	Policy       *PolicyStageFake
	EarlySignal  *EarlySignalFake
}

// NewPipelineFakes builds a bundle with every stage double fresh and
// unscripted.
func NewPipelineFakes() *PipelineFakes {
	return &PipelineFakes{
		Decision:     NewDecisionStageFake(),
		Writer:       NewWriterStageFake(),
		Verification: NewVerificationStageFake(),
		Policy:       NewPolicyStageFake(),
		EarlySignal:  NewEarlySignalFake(),
	}
}

// Ports wires the fakes onto the orchestrator port interfaces (the fixture
// builder's job: real repo + recorded stage doubles).
func (f *PipelineFakes) Ports() orchestrator.PipelinePorts {
	return orchestrator.PipelinePorts{
		Decision:     f.Decision,
		Writer:       f.Writer,
		Verification: f.Verification,
		Policy:       f.Policy,
		EarlySignal:  f.EarlySignal,
	}
}

// ResetCalls drops every recording log (scripted responses stay).
func (f *PipelineFakes) ResetCalls() {
	f.Decision.ResetCalls()
	f.Writer.ResetCalls()
	f.Verification.ResetCalls()
	f.Policy.ResetCalls()
	f.EarlySignal.Reset()
}
