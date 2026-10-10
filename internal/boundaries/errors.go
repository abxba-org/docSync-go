package boundaries

import "github.com/DUDE616/docSync-go/internal/docutil"

// EDOCSYNC_BOUNDARY is the distinguishable error code for refused docSync write
// paths. CONVENTIONS.md §Errors forbids inventing a new code, so the TS class
// code `EDOCSYNC_BOUNDARY` maps onto docutil's guard code
// (docutil.CodeGuard == "EDOCSYNC_GUARD"): a guard refusal is exactly a
// write-boundary guard failure. Callers read the code through
// docutil.CodeOf or GuardedPathError.Code.
const EDOCSYNC_BOUNDARY = docutil.CodeGuard

// GuardedPathError is the Go port of the TS `GuardedPathError` class: a refused
// docSync write path carrying the offending path and the preserved refusal
// message. Unlike TS it is a returned value, never a panic.
type GuardedPathError struct {
	// RelPath is the path the caller attempted to write.
	RelPath string
	// Message preserves the TS message text exactly.
	Message string
}

func (e *GuardedPathError) Error() string { return e.Message }

// Code returns the coded-error code for this refusal (docutil.CodeGuard).
func (e *GuardedPathError) Code() string { return EDOCSYNC_BOUNDARY }

// Unwrap exposes the docutil coded error so docutil.CodeOf/errors.As work
// uniformly across every docSync error.
func (e *GuardedPathError) Unwrap() error { return docutil.GuardError("%s", e.Message) }

// AssertStateWrite is the exception-style variant of GuardStateWrite for
// consumers that prefer errors over decisions (TS static
// GuardedPathError.assertStateWrite).
func AssertStateWrite(relPath string) error {
	decision := GuardStateWrite(relPath)
	if decision.Allowed {
		return nil
	}
	return &GuardedPathError{
		RelPath: relPath,
		Message: `state write refused for "` + relPath + `": ` + decision.Reason,
	}
}

// AssertConfigWrite is the exception-style variant of GuardConfigWrite for
// consumers that prefer errors over decisions (TS static
// GuardedPathError.assertConfigWrite).
func AssertConfigWrite(relPath string) error {
	decision := GuardConfigWrite(relPath)
	if decision.Allowed {
		return nil
	}
	return &GuardedPathError{
		RelPath: relPath,
		Message: `config write refused for "` + relPath + `": ` + decision.Reason,
	}
}
