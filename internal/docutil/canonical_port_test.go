package docutil_test

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/state.test.ts — describe('P0-T12: canonicalJson')
// plus the Go-only generic-form paths (json.Number, json.RawMessage, writeNumber).
func TestP0T12_CanonicalJsonSortsKeysRecursivelyArraysKeepOrder(t *testing.T) {
	out, err := docutil.CanonicalJSON(map[string]any{"b": 1, "a": 2})
	require.NoError(t, err)
	assert.Equal(t, `{"a":2,"b":1}`, out)

	// Key order does not matter.
	sortedA, err := docutil.CanonicalJSON(map[string]any{"b": 1, "a": 2})
	require.NoError(t, err)
	sortedB, err := docutil.CanonicalJSON(map[string]any{"a": 2, "b": 1})
	require.NoError(t, err)
	assert.Equal(t, sortedA, sortedB)

	// Nested objects sort; nested arrays keep their order.
	nested, err := docutil.CanonicalJSON(map[string]any{
		"z": map[string]any{"b": 1, "a": []any{2, 1}},
	})
	require.NoError(t, err)
	assert.Equal(t, `{"z":{"a":[2,1],"b":1}}`, nested)
}

func TestP0T12_CanonicalJsonThrowsOnNonJSONValues(t *testing.T) {
	// The TS suite rejects undefined/function/bigint/symbol/Date; the Go port
	// fails closed on every non-generic-form value (func, channel, struct…).
	for _, bad := range []any{
		func() {},
		make(chan int),
		struct{ X int }{X: 1},
		map[string]string{"a": "b"},
	} {
		_, err := docutil.CanonicalJSON(bad)
		require.Error(t, err, "%T", bad)
		assert.Equal(t, docutil.CodeState, docutil.CodeOf(err), "%T", bad)
	}
	// Nested unsupported values fail closed too.
	_, err := docutil.CanonicalJSON(map[string]any{"a": []any{func() {}}})
	require.Error(t, err)
}

func TestCanonicalJSONScalars(t *testing.T) {
	out, err := docutil.CanonicalJSON(nil)
	require.NoError(t, err)
	assert.Equal(t, "null", out)

	out, err = docutil.CanonicalJSON(true)
	require.NoError(t, err)
	assert.Equal(t, "true", out)

	out, err = docutil.CanonicalJSON(false)
	require.NoError(t, err)
	assert.Equal(t, "false", out)

	// Empty containers.
	out, err = docutil.CanonicalJSON(map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, "{}", out)

	out, err = docutil.CanonicalJSON([]any{})
	require.NoError(t, err)
	assert.Equal(t, "[]", out)
}

func TestCanonicalJSONNumbers(t *testing.T) {
	// JSON.stringify semantics: integers print without a decimal point.
	out, err := docutil.CanonicalJSON(map[string]any{"n": float64(2)})
	require.NoError(t, err)
	assert.Equal(t, `{"n":2}`, out)

	out, err = docutil.CanonicalJSON(map[string]any{"n": float64(2.5)})
	require.NoError(t, err)
	assert.Equal(t, `{"n":2.5}`, out)

	// Large magnitudes fall back to Go's marshaling (matches JSON.stringify).
	out, err = docutil.CanonicalJSON(map[string]any{"n": float64(1e21)})
	require.NoError(t, err)
	assert.Equal(t, `{"n":1e+21}`, out)

	// Generic-form ints.
	out, err = docutil.CanonicalJSON(map[string]any{"i": 42, "i64": int64(42)})
	require.NoError(t, err)
	assert.Equal(t, `{"i":42,"i64":42}`, out)

	// json.Number round-trips its literal form.
	out, err = docutil.CanonicalJSON(map[string]any{"n": json.Number("1.50")})
	require.NoError(t, err)
	assert.Equal(t, `{"n":1.50}`, out)
}

func TestCanonicalJSONRawMessage(t *testing.T) {
	// json.RawMessage is re-parsed into generic form and canonicalized.
	out, err := docutil.CanonicalJSON(json.RawMessage(`{"b":1,"a":2}`))
	require.NoError(t, err)
	assert.Equal(t, `{"a":2,"b":1}`, out)

	// Malformed raw JSON fails closed.
	_, err = docutil.CanonicalJSON(json.RawMessage(`{"b":`))
	require.Error(t, err)
}
