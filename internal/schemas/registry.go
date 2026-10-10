package schemas

import "sort"

// SchemasPackageVersion mirrors SCHEMAS_PACKAGE_VERSION from the TS package.
const SchemasPackageVersion = "0.0.0"

// RegisteredSchema is one registry entry. In TS this carried the zod schema
// itself; in Go it carries a representative validated example plus the
// parse/validate entry points, so drift against the registry fails closed
// (OD-R03) exactly as the TS store's registry validation did.
type RegisteredSchema struct {
	// ID is the registry id, e.g. "docsync.manifest".
	ID string
	// Version is the integer schema version (always 1 today).
	Version int
	// Description is the human-readable registry description.
	Description string
	// Example is a representative valid instance.
	Example any
	// Validate validates the example against the schema refinements.
	Validate func() []Issue
	// Parse strictly parses wire bytes for this artifact.
	Parse func(data []byte) error
	// ParseTyped strictly parses and returns the typed value.
	ParseTyped func(data []byte) (any, error)
}

var registryEntries []RegisteredSchema

// Register adds an entry to the schema registry (called from init() in the
// schema files; duplicates panic — the registry is a closed set).
func Register(entry RegisteredSchema) {
	for _, existing := range registryEntries {
		if existing.ID == entry.ID {
			panic("schemas: duplicate registry id " + entry.ID)
		}
	}
	registryEntries = append(registryEntries, entry)
}

// SchemaRegistry returns all entries sorted by id (deterministic order).
func SchemaRegistry() []RegisteredSchema {
	out := make([]RegisteredSchema, len(registryEntries))
	copy(out, registryEntries)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetSchema looks up an entry by id (and version when given).
func GetSchema(id string, version ...int) *RegisteredSchema {
	for i := range registryEntries {
		e := &registryEntries[i]
		if e.ID != id {
			continue
		}
		if len(version) > 0 && e.Version != version[0] {
			continue
		}
		return e
	}
	return nil
}

// SupportedManifestVersions lists the manifest versions this build accepts
// (acceptance is consumer-enforced, fail-closed per D8).
var SupportedManifestVersions = []int{1}

// IsSupportedManifestVersion reports whether version is accepted.
func IsSupportedManifestVersion(version int) bool {
	for _, v := range SupportedManifestVersions {
		if v == version {
			return true
		}
	}
	return false
}
