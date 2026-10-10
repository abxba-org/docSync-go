// Ported from packages/schemas/src/v1/judge-verdict.ts
package schemas

import "sort"

// JudgeVerdictSchemaID is the wire schema id of the Tier-3 judge body
// (`docsync.judge-verdict/v1`).
const JudgeVerdictSchemaID = "docsync.judge-verdict/v1"

// JudgeVerdictBody is the schema-validated MODEL RESPONSE body of the
// independent Tier-3 judge role (JUDGE_VERDICT_SCHEMA; ARCH §4.8 Tier 3, D13).
// This is the WIRE CONTRACT the judge adapter enforces on the model's output —
// the model emits ONLY this body; the report's `judge` block (see
// verification-report.go) is the assembled artifact where the verifier adds
// the identity/binding fields it owns (`model`, `requested_model`, `provider`
// from telemetry — D3/DX-P2-2 actually-routed pinning).
//
//   - `verdict` reuses the canonical VerificationVerdict closed set
//     (`pass | review | reject`) — one verdict taxonomy across artifacts (OD-2).
//   - `questions` reuses VerificationJudgeAnswer (value + probability, ARCH
//     §5.4 convention), keyed by the typed Tier-3 question ids of
//     VerificationJudgeQuestionIds (the universal four; the record keys stay
//     open — repository/policy-additive questions extend the set per policy
//     version, never this schema).
//   - `reasons` attaches the judge's evidence summary (invariant 5 —
//     verification evidence): MANDATORY for `reject` (fail closed,
//     superRefine), optional for `pass`/`review`. Note the asymmetry with the
//     report's judge block, which requires min(1) when present — the verifier
//     omits empty reason arrays when assembling the report from a body.
//
// ABSTENTION IS NOT IN THIS BODY: there is no `abstain`/`abstain_reason`
// field here. Judge abstention is first-class at the ADAPTER level
// (`JudgeResult { kind: 'abstain', reason }`) and in the report
// (`judge_abstain`); a body carrying any extra key fails strict parsing by
// construction, so a model cannot "declare" its own abstention — refusing to
// decide is a typed, system-owned outcome (D10/D13).
type JudgeVerdictBody struct {
	Verdict   VerificationVerdict                `json:"verdict"`
	Questions map[string]VerificationJudgeAnswer `json:"questions,omitempty"`
	Reasons   []string                           `json:"reasons,omitempty"`
}

// Validate checks the body against JUDGE_VERDICT_SCHEMA, including the
// superRefine evidence-attachment rule: a `reject` verdict without its
// evidence reasons is unusable downstream (the report's canonical mapping
// resolves a judge reject ⇒ report reject via ResolveVerificationVerdict) —
// never accept it (fail closed, invariant 5).
func (b *JudgeVerdictBody) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(b.Verdict), []string{"pass", "review", "reject"}, []string{"verdict"}))
	// Sorted keys keep multi-issue output deterministic (Go map iteration
	// order is randomized).
	keys := make([]string, 0, len(b.Questions))
	for key := range b.Questions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// z.record(z.string().min(1), …) — the key itself is a min(1) string.
		if len(key) < 1 {
			Collect(&issues, &Issue{Path: []string{"questions", key}, Message: "String must contain at least 1 character(s)"})
		}
		answer := b.Questions[key]
		if answer.Probability < 0 {
			Collect(&issues, &Issue{Path: []string{"questions", key, "probability"}, Message: "Number must be greater than or equal to 0"})
		}
		if answer.Probability > 1 {
			Collect(&issues, &Issue{Path: []string{"questions", key, "probability"}, Message: "Number must be less than or equal to 1"})
		}
	}
	for i, reason := range b.Reasons {
		Collect(&issues, RequiredString(reason, 1, []string{"reasons", itoa(i)}))
	}
	if b.Verdict == VerdictReject && len(b.Reasons) == 0 {
		Collect(&issues, &Issue{
			Path: []string{"reasons"},
			Message: "a reject verdict must attach its evidence reasons " +
				"(fail closed, invariant 5: verification evidence)",
		})
	}
	return issues
}

// JudgeVerdictExample is the deterministic example covering ALL FOUR universal
// Tier-3 question ids (VerificationJudgeQuestionIds) — validated against the
// schema in tests (V-P0-2 shape) and reused as the recorded-double fixture
// input by the judge-adapter tests.
var JudgeVerdictExample = JudgeVerdictBody{
	Verdict: VerdictPass,
	Questions: map[string]VerificationJudgeAnswer{
		"stale_before":              {Value: "true", Probability: 0.97},
		"resolved_after":            {Value: "true", Probability: 0.94},
		"minimal":                   {Value: "true", Probability: 0.98},
		"introduced_new_inaccuracy": {Value: "false", Probability: 0.96},
	},
	Reasons: []string{"all three tiers green; judge independently confirmed the drift is resolved"},
}

// ParseJudgeVerdictBody strictly parses and validates a judge verdict body,
// mirroring the zod strictObject contract (unknown keys — including any
// abstention declaration — are rejected).
func ParseJudgeVerdictBody(data []byte) (*JudgeVerdictBody, error) {
	var b JudgeVerdictBody
	if err := StrictDecode(data, &b); err != nil {
		return nil, err
	}
	if issues := b.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &b, nil
}
