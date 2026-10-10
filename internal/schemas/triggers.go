// Ported from packages/schemas/src/v1/triggers.ts
package schemas

import "sort"

// TriggerTaxonomyVersion is the version of the universal trigger taxonomy.
// Bumped only with explicit migration.
const TriggerTaxonomyVersion = 1

// UniversalTrigger values (verbatim from ARCH §8.3.1/§11.1).
const (
	UniversalTriggerPublicBehavior      UniversalTrigger = "public_behavior"
	UniversalTriggerAPIChange           UniversalTrigger = "api_change"
	UniversalTriggerConfigurationChange UniversalTrigger = "configuration_change"
	UniversalTriggerArchitectureChange  UniversalTrigger = "architecture_change"
	UniversalTriggerInternalRefactor    UniversalTrigger = "internal_refactor"
	UniversalTriggerTestOnly            UniversalTrigger = "test_only"
)

// UniversalTrigger is one universal trigger-vocabulary member.
type UniversalTrigger string

// UniversalTriggerValues lists the universal vocabulary in canonical order.
var UniversalTriggerValues = []UniversalTrigger{
	UniversalTriggerPublicBehavior, UniversalTriggerAPIChange,
	UniversalTriggerConfigurationChange, UniversalTriggerArchitectureChange,
	UniversalTriggerInternalRefactor, UniversalTriggerTestOnly,
}

// IsValidUniversalTrigger reports membership.
func IsValidUniversalTrigger(s string) bool {
	for _, v := range UniversalTriggerValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// TriggerMap is the versioned mapping table: user-facing manifest vocabulary
// → engine decision schema name (values of the engine `change_kind` space).
// Core implements resolution; unknown-vocabulary errors must name
// TRIGGER_TAXONOMY_VERSION.
type TriggerMap struct {
	TaxonomyVersion int               `json:"taxonomyVersion"`
	Mappings        map[string]string `json:"mappings"`
}

// Validate checks the trigger map: the taxonomy version literal, universal
// mapping keys, and non-empty mapping values.
func (t *TriggerMap) Validate() []Issue {
	var issues []Issue
	if t.TaxonomyVersion != TriggerTaxonomyVersion {
		Collect(&issues, &Issue{Path: []string{"taxonomyVersion"}, Message: "Invalid literal value, expected 1"})
	}
	keys := make([]string, 0, len(t.Mappings))
	for k := range t.Mappings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		Collect(&issues, CheckEnum(key, stringsOf(UniversalTriggerValues), []string{"mappings", key}))
		Collect(&issues, RequiredString(t.Mappings[key], 1, []string{"mappings", key}))
	}
	return issues
}

// ParseTriggerMap strictly parses and validates JSON trigger-map bytes.
func ParseTriggerMap(data []byte) (*TriggerMap, error) {
	var t TriggerMap
	if err := StrictDecode(data, &t); err != nil {
		return nil, err
	}
	if issues := t.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &t, nil
}

// TriggerMapExample is the canonical example mapping (golden data).
var TriggerMapExample = TriggerMap{
	TaxonomyVersion: 1,
	Mappings: map[string]string{
		"public_behavior":      "behavioral",
		"api_change":           "api_contract",
		"configuration_change": "configuration",
		"architecture_change":  "architecture",
		"internal_refactor":    "other",
		"test_only":            "test_only",
	},
}
