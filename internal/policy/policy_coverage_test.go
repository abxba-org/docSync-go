package policy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func covStrPtr(value string) *string { return &value }

func covIntPtr(value int) *int { return &value }

func covImpactPtr(value schemas.DocumentationImpact) *schemas.DocumentationImpact {
	return &value
}

type covArchPort struct {
	drafts []policy.PolicyArchitectDraft
	calls  int
	priors []*policy.PriorFailedAttempt
}

func (p *covArchPort) Propose(
	input policy.ArchitectLoopInputs,
) (policy.PolicyArchitectDraft, error) {
	p.calls++
	if p.calls > len(p.drafts) {
		return policy.PolicyArchitectDraft{}, fmt.Errorf(
			"coverage architect port: unexpected request identity (call %d)", p.calls)
	}
	p.priors = append(p.priors, input.PriorFailedAttempt)
	return p.drafts[p.calls-1], nil
}

type covExtPort struct {
	drafts []policy.ClaimExtractorDraft
	calls  int
	priors []*policy.PriorExtractionAttempt
}

func (p *covExtPort) Extract(
	input policy.ClaimExtractorLoopInputs,
) (policy.ClaimExtractorDraft, error) {
	p.calls++
	if p.calls > len(p.drafts) {
		return policy.ClaimExtractorDraft{}, fmt.Errorf(
			"coverage extractor port: unexpected request identity (call %d)", p.calls)
	}
	p.priors = append(p.priors, input.PriorFailedAttempt)
	return p.drafts[p.calls-1], nil
}

func covBadULIDs() policy.ArchitectNewId {
	return func() (string, error) { return "not-a-ulid", nil }
}

func covFailingIDs(failOnCall int) policy.ArchitectNewId {
	calls := 0
	return func() (string, error) {
		calls++
		if calls == failOnCall {
			return "", fmt.Errorf("coverage id factory exhausted on call %d", calls)
		}
		id := fmt.Sprintf("01HTXSCHEMA1EXAMP1ES%06d0", calls)
		if len(id) > 26 {
			id = id[:26]
		}
		return id, nil
	}
}

func TestCOV_POLICY_001_ArchitectFallbackProposalFailsSchemaValidationFailsClosed(t *testing.T) {
	runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
		Clock:       archClock,
		NewID:       covBadULIDs(),
		MaxAttempts: 1,
	})
	require.NoError(t, err)
	port := &covArchPort{drafts: []policy.PolicyArchitectDraft{archValidDraft()}}
	outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
		Port:               port,
		Inputs:             archInputs(),
		PolicyVersionBasis: 1,
	})
	require.Error(t, err)
	require.False(t, outcome.OK)
	require.Empty(t, outcome.Diagnostics)
	require.Empty(t, outcome.Proposal.ProposalID)
	var archErr *policy.PolicyArchitectError
	require.True(t, errors.As(err, &archErr),
		"expected a *policy.PolicyArchitectError, got %T: %v", err, err)
	require.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
	require.Contains(t, err.Error(), "EDOCSYNC_VALIDATION")
	require.Contains(t, err.Error(),
		"fallback invalid proposal failed schema validation — fail closed")
}

func TestCOV_POLICY_002_ArchitectIDMintingFailuresFailClosed(t *testing.T) {
	t.Run("the first proposal-id mint failure is a coded internal error", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock:       archClock,
			NewID:       covFailingIDs(1),
			MaxAttempts: 1,
		})
		require.NoError(t, err)
		_, err = runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Port:               &covArchPort{drafts: []policy.PolicyArchitectDraft{archValidDraft()}},
			Inputs:             archInputs(),
			PolicyVersionBasis: 1,
		})
		require.Error(t, err)
		require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "architect runner could not mint a proposal id")
	})

	t.Run("the fallback-id mint failure inside the schema-failure path fails closed", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock:       archClock,
			NewID:       covFailingIDs(2),
			MaxAttempts: 1,
		})
		require.NoError(t, err)
		_, err = runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Port:               &covArchPort{drafts: []policy.PolicyArchitectDraft{archValidDraft()}},
			Inputs:             archInputs(),
			PolicyVersionBasis: 0,
		})
		require.Error(t, err)
		require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "architect runner could not mint a proposal id")
	})
}

func TestCOV_POLICY_003_ArchitectRetryLoopFeedsThePriorFailedAttemptBack(t *testing.T) {
	runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
		Clock:       archClock,
		NewID:       archFixedIDs(),
		MaxAttempts: 2,
	})
	require.NoError(t, err)
	invalidDraft := archValidDraft()
	invalidDraft.Type = "not-a-proposal-type"
	port := &covArchPort{drafts: []policy.PolicyArchitectDraft{invalidDraft, archValidDraft()}}
	outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
		Port:               port,
		Inputs:             archInputs(),
		PolicyVersionBasis: 1,
	})
	require.NoError(t, err)
	require.True(t, outcome.OK)
	require.Equal(t, schemas.PolicyProposalStatusValidated, outcome.Proposal.Status)
	require.Len(t, port.priors, 2)
	require.Nil(t, port.priors[0], "the first attempt carries no prior failure")
	require.NotNil(t, port.priors[1], "the retry must receive the prior failed attempt (ARCH §8.2)")
	require.Equal(t, "not-a-proposal-type", port.priors[1].Type)
	require.NotEmpty(t, port.priors[1].Diagnostics)
	require.Contains(t, port.priors[1].Diagnostics[0], "proposal schema: ")
}

func TestCOV_POLICY_004_ArchitectSchemaRejectedDraftReturnsAnInvalidMarkerProposal(t *testing.T) {
	runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
		Clock:       archClock,
		NewID:       archFixedIDs(),
		MaxAttempts: 1,
	})
	require.NoError(t, err)
	draft := archValidDraft()
	draft.CountedEvidence = nil
	outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
		Port:               &covArchPort{drafts: []policy.PolicyArchitectDraft{draft}},
		Inputs:             archInputs(),
		PolicyVersionBasis: 1,
	})
	require.NoError(t, err)
	require.False(t, outcome.OK, "a draft without counted evidence cannot validate")
	require.Equal(t, policy.ArchitectRejectReasonSchema, outcome.Reason)
	require.Equal(t, schemas.PolicyProposalStatusInvalid, outcome.Proposal.Status)
	require.Len(t, outcome.Proposal.CountedEvidence, 1,
		"the invalid fallback marker carries a truthful 1/1 counted-evidence row")
	require.Equal(t, 1, outcome.Proposal.CountedEvidence[0].SupportCount)
	require.Equal(t, 1, outcome.Proposal.CountedEvidence[0].TotalCount)
	require.NotEmpty(t, outcome.Diagnostics)
	require.Contains(t, outcome.Diagnostics[0], "proposal schema: ")
}

func TestCOV_POLICY_005_ArchitectDefaultIDFactoryProducesValidProposals(t *testing.T) {
	runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
		Clock: archClock,
	})
	require.NoError(t, err)
	outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
		Port:               &covArchPort{drafts: []policy.PolicyArchitectDraft{archValidDraft()}},
		Inputs:             archInputs(),
		PolicyVersionBasis: 1,
	})
	require.NoError(t, err)
	require.True(t, outcome.OK)
	require.Len(t, outcome.Proposal.ProposalID, 26)
	queued := runner.QueueProposal(outcome.Proposal)
	require.Equal(t, schemas.PolicyProposalStatusQueued, queued.Status)
}

func TestCOV_POLICY_006_ExtractorFallbackProposalFailsSchemaValidationFailsClosed(t *testing.T) {
	runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
		Clock:       extClock,
		NewID:       covBadULIDs(),
		MaxAttempts: 1,
	})
	require.NoError(t, err)
	outcome, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
		Port:   &covExtPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
		Inputs: extInputs(),
	})
	require.Error(t, err)
	require.False(t, outcome.OK)
	require.Empty(t, outcome.Diagnostics)
	var extractorErr *policy.ClaimExtractorError
	require.True(t, errors.As(err, &extractorErr),
		"expected a *policy.ClaimExtractorError, got %T: %v", err, err)
	require.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
	require.Contains(t, err.Error(), "EDOCSYNC_VALIDATION")
	require.Contains(t, err.Error(),
		"fallback invalid claim proposal failed schema validation — fail closed")
}

func TestCOV_POLICY_007_ExtractorIDMintingFailuresFailClosed(t *testing.T) {
	t.Run("the first proposal-id mint failure is a coded internal error", func(t *testing.T) {
		runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock:       extClock,
			NewID:       covFailingIDs(1),
			MaxAttempts: 1,
		})
		require.NoError(t, err)
		_, err = runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &covExtPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
			Inputs: extInputs(),
		})
		require.Error(t, err)
		require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "claim extractor runner could not mint an id")
	})

	t.Run("the fallback-id mint failure inside the schema-failure path fails closed", func(t *testing.T) {
		runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock:       extClock,
			NewID:       covFailingIDs(3),
			MaxAttempts: 1,
		})
		require.NoError(t, err)
		invalid := extGoodDraft()
		invalid.Claims[0].ClaimText = ""
		_, err = runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &covExtPort{drafts: []policy.ClaimExtractorDraft{invalid}},
			Inputs: extInputs(),
		})
		require.Error(t, err)
		require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "claim extractor runner could not mint an id")
	})
}

func TestCOV_POLICY_008_ExtractorDefaultIDFactoryProducesValidProposals(t *testing.T) {
	runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
		Clock: extClock,
	})
	require.NoError(t, err)
	outcome, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
		Port:   &covExtPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
		Inputs: extInputs(),
	})
	require.NoError(t, err)
	require.True(t, outcome.OK)
	require.Len(t, outcome.Proposal.Claims, 1)
	require.Len(t, outcome.Proposal.Claims[0].ClaimID, 26)
}

func TestCOV_POLICY_009_ExtractorTruthGateSkipsPreviouslyVerifiedClaimsAndFlagsBadQuotes(t *testing.T) {
	runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
		Clock: extClock,
		NewID: extFixedIDs(),
	})
	require.NoError(t, err)
	proposals := make([]schemas.ClaimProposal, 0, 3)
	for i := 0; i < 3; i++ {
		outcome, proposeErr := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &covExtPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
			Inputs: extInputs(),
		})
		require.NoError(t, proposeErr)
		require.True(t, outcome.OK)
		proposals = append(proposals, outcome.Proposal)
	}

	alreadyVerified := proposals[0].Claims[0].ClaimID
	tampered := proposals[1].Claims[0]
	tampered.ClaimText = "Refunds require a UNIQUE idempotency key."
	tampered.SourceEvidence.Quote = "Refunds require a UNIQUE idempotency key."
	tampered.CandidateAnchor = schemas.ClaimCandidateAnchor{
		SectionHeading: covStrPtr("## Refunds"),
	}
	freshClaim := proposals[2].Claims[0]
	freshClaim.ClaimText = "Webhook retries use exponential backoff."
	freshClaim.CandidateAnchor = schemas.ClaimCandidateAnchor{
		SectionHeading: covStrPtr("## Webhooks"),
	}
	freshClaim.SourceEvidence = schemas.ClaimSourceEvidence{
		Quote:   "Webhook retries use exponential backoff.",
		DocPath: "docs/payments.md",
	}

	proposal := proposals[0]
	proposal.Claims = []schemas.ExtractedClaim{proposals[0].Claims[0], tampered, freshClaim}
	proposal.VerifiedClaims = []schemas.VerifiedClaim{{
		ClaimID:      alreadyVerified,
		VerifiedAt:   extClock(),
		Verification: "source_confirmed",
	}}
	gate, err := runner.VerifyProposals(policy.ClaimVerifyInput{
		Proposal:     proposal,
		DocContent:   extDocContent(),
		DocStructure: extDocStructure(),
	})
	require.NoError(t, err)
	require.Len(t, gate.VerifiedClaims, 2,
		"the pre-verified row is carried and the fresh claim is source-confirmed")
	require.Equal(t, alreadyVerified, gate.VerifiedClaims[0].ClaimID)
	require.Equal(t, freshClaim.ClaimID, gate.VerifiedClaims[1].ClaimID)
	require.Len(t, gate.Unverified, 1,
		"the tampered quote is unverifiable and explicitly flagged")
	require.Equal(t, tampered.ClaimID, gate.Unverified[0].ClaimID)
	require.Equal(t, policy.ClaimRejectReasonQuoteNotFound, gate.Unverified[0].Reason)
	require.Contains(t, gate.Unverified[0].Diagnostics[0], "source_evidence.quote not found verbatim")
}

func TestCOV_POLICY_010_ExtractorTruthGateResolvesTheContentHashAnchorForm(t *testing.T) {
	runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
		Clock: extClock,
		NewID: extFixedIDs(),
	})
	require.NoError(t, err)
	outcome, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
		Port:   &covExtPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
		Inputs: extInputs(),
	})
	require.NoError(t, err)
	require.True(t, outcome.OK)
	proposal := outcome.Proposal
	lineDigest := policy.CollapsedSha256("Refunds require an idempotency key.")
	proposal.Claims[0].CandidateAnchor = schemas.ClaimCandidateAnchor{
		SectionHeading: covStrPtr("Nonexistent heading"),
		LineHint:       covIntPtr(9999),
		ContentHash:    covStrPtr(lineDigest),
	}
	gate, err := runner.VerifyProposals(policy.ClaimVerifyInput{
		Proposal:     proposal,
		DocContent:   extDocContent(),
		DocStructure: extDocStructure(),
	})
	require.NoError(t, err)
	require.Empty(t, gate.Unverified)
	require.Len(t, gate.VerifiedClaims, 1,
		"the content_hash anchor form resolves against a line digest (TS || chain)")
}

func TestCOV_POLICY_011_LearningModeConfigFailuresSurfaceCodedValidationErrors(t *testing.T) {
	cases := []struct {
		name       string
		repoConfig any
		contains   string
	}{
		{
			name:       "an unrecognized root key without learning rejects",
			repoConfig: map[string]any{"bogus": true},
			contains:   `Unrecognized key: "bogus"`,
		},
		{
			name:       "an unrecognized learning key without mode rejects",
			repoConfig: map[string]any{"learning": map[string]any{"bogus": true}},
			contains:   `learning: Unrecognized key: "bogus"`,
		},
		{
			name:       "a null learning.mode is an invalid enum value (received null)",
			repoConfig: map[string]any{"learning": map[string]any{"mode": nil}},
			contains:   "learning.mode: Invalid enum value",
		},
		{
			name:       "an object learning.mode is an invalid enum value (received object)",
			repoConfig: map[string]any{"learning": map[string]any{"mode": map[string]any{}}},
			contains:   "received object",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
				RepoConfig: testCase.repoConfig,
			})
			require.Error(t, err)
			var validationErr *policy.LearningModeValidationError
			require.True(t, errors.As(err, &validationErr),
				"expected a *policy.LearningModeValidationError, got %T: %v", err, err)
			require.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
			require.Contains(t, err.Error(), "learning config invalid: ")
			require.Contains(t, err.Error(), testCase.contains)
		})
	}
}

func TestCOV_POLICY_012_LearningModeEscalationWithRecordedGateEvidenceResolvesCanary(t *testing.T) {
	gatesPassed := true
	resolution, err := policy.ResolveLearningMode(policy.LearningModeResolutionInput{
		RepoConfig:                 map[string]any{"learning": map[string]any{"mode": "canary"}},
		LatestPromotionGatesPassed: &gatesPassed,
	})
	require.NoError(t, err)
	require.Equal(t, policy.LearningModeCanary, resolution.Mode)
	require.Equal(t, policy.LearningModeSourceRepoConfig, resolution.Source)
	require.NotEmpty(t, resolution.Escalations)
	require.Contains(t, resolution.Escalations[0], "gates re-run inside the pipeline")
}

func TestCOV_POLICY_013_Stage1VerdictFailsClosedOnOutOfDomainImpactSignals(t *testing.T) {
	t.Run("an out-of-domain analysis impact without an answer impact fails closed", func(t *testing.T) {
		_, err := policy.ResolveStage1Verdict(policy.JevRoutingSignals{
			ChangeKind:     schemas.ChangeKindBehavioral,
			AnalysisImpact: schemas.DocumentationImpact("drifted"),
			Risk:           schemas.RiskLevelLow,
		})
		require.Error(t, err)
		require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
		require.Contains(t, err.Error(), "JEV_ROUTING_TABLE is not total")
		require.Contains(t, err.Error(), `"analysisImpact":"drifted"`)
	})

	t.Run("an agreeing out-of-domain answer impact still fails closed with the signal JSON", func(t *testing.T) {
		_, err := policy.ResolveStage1Verdict(policy.JevRoutingSignals{
			ChangeKind:     schemas.ChangeKindBehavioral,
			AnalysisImpact: schemas.DocumentationImpact("drifted"),
			AnswerImpact:   covImpactPtr(schemas.DocumentationImpact("drifted")),
			Risk:           schemas.RiskLevelLow,
			Abstained:      false,
		})
		require.Error(t, err)
		require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
		require.Contains(t, err.Error(), `"answerImpact":"drifted"`)
		require.Contains(t, err.Error(), `"abstained":false`)
	})
}

type covDecisionPort struct {
	result policy.JevPortResult
	calls  int
}

func (p *covDecisionPort) Decide(input policy.JevPortInput) (policy.JevPortResult, error) {
	p.calls++
	return p.result, nil
}

func TestCOV_POLICY_014_RunJevFailsClosedWhenTheRoutingTableCannotResolveTheSignals(t *testing.T) {
	questions := jevUniversalQuestions()
	for index := range questions {
		if questions[index].Key == "documentation_impact" {
			questions[index].Choices = []string{"drifted"}
		}
	}
	answers := map[string]schemas.Answer{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.9},
		"api_contract_changed":    {Value: json.RawMessage("false"), Probability: 0.9},
		"affected_doc_count":      {Value: json.RawMessage("3"), Probability: 0.9},
		"documentation_impact":    {Value: json.RawMessage(`"drifted"`), Probability: 0.9},
	}
	port := &covDecisionPort{result: policy.JevPortResult{
		Kind: policy.JevPortResultKindDecision,
		Decision: &schemas.Decision{
			Schema:              schemas.DecisionSchemaID,
			DecisionID:          "d_cov_drift",
			PolicyVersion:       1,
			Change:              schemas.DecisionChange{Commit: "81af92e", DiffRef: "git:81af92e", ChangeAnalysisRef: "analysis/01J9ZK6X8A"},
			ChangeKind:          schemas.ChangeKindBehavioral,
			DocumentationImpact: schemas.DocumentationImpact("drifted"),
			Risk:                schemas.RiskLevelLow,
			Confidence:          0.9,
			Answers:             answers,
			Candidates:          []string{"docs/payments.md"},
			Action:              schemas.ActionReview,
			Evidence:            []schemas.Evidence{},
			Trace:               schemas.DecisionTrace{Model: "provider/routed", PolicyHash: "sha256:" + docutil.ContentHash("policy"), LatencyMS: 120},
		},
	}}
	analysis := jevAnalysisContext()
	analysis.DocumentationImpact = schemas.DocumentationImpact("drifted")
	analysis.Answers = nil
	outcome, err := policy.RunJev(policy.RunJevInput{
		Compiled:        policy.CompiledPolicyWorkflow{Questions: questions},
		Port:            port,
		Change:          jevChange(),
		AnalysisContext: analysis,
	})
	require.Error(t, err)
	require.Empty(t, outcome.Kind)
	require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
	require.Contains(t, err.Error(), "JEV_ROUTING_TABLE is not total")
}

func TestCOV_POLICY_015_AnswerContractGateNamesTheJSONNullTypeOfABooleanAnswer(t *testing.T) {
	questions := []policy.Question{{
		Key:    "public_behavior_changed",
		Type:   policy.QuestionTypeBoolean,
		Prompt: "Did the change alter runtime behavior visible to users of this repository?",
	}}
	err := policy.AssertAnswerContract(questions, map[string]schemas.Answer{
		"public_behavior_changed": {Value: json.RawMessage("null"), Probability: 0.9},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(),
		"answer 'public_behavior_changed' is a boolean question — value must be true|false (got object)")
}

func TestCOV_POLICY_016_MapOutcomeFailsClosedOnAnUnknownRiskBucket(t *testing.T) {
	decision := outcomeBaseDecision()
	decision.Risk = schemas.RiskLevel("extreme")
	_, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: decision,
		Compiled: outcomeCompiled(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	require.Error(t, err)
	require.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
	var rangeErr *policy.EngineRangeError
	require.True(t, errors.As(err, &rangeErr),
		"expected a *policy.EngineRangeError, got %T: %v", err, err)
	require.Contains(t, err.Error(), "no matrix row for risk bucket 'extreme'")
}

func TestCOV_POLICY_017_AggregateAnswersRulesAndFailClosedGuards(t *testing.T) {
	empty, err := policy.AggregateAnswers(policy.JevAnswers{})
	require.NoError(t, err)
	require.Equal(t, 0.0, empty.Confidence)
	require.Equal(t, policy.ConfidenceRuleEmptyAnswersFloor, empty.Rule)

	deciding := policy.JevAnswers{
		"documentation_impact": {Value: json.RawMessage(`"possible"`), Probability: 0.83},
	}
	aggregated, err := policy.AggregateAnswers(deciding)
	require.NoError(t, err)
	require.Equal(t, 0.83, aggregated.Confidence)
	require.Equal(t, policy.ConfidenceRuleDecidingAnswerProbability, aggregated.Rule)

	weakest := policy.JevAnswers{
		"api_contract_changed": {Value: json.RawMessage("true"), Probability: 0.99},
		"affected_doc_count":   {Value: json.RawMessage("3"), Probability: 0.42},
	}
	minAggregated, err := policy.AggregateAnswers(weakest)
	require.NoError(t, err)
	require.Equal(t, 0.42, minAggregated.Confidence, "the weakest link gates the decision")
	require.Equal(t, policy.ConfidenceRuleMinAnswerProbability, minAggregated.Rule)

	_, err = policy.AggregateAnswers(policy.JevAnswers{
		"api_contract_changed": {Value: json.RawMessage("true"), Probability: math.NaN()},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "probability must be a finite number")
}
