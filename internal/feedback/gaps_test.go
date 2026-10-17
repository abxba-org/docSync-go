package feedback_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/feedback"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJsonlSink_ExistingDirFirstAttemptSucceeds(t *testing.T) {
	stateDir := t.TempDir()
	sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)), 0o755))
	require.NoError(t, sink.Append(schemas.FeedbackEventExample()))
}

func TestJsonlSink_AppendWriteError(t *testing.T) {
	stateDir := t.TempDir()
	sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
	require.NoError(t, err)
	feedbackDir := filepath.Join(stateDir, "feedback")
	require.NoError(t, os.MkdirAll(feedbackDir, 0o755))
	require.NoError(t, os.Chmod(feedbackDir, 0o555))
	err = sink.Append(schemas.FeedbackEventExample())
	require.Error(t, err)
	_ = os.Chmod(feedbackDir, 0o755)
}

func TestRecorderRecordSinkError(t *testing.T) {
	stateDir := t.TempDir()
	sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
	require.NoError(t, err)
	file := filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(""), 0o644))
	require.NoError(t, os.Chmod(file, 0o444))
	require.NoError(t, os.Chmod(filepath.Dir(file), 0o555))
	recorder, err := feedback.CreateFeedbackRecorder(feedback.FeedbackRecorderOptions{
		StateDir: stateDir,
		Clock:    func() string { return "2026-10-08T12:00:00Z" },
		Sink:     sink,
	})
	require.NoError(t, err)
	_, err = recorder.Record(feedback.FeedbackEventInput{
		DecisionID:    "d_test",
		PolicyVersion: 1,
		Signal:        schemas.FeedbackEventSignalAccept,
	})
	require.Error(t, err)
	_ = os.Chmod(filepath.Dir(file), 0o755)
	_ = os.Chmod(file, 0o644)
}

func TestRecorderListParseError(t *testing.T) {
	stateDir := t.TempDir()
	recorder := feedbackNewRecorder(t, stateDir)
	_, err := recorder.Record(feedbackFixtureInput(schemas.FeedbackEventSignalAccept))
	require.NoError(t, err)
	file := filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	badRow := []byte(`{"schema":"valid","event_id":"x"}`)
	require.NoError(t, os.WriteFile(file, append(content, badRow...), 0o644))
	_, err = recorder.List()
	require.Error(t, err)
}

func TestRecorderListByDecisionIDError(t *testing.T) {
	stateDir := t.TempDir()
	recorder := feedbackNewRecorder(t, stateDir)
	_, err := recorder.Record(feedbackFixtureInput(schemas.FeedbackEventSignalAccept))
	require.NoError(t, err)
	file := filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	badRow := []byte(`{"schema":"nope","event_id":"x"}`)
	require.NoError(t, os.WriteFile(file, append(content, badRow...), 0o644))
	_, err = recorder.ListByDecisionID("d_test")
	require.Error(t, err)
}

func TestHygieneWriteSinkError(t *testing.T) {
	stateDir := t.TempDir()
	sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_LABELS_FILENAME)
	require.NoError(t, err)
	store, err := feedback.CreateHygieneLabelStore(feedback.HygieneLabelStoreOptions{
		StateDir: stateDir,
		Sink:     sink,
	})
	require.NoError(t, err)
	labelsFile := filepath.Join(stateDir, feedback.FEEDBACK_LABELS_FILENAME)
	require.NoError(t, os.MkdirAll(filepath.Dir(labelsFile), 0o755))
	require.NoError(t, os.WriteFile(labelsFile, []byte(""), 0o644))
	require.NoError(t, os.Chmod(labelsFile, 0o444))
	require.NoError(t, os.Chmod(filepath.Dir(labelsFile), 0o555))
	_, err = store.Write(schemas.HygieneLabel{
		Schema:        "docsync.hygiene-label/v1",
		LabelID:       "01J9ZM4R9BYZ5C7D9E1F3A5B7C",
		DecisionID:    "d_test",
		PolicyVersion: 1,
		Label:         schemas.HygieneLabelAccepted,
		SourceSignal:  schemas.HygieneSourceAccept,
		HygieneRule:   "direct",
		NormalizedAt:  "2026-10-08T12:00:00Z",
	})
	require.Error(t, err)
	_ = os.Chmod(filepath.Dir(labelsFile), 0o755)
	_ = os.Chmod(labelsFile, 0o644)
}

func TestHygieneListByDecisionIDError(t *testing.T) {
	stateDir := t.TempDir()
	store := feedbackNewLabelStore(t, stateDir)
	labelsFile := filepath.Join(stateDir, feedback.FEEDBACK_LABELS_FILENAME)
	require.NoError(t, os.MkdirAll(filepath.Dir(labelsFile), 0o755))
	require.NoError(t, os.WriteFile(labelsFile, []byte(`{"schema":"docsync.hygiene-label/v1","label_id":"01J9ZM4R9BYZ5C7D9E1F3A5B7C","decision_id":"d_test","policy_version":1,"label":"accepted","source_signal":"accept","hygiene_rule":"direct","normalized_at":"2026-10-08T12:00:00Z"}`+"\n"), 0o644))
	content, err := os.ReadFile(labelsFile)
	require.NoError(t, err)
	badRow := []byte(`{"schema":"nope","label_id":"x"}`)
	require.NoError(t, os.WriteFile(labelsFile, append(content, badRow...), 0o644))
	_, err = store.ListByDecisionID("d_test")
	require.Error(t, err)
}

func TestOverrideAggregation_NullDocPathBucket(t *testing.T) {
	rows := []schemas.FeedbackEvent{
		feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalReject
			row.RejectReason = new("too verbose")
		}),
		feedbackCorrectionRowWithID("01JAR3TB000000000000000002", func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalReject
			row.RejectReason = new("too verbose")
		}),
	}
	acks, err := feedback.AggregateOverrides(feedback.AggregateOverridesInput{Rows: rows, At: "2026-10-08T12:00:00Z"})
	require.NoError(t, err)
	require.Len(t, acks, 1)
	assert.Nil(t, acks[0].DocPath)
	assert.Equal(t, 2, acks[0].Count)
}

func TestOverrideAggregation_EditSignalAggregates(t *testing.T) {
	rows := []schemas.FeedbackEvent{
		feedbackCorrectionRow(func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalEdit
			row.EditDiff = new("-a\n+b")
		}),
		feedbackCorrectionRowWithID("01JAR3TB000000000000000003", func(row *schemas.FeedbackEvent) {
			row.Signal = schemas.FeedbackEventSignalEdit
			row.EditDiff = new("-a\n+b")
		}),
	}
	acks, err := feedback.AggregateOverrides(feedback.AggregateOverridesInput{Rows: rows, At: "2026-10-08T12:00:00Z"})
	require.NoError(t, err)
	require.Len(t, acks, 1)
	assert.Equal(t, "-a +b", acks[0].Statement)
	assert.Equal(t, 2, acks[0].Count)
}
