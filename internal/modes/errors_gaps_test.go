package modes

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
)

func TestModeConflictErrorGaps(t *testing.T) {
	err := &ModeConflictError{Message: "conflict message"}
	assert.Equal(t, "conflict message", err.Error())
	assert.Equal(t, EDOCSYNC_MODE_CONFLICT, err.Code())
	assert.NotNil(t, err.Unwrap())
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
}

func TestModeFloorViolationErrorGaps(t *testing.T) {
	err := &ModeFloorViolationError{
		Path:          "docs/a.md",
		RequestedMode: schemas.DocModeAuto,
		FloorMode:     schemas.DocModeLocked,
		Message:       "floor message",
	}
	assert.Equal(t, "floor message", err.Error())
	assert.Equal(t, EDOCSYNC_FLOOR, err.Code())
	assert.NotNil(t, err.Unwrap())
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
}
