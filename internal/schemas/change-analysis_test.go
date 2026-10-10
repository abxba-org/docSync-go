package schemas_test

// Ported from packages/schemas/test/*.ts assertions covering
// packages/schemas/src/v1/change-analysis.ts:
//   - registry.test.ts ("every example validates against its schema"),
//   - openrouter/test/orchestrator-decision-composed.test.ts
//     (`expect(CHANGE_ANALYSIS_SCHEMA.safeParse(ANALYSIS).success).toBe(true)`),
//   - core/test/analysis-plugin.test.ts (verbatim TypeScript stub coverage
//     entry),
//   - core/test/workspace-assemble.test.ts (canonicalJson golden bytes).
// Black-box: only exported API; strict decoding is exercised through JSON
// (StrictDecode semantics mirroring z.strictObject).

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// caJSON marshals v to JSON bytes (test helper; distinct from the sibling
// test files' helpers in this package).
func caJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

// caMutateJSON decodes JSON into a generic tree, applies mutate, and returns
// the re-encoded JSON — the Go analogue of the TS object-spread variants.
func caMutateJSON(t *testing.T, data []byte, mutate func(any)) []byte {
	t.Helper()
	var tree any
	require.NoError(t, json.Unmarshal(data, &tree))
	mutate(tree)
	return caJSON(t, tree)
}

// caIssues extracts the validation issues carried by err.
func caIssues(t *testing.T, err error) []schemas.Issue {
	t.Helper()
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Issues
}

// caParseIssues strictly parses data and returns the validation issues it
// produced (the parse must fail).
func caParseIssues(t *testing.T, data []byte) []schemas.Issue {
	t.Helper()
	_, err := schemas.ParseChangeAnalysis(data)
	return caIssues(t, err)
}

func caFind(issues []schemas.Issue, path []string, message string) bool {
	for _, iss := range issues {
		if assert.ObjectsAreEqual(path, iss.Path) && iss.Message == message {
			return true
		}
	}
	return false
}

func TestChangeAnalysisExampleValidates(t *testing.T) {
	require.Empty(t, schemas.ChangeAnalysisExample.Validate())

	parsed, err := schemas.ParseChangeAnalysis(caJSON(t, schemas.ChangeAnalysisExample))
	require.NoError(t, err)
	require.NotNil(t, parsed)
	assert.Equal(t, schemas.ChangeAnalysisSchemaID, parsed.Schema)
	assert.Equal(t, "acme/payments#8a73f2c..81af92e", parsed.RunID)
}

func TestChangeAnalysisRoundTripsByteStable(t *testing.T) {
	first := caJSON(t, schemas.ChangeAnalysisExample)

	var decoded schemas.ChangeAnalysis
	require.NoError(t, schemas.StrictDecode(first, &decoded))
	second := caJSON(t, decoded)

	assert.JSONEq(t, string(first), string(second))
	assert.Equal(t, string(first), string(second))
	require.Empty(t, decoded.Validate())
}

// wantChangeAnalysisCanonical is the golden canonicalJson pin of
// CHANGE_ANALYSIS_EXAMPLE (workspace-assemble.test.ts asserts
// `canonicalJson(CHANGE_ANALYSIS_EXAMPLE)`): keys sorted at every level,
// arrays in order, optional fields present only when the example sets them.
const wantChangeAnalysisCanonical = `{"analyzer_coverage":[{"analyzer":"typescript","note":"` +
	`stub: no AST/symbol extraction in V1 — tree-sitter symbol/type-level analysis lands in Phase 11 (F11-1); per-file symbol diffs are a declared V1 coverage gap` +
	`","status":"stub"},{"analyzer":"generic","status":"ok"}],"base_sha":"8a73f2c",` +
	`"classification_hints":[{"kind":"test_only","note":"all changed paths match src/**/*.test.ts"}],` +
	`"deep_analysis":{"reason":"no type-aware analyzer coverage for this language","status":"skipped"},` +
	`"dependencies_changed":false,` +
	`"files":[{"api_diff":false,"config_diff":false,"formatting_only":false,"generated":false,` +
	`"path":"src/payments/refund.ts","status":"modified","symbols_changed":true,` +
	`"symbols_detail":{"added":[],"changed":[{"changeKinds":["type"],"name":"idempotencyKey"}],"removed":[]},` +
	`"test_only":false},{"path":"src/payments/refund.test.ts","status":"added","test_only":true}],` +
	`"head_sha":"81af92e","repo":"acme/payments","run_id":"acme/payments#8a73f2c..81af92e",` +
	`"schema":"docsync.change-analysis/v1"}`

func TestChangeAnalysisExampleCanonicalJSONGolden(t *testing.T) {
	var tree any
	require.NoError(t, json.Unmarshal(caJSON(t, schemas.ChangeAnalysisExample), &tree))
	canonical, err := docutil.CanonicalJSON(tree)
	require.NoError(t, err)
	assert.Equal(t, wantChangeAnalysisCanonical, canonical)
}

func TestChangeAnalysisEnumValueSetsAreClosedAndOrdered(t *testing.T) {
	assert.Equal(t, []schemas.FileStatus{
		schemas.FileStatusAdded, schemas.FileStatusModified, schemas.FileStatusDeleted, schemas.FileStatusRenamed,
	}, schemas.FileStatusValues)
	assert.Equal(t, []schemas.ClassificationHintKind{
		schemas.ClassificationHintTestOnly, schemas.ClassificationHintFormattingOnly,
		schemas.ClassificationHintGenerated, schemas.ClassificationHintOther,
	}, schemas.ClassificationHintKindValues)
	assert.Equal(t, []schemas.DeepAnalysisStatus{
		schemas.DeepAnalysisPerformed, schemas.DeepAnalysisSkipped,
	}, schemas.DeepAnalysisStatusValues)
	assert.Equal(t, []schemas.AnalyzerName{
		schemas.AnalyzerNameTypeScript, schemas.AnalyzerNameGeneric,
	}, schemas.AnalyzerNameValues)
	assert.Equal(t, []schemas.AnalyzerStatus{
		schemas.AnalyzerStatusOK, schemas.AnalyzerStatusUnavailable, schemas.AnalyzerStatusStub,
	}, schemas.AnalyzerStatusValues)
	assert.Equal(t, []schemas.SymbolChangeKind{
		schemas.SymbolChangeSignature, schemas.SymbolChangeType, schemas.SymbolChangeVisibility,
	}, schemas.SymbolChangeKindValues)
}

func TestChangeAnalysisExamplePinsTypeScriptStubCoverage(t *testing.T) {
	// core/test/analysis-plugin.test.ts reads the stub coverage entry off the
	// example and pins the verbatim stub note (the schemas package cannot
	// import the analyzer: dependency direction).
	var stub *schemas.AnalyzerCoverage
	for i := range schemas.ChangeAnalysisExample.AnalyzerCoverage {
		if schemas.ChangeAnalysisExample.AnalyzerCoverage[i].Analyzer == schemas.AnalyzerNameTypeScript {
			stub = &schemas.ChangeAnalysisExample.AnalyzerCoverage[i]
		}
	}
	require.NotNil(t, stub, "the example must declare the typescript analyzer coverage entry")
	assert.Equal(t, schemas.AnalyzerStatusStub, stub.Status)
	require.NotNil(t, stub.Note)
	assert.Equal(t,
		"stub: no AST/symbol extraction in V1 — tree-sitter symbol/type-level analysis lands in Phase 11 (F11-1); per-file symbol diffs are a declared V1 coverage gap",
		*stub.Note)
}

func TestChangeAnalysisRejectsUnknownKeysAtEveryLevel(t *testing.T) {
	base := caJSON(t, schemas.ChangeAnalysisExample)

	// Root level.
	_, err := schemas.ParseChangeAnalysis(caMutateJSON(t, base, func(tree any) {
		tree.(map[string]any)["extra"] = float64(1)
	}))
	require.Error(t, err)

	// Changed-file level.
	_, err = schemas.ParseChangeAnalysis(caMutateJSON(t, base, func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		files[0].(map[string]any)["extra"] = float64(1)
	}))
	require.Error(t, err)

	// symbols_detail level.
	_, err = schemas.ParseChangeAnalysis(caMutateJSON(t, base, func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		detail := files[0].(map[string]any)["symbols_detail"].(map[string]any)
		detail["extra"] = float64(1)
	}))
	require.Error(t, err)

	// Symbol change detail level.
	_, err = schemas.ParseChangeAnalysis(caMutateJSON(t, base, func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		detail := files[0].(map[string]any)["symbols_detail"].(map[string]any)
		changed := detail["changed"].([]any)
		changed[0].(map[string]any)["extra"] = float64(1)
	}))
	require.Error(t, err)

	// classification_hints entry level.
	_, err = schemas.ParseChangeAnalysis(caMutateJSON(t, base, func(tree any) {
		hints := tree.(map[string]any)["classification_hints"].([]any)
		hints[0].(map[string]any)["extra"] = float64(1)
	}))
	require.Error(t, err)

	// deep_analysis level.
	_, err = schemas.ParseChangeAnalysis(caMutateJSON(t, base, func(tree any) {
		tree.(map[string]any)["deep_analysis"].(map[string]any)["extra"] = float64(1)
	}))
	require.Error(t, err)

	// analyzer_coverage entry level.
	_, err = schemas.ParseChangeAnalysis(caMutateJSON(t, base, func(tree any) {
		coverage := tree.(map[string]any)["analyzer_coverage"].([]any)
		coverage[1].(map[string]any)["extra"] = float64(1)
	}))
	require.Error(t, err)
}

func TestChangeAnalysisRejectsWrongSchemaLiteral(t *testing.T) {
	mutated := caMutateJSON(t, caJSON(t, schemas.ChangeAnalysisExample), func(tree any) {
		tree.(map[string]any)["schema"] = "docsync.change-analysis/v2"
	})
	issues := caParseIssues(t, mutated)
	assert.True(t, caFind(issues, []string{"schema"},
		`Invalid literal value, expected "docsync.change-analysis/v1"`),
		"expected the z.literal schema issue, got %v", issues)
}

func TestChangeAnalysisRequiresNonEmptyIdentifiers(t *testing.T) {
	const want = "String must contain at least 1 character(s)"
	for _, field := range []string{"run_id", "repo", "base_sha", "head_sha"} {
		path := []string{field}
		mutated := caMutateJSON(t, caJSON(t, schemas.ChangeAnalysisExample), func(tree any) {
			tree.(map[string]any)[field] = ""
		})
		var decoded schemas.ChangeAnalysis
		require.NoError(t, json.Unmarshal(mutated, &decoded))
		issues := decoded.Validate()
		assert.True(t, caFind(issues, path, want), "field %q: expected min(1) issue, got %v", field, issues)
	}

	// files[].path — zod z.string().min(1) at files.0.path.
	mutated := caMutateJSON(t, caJSON(t, schemas.ChangeAnalysisExample), func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		files[0].(map[string]any)["path"] = ""
	})
	issues := caParseIssues(t, mutated)
	assert.True(t, caFind(issues, []string{"files", "0", "path"}, want),
		"expected the files.0.path min(1) issue, got %v", issues)
}

func TestChangeAnalysisRejectsInvalidFileStatus(t *testing.T) {
	mutated := caMutateJSON(t, caJSON(t, schemas.ChangeAnalysisExample), func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		files[0].(map[string]any)["status"] = "unknown"
	})
	issues := caParseIssues(t, mutated)
	assert.True(t, caFind(issues, []string{"files", "0", "status"},
		"Invalid enum value. Expected 'added' | 'modified' | 'deleted' | 'renamed', received 'unknown'"),
		"expected the file-status enum issue, got %v", issues)
}

func TestChangeAnalysisRejectsInvalidNestedEnums(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(tree any)
		path    []string
		message string
	}{
		{
			name: "classification hint kind",
			mutate: func(tree any) {
				hints := tree.(map[string]any)["classification_hints"].([]any)
				hints[0].(map[string]any)["kind"] = "maybe"
			},
			path:    []string{"classification_hints", "0", "kind"},
			message: "Invalid enum value. Expected 'test_only' | 'formatting_only' | 'generated' | 'other', received 'maybe'",
		},
		{
			name: "deep analysis status",
			mutate: func(tree any) {
				tree.(map[string]any)["deep_analysis"].(map[string]any)["status"] = "partial"
			},
			path:    []string{"deep_analysis", "status"},
			message: "Invalid enum value. Expected 'performed' | 'skipped', received 'partial'",
		},
		{
			name: "analyzer name",
			mutate: func(tree any) {
				coverage := tree.(map[string]any)["analyzer_coverage"].([]any)
				coverage[1].(map[string]any)["analyzer"] = "rust"
			},
			path:    []string{"analyzer_coverage", "1", "analyzer"},
			message: "Invalid enum value. Expected 'typescript' | 'generic', received 'rust'",
		},
		{
			name: "analyzer status",
			mutate: func(tree any) {
				coverage := tree.(map[string]any)["analyzer_coverage"].([]any)
				coverage[1].(map[string]any)["status"] = "degraded"
			},
			path:    []string{"analyzer_coverage", "1", "status"},
			message: "Invalid enum value. Expected 'ok' | 'unavailable' | 'stub', received 'degraded'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := caMutateJSON(t, caJSON(t, schemas.ChangeAnalysisExample), tc.mutate)
			issues := caParseIssues(t, mutated)
			assert.True(t, caFind(issues, tc.path, tc.message), "expected %v, got %v", tc.path, issues)
		})
	}
}

func TestChangeAnalysisSymbolDetailValidation(t *testing.T) {
	base := caJSON(t, schemas.ChangeAnalysisExample)

	// changeKinds has min(1).
	empty := caMutateJSON(t, base, func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		detail := files[0].(map[string]any)["symbols_detail"].(map[string]any)
		changed := detail["changed"].([]any)
		changed[0].(map[string]any)["changeKinds"] = []any{}
	})
	issues := caParseIssues(t, empty)
	assert.True(t, caFind(issues, []string{"files", "0", "symbols_detail", "changed", "0", "changeKinds"},
		"Array must contain at least 1 element(s)"),
		"expected the changeKinds min(1) issue, got %v", issues)

	// changeKinds entries come from the closed SYMBOL_CHANGE_KIND set.
	badKind := caMutateJSON(t, base, func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		detail := files[0].(map[string]any)["symbols_detail"].(map[string]any)
		changed := detail["changed"].([]any)
		changed[0].(map[string]any)["changeKinds"] = []any{"naming"}
	})
	issues = caParseIssues(t, badKind)
	assert.True(t, caFind(issues, []string{"files", "0", "symbols_detail", "changed", "0", "changeKinds", "0"},
		"Invalid enum value. Expected 'signature' | 'type' | 'visibility', received 'naming'"),
		"expected the changeKinds enum issue, got %v", issues)

	// name is min(1).
	badName := caMutateJSON(t, base, func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		detail := files[0].(map[string]any)["symbols_detail"].(map[string]any)
		changed := detail["changed"].([]any)
		changed[0].(map[string]any)["name"] = ""
	})
	issues = caParseIssues(t, badName)
	assert.True(t, caFind(issues, []string{"files", "0", "symbols_detail", "changed", "0", "name"},
		"String must contain at least 1 character(s)"),
		"expected the changed.0.name min(1) issue, got %v", issues)

	// added/removed entries are min(1) strings; the arrays themselves may be empty.
	badAdded := caMutateJSON(t, base, func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		detail := files[0].(map[string]any)["symbols_detail"].(map[string]any)
		detail["added"] = []any{"ok", ""}
	})
	issues = caParseIssues(t, badAdded)
	assert.True(t, caFind(issues, []string{"files", "0", "symbols_detail", "added", "1"},
		"String must contain at least 1 character(s)"),
		"expected the added.1 min(1) issue, got %v", issues)
}

func TestChangeAnalysisOptionalFieldsMayBeAbsent(t *testing.T) {
	// The example's second file carries only path/status/test_only — every
	// other field is optional (zod .optional() ⇒ pointer + omitempty).
	var decoded schemas.ChangeAnalysis
	require.NoError(t, json.Unmarshal(caJSON(t, schemas.ChangeAnalysisExample), &decoded))
	require.Len(t, decoded.Files, 2)
	second := decoded.Files[1]
	assert.Equal(t, "src/payments/refund.test.ts", second.Path)
	assert.Equal(t, schemas.FileStatusAdded, second.Status)
	assert.Nil(t, second.Generated)
	assert.Nil(t, second.FormattingOnly)
	assert.Nil(t, second.SymbolsChanged)
	assert.Nil(t, second.APIDiff)
	assert.Nil(t, second.ConfigDiff)
	assert.Nil(t, second.SymbolsDetail)
	assert.Nil(t, second.OldPath)
	require.NotNil(t, second.TestOnly)
	assert.True(t, *second.TestOnly)

	// old_path is optional but min(1) when present.
	withOldPath := caMutateJSON(t, caJSON(t, schemas.ChangeAnalysisExample), func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		files[1].(map[string]any)["old_path"] = "src/payments/refund.spec.ts"
	})
	parsed, err := schemas.ParseChangeAnalysis(withOldPath)
	require.NoError(t, err)
	require.NotNil(t, parsed.Files[1].OldPath)
	assert.Equal(t, "src/payments/refund.spec.ts", *parsed.Files[1].OldPath)

	badOldPath := caMutateJSON(t, caJSON(t, schemas.ChangeAnalysisExample), func(tree any) {
		files := tree.(map[string]any)["files"].([]any)
		files[1].(map[string]any)["old_path"] = ""
	})
	issues := caParseIssues(t, badOldPath)
	assert.True(t, caFind(issues, []string{"files", "1", "old_path"},
		"String must contain at least 1 character(s)"),
		"expected the files.1.old_path min(1) issue, got %v", issues)
}
