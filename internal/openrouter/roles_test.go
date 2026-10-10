package openrouter_test

// roles_test.go — Go test of the ported role surface in
// packages/openrouter/src/roles.ts (P2-T09; ARCH §10.2, D13). The TS package
// ships no dedicated roles.test.ts, so this suite pins the ARCH §10.2 role
// vocabulary, the OD-R41 structured-output modes, the judge verdict vocabulary,
// the operational-abstain classifier, and the V2 forward-declared role stubs
// (which fail closed with a typed NotYetImplementedRoleError).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
)

func TestARCHModelRolesVocabulary(t *testing.T) {
	assert.Equal(t, []openrouter.ArchModelRole{
		"decision-engine",
		"patch-writer",
		"judge",
		"policy-architect",
		"claim-extractor",
		"drift-auditor",
	}, openrouter.ARCHModelRoles)
	assert.True(t, openrouter.IsArchModelRole("decision-engine"))
	assert.True(t, openrouter.IsArchModelRole("drift-auditor"))
	assert.False(t, openrouter.IsArchModelRole("nonsense"))
	assert.False(t, openrouter.IsArchModelRole(""))
}

func TestStructuredOutputModes(t *testing.T) {
	assert.Equal(t, []openrouter.StructuredOutputMode{
		openrouter.StructuredOutputNative,
		openrouter.StructuredOutputPrompt,
	}, openrouter.LegalStructuredOutputModes)
	assert.True(t, openrouter.IsLegalStructuredOutputMode("native"))
	assert.True(t, openrouter.IsLegalStructuredOutputMode("prompt"))
	assert.False(t, openrouter.IsLegalStructuredOutputMode("freeform"))
}

func TestJudgeVerdicts(t *testing.T) {
	assert.True(t, openrouter.IsJudgeVerdict("pass"))
	assert.True(t, openrouter.IsJudgeVerdict("review"))
	assert.True(t, openrouter.IsJudgeVerdict("reject"))
	assert.False(t, openrouter.IsJudgeVerdict("maybe"))
}

func TestIsOperationalDecisionAbstain(t *testing.T) {
	// Operational degradation (kind 'abstain').
	assert.True(t, openrouter.IsOperationalDecisionAbstain("schema_violation"))
	assert.True(t, openrouter.IsOperationalDecisionAbstain("model_unavailable"))
	assert.True(t, openrouter.IsOperationalDecisionAbstain("cap_exceeded"))
	// Decision-level abstention (a SUCCESSFUL completion, kind 'decision').
	assert.False(t, openrouter.IsOperationalDecisionAbstain("insufficient_confidence"))
	assert.False(t, openrouter.IsOperationalDecisionAbstain("conflicting_evidence"))
}

func TestClaimExtractorRoleNotImplemented(t *testing.T) {
	// Compile-time check: the stub satisfies the role interface.
	var _ openrouter.ClaimExtractorRole = openrouter.ClaimExtractorRoleNotImplemented

	_, err := openrouter.ClaimExtractorRoleNotImplemented.ExtractClaims(openrouter.RoleInvocation{System: "s", User: "u"})
	require.Error(t, err)
	var e *openrouter.NotYetImplementedRoleError
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "claim-extractor", e.Role)
	assert.Contains(t, err.Error(), "claim-extractor")
	assert.Contains(t, err.Error(), "implementation yet")
}

func TestDriftAuditorRoleNotImplemented(t *testing.T) {
	var _ openrouter.DriftAuditorRole = openrouter.DriftAuditorRoleNotImplemented

	_, err := openrouter.DriftAuditorRoleNotImplemented.AuditDrift(openrouter.RoleInvocation{System: "s", User: "u"})
	require.Error(t, err)
	var e *openrouter.NotYetImplementedRoleError
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "drift-auditor", e.Role)
	assert.Contains(t, err.Error(), "drift-auditor")
}
