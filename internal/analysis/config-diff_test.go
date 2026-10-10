package analysis_test

// Ported from packages/core/test/config-diff.test.ts (traceability, plan.md
// §7): key-level resolved-value config diff tests (Phase 11, P11-T08; ARCH
// §4.1) — nested flattening to dot-paths, added/removed/changed classification,
// secret masking (change reported, values never leak), env-ref passthrough,
// array whole-value leaves, fail-closed typed errors on non-object roots, the
// versioned secret pattern, summary counts, and deterministic ordering.

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/docutil"
)

// requireConfigSide asserts a present diff side carrying the expected value.
func requireConfigSide(t *testing.T, side *any, expected any) {
	t.Helper()
	require.NotNil(t, side, "expected the side to be present")
	assert.Equal(t, expected, *side)
}

// requireConfigSideAbsent asserts a diff side omitted for a missing key
// (TS: the optional field absent).
func requireConfigSideAbsent(t *testing.T, side *any) {
	t.Helper()
	assert.Nil(t, side, "expected the side to be omitted")
}

// requireDiffKeys asserts the ordered key list of a diff result.
func requireDiffKeys(t *testing.T, diffs []analysis.ConfigKeyDiff, keys ...string) {
	t.Helper()
	got := make([]string, 0, len(diffs))
	for _, diff := range diffs {
		got = append(got, diff.Key)
	}
	assert.Equal(t, keys, got)
}

// diffByKey indexes a diff result by key (TS `new Map(diffs.map((d) => [d.key, d]))`).
func diffByKey(diffs []analysis.ConfigKeyDiff) map[string]analysis.ConfigKeyDiff {
	byKey := make(map[string]analysis.ConfigKeyDiff, len(diffs))
	for _, diff := range diffs {
		byKey[diff.Key] = diff
	}
	return byKey
}

// configDiffInputMessage returns the coded error's message — the exact TS
// Error.message text, without the code prefix docutil prepends on Error().
func configDiffInputMessage(t *testing.T, err error) string {
	t.Helper()
	var coded *docutil.Error
	require.ErrorAs(t, err, &coded)
	return coded.Msg
}

func TestDiffConfigKeys_NestedFlattening_P11T08(t *testing.T) {
	t.Run("flattens nested plain objects to dot-path leaves", func(t *testing.T) {
		before := map[string]any{"server": map[string]any{"port": 8080, "host": "localhost"}, "debug": false}
		after := map[string]any{"server": map[string]any{"port": 9090, "host": "localhost"}, "debug": false}
		diffs, err := analysis.DiffConfigKeys(before, after)
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		assert.Equal(t, "server.port", diffs[0].Key)
		requireConfigSide(t, diffs[0].Before, 8080)
		requireConfigSide(t, diffs[0].After, 9090)
		assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)
		assert.False(t, diffs[0].SecretMasked)
		assert.False(t, diffs[0].EnvRef)
	})

	t.Run("deeply nested keys resolve through multiple levels", func(t *testing.T) {
		before := map[string]any{"db": map[string]any{"connection": map[string]any{"host": "a", "port": 5432}}}
		after := map[string]any{"db": map[string]any{"connection": map[string]any{"host": "b", "port": 5432}}}
		diffs, err := analysis.DiffConfigKeys(before, after)
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		assert.Equal(t, "db.connection.host", diffs[0].Key)
		requireConfigSide(t, diffs[0].Before, "a")
		requireConfigSide(t, diffs[0].After, "b")
		assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)
		assert.False(t, diffs[0].SecretMasked)
	})

	t.Run("flat roots and mixed-depth roots both work; empty nested objects flatten away", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(map[string]any{"port": 1}, map[string]any{"port": 2})
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		assert.Equal(t, "port", diffs[0].Key)
		requireConfigSide(t, diffs[0].Before, 1)
		requireConfigSide(t, diffs[0].After, 2)
		assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)
		assert.False(t, diffs[0].SecretMasked)

		same, err := analysis.DiffConfigKeys(
			map[string]any{"a": map[string]any{"b": map[string]any{}}},
			map[string]any{"a": map[string]any{"b": map[string]any{}}},
		)
		require.NoError(t, err)
		assert.Empty(t, same)
	})
}

func TestDiffConfigKeys_ChangeClassification(t *testing.T) {
	t.Run("classifies added keys (before omitted) and removed keys (after omitted)", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(
			map[string]any{"keep": 1, "gone": "x"},
			map[string]any{"keep": 1, "fresh": true},
		)
		require.NoError(t, err)
		require.Len(t, diffs, 2)

		assert.Equal(t, "fresh", diffs[0].Key)
		requireConfigSideAbsent(t, diffs[0].Before)
		requireConfigSide(t, diffs[0].After, true)
		assert.Equal(t, analysis.ConfigKeyAdded, diffs[0].Change)
		assert.False(t, diffs[0].SecretMasked)

		assert.Equal(t, "gone", diffs[1].Key)
		requireConfigSide(t, diffs[1].Before, "x")
		requireConfigSideAbsent(t, diffs[1].After)
		assert.Equal(t, analysis.ConfigKeyRemoved, diffs[1].Change)
		assert.False(t, diffs[1].SecretMasked)
	})

	t.Run(`typed values compare typed: 1 vs "1" and null vs false are changes`, func(t *testing.T) {
		typed, err := analysis.DiffConfigKeys(map[string]any{"a": 1}, map[string]any{"a": "1"})
		require.NoError(t, err)
		assert.Len(t, typed, 1)

		nullVsFalse, err := analysis.DiffConfigKeys(map[string]any{"a": nil}, map[string]any{"a": false})
		require.NoError(t, err)
		require.Len(t, nullVsFalse, 1)
		requireConfigSide(t, nullVsFalse[0].Before, nil)
		requireConfigSide(t, nullVsFalse[0].After, false)
		assert.Equal(t, analysis.ConfigKeyChanged, nullVsFalse[0].Change)

		equal, err := analysis.DiffConfigKeys(map[string]any{"a": 1}, map[string]any{"a": 1})
		require.NoError(t, err)
		assert.Empty(t, equal)
	})

	t.Run("every Go numeric kind is one JS number (port strengthening)", func(t *testing.T) {
		// JS has a single number type, so an int32 leaf and an int leaf of the
		// same value are the SAME resolved value (parsers here emit float64,
		// test literals land on int; exotic kinds normalize the same way).
		same, err := analysis.DiffConfigKeys(map[string]any{"a": int32(1)}, map[string]any{"a": 1})
		require.NoError(t, err)
		assert.Empty(t, same)

		numbered, err := analysis.DiffConfigKeys(map[string]any{"a": json.Number("1")}, map[string]any{"a": 1})
		require.NoError(t, err)
		assert.Empty(t, numbered)

		changed, err := analysis.DiffConfigKeys(map[string]any{"a": int32(1)}, map[string]any{"a": 2})
		require.NoError(t, err)
		require.Len(t, changed, 1)
		requireConfigSide(t, changed[0].Before, int32(1))
		requireConfigSide(t, changed[0].After, 2)

		for _, pair := range [][2]any{
			{int8(1), 1}, {int16(1), 1}, {int64(1), 1},
			{uint(1), 1}, {uint8(1), 1}, {uint16(1), 1},
			{uint32(1), 1}, {uint64(1), 1}, {float32(1), 1},
		} {
			same, err := analysis.DiffConfigKeys(map[string]any{"a": pair[0]}, map[string]any{"a": pair[1]})
			require.NoError(t, err)
			assert.Empty(t, same, pair)
		}
	})
}

func TestDiffConfigKeys_SecretMasking(t *testing.T) {
	t.Run("db.password changed: both sides masked, change still reported, count 1", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(
			map[string]any{"db": map[string]any{"password": "hunter2"}},
			map[string]any{"db": map[string]any{"password": "s3cret"}},
		)
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		assert.Equal(t, "db.password", diffs[0].Key)
		requireConfigSide(t, diffs[0].Before, analysis.ConfigSecretMask)
		requireConfigSide(t, diffs[0].After, analysis.ConfigSecretMask)
		assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)
		assert.True(t, diffs[0].SecretMasked)
		assert.Equal(t, analysis.ConfigDiffSummary{Changed: 1, SecretMaskedCount: 1}, analysis.SummarizeConfigDiff(diffs))
	})

	t.Run("added and removed secret keys are masked too; credentials segments match", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(
			map[string]any{"db": map[string]any{"credentials": map[string]any{"host": "a"}}},
			map[string]any{"db": map[string]any{"credentials": map[string]any{"host": "b"}, "apiKey": "z"}},
		)
		require.NoError(t, err)
		byKey := diffByKey(diffs)

		host := byKey["db.credentials.host"]
		requireConfigSide(t, host.Before, analysis.ConfigSecretMask)
		requireConfigSide(t, host.After, analysis.ConfigSecretMask)
		assert.Equal(t, analysis.ConfigKeyChanged, host.Change)
		assert.True(t, host.SecretMasked)

		apiKey := byKey["db.apiKey"]
		requireConfigSideAbsent(t, apiKey.Before)
		requireConfigSide(t, apiKey.After, analysis.ConfigSecretMask)
		assert.Equal(t, analysis.ConfigKeyAdded, apiKey.Change)
		assert.True(t, apiKey.SecretMasked)

		removedSecret, err := analysis.DiffConfigKeys(map[string]any{"secretToken": "t"}, map[string]any{})
		require.NoError(t, err)
		require.Len(t, removedSecret, 1)
		assert.Equal(t, "secretToken", removedSecret[0].Key)
		requireConfigSide(t, removedSecret[0].Before, analysis.ConfigSecretMask)
		requireConfigSideAbsent(t, removedSecret[0].After)
		assert.Equal(t, analysis.ConfigKeyRemoved, removedSecret[0].Change)
		assert.True(t, removedSecret[0].SecretMasked)
	})

	t.Run("the masking rule is versioned exported data (case-insensitive match)", func(t *testing.T) {
		assert.True(t, analysis.ConfigSecretKeyPattern.MatchString("PASSWORD"))
		assert.True(t, analysis.ConfigSecretKeyPattern.MatchString("client_secret"))
		assert.True(t, analysis.ConfigSecretKeyPattern.MatchString("apikey"))
		assert.False(t, analysis.ConfigSecretKeyPattern.MatchString("port"))
	})
}

func TestDiffConfigKeys_EnvRefPassthrough(t *testing.T) {
	t.Run("reports ${VAR} and $VAR references verbatim as env-ref resolved values", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(
			map[string]any{"server": map[string]any{"port": "${PORT}"}},
			map[string]any{"server": map[string]any{"port": "$PORT"}},
		)
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		assert.Equal(t, "server.port", diffs[0].Key)
		requireConfigSide(t, diffs[0].Before, "${PORT}")
		requireConfigSide(t, diffs[0].After, "$PORT")
		assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)
		assert.False(t, diffs[0].SecretMasked)
		assert.True(t, diffs[0].EnvRef)
	})

	t.Run("an env-ref vs concrete value is still a change; plain strings are not env-refs", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(map[string]any{"a": "$HOST"}, map[string]any{"a": "localhost"})
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		requireConfigSide(t, diffs[0].Before, "$HOST")
		requireConfigSide(t, diffs[0].After, "localhost")
		assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)
		assert.False(t, diffs[0].SecretMasked)
		assert.True(t, diffs[0].EnvRef)

		plain, err := analysis.DiffConfigKeys(map[string]any{"a": "cost$total"}, map[string]any{"a": "cost$total2"})
		require.NoError(t, err)
		require.Len(t, plain, 1)
		requireConfigSide(t, plain[0].Before, "cost$total")
		requireConfigSide(t, plain[0].After, "cost$total2")
		assert.Equal(t, analysis.ConfigKeyChanged, plain[0].Change)
		assert.False(t, plain[0].SecretMasked)
		assert.False(t, plain[0].EnvRef)
	})
}

func TestDiffConfigKeys_ArrayWholeValues(t *testing.T) {
	t.Run("nested arrays compare as one blob, never flattened", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(map[string]any{"tags": []any{"a", "b"}}, map[string]any{"tags": []any{"b", "a"}})
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		assert.Equal(t, "tags", diffs[0].Key)
		requireConfigSide(t, diffs[0].Before, []any{"a", "b"})
		requireConfigSide(t, diffs[0].After, []any{"b", "a"})
		assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)
		assert.False(t, diffs[0].SecretMasked)

		same, err := analysis.DiffConfigKeys(map[string]any{"tags": []any{"a"}}, map[string]any{"tags": []any{"a"}})
		require.NoError(t, err)
		assert.Empty(t, same)
	})

	t.Run("a root array is ONE whole-value leaf under the empty key", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys([]any{"a"}, []any{"a", "b"})
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		assert.Equal(t, "", diffs[0].Key)
		requireConfigSide(t, diffs[0].Before, []any{"a"})
		requireConfigSide(t, diffs[0].After, []any{"a", "b"})
		assert.Equal(t, analysis.ConfigKeyChanged, diffs[0].Change)
		assert.False(t, diffs[0].SecretMasked)

		mixed, err := analysis.DiffConfigKeys([]any{"a"}, map[string]any{"a": 1})
		require.NoError(t, err)
		require.Len(t, mixed, 2)
		assert.Equal(t, "", mixed[0].Key)
		requireConfigSide(t, mixed[0].Before, []any{"a"})
		requireConfigSideAbsent(t, mixed[0].After)
		assert.Equal(t, analysis.ConfigKeyRemoved, mixed[0].Change)
		assert.False(t, mixed[0].SecretMasked)
		assert.Equal(t, "a", mixed[1].Key)
		requireConfigSideAbsent(t, mixed[1].Before)
		requireConfigSide(t, mixed[1].After, 1)
		assert.Equal(t, analysis.ConfigKeyAdded, mixed[1].Change)
		assert.False(t, mixed[1].SecretMasked)
	})
}

func TestDiffConfigKeys_FailClosedTypedErrors(t *testing.T) {
	t.Run("non-object roots fail closed with the coded config error", func(t *testing.T) {
		for _, bad := range []struct {
			value  any
			detail string
		}{
			{42, `type "number"`},
			{"text", `type "string"`},
			{true, `type "boolean"`},
			{nil, "null"},
		} {
			_, err := analysis.DiffConfigKeys(bad.value, map[string]any{})
			require.Error(t, err)
			assert.Equal(t, analysis.ConfigDiffInputCode, docutil.CodeOf(err))
			assert.Equal(t, "config diff: before root is not diffable — "+bad.detail, configDiffInputMessage(t, err))

			_, err = analysis.DiffConfigKeys(map[string]any{}, bad.value)
			require.Error(t, err)
			assert.Equal(t, analysis.ConfigDiffInputCode, docutil.CodeOf(err))
			assert.Equal(t, "config diff: after root is not diffable — "+bad.detail, configDiffInputMessage(t, err))
		}

		// TS asserted the distinguishable code EDOCSYNC_CONFIG_DIFF_INPUT and
		// that the message names the side; docutil carries no such code, so
		// the typed error maps to the closest listed one
		// (analysis.ConfigDiffInputCode = docutil.CodeConfig) with the exact
		// message text preserved (DEVIATION recorded per the conventions).
		_, err := analysis.DiffConfigKeys(nil, map[string]any{})
		require.Error(t, err)
		assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
		assert.Contains(t, err.Error(), "before")
	})

	t.Run("non-JSON leaf values fail closed per side", func(t *testing.T) {
		// TS: `{ a: new Date(0) }` — a Date is not JSON-representable here.
		_, err := analysis.DiffConfigKeys(map[string]any{"a": time.Unix(0, 0)}, map[string]any{})
		require.Error(t, err)
		assert.Equal(t, analysis.ConfigDiffInputCode, docutil.CodeOf(err))
		assert.Equal(t,
			`config diff: before root is not diffable — value at key "a" is not JSON-representable`,
			configDiffInputMessage(t, err))

		// TS: `{ a: 1n }` — Go has no bigint literal; *big.Int is the same
		// not-JSON-representable leaf class, and the message is type-free.
		_, err = analysis.DiffConfigKeys(map[string]any{}, map[string]any{"a": big.NewInt(1)})
		require.Error(t, err)
		assert.Equal(t, analysis.ConfigDiffInputCode, docutil.CodeOf(err))
		assert.Equal(t,
			`config diff: after root is not diffable — value at key "a" is not JSON-representable`,
			configDiffInputMessage(t, err))
	})

	t.Run("non-JSON-safe values at any depth and Go-only roots fail closed (port strengthening)", func(t *testing.T) {
		// A non-representable leaf under a nested object path.
		_, err := analysis.DiffConfigKeys(map[string]any{}, map[string]any{"a": map[string]any{"b": big.NewInt(1)}})
		require.Error(t, err)
		assert.Equal(t,
			`config diff: after root is not diffable — value at key "a.b" is not JSON-representable`,
			configDiffInputMessage(t, err))

		// A non-representable element inside an array leaf (recursive check).
		_, err = analysis.DiffConfigKeys(map[string]any{"tags": []any{time.Unix(0, 0)}}, map[string]any{})
		require.Error(t, err)
		assert.Equal(t,
			`config diff: before root is not diffable — value at key "tags" is not JSON-representable`,
			configDiffInputMessage(t, err))

		// A non-representable value inside an object nested in an array leaf.
		_, err = analysis.DiffConfigKeys(
			map[string]any{"tags": []any{map[string]any{"x": big.NewInt(1)}}},
			map[string]any{},
		)
		require.Error(t, err)
		assert.Equal(t,
			`config diff: before root is not diffable — value at key "tags" is not JSON-representable`,
			configDiffInputMessage(t, err))

		// A non-representable element inside the ROOT array leaf (checked
		// under the "root" key path).
		_, err = analysis.DiffConfigKeys([]any{time.Unix(0, 0)}, map[string]any{})
		require.Error(t, err)
		assert.Equal(t,
			`config diff: before root is not diffable — value at key "root" is not JSON-representable`,
			configDiffInputMessage(t, err))

		// Go-only root values (no TS analog) fail closed in the "object"
		// bucket — never a guessed empty diff (D14).
		_, err = analysis.DiffConfigKeys(time.Unix(0, 0), map[string]any{})
		require.Error(t, err)
		assert.Equal(t, `config diff: before root is not diffable — type "object"`, configDiffInputMessage(t, err))

		_, err = analysis.DiffConfigKeys(json.Number("1"), map[string]any{})
		require.Error(t, err)
		assert.Equal(t, `config diff: before root is not diffable — type "number"`, configDiffInputMessage(t, err))
	})
}

func TestSummarizeConfigDiff(t *testing.T) {
	t.Run("counts each change class and secret-masked entries", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(
			map[string]any{"keep": 1, "gone": 2, "db": map[string]any{"password": "x"}, "apiKey": "y"},
			map[string]any{"keep": 1, "fresh": 3, "db": map[string]any{"password": "z"}, "limits": []any{1}},
		)
		require.NoError(t, err)
		assert.Equal(t, analysis.ConfigDiffSummary{
			Added:             2, // fresh, limits
			Removed:           2, // gone, apiKey
			Changed:           1, // db.password
			SecretMaskedCount: 2, // db.password, apiKey
		}, analysis.SummarizeConfigDiff(diffs))
	})

	t.Run("an empty diff summarizes to zeros", func(t *testing.T) {
		diffs, err := analysis.DiffConfigKeys(map[string]any{"a": 1}, map[string]any{"a": 1})
		require.NoError(t, err)
		assert.Equal(t, analysis.ConfigDiffSummary{}, analysis.SummarizeConfigDiff(diffs))
	})
}

func TestDiffConfigKeys_Determinism(t *testing.T) {
	t.Run("output is sorted by key and stable across repeated runs", func(t *testing.T) {
		before := map[string]any{"z": 1, "a": map[string]any{"c": 1, "m": 2}, "b": 3}
		after := map[string]any{"z": 2, "a": map[string]any{"c": 2, "m": 2}, "b": 4}
		once, err := analysis.DiffConfigKeys(before, after)
		require.NoError(t, err)
		requireDiffKeys(t, once, "a.c", "b", "z")
		for i := 0; i < 5; i++ {
			again, err := analysis.DiffConfigKeys(before, after)
			require.NoError(t, err)
			assert.Equal(t, once, again)
		}
	})

	t.Run("key order is UTF-16 code units, not Go byte order (port strengthening)", func(t *testing.T) {
		// A non-BMP key (😀, UTF-16 surrogate pair D83D DE00) sorts BEFORE the
		// U+FFFF key under TS Array#sort code-unit comparison, while Go's
		// byte-wise order (F0-prefixed after EF-prefixed) would reverse them.
		// ASCII/BMP keys order identically either way.
		before := map[string]any{}
		after := map[string]any{"\uffff": 1, "😀": 2}
		diffs, err := analysis.DiffConfigKeys(before, after)
		require.NoError(t, err)
		requireDiffKeys(t, diffs, "😀", "\uffff")
	})
}
