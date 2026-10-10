package baselines_test

// Black-box port of packages/core/test/baselines.test.ts and
// baselines-ladder.test.ts: the BaselineStore §6.1 CAS guards (create / update
// / typed guard conflicts / the cas-conflict branch) over BOTH the real
// state.StateStore and an in-memory fake (TS parity), the pure invalidation
// engine (source-changed, source-orphaned, claim-source-changed, the
// symbol-change hook, deterministic ordering), and the D12 ladder rungs
// (V-P1-9 squash/force-push simulations over real system git in fresh
// t.TempDir() repos — fixture setup only, no network, no repo-state writes to
// the workspace). Deterministic: no wall clock inside the logic under test.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------ golden data */

// The TS suite pulled its examples from @docsync/schemas; the Go registry is
// the same single source of truth, so the golden literal is read from it
// (never redefined here).
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

var baselineExample = registryExample[schemas.Baseline]("docsync.baseline")

// guards mirrors the TS GUARDS constant: the run-start values observed from
// BASELINE_EXAMPLE.
func guards() baselines.BaselineGuards {
	return baselines.BaselineGuards{
		ExpectedDocContentHash: baselineExample.DocContentHash,
		ExpectedBaselineCommit: baselineExample.BaselineCommit,
	}
}

// makeBaseline mirrors the TS makeBaseline: BASELINE_EXAMPLE with overrides.
func makeBaseline(overrides func(*schemas.Baseline)) schemas.Baseline {
	baseline := baselineExample
	if overrides != nil {
		overrides(&baseline)
	}
	return baseline
}

func strptr(s string) *string { return &s }

func intptr(v int) *int { return &v }

/* ------------------------------------------------- in-memory backend fake */

// fakeStateFile is one in-memory state file: its raw canonical bytes plus the
// hash of exactly those bytes (the CAS identity).
type fakeStateFile struct {
	raw  string
	hash string
}

// inMemoryBaselineBackend is the port of the TS createInMemoryStateStore
// baseline surface: identical method names/signatures, identical CAS semantics
// (nil expectedHash = "expect absent", stale hashes lose with the
// expected-vs-actual pair), identical registry validation, and identical
// canonical bytes — only the fs layer is dropped.
type inMemoryBaselineBackend struct {
	mu    sync.Mutex
	files map[string]fakeStateFile
}

func newInMemoryBaselineBackend() *inMemoryBaselineBackend {
	return &inMemoryBaselineBackend{files: map[string]fakeStateFile{}}
}

func (f *inMemoryBaselineBackend) ReadBaseline(docPath string) (state.StateRead[*schemas.Baseline], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	relPath, err := state.BaselineRelPath(docPath)
	if err != nil {
		return state.StateRead[*schemas.Baseline]{}, err
	}
	entry, ok := f.files[relPath]
	if !ok {
		return state.StateRead[*schemas.Baseline]{}, nil
	}
	content, err := parseBaselineBytes(relPath, entry.raw)
	if err != nil {
		return state.StateRead[*schemas.Baseline]{}, err
	}
	return state.StateRead[*schemas.Baseline]{Content: content, Raw: entry.raw, Hash: entry.hash, Exists: true}, nil
}

func (f *inMemoryBaselineBackend) WriteBaseline(
	docPath string,
	baseline schemas.Baseline,
	expectedHash *string,
) (state.CasWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	relPath, err := state.BaselineRelPath(docPath)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	entry := schemas.GetSchema("docsync.baseline")
	if entry == nil {
		return state.CasWriteResult{}, state.StateSchemaErrorForPath(relPath,
			errors.New(`schema "docsync.baseline" is missing from the registry — refusing (fail closed)`))
	}
	parsed, err := entry.ParseTyped([]byte(mustCanonicalJSON(baseline)))
	if err != nil {
		return state.CasWriteResult{}, state.StateSchemaErrorForPath(relPath, err)
	}
	raw := mustCanonicalJSON(parsed)
	current, exists := f.files[relPath]
	var currentHash *string
	if exists {
		hash := current.hash
		currentHash = &hash
		if expectedHash == nil || currentHash == nil || *currentHash != *expectedHash {
			return state.CasWriteResult{OK: false, ExpectedHash: expectedHash, ActualHash: currentHash}, nil
		}
	} else if expectedHash != nil {
		return state.CasWriteResult{OK: false, ExpectedHash: expectedHash, ActualHash: nil}, nil
	}
	written := fakeStateFile{raw: raw, hash: docutil.ContentHash(raw)}
	f.files[relPath] = written
	return state.CasWriteResult{OK: true, Hash: written.hash}, nil
}

// parseBaselineBytes is the fake's read path: raw bytes must be valid JSON that
// validates against the registry (fail closed on drift, never "empty").
func parseBaselineBytes(relPath, raw string) (*schemas.Baseline, error) {
	if !json.Valid([]byte(raw)) {
		return nil, state.StateCorruptError(
			`state record "%s" is not valid JSON — refusing to read (fail closed)`, relPath)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, state.StateCorruptError(
			`state record "%s" is not valid JSON — refusing to read (fail closed)`, relPath)
	}
	wire, err := json.Marshal(generic)
	if err != nil {
		return nil, err
	}
	entry := schemas.GetSchema("docsync.baseline")
	if entry == nil {
		return nil, state.StateSchemaErrorForPath(relPath,
			errors.New(`schema "docsync.baseline" is missing from the registry — refusing (fail closed)`))
	}
	parsed, err := entry.ParseTyped(wire)
	if err != nil {
		return nil, state.StateSchemaErrorForPath(relPath, err)
	}
	baseline, ok := parsed.(*schemas.Baseline)
	if !ok {
		return nil, state.StateSchemaErrorForPath(relPath,
			fmt.Errorf("registry parse returned %T, want *schemas.Baseline", parsed))
	}
	return baseline, nil
}

// mustCanonicalJSON renders a value as canonical JSON — the exact bytes the CAS
// hashes and the state files carry (same path as the real store).
func mustCanonicalJSON(value any) string {
	wire, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	dec := json.NewDecoder(bytes.NewReader(wire))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		panic(err)
	}
	out, err := docutil.CanonicalJSON(generic)
	if err != nil {
		panic(err)
	}
	return out
}

// snapshotBackend is the cas-conflict test's seam: its read returns a stale
// snapshot (captured earlier) while writes pass through to the real fake — the
// shape of a loser that read before a concurrent winner landed.
type snapshotBackend struct {
	inner baselines.BaselineStoreBackend
	stale *state.StateRead[*schemas.Baseline]
}

func (s *snapshotBackend) ReadBaseline(docPath string) (state.StateRead[*schemas.Baseline], error) {
	if s.stale != nil {
		return *s.stale, nil
	}
	return s.inner.ReadBaseline(docPath)
}

func (s *snapshotBackend) WriteBaseline(
	docPath string,
	baseline schemas.Baseline,
	expectedHash *string,
) (state.CasWriteResult, error) {
	return s.inner.WriteBaseline(docPath, baseline, expectedHash)
}

/* ----------------------------------------------------- BaselineStore tests */

func makeStateStore(t *testing.T) *state.StateStore {
	t.Helper()
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: makeStateDir(t)})
	require.NoError(t, err)
	return store
}

func makeStateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".docsync", "state")
}

func TestBaselineStore_CreatesThenUpdatesUnderMatchingGuards_RealStoreAndFakeParity(t *testing.T) {
	real := makeStateStore(t)
	for _, backend := range []baselines.BaselineStoreBackend{real, newInMemoryBaselineBackend()} {
		store := baselines.NewBaselineStore(backend)

		got, err := store.Get("docs/payments.md")
		require.NoError(t, err)
		require.Nil(t, got)

		created, err := store.Upsert(baselineExample, guards())
		require.NoError(t, err)
		require.True(t, created.OK)
		require.True(t, created.Created)
		stored, err := store.Get("docs/payments.md")
		require.NoError(t, err)
		require.NotNil(t, stored)
		require.Equal(t, baselineExample, *stored)

		updated, err := store.Upsert(makeBaseline(func(b *schemas.Baseline) { b.VerifiedByRun = "run_2" }), guards())
		require.NoError(t, err)
		require.True(t, updated.OK)
		require.False(t, updated.Created)
		after, err := store.Get("docs/payments.md")
		require.NoError(t, err)
		require.NotNil(t, after)
		require.Equal(t, "run_2", after.VerifiedByRun)
		require.NotEqual(t, created.Hash, updated.Hash)
	}
}

func TestBaselineStore_StaleDocContentHashGuard_DocContentChanged(t *testing.T) {
	store := baselines.NewBaselineStore(makeStateStore(t))
	_, err := store.Upsert(baselineExample, guards())
	require.NoError(t, err)

	loser, err := store.Upsert(
		makeBaseline(func(b *schemas.Baseline) { b.VerifiedByRun = "run_loser" }),
		baselines.BaselineGuards{
			ExpectedDocContentHash: "sha256:stale",
			ExpectedBaselineCommit: baselineExample.BaselineCommit,
		})
	require.NoError(t, err)
	require.False(t, loser.OK)
	require.Equal(t, baselines.ConflictDocContentChanged, loser.Kind)
	require.Contains(t, loser.Detail, "run-start")

	// The winner's bytes survive.
	stored, err := store.Get("docs/payments.md")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, baselineExample, *stored)
}

func TestBaselineStore_StaleBaselineCommitGuard_BaselineCommitMoved(t *testing.T) {
	store := baselines.NewBaselineStore(newInMemoryBaselineBackend())
	_, err := store.Upsert(baselineExample, guards())
	require.NoError(t, err)

	loser, err := store.Upsert(
		makeBaseline(func(b *schemas.Baseline) { b.VerifiedByRun = "run_loser" }),
		baselines.BaselineGuards{
			ExpectedDocContentHash: baselineExample.DocContentHash,
			ExpectedBaselineCommit: "0000000000000000000000000000000000000000",
		})
	require.NoError(t, err)
	require.False(t, loser.OK)
	require.Equal(t, baselines.ConflictBaselineCommitMoved, loser.Kind)

	stored, err := store.Get("docs/payments.md")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, baselineExample, *stored)
}

func TestBaselineStore_RerunWithFreshGuardsAfterAStaleGuardConflict_R15(t *testing.T) {
	store := baselines.NewBaselineStore(makeStateStore(t))

	_, err := store.Upsert(
		makeBaseline(func(b *schemas.Baseline) {
			b.DocContentHash = "sha256:v1"
			b.BaselineCommit = "c1"
		}),
		baselines.BaselineGuards{ExpectedDocContentHash: "sha256:v0", ExpectedBaselineCommit: "c0"})
	require.NoError(t, err)

	winner, err := store.Upsert(
		makeBaseline(func(b *schemas.Baseline) {
			b.DocContentHash = "sha256:v2"
			b.BaselineCommit = "c2"
		}),
		baselines.BaselineGuards{ExpectedDocContentHash: "sha256:v1", ExpectedBaselineCommit: "c1"})
	require.NoError(t, err)
	require.True(t, winner.OK)

	stale, err := store.Upsert(
		makeBaseline(func(b *schemas.Baseline) {
			b.DocContentHash = "sha256:v3"
			b.BaselineCommit = "c3"
		}),
		baselines.BaselineGuards{ExpectedDocContentHash: "sha256:v0", ExpectedBaselineCommit: "c0"})
	require.NoError(t, err)
	require.False(t, stale.OK)

	afterLoss, err := store.Get("docs/payments.md")
	require.NoError(t, err)
	require.NotNil(t, afterLoss)
	require.Equal(t, "sha256:v2", afterLoss.DocContentHash)

	rerun, err := store.Upsert(
		makeBaseline(func(b *schemas.Baseline) {
			b.DocContentHash = "sha256:v3"
			b.BaselineCommit = "c3"
		}),
		baselines.BaselineGuards{ExpectedDocContentHash: "sha256:v2", ExpectedBaselineCommit: "c2"})
	require.NoError(t, err)
	require.True(t, rerun.OK)

	stored, err := store.Get("docs/payments.md")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "sha256:v3", stored.DocContentHash)
	require.Equal(t, "c3", stored.BaselineCommit)
}

func TestBaselineStore_CasConflictBranch_BytesMovedBetweenReadAndWrite(t *testing.T) {
	real := newInMemoryBaselineBackend()
	backend := &snapshotBackend{inner: real}
	store := baselines.NewBaselineStore(backend)

	_, err := store.Upsert(baselineExample, guards())
	require.NoError(t, err)
	snapshot, err := real.ReadBaseline("docs/payments.md")
	require.NoError(t, err)
	backend.stale = &snapshot

	// A concurrent winner lands after the loser's read but before its write:
	// the loser's CAS expectation (its read's hash) is now stale.
	concurrentWinner, err := real.WriteBaseline(
		"docs/payments.md",
		makeBaseline(func(b *schemas.Baseline) { b.VerifiedByRun = "run_winner" }),
		&snapshot.Hash)
	require.NoError(t, err)
	require.True(t, concurrentWinner.OK)

	loser, err := store.Upsert(
		makeBaseline(func(b *schemas.Baseline) { b.VerifiedByRun = "run_loser" }), guards())
	require.NoError(t, err)
	require.False(t, loser.OK)
	require.Equal(t, baselines.ConflictCasConflict, loser.Kind)
	require.Contains(t, loser.Detail, "loser re-runs")

	// The winner's bytes survive.
	after, err := real.ReadBaseline("docs/payments.md")
	require.NoError(t, err)
	require.True(t, after.Exists)
	require.NotNil(t, after.Content)
	require.Equal(t, "run_winner", after.Content.VerifiedByRun)

	// The loser re-runs with a FRESH read (poisoned snapshot cleared) and succeeds.
	backend.stale = nil
	rerun, err := store.Upsert(
		makeBaseline(func(b *schemas.Baseline) { b.VerifiedByRun = "run_loser" }), guards())
	require.NoError(t, err)
	require.True(t, rerun.OK)
}

func TestBaselineStore_RefusesToOverwriteARecordForADifferentDoc_FailClosed(t *testing.T) {
	real := makeStateStore(t)
	store := baselines.NewBaselineStore(real)
	// Poison the key: the stored record names a different doc than its file.
	_, err := real.WriteRawForTest(
		"state/baselines/docs__other.md.json",
		mustCanonicalJSON(makeBaseline(func(b *schemas.Baseline) { b.DocPath = "docs/payments.md" })),
		nil)
	require.NoError(t, err)

	misKeyed := makeBaseline(func(b *schemas.Baseline) { b.DocPath = "docs/other.md" })
	_, err = store.Upsert(misKeyed, baselines.BaselineGuards{
		ExpectedDocContentHash: misKeyed.DocContentHash,
		ExpectedBaselineCommit: misKeyed.BaselineCommit,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "different doc")
	require.Equal(t, state.CodeStateKey, docutil.CodeOf(err))

	// The miskeyed record is still there, untouched.
	read, err := real.ReadBaseline("docs/other.md")
	require.NoError(t, err)
	require.True(t, read.Exists)
	require.NotNil(t, read.Content)
	require.Equal(t, "docs/payments.md", read.Content.DocPath)
}

/* ----------------------------------------------- evaluateInvalidation tests */

func TestEvaluateInvalidation_MappedSourceChange_SourceChangedReasons(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) {
		b.SourceFiles = []string{"src/payments/refund.ts", "src/payments/**"}
	})
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:     &baseline,
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: "src/payments/refund.ts", Status: baselines.ChangeModified}},
	})
	require.NoError(t, err)
	require.True(t, result.Invalidated)
	// The change matches BOTH the literal source and the glob (byte-wise
	// detail order: the glob entry sorts first).
	require.Equal(t, []baselines.InvalidationReason{
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/**" matches changed file "src/payments/refund.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/refund.ts" matches changed file "src/payments/refund.ts" (modified)`,
		},
	}, result.Reasons)
}

func TestEvaluateInvalidation_UnmappedChange_NotInvalidated(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) {
		b.SourceFiles = []string{"src/payments/refund.ts", "src/payments/**"}
	})
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:     &baseline,
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: "src/unrelated/main.ts", Status: baselines.ChangeModified}},
	})
	require.NoError(t, err)
	require.False(t, result.Invalidated)
	require.Empty(t, result.Reasons)
}

func TestEvaluateInvalidation_DeletedLiteralSource_UnionOfSignals(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) {
		b.SourceFiles = []string{"src/payments/refund.ts", "src/payments/**"}
	})
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:     &baseline,
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: "src/payments/refund.ts", Status: baselines.ChangeDeleted}},
	})
	require.NoError(t, err)
	require.True(t, result.Invalidated)
	kinds := make([]baselines.InvalidationReasonKind, 0, len(result.Reasons))
	for _, reason := range result.Reasons {
		kinds = append(kinds, reason.Kind)
	}
	require.Contains(t, kinds, baselines.ReasonSourceChanged)
	require.Contains(t, kinds, baselines.ReasonSourceOrphaned)
	require.Contains(t, result.Reasons, baselines.InvalidationReason{
		Kind:   baselines.ReasonSourceOrphaned,
		Detail: `source "src/payments/refund.ts" deleted`,
	})
}

func TestEvaluateInvalidation_RenamedLiteralSource_OrphanNamesTheRename(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) {
		b.SourceFiles = []string{"src/payments/refund.ts", "src/payments/**"}
	})
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline: &baseline,
		ChangedFiles: []baselines.InvalidationChangedFile{{
			Path:    "src/payments/refund_v2.ts",
			Status:  baselines.ChangeRenamed,
			OldPath: strptr("src/payments/refund.ts"),
		}},
	})
	require.NoError(t, err)
	require.Contains(t, result.Reasons, baselines.InvalidationReason{
		Kind:   baselines.ReasonSourceOrphaned,
		Detail: `source "src/payments/refund.ts" renamed away to "src/payments/refund_v2.ts"`,
	})
	kinds := make([]baselines.InvalidationReasonKind, 0, len(result.Reasons))
	for _, reason := range result.Reasons {
		kinds = append(kinds, reason.Kind)
	}
	require.Contains(t, kinds, baselines.ReasonSourceChanged)
}

func TestEvaluateInvalidation_LiteralSourceAbsentAtPinnedRevision_OrphanFallback(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) {
		b.SourceFiles = []string{"src/payments/refund.ts", "src/payments/**"}
	})
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:     &baseline,
		ChangedFiles: nil,
		ExistsAtHead: func(path string) bool { return path != "src/payments/refund.ts" },
	})
	require.NoError(t, err)
	require.True(t, result.Invalidated)
	require.Equal(t, []baselines.InvalidationReason{{
		Kind:   baselines.ReasonSourceOrphaned,
		Detail: `source "src/payments/refund.ts" does not exist at the pinned revision`,
	}}, result.Reasons)
}

func TestEvaluateInvalidation_GlobSourcesNeverOrphanViaExistsAtHead(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) {
		b.SourceFiles = []string{"src/payments/**"}
	})
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:     &baseline,
		ChangedFiles: nil,
		ExistsAtHead: func(string) bool { return false },
	})
	require.NoError(t, err)
	require.False(t, result.Invalidated)
	require.Empty(t, result.Reasons)
}

func TestEvaluateInvalidation_ClaimSourceChangeNamesSortedClaimIDs(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) { b.SourceFiles = []string{} })
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:     &baseline,
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: "src/payments/refund.ts", Status: baselines.ChangeModified}},
		Claims: []baselines.InvalidationClaim{
			{ID: "claim_182", Source: baselines.InvalidationClaimSource{File: "src/payments/refund.ts"}},
			{ID: "claim_009", Source: baselines.InvalidationClaimSource{File: "src/payments/refund.ts"}},
			{ID: "claim_other", Source: baselines.InvalidationClaimSource{File: "src/other.ts"}},
		},
	})
	require.NoError(t, err)
	require.True(t, result.Invalidated)
	require.Equal(t, []baselines.InvalidationReason{{
		Kind:   baselines.ReasonClaimSourceChanged,
		Detail: `claims [claim_009, claim_182] reference changed file "src/payments/refund.ts" (modified)`,
	}}, result.Reasons)
}

func TestEvaluateInvalidation_SymbolChangeFiresOnlyWhenFlagIsTrue(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) { b.SourceFiles = []string{} })
	without, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:             &baseline,
		ChangedFiles:         nil,
		SourceSymbolsChanged: false,
	})
	require.NoError(t, err)
	require.False(t, without.Invalidated)
	require.Empty(t, without.Reasons)

	withFlag, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:             &baseline,
		ChangedFiles:         nil,
		SourceSymbolsChanged: true,
	})
	require.NoError(t, err)
	require.True(t, withFlag.Invalidated)
	require.Len(t, withFlag.Reasons, 1)
	require.Equal(t, baselines.ReasonSymbolChange, withFlag.Reasons[0].Kind)
}

func TestEvaluateInvalidation_ReasonsDeduplicatedAndSortedByKindThenDetail(t *testing.T) {
	baseline := makeBaseline(func(b *schemas.Baseline) {
		b.SourceFiles = []string{"src/payments/refund.ts", "src/payments/**"}
	})
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline: &baseline,
		ChangedFiles: []baselines.InvalidationChangedFile{
			{Path: "src/payments/refund.ts", Status: baselines.ChangeModified},
			{Path: "src/payments/aaa.ts", Status: baselines.ChangeAdded},
		},
		Claims: []baselines.InvalidationClaim{
			{ID: "claim_1", Source: baselines.InvalidationClaimSource{File: "src/payments/refund.ts"}},
		},
		SourceSymbolsChanged: true,
	})
	require.NoError(t, err)

	keys := make([]string, 0, len(result.Reasons))
	for _, reason := range result.Reasons {
		keys = append(keys, string(reason.Kind)+"|"+reason.Detail)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		require.False(t, seen[key], "duplicate reason key %q", key)
		seen[key] = true
	}
	sortedKeys := append([]string(nil), keys...)
	require.Equal(t, sortedCopy(sortedKeys), keys)
	// refund.ts matches the literal source AND the glob; aaa.ts the glob only.
	kinds := make([]baselines.InvalidationReasonKind, 0, len(result.Reasons))
	for _, reason := range result.Reasons {
		kinds = append(kinds, reason.Kind)
	}
	require.Equal(t, []baselines.InvalidationReasonKind{
		baselines.ReasonClaimSourceChanged,
		baselines.ReasonSourceChanged,
		baselines.ReasonSourceChanged,
		baselines.ReasonSourceChanged,
		baselines.ReasonSymbolChange,
	}, kinds)
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func TestEvaluateSourceIntersection_NoOrphanRules(t *testing.T) {
	// The intersection stage alone reports changed sources/claims/symbols but
	// NEVER orphan signals — deleted literals surface only via
	// BaselineOrphanSignals (structured, per source).
	result, err := baselines.EvaluateSourceIntersection(baselines.EvaluateSourceIntersectionInput{
		Sources:      []string{"src/payments/refund.ts", "src/payments/**"},
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: "src/payments/refund.ts", Status: baselines.ChangeDeleted}},
	})
	require.NoError(t, err)
	for _, reason := range result.Reasons {
		require.NotEqual(t, baselines.ReasonSourceOrphaned, reason.Kind)
	}
	require.True(t, result.Invalidated)

	signals := baselines.BaselineOrphanSignals(baselines.BaselineOrphanSignalsInput{
		Sources:      []string{"src/payments/refund.ts", "src/payments/**"},
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: "src/payments/refund.ts", Status: baselines.ChangeDeleted}},
	})
	require.Len(t, signals, 1)
	require.Equal(t, "src/payments/refund.ts", signals[0].Source)
	require.Equal(t, baselines.ReasonSourceOrphaned, signals[0].Reason.Kind)
	require.Equal(t, `source "src/payments/refund.ts" deleted`, signals[0].Reason.Detail)
}

/* ----------------------------------------------------------- ladder fixtures */

// identityArgs pins the commit identity per invocation (the fixture env points
// GIT_CONFIG_GLOBAL at /dev/null, so no user config exists).
var identityArgs = []string{"-c", "user.name=docsync-test", "-c", "user.email=docsync-test@invalid"}

// ladderRepo is a throwaway real repository (TS TestGitRepo).
type ladderRepo struct {
	root string
}

func newLadderRepo(t *testing.T) *ladderRepo {
	t.Helper()
	repo := &ladderRepo{root: t.TempDir()}
	repo.git(t, "init", "-b", "main")
	// Platform port (DX-P11-3): repo-local line-ending policy — the machine
	// level system gitconfig carries core.autocrlf=true, which would rewrite
	// fresh fixture files to CRLF. Pin LF in-repo before any file is written.
	repo.git(t, "config", "core.autocrlf", "false")
	repo.git(t, "config", "core.eol", "lf")
	return repo
}

func (r *ladderRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	return runGit(t, r.root, args...)
}

func (r *ladderRepo) gitIdentity(t *testing.T, args ...string) {
	t.Helper()
	runGit(t, r.root, append(append([]string(nil), identityArgs...), args...)...)
}

func (r *ladderRepo) write(t *testing.T, relPath, content string) {
	t.Helper()
	target := filepath.Join(r.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func (r *ladderRepo) commitAll(t *testing.T, message string) string {
	t.Helper()
	r.gitIdentity(t, "add", "-A")
	r.gitIdentity(t, "commit", "-q", "-m", message)
	return strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
}

func (r *ladderRepo) client(t *testing.T) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: r.root})
	require.NoError(t, err)
	return client
}

// runGit runs a raw git command outside the typed wrapper (fixtures only).
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = fixtureEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return string(out)
}

// fixtureEnv is a deterministic, isolated git environment: no user or system
// config, fixed identity and dates (TS GIT_ENV).
func fixtureEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"TERM=dumb",
	}
}

// ladderBaseline mirrors the TS makeBaseline of baselines-ladder.test.ts.
func ladderBaseline(baselineCommit, docPath string) schemas.Baseline {
	return schemas.Baseline{
		DocPath:        docPath,
		DocContentHash: "sha256:aaa",
		BaselineCommit: baselineCommit,
		SourceFiles:    []string{"src/payments/refund.ts"},
		SourceSymbols:  []string{},
		ClaimIDs:       []string{},
		VerifiedAt:     "2026-10-05T00:00:00Z",
		VerifiedByRun:  "run_1",
		PolicyVersion:  1,
	}
}

func TestD12LadderRungs_Rung1_ExactSHA(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "docs/a.md", "a\n")
	sha := repo.commitAll(t, "only")
	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(sha, "docs/payments.md")),
		Head:     sha,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunExactSHA, resolution.Run)
	require.True(t, resolution.BaselineValid)
	require.Equal(t, fmt.Sprintf("baseline commit %s is exactly HEAD", sha), resolution.Evidence)
}

func TestD12LadderRungs_Rung2_SameTreeOnAmend(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\n")
	repo.commitAll(t, "base")
	repo.write(t, "src/b.ts", "two\n")
	original := repo.commitAll(t, "original")
	// Force-push shape: amend, same tree, different commit object.
	repo.gitIdentity(t, "commit", "-q", "--amend", "-m", "amended")
	amended := strings.TrimSpace(repo.git(t, "rev-parse", "HEAD"))
	require.NotEqual(t, original, amended)

	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(original, "docs/payments.md")),
		Head:     amended,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunSameTree, resolution.Run)
	require.True(t, resolution.BaselineValid)
}

func TestD12LadderRungs_Rung3a_Ancestor(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "docs/a.md", "a\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "changed\n")
	head := repo.commitAll(t, "drift")
	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(base, "docs/payments.md")),
		Head:     head,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunAncestor, resolution.Run)
	require.True(t, resolution.BaselineValid)
}

func TestD12LadderRungs_Rung3b_PatchIDMatch(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "base")
	// Feature branch: the change the baseline was verified against.
	repo.git(t, "checkout", "-q", "-b", "feature")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feat: alpha -> beta")

	// Main moves on (different tree), then the SAME textual change re-lands
	// there (cherry-pick shape): not an ancestor, different tree, same patch.
	repo.git(t, "checkout", "-q", "main")
	repo.write(t, "src/other.ts", "other\n")
	repo.commitAll(t, "unrelated work")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	head := repo.commitAll(t, "re-land the same change")

	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunPatchIDMatch, resolution.Run)
	require.True(t, resolution.BaselineValid)
}

func TestD12LadderRungs_WalkDepth_MatchBeyondCapStaysOrphaned(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "base")
	// Feature branch: the baseline's change.
	repo.git(t, "checkout", "-q", "-b", "feature")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feat")

	// Main: re-land the change, then two unrelated commits push the match
	// three commits deep into head's walk.
	repo.git(t, "checkout", "-q", "main")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	repo.commitAll(t, "re-land the same change")
	repo.write(t, "src/filler1.ts", "f1\n")
	repo.commitAll(t, "filler 1")
	repo.write(t, "src/filler2.ts", "f2\n")
	head := repo.commitAll(t, "filler 2")

	deep, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline:    ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:        head,
		Git:         repo.client(t),
		SearchDepth: intptr(2),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunOrphanedSuspect, deep.Run)
	require.False(t, deep.BaselineValid)
	require.Contains(t, deep.Evidence, "no matching patch-id within depth 2")

	// The default depth (100) walks further and finds it.
	defaultDepth, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunPatchIDMatch, defaultDepth.Run)
	require.True(t, defaultDepth.BaselineValid)
	require.Contains(t, defaultDepth.Evidence, "walked 4 commits, depth 100")
}

func TestClampSearchDepth(t *testing.T) {
	require.Equal(t, 100, baselines.ClampSearchDepth(nil))
	require.Equal(t, 7, baselines.ClampSearchDepth(intptr(7)))
	require.Equal(t, 500, baselines.ClampSearchDepth(intptr(1000)))
	require.Equal(t, 1, baselines.ClampSearchDepth(intptr(0)))
	require.Equal(t, 500, baselines.MaxSearchDepth)
	require.Equal(t, 100, baselines.DefaultSearchDepth)
}

func ptrBaseline(baseline schemas.Baseline) *schemas.Baseline { return &baseline }

/* --------------------------------- V-P1-9 squash-merge simulation suite */

func TestVP1_9_a_ForcePushAmendSameTree_ResolvedViaSameTree(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "v1\n")
	repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "v2\n")
	baselineCommit := repo.commitAll(t, "feature")
	// Force-push shape: amend with identical content — new commit, same tree.
	repo.gitIdentity(t, "commit", "-q", "--amend", "-m", "feature (amended)")
	head := strings.TrimSpace(repo.git(t, "rev-parse", "HEAD"))
	require.NotEqual(t, baselineCommit, head)

	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunSameTree, resolution.Run)
	require.True(t, resolution.BaselineValid)
}

func TestVP1_9_b_SquashOfTwoCommitsIntoOne_TreeAndAncestorRungs(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\n")
	mainBase := repo.commitAll(t, "main base")

	// Feature branch: two commits on top of main.
	repo.git(t, "checkout", "-q", "-b", "feature")
	repo.write(t, "src/a.ts", "one\ntwo\n")
	repo.commitAll(t, "feat part 1")
	repo.write(t, "src/a.ts", "one\ntwo\nthree\n")
	featureTip := repo.commitAll(t, "feat part 2")

	// Squash merge: ONE new commit on main with the same final tree.
	repo.git(t, "checkout", "-q", "main")
	repo.git(t, "checkout", "-q", "-b", "squashed", mainBase)
	repo.write(t, "src/a.ts", "one\ntwo\nthree\n")
	squashed := repo.commitAll(t, "feature (squashed)")

	// The squashed commit shares main's history with the pre-feature base...
	ancestor, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(mainBase, "docs/payments.md")),
		Head:     squashed,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunAncestor, ancestor.Run)
	require.True(t, ancestor.BaselineValid)

	// ...and the feature TIP baseline resolves via the identical final tree.
	sameTree, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(featureTip, "docs/payments.md")),
		Head:     squashed,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunSameTree, sameTree.Run)
	require.True(t, sameTree.BaselineValid)
}

func TestVP1_9_c_ForcePushWithDifferentContent_OrphanedSuspect(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feature")
	// Force-push: amend with DIFFERENT content (history rewritten, new world).
	repo.write(t, "src/a.ts", "one\ngamma\nthree\nx\n")
	repo.gitIdentity(t, "add", "-A")
	repo.gitIdentity(t, "commit", "-q", "--amend", "-m", "feature (rewritten)")
	head := strings.TrimSpace(repo.git(t, "rev-parse", "HEAD"))

	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunOrphanedSuspect, resolution.Run)
	require.False(t, resolution.BaselineValid)
	require.Contains(t, resolution.Evidence, "no matching patch-id within depth 100")
}

func TestVP1_9_c2_UnrelatedHistories_OrphanedSuspect(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "one\nalpha\nthree\n")
	repo.commitAll(t, "main root")
	repo.write(t, "src/a.ts", "one\nbeta\nthree\n")
	baselineCommit := repo.commitAll(t, "feature")
	// Genuinely unrelated history: orphan branch with an independent root.
	repo.git(t, "checkout", "-q", "--orphan", "isolated")
	repo.git(t, "rm", "-q", "-rf", ".")
	repo.write(t, "other.ts", "independent\n")
	head := repo.commitAll(t, "isolated root")

	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunOrphanedSuspect, resolution.Run)
	require.False(t, resolution.BaselineValid)
}

func TestVP1_9_RootCommitBaselineWithNoComparablePatch_OrphanedSuspect(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "a\n")
	baselineCommit := repo.commitAll(t, "main root")
	// Independent root as head: merge-base empty, baseline has no parent.
	repo.git(t, "checkout", "-q", "--orphan", "orphan-root")
	repo.git(t, "rm", "-q", "-rf", ".")
	repo.write(t, "x.ts", "x\n")
	head := repo.commitAll(t, "orphan root")

	resolution, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(baselineCommit, "docs/payments.md")),
		Head:     head,
		Git:      repo.client(t),
	})
	require.NoError(t, err)
	require.Equal(t, baselines.RunOrphanedSuspect, resolution.Run)
	require.False(t, resolution.BaselineValid)
	require.Contains(t, resolution.Evidence, "its own patch is not computable (no parent or empty diff)")
}

/* ------------------------------------------- ladder failure semantics (D14) */

// failingGitExecutor is the injected outage: every git run fails with exit 128
// (TS `failing` executor).
type failingGitExecutor struct{}

func (failingGitExecutor) Run(_ context.Context, _ []string, _ *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	exitCode := 128
	return gitx.GitRunResult{
		ExitCode: &exitCode,
		Signal:   "",
		TimedOut: false,
		Stdout:   []byte{},
		Stderr:   []byte("fatal: simulated git outage\n"),
	}, nil
}

func TestLadderFailureSemantics_GitFailuresPropagateAsGitExecError(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "a\n")
	sha := repo.commitAll(t, "only")
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root, Executor: failingGitExecutor{}})
	require.NoError(t, err)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(sha, "docs/payments.md")),
		Head:     sha,
		Git:      client,
	})
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.True(t, errors.As(err, &gitErr), "want *gitx.GitExecError, got %T: %v", err, err)
	require.True(t, gitErr.Kind == gitx.KindUnknownRef || gitErr.Kind == gitx.KindExecFailure,
		"unexpected git error kind %q", gitErr.Kind)
	require.Equal(t, docutil.CodeGit, docutil.CodeOf(err))
}

func TestResolveBaseline_NilBaselineAndNilGit_FailClosed(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "a\n")
	sha := repo.commitAll(t, "only")

	_, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: nil,
		Head:     sha,
		Git:      repo.client(t),
	})
	require.Error(t, err)
	require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(sha, "docs/payments.md")),
		Head:     sha,
		Git:      nil,
	})
	require.Error(t, err)
	require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
}

// TestStoreBackendSurfaceParity is the compile-time parity check the TS suite
// kept in fakes.test.ts: the real state store and the in-memory fake satisfy
// the very same backend contract.
func TestStoreBackendSurfaceParity(t *testing.T) {
	real, err := state.NewStateStore(state.StateStoreOptions{StateDir: makeStateDir(t)})
	require.NoError(t, err)
	var _ baselines.BaselineStoreBackend = real
	var _ baselines.BaselineStoreBackend = newInMemoryBaselineBackend()
	assert.NotNil(t, real)
}

/* --------------------------------------------- fail-closed propagation tests */

// erroringBackend is the error/result-injection seam: the store's backend
// errors and CAS-conflict shapes must propagate exactly as the real contract
// reports them (D14: never swallowed).
type erroringBackend struct {
	read  func(docPath string) (state.StateRead[*schemas.Baseline], error)
	write func(docPath string, baseline schemas.Baseline, expectedHash *string) (state.CasWriteResult, error)
}

func (e *erroringBackend) ReadBaseline(docPath string) (state.StateRead[*schemas.Baseline], error) {
	if e.read == nil {
		return state.StateRead[*schemas.Baseline]{Exists: false}, nil
	}
	return e.read(docPath)
}

func (e *erroringBackend) WriteBaseline(
	docPath string,
	baseline schemas.Baseline,
	expectedHash *string,
) (state.CasWriteResult, error) {
	if e.write == nil {
		return state.CasWriteResult{OK: true, Hash: "sha256:written"}, nil
	}
	return e.write(docPath, baseline, expectedHash)
}

func TestBaselineStore_BackendErrorsPropagate(t *testing.T) {
	boom := errors.New("backend read failed")
	store := baselines.NewBaselineStore(&erroringBackend{
		read: func(string) (state.StateRead[*schemas.Baseline], error) {
			return state.StateRead[*schemas.Baseline]{}, boom
		},
	})
	_, err := store.Get("docs/payments.md")
	require.ErrorIs(t, err, boom)
	_, err = store.Upsert(baselineExample, guards())
	require.ErrorIs(t, err, boom)

	writeBoom := errors.New("backend write failed")
	store = baselines.NewBaselineStore(&erroringBackend{
		write: func(string, schemas.Baseline, *string) (state.CasWriteResult, error) {
			return state.CasWriteResult{}, writeBoom
		},
	})
	_, err = store.Upsert(baselineExample, guards())
	require.ErrorIs(t, err, writeBoom)
}

func TestBaselineStore_CreatePathConflictReportsAbsentOnDisk(t *testing.T) {
	// The create branch's CAS conflict: the record appeared between the
	// store's read and its write, so nothing exists to compare against.
	expected := "sha256:gone"
	store := baselines.NewBaselineStore(&erroringBackend{
		write: func(string, schemas.Baseline, *string) (state.CasWriteResult, error) {
			return state.CasWriteResult{OK: false, ExpectedHash: &expected, ActualHash: nil}, nil
		},
	})
	upsert, err := store.Upsert(baselineExample, guards())
	require.NoError(t, err)
	require.False(t, upsert.OK)
	require.Equal(t, baselines.ConflictCasConflict, upsert.Kind)
	require.Equal(t,
		"CAS conflict: expected sha256:gone, on disk absent — loser re-runs, winner's bytes survive (R15)",
		upsert.Detail)
}

func TestBaselineStore_UnencodableDocPathFailsClosed(t *testing.T) {
	store := baselines.NewBaselineStore(makeStateStore(t))
	_, err := store.Get("../escape.md")
	require.Error(t, err)
	require.Equal(t, state.CodeStateKey, docutil.CodeOf(err))

	_, err = store.Upsert(makeBaseline(func(b *schemas.Baseline) { b.DocPath = "../escape.md" }), guards())
	require.Error(t, err)
	require.Equal(t, state.CodeStateKey, docutil.CodeOf(err))
}

func TestEvaluateInvalidation_GuardedGlobSourceFailsClosed(t *testing.T) {
	// A glob source that fails the ARCH §11.3 escape guards is a hard error:
	// the engine must never degrade it to "no match" (fail closed, D8).
	baseline := makeBaseline(func(b *schemas.Baseline) {
		b.SourceFiles = []string{".docsync/state/**"}
	})
	_, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:     &baseline,
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: ".docsync/state/x.json", Status: baselines.ChangeModified}},
	})
	require.Error(t, err)
	var guardErr *globs.GlobGuardError
	require.True(t, errors.As(err, &guardErr), "want *globs.GlobGuardError, got %T: %v", err, err)
	require.Equal(t, globs.GuardDocsyncInternal, guardErr.Kind)

	// The intersection stage alone raises the same hard error.
	_, err = baselines.EvaluateSourceIntersection(baselines.EvaluateSourceIntersectionInput{
		Sources:      []string{".docsync/state/**"},
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: ".docsync/state/x.json", Status: baselines.ChangeAdded}},
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &guardErr))
}

func TestEvaluateInvalidation_NilBaselinePinsNoSources(t *testing.T) {
	// A nil record carries no source files: nothing to intersect, never
	// invalidated (the store never hands out a nil baseline that Exists).
	result, err := baselines.EvaluateInvalidation(baselines.EvaluateInvalidationInput{
		Baseline:     nil,
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: "src/a.ts", Status: baselines.ChangeModified}},
	})
	require.NoError(t, err)
	require.False(t, result.Invalidated)
	require.Empty(t, result.Reasons)

	require.Nil(t, baselines.BaselineOrphanSignals(baselines.BaselineOrphanSignalsInput{
		Sources:      nil,
		ChangedFiles: []baselines.InvalidationChangedFile{{Path: "src/a.ts", Status: baselines.ChangeDeleted}},
	}))
}

func TestResolveBaseline_UnknownRefsPropagate(t *testing.T) {
	repo := newLadderRepo(t)
	repo.write(t, "src/a.ts", "a\n")
	sha := repo.commitAll(t, "only")
	client := repo.client(t)

	_, err := baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(strings.Repeat("0", 40), "docs/payments.md")),
		Head:     sha,
		Git:      client,
	})
	require.Error(t, err)
	var gitErr *gitx.GitExecError
	require.True(t, errors.As(err, &gitErr), "want *gitx.GitExecError, got %T: %v", err, err)
	require.Equal(t, gitx.KindUnknownRef, gitErr.Kind)

	_, err = baselines.ResolveBaseline(context.Background(), baselines.ResolveBaselineInput{
		Baseline: ptrBaseline(ladderBaseline(sha, "docs/payments.md")),
		Head:     "refs/heads/does-not-exist",
		Git:      client,
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &gitErr))
}
