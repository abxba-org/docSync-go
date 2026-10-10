package schemas_test

// Ported from packages/schemas/test/policy-proposal.test.ts —
// docsync.policy-proposal/v1 (P10-T01; ARCH §8.1, CTX §16): counted evidence
// mandatory and ratio-checked; invalid proposals carry diagnostics; closed
// eight-type vocabulary.

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyProposalExampleValidates(t *testing.T) {
	proposal := schemas.PolicyProposalExample()
	require.Empty(t, proposal.Validate())
	_, err := schemas.ParsePolicyProposal(mustJSONTyped(t, proposal))
	require.NoError(t, err)
}

func TestPolicyProposalAllEightTypesParseUnknownTypeRefused(t *testing.T) {
	assert.Len(t, schemas.PolicyProposalTypeValues, 8)

	for _, proposalType := range schemas.PolicyProposalTypeValues {
		proposal := schemas.PolicyProposalExample()
		proposal.Type = proposalType
		require.Empty(t, proposal.Validate(), "type %s must parse", proposalType)

		_, err := schemas.ParsePolicyProposal(mustJSONTyped(t, proposal))
		require.NoError(t, err, "type %s must parse", proposalType)
	}

	rejected := schemas.PolicyProposalExample()
	rejected.Type = "prompt_tweak"
	require.NotEmpty(t, rejected.Validate())
	_, err := schemas.ParsePolicyProposal(mustJSONTyped(t, rejected))
	require.Error(t, err)
}

func TestPolicyProposalCountedEvidenceMandatoryAndRatioChecked(t *testing.T) {
	// Empty evidence list is refused.
	empty := schemas.PolicyProposalExample()
	empty.CountedEvidence = []schemas.CountedEvidence{}
	require.NotEmpty(t, empty.Validate())

	// support_count > total_count is refused with the self-describing message.
	ratio := schemas.PolicyProposalExample()
	ratio.CountedEvidence = []schemas.CountedEvidence{
		{Statement: "x", SupportCount: 25, TotalCount: 23},
	}
	issues := ratio.Validate()
	require.NotEmpty(t, issues)
	found := false
	for _, iss := range issues {
		if iss.Message == "counted evidence \"x\": support_count (25) exceeds total_count (23)" {
			found = true
		}
	}
	assert.True(t, found, "expected the ratio issue, got %v", issues)

	_, err := schemas.ParsePolicyProposal(mustJSONTyped(t, ratio))
	require.Error(t, err)
}

func TestPolicyProposalStatusInvalidRequiresDiagnostics(t *testing.T) {
	missing := schemas.PolicyProposalExample()
	missing.Status = schemas.PolicyProposalStatusInvalid
	missing.Diagnostics = nil

	issues := missing.Validate()
	require.NotEmpty(t, issues)
	found := false
	for _, iss := range issues {
		if iss.Message == "status invalid requires compiler diagnostics — no partial policies ever reach production (ARCH §8.2)" {
			found = true
		}
	}
	assert.True(t, found, "expected the diagnostics issue, got %v", issues)

	_, err := schemas.ParsePolicyProposal(mustJSONTyped(t, missing))
	require.Error(t, err)

	withDiagnostics := schemas.PolicyProposalExample()
	withDiagnostics.Status = schemas.PolicyProposalStatusInvalid
	withDiagnostics.Diagnostics = []string{"artifact.type unknown"}
	require.Empty(t, withDiagnostics.Validate())
	_, err = schemas.ParsePolicyProposal(mustJSONTyped(t, withDiagnostics))
	require.NoError(t, err)
}

func TestPolicyProposalUnknownKeysRefusedAndProposalIDMustBeULID(t *testing.T) {
	base := mustJSONTyped(t, schemas.PolicyProposalExample())

	extra := mutateJSON(t, base, func(tree any) {
		tree.(map[string]any)["extra"] = true
	})
	_, err := schemas.ParsePolicyProposal(extra)
	require.Error(t, err)

	badULID := schemas.PolicyProposalExample()
	badULID.ProposalID = "not-a-ulid"
	require.NotEmpty(t, badULID.Validate())
	_, err = schemas.ParsePolicyProposal(mustJSONTyped(t, badULID))
	require.Error(t, err)
}

func TestPolicyProposalStatusValuesAreTheClosedVocabulary(t *testing.T) {
	assert.Equal(t, []schemas.PolicyProposalStatus{
		"draft", "validated", "invalid", "queued",
	}, schemas.PolicyProposalStatusValues)

	rejected := schemas.PolicyProposalExample()
	rejected.Status = "archived"
	require.NotEmpty(t, rejected.Validate())
}
