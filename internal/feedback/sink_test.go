// Sink-level tests for the append-only JSONL port (core/src/feedback/sink.ts).
// The TS suite exercised the sink only through the recorder and the label
// store; the guards, the retry-then-fail-loud path, the missing-file soft
// outcome and the wire bytes are pinned here.

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

func TestJsonlSinkGuards(t *testing.T) {
	t.Run("rejects a non-absolute state dir (fail closed)", func(t *testing.T) {
		_, err := feedback.CreateJsonlSink(".docsync/state", feedback.FEEDBACK_EVENTS_FILENAME)
		require.Error(t, err)
		var sinkErr *feedback.FeedbackSinkError
		require.ErrorAs(t, err, &sinkErr)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, sinkErr.Code())
		assert.Contains(t, sinkErr.Message, "createJsonlSink: stateDir must be an absolute path (got \".docsync/state\")")
	})

	t.Run("rejects an empty, absolute or escaping filename", func(t *testing.T) {
		stateDir := t.TempDir()
		for _, filename := range []string{"", "/etc/feedback.jsonl", "../outside.jsonl", "feedback/../../outside.jsonl"} {
			_, err := feedback.CreateJsonlSink(stateDir, filename)
			require.Error(t, err, "filename %q must be refused", filename)
			var sinkErr *feedback.FeedbackSinkError
			require.ErrorAs(t, err, &sinkErr)
			assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, sinkErr.Code())
			assert.Contains(t, sinkErr.Message, "feedback sink filename must be a state-dir-relative path")
		}
	})

	t.Run("truncates an over-long state dir in the refusal message", func(t *testing.T) {
		long := strings.Repeat("a", 96) + "/state"
		_, err := feedback.CreateJsonlSink(long, feedback.FEEDBACK_EVENTS_FILENAME)
		require.Error(t, err)
		var sinkErr *feedback.FeedbackSinkError
		require.ErrorAs(t, err, &sinkErr)
		assert.Contains(t, sinkErr.Message, "createJsonlSink: stateDir must be an absolute path (got \""+
			strings.Repeat("a", 64)+"\")")
	})

	t.Run("OD-R22 row paths are the state-dir-relative feedback files", func(t *testing.T) {
		assert.Equal(t, "feedback", feedback.FEEDBACK_DIR_NAME)
		assert.Equal(t, "feedback/events.jsonl", feedback.FEEDBACK_EVENTS_FILENAME)
		assert.Equal(t, "feedback/labels.jsonl", feedback.FEEDBACK_LABELS_FILENAME)
	})
}

func TestJsonlSinkPersistence(t *testing.T) {
	t.Run("appends the TS wire bytes and never rewrites earlier rows", func(t *testing.T) {
		stateDir := t.TempDir()
		sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, err)

		event := schemas.FeedbackEventExample()
		require.NoError(t, sink.Append(event))
		label := schemas.HygieneLabelExample()
		require.NoError(t, sink.Append(label))

		raw, err := os.ReadFile(filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME))
		require.NoError(t, err)
		// Byte-for-byte the TS `JSON.stringify(row)` output: same keys, same
		// declared order, same escaping (the file is append-only wire data).
		expected := `{"schema":"docsync.feedback-event/v1","event_id":"01J9ZM2Q7AWX4B6C8D0E2F4G6H",` +
			`"at":"2026-10-05T09:14:00Z","decision_id":"d_01J9ZK7Q2M","policy_version":17,` +
			`"signal":"edit","edit_diff":"-rate = 0.19\n+rate = 0.21","run_id":"run_01J9ZK6X8A",` +
			`"doc_path":"docs/payments.md","repo":"acme/widgets"}` + "\n" +
			`{"schema":"docsync.hygiene-label/v1","label_id":"01J9ZM4R9BYZ5C7D9E1F3A5B7C",` +
			`"decision_id":"d_01J9ZK7Q2M","policy_version":17,"label":"corrected",` +
			`"source_signal":"edit","edit_diff":"-rate = 0.19\n+rate = 0.21",` +
			`"hygiene_rule":"edit⇒corrected","normalized_at":"2026-10-05T09:20:00Z"}` + "\n"
		assert.Equal(t, expected, string(raw))
	})

	t.Run("a missing file replays as the soft empty outcome", func(t *testing.T) {
		stateDir := t.TempDir()
		sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, err)
		rows, err := sink.ReadAll(func(value any, lineNumber int) (any, error) { return value, nil })
		require.NoError(t, err)
		assert.Empty(t, rows)
	})

	t.Run("replays rows with 1-based line numbers, skipping blank lines", func(t *testing.T) {
		stateDir := t.TempDir()
		file := filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte("\n{\"a\":1}\n\n{\"a\":2}\n"), 0o644))

		sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, err)
		seen := []int{}
		_, err = feedback.ReadAllRows(sink, func(value any, lineNumber int) (any, error) {
			seen = append(seen, lineNumber)
			return value, nil
		})
		require.NoError(t, err)
		assert.Equal(t, []int{2, 4}, seen)
	})

	t.Run("retries the append exactly once, then fails LOUD", func(t *testing.T) {
		stateDir := t.TempDir()
		// `feedback` as a regular FILE makes both the mkdir and the append
		// fail deterministically (no injected clock, no races).
		require.NoError(t, os.WriteFile(filepath.Join(stateDir, feedback.FEEDBACK_DIR_NAME), []byte("not a directory"), 0o644))
		sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, err)

		err = sink.Append(schemas.FeedbackEventExample())
		require.Error(t, err)
		var sinkErr *feedback.FeedbackSinkError
		require.ErrorAs(t, err, &sinkErr)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, sinkErr.Code())
		assert.Contains(t, sinkErr.Message, `feedback row could not be appended to "feedback/events.jsonl" after one retry`)
		assert.NotNil(t, sinkErr.Cause, "the underlying OS failure is preserved as the cause")
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, docutil.CodeOf(err))
		assert.Equal(t, "EDOCSYNC_FEEDBACK_SINK: "+sinkErr.Message, sinkErr.Error())
		var coded *docutil.Error
		require.ErrorAs(t, err, &coded)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, coded.Code)
		assert.NotNil(t, coded.Unwrap())
	})

	t.Run("a value that cannot be serialized is refused before any write", func(t *testing.T) {
		stateDir := t.TempDir()
		sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, err)
		err = sink.Append(make(chan int))
		require.Error(t, err)
		var sinkErr *feedback.FeedbackSinkError
		require.ErrorAs(t, err, &sinkErr)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, sinkErr.Code())
		assert.Contains(t, sinkErr.Message, "feedback row could not be serialized")
		_, statErr := os.Stat(filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME))
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("an unreadable (non-missing) sink file fails loud", func(t *testing.T) {
		stateDir := t.TempDir()
		file := filepath.Join(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.Mkdir(file, 0o755))
		sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, err)

		_, err = sink.ReadAll(func(value any, lineNumber int) (any, error) { return value, nil })
		require.Error(t, err)
		var sinkErr *feedback.FeedbackSinkError
		require.ErrorAs(t, err, &sinkErr)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, sinkErr.Code())
		assert.Contains(t, sinkErr.Message, `feedback sink "feedback/events.jsonl" could not be read`)
	})
}

func TestFeedbackRecorderStateDirGuard(t *testing.T) {
	t.Run("createFeedbackRecorder refuses a relative state dir at construction", func(t *testing.T) {
		_, err := feedback.CreateFeedbackRecorder(feedback.FeedbackRecorderOptions{
			StateDir: ".docsync/state",
			Clock:    feedbackClock,
		})
		require.Error(t, err)
		var sinkErr *feedback.FeedbackSinkError
		require.ErrorAs(t, err, &sinkErr)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, sinkErr.Code())

		_, err = feedback.CreateHygieneLabelStore(feedback.HygieneLabelStoreOptions{StateDir: ".docsync/state"})
		require.Error(t, err)
		require.ErrorAs(t, err, &sinkErr)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_SINK, sinkErr.Code())
	})

	t.Run("an injected sink bypasses the state dir, and an injected ULID generator stays deterministic", func(t *testing.T) {
		stateDir := t.TempDir()
		sink, err := feedback.CreateJsonlSink(stateDir, feedback.FEEDBACK_EVENTS_FILENAME)
		require.NoError(t, err)
		recorder, err := feedback.CreateFeedbackRecorder(feedback.FeedbackRecorderOptions{
			StateDir: stateDir,
			Clock:    feedbackClock,
			Sink:     sink,
			EventIDs: docutil.NewULIDGeneratorWith(
				func() int64 { return 0 },
				func(n int) ([]byte, error) { return make([]byte, n), nil },
			),
		})
		require.NoError(t, err)
		first, err := recorder.Record(feedbackFixtureInput(schemas.FeedbackEventSignalAccept))
		require.NoError(t, err)
		second, err := recorder.Record(feedbackFixtureInput(schemas.FeedbackEventSignalEdit))
		require.NoError(t, err)
		assert.Equal(t, "00000000000000000000000000", first.EventID)
		assert.Equal(t, "00000000000000000000000001", second.EventID)

		events, err := recorder.List()
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, first.EventID, events[0].EventID)
		assert.Equal(t, second.EventID, events[1].EventID)
	})
}

func TestLabelStoreRejectsUnknownKeys(t *testing.T) {
	t.Run("a row carrying an unknown key is refused on replay (strict)", func(t *testing.T) {
		stateDir := t.TempDir()
		file := filepath.Join(stateDir, feedback.FEEDBACK_LABELS_FILENAME)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(`{"schema":"docsync.hygiene-label/v1",`+
			`"label_id":"01J9ZM4R9BYZ5C7D9E1F3A5B7C","decision_id":"d_01J9ZK7Q2M","policy_version":17,`+
			`"label":"corrected","source_signal":"edit","hygiene_rule":"edit⇒corrected",`+
			`"normalized_at":"2026-10-05T09:20:00Z","surprise":true}`+"\n"), 0o644))

		store, err := feedback.CreateHygieneLabelStore(feedback.HygieneLabelStoreOptions{StateDir: stateDir})
		require.NoError(t, err)
		_, err = store.List()
		require.Error(t, err)
		var invalid *feedback.FeedbackValidationError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, feedback.EDOCSYNC_FEEDBACK_INVALID, invalid.Code())
		assert.Contains(t, invalid.Message, "hygiene label refused at hygiene.list line 1 (fail closed)")

		replayed, err := feedback.ParseHygieneLabel(schemas.HygieneLabelExample(), "test")
		require.NoError(t, err)
		assert.Equal(t, schemas.HygieneLabelCorrected, replayed.Label)
	})
}
