// Hygiene label — `docsync.hygiene-label/v1` (P10-T01; ARCH §8.7 label
// hygiene).
//
// Ported from packages/schemas/src/v1/hygiene-label.ts
// (HYGIENE_LABEL_SCHEMA_ID, HYGIENE_LABEL_VALUES, HYGIENE_LABEL,
// HYGIENE_SOURCE_SIGNAL_VALUES, HYGIENE_SOURCE_SIGNAL, HYGIENE_RULE_REGEX,
// HYGIENE_LABEL_SCHEMA, HYGIENE_LABEL_EXAMPLE).
//
// A hygiene label is the normalized, deduplicated state of a decision after
// reviewer signals and hygiene rules are folded in: it is the unit the Phase
// 10 evaluation consumes as ground truth. The `hygiene_rule` names the rule
// that produced the label (e.g. `timeout⇒unlabeled`, `edit⇒corrected`,
// `reject_reason:doc_already_correct⇒false_positive`,
// `reject_reason:other⇒style_rejected`,
// `post_merge_edit⇒false_negative_true_negative`, or `direct`) so every label
// is traceable to its derivation, not an assertion.
//
// Shape notes:
//   - Closed six-label union and a closed source-signal union (the ARCH §8.7
//     five reviewer signals plus the four non-reviewer sources `timeout`,
//     `post_merge_edit`, `spot_check`, `audit_finding`) — reuse, never
//     duplicate, the feedback-event signal vocabulary (OD-2).
//   - superRefine invariant (fail closed): a `timeout` source signal can never
//     normalize to `accepted` — an observation-window expiry is absence of
//     evidence, never acceptance (ARCH §8.7).
//   - `normalized_at` is an injected instant supplied by the emitter — never
//     generated here (no wall-clock reads anywhere).
//
// The ULID-shaped identifier vocabulary (TS ULID_REGEX, imported here from
// feedback-event.ts) is realized through the single canonical predicate
// docutil.IsValidULID — same pattern the feedback-event port uses.
package schemas

import (
	"regexp"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// HygieneLabelSchemaID is the literal schema tag of
// `docsync.hygiene-label/v1` (z.literal).
const HygieneLabelSchemaID = "docsync.hygiene-label/v1"

// HygieneLabelName is one member of the frozen normalized-label union — the
// complete ARCH §8.7 label vocabulary (HYGIENE_LABEL).
type HygieneLabelName string

// The six normalized labels in canonical order (HYGIENE_LABEL_VALUES).
const (
	HygieneLabelUnlabeled     HygieneLabelName = "unlabeled"
	HygieneLabelAccepted      HygieneLabelName = "accepted"
	HygieneLabelCorrected     HygieneLabelName = "corrected"
	HygieneLabelFalsePositive HygieneLabelName = "false_positive"
	HygieneLabelStyleRejected HygieneLabelName = "style_rejected"
	HygieneLabelTrueNegative  HygieneLabelName = "true_negative"
)

// HygieneLabelValues is the frozen normalized-label union — the complete
// ARCH §8.7 label vocabulary.
var HygieneLabelValues = []HygieneLabelName{
	HygieneLabelUnlabeled,
	HygieneLabelAccepted,
	HygieneLabelCorrected,
	HygieneLabelFalsePositive,
	HygieneLabelStyleRejected,
	HygieneLabelTrueNegative,
}

// IsValidHygieneLabelName reports membership in HYGIENE_LABEL.
func IsValidHygieneLabelName(s string) bool {
	for _, v := range HygieneLabelValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// HygieneSourceSignal is one member of the frozen source-signal union: the
// five reviewer signals + the four hygiene sources (HYGIENE_SOURCE_SIGNAL).
type HygieneSourceSignal string

// The nine source signals in canonical order (HYGIENE_SOURCE_SIGNAL_VALUES):
// the ARCH §8.7 five reviewer signals plus the four non-reviewer hygiene
// sources. The reviewer half is the FEEDBACK_EVENT_VALUES vocabulary verbatim
// (OD-2 reuse, never duplicated).
const (
	HygieneSourceAccept            HygieneSourceSignal = HygieneSourceSignal(FeedbackEventSignalAccept)
	HygieneSourceEdit              HygieneSourceSignal = HygieneSourceSignal(FeedbackEventSignalEdit)
	HygieneSourceReject            HygieneSourceSignal = HygieneSourceSignal(FeedbackEventSignalReject)
	HygieneSourceMarkAsIntentional HygieneSourceSignal = HygieneSourceSignal(FeedbackEventSignalMarkAsIntentional)
	HygieneSourceAlwaysIgnore      HygieneSourceSignal = HygieneSourceSignal(FeedbackEventSignalAlwaysIgnore)
	HygieneSourceTimeout           HygieneSourceSignal = "timeout"
	HygieneSourcePostMergeEdit     HygieneSourceSignal = "post_merge_edit"
	HygieneSourceSpotCheck         HygieneSourceSignal = "spot_check"
	HygieneSourceAuditFinding      HygieneSourceSignal = "audit_finding"
)

// HygieneSourceSignalValues is the frozen source-signal union: the five
// reviewer signals + the four hygiene sources.
var HygieneSourceSignalValues = []HygieneSourceSignal{
	HygieneSourceAccept,
	HygieneSourceEdit,
	HygieneSourceReject,
	HygieneSourceMarkAsIntentional,
	HygieneSourceAlwaysIgnore,
	HygieneSourceTimeout,
	HygieneSourcePostMergeEdit,
	HygieneSourceSpotCheck,
	HygieneSourceAuditFinding,
}

// IsValidHygieneSourceSignal reports membership in HYGIENE_SOURCE_SIGNAL.
func IsValidHygieneSourceSignal(s string) bool {
	for _, v := range HygieneSourceSignalValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// HygieneRuleRegex is the traceable-derivation grammar:
// `<source>[<:qualifier>]⇒<label>` or the bare `direct` marker for labels
// authored without an upstream signal. Covers every ARCH §8.7 example
// (`timeout⇒unlabeled`, `edit⇒corrected`,
// `reject_reason:doc_already_correct⇒false_positive`,
// `reject_reason:other⇒style_rejected`,
// `post_merge_edit⇒false_negative_true_negative`, `direct`).
var HygieneRuleRegex = regexp.MustCompile(`^(?:[a-z_]+(?::[a-z_]+)?⇒[a-z_]+|direct)$`)

// IsValidHygieneRule reports membership in the HYGIENE_RULE_REGEX grammar.
func IsValidHygieneRule(s string) bool {
	return HygieneRuleRegex.MatchString(s)
}

// HygieneLabel is one normalized decision label — the evaluation-loop ground
// truth unit (HYGIENE_LABEL_SCHEMA, z.infer HygieneLabel).
type HygieneLabel struct {
	Schema        string              `json:"schema"`
	LabelID       string              `json:"label_id"`
	DecisionID    string              `json:"decision_id"`
	PolicyVersion int                 `json:"policy_version"`
	Label         HygieneLabelName    `json:"label"`
	SourceSignal  HygieneSourceSignal `json:"source_signal"`
	EditDiff      *string             `json:"edit_diff,omitempty"`
	HygieneRule   string              `json:"hygiene_rule"`
	NormalizedAt  string              `json:"normalized_at"`
}

// Validate checks the label against HYGIENE_LABEL_SCHEMA (strict),
// reproducing the zod issue paths and the timeout-never-accepted invariant.
func (l *HygieneLabel) Validate() []Issue {
	var issues []Issue
	if l.Schema != HygieneLabelSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.hygiene-label/v1"`})
	}
	// z.string().regex(ULID_REGEX) — the canonical Crockford-base32 predicate.
	if !docutil.IsValidULID(l.LabelID) {
		Collect(&issues, &Issue{Path: []string{"label_id"}, Message: "Invalid"})
	}
	Collect(&issues, RequiredString(l.DecisionID, 1, []string{"decision_id"}))
	if l.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, CheckEnum(string(l.Label), stringsOf(HygieneLabelValues), []string{"label"}))
	Collect(&issues, CheckEnum(string(l.SourceSignal), stringsOf(HygieneSourceSignalValues), []string{"source_signal"}))
	if l.EditDiff != nil {
		Collect(&issues, RequiredString(*l.EditDiff, 1, []string{"edit_diff"}))
	}
	if !IsValidHygieneRule(l.HygieneRule) {
		Collect(&issues, &Issue{Path: []string{"hygiene_rule"}, Message: "Invalid"})
	}
	Collect(&issues, datetimeIssue(l.NormalizedAt, []string{"normalized_at"}))

	// Invariant (ARCH §8.7): timeout never accepted — an observation-window
	// expiry is absence of evidence, never acceptance; it normalizes to
	// `unlabeled` or lower, never `accepted`.
	if l.SourceSignal == HygieneSourceTimeout && l.Label == HygieneLabelAccepted {
		Collect(&issues, &Issue{
			Path:    []string{"label"},
			Message: "source_signal 'timeout' can never normalize to 'accepted' (ARCH §8.7)",
		})
	}
	return issues
}

// ParseHygieneLabel strictly decodes and validates `docsync.hygiene-label/v1`
// JSON bytes (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseHygieneLabel(data []byte) (*HygieneLabel, error) {
	var l HygieneLabel
	if err := StrictDecode(data, &l); err != nil {
		return nil, err
	}
	if issues := l.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &l, nil
}

// HygieneLabelExample returns the validated example: a reviewer edit folded
// into a `corrected` label (HYGIENE_LABEL_EXAMPLE; registered as
// 'docsync.hygiene-label' v1).
func HygieneLabelExample() HygieneLabel {
	return HygieneLabel{
		Schema:        HygieneLabelSchemaID,
		LabelID:       "01J9ZM4R9BYZ5C7D9E1F3A5B7C",
		DecisionID:    "d_01J9ZK7Q2M",
		PolicyVersion: 17,
		Label:         HygieneLabelCorrected,
		SourceSignal:  HygieneSourceEdit,
		EditDiff:      new("-rate = 0.19\n+rate = 0.21"),
		HygieneRule:   "edit⇒corrected",
		NormalizedAt:  "2026-10-05T09:20:00Z",
	}
}
