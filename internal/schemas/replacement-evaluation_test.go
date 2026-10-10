package schemas_test

// Ported from packages/schemas/test/routing-policy-schema-p12-t20.test.ts
// (P12-T20; ARCH §10.1, §10.3; R2; D9) — the replacement-evaluation describe
// blocks, black-box with testify. The registry describe block
// (SCHEMA_REGISTRY / getSchema / zodToJsonSchema) is intentionally NOT
// ported — the registry barrel belongs to the registry unit's file.
//
// Helper names are replacementEval-prefixed: sibling black-box test files
// in package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ helpers */

func replacementEvalMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func replacementEvalWire(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

// replacementEvalClone renders a value as a generic JSON map so tests can
// spread copies ({...BASE, ...}).
func replacementEvalClone(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(replacementEvalMarshal(t, v), &m))
	return m
}

// replacementEvalMutate clones the report example, applies a mutation to
// the map, and returns the wire bytes.
func replacementEvalMutate(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := replacementEvalClone(t, schemas.ReplacementEvaluationReportExample)
	mutate(m)
	return replacementEvalWire(t, m)
}

func replacementEvalParse(t *testing.T, data []byte) error {
	t.Helper()
	_, err := schemas.ParseReplacementEvaluationReport(data)
	return err
}

// replacementEvalBindingParse exercises REPLACEMENT_MODEL_BINDING_SCHEMA
// alone (the TS suite parses standalone minimal bindings).
func replacementEvalBindingParse(t *testing.T, binding map[string]any) error {
	t.Helper()
	var b schemas.ReplacementModelBinding
	if err := schemas.StrictDecode(replacementEvalWire(t, binding), &b); err != nil {
		return err
	}
	if issues := b.Validate(); len(issues) > 0 {
		return schemas.NewValidationError(issues...)
	}
	return nil
}

// replacementEvalHashes mirrors the TS HASHES literal ('a'*64, 'b'*64,
// 'c'*64).
func replacementEvalHashes() map[string]any {
	return map[string]any{
		"change_analysis": strings.Repeat("a", 64),
		"candidate_set":   strings.Repeat("b", 64),
		"diff_subset":     strings.Repeat("c", 64),
	}
}

// replacementEvalIssueAt finds an issue whose path starts with path and
// whose message contains needle (mirrors the TS expectIssue helper).
func replacementEvalIssueAt(t *testing.T, err error, path []string, needle string) {
	t.Helper()
	require.Error(t, err)
	var ve *schemas.ValidationError
	require.ErrorAs(t, err, &ve)
	for _, iss := range ve.Issues {
		if len(iss.Path) < len(path) {
			continue
		}
		match := true
		for i, seg := range path {
			if iss.Path[i] != seg {
				match = false
				break
			}
		}
		if match && strings.Contains(iss.Message, needle) {
			return
		}
	}
	require.Failf(t, "issue not found", "expected an issue at path %v containing %q, got %v", path, needle, ve.Issues)
}

/* ------------------------------------------------------------------- tests */

func TestP12T20_ReportKeepsPromotedIDAndSampler(t *testing.T) {
	assert.Equal(t, "docsync.routing-replacement-report/v1", schemas.ReplacementEvaluationReportSchemaID)
	assert.Equal(t, "sha256-hex-threshold/v1", schemas.ReplacementShadowSampler)
}

func TestP12T20_ReportValidatesTheExampleAndRoundTrips(t *testing.T) {
	require.NoError(t, replacementEvalParse(t, replacementEvalMarshal(t, schemas.ReplacementEvaluationReportExample)))
	roundTripped := replacementEvalClone(t, schemas.ReplacementEvaluationReportExample)
	require.NoError(t, replacementEvalParse(t, replacementEvalWire(t, roundTripped)))
	assert.Equal(t, "pass", schemas.ReplacementEvaluationReportExample.Verdict)
}

func TestP12T20_ReportAcceptsMultiCaseMixedAgreementBelowThreshold(t *testing.T) {
	mixed := replacementEvalMutate(t, func(m map[string]any) {
		first := m["cases"].([]any)[0].(map[string]any)
		second := map[string]any{
			"caseId":       "case-002",
			"riskBucket":   "medium",
			"old_outcome":  map[string]any{"action": "patch", "abstained": false},
			"new_outcome":  map[string]any{"action": "review", "abstained": false},
			"agreed":       false,
			"input_hashes": replacementEvalHashes(),
		}
		m["cases"] = []any{first, second}
		m["agreement"] = map[string]any{"total": 2, "agreed": 1, "disagreed": 1, "rate": 0.5}
		m["verdict"] = "fail"
	})
	require.NoError(t, replacementEvalParse(t, mixed))
}

func TestP12T20_ReportRejectsMalformedReportsFailClosed(t *testing.T) {
	invalid := [][]byte{
		replacementEvalMutate(t, func(m map[string]any) {
			m["schema"] = "docsync.routing-replacement-report/v2"
		}),
		replacementEvalMutate(t, func(m map[string]any) {
			m["kind"] = "replacement_model_canary"
		}),
		replacementEvalMutate(t, func(m map[string]any) { m["extra"] = true }),
		replacementEvalMutate(t, func(m map[string]any) { m["role"] = "" }),
		replacementEvalMutate(t, func(m map[string]any) { m["cases"] = []any{} }),
		replacementEvalMutate(t, func(m map[string]any) {
			bindings := m["bindings"].(map[string]any)
			newBinding := bindings["new"].(map[string]any)
			oldBinding := bindings["old"].(map[string]any)
			newBinding["primary"] = oldBinding["primary"]
		}),
		replacementEvalMutate(t, func(m map[string]any) {
			m["policy"] = map[string]any{"production_version": 2, "candidate_version": 2}
		}),
		replacementEvalMutate(t, func(m map[string]any) {
			m["policy"] = map[string]any{"production_version": 0, "candidate_version": 3}
		}),
		replacementEvalMutate(t, func(m map[string]any) {
			m["agreement"] = map[string]any{"total": 2, "agreed": 1, "disagreed": 0, "rate": 1}
		}),
		replacementEvalMutate(t, func(m map[string]any) {
			m["agreement"] = map[string]any{"total": 1, "agreed": 1, "disagreed": 0, "rate": 0.5}
		}),
		replacementEvalMutate(t, func(m map[string]any) {
			m["verdict"] = "pass"
			m["min_agreement_rate"] = 0.5
			m["agreement"] = map[string]any{"total": 1, "agreed": 0, "disagreed": 1, "rate": 0}
		}),
		replacementEvalMutate(t, func(m map[string]any) {
			m["verdict"] = "fail"
			m["min_agreement_rate"] = 1
			m["agreement"] = map[string]any{"total": 1, "agreed": 1, "disagreed": 0, "rate": 1}
		}),
		replacementEvalMutate(t, func(m map[string]any) { m["min_agreement_rate"] = 1.5 }),
		replacementEvalMutate(t, func(m map[string]any) {
			m["created_at"] = "2026-10-23T00:00:00.000Z"
			m["expires_at"] = "2026-10-09T00:00:00.000Z"
		}),
		replacementEvalMutate(t, func(m map[string]any) {
			m["cases"].([]any)[0].(map[string]any)["riskBucket"] = "critical"
		}),
		replacementEvalMutate(t, func(m map[string]any) { m["port_id"] = "" }),
		replacementEvalMutate(t, func(m map[string]any) {
			m["cases"].([]any)[0].(map[string]any)["agreed"] = false
		}),
	}
	for i, report := range invalid {
		assert.Error(t, replacementEvalParse(t, report), "invalid report #%d must reject", i)
	}
}

func TestP12T20_ReportSuperRefineMessagesMatchTheTSSuperRefine(t *testing.T) {
	err := replacementEvalParse(t, replacementEvalMutate(t, func(m map[string]any) {
		bindings := m["bindings"].(map[string]any)
		bindings["new"].(map[string]any)["primary"] = bindings["old"].(map[string]any)["primary"]
	}))
	replacementEvalIssueAt(t, err, []string{"bindings", "new", "primary"},
		"a replacement evaluation compares TWO bindings — the old and new primaries must differ")

	err = replacementEvalParse(t, replacementEvalMutate(t, func(m map[string]any) {
		m["policy"] = map[string]any{"production_version": 2, "candidate_version": 2}
	}))
	replacementEvalIssueAt(t, err, []string{"policy", "candidate_version"},
		"a replacement evaluation compares TWO policy versions — production and candidate must differ (R2)")

	err = replacementEvalParse(t, replacementEvalMutate(t, func(m map[string]any) {
		m["agreement"] = map[string]any{"total": 2, "agreed": 1, "disagreed": 0, "rate": 1}
	}))
	replacementEvalIssueAt(t, err, []string{"agreement", "disagreed"},
		"the agreement block must be internally consistent — agreed + disagreed === total")

	err = replacementEvalParse(t, replacementEvalMutate(t, func(m map[string]any) {
		m["agreement"] = map[string]any{"total": 2, "agreed": 1, "disagreed": 1, "rate": 0.5}
	}))
	replacementEvalIssueAt(t, err, []string{"agreement", "total"},
		"the agreement total must equal the number of recorded cases")

	err = replacementEvalParse(t, replacementEvalMutate(t, func(m map[string]any) {
		m["cases"].([]any)[0].(map[string]any)["agreed"] = false
	}))
	replacementEvalIssueAt(t, err, []string{"agreement", "agreed"},
		"the agreed count must equal the agreed cases in the report")

	err = replacementEvalParse(t, replacementEvalMutate(t, func(m map[string]any) {
		m["verdict"] = "pass"
		m["min_agreement_rate"] = 0.5
		m["agreement"] = map[string]any{"total": 1, "agreed": 0, "disagreed": 1, "rate": 0}
	}))
	replacementEvalIssueAt(t, err, []string{"verdict"},
		"a 'pass' verdict requires an agreement rate at or above the minimum")

	err = replacementEvalParse(t, replacementEvalMutate(t, func(m map[string]any) {
		m["verdict"] = "fail"
		m["min_agreement_rate"] = 1
		m["agreement"] = map[string]any{"total": 1, "agreed": 1, "disagreed": 0, "rate": 1}
	}))
	replacementEvalIssueAt(t, err, []string{"verdict"},
		"a 'fail' verdict requires an agreement rate below the minimum")

	err = replacementEvalParse(t, replacementEvalMutate(t, func(m map[string]any) {
		m["created_at"] = "2026-10-23T00:00:00.000Z"
		m["expires_at"] = "2026-10-09T00:00:00.000Z"
	}))
	replacementEvalIssueAt(t, err, []string{"expires_at"},
		"expires_at must be strictly after created_at (time-boxed canary)")
}

func TestP12T20_ReportKeepsTheMinimalBindingShapeStrict(t *testing.T) {
	require.NoError(t, replacementEvalBindingParse(t, map[string]any{"primary": "openai/gpt-5.1"}))
	require.NoError(t, replacementEvalBindingParse(t, map[string]any{
		"primary":   "openai/gpt-5.1",
		"fallbacks": []any{"openai/gpt-5.1-mini"},
	}))
	assert.Error(t, replacementEvalBindingParse(t, map[string]any{"primary": ""}))
	assert.Error(t, replacementEvalBindingParse(t, map[string]any{"model": "openai/gpt-5.1"}))
}
