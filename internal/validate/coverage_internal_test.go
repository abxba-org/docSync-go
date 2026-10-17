package validate

// Internal coverage for the pure AST/diagnostic helpers whose flat branches
// are not reachable through the valid-manifest happy paths (walk fallbacks,
// malformed message shapes, diagnostic ordering ties).

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

func validateMustParse(t *testing.T, text string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(text), &doc))
	return &doc
}

func TestCOV_INTERNAL_VALIDATE_nodeWalkHelpers(t *testing.T) {
	doc := validateMustParse(t, "documents:\n  - path: docs/a.md\n    mode: propose\n")

	// rootOf on a non-document node returns the node itself.
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Value: "x"}
	assert.Same(t, scalar, rootOf(scalar))

	// nodeAtPath: sequence index failures and non-existing keys yield nil.
	assert.Nil(t, nodeAtPath(doc, []string{"documents", "seven", "mode"}))
	assert.Nil(t, nodeAtPath(doc, []string{"documents", "0", "missing"}))
	assert.Nil(t, nodeAtPath(&yaml.Node{Kind: yaml.ScalarNode}, []string{"x"}))
	assert.Nil(t, nodeAtPath(nil, []string{"x"}))

	// mappingValue / mappingHasKey on structures and non-mappings.
	root := rootOf(doc)
	assert.Nil(t, mappingValue(root, "documents/not-here"))
	assert.Nil(t, mappingValue(&yaml.Node{Kind: yaml.ScalarNode}, "k"))
	assert.Nil(t, mappingValue(nil, "k"))
	assert.False(t, mappingHasKey(&yaml.Node{Kind: yaml.ScalarNode}, "k"))
	assert.False(t, mappingHasKey(nil, "k"))
	assert.True(t, mappingHasKey(root, "documents"))

	// entryNodeForPath prefix guards.
	assert.Nil(t, entryNodeForPath(doc, []string{}))
	assert.Nil(t, entryNodeForPath(doc, []string{"defaults", "1"}))
	entry := entryNodeForPath(doc, []string{"documents", "0", "mode"})
	require.NotNil(t, entry)
}

func TestCOV_INTERNAL_VALIDATE_positionWalkEdges(t *testing.T) {
	doc := validateMustParse(t, "documents:\n  - path: docs/a.md\n")

	// A missing-deep path falls back through the nearest enclosing node.
	pos := positionForPath(doc, []string{"documents", "0", "nope", "deep"})
	assert.Equal(t, 2, pos.line)

	// A root with no source lines reports the file-level fallback position.
	noLines := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	pos = positionForPath(noLines, []string{"any"})
	assert.Equal(t, 1, pos.line)
	assert.True(t, pos.fallback)
}

func TestCOV_INTERNAL_VALIDATE_messageShapes(t *testing.T) {
	assert.Equal(t, "abc", firstSemicolonPart("abc"))
	assert.Equal(t, "abc", firstSemicolonPart("abc;def"))
	assert.Equal(t, "abc", firstSemicolonPart("abc;"))

	line, ok := lineFromYamlError(errors.New("yaml: line 12: bad"))
	assert.True(t, ok)
	assert.Equal(t, 12, line)
	line, ok = lineFromYamlError(errors.New("plain error"))
	assert.False(t, ok)
	assert.Equal(t, 1, line)
	line, ok = lineFromYamlError(errors.New("yaml: line 999999999999999999999999999: too big"))
	assert.False(t, ok)
	assert.Equal(t, 1, line)

	received, ok := receivedEnumValue("Invalid enum. Expected 'a', received 'robotic'")
	assert.True(t, ok)
	assert.Equal(t, "robotic", received)
	_, ok = receivedEnumValue("no marker here")
	assert.False(t, ok)
	_, ok = receivedEnumValue("marker but no closing quote, received 'x")
	assert.False(t, ok)
}

func TestCOV_INTERNAL_VALIDATE_diagnosticOrdering(t *testing.T) {
	a := Diagnostic{Line: 1, Column: 1, Code: "B", File: "f", Message: "m"}
	assert.Equal(t, 0, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 1, Code: "B", File: "f", Message: "m"}))
	assert.Less(t, compareManifestDiagnostics(a, Diagnostic{Line: 2}), 0)
	assert.Greater(t, compareManifestDiagnostics(a, Diagnostic{Line: 0}), 0)
	assert.Less(t, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 2}), 0)
	assert.Greater(t, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 0}), 0)
	assert.Greater(t, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 1, Code: "A"}), 0)
	assert.Less(t, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 1, Code: "Z"}), 0)
	assert.Greater(t, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 1, Code: "B", File: "a"}), 0)
	assert.Less(t, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 1, Code: "B", File: "z"}), 0)
	assert.Greater(t, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 1, Code: "B", File: "f", Message: "a"}), 0)
	assert.Less(t, compareManifestDiagnostics(a, Diagnostic{Line: 1, Column: 1, Code: "B", File: "f", Message: "z"}), 0)
}

func TestCOV_INTERNAL_VALIDATE_checkFloorsAdvisoryOrdering(t *testing.T) {
	reviewed := false
	floors := []schemas.OrgFloor{
		{Kind: schemas.FloorKindReportOnlyUntilReviewed, Pattern: "a/x.md", Reviewed: &reviewed},
		{Kind: schemas.FloorKindReportOnlyUntilReviewed, Pattern: "a/**", Reviewed: &reviewed},
	}
	result, err := CheckFloors("a/x.md", schemas.DocModeSuggest, floors)
	require.NoError(t, err)
	require.Len(t, result.Advisories, 2)
	assert.Equal(t, "a/**", result.Advisories[0].Floor.Pattern)
	assert.Equal(t, "a/x.md", result.Advisories[1].Floor.Pattern)
}

func TestCOV_INTERNAL_VALIDATE_floorMatchingEdges(t *testing.T) {
	matched, err := floorMatchesPath("docs/**", "docs/x.md")
	require.NoError(t, err)
	assert.True(t, matched)
	matched, err = floorMatchesPath("docs/**", "docs")
	require.NoError(t, err)
	assert.True(t, matched)
	matched, err = floorMatchesPath("docs/**", "other/x")
	require.NoError(t, err)
	assert.False(t, matched)
	matched, err = floorMatchesPath("docs", "docs/x.md")
	require.NoError(t, err)
	assert.True(t, matched)
	matched, err = floorMatchesPath("**", "anything")
	require.NoError(t, err)
	assert.True(t, matched)

	matched, err = floorMatchesPath("a/b", "a/b/c.md")
	require.NoError(t, err)
	assert.True(t, matched)
	matched, err = floorMatchesPath("a/b", "a/bc.md")
	require.NoError(t, err)
	assert.False(t, matched)
}
