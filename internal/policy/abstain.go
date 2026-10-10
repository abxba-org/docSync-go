// Ported from packages/policy/src/decision/abstain.ts — abstain recording
// (P2-T14; ARCH §5.2), the review-queue layer behind decision-level
// abstention.
//
// Two-tier reason discipline (ratified taxonomy in @docsync/schemas):
//   - The FIVE canonical decision-level reasons (insufficient_confidence,
//     conflicting_evidence, policy_gap, calibration_floor,
//     verifier_uncertain) are the ONLY queueable abstentions. Every abstained
//     case lands in the human-review queue WITH evidence attached (at least one
//     machine-resolvable anchor) and becomes a labeled data point once
//     resolved — the (answers, reason, resolution) shape §5.2 mandates.
//   - OPERATIONAL reasons (schema_violation, model_unavailable,
//     cap_exceeded, policy_missing, other) BYPASS the queue: they are
//     gateway-degradation bookkeeping for degraded runs (bounded-retry
//     exhaustion etc.), never policy-learning data and never review-queue
//     items. Recording one fails with code 'operational_reason'.
//
// Pure in-memory module: NO filesystem, NO clocks, NO persistence — Phase 5/10
// wire storage behind the OnLabeledDataPoint hook and the returned typed
// records. Deterministic event ids (content hash over decision_id + reason);
// duplicate records are idempotent no-ops (no flood — §3 rule 11).
//
// The reason vocabularies are reused from internal/schemas
// (DecisionLevelAbstainReasons / OperationalAbstainReasons / AbstainReasonValues)
// per OD-2 — never duplicated here.
package policy

import (
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AbstainQueueCode values — the machine-readable failure codes of the abstain
// queue (TS AbstainQueueErrorCode). A single typed error carries the code.
const (
	AbstainQueueCodeDecisionNotAbstained = "decision_not_abstained"
	AbstainQueueCodeOperationalReason    = "operational_reason"
	AbstainQueueCodeReasonMismatch       = "reason_mismatch"
	AbstainQueueCodeEmptyEvidence        = "empty_evidence"
	AbstainQueueCodeEventNotFound        = "event_not_found"
	AbstainQueueCodeAlreadyResolved      = "already_resolved"
	AbstainQueueCodeInvalidResolution    = "invalid_resolution"
)

// AbstainQueueError is the one typed error of the abstain queue (TS
// AbstainQueueError): Code carries the machine-readable failure, Message is
// byte-preserved from the TS implementation. Callers match it with errors.As.
type AbstainQueueError struct {
	Code    string
	Message string
}

// Error renders the preserved message (the TS `message` field).
func (e *AbstainQueueError) Error() string { return e.Message }

// Unwrap maps the queue failure onto docutil's coded-error class so
// docutil.CodeOf reports EDOCSYNC_VALIDATION — the closest existing docutil
// code for a queue-input validation refusal (the TS class has no docutil
// counterpart; see CONVENTIONS.md §Errors).
func (e *AbstainQueueError) Unwrap() error {
	return docutil.New(docutil.CodeValidation, e.Message)
}

func newAbstainQueueError(code, message string) *AbstainQueueError {
	return &AbstainQueueError{Code: code, Message: message}
}

// AbstainResolutionValues is the closed resolution-label set for a resolved
// abstention (§5.2 labeled data point; §8.7 label hygiene) — TS
// ABSTAIN_RESOLUTION_VALUES. `unlabeled` exists precisely so a resolution
// without a usable human label is recorded AS unlabeled — never guessed into
// an implicit accept/reject (§8.7: implicit signals are ambiguous).
var AbstainResolutionValues = []string{
	"confirmed_affected",
	"not_affected",
	"corrected",
	"unlabeled",
}

// AbstainEvent is one queued decision-level abstention — the record payload
// (§5.2).
type AbstainEvent struct {
	// EventID is the deterministic content id: `a_<hex16>` over
	// sha256(decision_id ⊕ NUL ⊕ reason).
	EventID string `json:"event_id"`
	// Decision is the abstained decision verbatim (`abstain: true`,
	// decision-level `abstain_reason`).
	Decision *schemas.Decision `json:"decision"`
	// Reason is the decision-level abstain reason.
	Reason schemas.AbstainReason `json:"reason"`
	// Evidence is attached at queue time — non-empty, machine-resolvable
	// (§5.2).
	Evidence []schemas.Evidence `json:"evidence"`
}

// AbstainQueueEntry is a queue entry: the event plus its resolution once a
// human resolved it.
type AbstainQueueEntry struct {
	Event      AbstainEvent       `json:"event"`
	Resolution *AbstainResolution `json:"resolution,omitempty"`
}

// AbstainResolution is the closed-resolution record of a resolved abstention.
type AbstainResolution struct {
	// ResolvedAs names one of AbstainResolutionValues.
	ResolvedAs string `json:"resolvedAs"`
	// By is the human reviewer id — non-empty.
	By string `json:"by"`
	// Note is free text; may be empty.
	Note string `json:"note"`
}

// LabeledDataPoint is the labeled data point a resolved abstention becomes
// (§5.2, §8.7) — the (answers, reason, resolution) shape the evaluation split
// consumes. Carries `policy_version` + `decision_id` so evaluation
// distinguishes "the policy was wrong" from "the presentation was wrong"
// (§8.7). No clock here — Phase 10's recorder adds timestamps at persistence.
type LabeledDataPoint struct {
	EventID       string                    `json:"event_id"`
	DecisionID    string                    `json:"decision_id"`
	PolicyVersion int                       `json:"policy_version"`
	Reason        schemas.AbstainReason     `json:"reason"`
	Answers       map[string]schemas.Answer `json:"answers"`
	Resolution    *AbstainResolution        `json:"resolution"`
}

// ResolveAbstainEventID is the deterministic event id (TS
// resolveAbstainEventId): `a_<hex16>` over sha256(decision_id ⊕ NUL ⊕ reason).
func ResolveAbstainEventID(decisionID string, reason string) string {
	digest := docutil.ContentHash(decisionID + "\x00" + reason)
	return "a_" + digest[:16]
}

// RecordAbstainInput is the recordAbstain input (TS RecordAbstainInput).
type RecordAbstainInput struct {
	// Decision is the abstained decision (`abstain: true`, non-null
	// decision-level `abstain_reason`).
	Decision *schemas.Decision
	// Reason is the queueable reason. Untrusted input is typed as the full
	// AbstainReason union on purpose: the two-tier discipline is enforced at
	// RUNTIME (fail closed), so an operational reason is rejected with code
	// 'operational_reason' rather than being unrepresentable at compile time.
	Reason schemas.AbstainReason
	// Evidence is attached — REQUIRED non-empty and machine-resolvable (§5.2).
	Evidence []schemas.Evidence
}

// ResolveAbstainInput is the resolveAbstain input (TS ResolveAbstainInput).
type ResolveAbstainInput struct {
	ResolvedAs string
	By         string
	Note       string
}

// InMemoryAbstainQueueOptions configures the in-memory queue (TS
// InMemoryAbstainQueueOptions).
type InMemoryAbstainQueueOptions struct {
	// OnLabeledDataPoint is the recorded-data-point hook (P2-T14): invoked
	// with every labeled data point as it is resolved. Phase 5/10 wire
	// persistence/evaluation here. The hook runs OUTSIDE the queue lock; a
	// panicking hook propagates to the caller (fail closed — the resolution
	// IS stored; the orchestrator owns degradation).
	OnLabeledDataPoint func(dataPoint *LabeledDataPoint)
}

// InMemoryAbstainQueue is the in-memory AbstainQueue — deterministic,
// clock-free, no fs. See the module doc for the two-tier discipline and the
// idempotence contract. Safe for concurrent use (TS is single-threaded; the
// shared map gets a mutex per CONVENTIONS.md §Concurrency).
type InMemoryAbstainQueue struct {
	mu                 sync.Mutex
	entries            map[string]*AbstainQueueEntry
	order              []string
	onLabeledDataPoint func(dataPoint *LabeledDataPoint)
}

// NewInMemoryAbstainQueue builds the queue (TS constructor with default
// options).
func NewInMemoryAbstainQueue(options ...InMemoryAbstainQueueOptions) *InMemoryAbstainQueue {
	queue := &InMemoryAbstainQueue{entries: make(map[string]*AbstainQueueEntry)}
	if len(options) > 0 {
		queue.onLabeledDataPoint = options[0].OnLabeledDataPoint
	}
	return queue
}

// RecordAbstain records a decision-level abstention. Validates the tier
// (operational reasons bypass the queue), the reason/decision consistency, and
// the evidence attachment. Idempotent on the deterministic event id: recording
// the same abstention twice returns the first event unchanged (no flood).
//
// The value return matches the orchestrator's declared AbstainQueuePick seam
// (`RecordAbstain(input RecordAbstainInput) (AbstainEvent, error)`, the Go
// form of TS Pick<AbstainQueue,'recordAbstain'>).
func (q *InMemoryAbstainQueue) RecordAbstain(input RecordAbstainInput) (AbstainEvent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	decision := input.Decision
	if decision == nil || !decision.Abstain || decision.AbstainReason == nil {
		return AbstainEvent{}, newAbstainQueueError(
			AbstainQueueCodeDecisionNotAbstained,
			"only abstained decisions are queueable: decision.abstain must be true with a non-null "+
				"abstain_reason (ARCH §5.2)",
		)
	}
	reason := *decision.AbstainReason
	if containsAbstainReason(schemas.OperationalAbstainReasons, reason) {
		return AbstainEvent{}, newAbstainQueueError(
			AbstainQueueCodeOperationalReason,
			"operational abstain reason '"+string(reason)+"' bypasses the review queue — "+
				"gateway-degradation records are bookkeeping for degraded runs, never review-queue items "+
				"and never policy-learning data (ARCH §5.2 two-tier taxonomy)",
		)
	}
	if !containsAbstainReason(schemas.DecisionLevelAbstainReasons, reason) {
		return AbstainEvent{}, newAbstainQueueError(
			AbstainQueueCodeDecisionNotAbstained,
			"decision.abstain_reason '"+string(reason)+"' is not a known abstain reason of "+
				"docsync.decision/v1",
		)
	}
	if input.Reason != reason {
		return AbstainEvent{}, newAbstainQueueError(
			AbstainQueueCodeReasonMismatch,
			"input reason '"+string(input.Reason)+"' does not match the decision's abstain_reason '"+
				string(reason)+"' — the queue stores the decision verbatim",
		)
	}
	if len(input.Evidence) == 0 {
		return AbstainEvent{}, newAbstainQueueError(
			AbstainQueueCodeEmptyEvidence,
			"an abstained case lands in the review queue WITH evidence attached (ARCH §5.2) — "+
				"evidence must be non-empty",
		)
	}
	// Machine-resolvable anchors only — same taxonomy as emission (defense in
	// depth).
	if err := AssertEvidenceResolvable(input.Evidence); err != nil {
		return AbstainEvent{}, err
	}
	eventID := ResolveAbstainEventID(decision.DecisionID, string(reason))
	if existing, ok := q.entries[eventID]; ok {
		// Idempotent duplicate (no flood): same decision + reason → same event.
		return existing.Event, nil
	}
	event := AbstainEvent{
		EventID:  eventID,
		Decision: decision,
		Reason:   reason,
		Evidence: append([]schemas.Evidence{}, input.Evidence...),
	}
	q.entries[eventID] = &AbstainQueueEntry{Event: event}
	q.order = append(q.order, eventID)
	return event, nil
}

// ResolveAbstain resolves a recorded event into a labeled data point. Once
// only.
func (q *InMemoryAbstainQueue) ResolveAbstain(
	event AbstainEvent,
	resolution ResolveAbstainInput,
) (*LabeledDataPoint, error) {
	// State mutation under the lock; the hook runs outside it.
	dataPoint, err := q.resolveUnderLock(event, resolution)
	if err != nil {
		return nil, err
	}
	if q.onLabeledDataPoint != nil {
		q.onLabeledDataPoint(dataPoint)
	}
	return dataPoint, nil
}

func (q *InMemoryAbstainQueue) resolveUnderLock(
	event AbstainEvent,
	resolution ResolveAbstainInput,
) (*LabeledDataPoint, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	entry, ok := q.entries[event.EventID]
	if !ok {
		return nil, newAbstainQueueError(
			AbstainQueueCodeEventNotFound,
			"abstain event '"+event.EventID+"' is not queued — record it before resolving",
		)
	}
	if entry.Resolution != nil {
		return nil, newAbstainQueueError(
			AbstainQueueCodeAlreadyResolved,
			"abstain event '"+event.EventID+"' is already resolved (resolvedAs '"+
				entry.Resolution.ResolvedAs+"') — one resolution per event",
		)
	}
	if !containsString(AbstainResolutionValues, resolution.ResolvedAs) {
		return nil, newAbstainQueueError(
			AbstainQueueCodeInvalidResolution,
			"resolvedAs '"+resolution.ResolvedAs+"' is not a member of the closed resolution set ["+
				strings.Join(AbstainResolutionValues, ", ")+"]",
		)
	}
	if resolution.By == "" {
		return nil, newAbstainQueueError(
			AbstainQueueCodeInvalidResolution,
			"resolution.by must be a non-empty reviewer id",
		)
	}
	// The TS `typeof resolution.note !== 'string'` check is enforced by Go's
	// type system: Note is a string field, so a non-string note (the TS test's
	// `note: 42` case) is a compile-time error, not a runtime code path.
	normalized := &AbstainResolution{
		ResolvedAs: resolution.ResolvedAs,
		By:         resolution.By,
		Note:       resolution.Note,
	}
	entry.Resolution = normalized
	// The labeled data point is built from the QUEUED event — the queue stores
	// the decision verbatim, and TS reads the very object RecordAbstain
	// handed the caller. Reading the stored entry (rather than the passed
	// argument) keeps the nil-unsafe TS deref a non-reachable path in Go.
	stored := entry.Event
	dataPoint := &LabeledDataPoint{
		EventID:       stored.EventID,
		DecisionID:    stored.Decision.DecisionID,
		PolicyVersion: stored.Decision.PolicyVersion,
		Reason:        stored.Reason,
		Answers:       stored.Decision.Answers,
		Resolution:    normalized,
	}
	return dataPoint, nil
}

// List returns all entries in record order — pending first-in-first-out,
// resolved marked (TS `list()`).
func (q *InMemoryAbstainQueue) List() []AbstainQueueEntry {
	q.mu.Lock()
	defer q.mu.Unlock()

	entries := make([]AbstainQueueEntry, 0, len(q.order))
	for _, eventID := range q.order {
		if entry, ok := q.entries[eventID]; ok {
			entries = append(entries, *entry)
		}
	}
	return entries
}

// Get returns one entry by deterministic event id, or nil (TS `get()`).
func (q *InMemoryAbstainQueue) Get(eventID string) *AbstainQueueEntry {
	q.mu.Lock()
	defer q.mu.Unlock()

	if entry, ok := q.entries[eventID]; ok {
		found := *entry
		return &found
	}
	return nil
}

func containsAbstainReason(reasons []schemas.AbstainReason, reason schemas.AbstainReason) bool {
	for _, candidate := range reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
