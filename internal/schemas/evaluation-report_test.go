package schemas_test

// Ported from packages/schemas/test/evaluation-report.test.ts (black-box,
// testify).
//
// Covers EVALUATION_REPORT_SCHEMA (P10-T01; ARCH §9 metrics; CTX §35; P10-T24
// R5 verifier-judge disagreement): the validated example, the literal schema
// tag, the 0..1 unit-interval bounds, the non-negative minimality
// magnitudes, the positive integer sample_size, the optional
// baseline_version/objective_score, the UTC computed_at (non-UTC offsets
// rejected), and strict unknown-key rejection (OD-R01). The golden wire
// bytes pin the example's serializer output.
//
// Helper names are evalReport-prefixed: sibling black-box test files in
// package schemas_test are written concurrently by other delegates, so
// shared generic helper names would redeclare.

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evalReportWire marshals EvaluationReportExample() to JSON, applies the
// mutation (the TS suite does `{...EVALUATION_REPORT_EXAMPLE, ...}`), and
// re-serializes.
func evalReportWire(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(schemas.EvaluationReportExample())
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

// evalReportParseIssues strictly parses data and returns the validation
// issues it produced (the parse must fail).
func evalReportParseIssues(t *testing.T, data []byte) []schemas.Issue {
	t.Helper()
	_, err := schemas.ParseEvaluationReport(data)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Issues
}

// evalReportFind reports whether issues carry the exact path+message pair
// (the zod addIssue shape).
func evalReportFind(issues []schemas.Issue, path []string, message string) bool {
	for _, iss := range issues {
		if assert.ObjectsAreEqual(path, iss.Path) && iss.Message == message {
			return true
		}
	}
	return false
}

// evalReportPathHas reports whether any issue path includes segment (TS:
// issue.path.join('.').includes(<segment>)).
func evalReportPathHas(issues []schemas.Issue, segment string) bool {
	for _, iss := range issues {
		for _, seg := range iss.Path {
			if seg == segment {
				return true
			}
		}
	}
	return false
}

// wantEvaluationReportJSON is the golden serializer pin of
// EVALUATION_REPORT_EXAMPLE (wire keys in TS declaration order).
const wantEvaluationReportJSON = `{"schema":"docsync.evaluation-report/v1",` +
	`"policy_version":17,"baseline_version":16,"split_id":"frozen-2026-10","sample_size":120,` +
	`"metrics":{"detection_recall":0.94,"precision":0.91,"patch_correctness":0.88,` +
	`"minimality":{"mean_edit_distance":42.5,"mean_hunk_count":1.3},` +
	`"false_update_rate":0.02,"abstention_quality":0.86},` +
	`"abstain_health":{"abstain_rate":0.12,"override_rate":0.03},` +
	`"verifier_disagreement_rate":0.05,"objective_score":0.9,` +
	`"computed_at":"2026-10-05T12:00:00Z"}`

func TestEvaluationReportSchema_ValidatesTheExampleReport(t *testing.T) {
	example := schemas.EvaluationReportExample()
	require.Empty(t, example.Validate())
	parsed, err := schemas.ParseEvaluationReport(evalReportWire(t, nil))
	require.NoError(t, err)
	assert.Equal(t, schemas.EvaluationReportExample(), *parsed)
}

func TestEvaluationReportSchema_ExampleSerializesToGoldenWireBytes(t *testing.T) {
	b, err := json.Marshal(schemas.EvaluationReportExample())
	require.NoError(t, err)
	assert.Equal(t, wantEvaluationReportJSON, string(b))
}

func TestEvaluationReportSchema_ExampleRoundTripsByteStable(t *testing.T) {
	first, err := json.Marshal(schemas.EvaluationReportExample())
	require.NoError(t, err)
	parsed, err := schemas.ParseEvaluationReport(first)
	require.NoError(t, err)
	second, err := json.Marshal(parsed)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
}

func TestEvaluationReportSchema_CarriesTheLiteralSchemaTag(t *testing.T) {
	assert.Equal(t, "docsync.evaluation-report/v1", schemas.EvaluationReportSchemaID)
	issues := evalReportParseIssues(t, evalReportWire(t, func(m map[string]any) {
		m["schema"] = "docsync.evaluation-report/v2"
	}))
	assert.True(t, evalReportFind(issues, []string{"schema"},
		`Invalid literal value, expected "docsync.evaluation-report/v1"`),
		"expected the z.literal schema issue, got %v", issues)
}

func TestEvaluationReportSchema_ParsesWithAllMetricsAtTheBounds(t *testing.T) {
	data := evalReportWire(t, func(m map[string]any) {
		m["metrics"] = map[string]any{
			"detection_recall":   1,
			"precision":          0,
			"patch_correctness":  1,
			"minimality":         map[string]any{"mean_edit_distance": 0, "mean_hunk_count": 0},
			"false_update_rate":  0,
			"abstention_quality": 1,
		}
		m["abstain_health"] = map[string]any{"abstain_rate": 0, "override_rate": 1}
		m["verifier_disagreement_rate"] = 0
		m["objective_score"] = 0
	})
	parsed, err := schemas.ParseEvaluationReport(data)
	require.NoError(t, err)
	require.NotNil(t, parsed.ObjectiveScore)
	assert.Equal(t, 0.0, *parsed.ObjectiveScore)
}

func TestEvaluationReportSchema_RefusesEveryOutOfRangeBoundedMetric(t *testing.T) {
	// Ported verbatim from the TS table, including its mutation shape
	// (`bad[path[0]] = { ...bad[path[0]], [path[1]]: 1.5 }`): the loop keys the
	// replacement on path[1] even for the three-segment minimality paths (TS
	// therefore replaces metrics.minimality with the number 1.5) and for the
	// one-segment paths (the number field becomes an object). Each row is
	// refused exactly as the TS suite refuses it.
	paths := [][]string{
		{"metrics", "detection_recall"},
		{"metrics", "precision"},
		{"metrics", "patch_correctness"},
		{"metrics", "minimality", "mean_edit_distance"},
		{"metrics", "minimality", "mean_hunk_count"},
		{"metrics", "false_update_rate"},
		{"metrics", "abstention_quality"},
		{"abstain_health", "abstain_rate"},
		{"abstain_health", "override_rate"},
		{"verifier_disagreement_rate"},
		{"objective_score"},
	}
	for _, path := range paths {
		data := evalReportWire(t, func(m map[string]any) {
			parent, _ := m[path[0]].(map[string]any)
			repl := make(map[string]any, len(parent)+1)
			for k, v := range parent {
				repl[k] = v
			}
			key := path[0]
			if len(path) > 1 {
				key = path[1]
			}
			repl[key] = 1.5
			m[path[0]] = repl
		})
		_, err := schemas.ParseEvaluationReport(data)
		require.Error(t, err, "path %v must be refused", path)
	}
}

func TestEvaluationReportSchema_RefusesOutOfRangeRatesWithZodMessages(t *testing.T) {
	// The genuinely bounded rows of the out-of-range table, pinned to zod's
	// exact message and issue path.
	cases := []struct {
		name   string
		mutate func(map[string]any)
		path   []string
	}{
		{"metrics.detection_recall above 1", func(m map[string]any) {
			m["metrics"].(map[string]any)["detection_recall"] = 1.5
		}, []string{"metrics", "detection_recall"}},
		{"metrics.abstention_quality below 0", func(m map[string]any) {
			m["metrics"].(map[string]any)["abstention_quality"] = -0.5
		}, []string{"metrics", "abstention_quality"}},
		{"abstain_health.abstain_rate above 1", func(m map[string]any) {
			m["abstain_health"].(map[string]any)["abstain_rate"] = 1.5
		}, []string{"abstain_health", "abstain_rate"}},
		{"verifier_disagreement_rate above 1", func(m map[string]any) {
			m["verifier_disagreement_rate"] = 1.5
		}, []string{"verifier_disagreement_rate"}},
		{"objective_score above 1", func(m map[string]any) {
			m["objective_score"] = 1.5
		}, []string{"objective_score"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := evalReportParseIssues(t, evalReportWire(t, tc.mutate))
			assert.True(t, evalReportFind(issues, tc.path, "Number must be greater than or equal to 0") ||
				evalReportFind(issues, tc.path, "Number must be less than or equal to 1"),
				"expected the unit-interval issue at %v, got %v", tc.path, issues)
		})
	}
}

func TestEvaluationReportSchema_RefusesANegativeMinimalityMagnitude(t *testing.T) {
	data := evalReportWire(t, func(m map[string]any) {
		m["metrics"].(map[string]any)["minimality"] = map[string]any{
			"mean_edit_distance": -1,
			"mean_hunk_count":    1,
		}
	})
	issues := evalReportParseIssues(t, data)
	assert.True(t, evalReportPathHas(issues, "mean_edit_distance"),
		"expected an issue on the mean_edit_distance path, got %v", issues)
	assert.True(t, evalReportFind(issues, []string{"metrics", "minimality", "mean_edit_distance"},
		"Number must be greater than or equal to 0"),
		"expected the min(0) issue at metrics.minimality.mean_edit_distance, got %v", issues)
}

func TestEvaluationReportSchema_RefusesANonPositiveSampleSizeAndANonIntegerOne(t *testing.T) {
	_, err := schemas.ParseEvaluationReport(evalReportWire(t, func(m map[string]any) {
		m["sample_size"] = 0
	}))
	require.Error(t, err)

	_, err = schemas.ParseEvaluationReport(evalReportWire(t, func(m map[string]any) {
		m["sample_size"] = 1.5
	}))
	require.Error(t, err)
}

func TestEvaluationReportSchema_AllowsOmittingBaselineVersionAndObjectiveScore(t *testing.T) {
	data := evalReportWire(t, func(m map[string]any) {
		delete(m, "baseline_version")
		delete(m, "objective_score")
	})
	parsed, err := schemas.ParseEvaluationReport(data)
	require.NoError(t, err)
	assert.Nil(t, parsed.BaselineVersion)
	assert.Nil(t, parsed.ObjectiveScore)
}

func TestEvaluationReportSchema_RefusesANonUTCComputedAtAndUnknownKeys(t *testing.T) {
	_, err := schemas.ParseEvaluationReport(evalReportWire(t, func(m map[string]any) {
		m["computed_at"] = "2026-10-05T12:00:00+02:00"
	}))
	require.Error(t, err)

	data := evalReportWire(t, func(m map[string]any) { m["extra"] = true })
	_, err = schemas.ParseEvaluationReport(data)
	require.Error(t, err)
	// zod issue code 'unrecognized_keys' ⇒ Go DisallowUnknownFields; the
	// rejection outcome is preserved (message text is the stdlib's).
	assert.Contains(t, err.Error(), "unknown field")
}

func TestEvaluationReportSchema_RefusesNonPositiveVersionsAndNegativeHunkCount(t *testing.T) {
	// z.number().int().positive() for both version fields and z.number()
	// .min(0) for the minimality components — declared by the TS schema; the
	// TS suite pins sample_size only, so these pin the rest.
	issues := evalReportParseIssues(t, evalReportWire(t, func(m map[string]any) {
		m["policy_version"] = 0
	}))
	assert.True(t, evalReportFind(issues, []string{"policy_version"}, "Number must be greater than 0"),
		"expected the positive policy_version issue, got %v", issues)

	issues = evalReportParseIssues(t, evalReportWire(t, func(m map[string]any) {
		m["baseline_version"] = 0
	}))
	assert.True(t, evalReportFind(issues, []string{"baseline_version"}, "Number must be greater than 0"),
		"expected the positive baseline_version issue, got %v", issues)

	issues = evalReportParseIssues(t, evalReportWire(t, func(m map[string]any) {
		m["metrics"].(map[string]any)["minimality"].(map[string]any)["mean_hunk_count"] = -2
	}))
	assert.True(t, evalReportFind(issues, []string{"metrics", "minimality", "mean_hunk_count"},
		"Number must be greater than or equal to 0"),
		"expected the min(0) issue at metrics.minimality.mean_hunk_count, got %v", issues)
}
