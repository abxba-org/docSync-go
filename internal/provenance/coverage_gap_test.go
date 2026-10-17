package provenance_test

// Coverage-gap tests for internal/provenance: the JSON marshalling floors are
// reachable through non-finite detector confidence (json.Marshal rejects
// NaN/Inf), pinning the fail-closed marshal branches on the secret scan and
// the write path.

import (
	"math"
	"testing"

	"github.com/DUDE616/docSync-go/internal/provenance"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covNaNRecord(extra ...string) schemas.Provenance {
	r := schemas.Provenance{
		Schema:          schemas.ProvenanceSchemaID,
		RunID:           "01JZZ0000000000000000000R9",
		SourceCommit:    "0123456789abcdef0123456789abcdef01234567",
		SourceFiles:     []string{"src/x.ts"},
		Detector:        schemas.ProvenanceDetector{Model: "m", Confidence: math.NaN()},
		Writer:          schemas.ProvenanceWriter{Model: "w"},
		Verification:    schemas.ProvenanceVerification{Status: schemas.VerdictPass},
		GeneratedAt:     "2026-10-01T00:00:00.000Z",
		PolicyVersion:   1,
		ManifestVersion: "1",
	}
	for _, e := range extra {
		r.SourceFiles = append(r.SourceFiles, e)
	}
	return r
}

func TestCOV_PROVENANCE_NonFiniteConfidenceFailsClosed(t *testing.T) {
	record := covNaNRecord()

	// The marshal floor inside the secret scan refuses NaN confidence.
	_, err := provenance.ScanProvenanceRecord(record)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")

	require.Error(t, provenance.AssertNoSecrets(record))

	// The store refuses a record that cannot even be encoded (fail closed
	// before any archive byte).
	store, _ := makeStore(t)
	_, err = store.Write(record)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NaN")
}

func TestCOV_PROVENANCE_FiniteConfidenceEndToEndSecretScan(t *testing.T) {
	good := covNaNRecord()
	good.Detector.Confidence = 0.5
	scan, err := provenance.ScanProvenanceRecord(good)
	require.NoError(t, err)
	assert.True(t, scan.Ok)
	assert.Empty(t, scan.Findings)
	require.NoError(t, provenance.AssertNoSecrets(good))
}
