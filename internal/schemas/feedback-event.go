// Feedback event — `docsync.feedback-event/v1` (P10-T01; ARCH §8.7 five
// reviewer signals; ARCH §5.4 decision identity).
//
// Ported from packages/schemas/src/v1/feedback-event.ts
// (FEEDBACK_EVENT_SCHEMA_ID, FEEDBACK_EVENT_VALUES, FEEDBACK_EVENT_SIGNAL,
// FEEDBACK_EVENT_SCHEMA, FEEDBACK_EVENT_EXAMPLE).
//
// A feedback event is the atomic reviewer signal observed on a doc PR —
// accept, edit, reject, mark_as_intentional, always_ignore — recorded with the
// decision that produced the reviewed patch so the Phase 10 feedback loop can
// attribute it to a policy version (D7: evaluation/improvement loop input).
//
// The TS module is exported selectively via index.ts (FEEDBACK_EVENT_EXAMPLE,
// FEEDBACK_EVENT_SCHEMA, FEEDBACK_EVENT_SCHEMA_ID, FEEDBACK_EVENT_SIGNAL,
// FEEDBACK_EVENT_VALUES plus the FeedbackEvent/FeedbackEventSignal types) —
// this Go file just defines the symbols; barrels are orchestrator-owned.
//
// Shape notes:
//   - Flat envelope with a literal `schema` tag (OD-R01 strict, OD-2
//     vocabulary reuse), mirroring provenance.
//   - Closed five-signal union (ARCH §8.7): an unknown signal is a bug, not a
//     forward-compat option — fail closed.
//   - Conditional evidence rules (superRefine, fail closed): a `reject`
//     signal carries its `reject_reason`; an `edit` signal carries its
//     `edit_diff` — the edit diff is gold (the OD-R14 evidence basis for the
//     corrected-vs-generated comparison in the evaluation loop).
//   - `at` is an injected instant supplied by the emitter — never generated
//     here (no wall-clock reads anywhere).
//
// The ULID-shaped identifier vocabulary (TS ULID_REGEX
// /^[0-9A-HJKMNP-TV-Z]{26}$/) is realized through the single canonical
// predicate docutil.IsValidULID (internal/docutil), same as the claim-proposal
// port — the pattern is not duplicated in this package.
package schemas

import "github.com/DUDE616/docSync-go/internal/docutil"

// FeedbackEventSchemaID is the literal schema tag of
// `docsync.feedback-event/v1` (z.literal).
const FeedbackEventSchemaID = "docsync.feedback-event/v1"

// FeedbackEventSignal is one member of the frozen reviewer-signal union — the
// complete ARCH §8.7 five-signal vocabulary (FEEDBACK_EVENT_SIGNAL).
type FeedbackEventSignal string

// The five reviewer signals in canonical order (FEEDBACK_EVENT_VALUES).
const (
	FeedbackEventSignalAccept            FeedbackEventSignal = "accept"
	FeedbackEventSignalEdit              FeedbackEventSignal = "edit"
	FeedbackEventSignalReject            FeedbackEventSignal = "reject"
	FeedbackEventSignalMarkAsIntentional FeedbackEventSignal = "mark_as_intentional"
	FeedbackEventSignalAlwaysIgnore      FeedbackEventSignal = "always_ignore"
)

// FeedbackEventValues is the frozen reviewer-signal union — the complete
// ARCH §8.7 five-signal vocabulary.
var FeedbackEventValues = []FeedbackEventSignal{
	FeedbackEventSignalAccept,
	FeedbackEventSignalEdit,
	FeedbackEventSignalReject,
	FeedbackEventSignalMarkAsIntentional,
	FeedbackEventSignalAlwaysIgnore,
}

// IsValidFeedbackEventSignal reports membership in FEEDBACK_EVENT_SIGNAL.
func IsValidFeedbackEventSignal(s string) bool {
	for _, v := range FeedbackEventValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// FeedbackEvent is one reviewer signal event — written once per observed
// signal, append-only (FEEDBACK_EVENT_SCHEMA, z.infer FeedbackEvent).
type FeedbackEvent struct {
	Schema        string              `json:"schema"`
	EventID       string              `json:"event_id"`
	At            string              `json:"at"`
	DecisionID    string              `json:"decision_id"`
	PolicyVersion int                 `json:"policy_version"`
	Signal        FeedbackEventSignal `json:"signal"`
	// RejectReason is REQUIRED when signal === 'reject' (superRefine): why the
	// patch was rejected.
	RejectReason *string `json:"reject_reason,omitempty"`
	// EditDiff is REQUIRED when signal === 'edit' (superRefine): the edit diff
	// is gold (OD-R14).
	EditDiff *string `json:"edit_diff,omitempty"`
	RunID    *string `json:"run_id,omitempty"`
	DocPath  *string `json:"doc_path,omitempty"`
	Repo     *string `json:"repo,omitempty"`
}

// Validate checks the event against FEEDBACK_EVENT_SCHEMA (strict),
// reproducing the zod issue paths and the two superRefine rules.
func (e *FeedbackEvent) Validate() []Issue {
	var issues []Issue
	if e.Schema != FeedbackEventSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.feedback-event/v1"`})
	}
	// z.string().regex(ULID_REGEX) — the canonical Crockford-base32 predicate.
	if !docutil.IsValidULID(e.EventID) {
		Collect(&issues, &Issue{Path: []string{"event_id"}, Message: "Invalid"})
	}
	Collect(&issues, datetimeIssue(e.At, []string{"at"}))
	Collect(&issues, RequiredString(e.DecisionID, 1, []string{"decision_id"}))
	if e.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, CheckEnum(string(e.Signal), stringsOf(FeedbackEventValues), []string{"signal"}))
	if e.RejectReason != nil {
		Collect(&issues, RequiredString(*e.RejectReason, 1, []string{"reject_reason"}))
	}
	if e.EditDiff != nil {
		Collect(&issues, RequiredString(*e.EditDiff, 1, []string{"edit_diff"}))
	}
	if e.RunID != nil {
		Collect(&issues, RequiredString(*e.RunID, 1, []string{"run_id"}))
	}
	if e.DocPath != nil {
		Collect(&issues, RequiredString(*e.DocPath, 1, []string{"doc_path"}))
	}
	if e.Repo != nil {
		Collect(&issues, RequiredString(*e.Repo, 1, []string{"repo"}))
	}

	// Rule 1 (ARCH §8.7): a rejection without its reason is an assertion, not
	// evidence — the reason is what lets hygiene map reject→label.
	if e.Signal == FeedbackEventSignalReject && e.RejectReason == nil {
		Collect(&issues, &Issue{
			Path:    []string{"reject_reason"},
			Message: "signal 'reject' requires a non-empty reject_reason",
		})
	}

	// Rule 2 (ARCH §8.7; OD-R14): an edit without the stored edit diff cannot
	// feed the corrected-vs-generated comparison — fail closed.
	if e.Signal == FeedbackEventSignalEdit && e.EditDiff == nil {
		Collect(&issues, &Issue{
			Path:    []string{"edit_diff"},
			Message: "signal 'edit' requires a non-empty edit_diff (the edit diff is gold)",
		})
	}
	return issues
}

// ParseFeedbackEvent strictly decodes and validates `docsync.feedback-event/v1`
// JSON bytes (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseFeedbackEvent(data []byte) (*FeedbackEvent, error) {
	var e FeedbackEvent
	if err := StrictDecode(data, &e); err != nil {
		return nil, err
	}
	if issues := e.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &e, nil
}

// FeedbackEventExample returns the validated example mirroring a reviewer
// editing the generated patch (FEEDBACK_EVENT_EXAMPLE; registered as
// 'docsync.feedback-event' v1).
func FeedbackEventExample() FeedbackEvent {
	return FeedbackEvent{
		Schema:        FeedbackEventSchemaID,
		EventID:       "01J9ZM2Q7AWX4B6C8D0E2F4G6H",
		At:            "2026-10-05T09:14:00Z",
		DecisionID:    "d_01J9ZK7Q2M",
		PolicyVersion: 17,
		Signal:        FeedbackEventSignalEdit,
		EditDiff:      new("-rate = 0.19\n+rate = 0.21"),
		RunID:         new("run_01J9ZK6X8A"),
		DocPath:       new("docs/payments.md"),
		Repo:          new("acme/widgets"),
	}
}
