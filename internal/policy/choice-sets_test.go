// Ported from packages/policy/test/policy-schema.test.ts — the "closed choice
// sets are views over @docsync/schemas (OD-2 drift guard)" describe block
// (choice-sets.ts assertions).
package policy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// TestPolicyChoiceSetsMirrorCanonicalValueArrays ports "mirrors the canonical
// value arrays exactly".
func TestPolicyChoiceSetsMirrorCanonicalValueArrays(t *testing.T) {
	assert.Equal(t, evalStringsOf(schemas.ChangeKindValues), policy.PolicyChoiceSets[policy.PolicyChoiceSetChangeKind])
	assert.Equal(t, evalStringsOf(schemas.DocumentationImpactValues), policy.PolicyChoiceSets[policy.PolicyChoiceSetDocumentationImpact])
	assert.Equal(t, evalStringsOf(schemas.RiskLevelValues), policy.PolicyChoiceSets[policy.PolicyChoiceSetRisk])
	assert.Equal(t, evalStringsOf(schemas.ActionValues), policy.PolicyChoiceSets[policy.PolicyChoiceSetAction])
}

// TestPolicyChoiceSetIDsAreTheDecisionFields pins PolicyChoiceSetIDs (the
// TS `keyof typeof POLICY_CHOICE_SETS` order).
func TestPolicyChoiceSetIDsAreTheDecisionFields(t *testing.T) {
	assert.Equal(t, []policy.PolicyChoiceSetID{
		policy.PolicyChoiceSetChangeKind,
		policy.PolicyChoiceSetDocumentationImpact,
		policy.PolicyChoiceSetRisk,
		policy.PolicyChoiceSetAction,
	}, policy.PolicyChoiceSetIDs)
}

// TestClosedPolicyChoicesExposesTheClosedUnion ports "exposes the closed union
// used by the question refinement".
func TestClosedPolicyChoicesExposesTheClosedUnion(t *testing.T) {
	// The union carries no duplicates.
	seen := make(map[string]struct{}, len(policy.ClosedPolicyChoices))
	for _, choice := range policy.ClosedPolicyChoices {
		seen[choice] = struct{}{}
	}
	assert.Len(t, seen, len(policy.ClosedPolicyChoices))

	for _, choice := range []string{"behavioral", "possible", "high", "patch"} {
		assert.Contains(t, policy.ClosedPolicyChoices, choice)
		assert.True(t, policy.IsClosedPolicyChoice(choice), choice)
	}

	for _, choice := range []string{"extreme", "maybe", "auto", "", "High"} {
		assert.False(t, policy.IsClosedPolicyChoice(choice), choice)
	}
}

// TestClosedPolicyChoicesConcatenatesEverySet pins the union order (every set
// in canonical order, back to back).
func TestClosedPolicyChoicesConcatenatesEverySet(t *testing.T) {
	want := make([]string, 0,
		len(schemas.ChangeKindValues)+
			len(schemas.DocumentationImpactValues)+
			len(schemas.RiskLevelValues)+
			len(schemas.ActionValues))
	for _, id := range policy.PolicyChoiceSetIDs {
		want = append(want, policy.PolicyChoiceSets[id]...)
	}
	assert.Equal(t, want, policy.ClosedPolicyChoices)
}

func evalStringsOf[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}
