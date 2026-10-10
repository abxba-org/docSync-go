package schemas_test

// Ported from packages/schemas/test/org-policy-schema-p12-t13.test.ts
// (P12-T13; ARCH §11.2, §11.3; V-P12-12) — black-box, testify.
//
// Covers: schema registration id, example round-trip, floor kind parsing,
// mode-ceiling and report-only floor acceptance, and the fail-closed
// rejections (invalid floor kind, invalid maxMode, missing floors, invalid
// learning mode). The TS "registry" describe block (SCHEMA_REGISTRY /
// getSchema) is intentionally NOT ported: the registry barrel belongs to the
// registry unit's file, not this one.
//
// Helper names are orgPolicy-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ helpers */

func orgPolicyMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// orgPolicyToMap renders a value as a generic JSON map so tests can mutate
// fields the way the TS suite spreads copies ({...BASE, ...}).
func orgPolicyToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(orgPolicyMarshal(t, v), &m))
	return m
}

func orgPolicyWire(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

// orgPolicyMutate renders the example, applies a mutation to the map, and
// returns the wire bytes (mirrors {...ORG_POLICY_EXAMPLE, ...}).
func orgPolicyMutate(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := orgPolicyToMap(t, schemas.OrgPolicyExample)
	mutate(m)
	return orgPolicyWire(t, m)
}

func orgPolicyParse(t *testing.T, data []byte) error {
	t.Helper()
	_, err := schemas.ParseOrgPolicy(data)
	return err
}

/* ------------------------------------------------------------------- tests */

func TestP12T13_HasCorrectSchemaID(t *testing.T) {
	assert.Equal(t, "docsync.org-policy/v1", schemas.OrgPolicySchemaID)
}

func TestP12T13_ValidatesTheExample(t *testing.T) {
	require.NoError(t, orgPolicyParse(t, orgPolicyMarshal(t, schemas.OrgPolicyExample)))
}

func TestP12T13_RejectsInvalidFloorKind(t *testing.T) {
	data := orgPolicyMutate(t, func(m map[string]any) {
		m["floors"] = []any{map[string]any{"kind": "invalid_kind", "pattern": "docs/**", "maxMode": "suggest"}}
	})
	assert.Error(t, orgPolicyParse(t, data))
}

func TestP12T13_AcceptsModeCeilingFloors(t *testing.T) {
	data := orgPolicyMutate(t, func(m map[string]any) {
		m["floors"] = []any{map[string]any{"kind": "mode_ceiling", "pattern": "docs/security/**", "maxMode": "suggest"}}
	})
	assert.NoError(t, orgPolicyParse(t, data))
}

func TestP12T13_AcceptsReportOnlyUntilReviewedFloors(t *testing.T) {
	data := orgPolicyMutate(t, func(m map[string]any) {
		m["floors"] = []any{map[string]any{
			"kind":              "report_only_until_reviewed",
			"pattern":           "docs/**",
			"requiredReviewers": []any{"security-team"},
		}}
	})
	assert.NoError(t, orgPolicyParse(t, data))
}

func TestP12T13_RejectsModeCeilingWithInvalidMaxMode(t *testing.T) {
	data := orgPolicyMutate(t, func(m map[string]any) {
		m["floors"] = []any{map[string]any{"kind": "mode_ceiling", "pattern": "docs/**", "maxMode": "invalid_mode"}}
	})
	assert.Error(t, orgPolicyParse(t, data))
}

func TestP12T13_RejectsMissingFloorsArray(t *testing.T) {
	data := orgPolicyMutate(t, func(m map[string]any) {
		m["floors"] = []any{}
	})
	assert.Error(t, orgPolicyParse(t, data))
}

func TestP12T13_AcceptsOrgLearningDefaultMode(t *testing.T) {
	data := orgPolicyMutate(t, func(m map[string]any) {
		m["learning"] = map[string]any{"defaultMode": "canary"}
	})
	assert.NoError(t, orgPolicyParse(t, data))
}

func TestP12T13_RejectsInvalidLearningDefaultMode(t *testing.T) {
	data := orgPolicyMutate(t, func(m map[string]any) {
		m["learning"] = map[string]any{"defaultMode": "invalid_mode"}
	})
	assert.Error(t, orgPolicyParse(t, data))
}

func TestP12T13_RejectsCrossVariantFloorKeys(t *testing.T) {
	// z.strictObject per variant: a mode_ceiling floor carrying
	// report_only_until_reviewed keys rejects fail-closed.
	data := orgPolicyMutate(t, func(m map[string]any) {
		m["floors"] = []any{map[string]any{
			"kind":              "mode_ceiling",
			"pattern":           "docs/**",
			"maxMode":           "suggest",
			"requiredReviewers": []any{"security-team"},
			"reviewed":          true,
		}}
	})
	assert.Error(t, orgPolicyParse(t, data))
}

func TestP12T13_RejectsUnknownRootKeyAndInvalidDatetimes(t *testing.T) {
	// z.strictObject ⇒ unknown JSON keys are rejected at parse.
	unknown := orgPolicyMutate(t, func(m map[string]any) { m["extra"] = true })
	assert.Error(t, orgPolicyParse(t, unknown))
	createdAt := orgPolicyMutate(t, func(m map[string]any) { m["createdAt"] = "not-a-datetime" })
	assert.Error(t, orgPolicyParse(t, createdAt))
	updatedAt := orgPolicyMutate(t, func(m map[string]any) { m["updatedAt"] = "2026-10-01T12:00:00+02:00" })
	assert.Error(t, orgPolicyParse(t, updatedAt))
}

func TestP12T13_ExampleRoundTripsThroughJSON(t *testing.T) {
	data := orgPolicyMarshal(t, schemas.OrgPolicyExample)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	require.NoError(t, orgPolicyParse(t, orgPolicyWire(t, parsed)))
}
