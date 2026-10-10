package openrouter_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Port of packages/openrouter/test — errors taxonomy (errors.ts): every typed
// error is a coded error (EDOCSYNC_MODEL) carrying the TS class code verbatim
// and the preserved message text.

func TestGatewayErrorBaseIsCodedAndPreservesIdentity(t *testing.T) {
	err := openrouter.NewGatewayError("invalid_client_options", "apiKey must be a non-empty string resolved via resolveOpenRouterApiKey (env-only, ARCH §10.7)")
	require.Error(t, err)
	assert.Equal(t, "invalid_client_options", err.Code)
	assert.Equal(t, "apiKey must be a non-empty string resolved via resolveOpenRouterApiKey (env-only, ARCH §10.7)", err.Message)
	assert.Equal(t, docutil.CodeModel, err.ModelCode())
	assert.Equal(t, "EDOCSYNC_MODEL: "+err.Message, err.Error())
	assert.Equal(t, docutil.CodeModel, docutil.CodeOf(err))
	// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
	var coded *docutil.Error
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, docutil.CodeModel, coded.Code)
}

func TestTransportErrorCarriesStatusAndCause(t *testing.T) {
	cause := errors.New("connection reset by peer")
	err := openrouter.NewTransportError("transport request failed: connection reset by peer", 503, cause)
	assert.Equal(t, openrouter.CodeTransportFailure, err.Code)
	assert.Equal(t, 503, err.Status)
	assert.Equal(t, cause, err.Cause)
	var typed *openrouter.TransportError
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, docutil.CodeModel, docutil.CodeOf(typed))
	// Absent status renders as 0 (TS status?: number).
	assert.Equal(t, 0, openrouter.NewTransportError("m", 0, nil).Status)
}

func TestTotalInferenceFailureMessageAndDetails(t *testing.T) {
	attempts := []openrouter.ModelAttempt{
		{Target: "z-ai/glm-4.7", Attempt: 1, Status: orIntPtr(500), Error: "HTTP 500", LatencyMS: 3},
		{Target: "openai/gpt-4.1-mini", ProviderOrder: []string{"openai"}, Attempt: 1, Error: "boom", LatencyMS: 4},
	}
	err := openrouter.NewTotalInferenceFailure("decision-engine", attempts)
	assert.Equal(t, openrouter.CodeTotalInferenceFailure, err.Code)
	assert.Equal(t, "decision-engine", err.Role)
	require.Len(t, err.Attempts, 2)
	assert.Equal(t,
		"all model targets exhausted for role 'decision-engine' after 2 transport attempt(s) "+
			"(D10: the caller degrades to advisory/REVIEW — no result is fabricated)",
		err.Message,
	)
	var typed *openrouter.TotalInferenceFailure
	require.True(t, errors.As(err, &typed))
}

func TestModelAttemptJSONKeysMatchTSExactly(t *testing.T) {
	attempt := openrouter.ModelAttempt{
		Target:        "z-ai/glm-4.7",
		ProviderOrder: []string{"openai"},
		Attempt:       1,
		Status:        orIntPtr(500),
		Error:         "HTTP 500",
		LatencyMS:     3,
	}
	data, err := json.Marshal(attempt)
	require.NoError(t, err)
	// Wire format is law: providerOrder camelCase, latency_ms snake_case.
	assert.JSONEq(t,
		`{"target":"z-ai/glm-4.7","providerOrder":["openai"],"attempt":1,"status":500,"error":"HTTP 500","latency_ms":3}`,
		string(data),
	)
	// Absent optional fields are omitted (TS undefined).
	bare := openrouter.ModelAttempt{Target: "m", Attempt: 2, LatencyMS: 1}
	data, err = json.Marshal(bare)
	require.NoError(t, err)
	assert.JSONEq(t, `{"target":"m","attempt":2,"latency_ms":1}`, string(data))
}

func TestModelsConfigErrorAndRoleBindingError(t *testing.T) {
	configErr := openrouter.NewModelsConfigError("not a valid docsync.models/v1 binding", "roles.x.model: Required")
	assert.Equal(t, openrouter.CodeModelsConfigInvalid, configErr.Code)
	assert.Equal(t, []string{"roles.x.model: Required"}, configErr.Issues)
	// Variadic form (the models.go call site passes no issue list).
	bare := openrouter.NewModelsConfigError("rejected fail-closed")
	assert.Empty(t, bare.Issues)
	assert.Equal(t, "EDOCSYNC_MODEL: rejected fail-closed", bare.Error())

	bindingErr := openrouter.NewRoleBindingError("judge",
		"role 'judge' has no binding in models.yml (docsync.models/v1); bound roles: decision-engine, judge, writer")
	assert.Equal(t, openrouter.CodeRoleBindingMissing, bindingErr.Code)
	assert.Equal(t, "judge", bindingErr.Role)
}

func TestGatewayGuardErrorCarriesLocation(t *testing.T) {
	err := openrouter.NewGatewayGuardError(
		"API-key material detected in the outgoing payload at $.a.b[0].c — request blocked before any network I/O (ARCH §10.7)",
		"$.a.b[0].c",
	)
	assert.Equal(t, openrouter.CodeGatewayGuardViolation, err.Code)
	assert.Equal(t, "$.a.b[0].c", err.Location)
	assert.Equal(t, "", openrouter.NewGatewayGuardError("no location", "").Location)
}

func TestStructuredOutputErrorCarriesAttemptAndIssues(t *testing.T) {
	err := openrouter.NewStructuredOutputError("contract", 3, []string{"answer: Expected string, received number"})
	assert.Equal(t, openrouter.CodeStructuredOutputViolation, err.Code)
	assert.Equal(t, 3, err.Attempt)
	assert.Equal(t, []string{"answer: Expected string, received number"}, err.Issues)
	assert.Equal(t,
		"structured output violates the 'contract' contract: answer: Expected string, received number",
		err.Message,
	)
}

func TestStructuredOutputErrorJoinsMultipleIssues(t *testing.T) {
	err := openrouter.NewStructuredOutputError("contract", 2,
		[]string{"answer: Expected string, received number", "extra: Unrecognized key(s) in object: 'extra'"})
	assert.Equal(t,
		"structured output violates the 'contract' contract: "+
			"answer: Expected string, received number; extra: Unrecognized key(s) in object: 'extra'",
		err.Message,
	)
	require.Len(t, err.Issues, 2)
	assert.Equal(t, 2, err.Attempt)
}

func TestNotYetImplementedRoleErrorMessage(t *testing.T) {
	err := openrouter.NewNotYetImplementedRoleError("drift-auditor")
	assert.Equal(t, openrouter.CodeNotYetImplementedRole, err.Code)
	assert.Equal(t, "drift-auditor", err.Role)
	assert.Equal(t,
		"role 'drift-auditor' is forward-declared for a later phase and has no implementation yet",
		err.Message,
	)
}

func TestReplayExhaustedError(t *testing.T) {
	err := openrouter.NewReplayExhaustedError("replay transport exhausted after 3 recorded exchange(s)")
	assert.Equal(t, openrouter.CodeReplayExhausted, err.Code)
	assert.Equal(t, "replay transport exhausted after 3 recorded exchange(s)", err.Message)
	// errors.As reaches the coded base for every subtype.
	var typed *openrouter.ReplayExhaustedError
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, docutil.CodeModel, docutil.CodeOf(err))
}
