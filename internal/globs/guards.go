package globs

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Escape guards for manifest globs (TS guards.ts; P0-T10; ARCH §11.3 hard
// errors; D8). A glob that escapes the repository, or can match `.docsync/`
// internals or `.github/workflows/` files, is a CONFIGURATION ERROR — it must
// never compile (`CompileGlob` is the enforcement point; lint and validators
// surface it before any AI stage).
//
// `GuardGlob` itself never throws (the Go form of "never throws"): pure
// functions return typed results, and `CompileGlob` converts a failing result
// into a `GlobGuardError`.
//
// Conservatism (fail closed, D8 — reject more rather than less):
//
//   - Any pattern segment containing the substring `.docsync` is rejected, even
//     when surrounded by wildcards or suffixes (`.docsync*`, or an unanchored
//     `**` before a `.docsync` segment): wildcards before the segment make the
//     match unanchored, so the pattern as written can match a path containing a
//     `.docsync` segment.
//   - Any adjacent segment pair `<…github…>/<…workflows…>` is rejected (this
//     subsumes the explicit `.github/workflows…` prefix), plus a `.github`
//     segment directly followed by a `**` segment — that globstar can span into
//     `workflows/`.
//   - Known residual holes, deliberately accepted so legitimate source globs
//     keep working (the fail-closed manifest validator of the next wave owns
//     the final hard-error taxonomy and may tighten): an unanchored leading `**`
//     pattern such as a `**`-prefixed `state` glob can in principle match a
//     `.docsync`-named segment without containing it literally, and
//     `.github/<star>/**`-style patterns can reach `workflows/` without an
//     adjacent `workflows` pattern segment.

// GlobGuardKind is the failure kind of a rejected glob (TS GlobGuardKind).
type GlobGuardKind string

// The failure kinds, in TS declaration order.
const (
	GuardRepoEscape        GlobGuardKind = "repo_escape"
	GuardDocsyncInternal   GlobGuardKind = "docsync_internal"
	GuardWorkflows         GlobGuardKind = "workflows"
	GuardAbsoluteOrWindows GlobGuardKind = "absolute_or_windows"
	GuardEmpty             GlobGuardKind = "empty"
)

// GlobGuardResult is the outcome of GuardGlob (TS GlobGuardOk | GlobGuardErr).
// OK patterns carry only OK: true; rejected patterns carry OK: false plus the
// failure Kind and the exact TS Message.
type GlobGuardResult struct {
	OK      bool
	Kind    GlobGuardKind
	Message string
}

// GuardGlob guards a glob pattern. Check order (first failure wins): empty →
// absolute/Windows → repository escape (`..` segment) → `.docsync` internals →
// `.github/workflows`. Pure, deterministic, never throws.
func GuardGlob(pattern string) GlobGuardResult {
	if len(pattern) == 0 {
		return GlobGuardResult{
			OK:      false,
			Kind:    GuardEmpty,
			Message: `glob pattern is empty — write a relative POSIX glob such as "src/**"`,
		}
	}
	if strings.Contains(pattern, `\`) {
		return GlobGuardResult{
			OK:      false,
			Kind:    GuardAbsoluteOrWindows,
			Message: fmt.Sprintf(`glob pattern "%s" uses a backslash — docSync globs are POSIX-only; use "/" separators`, pattern),
		}
	}
	if strings.HasPrefix(pattern, "/") || isWindowsDrivePath(pattern) {
		return GlobGuardResult{
			OK:      false,
			Kind:    GuardAbsoluteOrWindows,
			Message: fmt.Sprintf(`glob pattern "%s" is absolute (or a Windows drive path) — globs must be relative to the repository root`, pattern),
		}
	}
	segments := strings.Split(pattern, "/")
	for _, segment := range segments {
		if segment == ".." {
			return GlobGuardResult{
				OK:      false,
				Kind:    GuardRepoEscape,
				Message: fmt.Sprintf(`glob pattern "%s" contains a ".." segment and escapes the repository (hard error per ARCH §11.3)`, pattern),
			}
		}
	}
	for _, segment := range segments {
		if strings.Contains(segment, ".docsync") {
			return GlobGuardResult{
				OK:      false,
				Kind:    GuardDocsyncInternal,
				Message: fmt.Sprintf(`glob pattern "%s" can match .docsync/ internals — the manifest must never target docSync's own configuration or state (hard error per ARCH §11.3)`, pattern),
			}
		}
	}
	for i := 0; i+1 < len(segments); i++ {
		current, next := segments[i], segments[i+1]
		if strings.Contains(current, ".github") &&
			(strings.Contains(next, "workflows") || strings.Contains(next, "**")) {
			return GlobGuardResult{
				OK:      false,
				Kind:    GuardWorkflows,
				Message: fmt.Sprintf(`glob pattern "%s" can match .github/workflows/ files — CI workflow definitions are never documentation targets or sources (hard error per ARCH §11.3)`, pattern),
			}
		}
	}
	return GlobGuardResult{OK: true}
}

// isWindowsDrivePath reports whether pattern starts with an ASCII letter
// followed by ":" (TS /^[A-Za-z]:/).
func isWindowsDrivePath(pattern string) bool {
	if len(pattern) < 2 || pattern[1] != ':' {
		return false
	}
	c := pattern[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// CodeGlob is the distinguishable error code for rejected glob patterns
// (TS EDOCSYNC_GLOB). The TS class code was not listed in docutil/errors.go;
// following the layout unit's precedent, the TS code string is preserved
// verbatim as the observable identity and carried through a docutil coded
// error (the closest listed generic, docutil.CodeGuard, would drop the
// observable EDOCSYNC_GLOB code).
const CodeGlob = "EDOCSYNC_GLOB"

// GlobGuardError is the typed hard error produced by CompileGlob (and
// MatchPaths) when a pattern fails its escape guard.
// Configuration-error semantics (exit code 2 later): the run must not proceed
// with an unsafe glob.
type GlobGuardError struct {
	// Kind is the guard failure kind (TS GlobGuardError.kind).
	Kind GlobGuardKind
	// Pattern is the rejected pattern (TS GlobGuardError.pattern).
	Pattern string
	// GuardMessage is the guard's own message (guard.message).
	GuardMessage string

	inner *docutil.Error
}

// NewGlobGuardError builds the error, preserving the TS message exactly:
// `glob pattern "<pattern>" rejected (<kind>): <guard message>`.
func NewGlobGuardError(pattern string, guard GlobGuardResult) *GlobGuardError {
	msg := fmt.Sprintf(`glob pattern "%s" rejected (%s): %s`, pattern, guard.Kind, guard.Message)
	return &GlobGuardError{
		Kind:         guard.Kind,
		Pattern:      pattern,
		GuardMessage: guard.Message,
		inner:        docutil.New(CodeGlob, msg),
	}
}

// Error renders the coded error (`EDOCSYNC_GLOB: <message>`).
func (e *GlobGuardError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_GLOB.
func (e *GlobGuardError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *GlobGuardError) Unwrap() error { return e.inner }
