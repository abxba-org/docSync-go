package feedback_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/feedback"
	"github.com/stretchr/testify/require"
)

func TestParseFeedbackEventWithNonJsonValue(t *testing.T) {
	value := map[string]any{
		"x": map[int]int{1: 2},
	}
	_, err := feedback.ParseFeedbackEvent(value, "test")
	require.Error(t, err)
}
