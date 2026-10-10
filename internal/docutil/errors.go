// Package docutil holds the shared primitives every other docSync Go package
// builds on: coded errors (mirroring the TypeScript EDocsync* error classes),
// content hashing, canonical JSON (stable CAS bytes), ULID generation, and
// strict JSON decoding helpers.
//
// Error convention: TypeScript threw classes (e.g. ManifestError,
// StateSchemaError). Go returns values; every error that carried a distinct
// class identity gets a Code here plus a preserved message format, so callers
// and tests can assert on codes/messages exactly as the TS suite did.
package docutil

import (
	"errors"
	"fmt"
)

// Error codes. The TS classes each had distinct semantics (config error →
// exit code 2, runtime failure → exit code 3, etc.). Codes keep that
// identity without class hierarchies.
const (
	CodeConfig     = "EDOCSYNC_CONFIG"
	CodeManifest   = "EDOCSYNC_MANIFEST"
	CodeValidation = "EDOCSYNC_VALIDATION"
	CodeState      = "EDOCSYNC_STATE"
	CodeStateKey   = "EDOCSYNC_STATE_KEY"
	CodeStateCAS   = "EDOCSYNC_STATE_CAS"
	CodeSchema     = "EDOCSYNC_SCHEMA"
	CodeGit        = "EDOCSYNC_GIT"
	CodeModel      = "EDOCSYNC_MODEL"
	CodeVerify     = "EDOCSYNC_VERIFY"
	CodeGuard      = "EDOCSYNC_GUARD"
	CodeIO         = "EDOCSYNC_IO"
	CodeInternal   = "EDOCSYNC_INTERNAL"
)

// Error is a coded error carrying an optional structured path (for
// line-precise config diagnostics) and a wrapped cause.
type Error struct {
	Code  string
	Msg   string
	Path  []string // e.g. ["docs", 3] rendered as docs[3]
	Cause error
}

func (e *Error) Error() string {
	if len(e.Path) > 0 {
		return fmt.Sprintf("%s at %s: %s", e.Code, formatPath(e.Path), e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

func (e *Error) Unwrap() error { return e.Cause }

func formatPath(path []string) string {
	out := ""
	for i, p := range path {
		if i > 0 {
			out += "."
		}
		out += p
	}
	return out
}

// New builds a coded error.
func New(code, msg string) *Error { return &Error{Code: code, Msg: msg} }

// Newf builds a coded error with a formatted message.
func Newf(code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// At returns a copy of e with the path set.
func (e *Error) At(path ...string) *Error {
	cp := *e
	cp.Path = append([]string(nil), path...)
	return &cp
}

// Wrap returns a copy of e with cause set.
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.Cause = cause
	return &cp
}

// CodeOf extracts the code from err (or "" if not a coded error).
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Convenience constructors mirroring the TS error classes.
func ConfigError(format string, args ...any) *Error {
	return Newf(CodeConfig, format, args...)
}
func ManifestError(format string, args ...any) *Error {
	return Newf(CodeManifest, format, args...)
}
func ValidationError(format string, args ...any) *Error {
	return Newf(CodeValidation, format, args...)
}
func StateError(format string, args ...any) *Error {
	return Newf(CodeState, format, args...)
}
func StateKeyError(format string, args ...any) *Error {
	return Newf(CodeStateKey, format, args...)
}
func StateCASError(format string, args ...any) *Error {
	return Newf(CodeStateCAS, format, args...)
}
func SchemaError(format string, args ...any) *Error {
	return Newf(CodeSchema, format, args...)
}
func GitError(format string, args ...any) *Error {
	return Newf(CodeGit, format, args...)
}
func ModelError(format string, args ...any) *Error {
	return Newf(CodeModel, format, args...)
}
func VerifyError(format string, args ...any) *Error {
	return Newf(CodeVerify, format, args...)
}
func GuardError(format string, args ...any) *Error {
	return Newf(CodeGuard, format, args...)
}
func IOError(format string, args ...any) *Error {
	return Newf(CodeIO, format, args...)
}
func InternalError(format string, args ...any) *Error {
	return Newf(CodeInternal, format, args...)
}
