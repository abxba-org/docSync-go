package schemas_test

// There is NO run-record.test.ts in the TS suite to port (verified: the
// schemas test dir has provenance.test.ts and run-outcome.test.ts but no
// run-record.test.ts — run records are covered indirectly via the
// run-outcome reuse tests). This file adds a golden-data test for the
// `docsync.run/v1` wire format so the port stays pinned: the example
// round-trips byte-stable, unknown keys are rejected (z.strictObject /
// OD-R01) and the validation messages/paths hold.
//
// Helper names are runRecord-prefixed: sibling black-box test files in
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

func runRecordWire(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(schemas.RunRecordExample())
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

func TestRunRecord_ExampleParsesAndRoundTripsByteStable(t *testing.T) {
	// Marshal the struct directly (not via runRecordWire's map) so key order
	// stays the TS declaration order the frozen wire format pins.
	first, err := json.Marshal(schemas.RunRecordExample())
	require.NoError(t, err)
	parsed, err := schemas.ParseRunRecord(first)
	require.NoError(t, err)
	assert.Equal(t, schemas.RunRecordExample(), *parsed)

	second, err := json.Marshal(parsed)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
}

func TestRunRecord_RejectsUnknownKeys(t *testing.T) {
	_, err := schemas.ParseRunRecord(runRecordWire(t, func(m map[string]any) { m["extra"] = true }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestRunRecord_RejectsInvalidShapes(t *testing.T) {
	cases := []struct {
		name   string
		wire   string
		needle string
	}{
		{"wrong schema tag", `{"schema":"docsync.run/v2","run_id":"r","key":{"repo":"a","base_sha":"b","head_sha":"c","manifest_version":1,"policy_version":1},"started_at":"2026-01-01T00:00:00Z","state_churn":{"added":0,"updated":0,"unchanged":0},"stages":[]}`, "Invalid literal value"},
		{"empty run_id", `{"schema":"docsync.run/v1","run_id":"","key":{"repo":"a","base_sha":"b","head_sha":"c","manifest_version":1,"policy_version":1},"started_at":"2026-01-01T00:00:00Z","state_churn":{"added":0,"updated":0,"unchanged":0},"stages":[]}`, "String must contain at least 1 character(s)"},
		{"bad started_at", `{"schema":"docsync.run/v1","run_id":"r","key":{"repo":"a","base_sha":"b","head_sha":"c","manifest_version":1,"policy_version":1},"started_at":"nope","state_churn":{"added":0,"updated":0,"unchanged":0},"stages":[]}`, "Invalid datetime"},
		{"negative churn counter", `{"schema":"docsync.run/v1","run_id":"r","key":{"repo":"a","base_sha":"b","head_sha":"c","manifest_version":1,"policy_version":1},"started_at":"2026-01-01T00:00:00Z","state_churn":{"added":-1,"updated":0,"unchanged":0},"stages":[]}`, "Number must be greater than or equal to 0"},
		{"non-positive manifest_version", `{"schema":"docsync.run/v1","run_id":"r","key":{"repo":"a","base_sha":"b","head_sha":"c","manifest_version":0,"policy_version":1},"started_at":"2026-01-01T00:00:00Z","state_churn":{"added":0,"updated":0,"unchanged":0},"stages":[]}`, "Number must be greater than 0"},
		{"bad stage status", `{"schema":"docsync.run/v1","run_id":"r","key":{"repo":"a","base_sha":"b","head_sha":"c","manifest_version":1,"policy_version":1},"started_at":"2026-01-01T00:00:00Z","state_churn":{"added":0,"updated":0,"unchanged":0},"stages":[{"stage":"writer","status":"weird"}]}`, "Invalid enum value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schemas.ParseRunRecord([]byte(tc.wire))
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tc.needle), "error %q should contain %q", err.Error(), tc.needle)
		})
	}
}
