package analysis_test

// Ported from packages/core/test/analysis-generated.test.ts and
// analysis-generated-feedback.test.ts (V-P11-13 + P11-T23; ARCH §4.1, §8.7,
// §8.8): the `generated-sdk` pattern → `inspect_generated_output` wiring
// end-to-end (routing record + misprediction feedback persisted via the
// recorder), the data-only no-bypass contract, the negative (non-generated
// changes never trigger the action), and the fail-closed recorder behavior.
// Deterministic fixtures — no network, no wall clock (injected), temp state
// dirs via t.TempDir.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

const (
	generatedClockInstant = "2026-10-09T06:40:00Z"
	feedbackClockInstant  = "2026-10-09T06:45:00Z"
	generatedEventID      = "01JP11T2300000000000000000"
	negativeEventID       = "01JP11T23NEG00000000000000"
)

func misStrPtr(s string) *string { return &s }

// generatedFixture is the deterministic V-P11-13 fixture (TS
// GENERATED_FIXTURE): a src/api/generated client change plus a payments
// change, and two candidate docs (only docs/api.md mentions the generated
// path stem "client").
func generatedFixture() analysis.GeneratedRoutingInput {
	return analysis.GeneratedRoutingInput{
		RunID:   "acme/payments#8a73f2c..81af92e",
		BaseSha: "8a73f2c",
		HeadSha: "81af92e",
		Files: []analysis.GeneratedRoutingFile{
			{
				DetectorFile: analysis.DetectorFile{
					Path:  "src/api/generated/client.ts",
					Patch: "@@ -1 +1 @@\n-export const VERSION = \"1\";\n+export const VERSION = \"2\";\n",
				},
				BlobSha: "b1d0g3n",
				Status:  "modified",
			},
			{
				DetectorFile: analysis.DetectorFile{
					Path:  "src/payments/refund.ts",
					Patch: "@@ -1 +1 @@\n-a\n+b\n",
				},
			},
		},
		Docs: []analysis.GeneratedRoutingDoc{
			{Path: "docs/api.md", Content: "# API\n\nThe generated client wraps refunds.\n"},
			{Path: "docs/overview.md", Content: "# Overview\n\nNothing about the SDK here.\n"},
		},
	}
}

// wireKeys marshals value and returns its sorted JSON keys (the Go analog of
// the TS `Object.keys(x).sort()`).
func routingWireKeys(t *testing.T, value any) []string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(data, &wire))
	keys := make([]string, 0, len(wire))
	for key := range wire {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestVP11_13_GeneratedSdkWiring (V-P11-13; P11-T23): a src/api/generated
// change triggers inspect_generated_output and the misprediction appears as
// feedback.
func TestVP11_13_GeneratedSdkWiring(t *testing.T) {
	// 1. Routing record: the diff touching src/api/generated produces the
	//    typed record with the analyzer action + evidence (paths, blob SHAs).
	fixture := generatedFixture()
	record := analysis.InspectGeneratedOutput(fixture)
	require.NotNil(t, record)
	assert.Equal(t, analysis.GeneratedSdkPattern, record.Pattern)
	assert.Equal(t, analysis.InspectGeneratedOutputAction, record.Action)
	require.Len(t, record.Generated, 1)
	assert.Equal(t, "src/api/generated/client.ts", record.Generated[0].Path)
	require.NotNil(t, record.Generated[0].BlobSha)
	assert.Equal(t, "b1d0g3n", *record.Generated[0].BlobSha)
	require.NotNil(t, record.Generated[0].Status)
	assert.Equal(t, "modified", *record.Generated[0].Status)
	assert.Equal(t, []string{"docs/api.md"}, record.AffectedDocs)
	assert.Equal(t, fixture.RunID, record.RunID)
	assert.Equal(t, fixture.BaseSha, record.BaseSha)
	assert.Equal(t, fixture.HeadSha, record.HeadSha)
	assert.Equal(t, analysis.InspectGeneratedOutputAction, record.Suggestion.AnalyzerAction)
	assert.Equal(t, analysis.GeneratedSdkPattern, record.Suggestion.Pattern)

	// 2. Forced misprediction: the routing predicted doc impact, the reviewer
	//    outcome contradicted it → ONE feedback event persisted via the
	//    EXISTING recorder (append-only JSONL, OD-R22), verified through BOTH
	//    the recorder replay and the sink file.
	stateDir := t.TempDir()
	options := analysis.FeedbackRecorderOptions{
		StateDir: stateDir,
		Clock:    func() string { return generatedClockInstant },
	}
	event, err := analysis.RecordAnalyzerMisprediction(analysis.AnalyzerMispredictionInput{
		DecisionID:    "d_01JZROUTINGDECISION",
		PolicyVersion: 17,
		Predicted:     "generated-sdk routing predicted doc impact",
		Actual:        "reviewer confirmed docs unaffected",
		Paths:         []string{"src/api/generated/client.ts"},
		Evidence:      []string{"b1d0g3n"},
		RunID:         misStrPtr(fixture.RunID),
		DocPath:       misStrPtr("docs/api.md"),
		Repo:          misStrPtr("acme/payments"),
		EventID:       misStrPtr(generatedEventID),
	}, options)
	require.NoError(t, err)
	assert.Equal(t, schemas.FeedbackEventSignalReject, event.Signal)
	assert.Equal(t, analysis.AnalyzerMispredictionSignal, string(event.Signal))
	assert.Equal(t, "reject", string(event.Signal))
	assert.Equal(t, generatedEventID, event.EventID)
	assert.Equal(t, generatedClockInstant, event.At)
	assert.Equal(t, "d_01JZROUTINGDECISION", event.DecisionID)
	assert.Equal(t, 17, event.PolicyVersion)
	require.NotNil(t, event.RunID)
	assert.Equal(t, fixture.RunID, *event.RunID)
	require.NotNil(t, event.DocPath)
	assert.Equal(t, "docs/api.md", *event.DocPath)
	require.NotNil(t, event.Repo)
	assert.Equal(t, "acme/payments", *event.Repo)
	require.NotNil(t, event.RejectReason)
	assert.Equal(t,
		"analyzer-misprediction (generated-sdk; ARCH §8.7); "+
			"predicted=generated-sdk routing predicted doc impact; "+
			"actual=reviewer confirmed docs unaffected; "+
			"paths=src/api/generated/client.ts; "+
			"evidence=b1d0g3n",
		*event.RejectReason)

	// Recorder replay (fail-closed parse on read).
	replay, err := analysis.ListFeedbackEvents(options)
	require.NoError(t, err)
	require.Len(t, replay, 1)
	assert.Equal(t, generatedEventID, replay[0].EventID)
	assert.Equal(t, schemas.FeedbackEventSignalReject, replay[0].Signal)
	require.NotNil(t, replay[0].RejectReason)
	assert.Equal(t, *event.RejectReason, *replay[0].RejectReason)

	// Sink file (append-only JSONL): exactly one line, same row.
	raw, err := os.ReadFile(filepath.Join(stateDir, "feedback", "events.jsonl"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 1)
	var stored map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &stored))
	var expected map[string]any
	eventJSON, err := json.Marshal(event)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(eventJSON, &expected))
	assert.Equal(t, expected, stored)
}

// TestP11_T23_DataOnlyRouting (P11-T23): the wiring is data-only — no policy
// bypass, outcome decided by the policy engine.
func TestP11_T23_DataOnlyRouting(t *testing.T) {
	record := analysis.InspectGeneratedOutput(generatedFixture())
	require.NotNil(t, record)

	// The analyzer action is NOT a member of the policy engine's CLOSED
	// decision-action vocabulary — the routing record can never be mistaken
	// for a decision; the engine decides from its own closed set
	// (ignore/review/investigate/patch).
	assert.Equal(t, []schemas.Action{
		schemas.ActionIgnore, schemas.ActionReview, schemas.ActionInvestigate, schemas.ActionPatch,
	}, schemas.ActionValues)
	actionStrings := make([]string, 0, len(schemas.ActionValues))
	for _, action := range schemas.ActionValues {
		actionStrings = append(actionStrings, string(action))
	}
	assert.NotContains(t, actionStrings, analysis.InspectGeneratedOutputAction)

	// The record carries a suggestion, NEVER an outcome: no decision/outcome
	// member exists (the key set is exactly the data-only surface), and the
	// suggestion is descriptive data only.
	assert.Equal(t, []string{
		"action", "affected_docs", "base_sha", "generated", "head_sha", "pattern", "run_id", "suggestion",
	}, routingWireKeys(t, record))
	assert.Equal(t, []string{"analyzer_action", "basis", "pattern"}, routingWireKeys(t, record.Suggestion))

	// No write path: the record is data with zero function members — nothing
	// in it can reach state, git, or the policy engine's write side. Go has no
	// Object.freeze equivalent, and Go structs cannot carry function members
	// through the JSON wire; the exhaustive key-set assertion above plus the
	// typed struct shape is the Go-observable contract.
	//
	// Deterministic: identical input → identical (deep-equal) record.
	assert.Equal(t, record, analysis.InspectGeneratedOutput(generatedFixture()))
}

// TestP11_T23_NonGeneratedNeverTriggers (P11-T23): non-generated changes never
// trigger the action — including formatting-only / test-only / mixed changes,
// even when the doc list would happily match a stem.
func TestP11_T23_NonGeneratedNeverTriggers(t *testing.T) {
	assert.Nil(t, analysis.InspectGeneratedOutput(analysis.GeneratedRoutingInput{
		RunID:   "acme/payments#a..b",
		BaseSha: "a",
		HeadSha: "b",
		Files: []analysis.GeneratedRoutingFile{
			{DetectorFile: analysis.DetectorFile{Path: "src/payments/refund.ts", Patch: "@@ -1 +1 @@\n-a\n+b\n"}},
			{DetectorFile: analysis.DetectorFile{Path: "docs/overview.md", Patch: "@@ -1 +1 @@\n-old\n+new\n"}},
		},
		Docs: []analysis.GeneratedRoutingDoc{{Path: "docs/api.md", Content: "mentions client explicitly"}},
	}))

	assert.Nil(t, analysis.InspectGeneratedOutput(analysis.GeneratedRoutingInput{
		RunID:   "acme/payments#c..d",
		BaseSha: "c",
		HeadSha: "d",
		Files: []analysis.GeneratedRoutingFile{
			{DetectorFile: analysis.DetectorFile{Path: "tests/client.test.ts", Patch: "@@ -1 +1 @@\n-x\n+x\n"}},
		},
		Docs: []analysis.GeneratedRoutingDoc{{Path: "docs/api.md", Content: "client"}},
	}))
}

// TestP11_T23_MispredictionFeedbackFailClosed (P11-T23; ARCH §8.7):
// analyzer-misprediction feedback is FAIL-CLOSED — a recorder rejection
// surfaces TYPED and nothing is dropped silently or partially persisted.
//
// Deviation note (documented per the migration conventions): the TS suite
// asserted on the FeedbackValidationError / FeedbackSinkError /
// AnalyzerMispredictionInputError CLASS identities; in Go those surface as
// docutil coded errors with the exact TS message text preserved
// (EDOCSYNC_VALIDATION / EDOCSYNC_IO / EDOCSYNC_VALIDATION respectively —
// see the code constants in generated.go).
func TestP11_T23_MispredictionFeedbackFailClosed(t *testing.T) {
	clock := func() string { return feedbackClockInstant }
	facts := func() analysis.AnalyzerMispredictionInput {
		return analysis.AnalyzerMispredictionInput{
			DecisionID:    "d_01JZMISPREDDECISION",
			PolicyVersion: 17,
			Predicted:     "generated-sdk routing predicted doc impact",
			Actual:        "reviewer confirmed docs unaffected",
			Paths:         []string{"src/api/generated/client.ts"},
		}
	}

	// 1. Fact-level gap: a misprediction without its facts is an assertion,
	//    not evidence — typed refusal BEFORE the recorder is even built, and
	//    nothing is persisted.
	emptyStateDir := t.TempDir()
	for _, broken := range []analysis.AnalyzerMispredictionInput{
		func() analysis.AnalyzerMispredictionInput { f := facts(); f.Predicted = ""; return f }(),
		func() analysis.AnalyzerMispredictionInput { f := facts(); f.Actual = ""; return f }(),
		func() analysis.AnalyzerMispredictionInput { f := facts(); f.Paths = nil; return f }(),
	} {
		_, err := analysis.RecordAnalyzerMisprediction(broken, analysis.FeedbackRecorderOptions{
			StateDir: emptyStateDir,
			Clock:    clock,
		})
		require.Error(t, err)
		assert.Equal(t, analysis.AnalyzerMispredictionInputCode, docutil.CodeOf(err))
		assert.Contains(t, err.Error(), "fail closed")
	}
	replay, err := analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{
		StateDir: emptyStateDir,
		Clock:    clock,
	})
	require.NoError(t, err)
	assert.Empty(t, replay)

	// 2. Recorder rejection (sink I/O failure): the state dir path is an
	//    existing FILE, so the real JSONL sink cannot create its directory —
	//    append fails, the one retry fails, and the error surfaces TYPED
	//    (sink error, fail loud) with nothing partially persisted.
	blockRoot := t.TempDir()
	blocker := filepath.Join(blockRoot, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o644))
	_, err = analysis.RecordAnalyzerMisprediction(facts(), analysis.FeedbackRecorderOptions{
		StateDir: blocker,
		Clock:    clock,
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "could not be appended")
	assert.Contains(t, err.Error(), "after one retry")

	// 3. Recorder rejection (config): a RELATIVE state dir is a typed refusal
	//    before any write attempt — again nothing persisted.
	_, err = analysis.RecordAnalyzerMisprediction(facts(), analysis.FeedbackRecorderOptions{
		StateDir: "relative/state",
		Clock:    clock,
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "stateDir must be an absolute path")

	// 4. Schema-level validation rejection stays typed through the same path:
	//    a malformed event id fails the recorder's fail-closed validation —
	//    surfaced, never dropped.
	invalidIDDir := t.TempDir()
	badID := facts()
	badID.EventID = misStrPtr("not-a-ulid")
	_, err = analysis.RecordAnalyzerMisprediction(badID, analysis.FeedbackRecorderOptions{
		StateDir: invalidIDDir,
		Clock:    clock,
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "fail closed")
	replay, err = analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{
		StateDir: invalidIDDir,
		Clock:    clock,
	})
	require.NoError(t, err)
	assert.Empty(t, replay)

	// 5. The happy path persists EXACTLY the validated row (nothing added,
	//    nothing dropped) once the facts and identity are sound.
	okDir := t.TempDir()
	okFacts := facts()
	okFacts.EventID = misStrPtr(negativeEventID)
	event, err := analysis.RecordAnalyzerMisprediction(okFacts, analysis.FeedbackRecorderOptions{
		StateDir: okDir,
		Clock:    clock,
	})
	require.NoError(t, err)
	assert.Equal(t, negativeEventID, event.EventID)
	replay, err = analysis.ListFeedbackEvents(analysis.FeedbackRecorderOptions{
		StateDir: okDir,
		Clock:    clock,
	})
	require.NoError(t, err)
	require.Len(t, replay, 1)
	assert.Equal(t, *event, replay[0])
}
