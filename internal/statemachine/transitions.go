package statemachine

import "github.com/DUDE616/docSync-go/internal/schemas"

// Ported from core/src/statemachine/transitions.ts (P1-T17; ARCH §6.2).
//
// The doc state machine's vocabulary is DOC_STATE_NAMES (internal/schemas);
// this file is the single authority for WHICH transitions are legal and WHO
// owns them. §6.2 core edges plus the documented conservative additions that
// close the graph (every non-terminal state keeps an exit; CLOSED is the only
// terminal):
//
//	VERIFIED → SUSPECT | CLOSED      (deterministic: analysis intersects the
//	                                  mapped sources/claims/baseline; doc
//	                                  deleted/archived/superseded)
//	SUSPECT  → CLEAR | REVIEW | STALE
//	CLEAR    → SUSPECT               (new drift re-suspects a cleared doc)
//	REVIEW   → STALE | CLEAR | CLOSED
//	STALE    → PROPOSED
//	PROPOSED → VERIFIED | REJECTED
//	REJECTED → SUSPECT               (a rejected proposal leaves the doc
//	                                  drifted again — back to the review loop)
//	CLOSED   → (terminal)
//
// Ownership (§6.2 + OD-R38 ruling): the DETERMINISTIC edges are Phase
// 1-owned — VERIFIED→SUSPECT (structural analysis intersects), VERIFIED→CLOSED
// (doc deletion/archival), CLEAR→SUSPECT (new mapped drift on a cleared doc is
// deterministic evidence — the suspect driver re-routes it, OD-R38), and
// REJECTED→SUSPECT (new drift on a rejected proposal is deterministic evidence
// — back to the review loop, OD-R38). Everything else is DECISION-owned and
// consumes Phase 2 decisions via Phase 5 (Jev/human/verifier/policy) —
// including REVIEW→CLOSED. The owner table is keyed by the compact
// `<FROM>-><TO>` form.

// LegalTransitions lists the legal targets per source state (the binding
// §6.2 table + documented additions).
var LegalTransitions = map[schemas.DocStateName][]schemas.DocStateName{
	schemas.DocStateVerified: {schemas.DocStateSuspect, schemas.DocStateClosed},
	schemas.DocStateSuspect:  {schemas.DocStateClear, schemas.DocStateReview, schemas.DocStateStale},
	schemas.DocStateClear:    {schemas.DocStateSuspect},
	schemas.DocStateReview:   {schemas.DocStateStale, schemas.DocStateClear, schemas.DocStateClosed},
	schemas.DocStateStale:    {schemas.DocStateProposed},
	schemas.DocStateProposed: {schemas.DocStateVerified, schemas.DocStateRejected},
	schemas.DocStateRejected: {schemas.DocStateSuspect},
	schemas.DocStateClosed:   {},
}

// TransitionOwner is the edge owner: 'deterministic' = structural Phase 1
// edges; 'decision' = Phase 2/5-decided edges.
type TransitionOwner string

// Transition owner values.
const (
	TransitionOwnerDeterministic TransitionOwner = "deterministic"
	TransitionOwnerDecision      TransitionOwner = "decision"
)

// TransitionKey builds the compact `<FROM>-><TO>` key for the owner table.
func TransitionKey(from, to schemas.DocStateName) string {
	return string(from) + "->" + string(to)
}

// TransitionOwners maps every legal edge to exactly one owner.
var TransitionOwners = map[string]TransitionOwner{
	"VERIFIED->SUSPECT":  TransitionOwnerDeterministic,
	"VERIFIED->CLOSED":   TransitionOwnerDeterministic,
	"SUSPECT->CLEAR":     TransitionOwnerDecision,
	"SUSPECT->REVIEW":    TransitionOwnerDecision,
	"SUSPECT->STALE":     TransitionOwnerDecision,
	"CLEAR->SUSPECT":     TransitionOwnerDeterministic,
	"REVIEW->STALE":      TransitionOwnerDecision,
	"REVIEW->CLEAR":      TransitionOwnerDecision,
	"REVIEW->CLOSED":     TransitionOwnerDecision,
	"STALE->PROPOSED":    TransitionOwnerDecision,
	"PROPOSED->VERIFIED": TransitionOwnerDecision,
	"PROPOSED->REJECTED": TransitionOwnerDecision,
	"REJECTED->SUSPECT":  TransitionOwnerDeterministic,
}

// IsLegalTransition reports whether the edge `<from> → <to>` is in the legal
// table (unknown names → false, fail closed).
func IsLegalTransition(from, to schemas.DocStateName) bool {
	for _, target := range LegalTransitions[from] {
		if target == to {
			return true
		}
	}
	return false
}

// TransitionOwnerOf returns the owner of a legal edge; ok is false for an
// illegal one (the TS `transitionOwner` null return — fail closed upstream).
func TransitionOwnerOf(from, to schemas.DocStateName) (TransitionOwner, bool) {
	if !IsLegalTransition(from, to) {
		return "", false
	}
	owner, ok := TransitionOwners[TransitionKey(from, to)]
	return owner, ok
}
