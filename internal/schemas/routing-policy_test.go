package schemas_test

// Ported from packages/schemas/test/routing-policy-schema-p12-t20.test.ts
// (P12-T20; ARCH §10.1, §10.3; R2; D9; V-P0-2, V-P12-11) — black-box,
// testify. The replacement-evaluation describe block of that TS file is
// ported in replacement-evaluation_test.go; the registry describe blocks
// (SCHEMA_REGISTRY / getSchema / zodToJsonSchema) are intentionally NOT
// ported — the registry barrel belongs to the registry unit's file.
//
// Helper names are routingPolicy-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ helpers */

func routingPolicyMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func routingPolicyWire(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

// routingPolicyClone renders a value as a generic JSON map so tests can
// spread copies ({...BASE, ...}).
func routingPolicyClone(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(routingPolicyMarshal(t, v), &m))
	return m
}

// routingPolicyMutate clones a base object, applies a mutation to the map,
// and returns the wire bytes.
func routingPolicyMutate(t *testing.T, base any, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := routingPolicyClone(t, base)
	mutate(m)
	return routingPolicyWire(t, m)
}

func routingPolicyParse(t *testing.T, data []byte) error {
	t.Helper()
	_, err := schemas.ParseRoutingPolicy(data)
	return err
}

func routingPolicyActivationParse(t *testing.T, data []byte) error {
	t.Helper()
	_, err := schemas.ParseRoutingPolicyActivation(data)
	return err
}

// routingPolicyBindingParse exercises ROUTING_POLICY_BINDING_SCHEMA alone
// (the TS suite parses standalone bindings for the params/provider_routing
// strictness checks).
func routingPolicyBindingParse(t *testing.T, binding map[string]any) error {
	t.Helper()
	var b schemas.RoutingPolicyBinding
	if err := schemas.StrictDecode(routingPolicyWire(t, binding), &b); err != nil {
		return err
	}
	if issues := b.Validate(); len(issues) > 0 {
		return schemas.NewValidationError(issues...)
	}
	return nil
}

// routingPolicyMinimal is a minimal-but-complete routing policy: every
// bucket bound explicitly (MINIMAL_POLICY).
func routingPolicyMinimal() map[string]any {
	return map[string]any{
		"schema":     schemas.RoutingPolicySchemaID,
		"version":    1,
		"source_ref": "org/routing.yml@baseline",
		"buckets": map[string]any{
			"low":    map[string]any{"primary": "z-ai/glm-4.7-air"},
			"medium": map[string]any{"primary": "z-ai/glm-4.7"},
			"high":   map[string]any{"primary": "z-ai/glm-4.7"},
		},
	}
}

// routingPolicyIssueAt finds an issue whose path starts with path and whose
// message contains needle (mirrors the TS expectIssue helper).
func routingPolicyIssueAt(t *testing.T, err error, path []string, needle string) {
	t.Helper()
	require.Error(t, err)
	var ve *schemas.ValidationError
	require.ErrorAs(t, err, &ve)
	for _, iss := range ve.Issues {
		if len(iss.Path) < len(path) {
			continue
		}
		match := true
		for i, seg := range path {
			if iss.Path[i] != seg {
				match = false
				break
			}
		}
		if match && strings.Contains(iss.Message, needle) {
			return
		}
	}
	require.Failf(t, "issue not found", "expected an issue at path %v containing %q, got %v", path, needle, ve.Issues)
}

/* ------------------------------------------------------------------- tests */

func TestP12T20_RoutingPolicyKeepsPromotedIDAndClosedRiskBuckets(t *testing.T) {
	assert.Equal(t, "docsync.routing-policy/v1", schemas.RoutingPolicySchemaID)
	assert.Equal(t, []schemas.RiskBucket{"low", "medium", "high"}, schemas.RiskBuckets)
}

func TestP12T20_RoutingPolicyValidatesTheExampleAndRoundTrips(t *testing.T) {
	require.NoError(t, routingPolicyParse(t, routingPolicyMarshal(t, schemas.RoutingPolicyExample)))
	roundTripped := routingPolicyClone(t, schemas.RoutingPolicyExample)
	require.NoError(t, routingPolicyParse(t, routingPolicyWire(t, roundTripped)))
}

func TestP12T20_RoutingPolicyAcceptsMinimalAndExtendedBindingFields(t *testing.T) {
	minimal := routingPolicyMinimal()
	require.NoError(t, routingPolicyParse(t, routingPolicyWire(t, minimal)))
	extended := routingPolicyMutate(t, minimal, func(m map[string]any) {
		buckets := m["buckets"].(map[string]any)
		buckets["high"] = map[string]any{
			"primary":   "anthropic/claude-sonnet-4.5",
			"fallbacks": []any{"anthropic/claude-3.7-sonnet"},
			"params":    map[string]any{"temperature": 0, "max_tokens": 4096, "structured_output": "native"},
			"provider_routing": map[string]any{
				"provider": map[string]any{"order": []any{"anthropic", "bedrock"}, "allow_fallbacks": true},
			},
		}
	})
	require.NoError(t, routingPolicyParse(t, extended))

	// The binding is strict: unknown keys and out-of-range values reject.
	for _, params := range []map[string]any{
		{"temperature": 3},
		{"max_tokens": 0},
		{"structured_output": "native "},
		{"unknown": true},
	} {
		binding := map[string]any{"primary": "z-ai/glm-4.7-air", "params": params}
		assert.Error(t, routingPolicyBindingParse(t, binding), "params %v must reject", params)
	}
	for _, providerRouting := range []map[string]any{
		{},
		{"provider": map[string]any{}},
		{"provider": map[string]any{"order": []any{}}},
		{"provider": map[string]any{"order": []any{"anthropic"}, "allow_fallbacks": "yes"}},
	} {
		binding := map[string]any{"primary": "z-ai/glm-4.7-air", "provider_routing": providerRouting}
		assert.Error(t, routingPolicyBindingParse(t, binding), "provider_routing %v must reject", providerRouting)
	}
}

func TestP12T20_RoutingPolicyRejectsMalformedPoliciesFailClosed(t *testing.T) {
	minimal := routingPolicyMinimal()
	missingBucket := routingPolicyMinimal()
	buckets := missingBucket["buckets"].(map[string]any)
	delete(buckets, "high")

	invalid := [][]byte{
		routingPolicyMutate(t, minimal, func(m map[string]any) { m["extra"] = true }),
		routingPolicyMutate(t, minimal, func(m map[string]any) { m["version"] = 0 }),
		routingPolicyMutate(t, minimal, func(m map[string]any) { m["version"] = 1.5 }),
		routingPolicyMutate(t, minimal, func(m map[string]any) { m["schema"] = "docsync.routing-policy/v2" }),
		routingPolicyMutate(t, minimal, func(m map[string]any) { m["source_ref"] = "" }),
		routingPolicyMutate(t, minimal, func(m map[string]any) {
			m["buckets"] = map[string]any{
				"low":      buckets["low"],
				"medium":   buckets["medium"],
				"high":     buckets["high"],
				"critical": map[string]any{"primary": "z-ai/glm-4.7"},
			}
		}),
		routingPolicyWire(t, missingBucket),
		routingPolicyMutate(t, minimal, func(m map[string]any) {
			m["buckets"] = map[string]any{
				"low":    map[string]any{"primary": ""},
				"medium": buckets["medium"],
				"high":   buckets["high"],
			}
		}),
		routingPolicyMutate(t, minimal, func(m map[string]any) {
			m["buckets"] = map[string]any{
				"low":    map[string]any{"primary": "z-ai/glm-4.7-air", "fallbacks": []any{""}},
				"medium": buckets["medium"],
				"high":   buckets["high"],
			}
		}),
		routingPolicyMutate(t, minimal, func(m map[string]any) {
			m["buckets"] = map[string]any{
				"low":    map[string]any{"primary": "m", "extra": 1},
				"medium": buckets["medium"],
				"high":   buckets["high"],
			}
		}),
	}
	for i, policy := range invalid {
		assert.Error(t, routingPolicyParse(t, policy), "invalid policy #%d must reject", i)
	}
}

func TestP12T20_ActivationValidatesTheExample(t *testing.T) {
	example := schemas.RoutingPolicyActivationExample
	require.NoError(t, routingPolicyActivationParse(t, routingPolicyMarshal(t, example)))
	assert.Equal(t, schemas.RoutingPolicyActive, example.State)
	roundTripped := routingPolicyClone(t, example)
	require.NoError(t, routingPolicyActivationParse(t, routingPolicyWire(t, roundTripped)))
}

func TestP12T20_DraftCarriesNothingEvaluatedCarriesOnlyTheReportRef(t *testing.T) {
	draft := map[string]any{"policy": routingPolicyMinimal(), "state": "draft"}
	require.NoError(t, routingPolicyActivationParse(t, routingPolicyWire(t, draft)))
	evaluated := map[string]any{
		"policy":              routingPolicyMinimal(),
		"state":               "evaluated",
		"evaluationReportRef": "canary/replacement-shadow-routing-v2-v1/report-01",
	}
	require.NoError(t, routingPolicyActivationParse(t, routingPolicyWire(t, evaluated)))

	var a schemas.RoutingPolicyActivation
	require.NoError(t, schemas.StrictDecode(routingPolicyWire(t, evaluated), &a))
	assert.Equal(t, schemas.RoutingPolicyEvaluated, a.State)
	assert.Nil(t, a.ActivatedBy)
	assert.Nil(t, a.ActivatedAt)
}

func TestP12T20_RetiredIsTerminalAndCarriesTheFullPriorLifecycle(t *testing.T) {
	retired := map[string]any{
		"policy":              routingPolicyMinimal(),
		"state":               "retired",
		"evaluationReportRef": "canary/replacement-shadow-routing-v2-v1/report-01",
		"activatedBy":         "app://promotion-cli",
		"activatedAt":         "2026-10-09T12:00:00.000Z",
		"retiredAt":           "2026-10-10T00:00:00.000Z",
	}
	require.NoError(t, routingPolicyActivationParse(t, routingPolicyWire(t, retired)))
	var a schemas.RoutingPolicyActivation
	require.NoError(t, schemas.StrictDecode(routingPolicyWire(t, retired), &a))
	assert.Equal(t, "2026-10-10T00:00:00.000Z", *a.RetiredAt)
}

func TestP12T20_ActivationRejectsEnvelopesThatViolateTheStateInvariants(t *testing.T) {
	minimal := routingPolicyMinimal()
	draft := map[string]any{"policy": minimal, "state": "draft"}
	evaluated := map[string]any{
		"policy":              minimal,
		"state":               "evaluated",
		"evaluationReportRef": "canary/x/report-01",
	}
	activeWithoutActor := map[string]any{
		"policy":              minimal,
		"state":               "active",
		"evaluationReportRef": "canary/x/report-01",
	}
	invalid := [][]byte{
		routingPolicyWire(t, map[string]any{"policy": minimal, "state": "promoted"}),
		routingPolicyWire(t, map[string]any{"policy": minimal}),
		routingPolicyMutate(t, draft, func(m map[string]any) {
			m["evaluationReportRef"] = "canary/x/report-01"
		}),
		routingPolicyMutate(t, draft, func(m map[string]any) { m["activatedBy"] = "app://x" }),
		routingPolicyMutate(t, draft, func(m map[string]any) { m["retiredAt"] = "2026-10-10T00:00:00.000Z" }),
		routingPolicyMutate(t, evaluated, func(m map[string]any) { delete(m, "evaluationReportRef") }),
		routingPolicyMutate(t, evaluated, func(m map[string]any) { m["evaluationReportRef"] = "" }),
		routingPolicyMutate(t, evaluated, func(m map[string]any) { m["retiredAt"] = "2026-10-10T00:00:00.000Z" }),
		routingPolicyWire(t, activeWithoutActor),
		routingPolicyMutate(t, activeWithoutActor, func(m map[string]any) { delete(m, "activatedBy") }),
		routingPolicyMutate(t, activeWithoutActor, func(m map[string]any) { delete(m, "activatedAt") }),
		routingPolicyMutate(t, activeWithoutActor, func(m map[string]any) { m["retiredAt"] = "2026-10-10T00:00:00.000Z" }),
		routingPolicyMutate(t, evaluated, func(m map[string]any) { m["state"] = "retired" }),
		routingPolicyMutate(t, schemas.RoutingPolicyActivationExample, func(m map[string]any) {
			policy := m["policy"].(map[string]any)
			policy["extra"] = true
		}),
		routingPolicyMutate(t, schemas.RoutingPolicyActivationExample, func(m map[string]any) { m["state"] = "draft" }),
	}
	for i, envelope := range invalid {
		assert.Error(t, routingPolicyActivationParse(t, envelope), "invalid envelope #%d must reject", i)
	}
}

func TestP12T20_ActivationStateMessagesMatchTheTSSuperRefine(t *testing.T) {
	minimal := routingPolicyMinimal()
	draft := map[string]any{"policy": minimal, "state": "draft"}
	evaluated := map[string]any{
		"policy":              minimal,
		"state":               "evaluated",
		"evaluationReportRef": "canary/x/report-01",
	}
	active := map[string]any{
		"policy":              minimal,
		"state":               "active",
		"evaluationReportRef": "canary/x/report-01",
	}

	err := routingPolicyActivationParse(t, routingPolicyMutate(t, draft, func(m map[string]any) {
		m["evaluationReportRef"] = "canary/x/report-01"
	}))
	routingPolicyIssueAt(t, err, []string{"evaluationReportRef"},
		"routing-policy activation in state 'draft' must not carry a report ref")

	err = routingPolicyActivationParse(t, routingPolicyWire(t, map[string]any{"policy": minimal, "state": "evaluated"}))
	routingPolicyIssueAt(t, err, []string{"evaluationReportRef"},
		"routing-policy activation in state 'evaluated' requires the shadow/canary evaluation report ref (V-P12-11)")

	err = routingPolicyActivationParse(t, routingPolicyWire(t, active))
	routingPolicyIssueAt(t, err, []string{"activatedBy"},
		"routing-policy activation in state 'active' requires the application-owned actor (§3 rule 7)")
	routingPolicyIssueAt(t, err, []string{"activatedAt"},
		"routing-policy activation in state 'active' requires the activation instant")

	err = routingPolicyActivationParse(t, routingPolicyMutate(t, evaluated, func(m map[string]any) {
		m["state"] = "retired"
	}))
	routingPolicyIssueAt(t, err, []string{"retiredAt"},
		"routing-policy activation in state 'retired' requires the retirement instant")
}
