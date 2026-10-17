package feedback

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingSink struct {
	appended []any
}

func (s *failingSink) Append(value any) error {
	s.appended = append(s.appended, value)
	return nil
}

func (s *failingSink) ReadAll(parse func(value any, lineNumber int) (any, error)) ([]any, error) {
	return nil, nil
}

func TestRecord_EventIDGenerateFailure(t *testing.T) {
	tmpDir := t.TempDir()

	failingGen := docutil.NewULIDGeneratorWith(
		func() int64 { return -1 },
		func(n int) ([]byte, error) { return make([]byte, n), nil },
	)

	recorder, err := CreateFeedbackRecorder(FeedbackRecorderOptions{
		StateDir: tmpDir,
		EventIDs: failingGen,
	})
	require.NoError(t, err)

	input := FeedbackEventInput{
		EventID:        nil,
		DecisionID:     "d1",
		PolicyVersion:  1,
		Signal:         schemas.FeedbackEventSignalReject,
		RejectReason:   func() *string { s := "doc already correct"; return &s }(),
	}

	_, err = recorder.Record(input)
	require.Error(t, err)
	assert.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "feedback event id generation failed")
	assert.Contains(t, err.Error(), "timestamp out of the 48-bit range")
}

func TestRecord_HappyPath(t *testing.T) {
	tmpDir := t.TempDir()

	workingGen := docutil.NewULIDGenerator()
	mockSink := &failingSink{}

	recorder, err := CreateFeedbackRecorder(FeedbackRecorderOptions{
		StateDir: tmpDir,
		EventIDs: workingGen,
		Sink:     mockSink,
	})
	require.NoError(t, err)

	input := FeedbackEventInput{
		EventID:        nil,
		DecisionID:     "d1",
		PolicyVersion:  1,
		Signal:         schemas.FeedbackEventSignalReject,
		RejectReason:   func() *string { s := "doc already correct"; return &s }(),
		At:             func() *string { s := "2026-10-08T12:00:00Z"; return &s }(),
	}

	_, err = recorder.Record(input)
	require.NoError(t, err)
	assert.Len(t, mockSink.appended, 1)
}
