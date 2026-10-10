package openrouter

// models.go — Go port of packages/openrouter/src/models.ts (P2-T03; D9,
// ARCH §10.3): roles — not models — are the stable seam. `models.yml` is
// validated against the Phase-0 `docsync.models/v1` base (internal/schemas)
// first, then against a LOCAL superset refinement (ExtendedModelsConfig) that
// adds the ARCH §10.3 binding fields the gateway consumes (`params`,
// `routing.provider`, `constraint`).
//
// DIVERGENCE (surface, do not edit internal/schemas): the Phase-0 binding is
// {model, fallbacks?} only, while ARCH §10.3 binds primary/fallbacks/
// params{temperature,max_tokens,structured_output}/
// routing.provider{order,allow_fallbacks}/constraint. The extended shape here
// is a strict SUPERSET refinement: a document that satisfies the base schema
// but carries malformed extended fields is REJECTED fail-closed. The parse
// result carries Phase0SchemaCompliant plus per-field Divergences naming which
// extended fields the registry schema cannot represent yet; the schemas
// artifact stays untouched.
//
// External imports are limited to internal/schemas (+ internal/docutil
// elsewhere in this unit). ModelsConfigError / RoleBindingError come from the
// sibling errors.go barrel (the GatewayError taxonomy); see NewModelsConfigError
// / NewRoleBindingError.

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// RoleBindingConstraintMustNotEqualPatchWriter is the single allowed constraint
// literal (P4-T16; D13/ARCH §10.3): the Tier-3 judge must be an independent
// model instance, never the patch-writer's. The vocabulary is CLOSED — any other
// non-empty value fails the parse fail-closed with the field path named.
const RoleBindingConstraintMustNotEqualPatchWriter = "must_not_equal_patch-writer_instance"

// RoleBindingConstraint is the (closed) constraint vocabulary type.
type RoleBindingConstraint string

// JudgeConstraintValues is the CLOSED constraint vocabulary — exactly the one
// instance constraint. Mirrors JUDGE_CONSTRAINT_VALUES.
var JudgeConstraintValues = []RoleBindingConstraint{
	RoleBindingConstraintMustNotEqualPatchWriter,
}

// RoleParams are the deterministic generation params a role may pin
// (ARCH §10.3 `params:`). Pointers distinguish an absent key from a zero value
// (temperature: 0 is meaningful).
type RoleParams struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	StructuredOutput string   `json:"structured_output,omitempty"` // "" absent; "native"/"prompt"
}

// ProviderRoutingPrefs is ARCH §10.3 `routing.provider`.
type ProviderRoutingPrefs struct {
	Order          []string `json:"order"`
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
}

// ProviderRouting is ARCH §10.3 `routing`.
type ProviderRouting struct {
	Provider ProviderRoutingPrefs `json:"provider"`
}

// ExtendedRoleBinding is the local superset refinement of
// schemas.ModelRoleBinding ({model, fallbacks?}) with the ARCH §10.3 extended
// fields. Strict unknown-key rejection is preserved (Parsed via
// schemas.StrictDecode). Constraint is a pointer so an explicit empty string
// is distinguishable from an absent key (both fail-closed, but the empty string
// is PRESENT and must be reported at the field path).
type ExtendedRoleBinding struct {
	Model      string                 `json:"model"`
	Fallbacks  []string               `json:"fallbacks,omitempty"`
	Params     *RoleParams            `json:"params,omitempty"`
	Routing    *ProviderRouting       `json:"routing,omitempty"`
	Constraint *RoleBindingConstraint `json:"constraint,omitempty"`
}

// ExtendedModelsConfig is the gateway's operational view of `docsync.models/v1`.
type ExtendedModelsConfig struct {
	Schema  string                         `json:"schema"`
	Version int                            `json:"version"`
	Roles   map[string]ExtendedRoleBinding `json:"roles"`
	// roleOrder preserves the source-document role-key insertion order so
	// divergences and bound-role listings match the TypeScript output.
	roleOrder []string
}

// ModelsConfigParse is the parse result (Untrusted input → validated config).
type ModelsConfigParse struct {
	Models                ExtendedModelsConfig
	Phase0SchemaCompliant bool
	Divergences           []string
}

// ResolvedRoleParams carries resolved optional params (structured_output "" ==
// omitted → 'native' at the consumer, OD-R41).
type ResolvedRoleParams struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	StructuredOutput string   `json:"structured_output,omitempty"`
}

// ResolvedProviderRouting mirrors the operational provider routing (allow_fallbacks
// defaulted to true).
type ResolvedProviderRouting struct {
	Order          []string `json:"order"`
	AllowFallbacks bool     `json:"allow_fallbacks"`
}

// ResolvedRoleBinding is the operational binding the client walks — Model is
// surfaced as Primary (ARCH §10.3 naming) with Fallbacks defaulted to an empty
// slice.
type ResolvedRoleBinding struct {
	Primary         string                   `json:"primary"`
	Fallbacks       []string                 `json:"fallbacks"`
	Params          *ResolvedRoleParams      `json:"params,omitempty"`
	ProviderRouting *ResolvedProviderRouting `json:"providerRouting,omitempty"`
	Constraint      *RoleBindingConstraint   `json:"constraint,omitempty"`
}

// RoutedModels is a SEAM type (D9/P9-T02; V-P9-4). In TypeScript it is imported
// from @docsync/core and consumed by buildProvenanceRecord. The orchestrator
// will wire core's RoutedModels; field names match the TS type exactly
// ({detectorModel, writerModel}) so the provenance builder consumes it verbatim.
//
// TODO(orchestrator): replace this seam with core's RoutedModels once the core
// provenance builder is landed; keep the JSON keys detectorModel/writerModel.
type RoutedModels struct {
	DetectorModel string `json:"detectorModel"`
	WriterModel   string `json:"writerModel"`
}

// formatModelsIssues renders validation issues like the TS formatIssues:
// "path.to.field: message", joined by "; ", with "(root)" for the empty path.
func formatModelsIssues(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, is := range issues {
		path := "(root)"
		if len(is.Path) > 0 {
			path = strings.Join(is.Path, ".")
		}
		parts = append(parts, path+": "+is.Message)
	}
	return strings.Join(parts, "; ")
}

// orderedRoleKeys returns the keys of the top-level `roles` object in source
// order. Go maps do not preserve insertion order, but the divergence array and
// the bound-role listing are observable in the TS tests, so the order is read
// straight from the raw JSON token stream (fail-closed: nil on any parse hiccup,
// which only happens when validation already failed).
func orderedRoleKeys(data []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	var rolesRaw json.RawMessage
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil
		}
		key, _ := kt.(string)
		if key == "roles" {
			if err := dec.Decode(&rolesRaw); err != nil {
				return nil
			}
			continue
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil
		}
	}
	if rolesRaw == nil {
		return nil
	}
	dec2 := json.NewDecoder(bytes.NewReader(rolesRaw))
	t2, err := dec2.Token()
	if err != nil {
		return nil
	}
	if d, ok := t2.(json.Delim); !ok || d != '{' {
		return nil
	}
	keys := []string{}
	for dec2.More() {
		kt, err := dec2.Token()
		if err != nil {
			return nil
		}
		k, _ := kt.(string)
		keys = append(keys, k)
		var v json.RawMessage
		if err := dec2.Decode(&v); err != nil {
			return nil
		}
	}
	return keys
}

// sortedRoleNames returns the config's role names, preferring the preserved
// source order and falling back to sorted map keys.
func (c *ExtendedModelsConfig) sortedRoleNames() []string {
	if len(c.roleOrder) > 0 {
		out := make([]string, len(c.roleOrder))
		copy(out, c.roleOrder)
		return out
	}
	names := make([]string, 0, len(c.Roles))
	for k := range c.Roles {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// validateExtendedShape checks a decoded extended config against the superset
// schema (base fields + ARCH §10.3 extended fields), emitting zod-shaped issues
// with precise field paths.
func validateExtendedShape(ext *ExtendedModelsConfig) []schemas.Issue {
	var issues []schemas.Issue
	if ext.Schema != "docsync.models/v1" {
		issues = append(issues, schemas.Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.models/v1"`})
	}
	if ext.Version < 1 {
		issues = append(issues, schemas.Issue{Path: []string{"version"}, Message: "Number must be greater than 0"})
	}
	for _, role := range ext.sortedRoleNames() {
		b := ext.Roles[role]
		if len(role) < 1 {
			issues = append(issues, schemas.Issue{Path: []string{"roles", role}, Message: "String must contain at least 1 character(s)"})
		}
		if len(b.Model) < 1 {
			issues = append(issues, schemas.Issue{Path: []string{"roles", role, "model"}, Message: "String must contain at least 1 character(s)"})
		}
		for i, f := range b.Fallbacks {
			if len(f) < 1 {
				issues = append(issues, schemas.Issue{Path: []string{"roles", role, "fallbacks", strconv.Itoa(i)}, Message: "String must contain at least 1 character(s)"})
			}
		}
		if b.Params != nil {
			if b.Params.Temperature != nil {
				t := *b.Params.Temperature
				if t < 0 {
					issues = append(issues, schemas.Issue{Path: []string{"roles", role, "params", "temperature"}, Message: "Number must be greater than or equal to 0"})
				}
				if t > 2 {
					issues = append(issues, schemas.Issue{Path: []string{"roles", role, "params", "temperature"}, Message: "Number must be less than or equal to 2"})
				}
			}
			if b.Params.MaxTokens != nil && *b.Params.MaxTokens < 1 {
				issues = append(issues, schemas.Issue{Path: []string{"roles", role, "params", "max_tokens"}, Message: "Number must be greater than 0"})
			}
			if b.Params.StructuredOutput != "" {
				if iss := schemas.CheckEnum(b.Params.StructuredOutput, []string{"native", "prompt"}, []string{"roles", role, "params", "structured_output"}); iss != nil {
					issues = append(issues, *iss)
				}
			}
		}
		if b.Routing != nil {
			order := b.Routing.Provider.Order
			if len(order) < 1 {
				issues = append(issues, schemas.Issue{Path: []string{"roles", role, "routing", "provider", "order"}, Message: "Array must contain at least 1 element(s)"})
			}
			for i, o := range order {
				if len(o) < 1 {
					issues = append(issues, schemas.Issue{Path: []string{"roles", role, "routing", "provider", "order", strconv.Itoa(i)}, Message: "String must contain at least 1 character(s)"})
				}
			}
		}
		if b.Constraint != nil && string(*b.Constraint) != RoleBindingConstraintMustNotEqualPatchWriter {
			issues = append(issues, schemas.Issue{Path: []string{"roles", role, "constraint"}, Message: `Invalid literal value, expected "must_not_equal_patch-writer_instance"`})
		}
	}
	return issues
}

// ParseModelsConfig validates a parsed `models.yml` object (untrusted JSON
// input). The operational gate is the local superset refinement: it accepts
// both plain {model, fallbacks?} bindings and the ARCH §10.3 extended fields and
// rejects malformed extended fields fail-closed. On failure it returns a typed
// ModelsConfigError whose message preserves the TS text byte-for-byte.
func ParseModelsConfig(input []byte) (*ModelsConfigParse, error) {
	var ext ExtendedModelsConfig
	var issues []schemas.Issue
	if err := schemas.StrictDecode(input, &ext); err != nil {
		var ve *schemas.ValidationError
		if errors.As(err, &ve) {
			issues = append(issues, ve.Issues...)
		} else {
			issues = append(issues, schemas.Issue{Message: err.Error()})
		}
	} else {
		issues = validateExtendedShape(&ext)
	}

	if len(issues) > 0 {
		_, baseErr := schemas.ParseModelsConfig(input)
		baseCompliant := "no"
		if baseErr == nil {
			baseCompliant = "yes"
		}
		msg := "not a valid docsync.models/v1 binding — fails the gateway extended binding shape " +
			"(params{temperature,max_tokens} / routing.provider{order,allow_fallbacks} / constraint; Phase-0 base compliance: " +
			baseCompliant + ") — rejected fail-closed: " + formatModelsIssues(issues)
		return nil, NewModelsConfigError(msg)
	}

	ext.roleOrder = orderedRoleKeys(input)

	_, baseErr := schemas.ParseModelsConfig(input)
	phase0 := baseErr == nil

	return &ModelsConfigParse{
		Models:                ext,
		Phase0SchemaCompliant: phase0,
		Divergences:           modelsDivergences(&ext),
	}, nil
}

// modelsDivergences names, per role in source order, the extended fields the
// Phase-0 registry schema cannot represent (params/routing/constraint).
func modelsDivergences(ext *ExtendedModelsConfig) []string {
	divergences := []string{}
	for _, role := range ext.sortedRoleNames() {
		b := ext.Roles[role]
		if b.Params != nil {
			divergences = append(divergences, "roles."+role+".params — accepted by the gateway extended binding (ARCH §10.3), unknown to Phase-0 MODELS_SCHEMA (strict)")
		}
		if b.Routing != nil {
			divergences = append(divergences, "roles."+role+".routing — accepted by the gateway extended binding (ARCH §10.3), unknown to Phase-0 MODELS_SCHEMA (strict)")
		}
		if b.Constraint != nil {
			divergences = append(divergences, "roles."+role+".constraint — accepted by the gateway extended binding (D13/ARCH §10.3), unknown to Phase-0 MODELS_SCHEMA (strict)")
		}
	}
	return divergences
}

// MissingRequiredRoles reports the Phase-0 REQUIRED_MODEL_ROLES gaps
// ('decision-engine','judge','writer' from internal/schemas) — read-only data
// this layer may read but never edit. Returns an empty (non-nil) slice when
// none are missing.
func MissingRequiredRoles(models *ExtendedModelsConfig) []string {
	missing := []string{}
	for _, role := range schemas.RequiredModelRoles {
		if _, ok := models.Roles[role]; !ok {
			missing = append(missing, role)
		}
	}
	return missing
}

// ResolveRoleBinding resolves one role's binding from a validated config.
// Unknown role → typed RoleBindingError naming the bound roles (fail-closed,
// no default model is ever guessed).
func ResolveRoleBinding(models *ExtendedModelsConfig, role string) (*ResolvedRoleBinding, error) {
	binding, ok := models.Roles[role]
	if !ok {
		bound := strings.Join(models.sortedRoleNames(), ", ")
		return nil, NewRoleBindingError(role, "role '"+role+"' has no binding in models.yml (docsync.models/v1); bound roles: "+bound)
	}
	fallbacks := binding.Fallbacks
	if fallbacks == nil {
		fallbacks = []string{}
	} else {
		fallbacks = append([]string(nil), fallbacks...)
	}
	resolved := &ResolvedRoleBinding{
		Primary:   binding.Model,
		Fallbacks: fallbacks,
	}
	if binding.Params != nil {
		resolved.Params = &ResolvedRoleParams{
			Temperature:      binding.Params.Temperature,
			MaxTokens:        binding.Params.MaxTokens,
			StructuredOutput: binding.Params.StructuredOutput,
		}
	}
	if binding.Routing != nil {
		allowFallbacks := true
		if binding.Routing.Provider.AllowFallbacks != nil {
			allowFallbacks = *binding.Routing.Provider.AllowFallbacks
		}
		order := append([]string(nil), binding.Routing.Provider.Order...)
		resolved.ProviderRouting = &ResolvedProviderRouting{
			Order:          order,
			AllowFallbacks: allowFallbacks,
		}
	}
	if binding.Constraint != nil {
		c := *binding.Constraint
		resolved.Constraint = &c
	}
	return resolved, nil
}
