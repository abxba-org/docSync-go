package openrouter_test

// adapter-policy-architect_test.go — Go port of
// packages/openrouter/test/adapters/policy-architect.test.ts (P10-T14; ARCH
// §8.1) — recorded doubles only: a valid draft passes the port shape; a
// schema-abstaining response raises ArchitectAbstainError; an oversized input
// refuses BEFORE any transport call.
//
// NOTE (deviation): as with the other V2 role adapters, the TS suite drove the
// REAL OpenRouterClient; here the adapter is driven over the recorded client
// double, so the transport-level "must not be called" guarantee is asserted as
// "the client double records no exchange".

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
)

// architectModels parses the models.yml the TS fixture uses.
func architectModels(t *testing.T) *openrouter.ExtendedModelsConfig {
	t.Helper()
	return modelsWithRoles(t, "decision-engine", "judge", "writer", "policy-architect")
}

// validArchitectDraft is the TS VALID_DRAFT fixture (the wire shape).
var validArchitectDraft = map[string]any{
	"type": "threshold",
	"counted_evidence": []any{
		map[string]any{
			"statement":     "17/23 reviewer overrides targeted generated-API routing",
			"support_count": 17,
			"total_count":   23,
		},
	},
	"artifact": map[string]any{"policy_version": 2},
}

// architectInput is the TS INPUT fixture.
func architectInput() openrouter.PolicyArchitectAdapterInput {
	return openrouter.PolicyArchitectAdapterInput{
		RepositoryProfile:   map[string]any{"language": "typescript"},
		CurrentPolicy:       map[string]any{"schema": "docsync.jev-policy/v1"},
		Manifest:            map[string]any{"docs": []any{"docs/**"}},
		Concepts:            map[string]any{},
		HistoricalPrs:       []map[string]any{},
		PriorDecisions:      []map[string]any{},
		FalsePositives:      []map[string]any{},
		FalseNegatives:      []map[string]any{},
		ReviewerCorrections: []map[string]any{},
		KnownExceptions:     []map[string]any{},
	}
}

func TestPolicyArchitectAdapter(t *testing.T) {
	t.Run("a valid structured draft passes the port shape", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: completionOf(toStructured(t, validArchitectDraft), "z-ai/glm-4.7"),
			}},
		}
		adapter, err := openrouter.CreatePolicyArchitectAdapter(openrouter.PolicyArchitectAdapterOptions{
			Client: client,
			Models: architectModels(t),
		})
		require.NoError(t, err)

		draft, err := adapter.Propose(architectInput())
		require.NoError(t, err)
		assert.Equal(t, "threshold", string(draft.Type))
		require.Len(t, draft.CountedEvidence, 1)
		assert.Equal(t, 17, draft.CountedEvidence[0].SupportCount)
		assert.Equal(t, 23, draft.CountedEvidence[0].TotalCount)
		// The artifact is opaque model data: numbers round-trip as json.Number
		// (lossless — the same representation the client's structured path
		// hands to every adapter).
		assert.Equal(t, map[string]any{"policy_version": json.Number("2")}, draft.Artifact)

		// The prompt carries the ten validated inputs as DATA (the AI seam,
		// ARCH §8.1) — never policy it could execute.
		request := client.lastRequest(t)
		assert.Equal(t, openrouter.ArchitectSystemPrompt, request.System)
		assert.Contains(t, request.System, "You PROPOSE policy changes as structured drafts for human promotion")
		assert.Contains(t, request.User, "typescript")
		assert.Contains(t, request.User, "docsync.jev-policy/v1")
	})

	t.Run("a schema-abstaining response RAISES ArchitectAbstainError — never a fabricated proposal", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: openrouter.RoleCompletion{
					Kind:   openrouter.RoleCompletionKindSchemaAbstain,
					Reason: openrouter.SchemaViolationAbstainReason,
				},
			}},
		}
		adapter, err := openrouter.CreatePolicyArchitectAdapter(openrouter.PolicyArchitectAdapterOptions{
			Client: client,
			Models: architectModels(t),
		})
		require.NoError(t, err)

		_, err = adapter.Propose(architectInput())
		require.Error(t, err)
		var abstain *openrouter.ArchitectAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Equal(t, "schema_violation", abstain.Reason)
		assert.Equal(t, "architect abstained: schema_violation", err.Error())
	})

	t.Run("a response shaped outside the draft contract is rejected fail-closed", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: completionOf(toStructured(t, map[string]any{"wrong": "shape"}), "z-ai/glm-4.7"),
			}},
		}
		adapter, err := openrouter.CreatePolicyArchitectAdapter(openrouter.PolicyArchitectAdapterOptions{
			Client: client,
			Models: architectModels(t),
		})
		require.NoError(t, err)

		_, err = adapter.Propose(architectInput())
		require.Error(t, err)
		var abstain *openrouter.ArchitectAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "failed the policy-architect draft contract")
	})

	t.Run("a draft without counted evidence is rejected (policy learning requires counts)", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: completionOf(toStructured(t, map[string]any{
					"type":             "threshold",
					"counted_evidence": []any{},
					"artifact":         map[string]any{"policy_version": 2},
				}), "z-ai/glm-4.7"),
			}},
		}
		adapter, err := openrouter.CreatePolicyArchitectAdapter(openrouter.PolicyArchitectAdapterOptions{
			Client: client,
			Models: architectModels(t),
		})
		require.NoError(t, err)

		_, err = adapter.Propose(architectInput())
		require.Error(t, err)
		var abstain *openrouter.ArchitectAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "failed the policy-architect draft contract")
	})

	t.Run("an oversized input refuses BEFORE any transport call", func(t *testing.T) {
		client := &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"}}
		adapter, err := openrouter.CreatePolicyArchitectAdapter(openrouter.PolicyArchitectAdapterOptions{
			Client: client,
			Models: architectModels(t),
		})
		require.NoError(t, err)

		oversized := architectInput()
		oversized.RepositoryProfile = map[string]any{"blob": repeatString("x", openrouter.ArchitectMaxInputBytes)}
		_, err = adapter.Propose(oversized)
		require.Error(t, err)
		var abstain *openrouter.ArchitectAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Regexp(t, `exceed the size bound \(\d+ > 200000 bytes\)`, err.Error())
		// The transport (client) was never called.
		assert.Equal(t, 0, client.calls())
	})

	t.Run("an unbound policy-architect role fails closed at construction", func(t *testing.T) {
		_, err := openrouter.CreatePolicyArchitectAdapter(openrouter.PolicyArchitectAdapterOptions{
			Client: &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"}},
			Models: modelsWithRoles(t, "decision-engine"),
		})
		require.Error(t, err)
		assert.Regexp(t, `policy-architect role is not bound`, err.Error())
	})

	t.Run("the output is a DRAFT only — no policy it can compile or execute", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: completionOf(toStructured(t, validArchitectDraft), "z-ai/glm-4.7"),
			}},
		}
		adapter, err := openrouter.CreatePolicyArchitectAdapter(openrouter.PolicyArchitectAdapterOptions{
			Client: client,
			Models: architectModels(t),
		})
		require.NoError(t, err)
		draft, err := adapter.Propose(architectInput())
		require.NoError(t, err)

		data, err := json.Marshal(draft)
		require.NoError(t, err)
		var draftMap map[string]any
		require.NoError(t, json.Unmarshal(data, &draftMap))
		assert.Equal(t, []string{"artifact", "counted_evidence", "type"}, sortedKeys(draftMap))
	})
}
