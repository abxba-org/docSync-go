package memory_test

// Coverage-gap tests for internal/memory: the last emitter branches (double-
// quoted newline folding variants, decimal/exponential/number formatting),
// store fail-closed paths (corrupt third file through ParseAll, ULID mint
// failures on both the append and the contradiction paths) and the parser's
// unterminated-block refusal.

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/memory"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gapRecord(observation, action string, confidence float64) *schemas.MemoryRecord {
	return &schemas.MemoryRecord{
		Schema:      schemas.MemoryRecordSchemaID,
		RecordID:    "01JZZ0000000000000000000AA",
		File:        schemas.MemoryFileKnownPatterns,
		PatternName: "p",
		EvidencePRs: []string{"acme/widgets#481"},
		Confidence:  confidence,
		CreatedAt:   fixedNow,
		Observation: observation,
		Action:      action,
	}
}

func TestCOV_MEMORY_SerializeNumberFormats(t *testing.T) {
	short := "obs"
	cases := []struct {
		name       string
		confidence float64
		wantLine   string
	}{
		{name: "decimal insertion", confidence: 12.5, wantLine: "confidence: 12.5\n"},
		{name: "negative zero", confidence: math.Copysign(0, -1), wantLine: "confidence: -0\n"},
		{name: "large exponent", confidence: 1e21, wantLine: "confidence: 1e+21\n"},
		{name: "tiny exponent", confidence: 1e-7, wantLine: "confidence: 1e-7\n"},
		{name: "leading zero", confidence: 0.05, wantLine: "confidence: 0.05\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block, err := memory.SerializeMemoryRecord(gapRecord(short, "act", tc.confidence))
			require.NoError(t, err)
			require.Contains(t, block, tc.wantLine)
		})
	}
}

func TestCOV_MEMORY_DoubleQuotedFoldingVariants(t *testing.T) {
	// A >40-char, single-quote-carrying observation becomes a double-quoted
	// scalar; its space-before-newline, consecutive-newline and
	// space-after-newline runs drive the folding branches. Round-trip is the
	// contract the store relies on.
	observation := "it's a folded line that must exceed forty characters with spaces  \n\nbetween two long narrative segments and a space \n after starting a new segment \n  "
	block, err := memory.SerializeMemoryRecord(gapRecord(observation, "act", 0.5))
	require.NoError(t, err)
	parsed, err := memory.ParseMemoryRecords(block)
	require.NoError(t, err)
	require.Len(t, parsed, 1)
	assert.Equal(t, observation, parsed[0].Observation)
}

func TestCOV_MEMORY_DoubleQuotedControlEscapes(t *testing.T) {
	// Control characters force the double-quoted style and its escape
	// rewrites: \0 \a \v \e plus the \x fallback for the rest.
	observation := "\x00\x07\x0b\x1b\x01 long enough to be well past forty characters each and every time it matters"
	block, err := memory.SerializeMemoryRecord(gapRecord(observation, "act", 0.5))
	require.NoError(t, err)
	parsed, err := memory.ParseMemoryRecords(block)
	require.NoError(t, err)
	require.Len(t, parsed, 1)
	assert.Equal(t, observation, parsed[0].Observation)
}

func TestCOV_MEMORY_SingleQuotedScalarEmission(t *testing.T) {
	// Values containing double quotes but no single quotes choose the
	// single-quoted style (single line, no whitespace-around-newline).
	block, err := memory.SerializeMemoryRecord(gapRecord(`#"hidden dq"`, "act", 0.5))
	require.NoError(t, err)
	require.Contains(t, block, "observation: '#\"hidden dq\"'\n")

	// A whitespace-wrapped newline inside a quote-preference value forces the
	// double-quoted fallback (still round-trips).
	block, err = memory.SerializeMemoryRecord(gapRecord("\"dq\"\n ", "act", 0.5))
	require.NoError(t, err)
	require.Contains(t, block, `observation: "\"dq\"\n "`)
	parsed, err := memory.ParseMemoryRecords(block)
	require.NoError(t, err)
	require.Len(t, parsed, 1)
	assert.Equal(t, "\"dq\"\n ", parsed[0].Observation)
}

func TestCOV_MEMORY_ParseMemoryRecordsUnterminatedAndInvalidYAML(t *testing.T) {
	_, err := memory.ParseMemoryRecords("---\nschema: docsync.memory-record/v1\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unterminated front-matter block")

	_, err = memory.ParseMemoryRecords("---\na: [\n---\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unparseable front-matter block")
}

func TestCOV_MEMORY_StoreParseAllFailsClosedOnCorruptThirdFile(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewMemoryStore(memory.MemoryStoreConfig{MemoryDir: dir, Clock: func() string { return fixedNow }})
	require.NoError(t, err)

	// The THIRD file (exceptions.md) is corrupted after enforcement.
	exceptionsPath := filepath.Join(dir, memory.MemoryFileName(schemas.MemoryFileExceptions))
	require.NoError(t, os.WriteFile(exceptionsPath, []byte("---\nbroken: [\n"), 0o644))

	_, err = store.ParseAll()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unterminated front-matter")
}

func TestCOV_MEMORY_AppendRecordULIDMintFailure(t *testing.T) {
	dir := t.TempDir()
	_, err := memory.NewMemoryStore(memory.MemoryStoreConfig{MemoryDir: dir, Clock: func() string { return fixedNow }})
	require.NoError(t, err)

	failingULID := docutil.NewULIDGeneratorWith(func() int64 { return -1 }, func(n int) ([]byte, error) { return make([]byte, n), nil })
	store, err := memory.NewMemoryStore(memory.MemoryStoreConfig{
		MemoryDir: dir,
		Clock:     func() string { return fixedNow },
		ULIDs:     failingULID,
	})
	require.NoError(t, err)
	_, err = store.AppendRecord(memory.MemoryRecordInput{
		File:        schemas.MemoryFileDecisions,
		PatternName: "p",
		EvidencePRs: []string{"acme/widgets#100"},
		Observation: "obs",
		Action:      "act",
		Confidence:  0.5,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint a record id")
}

func TestCOV_MEMORY_ContradictFailureIDMintFailsClosedAfterDecay(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	// First ULID mint (the pattern record) works; the SECOND mint (the
	// contradiction's failure record) fails — decay already happened and the
	// failure record is refused (fail closed, D6).
	ulid := docutil.NewULIDGeneratorWith(func() int64 {
		calls++
		if calls == 2 {
			return -1
		}
		return 1750000000000
	}, func(n int) ([]byte, error) { return make([]byte, n), nil })
	store, err := memory.NewMemoryStore(memory.MemoryStoreConfig{
		MemoryDir: dir,
		Clock:     func() string { return fixedNow },
		ULIDs:     ulid,
	})
	require.NoError(t, err)

	record, err := store.AppendRecord(memory.MemoryRecordInput{
		File:        schemas.MemoryFileDecisions,
		PatternName: "p",
		EvidencePRs: []string{"acme/widgets#101"},
		Observation: "obs",
		Action:      "act",
		Confidence:  0.8,
	})
	require.NoError(t, err)

	_, err = store.Contradict(memory.ContradictionInput{
		RecordID:    record.RecordID,
		Observation: "this belief is being refuted",
		Evidence:    []string{"acme/widgets#102"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint a record id")

	// The decayed rewrite already took effect before the failure mint failed.
	raw, err := os.ReadFile(filepath.Join(dir, memory.MemoryFileName(schemas.MemoryFileDecisions)))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "0.4")
	assert.False(t, strings.Contains(string(raw), "failures"))
}

func TestCOV_MEMORY_AtomicWriteTextRenameFailsClosed(t *testing.T) {
	// A temp file in the destination directory is written, then the rename
	// onto the target (an existing DIRECTORY) fails — the failure surfaces and
	// the temp file is cleaned up.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "target"), 0o755))
	err := memory.AtomicWriteText(filepath.Join(dir, "target"), "payload")
	require.Error(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), "target.tmp-"))
	}
}
