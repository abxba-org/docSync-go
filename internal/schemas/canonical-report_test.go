package schemas_test

// Ported from packages/schemas/test/canonical-report.test.ts (black-box,
// testify) — "canonical report: the optional advisories surface"
// (P12-T13/F5-1; OD-R30; V-P12-12).
//
// G1-1: `docsync check --json` emits `{ ...report, advisories }` on a repo
// with an unreviewed `report_only_until_reviewed` floor. The registered schema
// is a z.strictObject, so the emitted payload must validate through it —
// otherwise every consumer that re-parses stdout with this schema (the repo's
// own convention, packages/cli/test/check-exit-codes.test.ts) crashes on
// "Unrecognized key(s): 'advisories'". The key is therefore declared OPTIONAL:
//   - a report WITHOUT the key (every repo with no org policy, or a reviewed
//     floor) validates unchanged — existing producers/consumers are unaffected;
//   - a report WITH the key validates;
//   - a non-array value is still rejected, and the strict object still rejects
//     unknown keys (the additive field must not loosen the pin).
//
// The example deliberately carries no `advisories` (mirrors the absent
// optional `usage`): the field is optional by contract, so a minimal example
// stays valid.
//
// Helper names are canonicalReport-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var canonicalReportAdvisoryMessages = []string{
	"manifest not yet reviewed: docs/api.md (floor docs/**, reviewers: security-team) — report-only until reviewed",
	"manifest not yet reviewed: docs/locked.md (floor docs/**, reviewers: security-team) — report-only until reviewed",
}

// canonicalReportToMap renders the example as a generic JSON map so tests can
// mutate fields the way the TS suite spreads copies ({...BASE, ...}).
func canonicalReportToMap(t *testing.T) map[string]any {
	t.Helper()
	b, err := json.Marshal(schemas.CanonicalReportExample())
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func canonicalReportWire(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

func canonicalReportParse(t *testing.T, m map[string]any) error {
	t.Helper()
	_, err := schemas.ParseCanonicalReport(canonicalReportWire(t, m))
	return err
}

func TestCanonicalReportAdvisories_ValidatesWithoutTheAdvisoriesKey(t *testing.T) {
	// TS: 'validates a report WITHOUT the advisories key (existing payloads
	// unchanged)' — expect(result.success).toBe(true).
	_, err := schemas.ParseCanonicalReport(canonicalReportWire(t, canonicalReportToMap(t)))
	require.NoError(t, err)
}

func TestCanonicalReportAdvisories_ValidatesWithTheAdvisoriesArray(t *testing.T) {
	base := canonicalReportToMap(t)
	base["advisories"] = append([]string{}, canonicalReportAdvisoryMessages...)
	require.NoError(t, canonicalReportParse(t, base))
}

func TestCanonicalReportAdvisories_ValidatesAnEmptyAdvisoriesArray(t *testing.T) {
	base := canonicalReportToMap(t)
	base["advisories"] = []string{}
	require.NoError(t, canonicalReportParse(t, base))
}

func TestCanonicalReportAdvisories_RejectsANonArrayAdvisoriesValue(t *testing.T) {
	// A bare string is not the surface: z.array(z.string()) rejects it — in
	// Go the decode of a JSON string into []string fails first.
	base := canonicalReportToMap(t)
	base["advisories"] = canonicalReportAdvisoryMessages[0]
	require.Error(t, canonicalReportParse(t, base))
}

func TestCanonicalReportAdvisories_RejectsANonStringElementInsideAdvisories(t *testing.T) {
	base := canonicalReportToMap(t)
	base["advisories"] = []any{
		canonicalReportAdvisoryMessages[0],
		canonicalReportAdvisoryMessages[1],
		map[string]any{"message": "not a string"},
	}
	require.Error(t, canonicalReportParse(t, base))
}

func TestCanonicalReportAdvisories_StillRejectsAnUnknownKey(t *testing.T) {
	// The additive field never loosens the strict pin.
	base := canonicalReportToMap(t)
	base["advisories"] = append([]string{}, canonicalReportAdvisoryMessages...)
	base["somethingElse"] = "unexpected"
	err := canonicalReportParse(t, base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestCanonicalReportAdvisories_RoundTripsAnEmittedCheckJSONPayload(t *testing.T) {
	// TS: the emitted `check --json` payload (report + advisories) survives a
	// JSON round trip through the schema with every canonical field intact.
	emittedReport := schemas.CanonicalReportExample()
	emittedReport.Advisories = &canonicalReportAdvisoryMessages
	emitted, err := json.Marshal(emittedReport)
	require.NoError(t, err)

	parsed, err := schemas.ParseCanonicalReport(emitted)
	require.NoError(t, err)
	// TS: expect(result.data.advisories).toEqual([...ADVISORY_MESSAGES])
	require.NotNil(t, parsed.Advisories)
	assert.Equal(t, canonicalReportAdvisoryMessages, *parsed.Advisories)
	// Every canonical field survives the round trip untouched.
	// TS: expect(result.data.runId).toBe(CANONICAL_REPORT_EXAMPLE.runId)
	assert.Equal(t, emittedReport.RunID, parsed.RunID)
	assert.Equal(t, emittedReport, *parsed)
}

// TestCanonicalReportExample_ParsesAndRoundTripsByteStable pins the golden
// wire format of the example (CONVENTIONS: "TS *_EXAMPLE values become Go
// literals and are asserted byte-for-byte through the Go serializer"): the
// example parses through the strict schema, the parse equals the example, and
// re-serializing the parse reproduces the original bytes.
func TestCanonicalReportExample_ParsesAndRoundTripsByteStable(t *testing.T) {
	// Marshal the struct directly (not via canonicalReportToMap's map) so key
	// order stays the TS declaration order the wire format pins.
	first, err := json.Marshal(schemas.CanonicalReportExample())
	require.NoError(t, err)
	parsed, err := schemas.ParseCanonicalReport(first)
	require.NoError(t, err)
	assert.Equal(t, schemas.CanonicalReportExample(), *parsed)

	second, err := json.Marshal(parsed)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
}
