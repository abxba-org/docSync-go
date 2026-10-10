package openrouter_test

// routing-provenance_test.go — Go port of
// packages/openrouter/test/p12-t12-actual-model-provenance.test.ts (P12-T12;
// ARCH §10.6; §3 rule 10; invariant 10): missing or fabricated model
// provenance fails closed — `AssertActualModelProvenance`.
//
// Decision/writer/verifier records MUST name the ACTUAL routed model ids:
// telemetry-present authoritative (V-P9-4/V-P2-11 posture, mirrored from
// provenance.go's ResolveRoutedModels — the EXISTING seam),
// configured-passthrough with declared deviation when telemetry is absent,
// NEVER a fabricated id.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
)

// routedModelTelemetry builds the telemetry slice the gate consumes.
func routedModelTelemetry(routed string) *openrouter.RoutedModelTelemetry {
	return &openrouter.RoutedModelTelemetry{RoutedModel: routed}
}

var provenanceEvidence = []openrouter.ActualModelEvidence{
	{
		Role:            "decision-engine",
		ConfiguredModel: "z-ai/glm-4.7",
		Telemetry:       routedModelTelemetry("openai/gpt-4.1-mini"),
	},
	{Role: "patch-writer", ConfiguredModel: "anthropic/claude-sonnet-4.5"},
	{
		Role:            "judge",
		ConfiguredModel: "openai/gpt-4.1",
		Telemetry:       routedModelTelemetry("openai/gpt-4.1"),
	},
}

// provenanceClaims builds the claim set (the TS named-record map).
func provenanceClaims(named map[string]*string) []openrouter.ProvenanceModelClaim {
	roles := []string{"decision-engine", "patch-writer", "judge", "drift-auditor"}
	claims := make([]openrouter.ProvenanceModelClaim, 0, len(named))
	for _, role := range roles {
		if model, ok := named[role]; ok {
			claims = append(claims, openrouter.ProvenanceModelClaim{Role: role, NamedModel: model})
		}
	}
	return claims
}

func namedModel(model string) *string { return &model }

func TestAssertActualModelProvenance(t *testing.T) {
	t.Run("accepts records naming the telemetry-routed id (fallback engaged) and the configured passthrough ids", func(t *testing.T) {
		require.NoError(t, openrouter.AssertActualModelProvenance(
			provenanceClaims(map[string]*string{
				"decision-engine": namedModel("openai/gpt-4.1-mini"),
				"patch-writer":    namedModel("anthropic/claude-sonnet-4.5"),
				"judge":           namedModel("openai/gpt-4.1"),
			}),
			provenanceEvidence,
		))
	})

	t.Run("veto: a record naming NO model id fails closed", func(t *testing.T) {
		err := assertProvenanceError(t,
			provenanceClaims(map[string]*string{
				"decision-engine": namedModel("openai/gpt-4.1-mini"),
				"patch-writer":    namedModel("anthropic/claude-sonnet-4.5"),
				"judge":           nil,
			}),
			provenanceEvidence,
		)
		assert.Equal(t, "actual_model_provenance_violation", err.Code)
		require.Len(t, err.Violations, 1)
		assert.Contains(t, err.Violations[0], "judge")
		assert.Contains(t, err.Violations[0], "names NO model id")
		assert.Contains(t, err.Violations[0], "(telemetry-authoritative routed id)")
		assert.Contains(t, err.Error(), "actual-model provenance enforcement failed (1 violation(s))")
	})

	t.Run("veto: a fabricated id (neither routed nor configured) fails closed", func(t *testing.T) {
		err := assertProvenanceError(t,
			provenanceClaims(map[string]*string{
				"decision-engine": namedModel("openai/gpt-4.1-mini"),
				"patch-writer":    namedModel("openai/gpt-5.1"),
				"judge":           namedModel("openai/gpt-4.1"),
			}),
			provenanceEvidence,
		)
		// A fabricated id on a role with telemetry names a model the call never
		// routed (ARCH §10.6).
		assert.Contains(t, err.Violations[0], "openai/gpt-5.1")
		assert.Contains(t, err.Violations[0], "but the ACTUAL routed model is")

		// Naming the CONFIGURED id when telemetry says otherwise is also a
		// fabrication: the routed id is the authority.
		err = assertProvenanceError(t,
			provenanceClaims(map[string]*string{
				"decision-engine": namedModel("z-ai/glm-4.7"),
				"patch-writer":    namedModel("anthropic/claude-sonnet-4.5"),
				"judge":           namedModel("openai/gpt-4.1"),
			}),
			provenanceEvidence,
		)
		assert.Regexp(t, `fabricated provenance fails closed`, err.Error())
	})

	t.Run("unverifiable provenance fails closed: claims without evidence, evidence without claims, duplicates", func(t *testing.T) {
		// A claim for a role with no evidence at all.
		err := assertProvenanceError(t,
			provenanceClaims(map[string]*string{
				"decision-engine": namedModel("openai/gpt-4.1-mini"),
				"drift-auditor":   namedModel("z-ai/glm-4.7-air"),
			}),
			provenanceEvidence,
		)
		assert.Contains(t, err.Violations[0], "no telemetry/configured evidence")

		// Evidence with no record naming a model for it.
		err = assertProvenanceError(t,
			provenanceClaims(map[string]*string{"judge": namedModel("openai/gpt-4.1")}),
			provenanceEvidence,
		)
		assert.Contains(t, err.Error(), "has evidence but no record names a model")

		// Duplicate claims.
		err = assertProvenanceError(t,
			[]openrouter.ProvenanceModelClaim{
				{Role: "decision-engine", NamedModel: namedModel("openai/gpt-4.1-mini")},
				{Role: "patch-writer", NamedModel: namedModel("anthropic/claude-sonnet-4.5")},
				{Role: "judge", NamedModel: namedModel("openai/gpt-4.1")},
				{Role: "decision-engine", NamedModel: namedModel("openai/gpt-4.1-mini")},
			},
			provenanceEvidence,
		)
		assert.Contains(t, err.Error(), "duplicate provenance claim for role")

		// Duplicate evidence.
		duplicateEvidence := append(append([]openrouter.ActualModelEvidence{}, provenanceEvidence...),
			openrouter.ActualModelEvidence{Role: "judge", ConfiguredModel: "openai/gpt-4.1"})
		err = assertProvenanceError(t,
			provenanceClaims(map[string]*string{
				"decision-engine": namedModel("openai/gpt-4.1-mini"),
				"patch-writer":    namedModel("anthropic/claude-sonnet-4.5"),
				"judge":           namedModel("openai/gpt-4.1"),
			}),
			duplicateEvidence,
		)
		assert.Contains(t, err.Error(), "duplicate evidence")

		// Evidence with no configured model to name.
		err = assertProvenanceError(t,
			provenanceClaims(map[string]*string{
				"decision-engine": namedModel("openai/gpt-4.1-mini"),
				"patch-writer":    namedModel("anthropic/claude-sonnet-4.5"),
				"judge":           namedModel("openai/gpt-4.1"),
			}),
			[]openrouter.ActualModelEvidence{
				{Role: "decision-engine", ConfiguredModel: ""},
			},
		)
		assert.Contains(t, err.Error(), "empty configuredModel")
	})

	t.Run("mirrors the EXISTING seam: expectations equal what ResolveRoutedModels feeds the provenance record", func(t *testing.T) {
		detectorTelemetry := openrouter.RoleTelemetry{
			Role:           "decision-engine",
			RequestedModel: "z-ai/glm-4.7",
			RoutedModel:    "openai/gpt-4.1-mini",
			Attempt:        1,
			FallbackUsed:   true,
			LatencyMS:      12,
		}
		routed := openrouter.ResolveRoutedModels(openrouter.RoutedModelSources{
			DetectorTelemetry:       &detectorTelemetry,
			ConfiguredDetectorModel: "z-ai/glm-4.7",
			ConfiguredWriterModel:   "anthropic/claude-sonnet-4.5",
		})
		expectations := openrouter.ResolveActualModelExpectations(provenanceEvidence)
		require.Equal(t, []openrouter.ActualModelExpectation{
			{Role: "decision-engine", ExpectedModel: "openai/gpt-4.1-mini", TelemetryAuthoritative: true},
			{Role: "patch-writer", ExpectedModel: "anthropic/claude-sonnet-4.5"},
			{Role: "judge", ExpectedModel: "openai/gpt-4.1", TelemetryAuthoritative: true},
		}, expectations)

		// The records the provenance builder would write from the SAME sources
		// pass the gate.
		require.NoError(t, openrouter.AssertActualModelProvenance(
			provenanceClaims(map[string]*string{
				"decision-engine": namedModel(routed.DetectorModel),
				"patch-writer":    namedModel(routed.WriterModel),
			}),
			[]openrouter.ActualModelEvidence{
				provenanceEvidence[0],
				{Role: "patch-writer", ConfiguredModel: "anthropic/claude-sonnet-4.5"},
			},
		))
	})
}

// assertProvenanceError runs the gate and requires the typed failure.
func assertProvenanceError(
	t *testing.T,
	claims []openrouter.ProvenanceModelClaim,
	evidence []openrouter.ActualModelEvidence,
) *openrouter.ActualModelProvenanceError {
	t.Helper()
	err := openrouter.AssertActualModelProvenance(claims, evidence)
	require.Error(t, err)
	var provenanceErr *openrouter.ActualModelProvenanceError
	require.ErrorAs(t, err, &provenanceErr)
	return provenanceErr
}
