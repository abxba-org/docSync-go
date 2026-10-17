package audit

import (
	"errors"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// COVERAGE tests for unexported helpers that the black-box suites cannot
// reach (fail-closed message renderers, datetime codec branches, cursor
// resolvers). Deterministic, no IO.

func iptr(v int) *int { return &v }

func TestCOV_AUDIT_InternalMessageHelpers(t *testing.T) {
	// describeError(nil) renders the empty string (TS String(undefined)).
	require.Equal(t, "", describeError(nil))

	// registryMessage over a non-zod error falls back to err.Error().
	cause := errors.New("boom")
	require.Equal(t, "boom", registryMessage(cause))

	// registryMessage over a nil error is the "schema drift" fallback.
	require.Equal(t, "schema drift", registryMessage(nil))

	// firstIssueMessage over an empty issue set is the "schema drift"
	// fallback (the TS `issues[0]?.message ?? 'schema drift'`).
	require.Equal(t, "schema drift", firstIssueMessage(nil))
	require.Equal(t, "schema drift", firstIssueMessage([]schemas.Issue{}))

	// truncateForMessage slices the rune prefix exactly (TS `slice(0, 64)`).
	require.Equal(t, strings.Repeat("x", 64), truncateForMessage(strings.Repeat("x", 100), 64))
	require.Equal(t, "short", truncateForMessage("short", 64))

	// resolveConcurrency clamps a sub-1 cap to 1.
	require.Equal(t, 1, resolveConcurrency(iptr(0)))
	require.Equal(t, 1, resolveConcurrency(iptr(-3)))
}

func TestCOV_AUDIT_InternalDatetimeFracBranches(t *testing.T) {
	// A fractional-second datetime whose non-fraction body is the wrong
	// length is refused (the `len(body) != len(layout)` guard).
	require.False(t, isRFC3339UTCDatetime("2026-1-09T00:00:00.5Z"))

	// A datetime with the right body length but an out-of-range date fails
	// time.Parse and is refused (the `err != nil` guard). Go refuses day 32.
	require.False(t, isRFC3339UTCDatetime("2026-10-32T00:00:00Z"))

	// Sanity: valid forms still pass.
	require.True(t, isRFC3339UTCDatetime("2026-10-09T00:00:00Z"))
	require.True(t, isRFC3339UTCDatetime("2026-10-09T00:00:00.123Z"))
}

func TestCOV_AUDIT_InternalCanonicalJSONFailure(t *testing.T) {
	// A non-marshalable value (a channel) fails json.Marshal — the
	// fail-closed error path of canonicalJSONBytes.
	_, err := canonicalJSONBytes(make(chan int))
	require.Error(t, err)
}
