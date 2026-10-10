package verifier

// Ported from packages/verifier/src/storage/records.ts (P4-T20;
// PLAN §5.3/§5.4; ARCH §6.3): append-only, content-addressed verification
// records — the per-document doc-state record carries the verification
// ledger — every emitted `docsync.verification-report/v1` embedded whole
// (V1 files-only layout, OD-3) and content-addressed by `report_hash`
// (BARE 64-hex sha256 over the CANONICAL JSON of the report — the OD-R42
// bare-digest convention; dedup key) — plus the verifier-vs-human
// disagreement log (R5) that feeds Phase-10 policy recalibration. Entries
// live INSIDE the existing D1 doc-state layout (OD-R44: no new `state/`
// subdirectory).
//
// Append-only by construction: the surface is save/list/recordDisagreement
// ONLY — there is no delete or update API, and entries are never removed or
// reordered (audit replay). Dedup: a report already present under its
// `report_hash` is a no-op (`created:false`, NO write); a disagreement
// dedupes on EXACTLY the 4-tuple (run_id, verifier_verdict, human_outcome,
// reason) — `at` and `patch_ref` are EXCLUDED from the dedup identity (the
// same 4-tuple with a different timestamp or patch ref is still a duplicate
// no-op).
//
// Fail-closed posture: a MISSING doc-state record is a hard error — the
// orchestrator initializes document state before persisting verification
// records; this store never invents state. Writes go through the state
// store's CAS (`WriteDocState` with the hash read moments before): a CAS
// conflict is a typed error and the LOSER RE-RUNS — never an overwrite
// (R15 posture; orchestrator concern).
//
// Content addressing uses the same canonical-JSON protocol as the state
// store's own serialization (marshal → generic decode with UseNumber →
// docutil.CanonicalJSON), so the same logical report hashes identically
// across processes.
//
// Porting notes:
//
//   - The TS StateStore interface becomes the DocStateStore interface below
//     (the exact two methods used); the concrete *state.StateStore satisfies
//     it, and the seam keeps the CAS-conflict branch testable exactly as the
//     TS withStaleWriteHash wrapper did.
//   - The zod entry parses (VERIFICATION_RECORD_ENTRY_SCHEMA.parse /
//     DISAGREEMENT_ENTRY_SCHEMA.parse) are owned by the doc-state registry
//     entry in Go: WriteDocState re-validates the whole record — the new
//     entry included — BEFORE any byte moves, so an invalid report or an
//     unknown human_outcome still fails closed with the registry's schema
//     error (the zod ZodError analogue).

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// CodeVerificationStorage preserves the TS class's machine-readable code
// (VerificationStorageError.code === 'VERIFICATION_STORAGE'). It is not a
// docutil code: the wrapped docutil error carries EDOCSYNC_VERIFY for
// routing (CONVENTIONS.md §Errors — the closest registered code).
const CodeVerificationStorage = "VERIFICATION_STORAGE"

// VerificationStorageError is returned when a verification record cannot be
// persisted fail-closed (the TS VerificationStorageError class): the missing
// doc-state record, and the CAS conflict. The message is the TS
// constructor's text, verbatim.
type VerificationStorageError struct {
	Msg   string
	coded *docutil.Error
}

func newVerificationStorageError(msg string) *VerificationStorageError {
	return &VerificationStorageError{Msg: msg, coded: docutil.New(docutil.CodeVerify, msg)}
}

func (e *VerificationStorageError) Error() string { return e.Msg }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *VerificationStorageError) Unwrap() error { return e.coded }

// Code returns the TS class code for this refusal (VERIFICATION_STORAGE).
func (e *VerificationStorageError) Code() string { return CodeVerificationStorage }

// DisagreementInput is the input ONE disagreement record needs (R5
// first-class metric input).
type DisagreementInput struct {
	// DocPath is the document the disagreement is about (the doc-state
	// record's key).
	DocPath string
	// RunID is the run whose verifier verdict is being contradicted.
	RunID string
	// VerifierVerdict is what the verifier concluded (pass | review | reject).
	VerifierVerdict schemas.VerificationVerdict
	// HumanOutcome is what actually happened (closed taxonomy; the schema
	// rejects unknowns).
	HumanOutcome string
	// Reason is the non-empty human-readable reason (part of the dedup
	// identity).
	Reason string
	// PatchRef is the proposed patch's ref, when one existed.
	PatchRef *string
	// At is the INJECTED timestamp (ISO-8601; never a local Date).
	At string
}

// VerificationSaveOutcome is the save result: the content address, and
// whether the entry was created (a duplicate report_hash is a no-op).
type VerificationSaveOutcome struct {
	ReportHash string
	Created    bool
}

// DisagreementOutcome is the disagreement result (created:false on a dedup
// no-op).
type DisagreementOutcome struct {
	Created bool
}

// DocStateStore is the exact slice of the state store the verification
// ledger needs (the TS StateStore interface the suite wrapped to force the
// CAS seam; the concrete *state.StateStore satisfies it).
type DocStateStore interface {
	ReadDocState(docPath string) (state.StateRead[*schemas.DocState], error)
	WriteDocState(docPath string, docState schemas.DocState, expectedHash *string) (state.CasWriteResult, error)
}

// VerificationRecordStore is the append-only verification-record store over
// the D1 doc-state records. Append-only at the type level: only Save / List
// / RecordDisagreement exist — no delete, no update, no reorder.
type VerificationRecordStore struct {
	store DocStateStore
}

// NewVerificationRecordStore builds the store over a state store (the TS
// constructor).
func NewVerificationRecordStore(store DocStateStore) *VerificationRecordStore {
	return &VerificationRecordStore{store: store}
}

// Save appends ONE verification-record entry (the report embedded whole,
// content-addressed by its canonical-JSON sha256). Duplicate `report_hash`
// ⇒ created:false with NO write (idempotent). Missing doc-state record ⇒
// typed fail-closed error (state is never invented here). CAS conflict ⇒
// typed error; the loser re-runs (R15).
func (s *VerificationRecordStore) Save(report schemas.VerificationReport, now func() string) (VerificationSaveOutcome, error) {
	reportHash, err := reportContentHash(report)
	if err != nil {
		return VerificationSaveOutcome{}, err
	}
	read, err := s.store.ReadDocState(report.DocPath)
	if err != nil {
		return VerificationSaveOutcome{}, err
	}
	if !read.Exists || read.Content == nil {
		return VerificationSaveOutcome{}, newVerificationStorageError(
			missingDocStateMessage(report.DocPath, "verification records"))
	}
	current := read.Content
	for _, entry := range current.Verifications {
		if entry.ReportHash == reportHash {
			return VerificationSaveOutcome{ReportHash: reportHash, Created: false}, nil
		}
	}
	entry := schemas.VerificationRecordEntry{
		ReportHash: reportHash,
		Report:     report,
		At:         now(),
		RunID:      report.RunID,
	}
	next := *current
	next.Verifications = append(append([]schemas.VerificationRecordEntry{}, current.Verifications...), entry)
	write, err := s.store.WriteDocState(report.DocPath, next, storageStringPtr(read.Hash))
	if err != nil {
		return VerificationSaveOutcome{}, err
	}
	if !write.OK {
		return VerificationSaveOutcome{}, newVerificationStorageError(casConflictMessage(
			"verification record", report.DocPath, storageStringPtr(read.Hash), write.ActualHash))
	}
	return VerificationSaveOutcome{ReportHash: reportHash, Created: true}, nil
}

// List returns the doc's verification ledger, sorted by (at, report_hash) —
// the deterministic audit-replay order. A missing doc-state record reads as
// an empty ledger (a soft read outcome, mirroring the state store's read
// semantics); every other failure propagates.
func (s *VerificationRecordStore) List(docPath string) ([]schemas.VerificationRecordEntry, error) {
	read, err := s.store.ReadDocState(docPath)
	if err != nil {
		return nil, err
	}
	if !read.Exists || read.Content == nil {
		return nil, nil
	}
	entries := append([]schemas.VerificationRecordEntry{}, read.Content.Verifications...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].At != entries[j].At {
			return entries[i].At < entries[j].At
		}
		return entries[i].ReportHash < entries[j].ReportHash
	})
	return entries, nil
}

// RecordDisagreement appends ONE disagreement entry (verifier verdict
// contradicted by the human outcome — R5: first-class metric input for
// Phase 10). Deduped on EXACTLY the 4-tuple (run_id, verifier_verdict,
// human_outcome, reason) — `at` and `patch_ref` are excluded from the
// identity; an unknown `human_outcome` is rejected by the registered schema
// (fail closed). Same missing-record and CAS postures as Save.
func (s *VerificationRecordStore) RecordDisagreement(input DisagreementInput) (DisagreementOutcome, error) {
	entry := schemas.DisagreementEntry{
		At:              input.At,
		RunID:           input.RunID,
		VerifierVerdict: input.VerifierVerdict,
		HumanOutcome:    input.HumanOutcome,
		Reason:          input.Reason,
		PatchRef:        input.PatchRef,
	}
	read, err := s.store.ReadDocState(input.DocPath)
	if err != nil {
		return DisagreementOutcome{}, err
	}
	if !read.Exists || read.Content == nil {
		return DisagreementOutcome{}, newVerificationStorageError(
			missingDocStateMessage(input.DocPath, "disagreement records"))
	}
	current := read.Content
	for _, candidate := range current.Disagreements {
		if candidate.RunID == entry.RunID &&
			candidate.VerifierVerdict == entry.VerifierVerdict &&
			candidate.HumanOutcome == entry.HumanOutcome &&
			candidate.Reason == entry.Reason {
			return DisagreementOutcome{Created: false}, nil
		}
	}
	next := *current
	next.Disagreements = append(append([]schemas.DisagreementEntry{}, current.Disagreements...), entry)
	write, err := s.store.WriteDocState(input.DocPath, next, storageStringPtr(read.Hash))
	if err != nil {
		return DisagreementOutcome{}, err
	}
	if !write.OK {
		return DisagreementOutcome{}, newVerificationStorageError(casConflictMessage(
			"disagreement record", input.DocPath, storageStringPtr(read.Hash), write.ActualHash))
	}
	return DisagreementOutcome{Created: true}, nil
}

/* ------------------------------------------------------------- internals */

// reportContentHash is the OD-R42 bare-digest convention: sha256 over the
// CANONICAL JSON of the report (the same key order as the state store's own
// serialization for JSON-parsed data, so the same logical report hashes
// identically across processes).
func reportContentHash(report schemas.VerificationReport) (string, error) {
	canonical, err := canonicalValueBytes(report)
	if err != nil {
		return "", err
	}
	return docutil.ContentHash(canonical), nil
}

// canonicalValueBytes renders a value as canonical JSON: marshal, decode
// into generic form (UseNumber keeps integers integral), then
// docutil.CanonicalJSON — the exact protocol the state store uses for its
// own bytes.
func canonicalValueBytes(value any) (string, error) {
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

// missingDocStateMessage ports the TS fail-closed constructor text for a
// missing doc-state record (the record kind distinguishes the ledger from
// the disagreement log).
func missingDocStateMessage(docPath, recordKind string) string {
	return `no doc-state record for "` + docPath + `" — the orchestrator initializes ` +
		`document state before persisting ` + recordKind +
		` (fail closed; state is never invented)`
}

// casConflictMessage ports the TS CAS-conflict constructor text (the loser
// re-runs, never overwrites — R15 posture).
func casConflictMessage(recordKind, docPath string, expected, actual *string) string {
	return `CAS conflict persisting the ` + recordKind + ` for "` + docPath + `": expected ` +
		storageHashOrNull(expected) + `, actual ` + storageHashOrNull(actual) +
		` — the loser re-runs, never overwrites (R15 posture)`
}

// storageHashOrNull ports the TS `hash ?? 'null'` rendering of the
// expected/actual hash pair.
func storageHashOrNull(hash *string) string {
	if hash == nil {
		return "null"
	}
	return *hash
}

// storageStringPtr takes the address of a hash string (the Go twin of the TS
// non-null expected hash).
func storageStringPtr(hash string) *string {
	return &hash
}
