package contracts_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func proposalsGraph() *contracts.Graph {
	return &contracts.Graph{
		Mappings: []contracts.MappingRecord{
			{Doc: "docs/a.md", Sources: []string{"src/a/**"}, Apis: []string{"existing-api"}},
			{Doc: "docs/b.md", Sources: []string{"src/b/**"}},
		},
		ContractKinds: map[string]schemas.ContractKind{},
	}
}

func ev(kind string) contracts.MappingProposalEvidence {
	return contracts.MappingProposalEvidence{Kind: kind, Detail: kind + " detail"}
}

func TestDeriveMappingProposals_GroupsSortsDedupesSkipsExisting(t *testing.T) {
	graph := proposalsGraph()
	proposals, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/b.md", Field: contracts.MappingProposalFieldConfigs, Ref: "cfg1", Evidence: ev("cfg")},
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "api-new", Evidence: ev("api")},
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "existing-api", Evidence: ev("dup-existing")},
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "api-new", Evidence: ev("dup-group")},
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldConfigs, Ref: "c", Evidence: ev("acfg")},
			{DocPath: "docs/b.md", Field: contracts.MappingProposalFieldAPIs, Ref: "z", Evidence: ev("bapi")},
		},
		ProposalIDs: []string{"p1", "p2", "p3", "p4"},
	})
	require.NoError(t, err)

	require.Equal(t, []contracts.MappingProposal{
		{ProposalID: "p1", DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, AddRefs: []string{"api-new"}, Evidence: []contracts.MappingProposalEvidence{ev("api")}, Status: "proposed"},
		{ProposalID: "p2", DocPath: "docs/a.md", Field: contracts.MappingProposalFieldConfigs, AddRefs: []string{"c"}, Evidence: []contracts.MappingProposalEvidence{ev("acfg")}, Status: "proposed"},
		{ProposalID: "p3", DocPath: "docs/b.md", Field: contracts.MappingProposalFieldAPIs, AddRefs: []string{"z"}, Evidence: []contracts.MappingProposalEvidence{ev("bapi")}, Status: "proposed"},
		{ProposalID: "p4", DocPath: "docs/b.md", Field: contracts.MappingProposalFieldConfigs, AddRefs: []string{"cfg1"}, Evidence: []contracts.MappingProposalEvidence{ev("cfg")}, Status: "proposed"},
	}, proposals)

	// INV-3: the graph is NEVER mutated.
	require.Equal(t, []string{"existing-api"}, mappingByDoc(graph, "docs/a.md").Apis)
	require.Nil(t, mappingByDoc(graph, "docs/b.md").Apis)
}

func TestDeriveMappingProposals_GeneratesIDsWhenNotProvided(t *testing.T) {
	graph := proposalsGraph()
	n := 0
	gen := func() (string, error) {
		n++
		return fmt.Sprintf("GEN%02d", n), nil
	}
	proposals, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "api-new", Evidence: ev("api")},
			{DocPath: "docs/b.md", Field: contracts.MappingProposalFieldConfigs, Ref: "cfg1", Evidence: ev("cfg")},
		},
		GenerateID: gen,
	})
	require.NoError(t, err)
	require.Len(t, proposals, 2)
	require.Equal(t, "GEN01", proposals[0].ProposalID)
	require.Equal(t, "GEN02", proposals[1].ProposalID)
}

func TestDeriveMappingProposals_FailsClosedOnGraphErrors(t *testing.T) {
	graph := proposalsGraph()
	graph.Errors = []contracts.GraphConfigError{{Kind: contracts.GraphErrorDuplicateConceptName, Path: "concepts[0].name", Message: "dup"}}
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph:       graph,
		Suggestions: []contracts.MappingProposalSuggestion{{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "x", Evidence: ev("e")}},
	})
	require.Error(t, err)
	var typed *contracts.MappingProposalInputError
	require.True(t, errors.As(err, &typed))
	require.Equal(t, "EDOCSYNC_MAPPING_PROPOSAL_INPUT", typed.Code())
	require.Contains(t, err.Error(), "graph carries composition errors")
	require.Contains(t, err.Error(), "duplicate_concept_name")
}

func TestDeriveMappingProposals_FailsClosedOnEmptyRef(t *testing.T) {
	graph := proposalsGraph()
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "ok", Evidence: ev("e")},
			{DocPath: "docs/a.md", Field: contracts.MappingProposalFieldAPIs, Ref: "", Evidence: ev("e")},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "suggestion 1: ref must be non-empty")
}

func TestDeriveMappingProposals_FailsClosedOnUnknownDoc(t *testing.T) {
	graph := proposalsGraph()
	_, err := contracts.DeriveMappingProposals(contracts.DeriveMappingProposalsInput{
		Graph: graph,
		Suggestions: []contracts.MappingProposalSuggestion{
			{DocPath: "docs/ghost.md", Field: contracts.MappingProposalFieldAPIs, Ref: "x", Evidence: ev("e")},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `doc "docs/ghost.md" is not in the graph`)
	require.Contains(t, err.Error(), "never invent docs")
}
