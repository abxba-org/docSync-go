package schemas

// Ported from packages/schemas/src/v1/routing-policy.ts (P12-T20, promotion of
// the P12-T10 local schema into the registry; ARCH §10.1, §10.3; D9; R2): the
// bucket → model mapping ("high-risk cases → stronger model", ARCH §10.1
// diagram) is DECLARATIVE, VERSIONED data — a policy change, not a code
// change (ARCH §10.3: "Routing policy ... is itself declarative and
// versioned").
//
// Shape notes:
//   - Every bucket is bound EXPLICITLY (`low` | `medium` | `high`, closed —
//     ARCH §10.1) and the object is strict: unknown keys reject fail-closed,
//     and no default model is ever guessed (the router never invents a
//     binding it was not given).
//   - The per-bucket binding carries the SAME ARCH §10.3 fields the extended
//     models.yml binding uses (`params`, `provider_routing`) so routing
//     selections compose with the existing role-binding machinery.
//   - The document is IMMUTABLE data; promotion is state on top of it. The
//     activation envelope (RoutingPolicyActivation) records the lifecycle
//     state plus the shadow/canary evaluation report ref that
//     `draft → evaluated` REQUIRES (V-P12-11; R2: model/routing swaps are
//     evaluated like policy changes). The state MACHINE itself — the typed
//     vetoes, the application-owned actor (§3 rule 7), the "an evaluation
//     report alone NEVER auto-activates" rule — stays in the runtime layer;
//     this envelope is the DATA those transitions produce and consume.

// RoutingPolicySchemaID is the registry ID (ROUTING_POLICY_SCHEMA_ID).
const RoutingPolicySchemaID = "docsync.routing-policy/v1"

// RiskBucket is one risk bucket the router maps to model bindings
// (ARCH §10.1). CLOSED.
type RiskBucket string

// Risk buckets.
const (
	RiskBucketLow    RiskBucket = "low"
	RiskBucketMedium RiskBucket = "medium"
	RiskBucketHigh   RiskBucket = "high"
)

// RiskBuckets lists the closed risk-bucket vocabulary in canonical order
// (RISK_BUCKETS).
var RiskBuckets = []RiskBucket{RiskBucketLow, RiskBucketMedium, RiskBucketHigh}

// IsValidRiskBucket reports membership.
func IsValidRiskBucket(s string) bool {
	for _, v := range RiskBuckets {
		if string(v) == s {
			return true
		}
	}
	return false
}

// StructuredOutputMode is the OD-R41 structured-output mode for a binding
// (default 'native'): 'native' sends a `response_format` JSON-schema
// constraint AND keeps the caller's schema authoritative; 'prompt'
// downgrades to constrained prompting with strict re-validation only.
type StructuredOutputMode string

// Structured-output modes.
const (
	StructuredOutputNative StructuredOutputMode = "native"
	StructuredOutputPrompt StructuredOutputMode = "prompt"
)

// StructuredOutputModeValues lists the modes in canonical order.
var StructuredOutputModeValues = []StructuredOutputMode{StructuredOutputNative, StructuredOutputPrompt}

// RoutingPolicyParams carries the deterministic generation params a bucket
// binding may pin (ARCH §10.3 `params:`) — the same field set the gateway's
// extended models.yml binding accepts, including the OD-R41
// structured-output mode.
type RoutingPolicyParams struct {
	Temperature      *float64              `json:"temperature,omitempty"`
	MaxTokens        *int                  `json:"max_tokens,omitempty"`
	StructuredOutput *StructuredOutputMode `json:"structured_output,omitempty"`
}

// Validate checks the params block against ROUTING_POLICY_PARAMS_SCHEMA
// (strict).
func (p *RoutingPolicyParams) Validate() []Issue {
	var issues []Issue
	if p.Temperature != nil {
		if *p.Temperature < 0 {
			Collect(&issues, &Issue{Path: []string{"temperature"}, Message: "Number must be greater than or equal to 0"})
		}
		if *p.Temperature > 2 {
			Collect(&issues, &Issue{Path: []string{"temperature"}, Message: "Number must be less than or equal to 2"})
		}
	}
	if p.MaxTokens != nil && *p.MaxTokens <= 0 {
		Collect(&issues, &Issue{Path: []string{"max_tokens"}, Message: "Number must be greater than 0"})
	}
	if p.StructuredOutput != nil {
		Collect(&issues, CheckEnum(string(*p.StructuredOutput), stringsOf(StructuredOutputModeValues), []string{"structured_output"}))
	}
	return issues
}

// RoutingPolicyProviderPreference is the `provider:` block of the provider
// routing preferences (ARCH §10.3).
type RoutingPolicyProviderPreference struct {
	Order          []string `json:"order"`
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
}

// Validate checks the provider preference block (strict).
func (p *RoutingPolicyProviderPreference) Validate() []Issue {
	var issues []Issue
	if len(p.Order) < 1 {
		Collect(&issues, &Issue{Path: []string{"order"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i, o := range p.Order {
		Collect(&issues, RequiredString(o, 1, []string{"order", itoa(i)}))
	}
	return issues
}

// RoutingPolicyProviderRouting carries the provider routing preferences
// (ARCH §10.3 `provider_routing:`).
type RoutingPolicyProviderRouting struct {
	Provider RoutingPolicyProviderPreference `json:"provider"`
}

// Validate checks the provider routing block (strict).
func (r *RoutingPolicyProviderRouting) Validate() []Issue {
	var issues []Issue
	for _, iss := range r.Provider.Validate() {
		iss.Path = append([]string{"provider"}, iss.Path...)
		issues = append(issues, iss)
	}
	return issues
}

// RoutingPolicyBinding is the per-bucket model binding — same fields the
// models.yml extended binding uses (ROUTING_POLICY_BINDING_SCHEMA).
type RoutingPolicyBinding struct {
	Primary         string                        `json:"primary"`
	Fallbacks       []string                      `json:"fallbacks,omitempty"`
	Params          *RoutingPolicyParams          `json:"params,omitempty"`
	ProviderRouting *RoutingPolicyProviderRouting `json:"provider_routing,omitempty"`
}

// Validate checks the binding against ROUTING_POLICY_BINDING_SCHEMA (strict).
func (b *RoutingPolicyBinding) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(b.Primary, 1, []string{"primary"}))
	for i, f := range b.Fallbacks {
		Collect(&issues, RequiredString(f, 1, []string{"fallbacks", itoa(i)}))
	}
	if b.Params != nil {
		for _, iss := range b.Params.Validate() {
			iss.Path = append([]string{"params"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if b.ProviderRouting != nil {
		for _, iss := range b.ProviderRouting.Validate() {
			iss.Path = append([]string{"provider_routing"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// RoutingPolicyBuckets binds every risk bucket EXPLICITLY — no guessed
// default model (fail-closed: the router never invents a binding it was not
// given).
type RoutingPolicyBuckets struct {
	Low    RoutingPolicyBinding `json:"low"`
	Medium RoutingPolicyBinding `json:"medium"`
	High   RoutingPolicyBinding `json:"high"`
}

// RoutingPolicy is the versioned, reviewable declarative routing document
// (ROUTING_POLICY_SCHEMA).
type RoutingPolicy struct {
	Schema string `json:"schema"`
	// Version is the document version (positive integer).
	Version   int                  `json:"version"`
	SourceRef string               `json:"source_ref"`
	Buckets   RoutingPolicyBuckets `json:"buckets"`
}

// Validate checks the policy against ROUTING_POLICY_SCHEMA (strict).
func (p *RoutingPolicy) Validate() []Issue {
	var issues []Issue
	if p.Schema != RoutingPolicySchemaID {
		msg := `Invalid literal value, expected "` + RoutingPolicySchemaID + `"`
		if p.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	if p.Version <= 0 {
		Collect(&issues, &Issue{Path: []string{"version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, RequiredString(p.SourceRef, 1, []string{"source_ref"}))
	for _, iss := range p.Buckets.Low.Validate() {
		iss.Path = append([]string{"buckets", "low"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range p.Buckets.Medium.Validate() {
		iss.Path = append([]string{"buckets", "medium"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range p.Buckets.High.Validate() {
		iss.Path = append([]string{"buckets", "high"}, iss.Path...)
		issues = append(issues, iss)
	}
	return issues
}

// ParseRoutingPolicy strictly decodes and validates
// `docsync.routing-policy/v1` bytes (unknown JSON keys are rejected,
// mirroring z.strictObject).
func ParseRoutingPolicy(data []byte) (*RoutingPolicy, error) {
	var p RoutingPolicy
	if err := StrictDecode(data, &p); err != nil {
		return nil, err
	}
	if issues := p.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &p, nil
}

// RoutingPolicyActivationState is one activation state (P12-T10; R2 —
// evaluated like policy changes).
type RoutingPolicyActivationState string

// Activation states.
const (
	RoutingPolicyDraft     RoutingPolicyActivationState = "draft"
	RoutingPolicyEvaluated RoutingPolicyActivationState = "evaluated"
	RoutingPolicyActive    RoutingPolicyActivationState = "active"
	RoutingPolicyRetired   RoutingPolicyActivationState = "retired"
)

// RoutingPolicyStates lists the activation states in canonical order
// (ROUTING_POLICY_STATES).
var RoutingPolicyStates = []RoutingPolicyActivationState{
	RoutingPolicyDraft, RoutingPolicyEvaluated, RoutingPolicyActive, RoutingPolicyRetired,
}

// IsValidRoutingPolicyActivationState reports membership.
func IsValidRoutingPolicyActivationState(s string) bool {
	for _, v := range RoutingPolicyStates {
		if string(v) == s {
			return true
		}
	}
	return false
}

// RoutingPolicyActivation is the policy TOGETHER WITH its activation
// lifecycle state (V-P12-11). The document is immutable data; promotion is
// state on top of it:
//
//   - `draft` — no evaluation report yet; CANNOT become active (typed veto).
//   - `evaluated` — carries the shadow/canary evaluation report ref; still
//     NOT active: the report alone does not auto-activate.
//   - `active` — carries the report ref AND the application-owned actor +
//     activation instant of the human/application act that marked it
//     active.
//   - `retired` — terminal; carries the full prior lifecycle for audit.
//
// The state-conditional field requirements are enforced HERE (fail-closed,
// mirroring the TS superRefine) so the envelope cannot exist in a state that
// violates them; the typed transition vetoes (`draft → active` is
// impossible, no auto-activation) live in the runtime activation state
// machine.
type RoutingPolicyActivation struct {
	Policy              RoutingPolicy                `json:"policy"`
	State               RoutingPolicyActivationState `json:"state"`
	EvaluationReportRef *string                      `json:"evaluationReportRef,omitempty"`
	ActivatedBy         *string                      `json:"activatedBy,omitempty"`
	ActivatedAt         *string                      `json:"activatedAt,omitempty"`
	RetiredAt           *string                      `json:"retiredAt,omitempty"`
}

// Validate checks the envelope against ROUTING_POLICY_ACTIVATION_SCHEMA
// (strict) and applies the state-conditional field rules (the TS
// superRefine). Refinement rules run only when the field-level checks pass,
// mirroring zod, where refinements do not run on an aborted parse.
func (a *RoutingPolicyActivation) Validate() []Issue {
	var issues []Issue
	for _, iss := range a.Policy.Validate() {
		iss.Path = append([]string{"policy"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, CheckEnum(string(a.State), stringsOf(RoutingPolicyStates), []string{"state"}))
	Collect(&issues, activationStringField(a.EvaluationReportRef, []string{"evaluationReportRef"}))
	Collect(&issues, activationStringField(a.ActivatedBy, []string{"activatedBy"}))
	Collect(&issues, activationStringField(a.ActivatedAt, []string{"activatedAt"}))
	Collect(&issues, activationStringField(a.RetiredAt, []string{"retiredAt"}))
	if len(issues) > 0 {
		return issues
	}

	state := string(a.State)
	require := func(field string, present bool, what string) {
		if !present {
			Collect(&issues, &Issue{
				Path:    []string{field},
				Message: "routing-policy activation in state '" + state + "' requires " + what,
			})
		}
	}
	forbid := func(field string, present bool, what string) {
		if present {
			Collect(&issues, &Issue{
				Path:    []string{field},
				Message: "routing-policy activation in state '" + state + "' must not carry " + what,
			})
		}
	}

	if state == "draft" {
		forbid("evaluationReportRef", a.EvaluationReportRef != nil, "a report ref")
	} else {
		require("evaluationReportRef", a.EvaluationReportRef != nil, "the shadow/canary evaluation report ref (V-P12-11)")
	}

	if state == "active" || state == "retired" {
		require("activatedBy", a.ActivatedBy != nil, "the application-owned actor (§3 rule 7)")
		require("activatedAt", a.ActivatedAt != nil, "the activation instant")
	} else {
		forbid("activatedBy", a.ActivatedBy != nil, "an actor")
		forbid("activatedAt", a.ActivatedAt != nil, "an activation instant")
	}

	if state == "retired" {
		require("retiredAt", a.RetiredAt != nil, "the retirement instant")
	} else {
		forbid("retiredAt", a.RetiredAt != nil, "a retirement instant")
	}

	return issues
}

// activationStringField enforces z.string().min(1).optional() on an
// activation lifecycle field.
func activationStringField(v *string, path []string) *Issue {
	if v == nil {
		return nil
	}
	return RequiredString(*v, 1, path)
}

// ParseRoutingPolicyActivation strictly decodes and validates a
// routing-policy activation envelope (unknown JSON keys are rejected,
// mirroring z.strictObject).
func ParseRoutingPolicyActivation(data []byte) (*RoutingPolicyActivation, error) {
	var a RoutingPolicyActivation
	if err := StrictDecode(data, &a); err != nil {
		return nil, err
	}
	if issues := a.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &a, nil
}

// Typed pointer values backing the routing examples (orgPolicy-prefixed
// siblings live in org-policy.go; rp-prefixed names keep this file's
// namespace distinct).
var (
	rpTemperature      = 0.0
	rpMaxTokens        = 4096
	rpStructuredNative = StructuredOutputNative
	rpAllowFallbacks   = true
	rpEvaluationRef    = "canary/replacement-shadow-routing-v2-v1/report-01"
	rpActivatedBy      = "app://promotion-cli"
	rpActivatedAt      = "2026-10-09T12:00:00.000Z"
)

// RoutingPolicyExample mirrors ROUTING_POLICY_EXAMPLE: a v2 policy routing
// high-risk cases to the stronger model (golden data).
var RoutingPolicyExample = RoutingPolicy{
	Schema:    RoutingPolicySchemaID,
	Version:   2,
	SourceRef: "org/routing.yml@abc123",
	Buckets: RoutingPolicyBuckets{
		Low:    RoutingPolicyBinding{Primary: "z-ai/glm-4.7-air"},
		Medium: RoutingPolicyBinding{Primary: "z-ai/glm-4.7", Fallbacks: []string{"openai/gpt-4.1-mini"}},
		High: RoutingPolicyBinding{
			Primary:   "anthropic/claude-sonnet-4.5",
			Fallbacks: []string{"anthropic/claude-3.7-sonnet"},
			Params: &RoutingPolicyParams{
				Temperature:      &rpTemperature,
				MaxTokens:        &rpMaxTokens,
				StructuredOutput: &rpStructuredNative,
			},
			ProviderRouting: &RoutingPolicyProviderRouting{
				Provider: RoutingPolicyProviderPreference{
					Order:          []string{"anthropic", "bedrock"},
					AllowFallbacks: &rpAllowFallbacks,
				},
			},
		},
	},
}

// RoutingPolicyActivationExample mirrors ROUTING_POLICY_ACTIVATION_EXAMPLE:
// the same policy ACTIVE after an evaluated canary report (golden data).
var RoutingPolicyActivationExample = RoutingPolicyActivation{
	Policy:              RoutingPolicyExample,
	State:               RoutingPolicyActive,
	EvaluationReportRef: &rpEvaluationRef,
	ActivatedBy:         &rpActivatedBy,
	ActivatedAt:         &rpActivatedAt,
}
