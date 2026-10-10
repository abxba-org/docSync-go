// Ported from packages/openrouter/src/adapters/judge.ts (301 lines).
//
// Tier-3 judge adapter (P4-T08; ARCH §4.8, ARCH §10.2, D13): the Tier-3 judge
// role. Like the decision engine, this adapter owns NO policy thresholds and
// NO persistence — it returns (verdict, reasons, questions) supplied by the
// model, typed by schemas (the registered `docsync.judge-verdict/v1` contract
// IS the judge body).
//
// Responsibilities (ARCH §4.8; D13):
//   - Build the Tier-3 request from a caller-supplied prompt (the verifier
//     owns the Tier-3 prompt — the adapter never re-states verifier language;
//     it transports the caller's prompt verbatim, exactly like the
//     decision-engine adapter's grounding rules).
//   - Consume the client's bounded corrective-retry loop (schema-validated
//     structured output; D10) and pass the typed result through.
//   - Never inspect policy thresholds — the verifier owns majority policy;
//     identity fields do NOT live in this body (the routed model, latency,
//     provider authentication and policy/run identity are recorded by the
//     verifier in the assembled report's judge block — routed_model is the
//     ACTUALLY ROUTED model, V-P2-11; the VERIFIER owns assembling the
//     report's judge block from body + telemetry and pinning
//     policy_version/run identity).
//   - Abstention is NEVER model-proposed: there is no abstain field in the
//     body (strict schema rejects extra keys). `kind: 'abstain'` is operational
//     degradation only — schema exhaustion in the client's bounded corrective
//     retry loop (`schema_violation`), which the client has already bounded;
//     the adapter never hand-patches, never fabricates a verdict.
//
// Temperature (ARCH §10.3): the binding params rule — the invocation's
// `temperatureOverride` passes through when the caller supplies one; when
// absent the adapter does NOT override and the client applies the role-bound
// `params.temperature` (else the deterministic default). No writer-style
// hard-coded 0 here: the judge follows its binding, mirroring the
// decision-engine adapter's exact passthrough behavior (pinned by tests).
//
// Timeout (mirror choice, D13 minor 3 provenance): the decision-engine
// adapter intentionally ships WITHOUT timeout machinery (ARCH §3.4 lists the
// writer-model timeout → finding; no decision-engine budget is pinned there),
// while the judge follows the patch-writer: the optional `timeoutMs` races the
// whole completion (model walk + bounded corrective retries included) against
// an adapter wall-clock budget and raises the SHARED tagged provenance marker
// (PatchWriterBudgetTimeoutError, the landed Go form of the TS
// AdapterBudgetTimeoutError from adapters/patch-writer.ts — the same class the
// patch-writer raises; role-agnostic provenance tag) — it is THROWN typed, NOT
// converted into an abstain result here: converting a budget timeout into
// `kind: 'abstain'` would silently collapse provenance the verifier is
// required to distinguish; the verifier maps thrown typed errors to abstain
// itself. Transport/total failures likewise rethrow typed (*TransportError /
// *TotalInferenceFailure / *GatewayError), mirroring the decision adapter's
// exact failure surface.
//
// Landed peer types consumed here (NOT redeclared): CompletionRequest /
// RoleCompletion / RoleTelemetry / OpenRouterClient (client.go),
// ResolvedRoleBinding / ResolvedRoleParams (models.go), RoleInvocation /
// JudgeResult / JudgeRole / JudgeVerdict / JudgeQuestionAnswer and the
// structured-output mode vocabulary (roles.go), the gateway error family
// (errors.go), the structured-schema seam (structured.go) and the shared
// budget-timeout provenance marker (adapter-patch-writer.go).
package openrouter

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// JUDGE_BODY_SCHEMA_NAME — name carried into corrective instructions /
// native response_format for the body (TS JUDGE_BODY_SCHEMA_NAME).
const JUDGE_BODY_SCHEMA_NAME = "docsync_judge_verdict_body_v1"

// judgeRole is the ARCH §10.2 role this adapter implements (the client's
// binding key).
const judgeRole = "judge"

// judgeBodySchemaID is the registered id of the judge body contract (OD-2):
// the `docsync.judge-verdict/v1` contract IS the judge body — no
// adapter-owned identity fields to strip, so NO projection applies.
const judgeBodySchemaID = "docsync.judge-verdict"

// TS GatewayError class codes carried verbatim as the observable error identity.
const (
	codeInvalidJudgeInput      = "invalid_judge_input"
	codeJudgeAssemblyViolation = "judge_assembly_violation"
)

// judgeBodyStructuredSchema is the Go form of the TS `zodSchema:
// JUDGE_BODY_SCHEMA` — the caller-supplied schema the client's bounded
// corrective loop enforces (OD-R41: the caller schema stays authoritative;
// JUDGE_BODY_SCHEMA is the registered contract verbatim — no identity fields
// to strip, the reject⇒reasons superRefine stays attached as-is).
var judgeBodyStructuredSchema = ParseStrictSchema(func(data []byte) (any, error) {
	return schemas.ParseJudgeVerdictBody(data)
})

// JudgeAdapterOptions configures the judge role (TS JudgeAdapterOptions).
type JudgeAdapterOptions struct {
	// Client is the model-access seam (the landed *OpenRouterClient).
	Client chatCompletionPort
	// SchemaName overrides the name carried into corrective instructions /
	// native response_format ("" → JUDGE_BODY_SCHEMA_NAME).
	SchemaName string
	// TimeoutMs is the adapter-level wall-clock budget for ONE judge call,
	// raced around the whole client completion. On expiry the shared budget
	// timeout marker is THROWN typed (never converted to an abstain result).
	TimeoutMs *int
}

// judgeAdapter is the concrete JudgeRole.
type judgeAdapter struct {
	client          chatCompletionPort
	judgeJSONSchema any // nil == prompt mode (explicit downgrade)
	schemaName      string
	timeoutMs       *int
}

// CreateJudgeAdapter creates the concrete JudgeRole. The only failure surface:
// transport errors return typed (caller handles D10); schema exhaustion and
// transport-level failures during corrective retries return `kind: 'abstain'`
// with reason `schema_violation`. There is no model-proposed abstention: the
// body contract has no abstain field (strict parsing rejects one).
//
// Construction resolves the role binding ONCE (fail-closed on an unbound
// role) and — when the binding's `structured_output` is 'native' (the OD-R41
// default) — derives the native JSON Schema from the registered judge-verdict
// export exactly once per adapter (pure, deterministic, cacheable; NO
// projection — the body contract carries no adapter-owned identity fields).
//
// Identity note (mirrors decision-engine input validation): the generic
// RoleInvocation seam IS the judge's real input (the verifier builds the
// Tier-3 prompt), so unlike the patch-writer there is no identity-required
// typed error — only the cheap fail-closed check that system/user prompts are
// non-empty before any request is issued.
func CreateJudgeAdapter(options JudgeAdapterOptions) (JudgeRole, error) {
	binding, err := options.Client.BindingFor(judgeRole)
	if err != nil {
		return nil, err
	}
	// OD-R41: native mode (default) sends the registered judge-verdict JSON
	// Schema as the provider constraint (verbatim — no projection, no
	// adapter-owned identity fields to strip); prompt mode (explicit binding
	// downgrade) sends none. The body schema is ALWAYS authoritative.
	var judgeJSONSchema any
	if structuredOutputOf(binding) == StructuredOutputNative {
		exported, err := schemas.ExportJsonSchema(judgeBodySchemaID)
		if err != nil {
			return nil, err
		}
		judgeJSONSchema = exported
	}
	schemaName := options.SchemaName
	if schemaName == "" {
		schemaName = JUDGE_BODY_SCHEMA_NAME
	}
	return &judgeAdapter{
		client:          options.Client,
		judgeJSONSchema: judgeJSONSchema,
		schemaName:      schemaName,
		timeoutMs:       options.TimeoutMs,
	}, nil
}

// invalidInvocation is the fail-closed invocation validation (before any
// request is issued; TS invalidInvocation).
func invalidInvocation(message string) error {
	return NewGatewayError(codeInvalidJudgeInput, message)
}

// Judge judges one Tier-3 invocation.
func (a *judgeAdapter) Judge(invocation RoleInvocation) (JudgeResult, error) {
	if invocation.System == "" {
		return JudgeResult{}, invalidInvocation("system must be a non-empty string (the Tier-3 judge prompt)")
	}
	if invocation.User == "" {
		return JudgeResult{}, invalidInvocation("user must be a non-empty string (the judge task)")
	}
	request := CompletionRequest{
		System:           invocation.System,
		User:             invocation.User,
		UntrustedContent: invocation.UntrustedContent,
		ZodSchema:        judgeBodyStructuredSchema,
		SchemaName:       a.schemaName,
		// ARCH §10.3: follow the binding — pass the caller's override through
		// when supplied; never force a value (mirror of decision-engine).
		TemperatureOverride: invocation.TemperatureOverride,
		CachePrefix:         cachePrefixOrTrue(invocation.CachePrefix),
	}
	// OD-R41: native mode (default) sends the registered judge-verdict JSON
	// Schema as the provider constraint (verbatim — no projection, no
	// adapter-owned identity fields to strip); prompt mode (explicit binding
	// downgrade) sends none — the key must be ABSENT, never a nil schema. The
	// body schema is ALWAYS authoritative.
	if a.judgeJSONSchema != nil {
		request.JSONSchema = a.judgeJSONSchema
	}
	if a.timeoutMs == nil {
		// Decision-adapter precedent: no adapter-level timeout machinery —
		// typed transport/total failures propagate (the verifier maps them).
		completion, err := a.client.Complete(judgeRole, request)
		if err != nil {
			return JudgeResult{}, err
		}
		return resultFromCompletion(completion)
	}
	completion, err := raceWithinBudget(a.client, judgeRole, request, *a.timeoutMs)
	if err != nil {
		return JudgeResult{}, err
	}
	return resultFromCompletion(completion)
}

// raceWithinBudget races the client completion against the adapter wall-clock
// budget — mirror of the patch-writer adapter's raceWithinBudget (completion
// handlers ALWAYS attached so a late rejection can never become unhandled;
// timer cleared when the completion settles first).
func raceWithinBudget(
	client chatCompletionPort,
	role string,
	request CompletionRequest,
	budgetMs int,
) (RoleCompletion, error) {
	done := make(chan judgeRaceOutcome, 1)
	go func() {
		completion, err := client.Complete(role, request)
		done <- judgeRaceOutcome{completion: completion, err: err}
	}()
	timer := time.NewTimer(time.Duration(budgetMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case outcome := <-done:
		return outcome.completion, outcome.err
	case <-timer.C:
		return RoleCompletion{}, newAdapterBudgetTimeoutError(budgetMs)
	}
}

// judgeRaceOutcome carries one settled completion out of the budget race
// (buffered so a late settlement is never a goroutine leak of an unhandled
// value).
type judgeRaceOutcome struct {
	completion RoleCompletion
	err        error
}

// newAdapterBudgetTimeoutError raises the SHARED adapter-budget timeout
// provenance marker (TS AdapterBudgetTimeoutError from
// adapters/patch-writer.ts:448 — role-agnostic; the judge raises the SAME
// marker the patch-writer raises). D13 minor 3: classification treats this
// error as timeout BY PROVENANCE (errors.As on the marker type), never by
// message inspection.
func newAdapterBudgetTimeoutError(budgetMs int) error {
	return &PatchWriterBudgetTimeoutError{BudgetMS: budgetMs}
}

// resultFromCompletion maps one bounded completion onto a JudgeResult.
func resultFromCompletion(completion RoleCompletion) (JudgeResult, error) {
	if completion.Kind == RoleCompletionKindSchemaAbstain {
		// Operational degradation (D10): the bounded corrective-retry loop
		// already ran in the client — never hand-patched, never fabricated
		// into a verdict.
		reason := schemas.AbstainReasonSchemaViolation
		return JudgeResult{
			Kind:      JudgeKindAbstain,
			Reason:    &reason,
			Telemetry: completion.Telemetry,
		}, nil
	}
	if completion.Structured == nil {
		return JudgeResult{}, NewGatewayError(
			codeJudgeAssemblyViolation,
			"judge completion carried no structured value — internal invariant violation",
		)
	}
	// OD-R41 posture: strict re-validation of the parsed body (the provider
	// native constraint is never trusted alone) — re-parse the assembled
	// block through the REGISTERED parser (pure, fails only on schema
	// refusal, e.g. reject without reasons): a model-authored OE/REJECT pair
	// MUST surface verbatim as the verdict (V-P4-8), never coated with an
	// additional 'investigate'/'pass' answer record — the caller maps
	// verdicts to review/approval through policy, not through shape fixing.
	// A failure can only mean the gateway's schema layer was bypassed —
	// typed internal-invariant failure.
	wire, err := json.Marshal(completion.Structured)
	if err != nil {
		return JudgeResult{}, NewGatewayError(
			codeJudgeAssemblyViolation,
			"judge body failed JUDGE_VERDICT_SCHEMA re-parse — internal invariant violation",
		)
	}
	body, err := schemas.ParseJudgeVerdictBody(wire)
	if err != nil {
		detail := err.Error()
		var validation *schemas.ValidationError
		if errors.As(err, &validation) {
			detail = formatSchemaIssues(validation.Issues)
		}
		return JudgeResult{}, NewGatewayError(
			codeJudgeAssemblyViolation,
			"judge body failed JUDGE_VERDICT_SCHEMA re-parse — internal invariant violation: "+detail,
		)
	}
	return JudgeResult{
		Kind:    JudgeKindVerdict,
		Verdict: JudgeVerdict(body.Verdict),
		// Reasons pass through verbatim; absent (allowed for pass/review) → [].
		Reasons: judgeReasons(body.Reasons),
		// Typed-question answers survive to the result verbatim — the Tier-3
		// evidence the report's judge block consumes (ARCH §4.8 + §12.5).
		Questions: judgeQuestions(body.Questions),
		Telemetry: completion.Telemetry,
	}, nil
}

// judgeReasons mirrors `body.reasons ?? []`: an absent reasons array renders
// as an empty (non-nil) slice (unchanged semantics — nil only when the body
// carried a JSON null), and the reject⇒reasons pairing is the registered
// parser's refinement (V-P4-6: model-declared fact types are a closed set).
func judgeReasons(reasons []string) []string {
	if reasons == nil {
		return []string{}
	}
	return reasons
}

// judgeQuestions renders the schema-layer judge answers (docsync.judge-verdict
// value+probability pairs) as the role-layer JudgeQuestionAnswer map the
// caller consumes; absent stays nil (nothing fabricated).
func judgeQuestions(questions map[string]schemas.VerificationJudgeAnswer) map[string]JudgeQuestionAnswer {
	if questions == nil {
		return nil
	}
	out := make(map[string]JudgeQuestionAnswer, len(questions))
	for key, answer := range questions {
		out[key] = JudgeQuestionAnswer{Value: answer.Value, Probability: answer.Probability}
	}
	return out
}
