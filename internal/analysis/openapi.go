package analysis

// OpenAPI delta classifier (P11-T07; OD-R28; verification V-P11-4).
//
// Ported from core/src/analysis/openapi.ts (traceability, plan.md §7).
//
// Three-tier taxonomy over a before/after pair of PARSED OpenAPI/Swagger
// documents (the caller parses YAML/JSON — this module is pure over objects,
// no fs/network/clock, no new dependencies):
//
//   - "breaking"              — the wire contract changed for existing clients
//     (removals, type/ref changes, required-property additions, response
//     set/shape changes).
//   - "behavior-annotating"   — the contract still holds, but the annotation
//     changes possible documentation impact (deprecations, operationId
//     renames).
//   - "non-breaking"          — purely additive or descriptive changes
//     (new optional properties/endpoints, enum widening, description-only
//     changes — decided: description changes are tier-3, NOT tier-2).
//
// The classification rule table is versioned data (OpenAPIDeltaRules) —
// tiers are never hard-coded at emit sites; every delta entry looks its tier
// up from the table (single source of truth, D11 spirit).
//
// Fail-closed (D14): structurally invalid inputs (non-objects, missing or
// non-string `openapi`/`swagger` fields, wrong-typed `paths`/`components`)
// raise a typed *OpenApiClassifierError — never a guessed empty result.
// Otherwise-unclassified schema changes inside a request/response body fall
// back to the coarse `request_schema_changed` / `response_schema_changed`
// kinds (breaking) rather than being silently dropped.
//
// Declared V1 coverage gaps (documented, never silently guessed): policy
// metadata on operations (`security`, `servers`, `tags`), components other
// than `schemas`, path-item-level `parameters`, and schema recursion deeper
// than the documented depth cap are NOT classified.

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

/* ------------------------------------------------------------------- types */

// OpenApiTier is the three-tier delta taxonomy (OD-R28).
type OpenApiTier string

const (
	OpenApiTierBreaking           OpenApiTier = "breaking"
	OpenApiTierBehaviorAnnotating OpenApiTier = "behavior-annotating"
	OpenApiTierNonBreaking        OpenApiTier = "non-breaking"
)

// OpenApiDeltaKind is the delta taxonomy; every kind has exactly one rule.
type OpenApiDeltaKind string

const (
	OpenApiDeltaPathRemoved               OpenApiDeltaKind = "path_removed"
	OpenApiDeltaPathAdded                 OpenApiDeltaKind = "path_added"
	OpenApiDeltaOperationRemoved          OpenApiDeltaKind = "operation_removed"
	OpenApiDeltaOperationAdded            OpenApiDeltaKind = "operation_added"
	OpenApiDeltaParameterRemoved          OpenApiDeltaKind = "parameter_removed"
	OpenApiDeltaParameterAdded            OpenApiDeltaKind = "parameter_added"
	OpenApiDeltaParameterRequiredAdded    OpenApiDeltaKind = "parameter_required_added"
	OpenApiDeltaRequestSchemaChanged      OpenApiDeltaKind = "request_schema_changed"
	OpenApiDeltaResponseSchemaChanged     OpenApiDeltaKind = "response_schema_changed"
	OpenApiDeltaDeprecatedAdded           OpenApiDeltaKind = "deprecated_added"
	OpenApiDeltaDescriptionChanged        OpenApiDeltaKind = "description_changed"
	OpenApiDeltaOperationIDChanged        OpenApiDeltaKind = "operation_id_changed"
	OpenApiDeltaSchemaPropertyRemoved     OpenApiDeltaKind = "schema_property_removed"
	OpenApiDeltaSchemaPropertyTypeChanged OpenApiDeltaKind = "schema_property_type_changed"
	OpenApiDeltaSchemaPropertyAdded       OpenApiDeltaKind = "schema_property_added"
	OpenApiDeltaEnumNarrowed              OpenApiDeltaKind = "enum_narrowed"
	OpenApiDeltaEnumWidened               OpenApiDeltaKind = "enum_widened"
)

// OpenApiDeltaEntry is one classified delta.
type OpenApiDeltaEntry struct {
	Kind OpenApiDeltaKind `json:"kind"`
	/** JSON-pointer-style location, e.g. `/paths/~1payments~1{id}/get/responses/200`. */
	Location string      `json:"location"`
	Tier     OpenApiTier `json:"tier"`
	Detail   string      `json:"detail"`
}

// OpenApiDeltaRule is one versioned classification rule.
type OpenApiDeltaRule struct {
	Kind      OpenApiDeltaKind `json:"kind"`
	Tier      OpenApiTier      `json:"tier"`
	Rationale string           `json:"rationale"`
}

// OpenApiDeltaRuleset is the versioned rule table.
type OpenApiDeltaRuleset struct {
	Version string             `json:"version"`
	Rules   []OpenApiDeltaRule `json:"rules"`
}

// OpenApiDeltaSummary is the per-tier rollup of a classification.
type OpenApiDeltaSummary struct {
	Breaking           int         `json:"breaking"`
	BehaviorAnnotating int         `json:"behaviorAnnotating"`
	NonBreaking        int         `json:"nonBreaking"`
	WorstTier          OpenApiTier `json:"worstTier"`
}

// OpenApiDeltaClassification is the full classification result.
type OpenApiDeltaClassification struct {
	Deltas  []OpenApiDeltaEntry `json:"deltas"`
	Summary OpenApiDeltaSummary `json:"summary"`
	/** Version of the rule table that produced this classification (provenance). */
	RulesVersion string `json:"rulesVersion"`
}

/* --------------------------------------------------------------- rule table */

// openApiDeltaRuleList is the frozen classification data (OD-R28 three-tier
// taxonomy). The consistent rule decisions: description-only changes are
// tier-3 (they alter no contract semantics); `parameter_required_added`
// covers both operation parameters flipping to required AND schema
// properties newly listed in `required` (data previously optional is now
// mandatory on one side of the wire); whole component-schema removals re-use
// `schema_property_removed` (no dedicated kind in V1).
var openApiDeltaRuleList = []OpenApiDeltaRule{
	{
		Kind:      OpenApiDeltaPathRemoved,
		Tier:      OpenApiTierBreaking,
		Rationale: "a documented endpoint disappeared — clients and docs referencing it break",
	},
	{
		Kind:      OpenApiDeltaPathAdded,
		Tier:      OpenApiTierNonBreaking,
		Rationale: "purely additive — a new endpoint cannot break existing clients",
	},
	{
		Kind:      OpenApiDeltaOperationRemoved,
		Tier:      OpenApiTierBreaking,
		Rationale: "a method on an existing path disappeared — existing calls fail",
	},
	{
		Kind:      OpenApiDeltaOperationAdded,
		Tier:      OpenApiTierNonBreaking,
		Rationale: "purely additive — a new method cannot break existing calls",
	},
	{
		Kind:      OpenApiDeltaParameterRemoved,
		Tier:      OpenApiTierBreaking,
		Rationale: "a documented input disappeared — clients still sending it (or docs citing it) break",
	},
	{
		Kind:      OpenApiDeltaParameterAdded,
		Tier:      OpenApiTierNonBreaking,
		Rationale: "only emitted for optional parameter additions — required additions are classified as parameter_required_added",
	},
	{
		Kind:      OpenApiDeltaParameterRequiredAdded,
		Tier:      OpenApiTierBreaking,
		Rationale: "covers operation parameters flipping required=false→true AND schema properties newly listed in `required` — data previously optional is now mandatory",
	},
	{
		Kind:      OpenApiDeltaRequestSchemaChanged,
		Tier:      OpenApiTierBreaking,
		Rationale: "request body schema changed in a way the fine-grained rules do not cover ($ref swap, media-type change, unclassified structural change) — sent data may be rejected",
	},
	{
		Kind:      OpenApiDeltaResponseSchemaChanged,
		Tier:      OpenApiTierBreaking,
		Rationale: "any change to the response set or shape, including added/removed statuses and media types — documented API behavior changed (fail closed toward breaking)",
	},
	{
		Kind:      OpenApiDeltaDeprecatedAdded,
		Tier:      OpenApiTierBehaviorAnnotating,
		Rationale: "the contract still holds but a planned removal is announced — rouses docs without failing existing contracts",
	},
	{
		Kind:      OpenApiDeltaDescriptionChanged,
		Tier:      OpenApiTierNonBreaking,
		Rationale: "descriptive text only, no contract semantics — decided: description additions/changes are tier-3, not tier-2",
	},
	{
		Kind:      OpenApiDeltaOperationIDChanged,
		Tier:      OpenApiTierBehaviorAnnotating,
		Rationale: "operationId is a stable anchor for tooling and generated docs — renaming rouses docs without breaking the wire contract",
	},
	{
		Kind:      OpenApiDeltaSchemaPropertyRemoved,
		Tier:      OpenApiTierBreaking,
		Rationale: "data no longer exists in the schema — also re-used for whole component-schema removals (no dedicated kind in V1)",
	},
	{
		Kind:      OpenApiDeltaSchemaPropertyTypeChanged,
		Tier:      OpenApiTierBreaking,
		Rationale: "a property type/$ref changed (also the fail-closed catch for otherwise-unclassified property schema changes, e.g. format changes)",
	},
	{
		Kind:      OpenApiDeltaSchemaPropertyAdded,
		Tier:      OpenApiTierNonBreaking,
		Rationale: "only optional property additions (and component-schema additions) — required additions are classified as parameter_required_added",
	},
	{
		Kind:      OpenApiDeltaEnumNarrowed,
		Tier:      OpenApiTierBreaking,
		Rationale: "the accepted/produced value set shrank — previously valid values fail; non-monotonic enum changes also fail closed into this kind",
	},
	{
		Kind:      OpenApiDeltaEnumWidened,
		Tier:      OpenApiTierNonBreaking,
		Rationale: "the value set grew — previously valid values remain valid",
	},
}

// OpenAPIDeltaRules is the versioned classification data (OD-R28 three-tier
// taxonomy). Tiers are looked up from here at emit time — never hard-coded at
// emit sites. The Go equivalent of the TS deepFreeze: the table is package
// level and returned by value only.
var OpenAPIDeltaRules = OpenApiDeltaRuleset{
	Version: "1.0.0",
	Rules:   openApiDeltaRuleList,
}

func tierFor(kind OpenApiDeltaKind) (OpenApiTier, error) {
	for _, rule := range OpenAPIDeltaRules.Rules {
		if rule.Kind == kind {
			return rule.Tier, nil
		}
	}
	// Unreachable while the table above covers every kind — fail closed (D14).
	return "", newOpenApiClassifierError("rule table incomplete: no rule for kind '" + string(kind) + "'")
}

/* ------------------------------------------------------------------- error */

// OpenApiClassifierError is the typed fail-closed error of this classifier
// (structurally invalid inputs, incomplete rule table). It carries no code
// through docutil because the TS class has none either.
type OpenApiClassifierError struct {
	Message string
}

func (e *OpenApiClassifierError) Error() string { return e.Message }

func newOpenApiClassifierError(message string) *OpenApiClassifierError {
	return &OpenApiClassifierError{Message: message}
}

/* ------------------------------------------------------------------ helpers */

// asPlainObject mirrors the TS isPlainObject predicate: only string-keyed
// objects (the shape every JSON/YAML-to-JS parser produces) qualify — an
// array, a scalar or a non-string-keyed mapping does not.
func asPlainObject(value any) (map[string]any, bool) {
	obj, ok := value.(map[string]any)
	return obj, ok
}

// describeValue renders a value the way `typeof` plus the array special case
// do, for the fail-closed error messages.
func describeValue(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	default:
		return "object"
	}
}

func escapePointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func sortedKeys(record map[string]any) []string {
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// openApiCanonical is canonicalJson(value ?? null) with JSON.stringify
// semantics for the non-finite floats a YAML-parsed document can carry.
func openApiCanonical(value any) (string, error) {
	if number, ok := value.(float64); ok && (math.IsInf(number, 0) || math.IsNaN(number)) {
		return "null", nil
	}
	return docutil.CanonicalJSON(value)
}

func canonicalOf(value any) (string, error) { return openApiCanonical(value) }

// truncate caps a string at 80 characters (JS string length semantics; rune
// counting matches UTF-16 length for every non-surrogate character).
func truncate(text string) string {
	if utf8.RuneCountInString(text) <= 80 {
		return text
	}
	runes := []rune(text)
	return string(runes[:77]) + "…"
}

// openApiDeltaSink accumulates the emitted deltas (TS: the `Out` interface).
type openApiDeltaSink struct {
	deltas []OpenApiDeltaEntry
}

func push(out *openApiDeltaSink, kind OpenApiDeltaKind, location, detail string) error {
	tier, err := tierFor(kind)
	if err != nil {
		return err
	}
	out.deltas = append(out.deltas, OpenApiDeltaEntry{Kind: kind, Location: location, Tier: tier, Detail: detail})
	return nil
}

// unionSorted is the sorted union of both key sets — the deterministic walk
// order.
func unionSorted(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	union := make([]string, 0, len(a)+len(b))
	for _, key := range a {
		if !seen[key] {
			seen[key] = true
			union = append(union, key)
		}
	}
	for _, key := range b {
		if !seen[key] {
			seen[key] = true
			union = append(union, key)
		}
	}
	sort.Strings(union)
	return union
}

// asApiDoc validates the structural contract of one parsed side (fail closed,
// D14) and returns it as a plain object.
func asApiDoc(value any, side string) (map[string]any, error) {
	doc, ok := asPlainObject(value)
	if !ok {
		return nil, newOpenApiClassifierError(side + ": expected a parsed OpenAPI object, got " + describeValue(value))
	}
	if _, ok := doc["openapi"].(string); !ok {
		if _, ok := doc["swagger"].(string); !ok {
			return nil, newOpenApiClassifierError(side + ": missing required 'openapi' or 'swagger' string version field")
		}
	}
	for _, field := range []string{"paths", "components"} {
		present, exists := doc[field]
		if !exists {
			continue
		}
		if _, ok := asPlainObject(present); !ok {
			return nil, newOpenApiClassifierError(side + ": field '" + field + "' must be an object, got " + describeValue(present))
		}
	}
	return doc, nil
}

// optionalObject is a present-or-absent object field (TS: `Record<string,
// unknown> | undefined`).
type optionalObject struct {
	value   map[string]any
	present bool
}

// optionalAny is a present-or-absent field value (TS `unknown` vs
// `undefined`: a JSON null is PRESENT and therefore not "undefined").
type optionalAny struct {
	value   any
	present bool
}

func field(parent map[string]any, name string) optionalAny {
	value, exists := parent[name]
	return optionalAny{value: value, present: exists}
}

// recordField reads an object-typed field, failing closed when the field is
// present but not an object.
func recordField(parent map[string]any, field, context string) (optionalObject, error) {
	value, exists := parent[field]
	if !exists {
		return optionalObject{}, nil
	}
	obj, ok := asPlainObject(value)
	if !ok {
		return optionalObject{}, newOpenApiClassifierError(context + ": '" + field + "' must be an object")
	}
	return optionalObject{value: obj, present: true}, nil
}

/* ------------------------------------------------------------ enum compare */

func enumValues(schema map[string]any) ([]any, bool) {
	value, ok := schema["enum"].([]any)
	if !ok || len(value) == 0 {
		return nil, false
	}
	return value, true
}

func compareEnums(before, after map[string]any, location string, out *openApiDeltaSink) error {
	beforeValues, beforeOK := enumValues(before)
	afterValues, afterOK := enumValues(after)
	if !beforeOK || !afterOK {
		return nil
	}
	beforeSet := make([]string, 0, len(beforeValues))
	beforeSeen := make(map[string]bool, len(beforeValues))
	for _, value := range beforeValues {
		canonical, err := openApiCanonical(value)
		if err != nil {
			return err
		}
		if !beforeSeen[canonical] {
			beforeSeen[canonical] = true
			beforeSet = append(beforeSet, canonical)
		}
	}
	afterSet := make([]string, 0, len(afterValues))
	afterSeen := make(map[string]bool, len(afterValues))
	for _, value := range afterValues {
		canonical, err := openApiCanonical(value)
		if err != nil {
			return err
		}
		if !afterSeen[canonical] {
			afterSeen[canonical] = true
			afterSet = append(afterSet, canonical)
		}
	}
	inAfter := func(value string) bool { return afterSeen[value] }
	inBefore := func(value string) bool { return beforeSeen[value] }
	if len(beforeSet) == len(afterSet) {
		allPresent := true
		for _, value := range beforeSet {
			if !inAfter(value) {
				allPresent = false
				break
			}
		}
		if allPresent {
			return nil
		}
	}
	purelyNarrowed := true
	for _, value := range afterSet {
		if !inBefore(value) {
			purelyNarrowed = false
			break
		}
	}
	purelyWidened := true
	for _, value := range beforeSet {
		if !inAfter(value) {
			purelyWidened = false
			break
		}
	}
	if purelyNarrowed {
		removed := []any{}
		for _, value := range beforeSet {
			if !inAfter(value) {
				removed = append(removed, value)
			}
		}
		rendered, err := openApiCanonical(removed)
		if err != nil {
			return err
		}
		return push(out, OpenApiDeltaEnumNarrowed, location, "enum values removed: "+truncate(rendered))
	}
	if purelyWidened {
		added := []any{}
		for _, value := range afterSet {
			if !inBefore(value) {
				added = append(added, value)
			}
		}
		rendered, err := openApiCanonical(added)
		if err != nil {
			return err
		}
		return push(out, OpenApiDeltaEnumWidened, location, "enum values added: "+truncate(rendered))
	}
	return push(out, OpenApiDeltaEnumNarrowed, location,
		"enum changed non-monotonically (added and removed values) — fail closed toward breaking")
}

/* ----------------------------------------------------------- schema compare */

// maxSchemaDepth caps the fine-grained schema recursion.
const maxSchemaDepth = 4

func descriptionChanged(before, after map[string]any) (bool, error) {
	beforeText, err := openApiCanonical(before["description"])
	if err != nil {
		return false, err
	}
	afterText, err := openApiCanonical(after["description"])
	if err != nil {
		return false, err
	}
	return beforeText != afterText, nil
}

func compareDescription(before, after map[string]any, location string, out *openApiDeltaSink) error {
	changed, err := descriptionChanged(before, after)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	beforeText, err := openApiCanonical(before["description"])
	if err != nil {
		return err
	}
	afterText, err := openApiCanonical(after["description"])
	if err != nil {
		return err
	}
	beforeRendered := beforeText
	if _, exists := before["description"]; !exists {
		beforeRendered = "(absent)"
	}
	afterRendered := afterText
	if _, exists := after["description"]; !exists {
		afterRendered = "(absent)"
	}
	return push(out, OpenApiDeltaDescriptionChanged, location,
		"description changed: "+truncate(beforeRendered)+" → "+truncate(afterRendered))
}

func compareDeprecated(before, after map[string]any, location, what string, out *openApiDeltaSink) error {
	if after["deprecated"] == true && before["deprecated"] != true {
		return push(out, OpenApiDeltaDeprecatedAdded, location, what+" marked deprecated")
	}
	return nil
}

// compareSchemaObject is the fine-grained comparison of two schema objects
// (properties, required, enum, description). It emits nothing when
// canonically equal. Depth-capped recursion.
func compareSchemaObject(before, after map[string]any, location string, depth int, out *openApiDeltaSink) error {
	if err := compareEnums(before, after, location, out); err != nil {
		return err
	}
	if err := compareDescription(before, after, location, out); err != nil {
		return err
	}
	if err := compareDeprecated(before, after, location, "schema", out); err != nil {
		return err
	}
	beforeProps, err := recordField(before, "properties", location)
	if err != nil {
		return err
	}
	afterProps, err := recordField(after, "properties", location)
	if err != nil {
		return err
	}
	if beforeProps.present && afterProps.present {
		if err := compareProperties(beforeProps.value, afterProps.value, location, depth, out); err != nil {
			return err
		}
	}
	return compareRequiredList(before, after, beforeProps, afterProps, location, out)
}

func compareProperties(beforeProps, afterProps map[string]any, location string, depth int, out *openApiDeltaSink) error {
	for _, name := range unionSorted(sortedKeys(beforeProps), sortedKeys(afterProps)) {
		propLocation := location + "/properties/" + escapePointer(name)
		before, beforeExists := beforeProps[name]
		after, afterExists := afterProps[name]
		if !beforeExists {
			if isRequired(afterProps, name) {
				if err := push(out, OpenApiDeltaParameterRequiredAdded, propLocation,
					"property '"+name+"' added and required"); err != nil {
					return err
				}
			} else {
				if err := push(out, OpenApiDeltaSchemaPropertyAdded, propLocation,
					"optional property '"+name+"' added"); err != nil {
					return err
				}
			}
			continue
		}
		if !afterExists {
			if err := push(out, OpenApiDeltaSchemaPropertyRemoved, propLocation,
				"property '"+name+"' removed"); err != nil {
				return err
			}
			continue
		}
		if err := compareProperty(before, after, name, propLocation, depth, out); err != nil {
			return err
		}
		_, beforePlain := asPlainObject(before)
		_, afterPlain := asPlainObject(after)
		if !isRequired(beforeProps, name) && isRequired(afterProps, name) && beforePlain && afterPlain {
			if err := push(out, OpenApiDeltaParameterRequiredAdded, propLocation,
				"property '"+name+"' became required"); err != nil {
				return err
			}
		}
	}
	return nil
}

func isRequired(schemaProps map[string]any, name string) bool {
	required, ok := schemaProps["required"].([]any)
	if !ok {
		return false
	}
	for _, item := range required {
		if item == name {
			return true
		}
	}
	return false
}

func compareProperty(before, after any, name, location string, depth int, out *openApiDeltaSink) error {
	beforeObj, beforePlain := asPlainObject(before)
	afterObj, afterPlain := asPlainObject(after)
	if !beforePlain || !afterPlain {
		beforeCanonical, err := canonicalOf(before)
		if err != nil {
			return err
		}
		afterCanonical, err := canonicalOf(after)
		if err != nil {
			return err
		}
		if beforeCanonical != afterCanonical {
			return push(out, OpenApiDeltaSchemaPropertyTypeChanged, location,
				"property '"+name+"' schema changed: "+truncate(beforeCanonical)+" → "+truncate(afterCanonical))
		}
		return nil
	}
	start := len(out.deltas)
	beforeType, err := openApiCanonical(beforeObj["type"])
	if err != nil {
		return err
	}
	afterType, err := openApiCanonical(afterObj["type"])
	if err != nil {
		return err
	}
	if beforeType != afterType {
		return push(out, OpenApiDeltaSchemaPropertyTypeChanged, location,
			"property '"+name+"' type changed: "+beforeType+" → "+afterType)
		// type-level change supersedes finer comparisons (V1)
	}
	beforeRef, err := openApiCanonical(beforeObj["$ref"])
	if err != nil {
		return err
	}
	afterRef, err := openApiCanonical(afterObj["$ref"])
	if err != nil {
		return err
	}
	if beforeRef != afterRef {
		return push(out, OpenApiDeltaSchemaPropertyTypeChanged, location,
			"property '"+name+"' $ref changed: "+beforeRef+" → "+afterRef)
	}
	if err := compareEnums(beforeObj, afterObj, location, out); err != nil {
		return err
	}
	if err := compareDescription(beforeObj, afterObj, location, out); err != nil {
		return err
	}
	if depth < maxSchemaDepth {
		if err := compareSchemaObject(beforeObj, afterObj, location, depth+1, out); err != nil {
			return err
		}
	}
	if len(out.deltas) == start {
		beforeCanonical, err := canonicalOf(before)
		if err != nil {
			return err
		}
		afterCanonical, err := canonicalOf(after)
		if err != nil {
			return err
		}
		if beforeCanonical != afterCanonical {
			return push(out, OpenApiDeltaSchemaPropertyTypeChanged, location,
				"property '"+name+"' schema changed (no finer-grained rule applies)")
		}
	}
	return nil
}

func compareRequiredList(before, after map[string]any, beforeProps, afterProps optionalObject, location string, out *openApiDeltaSink) error {
	if !beforeProps.present || !afterProps.present {
		return nil
	}
	beforeRequired, beforeOK := before["required"].([]any)
	afterRequired, afterOK := after["required"].([]any)
	if !beforeOK || !afterOK {
		return nil
	}
	for _, name := range afterRequired {
		key, ok := name.(string)
		if !ok {
			continue
		}
		listed := false
		for _, item := range beforeRequired {
			if item == key {
				listed = true
				break
			}
		}
		if listed {
			continue
		}
		_, beforeHas := beforeProps.value[key]
		_, afterHas := afterProps.value[key]
		if beforeHas && afterHas {
			if err := push(out, OpenApiDeltaParameterRequiredAdded,
				location+"/properties/"+escapePointer(key),
				"property '"+key+"' became required"); err != nil {
				return err
			}
		}
	}
	return nil
}

// compareInlineSchema compares two inline schemas, falling back to the coarse
// catchKind when the schemas differ but no fine-grained rule fired (fail
// closed — drift is never silently dropped).
func compareInlineSchema(before, after any, location string, catchKind OpenApiDeltaKind, out *openApiDeltaSink) error {
	beforeCanonical, err := canonicalOf(before)
	if err != nil {
		return err
	}
	afterCanonical, err := canonicalOf(after)
	if err != nil {
		return err
	}
	if beforeCanonical == afterCanonical {
		return nil
	}
	beforeObj, beforePlain := asPlainObject(before)
	afterObj, afterPlain := asPlainObject(after)
	if !beforePlain || !afterPlain {
		return push(out, catchKind, location, "schema changed (non-object schema on at least one side)")
	}
	start := len(out.deltas)
	if err := compareSchemaObject(beforeObj, afterObj, location, 0, out); err != nil {
		return err
	}
	if len(out.deltas) == start {
		return push(out, catchKind, location, "schema changed (no finer-grained rule applies)")
	}
	return nil
}

/* -------------------------------------------------------------- parameters */

var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// paramKey is the `${in}|${name}` index key of one parameter.
type paramKey struct {
	key   string
	name  string
	where string
}

func parameterIndex(list optionalAny, context string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	if !list.present {
		return out, nil
	}
	entries, ok := list.value.([]any)
	if !ok {
		return nil, newOpenApiClassifierError(context + ": 'parameters' must be an array")
	}
	for _, entry := range entries {
		obj, isObj := asPlainObject(entry)
		if !isObj {
			return nil, newOpenApiClassifierError(context + ": every parameter must be an object with string 'name' and 'in'")
		}
		name, nameOK := obj["name"].(string)
		where, whereOK := obj["in"].(string)
		if !nameOK || !whereOK {
			return nil, newOpenApiClassifierError(context + ": every parameter must be an object with string 'name' and 'in'")
		}
		out[where+"|"+name] = obj
	}
	return out, nil
}

func compareParameters(beforeList, afterList optionalAny, location string, out *openApiDeltaSink) error {
	before, err := parameterIndex(beforeList, location)
	if err != nil {
		return err
	}
	after, err := parameterIndex(afterList, location)
	if err != nil {
		return err
	}
	beforeKeys := make([]string, 0, len(before))
	for key := range before {
		beforeKeys = append(beforeKeys, key)
	}
	afterKeys := make([]string, 0, len(after))
	for key := range after {
		afterKeys = append(afterKeys, key)
	}
	for _, key := range unionSorted(beforeKeys, afterKeys) {
		paramBefore, beforeExists := before[key]
		paramAfter, afterExists := after[key]
		parsed := parseParamKey(key)
		paramLocation := location + "/" + escapePointer(parsed.name)
		label := "parameter '" + parsed.name + "' (in " + parsed.where + ")"
		if !beforeExists {
			if err := push(out, OpenApiDeltaParameterAdded, paramLocation, label+" added"); err != nil {
				return err
			}
			continue
		}
		if !afterExists {
			if err := push(out, OpenApiDeltaParameterRemoved, paramLocation, label+" removed"); err != nil {
				return err
			}
			continue
		}
		if paramBefore["required"] != true && paramAfter["required"] == true {
			if err := push(out, OpenApiDeltaParameterRequiredAdded, paramLocation, label+" became required"); err != nil {
				return err
			}
		}
		if err := compareDeprecated(paramBefore, paramAfter, paramLocation, label, out); err != nil {
			return err
		}
		if err := compareDescription(paramBefore, paramAfter, paramLocation, out); err != nil {
			return err
		}
		if parsed.where == "body" {
			if err := compareInlineSchema(paramBefore["schema"], paramAfter["schema"],
				paramLocation+"/schema", OpenApiDeltaRequestSchemaChanged, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseParamKey(key string) paramKey {
	separator := strings.Index(key, "|")
	// Keys are built as `${in}|${name}` — the split is total by construction.
	return paramKey{key: key, where: key[:separator], name: key[separator+1:]}
}

/* ------------------------------------------------ request body / responses */

func compareContentSchemas(before, after map[string]any, location string, catchKind OpenApiDeltaKind, out *openApiDeltaSink) error {
	for _, mediaType := range unionSorted(sortedKeys(before), sortedKeys(after)) {
		mediaLocation := location + "/content/" + escapePointer(mediaType) + "/schema"
		schemaBefore := before[mediaType]
		schemaAfter := after[mediaType]
		// TS: entryBefore = isPlainObject(schemaBefore) ? schemaBefore : undefined
		entryBefore, beforePlain := asPlainObject(schemaBefore)
		entryAfter, afterPlain := asPlainObject(schemaAfter)
		if !beforePlain {
			if err := push(out, catchKind, mediaLocation, "media type '"+mediaType+"' added"); err != nil {
				return err
			}
			continue
		}
		if !afterPlain {
			if err := push(out, catchKind, mediaLocation, "media type '"+mediaType+"' removed"); err != nil {
				return err
			}
			continue
		}
		if err := compareInlineSchema(entryBefore["schema"], entryAfter["schema"], mediaLocation, catchKind, out); err != nil {
			return err
		}
	}
	return nil
}

func compareRequestBody(beforeOp, afterOp map[string]any, location string, out *openApiDeltaSink) error {
	before, err := recordField(beforeOp, "requestBody", location)
	if err != nil {
		return err
	}
	after, err := recordField(afterOp, "requestBody", location)
	if err != nil {
		return err
	}
	if !before.present && !after.present {
		return nil
	}
	bodyLocation := location + "/requestBody"
	if !before.present || !after.present {
		detail := "requestBody added"
		if before.present {
			detail = "requestBody removed"
		}
		return push(out, OpenApiDeltaRequestSchemaChanged, bodyLocation, detail)
	}
	start := len(out.deltas)
	if before.value["required"] != true && after.value["required"] == true {
		if err := push(out, OpenApiDeltaParameterRequiredAdded, bodyLocation, "requestBody became required"); err != nil {
			return err
		}
	}
	contentBefore, err := recordField(before.value, "content", bodyLocation)
	if err != nil {
		return err
	}
	contentAfter, err := recordField(after.value, "content", bodyLocation)
	if err != nil {
		return err
	}
	if contentBefore.present && contentAfter.present {
		if err := compareContentSchemas(contentBefore.value, contentAfter.value, bodyLocation,
			OpenApiDeltaRequestSchemaChanged, out); err != nil {
			return err
		}
	}
	if len(out.deltas) == start {
		beforeCanonical, err := canonicalOf(before.value)
		if err != nil {
			return err
		}
		afterCanonical, err := canonicalOf(after.value)
		if err != nil {
			return err
		}
		if beforeCanonical != afterCanonical {
			return push(out, OpenApiDeltaRequestSchemaChanged, bodyLocation,
				"requestBody changed (no finer rule applies)")
		}
	}
	return nil
}

func compareResponses(beforeOp, afterOp map[string]any, location string, out *openApiDeltaSink) error {
	before, err := recordField(beforeOp, "responses", location)
	if err != nil {
		return err
	}
	after, err := recordField(afterOp, "responses", location)
	if err != nil {
		return err
	}
	if !before.present && !after.present {
		return nil
	}
	responsesLocation := location + "/responses"
	if !before.present || !after.present {
		detail := "responses added"
		if before.present {
			detail = "responses removed"
		}
		return push(out, OpenApiDeltaResponseSchemaChanged, responsesLocation, detail)
	}
	for _, status := range unionSorted(sortedKeys(before.value), sortedKeys(after.value)) {
		statusLocation := responsesLocation + "/" + escapePointer(status)
		responseBefore, beforeExists := before.value[status]
		responseAfter, afterExists := after.value[status]
		if !beforeExists {
			if err := push(out, OpenApiDeltaResponseSchemaChanged, statusLocation,
				"response '"+status+"' added"); err != nil {
				return err
			}
			continue
		}
		if !afterExists {
			if err := push(out, OpenApiDeltaResponseSchemaChanged, statusLocation,
				"response '"+status+"' removed"); err != nil {
				return err
			}
			continue
		}
		responseBeforeObj, beforePlain := asPlainObject(responseBefore)
		responseAfterObj, afterPlain := asPlainObject(responseAfter)
		if !beforePlain || !afterPlain {
			return newOpenApiClassifierError(statusLocation + ": response objects must be objects on both sides")
		}
		start := len(out.deltas)
		if err := compareDescription(responseBeforeObj, responseAfterObj, statusLocation, out); err != nil {
			return err
		}
		contentBefore, err := recordField(responseBeforeObj, "content", statusLocation)
		if err != nil {
			return err
		}
		contentAfter, err := recordField(responseAfterObj, "content", statusLocation)
		if err != nil {
			return err
		}
		if contentBefore.present && contentAfter.present {
			if err := compareContentSchemas(contentBefore.value, contentAfter.value, statusLocation,
				OpenApiDeltaResponseSchemaChanged, out); err != nil {
				return err
			}
		}
		if len(out.deltas) == start {
			beforeCanonical, err := canonicalOf(responseBefore)
			if err != nil {
				return err
			}
			afterCanonical, err := canonicalOf(responseAfter)
			if err != nil {
				return err
			}
			if beforeCanonical != afterCanonical {
				if err := push(out, OpenApiDeltaResponseSchemaChanged, statusLocation,
					"response '"+status+"' changed (no finer rule applies)"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

/* ------------------------------------------------------------- operations */

func compareOperation(before, after map[string]any, location string, out *openApiDeltaSink) error {
	if err := compareParameters(field(before, "parameters"), field(after, "parameters"),
		location+"/parameters", out); err != nil {
		return err
	}
	if err := compareDescription(before, after, location, out); err != nil {
		return err
	}
	if err := compareDeprecated(before, after, location, "operation", out); err != nil {
		return err
	}
	beforeID, err := openApiCanonical(before["operationId"])
	if err != nil {
		return err
	}
	afterID, err := openApiCanonical(after["operationId"])
	if err != nil {
		return err
	}
	if beforeID != afterID {
		if err := push(out, OpenApiDeltaOperationIDChanged, location,
			"operationId changed: "+beforeID+" → "+afterID); err != nil {
			return err
		}
	}
	if err := compareRequestBody(before, after, location, out); err != nil {
		return err
	}
	return compareResponses(before, after, location, out)
}

func comparePathItem(before, after map[string]any, location string, out *openApiDeltaSink) error {
	if err := compareParameters(field(before, "parameters"), field(after, "parameters"),
		location+"/parameters", out); err != nil {
		return err
	}
	beforeMethods := make([]string, 0, len(before))
	for _, key := range sortedKeys(before) {
		if httpMethods[key] {
			beforeMethods = append(beforeMethods, key)
		}
	}
	afterMethods := make([]string, 0, len(after))
	for _, key := range sortedKeys(after) {
		if httpMethods[key] {
			afterMethods = append(afterMethods, key)
		}
	}
	for _, method := range unionSorted(beforeMethods, afterMethods) {
		operationLocation := location + "/" + method
		operationBefore, beforeExists := before[method]
		operationAfter, afterExists := after[method]
		if !beforeExists {
			if err := push(out, OpenApiDeltaOperationAdded, operationLocation,
				"operation "+strings.ToUpper(method)+" added"); err != nil {
				return err
			}
			continue
		}
		if !afterExists {
			if err := push(out, OpenApiDeltaOperationRemoved, operationLocation,
				"operation "+strings.ToUpper(method)+" removed"); err != nil {
				return err
			}
			continue
		}
		operationBeforeObj, beforePlain := asPlainObject(operationBefore)
		operationAfterObj, afterPlain := asPlainObject(operationAfter)
		if !beforePlain || !afterPlain {
			return newOpenApiClassifierError(operationLocation + ": operation objects must be objects on both sides")
		}
		if err := compareOperation(operationBeforeObj, operationAfterObj, operationLocation, out); err != nil {
			return err
		}
	}
	return nil
}

/* -------------------------------------------------------------- components */

func compareComponents(beforeDoc, afterDoc map[string]any, out *openApiDeltaSink) error {
	before, err := recordField(beforeDoc, "components", "")
	if err != nil {
		return err
	}
	after, err := recordField(afterDoc, "components", "")
	if err != nil {
		return err
	}
	if !before.present && !after.present {
		return nil
	}
	empty := map[string]any{}
	beforeRoot := empty
	if before.present {
		beforeRoot = before.value
	}
	afterRoot := empty
	if after.present {
		afterRoot = after.value
	}
	schemasBefore, err := recordField(beforeRoot, "schemas", "/components")
	if err != nil {
		return err
	}
	schemasAfter, err := recordField(afterRoot, "schemas", "/components")
	if err != nil {
		return err
	}
	if !schemasBefore.present && !schemasAfter.present {
		return nil
	}
	componentsLocation := "/components/schemas"
	if !schemasBefore.present || !schemasAfter.present {
		detail := "components.schemas added"
		if schemasBefore.present {
			detail = "components.schemas removed"
		}
		return push(out, OpenApiDeltaSchemaPropertyRemoved, componentsLocation, detail)
	}
	for _, name := range unionSorted(sortedKeys(schemasBefore.value), sortedKeys(schemasAfter.value)) {
		schemaLocation := componentsLocation + "/" + escapePointer(name)
		schemaBefore, beforeExists := schemasBefore.value[name]
		schemaAfter, afterExists := schemasAfter.value[name]
		if !beforeExists {
			if err := push(out, OpenApiDeltaSchemaPropertyAdded, schemaLocation,
				"component schema '"+name+"' added"); err != nil {
				return err
			}
			continue
		}
		if !afterExists {
			if err := push(out, OpenApiDeltaSchemaPropertyRemoved, schemaLocation,
				"component schema '"+name+"' removed"); err != nil {
				return err
			}
			continue
		}
		if err := compareInlineSchema(schemaBefore, schemaAfter, schemaLocation,
			OpenApiDeltaSchemaPropertyTypeChanged, out); err != nil {
			return err
		}
	}
	return nil
}

/* ------------------------------------------------------------------- entry */

func entryOrder(entry OpenApiDeltaEntry) string {
	return entry.Location + "\u0000" + string(entry.Kind) + "\u0000" + entry.Detail
}

func buildSummary(deltas []OpenApiDeltaEntry) OpenApiDeltaSummary {
	summary := OpenApiDeltaSummary{}
	for _, delta := range deltas {
		switch delta.Tier {
		case OpenApiTierBreaking:
			summary.Breaking += 1
		case OpenApiTierBehaviorAnnotating:
			summary.BehaviorAnnotating += 1
		default:
			summary.NonBreaking += 1
		}
	}
	worstTier := OpenApiTierNonBreaking
	switch {
	case summary.Breaking > 0:
		worstTier = OpenApiTierBreaking
	case summary.BehaviorAnnotating > 0:
		worstTier = OpenApiTierBehaviorAnnotating
	}
	summary.WorstTier = worstTier
	return summary
}

// ClassifyOpenApiDelta classifies the delta between two parsed OpenAPI/Swagger
// documents (P11-T07; OD-R28 three-tier; V-P11-4). Deterministic: entries are
// ordered by (location, kind, detail); identical inputs always produce
// identical output. Fails closed with a typed *OpenApiClassifierError on
// structurally invalid inputs.
func ClassifyOpenApiDelta(before, after any) (OpenApiDeltaClassification, error) {
	beforeDoc, err := asApiDoc(before, "before")
	if err != nil {
		return OpenApiDeltaClassification{}, err
	}
	afterDoc, err := asApiDoc(after, "after")
	if err != nil {
		return OpenApiDeltaClassification{}, err
	}
	out := &openApiDeltaSink{}

	pathsBefore, err := recordField(beforeDoc, "paths", "/")
	if err != nil {
		return OpenApiDeltaClassification{}, err
	}
	pathsAfter, err := recordField(afterDoc, "paths", "/")
	if err != nil {
		return OpenApiDeltaClassification{}, err
	}
	emptyPaths := map[string]any{}
	pathsBeforeValue := emptyPaths
	if pathsBefore.present {
		pathsBeforeValue = pathsBefore.value
	}
	pathsAfterValue := emptyPaths
	if pathsAfter.present {
		pathsAfterValue = pathsAfter.value
	}
	for _, path := range unionSorted(sortedKeys(pathsBeforeValue), sortedKeys(pathsAfterValue)) {
		location := "/paths/" + escapePointer(path)
		itemBefore, beforeExists := pathsBeforeValue[path]
		itemAfter, afterExists := pathsAfterValue[path]
		if !beforeExists {
			if err := push(out, OpenApiDeltaPathAdded, location, "path '"+path+"' added"); err != nil {
				return OpenApiDeltaClassification{}, err
			}
			continue
		}
		if !afterExists {
			if err := push(out, OpenApiDeltaPathRemoved, location, "path '"+path+"' removed"); err != nil {
				return OpenApiDeltaClassification{}, err
			}
			continue
		}
		itemBeforeObj, beforePlain := asPlainObject(itemBefore)
		itemAfterObj, afterPlain := asPlainObject(itemAfter)
		if !beforePlain || !afterPlain {
			return OpenApiDeltaClassification{}, newOpenApiClassifierError(
				location + ": path item must be an object on both sides")
		}
		if err := comparePathItem(itemBeforeObj, itemAfterObj, location, out); err != nil {
			return OpenApiDeltaClassification{}, err
		}
	}
	if err := compareComponents(beforeDoc, afterDoc, out); err != nil {
		return OpenApiDeltaClassification{}, err
	}

	deltas := out.deltas
	sort.SliceStable(deltas, func(i, j int) bool {
		return compareStrings(entryOrder(deltas[i]), entryOrder(deltas[j])) < 0
	})
	if deltas == nil {
		deltas = []OpenApiDeltaEntry{}
	}
	return OpenApiDeltaClassification{
		Deltas:       deltas,
		Summary:      buildSummary(deltas),
		RulesVersion: OpenAPIDeltaRules.Version,
	}, nil
}
