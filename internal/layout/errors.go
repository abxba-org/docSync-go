package layout

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Distinguishable error codes (TS: EDOCSYNC_LAYOUT / EDOCSYNC_LAYOUT_IO).
// The TypeScript classes were not yet listed in docutil/errors.go; the TS
// code strings themselves are preserved here as the observable identity and
// carried through docutil coded errors (CONVENTIONS.md: "pick the closest
// code ... preserve the TS error message text EXACTLY" — the closest listed
// generic would be docutil.CodeConfig, which would drop the observable
// EDOCSYNC_LAYOUT code, so the TS code is kept verbatim).
const (
	// EDOCSYNC_LAYOUT marks an invalid `.docsync/` layout (configuration error).
	EDOCSYNC_LAYOUT = "EDOCSYNC_LAYOUT"
	// EDOCSYNC_LAYOUT_IO marks an fs adapter failure reading `.docsync/`.
	EDOCSYNC_LAYOUT_IO = "EDOCSYNC_LAYOUT_IO"
)

// RenderViolation renders one line-precise diagnostic:
// `<relative-path>: <message> (expected <expected>)`.
func RenderViolation(violation LayoutViolation) string {
	return fmt.Sprintf("%s: %s (expected %s)", violation.Path, violation.Message, violation.Expected)
}

// DocsyncLayoutError is the typed error for an invalid `.docsync/` layout.
// It carries the full violation list (fail-closed reports ALL violations,
// never the first) and renders one line per violation.
type DocsyncLayoutError struct {
	Violations []LayoutViolation
	inner      *docutil.Error
}

// NewDocsyncLayoutError builds the error for the given violations, preserving
// the TS default message `invalid .docsync/ layout — N violation(s)`.
func NewDocsyncLayoutError(violations []LayoutViolation) *DocsyncLayoutError {
	return &DocsyncLayoutError{
		Violations: violations,
		inner:      docutil.New(EDOCSYNC_LAYOUT, fmt.Sprintf("invalid .docsync/ layout — %d violation(s)", len(violations))),
	}
}

// Error renders the coded error (`EDOCSYNC_LAYOUT: <message>`).
func (e *DocsyncLayoutError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_LAYOUT.
func (e *DocsyncLayoutError) Code() string { return EDOCSYNC_LAYOUT }

// Message returns the bare TS error message.
func (e *DocsyncLayoutError) Message() string { return e.inner.Msg }

// Unwrap exposes the underlying docutil coded error (docutil.CodeOf works).
func (e *DocsyncLayoutError) Unwrap() error { return e.inner }

// Render returns one `<relative-path>: <message> (expected <expected>)` line
// per violation, in violation order.
func (e *DocsyncLayoutError) Render() string {
	lines := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		lines[i] = RenderViolation(v)
	}
	return strings.Join(lines, "\n")
}

// LayoutIoError covers fs adapter failures — fail-closed, never "assume
// empty". It wraps the underlying cause.
type LayoutIoError struct {
	inner *docutil.Error
	cause error
}

// NewLayoutIoError builds an I/O error with an optional cause.
func NewLayoutIoError(message string, cause error) *LayoutIoError {
	return &LayoutIoError{inner: docutil.New(EDOCSYNC_LAYOUT_IO, message), cause: cause}
}

// Error renders the coded error (`EDOCSYNC_LAYOUT_IO: <message>`).
func (e *LayoutIoError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_LAYOUT_IO.
func (e *LayoutIoError) Code() string { return EDOCSYNC_LAYOUT_IO }

// Message returns the bare TS error message.
func (e *LayoutIoError) Message() string { return e.inner.Msg }

// Unwrap returns the underlying cause.
func (e *LayoutIoError) Unwrap() error { return e.cause }

// AssertLayoutOk bridges the result object to exception style (e.g. CLI →
// exit code 2): it returns the resolved canonical layout, or the
// *DocsyncLayoutError carrying every violation.
func AssertLayoutOk(result LayoutResult) (CanonicalLayout, error) {
	if result.OK {
		return result.Resolved, nil
	}
	return CanonicalLayout{}, NewDocsyncLayoutError(result.Violations)
}
