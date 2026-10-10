package docutil_test

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentHash(t *testing.T) {
	// Known sha256 of the empty string.
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", docutil.ContentHash(""))
	assert.Equal(t, docutil.ContentHash("hello"), docutil.ContentHashBytes([]byte("hello")))
}

func TestULIDDeterministic(t *testing.T) {
	// Ported from core/test/ulid.test.ts: fixed clock + deterministic rng ⇒
	// fully deterministic ULIDs; with reset the sequence restarts.
	var ms int64 = 1_700_000_000_000
	gen := docutil.NewULIDGeneratorWith(func() int64 { return ms }, func(n int) []byte {
		out := make([]byte, n)
		for i := range out {
			out[i] = byte(i + 1)
		}
		return out
	})
	first, err := gen.Generate()
	require.NoError(t, err)
	second, err := gen.Generate()
	require.NoError(t, err)
	assert.Len(t, first, 26)
	assert.True(t, docutil.IsValidULID(first))
	// Same millisecond ⇒ monotonic: second sorts after first.
	assert.True(t, second > first, "expected %s > %s", second, first)
	gen.Reset()
	third, err := gen.Generate()
	require.NoError(t, err)
	assert.Equal(t, first, third)
}

func TestULIDMonotonicAcrossClockSteps(t *testing.T) {
	steps := []int64{100, 50, 200, 10}
	idx := 0
	gen := docutil.NewULIDGeneratorWith(func() int64 { v := steps[idx%len(steps)]; idx++; return v }, func(n int) []byte {
		out := make([]byte, n)
		return out
	})
	prev := ""
	for i := 0; i < 12; i++ {
		v, err := gen.Generate()
		require.NoError(t, err)
		assert.True(t, docutil.IsValidULID(v))
		if prev != "" {
			assert.True(t, v > prev, "ulid must be monotonic: %s after %s", v, prev)
		}
		prev = v
	}
}

func TestULIDRejectsOutOfRangeTimestamp(t *testing.T) {
	gen := docutil.NewULIDGeneratorWith(func() int64 { return -1 }, func(n int) []byte { return make([]byte, n) })
	_, err := gen.Generate()
	require.Error(t, err)
}

func TestIsValidULID(t *testing.T) {
	assert.True(t, docutil.IsValidULID("01ARZ3NDEKTSV4RRFFQ69G5FAV"))
	assert.False(t, docutil.IsValidULID("short"))
	assert.False(t, docutil.IsValidULID("01ARZ3NDEKTSV4RRFFQ69G5FAI")) // I not in Crockford
	assert.False(t, docutil.IsValidULID("01ARZ3NDEKTSV4RRFFQ69G5FAVX"))
}

func TestCanonicalJSON(t *testing.T) {
	// Keys sorted recursively; arrays keep order.
	v := map[string]any{
		"b": 2,
		"a": []any{"y", "x", map[string]any{"d": 1, "c": 2}},
	}
	out, err := docutil.CanonicalJSON(v)
	require.NoError(t, err)
	assert.Equal(t, `{"a":["y","x",{"c":2,"d":1}],"b":2}`, out)
}

func TestCanonicalJSONPreservesStringEscaping(t *testing.T) {
	out, err := docutil.CanonicalJSON(map[string]any{"html": "<a>&\"'</a>", "uni": "é"})
	require.NoError(t, err)
	assert.Equal(t, `{"html":"<a>&\"'</a>","uni":"é"}`, out)
}

func TestCanonicalJSONRejectsUnsupportedTypes(t *testing.T) {
	_, err := docutil.CanonicalJSON(map[string]any{"f": func() {}})
	require.Error(t, err)
}

func TestCodedErrors(t *testing.T) {
	err := docutil.ConfigError("bad config: %s", "x")
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "bad config: x")
	wrapped := docutil.ManifestError("m").At("documents", "0").Wrap(err)
	assert.Equal(t, docutil.CodeManifest, docutil.CodeOf(wrapped))
	assert.True(t, strings.Contains(wrapped.Error(), "documents.0"))
	assert.ErrorIs(t, wrapped, err)
}
