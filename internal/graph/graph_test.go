package graph_test

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// GraphUnusableError is the module's public coded error consumed by routing
// consumers (statemachine/suspect, retrieval/engine); it is not constructed
// inside the graph package, so the ported suites do not reach it. This parity
// test pins its code, message text and the docutil.Unwrap seam.
func TestGraphUnusableError_CodeMessageAndSeam(t *testing.T) {
	errs := []graph.GraphConfigError{
		{Kind: graph.GraphConfigErrConceptsDocNotInManifest, Path: "docs[1].path", Message: "boom"},
	}
	u := graph.NewGraphUnusableError(errs)
	require.Equal(t, "EDOCSYNC_GRAPH_UNUSABLE", u.Code())
	require.Equal(t, graph.CodeGraphUnusable, u.Code())
	require.Equal(t, errs, u.Errors)
	require.Contains(t, u.Error(), "document graph has 1 configuration error(s) — refusing to route (fail closed)")
	require.Equal(t, graph.CodeGraphUnusable, docutil.CodeOf(u))
}

func TestGraphParseError_CodeSeam(t *testing.T) {
	_, err := graph.ParseGraph("[")
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)
	require.Equal(t, "EDOCSYNC_GRAPH_PARSE", gpe.Code())
	require.Equal(t, graph.CodeGraphParse, gpe.Code())
	require.Equal(t, graph.CodeGraphParse, docutil.CodeOf(err))
}

// Fixtures and small helpers for the two document-graph suites
// (graph.test.ts + graph-invariants.test.ts). Names are prefixed so the two
// source manifests/concepts (which differ) do not collide in one package.

// testMappingManifest / testMappingConcepts port the graph.test.ts fixtures.
func testMappingManifest() schemas.Manifest {
	return schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/api.md", Sources: []string{"src/api/**"}},
			{Path: "docs/payments.md", Sources: []string{"src/payments/**", "src/payments/refund.ts"}},
			{Path: "docs/guide.md", Sources: []string{"docs-src/guide.md"}},
		},
	}
}

func testMappingConcepts() schemas.ConceptsConfig {
	c := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/api.md", Contracts: []string{"request_schema", "authentication"}},
			{
				Path: "docs/payments.md",
				Claims: []schemas.ConceptsClaimDeclaration{
					{
						ID:      "claim_1",
						Text:    "Refunds require an idempotency key.",
						Anchor:  schemas.ConceptsClaimAnchor{File: "docs/payments.md", StartLine: 2, EndLine: 2},
						Source:  schemas.ClaimSource{File: "src/payments/refund.ts", BlobSHA: "abc1234"},
						Concept: nil,
					},
				},
			},
		},
		Concepts:      []schemas.ConceptEntry{{Name: "refunds", Docs: []string{"docs/payments.md", "docs/api.md"}}},
		ContractKinds: map[string]schemas.ContractKind{"request_schema": schemas.ContractKindAPISchema, "authentication": schemas.ContractKindBehavior},
	}
	return c
}

// invManifest / invConcepts port the graph-invariants.test.ts fixtures.
func invManifest() schemas.Manifest {
	return schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/api.md", Sources: []string{"src/api/**"}},
			{Path: "docs/payments.md", Sources: []string{"src/payments/refund.ts", "src/payments/types.ts"}},
			{Path: "docs/ops.md", Sources: []string{"ops/runbooks/**"}},
		},
	}
}

func invConcepts() schemas.ConceptsConfig {
	return schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/api.md", Contracts: []string{"request_schema", "authentication"}},
			{Path: "docs/payments.md", Contracts: []string{"idempotency"}},
			{Path: "docs/ops.md", Contracts: []string{"rollback_procedure"}},
		},
		Concepts: []schemas.ConceptEntry{{Name: "refunds", Docs: []string{"docs/api.md", "docs/payments.md"}}},
		ContractKinds: map[string]schemas.ContractKind{
			"request_schema":     schemas.ContractKindAPISchema,
			"authentication":     schemas.ContractKindBehavior,
			"idempotency":        schemas.ContractKindConfig,
			"rollback_procedure": schemas.ContractKindContent,
		},
	}
}

// invFixtureGraph composes the Phase-1 graph, then declares the Phase-11 rich
// edges on the mapping records (the declarative, reviewable home for richer
// edges). Shared refs across docs exercise multi-doc reverse resolution; literal
// AND glob refs exercise both match modes of the changed-path reverse index.
func invFixtureGraph(t *testing.T) graph.DocumentGraph {
	t.Helper()
	concepts := invConcepts()
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: invManifest(), Concepts: &concepts})
	require.Empty(t, g.Errors)
	for i := range g.Mappings {
		switch g.Mappings[i].Doc {
		case "docs/api.md":
			g.Mappings[i].APIs = []string{"src/api/server.ts", "openapi/**"}
			g.Mappings[i].Configs = []string{"config/payments.yml", "config/ops.yml"}
		case "docs/payments.md":
			g.Mappings[i].Workflows = []string{".github/workflows/refund.yml"}
			g.Mappings[i].Configs = []string{"config/ops.yml"}
		case "docs/ops.md":
			g.Mappings[i].Configs = []string{"config/ops.yml"}
			g.Mappings[i].Workflows = []string{"workflows/nightly/**"}
		}
	}
	return g
}

func docNames(g graph.DocumentGraph) []string {
	out := make([]string, len(g.Mappings))
	for i := range g.Mappings {
		out[i] = g.Mappings[i].Doc
	}
	return out
}

func mappingDocs(mappings []graph.MappingRecord) []string {
	out := make([]string, len(mappings))
	for i := range mappings {
		out[i] = mappings[i].Doc
	}
	return out
}

func setKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func distinctStringKinds(edges []graph.DocumentGraphEdge) []string {
	set := map[string]struct{}{}
	for _, e := range edges {
		set[string(e.Kind)] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func graphEdgeKindStrings() []string {
	out := make([]string, len(graph.GraphEdgeKindValues))
	for i, k := range graph.GraphEdgeKindValues {
		out[i] = string(k)
	}
	return out
}

func errorKinds(errors []graph.GraphConfigError) []string {
	out := make([]string, len(errors))
	for i, e := range errors {
		out[i] = string(e.Kind)
	}
	return out
}

// ---- mapping composition (manifest + concepts + contractKinds) ----

func TestBuildDocumentGraph_ComposesOneMappingPerManifestDoc(t *testing.T) {
	concepts := testMappingConcepts()
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Empty(t, g.Errors)
	require.Equal(t, []string{"docs/api.md", "docs/payments.md", "docs/guide.md"}, docNames(g))
	require.Equal(t, []string{"src/api/**"}, g.Mappings[0].Sources)
	require.Equal(t, []string{"src/payments/**", "src/payments/refund.ts"}, g.Mappings[1].Sources)
	require.Equal(t, []string{"docs-src/guide.md"}, g.Mappings[2].Sources)
}

func TestBuildDocumentGraph_MergesConceptMembershipAndContracts(t *testing.T) {
	concepts := testMappingConcepts()
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Equal(t, []string{"request_schema", "authentication"}, g.Mappings[0].Contracts)
	require.Equal(t, []string{"refunds"}, g.Mappings[0].Concepts)
	require.Equal(t, []string{"refunds"}, g.Mappings[1].Concepts)
	require.Equal(t, []string{}, g.Mappings[1].Contracts)
	require.Equal(t, []string{}, g.Mappings[2].Concepts)
}

func TestBuildDocumentGraph_CarriesValidatedContractKinds(t *testing.T) {
	concepts := testMappingConcepts()
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Equal(t, map[string]schemas.ContractKind{
		"request_schema": schemas.ContractKindAPISchema,
		"authentication": schemas.ContractKindBehavior,
	}, g.ContractKinds)
}

func TestBuildDocumentGraph_DeterministicFromManifestOnlyAndConcepts(t *testing.T) {
	bare := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest()})
	require.Empty(t, bare.Errors)
	require.Equal(t, []graph.MappingRecord{
		{Doc: "docs/api.md", Sources: []string{"src/api/**"}, Concepts: []string{}, Contracts: []string{}},
		{Doc: "docs/payments.md", Sources: []string{"src/payments/**", "src/payments/refund.ts"}, Concepts: []string{}, Contracts: []string{}},
		{Doc: "docs/guide.md", Sources: []string{"docs-src/guide.md"}, Concepts: []string{}, Contracts: []string{}},
	}, bare.Mappings)

	concepts := testMappingConcepts()
	a := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	b := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Equal(t, a, b)
}

// ---- V-P1-5: reverse + forward lookup both correct ----

func TestV_P1_5_ReverseForwardLookup(t *testing.T) {
	concepts := testMappingConcepts()
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})

	t.Run("routes a changed path through a source glob to the mapped doc", func(t *testing.T) {
		idx, err := graph.BuildReverseIndex(g, []string{"src/api/server.ts"})
		require.NoError(t, err)
		require.Equal(t, []string{"docs/api.md"}, mappingDocs(idx.DocsByChangedPath["src/api/server.ts"]))
		require.Equal(t, []string{"docs/api.md"}, setKeys(idx.AffectedDocs))
	})

	t.Run("lists a mapping once even when several of its sources match the same path", func(t *testing.T) {
		idx, err := graph.BuildReverseIndex(g, []string{"src/payments/refund.ts"})
		require.NoError(t, err)
		require.Equal(t, []string{"docs/payments.md"}, mappingDocs(idx.DocsByChangedPath["src/payments/refund.ts"]))
	})

	t.Run("matches literal sources by exact equality", func(t *testing.T) {
		require.True(t, graph.IsLiteralSourcePattern("docs-src/guide.md"))
		idx, err := graph.BuildReverseIndex(g, []string{"docs-src/guide.md", "docs-src/other.md"})
		require.NoError(t, err)
		require.Equal(t, []string{"docs/guide.md"}, mappingDocs(idx.DocsByChangedPath["docs-src/guide.md"]))
		require.Empty(t, idx.DocsByChangedPath["docs-src/other.md"])
	})

	t.Run("affectedDocs aggregates across the changed set", func(t *testing.T) {
		idx, err := graph.BuildReverseIndex(g, []string{"src/api/server.ts", "src/payments/refund.ts", "x.txt"})
		require.NoError(t, err)
		require.Equal(t, []string{"docs/api.md", "docs/payments.md"}, setKeys(idx.AffectedDocs))
		require.Empty(t, idx.DocsByChangedPath["x.txt"])
	})

	t.Run("forward lookup resolves a mapped doc and nil for an unmapped one", func(t *testing.T) {
		m := graph.ForwardLookup(g, "docs/api.md")
		require.NotNil(t, m)
		require.Equal(t, []string{"src/api/**"}, m.Sources)
		require.Nil(t, graph.ForwardLookup(g, "docs/unlisted.md"))
	})
}

// ---- GraphConfigError cases (fail closed, D8) ----

func TestGraphConfigError_ConceptsDocNotInManifestNeverInvented(t *testing.T) {
	concepts := testMappingConcepts()
	concepts.Docs = append(append([]schemas.ConceptsDocEntry{}, concepts.Docs...),
		schemas.ConceptsDocEntry{Path: "docs/ghost.md", Contracts: []string{"mystery"}})
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Contains(t, errorKinds(g.Errors), "concepts_doc_not_in_manifest")
	require.NotContains(t, docNames(g), "docs/ghost.md")
}

func TestGraphConfigError_ConceptReferencesUnknownDocKeepsKnownMembership(t *testing.T) {
	concepts := testMappingConcepts()
	concepts.Concepts = []schemas.ConceptEntry{{Name: "refunds", Docs: []string{"docs/payments.md", "docs/ghost.md"}}}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Contains(t, errorKinds(g.Errors), "concept_references_unknown_doc")
	require.Equal(t, []string{"refunds"}, g.Mappings[1].Concepts)
}

func TestGraphConfigError_DuplicateConceptNameKeepsFirst(t *testing.T) {
	concepts := testMappingConcepts()
	concepts.Concepts = []schemas.ConceptEntry{
		{Name: "refunds", Docs: []string{"docs/payments.md"}},
		{Name: "refunds", Docs: []string{"docs/api.md"}},
	}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Contains(t, errorKinds(g.Errors), "duplicate_concept_name")
	require.Equal(t, []string{"refunds"}, g.Mappings[1].Concepts)
	require.Equal(t, []string{}, g.Mappings[0].Concepts)
}

func TestGraphConfigError_ClaimIDDeclaredOnTwoDocs(t *testing.T) {
	concepts := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/payments.md", Claims: []schemas.ConceptsClaimDeclaration{{
				ID:     "claim_1",
				Text:   "A documented fact.",
				Anchor: schemas.ConceptsClaimAnchor{File: "docs/payments.md", StartLine: 1, EndLine: 1},
				Source: schemas.ClaimSource{File: "src/payments/refund.ts", BlobSHA: "abc1234"},
			}}},
			{Path: "docs/api.md", Claims: []schemas.ConceptsClaimDeclaration{{
				ID:     "claim_1",
				Text:   "A documented fact.",
				Anchor: schemas.ConceptsClaimAnchor{File: "docs/api.md", StartLine: 1, EndLine: 1},
				Source: schemas.ClaimSource{File: "src/payments/refund.ts", BlobSHA: "abc1234"},
			}}},
		},
	}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Contains(t, errorKinds(g.Errors), "duplicate_claim_id")
}

func TestGraphConfigError_DuplicateContractKeepsFirst(t *testing.T) {
	concepts := testMappingConcepts()
	concepts.Docs = []schemas.ConceptsDocEntry{
		{Path: "docs/api.md", Contracts: []string{"request_schema", "request_schema"}},
	}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Contains(t, errorKinds(g.Errors), "duplicate_contract_declaration")
	require.Equal(t, []string{"request_schema"}, g.Mappings[0].Contracts)
}

func TestGraphConfigError_UnknownContractKindDropped(t *testing.T) {
	concepts := testMappingConcepts()
	concepts.ContractKinds = map[string]schemas.ContractKind{
		"request_schema": schemas.ContractKindAPISchema,
		"magic":          schemas.ContractKind("quantum"),
	}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts})
	require.Contains(t, errorKinds(g.Errors), "unknown_contract_kind")
	require.Equal(t, map[string]schemas.ContractKind{"request_schema": schemas.ContractKindAPISchema}, g.ContractKinds)
}

func TestGraphConfigError_BestEffortLinePrecisionFromRawYAML(t *testing.T) {
	raw := []string{
		"schema: docsync.concepts/v1",
		"docs:",
		"  - path: docs/api.md",
		"    contracts:",
		"      - request_schema",
		"  - path: docs/ghost.md",
	}
	concepts := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/api.md", Contracts: []string{"request_schema"}},
			{Path: "docs/ghost.md"},
		},
	}
	rawText := ""
	for i, line := range raw {
		if i > 0 {
			rawText += "\n"
		}
		rawText += line
	}
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: testMappingManifest(), Concepts: &concepts, ConceptsRaw: rawText})
	var ghost *graph.GraphConfigError
	for i := range g.Errors {
		if g.Errors[i].Kind == "concepts_doc_not_in_manifest" {
			ghost = &g.Errors[i]
			break
		}
	}
	require.NotNil(t, ghost)
	require.NotNil(t, ghost.Line)
	require.Equal(t, 6, *ghost.Line)
	require.Equal(t, "docs[1].path", ghost.Path)
}

// ---- graph lookup invariants ----

func TestInvariants_FixtureCoversEveryEdgeKind(t *testing.T) {
	g := invFixtureGraph(t)
	edges := graph.GraphEdges(g)

	expected := graphEdgeKindStrings()
	sort.Strings(expected)
	require.Equal(t, expected, distinctStringKinds(edges))

	// Additive vocabulary: Phase-1 kinds keep their positions, new kinds appended.
	require.Equal(t, []string{"code", "concept", "doc", "api", "config", "workflow"}, graphEdgeKindStrings())
	require.True(t, graph.IsGraphEdgeKind("workflow"))
	require.True(t, graph.IsGraphEdgeKind("code"))
	require.False(t, graph.IsGraphEdgeKind("quantum"))
}

func TestInvariants_EveryEdgeResolvesBothDirectionsMutualInverse(t *testing.T) {
	g := invFixtureGraph(t)
	edges := graph.GraphEdges(g)
	docs := docNames(g)

	// Forward direction, doc → refs (typed edges, ALL kinds):
	forward := map[string]map[string]struct{}{}
	for _, doc := range docs {
		set := map[string]struct{}{}
		for _, e := range graph.ForwardEdges(g, doc) {
			set[e.Ref] = struct{}{}
		}
		forward[doc] = set
	}
	// Reverse direction, ref → docs (exact ref, ALL kinds):
	refSeen := map[string]struct{}{}
	var refs []string
	for _, e := range edges {
		if _, ok := refSeen[e.Ref]; !ok {
			refSeen[e.Ref] = struct{}{}
			refs = append(refs, e.Ref)
		}
	}
	reverse := map[string]map[string]struct{}{}
	for _, ref := range refs {
		set := map[string]struct{}{}
		for _, m := range graph.ReverseLookup(g, ref) {
			set[m.Doc] = struct{}{}
		}
		reverse[ref] = set
	}

	for _, edge := range edges {
		require.Contains(t, forward[edge.Doc], edge.Ref, fmt.Sprintf("forward misses %s → %s", edge.Doc, edge.Ref))
		require.Contains(t, reverse[edge.Ref], edge.Doc, fmt.Sprintf("reverse misses %s → %s", edge.Ref, edge.Doc))
	}
	// No phantom entries on either side — the maps are mutual inverses:
	for ref, hitDocs := range reverse {
		for doc := range hitDocs {
			require.Contains(t, forward[doc], ref, fmt.Sprintf("phantom reverse %s → %s", ref, doc))
		}
	}
	for doc, refsOfDoc := range forward {
		for ref := range refsOfDoc {
			require.Contains(t, reverse[ref], doc, fmt.Sprintf("phantom forward %s → %s", doc, ref))
		}
	}
	// Reverse lookup never duplicates a doc for one ref.
	for _, ref := range refs {
		hits := graph.ReverseLookup(g, ref)
		set := map[string]struct{}{}
		for _, m := range hits {
			set[m.Doc] = struct{}{}
		}
		require.Equal(t, len(set), len(hits), "reverse duplicated a doc for ref "+ref)
	}
}

func TestInvariants_PathKindEdgesRouteThroughChangedPathReverseIndex(t *testing.T) {
	g := invFixtureGraph(t)
	edges := graph.GraphEdges(g)
	pathKinds := map[graph.GraphEdgeKind]bool{
		graph.GraphEdgeKindCode: true, graph.GraphEdgeKindAPI: true,
		graph.GraphEdgeKindConfig: true, graph.GraphEdgeKindWorkflow: true,
	}
	var pathEdges []graph.DocumentGraphEdge
	for _, e := range edges {
		if pathKinds[e.Kind] {
			pathEdges = append(pathEdges, e)
		}
	}
	require.NotEmpty(t, pathEdges)
	for _, edge := range pathEdges {
		sample, err := samplePathForRef(edge.Ref)
		require.NoError(t, err)
		idx, err := graph.BuildReverseIndex(g, []string{sample})
		require.NoError(t, err)
		routed := mappingDocs(idx.DocsByChangedPath[sample])
		require.Contains(t, routed, edge.Doc, fmt.Sprintf("reverse index misses %s → %s", sample, edge.Doc))
		require.Contains(t, setKeys(idx.AffectedDocs), edge.Doc)
		forward := graph.ForwardEdges(g, edge.Doc)
		found := false
		for _, c := range forward {
			if c.Ref == edge.Ref && pathKinds[c.Kind] {
				found = true
				break
			}
		}
		require.True(t, found, fmt.Sprintf("forward edges of %s lack the edge behind %s", edge.Doc, sample))
	}
}

func TestInvariants_NameKindEdgesNotPathMatched(t *testing.T) {
	g := invFixtureGraph(t)
	edges := graph.GraphEdges(g)
	var nameRefs []graph.DocumentGraphEdge
	for _, e := range edges {
		if e.Kind == graph.GraphEdgeKindConcept || e.Kind == graph.GraphEdgeKindDoc {
			nameRefs = append(nameRefs, e)
		}
	}
	require.NotEmpty(t, nameRefs)
	conceptRef := ""
	for _, edge := range nameRefs {
		require.Contains(t, mappingDocs(graph.ReverseLookup(g, edge.Ref)), edge.Doc)
		require.Contains(t, graph.ForwardEdges(g, edge.Doc), edge)
		if edge.Kind == graph.GraphEdgeKindConcept && conceptRef == "" {
			conceptRef = edge.Ref
		}
	}
	// A concept name is NOT a repository path: the changed-path index routes nobody.
	idx, err := graph.BuildReverseIndex(g, []string{conceptRef})
	require.NoError(t, err)
	require.Empty(t, idx.DocsByChangedPath[conceptRef])
}

func TestInvariants_Phase1GraphsBackwardCompatible(t *testing.T) {
	concepts := invConcepts()
	bare := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: invManifest(), Concepts: &concepts})
	require.Equal(t, []string{"code", "concept", "doc"}, distinctStringKinds(graph.GraphEdges(bare)))
	idx, err := graph.BuildReverseIndex(bare, []string{"src/payments/refund.ts", "src/api/server.ts"})
	require.NoError(t, err)
	require.Equal(t, []string{"docs/api.md"}, mappingDocs(idx.DocsByChangedPath["src/api/server.ts"]))
}

// invPathSamples maps each glob PATH-ref in the invariants fixture to a concrete
// path it matches.
var invPathSamples = map[string]string{
	"src/api/**":           "src/api/server.ts",
	"openapi/**":           "openapi/refunds.json",
	"ops/runbooks/**":      "ops/runbooks/rollback.md",
	"workflows/nightly/**": "workflows/nightly/cleanup.yml",
}

func samplePathForRef(ref string) (string, error) {
	if graph.IsLiteralSourcePattern(ref) {
		return ref, nil
	}
	if s, ok := invPathSamples[ref]; ok {
		return s, nil
	}
	return "", fmt.Errorf("fixture must pin a sample path for glob ref %q", ref)
}

// ---- unknown-kind edges are refused (typed, fail closed) ----

func TestToGraphEdge_RefusesKindOutsideVocabulary(t *testing.T) {
	_, err := graph.ToGraphEdge(map[string]any{"kind": "quantum", "doc": "docs/api.md", "ref": "src/api/**"})
	require.Error(t, err)
	var ge *graph.GraphEdgeInvalidError
	require.ErrorAs(t, err, &ge)
	require.Equal(t, graph.CodeGraphEdgeInvalid, ge.Code())
	require.Equal(t, graph.GraphEdgeReasonUnknownKind, ge.Reason)
	require.Equal(t, "quantum", ge.Kind)
	require.Equal(t, "EDOCSYNC_GRAPH_EDGE_INVALID", ge.Code())
}

func TestToGraphEdge_RefusesMalformedShapes(t *testing.T) {
	bad := []any{
		nil,
		"api",
		[]any{"api", "docs/x.md"},
		map[string]any{"kind": "api", "doc": "docs/x.md"},
		map[string]any{"kind": "api", "ref": "src/**"},
		map[string]any{"kind": "api", "doc": "", "ref": "src/**"},
		map[string]any{"kind": "api", "doc": "docs/x.md", "ref": ""},
		map[string]any{"kind": 7, "doc": "docs/x.md", "ref": "src/**"},
	}
	for _, edge := range bad {
		_, err := graph.ToGraphEdge(edge)
		var ge *graph.GraphEdgeInvalidError
		require.ErrorAs(t, err, &ge, "edge %v must be refused as GraphEdgeInvalidError", edge)
	}
	_, err := graph.ToGraphEdge(map[string]any{"kind": "api", "doc": "", "ref": ""})
	var ge *graph.GraphEdgeInvalidError
	require.ErrorAs(t, err, &ge)
	require.Equal(t, graph.GraphEdgeReasonShape, ge.Reason)
}

func TestToGraphEdge_AcceptsEveryVocabularyKind(t *testing.T) {
	edge, err := graph.ToGraphEdge(map[string]any{"kind": "workflow", "doc": "docs/ops.md", "ref": "workflows/nightly/**"})
	require.NoError(t, err)
	require.Equal(t, graph.DocumentGraphEdge{Kind: graph.GraphEdgeKindWorkflow, Doc: "docs/ops.md", Ref: "workflows/nightly/**"}, edge)
}

// ---- graph-as-files round-trip (serialize → parse → same graph) ----

func TestGraphFiles_RoundTripRichFieldsIncluded(t *testing.T) {
	g := invFixtureGraph(t)
	bytes, err := graph.SerializeGraph(g)
	require.NoError(t, err)
	parsed, err := graph.ParseGraph(bytes)
	require.NoError(t, err)
	require.Equal(t, g, parsed)
	require.Equal(t, []string{"src/api/server.ts", "openapi/**"}, parsed.Mappings[0].APIs)
	require.Equal(t, []string{"workflows/nightly/**"}, parsed.Mappings[2].Workflows)
}

func TestGraphFiles_SerializationDeterministicAndVersioned(t *testing.T) {
	s1, err := graph.SerializeGraph(invFixtureGraph(t))
	require.NoError(t, err)
	s2, err := graph.SerializeGraph(invFixtureGraph(t))
	require.NoError(t, err)
	require.Equal(t, s1, s2)
	var env map[string]any
	require.NoError(t, json.Unmarshal([]byte(s1), &env))
	require.Equal(t, graph.GraphSerializationSchema, env["schema"])
	require.Equal(t, "docsync.graph/v1", graph.GraphSerializationSchema)
}

func TestGraphFiles_GraphsWithErrorsRoundTripExactly(t *testing.T) {
	concepts := invConcepts()
	concepts.Docs = append(append([]schemas.ConceptsDocEntry{}, concepts.Docs...),
		schemas.ConceptsDocEntry{Path: "docs/ghost.md", Contracts: []string{"mystery"}})
	raw := "schema: docsync.concepts/v1\ndocs:\n  - path: docs/ghost.md"
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: invManifest(), Concepts: &concepts, ConceptsRaw: raw})
	require.Greater(t, len(g.Errors), 0)
	hasLine := false
	for _, e := range g.Errors {
		if e.Line != nil {
			hasLine = true
			break
		}
	}
	require.True(t, hasLine)
	bytes, err := graph.SerializeGraph(g)
	require.NoError(t, err)
	parsed, err := graph.ParseGraph(bytes)
	require.NoError(t, err)
	require.Equal(t, g, parsed)
}

func TestGraphFiles_ParseGraphRefusesMalformedDocuments(t *testing.T) {
	bytes, err := graph.SerializeGraph(invFixtureGraph(t))
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(bytes), &parsed))

	reject := func(v any) {
		t.Helper()
		b, err := json.Marshal(v)
		require.NoError(t, err)
		_, perr := graph.ParseGraph(string(b))
		var gpe *graph.GraphParseError
		require.ErrorAs(t, perr, &gpe, "expected GraphParseError for %s", string(b))
	}

	// not an object / bad JSON:
	_, err = graph.ParseGraph("[")
	require.Error(t, err)
	var gpe *graph.GraphParseError
	require.ErrorAs(t, err, &gpe)
	reject([]any{parsed})

	// schema:
	badSchema := cloneJSONMap(parsed)
	badSchema["schema"] = "docsync.graph/v2"
	reject(badSchema)

	// top-level keys:
	missingErrors := cloneJSONMap(parsed)
	delete(missingErrors, "errors")
	reject(missingErrors)
	surprise := cloneJSONMap(parsed)
	surprise["surprise"] = 1
	reject(surprise)

	// mapping shape:
	mappings, ok := parsed["mappings"].([]any)
	require.True(t, ok)
	appended := append([]any{}, mappings...)
	appended = append(appended, map[string]any{"doc": "docs/x.md"})
	rejectMappings := cloneJSONMap(parsed)
	rejectMappings["mappings"] = appended
	reject(rejectMappings)

	first := cloneJSONMap(mappings[0].(map[string]any))
	first["phantom"] = true
	phantom := append([]any{}, mappings...)
	phantom[0] = first
	rejectPhantom := cloneJSONMap(parsed)
	rejectPhantom["mappings"] = phantom
	reject(rejectPhantom)

	firstEmpty := cloneJSONMap(mappings[0].(map[string]any))
	firstEmpty["sources"] = []any{"", "src/x.ts"}
	emptySources := append([]any{}, mappings...)
	emptySources[0] = firstEmpty
	rejectEmpty := cloneJSONMap(parsed)
	rejectEmpty["mappings"] = emptySources
	reject(rejectEmpty)

	// contract kinds:
	badKinds := cloneJSONMap(parsed)
	badKinds["contractKinds"] = map[string]any{"magic": "quantum"}
	reject(badKinds)

	// config-error kinds:
	badErrors := cloneJSONMap(parsed)
	badErrors["errors"] = []any{map[string]any{"kind": "quantum_kind", "path": "x", "message": "y"}}
	reject(badErrors)
}

func cloneJSONMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}
