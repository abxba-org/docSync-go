package triggers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// TRIGGER_MAP_SCHEMA_ID is the registry id of the versioned mapping-table schema.
const TRIGGER_MAP_SCHEMA_ID = "docsync.trigger-map"

// taxonomyVersion mirrors schemas.TriggerTaxonomyVersion (unexported there).
// Asserted against the registered docsync.trigger-map entry's version in the
// tests so taxonomy drift fails closed.
const taxonomyVersion = 1

// TRIGGER_MAP_SCHEMA_REF is the fully versioned decision-schema reference for
// the trigger taxonomy, e.g. `docsync.trigger-map/v1`. Every unknown-trigger
// error names it (V-P0-9). Its embedded version must track taxonomyVersion —
// asserted in the tests so taxonomy drift fails closed.
const TRIGGER_MAP_SCHEMA_REF = TRIGGER_MAP_SCHEMA_ID + "/v1"

// EDOCSYNC_TRIGGER_UNKNOWN is the distinguishable error code for a trigger value
// absent from the decision schema (taxonomy drift — R17). Fail-closed (D8).
const EDOCSYNC_TRIGGER_UNKNOWN = "EDOCSYNC_TRIGGER_UNKNOWN"

// UniversalTriggerName is a universal trigger name (the v1 user vocabulary,
// identical to the engine's).
type UniversalTriggerName = schemas.UniversalTrigger

// TriggerDirection is the direction of the manifest field being resolved.
type TriggerDirection string

// The two manifest directions (for precise diagnostics).
const (
	TriggerDirectionTriggers TriggerDirection = "triggers"
	TriggerDirectionIgnore   TriggerDirection = "ignore"
)

// TriggerMappingResult is the outcome of resolving user-supplied vocabulary.
type TriggerMappingResult struct {
	// Mapped are values the mapping layer accepted (v1: identity — same strings).
	Mapped []UniversalTriggerName `json:"mapped"`
	// UnknownValues are user-supplied values absent from the taxonomy, in input order.
	UnknownValues []string `json:"unknownValues"`
}

// TriggerTaxonomyDescription describes the supported taxonomy for docs/CLI.
type TriggerTaxonomyDescription struct {
	TaxonomyVersion int                    `json:"taxonomyVersion"`
	SchemaRef       string                 `json:"schemaRef"`
	Values          []UniversalTriggerName `json:"values"`
}

// knownTriggers is the accepted v1 vocabulary (identity membership set).
var knownTriggers = func() map[string]bool {
	m := make(map[string]bool, len(schemas.UniversalTriggerValues))
	for _, v := range schemas.UniversalTriggerValues {
		m[string(v)] = true
	}
	return m
}()

// supportedList is the canonical value list joined for diagnostics.
var supportedList = func() string {
	parts := make([]string, 0, len(schemas.UniversalTriggerValues))
	for _, v := range schemas.UniversalTriggerValues {
		parts = append(parts, string(v))
	}
	return strings.Join(parts, ", ")
}()

// jsonQuote renders a value as JSON.stringify would (a quoted JSON string).
func jsonQuote(value string) string {
	b, err := json.Marshal(value)
	if err != nil {
		// Unreachable for a plain string; fail closed with a quoted literal.
		return `"` + value + `"`
	}
	return string(b)
}

// FormatUnknownTriggers renders the version-naming diagnostic message for
// unknown trigger values (R17/V-P0-9): names the value(s), the direction, the
// expected taxonomy version, the decision-schema reference, and every supported
// value.
func FormatUnknownTriggers(unknownValues []string, direction TriggerDirection) string {
	plural := len(unknownValues) > 1
	quotedParts := make([]string, 0, len(unknownValues))
	for _, value := range unknownValues {
		quotedParts = append(quotedParts, jsonQuote(value))
	}
	suffix := ""
	if plural {
		suffix = "s"
	}
	return fmt.Sprintf(
		"unknown trigger value%s %s for %s; expected trigger-taxonomy version %d (decision schema %s); supported values: %s",
		suffix, strings.Join(quotedParts, ", "), direction, taxonomyVersion, TRIGGER_MAP_SCHEMA_REF, supportedList,
	)
}

// UnknownTriggerError is raised when the manifest vocabulary contains values the
// expected taxonomy version does not define. The message names the expected
// taxonomy version and the versioned decision schema verbatim (V-P0-9).
type UnknownTriggerError struct {
	ExpectedTaxonomyVersion int
	UnknownValues           []string

	inner *docutil.Error
}

// NewUnknownTriggerError builds the typed hard error for fail-closed callers.
func NewUnknownTriggerError(unknownValues []string, direction TriggerDirection) *UnknownTriggerError {
	return &UnknownTriggerError{
		ExpectedTaxonomyVersion: taxonomyVersion,
		UnknownValues:           append([]string{}, unknownValues...),
		inner:                   docutil.New(EDOCSYNC_TRIGGER_UNKNOWN, FormatUnknownTriggers(unknownValues, direction)),
	}
}

// Error renders the coded error (`EDOCSYNC_TRIGGER_UNKNOWN: <message>`).
func (e *UnknownTriggerError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_TRIGGER_UNKNOWN.
func (e *UnknownTriggerError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *UnknownTriggerError) Unwrap() error { return e.inner }

// MapTriggers resolves user-supplied trigger vocabulary onto the decision
// schema (v1: identity + validation against the universal set). Never throws —
// returns the unknown values so callers decide the failure shape; NewUnknownTriggerError
// is the typed hard error for fail-closed callers. An absent list (nil) maps
// cleanly to an empty result.
func MapTriggers(values []string) TriggerMappingResult {
	if values == nil {
		return TriggerMappingResult{Mapped: []UniversalTriggerName{}, UnknownValues: []string{}}
	}
	mapped := []UniversalTriggerName{}
	unknownValues := []string{}
	for _, value := range values {
		if knownTriggers[value] {
			mapped = append(mapped, UniversalTriggerName(value))
		} else if !containsString(unknownValues, value) {
			unknownValues = append(unknownValues, value)
		}
	}
	return TriggerMappingResult{Mapped: mapped, UnknownValues: unknownValues}
}

// DescribeTriggerTaxonomy is the supported trigger taxonomy for docs/CLI
// surfaces: version, versioned decision-schema reference, and the canonical
// value list.
func DescribeTriggerTaxonomy() TriggerTaxonomyDescription {
	values := append([]UniversalTriggerName{}, schemas.UniversalTriggerValues...)
	return TriggerTaxonomyDescription{
		TaxonomyVersion: taxonomyVersion,
		SchemaRef:       TRIGGER_MAP_SCHEMA_REF,
		Values:          values,
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
