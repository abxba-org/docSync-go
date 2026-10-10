package schemas_test

// Ported from packages/schemas/test/provenance.test.ts (black-box, testify).
//
// Covers PROVENANCE_SCHEMA (P9-T01; ARCH §12.5): the validated example,
// the literal schema tag, the 40-hex source_commit, nonempty source_files,
// datetime checks (incl. the non-UTC offset rejection), the 0..1 detector
// confidence, the OD-2 reuse of the verifier verdict vocabulary, the
// positive policy_version, the filename-safe run_id, and strict unknown-key
// rejection (OD-R01).
//
// Helper names are provenance-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// provenanceWire renders PROVENANCE_EXAMPLE as generic JSON, applies the
// mutation (the TS suite does `{...PROVENANCE_EXAMPLE, <field>: <bad>}`)
// and re-serializes.
func provenanceWire(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(schemas.ProvenanceExample())
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	if mutate != nil {
		mutate(m)
	}
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

// provenanceIssueAt asserts the parse failed with an issue on the path
// segment (TS: issue.path.includes(<segment>)).
func provenanceIssueAt(t *testing.T, data []byte, segment string) {
	t.Helper()
	_, err := schemas.ParseProvenance(data)
	require.Error(t, err)
	var ve *schemas.ValidationError
	require.ErrorAs(t, err, &ve)
	for _, iss := range ve.Issues {
		for _, seg := range iss.Path {
			if seg == segment {
				return
			}
		}
	}
	require.Failf(t, "issue not found", "expected an issue on path segment %q, got %v", segment, ve.Issues)
}

func TestProvenanceSchema_ValidatesTheExample(t *testing.T) {
	parsed, err := schemas.ParseProvenance(provenanceWire(t, nil))
	require.NoError(t, err)
	assert.Equal(t, schemas.ProvenanceExample(), *parsed)
}

func TestProvenanceSchema_CarriesTheLiteralSchemaTag(t *testing.T) {
	require.Equal(t, "docsync.provenance/v1", schemas.ProvenanceSchemaID)
	_, err := schemas.ParseProvenance(provenanceWire(t, func(m map[string]any) {
		m["schema"] = "docsync.provenance/v2"
	}))
	require.Error(t, err)
}

func TestProvenanceSchema_RejectsANon40HexSourceCommit(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) { m["source_commit"] = "81af92e" })
	provenanceIssueAt(t, data, "source_commit")
}

func TestProvenanceSchema_RejectsAnEmptySourceFilesArray(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) {
		m["source_files"] = []any{}
	})
	provenanceIssueAt(t, data, "source_files")
}

func TestProvenanceSchema_RejectsABadGeneratedAtDatetime(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) { m["generated_at"] = "not-a-date" })
	provenanceIssueAt(t, data, "generated_at")
}

func TestProvenanceSchema_RejectsNonUTCDatetimesWithoutOffsetSupport(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) { m["generated_at"] = "2026-10-04T22:31:00+02:00" })
	_, err := schemas.ParseProvenance(data)
	require.Error(t, err)
}

func TestProvenanceSchema_RejectsConfidenceOutsideZeroToOne(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) {
		detector := m["detector"].(map[string]any)
		detector["confidence"] = 1.5
	})
	_, err := schemas.ParseProvenance(data)
	require.Error(t, err)
}

func TestProvenanceSchema_RejectsAVerificationStatusOutsideTheVerdictVocabulary(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) {
		verification := m["verification"].(map[string]any)
		verification["status"] = "mismatch"
	})
	provenanceIssueAt(t, data, "status")
}

func TestProvenanceSchema_RejectsANonPositivePolicyVersion(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) { m["policy_version"] = 0 })
	_, err := schemas.ParseProvenance(data)
	require.Error(t, err)
}

func TestProvenanceSchema_RejectsAPathyRunID(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) { m["run_id"] = "../escape" })
	_, err := schemas.ParseProvenance(data)
	require.Error(t, err)
}

func TestProvenanceSchema_RejectsUnknownKeys(t *testing.T) {
	data := provenanceWire(t, func(m map[string]any) { m["extra"] = true })
	_, err := schemas.ParseProvenance(data)
	require.Error(t, err)
	// zod issue code 'unrecognized_keys' ⇒ Go DisallowUnknownFields; the
	// rejection outcome is preserved (message text is the stdlib's).
	assert.Contains(t, err.Error(), "unknown field")
}
