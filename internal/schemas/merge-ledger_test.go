package schemas_test

// Ported from packages/schemas/test/merge-ledger.test.ts —
// docsync.merge-ledger/v1 (P6-T18), including the fail-closed superRefine
// (doc_pr_number unique within the ledger).

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeLedgerExampleValidates(t *testing.T) {
	example := schemas.MergeLedgerExample()
	require.Empty(t, example.Validate())
	_, err := schemas.ParseMergeLedger(mustJSONTyped(t, example))
	require.NoError(t, err)
}

func TestMergeLedgerRoundTripsByteStable(t *testing.T) {
	first := mustJSONTyped(t, schemas.MergeLedgerExample())

	var decoded schemas.MergeLedger
	require.NoError(t, schemas.StrictDecode(first, &decoded))
	second := mustJSONTyped(t, decoded)

	assert.Equal(t, string(first), string(second))
	require.Empty(t, decoded.Validate())
}

func TestMergeLedgerRejectsUnknownTopLevelAndEntryKeys(t *testing.T) {
	base := mustJSONTyped(t, schemas.MergeLedgerExample())

	topLevel := mutateJSON(t, base, func(tree any) {
		tree.(map[string]any)["extra_field"] = true
	})
	_, err := schemas.ParseMergeLedger(topLevel)
	require.Error(t, err)

	entry := mutateJSON(t, base, func(tree any) {
		entries := tree.(map[string]any)["entries"].([]any)
		entries[0].(map[string]any)["extra_field"] = true
	})
	_, err = schemas.ParseMergeLedger(entry)
	require.Error(t, err)
}

func TestMergeLedgerRejectsBadFieldValues(t *testing.T) {
	ledgerCases := []struct {
		name   string
		mutate func(*schemas.MergeLedger)
	}{
		{"source_key", func(l *schemas.MergeLedger) { l.SourceKey = "" }},
		{"updated_at", func(l *schemas.MergeLedger) { l.UpdatedAt = "not-a-timestamp" }},
	}
	for _, tc := range ledgerCases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := schemas.MergeLedgerExample()
			tc.mutate(&ledger)
			require.NotEmpty(t, ledger.Validate(), "%s must be rejected", tc.name)

			_, err := schemas.ParseMergeLedger(mustJSONTyped(t, ledger))
			require.Error(t, err, "%s must be rejected at parse", tc.name)
		})
	}

	entryCases := []struct {
		name   string
		mutate func(*schemas.LedgerEntry)
	}{
		{"idempotency_key", func(e *schemas.LedgerEntry) { e.IdempotencyKey = "" }},
		{"at", func(e *schemas.LedgerEntry) { e.At = "not-a-timestamp" }},
		{"doc_pr_number", func(e *schemas.LedgerEntry) { e.DocPRNumber = 0 }},
		{"detail", func(e *schemas.LedgerEntry) { e.Detail = "" }},
	}
	for _, tc := range entryCases {
		t.Run("entry_"+tc.name, func(t *testing.T) {
			entry := schemas.MergeLedgerExample().Entries[0]
			tc.mutate(&entry)
			require.NotEmpty(t, entry.Validate(), "entry %s must be rejected", tc.name)

			ledger := schemas.MergeLedgerExample()
			ledger.Entries = []schemas.LedgerEntry{entry}
			require.NotEmpty(t, ledger.Validate(), "ledger carrying entry %s must be rejected", tc.name)

			_, err := schemas.ParseMergeLedger(mustJSONTyped(t, ledger))
			require.Error(t, err, "entry %s must be rejected at parse", tc.name)
		})
	}
}

func TestMergeLedgerEmptyEntriesIsValid(t *testing.T) {
	ledger := schemas.MergeLedgerExample()
	ledger.Entries = []schemas.LedgerEntry{}

	require.Empty(t, ledger.Validate())
	_, err := schemas.ParseMergeLedger(mustJSONTyped(t, ledger))
	require.NoError(t, err)
}

func TestMergeLedgerRejectsRepeatedDocPrNumberAtOffendingEntry(t *testing.T) {
	ledger := schemas.MergeLedgerExample()
	ledger.Entries = append(ledger.Entries, ledger.Entries[0])

	issues := ledger.Validate()
	require.NotEmpty(t, issues)
	found := false
	for _, iss := range issues {
		if assert.ObjectsAreEqual([]string{"entries", "2"}, iss.Path) &&
			iss.Message == "doc_pr_number 84 appears more than once in the ledger (merge-order ambiguity)" {
			found = true
		}
	}
	assert.True(t, found, "expected the duplicate issue at entries.2, got %v", issues)

	_, err := schemas.ParseMergeLedger(mustJSONTyped(t, ledger))
	require.Error(t, err)
}

func TestMergeLedgerDistinctDocPrNumbersAcrossStatesParse(t *testing.T) {
	ledger := schemas.MergeLedgerExample()
	ledger.Entries = []schemas.LedgerEntry{
		{
			DocPRNumber:    90,
			State:          schemas.MergeLedgerStateOpen,
			IdempotencyKey: "docsync-acme-payments-90",
			At:             "2026-10-05T10:00:00Z",
			Detail:         "proposal opened for docs/api.md",
		},
		{
			DocPRNumber:    91,
			State:          schemas.MergeLedgerStateConflicted,
			IdempotencyKey: "docsync-acme-payments-91",
			At:             "2026-10-05T10:01:00Z",
			Detail:         "merge conflict with source PR — needs rebase",
		},
		{
			DocPRNumber:    92,
			State:          schemas.MergeLedgerStateDropped,
			IdempotencyKey: "docsync-acme-payments-92",
			At:             "2026-10-05T10:02:00Z",
			Detail:         "dropped — the source PR was closed before the doc PR merged",
		},
	}

	require.Empty(t, ledger.Validate())
	_, err := schemas.ParseMergeLedger(mustJSONTyped(t, ledger))
	require.NoError(t, err)
}

func TestMergeLedgerStateValuesIsTheFrozenFiveStateUnion(t *testing.T) {
	assert.Equal(t, []schemas.MergeLedgerState{
		"open", "superseded", "merged", "conflicted", "dropped",
	}, schemas.MergeLedgerStateValues)

	entry := schemas.MergeLedgerExample().Entries[0]
	entry.State = "aborted"
	require.NotEmpty(t, entry.Validate())
}

func TestMergeLedgerYAMLRoundTripsTheExample(t *testing.T) {
	// YAML config inputs carry the same snake_case keys; yaml.v3 reads `yaml`
	// tags, so the example must decode through the YAML path too.
	data := []byte(`schema: docsync.merge-ledger/v1
source_key: source-pr-42
updated_at: '2026-10-05T09:20:00Z'
entries:
  - doc_pr_number: 84
    state: merged
    idempotency_key: docsync-acme-payments-84
    at: '2026-10-05T09:18:41Z'
    detail: merged as the freshest proposal for docs/payments.md
  - doc_pr_number: 83
    state: superseded
    idempotency_key: docsync-acme-payments-83
    at: '2026-10-05T09:17:02Z'
    detail: 'superseded by #84 before merge (same doc, newer run)'
`)
	ledger, err := schemas.ParseMergeLedger(data)
	require.NoError(t, err)
	require.Empty(t, ledger.Validate())
	assert.Equal(t, schemas.MergeLedgerExample(), *ledger)
}

func TestMergeLedgerJSONDecodeKeepsNumbersExact(t *testing.T) {
	var decoded schemas.MergeLedger
	require.NoError(t, schemas.StrictDecode(mustJSONTyped(t, schemas.MergeLedgerExample()), &decoded))
	assert.Equal(t, 84, decoded.Entries[0].DocPRNumber)
	assert.Equal(t, 83, decoded.Entries[1].DocPRNumber)
}

func TestMergeLedgerJSONRoundTripIsStableAcrossMarshal(t *testing.T) {
	// Guard: map/array ordering in JSON marshaling must not drift.
	first := mustJSONTyped(t, schemas.MergeLedgerExample())
	var redecoded schemas.MergeLedger
	require.NoError(t, json.Unmarshal(first, &redecoded))
	assert.Equal(t, string(first), string(mustJSONTyped(t, redecoded)))
}
