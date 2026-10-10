package globs_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuardGlob(t *testing.T) {
	// TS escape-guard table (globs.test.ts), with exact messages.
	for _, c := range []struct {
		pattern string
		ok      bool
		kind    globs.GlobGuardKind
		message string
	}{
		{"src/**", true, "", ""},
		{"docs/readme/*.md", true, "", ""},
		{".github/other/**", true, "", ""},
		{"docs/.github-notes/x", true, "", ""},
		{"src/payments/refunds/**", true, "", ""},
		{"**/*.ts", true, "", ""},
		{"../escape/**", false, globs.GuardRepoEscape,
			`glob pattern "../escape/**" contains a ".." segment and escapes the repository (hard error per ARCH §11.3)`},
		{"src/../../etc/**", false, globs.GuardRepoEscape,
			`glob pattern "src/../../etc/**" contains a ".." segment and escapes the repository (hard error per ARCH §11.3)`},
		{"/abs/**", false, globs.GuardAbsoluteOrWindows,
			`glob pattern "/abs/**" is absolute (or a Windows drive path) — globs must be relative to the repository root`},
		{"//unc/share/**", false, globs.GuardAbsoluteOrWindows,
			`glob pattern "//unc/share/**" is absolute (or a Windows drive path) — globs must be relative to the repository root`},
		{"C:/repo/**", false, globs.GuardAbsoluteOrWindows,
			`glob pattern "C:/repo/**" is absolute (or a Windows drive path) — globs must be relative to the repository root`},
		{`src\x/**`, false, globs.GuardAbsoluteOrWindows,
			`glob pattern "src\x/**" uses a backslash — docSync globs are POSIX-only; use "/" separators`},
		{".docsync/state/**", false, globs.GuardDocsyncInternal,
			`glob pattern ".docsync/state/**" can match .docsync/ internals — the manifest must never target docSync's own configuration or state (hard error per ARCH §11.3)`},
		{"**/.docsync/**", false, globs.GuardDocsyncInternal,
			`glob pattern "**/.docsync/**" can match .docsync/ internals — the manifest must never target docSync's own configuration or state (hard error per ARCH §11.3)`},
		{"docs/.docsync-backup/**", false, globs.GuardDocsyncInternal,
			`glob pattern "docs/.docsync-backup/**" can match .docsync/ internals — the manifest must never target docSync's own configuration or state (hard error per ARCH §11.3)`},
		{".github/workflows/ci.yml", false, globs.GuardWorkflows,
			`glob pattern ".github/workflows/ci.yml" can match .github/workflows/ files — CI workflow definitions are never documentation targets or sources (hard error per ARCH §11.3)`},
		{"**/.github/workflows/**", false, globs.GuardWorkflows,
			`glob pattern "**/.github/workflows/**" can match .github/workflows/ files — CI workflow definitions are never documentation targets or sources (hard error per ARCH §11.3)`},
		{".github/**", false, globs.GlobGuardKind("workflows"),
			`glob pattern ".github/**" can match .github/workflows/ files — CI workflow definitions are never documentation targets or sources (hard error per ARCH §11.3)`},
		{".github/**/ci.yml", false, globs.GuardWorkflows,
			`glob pattern ".github/**/ci.yml" can match .github/workflows/ files — CI workflow definitions are never documentation targets or sources (hard error per ARCH §11.3)`},
		{"", false, globs.GuardEmpty,
			`glob pattern is empty — write a relative POSIX glob such as "src/**"`},
	} {
		result := globs.GuardGlob(c.pattern)
		if c.ok {
			assert.Equalf(t, globs.GlobGuardResult{OK: true}, result, "GuardGlob(%q)", c.pattern)
			continue
		}
		assert.Falsef(t, result.OK, "GuardGlob(%q) must be rejected", c.pattern)
		assert.Equalf(t, c.kind, result.Kind, "GuardGlob(%q) kind", c.pattern)
		assert.Equalf(t, c.message, result.Message, "GuardGlob(%q) message", c.pattern)
	}
}

func TestGuardGlob_CheckOrderFirstFailureWins(t *testing.T) {
	// empty → absolute/Windows → repo escape → .docsync internals →
	// .github/workflows.
	// backslash beats a ".." segment.
	result := globs.GuardGlob(`..\.docsync/**`)
	assert.Equal(t, globs.GuardAbsoluteOrWindows, result.Kind)
	// ".." segment beats .docsync internals.
	result = globs.GuardGlob("../.docsync/**")
	assert.Equal(t, globs.GuardRepoEscape, result.Kind)
	// .docsync internals beats the workflows pair.
	result = globs.GuardGlob(".docsync/github/workflows/**")
	assert.Equal(t, globs.GuardDocsyncInternal, result.Kind)
	// a Windows drive path is caught even with a ".." segment.
	result = globs.GuardGlob("C:/../x")
	assert.Equal(t, globs.GuardAbsoluteOrWindows, result.Kind)
	// a drive-like prefix is only a path at the very start.
	assert.True(t, globs.GuardGlob("docs/C:/x").OK)
}

func TestGuardGlob_ConservativeSubstringRules(t *testing.T) {
	// Any segment containing ".docsync" (even with wildcards/suffixes).
	for _, pattern := range []string{".docsync*", "**/.docsync*/**", "docs/.docsync-backup/x", ".docsync", "a/.docsync/b"} {
		result := globs.GuardGlob(pattern)
		assert.Equalf(t, globs.GuardDocsyncInternal, result.Kind, "GuardGlob(%q)", pattern)
	}
	// Any adjacent <…github…>/<…workflows…> pair, plus ".github" followed by
	// a globstar segment.
	for _, pattern := range []string{
		"x.github/workflows/**", ".github/workflows", "my.github/**",
		".github/**", ".github/**/ci.yml",
	} {
		result := globs.GuardGlob(pattern)
		assert.Equalf(t, globs.GuardWorkflows, result.Kind, "GuardGlob(%q)", pattern)
	}
	// Documented residual holes are ACCEPTED (the fail-closed manifest
	// validator owns the final taxonomy): an unanchored leading globstar, and
	// a <star> between .github and the globstar.
	assert.True(t, globs.GuardGlob("**/state/**").OK)
	assert.True(t, globs.GuardGlob(".github/*/**").OK)
	assert.True(t, globs.GuardGlob("docs/.github-notes/x").OK)
}

func TestGlobGuardError_CodeKindPatternAndUnwrap(t *testing.T) {
	guard := globs.GuardGlob(".docsync/state/**")
	err := globs.NewGlobGuardError(".docsync/state/**", guard)
	require.Error(t, err)
	assert.Equal(t, "EDOCSYNC_GLOB", err.Code())
	assert.Equal(t, globs.GuardDocsyncInternal, err.Kind)
	assert.Equal(t, ".docsync/state/**", err.Pattern)
	assert.Equal(t,
		`EDOCSYNC_GLOB: glob pattern ".docsync/state/**" rejected (docsync_internal): glob pattern ".docsync/state/**" can match .docsync/ internals — the manifest must never target docSync's own configuration or state (hard error per ARCH §11.3)`,
		err.Error())

	// CompileGlob returns the same typed error (fail closed, D8).
	_, cerr := globs.CompileGlob(".docsync/state/**")
	var guardErr *globs.GlobGuardError
	require.True(t, errors.As(cerr, &guardErr))
	assert.Equal(t, globs.GuardDocsyncInternal, guardErr.Kind)

	// Safe patterns compile without error.
	compiled, cerr := globs.CompileGlob("src/**")
	require.NoError(t, cerr)
	assert.Equal(t, "src/**", compiled.Pattern)
}

func TestGuardGlob_NeverThrows(t *testing.T) {
	// GuardGlob is pure and total: any input yields a typed result.
	for _, pattern := range []string{"", "/", "\\", "{", "}", "..", "!", "[", "*", "**", "\x00", strings.Repeat("a", 4096)} {
		result := globs.GuardGlob(pattern) // must not panic
		_ = result
	}
}
