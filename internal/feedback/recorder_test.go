// Ported from packages/core/test/feedback-recorder.test.ts (V-P10-1..V-P10-4;
// P10-T02..T05; ARCH §8.7): feedback recorder, hygiene normalizer and FN/FP
// capture flows. Temp state dirs via t.TempDir(), injected clocks, no network;
// the package is imported directly (barrel wiring is a wave-boundary unit).
//
// Helper names are feedback-prefixed so sibling black-box test files in
// package feedback_test stay collision-free.

package feedback_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/feedback"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	feedbackClockAt         = "2026-10-08T12:00:00Z"
	feedbackLabelAt         = "2026-10-08T12:05:00Z"
	feedbackDecisionID      = "d_01JAR3T7DECISIONIDFIXTURE"
	feedbackPolicyVersion   = 17
	feedbackEditDiff        = "-rate = 0.19\n+rate = 0.21"
	feedbackTimeoutLabelID  = "01JAR3T80000000000000000AB"
	feedbackRejectedPatchID = "patch_01JAR3T9PATCHFIXTURE0000"
)

func feedbackClock() string { return feedbackClockAt }

func feedbackFixtureInput(signal schemas.FeedbackEventSignal) feedback.FeedbackEventInput {
	input := feedback.FeedbackEventInput{
		DecisionID:    feedbackDecisionID,
		PolicyVersion: feedbackPolicyVersion,
		Signal:        signal,
	}
	switch signal {
	case schemas.FeedbackEventSignalEdit:
		input.EditDiff = new(feedbackEditDiff)
	case schemas.FeedbackEventSignalReject:
		input.RejectReason = new(feedback.DOC_ALREADY_CORRECT_REASON)
	}
	return input
}

func feedbackNewRecorder(t *testing.T, stateDir string) *feedback.FeedbackRecorder {
	t.Helper()
	recorder, err := feedback.CreateFeedbackRecorder(feedback.FeedbackRecorderOptions{
		StateDir: stateDir,
		Clock:    feedbackClock,
	})
	require.NoError(t, err)
	return recorder
}

func feedbackNewLabelStore(t *testing.T, stateDir string) *feedback.HygieneLabelStore {
	t.Helper()
	store, err := feedback.CreateHygieneLabelStore(feedback.HygieneLabelStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	return store
}

// feedbackRawLineCount ports the TS `raw.trimEnd().split('\n')` length check.
func feedbackRawLineCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	trimmed := strings.TrimRight(string(raw), "\n")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func TestFeedbackRecorderAndHygieneNormalizer(t *testing.T) {
	t.Run("V-P10-1: fixture signals accept/edit/reject/mark-as-intentional/always-ignore produce exactly one correctly normalized feedback row each, carrying policy_version + decision_id + signal type", func(t *testing.T) {
		stateDir := t.TempDir()
		recorder := feedbackNewRecorder(t, stateDir)
		store := feedbackNewLabelStore(t, stateDir)

		signals := []schemas.FeedbackEventSignal{
			schemas.FeedbackEventSignalAccept,
			schemas.FeedbackEventSignalEdit,
			schemas.FeedbackEventSignalReject,
			schemas.FeedbackEventSignalMarkAsIntentional,
			schemas.FeedbackEventSignalAlwaysIgnore,
		}
		for _, signal := range signals {
			event, err := recorder.Record(feedbackFixtureInput(signal))
			require.NoError(t, err)
			label, err := feedback.NormalizeFeedbackEvent(*event, feedback.NormalizeOptions{}, feedback.NormalizeContext{At: feedbackLabelAt})
			require.NoError(t, err)
			_, err = store.Write(*label)
			require.NoError(t, err)
		}

		events, err := recorder.List()
		require.NoError(t, err)
		require.Len(t, events, len(signals))
		eventIDs := map[string]bool{}
		for index, signal := range signals {
			assert.Equal(t, signal, events[index].Signal)
			assert.Equal(t, feedbackPolicyVersion, events[index].PolicyVersion)
			assert.Equal(t, feedbackDecisionID, events[index].DecisionID)
			assert.Equal(t, feedbackClock(), events[index].At)
			assert.True(t, docutil.IsValidULID(events[index].EventID), "event_id must be a canonical ULID, got %q", events[index].EventID)
			assert.False(t, eventIDs[events[index].EventID], "event ids must stay distinct")
			eventIDs[events[index].EventID] = true
		}

		expectedLabels := map[schemas.FeedbackEventSignal]struct {
			label        schemas.HygieneLabelName
			sourceSignal schemas.HygieneSourceSignal
			hygieneRule  string
		}{
			schemas.FeedbackEventSignalAccept:            {schemas.HygieneLabelAccepted, schemas.HygieneSourceAccept, "direct"},
			schemas.FeedbackEventSignalEdit:              {schemas.HygieneLabelCorrected, schemas.HygieneSourceEdit, "edit⇒corrected"},
			schemas.FeedbackEventSignalReject:            {schemas.HygieneLabelFalsePositive, schemas.HygieneSourceReject, "reject_reason:doc_already_correct⇒false_positive"},
			schemas.FeedbackEventSignalMarkAsIntentional: {schemas.HygieneLabelAccepted, schemas.HygieneSourceMarkAsIntentional, "direct"},
			schemas.FeedbackEventSignalAlwaysIgnore:      {schemas.HygieneLabelStyleRejected, schemas.HygieneSourceAlwaysIgnore, "always_ignore⇒aggregation"},
		}

		labels, err := store.List()
		require.NoError(t, err)
		require.Len(t, labels, len(signals))
		for index, signal := range signals {
			expected := expectedLabels[signal]
			assert.Equal(t, expected.label, labels[index].Label)
			assert.Equal(t, expected.sourceSignal, labels[index].SourceSignal)
			assert.Equal(t, expected.hygieneRule, labels[index].HygieneRule)
			assert.Equal(t, feedbackPolicyVersion, labels[index].PolicyVersion)
			assert.Equal(t, feedbackDecisionID, labels[index].DecisionID)
			assert.Equal(t, feedbackLabelAt, labels[index].NormalizedAt)
			assert.True(t, docutil.IsValidULID(labels[index].LabelID), "label_id must be a canonical ULID, got %q", labels[index].LabelID)
		}

		eventsFile := filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		assert.Equal(t, len(signals), feedbackRawLineCount(t, eventsFile))
		labelsFile := filepath.Join(stateDir, feedback.FEEDBACK_LABELS_FILENAME)
		assert.Equal(t, len(signals), feedbackRawLineCount(t, labelsFile))
	})

	t.Run("V-P10-2: timeout case stored unlabeled (never accepted); edit case stores the reviewer edit diff with label corrected", func(t *testing.T) {
		stateDir := t.TempDir()
		recorder := feedbackNewRecorder(t, stateDir)
		store := feedbackNewLabelStore(t, stateDir)

		// Timeout: the observation window expired — even a weak stale accept on
		// the event must never promote the row to `accepted` (ARCH §8.7).
		staleAccept, err := recorder.Record(feedbackFixtureInput(schemas.FeedbackEventSignalAccept))
		require.NoError(t, err)
		timeoutSeconds := 900
		timeoutRow, err := feedback.NormalizeFeedbackEvent(*staleAccept, feedback.NormalizeOptions{TimeoutSeconds: &timeoutSeconds},
			feedback.NormalizeContext{At: feedbackLabelAt, LabelID: new(feedbackTimeoutLabelID)})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelUnlabeled, timeoutRow.Label)
		assert.NotEqual(t, schemas.HygieneLabelAccepted, timeoutRow.Label)
		assert.Equal(t, schemas.HygieneSourceTimeout, timeoutRow.SourceSignal)
		assert.Equal(t, "timeout⇒unlabeled", timeoutRow.HygieneRule)
		_, err = store.Write(*timeoutRow)
		require.NoError(t, err)

		// Edit: the edit diff is gold — stored on the label (OD-R14).
		editEvent, err := recorder.Record(feedbackFixtureInput(schemas.FeedbackEventSignalEdit))
		require.NoError(t, err)
		editRow, err := feedback.NormalizeFeedbackEvent(*editEvent, feedback.NormalizeOptions{}, feedback.NormalizeContext{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelCorrected, editRow.Label)
		assert.Equal(t, "edit⇒corrected", editRow.HygieneRule)
		require.NotNil(t, editRow.EditDiff)
		assert.Equal(t, feedbackEditDiff, *editRow.EditDiff)
		_, err = store.Write(*editRow)
		require.NoError(t, err)

		stored, err := store.List()
		require.NoError(t, err)
		require.Len(t, stored, 2)
		assert.Equal(t, schemas.HygieneLabelUnlabeled, stored[0].Label)
		assert.Equal(t, schemas.HygieneLabelCorrected, stored[1].Label)
		require.NotNil(t, stored[1].EditDiff)
		assert.Equal(t, feedbackEditDiff, *stored[1].EditDiff)
	})

	t.Run("V-P10-3: reject reason doc-already-correct ⇒ false_positive; any other reason ⇒ style_rejected", func(t *testing.T) {
		stateDir := t.TempDir()
		recorder := feedbackNewRecorder(t, stateDir)
		store := feedbackNewLabelStore(t, stateDir)

		fpInput := feedbackFixtureInput(schemas.FeedbackEventSignalReject)
		fpInput.RejectReason = new("Doc  already correct")
		fpEvent, err := recorder.Record(fpInput)
		require.NoError(t, err)
		fpRow, err := feedback.NormalizeFeedbackEvent(*fpEvent, feedback.NormalizeOptions{}, feedback.NormalizeContext{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelFalsePositive, fpRow.Label)
		assert.Equal(t, "reject_reason:doc_already_correct⇒false_positive", fpRow.HygieneRule)
		_, err = store.Write(*fpRow)
		require.NoError(t, err)

		styleInput := feedbackFixtureInput(schemas.FeedbackEventSignalReject)
		styleInput.RejectReason = new("the heading style is off")
		styleEvent, err := recorder.Record(styleInput)
		require.NoError(t, err)
		styleRow, err := feedback.NormalizeFeedbackEvent(*styleEvent, feedback.NormalizeOptions{}, feedback.NormalizeContext{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelStyleRejected, styleRow.Label)
		assert.Equal(t, "reject_reason:other⇒style_rejected", styleRow.HygieneRule)
		_, err = store.Write(*styleRow)
		require.NoError(t, err)

		// Same mapping from the direct FP capture flow (decision + patch).
		capture, err := feedback.CaptureFalsePositive(
			feedback.RejectedPatchContext{DecisionID: feedbackDecisionID, PolicyVersion: feedbackPolicyVersion},
			feedbackRejectedPatchID, feedback.DOC_ALREADY_CORRECT_REASON,
			feedback.CaptureFalsePositiveOptions{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelFalsePositive, capture.Label.Label)
		assert.Equal(t, feedbackRejectedPatchID, capture.PatchID)
		assert.True(t, docutil.IsValidULID(capture.Label.LabelID))

		captureStyle, err := feedback.CaptureFalsePositive(
			feedback.RejectedPatchContext{DecisionID: feedbackDecisionID, PolicyVersion: feedbackPolicyVersion},
			feedbackRejectedPatchID, "wording is too verbose for our style guide",
			feedback.CaptureFalsePositiveOptions{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelStyleRejected, captureStyle.Label.Label)
		assert.Equal(t, "reject_reason:other⇒style_rejected", captureStyle.Label.HygieneRule)

		stored, err := store.List()
		require.NoError(t, err)
		require.Len(t, stored, 2)
		assert.Equal(t, schemas.HygieneLabelFalsePositive, stored[0].Label)
		assert.Equal(t, schemas.HygieneLabelStyleRejected, stored[1].Label)
	})

	t.Run("V-P10-4: Jev ignored docs and a later commit edits the doc ⇒ a true-negative row bound to the originating decision", func(t *testing.T) {
		stateDir := t.TempDir()
		store := feedbackNewLabelStore(t, stateDir)

		rows, err := feedback.DetectFalseNegatives(feedback.DetectFalseNegativesInput{
			Decisions: []feedback.DecisionRecord{
				{DecisionID: "d_ignored", DocsAffected: false, PRNumber: 42, PolicyVersion: feedbackPolicyVersion},
				{DecisionID: "d_routed", DocsAffected: true, PRNumber: 43, PolicyVersion: feedbackPolicyVersion},
			},
			LaterDocEdits: []feedback.LaterDocEdit{
				{PRNumber: 42, DocPath: "docs/api.md", SHA: "abc123", At: feedbackLabelAt},
				{PRNumber: 43, DocPath: "docs/api.md", SHA: "def456", At: feedbackLabelAt},
			},
			At: feedbackLabelAt,
		})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		row := rows[0]
		assert.Equal(t, schemas.HygieneLabelTrueNegative, row.Label)
		assert.Equal(t, schemas.HygieneSourcePostMergeEdit, row.SourceSignal)
		assert.Equal(t, "post_merge_edit⇒true_negative", row.HygieneRule)
		assert.Equal(t, "d_ignored", row.DecisionID)
		assert.True(t, docutil.IsValidULID(row.LabelID))

		_, err = store.Write(row)
		require.NoError(t, err)
		bound, err := store.ListByDecisionID("d_ignored")
		require.NoError(t, err)
		require.Len(t, bound, 1)
		assert.Equal(t, schemas.HygieneLabelTrueNegative, bound[0].Label)
		unbound, err := store.ListByDecisionID("d_routed")
		require.NoError(t, err)
		assert.Empty(t, unbound)
	})
}

func TestFeedbackFailClosedBehavior(t *testing.T) {
	t.Run("refuses a malformed event (reject without reason) and stores nothing", func(t *testing.T) {
		stateDir := t.TempDir()
		recorder := feedbackNewRecorder(t, stateDir)
		_, err := recorder.Record(feedback.FeedbackEventInput{
			DecisionID:    feedbackDecisionID,
			PolicyVersion: feedbackPolicyVersion,
			Signal:        schemas.FeedbackEventSignalReject,
		})
		require.Error(t, err)
		var invalid *feedback.FeedbackValidationError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_INVALID, invalid.Code())
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_INVALID, docutil.CodeOf(err))
		assert.Contains(t, invalid.Message, "feedback event refused at recorder.record (fail closed)")

		events, err := recorder.List()
		require.NoError(t, err)
		assert.Empty(t, events)
		_, statErr := os.Stat(filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME))
		assert.True(t, os.IsNotExist(statErr), "a refused row must never touch the sink")
	})

	t.Run("refuses to replay a corrupt events.jsonl line (fail closed)", func(t *testing.T) {
		stateDir := t.TempDir()
		recorder := feedbackNewRecorder(t, stateDir)
		_, err := recorder.Record(feedbackFixtureInput(schemas.FeedbackEventSignalAccept))
		require.NoError(t, err)
		file := filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(file, append(raw, []byte("NOT-JSON\n")...), 0o644))

		_, err = recorder.List()
		require.Error(t, err)
		var invalid *feedback.FeedbackValidationError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_INVALID, invalid.Code())
		assert.Contains(t, invalid.Message, "feedback sink \"feedback/events.jsonl\" line 2 is not valid JSON — refusing to read (fail closed)")
	})

	t.Run("normalizer output round-trips through HYGIENE_LABEL_SCHEMA parsing", func(t *testing.T) {
		event, err := feedback.ParseFeedbackEvent(map[string]any{
			"schema":         "docsync.feedback-event/v1",
			"event_id":       "01JAR3TA000000000000000000",
			"at":             feedbackClock(),
			"decision_id":    feedbackDecisionID,
			"policy_version": feedbackPolicyVersion,
			"signal":         "edit",
			"edit_diff":      feedbackEditDiff,
		}, "test")
		require.NoError(t, err)
		row, err := feedback.NormalizeFeedbackEvent(*event, feedback.NormalizeOptions{}, feedback.NormalizeContext{At: feedbackLabelAt})
		require.NoError(t, err)
		assert.Equal(t, "docsync.hygiene-label/v1", row.Schema)
		assert.Equal(t, schemas.HygieneLabelCorrected, row.Label)
	})
}
