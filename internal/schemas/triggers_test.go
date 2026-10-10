package schemas_test

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func TestTriggerTaxonomy_VersionPinned(t *testing.T) {
	// Bumped only with explicit migration.
	require.Equal(t, 1, schemas.TriggerTaxonomyVersion)
}

func TestTriggerTaxonomy_UniversalValuesPinnedVerbatim(t *testing.T) {
	// Verbatim from ARCH §8.3.1/§11.1.
	require.Equal(t, []schemas.UniversalTrigger{
		schemas.UniversalTriggerPublicBehavior,
		schemas.UniversalTriggerAPIChange,
		schemas.UniversalTriggerConfigurationChange,
		schemas.UniversalTriggerArchitectureChange,
		schemas.UniversalTriggerInternalRefactor,
		schemas.UniversalTriggerTestOnly,
	}, schemas.UniversalTriggerValues)

	for _, trigger := range schemas.UniversalTriggerValues {
		require.True(t, schemas.IsValidUniversalTrigger(string(trigger)))
	}
	// Unknown vocabulary is rejected (must be in UNIVERSAL_TRIGGER for v1;
	// unknown-vocabulary errors name TRIGGER_TAXONOMY_VERSION core-side).
	require.False(t, schemas.IsValidUniversalTrigger("whenever_possible"))
	require.False(t, schemas.IsValidUniversalTrigger("low_confidence"))
	require.False(t, schemas.IsValidUniversalTrigger(""))
}

func TestTriggerMap_ExampleValidatesAndMapsEveryUniversalTrigger(t *testing.T) {
	require.Empty(t, schemas.TriggerMapExample.Validate())
	require.Equal(t, 1, schemas.TriggerMapExample.TaxonomyVersion)
	require.Equal(t, map[string]string{
		"public_behavior":      "behavioral",
		"api_change":           "api_contract",
		"configuration_change": "configuration",
		"architecture_change":  "architecture",
		"internal_refactor":    "other",
		"test_only":            "test_only",
	}, schemas.TriggerMapExample.Mappings)

	// The example maps the complete universal vocabulary onto the engine
	// change_kind space.
	require.Len(t, schemas.TriggerMapExample.Mappings, len(schemas.UniversalTriggerValues))
	for _, trigger := range schemas.UniversalTriggerValues {
		require.Contains(t, schemas.TriggerMapExample.Mappings, string(trigger))
	}
}

func TestTriggerMap_ValidatesVersionKeysAndValues(t *testing.T) {
	badVersion := schemas.TriggerMap{
		TaxonomyVersion: 2,
		Mappings:        map[string]string{"public_behavior": "behavioral"},
	}
	require.NotEmpty(t, badVersion.Validate())

	badKey := schemas.TriggerMap{
		TaxonomyVersion: 1,
		Mappings:        map[string]string{"whenever_possible": "behavioral"},
	}
	require.NotEmpty(t, badKey.Validate())

	emptyValue := schemas.TriggerMap{
		TaxonomyVersion: 1,
		Mappings:        map[string]string{"public_behavior": ""},
	}
	require.NotEmpty(t, emptyValue.Validate())
}

func TestTriggerMap_RoundTripsThroughStrictDecode(t *testing.T) {
	raw, err := json.Marshal(schemas.TriggerMapExample)
	require.NoError(t, err)
	parsed, err := schemas.ParseTriggerMap(raw)
	require.NoError(t, err)
	require.Equal(t, schemas.TriggerMapExample, *parsed)

	// Unknown keys are rejected (z.strictObject).
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	m["extra"] = true
	strict, err := json.Marshal(m)
	require.NoError(t, err)
	_, err = schemas.ParseTriggerMap(strict)
	require.Error(t, err)
	require.Contains(t, err.Error(), "extra")
}
