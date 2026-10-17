package graph_test

// Coverage-gap tests for internal/graph: fail-closed parser branches, the
// line-index scan of the concepts.yml shapes, and the deterministic orphan
// edge ordering.

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gapManifest(paths ...string) schemas.Manifest {
	docs := make([]schemas.ManifestEntry, 0, len(paths))
	for _, p := range paths {
		docs = append(docs, schemas.ManifestEntry{Path: p, Sources: []string{"src/x.ts"}})
	}
	return schemas.Manifest{Documents: docs}
}

func TestCOV_GRAPH_LineIndexConceptsContractKindsAndHasLine(t *testing.T) {
	raw := `# concepts.yml (fixed shape)
docs:
  - path: "docs/payments.md"
  - path: docs/other.md
  - path: docs/ghost.md
concepts:
  - name: alpha
    docs:
      - docs/payments.md
      - docs/not-in-manifest.md
  - name: alpha
contractKinds:
  money: api_schema
  broken_schema: api_schema
`
	concepts := &schemas.ConceptsConfig{
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/payments.md", Contracts: []string{"pay", "pay"}},
			{Path: "docs/other.md"},
			{Path: "docs/ghost.md"},
		},
		Concepts: []schemas.ConceptEntry{
			{Name: "alpha", Docs: []string{"docs/payments.md", "docs/not-in-manifest.md"}},
			{Name: "alpha", Docs: []string{"docs/other.md"}},
		},
		ContractKinds: map[string]schemas.ContractKind{"money": "api_schema", "broken_schema": "bogus"},
	}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest:    gapManifest("docs/payments.md", "docs/other.md"),
		Concepts:    concepts,
		ConceptsRaw: raw,
	})

	kinds := []graph.GraphConfigErrorKind{}
	lines := 0
	for _, e := range g.Errors {
		kinds = append(kinds, e.Kind)
		if e.Line != nil {
			lines++
		}
	}
	assert.Contains(t, kinds, graph.GraphConfigErrDuplicateContractDecl)
	assert.Contains(t, kinds, graph.GraphConfigErrDuplicateConceptName)
	assert.Contains(t, kinds, graph.GraphConfigErrConceptReferencesUnknownDoc)
	assert.Contains(t, kinds, graph.GraphConfigErrUnknownContractKind)
	assert.Contains(t, kinds, graph.GraphConfigErrConceptsDocNotInManifest)
	assert.Equal(t, 5, lines, "every reported config error carries a best-effort line")
}

func TestCOV_GRAPH_ParseGraphFailClosedEdges(t *testing.T) {
	mapping := `{"doc":"d.md","sources":["s.ts"],"concepts":[],"contracts":[]}`

	t.Run("mapping doc not a string", func(t *testing.T) {
		_, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[{"doc":42}],"contractKinds":{},"errors":[]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "doc must be a non-empty string")
	})

	t.Run("sources present but not an array", func(t *testing.T) {
		_, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[{"doc":"d","sources":42}],"contractKinds":{},"errors":[]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sources must be an array")
	})

	t.Run("array entry not a string", func(t *testing.T) {
		_, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[{"doc":"d","sources":[1]}],"contractKinds":{},"errors":[]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-empty string")
	})

	t.Run("contractKinds not an object", func(t *testing.T) {
		_, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[],"contractKinds":42,"errors":[]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be an object")
	})

	t.Run("errors not an array", func(t *testing.T) {
		_, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[],"contractKinds":{},"errors":"nope"}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"errors" must be an array`)
	})

	t.Run("error entry not an object", func(t *testing.T) {
		_, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[],"contractKinds":{},"errors":[7]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "errors[0] must be an object")
	})

	t.Run("nil kind renders as null", func(t *testing.T) {
		_, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[],"contractKinds":{"k":null},"errors":[]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown kind "null"`)
	})

	t.Run("boolean kind renders as false", func(t *testing.T) {
		_, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[],"contractKinds":{},"errors":[{"kind":false,"path":"p","message":"m"}]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `.kind is unknown "false"`)
	})

	t.Run("round trip of the rich mapping", func(t *testing.T) {
		g, err := graph.ParseGraph(`{"schema":"docsync.graph/v1","mappings":[` + mapping + `],"contractKinds":{},"errors":[]}`)
		require.NoError(t, err)
		require.Len(t, g.Mappings, 1)
		assert.Equal(t, []string{"s.ts"}, g.Mappings[0].Sources)
	})
}
