package state_test

// Wave-2 coverage hardening for internal/state. Targets every remaining
// function/statement below 95%: atomic-write fs error paths, relPath ownership
// edges, encode/decode edge cases, upsert/append/write error branches through
// the queue and the CAS, store helpers, and error constructors.
// Black-box (package state_test), testify, deterministic (temp dirs per test).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

/* ------------------------------------------------------------- atomicWrite: temp file write fails */

func TestCOV2_STATE_AtomicWrite_TempFileFails(t *testing.T) {
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	target := filepath.Join(stateDir, "sub", "out.json")
	blocker := filepath.Join(stateDir, "sub")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o444))
	_, err := state.CasWriteFile(target, "data", nil)
	require.Error(t, err)
}

/* ------------------------------------------------------------- CasWriteFileDetailed: read raw hard error */

func TestCOV2_STATE_CasWriteDetailed_ReadRawFailsHard(t *testing.T) {
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	_, err := state.CasWriteFileDetailed(stateDir, "v1", strPtr("some-hash"))
	require.Error(t, err)
}

/* ------------------------------------------------------------- ReadStateFile: invalid JSON */

func TestCOV2_STATE_ReadStateFile_InvalidJSONCorrupt(t *testing.T) {
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "baselines"), 0o755))
	badFile := filepath.Join(stateDir, "baselines", "bad.json")
	require.NoError(t, os.WriteFile(badFile, []byte("{not json}"), 0o644))
	_, err := state.ReadStateFile[any](badFile)
	require.Error(t, err)
	require.Equal(t, state.CodeStateCorrupt, docutil.CodeOf(err))
}

func TestCOV2_STATE_ReadStateFile_ValidJSONDecodeError(t *testing.T) {
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "baselines"), 0o755))
	badFile := filepath.Join(stateDir, "baselines", "d.json")
	require.NoError(t, os.WriteFile(badFile, []byte(`"just a string"`), 0o644))
	_, err := state.ReadStateFile[map[string]any](badFile)
	require.Error(t, err)
	require.Equal(t, state.CodeStateCorrupt, docutil.CodeOf(err))
}

/* ------------------------------------------------------------- newStateSchemaError */

func TestCOV2_STATE_NewStateSchemaError(t *testing.T) {
	verr := schemas.NewValidationError(schemas.Issue{Path: []string{"x"}, Message: "bad"})
	sse := state.StateSchemaErrorForPath("state/x.json", verr)
	require.NotNil(t, sse)
	require.Contains(t, sse.Summary, "x: bad")
	require.Contains(t, sse.Error(), "EDOCSYNC_STATE_SCHEMA")

	sse2 := state.StateSchemaErrorForPath("state/y.json", errors.New("plain error"))
	require.NotNil(t, sse2)
	require.Contains(t, sse2.Summary, "plain error")
}

/* ------------------------------------------------------------- NewStateStore */

func TestCOV2_STATE_NewStateStore_WithQueue(t *testing.T) {
	stateDir := makeStateDir(t)
	q := state.CreateInProcessQueue()
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir}, q)
	require.NoError(t, err)
	require.NotNil(t, store)
}

func TestCOV2_STATE_NewStateStore_NilQueue(t *testing.T) {
	stateDir := makeStateDir(t)
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	require.NotNil(t, store)
}

/* ------------------------------------------------------------- WriteMergeLedger: source key mismatch */

func TestCOV2_STATE_WriteMergeLedger_SourceKeyMismatch(t *testing.T) {
	store, _ := newStore(t, nil)
	ledger := schemas.MergeLedgerExample()
	badLedger := ledger
	badLedger.SourceKey = "other-key"
	_, err := store.WriteMergeLedger(ledger.SourceKey, badLedger, nil)
	requireStateKeyError(t, err)
}

/* ------------------------------------------------------------- UpsertRunRecord: existing key mismatch */

func TestCOV2_STATE_UpsertRunRecord_ExistingKeyMismatch(t *testing.T) {
	store, stateDir := newStore(t, nil)
	filename, err := state.RunRecordFilename(runKey)
	require.NoError(t, err)
	runsDir := filepath.Join(stateDir, "runs")
	require.NoError(t, os.MkdirAll(runsDir, 0o755))
	diffKey := runKey
	diffKey.PolicyVersion = 99
	diffRecord := runRecordExample
	diffRecord.Key = diffKey
	diffRecord.RunID = "diff-key-run"
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, filename), []byte(canonicalJSON(t, diffRecord)), 0o644))
	_, err = store.UpsertRunRecord(runKey, runRecordExample)
	require.Error(t, err)
	require.Equal(t, state.CodeStateCorrupt, docutil.CodeOf(err))
}

/* ------------------------------------------------------------- AppendTransition */

func TestCOV2_STATE_AppendTransition_TypeMismatch(t *testing.T) {
	store, stateDir := newStore(t, nil)
	docPath := "test/doc.md"
	encoded, err := state.EncodeStateDocPath(docPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "docs", encoded+".json"), []byte(`{"verified_by_run":"r1"}`), 0o644))
	_, err = store.AppendTransition(docPath, schemas.DocStateTransition{
		State: schemas.DocStateClear, At: "2026-10-05T10:00:00Z", RunID: "run_t", Reason: "test",
	}, nil)
	require.Error(t, err)
}

func TestCOV2_STATE_AppendTransition_CorruptExisting(t *testing.T) {
	store, stateDir := newStore(t, nil)
	docPath := docStateExample.Path
	encoded, err := state.EncodeStateDocPath(docPath)
	require.NoError(t, err)
	docsDir := filepath.Join(stateDir, "docs")
	require.NoError(t, os.MkdirAll(docsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(docsDir, encoded+".json"), []byte("{not json"), 0o644))
	_, err = store.AppendTransition(docPath, schemas.DocStateTransition{
		State: schemas.DocStateClear, At: "2026-10-05T10:00:00Z", RunID: "run_t2", Reason: "test",
	}, nil)
	require.Error(t, err)
	require.Equal(t, state.CodeStateCorrupt, docutil.CodeOf(err))
}

func TestCOV2_STATE_AppendTransition_CASConflict(t *testing.T) {
	store, _ := newStore(t, nil)
	docPath := docStateExample.Path
	_, err := store.WriteDocState(docPath, docStateExample, nil)
	require.NoError(t, err)
	staleHash := "deadbeef"
	result, err := store.AppendTransition(docPath, schemas.DocStateTransition{
		State: schemas.DocStateClear, At: "2026-10-05T10:00:00Z", RunID: "stale2", Reason: "stale2",
	}, &staleHash)
	require.NoError(t, err)
	require.False(t, result.OK)
}

/* ------------------------------------------------------------- WriteRawForTest stale hash */

func TestCOV2_STATE_WriteRawForTest_StaleHash(t *testing.T) {
	store, _ := newStore(t, nil)
	relPath := "state/wrtest.json"
	first, err := store.WriteRawForTest(relPath, "v1", nil)
	require.NoError(t, err)
	require.True(t, first.OK)
	result, err := store.WriteRawForTest(relPath, "v2", strPtr("stale"))
	require.NoError(t, err)
	require.False(t, result.OK)
}

/* ------------------------------------------------------------- WriteBaseline: CAS conflict no churn */

func TestCOV2_STATE_WriteBaseline_ConflictNoChurn(t *testing.T) {
	counter := state.CreateStateChurnCounter()
	store, _ := newStore(t, counter)
	docPath := baselineExample.DocPath
	_, err := store.WriteBaseline(docPath, baselineExample, nil)
	require.NoError(t, err)
	require.Equal(t, 1, counter.Snapshot().Added)
	_, err = store.WriteBaseline(docPath, baselineExample, strPtr("stale"))
	require.NoError(t, err)
	require.Equal(t, state.StateChurnCounter{Added: 1}, counter.Snapshot())
}

/* ------------------------------------------------------------- ReadClaim: type mismatch */

func TestCOV2_STATE_ReadClaim_TypeMismatch(t *testing.T) {
	store, stateDir := newStore(t, nil)
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "claims"), 0o755))
	baseline := baselineExample
	raw := canonicalJSON(t, baseline)
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "claims", "test-claim.json"), []byte(raw), 0o644))
	_, err := store.ReadClaim("test-claim")
	require.Error(t, err)
}

/* ------------------------------------------------------------- ListDocStatePaths: decode error with undecodable stem */

func TestCOV2_STATE_ListDocPaths_DecodeError(t *testing.T) {
	_, stateDir := newStore(t, nil)
	docsDir := filepath.Join(stateDir, "docs")
	require.NoError(t, os.MkdirAll(docsDir, 0o755))
	// A stem with a dangling underscore cannot decode.
	require.NoError(t, os.WriteFile(filepath.Join(docsDir, "a_.json"), []byte("{}"), 0o644))
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	paths, err := store.ListDocStatePaths()
	require.Error(t, err)
	require.Nil(t, paths)
}

/* ------------------------------------------------------------- ListDocStatePaths: non-ENOENT readdir */

func TestCOV2_STATE_ListDocPaths_NonENOENT(t *testing.T) {
	_, stateDir := newStore(t, nil)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	docsDir := filepath.Join(stateDir, "docs")
	require.NoError(t, os.WriteFile(docsDir, []byte("x"), 0o644))
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	_, lerr := store.ListDocStatePaths()
	require.Error(t, lerr)
	_ = lerr
}

/* ------------------------------------------------------------- ListClaimIds: non-ENOENT */

func TestCOV2_STATE_ListClaimIds_NonENOENT(t *testing.T) {
	_, stateDir := newStore(t, nil)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	claimsDir := filepath.Join(stateDir, "claims")
	require.NoError(t, os.WriteFile(claimsDir, []byte("x"), 0o644))
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	_, lerr := store.ListClaimIds()
	require.Error(t, lerr)
	_ = lerr
}

/* ------------------------------------------------------------- ReadStateFile: non-ENOENT error */

func TestCOV2_STATE_ReadStateFile_NonENOENT(t *testing.T) {
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	_, err := state.ReadStateFile[any](stateDir)
	require.Error(t, err)
}

/* ------------------------------------------------------------- DecodeStateSegment edges */

func TestCOV2_STATE_DecodeSegment_UnknownEscape(t *testing.T) {
	_, err := state.DecodeStateSegment("a_x")
	requireStateKeyError(t, err)
}

func TestCOV2_STATE_DecodeSegment_DanglingUnderscore(t *testing.T) {
	_, err := state.DecodeStateSegment("a_")
	requireStateKeyError(t, err)
}

/* ------------------------------------------------------------- RunRecordFilename assertComponent */

func TestCOV2_STATE_RunRecordFilename_EmptyBaseSHA(t *testing.T) {
	key := runKey
	key.BaseSHA = ""
	_, err := state.RunRecordFilename(key)
	requireStateKeyError(t, err)
}

func TestCOV2_STATE_RunRecordFilename_UnsafeBaseSHA(t *testing.T) {
	key := runKey
	key.BaseSHA = "8a73#f2c"
	_, err := state.RunRecordFilename(key)
	requireStateKeyError(t, err)
}

func TestCOV2_STATE_RunRecordFilename_EmptyRepo(t *testing.T) {
	key := runKey
	key.Repo = ""
	_, err := state.RunRecordFilename(key)
	requireStateKeyError(t, err)
}

/* ------------------------------------------------------------- encodeRunRepo empty segment */

func TestCOV2_STATE_EncodeRunRepo_EmptySegment(t *testing.T) {
	_, err := state.RunRecordFilename(runKeyWithRepo("acme//payments"))
	requireStateKeyError(t, err)
}

/* ------------------------------------------------------------- RelPath empty edges */

func TestCOV2_STATE_BaselineRelPath_Empty(t *testing.T) {
	_, err := state.BaselineRelPath("")
	requireStateKeyError(t, err)
}

func TestCOV2_STATE_DocLedgerRelPath_Empty(t *testing.T) {
	_, err := state.DocLedgerRelPath("")
	requireStateKeyError(t, err)
}

func TestCOV2_STATE_DocStateRelPath_Empty(t *testing.T) {
	_, err := state.DocStateRelPath("")
	requireStateKeyError(t, err)
}

func TestCOV2_STATE_ClaimRelPath_Empty(t *testing.T) {
	_, err := state.ClaimRelPath("")
	requireStateKeyError(t, err)
}

func TestCOV2_STATE_ClaimRelPath_Unsafe(t *testing.T) {
	_, err := state.ClaimRelPath("bad/id")
	requireStateKeyError(t, err)
	_, err = state.ClaimRelPath("..")
	requireStateKeyError(t, err)
}

/* ------------------------------------------------------------- jsonString helper */

func JsonStringForTest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

func TestCOV2_STATE_WriteRawForTest_NilQueuePath(t *testing.T) {
	store, _ := newStore(t, nil)
	result, err := store.WriteRawForTest("state/test.json", "data", nil)
	require.NoError(t, err)
	require.True(t, result.OK)
	result2, err := store.WriteRawForTest("state/test.json", "data", nil)
	require.NoError(t, err)
	require.False(t, result2.OK)
}


/* ------------------------------------------------------------- ReadMergeLedger empty key */

func TestCOV2_STATE_ReadMergeLedger_EmptyKey(t *testing.T) {
	store, _ := newStore(t, nil)
	_, err := store.ReadMergeLedger("")
	requireStateKeyError(t, err)
}

/* ------------------------------------------------------------- UpsertRunRecord guard refused */

func TestCOV2_STATE_UpsertRunRecord_GuardRefused(t *testing.T) {
	store, _ := newStore(t, nil)
	guardOverride := runKey
	guardOverride.Repo = "a//b"
	_, err := store.UpsertRunRecord(guardOverride, runRecordExample)
	requireStateKeyError(t, err)
}

/* ------------------------------------------------------------- GuardedPathError.Unwrap */

func TestCOV2_STATE_GuardedPathError_Unwrap(t *testing.T) {
	store, _ := newStore(t, nil)
	_, err := store.WriteRawForTest("manifest.yml", "x", nil)
	var gpe *state.GuardedPathError
	require.ErrorAs(t, err, &gpe)
	require.Equal(t, state.CodeBoundary, docutil.CodeOf(err))
}
