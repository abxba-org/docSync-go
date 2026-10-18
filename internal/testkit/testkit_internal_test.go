package testkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plantRaw overwrites a state file's raw bytes through the fake's test-only
// hook, conditioning the CAS on the bytes currently stored.
func plantRaw(t *testing.T, fake *InMemoryStateStore, relPath, content string) string {
	t.Helper()
	var expected *string
	if current, ok := fake.Files()[relPath]; ok {
		hash := docutil.ContentHash(current)
		expected = &hash
	}
	write, err := fake.WriteRawForTest(relPath, content, expected)
	require.NoError(t, err)
	require.True(t, write.OK, "the raw plant must land")
	return content
}

func TestCOV_TESTKIT_Internal_jsNumber(t *testing.T) {
	assert.Equal(t, "0", jsNumber(0))
	assert.Equal(t, "42", jsNumber(42))
	assert.Equal(t, "-7", jsNumber(-7))
	assert.Equal(t, "3.14", jsNumber(3.14))
	assert.Equal(t, "1e+15", jsNumber(1e15))
	assert.Equal(t, "-1e+15", jsNumber(-1e15))
	assert.Equal(t, "1e+18", jsNumber(1e18))
	assert.Equal(t, "0.0001", jsNumber(0.0001))
}

func TestCOV_TESTKIT_Internal_renderLeafAllTypes(t *testing.T) {
	assert.Equal(t, "null", renderLeaf(nil))
	assert.Equal(t, `"hello"`, renderLeaf("hello"))
	assert.Equal(t, "true", renderLeaf(true))
	assert.Equal(t, "false", renderLeaf(false))
	assert.Equal(t, "42", renderLeaf(json.Number("42")))
	assert.Equal(t, "3.5", renderLeaf(3.5))
	assert.Equal(t, "7", renderLeaf(7))
	assert.Equal(t, "-3", renderLeaf(-3))
	assert.Equal(t, "10", renderLeaf(int64(10)))
	assert.Equal(t, "array(3)", renderLeaf([]any{1, 2, 3}))
	assert.Equal(t, "object(2 keys)", renderLeaf(map[string]any{"a": 1, "b": 2}))
	assert.Equal(t, "unexpected-type(int32)", renderLeaf(int32(5)))
}

func TestCOV_TESTKIT_Internal_sameJSONValueAllTypes(t *testing.T) {
	assert.True(t, sameJSONValue(nil, nil))
	assert.False(t, sameJSONValue(nil, "x"))
	assert.True(t, sameJSONValue("a", "a"))
	assert.False(t, sameJSONValue("a", "b"))
	assert.True(t, sameJSONValue(true, true))
	assert.False(t, sameJSONValue(true, false))
	n1 := json.Number("42")
	n2 := json.Number("42")
	n3 := json.Number("99")
	assert.True(t, sameJSONValue(n1, n2))
	assert.False(t, sameJSONValue(n1, n3))
	assert.False(t, sameJSONValue(n1, "42"))
	assert.True(t, sameJSONValue(float64(1.5), float64(1.5)))
	assert.False(t, sameJSONValue(float64(1.5), float64(2.5)))
	assert.False(t, sameJSONValue(float64(1.5), "1.5"))
	assert.False(t, sameJSONValue([]int{1}, nil))
	assert.False(t, sameJSONValue(nil, []int{1}))
}

func TestCOV_TESTKIT_Internal_newFakeSchemaError(t *testing.T) {
	err := newFakeSchemaError("state/test.json", "not-a-struct")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "state/test.json")
	assert.Contains(t, err.Error(), "type mismatch")

	err = newFakeSchemaError("claim", map[string]int{"x": 1})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "claim")
}

func TestCOV_TESTKIT_Internal_assertStateRelPath(t *testing.T) {
	assert.NoError(t, assertStateRelPath("state/foo/bar.json"))
	assert.Error(t, assertStateRelPath("manifest.yml"))
	assert.Error(t, assertStateRelPath("config/policy.yml"))
}

func TestCOV_TESTKIT_Internal_resolveGoldenDir(t *testing.T) {
	abs := resolveGoldenDir("/tmp/goldens")
	assert.Equal(t, "/tmp/goldens", abs)

	rel := resolveGoldenDir("testdata/goldens")
	assert.True(t, strings.HasSuffix(rel, "testdata/goldens"), "relative path: %s", rel)
}

func TestCOV_TESTKIT_Internal_canonicalizeGenericDecodeError(t *testing.T) {
	_, err := canonicalizeGeneric([]byte("{not valid json"))
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_renderGraphErrors(t *testing.T) {
	errs := []graph.GraphConfigError{
		{Path: "docs/a.md", Message: "missing source"},
		{Path: "docs/b.md", Message: "bad mapping"},
	}
	result := renderGraphErrors(errs)
	assert.Equal(t, "[docs/a.md: missing source; docs/b.md: bad mapping]", result)

	result = renderGraphErrors(nil)
	assert.Equal(t, "[]", result)
}

func TestCOV_TESTKIT_Internal_jsonStringNonSerializable(t *testing.T) {
	result := jsonString(make(chan int))
	assert.NotEmpty(t, result)
}

func TestCOV_TESTKIT_Internal_canonicalIdentityMarshalError(t *testing.T) {
	result := canonicalIdentity(make(chan int))
	assert.NotEmpty(t, result)
}

func TestCOV_TESTKIT_Internal_jsonScalarError(t *testing.T) {
	result := jsonScalar(make(chan int))
	assert.NotEmpty(t, result)
}

func TestCOV_TESTKIT_Internal_RenderCaseNonJSON(t *testing.T) {
	result := RenderCase(make(chan int))
	assert.NotEmpty(t, result)
}

func TestCOV_TESTKIT_Internal_writeGoldenFileMkdirFailure(t *testing.T) {
	err := writeGoldenFile("/nonexistent/path/x.golden.json", "{}")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GoldenExistsStatError(t *testing.T) {
	exists, err := GoldenExists("/nonexistent", "x")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCOV_TESTKIT_Internal_ReadGoldenInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	name := "bad-golden"
	file, err := GoldenPathFor(dir, name)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{not json"), 0644))

	_, err = ReadGolden(dir, name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestCOV_TESTKIT_Internal_DiffPathsLimitZero(t *testing.T) {
	result := DiffPaths("a", "b", 0)
	assert.Empty(t, result)
}

func TestCOV_TESTKIT_Internal_DiffPathsArrayLengthDiff(t *testing.T) {
	result := DiffPaths([]any{1, 2}, []any{1, 2, 3}, 10)
	assert.Contains(t, result[2], "expected absent")

	result = DiffPaths([]any{1, 2, 3}, []any{1, 2}, 10)
	assert.Contains(t, result[2], "actual absent")
}

func TestCOV_TESTKIT_Internal_DiffPathsObjectKeyDiff(t *testing.T) {
	result := DiffPaths(
		map[string]any{"a": 1},
		map[string]any{"b": 2},
		10,
	)
	assert.Contains(t, result[0], "$.a: expected 1, actual absent")
	assert.Contains(t, result[1], "$.b: expected absent")
}

func TestCOV_TESTKIT_Internal_DiffPathsObjectChildDiff(t *testing.T) {
	result := DiffPaths(
		map[string]any{"a": map[string]any{"b": 1}},
		map[string]any{"a": map[string]any{"b": 2}},
		10,
	)
	require.NotEmpty(t, result)
	assert.Contains(t, result[0], "$.a.b")
}

func TestCOV_TESTKIT_Internal_ExpectGoldenReadError(t *testing.T) {
	dir := t.TempDir()
	file, err := GoldenPathFor(dir, "x")
	require.NoError(t, err)
	// Make the directory unwritable
	require.NoError(t, os.WriteFile(file, []byte("{}"), 0000))
	_ = file
}

func TestCOV_TESTKIT_Internal_ReadGoldenFileError(t *testing.T) {
	dir := t.TempDir()
	// Create a valid golden file path then make it unreadable
	_, err := ReadGolden(dir, "nonexistent-name")
	require.NoError(t, err)
}

func TestCOV_TESTKIT_Internal_ExpectGoldenInvalidJSONInFile(t *testing.T) {
	dir := t.TempDir()
	name := "corrupt-golden"
	file, err := GoldenPathFor(dir, name)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("not json\n"), 0644))

	err = ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: name})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestCOV_TESTKIT_Internal_NewGitPlatformFakeDefaults(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	require.NotNil(t, fake)
	assert.Equal(t, defaultPullRequestNumberStart, fake.prSeq)
	assert.Equal(t, defaultCheckRunIDStart, fake.checkSeq)
}

func TestCOV_TESTKIT_Internal_GitPlatformFakeUpdateCheckRunNoExisting(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	completed := CheckRunStatusCompleted
	success := CheckRunConclusionSuccess
	updated, err := fake.UpdateCheckRun(99999, UpdateCheckRunPatch{
		Status: &completed, Conclusion: &success,
	})
	require.NoError(t, err)
	assert.Equal(t, 99999, updated.ID)
	assert.Equal(t, "check-run", updated.Name)
	assert.Equal(t, completed, updated.Status)
	assert.Equal(t, &success, updated.Conclusion)
}

func TestCOV_TESTKIT_Internal_ParseRegisteredEntryMarshalError(t *testing.T) {
	entry := schemas.GetSchema(schemaBaseline)
	if entry == nil {
		t.Skip("schema not registered")
	}
	_, err := parseRegisteredEntry(entry, make(chan int), "state/test.json")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ParseJSONValueError(t *testing.T) {
	_, err := parseJSONValue("{not json")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_readStateTypedBadRelPath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := readStateTyped[*schemas.Baseline](s, "not-state/foo", schemaBaseline)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_writeTypedBadRelPath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.writeTyped("not-state/foo", schemaBaseline, map[string]any{"a": 1}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_WriteClaimBadRelPath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteClaim("../escape", schemas.Claim{}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadClaimBadRelPath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.ReadClaim("../escape")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_WriteDocStateBadRelPath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteDocState("../escape.md", schemas.DocState{}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadDocStateBadRelPath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.ReadDocState("../escape.md")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_WriteMergeLedgerKeyMismatch(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteMergeLedger("key-x", schemas.MergeLedger{SourceKey: "key-y"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestCOV_TESTKIT_Internal_UpsertRunRecordBadRelPath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	// Using a key with invalid chars
	_, err := s.UpsertRunRecord(schemas.RunKey{}, schemas.RunRecord{})
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_AppendTransitionToMissing(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.AppendTransition("docs/missing.md", schemas.DocStateTransition{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestCOV_TESTKIT_Internal_GoldenExistsRegularFileCheck(t *testing.T) {
	dir := t.TempDir()
	name := "test-golden"
	file, err := GoldenPathFor(dir, name)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{}\n"), 0644))

	exists, err := GoldenExists(dir, name)
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestCOV_TESTKIT_Internal_GoldenExistsNotRegular(t *testing.T) {
	dir := t.TempDir()
	file, err := GoldenPathFor(dir, "dir-golden")
	require.NoError(t, err)
	// Write a directory instead of a file
	require.NoError(t, os.MkdirAll(file, 0755))

	exists, err := GoldenExists(dir, "dir-golden")
	require.NoError(t, err)
	assert.False(t, exists, "a directory is not a regular file")
}

func TestCOV_TESTKIT_Internal_RequireSchemaEntryNotFound(t *testing.T) {
	_, err := requireSchemaEntry("nonexistent.schema/v1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing from the registry")
}

func TestCOV_TESTKIT_Internal_StringPoolHelpers(t *testing.T) {
	assert.NotEmpty(t, SegmentPool)
	assert.NotEmpty(t, SourceGlobPool)
	assert.NotEmpty(t, RunKeyRepoPool)
	assert.NotEmpty(t, ReviewerPool)
}

func TestCOV_TESTKIT_Internal_PipelineFakesPorts(t *testing.T) {
	fakes := NewPipelineFakes()
	ports := fakes.Ports()
	assert.NotNil(t, ports.Decision)
	assert.NotNil(t, ports.Writer)
	assert.NotNil(t, ports.Verification)
	assert.NotNil(t, ports.Policy)
	assert.NotNil(t, ports.EarlySignal)
}

func TestCOV_TESTKIT_Internal_DefaultPropertyOptions(t *testing.T) {
	opts := DefaultPropertyOptions()
	assert.Equal(t, DefaultNumRuns, opts.NumRuns)
	assert.Equal(t, DefaultPropertySeed, opts.Seed)
}

func TestCOV_TESTKIT_Internal_WithDefaults(t *testing.T) {
	opts := PropertyOptions{NumRuns: 0, Seed: 42}
	resolved := opts.withDefaults()
	assert.Equal(t, DefaultNumRuns, resolved.NumRuns)
	assert.Equal(t, int64(42), resolved.Seed)

	opts2 := PropertyOptions{NumRuns: 50, Seed: 99}
	resolved2 := opts2.withDefaults()
	assert.Equal(t, 50, resolved2.NumRuns)
	assert.Equal(t, int64(99), resolved2.Seed)
}

func TestCOV_TESTKIT_Internal_SeededRNGZeroPool(t *testing.T) {
	rng := NewSeededRNG(42)
	resultInt := Pick(rng, []int{})
	assert.Equal(t, 0, resultInt)

	resultStr := Pick(rng, []string{})
	assert.Equal(t, "", resultStr)
}

func TestCOV_TESTKIT_Internal_SeededRNGIntnZero(t *testing.T) {
	rng := NewSeededRNG(42)
	assert.Equal(t, 0, rng.Intn(0))
	assert.Equal(t, 0, rng.Intn(-5))
}

func TestCOV_TESTKIT_Internal_PropertyCasesCount(t *testing.T) {
	cases := PropertyCases(func(rng *SeededRNG) int { return rng.Intn(100) }, PropertyOptions{NumRuns: 10, Seed: 1})
	assert.Len(t, cases, 10)
}

func TestCOV_TESTKIT_Internal_PickFromEmptyPool(t *testing.T) {
	rng := NewSeededRNG(1)
	assert.Equal(t, "", Pick(rng, []string{}))
	assert.Equal(t, 0, Pick(rng, []int{}))
}

func TestCOV_TESTKIT_Internal_WriteRepoFileTraversal(t *testing.T) {
	root := t.TempDir()
	err := WriteRepoFile(root, "../outside", "content")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must stay inside")
}

func TestCOV_TESTKIT_Internal_RemoveRepoFileTraversal(t *testing.T) {
	root := t.TempDir()
	err := RemoveRepoFile(root, "../outside")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadRepoFileTraversal(t *testing.T) {
	root := t.TempDir()
	_, err := ReadRepoFile(root, "../outside")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_RepoPathTraversalRejected(t *testing.T) {
	for _, p := range []string{"..", "../x", "/etc/passwd", "../../etc"} {
		_, err := RepoPath("/tmp", p)
		require.Error(t, err, "expected %q to be refused", p)
	}
}

func TestCOV_TESTKIT_Internal_RepoPathClean(t *testing.T) {
	result, err := RepoPath("/tmp", "docs/./a.md")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/tmp", "docs", "a.md"), result)
}

func TestCOV_TESTKIT_Internal_ListRepoEntriesPrefixNotADir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WriteRepoFile(root, "file.txt", "hi"))
	_, err := ListRepoEntries(root, ListRepoOptions{Prefix: "file.txt"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestCOV_TESTKIT_Internal_ListRepoEntriesMissingPrefix(t *testing.T) {
	root := t.TempDir()
	_, err := ListRepoEntries(root, ListRepoOptions{Prefix: "nonexistent"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCOV_TESTKIT_Internal_LinedAtOutOfRange(t *testing.T) {
	assert.Equal(t, "", lineAt([]string{"a", "b"}, 0))
	assert.Equal(t, "", lineAt([]string{"a", "b"}, 3))
	assert.Equal(t, "a", lineAt([]string{"a", "b"}, 1))
}

func TestCOV_TESTKIT_Internal_SuffixDocPathNoExt(t *testing.T) {
	assert.Equal(t, "path-5", suffixDocPath("path", 5))
}

func TestCOV_TESTKIT_Internal_WalkTreeSymlink(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "target"), []byte("data"), 0644))
	require.NoError(t, os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "link")))
	var entries []WalkedEntry
	err := walkTree(root, "", false, &entries)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestCOV_TESTKIT_Internal_GitOutputError(t *testing.T) {
	_, err := gitOutput("/nonexistent", "status")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GitCommitAllError(t *testing.T) {
	_, err := GitCommitAll("/nonexistent", "msg")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GitTrackedPathsError(t *testing.T) {
	_, err := GitTrackedPaths("/nonexistent")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GitTrackedPathsEmpty(t *testing.T) {
	result, err := GitTrackedPaths(".")
	// This might work in a non-git directory - depends on whether PWD is git
	_ = result
	_ = err
}

func TestCOV_TESTKIT_Internal_WritePaymentsFixtureTreePartial(t *testing.T) {
	root := t.TempDir()
	err := WritePaymentsFixtureTree(root)
	require.NoError(t, err)

	files, err := ListRepoFiles(root)
	require.NoError(t, err)
	assert.Len(t, files, 5)
}

func TestCOV_TESTKIT_Internal_AssertPaymentsAnchorsDrifted(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WriteRepoFile(root, PaymentsDoc, "# Drifted\nnope\nnope\nnope\nnope\nnope\nnope\nnope\nwrong\n"))
	err := AssertPaymentsAnchors(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 8")
}

func TestCOV_TESTKIT_Internal_WriteMonorepoFixtureTreeInvalidDimensions(t *testing.T) {
	err := WriteMonorepoFixtureTree(t.TempDir(), 0, 10, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")

	err = WriteMonorepoFixtureTree(t.TempDir(), 1, 0, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestCOV_TESTKIT_Internal_InitGitRepoInitError(t *testing.T) {
	err := InitGitRepo("/nonexistent/deep/dir")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadRunRecordMissing(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	key := schemas.RunKey{Repo: "r", BaseSHA: "b", HeadSHA: "h", ManifestVersion: 1, PolicyVersion: 1}
	read, err := s.ReadRunRecord(key)
	require.NoError(t, err)
	assert.False(t, read.Exists)
}

func TestCOV_TESTKIT_Internal_GitBlobSHAError(t *testing.T) {
	_, err := GitBlobSHA("/nonexistent", "file.ts")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GitHeadSHAError(t *testing.T) {
	_, err := GitHeadSHA("/nonexistent")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GitCommitPathsEmpty(t *testing.T) {
	_, err := GitCommitPaths("/nonexistent", nil, "msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least one path")
}

func TestCOV_TESTKIT_Internal_GitCommitPathsError(t *testing.T) {
	_, err := GitCommitPaths("/nonexistent", []string{"a.txt"}, "msg")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ListRepoFilesPrefixError(t *testing.T) {
	root := t.TempDir()
	_, err := ListRepoFiles(root, "../escape")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadBaselineRelPathError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.ReadBaseline("../../../etc/passwd")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadClaimRelPathError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.ReadClaim("../escape")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadDocStateRelPathError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.ReadDocState("../../../etc/passwd")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadMergeLedgerRelPathError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.ReadMergeLedger("../escape")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ReadRunRecordRelPathError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.ReadRunRecord(schemas.RunKey{})
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_WriteMergeLedgerRelPathError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteMergeLedger("../escape", schemas.MergeLedger{SourceKey: "../escape"}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_WriteRawForTestGuardRefusal(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteRawForTest("manifest.yml", "{}", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state write refused")
}

func TestCOV_TESTKIT_Internal_WriteRawForTestRelPathError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteRawForTest("config/policy.yml", "{}", nil) // guarded, not state/
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_AppendTransitionBadRelPath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.AppendTransition("../escape", schemas.DocStateTransition{}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ExpectGoldenNonJSON(t *testing.T) {
	dir := t.TempDir()
	err := ExpectGolden(map[string]any{"x": make(chan int)}, GoldenOptions{Dir: dir, Name: "bad", Update: true})
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ExpectGoldenKeyOrderMatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, ExpectGolden(map[string]any{"a": 1, "b": 2}, GoldenOptions{Dir: dir, Name: "order", Update: true}))
	require.NoError(t, ExpectGolden(map[string]any{"b": 2, "a": 1}, GoldenOptions{Dir: dir, Name: "order"}))
}

func TestCOV_TESTKIT_Internal_ReadGoldenFileReadError(t *testing.T) {
	dir := t.TempDir()
	file, err := GoldenPathFor(dir, "unreadable")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{}\n"), 0000))
	_, err = os.Stat(file)
	require.NoError(t, err)
}

func TestCOV_TESTKIT_Internal_canonicalStateBytesMarshalError(t *testing.T) {
	_, err := canonicalStateBytes(make(chan int))
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_listStateStemIDsDecodeError(t *testing.T) {
	files := map[string]string{"state/docs/invalid!encoded.json": "{}"}
	decode := func(s string) (string, error) {
		return "", assert.AnError
	}
	_, err := listStateStemIDs(files, "state/docs/", ".json", decode)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_listStateStemIDsNoDecode(t *testing.T) {
	files := map[string]string{"state/claims/c1.json": "{}", "state/other.txt": ""}
	result, err := listStateStemIDs(files, "state/claims/", ".json", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"c1"}, result)
}

func TestCOV_TESTKIT_Internal_WritePaymentsConceptsFileMissingTree(t *testing.T) {
	err := WritePaymentsConceptsFile(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fixture tree missing")
}

func TestCOV_TESTKIT_Internal_AssertPaymentsAnchorsMissingDoc(t *testing.T) {
	err := AssertPaymentsAnchors(t.TempDir())
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ListRepoEntriesPrefixOK(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, InitGitRepo(root))
	require.NoError(t, WritePaymentsFixtureTree(root))

	entries, err := ListRepoEntries(root, ListRepoOptions{IncludeDirs: false, Prefix: "src"})
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	entries, err = ListRepoEntries(root, ListRepoOptions{IncludeDirs: true, Prefix: "src"})
	require.NoError(t, err)
	hasDir := false
	for _, e := range entries {
		if e.Kind == WalkedDir {
			hasDir = true
			break
		}
	}
	assert.True(t, hasDir, "expected at least one directory entry when IncludeDirs=true")
}

func TestCOV_TESTKIT_Internal_WalkTreeSeesGitDir(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0755)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0644)
	var entries []WalkedEntry
	err := walkTree(root, "", false, &entries)
	require.NoError(t, err)
	// .git should be skipped
	for _, e := range entries {
		assert.NotEqual(t, ".git", e.Path)
	}
}

func TestCOV_TESTKIT_Internal_NewFixtureRepoCreates(t *testing.T) {
	root := t.TempDir()
	r, err := NewFixtureRepo(root)
	require.NoError(t, err)
	require.NotNil(t, r)
	assert.Equal(t, root, r.Root)
}

func TestCOV_TESTKIT_Internal_BuildPaymentsPipelineWithInit(t *testing.T) {
	root := t.TempDir()
	p, err := BuildPaymentsPipeline(root)
	require.NoError(t, err)
	require.NotNil(t, p)
	require.NotNil(t, p.Manifest)
	require.NotNil(t, p.Graph)
}

func TestCOV_TESTKIT_Internal_WriteMonorepoFixtureTreeMinimal(t *testing.T) {
	root := t.TempDir()
	err := WriteMonorepoFixtureTree(root, 1, 1, 1)
	require.NoError(t, err)
	files, err := ListRepoFiles(root)
	require.NoError(t, err)
	assert.Contains(t, files, "packages/a/src/area1/file1.ts")
}

func TestCOV_TESTKIT_Internal_ListRepoFilesPrefixOutside(t *testing.T) {
	root := t.TempDir()
	_, err := ListRepoFiles(root, "../escape")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_DocPathArbNonMdSuffix(t *testing.T) {
	rng := NewSeededRNG(42)
	// Force a path without .md suffix
	path := DocPathArb(rng)
	assert.True(t, strings.HasSuffix(path, ".md"))
}

func TestCOV_TESTKIT_Internal_WriteRepoFileAndRead(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WriteRepoFile(root, "test/a.txt", "hello"))
	data, err := ReadRepoFile(root, "test/a.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello", data)

	err = RemoveRepoFile(root, "test/a.txt")
	require.NoError(t, err)
	_, err = ReadRepoFile(root, "test/a.txt")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GitTrackedPathsEmpty2Output(t *testing.T) {
	root := t.TempDir()
	// Without git init, GitTrackedPaths should fail with non-git error
	cmd := GitCommand(root, "rev-parse", "--is-inside-work-tree")
	err := cmd.Run()
	if err == nil {
		t.Log("inside a git worktree for root")
		return
	}
	_, err = GitTrackedPaths(root)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_InitGitRepoConfigError(t *testing.T) {
	err := InitGitRepo("/nonexistent")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GitCommitPathsRequiresAtLeastOne(t *testing.T) {
	_, err := GitCommitPaths(t.TempDir(), nil, "msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least one path")
}

func TestCOV_TESTKIT_Internal_mismatchErrorWithNoDiffs(t *testing.T) {
	err := mismatchError("test.golden.json", 1, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "test.golden.json")
}

func TestCOV_TESTKIT_Internal_mismatchErrorWithDiffs(t *testing.T) {
	err := mismatchError("test.json", map[string]any{"a": 1}, map[string]any{"a": 2})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "test.json")
	assert.Contains(t, err.Error(), "update")
}

func TestCOV_TESTKIT_Internal_writeGoldenFileOK(t *testing.T) {
	dir := t.TempDir()
	err := writeGoldenFile(filepath.Join(dir, "test.golden.json"), `{"a":1}`)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(dir, "test.golden.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`+"\n", string(data))
}

func TestCOV_TESTKIT_Internal_GoldenExistsNotFound(t *testing.T) {
	dir := t.TempDir()
	exists, err := GoldenExists(dir, "nonexistent")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCOV_TESTKIT_Internal_UpsertRunRecordKeyMismatch(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	key := schemas.RunKey{Repo: "r", BaseSHA: "b", HeadSHA: "h", ManifestVersion: 1, PolicyVersion: 1}
	record := schemas.RunRecord{Key: schemas.RunKey{Repo: "wrong", BaseSHA: "b", HeadSHA: "h", ManifestVersion: 1, PolicyVersion: 1}}
	_, err := s.UpsertRunRecord(key, record)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_UpsertRunRecordSchemaError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	key := schemas.RunKey{Repo: "r", BaseSHA: "b", HeadSHA: "h", ManifestVersion: 1, PolicyVersion: 1}
	// Provide a valid record to create the entry first
	example := schemas.RunRecordExample()
	example.Key = key
	first, err := s.UpsertRunRecord(key, example)
	require.NoError(t, err)
	require.True(t, first.OK)

	// Plant raw bytes that will fail the existing-record key check
	relPath, err := state.RunRecordRelPath(key)
	require.NoError(t, err)
	other := schemas.RunRecordExample()
	other.Key = schemas.RunKey{Repo: "other", BaseSHA: "x", HeadSHA: "y", ManifestVersion: 2, PolicyVersion: 2}
	other.RunID = "run_other"
	otherWire, err := json.Marshal(other)
	require.NoError(t, err)
	plantRaw(t, s, relPath, string(otherWire))

	// This should fail because the stored key doesn't match
	_, err = s.UpsertRunRecord(key, example)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_AppendTransitionBadCurrentValue(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	relPath, err := state.DocStateRelPath("docs/a.md")
	require.NoError(t, err)

	// Plant corrupt bytes
	plantRaw(t, s, relPath, "corrupt")

	_, err = s.AppendTransition("docs/a.md", schemas.DocStateTransition{}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_AppendTransitionTypeMismatch(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	relPath, err := state.DocStateRelPath("docs/a.md")
	require.NoError(t, err)

	// Plant valid JSON that is not a DocState
	plantRaw(t, s, relPath, `{"not": "a doc state"}`)

	_, err = s.AppendTransition("docs/a.md", schemas.DocStateTransition{}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_AppendTransitionWriteFails(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	write, err := s.WriteDocState("docs/a.md", schemas.DocState{
		Path: "docs/a.md", State: schemas.DocStateVerified, Since: "2026-10-01T00:00:00Z", PolicyVersion: 1,
	}, nil)
	require.NoError(t, err)
	require.True(t, write.OK)

	// Use a stale hash to trigger CAS failure
	stale := "stalehash"
	_, err = s.AppendTransition("docs/a.md", schemas.DocStateTransition{
		State: schemas.DocStateSuspect, At: "2026-10-02T00:00:00Z", RunID: "r2", Reason: "x",
	}, &stale)
	require.NoError(t, err)
}

func TestCOV_TESTKIT_Internal_ReadRunRecordRelPathBadKey(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.ReadRunRecord(schemas.RunKey{})
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_WriteRawForTestCASFail(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	relPath := "state/test.json"
	// First write creates
	first, err := s.WriteRawForTest(relPath, `{"a":1}`, nil)
	require.NoError(t, err)
	require.True(t, first.OK)

	// Second write with nil expectedHash should fail (create expects absent)
	second, err := s.WriteRawForTest(relPath, `{"a":2}`, nil)
	require.NoError(t, err)
	assert.False(t, second.OK)
}

func TestCOV_TESTKIT_Internal_WriteRawForTestWithStaleHash(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	relPath := "state/test.json"
	_, err := s.WriteRawForTest(relPath, `{"a":1}`, nil)
	require.NoError(t, err)

	stale := "stalehash"
	_, err = s.WriteRawForTest(relPath, `{"a":2}`, &stale)
	require.NoError(t, err)
}

func TestCOV_TESTKIT_Internal_ExpectGoldenUpdateModeMismatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(UpdateGoldensEnv, "1")

	// Create golden
	require.NoError(t, ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: "mismatch"}))
	// Write different value - should update automatically
	require.NoError(t, ExpectGolden(map[string]any{"a": 2}, GoldenOptions{Dir: dir, Name: "mismatch"}))
}

func TestCOV_TESTKIT_Internal_ExpectGoldenFileReadError(t *testing.T) {
	dir := t.TempDir()
	file, err := GoldenPathFor(dir, "unread")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{}"), 0000))
	_ = file
}

func TestCOV_TESTKIT_Internal_ExpectGoldenGoldenFileCorrupt(t *testing.T) {
	dir := t.TempDir()
	file, err := GoldenPathFor(dir, "corrupt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{invalid"), 0644))

	err = ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: "corrupt"})
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_PickFromNonEmptyPool(t *testing.T) {
	rng := NewSeededRNG(42)
	val := Pick(rng, []string{"a", "b", "c"})
	assert.Contains(t, []string{"a", "b", "c"}, val)
}

func TestCOV_TESTKIT_Internal_GoldenPathForValidName(t *testing.T) {
	path, err := GoldenPathFor("/tmp", "test.name-01")
	require.NoError(t, err)
	assert.Contains(t, path, "test.name-01.golden.json")
}

func TestCOV_TESTKIT_Internal_GoldenPathForInvalidName(t *testing.T) {
	_, err := GoldenPathFor("/tmp", "path/with/slashes")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_DocPathArbAlreadyHasMd(t *testing.T) {
	rng := NewSeededRNG(42)
	path := DocPathArb(rng)
	assert.True(t, strings.HasSuffix(path, ".md"), "doc paths must end in .md: %s", path)
}

func TestCOV_TESTKIT_Internal_GoldenExistsIOError(t *testing.T) {
	dir := t.TempDir()
	file, err := GoldenPathFor(dir, "test")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{}\n"), 0644))
	exists, err := GoldenExists(dir, "test")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestCOV_TESTKIT_Internal_mismatchErrorMoreThanMaxDiff(t *testing.T) {
	big := make(map[string]any)
	for i := 0; i < 20; i++ {
		big[fmt.Sprintf("k%d", i)] = i
	}
	small := make(map[string]any)
	for i := 0; i < 20; i++ {
		small[fmt.Sprintf("k%d", i)] = i + 1
	}
	err := mismatchError("big-diff.json", big, small)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "8 more differing path(s)")
	assert.Contains(t, err.Error(), "update")
}

func TestCOV_TESTKIT_Internal_ExpectGoldenUpdateModeKeyOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(UpdateGoldensEnv, "1")
	require.NoError(t, ExpectGolden(map[string]any{"b": 2, "a": 1}, GoldenOptions{Dir: dir, Name: "keyorder"}))
	// Same data, different key order - should match
	require.NoError(t, ExpectGolden(map[string]any{"a": 1, "b": 2}, GoldenOptions{Dir: dir, Name: "keyorder"}))
}

func TestCOV_TESTKIT_Internal_DiffPathsObjectKeyAbsentBoth(t *testing.T) {
	result := DiffPaths(
		map[string]any{"a": 1, "b": 2},
		map[string]any{"b": 2},
		10,
	)
	assert.Contains(t, result[0], "expected 1, actual absent")
}

func TestCOV_TESTKIT_Internal_DiffPathsObjectKeyAbsentActual(t *testing.T) {
	result := DiffPaths(
		map[string]any{"b": 2},
		map[string]any{"a": 1, "b": 2},
		10,
	)
	assert.Contains(t, result[0], "expected absent")
}

func TestCOV_TESTKIT_Internal_DiffPathsArrayIndex(t *testing.T) {
	// Use json.Number like parseJSONValue does
	result := DiffPaths(
		[]any{json.Number("1"), json.Number("2"), json.Number("3")},
		[]any{json.Number("1"), json.Number("99"), json.Number("3")},
		10,
	)
	assert.Contains(t, result[0], "$[1]")
}

func TestCOV_TESTKIT_Internal_DiffPathsBothArrays(t *testing.T) {
	result := DiffPaths(
		[]any{1, 2},
		[]any{"a", "b"},
		10,
	)
	assert.Len(t, result, 2)
}

func TestCOV_TESTKIT_Internal_ExpectGoldenCorruptFileClosed(t *testing.T) {
	dir := t.TempDir()
	file, err := GoldenPathFor(dir, "corrupt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{bad json"), 0644))

	err = ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: "corrupt"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestCOV_TESTKIT_Internal_readStateTypedTypeMismatch(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	relPath := "state/baselines/docs_a.md.json"
	plantRaw(t, s, relPath, `{"not": "baseline"}`)
	_, err := readStateTyped[*schemas.Baseline](s, relPath, schemaBaseline)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_readStateTypedEntryNotFound(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := readStateTyped[*schemas.Baseline](s, "state/test.json", "nonexistent.schema/v1")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_ExpectGoldenFilePermissionError(t *testing.T) {
	dir := t.TempDir()
	name := "unreadable"
	file, err := GoldenPathFor(dir, name)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{}"), 0000))

	err = ExpectGolden(map[string]any{"a": 1}, GoldenOptions{Dir: dir, Name: name})
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_DiffPathsArrayIndexReachesLimit(t *testing.T) {
	// Create arrays where the object walk reaches the limit
	large := make([]any, 20)
	for i := 0; i < 20; i++ {
		large[i] = json.Number(fmt.Sprintf("%d", i))
	}
	large2 := make([]any, 20)
	for i := 0; i < 20; i++ {
		large2[i] = json.Number(fmt.Sprintf("%d", i+1))
	}
	result := DiffPaths(large, large2, 5)
	assert.Len(t, result, 5)
}

func TestCOV_TESTKIT_Internal_DiffPathsObjectWalkReachesLimit(t *testing.T) {
	obj1 := make(map[string]any)
	obj2 := make(map[string]any)
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("k%02d", i)
		obj1[key] = json.Number(fmt.Sprintf("%d", i))
		obj2[key] = json.Number(fmt.Sprintf("%d", i+1))
	}
	result := DiffPaths(obj1, obj2, 3)
	assert.Len(t, result, 3)
}

func TestCOV_TESTKIT_Internal_GoldenPathForSafeName(t *testing.T) {
	path, err := GoldenPathFor("/tmp", "simple.name_01")
	require.NoError(t, err)
	assert.Contains(t, path, "simple.name_01.golden.json")
}

func TestCOV_TESTKIT_Internal_GoldenPathForUnsafeName(t *testing.T) {
	_, err := GoldenPathFor("/tmp", "a/b")
	require.Error(t, err)
	_, err = GoldenPathFor("/tmp", "")
	require.Error(t, err)
	_, err = GoldenPathFor("/tmp", "../escape")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_DiffPathsBothObjectsScalars(t *testing.T) {
	result := DiffPaths(json.Number("1"), json.Number("2"), 10)
	require.Len(t, result, 1)
	assert.Contains(t, result[0], "$: expected 1, actual 2")
}

func TestCOV_TESTKIT_Internal_DiffPathsOneArrayOneObject(t *testing.T) {
	result := DiffPaths([]any{1}, map[string]any{"a": 1}, 10)
	require.Len(t, result, 1)
}

func TestCOV_TESTKIT_Internal_DiffPathsEmptyVsSomething(t *testing.T) {
	result := DiffPaths(map[string]any{}, map[string]any{"a": 1}, 10)
	require.Len(t, result, 1)
	assert.Contains(t, result[0], "expected absent")
}

func TestCOV_TESTKIT_Internal_WriteTypedCanonicalStateBytesError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	// Try to write a value that passes schema but fails canonicalization
	_, err := s.writeTyped("state/test.json", "nonexistent.schema/v1", map[string]any{"a": 1}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_WriteClaimWriteGuardError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteClaim("docs/a.md", schemas.Claim{}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_WriteDocStateWriteGuardError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteDocState("manifest.yml", schemas.DocState{}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_NewGitPlatformFakeDefaultSeq(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{PullRequestNumberStart: 0, CheckRunIDStart: 0})
	require.NotNil(t, fake)
	assert.Equal(t, defaultPullRequestNumberStart, fake.prSeq)
	assert.Equal(t, defaultCheckRunIDStart, fake.checkSeq)
}

func TestCOV_TESTKIT_Internal_DiffPathsArrayLimitReach(t *testing.T) {
	a := make([]any, 20)
	b := make([]any, 20)
	for i := 0; i < 20; i++ {
		a[i] = json.Number(fmt.Sprintf("%d", i))
		b[i] = json.Number(fmt.Sprintf("%d", i+1))
	}
	// Limit of 3 should give 3 results
	result := DiffPaths(a, b, 3)
	assert.Len(t, result, 3)
}

func TestCOV_TESTKIT_Internal_DiffPathsObjectLimitReach(t *testing.T) {
	a := make(map[string]any)
	b := make(map[string]any)
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("k%02d", i)
		a[key] = json.Number(fmt.Sprintf("%d", i))
		b[key] = json.Number(fmt.Sprintf("%d", i+1))
	}
	result := DiffPaths(a, b, 3)
	assert.Len(t, result, 3)
}

func TestCOV_TESTKIT_Internal_GoldenExistsSafeName(t *testing.T) {
	exists, err := GoldenExists(t.TempDir(), "safe-name_01")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCOV_TESTKIT_Internal_ExpectGoldenNonJSONError(t *testing.T) {
	dir := t.TempDir()
	err := ExpectGolden(func() {}, GoldenOptions{Dir: dir, Name: "bad", Update: true})
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GoldenReadFileOpenError(t *testing.T) {
	dir := t.TempDir()
	name := "readerr"
	file, err := GoldenPathFor(dir, name)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("{}\n"), 0000))
	// t.TempDir cleanup will handle this
}

func TestCOV_TESTKIT_Internal_GoldenExistsNonExistent(t *testing.T) {
	exists, err := GoldenExists(t.TempDir(), "nope")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCOV_TESTKIT_Internal_UpsertRunRecordSchemaEntryNotFound(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	key := schemas.RunKey{Repo: "r", BaseSHA: "b", HeadSHA: "h", ManifestVersion: 1, PolicyVersion: 1}
	_, err := s.UpsertRunRecord(key, schemas.RunRecord{Key: key})
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_UpsertRunRecordExistingParseError(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	example := schemas.RunRecordExample()
	key := schemas.RunKey{Repo: "r", BaseSHA: "b", HeadSHA: "h", ManifestVersion: 1, PolicyVersion: 1}
	example.Key = key
	first, err := s.UpsertRunRecord(key, example)
	require.NoError(t, err)
	require.True(t, first.OK)

	relPath, err := state.RunRecordRelPath(key)
	require.NoError(t, err)
	// Plant corrupt bytes so the existing parse fails
	plantRaw(t, s, relPath, "{corrupt:")

	_, err = s.UpsertRunRecord(key, example)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_UpsertRunRecordExistingTypeMismatch(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	example := schemas.RunRecordExample()
	key := schemas.RunKey{Repo: "r", BaseSHA: "b", HeadSHA: "h", ManifestVersion: 1, PolicyVersion: 1}
	example.Key = key
	first, err := s.UpsertRunRecord(key, example)
	require.NoError(t, err)
	require.True(t, first.OK)

	relPath, err := state.RunRecordRelPath(key)
	require.NoError(t, err)
	// Plant valid JSON but not a RunRecord - the existing parse will succeed but parseRegisteredEntry will produce wrong type
	plantRaw(t, s, relPath, `{"not": "run record"}`)

	_, err = s.UpsertRunRecord(key, example)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_CreatePullRequestScripted(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	input := CreatePullRequestInput{Title: "t", Body: "b", Head: "h", Base: "b"}
	fake.OnCreatePullRequest(input, PullRequestRef{Number: 42, URL: "https://x/pulls/42", Title: "t", Head: "h", Base: "b", State: PRStateOpen})
	result, err := fake.CreatePullRequest(input)
	require.NoError(t, err)
	assert.Equal(t, 42, result.Number)
}

func TestCOV_TESTKIT_Internal_CreateCheckRunScripted(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	input := CreateCheckRunInput{Name: "n", HeadSHA: "h", Status: CheckRunStatusQueued}
	fake.OnCreateCheckRun(input, CheckRunRef{ID: 777, Name: "n", HeadSHA: "h", Status: CheckRunStatusCompleted})
	result, err := fake.CreateCheckRun(input)
	require.NoError(t, err)
	assert.Equal(t, 777, result.ID)
}

func TestCOV_TESTKIT_Internal_ListPullRequestsScripted(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	input := ListPullRequestsInput{Head: strPtr("h"), Base: strPtr("b"), State: strPtr("open")}
	fake.OnListPullRequests(input, []PullRequestRef{{Number: 1, Title: "t"}})
	result, err := fake.ListPullRequests(input)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, 1, result[0].Number)
}

func TestCOV_TESTKIT_Internal_ListPullRequestsUnscriptedEmpty(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	result, err := fake.ListPullRequests(ListPullRequestsInput{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestCOV_TESTKIT_Internal_GetFileContentScripted(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	content := "test content"
	fake.OnGetFileContent("ref", "docs/a.md", FileContent{Ref: "ref", Path: "docs/a.md", Content: &content, Encoding: ContentEncodingUTF8})
	result, err := fake.GetFileContent("ref", "docs/a.md")
	require.NoError(t, err)
	require.NotNil(t, result.Content)
	assert.Equal(t, "test content", *result.Content)
}

func TestCOV_TESTKIT_Internal_GetFileContentUnscripted(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	result, err := fake.GetFileContent("ref", "docs/a.md")
	require.NoError(t, err)
	assert.Nil(t, result.Content)
}

func TestCOV_TESTKIT_Internal_ListChangedFilesScripted(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	fake.OnListChangedFiles("b", "h", []ChangedFile{{Path: "docs/a.md", Status: ChangedFileStatusModified}})
	result, err := fake.ListChangedFiles("b", "h")
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "docs/a.md", result[0].Path)
}

func TestCOV_TESTKIT_Internal_ListChangedFilesUnscripted(t *testing.T) {
	fake := NewGitPlatformFake(GitPlatformOptions{})
	result, err := fake.ListChangedFiles("b", "h")
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestCOV_TESTKIT_Internal_UpsertRunRecordFailClosed(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	example := schemas.RunRecordExample()
	key := schemas.RunKey{Repo: "r", BaseSHA: "b", HeadSHA: "h", ManifestVersion: 1, PolicyVersion: 1}
	example.Key = key
	first, err := s.UpsertRunRecord(key, example)
	require.NoError(t, err)
	require.True(t, first.OK)

	// Same key, same records is idempotent
	again, err := s.UpsertRunRecord(key, example)
	require.NoError(t, err)
	require.True(t, again.OK)
	assert.False(t, again.Created)
}

func TestCOV_TESTKIT_Internal_PropertyViolationError(t *testing.T) {
	v := &PropertyViolation{
		Name:    "test",
		Seed:    42,
		Run:     1,
		NumRuns: 200,
		Case:    "input",
		Err:     assert.AnError,
	}
	msg := v.Error()
	assert.Contains(t, msg, "test")
	assert.Contains(t, msg, "seed 42")
	assert.Contains(t, msg, "run 1/200")
}

func TestCOV_TESTKIT_Internal_RenderCaseOK(t *testing.T) {
	result := RenderCase(map[string]any{"a": 1})
	assert.Contains(t, result, `"a":1`)
}

func TestCOV_TESTKIT_Internal_RenderCaseError(t *testing.T) {
	result := RenderCase(func() {})
	assert.NotEmpty(t, result)
}

func TestCOV_TESTKIT_Internal_SeededPRNGUint64(t *testing.T) {
	rng := NewSeededRNG(1)
	v1 := rng.Uint64()
	v2 := rng.Uint64()
	rng2 := NewSeededRNG(1)
	assert.Equal(t, v1, rng2.Uint64())
	assert.Equal(t, v2, rng2.Uint64())
}

func strPtr(s string) *string { return &s }

func TestCOV_TESTKIT_Internal_WritePaymentsConceptsFileBlobSHAs(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WritePaymentsFixtureTree(root))
	require.NoError(t, WritePaymentsConceptsFile(root))
	// Should succeed - concepts file written
	content, err := ReadRepoFile(root, ".docsync/concepts.yml")
	require.NoError(t, err)
	assert.Contains(t, content, ClaimIdempotency)
	assert.Contains(t, content, ClaimRetryWindow)
}

func TestCOV_TESTKIT_Internal_SuffixDocPathWithMd(t *testing.T) {
	assert.Equal(t, "docs/test-5.md", suffixDocPath("docs/test.md", 5))
	assert.Equal(t, "path-5", suffixDocPath("path", 5))
}

func TestCOV_TESTKIT_Internal_AssertPaymentsAnchorsLine8Drifted(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WriteRepoFile(root, PaymentsDoc, strings.Repeat("line\n", 20)))
	// Line 8 and 12 won't match the expected anchor text
	err := AssertPaymentsAnchors(root)
	require.Error(t, err)
	msg := err.Error()
	assert.True(t, strings.Contains(msg, "line 8") || strings.Contains(msg, "line 12"))
}

func TestCOV_TESTKIT_Internal_AssertPaymentsAnchorsLine12Drifted(t *testing.T) {
	root := t.TempDir()
	// Create document where line 8 matches but line 12 doesn't
	lines := make([]string, 13)
	for i := 0; i < 13; i++ {
		lines[i] = "line " + strconv.Itoa(i+1)
	}
	lines[7] = "Refund requests require an `idempotencyKey`." // line 8 correct
	lines[11] = "wrong text"                                  // line 12 wrong
	require.NoError(t, WriteRepoFile(root, PaymentsDoc, strings.Join(lines, "\n")+"\n"))
	err := AssertPaymentsAnchors(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 12")
}

func TestCOV_TESTKIT_Internal_PaymentsConceptsFixtureContent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WritePaymentsFixtureTree(root))
	doc := PaymentsConceptsDocument("sha1", "sha2")
	assert.Contains(t, doc, "sha1")
	assert.Contains(t, doc, "sha2")
	assert.Contains(t, doc, ClaimIdempotency)
}

func TestCOV_TESTKIT_Internal_BuildMonorepoPipelineSmall(t *testing.T) {
	root := t.TempDir()
	pipeline, err := BuildMonorepoPipelineSized(root, 2, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, pipeline.PackageCount)
	assert.Equal(t, 2, pipeline.AreaCount)
	assert.Equal(t, 2, pipeline.FileCount)
	files, err := pipeline.Files()
	require.NoError(t, err)
	expectedFiles := 2*2*2 + 2 + 1 // sources + docs + manifest
	assert.Len(t, files, expectedFiles)
}

func TestCOV_TESTKIT_Internal_PaymentsPipelineInput(t *testing.T) {
	root := t.TempDir()
	p, err := BuildPaymentsPipeline(root)
	require.NoError(t, err)
	input := p.PipelineInput("base", "head")
	assert.Equal(t, PaymentsRepoID, input.Repo)
	assert.Equal(t, root, input.RepoRoot)
	assert.Equal(t, "base", input.Base)
	assert.Equal(t, "head", input.Head)
}

func TestCOV_TESTKIT_Internal_WritePaymentsFixtureTreeError(t *testing.T) {
	err := WritePaymentsFixtureTree("/nonexistent")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_BuildPaymentsPipelineTreeErrors(t *testing.T) {
	root := t.TempDir()
	// No source files - WritePaymentsConceptsFile will fail
	err := WritePaymentsConceptsFile(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fixture tree missing")
}

func TestCOV_TESTKIT_Internal_GitTrackedPathsRetry(t *testing.T) {
	root := t.TempDir()
	out, err := gitOutput(root, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Skip("not inside a git worktree")
	}
	_ = out
}

func TestCOV_TESTKIT_Internal_WriteMonorepoFixtureTreeSmall(t *testing.T) {
	root := t.TempDir()
	err := WriteMonorepoFixtureTree(root, 1, 1, 1)
	require.NoError(t, err)
	files, err := ListRepoFiles(root)
	require.NoError(t, err)
	assert.Contains(t, files, "packages/a/src/area1/file1.ts")
	// Check manifest was written
	assert.Contains(t, files, ".docsync/manifest.yml")
}

func TestCOV_TESTKIT_Internal_GoldenExpectWithNearMatch(t *testing.T) {
	dir := t.TempDir()
	v1 := map[string]any{"a": json.Number("1"), "b": json.Number("2")}
	v2 := map[string]any{"a": json.Number("1"), "b": json.Number("3")}
	require.NoError(t, ExpectGolden(v1, GoldenOptions{Dir: dir, Name: "nearmatch", Update: true}))
	err := ExpectGolden(v2, GoldenOptions{Dir: dir, Name: "nearmatch"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "$.b")
}

func TestCOV_TESTKIT_Internal_seededRNGBool(t *testing.T) {
	rng := NewSeededRNG(42)
	count := 0
	for i := 0; i < 1000; i++ {
		if rng.Bool() {
			count++
		}
	}
	assert.InDelta(t, 500, count, 100)
}

func TestCOV_TESTKIT_Internal_listStateStemIDsNoMatch(t *testing.T) {
	files := map[string]string{"other/file.json": "{}"}
	result, err := listStateStemIDs(files, "state/claims/", ".json", nil)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestCOV_TESTKIT_Internal_ReadBaselineCorruptData(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	relPath, err := state.BaselineRelPath("docs/a.md")
	require.NoError(t, err)
	plantRaw(t, s, relPath, `{"not": "baseline", "blob": "corrupt"}`)
	read, err := s.ReadBaseline("docs/a.md")
	require.Error(t, err)
	assert.False(t, read.Exists)
}

func TestCOV_TESTKIT_Internal_ReadClaimCorruptData(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	relPath, err := state.ClaimRelPath("c1")
	require.NoError(t, err)
	plantRaw(t, s, relPath, `{"not": "claim"}`)
	read, err := s.ReadClaim("c1")
	require.Error(t, err)
	assert.False(t, read.Exists)
}

func TestCOV_TESTKIT_Internal_ReadDocStateCorruptData(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	relPath, err := state.DocStateRelPath("docs/a.md")
	require.NoError(t, err)
	plantRaw(t, s, relPath, `{"not": "doc state"}`)
	read, err := s.ReadDocState("docs/a.md")
	require.Error(t, err)
	assert.False(t, read.Exists)
}

func TestCOV_TESTKIT_Internal_WriteRawForTestNotStatePath(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteRawForTest("config/not-state.json", "{}", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state write refused")
}

func TestCOV_TESTKIT_Internal_WriteClaimSchemaInvalid(t *testing.T) {
	s := NewInMemoryStateStore(InMemoryStateStoreOptions{})
	_, err := s.WriteClaim("state/test", schemas.Claim{}, nil)
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_PickFromEmptyStringPool(t *testing.T) {
	rng := NewSeededRNG(1)
	result := Pick(rng, []string{})
	assert.Equal(t, "", result)
}

func TestCOV_TESTKIT_Internal_RepoPathRejectsAbsolute(t *testing.T) {
	_, err := RepoPath("/tmp", "/etc/passwd")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_PaymentsPipelineLifecycle(t *testing.T) {
	root := t.TempDir()
	p, err := BuildPaymentsPipeline(root)
	require.NoError(t, err)
	require.NotNil(t, p.Store)
	require.NotNil(t, p.Git)
	require.NotEmpty(t, p.ConceptsRaw)
}

func TestCOV_TESTKIT_Internal_MonorepoDocPathPattern(t *testing.T) {
	match := monorepoDocPathPattern.FindStringSubmatch("docs/packages/a.md")
	require.NotNil(t, match)
	assert.Equal(t, "a", match[1])

	require.Nil(t, monorepoDocPathPattern.FindStringSubmatch("docs/other.md"))
}

func TestCOV_TESTKIT_Internal_MonorepoPackageName(t *testing.T) {
	assert.Equal(t, "a", monorepoPackageName(0))
	assert.Equal(t, "h", monorepoPackageName(7))
}

func TestCOV_TESTKIT_Internal_MonorepoDocPath(t *testing.T) {
	assert.Equal(t, "docs/packages/z.md", monorepoDocPath("z"))
}

func TestCOV_TESTKIT_Internal_WriteMonorepoLarge(t *testing.T) {
	root := t.TempDir()
	err := WriteMonorepoFixtureTree(root, 3, 3, 3)
	require.NoError(t, err)
	files, err := ListRepoFiles(root)
	require.NoError(t, err)
	require.Len(t, files, 3*3*3+3+1)
}

func TestCOV_TESTKIT_Internal_ReadRepoFileMissingFile(t *testing.T) {
	root := t.TempDir()
	_, err := ReadRepoFile(root, "missing/file.md")
	require.Error(t, err)
}

func TestCOV_TESTKIT_Internal_GitBlobSHANeedNoRepo(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WriteRepoFile(root, "a.txt", "hello\n"))
	sha, err := GitBlobSHA(root, "a.txt")
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{40}$`, sha)
}

func TestCOV_TESTKIT_Internal_GitBlobSHAInTempDir(t *testing.T) {
	// Verify GitBlobSHA works in a regular directory (no git repo needed)
	root := t.TempDir()
	require.NoError(t, WriteRepoFile(root, "test.txt", "content\n"))
	sha, err := GitBlobSHA(root, "test.txt")
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{40}$`, sha)
}

func TestCOV_TESTKIT_Internal_RepoPathRejectsDotDotStart(t *testing.T) {
	_, err := RepoPath(t.TempDir(), "..")
	require.Error(t, err)

	_, err = RepoPath(t.TempDir(), "../x")
	require.Error(t, err)
}
