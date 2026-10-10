package schemas_test

// Ported from packages/schemas/test/eval-case.test.ts (black-box, testify).
//
// Covers EVAL_CASE_SCHEMA (P10-T01; ARCH §9 evaluation protocol): the
// validated example, the literal schema tag, the closed split union, the
// exactly-one-of ground truth superRefine (label_id XOR is_true_negative),
// the ULID case_id/label_id, the 40-hex run_key shas (GIT_SHA_REGEX reuse,
// OD-2), the positive run_key policy_version, and strict unknown-key
// rejection (OD-R01). The golden wire bytes pin the example's serializer
// output (optional keys omitted when absent, matching TS .optional()).
//
// Helper names are evalCase-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evalCaseWire marshals EvalCaseExample() to JSON, applies the mutation (the
// TS suite does `{...EVAL_CASE_EXAMPLE, <field>: <bad>}`), and re-serializes.
func evalCaseWire(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(schemas.EvalCaseExample())
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	if mutate != nil {
		mutate(m)
	}
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

// evalCaseParseIssues strictly parses data and returns the validation issues
// it produced (the parse must fail).
func evalCaseParseIssues(t *testing.T, data []byte) []schemas.Issue {
	t.Helper()
	_, err := schemas.ParseEvalCase(data)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Issues
}

// evalCaseFind reports whether issues carry the exact path+message pair (the
// zod addIssue shape).
func evalCaseFind(issues []schemas.Issue, path []string, message string) bool {
	for _, iss := range issues {
		if assert.ObjectsAreEqual(path, iss.Path) && iss.Message == message {
			return true
		}
	}
	return false
}

// evalCasePathHas reports whether any issue path includes segment (TS:
// issue.path.includes(<segment>)).
func evalCasePathHas(issues []schemas.Issue, segment string) bool {
	for _, iss := range issues {
		for _, seg := range iss.Path {
			if seg == segment {
				return true
			}
		}
	}
	return false
}

// wantEvalCaseJSON is the golden serializer pin of EVAL_CASE_EXAMPLE (wire
// keys in TS declaration order; is_true_negative absent — the example sets
// label_id, the exactly-one-of XOR).
const wantEvalCaseJSON = `{"schema":"docsync.eval-case/v1",` +
	`"case_id":"01J9ZM6T0CZA6D8E0A2C4D6E8F",` +
	`"run_key":{"repo":"acme/widgets",` +
	`"base_sha":"3f9c2a7b1d4e5f60718293a4b5c6d7e8f9012a3b",` +
	`"head_sha":"9b1deb4d3b7d4bec9a2c0f1e8d7c6b5a49382716",` +
	`"manifest_version":"1","policy_version":17},` +
	`"label_id":"01J9ZM4R9BYZ5C7D9E1F3A5B7C",` +
	`"split":"frozen","expected_doc_paths":["docs/payments.md"]}`

func TestEvalCaseSchema_ValidatesTheExampleCase(t *testing.T) {
	example := schemas.EvalCaseExample()
	require.Empty(t, example.Validate())
	parsed, err := schemas.ParseEvalCase(evalCaseWire(t, nil))
	require.NoError(t, err)
	assert.Equal(t, schemas.EvalCaseExample(), *parsed)
}

func TestEvalCaseSchema_ExampleSerializesToGoldenWireBytes(t *testing.T) {
	b, err := json.Marshal(schemas.EvalCaseExample())
	require.NoError(t, err)
	assert.Equal(t, wantEvalCaseJSON, string(b))
}

func TestEvalCaseSchema_ExampleRoundTripsByteStable(t *testing.T) {
	first, err := json.Marshal(schemas.EvalCaseExample())
	require.NoError(t, err)
	parsed, err := schemas.ParseEvalCase(first)
	require.NoError(t, err)
	second, err := json.Marshal(parsed)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
}

func TestEvalCaseSchema_CarriesTheLiteralSchemaTag(t *testing.T) {
	assert.Equal(t, "docsync.eval-case/v1", schemas.EvalCaseSchemaID)
	issues := evalCaseParseIssues(t, evalCaseWire(t, func(m map[string]any) {
		m["schema"] = "docsync.eval-case/v2"
	}))
	assert.True(t, evalCaseFind(issues, []string{"schema"},
		`Invalid literal value, expected "docsync.eval-case/v1"`),
		"expected the z.literal schema issue, got %v", issues)
}

func TestEvalCaseSchema_ExposesTheClosedSplitUnion(t *testing.T) {
	// TS: expect([...EVAL_SPLIT_VALUES]).toEqual(['frozen','rolling']) and
	// Object.isFrozen(EVAL_SPLIT_VALUES) — Go pins the closed, ordered value
	// set via EvalSplitValues (the compile-time type is the frozen union).
	assert.Equal(t, []schemas.EvalSplit{
		schemas.EvalSplitFrozen, schemas.EvalSplitRolling,
	}, schemas.EvalSplitValues)
}

func TestEvalCaseSchema_ParsesALabeledCaseAndATrueNegativeCase(t *testing.T) {
	_, err := schemas.ParseEvalCase(evalCaseWire(t, nil))
	require.NoError(t, err)

	trueNegative := evalCaseWire(t, func(m map[string]any) {
		m["case_id"] = "01J9ZM7V0DZC8E0A2D4F6B8C0E"
		m["label_id"] = nil
		m["is_true_negative"] = true
	})
	_, err = schemas.ParseEvalCase(trueNegative)
	require.NoError(t, err)
}

func TestEvalCaseSchema_RefusesACaseWithNeitherLabelIDNorIsTrueNegative(t *testing.T) {
	issues := evalCaseParseIssues(t, evalCaseWire(t, func(m map[string]any) {
		m["label_id"] = nil
	}))
	assert.True(t, evalCasePathHas(issues, "label_id"),
		"expected an issue on label_id, got %v", issues)
	assert.True(t, evalCaseFind(issues, []string{"label_id"},
		"exactly one of label_id or is_true_negative is required (never both, never neither)"),
		"expected the superRefine message on label_id, got %v", issues)
}

func TestEvalCaseSchema_RefusesACaseWithBothLabelIDAndIsTrueNegative(t *testing.T) {
	issues := evalCaseParseIssues(t, evalCaseWire(t, func(m map[string]any) {
		m["is_true_negative"] = true
	}))
	assert.True(t, evalCasePathHas(issues, "is_true_negative"),
		"expected an issue on is_true_negative, got %v", issues)
	assert.True(t, evalCaseFind(issues, []string{"is_true_negative"},
		"exactly one of label_id or is_true_negative is required (never both, never neither)"),
		"expected the superRefine message on is_true_negative, got %v", issues)
}

func TestEvalCaseSchema_RefusesAnUnknownSplitValue(t *testing.T) {
	_, err := schemas.ParseEvalCase(evalCaseWire(t, func(m map[string]any) {
		m["split"] = "holdout"
	}))
	require.Error(t, err)
}

func TestEvalCaseSchema_RefusesNon40HexRunKeyShas(t *testing.T) {
	for _, key := range []string{"base_sha", "head_sha"} {
		issues := evalCaseParseIssues(t, evalCaseWire(t, func(m map[string]any) {
			m["run_key"].(map[string]any)[key] = "81af92e"
		}))
		assert.True(t, evalCasePathHas(issues, key),
			"key %q: expected an issue on the sha path, got %v", key, issues)
	}
}

func TestEvalCaseSchema_RefusesANonPositiveRunKeyPolicyVersion(t *testing.T) {
	issues := evalCaseParseIssues(t, evalCaseWire(t, func(m map[string]any) {
		m["run_key"].(map[string]any)["policy_version"] = 0
	}))
	assert.True(t, evalCaseFind(issues, []string{"run_key", "policy_version"},
		"Number must be greater than 0"),
		"expected the positive policy_version issue, got %v", issues)
}

func TestEvalCaseSchema_RefusesANonULIDCaseIDAndUnknownKeys(t *testing.T) {
	_, err := schemas.ParseEvalCase(evalCaseWire(t, func(m map[string]any) {
		m["case_id"] = "case_1"
	}))
	require.Error(t, err)

	data := evalCaseWire(t, func(m map[string]any) { m["extra"] = true })
	_, err = schemas.ParseEvalCase(data)
	require.Error(t, err)
	// zod issue code 'unrecognized_keys' ⇒ Go DisallowUnknownFields; the
	// rejection outcome is preserved (message text is the stdlib's).
	assert.Contains(t, err.Error(), "unknown field")
}

func TestEvalCaseSchema_RefusesANonULIDLabelID(t *testing.T) {
	// ULID_REGEX applies to label_id too (z.string().regex(ULID_REGEX)
	// .optional()) — the TS suite pins case_id only, so this pins the
	// declared label_id rule.
	issues := evalCaseParseIssues(t, evalCaseWire(t, func(m map[string]any) {
		m["label_id"] = "not-a-ulid"
	}))
	assert.True(t, evalCaseFind(issues, []string{"label_id"}, "Invalid"),
		"expected the ULID regex failure on label_id, got %v", issues)
}
