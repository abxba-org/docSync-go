// Ported from packages/policy/src/choice-sets.ts.

package policy

import "github.com/DUDE616/docSync-go/internal/schemas"

// PolicyChoiceSetID names one closed choice set of the Jev policy's decision
// schema (PolicyChoiceSetId = keyof typeof POLICY_CHOICE_SETS).
type PolicyChoiceSetID string

// The closed set ids, one per `docsync.decision/v1` field.
const (
	PolicyChoiceSetChangeKind          PolicyChoiceSetID = "change_kind"
	PolicyChoiceSetDocumentationImpact PolicyChoiceSetID = "documentation_impact"
	PolicyChoiceSetRisk                PolicyChoiceSetID = "risk"
	PolicyChoiceSetAction              PolicyChoiceSetID = "action"
)

// PolicyChoiceSetIDs lists the set ids in canonical order.
var PolicyChoiceSetIDs = []PolicyChoiceSetID{
	PolicyChoiceSetChangeKind,
	PolicyChoiceSetDocumentationImpact,
	PolicyChoiceSetRisk,
	PolicyChoiceSetAction,
}

// PolicyChoiceSets holds the Jev policy's decision-schema choice sets, one per
// `docsync.decision/v1` field (POLICY_CHOICE_SETS).
//
// The sets themselves are NOT defined here — they are re-exported views over
// the canonical value arrays in internal/schemas (single schema source of
// truth, OD-2), so schema drift is impossible by construction: a change to a
// decision choice set in schemas flows through here automatically.
var PolicyChoiceSets = map[PolicyChoiceSetID][]string{
	PolicyChoiceSetChangeKind:          choiceSetStrings(schemas.ChangeKindValues),
	PolicyChoiceSetDocumentationImpact: choiceSetStrings(schemas.DocumentationImpactValues),
	PolicyChoiceSetRisk:                choiceSetStrings(schemas.RiskLevelValues),
	PolicyChoiceSetAction:              choiceSetStrings(schemas.ActionValues),
}

// ClosedPolicyChoices is the union of every choice string the policy schema
// accepts for `choice` questions, in canonical order (CLOSED_POLICY_CHOICES).
var ClosedPolicyChoices = func() []string {
	union := make([]string, 0,
		len(schemas.ChangeKindValues)+
			len(schemas.DocumentationImpactValues)+
			len(schemas.RiskLevelValues)+
			len(schemas.ActionValues))
	for _, id := range PolicyChoiceSetIDs {
		union = append(union, PolicyChoiceSets[id]...)
	}
	return union
}()

// closedPolicyChoiceSet is the membership index backing IsClosedPolicyChoice
// (CLOSED_POLICY_CHOICE_SET).
var closedPolicyChoiceSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(ClosedPolicyChoices))
	for _, choice := range ClosedPolicyChoices {
		set[choice] = struct{}{}
	}
	return set
}()

// IsClosedPolicyChoice reports whether choice is a member of a closed policy
// choice set (isClosedPolicyChoice).
//
// Binding rules for policy authors and the compiler:
//
//  1. Policy questions may ONLY branch on question keys — never on free-form
//     expressions about the change (ARCH §8.3: "anything not expressible in this
//     schema must be expressed as a new schema element via the Policy
//     Architect").
//  2. A `choice` question's `choices` array must be a closed set drawn from
//     POLICY_CHOICE_SETS. Introducing new choice strings is a schema change via
//     the Policy Architect + compiler, never prompt prose. The Jev policy
//     schema enforces this at parse time.
func IsClosedPolicyChoice(choice string) bool {
	_, ok := closedPolicyChoiceSet[choice]
	return ok
}

// choiceSetStrings converts a canonical typed value array into the plain
// string view choice-sets carries (the TS arrays are readonly string tuples).
func choiceSetStrings[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}
