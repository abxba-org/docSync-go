package provenance_test

// COVERAGE tests targeting every statement/branch not already exercised by the
// ported V-P9 suites. Black-box, deterministic, no filesystem beyond t.TempDir,
// no network, no clocks (now is a field).

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/provenance"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- record builder */

// TestCOV_PROVENANCE_RecordHappyPath exercises the fully-validated builder and
// the schema floor (assembled record round-trips through strict parse).
func TestCOV_PROVENANCE_RecordHappyPath(t *testing.T) {
	record, err := provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) {
		in.DetectorConfidence = 0
		in.PolicyVersion = 1
		in.ManifestVersion = "1"
	}))
	require.NoError(t, err)
	require.Empty(t, record.Validate())
	assert.Equal(t, schemas.ProvenanceSchemaID, record.Schema)
	assert.Equal(t, runID, record.RunID)
	assert.Equal(t, sha, record.SourceCommit)
	assert.Equal(t, []string{"src/payments/refund.ts", "src/payments/types.ts"}, record.SourceFiles)
	assert.Equal(t, detectorModel, record.Detector.Model)
	assert.InDelta(t, 0, record.Detector.Confidence, 1e-12)
	assert.Equal(t, writerModel, record.Writer.Model)
	assert.Equal(t, schemas.VerdictPass, record.Verification.Status)
	assert.Equal(t, 1, record.PolicyVersion)
	assert.Equal(t, "1", record.ManifestVersion)
	assert.Equal(t, now, record.GeneratedAt)
}

// TestCOV_PROVENANCE_BuilderSchemaFloorRejectsInvalidDate covers
// parseProvenanceRecord's final floor: a Now whose shape passes isoUTCRegex
// but is not a real calendar date fails the strict schema parse (fail closed).
func TestCOV_PROVENANCE_BuilderSchemaFloorRejectsInvalidDate(t *testing.T) {
	_, err := provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) {
		in.Now = "2026-13-45T99:99:99Z"
	}))
	require.Error(t, err)
	var buildErr *provenance.ProvenanceBuildError
	require.True(t, errors.As(err, &buildErr))
	assert.Equal(t, "<record>", buildErr.Field)
	assert.Contains(t, err.Error(), "assembled record failed the docsync.provenance/v1 schema (fail closed)")
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
}

// TestCOV_PROVENANCE_JsNumberAllVariants covers jsNumber's NaN / +/-Infinity /
// shortest-round-trip branches.
func TestCOV_PROVENANCE_JsNumberAllVariants(t *testing.T) {
	for _, confidence := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0.97} {
		_, err := provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) {
			in.DetectorConfidence = confidence
		}))
		if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
	}
}

// TestCOV_PROVENANCE_RecordSourceFilesCopiedAndEmptyRejected covers boundary:
// the builder copies the slice and refuses empty / empty-entry source files.
func TestCOV_PROVENANCE_RecordSourceFilesCopiedAndEmptyRejected(t *testing.T) {
	files := []string{"src/a.ts", "src/b.ts"}
	record, err := provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) {
		in.SourceFiles = files
	}))
	require.NoError(t, err)
	files[0] = "mutated"
	assert.Equal(t, "src/a.ts", record.SourceFiles[0])

	_, err = provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) {
		in.SourceFiles = []string{}
	}))
	require.Error(t, err)
	var buildErr *provenance.ProvenanceBuildError
	require.True(t, errors.As(err, &buildErr))
	assert.Equal(t, "source_files", buildErr.Field)
}

/* --------------------------------------------------------------- store */

// TestCOV_PROVENANCE_StoreNewWithCustomQueue covers the optional queue parameter.
func TestCOV_PROVENANCE_StoreNewWithCustomQueue(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), ".docsync", "state")
	store, err := provenance.NewProvenanceStore(provenance.ProvenanceStoreOptions{
		StateDir: stateRoot,
	}, state.CreateInProcessQueue())
	require.NoError(t, err)
	require.NotNil(t, store)
	record := buildRecord(t, nil)
	res, err := store.Write(*record)
	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.True(t, res.Created)
}

// TestCOV_PROVENANCE_StoreListEmptyWhenNothingWritten covers List returning
// nil, nil when the runs/ directory does not yet exist.
func TestCOV_PROVENANCE_StoreListEmptyWhenNothingWritten(t *testing.T) {
	store, _ := makeStore(t)
	ids, err := store.List()
	require.NoError(t, err)
	assert.Nil(t, ids)

	read, err := store.Read("whatever")
	require.NoError(t, err)
	assert.False(t, read.Exists)
}

// TestCOV_PROVENANCE_StoreWriteRejectsSchemaDriftAtWrite covers
// parseProvenanceForPath's parse-error branch: a record with a valid run id but
// invalid content is refused as schema drift before any byte moves.
func TestCOV_PROVENANCE_StoreWriteRejectsSchemaDriftAtWrite(t *testing.T) {
	store, _ := makeStore(t)
	bad := schemas.Provenance{
		Schema:          schemas.ProvenanceSchemaID,
		RunID:           "run_x",
		SourceCommit:    "not-hex",
		SourceFiles:     []string{"src/a.ts"},
		Detector:        schemas.ProvenanceDetector{Model: "m", Confidence: 0.5},
		Writer:          schemas.ProvenanceWriter{Model: "m"},
		Verification:    schemas.ProvenanceVerification{Status: schemas.VerdictPass},
		GeneratedAt:     now,
		PolicyVersion:   1,
		ManifestVersion: "1",
	}
	_, err := store.Write(bad)
	require.Error(t, err)
	var schemaErr *state.StateSchemaError
	require.True(t, errors.As(err, &schemaErr))
	assert.Equal(t, state.CodeStateSchema, docutil.CodeOf(err))

	// Nothing landed.
	ids, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// TestCOV_PROVENANCE_StoreReadAndListRoundTrip covers Read of a written record,
// List ordering, and a soft absent read after real writes exist.
func TestCOV_PROVENANCE_StoreReadAndListRoundTrip(t *testing.T) {
	store, _ := makeStore(t)
	first := buildRecord(t, func(in *provenance.BuildProvenanceInput) { in.RunID = "run_z" })
	second := buildRecord(t, func(in *provenance.BuildProvenanceInput) { in.RunID = "run_a" })
	_, err := store.Write(*first)
	require.NoError(t, err)
	_, err = store.Write(*second)
	require.NoError(t, err)

	ids, err := store.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"run_a", "run_z"}, ids)

	read, err := store.Read("run_z")
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.NotNil(t, read.Content)
	assert.Equal(t, "run_z", read.Content.RunID)
	assert.NotEmpty(t, read.Hash)
	assert.Equal(t, string(archivalBytes(t, store, "run_z")), read.Raw)

	read, err = store.Read("does-not-exist")
	require.NoError(t, err)
	assert.False(t, read.Exists)
	assert.Empty(t, read.Raw)
	assert.Empty(t, read.Hash)
	assert.Nil(t, read.Content)
}

// TestCOV_PROVENANCE_StoreConcurrentDistinctRunsAndReplay covers concurrent
// writes of distinct runs plus the no-op replay path under the queue.
func TestCOV_PROVENANCE_StoreConcurrentDistinctRunsAndReplay(t *testing.T) {
	store, _ := makeStore(t)
	record := buildRecord(t, nil)

	first, err := store.Write(*record)
	require.NoError(t, err)
	assert.True(t, first.Created)
	assert.False(t, first.Replay)

	// Replay of the identical record is a no-op.
	second, err := store.Write(*record)
	require.NoError(t, err)
	assert.True(t, second.OK)
	assert.False(t, second.Created)
	assert.True(t, second.Replay)
	assert.Equal(t, first.Hash, second.Hash)

	// Distinct runs land concurrently under the same queue.
	const writers = 6
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			runID := "run_c" + strconv.Itoa(index)
			_, err := store.Write(*buildRecord(t, func(in *provenance.BuildProvenanceInput) {
				in.RunID = runID
			}))
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	ids, err := store.List()
	require.NoError(t, err)
	assert.Len(t, ids, 7)
	assert.Contains(t, ids, record.RunID)
	for i := 0; i < 6; i++ {
		assert.Contains(t, ids, "run_c"+strconv.Itoa(i))
	}
}

// TestCOV_PROVENANCE_StoreErrorTypes exposes the coded surfaces of every
// provenance error type, including CodeOf and errors.As seams.
func TestCOV_PROVENANCE_StoreErrorTypes(t *testing.T) {
	appendErr := provenance.NewProvenanceAppendError("run_y", "detail")
	assert.Equal(t, docutil.CodeState, appendErr.Code())
	assert.Equal(t, docutil.CodeState, docutil.CodeOf(appendErr))
	assert.NotNil(t, appendErr.Unwrap())

	// BuildError via a refused build.
	_, buildErrRaw := provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) {
		in.RunID = ""
	}))
	var buildErr *provenance.ProvenanceBuildError
	require.True(t, errors.As(buildErrRaw, &buildErr))
	assert.Equal(t, docutil.CodeValidation, buildErr.Code())
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(buildErr))
	assert.NotNil(t, buildErr.Unwrap())

	// SecretError via a refused secret scan.
	secretErrRaw := provenance.AssertNoSecrets(*recordWithSecret(t, "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6qR"))
	var secretErr *provenance.ProvenanceSecretError
	require.True(t, errors.As(secretErrRaw, &secretErr))
	assert.Equal(t, docutil.CodeGuard, secretErr.Code())
	assert.Equal(t, docutil.CodeGuard, docutil.CodeOf(secretErr))
	assert.NotNil(t, secretErr.Unwrap())
	assert.NotEmpty(t, secretErr.Findings)
}

/* --------------------------------------------------------------- secret scan */

// TestCOV_PROVENANCE_SecretScanMultipleAndEncoded covers multiple secrets in
// one record, base64/hex long runs, PEM, and the OPENROUTER_API_KEY quoted form.
func TestCOV_PROVENANCE_SecretScanMultipleAndEncoded(t *testing.T) {
	// Two different secret classes in the same source file.
	dirty := recordWithSecret(t, "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6qR reviewer@example.com")
	scan, err := provenance.ScanProvenanceRecord(*dirty)
	require.NoError(t, err)
	require.False(t, scan.Ok)
	assert.Len(t, scan.Findings, 2)
	for _, f := range scan.Findings {
		assert.NotContains(t, f, "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6qR reviewer@example.com")
	}

	// A base64 long run (≥40 chars) that is not the source commit.
	scan, err = provenance.ScanProvenanceRecord(*recordWithSecret(t, strings.Repeat("Z", 56)))
	require.NoError(t, err)
	require.False(t, scan.Ok)
	assert.Contains(t, scan.Findings[0], "long opaque base64/hex run (56 chars)")

	// PEM block.
	scan, err = provenance.ScanProvenanceRecord(*recordWithSecret(t, "-----BEGIN CERTIFICATE-----"))
	require.NoError(t, err)
	require.False(t, scan.Ok)
	assert.Contains(t, scan.Findings[0], "key material (PEM block)")

	// Quoted OPENROUTER_API_KEY assignment.
	scan, err = provenance.ScanProvenanceRecord(*recordWithSecret(t, `OPENROUTER_API_KEY="sk-abc1234567890"`))
	require.NoError(t, err)
	require.False(t, scan.Ok)
	assert.Contains(t, scan.Findings[0], "OPENROUTER_API_KEY value")

	// Bearer credential.
	scan, err = provenance.ScanProvenanceRecord(*recordWithSecret(t, "Bearer AbCdEfGhIjKlMnOp"))
	require.NoError(t, err)
	require.False(t, scan.Ok)
	assert.Contains(t, scan.Findings[0], "Bearer credential")
}

// TestCOV_PROVENANCE_AssertNoSecretsGate covers AssertNoSecrets clean/dirty
// and the typed secret error surface.
func TestCOV_PROVENANCE_AssertNoSecretsGate(t *testing.T) {
	require.NoError(t, provenance.AssertNoSecrets(*buildRecord(t, nil)))

	dirty := recordWithSecret(t, "sk-"+strings.Repeat("k", 20))
	err := provenance.AssertNoSecrets(*dirty)
	require.Error(t, err)
	var secretErr *provenance.ProvenanceSecretError
	require.ErrorAs(t, err, &secretErr)
	assert.NotEmpty(t, secretErr.Findings)
	assert.Equal(t, docutil.CodeGuard, secretErr.Code())
	assert.Contains(t, err.Error(), "a provenance record is not a secret container (ARCH §12.5)")

	// Findings never leak the secret value.
	scan, err := provenance.ScanProvenanceRecord(*dirty)
	require.NoError(t, err)
	for _, f := range scan.Findings {
		assert.NotContains(t, f, "sk-"+strings.Repeat("k", 20))
	}
}

// TestCOV_PROVENANCE_StoreSecretGateRefusesDirtyRecordPreWrite covers the
// store refusing a secret-carrying record before any byte touches disk.
func TestCOV_PROVENANCE_StoreSecretGateRefusesDirtyRecordPreWrite(t *testing.T) {
	store, stateRoot := makeStore(t)
	dirty := recordWithSecret(t, "ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6qR")
	_, err := store.Write(*dirty)
	require.Error(t, err)
	var secretErr *provenance.ProvenanceSecretError
	require.True(t, errors.As(err, &secretErr))
	assert.Equal(t, docutil.CodeGuard, docutil.CodeOf(err))

	ids, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
	_, statErr := os.Stat(filepath.Join(stateRoot, "runs"))
	require.True(t, os.IsNotExist(statErr))
}

// TestCOV_PROVENANCE_StoreCASConflictViaConcurrentSameRun covers the CAS
// conflict result shape by racing the identical run; exactly one creates and
// the rest replay.
func TestCOV_PROVENANCE_StoreCASConflictViaConcurrentSameRun(t *testing.T) {
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
	replays := 0
	for i := 0; i < writers; i++ {
		require.NoError(t, errs[i])
		require.True(t, results[i].OK)
		if results[i].Created {
			created++
		}
		if results[i].Replay {
			replays++
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, writers-1, replays)
}

// _ keeps json.Marshal imported (used indirectly via helpers).
var _ = json.Marshal

// TestCOV_PROVENANCE_EmptySourceCommitAndStatus covers the requireNonEmpty
// refusal branches for source_commit and verification.status (no existing
// case exercises the empty string for those fields).
func TestCOV_PROVENANCE_EmptySourceCommitAndStatus(t *testing.T) {
	_, err := provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) {
		in.SourceCommit = ""
	}))
	require.Error(t, err)
	var buildErr *provenance.ProvenanceBuildError
	require.True(t, errors.As(err, &buildErr))
	assert.Equal(t, "source_commit", buildErr.Field)

	_, err = provenance.BuildProvenanceRecord(validInput(func(in *provenance.BuildProvenanceInput) {
		in.VerificationStatus = ""
	}))
	require.Error(t, err)
	require.True(t, errors.As(err, &buildErr))
	assert.Equal(t, "verification.status", buildErr.Field)
}

// TestCOV_PROVENANCE_StoreWriteExistingSchemaDrift covers parseProvenanceForPath's
// schema-error branch during Write: an existing file with valid JSON but schema
// drift (unknown keys) is refused when rewriting with different bytes.
func TestCOV_PROVENANCE_StoreWriteExistingSchemaDrift(t *testing.T) {
	store, _ := makeStore(t)
	record := buildRecord(t, nil)
	_, err := store.Write(*record)
	require.NoError(t, err)

	// Plant a file with valid JSON but unknown keys at the same run path.
	path, err := store.RecordFSPath(record.RunID)
	require.NoError(t, err)
	drift := append(append([]byte{}, archivalBytes(t, store, record.RunID)[:len(archivalBytes(t, store, record.RunID))-1]...), []byte(`,"bogus":1}`)...)
	require.NoError(t, os.WriteFile(path, drift, 0o644))

	// Different valid record for the same run: not a replay, existing drifts → schema error.
	other := buildRecord(t, func(in *provenance.BuildProvenanceInput) {
		in.DetectorConfidence = 0.5
	})
	_, err = store.Write(*other)
	require.Error(t, err)
	var schemaErr *state.StateSchemaError
	require.True(t, errors.As(err, &schemaErr))
	assert.Equal(t, state.CodeStateSchema, docutil.CodeOf(err))
}

// TestCOV_PROVENANCE_StoreReadStateFileError covers Read's ReadStateFile
// non-ENOENT error branch by placing a directory where a file is expected.
func TestCOV_PROVENANCE_StoreReadStateFileError(t *testing.T) {
	store, _ := makeStore(t)
	record := buildRecord(t, nil)
	_, err := store.Write(*record)
	require.NoError(t, err)
	path, err := store.RecordFSPath(record.RunID)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.MkdirAll(path, 0o755))

	_, err = store.Read(record.RunID)
	require.Error(t, err)
}

// TestCOV_PROVENANCE_StoreListNonExistentDirError covers List's non-ENOENT
// error branch: runs/ exists but is a file (not a directory).
func TestCOV_PROVENANCE_StoreListNonExistentDirError(t *testing.T) {
	store, stateRoot := makeStore(t)
	require.NoError(t, os.MkdirAll(stateRoot, 0o755))
	runsDir := filepath.Join(stateRoot, runsSubdirName())
	require.NoError(t, os.WriteFile(runsDir, []byte("not a dir"), 0o644))
	_, err := store.List()
	require.Error(t, err)
}

func runsSubdirName() string { return "runs" }
