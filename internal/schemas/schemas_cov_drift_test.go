package schemas_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCOV_SCHEMA_031_DriftReportFactsAndSummary covers the drift differential
// guards: the source/symbol/claim fact validators and the summary counters.
func TestCOV_SCHEMA_031_DriftReportFactsAndSummary(t *testing.T) {
	assert.Empty(t, schemas.DriftReportExample.Validate())

	validSource := schemas.DriftSourceFact{
		Path:      "src/payments/refund.ts",
		Status:    schemas.DriftSourceStatusModified,
		OldPath:   covPtr("src/payments/legacy.ts"),
		Additions: covPtr(2),
		Deletions: covPtr(1),
	}
	assert.Empty(t, validSource.Validate())

	badSource := schemas.DriftSourceFact{
		Path:      "",
		Status:    schemas.DriftSourceStatus("created"),
		OldPath:   covPtr(""),
		Additions: covPtr(-1),
		Deletions: covPtr(-1),
	}
	issues := badSource.Validate()
	require.NotNil(t, covIssueAt(issues, "path"))
	require.NotNil(t, covIssueAt(issues, "status"))
	assert.Contains(t, covIssueAt(issues, "status").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "old_path"))
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "additions").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "deletions").Message)

	noOldPath := validSource
	noOldPath.OldPath = nil
	assert.Empty(t, noOldPath.Validate())

	validSymbol := schemas.DriftSymbolFact{Name: "refundRequest", Path: "src/payments/refund.ts", Kind: schemas.DriftSymbolPresent}
	assert.Empty(t, validSymbol.Validate())

	badSymbol := schemas.DriftSymbolFact{Name: "", Path: "", Kind: schemas.DriftSymbolFactKind("renamed")}
	issues = badSymbol.Validate()
	require.NotNil(t, covIssueAt(issues, "name"))
	require.NotNil(t, covIssueAt(issues, "path"))
	assert.Contains(t, covIssueAt(issues, "kind").Message, "Invalid enum value. Expected 'symbol-present' | 'symbol-absent'")

	validClaim := schemas.DriftClaimFact{
		ClaimID:             "claim_182",
		Verdict:             schemas.DriftClaimSupportMoved,
		PreviousStatus:      schemas.ClaimStatusVerified,
		ProposedStatus:      covPtr(schemas.ClaimStatusSuspect),
		SourceBlobSHA:       "e0d1fa5",
		SourceBlobSHAAtHead: covPtr("b7c2d3e"),
		EvidencePaths:       []string{"src/payments/refund.ts"},
	}
	assert.Empty(t, validClaim.Validate())

	badClaim := schemas.DriftClaimFact{
		ClaimID:        "",
		Verdict:        schemas.DriftClaimVerdict("refuted"),
		PreviousStatus: "unknown",
		ProposedStatus: covPtr("unknown"),
		SourceBlobSHA:  "",
		EvidencePaths:  []string{""},
	}
	issues = badClaim.Validate()
	require.NotNil(t, covIssueAt(issues, "claim_id"))
	assert.Contains(t, covIssueAt(issues, "verdict").Message, "Invalid enum value. Expected")
	assert.Contains(t, covIssueAt(issues, "previous_status").Message, "Invalid enum value. Expected")
	assert.Contains(t, covIssueAt(issues, "proposed_status").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "evidence_paths", "0"))

	noProposed := validClaim
	noProposed.ProposedStatus = nil
	assert.Empty(t, noProposed.Validate())

	badSummary := schemas.DriftReportSummary{SourcesChanged: -1, SymbolsDrifted: -1, ClaimsDrifted: -1}
	issues = badSummary.Validate()
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "sources_changed").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "symbols_drifted").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "claims_drifted").Message)
}

// TestCOV_SCHEMA_032_DriftReportRootAndStrictness covers the root differential
// guards plus its parse-time strictness (unknown keys, missing root keys).
func TestCOV_SCHEMA_032_DriftReportRootAndStrictness(t *testing.T) {
	// The example record is the golden deterministic baseline: it must stay clean.
	assert.Empty(t, schemas.DriftReportExample.Validate())

	broken := schemas.DriftReport{
		Schema:         "",
		RunID:          "",
		At:             "nope",
		Head:           "",
		BaselineCommit: "",
		LadderRun:      schemas.LadderRun("merge-base-of-everything"),
		LadderEvidence: "",
		DocPath:        "",
		Sources:        []schemas.DriftSourceFact{{}},
		Symbols:        []schemas.DriftSymbolFact{{}},
		Claims:         []schemas.DriftClaimFact{{}},
		Summary:        schemas.DriftReportSummary{SourcesChanged: -1},
	}
	issues := broken.Validate()
	assert.Equal(t, "Required", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "run_id"))
	assert.Contains(t, covIssueAt(issues, "at").Message, "Invalid datetime")
	require.NotNil(t, covIssueAt(issues, "head"))
	require.NotNil(t, covIssueAt(issues, "baseline_commit"))
	assert.Contains(t, covIssueAt(issues, "ladder_run").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "ladder_evidence"))
	require.NotNil(t, covIssueAt(issues, "doc_path"))
	require.NotNil(t, covIssueAt(issues, "sources", "0", "path"))
	require.NotNil(t, covIssueAt(issues, "symbols", "0", "name"))
	require.NotNil(t, covIssueAt(issues, "claims", "0", "claim_id"))
	require.NotNil(t, covIssueAt(issues, "summary", "sources_changed"))

	minimal := schemas.DriftReport{
		Schema:         schemas.DriftReportSchemaID,
		RunID:          "run_01J9ZK6X8A",
		At:             "2026-10-08T22:31:00Z",
		Head:           "81af92e",
		BaselineCommit: "8a73f2c",
		LadderRun:      schemas.LadderRunAncestor,
		LadderEvidence: "merge-base equals the recorded baseline commit",
		DocPath:        "docs/payments.md",
	}
	assert.Empty(t, minimal.Validate())
}

// TestCOV_SCHEMA_033_ClaimSupportReportValidators covers the sweep artifact
// validators: the evidence triple, per-claim entry, flip, conflict and
// summary counters.
func TestCOV_SCHEMA_033_ClaimSupportReportValidators(t *testing.T) {
	assert.Empty(t, schemas.ClaimSupportReportExample.Validate())

	validEvidence := schemas.ClaimSupportEvidence{
		ClaimID:             "claim_182",
		AnchorFile:          "docs/payments.md",
		AnchorStartLine:     42,
		AnchorEndLine:       44,
		AnchorResolution:    schemas.ClaimSupportAnchorUnchanged,
		SourceFile:          "src/payments/refund.ts",
		SourceLine:          covPtr(42),
		SourceBlobSHA:       "e0d1fa5",
		SourceBlobSHAAtHead: covPtr("b7c2d3e"),
		Commits:             []string{"81af92e"},
		Detail:              "backing bytes moved",
	}
	assert.Empty(t, validEvidence.Validate())

	badEvidence := schemas.ClaimSupportEvidence{
		ClaimID:          "",
		AnchorFile:       "",
		AnchorStartLine:  0,
		AnchorEndLine:    0,
		AnchorResolution: schemas.ClaimSupportAnchorResolution("re-anchored-then-forgotten"),
		SourceFile:       "",
		SourceLine:       covPtr(0),
		SourceBlobSHA:    "",
		Commits:          []string{""},
		Detail:           "",
	}
	issues := badEvidence.Validate()
	require.NotNil(t, covIssueAt(issues, "claim_id"))
	require.NotNil(t, covIssueAt(issues, "anchor_file"))
	assert.Equal(t, "Number must be greater than or equal to 1", covIssueAt(issues, "anchor_start_line").Message)
	assert.Equal(t, "Number must be greater than or equal to 1", covIssueAt(issues, "anchor_end_line").Message)
	assert.Contains(t, covIssueAt(issues, "anchor_resolution").Message, "Invalid enum value. Expected 'unchanged' | 're-anchored' | 'unanchored'")
	require.NotNil(t, covIssueAt(issues, "source_file"))
	assert.Equal(t, "Number must be greater than or equal to 1", covIssueAt(issues, "source_line").Message)
	require.NotNil(t, covIssueAt(issues, "source_blob_sha"))
	require.NotNil(t, covIssueAt(issues, "commits", "0"))
	require.NotNil(t, covIssueAt(issues, "detail"))

	noSourceLine := validEvidence
	noSourceLine.SourceLine = nil
	assert.Empty(t, noSourceLine.Validate())

	validEntry := schemas.ClaimSupportEntry{
		ClaimID:      "claim_182",
		Verdict:      schemas.ClaimSupportLostSupport,
		StatusBefore: schemas.ClaimStatusVerified,
		Reason:       "backing bytes moved",
		Evidence:     validEvidence,
	}
	assert.Empty(t, validEntry.Validate())

	badEntry := schemas.ClaimSupportEntry{
		ClaimID:      "",
		Verdict:      schemas.ClaimSupportVerdict("maybe-supported"),
		StatusBefore: "nope",
		Reason:       "",
		Evidence:     schemas.ClaimSupportEvidence{},
	}
	issues = badEntry.Validate()
	require.NotNil(t, covIssueAt(issues, "claim_id"))
	assert.Contains(t, covIssueAt(issues, "verdict").Message, "Invalid enum value. Expected 'supported' | 'lost-support' | 'unresolvable-anchor'")
	assert.Contains(t, covIssueAt(issues, "status_before").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "reason"))
	require.NotNil(t, covIssueAt(issues, "evidence", "anchor_file"))

	validFlip := schemas.ClaimSupportFlip{
		ClaimID:    "claim_182",
		From:       schemas.ClaimStatusVerified,
		To:         schemas.ClaimStatusSuspect,
		Transition: schemas.ClaimSupportTransitionLoseSupport,
		Event:      "evt_1",
		Hash:       "sha256:abc",
	}
	assert.Empty(t, validFlip.Validate())

	badFlip := schemas.ClaimSupportFlip{
		ClaimID:    "",
		From:       "nope",
		To:         "nope",
		Transition: schemas.ClaimSupportTransition("reAnchor"),
		Event:      "",
		Hash:       "",
	}
	issues = badFlip.Validate()
	require.NotNil(t, covIssueAt(issues, "claim_id"))
	assert.Contains(t, covIssueAt(issues, "from").Message, "Invalid enum value. Expected")
	assert.Contains(t, covIssueAt(issues, "to").Message, "Invalid enum value. Expected")
	assert.Contains(t, covIssueAt(issues, "transition").Message, "Invalid enum value. Expected 'loseSupport' | 'markUnanchored'")
	require.NotNil(t, covIssueAt(issues, "event"))
	require.NotNil(t, covIssueAt(issues, "hash"))

	validConflict := schemas.ClaimSupportConflict{
		ClaimID:      "claim_182",
		Transition:   schemas.ClaimSupportTransitionMarkUnanchored,
		ExpectedHash: covPtr("a"),
		ActualHash:   covPtr("b"),
	}
	assert.Empty(t, validConflict.Validate())

	badConflict := schemas.ClaimSupportConflict{ClaimID: "", Transition: schemas.ClaimSupportTransition("nope")}
	issues = badConflict.Validate()
	require.NotNil(t, covIssueAt(issues, "claim_id"))
	assert.Contains(t, covIssueAt(issues, "transition").Message, "Invalid enum value. Expected")

	badSummary := schemas.ClaimSupportSummary{Total: -1, Supported: -1, LostSupport: -1, UnresolvableAnchor: -1, Flipped: -1}
	issues = badSummary.Validate()
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "total").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "supported").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "lost_support").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "unresolvable_anchor").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "flipped").Message)

	brokenRoot := schemas.ClaimSupportReport{
		Schema:    "",
		RunID:     "",
		At:        "nope",
		Head:      "",
		Claims:    []schemas.ClaimSupportEntry{{}},
		Flips:     []schemas.ClaimSupportFlip{{}},
		Conflicts: []schemas.ClaimSupportConflict{{}},
		Summary:   schemas.ClaimSupportSummary{},
	}
	issues = brokenRoot.Validate()
	assert.Equal(t, "Required", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "run_id"))
	assert.Contains(t, covIssueAt(issues, "at").Message, "Invalid datetime")
	require.NotNil(t, covIssueAt(issues, "head"))
	require.NotNil(t, covIssueAt(issues, "claims", "0", "reason"))
	require.NotNil(t, covIssueAt(issues, "flips", "0", "claim_id"))
	require.NotNil(t, covIssueAt(issues, "conflicts", "0", "claim_id"))

	assert.False(t, schemas.IsValidClaimSupportVerdict("nope"))
	assert.False(t, schemas.IsValidClaimSupportAnchorResolution("nope"))
	assert.False(t, schemas.IsValidClaimSupportTransition("nope"))
	assert.True(t, schemas.IsValidClaimSupportVerdict("lost-support"))
	assert.True(t, schemas.IsValidClaimSupportAnchorResolution("unanchored"))
	assert.True(t, schemas.IsValidClaimSupportTransition("markUnanchored"))
}

// TestCOV_SCHEMA_034_DriftFindingRecordValidators covers the advisory drift
// finding guards and its parse-time root-key requirement.
func TestCOV_SCHEMA_034_DriftFindingRecordValidators(t *testing.T) {
	assert.Empty(t, schemas.DriftFindingExample.Validate())

	badFinding := schemas.DriftFindingRecord{
		Schema:     "",
		ID:         "",
		ClaimID:    covPtr(""),
		DocPath:    "",
		Kind:       schemas.DriftFindingKind("code-drift"),
		Evidence:   schemas.DriftFindingEvidence{Commits: []string{""}, Paths: []string{""}},
		Confidence: covPtr(1.5),
		CreatedAt:  "nope",
	}
	issues := badFinding.Validate()
	assert.Equal(t, "Required", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "id"))
	require.NotNil(t, covIssueAt(issues, "claim_id"))
	require.NotNil(t, covIssueAt(issues, "doc_path"))
	assert.Contains(t, covIssueAt(issues, "kind").Message, "Invalid enum value. Expected 'claim-lost-support' | 'claim-unanchored' | 'doc-drift' | 'symbol-absent'")
	require.NotNil(t, covIssueAt(issues, "evidence", "commits", "0"))
	require.NotNil(t, covIssueAt(issues, "evidence", "paths", "0"))
	require.NotNil(t, covIssueAt(issues, "evidence", "detail"))
	assert.Equal(t, "Number must be less than or equal to 1", covIssueAt(issues, "confidence").Message)
	assert.Contains(t, covIssueAt(issues, "created_at").Message, "Invalid datetime")

	negativeConfidence := schemas.DriftFindingExample
	negativeConfidence.Confidence = covPtr(float64(-0.5))
	require.NotNil(t, covIssueAt(negativeConfidence.Validate(), "confidence"))

	noClaimID := schemas.DriftFindingExample
	noClaimID.ClaimID = nil
	assert.Empty(t, noClaimID.Validate())

	assert.False(t, schemas.IsValidDriftFindingKind("nope"))
	assert.True(t, schemas.IsValidDriftFindingKind("symbol-absent"))

	_, err := schemas.ParseDriftFindingRecord([]byte(`{"schema":"docsync.drift-finding/v1","id":"f1","doc_path":"d.md","kind":"doc-drift","created_at":"2026-10-09T00:00:00.000Z"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evidence: Required")
}

// TestCOV_SCHEMA_035_DriftAuditRunRecordValidators covers the audit run
// record guards, including the nested findings wiring and its strictness.
func TestCOV_SCHEMA_035_DriftAuditRunRecordValidators(t *testing.T) {
	assert.Empty(t, schemas.DriftAuditRunRecordExample.Validate())

	broken := schemas.DriftAuditRunRecord{
		Schema:       "",
		RunID:        "",
		At:           "nope",
		Trigger:      schemas.DriftAuditTrigger("cron-every-minute"),
		Position:     "",
		CursorBefore: covPtr(""),
		CursorAfter:  covPtr(""),
		AuditedDocs:  []string{""},
		SkippedDocs:  []string{""},
		Status:       schemas.DriftAuditRunStatus("in-progress"),
		Detail:       covPtr(""),
		Findings:     []schemas.DriftFindingRecord{{}},
	}
	issues := broken.Validate()
	assert.Equal(t, "Required", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "run_id"))
	assert.Contains(t, covIssueAt(issues, "at").Message, "Invalid datetime")
	assert.Contains(t, covIssueAt(issues, "trigger").Message, "Invalid enum value. Expected 'nightly-scheduled' | 'post-merge-default-branch' | 'manual'")
	require.NotNil(t, covIssueAt(issues, "position"))
	require.NotNil(t, covIssueAt(issues, "audited_docs", "0"))
	require.NotNil(t, covIssueAt(issues, "skipped_docs", "0"))
	assert.Contains(t, covIssueAt(issues, "status").Message, "Invalid enum value. Expected 'completed' | 'partial' | 're-run-required' | 'abstained'")
	require.NotNil(t, covIssueAt(issues, "findings", "0", "doc_path"))

	assert.False(t, schemas.IsValidDriftAuditTrigger("nope"))
	assert.True(t, schemas.IsValidDriftAuditTrigger(string(schemas.DriftAuditTriggerManual)))
	assert.False(t, schemas.IsValidDriftAuditRunStatus("nope"))
	assert.True(t, schemas.IsValidDriftAuditRunStatus(string(schemas.DriftAuditRunAbstained)))
	assert.True(t, schemas.IsValidDriftSymbolFactKind("symbol-absent"))
	assert.False(t, schemas.IsValidDriftClaimVerdict("nope"))
	assert.True(t, schemas.IsValidDriftClaimVerdict("support-moved"))

	valid := schemas.DriftAuditRunRecord{
		Schema:   schemas.DriftAuditRunRecordSchemaID,
		RunID:    "run_1",
		At:       "2026-10-09T00:00:00.000Z",
		Trigger:  schemas.DriftAuditTriggerManual,
		Position: "git:81af92e",
		Status:   schemas.DriftAuditRunCompleted,
	}
	assert.Empty(t, valid.Validate())

	_, err := schemas.ParseDriftAuditRunRecord([]byte(`{"schema":"docsync.drift-audit-run-record/v1","run_id":"r","at":"2026-10-09T00:00:00.000Z","trigger":"manual","position":"p","audited_docs":[],"skipped_docs":[],"status":"completed","findings":[]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cursor_before: Required")
	assert.Contains(t, err.Error(), "detail: Required")
}
