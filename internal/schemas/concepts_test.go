package schemas_test

// Ported from packages/schemas/test/concepts.test.ts — docsync.concepts/v1
// (OD-R36). Black-box: only exported API; strict decoding is exercised
// through JSON (StrictDecode semantics).

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mutateJSON decodes JSON into a generic tree, applies mutate, and returns
// the re-encoded JSON — the Go analogue of the TS object-spread variants.
func mutateJSON(t *testing.T, data []byte, mutate func(any)) []byte {
	t.Helper()
	var tree any
	require.NoError(t, json.Unmarshal(data, &tree))
	mutate(tree)
	out, err := json.Marshal(tree)
	require.NoError(t, err)
	return out
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

func TestConceptsExampleValidates(t *testing.T) {
	example := schemas.ConceptsExample()
	require.Empty(t, example.Validate())
	_, err := schemas.ParseConcepts(mustJSON(t, example))
	require.NoError(t, err)
}

func TestConceptsRoundTripsByteStable(t *testing.T) {
	example := schemas.ConceptsExample()
	first := mustJSON(t, example)

	var decoded schemas.ConceptsConfig
	require.NoError(t, schemas.StrictDecode(first, &decoded))
	second := mustJSON(t, decoded)

	assert.JSONEq(t, string(first), string(second))
	assert.Equal(t, string(first), string(second))
	require.Empty(t, decoded.Validate())
}

func TestConceptsAcceptsClaimWithoutContentHash(t *testing.T) {
	var declaration schemas.ConceptsConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"schema": "docsync.concepts/v1",
		"docs": [
			{
				"path": "docs/payments.md",
				"claims": [
					{
						"id": "claim_1",
						"text": "Some documented fact.",
						"anchor": {"file": "docs/payments.md", "start_line": 3, "end_line": 4},
						"source": {"file": "src/payments/refund.ts", "blob_sha": "abc1234"}
					}
				]
			}
		]
	}`), &declaration))
	require.Empty(t, declaration.Validate())

	_, err := schemas.ParseConcepts([]byte(`{
		"schema": "docsync.concepts/v1",
		"docs": [
			{
				"path": "docs/payments.md",
				"claims": [
					{
						"id": "claim_1",
						"text": "Some documented fact.",
						"anchor": {"file": "docs/payments.md", "start_line": 3, "end_line": 4},
						"source": {"file": "src/payments/refund.ts", "blob_sha": "abc1234"}
					}
				]
			}
		]
	}`))
	require.NoError(t, err)
}

func TestConceptsRejectsUnknownKeysAtEveryLevel(t *testing.T) {
	base := mustJSON(t, schemas.ConceptsExample())

	// Root level.
	root := mutateJSON(t, base, func(tree any) {
		tree.(map[string]any)["extra"] = float64(1)
	})
	_, err := schemas.ParseConcepts(root)
	require.Error(t, err)

	// Doc entry level.
	docEntry := mutateJSON(t, base, func(tree any) {
		docs := tree.(map[string]any)["docs"].([]any)
		docs[0].(map[string]any)["extra"] = float64(1)
	})
	_, err = schemas.ParseConcepts(docEntry)
	require.Error(t, err)

	// Claim declaration level.
	claim := mutateJSON(t, base, func(tree any) {
		docs := tree.(map[string]any)["docs"].([]any)
		claims := docs[0].(map[string]any)["claims"].([]any)
		claims[0].(map[string]any)["extra"] = float64(1)
	})
	_, err = schemas.ParseConcepts(claim)
	require.Error(t, err)

	// Anchor level.
	anchor := mutateJSON(t, base, func(tree any) {
		docs := tree.(map[string]any)["docs"].([]any)
		claims := docs[0].(map[string]any)["claims"].([]any)
		claims[0].(map[string]any)["anchor"].(map[string]any)["extra"] = float64(1)
	})
	_, err = schemas.ParseConcepts(anchor)
	require.Error(t, err)
}

func TestConceptsRejectsAnchorEndLineBeforeStartLine(t *testing.T) {
	base := mustJSON(t, schemas.ConceptsExample())
	mutated := mutateJSON(t, base, func(tree any) {
		docs := tree.(map[string]any)["docs"].([]any)
		claims := docs[0].(map[string]any)["claims"].([]any)
		anchor := claims[0].(map[string]any)["anchor"].(map[string]any)
		anchor["start_line"] = float64(9)
		anchor["end_line"] = float64(3)
	})

	parsed, err := schemas.ParseConcepts(mutated)
	require.Error(t, err)
	_ = parsed

	var decoded schemas.ConceptsConfig
	require.NoError(t, json.Unmarshal(mutated, &decoded))
	issues := decoded.Validate()
	require.NotEmpty(t, issues)
	found := false
	for _, iss := range issues {
		if assert.ObjectsAreEqual([]string{"docs", "0", "claims", "0", "anchor", "end_line"}, iss.Path) &&
			iss.Message == "end_line must be greater than or equal to start_line" {
			found = true
		}
	}
	assert.True(t, found, "expected the end_line refinement issue at docs.0.claims.0.anchor.end_line, got %v", issues)
}

func TestConceptsRejectsUnknownContractKind(t *testing.T) {
	example := schemas.ConceptsExample()
	example.ContractKinds["magic"] = "quantum"

	issues := example.Validate()
	require.NotEmpty(t, issues)

	base := mustJSON(t, schemas.ConceptsExample())
	mutated := mutateJSON(t, base, func(tree any) {
		tree.(map[string]any)["contractKinds"].(map[string]any)["magic"] = "quantum"
	})
	_, err := schemas.ParseConcepts(mutated)
	require.Error(t, err)
}

func TestConceptsRejectsConceptEntryWithEmptyDocs(t *testing.T) {
	var decoded schemas.ConceptsConfig
	require.NoError(t, json.Unmarshal(mustJSON(t, schemas.ConceptsExample()), &decoded))
	decoded.Concepts = []schemas.ConceptEntry{{Name: "empty", Docs: []string{}}}

	issues := decoded.Validate()
	require.NotEmpty(t, issues)
	found := false
	for _, iss := range issues {
		if assert.ObjectsAreEqual([]string{"concepts", "0", "docs"}, iss.Path) &&
			iss.Message == "Array must contain at least 1 element(s)" {
			found = true
		}
	}
	assert.True(t, found, "expected the concepts.0.docs min-length issue, got %v", issues)
}

func TestConceptsRejectsNonRelativeOrTraversingDocPaths(t *testing.T) {
	for _, path := range []string{"/abs/path.md", "../escape.md", "a//b.md", ""} {
		var decoded schemas.ConceptsConfig
		require.NoError(t, json.Unmarshal(mustJSON(t, schemas.ConceptsExample()), &decoded))
		decoded.Docs = []schemas.ConceptsDocEntry{{Path: path, Contracts: []string{"c"}}}

		issues := decoded.Validate()
		assert.NotEmpty(t, issues, "path %q must be rejected", path)

		base := mustJSON(t, schemas.ConceptsExample())
		mutated := mutateJSON(t, base, func(tree any) {
			docs := tree.(map[string]any)["docs"].([]any)
			docs[0].(map[string]any)["path"] = path
		})
		_, err := schemas.ParseConcepts(mutated)
		assert.Error(t, err, "path %q must be rejected at parse", path)
	}
}
