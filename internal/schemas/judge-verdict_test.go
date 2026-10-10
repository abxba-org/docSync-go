package schemas_test

// Ported from packages/schemas/test/judge-verdict.test.ts — the
// `docsync.judge-verdict/v1` Tier-3 judge body contract (P4-T15 groundwork /
// P4-T21 retry surface): example validation (all four universal Tier-3
// question ids), the reject-must-carry-reasons refinement (invariant 5, fail
// closed), the open question-key surface, probability bounds, abstention NOT
// in the body, and the mirror pin against the verification report's judge
// block. Black-box: only exported API.

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jvJSON marshals v to JSON bytes.
func jvJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

// jvParseIssues strictly parses data and returns the validation issues it
// produced (the parse must fail).
func jvParseIssues(t *testing.T, data []byte) []schemas.Issue {
	t.Helper()
	_, err := schemas.ParseJudgeVerdictBody(data)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Issues
}

func jvFind(issues []schemas.Issue, path []string, message string) bool {
	for _, iss := range issues {
		if assert.ObjectsAreEqual(path, iss.Path) && iss.Message == message {
			return true
		}
	}
	return false
}

func TestJudgeVerdictExampleValidates(t *testing.T) {
	require.Empty(t, schemas.JudgeVerdictExample.Validate())

	parsed, err := schemas.ParseJudgeVerdictBody(jvJSON(t, schemas.JudgeVerdictExample))
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Equal(t, schemas.VerdictPass, parsed.Verdict)
	assert.Len(t, parsed.Reasons, 1)
}

func TestJudgeVerdictExampleCanonicalJSONGolden(t *testing.T) {
	var tree any
	require.NoError(t, json.Unmarshal(jvJSON(t, schemas.JudgeVerdictExample), &tree))
	canonical, err := docutil.CanonicalJSON(tree)
	require.NoError(t, err)
	assert.Equal(t,
		`{"questions":{"introduced_new_inaccuracy":{"probability":0.96,"value":"false"},`+
			`"minimal":{"probability":0.98,"value":"true"},"resolved_after":{"probability":0.94,"value":"true"},`+
			`"stale_before":{"probability":0.97,"value":"true"}},`+
			`"reasons":["all three tiers green; judge independently confirmed the drift is resolved"],`+
			`"verdict":"pass"}`,
		canonical)
}

func TestJudgeVerdictExampleCoversAllFourUniversalQuestionIds(t *testing.T) {
	keys := make([]string, 0, len(schemas.JudgeVerdictExample.Questions))
	for key := range schemas.JudgeVerdictExample.Questions {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	universal := append([]string(nil), schemas.VerificationJudgeQuestionIds...)
	sort.Strings(universal)
	assert.Equal(t, universal, keys)
}

func TestJudgeVerdictRejectWithoutReasonsFailsClosed(t *testing.T) {
	// superRefine: reject + no reasons ⇒ fail closed (invariant 5).
	body := schemas.JudgeVerdictBody{Verdict: schemas.VerdictReject}
	issues := body.Validate()
	require.NotEmpty(t, issues)
	found := false
	for _, iss := range issues {
		if assert.ObjectsAreEqual([]string{"reasons"}, iss.Path) &&
			iss.Message == "a reject verdict must attach its evidence reasons (fail closed, invariant 5: verification evidence)" {
			found = true
		}
	}
	assert.True(t, found, "expected the reject-reasons refinement issue, got %v", issues)

	// The message must carry the invariant-5 marker the TS test pins.
	assert.Contains(t, issues[0].Message, "invariant 5")

	// Wire level too.
	assert.NotEmpty(t, jvParseIssues(t, []byte(`{"verdict":"reject"}`)))
}

func TestJudgeVerdictRejectWithEmptyReasonsFailsClosed(t *testing.T) {
	body := schemas.JudgeVerdictBody{Verdict: schemas.VerdictReject, Reasons: []string{}}
	assert.NotEmpty(t, body.Validate())

	assert.NotEmpty(t, jvParseIssues(t, []byte(`{"verdict":"reject","reasons":[]}`)))
}

func TestJudgeVerdictRejectWithReasonsParses(t *testing.T) {
	body := schemas.JudgeVerdictBody{
		Verdict: schemas.VerdictReject,
		Questions: map[string]schemas.VerificationJudgeAnswer{
			"minimal": {Value: "false", Probability: 0.82},
		},
		Reasons: []string{"hunk 1 over-deletes the rollback section"},
	}
	require.Empty(t, body.Validate())
	_, err := schemas.ParseJudgeVerdictBody(jvJSON(t, body))
	require.NoError(t, err)

	// reasons entries are min(1) strings — an empty entry fails.
	emptyReason := schemas.JudgeVerdictBody{Verdict: schemas.VerdictReview, Reasons: []string{""}}
	issues := emptyReason.Validate()
	require.NotEmpty(t, issues)
	assert.True(t, jvFind(issues, []string{"reasons", "0"}, "String must contain at least 1 character(s)"),
		"expected the reasons.0 min(1) issue, got %v", issues)

	assert.NotEmpty(t, jvParseIssues(t, []byte(`{"verdict":"review","reasons":[""]}`)))
}

func TestJudgeVerdictPassReviewMayOmitReasons(t *testing.T) {
	passOnly := schemas.JudgeVerdictBody{Verdict: schemas.VerdictPass}
	require.Empty(t, passOnly.Validate())
	reviewEmpty := schemas.JudgeVerdictBody{Verdict: schemas.VerdictReview, Reasons: []string{}}
	require.Empty(t, reviewEmpty.Validate())

	_, err := schemas.ParseJudgeVerdictBody([]byte(`{"verdict":"pass"}`))
	require.NoError(t, err)
	_, err = schemas.ParseJudgeVerdictBody([]byte(`{"verdict":"review","reasons":[]}`))
	require.NoError(t, err)

	// verdict is the only required field.
	issues := jvParseIssues(t, []byte(`{}`))
	assert.True(t, jvFind(issues, []string{"verdict"},
		"Invalid enum value. Expected 'pass' | 'review' | 'reject', received ''"),
		"expected the missing-verdict enum issue, got %v", issues)

	// Unknown verdict values are rejected by the closed set.
	issues = jvParseIssues(t, []byte(`{"verdict":"maybe"}`))
	assert.True(t, jvFind(issues, []string{"verdict"},
		"Invalid enum value. Expected 'pass' | 'review' | 'reject', received 'maybe'"),
		"expected the invalid-verdict enum issue, got %v", issues)
}

func TestJudgeVerdictQuestionsKeysAreOpenBeyondTheUniversalFour(t *testing.T) {
	extended := schemas.JudgeVerdictBody{
		Verdict: schemas.VerdictReview,
		Questions: map[string]schemas.VerificationJudgeAnswer{
			"stale_before":         {Value: "true", Probability: 1},
			"custom_repo_question": {Value: "n/a", Probability: 0.5},
		},
	}
	require.Empty(t, extended.Validate())

	_, err := schemas.ParseJudgeVerdictBody(jvJSON(t, extended))
	require.NoError(t, err)
}

func TestJudgeVerdictQuestionsProbabilityMustBeWithinZeroAndOne(t *testing.T) {
	over := schemas.JudgeVerdictBody{
		Verdict: schemas.VerdictReview,
		Questions: map[string]schemas.VerificationJudgeAnswer{
			"minimal": {Value: "true", Probability: 1.01},
		},
	}
	issues := over.Validate()
	require.NotEmpty(t, issues)
	assert.True(t, jvFind(issues, []string{"questions", "minimal", "probability"},
		"Number must be less than or equal to 1"),
		"expected the probability max issue, got %v", issues)

	under := schemas.JudgeVerdictBody{
		Verdict: schemas.VerdictReview,
		Questions: map[string]schemas.VerificationJudgeAnswer{
			"minimal": {Value: "true", Probability: -0.1},
		},
	}
	issues = under.Validate()
	require.NotEmpty(t, issues)
	assert.True(t, jvFind(issues, []string{"questions", "minimal", "probability"},
		"Number must be greater than or equal to 0"),
		"expected the probability min issue, got %v", issues)

	// Wire level: probability 1.01 fails.
	assert.NotEmpty(t, jvParseIssues(t, []byte(`{"verdict":"review","questions":{"minimal":{"value":"true","probability":1.01}}}`)))

	// Record keys are min(1) strings (z.record(z.string().min(1), …)).
	emptyKey := schemas.JudgeVerdictBody{
		Verdict: schemas.VerdictReview,
		Questions: map[string]schemas.VerificationJudgeAnswer{
			"": {Value: "true", Probability: 0.5},
		},
	}
	issues = emptyKey.Validate()
	require.NotEmpty(t, issues)
	assert.True(t, jvFind(issues, []string{"questions", ""}, "String must contain at least 1 character(s)"),
		"expected the empty question-key issue, got %v", issues)
}

func TestJudgeVerdictQuestionsValueIsRequired(t *testing.T) {
	// zod VERIFICATION_JUDGE_ANSWER_SCHEMA requires `value` (z.string()); the
	// Go struct cannot distinguish an absent key from an empty one, so the
	// wire contract is pinned here at the raw-JSON level: the key must be
	// present in the body.
	const body = `{"verdict":"review","questions":{"minimal":{"probability":0.5}}}`
	var raw map[string]json.RawMessage
	require.NoError(t, schemas.StrictDecode([]byte(body), &raw))
	var questions map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["questions"], &questions))
	answer, ok := questions["minimal"]
	require.True(t, ok)
	_, hasValue := answer["value"]
	assert.False(t, hasValue, "the judge answer must carry a value key")

	const withValue = `{"verdict":"review","questions":{"minimal":{"value":"true","probability":0.5}}}`
	parsed, err := schemas.ParseJudgeVerdictBody([]byte(withValue))
	require.NoError(t, err)
	require.Contains(t, parsed.Questions, "minimal")
	assert.Equal(t, "true", parsed.Questions["minimal"].Value)
}

func TestJudgeVerdictBodyRejectsAbstentionKeys(t *testing.T) {
	// Abstention is NOT in the body: any extra key fails strict parsing — a
	// model cannot declare its own abstention (D10/D13).
	assert.NotEmpty(t, jvParseIssues(t, []byte(
		`{"verdict":"pass","abstain":true,"abstain_reason":"verifier_uncertain"}`)))
}

func TestJudgeVerdictMirrorsReportJudgeBlock(t *testing.T) {
	// Mirror pin: the verdict/questions/reasons trio of the verification
	// report example's judge block (VERIFICATION_REPORT_EXAMPLE.judge) must
	// validate against this body contract — the block adds only the
	// verifier-owned identity/binding fields (model/requested_model/provider).
	judge := schemas.VerificationJudge{
		Model:          "anthropic/claude-sonnet-4",
		RequestedModel: jvString("anthropic/claude-sonnet-4"),
		Provider:       jvString("anthropic"),
		Verdict:        schemas.VerdictPass,
		Questions: map[string]schemas.VerificationJudgeAnswer{
			"stale_before":              {Value: "true", Probability: 0.97},
			"resolved_after":            {Value: "true", Probability: 0.94},
			"minimal":                   {Value: "true", Probability: 0.98},
			"introduced_new_inaccuracy": {Value: "false", Probability: 0.96},
		},
		Reasons: []string{"all three tiers green; judge independently confirmed the drift is resolved"},
	}
	body := schemas.JudgeVerdictBody{
		Verdict:   judge.Verdict,
		Questions: judge.Questions,
		Reasons:   judge.Reasons,
	}
	require.NotEmpty(t, judge.Questions, "the report example judge block carries questions")
	require.NotNil(t, judge.RequestedModel)
	require.NotNil(t, judge.Provider)
	require.Empty(t, body.Validate())
	_, err := schemas.ParseJudgeVerdictBody(jvJSON(t, body))
	require.NoError(t, err)
}

// jvString returns a pointer to s (verifier-owned optional binding fields).
func jvString(s string) *string { return &s }
