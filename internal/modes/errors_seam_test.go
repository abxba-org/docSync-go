package modes

// Internal test for the ModeFloorViolationError default message: the TS class
// constructor accepted an optional message and fell back to the canonical
// "org floor violation: ..." template. EnforceFloors always supplies the
// floor-engine message, so the fallback is only reachable through the
// constructor itself (the TS suite exercised it via `new ModeFloorViolationError(...)`).

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
)

func TestModeFloorViolationErrorDefaultMessage(t *testing.T) {
	err := newModeFloorViolationError(ModeFloorViolation{
		Path:          "docs/security/auth.md",
		RequestedMode: schemas.DocModeAuto,
		FloorMode:     schemas.DocModeSuggest,
	}, "")

	assert.Equal(t,
		`org floor violation: "docs/security/auth.md" requests mode "auto" which exceeds floor maximum "suggest"`,
		err.Message)
	assert.Equal(t, "docs/security/auth.md", err.Path)
	assert.Equal(t, schemas.DocModeAuto, err.RequestedMode)
	assert.Equal(t, schemas.DocModeSuggest, err.FloorMode)
	assert.Equal(t, EDOCSYNC_FLOOR, err.Code())
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
}

func TestModeFloorViolationErrorExplicitMessageWins(t *testing.T) {
	err := newModeFloorViolationError(ModeFloorViolation{
		Path:          "docs/a.md",
		RequestedMode: schemas.DocModeAuto,
		FloorMode:     schemas.DocModeLocked,
	}, `"docs/a.md" requests mode "auto" which exceeds org floor maximum "locked" (pattern "docs/**")`)

	assert.Contains(t, err.Message, "org floor maximum")
	assert.Equal(t, err.Message, err.Error())
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
}
