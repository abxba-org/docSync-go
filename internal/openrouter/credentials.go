package openrouter

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Env-only credential resolution and gateway guards (ARCH §10.7, invariants
// 8/10): OPENROUTER_API_KEY lives only in the orchestrator environment; models
// never hold credentials; keys are never sent to models in prompts, never
// logged, never returned in telemetry, and never stored in `.docsync/` files.

// OPENROUTER_API_KEY_ENV is the env var name — the ONLY credential source this
// layer reads.
const OPENROUTER_API_KEY_ENV = "OPENROUTER_API_KEY"

// REDACTED is the placeholder written over any detected secret occurrence.
const REDACTED = "[REDACTED]"

// CredentialResolution is the outcome of ResolveOpenRouterApiKey
// (TS OpenRouterCredentialResolution). OK false carries the fail-closed Reason.
type CredentialResolution struct {
	// OK is true when a key was resolved.
	OK bool
	// Key is the trimmed API key (only when OK).
	Key string
	// Reason explains why resolution failed (only when !OK).
	Reason string
}

// ResolveOpenRouterApiKey resolves the API key from an env map (shape of
// `process.env`; injectable for tests). Missing/empty keys resolve
// {OK: false, Reason} — callers fail closed (exit-code-2-style config error)
// BEFORE any AI stage. The key is trimmed so a trailing newline from a CI
// secret store cannot break auth.
func ResolveOpenRouterApiKey(env map[string]string) CredentialResolution {
	raw, ok := env[OPENROUTER_API_KEY_ENV]
	if !ok || strings.TrimSpace(raw) == "" {
		return CredentialResolution{
			OK: false,
			Reason: OPENROUTER_API_KEY_ENV + " is not set — credentials are env-only " +
				"(ARCH §10.7); the gateway cannot run without it (fail closed)",
		}
	}
	return CredentialResolution{OK: true, Key: strings.TrimSpace(raw)}
}

// AssertNoSecretInPayload is the gateway guard, run before EVERY outgoing
// request: if the API key string appears anywhere in the payload (string
// values, object keys, or the raw JSON text), the request is blocked with a
// typed *GatewayGuardError — the key never reaches the model as prompt data
// (invariant 8). Note: the key legitimately lives in the `Authorization`
// HEADER by protocol; the guard covers the BODY payload only.
//
// Map keys are scanned in sorted order so the reported location is
// deterministic (Go maps have no insertion order).
func AssertNoSecretInPayload(payload any, key string) error {
	if key == "" {
		return NewGatewayGuardError(
			"assertNoSecretInPayload requires a non-empty key (an empty key would match every payload)",
			"",
		)
	}
	located := findSecretLocation(payload, key, "$")
	if located != "" {
		return NewGatewayGuardError(
			fmt.Sprintf(
				"API-key material detected in the outgoing payload at %s — request blocked before any network I/O (ARCH §10.7)",
				located,
			),
			located,
		)
	}
	serialized, err := json.Marshal(payload)
	if err != nil {
		// TS: JSON.stringify throwing falls back to String(payload).
		serialized = []byte(fmt.Sprintf("%v", payload))
	}
	if strings.Contains(string(serialized), key) {
		return NewGatewayGuardError(
			"API-key material detected in the serialized outgoing payload — request blocked before any network I/O (ARCH §10.7)",
			"",
		)
	}
	return nil
}

// findSecretLocation walks the payload depth-first and returns the JSON path of
// the first value (string, array element, or object key) containing the key, or
// "" when nothing matches (TS findSecretLocation; "" == undefined).
func findSecretLocation(value any, key, path string) string {
	switch v := value.(type) {
	case string:
		if strings.Contains(v, key) {
			return path
		}
		return ""
	case []any:
		for index, entry := range v {
			hit := findSecretLocation(entry, key, fmt.Sprintf("%s[%d]", path, index))
			if hit != "" {
				return hit
			}
		}
		return ""
	case map[string]any:
		keys := make([]string, 0, len(v))
		for entryKey := range v {
			keys = append(keys, entryKey)
		}
		sort.Strings(keys)
		for _, entryKey := range keys {
			if strings.Contains(entryKey, key) {
				return path + "<key>"
			}
			hit := findSecretLocation(v[entryKey], key, path+"."+entryKey)
			if hit != "" {
				return hit
			}
		}
		return ""
	default:
		return ""
	}
}

// RedactKeyDeep deep-clones value, replacing every occurrence of key inside
// string values with REDACTED. It is the final defense before any payload
// reaches a telemetry sink — sinks receive REDACTED data only. Object KEY names
// are left untouched (telemetry records are client-built shapes with fixed
// keys); string VALUES are scrubbed. An empty key returns the value unchanged
// (nothing to scrub) — exactly as the TS redactKeyDeep.
func RedactKeyDeep[T any](value T, key string) T {
	if key == "" {
		return value
	}
	redacted := redactValue(value, key)
	if out, ok := redacted.(T); ok {
		return out
	}
	return value
}

// redactValue is the untyped worker behind RedactKeyDeep.
func redactValue(value any, key string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, key, REDACTED)
	case []any:
		out := make([]any, len(v))
		for index, entry := range v {
			out[index] = redactValue(entry, key)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for entryKey, entryValue := range v {
			out[entryKey] = redactValue(entryValue, key)
		}
		return out
	default:
		return value
	}
}
