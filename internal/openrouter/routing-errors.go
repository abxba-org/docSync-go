package openrouter

// routing-errors.go — Go port of packages/openrouter/src/routing/errors.ts
// (P12-T10/T11/T12; ARCH §10.1, §10.3, §10.6; D9; R2): typed errors for the
// declarative routing layer. In TypeScript each extends the package's
// GatewayError taxonomy so callers degrade fail-closed through the existing
// typed seam.
//
// Go has no class hierarchy, so each routing error is a concrete typed error
// that carries BOTH the TS GatewayError `.code` discriminator (exported Code
// field — preserved verbatim) and a wrapped docutil coded error (so
// docutil.CodeOf / errors.As keep working, matching the errors.go
// GatewayError family). Error() renders the coded message; because that
// prepends the docutil code, callers/tests match message text with substring
// checks (the TS text is preserved inside).
//
// OWNERSHIP/SEAM NOTE (plan.md — one writer per file): the TS classes live in
// src/routing/errors.ts. This file is that port's single home and is the source
// of truth for the four routing error types + their exported constructors;
// routing-policy.go / routing-replacement.go / routing-provenance.go (owned by
// the routing unit) construct these and MUST NOT redeclare the types. The
// constructors are exported so both this package and package openrouter_test can
// build them.

import "github.com/DUDE616/docSync-go/internal/docutil"

// Routing error codes — the TS GatewayError.code discriminators, verbatim.
const (
	RoutingPolicyErrorCode           = "routing_policy_invalid"
	RoutingPolicyActivationErrorCode = "routing_activation_veto"
	ReplacementEvaluationErrorCode   = "replacement_evaluation_invalid"
	ActualModelProvenanceErrorCode   = "actual_model_provenance_violation"
)

// RoutingPolicyError is a routing-policy document rejected (parse/resolution) —
// a config error, fail-closed before any request (mirrors ModelsConfigError).
type RoutingPolicyError struct {
	// Code is the TS GatewayError discriminator ('routing_policy_invalid').
	Code string
	// Issues are the formatted validation issues (`path: message`); may be empty.
	Issues []string

	coded *docutil.Error
}

func (e *RoutingPolicyError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *RoutingPolicyError) Unwrap() error { return e.coded }

// NewRoutingPolicyError builds a RoutingPolicyError (issues empty when nil).
func NewRoutingPolicyError(message string, issues []string) *RoutingPolicyError {
	if issues == nil {
		issues = []string{}
	}
	return &RoutingPolicyError{Code: RoutingPolicyErrorCode, Issues: issues, coded: docutil.New(docutil.CodeConfig, message)}
}

// RoutingPolicyActivationError is a routing-policy activation-state transition
// vetoed (P12-T10; V-P12-11; R2): the state machine never lets an un-evaluated
// policy become active, and never auto-activates from an evaluation report — a
// human/application act (§3 rule 7: "models propose; humans promote") is
// required, carried as the explicit actor argument.
type RoutingPolicyActivationError struct {
	// From / To are the vetoed transition endpoints (TS `from` / `to`).
	From string
	To   string
	// Code is the TS GatewayError discriminator ('routing_activation_veto').
	Code string

	coded *docutil.Error
}

func (e *RoutingPolicyActivationError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *RoutingPolicyActivationError) Unwrap() error { return e.coded }

// NewRoutingPolicyActivationError builds a RoutingPolicyActivationError for a
// from→to transition vetoed with the given message.
func NewRoutingPolicyActivationError(from, to, message string) *RoutingPolicyActivationError {
	return &RoutingPolicyActivationError{From: from, To: to, Code: RoutingPolicyActivationErrorCode, coded: docutil.New(docutil.CodeState, message)}
}

// ReplacementEvaluationError is a replacement-model shadow-evaluation request
// rejected before it ran.
type ReplacementEvaluationError struct {
	// Code is the TS GatewayError discriminator ('replacement_evaluation_invalid').
	Code string

	coded *docutil.Error
}

func (e *ReplacementEvaluationError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *ReplacementEvaluationError) Unwrap() error { return e.coded }

// NewReplacementEvaluationError builds a ReplacementEvaluationError.
func NewReplacementEvaluationError(message string) *ReplacementEvaluationError {
	return &ReplacementEvaluationError{Code: ReplacementEvaluationErrorCode, coded: docutil.New(docutil.CodeValidation, message)}
}

// ActualModelProvenanceError is provenance enforcement (P12-T12; ARCH §10.6):
// a decision/writer/verifier record names a model id that is missing or
// fabricated (neither the telemetry-routed id nor — when telemetry is absent —
// the configured passthrough). The record is rejected fail-closed; provenance
// or it didn't happen (§3 rule 10; invariant 10).
type ActualModelProvenanceError struct {
	// Code is the TS GatewayError discriminator ('actual_model_provenance_violation').
	Code string
	// Violations lists ALL violations found (fail closed); empty when none.
	Violations []string

	coded *docutil.Error
}

func (e *ActualModelProvenanceError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *ActualModelProvenanceError) Unwrap() error { return e.coded }

// NewActualModelProvenanceError builds an ActualModelProvenanceError listing all
// violations (violations empty when nil).
func NewActualModelProvenanceError(message string, violations []string) *ActualModelProvenanceError {
	if violations == nil {
		violations = []string{}
	}
	// DOCUTIL DEVIATION: the planned EDOCSYNC_PROVENANCE code is not landed in
	// docutil yet, so the closest existing family (EDOCSYNC_VERIFY — a verify /
	// integrity gate on provenance records) carries the coded message; the TS
	// discriminator ('actual_model_provenance_violation') is preserved verbatim
	// in the exported Code field.
	return &ActualModelProvenanceError{Code: ActualModelProvenanceErrorCode, Violations: violations, coded: docutil.New(docutil.CodeVerify, message)}
}
