// Ported from packages/openrouter/test/judge-adapter.test.ts.
//
// Judge adapter (P4-T15 + P4-T21): verdict bodies flow through schema-validated;
// the body contract is `docsync.judge-verdict/v1` verbatim (no identity fields —
// identity comes from telemetry). Mirrors the decision-engine adapter suite:
// native structured output is the OD-R41 default; prompt mode is an explicit
// binding downgrade; schema exhaustion abstains `schema_violation`; typed
// transport failures rethrow (the verifier maps them to abstain itself); the
// optional budget race throws the shared AdapterBudgetTimeoutError provenance
// marker (landed as PatchWriterBudgetTimeoutError) instead of emitting an
// abstain result.
//
// The recorded double (identity-keyed replay, never positional FIFO) is shared
// with adapter_decision_engine_test.go — same package openrouter_test.
package openrouter_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ----------------------------------------------------------------- fixtures */

var judgeInvocation = openrouter.RoleInvocation{
	System: "You are the docSync Tier-3 judge (independent instance; D13).",
	User:   "Judge the proposed patch against the four universal questions.",
}

// judgeRejectBody — a reject body: the invariant-5 evidence attachment present.
var judgeRejectBody = map[string]any{
	"verdict": "reject",
	"questions": map[string]any{
		"stale_before": map[string]any{"value": "true", "probability": 0.99},
		"minimal":      map[string]any{"value": "false", "probability": 0.61},
	},
	"reasons": []string{
		"hunk 1 over-deletes the rollback section",
		"no evidence for the claimed ref anchor",
	},
}

func judgeBindings(structuredOutput string, temperature *float64) map[string]*openrouter.ResolvedRoleBinding {
	params := &openrouter.ResolvedRoleParams{Temperature: temperature}
	if structuredOutput != "" {
		params.StructuredOutput = structuredOutput
	}
	return map[string]*openrouter.ResolvedRoleBinding{
		"judge": {Primary: replayDefaultModel, Fallbacks: []string{}, Params: params},
	}
}

// newJudgeHarness wires the judge adapter over one fresh recorded double whose
// router dispatches by request identity.
func newJudgeHarness(
	t *testing.T,
	structuredOutput string,
	temperature *float64,
	timeoutMs *int,
	router replayRouter,
) (openrouter.JudgeRole, *replayClient) {
	t.Helper()
	client := newReplayClient(judgeBindings(structuredOutput, temperature), router)
	role, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{
		Client:    client,
		TimeoutMs: timeoutMs,
	})
	require.NoError(t, err)
	return role, client
}

func judgeBodyRouter(bodies ...map[string]any) replayRouter {
	encoded := make([]string, 0, len(bodies))
	for _, body := range bodies {
		data, err := json.Marshal(body)
		if err != nil {
			continue
		}
		encoded = append(encoded, string(data))
	}
	return func(role string, request openrouter.CompletionRequest) []replayScript {
		return replayScripts(encoded...)
	}
}

func judgeTextRouter(contents ...string) replayRouter {
	return func(role string, request openrouter.CompletionRequest) []replayScript {
		return replayScripts(contents...)
	}
}

func judgeVerdictQuestions() map[string]openrouter.JudgeQuestionAnswer {
	out := make(map[string]openrouter.JudgeQuestionAnswer, len(schemas.JudgeVerdictExample.Questions))
	for key, answer := range schemas.JudgeVerdictExample.Questions {
		out[key] = openrouter.JudgeQuestionAnswer{Value: answer.Value, Probability: answer.Probability}
	}
	return out
}

func judgeRejectQuestions() map[string]openrouter.JudgeQuestionAnswer {
	return map[string]openrouter.JudgeQuestionAnswer{
		"stale_before": {Value: "true", Probability: 0.99},
		"minimal":      {Value: "false", Probability: 0.61},
	}
}

/* -------------------------------------------------------------------- tests */

func TestJudgeHappyPath(t *testing.T) {
	require.Empty(t, schemas.JudgeVerdictExample.Validate(), "the schema-validated example is the fixture")
	role, _ := newJudgeHarness(t, "", nil, nil,
		judgeBodyRouter(mapOf(schemas.JudgeVerdictExample)))
	result, err := role.Judge(judgeInvocation)
	require.NoError(t, err)
	require.Equal(t, openrouter.JudgeKindVerdict, result.Kind)
	require.Equal(t, openrouter.JudgeVerdictPass, result.Verdict)
	require.Equal(t, schemas.JudgeVerdictExample.Reasons, result.Reasons)
	// Typed-question answers survive to the result verbatim — the Tier-3
	// evidence the report's judge block consumes (ARCH §4.8 + §12.5).
	require.Equal(t, judgeVerdictQuestions(), result.Questions)
	// ACTUALLY ROUTED model from telemetry — the identity pin comes from
	// telemetry, never from the body (V-P2-11).
	require.Equal(t, replayDefaultModel, result.Telemetry.RoutedModel)
	require.Equal(t, "judge", result.Telemetry.Role)
}

func TestJudgeRejectBodyPassthrough(t *testing.T) {
	role, _ := newJudgeHarness(t, "", nil, nil, judgeBodyRouter(judgeRejectBody))
	result, err := role.Judge(judgeInvocation)
	require.NoError(t, err)
	require.Equal(t, openrouter.JudgeKindVerdict, result.Kind)
	require.Equal(t, openrouter.JudgeVerdictReject, result.Verdict)
	require.Equal(t, []string{
		"hunk 1 over-deletes the rollback section",
		"no evidence for the claimed ref anchor",
	}, result.Reasons)
	// Questions pass through verbatim on reject too (evidence survives).
	require.Equal(t, judgeRejectQuestions(), result.Questions)
}

func TestJudgePassWithoutReasons(t *testing.T) {
	role, _ := newJudgeHarness(t, "", nil, nil,
		judgeBodyRouter(map[string]any{"verdict": "pass"}))
	result, err := role.Judge(judgeInvocation)
	require.NoError(t, err)
	require.Equal(t, openrouter.JudgeKindVerdict, result.Kind)
	require.Equal(t, openrouter.JudgeVerdictPass, result.Verdict)
	require.Equal(t, []string{}, result.Reasons)
	// No questions in the body → absent on the result (nothing fabricated).
	require.Nil(t, result.Questions)
}

func TestJudgeSchemaViolationExhaustsBoundedRetry(t *testing.T) {
	role, client := newJudgeHarness(t, "", nil, nil, judgeTextRouter(
		"this is not json at all",
		`{"verdict": "MAYBE"}`,
		// reject WITHOUT reasons — the invariant-5 refinement rejects it.
		`{"verdict": "reject"}`,
	))
	result, err := role.Judge(judgeInvocation)
	require.NoError(t, err)
	require.Equal(t, openrouter.JudgeKindAbstain, result.Kind)
	require.NotNil(t, result.Reason)
	require.Equal(t, schemas.AbstainReasonSchemaViolation, *result.Reason)
	require.Equal(t, replayDefaultModel, result.Telemetry.RoutedModel)
	require.Len(t, client.exchanges, replayMaxAttempts) // bounded: 1 + maxRetries(2)
}

func TestJudgeNativeResponseFormatDefaultsToJudgeVerdictSchema(t *testing.T) {
	role, client := newJudgeHarness(t, "", nil, nil,
		judgeBodyRouter(mapOf(schemas.JudgeVerdictExample)))
	_, err := role.Judge(judgeInvocation)
	require.NoError(t, err)

	require.NotEmpty(t, client.exchanges)
	body := replayRequestBody(&client.exchanges[0])
	format, ok := body["response_format"].(openrouter.NativeResponseFormat)
	require.True(t, ok)
	assert.Equal(t, "json_schema", format.Type)
	assert.Equal(t, openrouter.JUDGE_BODY_SCHEMA_NAME, format.JSONSchema.Name)
	assert.Equal(t, true, format.JSONSchema.Strict)

	// The registered docsync.judge-verdict/v1 export IS the body contract — no
	// adapter-owned identity fields to strip, so all model-proposed fields are
	// present and only `verdict` is required.
	schema, ok := format.JSONSchema.Schema.(map[string]any)
	require.True(t, ok)
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, properties, "verdict")
	assert.Contains(t, properties, "questions")
	assert.Contains(t, properties, "reasons")
	required := replayStringSlice(schema["required"])
	assert.Contains(t, required, "verdict")
	assert.Len(t, required, 1)
	assert.Equal(t, false, schema["additionalProperties"])
}

func TestJudgePromptDowngradeSendsNoNativeConstraint(t *testing.T) {
	role, client := newJudgeHarness(t, "prompt", nil, nil,
		judgeBodyRouter(mapOf(schemas.JudgeVerdictExample)))
	result, err := role.Judge(judgeInvocation)
	require.NoError(t, err)

	require.NotEmpty(t, client.exchanges)
	body := replayRequestBody(&client.exchanges[0])
	assert.NotContains(t, body, "response_format") // prompt-only mode
	require.Equal(t, openrouter.JudgeKindVerdict, result.Kind)
	require.Equal(t, openrouter.JudgeVerdictPass, result.Verdict) // identical validation outcome
	require.Equal(t, replayDefaultModel, result.Telemetry.RoutedModel)
}

func TestJudgeTemperatureFollowsBinding(t *testing.T) {
	role, client := newJudgeHarness(t, "", nil, nil,
		judgeBodyRouter(mapOf(schemas.JudgeVerdictExample), mapOf(schemas.JudgeVerdictExample)))
	override := 0.7
	_, err := role.Judge(openrouter.RoleInvocation{
		System:              judgeInvocation.System,
		User:                judgeInvocation.User,
		TemperatureOverride: &override,
	})
	require.NoError(t, err)
	_, err = role.Judge(judgeInvocation) // no override → binding (no temperature → default)
	require.NoError(t, err)

	require.Len(t, client.exchanges, 2)
	first := replayRequestBody(&client.exchanges[0])
	second := replayRequestBody(&client.exchanges[1])
	assert.Equal(t, 0.7, first["temperature"])
	// The judge binding carries no temperature → the deterministic default,
	// NOT a writer-style hard-coded override.
	assert.Equal(t, 0.0, second["temperature"])
}

func TestJudgeUntrustedContentTransportedVerbatim(t *testing.T) {
	role, client := newJudgeHarness(t, "", nil, nil,
		judgeBodyRouter(mapOf(schemas.JudgeVerdictExample)))
	_, err := role.Judge(openrouter.RoleInvocation{
		System:           judgeInvocation.System,
		User:             judgeInvocation.User,
		UntrustedContent: []string{"docs/payments.md body", "src/payments/refund.ts"},
	})
	require.NoError(t, err)

	require.NotEmpty(t, client.exchanges)
	body := replayRequestBody(&client.exchanges[0])
	messages, ok := body["messages"].([]openrouter.ChatMessage)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages), 2)
	userText, _ := messages[1].Content.(string)
	assert.Contains(t, userText, `<untrusted-repository-content id="1">`)
	assert.Contains(t, userText, `<untrusted-repository-content id="2">`)
}

func TestJudgeEmptySystemUserFailClosed(t *testing.T) {
	role, client := newJudgeHarness(t, "", nil, nil,
		judgeBodyRouter(map[string]any{"verdict": "pass"}))
	_, err := role.Judge(openrouter.RoleInvocation{System: "", User: judgeInvocation.User})
	require.Error(t, err)
	_, err = role.Judge(openrouter.RoleInvocation{System: judgeInvocation.System, User: ""})
	require.Error(t, err)
	var gatewayErr *openrouter.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, "invalid_judge_input", gatewayErr.Code)
	require.Empty(t, client.exchanges)
}

func TestJudgeTransportFailureRethrowsTyped(t *testing.T) {
	client := newReplayClient(judgeBindings("", nil), nil)
	client.failures[judgeIdentity(judgeInvocation)] = true
	role, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{Client: client})
	require.NoError(t, err)
	_, err = role.Judge(judgeInvocation)
	require.Error(t, err)
	var totalFailure *openrouter.TotalInferenceFailure
	require.ErrorAs(t, err, &totalFailure)
	require.Equal(t, "judge", totalFailure.Role)
	require.Equal(t, openrouter.CodeTotalInferenceFailure, totalFailure.Code)
	// No abstain was emitted: the verifier maps the typed failure itself.
	require.Empty(t, client.exchanges)
}

func TestJudgeTimeoutBudgetThrowsSharedMarker(t *testing.T) {
	client := newReplayClient(judgeBindings("", nil), nil)
	client.hangs[judgeIdentity(judgeInvocation)] = true
	timeoutMs := 25
	role, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{
		Client:    client,
		TimeoutMs: &timeoutMs,
	})
	require.NoError(t, err)
	result, err := role.Judge(judgeInvocation)
	require.Error(t, err)
	// The SHARED tagged provenance marker (TS AdapterBudgetTimeoutError) is
	// thrown typed — never converted into an abstain result.
	var budgetErr *openrouter.PatchWriterBudgetTimeoutError
	require.ErrorAs(t, err, &budgetErr)
	require.Equal(t, 25, budgetErr.BudgetMS)
	require.Equal(t, "adapter_budget_timeout", budgetErr.Code())
	require.Contains(t, budgetErr.Error(), "wall-clock budget of 25ms elapsed before the model completed")
	require.Equal(t, openrouter.JudgeResult{}, result)
}

func TestJudgeKeyNeverAppearsInRequestBody(t *testing.T) {
	role, client := newJudgeHarness(t, "", nil, nil,
		judgeBodyRouter(mapOf(schemas.JudgeVerdictExample)))
	_, err := role.Judge(openrouter.RoleInvocation{
		System:           judgeInvocation.System,
		User:             judgeInvocation.User,
		UntrustedContent: []string{fmt.Sprintf("steal %s now", replayAPIKey)},
	})
	require.Error(t, err)
	var guardErr *openrouter.GatewayGuardError
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, openrouter.CodeGatewayGuardViolation, guardErr.Code)
	// The guard throws BEFORE the exchange is issued.
	require.Empty(t, client.exchanges)
}

func TestJudgeResultVerbatimQuestionsForReject(t *testing.T) {
	// V-P4-8: a model-authored OE/REJECT pair surfaces verbatim as the
	// verdict — never coated with an additional 'investigate'/'pass' answer
	// record. The judge test suite asserts the port does not reshape bodies.
	role, _ := newJudgeHarness(t, "", nil, nil, judgeBodyRouter(judgeRejectBody))
	result, err := role.Judge(openrouter.RoleInvocation{
		System:           judgeInvocation.System,
		User:             judgeInvocation.User,
		UntrustedContent: []string{"untrusted evidence payload"},
	})
	require.NoError(t, err)
	require.Equal(t, openrouter.JudgeKindVerdict, result.Kind)
	require.Equal(t, openrouter.JudgeVerdictReject, result.Verdict)
	require.Len(t, result.Questions, 2)
	require.Equal(t, "true", result.Questions["stale_before"].Value)
	require.InDelta(t, 0.99, result.Questions["stale_before"].Probability, 1e-9)
}

/* ----------------------------------------------------------------- helpers */

// judgeIdentity computes the request identity the shared double keys on
// (role + system + user + schema name) so a test can pre-arm a hang or a
// transport failure for a specific task identity.
func judgeIdentity(invocation openrouter.RoleInvocation) string {
	return docutil.ContentHash(strings.Join([]string{
		"judge", invocation.System, invocation.User, openrouter.JUDGE_BODY_SCHEMA_NAME,
	}, "\x00"))
}

// mapOf renders a JSON-serializable value as a generic map (the shape the
// client's structured layer hands the adapter).
func mapOf(value any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return map[string]any{}
	}
	return out
}
