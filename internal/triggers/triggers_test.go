package triggers_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/triggers"
)

var allSix = []string{
	"public_behavior",
	"api_change",
	"configuration_change",
	"architecture_change",
	"internal_refactor",
	"test_only",
}

var allSixValues = []schemas.UniversalTrigger{
	schemas.UniversalTriggerPublicBehavior,
	schemas.UniversalTriggerAPIChange,
	schemas.UniversalTriggerConfigurationChange,
	schemas.UniversalTriggerArchitectureChange,
	schemas.UniversalTriggerInternalRefactor,
	schemas.UniversalTriggerTestOnly,
}

func TestMapTriggers_V1IdentityOverUniversalVocabulary(t *testing.T) {
	result := triggers.MapTriggers(allSix)
	require.Equal(t, allSixValues, result.Mapped)
	require.Empty(t, result.UnknownValues)
}

func TestMapTriggers_AbsentListEmptyResult(t *testing.T) {
	require.Empty(t, triggers.MapTriggers(nil).Mapped)
	require.Empty(t, triggers.MapTriggers(nil).UnknownValues)
}

func TestMapTriggers_KeepsKnownOrderReportsUnknownInInputOrder(t *testing.T) {
	result := triggers.MapTriggers([]string{"api_change", "pay_flow_changed", "public_behavior"})
	require.Equal(t, []schemas.UniversalTrigger{schemas.UniversalTriggerAPIChange, schemas.UniversalTriggerPublicBehavior}, result.Mapped)
	require.Equal(t, []string{"pay_flow_changed"}, result.UnknownValues)
}

func TestMapTriggers_DedupesRepeatedUnknownValues(t *testing.T) {
	result := triggers.MapTriggers([]string{"ghost_trigger", "ghost_trigger", "api_change"})
	require.Equal(t, []string{"ghost_trigger"}, result.UnknownValues)
	require.Equal(t, []schemas.UniversalTrigger{schemas.UniversalTriggerAPIChange}, result.Mapped)
}

func TestMapTriggers_RejectsLookalikesNoFuzzyMatching(t *testing.T) {
	result := triggers.MapTriggers([]string{"public_behaviour", "API_CHANGE"})
	require.Equal(t, []string{"public_behaviour", "API_CHANGE"}, result.UnknownValues)
	require.Empty(t, result.Mapped)
}

func TestUnknownTriggerError_NamesTaxonomyVersionAndSchema(t *testing.T) {
	result := triggers.MapTriggers([]string{"pay_flow_changed"})
	err := triggers.NewUnknownTriggerError(result.UnknownValues, triggers.TriggerDirectionTriggers)
	require.Contains(t, err.Error(), `unknown trigger value "pay_flow_changed" for triggers`)
	require.Contains(t, err.Error(), "expected trigger-taxonomy version 1")
	require.Contains(t, err.Error(), "(decision schema docsync.trigger-map/v1)")
	require.Contains(t, err.Error(), "supported values: "+strings.Join(allSix, ", "))
}

func TestUnknownTriggerError_CarriesCodeVersionAndValues(t *testing.T) {
	result := triggers.MapTriggers([]string{"x1", "x2"})
	err := triggers.NewUnknownTriggerError(result.UnknownValues, triggers.TriggerDirectionIgnore)
	require.Equal(t, "EDOCSYNC_TRIGGER_UNKNOWN", err.Code())
	require.Equal(t, "EDOCSYNC_TRIGGER_UNKNOWN", triggers.EDOCSYNC_TRIGGER_UNKNOWN)
	require.Equal(t, 1, err.ExpectedTaxonomyVersion)
	require.Equal(t, []string{"x1", "x2"}, err.UnknownValues)
}

func TestUnknownTriggerError_NamesIgnoreDirection(t *testing.T) {
	err := triggers.NewUnknownTriggerError([]string{"nope"}, triggers.TriggerDirectionIgnore)
	require.Contains(t, err.Error(), `unknown trigger value "nope" for ignore`)
	require.Contains(t, err.Error(), "version 1")
	require.Contains(t, err.Error(), "docsync.trigger-map/v1")
}

func TestSchemaConstantsExposedConsistently(t *testing.T) {
	require.Equal(t, "docsync.trigger-map", triggers.TRIGGER_MAP_SCHEMA_ID)
	require.Equal(t, "docsync.trigger-map/v1", triggers.TRIGGER_MAP_SCHEMA_REF)
}

func TestDescribeTriggerTaxonomy_ListsSixValuesVersion1(t *testing.T) {
	description := triggers.DescribeTriggerTaxonomy()
	require.Equal(t, 1, description.TaxonomyVersion)
	require.Equal(t, "docsync.trigger-map/v1", description.SchemaRef)
	require.Len(t, description.Values, 6)
	require.Equal(t, allSixValues, description.Values)
}

func TestRegistryCoupling_TriggerMapRegisteredAtTaxonomyVersion(t *testing.T) {
	// The mapping layer pins the registered taxonomy.
	require.Equal(t, 1, triggers.DescribeTriggerTaxonomy().TaxonomyVersion)

	registered := schemas.GetSchema(triggers.TRIGGER_MAP_SCHEMA_ID)
	require.NotNil(t, registered)
	require.Equal(t, 1, registered.Version)

	// The registered example is schema-valid …
	require.Empty(t, schemas.TriggerMapExample.Validate())
	// … and round-trips through the strict trigger-map parser.
	data, err := json.Marshal(schemas.TriggerMapExample)
	require.NoError(t, err)
	parsed, err := schemas.ParseTriggerMap(data)
	require.NoError(t, err)
	require.Equal(t, schemas.TriggerMapExample, *parsed)
}
