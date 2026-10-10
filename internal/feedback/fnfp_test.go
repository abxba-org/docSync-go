// FN/FP capture-flow tests (core/src/feedback/fnfp.ts) plus the normalization
// edges the TS suite exercised only implicitly: the out-of-band fallbacks, the
// fail-closed unknown signal, and the label-store write guard.

package feedback_test

import (
	"fmt"
	"testing"

	"github.com/DUDE616/docSync-go/internal/feedback"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectFalseNegativesFlows(t *testing.T) {
	t.Run("one row per (decision, edited doc path), deduplicated per path", func(t *testing.T) {
		rows, err := feedback.DetectFalseNegatives(feedback.DetectFalseNegativesInput{
			Decisions: []feedback.DecisionRecord{
				{DecisionID: "d_a", DocsAffected: false, PRNumber: 7, PolicyVersion: 3},
				{DecisionID: "d_b", DocsAffected: false, PRNumber: 8, PolicyVersion: 3},
			},
			LaterDocEdits: []feedback.LaterDocEdit{
				{PRNumber: 7, DocPath: "docs/a.md", SHA: "aaa", At: feedbackLabelAt},
				{PRNumber: 7, DocPath: "docs/a.md", SHA: "bbb", At: feedbackLabelAt},
				{PRNumber: 7, DocPath: "docs/b.md", SHA: "ccc", At: feedbackLabelAt},
				{PRNumber: 8, DocPath: "docs/a.md", SHA: "ddd", At: feedbackLabelAt},
			},
			At: feedbackLabelAt,
		})
		require.NoError(t, err)
		require.Len(t, rows, 3)
		assert.Equal(t, "d_a", rows[0].DecisionID)
		assert.Equal(t, "d_a", rows[1].DecisionID)
		assert.Equal(t, "d_b", rows[2].DecisionID)
		for _, row := range rows {
			assert.Equal(t, schemas.HygieneLabelTrueNegative, row.Label)
			assert.Equal(t, schemas.HygieneSourcePostMergeEdit, row.SourceSignal)
			assert.Equal(t, "post_merge_edit⇒true_negative", row.HygieneRule)
			assert.Equal(t, feedbackLabelAt, row.NormalizedAt)
			assert.Equal(t, 3, row.PolicyVersion)
		}
		labelIDs := map[string]bool{}
		for _, row := range rows {
			assert.False(t, labelIDs[row.LabelID], "default label ids must be distinct")
			labelIDs[row.LabelID] = true
		}
	})

	t.Run("decisions with docs_affected = true never match, and an unmatched PR yields nothing", func(t *testing.T) {
		rows, err := feedback.DetectFalseNegatives(feedback.DetectFalseNegativesInput{
			Decisions: []feedback.DecisionRecord{
				{DecisionID: "d_routed", DocsAffected: true, PRNumber: 7, PolicyVersion: 3},
			},
			LaterDocEdits: []feedback.LaterDocEdit{
				{PRNumber: 7, DocPath: "docs/a.md", SHA: "aaa", At: feedbackLabelAt},
				{PRNumber: 99, DocPath: "docs/a.md", SHA: "bbb", At: feedbackLabelAt},
			},
			At: feedbackLabelAt,
		})
		require.NoError(t, err)
		assert.Empty(t, rows)
	})

	t.Run("an injected label-id factory is used verbatim", func(t *testing.T) {
		counter := 0
		rows, err := feedback.DetectFalseNegatives(feedback.DetectFalseNegativesInput{
			Decisions: []feedback.DecisionRecord{
				{DecisionID: "d_a", DocsAffected: false, PRNumber: 7, PolicyVersion: 3},
			},
			LaterDocEdits: []feedback.LaterDocEdit{
				{PRNumber: 7, DocPath: "docs/a.md", SHA: "aaa", At: feedbackLabelAt},
				{PRNumber: 7, DocPath: "docs/b.md", SHA: "bbb", At: feedbackLabelAt},
			},
			At: feedbackLabelAt,
			LabelID: func() string {
				counter++
				return fmt.Sprintf("01JAR3TD%018d", counter)
			},
		})
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, "01JAR3TD000000000000000001", rows[0].LabelID)
		assert.Equal(t, "01JAR3TD000000000000000002", rows[1].LabelID)
	})
}

func TestCaptureFalsePositiveFlows(t *testing.T) {
	decision := feedback.RejectedPatchContext{DecisionID: feedbackDecisionID, PolicyVersion: feedbackPolicyVersion}

	t.Run("the canonical FP reason captures false_positive with the patch linkage", func(t *testing.T) {
		capture, err := feedback.CaptureFalsePositive(decision, feedbackRejectedPatchID,
			"  DOC   already  correct  ", feedback.CaptureFalsePositiveOptions{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, feedbackRejectedPatchID, capture.PatchID)
		assert.Equal(t, schemas.HygieneLabelFalsePositive, capture.Label.Label)
		assert.Equal(t, "reject_reason:doc_already_correct⇒false_positive", capture.Label.HygieneRule)
		assert.Equal(t, schemas.HygieneSourceReject, capture.Label.SourceSignal)
		assert.Equal(t, feedbackDecisionID, capture.Label.DecisionID)
		assert.Equal(t, feedbackPolicyVersion, capture.Label.PolicyVersion)
		assert.Equal(t, feedbackLabelAt, capture.Label.NormalizedAt)
		assert.Nil(t, capture.Label.EditDiff)
	})

	t.Run("any other reason is a style rejection, never reinterpreted as an FP", func(t *testing.T) {
		capture, err := feedback.CaptureFalsePositive(decision, feedbackRejectedPatchID,
			"the docs were already right", feedback.CaptureFalsePositiveOptions{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelStyleRejected, capture.Label.Label)
		assert.Equal(t, "reject_reason:other⇒style_rejected", capture.Label.HygieneRule)
		assert.Equal(t, feedbackRejectedPatchID, capture.PatchID)
	})

	t.Run("a pre-assigned label id is honored", func(t *testing.T) {
		capture, err := feedback.CaptureFalsePositive(decision, feedbackRejectedPatchID,
			feedback.DOC_ALREADY_CORRECT_REASON,
			feedback.CaptureFalsePositiveOptions{At: feedbackLabelAt, LabelID: new(feedbackTimeoutLabelID)})
		require.NoError(t, err)
		assert.Equal(t, feedbackTimeoutLabelID, capture.Label.LabelID)
		assert.Equal(t, "docsync.hygiene-label/v1", capture.Label.Schema)
	})
}

func TestNormalizeFeedbackEventEdges(t *testing.T) {
	t.Run("the out-of-band edit-diff and reject-reason fallbacks apply", func(t *testing.T) {
		edit, err := feedback.NormalizeFeedbackEvent(schemas.FeedbackEvent{
			Schema:        "docsync.feedback-event/v1",
			EventID:       "01JAR3TA000000000000000000",
			At:            feedbackClockAt,
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Signal:        schemas.FeedbackEventSignalEdit,
		}, feedback.NormalizeOptions{EditDiff: new(feedbackEditDiff)}, feedback.NormalizeContext{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelCorrected, edit.Label)
		require.NotNil(t, edit.EditDiff)
		assert.Equal(t, feedbackEditDiff, *edit.EditDiff)

		reject, err := feedback.NormalizeFeedbackEvent(schemas.FeedbackEvent{
			Schema:        "docsync.feedback-event/v1",
			EventID:       "01JAR3TA000000000000000000",
			At:            feedbackClockAt,
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Signal:        schemas.FeedbackEventSignalReject,
		}, feedback.NormalizeOptions{RejectReason: new("Doc ALREADY correct")}, feedback.NormalizeContext{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelFalsePositive, reject.Label)
		assert.Equal(t, "reject_reason:doc_already_correct⇒false_positive", reject.HygieneRule)
	})

	t.Run("the event's own evidence wins over the fallbacks", func(t *testing.T) {
		event := schemas.FeedbackEvent{
			Schema:        "docsync.feedback-event/v1",
			EventID:       "01JAR3TA000000000000000000",
			At:            feedbackClockAt,
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Signal:        schemas.FeedbackEventSignalReject,
			RejectReason:  new("the heading style is off"),
		}
		row, err := feedback.NormalizeFeedbackEvent(event,
			feedback.NormalizeOptions{RejectReason: new(feedback.DOC_ALREADY_CORRECT_REASON)},
			feedback.NormalizeContext{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelStyleRejected, row.Label)
	})

	t.Run("an unknown signal fails closed instead of guessing a label", func(t *testing.T) {
		_, err := feedback.NormalizeFeedbackEvent(schemas.FeedbackEvent{
			Schema:        "docsync.feedback-event/v1",
			EventID:       "01JAR3TA000000000000000000",
			At:            feedbackClockAt,
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Signal:        schemas.FeedbackEventSignal("reopen"),
		}, feedback.NormalizeOptions{}, feedback.NormalizeContext{At: feedbackLabelAt})
		require.Error(t, err)
		var invalid *feedback.FeedbackValidationError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_INVALID, invalid.Code())
		assert.Contains(t, invalid.Message, "hygiene label refused at hygiene.normalize (fail closed)")
	})

	t.Run("a timeout row never becomes accepted even with a stale accept", func(t *testing.T) {
		timeoutSeconds := 0
		row, err := feedback.NormalizeFeedbackEvent(schemas.FeedbackEvent{
			Schema:        "docsync.feedback-event/v1",
			EventID:       "01JAR3TA000000000000000000",
			At:            feedbackClockAt,
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Signal:        schemas.FeedbackEventSignalAccept,
		}, feedback.NormalizeOptions{TimeoutSeconds: &timeoutSeconds}, feedback.NormalizeContext{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelUnlabeled, row.Label)
		assert.Equal(t, schemas.HygieneSourceTimeout, row.SourceSignal)
		assert.Equal(t, "timeout⇒unlabeled", row.HygieneRule)
	})

	t.Run("the rendered error keeps the TS code prefix and message", func(t *testing.T) {
		_, err := feedback.ParseHygieneLabel(schemas.HygieneLabel{Schema: "nope"}, "test")
		require.Error(t, err)
		assert.Equal(t, "EDOCSYNC_FEEDBACK_INVALID: hygiene label refused at test (fail closed): validation failed: "+
			"schema: Invalid literal value, expected \"docsync.hygiene-label/v1\"; label_id: Invalid; "+
			"decision_id: String must contain at least 1 character(s); policy_version: Number must be greater than 0; "+
			"label: Invalid enum value. Expected 'unlabeled' | 'accepted' | 'corrected' | 'false_positive' | "+
			"'style_rejected' | 'true_negative', received ''; source_signal: Invalid enum value. Expected "+
			"'accept' | 'edit' | 'reject' | 'mark_as_intentional' | 'always_ignore' | 'timeout' | "+
			"'post_merge_edit' | 'spot_check' | 'audit_finding', received ''; hygiene_rule: Invalid; "+
			"normalized_at: Invalid datetime", err.Error())
	})
}

func TestRecorderInjectionSurface(t *testing.T) {
	t.Run("ListByDecisionID filters the replay", func(t *testing.T) {
		stateDir := t.TempDir()
		recorder := feedbackNewRecorder(t, stateDir)
		for _, decisionID := range []string{"d_one", "d_two", "d_one"} {
			input := feedbackFixtureInput(schemas.FeedbackEventSignalAccept)
			input.DecisionID = decisionID
			_, err := recorder.Record(input)
			require.NoError(t, err)
		}
		all, err := recorder.List()
		require.NoError(t, err)
		require.Len(t, all, 3)
		filtered, err := recorder.ListByDecisionID("d_one")
		require.NoError(t, err)
		require.Len(t, filtered, 2)
		for _, row := range filtered {
			assert.Equal(t, "d_one", row.DecisionID)
		}
		empty, err := recorder.ListByDecisionID("d_missing")
		require.NoError(t, err)
		assert.Empty(t, empty)
	})

	t.Run("pre-assigned event_id/at are honored and a missing clock fails closed", func(t *testing.T) {
		stateDir := t.TempDir()
		recorder, err := feedback.CreateFeedbackRecorder(feedback.FeedbackRecorderOptions{StateDir: stateDir})
		require.NoError(t, err)
		event, err := recorder.Record(feedback.FeedbackEventInput{
			EventID:       new("01JAR3TE00000000000000000A"),
			At:            new(feedbackLabelAt),
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Signal:        schemas.FeedbackEventSignalEdit,
			EditDiff:      new(feedbackEditDiff),
		})
		require.NoError(t, err)
		assert.Equal(t, "01JAR3TE00000000000000000A", event.EventID)
		assert.Equal(t, feedbackLabelAt, event.At)

		_, err = recorder.Record(feedback.FeedbackEventInput{
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Signal:        schemas.FeedbackEventSignalAccept,
		})
		require.Error(t, err)
		var invalid *feedback.FeedbackValidationError
		require.ErrorAs(t, err, &invalid)
		assert.Contains(t, invalid.Message, "feedback event refused at recorder.record (fail closed)")
	})
}

func TestHygieneLabelStoreWriteGuard(t *testing.T) {
	t.Run("a malformed label is refused before persistence", func(t *testing.T) {
		stateDir := t.TempDir()
		store := feedbackNewLabelStore(t, stateDir)
		_, err := store.Write(schemas.HygieneLabel{
			Schema:        "docsync.hygiene-label/v1",
			LabelID:       "not-a-ulid",
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Label:         schemas.HygieneLabelAccepted,
			SourceSignal:  schemas.HygieneSourceAccept,
			HygieneRule:   "direct",
			NormalizedAt:  feedbackLabelAt,
		})
		require.Error(t, err)
		var invalid *feedback.FeedbackValidationError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_INVALID, invalid.Code())
		assert.Contains(t, invalid.Message, "hygiene label refused at hygiene.write (fail closed)")

		labels, err := store.List()
		require.NoError(t, err)
		assert.Empty(t, labels)
	})

	t.Run("the timeout-never-accepted invariant is enforced at the write", func(t *testing.T) {
		stateDir := t.TempDir()
		store := feedbackNewLabelStore(t, stateDir)
		_, err := store.Write(schemas.HygieneLabel{
			Schema:        "docsync.hygiene-label/v1",
			LabelID:       "01J9ZM4R9BYZ5C7D9E1F3A5B7C",
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Label:         schemas.HygieneLabelAccepted,
			SourceSignal:  schemas.HygieneSourceTimeout,
			HygieneRule:   "timeout⇒unlabeled",
			NormalizedAt:  feedbackLabelAt,
		})
		require.Error(t, err)
		var invalid *feedback.FeedbackValidationError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_INVALID, invalid.Code())
	})
}
