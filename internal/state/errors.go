package state

import (
	"errors"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Distinguishable state error codes (ported from state/errors.ts): schema
// drift (OD-R03), corruption of an existing state file, missing state for an
// append-style write, and an identifier that is not safely encodable — all
// distinct from EDOCSYNC_BOUNDARY (guard refusals) and from ordinary fs
// errors, which propagate untouched.
const (
	CodeStateSchema  = "EDOCSYNC_STATE_SCHEMA"
	CodeStateCorrupt = "EDOCSYNC_STATE_CORRUPT"
	CodeStateMissing = "EDOCSYNC_STATE_MISSING"
	CodeStateKey     = docutil.CodeStateKey // EDOCSYNC_STATE_KEY
)

// RenderIssues ports renderZodIssues: a one-line summary rendered as
// `path: message; path: message`, with `<root>` standing in for the empty
// path. Callers that need code/message discrimination read the structured
// []schemas.Issue instead; this helper renders summaries only.
func RenderIssues(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		path := strings.Join(issue.Path, ".")
		if path == "" {
			path = "<root>"
		}
		parts = append(parts, path+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

// StateSchemaError is schema drift against the registry (OD-R03): a distinct
// type so callers (and REVIEW routing) can also read the compact issue
// Summary — never the full validation dump. It wraps the coded error so
// docutil.CodeOf still reports EDOCSYNC_STATE_SCHEMA.
type StateSchemaError struct {
	Summary string
	coded   *docutil.Error
}

func (e *StateSchemaError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error, so errors.As / docutil.CodeOf keep working
// through the Summary-carrying wrapper.
func (e *StateSchemaError) Unwrap() error { return e.coded }

// StateSchemaErrorForPath wraps a parse/validation failure for a specific
// state relPath (read or write), mirroring StateSchemaError.forPath.
func StateSchemaErrorForPath(relPath string, cause error) *StateSchemaError {
	summary := cause.Error()
	var verr *schemas.ValidationError
	if errors.As(cause, &verr) {
		summary = RenderIssues(verr.Issues)
	}
	return &StateSchemaError{
		Summary: summary,
		coded: docutil.Newf(CodeStateSchema,
			`schema drift for "%s" — refusing (fail closed, OD-R03): %s`, relPath, summary),
	}
}

// newStateSchemaError builds a drift error with an explicit summary (the TS
// constructor took (message, summary)).
func newStateSchemaError(msg, summary string) *StateSchemaError {
	return &StateSchemaError{Summary: summary, coded: docutil.New(CodeStateSchema, msg)}
}

// StateCorruptError reports an existing state file that is not valid JSON
// (fail closed — never "treat as empty").
func StateCorruptError(format string, args ...any) *docutil.Error {
	return docutil.Newf(CodeStateCorrupt, format, args...)
}

// StateMissingError reports an append/update that targeted state which does
// not exist.
func StateMissingError(format string, args ...any) *docutil.Error {
	return docutil.Newf(CodeStateMissing, format, args...)
}

// StateKeyError reports an identifier (claim id, run key, doc path) that is
// not safely encodable, or a write that would violate its idempotency key.
func StateKeyError(format string, args ...any) *docutil.Error {
	return docutil.StateKeyError(format, args...)
}
