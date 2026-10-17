package schemas_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// covPassingTiers is the canonical three-tier, all-passing tier set.
func covPassingTiers() []schemas.VerificationTierReport {
	return []schemas.VerificationTierReport{
		{Tier: 1, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "patch-applies-cleanly", Status: schemas.CheckPass}}},
		{Tier: 2, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "claim-decomposition", Status: schemas.CheckPass}}},
		{Tier: 3, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "judge-second-pass", Status: schemas.CheckPass}}},
	}
}

// covBaseReport is a structurally valid verification report (all three tiers
// pass, the judge passes, no claims, auto-eligible).
func covBaseReport() schemas.VerificationReport {
	return schemas.VerificationReport{
		Schema:           "docsync.verification-report/v1",
		RunID:            "run_01J9ZK6X8A",
		DocPath:          "docs/payments.md",
		PatchRef:         "state/docs/payments.md#p1",
		Tiers:            covPassingTiers(),
		Claims:           []schemas.VerificationClaim{},
		Verdict:          schemas.VerdictPass,
		AutoEligible:     true,
		AutoBlockReasons: []string{},
		PolicyVersion:    17,
		Judge: &schemas.VerificationJudge{
			Model:   "anthropic/claude-sonnet-4",
			Verdict: schemas.VerdictPass,
		},
	}
}

// TestCOV_SCHEMA_022_VerificationReportTierSetAndTierCoherence covers the
// tier-count and duplicate-tier guards plus the check-id → tier coherence
// mapping.
func TestCOV_SCHEMA_022_VerificationReportTierSetAndTierCoherence(t *testing.T) {
	base := covBaseReport()
	assert.Empty(t, base.ValidateReport())

	onlyTwo := base
	onlyTwo.Tiers = covPassingTiers()[:2]
	issues := onlyTwo.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "tiers"))
	assert.Contains(t, covIssueAt(issues, "tiers").Message, "tiers must contain exactly three entries")

	duplicated := base
	duplicated.Tiers = []schemas.VerificationTierReport{
		{Tier: 1, Status: schemas.CheckPass},
		{Tier: 1, Status: schemas.CheckPass},
		{Tier: 3, Status: schemas.CheckPass},
	}
	issues = duplicated.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "tiers"))
	assert.Equal(t, "duplicate tier entries — tiers 1, 2 and 3 must each appear exactly once",
		covIssueAt(issues, "tiers").Message)

	wrongTier := base
	wrongTier.Tiers = []schemas.VerificationTierReport{
		{Tier: 1, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "claim-decomposition", Status: schemas.CheckPass}}},
		{Tier: 2, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "claim-decomposition", Status: schemas.CheckPass}}},
		{Tier: 3, Status: schemas.CheckPass},
	}
	issues = wrongTier.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "tiers", "0", "checks", "0", "id"))
	assert.Equal(t,
		"check 'claim-decomposition' belongs to tier 2 but appears in the tier-1 report (closed vocabulary mapping, CHECK_TIER)",
		covIssueAt(issues, "tiers", "0", "checks", "0", "id").Message)

	// An unknown check id is vocabulary-open at this layer (no mapping entry).
	unknownCheck := base
	unknownCheck.Tiers = []schemas.VerificationTierReport{
		{Tier: 1, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "not-a-real-check", Status: schemas.CheckPass}}},
		{Tier: 2, Status: schemas.CheckPass},
		{Tier: 3, Status: schemas.CheckPass},
	}
	assert.Nil(t, covIssueAt(unknownCheck.ValidateReport(), "tiers", "0", "checks", "0", "id"))

	// A failing check must carry its detail (invariant 5).
	failingWithoutDetail := base
	failingWithoutDetail.Tiers = []schemas.VerificationTierReport{
		{
			Tier:   1,
			Status: schemas.CheckFail,
			Checks: []schemas.VerificationCheck{
				{ID: "link-resolution", Status: schemas.CheckFail},
				{ID: "manifest-allowlist-only", Status: schemas.CheckFail, Detail: covPtr("docs/absent.md does not resolve")},
			},
		},
		{Tier: 2, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "claim-decomposition", Status: schemas.CheckPass}}},
		{Tier: 3, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "judge-second-pass", Status: schemas.CheckPass}}},
	}
	failingWithoutDetail.Verdict = schemas.VerdictReject
	issues = failingWithoutDetail.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "tiers", "0", "checks", "0", "detail"))
	assert.Equal(t, "failing check 'link-resolution' must carry a non-empty detail (invariant 5: verification evidence)",
		covIssueAt(issues, "tiers", "0", "checks", "0", "detail").Message)
	assert.Nil(t, covIssueAt(issues, "tiers", "0", "checks", "1", "detail"))

	// An empty detail string is equivalent to a missing detail.
	emptyDetail := base
	emptyDetail.Tiers = []schemas.VerificationTierReport{
		{Tier: 1, Status: schemas.CheckFail, Checks: []schemas.VerificationCheck{{ID: "link-resolution", Status: schemas.CheckFail, Detail: covPtr("")}}},
		{Tier: 2, Status: schemas.CheckPass},
		{Tier: 3, Status: schemas.CheckPass},
	}
	emptyDetail.Verdict = schemas.VerdictReject
	require.NotNil(t, covIssueAt(emptyDetail.ValidateReport(), "tiers", "0", "checks", "0", "detail"))
}

// TestCOV_SCHEMA_023_VerificationReportVerdictMapping covers every canonical
// verdict-mapping violation: contradiction, pinned judge-reject redundancy and
// the cross-patch-conflict prohibition.
func TestCOV_SCHEMA_023_VerificationReportVerdictMapping(t *testing.T) {
	base := covBaseReport()

	contradiction := base
	contradiction.Verdict = schemas.VerdictReview
	issues := contradiction.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "verdict"))
	assert.Contains(t, covIssueAt(issues, "verdict").Message,
		"verdict 'review' contradicts the canonical mapping 'pass'")

	judgeRejects := base
	judgeRejects.Judge = &schemas.VerificationJudge{Model: "anthropic/claude-sonnet-4", Verdict: schemas.VerdictReject}
	judgeRejects.Verdict = schemas.VerdictReview
	issues = judgeRejects.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "judge", "verdict"))
	assert.Equal(t, "judge verdict reject ⇒ report verdict reject (fail closed; ARCH §4.8)",
		covIssueAt(issues, "judge", "verdict").Message)

	crossPatch := base
	crossPatch.CrossPatchConflict = covPtr(true)
	crossPatch.Verdict = schemas.VerdictPass
	issues = crossPatch.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "cross_patch_conflict"))
	assert.Contains(t, covIssueAt(issues, "cross_patch_conflict").Message,
		"cross_patch_conflict true with verdict 'pass' is forbidden")

	crossPatchReview := base
	crossPatchReview.CrossPatchConflict = covPtr(true)
	crossPatchReview.Verdict = schemas.VerdictReview
	assert.Empty(t, crossPatchReview.ValidateReport())

	crossPatchFalse := base
	crossPatchFalse.CrossPatchConflict = covPtr(false)
	assert.Empty(t, crossPatchFalse.ValidateReport())
}

// TestCOV_SCHEMA_024_VerificationReportJudgeAbstentionConsistency covers the
// mutual exclusion, the tier-3-skipped pairing and the pass prohibition.
func TestCOV_SCHEMA_024_VerificationReportJudgeAbstentionConsistency(t *testing.T) {
	base := covBaseReport()

	both := base
	both.Judge = &schemas.VerificationJudge{Model: "anthropic/claude-sonnet-4", Verdict: schemas.VerdictPass}
	both.JudgeAbstain = &schemas.VerificationJudgeAbstain{Reason: "gateway-unavailable", Detail: "judge model timed out"}
	issues := both.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "judge"))
	assert.Equal(t, "judge and judge_abstain are mutually exclusive — an abstaining judge emits no judge block",
		covIssueAt(issues, "judge").Message)

	// A valid abstention: tier 3 skipped, verdict review, no judge block.
	abstainOnly := base
	abstainOnly.Judge = nil
	abstainOnly.JudgeAbstain = &schemas.VerificationJudgeAbstain{Reason: "gateway-unavailable", Detail: "judge model timed out"}
	abstainOnly.Tiers = []schemas.VerificationTierReport{
		{Tier: 1, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "patch-applies-cleanly", Status: schemas.CheckPass}}},
		{Tier: 2, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "claim-decomposition", Status: schemas.CheckPass}}},
		{Tier: 3, Status: schemas.CheckSkipped, Checks: []schemas.VerificationCheck{{ID: "judge-second-pass", Status: schemas.CheckSkipped}}},
	}
	abstainOnly.Verdict = schemas.VerdictReview
	assert.Empty(t, abstainOnly.ValidateReport())

	// Abstention present but tier 3 is not skipped.
	abstainUnpaired := abstainOnly
	abstainUnpaired.Tiers = covPassingTiers()
	issues = abstainUnpaired.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "judge_abstain"))
	assert.Equal(t, "judge_abstain present ⇒ tier 3 status must be skipped (the judge did not run)",
		covIssueAt(issues, "judge_abstain").Message)

	// Tier 3 skipped with no abstention record at all.
	skippedNoAbstain := base
	skippedNoAbstain.Judge = nil
	skippedNoAbstain.JudgeAbstain = nil
	skippedNoAbstain.Tiers = []schemas.VerificationTierReport{
		{Tier: 1, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "patch-applies-cleanly", Status: schemas.CheckPass}}},
		{Tier: 2, Status: schemas.CheckPass, Checks: []schemas.VerificationCheck{{ID: "claim-decomposition", Status: schemas.CheckPass}}},
		{Tier: 3, Status: schemas.CheckSkipped},
	}
	skippedNoAbstain.Verdict = schemas.VerdictReview
	issues = skippedNoAbstain.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "judge_abstain"))
	assert.Equal(t,
		"tier 3 'skipped' requires a judge_abstain record — a skipped judge tier fails closed to review (invariant 9)",
		covIssueAt(issues, "judge_abstain").Message)

	// A skipped tier 3 must never resolve to 'pass'.
	skippedWithPass := skippedNoAbstain
	skippedWithPass.Verdict = schemas.VerdictPass
	issues = skippedWithPass.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "verdict"))
	assert.Equal(t, "tier 3 'skipped' ⇒ verdict must not be 'pass' — judge abstention reviews",
		covIssueAt(issues, "verdict").Message)
}

// TestCOV_SCHEMA_025_VerificationReportAutoEligibilityGate covers the
// auto_block_reasons ⇔ auto_eligible gate and the claim-derived floor rule.
func TestCOV_SCHEMA_025_VerificationReportAutoEligibilityGate(t *testing.T) {
	base := covBaseReport()

	eligibleWithReasons := base
	eligibleWithReasons.AutoEligible = true
	eligibleWithReasons.AutoBlockReasons = []string{schemas.AutoBlockUnanchoredClaims}
	issues := eligibleWithReasons.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "auto_eligible"))
	assert.Equal(t, "auto_block_reasons empty ⇔ auto_eligible true (OD-R09 gate)",
		covIssueAt(issues, "auto_eligible").Message)

	blockedWithoutReasons := base
	blockedWithoutReasons.AutoEligible = false
	issues = blockedWithoutReasons.ValidateReport()
	require.NotNil(t, covIssueAt(issues, "auto_block_reasons"))
	assert.Equal(t, "auto_block_reasons empty ⇔ auto_eligible true (OD-R09 gate)",
		covIssueAt(issues, "auto_block_reasons").Message)

	unanchored := base
	unanchored.Claims = []schemas.VerificationClaim{{ClaimID: "c1", Status: schemas.ClaimVerdictUnanchored}}
	issues = unanchored.ValidateReport()
	assert.True(t, covHasMessage(issues,
		"claim statuses imply auto_block_reason 'unanchored-claims' (OD-R09) but it is absent — claim-derived block reasons are a floor"))

	behavioral := base
	behavioral.Claims = []schemas.VerificationClaim{
		{ClaimID: "c1", Status: schemas.ClaimVerdictBehavioralUnverified},
		{ClaimID: "c2", Status: schemas.ClaimVerdictSupported},
	}
	issues = behavioral.ValidateReport()
	assert.True(t, covHasMessage(issues,
		"claim statuses imply auto_block_reason 'behavioral-predicate-unverified' (OD-R09) but it is absent — claim-derived block reasons are a floor"))

	covered := base
	covered.Claims = []schemas.VerificationClaim{{ClaimID: "c1", Status: schemas.ClaimVerdictUnanchored}}
	covered.AutoEligible = false
	covered.AutoBlockReasons = []string{schemas.AutoBlockUnanchoredClaims}
	assert.Empty(t, covered.ValidateReport())
}

// TestCOV_SCHEMA_026_ResolveVerificationVerdictCanonicalMapping covers each
// precedence branch of the canonical verdict derivation.
func TestCOV_SCHEMA_026_ResolveVerificationVerdictCanonicalMapping(t *testing.T) {
	failing := []schemas.VerificationTierReport{{Tier: 1, Status: schemas.CheckFail}}
	assert.Equal(t, schemas.VerdictReject,
		schemas.ResolveVerificationVerdict(failing, nil, nil, false))

	assert.Equal(t, schemas.VerdictReject,
		schemas.ResolveVerificationVerdict(covPassingTiers(),
			&schemas.VerificationJudge{Verdict: schemas.VerdictReject}, nil, false))

	assert.Equal(t, schemas.VerdictReview,
		schemas.ResolveVerificationVerdict(covPassingTiers(), nil, nil, true))

	assert.Equal(t, schemas.VerdictReview,
		schemas.ResolveVerificationVerdict(covPassingTiers(),
			&schemas.VerificationJudge{Verdict: schemas.VerdictReview}, nil, false))

	assert.Equal(t, schemas.VerdictReview,
		schemas.ResolveVerificationVerdict(covPassingTiers(), nil,
			&schemas.VerificationJudgeAbstain{Reason: "gateway-unavailable"}, false))

	assert.Equal(t, schemas.VerdictPass,
		schemas.ResolveVerificationVerdict(covPassingTiers(),
			&schemas.VerificationJudge{Verdict: schemas.VerdictPass}, nil, false))

	// Tier failure wins over a passing judge.
	assert.Equal(t, schemas.VerdictReject,
		schemas.ResolveVerificationVerdict(failing,
			&schemas.VerificationJudge{Verdict: schemas.VerdictPass}, nil, true))
}

// TestCOV_SCHEMA_027_ResolveAutoEligibilityCanonicalOrder covers the
// derivation, its canonical ordering and the widening/floor semantics.
func TestCOV_SCHEMA_027_ResolveAutoEligibilityCanonicalOrder(t *testing.T) {
	clean := schemas.ResolveAutoEligibility(nil)
	assert.True(t, clean.AutoEligible)
	assert.Empty(t, clean.AutoBlockReasons)

	unanchoredOnly := schemas.ResolveAutoEligibility([]schemas.VerificationClaim{
		{ClaimID: "c1", Status: schemas.ClaimVerdictUnanchored},
		{ClaimID: "c2", Status: schemas.ClaimVerdictSupported},
	})
	assert.False(t, unanchoredOnly.AutoEligible)
	assert.Equal(t, []string{schemas.AutoBlockUnanchoredClaims}, unanchoredOnly.AutoBlockReasons)

	behavioralOnly := schemas.ResolveAutoEligibility([]schemas.VerificationClaim{
		{ClaimID: "c1", Status: schemas.ClaimVerdictBehavioralUnverified},
	})
	assert.False(t, behavioralOnly.AutoEligible)
	assert.Equal(t, []string{schemas.AutoBlockBehavioralPredicate}, behavioralOnly.AutoBlockReasons)

	both := schemas.ResolveAutoEligibility([]schemas.VerificationClaim{
		{ClaimID: "c1", Status: schemas.ClaimVerdictBehavioralUnverified},
		{ClaimID: "c2", Status: schemas.ClaimVerdictUnanchored},
	})
	assert.False(t, both.AutoEligible)
	assert.Equal(t, []string{schemas.AutoBlockUnanchoredClaims, schemas.AutoBlockBehavioralPredicate}, both.AutoBlockReasons)

	widened := schemas.ResolveAutoEligibility(
		[]schemas.VerificationClaim{{ClaimID: "c1", Status: schemas.ClaimVerdictUnanchored}},
		schemas.AutoBlockBehavioralPredicate,
		schemas.AutoBlockUnanchoredClaims,
	)
	assert.Equal(t, []string{schemas.AutoBlockUnanchoredClaims, schemas.AutoBlockBehavioralPredicate}, widened.AutoBlockReasons)

	// Only the closed canonical reason set is emitted; a non-canonical extra
	// reason is dropped (documented in the suspected-bug list: the doc
	// comment claims extras widen the block set, but the derivation filters
	// to AutoBlockReasonValues and derives eligibility from the survivors).
	unlisted := schemas.ResolveAutoEligibility(nil, "operator-requested")
	assert.True(t, unlisted.AutoEligible)
	assert.Empty(t, unlisted.AutoBlockReasons)
}

// TestCOV_SCHEMA_028_ParseVerificationReportErrorPaths drives both rejection
// branches of ParseVerificationReport.
func TestCOV_SCHEMA_028_ParseVerificationReportErrorPaths(t *testing.T) {
	valid := []byte(`{"schema":"docsync.verification-report/v1","run_id":"run_01J9ZK6X8A",` +
		`"doc_path":"docs/payments.md","patch_ref":"state/docs/payments.md#p1",` +
		`"tiers":[{"tier":1,"status":"pass","checks":[]},{"tier":2,"status":"pass","checks":[]},{"tier":3,"status":"pass","checks":[]}],` +
		`"claims":[],"verdict":"pass","auto_eligible":true,"auto_block_reasons":[],"policy_version":17}`)
	parsed, err := schemas.ParseVerificationReport(valid)
	require.NoError(t, err)
	assert.Equal(t, schemas.VerdictPass, parsed.Verdict)

	_, err = schemas.ParseVerificationReport([]byte(`{"nope":1}`))
	require.Error(t, err)

	invalid := []byte(`{"schema":"docsync.verification-report/v1","run_id":"",` +
		`"doc_path":"docs/payments.md","patch_ref":"p","tiers":[],"claims":[],` +
		`"verdict":"pass","auto_eligible":true,"auto_block_reasons":[],"policy_version":17}`)
	_, err = schemas.ParseVerificationReport(invalid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tiers: tiers must contain exactly three entries")
}

// TestCOV_SCHEMA_029_CanonicalReportRowValidate covers every guard of the
// canonical-report row validator.
func TestCOV_SCHEMA_029_CanonicalReportRowValidate(t *testing.T) {
	valid := schemas.CanonicalReportRow{
		DocPath:           "docs/payments.md",
		Glyph:             schemas.ReportGlyphWarn,
		Outcome:           schemas.PipelineOutcomeAuto,
		Confidence:        covPtr(0.96),
		ConfidenceDisplay: covPtr("96%"),
		ModelBinding:      covPtr("z-ai/glm-4.7"),
		Detail:            covPtr("below the review threshold"),
		Evidence:          []string{"routing:CONTINUE"},
	}
	assert.Empty(t, valid.Validate())

	badGlyph := valid
	badGlyph.Glyph = schemas.ReportGlyph("critical")
	issues := badGlyph.Validate()
	require.NotNil(t, covIssueAt(issues, "glyph"))
	assert.Contains(t, covIssueAt(issues, "glyph").Message, "Invalid enum value. Expected")

	badOutcome := valid
	badOutcome.Outcome = schemas.PipelineOutcome("queued")
	require.NotNil(t, covIssueAt(badOutcome.Validate(), "outcome"))

	badConfidence := valid
	badConfidence.Confidence = covPtr(1.5)
	issues = badConfidence.Validate()
	require.NotNil(t, covIssueAt(issues, "confidence"))
	assert.Equal(t, "Number must be less than or equal to 1", covIssueAt(issues, "confidence").Message)

	negativeConfidence := valid
	negativeConfidence.Confidence = covPtr(-0.1)
	issues = negativeConfidence.Validate()
	require.NotNil(t, covIssueAt(issues, "confidence"))
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "confidence").Message)

	emptyOptionals := schemas.CanonicalReportRow{DocPath: "", Glyph: "", Outcome: ""}
	issues = emptyOptionals.Validate()
	require.NotNil(t, covIssueAt(issues, "docPath"))
	require.NotNil(t, covIssueAt(issues, "glyph"))

	blankOptional := valid
	blankOptional.ConfidenceDisplay = covPtr("")
	require.NotNil(t, covIssueAt(blankOptional.Validate(), "confidenceDisplay"))

	blankBinding := valid
	blankBinding.ModelBinding = covPtr("")
	require.NotNil(t, covIssueAt(blankBinding.Validate(), "modelBinding"))

	blankDetail := valid
	blankDetail.Detail = covPtr("")
	require.NotNil(t, covIssueAt(blankDetail.Validate(), "detail"))

	blankEvidence := valid
	blankEvidence.Evidence = []string{""}
	require.NotNil(t, covIssueAt(blankEvidence.Validate(), "evidence", "0"))

	// nil optional pointers skip their guards entirely.
	nilOptionals := schemas.CanonicalReportRow{DocPath: "docs/api.md", Glyph: schemas.ReportGlyphOk, Outcome: schemas.PipelineOutcomeIgnore}
	assert.Empty(t, nilOptionals.Validate())
}

// TestCOV_SCHEMA_030_CanonicalReportRootValidate covers the root report
// guards (run id, key nesting, status enum, optional generated-at, policy
// version and every repeated block).
func TestCOV_SCHEMA_030_CanonicalReportRootValidate(t *testing.T) {
	example := schemas.CanonicalReportExample()
	assert.Empty(t, example.Validate())

	badRun := example
	badRun.RunID = ""
	issues := badRun.Validate()
	require.NotNil(t, covIssueAt(issues, "runId"))

	badKey := example
	badKey.Key.Repo = ""
	require.NotNil(t, covIssueAt(badKey.Validate(), "key", "repo"))

	badStatus := example
	badStatus.Status = schemas.RunStatus("aborted")
	require.NotNil(t, covIssueAt(badStatus.Validate(), "status"))

	badGeneratedAt := example
	badGeneratedAt.GeneratedAt = covPtr("2026-10-04T22:30:47")
	require.NotNil(t, covIssueAt(badGeneratedAt.Validate(), "generatedAt"))

	badVersion := example
	badVersion.PolicyVersion = 0
	require.NotNil(t, covIssueAt(badVersion.Validate(), "policyVersion"))

	badBindings := example
	badBindings.ModelBindings = []schemas.RunModelBinding{{Role: "not-a-role", Model: ""}}
	issues = badBindings.Validate()
	require.NotNil(t, covIssueAt(issues, "modelBindings", "0", "role"))
	require.NotNil(t, covIssueAt(issues, "modelBindings", "0", "model"))

	badRow := example
	badRow.Rows = []schemas.CanonicalReportRow{{DocPath: "", Glyph: "", Outcome: ""}}
	require.NotNil(t, covIssueAt(badRow.Validate(), "rows", "0", "docPath"))

	badAbstain := example
	badAbstain.Abstains = []schemas.RunAbstain{{AbstainID: "", Reason: "", Detail: ""}}
	require.NotNil(t, covIssueAt(badAbstain.Validate(), "abstains", "0", "abstain_id"))

	badFinding := example
	badFinding.Findings = []schemas.RunFinding{{Kind: "", Detail: "", PatchRef: covPtr("")}}
	issues = badFinding.Validate()
	require.NotNil(t, covIssueAt(issues, "findings", "0", "kind"))
	require.NotNil(t, covIssueAt(issues, "findings", "0", "patch_ref"))

	badDegradation := example
	badDegradation.Degradations = []schemas.Degradation{{Stage: "", Kind: "nope", Detail: ""}}
	issues = badDegradation.Validate()
	require.NotNil(t, covIssueAt(issues, "degradations", "0", "stage"))
	require.NotNil(t, covIssueAt(issues, "degradations", "0", "kind"))

	blankCoverage := example
	blankCoverage.UnknownCoverage = []string{""}
	require.NotNil(t, covIssueAt(blankCoverage.Validate(), "unknownCoverage", "0"))

	emptyCoverage := example
	emptyCoverage.UnknownCoverage = nil
	assert.Empty(t, emptyCoverage.Validate())

	badUsage := example
	badUsage.Usage = &schemas.RunUsage{PromptTokens: -1, CompletionTokens: -1, TotalTokens: covPtr(-1), Cost: covPtr(float64(-0.5)), PolicyVersion: 0}
	issues = badUsage.Validate()
	require.NotNil(t, covIssueAt(issues, "usage", "prompt_tokens"))
	require.NotNil(t, covIssueAt(issues, "usage", "completion_tokens"))
	require.NotNil(t, covIssueAt(issues, "usage", "total_tokens"))
	require.NotNil(t, covIssueAt(issues, "usage", "cost"))
	require.NotNil(t, covIssueAt(issues, "usage", "policy_version"))
}
