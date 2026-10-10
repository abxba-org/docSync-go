package gitpr_test

// Black-box port of test/authority-surface.test.ts (Phase 9, OD-R50; PLAN
// §10.3) for the PR-side classifier this unit owns.

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/stretchr/testify/require"
)

func TestAuthorityConfigSurface(t *testing.T) {
	testCases := []struct {
		path string
		want bool
	}{
		{".docsync/manifest.yml", true},
		{".docsync/models.yml", true},
		{".docsync/org-policy.yml", true},
		{".docsync/policies/", true},
		{".docsync/policies/slo-review.yml", true},
		{".docsync/policies/nested/deep/review.yml", true},
		// negatives — out-of-allowlist paths that are NOT authority surfaces
		{"src/handlers/payments.ts", false},
		{"manifest.yml", false},
		{".github/workflows/ci.yml", false},
		{".docsync/state/", false},
		{".docsync/state/decisions.jsonl", false},
		{"docs/payments.md", false},
		{"", false},
		// prefix-shaped near-misses that must NOT match
		{"docsync/policies/", false},
		{" .docsync/policies/", false},
		{".docsync/policy/slo.yml", false},
		{".docsync/manifest.yaml", false},
		{".docsync/models.json", false},
		{".docsync/org-policy.yaml", false},
		{".docsync/models.yml.bak", false},
		{"docs/.docsync/policies/review.yml", false},
		{"docsync/org-policy.yml", false},
	}

	for _, tc := range testCases {
		require.Equal(t, tc.want, gitpr.IsAuthorityConfigSurface(tc.path),
			"path %q", tc.path)
	}
}

func TestAuthorityConfigSurfacesAreExportedConstants(t *testing.T) {
	require.Equal(t, ".docsync/manifest.yml", gitpr.AuthorityConfigDoc)
	require.Equal(t, ".docsync/models.yml", gitpr.AuthorityConfigModels)
	require.Equal(t, ".docsync/org-policy.yml", gitpr.AuthorityConfigOrgPolicy)
	require.Equal(t, ".docsync/policies/", gitpr.AuthorityConfigPoliciesPrefix)

	// The classifier is total over the exported set (the emit points rely on it).
	for _, surface := range []string{
		gitpr.AuthorityConfigDoc,
		gitpr.AuthorityConfigModels,
		gitpr.AuthorityConfigOrgPolicy,
		gitpr.AuthorityConfigPoliciesPrefix + "any-policy.yml",
	} {
		require.True(t, gitpr.IsAuthorityConfigSurface(surface), "surface %q", surface)
	}
}
