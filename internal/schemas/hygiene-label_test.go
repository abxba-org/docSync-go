package schemas_test

// Ported from packages/schemas/test/hygiene-label.test.ts (black-box,
// testify).
//
// Covers the `docsync.hygiene-label/v1` normalized decision label (P10-T01;
// ARCH §8.7 label hygiene): the validated example row, the literal schema tag,
// the closed six-label and nine-source-signal unions (the five reviewer signals
// PLUS the four non-reviewer hygiene sources — OD-2 reuse of the feedback-event
// vocabulary), a row for every ARCH §8.7 hygiene-rule shape
// (timeout⇒unlabeled, edit⇒corrected,
// reject_reason:doc_already_correct⇒false_positive,
// reject_reason:other⇒style_rejected,
// post_merge_edit⇒false_negative_true_negative, direct), the fail-closed
// timeout-never-accepted invariant, the derivation grammar, and strict
// unknown-key rejection (OD-R01).
//
// As in the TS suite, the registry/json-schema coverage is out of scope: the
// label is imported directly from the source module.
//
// Helper names are hygieneLabel-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so shared
// generic helper names would redeclare.

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ helpers */

// hygieneLabelWire renders HYGIENE_LABEL_EXAMPLE as generic JSON, applies the
// mutation (the TS suite spreads copies: {...EXAMPLE, <field>: <bad>}) and
// re-serializes. Deleting a key models the TS `undefined` assignment.
func hygieneLabelWire(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(schemas.HygieneLabelExample())
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	if mutate != nil {
		mutate(m)
	}
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

// hygieneLabelIssues strictly parses data (which MUST fail) and returns the
// zod-mirroring issues.
func hygieneLabelIssues(t *testing.T, data []byte) []schemas.Issue {
	t.Helper()
	_, err := schemas.ParseHygieneLabel(data)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Issues
}

// hygieneLabelIssueAt asserts an issue exists on the path segment (TS:
// issue.path.includes(<segment>)).
func hygieneLabelIssueAt(t *testing.T, data []byte, segment string) {
	t.Helper()
	issues := hygieneLabelIssues(t, data)
	for _, iss := range issues {
		for _, seg := range iss.Path {
			if seg == segment {
				return
			}
		}
	}
	require.Failf(t, "issue not found", "expected an issue on path segment %q, got %v", segment, issues)
}

/* -------------------------------------------------------------------- tests */

func TestHygieneLabel_ValidatesTheExampleRow(t *testing.T) {
	parsed, err := schemas.ParseHygieneLabel(hygieneLabelWire(t, nil))
	require.NoError(t, err)
	assert.Equal(t, schemas.HygieneLabelExample(), *parsed)
}

func TestHygieneLabel_CarriesTheLiteralSchemaTag(t *testing.T) {
	require.Equal(t, "docsync.hygiene-label/v1", schemas.HygieneLabelSchemaID)
	_, err := schemas.ParseHygieneLabel(hygieneLabelWire(t, func(m map[string]any) {
		m["schema"] = "docsync.hygiene-label/v2"
	}))
	require.Error(t, err)
}

func TestHygieneLabel_RefusesANonPositivePolicyVersion(t *testing.T) {
	_, err := schemas.ParseHygieneLabel(hygieneLabelWire(t, func(m map[string]any) {
		m["policy_version"] = 0
	}))
	require.Error(t, err)
}

func TestHygieneLabel_ExposesTheClosedSixLabelAndNineSourceUnions(t *testing.T) {
	assert.Equal(t, []schemas.HygieneLabelName{
		"unlabeled", "accepted", "corrected", "false_positive", "style_rejected", "true_negative",
	}, schemas.HygieneLabelValues)

	assert.Equal(t, []schemas.HygieneSourceSignal{
		"accept", "edit", "reject", "mark_as_intentional", "always_ignore",
		"timeout", "post_merge_edit", "spot_check", "audit_finding",
	}, schemas.HygieneSourceSignalValues)

	// The reviewer half IS the feedback-event vocabulary (OD-2 reuse, never
	// duplicated): the first five source signals equal FEEDBACK_EVENT_VALUES.
	require.Len(t, schemas.HygieneSourceSignalValues, len(schemas.FeedbackEventValues)+4)
	for i, v := range schemas.FeedbackEventValues {
		assert.Equal(t, string(v), string(schemas.HygieneSourceSignalValues[i]))
	}

	// The TS Object.isFrozen assertions have no Go counterpart: the slices are
	// the single source of truth in this package and nothing mutates them.
	_, err := schemas.ParseHygieneLabel(hygieneLabelWire(t, func(m map[string]any) {
		m["label"] = "promoted"
	}))
	require.Error(t, err)
	_, err = schemas.ParseHygieneLabel(hygieneLabelWire(t, func(m map[string]any) {
		m["source_signal"] = "vibes"
	}))
	require.Error(t, err)
}

// hygieneLabelCanonical renders v through docutil.CanonicalJSON — the exact
// serialization state files are written with (CAS hashes match the TS
// implementation only if this stays byte-stable).
func hygieneLabelCanonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var tree any
	require.NoError(t, json.Unmarshal(b, &tree))
	canonical, err := docutil.CanonicalJSON(tree)
	require.NoError(t, err)
	return canonical
}

// TestHygieneLabelExampleCanonicalJSONGolden pins the example as golden data
// (CONVENTIONS.md: TS *_EXAMPLE values are asserted byte-for-byte through the
// Go serializer).
func TestHygieneLabelExampleCanonicalJSONGolden(t *testing.T) {
	assert.Equal(t,
		`{"decision_id":"d_01J9ZK7Q2M","edit_diff":"-rate = 0.19\n+rate = 0.21",`+
			`"hygiene_rule":"edit⇒corrected","label":"corrected",`+
			`"label_id":"01J9ZM4R9BYZ5C7D9E1F3A5B7C","normalized_at":"2026-10-05T09:20:00Z",`+
			`"policy_version":17,"schema":"docsync.hygiene-label/v1","source_signal":"edit"}`,
		hygieneLabelCanonical(t, schemas.HygieneLabelExample()))
}

func TestHygieneLabel_MembershipAndGrammarHelpersAgreeWithTheFrozenUnions(t *testing.T) {
	for _, v := range schemas.HygieneLabelValues {
		assert.True(t, schemas.IsValidHygieneLabelName(string(v)), "label %q", v)
	}
	assert.False(t, schemas.IsValidHygieneLabelName("promoted"))

	for _, v := range schemas.HygieneSourceSignalValues {
		assert.True(t, schemas.IsValidHygieneSourceSignal(string(v)), "source_signal %q", v)
	}
	assert.False(t, schemas.IsValidHygieneSourceSignal("vibes"))

	// The derivation grammar: every ARCH §8.7 shape validates, the malformed
	// ones do not.
	for _, rule := range []string{
		"timeout⇒unlabeled", "edit⇒corrected",
		"reject_reason:doc_already_correct⇒false_positive",
		"reject_reason:other⇒style_rejected",
		"post_merge_edit⇒false_negative_true_negative", "direct",
	} {
		assert.True(t, schemas.IsValidHygieneRule(rule), "hygiene_rule %q", rule)
	}
	for _, rule := range []string{"timeout -> unlabeled", "TIMEOUT⇒unlabeled", "⇒unlabeled", "direct ", "timeout⇒unlabeled⇒accepted"} {
		assert.False(t, schemas.IsValidHygieneRule(rule), "hygiene_rule %q", rule)
	}
}

func TestHygieneLabel_ParsesARowForEveryHygieneRuleShape(t *testing.T) {
	rows := []struct {
		name        string
		labelID     string
		label       string
		source      string
		rule        string
		dropEditDif bool
	}{
		{"timeout⇒unlabeled", "01J9ZM5S0BXA6C8E0B2D4F6A8C", "unlabeled", "timeout", "timeout⇒unlabeled", true},
		{"edit⇒corrected", "", "corrected", "edit", "edit⇒corrected", false},
		{"reject_reason:doc_already_correct⇒false_positive", "01J9ZM6T0CYB7D9F1C3E5A7B9D", "false_positive", "reject", "reject_reason:doc_already_correct⇒false_positive", true},
		{"reject_reason:other⇒style_rejected", "01J9ZM7V0DZC8E0A2D4F6B8C0E", "style_rejected", "reject", "reject_reason:other⇒style_rejected", true},
		{"post_merge_edit⇒false_negative_true_negative", "01J9ZM8V1EAD9F1B3E5A7C9D1F", "true_negative", "post_merge_edit", "post_merge_edit⇒false_negative_true_negative", true},
		{"direct", "01J9ZM9W2FBE0A2C4F6B8D0E2A", "corrected", "edit", "direct", false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			data := hygieneLabelWire(t, func(m map[string]any) {
				if row.labelID != "" {
					m["label_id"] = row.labelID
				}
				m["label"] = row.label
				m["source_signal"] = row.source
				m["hygiene_rule"] = row.rule
				if row.dropEditDif {
					delete(m, "edit_diff")
				}
			})
			_, err := schemas.ParseHygieneLabel(data)
			require.NoError(t, err)
		})
	}
}

func TestHygieneLabel_RefusesAcceptedWithSourceSignalTimeout(t *testing.T) {
	// Timeout never accepted — an observation-window expiry is absence of
	// evidence, never acceptance (ARCH §8.7).
	data := hygieneLabelWire(t, func(m map[string]any) {
		m["label"] = "accepted"
		m["source_signal"] = "timeout"
		m["hygiene_rule"] = "timeout⇒accepted"
	})
	hygieneLabelIssueAt(t, data, "label")
}

func TestHygieneLabel_AcceptsAcceptedWithAReviewerSourceSignal(t *testing.T) {
	data := hygieneLabelWire(t, func(m map[string]any) {
		m["label"] = "accepted"
		m["source_signal"] = "accept"
		m["hygiene_rule"] = "accept⇒accepted"
	})
	_, err := schemas.ParseHygieneLabel(data)
	require.NoError(t, err)
}

func TestHygieneLabel_RefusesAHygieneRuleOutsideTheDerivationGrammar(t *testing.T) {
	for _, rule := range []string{"timeout -> unlabeled", "TIMEOUT⇒unlabeled", "⇒unlabeled"} {
		data := hygieneLabelWire(t, func(m map[string]any) { m["hygiene_rule"] = rule })
		hygieneLabelIssueAt(t, data, "hygiene_rule")
	}
}

func TestHygieneLabel_RefusesANonULIDLabelID(t *testing.T) {
	_, err := schemas.ParseHygieneLabel(hygieneLabelWire(t, func(m map[string]any) {
		m["label_id"] = "lbl_1"
	}))
	require.Error(t, err)
}

func TestHygieneLabel_RejectsUnknownKeys(t *testing.T) {
	// zod issue code 'unrecognized_keys' ⇒ Go DisallowUnknownFields; the
	// rejection outcome is preserved (message text is the stdlib's).
	_, err := schemas.ParseHygieneLabel(hygieneLabelWire(t, func(m map[string]any) {
		m["extra"] = true
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestHygieneLabel_EditDiffIsOptionalAndNonEmptyWhenPresent(t *testing.T) {
	// z.string().min(1).optional() — a timeout row legitimately carries no diff.
	data := hygieneLabelWire(t, func(m map[string]any) {
		m["source_signal"] = "timeout"
		m["label"] = "unlabeled"
		m["hygiene_rule"] = "timeout⇒unlabeled"
		delete(m, "edit_diff")
	})
	parsed, err := schemas.ParseHygieneLabel(data)
	require.NoError(t, err)
	assert.Nil(t, parsed.EditDiff)

	empty := hygieneLabelWire(t, func(m map[string]any) { m["edit_diff"] = "" })
	hygieneLabelIssueAt(t, empty, "edit_diff")
}
