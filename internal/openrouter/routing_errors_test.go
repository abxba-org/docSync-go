package openrouter_test

// routing_errors_test.go — Go test of packages/openrouter/src/routing/errors.ts
// typed errors (P12-T10/T11/T12; ARCH §10.1, §10.3, §10.6; D9; R2). Focused on
// the error TYPES themselves: the verbatim .code, the structured fields
// (Issues / From / To / Violations), and the preserved message text. Error()
// renders the docutil-coded message (which prepends the docutil code), so
// message text is matched by substring; the TS text is preserved inside.
//
// The behavioral tests (does an un-evaluated activation actually veto, does a
// fabricated model id actually throw) live with the routing policy/replacement/
// provenance units that construct these errors.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/openrouter"
)

func TestRoutingPolicyError(t *testing.T) {
	err := openrouter.NewRoutingPolicyError("not a valid docsync.routing-policy/v1 document", []string{"roles.low.primary: Required", "roles.high.primary: Required"})
	var pe *openrouter.RoutingPolicyError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "routing_policy_invalid", pe.Code)
	assert.Contains(t, err.Error(), "not a valid docsync.routing-policy/v1 document")
	assert.Equal(t, []string{"roles.low.primary: Required", "roles.high.primary: Required"}, pe.Issues)

	// Unwraps to a docutil coded error (Family pattern used across the repo).
	var coded *docutil.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, docutil.CodeConfig, coded.Code)

	// issues default empty, never nil.
	bare := openrouter.NewRoutingPolicyError("bare", nil)
	require.ErrorAs(t, bare, &pe)
	assert.Equal(t, []string{}, pe.Issues)
}

func TestRoutingPolicyActivationError(t *testing.T) {
	err := openrouter.NewRoutingPolicyActivationError("draft", "active", "activation requires the explicit application-owned actor")
	var ae *openrouter.RoutingPolicyActivationError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "routing_activation_veto", ae.Code)
	assert.Equal(t, "draft", ae.From)
	assert.Equal(t, "active", ae.To)
	assert.Contains(t, err.Error(), "activation requires the explicit application-owned actor")

	var coded *docutil.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, docutil.CodeState, coded.Code)
}

func TestReplacementEvaluationError(t *testing.T) {
	err := openrouter.NewReplacementEvaluationError("minAgreementRate must be in [0, 1]")
	var re *openrouter.ReplacementEvaluationError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "replacement_evaluation_invalid", re.Code)
	assert.Contains(t, err.Error(), "minAgreementRate must be in [0, 1]")

	var coded *docutil.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, docutil.CodeValidation, coded.Code)
}

func TestActualModelProvenanceError(t *testing.T) {
	err := openrouter.NewActualModelProvenanceError("actual-model provenance enforcement failed (1 violation(s)) — records MUST name the actual routed model ids", []string{"judge record names NO model id"})
	var ap *openrouter.ActualModelProvenanceError
	require.ErrorAs(t, err, &ap)
	assert.Equal(t, "actual_model_provenance_violation", ap.Code)
	assert.Equal(t, []string{"judge record names NO model id"}, ap.Violations)
	assert.Contains(t, err.Error(), "actual-model provenance enforcement failed")

	var coded *docutil.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, docutil.CodeVerify, coded.Code)

	// violations default empty, never nil.
	bare := openrouter.NewActualModelProvenanceError("x", nil)
	require.ErrorAs(t, bare, &ap)
	assert.Equal(t, []string{}, bare.Violations)
}

func TestRoutingErrorCodesPinnedVerbatim(t *testing.T) {
	assert.Equal(t, "routing_policy_invalid", openrouter.RoutingPolicyErrorCode)
	assert.Equal(t, "routing_activation_veto", openrouter.RoutingPolicyActivationErrorCode)
	assert.Equal(t, "replacement_evaluation_invalid", openrouter.ReplacementEvaluationErrorCode)
	assert.Equal(t, "actual_model_provenance_violation", openrouter.ActualModelProvenanceErrorCode)
}
