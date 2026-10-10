package state_test

// Black-box port of packages/core/test/state.test.ts and
// state.property.test.ts: the state-store primitives (P0-T12/T14/T15/T16) —
// CAS conflict semantics, doc-path filename mapping (incl. the a__b.md →
// a_u_ub.md collision case), run-record filename injectivity, queue
// serialization, churn counters, the V-P0-13 write boundary and the V-P0-14
// simulated run. Deterministic: seeded generators stand in for fast-check and
// every test uses its own t.TempDir() state dir.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------ golden data */

// The TS suite pulled its examples from @docsync/schemas; the Go registry is
// the same single source of truth, so the golden literals are read from it
// (never redefined here) and asserted byte-for-byte through the Go
// serializer.
func registryExample[T any](id string) T {
	entry := schemas.GetSchema(id)
	if entry == nil {
		panic("schemas: registry entry " + id + " is missing")
	}
	value, ok := entry.Example.(T)
	if !ok {
		panic(fmt.Sprintf("schemas: registry example %q has type %T, want %T", id, entry.Example, *new(T)))
	}
	return value
}

var (
	baselineExample    = registryExample[schemas.Baseline]("docsync.baseline")
	claimExample       = registryExample[schemas.Claim]("docsync.claim")
	docStateExample    = registryExample[schemas.DocState]("docsync.doc-state")
	auditCursorExample = registryExample[schemas.AuditCursor]("docsync.audit-cursor")
)

var runRecordExample = schemas.RunRecordExample()

var runKey = runRecordExample.Key

/* ---------------------------------------------------------------- helpers */

func strptr(s string) *string { return &s }

func makeStateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".docsync", "state")
}

func newStore(t *testing.T, churn state.StateChurnHook) (*state.StateStore, string) {
	t.Helper()
	stateDir := makeStateDir(t)
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir, Churn: churn})
	require.NoError(t, err)
	return store, stateDir
}

// canonicalJSON is what the store must persist: canonical serialization of a
// value through the same generic-form path the CAS bytes take.
func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	wire, err := json.Marshal(value)
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(wire))
	dec.UseNumber()
	var generic any
	require.NoError(t, dec.Decode(&generic))
	out, err := docutil.CanonicalJSON(generic)
	require.NoError(t, err)
	return out
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func requireStateKeyError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, state.CodeStateKey, docutil.CodeOf(err))
}

func requireStateMissingError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, state.CodeStateMissing, docutil.CodeOf(err))
}

func requireGuardedPath(t *testing.T, err error, relPath string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, state.CodeBoundary, docutil.CodeOf(err))
	var guarded *state.GuardedPathError
	require.True(t, errors.As(err, &guarded))
	require.Equal(t, relPath, guarded.RelPath)
}

func requireSchemaDrift(t *testing.T, err error, summaryContains string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, state.CodeStateSchema, docutil.CodeOf(err))
	var schemaErr *state.StateSchemaError
	require.True(t, errors.As(err, &schemaErr))
	require.Contains(t, schemaErr.Summary, summaryContains)
}

/* ------------------------------------------------------- canonical + hash */

func TestContentHashCoversTinyValuesDeterministically(t *testing.T) {
	require.Equal(t,
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		docutil.ContentHash(""))
	require.Equal(t,
		"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		docutil.ContentHash("abc"))
	require.Equal(t, docutil.ContentHash("state-bytes"), docutil.ContentHash("state-bytes"))
	require.NotEqual(t, docutil.ContentHash("state-bytes"), docutil.ContentHash("state-bytez"))
	require.Equal(t, docutil.ContentHash("state-bytes"), docutil.ContentHashBytes([]byte("state-bytes")))
}

/* ------------------------------------------------ doc-path mapping (D1) */

func TestDocPathFilenameMappingFlattensWithDoubleUnderscore(t *testing.T) {
	encoded, err := state.EncodeStateDocPath("docs/payments.md")
	require.NoError(t, err)
	require.Equal(t, "docs__payments.md", encoded)
	decoded, err := state.DecodeStateDocPath("docs__payments.md")
	require.NoError(t, err)
	require.Equal(t, "docs/payments.md", decoded)
}

func TestDocPathRoundTripsWithoutUnderscoreCollisions(t *testing.T) {
	for _, docPath := range []string{
		"a__b.md",
		"my_file.md",
		"docs/arch/payments.md",
		"README.md",
		"a/b__c/d.md",
		"_leading.md",
		"trailing_.md",
	} {
		encoded, err := state.EncodeStateDocPath(docPath)
		require.NoError(t, err, docPath)
		require.NotContains(t, encoded, "/", docPath)
		decoded, err := state.DecodeStateDocPath(encoded)
		require.NoError(t, err, docPath)
		require.Equal(t, docPath, decoded, docPath)
	}

	// `my_file.md` encodes to `my_ufile.md`, NOT the stem a `__`-separated
	// path would land on — the escape keeps the two apart.
	encoded, err := state.EncodeStateDocPath("my_file.md")
	require.NoError(t, err)
	require.Equal(t, "my_ufile.md", encoded)
	encoded, err = state.EncodeStateDocPath("docs/__2__.md")
	require.NoError(t, err)
	require.Equal(t, "docs___u_u2_u_u.md", encoded)
	decoded, err := state.DecodeStateDocPath("docs___u_u2_u_u.md")
	require.NoError(t, err)
	require.Equal(t, "docs/__2__.md", decoded)
}

func TestUnderscoreAndSlashStemsDoNotCollide(t *testing.T) {
	withUnderscore, err := state.EncodeStateDocPath("a__b.md")
	require.NoError(t, err)
	require.Equal(t, "a_u_ub.md", withUnderscore)
	withSlash, err := state.EncodeStateDocPath("a/b.md")
	require.NoError(t, err)
	require.Equal(t, "a__b.md", withSlash)
	require.NotEqual(t, withUnderscore, withSlash)
}

func TestDecodeStateDocPathRefusesUndecodableStems(t *testing.T) {
	// `a_x` — an unknown escape — and `a_` — a dangling `_` — must NOT decode.
	for _, encoded := range []string{"a_x", "a_"} {
		_, err := state.DecodeStateDocPath(encoded)
		requireStateKeyError(t, err)
	}
	_, err := state.DecodeStateSegment("a_")
	requireStateKeyError(t, err)
}

func TestEncodeStateDocPathRefusesNonEncodablePaths(t *testing.T) {
	for _, bad := range []string{"", "/abs/path.md", "../etc/passwd", "docs//x.md", "a b.md", `back\slash.md`} {
		_, err := state.EncodeStateDocPath(bad)
		requireStateKeyError(t, err)
	}
	_, err := state.EncodeStateSegment("")
	requireStateKeyError(t, err)
	_, err = state.EncodeStateSegment("a/b")
	requireStateKeyError(t, err)
}

func TestStateErrorMessagesArePreserved(t *testing.T) {
	_, err := state.EncodeStateSegment("a/b")
	require.Error(t, err)
	require.Equal(t,
		`EDOCSYNC_STATE_KEY: path segment "a/b" is outside [A-Za-z0-9._-] — not encodable`,
		err.Error())

	_, err = state.EncodeStateSegment("")
	require.Error(t, err)
	require.Equal(t, "EDOCSYNC_STATE_KEY: path segment is empty", err.Error())

	_, err = state.DecodeStateDocPath("a_")
	require.Error(t, err)
	require.Equal(t,
		`EDOCSYNC_STATE_KEY: encoded path segment "a_" has a malformed '_' escape at index 1`,
		err.Error())

	_, err = state.EncodeStateDocPath("../etc/passwd")
	require.Error(t, err)
	require.Equal(t,
		`EDOCSYNC_STATE_KEY: doc path "../etc/passwd" contains '..' traversal`,
		err.Error())

	_, err = state.ClaimRelPath("bad/id")
	require.Error(t, err)
	require.Equal(t,
		`EDOCSYNC_STATE_KEY: claim id "bad/id" is not filename-safe ([A-Za-z0-9._-], no '..')`,
		err.Error())

	_, err = state.ClaimRelPath("")
	require.Error(t, err)
	require.Equal(t, "EDOCSYNC_STATE_KEY: claim id is empty", err.Error())

	_, err = state.RunRecordFilename(runKeyWithRepo("acme#x"))
	require.Error(t, err)
	require.Equal(t,
		`EDOCSYNC_STATE_KEY: run key repo "acme#x" is not filename-safe ([A-Za-z0-9._-] segments, no '#'); refusing (fail closed)`,
		err.Error())
}

func TestRenderIssuesRendersZodStyleSummary(t *testing.T) {
	require.Equal(t,
		"policy_version: Number must be greater than 0; key.base_sha: Required",
		state.RenderIssues([]schemas.Issue{
			{Path: []string{"policy_version"}, Message: "Number must be greater than 0"},
			{Path: []string{"key", "base_sha"}, Message: "Required"},
		}))
	require.Equal(t, "<root>: boom", state.RenderIssues([]schemas.Issue{{Message: "boom"}}))
	require.Equal(t, "", state.RenderIssues(nil))
}

/* --------------------------------------------- run-record filename + key */

func runKeyWithRepo(repo string) schemas.RunKey {
	key := runKey
	key.Repo = repo
	return key
}

func TestRunRecordFilenameRendersRepoBaseHead(t *testing.T) {
	filename, err := state.RunRecordFilename(runKey)
	require.NoError(t, err)
	require.Equal(t, "acme__payments#8a73f2c..81af92e#m1#p17.json", filename)

	otherManifest := runKey
	otherManifest.ManifestVersion = 2
	filename2, err := state.RunRecordFilename(otherManifest)
	require.NoError(t, err)
	require.NotEqual(t, filename, filename2)

	otherPolicy := runKey
	otherPolicy.PolicyVersion = 18
	filename3, err := state.RunRecordFilename(otherPolicy)
	require.NoError(t, err)
	require.NotEqual(t, filename, filename3)

	otherHead := runKey
	otherHead.HeadSHA = "81af93e"
	filename4, err := state.RunRecordFilename(otherHead)
	require.NoError(t, err)
	require.NotEqual(t, filename, filename4)
}

func TestRunRecordFilenameRefusesHashInStructuralFields(t *testing.T) {
	_, err := state.RunRecordFilename(runKeyWithRepo("acme#x"))
	requireStateKeyError(t, err)
	key := runKey
	key.BaseSHA = "8a#3f2c"
	_, err = state.RunRecordFilename(key)
	requireStateKeyError(t, err)
	key = runKey
	key.HeadSHA = ""
	_, err = state.RunRecordFilename(key)
	requireStateKeyError(t, err)
	key = runKey
	key.Repo = ""
	_, err = state.RunRecordFilename(key)
	requireStateKeyError(t, err)
}

func TestRunRecordFilenameRepoMappingIsInjectable(t *testing.T) {
	byRepo := map[string]string{
		"acme/payments": "acme__payments",
		"acme__corp":    "acme_u_ucorp",
		"a/b/c":         "a__b__c",
		"a_b":           "a_ub",
	}
	for repo, wantRepoPart := range byRepo {
		filename, err := state.RunRecordFilename(runKeyWithRepo(repo))
		require.NoError(t, err, repo)
		require.Equal(t, wantRepoPart+"#8a73f2c..81af92e#m1#p17.json", filename, repo)
	}
	distinct := map[string]bool{}
	for repo := range byRepo {
		filename, err := state.RunRecordFilename(runKeyWithRepo(repo))
		require.NoError(t, err)
		require.False(t, distinct[filename], "distinct repos must not collide: %s", repo)
		distinct[filename] = true
	}
}

func TestDecodeRunRecordRepoInvertsFilenameRepoComponent(t *testing.T) {
	for _, repo := range []string{"acme/payments", "acme__corp", "a/b/c", "a_b", "my_repo/core", "org/sub_dir/x"} {
		filename, err := state.RunRecordFilename(runKeyWithRepo(repo))
		require.NoError(t, err, repo)
		repoPart := filename[:strings.Index(filename, "#")]
		decoded, err := state.DecodeRunRecordRepo(repoPart)
		require.NoError(t, err, repo)
		require.Equal(t, repo, decoded, repo)
	}
}

func TestRunRecordFilenameRefusesUnsafeRepoSegments(t *testing.T) {
	for _, repo := range []string{"acme//payments", "/acme", "acme/", "acme/pay ments"} {
		_, err := state.RunRecordFilename(runKeyWithRepo(repo))
		requireStateKeyError(t, err)
	}
}

func TestSameRunKeyIsStrictFiveTupleIdentity(t *testing.T) {
	require.True(t, state.SameRunKey(runKey, runRecordExample.Key))

	cases := []func(k *schemas.RunKey){
		func(k *schemas.RunKey) { k.Repo = "acme/other" },
		func(k *schemas.RunKey) { k.BaseSHA = "8a73f2d" },
		func(k *schemas.RunKey) { k.HeadSHA = "81af93e" },
		func(k *schemas.RunKey) { k.ManifestVersion = 2 },
		func(k *schemas.RunKey) { k.PolicyVersion = 18 },
	}
	for i, mutate := range cases {
		other := runKey
		mutate(&other)
		require.False(t, state.SameRunKey(runKey, other), "case %d", i)
		filenameA, err := state.RunRecordFilename(runKey)
		require.NoError(t, err)
		filenameB, err := state.RunRecordFilename(other)
		require.NoError(t, err)
		require.NotEqual(t, filenameA, filenameB, "case %d", i)
	}
}

/* ----------------------------------------------------------- CAS primitives */

func TestReadStateFileENOENTIsDeterministicEmpty(t *testing.T) {
	stateDir := makeStateDir(t)
	read, err := state.ReadStateFile[any](filepath.Join(stateDir, "baselines", "missing.json"))
	require.NoError(t, err)
	require.False(t, read.Exists)
	require.Nil(t, read.Content)
	require.Empty(t, read.Raw)
	require.Empty(t, read.Hash)
}

func TestCasWriteCreatesUpdatesAndRefusesStaleHashes(t *testing.T) {
	stateDir := makeStateDir(t)
	file := filepath.Join(stateDir, "cas.json")

	created, err := state.CasWriteFile(file, "v1", nil)
	require.NoError(t, err)
	require.True(t, created.OK)
	require.Equal(t, docutil.ContentHash("v1"), created.Hash)
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "v1", string(raw))

	// Create over an existing file with no expectation ⇒ conflict, no write.
	createOverExisting, err := state.CasWriteFile(file, "v2", nil)
	require.NoError(t, err)
	require.False(t, createOverExisting.OK)
	require.Nil(t, createOverExisting.ExpectedHash)
	require.NotNil(t, createOverExisting.ActualHash)
	require.Equal(t, docutil.ContentHash("v1"), *createOverExisting.ActualHash)
	raw, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "v1", string(raw))

	// Stale hash ⇒ conflict, no write.
	stale, err := state.CasWriteFile(file, "v3", strptr("stale-hash"))
	require.NoError(t, err)
	require.False(t, stale.OK)
	require.Equal(t, "stale-hash", *stale.ExpectedHash)
	require.Equal(t, docutil.ContentHash("v1"), *stale.ActualHash)
	raw, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "v1", string(raw))

	// Expected-to-exist but absent ⇒ conflict with a nil actual.
	missingButExpected, err := state.CasWriteFile(filepath.Join(stateDir, "absent.json"), "v1", strptr("some-hash"))
	require.NoError(t, err)
	require.False(t, missingButExpected.OK)
	require.Equal(t, "some-hash", *missingButExpected.ExpectedHash)
	require.Nil(t, missingButExpected.ActualHash)
	_, statErr := os.Stat(filepath.Join(stateDir, "absent.json"))
	require.True(t, errors.Is(statErr, fs.ErrNotExist))

	// Matching hash ⇒ update.
	updated, err := state.CasWriteFile(file, "v2", strptr(docutil.ContentHash("v1")))
	require.NoError(t, err)
	require.True(t, updated.OK)
	require.Equal(t, docutil.ContentHash("v2"), updated.Hash)
	raw, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "v2", string(raw))
}

func TestCasWriteDetailedReportsCreatedFlag(t *testing.T) {
	stateDir := makeStateDir(t)
	file := filepath.Join(stateDir, "detailed.json")

	created, err := state.CasWriteFileDetailed(file, "v1", nil)
	require.NoError(t, err)
	require.True(t, created.OK)
	require.True(t, created.Created)

	updated, err := state.CasWriteFileDetailed(file, "v2", strptr(docutil.ContentHash("v1")))
	require.NoError(t, err)
	require.True(t, updated.OK)
	require.False(t, updated.Created)

	conflict, err := state.CasWriteFileDetailed(file, "v3", strptr("stale"))
	require.NoError(t, err)
	require.False(t, conflict.OK)
	require.Equal(t, "stale", *conflict.ExpectedHash)
	require.Equal(t, docutil.ContentHash("v2"), *conflict.ActualHash)
}

func TestReadStateFileThrowsNonENOENTErrorsAndThereIsNoDeleteAPI(t *testing.T) {
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	// Reading a directory is not ENOENT: a hard error, fail closed (never the
	// empty-read outcome).
	_, err := state.ReadStateFile[any](stateDir)
	require.Error(t, err)
	require.NotEqual(t, state.CodeStateCorrupt, docutil.CodeOf(err))

	// The store exposes no delete/remove/unlink method at all (absence IS
	// the contract).
	storeType := reflect.TypeOf((*state.StateStore)(nil))
	for i := 0; i < storeType.NumMethod(); i++ {
		require.NotRegexp(t, regexp.MustCompile(`(?i)delete|remove|unlink`), storeType.Method(i).Name)
	}
}

/* ------------------------------------------------------------- the queue */

func TestInProcessQueueSerializesSameKeyInCallOrder(t *testing.T) {
	q := state.CreateInProcessQueue()
	var mu sync.Mutex
	var order []int
	entered := make(chan int, 3)
	var wg sync.WaitGroup
	start := func(n int, hold time.Duration) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := q.Run("k", func() error {
				entered <- n
				time.Sleep(hold)
				mu.Lock()
				order = append(order, n)
				mu.Unlock()
				return nil
			})
			assert.NoError(t, err)
		}()
	}
	// Each run is registered only after the previous one has entered, so the
	// recorded order is the serialized call order — deterministically.
	start(1, 15*time.Millisecond)
	require.Equal(t, 1, <-entered)
	start(2, 8*time.Millisecond)
	require.Equal(t, 2, <-entered)
	start(3, 0)
	require.Equal(t, 3, <-entered)
	wg.Wait()
	require.Equal(t, []int{1, 2, 3}, order)
}

func TestInProcessQueueRunsDifferentKeysConcurrently(t *testing.T) {
	q := state.CreateInProcessQueue()
	var arrived atomic.Int64
	ready := make(chan struct{})
	run := func(key string) func() error {
		return func() error {
			if arrived.Add(1) == 2 {
				close(ready)
			}
			select {
			case <-ready:
				return nil
			case <-time.After(5 * time.Second):
				return fmt.Errorf("timed out waiting for %q to run concurrently", key)
			}
		}
	}
	done := make(chan error, 2)
	go func() { done <- q.Run("a", run("a")) }()
	go func() { done <- q.Run("b", run("b")) }()
	require.NoError(t, <-done)
	require.NoError(t, <-done)
}

func TestInProcessQueuePropagatesFailuresAndStaysUsableAfterwards(t *testing.T) {
	q := state.CreateInProcessQueue()
	boom := errors.New("boom")
	err := q.Run("k", func() error { return boom })
	require.EqualError(t, err, "boom")
	require.ErrorIs(t, err, boom)

	var ran bool
	require.NoError(t, q.Run("k", func() error { ran = true; return nil }))
	require.True(t, ran)

	// The same-key entry was retired, so a fresh registration still works.
	var ranAgain bool
	require.NoError(t, q.Run("k", func() error { ranAgain = true; return nil }))
	require.True(t, ranAgain)
}

/* ------------------------------------------------------------ StateStore */

func TestStateStoreWritesValidatedBaselineWithCanonicalBytesAndChurn(t *testing.T) {
	stateDir := makeStateDir(t)
	store, err := state.NewStateStore(state.StateStoreOptions{
		StateDir: stateDir,
		Churn:    state.CreateStateChurnCounter(),
	})
	require.NoError(t, err)
	counter := state.CreateStateChurnCounter()
	storeWithCounter, err := state.NewStateStore(state.StateStoreOptions{
		StateDir: stateDir,
		Churn:    counter,
	})
	require.NoError(t, err)

	baseline := baselineExample
	created, err := storeWithCounter.WriteBaseline(baseline.DocPath, baseline, nil)
	require.NoError(t, err)
	require.True(t, created.OK)
	require.Equal(t, docutil.ContentHash(canonicalJSON(t, baseline)), created.Hash)
	require.Equal(t, state.StateChurnCounter{Added: 1, Updated: 0, Unchanged: 0}, counter.Snapshot())

	// Canonical bytes on disk: hash input and file bytes are the same exact
	// serialization, and every read re-hits the registry.
	read, err := storeWithCounter.ReadBaseline(baseline.DocPath)
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.NotNil(t, read.Content)
	require.Equal(t, baseline, *read.Content)
	require.Equal(t, canonicalJSON(t, baseline), read.Raw)
	require.Equal(t, created.Hash, read.Hash)

	updatedValue := baseline
	updatedValue.VerifiedByRun = "run_2"
	updated, err := storeWithCounter.WriteBaseline(baseline.DocPath, updatedValue, &read.Hash)
	require.NoError(t, err)
	require.True(t, updated.OK)
	require.NotEqual(t, created.Hash, updated.Hash)
	require.Equal(t, state.StateChurnCounter{Added: 1, Updated: 1, Unchanged: 0}, counter.Snapshot())
	require.Equal(t, schemas.StateChurn{Added: 1, Updated: 1, Unchanged: 0},
		state.ChurnToRecordFields(counter.Snapshot()))

	// A second store on the same dir reads the same bytes (canonical JSON is
	// stable across stores and runs).
	other, err := store.ReadBaseline(baseline.DocPath)
	require.NoError(t, err)
	require.Equal(t, updatedValue, *other.Content)
	require.Equal(t, updated.Hash, other.Hash)
}

func TestStateStoreCasRaceLoserConflictsWinnerSurvives(t *testing.T) {
	store, _ := newStore(t, nil)
	docPath := baselineExample.DocPath

	baselineCreate, err := store.WriteBaseline(docPath, baselineExample, nil)
	require.NoError(t, err)
	require.True(t, baselineCreate.OK)

	readA, err := store.ReadBaseline(docPath)
	require.NoError(t, err)
	require.Equal(t, baselineCreate.Hash, readA.Hash)

	baselineB := baselineExample
	baselineB.VerifiedByRun = "run_B"
	baselineBWrite, err := store.WriteBaseline(docPath, baselineB, &baselineCreate.Hash)
	require.NoError(t, err)
	require.True(t, baselineBWrite.OK)

	staleA := baselineExample
	staleA.VerifiedByRun = "run_A"
	lostRace, err := store.WriteBaseline(docPath, staleA, &baselineCreate.Hash)
	require.NoError(t, err)
	require.False(t, lostRace.OK)
	require.Equal(t, baselineCreate.Hash, *lostRace.ExpectedHash)
	require.Equal(t, baselineBWrite.Hash, *lostRace.ActualHash)

	after, err := store.ReadBaseline(docPath)
	require.NoError(t, err)
	require.Equal(t, baselineB, *after.Content)
	require.Equal(t, baselineBWrite.Hash, after.Hash)

	reApplied, err := store.WriteBaseline(docPath, staleA, &after.Hash)
	require.NoError(t, err)
	require.True(t, reApplied.OK)
	reread, err := store.ReadBaseline(docPath)
	require.NoError(t, err)
	require.Equal(t, staleA, *reread.Content)
}

func TestStateStoreFailsClosedOnSchemaDrift(t *testing.T) {
	store, _ := newStore(t, nil)
	baseline := baselineExample

	first, err := store.WriteBaseline(baseline.DocPath, baseline, nil)
	require.NoError(t, err)
	require.True(t, first.OK)
	before, err := store.ReadBaseline(baseline.DocPath)
	require.NoError(t, err)

	drifted := baseline
	drifted.PolicyVersion = 0 // zod-equivalent drift: "Number must be greater than 0"
	_, driftErr := store.WriteBaseline(baseline.DocPath, drifted, &first.Hash)
	requireSchemaDrift(t, driftErr, "policy_version")

	relPath, err := state.BaselineRelPath(baseline.DocPath)
	require.NoError(t, err)
	require.Equal(t,
		`EDOCSYNC_STATE_SCHEMA: schema drift for "`+relPath+
			`" — refusing (fail closed, OD-R03): policy_version: Number must be greater than 0`,
		driftErr.Error())

	_, err = store.WriteBaseline("other.md", drifted, nil)
	requireSchemaDrift(t, err, "policy_version")

	// Drift fails closed: the drifted content never lands.
	after, err := store.ReadBaseline("other.md")
	require.NoError(t, err)
	require.False(t, after.Exists)
	reread, err := store.ReadBaseline(baseline.DocPath)
	require.NoError(t, err)
	require.Equal(t, before.Raw, reread.Raw)
}

func TestStateStoreFailsClosedReadingCorruptAndSchemaInvalidFiles(t *testing.T) {
	store, stateDir := newStore(t, nil)
	require.NoError(t, os.MkdirAll(filepath.Join(stateDir, "baselines"), 0o755))

	encoded, err := state.EncodeStateDocPath("docs/broken.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "baselines", encoded+".json"), []byte("{not json"), 0o644))
	_, err = store.ReadBaseline("docs/broken.md")
	require.Error(t, err)
	require.Equal(t, state.CodeStateCorrupt, docutil.CodeOf(err))

	encoded, err = state.EncodeStateDocPath("docs/wrong.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(stateDir, "baselines", encoded+".json"), []byte(`{"nope":true}`), 0o644))
	_, err = store.ReadBaseline("docs/wrong.md")
	require.Error(t, err)
	require.Equal(t, state.CodeStateSchema, docutil.CodeOf(err))
}

func TestStateStoreRefusesConfigAndUnknownPathsEndToEnd(t *testing.T) {
	store, stateDir := newStore(t, nil)

	// Human-owned config and repo files are refused BOTH ways (V-P0-13).
	_, err := store.WriteRawForTest("manifest.yml", "junk", nil)
	requireGuardedPath(t, err, "manifest.yml")
	_, err = store.WriteRawForTest("docs/payments.md", "junk", nil)
	requireGuardedPath(t, err, "docs/payments.md")
	_, statErr := os.Stat(filepath.Join(stateDir, "..", "manifest.yml"))
	require.True(t, errors.Is(statErr, fs.ErrNotExist))

	// A state-class path is allowed (layout violations are the layout
	// validator's business, not the guard's).
	allowed, err := store.WriteRawForTest("state/rogue.json", "{}", nil)
	require.NoError(t, err)
	require.True(t, allowed.OK)
	require.Equal(t, docutil.ContentHash("{}"), allowed.Hash)
	raw, err := os.ReadFile(filepath.Join(stateDir, "rogue.json"))
	require.NoError(t, err)
	require.Equal(t, "{}", string(raw))
}

func TestStateStoreRoutesClaimsAndRefusesUnsafeIds(t *testing.T) {
	store, _ := newStore(t, nil)
	claim := claimExample

	claimWrite, err := store.WriteClaim(claim.ID, claim, nil)
	require.NoError(t, err)
	require.True(t, claimWrite.OK)

	read, err := store.ReadClaim(claim.ID)
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.NotNil(t, read.Content)
	require.Equal(t, claim, *read.Content)
	require.Equal(t, canonicalJSON(t, claim), read.Raw)

	_, err = store.WriteClaim("bad/id", claim, nil)
	requireStateKeyError(t, err)
	_, err = store.WriteClaim("..", claim, nil)
	requireStateKeyError(t, err)
}

func TestStateStoreRoutesAuditCursorWithoutSuffix(t *testing.T) {
	store, stateDir := newStore(t, nil)
	cursor := auditCursorExample

	create, err := store.WriteAuditCursor(cursor, nil)
	require.NoError(t, err)
	require.True(t, create.OK)

	names := dirNames(t, stateDir)
	require.Contains(t, names, "audit-cursor")
	require.NotContains(t, names, "audit-cursor.json")

	read, err := store.ReadAuditCursor()
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.NotNil(t, read.Content)
	require.Equal(t, cursor, *read.Content)
	require.Equal(t, create.Hash, read.Hash)

	moved := cursor
	moved.Position = "git:zzz"
	update, err := store.WriteAuditCursor(moved, &read.Hash)
	require.NoError(t, err)
	require.True(t, update.OK)

	stale, err := store.WriteAuditCursor(cursor, &read.Hash)
	require.NoError(t, err)
	require.False(t, stale.OK)
	require.Equal(t, read.Hash, *stale.ExpectedHash)
	require.Equal(t, update.Hash, *stale.ActualHash)

	after, err := store.ReadAuditCursor()
	require.NoError(t, err)
	require.Equal(t, moved, *after.Content)
}

func TestStateStoreListsClaimIdsAndDocStatePaths(t *testing.T) {
	store, _ := newStore(t, nil)

	// No directories yet: empty, not an error.
	ids, err := store.ListClaimIds()
	require.NoError(t, err)
	require.Empty(t, ids)
	paths, err := store.ListDocStatePaths()
	require.NoError(t, err)
	require.Empty(t, paths)

	first := claimExample
	_, err = store.WriteClaim(first.ID, first, nil)
	require.NoError(t, err)
	second := claimExample
	second.ID = "claim_999"
	_, err = store.WriteClaim(second.ID, second, nil)
	require.NoError(t, err)
	ids, err = store.ListClaimIds()
	require.NoError(t, err)
	require.Equal(t, []string{"claim_182", "claim_999"}, ids)

	doc := docStateExample
	_, err = store.WriteDocState(doc.Path, doc, nil)
	require.NoError(t, err)
	paths, err = store.ListDocStatePaths()
	require.NoError(t, err)
	require.Equal(t, []string{doc.Path}, paths)
}

func TestStateStoreWriteMergeLedgerMatchesSourceKey(t *testing.T) {
	store, _ := newStore(t, nil)
	ledger := schemas.MergeLedgerExample()

	created, err := store.WriteMergeLedger(ledger.SourceKey, ledger, nil)
	require.NoError(t, err)
	require.True(t, created.OK)

	read, err := store.ReadMergeLedger(ledger.SourceKey)
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.NotNil(t, read.Content)
	require.Equal(t, ledger, *read.Content)

	_, err = store.WriteMergeLedger("other-key", ledger, nil)
	requireStateKeyError(t, err)
}

func TestStateStoreRequiresAbsoluteStateDir(t *testing.T) {
	_, err := state.NewStateStore(state.StateStoreOptions{StateDir: "relative/state"})
	require.Error(t, err)
	_, err = state.NewStateStore(state.StateStoreOptions{StateDir: ""})
	require.Error(t, err)
}

func TestGuardStateWriteRefusalMessagesArePreserved(t *testing.T) {
	store, _ := newStore(t, nil)
	cases := []struct {
		relPath string
		reason  string
	}{
		{`back\slash.md`, `backslashes are not allowed in docsync paths ("back\slash.md") — use POSIX '/' separators`},
		{"/abs.md", `absolute paths are not allowed ("/abs.md") — use a path relative to .docsync/`},
		{"C:/win.md", `absolute paths are not allowed ("C:/win.md") — use a path relative to .docsync/`},
		{"nul\x00.md", "NUL bytes are not allowed in docsync paths (\"nul\x00.md\")"},
		{"", `path is empty ("") — expected a file path inside .docsync/`},
		{".docsync/", `path is empty (".docsync/") — expected a file path inside .docsync/`},
		{"state/../manifest.yml", `path traversal ('..') is not allowed ("state/../manifest.yml")`},
		{"state//x.json", `empty path segments are not allowed ("state//x.json")`},
		{"state/./x.json", `'.' segments are not allowed ("state/./x.json") — use a direct path inside .docsync/`},
		{"manifest.yml", `state writers must not touch human-owned config; "manifest.yml" (from "manifest.yml") is config-owned — edit via a reviewable PR`},
		{"evaluation/x.json", `state writers must not touch human-owned config; "evaluation/x.json" (from "evaluation/x.json") is config-owned — edit via a reviewable PR`},
		{"policy-history/v1.yml", `state writers must not touch human-owned config; "policy-history/v1.yml" (from "policy-history/v1.yml") is config-owned — edit via a reviewable PR`},
		{"README.md", `"README.md" is outside the .docsync/ ownership model (config files, evaluation/, memory/, policy-history/, state/); refusing both config and state writes for "README.md" (fail closed)`},
	}
	for _, testCase := range cases {
		_, err := store.WriteRawForTest(testCase.relPath, "x", nil)
		requireGuardedPath(t, err, testCase.relPath)
		require.Equal(t,
			`EDOCSYNC_BOUNDARY: state write refused for "`+testCase.relPath+`": `+testCase.reason,
			err.Error(), testCase.relPath)
	}
}

func TestCasWritePropagatesAtomicWriteFailures(t *testing.T) {
	stateDir := makeStateDir(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	// A regular file where a directory is required: the write fails closed
	// instead of partially landing.
	blocker := filepath.Join(stateDir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	_, err := state.CasWriteFile(filepath.Join(blocker, "child.json"), "v1", nil)
	require.Error(t, err)
}

/* ---------------------------------------------------- run-record upsert */

func TestUpsertRunRecordSameKeyTwiceUsesOneFile(t *testing.T) {
	store, stateDir := newStore(t, nil)

	first, err := store.UpsertRunRecord(runKey, runRecordExample)
	require.NoError(t, err)
	require.True(t, first.OK)
	require.True(t, first.Created)
	require.Equal(t, docutil.ContentHash(canonicalJSON(t, runRecordExample)), first.Hash)

	secondRecord := runRecordExample
	secondRecord.RunID = "run_rerun"
	secondRecord.Outcome = strptr("ok")
	second, err := store.UpsertRunRecord(runKey, secondRecord)
	require.NoError(t, err)
	require.True(t, second.OK)
	require.False(t, second.Created)

	filename, err := state.RunRecordFilename(runKey)
	require.NoError(t, err)
	require.Equal(t, []string{filename}, dirNames(t, filepath.Join(stateDir, "runs")))

	raw, err := os.ReadFile(filepath.Join(stateDir, "runs", filename))
	require.NoError(t, err)
	var stored schemas.RunRecord
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Equal(t, secondRecord, stored)

	read, err := store.ReadRunRecord(runKey)
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.NotNil(t, read.Content)
	require.Equal(t, secondRecord, *read.Content)
}

func TestUpsertRunRecordDifferentManifestVersionCoexists(t *testing.T) {
	store, stateDir := newStore(t, nil)
	key2 := runKey
	key2.ManifestVersion = 2
	record2 := runRecordExample
	record2.Key = key2
	record2.RunID = "run_manifest2"

	first, err := store.UpsertRunRecord(runKey, runRecordExample)
	require.NoError(t, err)
	require.True(t, first.OK)
	second, err := store.UpsertRunRecord(key2, record2)
	require.NoError(t, err)
	require.True(t, second.OK)

	filename1, err := state.RunRecordFilename(runKey)
	require.NoError(t, err)
	filename2, err := state.RunRecordFilename(key2)
	require.NoError(t, err)
	require.Equal(t, []string{filename1, filename2}, dirNames(t, filepath.Join(stateDir, "runs")))

	read1, err := store.ReadRunRecord(runKey)
	require.NoError(t, err)
	require.Equal(t, runRecordExample, *read1.Content)
	read2, err := store.ReadRunRecord(key2)
	require.NoError(t, err)
	require.Equal(t, record2, *read2.Content)
}

func TestUpsertRunRecordRefusesMismatchedKey(t *testing.T) {
	store, stateDir := newStore(t, nil)
	mismatched := runRecordExample
	mismatched.Key.PolicyVersion = 99

	_, err := store.UpsertRunRecord(runKey, mismatched)
	requireStateKeyError(t, err)

	filename, err := state.RunRecordFilename(runKey)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(stateDir, "runs", filename))
	require.True(t, errors.Is(statErr, fs.ErrNotExist))
}

func TestReadRunRecordUnknownKeyIsEmptyNotCorrupt(t *testing.T) {
	store, _ := newStore(t, nil)
	other := runKey
	other.HeadSHA = "0000000"

	read, err := store.ReadRunRecord(other)
	require.NoError(t, err)
	require.False(t, read.Exists)
	require.Nil(t, read.Content)
	require.Empty(t, read.Raw)
	require.Empty(t, read.Hash)
}

func TestTwoStoresProduceByteIdenticalRunRecords(t *testing.T) {
	store1, stateDir1 := newStore(t, nil)
	store2, stateDir2 := newStore(t, nil)

	_, err := store1.UpsertRunRecord(runKey, runRecordExample)
	require.NoError(t, err)
	_, err = store2.UpsertRunRecord(runKey, runRecordExample)
	require.NoError(t, err)

	filename, err := state.RunRecordFilename(runKey)
	require.NoError(t, err)
	raw1, err := os.ReadFile(filepath.Join(stateDir1, "runs", filename))
	require.NoError(t, err)
	raw2, err := os.ReadFile(filepath.Join(stateDir2, "runs", filename))
	require.NoError(t, err)
	require.Equal(t, string(raw1), string(raw2))
	require.Equal(t, canonicalJSON(t, runRecordExample), string(raw1))
}

/* --------------------------------------------------- transition appends */

func TestAppendTransitionAppendsInOrderAndRefusesStaleHash(t *testing.T) {
	store, _ := newStore(t, nil)
	doc := docStateExample

	_, err := store.WriteDocState(doc.Path, doc, nil)
	require.NoError(t, err)
	before, err := store.ReadDocState(doc.Path)
	require.NoError(t, err)
	require.True(t, before.Exists)
	require.Len(t, before.Content.Transitions, 1)
	firstTransition := before.Content.Transitions[0]

	second := schemas.DocStateTransition{
		State:  schemas.DocStateClear,
		At:     "2026-10-05T10:00:00Z",
		RunID:  "run_2",
		Reason: "re-verified after run_2",
	}
	third := schemas.DocStateTransition{
		State:  schemas.DocStateVerified,
		At:     "2026-10-05T11:00:00Z",
		RunID:  "run_3",
		Reason: "verified",
	}

	appended1, err := store.AppendTransition(doc.Path, second, &before.Hash)
	require.NoError(t, err)
	require.True(t, appended1.OK)

	stale, err := store.AppendTransition(doc.Path, third, &before.Hash)
	require.NoError(t, err)
	require.False(t, stale.OK)
	require.Equal(t, before.Hash, *stale.ExpectedHash)
	require.Equal(t, appended1.Hash, *stale.ActualHash)

	mid, err := store.ReadDocState(doc.Path)
	require.NoError(t, err)
	require.Equal(t, []schemas.DocStateTransition{firstTransition, second}, mid.Content.Transitions)

	appended2, err := store.AppendTransition(doc.Path, third, &mid.Hash)
	require.NoError(t, err)
	require.True(t, appended2.OK)

	final, err := store.ReadDocState(doc.Path)
	require.NoError(t, err)
	require.Equal(t, []schemas.DocStateTransition{firstTransition, second, third}, final.Content.Transitions)
	require.Equal(t, doc.Path, final.Content.Path)
}

func TestAppendTransitionRefusesMissingDocState(t *testing.T) {
	store, _ := newStore(t, nil)
	_, err := store.AppendTransition("docs/absent.md", schemas.DocStateTransition{
		State:  schemas.DocStateClear,
		At:     "2026-10-05T10:00:00Z",
		RunID:  "run_2",
		Reason: "re-verified",
	}, nil)
	requireStateMissingError(t, err)
}

/* --------------------------------------------------------- churn helpers */

func TestChurnHookCountsCreatedAndUpdatedNotConflicts(t *testing.T) {
	counter := state.CreateStateChurnCounter()
	store, _ := newStore(t, counter)

	first, err := store.WriteAuditCursor(auditCursorExample, nil)
	require.NoError(t, err)
	require.True(t, first.OK)
	require.Equal(t, state.StateChurnCounter{Added: 1}, counter.Snapshot())

	moved := auditCursorExample
	moved.Position = "git:zzz"
	second, err := store.WriteAuditCursor(moved, &first.Hash)
	require.NoError(t, err)
	require.True(t, second.OK)
	require.Equal(t, state.StateChurnCounter{Added: 1, Updated: 1}, counter.Snapshot())

	// A CAS conflict is NOT a write: the hook stays silent.
	stale := auditCursorExample
	stale.Position = "git:old"
	_, err = store.WriteAuditCursor(stale, &first.Hash)
	require.NoError(t, err)
	require.Equal(t, state.StateChurnCounter{Added: 1, Updated: 1}, counter.Snapshot())
}

func TestCreateStateChurnCounterStartsZeroed(t *testing.T) {
	counter := state.CreateStateChurnCounter()
	require.Equal(t, state.StateChurnCounter{}, counter.Snapshot())
	counter.RecordCreated()
	require.Equal(t, state.StateChurnCounter{Added: 1}, counter.Snapshot())
	counter.RecordUpdated()
	require.Equal(t, state.StateChurnCounter{Added: 1, Updated: 1}, counter.Snapshot())
	counter.RecordUnchanged()
	require.Equal(t, state.StateChurnCounter{Added: 1, Updated: 1, Unchanged: 1}, counter.Snapshot())
	require.Equal(t, schemas.StateChurn{Added: 1, Updated: 1, Unchanged: 1},
		state.ChurnToRecordFields(counter.Snapshot()))
}

func TestDeriveStateChurnFoldsWriteOutcomes(t *testing.T) {
	zero := schemas.StateChurn{}
	require.Equal(t, schemas.StateChurn{Added: 1},
		state.DeriveStateChurn(zero, state.StateChurnChange{Written: true, Created: true}))
	require.Equal(t, schemas.StateChurn{Updated: 1},
		state.DeriveStateChurn(zero, state.StateChurnChange{Written: true}))
	require.Equal(t, schemas.StateChurn{Unchanged: 1},
		state.DeriveStateChurn(zero, state.StateChurnChange{}))

	before := schemas.StateChurn{Added: 2, Updated: 1, Unchanged: 3}
	require.Equal(t, schemas.StateChurn{Added: 3, Updated: 1, Unchanged: 3},
		state.DeriveStateChurn(before, state.StateChurnChange{Written: true, Created: true}))
	require.Equal(t, schemas.StateChurn{Added: 2, Updated: 2, Unchanged: 3},
		state.DeriveStateChurn(before, state.StateChurnChange{Written: true}))
	require.Equal(t, schemas.StateChurn{Added: 2, Updated: 1, Unchanged: 4},
		state.DeriveStateChurn(before, state.StateChurnChange{}))
}

func TestV_P0_14_SimulatedRunChurnFlowsThroughPersistedRunRecord(t *testing.T) {
	counter := state.CreateStateChurnCounter()
	store, _ := newStore(t, counter)
	docPath := baselineExample.DocPath

	// Step 1 — writeBaseline: created.
	baselineCreate, err := store.WriteBaseline(docPath, baselineExample, nil)
	require.NoError(t, err)
	require.True(t, baselineCreate.OK)
	require.Equal(t, state.StateChurnCounter{Added: 1}, counter.Snapshot())

	// Step 2 — writeClaim: created.
	claimWrite, err := store.WriteClaim(claimExample.ID, claimExample, nil)
	require.NoError(t, err)
	require.True(t, claimWrite.OK)
	require.Equal(t, state.StateChurnCounter{Added: 2}, counter.Snapshot())

	// Step 3 — writeBaseline again with changed content: updated.
	beforeUpdate, err := store.ReadBaseline(docPath)
	require.NoError(t, err)
	baselineUpdate := baselineExample
	baselineUpdate.VerifiedByRun = "run_2"
	baselineUpdateWrite, err := store.WriteBaseline(docPath, baselineUpdate, &beforeUpdate.Hash)
	require.NoError(t, err)
	require.True(t, baselineUpdateWrite.OK)
	require.Equal(t, state.StateChurnCounter{Added: 2, Updated: 1}, counter.Snapshot())

	// Step 4 — reads are never writes: the hook stays silent, and the
	// orchestrator folds the read-only step as unchanged.
	_, err = store.ReadBaseline(docPath)
	require.NoError(t, err)
	_, err = store.ReadClaim(claimExample.ID)
	require.NoError(t, err)
	require.Equal(t, state.StateChurnCounter{Added: 2, Updated: 1}, counter.Snapshot())
	counter.RecordUnchanged()
	churnAtUpsert := state.ChurnToRecordFields(counter.Snapshot())
	require.Equal(t, schemas.StateChurn{Added: 2, Updated: 1, Unchanged: 1}, churnAtUpsert)

	// Step 5 — the run record carries the counter snapshot at upsert time.
	record := runRecordExample
	record.StateChurn = churnAtUpsert
	upsert, err := store.UpsertRunRecord(runKey, record)
	require.NoError(t, err)
	require.True(t, upsert.OK)
	require.True(t, upsert.Created)
	require.Equal(t, docutil.ContentHash(canonicalJSON(t, record)), upsert.Hash)

	// The upsert itself was a real state write — counted as created.
	require.Equal(t, state.StateChurnCounter{Added: 3, Updated: 1, Unchanged: 1}, counter.Snapshot())

	// Step 6 — re-read: the persisted state_churn matches the snapshot
	// captured at upsert time and validates against the registered schema.
	read, err := store.ReadRunRecord(runKey)
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.Equal(t, churnAtUpsert, read.Content.StateChurn)
	require.Equal(t, schemas.StateChurn{Added: 2, Updated: 1, Unchanged: 1}, read.Content.StateChurn)

	entry := schemas.GetSchema("docsync.run-record")
	require.NotNil(t, entry)
	require.NoError(t, entry.Parse([]byte(read.Raw)))
	require.True(t, state.SameRunKey(read.Content.Key, runKey))
}

/* --------------------------------------------------- aggregate churn report */

func runRecordWithChurn(runID string, added, updated int) schemas.RunRecord {
	record := schemas.RunRecordExample()
	record.RunID = runID
	record.StateChurn = schemas.StateChurn{Added: added, Updated: updated}
	return record
}

func TestAggregateChurnReportsRunsWritesMeanAndWorstRun(t *testing.T) {
	require.Equal(t, 50, state.ChurnReviewThreshold)

	empty := state.AggregateChurn(state.AggregateChurnInput{})
	require.Equal(t, 0, empty.Runs)
	require.Equal(t, 0, empty.TotalStateWrites)
	require.InDelta(t, 0.0, empty.MeanWritesPerRun, 1e-12)
	require.Nil(t, empty.WorstRunRunID)
	require.Nil(t, empty.TotalStateReads)
	require.Equal(t, state.ChurnDataSourceFixture, empty.DataSource)

	report := state.AggregateChurn(state.AggregateChurnInput{
		Records: []schemas.RunRecord{
			runRecordWithChurn("run_a", 1, 2),
			runRecordWithChurn("run_b", 5, 5),
			runRecordWithChurn("run_c", 0, 0),
		},
	})
	require.Equal(t, 3, report.Runs)
	require.Equal(t, 13, report.TotalStateWrites)
	require.InDelta(t, 13.0/3.0, report.MeanWritesPerRun, 1e-12)
	require.NotNil(t, report.WorstRunRunID)
	require.Equal(t, "run_b", *report.WorstRunRunID)
	require.Equal(t, state.ChurnDataSourceFixture, report.DataSource)

	// Ties go to the first record.
	tie := state.AggregateChurn(state.AggregateChurnInput{
		Records: []schemas.RunRecord{
			runRecordWithChurn("run_x", 2, 0),
			runRecordWithChurn("run_y", 0, 2),
		},
	})
	require.Equal(t, "run_x", *tie.WorstRunRunID)

	pilot := state.AggregateChurn(state.AggregateChurnInput{
		Records:    []schemas.RunRecord{runRecordWithChurn("run_p", 1, 0)},
		DataSource: state.ChurnDataSourcePilot,
	})
	require.Equal(t, state.ChurnDataSourcePilot, pilot.DataSource)
	require.Equal(t, "run_p", *pilot.WorstRunRunID)
}

/* ------------------------------------------------------ property ports */

// The TS property suites used fast-check with 200 runs; Go ports use fixed
// seeds over the same generators, which is deterministic run to run.

func TestCanonicalJSONKeyOrderInvarianceProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20261010))
	for i := 0; i < 200; i++ {
		value := randomJSONValue(rng, 0)
		canonical, err := docutil.CanonicalJSON(value)
		require.NoError(t, err)

		var reparsed any
		require.NoError(t, json.Unmarshal([]byte(canonical), &reparsed))
		recanonical, err := docutil.CanonicalJSON(reparsed)
		require.NoError(t, err)
		require.Equal(t, canonical, recanonical, "run %d", i)

		if object, ok := value.(map[string]any); ok {
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			reversed := make(map[string]any, len(object))
			for j := len(keys) - 1; j >= 0; j-- {
				reversed[keys[j]] = object[keys[j]]
			}
			reversedCanonical, err := docutil.CanonicalJSON(reversed)
			require.NoError(t, err)
			require.Equal(t, canonical, reversedCanonical, "run %d", i)
		}
	}
}

func TestDocPathMappingRoundTripProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(4242))
	for i := 0; i < 200; i++ {
		segments := make([]string, 1+rng.Intn(4))
		for j := range segments {
			segments[j] = randomSafeSegment(rng)
		}
		docPath := strings.Join(segments, "/")

		encoded, err := state.EncodeStateDocPath(docPath)
		require.NoError(t, err, docPath)
		require.NotContains(t, encoded, "/", docPath)
		decoded, err := state.DecodeStateDocPath(encoded)
		require.NoError(t, err, docPath)
		require.Equal(t, docPath, decoded, docPath)
	}
}

func TestRunRecordFilenameInjectivityProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(777))
	randomKey := func() schemas.RunKey {
		segments := make([]string, 1+rng.Intn(3))
		for i := range segments {
			segments[i] = randomRepoSegment(rng)
		}
		return schemas.RunKey{
			Repo:            strings.Join(segments, "/"),
			BaseSHA:         randomHex(rng, 7+rng.Intn(34)),
			HeadSHA:         randomHex(rng, 7+rng.Intn(34)),
			ManifestVersion: 1 + rng.Intn(50),
			PolicyVersion:   1 + rng.Intn(50),
		}
	}
	for i := 0; i < 200; i++ {
		a, b := randomKey(), randomKey()
		filenameA, err := state.RunRecordFilename(a)
		require.NoError(t, err)
		filenameB, err := state.RunRecordFilename(b)
		require.NoError(t, err)
		if state.SameRunKey(a, b) {
			require.Equal(t, filenameA, filenameB, "run %d", i)
		} else {
			require.NotEqual(t, filenameA, filenameB, "run %d", i)
		}
	}
}

func TestDecodeRunRecordRepoProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(31337))
	for i := 0; i < 200; i++ {
		segments := make([]string, 1+rng.Intn(3))
		for j := range segments {
			segments[j] = randomRepoSegment(rng)
		}
		key := schemas.RunKey{
			Repo:            strings.Join(segments, "/"),
			BaseSHA:         randomHex(rng, 7+rng.Intn(34)),
			HeadSHA:         randomHex(rng, 7+rng.Intn(34)),
			ManifestVersion: 1 + rng.Intn(50),
			PolicyVersion:   1 + rng.Intn(50),
		}
		filename, err := state.RunRecordFilename(key)
		require.NoError(t, err)
		repoPart := filename[:strings.Index(filename, "#")]
		decoded, err := state.DecodeRunRecordRepo(repoPart)
		require.NoError(t, err)
		require.Equal(t, key.Repo, decoded, "run %d", i)
	}
}

// randomJSONValue mirrors fc.jsonValue(): scalars, arrays and objects at
// small depth. Numbers stay round-trip safe (integers and thirds) so the
// canonical re-parse assertion is exact.
func randomJSONValue(rng *rand.Rand, depth int) any {
	if depth >= 3 || rng.Intn(3) == 0 {
		return randomScalar(rng)
	}
	if rng.Intn(2) == 0 {
		length := rng.Intn(4)
		items := make([]any, 0, length)
		for i := 0; i < length; i++ {
			items = append(items, randomJSONValue(rng, depth+1))
		}
		return items
	}
	length := rng.Intn(4)
	object := make(map[string]any, length)
	for i := 0; i < length; i++ {
		object[randomString(rng)] = randomJSONValue(rng, depth+1)
	}
	return object
}

func randomScalar(rng *rand.Rand) any {
	switch rng.Intn(5) {
	case 0:
		return nil
	case 1:
		return rng.Intn(2) == 0
	case 2:
		return rng.Intn(2001) - 1000
	case 3:
		return float64(rng.Intn(2001)-1000) / 1000
	default:
		return randomString(rng)
	}
}

var stringAlphabet = []rune("ab_\"\\/<>&é\n\t")

func randomString(rng *rand.Rand) string {
	var sb strings.Builder
	for i, n := 0, rng.Intn(8); i < n; i++ {
		sb.WriteRune(stringAlphabet[rng.Intn(len(stringAlphabet))])
	}
	return sb.String()
}

const safeSegmentAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

func randomSafeSegment(rng *rand.Rand) string {
	for {
		var sb strings.Builder
		for i, n := 0, 1+rng.Intn(12); i < n; i++ {
			sb.WriteByte(safeSegmentAlphabet[rng.Intn(len(safeSegmentAlphabet))])
		}
		segment := sb.String()
		if segment != "." && segment != ".." {
			return segment
		}
	}
}

const repoSegmentAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789_"

func randomRepoSegment(rng *rand.Rand) string {
	var sb strings.Builder
	for i, n := 0, 1+rng.Intn(6); i < n; i++ {
		sb.WriteByte(repoSegmentAlphabet[rng.Intn(len(repoSegmentAlphabet))])
	}
	return sb.String()
}

const hexAlphabet = "0123456789abcdef"

func randomHex(rng *rand.Rand, length int) string {
	var sb strings.Builder
	for i := 0; i < length; i++ {
		sb.WriteByte(hexAlphabet[rng.Intn(len(hexAlphabet))])
	}
	return sb.String()
}
