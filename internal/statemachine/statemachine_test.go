package statemachine_test

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/statemachine"
)

// Ported from packages/core/test/statemachine.test.ts (doc state machine
// tests, P1-T17) and packages/core/test/statemachine-suspect.test.ts
// (deterministic SUSPECT driver tests, P1-T17 wiring for V-P1-2/V-P1-6;
// OD-R38 rulings). Both suites run over the REAL StateStore (temp state
// dirs) and — for the machine tests — an in-memory backend with the same CAS
// semantics (V-P0-11 CAS parity); the suspect suite uses real system git in
// fresh temp repos plus the real StateStore.

/* ------------------------------------------------------------- helpers */

const (
	smDocPath  = "docs/payments.md"
	smRunID    = "run_01J9ZK6X8A"
	smAt       = "2026-10-05T00:00:00Z"
	smPolicy   = 17
	suspectDoc = "docs/payments.md"
	suspectSrc = "src/payments/refund.ts"
)

func boolPtr(b bool) *bool { return &b }

func strPtr(s string) *string { return &s }

// inMemoryStateStore is the Go twin of the TS createInMemoryStateStore fake:
// the same compare-and-swap semantics as the real store (conflict shapes,
// create-vs-overwrite, hash of the stored bytes), keyed by doc path.
type inMemoryStateStore struct {
	mu    sync.Mutex
	files map[string]string
}

func newInMemoryStateStore() *inMemoryStateStore {
	return &inMemoryStateStore{files: make(map[string]string)}
}

func (s *inMemoryStateStore) ReadDocState(docPath string) (state.StateRead[*schemas.DocState], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.files[docPath]
	if !ok {
		return state.StateRead[*schemas.DocState]{}, nil
	}
	var docState schemas.DocState
	if err := json.Unmarshal([]byte(raw), &docState); err != nil {
		return state.StateRead[*schemas.DocState]{}, err
	}
	return state.StateRead[*schemas.DocState]{
		Content: &docState,
		Raw:     raw,
		Hash:    docutil.ContentHash(raw),
		Exists:  true,
	}, nil
}

func (s *inMemoryStateStore) WriteDocState(docPath string, docState schemas.DocState, expectedHash *string) (state.CasWriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wire, err := json.Marshal(docState)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(wire))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return state.CasWriteResult{}, err
	}
	raw, err := docutil.CanonicalJSON(generic)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	currentRaw, exists := s.files[docPath]
	if exists {
		currentHash := docutil.ContentHash(currentRaw)
		if expectedHash == nil || currentHash != *expectedHash {
			return state.CasWriteResult{OK: false, ExpectedHash: expectedHash, ActualHash: &currentHash}, nil
		}
	} else if expectedHash != nil {
		return state.CasWriteResult{OK: false, ExpectedHash: expectedHash, ActualHash: nil}, nil
	}
	s.files[docPath] = raw
	return state.CasWriteResult{OK: true, Hash: docutil.ContentHash(raw)}, nil
}

func (s *inMemoryStateStore) ListDocStatePaths() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.files))
	for docPath := range s.files {
		paths = append(paths, docPath)
	}
	sortStrings(paths)
	return paths, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

type backendKind string

const (
	backendStateStore backendKind = "StateStore"
	backendInMemory   backendKind = "InMemoryStateStore"
)

func backends() []backendKind {
	return []backendKind{backendStateStore, backendInMemory}
}

func makeBackend(t *testing.T, kind backendKind) statemachine.DocStateMachineBackend {
	t.Helper()
	switch kind {
	case backendStateStore:
		store, err := state.NewStateStore(state.StateStoreOptions{
			StateDir: filepath.Join(t.TempDir(), "state"),
		})
		require.NoError(t, err)
		return store
	case backendInMemory:
		return newInMemoryStateStore()
	}
	t.Fatalf("unknown backend kind %q", kind)
	return nil
}

func makeMachine(t *testing.T, kind backendKind) *statemachine.DocStateMachine {
	t.Helper()
	return statemachine.NewDocStateMachine(makeBackend(t, kind))
}

func hasReasonKind(reasons []baselines.InvalidationReason, kind baselines.InvalidationReasonKind) bool {
	for _, reason := range reasons {
		if reason.Kind == kind {
			return true
		}
	}
	return false
}

func reasonsOfKind(reasons []baselines.InvalidationReason, kind baselines.InvalidationReasonKind) []baselines.InvalidationReason {
	out := make([]baselines.InvalidationReason, 0)
	for _, reason := range reasons {
		if reason.Kind == kind {
			out = append(out, reason)
		}
	}
	return out
}

func ownerOf(t *testing.T, from, to schemas.DocStateName) statemachine.TransitionOwner {
	t.Helper()
	owner, ok := statemachine.TransitionOwnerOf(from, to)
	require.True(t, ok, "expected an owner for %s -> %s", from, to)
	return owner
}

/* ------------------------------------------- §6.2 transition table (P1-T17) */

func TestTransitionTableLegalPairsHold(t *testing.T) {
	assert.Equal(t, []schemas.DocStateName{schemas.DocStateSuspect, schemas.DocStateClosed},
		statemachine.LegalTransitions[schemas.DocStateVerified])
	assert.Equal(t, []schemas.DocStateName{schemas.DocStateClear, schemas.DocStateReview, schemas.DocStateStale},
		statemachine.LegalTransitions[schemas.DocStateSuspect])
	assert.Equal(t, []schemas.DocStateName{schemas.DocStateProposed},
		statemachine.LegalTransitions[schemas.DocStateStale])
	assert.Equal(t, []schemas.DocStateName{schemas.DocStateVerified, schemas.DocStateRejected},
		statemachine.LegalTransitions[schemas.DocStateProposed])
	assert.Equal(t, []schemas.DocStateName{schemas.DocStateStale, schemas.DocStateClear, schemas.DocStateClosed},
		statemachine.LegalTransitions[schemas.DocStateReview])
	assert.Equal(t, []schemas.DocStateName{schemas.DocStateSuspect},
		statemachine.LegalTransitions[schemas.DocStateRejected])
	assert.Empty(t, statemachine.LegalTransitions[schemas.DocStateClosed])
	assert.Equal(t, []schemas.DocStateName{schemas.DocStateSuspect},
		statemachine.LegalTransitions[schemas.DocStateClear])

	for _, from := range schemas.DocStateNameValues {
		for _, to := range schemas.DocStateNameValues {
			expected := false
			for _, target := range statemachine.LegalTransitions[from] {
				if target == to {
					expected = true
					break
				}
			}
			assert.Equal(t, expected, statemachine.IsLegalTransition(from, to),
				"isLegalTransition(%s, %s)", from, to)
		}
	}
	// Unknown states are refused, never guessed (fail closed).
	assert.False(t, statemachine.IsLegalTransition(schemas.DocStateName("UNKNOWN"), schemas.DocStateSuspect))
}

func TestTransitionTableEveryLegalEdgeHasExactlyOneOwner(t *testing.T) {
	for _, from := range schemas.DocStateNameValues {
		for _, to := range statemachine.LegalTransitions[from] {
			key := statemachine.TransitionKey(from, to)
			owner, ok := statemachine.TransitionOwners[key]
			require.True(t, ok, "expected an owner entry for %s", key)
			assert.Equal(t, owner, ownerOf(t, from, to))
		}
	}
	assert.Equal(t, statemachine.TransitionOwnerDeterministic, ownerOf(t, schemas.DocStateVerified, schemas.DocStateSuspect))
	assert.Equal(t, statemachine.TransitionOwnerDeterministic, ownerOf(t, schemas.DocStateVerified, schemas.DocStateClosed))
	// OD-R38: new drift on a cleared/rejected doc is deterministic evidence —
	// the suspect driver re-routes it; REVIEW→CLOSED stays decision-owned.
	assert.Equal(t, statemachine.TransitionOwnerDeterministic, ownerOf(t, schemas.DocStateClear, schemas.DocStateSuspect))
	assert.Equal(t, statemachine.TransitionOwnerDeterministic, ownerOf(t, schemas.DocStateRejected, schemas.DocStateSuspect))
	assert.Equal(t, statemachine.TransitionOwnerDecision, ownerOf(t, schemas.DocStateReview, schemas.DocStateClosed))
	assert.Equal(t, statemachine.TransitionOwnerDecision, ownerOf(t, schemas.DocStateSuspect, schemas.DocStateClear))
	_, ok := statemachine.TransitionOwnerOf(schemas.DocStateClosed, schemas.DocStateVerified)
	assert.False(t, ok)
}

/* --------------------------------------------- DocStateMachine (P1-T17) */

func TestDocStateMachineInitializeCreatesFirstRecordSecondLosesCAS(t *testing.T) {
	for _, backend := range backends() {
		machine := makeMachine(t, backend)
		created, err := machine.Initialize(statemachine.InitializeStateInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateVerified,
			RunID:         smRunID,
			PolicyVersion: smPolicy,
			At:            smAt,
			EvidenceRefs:  []string{"manifest/v1"},
		})
		require.NoError(t, err)
		require.True(t, created.OK)
		require.NotNil(t, created.State)
		assert.Equal(t, "docs/payments.md", created.State.Path)
		assert.Equal(t, schemas.DocStateVerified, created.State.State)
		assert.Equal(t, smAt, created.State.Since)
		require.NotNil(t, created.State.LastRun)
		assert.Equal(t, smRunID, *created.State.LastRun)
		assert.Equal(t, smPolicy, created.State.PolicyVersion)
		assert.Equal(t, []string{"manifest/v1"}, created.State.EvidenceRefs)
		require.Len(t, created.State.Transitions, 1)
		assert.Equal(t, schemas.DocStateVerified, created.State.Transitions[0].State)
		assert.Equal(t, smAt, created.State.Transitions[0].At)
		assert.Equal(t, smRunID, created.State.Transitions[0].RunID)
		assert.Equal(t, "initial state VERIFIED recorded", created.State.Transitions[0].Reason)

		again, err := machine.Initialize(statemachine.InitializeStateInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateVerified,
			RunID:         smRunID,
			PolicyVersion: smPolicy,
			At:            smAt,
		})
		require.NoError(t, err)
		assert.False(t, again.OK)
		assert.Equal(t, statemachine.StateMutationKindCasConflict, again.Kind)
	}
}

func TestDocStateMachineAcceptsEveryLegalTransitionAndGrowsLog(t *testing.T) {
	for _, backend := range backends() {
		machine := makeMachine(t, backend)
		_, err := machine.Initialize(statemachine.InitializeStateInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateVerified,
			RunID:         smRunID,
			PolicyVersion: smPolicy,
			At:            smAt,
			Reason:        "bootstrap",
		})
		require.NoError(t, err)

		// Walk one reachable legal chain end-to-end.
		chain := []schemas.DocStateName{
			schemas.DocStateVerified, schemas.DocStateSuspect,
			schemas.DocStateStale, schemas.DocStateProposed, schemas.DocStateVerified,
		}
		hashes := make([]string, 0)
		priorTransitions := 1
		for index := 0; index < len(chain)-1; index++ {
			from, to := chain[index], chain[index+1]
			var decision *statemachine.TransitionDecision
			if ownerOf(t, from, to) == statemachine.TransitionOwnerDecision {
				decision = &statemachine.TransitionDecision{
					Source: statemachine.TransitionDecisionSourceJev,
					Ref:    "dec_1",
				}
			}
			result, err := machine.ApplyTransition(statemachine.ApplyTransitionInput{
				DocPath:       smDocPath,
				To:            to,
				RunID:         smRunID,
				Reason:        fmt.Sprintf("%s -> %s", from, to),
				PolicyVersion: smPolicy,
				At:            fmt.Sprintf("2026-10-05T00:0%d:00Z", index+1),
				Decision:      decision,
			})
			require.NoError(t, err)
			require.True(t, result.OK)
			require.NotNil(t, result.State)
			log := result.State.Transitions
			require.Len(t, log, priorTransitions+1)
			assert.Equal(t, from, log[priorTransitions-1].State)
			assert.Equal(t, to, log[priorTransitions].State)
			priorTransitions++
			hashes = append(hashes, result.Hash)
		}
		assert.Len(t, hashes, 4)
	}
}

func TestDocStateMachineRefusesIllegalTransitionsFailClosed(t *testing.T) {
	illegal := [][2]schemas.DocStateName{
		{schemas.DocStateVerified, schemas.DocStateProposed},
		{schemas.DocStateVerified, schemas.DocStateReview},
		{schemas.DocStateClosed, schemas.DocStateVerified},
		{schemas.DocStateSuspect, schemas.DocStateSuspect},
		{schemas.DocStateVerified, schemas.DocStateVerified},
	}
	for _, backend := range backends() {
		for _, pair := range illegal {
			from, to := pair[0], pair[1]
			machine := makeMachine(t, backend)
			docPath := fmt.Sprintf("docs/%s-%s.md", strings.ToLower(string(from)), strings.ToLower(string(to)))
			_, err := machine.Initialize(statemachine.InitializeStateInput{
				DocPath:       docPath,
				To:            from,
				RunID:         "init-" + string(from),
				PolicyVersion: smPolicy,
				At:            smAt,
			})
			require.NoError(t, err)
			before, err := machine.GetState(docPath)
			require.NoError(t, err)
			_, err = machine.ApplyTransition(statemachine.ApplyTransitionInput{
				DocPath:       docPath,
				To:            to,
				RunID:         smRunID,
				Reason:        "nope",
				PolicyVersion: smPolicy,
				At:            smAt,
			})
			require.Error(t, err)
			var illegalErr *statemachine.IllegalTransitionError
			require.True(t, errors.As(err, &illegalErr), "expected IllegalTransitionError, got %v", err)
			assert.Equal(t, from, illegalErr.From)
			assert.Equal(t, to, illegalErr.To)
			assert.Equal(t, "EDOCSYNC_STATE_MACHINE", docutil.CodeOf(err))
			after, err := machine.GetState(docPath)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		}
	}
}

func TestDocStateMachineClosedIsTerminal(t *testing.T) {
	machine := makeMachine(t, backendInMemory)
	_, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateClosed,
		RunID:         smRunID,
		PolicyVersion: smPolicy,
		At:            smAt,
	})
	require.NoError(t, err)
	for _, to := range schemas.DocStateNameValues {
		_, err := machine.ApplyTransition(statemachine.ApplyTransitionInput{
			DocPath:       smDocPath,
			To:            to,
			RunID:         smRunID,
			Reason:        "nope",
			PolicyVersion: smPolicy,
			At:            smAt,
		})
		require.Error(t, err)
		var illegalErr *statemachine.IllegalTransitionError
		require.True(t, errors.As(err, &illegalErr), "expected IllegalTransitionError for CLOSED -> %s", to)
	}
}

func TestDocStateMachineDecisionOwnedTransitions(t *testing.T) {
	for _, backend := range backends() {
		machine := makeMachine(t, backend)
		_, err := machine.Initialize(statemachine.InitializeStateInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateSuspect,
			RunID:         smRunID,
			PolicyVersion: smPolicy,
			At:            smAt,
		})
		require.NoError(t, err)

		_, err = machine.ApplyTransition(statemachine.ApplyTransitionInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateClear,
			RunID:         smRunID,
			Reason:        "looks fine",
			PolicyVersion: smPolicy,
			At:            smAt,
		})
		require.Error(t, err)
		var decisionErr *statemachine.DecisionRequiredError
		require.True(t, errors.As(err, &decisionErr), "expected DecisionRequiredError, got %v", err)
		assert.Equal(t, schemas.DocStateSuspect, decisionErr.From)
		assert.Equal(t, schemas.DocStateClear, decisionErr.To)
		assert.Equal(t, "EDOCSYNC_STATE_MACHINE", docutil.CodeOf(err))

		decided, err := machine.ApplyTransition(statemachine.ApplyTransitionInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateClear,
			RunID:         smRunID,
			Reason:        "looks fine",
			PolicyVersion: smPolicy,
			At:            smAt,
			Decision: &statemachine.TransitionDecision{
				Source: statemachine.TransitionDecisionSourceJev,
				Ref:    "dec_42",
			},
		})
		require.NoError(t, err)
		require.True(t, decided.OK)
		require.NotNil(t, decided.State)
		last := decided.State.Transitions[len(decided.State.Transitions)-1]
		assert.Contains(t, last.Reason, "(decision: jev:dec_42)")
		assert.Contains(t, decided.State.EvidenceRefs, "decision:jev:dec_42")

		// Deterministic edges never require a decision.
		machine2 := makeMachine(t, backend)
		_, err = machine2.Initialize(statemachine.InitializeStateInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateVerified,
			RunID:         smRunID,
			PolicyVersion: smPolicy,
			At:            smAt,
		})
		require.NoError(t, err)
		plain, err := machine2.ApplyTransition(statemachine.ApplyTransitionInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateSuspect,
			RunID:         smRunID,
			Reason:        "mapped source changed",
			PolicyVersion: smPolicy,
			At:            smAt,
		})
		require.NoError(t, err)
		assert.True(t, plain.OK)
	}
}

func TestDocStateMachineTransitionWithoutRecordFailsClosed(t *testing.T) {
	machine := makeMachine(t, backendInMemory)
	_, err := machine.ApplyTransition(statemachine.ApplyTransitionInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateSuspect,
		RunID:         smRunID,
		Reason:        "drift",
		PolicyVersion: smPolicy,
		At:            smAt,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no state record exists")
}

func TestDocStateMachineVP11CASRace(t *testing.T) {
	for _, backend := range backends() {
		store := makeBackend(t, backend)
		machine := statemachine.NewDocStateMachine(store)
		_, err := machine.Initialize(statemachine.InitializeStateInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateVerified,
			RunID:         smRunID,
			PolicyVersion: smPolicy,
			At:            smAt,
			Reason:        "bootstrap",
		})
		require.NoError(t, err)

		// Both writers read the VERIFIED record (hash h0); writer A wins first.
		winner, err := machine.ApplyTransition(statemachine.ApplyTransitionInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateSuspect,
			RunID:         smRunID,
			Reason:        "mapped source changed",
			PolicyVersion: smPolicy,
			At:            smAt,
			EvidenceRefs:  []string{"analysis/run_A"},
		})
		require.NoError(t, err)
		require.True(t, winner.OK)
		stale, err := store.ReadDocState(smDocPath)
		require.NoError(t, err)
		require.True(t, stale.Exists)
		staleHash := stale.Hash

		// Writer B (loser, stale hash, stale state view): the machine evaluates
		// against the FRESH record — SUSPECT→SUSPECT is not an edge, so the
		// re-run fails CLOSED instead of appending a corrupt duplicate.
		_, err = machine.ApplyTransition(statemachine.ApplyTransitionInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateSuspect,
			RunID:         smRunID,
			Reason:        "mapped source changed (stale view)",
			PolicyVersion: smPolicy,
			At:            smAt,
			ExpectedHash:  &staleHash,
		})
		require.Error(t, err)
		var illegalErr *statemachine.IllegalTransitionError
		require.True(t, errors.As(err, &illegalErr), "expected IllegalTransitionError, got %v", err)

		// The winner's record is intact: one appended transition, not two.
		after, err := machine.GetState(smDocPath)
		require.NoError(t, err)
		require.NotNil(t, after)
		assert.Equal(t, schemas.DocStateSuspect, after.State)
		require.Len(t, after.Transitions, 2)
		assert.Equal(t, []string{"bootstrap", "mapped source changed"},
			[]string{after.Transitions[0].Reason, after.Transitions[1].Reason})
	}
}

func TestDocStateMachineCASConflictShape(t *testing.T) {
	for _, backend := range backends() {
		store := makeBackend(t, backend)
		machine := statemachine.NewDocStateMachine(store)
		_, err := machine.Initialize(statemachine.InitializeStateInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateVerified,
			RunID:         smRunID,
			PolicyVersion: smPolicy,
			At:            smAt,
			Reason:        "bootstrap",
		})
		require.NoError(t, err)
		actual, err := store.ReadDocState(smDocPath)
		require.NoError(t, err)
		require.True(t, actual.Exists)

		// A concurrent writer touched the bytes (same state string): the next
		// transition conditioned on a stale hash still loses gracefully.
		stale := strings.Repeat("e", 64)
		if stale == actual.Hash {
			stale = strings.Repeat("d", 64)
		}
		result, err := machine.ApplyTransition(statemachine.ApplyTransitionInput{
			DocPath:       smDocPath,
			To:            schemas.DocStateSuspect,
			RunID:         smRunID,
			Reason:        "mapped source changed",
			PolicyVersion: smPolicy,
			At:            smAt,
			ExpectedHash:  &stale,
		})
		require.NoError(t, err)
		assert.False(t, result.OK)
		assert.Equal(t, statemachine.StateMutationKindCasConflict, result.Kind)
		require.NotNil(t, result.ExpectedHash)
		assert.Equal(t, stale, *result.ExpectedHash)
		require.NotNil(t, result.ActualHash)
		assert.Equal(t, actual.Hash, *result.ActualHash)
		// Nothing was written: the record is byte-identical.
		afterRead, err := store.ReadDocState(smDocPath)
		require.NoError(t, err)
		assert.Equal(t, actual.Raw, afterRead.Raw)
	}
}

func TestDocStateMachineGetStateListStatesDeterministic(t *testing.T) {
	store, err := state.NewStateStore(state.StateStoreOptions{
		StateDir: filepath.Join(t.TempDir(), "state"),
	})
	require.NoError(t, err)
	machine := statemachine.NewDocStateMachine(store)

	missing, err := machine.GetState("docs/missing.md")
	require.NoError(t, err)
	assert.Nil(t, missing)

	_, err = machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateVerified,
		RunID:         smRunID,
		PolicyVersion: smPolicy,
		At:            smAt,
	})
	require.NoError(t, err)
	_, err = machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       "docs/nested/deep_doc.md",
		To:            schemas.DocStateSuspect,
		RunID:         smRunID,
		PolicyVersion: smPolicy,
		At:            smAt,
	})
	require.NoError(t, err)

	states, err := machine.ListStates()
	require.NoError(t, err)
	require.Len(t, states, 2)
	// Sorted by doc path (deterministic) — flattened paths decode back.
	assert.Equal(t, "docs/nested/deep_doc.md", states[0].Path)
	assert.Equal(t, "docs/payments.md", states[1].Path)
	assert.Equal(t, schemas.DocStateSuspect, states[0].State)
	assert.Equal(t, schemas.DocStateVerified, states[1].State)
}

func TestDocStateMachineEvidenceRefsMergeUniquely(t *testing.T) {
	machine := makeMachine(t, backendInMemory)
	_, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateVerified,
		RunID:         smRunID,
		PolicyVersion: smPolicy,
		At:            smAt,
		EvidenceRefs:  []string{"manifest/v1", "analysis/run_0"},
	})
	require.NoError(t, err)
	result, err := machine.ApplyTransition(statemachine.ApplyTransitionInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateSuspect,
		RunID:         smRunID,
		Reason:        "drift",
		PolicyVersion: smPolicy,
		At:            smAt,
		EvidenceRefs:  []string{"analysis/run_0", "analysis/run_1"},
	})
	require.NoError(t, err)
	require.True(t, result.OK)
	require.NotNil(t, result.State)
	assert.Equal(t, []string{"manifest/v1", "analysis/run_0", "analysis/run_1"}, result.State.EvidenceRefs)
}

/* ------------------------------------- markSuspectsFromAnalysis (P1-T17) */

const (
	suspectRunID         = "run_01J9ZK6X8A"
	suspectPolicyVersion = 17
	suspectAt            = "2026-10-05T12:00:00Z"
	suspectHead          = "HEAD"
	suspectAnalysisRunID = "run_acme#m0..head"
)

type suspectFixture struct {
	root       string
	stateStore *state.StateStore
	git        *gitx.GitClient
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func newSuspectFixture(t *testing.T) *suspectFixture {
	t.Helper()
	root := t.TempDir()
	// Windows platform-port (DX-P11-3): repo-local line-ending policy — the
	// machine-level system gitconfig carries core.autocrlf=true, which would
	// rewrite fresh fixture/checkout files to CRLF and surface them as ` M` in
	// worktree assertions. Pin LF in-repo before any file is written.
	initCmd := exec.Command("git", "init", "-q", "-b", "main", root)
	initCmd.Env = gitEnv()
	initOut, err := initCmd.CombinedOutput()
	require.NoError(t, err, "git init: %s", initOut)
	runGit(t, root, "config", "core.autocrlf", "false")
	runGit(t, root, "config", "core.eol", "lf")
	stateDir := filepath.Join(root, ".docsync", "state")
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	return &suspectFixture{root: root, stateStore: store, git: git}
}

func (f *suspectFixture) write(t *testing.T, relPath, content string) {
	t.Helper()
	target := filepath.Join(f.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func (f *suspectFixture) remove(t *testing.T, relPath string) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(f.root, relPath)))
}

var gitIdentity = []string{"-c", "user.name=docsync-test", "-c", "user.email=docsync-test@invalid"}

func (f *suspectFixture) commitAll(t *testing.T, message string) string {
	t.Helper()
	runGit(t, f.root, append(append([]string{}, gitIdentity...), "add", "-A")...)
	runGit(t, f.root, append(append([]string{}, gitIdentity...), "commit", "-q", "-m", message)...)
	return runGit(t, f.root, "rev-parse", "HEAD")
}

func (f *suspectFixture) amendAll(t *testing.T, message string) string {
	t.Helper()
	runGit(t, f.root, append(append([]string{}, gitIdentity...), "add", "-A")...)
	runGit(t, f.root, append(append([]string{}, gitIdentity...), "commit", "-q", "--amend", "-m", message)...)
	return runGit(t, f.root, "rev-parse", "HEAD")
}

func makeGraph(errs ...graph.GraphConfigError) graph.DocumentGraph {
	return graph.DocumentGraph{
		Mappings: []graph.MappingRecord{{
			Doc:       suspectDoc,
			Sources:   []string{suspectSrc},
			Concepts:  []string{},
			Contracts: []string{},
		}},
		ContractKinds: map[string]schemas.ContractKind{},
		Errors:        errs,
	}
}

func makeAnalysis(files []schemas.ChangedFile, hints ...schemas.ClassificationHint) schemas.ChangeAnalysis {
	return schemas.ChangeAnalysis{
		Schema:              schemas.ChangeAnalysisSchemaID,
		RunID:               suspectAnalysisRunID,
		Repo:                "acme/payments",
		BaseSHA:             "0000000000000000000000000000000000000000",
		HeadSHA:             "1111111111111111111111111111111111111111",
		Files:               files,
		DependenciesChanged: false,
		ClassificationHints: hints,
		DeepAnalysis:        schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: strPtr("stub analyzers in V1")},
		AnalyzerCoverage:    []schemas.AnalyzerCoverage{{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK}},
	}
}

func makeBaseline(baselineCommit string) schemas.Baseline {
	return schemas.Baseline{
		DocPath:        suspectDoc,
		DocContentHash: "sha256:docv1",
		BaselineCommit: baselineCommit,
		SourceFiles:    []string{suspectSrc},
		SourceSymbols:  []string{},
		ClaimIDs:       []string{},
		VerifiedAt:     "2026-10-05T00:00:00Z",
		VerifiedByRun:  "run_0",
		PolicyVersion:  17,
	}
}

func suspectInput(fixture *suspectFixture, docGraph graph.DocumentGraph, analysis schemas.ChangeAnalysis, head string) statemachine.SuspectDriverInput {
	if head == "" {
		head = suspectHead
	}
	return statemachine.SuspectDriverInput{
		Store:         fixture.stateStore,
		Graph:         docGraph,
		Analysis:      analysis,
		Git:           fixture.git,
		Head:          head,
		RunID:         suspectRunID,
		PolicyVersion: suspectPolicyVersion,
		At:            suspectAt,
	}
}

func seedDocState(t *testing.T, fixture *suspectFixture, docState schemas.DocStateName) {
	t.Helper()
	machine := statemachine.NewDocStateMachine(fixture.stateStore)
	initialized, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       suspectDoc,
		To:            docState,
		RunID:         "run_0",
		PolicyVersion: 17,
		At:            "2026-10-04T00:00:00Z",
	})
	require.NoError(t, err)
	require.True(t, initialized.OK, "initialize failed")
}

type baselineUpsertOutcome struct {
	ok      bool
	hash    string
	created bool
	kind    string
	detail  string
}

func describeCasWrite(write state.CasWriteResult) string {
	expected, actual := "absent", "absent"
	if write.ExpectedHash != nil {
		expected = *write.ExpectedHash
	}
	if write.ActualHash != nil {
		actual = *write.ActualHash
	}
	return fmt.Sprintf(
		"CAS conflict: expected %s, on disk %s — loser re-runs, winner's bytes survive (R15)",
		expected, actual)
}

// upsertBaseline ports the seeding half of core/src/baselines/store.ts
// (BaselineStore.upsert §6.1 CAS guards) over the real StateStore baseline
// APIs — the internal/baselines package is not landed yet, and the driver
// only READS baselines.
func upsertBaseline(
	t *testing.T,
	fixture *suspectFixture,
	baseline schemas.Baseline,
	expectedDocContentHash, expectedBaselineCommit string,
) baselineUpsertOutcome {
	t.Helper()
	docPath := baseline.DocPath
	current, err := fixture.stateStore.ReadBaseline(docPath)
	require.NoError(t, err)
	if !current.Exists || current.Content == nil {
		write, err := fixture.stateStore.WriteBaseline(docPath, baseline, nil)
		require.NoError(t, err)
		if write.OK {
			return baselineUpsertOutcome{ok: true, hash: write.Hash, created: true}
		}
		return baselineUpsertOutcome{ok: false, kind: "cas-conflict", detail: describeCasWrite(write)}
	}
	if current.Content.DocPath != docPath {
		t.Fatalf("baseline file for %q stores a record for %q — refusing to overwrite a different doc (fail closed)",
			docPath, current.Content.DocPath)
	}
	if current.Content.DocContentHash != expectedDocContentHash {
		return baselineUpsertOutcome{
			ok:   false,
			kind: "doc-content-changed",
			detail: fmt.Sprintf(
				"stored doc_content_hash %s != run-start %s — re-run claim evaluation (R15)",
				current.Content.DocContentHash, expectedDocContentHash),
		}
	}
	if current.Content.BaselineCommit != expectedBaselineCommit {
		return baselineUpsertOutcome{
			ok:   false,
			kind: "baseline-commit-moved",
			detail: fmt.Sprintf("stored baseline_commit %s != run-start %s",
				current.Content.BaselineCommit, expectedBaselineCommit),
		}
	}
	currentHash := current.Hash
	write, err := fixture.stateStore.WriteBaseline(docPath, baseline, &currentHash)
	require.NoError(t, err)
	if write.OK {
		return baselineUpsertOutcome{ok: true, hash: write.Hash, created: false}
	}
	return baselineUpsertOutcome{ok: false, kind: "cas-conflict", detail: describeCasWrite(write)}
}

func seedVerifiedDoc(t *testing.T, fixture *suspectFixture, baselineCommit string, docState schemas.DocStateName) {
	t.Helper()
	seedDocState(t, fixture, docState)
	upsert := upsertBaseline(t, fixture, makeBaseline(baselineCommit), "sha256:docv1", baselineCommit)
	require.True(t, upsert.ok, "baseline upsert failed: %+v", upsert)
}

func mustGetDocState(t *testing.T, fixture *suspectFixture) *schemas.DocState {
	t.Helper()
	machine := statemachine.NewDocStateMachine(fixture.stateStore)
	docState, err := machine.GetState(suspectDoc)
	require.NoError(t, err)
	require.NotNil(t, docState)
	return docState
}

func TestMarkSuspectsMappedChangeVerifiedToSuspect(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateVerified)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusModified},
		}), ""))
	require.NoError(t, err)

	assert.Equal(t, 1, report.TransitionedCount)
	assert.Equal(t, 0, report.ModelInvocations)
	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	assert.True(t, hasReasonKind(entry.Reasons, baselines.ReasonSourceChanged))
	require.NotNil(t, entry.LadderRun)
	assert.Equal(t, baselines.RunAncestor, *entry.LadderRun)

	docState := mustGetDocState(t, fixture)
	assert.Equal(t, schemas.DocStateSuspect, docState.State)
	require.Len(t, docState.Transitions, 2)
	assert.Equal(t, schemas.DocStateSuspect, docState.Transitions[1].State)
	assert.Equal(t, suspectRunID, docState.Transitions[1].RunID)
	assert.Contains(t, docState.Transitions[1].Reason, "source-changed")
	assert.Contains(t, docState.EvidenceRefs, suspectAnalysisRunID)
}

func TestMarkSuspectsOrphanedLiteralSource(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.remove(t, suspectSrc)
	fixture.commitAll(t, "delete the mapped source")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateVerified)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusDeleted},
		}), ""))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	// Count-pin (same-source collapse, MAJOR fix M): the graph orphan edge AND
	// the baseline orphan signal overlap for the SAME vanished source —
	// EXACTLY ONE 'source-orphaned' reason survives for it, carrying the
	// graph-edge wording that names that source.
	orphanReasons := reasonsOfKind(entry.Reasons, baselines.ReasonSourceOrphaned)
	require.Len(t, orphanReasons, 1)
	assert.Contains(t, orphanReasons[0].Detail, fmt.Sprintf(`graph source "%s"`, suspectSrc))
	assert.Equal(t, 1, report.TransitionedCount)
}

func TestMarkSuspectsAlreadySuspectReportedNotTransitioned(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateSuspect)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusModified},
		}), ""))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateSuspect, *entry.From)
	assert.Nil(t, entry.To)
	assert.False(t, entry.Transitioned)
	assert.NotEmpty(t, entry.Reasons)
	assert.Equal(t, 0, report.TransitionedCount)
	assert.Len(t, mustGetDocState(t, fixture).Transitions, 1)
}

func TestMarkSuspectsClearReSuspects(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateClear)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusModified},
		}), ""))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateClear, *entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	assert.True(t, hasReasonKind(entry.Reasons, baselines.ReasonSourceChanged))
	assert.Equal(t, 1, report.TransitionedCount)
	docState := mustGetDocState(t, fixture)
	assert.Equal(t, schemas.DocStateSuspect, docState.State)
	require.Len(t, docState.Transitions, 2) // initial CLEAR + the driver's SUSPECT
	assert.Equal(t, schemas.DocStateSuspect, docState.Transitions[1].State)
	assert.Equal(t, suspectRunID, docState.Transitions[1].RunID)
}

func TestMarkSuspectsRejectedReSuspects(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateRejected)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusModified},
		}), ""))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateRejected, *entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	assert.Equal(t, 1, report.TransitionedCount)
}

func TestMarkSuspectsNoBaselineStillReRoutes(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")
	// VERIFIED state, deliberately NO baseline record: the §6.2 trigger is
	// the MAPPED-SOURCE intersection, which exists independent of baselines.
	seedDocState(t, fixture, schemas.DocStateVerified)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusModified},
		}), ""))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	// No baseline → no ladder run; the mapped-source intersection speaks.
	assert.Nil(t, entry.LadderRun)
	assert.Equal(t, []baselines.InvalidationReason{{
		Kind: baselines.ReasonSourceChanged,
		Detail: fmt.Sprintf(`source "%s" matches changed file "%s" (modified)`,
			suspectSrc, suspectSrc),
	}}, entry.Reasons)
	assert.Equal(t, 1, report.TransitionedCount)
	assert.Equal(t, schemas.DocStateSuspect, mustGetDocState(t, fixture).State)
}

func TestMarkSuspectsShortCircuitFormattingOnly(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "whitespace-only sweep")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateVerified)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis(
			[]schemas.ChangedFile{{Path: suspectSrc, Status: schemas.FileStatusModified, FormattingOnly: boolPtr(true)}},
			[]schemas.ClassificationHint{{
				Kind: schemas.ClassificationHintFormattingOnly,
				Note: "all changed paths are whitespace-equivalent — skip Jev entirely (R10)",
			}}...,
		), ""))
	require.NoError(t, err)

	assert.Equal(t, 0, report.ModelInvocations)
	assert.Equal(t, 0, report.TransitionedCount)
	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	assert.Nil(t, entry.To)
	assert.False(t, entry.Transitioned)
	// The evidence is still reported; the short-circuit reason (appended
	// last) names the hint and the R10 decision-stage skip.
	require.NotEmpty(t, entry.Reasons)
	assert.Equal(t, baselines.InvalidationReason{
		Kind:   baselines.ReasonShortCircuit,
		Detail: "whole-change short-circuit hint \"formatting_only\" — R10 decision-stage skip (no SUSPECT churn for formatting/test-only sweeps)",
	}, entry.Reasons[len(entry.Reasons)-1])
	assert.True(t, hasReasonKind(entry.Reasons, baselines.ReasonSourceChanged))
	// No churn: the doc stays VERIFIED with its initial log only.
	docState := mustGetDocState(t, fixture)
	assert.Equal(t, schemas.DocStateVerified, docState.State)
	assert.Len(t, docState.Transitions, 1)
}

func TestMarkSuspectsShortCircuitTestOnly(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "test-only sweep")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateVerified)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis(
			[]schemas.ChangedFile{{Path: suspectSrc, Status: schemas.FileStatusModified, TestOnly: boolPtr(true)}},
			[]schemas.ClassificationHint{{
				Kind: schemas.ClassificationHintTestOnly,
				Note: "all changed paths match test patterns — skip Jev entirely (R10)",
			}}...,
		), ""))
	require.NoError(t, err)

	assert.Equal(t, 0, report.TransitionedCount)
	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	assert.Nil(t, entry.To)
	assert.False(t, entry.Transitioned)
	require.NotEmpty(t, entry.Reasons)
	last := entry.Reasons[len(entry.Reasons)-1]
	assert.Equal(t, baselines.ReasonShortCircuit, last.Kind)
	assert.Contains(t, last.Detail, `"test_only"`)
	assert.Equal(t, schemas.DocStateVerified, mustGetDocState(t, fixture).State)
}

func TestMarkSuspectsGeneratedHintDoesNotShortCircuit(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateVerified)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis(
			[]schemas.ChangedFile{{Path: suspectSrc, Status: schemas.FileStatusModified}},
			[]schemas.ClassificationHint{{
				Kind: schemas.ClassificationHintGenerated,
				Note: "generated file(s) present in the diff — routing per policy: inspect_generated_output (ARCH §8.8 memory pattern: generated-sdk)",
			}}...,
		), ""))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	assert.Equal(t, 1, report.TransitionedCount)
}

func TestMarkSuspectsUnmappedChangeNoTransition(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, "src/other.ts", "unmapped\n")
	fixture.commitAll(t, "unmapped change")
	seedVerifiedDoc(t, fixture, base, schemas.DocStateVerified)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: "src/other.ts", Status: schemas.FileStatusAdded},
		}), ""))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	assert.Nil(t, entry.To)
	assert.False(t, entry.Transitioned)
	assert.Empty(t, entry.Reasons)
	require.NotNil(t, entry.LadderRun)
	assert.Equal(t, baselines.RunAncestor, *entry.LadderRun)
	assert.Equal(t, 0, report.TransitionedCount)
}

func TestMarkSuspectsDocWithoutStateRecord(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusModified},
		}), ""))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	assert.Nil(t, entry.From)
	assert.Nil(t, entry.To)
	assert.False(t, entry.Transitioned)
	assert.Equal(t, 0, report.TransitionedCount)
}

func TestMarkSuspectsGraphUnusableRefused(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	fixture.commitAll(t, "base")

	_, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture,
		makeGraph(graph.GraphConfigError{
			Kind:    "duplicate_concept_name",
			Path:    "concepts[1].name",
			Message: "duplicate",
		}),
		makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusModified},
		}), ""))
	require.Error(t, err)
	var graphErr *graph.GraphUnusableError
	require.True(t, errors.As(err, &graphErr), "expected GraphUnusableError, got %v", err)
	require.Len(t, graphErr.Errors, 1)
	assert.Equal(t, graph.GraphConfigErrDuplicateConceptName, graphErr.Errors[0].Kind)
	assert.Contains(t, graphErr.Error(),
		"document graph has 1 configuration error(s) — refusing to route (fail closed)")
	assert.Equal(t, "EDOCSYNC_GRAPH_UNUSABLE", docutil.CodeOf(err))
}

func TestMarkSuspectsHistoryRewriteRoutesToSuspect(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	baselineCommit := fixture.commitAll(t, "feature")
	// Force-push: amend with different content — the baseline commit no
	// longer exists anywhere under HEAD.
	fixture.write(t, suspectSrc, "source v3\nextra\n")
	head := fixture.amendAll(t, "feature (rewritten)")
	seedVerifiedDoc(t, fixture, baselineCommit, schemas.DocStateVerified)

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), suspectInput(
		fixture, makeGraph(), makeAnalysis([]schemas.ChangedFile{
			{Path: suspectSrc, Status: schemas.FileStatusModified},
		}), head))
	require.NoError(t, err)

	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	require.NotNil(t, entry.LadderRun)
	assert.Equal(t, baselines.RunOrphanedSuspect, *entry.LadderRun)
	assert.True(t, hasReasonKind(entry.Reasons, baselines.ReasonHistoryRewrite))
	require.NotNil(t, entry.From)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	assert.Equal(t, 1, report.TransitionedCount)
}

/* ------------------------------ machine error texts + explicit hashes */

func TestDocStateMachineErrorMessages(t *testing.T) {
	machine := makeMachine(t, backendInMemory)
	_, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateVerified,
		RunID:         smRunID,
		PolicyVersion: smPolicy,
		At:            smAt,
	})
	require.NoError(t, err)
	_, err = machine.ApplyTransition(statemachine.ApplyTransitionInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateProposed,
		RunID:         smRunID,
		Reason:        "nope",
		PolicyVersion: smPolicy,
		At:            smAt,
	})
	var illegalErr *statemachine.IllegalTransitionError
	require.ErrorAs(t, err, &illegalErr)
	assert.Contains(t, illegalErr.Error(),
		"illegal doc-state transition VERIFIED -> PROPOSED (ARCH §6.2 table) — refusing (fail closed)")

	_, err = machine.ApplyTransition(statemachine.ApplyTransitionInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateSuspect,
		RunID:         smRunID,
		Reason:        "drift",
		PolicyVersion: smPolicy,
		At:            smAt,
	})
	require.NoError(t, err)
	_, err = machine.ApplyTransition(statemachine.ApplyTransitionInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateClear,
		RunID:         smRunID,
		Reason:        "looks fine",
		PolicyVersion: smPolicy,
		At:            smAt,
	})
	var decisionErr *statemachine.DecisionRequiredError
	require.ErrorAs(t, err, &decisionErr)
	assert.Contains(t, decisionErr.Error(),
		"transition SUSPECT -> CLEAR is decision-owned and requires a decision {source, ref} (Phase 2/5 wire Jev/human/verifier/policy)")
}

func TestDocStateMachineInitializeRecordsDecision(t *testing.T) {
	machine := makeMachine(t, backendInMemory)
	created, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateVerified,
		RunID:         smRunID,
		PolicyVersion: smPolicy,
		At:            smAt,
		EvidenceRefs:  []string{"manifest/v1"},
		Decision: &statemachine.TransitionDecision{
			Source: statemachine.TransitionDecisionSourceHuman,
			Ref:    "h1",
		},
	})
	require.NoError(t, err)
	require.True(t, created.OK)
	require.NotNil(t, created.State)
	assert.Equal(t, "initial state VERIFIED recorded (decision: human:h1)",
		created.State.Transitions[0].Reason)
	assert.Equal(t, []string{"manifest/v1", "decision:human:h1"}, created.State.EvidenceRefs)
}

func TestDocStateMachineExplicitHashExpectations(t *testing.T) {
	store := makeBackend(t, backendInMemory)
	machine := statemachine.NewDocStateMachine(store)
	created, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateVerified,
		RunID:         smRunID,
		PolicyVersion: smPolicy,
		At:            smAt,
		Reason:        "bootstrap",
	})
	require.NoError(t, err)
	require.True(t, created.OK)

	// An explicit stale hash loses the CAS on the initialize path.
	stale := strings.Repeat("f", 64)
	again, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateVerified,
		RunID:         smRunID,
		PolicyVersion: smPolicy,
		At:            smAt,
		ExpectedHash:  &stale,
	})
	require.NoError(t, err)
	assert.False(t, again.OK)
	assert.Equal(t, statemachine.StateMutationKindCasConflict, again.Kind)

	// An explicit expect-absent expectation can only lose the CAS (it never
	// overwrites, fail closed) — the record stays untouched.
	absent, err := machine.ApplyTransition(statemachine.ApplyTransitionInput{
		DocPath:       smDocPath,
		To:            schemas.DocStateSuspect,
		RunID:         smRunID,
		Reason:        "drift",
		PolicyVersion: smPolicy,
		At:            smAt,
		ExpectAbsent:  true,
	})
	require.NoError(t, err)
	assert.False(t, absent.OK)
	assert.Equal(t, statemachine.StateMutationKindCasConflict, absent.Kind)
	require.NotNil(t, absent.ActualHash)
	assert.Equal(t, created.Hash, *absent.ActualHash)
	after, err := machine.GetState(smDocPath)
	require.NoError(t, err)
	assert.Equal(t, schemas.DocStateVerified, after.State)
	assert.Len(t, after.Transitions, 1)
}

func TestDocStateMachineBackendErrorsPropagate(t *testing.T) {
	store := newFakeDriverStore()
	store.readErr = errors.New("boom-read")
	store.listErr = errors.New("boom-list")
	store.writeErr = errors.New("boom-write")
	machine := statemachine.NewDocStateMachine(store)
	_, err := machine.GetState(smDocPath)
	require.Error(t, err)
	_, err = machine.ListStates()
	require.Error(t, err)
	_, err = machine.Initialize(statemachine.InitializeStateInput{
		DocPath: smDocPath, To: schemas.DocStateVerified, RunID: smRunID, PolicyVersion: smPolicy, At: smAt,
	})
	require.Error(t, err)
	_, err = machine.ApplyTransition(statemachine.ApplyTransitionInput{
		DocPath: smDocPath, To: schemas.DocStateSuspect, RunID: smRunID, PolicyVersion: smPolicy, At: smAt,
	})
	require.Error(t, err)
}

/* --------------------------- deterministic driver paths (fake seams) */

type fakeDriverStore struct {
	docStates       map[string]*schemas.DocState
	docHashes       map[string]string
	baselines       map[string]schemas.Baseline
	forceConflict   map[string]bool
	readErr         error
	readBaselineErr error
	writeErr        error
	listErr         error
}

func newFakeDriverStore() *fakeDriverStore {
	return &fakeDriverStore{
		docStates:     map[string]*schemas.DocState{},
		docHashes:     map[string]string{},
		baselines:     map[string]schemas.Baseline{},
		forceConflict: map[string]bool{},
	}
}

func (s *fakeDriverStore) seedDoc(docPath string, docState schemas.DocStateName) {
	runID := "run_0"
	s.docStates[docPath] = &schemas.DocState{
		Path:          docPath,
		State:         docState,
		Since:         "2026-10-04T00:00:00Z",
		LastRun:       &runID,
		PolicyVersion: 17,
		EvidenceRefs:  []string{},
		Transitions: []schemas.DocStateTransition{{
			State:  docState,
			At:     "2026-10-04T00:00:00Z",
			RunID:  "run_0",
			Reason: "seed",
		}},
	}
	s.docHashes[docPath] = docutil.ContentHash(docPath + string(docState))
}

func (s *fakeDriverStore) ReadDocState(docPath string) (state.StateRead[*schemas.DocState], error) {
	if s.readErr != nil {
		return state.StateRead[*schemas.DocState]{}, s.readErr
	}
	docState, ok := s.docStates[docPath]
	if !ok {
		return state.StateRead[*schemas.DocState]{}, nil
	}
	return state.StateRead[*schemas.DocState]{Content: docState, Hash: s.docHashes[docPath], Exists: true}, nil
}

func (s *fakeDriverStore) WriteDocState(docPath string, docState schemas.DocState, expectedHash *string) (state.CasWriteResult, error) {
	if s.writeErr != nil {
		return state.CasWriteResult{}, s.writeErr
	}
	if s.forceConflict[docPath] {
		expected, actual := "stale-expected", "fresh-actual"
		return state.CasWriteResult{OK: false, ExpectedHash: &expected, ActualHash: &actual}, nil
	}
	if current, exists := s.docStates[docPath]; exists {
		_ = current
		if expectedHash == nil || *expectedHash != s.docHashes[docPath] {
			actual := s.docHashes[docPath]
			return state.CasWriteResult{OK: false, ExpectedHash: expectedHash, ActualHash: &actual}, nil
		}
	} else if expectedHash != nil {
		expected := *expectedHash
		return state.CasWriteResult{OK: false, ExpectedHash: &expected, ActualHash: nil}, nil
	}
	next := docState
	s.docStates[docPath] = &next
	hash := docutil.ContentHash(fmt.Sprintf("%s|%s|%d", docPath, docState.State, docState.PolicyVersion))
	s.docHashes[docPath] = hash
	return state.CasWriteResult{OK: true, Hash: hash}, nil
}

func (s *fakeDriverStore) ListDocStatePaths() ([]string, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	paths := make([]string, 0, len(s.docStates))
	for docPath := range s.docStates {
		paths = append(paths, docPath)
	}
	sortStrings(paths)
	return paths, nil
}

func (s *fakeDriverStore) ReadBaseline(docPath string) (state.StateRead[*schemas.Baseline], error) {
	if s.readBaselineErr != nil {
		return state.StateRead[*schemas.Baseline]{}, s.readBaselineErr
	}
	baseline, ok := s.baselines[docPath]
	if !ok {
		return state.StateRead[*schemas.Baseline]{}, nil
	}
	return state.StateRead[*schemas.Baseline]{Content: &baseline, Exists: true}, nil
}

// The driver's git surface is the concrete *gitx.GitClient (exactly the TS
// structural GitClient — the D12 ladder consumes the same client), so the
// driver paths below run against real system git in fresh temp repos; only the
// STORE seam is faked (CAS conflict + backend errors).

func TestMarkSuspectsDriverCASConflictReported(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")
	// Real git (the ladder resolves for real); the store seam forces the
	// state-write CAS conflict.
	store := newFakeDriverStore()
	store.seedDoc(suspectDoc, schemas.DocStateVerified)
	store.baselines[suspectDoc] = makeBaseline(base)
	store.forceConflict[suspectDoc] = true

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), statemachine.SuspectDriverInput{
		Store:         store,
		Graph:         makeGraph(),
		Analysis:      makeAnalysis([]schemas.ChangedFile{{Path: suspectSrc, Status: schemas.FileStatusModified}}),
		Git:           fixture.git,
		Head:          "HEAD",
		RunID:         suspectRunID,
		PolicyVersion: suspectPolicyVersion,
		At:            suspectAt,
	})
	require.NoError(t, err)
	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	assert.False(t, entry.Transitioned)
	assert.Equal(t, statemachine.StateMutationKindCasConflict, entry.Conflict)
	assert.Equal(t, 0, report.TransitionedCount)
	// The loser never corrupts the winner: the record is untouched.
	assert.Equal(t, schemas.DocStateVerified, store.docStates[suspectDoc].State)
	assert.Len(t, store.docStates[suspectDoc].Transitions, 1)
	// The ladder still ran for real (baseline = the base commit, ancestor of HEAD).
	require.NotNil(t, entry.LadderRun)
	assert.Equal(t, baselines.RunAncestor, *entry.LadderRun)
}

func TestMarkSuspectsDriverClaimsAndSymbolHooks(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, "src/a.ts", "source v1\n")
	fixture.commitAll(t, "base")
	fixture.write(t, "src/a.ts", "source v2\n")
	fixture.commitAll(t, "change the source")
	store := newFakeDriverStore()
	store.seedDoc(suspectDoc, schemas.DocStateVerified)
	// No mapping sources and no baseline: the claim intersection and the
	// symbol-change hook alone drive the transition (ARCH §6.2 trigger: ANY of
	// mapped sources / claims / baseline intersects).
	claimsGraph := graph.DocumentGraph{
		Mappings:      []graph.MappingRecord{{Doc: suspectDoc, Sources: []string{}}},
		ContractKinds: map[string]schemas.ContractKind{},
	}

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), statemachine.SuspectDriverInput{
		Store:         store,
		Graph:         claimsGraph,
		Analysis:      makeAnalysis([]schemas.ChangedFile{{Path: "src/a.ts", Status: schemas.FileStatusModified, SymbolsChanged: boolPtr(true)}}),
		Git:           fixture.git,
		Head:          "HEAD",
		RunID:         suspectRunID,
		PolicyVersion: suspectPolicyVersion,
		At:            suspectAt,
		Claims: []baselines.InvalidationClaim{
			{ID: "c1", Source: baselines.InvalidationClaimSource{File: "src/a.ts"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	assert.True(t, entry.Transitioned)
	assert.Equal(t, 1, report.TransitionedCount)
	assert.True(t, hasReasonKind(entry.Reasons, baselines.ReasonClaimSourceChanged))
	assert.True(t, hasReasonKind(entry.Reasons, baselines.ReasonSymbolChange))
	assert.Nil(t, entry.LadderRun)
}

func TestMarkSuspectsDriverRenamedSourceOrphan(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, "src/old.ts", "source v1\n")
	fixture.commitAll(t, "base")
	// The literal source is renamed away: it vanishes from the HEAD tree.
	fixture.remove(t, "src/old.ts")
	fixture.write(t, "src/new.ts", "source v1\n")
	fixture.commitAll(t, "rename the mapped source")
	store := newFakeDriverStore()
	store.seedDoc(suspectDoc, schemas.DocStateVerified)
	renameGraph := graph.DocumentGraph{
		Mappings:      []graph.MappingRecord{{Doc: suspectDoc, Sources: []string{"src/old.ts"}}},
		ContractKinds: map[string]schemas.ContractKind{},
	}

	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), statemachine.SuspectDriverInput{
		Store:         store,
		Graph:         renameGraph,
		Analysis:      makeAnalysis([]schemas.ChangedFile{{Path: "src/new.ts", Status: schemas.FileStatusRenamed, OldPath: strPtr("src/old.ts")}}),
		Git:           fixture.git,
		Head:          "HEAD",
		RunID:         suspectRunID,
		PolicyVersion: suspectPolicyVersion,
		At:            suspectAt,
	})
	require.NoError(t, err)
	require.Len(t, report.Entries, 1)
	entry := report.Entries[0]
	assert.True(t, entry.Transitioned)
	orphanReasons := reasonsOfKind(entry.Reasons, baselines.ReasonSourceOrphaned)
	require.Len(t, orphanReasons, 1)
	assert.Equal(t, baselines.InvalidationReason{
		Kind:   baselines.ReasonSourceOrphaned,
		Detail: `graph source "src/old.ts" source-renamed (from "src/old.ts")`,
	}, orphanReasons[0])
	assert.True(t, hasReasonKind(entry.Reasons, baselines.ReasonSourceChanged))
}

func TestMarkSuspectsDriverStoreErrorsPropagate(t *testing.T) {
	fixture := newSuspectFixture(t)
	fixture.write(t, suspectDoc, "v1\n")
	fixture.write(t, suspectSrc, "source v1\n")
	base := fixture.commitAll(t, "base")
	fixture.write(t, suspectSrc, "source v2\n")
	fixture.commitAll(t, "change the mapped source")
	store := newFakeDriverStore()
	store.seedDoc(suspectDoc, schemas.DocStateVerified)
	store.readBaselineErr = errors.New("baseline read boom")

	_, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), statemachine.SuspectDriverInput{
		Store:         store,
		Graph:         makeGraph(),
		Analysis:      makeAnalysis([]schemas.ChangedFile{{Path: suspectSrc, Status: schemas.FileStatusModified}}),
		Git:           fixture.git,
		Head:          "HEAD",
		RunID:         suspectRunID,
		PolicyVersion: suspectPolicyVersion,
		At:            suspectAt,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseline read boom")
	_ = base
}
