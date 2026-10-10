package gitpr

// Ported from packages/core/src/gitpr/merge-ledger.ts (P6-T18; PLAN §7.3
// "Merge-order conflicts & PR lifecycle"; ARCH §12.3). Per-`source_key` record of
// doc-PR lifecycle state (`open | superseded | merged | conflicted | dropped`)
// driving rebase/supersede/no-op transitions, persisted under
// `.docsync/state/merge-ledger/<safe-key>.json` through the typed state store
// (`docsync.merge-ledger/v1`, OD-R03) with CAS semantics (V-P0-11).
//
// Three pieces:
//
//  1. {@link MergeLedgerStore.Load} — registry-validated read; a missing ledger
//     is the soft `nil` outcome (never an error).
//  2. {@link MergeLedgerStore.Record} — CAS upsert of ONE entry: fresh load →
//     entry-shape validation (LedgerEntry.Validate) + `doc_pr_number`
//     uniqueness → `WriteMergeLedger` under the expected hash. The CAS loser
//     gets `{ ok: false, kind: 'cas-conflict' }` and the CALLER re-runs against
//     a fresh read — the ledger never overwrites a winner (R15, V-P0-11). A
//     caller may assert its own last-read hash via RecordLedgerOptions
//     (optimistic concurrency); nil derives the expectation from the fresh
//     internal read.
//  3. {@link LedgerTransition} — the PURE closed transition table. Record is the
//     mechanics; LedgerTransition is the law: callers compute the next state
//     through it before recording, so an illegal lifecycle step is a typed
//     error at the decision site, not silent ledger drift.
//
// Timestamps are emitter-supplied (the injected `clock`), never generated here
// (P19; schema docs). No delete API exists — append/upsert only.

import (
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// CodeMergeLedger preserves the TS EDOCSYNC_MERGE_LEDGER code verbatim
// (CONVENTIONS.md §Errors: the code is not in internal/docutil, so it rides the
// error text through docutil.New instead of inventing a new docutil code).
const CodeMergeLedger = "EDOCSYNC_MERGE_LEDGER"

// CodeMergeLedgerTransition preserves the TS EDOCSYNC_MERGE_LEDGER_TRANSITION
// code verbatim (same CONVENTIONS.md §Errors note).
const CodeMergeLedgerTransition = "EDOCSYNC_MERGE_LEDGER_TRANSITION"

// mergeLedgerValidationError builds the MergeLedgerValidationError equivalent.
func mergeLedgerValidationError(message string) error {
	return docutil.New(CodeMergeLedger, message)
}

// mergeLedgerTransitionError builds the MergeLedgerTransitionError equivalent.
func mergeLedgerTransitionError(message string) error {
	return docutil.New(CodeMergeLedgerTransition, message)
}

/* ------------------------------------------------------------ transition table */

// legalTransitions is the closed transition table (P6-T18; ARCH §12.3): `open`
// may go anywhere except backward to itself; `superseded` may still merge or be
// dropped; a `conflicted` ledger re-opens on a successful re-run; `merged` and
// `dropped` are TERMINAL.
var legalTransitions = map[schemas.MergeLedgerState][]schemas.MergeLedgerState{
	schemas.MergeLedgerStateOpen: {
		schemas.MergeLedgerStateSuperseded,
		schemas.MergeLedgerStateMerged,
		schemas.MergeLedgerStateConflicted,
		schemas.MergeLedgerStateDropped,
	},
	schemas.MergeLedgerStateSuperseded: {
		schemas.MergeLedgerStateMerged,
		schemas.MergeLedgerStateDropped,
	},
	schemas.MergeLedgerStateConflicted: {
		schemas.MergeLedgerStateOpen,
		schemas.MergeLedgerStateSuperseded,
		schemas.MergeLedgerStateDropped,
	},
	schemas.MergeLedgerStateMerged:  {},
	schemas.MergeLedgerStateDropped: {},
}

// LedgerTransition is the pure transition guard (TS transition): returns `next`
// when `current → next` is legal, returns a typed error otherwise (terminal
// states and unlisted edges are illegal — fail closed, never a silent state
// drift).
func LedgerTransition(current, next schemas.MergeLedgerState) (schemas.MergeLedgerState, error) {
	legal, known := legalTransitions[current]
	if !known {
		return "", mergeLedgerTransitionError(
			"unknown current ledger state \"" + string(current) + "\" — the state union is closed (" +
				strings.Join([]string{"open", "superseded", "merged", "conflicted", "dropped"}, " | ") + ")")
	}
	for _, candidate := range legal {
		if candidate == next {
			return next, nil
		}
	}
	detail := " — legal: " + strings.Join(stateNames(legal), ", ")
	if len(legal) == 0 {
		detail = " — \"" + string(current) + "\" is terminal"
	}
	return "", mergeLedgerTransitionError(
		"illegal ledger transition " + string(current) + " → " + string(next) + detail + " (P6-T18)")
}

// stateNames renders the legal-target list for the error message.
func stateNames(states []schemas.MergeLedgerState) []string {
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, string(state))
	}
	return names
}

/* ---------------------------------------------------------------- ledger store */

// MergeLedgerSchemaID / MergeLedgerSchemaVersion are the schema identity
// constants (TS MERGE_LEDGER_SCHEMA_ID / MERGE_LEDGER_SCHEMA_VERSION).
const (
	MergeLedgerSchemaID      = "docsync.merge-ledger"
	MergeLedgerSchemaVersion = "docsync.merge-ledger/v1"
)

// RecordLedgerEntryInput is the entry input of Record — a camelCase seam over the
// schema's snake_case entry.
type RecordLedgerEntryInput struct {
	// DocPRNumber is the doc PR this entry tracks — unique within the ledger
	// (fail closed).
	DocPRNumber int
	// State is the lifecycle state; compute it through LedgerTransition at the
	// call site.
	State schemas.MergeLedgerState
	// IdempotencyKey is the P6-T05 idempotency key of the run that produced
	// this state.
	IdempotencyKey string
	// Detail is human-auditable evidence (why the state changed).
	Detail string
}

// RecordLedgerOptions is the optional CAS assertion for Record.
type RecordLedgerOptions struct {
	// ExpectedHash asserts the caller's last-known ledger hash verbatim by the
	// CAS write (ExpectCreate = "expect create"). A nil ExpectedHash with
	// ExpectCreate false means OMITTED — the expectation is derived from
	// record's fresh internal read. A stale assertion yields the cas-conflict
	// result — re-run against a fresh read (R15).
	ExpectedHash *string
	ExpectCreate bool
}

// MergeLedgerRecordResult is the Record result (TS
// MergeLedgerRecordResult): the tagged `{ok: true, hash}` | `{ok: false,
// kind: 'cas-conflict', detail}` union — the Go struct with the OK flag; Detail
// is empty when OK.
type MergeLedgerRecordResult struct {
	OK     bool
	Hash   string
	Detail string
}

// MergeLedgerStateStore is the injected state-store surface (the two
// merge-ledger methods of *state.StateStore): guard, registry validation,
// canonical bytes and CAS are the store's guarantees, not re-implemented here.
type MergeLedgerStateStore interface {
	ReadMergeLedger(sourceKey string) (state.StateRead[*schemas.MergeLedger], error)
	WriteMergeLedger(sourceKey string, ledger schemas.MergeLedger, expectedHash *string) (state.CasWriteResult, error)
}

// MergeLedgerStoreDeps is the factory dependency bundle.
type MergeLedgerStoreDeps struct {
	Store MergeLedgerStateStore
}

// MergeLedgerStore is the merge-order ledger surface (P6-T18).
type MergeLedgerStore interface {
	// Load is the registry-validated read; a missing ledger is nil (soft
	// outcome).
	Load(sourceKey string) (*schemas.MergeLedger, error)
	// Record is the CAS upsert of one entry (see the module docs — the caller
	// re-runs on conflict, R15).
	Record(sourceKey string, entry RecordLedgerEntryInput, clock func() string, opts *RecordLedgerOptions) (MergeLedgerRecordResult, error)
	// Transition is the pure closed transition table ({@link LedgerTransition}).
	Transition(current, next schemas.MergeLedgerState) (schemas.MergeLedgerState, error)
}

// mergeLedgerStore is the constructed store: all persistence goes through the
// injected state store.
type mergeLedgerStore struct {
	store MergeLedgerStateStore
}

// NewMergeLedgerStore builds the merge-order ledger store (TS
// createMergeLedgerStore). It THROWS — i.e. returns a typed error — on a store
// missing the ledger methods.
func NewMergeLedgerStore(deps MergeLedgerStoreDeps) (MergeLedgerStore, error) {
	if deps.Store == nil {
		return nil, mergeLedgerValidationError(
			"createMergeLedgerStore: deps.store must be a StateStore exposing readMergeLedger/writeMergeLedger")
	}
	return &mergeLedgerStore{store: deps.Store}, nil
}

// Load — see the interface.
func (s *mergeLedgerStore) Load(sourceKey string) (*schemas.MergeLedger, error) {
	if sourceKey == "" {
		return nil, mergeLedgerValidationError("sourceKey must be a nonempty string")
	}
	read, err := s.store.ReadMergeLedger(sourceKey)
	if err != nil {
		return nil, err
	}
	if !read.Exists {
		return nil, nil
	}
	return read.Content, nil
}

// buildEntry validates and normalizes one entry (TS buildEntry): the schema's
// own Validate is the authority (never a hand-rolled copy of the refinement).
func buildLedgerEntry(entry RecordLedgerEntryInput, at string) (schemas.LedgerEntry, error) {
	candidate := schemas.LedgerEntry{
		DocPRNumber:    entry.DocPRNumber,
		State:          entry.State,
		IdempotencyKey: entry.IdempotencyKey,
		At:             at,
		Detail:         entry.Detail,
	}
	if issues := candidate.Validate(); len(issues) > 0 {
		return schemas.LedgerEntry{}, mergeLedgerValidationError(
			"ledger entry fails " + MergeLedgerSchemaID + " entry shape: " + mergeLedgerIssueSummary(issues))
	}
	return candidate, nil
}

// mergeLedgerIssueSummary renders the schema issues the way the TS mapped zod
// issues (`<path>: <message>`, `<root>: <message>` when the issue has no path).
func mergeLedgerIssueSummary(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		path := strings.Join(issue.Path, ".")
		if path == "" {
			path = "<root>"
		}
		parts = append(parts, path+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

// upsertEntry replaces in-place on a matching `doc_pr_number`, appends
// otherwise (TS upsertEntry).
func upsertLedgerEntry(entries []schemas.LedgerEntry, candidate schemas.LedgerEntry) []schemas.LedgerEntry {
	for index := range entries {
		if entries[index].DocPRNumber == candidate.DocPRNumber {
			next := append([]schemas.LedgerEntry(nil), entries...)
			next[index] = candidate
			return next
		}
	}
	return append(append([]schemas.LedgerEntry(nil), entries...), candidate)
}

// Record — see the interface. The CAS loser NEVER overwrites the winner (R15):
// the caller re-runs evaluation against a fresh read.
func (s *mergeLedgerStore) Record(
	sourceKey string,
	entry RecordLedgerEntryInput,
	clock func() string,
	opts *RecordLedgerOptions,
) (MergeLedgerRecordResult, error) {
	if sourceKey == "" {
		return MergeLedgerRecordResult{}, mergeLedgerValidationError("sourceKey must be a nonempty string")
	}
	if clock == nil {
		return MergeLedgerRecordResult{}, mergeLedgerValidationError("record: clock must be a () => string function")
	}
	at := clock()
	candidate, err := buildLedgerEntry(entry, at)
	if err != nil {
		return MergeLedgerRecordResult{}, err
	}

	read, err := s.store.ReadMergeLedger(sourceKey)
	if err != nil {
		return MergeLedgerRecordResult{}, err
	}
	var current *schemas.MergeLedger
	if read.Exists {
		current = read.Content
	}
	// TS: `opts?.expectedHash !== undefined ? opts.expectedHash : read.exists ?
	// read.hash : null` — the Go projection: ExpectCreate is the TS explicit
	// `null` ("expect create"), a non-nil ExpectedHash asserts verbatim, and
	// nil (omitted) derives the expectation from the fresh internal read.
	var expectedHash *string
	switch {
	case opts != nil && opts.ExpectCreate:
		expectedHash = nil
	case opts != nil && opts.ExpectedHash != nil:
		expectedHash = opts.ExpectedHash
	case read.Exists:
		expectedHash = &read.Hash
	default:
		expectedHash = nil
	}

	var existingEntries []schemas.LedgerEntry
	if current != nil {
		existingEntries = current.Entries
	}
	entries := upsertLedgerEntry(existingEntries, candidate)
	// Defense-in-depth before the write: the schema's superRefine enforces
	// doc_pr_number uniqueness again at the store boundary, but the invariant
	// is asserted at the decision site too (fail closed, P6-T18).
	seen := map[int]bool{}
	for _, existing := range entries {
		if seen[existing.DocPRNumber] {
			return MergeLedgerRecordResult{}, mergeLedgerValidationError(
				"doc_pr_number " + strconv.Itoa(existing.DocPRNumber) +
					" would appear more than once in the ledger for \"" + sourceKey +
					"\" (merge-order ambiguity, fail closed)")
		}
		seen[existing.DocPRNumber] = true
	}
	next := schemas.MergeLedger{
		Schema:    MergeLedgerSchemaVersion,
		SourceKey: sourceKey,
		UpdatedAt: at,
		Entries:   entries,
	}
	if issues := next.Validate(); len(issues) > 0 {
		return MergeLedgerRecordResult{}, mergeLedgerValidationError(
			"ledger for \"" + sourceKey + "\" fails " + MergeLedgerSchemaID + ": " +
				mergeLedgerIssueSummary(issues))
	}

	write, err := s.store.WriteMergeLedger(sourceKey, next, expectedHash)
	if err != nil {
		return MergeLedgerRecordResult{}, err
	}
	if !write.OK {
		return MergeLedgerRecordResult{
			OK:   false,
			Hash: "",
			Detail: "merge-order ledger for \"" + sourceKey + "\" CAS conflict: expected hash " +
				mergeLedgerStringOrNull(write.ExpectedHash) + " but the file now holds " +
				mergeLedgerStringOrNull(write.ActualHash) +
				" — a concurrent writer won; re-run record() against a fresh read, never overwrite the winner (R15)",
		}, nil
	}
	return MergeLedgerRecordResult{OK: true, Hash: write.Hash, Detail: ""}, nil
}

// mergeLedgerStringOrNull renders a hash pointer the way the TS `String(x)`
// rendered it (`null` when absent).
func mergeLedgerStringOrNull(value *string) string {
	if value == nil {
		return "null"
	}
	return *value
}

// Transition — see the interface.
func (s *mergeLedgerStore) Transition(current, next schemas.MergeLedgerState) (schemas.MergeLedgerState, error) {
	return LedgerTransition(current, next)
}
