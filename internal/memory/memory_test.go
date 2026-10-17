package memory_test

// Ported from packages/core/test/memory.test.ts (P10-T20..T21, V-P10-14) —
// D1 exactly-four layout (extra files reported, never deleted), front-matter
// round-trip, R12 contradiction decay (failures.md + confidence decay with
// floor), the unverifiable-near-floor rule, and the INV-7 pure feed shapes.
// Black-box: only the exported API is exercised.
//
// Determinism: the clock is fixed (timestamps are always caller-supplied, D6)
// and record ids come from an injected deterministic ULID generator, so every
// assertion below is reproducible (CONVENTIONS.md: inject clocks/ULID
// generators, never read the wall clock inside logic under test).

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/memory"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixedNow = "2026-10-08T12:00:00.000Z"

func fixedClock() string { return fixedNow }

// deterministicULIDs is the injected id mint: fixed clock + deterministic
// byte source (the TS suite's module-level generator, made deterministic).
func deterministicULIDs() *docutil.ULIDGenerator {
	return docutil.NewULIDGeneratorWith(
		func() int64 { return 1_760_000_000_000 },
		func(n int) ([]byte, error) {
			out := make([]byte, n)
			for i := range out {
				out[i] = byte(i + 1)
			}
			return out, nil
		},
	)
}

// newTestStore builds a store over a fresh temp memory dir with the D1
// layout enforced (TS tempMemoryDir + createMemoryStore).
func newTestStore(t *testing.T) (*memory.MemoryStore, string) {
	t.Helper()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	store, err := memory.NewMemoryStore(memory.MemoryStoreConfig{
		MemoryDir: memoryDir,
		Clock:     fixedClock,
		ULIDs:     deterministicULIDs(),
	})
	require.NoError(t, err)
	return store, memoryDir
}

// patternInput mirrors the TS test's patternInput factory.
func patternInput() memory.MemoryRecordInput {
	return memory.MemoryRecordInput{
		File:         schemas.MemoryFileKnownPatterns,
		PatternName:  "generated-sdk-docs-follow-api-changes",
		EvidencePRs:  []string{"acme/widgets#481", "acme/widgets#502"},
		Observation:  "generated SDK docs drift whenever the API client changes",
		Action:       "route generated-sdk/** doc claims through the generated-docs lane",
		Confidence:   0.72,
		Unverifiable: false,
	}
}

func sortedNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func TestV_P10_14_MemoryLayoutDecayAndNearFloorUnverifiable(t *testing.T) {
	store, memoryDir := newTestStore(t)

	// Exactly the D1 four files exist after enforcement.
	layout, err := memory.EnforceMemoryLayout(memoryDir)
	require.NoError(t, err)
	assert.True(t, layout.OK)
	assert.Empty(t, layout.Missing)
	assert.Empty(t, layout.Extra)
	expectedNames := append([]string(nil), memory.MemoryFiles...)
	sort.Strings(expectedNames)
	assert.Equal(t, expectedNames, sortedNames(t, memoryDir))

	// The pattern record.
	pattern, err := store.AppendRecord(patternInput())
	require.NoError(t, err)
	patterns, err := store.ParseFile(schemas.MemoryFileKnownPatterns)
	require.NoError(t, err)
	require.Len(t, patterns, 1)
	assert.Equal(t, 0.72, patterns[0].Confidence)

	// A synthetic contradiction: lands in failures.md (with contradiction_of)
	// AND decays the pattern's confidence by the decay factor.
	result, err := store.Contradict(memory.ContradictionInput{
		RecordID:    pattern.RecordID,
		Observation: "the generated-docs lane misfired on API renames (PR audit)",
		Evidence:    []string{"acme/widgets#900"},
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.MemoryFileKnownPatterns, result.ContradictedFile)
	assert.Equal(t, 0.72, result.PreviousConfidence)
	assert.Equal(t, 0.72*memory.MemoryDecayFactor, result.DecayedConfidence)

	failures, err := store.ParseFile(schemas.MemoryFileFailures)
	require.NoError(t, err)
	require.Len(t, failures, 1)
	assert.Equal(t, schemas.MemoryFileFailures, failures[0].File)
	require.NotNil(t, failures[0].ContradictionOf)
	assert.Equal(t, pattern.RecordID, *failures[0].ContradictionOf)
	// The failure record is its OWN record (fresh id), not a copy of the
	// source — contradiction_of is a link, not an identity reuse.
	assert.NotEqual(t, pattern.RecordID, failures[0].RecordID)
	assert.NotEmpty(t, failures[0].RecordID)
	assert.Equal(t, pattern.PatternName, failures[0].PatternName)
	assert.Equal(t, []string{"acme/widgets#900"}, failures[0].EvidencePRs)
	failuresRaw, err := os.ReadFile(filepath.Join(memoryDir, memory.MemoryFileName(schemas.MemoryFileFailures)))
	require.NoError(t, err)
	assert.Contains(t, string(failuresRaw), pattern.RecordID)

	// Decay visible in the pattern's source file.
	decayedPatterns, err := store.ParseFile(schemas.MemoryFileKnownPatterns)
	require.NoError(t, err)
	require.Len(t, decayedPatterns, 1)
	assert.Equal(t, 0.72*memory.MemoryDecayFactor, decayedPatterns[0].Confidence)

	// Decay floors at MemoryConfidenceFloor, never below (R12).
	weakInput := patternInput()
	weakInput.PatternName = "weak-belief"
	weakInput.EvidencePRs = []string{"acme/widgets#1"}
	weakInput.Observation = "weak belief"
	weakInput.Action = "watch it"
	weakInput.Confidence = 0.08
	weak, err := store.AppendRecord(weakInput)
	require.NoError(t, err)
	weakResult, err := store.Contradict(memory.ContradictionInput{
		RecordID:    weak.RecordID,
		Observation: "refuted by the PR audit",
		Evidence:    []string{"acme/widgets#2"},
	})
	require.NoError(t, err)
	assert.Equal(t, memory.MemoryConfidenceFloor, weakResult.DecayedConfidence)

	// An unverifiable record is stored near floor confidence (schema allows
	// at most 0.3; the anecdote-hypothesis rule, ARCH §8.8).
	anecdoteInput := patternInput()
	anecdoteInput.File = schemas.MemoryFileExceptions
	anecdoteInput.PatternName = "suspected-ci-doc-drift"
	anecdoteInput.EvidencePRs = []string{"acme/widgets#777"}
	anecdoteInput.Observation = "CI artifacts occasionally show doc drift with no source change"
	anecdoteInput.Action = "observe only — hypothesis"
	anecdoteInput.Confidence = memory.MemoryConfidenceFloor
	anecdoteInput.Unverifiable = true
	anecdote, err := store.AppendRecord(anecdoteInput)
	require.NoError(t, err)
	exceptions, err := store.ParseFile(schemas.MemoryFileExceptions)
	require.NoError(t, err)
	require.Len(t, exceptions, 1)
	assert.True(t, exceptions[0].Unverifiable)
	assert.Equal(t, memory.MemoryConfidenceFloor, exceptions[0].Confidence)
	assert.Equal(t, memory.MemoryConfidenceFloor, anecdote.Confidence)

	// Fail-closed: an unverifiable record ABOVE 0.3 is refused (superRefine).
	tooConfident := patternInput()
	tooConfident.File = schemas.MemoryFileExceptions
	tooConfident.PatternName = "too-confident-anecdote"
	tooConfident.Confidence = 0.9
	tooConfident.Unverifiable = true
	_, err = store.AppendRecord(tooConfident)
	require.Error(t, err)
	var recordErr *memory.MemoryRecordError
	assert.ErrorAs(t, err, &recordErr)
	assert.Equal(t, memory.EDOCSYNC_MEMORY_RECORD, docutil.CodeOf(err))
	// Nothing was written for the refused record.
	exceptions, err = store.ParseFile(schemas.MemoryFileExceptions)
	require.NoError(t, err)
	assert.Len(t, exceptions, 1)
}

func TestMemoryLayoutReportsExtraFilesAndNeverDeletesThem(t *testing.T) {
	_, memoryDir := newTestStore(t)
	require.NoError(t, os.WriteFile(filepath.Join(memoryDir, "stray-notes.md"), []byte("human content\n"), 0o644))

	report, err := memory.EnforceMemoryLayout(memoryDir)
	require.NoError(t, err)
	assert.False(t, report.OK)
	assert.Equal(t, []string{"stray-notes.md"}, report.Extra)
	assert.Empty(t, report.Missing)
	// Extra files stay on disk, byte-identical (report, never delete).
	stray, err := os.ReadFile(filepath.Join(memoryDir, "stray-notes.md"))
	require.NoError(t, err)
	assert.Equal(t, "human content\n", string(stray))

	// The store refuses to operate while an extra file is reported (fail closed).
	_, err = memory.NewMemoryStore(memory.MemoryStoreConfig{MemoryDir: memoryDir, Clock: fixedClock})
	require.Error(t, err)
	var layoutErr *memory.MemoryLayoutError
	assert.ErrorAs(t, err, &layoutErr)
	assert.Equal(t, memory.EDOCSYNC_MEMORY_LAYOUT, docutil.CodeOf(err))
	assert.Contains(t, layoutErr.Message(), "stray-notes.md")
}

func TestMemoryFrontMatterRoundTripIsByteDeterministic(t *testing.T) {
	store, memoryDir := newTestStore(t)

	first, err := store.AppendRecord(patternInput())
	require.NoError(t, err)
	secondInput := patternInput()
	secondInput.PatternName = "multi-line-prose"
	secondInput.EvidencePRs = []string{"acme/widgets#42"}
	secondInput.Observation = "line one: with colons, quotes \"and\" — dashes\nline two follows"
	secondInput.Action = "keep the doc in sync: every change, always"
	secondInput.Confidence = 0.36
	secondInput.ContradictionOf = &first.RecordID
	second, err := store.AppendRecord(secondInput)
	require.NoError(t, err)

	// Round-trip: the parsed records deep-equal the appended inputs.
	parsed, err := store.ParseFile(schemas.MemoryFileKnownPatterns)
	require.NoError(t, err)
	assert.Equal(t, []schemas.MemoryRecord{*first, *second}, parsed)

	// Serialization is deterministic (decay rewrites depend on this).
	firstBlock, err := memory.SerializeMemoryRecord(first)
	require.NoError(t, err)
	firstBlockAgain, err := memory.SerializeMemoryRecord(first)
	require.NoError(t, err)
	assert.Equal(t, firstBlock, firstBlockAgain)

	// The prose projection renders observation + action after the fence.
	raw, err := os.ReadFile(filepath.Join(memoryDir, memory.MemoryFileName(schemas.MemoryFileKnownPatterns)))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "---\n")
	assert.Contains(t, string(raw), "line two follows")
	assert.Contains(t, string(raw), "every change, always")

	// Fail-closed: prose that would break the fence framing is refused.
	_, err = memory.SerializeMemoryRecord(&schemas.MemoryRecord{
		Schema:      second.Schema,
		RecordID:    second.RecordID,
		File:        second.File,
		PatternName: second.PatternName,
		EvidencePRs: second.EvidencePRs,
		Confidence:  second.Confidence,
		CreatedAt:   second.CreatedAt,
		Observation: "bad\n---\nframing",
		Action:      second.Action,
	})
	require.Error(t, err)
	var recordErr *memory.MemoryRecordError
	assert.ErrorAs(t, err, &recordErr)

	// Fail-closed: a schema-violating block is a typed error, never skipped.
	require.NoError(t, os.WriteFile(
		filepath.Join(memoryDir, memory.MemoryFileName(schemas.MemoryFileDecisions)),
		[]byte("---\nfile: decisions\n---\nbroken\n\n"), 0o644))
	_, err = store.ParseFile(schemas.MemoryFileDecisions)
	require.Error(t, err)
	var blockErr *memory.MemoryRecordError
	assert.ErrorAs(t, err, &blockErr)

	// Fail-closed: an unterminated front-matter block.
	_, err = memory.ParseMemoryRecords("---\nno closing fence\n")
	require.Error(t, err)
	var unterminatedErr *memory.MemoryRecordError
	assert.ErrorAs(t, err, &unterminatedErr)
}

func TestMemoryContradictRefusesUnknownRecordID(t *testing.T) {
	store, _ := newTestStore(t)
	_, err := store.Contradict(memory.ContradictionInput{
		RecordID:    "01JZZZZZZZZZZZZZZZZZZZZZZZ",
		Observation: "x",
		Evidence:    []string{"p#1"},
	})
	require.Error(t, err)
	var recordErr *memory.MemoryRecordError
	assert.ErrorAs(t, err, &recordErr)
	// Nothing landed in failures.md (nothing written on refusal).
	failures, err := store.ParseFile(schemas.MemoryFileFailures)
	require.NoError(t, err)
	assert.Len(t, failures, 0)
}

func TestMemoryFeedShapes(t *testing.T) {
	store, _ := newTestStore(t)
	pattern, err := store.AppendRecord(patternInput())
	require.NoError(t, err)
	exceptionInput := patternInput()
	exceptionInput.File = schemas.MemoryFileExceptions
	exceptionInput.PatternName = "changelog-exempt"
	exceptionInput.Action = "never route CHANGELOG.md claims"
	exceptionInput.Confidence = 0.8
	_, err = store.AppendRecord(exceptionInput)
	require.NoError(t, err)
	_, err = store.Contradict(memory.ContradictionInput{
		RecordID:    pattern.RecordID,
		Observation: "misfire on API renames",
		Evidence:    []string{"acme/widgets#900"},
	})
	require.NoError(t, err)

	parsed, err := store.ParseAll()
	require.NoError(t, err)
	feeds := memory.ProjectMemoryFeeds(parsed)

	// jevContextRows: pattern_name + observation + confidence from known-patterns.
	assert.Equal(t, []memory.JevContextRow{
		{
			Pattern:     "generated-sdk-docs-follow-api-changes",
			Observation: "generated SDK docs drift whenever the API client changes",
			Confidence:  0.72 * memory.MemoryDecayFactor,
		},
	}, feeds.JevContextRows)

	// policyExceptions: pattern_name/action candidates from exceptions.
	assert.Equal(t, []memory.PolicyExceptionCandidate{
		{Pattern: "changelog-exempt", Action: "never route CHANGELOG.md claims"},
	}, feeds.PolicyExceptions)

	// negativeExamples: contradiction_of + observation from failures.
	require.Len(t, feeds.NegativeExamples, 1)
	require.NotNil(t, feeds.NegativeExamples[0].ContradictionOf)
	assert.Equal(t, pattern.RecordID, *feeds.NegativeExamples[0].ContradictionOf)
	assert.Equal(t, "misfire on API renames", feeds.NegativeExamples[0].Observation)

	// INV-7, asserted on the SHAPE (the TS test's Object.isFrozen contract):
	// the feed is plain data only — no functions, no fs/store/policy handles
	// anywhere in the payload, and mutating the returned rows cannot reach
	// back into the store's records (a pure projection has no write path).
	feeds.JevContextRows[0].Confidence = 0
	feeds.JevContextRows[0].Observation = "mutated"
	feeds.PolicyExceptions[0].Action = "mutated"
	if len(feeds.NegativeExamples) > 0 {
		feeds.NegativeExamples[0].Observation = "mutated"
	}
	reparsed, err := store.ParseAll()
	require.NoError(t, err)
	fresh := memory.ProjectMemoryFeeds(reparsed)
	assert.Equal(t, 0.72*memory.MemoryDecayFactor, fresh.JevContextRows[0].Confidence)
	assert.Equal(t, "generated SDK docs drift whenever the API client changes", fresh.JevContextRows[0].Observation)
	assert.Equal(t, "never route CHANGELOG.md claims", fresh.PolicyExceptions[0].Action)
	assert.Equal(t, "misfire on API renames", fresh.NegativeExamples[0].Observation)
}

/* ------------------------------------------------------------ golden fidelity */

func TestEmptyMemoryFileScaffoldGoldenBytes(t *testing.T) {
	// Golden: the TS scaffold template, captured from the reference module
	// (no `---` fence lines, so a fresh scaffold parses to zero records).
	golden := "# docsync memory — %s\n\n" +
		"Appended, machine-parseable front-matter records only (D1 exactly-four\n" +
		"layout; PLAN §11.6: Markdown with structured front-matter records).\n" +
		"Evidence fields are mandatory on every record (PRIN-18); contradictions\n" +
		"land in failures.md and decay the contradicted record (R12).\n"
	for _, file := range schemas.MemoryFileValues {
		assert.Equal(t, strings.Replace(golden, "%s", string(file), 1), memory.EmptyMemoryFileScaffold(file))
		// A scaffold holds no front-matter records.
		records, err := memory.ParseMemoryRecords(memory.EmptyMemoryFileScaffold(file))
		require.NoError(t, err)
		assert.Empty(t, records)
	}
}

func TestSerializeMemoryRecordGoldenYAMLBytes(t *testing.T) {
	for _, tc := range goldenYAMLCases {
		t.Run(tc.name, func(t *testing.T) {
			record := schemas.MemoryRecord{
				Schema:       schemas.MemoryRecordSchemaID,
				RecordID:     "01JZZ0000000000000000000AA",
				File:         schemas.MemoryFileKnownPatterns,
				PatternName:  "p",
				EvidencePRs:  []string{"acme/widgets#481"},
				Confidence:   tc.confidence,
				Unverifiable: false,
				CreatedAt:    fixedNow,
				Observation:  tc.observation,
				Action:       tc.action,
			}
			block, err := memory.SerializeMemoryRecord(&record)
			require.NoError(t, err)
			// Byte-for-byte through the Go serializer, equal to the bytes the
			// TS reference (yaml v2, lineWidth 0) emits for the same record.
			assert.Equal(t, tc.golden, block)
			// …and the block parses back to the same record (round-trip).
			parsed, err := memory.ParseMemoryRecords(block)
			require.NoError(t, err)
			require.Len(t, parsed, 1)
			assert.Equal(t, record, parsed[0])
		})
	}
}

func TestSerializeMemoryRecordGoldenExample(t *testing.T) {
	// The TS test's round-trip record, pinned byte-for-byte.
	contradictionOf := "01JZZ0000000000000000000AA"
	record := schemas.MemoryRecord{
		Schema:          schemas.MemoryRecordSchemaID,
		RecordID:        "01JZZ0000000000000000000AA",
		File:            schemas.MemoryFileKnownPatterns,
		PatternName:     "multi-line-prose",
		EvidencePRs:     []string{"acme/widgets#42"},
		Confidence:      0.36,
		Unverifiable:    false,
		ContradictionOf: &contradictionOf,
		CreatedAt:       fixedNow,
		Observation:     "line one: with colons, quotes \"and\" — dashes\nline two follows",
		Action:          "keep the doc in sync: every change, always",
	}
	block, err := memory.SerializeMemoryRecord(&record)
	require.NoError(t, err)
	const golden = "---\n" +
		"schema: docsync.memory-record/v1\n" +
		"record_id: 01JZZ0000000000000000000AA\n" +
		"file: known-patterns\n" +
		"pattern_name: multi-line-prose\n" +
		"evidence_prs:\n" +
		"  - acme/widgets#42\n" +
		"confidence: 0.36\n" +
		"unverifiable: false\n" +
		"contradiction_of: 01JZZ0000000000000000000AA\n" +
		"created_at: 2026-10-08T12:00:00.000Z\n" +
		"observation: |-\n" +
		"  line one: with colons, quotes \"and\" — dashes\n" +
		"  line two follows\n" +
		"action: \"keep the doc in sync: every change, always\"\n" +
		"---\n" +
		"line one: with colons, quotes \"and\" — dashes\n" +
		"line two follows\n" +
		"\n" +
		"keep the doc in sync: every change, always\n" +
		"\n"
	assert.Equal(t, golden, block)

	parsed, err := memory.ParseMemoryRecords(block)
	require.NoError(t, err)
	require.Len(t, parsed, 1)
	assert.Equal(t, record, parsed[0])
}

func TestParseMemoryRecordsRefusesUnparseableBlock(t *testing.T) {
	_, err := memory.ParseMemoryRecords("---\na: b:\n  - [\n---\n")
	require.Error(t, err)
	var recordErr *memory.MemoryRecordError
	assert.ErrorAs(t, err, &recordErr)
}

// goldenYAMLCases pins the front-matter bytes the TypeScript yaml
// package (eemeli/yaml v2, stringify(fields, { lineWidth: 0 })) emits for
// the record schema value domain — captured from the reference
// implementation and asserted byte-for-byte through SerializeMemoryRecord.
var goldenYAMLCases = []struct {
	name        string
	observation string
	action      string
	confidence  float64
	golden      string
}{
	{
		name:        "plain observation and action",
		observation: "generated SDK docs drift whenever the API client changes",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: generated SDK docs drift whenever the API client changes\naction: route it\n---\ngenerated SDK docs drift whenever the API client changes\n\nroute it\n\n",
	},
	{
		name:        "colon space in action",
		observation: "generated SDK docs drift whenever the API client changes",
		action:      "keep the doc in sync: every change, always",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: generated SDK docs drift whenever the API client changes\naction: \"keep the doc in sync: every change, always\"\n---\ngenerated SDK docs drift whenever the API client changes\n\nkeep the doc in sync: every change, always\n\n",
	},
	{
		name:        "multi-line prose",
		observation: "line one: with colons, quotes \"and\" — dashes\nline two follows",
		action:      "keep the doc in sync: every change, always",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |-\n  line one: with colons, quotes \"and\" — dashes\n  line two follows\naction: \"keep the doc in sync: every change, always\"\n---\nline one: with colons, quotes \"and\" — dashes\nline two follows\n\nkeep the doc in sync: every change, always\n\n",
	},
	{
		name:        "leading space",
		observation: " leading space",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \" leading space\"\naction: route it\n---\n leading space\n\nroute it\n\n",
	},
	{
		name:        "trailing space",
		observation: "trailing space ",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"trailing space \"\naction: route it\n---\ntrailing space \n\nroute it\n\n",
	},
	{
		name:        "tab inside",
		observation: "tab\tinside",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: tab\tinside\naction: route it\n---\ntab\tinside\n\nroute it\n\n",
	},
	{
		name:        "looks like bool",
		observation: "true",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"true\"\naction: route it\n---\ntrue\n\nroute it\n\n",
	},
	{
		name:        "looks like int",
		observation: "123",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"123\"\naction: route it\n---\n123\n\nroute it\n\n",
	},
	{
		name:        "looks like null",
		observation: "null",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"null\"\naction: route it\n---\nnull\n\nroute it\n\n",
	},
	{
		name:        "tilde null",
		observation: "~",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"~\"\naction: route it\n---\n~\n\nroute it\n\n",
	},
	{
		name:        "looks like hex",
		observation: "0x1f",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"0x1f\"\naction: route it\n---\n0x1f\n\nroute it\n\n",
	},
	{
		name:        "looks like exp float",
		observation: "1e5",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"1e5\"\naction: route it\n---\n1e5\n\nroute it\n\n",
	},
	{
		name:        "looks like inf",
		observation: ".inf",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \".inf\"\naction: route it\n---\n.inf\n\nroute it\n\n",
	},
	{
		name:        "yes is plain",
		observation: "yes",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: yes\naction: route it\n---\nyes\n\nroute it\n\n",
	},
	{
		name:        "time-like is plain",
		observation: "12:30",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: 12:30\naction: route it\n---\n12:30\n\nroute it\n\n",
	},
	{
		name:        "dash without space is plain",
		observation: "-dash",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: -dash\naction: route it\n---\n-dash\n\nroute it\n\n",
	},
	{
		name:        "colon without space is plain",
		observation: "a:b",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: a:b\naction: route it\n---\na:b\n\nroute it\n\n",
	},
	{
		name:        "hash not after space is plain",
		observation: "hash#inside",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: hash#inside\naction: route it\n---\nhash#inside\n\nroute it\n\n",
	},
	{
		name:        "single quote inside is plain",
		observation: "sq'inside",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: sq'inside\naction: route it\n---\nsq'inside\n\nroute it\n\n",
	},
	{
		name:        "double quote inside is plain",
		observation: "dq\"inside",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: dq\"inside\naction: route it\n---\ndq\"inside\n\nroute it\n\n",
	},
	{
		name:        "both quotes is plain",
		observation: "both'and\"",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: both'and\"\naction: route it\n---\nboth'and\"\n\nroute it\n\n",
	},
	{
		name:        "backslash is plain",
		observation: "back\\slash",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: back\\slash\naction: route it\n---\nback\\slash\n\nroute it\n\n",
	},
	{
		name:        "dash start",
		observation: "- dash-start",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"- dash-start\"\naction: route it\n---\n- dash-start\n\nroute it\n\n",
	},
	{
		name:        "question start",
		observation: "? question",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"? question\"\naction: route it\n---\n? question\n\nroute it\n\n",
	},
	{
		name:        "colon start",
		observation: ": colon-start",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \": colon-start\"\naction: route it\n---\n: colon-start\n\nroute it\n\n",
	},
	{
		name:        "hash start",
		observation: "#start",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"#start\"\naction: route it\n---\n#start\n\nroute it\n\n",
	},
	{
		name:        "space then hash",
		observation: "a #b",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"a #b\"\naction: route it\n---\na #b\n\nroute it\n\n",
	},
	{
		name:        "star start",
		observation: "* star",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"* star\"\naction: route it\n---\n* star\n\nroute it\n\n",
	},
	{
		name:        "amp start",
		observation: "& amp",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"& amp\"\naction: route it\n---\n& amp\n\nroute it\n\n",
	},
	{
		name:        "bang start",
		observation: "! bang",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"! bang\"\naction: route it\n---\n! bang\n\nroute it\n\n",
	},
	{
		name:        "pipe start",
		observation: "| pipe",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"| pipe\"\naction: route it\n---\n| pipe\n\nroute it\n\n",
	},
	{
		name:        "gt start",
		observation: "> gt",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"> gt\"\naction: route it\n---\n> gt\n\nroute it\n\n",
	},
	{
		name:        "percent start",
		observation: "% pct",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"% pct\"\naction: route it\n---\n% pct\n\nroute it\n\n",
	},
	{
		name:        "at start",
		observation: "@ at",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"@ at\"\naction: route it\n---\n@ at\n\nroute it\n\n",
	},
	{
		name:        "backtick start",
		observation: "` tick",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"` tick\"\naction: route it\n---\n` tick\n\nroute it\n\n",
	},
	{
		name:        "flow seq start",
		observation: "[a]",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"[a]\"\naction: route it\n---\n[a]\n\nroute it\n\n",
	},
	{
		name:        "flow map start",
		observation: "{a}",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"{a}\"\naction: route it\n---\n{a}\n\nroute it\n\n",
	},
	{
		name:        "comma start",
		observation: ", comma",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \", comma\"\naction: route it\n---\n, comma\n\nroute it\n\n",
	},
	{
		name:        "single quote start",
		observation: "' squote",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"' squote\"\naction: route it\n---\n' squote\n\nroute it\n\n",
	},
	{
		name:        "double quote start",
		observation: "\" dquote",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: '\" dquote'\naction: route it\n---\n\" dquote\n\nroute it\n\n",
	},
	{
		name:        "multi-line literal strip",
		observation: "multi\nline",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |-\n  multi\n  line\naction: route it\n---\nmulti\nline\n\nroute it\n\n",
	},
	{
		name:        "trailing newline clips",
		observation: "trail\n",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |\n  trail\naction: route it\n---\ntrail\n\n\nroute it\n\n",
	},
	{
		name:        "two trailing newlines keeps",
		observation: "trail\n\n",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |+\n  trail\n\naction: route it\n---\ntrail\n\n\n\nroute it\n\n",
	},
	{
		name:        "blank line inside",
		observation: "blank\n\nbetween",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |-\n  blank\n\n  between\naction: route it\n---\nblank\n\nbetween\n\nroute it\n\n",
	},
	{
		name:        "leading newline",
		observation: "\nleading-nl",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |-\n  \n  leading-nl\naction: route it\n---\n\nleading-nl\n\nroute it\n\n",
	},
	{
		name:        "leading spaces with indent indicator",
		observation: "  \n  starts with spaces then newline",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |2-\n    \n    starts with spaces then newline\naction: route it\n---\n  \n  starts with spaces then newline\n\nroute it\n\n",
	},
	{
		name:        "more-indented interior line",
		observation: "newline then spaces\n   next line",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |-\n  newline then spaces\n     next line\naction: route it\n---\nnewline then spaces\n   next line\n\nroute it\n\n",
	},
	{
		name:        "ends with newline plus space",
		observation: "ends with newline+space\n ",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"ends with newline+space\\n \"\naction: route it\n---\nends with newline+space\n \n\nroute it\n\n",
	},
	{
		name:        "carriage return",
		observation: "a\r\nb",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"a\\r\\nb\"\naction: route it\n---\na\r\nb\n\nroute it\n\n",
	},
	{
		name:        "control char",
		observation: "control\u0001char",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"control\\x01char\"\naction: route it\n---\ncontrol\u0001char\n\nroute it\n\n",
	},
	{
		name:        "long with crlf folding",
		observation: "long value with \r\n and enough length to pass forty chars easily yes",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"long value with \\r\n\n  \\ and enough length to pass forty chars easily yes\"\naction: route it\n---\nlong value with \r\n and enough length to pass forty chars easily yes\n\nroute it\n\n",
	},
	{
		name:        "unicode",
		observation: "unicode ünïcödé ✓",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: unicode ünïcödé ✓\naction: route it\n---\nunicode ünïcödé ✓\n\nroute it\n\n",
	},
	{
		name:        "emoji",
		observation: "emoji 🚀",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: emoji 🚀\naction: route it\n---\nemoji 🚀\n\nroute it\n\n",
	},
	{
		name:        "long single line",
		observation: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\naction: route it\n---\nxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n\nroute it\n\n",
	},
	{
		name:        "single space",
		observation: " ",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \" \"\naction: route it\n---\n \n\nroute it\n\n",
	},
	{
		name:        "single tab",
		observation: "\t",
		action:      "route it",
		confidence:  0.72,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"\\t\"\naction: route it\n---\n\t\n\nroute it\n\n",
	},
	{
		name:        "confidence one",
		observation: "generated SDK docs drift whenever the API client changes",
		action:      "route it",
		confidence:  1,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 1\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: generated SDK docs drift whenever the API client changes\naction: route it\n---\ngenerated SDK docs drift whenever the API client changes\n\nroute it\n\n",
	},
	{
		name:        "confidence floor",
		observation: "generated SDK docs drift whenever the API client changes",
		action:      "route it",
		confidence:  0.05,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.05\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: generated SDK docs drift whenever the API client changes\naction: route it\n---\ngenerated SDK docs drift whenever the API client changes\n\nroute it\n\n",
	},
	{
		name:        "confidence decayed",
		observation: "generated SDK docs drift whenever the API client changes",
		action:      "route it",
		confidence:  0.36,
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.36\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: generated SDK docs drift whenever the API client changes\naction: route it\n---\ngenerated SDK docs drift whenever the API client changes\n\nroute it\n\n",
	},
}

/* ---------------------------------------------------- edge-branch golden pins */

// goldenYAMLExtraCases pins the remaining emitter branches (JS number
// formatting for confidence, quote preference + fallbacks, and the
// double-quoted escape rewrites) against the same reference bytes.
var goldenYAMLExtraCases = []struct {
	name        string
	observation string
	action      string
	confidence  float64
	evidence    []string
	golden      string
}{
	{
		name:        "conf 0",
		observation: "obs",
		action:      "act",
		confidence:  0,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf 1",
		observation: "obs",
		action:      "act",
		confidence:  1,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 1\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf half",
		observation: "obs",
		action:      "act",
		confidence:  0.5,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.5\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf tenth",
		observation: "obs",
		action:      "act",
		confidence:  0.1,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.1\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf third",
		observation: "obs",
		action:      "act",
		confidence:  0.3333333333333333,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.3333333333333333\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf 1e-6",
		observation: "obs",
		action:      "act",
		confidence:  0.000001,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.000001\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf 1e-7",
		observation: "obs",
		action:      "act",
		confidence:  1e-7,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 1e-7\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf 1e21",
		observation: "obs",
		action:      "act",
		confidence:  1e+21,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 1e+21\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf 1.5e-10",
		observation: "obs",
		action:      "act",
		confidence:  1.5e-10,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 1.5e-10\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "conf float garbage",
		observation: "obs",
		action:      "act",
		confidence:  0.30000000000000004,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.30000000000000004\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "obs starts with double quote",
		observation: "\"dq\"",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: '\"dq\"'\naction: act\n---\n\"dq\"\n\nact\n\n",
	},
	{
		name:        "obs dash then double quote",
		observation: "- \"quoted\" start",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: '- \"quoted\" start'\naction: act\n---\n- \"quoted\" start\n\nact\n\n",
	},
	{
		name:        "obs both quote kinds",
		observation: "sq'and\"dq",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: sq'and\"dq\naction: act\n---\nsq'and\"dq\n\nact\n\n",
	},
	{
		name:        "obs double quote with newline and wrapper whitespace",
		observation: "\"dq\"\n\tx",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |-\n  \"dq\"\n  \tx\naction: act\n---\n\"dq\"\n\tx\n\nact\n\n",
	},
	{
		name:        "obs double quote multi-line long",
		observation: "start \"dq\" line\nsecond \"dq\" line padded beyond forty characters",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |-\n  start \"dq\" line\n  second \"dq\" line padded beyond forty characters\naction: act\n---\nstart \"dq\" line\nsecond \"dq\" line padded beyond forty characters\n\nact\n\n",
	},
	{
		name:        "obs single quote then newline long",
		observation: "sq's line\nsecond sq's line padded beyond forty characters",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: |-\n  sq's line\n  second sq's line padded beyond forty characters\naction: act\n---\nsq's line\nsecond sq's line padded beyond forty characters\n\nact\n\n",
	},
	{
		name:        "obs backspace escape",
		observation: "\b",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"\\b\"\naction: act\n---\n\b\n\nact\n\n",
	},
	{
		name:        "obs formfeed escape",
		observation: "\f",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"\\f\"\naction: act\n---\n\f\n\nact\n\n",
	},
	{
		name:        "obs bell escape",
		observation: "\u0007x",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"\\ax\"\naction: act\n---\n\u0007x\n\nact\n\n",
	},
	{
		name:        "obs escape seq",
		observation: "\u001b[A",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"\\e[A\"\naction: act\n---\n\u001b[A\n\nact\n\n",
	},
	{
		name:        "obs nul",
		observation: "a\u0000b",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"a\\0b\"\naction: act\n---\na\u0000b\n\nact\n\n",
	},
	{
		name:        "obs vertical tab",
		observation: "\u000b",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"\\v\"\naction: act\n---\n\u000b\n\nact\n\n",
	},
	{
		name:        "evidence item quoted",
		observation: "obs",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
	{
		name:        "evidence item needing quotes",
		observation: "obs",
		action:      "act",
		confidence:  0.72,
		evidence:    []string{"acme/widgets#481", "a: b"},
		golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\n  - \"a: b\"\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
	},
}

func TestSerializeMemoryRecordGoldenYAMLEdgeCases(t *testing.T) {
	for _, tc := range goldenYAMLExtraCases {
		t.Run(tc.name, func(t *testing.T) {
			record := schemas.MemoryRecord{
				Schema:      schemas.MemoryRecordSchemaID,
				RecordID:    "01JZZ0000000000000000000AA",
				File:        schemas.MemoryFileKnownPatterns,
				PatternName: "p",
				EvidencePRs: tc.evidence,
				Confidence:  tc.confidence,
				CreatedAt:   fixedNow,
				Observation: tc.observation,
				Action:      tc.action,
			}
			block, err := memory.SerializeMemoryRecord(&record)
			require.NoError(t, err)
			assert.Equal(t, tc.golden, block)
			// Round-trip is only meaningful inside the schema's confidence
			// domain; the formatting cases beyond it pin the writer bytes only.
			if tc.confidence >= 0 && tc.confidence <= 1 {
				parsed, err := memory.ParseMemoryRecords(block)
				require.NoError(t, err)
				require.Len(t, parsed, 1)
				assert.Equal(t, record, parsed[0])
			}
		})
	}
}

/* ------------------------------------------------------------- error surfaces */

func TestNewMemoryStoreRequiresCallerSuppliedClock(t *testing.T) {
	// Timestamps are ALWAYS caller-supplied (D6): a missing clock is a
	// configuration error, never a wall-clock fallback.
	_, err := memory.NewMemoryStore(memory.MemoryStoreConfig{
		MemoryDir: filepath.Join(t.TempDir(), "memory"),
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))

	_, err = memory.NewMemoryStore(memory.MemoryStoreConfig{Clock: fixedClock})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
}

func TestMemoryDirAccessor(t *testing.T) {
	store, memoryDir := newTestStore(t)
	assert.Equal(t, memoryDir, store.MemoryDir())
}

func TestEnforceMemoryLayoutReportsIoFailures(t *testing.T) {
	// A file where the memory directory belongs: mkdir fails (fail closed).
	dir := t.TempDir()
	fileAsDir := filepath.Join(dir, "not-a-dir")
	require.NoError(t, os.WriteFile(fileAsDir, []byte("x"), 0o644))
	_, err := memory.EnforceMemoryLayout(fileAsDir)
	require.Error(t, err)
	var layoutErr *memory.MemoryLayoutError
	assert.ErrorAs(t, err, &layoutErr)
	assert.Contains(t, layoutErr.Message(), "failed to create memory directory")

	// A missing directory: readdir fails after mkdir fails — the same
	// typed surface, never "assume empty".
	_, err = memory.ValidateMemoryLayout(filepath.Join(dir, "absent"))
	require.Error(t, err)
	var readErr *memory.MemoryLayoutError
	assert.ErrorAs(t, err, &readErr)
	assert.Contains(t, readErr.Message(), "failed to list memory directory")
}

func TestReadMemoryTextFileFailClosedOnMissing(t *testing.T) {
	_, memoryDir := newTestStore(t)
	_, err := memory.ReadMemoryTextFile(memoryDir, schemas.MemoryFileDecisions)
	require.NoError(t, err) // enforcement scaffolded all four

	// Delete one file: reading it is a typed layout error, never "".
	require.NoError(t, os.Remove(filepath.Join(memoryDir, memory.MemoryFileName(schemas.MemoryFileExceptions))))
	_, err = memory.ReadMemoryTextFile(memoryDir, schemas.MemoryFileExceptions)
	require.Error(t, err)
	var layoutErr *memory.MemoryLayoutError
	assert.ErrorAs(t, err, &layoutErr)
	assert.Contains(t, layoutErr.Message(), "run enforceMemoryLayout before reading")

	// A directory where a file belongs: a hard I/O error.
	failuresPath := filepath.Join(memoryDir, memory.MemoryFileName(schemas.MemoryFileFailures))
	require.NoError(t, os.Remove(failuresPath))
	require.NoError(t, os.Mkdir(failuresPath, 0o755))
	_, err = memory.ReadMemoryTextFile(memoryDir, schemas.MemoryFileFailures)
	require.Error(t, err)
	var readErr *memory.MemoryLayoutError
	assert.ErrorAs(t, err, &readErr)
	assert.Contains(t, readErr.Message(), "failed to read memory file")
}

func TestAtomicWriteTextReplacesAndFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.md")
	require.NoError(t, memory.AtomicWriteText(path, "one\n"))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "one\n", string(raw))
	// Replace (no temp leftovers).
	require.NoError(t, memory.AtomicWriteText(path, "two\n"))
	raw, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "two\n", string(raw))
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	// Nonexistent parent: a hard error, not a silent no-op.
	require.Error(t, memory.AtomicWriteText(
		filepath.Join(t.TempDir(), "absent", "memory.md"), "x\n"))
}

func TestStoreFailsClosedOnDeletedFileAndCorruptRecord(t *testing.T) {
	store, memoryDir := newTestStore(t)
	// A file deleted after enforcement: ParseFile is a typed layout error.
	require.NoError(t, os.Remove(filepath.Join(memoryDir, memory.MemoryFileName(schemas.MemoryFileDecisions))))
	_, err := store.ParseFile(schemas.MemoryFileDecisions)
	require.Error(t, err)
	var layoutErr *memory.MemoryLayoutError
	assert.ErrorAs(t, err, &layoutErr)

	// ParseAll propagates the failure (fail closed).
	_, err = store.ParseAll()
	require.Error(t, err)
	assert.ErrorAs(t, err, &layoutErr)
}

func TestAppendRecordRefusesEmptyEvidence(t *testing.T) {
	store, _ := newTestStore(t)
	input := patternInput()
	input.EvidencePRs = nil
	_, err := store.AppendRecord(input)
	require.Error(t, err)
	var recordErr *memory.MemoryRecordError
	assert.ErrorAs(t, err, &recordErr)
	assert.Contains(t, recordErr.Message(), "memory record refused (fail closed, nothing written, D6)")
}

/* ------------------------------------------------- more emitter branch pins */

func TestSerializeMemoryRecordGoldenWriterBranches(t *testing.T) {
	// Last emitter branches: negative/exponential confidence formatting, the
	// double-quoted newline folding (>= 40 chars), the single-quote fallback
	// when whitespace wraps a newline, and the flow form of an empty
	// evidence array (unreachable through schema validation — pinned for
	// writer fidelity only).
	cases := []struct {
		name        string
		observation string
		action      string
		confidence  float64
		evidence    []string
		golden      string
	}{
		{
			name:        "negative confidence",
			observation: "obs",
			action:      "act",
			confidence:  -0.5,
			evidence:    []string{"acme/widgets#481"},
			golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: -0.5\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
		},
		{
			name:        "negative tiny confidence",
			observation: "obs",
			action:      "act",
			confidence:  -1e-7,
			evidence:    []string{"acme/widgets#481"},
			golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: -1e-7\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
		},
		{
			name:        "long crlf folding",
			observation: "long value with \r\n and enough length to pass forty chars easily yes",
			action:      "act",
			confidence:  0.72,
			evidence:    []string{"acme/widgets#481"},
			golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"long value with \\r\n\n  \\ and enough length to pass forty chars easily yes\"\naction: act\n---\nlong value with \r\n and enough length to pass forty chars easily yes\n\nact\n\n",
		},
		{
			name:        "single quote fallback via trailing ws",
			observation: "\"dq\"\n ",
			action:      "act",
			confidence:  0.72,
			evidence:    []string{"acme/widgets#481"},
			golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs:\n  - acme/widgets#481\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: \"\\\"dq\\\"\\n \"\naction: act\n---\n\"dq\"\n \n\nact\n\n",
		},
		{
			name:        "no evidence items",
			observation: "obs",
			action:      "act",
			confidence:  0.72,
			evidence:    []string{},
			golden:      "---\nschema: docsync.memory-record/v1\nrecord_id: 01JZZ0000000000000000000AA\nfile: known-patterns\npattern_name: p\nevidence_prs: []\nconfidence: 0.72\nunverifiable: false\ncreated_at: 2026-10-08T12:00:00.000Z\nobservation: obs\naction: act\n---\nobs\n\nact\n\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := schemas.MemoryRecord{
				Schema:      schemas.MemoryRecordSchemaID,
				RecordID:    "01JZZ0000000000000000000AA",
				File:        schemas.MemoryFileKnownPatterns,
				PatternName: "p",
				EvidencePRs: tc.evidence,
				Confidence:  tc.confidence,
				CreatedAt:   fixedNow,
				Observation: tc.observation,
				Action:      tc.action,
			}
			block, err := memory.SerializeMemoryRecord(&record)
			require.NoError(t, err)
			assert.Equal(t, tc.golden, block)
		})
	}
}

func TestMemoryErrorAccessors(t *testing.T) {
	layoutErr := memory.NewMemoryLayoutError("boom", nil)
	assert.Equal(t, memory.EDOCSYNC_MEMORY_LAYOUT, layoutErr.Code())
	assert.Equal(t, "boom", layoutErr.Message())
	assert.Equal(t, "EDOCSYNC_MEMORY_LAYOUT: boom", layoutErr.Error())
	// Unwrap exposes the coded error, so docutil.CodeOf works through it.
	var coded *docutil.Error
	require.True(t, errors.As(error(layoutErr), &coded))
	assert.Equal(t, memory.EDOCSYNC_MEMORY_LAYOUT, coded.Code)

	recordErr := memory.NewMemoryRecordError("nope")
	assert.Equal(t, memory.EDOCSYNC_MEMORY_RECORD, recordErr.Code())
	assert.Equal(t, "nope", recordErr.Message())
	assert.Equal(t, "EDOCSYNC_MEMORY_RECORD: nope", recordErr.Error())

	// The layout error wraps its cause (errors.Is reaches it).
	cause := errors.New("io")
	wrapped := memory.NewMemoryLayoutError("boom", cause)
	assert.ErrorIs(t, wrapped, cause)
	assert.Equal(t, docutil.CodeOf(wrapped), memory.EDOCSYNC_MEMORY_LAYOUT)
}

func TestAppendRecordFailsClosedWhenIdMintFails(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	steps := []int64{1_760_000_000_000, -1}
	call := 0
	store, err := memory.NewMemoryStore(memory.MemoryStoreConfig{
		MemoryDir: memoryDir,
		Clock:     fixedClock,
		ULIDs: docutil.NewULIDGeneratorWith(func() int64 {
			v := steps[min(call, len(steps)-1)]
			call++
			return v
		}, func(n int) ([]byte, error) { return make([]byte, n), nil }),
	})
	require.NoError(t, err)
	// First append mints with a valid timestamp; the contradict's failure
	// record mint hits the out-of-range clock (fail closed, nothing written).
	pattern, err := store.AppendRecord(patternInput())
	require.NoError(t, err)
	_, err = store.Contradict(memory.ContradictionInput{
		RecordID:    pattern.RecordID,
		Observation: "refuted",
		Evidence:    []string{"p#1"},
	})
	require.Error(t, err)
	failures, err := store.ParseFile(schemas.MemoryFileFailures)
	require.NoError(t, err)
	assert.Len(t, failures, 0)
}

func TestAppendRecordFailsClosedWhenFileVanishes(t *testing.T) {
	store, memoryDir := newTestStore(t)
	require.NoError(t, os.RemoveAll(memoryDir))
	_, err := store.AppendRecord(patternInput())
	require.Error(t, err)
	var layoutErr *memory.MemoryLayoutError
	assert.ErrorAs(t, err, &layoutErr)
}

func TestContradictRewritesDecayedRecordExactBytes(t *testing.T) {
	// The R12 rewrite replaces the source file with the re-serialized records
	// (decayed) — byte-for-byte what the TS module writes, scaffold text
	// included in nothing: the file content is exactly the record blocks.
	store, memoryDir := newTestStore(t)
	pattern, err := store.AppendRecord(patternInput())
	require.NoError(t, err)
	_, err = store.Contradict(memory.ContradictionInput{
		RecordID:    pattern.RecordID,
		Observation: "the generated-docs lane misfired on API renames (PR audit)",
		Evidence:    []string{"acme/widgets#900"},
	})
	require.NoError(t, err)

	rewritten, err := os.ReadFile(filepath.Join(memoryDir, memory.MemoryFileName(schemas.MemoryFileKnownPatterns)))
	require.NoError(t, err)
	decayed := *pattern
	decayed.Confidence = 0.72 * memory.MemoryDecayFactor
	block, err := memory.SerializeMemoryRecord(&decayed)
	require.NoError(t, err)
	assert.Equal(t, block, string(rewritten))

	// The stored record keeps its identity (id, file, prose) and only the
	// confidence moved — and the decayed file still parses to it.
	patterns, err := store.ParseFile(schemas.MemoryFileKnownPatterns)
	require.NoError(t, err)
	require.Len(t, patterns, 1)
	assert.Equal(t, decayed, patterns[0])
}
