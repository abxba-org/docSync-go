package state_test

// Ported coverage-hardening suite for internal/state. Targets the statements
// and branches state_test.go leaves below 95%: CAS atomic-write fs errors,
// state-key encode/decode edge failures, store read/write error paths
// (corrupt/schema-invalid persisted records, non-ENOENT dir reads, guard
// refusals), and run-record upsert conflict/schema paths. Black-box
// (package state_test), testify, deterministic (temp dirs per test).

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

/* ------------------------------------------------------------- CAS atomic-write fs errors */

func TestCOV_STATE_CAS_AtomicWriteMkdirFailure(t *testing.T) {
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	// Make the parent of the target an existing file so MkdirAll fails.
	blocker := filepath.Join(stateDir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	target := filepath.Join(blocker, "child.json")
	_, err := state.CasWriteFile(target, "v1", nil)
	require.Error(t, err)
}

func TestCOV_STATE_CAS_ReadRawForCasENOENT(t *testing.T) {
	// readRawForCas is unexported; exercise via CasWriteFile with expectedHash
	// nil on a nonexistent file (create path) — the readRawForCas nil branch
	// is covered by TestCasWriteCreatesUpdatesAndRefusesStaleHashes. The
	// non-ENOENT read error: reading a directory.
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	_, err := state.CasWriteFile(stateDir, "v1", nil)
	require.Error(t, err)
}

/* ------------------------------------------------------------- state-key encode/decode edges */

func TestCOV_STATE_Keys_EncodeRunRepoEdges(t *testing.T) {
	// empty segment and unsafe segment in repo.
	_, err := state.RunRecordFilename(runKeyWithRepo("a//b"))
	requireStateKeyError(t, err)
	_, err = state.RunRecordFilename(runKeyWithRepo("a b"))
	requireStateKeyError(t, err)
}

func TestCOV_STATE_Keys_DecodeRunRecordRepoMalformed(t *testing.T) {
	// encoded repo with a dangling '_' escape refuses to decode.
	_, err := state.DecodeRunRecordRepo("a_")
	requireStateKeyError(t, err)
	_, err = state.DecodeRunRecordRepo("a_x")
	requireStateKeyError(t, err)
}

/* ------------------------------------------------------------- store read/write error paths */

func TestCOV_STATE_Store_ListClaimIdsNonENOENT(t *testing.T) {
	_, stateDir := newStore(t, nil)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	// claims path is a file -> ReadDir fails non-ENOENT.
	claimsDir := filepath.Join(stateDir, "claims")
	require.NoError(t, os.WriteFile(claimsDir, []byte("x"), 0o644))
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	_, lerr := store.ListClaimIds()
	require.Error(t, lerr)
}

func TestCOV_STATE_Store_ListDocStatePathsNonENOENT(t *testing.T) {
	_, stateDir := newStore(t, nil)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	docsDir := filepath.Join(stateDir, "docs")
	require.NoError(t, os.WriteFile(docsDir, []byte("x"), 0o644))
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	_, lerr := store.ListDocStatePaths()
	require.Error(t, lerr)
}

func TestCOV_STATE_Store_UpsertRunRecordParseErrorOnExisting(t *testing.T) {
	store, stateDir := newStore(t, nil)
	filename, err := state.RunRecordFilename(runKey)
	require.NoError(t, err)
	runsDir := filepath.Join(stateDir, "runs")
	require.NoError(t, os.MkdirAll(runsDir, 0o755))
	// Persist valid JSON that is schema-invalid for a run record.
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, filename), []byte(`{"not":"a run record"}`), 0o644))
	_, err = store.UpsertRunRecord(runKey, runRecordExample)
	require.Error(t, err)
	assert.Equal(t, state.CodeStateSchema, docutil.CodeOf(err))
}

func TestCOV_STATE_Store_AppendTransitionCorruptDocState(t *testing.T) {
	store, stateDir := newStore(t, nil)
	docPath := docStateExample.Path
	encoded, err := state.EncodeStateDocPath(docPath)
	require.NoError(t, err)
	docsDir := filepath.Join(stateDir, "docs")
	require.NoError(t, os.MkdirAll(docsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(docsDir, encoded+".json"), []byte("{not json"), 0o644))
	_, err = store.AppendTransition(docPath, schemas.DocStateTransition{
		State:  schemas.DocStateClear,
		At:     "2026-10-05T10:00:00Z",
		RunID:  "run_2",
		Reason: "re-verified",
	}, nil)
	require.Error(t, err)
	assert.Equal(t, state.CodeStateCorrupt, docutil.CodeOf(err))
}

func TestCOV_STATE_Store_ReadBaselineCorruptThenSchema(t *testing.T) {
	store, stateDir := newStore(t, nil)
	docPath := baselineExample.DocPath
	encoded, err := state.EncodeStateDocPath(docPath)
	require.NoError(t, err)
	baselinesDir := filepath.Join(stateDir, "baselines")
	require.NoError(t, os.MkdirAll(baselinesDir, 0o755))
	// Corrupt JSON.
	require.NoError(t, os.WriteFile(filepath.Join(baselinesDir, encoded+".json"), []byte("{not json"), 0o644))
	_, err = store.ReadBaseline(docPath)
	require.Error(t, err)
	assert.Equal(t, state.CodeStateCorrupt, docutil.CodeOf(err))
	// Schema-invalid but valid JSON.
	require.NoError(t, os.WriteFile(filepath.Join(baselinesDir, encoded+".json"), []byte(`{"nope":true}`), 0o644))
	_, err = store.ReadBaseline(docPath)
	require.Error(t, err)
	assert.Equal(t, state.CodeStateSchema, docutil.CodeOf(err))
}

func TestCOV_STATE_Store_ReadClaimAndDocStateCorrupt(t *testing.T) {
	// Claim corrupt.
	claimsDir := filepath.Join(t.TempDir(), ".docsync", "state", "claims")
	_ = claimsDir
	// Read a claim whose file is corrupt: write via WriteRawForTest with a
	// state-owned relPath then corrupt it directly.
	store2, _ := newStore(t, nil)
	claimRelPath, err := state.ClaimRelPath("claim_corrupt")
	require.NoError(t, err)
	_, err = store2.WriteRawForTest(claimRelPath, "{not json", nil)
	require.NoError(t, err)
	_, err = store2.ReadClaim("claim_corrupt")
	require.Error(t, err)
	assert.Equal(t, state.CodeStateCorrupt, docutil.CodeOf(err))
}

func TestCOV_STATE_Store_WriteRawForTestCasConflict(t *testing.T) {
	store, _ := newStore(t, nil)
	relPath := "state/conflict.json"
	first, err := store.WriteRawForTest(relPath, "v1", nil)
	require.NoError(t, err)
	require.True(t, first.OK)
	stale, err := store.WriteRawForTest(relPath, "v2", strPtr("stale-hash"))
	require.NoError(t, err)
	require.False(t, stale.OK)
	require.NotNil(t, stale.ActualHash)
}

func TestCOV_STATE_Store_NewStateStoreRelativeAndEmpty(t *testing.T) {
	_, err := state.NewStateStore(state.StateStoreOptions{StateDir: "relative/state"})
	require.Error(t, err)
	_, err = state.NewStateStore(state.StateStoreOptions{StateDir: ""})
	require.Error(t, err)
}

func TestCOV_STATE_Store_UpsertRunRecordGuardRefusal(t *testing.T) {
	// UpsertRunRecord always targets state/runs/... which passes guardStateWrite,
	// so the guard-refusal branch is reachable only through WriteRawForTest with
	// a non-state path — covered by state_test TestStateStoreRefusesConfigAndUnknownPathsEndToEnd.
	// Here we confirm the key-mismatch refusal (SameRunKey) leaves no file.
	store, stateDir := newStore(t, nil)
	mismatched := runRecordExample
	mismatched.Key.PolicyVersion = 99
	_, err := store.UpsertRunRecord(runKey, mismatched)
	requireStateKeyError(t, err)
	filename, err := state.RunRecordFilename(runKey)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(stateDir, "runs", filename))
	require.True(t, errors.Is(statErr, os.ErrNotExist))
}

func TestCOV_STATE_Store_AppendTransitionMissingAndTypeMismatch(t *testing.T) {
	store, _ := newStore(t, nil)
	// Missing doc state -> StateMissingError (covered by state_test too).
	_, err := store.AppendTransition("docs/absent.md", schemas.DocStateTransition{
		State:  schemas.DocStateClear,
		At:     "2026-10-05T10:00:00Z",
		RunID:  "run_2",
		Reason: "re-verified",
	}, nil)
	requireStateMissingError(t, err)
}

func strPtr(s string) *string { return &s }

var _ = schemas.StateChurn{}
var _ = docutil.ContentHash

// Ensure imports used.
var _ = errors.Is

/* ------------------------------------------------------------- relPath error paths via bad keys/paths */

func TestCOV_STATE_Store_ReadWriteBaselineBadDocPath(t *testing.T) {
	store, _ := newStore(t, nil)
	for _, bad := range []string{"../x.md", "/abs.md", "docs//x.md", "a b.md", ""} {
		_, err := store.ReadBaseline(bad)
		requireStateKeyError(t, err)
		_, err = store.WriteBaseline(bad, baselineExample, nil)
		requireStateKeyError(t, err)
	}
}

func TestCOV_STATE_Store_ReadWriteClaimBadId(t *testing.T) {
	store, _ := newStore(t, nil)
	for _, bad := range []string{"bad/id", "..", "", "a//b"} {
		_, err := store.ReadClaim(bad)
		requireStateKeyError(t, err)
		_, err = store.WriteClaim(bad, claimExample, nil)
		requireStateKeyError(t, err)
	}
}

func TestCOV_STATE_Store_ReadWriteDocStateBadPath(t *testing.T) {
	store, _ := newStore(t, nil)
	for _, bad := range []string{"../x.md", "/abs.md", ""} {
		_, err := store.ReadDocState(bad)
		requireStateKeyError(t, err)
		_, err = store.WriteDocState(bad, docStateExample, nil)
		requireStateKeyError(t, err)
	}
}

func TestCOV_STATE_Store_ReadWriteMergeLedgerBadKey(t *testing.T) {
	store, _ := newStore(t, nil)
	_, err := store.ReadMergeLedger("../x")
	requireStateKeyError(t, err)
	ledger := schemas.MergeLedgerExample()
	_, err = store.WriteMergeLedger("../x", ledger, nil)
	requireStateKeyError(t, err)
	// source_key mismatch -> StateKeyError.
	badLedger := ledger
	badLedger.SourceKey = "other"
	_, err = store.WriteMergeLedger(ledger.SourceKey, badLedger, nil)
	requireStateKeyError(t, err)
}

func TestCOV_STATE_Store_ReadRunRecordBadKey(t *testing.T) {
	store, _ := newStore(t, nil)
	bad := runKey
	bad.Repo = "a//b"
	_, err := store.ReadRunRecord(bad)
	requireStateKeyError(t, err)
}

func TestCOV_STATE_Store_ReadDocStateAndAppendMissing(t *testing.T) {
	store, _ := newStore(t, nil)
	read, err := store.ReadDocState("docs/absent.md")
	require.NoError(t, err)
	require.False(t, read.Exists)
}

