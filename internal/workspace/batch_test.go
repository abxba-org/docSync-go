package workspace_test

// Ported from packages/core/test/workspace-batch.test.ts (P3-T03; ARCH §7.1
// item 6, §3.7):
//   - shared-concept siblings batch together first (R16 groundwork);
//   - level-1/2 docs group by shared source-pattern overlap;
//   - remaining candidates are singleton sessions;
//   - groups larger than maxDocsPerSession split into consecutive chunks;
//   - sessions beyond maxSessionCount overflow → unbatched (reported — Phase 5
//     routes overflow to REVIEW, mirroring retrieval overflow semantics);
//   - the plan is deterministic and fail-closed on an unusable graph / bad caps.

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/retrieval"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ----------------------------------------------------------------- fixture */

var batchManifest = schemas.Manifest{
	Version: 1,
	Documents: []schemas.ManifestEntry{
		{Path: "docs/payments.md", Sources: []string{"src/payments/**"}},
		{Path: "docs/payments-sibling.md", Sources: []string{"src/sib/**"}},
		{Path: "docs/api.md", Sources: []string{"src/payments/**", "src/api/**"}},
		{Path: "docs/orbit.md", Sources: []string{"src/orbital/**"}},
		{Path: "docs/lonely.md", Sources: []string{"src/lonely/**"}},
	},
}

func batchConcepts() *schemas.ConceptsConfig {
	return &schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/payments.md"},
			{Path: "docs/payments-sibling.md"},
		},
		Concepts: []schemas.ConceptEntry{
			{Name: "refunds", Docs: []string{"docs/payments.md", "docs/payments-sibling.md"}},
		},
	}
}

// batchGraph is the composed document graph over the fixture (TS module-level
// `graph`).
func batchGraph(t *testing.T) graph.DocumentGraph {
	t.Helper()
	composed := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest: batchManifest,
		Concepts: batchConcepts(),
	})
	require.Empty(t, composed.Errors)
	return composed
}

// batchCandidate mirrors the TS candidate() fixture builder.
func batchCandidate(doc string, level retrieval.RetrievalLevel, semanticOnly bool) retrieval.RetrievalCandidate {
	maxAction := retrieval.MaxActionAuto
	if semanticOnly {
		maxAction = retrieval.MaxActionReview
	}
	return retrieval.RetrievalCandidate{
		Doc:          doc,
		Level:        level,
		Levels:       []retrieval.RetrievalLevel{level},
		Provenance:   []retrieval.RetrievalProvenance{{Level: level, Source: "manifest-source:" + doc}},
		SemanticOnly: semanticOnly,
		MaxAction:    maxAction,
	}
}

func requirePlan(t *testing.T, input workspace.BatchRelatedDocsInput) *workspace.BatchPlan {
	t.Helper()
	plan, err := workspace.BatchRelatedDocs(input)
	require.NoError(t, err)
	require.NotNil(t, plan)
	return plan
}

func requireBatchError(t *testing.T, input workspace.BatchRelatedDocsInput) error {
	t.Helper()
	plan, err := workspace.BatchRelatedDocs(input)
	require.Error(t, err)
	require.Nil(t, plan)
	return err
}

/* -------------------------------------------------------------------- tests */

func TestBatchRelatedDocs_SharedConceptSiblingsBatchFirst(t *testing.T) {
	plan := requirePlan(t, workspace.BatchRelatedDocsInput{
		Candidates: []retrieval.RetrievalCandidate{
			batchCandidate("docs/payments.md", retrieval.RetrievalLevel1, false),
			batchCandidate("docs/payments-sibling.md", retrieval.RetrievalLevel5, false),
		},
		Graph:             batchGraph(t),
		MaxDocsPerSession: 5,
		MaxSessionCount:   5,
	})
	require.Len(t, plan.Sessions, 1)
	assert.Equal(t, []string{"docs/payments.md", "docs/payments-sibling.md"}, plan.Sessions[0].Docs)
	assert.Contains(t, plan.Sessions[0].Reason, "shared concept(s): refunds")
	assert.Equal(t, []string{}, plan.Unbatched)
}

func TestBatchRelatedDocs_Level12GroupBySharedSourcePatternExactEquality(t *testing.T) {
	// 'docs/payments.md' and 'docs/api.md' share the LITERAL pattern
	// 'src/payments/**' — they batch even without any concept link.
	plan := requirePlan(t, workspace.BatchRelatedDocsInput{
		Candidates: []retrieval.RetrievalCandidate{
			batchCandidate("docs/payments.md", retrieval.RetrievalLevel2, false),
			batchCandidate("docs/api.md", retrieval.RetrievalLevel2, false),
		},
		Graph:             batchGraph(t),
		MaxDocsPerSession: 5,
		MaxSessionCount:   5,
	})
	require.Len(t, plan.Sessions, 1)
	assert.Equal(t, []string{"docs/payments.md", "docs/api.md"}, plan.Sessions[0].Docs)
	assert.Contains(t, plan.Sessions[0].Reason, "shared source pattern(s): src/payments/**")
}

func TestBatchRelatedDocs_ConceptGroupingPrecedesUnrelatedSingletons(t *testing.T) {
	plan := requirePlan(t, workspace.BatchRelatedDocsInput{
		Candidates: []retrieval.RetrievalCandidate{
			batchCandidate("docs/payments.md", retrieval.RetrievalLevel1, false),
			batchCandidate("docs/payments-sibling.md", retrieval.RetrievalLevel5, false),
			batchCandidate("docs/orbit.md", retrieval.RetrievalLevel2, false),
			batchCandidate("docs/lonely.md", retrieval.RetrievalLevel6, true),
		},
		Graph:             batchGraph(t),
		MaxDocsPerSession: 5,
		MaxSessionCount:   5,
	})
	// Session 1: the concept siblings; sessions 2 and 3: singletons.
	docs := make([][]string, 0, len(plan.Sessions))
	for _, session := range plan.Sessions {
		docs = append(docs, session.Docs)
	}
	assert.Equal(t, [][]string{
		{"docs/payments.md", "docs/payments-sibling.md"},
		{"docs/orbit.md"},
		{"docs/lonely.md"},
	}, docs)
	assert.Contains(t, plan.Sessions[1].Reason, "isolated session")
	assert.Contains(t, plan.Sessions[2].Reason, "isolated session")
	assert.Equal(t, []string{}, plan.Unbatched)
}

func TestBatchRelatedDocs_OversizedGroupsSplitIntoConsecutiveChunks(t *testing.T) {
	plan := requirePlan(t, workspace.BatchRelatedDocsInput{
		Candidates: []retrieval.RetrievalCandidate{
			batchCandidate("docs/payments.md", retrieval.RetrievalLevel1, false),
			batchCandidate("docs/payments-sibling.md", retrieval.RetrievalLevel5, false),
			batchCandidate("docs/api.md", retrieval.RetrievalLevel2, false),
			batchCandidate("docs/orbit.md", retrieval.RetrievalLevel2, false),
		},
		Graph:             batchGraph(t),
		MaxDocsPerSession: 2,
		MaxSessionCount:   10,
	})
	// 'docs/payments.md'+'docs/api.md' share 'src/payments/**' and all four
	// form ONE pattern group (payments/api via src/payments/**; orbit alone)
	// — actually payments+api+orbit are NOT all linked: orbit shares nothing.
	assert.Equal(t, []string{}, plan.Unbatched)
	assert.GreaterOrEqual(t, len(plan.Sessions), 2)
	for _, session := range plan.Sessions {
		assert.LessOrEqual(t, len(session.Docs), 2)
	}
}

func TestBatchRelatedDocs_SessionsBeyondMaxCountOverflowToUnbatched(t *testing.T) {
	plan := requirePlan(t, workspace.BatchRelatedDocsInput{
		Candidates: []retrieval.RetrievalCandidate{
			batchCandidate("docs/payments.md", retrieval.RetrievalLevel1, false),
			batchCandidate("docs/payments-sibling.md", retrieval.RetrievalLevel5, false),
			batchCandidate("docs/orbit.md", retrieval.RetrievalLevel2, false),
			batchCandidate("docs/lonely.md", retrieval.RetrievalLevel6, true),
		},
		Graph:             batchGraph(t),
		MaxDocsPerSession: 5,
		MaxSessionCount:   2,
	})
	assert.Len(t, plan.Sessions, 2)
	assert.Equal(t, []string{"docs/lonely.md"}, plan.Unbatched)
}

func TestBatchRelatedDocs_PlanIsDeterministic(t *testing.T) {
	candidates := []retrieval.RetrievalCandidate{
		batchCandidate("docs/payments.md", retrieval.RetrievalLevel1, false),
		batchCandidate("docs/payments-sibling.md", retrieval.RetrievalLevel5, false),
		batchCandidate("docs/api.md", retrieval.RetrievalLevel2, false),
		batchCandidate("docs/orbit.md", retrieval.RetrievalLevel2, false),
		batchCandidate("docs/lonely.md", retrieval.RetrievalLevel6, true),
	}
	build := func() workspace.BatchRelatedDocsInput {
		return workspace.BatchRelatedDocsInput{
			Candidates:        candidates,
			Graph:             batchGraph(t),
			MaxDocsPerSession: 3,
			MaxSessionCount:   4,
		}
	}
	assert.Equal(t, requirePlan(t, build()), requirePlan(t, build()))
}

func TestBatchRelatedDocs_EmptyCandidatesYieldEmptyPlan(t *testing.T) {
	plan := requirePlan(t, workspace.BatchRelatedDocsInput{
		Candidates:        []retrieval.RetrievalCandidate{},
		Graph:             batchGraph(t),
		MaxDocsPerSession: 5,
		MaxSessionCount:   5,
	})
	assert.Equal(t, &workspace.BatchPlan{Sessions: []workspace.BatchSession{}, Unbatched: []string{}}, plan)
}

func TestBatchRelatedDocs_FailClosedUnusableGraphAndBadCaps(t *testing.T) {
	broken := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest: batchManifest,
		Concepts: &schemas.ConceptsConfig{
			Schema: "docsync.concepts/v1",
			Docs:   []schemas.ConceptsDocEntry{},
			Concepts: []schemas.ConceptEntry{
				{Name: "dup", Docs: []string{"docs/payments.md"}},
				{Name: "dup", Docs: []string{"docs/api.md"}},
			},
		},
	})
	require.NotEmpty(t, broken.Errors)

	// An unusable graph returns the SAME shared typed error as every other
	// routing consumer (fail closed, D8).
	var unusable *graph.GraphUnusableError
	require.ErrorAs(t, requireBatchError(t, workspace.BatchRelatedDocsInput{
		Candidates:        []retrieval.RetrievalCandidate{batchCandidate("docs/payments.md", retrieval.RetrievalLevel1, false)},
		Graph:             broken,
		MaxDocsPerSession: 5,
		MaxSessionCount:   5,
	}), &unusable)

	// Malformed caps are the batching-specific typed error.
	var badDocs *workspace.BatchConfigError
	require.ErrorAs(t, requireBatchError(t, workspace.BatchRelatedDocsInput{
		Candidates:        []retrieval.RetrievalCandidate{},
		Graph:             batchGraph(t),
		MaxDocsPerSession: 0,
		MaxSessionCount:   5,
	}), &badDocs)
	assert.Contains(t, badDocs.Message, "maxDocsPerSession must be a positive integer, got 0")

	var badSessions *workspace.BatchConfigError
	require.ErrorAs(t, requireBatchError(t, workspace.BatchRelatedDocsInput{
		Candidates:        []retrieval.RetrievalCandidate{},
		Graph:             batchGraph(t),
		MaxDocsPerSession: 5,
		MaxSessionCount:   -1,
	}), &badSessions)
	assert.Contains(t, badSessions.Message, "maxSessionCount must be a positive integer, got -1")
}

func TestBatchRelatedDocs_SessionReasonsCarryBatchPartSuffix(t *testing.T) {
	// Three concept-linked docs with maxDocsPerSession=2 split into two chunks
	// whose reasons are disambiguated by the deterministic part suffix.
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/a.md", Sources: []string{"src/a/**"}},
			{Path: "docs/b.md", Sources: []string{"src/b/**"}},
			{Path: "docs/c.md", Sources: []string{"src/c/**"}},
		},
	}
	concepts := &schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/a.md"}, {Path: "docs/b.md"}, {Path: "docs/c.md"},
		},
		Concepts: []schemas.ConceptEntry{
			{Name: "shared", Docs: []string{"docs/a.md", "docs/b.md", "docs/c.md"}},
		},
	}
	composed := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest: manifest,
		Concepts: concepts,
	})
	require.Empty(t, composed.Errors)

	plan := requirePlan(t, workspace.BatchRelatedDocsInput{
		Candidates: []retrieval.RetrievalCandidate{
			batchCandidate("docs/a.md", retrieval.RetrievalLevel1, false),
			batchCandidate("docs/b.md", retrieval.RetrievalLevel1, false),
			batchCandidate("docs/c.md", retrieval.RetrievalLevel1, false),
		},
		Graph:             composed,
		MaxDocsPerSession: 2,
		MaxSessionCount:   5,
	})
	require.Len(t, plan.Sessions, 2)
	assert.Equal(t, "shared concept(s): shared (batch part 1/2)", plan.Sessions[0].Reason)
	assert.Equal(t, "shared concept(s): shared (batch part 2/2)", plan.Sessions[1].Reason)
}
