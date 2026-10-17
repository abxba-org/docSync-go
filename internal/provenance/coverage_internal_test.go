package provenance

// Internal coverage for the builder's fail-closed marshal floor: a record
// that cannot be serialized (non-finite detector confidence, which the wire
// forbids) is refused before any text is computed.

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

func covNaNRecordInternal() *schemas.Provenance {
	return &schemas.Provenance{
		Schema:       schemas.ProvenanceSchemaID,
		RunID:        "01JZZ0000000000000000000R9",
		SourceCommit: "0123456789abcdef0123456789abcdef01234567",
		SourceFiles:  []string{"src/x.ts"},
		Detector:     schemas.ProvenanceDetector{Model: "m", Confidence: math.NaN()},
		Writer:       schemas.ProvenanceWriter{Model: "w"},
		Verification: schemas.ProvenanceVerification{Status: schemas.VerdictPass},
		GeneratedAt:  "2026-10-01T00:00:00.000Z",
	}
}

func TestCOV_INTERNAL_PROVENANCE_marshalFloors(t *testing.T) {
	_, err := parseProvenanceRecord(covNaNRecordInternal())
	require.Error(t, err)

	_, err = canonicalRecordBytes(covNaNRecordInternal())
	require.Error(t, err)

	_, err = serializeForScan(*covNaNRecordInternal())
	require.Error(t, err)
}

func TestCOV_INTERNAL_PROVENANCE_toFsPathInvariant(t *testing.T) {
	store, stateRoot := makeInternalStore(t)
	_, err := store.toFsPath("manifest.yml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state/ relPath")
	_ = stateRoot
}

func makeInternalStore(t *testing.T) (*ProvenanceStore, string) {
	t.Helper()
	stateRoot := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(stateRoot, 0o755))
	store, err := NewProvenanceStore(ProvenanceStoreOptions{StateDir: stateRoot})
	require.NoError(t, err)
	return store, stateRoot
}
