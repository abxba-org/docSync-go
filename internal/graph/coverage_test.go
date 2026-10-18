package graph_test

// COVERAGE tests targeting statements/branches not already exercised by the
// ported graph suites. Black-box, deterministic, no network.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- error surfaces */

// TestCOV_GRAPH_ErrorSurfaces_ErrorAndUnwrap covers the Error()/Unwrap() seams
// of GraphEdgeInvalidError and GraphParseError (fail-closed typed errors).
func TestCOV_GRAPH_ErrorSurfaces_ErrorAndUnwrap(t *testing.T) {
	// GraphEdgeInvalidError: unknown kind.
	_, err := graph.ToGraphEdge(map[string]any{"kind": "quantum", "doc": "docs/x.md", "ref": "src/**"})
	var ge *graph.GraphEdgeInvalidError
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, graph.CodeGraphEdgeInvalid, ge.Code())
	assert.Equal(t, docutil.CodeOf(err), graph.CodeGraphEdgeInvalid)
	assert.Contains(t, ge.Error(), "(fail closed)")
	assert.NotNil(t, ge.Unwrap())
	assert.Equal(t, graph.GraphEdgeReasonUnknownKind, ge.Reason)
	assert.Equal(t, "quantum", ge.Kind)

	// GraphEdgeInvalidError: shape.
	_, err = graph.ToGraphEdge(map[string]any{"kind": "api", "doc": "", "ref": ""})
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, graph.GraphEdgeReasonShape, ge.Reason)
	assert.Equal(t, "", ge.Kind)
	assert.Contains(t, ge.Error(), "(fail closed)")

	// GraphParseError: malformed JSON.
	_, err = graph.ParseGraph("[")
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)
	assert.Equal(t, graph.CodeGraphParse, gpe.Code())
	assert.Equal(t, docutil.CodeOf(err), graph.CodeGraphParse)
	assert.Contains(t, gpe.Error(), "(fail closed)")
	assert.NotNil(t, gpe.Unwrap())
}

// TestCOV_GRAPH_ParseError_UnknownFieldAndBadLine covers parseError's
// unknown-field, empty path/message, and invalid line branches.
func TestCOV_GRAPH_ParseError_UnknownFieldAndBadLine(t *testing.T) {
	base := mustEnvelope(t)

	// unknown field on a config error.
	bad := cloneMap(t, base)
	bad["errors"] = []any{map[string]any{"kind": "concepts_doc_not_in_manifest", "path": "x", "message": "y", "bogus": 1}}
	_, err := graph.ParseGraph(mustJSON(t, bad))
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), "unknown field")

	// empty path.
	bad = cloneMap(t, base)
	bad["errors"] = []any{map[string]any{"kind": "concepts_doc_not_in_manifest", "path": "", "message": "y"}}
	_, err = graph.ParseGraph(mustJSON(t, bad))
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), "path must be a non-empty string")

	// empty message.
	bad = cloneMap(t, base)
	bad["errors"] = []any{map[string]any{"kind": "concepts_doc_not_in_manifest", "path": "x", "message": ""}}
	_, err = graph.ParseGraph(mustJSON(t, bad))
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), "message must be a non-empty string")

	// line is a non-integer float.
	bad = cloneMap(t, base)
	bad["errors"] = []any{map[string]any{"kind": "concepts_doc_not_in_manifest", "path": "x", "message": "y", "line": 1.5}}
	_, err = graph.ParseGraph(mustJSON(t, bad))
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), "line must be a positive integer")

	// line is below 1.
	bad = cloneMap(t, base)
	bad["errors"] = []any{map[string]any{"kind": "concepts_doc_not_in_manifest", "path": "x", "message": "y", "line": 0}}
	_, err = graph.ParseGraph(mustJSON(t, bad))
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), "line must be a positive integer")
}

// TestCOV_GRAPH_ParseSchema_jsStringVariants covers jsString's bool / float64 /
// default branches by supplying a non-string schema value.
func TestCOV_GRAPH_ParseSchema_jsStringVariants(t *testing.T) {
	makeBadSchema := func(schema any) string {
		env := cloneMap(t, mustEnvelope(t))
		env["schema"] = schema
		return mustJSON(t, env)
	}

	_, err := graph.ParseGraph(makeBadSchema(true))
	require.Error(t, err)
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)

	_, err = graph.ParseGraph(makeBadSchema(2))
	require.Error(t, err)
	require.ErrorAs(t, err, &gpe)

	_, err = graph.ParseGraph(makeBadSchema([]any{"a"}))
	require.Error(t, err)
	require.ErrorAs(t, err, &gpe)
}

// TestCOV_GRAPH_ParseContractKinds_EmptyKey covers the empty contract kind key.
func TestCOV_GRAPH_ParseContractKinds_EmptyKey(t *testing.T) {
	env := cloneMap(t, mustEnvelope(t))
	env["contractKinds"] = map[string]any{"": "api_schema", "real": "api_schema"}
	_, err := graph.ParseGraph(mustJSON(t, env))
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), "contractKinds keys must be non-empty")
}

/* --------------------------------------------------------------- mapping composition */

// TestCOV_GRAPH_BuildDocumentGraph_NilConceptsFields covers nil Contracts /
// Concepts / ContractKinds so every concepts block is skipped.
func TestCOV_GRAPH_BuildDocumentGraph_NilConceptsFields(t *testing.T) {
	concepts := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/api.md"}, // nil Contracts
		},
		// Concepts nil, ContractKinds nil
	}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest: testMappingManifest(),
		Concepts: &concepts,
	})
	require.Empty(t, g.Errors)
	require.Equal(t, []string{}, g.Mappings[0].Contracts)
	assert.Empty(t, g.ContractKinds)
	assert.Empty(t, g.Mappings[0].Concepts)
}

// TestCOV_GRAPH_BuildDocumentGraph_QuotedPaths covers unquote's quoted branch
// in buildLineIndex via a raw concepts.yml with quoted paths.
func TestCOV_GRAPH_BuildDocumentGraph_QuotedPaths(t *testing.T) {
	concepts := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs:   []schemas.ConceptsDocEntry{{Path: `docs/api.md`}},
	}
	raw := "schema: docsync.concepts/v1\ndocs:\n  - path: \"docs/api.md\"\n"
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest:    testMappingManifest(),
		Concepts:    &concepts,
		ConceptsRaw: raw,
	})
	require.Empty(t, g.Errors)
	require.Len(t, g.Mappings, 3)
	assert.Empty(t, g.Errors)
}

/* --------------------------------------------------------------- lookup */

// TestCOV_GRAPH_SourceMatchesPath_GuardError covers the glob-guard error
// branch of SourceMatchesPath through BuildReverseIndex (fail closed).
func TestCOV_GRAPH_SourceMatchesPath_GuardError(t *testing.T) {
	g := graph.DocumentGraph{
		Mappings: []graph.MappingRecord{
			{Doc: "docs/x.md", Sources: []string{"/abs/**"}},
		},
	}
	_, err := graph.BuildReverseIndex(g, []string{"anything"})
	require.Error(t, err)
	var guardErr *globs.GlobGuardError
	require.True(t, errors.As(err, &guardErr), "want *globs.GlobGuardError, got %T", err)
	assert.Equal(t, globs.GuardAbsoluteOrWindows, guardErr.Kind)
}

// TestCOV_GRAPH_ReverseLookup_ReturnsNilSliceWhenNoMatch covers ReverseLookup
// returning a nil (empty) slice for a ref with no edges.
func TestCOV_GRAPH_ReverseLookup_ReturnsNilSliceWhenNoMatch(t *testing.T) {
	g := invFixtureGraph(t)
	hits := graph.ReverseLookup(g, "no-such-ref-xyz")
	assert.Empty(t, hits)
	assert.Nil(t, hits)
}

/* --------------------------------------------------------------- orphans */

// TestCOV_GRAPH_DetectOrphans_MixedSignals covers the Doc comparison branch of
// compareOrphanEdges (two docs sharing one deleted source) plus renamed and
// existsAtHead signals in one deterministic run.
func TestCOV_GRAPH_DetectOrphans_MixedSignals(t *testing.T) {
	g := graph.DocumentGraph{
		Mappings: []graph.MappingRecord{
			{Doc: "docs/a.md", Sources: []string{"src/a.ts", "src/b.ts"}},
			{Doc: "docs/b.md", Sources: []string{"src/a.ts"}},
		},
	}
	edges := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{
			{Path: "src/a.ts", Status: "deleted"},
			{Path: "src/c.ts", Status: "renamed", OldPath: "src/b.ts"},
		},
		Graph:        g,
		ExistsAtHead: func(string) bool { return true },
	})
	require.Len(t, edges, 3)
	// Two docs share the deleted src/a.ts source (Doc differs → sort comparison).
	byDoc := map[string]int{}
	for _, e := range edges {
		byDoc[e.Doc]++
	}
	assert.Equal(t, 2, byDoc["docs/a.md"])
	assert.Equal(t, 1, byDoc["docs/b.md"])
	// Renamed edge carries the old path.
	renamed := 0
	for _, e := range edges {
		if e.Kind == "source-renamed" {
			renamed++
			assert.Equal(t, "src/b.ts", e.OldPath)
		}
	}
	assert.Equal(t, 1, renamed)
}

// TestCOV_GRAPH_OrphanDeterminismUnderReorder confirms sorted output regardless
// of changed-file order.
func TestCOV_GRAPH_OrphanDeterminismUnderReorder(t *testing.T) {
	g := orphanGraph(t)
	a := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{
			{Path: "src/ops/run.sh", Status: "deleted"},
			{Path: "src/payments/refund.ts", Status: "deleted"},
		},
		Graph:        g,
		ExistsAtHead: func(string) bool { return false },
	})
	b := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: []graph.OrphanChangedFile{
			{Path: "src/payments/refund.ts", Status: "deleted"},
			{Path: "src/ops/run.sh", Status: "deleted"},
		},
		Graph:        g,
		ExistsAtHead: func(string) bool { return false },
	})
	assert.Equal(t, a, b)
}

/* --------------------------------------------------------------- helpers */

func mustEnvelope(t *testing.T) map[string]any {
	t.Helper()
	g := invFixtureGraph(t)
	raw, err := graph.SerializeGraph(g)
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &env))
	return env
}

func cloneMap(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// TestCOV_GRAPH_ParseGraph_MappingsNonArray covers parseMappings rejecting a
// non-array "mappings" value.
func TestCOV_GRAPH_ParseGraph_MappingsNonArray(t *testing.T) {
	env := cloneMap(t, mustEnvelope(t))
	env["mappings"] = "not-an-array"
	_, err := graph.ParseGraph(mustJSON(t, env))
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), `"mappings" must be an array`)
}

// TestCOV_GRAPH_ParseGraph_MappingNonObject covers parseMapping rejecting a
// non-object mapping entry.
func TestCOV_GRAPH_ParseGraph_MappingNonObject(t *testing.T) {
	env := cloneMap(t, mustEnvelope(t))
	env["mappings"] = []any{"not-an-object"}
	_, err := graph.ParseGraph(mustJSON(t, env))
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), "mapping[0] must be an object")
}

// TestCOV_GRAPH_ParseGraph_MappingSourcesMissing covers parseMapping rejecting
// a mapping whose "sources" field is absent.
func TestCOV_GRAPH_ParseGraph_MappingSourcesMissing(t *testing.T) {
	env := cloneMap(t, mustEnvelope(t))
	m := cloneMap(t, mustEnvelope(t))["mappings"].([]any)
	first := cloneMap(t, m[0].(map[string]any))
	delete(first, "sources")
	env["mappings"] = []any{first}
	_, err := graph.ParseGraph(mustJSON(t, env))
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)
	assert.Contains(t, gpe.Error(), "sources must be present")
}

// TestCOV_GRAPH_BuildDocumentGraph_ContractsDeclared covers the entry.Contracts
// non-nil branch in mapping composition (buildLineIndex / contract acceptance).
func TestCOV_GRAPH_BuildDocumentGraph_ContractsDeclared(t *testing.T) {
	concepts := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/api.md", Contracts: []string{"request_schema"}},
		},
		ContractKinds: map[string]schemas.ContractKind{"request_schema": schemas.ContractKindAPISchema},
	}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest: testMappingManifest(),
		Concepts: &concepts,
	})
	require.Empty(t, g.Errors)
	require.Equal(t, []string{"request_schema"}, g.Mappings[0].Contracts)
	assert.Equal(t, map[string]schemas.ContractKind{"request_schema": schemas.ContractKindAPISchema}, g.ContractKinds)
}

// TestCOV_GRAPH_ReverseLookup_DupDocBranch covers ReverseLookup's
// seen[edge.Doc] duplicate branch: the same ref appears on two edges of one
// doc (different kinds), so the second is skipped.
func TestCOV_GRAPH_ReverseLookup_DupDocBranch(t *testing.T) {
	g := graph.DocumentGraph{
		Mappings: []graph.MappingRecord{
			{Doc: "docs/x.md", APIs: []string{"config/ops.yml"}, Configs: []string{"config/ops.yml"}},
		},
	}
	hits := graph.ReverseLookup(g, "config/ops.yml")
	require.Len(t, hits, 1)
	assert.Equal(t, "docs/x.md", hits[0].Doc)
}

// TestCOV_GRAPH_ReverseLookup_NilSliceWhenDocHasNoEdges covers ReverseLookup
// returning a nil slice for a ref present only on a different doc.
func TestCOV_GRAPH_ReverseLookup_NilSliceWhenDocHasNoEdges(t *testing.T) {
	g := graph.DocumentGraph{
		Mappings: []graph.MappingRecord{
			{Doc: "docs/a.md", Sources: []string{"src/a.ts"}},
		},
	}
	hits := graph.ReverseLookup(g, "src/z.ts")
	assert.Empty(t, hits)
	assert.Nil(t, hits)
}
