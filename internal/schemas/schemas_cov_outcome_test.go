package schemas_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCOV_SCHEMA_036_RunOutcomeLeafValidators covers every leaf validator the
// root wires in: the run key, stage, per-document verification/abstain,
// candidate, model binding, provenance, early signal and usage blocks.
func TestCOV_SCHEMA_036_RunOutcomeLeafValidators(t *testing.T) {
	example := schemas.RunOutcomeExample()
	assert.Empty(t, example.Validate())

	badVerification := schemas.RunDocumentVerification{
		Verdict:          schemas.VerificationVerdict("abstained"),
		AutoBlockReasons: []string{"not-a-canonical-reason"},
		PatchRef:         covPtr(""),
	}
	issues := badVerification.Validate()
	require.NotNil(t, covIssueAt(issues, "verdict"))
	assert.Contains(t, covIssueAt(issues, "verdict").Message, "Invalid enum value. Expected 'pass' | 'review' | 'reject'")
	require.NotNil(t, covIssueAt(issues, "auto_block_reasons", "0"))
	assert.Contains(t, covIssueAt(issues, "auto_block_reasons", "0").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "patch_ref"))

	cleanVerification := schemas.RunDocumentVerification{
		Verdict:          schemas.VerdictPass,
		AutoBlockReasons: []string{schemas.AutoBlockUnanchoredClaims},
	}
	assert.Empty(t, cleanVerification.Validate())

	badDoc := schemas.RunDocument{
		DocPath:         "",
		PolicyOutcome:   schemas.PipelineOutcome("queued"),
		DecisionID:      covPtr(""),
		Confidence:      covPtr(float64(-0.1)),
		Routing:         covPtr(schemas.PipelineRouting("ESCALATE")),
		Verification:    &schemas.RunDocumentVerification{Verdict: schemas.VerificationVerdict("abstained")},
		Abstain:         &schemas.RunDocumentAbstain{Reason: "nope", Detail: ""},
		BaselineRun:     covPtr(schemas.BaselineRun("fingerprint-only")),
		PartialCoverage: false,
	}
	issues = badDoc.Validate()
	require.NotNil(t, covIssueAt(issues, "doc_path"))
	require.NotNil(t, covIssueAt(issues, "decision_id"))
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "confidence").Message)
	require.NotNil(t, covIssueAt(issues, "routing"))
	assert.Contains(t, covIssueAt(issues, "routing").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "abstain", "reason"))
	require.NotNil(t, covIssueAt(issues, "abstain", "detail"))
	require.NotNil(t, covIssueAt(issues, "verification", "verdict"))

	highConfidence := schemas.RunDocument{
		DocPath:       "docs/api.md",
		PolicyOutcome: schemas.PipelineOutcomeIgnore,
		Confidence:    covPtr(1.5),
	}
	issues = highConfidence.Validate()
	require.NotNil(t, covIssueAt(issues, "confidence"))
	assert.Equal(t, "Number must be less than or equal to 1", covIssueAt(issues, "confidence").Message)

	badLateSignal := schemas.RunDocument{PartialCoverage: true}
	_ = badLateSignal

	badCandidate := schemas.RunCandidate{
		DocPath: "",
		Levels:  []int{-1, 2},
		Provenance: []schemas.RunCandidateProvenance{
			{
				Level:    -1,
				Source:   "",
				Evidence: &schemas.RunCandidateLevelEvidence{},
			},
		},
		MaxAction: schemas.PipelineMaxAction("hard-block"),
	}
	issues = badCandidate.Validate()
	require.NotNil(t, covIssueAt(issues, "doc_path"))
	require.NotNil(t, covIssueAt(issues, "levels", "0"))
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "provenance", "0", "level").Message)
	require.NotNil(t, covIssueAt(issues, "provenance", "0", "source"))
	assert.Contains(t, covIssueAt(issues, "max_action").Message, "Invalid enum value. Expected 'review' | 'auto'")

	badBinding := schemas.RunModelBinding{Role: "reviewer", Model: "", Provider: covPtr("")}
	issues = badBinding.Validate()
	require.NotNil(t, covIssueAt(issues, "role"))
	assert.Contains(t, covIssueAt(issues, "role").Message, "Invalid enum value. Expected 'decision-engine' | 'judge' | 'writer'")
	require.NotNil(t, covIssueAt(issues, "model"))
	require.NotNil(t, covIssueAt(issues, "provider"))

	badProvenance := schemas.RunProvenance{
		PolicyVersion:   0,
		ModelBindings:   []schemas.RunModelBinding{{Role: "judge", Model: ""}},
		WriterSessionID: covPtr(""),
		JudgeSessionID:  covPtr(""),
	}
	issues = badProvenance.Validate()
	require.NotNil(t, covIssueAt(issues, "policy_version"))
	require.NotNil(t, covIssueAt(issues, "model_bindings", "0", "model"))
	require.NotNil(t, covIssueAt(issues, "writer_session_id"))
	require.NotNil(t, covIssueAt(issues, "judge_session_id"))

	earlySignal := schemas.EarlySignal{PostedAt: covPtr("nope"), UpdatedAt: covPtr("nope")}
	badEarlySignal := &earlySignal
	issues = badEarlySignal.Validate()
	require.NotNil(t, covIssueAt(issues, "posted_at"))
	assert.Contains(t, covIssueAt(issues, "posted_at").Message, "Invalid datetime")
	require.NotNil(t, covIssueAt(issues, "updated_at"))
	assert.Contains(t, covIssueAt(issues, "updated_at").Message, "Invalid datetime")

	emptyEarlySignal := schemas.EarlySignal{}
	assert.Empty(t, (&emptyEarlySignal).Validate())

	usage := schemas.RunUsage{PromptTokens: -1, CompletionTokens: -1, TotalTokens: covPtr(-1), Cost: covPtr(float64(-0.01)), PolicyVersion: 0}
	badUsage := &usage
	issues = badUsage.Validate()
	require.NotNil(t, covIssueAt(issues, "prompt_tokens"))
	require.NotNil(t, covIssueAt(issues, "completion_tokens"))
	require.NotNil(t, covIssueAt(issues, "total_tokens"))
	require.NotNil(t, covIssueAt(issues, "cost"))
	require.NotNil(t, covIssueAt(issues, "policy_version"))

	emptyUsage := schemas.RunUsage{PolicyVersion: 1}
	assert.Empty(t, (&emptyUsage).Validate())

	badAbstain := schemas.RunAbstain{AbstainID: "", DocPath: covPtr(""), Reason: "nope", Detail: "", DecisionID: covPtr(""), QueueEventID: covPtr("")}
	issues = badAbstain.Validate()
	require.NotNil(t, covIssueAt(issues, "abstain_id"))
	require.NotNil(t, covIssueAt(issues, "doc_path"))
	require.NotNil(t, covIssueAt(issues, "reason"))
	require.NotNil(t, covIssueAt(issues, "detail"))
	require.NotNil(t, covIssueAt(issues, "decision_id"))
	require.NotNil(t, covIssueAt(issues, "queue_event_id"))

	badFinding := schemas.RunFinding{Kind: "", Detail: "", PatchRef: covPtr("")}
	issues = badFinding.Validate()
	require.NotNil(t, covIssueAt(issues, "kind"))
	require.NotNil(t, covIssueAt(issues, "detail"))
	require.NotNil(t, covIssueAt(issues, "patch_ref"))

	badStage := schemas.PipelineStage{Stage: "", Status: schemas.RunStageStatus("aborted"), LatencyMS: covPtr(-1)}
	issues = badStage.Validate()
	require.NotNil(t, covIssueAt(issues, "stage"))
	assert.Contains(t, covIssueAt(issues, "status").Message, "Invalid enum value. Expected")
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "latency_ms").Message)
}

// TestCOV_SCHEMA_037_RunOutcomeRootWiring covers the root record wiring and
// its three V-P5-16 refinements (completed-is-finished, every document
// accounted for, capped coverage is a declared degradation).
func TestCOV_SCHEMA_037_RunOutcomeRootWiring(t *testing.T) {
	base := schemas.RunOutcomeExample()
	assert.Empty(t, base.Validate())

	broken := base
	broken.Schema = ""
	broken.RunID = ""
	broken.Status = schemas.RunStatus("aborted")
	broken.StartedAt = covPtr("nope")
	broken.FinishedAt = covPtr("nope")
	broken.Stages = []schemas.PipelineStage{{}}
	broken.Documents = []schemas.RunDocument{{}}
	broken.Abstains = []schemas.RunAbstain{{}}
	broken.Candidates = []schemas.RunCandidate{{}}
	broken.UnknownCoverage = []string{""}
	broken.Provenance = schemas.RunProvenance{}
	broken.Degradations = []schemas.Degradation{{}}
	broken.Findings = []schemas.RunFinding{{}}
	broken.EarlySignal = schemas.EarlySignal{}
	broken.ConcurrencyGroup = covPtr("")
	broken.StateChurn = schemas.StateChurn{Added: -1}
	issues := broken.Validate()
	assert.Equal(t, "Invalid literal value, expected \"docsync.run-outcome/v1\"", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "run_id"))
	assert.Contains(t, covIssueAt(issues, "status").Message, "Invalid enum value. Expected")
	assert.Contains(t, covIssueAt(issues, "started_at").Message, "Invalid datetime")
	require.NotNil(t, covIssueAt(issues, "stages", "0", "stage"))
	require.NotNil(t, covIssueAt(issues, "documents", "0", "doc_path"))
	require.NotNil(t, covIssueAt(issues, "abstains", "0", "abstain_id"))
	require.NotNil(t, covIssueAt(issues, "candidates", "0", "doc_path"))
	require.NotNil(t, covIssueAt(issues, "unknown_coverage", "0"))
	require.NotNil(t, covIssueAt(issues, "provenance", "policy_version"))
	require.NotNil(t, covIssueAt(issues, "degradations", "0", "stage"))
	require.NotNil(t, covIssueAt(issues, "findings", "0", "kind"))
	require.NotNil(t, covIssueAt(issues, "concurrency_group"))
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "state_churn", "added").Message)

	// Rule 1: a completed run is finished.
	unfinished := base
	unfinished.FinishedAt = nil
	issues = unfinished.Validate()
	require.NotNil(t, covIssueAt(issues, "finished_at"))
	assert.Equal(t, "status 'completed' requires a non-null finished_at (a completed run is finished; V-P5-16)",
		covIssueAt(issues, "finished_at").Message)

	degraded := schemas.RunOutcomeDegradedExample()
	assert.Empty(t, degraded.Validate())

	// Rule 2: an account-less document in a completed run.
	unaccounted := base
	unaccounted.Documents = []schemas.RunDocument{
		{DocPath: "docs/orphan.md", PolicyOutcome: schemas.PipelineOutcomeAuto},
		{DocPath: "docs/ok.md", PolicyOutcome: schemas.PipelineOutcomeIgnore},
	}
	issues = unaccounted.Validate()
	require.NotNil(t, covIssueAt(issues, "documents", "0"))
	assert.Contains(t, covIssueAt(issues, "documents", "0").Message,
		"completed run: document 'docs/orphan.md' needs a verification block, an abstain record, a matching finding, or policy_outcome 'ignore'/'review'")
	assert.Nil(t, covIssueAt(issues, "documents", "1"))

	// Rule 3: partial coverage must declare the cap-overflow degradation.
	partial := base
	partial.Documents = []schemas.RunDocument{{DocPath: "docs/payments.md", PolicyOutcome: schemas.PipelineOutcomeAuto, PartialCoverage: true}}
	partial.Degradations = []schemas.Degradation{}
	issues = partial.Validate()
	require.NotNil(t, covIssueAt(issues, "degradations"))
	assert.Contains(t, covIssueAt(issues, "degradations").Message,
		"partial_coverage document requires a degradations entry with kind 'cap-overflow'")

	declared := partial
	declared.Documents = []schemas.RunDocument{{
		DocPath:         "docs/payments.md",
		PolicyOutcome:   schemas.PipelineOutcomeAuto,
		PartialCoverage: true,
		Verification:    &schemas.RunDocumentVerification{Verdict: schemas.VerdictPass},
	}}
	declared.Degradations = []schemas.Degradation{{Stage: "change-analysis", Kind: schemas.DegradationCapOverflow, Detail: "capped at 50 candidates"}}
	assert.Empty(t, declared.Validate())

	// Refinements never run on an already-invalid record: a structurally
	// broken base record short-circuits before the refinement rules.
	bothBroken := base
	bothBroken.Status = schemas.RunStatus("aborted")
	bothBroken.Documents = []schemas.RunDocument{{DocPath: "docs/orphan.md", PolicyOutcome: schemas.PipelineOutcomeAuto, PartialCoverage: true}}
	issues = bothBroken.Validate()
	assert.Nil(t, covIssueAt(issues, "degradations"))
	assert.Nil(t, covIssueAt(issues, "finished_at"))
}

// covAnchor builds a valid bare-64-hex anchor without repeating the digest.
func covAnchor(start, end int) schemas.PatchAnchor {
	return schemas.PatchAnchor{
		StartLine:   start,
		EndLine:     end,
		ContentHash: "cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64",
	}
}

// covHunk builds one hunk with the minimum mandatory evidence ref.
func covHunk(deleteText, insertText string, anchor schemas.PatchAnchor) schemas.PatchHunk {
	return schemas.PatchHunk{
		Anchor:       anchor,
		DeleteText:   deleteText,
		InsertText:   insertText,
		EvidenceRefs: []schemas.Evidence{{Kind: "symbol_diff", Ref: "src/a.ts:1"}},
	}
}

const covTenLineDoc = "line one\nline two\nline three\nline four\nline five\n" +
	"line six\nline seven\nline eight\nline nine\nline ten\n"

// TestCOV_SCHEMA_038_DocPatchStructuralGuards covers the structural rewrite
// predicates: rule A (deletion span), rule B (structural diff), the zero-
// document short-circuit and the measured-ratio surface.
func TestCOV_SCHEMA_038_DocPatchStructuralGuards(t *testing.T) {
	assert.Empty(t, schemas.DocPatchExample.Validate())

	// An empty document has nothing to rewrite.
	creationPatch := &schemas.DocPatch{Hunks: []schemas.PatchHunk{covHunk("", "brand new line", covAnchor(1, 1))}}
	assert.False(t, schemas.IsWholeDocumentRewrite(creationPatch, ""))
	assert.Equal(t, 0.0, schemas.MeasureStructuralDiffRatio(&schemas.DocPatch{}, ""))
	assert.Equal(t, 0.0, schemas.MeasureStructuralDiffRatio(nil, ""))

	// Rule B: a single insertion against a ten-line document stays a patch.
	smallInsert := &schemas.DocPatch{Hunks: []schemas.PatchHunk{covHunk("", "inserted", covAnchor(1, 1))}}
	assert.False(t, schemas.IsWholeDocumentRewrite(smallInsert, covTenLineDoc))
	assert.InDelta(t, 0.1, schemas.MeasureStructuralDiffRatio(smallInsert, covTenLineDoc), 0.0001)

	// Rule B backstop: duplicating every line drives the diff ratio past 0.9.
	duplicating := &schemas.DocPatch{Hunks: []schemas.PatchHunk{covHunk("", covTenLineDoc, covAnchor(1, 1))}}
	assert.True(t, schemas.IsWholeDocumentRewrite(duplicating, covTenLineDoc))

	err := schemas.AssertNotWholeDocumentRewrite(duplicating, covTenLineDoc)
	require.Error(t, err)
	rewriteErr := &schemas.WholeDocumentRewriteError{}
	require.ErrorAs(t, err, &rewriteErr)
	assert.Equal(t, schemas.WholeDocumentRewriteStructuralDiff, rewriteErr.Reason)
	assert.Equal(t, "WHOLE_DOCUMENT_REWRITE", rewriteErr.Code())
	assert.Contains(t, rewriteErr.Error(),
		"whole-document rewrite rejected structurally (structural_diff): measured ratio 1 >= WHOLE_DOC_REWRITE_RATIO 0.9")
	assert.NotEmpty(t, schemas.RewriteRejectionSchemaNote)

	// Rule A: a deletion span covering the whole document.
	deleting := &schemas.DocPatch{Hunks: []schemas.PatchHunk{
		covHunk("delete everything", "", covAnchor(1, 10)),
	}}
	err = schemas.AssertNotWholeDocumentRewrite(deleting, covTenLineDoc)
	require.Error(t, err)
	require.ErrorAs(t, err, &rewriteErr)
	assert.Equal(t, schemas.WholeDocumentRewriteDeletionSpan, rewriteErr.Reason)
	assert.Equal(t, 1.0, rewriteErr.MeasuredRatio)

	// A deletion span below the threshold passes both rules.
	partialDelete := &schemas.DocPatch{Hunks: []schemas.PatchHunk{covHunk("just one line", "", covAnchor(1, 1))}}
	assert.NoError(t, schemas.AssertNotWholeDocumentRewrite(partialDelete, covTenLineDoc))

	assert.Equal(t, 0.9, schemas.WholeDocumentRewriteRatio)
}
