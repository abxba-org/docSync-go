package gitpr_test

// Black-box port of test/gitpr-feedback.test.ts's feedback-capture half
// (P6-T19..T24; ARCH §8.7, §11.4; OD-R13, OD-R14). The self-trigger cells of
// that TS suite belong to self-trigger.go (another delegate) and are not ported
// here.
//
// Eigenschaften asserted: the closed event→signal table, the schema fail-closed
// emission (`docsync.feedback-record/v1`), the content-hash `record_id`
// determinism, the false-negative detection/emission seam (P6-T23) and the
// append-only operator-override proposal queue (P6-T24 — models propose, humans
// promote; INV-7: feedback never bypasses evaluation).

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

const (
	fbT0            = "2026-10-07T00:00:00Z"
	fbT1            = "2026-10-07T01:00:00Z"
	fbRunID         = "run-fb-1"
	fbPolicyVersion = 3
	fbDecisionID    = "dec-routing-1"
)

func fbClock0() string { return fbT0 }
func fbClock1() string { return fbT1 }

func fbDecision() *string { d := fbDecisionID; return &d }

func fbStrPtr(s string) *string { return &s }
func fbIntPtr(i int) *int       { return &i }

func TestP6T20_FiveReviewerActionsMapToCorrectSignals(t *testing.T) {
	merged := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventMerged})
	require.Equal(t, schemas.FeedbackSignalAccepted, merged.Signal)
	require.Nil(t, merged.Basis)

	edited := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{
		Kind: gitpr.ReviewerEventReviewChanges, EditedDiff: "x",
	})
	require.Equal(t, schemas.FeedbackSignalCorrected, edited.Signal)
	require.NotNil(t, edited.Basis)
	require.Equal(t, schemas.FeedbackBasisReviewEvent, *edited.Basis)
	require.NotNil(t, edited.CorrectedDiff)
	require.Equal(t, "x", *edited.CorrectedDiff)

	alreadyCorrect := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{
		Kind: gitpr.ReviewerEventClosedRejected, StatedReason: "docs are already correct",
	})
	require.Equal(t, schemas.FeedbackSignalFalsePositive, alreadyCorrect.Signal)

	styleRejected := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{
		Kind: gitpr.ReviewerEventClosedRejected, StatedReason: "wrong tone",
	})
	require.Equal(t, schemas.FeedbackSignalStyleRejected, styleRejected.Signal)

	override := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{
		Kind: gitpr.ReviewerEventLabel, Label: "docsync:intentional",
	})
	require.Equal(t, schemas.FeedbackSignalOverrideAck, override.Signal)
	require.Contains(t, override.Detail, "docsync:intentional")

	timeout := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventTimeout})
	require.Equal(t, schemas.FeedbackSignalUnlabeled, timeout.Signal)
	require.NotEqual(t, schemas.FeedbackSignalAccepted, timeout.Signal)
}

func TestP6T21_RejectReasonSplitsFalsePositiveVsStyleRejected(t *testing.T) {
	falsePositive := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{
		Kind: gitpr.ReviewerEventClosedRejected, StatedReason: "the docs are already correct upstream",
	})
	require.Equal(t, schemas.FeedbackSignalFalsePositive, falsePositive.Signal)
	require.Contains(t, falsePositive.Detail, "already correct")

	styleRejected := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{
		Kind: gitpr.ReviewerEventClosedRejected, StatedReason: "wrong tone",
	})
	require.Equal(t, schemas.FeedbackSignalStyleRejected, styleRejected.Signal)

	noStatedReason := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventClosedRejected})
	require.Equal(t, schemas.FeedbackSignalStyleRejected, noStatedReason.Signal)
	require.Contains(t, noStatedReason.Detail, "<none>")

	withoutEditedDiff := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventReviewChanges})
	require.Equal(t, schemas.FeedbackSignalCorrected, withoutEditedDiff.Signal)
	require.NotNil(t, withoutEditedDiff.Basis)
	require.Equal(t, schemas.FeedbackBasisPRHead, *withoutEditedDiff.Basis)
	require.Nil(t, withoutEditedDiff.CorrectedDiff)

	comment := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventReviewComment})
	require.Equal(t, schemas.FeedbackSignalUnlabeled, comment.Signal)

	otherLabel := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{
		Kind: gitpr.ReviewerEventLabel, Label: "priority",
	})
	require.Equal(t, schemas.FeedbackSignalUnlabeled, otherLabel.Signal)
	require.Contains(t, otherLabel.Detail, "is not a docsync override label")

	noLabel := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventLabel})
	require.Equal(t, schemas.FeedbackSignalUnlabeled, noLabel.Signal)
	require.Contains(t, noLabel.Detail, "no label provided")

	bothOverrideLabels := []string{"docsync:intentional", "docsync:ignore-pattern"}
	for _, label := range bothOverrideLabels {
		action := gitpr.NormalizeReviewerAction(gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventLabel, Label: label})
		require.Equal(t, schemas.FeedbackSignalOverrideAck, action.Signal)
	}
}

func TestP6T22_FeedbackRecordsValidateAgainstSharedSchema(t *testing.T) {
	accepted, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
		Event:         gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventMerged},
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		DecisionID:    fbDecision(),
		DocPrNumber:   fbIntPtr(7),
		Clock:         fbClock0,
	})
	require.NoError(t, err)
	require.Equal(t, schemas.FeedbackSourceDocPR, accepted.Source)
	require.Equal(t, schemas.FeedbackSignalAccepted, accepted.Signal)
	require.NoError(t, validateFeedbackRecord(accepted))

	corrected, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
		Event:         gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventReviewChanges, EditedDiff: "- stale\n+ fresh\n"},
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		DecisionID:    fbDecision(),
		Clock:         fbClock0,
	})
	require.NoError(t, err)
	require.NotNil(t, corrected.CorrectedDiff)
	require.Equal(t, "- stale\n+ fresh\n", *corrected.CorrectedDiff)
	require.NotNil(t, corrected.Basis)
	require.Equal(t, schemas.FeedbackBasisReviewEvent, *corrected.Basis)
	require.Equal(t, schemas.FeedbackSourceSourcePR, corrected.Source, "no doc PR number ⇒ source_pr")
	require.NoError(t, validateFeedbackRecord(corrected))

	overrideAck, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
		Event:         gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventLabel, Label: "docsync:intentional"},
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		DecisionID:    fbDecision(),
		Clock:         fbClock0,
	})
	require.NoError(t, err)
	require.Equal(t, schemas.FeedbackSignalOverrideAck, overrideAck.Signal)
	require.NoError(t, validateFeedbackRecord(overrideAck))

	fnEmission, err := gitpr.EmitFalseNegativeRecord(gitpr.FalseNegativeEmissionInput{
		Routing:           gitpr.RoutingIgnore,
		SourcePrDiffPaths: []string{"src/a.ts", "docs/payments.md"},
		ManifestDocPaths:  []string{"docs/payments.md", "docs/arch.md"},
		RunID:             fbRunID,
		PolicyVersion:     fbPolicyVersion,
		DecisionID:        fbDecision(),
		Clock:             fbClock0,
	})
	require.NoError(t, err)
	require.NotNil(t, fnEmission.Record)
	fnRecord := fnEmission.Record
	require.Equal(t, schemas.FeedbackSignalFalseNegative, fnRecord.Signal)
	require.Equal(t, fbDecisionID, *fnRecord.DecisionID)
	require.Contains(t, fnRecord.Detail, "docs/payments.md")
	require.NoError(t, validateFeedbackRecord(fnRecord))

	fbIDPattern := regexp.MustCompile(`^fb-[0-9a-f]{16}$`)
	for _, record := range []*schemas.FeedbackRecord{accepted, corrected, overrideAck, fnRecord} {
		require.Equal(t, fbPolicyVersion, record.PolicyVersion)
		require.Regexp(t, fbIDPattern, record.RecordID)
	}

	// record_id is a CONTENT hash: the same inputs re-emit the same id.
	acceptedAgain, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
		Event:         gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventMerged},
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		DecisionID:    fbDecision(),
		DocPrNumber:   fbIntPtr(7),
		Clock:         fbClock0,
	})
	require.NoError(t, err)
	require.Equal(t, accepted.RecordID, acceptedAgain.RecordID)

	// …and a different clock stamps a DIFFERENT id (the timestamp is hashed).
	acceptedLater, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
		Event:         gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventMerged},
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		DecisionID:    fbDecision(),
		DocPrNumber:   fbIntPtr(7),
		Clock:         fbClock1,
	})
	require.NoError(t, err)
	require.NotEqual(t, accepted.RecordID, acceptedLater.RecordID)
	require.Equal(t, fbT1, acceptedLater.At)
}

func TestP6T22_CorrectedWithoutDiffFailsClosed(t *testing.T) {
	// OD-R14: a `corrected` signal without the stored edit diff is an assertion,
	// not evidence — the record is REFUSED (nothing emitted).
	_, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
		Event:         gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventReviewChanges},
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		DecisionID:    fbDecision(),
		Clock:         fbClock0,
	})
	require.Error(t, err)
	var recordErr *gitpr.FeedbackRecordError
	require.ErrorAs(t, err, &recordErr)
	require.Equal(t, gitpr.EDOCSYNCFeedbackRecord, recordErr.Code())
	require.Contains(t, err.Error(), "feedback record refused (fail closed, nothing emitted)")
	require.Contains(t, err.Error(), "corrected_diff")
}

func TestP6T22_CalibrationSignalsRequireDecisionID(t *testing.T) {
	// superRefine rule 2/3: the calibration-relevant signals fail closed
	// without their routing provenance.
	signals := []gitpr.ReviewerEvent{
		{Kind: gitpr.ReviewerEventClosedRejected, StatedReason: "wrong tone"},
		{Kind: gitpr.ReviewerEventTimeout},
	}
	for _, event := range signals {
		_, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
			Event:         event,
			RunID:         fbRunID,
			PolicyVersion: fbPolicyVersion,
			Clock:         fbClock0,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "decision_id")
	}

	_, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
		Event:         gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventMerged},
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		Clock:         fbClock0,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "signal 'accepted' requires decision_id")
}

func TestP6T23_IgnoreRoutedDocEditRecordsFalseNegative(t *testing.T) {
	input := gitpr.FalseNegativeEmissionInput{
		SourcePrDiffPaths: []string{"src/a.ts", "docs/payments.md"},
		ManifestDocPaths:  []string{"docs/payments.md", "docs/arch.md"},
	}

	detection := gitpr.DetectFalseNegative(gitpr.RoutingIgnore, input.SourcePrDiffPaths, input.ManifestDocPaths)
	require.True(t, detection.FalseNegative)
	require.Equal(t, []string{"docs/payments.md"}, detection.TouchedDocs)

	emitted, err := gitpr.EmitFalseNegativeRecord(gitpr.FalseNegativeEmissionInput{
		Routing:           gitpr.RoutingIgnore,
		SourcePrDiffPaths: input.SourcePrDiffPaths,
		ManifestDocPaths:  input.ManifestDocPaths,
		RunID:             fbRunID,
		PolicyVersion:     fbPolicyVersion,
		DecisionID:        fbDecision(),
		Clock:             fbClock0,
	})
	require.NoError(t, err)
	require.True(t, emitted.Detection.FalseNegative)
	require.Equal(t, []string{"docs/payments.md"}, emitted.Detection.TouchedDocs)
	require.NotNil(t, emitted.Record)
	record := emitted.Record
	require.Equal(t, schemas.FeedbackSignalFalseNegative, record.Signal)
	require.Equal(t, fbDecisionID, *record.DecisionID)
	require.Equal(t, fbRunID, record.RunID)
	require.Equal(t, fbPolicyVersion, record.PolicyVersion)
	require.NotNil(t, record.DocPath)
	require.Equal(t, "docs/payments.md", *record.DocPath, "defaults to the first sorted touched doc")
	require.Contains(t, record.Detail, "docs/payments.md")
	require.Contains(t, record.Detail, "ignore")
	require.Regexp(t, regexp.MustCompile(`^fb-[0-9a-f]{16}$`), record.RecordID)
	require.NoError(t, validateFeedbackRecord(record))

	// Negative: review routing is never a false negative — detection is still
	// returned (the touched-doc intersection), but NOTHING is emitted.
	reviewed, err := gitpr.EmitFalseNegativeRecord(gitpr.FalseNegativeEmissionInput{
		Routing:           gitpr.RoutingReview,
		SourcePrDiffPaths: input.SourcePrDiffPaths,
		ManifestDocPaths:  input.ManifestDocPaths,
		RunID:             fbRunID,
		PolicyVersion:     fbPolicyVersion,
		DecisionID:        fbDecision(),
		Clock:             fbClock0,
	})
	require.NoError(t, err)
	require.Nil(t, reviewed.Record)
	require.False(t, reviewed.Detection.FalseNegative)
	require.Equal(t, []string{"docs/payments.md"}, reviewed.Detection.TouchedDocs)

	// continue routing is never a false negative either.
	continued, err := gitpr.EmitFalseNegativeRecord(gitpr.FalseNegativeEmissionInput{
		Routing:           gitpr.RoutingContinue,
		SourcePrDiffPaths: input.SourcePrDiffPaths,
		ManifestDocPaths:  input.ManifestDocPaths,
		RunID:             fbRunID,
		PolicyVersion:     fbPolicyVersion,
		DecisionID:        fbDecision(),
		Clock:             fbClock0,
	})
	require.NoError(t, err)
	require.Nil(t, continued.Record)
}

func TestP6T23_TouchedDocsAreSortedAndDeduplicated(t *testing.T) {
	detection := gitpr.DetectFalseNegative(gitpr.RoutingIgnore,
		[]string{"docs/z.md", "src/a.ts", "docs/a.md", "docs/z.md"},
		[]string{"docs/a.md", "docs/z.md", "docs/m.md"})
	require.True(t, detection.FalseNegative)
	require.Equal(t, []string{"docs/a.md", "docs/z.md"}, detection.TouchedDocs)
}

func TestP6T23_FalseNegativeRequiresDecisionID(t *testing.T) {
	_, err := gitpr.EmitFalseNegativeRecord(gitpr.FalseNegativeEmissionInput{
		Routing:           gitpr.RoutingIgnore,
		SourcePrDiffPaths: []string{"docs/payments.md"},
		ManifestDocPaths:  []string{"docs/payments.md"},
		RunID:             fbRunID,
		PolicyVersion:     fbPolicyVersion,
		Clock:             fbClock0,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "signal 'false_negative' requires decision_id")
}

func TestP6T24_OverrideAckInstantWithProposalQueueEntry(t *testing.T) {
	// Purity note: AcknowledgeOverride and CreateProposalQueue are pure in-memory
	// APIs — nothing writes the filesystem, so no policy file can be mutated
	// through them.
	queue := gitpr.CreateProposalQueue()

	ack := gitpr.AcknowledgeOverride(gitpr.OverrideAckInput{
		Pattern:       "src/generated/**",
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		Clock:         fbClock0,
		Queue:         queue,
	})
	require.True(t, ack.Ack)
	require.Equal(t, gitpr.ProposalEntryKind, ack.Entry.Kind)
	require.Equal(t, "src/generated/**", ack.Entry.Pattern)
	require.Equal(t, fbT0, ack.Entry.At)

	drained := queue.Drain()
	require.Len(t, drained, 1)
	require.Equal(t, gitpr.ProposalEntry{
		Kind:          "ignore-pattern",
		Pattern:       "src/generated/**",
		PolicyVersion: fbPolicyVersion,
		At:            fbT0,
		RunID:         fbRunID,
	}, drained[0])

	// The queue is APPEND-ONLY: drain never consumes.
	require.Len(t, queue.Drain(), 1)

	second := gitpr.AcknowledgeOverride(gitpr.OverrideAckInput{
		Pattern:       "vendor/**",
		RunID:         "run-fb-2",
		PolicyVersion: fbPolicyVersion,
		Clock:         fbClock1,
		Queue:         queue,
	})
	require.True(t, second.Ack)
	afterSecond := queue.Drain()
	require.Len(t, afterSecond, 2)
	require.Equal(t, "vendor/**", afterSecond[1].Pattern)
	require.Equal(t, fbT1, afterSecond[1].At)
	require.Len(t, queue.Drain(), 2)

	// Mutating a drained snapshot never touches the queue (value semantics =
	// the TS Object.freeze contract).
	afterSecond[1].Pattern = "src/rewritten/**"
	require.Equal(t, "vendor/**", queue.Drain()[1].Pattern)
}

func TestP6T24_AcknowledgeOverrideWithoutQueueStillAcks(t *testing.T) {
	ack := gitpr.AcknowledgeOverride(gitpr.OverrideAckInput{
		Pattern:       "docs/generated/**",
		RunID:         fbRunID,
		PolicyVersion: fbPolicyVersion,
		Clock:         fbClock0,
	})
	require.True(t, ack.Ack)
	require.Equal(t, "docs/generated/**", ack.Entry.Pattern)
}

func TestODR14_CorrectedRecordCarriesStoredDiffAndBasis(t *testing.T) {
	// Od-R14 basis completeness: a `corrected` record keeps BOTH the stored diff
	// AND which diff basis it was measured against, so the calibration loop can
	// reproduce the signal instead of trusting an assertion.
	record, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
		Event:          gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventReviewChanges, EditedDiff: "--- a\n+++ b\n"},
		RunID:          fbRunID,
		PolicyVersion:  fbPolicyVersion,
		DecisionID:     fbDecision(),
		DocPath:        fbStrPtr("docs/payments.md"),
		SourcePrNumber: fbIntPtr(481),
		DocPrNumber:    fbIntPtr(7),
		Clock:          fbClock0,
	})
	require.NoError(t, err)
	require.NotNil(t, record.CorrectedDiff)
	require.NotNil(t, record.Basis)
	require.Equal(t, schemas.FeedbackBasisReviewEvent, *record.Basis)
	require.Equal(t, "docs/payments.md", *record.DocPath)
	require.NotNil(t, record.SourcePRNumber)
	require.Equal(t, 481, *record.SourcePRNumber)
	require.NotNil(t, record.DocPRNumber)
	require.Equal(t, 7, *record.DocPRNumber)
	require.Equal(t, schemas.FeedbackSourceDocPR, record.Source)
	require.NoError(t, validateFeedbackRecord(record))
}

// validateFeedbackRecord re-parses the emitted record through the shared
// `docsync.feedback-record/v1` schema (the TS `FEEDBACK_RECORD_SCHEMA.safeParse`
// check), so a regression in the port cannot ship an invalid record.
func validateFeedbackRecord(record *schemas.FeedbackRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = schemas.ParseFeedbackRecord(raw)
	return err
}
