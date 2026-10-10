package modes

import (
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// EDOCSYNC_FLOOR is the distinguishable error code for an effective mode that
// exceeds an org floor (V-P0-6). CONVENTIONS.md §Errors forbids inventing a new
// code, so the TS class code `EDOCSYNC_FLOOR` maps onto docutil's validation
// code (docutil.CodeValidation == "EDOCSYNC_VALIDATION"): a floor violation is
// a VALIDATION ERROR that upstream stages route to exit-code-2 semantics.
const EDOCSYNC_FLOOR = docutil.CodeValidation

// EDOCSYNC_MODE_CONFLICT is the distinguishable error code for contradictory or
// malformed mode/floor inputs. It maps onto docutil's config code
// (docutil.CodeConfig == "EDOCSYNC_CONFIG"): malformed mode/floor inputs are
// configuration errors that fail closed (D8).
const EDOCSYNC_MODE_CONFLICT = docutil.CodeConfig

// ModeFloorViolation is the (doc, floor) pair behind a
// ModeFloorViolationError (TS ModeFloorViolation).
type ModeFloorViolation struct {
	// Path is the document path whose requested mode is being checked.
	Path string
	// RequestedMode is the effective mode the document would resolve to.
	RequestedMode schemas.DocMode
	// FloorMode is the floor's maximum permissive mode that was exceeded.
	FloorMode schemas.DocMode
}

// ModeFloorViolationError is raised when an effective mode exceeds an org floor
// (V-P0-6). Callers must NOT clamp to the floor and continue: the configuration
// is invalid until a human fixes its source (manifest.yml / policy), because
// floors dominate every other layer (ARCH §11.3) and only a human PR can change
// them. It is a returned value in Go, never a panic.
type ModeFloorViolationError struct {
	// Path is the document path whose requested mode is being checked.
	Path string
	// RequestedMode is the effective mode the document would resolve to.
	RequestedMode schemas.DocMode
	// FloorMode is the floor's maximum permissive mode that was exceeded.
	FloorMode schemas.DocMode
	// Message preserves the TS message text exactly.
	Message string
}

func (e *ModeFloorViolationError) Error() string { return e.Message }

// Code returns the coded-error code for a floor violation
// (docutil.CodeValidation).
func (e *ModeFloorViolationError) Code() string { return EDOCSYNC_FLOOR }

// Unwrap exposes the docutil coded error so docutil.CodeOf/errors.As work
// uniformly across every docSync error.
func (e *ModeFloorViolationError) Unwrap() error {
	return docutil.ValidationError("%s", e.Message)
}

// newModeFloorViolationError builds the error for one violated (doc, floor)
// pair, defaulting the message to the TS template when none is supplied.
func newModeFloorViolationError(violation ModeFloorViolation, message string) *ModeFloorViolationError {
	if message == "" {
		message = `org floor violation: "` + violation.Path + `" requests mode "` +
			string(violation.RequestedMode) + `" which exceeds floor maximum "` +
			string(violation.FloorMode) + `"`
	}
	return &ModeFloorViolationError{
		Path:          violation.Path,
		RequestedMode: violation.RequestedMode,
		FloorMode:     violation.FloorMode,
		Message:       message,
	}
}

// ModeConflictError is raised for contradictory or malformed mode/floor inputs
// (fail-closed, D8). It is a returned value in Go, never a panic.
type ModeConflictError struct {
	// Message preserves the TS message text exactly.
	Message string
}

func (e *ModeConflictError) Error() string { return e.Message }

// Code returns the coded-error code for a malformed input
// (docutil.CodeConfig).
func (e *ModeConflictError) Code() string { return EDOCSYNC_MODE_CONFLICT }

// Unwrap exposes the docutil coded error so docutil.CodeOf/errors.As work
// uniformly across every docSync error.
func (e *ModeConflictError) Unwrap() error {
	return docutil.ConfigError("%s", e.Message)
}
