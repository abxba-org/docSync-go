package feedback_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/feedback"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureFalsePositiveStyleRejectionNoLabelID(t *testing.T) {
	capture, err := feedback.CaptureFalsePositive(
		feedback.RejectedPatchContext{DecisionID: "d_test", PolicyVersion: 1},
		"patch_001", "some style concern",
		feedback.CaptureFalsePositiveOptions{At: "2026-10-08T12:00:00Z"})
	require.NoError(t, err)
	assert.Equal(t, schemas.HygieneLabelStyleRejected, capture.Label.Label)
	assert.Equal(t, "reject_reason:other⇒style_rejected", capture.Label.HygieneRule)
	assert.NotEmpty(t, capture.Label.LabelID)
}

func TestDetectFalseNegatives_EmptyInputs(t *testing.T) {
	rows, err := feedback.DetectFalseNegatives(feedback.DetectFalseNegativesInput{
		Decisions:     nil,
		LaterDocEdits: nil,
		At:            "2026-10-08T12:00:00Z",
	})
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestNormalizeRejectReasonEmptyString(t *testing.T) {
	result := feedback.IsDocAlreadyCorrect("  ")
	assert.False(t, result)
}

func TestNormalizeCorrectionStatementEdgeCases(t *testing.T) {
	assert.Equal(t, "", feedback.NormalizeCorrectionStatement(""))
	assert.Equal(t, "", feedback.NormalizeCorrectionStatement("   "))
	assert.Equal(t, "a b", feedback.NormalizeCorrectionStatement("A   B  "))
}
