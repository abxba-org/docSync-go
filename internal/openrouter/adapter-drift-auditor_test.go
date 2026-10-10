package openrouter_test

// adapter-drift-auditor_test.go — Go port of
// packages/openrouter/test/adapters/drift-auditor.test.ts (P11-T21; ARCH
// §7.2) — recorded doubles only: a valid draft passes with the ACTUALLY ROUTED
// model stamped as audit_model and recorded routed-model telemetry; a malformed
// response then raises DriftAuditorAbstainError (never a fabricated finding);
// the output shape is advisory analysis data only — no patch/write decisions;
// an unbound role fails closed at construction.
//
// NOTE (deviation): the TS suite drove the REAL OpenRouterClient, so its
// bounded-retry assertion (`counter.calls === 3`) exercised the CLIENT's
// corrective-retry loop. The Go client is ported concurrently, so this suite
// drives the adapter over the recorded client double: the retry-bound behavior
// belongs to the client unit's suite, and the adapter-side observable — a
// schema-abstain completion raises, never fabricating findings — is asserted
// here.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
)

// driftAuditorModels parses the models.yml the TS fixture uses.
func driftAuditorModels(t *testing.T) *openrouter.ExtendedModelsConfig {
	t.Helper()
	return modelsWithRoles(t,
		"decision-engine", "judge", "writer", "drift-auditor")
}

// modelsWithRoles parses a models.yml binding the given roles (TS
// parseModelsConfig).
func modelsWithRoles(t *testing.T, roles ...string) *openrouter.ExtendedModelsConfig {
	t.Helper()
	document := map[string]any{
		"schema":  "docsync.models/v1",
		"version": 1,
		"roles":   map[string]any{},
	}
	roleMap := document["roles"].(map[string]any)
	for _, role := range roles {
		roleMap[role] = map[string]any{"model": "z-ai/glm-4.7"}
	}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	parsed, err := openrouter.ParseModelsConfig(data)
	require.NoError(t, err)
	return &parsed.Models
}

// validDriftDraft is the TS VALID_DRAFT fixture (the wire shape).
var validDriftDraft = map[string]any{
	"summary": "One documented claim lost its backing source evidence.",
	"proposed_findings": []any{
		map[string]any{
			"id":       "finding_claim_182_docs_payments_md",
			"claim_id": "claim_182",
			"doc_path": "docs/payments.md",
			"kind":     "claim-lost-support",
			"evidence": map[string]any{
				"commits": []any{"81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5"},
				"paths":   []any{"src/payments/refund.ts"},
				"detail":  "recorded backing bytes no longer exist at head (ARCH §4.3)",
			},
			"confidence": 0.9,
		},
	},
}

// driftAuditorInput is the TS INPUT fixture.
func driftAuditorInput() openrouter.DriftAuditorAdapterInput {
	return openrouter.DriftAuditorAdapterInput{
		RunID:       "audit_run_01J9ZK7Q2M",
		Trigger:     "nightly-scheduled",
		AuditedDocs: []string{"docs/payments.md"},
		SkippedDocs: []string{},
		Findings: []map[string]any{
			{
				"id":       "finding_claim_182_docs_payments_md",
				"claim_id": "claim_182",
				"doc_path": "docs/payments.md",
				"kind":     "claim-lost-support",
				"evidence": map[string]any{
					"commits": []any{"81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5"},
					"paths":   []any{"src/payments/refund.ts"},
					"detail":  "recorded backing bytes no longer exist at head (ARCH §4.3)",
				},
				"created_at": "2026-10-09T00:00:00.000Z",
			},
		},
	}
}

// completionOf builds a completion carrying a structured draft value.
func completionOf(value any, routedModel string) openrouter.RoleCompletion {
	return openrouter.RoleCompletion{
		Kind:       openrouter.RoleCompletionKindCompletion,
		Structured: value,
		Telemetry: openrouter.RoleTelemetry{
			Role:           "drift-auditor",
			RequestedModel: "z-ai/glm-4.7",
			RoutedModel:    routedModel,
		},
	}
}

// toStructured re-decodes a JSON tree the way a structured completion's value
// arrives (generic JSON, not a typed draft).
func toStructured(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded any
	require.NoError(t, json.Unmarshal(data, &decoded))
	return decoded
}

func TestDriftAuditorAdapter(t *testing.T) {
	t.Run("binds via models.yml and records routed-model telemetry", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: completionOf(toStructured(t, validDriftDraft), "z-ai/glm-4.7-fallback"),
			}},
		}
		adapter, err := openrouter.CreateDriftAuditorAdapter(openrouter.DriftAuditorAdapterOptions{
			Client: client,
			Models: driftAuditorModels(t),
		})
		require.NoError(t, err)

		analysis, err := adapter.AuditDrift(driftAuditorInput())
		require.NoError(t, err)
		assert.Contains(t, analysis.Summary, "lost its backing source evidence")
		require.Len(t, analysis.ProposedFindings, 1)
		finding := analysis.ProposedFindings[0]
		assert.Equal(t, "docs/payments.md", finding.DocPath)
		require.Len(t, finding.Evidence.Commits, 1)
		assert.Equal(t, "81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5", finding.Evidence.Commits[0])
		require.NotNil(t, finding.ClaimID)
		assert.Equal(t, "claim_182", *finding.ClaimID)
		assert.Equal(t, "claim-lost-support", string(finding.Kind))
		require.NotNil(t, finding.Confidence)
		assert.InDelta(t, 0.9, *finding.Confidence, 1e-9)

		// Provenance: the ACTUALLY ROUTED model id (response body `model`
		// field), NOT the role-bound primary (V-P2-11; ARCH §10.6).
		assert.Equal(t, "z-ai/glm-4.7-fallback", finding.AuditModel)
		assert.Equal(t, "drift-auditor", analysis.Telemetry.Role)
		assert.Equal(t, "z-ai/glm-4.7", analysis.Telemetry.RequestedModel)
		assert.Equal(t, "z-ai/glm-4.7-fallback", analysis.Telemetry.RoutedModel)

		request := client.lastRequest(t)
		assert.Equal(t, openrouter.DriftAuditorSystemPrompt, request.System)
		assert.Contains(t, request.System, "You do not decide writes and you do not apply changes")
		assert.Contains(t, request.User, "audit_run_01J9ZK7Q2M")
	})

	t.Run("a malformed response RAISES a typed abstain (never fabricated findings)", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				// The client's bounded corrective retries were exhausted: the
				// adapter receives the typed schema-abstain and must NOT
				// fabricate findings from it.
				Completion: openrouter.RoleCompletion{
					Kind:   openrouter.RoleCompletionKindSchemaAbstain,
					Reason: openrouter.SchemaViolationAbstainReason,
				},
			}},
		}
		adapter, err := openrouter.CreateDriftAuditorAdapter(openrouter.DriftAuditorAdapterOptions{
			Client: client,
			Models: driftAuditorModels(t),
		})
		require.NoError(t, err)

		_, err = adapter.AuditDrift(driftAuditorInput())
		require.Error(t, err)
		var abstain *openrouter.DriftAuditorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Equal(t, "schema_violation", abstain.Reason)
		assert.Equal(t, "drift auditor abstained: schema_violation", err.Error())
	})

	t.Run("a response shaped outside the draft contract is rejected fail-closed", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: completionOf(toStructured(t, map[string]any{"wrong": "shape"}), "z-ai/glm-4.7"),
			}},
		}
		adapter, err := openrouter.CreateDriftAuditorAdapter(openrouter.DriftAuditorAdapterOptions{
			Client: client,
			Models: driftAuditorModels(t),
		})
		require.NoError(t, err)

		_, err = adapter.AuditDrift(driftAuditorInput())
		require.Error(t, err)
		var abstain *openrouter.DriftAuditorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "failed the drift-auditor draft contract")
	})

	t.Run("refuses above the size bound before any model call", func(t *testing.T) {
		client := &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"}}
		adapter, err := openrouter.CreateDriftAuditorAdapter(openrouter.DriftAuditorAdapterOptions{
			Client: client,
			Models: driftAuditorModels(t),
		})
		require.NoError(t, err)

		oversized := driftAuditorInput()
		oversized.Findings = []map[string]any{{"blob": ""}}
		oversized.Findings[0]["blob"] = repeatString("x", 200_000)
		_, err = adapter.AuditDrift(oversized)
		require.Error(t, err)
		var abstain *openrouter.DriftAuditorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Regexp(t, `exceed the size bound \(\d+ > 200000 bytes\)`, err.Error())
		assert.Equal(t, 0, client.calls())
	})

	t.Run("output is advisory data — the adapter emits no patch/write decisions", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: completionOf(toStructured(t, validDriftDraft), "z-ai/glm-4.7-fallback"),
			}},
		}
		adapter, err := openrouter.CreateDriftAuditorAdapter(openrouter.DriftAuditorAdapterOptions{
			Client: client,
			Models: driftAuditorModels(t),
		})
		require.NoError(t, err)
		analysis, err := adapter.AuditDrift(driftAuditorInput())
		require.NoError(t, err)

		// Structural assert on the output shape: summary + proposed findings +
		// telemetry ONLY — no decision/patch/write surface.
		data, err := json.Marshal(analysis)
		require.NoError(t, err)
		var analysisMap map[string]any
		require.NoError(t, json.Unmarshal(data, &analysisMap))
		assert.Equal(t, []string{"proposed_findings", "summary", "telemetry"}, sortedKeys(analysisMap))

		// The response contract itself is advisory-shaped: summary +
		// proposed_findings only.
		assert.Equal(t, []string{"proposed_findings", "summary"}, sortedKeys(validDriftDraft))

		for _, rawFinding := range analysisMap["proposed_findings"].([]any) {
			finding := rawFinding.(map[string]any)
			assert.Equal(t,
				[]string{"audit_model", "claim_id", "confidence", "doc_path", "evidence", "id", "kind"},
				sortedKeys(finding))
		}

		// No write-decision vocabulary anywhere in the emitted data.
		for _, forbidden := range []string{`"hunks"`, `"action"`, `"patch"`, `"apply"`, `"write"`} {
			assert.NotContains(t, string(data), forbidden)
		}
	})

	t.Run("an unbound drift-auditor role fails closed at construction", func(t *testing.T) {
		_, err := openrouter.CreateDriftAuditorAdapter(openrouter.DriftAuditorAdapterOptions{
			Client: &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"}},
			Models: modelsWithRoles(t, "decision-engine"),
		})
		require.Error(t, err)
		assert.Regexp(t, `drift-auditor role is not bound`, err.Error())
	})
}

// repeatString is strings.Repeat under a local name (the TS 'x'.repeat(n)).
func repeatString(value string, count int) string {
	out := make([]byte, 0, len(value)*count)
	for i := 0; i < count; i++ {
		out = append(out, value...)
	}
	return string(out)
}
