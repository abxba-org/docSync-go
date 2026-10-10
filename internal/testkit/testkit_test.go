package testkit_test

// Ported from packages/core/test/golden.test.ts, testing-kit.property.test.ts,
// fakes.test.ts and temp-repo.test.ts (P0-T17; the temp-repo tests adapt the
// TS node-fs fixtures to the Go caller-owned-root form).
//
// Package testkit_test (external): the kit is exercised exactly the way its
// consumers use it, so only the exported surface is tested.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/boundaries"
	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/modes"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/statemachine"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

// Compile-time structural parity: the REAL store satisfies the fake's surface
// (the TS `const realAsSurface: StateStoreSurface = new StateStore(...)`
// assignability check).
var (
	_ testkit.StateStoreSurface = (*state.StateStore)(nil)
	_ testkit.GitPlatform       = (*testkit.GitPlatformFake)(nil)
)

/* ============================== shared helpers ============================= */

// conclusionPointer takes the address of a conclusion constant.
func conclusionPointer(conclusion testkit.CheckRunConclusion) *testkit.CheckRunConclusion {
	return &conclusion
}

// decodeForCompare normalizes a Go value into the generic JSON form the
// golden readers return (json.Number for numbers).
func decodeForCompare(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out any
	require.NoError(t, decoder.Decode(&out))
	return out
}

// plantRaw overwrites a state file's raw bytes through the fake's test-only
// hook, conditioning the CAS on the bytes currently stored (the hook refuses
// an unconditional overwrite of an existing file, exactly like the real one).
func plantRaw(t *testing.T, fake *testkit.InMemoryStateStore, relPath, content string) string {
	t.Helper()
	var expected *string
	if current, ok := fake.Files()[relPath]; ok {
		hash := docutil.ContentHash(current)
		expected = &hash
	}
	write, err := fake.WriteRawForTest(relPath, content, expected)
	require.NoError(t, err)
	require.True(t, write.OK, "the raw plant must land")
	return content
}

// requireGit skips the caller-owned-repo tests when no git binary is present.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary unavailable: caller-owned repo fixtures are skipped")
	}
}

// stringOf dereferences a pointer for assertions (the CAS conflict pair).
func stringOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// gitBlobSHA is the independent (crypto-level) blob identity the fixture's
// concepts must carry: sha1("blob <len>\0" + content).
func gitBlobSHA(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return fmt.Sprintf("%x", sum)
}

/* ================================ golden tests ============================= */

// goldenValue mirrors the TS GOLDEN_VALUE: an object containing float, array
// and nested-object members, so key order and ordering semantics both apply.
var goldenValue = map[string]any{
	"agent_id":   "agent_123",
	"status":     "complete",
	"confidence": 0.94,
	"items": []any{
		map[string]any{"id": "1", "name": "alpha"},
		map[string]any{"id": "2", "name": "beta"},
	},
	"meta": map[string]any{"correlation_id": "run_001", "docs": "1"},
}

const goldenCaseName = "docsync-golden-case"

// TestGoldenLifecycle ports the TS golden lifecycle: without update mode a
// missing golden throws (returns the typed AssertionError twin), update mode
// materializes it, and repeated runs pass.
func TestGoldenLifecycle(t *testing.T) {
	root := t.TempDir()

	exists, err := testkit.GoldenExists(root, goldenCaseName)
	require.NoError(t, err)
	assert.False(t, exists)

	err = testkit.ExpectGolden(goldenValue, testkit.GoldenOptions{Dir: root, Name: goldenCaseName})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "golden file missing")
	assert.Contains(t, err.Error(), testkit.UpdateGoldensEnv)

	require.NoError(t, testkit.ExpectGolden(goldenValue,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName, Update: true}))

	exists, err = testkit.GoldenExists(root, goldenCaseName)
	require.NoError(t, err)
	assert.True(t, exists)

	// Re-running passes (idempotent).
	require.NoError(t, testkit.ExpectGolden(goldenValue,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName}))

	read, err := testkit.ReadGolden(root, goldenCaseName)
	require.NoError(t, err)
	require.True(t, read.Exists)
	assert.Equal(t, decodeForCompare(t, goldenValue), read.Parsed)
	// POSIX-friendly: exactly one trailing newline.
	assert.True(t, strings.HasSuffix(read.Raw, "\n"))
	assert.False(t, strings.HasSuffix(read.Raw, "\n\n"))

	missing, err := testkit.ReadGolden(root, "never-written")
	require.NoError(t, err)
	assert.False(t, missing.Exists)
	assert.Nil(t, missing.Parsed)
}

// TestGoldenUpdateViaEnvironmentVariable ports the TS
// "update mode via env var" case: DOCSYNC_UPDATE_GOLDENS=1 alone both creates a
// missing golden AND refreshes a stale one.
func TestGoldenUpdateViaEnvironmentVariable(t *testing.T) {
	root := t.TempDir()
	t.Setenv(testkit.UpdateGoldensEnv, "1")

	require.NoError(t, testkit.ExpectGolden(goldenValue,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName}))

	changed := map[string]any{
		"agent_id":   "agent_456",
		"status":     "complete",
		"confidence": 0.5,
		"items":      []any{map[string]any{"id": "1", "name": "alpha"}},
		"meta":       map[string]any{"correlation_id": "run_002"},
	}
	require.NoError(t, testkit.ExpectGolden(changed,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName}))

	read, err := testkit.ReadGolden(root, goldenCaseName)
	require.NoError(t, err)
	assert.Equal(t, "0.5", read.Parsed.(map[string]any)["confidence"].(json.Number).String())
	assert.Equal(t, "agent_456", read.Parsed.(map[string]any)["agent_id"])
}

// TestGoldenMismatchReportsDeterministicDiff ports the TS
// "deterministic mismatch diff" case: the message is stable, JSON-path
// addressed, and cut at MaxDiffPaths with the update-mode hint.
func TestGoldenMismatchReportsDeterministicDiff(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.ExpectGolden(goldenValue,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName, Update: true}))

	actual := map[string]any{
		"agent_id":   "agent_123",
		"status":     "complete",
		"confidence": 0.72,
		"items": []any{
			map[string]any{"id": "other", "name": "alpha"},
			map[string]any{"id": "2", "name": "beta"},
		},
		"extra": "unexpected",
		"meta":  map[string]any{"correlation_id": "run_001", "docs": "2"},
	}

	var first, second *testkit.GoldenAssertionError
	err := testkit.ExpectGolden(actual, testkit.GoldenOptions{Dir: root, Name: goldenCaseName})
	require.ErrorAs(t, err, &first)
	err = testkit.ExpectGolden(actual, testkit.GoldenOptions{Dir: root, Name: goldenCaseName})
	require.ErrorAs(t, err, &second)

	assert.Equal(t, first.Message, second.Message)
	assert.Contains(t, first.Message, "$.confidence: expected 0.94, actual 0.72")
	assert.Contains(t, first.Message, "$.items[0].id: expected \"1\", actual \"other\"")
	assert.Contains(t, first.Message, "$.extra: expected absent, actual \"unexpected\"")
	assert.Contains(t, first.Message, testkit.UpdateGoldensEnv+"=1")
	assert.Equal(t, "0.72", first.Actual.(map[string]any)["confidence"].(json.Number).String())
	assert.Equal(t, "1", first.Expected.(map[string]any)["items"].([]any)[0].(map[string]any)["id"])
}

// TestGoldenWebhookHookMismatchesWhenExactlyOneChanged ports the TS
// "mismatches when exactly one changed" case (surface minimality: one changed
// scalar is one diff line, not a whole-document rewrite).
func TestGoldenMismatchesWhenExactlyOneChanged(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.ExpectGolden(goldenValue,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName, Update: true}))

	oneChanged := map[string]any{
		"agent_id":   "agent_999", // ← the only drift
		"status":     "complete",
		"confidence": 0.94,
		"items": []any{
			map[string]any{"id": "1", "name": "alpha"},
			map[string]any{"id": "2", "name": "beta"},
		},
		"meta": map[string]any{"correlation_id": "run_001", "docs": "1"},
	}
	var assertion *testkit.GoldenAssertionError
	err := testkit.ExpectGolden(oneChanged, testkit.GoldenOptions{Dir: root, Name: goldenCaseName})
	require.ErrorAs(t, err, &assertion)
	assert.Contains(t, assertion.Message, "$.agent_id")
	assert.NotContains(t, assertion.Message, "$.confidence")
	assert.NotContains(t, assertion.Message, "$.status")
}

// TestGoldenKeyOrderDoesNotMatter ports the TS "logical equality across key
// order" property: the SAME value in a different insertion order passes,
// because canonical JSON sorts object keys recursively.
func TestGoldenKeyOrderDoesNotMatter(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.ExpectGolden(goldenValue,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName, Update: true}))

	reordered := map[string]any{
		"meta":       map[string]any{"docs": "1", "correlation_id": "run_001"},
		"items":      []any{map[string]any{"name": "alpha", "id": "1"}, map[string]any{"id": "2", "name": "beta"}},
		"confidence": 0.94,
		"status":     "complete",
		"agent_id":   "agent_123",
	}
	require.NoError(t, testkit.ExpectGolden(reordered,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName}))
}

// TestGoldenFilesAreByteStable pins the core promise: once written, the golden
// bytes never move — not across repeat runs, and not across logically equal
// values in a different key order (so `git diff` stays empty).
func TestGoldenFilesAreByteStable(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.ExpectGolden(goldenValue,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName, Update: true}))

	read, err := testkit.ReadGolden(root, goldenCaseName)
	require.NoError(t, err)
	first := read.Raw

	for i := 0; i < 3; i++ {
		require.NoError(t, testkit.ExpectGolden(goldenValue,
			testkit.GoldenOptions{Dir: root, Name: goldenCaseName}))
		again, err := testkit.ReadGolden(root, goldenCaseName)
		require.NoError(t, err)
		assert.Equal(t, first, again.Raw, "golden bytes drifted on repeat run %d", i)
	}

	reordered := map[string]any{
		"confidence": 0.94,
		"agent_id":   "agent_123",
		"status":     "complete",
		"meta":       map[string]any{"docs": "1", "correlation_id": "run_001"},
		"items":      []any{map[string]any{"id": "1", "name": "alpha"}, map[string]any{"id": "2", "name": "beta"}},
	}
	require.NoError(t, testkit.ExpectGolden(reordered,
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName}))
	after, err := testkit.ReadGolden(root, goldenCaseName)
	require.NoError(t, err)
	assert.Equal(t, first, after.Raw, "key order changed the committed bytes")
}

// TestGoldenPathRejectsUnsafeNames ports the TS filename-safety rule: a name
// is ONE path segment with a restricted charset, so `a/b` and traversal
// patterns are refused before any fs access.
func TestGoldenPathRejectsUnsafeNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"with space", "bad/name", "../escape", "a\\b", "", "$(rm)", ":x"} {
		_, err := testkit.GoldenPathFor(root, name)
		require.Error(t, err, "expected %q to be refused", name)
	}
	file, err := testkit.GoldenPathFor(root, "docsync-golden.01")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "docsync-golden.01"+testkit.GoldenSuffix), file)
}

// TestExpectGoldenRefusesNonJSONValues pins the fail-closed boundary: a value
// that cannot round-trip through JSON is refused BEFORE any file is written.
func TestExpectGoldenRefusesNonJSONValues(t *testing.T) {
	root := t.TempDir()
	err := testkit.ExpectGolden(map[string]any{"blocked": make(chan int)},
		testkit.GoldenOptions{Dir: root, Name: goldenCaseName, Update: true})
	require.Error(t, err)

	exists, err := testkit.GoldenExists(root, goldenCaseName)
	require.NoError(t, err)
	assert.False(t, exists, "nothing may be written before the value is refused")
}

// TestGoldenCorruptFileFailsClosed ports the TS fail-closed read: invalid JSON
// in a golden file is an error, never a silently empty golden.
func TestGoldenCorruptFileFailsClosed(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, goldenCaseName+testkit.GoldenSuffix)
	require.NoError(t, os.WriteFile(file, []byte("{not valid json"), 0o644))

	err := testkit.ExpectGolden(goldenValue, testkit.GoldenOptions{Dir: root, Name: goldenCaseName})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")

	_, err = testkit.ReadGolden(root, goldenCaseName)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

/* ============================== property tests ============================= */

// TestPathArbGeneratesSafeRelativePosixPaths ports the pathArb property: safe
// POSIX relative paths only, every segment within the core charset.
func TestPathArbGeneratesSafeRelativePosixPaths(t *testing.T) {
	safeSegment := regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	rng := testkit.NewSeededRNG(testkit.DefaultPropertySeed)
	for i := 0; i < testkit.DefaultNumRuns; i++ {
		path := testkit.PathArb(rng)
		require.NotEmpty(t, path)
		assert.False(t, strings.HasPrefix(path, "/"), "must be relative: %q", path)
		assert.NotContains(t, path, "\\", "must be POSIX: %q", path)
		assert.NotContains(t, path, "..", "no traversal: %q", path)
		for _, segment := range strings.Split(path, "/") {
			assert.Regexp(t, safeSegment, segment, "unsafe segment in %q", path)
		}
	}
}

// TestDocPathArbRoundTripsThroughStateEncoding ports the docPathArb property:
// every generated doc path is `.md`, manifest-valid, and state-encodable.
func TestDocPathArbRoundTripsThroughStateEncoding(t *testing.T) {
	rng := testkit.NewSeededRNG(testkit.DefaultPropertySeed)
	for i := 0; i < testkit.DefaultNumRuns; i++ {
		docPath := testkit.DocPathArb(rng)
		assert.True(t, strings.HasSuffix(docPath, ".md"), "docs are markdown: %q", docPath)
		require.True(t, schemas.IsValidManifestPath(docPath), "manifest-valid doc path: %q", docPath)

		encoded, err := state.EncodeStateDocPath(docPath)
		require.NoError(t, err)
		decoded, err := state.DecodeStateDocPath(encoded)
		require.NoError(t, err)
		assert.Equal(t, docPath, decoded)
	}
}

// TestManifestArbGeneratesValidManifests ports the manifestArb property: every
// generated manifest parses strictly, carries unique relative paths, and
// stays inside the DOC_MODE vocabulary.
func TestManifestArbGeneratesValidManifests(t *testing.T) {
	options := testkit.DefaultPropertyOptions()
	for _, manifest := range testkit.PropertyCases(testkit.ManifestArb, options) {
		wire, err := json.Marshal(manifest)
		require.NoError(t, err)
		parsed, err := schemas.ParseManifest(wire)
		require.NoError(t, err)
		assert.Equal(t, 1, parsed.Version)
		require.NotEmpty(t, parsed.Documents)

		seen := map[string]bool{}
		for _, entry := range parsed.Documents {
			require.False(t, seen[entry.Path], "duplicate doc path %q", entry.Path)
			seen[entry.Path] = true
			assert.True(t, schemas.IsValidManifestPath(entry.Path))
			if entry.Mode != nil {
				assert.True(t, schemas.IsValidDocMode(string(*entry.Mode)))
			}
		}
	}
}

// TestModeMixedManifestArbCarriesExplicitModes ports the P12-T17
// modeMixedManifestArb property: every entry carries an EXPLICIT mode from the
// full ARCH §11.2 vocabulary (a mixed manifest is either fully explicit or
// fully implicit).
func TestModeMixedManifestArbCarriesExplicitModes(t *testing.T) {
	for _, manifest := range testkit.PropertyCases(testkit.ModeMixedManifestArb, testkit.DefaultPropertyOptions()) {
		require.NotEmpty(t, manifest.Documents)
		seen := map[string]bool{}
		for _, entry := range manifest.Documents {
			require.NotNil(t, entry.Mode, "mode-mixed manifests carry explicit modes: %q", entry.Path)
			assert.True(t, schemas.IsValidDocMode(string(*entry.Mode)))
			require.False(t, seen[entry.Path])
			seen[entry.Path] = true
		}
	}
}

// TestFloorArbGeneratesWellFormedPatterns ports the floorArb property: every
// pattern is accepted by AssertValidFloorPattern and LITERALLY matches itself.
func TestFloorArbGeneratesWellFormedPatterns(t *testing.T) {
	for _, floor := range testkit.PropertyCases(testkit.FloorArb, testkit.DefaultPropertyOptions()) {
		require.NoError(t, modes.AssertValidFloorPattern(floor.Pattern))
		matches, err := modes.FloorMatchesPath(floor.Pattern, floor.Pattern)
		require.NoError(t, err)
		assert.True(t, matches, "pattern %q must match itself", floor.Pattern)

		switch floor.Kind {
		case schemas.FloorKindModeCeiling:
			assert.True(t, schemas.IsValidDocMode(string(floor.MaxMode)))
		case schemas.FloorKindReportOnlyUntilReviewed:
			// Absence is NOT a review: the flag is left unset here.
			assert.Nil(t, floor.Reviewed)
		default:
			t.Fatalf("unknown floor kind %q", floor.Kind)
		}
	}
}

// TestRunKeyArbGeneratesFilenameSafeKeys ports the runKeyArb property: every
// field is filename-safe in the `repo#base..head` run filename.
func TestRunKeyArbGeneratesFilenameSafeKeys(t *testing.T) {
	safeFilename := regexp.MustCompile(`^[A-Za-z0-9._#-]+$`)
	for _, key := range testkit.PropertyCases(testkit.RunKeyArb, testkit.DefaultPropertyOptions()) {
		filename, err := state.RunRecordFilename(key)
		require.NoError(t, err)
		assert.Contains(t, filename, "#")
		assert.True(t, strings.HasSuffix(filename, ".json"))
		assert.Regexp(t, safeFilename, filename)
		assert.Equal(t, 3, strings.Count(filename, "#"),
			"the run filename separates repo, shas and the m/v versions with '#'")
	}
}

// TestLearningModeArbDrawsTheFrozenVocabulary pins the learning-mode pool
// (off/propose/canary/auto — the TS LEARNING_MODE_VALUES set).
func TestLearningModeArbDrawsTheFrozenVocabulary(t *testing.T) {
	rng := testkit.NewSeededRNG(testkit.DefaultPropertySeed)
	seen := map[schemas.LearningMode]bool{}
	for i := 0; i < testkit.DefaultNumRuns; i++ {
		mode := testkit.LearningModeArb(rng)
		require.True(t, schemas.IsValidLearningMode(string(mode)))
		seen[mode] = true
	}
	assert.Len(t, seen, len(schemas.LearningModeValues))
}

// TestSeededRNGIsDeterministicForOneSeed pins the replay contract: identical
// seeds produce identical streams, different seeds diverge.
func TestSeededRNGIsDeterministicForOneSeed(t *testing.T) {
	options := testkit.PropertyOptions{NumRuns: 25, Seed: 7}
	first := testkit.PropertyCases(testkit.SegmentArb, options)
	second := testkit.PropertyCases(testkit.SegmentArb, options)
	assert.Equal(t, first, second)

	other := testkit.PropertyCases(testkit.SegmentArb, testkit.PropertyOptions{NumRuns: 25, Seed: 8})
	assert.NotEqual(t, first, other)
}

// TestCheckPropertyAssertsRunCountAndIsRepeatable pins the two runner
// promises: the predicate is invoked exactly NumRuns times, and the same
// options regenerate the same case sequence.
func TestCheckPropertyAssertsRunCountAndIsRepeatable(t *testing.T) {
	options := testkit.PropertyOptions{NumRuns: 40, Seed: 99}
	invocations := 0
	err := testkit.CheckProperty("run count", testkit.DocPathArb, func(docPath string) error {
		invocations++
		require.NotEmpty(t, docPath)
		return nil
	}, options)
	require.NoError(t, err)
	assert.Equal(t, 40, invocations)

	first := testkit.PropertyCases(testkit.DocPathArb, options)
	second := testkit.PropertyCases(testkit.DocPathArb, options)
	assert.Equal(t, first, second, "same options must replay the same cases")
}

// TestCheckPropertyReportsReplayableViolation pins the failure contract: the
// violation names the seed and the run index, carries the failing case, and
// renders it deterministically.
func TestCheckPropertyReportsReplayableViolation(t *testing.T) {
	options := testkit.PropertyOptions{NumRuns: 10, Seed: 4242}
	alphaFirst := func(rng *testkit.SeededRNG) string {
		return "alpha/" + testkit.SegmentArb(rng)
	}
	err := testkit.CheckProperty("replay", alphaFirst, func(path string) error {
		if strings.HasPrefix(path, "alpha/") {
			return fmt.Errorf("refused path %q", path)
		}
		return nil
	}, options)
	require.Error(t, err)

	var violation *testkit.PropertyViolation
	require.ErrorAs(t, err, &violation)
	assert.Equal(t, "replay", violation.Name)
	assert.Equal(t, int64(4242), violation.Seed)
	assert.Equal(t, 10, violation.NumRuns)
	assert.GreaterOrEqual(t, violation.Run, 1)
	assert.LessOrEqual(t, violation.Run, 10)
	assert.Contains(t, violation.Case, "alpha")
	assert.Contains(t, violation.Error(), "seed 4242")
	assert.Contains(t, violation.Error(), "run 1/10")
}

// TestRunPropertyRunsTheStandardizedDefaults pins DEFAULT_NUM_RUNS: 200 cases
// on the fixed seed, with no clock and no wall-clock dependence.
func TestRunPropertyRunsTheStandardizedDefaults(t *testing.T) {
	options := testkit.DefaultPropertyOptions()
	assert.Equal(t, 200, options.NumRuns)

	invocations := 0
	testkit.RunProperty(t, "default run count", testkit.DocModeArb, func(mode schemas.DocMode) error {
		invocations++
		require.True(t, schemas.IsValidDocMode(string(mode)))
		return nil
	})
	assert.Equal(t, testkit.DefaultNumRuns, invocations)
}

// TestRunPropertyWithExplicitOptions pins the option override path.
func TestRunPropertyWithExplicitOptions(t *testing.T) {
	invocations := 0
	testkit.RunProperty(t, "shrunken run count", testkit.SegmentArb, func(segment string) error {
		invocations++
		require.NotEmpty(t, segment)
		return nil
	}, testkit.PropertyOptions{NumRuns: 5, Seed: 11})
	assert.Equal(t, 5, invocations)
}

/* ================================ fakes: state ============================= */

// baselineWire renders a registry-valid `docsync.baseline/v1` payload for the
// tests (the TS suite used MANIFEST/BASELINE literals the same way).
func baselineWire(t *testing.T, docPath, contentHash, baselineCommit string) string {
	t.Helper()
	raw := fmt.Sprintf(`{"doc_path":%q,"doc_content_hash":%q,`+
		`"baseline_commit":%q,"source_files":["src/a.ts"],"source_symbols":[],`+
		`"claim_ids":[],"verified_at":"2026-10-01T00:00:00Z","verified_by_run":"run_01","policy_version":1}`,
		docPath, contentHash, baselineCommit)
	_, err := schemas.ParseBaseline([]byte(raw))
	require.NoError(t, err)
	return raw
}

// parseBaseline validates and decodes a baseline payload.
func parseBaseline(t *testing.T, raw string) *schemas.Baseline {
	t.Helper()
	baseline, err := schemas.ParseBaseline([]byte(raw))
	require.NoError(t, err)
	return baseline
}

// TestInMemoryStateStoreCASWinnerAndLoserReRun ports the V-P0-11 parity check:
// the winner's write lands, the stale-hash loser loses the CAS AND changes
// nothing, and a re-run with the fresh hash lands.
func TestInMemoryStateStoreCASWinnerAndLoserReRun(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	hash := docutil.ContentHash("docs content v1")

	initial := parseBaseline(t, baselineWire(t, "docs/a.md", hash, "commit_initial"))
	winner, err := fake.WriteBaseline("docs/a.md", *initial, nil)
	require.NoError(t, err)
	require.True(t, winner.OK)

	read, err := fake.ReadBaseline("docs/a.md")
	require.NoError(t, err)
	require.True(t, read.Exists)
	assert.Equal(t, docutil.ContentHash(read.Raw), read.Hash)

	stale := read.Hash + "0"
	updated := parseBaseline(t, baselineWire(t, "docs/a.md", hash, "commit_updated"))
	loser, err := fake.WriteBaseline("docs/a.md", *updated, &stale)
	require.NoError(t, err)
	assert.False(t, loser.OK)
	assert.Equal(t, stale, stringOf(loser.ExpectedHash))
	assert.Equal(t, read.Hash, stringOf(loser.ActualHash))

	// The loser's bytes survive untouched.
	after, err := fake.ReadBaseline("docs/a.md")
	require.NoError(t, err)
	assert.Equal(t, *initial, *after.Content)

	// A re-run with the freshly read hash wins.
	fresh := after.Hash
	rerun, err := fake.WriteBaseline("docs/a.md", *updated, &fresh)
	require.NoError(t, err)
	require.True(t, rerun.OK)
	final, err := fake.ReadBaseline("docs/a.md")
	require.NoError(t, err)
	assert.Equal(t, "commit_updated", final.Content.BaselineCommit)
}

// TestInMemoryStateStoreCreateOverExistingAndMissingExpected ports the two
// create-over/absent-expectation CAS branches.
func TestInMemoryStateStoreCreateOverExistingAndMissingExpected(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	hash := docutil.ContentHash("docs content v1")

	baseline := parseBaseline(t, baselineWire(t, "docs/a.md", hash, "commit_1"))
	created, err := fake.WriteBaseline("docs/a.md", *baseline, nil)
	require.NoError(t, err)
	require.True(t, created.OK)

	over, err := fake.WriteBaseline("docs/a.md", *baseline, nil)
	require.NoError(t, err)
	assert.False(t, over.OK, "a create (nil expectation) loses when the record exists")
	assert.Nil(t, over.ExpectedHash)
	require.NotNil(t, over.ActualHash)

	absentExpectation := "0deadbeef"
	missing, err := fake.WriteBaseline("docs/b.md", *baseline, &absentExpectation)
	require.NoError(t, err)
	assert.False(t, missing.OK)
	assert.Equal(t, "0deadbeef", stringOf(missing.ExpectedHash))
	assert.Nil(t, missing.ActualHash, "an absent file reports a nil actual hash")

	absent, err := fake.ReadBaseline("docs/b.md")
	require.NoError(t, err)
	assert.False(t, absent.Exists)
}

// TestInMemoryStateStoreStoresCanonicalBytes pins the byte contract: the fake
// writes canonical JSON (sorted keys) and hashes exactly those bytes, so the
// fake and the real store produce the same on-disk content for one value.
func TestInMemoryStateStoreStoresCanonicalBytes(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	hash := docutil.ContentHash("docs content v1")
	baseline := parseBaseline(t, baselineWire(t, "docs/a.md", hash, "commit_1"))
	_, err := fake.WriteBaseline("docs/a.md", *baseline, nil)
	require.NoError(t, err)

	// The canonical form: json.Marshal of the parsed record with sorted keys,
	// which is exactly what docutil.CanonicalJSON produces for the decoded
	// generic value.
	wire, err := json.Marshal(baseline)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.UseNumber()
	var generic any
	require.NoError(t, decoder.Decode(&generic))
	var expected bytes.Buffer
	encoder := json.NewEncoder(&expected)
	encoder.SetEscapeHTML(false)
	require.NoError(t, encoder.Encode(generic))

	relPath, err := state.BaselineRelPath("docs/a.md")
	require.NoError(t, err)
	files := fake.Files()
	require.Contains(t, files, relPath)
	assert.Equal(t, docutil.ContentHash(strings.TrimSuffix(expected.String(), "\n")), docutil.ContentHash(files[relPath]))
	assert.True(t, strings.HasPrefix(files[relPath], `{"baseline_commit":"commit_1"`),
		"canonical bytes start with the alphabetically first key: %s", files[relPath])
	// Recursively sorted keys: baseline_commit < claim_ids < doc_content_hash.
	assert.Less(t, strings.Index(files[relPath], `"baseline_commit"`), strings.Index(files[relPath], `"claim_ids"`))
	assert.Less(t, strings.Index(files[relPath], `"claim_ids"`), strings.Index(files[relPath], `"doc_content_hash"`))
	assert.Contains(t, files[relPath], "\"doc_path\":\"docs/a.md\"")
}

// TestInMemoryStateStoreUpsertRunRecordAndAppendTransitions ports the
// upsert/append parity: same key ⇒ idempotent upsert, a different key payload
// ⇒ refused, and transitions append in order under CAS.
func TestInMemoryStateStoreUpsertRunRecordAndAppendTransitions(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	key := schemas.RunKey{Repo: "acme/payments", BaseSHA: "8a73f2c", HeadSHA: "81af92e", ManifestVersion: 1, PolicyVersion: 17}
	record := schemas.RunRecordExample()

	first, err := fake.UpsertRunRecord(key, record)
	require.NoError(t, err)
	require.True(t, first.OK)
	assert.True(t, first.Created)

	again, err := fake.UpsertRunRecord(key, record)
	require.NoError(t, err)
	require.True(t, again.OK)
	assert.False(t, again.Created, "same key re-upsert updates, never re-creates")

	// Same key ⇒ the record is re-upserted (never a second create).
	drifted := schemas.RunRecordExample()
	drifted.RunID = "run_other"
	upserted, err := fake.UpsertRunRecord(key, drifted)
	require.NoError(t, err)
	require.True(t, upserted.OK)
	assert.False(t, upserted.Created)
	storedRecord, err := fake.ReadRunRecord(key)
	require.NoError(t, err)
	require.True(t, storedRecord.Exists)
	assert.Equal(t, "run_other", storedRecord.Content.RunID)

	// A record holding a DIFFERENT key under the same filename is corrupt and
	// is refused rather than overwritten (fail closed).
	relPath, err := state.RunRecordRelPath(key)
	require.NoError(t, err)
	otherKey := schemas.RunKey{Repo: "acme/other", BaseSHA: "0000000", HeadSHA: "0000001", ManifestVersion: 1, PolicyVersion: 1}
	other := schemas.RunRecordExample()
	other.Key = otherKey
	otherWire, err := json.Marshal(other)
	require.NoError(t, err)
	plantRaw(t, fake, relPath, string(otherWire))
	_, err = fake.UpsertRunRecord(key, drifted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite a different run")

	// Append-only transitions.
	writeDoc, err := fake.WriteDocState("docs/a.md", schemas.DocState{
		Path:          "docs/a.md",
		State:         schemas.DocStateVerified,
		Since:         "2026-10-01T00:00:00Z",
		PolicyVersion: 1,
		Transitions:   nil,
	}, nil)
	require.NoError(t, err)
	require.True(t, writeDoc.OK)

	appended, err := fake.AppendTransition("docs/a.md", schemas.DocStateTransition{
		State:  schemas.DocStateSuspect,
		At:     "2026-10-02T00:00:00Z",
		RunID:  "run_02",
		Reason: "src/a.ts changed",
	}, &writeDoc.Hash)
	require.NoError(t, err)
	require.True(t, appended.OK)

	docState, err := fake.ReadDocState("docs/a.md")
	require.NoError(t, err)
	require.NotNil(t, docState.Content)
	require.Len(t, docState.Content.Transitions, 1)
	assert.Equal(t, "run_02", docState.Content.Transitions[0].RunID)
}

// TestInMemoryStateStoreRefusesKeyMismatch ports the key-discipline refusals:
// merge-ledger source_key and run-record key must match the write key.
func TestInMemoryStateStoreRefusesKeyMismatch(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})

	// The run record refuses a payload key that differs from the upsert key.
	record := schemas.RunRecordExample()
	record.Key = schemas.RunKey{Repo: "acme/other", BaseSHA: "0000000", HeadSHA: "0000001", ManifestVersion: 1, PolicyVersion: 1}
	_, err := fake.UpsertRunRecord(schemas.RunKey{
		Repo: "acme/payments", BaseSHA: "8a73f2c", HeadSHA: "81af92e", ManifestVersion: 1, PolicyVersion: 17,
	}, record)
	require.Error(t, err)
	var keyError *docutil.Error
	require.ErrorAs(t, err, &keyError)
	assert.Equal(t, docutil.CodeStateKey, docutil.CodeOf(err))
}

// TestInMemoryStateStoreRefusesAppendToMissingDocState ports the fail-closed
// append: the append-only log requires an existing record.
func TestInMemoryStateStoreRefusesAppendToMissingDocState(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	write, err := fake.AppendTransition("docs/missing.md", schemas.DocStateTransition{
		State: schemas.DocStateSuspect, At: "2026-10-02T00:00:00Z", RunID: "run_02", Reason: "drift",
	}, nil)
	require.Error(t, err)
	assert.False(t, write.OK)
	assert.Contains(t, err.Error(), "does not exist")
}

// TestInMemoryStateStoreRefusesSchemaDrift ports the registry validation at
// BOTH edges: a write whose payload is not a valid registry record fails
// closed, and a drifted/corrupt file on read fails closed too.
func TestInMemoryStateStoreRefusesSchemaDrift(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	valid := parseBaseline(t, baselineWire(t, "docs/a.md", docutil.ContentHash("v1"), "commit_1"))

	// A schema-invalid payload never reaches the map: verified_at must be a
	// real ISO-8601 instant, and the registry refuses the write before any
	// byte moves.
	invalid := *valid
	invalid.VerifiedAt = "not-a-datetime"
	_, err := fake.WriteBaseline("docs/a.md", invalid, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verified_at")

	// The valid record still lands afterwards.
	_, err = fake.WriteBaseline("docs/a.md", *valid, nil)
	require.NoError(t, err)

	relPath, err := state.BaselineRelPath("docs/a.md")
	require.NoError(t, err)

	// Corrupt (non-JSON) bytes: refused on READ, never "no baseline".
	plantRaw(t, fake, relPath, "{not json")
	read, err := fake.ReadBaseline("docs/a.md")
	require.Error(t, err)
	assert.False(t, read.Exists)
	assert.Contains(t, err.Error(), "not valid JSON")

	// Schema drift: valid JSON, wrong artifact shape → refused on READ.
	plantRaw(t, fake, relPath, `{"id":"x","text":"y"}`)
	_, err = fake.ReadBaseline("docs/a.md")
	require.Error(t, err)
}

// TestInMemoryStateStoreGuardRefusalScope ports the 3-ownership-model guard
// checks: config (`manifest.yml`, `policy.yml`) and "docs/* live under the
// repo, not under .docsync/" are refused, while `state/`-owned paths are
// allowed — and the refusal carries the TS message shape.
func TestInMemoryStateStoreGuardRefusalScope(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})

	_, err := fake.WriteRawForTest("manifest.yml", "{}", nil)
	require.Error(t, err)
	var guardError *boundaries.GuardedPathError
	require.ErrorAs(t, err, &guardError)
	assert.Equal(t, "manifest.yml", guardError.RelPath)
	assert.Contains(t, err.Error(), "state write refused for \"manifest.yml\"")
	assert.Contains(t, err.Error(), "state write refused for")

	_, err = fake.WriteRawForTest("policy.yml", "{}", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state write refused for \"policy.yml\"")

	_, err = fake.WriteRawForTest("docs/payments.md", "#", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state write refused for \"docs/payments.md\"")

	_, err = fake.WriteRawForTest("state/rogue.json", "{}", nil)
	require.NoError(t, err)
}

// TestInMemoryStateStoreRefusesTraversalAndNotStatePaths pins the record-key
// discipline: unsafe doc paths and non-`state/` internal paths are refused.
func TestInMemoryStateStoreRefusesTraversalAndNotStatePaths(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	hash := docutil.ContentHash("v1")
	baseline := parseBaseline(t, baselineWire(t, "docs/a.md", hash, "commit_1"))

	_, err := fake.WriteBaseline("../escape.md", *baseline, nil)
	require.Error(t, err)
	var keyError *docutil.Error
	require.ErrorAs(t, err, &keyError)
	assert.Equal(t, docutil.CodeStateKey, docutil.CodeOf(err))
}

// TestInMemoryStateStoreCorruptReadFailsClosed pins that corruption DOES
// propagate loudly: an unreadable doc state is an error, not "no doc state".
func TestInMemoryStateStoreCorruptReadFailsClosed(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	machine := statemachine.NewDocStateMachine(fake)
	require.Nil(t, machineState(t, machine, "docs/a.md"))

	// The record exists but is corrupt → the read FAILS instead of returning
	// nil, so a "no doc state" answer can never mask corruption.
	_, err := fake.WriteRawForTest(stateFileOrDie(t, "docs/a.md"), "corrupt", nil)
	require.NoError(t, err)
	_, err = machine.GetState("docs/a.md")
	require.Error(t, err)
}

// machineState is the nil-safe machine read used by the corruption test.
func machineState(t *testing.T, machine *statemachine.DocStateMachine, docPath string) *schemas.DocState {
	t.Helper()
	docState, err := machine.GetState(docPath)
	require.NoError(t, err)
	return docState
}

// stateFileOrDie resolves a doc-state rel path (test helper).
func stateFileOrDie(t *testing.T, docPath string) string {
	t.Helper()
	relPath, err := state.DocStateRelPath(docPath)
	require.NoError(t, err)
	return relPath
}

// TestInMemoryStateStoreCountsChurnHook ops the V-P0-11 churn hook: exactly
// one hook op per successful write, zero on a failed CAS.
func TestInMemoryStateStoreCountsChurnHook(t *testing.T) {
	counter := &testkit.RecordingStateChurnHook{}
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{Churn: counter})

	hash := docutil.ContentHash("v1")
	baseline := parseBaseline(t, baselineWire(t, "docs/a.md", hash, "commit_1"))
	first, err := fake.WriteBaseline("docs/a.md", *baseline, nil)
	require.NoError(t, err)
	require.True(t, first.OK)

	stale := first.Hash + "0"
	loser, err := fake.WriteBaseline("docs/a.md", *baseline, &stale)
	require.NoError(t, err)
	require.False(t, loser.OK)

	second, err := fake.WriteBaseline("docs/a.md", *baseline, &first.Hash)
	require.NoError(t, err)
	require.True(t, second.OK)

	assert.Equal(t, 2, counter.Added()+counter.Updated(), "created + updated")
	assert.Equal(t, 1, counter.Added())
	assert.Equal(t, 1, counter.Updated())
}

// TestInMemoryStateStoreSubstitutesTheRealStoreBackends exercises the fake in
// place of the real store across the three structural backends the real store
// satisfies: baselines, claims and the doc-state machine.
func TestInMemoryStateStoreSubstitutesTheRealStoreBackends(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})

	// baselines over the fake.
	upsert, err := baselines.NewBaselineStore(fake).Upsert(*parseBaseline(t,
		baselineWire(t, "docs/a.md", docutil.ContentHash("v1"), "commit_1")), baselines.BaselineGuards{
		ExpectedDocContentHash: docutil.ContentHash("v1"),
		ExpectedBaselineCommit: "commit_1",
	})
	require.NoError(t, err)
	require.True(t, upsert.OK)
	require.True(t, upsert.Created)

	stored, err := baselines.NewBaselineStore(fake).Get("docs/a.md")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "commit_1", stored.BaselineCommit)

	// claims over the fake: a CAS-create of a registry-valid claim, then a
	// second create that loses the CAS.
	claimStore := claims.NewClaimStore(fake)
	claimID := "claim_added_source_symbols"
	claim := parseClaim(t, schemaClaimOrDie(t, claimID))
	absent, err := claimStore.Get(claimID)
	require.NoError(t, err)
	assert.Nil(t, absent, "no claim exists before the create")

	created, err := claimStore.Upsert(*claim, &claims.ClaimUpsertOptions{})
	require.NoError(t, err)
	assert.True(t, created.OK)

	second, err := claimStore.Upsert(*claim, &claims.ClaimUpsertOptions{})
	require.NoError(t, err)
	assert.False(t, second.OK, "a second create loses the CAS")

	readClaim, err := claimStore.Get(claimID)
	require.NoError(t, err)
	require.NotNil(t, readClaim)
	assert.Equal(t, claimID, readClaim.ID)

	// the doc-state machine over the fake.
	machine := statemachine.NewDocStateMachine(fake)
	initialized, err := machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       "docs/a.md",
		To:            schemas.DocStateVerified,
		RunID:         "run_10",
		PolicyVersion: 1,
		At:            "2026-10-01T00:00:00Z",
	})
	require.NoError(t, err)
	require.True(t, initialized.OK)

	states, err := machine.ListStates()
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, schemas.DocStateVerified, states[0].State)
}

// parseClaim validates and decodes a claim payload.
func parseClaim(t *testing.T, raw string) *schemas.Claim {
	t.Helper()
	claim, err := schemas.ParseClaim([]byte(raw))
	require.NoError(t, err)
	return claim
}

// schemaClaimOrDie renders a registry-valid claim payload (test helper).
func schemaClaimOrDie(t *testing.T, id string) string {
	t.Helper()
	raw := fmt.Sprintf(`{"id":%q,"text":"A claim.",`+
		`"anchor":{"file":"docs/a.md","start_line":1,"end_line":1,"content_hash":%q},`+
		`"source":{"file":"src/a.ts","blob_sha":%q},"status":"verified","history":[]}`, id,
		docutil.ContentHash("A claim."), docutil.ContentHash("source"))
	_, err := schemas.ParseClaim([]byte(raw))
	require.NoError(t, err)
	return raw
}

// TestInMemoryStateStoreListsPathsSorted ports the deterministic list
// behavior: doc-state paths and claim ids come back sorted.
func TestInMemoryStateStoreListsPathsSorted(t *testing.T) {
	fake := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	machine := statemachine.NewDocStateMachine(fake)
	for _, docPath := range []string{"docs/c.md", "docs/a.md", "docs/b.md"} {
		initialized, err := machine.Initialize(statemachine.InitializeStateInput{
			DocPath: docPath, To: schemas.DocStateVerified, RunID: "run_10", PolicyVersion: 1,
			At: "2026-10-01T00:00:00Z",
		})
		require.NoError(t, err)
		require.True(t, initialized.OK)
	}
	paths, err := fake.ListDocStatePaths()
	require.NoError(t, err)
	assert.Equal(t, []string{"docs/a.md", "docs/b.md", "docs/c.md"}, paths)
	assert.True(t, sort.StringsAreSorted(paths))
}

// TestInMemoryStateStoreCoversEveryStateArtifact exercises the remaining
// registry artifacts through the fake: runs (already covered), the audit
// cursor, the merge ledger and the claim listing — the artifacts the real
// store also owns.
func TestInMemoryStateStoreCoversEveryStateArtifact(t *testing.T) {
	fake := testkit.CreateInMemoryStateStore(testkit.InMemoryStateStoreOptions{})

	// The audit cursor: create, then a CAS update on the fresh hash.
	cursorWrite, err := fake.WriteAuditCursor(schemas.AuditCursor{
		Position: "2026-10-01T00:00:00Z", UpdatedAt: "2026-10-01T00:00:00Z",
	}, nil)
	require.NoError(t, err)
	require.True(t, cursorWrite.OK)
	cursorRead, err := fake.ReadAuditCursor()
	require.NoError(t, err)
	require.True(t, cursorRead.Exists)
	assert.Equal(t, "2026-10-01T00:00:00Z", cursorRead.Content.Position)

	cursorUpdate, err := fake.WriteAuditCursor(schemas.AuditCursor{
		Position: "2026-10-02T00:00:00Z", UpdatedAt: "2026-10-02T00:00:00Z",
	}, &cursorRead.Hash)
	require.NoError(t, err)
	require.True(t, cursorUpdate.OK)

	// The merge ledger: keyed by its own source_key, unique doc_pr_number.
	ledger := schemas.MergeLedgerExample()
	ledgerWrite, err := fake.WriteMergeLedger(ledger.SourceKey, ledger, nil)
	require.NoError(t, err)
	require.True(t, ledgerWrite.OK)
	ledgerRead, err := fake.ReadMergeLedger(ledger.SourceKey)
	require.NoError(t, err)
	require.True(t, ledgerRead.Exists)
	require.Len(t, ledgerRead.Content.Entries, 2)

	// A key mismatch fails closed before any byte moves.
	mismatched := ledger
	mismatched.SourceKey = "source-pr-99"
	_, err = fake.WriteMergeLedger("source-pr-42", mismatched, nil)
	require.Error(t, err)
	var keyError *docutil.Error
	require.ErrorAs(t, err, &keyError)

	// An unknown source key reads as absent (never a zeros record).
	absent, err := fake.ReadMergeLedger("source-pr-999")
	require.NoError(t, err)
	assert.False(t, absent.Exists)

	// Claim listing over created claims.
	claimStore := claims.NewClaimStore(fake)
	claim := parseClaim(t, schemaClaimOrDie(t, "claim_list_index"))
	writeClaim, err := claimStore.Upsert(*claim, &claims.ClaimUpsertOptions{})
	require.NoError(t, err)
	require.True(t, writeClaim.OK)
	ids, err := fake.ListClaimIds()
	require.NoError(t, err)
	assert.Equal(t, []string{"claim_list_index"}, ids)

	// A missing claim reads as absent (never a zeros record).
	missing, err := fake.ReadClaim("claim_not_there")
	require.NoError(t, err)
	assert.False(t, missing.Exists)
}

/* ============================ fakes: git platform ========================== */

// TestGitPlatformFakeRecordsCallsWithMonotonicIds ports the TS
// "deterministic default ids" case: synthesized PR numbers and check-run ids
// advance monotonically, starting from the configured series, and every call
// is recorded with a monotonic `at` counter.
func TestGitPlatformFakeRecordsCallsWithMonotonicIds(t *testing.T) {
	fake := testkit.NewGitPlatformFake(testkit.GitPlatformOptions{})
	firstPR, err := fake.CreatePullRequest(testkit.CreatePullRequestInput{Title: "Docs PR", Body: "b", Head: "head_1", Base: "main"})
	require.NoError(t, err)
	assert.Equal(t, 101, firstPR.Number)
	assert.Equal(t, "https://gitplatform.fake/pulls/101", firstPR.URL)
	assert.Equal(t, testkit.PRStateOpen, firstPR.State)

	secondPR, err := fake.CreatePullRequest(testkit.CreatePullRequestInput{Title: "Docs PR 2", Body: "b", Head: "head_2", Base: "main"})
	require.NoError(t, err)
	assert.Equal(t, 102, secondPR.Number)

	// Run creation pins the recorded status.
	check, err := fake.CreateCheckRun(testkit.CreateCheckRunInput{
		Name: "docsync", HeadSHA: firstPR.Head, Status: testkit.CheckRunStatusInProgress,
	})
	require.NoError(t, err)
	assert.Equal(t, 9001, check.ID)
	assert.Equal(t, testkit.CheckRunStatusInProgress, check.Status)
	assert.Equal(t, firstPR.Head, check.HeadSHA)
	assert.Nil(t, check.Conclusion)

	listed, err := fake.ListChangedFiles("base_1", "head_1")
	require.NoError(t, err)
	assert.Empty(t, listed)

	listedPRs, err := fake.ListPullRequests(testkit.ListPullRequestsInput{})
	require.NoError(t, err)
	assert.Empty(t, listedPRs)

	calls := fake.Calls()
	require.Len(t, calls, 5)
	for index, call := range calls {
		assert.Equal(t, index, call.At)
	}
	assert.Equal(t, testkit.GitPlatformMethodCreatePullRequest, calls[0].Method)
	assert.Equal(t, testkit.GitPlatformMethodCreatePullRequest, calls[1].Method)
	assert.Equal(t, testkit.GitPlatformMethodCreateCheckRun, calls[2].Method)
	assert.Equal(t, testkit.GitPlatformMethodListChangedFiles, calls[3].Method)

	fake.ResetCalls()
	assert.Empty(t, fake.Calls())
}

// TestGitPlatformFakeCheckRunStatusMerge ports the deterministic check-run
// default: a status update merges over the created run and records the new
// status/conclusion.
func TestGitPlatformFakeCheckRunStatusMerge(t *testing.T) {
	fake := testkit.NewGitPlatformFake(testkit.GitPlatformOptions{})
	created, err := fake.CreateCheckRun(testkit.CreateCheckRunInput{
		Name: "docsync", HeadSHA: "head_1", Status: testkit.CheckRunStatusQueued,
	})
	require.NoError(t, err)

	completed := testkit.CheckRunStatusCompleted
	success := testkit.CheckRunConclusionSuccess
	updated, err := fake.UpdateCheckRun(created.ID, testkit.UpdateCheckRunPatch{
		Status: &completed, Conclusion: &success,
	})
	require.NoError(t, err)
	assert.Equal(t, testkit.CheckRunStatusCompleted, updated.Status)
	assert.Equal(t, testkit.CheckRunConclusionSuccess, *updated.Conclusion)
	assert.Equal(t, created.HeadSHA, updated.HeadSHA)
}

// TestGitPlatformFakeKeyedDispatchIsNotPositionalFIFO ports the AGENT-behavior
// replay rule: scripted responses dispatch by REQUEST IDENTITY, so concurrent
// callers with different identities each get their own record — completion
// order cannot cross-wire the script.
func TestGitPlatformFakeKeyedDispatchIsNotPositionalFIFO(t *testing.T) {
	const callers = 16
	fake := testkit.NewGitPlatformFake(testkit.GitPlatformOptions{})

	// Every caller gets its OWN (ref, path) identity with its own content, so
	// a positional (FIFO) script would hand out somebody else's bytes to at
	// least half of them.
	contents := make([]string, callers)
	for index := 0; index < callers; index++ {
		ref := "ref_base"
		if index%2 == 0 {
			ref = "ref_head"
		}
		path := fmt.Sprintf("docs/a-%02d.md", index)
		content := fmt.Sprintf("content-for-%s-%s", ref, path)
		contents[index] = content
		fake.OnGetFileContent(ref, path, testkit.FileContent{
			Ref: ref, Path: path, Content: &content, Encoding: testkit.ContentEncodingUTF8,
		})
	}

	results := make([]string, callers)
	failures := make([]error, callers)
	var waitGroup sync.WaitGroup
	for index := 0; index < callers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			ref := "ref_base"
			if index%2 == 0 {
				ref = "ref_head"
			}
			path := fmt.Sprintf("docs/a-%02d.md", index)
			content, err := fake.GetFileContent(ref, path)
			if err != nil {
				failures[index] = err
				return
			}
			if content.Content == nil {
				failures[index] = fmt.Errorf("nil content for %s %s", ref, path)
				return
			}
			results[index] = *content.Content
		}(index)
	}
	waitGroup.Wait()

	for index := 0; index < callers; index++ {
		require.NoError(t, failures[index], "caller %d", index)
		assert.Equal(t, contents[index], results[index], "caller %d crossed the identity wire", index)
	}
	assert.Len(t, fake.Calls(), callers)
}

// TestGitPlatformFakeScriptsEveryMethodThroughTheFluentAPI ports the fluent
// script API: every method's On* helper keys the response by that method's
// request identity, and a scripted method prefers the script over its default.
func TestGitPlatformFakeScriptsEveryMethodThroughTheFluentAPI(t *testing.T) {
	fake := testkit.NewGitPlatformFake(testkit.GitPlatformOptions{})

	listInput := testkit.ListPullRequestsInput{}
	prs := []testkit.PullRequestRef{{Number: 7, URL: "https://x/pulls/7", Title: "t", Head: "h", Base: "b", State: testkit.PRStateClosed}}
	fake.OnListPullRequests(listInput, prs)

	createInput := testkit.CreatePullRequestInput{Title: "t", Body: "b", Head: "h", Base: "b"}
	pr := testkit.PullRequestRef{Number: 12, URL: "https://x/pulls/12", Title: "t", Head: "h", Base: "b", State: testkit.PRStateClosed}
	fake.OnCreatePullRequest(createInput, pr)

	checkInput := testkit.CreateCheckRunInput{Name: "n", HeadSHA: "h", Status: testkit.CheckRunStatusQueued}
	run := testkit.CheckRunRef{ID: 4242, Name: "n", HeadSHA: "h", Status: testkit.CheckRunStatusCompleted}
	fake.OnCreateCheckRun(checkInput, run)

	patch := testkit.UpdateCheckRunPatch{Conclusion: conclusionPointer(testkit.CheckRunConclusionSkipped)}
	updated := testkit.CheckRunRef{ID: 4242, Name: "n", HeadSHA: "h", Status: testkit.CheckRunStatusCompleted,
		Conclusion: conclusionPointer(testkit.CheckRunConclusionSkipped)}
	fake.OnUpdateCheckRun(4242, patch, updated)

	baseContent := "base bytes"
	fake.OnGetFileContent("ref", "docs/a.md", testkit.FileContent{
		Ref: "ref", Path: "docs/a.md", Content: &baseContent, Encoding: testkit.ContentEncodingUTF8,
	})
	changed := []testkit.ChangedFile{{Path: "docs/a.md", Status: testkit.ChangedFileStatusModified}}
	fake.OnListChangedFiles("b", "h", changed)

	listedPRs, err := fake.ListPullRequests(listInput)
	require.NoError(t, err)
	require.Len(t, listedPRs, 1)
	assert.Equal(t, 7, listedPRs[0].Number)

	createdPR, err := fake.CreatePullRequest(createInput)
	require.NoError(t, err)
	assert.Equal(t, 12, createdPR.Number, "the scripted PR wins over the synthesized counter")

	createdRun, err := fake.CreateCheckRun(checkInput)
	require.NoError(t, err)
	assert.Equal(t, 4242, createdRun.ID)

	updatedRun, err := fake.UpdateCheckRun(4242, patch)
	require.NoError(t, err)
	require.NotNil(t, updatedRun.Conclusion)
	assert.Equal(t, testkit.CheckRunConclusionSkipped, *updatedRun.Conclusion)

	content, err := fake.GetFileContent("ref", "docs/a.md")
	require.NoError(t, err)
	require.NotNil(t, content.Content)
	assert.Equal(t, "base bytes", *content.Content)

	changedFiles, err := fake.ListChangedFiles("b", "h")
	require.NoError(t, err)
	require.Len(t, changedFiles, 1)
	assert.Equal(t, testkit.ChangedFileStatusModified, changedFiles[0].Status)

	assert.Len(t, fake.Calls(), 6)
}

// TestGitPlatformFakeFailsLoudOnUnknownIdentity ports the fail-closed rule: a
// scripted method with no response for the request identity errors instead of
// silently returning a default.
func TestGitPlatformFakeFailsLoudOnUnknownIdentity(t *testing.T) {
	fake := testkit.NewGitPlatformFake(testkit.GitPlatformOptions{})
	fake.OnGetFileContent("ref_known", "docs/a.md", testkit.FileContent{
		Ref: "ref_known", Path: "docs/a.md", Content: nil, Encoding: testkit.ContentEncodingUTF8,
	})

	_, err := fake.GetFileContent("ref_known", "docs/a.md")
	require.NoError(t, err)

	_, err = fake.GetFileContent("ref_unknown", "docs/a.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "script exhausted")
	assert.Contains(t, err.Error(), "getFileContent")

	// A fully unscripted method still falls back to its deterministic default.
	changed, err := fake.ListChangedFiles("base", "head")
	require.NoError(t, err)
	assert.Empty(t, changed)
}

// TestGitPlatformFakeFiltersRecordCalls verifies the per-method call filter.
func TestGitPlatformFakeFiltersRecordCalls(t *testing.T) {
	fake := testkit.NewGitPlatformFake(testkit.GitPlatformOptions{})
	_, err := fake.CreateCheckRun(testkit.CreateCheckRunInput{Name: "a", HeadSHA: "h", Status: testkit.CheckRunStatusQueued})
	require.NoError(t, err)
	_, err = fake.CreateCheckRun(testkit.CreateCheckRunInput{Name: "b", HeadSHA: "h", Status: testkit.CheckRunStatusQueued})
	require.NoError(t, err)
	_, err = fake.ListChangedFiles("b", "h")
	require.NoError(t, err)

	checkRuns := fake.CallsFor(testkit.GitPlatformMethodCreateCheckRun)
	require.Len(t, checkRuns, 2)
	assert.Equal(t, 0, checkRuns[0].At)
	assert.Equal(t, 1, checkRuns[1].At)
	assert.Len(t, checkRuns[0].Args, 1)
	assert.Len(t, fake.CallsFor(testkit.GitPlatformMethodGetFileContent), 0)
}

// TestGitPlatformFakeScriptedQueueReplaysIdenticalRequests ports the
// per-identity queue: identical requests ARE interchangeable, so the Nth call
// with one identity consumes the Nth scripted value for it.
func TestGitPlatformFakeScriptedQueueReplaysIdenticalRequests(t *testing.T) {
	fake := testkit.NewGitPlatformFake(testkit.GitPlatformOptions{})
	first := "first"
	second := "second"
	fake.OnGetFileContent("ref", "docs/a.md", testkit.FileContent{
		Ref: "ref", Path: "docs/a.md", Content: &first, Encoding: testkit.ContentEncodingUTF8,
	})
	fake.OnGetFileContent("ref", "docs/a.md", testkit.FileContent{
		Ref: "ref", Path: "docs/a.md", Content: &second, Encoding: testkit.ContentEncodingUTF8,
	})

	one, err := fake.GetFileContent("ref", "docs/a.md")
	require.NoError(t, err)
	assert.Equal(t, "first", *one.Content)
	two, err := fake.GetFileContent("ref", "docs/a.md")
	require.NoError(t, err)
	assert.Equal(t, "second", *two.Content)
	_, err = fake.GetFileContent("ref", "docs/a.md")
	require.Error(t, err)
}

/* ======================= fakes: keyed model-stage doubles ================== */

// TestPipelineFakesDispatchStageCallsByRequestIdentity is the replay-rule core
// test: the writer double is exercised from concurrent goroutines with two
// different doc identities, and every caller receives the record scripted for
// ITS identity (never its neighbour's).
func TestPipelineFakesDispatchStageCallsByRequestIdentity(t *testing.T) {
	fakes := testkit.NewPipelineFakes()
	var _ orchestrator.WriterStagePort = fakes.Writer

	docsPatch := schemas.DocPatchExample
	docsPatchA := docsPatch
	docsPatchA.DocPath = "docs/a.md"
	docsPatchB := docsPatch
	docsPatchB.DocPath = "docs/b.md"
	fakes.Writer.OnDoc("docs/a.md", orchestrator.WriterStageResult{Kind: orchestrator.WriterResultPatch, Patch: &docsPatchA})
	fakes.Writer.OnDoc("docs/b.md", orchestrator.WriterStageResult{Kind: orchestrator.WriterResultPatch, Patch: &docsPatchB})

	var waitGroup sync.WaitGroup
	for i := 0; i < 24; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			docPath := "docs/a.md"
			if index%2 == 1 {
				docPath = "docs/b.md"
			}
			result, err := fakes.Writer.Write(orchestrator.WriterStageInput{
				Doc: orchestrator.WorkspaceDocTarget{DocPath: docPath},
			})
			require.NoError(t, err)
			require.NotNil(t, result.Patch)
			assert.Equal(t, docPath, result.Patch.DocPath)
		}(i)
	}
	waitGroup.Wait()

	calls := fakes.Writer.Calls()
	assert.Len(t, calls, 24)
	assert.Len(t, fakes.Writer.CallsFor("docs/a.md"), 12)
	assert.Len(t, fakes.Writer.CallsFor("docs/b.md"), 12)
}

// TestPipelineFakesFailLoudOnUnscriptedIdentities pins the loud failure: an
// unscripted identity (no default scripted) errors instead of returning a
// zero record that would read as a legitimate abstention.
func TestPipelineFakesFailLoudOnUnscriptedIdentities(t *testing.T) {
	fakes := testkit.NewPipelineFakes()
	var _ orchestrator.DecisionStagePort = fakes.Decision
	var _ orchestrator.VerificationStagePort = fakes.Verification
	var _ orchestrator.PolicyStagePort = fakes.Policy
	var _ orchestrator.EarlySignalPort = fakes.EarlySignal

	_, err := fakes.Decision.Decide(orchestrator.DecisionStageInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request identity")
	assert.Contains(t, err.Error(), "never positional FIFO")

	// An explicit default covers every unscripted identity.
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{Abstained: false})
	result, err := fakes.Decision.Decide(orchestrator.DecisionStageInput{})
	require.NoError(t, err)
	assert.False(t, result.Abstained)
	assert.Nil(t, result.Decision)
	assert.Len(t, fakes.Decision.Calls(), 2)
}

// TestPipelineFakesScriptAFailure keys a stage failure to its identity (the
// transport-failure surface the orchestrator degrades on), and keeps the call
// recorded.
func TestPipelineFakesScriptAFailure(t *testing.T) {
	fakes := testkit.NewPipelineFakes()
	boom := errors.New("model transport unavailable")
	// The verification double keys by the patch's doc path.
	docPath := schemas.DocPatchExample.DocPath
	fakes.Verification.OnError(docPath, boom)

	_, err := fakes.Verification.Verify(orchestrator.VerificationStageInput{
		Patch: schemas.DocPatchExample,
	})
	require.ErrorIs(t, err, boom)

	// The failed call is still observable, keyed by its identity.
	require.Len(t, fakes.Verification.CallsFor(docPath), 1)
	assert.Len(t, fakes.Verification.Calls(), 1)
}

// TestPipelineFakesEarlySignalRecordsInOrder ports the observe-only early
// signal sink.
func TestPipelineFakesEarlySignalRecordsInOrder(t *testing.T) {
	fakes := testkit.NewPipelineFakes()
	confidence := 0.9
	require.NoError(t, fakes.EarlySignal.Post(orchestrator.EarlySignalPayload{
		RunID:           "run_1",
		Key:             schemas.RunKey{Repo: "acme/payments", BaseSHA: "8a73f2c", HeadSHA: "81af92e", ManifestVersion: 1, PolicyVersion: 17},
		DecisionSummary: orchestrator.DecisionSummary{Routing: orchestrator.RoutingContinue, Confidence: &confidence, CandidateCount: 1},
		At:              testkit.PipelineT0,
	}))
	require.NoError(t, fakes.EarlySignal.Update(orchestrator.EarlySignalPayload{
		RunID: "run_1",
		At:    testkit.PipelineT0,
		Final: true,
	}))
	payloads := fakes.EarlySignal.Payloads()
	require.Len(t, payloads, 2)
	assert.Equal(t, orchestrator.RoutingContinue, payloads[0].DecisionSummary.Routing)
	assert.Equal(t, 1, payloads[0].DecisionSummary.CandidateCount)
	assert.True(t, payloads[1].Final)
	assert.Equal(t, testkit.PipelineT0, payloads[1].At)

	fakes.ResetCalls()
	assert.Empty(t, fakes.EarlySignal.Payloads())
	assert.Empty(t, fakes.Writer.Calls())
}

/* ========================== fixtures: payments repo ======================== */

// TestWritePaymentsFixtureTreeMaterializesTheFixture ports the
// temp-repo materialization case: the fixture tree lands under the caller's
// root with the expected files.
func TestWritePaymentsFixtureTreeMaterializesTheFixture(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.WritePaymentsFixtureTree(root))

	files, err := testkit.ListRepoFiles(root)
	require.NoError(t, err)
	assert.Equal(t, []string{
		".docsync/manifest.yml",
		"docs/payments.md",
		"src/api/payments/client.ts",
		"src/payments/refund.ts",
		"src/payments/types.ts",
	}, files)

	content, err := testkit.ReadRepoFile(root, testkit.PaymentsDoc)
	require.NoError(t, err)
	assert.Contains(t, content, "Refund requests require an `idempotencyKey`.")
	require.NoError(t, testkit.AssertPaymentsAnchors(root))
}

// TestPaymentsFixtureBlobShasAreRealGitBlobShas pins that the fixture's claim
// provenance carries the REAL git content identity (not a made-up hash): the
// blob sha is sha1("blob <len>\0" + content), the git algorithm.
func TestPaymentsFixtureBlobShasAreRealGitBlobShas(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.WritePaymentsFixtureTree(root))
	require.NoError(t, testkit.WritePaymentsConceptsFile(root))

	concepts, err := testkit.ReadRepoFile(root, ".docsync/concepts.yml")
	require.NoError(t, err)
	blobShas := regexp.MustCompile(`"blob_sha":\s*"([0-9a-f]{40})"`).FindAllStringSubmatch(concepts, -1)
	require.Len(t, blobShas, 2)
	refundSHA, clientSHA := blobShas[0][1], blobShas[1][1]
	assert.Regexp(t, `^[0-9a-f]{40}$`, refundSHA)
	assert.Regexp(t, `^[0-9a-f]{40}$`, clientSHA)

	refundSource, err := testkit.ReadRepoFile(root, "src/payments/refund.ts")
	require.NoError(t, err)
	clientSource, err := testkit.ReadRepoFile(root, "src/api/payments/client.ts")
	require.NoError(t, err)
	assert.Equal(t, gitBlobSHA(refundSource), refundSHA)
	assert.Equal(t, gitBlobSHA(clientSource), clientSHA)
}

// TestPaymentsFixtureAnchorsMatchTheConceptsFile ports the fail-loud drift
// guard: the anchor lines the concepts claim are verified at materialization.
func TestPaymentsFixtureAnchorsMatchTheConceptsFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.WritePaymentsFixtureTree(root))
	require.NoError(t, testkit.AssertPaymentsAnchors(root))

	// Drifting the document breaks the anchors loudly.
	require.NoError(t, testkit.WriteRepoFile(root, testkit.PaymentsDoc, "# drifted\n"))
	err := testkit.AssertPaymentsAnchors(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 8")
}

// TestBuildPaymentsPipelineComposesGraphAndSeedsState ports the end-to-end
// fixture build over a caller-owned git repo: git init → fixture materialize →
// commit → intake the two hand-anchored claims → seed the VERIFIED doc and its
// baseline.
func TestBuildPaymentsPipelineComposesGraphAndSeedsState(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))

	pipeline, err := testkit.BuildPaymentsPipeline(root)
	require.NoError(t, err)

	intake, err := pipeline.IntakeHandAnchoredClaims()
	require.NoError(t, err)
	require.Equal(t, 2, intake.Created)
	require.Empty(t, intake.Errors)

	initialCommit, err := pipeline.CommitAll("chore(payments): initial fixture")
	require.NoError(t, err)
	require.Len(t, initialCommit, 40)

	// Two claims, both scoped by the fixture run id.
	created, err := pipeline.Claims.Get(testkit.ClaimIdempotency)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, testkit.ClaimIdempotency, created.ID)
	assert.Equal(t, "docs/payments.md", created.Anchor.File)
	assert.Equal(t, 8, created.Anchor.StartLine)

	retry, err := pipeline.Claims.Get(testkit.ClaimRetryWindow)
	require.NoError(t, err)
	require.NotNil(t, retry)
	assert.Equal(t, 12, retry.Anchor.StartLine)

	// The graph resolved the manifest mapping (no configuration errors) and the
	// mapping's sources are the manifest globs VERBATIM.
	mapping := graph.ForwardLookup(pipeline.Graph, testkit.PaymentsDoc)
	require.NotNil(t, mapping)
	assert.Equal(t, []string{"src/payments/**", "src/api/payments/**"}, mapping.Sources)

	// Seed the VERIFIED doc + its baseline against the initial commit.
	require.NoError(t, pipeline.SeedVerifiedDoc(initialCommit))

	machine := statemachine.NewDocStateMachine(pipeline.Store)
	docState, err := machine.GetState(testkit.PaymentsDoc)
	require.NoError(t, err)
	require.NotNil(t, docState)
	assert.Equal(t, schemas.DocStateVerified, docState.State)
	assert.Equal(t, testkit.PipelineInitRun, docState.Transitions[0].RunID)
	assert.Equal(t, testkit.PipelineT0, docState.Transitions[0].At)

	baseline, err := baselines.NewBaselineStore(pipeline.Store).Get(testkit.PaymentsDoc)
	require.NoError(t, err)
	require.NotNil(t, baseline)
	assert.Equal(t, []string{testkit.ClaimIdempotency, testkit.ClaimRetryWindow}, baseline.ClaimIDs)
	assert.Equal(t, testkit.PipelineT0, baseline.VerifiedAt)
	assert.Equal(t, initialCommit, baseline.BaselineCommit)
	assert.Equal(t, []string{
		"src/api/payments/client.ts",
		"src/payments/refund.ts",
		"src/payments/types.ts",
	}, baseline.SourceFiles, "source_files are the literal files the manifest globs resolve to")

	// The state files themselves carry canonical bytes.
	relPath, err := state.BaselineRelPath(testkit.PaymentsDoc)
	require.NoError(t, err)
	baselineRead, err := pipeline.Store.ReadBaseline(testkit.PaymentsDoc)
	require.NoError(t, err)
	require.True(t, baselineRead.Exists)
	assert.Equal(t, docutil.ContentHash(baselineRead.Raw), baselineRead.Hash)
	assert.Contains(t, baselineRead.Raw, "\"doc_path\":\""+testkit.PaymentsDoc+"\"")
	_, statErr := os.Stat(filepath.Join(pipeline.Root, schemas.DocSyncDir, relPath))
	require.NoError(t, statErr, "the real store persisted the canonical bytes on disk")
}

// TestPaymentsPipelineInputAndPortsWiring ports the fixture's orchestration
// seam: the input carries the fixture manifest, root and FIXED clock, and the
// port bundle exposes the doubles.
func TestPaymentsPipelineInputAndPortsWiring(t *testing.T) {
	root := t.TempDir()
	pipeline, err := testkit.BuildPaymentsPipeline(root)
	require.NoError(t, err)

	input := pipeline.PipelineInput("base_sha", "head_sha")
	assert.Equal(t, testkit.PaymentsRepoID, input.Repo)
	assert.Equal(t, root, input.RepoRoot)
	assert.Equal(t, "base_sha", input.Base)
	assert.Equal(t, "head_sha", input.Head)
	assert.Equal(t, testkit.PipelineT0, input.Clock())
	require.NotEmpty(t, input.Manifest.Documents)
	assert.Equal(t, testkit.PaymentsDoc, input.Manifest.Documents[0].Path)

	fakes := testkit.NewPipelineFakes()
	ports := fakes.Ports()
	assert.Equal(t, fakes.Decision, ports.Decision)
	assert.Equal(t, fakes.Writer, ports.Writer)
	assert.Equal(t, fakes.Verification, ports.Verification)
	assert.Equal(t, fakes.Policy, ports.Policy)
	assert.Equal(t, fakes.EarlySignal, ports.EarlySignal)
}

// TestRunPipelineOverThePaymentsFixtureWithKeyedFakes is the end-to-end
// composition the fixture builder exists for: a REAL git repo (caller-owned),
// the real analyzer lineup, and every LLM stage answered by the keyed
// model-stage doubles the port bundle is wired with.
func TestRunPipelineOverThePaymentsFixtureWithKeyedFakes(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))

	pipeline, err := testkit.BuildPaymentsPipeline(root)
	require.NoError(t, err)
	_, err = pipeline.IntakeHandAnchoredClaims()
	require.NoError(t, err)

	base, err := pipeline.CommitAll("chore(payments): initial fixture")
	require.NoError(t, err)

	// A source change inside a mapped source file → docs/payments.md becomes a
	// SUSPECT candidate (the pipeline's own routing, not a scripted answer).
	require.NoError(t, pipeline.Write("src/payments/refund.ts",
		"// touched fixture source (the end-to-end change window)\nexport const refundTouched = true;\n"))
	head, err := pipeline.CommitPaths([]string{"src/payments/refund.ts"}, "fix(payments): touch refunds")
	require.NoError(t, err)

	fakes := testkit.NewPipelineFakes()
	runID := testkit.PaymentsRepoID + "#" + base + ".." + head

	// JEV investigates (routing CONTINUE) → the writer stage runs.
	decision := schemas.DecisionExample
	decision.Action = schemas.ActionInvestigate
	decision.Candidates = []string{testkit.PaymentsDoc}
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{Decision: &decision})
	// The writer declines to patch: a noop result, so there is nothing for the
	// policy stage to resolve and the double below is never invoked.
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{
		Kind: orchestrator.WriterResultNoop, Detail: "fixture noop",
	})

	outcome, err := pipeline.RunPipeline(context.Background(), fakes, base, head)
	require.NoError(t, err)
	require.NotNil(t, outcome)
	assert.Equal(t, runID, outcome.RunID)
	assert.Equal(t, schemas.RunStatusCompleted, outcome.Status)
	assert.Equal(t, testkit.PipelineT0, *outcome.StartedAt)
	assert.Equal(t, testkit.PipelineT0, *outcome.FinishedAt)

	// The decision double saw exactly one call, keyed by the run id.
	require.Len(t, fakes.Decision.CallsFor(runID), 1)
	assert.Len(t, fakes.Decision.Calls(), 1)

	// The writer double was keyed by DOC PATH (the phase-6 routing precedent):
	// one call for the routed candidate, nothing for any other identity.
	require.Len(t, fakes.Writer.CallsFor(testkit.PaymentsDoc), 1)
	assert.Empty(t, fakes.Writer.CallsFor("docs/other.md"))

	// A noop patch leaves nothing to resolve, so the policy double is never
	// invoked (and its loud miss therefore never fires).
	assert.Empty(t, fakes.Policy.Calls())
	require.Len(t, outcome.Documents, 1)
	assert.Equal(t, testkit.PaymentsDoc, outcome.Documents[0].DocPath)
	assert.Equal(t, orchestrator.RoutingContinue, *outcome.Documents[0].Routing)
	assert.Equal(t, schemas.PipelineOutcomeIgnore, outcome.Documents[0].PolicyOutcome)

	// The run persisted its record through the real CAS store over the
	// fixture's state directory (one created file, no updates).
	relPath, err := state.RunRecordRelPath(outcome.Key)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, schemas.DocSyncDir, relPath))
	require.NoError(t, err)
	assert.Equal(t, 1, outcome.StateChurn.Added)
	assert.Equal(t, 0, outcome.StateChurn.Updated)

	// The early-signal sink recorded the post-decision snapshot and its final
	// update, both stamped by the FIXED fixture clock.
	payloads := fakes.EarlySignal.Payloads()
	require.Len(t, payloads, 2)
	assert.Equal(t, testkit.PipelineT0, payloads[0].At)
	assert.True(t, payloads[1].Final)
	assert.Equal(t, runID, payloads[0].RunID)
}

// TestPaymentsFixtureIntakeRefusesOutOfRangeAnchors pins the intake fail-closed
// ordering: an anchor line outside the document is a typed intake error, so no
// half-written claim is left behind.
func TestPaymentsFixtureIntakeRefusesOutOfRangeAnchors(t *testing.T) {
	root := t.TempDir()
	pipeline, err := testkit.BuildPaymentsPipeline(root)
	require.NoError(t, err)

	// Shrink the document so the claim anchors (lines 8 and 12) fall outside it.
	require.NoError(t, pipeline.Write(testkit.PaymentsDoc, "# Payments\n\nToo short.\n"))
	report, err := pipeline.IntakeHandAnchoredClaims()
	require.NoError(t, err)
	assert.Zero(t, report.Created)
	require.Len(t, report.Errors, 2)
	for _, intakeError := range report.Errors {
		assert.Equal(t, claims.IntakeErrLinesOutOfRange, intakeError.Kind)
		assert.Contains(t, intakeError.Message, "outside")
	}

	// Nothing was written: the store stays empty.
	claims_, err := pipeline.Claims.List()
	require.NoError(t, err)
	assert.Empty(t, claims_)
}

// TestWritePaymentsConceptsFileRequiresTheTree ports the fail-closed
// precondition: concepts cannot be written before the sources exist (their
// blob shas are the provenance).
func TestWritePaymentsConceptsFileRequiresTheTree(t *testing.T) {
	root := t.TempDir()
	err := testkit.WritePaymentsConceptsFile(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fixture tree missing")
}

/* ============================ fixtures: git plumbing ======================= */

// TestGitCommitPathsDoesNotSweepState ports the commit discipline: the initial
// commit sweeps the tree, and after state exists, a targeted commit touches
// ONLY the named paths — untracked `.docsync/state/` never enters it.
func TestGitCommitPathsDoesNotSweepState(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))

	pipeline, err := testkit.BuildPaymentsPipeline(root)
	require.NoError(t, err)
	initialCommit, err := pipeline.CommitAll("chore(payments): initial fixture")
	require.NoError(t, err)
	require.NoError(t, pipeline.SeedVerifiedDoc(initialCommit))

	// State now exists under .docsync/state and is untracked.
	tracked, err := testkit.GitTrackedPaths(root)
	require.NoError(t, err)
	assert.NotContains(t, tracked, ".docsync/state/docs/payments.md")

	drifted := strings.ReplaceAll(testkit.PaymentsDocContent, "## Refunds", "## Refunds (v2)")
	require.NoError(t, pipeline.Write(testkit.PaymentsDoc, drifted))
	secondCommit, err := pipeline.CommitPaths([]string{testkit.PaymentsDoc}, "docs(payments): refine refunds")
	require.NoError(t, err)
	assert.NotEqual(t, initialCommit, secondCommit)

	// The targeted commit changed exactly one path, and the state file is
	// still untracked on disk.
	names, err := gitCommittedPaths(t, root, initialCommit, secondCommit)
	require.NoError(t, err)
	assert.Equal(t, []string{testkit.PaymentsDoc}, names)

	untrackedState, err := state.DocStateRelPath(testkit.PaymentsDoc)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, schemas.DocSyncDir, untrackedState))
	require.NoError(t, err)
}

// gitCommittedPaths lists the repo-relative paths one commit changed (git
// diff-tree), via the caller-owned git binary.
func gitCommittedPaths(t *testing.T, root, base, head string) ([]string, error) {
	t.Helper()
	cmd := testkit.GitCommand(root, "diff-tree", "--no-commit-id", "--name-only", "-r", base, head)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff-tree failed: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return []string{}, nil
	}
	return strings.Split(text, "\n"), nil
}

// TestGitBlobShaMatchesTheGitBinary ports the blob-identity parity check (the
// helper and the raw git binary agree).
func TestGitBlobShaMatchesTheGitBinary(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	require.NoError(t, testkit.WritePaymentsFixtureTree(root))

	fromKit, err := testkit.GitBlobSHA(root, "src/payments/refund.ts")
	require.NoError(t, err)

	cmd := testkit.GitCommand(root, "hash-object", "src/payments/refund.ts")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(out)), fromKit)
}

// TestListRepoFilesIsDeterministicAndPrefixed ports listRepoFiles: sorted
// POSIX paths, prefix filtering, and fail-closed prefix errors.
func TestListRepoFilesIsDeterministicAndPrefixed(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.WritePaymentsFixtureTree(root))

	first, err := testkit.ListRepoFiles(root)
	require.NoError(t, err)
	second, err := testkit.ListRepoFiles(root)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.True(t, sort.StringsAreSorted(first))

	sources, err := testkit.ListRepoFiles(root, "src/")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"src/api/payments/client.ts",
		"src/payments/refund.ts",
		"src/payments/types.ts",
	}, sources)

	_, err = testkit.ListRepoFiles(root, "nope/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	entries, err := testkit.ListRepoEntries(root, testkit.ListRepoOptions{IncludeDirs: true, Prefix: "src"})
	require.NoError(t, err)
	kinds := map[string]string{}
	for _, entry := range entries {
		kinds[entry.Path] = entry.Kind
	}
	assert.Equal(t, testkit.WalkedDir, kinds["src/api"])
	assert.Equal(t, testkit.WalkedDir, kinds["src/payments"])
	assert.Equal(t, testkit.WalkedFile, kinds["src/payments/refund.ts"])
}

// TestRepoPathRejectsTraversal ports repoPath: `/`, `..` escapes and backslash
// drive prefixes are refused before any path is constructed.
func TestRepoPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, escapePath := range []string{"../outside.txt", "..", "/etc/passwd", "src/../../x"} {
		_, err := testkit.RepoPath(root, escapePath)
		require.Error(t, err, "expected %q to be refused", escapePath)
	}
	inside, err := testkit.RepoPath(root, "docs/payments.md")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "docs/payments.md"), inside)
}

/* ========================= fixtures: monorepo-scale ======================== */

// TestBuildMonorepoPipelineMaterializesTheScale ports the V-P1-4 scale
// material: 8 packages × 10 areas × 5 files = 400 sources + 8 mapped docs.
func TestBuildMonorepoPipelineMaterializesTheScale(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))

	pipeline, err := testkit.BuildMonorepoPipeline(root)
	require.NoError(t, err)
	assert.Equal(t, 8, pipeline.PackageCount)
	assert.Equal(t, 10, pipeline.AreaCount)
	assert.Equal(t, 5, pipeline.FileCount)
	assert.Empty(t, pipeline.Graph.Errors)
	require.Len(t, pipeline.Graph.Mappings, 8)
	require.Len(t, pipeline.Manifest.Documents, 8)

	// 400 sources + 8 docs + the manifest.
	files, err := pipeline.Files()
	require.NoError(t, err)
	assert.Len(t, files, 400+8+1)
	sources, err := pipeline.Files("packages/a/src")
	require.NoError(t, err)
	assert.Len(t, sources, 50)

	initialCommit, err := pipeline.CommitAll("chore: initial fixture")
	require.NoError(t, err)
	require.NoError(t, pipeline.SeedVerifiedDoc("docs/packages/a.md", initialCommit))

	docState, err := statemachine.NewDocStateMachine(pipeline.Store).GetState("docs/packages/a.md")
	require.NoError(t, err)
	require.NotNil(t, docState)
	assert.Equal(t, schemas.DocStateVerified, docState.State)

	baseline, err := baselines.NewBaselineStore(pipeline.Store).Get("docs/packages/a.md")
	require.NoError(t, err)
	require.NotNil(t, baseline)
	assert.Len(t, baseline.SourceFiles, 50)
	assert.Equal(t, initialCommit, baseline.BaselineCommit)

	// A doc path outside the fixture shape is refused.
	require.Error(t, pipeline.SeedVerifiedDoc("docs/other.md", initialCommit))

	// The monorepo pipeline input keeps the fixed clock.
	input := pipeline.PipelineInput("base_sha", "head_sha")
	assert.Equal(t, testkit.MonorepoRepoID, input.Repo)
	assert.Equal(t, testkit.PipelineT0, input.Clock())
}

// TestBuildMonorepoPipelineRejectsNonsenseDimensions ports the dimension guard.
func TestBuildMonorepoPipelineRejectsNonsenseDimensions(t *testing.T) {
	root := t.TempDir()
	_, err := testkit.BuildMonorepoPipelineSized(root, 0, 10, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}
