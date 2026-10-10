package openrouter_test

// routing-policy_test.go — Go port of
// packages/openrouter/test/p12-t10-routing-policy-activation.test.ts and
// routing-activation-roundtrip-p12-t20.test.ts (P12-T10 + P12-T20; ARCH §10.3;
// PLAN §13.6).
//
// Black-box (package openrouter_test) with testify: routing policies are
// VERSIONED DECLARATIVE data parsed fail-closed, with a typed activation state
// machine — activation WITHOUT a shadow/canary evaluation report is VETOED,
// and an evaluation report alone does NOT auto-activate. No network, no clocks
// (injected instants).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

var policyV2 = map[string]any{
	"schema":     openrouter.RoutingPolicySchemaID,
	"version":    2,
	"source_ref": "org/routing.yml@abc123",
	"buckets": map[string]any{
		"low":    map[string]any{"primary": "z-ai/glm-4.7-air"},
		"medium": map[string]any{"primary": "z-ai/glm-4.7"},
		"high":   map[string]any{"primary": "anthropic/claude-sonnet-4.5"},
	},
}

const routingReportRef = "canary/replacement-shadow-v2/report-01"

func routingPolicyV2(t *testing.T) *schemas.RoutingPolicy {
	t.Helper()
	policy, err := openrouter.ParseRoutingPolicy(policyV2)
	require.NoError(t, err)
	return policy
}

func TestParseRoutingPolicy(t *testing.T) {
	t.Run("parses a valid versioned routing policy fail-closed and keeps the source ref", func(t *testing.T) {
		policy := routingPolicyV2(t)
		assert.Equal(t, 2, policy.Version)
		assert.Equal(t, "org/routing.yml@abc123", policy.SourceRef)
		assert.Equal(t, "anthropic/claude-sonnet-4.5", policy.Buckets.High.Primary)
	})

	t.Run("rejects malformed policy documents fail-closed (unknown keys, missing buckets, bad versions)", func(t *testing.T) {
		missingBucket := map[string]any{
			"schema":     openrouter.RoutingPolicySchemaID,
			"version":    2,
			"source_ref": "org/routing.yml@abc123",
			"buckets": map[string]any{
				"low":    map[string]any{"primary": "z-ai/glm-4.7-air"},
				"medium": map[string]any{"primary": "z-ai/glm-4.7"},
			},
		}
		emptyPrimary := map[string]any{
			"schema":     openrouter.RoutingPolicySchemaID,
			"version":    2,
			"source_ref": "org/routing.yml@abc123",
			"buckets": map[string]any{
				"low":    map[string]any{"primary": "z-ai/glm-4.7-air"},
				"medium": map[string]any{"primary": "z-ai/glm-4.7"},
				"high":   map[string]any{"primary": ""},
			},
		}
		cases := []struct {
			name  string
			input map[string]any
		}{
			{"missing bucket", missingBucket},
			{"unknown key", withKey(policyV2, "extra", true)},
			{"empty primary", emptyPrimary},
			{"zero version", withKey(policyV2, "version", 0)},
			{"empty source_ref", withKey(policyV2, "source_ref", "")},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				_, err := openrouter.ParseRoutingPolicy(testCase.input)
				require.Error(t, err)
				var policyErr *openrouter.RoutingPolicyError
				require.ErrorAs(t, err, &policyErr)
				assert.Equal(t, "routing_policy_invalid", policyErr.Code)
				assert.NotEmpty(t, policyErr.Issues)
				assert.Contains(t, err.Error(), "rejected fail-closed")
			})
		}
	})
}

// withKey copies a map with one key overridden (the TS object spread).
func withKey(source map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(source)+1)
	for name, item := range source {
		out[name] = item
	}
	out[key] = value
	return out
}

func TestResolveRoute(t *testing.T) {
	t.Run("resolves each bucket into the ResolvedRoleBinding shape the client already walks", func(t *testing.T) {
		policy := routingPolicyV2(t)
		high, err := openrouter.ResolveRoute(policy, "high", openrouter.RoutingRouteContext{Role: "patch-writer"})
		require.NoError(t, err)
		assert.Equal(t, "anthropic/claude-sonnet-4.5", high.Binding.Primary)
		assert.Equal(t, []string{}, high.Binding.Fallbacks)
		assert.Equal(t, 2, high.PolicyVersion)
		assert.Equal(t, "org/routing.yml@abc123", high.SourceRef)
		assert.Equal(t, schemas.RiskBucketHigh, high.Bucket)
		assert.Equal(t, "patch-writer", high.Role)

		low, err := openrouter.ResolveRoute(policy, "low", openrouter.RoutingRouteContext{Role: "decision-engine"})
		require.NoError(t, err)
		assert.Equal(t, "z-ai/glm-4.7-air", low.Binding.Primary)

		_, err = openrouter.ResolveRoute(policy, "critical", openrouter.RoutingRouteContext{Role: "patch-writer"})
		require.Error(t, err)
		var policyErr *openrouter.RoutingPolicyError
		require.ErrorAs(t, err, &policyErr)
		assert.Contains(t, err.Error(), "unknown risk bucket")

		_, err = openrouter.ResolveRoute(policy, "high", openrouter.RoutingRouteContext{Role: ""})
		require.Error(t, err)
		require.ErrorAs(t, err, &policyErr)
		assert.Contains(t, err.Error(), "context.role must be a non-empty string")
	})
}

func TestActivationDraftToEvaluated(t *testing.T) {
	t.Run("veto: activating a DRAFT policy (no evaluation report) is a typed error", func(t *testing.T) {
		draft := openrouter.DraftRoutingPolicyActivation(routingPolicyV2(t))
		require.Equal(t, schemas.RoutingPolicyDraft, draft.State)

		_, err := openrouter.ActivateRoutingPolicy(draft, "app://promotion-cli", "2026-10-09T00:00:00.000Z")
		require.Error(t, err)
		var veto *openrouter.RoutingPolicyActivationError
		require.ErrorAs(t, err, &veto)
		assert.Equal(t, "routing_activation_veto", veto.Code)
		assert.Equal(t, "draft", veto.From)
		assert.Equal(t, "active", veto.To)
		assert.Regexp(t, `VETO \(V-P12-11\)`, err.Error())
	})

	t.Run("markEvaluated attaches the report ref but leaves the policy NOT active — no auto-activation", func(t *testing.T) {
		draft := openrouter.DraftRoutingPolicyActivation(routingPolicyV2(t))
		evaluated, err := openrouter.MarkEvaluated(draft, routingReportRef)
		require.NoError(t, err)
		assert.Equal(t, schemas.RoutingPolicyEvaluated, evaluated.State)
		require.NotNil(t, evaluated.EvaluationReportRef)
		assert.Equal(t, routingReportRef, *evaluated.EvaluationReportRef)
		assert.Nil(t, evaluated.ActivatedBy)
		assert.Nil(t, evaluated.ActivatedAt)

		// The state machine's output parses back: the registered envelope
		// forbids an `active` envelope without the application-owned actor.
		forged := map[string]any{
			"policy":              policyV2,
			"state":               "active",
			"evaluationReportRef": routingReportRef,
		}
		_, err = openrouter.ParseRoutingPolicyActivation(forged)
		require.Error(t, err)
		var veto *openrouter.RoutingPolicyActivationError
		require.ErrorAs(t, err, &veto)
		assert.Regexp(t, `must not carry|is missing the required 'activatedBy'`, err.Error())
	})

	t.Run("activation requires the explicit application-owned actor (human/application act)", func(t *testing.T) {
		draft := openrouter.DraftRoutingPolicyActivation(routingPolicyV2(t))
		evaluated, err := openrouter.MarkEvaluated(draft, routingReportRef)
		require.NoError(t, err)

		_, err = openrouter.ActivateRoutingPolicy(evaluated, "", "2026-10-09T00:00:00.000Z")
		require.Error(t, err)
		var veto *openrouter.RoutingPolicyActivationError
		require.ErrorAs(t, err, &veto)
		assert.Contains(t, err.Error(), "application-owned actor")

		active, err := openrouter.ActivateRoutingPolicy(evaluated, "app://promotion-cli", "2026-10-09T00:00:00.000Z")
		require.NoError(t, err)
		assert.Equal(t, schemas.RoutingPolicyActive, active.State)
		require.NotNil(t, active.EvaluationReportRef)
		assert.Equal(t, routingReportRef, *active.EvaluationReportRef)
		require.NotNil(t, active.ActivatedBy)
		assert.Equal(t, "app://promotion-cli", *active.ActivatedBy)
		require.NotNil(t, active.ActivatedAt)
		assert.Equal(t, "2026-10-09T00:00:00.000Z", *active.ActivatedAt)
	})

	t.Run("veto: illegal transitions (active→active, retired→any, draft→retired, evaluated twice)", func(t *testing.T) {
		draft := openrouter.DraftRoutingPolicyActivation(routingPolicyV2(t))
		evaluated, err := openrouter.MarkEvaluated(draft, routingReportRef)
		require.NoError(t, err)
		active, err := openrouter.ActivateRoutingPolicy(evaluated, "app://promotion-cli", "2026-10-09T00:00:00.000Z")
		require.NoError(t, err)
		var veto *openrouter.RoutingPolicyActivationError

		_, err = openrouter.ActivateRoutingPolicy(active, "app://x", "2026-10-09T01:00:00.000Z")
		require.ErrorAs(t, err, &veto)
		_, err = openrouter.MarkEvaluated(active, routingReportRef)
		require.ErrorAs(t, err, &veto)

		retired, err := openrouter.RetireRoutingPolicy(active, "2026-10-10T00:00:00.000Z")
		require.NoError(t, err)
		assert.Equal(t, schemas.RoutingPolicyRetired, retired.State)
		require.NotNil(t, retired.RetiredAt)
		assert.Equal(t, "2026-10-10T00:00:00.000Z", *retired.RetiredAt)

		_, err = openrouter.MarkEvaluated(retired, routingReportRef)
		require.ErrorAs(t, err, &veto)
		_, err = openrouter.ActivateRoutingPolicy(retired, "app://x", "2026-10-10T01:00:00.000Z")
		require.ErrorAs(t, err, &veto)
		_, err = openrouter.RetireRoutingPolicy(draft, "2026-10-10T00:00:00.000Z")
		require.ErrorAs(t, err, &veto)
		assert.Contains(t, err.Error(), "cannot be retired from state 'draft'")

		// The evaluated-only state is DROPPED, never retired (V-P12-11).
		_, err = openrouter.RetireRoutingPolicy(evaluated, "2026-10-10T00:00:00.000Z")
		require.ErrorAs(t, err, &veto)
		assert.Regexp(t, `DROPPED, never retired`, err.Error())
	})
}

func TestRoutingPolicyActivationRoundTrip(t *testing.T) {
	// P12-T20 round-trip property: the state machine's output parses back AND
	// satisfies the registered envelope (`docsync.routing-policy/v1`).
	policy := routingPolicyV2(t)

	draft := openrouter.DraftRoutingPolicyActivation(policy)
	evaluated, err := openrouter.MarkEvaluated(draft, routingReportRef)
	require.NoError(t, err)
	active, err := openrouter.ActivateRoutingPolicy(evaluated, "app://promotion-cli", "2026-10-09T00:00:00.000Z")
	require.NoError(t, err)
	retired, err := openrouter.RetireRoutingPolicy(active, "2026-10-10T00:00:00.000Z")
	require.NoError(t, err)

	for name, envelope := range map[string]*schemas.RoutingPolicyActivation{
		"draft":     draft,
		"evaluated": evaluated,
		"active":    active,
		"retired":   retired,
	} {
		t.Run(name, func(t *testing.T) {
			// The registered envelope accepts the machine's output.
			require.Empty(t, envelope.Validate())
			// JSON.parse(JSON.stringify(activation)) → parseRoutingPolicyActivation.
			data, err := json.Marshal(envelope)
			require.NoError(t, err)
			parsed, err := openrouter.ParseRoutingPolicyActivation(data)
			require.NoError(t, err)
			require.Empty(t, parsed.Validate())
			assert.Equal(t, envelope.State, parsed.State)
			assert.Equal(t, envelope.Policy, parsed.Policy)
			if envelope.EvaluationReportRef != nil {
				require.NotNil(t, parsed.EvaluationReportRef)
				assert.Equal(t, *envelope.EvaluationReportRef, *parsed.EvaluationReportRef)
			}
			if envelope.ActivatedBy != nil {
				require.NotNil(t, parsed.ActivatedBy)
				assert.Equal(t, *envelope.ActivatedBy, *parsed.ActivatedBy)
			}
			if envelope.ActivatedAt != nil {
				require.NotNil(t, parsed.ActivatedAt)
				assert.Equal(t, *envelope.ActivatedAt, *parsed.ActivatedAt)
			}
			if envelope.RetiredAt != nil {
				require.NotNil(t, parsed.RetiredAt)
				assert.Equal(t, *envelope.RetiredAt, *parsed.RetiredAt)
			}
		})
	}

	t.Run("an unknown activation state is rejected fail-closed", func(t *testing.T) {
		_, err := openrouter.ParseRoutingPolicyActivation(map[string]any{
			"policy": policyV2,
			"state":  "zombie",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown routing-policy activation state: zombie")
	})

	t.Run("transitions never mutate the input envelope", func(t *testing.T) {
		original := openrouter.DraftRoutingPolicyActivation(policy)
		_, err := openrouter.MarkEvaluated(original, routingReportRef)
		require.NoError(t, err)
		assert.Equal(t, schemas.RoutingPolicyDraft, original.State)
		assert.Nil(t, original.EvaluationReportRef)
		assert.Nil(t, original.ActivatedBy)
		assert.Nil(t, original.ActivatedAt)
		assert.Nil(t, original.RetiredAt)
		// The veto does not carry the envelope into `active` either.
		_, err = openrouter.ActivateRoutingPolicy(original, "app://promotion-cli", "2026-10-09T00:00:00.000Z")
		require.Error(t, err)
		assert.Equal(t, schemas.RoutingPolicyDraft, original.State)
	})
}

/* --------------------------------------------- V-P12-11 end-to-end (canary) */

var (
	routingBaselineV1 = map[string]any{
		"schema":     openrouter.RoutingPolicySchemaID,
		"version":    1,
		"source_ref": "org/routing.yml@baseline",
		"buckets": map[string]any{
			"low":    map[string]any{"primary": "z-ai/glm-4.7-air"},
			"medium": map[string]any{"primary": "z-ai/glm-4.7"},
			"high":   map[string]any{"primary": "z-ai/glm-4.7"},
		},
	}
	routingCandidateV2 = map[string]any{
		"schema":     openrouter.RoutingPolicySchemaID,
		"version":    2,
		"source_ref": "org/routing.yml@candidate-stronger-high",
		"buckets": map[string]any{
			"low":    map[string]any{"primary": "z-ai/glm-4.7-air"},
			"medium": map[string]any{"primary": "z-ai/glm-4.7"},
			// The change: high-risk → STRONGER model (ARCH §10.1 diagram).
			"high": map[string]any{"primary": "anthropic/claude-sonnet-4.5"},
		},
	}
)

// bindingAssertingPort is the recorded evaluation port of the V-P12-11 flow: it
// fails loud when the evaluation is not run under the expected bindings.
type bindingAssertingPort struct{ calls int }

func (p *bindingAssertingPort) PortID() string { return "recorded-double/v1" }

func (p *bindingAssertingPort) RunCase(
	input openrouter.ReplacementCaseInput,
) (openrouter.ReplacementCaseResult, error) {
	p.calls++
	if input.OldModel != "z-ai/glm-4.7" || input.NewModel != "anthropic/claude-sonnet-4.5" {
		return openrouter.ReplacementCaseResult{}, &bindingAssertionError{
			oldModel: input.OldModel, newModel: input.NewModel,
		}
	}
	return openrouter.ReplacementCaseResult{
		OldOutcome: schemas.ShadowDecision{Action: schemas.ActionPatch},
		NewOutcome: schemas.ShadowDecision{Action: schemas.ActionPatch},
		InputHashes: schemas.ShadowRecordedInputHashes{
			ChangeAnalysis: strings.Repeat("a", 64),
			CandidateSet:   strings.Repeat("b", 64),
			DiffSubset:     strings.Repeat("c", 64),
		},
	}, nil
}

// bindingAssertionError is the port's loud failure (TS `unexpected bindings`).
type bindingAssertionError struct{ oldModel, newModel string }

func (e *bindingAssertionError) Error() string {
	return "unexpected bindings: " + e.oldModel + " → " + e.newModel
}

func TestV_P12_11RoutingPolicyCanary(t *testing.T) {
	t.Run("veto → shadow evaluation → report-attached evaluation → explicit activation → stronger-model routing → honest provenance", func(t *testing.T) {
		candidate, err := openrouter.ParseRoutingPolicy(routingCandidateV2)
		require.NoError(t, err)
		draft := openrouter.DraftRoutingPolicyActivation(candidate)

		// 1. VETO: promoting the new routing policy without an evaluation report.
		_, err = openrouter.ActivateRoutingPolicy(draft, "app://promotion-cli", replacementClock)
		require.Error(t, err)
		var veto *openrouter.RoutingPolicyActivationError
		require.ErrorAs(t, err, &veto)

		// 2. The swap rides the canary machinery: shadow-evaluate the high-risk
		//    binding change (old v1 high binding → new v2 high binding).
		baseline, err := openrouter.ParseRoutingPolicy(routingBaselineV1)
		require.NoError(t, err)
		oldRoute, err := openrouter.ResolveRoute(baseline, "high", openrouter.RoutingRouteContext{Role: "patch-writer"})
		require.NoError(t, err)
		newRoute, err := openrouter.ResolveRoute(candidate, "high", openrouter.RoutingRouteContext{Role: "patch-writer"})
		require.NoError(t, err)

		port := &bindingAssertingPort{}
		result, err := openrouter.EvaluateReplacementModel(openrouter.EvaluateReplacementModelInput{
			Role:              "patch-writer",
			Repo:              "acme/widgets",
			RunID:             "replacement-shadow-routing-v2-v1",
			ProductionVersion: baseline.Version,
			CandidateVersion:  candidate.Version,
			OldBinding:        schemas.ReplacementModelBinding{Primary: oldRoute.Binding.Primary},
			NewBinding:        schemas.ReplacementModelBinding{Primary: newRoute.Binding.Primary},
			Cases:             replacementCases("high-001", "high-002"),
			Port:              port,
			Clock:             func() string { return replacementClock },
			NewRecordID:       deterministicRecordId,
			TimeBoxDays:       14,
			MinAgreementRate:  0.9,
		})
		require.NoError(t, err)
		assert.Equal(t, "pass", result.Report.Verdict)
		for _, record := range result.CanaryRecords {
			assert.True(t, record.ShadowOnly)
		}

		// 3. The report alone does NOT activate: the policy is only
		//    'evaluated', carrying the report ref (V-P12-11
		//    report-before-promotion).
		reportRef := "canary/" + result.Report.RunID
		evaluated, err := openrouter.MarkEvaluated(draft, reportRef)
		require.NoError(t, err)
		assert.Equal(t, schemas.RoutingPolicyEvaluated, evaluated.State)
		assert.Nil(t, evaluated.ActivatedBy)

		// 4. The human/application act marks it active (application-owned actor).
		active, err := openrouter.ActivateRoutingPolicy(evaluated, "app://promotion-cli", "2026-10-09T12:00:00.000Z")
		require.NoError(t, err)
		assert.Equal(t, schemas.RoutingPolicyActive, active.State)
		require.NotNil(t, active.EvaluationReportRef)
		assert.Equal(t, reportRef, *active.EvaluationReportRef)

		// 5. The high-risk bucket DEMONSTRABLY routes to the stronger model.
		routedHigh, err := openrouter.ResolveRoute(&active.Policy, "high", openrouter.RoutingRouteContext{Role: "patch-writer"})
		require.NoError(t, err)
		assert.Equal(t, "anthropic/claude-sonnet-4.5", routedHigh.Binding.Primary)
		baselineHigh, err := openrouter.ResolveRoute(baseline, "high", openrouter.RoutingRouteContext{Role: "patch-writer"})
		require.NoError(t, err)
		assert.NotEqual(t, baselineHigh.Binding.Primary, routedHigh.Binding.Primary)

		// 6. Provenance names the ACTUAL routed model (telemetry-authoritative;
		//    configured passthrough would only apply with telemetry absent).
		detectorTelemetry := openrouter.RoleTelemetry{
			Role:           "decision-engine",
			RequestedModel: "z-ai/glm-4.7",
			// Routing/fallback sent the call elsewhere: provenance must name
			// THIS id.
			RoutedModel:  "openai/gpt-4.1-mini",
			Attempt:      1,
			FallbackUsed: true,
			LatencyMS:    11,
		}
		_ = detectorTelemetry
		require.NoError(t, openrouter.AssertActualModelProvenance(
			[]openrouter.ProvenanceModelClaim{
				{Role: "decision-engine", NamedModel: namedModel("openai/gpt-4.1-mini")},
				{Role: "patch-writer", NamedModel: namedModel(routedHigh.Binding.Primary)},
			},
			[]openrouter.ActualModelEvidence{
				{
					Role:            "decision-engine",
					ConfiguredModel: "z-ai/glm-4.7",
					Telemetry:       &openrouter.RoutedModelTelemetry{RoutedModel: "openai/gpt-4.1-mini"},
				},
				{Role: "patch-writer", ConfiguredModel: routedHigh.Binding.Primary},
			},
		))

		// …and naming the configured primary instead of the actually-routed id
		//    is fabricated provenance — fails closed (ARCH §10.6).
		fabrication := openrouter.AssertActualModelProvenance(
			[]openrouter.ProvenanceModelClaim{
				{Role: "decision-engine", NamedModel: namedModel("z-ai/glm-4.7")},
			},
			[]openrouter.ActualModelEvidence{
				{
					Role:            "decision-engine",
					ConfiguredModel: "z-ai/glm-4.7",
					Telemetry:       &openrouter.RoutedModelTelemetry{RoutedModel: "openai/gpt-4.1-mini"},
				},
			},
		)
		require.Error(t, fabrication)
		assert.Regexp(t, `fabricated provenance fails closed`, fabrication.Error())
	})
}
