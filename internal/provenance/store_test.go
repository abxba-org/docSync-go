package provenance_test

// Black-box port of packages/core/test/provenance.test.ts, store half
// (V-P9-1: schema-invalid records refused pre-write; unsafe run ids refused;
// V-P9-3: byte-identical replay NO-OP and append-only rewrite refusal;
// V-P9-7: the secret-scan gate fires before any byte moves).
//
// Temp dirs via t.TempDir(); no network; no clocks (fields, never generated).

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/provenance"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- helpers */

// makeStore builds a store over a fresh temp `.docsync/state` (the TS makeStore
// helper). The state root is returned alongside it: the TS suite reached into
// the store's private `stateDir`, and the Go suite captures what it constructed.
func makeStore(t *testing.T) (*provenance.ProvenanceStore, string) {
	t.Helper()
	stateRoot := filepath.Join(t.TempDir(), ".docsync", "state")
	store, err := provenance.NewProvenanceStore(provenance.ProvenanceStoreOptions{StateDir: stateRoot})
	require.NoError(t, err)
	return store, stateRoot
}

// requireCoded asserts a docutil-coded error carrying the wanted code.
func requireCoded(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, docutil.CodeOf(err))
}

// archivalBytes reads the archived bytes for a run id.
func archivalBytes(t *testing.T, store *provenance.ProvenanceStore, runID string) []byte {
	t.Helper()
	path, err := store.RecordFSPath(runID)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

/* ----------------------------------------------------------------- tests */

func TestNewProvenanceStore_RefusesRelativeStateDir(t *testing.T) {
	// The TS constructor threw a TypeError for a non-absolute stateDir.
	store, err := provenance.NewProvenanceStore(provenance.ProvenanceStoreOptions{
		StateDir: filepath.Join("relative", "docsync", "state"),
	})
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "ProvenanceStore.stateDir must be an absolute path")

	_, err = provenance.NewProvenanceStore(provenance.ProvenanceStoreOptions{StateDir: ""})
	require.Error(t, err)
}

func TestProvenanceRecordRelPath_IsOdrr44Compliant(t *testing.T) {
	relPath, err := provenance.ProvenanceRecordRelPath("run_01J9ZK6X8A")
	require.NoError(t, err)
	assert.Equal(t, "state/runs/prov-run_01J9ZK6X8A.json", relPath)

	_, err = provenance.ProvenanceRecordRelPath("../escape")
	require.Error(t, err)
	requireCoded(t, err, docutil.CodeStateKey)
}

func TestV_P9_1_StoreRefusesSchemaInvalidRecordPreWrite(t *testing.T) {
	store, _ := makeStore(t)

	// A half-record: the schema tag and the run id only.
	bogus := schemas.Provenance{Schema: schemas.ProvenanceSchemaID, RunID: runID}
	_, err := store.Write(bogus)
	require.Error(t, err)

	// Typed refusal: StateSchemaError (EDOCSYNC_STATE_SCHEMA) — no file lands.
	var schemaErr *state.StateSchemaError
	require.True(t, errors.As(err, &schemaErr), "want *state.StateSchemaError, got %T", err)
	assert.Equal(t, state.CodeStateSchema, docutil.CodeOf(err))
	assert.Contains(t, schemaErr.Summary, "source_commit")

	ids, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, ids)

	read, err := store.Read(runID)
	require.NoError(t, err)
	assert.False(t, read.Exists)
}

func TestV_P9_1_UnsafeRunIDsAreRefusedOnWriteAndRead(t *testing.T) {
	store, _ := makeStore(t)

	// The TS suite forged the unsafe id by spreading a BUILT record — the
	// builder itself refuses it, so the fixture is a valid record with the id
	// overwritten afterwards.
	unsafe := *buildRecord(t, nil)
	unsafe.RunID = "../escape"
	_, err := store.Write(unsafe)
	requireCoded(t, err, docutil.CodeStateKey)
	assert.Contains(t, err.Error(), `provenance run id "../escape" is not filename-safe`)

	_, err = store.Read("../escape")
	requireCoded(t, err, docutil.CodeStateKey)

	_, err = store.Read("")
	requireCoded(t, err, docutil.CodeStateKey)
	assert.Contains(t, err.Error(), "provenance run id is empty")
}

func TestV_P9_3_ReplayNoOpLeavesArchivalStateByteIdentical(t *testing.T) {
	store, _ := makeStore(t)
	record := buildRecord(t, nil)

	first, err := store.Write(*record)
	require.NoError(t, err)
	require.True(t, first.OK)
	assert.True(t, first.Created)
	assert.False(t, first.Replay)
	assert.NotEmpty(t, first.Hash)

	bytesBefore := archivalBytes(t, store, record.RunID)

	// Re-running the same run is a NO-OP, never a rewrite.
	second, err := store.Write(*record)
	require.NoError(t, err)
	require.True(t, second.OK)
	assert.False(t, second.Created)
	assert.True(t, second.Replay)
	assert.Equal(t, first.Hash, second.Hash)

	assert.Equal(t, bytesBefore, archivalBytes(t, store, record.RunID))

	ids, err := store.List()
	require.NoError(t, err)
	assert.Equal(t, []string{record.RunID}, ids)

	read, err := store.Read(record.RunID)
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.NotNil(t, read.Content)
	assert.Equal(t, *record, *read.Content)
	assert.Equal(t, first.Hash, read.Hash)
	assert.Equal(t, string(bytesBefore), read.Raw)
}

func TestV_P9_3_RewriteAttemptThrowsAndLeavesOriginalIntact(t *testing.T) {
	store, _ := makeStore(t)
	record := buildRecord(t, nil)

	_, err := store.Write(*record)
	require.NoError(t, err)
	bytesBefore := archivalBytes(t, store, record.RunID)

	// Same run id, different bytes: a history-rewrite attempt (bug class).
	forged := buildRecord(t, func(in *provenance.BuildProvenanceInput) {
		in.DetectorConfidence = 0.5
	})
	_, err = store.Write(*forged)
	require.Error(t, err)
	var appendErr *provenance.ProvenanceAppendError
	require.True(t, errors.As(err, &appendErr), "want *provenance.ProvenanceAppendError, got %T", err)
	assert.Equal(t, record.RunID, appendErr.RunID)
	assert.Contains(t, err.Error(), "append-only archival refuses history rewrites")
	assert.Contains(t, err.Error(), "existing sha256 ")
	requireCoded(t, err, docutil.CodeState)

	// The original bytes survive verbatim.
	assert.Equal(t, bytesBefore, archivalBytes(t, store, record.RunID))
	ids, err := store.List()
	require.NoError(t, err)
	assert.Equal(t, []string{record.RunID}, ids)
}

func TestV_P9_3_ListIsSortedAndAbsentReadIsSoft(t *testing.T) {
	store, _ := makeStore(t)

	second := buildRecord(t, func(in *provenance.BuildProvenanceInput) { in.RunID = "run_b" })
	first := buildRecord(t, func(in *provenance.BuildProvenanceInput) { in.RunID = "run_a" })

	_, err := store.Write(*second)
	require.NoError(t, err)
	_, err = store.Write(*first)
	require.NoError(t, err)

	ids, err := store.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"run_a", "run_b"}, ids)

	read, err := store.Read("missing")
	require.NoError(t, err)
	assert.False(t, read.Exists)
	assert.Empty(t, read.Raw)
	assert.Empty(t, read.Hash)
	assert.Nil(t, read.Content)
}

func TestV_P9_3_UnknownKeysOnDiskAreRefusedAtRead(t *testing.T) {
	// Fail closed: a stored file carrying unknown keys is schema drift, not a
	// readable record (the Go registry validates wire bytes strictly).
	store, _ := makeStore(t)
	record := buildRecord(t, nil)
	_, err := store.Write(*record)
	require.NoError(t, err)

	path, err := store.RecordFSPath(record.RunID)
	require.NoError(t, err)
	archived := archivalBytes(t, store, record.RunID)
	mutated := append(append([]byte{}, archived[:len(archived)-1]...), []byte(`,"unexpected":1}`)...)
	require.NoError(t, os.WriteFile(path, mutated, 0o644))

	_, err = store.Read(record.RunID)
	var schemaErr *state.StateSchemaError
	require.True(t, errors.As(err, &schemaErr), "want *state.StateSchemaError, got %T", err)
	requireCoded(t, err, state.CodeStateSchema)
}

func TestProvenanceStore_CorruptExistingFileFailsClosed(t *testing.T) {
	// An existing file that is not valid JSON is CORRUPT state, never
	// "treated as empty" and never a rewrite refusal.
	store, stateRoot := makeStore(t)
	runsDir := filepath.Join(stateRoot, "runs")
	require.NoError(t, os.MkdirAll(runsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, "prov-"+runID+".json"), []byte("{not json"), 0o644))

	record := buildRecord(t, nil)
	_, err := store.Write(*record)
	requireCoded(t, err, state.CodeStateCorrupt)
}

func TestV_P9_7_SecretCarryingRecordIsRefusedPreWrite(t *testing.T) {
	store, stateRoot := makeStore(t)
	dirty := recordWithSecret(t, "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6qR")

	_, err := store.Write(*dirty)
	require.Error(t, err)
	var secretErr *provenance.ProvenanceSecretError
	require.True(t, errors.As(err, &secretErr), "want *provenance.ProvenanceSecretError, got %T", err)
	require.NotEmpty(t, secretErr.Findings)
	assert.Contains(t, err.Error(), "provenance record refused by the write-path secret scan (1 finding(s))")
	requireCoded(t, err, docutil.CodeGuard)

	// Nothing landed: neither the record nor the runs/ directory.
	ids, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, ids)

	_, statErr := os.Stat(filepath.Join(stateRoot, "runs"))
	require.True(t, os.IsNotExist(statErr), "runs/ must not exist after a refused write")
}

func TestProvenanceStore_ConcurrentReplaysAreSerialized(t *testing.T) {
	// Concurrent identical writes of the SAME run: exactly one creates, the
	// rest are byte-identical replay no-ops (append-only, never a rewrite).
	store, _ := makeStore(t)
	record := buildRecord(t, nil)

	const writers = 8
	var wg sync.WaitGroup
	results := make([]provenance.ProvenanceWriteResult, writers)
	errs := make([]error, writers)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = store.Write(*record)
		}(i)
	}
	close(start)
	wg.Wait()

	created := 0
	for i := 0; i < writers; i++ {
		require.NoError(t, errs[i])
		require.True(t, results[i].OK)
		if results[i].Created {
			created++
		}
	}
	assert.Equal(t, 1, created)

	ids, err := store.List()
	require.NoError(t, err)
	assert.Equal(t, []string{record.RunID}, ids)
}

func TestProvenanceStore_ConcurrentDistinctRunsAllLand(t *testing.T) {
	store, _ := makeStore(t)
	var wg sync.WaitGroup
	for _, id := range []string{"run_a", "run_b", "run_c", "run_d"} {
		wg.Add(1)
		go func(runID string) {
			defer wg.Done()
			_, err := store.Write(*buildRecord(t, func(in *provenance.BuildProvenanceInput) { in.RunID = runID }))
			assert.NoError(t, err)
		}(id)
	}
	wg.Wait()

	ids, err := store.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"run_a", "run_b", "run_c", "run_d"}, ids)
}

func TestRecordFSPath_RefusesUnsafeRunID(t *testing.T) {
	store, _ := makeStore(t)
	_, err := store.RecordFSPath("../escape")
	requireCoded(t, err, docutil.CodeStateKey)
}

func TestProvenanceErrorTypesExposeCodedSurfaces(t *testing.T) {
	// The TS classes each carried a distinguishable code; the Go types keep
	// that identity through Code() / docutil.CodeOf / errors.As.
	appendErr := provenance.NewProvenanceAppendError("run_x", "existing sha256 abc differs from the incoming bytes")
	assert.Equal(t, docutil.CodeState, appendErr.Code())
	assert.Equal(t, docutil.CodeState, docutil.CodeOf(appendErr))
	assert.Equal(t, "run_x", appendErr.RunID)
	assert.Contains(t, appendErr.Error(), `provenance record for run "run_x" already exists with DIFFERENT content`)
	assert.Contains(t, appendErr.Error(), "append-only archival refuses history rewrites (fail closed)")

	_, err := provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) { in.RunID = "" }))
	var buildErr *provenance.ProvenanceBuildError
	require.ErrorAs(t, err, &buildErr)
	assert.Equal(t, docutil.CodeValidation, buildErr.Code())
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(buildErr))

	err = provenance.AssertNoSecrets(*recordWithSecret(t, "reviewer@example.com"))
	var secretErr *provenance.ProvenanceSecretError
	require.ErrorAs(t, err, &secretErr)
	assert.Equal(t, docutil.CodeGuard, secretErr.Code())
	assert.Equal(t, docutil.CodeGuard, docutil.CodeOf(secretErr))
}
