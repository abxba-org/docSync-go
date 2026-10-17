package docutil_test

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/ulid.test.ts.

// crockfordAlphabet is the canonical ULID encoding alphabet (no I, L, O, U).
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// decodeTimeChars decodes the 10-char timestamp half of a ULID back to
// milliseconds (the TS test's decodeTimeChars).
func decodeTimeChars(t *testing.T, ulid string) int64 {
	t.Helper()
	var value int64
	for _, char := range ulid[:10] {
		digit := int64(strings.IndexRune(crockfordAlphabet, char))
		require.GreaterOrEqual(t, digit, int64(0), "non-Crockford char in ULID: %c", char)
		value = value*32 + digit
	}
	return value
}

// fixedRng returns the SAME deterministic bytes on every call, mirroring the
// TS test's fixedRng.
func fixedRng(n int) ([]byte, error) {
	out := make([]byte, n)
	for index := range out {
		out[index] = byte((index*7 + 3) % 256)
	}
	return out, nil
}

func TestUlidShapeAndSameMillisecondMonotonicity(t *testing.T) {
	ms := int64(1_700_000_000_000)
	gen := docutil.NewULIDGeneratorWith(func() int64 { return ms }, fixedRng)

	first, err := gen.Generate()
	require.NoError(t, err)
	second, err := gen.Generate()
	require.NoError(t, err)
	third, err := gen.Generate()
	require.NoError(t, err)

	for _, ulid := range []string{first, second, third} {
		assert.Len(t, ulid, 26)
		assert.True(t, docutil.IsValidULID(ulid))
		// Canonical shape is uppercase.
		assert.Equal(t, ulid, strings.ToUpper(ulid))
	}
	// Monotonic: identical injected time → strictly increasing values.
	assert.True(t, second > first)
	assert.True(t, third > second)
	// The timestamp half encodes the injected millisecond exactly.
	decodeTimeChars(t, first)
	assert.Equal(t, ms, decodeTimeChars(t, first))
	assert.Equal(t, ms, decodeTimeChars(t, third))
}

func TestUlidLaterTimestampsSortAfterEarlierBackwardsClocksNeverShrink(t *testing.T) {
	steps := []int64{1_000, 2_000, 500}
	idx := 0
	gen := docutil.NewULIDGeneratorWith(func() int64 { v := steps[idx]; idx++; return v }, fixedRng)

	earlier, err := gen.Generate()
	require.NoError(t, err)
	later, err := gen.Generate()
	require.NoError(t, err)
	assert.True(t, later > earlier)
	// Clock steps backwards: the timestamp is held, randomness increments —
	// the id still grows (monotonicity guarantee).
	backwards, err := gen.Generate()
	require.NoError(t, err)
	assert.True(t, backwards > later)
}

func TestUlidRejectsOutOfRangeTimestamps(t *testing.T) {
	// RangeError for -1 and 2^48 in TS.
	gen := docutil.NewULIDGeneratorWith(func() int64 { return -1 }, fixedRng)
	_, err := gen.Generate()
	require.Error(t, err)

	gen = docutil.NewULIDGeneratorWith(func() int64 { return int64(1) << 48 }, fixedRng)
	_, err = gen.Generate()
	require.Error(t, err)

	// TIME_MAX (2^48 - 1) itself is in range.
	gen = docutil.NewULIDGeneratorWith(func() int64 { return (int64(1) << 48) - 1 }, fixedRng)
	_, err = gen.Generate()
	require.NoError(t, err)
}

func TestUlidRejectsMalformedIds(t *testing.T) {
	assert.True(t, docutil.IsValidULID("01ARZ3NDEKTSV4RRFFQ69G5FAV"))
	// Lowercase is not canonical Crockford.
	assert.False(t, docutil.IsValidULID("01arz3ndektsv4rrffq69g5fav"))
	assert.False(t, docutil.IsValidULID("01ARZ3NDEKTSV4RRFFQ69G5FA"))
	assert.False(t, docutil.IsValidULID("01ARZ3NDEKTSV4RRFFQ69G5FAI"))
}

func TestUlidDeterministicUnderInjectedClockAndRng(t *testing.T) {
	gen := docutil.NewULIDGeneratorWith(func() int64 { return 7_000 }, fixedRng)

	gen.Reset()
	a1, err := gen.Generate()
	require.NoError(t, err)
	a2, err := gen.Generate()
	require.NoError(t, err)

	gen.Reset()
	b1, err := gen.Generate()
	require.NoError(t, err)
	b2, err := gen.Generate()
	require.NoError(t, err)

	// Same clock+rng + reset state → identical sequence.
	assert.Equal(t, []string{a1, a2}, []string{b1, b2})
}

func TestNewULIDGeneratorProducesValidIds(t *testing.T) {
	// Default path: wall clock + crypto/rand — shape must still be canonical.
	gen := docutil.NewULIDGenerator()
	first, err := gen.Generate()
	require.NoError(t, err)
	second, err := gen.Generate()
	require.NoError(t, err)
	assert.True(t, docutil.IsValidULID(first))
	assert.True(t, docutil.IsValidULID(second))
	assert.True(t, second >= first)
}
