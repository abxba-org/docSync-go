// Feedback recorder (P10-T02; ARCH §8.7; CTX §23).
//
// Ported from core/src/feedback/recorder.ts. The recorder is the ONE write
// path for the five reviewer signals — accept, edit, reject,
// mark_as_intentional, always_ignore — observed on a doc PR. Each signal is
// recorded as an append-only `docsync.feedback-event/v1` row under
// `<stateDir>/feedback/events.jsonl` (OD-R22), validated against
// FEEDBACK_EVENT_SCHEMA before persistence (fail closed: malformed evidence
// is refused, never stored).
//
// Identity & time are injected, never generated behind the caller's back:
// `event_id` defaults to a canonical ULID (docutil.ULIDGenerator), `at`
// defaults to the injected clock — there is no `new Date()` anywhere in this
// module and no `time.Now()` here.
//
// The hygiene normalization table (PLAN §11.3 F10-2) is implemented ONCE in
// hygiene.go, before any counting; the recorder only captures RAW rows.

package feedback

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// FeedbackEventSchemaID is the RAW feedback event row schema id
// (`docsync.feedback-event/v1`; TS FEEDBACK_EVENT_SCHEMA_ID, declared in
// recorder.ts). The twin in internal/schemas is unexported
// (schemas/feedback-event.go owns the schema itself); the literal is carried
// here so this package can tag rows without reaching into another package's
// internals.
const FeedbackEventSchemaID = "docsync.feedback-event/v1"

// FeedbackEventInput is the input of FeedbackRecorder.Record: a feedback event
// minus the generated fields (TS FeedbackEventInput). Nil pointers mean
// "absent" (omitted from the event, exactly like the TS conditional spread).
type FeedbackEventInput struct {
	EventID       *string
	At            *string
	DecisionID    string
	PolicyVersion int
	Signal        schemas.FeedbackEventSignal
	RejectReason  *string
	EditDiff      *string
	RunID         *string
	DocPath       *string
	Repo          *string
}

// FeedbackRecorderOptions carries the recorder wiring (TS
// FeedbackRecorderOptions).
type FeedbackRecorderOptions struct {
	// StateDir is the ABSOLUTE path to `.docsync/state` (D1; OD-R22 rows under
	// `state/feedback/`).
	StateDir string
	// Clock is the injected time source: returns the current ISO-8601 instant
	// (never new Date()). Optional — every row may also carry its own `At`.
	Clock func() string
	// Sink is the injected sink override (tests); defaults to the JSONL file
	// sink at `<stateDir>/feedback/events.jsonl`.
	Sink JsonlSink
	// EventIDs overrides the event-id generator (tests); defaults to a
	// canonical ULID generator owned by the recorder.
	EventIDs *docutil.ULIDGenerator
}

// FeedbackRecorder is the feedback capture surface: append-only RAW rows +
// fail-closed replay (TS FeedbackRecorder).
type FeedbackRecorder struct {
	sink     JsonlSink
	clock    func() string
	eventIDs *docutil.ULIDGenerator
}

// ParseFeedbackEvent parses one RAW feedback event row; it returns a
// FeedbackValidationError on drift (TS parseFeedbackEvent). The row is
// re-serialized through the strict `docsync.feedback-event/v1` parser, so
// unknown keys and every superRefine rule fail closed.
func ParseFeedbackEvent(value any, origin string) (*schemas.FeedbackEvent, error) {
	data, err := marshalRowCompact(value)
	if err != nil {
		return nil, newFeedbackValidationError(fmt.Sprintf(
			"feedback event refused at %s (fail closed): %v", origin, err))
	}
	event, err := schemas.ParseFeedbackEvent(data)
	if err != nil {
		return nil, newFeedbackValidationError(fmt.Sprintf(
			"feedback event refused at %s (fail closed): %s", origin, err))
	}
	return event, nil
}

// CreateFeedbackRecorder builds the feedback recorder over an append-only
// JSONL sink at `<stateDir>/feedback/events.jsonl` (OD-R22; same deliberate
// StateStore-bypass rationale as the incident log — append-only semantics).
func CreateFeedbackRecorder(options FeedbackRecorderOptions) (*FeedbackRecorder, error) {
	sink := options.Sink
	if sink == nil {
		fileSink, err := CreateJsonlSink(options.StateDir, FEEDBACK_EVENTS_FILENAME)
		if err != nil {
			return nil, err
		}
		sink = fileSink
	}
	eventIDs := options.EventIDs
	if eventIDs == nil {
		eventIDs = docutil.NewULIDGenerator()
	}
	return &FeedbackRecorder{sink: sink, clock: options.Clock, eventIDs: eventIDs}, nil
}

// Record validates and appends one feedback event; it returns the stored row
// (TS FeedbackRecorder.record). A validation failure stores nothing.
func (r *FeedbackRecorder) Record(input FeedbackEventInput) (*schemas.FeedbackEvent, error) {
	candidate := schemas.FeedbackEvent{
		Schema:        FeedbackEventSchemaID,
		DecisionID:    input.DecisionID,
		PolicyVersion: input.PolicyVersion,
		Signal:        input.Signal,
		RejectReason:  input.RejectReason,
		EditDiff:      input.EditDiff,
		RunID:         input.RunID,
		DocPath:       input.DocPath,
		Repo:          input.Repo,
	}
	// Identity & time are injected, never generated behind the caller's back
	// (TS: `input.event_id ?? generateUlid()`, `input.at ?? options.clock()`).
	if input.EventID != nil {
		candidate.EventID = *input.EventID
	} else {
		generated, err := r.eventIDs.Generate()
		if err != nil {
			return nil, docutil.Newf(docutil.CodeInternal, "feedback event id generation failed: %v", err)
		}
		candidate.EventID = generated
	}
	if input.At != nil {
		candidate.At = *input.At
	} else if r.clock != nil {
		candidate.At = r.clock()
	}
	event, err := ParseFeedbackEvent(candidate, "recorder.record")
	if err != nil {
		return nil, err
	}
	if err := r.sink.Append(event); err != nil {
		return nil, err
	}
	return event, nil
}

// List replays every stored feedback event (TS FeedbackRecorder.list without
// the filter). A missing file replays as empty; a malformed line errors (fail
// closed).
func (r *FeedbackRecorder) List() ([]schemas.FeedbackEvent, error) {
	return ReadAllRows(r.sink, func(value any, lineNumber int) (schemas.FeedbackEvent, error) {
		event, err := ParseFeedbackEvent(value, fmt.Sprintf("recorder.list line %d", lineNumber))
		if err != nil {
			return schemas.FeedbackEvent{}, err
		}
		return *event, nil
	})
}

// ListByDecisionID replays every stored feedback event originating from
// decisionID (TS FeedbackRecorder.list with the filter).
func (r *FeedbackRecorder) ListByDecisionID(decisionID string) ([]schemas.FeedbackEvent, error) {
	rows, err := r.List()
	if err != nil {
		return nil, err
	}
	return listFiltered(rows, decisionID, func(row schemas.FeedbackEvent) string { return row.DecisionID }), nil
}

// listFiltered is the shared filter body of the recorder/store list variants
// (TS `.filter((row) => row.decision_id === decisionId)`).
func listFiltered[T any](rows []T, decisionID string, idOf func(T) string) []T {
	filtered := make([]T, 0, len(rows))
	for _, row := range rows {
		if idOf(row) == decisionID {
			filtered = append(filtered, row)
		}
	}
	return filtered
}
