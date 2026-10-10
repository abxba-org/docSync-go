// Ported from packages/policy/test/abstain-queue.test.ts — the abstain review
// queue: two-tier discipline, queue validation, idempotence and the resolution
// lifecycle (V-P2-9 groundwork; P2-T14; ARCH §5.2).
//
// The compiled-workflow seam is built as a literal with the bootstrap policy v1
// data (policy_version 1, the conservative calibrated routing thresholds) —
// the TS suite compiles it through compileBootstrapPolicyV1, whose
// CompiledPolicyWorkflow type is owned by the compiler unit. The policy_hash
// value is not pinned by the TS assertions (they only assert decision_id
// determinism), so a deterministic literal carries the same contract.
package policy_test

import (
	"encoding/json"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"testing"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// abstainPolicyHash is the bootstrap policy's `sha256:<hex>` trace hash — a
// deterministic literal (the TS suite does not pin its value).
const abstainPolicyHash = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func abstainCompiled() *policy.CompiledPolicyWorkflow {
	calibrationFloor := 0.94
	docImpactFloor := 0.94
	return &policy.CompiledPolicyWorkflow{
		Policy: policy.Policy{PolicyVersion: 1},
		Routing: policy.Routing{
			HighConfidence:               0.94,
			Review:                       0.72,
			CalibrationFloor:             &calibrationFloor,
			DocumentationImpactAutoFloor: &docImpactFloor,
		},
		PolicyHash: abstainPolicyHash,
	}
}

var abstainManifestPaths = []string{"docs/payments.md", "docs/api.md"}

var abstainChange = schemas.DecisionChange{
	Commit:            "81af92e",
	DiffRef:           "git:81af92e",
	ChangeAnalysisRef: "analysis/01J9ZK6X8A",
}

func abstainAnswers() map[string]schemas.Answer {
	return map[string]schemas.Answer{
		"public_behavior_changed": {Value: json.RawMessage(`true`), Probability: 0.61},
		"api_contract_changed":    {Value: json.RawMessage(`false`), Probability: 0.3},
		"affected_doc_count":      {Value: json.RawMessage(`1`), Probability: 0.66},
		// M4 rule-consistency: the deciding answer's probability equals the
		// overall confidence (0.66), mirroring DECISION_ABSTAIN_EXAMPLE.
		"documentation_impact": {Value: json.RawMessage(`"unknown"`), Probability: 0.66},
	}
}

func abstainEvidence() []schemas.Evidence {
	return []schemas.Evidence{
		{Kind: schemas.EvidenceKindSymbolDiff, Ref: "src/payments/refund.ts:42", Note: "idempotencyKey became optional"},
	}
}

func abstainedDecision(t *testing.T, reason schemas.AbstainReason) *schemas.Decision {
	t.Helper()
	bodyReason := reason
	decision, err := policy.EmitDecision(policy.EmitDecisionInput{
		Compiled: abstainCompiled(),
		Change:   abstainChange,
		Answers:  abstainAnswers(),
		Body: policy.EmitDecisionBody{
			ChangeKind:          schemas.ChangeKindBehavioral,
			DocumentationImpact: schemas.DocumentationImpactUnknown,
			Risk:                schemas.RiskLevelMedium,
			Confidence:          0.66,
			Action:              schemas.ActionReview,
			Abstain:             true,
			AbstainReason:       &bodyReason,
		},
		Candidates:       []string{"docs/payments.md"},
		ManifestDocPaths: abstainManifestPaths,
		Evidence:         abstainEvidence(),
		RoutedModel:      "provider/routed",
		LatencyMs:        810,
	})
	require.NoError(t, err)
	return decision
}

func nonAbstainedDecision(t *testing.T) *schemas.Decision {
	t.Helper()
	decision, err := policy.EmitDecision(policy.EmitDecisionInput{
		Compiled: abstainCompiled(),
		Change:   abstainChange,
		Answers:  abstainAnswers(),
		Body: policy.EmitDecisionBody{
			ChangeKind:          schemas.ChangeKindBehavioral,
			DocumentationImpact: schemas.DocumentationImpactPossible,
			Risk:                schemas.RiskLevelMedium,
			Confidence:          0.91,
			Action:              schemas.ActionInvestigate,
			Abstain:             false,
			AbstainReason:       nil,
		},
		Candidates:       []string{"docs/payments.md"},
		ManifestDocPaths: abstainManifestPaths,
		Evidence:         abstainEvidence(),
		RoutedModel:      "provider/routed",
		LatencyMs:        740,
	})
	require.NoError(t, err)
	return decision
}

func abstainQueueCodeOf(t *testing.T, action func() error) string {
	t.Helper()
	err := action()
	require.Error(t, err)
	var queueErr *policy.AbstainQueueError
	require.ErrorAs(t, err, &queueErr)
	return queueErr.Code
}

func TestAbstainQueue_VP29_DecisionLevelReasonsQueueResolveAndFireTheHook(t *testing.T) {
	for _, reason := range schemas.DecisionLevelAbstainReasons {
		t.Run(string(reason), func(t *testing.T) {
			var labeled []*policy.LabeledDataPoint
			queue := policy.NewInMemoryAbstainQueue(policy.InMemoryAbstainQueueOptions{
				OnLabeledDataPoint: func(dataPoint *policy.LabeledDataPoint) {
					labeled = append(labeled, dataPoint)
				},
			})
			decision := abstainedDecision(t, reason)
			event, err := queue.RecordAbstain(policy.RecordAbstainInput{
				Decision: decision,
				Reason:   reason,
				Evidence: abstainEvidence(),
			})
			require.NoError(t, err)
			require.Equal(t, reason, event.Reason)
			require.True(t, event.Decision.Abstain)
			require.Equal(t, reason, *event.Decision.AbstainReason)
			require.Equal(t, abstainEvidence(), event.Evidence)
			require.Equal(t, policy.ResolveAbstainEventID(decision.DecisionID, string(reason)), event.EventID)
			require.Regexp(t, `^a_[0-9a-f]{16}$`, event.EventID)
			require.Len(t, queue.List(), 1)
			require.Nil(t, queue.Get(event.EventID).Resolution)

			dataPoint, err := queue.ResolveAbstain(event, policy.ResolveAbstainInput{
				ResolvedAs: "confirmed_affected",
				By:         "reviewer-1",
				Note:       "docs were stale",
			})
			require.NoError(t, err)
			require.Equal(t, event.EventID, dataPoint.EventID)
			require.Equal(t, decision.DecisionID, dataPoint.DecisionID)
			require.Equal(t, abstainCompiled().Policy.PolicyVersion, dataPoint.PolicyVersion)
			require.Equal(t, reason, dataPoint.Reason)
			answer, ok := dataPoint.Answers["documentation_impact"]
			require.True(t, ok)
			require.NotEmpty(t, answer.Value)
			require.Equal(t, 0.66, answer.Probability)
			require.Equal(t, &policy.AbstainResolution{
				ResolvedAs: "confirmed_affected",
				By:         "reviewer-1",
				Note:       "docs were stale",
			}, dataPoint.Resolution)
			require.Len(t, labeled, 1)
			require.Equal(t, event.EventID, labeled[0].EventID)
			require.Equal(t, dataPoint.Resolution, queue.Get(event.EventID).Resolution)
		})
	}
}

func TestAbstainQueue_DecisionLevelReasonsAreExactlyTheCanonicalFive(t *testing.T) {
	require.Equal(t, []schemas.AbstainReason{
		schemas.AbstainReasonInsufficientConfidence,
		schemas.AbstainReasonConflictingEvidence,
		schemas.AbstainReasonPolicyGap,
		schemas.AbstainReasonCalibrationFloor,
		schemas.AbstainReasonVerifierUncertain,
	}, schemas.DecisionLevelAbstainReasons)
}

func TestAbstainQueue_OperationalReasonBypassesTheQueue(t *testing.T) {
	decision := abstainedDecision(t, schemas.AbstainReasonSchemaViolation)
	queue := policy.NewInMemoryAbstainQueue()
	record := func() error {
		_, err := queue.RecordAbstain(policy.RecordAbstainInput{
			Decision: decision,
			Reason:   schemas.AbstainReasonSchemaViolation,
			Evidence: abstainEvidence(),
		})
		return err
	}
	err := record()
	var queueErr *policy.AbstainQueueError
	require.ErrorAs(t, err, &queueErr)
	require.Equal(t, "operational_reason", abstainQueueCodeOf(t, record))
	require.Empty(t, queue.List())
	require.Contains(t, queueErr.Error(), "bypasses the review queue")
	require.Contains(t, queueErr.Error(), "never policy-learning data")
}

func TestAbstainQueue_EveryOperationalReasonBypassesTheQueue(t *testing.T) {
	for _, reason := range []schemas.AbstainReason{
		schemas.AbstainReasonModelUnavailable,
		schemas.AbstainReasonCapExceeded,
		schemas.AbstainReasonPolicyMissing,
		schemas.AbstainReasonOther,
	} {
		t.Run(string(reason), func(t *testing.T) {
			queue := policy.NewInMemoryAbstainQueue()
			decision := abstainedDecision(t, reason)
			record := func() error {
				_, err := queue.RecordAbstain(policy.RecordAbstainInput{
					Decision: decision,
					Reason:   reason,
					Evidence: abstainEvidence(),
				})
				return err
			}
			var queueErr *policy.AbstainQueueError
			require.ErrorAs(t, record(), &queueErr)
			require.Equal(t, "operational_reason", abstainQueueCodeOf(t, record))
		})
	}
}

func TestAbstainQueue_NonAbstainedDecisionRejected(t *testing.T) {
	queue := policy.NewInMemoryAbstainQueue()
	record := func() error {
		_, err := queue.RecordAbstain(policy.RecordAbstainInput{
			Decision: nonAbstainedDecision(t),
			Reason:   schemas.AbstainReasonInsufficientConfidence,
			Evidence: abstainEvidence(),
		})
		return err
	}
	var queueErr *policy.AbstainQueueError
	require.ErrorAs(t, record(), &queueErr)
	require.Equal(t, "decision_not_abstained", abstainQueueCodeOf(t, record))
}

func TestAbstainQueue_InputReasonMustMatchDecisionAbstainReason(t *testing.T) {
	queue := policy.NewInMemoryAbstainQueue()
	decision := abstainedDecision(t, schemas.AbstainReasonInsufficientConfidence)
	action := func() error {
		_, err := queue.RecordAbstain(policy.RecordAbstainInput{
			Decision: decision,
			Reason:   schemas.AbstainReasonPolicyGap,
			Evidence: abstainEvidence(),
		})
		return err
	}
	require.Equal(t, "reason_mismatch", abstainQueueCodeOf(t, action))
}

func TestAbstainQueue_EmptyEvidenceRejected(t *testing.T) {
	queue := policy.NewInMemoryAbstainQueue()
	decision := abstainedDecision(t, schemas.AbstainReasonInsufficientConfidence)
	action := func() error {
		_, err := queue.RecordAbstain(policy.RecordAbstainInput{
			Decision: decision,
			Reason:   schemas.AbstainReasonInsufficientConfidence,
			Evidence: nil,
		})
		return err
	}
	require.Equal(t, "empty_evidence", abstainQueueCodeOf(t, action))
}

func TestAbstainQueue_UnresolvableEvidenceRejectedWithTheEmissionTaxonomyError(t *testing.T) {
	queue := policy.NewInMemoryAbstainQueue()
	decision := abstainedDecision(t, schemas.AbstainReasonInsufficientConfidence)
	_, err := queue.RecordAbstain(policy.RecordAbstainInput{
		Decision: decision,
		Reason:   schemas.AbstainReasonInsufficientConfidence,
		Evidence: []schemas.Evidence{{Kind: schemas.EvidenceKindSymbolDiff, Ref: "free text no anchor", Note: ""}},
	})
	var evidenceErr *policy.EvidenceNotResolvableError
	require.ErrorAs(t, err, &evidenceErr)
}

func TestAbstainQueue_DuplicateRecordsAreIdempotent(t *testing.T) {
	queue := policy.NewInMemoryAbstainQueue()
	decision := abstainedDecision(t, schemas.AbstainReasonConflictingEvidence)
	first, err := queue.RecordAbstain(policy.RecordAbstainInput{
		Decision: decision,
		Reason:   schemas.AbstainReasonConflictingEvidence,
		Evidence: abstainEvidence(),
	})
	require.NoError(t, err)
	second, err := queue.RecordAbstain(policy.RecordAbstainInput{
		Decision: decision,
		Reason:   schemas.AbstainReasonConflictingEvidence,
		Evidence: abstainEvidence(),
	})
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Len(t, queue.List(), 1)
}

func TestAbstainQueue_ResolvingAnUnknownEventFails(t *testing.T) {
	queue := policy.NewInMemoryAbstainQueue()
	decision := abstainedDecision(t, schemas.AbstainReasonPolicyGap)
	ghost := policy.AbstainEvent{
		EventID:  "a_0000000000000000",
		Decision: decision,
		Reason:   schemas.AbstainReasonPolicyGap,
		Evidence: abstainEvidence(),
	}
	action := func() error {
		_, err := queue.ResolveAbstain(ghost, policy.ResolveAbstainInput{
			ResolvedAs: "confirmed_affected",
			By:         "r",
			Note:       "",
		})
		return err
	}
	require.Equal(t, "event_not_found", abstainQueueCodeOf(t, action))
}

func TestAbstainQueue_DoubleResolutionRejected(t *testing.T) {
	queue := policy.NewInMemoryAbstainQueue()
	event, err := queue.RecordAbstain(policy.RecordAbstainInput{
		Decision: abstainedDecision(t, schemas.AbstainReasonCalibrationFloor),
		Reason:   schemas.AbstainReasonCalibrationFloor,
		Evidence: abstainEvidence(),
	})
	require.NoError(t, err)
	_, err = queue.ResolveAbstain(event, policy.ResolveAbstainInput{
		ResolvedAs: "not_affected",
		By:         "reviewer-2",
		Note:       "",
	})
	require.NoError(t, err)
	action := func() error {
		_, err := queue.ResolveAbstain(event, policy.ResolveAbstainInput{
			ResolvedAs: "not_affected",
			By:         "reviewer-2",
			Note:       "",
		})
		return err
	}
	require.Equal(t, "already_resolved", abstainQueueCodeOf(t, action))
}

func TestAbstainQueue_InvalidResolutionPayloadsRejected(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		resolvedAs string
		by         string
	}{
		{name: "unknown resolvedAs", resolvedAs: "maybe", by: "reviewer-1"},
		{name: "empty reviewer id", resolvedAs: "confirmed_affected", by: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			queue := policy.NewInMemoryAbstainQueue()
			event, err := queue.RecordAbstain(policy.RecordAbstainInput{
				Decision: abstainedDecision(t, schemas.AbstainReasonVerifierUncertain),
				Reason:   schemas.AbstainReasonVerifierUncertain,
				Evidence: abstainEvidence(),
			})
			require.NoError(t, err)
			action := func() error {
				_, err := queue.ResolveAbstain(event, policy.ResolveAbstainInput{
					ResolvedAs: testCase.resolvedAs,
					By:         testCase.by,
					Note:       "",
				})
				return err
			}
			require.Equal(t, "invalid_resolution", abstainQueueCodeOf(t, action))
		})
	}
	// The TS suite's third case (`note: 42`) is a runtime typeof check with no
	// Go counterpart: Note is a string field, so a non-string note is a
	// compile-time error. Documented in the unit report (DEVIATIONS).
}

func TestAbstainQueue_ResolutionVocabularyIsTheFourLabelSet(t *testing.T) {
	require.Equal(t, []string{
		"confirmed_affected",
		"not_affected",
		"corrected",
		"unlabeled",
	}, policy.AbstainResolutionValues)
}

func TestAbstainQueue_ListReturnsRecordOrderAndGetMisses(t *testing.T) {
	queue := policy.NewInMemoryAbstainQueue()
	first, err := queue.RecordAbstain(policy.RecordAbstainInput{
		Decision: abstainedDecision(t, schemas.AbstainReasonInsufficientConfidence),
		Reason:   schemas.AbstainReasonInsufficientConfidence,
		Evidence: abstainEvidence(),
	})
	require.NoError(t, err)
	second, err := queue.RecordAbstain(policy.RecordAbstainInput{
		Decision: abstainedDecision(t, schemas.AbstainReasonPolicyGap),
		Reason:   schemas.AbstainReasonPolicyGap,
		Evidence: abstainEvidence(),
	})
	require.NoError(t, err)
	_, err = queue.ResolveAbstain(first, policy.ResolveAbstainInput{
		ResolvedAs: "corrected",
		By:         "reviewer-3",
		Note:       "human edited the doc",
	})
	require.NoError(t, err)

	entries := queue.List()
	require.Len(t, entries, 2)
	require.Equal(t, first.EventID, entries[0].Event.EventID)
	require.Equal(t, second.EventID, entries[1].Event.EventID)
	require.NotNil(t, entries[0].Resolution)
	require.Nil(t, entries[1].Resolution)
	require.Nil(t, queue.Get("a_missing"))
}

func TestAbstainQueue_UnknownDecisionLevelReasonRejected(t *testing.T) {
	// A decision whose abstain_reason is outside the closed vocabulary fails
	// closed — defensive: the wire schema rejects such a decision first, and
	// the queue keeps its own guard.
	queue := policy.NewInMemoryAbstainQueue()
	decision := abstainedDecision(t, schemas.AbstainReasonPolicyGap)
	unknown := schemas.AbstainReason("not_a_real_reason")
	decision.AbstainReason = &unknown
	action := func() error {
		_, err := queue.RecordAbstain(policy.RecordAbstainInput{
			Decision: decision,
			Reason:   unknown,
			Evidence: abstainEvidence(),
		})
		return err
	}
	err := action()
	var queueErr *policy.AbstainQueueError
	require.ErrorAs(t, err, &queueErr)
	require.Equal(t, "decision_not_abstained", abstainQueueCodeOf(t, action))
	require.Contains(t, queueErr.Error(), "is not a known abstain reason of docsync.decision/v1")
}

func TestAbstainQueueError_IsACodedError(t *testing.T) {
	// The typed queue error maps onto docutil's coded-error class so
	// docutil.CodeOf reports EDOCSYNC_VALIDATION (closest existing code — the
	// TS class has no docutil counterpart).
	queue := policy.NewInMemoryAbstainQueue()
	_, err := queue.RecordAbstain(policy.RecordAbstainInput{
		Decision: nonAbstainedDecision(t),
		Reason:   schemas.AbstainReasonInsufficientConfidence,
		Evidence: abstainEvidence(),
	})
	var queueErr *policy.AbstainQueueError
	require.ErrorAs(t, err, &queueErr)
	require.NotEmpty(t, queueErr.Error())
	require.Equal(t, docutil.CodeValidation, docutil.CodeOf(queueErr))
}
