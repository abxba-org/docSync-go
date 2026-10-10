package boundaries_test

// Ported from packages/core/test/boundaries.test.ts (P0-T14, V-P0-13, invariant
// 3): path classification, bidirectional config/state write guards, fail-closed
// refusals for unknown paths, and the GuardedPathError error surface.

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/boundaries"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyDocsyncPath(t *testing.T) {
	cases := []struct {
		input    string
		expected boundaries.DocsyncPathClass
	}{
		{"manifest.yml", boundaries.PathClassConfig},
		{"policy.yml", boundaries.PathClassConfig},
		{"concepts.yml", boundaries.PathClassConfig},
		{"instructions.md", boundaries.PathClassConfig},
		{"models.yml", boundaries.PathClassConfig},
		{"org-policy.yml", boundaries.PathClassConfig},
		{".docsync/manifest.yml", boundaries.PathClassConfig},
		{"evaluation/cases/x.json", boundaries.PathClassConfig},
		{"evaluation/a/b/c/deep.txt", boundaries.PathClassConfig},
		{"memory/decisions.md", boundaries.PathClassConfig},
		{"memory/anything/at-all.txt", boundaries.PathClassConfig},
		{"policy-history/v1.json", boundaries.PathClassConfig},
		{"policy-history/archive/v2.json", boundaries.PathClassConfig},
		{"state/baselines/docs.md.json", boundaries.PathClassState},
		{"state/claims/c.json", boundaries.PathClassState},
		{"state/docs/d.md.json", boundaries.PathClassState},
		{"state/runs/r.json", boundaries.PathClassState},
		{"state/runs/run-1/steps/0.json", boundaries.PathClassState},
		{"state/audit-cursor", boundaries.PathClassState},
		{".docsync/state/baselines/x.json", boundaries.PathClassState},
		{"README.md", boundaries.PathClassUnknown},
		{"src/payments/refund.ts", boundaries.PathClassUnknown},
		{"state", boundaries.PathClassUnknown},
		{"memory", boundaries.PathClassUnknown},
		{"evaluation", boundaries.PathClassUnknown},
		{".docsync", boundaries.PathClassUnknown},
		{".docsync/.docsync/manifest.yml", boundaries.PathClassUnknown},
		{"manifest.yml.bak", boundaries.PathClassUnknown},
		{"stateX/baselines/x.json", boundaries.PathClassUnknown},
		{"evaluation/../manifest.yml", boundaries.PathClassUnknown},
		{"/etc/passwd", boundaries.PathClassUnknown},
		{"state\\baselines\\x.json", boundaries.PathClassUnknown},
		{"", boundaries.PathClassUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.expected, boundaries.ClassifyDocsyncPath(tc.input))
		})
	}
}

func refusalReason(t *testing.T, decision boundaries.WriteDecision) string {
	t.Helper()
	require.False(t, decision.Allowed, "expected a refusal")
	return decision.Reason
}

func TestGuardStateWriteRefusesHumanOwnedConfig(t *testing.T) {
	// V-P0-13: state writers refuse human-owned config with a PR-review pointer.
	for _, path := range []string{
		"manifest.yml",
		"policy.yml",
		"concepts.yml",
		"instructions.md",
		"models.yml",
		".docsync/policy.yml",
		"memory/decisions.md",
		"policy-history/v1.json",
		"evaluation/cases/x.json",
	} {
		t.Run(path, func(t *testing.T) {
			reason := refusalReason(t, boundaries.GuardStateWrite(path))
			assert.Contains(t, reason, "state writers must not touch human-owned config")
			assert.Contains(t, reason, "config-owned")
			assert.Contains(t, reason, "reviewable PR")
		})
	}
}

func TestGuardConfigWriteRefusesState(t *testing.T) {
	// V-P0-13: config writers refuse system-written state/.
	for _, path := range []string{
		"state/baselines/docs.md.json",
		"state/claims/c.json",
		"state/docs/d.md.json",
		"state/runs/r.json",
		"state/audit-cursor",
		".docsync/state/audit-cursor",
	} {
		t.Run(path, func(t *testing.T) {
			reason := refusalReason(t, boundaries.GuardConfigWrite(path))
			assert.Contains(t, reason, "state/ is system-written")
			assert.Contains(t, reason, "config writers must not touch")
		})
	}
}

func TestBothGuardsRefuseUnknownPaths(t *testing.T) {
	for _, path := range []string{
		"README.md",
		"src/payments/refund.ts",
		"evaluation/../manifest.yml",
		"../README.md",
		"/etc/passwd",
		"state\\baselines\\x.json",
		".docsync",
		"docs/payments.md",
		"",
	} {
		t.Run(path, func(t *testing.T) {
			assert.False(t, boundaries.GuardStateWrite(path).Allowed)
			assert.False(t, boundaries.GuardConfigWrite(path).Allowed)
		})
	}
}

func TestBothGuardsReportTraversalAsTheReason(t *testing.T) {
	for _, path := range []string{"evaluation/../manifest.yml", "../README.md", "state/../../x"} {
		t.Run(path, func(t *testing.T) {
			stateReason := refusalReason(t, boundaries.GuardStateWrite(path))
			assert.Contains(t, stateReason, "path traversal ('..') is not allowed")
			configReason := refusalReason(t, boundaries.GuardConfigWrite(path))
			assert.Contains(t, configReason, "path traversal ('..') is not allowed")
		})
	}
}

func TestGuardsReportAbsoluteAndBackslashReasons(t *testing.T) {
	assert.Contains(t, refusalReason(t, boundaries.GuardStateWrite("/etc/passwd")),
		"absolute paths are not allowed")
	assert.Contains(t, refusalReason(t, boundaries.GuardConfigWrite("C:/docsync/state/x")),
		"absolute paths are not allowed")
	assert.Contains(t, refusalReason(t, boundaries.GuardConfigWrite("state\\baselines\\x.json")),
		"backslashes are not allowed")
}

func TestGuardStateWriteAllowsStatePaths(t *testing.T) {
	for _, path := range []string{
		"state/baselines/docs-payments.json",
		"state/claims/c1.json",
		"state/docs/d.md.json",
		"state/runs/2026-10-05T17-00.json",
		"state/runs/run-1/steps/0.json",
		"state/audit-cursor",
	} {
		t.Run(path, func(t *testing.T) {
			assert.Equal(t, boundaries.WriteDecision{Allowed: true}, boundaries.GuardStateWrite(path))
		})
	}
}

func TestGuardConfigWriteAllowsConfigPaths(t *testing.T) {
	for _, path := range []string{
		"manifest.yml",
		"policy.yml",
		"concepts.yml",
		"instructions.md",
		"models.yml",
		".docsync/manifest.yml",
		"memory/decisions.md",
		"memory/known-patterns.md",
		"memory/exceptions.md",
		"memory/failures.md",
		"policy-history/v1.json",
		"evaluation/cases/x.json",
	} {
		t.Run(path, func(t *testing.T) {
			assert.Equal(t, boundaries.WriteDecision{Allowed: true}, boundaries.GuardConfigWrite(path))
		})
	}
}

// TestGuardMessagesAreExact locks the TS refusal templates byte-for-byte: the
// message text is what operators and downstream diagnostics observe.
func TestGuardMessagesAreExact(t *testing.T) {
	assert.Equal(t,
		`state writers must not touch human-owned config; "manifest.yml" (from ".docsync/manifest.yml") is config-owned — edit via a reviewable PR`,
		boundaries.GuardStateWrite(".docsync/manifest.yml").Reason)
	assert.Equal(t,
		`state/ is system-written; config writers must not touch "state/audit-cursor" (from "state/audit-cursor")`,
		boundaries.GuardConfigWrite("state/audit-cursor").Reason)
	assert.Equal(t,
		`"README.md" is outside the .docsync/ ownership model (config files, evaluation/, memory/, policy-history/, state/); refusing both config and state writes for "README.md" (fail closed)`,
		boundaries.GuardStateWrite("README.md").Reason)
	assert.Equal(t,
		`"docs/payments.md" is outside the .docsync/ ownership model (config files, evaluation/, memory/, policy-history/, state/); refusing both config and state writes for "docs/payments.md" (fail closed)`,
		boundaries.GuardConfigWrite("docs/payments.md").Reason)
	assert.Equal(t,
		`"baselines/x.json" is outside the .docsync/ ownership model (config files, evaluation/, memory/, policy-history/, state/); refusing both config and state writes for ".docsync/baselines/x.json" (fail closed)`,
		boundaries.GuardStateWrite(".docsync/baselines/x.json").Reason)
	assert.Equal(t,
		`backslashes are not allowed in docsync paths ("state\baselines\x.json") — use POSIX '/' separators`,
		boundaries.GuardStateWrite(`state\baselines\x.json`).Reason)
	assert.Equal(t,
		`absolute paths are not allowed ("/etc/passwd") — use a path relative to .docsync/`,
		boundaries.GuardConfigWrite("/etc/passwd").Reason)
	assert.Equal(t,
		`NUL bytes are not allowed in docsync paths ("state/`+"\x00"+`x.json")`,
		boundaries.GuardStateWrite("state/\x00x.json").Reason)
	assert.Equal(t,
		`path is empty ("") — expected a file path inside .docsync/`,
		boundaries.GuardStateWrite("").Reason)
	assert.Equal(t,
		`'.' segments are not allowed ("state/./x.json") — use a direct path inside .docsync/`,
		boundaries.GuardStateWrite("state/./x.json").Reason)
	assert.Equal(t,
		`empty path segments are not allowed ("state//x.json")`,
		boundaries.GuardConfigWrite("state//x.json").Reason)
}

func TestAssertStateWriteThrowsCodedErrorForConfigPaths(t *testing.T) {
	err := boundaries.AssertStateWrite("manifest.yml")
	require.Error(t, err)

	var guarded *boundaries.GuardedPathError
	require.True(t, errors.As(err, &guarded))
	assert.Equal(t, boundaries.EDOCSYNC_BOUNDARY, guarded.Code())
	assert.Equal(t, docutil.CodeGuard, docutil.CodeOf(err))
	assert.Equal(t, "manifest.yml", guarded.RelPath)
	assert.Equal(t, `state write refused for "manifest.yml": state writers must not touch human-owned config; "manifest.yml" (from "manifest.yml") is config-owned — edit via a reviewable PR`, guarded.Message)
	assert.Contains(t, guarded.Error(), "config-owned")
}

func TestAssertConfigWriteThrowsCodedErrorForStatePaths(t *testing.T) {
	err := boundaries.AssertConfigWrite("state/audit-cursor")
	require.Error(t, err)

	var guarded *boundaries.GuardedPathError
	require.True(t, errors.As(err, &guarded))
	assert.Equal(t, boundaries.EDOCSYNC_BOUNDARY, guarded.Code())
	assert.Equal(t, docutil.CodeGuard, docutil.CodeOf(err))
	assert.Equal(t, "state/audit-cursor", guarded.RelPath)
	assert.Equal(t, `config write refused for "state/audit-cursor": state/ is system-written; config writers must not touch "state/audit-cursor" (from "state/audit-cursor")`, guarded.Message)
	assert.Contains(t, guarded.Error(), "system-written")
}

func TestAssertVariantsDoNotErrorForAllowedPaths(t *testing.T) {
	assert.NoError(t, boundaries.AssertStateWrite("state/baselines/docs-payments.json"))
	assert.NoError(t, boundaries.AssertStateWrite("state/audit-cursor"))
	assert.NoError(t, boundaries.AssertConfigWrite("policy.yml"))
	assert.NoError(t, boundaries.AssertConfigWrite("policy-history/v1.json"))
}

func TestAssertVariantsRefuseUnknownPathsBothWays(t *testing.T) {
	for _, path := range []string{"README.md", "docs/payments.md", "../secrets.txt", ".docsync"} {
		t.Run(path, func(t *testing.T) {
			assert.Error(t, boundaries.AssertStateWrite(path))
			assert.Error(t, boundaries.AssertConfigWrite(path))
		})
	}
}

func TestEDOCSYNCBoundaryCodeIsGuarded(t *testing.T) {
	assert.Equal(t, "EDOCSYNC_GUARD", boundaries.EDOCSYNC_BOUNDARY)
	assert.Equal(t, docutil.CodeGuard, boundaries.EDOCSYNC_BOUNDARY)
}
