package openrouter_test

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Port of packages/openrouter/test/credentials.test.ts — credential guards
// (invariants 8/10): env-only resolution; the API key must never appear in an
// outgoing payload; telemetry redaction.

// orTestAPIKey is the distinct sentinel key the guards/redaction tests detect;
// never a real key (TS TEST_API_KEY).
const orTestAPIKey = "sk-or-v1-test-key-abc123def456"

func TestResolveOpenRouterApiKeyEnvOnlyTrimmedFailClosed(t *testing.T) {
	resolved := openrouter.ResolveOpenRouterApiKey(map[string]string{openrouter.OPENROUTER_API_KEY_ENV: orTestAPIKey})
	assert.True(t, resolved.OK)
	assert.Equal(t, orTestAPIKey, resolved.Key)

	trimmed := openrouter.ResolveOpenRouterApiKey(map[string]string{openrouter.OPENROUTER_API_KEY_ENV: "  key-with-space  "})
	assert.True(t, trimmed.OK)
	assert.Equal(t, "key-with-space", trimmed.Key)

	absent := openrouter.ResolveOpenRouterApiKey(map[string]string{})
	assert.False(t, absent.OK)
	assert.Equal(t, "OPENROUTER_API_KEY is not set — credentials are env-only (ARCH §10.7); the gateway cannot run without it (fail closed)", absent.Reason)

	blank := openrouter.ResolveOpenRouterApiKey(map[string]string{openrouter.OPENROUTER_API_KEY_ENV: "   "})
	assert.False(t, blank.OK)
	assert.Equal(t, "OPENROUTER_API_KEY is not set — credentials are env-only (ARCH §10.7); the gateway cannot run without it (fail closed)", blank.Reason)

	nilEnv := openrouter.ResolveOpenRouterApiKey(nil)
	assert.False(t, nilEnv.OK)
}

func TestAssertNoSecretInPayloadBlocksNestedStringsArraysAndKeys(t *testing.T) {
	nested := openrouter.AssertNoSecretInPayload(
		map[string]any{"messages": []any{map[string]any{"content": "leak " + orTestAPIKey}}}, orTestAPIKey)
	require.Error(t, nested)
	var guardErr *openrouter.GatewayGuardError
	require.ErrorAs(t, nested, &guardErr)
	assert.Equal(t, "$.messages[0].content", guardErr.Location)

	arrays := openrouter.AssertNoSecretInPayload(
		map[string]any{"nested": map[string]any{"deep": []any{orTestAPIKey}}}, orTestAPIKey)
	require.Error(t, arrays)
	require.ErrorAs(t, arrays, &guardErr)
	assert.Equal(t, "$.nested.deep[0]", guardErr.Location)

	keys := openrouter.AssertNoSecretInPayload(map[string]any{"x" + orTestAPIKey + "y": "value"}, orTestAPIKey)
	require.Error(t, keys)
	require.ErrorAs(t, keys, &guardErr)
	assert.Equal(t, "$<key>", guardErr.Location)
}

func TestAssertNoSecretInPayloadPassesCleanAndReportsLocation(t *testing.T) {
	require.NoError(t, openrouter.AssertNoSecretInPayload(
		map[string]any{"model": "z-ai/glm-4.7", "messages": []any{map[string]any{"role": "user", "content": "clean"}}},
		orTestAPIKey,
	))
	// Numbers, booleans, nil and nested clean structures pass.
	require.NoError(t, openrouter.AssertNoSecretInPayload(
		map[string]any{"temperature": float64(0), "strict": true, "nothing": nil,
			"deep": map[string]any{"list": []any{"a", "b"}}},
		orTestAPIKey,
	))

	err := openrouter.AssertNoSecretInPayload(
		map[string]any{"a": map[string]any{"b": []any{map[string]any{"c": orTestAPIKey}}}}, orTestAPIKey)
	require.Error(t, err)
	var guardErr *openrouter.GatewayGuardError
	require.ErrorAs(t, err, &guardErr)
	assert.Equal(t, "$.a.b[0].c", guardErr.Location)
	// The TS test pins this exact location string.
	assert.Equal(t, "$.a.b[0].c", guardErr.Location)
}

func TestAssertNoSecretInPayloadMessagesNeverEchoKeyMaterial(t *testing.T) {
	err := openrouter.AssertNoSecretInPayload(map[string]any{"content": orTestAPIKey}, orTestAPIKey)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), orTestAPIKey)

	nested := openrouter.AssertNoSecretInPayload(
		map[string]any{"messages": []any{map[string]any{"content": "leak " + orTestAPIKey}}}, orTestAPIKey)
	require.Error(t, nested)
	assert.NotContains(t, nested.Error(), orTestAPIKey)
}

func TestRedactKeyDeepScrubsTelemetryShapedValues(t *testing.T) {
	redacted := openrouter.RedactKeyDeep(
		map[string]any{
			"role":         "r",
			"routed_model": "m " + orTestAPIKey,
			"list":         []any{orTestAPIKey, "clean"},
		},
		orTestAPIKey,
	)
	data, err := json.Marshal(redacted)
	require.NoError(t, err)
	assert.NotContains(t, string(data), orTestAPIKey)
	assert.Contains(t, string(data), openrouter.REDACTED)

	// Deep clone semantics: the input is untouched (TS redactKeyDeep clones).
	input := map[string]any{"nested": map[string]any{"v": "has " + orTestAPIKey}}
	_ = openrouter.RedactKeyDeep(input, orTestAPIKey)
	nested := input["nested"].(map[string]any)
	assert.Equal(t, "has "+orTestAPIKey, nested["v"])

	// An empty key returns the value unchanged (nothing to scrub).
	assert.Equal(t, "clean", openrouter.RedactKeyDeep("clean", ""))
	// Non-container values pass through.
	assert.Equal(t, 42, openrouter.RedactKeyDeep(42, orTestAPIKey))
	assert.Equal(t, true, openrouter.RedactKeyDeep(true, orTestAPIKey))
}

func TestAssertNoSecretInPayloadEmptyKeyIsTypedError(t *testing.T) {
	err := openrouter.AssertNoSecretInPayload(map[string]any{"a": 1}, "")
	require.Error(t, err)
	var guardErr *openrouter.GatewayGuardError
	require.ErrorAs(t, err, &guardErr)
	assert.Equal(t,
		"assertNoSecretInPayload requires a non-empty key (an empty key would match every payload)",
		guardErr.Message,
	)
}

func TestAssertNoSecretInPayloadFallsBackToStringForUnserializablePayloads(t *testing.T) {
	// TS JSON.stringify throws → String(payload) fallback; Go json.Marshal
	// errors the same way.
	unserializable := map[string]any{"bad": func() {}}
	require.NoError(t, openrouter.AssertNoSecretInPayload(unserializable, orTestAPIKey))
	// The serialized-text fallback covers shapes the deep walk cannot
	// traverse (typed slices/maps) — the Go port of the TS JSON.stringify scan.
	typedValues := map[string]any{"fallbacks": []string{orTestAPIKey}}
	err := openrouter.AssertNoSecretInPayload(typedValues, orTestAPIKey)
	require.Error(t, err)
	var guardErr *openrouter.GatewayGuardError
	require.ErrorAs(t, err, &guardErr)
	assert.Equal(t, "API-key material detected in the serialized outgoing payload — request blocked before any network I/O (ARCH §10.7)", guardErr.Message)
	assert.Equal(t, "", guardErr.Location)
	// A nil payload passes (nothing to scan).
	require.NoError(t, openrouter.AssertNoSecretInPayload(nil, orTestAPIKey))
}
