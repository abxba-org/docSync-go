package contracts_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ----------------------------------------------------------- test fixtures */

func boolPtr(b bool) *bool { return &b }

// composeGraph mirrors the clean path of graph/buildDocumentGraph for the
// fixtures used in these ports (one mapping per manifest document, concepts.yml
// composition, validated contractKinds, no errors). Contracts is an independent
// unit — it does not build on internal/graph — so the fixtures compose the
// graph shape directly.
func composeGraph(manifest schemas.Manifest, concepts schemas.ConceptsConfig) *contracts.Graph {
	contractsByDoc := map[string][]string{}
	for _, d := range concepts.Docs {
		if _, exists := contractsByDoc[d.Path]; !exists {
			contractsByDoc[d.Path] = d.Contracts
		}
	}
	conceptDocs := map[string][]string{}
	var conceptNames []string
	for _, c := range concepts.Concepts {
		if _, exists := conceptDocs[c.Name]; !exists {
			conceptNames = append(conceptNames, c.Name)
		}
		conceptDocs[c.Name] = c.Docs
	}
	mappings := make([]contracts.MappingRecord, 0, len(manifest.Documents))
	for _, entry := range manifest.Documents {
		var docConcepts []string
		for _, name := range conceptNames {
			for _, d := range conceptDocs[name] {
				if d == entry.Path {
					docConcepts = append(docConcepts, name)
					break
				}
			}
		}
		mappings = append(mappings, contracts.MappingRecord{
			Doc:       entry.Path,
			Sources:   entry.Sources,
			Concepts:  docConcepts,
			Contracts: contractsByDoc[entry.Path],
		})
	}
	var kinds map[string]schemas.ContractKind
	if len(concepts.ContractKinds) > 0 {
		kinds = map[string]schemas.ContractKind{}
		for k, v := range concepts.ContractKinds {
			kinds[k] = v
		}
	}
	return &contracts.Graph{Mappings: mappings, ContractKinds: kinds}
}

func analysis(files []schemas.ChangedFile) *schemas.ChangeAnalysis {
	return &schemas.ChangeAnalysis{Files: files}
}

func mustClassify(t *testing.T, in contracts.ClassifyContractsInput) contracts.ContractVector {
	t.Helper()
	v, err := contracts.ClassifyContracts(in)
	require.NoError(t, err)
	return v
}

func findEntry(t *testing.T, vector contracts.ContractVector, name string) *contracts.ContractVectorEntry {
	t.Helper()
	for i := range vector.Vectors {
		if vector.Vectors[i].Contract == name {
			return &vector.Vectors[i]
		}
	}
	t.Fatalf("no contract entry %q in vector for %q", name, vector.Doc)
	return nil
}

func mappingByDoc(g *contracts.Graph, doc string) *contracts.MappingRecord {
	for i := range g.Mappings {
		if g.Mappings[i].Doc == doc {
			return &g.Mappings[i]
		}
	}
	return nil
}

// The V-P1-12 fixture graph (from contracts.test.ts).
func vp112Graph() *contracts.Graph {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/api.md", Sources: []string{"src/api/**"}},
			{Path: "docs/payments.md", Sources: []string{"src/payments/**"}},
			{Path: "docs/free.md", Sources: []string{"src/free/**"}},
		},
	}
	concepts := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/api.md", Contracts: []string{"request_schema", "authentication", "mystery_contract"}},
			{Path: "docs/payments.md", Contracts: []string{"user_guide_tone"}},
		},
		ContractKinds: map[string]schemas.ContractKind{
			"request_schema": schemas.ContractKindAPISchema,
			"authentication": schemas.ContractKindBehavior,
		},
	}
	return composeGraph(manifest, concepts)
}

/* ------------------ V-P1-12: contract vector scopes + sub-threshold abstain */

func TestV_P1_12_APIDiffHitsAPISchemaAndBehaviorNoHit(t *testing.T) {
	graph := vp112Graph()
	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph,
		Doc:   "docs/api.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{
			{Path: "src/api/schema.ts", Status: schemas.FileStatusModified, APIDiff: boolPtr(true)},
		}),
	})
	require.Equal(t, "docs/api.md", vector.Doc)
	require.Len(t, vector.Vectors, 3)

	require.Equal(t, contracts.ContractVectorEntry{
		Contract:   "request_schema",
		Kind:       schemas.ContractKindAPISchema,
		Hit:        true,
		Confidence: 0.98,
		Evidence:   []string{"src/api/schema.ts (api_diff)"},
		Abstain:    false,
	}, *findEntry(t, vector, "request_schema"))

	require.Equal(t, contracts.ContractVectorEntry{
		Contract:   "authentication",
		Kind:       schemas.ContractKindBehavior,
		Hit:        false,
		Confidence: 0,
		Evidence:   []string{},
		Abstain:    true,
	}, *findEntry(t, vector, "authentication"))
}

func TestV_P1_12_UndeclaredContractDefaultsContent(t *testing.T) {
	graph := vp112Graph()

	formatting := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{
			{Path: "src/api/schema.ts", Status: schemas.FileStatusModified, FormattingOnly: boolPtr(true)},
		}),
	})
	mysteryFormatting := findEntry(t, formatting, "mystery_contract")
	require.Equal(t, schemas.ContractKindContent, mysteryFormatting.Kind)
	require.False(t, mysteryFormatting.Hit)
	require.Equal(t, 0.0, mysteryFormatting.Confidence)
	require.True(t, mysteryFormatting.Abstain)

	real := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{
			{Path: "src/api/schema.ts", Status: schemas.FileStatusModified},
		}),
	})
	mysteryReal := findEntry(t, real, "mystery_contract")
	require.True(t, mysteryReal.Hit)
	require.Equal(t, contracts.BootstrapContractKindConfidence[schemas.ContractKindContent], mysteryReal.Confidence)
	require.Equal(t, []string{"src/api/schema.ts (non-formatting)"}, mysteryReal.Evidence)
}

func TestV_P1_12_BehaviorSignalsAndThresholdAbstain(t *testing.T) {
	graph := vp112Graph()
	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{
			{Path: "src/api/flow.ts", Status: schemas.FileStatusModified, SymbolsChanged: boolPtr(true)},
			{Path: "src/api/flow.test.ts", Status: schemas.FileStatusAdded, TestOnly: boolPtr(true)},
		}),
	})
	authentication := findEntry(t, vector, "authentication")
	require.True(t, authentication.Hit)
	require.Equal(t, 0.85, authentication.Confidence)
	require.Equal(t, []string{"src/api/flow.ts (symbols_changed)", "src/api/flow.test.ts (test_only)"}, authentication.Evidence)
	// Default threshold: behavior (0.85) is exactly at it → not abstaining.
	require.False(t, authentication.Abstain)

	strict := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md", Threshold: 0.9,
		ChangeAnalysis: analysis([]schemas.ChangedFile{
			{Path: "src/api/flow.ts", Status: schemas.FileStatusModified, SymbolsChanged: boolPtr(true)},
		}),
	})
	authenticationStrict := findEntry(t, strict, "authentication")
	require.True(t, authenticationStrict.Hit)
	require.Equal(t, 0.85, authenticationStrict.Confidence)
	require.True(t, authenticationStrict.Abstain)
}

func TestV_P1_12_ConfigSignalsAndContentKind(t *testing.T) {
	graph := vp112Graph()
	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/payments.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{
			{Path: "src/payments/config.ts", Status: schemas.FileStatusModified, ConfigDiff: boolPtr(true)},
		}),
	})
	tone := findEntry(t, vector, "user_guide_tone")
	require.Equal(t, schemas.ContractKindContent, tone.Kind)
	require.True(t, tone.Hit)
	require.Equal(t, 0.8, tone.Confidence)
	require.Equal(t, []string{"src/payments/config.ts (non-formatting)"}, tone.Evidence)
	require.True(t, tone.Abstain)

	formattingVector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/payments.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{
			{Path: "src/payments/config.ts", Status: schemas.FileStatusModified, FormattingOnly: boolPtr(true)},
		}),
	})
	toneFmt := findEntry(t, formattingVector, "user_guide_tone")
	require.False(t, toneFmt.Hit)
	require.Equal(t, 0.0, toneFmt.Confidence)
	require.Empty(t, toneFmt.Evidence)
	require.True(t, toneFmt.Abstain)

	// Re-declare user_guide_tone as a config contract → config signal hits it.
	configGraph := composeGraph(
		schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{
			{Path: "docs/api.md", Sources: []string{"src/api/**"}},
			{Path: "docs/payments.md", Sources: []string{"src/payments/**"}},
			{Path: "docs/free.md", Sources: []string{"src/free/**"}},
		}},
		schemas.ConceptsConfig{
			Schema: "docsync.concepts/v1",
			Docs: []schemas.ConceptsDocEntry{
				{Path: "docs/api.md", Contracts: []string{"request_schema", "authentication", "mystery_contract"}},
				{Path: "docs/payments.md", Contracts: []string{"user_guide_tone"}},
			},
			ContractKinds: map[string]schemas.ContractKind{
				"request_schema":  schemas.ContractKindAPISchema,
				"authentication":  schemas.ContractKindBehavior,
				"user_guide_tone": schemas.ContractKindConfig,
			},
		},
	)
	configKind := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: configGraph, Doc: "docs/payments.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{
			{Path: "src/payments/config.ts", Status: schemas.FileStatusModified, ConfigDiff: boolPtr(true)},
		}),
	})
	first := configKind.Vectors[0]
	require.Equal(t, schemas.ContractKindConfig, first.Kind)
	require.Equal(t, 0.9, first.Confidence)
	require.Equal(t, []string{"src/payments/config.ts (config_diff)"}, first.Evidence)
	require.False(t, first.Abstain)
}

func TestV_P1_12_UnmappedOrContractLessYieldsEmptyAndPolicyPinned(t *testing.T) {
	graph := vp112Graph()

	free := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/free.md", ChangeAnalysis: analysis(nil),
	})
	require.Empty(t, free.Vectors)

	unmapped := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/unmapped.md", ChangeAnalysis: analysis(nil),
	})
	require.Empty(t, unmapped.Vectors)

	require.Equal(t, 0.85, contracts.DefaultContractReviewThreshold)
	require.Equal(t, map[schemas.ContractKind]float64{
		schemas.ContractKindAPISchema: 0.98,
		schemas.ContractKindConfig:    0.9,
		schemas.ContractKindBehavior:  0.85,
		schemas.ContractKindContent:   0.8,
	}, contracts.BootstrapContractKindConfidence)
}

/* -------------------------- zero-claims coverage defect flagged (P1-T12) */

func findCoverage(entries []contracts.ContractCoverageEntry, doc string) *contracts.ContractCoverageEntry {
	for i := range entries {
		if entries[i].Doc == doc {
			return &entries[i]
		}
	}
	return nil
}

func TestCoverage_DeclaredContractsNoClaimsFlagged(t *testing.T) {
	graph := vp112Graph()
	coverage := contracts.ContractCoverage(graph, nil)

	docs := make([]string, 0, len(coverage))
	for _, e := range coverage {
		docs = append(docs, e.Doc)
		require.True(t, e.ZeroClaims)
	}
	require.Equal(t, []string{"docs/api.md", "docs/payments.md"}, docs)
	require.Nil(t, findCoverage(coverage, "docs/free.md"))
}

func TestCoverage_LiveClaimClearsFlagClosedClaimDoesNot(t *testing.T) {
	graph := vp112Graph()

	live := []schemas.Claim{{
		ID:   "claim_a",
		Text: "Refunds require an idempotency key.",
		Anchor: schemas.ClaimAnchor{
			File: "docs/api.md", StartLine: 1, EndLine: 1,
			ContentHash: docutil.ContentHash("anything"),
		},
		Source: schemas.ClaimSource{File: "src/api/schema.ts", BlobSHA: "abc1234"},
		Status: schemas.ClaimStatusVerified,
	}}
	coverage := contracts.ContractCoverage(graph, live)
	require.Equal(t, contracts.ContractCoverageEntry{
		Doc:        "docs/api.md",
		Contracts:  []string{"request_schema", "authentication", "mystery_contract"},
		ZeroClaims: false,
	}, *findCoverage(coverage, "docs/api.md"))
	require.True(t, findCoverage(coverage, "docs/payments.md").ZeroClaims)

	closed := []schemas.Claim{{
		ID:   "claim_retired",
		Text: "Old claim.",
		Anchor: schemas.ClaimAnchor{
			File: "docs/payments.md", StartLine: 1, EndLine: 1,
			ContentHash: docutil.ContentHash("anything"),
		},
		Source: schemas.ClaimSource{File: "src/payments/config.ts", BlobSHA: "abc1234"},
		Status: schemas.ClaimStatusClosed,
	}}
	coverageClosed := contracts.ContractCoverage(graph, closed)
	require.True(t, findCoverage(coverageClosed, "docs/payments.md").ZeroClaims)
}

/* --------- P11-T10: contract classification consumes the new diff kinds ---- */

func p11t10Graph() *contracts.Graph {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/api.md", Sources: []string{"src/api/**"}},
			{Path: "docs/config.md", Sources: []string{"src/config/**"}},
		},
	}
	concepts := schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{Path: "docs/api.md", Contracts: []string{"request_schema", "auth_tone"}},
		},
		ContractKinds: map[string]schemas.ContractKind{
			"request_schema": schemas.ContractKindAPISchema,
			"auth_tone":      schemas.ContractKindContent,
		},
	}
	return composeGraph(manifest, concepts)
}

func TestP11_T10_SymbolTypeChangeRousesWithSymbolEvidence(t *testing.T) {
	graph := p11t10Graph()
	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{{
			Path: "src/api/refund.ts", Status: schemas.FileStatusModified,
			SymbolsChanged: boolPtr(true),
			SymbolsDetail: &schemas.SymbolsDetail{
				Added: []string{}, Removed: []string{},
				Changed: []schemas.SymbolChangeDetail{
					{Name: "RefundRequest", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeType}},
				},
			},
		}}),
	})
	row := findEntry(t, vector, "request_schema")
	require.True(t, row.Hit)
	require.False(t, row.Abstain)
	require.Equal(t, []string{"src/api/refund.ts#RefundRequest (type change)"}, row.SymbolTypeChanges)
}

func TestP11_T10_NonTypeSymbolChangeDoesNotRouse(t *testing.T) {
	graph := p11t10Graph()
	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md",
		ChangeAnalysis: analysis([]schemas.ChangedFile{{
			Path: "src/api/refund.ts", Status: schemas.FileStatusModified,
			SymbolsChanged: boolPtr(true),
			SymbolsDetail: &schemas.SymbolsDetail{
				Added: []string{}, Removed: []string{},
				Changed: []schemas.SymbolChangeDetail{
					{Name: "RefundRequest", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeSignature}},
				},
			},
		}}),
	})
	row := findEntry(t, vector, "request_schema")
	require.False(t, row.Hit)
	require.Nil(t, row.SymbolTypeChanges)
	require.True(t, row.Abstain)
}

func TestP11_T10_BreakingOpenApiDeltaRousesApiEdgeDoc(t *testing.T) {
	graph := p11t10Graph()
	require.NotNil(t, mappingByDoc(graph, "docs/api.md"))
	mappingByDoc(graph, "docs/api.md").Apis = []string{"payments-api"}

	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md", ChangeAnalysis: analysis(nil),
		ApiDelta: &contracts.ApiDelta{
			Deltas: []contracts.ApiDeltaEntry{{Location: "/components/schemas/Refund/properties/amount"}},
			Summary: contracts.ApiDeltaSummary{
				Breaking: 1, BehaviorAnnotating: 0, NonBreaking: 0, WorstTier: contracts.OpenApiTierBreaking,
			},
		},
	})
	row := findEntry(t, vector, "request_schema")
	require.True(t, row.Hit)
	require.Equal(t, &contracts.ContractApiDeltaEvidence{
		WorstTier:          contracts.OpenApiTierBreaking,
		Breaking:           1,
		BehaviorAnnotating: 0,
		NonBreaking:        0,
		Locations:          []string{"/components/schemas/Refund/properties/amount"},
	}, row.ApiDeltaEvidence)
}

func TestP11_T10_NonBreakingOpenApiDeltaNeverRouses(t *testing.T) {
	graph := p11t10Graph()
	mappingByDoc(graph, "docs/api.md").Apis = []string{"payments-api"}

	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md", ChangeAnalysis: analysis(nil),
		ApiDelta: &contracts.ApiDelta{
			Deltas: []contracts.ApiDeltaEntry{{Location: "/paths/~1payments/get"}},
			Summary: contracts.ApiDeltaSummary{
				Breaking: 0, BehaviorAnnotating: 0, NonBreaking: 1, WorstTier: contracts.OpenApiTierNonBreaking,
			},
		},
	})
	row := findEntry(t, vector, "request_schema")
	require.False(t, row.Hit)
	require.Nil(t, row.ApiDeltaEvidence)
}

func TestP11_T10_ConfigKeyChangeRousesConfigEdgeDoc(t *testing.T) {
	graph := p11t10Graph()
	mappingByDoc(graph, "docs/api.md").Configs = []string{"server"}

	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md", ChangeAnalysis: analysis(nil),
		ConfigDiff: []contracts.ConfigKeyDiff{{Key: "server.port", Change: contracts.ConfigKeyChanged}},
	})
	row := findEntry(t, vector, "request_schema")
	require.True(t, row.Hit)
	require.Equal(t, []string{"server.port (changed)"}, row.ConfigKeyEvidence)
}

func TestP11_T10_WithoutNewDiffEvidenceBehavesAsPhase1(t *testing.T) {
	graph := p11t10Graph()
	vector := mustClassify(t, contracts.ClassifyContractsInput{
		Graph: graph, Doc: "docs/api.md", ChangeAnalysis: analysis(nil),
	})
	require.Equal(t, contracts.ContractVectorEntry{
		Contract:   "request_schema",
		Kind:       schemas.ContractKindAPISchema,
		Hit:        false,
		Confidence: 0,
		Evidence:   []string{},
		Abstain:    true,
	}, *findEntry(t, vector, "request_schema"))
}
