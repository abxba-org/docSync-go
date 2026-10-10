package openrouter_test

// provenance_test.go — Go port of packages/openrouter/test/v-p9-4-routed-provenance.test.ts
// (P9-T02; V-P9-4; ARCH §10.6 — the record names what actually ran). The pure
// resolveRoutedModels decision cases are ported here; the end-to-end client walk
// + @docsync/core buildProvenanceRecord integration lives in the client/core
// suites (network-free recorded doubles are out of this unit's file set). The
// additive D3 pin (RoutedModelsContentHash) is exercised too.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/openrouter"
)

const (
	detectorConfigured = "z-ai/glm-4.7"
	writerConfigured   = "anthropic/claude-sonnet-4.5"
)

func TestVP94_RoutedModelProvenance_NamesActuallyRouted(t *testing.T) {
	t.Run("when a fallback engages, routed names the actually-routed id, not the configured primary", func(t *testing.T) {
		detector := &openrouter.RoleTelemetry{RoutedModel: "openai/gpt-4.1-mini"}
		writer := &openrouter.RoleTelemetry{RoutedModel: "google/gemini-2.5-flash"}
		routed := openrouter.ResolveRoutedModels(openrouter.RoutedModelSources{
			DetectorTelemetry:       detector,
			WriterTelemetry:         writer,
			ConfiguredDetectorModel: detectorConfigured,
			ConfiguredWriterModel:   writerConfigured,
		})
		assert.Equal(t, "openai/gpt-4.1-mini", routed.DetectorModel)
		assert.Equal(t, "google/gemini-2.5-flash", routed.WriterModel)
		assert.NotEqual(t, detectorConfigured, routed.DetectorModel)
		assert.NotEqual(t, writerConfigured, routed.WriterModel)
	})

	t.Run("contrast: with no fallback engaged, routed == configured", func(t *testing.T) {
		detector := &openrouter.RoleTelemetry{RoutedModel: detectorConfigured}
		writer := &openrouter.RoleTelemetry{RoutedModel: writerConfigured}
		routed := openrouter.ResolveRoutedModels(openrouter.RoutedModelSources{
			DetectorTelemetry:       detector,
			WriterTelemetry:         writer,
			ConfiguredDetectorModel: detectorConfigured,
			ConfiguredWriterModel:   writerConfigured,
		})
		assert.Equal(t, detectorConfigured, routed.DetectorModel)
		assert.Equal(t, writerConfigured, routed.WriterModel)
	})

	t.Run("telemetry absent: configured passthrough (declared deviation) — no fabricated fallback id", func(t *testing.T) {
		routed := openrouter.ResolveRoutedModels(openrouter.RoutedModelSources{
			ConfiguredDetectorModel: detectorConfigured,
			ConfiguredWriterModel:   writerConfigured,
		})
		assert.Equal(t, detectorConfigured, routed.DetectorModel)
		assert.Equal(t, writerConfigured, routed.WriterModel)
	})

	t.Run("empty routed_model degrades to the configured passthrough (never a fabricated id)", func(t *testing.T) {
		routed := openrouter.ResolveRoutedModels(openrouter.RoutedModelSources{
			DetectorTelemetry:       &openrouter.RoleTelemetry{RoutedModel: ""},
			WriterTelemetry:         &openrouter.RoleTelemetry{RoutedModel: ""},
			ConfiguredDetectorModel: detectorConfigured,
			ConfiguredWriterModel:   writerConfigured,
		})
		assert.Equal(t, detectorConfigured, routed.DetectorModel)
		assert.Equal(t, writerConfigured, routed.WriterModel)
	})
}

func TestD3_RoutedModelsContentHash_PinsModelBinding(t *testing.T) {
	base := openrouter.RoutedModels{DetectorModel: "openai/gpt-4.1-mini", WriterModel: "google/gemini-2.5-flash"}
	h1, err := openrouter.RoutedModelsContentHash(base)
	require.NoError(t, err)
	require.NotEmpty(t, h1)

	// Deterministic: same pair -> same canonical bytes -> same hash.
	h2, err := openrouter.RoutedModelsContentHash(base)
	require.NoError(t, err)
	assert.Equal(t, h1, h2)

	// Detection-write model swap changes the pin (D9: a model swap is a policy change).
	swapped := openrouter.RoutedModels{DetectorModel: "z-ai/glm-4.7", WriterModel: base.WriterModel}
	h3, err := openrouter.RoutedModelsContentHash(swapped)
	require.NoError(t, err)
	assert.NotEqual(t, h1, h3)
}
