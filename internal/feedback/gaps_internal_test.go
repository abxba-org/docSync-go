package feedback

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFeedbackSinkError(t *testing.T) {
	t.Run("with cause", func(t *testing.T) {
		cause := docutil.New(docutil.CodeIO, "underlying error")
		err := newFeedbackSinkError("sink failed", cause)

		require.NotNil(t, err)
		assert.NotEmpty(t, err.Error())
		assert.Equal(t, EDOCSYNC_FEEDBACK_SINK, err.Code())
		// The inner error wraps the cause
		assert.Equal(t, cause, errorsAsCause(err))
	})

	t.Run("with nil cause", func(t *testing.T) {
		err := newFeedbackSinkError("sink failed", nil)

		require.NotNil(t, err)
		assert.NotEmpty(t, err.Error())
		assert.Equal(t, EDOCSYNC_FEEDBACK_SINK, err.Code())
		unwrapped := err.Unwrap()
		require.NotNil(t, unwrapped)
		_, ok := unwrapped.(*docutil.Error)
		assert.True(t, ok)
	})
}

func errorsAsCause(err *FeedbackSinkError) error {
	if err == nil {
		return nil
	}
	u := err.Unwrap()
	if u == nil {
		return nil
	}
	if de, ok := u.(*docutil.Error); ok && de != nil {
		return de.Cause
	}
	return u
}

func TestNewFeedbackValidationError(t *testing.T) {
	err := newFeedbackValidationError("invalid data")

	require.NotNil(t, err)
	assert.NotEmpty(t, err.Error())
	assert.Equal(t, EDOCSYNC_FEEDBACK_INVALID, err.Code())
	unwrapped := err.Unwrap()
	require.NotNil(t, unwrapped)
	_, ok := unwrapped.(*docutil.Error)
	assert.True(t, ok)
}

func TestFeedbackSinkErrorMethods(t *testing.T) {
	cause := docutil.New(docutil.CodeIO, "io error")
	err := &FeedbackSinkError{
		Message: "test message",
		Cause:   cause,
		inner:   docutil.New(EDOCSYNC_FEEDBACK_SINK, "test message").Wrap(cause),
	}

	assert.NotEmpty(t, err.Error())
	assert.Equal(t, EDOCSYNC_FEEDBACK_SINK, err.Code())
	assert.Equal(t, err.inner, err.Unwrap())
}

func TestFeedbackValidationErrorMethods(t *testing.T) {
	err := &FeedbackValidationError{
		Message: "validation failed",
		inner:   docutil.New(EDOCSYNC_FEEDBACK_INVALID, "validation failed"),
	}

	assert.NotEmpty(t, err.Error())
	assert.Equal(t, EDOCSYNC_FEEDBACK_INVALID, err.Code())
	assert.Equal(t, err.inner, err.Unwrap())
}

func TestGenerateID(t *testing.T) {
	t.Run("hygiene label", func(t *testing.T) {
		id, err := generateID("hygiene label")
		require.NoError(t, err)
		assert.NotEmpty(t, id)
		assert.Len(t, id, 26)
		assert.True(t, docutil.IsValidULID(id))
	})

	t.Run("policy proposal", func(t *testing.T) {
		id, err := generateID("policy proposal")
		require.NoError(t, err)
		assert.NotEmpty(t, id)
		assert.Len(t, id, 26)
		assert.True(t, docutil.IsValidULID(id))
	})
}

func TestCollapseWhitespace(t *testing.T) {
	result := collapseWhitespace("  hello   world  ")
	assert.Equal(t, "hello world", result)
}

func TestCollapseWhitespaceFold(t *testing.T) {
	result := collapseWhitespaceFold("  HeLLo   WoRLD  ")
	assert.Equal(t, "hello world", result)
}

func TestDeref(t *testing.T) {
	var nilPtr *string
	result := deref(nilPtr)
	assert.Equal(t, "", result)

	s := "x"
	result = deref(&s)
	assert.Equal(t, "x", result)
}

func TestOnDocPathSuffix(t *testing.T) {
	result := onDocPathSuffix(nil)
	assert.Equal(t, "", result)

	s := "docs/x.md"
	result = onDocPathSuffix(&s)
	assert.Equal(t, " on docs/x.md", result)
}
