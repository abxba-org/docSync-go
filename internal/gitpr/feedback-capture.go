package gitpr

// Ported from core/src/gitpr/feedback-capture.ts (P6-T20..T24; ARCH §8.7
// "Closed-loop learning from GitHub / review feedback", ARCH §11.4; OD-R13,
// OD-R14): reviewer feedback capture.
//
// A doc PR earns lifecycle signals from its human reviewer — merge, edit,
// close, label, silence. This module is the PURE capture layer that turns such a
// reviewer event into the one shared outcome record
// (`docsync.feedback-record/v1`, P6-T22) feeding the Phase 10 evaluation /
// policy self-calibration loop, and into the operator-override seam that
// ACKNOWLEDGES (never auto-applies) ignore-pattern proposals for the next
// policy version (P6-T24, ARCH §11.4 — the Phase 10 Policy-Architect seam:
// models propose, humans promote; a model never promotes its own output to
// production policy).
//
// Event → signal mapping (closed table; Kind is a closed union so every arm
// below is exhaustive — unlisted combinations are unrepresentable):
//
//	| event.Kind       | deciding input                | signal         | diff basis                      |
//	|------------------|-------------------------------|----------------|----------------------------------|
//	| merged           | —                             | accepted       | —                                |
//	| review-changes   | EditedDiff present            | corrected      | 'review_event' + corrected_diff |
//	| review-changes   | EditedDiff absent (OD-R14)    | corrected      | 'pr_head', NO corrected_diff     |
//	| closed-rejected  | StatedReason ~ /already\s+correct/i | false_positive | —                         |
//	| closed-rejected  | any other StatedReason (incl. none) | style_rejected | —                        |
//	| label            | 'docsync:intentional' / 'docsync:ignore-pattern' | override_ack | —           |
//	| label            | any other or missing label    | unlabeled      | —                                |
//	| timeout          | —                             | unlabeled      | — (NEVER 'accepted')             |
//	| review-comment   | —                             | unlabeled      | — (no free-text grammar, OD-R13) |
//	| false-negative   | MissedDocs (machine-derived; EmitFalseNegativeRecord) | false_negative | —    |
//
// Fail-closed notes:
//
//   - A review-changes event without an edited diff still normalizes to
//     `corrected` with basis 'pr_head' and NO CorrectedDiff (OD-R14: the PR-head
//     basis is what is known; the stored edit diff is what makes the signal
//     evidence instead of an assertion). The FeedbackRecord schema's superRefine
//     rule 1 then REJECTS such a record at parse time — the caller must capture
//     the stored diff or nothing ships.
//   - `decision_id` is included when provided; the schema's superRefine rules
//     2/3 fail closed when a calibration-relevant signal lacks it.
//   - `source` is 'doc_pr' when a doc PR number is present, else 'source_pr'.
//   - `record_id` = 'fb-' + the first 16 hex chars of the sha256 hex digest over
//     the canonical JSON (recursively sorted keys) of the record body WITHOUT
//     record_id.
//   - Timestamps come from the caller's Clock — never generated here (no
//     `time.Now()` anywhere, mirroring the schema's own discipline).

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// EDOCSYNCFeedbackRecord preserves the TS static `FeedbackRecordError.code`
// (EDOCSYNC_FEEDBACK_RECORD), which internal/docutil does not list.
const EDOCSYNCFeedbackRecord = "EDOCSYNC_FEEDBACK_RECORD"

// FeedbackRecordError is the fail-closed refusal of a record that does not
// satisfy the v1 schema (typed, fail loud).
type FeedbackRecordError struct {
	Msg string
}

// Error reproduces the TS message text.
func (e *FeedbackRecordError) Error() string { return e.Msg }

// Code exposes the machine-readable failure class (TS: static/instance `code`).
func (e *FeedbackRecordError) Code() string { return EDOCSYNCFeedbackRecord }

func newFeedbackRecordError(msg string) *FeedbackRecordError {
	return &FeedbackRecordError{Msg: msg}
}

/* --------------------------------------------------------- event normalization */

// ReviewerEventKind is the closed reviewer-event union.
type ReviewerEventKind string

// The seven reviewer-event kinds (TS: ReviewerEvent['kind']).
const (
	ReviewerEventMerged         ReviewerEventKind = "merged"
	ReviewerEventReviewChanges  ReviewerEventKind = "review-changes"
	ReviewerEventClosedRejected ReviewerEventKind = "closed-rejected"
	ReviewerEventLabel          ReviewerEventKind = "label"
	ReviewerEventTimeout        ReviewerEventKind = "timeout"
	ReviewerEventReviewComment  ReviewerEventKind = "review-comment"
	ReviewerEventFalseNegative  ReviewerEventKind = "false-negative"
)

// ReviewerEvent is a reviewer lifecycle event observed on a doc PR
// (P6-T20..T21).
type ReviewerEvent struct {
	Kind ReviewerEventKind `json:"kind"`
	// Label is the label text, for `label` events.
	Label string `json:"label,omitempty"`
	// StatedReason is the reviewer's stated reason, for `closed-rejected` events.
	StatedReason string `json:"stated_reason,omitempty"`
	// EditedDiff is the reviewer's edited diff, when the platform preserved it
	// (OD-R14).
	EditedDiff string `json:"edited_diff,omitempty"`
	// MissedDocs is the manifest docs an ignore-routed run actually touched, for
	// `false-negative` events — the ONLY machine-derived event (never a human
	// reviewer action); fed exclusively by EmitFalseNegativeRecord.
	MissedDocs []string `json:"missed_docs,omitempty"`
}

// NormalizedReviewerAction is the normalized decision derived from a
// ReviewerEvent.
type NormalizedReviewerAction struct {
	Signal schemas.FeedbackSignal `json:"signal"`
	// Basis is which diff the signal is measured against (OD-R14); nil when not
	// diff-based.
	Basis *schemas.FeedbackBasis `json:"basis,omitempty"`
	// CorrectedDiff is the stored edit diff, present only for diff-based
	// `review_event` corrections.
	CorrectedDiff *string `json:"corrected_diff,omitempty"`
	// Detail is non-empty, human-auditable evidence (kind + the deciding reason).
	Detail string `json:"detail"`
}

// OverrideLabels are the two V1 override labels (OD-R13 — labels, never
// free-text comment grammar).
var overrideLabels = map[string]bool{
	"docsync:intentional":    true,
	"docsync:ignore-pattern": true,
}

// AlreadyCorrectPattern is the stated-reason pattern distinguishing "already
// correct" from style rejection (TS: /already\s+correct/i).
var alreadyCorrectPattern = regexp.MustCompile(`(?i)already\s+correct`)

// NormalizeReviewerAction is the pure normalization (P6-T21): map a reviewer
// event onto the closed feedback signal vocabulary per the module header table.
// Never fails — every event kind has a defined arm; the fail-closed enforcement
// happens at record parse time (EmitFeedbackRecord). An unknown kind maps to
// `unlabeled` (fail closed, never `accepted`).
func NormalizeReviewerAction(event ReviewerEvent) NormalizedReviewerAction {
	switch event.Kind {
	case ReviewerEventMerged:
		return NormalizedReviewerAction{
			Signal: schemas.FeedbackSignalAccepted,
			Detail: "merged: doc PR merged without human edits to the proposed documentation",
		}
	case ReviewerEventReviewChanges:
		if event.EditedDiff != "" {
			editedDiff := event.EditedDiff
			basis := schemas.FeedbackBasisReviewEvent
			return NormalizedReviewerAction{
				Signal:        schemas.FeedbackSignalCorrected,
				Basis:         &basis,
				CorrectedDiff: &editedDiff,
				Detail: "review-changes: human correction applied (basis 'review_event'; the reviewer's " +
					"edited diff is stored as corrected_diff)",
			}
		}
		basis := schemas.FeedbackBasisPRHead
		return NormalizedReviewerAction{
			Signal: schemas.FeedbackSignalCorrected,
			Basis:  &basis,
			Detail: "review-changes: correction reported without an edited diff (basis 'pr_head', OD-R14) — " +
				"no corrected_diff stored, so the record fails closed at parse time until the " +
				"stored diff is supplied",
		}
	case ReviewerEventClosedRejected:
		alreadyCorrect := event.StatedReason != "" && alreadyCorrectPattern.MatchString(event.StatedReason)
		reason := "<none>"
		if event.StatedReason != "" {
			reason = feedbackJSONString(event.StatedReason)
		}
		if alreadyCorrect {
			return NormalizedReviewerAction{
				Signal: schemas.FeedbackSignalFalsePositive,
				Detail: "closed-rejected: closed as already correct (statedReason: " + reason + ")",
			}
		}
		return NormalizedReviewerAction{
			Signal: schemas.FeedbackSignalStyleRejected,
			Detail: "closed-rejected: closed as a style rejection (statedReason: " + reason + ")",
		}
	case ReviewerEventLabel:
		if event.Label != "" && overrideLabels[event.Label] {
			return NormalizedReviewerAction{
				Signal: schemas.FeedbackSignalOverrideAck,
				Detail: "label: operator override acknowledged via label '" + event.Label + "'",
			}
		}
		if event.Label == "" {
			return NormalizedReviewerAction{
				Signal: schemas.FeedbackSignalUnlabeled,
				Detail: "label: no label provided on the review event",
			}
		}
		return NormalizedReviewerAction{
			Signal: schemas.FeedbackSignalUnlabeled,
			Detail: "label: label '" + event.Label + "' is not a docsync override label " +
				"('docsync:intentional' | 'docsync:ignore-pattern')",
		}
	case ReviewerEventTimeout:
		return NormalizedReviewerAction{
			Signal: schemas.FeedbackSignalUnlabeled,
			Detail: "timeout: observation window elapsed without reviewer action — never treated as accepted",
		}
	case ReviewerEventReviewComment:
		return NormalizedReviewerAction{
			Signal: schemas.FeedbackSignalUnlabeled,
			Detail: "review-comment: free-text comment carries no machine-readable grammar in V1 " +
				"(OD-R13) — mapped to unlabeled",
		}
	case ReviewerEventFalseNegative:
		missed := event.MissedDocs
		if len(missed) > 0 {
			return NormalizedReviewerAction{
				Signal: schemas.FeedbackSignalFalseNegative,
				Detail: "false-negative: 'ignore' routing silently skipped manifest docs touched by the " +
					"source PR: " + strings.Join(missed, ", "),
			}
		}
		return NormalizedReviewerAction{
			Signal: schemas.FeedbackSignalFalseNegative,
			Detail: "false-negative: 'ignore' routing silently skipped manifest docs (none recorded)",
		}
	default:
		// Go's closed union makes the unknown-kind arm unreachable for valid
		// callers; it stays as the fail-closed default (never 'accepted').
		return NormalizedReviewerAction{
			Signal: schemas.FeedbackSignalUnlabeled,
			Detail: "feedback-capture: unknown reviewer event kind '" + string(event.Kind) +
				"' — fail closed, mapped to unlabeled (never accepted)",
		}
	}
}

// feedbackJSONString mirrors the JSON.stringify used in the TS detail strings.
func feedbackJSONString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "\"" + value + "\""
	}
	return string(raw)
}

/* -------------------------------------------------------------- record emission */

// FeedbackCaptureInput is the input of EmitFeedbackRecord (P6-T22).
type FeedbackCaptureInput struct {
	// Event is the reviewer lifecycle event to capture.
	Event ReviewerEvent
	// RunID is the run that produced the routed decision being fed back on.
	RunID string
	// PolicyVersion is the policy version the decision was made under
	// (calibration axis, D11).
	PolicyVersion int
	// DecisionID is routing provenance; required by the schema for every
	// calibration signal.
	DecisionID *string
	// DocPath is the manifest doc the decision touched, when the signal is
	// doc-scoped.
	DocPath *string
	// SourcePrNumber is the source PR that triggered the run.
	SourcePrNumber *int
	// DocPrNumber is the doc PR under review — its presence selects source
	// 'doc_pr'.
	DocPrNumber *int
	// Clock is the timestamp supplier — timestamps are ALWAYS caller-supplied,
	// never generated here.
	Clock func() string
}

// EmitFeedbackRecord composes the feedback record (P6-T22): normalize → build
// the `docsync.feedback-record/v1` object → derive `record_id` as a content
// hash over the canonical JSON of the record body (sorted keys, minus
// record_id) → parse with the schema, FAIL CLOSED on any violation by throwing
// FeedbackRecordError with the issue summary. No I/O — the caller persists the
// returned record.
func EmitFeedbackRecord(input FeedbackCaptureInput) (*schemas.FeedbackRecord, error) {
	action := NormalizeReviewerAction(input.Event)
	at := ""
	if input.Clock != nil {
		at = input.Clock()
	}
	source := schemas.FeedbackSourceSourcePR
	if input.DocPrNumber != nil {
		source = schemas.FeedbackSourceDocPR
	}

	// Built WITHOUT record_id first: the id is the content hash over exactly
	// these fields (canonicalJson sorts keys recursively).
	body := map[string]any{
		"schema":         schemas.FeedbackRecordSchemaID,
		"at":             at,
		"source":         string(source),
		"run_id":         input.RunID,
		"policy_version": input.PolicyVersion,
		"signal":         string(action.Signal),
		"detail":         action.Detail,
	}
	if input.DecisionID != nil {
		body["decision_id"] = *input.DecisionID
	}
	if input.DocPath != nil {
		body["doc_path"] = *input.DocPath
	}
	if action.CorrectedDiff != nil {
		body["corrected_diff"] = *action.CorrectedDiff
	}
	if action.Basis != nil {
		body["basis"] = string(*action.Basis)
	}
	if input.SourcePrNumber != nil {
		body["source_pr_number"] = *input.SourcePrNumber
	}
	if input.DocPrNumber != nil {
		body["doc_pr_number"] = *input.DocPrNumber
	}

	canonicalBody, err := docutil.CanonicalJSON(body)
	if err != nil {
		return nil, newFeedbackRecordError(
			"feedback record refused (fail closed, nothing emitted): " + err.Error())
	}
	recordID := "fb-" + docutil.ContentHash(canonicalBody)[:16]

	record := schemas.FeedbackRecord{
		Schema:         schemas.FeedbackRecordSchemaID,
		RecordID:       recordID,
		At:             at,
		Source:         source,
		RunID:          input.RunID,
		PolicyVersion:  input.PolicyVersion,
		DecisionID:     input.DecisionID,
		DocPath:        input.DocPath,
		Signal:         action.Signal,
		Detail:         action.Detail,
		CorrectedDiff:  action.CorrectedDiff,
		Basis:          action.Basis,
		SourcePRNumber: input.SourcePrNumber,
		DocPRNumber:    input.DocPrNumber,
	}

	raw, err := json.Marshal(&record)
	if err != nil {
		return nil, newFeedbackRecordError(
			"feedback record refused (fail closed, nothing emitted): " + err.Error())
	}
	parsed, err := schemas.ParseFeedbackRecord(raw)
	if err != nil {
		return nil, newFeedbackRecordError(
			"feedback record refused (fail closed, nothing emitted): " + feedbackIssueSummary(err))
	}
	return parsed, nil
}

// feedbackIssueSummary renders a schema-validation failure the way the TS
// mapped zod issues — `<path>.<to>.<issue>: <message>`, `<root>: <message>`
// when the issue carries no path.
func feedbackIssueSummary(err error) string {
	var validationErr *schemas.ValidationError
	if errors.As(err, &validationErr) {
		parts := make([]string, 0, len(validationErr.Issues))
		for _, issue := range validationErr.Issues {
			path := strings.Join(issue.Path, ".")
			if path == "" {
				path = "<root>"
			}
			parts = append(parts, path+": "+issue.Message)
		}
		return strings.Join(parts, "; ")
	}
	return err.Error()
}

/* -------------------------------------------------------------- false negatives */

// FalseNegativeReport is the result of DetectFalseNegative (P6-T23).
type FalseNegativeReport struct {
	// FalseNegative is true when routing ignored a run that actually touched
	// manifest docs.
	FalseNegative bool `json:"false_negative"`
	// TouchedDocs is the sorted, de-duplicated intersection of source-PR diff
	// paths and manifest doc paths.
	TouchedDocs []string `json:"touched_docs"`
}

// DetectFalseNegative is the pure detection (P6-T23): a routed-away run
// (routing 'ignore') that nonetheless touched manifest docs is a MISSED CASE —
// the strongest calibration signal that the path-level policy under-fired.
// Review/continue routings are never false negatives (they did not silently
// skip anything).
func DetectFalseNegative(routing string, sourcePrDiffPaths, manifestDocPaths []string) FalseNegativeReport {
	manifestDocs := make(map[string]bool, len(manifestDocPaths))
	for _, path := range manifestDocPaths {
		manifestDocs[path] = true
	}
	seen := map[string]bool{}
	touched := []string{}
	for _, path := range sourcePrDiffPaths {
		if manifestDocs[path] && !seen[path] {
			seen[path] = true
			touched = append(touched, path)
		}
	}
	sort.Strings(touched)
	return FalseNegativeReport{
		FalseNegative: routing == RoutingIgnore && len(touched) > 0,
		TouchedDocs:   touched,
	}
}

// Routing values (TS: 'ignore' | 'review' | 'continue').
const (
	RoutingIgnore   = "ignore"
	RoutingReview   = "review"
	RoutingContinue = "continue"
)

// FalseNegativeEmissionInput is the input of EmitFalseNegativeRecord.
type FalseNegativeEmissionInput struct {
	// Routing is the routing decision the run made; only 'ignore' can be a false
	// negative.
	Routing string
	// SourcePrDiffPaths are the paths changed in the source PR diff.
	SourcePrDiffPaths []string
	// ManifestDocPaths are the paths the manifest allowlists as documentation.
	ManifestDocPaths []string
	// RunID is the run that produced the routed decision being fed back on.
	RunID string
	// PolicyVersion is the policy version the decision was made under
	// (calibration axis, D11).
	PolicyVersion int
	// DecisionID is routing provenance; the schema's superRefine rule 2 REQUIRES
	// `decision_id` for the `false_negative` signal — omitting it fails closed
	// via FeedbackRecordError at the emit path.
	DecisionID *string
	// DocPath scopes the record to a doc; defaults to the first (sorted) touched
	// doc — the detection already established it was touched, so the default is
	// evidence-backed, never guessed.
	DocPath *string
	// SourcePrNumber is the source PR that triggered the run.
	SourcePrNumber *int
	// DocPrNumber is the doc PR under review, when one exists.
	DocPrNumber *int
	// Clock is the timestamp supplier — timestamps are ALWAYS caller-supplied,
	// never generated here.
	Clock func() string
}

// FalseNegativeEmission is the result of EmitFalseNegativeRecord.
type FalseNegativeEmission struct {
	// Record is the schema-validated `false_negative` record, or nil when
	// detection found no false negative — the detection is returned either way,
	// and NOTHING is emitted in the nil case.
	Record *schemas.FeedbackRecord
	// Detection is the underlying DetectFalseNegative report.
	Detection FalseNegativeReport
}

// EmitFalseNegativeRecord is the composed emission seam (P6-T23, closes m7):
// DetectFalseNegative → emit through the shared EmitFeedbackRecord path with
// signal 'false_negative' — the detail names the touched docs + the ignore
// routing, `decision_id` threads through for the schema's rejection-signal rule
// (superRefine rule 2). Fail-closed exactly like every other record: a schema
// violation throws FeedbackRecordError. No false negative → `{Record: nil,
// Detection}`, nothing emitted. Pure — no I/O; the caller persists the returned
// record.
func EmitFalseNegativeRecord(input FalseNegativeEmissionInput) (FalseNegativeEmission, error) {
	detection := DetectFalseNegative(input.Routing, input.SourcePrDiffPaths, input.ManifestDocPaths)
	if !detection.FalseNegative {
		return FalseNegativeEmission{Record: nil, Detection: detection}, nil
	}
	docPath := input.DocPath
	if docPath == nil {
		first := detection.TouchedDocs[0]
		docPath = &first
	}
	record, err := EmitFeedbackRecord(FeedbackCaptureInput{
		Event: ReviewerEvent{
			Kind:       ReviewerEventFalseNegative,
			MissedDocs: detection.TouchedDocs,
		},
		RunID:          input.RunID,
		PolicyVersion:  input.PolicyVersion,
		DecisionID:     input.DecisionID,
		DocPath:        docPath,
		SourcePrNumber: input.SourcePrNumber,
		DocPrNumber:    input.DocPrNumber,
		Clock:          input.Clock,
	})
	if err != nil {
		return FalseNegativeEmission{}, err
	}
	return FalseNegativeEmission{Record: record, Detection: detection}, nil
}

/* ------------------------------------------------- override proposal queue (T24) */

// ProposalEntryKind is the closed proposal kind (TS: 'ignore-pattern').
const ProposalEntryKind = "ignore-pattern"

// ProposalEntry is one proposal awaiting the Phase 10 Policy-Architect seam
// (P6-T24, ARCH §11.4): an operator override ack proposing an ignore pattern
// for the NEXT policy version. Models propose; humans promote — this queue
// NEVER applies anything; it only accumulates proposals.
type ProposalEntry struct {
	Kind          string `json:"kind"`
	Pattern       string `json:"pattern"`
	PolicyVersion int    `json:"policy_version"`
	At            string `json:"at"`
	RunID         string `json:"run_id"`
}

// ProposalQueue is the in-memory proposal queue surface (P6-T24).
type ProposalQueue struct {
	mu      sync.Mutex
	entries []ProposalEntry
}

// Push freezes the entry and appends it (TS: Object.freeze on push).
func (q *ProposalQueue) Push(entry ProposalEntry) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.entries = append(q.entries, entry)
}

// Drain returns a frozen snapshot of every entry accumulated so far — the queue
// is APPEND-ONLY: drain never consumes, so the Phase 10 consumer can re-read
// the full proposal history; promotion state lives with the policy pipeline,
// not here.
func (q *ProposalQueue) Drain() []ProposalEntry {
	if q == nil {
		return []ProposalEntry{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	snapshot := make([]ProposalEntry, len(q.entries))
	copy(snapshot, q.entries)
	return snapshot
}

// CreateProposalQueue builds the in-memory, append-only proposal queue.
func CreateProposalQueue() *ProposalQueue {
	return &ProposalQueue{entries: []ProposalEntry{}}
}

// OverrideAcknowledgement is the result of AcknowledgeOverride.
type OverrideAcknowledgement struct {
	Ack   bool          `json:"ack"`
	Entry ProposalEntry `json:"entry"`
}

// AcknowledgeOverride is the instant operator-override ack (P6-T24): the
// override is acknowledged IMMEDIATELY (the operator's intent is recorded,
// never second-guessed) and, when a queue is provided, exactly one proposal
// entry is pushed onto it for the Phase 10 Policy-Architect seam to consider
// for the next policy version.
func AcknowledgeOverride(input OverrideAckInput) OverrideAcknowledgement {
	at := ""
	if input.Clock != nil {
		at = input.Clock()
	}
	entry := ProposalEntry{
		Kind:          ProposalEntryKind,
		Pattern:       input.Pattern,
		PolicyVersion: input.PolicyVersion,
		At:            at,
		RunID:         input.RunID,
	}
	if input.Queue != nil {
		input.Queue.Push(entry)
	}
	return OverrideAcknowledgement{Ack: true, Entry: entry}
}

// OverrideAckInput is the input of AcknowledgeOverride.
type OverrideAckInput struct {
	Pattern       string
	RunID         string
	PolicyVersion int
	Clock         func() string
	Queue         *ProposalQueue
}
