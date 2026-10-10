package gitpr

// Ported from core/src/gitpr/authority-surface.ts (Phase 9, OD-R50; PLAN
// §10.3): authority-config surface classification.
//
// A refused path that targets the repo's authority-config surfaces is not
// merely out-of-allowlist — it is model output attempting to influence the
// system's permissions, file selection, or credentials (incident class
// `model_influence_on_authority`). Pure classifier; refusal seams call it when
// emitting the incident for a refused path, so the closed taxonomy
// distinguishes authority influence from ordinary allowlist violations.
//
// P12-T13 adds the org policy file (`.docsync/org-policy.yml`) to the surface:
// it declares the org's floors and the `reviewed` flags that stop their
// advisories, so a model-proposed edit to it would rewrite the very floors
// that bind every repository.

// AuthorityConfigDoc is the manifest (the docSync authority-config root).
const AuthorityConfigDoc = ".docsync/manifest.yml"

// AuthorityConfigModels is the models.yml role bindings.
const AuthorityConfigModels = ".docsync/models.yml"

// AuthorityConfigOrgPolicy is the org policy file (P12-T13): the org's floors
// and their `reviewed` declarations.
const AuthorityConfigOrgPolicy = ".docsync/org-policy.yml"

// AuthorityConfigPoliciesPrefix is the promotion-pipeline policy store prefix.
const AuthorityConfigPoliciesPrefix = ".docsync/policies/"

// IsAuthorityConfigSurface reports whether path targets an authority-config
// surface: the manifest, the models.yml role bindings, the org policy file, or
// the promotion-pipeline policy store.
func IsAuthorityConfigSurface(path string) bool {
	return path == AuthorityConfigDoc ||
		path == AuthorityConfigModels ||
		path == AuthorityConfigOrgPolicy ||
		hasPrefix(path, AuthorityConfigPoliciesPrefix)
}

// hasPrefix is strings.HasPrefix (kept local so the port reads like the TS
// `path.startsWith(...)` without a strings import in the pure classifier).
func hasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}
