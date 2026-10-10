package openrouter

// Ported from packages/openrouter/src/routing/policy.ts (P12-T10; ARCH
// §10.1/§10.3; D9; R2) — the runtime half of the org routing-policy layer:
// the bucket → model mapping ("high-risk cases → stronger model", ARCH §10.1
// diagram) is DECLARATIVE, VERSIONED data — a policy change, not a code
// change (ARCH §10.3: "Routing policy ... is itself declarative and
// versioned").
//
// PROMOTION (mirrored from the TS side, P12-T20): the policy document schema,
// its per-bucket binding schema and the activation envelope are the REGISTERED
// `docsync.routing-policy/v1` artifacts in internal/schemas
// (routing-policy.go there). This module consumes them — exactly ONE
// definition of those shapes exists, in the registry. What stays here is the
// RUNTIME half:
//
//   - ParseRoutingPolicy validates an untrusted document fail-closed against
//     the registered schema and raises the typed RoutingPolicyError
//     (NewRoutingPolicyError — routing-errors.go) — a config error,
//     fail-closed before any request.
//   - Activation is a LOCAL state machine: `draft → evaluated → active`,
//     `retired` terminal (R2: model/routing swaps are evaluated like policy
//     changes). `draft → evaluated` REQUIRES a shadow/canary evaluation
//     report ref (V-P12-11); `evaluated → active` REQUIRES an explicit
//     application-owned actor — an evaluation report alone NEVER
//     auto-activates (§3 rule 7). The veto is TYPED
//     (NewRoutingPolicyActivationError), not a boolean.
//   - ResolveRoute resolves one bucket's binding onto the SAME
//     ResolvedRoleBinding shape the client already walks (models.go, the port
//     of src/models.ts), so routing selections compose with the existing
//     role-binding machinery without a second walk implementation.
//
// Fail-closed everywhere: every bucket is bound explicitly by the registered
// schema, so no default model is ever guessed.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// RoutingPolicySchemaID re-exports the registered schema id (TS
// ROUTING_POLICY_SCHEMA_ID re-export) so this module's public surface stays
// unchanged by the P12-T20 promotion.
const RoutingPolicySchemaID = schemas.RoutingPolicySchemaID

/* ------------------------------------------------------------------ helpers */

// jsonString renders a value exactly as the TS JSON.stringify did inside the
// ported messages (double quotes, JSON escaping). strconv.Quote is NOT used:
// it escapes non-ASCII characters differently from JSON.stringify.
func jsonString(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

// marshalSeamInput serializes a value the way the TS JSON.stringify did for
// prompt bytes (no HTML escaping of <, >, & — the TS default).
func marshalSeamInput(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// formatIssues mirrors the TS formatIssues helper: `path: message` joined by
// "; ", with `(root)` standing in for an empty path.
func formatIssues(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, renderIssue(issue))
	}
	return strings.Join(parts, "; ")
}

// issueStrings renders every issue as its own `path: message` string (the TS
// error's `issues` array).
func issueStrings(issues []schemas.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, renderIssue(issue))
	}
	return out
}

// renderIssue is one zod-style issue string (`(root): message` when the path
// is empty — the same rendering IssuesFromValidationError produces).
func renderIssue(issue schemas.Issue) string {
	path := "(root)"
	if len(issue.Path) > 0 {
		path = strings.Join(issue.Path, ".")
	}
	return path + ": " + issue.Message
}

// validationIssues extracts the issues from a parse/validation error (both the
// strict-decode and the Validate paths return *schemas.ValidationError).
func validationIssues(err error) []schemas.Issue {
	var validationErr *schemas.ValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Issues
	}
	return []schemas.Issue{{Message: err.Error()}}
}

/* ---------------------------------------------------------------- the policy */

// ParseRoutingPolicy validates an untrusted routing-policy document
// fail-closed (TS parseRoutingPolicy): unknown keys, missing buckets, empty
// model ids → typed RoutingPolicyError. The input may be the decoded struct
// or any JSON-shaped value (map/struct); it is strictly re-decoded so
// z.strictObject semantics hold either way.
func ParseRoutingPolicy(input any) (*schemas.RoutingPolicy, error) {
	if policy, ok := input.(*schemas.RoutingPolicy); ok {
		if issues := policy.Validate(); len(issues) > 0 {
			return nil, rejectedRoutingPolicy(issues)
		}
		return policy, nil
	}
	// Raw JSON input is decoded directly — re-marshaling bytes would wrap them
	// in a base64 string.
	switch typed := input.(type) {
	case []byte:
		return parseRoutingPolicyBytes(typed)
	case json.RawMessage:
		return parseRoutingPolicyBytes(typed)
	case string:
		return parseRoutingPolicyBytes([]byte(typed))
	}
	data, err := marshalSeamInput(input)
	if err != nil {
		return nil, NewRoutingPolicyError(
			"not a valid "+RoutingPolicySchemaID+" routing policy — rejected fail-closed: "+
				"(root): "+err.Error(), nil)
	}
	return parseRoutingPolicyBytes(data)
}

func parseRoutingPolicyBytes(data []byte) (*schemas.RoutingPolicy, error) {
	policy, err := schemas.ParseRoutingPolicy(data)
	if err != nil {
		return nil, rejectedRoutingPolicy(validationIssues(err))
	}
	return policy, nil
}

func rejectedRoutingPolicy(issues []schemas.Issue) error {
	return NewRoutingPolicyError(
		"not a valid "+RoutingPolicySchemaID+" routing policy — rejected fail-closed: "+
			formatIssues(issues),
		issueStrings(issues))
}

/* ------------------------------------------------------ activation lifecycle */

// ParseRoutingPolicyActivation validates the state invariants of an untrusted
// activation envelope (TS parseRoutingPolicyActivation) and returns the
// envelope. The registered envelope schema (internal/schemas) enforces the
// same state-conditional rules for persisted data; this runtime check
// preserves the TS parser's message text for vetoes.
func ParseRoutingPolicyActivation(input any) (*schemas.RoutingPolicyActivation, error) {
	activation, err := decodeRoutingPolicyActivation(input)
	if err != nil {
		return nil, err
	}
	if err := checkActivationInvariants(activation); err != nil {
		return nil, err
	}
	return activation, nil
}

func decodeRoutingPolicyActivation(input any) (*schemas.RoutingPolicyActivation, error) {
	if activation, ok := input.(*schemas.RoutingPolicyActivation); ok {
		return activation, nil
	}
	var data []byte
	switch typed := input.(type) {
	// Raw JSON input is decoded directly — re-marshaling bytes would wrap them
	// in a base64 string.
	case []byte:
		data = typed
	case json.RawMessage:
		data = typed
	case string:
		data = []byte(typed)
	default:
		serialized, err := marshalSeamInput(input)
		if err != nil {
			return nil, NewRoutingPolicyActivationError("(any)", "(any)",
				"routing-policy activation envelope is not serializable: "+err.Error())
		}
		data = serialized
	}
	var activation schemas.RoutingPolicyActivation
	if err := schemas.StrictDecode(data, &activation); err != nil {
		return nil, NewRoutingPolicyActivationError("(any)", "(any)",
			"routing-policy activation envelope is not decodable (unknown keys are rejected fail-closed): "+
				err.Error())
	}
	return &activation, nil
}

// checkActivationInvariants applies the TS state rules in the TS order (first
// offending field vetoes, exactly like the TS throw-on-first pattern).
func checkActivationInvariants(activation *schemas.RoutingPolicyActivation) error {
	state := string(activation.State)
	if !schemas.IsValidRoutingPolicyActivationState(state) {
		return NewRoutingPolicyActivationError(state, "(any)",
			"unknown routing-policy activation state: "+state)
	}
	if state == "draft" {
		if err := forbidActivationField(state, "evaluationReportRef", activation.EvaluationReportRef != nil); err != nil {
			return err
		}
		if err := forbidActivationField(state, "activatedBy", activation.ActivatedBy != nil); err != nil {
			return err
		}
		if err := forbidActivationField(state, "activatedAt", activation.ActivatedAt != nil); err != nil {
			return err
		}
		return forbidActivationField(state, "retiredAt", activation.RetiredAt != nil)
	}
	if err := requireActivationField(state, "evaluationReportRef", activation.EvaluationReportRef != nil); err != nil {
		return err
	}
	if state == "active" || state == "retired" {
		if err := requireActivationField(state, "activatedBy", activation.ActivatedBy != nil); err != nil {
			return err
		}
		return requireActivationField(state, "activatedAt", activation.ActivatedAt != nil)
	}
	if err := forbidActivationField(state, "activatedBy", activation.ActivatedBy != nil); err != nil {
		return err
	}
	if err := forbidActivationField(state, "activatedAt", activation.ActivatedAt != nil); err != nil {
		return err
	}
	if state == "retired" {
		return requireActivationField(state, "retiredAt", activation.RetiredAt != nil)
	}
	return forbidActivationField(state, "retiredAt", activation.RetiredAt != nil)
}

func requireActivationField(state, field string, present bool) error {
	if present {
		return nil
	}
	return NewRoutingPolicyActivationError(state, state,
		"routing-policy activation in state '"+state+"' is missing the required '"+field+"' field")
}

func forbidActivationField(state, field string, present bool) error {
	if !present {
		return nil
	}
	return NewRoutingPolicyActivationError(state, state,
		"routing-policy activation in state '"+state+"' must not carry '"+field+"'")
}

// DraftRoutingPolicyActivation starts a lifecycle: a fresh policy document is
// DRAFT (unevaluated) — TS draftRoutingPolicyActivation.
func DraftRoutingPolicyActivation(policy *schemas.RoutingPolicy) *schemas.RoutingPolicyActivation {
	return &schemas.RoutingPolicyActivation{Policy: *policy, State: schemas.RoutingPolicyDraft}
}

// MarkEvaluated performs `draft → evaluated` — attaches the shadow/canary
// evaluation report ref (V-P12-11: promotion REQUIRES the report BEFORE the
// policy can even be considered for activation). Any other source state is a
// typed veto. The input envelope is never mutated.
func MarkEvaluated(
	activation *schemas.RoutingPolicyActivation,
	evaluationReportRef string,
) (*schemas.RoutingPolicyActivation, error) {
	if activation.State != schemas.RoutingPolicyDraft {
		return nil, NewRoutingPolicyActivationError(string(activation.State), "evaluated",
			fmt.Sprintf("routing policy v%d cannot be marked evaluated from state '%s'",
				activation.Policy.Version, activation.State))
	}
	if evaluationReportRef == "" {
		return nil, NewRoutingPolicyActivationError("draft", "evaluated",
			"markEvaluated requires a non-empty shadow/canary evaluation report ref")
	}
	next := *activation
	next.State = schemas.RoutingPolicyEvaluated
	ref := evaluationReportRef
	next.EvaluationReportRef = &ref
	return &next, nil
}

// ActivateRoutingPolicy performs `evaluated → active` — the human/application
// act (§3 rule 7). VETOED (typed) from `draft`: an un-evaluated policy CANNOT
// become active. There is NO automatic path from an evaluation report to
// `active`: this function must be called explicitly with an application-owned
// actor, which is exactly the "report alone does not auto-activate"
// guarantee. The input envelope is never mutated.
func ActivateRoutingPolicy(
	activation *schemas.RoutingPolicyActivation,
	actor string,
	now string,
) (*schemas.RoutingPolicyActivation, error) {
	state := string(activation.State)
	if activation.State == schemas.RoutingPolicyDraft {
		return nil, NewRoutingPolicyActivationError(state, "active",
			fmt.Sprintf("VETO (V-P12-11): routing policy v%d has no shadow/canary evaluation report — "+
				"an un-evaluated policy CANNOT become active (R2: evaluated like policy changes)",
				activation.Policy.Version))
	}
	if activation.State != schemas.RoutingPolicyEvaluated {
		return nil, NewRoutingPolicyActivationError(state, "active",
			fmt.Sprintf("routing policy v%d cannot be activated from state '%s'",
				activation.Policy.Version, activation.State))
	}
	if actor == "" || now == "" {
		return nil, NewRoutingPolicyActivationError(state, "active",
			"activateRoutingPolicy requires an application-owned actor and an activation instant")
	}
	next := *activation
	next.State = schemas.RoutingPolicyActive
	next.ActivatedBy = &actor
	next.ActivatedAt = &now
	return &next, nil
}

// RetireRoutingPolicy performs `active → retired` (terminal). Only an ACTIVE
// policy can be retired: the registered envelope requires the retired record
// to carry the application-owned activation actor + instant, and an
// evaluated-but-never-active policy has NEITHER to name (§3 rule 7) —
// retiring one would produce a record the registered schema rejects. Such a
// policy is therefore simply DROPPED, never retired — a typed VETO. The input
// envelope is never mutated.
func RetireRoutingPolicy(
	activation *schemas.RoutingPolicyActivation,
	now string,
) (*schemas.RoutingPolicyActivation, error) {
	state := string(activation.State)
	if activation.State == schemas.RoutingPolicyEvaluated {
		return nil, NewRoutingPolicyActivationError(state, "retired",
			fmt.Sprintf("VETO: routing policy v%d cannot be retired from state 'evaluated' — "+
				"an evaluated-but-never-active policy is DROPPED, never retired (a retired envelope requires the "+
				"application-owned activation actor + activation instant, which a never-activated policy cannot have — §3 rule 7)",
				activation.Policy.Version))
	}
	if activation.State != schemas.RoutingPolicyActive {
		return nil, NewRoutingPolicyActivationError(state, "retired",
			fmt.Sprintf("routing policy v%d cannot be retired from state '%s'",
				activation.Policy.Version, activation.State))
	}
	next := *activation
	next.State = schemas.RoutingPolicyRetired
	next.RetiredAt = &now
	return &next, nil
}

/* --------------------------------------------------------------- resolution */

// RoutingRouteContext labels the route for provenance (ARCH §10.6).
type RoutingRouteContext struct {
	// Role is the logical role this route serves (e.g. `patch-writer`).
	Role string
}

// ResolvedRoute is the resolved route: the SAME ResolvedRoleBinding shape
// resolveRoleBinding returns (models.go — the port of src/models.ts), plus the
// routing lineage (bucket, policy version, source ref) provenance needs to
// name the ACTUAL routed model under routing (ARCH §10.6).
type ResolvedRoute struct {
	Bucket        schemas.RiskBucket
	Role          string
	Binding       ResolvedRoleBinding
	PolicyVersion int
	SourceRef     string
}

// ResolveRoute resolves the binding for one risk bucket (TS resolveRoute),
// fail-closed: unknown bucket → typed RoutingPolicyError; the policy
// guarantees every bucket is explicitly bound, so no default is ever guessed.
func ResolveRoute(
	policy *schemas.RoutingPolicy,
	bucket string,
	context RoutingRouteContext,
) (*ResolvedRoute, error) {
	if !schemas.IsValidRiskBucket(bucket) {
		return nil, NewRoutingPolicyError(
			fmt.Sprintf("resolveRoute: unknown risk bucket %s (known buckets: %s)",
				jsonString(bucket), joinRiskBuckets()), nil)
	}
	if context.Role == "" {
		return nil, NewRoutingPolicyError("resolveRoute: context.role must be a non-empty string", nil)
	}
	var binding schemas.RoutingPolicyBinding
	switch schemas.RiskBucket(bucket) {
	case schemas.RiskBucketLow:
		binding = policy.Buckets.Low
	case schemas.RiskBucketMedium:
		binding = policy.Buckets.Medium
	case schemas.RiskBucketHigh:
		binding = policy.Buckets.High
	}
	return &ResolvedRoute{
		Bucket:        schemas.RiskBucket(bucket),
		Role:          context.Role,
		Binding:       resolvedBindingFromPolicy(binding),
		PolicyVersion: policy.Version,
		SourceRef:     policy.SourceRef,
	}, nil
}

// joinRiskBuckets renders RISK_BUCKETS the way the TS join(' | ') did.
func joinRiskBuckets() string {
	names := make([]string, 0, len(schemas.RiskBuckets))
	for _, bucket := range schemas.RiskBuckets {
		names = append(names, string(bucket))
	}
	return strings.Join(names, " | ")
}

// resolvedBindingFromPolicy maps the registered policy binding onto the
// client-walk shape (models.go's ResolvedRoleBinding), applying the TS
// defaults: fallbacks ?? [] and provider_routing.provider.allow_fallbacks ??
// true. The routing-policy binding carries no instance constraint, so the
// verbatim `constraint` field stays absent.
func resolvedBindingFromPolicy(binding schemas.RoutingPolicyBinding) ResolvedRoleBinding {
	out := ResolvedRoleBinding{
		Primary:   binding.Primary,
		Fallbacks: append([]string{}, binding.Fallbacks...),
	}
	if binding.Params != nil {
		out.Params = &ResolvedRoleParams{
			Temperature: binding.Params.Temperature,
			MaxTokens:   binding.Params.MaxTokens,
		}
		if binding.Params.StructuredOutput != nil {
			mode := string(*binding.Params.StructuredOutput)
			out.Params.StructuredOutput = mode
		}
	}
	if binding.ProviderRouting != nil {
		allowFallbacks := true
		if binding.ProviderRouting.Provider.AllowFallbacks != nil {
			allowFallbacks = *binding.ProviderRouting.Provider.AllowFallbacks
		}
		out.ProviderRouting = &ResolvedProviderRouting{
			Order:          append([]string{}, binding.ProviderRouting.Provider.Order...),
			AllowFallbacks: allowFallbacks,
		}
	}
	return out
}
