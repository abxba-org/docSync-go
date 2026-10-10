package openrouter

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Typed error taxonomy for the OpenRouter model-access layer (ARCH §10, D10) —
// the Go port of errors.ts.
//
// Everything that crosses the public seam is a *GatewayError subtype — callers
// degrade to advisory/REVIEW behavior, never crash on untyped errors.
// Abstain-type outcomes are NOT errors: the client returns typed results
// (*TotalInferenceFailure is the one failure that throws in TS, because there
// is no completion to return — D10 says the CALLER decides the degradation).
//
// Coding convention (CONVENTIONS.md "Errors"): every error is a coded error
// carried through docutil.ModelError (code EDOCSYNC_MODEL — the closest listed
// code for the model-access layer; the TS class codes are preserved verbatim in
// the Code field so callers and tests keep the observable identity).

// TS class codes, preserved verbatim as the observable identity of each error.
const (
	CodeTransportFailure          = "transport_failure"
	CodeTotalInferenceFailure     = "total_inference_failure"
	CodeModelsConfigInvalid       = "models_config_invalid"
	CodeRoleBindingMissing        = "role_binding_missing"
	CodeGatewayGuardViolation     = "gateway_guard_violation"
	CodeStructuredOutputViolation = "structured_output_violation"
	CodeNotYetImplementedRole     = "not_yet_implemented_role"
	CodeReplayExhausted           = "replay_exhausted"
)

// ModelAttempt is one transport-level attempt inside a model walk (shared
// failure data; TS ModelAttempt). JSON keys stay exactly as TS declared them:
// `providerOrder` is camelCase, `latency_ms` is snake_case.
type ModelAttempt struct {
	// Target is the model id this attempt targeted.
	Target string `json:"target"`
	// ProviderOrder is the provider pin for the attempt, when the
	// provider-order walk pinned one (nil == TS undefined).
	ProviderOrder []string `json:"providerOrder,omitempty"`
	// Attempt is the 1-based attempt number within the target (transport-level
	// retries included).
	Attempt int `json:"attempt"`
	// Status is the HTTP status when the exchange completed; absent for
	// network/timeout failures.
	Status *int `json:"status,omitempty"`
	// Error is the error summary (never contains the API key — guarded at the
	// request boundary).
	Error string `json:"error,omitempty"`
	// LatencyMS is the attempt latency in milliseconds.
	LatencyMS int `json:"latency_ms"`
}

// GatewayError is the base coded error for every typed error this package
// produces (TS GatewayError). Code carries the TS class code verbatim
// (e.g. "transport_failure"); the rendered message goes through
// docutil.ModelError so EDOCSYNC_MODEL is the wire code.
type GatewayError struct {
	// Code is the TS class code (CodeTransportFailure, …).
	Code string
	// Message is the TS error message text, preserved exactly.
	Message string

	inner *docutil.Error
}

// NewGatewayError builds the base error with the TS class code + message.
func NewGatewayError(code, message string) *GatewayError {
	return &GatewayError{
		Code:    code,
		Message: message,
		inner:   docutil.ModelError("%s", message),
	}
}

// Error renders the coded error (`EDOCSYNC_MODEL: <message>`).
func (e *GatewayError) Error() string { return e.inner.Error() }

// ModelCode returns EDOCSYNC_MODEL (the docutil code of the family).
func (e *GatewayError) ModelCode() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *GatewayError) Unwrap() error { return e.inner }

// TransportError is a network-level or timeout failure at the transport seam
// (retryable by the client; TS TransportError).
type TransportError struct {
	GatewayError

	// Status is the HTTP status when the exchange completed but the body was
	// unusable; 0 == TS undefined.
	Status int
	// Cause is the underlying error, when there was one (TS options.cause).
	Cause error
}

// NewTransportError builds a TransportError. status 0 means "no HTTP status".
func NewTransportError(message string, status int, cause error) *TransportError {
	return &TransportError{
		GatewayError: *NewGatewayError(CodeTransportFailure, message),
		Status:       status,
		Cause:        cause,
	}
}

// TotalInferenceFailure means every model target for a role was exhausted
// (primary, fallbacks, provider pins). It is the Go form of the TS throw — the
// caller degrades to advisory/REVIEW (D10); the client never fabricates a
// result.
type TotalInferenceFailure struct {
	GatewayError

	// Role is the role whose walk was exhausted.
	Role string
	// Attempts are the per-target attempt records (key-scrubbed by the client
	// before the failure leaves the gateway).
	Attempts []ModelAttempt
}

// NewTotalInferenceFailure builds the failure with the TS message format:
// `all model targets exhausted for role '<role>' after <n> transport
// attempt(s) (D10: the caller degrades to advisory/REVIEW — no result is
// fabricated)`.
func NewTotalInferenceFailure(role string, attempts []ModelAttempt) *TotalInferenceFailure {
	message := fmt.Sprintf(
		"all model targets exhausted for role '%s' after %d transport attempt(s) "+
			"(D10: the caller degrades to advisory/REVIEW — no result is fabricated)",
		role, len(attempts),
	)
	return &TotalInferenceFailure{
		GatewayError: *NewGatewayError(CodeTotalInferenceFailure, message),
		Role:         role,
		Attempts:     attempts,
	}
}

// ModelsConfigError is a rejected models.yml document — config error,
// fail-closed before any request (TS ModelsConfigError).
type ModelsConfigError struct {
	GatewayError

	// Issues are the formatted validation issues (may be empty).
	Issues []string
}

// NewModelsConfigError builds a ModelsConfigError. Issues is variadic so the
// gateway can build the error with or without the formatted issue list.
func NewModelsConfigError(message string, issues ...string) *ModelsConfigError {
	return &ModelsConfigError{
		GatewayError: *NewGatewayError(CodeModelsConfigInvalid, message),
		Issues:       issues,
	}
}

// RoleBindingError means a role has no binding in the (valid) models config, or
// is unknown (TS RoleBindingError).
type RoleBindingError struct {
	GatewayError

	// Role is the unbound role.
	Role string
}

// NewRoleBindingError builds a RoleBindingError with the TS message format:
// `role '<role>' has no binding in models.yml (docsync.models/v1); bound
// roles: <roles>` (models.go builds and passes the message).
func NewRoleBindingError(role, message string) *RoleBindingError {
	return &RoleBindingError{
		GatewayError: *NewGatewayError(CodeRoleBindingMissing, message),
		Role:         role,
	}
}

// GatewayGuardError means the gateway guard fired: API-key material detected in
// an outgoing payload, or an empty key was guarded. The request is blocked
// BEFORE any network I/O (TS GatewayGuardError).
type GatewayGuardError struct {
	GatewayError

	// Location is the JSON path of the offending value ("$.a.b[0].c"), when the
	// deep scan located it (TS options.location).
	Location string
}

// NewGatewayGuardError builds a GatewayGuardError ("" location == absent).
func NewGatewayGuardError(message, location string) *GatewayGuardError {
	return &GatewayGuardError{
		GatewayError: *NewGatewayError(CodeGatewayGuardViolation, message),
		Location:     location,
	}
}

// StructuredOutputError means structured-output enforcement failed for one
// attempt (bounded-retry input data; TS StructuredOutputError).
type StructuredOutputError struct {
	GatewayError

	// Attempt is the 1-based attempt number.
	Attempt int
	// Issues are the schema-violation issue strings.
	Issues []string
}

// NewStructuredOutputError builds a StructuredOutputError with the TS message
// format: `structured output violates the '<name>' contract: <issues joined by
// '; '>`.
func NewStructuredOutputError(schemaName string, attempt int, issues []string) *StructuredOutputError {
	message := fmt.Sprintf(
		"structured output violates the '%s' contract: %s",
		schemaName, joinSemicolon(issues),
	)
	return &StructuredOutputError{
		GatewayError: *NewGatewayError(CodeStructuredOutputViolation, message),
		Attempt:      attempt,
		Issues:       issues,
	}
}

// NotYetImplementedRoleError means a V2+ forward-declared role was invoked
// before its phase (TS NotYetImplementedRoleError).
type NotYetImplementedRoleError struct {
	GatewayError

	// Role is the forward-declared role.
	Role string
}

// NewNotYetImplementedRoleError builds the error with the fixed TS message:
// `role '<role>' is forward-declared for a later phase and has no
// implementation yet`.
func NewNotYetImplementedRoleError(role string) *NotYetImplementedRoleError {
	message := fmt.Sprintf(
		"role '%s' is forward-declared for a later phase and has no implementation yet",
		role,
	)
	return &NotYetImplementedRoleError{
		GatewayError: *NewGatewayError(CodeNotYetImplementedRole, message),
		Role:         role,
	}
}

// ReplayExhaustedError is testing-only: a scripted transport ran out of
// recorded/scripted exchanges (TS ReplayExhaustedError).
type ReplayExhaustedError struct {
	GatewayError
}

// NewReplayExhaustedError builds a ReplayExhaustedError.
func NewReplayExhaustedError(message string) *ReplayExhaustedError {
	return &ReplayExhaustedError{
		GatewayError: *NewGatewayError(CodeReplayExhausted, message),
	}
}

// joinSemicolon joins strings with "; " (TS issues.join('; ')).
func joinSemicolon(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += "; "
		}
		out += v
	}
	return out
}
