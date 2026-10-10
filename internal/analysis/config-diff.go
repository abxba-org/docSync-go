package analysis

// Key-level resolved-value config diffs (Phase 11, P11-T08; ARCH §4.1).
//
// Ported from core/src/analysis/config-diff.ts (traceability, plan.md §7).
//
// V1 config semantic diffs (P1-T05, semantics.go) were path/file-level with
// shallow key extraction; this module deepens them to KEY-LEVEL resolved
// values over already-parsed config objects (flat or nested).
//
// Conventions (binding, documented — ported verbatim from the TS header):
//   - RESOLVED means nested plain objects flatten to leaf keys with their
//     scalar values, joined as dot-paths (`server.port`,
//     `db.connection.host`).
//   - ARRAYS are whole-value leaves: an array never flattens; it compares as
//     one canonical-JSON blob under its key (documented convention, same as
//     the JSON walker in semantics.go).
//   - ENV REFERENCES: string values that are exactly `${VAR}` or `$VAR` are
//     reported as `env-ref` resolved values — the REFERENCE is the resolved
//     value at this layer. Reference strings pass through verbatim;
//     underlying environment values are never guessed here.
//   - SECRET MASKING: keys whose name matches ConfigSecretKeyPattern
//     (case-insensitive substring, ANY dot-path segment) are MASKED in the
//     diff detail — values are replaced with ConfigSecretMask — but the
//     CHANGE itself is still reported. Values never leak into records
//     (INV-8-adjacent honesty: presence of a change is evidence; secret
//     contents are not).
//   - A missing side of a key is omitted (`Before` for added, `After` for
//     removed); a root ARRAY is treated as ONE whole-value leaf under the
//     empty key; any other non-object root is a typed error (fail closed,
//     never guessed).
//
// All functions are PURE over their inputs — no fs/network/git/clock — and
// deterministic (output ordered by key, UTF-16 code-unit sort — see
// sortConfigKeys).
//
// Go type mapping (the shapes this repo's JSON/YAML parsers produce, the same
// doctrine as openapi.go's asPlainObject): a TS plain object is a
// `map[string]any`, a TS array is a `[]any`, and JSON scalars are `string`,
// `bool`, the Go integer/float kinds (one JS number) and nil (JSON null).
// Any other Go value (Date-like structs, bigints, funcs, channels, non-`[]any`
// slices, non-plain maps) is not JSON-representable here and fails closed,
// exactly like the TS isJsonSafe predicate rejected non-plain objects.
//
// This module's result types are ConfigKeyDiff / ConfigDiffSummary. The
// P1-T05 file-level ConfigDiffResult lives in semantics.go (landed, stable)
// and is deliberately not duplicated here.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

/* ---------------------------------------------------------- versioned data */

// ConfigSecretMask (TS CONFIG_SECRET_MASK) is the masked replacement for
// secret values in diff details (INV-8-adjacent).
const ConfigSecretMask = "[REDACTED]"

// ConfigSecretKeyPattern (TS CONFIG_SECRET_KEY_PATTERN) is the versioned
// masking rule: a key is secret-ish when ANY dot-path segment matches this
// pattern (case-insensitive substring). Versioned data, not a hard-coded
// call-site literal — policy versions may evolve it (D11-adjacent).
var ConfigSecretKeyPattern = regexp.MustCompile(`(?i)(?:password|secret|token|api_key|apikey|credential)`)

// ConfigEnvRefPattern (TS CONFIG_ENV_REF_PATTERN) is the versioned
// env-reference rule: a string value that is EXACTLY `${VAR}` or `$VAR`
// (identifier-style name). Such values are env references; the reference
// itself is the resolved value at this layer.
var ConfigEnvRefPattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$|^\$[A-Za-z_][A-Za-z0-9_]*$`)

/* ------------------------------------------------------------------- error */

// configSide mirrors the TS `'before' | 'after'` literal carried in error text.
type configSide string

const (
	configSideBefore configSide = "before"
	configSideAfter  configSide = "after"
)

// ConfigDiffInputCode is the code carried by the fail-closed config-diff input
// error (TS ConfigDiffInputError, EDOCSYNC_CONFIG_DIFF_INPUT).
// DEVIATION (recorded per the migration conventions): docutil's code list has
// no such code, so the closest existing one is used — EDOCSYNC_CONFIG, a
// fail-closed config input refusal — with the exact TS message text preserved
// (`config diff: <side> root is not diffable — <detail>`).
const ConfigDiffInputCode = docutil.CodeConfig

// newConfigDiffInputError builds the typed fail-closed error for a
// non-diffable config-diff input root. Never a guessed empty diff (D14).
func newConfigDiffInputError(side configSide, detail string) error {
	return docutil.New(ConfigDiffInputCode, "config diff: "+string(side)+" root is not diffable — "+detail)
}

/* ------------------------------------------------------------------- types */

// ConfigKeyDiffChange (TS ConfigKeyDiffChange) is the change classification
// for a single config key.
type ConfigKeyDiffChange string

const (
	ConfigKeyAdded   ConfigKeyDiffChange = "added"
	ConfigKeyRemoved ConfigKeyDiffChange = "removed"
	ConfigKeyChanged ConfigKeyDiffChange = "changed"
)

// ConfigKeyDiff (TS ConfigKeyDiff) is one key-level resolved-value diff entry.
//
// Before/After mirror the TS optional fields: a nil pointer means the side is
// omitted (added key → no Before; removed key → no After), while a present
// JSON null is a non-nil pointer to nil. Both carry the resolved value —
// masked (ConfigSecretMask) when SecretMasked, verbatim otherwise.
type ConfigKeyDiff struct {
	// Key is the dot-path of the leaf key (`server.port`); the empty string
	// is the whole root-array leaf.
	Key    string              `json:"key"`
	Before *any                `json:"before,omitempty"`
	After  *any                `json:"after,omitempty"`
	Change ConfigKeyDiffChange `json:"change"`
	// SecretMasked is true when the key matched ConfigSecretKeyPattern and
	// the values were masked.
	SecretMasked bool `json:"secretMasked"`
	// EnvRef is true when the reported value(s) are env references (passed
	// through verbatim); omitted when false, like the TS optional field.
	EnvRef bool `json:"envRef,omitempty"`
}

// ConfigDiffSummary (TS ConfigDiffSummary) holds aggregate counts over a
// key-level diff list.
type ConfigDiffSummary struct {
	Added             int `json:"added"`
	Removed           int `json:"removed"`
	Changed           int `json:"changed"`
	SecretMaskedCount int `json:"secretMaskedCount"`
}

/* -------------------------------------------------------------- the public API */

// DiffConfigKeys (TS diffConfigKeys) is the key-level resolved-value diff of
// two parsed config roots. Only keys whose resolved value differs appear;
// output is sorted by key (deterministic). Arrays (at any depth, including
// the root) are whole-value leaves; plain objects flatten recursively; any
// other non-object root — and any non-JSON-representable leaf — fails closed
// with the typed coded error (ConfigDiffInputCode), never a guessed result.
func DiffConfigKeys(before, after any) ([]ConfigKeyDiff, error) {
	beforeKeys, err := flattenRoot(before, configSideBefore)
	if err != nil {
		return nil, err
	}
	afterKeys, err := flattenRoot(after, configSideAfter)
	if err != nil {
		return nil, err
	}
	keys := unionSortedKeys(beforeKeys, afterKeys)
	diffs := make([]ConfigKeyDiff, 0, len(keys))
	for _, key := range keys {
		oldValue, hadBefore := beforeKeys[key]
		newValue, hadAfter := afterKeys[key]
		if hadBefore && hadAfter && resolvedValueEquals(oldValue, newValue) {
			continue
		}
		secretMasked := keyIsSecret(key)
		diff := ConfigKeyDiff{
			Key:          key,
			Change:       configKeyChangeFor(hadBefore, hadAfter),
			SecretMasked: secretMasked,
		}
		if hadBefore {
			before := maskedConfigValue(oldValue, secretMasked)
			diff.Before = &before
		}
		if hadAfter {
			after := maskedConfigValue(newValue, secretMasked)
			diff.After = &after
		}
		if isEnvRef(oldValue) || isEnvRef(newValue) {
			diff.EnvRef = true
		}
		diffs = append(diffs, diff)
	}
	return diffs, nil
}

// SummarizeConfigDiff (TS summarizeConfigDiff) counts per-change-class
// entries plus the number of entries whose values were secret-masked.
func SummarizeConfigDiff(diffs []ConfigKeyDiff) ConfigDiffSummary {
	var summary ConfigDiffSummary
	for _, diff := range diffs {
		switch diff.Change {
		case ConfigKeyAdded:
			summary.Added++
		case ConfigKeyRemoved:
			summary.Removed++
		default:
			// TS: anything that is not added/removed counts as changed.
			summary.Changed++
		}
		if diff.SecretMasked {
			summary.SecretMaskedCount++
		}
	}
	return summary
}

/* -------------------------------------------------------------- flattening */

// ResolvedKeys is one side's flattened leaf map (TS ResolvedKeys).
type ResolvedKeys = map[string]any

// flattenRoot (TS flattenRoot) resolves the root to its leaf keys. Plain
// objects walk recursively; arrays are ONE whole-value leaf under the empty
// key (documented convention); everything else is a fail-closed typed input
// error — never guessed into an empty diff.
func flattenRoot(root any, side configSide) (ResolvedKeys, error) {
	if obj, ok := asPlainObject(root); ok {
		out := make(ResolvedKeys, len(obj))
		if err := collectKeys(obj, "", out, side); err != nil {
			return nil, err
		}
		return out, nil
	}
	if array, ok := root.([]any); ok {
		if err := assertJSONLeaf(array, side, "root"); err != nil {
			return nil, err
		}
		return ResolvedKeys{"": array}, nil
	}
	return nil, newConfigDiffInputError(side, describeNonObjectRoot(root))
}

// describeNonObjectRoot (TS describeNonObjectRoot) renders the fail-closed
// detail for a non-object root. Go's nil conflates the TS null and undefined
// cases and reports "null" (the TS first branch); every other value renders
// its `typeof` name. Exotic Go values (funcs, channels, non-`[]any` slices,
// non-plain maps) fall into "object" — the same bucket openapi.go's
// describeValue uses.
func describeNonObjectRoot(root any) string {
	if root == nil {
		return "null"
	}
	return `type "` + configJSTypeName(root) + `"`
}

// configJSTypeName renders the JS `typeof` name for a value that is neither a
// plain object nor a `[]any` array.
func configJSTypeName(value any) string {
	switch value.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		return "number"
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return "number"
	default:
		return "object"
	}
}

// collectKeys (TS collectKeys) walks one plain-object level into leaf keys.
// Each level is walked in key order (UTF-16 code units) so that a literal
// dotted key colliding with a nested dot-path resolves deterministically —
// Go maps carry no insertion order, while the TS Object.entries iteration
// would resolve the same collision by textual order. The two agree whenever no
// such collision exists, which holds for every config these parsers emit.
func collectKeys(value map[string]any, prefix string, out ResolvedKeys, side configSide) error {
	for _, key := range sortedConfigKeys(value) {
		child := value[key]
		keyPath := key
		if prefix != "" {
			keyPath = prefix + "." + key
		}
		if childObj, ok := asPlainObject(child); ok {
			if err := collectKeys(childObj, keyPath, out, side); err != nil {
				return err
			}
			continue // empty nested objects flatten away
		}
		if err := assertJSONLeaf(child, side, keyPath); err != nil {
			return err
		}
		out[keyPath] = child // scalars, arrays and null are leaves
	}
	return nil
}

// assertJSONLeaf (TS assertJsonLeaf) fails closed on leaf values that are not
// JSON-representable — the same contract as canonical JSON state files.
func assertJSONLeaf(value any, side configSide, keyPath string) error {
	if !isJSONSafe(value) {
		return newConfigDiffInputError(side, "value at key \""+keyPath+"\" is not JSON-representable")
	}
	return nil
}

// isJSONSafe (TS isJsonSafe) reports whether a leaf value is
// JSON-representable (fail closed on Date/bigint/function/symbol/Map-like
// values, which the Go type system expresses as any value outside the JSON
// scalar / `[]any` / `map[string]any` shapes). Containers are checked
// recursively; every Go integer/float kind is one JS number, and json.Number
// (the shape a UseNumber decoder emits) is a number too.
func isJSONSafe(value any) bool {
	switch typed := value.(type) {
	case nil, string, bool, json.Number:
		return true
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	case []any:
		for _, item := range typed {
			if !isJSONSafe(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range typed {
			if !isJSONSafe(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

/* --------------------------------------------------------------- predicates */

// resolvedValueEquals (TS resolvedValueEquals) compares leaves through
// canonical JSON (arrays compare as whole blobs; missing ≡ null).
func resolvedValueEquals(a, b any) bool {
	left, ok := canonicalResolvedValue(a)
	if !ok {
		return false // unreachable: leaves are isJSONSafe-gated — fail closed
	}
	right, ok := canonicalResolvedValue(b)
	if !ok {
		return false
	}
	return left == right
}

// canonicalResolvedValue is canonicalJson(value) under JS number semantics:
// every Go integer/float kind is one JS number, and the non-finite floats
// serialize as null exactly like JSON.stringify. It reuses canonicalOrNull
// (semantics.go — owned there, reused never redefined); types canonical JSON
// cannot represent never reach it because flattenRoot gatekeeps leaves.
func canonicalResolvedValue(value any) (string, bool) {
	if number, ok := configNumber(value); ok {
		value = number
	}
	return canonicalOrNull(value)
}

// configNumber normalizes every Go numeric kind to the single JS number type
// (float64). This repo's parsers emit float64 and test literals land on int,
// so this only matters for exotic kinds.
func configNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

// keyIsSecret (TS keyIsSecret) reports whether ANY dot-path segment of the key
// matches the versioned secret pattern (case-insensitive substring).
func keyIsSecret(key string) bool {
	for _, segment := range strings.Split(key, ".") {
		if ConfigSecretKeyPattern.MatchString(segment) {
			return true
		}
	}
	return false
}

// isEnvRef (TS isEnvRef) reports whether a value is an env-reference string
// (`${VAR}` or `$VAR`, identifier-style).
func isEnvRef(value any) bool {
	text, ok := value.(string)
	return ok && ConfigEnvRefPattern.MatchString(text)
}

/* ---------------------------------------------------------- diff assembly */

// configKeyChangeFor (TS inline classification) maps side presence to the
// change class: no before = added, no after = removed, both = changed.
func configKeyChangeFor(hadBefore, hadAfter bool) ConfigKeyDiffChange {
	switch {
	case !hadBefore:
		return ConfigKeyAdded
	case !hadAfter:
		return ConfigKeyRemoved
	default:
		return ConfigKeyChanged
	}
}

// maskedConfigValue replaces secret values with the mask — the CHANGE is still
// reported, only the value is hidden (INV-8-adjacent).
func maskedConfigValue(value any, secretMasked bool) any {
	if secretMasked {
		return ConfigSecretMask
	}
	return value
}

/* ---------------------------------------------------------------- ordering */

// unionSortedKeys (TS `[...new Set([...beforeKeys, ...afterKeys])].sort()`)
// returns the union of both key sets ordered by UTF-16 code units.
func unionSortedKeys(beforeKeys, afterKeys ResolvedKeys) []string {
	seen := make(map[string]bool, len(beforeKeys)+len(afterKeys))
	keys := make([]string, 0, len(beforeKeys)+len(afterKeys))
	for _, source := range []ResolvedKeys{beforeKeys, afterKeys} {
		for key := range source {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sortConfigKeys(keys)
	return keys
}

// sortedConfigKeys returns one plain object's keys in UTF-16 code-unit order.
func sortedConfigKeys(record map[string]any) []string {
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	sortConfigKeys(keys)
	return keys
}

// sortConfigKeys orders keys by UTF-16 code units — the exact order of the TS
// default `Array#sort` (locale-independent code-unit comparison) the module
// contract requires.
//
// DEVIATION NOTE: Go's byte-wise string comparison (`sort.Strings`) agrees
// with code-unit order for every BMP-only key, ASCII keys included; it
// diverges only when a non-BMP key (UTF-16 surrogate pair, units D800–DFFF)
// sorts against a U+E000–U+FFFF key, which UTF-8 orders F0-prefixed (after
// EF-prefixed) instead. Comparing code units keeps the port exact for every
// key. Keys here come from JSON/YAML decoders, so invalid UTF-8 (decoded
// leniently to U+FFFD, one per bad byte) is a defensive case only.
func sortConfigKeys(keys []string) {
	units := make(map[string][]uint16, len(keys))
	for _, key := range keys {
		units[key] = utf16.Encode([]rune(key))
	}
	sort.Slice(keys, func(i, j int) bool {
		return utf16UnitsLess(units[keys[i]], units[keys[j]])
	})
}

// utf16UnitsLess is JS string `<` semantics: code-unit lexicographic, then
// length.
func utf16UnitsLess(a, b []uint16) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
