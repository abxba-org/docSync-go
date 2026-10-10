package schemas_test

// Ported from packages/schemas/test/drift-report.test.ts and
// packages/schemas/test/drift-finding.test.ts, plus golden tests for the
// sibling artifacts ported in the same unit (coverage-report.ts,
// org-drift-report.ts) which have no TS test file of their own.
//
// The TS suites assert, per schema: the registered example validates, the
// schema is strict (unknown keys are rejected — fail closed, PRIN-2/INV-11),
// and the required provenance keys (the D12 ladder context, `applied`, the
// evidence triple, the audit trigger) are mandatory. Those assertions map
// onto the Go strict parser (StrictDecode + requireRootKeys presence check +
// Validate).
//
// Black-box: only exported API. Helper names are drift-prefixed: sibling
// black-box test files in package schemas_test are written concurrently by
// other delegates, so shared generic helper names would redeclare.

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// driftJSON marshals v to JSON bytes (test helper; distinct from the sibling
// test files' helpers in this package).
func driftJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

// driftWire marshals v, decodes it into a generic tree, applies mutate, and
// returns the re-encoded JSON — the Go analogue of the TS object-spread
// variants (`{ ...example, extra: true }`, `delete copy.key`).
func driftWire(t *testing.T, v any, mutate func(tree map[string]any)) []byte {
	t.Helper()
	var tree map[string]any
	require.NoError(t, json.Unmarshal(driftJSON(t, v), &tree))
	mutate(tree)
	return driftJSON(t, tree)
}

// driftParseIssues strictly parses data with parse and returns the validation
// issues it produced (the parse must fail).
func driftParseIssues[T any](t *testing.T, data []byte, parse func([]byte) (T, error)) []schemas.Issue {
	t.Helper()
	_, err := parse(data)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Issues
}

// driftFind reports whether issues carries the exact zod-issue
// path/message pair.
func driftFind(issues []schemas.Issue, path []string, message string) bool {
	for _, iss := range issues {
		if assert.ObjectsAreEqual(path, iss.Path) && iss.Message == message {
			return true
		}
	}
	return false
}

// driftKeys returns the root object's keys as a set.
func driftKeys(t *testing.T, data []byte) []string {
	t.Helper()
	var tree map[string]any
	require.NoError(t, json.Unmarshal(data, &tree))
	keys := make([]string, 0, len(tree))
	for k := range tree {
		keys = append(keys, k)
	}
	return keys
}

/* --------------------------------------- docsync.drift-report/v1 (P11-T16) */

func TestDriftReportExampleValidates(t *testing.T) {
	// "is registered and its example validates" (registry.test.ts convention).
	require.Empty(t, schemas.DriftReportExample.Validate())

	parsed, err := schemas.ParseDriftReport(driftJSON(t, schemas.DriftReportExample))
	require.NoError(t, err)
	assert.Equal(t, schemas.DriftReportSchemaID, parsed.Schema)
	assert.Equal(t, "run_01J9ZK6X8A", parsed.RunID)
	assert.Equal(t, schemas.LadderRunAncestor, parsed.LadderRun)
}

func TestDriftReportExampleRoundTripsByteStable(t *testing.T) {
	first := driftJSON(t, schemas.DriftReportExample)

	var decoded schemas.DriftReport
	require.NoError(t, schemas.StrictDecode(first, &decoded))
	second := driftJSON(t, decoded)

	assert.JSONEq(t, string(first), string(second))
	assert.Equal(t, string(first), string(second), "wire key order must stay the zod declaration order")
	require.Empty(t, decoded.Validate())
}

func TestDriftReportExampleKeepsTSJSONKeys(t *testing.T) {
	assert.ElementsMatch(t, []string{
		"schema", "run_id", "at", "head", "baseline_commit", "ladder_run",
		"ladder_evidence", "doc_path", "sources", "symbols", "claims", "summary",
	}, driftKeys(t, driftJSON(t, schemas.DriftReportExample)))
}

func TestDriftReportIsStrictUnknownKeysRejected(t *testing.T) {
	// "is strict — unknown keys are rejected (fail closed)":
	// blanket_scan: true (PRIN-2: a blanket-scan flag must never validate).
	mutated := driftWire(t, schemas.DriftReportExample, func(tree map[string]any) {
		tree["blanket_scan"] = true
	})
	_, err := schemas.ParseDriftReport(mutated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestDriftReportRequiresLadderContext(t *testing.T) {
	// "requires the D12 ladder context on every report".
	mutated := driftWire(t, schemas.DriftReportExample, func(tree map[string]any) {
		delete(tree, "ladder_run")
	})
	issues := driftParseIssues(t, mutated, schemas.ParseDriftReport)
	assert.True(t, driftFind(issues, []string{"ladder_run"}, "Required"),
		"expected the ladder_run Required issue, got %v", issues)
}

func TestDriftReportRejectsInvalidLadderRung(t *testing.T) {
	mutated := driftWire(t, schemas.DriftReportExample, func(tree map[string]any) {
		tree["ladder_run"] = "guesswork"
	})
	issues := driftParseIssues(t, mutated, schemas.ParseDriftReport)
	assert.True(t, driftFind(issues, []string{"ladder_run"},
		"Invalid enum value. Expected 'exact-sha' | 'same-tree' | 'ancestor' | 'patch-id-match' | 'orphaned-suspect', received 'guesswork'"),
		"expected the ladder_run enum issue, got %v", issues)
}

func TestDriftReportNullableNumstatCountsAndBlobSHAs(t *testing.T) {
	// additions/deletions are absent for unchanged files; source_blob_sha_at_head
	// is required-but-nullable (z.string().nullable() — no omitempty on the wire).
	first := driftJSON(t, schemas.DriftReportExample)
	assert.Contains(t, string(first), `"source_blob_sha_at_head":"b7c2d3e"`)
	assert.NotContains(t, string(first), `"path":"src/api/payments/client.ts","status":"unchanged","additions"`)

	// A null head blob sha is accepted (the recorded blob no longer exists at head).
	nulled := driftWire(t, schemas.DriftReportExample, func(tree map[string]any) {
		claims := tree["claims"].([]any)
		claims[0].(map[string]any)["source_blob_sha_at_head"] = nil
	})
	parsed, err := schemas.ParseDriftReport(nulled)
	require.NoError(t, err)
	assert.Nil(t, parsed.Claims[0].SourceBlobSHAAtHead)

	// Negative numstat counts fail closed.
	bad := driftWire(t, schemas.DriftReportExample, func(tree map[string]any) {
		sources := tree["sources"].([]any)
		sources[0].(map[string]any)["additions"] = -2
	})
	issues := driftParseIssues(t, bad, schemas.ParseDriftReport)
	assert.True(t, driftFind(issues, []string{"sources", "0", "additions"},
		"Number must be greater than or equal to 0"),
		"expected the additions min(0) issue, got %v", issues)
}

/* --------------------------------- docsync.claim-support-report/v1 (P11-T17) */

func TestClaimSupportReportExampleValidates(t *testing.T) {
	require.Empty(t, schemas.ClaimSupportReportExample.Validate())

	parsed, err := schemas.ParseClaimSupportReport(driftJSON(t, schemas.ClaimSupportReportExample))
	require.NoError(t, err)
	assert.Equal(t, schemas.ClaimSupportReportSchemaID, parsed.Schema)
	assert.False(t, parsed.Applied, "the example is report-only (applied: false)")
	require.Len(t, parsed.Claims, 2)
	assert.Equal(t, schemas.ClaimSupportLostSupport, parsed.Claims[0].Verdict)
}

func TestClaimSupportReportExampleRoundTripsByteStable(t *testing.T) {
	first := driftJSON(t, schemas.ClaimSupportReportExample)

	var decoded schemas.ClaimSupportReport
	require.NoError(t, schemas.StrictDecode(first, &decoded))
	second := driftJSON(t, decoded)

	assert.JSONEq(t, string(first), string(second))
	assert.Equal(t, string(first), string(second))
	require.Empty(t, decoded.Validate())
}

func TestClaimSupportReportIsStrictUnknownKeysRejected(t *testing.T) {
	// silent_write: true (the sweep is a QUERY — such a field must never validate).
	mutated := driftWire(t, schemas.ClaimSupportReportExample, func(tree map[string]any) {
		tree["silent_write"] = true
	})
	_, err := schemas.ParseClaimSupportReport(mutated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestClaimSupportReportRecordsReportOnlyDisciplineViaApplied(t *testing.T) {
	// "records the report-only discipline explicitly via `applied`".
	mutated := driftWire(t, schemas.ClaimSupportReportExample, func(tree map[string]any) {
		delete(tree, "applied")
	})
	issues := driftParseIssues(t, mutated, schemas.ParseClaimSupportReport)
	assert.True(t, driftFind(issues, []string{"applied"}, "Required"),
		"expected the applied Required issue, got %v", issues)
}

func TestClaimSupportReportAcceptsAppliedFlipsAndSurfacedConflicts(t *testing.T) {
	// The sweep may opt into state flips: every transition goes through the
	// claim state machine's CAS functions and every CAS conflict is surfaced
	// (never a silent write; R15).
	applied := driftWire(t, schemas.ClaimSupportReportExample, func(tree map[string]any) {
		tree["applied"] = true
		tree["flips"] = []any{map[string]any{
			"claim_id":   "claim_182",
			"from":       "verified",
			"to":         "suspect",
			"transition": "loseSupport",
			"event":      "claim_lost_support",
			"hash":       "sha256:9f86d081884c7d65",
		}}
		tree["conflicts"] = []any{map[string]any{
			"claim_id":      "claim_190",
			"transition":    "markUnanchored",
			"expected_hash": nil,
			"actual_hash":   "sha256:2c26b46b68ffc68f",
		}}
	})
	parsed, err := schemas.ParseClaimSupportReport(applied)
	require.NoError(t, err)
	assert.True(t, parsed.Applied)
	require.Len(t, parsed.Flips, 1)
	assert.Equal(t, schemas.ClaimSupportTransitionLoseSupport, parsed.Flips[0].Transition)
	assert.Equal(t, "verified", parsed.Flips[0].From)
	assert.Equal(t, "suspect", parsed.Flips[0].To)
	require.Len(t, parsed.Conflicts, 1)
	assert.Equal(t, schemas.ClaimSupportTransitionMarkUnanchored, parsed.Conflicts[0].Transition)
	assert.Nil(t, parsed.Conflicts[0].ExpectedHash)
	require.NotNil(t, parsed.Conflicts[0].ActualHash)

	badFlip := driftWire(t, schemas.ClaimSupportReportExample, func(tree map[string]any) {
		tree["flips"] = []any{map[string]any{
			"claim_id":   "claim_182",
			"from":       "verified",
			"to":         "invented-status",
			"transition": "loseSupport",
			"event":      "claim_lost_support",
			"hash":       "sha256:9f86d081884c7d65",
		}}
	})
	issues := driftParseIssues(t, badFlip, schemas.ParseClaimSupportReport)
	assert.True(t, driftFind(issues, []string{"flips", "0", "to"},
		"Invalid enum value. Expected 'verified' | 'suspect' | 'unanchored' | 'stale' | 'closed', received 'invented-status'"),
		"expected the flips.0.to claim-status enum issue, got %v", issues)

	badConflict := driftWire(t, schemas.ClaimSupportReportExample, func(tree map[string]any) {
		tree["conflicts"] = []any{map[string]any{
			"claim_id":      "claim_190",
			"transition":    "forceWrite",
			"expected_hash": nil,
			"actual_hash":   nil,
		}}
	})
	issues = driftParseIssues(t, badConflict, schemas.ParseClaimSupportReport)
	assert.True(t, driftFind(issues, []string{"conflicts", "0", "transition"},
		"Invalid enum value. Expected 'loseSupport' | 'markUnanchored', received 'forceWrite'"),
		"expected the conflicts.0.transition enum issue, got %v", issues)
}

/* ----------------------------------------- docsync.drift-finding/v1 (P11-T20) */

func TestDriftFindingExampleValidates(t *testing.T) {
	require.Empty(t, schemas.DriftFindingExample.Validate())

	parsed, err := schemas.ParseDriftFindingRecord(driftJSON(t, schemas.DriftFindingExample))
	require.NoError(t, err)
	assert.Equal(t, schemas.DriftFindingClaimLostSupport, parsed.Kind)
	require.NotNil(t, parsed.Confidence)
	assert.InDelta(t, 0.9, *parsed.Confidence, 1e-9)
}

func TestDriftFindingIsStrictUnknownKeysRejected(t *testing.T) {
	// A write instruction must never validate (Loop B never auto-applies).
	mutated := driftWire(t, schemas.DriftFindingExample, func(tree map[string]any) {
		tree["patch"] = "--- a/docs/payments.md\n+++ b/docs/payments.md"
	})
	_, err := schemas.ParseDriftFindingRecord(mutated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestDriftFindingCarriesEvidenceTripleMandatorily(t *testing.T) {
	mutated := driftWire(t, schemas.DriftFindingExample, func(tree map[string]any) {
		delete(tree, "evidence")
	})
	issues := driftParseIssues(t, mutated, schemas.ParseDriftFindingRecord)
	assert.True(t, driftFind(issues, []string{"evidence"}, "Required"),
		"expected the evidence Required issue, got %v", issues)
}

func TestDriftFindingBoundsConfidenceToZeroToOne(t *testing.T) {
	over := driftWire(t, schemas.DriftFindingExample, func(tree map[string]any) {
		tree["confidence"] = 1.5
	})
	issues := driftParseIssues(t, over, schemas.ParseDriftFindingRecord)
	assert.True(t, driftFind(issues, []string{"confidence"}, "Number must be less than or equal to 1"),
		"expected the confidence max issue, got %v", issues)

	under := driftWire(t, schemas.DriftFindingExample, func(tree map[string]any) {
		tree["confidence"] = -0.1
	})
	issues = driftParseIssues(t, under, schemas.ParseDriftFindingRecord)
	assert.True(t, driftFind(issues, []string{"confidence"}, "Number must be greater than or equal to 0"),
		"expected the confidence min issue, got %v", issues)
}

func TestDriftFindingUsesAClosedDriftKindVocabulary(t *testing.T) {
	mystery := driftWire(t, schemas.DriftFindingExample, func(tree map[string]any) {
		tree["kind"] = "mystery-kind"
	})
	_, err := schemas.ParseDriftFindingRecord(mystery)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid enum value")

	for _, kind := range schemas.DriftFindingKindValues {
		withKind := driftWire(t, schemas.DriftFindingExample, func(tree map[string]any) {
			tree["kind"] = string(kind)
		})
		parsed, err := schemas.ParseDriftFindingRecord(withKind)
		require.NoError(t, err, "kind %q must validate", kind)
		assert.Equal(t, schemas.DriftFindingKind(kind), parsed.Kind)
	}
}

func TestDriftFindingClaimIDIsOptional(t *testing.T) {
	// claim_id is the dedup identity (ARCH §7.2) but optional on the wire.
	withoutClaimID := driftWire(t, schemas.DriftFindingExample, func(tree map[string]any) {
		delete(tree, "claim_id")
	})
	parsed, err := schemas.ParseDriftFindingRecord(withoutClaimID)
	require.NoError(t, err)
	assert.Nil(t, parsed.ClaimID)
}

/* ------------------------------- docsync.drift-audit-run-record/v1 (P11-T20) */

func TestDriftAuditRunRecordExampleValidates(t *testing.T) {
	require.Empty(t, schemas.DriftAuditRunRecordExample.Validate())

	parsed, err := schemas.ParseDriftAuditRunRecord(driftJSON(t, schemas.DriftAuditRunRecordExample))
	require.NoError(t, err)
	assert.Equal(t, schemas.DriftAuditRunRecordSchemaID, parsed.Schema)
	assert.Equal(t, schemas.DriftAuditTriggerNightlyScheduled, parsed.Trigger)
	assert.Equal(t, schemas.DriftAuditRunCompleted, parsed.Status)
	assert.Nil(t, parsed.CursorBefore)
	require.NotNil(t, parsed.CursorAfter)
	require.Len(t, parsed.Findings, 1)
	assert.Equal(t, schemas.DriftFindingExample, parsed.Findings[0])
}

func TestDriftAuditRunRecordIsStrictUnknownKeysRejected(t *testing.T) {
	// auto_apply: true — Loop B never auto-applies.
	mutated := driftWire(t, schemas.DriftAuditRunRecordExample, func(tree map[string]any) {
		tree["auto_apply"] = true
	})
	_, err := schemas.ParseDriftAuditRunRecord(mutated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestDriftAuditRunRecordRequiresTriggerProvenance(t *testing.T) {
	mutated := driftWire(t, schemas.DriftAuditRunRecordExample, func(tree map[string]any) {
		delete(tree, "trigger")
	})
	issues := driftParseIssues(t, mutated, schemas.ParseDriftAuditRunRecord)
	assert.True(t, driftFind(issues, []string{"trigger"}, "Required"),
		"expected the trigger Required issue, got %v", issues)
}

/* ---------------------- docsync.behavioral-proxy-coverage-report/v1 (P11-T22) */

func TestCoverageReportExampleGoldenRoundTrip(t *testing.T) {
	// Golden data: BEHAVIORAL_PROXY_COVERAGE_REPORT_EXAMPLE round-trips
	// byte-for-byte through the Go serializer (CONVENTIONS.md "Golden data").
	first := driftJSON(t, schemas.BehavioralProxyCoverageReportExample)

	var decoded schemas.BehavioralProxyCoverageReport
	require.NoError(t, schemas.StrictDecode(first, &decoded))
	second := driftJSON(t, decoded)

	assert.JSONEq(t, string(first), string(second))
	assert.Equal(t, string(first), string(second))
	require.Empty(t, decoded.Validate())

	assert.ElementsMatch(t, []string{
		"schema", "corpus_id", "at", "recall_floor", "data_source", "languages",
		"cases", "summary", "verdict", "verdict_reason",
	}, driftKeys(t, first))

	assert.Equal(t, schemas.BehavioralProxyCoverageReportSchemaID, decoded.Schema)
	assert.Equal(t, "fixtures/coverage/a5-pilot", decoded.CorpusID)
	assert.InDelta(t, 0.5, decoded.RecallFloor, 1e-9)
	assert.Equal(t, schemas.AuditDataSourceFixture, decoded.DataSource)
	assert.Equal(t, schemas.AuditVerdictPass, decoded.Verdict)
	assert.Equal(t, "all language recalls (min 1) meet the recall floor 0.5 with no declared gaps",
		decoded.VerdictReason)
	require.Len(t, decoded.Languages, 2)
	require.Len(t, decoded.Cases, 4)
	assert.Equal(t, schemas.AuditSummary{Cases: 4, BehaviorRelevant: 3, ProxyHits: 3}, decoded.Summary)
}

func TestCoverageReportIsStrictUnknownKeysRejected(t *testing.T) {
	mutated := driftWire(t, schemas.BehavioralProxyCoverageReportExample, func(tree map[string]any) {
		tree["llm_judged"] = true
	})
	_, err := schemas.ParseBehavioralProxyCoverageReport(mutated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestCoverageReportRejectsOutOfRangeMetricsAndEnums(t *testing.T) {
	badRecall := driftWire(t, schemas.BehavioralProxyCoverageReportExample, func(tree map[string]any) {
		languages := tree["languages"].([]any)
		languages[0].(map[string]any)["recall"] = 1.5
	})
	issues := driftParseIssues(t, badRecall, schemas.ParseBehavioralProxyCoverageReport)
	assert.True(t, driftFind(issues, []string{"languages", "0", "recall"},
		"Number must be less than or equal to 1"),
		"expected the recall max issue, got %v", issues)

	badFloor := driftWire(t, schemas.BehavioralProxyCoverageReportExample, func(tree map[string]any) {
		tree["recall_floor"] = -0.5
	})
	issues = driftParseIssues(t, badFloor, schemas.ParseBehavioralProxyCoverageReport)
	assert.True(t, driftFind(issues, []string{"recall_floor"},
		"Number must be greater than or equal to 0"),
		"expected the recall_floor min issue, got %v", issues)

	badProxy := driftWire(t, schemas.BehavioralProxyCoverageReportExample, func(tree map[string]any) {
		cases := tree["cases"].([]any)
		cases[0].(map[string]any)["fired_proxies"] = []any{"vibes"}
	})
	issues = driftParseIssues(t, badProxy, schemas.ParseBehavioralProxyCoverageReport)
	assert.True(t, driftFind(issues, []string{"cases", "0", "fired_proxies", "0"},
		"Invalid enum value. Expected 'signature' | 'type' | 'visibility' | 'api-surface' | 'config', received 'vibes'"),
		"expected the fired_proxies enum issue, got %v", issues)

	badOutcome := driftWire(t, schemas.BehavioralProxyCoverageReportExample, func(tree map[string]any) {
		cases := tree["cases"].([]any)
		cases[3].(map[string]any)["outcome"] = "maybe"
	})
	issues = driftParseIssues(t, badOutcome, schemas.ParseBehavioralProxyCoverageReport)
	assert.True(t, driftFind(issues, []string{"cases", "3", "outcome"},
		"Invalid enum value. Expected 'proxy-hit' | 'proxy-miss' | 'false-positive' | 'true-non-behavioral' | 'review', received 'maybe'"),
		"expected the outcome enum issue, got %v", issues)
}

func TestCoverageReportVocabularySetsAreClosedAndOrdered(t *testing.T) {
	assert.Equal(t, []schemas.ProxyKind{
		schemas.ProxyKindSignature, schemas.ProxyKindType, schemas.ProxyKindVisibility,
		schemas.ProxyKindAPISurface, schemas.ProxyKindConfig,
	}, schemas.ProxyKindValues)
	assert.Equal(t, []schemas.BehavioralProxyTruth{
		schemas.ProxyTruthBehavioral, schemas.ProxyTruthNonBehavioral, schemas.ProxyTruthUncertain,
	}, schemas.BehavioralProxyTruthValues)
	assert.Equal(t, []schemas.AuditOutcome{
		schemas.AuditOutcomeProxyHit, schemas.AuditOutcomeProxyMiss, schemas.AuditOutcomeFalsePositive,
		schemas.AuditOutcomeTrueNonBehavioral, schemas.AuditOutcomeReview,
	}, schemas.AuditOutcomeValues)
	assert.Equal(t, []schemas.AuditVerdict{
		schemas.AuditVerdictPass, schemas.AuditVerdictPassWithDeclaredGaps, schemas.AuditVerdictFail,
	}, schemas.AuditVerdictValues)
	assert.Equal(t, []schemas.AuditDataSource{
		schemas.AuditDataSourceFixture, schemas.AuditDataSourceRepository,
	}, schemas.AuditDataSourceValues)
}

func TestCoverageReportDeclaresRecallGapsExplicitly(t *testing.T) {
	// A behaviorally-relevant case no proxy detected is a DECLARED gap —
	// never silent (D14); uncertain truths route REVIEW and are excluded
	// from the recall math by construction.
	withGap := driftWire(t, schemas.BehavioralProxyCoverageReportExample, func(tree map[string]any) {
		languages := tree["languages"].([]any)
		typescript := languages[0].(map[string]any)
		typescript["declared_gaps"] = []any{map[string]any{
			"case_id": "case_rename",
			"detail":  "rename-only delta: no deterministic proxy fired",
		}}
		typescript["review_routed"] = []any{"case_rename"}
		cases := tree["cases"].([]any)
		cases = append(cases, map[string]any{
			"case_id":       "case_rename",
			"language":      "typescript",
			"truth":         "uncertain",
			"proxy_fired":   false,
			"fired_proxies": []any{},
			"outcome":       "review",
			"review_routed": true,
		})
		tree["cases"] = cases
		summary := tree["summary"].(map[string]any)
		summary["cases"] = 5
		summary["declared_gaps"] = 1
		summary["review_routed"] = 1
	})
	parsed, err := schemas.ParseBehavioralProxyCoverageReport(withGap)
	require.NoError(t, err)
	require.Len(t, parsed.Languages[0].DeclaredGaps, 1)
	assert.Equal(t, "case_rename", parsed.Languages[0].DeclaredGaps[0].CaseID)
	assert.Equal(t, []string{"case_rename"}, parsed.Languages[0].ReviewRouted)
	require.Len(t, parsed.Cases, 5)
	assert.Equal(t, schemas.AuditOutcomeReview, parsed.Cases[4].Outcome)
	assert.Equal(t, schemas.ProxyTruthUncertain, parsed.Cases[4].Truth)

	silentGap := driftWire(t, schemas.BehavioralProxyCoverageReportExample, func(tree map[string]any) {
		languages := tree["languages"].([]any)
		languages[0].(map[string]any)["declared_gaps"] = []any{map[string]any{
			"case_id": "case_rename",
			"detail":  "",
		}}
	})
	issues := driftParseIssues(t, silentGap, schemas.ParseBehavioralProxyCoverageReport)
	assert.True(t, driftFind(issues, []string{"languages", "0", "declared_gaps", "0", "detail"},
		"String must contain at least 1 character(s)"),
		"expected the declared gap detail min(1) issue, got %v", issues)

	unrouted := driftWire(t, schemas.BehavioralProxyCoverageReportExample, func(tree map[string]any) {
		languages := tree["languages"].([]any)
		languages[0].(map[string]any)["review_routed"] = []any{""}
	})
	issues = driftParseIssues(t, unrouted, schemas.ParseBehavioralProxyCoverageReport)
	assert.True(t, driftFind(issues, []string{"languages", "0", "review_routed", "0"},
		"String must contain at least 1 character(s)"),
		"expected the review_routed entry min(1) issue, got %v", issues)
}

func TestDriftVocabularyPredicatesReportMembership(t *testing.T) {
	assert.True(t, schemas.IsValidLadderRun("patch-id-match"))
	assert.False(t, schemas.IsValidLadderRun("guesswork"))
	assert.True(t, schemas.IsValidDriftSourceStatus("unchanged"))
	assert.False(t, schemas.IsValidDriftSourceStatus("added-but-not-really"))
	assert.True(t, schemas.IsValidDriftSymbolFactKind("symbol-absent"))
	assert.False(t, schemas.IsValidDriftSymbolFactKind("symbol-maybe"))
	assert.True(t, schemas.IsValidDriftClaimVerdict("support-moved"))
	assert.False(t, schemas.IsValidDriftClaimVerdict("support-guessed"))
	assert.True(t, schemas.IsValidClaimSupportVerdict("unresolvable-anchor"))
	assert.False(t, schemas.IsValidClaimSupportVerdict("maybe-supported"))
	assert.True(t, schemas.IsValidClaimSupportAnchorResolution("re-anchored"))
	assert.False(t, schemas.IsValidClaimSupportAnchorResolution("re-anchored-silently"))
	assert.True(t, schemas.IsValidClaimSupportTransition("markUnanchored"))
	assert.False(t, schemas.IsValidClaimSupportTransition("forceWrite"))
	assert.True(t, schemas.IsValidDriftFindingKind("claim-unanchored"))
	assert.False(t, schemas.IsValidDriftFindingKind("claim-unknown"))
	assert.True(t, schemas.IsValidDriftAuditTrigger("post-merge-default-branch"))
	assert.False(t, schemas.IsValidDriftAuditTrigger("pr-critical-path"))
	assert.True(t, schemas.IsValidDriftAuditRunStatus("re-run-required"))
	assert.False(t, schemas.IsValidDriftAuditRunStatus("finished"))
	assert.True(t, schemas.IsValidProxyKind("api-surface"))
	assert.False(t, schemas.IsValidProxyKind("telepathy"))
	assert.True(t, schemas.IsValidBehavioralProxyTruth("non-behavioral"))
	assert.False(t, schemas.IsValidBehavioralProxyTruth("behavioral-maybe"))
	assert.True(t, schemas.IsValidAuditOutcome("true-non-behavioral"))
	assert.False(t, schemas.IsValidAuditOutcome("sort-of"))
	assert.True(t, schemas.IsValidAuditVerdict("pass-with-declared-gaps"))
	assert.False(t, schemas.IsValidAuditVerdict("mostly-pass"))
	assert.True(t, schemas.IsValidAuditDataSource("repository"))
	assert.False(t, schemas.IsValidAuditDataSource("vibes"))
}

/* ------------------------------------------ docsync.org-drift-report/v1 (P12-T15) */

func TestOrgDriftReportExampleGoldenRoundTrip(t *testing.T) {
	// Golden data: ORG_DRIFT_REPORT_EXAMPLE round-trips byte-for-byte through
	// the Go serializer (CONVENTIONS.md "Golden data").
	first := driftJSON(t, schemas.OrgDriftReportExample)

	var decoded schemas.OrgDriftReport
	require.NoError(t, schemas.StrictDecode(first, &decoded))
	second := driftJSON(t, decoded)

	assert.JSONEq(t, string(first), string(second))
	assert.Equal(t, string(first), string(second))
	require.Empty(t, decoded.Validate())

	assert.ElementsMatch(t, []string{
		"schema", "run_id", "org", "generated_at", "period", "repos",
		"cross_repo_concepts", "summary", "advisory",
	}, driftKeys(t, first))

	assert.Equal(t, schemas.OrgDriftReportSchemaID, decoded.Schema)
	assert.Equal(t, "run_org_01J9ZMAP", decoded.RunID)
	assert.Equal(t, "acme", decoded.Org)
	assert.True(t, decoded.Advisory, "advisory-only is a schema-level invariant (INV-11)")
	require.NotNil(t, decoded.Period)
	assert.Equal(t, "2026-10-02T03:00:00Z", decoded.Period.From)
	assert.Equal(t, "2026-10-09T03:00:00Z", decoded.Period.To)
	require.Len(t, decoded.Repos, 2)
	assert.Equal(t, "acme/widgets", decoded.Repos[0].Repo)
	assert.Equal(t, 2, decoded.Repos[0].FindingsCount)
	require.Len(t, decoded.CrossRepoConcepts, 1)
	assert.Equal(t, []string{"acme/payments", "acme/widgets"}, decoded.CrossRepoConcepts[0].Repos)
	assert.Equal(t, schemas.OrgDriftSummary{Repositories: 2, Findings: 3, CrossRepoConcepts: 1},
		decoded.Summary)
}

func TestOrgDriftReportIsStrictUnknownKeysRejected(t *testing.T) {
	mutated := driftWire(t, schemas.OrgDriftReportExample, func(tree map[string]any) {
		tree["auto_write"] = true
	})
	_, err := schemas.ParseOrgDriftReport(mutated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestOrgDriftReportAdvisoryOnlyIsASchemaLevelInvariant(t *testing.T) {
	// `advisory` is z.literal(true) — no producer can ever emit a
	// non-advisory variant of this artifact (INV-11).
	mutated := driftWire(t, schemas.OrgDriftReportExample, func(tree map[string]any) {
		tree["advisory"] = false
	})
	issues := driftParseIssues(t, mutated, schemas.ParseOrgDriftReport)
	assert.True(t, driftFind(issues, []string{"advisory"}, "Invalid literal value, expected true"),
		"expected the advisory literal issue, got %v", issues)

	missing := driftWire(t, schemas.OrgDriftReportExample, func(tree map[string]any) {
		delete(tree, "advisory")
	})
	issues = driftParseIssues(t, missing, schemas.ParseOrgDriftReport)
	assert.True(t, driftFind(issues, []string{"advisory"}, "Required"),
		"expected the advisory Required issue, got %v", issues)
}

func TestOrgDriftReportGuardsCrossRepoConceptMinimums(t *testing.T) {
	singleRepo := driftWire(t, schemas.OrgDriftReportExample, func(tree map[string]any) {
		concepts := tree["cross_repo_concepts"].([]any)
		concepts[0].(map[string]any)["repos"] = []any{"acme/widgets"}
	})
	issues := driftParseIssues(t, singleRepo, schemas.ParseOrgDriftReport)
	assert.True(t, driftFind(issues, []string{"cross_repo_concepts", "0", "repos"},
		"Array must contain at least 2 element(s)"),
		"expected the repos min(2) issue, got %v", issues)

	once := driftWire(t, schemas.OrgDriftReportExample, func(tree map[string]any) {
		concepts := tree["cross_repo_concepts"].([]any)
		concepts[0].(map[string]any)["occurrences"] = 1
	})
	issues = driftParseIssues(t, once, schemas.ParseOrgDriftReport)
	assert.True(t, driftFind(issues, []string{"cross_repo_concepts", "0", "occurrences"},
		"Number must be greater than or equal to 2"),
		"expected the occurrences min(2) issue, got %v", issues)
}

func TestOrgDriftReportPeriodIsOptional(t *testing.T) {
	// The covered period is optional (z.strictObject(...).optional()).
	withoutPeriod := driftWire(t, schemas.OrgDriftReportExample, func(tree map[string]any) {
		delete(tree, "period")
	})
	parsed, err := schemas.ParseOrgDriftReport(withoutPeriod)
	require.NoError(t, err)
	assert.Nil(t, parsed.Period)
	assert.NotContains(t, string(driftJSON(t, *parsed)), "period")
}

func TestOrgDriftReportRejectsInvalidPeriodDatetimes(t *testing.T) {
	badPeriod := driftWire(t, schemas.OrgDriftReportExample, func(tree map[string]any) {
		period := tree["period"].(map[string]any)
		period["to"] = "2026-10-09T03:00:00+02:00"
	})
	issues := driftParseIssues(t, badPeriod, schemas.ParseOrgDriftReport)
	assert.True(t, driftFind(issues, []string{"period", "to"}, "Invalid datetime"),
		"expected the period.to datetime issue, got %v", issues)
}
