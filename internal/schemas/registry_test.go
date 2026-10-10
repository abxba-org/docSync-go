package schemas_test

// Ported from packages/schemas/test/registry.test.ts (V-P0-2) and
// packages/schemas/test/smoke.test.ts.
//
// The TS registry test asserted (a) the required ids are present, (b) ids are
// unique, (c) every example validates against its schema, (d) every version is
// integer 1, (e) every description is non-empty, plus getSchema resolution and
// SUPPORTED_MANIFEST_VERSIONS. The JSON-Schema export assertion has no Go
// counterpart (no zod-to-json-schema pipeline) and is intentionally dropped;
// the Go equivalents of "validates against its schema" are entry.Validate()
// (the schema refinements) and entry.Parse (the strictObject wire contract).

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// requiredIDs mirrors REQUIRED_IDS in registry.test.ts.
var requiredIDs = []string{
	"docsync.manifest",
	"docsync.decision",
	"docsync.change-analysis",
	"docsync.doc-patch",
	"docsync.verification-report",
	"docsync.provenance",
	"docsync.run-record",
	"docsync.baseline",
	"docsync.claim",
	"docsync.doc-state",
	"docsync.audit-cursor",
	"docsync.policy",
	"docsync.models",
	"docsync.trigger-map",
}

// registrySize is the closed-set size of SCHEMA_REGISTRY in registry.ts —
// the registry panics on duplicate ids, so a drift here is a porting bug.
const registrySize = 43

func TestRegistry_NonEmptyAndContainsEveryRequiredID(t *testing.T) {
	registry := schemas.SchemaRegistry()
	require.Greater(t, len(registry), 0)
	require.Len(t, registry, registrySize)

	ids := make(map[string]bool, len(registry))
	for _, entry := range registry {
		ids[entry.ID] = true
	}
	for _, required := range requiredIDs {
		require.Contains(t, ids, required)
	}
}

func TestRegistry_HasUniqueIDs(t *testing.T) {
	registry := schemas.SchemaRegistry()
	seen := make(map[string]bool, len(registry))
	for _, entry := range registry {
		require.False(t, seen[entry.ID], "duplicate registry id %s", entry.ID)
		seen[entry.ID] = true
	}
}

func TestRegistry_EveryExampleValidatesAgainstItsSchema(t *testing.T) {
	for _, entry := range schemas.SchemaRegistry() {
		require.NotNil(t, entry.Validate, "entry %s carries no Validate closure", entry.ID)
		issues := entry.Validate()
		require.Empty(t, issues, "entry %s example failed validation: %v", entry.ID, issues)
	}
}

func TestRegistry_AllVersionsAreIntegerOne(t *testing.T) {
	for _, entry := range schemas.SchemaRegistry() {
		require.Equal(t, 1, entry.Version, "entry %s must register version 1", entry.ID)
	}
}

func TestRegistry_EveryEntryCarriesNonEmptyDescription(t *testing.T) {
	for _, entry := range schemas.SchemaRegistry() {
		require.NotEmpty(t, entry.Description, "entry %s has an empty description", entry.ID)
	}
}

func TestRegistry_EveryExampleRoundTripsThroughItsStrictParser(t *testing.T) {
	// Golden data: the registered example must survive its own strict parser
	// (z.strictObject wire contract) byte-for-byte after serialization.
	for _, entry := range schemas.SchemaRegistry() {
		data, err := json.Marshal(entry.Example)
		require.NoError(t, err, "entry %s example not serializable", entry.ID)
		require.NoError(t, entry.Parse(data), "entry %s example does not re-parse", entry.ID)
		typed, err := entry.ParseTyped(data)
		require.NoError(t, err, "entry %s example does not re-parse typed", entry.ID)
		require.NotNil(t, typed, "entry %s ParseTyped returned nil", entry.ID)
	}
}

func TestGetSchema_ResolvesByIDAndVersion(t *testing.T) {
	registered := schemas.GetSchema("docsync.manifest")
	require.NotNil(t, registered)
	require.Equal(t, "docsync.manifest", registered.ID)
	require.Equal(t, 1, registered.Version)

	byVersion := schemas.GetSchema("docsync.decision", 1)
	require.NotNil(t, byVersion)
	require.Equal(t, "docsync.decision", byVersion.ID)

	require.Nil(t, schemas.GetSchema("docsync.decision", 2))
	require.Nil(t, schemas.GetSchema("docsync.nonexistent"))
}

func TestSupportedManifestVersions_DeclaresV1(t *testing.T) {
	require.Equal(t, []int{1}, schemas.SupportedManifestVersions)
	require.True(t, schemas.IsSupportedManifestVersion(1))
	require.False(t, schemas.IsSupportedManifestVersion(2))
	require.False(t, schemas.IsSupportedManifestVersion(0))
}

func TestSmoke_SchemasPackageVersionIsAString(t *testing.T) {
	version := schemas.SchemasPackageVersion
	require.NotEmpty(t, version)
	require.IsType(t, "", version)
}
