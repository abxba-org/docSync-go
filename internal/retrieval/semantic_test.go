package retrieval_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/retrieval"
)

// Semantic index tests (P1-T14; ARCH §4.5 level 6, OD-R37): deterministic
// tokenization + inverted index, documented integer scoring with path-ascending
// tie-break, lazy per-shard rebuild (refreshShard), staleness tolerance
// (querying a stale shard still returns hits, flagged `stale: true`), and the
// no-model-invocation contract (pure in-memory lexical data only).
//
// Ported from packages/core/test/retrieval-semantic.test.ts (black-box, testify).

func TestSemanticTokens(t *testing.T) {
	t.Run("lowercases, splits on non-alphanumeric runs, dedupes and sorts", func(t *testing.T) {
		assert.Equal(t, []string{"api", "payments", "refund", "src", "ts"},
			retrieval.SemanticTokens("src/payments/refund-API.ts!"))
	})

	t.Run("drops stopwords and tokens shorter than 2 characters", func(t *testing.T) {
		_, hasThe := retrieval.SemanticStopwords["the"]
		assert.True(t, hasThe)
		assert.Equal(t, []string{"refund"}, retrieval.SemanticTokens("The A and of X refund"))
	})

	t.Run("deterministic: identical text → identical token list", func(t *testing.T) {
		assert.Equal(t,
			retrieval.SemanticTokens("Idempotency-Key: refund"),
			retrieval.SemanticTokens("refund idempotency key"))
	})
}

func TestBuildSemanticIndex(t *testing.T) {
	t.Run("builds per-doc shards with content-hash and inverted postings", func(t *testing.T) {
		index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
			{Path: "docs/a.md", Content: "alpha beta"},
			{Path: "docs/b.md", Content: "beta gamma"},
		})
		shardA := index.Shards["docs/a.md"]
		require.NotNil(t, shardA)
		assert.Equal(t, docutil.ContentHash("alpha beta"), shardA.ContentHash)
		assert.Equal(t, []string{"alpha", "beta"}, shardA.Tokens)
		assert.Equal(t, map[string]int{"docs/a.md": 1, "docs/b.md": 1}, index.Postings["beta"])
	})

	t.Run("deterministic: identical docs → identical index (shards + postings)", func(t *testing.T) {
		docs := []retrieval.SemanticDoc{
			{Path: "docs/a.md", Content: "alpha beta"},
			{Path: "docs/b.md", Content: "beta gamma"},
		}
		assert.Equal(t, retrieval.BuildSemanticIndex(docs), retrieval.BuildSemanticIndex(docs))
	})

	t.Run("duplicate paths: last entry wins (replace semantics)", func(t *testing.T) {
		index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
			{Path: "docs/a.md", Content: "alpha"},
			{Path: "docs/a.md", Content: "delta"},
		})
		shard := index.Shards["docs/a.md"]
		require.NotNil(t, shard)
		assert.Equal(t, docutil.ContentHash("delta"), shard.ContentHash)
		assert.NotContains(t, index.Postings, "alpha")
		assert.Equal(t, map[string]int{"docs/a.md": 1}, index.Postings["delta"])
	})
}

func TestQueryIndexScoring(t *testing.T) {
	index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
		{Path: "docs/b.md", Content: "beta gamma"},
		{Path: "docs/a.md", Content: "alpha beta beta"},
	})

	t.Run("scores distinct-token coverage plus capped repetition (10 + min(tf-1, 4))", func(t *testing.T) {
		// docs/a.md: alpha → 10; beta tf=2 → 10 + 1 = 11 ⇒ 21. docs/b.md: beta → 10.
		assert.Equal(t, []retrieval.SemanticHit{
			{Path: "docs/a.md", Score: 21},
			{Path: "docs/b.md", Score: 10},
		}, retrieval.QueryIndex(index, []string{"alpha", "beta"}, nil))
	})

	t.Run("breaks score ties by path ascending", func(t *testing.T) {
		ties := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
			{Path: "docs/z.md", Content: "same word"},
			{Path: "docs/a.md", Content: "same word"},
		})
		hits := retrieval.QueryIndex(ties, []string{"same", "word"}, nil)
		paths := make([]string, 0, len(hits))
		for _, hit := range hits {
			paths = append(paths, hit.Path)
		}
		assert.Equal(t, []string{"docs/a.md", "docs/z.md"}, paths)
	})

	t.Run("limit keeps the sorted prefix; empty/stopword queries return []", func(t *testing.T) {
		assert.Equal(t, []retrieval.SemanticHit{{Path: "docs/a.md", Score: 11}},
			retrieval.QueryIndex(index, []string{"beta"}, &retrieval.QueryIndexOptions{Limit: intp(1)}))
		assert.Empty(t, retrieval.QueryIndex(index, nil, nil))
		assert.Empty(t, retrieval.QueryIndex(index, retrieval.SemanticTokens("the of and"), nil))
	})

	t.Run("deterministic: identical query → identical hits", func(t *testing.T) {
		assert.Equal(t,
			retrieval.QueryIndex(index, []string{"alpha", "beta"}, nil),
			retrieval.QueryIndex(index, []string{"beta", "alpha"}, nil))
	})
}

func TestRefreshShardLazyPerShardRebuild(t *testing.T) {
	t.Run("replaces one shard and its posting lists, leaving other shards untouched", func(t *testing.T) {
		index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
			{Path: "docs/a.md", Content: "alpha beta"},
			{Path: "docs/b.md", Content: "gamma"},
		})
		retrieval.RefreshShard(index, retrieval.SemanticDoc{Path: "docs/a.md", Content: "alpha delta"})
		shard := index.Shards["docs/a.md"]
		require.NotNil(t, shard)
		assert.Equal(t, docutil.ContentHash("alpha delta"), shard.ContentHash)
		hits := retrieval.QueryIndex(index, []string{"delta"}, nil)
		require.Len(t, hits, 1)
		assert.Equal(t, "docs/a.md", hits[0].Path)
		assert.Empty(t, retrieval.QueryIndex(index, []string{"beta"}, nil)) // old token gone for a.md
		hits = retrieval.QueryIndex(index, []string{"gamma"}, nil)
		require.Len(t, hits, 1)
		assert.Equal(t, "docs/b.md", hits[0].Path) // untouched
	})

	t.Run("upserts a shard for a doc the index never held (lazy create)", func(t *testing.T) {
		index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{{Path: "docs/a.md", Content: "alpha"}})
		retrieval.RefreshShard(index, retrieval.SemanticDoc{Path: "docs/new.md", Content: "nova"})
		hits := retrieval.QueryIndex(index, []string{"nova"}, nil)
		require.Len(t, hits, 1)
		assert.Equal(t, "docs/new.md", hits[0].Path)
		hits = retrieval.QueryIndex(index, []string{"alpha"}, nil)
		require.Len(t, hits, 1)
		assert.Equal(t, "docs/a.md", hits[0].Path)
	})

	t.Run("query results are identical for a refreshed index vs a rebuilt index", func(t *testing.T) {
		refreshed := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
			{Path: "docs/a.md", Content: "alpha beta"},
			{Path: "docs/b.md", Content: "gamma"},
		})
		retrieval.RefreshShard(refreshed, retrieval.SemanticDoc{Path: "docs/a.md", Content: "delta beta"})
		rebuilt := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
			{Path: "docs/a.md", Content: "delta beta"},
			{Path: "docs/b.md", Content: "gamma"},
		})
		assert.Equal(t,
			retrieval.QueryIndex(rebuilt, []string{"delta", "gamma"}, nil),
			retrieval.QueryIndex(refreshed, []string{"delta", "gamma"}, nil))
	})
}

func TestStalenessTolerated(t *testing.T) {
	const contentA = "alpha beta"
	const contentB = "alpha gamma"

	t.Run("isStale detects a shard built from older content; matching hash → false", func(t *testing.T) {
		index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{{Path: "docs/a.md", Content: contentA}})
		assert.True(t, retrieval.IsStale(index, "docs/a.md", docutil.ContentHash(contentB)))
		assert.False(t, retrieval.IsStale(index, "docs/a.md", docutil.ContentHash(contentA)))
	})

	t.Run("a missing shard is non-coverage, not staleness (false)", func(t *testing.T) {
		index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{{Path: "docs/a.md", Content: contentA}})
		assert.False(t, retrieval.IsStale(index, "docs/never.md", "whatever"))
	})

	t.Run("querying a stale shard still returns hits, flagged stale: true", func(t *testing.T) {
		index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{{Path: "docs/a.md", Content: contentA}})
		// Doc changed to contentB WITHOUT refresh: the shard still carries old tokens.
		assert.Equal(t, []retrieval.SemanticHit{{Path: "docs/a.md", Score: 10, Stale: boolp(true)}},
			retrieval.QueryIndex(index, []string{"alpha"}, &retrieval.QueryIndexOptions{
				CurrentHashes: map[string]string{"docs/a.md": docutil.ContentHash(contentB)},
			}))
	})

	t.Run("current hashes matching the shard, or absent from the map, carry no stale flag", func(t *testing.T) {
		index := retrieval.BuildSemanticIndex([]retrieval.SemanticDoc{
			{Path: "docs/a.md", Content: contentA},
			{Path: "docs/b.md", Content: "beta"},
		})
		assert.Equal(t, []retrieval.SemanticHit{
			{Path: "docs/a.md", Score: 20},
			{Path: "docs/b.md", Score: 10},
		}, retrieval.QueryIndex(index, []string{"alpha", "beta"}, &retrieval.QueryIndexOptions{
			CurrentHashes: map[string]string{"docs/a.md": docutil.ContentHash(contentA)},
		}))
	})
}
