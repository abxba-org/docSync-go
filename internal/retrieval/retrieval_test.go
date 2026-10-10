package retrieval_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/retrieval"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Retrieval engine tests (P1-T13; ARCH §4.5):
//   - V-P1-8: semantic-only candidates cannot drive any automatic action;
//   - levels 3–4 declared inactive with coverage-gap entries (D14/OD-4);
//   - level 1 (literal) vs level 2 (glob) matching and level-5 sibling
//     traversal (concept siblings + claim concepts, single hop);
//   - provenance shape, deterministic ranking, per-level caps + overflow;
//   - levels 3–4 activation with MANDATORY per-level structured provenance
//     (P11-T04; F11-2), absent evidence ⇒ declared gap row, never silence.
//
// Ported from packages/core/test/retrieval-engine.test.ts and
// packages/core/test/retrieval-levels.test.ts (black-box, testify).

/* --------------------------------------------------------------- fixtures */

func strp(value string) *string { return &value }
func boolp(value bool) *bool    { return &value }
func intp(value int) *int       { return &value }

const changeAnalysisSchemaID = "docsync.change-analysis/v1"

// engineManifest is the MANIFEST fixture of retrieval-engine.test.ts.
var engineManifest = schemas.Manifest{
	Version: 1,
	Documents: []schemas.ManifestEntry{
		{Path: "docs/api.md", Sources: []string{"src/api/server.ts"}},
		{Path: "docs/payments.md", Sources: []string{"src/payments/**"}},
		{Path: "docs/both.md", Sources: []string{"src/both/**", "src/both/exact.ts"}},
		{Path: "docs/zeta.md", Sources: []string{"src/zeta/**"}},
		{Path: "docs/kappa.md", Sources: []string{"src/kappa/**"}},
		{Path: "docs/antigravity.md", Sources: []string{"src/antigravity/**"}},
		{Path: "docs/payments-sibling.md", Sources: []string{"src/sib/**"}},
		{Path: "docs/claim-doc.md", Sources: []string{"src/claims/**"}},
		{Path: "docs/orbit.md", Sources: []string{"src/orbital/**"}},
	},
}

// engineConcepts is the CONCEPTS fixture of retrieval-engine.test.ts.
var engineConcepts = &schemas.ConceptsConfig{
	Schema: "docsync.concepts/v1",
	Docs: []schemas.ConceptsDocEntry{
		{Path: "docs/payments.md", Contracts: []string{"refund_flow"}},
		{Path: "docs/payments-sibling.md"},
		{Path: "docs/claim-doc.md"},
	},
	Concepts: []schemas.ConceptEntry{
		{Name: "refunds", Docs: []string{"docs/payments.md", "docs/payments-sibling.md"}},
	},
}

// engineClaims is the CLAIMS fixture of retrieval-engine.test.ts (fresh copy per call).
func engineClaims() []schemas.Claim {
	return []schemas.Claim{
		{
			ID:      "claim_1",
			Text:    "Refunds require an idempotency key.",
			Anchor:  schemas.ClaimAnchor{File: "docs/claim-doc.md", StartLine: 1, EndLine: 2, ContentHash: "deadbeef"},
			Source:  schemas.ClaimSource{File: "src/payments/refund.ts", BlobSHA: "abc1234"},
			Concept: strp("refunds"),
			Status:  "verified",
			History: []schemas.ClaimHistoryEvent{},
		},
	}
}

func analysisOf(files []schemas.ChangedFile) schemas.ChangeAnalysis {
	return schemas.ChangeAnalysis{
		Schema:              changeAnalysisSchemaID,
		RunID:               "acme/payments#base..head",
		Repo:                "acme/payments",
		BaseSHA:             "base",
		HeadSHA:             "head",
		Files:               files,
		DependenciesChanged: false,
		ClassificationHints: []schemas.ClassificationHint{},
		DeepAnalysis:        schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: strp("test fixture")},
		AnalyzerCoverage:    []schemas.AnalyzerCoverage{{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK}},
	}
}

// retrieveOptions carries the optional engine inputs of the TS helpers.
type retrieveOptions struct {
	semanticIndex    *retrieval.SemanticIndex
	docContentHashes map[string]string
	caps             retrieval.RetrievalCaps
	apiDelta         *retrieval.ApiDelta
	configDiff       []retrieval.ConfigKeyDiff
}

// buildGraph composes the graph from the given manifest + concepts.
func buildGraph(manifest schemas.Manifest, concepts *schemas.ConceptsConfig) graph.DocumentGraph {
	return graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: manifest, Concepts: concepts})
}

// retrieve mirrors the TS `retrieve` helper over the ENGINE fixtures: claims == nil
// means the CLAIMS fixture, an empty (non-nil) slice means no claims at all.
func retrieve(t *testing.T, files []schemas.ChangedFile, claims []schemas.Claim, options retrieveOptions) retrieval.RetrievalResult {
	t.Helper()
	if claims == nil {
		claims = engineClaims()
	}
	result, err := retrieval.RetrieveCandidates(retrieval.RetrieveCandidatesInput{
		ChangeAnalysis:   analysisOf(files),
		Graph:            buildGraph(engineManifest, engineConcepts),
		Manifest:         engineManifest,
		Claims:           claims,
		SemanticIndex:    options.semanticIndex,
		DocContentHashes: options.docContentHashes,
		Caps:             options.caps,
		ApiDelta:         options.apiDelta,
		ConfigDiff:       options.configDiff,
	})
	require.NoError(t, err)
	return result
}

// findCandidate returns the candidate for doc (fails the test when absent).
func findCandidate(t *testing.T, result retrieval.RetrievalResult, doc string) retrieval.RetrievalCandidate {
	t.Helper()
	for _, candidate := range result.Candidates {
		if candidate.Doc == doc {
			return candidate
		}
	}
	t.Fatalf("expected a candidate for %s, got %+v", doc, result.Candidates)
	return retrieval.RetrievalCandidate{}
}

// docPaths maps candidates to their doc paths (TS `candidates.map(c => c.doc)`).
func docPaths(candidates []retrieval.RetrievalCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.Doc)
	}
	return out
}

// indexSnapshot renders an index deterministically (TS `snapshot`), so the
// purity test can prove the engine never mutated the injected index.
func indexSnapshot(t *testing.T, index *retrieval.SemanticIndex) string {
	t.Helper()
	data, err := json.Marshal(index)
	require.NoError(t, err)
	return string(data)
}

/* --------------------------------------- levels 1–2: literal vs glob sources */

func TestRetrievalEngine_Level1_LiteralSourceMatchedByExactPathEquality(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/api/server.ts", Status: schemas.FileStatusModified}}, nil, retrieveOptions{})
	require.Len(t, result.Candidates, 1)
	candidate := result.Candidates[0]
	assert.Equal(t, "docs/api.md", candidate.Doc)
	assert.Equal(t, retrieval.RetrievalLevel1, candidate.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel1}, candidate.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel1, Source: "manifest-source:src/api/server.ts"},
	}, candidate.Provenance)
	assert.False(t, candidate.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionAuto, candidate.MaxAction)
}

func TestRetrievalEngine_Level2_GlobSourceMatchedThroughTheGlobEngine(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified}}, []schemas.Claim{}, retrieveOptions{})
	candidate := findCandidate(t, result, "docs/payments.md")
	assert.Equal(t, retrieval.RetrievalLevel2, candidate.Level)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel2, Source: "manifest-source:src/payments/**"},
	}, candidate.Provenance)
}

func TestRetrievalEngine_LiteralBeatsGlobWithinOneDocAndPath(t *testing.T) {
	literalOnly := retrieve(t, []schemas.ChangedFile{{Path: "src/both/exact.ts", Status: schemas.FileStatusModified}}, nil, retrieveOptions{})
	both := findCandidate(t, literalOnly, "docs/both.md")
	assert.Equal(t, retrieval.RetrievalLevel1, both.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel1}, both.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel1, Source: "manifest-source:src/both/exact.ts"},
	}, both.Provenance)

	globOnly := retrieve(t, []schemas.ChangedFile{{Path: "src/both/other.ts", Status: schemas.FileStatusModified}}, nil, retrieveOptions{})
	viaGlob := findCandidate(t, globOnly, "docs/both.md")
	assert.Equal(t, retrieval.RetrievalLevel2, viaGlob.Level)

	mixed := retrieve(t, []schemas.ChangedFile{
		{Path: "src/both/exact.ts", Status: schemas.FileStatusModified},
		{Path: "src/both/other.ts", Status: schemas.FileStatusModified},
	}, nil, retrieveOptions{})
	multi := findCandidate(t, mixed, "docs/both.md")
	assert.Equal(t, retrieval.RetrievalLevel1, multi.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel1, retrieval.RetrievalLevel2}, multi.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel1, Source: "manifest-source:src/both/exact.ts"},
		{Level: retrieval.RetrievalLevel2, Source: "manifest-source:src/both/**"},
	}, multi.Provenance)
}

func TestRetrievalEngine_UnmatchedChangedPathElectsNoCandidate(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/nowhere/file.ts", Status: schemas.FileStatusAdded}}, nil, retrieveOptions{})
	assert.Empty(t, result.Candidates)
	assert.Empty(t, result.Overflow)
}

/* --------------------- level 5: sibling docs through concept nodes (one hop) */

func TestRetrievalEngine_Level5_ConceptSiblingsOfALevel2Seed(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified}}, nil, retrieveOptions{})
	sibling := findCandidate(t, result, "docs/payments-sibling.md")
	assert.Equal(t, retrieval.RetrievalLevel5, sibling.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel5}, sibling.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel5, Source: "concept:refunds"},
	}, sibling.Provenance)
	assert.False(t, sibling.SemanticOnly)
	// The seed keeps its best level (2); it is NOT its own sibling.
	seed := findCandidate(t, result, "docs/payments.md")
	assert.Equal(t, retrieval.RetrievalLevel2, seed.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel2}, seed.Levels)
}

func TestRetrievalEngine_Level5_ClaimConceptsAnchorTheirDoc(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified}}, nil, retrieveOptions{})
	claimDoc := findCandidate(t, result, "docs/claim-doc.md")
	assert.Equal(t, retrieval.RetrievalLevel5, claimDoc.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel5}, claimDoc.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel5, Source: "claim:claim_1"},
	}, claimDoc.Provenance)
}

func TestRetrievalEngine_Level5_ClaimsWithoutConceptOrOutsideManifestContributeNothing(t *testing.T) {
	claims := []schemas.Claim{}
	for _, claim := range engineClaims() {
		claims = append(claims, claim)
	}
	plain := claims[0]
	plain.ID = "claim_plain"
	plain.Concept = nil
	ghost := claims[0]
	ghost.ID = "claim_ghost"
	ghost.Anchor = schemas.ClaimAnchor{File: "docs/ghost.md", StartLine: 1, EndLine: 2, ContentHash: "deadbeef"}

	result := retrieve(t, []schemas.ChangedFile{{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified}}, []schemas.Claim{plain, ghost}, retrieveOptions{})
	assert.NotContains(t, docPaths(result.Candidates), "docs/ghost.md")
	for _, candidate := range result.Candidates {
		assert.NotEqual(t, "docs/claim-doc.md", candidate.Doc)
	}
}

func TestRetrievalEngine_Level5_SeedDocsWithoutConceptsElectNoSiblings(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/zeta/one.ts", Status: schemas.FileStatusModified}}, nil, retrieveOptions{})
	zeta := findCandidate(t, result, "docs/zeta.md")
	assert.Equal(t, retrieval.RetrievalLevel2, zeta.Level)
	for _, candidate := range result.Candidates {
		assert.NotContains(t, candidate.Levels, retrieval.RetrievalLevel5)
	}
}

/* -------------------------- level 6: semantic fallback recall (lexical index) */

func level6Index() *retrieval.SemanticIndex {
	return retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
		{Path: "docs/orbit.md", Content: "quantum flux engine"},
		{Path: "docs/payments.md", Content: "refund payments idempotency"},
	})
}

func TestRetrievalEngine_Level6_ChangedPathWithoutDeterministicMatchFallsBackToSemanticOnly(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/quantum/flux.ts", Status: schemas.FileStatusAdded}}, nil,
		retrieveOptions{semanticIndex: level6Index()})
	candidate := findCandidate(t, result, "docs/orbit.md")
	assert.Equal(t, retrieval.RetrievalLevel6, candidate.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel6}, candidate.Levels)
	assert.True(t, candidate.SemanticOnly)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel6, Source: "semantic-index", Score: intp(20)},
	}, candidate.Provenance)
}

func TestRetrievalEngine_Level6_CorroboratedDocKeepsLevel2AndListsAllProvenance(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified}}, nil,
		retrieveOptions{semanticIndex: level6Index()})
	candidate := findCandidate(t, result, "docs/payments.md")
	assert.Equal(t, retrieval.RetrievalLevel2, candidate.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel2, retrieval.RetrievalLevel6}, candidate.Levels)
	assert.False(t, candidate.SemanticOnly)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel2, Source: "manifest-source:src/payments/**"},
		{Level: retrieval.RetrievalLevel6, Source: "semantic-index", Score: intp(20)},
	}, candidate.Provenance)
}

func TestRetrievalEngine_Level6_FormattingOnlyChangedFilesFeedNoSemanticQuery(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{
		{Path: "src/quantum/flux.ts", Status: schemas.FileStatusModified, FormattingOnly: boolp(true)},
	}, nil, retrieveOptions{semanticIndex: level6Index()})
	assert.Empty(t, result.Candidates)
}

func TestRetrievalEngine_Level6_RenameOldPathFeedsTheSemanticQuery(t *testing.T) {
	renameIndex := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{{Path: "docs/orbit.md", Content: "orbital moon"}})
	result := retrieve(t, []schemas.ChangedFile{
		{Path: "src/nowhere/file.ts", Status: schemas.FileStatusRenamed, OldPath: strp("src/orbital/moon.ts")},
	}, nil, retrieveOptions{semanticIndex: renameIndex})
	assert.Equal(t, []string{"docs/orbit.md"}, docPaths(result.Candidates))
	assert.True(t, result.Candidates[0].SemanticOnly)
}

func TestRetrievalEngine_Level6_StalenessFlowsIntoProvenanceFromDocContentHashes(t *testing.T) {
	files := []schemas.ChangedFile{{Path: "src/quantum/flux.ts", Status: schemas.FileStatusAdded}}

	stale := retrieve(t, files, nil, retrieveOptions{
		semanticIndex:    level6Index(),
		docContentHashes: map[string]string{"docs/orbit.md": "not-the-shard-hash"},
	})
	assert.Equal(t, retrieval.RetrievalProvenance{
		Level: retrieval.RetrievalLevel6, Source: "semantic-index", Score: intp(20), Stale: boolp(true),
	}, findCandidate(t, stale, "docs/orbit.md").Provenance[0])

	fresh := retrieve(t, files, nil, retrieveOptions{
		semanticIndex:    level6Index(),
		docContentHashes: map[string]string{"docs/orbit.md": docutil.ContentHash("quantum flux engine")},
	})
	assert.Equal(t, retrieval.RetrievalProvenance{
		Level: retrieval.RetrievalLevel6, Source: "semantic-index", Score: intp(20),
	}, fresh.Candidates[0].Provenance[0])

	unknown := retrieve(t, files, nil, retrieveOptions{semanticIndex: level6Index()})
	assert.Equal(t, retrieval.RetrievalProvenance{
		Level: retrieval.RetrievalLevel6, Source: "semantic-index", Score: intp(20),
	}, unknown.Candidates[0].Provenance[0])
}

func TestRetrievalEngine_Level6_EngineNeverMutatesTheInjectedIndex(t *testing.T) {
	index := level6Index()
	before := indexSnapshot(t, index)
	retrieve(t, []schemas.ChangedFile{{Path: "src/quantum/flux.ts", Status: schemas.FileStatusAdded}}, nil,
		retrieveOptions{semanticIndex: index})
	assert.Equal(t, before, indexSnapshot(t, index))
}

/* ------------- V-P1-8: semantic-only candidates cannot drive automation */

func TestRetrievalEngine_VP18_SemanticOnlyCandidateCannotDriveAutomaticAction(t *testing.T) {
	semanticIndex := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{{Path: "docs/orbit.md", Content: "quantum flux engine"}})
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/quantum/flux.ts", Status: schemas.FileStatusAdded}}, nil,
		retrieveOptions{semanticIndex: semanticIndex})
	semanticOnly := findCandidate(t, result, "docs/orbit.md")
	assert.True(t, semanticOnly.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionReview, semanticOnly.MaxAction)
	assert.False(t, retrieval.CanDriveAutomaticAction(semanticOnly))

	corroborated := retrieve(t, []schemas.ChangedFile{{Path: "src/api/server.ts", Status: schemas.FileStatusModified}}, nil, retrieveOptions{})
	literal := corroborated.Candidates[0]
	assert.False(t, literal.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionAuto, literal.MaxAction)
	assert.True(t, retrieval.CanDriveAutomaticAction(literal))
}

/* ----------- levels 3–4 declared inactive with coverage-gap entries */

func TestRetrievalEngine_Levels34_DeclaredInactiveWithCoverageGapEntries(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/api/server.ts", Status: schemas.FileStatusModified}}, nil, retrieveOptions{})
	assert.Equal(t, []retrieval.RetrievalCoverageGap{
		{Level: retrieval.InactiveLevel3, Status: "inactive", Reason: retrieval.InactiveLevelReason},
		{Level: retrieval.InactiveLevel4, Status: "inactive", Reason: retrieval.InactiveLevelReason},
	}, result.CoverageGaps)
	assert.Contains(t, retrieval.InactiveLevelReason, "Phase 11 (F11-2)")
	assert.Contains(t, retrieval.InactiveLevelReason, "declared inactive, never silent")
}

func TestRetrievalEngine_CandidatesNeverCarryAnInactiveLevel(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{
		{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified},
		{Path: "src/quantum/flux.ts", Status: schemas.FileStatusAdded},
	}, nil, retrieveOptions{})
	for _, candidate := range result.Candidates {
		for _, level := range candidate.Levels {
			assert.Contains(t, []retrieval.RetrievalLevel{
				retrieval.RetrievalLevel1, retrieval.RetrievalLevel2,
				retrieval.RetrievalLevel5, retrieval.RetrievalLevel6,
			}, level)
		}
	}
}

/* ------------------------- deterministic ranking + per-level caps + overflow */

func TestRetrievalEngine_RankingLevelAscendingScoreDescendingPathAscending(t *testing.T) {
	rankIndex := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
		{Path: "docs/orbit.md", Content: "quantum flux engine"},
		{Path: "docs/antigravity.md", Content: "quantum flux drive"},
	})
	files := []schemas.ChangedFile{
		{Path: "src/zeta/one.ts", Status: schemas.FileStatusModified},
		{Path: "src/quantum/flux.ts", Status: schemas.FileStatusAdded},
	}
	result := retrieve(t, files, []schemas.Claim{}, retrieveOptions{semanticIndex: rankIndex})
	assert.Equal(t, []string{
		"docs/zeta.md", // level 2
		// level 6, score-20 tie → path ascending
		"docs/antigravity.md",
		"docs/orbit.md",
	}, docPaths(result.Candidates))
	// Deterministic: identical inputs → identical result.
	again := retrieve(t, files, []schemas.Claim{}, retrieveOptions{semanticIndex: rankIndex})
	assert.Equal(t, result, again)
}

func TestRetrievalEngine_CapsTruncateDeterministicallyAndReportOverflow(t *testing.T) {
	files := []schemas.ChangedFile{
		{Path: "src/kappa/one.ts", Status: schemas.FileStatusModified},
		{Path: "src/zeta/two.ts", Status: schemas.FileStatusModified},
	}
	uncapped := retrieve(t, files, []schemas.Claim{}, retrieveOptions{})
	assert.Equal(t, []string{"docs/kappa.md", "docs/zeta.md"}, docPaths(uncapped.Candidates))
	assert.Empty(t, uncapped.Overflow)

	capped := retrieve(t, files, []schemas.Claim{}, retrieveOptions{caps: retrieval.RetrievalCaps{retrieval.RetrievalLevel2: 1}})
	assert.Equal(t, []string{"docs/kappa.md"}, docPaths(capped.Candidates))
	assert.Equal(t, []retrieval.RetrievalOverflow{{Level: retrieval.RetrievalLevel2, Dropped: 1}}, capped.Overflow)
	assert.Equal(t, retrieval.RetrievalCaps{retrieval.RetrievalLevel2: 1}, capped.Caps)
}

func TestRetrievalEngine_SemanticCapsKeepTheHighestScoredCandidatesFirst(t *testing.T) {
	capIndex := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
		{Path: "docs/orbit.md", Content: "quantum flux engine"},
		{Path: "docs/antigravity.md", Content: "quantum"},
	})
	result := retrieve(t, []schemas.ChangedFile{{Path: "src/quantum/flux.ts", Status: schemas.FileStatusAdded}}, nil,
		retrieveOptions{semanticIndex: capIndex, caps: retrieval.RetrievalCaps{retrieval.RetrievalLevel6: 1}})
	assert.Equal(t, []string{"docs/orbit.md"}, docPaths(result.Candidates))
	require.Len(t, result.Candidates[0].Provenance, 1)
	assert.Equal(t, 20, *result.Candidates[0].Provenance[0].Score)
	assert.Equal(t, []retrieval.RetrievalOverflow{{Level: retrieval.RetrievalLevel6, Dropped: 1}}, result.Overflow)
}

func TestRetrievalEngine_CoverageGapsSurviveEmptyChangesAndCapsEchoEmpty(t *testing.T) {
	result := retrieve(t, []schemas.ChangedFile{}, []schemas.Claim{}, retrieveOptions{})
	assert.Equal(t, retrieval.RetrievalResult{
		Candidates: []retrieval.RetrievalCandidate{},
		CoverageGaps: []retrieval.RetrievalCoverageGap{
			{Level: retrieval.InactiveLevel3, Status: "inactive", Reason: retrieval.InactiveLevelReason},
			{Level: retrieval.InactiveLevel4, Status: "inactive", Reason: retrieval.InactiveLevelReason},
		},
		Overflow: []retrieval.RetrievalOverflow{},
		Caps:     retrieval.RetrievalCaps{},
	}, result)
}

/* -------------------------------- fail closed on unusable inputs */

func TestRetrievalEngine_UnusableGraphThrowsTheSharedGraphUnusableError(t *testing.T) {
	brokenConcepts := &schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs:   []schemas.ConceptsDocEntry{{Path: "docs/payments.md"}},
		Concepts: []schemas.ConceptEntry{
			{Name: "ghost-ref", Docs: []string{"docs/payments.md", "docs/ghost.md"}},
		},
	}
	broken := buildGraph(engineManifest, brokenConcepts)
	require.NotEmpty(t, broken.Errors)

	// SAME error type as the suspect driver (single home, graph/errors.go);
	// RetrievalConfigError remains retrieval-specific (caps only).
	_, err := retrieval.RetrieveCandidates(retrieval.RetrieveCandidatesInput{
		ChangeAnalysis: analysisOf([]schemas.ChangedFile{{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified}}),
		Graph:          broken,
		Manifest:       engineManifest,
	})
	var graphErr *graph.GraphUnusableError
	require.Error(t, err)
	assert.ErrorAs(t, err, &graphErr)
}

func TestRetrievalEngine_NegativeOrNonIntegerCapsAreRefused(t *testing.T) {
	_, negativeErr := retrieval.RetrieveCandidates(retrieval.RetrieveCandidatesInput{
		ChangeAnalysis: analysisOf([]schemas.ChangedFile{}),
		Graph:          buildGraph(engineManifest, engineConcepts),
		Manifest:       engineManifest,
		Caps:           retrieval.RetrievalCaps{retrieval.RetrievalLevel2: -1},
	})
	var negativeConfigErr *retrieval.RetrievalConfigError
	require.Error(t, negativeErr)
	assert.ErrorAs(t, negativeErr, &negativeConfigErr)

	_, nonIntegerErr := retrieval.ParseRetrievalCaps(map[retrieval.RetrievalLevel]float64{retrieval.RetrievalLevel5: 1.5})
	var nonIntegerConfigErr *retrieval.RetrievalConfigError
	require.Error(t, nonIntegerErr)
	assert.ErrorAs(t, nonIntegerErr, &nonIntegerConfigErr)
}

/* ------------------------------------------------------------------ */

// The fixtures and helpers below port retrieval-levels.test.ts (P11-T04; F11-2):
// levels 3–4 activation, MANDATORY per-level structured provenance, absent
// evidence ⇒ declared gap row, and levels 1/2/5/6 outputs unchanged.

var levelsManifest = schemas.Manifest{
	Version: 1,
	Documents: []schemas.ManifestEntry{
		{Path: "docs/api.md", Sources: []string{"src/api/**"}},
		{Path: "docs/refunds.md", Sources: []string{"src/refunds/**"}},
		{Path: "docs/config.md", Sources: []string{"src/config/**"}},
		{Path: "docs/deployment.md", Sources: []string{"src/deploy/**"}},
	},
}

var levelsDocContents = []retrieval.SemanticDoc{
	{Path: "docs/api.md", Content: "The endpoint /payments/{id} returns one Payment."},
	{Path: "docs/refunds.md", Content: "Refunds require an idempotencyKey header."},
	{Path: "docs/config.md", Content: "Set server.port before boot."},
	{Path: "docs/deployment.md", Content: "Deploy with care."},
}

// levelsSymbolsDetail is the composed symbols_detail fixture (schema-example
// shape, SYMBOLS_DETAIL_SCHEMA).
var levelsSymbolsDetail = schemas.SymbolsDetail{
	Added:   []string{},
	Removed: []string{},
	Changed: []schemas.SymbolChangeDetail{{Name: "idempotencyKey", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeType}}},
}

// levelsClaims is the CLAIMS fixture of retrieval-levels.test.ts (fresh copy per call).
func levelsClaims() []schemas.Claim {
	return []schemas.Claim{
		{
			ID:      "claim_9",
			Text:    "The idempotencyKey header makes refunds idempotent.",
			Anchor:  schemas.ClaimAnchor{File: "docs/refunds.md", StartLine: 3, EndLine: 3, ContentHash: "cafe01"},
			Source:  schemas.ClaimSource{File: "src/refunds/refund.ts", BlobSHA: "abc999"},
			Status:  "verified",
			History: []schemas.ClaimHistoryEvent{},
		},
		{
			ID:      "claim_12",
			Text:    "Set server.port before boot or the server refuses to start.",
			Anchor:  schemas.ClaimAnchor{File: "docs/config.md", StartLine: 7, EndLine: 8, ContentHash: "cafe02"},
			Source:  schemas.ClaimSource{File: "src/config/load.ts", BlobSHA: "abc777"},
			Status:  "verified",
			History: []schemas.ClaimHistoryEvent{},
		},
	}
}

// apiDeltaFor is the real P11-T07 OpenAPI delta of the TS fixture: the
// /payments/{id} GET operation is removed (breaking) — its verbatim delta
// location is what level 4 consumes.
func apiDeltaFor() *retrieval.ApiDelta {
	return &retrieval.ApiDelta{Deltas: []retrieval.ApiDeltaEntry{{Location: "/paths/~1payments~1{id}"}}}
}

// schemaApiDeltaFor is the component-schema delta location of the TS fixture.
func schemaApiDeltaFor() *retrieval.ApiDelta {
	return &retrieval.ApiDelta{Deltas: []retrieval.ApiDeltaEntry{{Location: "/components/schemas/Payment"}}}
}

// configDiffFor is the real P11-T08 config diff of the TS fixture
// (server.port changed).
func configDiffFor() []retrieval.ConfigKeyDiff {
	return []retrieval.ConfigKeyDiff{{Key: "server.port", Change: retrieval.ConfigKeyDiffChanged}}
}

// levelsOptions carries the optional inputs of the TS `retrieve` helper
// (retrieval-levels.test.ts).
type levelsOptions struct {
	claims        []schemas.Claim
	semanticIndex *retrieval.SemanticIndex
	symbolsDetail *schemas.SymbolsDetail
	withApiDelta  bool
	withConfigKey bool
	caps          retrieval.RetrievalCaps
	apiDelta      *retrieval.ApiDelta
}

// retrieveLevels mirrors the TS helper: no claims unless passed explicitly
// (the TS helper passed `claims: options.claims`, undefined by default).
func retrieveLevels(t *testing.T, files []schemas.ChangedFile, options levelsOptions) retrieval.RetrievalResult {
	t.Helper()
	input := retrieval.RetrieveCandidatesInput{
		ChangeAnalysis: analysisOf(files),
		Graph:          buildGraph(levelsManifest, nil),
		Manifest:       levelsManifest,
		Claims:         options.claims,
		SemanticIndex:  options.semanticIndex,
		Caps:           options.caps,
	}
	if options.withApiDelta {
		input.ApiDelta = apiDeltaFor()
	}
	if options.apiDelta != nil {
		input.ApiDelta = options.apiDelta
	}
	if options.withConfigKey {
		input.ConfigDiff = configDiffFor()
	}
	result, err := retrieval.RetrieveCandidates(input)
	require.NoError(t, err)
	return result
}

func withSymbols(file schemas.ChangedFile, detail schemas.SymbolsDetail) schemas.ChangedFile {
	file.SymbolsDetail = &detail
	return file
}

func TestRetrievalLevels34_Level3ClaimTextMentionAnchorsItsDocWithSymbolEvidence(t *testing.T) {
	result := retrieveLevels(t, []schemas.ChangedFile{withSymbols(schemas.ChangedFile{
		Path: "src/nowhere/refund.ts", Status: schemas.FileStatusModified,
	}, levelsSymbolsDetail)}, levelsOptions{claims: levelsClaims()})

	refunds := findCandidate(t, result, "docs/refunds.md")
	assert.Equal(t, retrieval.RetrievalLevel3, refunds.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel3}, refunds.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{
			Level:    retrieval.RetrievalLevel3,
			Source:   "claim:claim_9",
			Evidence: retrieval.RetrievalSymbolEvidence{Symbols: []string{"idempotencyKey"}},
		},
	}, refunds.Provenance)
	assert.False(t, refunds.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionAuto, refunds.MaxAction)
	assert.True(t, retrieval.CanDriveAutomaticAction(refunds))
	// Level 3 ran (no gap row); level 4 had no evidence input (declared gap).
	assert.Equal(t, []retrieval.RetrievalCoverageGap{
		{Level: retrieval.InactiveLevel4, Status: "inactive", Reason: retrieval.InactiveLevelReason},
	}, result.CoverageGaps)
}

func TestRetrievalLevels34_Level3IndexDocContentMentionIsRecallGrade(t *testing.T) {
	index := retrieval.BuildSemanticIndex(levelsDocContents)
	result := retrieveLevels(t, []schemas.ChangedFile{withSymbols(schemas.ChangedFile{
		Path: "src/nowhere/other.ts", Status: schemas.FileStatusModified,
	}, schemas.SymbolsDetail{
		Added:   []string{"createRefund"},
		Removed: []string{"legacyRefund"},
		Changed: []schemas.SymbolChangeDetail{{Name: "idempotencyKey", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeSignature}}},
	})}, levelsOptions{semanticIndex: index})

	refunds := findCandidate(t, result, "docs/refunds.md")
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel3}, refunds.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{
			Level:    retrieval.RetrievalLevel3,
			Source:   "symbol-mention:idempotencyKey",
			Evidence: retrieval.RetrievalSymbolEvidence{Symbols: []string{"idempotencyKey"}},
		},
	}, refunds.Provenance)
	// Recall-grade only (no claim corroboration): V-P1-8 routes to REVIEW.
	assert.True(t, refunds.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionReview, refunds.MaxAction)
	assert.False(t, retrieval.CanDriveAutomaticAction(refunds))
}

func TestRetrievalLevels34_Level3SymbolMatchingRespectsIdentifierBoundaries(t *testing.T) {
	claims := []schemas.Claim{levelsClaims()[0]}
	claims[0].ID = "claim_pool"
	claims[0].Text = "The poolSize knob supports tuning."
	claims[0].Anchor = schemas.ClaimAnchor{File: "docs/config.md", StartLine: 1, EndLine: 1, ContentHash: "cafe01"}

	result := retrieveLevels(t, []schemas.ChangedFile{withSymbols(schemas.ChangedFile{
		Path: "src/nowhere/limits.ts", Status: schemas.FileStatusModified,
	}, schemas.SymbolsDetail{
		Added:   []string{"port"},
		Removed: []string{},
		Changed: []schemas.SymbolChangeDetail{},
	})}, levelsOptions{claims: claims})

	// 'port' must not match inside 'poolSize' — level 3 elects NOTHING while
	// having run (no level-3 gap row).
	assert.Empty(t, result.Candidates)
	assert.Equal(t, []retrieval.RetrievalCoverageGap{
		{Level: retrieval.InactiveLevel4, Status: "inactive", Reason: retrieval.InactiveLevelReason},
	}, result.CoverageGaps)
}

func TestRetrievalLevels34_Level4OpenApiDeltaLocationRetrievesTheApiPathDoc(t *testing.T) {
	index := retrieval.BuildSemanticIndex(levelsDocContents)
	result := retrieveLevels(t, []schemas.ChangedFile{
		{Path: "src/nowhere/routes.ts", Status: schemas.FileStatusModified},
	}, levelsOptions{semanticIndex: index, withApiDelta: true})

	api := findCandidate(t, result, "docs/api.md")
	assert.Equal(t, retrieval.RetrievalLevel4, api.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel4}, api.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{
			Level:  retrieval.RetrievalLevel4,
			Source: "api-path-mention:/payments/{id}",
			Evidence: retrieval.RetrievalApiSchemaEvidence{
				Locations: []string{"/paths/~1payments~1{id}"},
				Keys:      []string{},
			},
		},
	}, api.Provenance)
	// Recall-grade only (index mention, no claim corroboration).
	assert.True(t, api.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionReview, api.MaxAction)
	// Level 4 ran; level 3 had no evidence input.
	assert.Equal(t, []retrieval.RetrievalCoverageGap{
		{Level: retrieval.InactiveLevel3, Status: "inactive", Reason: retrieval.InactiveLevelReason},
	}, result.CoverageGaps)
}

func TestRetrievalLevels34_Level4ComponentSchemaDeltaLocationRetrievesTheSchemaDoc(t *testing.T) {
	index := retrieval.BuildSemanticIndex(levelsDocContents)
	result := retrieveLevels(t, []schemas.ChangedFile{
		{Path: "src/nowhere/routes.ts", Status: schemas.FileStatusModified},
	}, levelsOptions{semanticIndex: index, apiDelta: schemaApiDeltaFor()})

	api := findCandidate(t, result, "docs/api.md")
	assert.Equal(t, retrieval.RetrievalLevel4, api.Level)
	var schemaEntry *retrieval.RetrievalProvenance
	for i := range api.Provenance {
		entry := api.Provenance[i]
		if len(entry.Source) >= len("api-path-mention:Payment") && entry.Source[:len("api-path-mention:Payment")] == "api-path-mention:Payment" {
			schemaEntry = &api.Provenance[i]
			break
		}
	}
	require.NotNil(t, schemaEntry)
	assert.Equal(t, retrieval.RetrievalProvenance{
		Level:  retrieval.RetrievalLevel4,
		Source: "api-path-mention:Payment",
		Evidence: retrieval.RetrievalApiSchemaEvidence{
			Locations: []string{"/components/schemas/Payment"},
			Keys:      []string{},
		},
	}, *schemaEntry)
}

func TestRetrievalLevels34_Level4ConfigDiffKeyMatchedByClaimAndDocContent(t *testing.T) {
	index := retrieval.BuildSemanticIndex(levelsDocContents)
	result := retrieveLevels(t, []schemas.ChangedFile{
		{Path: "src/nowhere/load.ts", Status: schemas.FileStatusModified},
	}, levelsOptions{claims: levelsClaims(), semanticIndex: index, withConfigKey: true})

	config := findCandidate(t, result, "docs/config.md")
	assert.Equal(t, retrieval.RetrievalLevel4, config.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel4}, config.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{
			Level:    retrieval.RetrievalLevel4,
			Source:   "claim:claim_12",
			Evidence: retrieval.RetrievalApiSchemaEvidence{Locations: []string{}, Keys: []string{"server.port"}},
		},
		{
			Level:    retrieval.RetrievalLevel4,
			Source:   "config-key-mention:server.port",
			Evidence: retrieval.RetrievalApiSchemaEvidence{Locations: []string{}, Keys: []string{"server.port"}},
		},
	}, config.Provenance)
	// Claim-anchored hit = deterministic corroboration → auto (V-P1-8 holds).
	assert.False(t, config.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionAuto, config.MaxAction)
	assert.Equal(t, []retrieval.RetrievalCoverageGap{
		{Level: retrieval.InactiveLevel3, Status: "inactive", Reason: retrieval.InactiveLevelReason},
	}, result.CoverageGaps)
}

func TestRetrievalLevels34_AbsentEvidenceKeepsBothDeclaredGapRows(t *testing.T) {
	result := retrieveLevels(t, []schemas.ChangedFile{
		{Path: "src/nowhere/step.ts", Status: schemas.FileStatusModified},
	}, levelsOptions{claims: []schemas.Claim{}})
	assert.Empty(t, result.Candidates)
	assert.Equal(t, []retrieval.RetrievalCoverageGap{
		{Level: retrieval.InactiveLevel3, Status: "inactive", Reason: retrieval.InactiveLevelReason},
		{Level: retrieval.InactiveLevel4, Status: "inactive", Reason: retrieval.InactiveLevelReason},
	}, result.CoverageGaps)
}

func TestRetrievalLevels34_EvidencePresentButUnmatchedReportsNoGapRow(t *testing.T) {
	result := retrieveLevels(t, []schemas.ChangedFile{withSymbols(schemas.ChangedFile{
		Path: "src/nowhere/step.ts", Status: schemas.FileStatusModified,
	}, schemas.SymbolsDetail{
		Added:   []string{"unmentionedThing"},
		Removed: []string{},
		Changed: []schemas.SymbolChangeDetail{},
	})}, levelsOptions{claims: []schemas.Claim{}, withApiDelta: true, withConfigKey: true})
	assert.Empty(t, result.Candidates)
	assert.Empty(t, result.CoverageGaps) // both levels ran; neither is silent
}

func TestRetrievalLevels34_FormattingOnlyFilesFeedNoSymbolEvidence(t *testing.T) {
	file := withSymbols(schemas.ChangedFile{
		Path: "src/nowhere/refund.ts", Status: schemas.FileStatusModified, FormattingOnly: boolp(true),
	}, levelsSymbolsDetail)
	result := retrieveLevels(t, []schemas.ChangedFile{file}, levelsOptions{claims: levelsClaims()})
	assert.Empty(t, result.Candidates)
	assert.Equal(t, []retrieval.RetrievalCoverageGap{
		{Level: retrieval.InactiveLevel3, Status: "inactive", Reason: retrieval.InactiveLevelReason},
		{Level: retrieval.InactiveLevel4, Status: "inactive", Reason: retrieval.InactiveLevelReason},
	}, result.CoverageGaps)
}

func TestRetrievalLevels34_CompositionLevel2AndLevel3BothListed(t *testing.T) {
	result := retrieveLevels(t, []schemas.ChangedFile{withSymbols(schemas.ChangedFile{
		Path: "src/refunds/refund.ts", Status: schemas.FileStatusModified,
	}, levelsSymbolsDetail)}, levelsOptions{claims: levelsClaims()})

	refunds := findCandidate(t, result, "docs/refunds.md")
	assert.Equal(t, retrieval.RetrievalLevel2, refunds.Level)
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel2, retrieval.RetrievalLevel3}, refunds.Levels)
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel2, Source: "manifest-source:src/refunds/**"},
		{
			Level:    retrieval.RetrievalLevel3,
			Source:   "claim:claim_9",
			Evidence: retrieval.RetrievalSymbolEvidence{Symbols: []string{"idempotencyKey"}},
		},
	}, refunds.Provenance)
	assert.False(t, refunds.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionAuto, refunds.MaxAction)
}

func TestRetrievalLevels34_CompositionLevel3OnlyDocNeverSeedsLevel5(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/refunds.md", Sources: []string{"src/refunds/**"}},
			{Path: "docs/sibling.md", Sources: []string{"src/other/**"}},
		},
	}
	concepts := &schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs:   []schemas.ConceptsDocEntry{{Path: "docs/refunds.md"}, {Path: "docs/sibling.md"}},
		Concepts: []schemas.ConceptEntry{
			{Name: "refunds", Docs: []string{"docs/refunds.md", "docs/sibling.md"}},
		},
	}
	claims := levelsClaims()
	claims[0].Anchor = schemas.ClaimAnchor{File: "docs/refunds.md", StartLine: 3, EndLine: 3, ContentHash: "cafe01"}

	// No level-1/2 seed (the changed path matches no manifest source), so the
	// level-3 hit on docs/refunds.md must NOT expand the 'refunds' concept to
	// docs/sibling.md at level 5.
	result, err := retrieval.RetrieveCandidates(retrieval.RetrieveCandidatesInput{
		ChangeAnalysis: analysisOf([]schemas.ChangedFile{withSymbols(schemas.ChangedFile{
			Path: "src/nowhere/refund.ts", Status: schemas.FileStatusModified,
		}, levelsSymbolsDetail)}),
		Graph:    buildGraph(manifest, concepts),
		Manifest: manifest,
		Claims:   claims,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"docs/refunds.md"}, docPaths(result.Candidates))
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel3}, result.Candidates[0].Levels)
}

func TestRetrievalLevels34_Levels126OutputsUnchangedWhen34EvidenceArrives(t *testing.T) {
	index := retrieval.BuildSemanticIndex(levelsDocContents)
	withEvidenceFile := withSymbols(schemas.ChangedFile{
		Path: "src/refunds/refund.ts", Status: schemas.FileStatusModified,
	}, levelsSymbolsDetail)

	withoutEvidence := retrieveLevels(t, []schemas.ChangedFile{
		{Path: "src/refunds/refund.ts", Status: schemas.FileStatusModified},
	}, levelsOptions{claims: levelsClaims(), semanticIndex: index})
	withEvidence := retrieveLevels(t, []schemas.ChangedFile{withEvidenceFile},
		levelsOptions{claims: levelsClaims(), semanticIndex: index, withConfigKey: true})

	before := findCandidate(t, withoutEvidence, "docs/refunds.md")
	after := findCandidate(t, withEvidence, "docs/refunds.md")
	// The pre-existing level-2 and level-6 rows are byte-identical.
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel2, Source: "manifest-source:src/refunds/**"},
		{Level: retrieval.RetrievalLevel6, Source: "semantic-index", Score: intp(10)},
	}, before.Provenance)

	level26 := []retrieval.RetrievalProvenance{}
	for _, entry := range after.Provenance {
		if entry.Level == retrieval.RetrievalLevel2 || entry.Level == retrieval.RetrievalLevel6 {
			level26 = append(level26, entry)
		}
	}
	assert.Equal(t, before.Provenance, level26)
	// Level 3/4 rows are appended: claim-mention AND index-mention level 3 (the
	// doc content mentions the symbol too); the config-key claim hit lands on
	// docs/config.md, not here.
	assert.Equal(t, []retrieval.RetrievalProvenance{
		{Level: retrieval.RetrievalLevel2, Source: "manifest-source:src/refunds/**"},
		{
			Level:    retrieval.RetrievalLevel3,
			Source:   "claim:claim_9",
			Evidence: retrieval.RetrievalSymbolEvidence{Symbols: []string{"idempotencyKey"}},
		},
		{
			Level:    retrieval.RetrievalLevel3,
			Source:   "symbol-mention:idempotencyKey",
			Evidence: retrieval.RetrievalSymbolEvidence{Symbols: []string{"idempotencyKey"}},
		},
		{Level: retrieval.RetrievalLevel6, Source: "semantic-index", Score: intp(10)},
	}, after.Provenance)
	assert.Equal(t, []retrieval.RetrievalLevel{
		retrieval.RetrievalLevel2, retrieval.RetrievalLevel3, retrieval.RetrievalLevel6,
	}, after.Levels)
}

func TestRetrievalLevels34_RecallOnlyMixingStaysReviewGated(t *testing.T) {
	index := retrieval.BuildSemanticIndex(levelsDocContents)
	result := retrieveLevels(t, []schemas.ChangedFile{withSymbols(schemas.ChangedFile{
		Path: "src/inner/refunds/refund.ts", Status: schemas.FileStatusModified,
	}, levelsSymbolsDetail)}, levelsOptions{claims: []schemas.Claim{}, semanticIndex: index})

	refunds := findCandidate(t, result, "docs/refunds.md")
	assert.Equal(t, []retrieval.RetrievalLevel{retrieval.RetrievalLevel3, retrieval.RetrievalLevel6}, refunds.Levels)
	assert.True(t, refunds.SemanticOnly)
	assert.Equal(t, retrieval.MaxActionReview, refunds.MaxAction)
	assert.False(t, retrieval.CanDriveAutomaticAction(refunds))
}

func TestRetrievalLevels34_DeterministicIdenticalInputsDeepEqualResults(t *testing.T) {
	index := retrieval.BuildSemanticIndex(levelsDocContents)
	files := []schemas.ChangedFile{withSymbols(schemas.ChangedFile{
		Path: "src/nowhere/refund.ts", Status: schemas.FileStatusModified,
	}, levelsSymbolsDetail)}
	options := levelsOptions{claims: levelsClaims(), semanticIndex: index, withApiDelta: true, withConfigKey: true}
	first := retrieveLevels(t, files, options)
	second := retrieveLevels(t, files, options)
	assert.Equal(t, first, second)
}

func TestRetrievalLevels34_PerLevelCapsTruncateLevel4Deterministically(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/api.md", Sources: []string{"src/api2/**"}},
			{Path: "docs/payments-guide.md", Sources: []string{"src/guide/**"}},
		},
	}
	apiIndex := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
		{Path: "docs/api.md", Content: "covers /payments/{id}"},
		{Path: "docs/payments-guide.md", Content: "also covers payments/{id} flows"},
	})
	result, err := retrieval.RetrieveCandidates(retrieval.RetrieveCandidatesInput{
		ChangeAnalysis: analysisOf([]schemas.ChangedFile{{Path: "src/nowhere/routes.ts", Status: schemas.FileStatusModified}}),
		Graph:          buildGraph(manifest, nil),
		Manifest:       manifest,
		SemanticIndex:  apiIndex,
		ApiDelta:       apiDeltaFor(),
		Caps:           retrieval.RetrievalCaps{retrieval.RetrievalLevel4: 1},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"docs/api.md"}, docPaths(result.Candidates))
	assert.Equal(t, []retrieval.RetrievalOverflow{{Level: retrieval.RetrievalLevel4, Dropped: 1}}, result.Overflow)
}
