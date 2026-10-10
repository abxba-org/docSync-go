package openrouter_test

// adapter-claim-extractor_test.go — Go port of
// packages/openrouter/test/adapters/claim-extractor.test.ts (P11-T12; ARCH
// §4.3, §8.1 posture) — black-box with testify, driving the adapter over a
// recorded CLIENT double (the TS suite drove the REAL OpenRouterClient over
// recorded transports; the client is ported concurrently, so the seam is the
// recorded double — OD-2: no network).

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
)

/* ----------------------------------------------------------------- doubles */

// scriptedCompletion is one recorded client result.
type scriptedCompletion struct {
	Completion openrouter.RoleCompletion
	Err        error
}

// recordedClient is the recorded client double (TS: testClient over
// createRecordingTransport/createReplayTransport/createFailingTransport):
// it captures every exchange (role + the outgoing CompletionRequest, appended
// BEFORE the scripted outcome applies) and replays scripted completions.
type recordedClient struct {
	binding openrouter.ResolvedRoleBinding

	mu        sync.Mutex
	script    []scriptedCompletion
	exchanges []recordedClientExchange
}

type recordedClientExchange struct {
	Role    string
	Request openrouter.CompletionRequest
}

func (c *recordedClient) BindingFor(_ string) (*openrouter.ResolvedRoleBinding, error) {
	binding := c.binding
	return &binding, nil
}

func (c *recordedClient) Complete(role string, request openrouter.CompletionRequest) (openrouter.RoleCompletion, error) {
	c.mu.Lock()
	c.exchanges = append(c.exchanges, recordedClientExchange{Role: role, Request: request})
	if len(c.script) == 0 {
		c.mu.Unlock()
		return openrouter.RoleCompletion{}, openrouter.NewReplayExhaustedError("no scripted completion left")
	}
	scripted := c.script[0]
	c.script = c.script[1:]
	c.mu.Unlock()
	return scripted.Completion, scripted.Err
}

func (c *recordedClient) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.exchanges)
}

func (c *recordedClient) lastRequest(t *testing.T) openrouter.CompletionRequest {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.exchanges)
	return c.exchanges[len(c.exchanges)-1].Request
}

// telemetryOf builds a completion telemetry carrying the routed model.
func telemetryOf(routedModel string) openrouter.RoleTelemetry {
	return openrouter.RoleTelemetry{
		Role:           "claim-extractor",
		RequestedModel: "z-ai/glm-4.7",
		RoutedModel:    routedModel,
	}
}

// draftStructured is the model-visible draft (the wire shape, as a generic
// JSON tree — exactly what a structured completion carries).
func draftStructured(t *testing.T, claims []map[string]any) any {
	t.Helper()
	var value any
	require.NoError(t, json.Unmarshal([]byte(`{"claims":[]}`), &value))
	data, err := json.Marshal(map[string]any{"claims": claims})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &value))
	return value
}

// extractorModels parses a models.yml binding the given roles (TS
// parseModelsConfig).
func extractorModels(t *testing.T, roles ...string) *openrouter.ExtendedModelsConfig {
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

// extractorInput builds the extractor input (TS INPUT).
func extractorInput() openrouter.ClaimExtractorAdapterInput {
	return openrouter.ClaimExtractorAdapterInput{
		DocPath:    "docs/payments.md",
		DocContent: "# Payments\nRefunds require an idempotency key.",
		DocStructure: openrouter.ClaimExtractorDocStructure{
			SectionHeadings: []string{"Payments"},
			LineCount:       2,
		},
		ExistingClaims: []openrouter.ClaimExtractorExistingClaim{
			{
				ClaimText: "Refunds take 5-7 business days.",
				Anchor:    map[string]any{"section_heading": "Payments"},
			},
		},
		PolicyVersion: 17,
	}
}

func TestClaimExtractorAdapter(t *testing.T) {
	t.Run("builds a constrained prompt from read-only doc data and returns a contract-governed draft", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: openrouter.RoleCompletion{
					Kind: openrouter.RoleCompletionKindCompletion,
					Structured: draftStructured(t, []map[string]any{
						{
							"claim_text": "Refunds require an idempotency key.",
							"candidate_anchor": map[string]any{
								"section_heading": "Payments",
								"line_hint":       3,
							},
							"source_evidence": map[string]any{
								"quote":    "Refunds require an idempotency key.",
								"doc_path": "docs/payments.md",
							},
						},
					}),
					Telemetry: telemetryOf("z-ai/glm-4.7-fallback"),
				},
			}},
		}
		adapter, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)

		draft, err := adapter.Extract(extractorInput())
		require.NoError(t, err)
		require.Len(t, draft.Claims, 1)

		claim := draft.Claims[0]
		assert.Equal(t, "Refunds require an idempotency key.", claim.ClaimText)
		require.NotNil(t, claim.CandidateAnchor.SectionHeading)
		assert.Equal(t, "Payments", *claim.CandidateAnchor.SectionHeading)
		require.NotNil(t, claim.CandidateAnchor.LineHint)
		assert.Equal(t, 3, *claim.CandidateAnchor.LineHint)
		assert.Equal(t, "Refunds require an idempotency key.", claim.SourceEvidence.Quote)
		assert.Equal(t, "docs/payments.md", claim.SourceEvidence.DocPath)
		// The claim carries the model that ACTUALLY served the extraction
		// (ARCH §10.6) — never a stale id.
		assert.Equal(t, "z-ai/glm-4.7-fallback", claim.ExtractionModel)

		// The prompt is read-only doc DATA, contract-instructed: the system
		// prompt is the fixed extractor contract and the user prompt carries
		// the serialized inputs (doc path, content, structure, prior claims).
		request := client.lastRequest(t)
		assert.Equal(t, openrouter.ClaimExtractorSystemPrompt, request.System)
		assert.Contains(t, request.System, "DOC → claims, never code → claims")
		assert.Contains(t, request.User, "docs/payments.md")
		assert.Contains(t, request.User, "Refunds require an idempotency key.")
		assert.Contains(t, request.User, "Refunds take 5-7 business days.")
	})

	t.Run("an empty claims array is an honest draft (never fabricated evidence)", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: openrouter.RoleCompletion{
					Kind:       openrouter.RoleCompletionKindCompletion,
					Structured: draftStructured(t, nil),
					Telemetry:  telemetryOf("z-ai/glm-4.7"),
				},
			}},
		}
		adapter, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)
		draft, err := adapter.Extract(extractorInput())
		require.NoError(t, err)
		assert.Empty(t, draft.Claims)
	})

	t.Run("refuses above the size bound before any model call (read-only untrusted content)", func(t *testing.T) {
		client := &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"}}
		adapter, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)

		oversized := extractorInput()
		oversized.DocContent = strings.Repeat("x", 200_000)
		_, err = adapter.Extract(oversized)
		require.Error(t, err)
		var abstain *openrouter.ClaimExtractorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Regexp(t, `exceed the size bound \(\d+ > 200000 bytes\)`, err.Error())
		// Refused BEFORE any model call.
		assert.Equal(t, 0, client.calls())
	})

	t.Run("a schema-abstain completion RAISES — it never fabricates claims", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: openrouter.RoleCompletion{
					Kind:   openrouter.RoleCompletionKindSchemaAbstain,
					Reason: openrouter.SchemaViolationAbstainReason,
				},
			}},
		}
		adapter, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)
		_, err = adapter.Extract(extractorInput())
		require.Error(t, err)
		var abstain *openrouter.ClaimExtractorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Equal(t, "schema_violation", abstain.Reason)
		assert.Equal(t, "claim extractor abstained: schema_violation", err.Error())
	})

	t.Run("a transport failure propagates typed (the caller degrades — D10)", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Err: openrouter.NewTransportError("transport request failed: connection refused", 0, nil),
			}},
		}
		adapter, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)
		_, err = adapter.Extract(extractorInput())
		require.Error(t, err)
		var transportErr *openrouter.TransportError
		require.ErrorAs(t, err, &transportErr)
	})

	t.Run("a draft violating the contract (no quote, no anchor) is rejected fail-closed", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: openrouter.RoleCompletion{
					Kind: openrouter.RoleCompletionKindCompletion,
					Structured: draftStructured(t, []map[string]any{
						{
							"claim_text":       "A claim with no evidence at all.",
							"candidate_anchor": map[string]any{},
							"source_evidence": map[string]any{
								"quote":    "",
								"doc_path": "docs/payments.md",
							},
						},
					}),
					Telemetry: telemetryOf("z-ai/glm-4.7"),
				},
			}},
		}
		adapter, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)
		_, err = adapter.Extract(extractorInput())
		require.Error(t, err)
		var abstain *openrouter.ClaimExtractorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "failed the claim-extractor draft contract")
	})

	t.Run("an unknown field in the structured draft is rejected (strict contract)", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: openrouter.RoleCompletion{
					Kind: openrouter.RoleCompletionKindCompletion,
					Structured: draftStructured(t, []map[string]any{
						{
							"claim_text": "Refunds require an idempotency key.",
							"candidate_anchor": map[string]any{
								"section_heading": "Payments",
							},
							"source_evidence": map[string]any{
								"quote":    "Refunds require an idempotency key.",
								"doc_path": "docs/payments.md",
							},
							"invented_field": "not part of the contract",
						},
					}),
					Telemetry: telemetryOf("z-ai/glm-4.7"),
				},
			}},
		}
		adapter, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)
		_, err = adapter.Extract(extractorInput())
		require.Error(t, err)
		var abstain *openrouter.ClaimExtractorAbstainError
		require.ErrorAs(t, err, &abstain)
		assert.Contains(t, err.Error(), "failed the claim-extractor draft contract")
	})

	t.Run("fails closed when the claim-extractor role is unbound in models.yml", func(t *testing.T) {
		_, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: &recordedClient{binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"}},
			Models: extractorModels(t, "writer"),
		})
		require.Error(t, err)
		assert.Regexp(t, `claim-extractor role is not bound`, err.Error())
		// The refusal is TYPED through the gateway error family with the
		// `role_binding_missing` class code (fail-closed before any request).
		var gatewayErr *openrouter.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, openrouter.CodeRoleBindingMissing, gatewayErr.Code)
	})

	t.Run("the output carries write-decision vocabulary nowhere (claims + anchors only)", func(t *testing.T) {
		client := &recordedClient{
			binding: openrouter.ResolvedRoleBinding{Primary: "z-ai/glm-4.7"},
			script: []scriptedCompletion{{
				Completion: openrouter.RoleCompletion{
					Kind: openrouter.RoleCompletionKindCompletion,
					Structured: draftStructured(t, []map[string]any{
						{
							"claim_text": "Refunds require an idempotency key.",
							"candidate_anchor": map[string]any{
								"section_heading": "Payments",
							},
							"source_evidence": map[string]any{
								"quote":    "Refunds require an idempotency key.",
								"doc_path": "docs/payments.md",
							},
						},
					}),
					Telemetry: telemetryOf("z-ai/glm-4.7"),
				},
			}},
		}
		adapter, err := openrouter.CreateClaimExtractorAdapter(openrouter.ClaimExtractorAdapterOptions{
			Client: client,
			Models: extractorModels(t, "claim-extractor"),
		})
		require.NoError(t, err)
		draft, err := adapter.Extract(extractorInput())
		require.NoError(t, err)

		data, err := json.Marshal(draft)
		require.NoError(t, err)
		var analysis map[string]any
		require.NoError(t, json.Unmarshal(data, &analysis))
		assert.Equal(t, []string{"claims"}, sortedKeys(analysis))
		claim := analysis["claims"].([]any)[0].(map[string]any)
		assert.Equal(t,
			[]string{"candidate_anchor", "claim_text", "extraction_model", "source_evidence"},
			sortedKeys(claim))
		for _, forbidden := range []string{`"apply"`, `"hunks"`, `"action"`, `"status":`, `"patch"`} {
			assert.NotContains(t, string(data), forbidden)
		}
	})
}

// sortedKeys returns a map's keys in sorted order (the TS key-set assertions).
func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
