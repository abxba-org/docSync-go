package openrouter

// provenance.go — Go port of packages/openrouter/src/provenance.ts (P9-T02;
// V-P9-4; ARCH §10.6, §12.5): resolveRoutedModels maps the per-call OpenRouter
// telemetry into the {detectorModel, writerModel} pair the @docsync/core
// provenance builder consumes. record.ts's header notes that reflecting fallback
// outcomes is the TRANSPORT layer's job — this file is exactly that mapping, as
// a small pure function.
//
// Decision rule (D3 provenance pin — ARCH §10.6):
//   - Telemetry PRESENT → telemetry.routed_model is AUTHORITATIVE: it is the
//     response-body `model` field when the gateway served one, else the walk
//     target that actually ran (V-P2-11) — including a fallback target when one
//     engaged. The provenance record names the ACTUALLY-ROUTED id, never the
//     configured primary.
//   - Telemetry ABSENT → DECLARED DEVIATION: the configured model is the only id
//     that exists for the call, so it is carried through verbatim. No fallback
//     id is EVER fabricated here — an absent-telemetry passthrough records the
//     configured model, and the absence itself is the declared deviation.
//
// Pure: no client, no clock, no network, no mutation. Role-name spelling
// (decision-engine vs writer, ARCH §10.2 vs Phase-0 registry) is the caller's
// models.yml concern — telemetry arrives pre-attached per role.
//
// RoleTelemetry comes from the sibling client.go barrel (same package);
// RoutedModels is the seam type declared in models.go. RoutedModelsContentHash
// is an ADDITIVE Go convenience (no TS counterpart) that pins the resolved pair
// through docutil.ContentHash for downstream actual-model provenance
// enforcement (P12-T12) — canonical bytes first, then sha256-hex (OD-2/D10).

import "github.com/DUDE616/docSync-go/internal/docutil"

// RoutedModelSources are the inputs for ResolveRoutedModels.
type RoutedModelSources struct {
	// DetectorTelemetry is the per-call telemetry of the detector
	// (decision-engine) call, when present (nil == TS undefined).
	DetectorTelemetry *RoleTelemetry
	// WriterTelemetry is the per-call telemetry of the writer (patch-writer)
	// call, when present (nil == TS undefined).
	WriterTelemetry *RoleTelemetry
	// ConfiguredDetectorModel is the configured primary for the detector role
	// (what was asked for).
	ConfiguredDetectorModel string
	// ConfiguredWriterModel is the configured primary for the writer role (what
	// was asked for).
	ConfiguredWriterModel string
}

// routedOrConfigured returns the authoritative routed id when telemetry names
// one, else the configured passthrough. An absent (or empty) routed_model
// cannot name a routed model — it degrades to the configured id (the declared
// deviation above), honestly, rather than to a fabricated id.
func routedOrConfigured(telemetry *RoleTelemetry, configured string) string {
	if telemetry != nil && telemetry.RoutedModel != "" {
		return telemetry.RoutedModel
	}
	return configured
}

// ResolveRoutedModels maps per-call routed-model telemetry evidence into the
// provenance builder's RoutedModels shape (fail-closed downstream: the
// provenance builder re-validates every field it consumes).
func ResolveRoutedModels(input RoutedModelSources) RoutedModels {
	return RoutedModels{
		DetectorModel: routedOrConfigured(input.DetectorTelemetry, input.ConfiguredDetectorModel),
		WriterModel:   routedOrConfigured(input.WriterTelemetry, input.ConfiguredWriterModel),
	}
}

// RoutedModelsContentHash is an ADDITIVE Go convenience (no TS counterpart): it
// canonicalizes the resolved routed-model pair and returns its sha256-hex via
// docutil.ContentHash. It gives the D3 pin a stable, tamper-evident identity
// for actual-model provenance enforcement and model-swap detection (D9: a model
// swap changes the hash — a policy change, shadow/canary first). Canonical
// bytes come from docutil.CanonicalJSON so the hash is deterministic across
// processes and runs.
func RoutedModelsContentHash(models RoutedModels) (string, error) {
	canonical, err := docutil.CanonicalJSON(map[string]any{
		"detectorModel": models.DetectorModel,
		"writerModel":   models.WriterModel,
	})
	if err != nil {
		return "", err
	}
	return docutil.ContentHash(canonical), nil
}
