// Ported from packages/github-app/src/permissions.ts (P8-T04): the GitHub App
// permission table + the signup asserts that keep the app safe to install.
//
// Signup asserts (MANIFEST §5, LEVERS.md/WGL §5, D16):
//   - assertNoForbiddenPermission — reject a grant outside the exact allowlist
//     (fail closed on anything we don't use).
//   - assertRepoScopedInstallation — only repository_selection=selected passes.
//
// AND app-manifest.ts (P8-T04): the app manifest builder + the webhook events.
// The audited webhook surface is EXACTLY the three events the receiver routes
// (pull_request, check_run, push) — never a write hook (POLICY §4, D16).
//
// Contract note (docs/ARCHITECTURE.md §5.3 Contracts): TS used Object.freeze
// for the immutable tables; Go has no frozen map, so the canonical tables are
// UNEXPORTED and every accessor returns a COPY — a caller can never mutate the
// app's security posture through the export.

package githubapp

// permissionReadWrite is the only grant this app uses for its three scopes.
const permissionReadWrite = "read&write"

// appPermissions is the canonical, immutable-seeming grant table (TS
// APP_PERMISSIONS). Unexported; AppPermissions returns a copy.
var appPermissions = map[string]string{
	"contents":      permissionReadWrite,
	"pull_requests": permissionReadWrite,
	"checks":        permissionReadWrite,
}

// appNotGranted is the full forbidden list (TS APP_NOT_GRANTED_PERMISSIONS).
var appNotGranted = []string{
	"administration",
	"deployments",
	"issues",
	"members",
	"secrets",
	"webhooks",
	"workflows",
}

// AppPermissions returns a COPY of the grant table (contents/pull_requests/
// checks, each read&write).
func AppPermissions() map[string]string {
	out := make(map[string]string, len(appPermissions))
	for scope, permission := range appPermissions {
		out[scope] = permission
	}
	return out
}

// AppNotGrantedPermissions returns a COPY of the forbidden-permission list.
func AppNotGrantedPermissions() []string {
	return append([]string(nil), appNotGranted...)
}

// appPermissionScopes is the small allowlist of scope keys the app may grant.
var appPermissionScopes = map[string]bool{"contents": true, "pull_requests": true, "checks": true}

// ForbiddenPermissionError is the signup assert refusal for a forbidden or
// unknown permission grant (TS ForbiddenPermissionError).
type ForbiddenPermissionError struct {
	*githubAppError
	Permission string
}

// RepoScopeInstallationError is the signup assert refusal for a non
// repo-selected installation (TS RepoScopeInstallationError).
type RepoScopeInstallationError struct {
	*githubAppError
	Selection string
}

// AssertNoForbiddenPermission throws for any grant outside the exact
// allowlist {contents: read&write, pull_requests: read&write, checks:
// read&write}. Cardinality + each value must match exactly (a singular request
// permission rather than the app's permission is refused — signup asserts run
// BOTH against the requested manifest and the granted permissions).
func AssertNoForbiddenPermission(permissions map[string]string) error {
	if len(permissions) != len(appPermissions) {
		return &ForbiddenPermissionError{
			githubAppError: newGithubAppError(
				"app permissions must be the exact allowlist {contents, pull_requests, checks} each read&write — " +
					"a cardinality mismatch is a forbidden grant (D16)"),
			Permission: "",
		}
	}
	for scope, permission := range permissions {
		expected := appPermissions[scope]
		if !appPermissionScopes[scope] || permission != expected {
			return &ForbiddenPermissionError{
				githubAppError: newGithubAppError(
					"app permissions must be the exact allowlist {contents, pull_requests, checks} each read&write — " +
						"'" + scope + ": " + permission + "' is forbidden (D16)"),
				Permission: scope,
			}
		}
	}
	return nil
}

// AssertRepoScopedInstallation throws unless repository_selection ===
// 'selected' (repo-scoped tokens only — never all-repos).
func AssertRepoScopedInstallation(config map[string]any) error {
	selection, _ := config["repository_selection"].(string)
	if selection != "selected" {
		return &RepoScopeInstallationError{
			githubAppError: newGithubAppError(
				"app installation must be repository-selected (repository-selection=selected) — " +
					"refuse all-repos installs (D16)"),
			Selection: selection,
		}
	}
	return nil
}

// appWebhookEvents is the audited webhook surface (TS APP_WEBHOOK_EVENTS):
// EXACTLY pull_request, check_run, push — the three the receiver routes.
var appWebhookEvents = []string{"pull_request", "check_run", "push"}

// AppWebhookEvents returns a COPY of the audited webhook events.
func AppWebhookEvents() []string { return append([]string(nil), appWebhookEvents...) }

// AppManifestInput is the configurable GitHub App Manifest input (TS
// AppManifestInput).
type AppManifestInput struct {
	Name       string
	URL        string
	WebhookURL string
}

// AppManifest is the resulting GitHub App Manifest (TS AppManifest).
type AppManifest struct {
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	WebhookURL  string            `json:"webhook_url"`
	Permissions map[string]string `json:"permissions"`
	Events      []string          `json:"events"`
}

// BuildAppManifest builds the GitHub App Manifest from the configurable
// server-side surfaces plus the FIXED permission table + audited webhook events
// (TS buildAppManifest).
func BuildAppManifest(input AppManifestInput) AppManifest {
	return AppManifest{
		Name:        input.Name,
		URL:         input.URL,
		WebhookURL:  input.WebhookURL,
		Permissions: AppPermissions(),
		Events:      AppWebhookEvents(),
	}
}
