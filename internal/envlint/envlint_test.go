package envlint_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/envlint"
)

func codesOf(violations []envlint.EnvPolicyViolation) []string {
	out := make([]string, 0, len(violations))
	for _, v := range violations {
		out = append(out, v.Variable)
	}
	return out
}

func TestPolicyEnvPrefixesPinned(t *testing.T) {
	require.Equal(t, []string{
		"DOCSYNC_POLICY_",
		"DOCSYNC_MODE_",
		"DOCSYNC_FLOOR_",
		"DOCSYNC_MANIFEST_",
	}, envlint.PolicyEnvPrefixes)
}

func TestAllowedEnvVarsPinned(t *testing.T) {
	require.Equal(t, []string{
		"OPENROUTER_API_KEY",
		"DOCSYNC_OPENROUTER_BASE_URL",
		"DOCSYNC_LOG_LEVEL",
		"NO_COLOR",
		"CI",
	}, envlint.AllowedEnvVars)
}

func TestLintEnvironment_AllowsCredentialsAndEndpoints(t *testing.T) {
	require.Empty(t, envlint.LintEnvironment(map[string]string{"OPENROUTER_API_KEY": "sk-test"}).Violations)
	require.Empty(t, envlint.LintEnvironment(map[string]string{"DOCSYNC_OPENROUTER_BASE_URL": "https://openrouter.test"}).Violations)
	require.Empty(t, envlint.LintEnvironment(map[string]string{"DOCSYNC_LOG_LEVEL": "debug", "NO_COLOR": "1", "CI": "true"}).Violations)
}

func TestLintEnvironment_FlagsPolicyNamespaceWithExactMessage(t *testing.T) {
	result := envlint.LintEnvironment(map[string]string{"DOCSYNC_POLICY_X": "auto"})
	require.Len(t, result.Violations, 1)
	v := result.Violations[0]
	require.Equal(t, "DOCSYNC_POLICY_X", v.Variable)
	require.Equal(t, "auto", v.Value)
	require.Equal(t, envlint.EnvPolicyMessage, v.Message)
	require.Equal(t,
		"policy must be reviewable in Git (ARCH §11.3); environment variables carry credentials and endpoints only",
		v.Message,
	)
}

func TestLintEnvironment_FlagsEveryPolicyNamespace(t *testing.T) {
	result := envlint.LintEnvironment(map[string]string{
		"DOCSYNC_POLICY_STRICT": "1",
		"DOCSYNC_MODE_DEFAULT":  "auto",
		"DOCSYNC_FLOOR_MIN":     "suggest",
		"DOCSYNC_MANIFEST_PATH": "elsewhere.yml",
	})
	require.Equal(t, []string{
		"DOCSYNC_FLOOR_MIN",
		"DOCSYNC_MANIFEST_PATH",
		"DOCSYNC_MODE_DEFAULT",
		"DOCSYNC_POLICY_STRICT",
	}, codesOf(result.Violations))
}

func TestLintEnvironment_FailsClosedOnUnknownDocsyncNamespace(t *testing.T) {
	result := envlint.LintEnvironment(map[string]string{"DOCSYNC_SOMETHING_ELSE": "v"})
	require.Equal(t, []string{"DOCSYNC_SOMETHING_ELSE"}, codesOf(result.Violations))
}

func TestLintEnvironment_IgnoresUnrelatedVariables(t *testing.T) {
	require.Empty(t, envlint.LintEnvironment(map[string]string{
		"PATH": "/usr/bin", "HOME": "/home/x", "npm_config_registry": "y",
	}).Violations)
}

func TestLintEnvironment_IgnoresAbsentVariables(t *testing.T) {
	// TS "ignores undefined values (absent variables)": in Go absent variables
	// simply never enter the map, so an env carrying only unrelated vars is clean.
	require.Empty(t, envlint.LintEnvironment(map[string]string{}).Violations)
	require.Empty(t, envlint.LintEnvironment(map[string]string{"PATH": "/usr/bin"}).Violations)
}

func TestLintEnvironment_DoesNotReadProcessEnv(t *testing.T) {
	// A fake env fully determines the result (no process.env is read here).
	require.Empty(t, envlint.LintEnvironment(map[string]string{"FAKE_ONLY": "1"}).Violations)
	require.Equal(t, []string{"DOCSYNC_POLICY_FAKE"},
		codesOf(envlint.LintEnvironment(map[string]string{"FAKE_ONLY": "1", "DOCSYNC_POLICY_FAKE": "1"}).Violations))
}

func TestLintEnvironment_SortsViolationsByVariableName(t *testing.T) {
	result := envlint.LintEnvironment(map[string]string{"DOCSYNC_POLICY_B": "2", "DOCSYNC_POLICY_A": "1"})
	require.Equal(t, []string{"DOCSYNC_POLICY_A", "DOCSYNC_POLICY_B"}, codesOf(result.Violations))
}
