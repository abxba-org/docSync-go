package schemas_test

// Ported from packages/schemas/test/feedback-record.test.ts (black-box,
// testify).
//
// Covers the `docsync.feedback-record/v1` record (P6-T22): the validated
// example, the byte-stable round trip, strict unknown-key rejection
// (z.strictObject), the empty-string/datetime/policy_version surface, the
// optional-at-the-type-level decision_id/doc_path (override_ack has no routed
// decision), the optional corrected_diff/basis/PR numbers, the three
// superRefine rules (fail closed: corrected requires the stored edit diff,
// calibration-relevant signals require decision_id routing provenance,
// accepted requires decision_id), and the closed frozen vocabularies.
//
// The TS "registry + JSON Schema coverage" describe block is intentionally NOT
// ported: it exercises the TS registry barrel (getSchema/exportJsonSchema),
// which is orchestrator-owned infrastructure and not part of this unit's
// files — the same boundary the sibling run-outcome port documents.
//
// Helper names are feedbackRecord-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so shared
// generic helper names would redeclare.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ helpers */

// feedbackRecordWire renders FEEDBACK_RECORD_EXAMPLE as generic JSON, applies
// the mutation (the TS suite spreads copies: {...BASE, <field>: <bad>}) and
// re-serializes. Deleting a key models the TS `undefined` assignment.
func feedbackRecordWire(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(schemas.FeedbackRecordExample())
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

// feedbackRecordIssues strictly parses data (which MUST fail) and returns the
// zod-mirroring issues.
func feedbackRecordIssues(t *testing.T, data []byte) []schemas.Issue {
	t.Helper()
	_, err := schemas.ParseFeedbackRecord(data)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Issues
}

// feedbackRecordIssueAt asserts an issue exists whose path contains every
// segment (TS: issue.path.includes(<segment>)).
func feedbackRecordIssueAt(t *testing.T, data []byte, segment string) []schemas.Issue {
	t.Helper()
	issues := feedbackRecordIssues(t, data)
	for _, iss := range issues {
		for _, seg := range iss.Path {
			if seg == segment {
				return issues
			}
		}
	}
	require.Failf(t, "issue not found", "expected an issue on path segment %q, got %v", segment, issues)
	return nil
}

// feedbackRecordExpectIssue is the Go port of the TS expectIssue helper: the
// parse must fail with an issue whose message contains needle and whose path
// starts with path (every segment matches at its index).
func feedbackRecordExpectIssue(t *testing.T, data []byte, needle string, path ...string) {
	t.Helper()
	issues := feedbackRecordIssues(t, data)
	for _, iss := range issues {
		if !strings.Contains(iss.Message, needle) {
			continue
		}
		if len(iss.Path) < len(path) {
			continue
		}
		matched := true
		for i, seg := range path {
			if iss.Path[i] != seg {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	require.Failf(t, "issue not found", "expected an issue %q on path %v, got %v", needle, path, issues)
}

/* -------------------------------------------------------------------- tests */

func TestFeedbackRecord_ValidatesTheExample(t *testing.T) {
	parsed, err := schemas.ParseFeedbackRecord(feedbackRecordWire(t, nil))
	require.NoError(t, err)
	assert.Equal(t, schemas.FeedbackRecordExample(), *parsed)
}

func TestFeedbackRecord_ParsingTheExampleIsByteStable(t *testing.T) {
	// Marshal the struct directly (not via feedbackRecordWire's map) so key
	// order stays the TS declaration order the frozen wire format pins.
	first, err := json.Marshal(schemas.FeedbackRecordExample())
	require.NoError(t, err)
	parsed, err := schemas.ParseFeedbackRecord(first)
	require.NoError(t, err)
	second, err := json.Marshal(parsed)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
}

func TestFeedbackRecord_RejectsUnknownTopLevelKeys(t *testing.T) {
	// zod issue code 'unrecognized_keys' ⇒ Go DisallowUnknownFields; the
	// rejection outcome is preserved (message text is the stdlib's).
	_, err := schemas.ParseFeedbackRecord(feedbackRecordWire(t, func(m map[string]any) {
		m["extra_field"] = true
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestFeedbackRecord_CarriesTheLiteralSchemaTag(t *testing.T) {
	_, err := schemas.ParseFeedbackRecord(feedbackRecordWire(t, func(m map[string]any) {
		m["schema"] = "docsync.feedback-record/v2"
	}))
	require.Error(t, err)
	feedbackRecordExpectIssue(t, feedbackRecordWire(t, func(m map[string]any) {
		m["schema"] = "docsync.feedback-record/v2"
	}), `Invalid literal value, expected "docsync.feedback-record/v1"`, "schema")
}

func TestFeedbackRecord_PRNumbersMustBePositiveWhenPresent(t *testing.T) {
	// z.number().int().positive().optional() on both PR numbers.
	zero := feedbackRecordWire(t, func(m map[string]any) {
		m["source_pr_number"] = 0
		m["doc_pr_number"] = 0
	})
	feedbackRecordIssueAt(t, zero, "source_pr_number")
	feedbackRecordIssueAt(t, zero, "doc_pr_number")
}

func TestFeedbackRecord_RejectsEmptyRequiredStringsBadTimestampsAndNonPositivePolicyVersion(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value any
	}{
		{"empty record_id", "record_id", ""},
		{"bad at timestamp", "at", "not-a-timestamp"},
		{"empty run_id", "run_id", ""},
		{"zero policy_version", "policy_version", 0},
		{"negative policy_version", "policy_version", -3},
		// z.number().int().positive() — a fractional policy_version is rejected
		// even though the raw JSON number is well-formed.
		{"fractional policy_version", "policy_version", 1.5},
		{"empty detail", "detail", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schemas.ParseFeedbackRecord(feedbackRecordWire(t, func(m map[string]any) {
				m[tc.field] = tc.value
			}))
			require.Error(t, err)
		})
	}
}

func TestFeedbackRecord_DecisionIDAndDocPathAreOptionalAtTheTypeLevel(t *testing.T) {
	// 'override_ack' legitimately has no decision — optionality without
	// tripping superRefine.
	data := feedbackRecordWire(t, func(m map[string]any) {
		m["signal"] = "override_ack"
		delete(m, "decision_id")
		delete(m, "doc_path")
	})
	parsed, err := schemas.ParseFeedbackRecord(data)
	require.NoError(t, err)
	assert.Nil(t, parsed.DecisionID)
	assert.Nil(t, parsed.DocPath)
	assert.Equal(t, schemas.FeedbackSignalOverrideAck, parsed.Signal)
}

func TestFeedbackRecord_CorrectedDiffBasisAndPRNumbersAreOptionalFieldsNeverGenerated(t *testing.T) {
	minimal := feedbackRecordWire(t, func(m map[string]any) {
		m["signal"] = "override_ack"
		delete(m, "decision_id")
		delete(m, "basis")
		delete(m, "source_pr_number")
		delete(m, "doc_pr_number")
		delete(m, "metadata")
	})
	parsed, err := schemas.ParseFeedbackRecord(minimal)
	require.NoError(t, err)
	assert.Nil(t, parsed.CorrectedDiff)
	assert.Nil(t, parsed.Basis)
	assert.Nil(t, parsed.SourcePRNumber)
	assert.Nil(t, parsed.DocPRNumber)
	assert.Empty(t, parsed.Metadata)

	bad := feedbackRecordWire(t, func(m map[string]any) {
		m["signal"] = "override_ack"
		delete(m, "decision_id")
		delete(m, "basis")
		delete(m, "source_pr_number")
		delete(m, "doc_pr_number")
		delete(m, "metadata")
		m["at"] = "x"
	})
	_, err = schemas.ParseFeedbackRecord(bad)
	require.Error(t, err)
}

func TestFeedbackRecord_Rule1_CorrectedWithoutCorrectedDiffIsRejected(t *testing.T) {
	data := feedbackRecordWire(t, func(m map[string]any) {
		m["signal"] = "corrected"
		m["detail"] = "a human edit landed on top of the proposal"
		delete(m, "corrected_diff")
	})
	feedbackRecordExpectIssue(t, data, "requires a non-empty corrected_diff", "corrected_diff")
}

func TestFeedbackRecord_Rule1_CorrectedWithTheStoredEditDiffParses(t *testing.T) {
	// OD-R14 basis: the stored edit diff + the diff basis it was measured
	// against make `corrected` reproducible evidence, not an assertion.
	data := feedbackRecordWire(t, func(m map[string]any) {
		m["signal"] = "corrected"
		m["detail"] = "a human edit landed on top of the proposal"
		m["corrected_diff"] = "-old line\n+new line"
		m["basis"] = "pr_head"
	})
	parsed, err := schemas.ParseFeedbackRecord(data)
	require.NoError(t, err)
	require.NotNil(t, parsed.CorrectedDiff)
	assert.Equal(t, "-old line\n+new line", *parsed.CorrectedDiff)
	require.NotNil(t, parsed.Basis)
	assert.Equal(t, schemas.FeedbackBasisPRHead, *parsed.Basis)
}

func TestFeedbackRecord_Rule2_CalibrationSignalsRequireDecisionID(t *testing.T) {
	for _, signal := range []schemas.FeedbackSignal{
		schemas.FeedbackSignalFalsePositive,
		schemas.FeedbackSignalStyleRejected,
		schemas.FeedbackSignalUnlabeled,
		schemas.FeedbackSignalFalseNegative,
	} {
		t.Run(string(signal), func(t *testing.T) {
			data := feedbackRecordWire(t, func(m map[string]any) {
				m["signal"] = string(signal)
				delete(m, "decision_id")
			})
			feedbackRecordExpectIssue(t, data, "requires decision_id (routing provenance)", "decision_id")
		})
	}
}

func TestFeedbackRecord_Rule3_AcceptedWithoutDecisionIDIsRejected(t *testing.T) {
	data := feedbackRecordWire(t, func(m map[string]any) {
		m["signal"] = "accepted"
		delete(m, "decision_id")
	})
	feedbackRecordExpectIssue(t, data, "signal 'accepted' requires decision_id", "decision_id")
}

func TestFeedbackRecord_OverrideAckIsExempt(t *testing.T) {
	// A lifecycle record with no decision — and no corrected_diff — is valid.
	data := feedbackRecordWire(t, func(m map[string]any) {
		m["signal"] = "override_ack"
		delete(m, "decision_id")
		delete(m, "corrected_diff")
		m["detail"] = "operator acknowledged an override with no routed decision on record"
	})
	_, err := schemas.ParseFeedbackRecord(data)
	require.NoError(t, err)
}

func TestFeedbackRecord_ClosedVocabulariesArePinnedInCanonicalOrder(t *testing.T) {
	assert.Equal(t, []schemas.FeedbackSignal{
		"accepted", "corrected", "false_positive", "style_rejected",
		"unlabeled", "false_negative", "override_ack",
	}, schemas.FeedbackSignalValues)
	assert.Equal(t, []schemas.FeedbackSource{"doc_pr", "source_pr", "local"}, schemas.FeedbackSourceValues)
	assert.Equal(t, []schemas.FeedbackBasis{"review_event", "pr_head"}, schemas.FeedbackBasisValues)

	// The TS Object.isFrozen assertions have no Go counterpart: the slices are
	// the single source of truth in this package and nothing mutates them.
	// The closed unions are pinned by rejecting out-of-vocabulary values.
	_, err := schemas.ParseFeedbackRecord(feedbackRecordWire(t, func(m map[string]any) {
		m["signal"] = "ignored"
	}))
	require.Error(t, err)
	_, err = schemas.ParseFeedbackRecord(feedbackRecordWire(t, func(m map[string]any) {
		m["source"] = "ci"
	}))
	require.Error(t, err)
	_, err = schemas.ParseFeedbackRecord(feedbackRecordWire(t, func(m map[string]any) {
		m["basis"] = "working_tree"
	}))
	require.Error(t, err)
}

// feedbackRecordCanonical renders v through docutil.CanonicalJSON — the exact
// serialization state files are written with (CAS hashes match the TS
// implementation only if this stays byte-stable).
func feedbackRecordCanonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var tree any
	require.NoError(t, json.Unmarshal(b, &tree))
	canonical, err := docutil.CanonicalJSON(tree)
	require.NoError(t, err)
	return canonical
}

// TestFeedbackRecordExampleCanonicalJSONGolden pins the example as golden data
// (CONVENTIONS.md: TS *_EXAMPLE values are asserted byte-for-byte through the
// Go serializer).
func TestFeedbackRecordExampleCanonicalJSONGolden(t *testing.T) {
	assert.Equal(t,
		`{"at":"2026-10-05T09:14:00Z","basis":"pr_head","decision_id":"d_01J9ZK7Q2M",`+
			`"detail":"doc PR #84 merged without human edits within the observation window",`+
			`"doc_path":"docs/payments.md","doc_pr_number":84,"metadata":{"observation_window":"72h"},`+
			`"policy_version":17,"record_id":"fb_01J9ZM2Q7A","run_id":"run_01J9ZK6X8A",`+
			`"schema":"docsync.feedback-record/v1","signal":"accepted","source":"doc_pr",`+
			`"source_pr_number":42}`,
		feedbackRecordCanonical(t, schemas.FeedbackRecordExample()))
}

func TestFeedbackRecord_MembershipHelpersAgreeWithTheFrozenUnions(t *testing.T) {
	for _, v := range schemas.FeedbackSignalValues {
		assert.True(t, schemas.IsValidFeedbackSignal(string(v)), "signal %q", v)
	}
	assert.False(t, schemas.IsValidFeedbackSignal("ignored"))

	for _, v := range schemas.FeedbackSourceValues {
		assert.True(t, schemas.IsValidFeedbackSource(string(v)), "source %q", v)
	}
	assert.False(t, schemas.IsValidFeedbackSource("ci"))

	for _, v := range schemas.FeedbackBasisValues {
		assert.True(t, schemas.IsValidFeedbackBasis(string(v)), "basis %q", v)
	}
	assert.False(t, schemas.IsValidFeedbackBasis("working_tree"))
}

func TestFeedbackRecord_MetadataKeysAndValuesMustBeNonEmpty(t *testing.T) {
	// z.record(z.string().min(1), z.string().min(1)).
	data := feedbackRecordWire(t, func(m map[string]any) {
		m["metadata"] = map[string]any{"observation_window": "72h"}
	})
	_, err := schemas.ParseFeedbackRecord(data)
	require.NoError(t, err)

	empty := feedbackRecordWire(t, func(m map[string]any) {
		m["metadata"] = map[string]any{"observation_window": ""}
	})
	feedbackRecordIssueAt(t, empty, "metadata")
}
