package retrieval

import (
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Semantic retrieval index (P1-T14; ARCH §4.5 level 6; OD-R37). V1 level-6
// retrieval is a DETERMINISTIC LEXICAL index: a token inverted index with
// per-doc shards, lazy per-shard rebuild, staleness tolerated (§3.7).
// Embedding-backed recall requires model access (Phase 2+ consumption
// decision); the interface stays identical, so that swap is data, not code.
// Zero model invocations in Phase 1 is binding (OD-R37).
//
// Contracts:
//   - FALLBACK RECALL ONLY — the index never decides anything by itself; its
//     hits are level-6 candidates that route to REVIEW at best (V-P1-8), and
//     correctness never depends on the index: with no index, retrieval simply
//     loses level-6 recall and gains nothing false (fail-closed direction);
//   - STALENESS TOLERATED — querying against a stale shard still returns its
//     results, flagged `stale: true` when the caller supplies current content
//     hashes (`CurrentHashes` / IsStale). A stale hit is evidence for a human
//     review, never for automation;
//   - DETERMINISM — identical inputs produce identical shards, posting lists
//     and scores (integer arithmetic, sorted iteration, path-ascending
//     tie-breaks). No wall-clock, no randomness, no corpus-dependent ranking.
//
// The Go port keeps the TS map shapes (doc path → shard, token → doc path →
// occurrence count) but never relies on Go map iteration order: every traversal
// that must be deterministic walks sorted keys, which is exactly the order the
// TS implementation got from its insertion-ordered Maps (shards are inserted
// path-ascending by BuildSemanticIndex).

// SemanticStopwords is the fixed V1 stopword set (bootstrap data, like
// thresholds in D11: revising it is a policy-data change, not a code change).
// Deliberately small — the lexical index only needs the most common English
// function words removed. Verbatim SEMANTIC_STOPWORDS from semantic.ts.
var SemanticStopwords = map[string]struct{}{
	"a": {}, "all": {}, "an": {}, "and": {}, "any": {}, "are": {}, "as": {},
	"at": {}, "be": {}, "been": {}, "but": {}, "by": {}, "can": {}, "do": {},
	"for": {}, "from": {}, "has": {}, "have": {}, "in": {}, "into": {},
	"is": {}, "it": {}, "its": {}, "not": {}, "of": {}, "on": {}, "or": {},
	"the": {}, "this": {}, "to": {}, "was": {}, "were": {}, "will": {},
	"with": {},
}

// MinTokenLength is the minimum token length after splitting (single
// characters carry no recall signal).
const MinTokenLength = 2

// SemanticShard is one doc's shard: its content hash plus the token multiset of
// its text.
type SemanticShard struct {
	// Path is the doc path (shard identity).
	Path string `json:"path"`
	// ContentHash is the sha256 hex of the doc content the shard was built from
	// (docutil.ContentHash convention).
	ContentHash string `json:"contentHash"`
	// Tokens holds distinct tokens, ascending.
	Tokens []string `json:"tokens"`
	// Counts maps token → occurrence count within this doc.
	Counts map[string]int `json:"counts"`
}

// SemanticIndex is the lexical index: per-doc shards plus the inverted posting
// lists (doc path → shard; token → doc path → occurrence count).
type SemanticIndex struct {
	Shards   map[string]*SemanticShard
	Postings map[string]map[string]int
}

// SemanticDoc is one indexed document (path + content).
type SemanticDoc struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// semanticTokenizeLower lowercases text (TS toLowerCase) for tokenization.
func semanticTokenizeLower(text string) string {
	return strings.ToLower(text)
}

// splitTokens splits already-lowercased text on non-alphanumeric runs, dropping
// tokens shorter than MinTokenLength and the fixed stopword set (TS
// TOKEN_SPLIT = /[^a-z0-9]+/ plus the two filters). Output order follows the
// text; callers that need set semantics dedupe + sort.
func splitTokens(lowered string) []string {
	runes := []rune(lowered)
	tokens := []string{}
	start := -1
	for i, r := range runes {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			tokens = appendKeptToken(tokens, string(runes[start:i]))
			start = -1
		}
	}
	if start >= 0 {
		tokens = appendKeptToken(tokens, string(runes[start:]))
	}
	return tokens
}

// appendKeptToken appends token when it survives the length + stopword filters.
func appendKeptToken(tokens []string, token string) []string {
	if len(token) < MinTokenLength {
		return tokens
	}
	if _, stop := SemanticStopwords[token]; stop {
		return tokens
	}
	return append(tokens, token)
}

// SemanticTokens tokenizes change-derived text deterministically: lowercase,
// split on non-alphanumeric runs, drop tokens shorter than 2 characters, drop
// the fixed stopword set, then DEDUPLICATE and sort ascending (set semantics —
// query weighting is overlap-based, so duplicates never help). Ported from
// semanticTokens in semantic.ts.
//
// Callers feed the engine's change-derived text: the changed file paths
// (path + `old_path` for renames) of every changed file EXCEPT formatting-only
// ones (formatting-only carries no content signal — the same convention the
// contract classifier uses).
func SemanticTokens(text string) []string {
	seen := map[string]struct{}{}
	for _, token := range splitTokens(semanticTokenizeLower(text)) {
		seen[token] = struct{}{}
	}
	tokens := make([]string, 0, len(seen))
	for token := range seen {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

// tokenizeContent tokenizes doc CONTENT into distinct tokens plus their
// occurrence counts (TS tokenizeContent).
func tokenizeContent(content string) ([]string, map[string]int) {
	counts := map[string]int{}
	for _, token := range splitTokens(semanticTokenizeLower(content)) {
		counts[token]++
	}
	tokens := make([]string, 0, len(counts))
	for token := range counts {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens, counts
}

// buildShard builds one doc's shard from its content (TS buildShard; the
// content hash uses the shared docutil.ContentHash convention).
func buildShard(path, content string) *SemanticShard {
	tokens, counts := tokenizeContent(content)
	return &SemanticShard{Path: path, ContentHash: docutil.ContentHash(content), Tokens: tokens, Counts: counts}
}

// removeFromPostings removes one path from every posting list it appears in
// (TS removeFromPostings).
func (index *SemanticIndex) removeFromPostings(path string, tokens []string) {
	for _, token := range tokens {
		paths, ok := index.Postings[token]
		if !ok {
			continue
		}
		delete(paths, path)
		if len(paths) == 0 {
			delete(index.Postings, token)
		}
	}
}

// addToPostings adds one path to the posting lists for its token counts
// (TS addToPostings).
func (index *SemanticIndex) addToPostings(path string, counts map[string]int) {
	for token, count := range counts {
		paths, ok := index.Postings[token]
		if !ok {
			index.Postings[token] = map[string]int{path: count}
			continue
		}
		paths[path] = count
	}
}

// BuildSemanticIndex builds an index from docs (duplicate paths: LAST entry
// wins, matching the replace semantics of RefreshShard). Shards are inserted in
// path-ascending order so every traversal is deterministic. Ported from
// buildSemanticIndex in semantic.ts.
func BuildSemanticIndex(docs []SemanticDoc) *SemanticIndex {
	byPath := map[string]string{}
	order := []string{}
	for _, doc := range docs {
		if _, seen := byPath[doc.Path]; !seen {
			order = append(order, doc.Path)
		}
		byPath[doc.Path] = doc.Content
	}
	sort.Strings(order)
	index := &SemanticIndex{Shards: map[string]*SemanticShard{}, Postings: map[string]map[string]int{}}
	for _, path := range order {
		shard := buildShard(path, byPath[path])
		index.Shards[path] = shard
		index.addToPostings(path, shard.Counts)
	}
	return index
}

// RefreshShard is the lazy per-shard rebuild (§3.7): replaces the shard for
// `doc.Path` (creating it when absent), recomputes its posting lists, and
// leaves every other shard untouched. MUTATES the injected index — the
// retrieval engine never calls this; orchestrators refresh shards when they
// observe changed doc content. Ported from refreshShard in semantic.ts.
func RefreshShard(index *SemanticIndex, doc SemanticDoc) {
	if existing, ok := index.Shards[doc.Path]; ok {
		index.removeFromPostings(doc.Path, existing.Tokens)
	}
	shard := buildShard(doc.Path, doc.Content)
	index.Shards[doc.Path] = shard
	index.addToPostings(doc.Path, shard.Counts)
}

// IsStale is the point staleness check: true when the index holds a shard for
// `path` whose content hash differs from the caller-supplied CURRENT hash. A
// missing shard is NOT stale — it is non-coverage (no level-6 recall for that
// doc, the fail-closed direction); staleness never fabricates coverage. Ported
// from isStale in semantic.ts.
func IsStale(index *SemanticIndex, path, contentHash string) bool {
	shard, ok := index.Shards[path]
	return ok && shard.ContentHash != contentHash
}

// QueryIndexOptions configures a query (TS QueryIndexOptions).
type QueryIndexOptions struct {
	// Limit caps the returned hits (applied after sorting); nil = all matches.
	Limit *int
	// CurrentHashes holds current doc content hashes (path → sha256 hex). When
	// supplied, a hit is flagged `stale: true` when its shard's content hash
	// differs from the supplied hash for that path. Omitted (or path absent from
	// the map): no staleness flag — the index never invents staleness.
	CurrentHashes map[string]string
}

// SemanticHit is one query hit: the doc path, its score, and its staleness flag
// when known.
type SemanticHit struct {
	Path  string `json:"path"`
	Score int    `json:"score"`
	// Stale is true when the queried shard was stale at query time (nil = unknown/absent).
	Stale *bool `json:"stale,omitempty"`
}

// Scoring rule (documented, integer, deterministic): for each DISTINCT query
// token present in the doc's shard, the doc earns `10` plus `1` per additional
// occurrence of that token in the doc, capped at 4 extras. I.e. score = Σ over
// matched tokens of (10 + min(tf − 1, 4)) — tenths of a token-weight unit.
// Higher is better; ties break by path ascending. Pure token-overlap: no corpus
// statistics, no idf, no floats.
const (
	// SemanticScoreBase is the per-distinct-token base score.
	SemanticScoreBase = 10
	// SemanticScoreMaxExtra caps the per-token repetition bonus (min(tf-1, 4)).
	SemanticScoreMaxExtra = 4
)

// QueryIndex queries the inverted index. Deterministic: identical index +
// tokens + options → identical hit list (score descending, path ascending
// tie-break, `Limit` keeps the sorted prefix). Empty/stopword-only token lists
// return no hits. Querying a stale shard still returns its hits — staleness
// tolerance means recall degrades gracefully and is flagged, never silently
// trusted. Ported from queryIndex in semantic.ts.
func QueryIndex(index *SemanticIndex, tokens []string, options *QueryIndexOptions) []SemanticHit {
	// Distinct query tokens, sorted (TS [...new Set(tokens)].sort()).
	distinct := map[string]struct{}{}
	for _, token := range tokens {
		distinct[token] = struct{}{}
	}
	ordered := make([]string, 0, len(distinct))
	for token := range distinct {
		ordered = append(ordered, token)
	}
	sort.Strings(ordered)

	scores := map[string]int{}
	for _, token := range ordered {
		paths, ok := index.Postings[token]
		if !ok {
			continue
		}
		// Sorted path iteration keeps the (order-independent) accumulation
		// deterministic regardless of Go map iteration order.
		for _, path := range sortedKeys(paths) {
			tf := paths[path]
			extra := tf - 1
			if extra > SemanticScoreMaxExtra {
				extra = SemanticScoreMaxExtra
			}
			scores[path] += SemanticScoreBase + extra
		}
	}

	hits := make([]SemanticHit, 0, len(scores))
	var currentHashes map[string]string
	var limit *int
	if options != nil {
		currentHashes = options.CurrentHashes
		limit = options.Limit
	}
	for _, path := range sortedKeys(scores) {
		hit := SemanticHit{Path: path, Score: scores[path]}
		if currentHashes != nil {
			if current, ok := currentHashes[path]; ok && IsStale(index, path, current) {
				stale := true
				hit.Stale = &stale
			}
		}
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
	if limit != nil {
		keep := *limit
		if keep < 0 {
			keep = 0
		}
		if keep > len(hits) {
			keep = len(hits)
		}
		hits = hits[:keep]
	}
	return hits
}

// sortedKeys returns a map's keys sorted ascending (the deterministic stand-in
// for the TS Maps' insertion order).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
