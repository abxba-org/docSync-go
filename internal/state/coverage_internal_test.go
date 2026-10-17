package state

// Internal coverage for the tiny flat branches the store surface cannot reach:
// the atomic-write failure floors and the schema-error constructor used by the
// registry-mismatch refusals.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func TestCOV_INTERNAL_STATE_atomicWriteMkdirFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	err := atomicWrite(filepath.Join(blocker, "child.json"), "v1")
	require.Error(t, err)
}

func TestCOV_INTERNAL_STATE_atomicWriteRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sink")
	require.NoError(t, os.MkdirAll(target, 0o755))
	err := atomicWrite(target, "x")
	require.Error(t, err)
	// The temp file is cleaned up even when the rename fails.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), ".tmp-")
	}
}

func TestCOV_INTERNAL_STATE_newStateSchemaError(t *testing.T) {
	err := newStateSchemaError(`schema "x" is missing from the registry`, "registry miss")
	require.Error(t, err)
	assert.Equal(t, CodeStateSchema, docutil.CodeOf(err))
	assert.Equal(t, "registry miss", err.Summary)
}

func TestCOV_INTERNAL_STATE_cannotSerializeValuesFailsClosed(t *testing.T) {
	s, stateDir := newInternalStore(t)

	// parseRegistered marshal failure.
	schema := schemas.GetSchema("docsync.run-record")
	require.NotNil(t, schema)
	_, err := parseRegistered(schema, make(chan int), "state/runs/x.json")
	require.Error(t, err)

	// canonicalBytes marshal failure.
	_, err = canonicalBytes(make(chan int))
	require.Error(t, err)

	// jsonString fallback for a non-serializable value.
	assert.True(t, strings.HasPrefix(jsonString(make(chan int)), "0x"))

	// toFsPath rejects a non-state relPath (internal invariant).
	_, err = s.toFsPath("other/x.json")
	require.Error(t, err)
	_, err = s.toFsPath("manifest.yml")
	require.Error(t, err)

	// writeTyped surfaces the CAS read failure when the target parent is a
	// regular FILE (WriteRawForTest analogue).
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "docs"), []byte("x"), 0o644))
	_, err = s.writeTyped(
		"state/docs/blocked.json",
		"docsync.doc-state",
		schemas.DocState{
			Path: "docs/blk.md", State: schemas.DocStateSuspect,
			Since: "2026-10-01T00:00:00Z", PolicyVersion: 1,
		},
		nil,
	)
	require.Error(t, err)
}

// TestCOV_INTERNAL_STATE_UpsertRunRecordReadFailure: a runs/ parent that is a
// file surfaces the state read failure (fail closed, never "empty").
func TestCOV_INTERNAL_STATE_UpsertRunRecordReadFailure(t *testing.T) {
	s, stateDir := newInternalStore(t)
	record := schemas.RunRecordExample()
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "runs"), []byte("x"), 0o644))
	_, err := s.UpsertRunRecord(record.Key, record)
	require.Error(t, err)
}

// TestCOV_INTERNAL_STATE_WriteRawForTestSurfacesCastFailure: a target whose
// parent is a regular file fails the CAS read (fail closed).
func TestCOV_INTERNAL_STATE_WriteRawForTestSurfacesCastFailure(t *testing.T) {
	s, stateDir := newInternalStore(t)
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "runbucket"), []byte("x"), 0o644))
	_, err := s.WriteRawForTest("state/runbucket/x.json", `{"x":1}`, nil)
	require.Error(t, err)
}

func newInternalStore(t *testing.T) (*StateStore, string) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	store, err := NewStateStore(StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	return store, stateDir
}

func TestCOV_INTERNAL_STATE_readTypedAndWriteTypedFloors(t *testing.T) {
	s, _ := newInternalStore(t)

	// readTyped: registry miss surfaces (and requireSchema's guard).
	_, err := readTyped[any](s, "state/baselines/x.json", "no-such-schema")
	require.Error(t, err)
	assert.Equal(t, "EDOCSYNC_STATE_SCHEMA", docutil.CodeOf(err))

	// readTyped: valid schema but a non-state relPath fails the invariant.
	_, err = readTyped[any](s, "manifest.yml", "docsync.run-record")
	require.Error(t, err)

	// writeTyped: registry miss surfaces.
	_, err = s.writeTyped("state/runs/x.json", "no-such-schema", schemas.RunRecordExample(), nil)
	require.Error(t, err)

	// writeTyped: the boundary guard refuses a non-state path.
	_, err = s.writeTyped("manifest.yml", "docsync.run-record", schemas.RunRecordExample(), nil)
	require.Error(t, err)
	var gpe *GuardedPathError
	require.ErrorAs(t, err, &gpe)
}
