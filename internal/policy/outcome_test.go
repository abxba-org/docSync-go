// Ported from packages/policy/test/engine-outcome.test.ts — abstain-first
// policy outcome mapping (P2-T19; ARCH §5.2/§5.3; V-P2-9 groundwork): the
// calibration-floor band, seam abstains, the deterministic ignore
// short-circuit, matrix cells + mode permission (V-P2-8 groundwork) and the
// D2 ownership floor.
//
// The TS suite compiles POLICY_EXAMPLE through compilePolicy; the Go test uses
// the landed policy.PolicyExample (the schema unit's mirror of POLICY_EXAMPLE,
// v17) as the workflow's policy — the policy_hash value is not pinned by the
// TS assertions, so a deterministic literal carries the same contract.
package policy_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

const outcomePolicyHash = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// outcomeCompiled mirrors POLICY_EXAMPLE: routing {high_confidence 0.94,
// review 0.72, calibration_floor 0.94, documentation_impact_auto_floor 0.94},
// v17.
func outcomeCompiled() *policy.CompiledPolicyWorkflow {
	return &policy.CompiledPolicyWorkflow{
		Policy:     policy.PolicyExample,
		Routing:    policy.PolicyExample.Routing,
		PolicyHash: outcomePolicyHash,
	}
}

// outcomeCompiledDocFloor09 is a routing where the per-answer
// documentation_impact floor (0.9) differs from the global calibration floor
// (0.94).
func outcomeCompiledDocFloor09() *policy.CompiledPolicyWorkflow {
	routing := policy.PolicyExample.Routing
	docImpactFloor := 0.9
	routing.DocumentationImpactAutoFloor = &docImpactFloor
	return &policy.CompiledPolicyWorkflow{
		Policy:     policy.PolicyExample,
		Routing:    routing,
		PolicyHash: outcomePolicyHash,
	}
}

// outcomeBaseDecision — an out-of-band decision on the low×high auto cell:
// doc-impact probability 0.97 ≥ 0.94, confidence 0.99.
func outcomeBaseDecision() policy.PendingDecision {
	return policy.PendingDecision{
		ChangeKind:          schemas.ChangeKindBehavioral,
		DocumentationImpact: schemas.DocumentationImpactPossible,
		Risk:                schemas.RiskLevelLow,
		Confidence:          0.99,
		Answers: map[string]policy.PendingDecisionAnswer{
			"documentation_impact": {Value: []byte(`"possible"`), Probability: 0.97},
		},
	}
}

func outcomeDecisionWith(probability float64) policy.PendingDecision {
	decision := outcomeBaseDecision()
	decision.Answers = map[string]policy.PendingDecisionAnswer{
		"documentation_impact": {Value: []byte(`"possible"`), Probability: probability},
	}
	return decision
}

func outcomePrefixMatcher(path, glob string) bool {
	base := glob
	for {
		if len(base) >= 2 && base[len(base)-2:] == "**" {
			base = base[:len(base)-2]
			continue
		}
		if len(base) >= 1 && base[len(base)-1] == '/' {
			base = base[:len(base)-1]
			continue
		}
		break
	}
	return len(base) > 0 && (path == base || (len(path) > len(base)+1 && path[:len(base)] == base && path[len(base)] == '/'))
}

func TestMapOutcome_VP29_CalibrationFloorAbstainsWithNamedReasonAndThresholds(t *testing.T) {
	outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: outcomeDecisionWith(0.83),
		Compiled: outcomeCompiled(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	require.NoError(t, err)
	require.Equal(t, policy.PolicyOutcomeReview, outcome.Outcome)
	require.True(t, outcome.ReviewRequired)
	require.NotNil(t, outcome.Abstain)
	require.Equal(t, schemas.AbstainReasonCalibrationFloor, outcome.Abstain.Reason)
	// §5.2 example wording: probability + BOTH thresholds + policy version.
	require.Contains(t, outcome.Abstain.Detail, "0.83")
	require.Contains(t, outcome.Abstain.Detail, "(0.94)")
	require.Contains(t, outcome.Abstain.Detail, "(0.72)")
	require.Contains(t, outcome.Abstain.Detail, "policy v17")
	// Abstain is NEVER mapped to auto/ignore (§5.2 rule 1).
	require.NotEqual(t, policy.PolicyOutcomeAuto, outcome.Outcome)
	require.NotEqual(t, policy.PolicyOutcomeIgnore, outcome.Outcome)
	// No matrix evaluation happened on the abstain path.
	require.Nil(t, outcome.Cell)
}

func TestMapOutcome_CalibrationBandBoundariesAreStrict(t *testing.T) {
	for _, probability := range []float64{0.72, 0.94} {
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: outcomeDecisionWith(probability),
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.Nil(t, outcome.Abstain, "probability %v must not abstain", probability)
		require.Equal(t, policy.PolicyOutcomeAuto, outcome.Outcome)
	}
}

func TestMapOutcome_JustInsideTheBandAbstains(t *testing.T) {
	for _, probability := range []float64{0.73, 0.9399} {
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: outcomeDecisionWith(probability),
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.NotNil(t, outcome.Abstain)
		require.Equal(t, schemas.AbstainReasonCalibrationFloor, outcome.Abstain.Reason)
	}
}

func TestMapOutcome_DocumentationImpactAutoFloorTakesPrecedenceForThatAnswer(t *testing.T) {
	// doc-impact floor 0.9: 0.85 is strictly inside (0.72, 0.9) → abstain; the
	// detail names the PER-ANSWER floor, not the global calibration floor.
	abstained, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: outcomeDecisionWith(0.85),
		Compiled: outcomeCompiledDocFloor09(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	require.NoError(t, err)
	require.NotNil(t, abstained.Abstain)
	require.Equal(t, schemas.AbstainReasonCalibrationFloor, abstained.Abstain.Reason)
	require.Contains(t, abstained.Abstain.Detail, "(0.9)")

	// 0.91 ≥ the per-answer floor 0.9 → no abstain (would abstain under the
	// global 0.94 band only above 0.94).
	cleared, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: outcomeDecisionWith(0.91),
		Compiled: outcomeCompiledDocFloor09(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	require.NoError(t, err)
	require.Nil(t, cleared.Abstain)
	require.Equal(t, policy.PolicyOutcomeAuto, cleared.Outcome)
}

func TestMapOutcome_PerAnswerFloorDoesNotApplyToOverallConfidence(t *testing.T) {
	// No answers at all → deciding probability falls back to overall confidence
	// 0.91; the per-answer floor (0.9) must not widen that band — the GLOBAL
	// band (0.72, 0.94) applies and 0.91 is inside it → abstain.
	decision := outcomeBaseDecision()
	decision.Confidence = 0.91
	decision.Answers = nil
	outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: decision,
		Compiled: outcomeCompiledDocFloor09(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	require.NoError(t, err)
	require.NotNil(t, outcome.Abstain)
	require.Equal(t, schemas.AbstainReasonCalibrationFloor, outcome.Abstain.Reason)
	require.Contains(t, outcome.Abstain.Detail, "(0.94)")
}

func TestMapOutcome_ExplicitDecidingProbabilityIsTreatedAsTheDocImpactAnswer(t *testing.T) {
	decision := outcomeBaseDecision()
	decision.Answers = nil
	deciding := 0.83
	decision.DecidingProbability = &deciding
	decision.Confidence = 0.99
	outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: decision,
		Compiled: outcomeCompiledDocFloor09(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	require.NoError(t, err)
	require.NotNil(t, outcome.Abstain)
	require.Equal(t, schemas.AbstainReasonCalibrationFloor, outcome.Abstain.Reason)
	require.Contains(t, outcome.Abstain.Detail, "(0.9)")
}

func TestMapOutcome_AbstainFirstBeatsAutoCellAndIgnoreShortCircuit(t *testing.T) {
	t.Run("an in-band probability on an otherwise-auto cell → abstain, not auto", func(t *testing.T) {
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: outcomeDecisionWith(0.83),
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.Equal(t, policy.PolicyOutcomeReview, outcome.Outcome)
		require.NotNil(t, outcome.Abstain)
		require.Equal(t, schemas.AbstainReasonCalibrationFloor, outcome.Abstain.Reason)
		require.NotEqual(t, policy.PolicyOutcomeAuto, outcome.Outcome)
	})

	t.Run("policy_gap abstains with the change kind named", func(t *testing.T) {
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision:  outcomeBaseDecision(),
			Compiled:  outcomeCompiled(),
			DocMode:   policy.OwnershipFloorAuto,
			PolicyGap: true,
		})
		require.NoError(t, err)
		require.Equal(t, policy.PolicyOutcomeReview, outcome.Outcome)
		require.True(t, outcome.ReviewRequired)
		require.NotNil(t, outcome.Abstain)
		require.Equal(t, schemas.AbstainReasonPolicyGap, outcome.Abstain.Reason)
		require.Contains(t, outcome.Abstain.Detail, "behavioral")
		require.Contains(t, outcome.Abstain.Detail, "policy v17")
	})

	t.Run("conflicting_evidence and verifier_uncertain seams abstain", func(t *testing.T) {
		conflicting, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision:            outcomeBaseDecision(),
			Compiled:            outcomeCompiled(),
			DocMode:             policy.OwnershipFloorAuto,
			ConflictingEvidence: true,
		})
		require.NoError(t, err)
		require.NotNil(t, conflicting.Abstain)
		require.Equal(t, schemas.AbstainReasonConflictingEvidence, conflicting.Abstain.Reason)

		uncertain, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision:          outcomeBaseDecision(),
			Compiled:          outcomeCompiled(),
			DocMode:           policy.OwnershipFloorAuto,
			VerifierUncertain: true,
		})
		require.NoError(t, err)
		require.NotNil(t, uncertain.Abstain)
		require.Equal(t, schemas.AbstainReasonVerifierUncertain, uncertain.Abstain.Reason)
	})

	t.Run("an upstream Stage-1 abstain passes through verbatim and wins over the seams", func(t *testing.T) {
		decision := outcomeBaseDecision()
		decision.Abstain = true
		reason := schemas.AbstainReasonInsufficientConfidence
		decision.AbstainReason = &reason
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision:  decision,
			Compiled:  outcomeCompiled(),
			DocMode:   policy.OwnershipFloorAuto,
			PolicyGap: true,
		})
		require.NoError(t, err)
		require.NotNil(t, outcome.Abstain)
		require.Equal(t, schemas.AbstainReasonInsufficientConfidence, outcome.Abstain.Reason)
		require.Equal(t, policy.PolicyOutcomeReview, outcome.Outcome)
	})

	t.Run("abstain beats the deterministic ignore short-circuit", func(t *testing.T) {
		decision := outcomeDecisionWith(0.83)
		decision.ChangeKind = schemas.ChangeKindFormattingOnly
		decision.DocumentationImpact = schemas.DocumentationImpactNone
		decision.Confidence = 0.99
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: decision,
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.NotNil(t, outcome.Abstain)
		require.Equal(t, schemas.AbstainReasonCalibrationFloor, outcome.Abstain.Reason)
		require.Equal(t, policy.PolicyOutcomeReview, outcome.Outcome)
	})
}

func TestMapOutcome_DeterministicIgnoreShortCircuit(t *testing.T) {
	t.Run("formatting_only + documentation_impact none → ignore", func(t *testing.T) {
		decision := outcomeBaseDecision()
		decision.ChangeKind = schemas.ChangeKindFormattingOnly
		decision.DocumentationImpact = schemas.DocumentationImpactNone
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: decision,
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.Equal(t, policy.PolicyOutcomeIgnore, outcome.Outcome)
		require.False(t, outcome.ReviewRequired)
		require.Nil(t, outcome.Abstain)
		require.Nil(t, outcome.Cell)
	})

	t.Run("test_only + documentation_impact none → ignore even on the low-confidence bucket", func(t *testing.T) {
		decision := outcomeBaseDecision()
		decision.ChangeKind = schemas.ChangeKindTestOnly
		decision.DocumentationImpact = schemas.DocumentationImpactNone
		decision.Confidence = 0.3
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: decision,
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.Equal(t, policy.PolicyOutcomeIgnore, outcome.Outcome)
	})

	t.Run("an ignorable change kind WITH a documentation impact takes the matrix path", func(t *testing.T) {
		decision := outcomeBaseDecision()
		decision.ChangeKind = schemas.ChangeKindFormattingOnly
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: decision,
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.Equal(t, policy.PolicyOutcomeAuto, outcome.Outcome)
		require.NotNil(t, outcome.Cell)
		require.Equal(t, policy.ResolvedCellAuto, *outcome.Cell)
	})
}

func TestMapOutcome_MatrixPathModePermissionAndOwnershipFloor(t *testing.T) {
	t.Run("the auto cell fires only in docMode auto — suggest/human degrade it to suggest", func(t *testing.T) {
		for _, testCase := range []struct {
			mode     policy.OwnershipFloorMode
			expected policy.PolicyOutcomeLabel
		}{
			{policy.OwnershipFloorAuto, policy.PolicyOutcomeAuto},
			{policy.OwnershipFloorSuggest, policy.PolicyOutcomeSuggest},
			{policy.OwnershipFloorHuman, policy.PolicyOutcomeSuggest},
		} {
			outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
				Decision: outcomeBaseDecision(),
				Compiled: outcomeCompiled(),
				DocMode:  testCase.mode,
			})
			require.NoError(t, err, "docMode %s", testCase.mode)
			require.NotNil(t, outcome.Cell, "docMode %s", testCase.mode)
			require.Equal(t, policy.ResolvedCellAuto, *outcome.Cell, "docMode %s", testCase.mode)
			require.Equal(t, testCase.expected, outcome.Outcome, "docMode %s", testCase.mode)
			require.False(t, outcome.ReviewRequired, "docMode %s", testCase.mode)
		}
	})

	t.Run("block cells resolve to blocked regardless of mode", func(t *testing.T) {
		decision := outcomeBaseDecision()
		decision.Risk = schemas.RiskLevelMedium
		decision.Confidence = 0.5
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: decision,
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.NotNil(t, outcome.Cell)
		require.Equal(t, policy.ResolvedCellBlocked, *outcome.Cell)
		require.Equal(t, policy.PolicyOutcomeBlocked, outcome.Outcome)
		require.False(t, outcome.ReviewRequired)
	})

	t.Run("the diagonal resolves to review with reviewRequired true", func(t *testing.T) {
		decision := outcomeBaseDecision()
		decision.Risk = schemas.RiskLevelMedium
		decision.Confidence = 0.8
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: decision,
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.NotNil(t, outcome.Cell)
		require.Equal(t, policy.ResolvedCellReview, *outcome.Cell)
		require.Equal(t, policy.PolicyOutcomeReview, outcome.Outcome)
		require.True(t, outcome.ReviewRequired)
	})

	t.Run("locked/generated/external resolve to blocked at any confidence, even on the auto cell", func(t *testing.T) {
		for _, mode := range []policy.OwnershipFloorMode{
			policy.OwnershipFloorLocked, policy.OwnershipFloorGenerated, policy.OwnershipFloorExternal,
		} {
			decision := outcomeBaseDecision()
			decision.Confidence = 1
			outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
				Decision: decision,
				Compiled: outcomeCompiled(),
				DocMode:  mode,
			})
			require.NoError(t, err, "docMode %s", mode)
			require.NotNil(t, outcome.Cell, "docMode %s", mode)
			require.Equal(t, policy.ResolvedCellBlocked, *outcome.Cell, "docMode %s", mode)
			require.Equal(t, policy.PolicyOutcomeBlocked, outcome.Outcome, "docMode %s", mode)
		}
	})

	t.Run("reviewRequired is true iff the outcome is review", func(t *testing.T) {
		blocked, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: func() policy.PendingDecision {
				decision := outcomeBaseDecision()
				decision.Risk = schemas.RiskLevelMedium
				decision.Confidence = 0.5
				return decision
			}(),
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		review, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: func() policy.PendingDecision {
				decision := outcomeBaseDecision()
				decision.Risk = schemas.RiskLevelMedium
				decision.Confidence = 0.8
				return decision
			}(),
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		auto, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: outcomeBaseDecision(),
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.False(t, blocked.ReviewRequired)
		require.True(t, review.ReviewRequired)
		require.False(t, auto.ReviewRequired)
	})
}

func TestMapOutcome_ConservativeMaxRiskFlowsThrough(t *testing.T) {
	t.Run("manifest high + jev low → high-risk row → never auto", func(t *testing.T) {
		high := schemas.RiskLevelHigh
		decision := outcomeBaseDecision()
		decision.Risk = schemas.RiskLevelLow
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision:     decision,
			Compiled:     outcomeCompiled(),
			DocMode:      policy.OwnershipFloorAuto,
			ManifestRisk: &high,
		})
		require.NoError(t, err)
		require.Equal(t, schemas.RiskLevelHigh, outcome.RiskBucket)
		require.NotNil(t, outcome.Cell)
		require.Equal(t, policy.ResolvedCellReview, *outcome.Cell)
		require.Equal(t, policy.PolicyOutcomeReview, outcome.Outcome)
		require.NotEqual(t, policy.PolicyOutcomeAuto, outcome.Outcome)
	})

	t.Run("override globs applied via the injected matcher flip the row conservatively", func(t *testing.T) {
		matching, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision:           outcomeBaseDecision(),
			Compiled:           outcomeCompiled(),
			DocMode:            policy.OwnershipFloorAuto,
			RiskClassOverrides: map[string]schemas.RiskLevel{"docs/security/**": schemas.RiskLevelHigh},
			DocPath:            outcomeStrPtr("docs/security/auth.md"),
			MatchGlobs:         outcomePrefixMatcher,
		})
		require.NoError(t, err)
		require.Equal(t, schemas.RiskLevelHigh, matching.RiskBucket)
		require.Equal(t, policy.PolicyOutcomeReview, matching.Outcome)

		nonMatching, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision:           outcomeBaseDecision(),
			Compiled:           outcomeCompiled(),
			DocMode:            policy.OwnershipFloorAuto,
			RiskClassOverrides: map[string]schemas.RiskLevel{"docs/security/**": schemas.RiskLevelHigh},
			DocPath:            outcomeStrPtr("docs/other.md"),
			MatchGlobs:         outcomePrefixMatcher,
		})
		require.NoError(t, err)
		require.Equal(t, schemas.RiskLevelLow, nonMatching.RiskBucket)
		require.Equal(t, policy.PolicyOutcomeAuto, nonMatching.Outcome)
	})

	t.Run("provenance: buckets always populated, cell only where the matrix ran", func(t *testing.T) {
		decision := outcomeDecisionWith(0.83)
		decision.Risk = schemas.RiskLevelMedium
		decision.Confidence = 0.8
		outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
			Decision: decision,
			Compiled: outcomeCompiled(),
			DocMode:  policy.OwnershipFloorAuto,
		})
		require.NoError(t, err)
		require.Equal(t, schemas.RiskLevelMedium, outcome.RiskBucket)
		require.Equal(t, policy.ConfidenceBucketMedium, outcome.ConfidenceBucket)
		require.Nil(t, outcome.Cell)
	})
}

// TestMapOutcome_OutOfRangeConfidenceFailsClosed pins the propagated fail-closed
// range guard: an out-of-range overall confidence never silently buckets (the
// matrix suite pins the guard itself; this pins its propagation through
// mapOutcome).
func TestMapOutcome_OutOfRangeConfidenceFailsClosed(t *testing.T) {
	decision := outcomeBaseDecision()
	decision.Confidence = 1.01
	_, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: decision,
		Compiled: outcomeCompiled(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	var rangeErr *policy.EngineRangeError
	require.ErrorAs(t, err, &rangeErr)
}

func outcomeStrPtr(v string) *string { return &v }

func TestMapOutcome_RiskBucketErrorsPropagate(t *testing.T) {
	// Overrides without the injected matcher is a programmer error in the risk
	// layer; mapOutcome propagates it fail-closed (never silently skipped).
	_, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision:           outcomeBaseDecision(),
		Compiled:           outcomeCompiled(),
		DocMode:            policy.OwnershipFloorAuto,
		RiskClassOverrides: map[string]schemas.RiskLevel{"docs/**": schemas.RiskLevelHigh},
	})
	var rangeErr *policy.EngineRangeError
	require.ErrorAs(t, err, &rangeErr)
}

func TestMapOutcome_Stage1AbstainWithoutReasonDefaultsToInsufficientConfidence(t *testing.T) {
	decision := outcomeBaseDecision()
	decision.Abstain = true
	decision.AbstainReason = nil
	outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: decision,
		Compiled: outcomeCompiled(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	require.NoError(t, err)
	require.NotNil(t, outcome.Abstain)
	require.Equal(t, schemas.AbstainReasonInsufficientConfidence, outcome.Abstain.Reason)
	require.Contains(t, outcome.Abstain.Detail, "insufficient_confidence")
	require.Equal(t, policy.PolicyOutcomeReview, outcome.Outcome)
}

func TestMapOutcome_MediumConfidenceOnLowRiskResolvesSuggest(t *testing.T) {
	// low × medium is a `suggest` cell (§5.3) — mapped to outcome `suggest`
	// with no review workflow (the TS suite omits this reachable §5.2 outcome).
	decision := outcomeDecisionWith(0.97)
	decision.Confidence = 0.8
	outcome, err := policy.MapOutcome(policy.PolicyOutcomeInput{
		Decision: decision,
		Compiled: outcomeCompiled(),
		DocMode:  policy.OwnershipFloorAuto,
	})
	require.NoError(t, err)
	require.NotNil(t, outcome.Cell)
	require.Equal(t, policy.ResolvedCellSuggest, *outcome.Cell)
	require.Equal(t, policy.PolicyOutcomeSuggest, outcome.Outcome)
	require.False(t, outcome.ReviewRequired)
	require.Nil(t, outcome.Abstain)
}
