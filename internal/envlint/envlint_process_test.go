package envlint_test

// Ports the remaining assertions of packages/core/test/envlint.test.ts
// (P0-T16): the thin `lintProcessEnvironment` adapter — the only place the
// module touches the real environment (the pure LintEnvironment is fully
// covered by the main suite) — plus the TS E_ENV_POLICY end-to-end's env
// snapshot determinism.
//
// The TS `E_ENV_POLICY end-to-end in validateRepositoryConfig` half of the file
// drives the lint through the config validator with an `env` option; the Go
// ValidateOptions carries no env input (the Go port applies the env lint via
// this adapter at the caller), so the adapter is pinned here directly: it
// snapshots process env, every violation carries the exact message, and the
// allowlist still passes through.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/envlint"
)

// findViolation returns the violation for one variable (or false).
func findViolation(violations []envlint.EnvPolicyViolation, variable string) (envlint.EnvPolicyViolation, bool) {
	for _, v := range violations {
		if v.Variable == variable {
			return v, true
		}
	}
	return envlint.EnvPolicyViolation{}, false
}

func TestLintProcessEnvironment_SnapshotsTheRealEnvironment(t *testing.T) {
	// A policy namespace arriving in the real process env is flagged with the
	// exact message and the process value (t.Setenv restores afterwards).
	t.Setenv("DOCSYNC_POLICY_FROM_PROCESS", "auto")

	result := envlint.LintProcessEnvironment()
	violation, found := findViolation(result.Violations, "DOCSYNC_POLICY_FROM_PROCESS")
	require.True(t, found, "process-env policy violation must surface through the adapter")
	assert.Equal(t, "auto", violation.Value)
	assert.Equal(t, envlint.EnvPolicyMessage, violation.Message)

	// The output stays deterministically sorted by variable name.
	names := codesOf(result.Violations)
	for i := 1; i < len(names); i++ {
		assert.Less(t, names[i-1], names[i], "violations must be sorted by variable name")
	}
}

func TestLintProcessEnvironment_AllowsCredentialsAndEndpointsOnly(t *testing.T) {
	// The allowlisted credential/ops vars pass through the adapter untouched.
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	t.Setenv("DOCSYNC_LOG_LEVEL", "debug")
	t.Setenv("DOCSYNC_OPENROUTER_BASE_URL", "https://openrouter.test")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CI", "true")

	result := envlint.LintProcessEnvironment()
	for _, allowed := range []string{
		"OPENROUTER_API_KEY",
		"DOCSYNC_LOG_LEVEL",
		"DOCSYNC_OPENROUTER_BASE_URL",
		"NO_COLOR",
		"CI",
	} {
		_, found := findViolation(result.Violations, allowed)
		assert.False(t, found, "%s is allowlisted and must never be a violation", allowed)
	}
}

func TestLintProcessEnvironment_FailsClosedOnUnknownDocsyncNamespace(t *testing.T) {
	// Unknown docSync namespaces arriving via the real environment fail closed.
	t.Setenv("DOCSYNC_SOMETHING_FROM_PROCESS", "v")

	result := envlint.LintProcessEnvironment()
	violation, found := findViolation(result.Violations, "DOCSYNC_SOMETHING_FROM_PROCESS")
	require.True(t, found)
	assert.Equal(t, "v", violation.Value)
	assert.Equal(t,
		"policy must be reviewable in Git (ARCH §11.3); environment variables carry credentials and endpoints only",
		violation.Message)
}

func TestLintProcessEnvironment_OnlyDocsyncVariablesAreFlagged(t *testing.T) {
	// Variables outside docSync's business are ignored even in the real
	// environment (PATH and friends are always present there).
	result := envlint.LintProcessEnvironment()
	for _, v := range result.Violations {
		assert.True(t, strings.HasPrefix(v.Variable, "DOCSYNC_"),
			"only DOCSYNC_* variables may ever be flagged, got %+v", v)
	}
	_, found := findViolation(result.Violations, "PATH")
	assert.False(t, found, "PATH is not docSync's business")
	_, found = findViolation(result.Violations, "HOME")
	assert.False(t, found, "HOME is not docSync's business")
}
