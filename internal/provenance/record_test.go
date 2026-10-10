package provenance_test

// Black-box port of packages/core/test/provenance.test.ts, record half
// (V-P9-1: provenance record validates; a missing/invalid field refuses the
// write). Pure builder surface: no filesystem, no clocks — `now` is a field.
//
// The TS suite pulled its example from @docsync/schemas; the Go registry is the
// same single source of truth, so the registered example is read from it, never
// redefined here.

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/provenance"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------ golden data */

const (
	// SHA mirrors the TS SHA constant (a full 40-hex commit sha).
	sha = "3f9c2a7b1d4e5f60718293a4b5c6d7e8f9012a3b"
	// NOW mirrors the TS NOW constant (an injected ISO-8601 UTC instant).
	now = "2026-10-07T18:00:00Z"
	// runID is the builder's run id in the TS suite.
	runID = "run_01J9ZK6X8A"
	// detectorModel / writerModel are the TS suite's routed model ids.
	detectorModel = "z-ai/glm-4.7"
	writerModel   = "anthropic/claude-sonnet-4.5"
)

// validInput mirrors the TS validInput helper: the canonical builder input plus
// overrides.
func validInput(overrides func(*provenance.BuildProvenanceInput)) provenance.BuildProvenanceInput {
	input := provenance.BuildProvenanceInput{
		RunID:              runID,
		SourceCommit:       sha,
		SourceFiles:        []string{"src/payments/refund.ts", "src/payments/types.ts"},
		RoutedModels:       provenance.RoutedModels{DetectorModel: detectorModel, WriterModel: writerModel},
		DetectorConfidence: 0.97,
		VerificationStatus: string(schemas.VerdictPass),
		PolicyVersion:      17,
		ManifestVersion:    "1",
		Now:                now,
	}
	if overrides != nil {
		overrides(&input)
	}
	return input
}

// registeredProvenanceExample is the registry's docsync.provenance example
// (PROVENANCE_EXAMPLE), read — never redefined — from the schema registry.
func registeredProvenanceExample() schemas.Provenance {
	entry := schemas.GetSchema("docsync.provenance")
	if entry == nil {
		panic("schemas: registry entry \"docsync.provenance\" is missing")
	}
	example, ok := entry.Example.(schemas.Provenance)
	if !ok {
		panic("schemas: registry example \"docsync.provenance\" has the wrong type")
	}
	return example
}

// buildRecord builds a record from validInput plus overrides, failing the test
// on refusal (the helper for tests that need a VALID record).
func buildRecord(t *testing.T, overrides func(*provenance.BuildProvenanceInput)) *schemas.Provenance {
	t.Helper()
	record, err := provenance.BuildProvenanceRecord(validInput(overrides))
	require.NoError(t, err)
	return record
}

/* ----------------------------------------------------------------- tests */

func TestV_P9_1_BuilderOutputValidates(t *testing.T) {
	record, err := provenance.BuildProvenanceRecord(validInput(nil))
	require.NoError(t, err)

	// The builder output validates against docsync.provenance/v1.
	require.Empty(t, record.Validate())
	// The record carries the registered schema tag.
	assert.Equal(t, schemas.ProvenanceSchemaID, record.Schema)

	// Field-for-field spot checks against the input (no silent defaults).
	assert.Equal(t, runID, record.RunID)
	assert.Equal(t, sha, record.SourceCommit)
	assert.Equal(t, []string{"src/payments/refund.ts", "src/payments/types.ts"}, record.SourceFiles)
	assert.Equal(t, detectorModel, record.Detector.Model)
	assert.Equal(t, writerModel, record.Writer.Model)
	assert.InDelta(t, 0.97, record.Detector.Confidence, 1e-9)
	assert.Equal(t, schemas.VerdictPass, record.Verification.Status)
	assert.Equal(t, 17, record.PolicyVersion)
	assert.Equal(t, "1", record.ManifestVersion)
	assert.Equal(t, now, record.GeneratedAt)
}

func TestV_P9_1_RegisteredExampleValidates(t *testing.T) {
	example := registeredProvenanceExample()
	require.Empty(t, example.Validate())
	// The registered example is also strictly decodable (unknown keys rejected).
	parsed, err := schemas.ParseProvenance(mustMarshal(t, example))
	require.NoError(t, err)
	require.NotNil(t, parsed)
}

// mustMarshal marshals a record the way the archival path serializes it.
func mustMarshal(t *testing.T, record schemas.Provenance) []byte {
	t.Helper()
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	return raw
}

func TestV_P9_1_BuilderRefusesEachInvalidField(t *testing.T) {
	cases := []struct {
		name      string
		overrides func(*provenance.BuildProvenanceInput)
		field     string
		message   string
	}{
		{
			name:      "empty run id",
			overrides: func(in *provenance.BuildProvenanceInput) { in.RunID = "" },
			field:     "run_id",
			message:   "run_id must be a non-empty string",
		},
		{
			name:      "unsafe run id",
			overrides: func(in *provenance.BuildProvenanceInput) { in.RunID = "../escape" },
			field:     "run_id",
			message:   `run_id "../escape" is not filename-safe`,
		},
		{
			name:      "short source commit",
			overrides: func(in *provenance.BuildProvenanceInput) { in.SourceCommit = "81af92e" },
			field:     "source_commit",
			message:   `source_commit "81af92e" is not a 40-hex git sha`,
		},
		{
			name:      "empty source files",
			overrides: func(in *provenance.BuildProvenanceInput) { in.SourceFiles = []string{} },
			field:     "source_files",
			message:   "source_files must be a non-empty array",
		},
		{
			name: "empty source file entry",
			overrides: func(in *provenance.BuildProvenanceInput) {
				in.SourceFiles = []string{"src/a.ts", ""}
			},
			field:   "source_files",
			message: "every source_files entry must be a non-empty string",
		},
		{
			name: "empty detector model",
			overrides: func(in *provenance.BuildProvenanceInput) {
				in.RoutedModels = provenance.RoutedModels{DetectorModel: "", WriterModel: "m"}
			},
			field:   "detector.model",
			message: "detector.model must be a non-empty string",
		},
		{
			name: "empty writer model",
			overrides: func(in *provenance.BuildProvenanceInput) {
				in.RoutedModels = provenance.RoutedModels{DetectorModel: "m", WriterModel: ""}
			},
			field:   "writer.model",
			message: "writer.model must be a non-empty string",
		},
		{
			name:      "confidence above one",
			overrides: func(in *provenance.BuildProvenanceInput) { in.DetectorConfidence = 1.5 },
			field:     "detector.confidence",
			message:   "detector.confidence must be a finite number in [0, 1]",
		},
		{
			name:      "NaN confidence",
			overrides: func(in *provenance.BuildProvenanceInput) { in.DetectorConfidence = math.NaN() },
			field:     "detector.confidence",
			message:   "detector.confidence must be a finite number in [0, 1]",
		},
		{
			name:      "negative confidence",
			overrides: func(in *provenance.BuildProvenanceInput) { in.DetectorConfidence = -0.01 },
			field:     "detector.confidence",
			message:   "detector.confidence must be a finite number in [0, 1]",
		},
		{
			name:      "infinite confidence",
			overrides: func(in *provenance.BuildProvenanceInput) { in.DetectorConfidence = math.Inf(1) },
			field:     "detector.confidence",
			message:   "detector.confidence must be a finite number in [0, 1]",
		},
		{
			name:      "unknown verification status",
			overrides: func(in *provenance.BuildProvenanceInput) { in.VerificationStatus = "mismatch" },
			field:     "verification.status",
			message:   `verification.status "mismatch" is outside the verifier verdict vocabulary`,
		},
		{
			name:      "zero policy version",
			overrides: func(in *provenance.BuildProvenanceInput) { in.PolicyVersion = 0 },
			field:     "policy_version",
			message:   "policy_version must be a positive integer (got 0)",
		},
		{
			name:      "negative policy version",
			overrides: func(in *provenance.BuildProvenanceInput) { in.PolicyVersion = -3 },
			field:     "policy_version",
			message:   "policy_version must be a positive integer (got -3)",
		},
		{
			name:      "empty manifest version",
			overrides: func(in *provenance.BuildProvenanceInput) { in.ManifestVersion = "" },
			field:     "manifest_version",
			message:   "manifest_version must be a non-empty string",
		},
		{
			name:      "empty generated_at",
			overrides: func(in *provenance.BuildProvenanceInput) { in.Now = "" },
			field:     "generated_at",
			message:   "generated_at must be a non-empty string",
		},
		{
			name:      "non-UTC generated_at",
			overrides: func(in *provenance.BuildProvenanceInput) { in.Now = "2026-10-07T18:00:00+02:00" },
			field:     "generated_at",
			message:   `generated_at "2026-10-07T18:00:00+02:00" is not an ISO-8601 UTC instant`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record, err := provenance.BuildProvenanceRecord(validInput(tc.overrides))
			require.Error(t, err)
			require.Nil(t, record)

			// Typed refusal: the Go ProvenanceBuildError type IS the TS class
			// identity (errors.As works through the Unwrap chain).
			var buildErr *provenance.ProvenanceBuildError
			require.True(t, errors.As(err, &buildErr), "want *provenance.ProvenanceBuildError, got %T", err)
			assert.Equal(t, tc.field, buildErr.Field)
			assert.Contains(t, err.Error(), tc.message)
			// The coded error carries a registered docutil code.
			assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
		})
	}
}

func TestV_P9_1_BuilderAcceptsCanonicalVerdictVocabulary(t *testing.T) {
	for _, verdict := range []schemas.VerificationVerdict{
		schemas.VerdictPass, schemas.VerdictReview, schemas.VerdictReject,
	} {
		record, err := provenance.BuildProvenanceRecord(
			validInput(func(in *provenance.BuildProvenanceInput) { in.VerificationStatus = string(verdict) }))
		require.NoError(t, err)
		assert.Equal(t, verdict, record.Verification.Status)
	}
}

func TestV_P9_1_BuilderBoundaryValuesAreAccepted(t *testing.T) {
	// Confidence 0 and 1 are both inside [0, 1]; policy version 1 is the floor.
	for _, confidence := range []float64{0, 0.5, 1} {
		record, err := provenance.BuildProvenanceRecord(
			validInput(func(in *provenance.BuildProvenanceInput) {
				in.DetectorConfidence = confidence
				in.PolicyVersion = 1
			}))
		require.NoError(t, err)
		assert.InDelta(t, confidence, record.Detector.Confidence, 1e-12)
		assert.Equal(t, 1, record.PolicyVersion)
	}
}

func TestV_P9_1_BuilderCopiesSourceFiles(t *testing.T) {
	// The builder must not alias the caller's slice (TS `[...sourceFiles]`).
	files := []string{"src/a.ts"}
	record, err := provenance.BuildProvenanceRecord(
		validInput(func(in *provenance.BuildProvenanceInput) { in.SourceFiles = files }))
	require.NoError(t, err)
	files[0] = "src/mutated.ts"
	assert.Equal(t, "src/a.ts", record.SourceFiles[0])
}
