package gitpr_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/{org-policy-tsurface-p12-t13,machine-mode-transition-p12-t14,gitpr-flow}.test.ts
// — the three machine-PR content guard (P6-T27) unit views. Pure, network-free.

// cgTextPtr is the optional modeAfter constructor.
func cgTextPtr(v string) *string { return &v }

func TestGuardMachinePrContent_defaultProtectedSet(t *testing.T) {
	assert.Contains(t, gitpr.DefaultProtectedPolicyPaths, "manifest.yml")
	assert.Contains(t, gitpr.DefaultProtectedPolicyPaths, ".docsync/manifest.yml")
	assert.Contains(t, gitpr.DefaultProtectedPolicyPaths, ".docsync/policy*")
	// P12-T13: the org floors are human-owned configuration too.
	assert.Contains(t, gitpr.DefaultProtectedPolicyPaths, ".docsync/org-policy.yml")
	assert.Len(t, gitpr.DefaultProtectedPolicyPaths, 4)
}

func TestGuardMachinePrContent_manifestAndPolicyEditsRefused(t *testing.T) {
	guarded := func(path string) gitpr.GuardMachinePrContentResult {
		result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
			ChangedPaths:  []string{path},
			ManifestPaths: []string{},
			DocPatches:    []gitpr.MachinePrDocPatch{},
		})
		require.NoError(t, err)
		return result
	}

	// NOTE: '.docsync/policies/...' is a DIFFERENT prefix than '.docsync/policy'
	// (p-o-l-i-c-i-e-s vs p-o-l-i-c-y) — repos needing it inject protectedPaths.
	for _, path := range []string{
		"manifest.yml",
		".docsync/manifest.yml",
		".docsync/policy.yml",
		".docsync/policy-v3.json",
		".docsync/org-policy.yml",
	} {
		result := guarded(path)
		require.False(t, result.OK, "%s must be refused", path)
		require.Len(t, result.Violations, 1)
		assert.Equal(t, gitpr.MachinePrRuleManifestPolicy, result.Violations[0].Rule)
		assert.Equal(t, path, result.Violations[0].Path)
		assert.Contains(t, result.Violations[0].Detail, "INV-7")
	}

	// NOTE: '.docsync/policies/...' is a DIFFERENT prefix than
	// '.docsync/policy' (p-o-l-i-c-i-e-s vs p-o-l-i-c-y) — repos that need it
	// inject protectedPaths.
	result := guarded(".docsync/policies/rates.yml")
	assert.True(t, result.OK, "the default set does not protect .docsync/policies/*")

	injected, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:   []string{".docsync/policies/rates.yml"},
		ManifestPaths:  []string{},
		DocPatches:     []gitpr.MachinePrDocPatch{},
		ProtectedPaths: []string{".docsync/policies*"},
	})
	require.NoError(t, err)
	require.False(t, injected.OK)
	require.Len(t, injected.Violations, 1)
	assert.Equal(t, gitpr.MachinePrRuleManifestPolicy, injected.Violations[0].Rule)
	assert.Equal(t, ".docsync/policies/rates.yml", injected.Violations[0].Path)
}

func TestGuardMachinePrContent_ordinaryMachinePRPasses(t *testing.T) {
	result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{"docs/api.md", ".docsync/state/runs/r.json", "docs/payments.md"},
		ManifestPaths: []string{"docs/api.md", "docs/payments.md"},
		DocPatches: []gitpr.MachinePrDocPatch{
			{DocPath: "docs/api.md"},
			{DocPath: "docs/payments.md", ModeAfter: cgTextPtr("auto-fix")},
		},
	})
	require.NoError(t, err)
	assert.True(t, result.OK)
	assert.Empty(t, result.Violations)
}

func TestGuardMachinePrContent_customProtectedPathsReplaceTheDefault(t *testing.T) {
	result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:   []string{"config/docsync-manifest.yaml"},
		ManifestPaths:  []string{},
		DocPatches:     []gitpr.MachinePrDocPatch{},
		ProtectedPaths: []string{"config/docsync-manifest.yaml"},
	})
	require.NoError(t, err)
	assert.False(t, result.OK)
	require.Len(t, result.Violations, 1)
	assert.Contains(t, result.Violations[0].Detail, "config/docsync-manifest.yaml")

	// A custom list does NOT protect the default names any more.
	result, err = gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:   []string{"manifest.yml"},
		ManifestPaths:  []string{},
		DocPatches:     []gitpr.MachinePrDocPatch{},
		ProtectedPaths: []string{"config/docsync-manifest.yaml"},
	})
	require.NoError(t, err)
	assert.True(t, result.OK)
}

// TestGuardMachinePrContent_manifestDetailCarriesDeepDedupEvidence ports the
// organization-policy unit view: the detail names the matched entry, the
// manifest-tracked suffix, and the surviving human floors.
func TestGuardMachinePrContent_manifestDetailCarriesDeepDedupEvidence(t *testing.T) {
	result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{".docsync/policy.yml"},
		ManifestPaths: []string{".docsync/policy.yml"},
		DocPatches:    []gitpr.MachinePrDocPatch{},
	})
	require.NoError(t, err)
	require.False(t, result.OK)
	detail := result.Violations[0].Detail
	assert.Contains(t, detail, "org policy")
	assert.Contains(t, detail, ".docsync/policy*")
	assert.Contains(t, detail, "human-owned configuration")
	assert.Contains(t, detail, "the path is also a manifest-tracked document path")

	// not a carried document
	result, err = gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{"manifest.yml"},
		ManifestPaths: []string{"docs/api.md"},
		DocPatches:    []gitpr.MachinePrDocPatch{},
	})
	require.NoError(t, err)
	require.False(t, result.OK)
	assert.NotContains(t, result.Violations[0].Detail, "also a manifest-tracked document path")
}

// TestGuardMachinePrContent_duplicatePathsDedup ports the ORDERING assert:
// one violation per path, not one per occurrence.
func TestGuardMachinePrContent_duplicatePathsDedup(t *testing.T) {
	result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths: []string{
			"manifest.yml",
			"docs/api.md",
			".docsync/manifest.yml",
			"manifest.yml",
			".docsync/manifest.yml",
		},
		ManifestPaths: []string{},
		DocPatches:    []gitpr.MachinePrDocPatch{},
	})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.Len(t, result.Violations, 2)
	// Changed-path order, one violation per PATH (not per occurrence).
	assert.Equal(t, gitpr.MachinePrRuleManifestPolicy, result.Violations[0].Rule)
	assert.Equal(t, "manifest.yml", result.Violations[0].Path)
	assert.Equal(t, gitpr.MachinePrRuleManifestPolicy, result.Violations[1].Rule)
	assert.Equal(t, ".docsync/manifest.yml", result.Violations[1].Path)
}

func TestGuardMachinePrContent_modeAfterAutoIsRefused(t *testing.T) {
	result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{"docs/payments.md"},
		ManifestPaths: []string{"docs/payments.md"},
		DocPatches: []gitpr.MachinePrDocPatch{
			{DocPath: "docs/payments.md", ModeAfter: cgTextPtr("auto")},
		},
	})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.NotEmpty(t, result.Violations)
	assert.Equal(t, gitpr.MachinePrRuleModeTransition, result.Violations[0].Rule)
	assert.Equal(t, "docs/payments.md", result.Violations[0].Path)
	assert.Contains(t, result.Violations[0].Detail, "human PR")
	assert.Contains(t, result.Violations[0].Detail, "mode transition")
	assert.Contains(t, result.Violations[0].Detail, "D2")
	assert.Contains(t, result.Violations[0].Detail, "INV-7")
	assert.Contains(t, result.Violations[0].Detail, "post-patch mode of 'auto'")
	assert.Contains(t, result.Violations[0].Detail, "even from 'suggest'")
}

// TestGuardMachinePrContent_contentOnlyPatchesNeverTripTheModeRule ports the
// "'auto' → 'auto' is NOT a transition" case: the flow never asserts
// modeAfter for a content-anchored patch, so a document that STAYS auto is
// never a transition, and any asserted non-auto mode passes.
func TestGuardMachinePrContent_contentOnlyPatchesNeverTripTheModeRule(t *testing.T) {
	for _, docPatches := range [][]gitpr.MachinePrDocPatch{
		{{DocPath: "docs/payments.md"}},
		{{DocPath: "docs/payments.md", ModeAfter: cgTextPtr("suggest")}},
	} {
		result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
			ChangedPaths:  []string{"docs/payments.md"},
			ManifestPaths: []string{"docs/payments.md"},
			DocPatches:    docPatches,
		})
		require.NoError(t, err)
		assert.True(t, result.OK)
	}
}

// TestGuardMachinePrContent_notManifestListedIsStillDedupedFreeToPatch pins
// the not-manifest-listed assertion (documenting the P6-T11 allowlist
// boundary: a not-manifest-listed document is NOT blocklist-denied, but its
// mode may not be auto-carried).
func TestGuardMachinePrContent_notManifestListedIsStillDedupedFreeToPatch(t *testing.T) {
	result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{"random.md"},
		ManifestPaths: []string{},
		DocPatches: []gitpr.MachinePrDocPatch{
			{DocPath: "random.md", ModeAfter: cgTextPtr("auto")},
		},
	})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, gitpr.MachinePrRuleModeTransition, result.Violations[0].Rule)
	assert.Contains(t, result.Violations[0].Detail, "not manifest-listed")
}

// TestGuardMachinePrContent_bothRulesFire ports the composed violation shape.
func TestGuardMachinePrContent_bothRulesFire(t *testing.T) {
	result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{"manifest.yml", "docs/api.md"},
		ManifestPaths: []string{"docs/api.md"},
		DocPatches: []gitpr.MachinePrDocPatch{
			{DocPath: "docs/api.md", ModeAfter: cgTextPtr("auto")},
		},
	})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.Len(t, result.Violations, 2)
	// changed-path violations come first, in changed-path order
	assert.Equal(t, gitpr.MachinePrRuleManifestPolicy, result.Violations[0].Rule)
	assert.Equal(t, "manifest.yml", result.Violations[0].Path)
	assert.Equal(t, gitpr.MachinePrRuleModeTransition, result.Violations[1].Rule)
	assert.Equal(t, "docs/api.md", result.Violations[1].Path)
}

// TestGuardMachinePrContent_deniesInjectedPolicyTheMachineNeverEdits pins the
// injected policy boundary.
func TestGuardMachinePrContent_deniesInjectedPolicyTheMachineNeverEdits(t *testing.T) {
	result, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{"docs/policy.yml"},
		ManifestPaths: []string{"docs/policy.yml"},
		DocPatches: []gitpr.MachinePrDocPatch{
			{DocPath: "docs/policy.yml", ModeAfter: cgTextPtr("suggest")},
		},
		ProtectedPaths: []string{"docs/policy.yml"},
	})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, gitpr.MachinePrRuleManifestPolicy, result.Violations[0].Rule)
	assert.Contains(t, result.Violations[0].Detail, "matched protected entry 'docs/policy.yml'")
}

// TestGuardMachinePrContent_malformedInputFailsClosed ports the typed
// refusal of an empty path/patch entry.
func TestGuardMachinePrContent_malformedInputFailsClosed(t *testing.T) {
	_, err := gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{""},
		ManifestPaths: []string{},
		DocPatches:    []gitpr.MachinePrDocPatch{},
	})
	require.Error(t, err)
	var guardErr *gitpr.MachinePrContentGuardError
	require.ErrorAs(t, err, &guardErr)
	assert.Equal(t, "EDOCSYNC_MACHINE_PR_CONTENT_GUARD", guardErr.Code())
	assert.Contains(t, err.Error(), "changedPaths")

	_, err = gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{"docs/api.md"},
		ManifestPaths: []string{"docs/api.md"},
		DocPatches: []gitpr.MachinePrDocPatch{
			{DocPath: "", ModeAfter: cgTextPtr("auto")},
		},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &guardErr)

	_, err = gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:  []string{"docs/api.md"},
		ManifestPaths: []string{""},
		DocPatches:    []gitpr.MachinePrDocPatch{},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &guardErr)

	_, err = gitpr.GuardMachinePrContent(gitpr.GuardMachinePrContentInput{
		ChangedPaths:   []string{"docs/api.md"},
		ManifestPaths:  []string{},
		DocPatches:     []gitpr.MachinePrDocPatch{},
		ProtectedPaths: []string{"docs/api.md", ""},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &guardErr)
}
