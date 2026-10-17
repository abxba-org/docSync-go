package testkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTkCov_goldenResolveDir(t *testing.T) {
	abs := t.TempDir()
	r := resolveGoldenDir(abs)
	assert.Equal(t, abs, r)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	r = resolveGoldenDir("testdata/rel")
	assert.Equal(t, filepath.Join(cwd, "testdata", "rel"), r)
	r2 := resolveGoldenDir("testdata/rel/")
	assert.Equal(t, r, r2)
}

func TestTkCov_goldenPathForUnsafeName(t *testing.T) {
	_, err := GoldenPathFor("/tmp", "bad/name")
	require.Error(t, err)
	_, err = GoldenPathFor("/tmp", "a/b")
	require.Error(t, err)
	p, err := GoldenPathFor("/tmp", "safe")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/tmp", "safe"+GoldenSuffix), p)
}

func TestTkCov_canonicalizeNonJSON(t *testing.T) {
	_, err := canonicalize(make(chan int))
	require.Error(t, err)
}

func TestTkCov_ExpectGolden_missingFile(t *testing.T) {
	dir := t.TempDir()
	err := ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: "nope"})
	require.Error(t, err)
	var assertErr *GoldenAssertionError
	require.ErrorAs(t, err, &assertErr)
	assert.Contains(t, err.Error(), "golden file missing")
}

func TestTkCov_ExpectGolden_updateModeCreates(t *testing.T) {
	dir := t.TempDir()
	err := ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: "create", Update: true})
	require.NoError(t, err)
	exists, err := GoldenExists(dir, "create")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestTkCov_ExpectGolden_badJSONFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bad.golden.json")
	require.NoError(t, os.WriteFile(file, []byte("not json"), 0o644))
	err := ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: "bad"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestTkCov_ExpectGolden_mismatch(t *testing.T) {
	dir := t.TempDir()
	err := ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: "m", Update: true})
	require.NoError(t, err)
	err = ExpectGolden(map[string]any{"a": 2}, GoldenOptions{Dir: dir, Name: "m"})
	require.Error(t, err)
	var assertErr *GoldenAssertionError
	require.ErrorAs(t, err, &assertErr)
	assert.Equal(t, json.Number("2"), assertErr.Actual.(map[string]any)["a"])
}

func TestTkCov_ExpectGolden_keyOrderInsensitiveMatch(t *testing.T) {
	dir := t.TempDir()
	err := ExpectGolden(map[string]any{"a": 1, "b": 2}, GoldenOptions{Dir: dir, Name: "order", Update: true})
	require.NoError(t, err)
	err = ExpectGolden(map[string]any{"b": 2, "a": 1}, GoldenOptions{Dir: dir, Name: "order"})
	require.NoError(t, err)
}

func TestTkCov_ReadGolden_absent(t *testing.T) {
	dir := t.TempDir()
	read, err := ReadGolden(dir, "absent")
	require.NoError(t, err)
	assert.False(t, read.Exists)
}

func TestTkCov_ReadGolden_invalidJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.golden.json"), []byte("{"), 0o644))
	_, err := ReadGolden(dir, "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestTkCov_ReadGolden_valid(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "v.golden.json"), []byte("{\"a\":1}\n"), 0o644))
	read, err := ReadGolden(dir, "v")
	require.NoError(t, err)
	assert.True(t, read.Exists)
	assert.NotNil(t, read.Parsed)
}

func TestTkCov_GoldenExists_absent(t *testing.T) {
	dir := t.TempDir()
	exists, err := GoldenExists(dir, "absent")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestTkCov_GoldenExists_directory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub"+GoldenSuffix)
	require.NoError(t, os.MkdirAll(sub, 0o755))
	exists, err := GoldenExists(dir, "sub")
	require.NoError(t, err)
	assert.False(t, exists, "a directory is not a regular file")
}

func TestTkCov_GoldenExists_invalidName(t *testing.T) {
	_, err := GoldenExists("/tmp", "bad/name")
	require.Error(t, err)
}

func TestTkCov_DiffPaths(t *testing.T) {
	assert.Empty(t, DiffPaths(nil, nil, 0))
	assert.Equal(t, []string{"$: expected 1, actual 2"},
		DiffPaths(json.Number("1"), json.Number("2"), 10))
	diffs := DiffPaths(map[string]any{"a": 1}, map[string]any{"a": 2}, 10)
	require.NotEmpty(t, diffs)
}

func TestTkCov_DiffPaths_arrays(t *testing.T) {
	diffs := DiffPaths([]any{1, 2}, []any{1}, 10)
	require.NotEmpty(t, diffs)
	diffs = DiffPaths([]any{1}, []any{1, 2}, 10)
	require.NotEmpty(t, diffs)
}

func TestTkCov_DiffPaths_objects(t *testing.T) {
	diffs := DiffPaths(map[string]any{"b": 1}, map[string]any{"a": 2, "b": 1}, 10)
	require.NotEmpty(t, diffs)
	diffs = DiffPaths(map[string]any{"a": 1, "b": 2}, map[string]any{"a": 1}, 10)
	require.NotEmpty(t, diffs)
}

func TestTkCov_isUpdateMode_env(t *testing.T) {
	old := os.Getenv(UpdateGoldensEnv)
	defer func() { _ = os.Setenv(UpdateGoldensEnv, old) }()
	require.NoError(t, os.Setenv(UpdateGoldensEnv, "1"))
	assert.True(t, isUpdateMode(false))
	require.NoError(t, os.Setenv(UpdateGoldensEnv, ""))
	assert.False(t, isUpdateMode(false))
	assert.True(t, isUpdateMode(true))
}

func TestTkCov_renderLeaf(t *testing.T) {
	assert.Equal(t, "null", renderLeaf(nil))
	assert.Equal(t, "1", renderLeaf(json.Number("1")))
	assert.Equal(t, "1.5", renderLeaf(1.5))
	assert.Equal(t, "1", renderLeaf(int(1)))
	assert.Equal(t, "2", renderLeaf(int64(2)))
	assert.Equal(t, "array(2)", renderLeaf([]any{1, 2}))
	assert.Equal(t, "object(1 keys)", renderLeaf(map[string]any{"a": 1}))
	assert.Contains(t, renderLeaf(struct{}{}), "unexpected-type")
	assert.Equal(t, "true", renderLeaf(true))
	assert.Contains(t, renderLeaf("abc"), "abc")
}

func TestTkCov_jsNumber(t *testing.T) {
	assert.Equal(t, "1", jsNumber(1.0))
	assert.Equal(t, "1.5", jsNumber(1.5))
	assert.Equal(t, "1e+30", jsNumber(1e30))
}

func TestTkCov_sameJSONValue(t *testing.T) {
	assert.True(t, sameJSONValue(nil, nil))
	assert.False(t, sameJSONValue(nil, 1))
	assert.True(t, sameJSONValue("a", "a"))
	assert.False(t, sameJSONValue("a", "b"))
	assert.True(t, sameJSONValue(true, true))
	assert.False(t, sameJSONValue(true, false))
	assert.True(t, sameJSONValue(json.Number("1"), json.Number("1")))
	assert.False(t, sameJSONValue(json.Number("1"), json.Number("2")))
	assert.True(t, sameJSONValue(1.5, 1.5))
	assert.False(t, sameJSONValue(1.5, 2.5))
	assert.False(t, sameJSONValue([]any{1}, []any{1}))
	assert.False(t, sameJSONValue(map[string]any{"a": 1}, map[string]any{"a": 1}))
}

func TestTkCov_writeGoldenFile_failure(t *testing.T) {
	// An unwritable parent dir triggers the WriteFile failure.
	dir := t.TempDir()
	if os.Getuid() == 0 {
		t.Skip("running as root; permission checks are bypassed")
	}
	roDir := filepath.Join(dir, "ro")
	require.NoError(t, os.MkdirAll(roDir, 0o555))
	file := filepath.Join(roDir, "x.golden.json")
	err := os.WriteFile(file, []byte("x"), 0o644)
	if err == nil {
		t.Skip("directory not actually unwritable")
	}
	err = writeGoldenFile(file, "{}")
	require.Error(t, err)
}
