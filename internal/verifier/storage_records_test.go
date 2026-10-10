package verifier_test

// Black-box port of packages/verifier/test/storage.test.ts (P4-T20;
// V-P4-13, V-P4-14): append-only + content-addressed + provenance-pinned
// ledger (dedup by `report_hash`, no delete/update API), deterministic
// (at, report_hash) listing order, the fail-closed missing-doc-state error,
// the CAS-conflict typed error (R15: the loser re-runs), and the
// verifier-vs-human disagreement log with its 4-tuple dedup identity (R5 —
// Phase-10 metric input).
//
// The TS suite took its reports from the real `verifyPatch` over the
// payments fixture (pipeline/ is a separate delegate's unit); the store logic
// under test is report-shape agnostic, so schema-valid report literals carry
// the same coverage here. Reports are never clock-generated: timestamps are
// injected through the `now` parameter (CONVENTIONS.md determinism rule).

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	paymentsDoc   = "docs/payments.md"
	unlistedDoc   = "docs/unlisted.md"
	storageRunID  = "run_01JAUDITRUN0000000000"
	storageT0     = "2026-10-06T12:00:00.000Z"
	storageT1     = "2026-10-06T12:31:00.000Z"
	storageReason = "the human reviewer still found the claim stale"
)

// newStateStore builds a state store over a fresh temp `.docsync/state`.
func newStateStore(t *testing.T) *state.StateStore {
	t.Helper()
	store, err := state.NewStateStore(state.StateStoreOptions{
		StateDir: filepath.Join(t.TempDir(), ".docsync", "state"),
	})
	require.NoError(t, err)
	return store
}

// seedDocState creates the doc-state record the ledger hangs off — the
// orchestrator initializes document state before persisting verification
// records (fail closed; state is never invented).
func seedDocState(t *testing.T, store *state.StateStore, docPath string) {
	t.Helper()
	write, err := store.WriteDocState(docPath, schemas.DocState{
		Path:          docPath,
		State:         schemas.DocStateSuspect,
		Since:         storageT0,
		PolicyVersion: 17,
		EvidenceRefs:  []string{},
	}, nil)
	require.NoError(t, err)
	require.True(t, write.OK, "doc-state seed write must land")
}

// tier builds one all-green tier report over the closed check-id vocabulary.
func tier(tierNumber int, ids ...string) schemas.VerificationTierReport {
	checks := make([]schemas.VerificationCheck, 0, len(ids))
	for _, id := range ids {
		checks = append(checks, schemas.VerificationCheck{ID: id, Status: schemas.CheckPass})
	}
	return schemas.VerificationTierReport{Tier: tierNumber, Status: schemas.CheckPass, Checks: checks}
}

// validReport is a schema-valid `docsync.verification-report/v1`.
func validReport(docPath, runID string) schemas.VerificationReport {
	return schemas.VerificationReport{
		Schema:   "docsync.verification-report/v1",
		RunID:    runID,
		DocPath:  docPath,
		PatchRef: "state/docs/docs__payments.md#proposal-01JAUDIT",
		Tiers: []schemas.VerificationTierReport{
			tier(1,
				"patch-applies-cleanly", "manifest-allowlist-only", "ownership-floor",
				"minimality-bound", "whole-document-rewrite", "structural-integrity",
				"link-resolution", "examples-parse"),
			tier(2,
				"claim-decomposition", "claim-anchor-binding", "head-verification",
				"over-deletion-guard", "contract-aware-check", "coverage-defect"),
			tier(3, "judge-second-pass", "judge-independence"),
		},
		Claims:           []schemas.VerificationClaim{{ClaimID: "claim_182", Status: schemas.ClaimVerdictSupported}},
		Verdict:          schemas.VerdictPass,
		AutoEligible:     true,
		AutoBlockReasons: []string{},
		PolicyVersion:    17,
	}
}

// canonicalReportHash recomputes the OD-R42 bare digest over the canonical
// JSON of a report (the TS suite's
// `createHash('sha256').update(canonicalJson(report)).digest('hex')`).
func canonicalReportHash(t *testing.T, report schemas.VerificationReport) string {
	t.Helper()
	wire, err := json.Marshal(report)
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(wire))
	dec.UseNumber()
	var generic any
	require.NoError(t, dec.Decode(&generic))
	canonical, err := docutil.CanonicalJSON(generic)
	require.NoError(t, err)
	return docutil.ContentHash(canonical)
}

// findEntry returns the ledger entry with the wanted content address.
func findEntry(entries []schemas.VerificationRecordEntry, reportHash string) *schemas.VerificationRecordEntry {
	for i := range entries {
		if entries[i].ReportHash == reportHash {
			return &entries[i]
		}
	}
	return nil
}

// staleWriteStore forces the CAS-conflict branch deterministically: every
// write expects a stale hash (the TS withStaleWriteHash wrapper — the real
// store never conflicts in a single-process test; R15's loser-re-runs
// posture is still pinned).
type staleWriteStore struct {
	inner *state.StateStore
}

func (s staleWriteStore) ReadDocState(docPath string) (state.StateRead[*schemas.DocState], error) {
	return s.inner.ReadDocState(docPath)
}

func (s staleWriteStore) WriteDocState(docPath string, docState schemas.DocState, expectedHash *string) (state.CasWriteResult, error) {
	stale := strings.Repeat("0", 64)
	return s.inner.WriteDocState(docPath, docState, &stale)
}

func TestV_P4_13_DedupsByReportHashAppendsDistinctListsDeterministically(t *testing.T) {
	store := newStateStore(t)
	seedDocState(t, store, paymentsDoc)
	records := verifier.NewVerificationRecordStore(store)

	report := validReport(paymentsDoc, storageRunID)
	variant := report
	variant.RunID = storageRunID + "#variant"
	earlier := report
	earlier.RunID = storageRunID + "#earlier"

	// first save creates the entry, content-addressed by the canonical report
	first, err := records.Save(report, func() string { return storageT1 })
	require.NoError(t, err)
	expectedHash := canonicalReportHash(t, report)
	assert.Equal(t, expectedHash, first.ReportHash)
	assert.True(t, first.Created)
	assert.True(t, schemas.IsBareSHA256Hex(first.ReportHash), "report_hash is bare 64-hex sha256")

	// re-saving the SAME report dedups: created:false, NO second entry
	second, err := records.Save(report, func() string { return storageT1 })
	require.NoError(t, err)
	assert.Equal(t, expectedHash, second.ReportHash)
	assert.False(t, second.Created)
	listed, err := records.List(paymentsDoc)
	require.NoError(t, err)
	assert.Len(t, listed, 1)

	// a DIFFERENT report appends (append-only ledger grows)
	third, err := records.Save(variant, func() string { return storageT1 })
	require.NoError(t, err)
	assert.True(t, third.Created)
	// an earlier `at` sorts first (deterministic (at, report_hash) order)
	_, err = records.Save(earlier, func() string { return storageT0 })
	require.NoError(t, err)

	entries, err := records.List(paymentsDoc)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, []string{storageT0, storageT1, storageT1},
		[]string{entries[0].At, entries[1].At, entries[2].At})
	sameAt := []string{entries[1].ReportHash, entries[2].ReportHash}
	sorted := append([]string{}, sameAt...)
	sort.Strings(sorted)
	assert.Equal(t, sorted, sameAt, "same-at entries sort by report_hash")

	// provenance pins: the full embedded report + run id + timestamp
	pinned := findEntry(entries, expectedHash)
	require.NotNil(t, pinned)
	assert.Equal(t, report, pinned.Report)
	assert.Equal(t, storageRunID, pinned.RunID)
	assert.Equal(t, storageT1, pinned.At)
	for i := range entries {
		assert.Empty(t, entries[i].Report.ValidateReport(),
			"every embedded report is schema-valid")
	}

	// append-only surface: NO delete/update method exists. (The TS suite
	// asserted the prototype's exact own-property set; Go pins the exported
	// method set of the store type instead.)
	methods := reflect.TypeOf(records)
	assert.Equal(t, 3, methods.NumMethod())
	names := make([]string, 0, methods.NumMethod())
	for i := 0; i < methods.NumMethod(); i++ {
		names = append(names, methods.Method(i).Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"List", "RecordDisagreement", "Save"}, names)
}

func TestV_P4_13_MissingDocStateRecordFailsClosed(t *testing.T) {
	store := newStateStore(t)
	records := verifier.NewVerificationRecordStore(store)
	report := validReport(unlistedDoc, storageRunID)

	_, err := records.Save(report, func() string { return storageT1 })
	require.Error(t, err)
	var storageErr *verifier.VerificationStorageError
	require.True(t, errors.As(err, &storageErr), "want *verifier.VerificationStorageError, got %T", err)
	assert.Equal(t, verifier.CodeVerificationStorage, storageErr.Code())
	assert.Contains(t, err.Error(), `no doc-state record for "`+unlistedDoc+`"`)
	assert.Contains(t, err.Error(), "state is never invented")

	listed, err := records.List(unlistedDoc)
	require.NoError(t, err)
	assert.Empty(t, listed, "a missing doc-state record reads as an empty ledger")
}

func TestV_P4_13_CASConflictIsATypedErrorLoserReruns(t *testing.T) {
	store := newStateStore(t)
	seedDocState(t, store, paymentsDoc)
	records := verifier.NewVerificationRecordStore(store)
	report := validReport(paymentsDoc, storageRunID)
	_, err := records.Save(report, func() string { return storageT1 })
	require.NoError(t, err)

	variant := report
	variant.RunID = storageRunID + "#variant"
	stale := verifier.NewVerificationRecordStore(staleWriteStore{inner: store})
	_, err = stale.Save(variant, func() string { return storageT1 })
	require.Error(t, err)
	var storageErr *verifier.VerificationStorageError
	require.True(t, errors.As(err, &storageErr), "want *verifier.VerificationStorageError, got %T", err)
	assert.Equal(t, verifier.CodeVerificationStorage, storageErr.Code())
	assert.Equal(t, docutil.CodeVerify, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "CAS conflict persisting the verification record")
	assert.Contains(t, err.Error(), "the loser re-runs, never overwrites (R15 posture)")

	// the conflicted save wrote nothing — the ledger is untouched
	entries, err := records.List(paymentsDoc)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestV_P4_14_DisagreementsAppendDedupeThe4TupleRejectUnknownOutcomes(t *testing.T) {
	store := newStateStore(t)
	seedDocState(t, store, paymentsDoc)
	records := verifier.NewVerificationRecordStore(store)
	patchRef := "state/docs/docs__payments.md#patch-0000000000000000"
	input := verifier.DisagreementInput{
		DocPath:         paymentsDoc,
		RunID:           storageRunID,
		VerifierVerdict: schemas.VerdictPass,
		HumanOutcome:    "rejected",
		Reason:          storageReason,
		PatchRef:        &patchRef,
		At:              "2026-10-06T13:00:00.000Z",
	}

	first, err := records.RecordDisagreement(input)
	require.NoError(t, err)
	assert.True(t, first.Created)

	// identical input dedups on the 4-tuple identity
	duplicate, err := records.RecordDisagreement(input)
	require.NoError(t, err)
	assert.False(t, duplicate.Created)

	// a different timestamp is still a duplicate (`at` is excluded)
	otherAt := input
	otherAt.At = "2026-10-06T14:00:00.000Z"
	duplicateAt, err := records.RecordDisagreement(otherAt)
	require.NoError(t, err)
	assert.False(t, duplicateAt.Created)

	// a different reason is a DIFFERENT event
	different := input
	different.Reason = "edited instead"
	differentOutcome, err := records.RecordDisagreement(different)
	require.NoError(t, err)
	assert.True(t, differentOutcome.Created)

	// entries are pinned in the doc state with their provenance
	read, err := store.ReadDocState(paymentsDoc)
	require.NoError(t, err)
	require.NotNil(t, read.Content)
	disagreements := read.Content.Disagreements
	require.Len(t, disagreements, 2)
	assert.Equal(t, "2026-10-06T13:00:00.000Z", disagreements[0].At)
	assert.Equal(t, storageRunID, disagreements[0].RunID)
	assert.Equal(t, schemas.VerdictPass, disagreements[0].VerifierVerdict)
	assert.Equal(t, "rejected", disagreements[0].HumanOutcome)
	assert.Equal(t, storageReason, disagreements[0].Reason)
	require.NotNil(t, disagreements[0].PatchRef)
	assert.Equal(t, patchRef, *disagreements[0].PatchRef)
	assert.Equal(t, "edited instead", disagreements[1].Reason)

	// unknown human_outcome is rejected by the registered schema (fail closed)
	bad := input
	bad.HumanOutcome = "maybe"
	_, err = records.RecordDisagreement(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "human_outcome")

	// the refused entry never landed — the log stays at two entries
	read, err = store.ReadDocState(paymentsDoc)
	require.NoError(t, err)
	require.NotNil(t, read.Content)
	assert.Len(t, read.Content.Disagreements, 2)
}

func TestV_P4_14_MissingDocStateRecordFailsClosedForDisagreements(t *testing.T) {
	store := newStateStore(t)
	records := verifier.NewVerificationRecordStore(store)

	_, err := records.RecordDisagreement(verifier.DisagreementInput{
		DocPath:         unlistedDoc,
		RunID:           storageRunID,
		VerifierVerdict: schemas.VerdictReview,
		HumanOutcome:    "accepted",
		Reason:          "unlisted doc has no state record",
		At:              storageT1,
	})
	require.Error(t, err)
	var storageErr *verifier.VerificationStorageError
	require.True(t, errors.As(err, &storageErr), "want *verifier.VerificationStorageError, got %T", err)
	assert.Contains(t, err.Error(), `no doc-state record for "`+unlistedDoc+`"`)
	assert.Contains(t, err.Error(), "disagreement records")
}

func TestV_P4_14_ReportHashIsStableAcrossStores(t *testing.T) {
	// The content address is a pure function of the canonical report bytes —
	// the OD-R42 digest must not depend on the store instance or the run.
	storeA := newStateStore(t)
	storeB := newStateStore(t)
	seedDocState(t, storeA, paymentsDoc)
	seedDocState(t, storeB, paymentsDoc)
	recordsA := verifier.NewVerificationRecordStore(storeA)
	recordsB := verifier.NewVerificationRecordStore(storeB)

	report := validReport(paymentsDoc, storageRunID)
	first, err := recordsA.Save(report, func() string { return storageT1 })
	require.NoError(t, err)
	second, err := recordsB.Save(report, func() string { return storageT1 })
	require.NoError(t, err)
	assert.Equal(t, first.ReportHash, second.ReportHash)
	assert.Equal(t, canonicalReportHash(t, report), second.ReportHash)
}
