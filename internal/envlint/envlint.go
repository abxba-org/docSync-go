package envlint

import (
	"os"
	"sort"
	"strings"
)

// PolicyEnvPrefixes are the namespaces that are policy-carrying by definition —
// always a violation (ARCH §11.3).
var PolicyEnvPrefixes = []string{
	"DOCSYNC_POLICY_",
	"DOCSYNC_MODE_",
	"DOCSYNC_FLOOR_",
	"DOCSYNC_MANIFEST_",
}

// AllowedEnvVars is the complete allowlist: credentials, endpoints, and ops
// switches only (D9/invariant 8). Nothing here can change a mode, floor, or
// manifest.
var AllowedEnvVars = []string{
	"OPENROUTER_API_KEY",
	"DOCSYNC_OPENROUTER_BASE_URL",
	"DOCSYNC_LOG_LEVEL",
	"NO_COLOR",
	"CI",
}

// EnvPolicyMessage is the exact message carried by every violation.
const EnvPolicyMessage = "policy must be reviewable in Git (ARCH §11.3); environment variables carry credentials and endpoints only"

// EnvPolicyViolation is one policy-via-env violation.
type EnvPolicyViolation struct {
	Variable string `json:"variable"`
	Value    string `json:"value"`
	Message  string `json:"message"`
}

// EnvLintResult is the pure lint result (violations, sorted by variable name).
type EnvLintResult struct {
	Violations []EnvPolicyViolation `json:"violations"`
}

// allowedSet is the membership set over AllowedEnvVars.
var allowedSet = func() map[string]bool {
	m := make(map[string]bool, len(AllowedEnvVars))
	for _, v := range AllowedEnvVars {
		m[v] = true
	}
	return m
}()

// isPolicyEnvPrefix reports whether variable is in a policy namespace.
func isPolicyEnvPrefix(variable string) bool {
	for _, prefix := range PolicyEnvPrefixes {
		if strings.HasPrefix(variable, prefix) {
			return true
		}
	}
	return false
}

// LintEnvironment lints an environment snapshot for policy arriving via
// environment variables. Pure: same input → deep-equal output; the process
// environment is never read here. Undefined values (absent variables) are
// naturally ignored (they never enter the map). Variables are linted in sorted
// (variable-name) order for deterministic output.
func LintEnvironment(env map[string]string) EnvLintResult {
	violations := []EnvPolicyViolation{}
	variables := make([]string, 0, len(env))
	for name := range env {
		variables = append(variables, name)
	}
	sort.Strings(variables)
	for _, variable := range variables {
		value := env[variable]
		if isPolicyEnvPrefix(variable) {
			violations = append(violations, EnvPolicyViolation{Variable: variable, Value: value, Message: EnvPolicyMessage})
			continue
		}
		if strings.HasPrefix(variable, "DOCSYNC_") && !allowedSet[variable] {
			violations = append(violations, EnvPolicyViolation{Variable: variable, Value: value, Message: EnvPolicyMessage})
		}
	}
	return EnvLintResult{Violations: violations}
}

// LintProcessEnvironment is the thin adapter that snapshots the calling
// process's environment and lints it. The only place this module touches the
// real environment; the orchestrator calls this, tests call the pure
// LintEnvironment directly.
func LintProcessEnvironment() EnvLintResult {
	env := make(map[string]string)
	for _, kv := range os.Environ() {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			env[kv[:idx]] = kv[idx+1:]
		}
	}
	return LintEnvironment(env)
}
