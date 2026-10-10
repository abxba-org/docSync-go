package orchestrator_test

// Ported from packages/core/test/orchestrator-e2e.test.ts (P5-T18; V-P5-1/2/
// 3/5/7/8/10/15) — PARTIAL PORT over the Go API surface that has landed.
//
// The ported assertions, kept here: the deterministic stage graph the
// runPipeline walk consumes (graph.ts — IGNORE/REVIEW/CONTINUE three-way
// branch, ARCH §7.1), the frozen stage list, the budget-merge machinery the
// composed runs drive (types.ts — UNBOUNDED_STAGE_BUDGETS and the forced
// `maxPatchesPerRun` cap of the A10 probe), and the graph→JEV relevance
// context with its ARCH §3.3/§3.7 bounding (types.ts BuildRelevanceContext).
// These are the run-level invariants the TS suite proves WITHOUT a port
// call — routing reachability, budget semantics and edge bounding are pure
// data in this package too.
//
// DEFERRED (requires the runner itself): the TS suite drives these facts
// through `runPipeline` on the payments/monorepo fixture repos with inline
// fake ports, so the end-to-end assertions — V-P5-2 (every stage walked, the
// writer/verification/policy invocation COUNTS, the persisted run record),
// V-P5-3 (abstain surface + review queue), V-P5-5 (writer-timeout finding,
// verification never reached), V-P5-7/8 (provider-outage and analyzer-crash
// degradation), V-P5-10 (early-signal post-then-update), V-P5-15 (A10 load
// probe with real-timer modeled latencies and cap-overflow evidence) — cannot
// be ported yet: internal/orchestrator/run.go is a STUB (no `RunPipeline`
// entry), and this repo has no `internal/testing` pipeline-fixture factory
// (the TS `createPaymentsPipelineRepo` / `createMonorepoPipelineRepo`
// helpers). Both land as a follow-up unit; the routing facts below are the
// half of the same matrix that the landed Go API already owns, so they are
// pinned now rather than left implicit in the graph module.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

/* ------------------------------------------------------- shared test helpers */

// orchPtr returns a pointer to v (optional numeric members; a zero int cannot
// express "unset" in Go).
func orchPtr[T any](v T) *T { return &v }

/* ------------------------------------------------------ stage graph (V-P5-1) */

// orchWalkStages walks the declared stage graph from `analyze` under the given
// routing, exactly the stage list the runner records in the outcome (the
// conditional shortcuts resolve first, mirroring the runner's edge walk).
func orchWalkStages(routing orchestrator.Routing) []orchestrator.StageName {
	walked := []orchestrator.StageName{orchestrator.StageAnalyze}
	current := orchestrator.StageAnalyze
	// The frozen stage list is the walk bound (a cycle would be a graph
	// defect — bail out instead of looping).
	for steps := 0; steps <= len(orchestrator.PipelineStages); steps++ {
		next := orchestrator.NextStage(current, routing)
		if next == "" {
			return walked
		}
		walked = append(walked, next)
		current = next
	}
	return walked
}

func TestPipelineStagesAreTheFrozenCanonicalOrder(t *testing.T) {
	// The exact ordered pipeline stages (PLAN §6.4): analyze → map →
	// invalidate → jev → retrieval → writer → verification → policy →
	// persist. Frozen: no stage can be appended/removed at runtime.
	assert.Equal(t, []orchestrator.StageName{
		orchestrator.StageAnalyze,
		orchestrator.StageMap,
		orchestrator.StageInvalidate,
		orchestrator.StageJev,
		orchestrator.StageRetrieval,
		orchestrator.StageWriter,
		orchestrator.StageVerification,
		orchestrator.StagePolicy,
		orchestrator.StagePersist,
	}, orchestrator.PipelineStages)
}

func TestDeclareStageGraphIsTheFrozenCanonicalEdgeList(t *testing.T) {
	require.Equal(t, []orchestrator.StageEdge{
		{From: orchestrator.StageAnalyze, To: orchestrator.StageMap},
		{From: orchestrator.StageMap, To: orchestrator.StageInvalidate},
		{From: orchestrator.StageInvalidate, To: orchestrator.StageJev},
		{
			From:   orchestrator.StageJev,
			To:     orchestrator.StagePersist,
			SkipIf: &orchestrator.StageEdgeGuard{Routing: orchestrator.RoutingIgnore, Equals: orchestrator.RoutingIgnore},
		},
		{
			From:   orchestrator.StageJev,
			To:     orchestrator.StagePolicy,
			SkipIf: &orchestrator.StageEdgeGuard{Routing: orchestrator.RoutingReview, Equals: orchestrator.RoutingReview},
		},
		{From: orchestrator.StageJev, To: orchestrator.StageRetrieval},
		{From: orchestrator.StageRetrieval, To: orchestrator.StageWriter},
		{From: orchestrator.StageWriter, To: orchestrator.StageVerification},
		{From: orchestrator.StageVerification, To: orchestrator.StagePolicy},
		{From: orchestrator.StagePolicy, To: orchestrator.StagePersist},
	}, orchestrator.DeclareStageGraph())
}

// V-P5-1: the IGNORE shortcut — the run completes on the jev→persist shortcut
// and the writer AND verification AND policy stages are unreachable under
// IGNORE (ARCH §7.1: the ignore-routed majority never invokes the writer).
// This is the routing fact V-P5-1 proves with the fake-port call counters;
// here it is proven on the graph the runner walks.
func TestV_P5_1_IgnoreRoutedWalkNeverReachesWriterVerificationOrPolicy(t *testing.T) {
	walked := orchWalkStages(orchestrator.RoutingIgnore)
	assert.Equal(t, []orchestrator.StageName{
		orchestrator.StageAnalyze,
		orchestrator.StageMap,
		orchestrator.StageInvalidate,
		orchestrator.StageJev,
		orchestrator.StagePersist,
	}, walked)

	for _, unreachable := range []orchestrator.StageName{
		orchestrator.StageRetrieval,
		orchestrator.StageWriter,
		orchestrator.StageVerification,
		orchestrator.StagePolicy,
	} {
		assert.NotContains(t, walked, unreachable,
			"stage %q must be unreachable under IGNORE routing (ARCH §7.1)", unreachable)
	}
}

// The REVIEW branch (the provider-outage / abstain route of V-P5-3/5/7/8)
// goes straight to policy for review resolution — the patch path
// (retrieval, writer, verification) is never entered either.
func TestReviewRoutedWalkResolvesAtPolicyWithoutThePatchPath(t *testing.T) {
	walked := orchWalkStages(orchestrator.RoutingReview)
	assert.Equal(t, []orchestrator.StageName{
		orchestrator.StageAnalyze,
		orchestrator.StageMap,
		orchestrator.StageInvalidate,
		orchestrator.StageJev,
		orchestrator.StagePolicy,
		orchestrator.StagePersist,
	}, walked)
	for _, unreachable := range []orchestrator.StageName{
		orchestrator.StageRetrieval,
		orchestrator.StageWriter,
		orchestrator.StageVerification,
	} {
		assert.NotContains(t, walked, unreachable,
			"stage %q must be unreachable under REVIEW routing", unreachable)
	}
}

// The CONTINUE walk (V-P5-2) reaches every stage in canonical order.
func TestContinueRoutedWalkReachesEveryStageInCanonicalOrder(t *testing.T) {
	assert.Equal(t, orchestrator.PipelineStages, orchWalkStages(orchestrator.RoutingContinue))
}

// Every declared edge connects the frozen stages, and the jev shortcuts
// resolve before the plain jev→retrieval path (the runner takes the first
// edge whose guard matches).
func TestDeclaredEdgesOnlyConnectFrozenStages(t *testing.T) {
	known := make(map[orchestrator.StageName]struct{}, len(orchestrator.PipelineStages))
	for _, stage := range orchestrator.PipelineStages {
		known[stage] = struct{}{}
	}
	for _, edge := range orchestrator.DeclareStageGraph() {
		_, fromKnown := known[edge.From]
		_, toKnown := known[edge.To]
		assert.True(t, fromKnown, "edge from unknown stage %q", edge.From)
		assert.True(t, toKnown, "edge to unknown stage %q", edge.To)
	}
	edges := orchestrator.DeclareStageGraph()
	var jevEdges []orchestrator.StageEdge
	for _, edge := range edges {
		if edge.From == orchestrator.StageJev {
			jevEdges = append(jevEdges, edge)
		}
	}
	require.Len(t, jevEdges, 3)
	assert.NotNil(t, jevEdges[0].SkipIf, "the IGNORE shortcut must resolve first")
	assert.Equal(t, orchestrator.RoutingIgnore, jevEdges[0].SkipIf.Equals)
	assert.NotNil(t, jevEdges[1].SkipIf, "the REVIEW shortcut must resolve second")
	assert.Equal(t, orchestrator.RoutingReview, jevEdges[1].SkipIf.Equals)
	assert.Nil(t, jevEdges[2].SkipIf, "the plain jev→retrieval path must resolve last")
}

/* --------------------------------------------------------------- budgets */

// The composed runs of the TS suite drive every stage unbounded (the suite
// asserts routing/composition closure, not machine speed): the override
// carries a nil entry per stage and leaves the other budgets at default.
func TestUnboundedStageBudgetsMergeOverTheDefaults(t *testing.T) {
	stageBudgetMS := make(map[orchestrator.StageName]*int, len(orchestrator.PipelineStages))
	for _, stage := range orchestrator.PipelineStages {
		stageBudgetMS[stage] = nil
	}
	merged := orchestrator.MergePipelineBudgets(&orchestrator.PipelineBudgetOverrides{StageBudgetMS: stageBudgetMS})

	for _, stage := range orchestrator.PipelineStages {
		require.Contains(t, merged.StageBudgetMS, stage)
		assert.Nil(t, merged.StageBudgetMS[stage], "stage %q must be unbounded", stage)
	}
	assert.Equal(t, 20, merged.MaxDocsInvestigated)
	assert.Equal(t, 100, merged.MaxRelevanceDocs)
	assert.Equal(t, 10, merged.MaxPatchesPerRun)
	assert.Equal(t, 4, merged.MaxConcurrentDocs)
}

// A partial override merges over the defaults (TS `{...DEFAULT_PIPELINE_BUDGETS,
// ...input.budgets}`): an ABSENT key keeps the default, a nil value sets that
// stage unbounded, a non-nil value overrides.
func TestBudgetOverridesMergeOverTheDefaults(t *testing.T) {
	defaults := orchestrator.DefaultPipelineBudgets()
	require.NotNil(t, defaults.StageBudgetMS[orchestrator.StageAnalyze])
	require.NotNil(t, defaults.StageBudgetMS[orchestrator.StageJev])
	assert.Equal(t, 5000, *defaults.StageBudgetMS[orchestrator.StageAnalyze])
	assert.Equal(t, 1000, *defaults.StageBudgetMS[orchestrator.StageJev])

	// Nil input leaves the defaults in force (the TS spread of `undefined`).
	assert.Equal(t, defaults, orchestrator.MergePipelineBudgets(nil))

	// The A10 probe's forced patch cap (5) sits below the 8-candidate fan-out
	// so the cap-overflow downgrade engages deterministically.
	merged := orchestrator.MergePipelineBudgets(&orchestrator.PipelineBudgetOverrides{
		MaxPatchesPerRun: orchPtr(5),
	})
	assert.Equal(t, 5, merged.MaxPatchesPerRun)
	// Absent members keep their defaults.
	assert.Equal(t, defaults.MaxDocsInvestigated, merged.MaxDocsInvestigated)
	assert.Equal(t, defaults.MaxRelevanceDocs, merged.MaxRelevanceDocs)
	assert.Equal(t, defaults.MaxConcurrentDocs, merged.MaxConcurrentDocs)
	// The stage map is per-key: absent key = keep default.
	require.NotNil(t, merged.StageBudgetMS[orchestrator.StageAnalyze])
	assert.Equal(t, 5000, *merged.StageBudgetMS[orchestrator.StageAnalyze])

	// A nil value sets that stage unbounded; a non-nil value overrides.
	unbounded := orchestrator.MergePipelineBudgets(&orchestrator.PipelineBudgetOverrides{
		StageBudgetMS: map[orchestrator.StageName]*int{
			orchestrator.StageAnalyze: nil,
			orchestrator.StageJev:     orchPtr(0),
		},
	})
	assert.Nil(t, unbounded.StageBudgetMS[orchestrator.StageAnalyze])
	require.NotNil(t, unbounded.StageBudgetMS[orchestrator.StageJev])
	assert.Equal(t, 0, *unbounded.StageBudgetMS[orchestrator.StageJev])
	assert.Nil(t, unbounded.StageBudgetMS[orchestrator.StageWriter], "writer stays unbounded by default")
}

/* ------------------------------------------- relevance context (graph → jev) */

// orchGraphFixture is the payments-shaped document graph the fixtures map
// (architecture ← everything, payments ← src/payments/**, security ← unmapped
// here so the `unmapped-candidate` provenance branch is exercised).
func orchGraphFixture() graph.DocumentGraph {
	return graph.DocumentGraph{
		Mappings: []graph.MappingRecord{
			{
				Doc:       "docs/architecture.md",
				Sources:   []string{"src/**"},
				Concepts:  []string{"refunds", "idempotency"},
				Contracts: []string{"payments-api"},
			},
			{
				Doc:      "docs/payments.md",
				Sources:  []string{"src/payments/**"},
				Concepts: []string{"idempotency"},
			},
		},
		ContractKinds: map[string]schemas.ContractKind{},
	}
}

// The bounded DOCUMENT GRAPH → JEV edge payload: candidate docs with their
// manifest modes, bounded `via` provenance trails (one mapping-source entry
// per declared source pattern, one concept entry per concept that includes
// the doc, or the single `unmapped-candidate` entry when no mapping record
// exists), contract names, concept names, and the caps. Pure — no I/O.
func TestBuildRelevanceContextDerivesProvenanceFromTheGraph(t *testing.T) {
	targets := []orchestrator.RelevanceDocTarget{
		{DocPath: "docs/architecture.md", Mode: workspace.WorkspaceModeSuggest},
		{DocPath: "docs/payments.md", Mode: workspace.WorkspaceModeAuto},
		{DocPath: "docs/security.md", Mode: workspace.WorkspaceModeLocked},
	}
	context, err := orchestrator.BuildRelevanceContext(orchGraphFixture(), targets, 5)
	require.NoError(t, err)

	require.Len(t, context.Docs, 3)
	// Declaration order for source patterns, then concept entries.
	assert.Equal(t, []orchestrator.RelevanceVia{
		{Kind: orchestrator.ViaKindMappingSource, Detail: "src/**"},
		{Kind: orchestrator.ViaKindConcept, Detail: "refunds"},
		{Kind: orchestrator.ViaKindConcept, Detail: "idempotency"},
	}, context.Docs[0].Via)
	assert.Equal(t, workspace.WorkspaceModeSuggest, context.Docs[0].Mode)
	assert.Equal(t, []string{"payments-api"}, context.Docs[0].Contracts)

	assert.Equal(t, []orchestrator.RelevanceVia{
		{Kind: orchestrator.ViaKindMappingSource, Detail: "src/payments/**"},
		{Kind: orchestrator.ViaKindConcept, Detail: "idempotency"},
	}, context.Docs[1].Via)
	// No contracts declared for the payments mapping → the empty list.
	assert.Equal(t, []string{}, context.Docs[1].Contracts)

	// No mapping record (and no derivable source pattern) → the single
	// `unmapped-candidate` via entry.
	assert.Equal(t, []orchestrator.RelevanceVia{
		{Kind: orchestrator.ViaKindMappingSource, Detail: "unmapped-candidate"},
	}, context.Docs[2].Via)
	assert.Equal(t, []string{}, context.Docs[2].Contracts)

	// Concepts are collected across records, deduped, in declaration order.
	assert.Equal(t, []string{"refunds", "idempotency"}, context.Concepts)
	assert.Equal(t, orchestrator.RelevanceCaps{MaxDocs: 5}, context.Caps)

	// Deterministic: same input → same frozen list.
	again, err := orchestrator.BuildRelevanceContext(orchGraphFixture(), targets, 5)
	require.NoError(t, err)
	assert.Equal(t, context, again)
}

// ARCH §3.3/§3.7 bounding: overflow is REPORTED (fail loud), never silently
// truncated — docPaths.length > maxDocs fails the retrieval stage, it does
// not return a shortened context.
func TestBuildRelevanceContextOverflowIsReportedNeverTruncated(t *testing.T) {
	targets := []orchestrator.RelevanceDocTarget{
		{DocPath: "docs/architecture.md", Mode: workspace.WorkspaceModeSuggest},
		{DocPath: "docs/payments.md", Mode: workspace.WorkspaceModeAuto},
	}
	context, err := orchestrator.BuildRelevanceContext(orchGraphFixture(), targets, 1)
	require.Error(t, err)
	assert.Equal(t, orchestrator.RelevanceContext{}, context, "overflow never yields a partial context")

	pipelineErr, ok := orchestrator.AsPipelineError(err)
	require.True(t, ok, "overflow must be a typed PipelineError")
	assert.Equal(t, orchestrator.CodePipeline, pipelineErr.Code())
	assert.Equal(t, orchestrator.StageRetrieval, pipelineErr.Stage())
	assert.Contains(t, pipelineErr.Detail(), "relevance context overflow")
	assert.Contains(t, pipelineErr.Detail(), "maxDocs=1")

	// Exactly at the bound is fine (the bound is inclusive).
	okContext, err := orchestrator.BuildRelevanceContext(orchGraphFixture(), targets, 2)
	require.NoError(t, err)
	assert.Len(t, okContext.Docs, 2)
}

// The typed config defect is distinguishable from a plain run failure (the
// exit-2 lane) — a config error is NOT an ablation.
func TestPipelineConfigErrorIsDistinguishableFromARunFailure(t *testing.T) {
	configErr := orchestrator.NewPipelineConfigError("jev rejected its own configuration", orchestrator.StageJev, nil)
	runErr := orchestrator.NewPipelineRunError("stage failed", orchestrator.StageJev, nil)

	assert.True(t, orchestrator.IsPipelineConfigError(configErr))
	assert.False(t, orchestrator.IsPipelineConfigError(runErr))

	pipelineErr, ok := orchestrator.AsPipelineError(configErr)
	require.True(t, ok)
	assert.Equal(t, orchestrator.CodePipelineConfig, pipelineErr.Code())
	assert.Equal(t, orchestrator.StageJev, pipelineErr.Stage())
	assert.Equal(t, "jev rejected its own configuration", pipelineErr.Detail())
	assert.True(t, strings.Contains(pipelineErr.Error(), "stage 'jev' failed"),
		"stage-scoped failures render the stage in the message")

	// An UNSCOPED failure renders the bare detail behind the coded identity
	// (the stage-contract lane carries no stage scope — TS super(detail,
	// undefined)).
	unscoped := orchestrator.NewPipelineRunError("stage failed", "", nil)
	assert.Equal(t, "EDOCSYNC_PIPELINE: stage failed", unscoped.Error())
	pipelineErr, ok = orchestrator.AsPipelineError(unscoped)
	require.True(t, ok)
	assert.Empty(t, pipelineErr.Stage())
	assert.Equal(t, "stage failed", pipelineErr.Detail())
}
