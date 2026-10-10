package schemas_test

// Ported from packages/schemas/test/memory-record.test.ts —
// docsync.memory-record/v1 (P10-T01; ARCH §8.8, CTX §25, D1): exactly four
// memory files; evidence-backed mandatory; unverifiable ⇒ near-floor
// confidence (R12).

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryRecordExampleValidates(t *testing.T) {
	record := schemas.MemoryRecordExample()
	require.Empty(t, record.Validate())
	_, err := schemas.ParseMemoryRecord(mustJSONTyped(t, record))
	require.NoError(t, err)
}

func TestMemoryRecordFileIsTheD1FourFileLayoutClosed(t *testing.T) {
	assert.Equal(t, []schemas.MemoryFile{
		"decisions", "known-patterns", "exceptions", "failures",
	}, schemas.MemoryFileValues)

	rejected := schemas.MemoryRecordExample()
	rejected.File = "notes"
	require.NotEmpty(t, rejected.Validate())
	_, err := schemas.ParseMemoryRecord(mustJSONTyped(t, rejected))
	require.Error(t, err)
}

func TestMemoryRecordEvidenceBackedIsMandatory(t *testing.T) {
	rejected := schemas.MemoryRecordExample()
	rejected.EvidencePRs = []string{}

	issues := rejected.Validate()
	require.NotEmpty(t, issues)
	found := false
	for _, iss := range issues {
		if assert.ObjectsAreEqual([]string{"evidence_prs"}, iss.Path) &&
			iss.Message == "Array must contain at least 1 element(s)" {
			found = true
		}
	}
	assert.True(t, found, "expected the evidence_prs min-length issue, got %v", issues)

	_, err := schemas.ParseMemoryRecord(mustJSONTyped(t, rejected))
	require.Error(t, err)
}

func TestMemoryRecordUnverifiableMustSitNearConfidenceFloor(t *testing.T) {
	high := schemas.MemoryRecordExample()
	high.Unverifiable = true
	high.Confidence = 0.9

	issues := high.Validate()
	require.NotEmpty(t, issues)
	found := false
	for _, iss := range issues {
		if iss.Message == "an unverifiable entry must be stored with confidence near the floor (<= 0.3) — an anecdote is a hypothesis, not a fact (ARCH §8.8, R12)" {
			found = true
		}
	}
	assert.True(t, found, "expected the near-floor issue, got %v", issues)

	_, err := schemas.ParseMemoryRecord(mustJSONTyped(t, high))
	require.Error(t, err)

	floor := schemas.MemoryRecordExample()
	floor.Unverifiable = true
	floor.Confidence = 0.3
	require.Empty(t, floor.Validate())
	_, err = schemas.ParseMemoryRecord(mustJSONTyped(t, floor))
	require.NoError(t, err)
}

func TestMemoryRecordConfidenceIsAUnitInterval(t *testing.T) {
	for _, confidence := range []float64{-0.1, 1.1} {
		rejected := schemas.MemoryRecordExample()
		rejected.Confidence = confidence
		require.NotEmpty(t, rejected.Validate(), "confidence %v is outside the unit interval", confidence)
	}

	edges := schemas.MemoryRecordExample()
	edges.Confidence = 1
	require.Empty(t, edges.Validate())
	edges.Confidence = 0
	require.Empty(t, edges.Validate())
}

func TestMemoryRecordUnknownKeysRefused(t *testing.T) {
	base := mustJSONTyped(t, schemas.MemoryRecordExample())
	extra := mutateJSON(t, base, func(tree any) {
		tree.(map[string]any)["extra"] = float64(1)
	})
	_, err := schemas.ParseMemoryRecord(extra)
	require.Error(t, err)
}

func TestMemoryRecordContradictionOfMustBeULIDWhenPresent(t *testing.T) {
	rejected := schemas.MemoryRecordExample()
	rejected.ContradictionOf = new("not-a-ulid")
	require.NotEmpty(t, rejected.Validate())

	ok := schemas.MemoryRecordExample()
	ok.ContradictionOf = new("01JMEMCRY1EXAMP1E000000000")
	require.Empty(t, ok.Validate())
}
