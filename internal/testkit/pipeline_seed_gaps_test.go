package testkit

import (
	"os"
	
	"testing"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/statemachine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeedVerifiedDoc_InitializeError(t *testing.T) {
	root := t.TempDir()
	pipeline, err := BuildPaymentsPipeline(root)
	require.NoError(t, err)

	// Corrupt the state store by replacing state dir with a file
	// so that Initialize cannot create/write state
	stateDir := pipeline.StateDir()
	require.NoError(t, os.RemoveAll(stateDir))
	require.NoError(t, os.WriteFile(stateDir, []byte("corrupted"), 0644))

	err = pipeline.SeedVerifiedDoc("initial-commit")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "doc state initialize failed")
}

func TestSeedVerifiedDoc_InitializeNotOK(t *testing.T) {
	root := t.TempDir()
	pipeline, err := BuildPaymentsPipeline(root)
	require.NoError(t, err)

	// Pre-create a state record for PaymentsDoc so Initialize sees it exists
	machine := statemachine.NewDocStateMachine(pipeline.Store)
	_, err = machine.Initialize(statemachine.InitializeStateInput{
		DocPath:       PaymentsDoc,
		To:            schemas.DocStateSuspect,
		RunID:         PipelineInitRun,
		PolicyVersion: PipelinePolicyVersion,
		At:            PipelineT0,
	})
	require.NoError(t, err)

	// Now Initialize will fail with CAS conflict (expects absent) -> returns !OK
	err = pipeline.SeedVerifiedDoc("initial-commit")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "doc state initialize failed")
}

func TestSeedVerifiedDoc_ForwardLookupNil(t *testing.T) {
	root := t.TempDir()
	pipeline, err := BuildPaymentsPipeline(root)
	require.NoError(t, err)

	// Replace graph with empty graph so ForwardLookup returns nil
	pipeline.Graph = graph.DocumentGraph{}

	err = pipeline.SeedVerifiedDoc("initial-commit")
	require.Error(t, err)
	assert.Equal(t, "payments fixture doc is not mapped", err.Error())
}
