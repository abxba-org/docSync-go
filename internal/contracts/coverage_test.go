package contracts_test

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func TestUnwrap_MappingProposalInputError(t *testing.T) {
	graph := proposalsGraph()
	graph.Errors = []contracts.GraphConfigError{{Kind: contracts.GraphErrorDuplicateConceptName, Path: "x", Message: "dup"}}
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "x", Evidence: ev("e")},
		},
	})
	require.Error(t, err)
	var typedErr *contracts.MappingProposalInputError
	require.True(t, errors.As(err, &typedErr))
	require.Equal(t, "EDOCSYNC_MAPPING_PROPOSAL_INPUT", typedErr.Code())
	require.Contains(t, typedErr.Error(), "graph carries composition errors")
	require.Error(t, typedErr.Unwrap())
}

func TestDeriveMappingProposals_UnknownField(t *testing.T) {
	graph := proposalsGraph()
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/a.md", Field: "unknown_field", Ref: "x", Evidence: ev("e")},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "field \"unknown_field\" is not a mapping-proposal field")
}

func TestDeriveMappingProposals_UnknownDoc(t *testing.T) {
	graph := proposalsGraph()
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/ghost.md", Field: contracts.MappingProposalFieldAPIs, Ref: "x", Evidence: ev("e")},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "suggestion 0: doc \"docs/ghost.md\" is not in the graph")
}

func TestDeriveMappingProposals_MultipleErrors(t *testing.T) {
	graph := proposalsGraph()
	graph.Errors = []contracts.GraphConfigError{
		{Kind: contracts.GraphErrorDuplicateConceptName, Path: "x", Message: "a"},
		{Kind: contracts.GraphErrorDuplicateClaimID, Path: "y", Message: "b"},
	}
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "x", Evidence: ev("e")},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate_concept_name")
	require.Contains(t, err.Error(), "duplicate_claim_id")
}

func TestSourceMatchesPath_LiteralAndGlob(t *testing.T) {
	lit, err := contracts.SourceMatchesPath("src/foo/bar.go", "src/foo/bar.go")
	require.NoError(t, err)
	require.True(t, lit)
	litNo, err := contracts.SourceMatchesPath("src/foo/bar.go", "src/other/file.go")
	require.NoError(t, err)
	require.False(t, litNo)
	glob, err := contracts.SourceMatchesPath("src/**/*.go", "src/foo/bar.go")
	require.NoError(t, err)
	require.True(t, glob)
	globNo, err := contracts.SourceMatchesPath("src/other/**", "src/foo/bar.go")
	require.NoError(t, err)
	require.False(t, globNo)
}

func TestSourceMatchesPath_GlobGuardError(t *testing.T) {
	_, err := contracts.SourceMatchesPath("../escape/**", "src/foo/bar.go")
	require.Error(t, err)
}

func TestDeriveMappingProposals_GenerateIDError(t *testing.T) {
	graph := proposalsGraph()
	genErr := errors.New("generate id failed")
	gen := func() (string, error) { return "", genErr }
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "api-new", Evidence: ev("api")},
		},
		GenerateID: gen,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, genErr)
}

func TestDeriveMappingProposals_SkipsExistingRef(t *testing.T) {
	graph := proposalsGraph()
	proposals, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "existing-api", Evidence: ev("dup-existing")},
		},
		ProposalIDs: []string{"p1"},
	})
	require.NoError(t, err)
	require.Empty(t, proposals)
}

func TestDeriveMappingProposals_EmptyRef(t *testing.T) {
	graph := proposalsGraph()
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "", Evidence: ev("e")},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ref must be non-empty")
}

func TestConfigKeyEvidence_Nil(t *testing.T) {
	graph := vp112Graph()
	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md", ChangeAnalysis: analysis(nil),
	})
	row := findEntry(t, vector, "request_schema")
	require.Nil(t, row.ConfigKeyEvidence)
}

func TestClassifyContracts_SymbolTypeEvidenceError(t *testing.T) {
	manifest := schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{
		{Path: "docs/x.md", Sources: []string{"../escape/**"}},
	}}
	concepts := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs:   []schemas.ConceptsDocEntry{{Path: "docs/x.md", Contracts: []string{"c1"}}},
	}
	graph := composeGraph(manifest, concepts)
	_, err := contracts.ClassifyContracts(contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/x.md",
		ChangeAnalysis: &schemas.ChangeAnalysis{Files: []schemas.ChangedFile{{
			Path:          "../escape/some/file.go",
			Status:        schemas.FileStatusModified,
			SymbolsDetail: &schemas.SymbolsDetail{Changed: []schemas.SymbolChangeDetail{{Name: "Foo", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeType}}}},
		}}},
	})
	require.Error(t, err)
}
