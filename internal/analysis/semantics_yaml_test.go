package analysis_test

// Ported from the YAML half of packages/core/test/analysis-semantics.test.ts
// (P1-T05; OD-R06 shallow scope; D14 declared gaps), extended to the scalar
// resolution rules the Go port documents in semantics.go: the eemeli `yaml`
// core-schema (YAML 1.2) resolution applied to the source text for
// plain/quoted/block scalars, explicit core tags (match → resolved, mismatch →
// raw string), YAML-1.1-only forms the core schema leaves as strings
// (underscores, sexagesimals), non-core tags (Date/Buffer) as declared gaps,
// JS String(...) key stringification, merge keys as ordinary strings, and the
// anchor rules (a node reachable twice — cycle OR shared reference — is a
// declared gap).
//
// Every case is asserted through the PUBLIC ConfigDiffFor surface, so the
// expectations are the portable contract: exact notes for changes, the fixed
// gap note for declared gaps, and config_diff false for unchanged.

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/analysis"
)

// yamlGapNote is the fixed unparseable/unsupported note (D14).
const yamlGapNote = "unparseable — semantic config diff unavailable"

// analysisConfigDiffYAML runs the public config-diff surface over a YAML path.
func analysisConfigDiffYAML(oldText, newText string) *analysis.ConfigDiffResult {
	return analysis.ConfigDiffFor("conf.yaml", oldText, newText)
}

// requireYAMLChanged asserts one changed YAML config diff with the exact note.
func requireYAMLChanged(t *testing.T, oldText, newText, note string) {
	t.Helper()
	result := analysisConfigDiffYAML(oldText, newText)
	requireChanged(t, result, note)
}

// requireYAMLUnchanged asserts an unchanged YAML config diff.
func requireYAMLUnchanged(t *testing.T, oldText, newText string) {
	t.Helper()
	requireUnchanged(t, analysisConfigDiffYAML(oldText, newText))
}

// requireYAMLGap asserts the declared-gap verdict for either side.
func requireYAMLGap(t *testing.T, oldText, newText string) {
	t.Helper()
	requireGap(t, analysisConfigDiffYAML(oldText, newText), yamlGapNote)
}

func TestConfigDiffFor_YAMLScalarResolution_P1T05(t *testing.T) {
	t.Run("typed scalars resolve through the YAML 1.2 core schema", func(t *testing.T) {
		requireYAMLChanged(t,
			"i: 1\nb: true\nn: null\nf: 1.5\ns: text\n",
			"i: 2\nb: false\nn: 5\nf: 2.5\ns: other\n",
			`config keys changed (5): b: true → false; f: 1.5 → 2.5; i: 1 → 2; n: null → 5; s: "text" → "other"`)
	})

	t.Run("YAML 1.2 core schema does not coerce yes/no/on/off to booleans", func(t *testing.T) {
		requireYAMLChanged(t, "flag: yes\n", "flag: no\n",
			`config keys changed (1): flag: "yes" → "no"`)
	})

	t.Run("quoted scalars are strings even when they look numeric", func(t *testing.T) {
		requireYAMLChanged(t, "n: \"123\"\n", "n: \"124\"\n",
			`config keys changed (1): n: "123" → "124"`)
	})

	t.Run("block scalars (literal and folded) are strings", func(t *testing.T) {
		requireYAMLChanged(t, "text: |\n  line1\n  line2\n", "text: |\n  line1\n  line3\n",
			`config keys changed (1): text: "line1\nline2\n" → "line1\nline3\n"`)
	})

	t.Run("radix integers resolve to numbers", func(t *testing.T) {
		requireYAMLChanged(t, "hex: 0x1F\noct: 0o17\nneg: -0x10\n", "hex: 0x20\noct: 0o20\nneg: -0x11\n",
			`config keys changed (3): hex: 31 → 32; neg: "-0x10" → "-0x11"; oct: 15 → 16`)
	})

	t.Run("integers beyond int64 saturate to float64 (JS parseInt semantics)", func(t *testing.T) {
		// 99999999999999999999 parses as the float64 1e20 (precision loss),
		// which renders the way JS String() does.
		requireYAMLChanged(t, "big: 99999999999999999999\n", "big: 1\n",
			`config keys changed (1): big: 100000000000000000000 → 1`)
	})

	t.Run("integers beyond float64 range stay the parser's string (conservative)", func(t *testing.T) {
		// The Go parser tags a beyond-float64-range integer plain scalar as
		// !!str, and the port keeps that string (eemeli's core schema would
		// resolve it to a float — Infinity as a JS number — which canonical
		// JSON cannot represent; the string keeps the evidence instead).
		requireYAMLChanged(t,
			"huge: "+strings.Repeat("1", 400)+"\n",
			"huge: 1\n",
			`config keys changed (1): huge: "`+strings.Repeat("1", 400)+`" → 1`)
	})

	t.Run("non-finite scalars render Infinity/-Infinity/NaN like JS String()", func(t *testing.T) {
		requireYAMLChanged(t, "f: .inf\ng: -.inf\nh: .nan\n", "f: 1.5\ng: 2.5\nh: 3.5\n",
			`config keys changed (3): f: Infinity → 1.5; g: -Infinity → 2.5; h: NaN → 3.5`)
	})

	t.Run("YAML 1.1 underscores stay strings in the core schema", func(t *testing.T) {
		requireYAMLChanged(t, "u: 1_000\n", "u: 2_000\n",
			`config keys changed (1): u: "1_000" → "2_000"`)
	})

	t.Run("comments are ignored by the resolver", func(t *testing.T) {
		requireYAMLChanged(t, "a: 1 # comment\n", "a: 2 # another\n",
			`config keys changed (1): a: 1 → 2`)
	})
}

func TestConfigDiffFor_YAMLExplicitTags_P1T05(t *testing.T) {
	t.Run("core tags resolve when the text satisfies the tag test", func(t *testing.T) {
		// The eemeli core-schema float test requires a decimal point: "7.0"
		// resolves as a float (rendering "7" — JS String() drops the point),
		// while a bare "7" would fail the test and stay a string.
		requireYAMLChanged(t,
			"s: !!str 7\ni: !!int 7\nb: !!bool true\nn: !!null ~\nf: !!float 7.0\n",
			"s: !!str 8\ni: !!int 8\nb: !!bool false\nn: !!null 9\nf: !!float 8.0\n",
			`config keys changed (5): b: true → false; f: 7 → 8; i: 7 → 8; n: null → "9"; s: "7" → "8"`)
	})

	t.Run("core tags keep the raw string when the text fails the tag test", func(t *testing.T) {
		requireYAMLChanged(t,
			"s: !!str 123\nu: !!int xyz\nb: !!bool maybe\nl: !!null notnull\nf: !!float abc\n",
			"s: !!str 124\nu: !!int zzz\nb: !!bool perhaps\nl: !!null stillnot\nf: !!float def\n",
			`config keys changed (5): b: "maybe" → "perhaps"; f: "abc" → "def"; l: "notnull" → "stillnot"; s: "123" → "124"; u: "xyz" → "zzz"`)
	})

	t.Run("an untagged date stays the string; an explicit !!timestamp is a declared gap", func(t *testing.T) {
		// Untagged: the Go parser auto-detects YAML-1.1 timestamps, but
		// eemeli's core schema does not — the text stays a string.
		requireYAMLChanged(t, "d: 2020-01-02\n", "d: 2020-01-03\n",
			`config keys changed (1): d: "2020-01-02" → "2020-01-03"`)
		// Explicit: eemeli resolves !!timestamp to a Date, which canonical
		// JSON cannot serialize — fail closed (D14).
		requireYAMLGap(t, "d: 2020-01-02\n", "d: !!timestamp 2020-01-03\n")
	})

	t.Run("an explicit !!binary is a declared gap (Buffer is not JSON)", func(t *testing.T) {
		requireYAMLGap(t, "b: !!binary aGVsbG8=\n", "b: !!binary aGVsbG8x\n")
	})

	t.Run("a non-core custom tag is a declared gap", func(t *testing.T) {
		requireYAMLGap(t, "x: !custom value\n", "x: !custom other\n")
	})
}

func TestConfigDiffFor_YAMLKeys_P1T05(t *testing.T) {
	t.Run("the merge key << is an ordinary string key (core schema merge disabled)", func(t *testing.T) {
		requireYAMLChanged(t, "<<: 1\n", "<<: 2\n",
			`config keys changed (1): <<: 1 → 2`)
	})

	t.Run("non-string keys stringify like JS String()", func(t *testing.T) {
		requireYAMLChanged(t, "1: a\ntrue: b\n~: c\n", "1: x\ntrue: b\n~: c\n",
			`config keys changed (1): 1: "a" → "x"`)
	})

	t.Run("distinct key identities sharing a stringified key keep the last value", func(t *testing.T) {
		// A number 1 and a string "1" are distinct identities, but both
		// stringify to "1" in the plain object the TS side builds — one entry,
		// last one winning.
		requireYAMLChanged(t, "1: a\n\"1\": b\n", "1: a\n\"1\": c\n",
			`config keys changed (1): 1: "b" → "c"`)
	})

	t.Run("a null value and an absent key are the same (TS a ?? null)", func(t *testing.T) {
		requireYAMLUnchanged(t, "a: 1\nb: null\n", "a: 1\n")
	})

	t.Run("a collection key is a declared gap", func(t *testing.T) {
		requireYAMLGap(t, "? [a, b]\n: v1\n", "? [a, b]\n: v2\n")
	})
}

func TestConfigDiffFor_YAMLAnchorsAndDocuments_P1T05(t *testing.T) {
	t.Run("a shared anchor reference is a declared gap", func(t *testing.T) {
		// &c is reachable twice (as common's value and through the alias) —
		// the TS isJsonSafe predicate rejects any node reachable twice.
		requireYAMLGap(t,
			"common: &c\n  timeout: 30\nservice:\n  <<: *c\n",
			"common: &c\n  timeout: 31\nservice:\n  <<: *c\n")
	})

	t.Run("an anchor cycle is a declared gap", func(t *testing.T) {
		requireYAMLGap(t, "a: &x\n  b: *x\n", "a: &x\n  b: *y\n")
	})

	t.Run("multiple documents are a declared gap", func(t *testing.T) {
		requireYAMLGap(t, "a: 1\n---\na: 2\n", "a: 1\n")
	})

	t.Run("empty and comment-only documents are declared gaps", func(t *testing.T) {
		requireYAMLGap(t, "", "a: 1\n")
		requireYAMLGap(t, "# just a comment\n", "a: 1\n")
	})

	t.Run("a non-mapping root is a declared gap", func(t *testing.T) {
		requireYAMLGap(t, "- a\n- b\n", "- a\n- c\n")
		requireYAMLGap(t, "scalar\n", "scalar2\n")
	})
}

// TestConfigDiffFor_YAMLShallowScopeStable pins the OD-R06 shallow scope once
// more through the parser: deeper objects stay leaf blobs, and arrays are
// whole-value leaves with the same canonical-JSON rendering.
func TestConfigDiffFor_YAMLShallowScopeStable(t *testing.T) {
	requireYAMLChanged(t,
		"service:\n  db:\n    timeout: 30\n  tags:\n    - a\n    - b\n",
		"service:\n  db:\n    timeout: 31\n  tags:\n    - a\n    - c\n",
		`config keys changed (2): service.db: {"timeout":30} → {"timeout":31}; service.tags: ["a","b"] → ["a","c"]`)
}
