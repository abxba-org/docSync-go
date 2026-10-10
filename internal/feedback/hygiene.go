// Label-hygiene normalizer (P10-T03; ARCH §8.7; PLAN §11.3 F10-2).
//
// Ported from core/src/feedback/hygiene.ts. Implicit reviewer signals are
// ambiguous ("a reviewer ignoring a suggestion may mean wrong or busy"), so the
// recorder normalizes labels ONCE, before any counting, via this table (each
// row is a `docsync.hygiene-label/v1` value):
//
//	| source signal            | condition                      | label            | hygiene_rule                                        |
//	|--------------------------|--------------------------------|------------------|-----------------------------------------------------|
//	| `timeout` (opts)         | observation window expired     | `unlabeled`      | `timeout⇒unlabeled` — NEVER `accepted` (V-P10-2)    |
//	| `edit`                   | —                              | `corrected`      | `edit⇒corrected` (the edit diff is gold — stored)   |
//	| `reject`                 | reason = "doc already correct" | `false_positive` | `reject_reason:doc_already_correct⇒false_positive`  |
//	| `reject`                 | any other reason               | `style_rejected` | `reject_reason:other⇒style_rejected`                |
//	| `accept`                 | —                              | `accepted`       | `direct`                                            |
//	| `mark_as_intentional`    | —                              | `accepted`       | `direct` (the update was intentional — the doc WAS  |
//	|                          |                                |                  | stale, so the generated patch was right)            |
//	| `always_ignore`          | —                              | `style_rejected` | `always_ignore⇒aggregation`                         |
//
// ALWAYS-IGNORE DECISION (declared): `always_ignore` is an override pattern,
// not a per-decision quality verdict — the reviewer asks for the PATTERN to
// stop (F10-4 aggregation input), so it is never `accepted`. It normalizes to
// `style_rejected` carrying the distinct rule `always_ignore⇒aggregation` so
// the aggregator (overrides.go) can pick these rows up as counted evidence
// while evaluation still distinguishes them from style rejections via the rule
// name.
//
// The `timeout` path (V-P10-2): when opts.timeoutSeconds is supplied the
// observation window expired — absence of evidence. The row normalizes to
// `unlabeled` REGARDLESS of any weak signal carried on the event: a stale
// accept must never promote an expired observation window to `accepted` (the
// HYGIENE_LABEL_SCHEMA superRefine invariant makes timeout⇒accepted
// unrepresentable). Labels are written append-only to
// `<stateDir>/feedback/labels.jsonl` (OD-R22).

package feedback

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// hygieneLabelSchemaID is the hygiene-label row schema id
// (`docsync.hygiene-label/v1`); the twin in internal/schemas is unexported
// (schemas/hygiene-label.go owns the schema itself).
const hygieneLabelSchemaID = "docsync.hygiene-label/v1"

// DOC_ALREADY_CORRECT_REASON is the canonical FP reject reason (ARCH §8.7):
// "the documentation was already correct".
const DOC_ALREADY_CORRECT_REASON = "doc already correct"

// Hygiene rules applied by the table above — kept as named constants so the
// grammar (schemas.IsValidHygieneRule) and the TS text cannot drift apart.
const (
	hygieneRuleTimeoutUnlabeled = "timeout⇒unlabeled"
	hygieneRuleDirect           = "direct"
	hygieneRuleEditCorrected    = "edit⇒corrected"
	hygieneRuleRejectDocCorrect = "reject_reason:doc_already_correct⇒false_positive"
	hygieneRuleRejectOther      = "reject_reason:other⇒style_rejected"
	hygieneRuleAlwaysIgnore     = "always_ignore⇒aggregation"
	hygieneRulePostMergeEdit    = "post_merge_edit⇒true_negative"
)

// newLabelID mints the default `label_id` (TS generateUlid).
func newLabelID() (string, error) {
	return generateID("hygiene label")
}

// IsDocAlreadyCorrect reports whether the stated reject reason IS "the
// documentation was already correct" (ARCH §8.7 FP definition). Matching is
// exact after collapsing whitespace and case-folding — a semantically
// different reason is a style rejection, never silently reinterpreted (fail
// closed: abstain, don't guess).
func IsDocAlreadyCorrect(reason string) bool {
	return collapseWhitespaceFold(reason) == DOC_ALREADY_CORRECT_REASON
}

// NormalizeOptions are the options of NormalizeFeedbackEvent beyond the event
// itself (TS NormalizeOptions).
type NormalizeOptions struct {
	// TimeoutSeconds is supplied when the observation window expired — its
	// PRESENCE (not the value) forces `unlabeled` (TS: `!== undefined`).
	TimeoutSeconds *int
	// EditDiff is the edit-diff fallback when the capture layer carries the
	// diff out-of-band.
	EditDiff *string
	// RejectReason is the reject-reason fallback when the capture layer
	// carries the reason out-of-band.
	RejectReason *string
}

// NormalizeContext is the identity/time context for normalization — injected,
// never generated here (TS NormalizeContext).
type NormalizeContext struct {
	// At is the ISO-8601 instant for `normalized_at` (injected clock value).
	At string
	// LabelID is the optional pre-assigned `label_id`; defaults to a canonical
	// ULID.
	LabelID *string
}

// ParseHygieneLabel parses one hygiene label row; it returns a
// FeedbackValidationError on drift (TS parseHygieneLabel). The row is
// re-serialized through the strict `docsync.hygiene-label/v1` parser, so
// unknown keys and the timeout-never-accepted invariant fail closed.
func ParseHygieneLabel(value any, origin string) (*schemas.HygieneLabel, error) {
	data, err := marshalRowCompact(value)
	if err != nil {
		return nil, newFeedbackValidationError(fmt.Sprintf(
			"hygiene label refused at %s (fail closed): %v", origin, err))
	}
	label, err := schemas.ParseHygieneLabel(data)
	if err != nil {
		return nil, newFeedbackValidationError(fmt.Sprintf(
			"hygiene label refused at %s (fail closed): %s", origin, err))
	}
	return label, nil
}

// NormalizeFeedbackEvent applies the ARCH §8.7 hygiene table ONCE (module
// header) to one feedback event, producing the normalized ground-truth label
// the evaluation loop consumes. The context supplies `normalized_at` (injected
// clock value) and optionally the `label_id`.
func NormalizeFeedbackEvent(event schemas.FeedbackEvent, options NormalizeOptions, context NormalizeContext) (*schemas.HygieneLabel, error) {
	var label schemas.HygieneLabelName
	var sourceSignal schemas.HygieneSourceSignal
	var hygieneRule string
	var editDiff *string

	if options.TimeoutSeconds != nil {
		// Observation window expired: absence of evidence, never acceptance.
		label = schemas.HygieneLabelUnlabeled
		sourceSignal = schemas.HygieneSourceTimeout
		hygieneRule = hygieneRuleTimeoutUnlabeled
	} else {
		switch event.Signal {
		case schemas.FeedbackEventSignalAccept, schemas.FeedbackEventSignalMarkAsIntentional:
			// mark_as_intentional: the doc WAS stale but the change was
			// intentional — the generated patch was right → `accepted`.
			label = schemas.HygieneLabelAccepted
			sourceSignal = schemas.HygieneSourceSignal(event.Signal)
			hygieneRule = hygieneRuleDirect
		case schemas.FeedbackEventSignalEdit:
			label = schemas.HygieneLabelCorrected
			sourceSignal = schemas.HygieneSourceEdit
			hygieneRule = hygieneRuleEditCorrected
			editDiff = event.EditDiff
			if editDiff == nil {
				editDiff = options.EditDiff
			}
		case schemas.FeedbackEventSignalReject:
			reason := ""
			switch {
			case event.RejectReason != nil:
				reason = *event.RejectReason
			case options.RejectReason != nil:
				reason = *options.RejectReason
			}
			if IsDocAlreadyCorrect(reason) {
				label = schemas.HygieneLabelFalsePositive
				hygieneRule = hygieneRuleRejectDocCorrect
			} else {
				label = schemas.HygieneLabelStyleRejected
				hygieneRule = hygieneRuleRejectOther
			}
			sourceSignal = schemas.HygieneSourceReject
		case schemas.FeedbackEventSignalAlwaysIgnore:
			// Override pattern, not a quality verdict (see module header): the
			// aggregator consumes these rows as counted evidence (F10-4).
			label = schemas.HygieneLabelStyleRejected
			sourceSignal = schemas.HygieneSourceAlwaysIgnore
			hygieneRule = hygieneRuleAlwaysIgnore
		default:
			// Unreachable through a validated event (the signal union is
			// closed): fail closed — the row refuses to normalize instead of
			// guessing a label for an unknown signal.
			label = schemas.HygieneLabelName(event.Signal)
			sourceSignal = schemas.HygieneSourceSignal(event.Signal)
			hygieneRule = hygieneRuleDirect
		}
	}

	candidate := schemas.HygieneLabel{
		Schema:        hygieneLabelSchemaID,
		DecisionID:    event.DecisionID,
		PolicyVersion: event.PolicyVersion,
		Label:         label,
		SourceSignal:  sourceSignal,
		EditDiff:      editDiff,
		HygieneRule:   hygieneRule,
		NormalizedAt:  context.At,
	}
	if context.LabelID != nil {
		candidate.LabelID = *context.LabelID
	} else {
		generated, err := newLabelID()
		if err != nil {
			return nil, err
		}
		candidate.LabelID = generated
	}
	return ParseHygieneLabel(candidate, "hygiene.normalize")
}

// HygieneLabelStoreOptions carries the label-store wiring (TS
// HygieneLabelStoreOptions).
type HygieneLabelStoreOptions struct {
	// StateDir is the ABSOLUTE path to `.docsync/state` (D1; OD-R22 rows under
	// `state/feedback/`).
	StateDir string
	// Sink is the injected sink override (tests); defaults to the JSONL file
	// sink at `<stateDir>/feedback/labels.jsonl`.
	Sink JsonlSink
}

// HygieneLabelStore is the hygiene-label persistence surface: append-only rows
// + fail-closed replay (TS HygieneLabelStore).
type HygieneLabelStore struct {
	sink JsonlSink
}

// CreateHygieneLabelStore builds the hygiene-label store over an append-only
// JSONL sink at `<stateDir>/feedback/labels.jsonl` (OD-R22; curated artifacts
// live under `evaluation/`, not here).
func CreateHygieneLabelStore(options HygieneLabelStoreOptions) (*HygieneLabelStore, error) {
	sink := options.Sink
	if sink == nil {
		fileSink, err := CreateJsonlSink(options.StateDir, FEEDBACK_LABELS_FILENAME)
		if err != nil {
			return nil, err
		}
		sink = fileSink
	}
	return &HygieneLabelStore{sink: sink}, nil
}

// Write validates and appends one normalized label; it returns the stored row
// (TS HygieneLabelStore.write).
func (s *HygieneLabelStore) Write(label schemas.HygieneLabel) (*schemas.HygieneLabel, error) {
	valid, err := ParseHygieneLabel(label, "hygiene.write")
	if err != nil {
		return nil, err
	}
	if err := s.sink.Append(valid); err != nil {
		return nil, err
	}
	return valid, nil
}

// List replays every stored label (TS HygieneLabelStore.list without the
// filter). A missing file replays as empty; a malformed line errors (fail
// closed).
func (s *HygieneLabelStore) List() ([]schemas.HygieneLabel, error) {
	return ReadAllRows(s.sink, func(value any, lineNumber int) (schemas.HygieneLabel, error) {
		label, err := ParseHygieneLabel(value, fmt.Sprintf("hygiene.list line %d", lineNumber))
		if err != nil {
			return schemas.HygieneLabel{}, err
		}
		return *label, nil
	})
}

// ListByDecisionID replays every stored label bound to decisionID
// (TS HygieneLabelStore.list with the filter).
func (s *HygieneLabelStore) ListByDecisionID(decisionID string) ([]schemas.HygieneLabel, error) {
	rows, err := s.List()
	if err != nil {
		return nil, err
	}
	return listFiltered(rows, decisionID, func(row schemas.HygieneLabel) string { return row.DecisionID }), nil
}
