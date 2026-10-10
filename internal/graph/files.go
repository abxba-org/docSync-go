package graph

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Graph-as-files serialization (Phase-11, P11-T09; ARCH §4.2: the document graph
// is "stored as reviewable files under `.docsync/`", repository-versioned). The
// serialized form is the versioned snapshot of the graph — one canonical-JSON
// envelope, deterministic bytes (same graph → same bytes), so it is Git-diffable
// and reviewable like every `.docsync/` artifact.
//
// Fail-closed parse (D8): ParseGraph is STRICT — unknown top-level keys, unknown
// mapping fields, unknown contract kinds, unknown config-error kinds and
// malformed values are REFUSED with a *GraphParseError, never coerced or silently
// dropped. Forward-compatible evolution happens through a schema version bump
// (GraphSerializationSchema today), never through silent leniency.

// GraphSerializationSchema is the serialization schema version (additive field
// policy; bump to evolve).
const GraphSerializationSchema = "docsync.graph/v1"

// mappingFields and stringArrayFields mirror the TS MAPPING_FIELDS /
// STRING_ARRAY_FIELDS constants.
var (
	mappingFields   = []string{"doc", "sources", "concepts", "contracts", "apis", "configs", "workflows"}
	mappingFieldSet = func() map[string]struct{} {
		s := make(map[string]struct{}, len(mappingFields))
		for _, f := range mappingFields {
			s[f] = struct{}{}
		}
		return s
	}()
	stringArrayFields = []string{"sources", "concepts", "contracts", "apis", "configs", "workflows"}
)

// SerializeGraph serializes the graph to deterministic canonical-JSON bytes
// (same graph → same bytes).
func SerializeGraph(graph DocumentGraph) (string, error) {
	mappings := make([]any, 0, len(graph.Mappings))
	for _, m := range graph.Mappings {
		mappings = append(mappings, mappingToMap(m))
	}
	contractKinds := make(map[string]any, len(graph.ContractKinds))
	for name, kind := range graph.ContractKinds {
		contractKinds[name] = string(kind)
	}
	errs := make([]any, 0, len(graph.Errors))
	for _, e := range graph.Errors {
		errs = append(errs, errorToMap(e))
	}
	envelope := map[string]any{
		"schema":        GraphSerializationSchema,
		"mappings":      mappings,
		"contractKinds": contractKinds,
		"errors":        errs,
	}
	return docutil.CanonicalJSON(envelope)
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func mappingToMap(m MappingRecord) map[string]any {
	out := map[string]any{
		"doc":       m.Doc,
		"sources":   toAnySlice(m.Sources),
		"concepts":  toAnySlice(m.Concepts),
		"contracts": toAnySlice(m.Contracts),
	}
	if m.APIs != nil {
		out["apis"] = toAnySlice(m.APIs)
	}
	if m.Configs != nil {
		out["configs"] = toAnySlice(m.Configs)
	}
	if m.Workflows != nil {
		out["workflows"] = toAnySlice(m.Workflows)
	}
	return out
}

func errorToMap(e GraphConfigError) map[string]any {
	out := map[string]any{
		"kind":    string(e.Kind),
		"path":    e.Path,
		"message": e.Message,
	}
	if e.Line != nil {
		out["line"] = *e.Line
	}
	return out
}

// ParseGraph parses + strictly validates a serialized graph document (fail closed
// on any deviation).
func ParseGraph(text string) (DocumentGraph, error) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return DocumentGraph{}, NewGraphParseError(fmt.Sprintf("graph document is not valid JSON: %v", err))
	}
	envelope, ok := value.(map[string]any)
	if !ok {
		return DocumentGraph{}, NewGraphParseError("graph document must be a JSON object")
	}
	for _, key := range []string{"schema", "mappings", "contractKinds", "errors"} {
		if _, present := envelope[key]; !present {
			return DocumentGraph{}, NewGraphParseError(fmt.Sprintf("graph document is missing %q", key))
		}
	}
	for key := range envelope {
		switch key {
		case "schema", "mappings", "contractKinds", "errors":
		default:
			return DocumentGraph{}, NewGraphParseError(fmt.Sprintf("graph document has unknown top-level key %q", key))
		}
	}
	if schema := jsString(envelope["schema"]); schema != GraphSerializationSchema {
		return DocumentGraph{}, NewGraphParseError(fmt.Sprintf("graph document schema must be %q, got %q", GraphSerializationSchema, schema))
	}
	mappings, err := parseMappings(envelope["mappings"])
	if err != nil {
		return DocumentGraph{}, err
	}
	contractKinds, err := parseContractKinds(envelope["contractKinds"])
	if err != nil {
		return DocumentGraph{}, err
	}
	graphErrors, err := parseErrors(envelope["errors"])
	if err != nil {
		return DocumentGraph{}, err
	}
	return DocumentGraph{Mappings: mappings, ContractKinds: contractKinds, Errors: graphErrors}, nil
}

func parseMappings(raw any) ([]MappingRecord, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, NewGraphParseError(`graph "mappings" must be an array`)
	}
	result := make([]MappingRecord, 0, len(arr))
	for i, entry := range arr {
		m, err := parseMapping(entry, i)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, nil
}

func parseMapping(raw any, index int) (MappingRecord, error) {
	record, ok := raw.(map[string]any)
	if !ok {
		return MappingRecord{}, NewGraphParseError(fmt.Sprintf("graph mapping[%d] must be an object", index))
	}
	for key := range record {
		if _, ok := mappingFieldSet[key]; !ok {
			return MappingRecord{}, NewGraphParseError(fmt.Sprintf("graph mapping[%d] has unknown field %q", index, key))
		}
	}
	doc, ok := record["doc"].(string)
	if !ok || doc == "" {
		return MappingRecord{}, NewGraphParseError(fmt.Sprintf("graph mapping[%d].doc must be a non-empty string", index))
	}
	m := MappingRecord{Doc: doc, Concepts: []string{}, Contracts: []string{}}
	for _, field := range stringArrayFields {
		value, present := record[field]
		if !present {
			if field == "sources" {
				return MappingRecord{}, NewGraphParseError(fmt.Sprintf("graph mapping[%d].sources must be present", index))
			}
			continue
		}
		arr, ok := value.([]any)
		if !ok {
			return MappingRecord{}, NewGraphParseError(fmt.Sprintf("graph mapping[%d].%s must be an array of strings", index, field))
		}
		strs := make([]string, 0, len(arr))
		for entryIndex, e := range arr {
			s, ok := e.(string)
			if !ok || s == "" {
				return MappingRecord{}, NewGraphParseError(fmt.Sprintf("graph mapping[%d].%s[%d] must be a non-empty string", index, field, entryIndex))
			}
			strs = append(strs, s)
		}
		switch field {
		case "sources":
			m.Sources = strs
		case "concepts":
			m.Concepts = strs
		case "contracts":
			m.Contracts = strs
		case "apis":
			m.APIs = strs
		case "configs":
			m.Configs = strs
		case "workflows":
			m.Workflows = strs
		}
	}
	return m, nil
}

func parseContractKinds(raw any) (map[string]schemas.ContractKind, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, NewGraphParseError(`graph "contractKinds" must be an object`)
	}
	kinds := make(map[string]schemas.ContractKind, len(obj))
	for name, kind := range obj {
		if len(name) == 0 {
			return nil, NewGraphParseError("graph contractKinds keys must be non-empty")
		}
		kindStr := jsString(kind)
		if !schemas.IsValidContractKind(kindStr) {
			return nil, NewGraphParseError(fmt.Sprintf("graph contractKinds[%q] has unknown kind %q — kinds: %s", name, kindStr, joinContractKindValues()))
		}
		kinds[name] = schemas.ContractKind(kindStr)
	}
	return kinds, nil
}

func parseErrors(raw any) ([]GraphConfigError, error) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, NewGraphParseError(`graph "errors" must be an array`)
	}
	result := make([]GraphConfigError, 0, len(arr))
	for i, entry := range arr {
		e, err := parseError(entry, i)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, nil
}

func parseError(raw any, index int) (GraphConfigError, error) {
	record, ok := raw.(map[string]any)
	if !ok {
		return GraphConfigError{}, NewGraphParseError(fmt.Sprintf("graph errors[%d] must be an object", index))
	}
	for key := range record {
		if key != "kind" && key != "path" && key != "message" && key != "line" {
			return GraphConfigError{}, NewGraphParseError(fmt.Sprintf("graph errors[%d] has unknown field %q", index, key))
		}
	}
	kind, ok := record["kind"].(string)
	if !ok || !isGraphConfigErrorKind(kind) {
		return GraphConfigError{}, NewGraphParseError(fmt.Sprintf("graph errors[%d].kind is unknown %q — kinds: %s", index, jsString(record["kind"]), joinGraphConfigErrorKindValues()))
	}
	for _, field := range []string{"path", "message"} {
		s, ok := record[field].(string)
		if !ok || s == "" {
			return GraphConfigError{}, NewGraphParseError(fmt.Sprintf("graph errors[%d].%s must be a non-empty string", index, field))
		}
	}
	e := GraphConfigError{
		Kind:    GraphConfigErrorKind(kind),
		Path:    record["path"].(string),
		Message: record["message"].(string),
	}
	if lineVal, present := record["line"]; present {
		f, ok := lineVal.(float64)
		if !ok || f != math.Trunc(f) || f < 1 {
			return GraphConfigError{}, NewGraphParseError(fmt.Sprintf("graph errors[%d].line must be a positive integer", index))
		}
		lineInt := int(f)
		e.Line = &lineInt
	}
	return e, nil
}

// isGraphConfigErrorKind reports membership in GraphConfigErrorKindValues.
func isGraphConfigErrorKind(v string) bool {
	for _, k := range GraphConfigErrorKindValues {
		if string(k) == v {
			return true
		}
	}
	return false
}

// joinGraphConfigErrorKindValues renders GraphConfigErrorKindValues as the
// comma-separated list used in error messages.
func joinGraphConfigErrorKindValues() string {
	parts := make([]string, len(GraphConfigErrorKindValues))
	for i, k := range GraphConfigErrorKindValues {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}

// jsString renders a decoded JSON value the way JavaScript's String() would, so
// fail-closed parse messages that echo an offending value match the TS text for
// the common string/number/boolean cases.
func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.6f", t), "0"), ".")
	default:
		return fmt.Sprintf("%v", t)
	}
}
