package schemas_test

// Ported from packages/schemas/test/run-outcome.test.ts (black-box, testify).
//
// Covers: candidate provenance evidence (P11-T04), the FROZEN
// run-outcome schema shape (P5-T15/V-P5-16), the superRefine consistency
// rules 1–3, the closed vocabularies, and the provenance-pinning /
// lifecycle fields. The TS "registry + JSON Schema coverage" describe
// block is intentionally NOT ported: it exercises the TS registry barrel
// (getSchema/exportJsonSchema), which is orchestrator-owned infrastructure
// and not part of this unit's files.
//
// Helper names are runOutcome-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ helpers */

func runOutcomeMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// runOutcomeToMap renders an example as a generic JSON map so tests can
// mutate fields the way the TS suite spreads copies ({...BASE, ...}).
func runOutcomeToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(runOutcomeMarshal(t, v), &m))
	return m
}

func runOutcomeWire(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

// runOutcomeIssueAt finds an issue whose path starts with path and whose
// message contains needle (mirrors the TS expectIssue helper).
func runOutcomeIssueAt(t *testing.T, err error, path []string, needle string) {
	t.Helper()
	require.Error(t, err)
	var ve *schemas.ValidationError
	require.ErrorAs(t, err, &ve)
	for _, iss := range ve.Issues {
		if len(iss.Path) < len(path) {
			continue
		}
		match := true
		for i, seg := range path {
			if iss.Path[i] != seg {
				match = false
				break
			}
		}
		if match && strings.Contains(iss.Message, needle) {
			return
		}
	}
	require.Failf(t, "issue not found", "expected an issue at path %v containing %q, got %v", path, needle, ve.Issues)
}

func runOutcomeParse(t *testing.T, m map[string]any) error {
	t.Helper()
	_, err := schemas.ParseRunOutcome(runOutcomeWire(t, m))
	return err
}

/* ----------------------------- candidate provenance evidence (P11-T04) */

func TestCandidateProvenanceEvidence_AcceptsLevel34PayloadsAndRecordsWithoutEvidence(t *testing.T) {
	cases := []struct {
		name string
		wire string
	}{
		{"level-3 symbol evidence", `{"level":3,"source":"claim:claim_1","evidence":{"symbols":["idempotencyKey"]}}`},
		{"level-4 locations+keys", `{"level":4,"source":"api-path-mention:refunds","evidence":{"locations":["/paths/~1refunds/get"],"keys":[]}}`},
		{"record without evidence", `{"level":1,"source":"manifest-source:src/x.ts"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p schemas.RunCandidateProvenance
			require.NoError(t, schemas.StrictDecode([]byte(tc.wire), &p))
			assert.Empty(t, p.Validate())
		})
	}
}

func TestCandidateProvenanceEvidence_RejectsUnknownShapesAndEmptySymbolLists(t *testing.T) {
	t.Run("empty symbol list", func(t *testing.T) {
		var p schemas.RunCandidateProvenance
		require.NoError(t, schemas.StrictDecode([]byte(`{"level":3,"source":"claim:claim_1","evidence":{"symbols":[]}}`), &p))
		assert.NotEmpty(t, p.Validate())
	})
	t.Run("symbols and locations mixed (closed union, strict)", func(t *testing.T) {
		var p schemas.RunCandidateProvenance
		require.NoError(t, schemas.StrictDecode([]byte(`{"level":4,"source":"claim:claim_1","evidence":{"symbols":["a"],"locations":["/x"]}}`), &p))
		assert.NotEmpty(t, p.Validate())
	})
	t.Run("unknown evidence key rejected at decode (strictObject)", func(t *testing.T) {
		var p schemas.RunCandidateProvenance
		err := schemas.StrictDecode([]byte(`{"level":4,"source":"claim:claim_1","evidence":{"unknown":true}}`), &p)
		require.Error(t, err)
	})
}

/* --------------------------------------------------------------- schema shape */

func TestRunOutcomeSchema_ValidatesTheCompletedAndTheDegradedExamples(t *testing.T) {
	cases := []struct {
		name    string
		example schemas.RunOutcome
	}{
		{"completed", schemas.RunOutcomeExample()},
		{"degraded", schemas.RunOutcomeDegradedExample()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := schemas.ParseRunOutcome(runOutcomeMarshal(t, tc.example))
			require.NoError(t, err)
			// TS: expect(result).toEqual(example)
			assert.Equal(t, tc.example, *parsed)
		})
	}
}

func TestRunOutcomeSchema_RoundTripsParsingTheExamplesIsByteStable(t *testing.T) {
	for _, example := range []schemas.RunOutcome{schemas.RunOutcomeExample(), schemas.RunOutcomeDegradedExample()} {
		first := runOutcomeMarshal(t, example)
		parsed, err := schemas.ParseRunOutcome(first)
		require.NoError(t, err)
		second := runOutcomeMarshal(t, parsed)
		// TS: expect(JSON.stringify(result)).toBe(JSON.stringify(example))
		require.Equal(t, string(first), string(second))
	}
}

func TestRunOutcomeSchema_RejectsUnknownTopLevelAndNestedKeys(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["extra_field"] = true
	err := runOutcomeParse(t, base)
	require.Error(t, err)
	// zod issue code 'unrecognized_keys' ⇒ Go DisallowUnknownFields; the
	// rejection outcome is preserved (message text is the stdlib's).
	assert.Contains(t, err.Error(), "unknown field")

	base = runOutcomeToMap(t, schemas.RunOutcomeExample())
	docs := base["documents"].([]any)
	doc := docs[0].(map[string]any)
	doc["extra_field"] = true
	err = runOutcomeParse(t, base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestRunOutcomeSchema_ReusesTheRunRecordKeyAndStateChurnShapes(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	key := base["key"].(map[string]any)
	key["extra_field"] = true
	require.Error(t, runOutcomeParse(t, base))

	base = runOutcomeToMap(t, schemas.RunOutcomeExample())
	churn := base["state_churn"].(map[string]any)
	churn["extra_field"] = true
	require.Error(t, runOutcomeParse(t, base))
}

func TestRunOutcomeSchema_ConfidenceIsBoundedAndStageLatencyIsNonnegativeIntOrNull(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["documents"].([]any)[0].(map[string]any)["confidence"] = 1.5
	require.Error(t, runOutcomeParse(t, base))

	base = runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["stages"].([]any)[0].(map[string]any)["latency_ms"] = -1
	require.Error(t, runOutcomeParse(t, base))
}

/* --------------------------------------------------------- superRefine rules */

func TestRunOutcomeSuperRefine_Rule1_CompletedWithFinishedAtNullIsRejected(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["finished_at"] = nil
	runOutcomeIssueAt(t, runOutcomeParse(t, base), []string{"finished_at"}, "requires a non-null finished_at")
}

func TestRunOutcomeSuperRefine_Rule1_IsExhaustiveToCompleted(t *testing.T) {
	degraded := runOutcomeToMap(t, schemas.RunOutcomeDegradedExample())
	degraded["finished_at"] = nil
	require.NoError(t, runOutcomeParse(t, degraded))
}

func TestRunOutcomeSuperRefine_Rule2_UnaccountedSuggestDocumentIsRejected(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["documents"] = []any{
		map[string]any{"doc_path": "docs/notes.md", "policy_outcome": "suggest", "partial_coverage": false},
	}
	runOutcomeIssueAt(t, runOutcomeParse(t, base), []string{"documents", "0"},
		"needs a verification block, an abstain record, a matching finding, or policy_outcome 'ignore'/'review'")
}

func TestRunOutcomeSuperRefine_Rule2_MatchingFindingAccountsForTheDocument(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["documents"] = []any{
		map[string]any{"doc_path": "docs/notes.md", "policy_outcome": "suggest", "partial_coverage": false},
	}
	base["findings"] = []any{
		map[string]any{"doc_path": "docs/notes.md", "kind": "potentially-stale", "detail": "writer timed out"},
	}
	require.NoError(t, runOutcomeParse(t, base))
}

func TestRunOutcomeSuperRefine_Rule2_AbstainRecordAccountsForTheDocument(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["documents"] = []any{
		map[string]any{
			"doc_path":         "docs/notes.md",
			"policy_outcome":   "suggest",
			"abstain":          map[string]any{"reason": "verifier_uncertain", "detail": "evidence attached"},
			"partial_coverage": false,
		},
	}
	require.NoError(t, runOutcomeParse(t, base))
}

func TestRunOutcomeSuperRefine_Rule2_OnlyFiresOnCompleted(t *testing.T) {
	degraded := runOutcomeToMap(t, schemas.RunOutcomeDegradedExample())
	degraded["documents"] = append(degraded["documents"].([]any),
		map[string]any{"doc_path": "docs/notes.md", "policy_outcome": "suggest", "partial_coverage": false})
	require.NoError(t, runOutcomeParse(t, degraded))
}

func TestRunOutcomeSuperRefine_Rule3_PartialCoverageWithoutCapOverflowIsRejected(t *testing.T) {
	degraded := runOutcomeToMap(t, schemas.RunOutcomeDegradedExample())
	kept := make([]any, 0, 3)
	for _, entry := range degraded["degradations"].([]any) {
		if entry.(map[string]any)["kind"] == "cap-overflow" {
			continue
		}
		kept = append(kept, entry)
	}
	degraded["degradations"] = kept
	runOutcomeIssueAt(t, runOutcomeParse(t, degraded), []string{"degradations"}, "'cap-overflow'")
}

func TestRunOutcomeSuperRefine_Rule3_PartialCoverageWithCapOverflowParses(t *testing.T) {
	parsed, err := schemas.ParseRunOutcome(runOutcomeMarshal(t, schemas.RunOutcomeDegradedExample()))
	require.NoError(t, err)
	require.NotNil(t, parsed)
}

func TestRunOutcomeSuperRefine_Rule3_FiresOnAnyStatusIncludingCompleted(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["documents"].([]any)[0].(map[string]any)["partial_coverage"] = true
	base["degradations"] = []any{}
	runOutcomeIssueAt(t, runOutcomeParse(t, base), []string{"degradations"}, "'cap-overflow'")
}

/* --------------------------------------------------------- closed vocabularies */

func TestClosedVocabularies_RunStatusIsTheFrozenCompletedDegradedFailedUnion(t *testing.T) {
	require.Equal(t, []schemas.RunStatus{"completed", "degraded", "failed"}, schemas.RunStatusValues)

	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["status"] = "aborted"
	require.Error(t, runOutcomeParse(t, base))
}

func TestClosedVocabularies_DegradationKindsIsTheFrozenMatrix(t *testing.T) {
	require.Equal(t, []schemas.DegradationKind{
		"analyzer-inconclusive",
		"jev-unavailable",
		"writer-timeout",
		"content-unavailable",
		"verification-failed",
		"provider-outage",
		"signal-sink-failed",
		"cap-overflow",
		"budget-exceeded",
		"cas-conflict-retry",
	}, schemas.DegradationKindValues)

	degraded := runOutcomeToMap(t, schemas.RunOutcomeDegradedExample())
	degraded["degradations"] = []any{
		map[string]any{"stage": "x", "kind": "other", "detail": "y"},
	}
	require.Error(t, runOutcomeParse(t, degraded))
}

func TestClosedVocabularies_PipelineOutcomeIsTheFrozenFiveValueVocabulary(t *testing.T) {
	require.Equal(t, []schemas.PipelineOutcome{"ignore", "suggest", "auto", "blocked", "review"}, schemas.PipelineOutcomeValues)

	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["documents"].([]any)[1].(map[string]any)["policy_outcome"] = "update"
	require.Error(t, runOutcomeParse(t, base))
}

func TestClosedVocabularies_RoutingMaxActionAndBaselineRunAreFrozen(t *testing.T) {
	require.Equal(t, []schemas.PipelineRouting{"IGNORE", "REVIEW", "CONTINUE"}, schemas.PipelineRoutingValues)
	require.Equal(t, []schemas.PipelineMaxAction{"review", "auto"}, schemas.PipelineMaxActionValues)
	require.Equal(t, []schemas.BaselineRun{
		"exact-sha",
		"same-tree",
		"ancestor",
		"patch-id-match",
		"orphaned-suspect",
		"none",
	}, schemas.BaselineRunValues)
}

func TestClosedVocabularies_AbstainReasonsReuseTheCanonicalDecisionTaxonomy(t *testing.T) {
	parse := func(wire string) ([]schemas.Issue, error) {
		var a schemas.RunAbstain
		if err := schemas.StrictDecode([]byte(wire), &a); err != nil {
			return nil, err
		}
		return a.Validate(), nil
	}
	for _, reason := range []string{"verifier_uncertain", "model_unavailable"} {
		issues, err := parse(`{"abstain_id":"a","doc_path":null,"reason":"` + reason + `","detail":"d"}`)
		require.NoError(t, err)
		assert.Empty(t, issues)
	}
	// low_confidence was renamed insufficient_confidence (ARCH §5.2).
	issues, err := parse(`{"abstain_id":"a","doc_path":null,"reason":"low_confidence","detail":"d"}`)
	require.NoError(t, err)
	assert.NotEmpty(t, issues)

	issues, err = parse(`{"abstain_id":"","doc_path":null,"reason":"policy_gap","detail":"d"}`)
	require.NoError(t, err)
	assert.NotEmpty(t, issues)
}

/* ------------------------------------------- D3/DX-P2-2 pinning + early signal */

func TestProvenancePinning_ModelBindingsPinRoleRoutedModelAndOptionalProvider(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["provenance"] = map[string]any{
		"policy_version": 17,
		"model_bindings": []any{map[string]any{"role": "writer", "model": "m"}},
	}
	require.NoError(t, runOutcomeParse(t, base))

	base = runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["provenance"] = map[string]any{
		"policy_version": 17,
		"model_bindings": []any{map[string]any{"role": "observer", "model": "m"}},
	}
	require.Error(t, runOutcomeParse(t, base))

	base = runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["provenance"] = map[string]any{
		"policy_version": 17,
		"model_bindings": []any{map[string]any{"role": "writer", "model": ""}},
	}
	require.Error(t, runOutcomeParse(t, base))
}

func TestEarlySignal_TimestampsAreNullableFieldsNeverGenerated(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["early_signal"] = map[string]any{"posted_at": nil, "updated_at": nil}
	require.NoError(t, runOutcomeParse(t, base))

	base = runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["early_signal"] = map[string]any{"posted_at": "not-a-timestamp", "updated_at": nil}
	require.Error(t, runOutcomeParse(t, base))
}

func TestConcurrencyGroup_IsANonEmptyStringOrNull(t *testing.T) {
	base := runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["concurrency_group"] = nil
	require.NoError(t, runOutcomeParse(t, base))

	base = runOutcomeToMap(t, schemas.RunOutcomeExample())
	base["concurrency_group"] = ""
	require.Error(t, runOutcomeParse(t, base))
}

// TestRunUsageAndVocabularyPredicates covers the optional usage block
// (P5-T17; absent from both TS examples, so no ported test exercises it)
// and the exported membership predicates of the closed vocabularies.
func TestRunUsageAndVocabularyPredicates(t *testing.T) {
	prompt := 1200
	completion := 340
	total := 1540
	cost := 0.0042
	usage := schemas.RunUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      &total,
		Cost:             &cost,
		PolicyVersion:    17,
	}
	assert.Empty(t, usage.Validate())

	bad := schemas.RunUsage{PromptTokens: -1, CompletionTokens: -1, PolicyVersion: 0}
	assert.NotEmpty(t, bad.Validate())

	badUsage := schemas.RunUsage{PromptTokens: 1, CompletionTokens: 1, PolicyVersion: 1, TotalTokens: &total, Cost: func() *float64 { n := -0.1; return &n }()}
	assert.NotEmpty(t, badUsage.Validate())

	assert.True(t, schemas.IsValidRunStatus("degraded"))
	assert.False(t, schemas.IsValidRunStatus("aborted"))
	assert.True(t, schemas.IsValidPipelineOutcome("suggest"))
	assert.False(t, schemas.IsValidPipelineOutcome("update"))
	assert.True(t, schemas.IsValidPipelineRouting("CONTINUE"))
	assert.False(t, schemas.IsValidPipelineRouting("MAYBE"))
	assert.True(t, schemas.IsValidPipelineMaxAction("auto"))
	assert.False(t, schemas.IsValidPipelineMaxAction("ignore"))
	assert.True(t, schemas.IsValidBaselineRun("patch-id-match"))
	assert.False(t, schemas.IsValidBaselineRun("guess"))
	assert.True(t, schemas.IsValidDegradationKind("cap-overflow"))
	assert.False(t, schemas.IsValidDegradationKind("meltdown"))
	assert.True(t, schemas.IsValidRunStageStatus("inconclusive"))
	assert.False(t, schemas.IsValidRunStageStatus("passing"))
}
