package retrieval

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetrCov_configErrorSurface(t *testing.T) {
	err := NewRetrievalConfigError("test message")
	assert.Contains(t, err.Error(), "test message")
	assert.Equal(t, CodeRetrievalConfig, err.Code())
	inner := err.Unwrap()
	require.NotNil(t, inner)
}

func TestRetrCov_accumulator_mark(t *testing.T) {
	acc := newAccumulator()
	acc.mark(RetrievalProvenance{Level: RetrievalLevel1, Source: "src/s1.ts"})
	acc.mark(RetrievalProvenance{Level: RetrievalLevel1, Source: "src/s1.ts"})
	acc.mark(RetrievalProvenance{Level: RetrievalLevel2, Source: "src/s2.ts"})
	entries := acc.entries()
	assert.Len(t, entries, 2)
	levels := acc.sortedLevels()
	assert.Contains(t, levels, RetrievalLevel1)
	assert.Contains(t, levels, RetrievalLevel2)
}

func TestRetrCov_mentions(t *testing.T) {
	assert.True(t, mentions("the server port setting", "port", false))
	assert.False(t, mentions("poolSize setting", "port", false))
	assert.False(t, mentions("", "", false))
	assert.True(t, mentions("Payments PORT", "port", true))
}

func TestRetrCov_apiExpressionsFor(t *testing.T) {
	r := apiExpressionsFor("/paths/~1payments~1{id}/get/200")
	assert.Equal(t, []string{"/payments/{id}"}, r)
	r = apiExpressionsFor("/components/schemas/Payment/type")
	assert.Equal(t, []string{"Payment"}, r)
	r = apiExpressionsFor("/other/location")
	assert.Nil(t, r)
}

func TestRetrCov_mappingFor(t *testing.T) {
	g := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest: schemas.Manifest{
			Version: 1,
			Documents: []schemas.ManifestEntry{{Path: "docs/a.md", Sources: []string{"src/a/**"}}},
		},
	})
	_, ok := mappingFor(g, "nonexistent")
	assert.False(t, ok)
	m, ok := mappingFor(g, "docs/a.md")
	assert.True(t, ok)
	assert.Equal(t, "docs/a.md", m.Doc)
}

func TestRetrCov_scoreOf(t *testing.T) {
	s := scoreOf(RetrievalCandidate{Level: RetrievalLevel1})
	assert.Equal(t, -1, s)
	score := 20
	s = scoreOf(RetrievalCandidate{Level: RetrievalLevel6, Provenance: []RetrievalProvenance{
		{Level: RetrievalLevel6, Score: &score},
	}})
	assert.Equal(t, 20, s)
}

func TestRetrCov_parseRetrievalCaps(t *testing.T) {
	caps, err := ParseRetrievalCaps(map[RetrievalLevel]float64{RetrievalLevel1: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, caps[RetrievalLevel1])

	_, err = ParseRetrievalCaps(map[RetrievalLevel]float64{RetrievalLevel1: -1})
	require.Error(t, err)
	_, err = ParseRetrievalCaps(map[RetrievalLevel]float64{RetrievalLevel1: 1.5})
	require.Error(t, err)
}

func TestRetrCov_evidenceTypes(t *testing.T) {
	var symbolEv RetrievalLevelEvidence = RetrievalSymbolEvidence{Symbols: []string{"x"}}
	assert.NotNil(t, symbolEv)
	var apiEv RetrievalLevelEvidence = RetrievalApiSchemaEvidence{Locations: []string{"/l"}, Keys: []string{"k"}}
	assert.NotNil(t, apiEv)
}

func TestRetrCov_validateCaps(t *testing.T) {
	err := validateCaps(RetrievalCaps{RetrievalLevel1: -1})
	require.Error(t, err)
	err = validateCaps(RetrievalCaps{})
	require.NoError(t, err)
	err = validateCaps(nil)
	require.NoError(t, err)
}

func TestRetrCov_removeFromPostings(t *testing.T) {
	index := BuildSemanticIndex([]SemanticDoc{
		{Path: "docs/a.md", Content: "term one"},
		{Path: "docs/b.md", Content: "term two"},
	})
	require.NotNil(t, index)
	index.removeFromPostings("docs/a.md", []string{"term", "one"})
	assert.NotContains(t, index.Postings, "one", "no other doc holds 'one'")
	assert.Contains(t, index.Postings, "term", "docs/b.md still holds 'term'")
}

func TestRetrCov_shardContainsAllTokens(t *testing.T) {
	shard := &SemanticShard{Path: "p", Counts: map[string]int{"a": 1, "b": 2}}
	assert.True(t, shardContainsAllTokens(shard, []string{"a", "b"}))
	assert.False(t, shardContainsAllTokens(shard, []string{"a", "c"}))
}

func TestRetrCov_unescapePointerToken(t *testing.T) {
	assert.Equal(t, "payments/{id}", unescapePointerToken("payments~1{id}"))
	assert.Equal(t, "a~b", unescapePointerToken("a~0b"))
}

func TestRetrCov_runeEqual(t *testing.T) {
	assert.True(t, runeEqual('a', 'a', false))
	assert.False(t, runeEqual('A', 'a', false))
	assert.True(t, runeEqual('A', 'a', true))
}

func TestRetrCov_isIdentifierRune(t *testing.T) {
	assert.True(t, isIdentifierRune('a'))
	assert.True(t, isIdentifierRune('_'))
	assert.True(t, isIdentifierRune('$'))
	assert.False(t, isIdentifierRune('-'))
}

func TestRetrCov_seedHoldersOnlySelf(t *testing.T) {
	assert.True(t, seedHoldersOnlySelf([]string{"d1"}, "d1"))
	assert.False(t, seedHoldersOnlySelf([]string{"d1", "d2"}, "d1"))
}
