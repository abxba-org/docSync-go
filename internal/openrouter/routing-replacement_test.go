package openrouter_test

// routing-replacement_test.go — Go port of
// packages/openrouter/test/p12-t11-replacement-shadow.test.ts (P12-T11; ARCH
// §10.3; R2; D9): a replacement model is shadow-evaluated through the SAME
// canary-shaped machinery — a model swap IS a policy change.
//
// The openrouter-side half runs the paired evaluation through an injected port
// (recorded double, OD-2) and emits validated `docsync.shadow-run-record/v1`
// rows — the record type the policy-side canary lifecycle consumes. The
// composition seam is the RECORD FORMAT; the openrouter runtime modules stay
// policy-free.

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

const replacementClock = "2026-10-09T00:00:00.000Z"

// deterministicRecordId is a deterministic ULID-shaped record id — the same
// construction as the policy-side shadow sampler (sha256(seed) over the
// Crockford base32 alphabet; TS deterministicRecordId).
func deterministicRecordId(seed string) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	digest := sha256.Sum256([]byte(seed))
	id := make([]byte, 0, 26)
	for index := 0; index < 26; index++ {
		value := byte(0)
		if index < len(digest) {
			value = digest[index]
		}
		id = append(id, alphabet[int(value)%len(alphabet)])
	}
	return string(id)
}

// recordedCase is what the double saw (TS recordedPort's `seen`).
type recordedCase struct {
	CaseID   string
	OldModel string
	NewModel string
}

// replacementScriptedOutcome is one scripted paired outcome (TS recordedPort's map value).
type replacementScriptedOutcome struct {
	Action schemas.Action
	Agree  bool
	// OldReason / NewReason optionally abstain on that side; the TS
	// sameOutcome compares `abstain_reason ?? undefined`, so equal reasons
	// agree and different ones disagree.
	OldReason *schemas.AbstainReason
	NewReason *schemas.AbstainReason
}

// recordedEvaluationPort is the recorded port double (TS recordedPort):
// deterministic paired outcomes per case, never a network.
type recordedEvaluationPort struct {
	outcomes map[string]replacementScriptedOutcome
	seen     []recordedCase
	calls    int
}

func newRecordedEvaluationPort(t *testing.T, outcomes map[string]replacementScriptedOutcome) *recordedEvaluationPort {
	t.Helper()
	return &recordedEvaluationPort{outcomes: outcomes}
}

func (p *recordedEvaluationPort) PortID() string { return "recorded-double/v1" }

func (p *recordedEvaluationPort) RunCase(
	input openrouter.ReplacementCaseInput,
) (openrouter.ReplacementCaseResult, error) {
	p.calls++
	p.seen = append(p.seen, recordedCase{
		CaseID:   input.CaseID,
		OldModel: input.OldModel,
		NewModel: input.NewModel,
	})
	scripted, ok := p.outcomes[input.CaseID]
	if !ok {
		return openrouter.ReplacementCaseResult{}, &unscriptedCaseError{caseID: input.CaseID}
	}
	oldOutcome := schemas.ShadowDecision{Action: schemas.ActionPatch}
	newOutcome := oldOutcome
	if !scripted.Agree {
		newOutcome = schemas.ShadowDecision{Action: scripted.Action}
	}
	if scripted.OldReason != nil {
		oldOutcome.Abstained = true
		oldOutcome.AbstainReason = scripted.OldReason
	}
	if scripted.NewReason != nil {
		newOutcome.Abstained = true
		newOutcome.AbstainReason = scripted.NewReason
	}
	return openrouter.ReplacementCaseResult{
		OldOutcome: oldOutcome,
		NewOutcome: newOutcome,
		InputHashes: schemas.ShadowRecordedInputHashes{
			// Real 64-hex digests (docutil.ContentHash — the identical-input
			// hash helper), standing in for the TS 'a'.repeat(64) literals.
			ChangeAnalysis: docutil.ContentHash("change-analysis:" + input.CaseID),
			CandidateSet:   docutil.ContentHash("candidate-set:" + input.CaseID),
			DiffSubset:     docutil.ContentHash("diff-subset:" + input.CaseID),
		},
	}, nil
}

// unscriptedCaseError is the double's loud failure on an unscripted case (TS
// `throw new Error('unscripted case ...')`).
type unscriptedCaseError struct{ caseID string }

func (e *unscriptedCaseError) Error() string { return "unscripted case " + e.caseID }

func replacementBaseInput(port openrouter.ReplacementEvaluationPort) openrouter.EvaluateReplacementModelInput {
	return openrouter.EvaluateReplacementModelInput{
		Role:              "patch-writer",
		Repo:              "acme/widgets",
		RunID:             "replacement-shadow-patch-writer-v3-v2",
		ProductionVersion: 2,
		CandidateVersion:  3,
		OldBinding: schemas.ReplacementModelBinding{
			Primary:   "anthropic/claude-sonnet-4.5",
			Fallbacks: []string{"anthropic/claude-3.7-sonnet"},
		},
		NewBinding:       schemas.ReplacementModelBinding{Primary: "openai/gpt-5.1"},
		Cases:            replacementCases("case-001", "case-002"),
		Port:             port,
		Clock:            func() string { return replacementClock },
		NewRecordID:      deterministicRecordId,
		TimeBoxDays:      14,
		MinAgreementRate: 0.9,
	}
}

func replacementCases(caseIDs ...string) []schemas.ReplacementEvalCaseInput {
	buckets := []schemas.RiskBucket{
		schemas.RiskBucketHigh, schemas.RiskBucketMedium,
		schemas.RiskBucketLow, schemas.RiskBucketHigh,
	}
	cases := make([]schemas.ReplacementEvalCaseInput, 0, len(caseIDs))
	for index, caseID := range caseIDs {
		bucket := buckets[index%len(buckets)]
		cases = append(cases, schemas.ReplacementEvalCaseInput{CaseID: caseID, RiskBucket: bucket})
	}
	return cases
}

func TestEvaluateReplacementModel(t *testing.T) {
	t.Run("runs every case through the injected port under BOTH bindings and emits canary-shaped records", func(t *testing.T) {
		port := newRecordedEvaluationPort(t, map[string]replacementScriptedOutcome{
			"case-001": {Action: schemas.ActionPatch, Agree: true},
			"case-002": {Action: schemas.ActionPatch, Agree: true},
		})
		result, err := openrouter.EvaluateReplacementModel(replacementBaseInput(port))
		require.NoError(t, err)

		// Every case ran under BOTH bindings, in input order.
		require.Equal(t, []recordedCase{
			{CaseID: "case-001", OldModel: "anthropic/claude-sonnet-4.5", NewModel: "openai/gpt-5.1"},
			{CaseID: "case-002", OldModel: "anthropic/claude-sonnet-4.5", NewModel: "openai/gpt-5.1"},
		}, port.seen)

		assert.Equal(t, "pass", result.Report.Verdict)
		assert.Equal(t, schemas.ReplacementEvalAgreement{Total: 2, Agreed: 2, Disagreed: 0, Rate: 1}, result.Report.Agreement)
		assert.Equal(t, "docsync.routing-replacement-report/v1", result.Report.Schema)
		assert.Equal(t, schemas.ReplacementEvalPolicy{ProductionVersion: 2, CandidateVersion: 3}, result.Report.Policy)
		assert.Equal(t, "replacement_model_shadow_evaluation", result.Report.Kind)
		assert.Equal(t, replacementClock, result.Report.CreatedAt)
		assert.Equal(t, "2026-10-23T00:00:00.000Z", result.Report.ExpiresAt)

		require.Len(t, result.CanaryRecords, 2)
		for _, record := range result.CanaryRecords {
			// Canary-shaped: the policy-side lifecycle consumes these records
			// verbatim, so each one must satisfy the registered
			// `docsync.shadow-run-record/v1` envelope it validates first.
			require.Empty(t, record.Validate())
			assert.Equal(t, "docsync.shadow-run-record/v1", record.Schema)
			assert.True(t, record.ShadowOnly)
			assert.True(t, record.Sampled)
			assert.Equal(t, "sha256-hex-threshold/v1", string(record.Sampling.Sampler))
			assert.Equal(t, float64(1), record.Sampling.Fraction)
			assert.Equal(t, "replacement-shadow-patch-writer-v3-v2", record.RunID)
			// The deterministic draw: sha256(run_id ⊕ NUL ⊕ case_id), first 8
			// hex chars — identical formula to the policy-side sampler.
			sum := sha256.Sum256([]byte("replacement-shadow-patch-writer-v3-v2\x00" + record.PrID))
			assert.Equal(t, hex.EncodeToString(sum[:])[:8], record.Sampling.DrawHex)
			assert.True(t, docutil.IsValidULID(record.RecordID))
			assert.Equal(t, "acme/widgets", record.Repo)
			assert.Equal(t, replacementClock, record.CreatedAt)
			assert.Equal(t, "2026-10-23T00:00:00.000Z", record.ExpiresAt)
		}
	})

	t.Run("a disagreeing workload fails the agreement verdict (caller-supplied threshold — D11)", func(t *testing.T) {
		port := newRecordedEvaluationPort(t, map[string]replacementScriptedOutcome{
			"case-001": {Action: schemas.ActionReview, Agree: false},
			"case-002": {Action: schemas.ActionPatch, Agree: true},
		})
		result, err := openrouter.EvaluateReplacementModel(replacementBaseInput(port))
		require.NoError(t, err)
		assert.Equal(t, "fail", result.Report.Verdict)
		assert.Equal(t, schemas.ReplacementEvalAgreement{Total: 2, Agreed: 1, Disagreed: 1, Rate: 0.5}, result.Report.Agreement)
		require.NotEmpty(t, result.Report.Cases)
		assert.False(t, result.Report.Cases[0].Agreed)
		require.NotNil(t, result.CanaryRecords[0].Decision)
		assert.Equal(t, schemas.ShadowDecision{Action: schemas.ActionReview}, *result.CanaryRecords[0].Decision)
	})

	t.Run("the same abstain reason on BOTH sides still agrees (nil-normalized comparison)", func(t *testing.T) {
		sameReason := schemas.AbstainReasonVerifierUncertain
		port := newRecordedEvaluationPort(t, map[string]replacementScriptedOutcome{
			"case-001": {Action: schemas.ActionPatch, Agree: true, OldReason: &sameReason, NewReason: &sameReason},
			"case-002": {Action: schemas.ActionPatch, Agree: true},
		})
		result, err := openrouter.EvaluateReplacementModel(replacementBaseInput(port))
		require.NoError(t, err)
		assert.True(t, result.Report.Cases[0].Agreed)
		assert.Equal(t, "pass", result.Report.Verdict)
	})

	t.Run("DIFFERENT abstain reasons disagree even when the action matches", func(t *testing.T) {
		oldReason := schemas.AbstainReasonVerifierUncertain
		newReason := schemas.AbstainReasonConflictingEvidence
		port := newRecordedEvaluationPort(t, map[string]replacementScriptedOutcome{
			"case-001": {Action: schemas.ActionPatch, Agree: true, OldReason: &oldReason, NewReason: &newReason},
			"case-002": {Action: schemas.ActionPatch, Agree: true},
		})
		result, err := openrouter.EvaluateReplacementModel(replacementBaseInput(port))
		require.NoError(t, err)
		assert.False(t, result.Report.Cases[0].Agreed)
		assert.Equal(t, "fail", result.Report.Verdict)
	})

	t.Run("rejects malformed evaluations fail-closed BEFORE any case runs", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(input openrouter.EvaluateReplacementModelInput) openrouter.EvaluateReplacementModelInput
		}{
			{"same primary", func(input openrouter.EvaluateReplacementModelInput) openrouter.EvaluateReplacementModelInput {
				input.OldBinding = schemas.ReplacementModelBinding{Primary: "openai/gpt-5.1"}
				return input
			}},
			{"no cases", func(input openrouter.EvaluateReplacementModelInput) openrouter.EvaluateReplacementModelInput {
				input.Cases = nil
				return input
			}},
			{"equal versions", func(input openrouter.EvaluateReplacementModelInput) openrouter.EvaluateReplacementModelInput {
				input.ProductionVersion = 3
				return input
			}},
			{"out-of-range threshold", func(input openrouter.EvaluateReplacementModelInput) openrouter.EvaluateReplacementModelInput {
				input.MinAgreementRate = 1.5
				return input
			}},
			{"zero time box", func(input openrouter.EvaluateReplacementModelInput) openrouter.EvaluateReplacementModelInput {
				input.TimeBoxDays = 0
				return input
			}},
			{"non-canonical clock", func(input openrouter.EvaluateReplacementModelInput) openrouter.EvaluateReplacementModelInput {
				input.Clock = func() string { return "not-an-instant" }
				return input
			}},
			{"duplicate case ids", func(input openrouter.EvaluateReplacementModelInput) openrouter.EvaluateReplacementModelInput {
				input.Cases = []schemas.ReplacementEvalCaseInput{
					{CaseID: "case-001", RiskBucket: schemas.RiskBucketHigh},
					{CaseID: "case-001", RiskBucket: schemas.RiskBucketLow},
				}
				return input
			}},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				port := newRecordedEvaluationPort(t, nil)
				_, err := openrouter.EvaluateReplacementModel(
					testCase.mutate(replacementBaseInput(port)))
				require.Error(t, err)
				var evaluationErr *openrouter.ReplacementEvaluationError
				require.ErrorAs(t, err, &evaluationErr)
				assert.Equal(t, "replacement_evaluation_invalid", evaluationErr.Code)
				assert.Contains(t, err.Error(), "rejected fail-closed")
				// Fail-closed BEFORE any case runs: the port was never called.
				assert.Equal(t, 0, port.calls)
			})
		}
	})

	t.Run("rejects a non-ULID record id from the injected factory (fail-closed at emission)", func(t *testing.T) {
		port := newRecordedEvaluationPort(t, map[string]replacementScriptedOutcome{"case-001": {Action: schemas.ActionPatch, Agree: true}})
		input := replacementBaseInput(port)
		input.Cases = replacementCases("case-001")
		input.NewRecordID = func(string) string { return "not-a-ulid" }
		_, err := openrouter.EvaluateReplacementModel(input)
		require.Error(t, err)
		var evaluationErr *openrouter.ReplacementEvaluationError
		require.ErrorAs(t, err, &evaluationErr)
		assert.Contains(t, err.Error(), "non-ULID")
	})

	t.Run("an unscripted case fails closed instead of fabricating an outcome", func(t *testing.T) {
		port := newRecordedEvaluationPort(t, nil)
		_, err := openrouter.EvaluateReplacementModel(replacementBaseInput(port))
		require.Error(t, err)
		var evaluationErr *openrouter.ReplacementEvaluationError
		require.ErrorAs(t, err, &evaluationErr)
		assert.Contains(t, err.Error(), "unscripted case case-001")
	})
}
