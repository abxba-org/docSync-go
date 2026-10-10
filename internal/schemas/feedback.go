// Feedback record — `docsync.feedback-record/v1` (P6-T22).
//
// Ported from packages/schemas/src/v1/feedback-record.ts
// (FEEDBACK_SIGNAL_VALUES, FEEDBACK_SOURCE_VALUES, FEEDBACK_BASIS_VALUES,
// FEEDBACK_RECORD_SCHEMA, FEEDBACK_RECORD_EXAMPLE).
//
// The shared outcome record for every post-merge lifecycle signal a doc PR can
// earn: accepted, corrected by a human, rejected as a false positive, style
// rejected, unlabeled, a missed case (false negative) or an acknowledged
// operator override. This is the unit that feeds the Phase 10 evaluation,
// feedback and policy self-calibration loop (OD-R14 basis: the stored edit
// diff + which diff basis it was measured against is what makes `corrected`
// signals reproducible evidence instead of an assertion).
//
// `decision_id` is optional at the TYPE level — some lifecycle records have no
// decision — but the superRefine rules fail closed: every signal that feeds
// policy calibration through a routed decision (`accepted`, `false_positive`,
// `style_rejected`, `unlabeled`, `false_negative`) MUST carry its decision id
// (routing provenance), and a `corrected` signal MUST carry the
// `corrected_diff` it is correcting with. z.strictObject everywhere, every
// union closed, every timestamp a field supplied by the emitter — never
// generated here (no wall-clock reads anywhere).
package schemas

import (
	"fmt"
	"sort"
)

/* -------------------------------------------------------------- closed vocabularies */

// FeedbackSignal is one member of the frozen feedback-signal union — the
// complete Phase 6/10 lifecycle vocabulary (FEEDBACK_SIGNAL).
type FeedbackSignal string

// The seven lifecycle signals in canonical order (FEEDBACK_SIGNAL_VALUES).
const (
	FeedbackSignalAccepted      FeedbackSignal = "accepted"
	FeedbackSignalCorrected     FeedbackSignal = "corrected"
	FeedbackSignalFalsePositive FeedbackSignal = "false_positive"
	FeedbackSignalStyleRejected FeedbackSignal = "style_rejected"
	FeedbackSignalUnlabeled     FeedbackSignal = "unlabeled"
	FeedbackSignalFalseNegative FeedbackSignal = "false_negative"
	FeedbackSignalOverrideAck   FeedbackSignal = "override_ack"
)

// FeedbackSignalValues is the frozen seven-signal lifecycle union — the
// complete Phase 6/10 lifecycle vocabulary.
var FeedbackSignalValues = []FeedbackSignal{
	FeedbackSignalAccepted,
	FeedbackSignalCorrected,
	FeedbackSignalFalsePositive,
	FeedbackSignalStyleRejected,
	FeedbackSignalUnlabeled,
	FeedbackSignalFalseNegative,
	FeedbackSignalOverrideAck,
}

// IsValidFeedbackSignal reports membership in FEEDBACK_SIGNAL.
func IsValidFeedbackSignal(s string) bool {
	for _, v := range FeedbackSignalValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// FeedbackSource is one member of the frozen origin union: where the feedback
// was observed — on the doc PR itself, on the source PR that triggered the
// run, or purely locally (FEEDBACK_SOURCE).
type FeedbackSource string

// The three origin values in canonical order (FEEDBACK_SOURCE_VALUES).
const (
	FeedbackSourceDocPR    FeedbackSource = "doc_pr"
	FeedbackSourceSourcePR FeedbackSource = "source_pr"
	FeedbackSourceLocal    FeedbackSource = "local"
)

// FeedbackSourceValues is the frozen doc_pr/source_pr/local union.
var FeedbackSourceValues = []FeedbackSource{
	FeedbackSourceDocPR, FeedbackSourceSourcePR, FeedbackSourceLocal,
}

// IsValidFeedbackSource reports membership in FEEDBACK_SOURCE.
func IsValidFeedbackSource(s string) bool {
	for _, v := range FeedbackSourceValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// FeedbackBasis is one member of the frozen diff-basis union (OD-R14): which
// diff the signal was measured against — the review-event diff or the PR-head
// diff. Optional because not every signal is diff-based (FEEDBACK_BASIS).
type FeedbackBasis string

// The two diff-basis values in canonical order (FEEDBACK_BASIS_VALUES).
const (
	FeedbackBasisReviewEvent FeedbackBasis = "review_event"
	FeedbackBasisPRHead      FeedbackBasis = "pr_head"
)

// FeedbackBasisValues is the frozen review_event/pr_head union (OD-R14).
var FeedbackBasisValues = []FeedbackBasis{
	FeedbackBasisReviewEvent, FeedbackBasisPRHead,
}

// IsValidFeedbackBasis reports membership in FEEDBACK_BASIS.
func IsValidFeedbackBasis(s string) bool {
	for _, v := range FeedbackBasisValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

/* ------------------------------------------------------------------ record shape */

// FeedbackRecordSchemaID is the literal schema tag of
// `docsync.feedback-record/v1` (z.literal).
const FeedbackRecordSchemaID = "docsync.feedback-record/v1"

// FeedbackRecord is the `docsync.feedback-record/v1` record
// (FEEDBACK_RECORD_SCHEMA, z.infer FeedbackRecord).
type FeedbackRecord struct {
	Schema        string         `json:"schema"`
	RecordID      string         `json:"record_id"`
	At            string         `json:"at"`
	Source        FeedbackSource `json:"source"`
	RunID         string         `json:"run_id"`
	PolicyVersion int            `json:"policy_version"`
	// DecisionID is optional at the type level, but the superRefine rules
	// fail closed for calibration-relevant signals (routing provenance).
	DecisionID *string        `json:"decision_id,omitempty"`
	DocPath    *string        `json:"doc_path,omitempty"`
	Signal     FeedbackSignal `json:"signal"`
	Detail     string         `json:"detail"`
	// CorrectedDiff — OD-R14 basis note: the stored edit diff the human
	// correction applied — REQUIRED for `corrected` (superRefine), absent
	// otherwise.
	CorrectedDiff  *string        `json:"corrected_diff,omitempty"`
	Basis          *FeedbackBasis `json:"basis,omitempty"`
	SourcePRNumber *int           `json:"source_pr_number,omitempty"`
	DocPRNumber    *int           `json:"doc_pr_number,omitempty"`
	// Metadata is z.record(z.string().min(1), z.string().min(1)) — every key
	// and every value is a non-empty string.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Validate checks the record against FEEDBACK_RECORD_SCHEMA (strict),
// reproducing the zod issue paths and the three superRefine rules.
func (r *FeedbackRecord) Validate() []Issue {
	var issues []Issue
	if r.Schema != FeedbackRecordSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.feedback-record/v1"`})
	}
	Collect(&issues, RequiredString(r.RecordID, 1, []string{"record_id"}))
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	Collect(&issues, CheckEnum(string(r.Source), stringsOf(FeedbackSourceValues), []string{"source"}))
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	if r.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	if r.DecisionID != nil {
		Collect(&issues, RequiredString(*r.DecisionID, 1, []string{"decision_id"}))
	}
	if r.DocPath != nil {
		Collect(&issues, RequiredString(*r.DocPath, 1, []string{"doc_path"}))
	}
	Collect(&issues, CheckEnum(string(r.Signal), stringsOf(FeedbackSignalValues), []string{"signal"}))
	Collect(&issues, RequiredString(r.Detail, 1, []string{"detail"}))
	if r.CorrectedDiff != nil {
		Collect(&issues, RequiredString(*r.CorrectedDiff, 1, []string{"corrected_diff"}))
	}
	if r.Basis != nil {
		Collect(&issues, CheckEnum(string(*r.Basis), stringsOf(FeedbackBasisValues), []string{"basis"}))
	}
	if r.SourcePRNumber != nil && *r.SourcePRNumber < 1 {
		Collect(&issues, &Issue{Path: []string{"source_pr_number"}, Message: "Number must be greater than 0"})
	}
	if r.DocPRNumber != nil && *r.DocPRNumber < 1 {
		Collect(&issues, &Issue{Path: []string{"doc_pr_number"}, Message: "Number must be greater than 0"})
	}
	if len(r.Metadata) > 0 {
		keys := make([]string, 0, len(r.Metadata))
		for k := range r.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			Collect(&issues, RequiredString(key, 1, []string{"metadata", key}))
			Collect(&issues, RequiredString(r.Metadata[key], 1, []string{"metadata", key}))
		}
	}

	// Rule 1 (P6-T22): a `corrected` signal without the stored edit diff is an
	// assertion, not evidence — fail closed (OD-R14).
	if r.Signal == FeedbackSignalCorrected && r.CorrectedDiff == nil {
		Collect(&issues, &Issue{
			Path: []string{"corrected_diff"},
			Message: "signal 'corrected' requires a non-empty corrected_diff " +
				"(the stored edit diff is the OD-R14 evidence basis)",
		})
	}

	// Rule 2 (P6-T22): every calibration-relevant rejection/unlabel signal
	// must carry its decision id — routing provenance, or the record cannot
	// be attributed to a policy decision.
	if (r.Signal == FeedbackSignalFalsePositive ||
		r.Signal == FeedbackSignalStyleRejected ||
		r.Signal == FeedbackSignalUnlabeled ||
		r.Signal == FeedbackSignalFalseNegative) &&
		r.DecisionID == nil {
		Collect(&issues, &Issue{
			Path:    []string{"decision_id"},
			Message: fmt.Sprintf("signal '%s' requires decision_id (routing provenance)", string(r.Signal)),
		})
	}

	// Rule 3 (P6-T22): an `accepted` signal is only meaningful against the
	// decision that produced the accepted patch.
	if r.Signal == FeedbackSignalAccepted && r.DecisionID == nil {
		Collect(&issues, &Issue{
			Path:    []string{"decision_id"},
			Message: "signal 'accepted' requires decision_id (routing provenance)",
		})
	}
	return issues
}

// ParseFeedbackRecord strictly decodes and validates `docsync.feedback-record/v1`
// JSON bytes (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseFeedbackRecord(data []byte) (*FeedbackRecord, error) {
	var r FeedbackRecord
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// FeedbackRecordExample returns the canonical example record
// (FEEDBACK_RECORD_EXAMPLE; registered as 'docsync.feedback-record' v1 —
// golden data for byte-for-byte serializer checks).
func FeedbackRecordExample() FeedbackRecord {
	return FeedbackRecord{
		Schema:         FeedbackRecordSchemaID,
		RecordID:       "fb_01J9ZM2Q7A",
		At:             "2026-10-05T09:14:00Z",
		Source:         FeedbackSourceDocPR,
		RunID:          "run_01J9ZK6X8A",
		PolicyVersion:  17,
		DecisionID:     new("d_01J9ZK7Q2M"),
		DocPath:        new("docs/payments.md"),
		Signal:         FeedbackSignalAccepted,
		Detail:         "doc PR #84 merged without human edits within the observation window",
		Basis:          new(FeedbackBasisPRHead),
		SourcePRNumber: new(42),
		DocPRNumber:    new(84),
		Metadata:       map[string]string{"observation_window": "72h"},
	}
}
