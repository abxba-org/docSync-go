package schemas_test

// Ported from packages/schemas/test/feedback-event.test.ts (black-box,
// testify).
//
// Covers the `docsync.feedback-event/v1` reviewer signal (P10-T01; ARCH §8.7
// five reviewer signals; ARCH §5.4 decision identity): the validated example
// row, the literal schema tag, the closed five-signal union, a valid row for
// every signal, the two fail-closed superRefine rules (reject requires its
// reject_reason, edit requires its edit_diff — the edit diff is gold, OD-R14),
// the ULID event_id, the non-empty decision_id, UTC-only timestamps (no offset
// support), the positive policy_version, and strict unknown-key rejection
// (OD-R01).
//
// As in the TS suite, the registry/json-schema coverage is out of scope: the
// event is imported directly from the source module.
//
// Helper names are feedbackEvent-prefixed: sibling black-box test files in
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

// feedbackEventWire renders FEEDBACK_EVENT_EXAMPLE as generic JSON, applies
// the mutation (the TS suite spreads copies: {...EXAMPLE, <field>: <bad>}) and
// re-serializes. Deleting a key models the TS `undefined` assignment.
func feedbackEventWire(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(schemas.FeedbackEventExample())
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

// feedbackEventIssues strictly parses data (which MUST fail) and returns the
// zod-mirroring issues.
func feedbackEventIssues(t *testing.T, data []byte) []schemas.Issue {
	t.Helper()
	_, err := schemas.ParseFeedbackEvent(data)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Issues
}

// feedbackEventIssueAt asserts an issue exists on the path segment (TS:
// issue.path.includes(<segment>)).
func feedbackEventIssueAt(t *testing.T, data []byte, segment string) {
	t.Helper()
	issues := feedbackEventIssues(t, data)
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

func TestFeedbackEvent_ValidatesTheExampleRow(t *testing.T) {
	parsed, err := schemas.ParseFeedbackEvent(feedbackEventWire(t, nil))
	require.NoError(t, err)
	assert.Equal(t, schemas.FeedbackEventExample(), *parsed)
}

func TestFeedbackEvent_CarriesTheLiteralSchemaTag(t *testing.T) {
	require.Equal(t, "docsync.feedback-event/v1", schemas.FeedbackEventSchemaID)
	_, err := schemas.ParseFeedbackEvent(feedbackEventWire(t, func(m map[string]any) {
		m["schema"] = "docsync.feedback-event/v2"
	}))
	require.Error(t, err)
}

func TestFeedbackEvent_ExposesTheClosedFiveSignalUnion(t *testing.T) {
	assert.Equal(t, []schemas.FeedbackEventSignal{
		"accept", "edit", "reject", "mark_as_intentional", "always_ignore",
	}, schemas.FeedbackEventValues)

	// The TS Object.isFrozen assertion has no Go counterpart: FeedbackEventValues
	// is the single source of truth in this package and nothing mutates it.
	_, err := schemas.ParseFeedbackEvent(feedbackEventWire(t, func(m map[string]any) {
		m["signal"] = "maybe"
	}))
	require.Error(t, err)
}

// feedbackEventCanonical renders v through docutil.CanonicalJSON — the exact
// serialization state files are written with (CAS hashes match the TS
// implementation only if this stays byte-stable).
func feedbackEventCanonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var tree any
	require.NoError(t, json.Unmarshal(b, &tree))
	canonical, err := docutil.CanonicalJSON(tree)
	require.NoError(t, err)
	return canonical
}

// TestFeedbackEventExampleCanonicalJSONGolden pins the example as golden data
// (CONVENTIONS.md: TS *_EXAMPLE values are asserted byte-for-byte through the
// Go serializer).
func TestFeedbackEventExampleCanonicalJSONGolden(t *testing.T) {
	assert.Equal(t,
		`{"at":"2026-10-05T09:14:00Z","decision_id":"d_01J9ZK7Q2M","doc_path":"docs/payments.md",`+
			`"edit_diff":"-rate = 0.19\n+rate = 0.21","event_id":"01J9ZM2Q7AWX4B6C8D0E2F4G6H",`+
			`"policy_version":17,"repo":"acme/widgets","run_id":"run_01J9ZK6X8A",`+
			`"schema":"docsync.feedback-event/v1","signal":"edit"}`,
		feedbackEventCanonical(t, schemas.FeedbackEventExample()))
}

func TestFeedbackEvent_MembershipHelperAgreesWithTheClosedUnion(t *testing.T) {
	for _, v := range schemas.FeedbackEventValues {
		assert.True(t, schemas.IsValidFeedbackEventSignal(string(v)), "signal %q", v)
	}
	assert.False(t, schemas.IsValidFeedbackEventSignal("maybe"))
}

func TestFeedbackEvent_ParsesValidRowsForEverySignal(t *testing.T) {
	for _, signal := range schemas.FeedbackEventValues {
		t.Run(string(signal), func(t *testing.T) {
			data := feedbackEventWire(t, func(m map[string]any) {
				m["signal"] = string(signal)
				if signal == schemas.FeedbackEventSignalReject {
					m["reject_reason"] = "doc_already_correct"
				}
				if signal == schemas.FeedbackEventSignalEdit {
					m["edit_diff"] = "-rate = 0.19\n+rate = 0.21"
				}
			})
			_, err := schemas.ParseFeedbackEvent(data)
			require.NoError(t, err)
		})
	}
}

func TestFeedbackEvent_RefusesARejectWithoutRejectReason(t *testing.T) {
	data := feedbackEventWire(t, func(m map[string]any) {
		m["signal"] = "reject"
		delete(m, "edit_diff")
	})
	feedbackEventIssueAt(t, data, "reject_reason")
}

func TestFeedbackEvent_RefusesAnEditWithoutEditDiff(t *testing.T) {
	// The edit diff is gold (OD-R14).
	data := feedbackEventWire(t, func(m map[string]any) {
		m["signal"] = "edit"
		delete(m, "edit_diff")
	})
	feedbackEventIssueAt(t, data, "edit_diff")
}

func TestFeedbackEvent_RefusesANonULIDEventID(t *testing.T) {
	data := feedbackEventWire(t, func(m map[string]any) { m["event_id"] = "evt_1" })
	feedbackEventIssueAt(t, data, "event_id")
}

func TestFeedbackEvent_RefusesAnEmptyDecisionID(t *testing.T) {
	// ARCH §5.4 decision identity.
	_, err := schemas.ParseFeedbackEvent(feedbackEventWire(t, func(m map[string]any) {
		m["decision_id"] = ""
	}))
	require.Error(t, err)
}

func TestFeedbackEvent_RefusesANonUTCOrOffsetTimestamp(t *testing.T) {
	for _, at := range []string{"not-a-date", "2026-10-05T09:14:00+02:00"} {
		_, err := schemas.ParseFeedbackEvent(feedbackEventWire(t, func(m map[string]any) {
			m["at"] = at
		}))
		require.Error(t, err, "at = %q must be rejected", at)
	}
}

func TestFeedbackEvent_RefusesANonPositivePolicyVersion(t *testing.T) {
	_, err := schemas.ParseFeedbackEvent(feedbackEventWire(t, func(m map[string]any) {
		m["policy_version"] = 0
	}))
	require.Error(t, err)
}

func TestFeedbackEvent_RejectsUnknownKeys(t *testing.T) {
	// zod issue code 'unrecognized_keys' ⇒ Go DisallowUnknownFields; the
	// rejection outcome is preserved (message text is the stdlib's).
	_, err := schemas.ParseFeedbackEvent(feedbackEventWire(t, func(m map[string]any) {
		m["extra"] = true
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestFeedbackEvent_SuperRefineMessagesAreExact(t *testing.T) {
	// The TS messages are reproduced verbatim: they are what the Phase 10
	// feedback-loop operators read when a capture fails closed.
	reject := feedbackEventWire(t, func(m map[string]any) {
		m["signal"] = "reject"
		delete(m, "edit_diff")
	})
	assert.Contains(t, feedbackEventError(t, reject).Error(), "signal 'reject' requires a non-empty reject_reason")

	edit := feedbackEventWire(t, func(m map[string]any) {
		m["signal"] = "edit"
		delete(m, "edit_diff")
	})
	assert.Contains(t, feedbackEventError(t, edit).Error(),
		"signal 'edit' requires a non-empty edit_diff (the edit diff is gold)")
}

// feedbackEventError parses data (which MUST fail) and returns the error.
func feedbackEventError(t *testing.T, data []byte) error {
	t.Helper()
	_, err := schemas.ParseFeedbackEvent(data)
	require.Error(t, err)
	return err
}
