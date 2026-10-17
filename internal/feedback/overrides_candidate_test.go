// overrides_candidate_test.go
package feedback

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicyProposalCandidate(t *testing.T) {
	t.Run("valid candidate — returns a *schemas.PolicyProposal with no error", func(t *testing.T) {
		candidate := map[string]any{
			"schema":               schemas.PolicyProposalSchemaID,
			"proposal_id":          "01JAR3TC00000000000000000A",
			"created_at":           "2026-10-08T12:00:00Z",
			"policy_version_basis": 17,
			"type":                 string(schemas.PolicyProposalTypeRoutingRule),
			"counted_evidence": []schemas.CountedEvidence{
				{
					Statement:    `2/2 identical reviewer overrides on docs/sdk.md: "src/generated/** is not documentation-relevant"`,
					SupportCount: 2,
					TotalCount:   2,
				},
			},
			"artifact": map[string]any{
				"kind":      "reviewer_override_aggregation",
				"doc_path":  "docs/sdk.md",
				"statement": "src/generated/** is not documentation-relevant",
			},
			"status": string(schemas.PolicyProposalStatusQueued),
		}
		proposal, err := parsePolicyProposalCandidate(candidate)
		require.NoError(t, err)
		require.NotNil(t, proposal)
		assert.Equal(t, schemas.PolicyProposalSchemaID, proposal.Schema)
		assert.Equal(t, "01JAR3TC00000000000000000A", proposal.ProposalID)
		assert.Equal(t, "2026-10-08T12:00:00Z", proposal.CreatedAt)
		assert.Equal(t, 17, proposal.PolicyVersionBasis)
		assert.Equal(t, schemas.PolicyProposalTypeRoutingRule, proposal.Type)
		assert.Equal(t, schemas.PolicyProposalStatusQueued, proposal.Status)
	})

	t.Run("invalid candidate — schemas.ParsePolicyProposal fails", func(t *testing.T) {
		candidate := map[string]any{
			"proposal_id":          "01JAR3TC00000000000000000B",
			"created_at":           "2026-10-08T12:00:00Z",
			"policy_version_basis": 17,
			"type":                 string(schemas.PolicyProposalTypeRoutingRule),
			"counted_evidence":     []schemas.CountedEvidence{},
			"artifact":             map[string]any{"kind": "test"},
			"status":               "invalid_status",
		}
		_, err := parsePolicyProposalCandidate(candidate)
		require.Error(t, err)
		var validationErr *FeedbackValidationError
		require.ErrorAs(t, err, &validationErr)
		assert.Equal(t, EDOCSYNC_FEEDBACK_INVALID, validationErr.Code())
	})

	t.Run("marshalRowCompact failure arm", func(t *testing.T) {
		candidate := map[string]any{
			"schema":               schemas.PolicyProposalSchemaID,
			"proposal_id":          "01JAR3TC00000000000000000C",
			"created_at":           "2026-10-08T12:00:00Z",
			"policy_version_basis": 17,
			"type":                 string(schemas.PolicyProposalTypeRoutingRule),
			"counted_evidence":     []schemas.CountedEvidence{},
			"artifact":             map[string]any{"kind": "test", "bad": make(chan int)},
			"status":               string(schemas.PolicyProposalStatusQueued),
		}
		_, err := parsePolicyProposalCandidate(candidate)
		require.Error(t, err)
		var validationErr *FeedbackValidationError
		require.ErrorAs(t, err, &validationErr)
		assert.Equal(t, EDOCSYNC_FEEDBACK_INVALID, validationErr.Code())
	})
}

func TestAggregateOverridesSpansPolicyVersions(t *testing.T) {
	t.Run("group spanning policy versions should return newFeedbackValidationError", func(t *testing.T) {
		rows := []schemas.FeedbackEvent{
			{
				Schema:        "docsync.feedback-event/v1",
				EventID:       "01JAR3TB000000000000000000",
				At:            "2026-10-08T12:00:00Z",
				DecisionID:    "d_override",
				PolicyVersion: 17,
				Signal:        schemas.FeedbackEventSignalReject,
				RejectReason:  func() *string { s := "too verbose"; return &s }(),
			},
			{
				Schema:        "docsync.feedback-event/v1",
				EventID:       "01JAR3TB000000000000000001",
				At:            "2026-10-08T12:00:00Z",
				DecisionID:    "d_override",
				PolicyVersion: 18,
				Signal:        schemas.FeedbackEventSignalReject,
				RejectReason:  func() *string { s := "too verbose"; return &s }(),
			},
		}
		_, err := AggregateOverrides(AggregateOverridesInput{Rows: rows, At: "2026-10-08T12:00:00Z"})
		require.Error(t, err)
		var validationErr *FeedbackValidationError
		require.ErrorAs(t, err, &validationErr)
		assert.Equal(t, EDOCSYNC_FEEDBACK_INVALID, validationErr.Code())
		assert.Contains(t, validationErr.Message, "spans policy versions")
	})
}
