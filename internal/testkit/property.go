package testkit

// Ported from packages/core/src/testing/property.ts (P0-T17): the shared
// arbitraries and the standardized property runner that fed the Phase 1-5
// suites schema-shaped random inputs.
//
// The Go form of the fast-check harness:
//
//   - the PRNG is an explicit SEED (splitmix64) — the kit itself calls no
//     Date/random source, so a seed replays its whole case stream;
//   - generation is shrink-free by construction: every generator draws from
//     fixed, ASCII-only pools and structural combinators, so the first
//     violating case is already minimal enough to debug (the TS shrinker
//     existed to tame fast-check's much wider arbitraries);
//   - the run count is asserted: DefaultNumRuns (200) per property, exactly
//     like the TS fcProperty default;
//   - failures report a REPLAYABLE seed plus the failing case (the TS
//     fast-check convention), never just "property failed".
//
// Constraints honored by the arbitraries (unchanged from the TS kit):
//
//   - pathArb/docPathArb produce safe POSIX relative paths whose segments
//     match core's safe-charset rule (`[A-Za-z0-9._-]+`) so they are
//     encodable state keys and manifest-valid doc paths;
//   - manifestArb produces values that PASS the manifest schema (strict,
//     relative paths, DOC_MODE vocabulary only);
//   - floorArb produces OrgFloor values in core's supported floor-pattern
//     language (exact path, bare dir, trailing `dir/**`, bare `**`).

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/modes"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DefaultNumRuns is the standardized run count per property (TS
// DEFAULT_NUM_RUNS).
const DefaultNumRuns = 200

// DefaultPropertySeed is the fixed seed of the standardized runner: every
// unparameterized property replays identically across runs, machines and Go
// versions (the kit never derives a seed from a clock).
const DefaultPropertySeed int64 = 20260101

// SegmentPool is the fixed pool of path segments satisfying core's
// safe-charset rule everywhere (TS SEGMENT_POOL).
var SegmentPool = []string{
	"alpha", "beta", "gamma", "orders", "billing", "users",
	"v1", "docs", "src", "core.api", "x_9", "kebab-case",
}

// SourceGlobPool is the source-glob pool: patterns the core glob engine is
// expected to accept (TS SOURCE_GLOB_POOL).
var SourceGlobPool = []string{
	"src/**", "docs/**", "src/payments/**", "src/api/**",
	"packages/*/src/**", "README.md", "src",
}

// RunKeyRepoPool is the repo-identity pool of runKeyArb: repos with exactly
// one '/' whose fields are all filename-safe (TS the constantFrom list).
var RunKeyRepoPool = []string{"acme/payments", "acme/shop", "globex/core", "initech/docs"}

// ReviewerPool is the small ASCII pool floorArb draws optional
// requiredReviewers from (the TS fc.string() membership is opaque to every
// consumer; only presence/absence is observable).
var ReviewerPool = []string{"security-team", "compliance-team", "docs-owners", "@sec"}

/* ------------------------------------------------------------- the PRNG */

// SeededRNG is a deterministic splitmix64 PRNG: identical seeds produce
// identical streams on every machine, Go version and run, with no global
// state and no dependency on math/rand's (versioned) generators.
type SeededRNG struct {
	state uint64
}

// NewSeededRNG builds the stream for seed (a negative seed is wrapped, never
// rejected — seed identity is what matters, not its sign).
func NewSeededRNG(seed int64) *SeededRNG {
	return &SeededRNG{state: uint64(seed)}
}

// Uint64 is the next 64 random bits of the stream.
func (r *SeededRNG) Uint64() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Intn is a uniform draw from [0, n) (0 for a non-positive n — the generators
// never ask for one).
func (r *SeededRNG) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.Uint64() % uint64(n))
}

// Bool is one fair bit.
func (r *SeededRNG) Bool() bool { return r.Uint64()&1 == 1 }

// Pick returns one element of values (the zero value for an empty pool).
func Pick[T any](rng *SeededRNG, values []T) T {
	var zero T
	if len(values) == 0 {
		return zero
	}
	return values[rng.Intn(len(values))]
}

/* --------------------------------------------------------- the runner */

// PropertyOptions is the standardized property configuration (TS
// FcPropertyOptions, minus the vitest timeout the Go subtest model does not
// need).
type PropertyOptions struct {
	// NumRuns is the number of generated cases (default DefaultNumRuns).
	NumRuns int
	// Seed is the PRNG seed (default DefaultPropertySeed).
	Seed int64
}

// DefaultPropertyOptions is the standardized configuration: 200 runs on the
// fixed seed.
func DefaultPropertyOptions() PropertyOptions {
	return PropertyOptions{NumRuns: DefaultNumRuns, Seed: DefaultPropertySeed}
}

// withDefaults fills a non-positive run count with the standardized value. An
// explicit seed — 0 included — is honored as-is: seed identity is what makes a
// property replayable, so it is never normalized away.
func (o PropertyOptions) withDefaults() PropertyOptions {
	if o.NumRuns <= 0 {
		o.NumRuns = DefaultNumRuns
	}
	return o
}

// PropertyViolation is the deterministic failure record of one property run:
// the seed and run index replay it, the case is the generated input, and Err
// is the predicate's own error.
type PropertyViolation struct {
	Name    string
	Seed    int64
	Run     int
	NumRuns int
	Case    any
	Err     error
}

// Error renders the replayable failure message (TS fast-check's seed
// reporting convention).
func (v *PropertyViolation) Error() string {
	return fmt.Sprintf("property %q violated on run %d/%d (seed %d — replay with PropertyOptions{Seed: %d}): %v\ncase: %s",
		v.Name, v.Run, v.NumRuns, v.Seed, v.Seed, v.Err, RenderCase(v.Case))
}

// RenderCase renders a generated case deterministically (canonical JSON; a
// non-JSON case degrades to its Go formatting rather than failing the report).
func RenderCase(value any) string {
	rendered, err := canonicalize(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return rendered
}

// Generator draws one property case from the seeded stream — the Go form of
// a fast-check arbitrary (total, deterministic, shrink-free).
type Generator[T any] func(rng *SeededRNG) T

// CheckProperty runs gen through predicate options.NumRuns times from the
// seeded stream and returns the first violation as a *PropertyViolation
// (nil when the property holds). It never touches *testing.T: the subtest
// wrapper (RunProperty) is the only test-facing surface.
func CheckProperty[T any](name string, gen Generator[T], predicate func(value T) error, options PropertyOptions) error {
	resolved := options.withDefaults()
	rng := NewSeededRNG(resolved.Seed)
	for run := 1; run <= resolved.NumRuns; run++ {
		value := gen(rng)
		if err := predicate(value); err != nil {
			return &PropertyViolation{
				Name:    name,
				Seed:    resolved.Seed,
				Run:     run,
				NumRuns: resolved.NumRuns,
				Case:    value,
				Err:     err,
			}
		}
	}
	return nil
}

// RunProperty registers the property as a named subtest running
// DefaultNumRuns cases (200) on DefaultPropertySeed — the Go twin of the TS
// fcProperty wrapper over fc.assert(fc.asyncProperty(...)). Options override
// the run count and the seed.
func RunProperty[T any](t *testing.T, name string, gen Generator[T], predicate func(value T) error, options ...PropertyOptions) {
	t.Helper()
	resolved := DefaultPropertyOptions()
	if len(options) > 0 {
		resolved = options[0]
	}
	t.Run(name, func(t *testing.T) {
		t.Helper()
		if err := CheckProperty(name, gen, predicate, resolved); err != nil {
			t.Fatalf("%v", err)
		}
	})
}

// PropertyCases draws exactly options.NumRuns cases from one seeded stream —
// the deterministic replay surface behind the determinism/repeatability
// assertions (same options → the same slice, every time).
func PropertyCases[T any](gen Generator[T], options PropertyOptions) []T {
	resolved := options.withDefaults()
	rng := NewSeededRNG(resolved.Seed)
	cases := make([]T, 0, resolved.NumRuns)
	for run := 0; run < resolved.NumRuns; run++ {
		cases = append(cases, gen(rng))
	}
	return cases
}

/* -------------------------------------------------------- the arbitraries */

// SegmentArb is one safe path segment (TS segmentArb).
func SegmentArb(rng *SeededRNG) string { return Pick(rng, SegmentPool) }

// PathArb is a safe POSIX relative path: 1-4 segments, each
// `[A-Za-z0-9._-]+` (TS pathArb).
func PathArb(rng *SeededRNG) string {
	count := 1 + rng.Intn(4) // 1..4 segments
	segments := make([]string, 0, count)
	for i := 0; i < count; i++ {
		segments = append(segments, SegmentArb(rng))
	}
	return strings.Join(segments, "/")
}

// DocPathArb is a document path: a safe relative path, always `.md`
// (documents are markdown) (TS docPathArb).
func DocPathArb(rng *SeededRNG) string {
	path := PathArb(rng)
	if strings.HasSuffix(path, ".md") {
		return path
	}
	return path + ".md"
}

// DocModeArb is one DOC_MODE value (TS docModeArb).
func DocModeArb(rng *SeededRNG) schemas.DocMode { return Pick(rng, schemas.DocModeValues) }

// TriggerArb is one universal-taxonomy trigger (TS triggerArb).
func TriggerArb(rng *SeededRNG) schemas.UniversalTrigger {
	return Pick(rng, schemas.UniversalTriggerValues)
}

// LearningModeArb is one learning-mode value (off/propose/canary/auto —
// TS learningModeArb).
func LearningModeArb(rng *SeededRNG) schemas.LearningMode {
	return Pick(rng, schemas.LearningModeValues)
}

// SHAArb is a lowercase hex string of 7-40 characters — the abbreviated-sha
// shape both git identities and state paths accept (TS shaArb).
func SHAArb(rng *SeededRNG) string {
	const hexDigits = "0123456789abcdef"
	length := 7 + rng.Intn(34) // 7..40 characters
	var sb strings.Builder
	for i := 0; i < length; i++ {
		sb.WriteByte(hexDigits[rng.Intn(len(hexDigits))])
	}
	return sb.String()
}

// floorDirPrefix is the shared "2-3 segments, last dropped" bare-directory
// form both the bare and trailing-`/**` pattern arms draw from (TS the two
// array(segmentArb, {minLength:2, maxLength:3}).map(slices) combinators).
func floorDirPrefix(rng *SeededRNG) string {
	count := 2 + rng.Intn(2) // 2..3 segments
	segments := make([]string, 0, count)
	for i := 0; i < count; i++ {
		segments = append(segments, SegmentArb(rng))
	}
	return strings.Join(segments[:len(segments)-1], "/")
}

// FloorPatternArb is one floor pattern in core's supported forms: exact
// document path, bare directory (≥1 segment), trailing `dir/**`, or the bare
// `**` that matches everything (TS floorPatternArb).
func FloorPatternArb(rng *SeededRNG) string {
	switch rng.Intn(4) {
	case 0:
		return PathArb(rng)
	case 1:
		return floorDirPrefix(rng)
	case 2:
		return floorDirPrefix(rng) + "/**"
	default:
		return "**"
	}
}

// FloorArb is one OrgFloor with a supported pattern form and a valid payload
// for its kind (TS floorArb).
func FloorArb(rng *SeededRNG) modes.OrgFloor {
	pattern := FloorPatternArb(rng)
	if rng.Bool() {
		return modes.OrgFloor{
			Kind:    schemas.FloorKindModeCeiling,
			Pattern: pattern,
			MaxMode: DocModeArb(rng),
		}
	}
	floor := modes.OrgFloor{
		Kind:    schemas.FloorKindReportOnlyUntilReviewed,
		Pattern: pattern,
	}
	if rng.Bool() {
		reviewers := make([]string, 0, 3)
		count := 1 + rng.Intn(3)
		for i := 0; i < count; i++ {
			reviewers = append(reviewers, Pick(rng, ReviewerPool))
		}
		floor.RequiredReviewers = reviewers
	}
	return floor
}

// sourcesArb is 1-3 source entries from the glob pool (TS sourcesArb).
func sourcesArb(rng *SeededRNG) []string {
	count := 1 + rng.Intn(3)
	sources := make([]string, 0, count)
	for i := 0; i < count; i++ {
		sources = append(sources, Pick(rng, SourceGlobPool))
	}
	return sources
}

// triggersArb is 1-3 universal triggers (TS fc.array(triggerArb, {minLength:
// 1, maxLength: 3})).
func triggersArb(rng *SeededRNG) []string {
	count := 1 + rng.Intn(3)
	triggers := make([]string, 0, count)
	for i := 0; i < count; i++ {
		triggers = append(triggers, string(TriggerArb(rng)))
	}
	return triggers
}

// ignoreArb is 1-2 ignored triggers (TS fc.array(triggerArb, {minLength: 1,
// maxLength: 2})).
func ignoreArb(rng *SeededRNG) []string {
	count := 1 + rng.Intn(2)
	ignored := make([]string, 0, count)
	for i := 0; i < count; i++ {
		ignored = append(ignored, string(TriggerArb(rng)))
	}
	return ignored
}

// entryArb is one schema-valid manifest entry with the optional mode either
// present or absent (TS entryArb, where `fc.option(..., {nil: undefined})`
// made the undefined case skip the key).
func entryArb(rng *SeededRNG) schemas.ManifestEntry {
	entry := schemas.ManifestEntry{
		Path:     DocPathArb(rng),
		Sources:  sourcesArb(rng),
		Triggers: triggersArb(rng),
		Ignore:   ignoreArb(rng),
	}
	if rng.Bool() {
		mode := DocModeArb(rng)
		entry.Mode = &mode
	}
	return entry
}

// suffixDocPath inserts `-<index>` before the trailing `.md`, making the doc
// path unique while keeping it safe (TS
// `path.replace(/(\.md)$/, `-${index}$1`)`).
func suffixDocPath(path string, index int) string {
	suffix := "-" + strconv.Itoa(index)
	if strings.HasSuffix(path, ".md") {
		return path[:len(path)-len(".md")] + suffix + ".md"
	}
	return path + suffix
}

// ManifestArb is a schema-valid manifest: strictObject shape, unique
// document paths (index-suffixed), version 1 (TS manifestArb).
func ManifestArb(rng *SeededRNG) schemas.Manifest {
	count := 1 + rng.Intn(5) // 1..5 documents
	documents := make([]schemas.ManifestEntry, 0, count)
	for index := 0; index < count; index++ {
		entry := entryArb(rng)
		entry.Path = suffixDocPath(entry.Path, index)
		documents = append(documents, entry)
	}
	return schemas.Manifest{Version: 1, Documents: documents}
}

// ModeMixedManifestArb is a MODE-MIXED manifest (P12-T17): 1-3 entries, each
// carrying an EXPLICIT mode drawn from the full ARCH §11.2 vocabulary, with
// distinct still-safe doc paths — so write-path invariants exercise both the
// ship and the INV-4 refusal branches (TS modeMixedManifestArb).
func ModeMixedManifestArb(rng *SeededRNG) schemas.Manifest {
	count := 1 + rng.Intn(3) // 1..3 documents
	documents := make([]schemas.ManifestEntry, 0, count)
	for index := 0; index < count; index++ {
		mode := DocModeArb(rng)
		documents = append(documents, schemas.ManifestEntry{
			Path:     suffixDocPath(DocPathArb(rng), index),
			Mode:     &mode,
			Sources:  sourcesArb(rng),
			Triggers: triggersArb(rng),
		})
	}
	return schemas.Manifest{Version: 1, Documents: documents}
}

// RunKeyArb is a run-record idempotency key whose fields are all
// filename-safe (TS runKeyArb).
func RunKeyArb(rng *SeededRNG) schemas.RunKey {
	return schemas.RunKey{
		Repo:            Pick(rng, RunKeyRepoPool),
		BaseSHA:         SHAArb(rng),
		HeadSHA:         SHAArb(rng),
		ManifestVersion: 1 + rng.Intn(50), // 1..50
		PolicyVersion:   1 + rng.Intn(50), // 1..50
	}
}
