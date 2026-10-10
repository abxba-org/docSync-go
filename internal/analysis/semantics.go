package analysis

// Semantic differs (P1-T05; OD-R06): path families, the change-level
// dependency flag, and per-file API/config semantic diffs.
//
// Ported from core/src/analysis/semantics.ts (traceability, plan.md §7).
//
// OD-R06 binding: key-level diffing with typed value parsing for a SMALL
// family set only — `.env` (KEY=VALUE), `.json` (JSON), `.yaml`/`.yml`
// (the repo's existing `yaml` dependency). Everything else is a DECLARED GAP:
// the result carries `config_diff: undefined` ("not assessed") plus a fixed
// note — never a guessed false/true, and never a parse failure silently read
// as "no config change" (D14: analyzer/assessor failure is inconclusive, not
// negative).
//
// All functions are PURE over their inputs — no fs/network/git/clock. YAML
// parsing reads the FIRST document of a stream (declared V1 limitation) and
// YAML key extraction is shallow by design: top-level keys plus ONE nested
// level; deeper objects are compared as canonical-JSON leaf blobs. JSON walks
// nested objects without a depth limit.
//
// The `api_diff` result is a V1 boolean flag plus a note (OD-R28: the 3-tier
// delta classifier is Phase 11 — see openapi.go).

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

/* ------------------------------------------------------------- path families */

// PathFamily is the family taxonomy driving which semantic differs run (P1-T05).
type PathFamily string

const (
	PathFamilyDependencyManifest PathFamily = "dependency-manifest"
	PathFamilyOpenAPI            PathFamily = "openapi"
	PathFamilyConfig             PathFamily = "config"
	PathFamilySource             PathFamily = "source"
	PathFamilyTest               PathFamily = "test"
	PathFamilyOther              PathFamily = "other"
)

// Family precedence (documented, first match wins): dependency-manifest and
// openapi are precise filename conventions that must not be diluted by the
// broader config-extension rule; config beats test so a `.env.test` file is
// still semantically diffable; test beats source so test paths keep their
// family even when they carry source extensions.
var dependencyManifestBasenames = map[string]bool{
	"package.json":      true,
	"pnpm-lock.yaml":    true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"requirements.txt":  true,
	"pyproject.toml":    true,
	"go.mod":            true,
	"Cargo.toml":        true,
	"Cargo.lock":        true,
}

var openapiSpecBasenames = map[string]bool{
	"openapi.json": true,
	"openapi.yaml": true,
	"openapi.yml":  true,
	"swagger.json": true,
	"swagger.yaml": true,
	"swagger.yml":  true,
}

var configFileExtensions = map[string]bool{
	"json": true,
	"yaml": true,
	"yml":  true,
	"toml": true,
	"ini":  true,
}

var sourceFileExtensions = map[string]bool{
	"ts": true, "tsx": true, "mts": true, "cts": true,
	"js": true, "jsx": true, "mjs": true, "cjs": true,
	"py": true, "go": true, "rs": true, "java": true, "rb": true,
	"kt": true, "kts": true, "swift": true, "c": true, "h": true,
	"cpp": true, "hpp": true, "cc": true, "cs": true, "php": true,
	"scala": true, "sh": true,
}

// PathOnlyFile is the minimal `{ path: string }` structural input mirrored from
// the TypeScript signatures (TS: `ReadonlyArray<{ path: string }>` for
// dependencyChanged and `Pick<DetectorFile, 'path'>` for apiDiffFor).
type PathOnlyFile struct {
	Path string `json:"path"`
}

func basenameOf(p string) string {
	if p == "" {
		// node:path posix basename("") is "", path.Base("") is ".".
		return ""
	}
	return path.Base(p)
}

func extensionOf(base string) string {
	dot := strings.LastIndex(base, ".")
	// dot <= 0 covers dotfiles like `.env` (no extension) — `gitignore`-style.
	if dot <= 0 {
		return ""
	}
	return strings.ToLower(base[dot+1:])
}

// ClassifyPathFamily classifies one changed path into its semantic family
// (P1-T05; OD-R06). Deterministic and pure: the same path always yields the
// same family.
func ClassifyPathFamily(p string) PathFamily {
	base := basenameOf(p)
	if dependencyManifestBasenames[base] {
		return PathFamilyDependencyManifest
	}
	if openapiSpecBasenames[base] || strings.HasSuffix(base, ".schema.json") {
		return PathFamilyOpenAPI
	}
	if strings.HasPrefix(base, ".env") || configFileExtensions[extensionOf(base)] {
		return PathFamilyConfig
	}
	// isTestOnlyFile is ported from core/src/analysis/detect.ts (owned by the
	// detect.go writer in this package — reused, never redefined).
	if IsTestOnlyFile(p) {
		return PathFamilyTest
	}
	if sourceFileExtensions[extensionOf(base)] {
		return PathFamilySource
	}
	return PathFamilyOther
}

/* --------------------------------------------------------- change-level dep */

// DependencyChanged is the change-level `dependencies_changed` flag: true iff
// ANY changed path is in the manifest family.
func DependencyChanged(files []PathOnlyFile) bool {
	for _, file := range files {
		if ClassifyPathFamily(file.Path) == PathFamilyDependencyManifest {
			return true
		}
	}
	return false
}

/* -------------------------------------------------------------- api differs */

// ApiDiffResult is the V1 API-contract signal (OD-R28).
type ApiDiffResult struct {
	ApiDiff bool   `json:"api_diff"`
	Note    string `json:"note"`
}

// OpenAPIDiffNote is the fixed note carried by a positive api_diff result.
const OpenAPIDiffNote = "OpenAPI/Swagger family path changed — API contract may differ (V1: boolean flag; 3-tier delta classifier lands in Phase 11 per OD-R28)"

// ApiDiffFor returns the V1 API-contract signal for one changed file:
// openapi-family paths → `api_diff: true` plus a fixed note; anything else →
// nil (not assessed — absence is the declared "no signal", never a negative
// result).
func ApiDiffFor(file PathOnlyFile) *ApiDiffResult {
	if ClassifyPathFamily(file.Path) != PathFamilyOpenAPI {
		return nil
	}
	return &ApiDiffResult{ApiDiff: true, Note: OpenAPIDiffNote}
}

/* ----------------------------------------------------------- config differs */

// ConfigDiffResult is the P1-T05 semantic config diff result.
//
// ConfigDiff pointer tri-state (mirroring the TS optional field):
//   - non-nil true  = assessed and keys changed;
//   - non-nil false = assessed and unchanged;
//   - nil           = NOT assessed (declared gap — unparseable input or an
//     unsupported format; D14: never a silent false negative).
//
// Note is present for true and for declared gaps (fixed deterministic
// evidence text).
type ConfigDiffResult struct {
	ConfigDiff *bool   `json:"config_diff,omitempty"`
	Note       *string `json:"note,omitempty"`
}

const unparseableNote = "unparseable — semantic config diff unavailable"
const maxKeysInNote = 20
const configKeySeparator = "; "

// ConfigDiffFor is the P1-T05 semantic config diff. It returns nil for
// non-config paths (the caller should not have asked), a declared-gap result
// when either side cannot be parsed (or the format has no V1 parser),
// `{config_diff: false}` when both sides parse and no key changed, and
// `{config_diff: true, note}` with a deterministic sorted `key: old → new`
// list (typed values kept bare; strings quoted; capped at 20 keys with a
// trailing ellipsis).
func ConfigDiffFor(p, oldContent, newContent string) *ConfigDiffResult {
	if ClassifyPathFamily(p) != PathFamilyConfig {
		return nil
	}
	base := basenameOf(p)
	ext := extensionOf(base)
	if ext == "toml" || ext == "ini" {
		note := "toml/ini semantic config diff unavailable in V1 — declared gap (D14)"
		return &ConfigDiffResult{Note: &note}
	}
	if strings.HasPrefix(base, ".env") {
		oldMap := parseEnv(oldContent)
		newMap := parseEnv(newContent)
		if oldMap == nil || newMap == nil {
			return gapResult()
		}
		return diffConfigMaps(oldMap, newMap)
	}
	if ext == "json" {
		oldRoot := parseJSONObject(oldContent)
		newRoot := parseJSONObject(newContent)
		if oldRoot == nil || newRoot == nil {
			return gapResult()
		}
		oldMap := map[string]any{}
		collectJSONKeys(oldRoot, "", oldMap)
		newMap := map[string]any{}
		collectJSONKeys(newRoot, "", newMap)
		return diffConfigMaps(oldMap, newMap)
	}
	if ext == "yaml" || ext == "yml" {
		oldMap := collectYAMLKeys(oldContent)
		newMap := collectYAMLKeys(newContent)
		if oldMap == nil || newMap == nil {
			return gapResult()
		}
		return diffConfigMaps(oldMap, newMap)
	}
	// Unreachable for the current config family definition — fail closed with
	// a declared gap rather than guessing if the family set ever grows.
	label := ext
	if label == "" {
		label = "(no extension)"
	}
	note := "semantic config diff unavailable for ." + label + " in V1 — declared gap (D14)"
	return &ConfigDiffResult{Note: &note}
}

func gapResult() *ConfigDiffResult {
	note := unparseableNote
	return &ConfigDiffResult{Note: &note}
}

/* ------------------------------------------------------------- key walkers */

// parseJSONObject parses text and returns the root plain object (TS: JSON.parse
// plus isPlainObject; a non-object root or a parse failure yields nil).
func parseJSONObject(text string) map[string]any {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

// collectJSONKeys walks nested objects without a depth limit; scalars and
// arrays are leaves.
func collectJSONKeys(value map[string]any, prefix string, out map[string]any) {
	for key, child := range value {
		keyPath := key
		if prefix != "" {
			keyPath = prefix + "." + key
		}
		if childObj, ok := child.(map[string]any); ok {
			collectJSONKeys(childObj, keyPath, out)
		} else {
			out[keyPath] = child // scalars and arrays are leaves
		}
	}
}

// yamlMaxKeyDepth caps YAML key extraction at top-level keys plus ONE nested
// level (OD-R06 scope).
const yamlMaxKeyDepth = 1

// collectYAMLKeys collects the YAML key map for a config document, or nil when
// the document cannot be parsed / is not a plain-object root / is not
// JSON-representable (declared gap, D14).
func collectYAMLKeys(text string) map[string]any {
	root, ok := parseYAMLDocument(text)
	if !ok {
		return nil
	}
	out := map[string]any{}
	collectYAMLLevel(root, "", 0, out)
	return out
}

// collectYAMLLevel walks one mapping level; deeper objects compare as leaf
// blobs (declared shallow scope).
func collectYAMLLevel(value map[string]any, prefix string, depth int, out map[string]any) {
	for key, child := range value {
		keyPath := key
		if prefix != "" {
			keyPath = prefix + "." + key
		}
		if childObj, ok := child.(map[string]any); ok && depth < yamlMaxKeyDepth {
			collectYAMLLevel(childObj, keyPath, depth+1, out)
		} else {
			out[keyPath] = child // deeper objects compare as leaf blobs
		}
	}
}

/* ------------------------------------------------------- yaml document walk */

// parseYAMLDocument parses the FIRST document of a YAML stream and converts it
// to the generic value tree the TS `yaml` dependency's `toJS()` would produce
// (plain objects, arrays, JSON scalars). It returns ok=false for every
// declared-gap input: parse errors, multiple documents (eemeli's
// parseDocument reports "Source contains multiple documents"), an empty
// document (toJS → null), a non-mapping root, and any value the TS
// implementation could not canonical-JSON (Dates, Maps, anchor cycles).
func parseYAMLDocument(text string) (map[string]any, bool) {
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		// Empty document (toJS: null) or a parse error — both are gaps.
		return nil, false
	}
	var second yaml.Node
	switch err := decoder.Decode(&second); {
	case err == nil:
		return nil, false // multiple documents — eemeli parseDocument errors
	case errors.Is(err, io.EOF):
		// Exactly one document — the supported V1 case.
	default:
		return nil, false
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false // null contents — not a plain object
	}
	seen := map[*yaml.Node]bool{}
	value, ok := yamlNodeValue(doc.Content[0], seen)
	if !ok {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, false // root must be a plain object
	}
	return obj, true
}

// yamlNodeValue converts one YAML node to its toJS()-equivalent generic value,
// mirroring the TS isJsonSafe predicate (anything not JSON representable, and
// any node reachable twice through an anchor — cycle OR shared reference —
// yields ok=false, i.e. the declared gap).
func yamlNodeValue(node *yaml.Node, seen map[*yaml.Node]bool) (any, bool) {
	switch node.Kind {
	case yaml.AliasNode:
		if node.Alias == nil {
			return nil, false
		}
		return yamlNodeValue(node.Alias, seen)
	case yaml.ScalarNode:
		return yamlScalarValue(node, false)
	case yaml.SequenceNode:
		if node.Tag != "" && node.Tag != "!!seq" {
			return nil, false // e.g. an explicit !!omap/!!pairs tag — not a plain array
		}
		if seen[node] {
			return nil, false
		}
		seen[node] = true
		items := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			converted, ok := yamlNodeValue(item, seen)
			if !ok {
				return nil, false
			}
			items = append(items, converted)
		}
		return items, true
	case yaml.MappingNode:
		if node.Tag != "" && node.Tag != "!!map" {
			return nil, false // e.g. an explicit !!set tag — not a plain object
		}
		if seen[node] {
			return nil, false
		}
		seen[node] = true
		out := make(map[string]any, len(node.Content)/2)
		identities := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, identity, ok := yamlMappingKey(node.Content[i], seen)
			if !ok {
				return nil, false
			}
			if identities[identity] {
				// eemeli reports "Map keys must be unique" — a parse error, so
				// the TS side lands in the declared gap. Keep that (D14).
				return nil, false
			}
			identities[identity] = true
			converted, ok := yamlNodeValue(node.Content[i+1], seen)
			if !ok {
				return nil, false
			}
			// Distinct identities that stringify to the same key share one
			// entry, last one winning — exactly like the JS object the TS
			// `yaml` dependency builds.
			out[key] = converted
		}
		return out, true
	default:
		return nil, false
	}
}

// yamlMappingKey resolves a mapping key node the way the TS `yaml` dependency
// does when building a plain object: scalar keys are stringified via JS
// String(...) semantics ("1", "true", "1.5", "Infinity", …), and a null key
// becomes the empty string. The second return value is the key identity the TS
// side compares for duplicates — a number 1 and a string "1" stay distinct
// exactly like eemeli's Map. Collection keys are a declared gap (eemeli
// stringifies their node form, which no docSync config relies on; fail closed
// per D14).
func yamlMappingKey(node *yaml.Node, seen map[*yaml.Node]bool) (string, string, bool) {
	if node.Kind == yaml.AliasNode {
		if node.Alias == nil {
			return "", "", false
		}
		node = node.Alias
	}
	if node.Kind != yaml.ScalarNode {
		return "", "", false
	}
	value, ok := yamlScalarValue(node, true)
	if !ok {
		return "", "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, "s:" + typed, true
	case float64:
		return formatJSNumber(typed), "n:" + strconv.FormatFloat(typed, 'g', -1, 64), true
	case bool:
		if typed {
			return "true", "b:true", true
		}
		return "false", "b:false", true
	case nil:
		// eemeli renders a null object key as the empty string.
		return "", "z:null", true
	default:
		return "", "", false
	}
}

// yamlScalarValue resolves one scalar node. Plain/quoted/block scalars follow
// the eemeli `yaml` core-schema (YAML 1.2) resolution rules applied to the
// source text — the Go parser additionally auto-detects YAML-1.1-only forms
// (timestamps, sexagesimals) that eemeli's core schema leaves as strings, so
// the text decides for plain scalars. Explicit non-core tags (!!timestamp,
// !!binary, …) resolve to Date/Buffer values in eemeli, which canonical JSON
// cannot serialize — the declared gap.
//
// asKey selects key-position semantics: eemeli's default core schema has merge
// keys disabled, so `<<` is an ordinary string key there, while an explicit
// !!merge VALUE is unresolvable (eemeli parse error → declared gap).
func yamlScalarValue(node *yaml.Node, asKey bool) (any, bool) {
	if asKey && node.Tag == "!!merge" {
		// eemeli's default core schema (merge: false) treats `<<` as an
		// ordinary string key — port that exactly.
		return "<<", true
	}
	resolved, coreTag := resolveYAMLScalarText(node.Value)
	if node.Style&yaml.TaggedStyle != 0 {
		// An explicit tag: eemeli resolves through the tag when the source
		// text satisfies the tag's test, keeps the raw string otherwise, and
		// resolves the YAML-1.1 tags (!!timestamp, !!binary, …) to Date/Buffer
		// values canonical JSON cannot serialize — the declared gap.
		switch {
		case node.Tag == "!!str":
			return node.Value, true
		case isCoreYAMLTag(node.Tag) && yamlTagTestPasses(node.Tag, node.Value):
			return resolved, true
		case isCoreYAMLTag(node.Tag):
			return node.Value, true
		default:
			return nil, false
		}
	}
	switch {
	case node.Tag == coreTag:
		return resolved, true
	case node.Tag == "!!str":
		// Quoted/literal/folded scalars are strings in every schema.
		return node.Value, true
	case node.Tag == "!!merge":
		// eemeli's core schema has merge keys disabled, so a plain `<<` is an
		// ordinary string here too.
		return node.Value, true
	case node.Tag == "!!timestamp":
		// The Go parser auto-detects YAML-1.1 timestamps; eemeli's core schema
		// does not, so an untagged date stays the string the text resolves to.
		return resolved, true
	case node.Tag == "":
		return resolved, true
	case node.Style == 0 && isCoreYAMLTag(node.Tag):
		// Plain text the Go parser tagged with a core type eemeli's core schema
		// resolves differently (integers beyond int64 tag as !!float;
		// underscores, sexagesimals, …): eemeli resolves the text itself, so
		// honour the text.
		return resolved, true
	default:
		return nil, false
	}
}

// yamlTagTestPasses reports whether text satisfies the eemeli core-schema test
// of the given scalar tag.
func yamlTagTestPasses(tag, text string) bool {
	switch tag {
	case "!!null":
		return yamlNullRe.MatchString(text)
	case "!!bool":
		return yamlBoolRe.MatchString(text)
	case "!!int":
		return yamlIntRe.MatchString(text) || yamlIntHexRe.MatchString(text) || yamlIntOctRe.MatchString(text)
	case "!!float":
		return yamlFloatRe.MatchString(text) || yamlFloatExpRe.MatchString(text) || yamlFloatNaNRe.MatchString(text)
	default:
		return true
	}
}

func isCoreYAMLTag(tag string) bool {
	switch tag {
	case "!!null", "!!bool", "!!int", "!!float", "!!str":
		return true
	default:
		return false
	}
}

// The eemeli `yaml` core-schema scalar tests (dist/schema/core/{bool,float,int}.js
// and dist/schema/common/null.js), ported verbatim. Every number resolves to a
// float64 — JS has one number type.
var (
	yamlBoolRe     = regexp.MustCompile(`^(?:[Tt]rue|TRUE|[Ff]alse|FALSE)$`)
	yamlNullRe     = regexp.MustCompile(`^(?:~|[Nn]ull|NULL)?$`)
	yamlIntRe      = regexp.MustCompile(`^[-+]?[0-9]+$`)
	yamlIntHexRe   = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	yamlIntOctRe   = regexp.MustCompile(`^0o[0-7]+$`)
	yamlFloatRe    = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+\.[0-9]*)$`)
	yamlFloatExpRe = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)[eE][-+]?[0-9]+$`)
	yamlFloatNaNRe = regexp.MustCompile(`^(?:[-+]?\.(?:inf|Inf|INF)|\.nan|\.NaN|\.NAN)$`)
)

func resolveYAMLScalarText(text string) (any, string) {
	switch {
	case yamlNullRe.MatchString(text):
		return nil, "!!null"
	case yamlBoolRe.MatchString(text):
		return text[0] == 't' || text[0] == 'T', "!!bool"
	case yamlIntRe.MatchString(text):
		return yamlParseInt(text), "!!int"
	case yamlIntHexRe.MatchString(text):
		return yamlParseRadix(text[2:], 16), "!!int"
	case yamlIntOctRe.MatchString(text):
		return yamlParseRadix(text[2:], 8), "!!int"
	case yamlFloatRe.MatchString(text), yamlFloatExpRe.MatchString(text):
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return text, "!!str"
		}
		return value, "!!float"
	case yamlFloatNaNRe.MatchString(text):
		if strings.EqualFold(text[len(text)-3:], "nan") {
			return math.NaN(), "!!float"
		}
		if strings.HasPrefix(text, "-") {
			return math.Inf(-1), "!!float"
		}
		return math.Inf(1), "!!float"
	default:
		return text, "!!str"
	}
}

// yamlParseInt mirrors parseInt(str, 10): JS integers saturate to float64.
func yamlParseInt(text string) float64 {
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		// Out of int64 range: JS keeps parsing as a float64 (precision loss).
		fallback, ferr := strconv.ParseFloat(text, 64)
		if ferr != nil {
			return math.NaN()
		}
		return fallback
	}
	return float64(value)
}

func yamlParseRadix(digits string, radix int) float64 {
	value, err := strconv.ParseInt(digits, radix, 64)
	if err != nil {
		fallback, ferr := strconv.ParseFloat(digits, 64)
		if ferr != nil {
			return math.NaN()
		}
		return fallback
	}
	return float64(value)
}

/* --------------------------------------------------------------- env parser */

var envNumberRe = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

// parseEnv parses `.env` KEY=VALUE text: blank/`#` lines skipped, optional
// `export ` prefix stripped, values type-parsed (true/false/null/number
// bare, symmetric quotes stripped, anything else a string). A non-empty
// non-comment line without `=` is a parse failure → nil (the caller emits the
// declared gap).
func parseEnv(text string) map[string]any {
	out := map[string]any{}
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		body := line
		if strings.HasPrefix(body, "export ") || strings.HasPrefix(body, "export\t") {
			body = strings.TrimSpace(body[len("export "):])
		}
		eq := strings.Index(body, "=")
		if eq <= 0 {
			return nil // no '=' or empty key — fail closed, never guess
		}
		key := strings.TrimSpace(body[:eq])
		if key == "" {
			return nil
		}
		out[key] = parseEnvValue(strings.TrimSpace(body[eq+1:]))
	}
	return out
}

func parseEnvValue(raw string) any {
	switch raw {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if envNumberRe.MatchString(raw) {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return math.NaN()
		}
		return value
	}
	if len(raw) >= 2 && ((strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`)) ||
		(strings.HasPrefix(raw, "'") && strings.HasSuffix(raw, "'"))) {
		return raw[1 : len(raw)-1]
	}
	return raw
}

/* ----------------------------------------------------------------- diffing */

// keyChange is one changed config key with both typed sides. A side that never
// existed carries absentValue (TS `undefined`), rendered as "absent".
type keyChange struct {
	key      string
	oldValue any
	newValue any
}

// absentValue marks a key missing from one side (TS `undefined`).
type absentValue struct{}

var absent = absentValue{}

func isAbsent(value any) bool {
	_, ok := value.(absentValue)
	return ok
}

// diffConfigMaps diffs two key maps and returns the deterministic
// ConfigDiffResult, or nil when canonicalization fails (fail closed — an
// unreachable path for the pure parsers above, D14).
func diffConfigMaps(oldMap, newMap map[string]any) *ConfigDiffResult {
	keys := make([]string, 0, len(oldMap)+len(newMap))
	seen := make(map[string]bool, len(oldMap)+len(newMap))
	for key := range oldMap {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for key := range newMap {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	changes := make([]keyChange, 0, len(keys))
	for _, key := range keys {
		oldValue, oldOK := oldMap[key]
		if !oldOK {
			oldValue = absent
		}
		newValue, newOK := newMap[key]
		if !newOK {
			newValue = absent
		}
		equal, ok := configValueEquals(oldValue, newValue)
		if !ok {
			return nil
		}
		if !equal {
			changes = append(changes, keyChange{key: key, oldValue: oldValue, newValue: newValue})
		}
	}
	if len(changes) == 0 {
		unchanged := false
		return &ConfigDiffResult{ConfigDiff: &unchanged}
	}
	truncated := len(changes) > maxKeysInNote
	shown := make([]string, 0, maxKeysInNote)
	for _, change := range changes[:min(len(changes), maxKeysInNote)] {
		line, ok := renderKeyChange(change)
		if !ok {
			return nil
		}
		shown = append(shown, line)
	}
	note := "config keys changed (" + strconv.Itoa(len(changes)) + "): " + strings.Join(shown, configKeySeparator)
	if truncated {
		note += configKeySeparator + "…"
	}
	changed := true
	return &ConfigDiffResult{ConfigDiff: &changed, Note: &note}
}

func renderKeyChange(change keyChange) (string, bool) {
	oldText, ok := formatConfigValue(change.oldValue)
	if !ok {
		return "", false
	}
	newText, ok := formatConfigValue(change.newValue)
	if !ok {
		return "", false
	}
	return change.key + ": " + oldText + " → " + newText, true
}

// configValueEquals is the typed equality via canonical JSON (objects/arrays
// included; missing ≡ null — TS `canonicalJson(a ?? null) === canonicalJson(b ?? null)`).
func configValueEquals(a, b any) (bool, bool) {
	left, ok := canonicalOrNull(a)
	if !ok {
		return false, false
	}
	right, ok := canonicalOrNull(b)
	if !ok {
		return false, false
	}
	return left == right, true
}

// canonicalOrNull mirrors `canonicalJson(value ?? null)`: absent and null both
// canonicalize to "null"; non-finite numbers serialize like JSON.stringify
// (null), which is the only way a YAML `.inf`/`.nan` can reach here.
func canonicalOrNull(value any) (string, bool) {
	if value == nil || isAbsent(value) {
		return "null", true
	}
	if number, ok := value.(float64); ok && (math.IsInf(number, 0) || math.IsNaN(number)) {
		return "null", true
	}
	canonical, err := docutil.CanonicalJSON(value)
	if err != nil {
		return "", false
	}
	return canonical, true
}

// formatConfigValue is the typed rendering: true/false null bare; numbers via
// JS String(number) semantics; strings JSON.stringify'd; blobs canonical JSON;
// a missing side renders as "absent" (TS `undefined`).
func formatConfigValue(value any) (string, bool) {
	switch typed := value.(type) {
	case absentValue:
		return "absent", true
	case nil:
		return "null", true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	case float64:
		return formatJSNumber(typed), true
	case string:
		return jsonStringLiteral(typed), true
	default:
		canonical, err := docutil.CanonicalJSON(value)
		if err != nil {
			return "", false
		}
		return canonical, true
	}
}

// jsonStringLiteral quotes a string exactly like JSON.stringify (no HTML
// escaping — matching docutil's canonical writer).
func jsonStringLiteral(s string) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s); err != nil {
		return `""`
	}
	// Encode appends a newline; JSON.stringify does not emit one.
	return strings.TrimSuffix(buf.String(), "\n")
}

// formatJSNumber renders a float64 the way JS String(number) does: integers
// without a decimal point, shortest round-trip otherwise, "Infinity"/"NaN"
// for the non-finite values. Equivalent to Go's encoding/json float encoding.
func formatJSNumber(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	}
	abs := math.Abs(value)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	text := strconv.FormatFloat(value, format, -1, 64)
	if format == 'e' {
		// encoding/json cleanup: e-09 → e-9 (JS exponent form).
		if n := len(text); n >= 4 && text[n-4] == 'e' && text[n-3] == '-' && text[n-2] == '0' {
			text = text[:n-2] + text[n-1:]
		}
	}
	return text
}
