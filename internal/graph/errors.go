package graph

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// The three distinguishable error codes (TS error code constants of the same
// name). Following the globs unit's precedent, the TS class code is preserved
// verbatim as the OBSERVABLE identity and carried through a docutil coded
// error: consumers and tests assert on these strings exactly as the TS suite
// did (docutil.CodeOf errors.As work uniformly across every docSync error).
const (
	CodeGraphUnusable    = "EDOCSYNC_GRAPH_UNUSABLE"
	CodeGraphEdgeInvalid = "EDOCSYNC_GRAPH_EDGE_INVALID"
	CodeGraphParse       = "EDOCSYNC_GRAPH_PARSE"
)

// GraphUnusableError is returned when a graph with configuration errors is
// refused (fail closed, D8): every consumer that routes on an unusable graph
// returns THE SAME typed error. It is a returned value in Go, never a panic.
type GraphUnusableError struct {
	// Errors is the graph's configuration-error list.
	Errors []GraphConfigError
	inner  *docutil.Error
}

// NewGraphUnusableError builds the error, preserving the TS message exactly:
// `document graph has <n> configuration error(s) — refusing to route (fail closed)`.
func NewGraphUnusableError(errors []GraphConfigError) *GraphUnusableError {
	msg := fmt.Sprintf("document graph has %d configuration error(s) — refusing to route (fail closed)", len(errors))
	return &GraphUnusableError{Errors: errors, inner: docutil.New(CodeGraphUnusable, msg)}
}

func (e *GraphUnusableError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_GRAPH_UNUSABLE.
func (e *GraphUnusableError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *GraphUnusableError) Unwrap() error { return e.inner }

// GraphEdgeInvalidReason is why an edge was refused (TS: 'unknown_kind' | 'shape').
type GraphEdgeInvalidReason string

// The refusal reasons (TS GraphEdgeInvalidError.reason values).
const (
	GraphEdgeReasonUnknownKind GraphEdgeInvalidReason = "unknown_kind"
	GraphEdgeReasonShape       GraphEdgeInvalidReason = "shape"
)

// GraphEdgeInvalidError is returned when edge DATA has an unknown kind or a
// malformed shape (refused, never coerced — toGraphEdge).
type GraphEdgeInvalidError struct {
	// Reason is why the edge was refused.
	Reason GraphEdgeInvalidReason
	// Kind is the offending kind string, when one was supplied (empty for shape errors).
	Kind  string
	inner *docutil.Error
}

// NewGraphEdgeInvalidError builds the error, appending " (fail closed)" to the
// message exactly as the TS constructor did. Pass an empty kind for shape errors.
func NewGraphEdgeInvalidError(message string, reason GraphEdgeInvalidReason, kind string) *GraphEdgeInvalidError {
	return &GraphEdgeInvalidError{
		Reason: reason,
		Kind:   kind,
		inner:  docutil.New(CodeGraphEdgeInvalid, message+" (fail closed)"),
	}
}

func (e *GraphEdgeInvalidError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_GRAPH_EDGE_INVALID.
func (e *GraphEdgeInvalidError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *GraphEdgeInvalidError) Unwrap() error { return e.inner }

// GraphParseError is returned when a serialized graph document fails its strict
// file-format validation (refused, never partially parsed — parseGraph).
type GraphParseError struct {
	inner *docutil.Error
}

// NewGraphParseError builds the error, appending " (fail closed)" to the message
// exactly as the TS constructor did.
func NewGraphParseError(message string) *GraphParseError {
	return &GraphParseError{inner: docutil.New(CodeGraphParse, message+" (fail closed)")}
}

func (e *GraphParseError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_GRAPH_PARSE.
func (e *GraphParseError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *GraphParseError) Unwrap() error { return e.inner }
