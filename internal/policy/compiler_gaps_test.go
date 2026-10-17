package policy

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatIssueListValidationError(t *testing.T) {
	issues := []schemas.Issue{
		{Path: []string{"routing", "high_confidence"}, Message: "must be within [0, 1]"},
		{Path: []string{"questions", "0", "type"}, Message: "unknown question type"},
	}
	validationErr := schemas.NewValidationError(issues...)

	got := formatIssueList(validationErr)
	require.NotEmpty(t, got)
	assert.Equal(t, "routing.high_confidence: must be within [0, 1]; questions.0.type: unknown question type", got)
}

func TestFormatIssueListPlainError(t *testing.T) {
	plain := errors.New("something went wrong")
	assert.Equal(t, "something went wrong", formatIssueList(plain))
}

func TestFormatIssueListNil(t *testing.T) {
	assert.Equal(t, "", formatIssueList(nil))
}
