// Ported from packages/openrouter/test/adapters/decision-engine.test.ts.
//
// The decision-engine adapter suite (P4-T15): round-trip identity fields,
// decision_id determinism, abstain passthrough, bounded schema-violation
// abstention, the prompt contract (closed sets + answer contract + policy
// questions + containment wrapping), and the OD-R41 native/prompt
// structured-output modes.
//
// Test double (AGENT-behavior replay rule): the recorded double dispatches by
// REQUEST IDENTITY — a router selects the recorded script from the OUTGOING
// REQUEST (role|system|user|schemaName), and each resolved identity owns its
// ordered corrective-attempt script (the bounded loop's 1 + maxRetries(2) = 3
// attempts on that identity). Nothing is consumed positionally, so two
// concurrent tasks can never cross-wire each other's recorded bodies. The
// double emulates the client contract the adapters consume — message
// composition with cache-prefixing, containment wrapping, the API-key guard via
// the landed credentials.AssertNoSecretInPayload, the bounded corrective loop
// through the landed structured.ValidateStructured (the SAME validator the real
// client runs, over the adapter's own zod-form schema), and telemetry — so the
// same suite runs against the real *OpenRouterClient through the unchanged
// chatCompletionPort seam once client-level fixtures exist.
//
// The shared double declared here is reused by adapter_judge_test.go (same
// package openrouter_test).
package openrouter_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ doubles */

const (
	replayAPIKey       = "test-key-value-1234567890"
	replayDefaultModel = "z-ai/glm-4.7"
	replayMaxAttempts  = 3 // 1 + maxRetries(2) — the bounded corrective loop
)

// replayScript is one recorded model output for a request identity.
type replayScript struct {
	content string
	// model overrides the response `model` field ("" → replayDefaultModel).
	model string
}

// replayExchange is one recorded transport exchange (the request as issued
// plus the request body and parsed response body).
type replayExchange struct {
	request  openrouter.CompletionRequest
	body     map[string]any
	response map[string]any
}

// replayRouter selects the recorded script for a role+request by REQUEST
// IDENTITY (never by consumption order — the AGENT-behavior replay rule).
type replayRouter func(role string, request openrouter.CompletionRequest) []replayScript

// replayClient is the recorded client double: it satisfies the adapters'
// chatCompletionPort seam structurally, resolves role bindings from an injected
// map, and replays identity-resolved scripts through the bounded corrective
// loop (message composition, containment wrapping, the API-key guard and the
// schema validation all mirror the landed client).
type replayClient struct {
	apiKey    string
	bindings  map[string]*openrouter.ResolvedRoleBinding
	router    replayRouter
	scripts   map[string][]replayScript
	hangs     map[string]bool
	failures  map[string]bool
	exchanges []replayExchange
	latencyMs int
}

func newReplayClient(
	bindings map[string]*openrouter.ResolvedRoleBinding,
	router replayRouter,
) *replayClient {
	return &replayClient{
		apiKey:   replayAPIKey,
		bindings: bindings,
		router:   router,
		scripts:  map[string][]replayScript{},
		hangs:    map[string]bool{},
		failures: map[string]bool{},
	}
}

// BindingFor is the client seam's binding resolution (fail-closed typed error
// on an unbound role).
func (c *replayClient) BindingFor(role string) (*openrouter.ResolvedRoleBinding, error) {
	binding, ok := c.bindings[role]
	if !ok {
		names := make([]string, 0, len(c.bindings))
		for name := range c.bindings {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, openrouter.NewRoleBindingError(role,
			"role '"+role+"' has no binding in models.yml (docsync.models/v1); bound roles: "+strings.Join(names, ", "))
	}
	return binding, nil
}

// Complete runs one bounded role completion: build the messages, apply the
// gateway guard, issue the exchange (recording it), and run the bounded
// corrective-retry loop on the same target.
func (c *replayClient) Complete(role string, request openrouter.CompletionRequest) (openrouter.RoleCompletion, error) {
	binding := c.bindings[role]
	identity := replayIdentity(role, request)
	if c.hangs[identity] {
		// Never settles — the adapter's budget race must fire.
		select {}
	}
	baseMessages := replayBuildMessages(request)
	var correctiveMessages []openrouter.ChatMessage
	var walkAttempts []openrouter.ModelAttempt
	var lastText string
	var lastModel string
	for attempt := 1; attempt <= replayMaxAttempts; attempt++ {
		body := replayBuildRequestBody(request, binding, append(append([]openrouter.ChatMessage{}, baseMessages...), correctiveMessages...))
		if err := openrouter.AssertNoSecretInPayload(body, c.apiKey); err != nil {
			// The guard throws BEFORE any exchange is issued.
			return openrouter.RoleCompletion{}, err
		}
		if c.failures[identity] {
			walkAttempts = append(walkAttempts, openrouter.ModelAttempt{
				Target:    binding.Primary,
				Attempt:   attempt,
				Error:     "network unreachable",
				LatencyMS: c.latencyMs,
			})
			continue
		}
		script := c.resolveScript(identity, role, request)
		if len(script) == 0 {
			return openrouter.RoleCompletion{}, openrouter.NewReplayExhaustedError(fmt.Sprintf(
				"replay script for identity %s exhausted after %d exchange(s)", identity, attempt))
		}
		next := script[0]
		c.scripts[identity] = script[1:]
		model := replayDefaultModel
		if next.model != "" {
			model = next.model
		}
		lastModel = model
		response := map[string]any{
			"model": model,
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": next.content},
			}},
		}
		c.exchanges = append(c.exchanges, replayExchange{
			request:  request,
			body:     body,
			response: response,
		})
		lastText = next.content
		// The SAME validation the landed client runs: strict extraction plus
		// the adapter's zap-form schema (the caller schema stays
		// authoritative — OD-R41).
		validation := openrouter.ValidateStructured(openrouter.ValidateStructuredOptions{
			Text:       next.content,
			ZodSchema:  request.ZodSchema,
			JSONSchema: request.JSONSchema,
		})
		if validation.OK {
			return openrouter.RoleCompletion{
				Kind:       openrouter.RoleCompletionKindCompletion,
				Text:       next.content,
				Structured: validation.Value,
				Telemetry: openrouter.RoleTelemetry{
					Role:           role,
					RequestedModel: binding.Primary,
					RoutedModel:    model,
					LatencyMS:      c.latencyMs,
					Attempt:        attempt,
					FallbackUsed:   false,
					PolicyVersion:  request.PolicyVersion,
				},
			}, nil
		}
		if attempt == replayMaxAttempts {
			break
		}
		// Corrective exchange: the base messages plus the model's offending
		// output and the corrective instruction (issued on the SAME target).
		correctiveMessages = []openrouter.ChatMessage{
			{Role: "assistant", Content: next.content},
			{Role: "user", Content: replayCorrectiveMessage(request.SchemaName, validation.Issues)},
		}
	}
	if c.failures[identity] {
		return openrouter.RoleCompletion{}, openrouter.NewTotalInferenceFailure(role, walkAttempts)
	}
	// Bounded corrective-retry exhausted — degrade to the typed
	// schema-abstain (never a fabricated result, D10). Telemetry of the FINAL
	// exchange still flows (V-P2-11): the routed model names the model that
	// actually served the exhausted attempts.
	return openrouter.RoleCompletion{
		Kind:   openrouter.RoleCompletionKindSchemaAbstain,
		Text:   lastText,
		Reason: openrouter.SchemaViolationAbstainReason,
		Telemetry: openrouter.RoleTelemetry{
			Role:           role,
			RequestedModel: binding.Primary,
			RoutedModel:    lastModel,
			LatencyMS:      c.latencyMs,
			Attempt:        replayMaxAttempts,
			PolicyVersion:  request.PolicyVersion,
		},
	}, nil
}

// resolveScript resolves (and caches) the recorded script for a request
// identity — dispatch by the outgoing request, never by call order.
func (c *replayClient) resolveScript(identity, role string, request openrouter.CompletionRequest) []replayScript {
	if script, ok := c.scripts[identity]; ok {
		return script
	}
	if c.router != nil {
		script := c.router(role, request)
		c.scripts[identity] = script
		return script
	}
	return nil
}

// replayIdentity is the request identity: role + system + user + schema name.
func replayIdentity(role string, request openrouter.CompletionRequest) string {
	return docutil.ContentHash(strings.Join([]string{
		role, request.System, request.User, request.SchemaName,
	}, "\x00"))
}

// replayBuildMessages mirrors the client's message composition: the system
// prefix is cache-prefixed (an array of parts) when cachePrefix is true, and
// the user message carries the fixed containment instruction, the
// delimiter-wrapped untrusted blocks and the task.
func replayBuildMessages(request openrouter.CompletionRequest) []openrouter.ChatMessage {
	messages := make([]openrouter.ChatMessage, 0, 2)
	if request.CachePrefix != nil && *request.CachePrefix {
		messages = append(messages, openrouter.ChatMessage{
			Role: "system",
			Content: []openrouter.ChatTextPart{{
				Type:         "text",
				Text:         request.System,
				CacheControl: &openrouter.ChatCacheControl{Type: "ephemeral"},
			}},
		})
	} else {
		messages = append(messages, openrouter.ChatMessage{Role: "system", Content: request.System})
	}
	messages = append(messages, openrouter.ChatMessage{
		Role:    "user",
		Content: openrouter.ComposeUserMessage(request.User, request.UntrustedContent),
	})
	return messages
}

// replayBuildRequestBody mirrors the client's request-body builder: model,
// messages, the temperature precedence (override → binding → default 0),
// optional max_tokens and the native response_format.
func replayBuildRequestBody(
	request openrouter.CompletionRequest,
	binding *openrouter.ResolvedRoleBinding,
	messages []openrouter.ChatMessage,
) map[string]any {
	temperature := openrouter.DefaultTemperature
	if request.TemperatureOverride != nil {
		temperature = *request.TemperatureOverride
	} else if binding.Params != nil && binding.Params.Temperature != nil {
		temperature = *binding.Params.Temperature
	}
	body := map[string]any{
		"model":       binding.Primary,
		"messages":    messages,
		"temperature": temperature,
	}
	if binding.Params != nil && binding.Params.MaxTokens != nil {
		body["max_tokens"] = *binding.Params.MaxTokens
	}
	if request.JSONSchema != nil {
		body["response_format"] = openrouter.BuildNativeResponseFormat(request.JSONSchema, request.SchemaName)
	}
	return body
}

func replayCorrectiveMessage(schemaName string, issues []string) string {
	name := schemaName
	if name == "" {
		name = "response"
	}
	return strings.Join([]string{
		fmt.Sprintf("%s schema: '%s'.", openrouter.CorrectiveRetryInstructionPrefix, name),
		fmt.Sprintf("Violations: %s", strings.Join(issues, " | ")),
		"Return ONLY the corrected JSON value that strictly conforms to the schema. No prose, no markdown fences, no extra keys.",
	}, "\n")
}

// replayExchangeRequestBody renders one recorded exchange's request body (the
// Go form of the TS requestBody helper).
func replayRequestBody(exchange *replayExchange) map[string]any {
	if exchange == nil {
		return map[string]any{}
	}
	return exchange.body
}

// replayStringSlice reads a JSON-Schema `required`-style value ([]string or
// []any) as []string.
func replayStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if key, ok := entry.(string); ok {
				out = append(out, key)
			}
		}
		return out
	default:
		return nil
	}
}

func replayJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}

func replayBoolPtr(value bool) *bool { return &value }

/* ----------------------------------------------------------------- fixtures */

const decisionBodySchemaName = "docsync_decision_body_v1"

var decisionEngineWorkflow = openrouter.CompiledPolicyWorkflow{
	Policy: openrouter.CompiledPolicy{PolicyVersion: 17},
	Questions: []openrouter.CompiledQuestion{
		{Key: "public_behavior_changed", Type: "boolean", Prompt: "Did the change alter runtime behavior visible to users of this repository?"},
		{Key: "api_contract_changed", Type: "boolean", Prompt: "Did the change alter the public contract (exported API surface)?"},
		{Key: "affected_doc_count", Type: "score", Prompt: "How many known documentation candidates does this change affect?", Scoring: &openrouter.CompiledQuestionRange{Min: 0, Max: 50}},
		{Key: "documentation_impact", Type: "choice", Prompt: "What is the documentation impact of this change?", Choices: []string{"none", "possible", "definite", "unknown"}},
	},
	PolicyHash:            "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	DecisionSchemaVersion: "docsync.decision/v1",
}

func decisionEngineChange() openrouter.DecisionChangeRefs {
	return openrouter.DecisionChangeRefs{
		Commit:            "81af92e",
		DiffRef:           "git:81af92e",
		ChangeAnalysisRef: "analysis/01J9ZK6X8A",
	}
}

func decisionEngineAnalysisContext() openrouter.DecisionAnalysisContext {
	return openrouter.DecisionAnalysisContext{
		ChangeKind:          "behavioral",
		DocumentationImpact: "possible",
		Risk:                "medium",
		Confidence:          0.61,
		Answers: map[string]openrouter.DecisionAnswer{
			"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.92},
			"api_contract_changed":    {Value: json.RawMessage("false"), Probability: 0.88},
			"affected_doc_count":      {Value: json.RawMessage("2"), Probability: 0.79},
			"documentation_impact":    {Value: json.RawMessage(`"possible"`), Probability: 0.87},
		},
		Candidates: []string{"docs/payments.md", "docs/api.md"},
		Evidence: []schemas.Evidence{{
			Kind: "symbol_diff",
			Ref:  "src/payments/refund.ts:42",
			Note: "idempotencyKey became optional",
		}},
	}
}

func decisionEngineInput() openrouter.DecisionEngineInput {
	return openrouter.DecisionEngineInput{
		AnalysisContext:  decisionEngineAnalysisContext(),
		Change:           decisionEngineChange(),
		UntrustedContent: []string{"untrusted repository content"},
	}
}

// decisionEngineBindings builds the models-config view the client seam
// resolves: the decision-engine role bound to the primary model with the given
// OD-R41 structured_output param ("" → no params, the native default).
func decisionEngineBindings(structuredOutput string) map[string]*openrouter.ResolvedRoleBinding {
	params := &openrouter.ResolvedRoleParams{}
	if structuredOutput != "" {
		params.StructuredOutput = structuredOutput
	}
	return map[string]*openrouter.ResolvedRoleBinding{
		"decision-engine": {Primary: replayDefaultModel, Fallbacks: []string{}, Params: params},
	}
}

func decisionBodyOf(decision *schemas.Decision) openrouter.DecisionBody {
	return openrouter.DecisionBody{
		ChangeKind:          decision.ChangeKind,
		DocumentationImpact: decision.DocumentationImpact,
		Risk:                decision.Risk,
		Confidence:          decision.Confidence,
		Answers:             decision.Answers,
		Candidates:          decision.Candidates,
		Action:              decision.Action,
		Abstain:             decision.Abstain,
		AbstainReason:       decision.AbstainReason,
		Evidence:            decision.Evidence,
	}
}

func decisionBodyJSON(t *testing.T, decision *schemas.Decision) string {
	t.Helper()
	body := decisionBodyOf(decision)
	data, err := json.Marshal(body)
	require.NoError(t, err)
	return string(data)
}

// replayScripts turns raw model texts into recorded scripts.
func replayScripts(responses ...string) []replayScript {
	scripts := make([]replayScript, 0, len(responses))
	for _, response := range responses {
		scripts = append(scripts, replayScript{content: response})
	}
	return scripts
}

// newDecisionEngineHarness wires the adapter over one fresh recorded double
// whose router dispatches by request identity.
func newDecisionEngineHarness(
	t *testing.T,
	structuredOutput string,
	router replayRouter,
) (openrouter.DecisionEngineRole, *replayClient) {
	t.Helper()
	client := newReplayClient(decisionEngineBindings(structuredOutput), router)
	role, err := openrouter.CreateDecisionEngineAdapter(openrouter.DecisionEngineAdapterOptions{
		Client:   client,
		Compiled: decisionEngineWorkflow,
	})
	require.NoError(t, err)
	return role, client
}

// decisionBodyRouter routes every request identity to the given recorded
// bodies (each test drives one task shape).
func decisionBodyRouter(bodies ...string) replayRouter {
	return func(role string, request openrouter.CompletionRequest) []replayScript {
		return replayScripts(bodies...)
	}
}

/* -------------------------------------------------------------------- tests */

func TestDecisionEngineHappyPath(t *testing.T) {
	role, _ := newDecisionEngineHarness(t, "", decisionBodyRouter(
		decisionBodyJSON(t, &schemas.DecisionExample)))
	result, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.DecisionEngineKindDecision, result.Kind)
	decision := result.Decision
	require.NotNil(t, decision)

	// Identity fields pass through unchanged (input → output).
	require.Equal(t, decisionEngineChange().Commit, decision.Change.Commit)
	require.Equal(t, decisionEngineChange().DiffRef, decision.Change.DiffRef)
	require.Equal(t, decisionEngineChange().ChangeAnalysisRef, decision.Change.ChangeAnalysisRef)
	require.Equal(t, decisionEngineWorkflow.Policy.PolicyVersion, decision.PolicyVersion)
	require.Equal(t, schemas.DecisionSchemaID, decision.Schema)

	// Decision-level fields pass through from the example.
	require.Equal(t, schemas.DecisionExample.Action, decision.Action)
	require.Equal(t, schemas.DecisionExample.Confidence, decision.Confidence)
	require.Equal(t, schemas.DecisionExample.Candidates, decision.Candidates)
	require.False(t, decision.Abstain)
	require.Nil(t, decision.AbstainReason)

	// Deterministic decision_id (same change + same policy → same id).
	require.Regexp(t, regexp.MustCompile(`^d_[0-9a-f]{16}$`), decision.DecisionID)

	// Trace identity: ACTUALLY ROUTED model from telemetry (V-P2-11), the
	// compiled policy hash, and the adapter-measured latency.
	require.Equal(t, result.Telemetry.RoutedModel, decision.Trace.Model)
	require.Equal(t, replayDefaultModel, decision.Trace.Model)
	require.Equal(t, decisionEngineWorkflow.PolicyHash, decision.Trace.PolicyHash)
	require.Equal(t, decision.Trace.LatencyMS, result.Telemetry.LatencyMS)
	require.NotNil(t, result.Telemetry.PolicyVersion)
	require.Equal(t, decisionEngineWorkflow.Policy.PolicyVersion, *result.Telemetry.PolicyVersion)

	// The assembled decision parses through the registered decision schema
	// (canonical JSON unchanged — the schema-validated Decision round-trip).
	canonical, err := json.Marshal(decision)
	require.NoError(t, err)
	_, err = schemas.ParseDecision(canonical)
	require.NoError(t, err)
}

func TestDecisionIDDeterministic(t *testing.T) {
	example := decisionBodyJSON(t, &schemas.DecisionExample)
	role, _ := newDecisionEngineHarness(t, "", func(role string, request openrouter.CompletionRequest) []replayScript {
		// Dispatch by request identity: the different-commit task gets its
		// own recorded body.
		if strings.Contains(request.User, "differentcommit") {
			return replayScripts(example)
		}
		return replayScripts(example, example)
	})

	first, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.DecisionEngineKindDecision, first.Kind)
	second, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.DecisionEngineKindDecision, second.Kind)
	require.Equal(t, first.Decision.DecisionID, second.Decision.DecisionID)

	third, err := role.Decide(openrouter.DecisionEngineInput{
		AnalysisContext:  decisionEngineAnalysisContext(),
		Change:           openrouter.DecisionChangeRefs{Commit: "differentcommit", DiffRef: decisionEngineChange().DiffRef, ChangeAnalysisRef: decisionEngineChange().ChangeAnalysisRef},
		UntrustedContent: decisionEngineInput().UntrustedContent,
	})
	require.NoError(t, err)
	require.Equal(t, openrouter.DecisionEngineKindDecision, third.Kind)
	require.NotEqual(t, first.Decision.DecisionID, third.Decision.DecisionID)
}

func TestAbstainShapedBodyPassthrough(t *testing.T) {
	role, _ := newDecisionEngineHarness(t, "", decisionBodyRouter(
		decisionBodyJSON(t, &schemas.DecisionAbstainExample)))
	result, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.DecisionEngineKindDecision, result.Kind)

	decision := result.Decision
	require.NotNil(t, decision)
	require.True(t, decision.Abstain)
	require.NotNil(t, decision.AbstainReason)
	require.Equal(t, schemas.AbstainReasonInsufficientConfidence, *decision.AbstainReason)

	// Abstain-shaped decisions stay schema-valid and canonically unchanged.
	canonical, err := json.Marshal(decision)
	require.NoError(t, err)
	_, err = schemas.ParseDecision(canonical)
	require.NoError(t, err)
}

func TestSchemaViolationExhaustsBoundedRetry(t *testing.T) {
	role, client := newDecisionEngineHarness(t, "", decisionBodyRouter(
		"this is not json at all",
		`{"change_kind": "WRONG"}`,
		`{"abstain": true, "abstain_reason": null}`,
	))
	result, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.DecisionEngineKindAbstain, result.Kind)
	require.Nil(t, result.Decision)
	require.NotNil(t, result.Reason)
	require.Equal(t, schemas.AbstainReasonSchemaViolation, *result.Reason)

	// Bounded: 1 initial + 2 corrective retries = 3 exchanges, no fabrication.
	require.Len(t, client.exchanges, replayMaxAttempts)
}

func TestPromptsCarryPolicyContractAndContainment(t *testing.T) {
	role, client := newDecisionEngineHarness(t, "", decisionBodyRouter(
		decisionBodyJSON(t, &schemas.DecisionExample)))
	_, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)

	require.NotEmpty(t, client.exchanges)
	body := replayRequestBody(&client.exchanges[0])
	messages, ok := body["messages"].([]openrouter.ChatMessage)
	require.True(t, ok, "request body carries a messages array")
	require.GreaterOrEqual(t, len(messages), 2)

	// cachePrefix defaults true for the adapter → the system message content
	// is an array of parts (the provider prompt-cache hint).
	systemParts, ok := messages[0].Content.([]openrouter.ChatTextPart)
	require.True(t, ok, "cachePrefix defaults true for the adapter (system is an array of parts)")
	require.Len(t, systemParts, 1)
	require.NotNil(t, systemParts[0].CacheControl)
	assert.Equal(t, "ephemeral", systemParts[0].CacheControl.Type)
	// The raw prompt text (the emitted prompt bytes — no JSON escaping).
	systemText := systemParts[0].Text
	assert.Contains(t, systemText, "decision-engine")
	assert.Contains(t, systemText, `{"value": <JSON value>, "probability": <number 0..1>}`)
	assert.Contains(t, systemText, "P2-T13")
	questionsJSON, err := json.Marshal(decisionEngineWorkflow.Questions)
	require.NoError(t, err)
	assert.Contains(t, systemText, string(questionsJSON[:40]))

	userText, _ := messages[1].Content.(string)
	assert.Contains(t, userText, "Deterministic analysis context")
	assert.Contains(t, userText, `<untrusted-repository-content id="1">`)
}

func TestNativeResponseFormatDefaultsForDecisionEngine(t *testing.T) {
	role, client := newDecisionEngineHarness(t, "", decisionBodyRouter(
		decisionBodyJSON(t, &schemas.DecisionExample)))
	_, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)

	require.NotEmpty(t, client.exchanges)
	body := replayRequestBody(&client.exchanges[0])
	format, ok := body["response_format"].(openrouter.NativeResponseFormat)
	require.True(t, ok, "nativeness is the default — response_format is present")
	assert.Equal(t, "json_schema", format.Type)
	assert.Equal(t, decisionBodySchemaName, format.JSONSchema.Name)
	assert.Equal(t, true, format.JSONSchema.Strict)

	schema, ok := format.JSONSchema.Schema.(map[string]any)
	require.True(t, ok)
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	// The ADAPTER-owned identity fields are projected out: the model proposes
	// only the 10-field DecisionBody contract.
	for _, key := range []string{"schema", "decision_id", "policy_version", "change", "trace"} {
		assert.NotContains(t, properties, key)
		assert.NotContains(t, replayStringSlice(schema["required"]), key)
	}
	// The body contract is present with its required fields.
	assert.Contains(t, properties, "change_kind")
	assert.Contains(t, properties, "documentation_impact")
	assert.Contains(t, properties, "answers")
	assert.Contains(t, replayStringSlice(schema["required"]), "change_kind")
	assert.Equal(t, false, schema["additionalProperties"])
}

func TestPromptModeSendsNoNativeConstraint(t *testing.T) {
	role, client := newDecisionEngineHarness(t, "prompt", decisionBodyRouter(
		decisionBodyJSON(t, &schemas.DecisionExample)))
	result, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.DecisionEngineKindDecision, result.Kind)

	require.NotEmpty(t, client.exchanges)
	body := replayRequestBody(&client.exchanges[0])
	assert.NotContains(t, body, "response_format")
	// Validation is identical: the prompt-mode body still parses through the
	// same authoritative schema.
	require.NotNil(t, result.Decision)
	require.Equal(t, schemas.DecisionSchemaID, result.Decision.Schema)
}

func TestUnboundRoleFailsClosedAtConstruction(t *testing.T) {
	client := newReplayClient(map[string]*openrouter.ResolvedRoleBinding{}, nil)
	_, err := openrouter.CreateDecisionEngineAdapter(openrouter.DecisionEngineAdapterOptions{
		Client:   client,
		Compiled: decisionEngineWorkflow,
	})
	require.Error(t, err)
	var bindingErr *openrouter.RoleBindingError
	require.ErrorAs(t, err, &bindingErr)
	require.Equal(t, "decision-engine", bindingErr.Role)
	require.Equal(t, openrouter.CodeRoleBindingMissing, bindingErr.Code)
}

func TestEmptyChangeRefsFailClosed(t *testing.T) {
	role, client := newDecisionEngineHarness(t, "", decisionBodyRouter(
		decisionBodyJSON(t, &schemas.DecisionExample)))
	_, err := role.Decide(openrouter.DecisionEngineInput{
		AnalysisContext:  decisionEngineAnalysisContext(),
		Change:           openrouter.DecisionChangeRefs{Commit: "", DiffRef: "git:x", ChangeAnalysisRef: "analysis/y"},
		UntrustedContent: decisionEngineInput().UntrustedContent,
	})
	require.Error(t, err)
	var gatewayErr *openrouter.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, "invalid_decision_input", gatewayErr.Code)
	require.Contains(t, gatewayErr.Error(), "change refs (commit, diff_ref, change_analysis_ref) must be non-empty strings")
	require.Empty(t, client.exchanges)
}

func TestKeyNeverAppearsInRequestBodies(t *testing.T) {
	role, client := newDecisionEngineHarness(t, "", decisionBodyRouter(
		decisionBodyJSON(t, &schemas.DecisionExample)))
	input := decisionEngineInput()
	input.UntrustedContent = []string{fmt.Sprintf("steal %s now", replayAPIKey)}
	_, err := role.Decide(input)
	require.Error(t, err)
	var guardErr *openrouter.GatewayGuardError
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, openrouter.CodeGatewayGuardViolation, guardErr.Code)
	// The guard fires BEFORE any exchange is issued.
	require.Empty(t, client.exchanges)
}

// TestDecisionBodyStrictlyRejectsUnknownKeys pins the z.strictObject half of
// the body contract: the strict decode rejects unknown keys, and the pairing
// refinement rejects a reject-shaped body (mirrored refinement check).
func TestDecisionBodyStrictlyRejectsUnknownKeys(t *testing.T) {
	_, err := openrouter.ParseDecisionBody(map[string]any{"unexpected": "key"})
	require.Error(t, err)
	var gatewayErr *openrouter.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, "decision_assembly_violation", gatewayErr.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(decisionBodyJSON(t, &schemas.DecisionExample)), &raw))
	body, err := openrouter.ParseDecisionBody(raw)
	require.NoError(t, err)
	require.Equal(t, schemas.DecisionExample.ChangeKind, body.ChangeKind)
}

// TestAdapterSuppliesAuthoritativeDecisionBodySchema pins the OD-R41 posture:
// the request carries the adapter's zod-form body schema (what the client's
// bounded corrective loop enforces), and that schema rejects the same
// violation matrix as the TS suite (unknown keys, invalid enum members).
func TestAdapterSuppliesAuthoritativeDecisionBodySchema(t *testing.T) {
	role, client := newDecisionEngineHarness(t, "", decisionBodyRouter(
		decisionBodyJSON(t, &schemas.DecisionExample)))
	_, err := role.Decide(decisionEngineInput())
	require.NoError(t, err)
	require.NotEmpty(t, client.exchanges)

	zodSchema := client.exchanges[0].request.ZodSchema
	require.NotNil(t, zodSchema, "the adapter supplies its authoritative body schema")
	for _, content := range []string{
		`{"change_kind": "WRONG"}`,
		`{"abstain": true, "abstain_reason": null}`,
	} {
		extracted := openrouter.ExtractJsonCandidate(content)
		require.True(t, extracted.OK, "extraction for %q", content)
		assert.NotEmpty(t, zodSchema(extracted.Value), "violation issues for %q", content)
	}
	extracted := openrouter.ExtractJsonCandidate(decisionBodyJSON(t, &schemas.DecisionExample))
	require.True(t, extracted.OK)
	assert.Empty(t, zodSchema(extracted.Value), "the example body satisfies the adapter's schema")
}
