package statemachine

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// Ported from core/src/statemachine/machine.ts (P1-T17; ARCH §6.2).
//
// DocStateMachine is the INTERNAL truth for document workflow state — GitHub
// surfaces (check runs, comments) are projections and land in later phases.
// State lives in `state/docs/<path>.json` (D1) and is mutated only through
// this machine, which enforces, in order (fail closed at each step):
//
//  1. LEGALITY — the edge must be in the §6.2 table (transitions.go); an
//     illegal edge returns IllegalTransitionError (a programming error, never
//     a graceful no-op).
//  2. OWNERSHIP — decision-owned edges REQUIRE a decision {source, ref};
//     missing → DecisionRequiredError. Deterministic edges accept (but do not
//     require) one. The decision is recorded BOTH in the appended transition's
//     reason (`(decision: <source>:<ref>)`) and in evidence_refs
//     (`decision:<source>:<ref>`) — provenance or it didn't happen. Phase 2/5
//     wire the real decisions; this field is the typed seam.
//  3. APPEND-ONLY LOG — the transition is appended to transitions[] (existing
//     entries are never rewritten or removed; the new state/since/last_run/
//     policy_version are the moving pointer, the log is history).
//  4. CAS — the write is conditioned on the state hash (freshly read, or the
//     caller's explicit expectation); a conflicting concurrent transition
//     LOSES gracefully with the cas-conflict shape and the loser re-runs its
//     evaluation (R15, V-P1-11) — it never corrupts or overwrites the winner's
//     bytes.
//
// Initialize is the creation path: it writes the FIRST record (initial state +
// its log entry). Initialization is not a transition (no `from`), so the
// legality table does not apply; a record that already exists loses the CAS
// (expect-absent default) — initialization never overwrites.
//
// Timestamps are caller-owned (At, ISO-8601): core keeps no hidden clocks.

// CodeStateMachine preserves the TS EDOCSYNC_STATE_MACHINE code (machine.ts)
// verbatim through docutil.New — docutil has no state-machine code yet, the
// same package-local precedent as internal/globs' CodeGlob.
const CodeStateMachine = "EDOCSYNC_STATE_MACHINE"

// IllegalTransitionError is an edge outside the §6.2 table — programming
// error, fail loud (fail closed). Carries the refused edge.
type IllegalTransitionError struct {
	From  schemas.DocStateName
	To    schemas.DocStateName
	coded *docutil.Error
}

func newIllegalTransitionError(from, to schemas.DocStateName) *IllegalTransitionError {
	return &IllegalTransitionError{
		From: from,
		To:   to,
		coded: docutil.New(CodeStateMachine, fmt.Sprintf(
			"illegal doc-state transition %s -> %s (ARCH §6.2 table) — refusing (fail closed)",
			from, to)),
	}
}

func (e *IllegalTransitionError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf reports EDOCSYNC_STATE_MACHINE.
func (e *IllegalTransitionError) Unwrap() error { return e.coded }

// DecisionRequiredError is a decision-owned edge attempted without a recorded
// decision. Carries the refused edge.
type DecisionRequiredError struct {
	From  schemas.DocStateName
	To    schemas.DocStateName
	coded *docutil.Error
}

func newDecisionRequiredError(from, to schemas.DocStateName) *DecisionRequiredError {
	return &DecisionRequiredError{
		From: from,
		To:   to,
		coded: docutil.New(CodeStateMachine, fmt.Sprintf(
			"transition %s -> %s is decision-owned and requires a decision {source, ref} (Phase 2/5 wire Jev/human/verifier/policy)",
			from, to)),
	}
}

func (e *DecisionRequiredError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf reports EDOCSYNC_STATE_MACHINE.
func (e *DecisionRequiredError) Unwrap() error { return e.coded }

// TransitionDecisionSource is the decision provenance (TS
// TransitionDecisionSource).
type TransitionDecisionSource string

// Transition decision sources.
const (
	TransitionDecisionSourceJev      TransitionDecisionSource = "jev"
	TransitionDecisionSourceHuman    TransitionDecisionSource = "human"
	TransitionDecisionSourceVerifier TransitionDecisionSource = "verifier"
	TransitionDecisionSourcePolicy   TransitionDecisionSource = "policy"
)

// TransitionDecision is the typed seam for Phase 2/5 decisions; recorded in
// reason + evidence.
type TransitionDecision struct {
	Source TransitionDecisionSource
	Ref    string
}

// DocStateMachineBackend is the structural backend: exactly the StateStore
// surface the machine needs (*state.StateStore satisfies it).
type DocStateMachineBackend interface {
	ReadDocState(docPath string) (state.StateRead[*schemas.DocState], error)
	WriteDocState(docPath string, docState schemas.DocState, expectedHash *string) (state.CasWriteResult, error)
	ListDocStatePaths() ([]string, error)
}

// ApplyTransitionInput is one transition request. ExpectedHash/ExpectAbsent
// model the TS `expectedHash?: string | null` tri-state: both zero ⇒ omitted
// (the write is conditioned on the hash the machine just read).
type ApplyTransitionInput struct {
	DocPath       string
	To            schemas.DocStateName
	RunID         string
	Reason        string
	PolicyVersion int
	// At is the ISO-8601 timestamp of the transition (caller-owned; no hidden clock).
	At           string
	EvidenceRefs []string
	Decision     *TransitionDecision
	// ExpectedHash is the explicit CAS expectation (stale hash → conflict result).
	ExpectedHash *string
	// ExpectAbsent is the explicit TS `expectedHash: null`: the write expects
	// the record to be ABSENT, so it can only lose the CAS — it never
	// overwrites (fail closed). Ignored when ExpectedHash is set.
	ExpectAbsent bool
}

// InitializeStateInput creates the FIRST state record for a doc. ExpectedHash
// omitted (nil) is the TS `?? null` default — the write expects the record to
// be ABSENT, so initialization never overwrites (a second initialize loses
// the CAS).
type InitializeStateInput struct {
	DocPath       string
	To            schemas.DocStateName
	RunID         string
	Reason        string
	PolicyVersion int
	At            string
	EvidenceRefs  []string
	Decision      *TransitionDecision
	ExpectedHash  *string
}

// StateMutationKindCasConflict is the graceful-loser kind (TS
// `{ok: false, kind: 'cas-conflict'}`): the run re-runs, nothing corrupts.
const StateMutationKindCasConflict = "cas-conflict"

// StateMutation is the outcome of a state mutation: success carries the new
// record + hash; the cas-conflict kind is the graceful loser shape — never a
// thrown race (the TS StateMutation union).
type StateMutation struct {
	OK           bool
	State        *schemas.DocState
	Hash         string
	Kind         string
	ExpectedHash *string
	ActualHash   *string
}

func stateMutationOK(docState *schemas.DocState, hash string) StateMutation {
	return StateMutation{OK: true, State: docState, Hash: hash}
}

func stateMutationConflict(expected, actual *string) StateMutation {
	return StateMutation{
		OK:           false,
		Kind:         StateMutationKindCasConflict,
		ExpectedHash: expected,
		ActualHash:   actual,
	}
}

// mergeEvidenceRefs appends the added refs uniquely, preserving order (append,
// never reorder — the TS mergeEvidenceRefs).
func mergeEvidenceRefs(existing, added []string) []string {
	merged := make([]string, 0, len(existing)+len(added))
	merged = append(merged, existing...)
	for _, ref := range added {
		found := false
		for _, have := range merged {
			if have == ref {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, ref)
		}
	}
	return merged
}

// decisionSuffix renders the reason suffix for a recorded decision ("" when
// none — the TS decisionSuffix).
func decisionSuffix(decision *TransitionDecision) string {
	if decision == nil {
		return ""
	}
	return fmt.Sprintf(" (decision: %s:%s)", decision.Source, decision.Ref)
}

// decisionEvidenceRef renders the decision's evidence ref ("" → none — the TS
// decisionEvidenceRef).
func decisionEvidenceRef(decision *TransitionDecision) []string {
	if decision == nil {
		return nil
	}
	return []string{fmt.Sprintf("decision:%s:%s", decision.Source, decision.Ref)}
}

// resolveExpectedHash conditions a write: the caller's explicit hash wins;
// an explicit expect-absent is a nil expectation (create-only); otherwise the
// freshly-read hash conditions the update.
func resolveExpectedHash(explicit *string, expectAbsent bool, fresh string) *string {
	if explicit != nil {
		return explicit
	}
	if expectAbsent {
		return nil
	}
	hash := fresh
	return &hash
}

// DocStateMachine is the CAS-guarded doc lifecycle machine.
type DocStateMachine struct {
	backend DocStateMachineBackend
}

// NewDocStateMachine binds the machine to a backend.
func NewDocStateMachine(backend DocStateMachineBackend) *DocStateMachine {
	return &DocStateMachine{backend: backend}
}

// GetState returns the current doc state, or nil when the doc has no state
// record yet.
func (m *DocStateMachine) GetState(docPath string) (*schemas.DocState, error) {
	read, err := m.backend.ReadDocState(docPath)
	if err != nil {
		return nil, err
	}
	if !read.Exists || read.Content == nil {
		return nil, nil
	}
	return read.Content, nil
}

// ListStates returns all doc states, sorted by doc path (deterministic — the
// backend lists paths in sorted order).
func (m *DocStateMachine) ListStates() ([]*schemas.DocState, error) {
	paths, err := m.backend.ListDocStatePaths()
	if err != nil {
		return nil, err
	}
	states := make([]*schemas.DocState, 0, len(paths))
	for _, docPath := range paths {
		docState, err := m.GetState(docPath)
		if err != nil {
			return nil, err
		}
		if docState != nil {
			states = append(states, docState)
		}
	}
	return states, nil
}

// Initialize creates the FIRST state record for a doc (initial state + its log
// entry). Not a transition — the legality table does not apply. An existing
// record wins the CAS (expect-absent default) → the conflict shape;
// initialization never overwrites.
func (m *DocStateMachine) Initialize(input InitializeStateInput) (StateMutation, error) {
	reason := input.Reason
	if reason == "" {
		reason = fmt.Sprintf("initial state %s recorded", input.To)
	}
	reason += decisionSuffix(input.Decision)
	transition := schemas.DocStateTransition{
		State:  input.To,
		At:     input.At,
		RunID:  input.RunID,
		Reason: reason,
	}
	runID := input.RunID
	docState := schemas.DocState{
		Path:          input.DocPath,
		State:         input.To,
		Since:         input.At,
		LastRun:       &runID,
		PolicyVersion: input.PolicyVersion,
		EvidenceRefs:  mergeEvidenceRefs(input.EvidenceRefs, decisionEvidenceRef(input.Decision)),
		Transitions:   []schemas.DocStateTransition{transition},
	}
	write, err := m.backend.WriteDocState(input.DocPath, docState, input.ExpectedHash)
	if err != nil {
		return StateMutation{}, err
	}
	if !write.OK {
		return stateMutationConflict(write.ExpectedHash, write.ActualHash), nil
	}
	return stateMutationOK(&docState, write.Hash), nil
}

// ApplyTransition validates and applies one transition (legality → ownership
// → append-only log → CAS). Illegal edges and missing decisions return
// IllegalTransitionError / DecisionRequiredError (fail closed); CAS races
// return the graceful conflict shape.
func (m *DocStateMachine) ApplyTransition(input ApplyTransitionInput) (StateMutation, error) {
	read, err := m.backend.ReadDocState(input.DocPath)
	if err != nil {
		return StateMutation{}, err
	}
	if !read.Exists || read.Content == nil {
		return StateMutation{}, state.StateMissingError(
			`cannot transition doc "%s": no state record exists (initialize first)`,
			input.DocPath)
	}
	current := read.Content
	from := current.State
	if !IsLegalTransition(from, input.To) {
		return StateMutation{}, newIllegalTransitionError(from, input.To)
	}
	owner, ok := TransitionOwnerOf(from, input.To)
	if ok && owner == TransitionOwnerDecision && input.Decision == nil {
		return StateMutation{}, newDecisionRequiredError(from, input.To)
	}
	transition := schemas.DocStateTransition{
		State:  input.To,
		At:     input.At,
		RunID:  input.RunID,
		Reason: input.Reason + decisionSuffix(input.Decision),
	}
	runID := input.RunID
	next := *current
	next.State = input.To
	next.Since = input.At
	next.LastRun = &runID
	next.PolicyVersion = input.PolicyVersion
	next.EvidenceRefs = mergeEvidenceRefs(
		mergeEvidenceRefs(current.EvidenceRefs, input.EvidenceRefs),
		decisionEvidenceRef(input.Decision))
	next.Transitions = append(append([]schemas.DocStateTransition{}, current.Transitions...), transition)
	// Freshly-read hash by default (readDocState always sets it for an
	// existing record); an explicit caller expectation wins. A nil expectation
	// can only lose the CAS — it never overwrites (fail closed).
	expectedHash := resolveExpectedHash(input.ExpectedHash, input.ExpectAbsent, read.Hash)
	write, err := m.backend.WriteDocState(input.DocPath, next, expectedHash)
	if err != nil {
		return StateMutation{}, err
	}
	if !write.OK {
		return stateMutationConflict(write.ExpectedHash, write.ActualHash), nil
	}
	return stateMutationOK(&next, write.Hash), nil
}
