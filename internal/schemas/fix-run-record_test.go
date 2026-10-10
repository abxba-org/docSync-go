package schemas_test

// There is NO fix-run-record.test.ts in the TS suite (verified: the schemas
// test dir has no fix-run-record test — the record is validated only via its
// zod schema and the Phase 7 fix-flow code). Following the run-record.go
// precedent (run-record_test.go), this file pins the port with golden-data
// and validation tests for the `docsync.fix-run-record/v1` wire format: the
// example round-trips byte-stable, unknown keys are rejected (z.strictObject
// / OD-R01), the closed skip/refuse vocabularies are pinned, and the exact
// zod validation messages/paths hold.
//
// Helper names are fixRunRecord-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixRunRecordWire(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(schemas.FixRunRecordExample())
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

func TestFixRunRecord_ExampleParsesAndRoundTripsByteStable(t *testing.T) {
	// Marshal the struct directly (not via fixRunRecordWire's map) so key order
	// stays the TS declaration order the wire format pins.
	first, err := json.Marshal(schemas.FixRunRecordExample())
	require.NoError(t, err)
	parsed, err := schemas.ParseFixRunRecord(first)
	require.NoError(t, err)
	assert.Equal(t, schemas.FixRunRecordExample(), *parsed)

	second, err := json.Marshal(parsed)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
}

func TestFixRunRecord_RejectsUnknownKeys(t *testing.T) {
	_, err := schemas.ParseFixRunRecord(fixRunRecordWire(t, func(m map[string]any) { m["extra"] = true }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")

	_, err = schemas.ParseFixRunRecord(fixRunRecordWire(t, func(m map[string]any) {
		m["applied"].([]any)[0].(map[string]any)["extra"] = true
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")

	_, err = schemas.ParseFixRunRecord(fixRunRecordWire(t, func(m map[string]any) {
		m["skipped"].([]any)[0].(map[string]any)["extra"] = true
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")

	_, err = schemas.ParseFixRunRecord(fixRunRecordWire(t, func(m map[string]any) {
		m["refused"].([]any)[0].(map[string]any)["extra"] = true
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestFixRunRecord_RejectsInvalidShapes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		needle string
	}{
		{
			"wrong schema tag",
			func(m map[string]any) { m["schema"] = "docsync.fix-run-record/v2" },
			"Invalid literal value",
		},
		{
			"missing schema tag",
			func(m map[string]any) { m["schema"] = "" },
			"Required",
		},
		{
			"run_id not a ULID",
			func(m map[string]any) { m["run_id"] = "run_01J9ZK7Q2M8P4X6T2W5Y3C0A9E" },
			"run_id must be a ULID (26-char Crockford base32)",
		},
		{
			"run_id empty",
			func(m map[string]any) { m["run_id"] = "" },
			"run_id must be a ULID (26-char Crockford base32)",
		},
		{
			"bad at",
			func(m map[string]any) { m["at"] = "2026-10-07 18:00:00" },
			"Invalid datetime",
		},
		{
			"at with offset is rejected (Z-suffix only)",
			func(m map[string]any) { m["at"] = "2026-10-07T18:00:00+02:00" },
			"Invalid datetime",
		},
		{
			"empty repo",
			func(m map[string]any) { m["repo"] = "" },
			"String must contain at least 1 character(s)",
		},
		{
			"empty base",
			func(m map[string]any) { m["base"] = "" },
			"String must contain at least 1 character(s)",
		},
		{
			"empty head",
			func(m map[string]any) { m["head"] = "" },
			"String must contain at least 1 character(s)",
		},
		{
			"non-positive policy_version",
			func(m map[string]any) { m["policy_version"] = 0 },
			"Number must be greater than 0",
		},
		{
			"applied entry with empty doc_path",
			func(m map[string]any) {
				m["applied"].([]any)[0].(map[string]any)["doc_path"] = ""
			},
			"String must contain at least 1 character(s)",
		},
		{
			"applied entry with empty patch_ref",
			func(m map[string]any) {
				m["applied"].([]any)[0].(map[string]any)["patch_ref"] = ""
			},
			"String must contain at least 1 character(s)",
		},
		{
			"applied entry with non-boolean three_way_used",
			func(m map[string]any) {
				m["applied"].([]any)[0].(map[string]any)["three_way_used"] = "yes"
			},
			"cannot unmarshal",
		},
		{
			"skipped entry with unknown reason",
			func(m map[string]any) {
				m["skipped"].([]any)[0].(map[string]any)["reason"] = "mystery"
			},
			"Invalid enum value",
		},
		{
			"skipped entry with empty detail",
			func(m map[string]any) {
				m["skipped"].([]any)[0].(map[string]any)["detail"] = ""
			},
			"String must contain at least 1 character(s)",
		},
		{
			"refused entry with unknown reason",
			func(m map[string]any) {
				m["refused"].([]any)[0].(map[string]any)["reason"] = "mystery"
			},
			"Invalid enum value",
		},
	}
	require.NotEmpty(t, schemas.FixRunRecordExample().Applied)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schemas.ParseFixRunRecord(fixRunRecordWire(t, tc.mutate))
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tc.needle), "error %q should contain %q", err.Error(), tc.needle)
		})
	}
}

func TestFixRunRecord_ClosedVocabulariesArePinned(t *testing.T) {
	require.Equal(t, []schemas.FixSkipReason{
		"unverified",
		"dirty",
		"locked-or-protected",
		"out-of-scope",
		"not-a-candidate",
		"same-run-skip",
	}, schemas.FixSkipReasonValues)
	require.Equal(t, []schemas.FixRefuseReason{
		"stale-patch",
		"unanchored",
		"abstain",
	}, schemas.FixRefuseReasonValues)

	assert.True(t, schemas.IsValidFixSkipReason("same-run-skip"))
	assert.False(t, schemas.IsValidFixSkipReason("unknown"))
	assert.True(t, schemas.IsValidFixRefuseReason("unanchored"))
	assert.False(t, schemas.IsValidFixRefuseReason("stale"))
}

func TestFixRunRecord_EmptyLanesAreValid(t *testing.T) {
	// An honest fix run may apply, skip and refuse nothing (z.array has no min).
	wire := fixRunRecordWire(t, func(m map[string]any) {
		m["applied"] = []any{}
		m["skipped"] = []any{}
		m["refused"] = []any{}
	})
	parsed, err := schemas.ParseFixRunRecord(wire)
	require.NoError(t, err)
	assert.Empty(t, parsed.Applied)
	assert.Empty(t, parsed.Skipped)
	assert.Empty(t, parsed.Refused)
}
