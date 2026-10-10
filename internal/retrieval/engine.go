package retrieval

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Retrieval engine (P1-T13; ARCH §4.5). Pure, deterministic candidate retrieval
// over the strictly ordered level hierarchy: level 1 (explicit manifest,
// literal sources), 2 (path mapping via glob sources), 3 (symbol mapping —
// P11-T04), 4 (API/schema mapping — P11-T04), 5 (concept/document graph
// siblings), 6 (semantic lexical fallback — OD-R37).
//
// Levels 3–4 (Phase 11, F11-2/P11-T04) run ONLY when their evidence inputs are
// present: level 3 needs `symbols_detail` on a non-formatting-only changed
// file; level 4 needs a classified OpenAPI delta (`apiDelta`) and/or key-level
// config diffs (`configDiff`). ABSENT evidence keeps that level's DECLARED gap
// row in `coverageGaps`, never silent (D14/OD-4). Every 3/4 provenance entry
// carries its structured `evidence` payload (per-level provenance, MANDATORY —
// ARCH §4.5).
//
// Mention strategies at levels 3–4 (deterministic, in this order):
//   - (a) claim-text mentions — a stored claim whose text mentions the symbol
//     name / API path or schema name / config key anchors its `anchor.file`
//     (allowlist-filtered). Claim anchors are committed, content-hash-anchored
//     state → DETERMINISTIC evidence (auto-eligible).
//   - (b) index mentions — the injected lexical index's doc-content tokens: a
//     doc whose shard contains ALL tokens of the mention text is hit. This is
//     RECALL-grade (caller-supplied cache data, D7) — recall-only candidates
//     route to REVIEW exactly like level-6-only candidates (V-P1-8, via
//     `semanticOnly`).
//
// Determinism: identical inputs → identical result (manifest-order doc
// iteration, sorted concept expansion, integer lexical scores, total-order
// ranking). Purity: the semantic index is INJECTED and never mutated here; the
// engine performs no I/O and reads no wall clock.
//
// Traversal definitions implemented (binding, see REPORT/AGENTS context):
//   - Level 1: a doc is hit at level 1 for changed path `p` when one of its
//     manifest sources is a LITERAL pattern equal to `p` (exact path equality,
//     graph.IsLiteralSourcePattern — strongest evidence).
//   - Level 2: a doc is hit at level 2 for `p` when one of its GLOB sources
//     matches `p` through the core glob engine (graph.SourceMatchesPath). A doc
//     is never credited level 2 for a path its literal source matched (level 1
//     wins within the same doc/path); glob hits from other changed paths still
//     add level-2 provenance.
//   - Level 3: symbols = the verbatim `symbols_detail` names (added, removed,
//     changed — first-occurrence dedupe) of every non-formatting-only changed
//     file, mapped to docs by the mention strategies above.
//   - Level 4: API path/schema expressions derived from the delta locations
//     (`/paths/<token>` → unescaped path; `/components/schemas/<n>` → schema
//     name) plus the verbatim config-diff keys, mapped to docs by the same
//     mention strategies. Evidence ABSENT for a level → its declared gap row,
//     never silent (D14/OD-4).
//   - Level 5 (single hop, no transitive closure): let S be the docs hit at
//     level 1/2 and C the union of their mapping concepts.
//     (a) concept siblings — for each c ∈ C (sorted), every manifest doc whose
//     mapping declares c is hit at level 5, UNLESS the only seed declaring c is
//     the holder itself (self-sibling excluded; a seed sharing c with a
//     DIFFERENT seed is legitimately hit at 5 too and keeps its best level).
//     Provenance: one entry per shared concept.
//     (b) claim concepts — every claim whose `concept` ∈ C anchors its
//     `anchor.file` at level 5 (provenance `claim:<id>`), provided that file is
//     in the manifest allowlist; claims without a concept or anchored outside
//     the manifest contribute nothing. Level-1/2 seed eligibility is UNCHANGED
//     by levels 3–4: a level-3/4-only doc never seeds concept expansion
//     (levels 1–2/5/6 outputs are preserved).
//   - Level 6: the changed paths (+ `old_path` for renames) of all changed
//     files EXCEPT formatting-only ones are tokenized (SemanticTokens) and
//     queried against the injected lexical index. Hits outside the manifest
//     allowlist are ignored (the index is caller-supplied cache data, not an
//     authority — D7); staleness supplied via `docContentHashes` flows into
//     provenance. Level 6 is fallback recall only: a candidate hit ONLY at
//     level 6 is `semanticOnly` and can never drive an automatic action
//     (V-P1-8 guard) — Phase 2 policy MUST route it to REVIEW.
//
// Candidate = DOC path. A doc hit at several levels keeps its BEST (lowest)
// level and lists every level in `levels`/`provenance`. Ranking: level
// ascending, then score descending (level-6 candidates only), then path
// ascending — a deterministic total order. Per-level caps truncate that order
// deterministically and report `overflow` honestly (Phase 2/5 turn overflow
// into REVIEW routing).

// CodeRetrievalConfig is the coded-error code carried by RetrievalConfigError.
// The TS RetrievalConfigError was a plain Error subclass (no EDOCSYNC code), so
// per the migration conventions the closest listed code is used: docutil.CodeConfig
// (EDOCSYNC_CONFIG — a configuration problem, fail closed, exit code 2).
const CodeRetrievalConfig = docutil.CodeConfig

// RetrievalConfigError mirrors the TS RetrievalConfigError: a
// retrieval-specific configuration problem (malformed caps). An UNUSABLE graph
// (composition errors) is NOT retrieval-specific: it returns the SAME shared
// graph.GraphUnusableError as every other routing consumer (single home in
// graph/errors.go, fail closed, D8).
type RetrievalConfigError struct {
	// Message is the TS message text, preserved exactly.
	Message string
	inner   *docutil.Error
}

// NewRetrievalConfigError builds the coded error.
func NewRetrievalConfigError(message string) *RetrievalConfigError {
	return &RetrievalConfigError{Message: message, inner: docutil.New(CodeRetrievalConfig, message)}
}

// Error renders the coded error (`EDOCSYNC_CONFIG: <message>`).
func (e *RetrievalConfigError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_CONFIG.
func (e *RetrievalConfigError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *RetrievalConfigError) Unwrap() error { return e.inner }

// ApiDeltaEntry is one classified OpenAPI delta. SEAM: mirrors the
// internal/analysis OpenApiDeltaEntry (the only field retrieval reads is
// Location). Declared locally — the same documented seam pattern
// internal/contracts used — so this package builds stand-alone.
type ApiDeltaEntry struct {
	// Location is the verbatim JSON-pointer-style delta location.
	Location string `json:"location"`
}

// ApiDelta is a classified OpenAPI delta. SEAM: mirrors internal/analysis
// OpenApiDeltaClassification (only Deltas is read here).
type ApiDelta struct {
	Deltas []ApiDeltaEntry `json:"deltas"`
}

// ConfigKeyDiffChange classifies one config-key diff. SEAM: mirrors
// internal/analysis ConfigKeyDiffChange.
type ConfigKeyDiffChange string

// The config-key diff changes (TS ConfigKeyDiffChange values).
const (
	ConfigKeyDiffAdded   ConfigKeyDiffChange = "added"
	ConfigKeyDiffRemoved ConfigKeyDiffChange = "removed"
	ConfigKeyDiffChanged ConfigKeyDiffChange = "changed"
)

// ConfigKeyDiff is one key-level resolved-value diff entry. SEAM: mirrors
// internal/analysis ConfigKeyDiff (only Key is read here).
type ConfigKeyDiff struct {
	Key          string              `json:"key"`
	Change       ConfigKeyDiffChange `json:"change"`
	SecretMasked bool                `json:"secretMasked,omitempty"`
}

// RetrieveCandidatesInput is the engine input. Everything is injected; nothing
// is fetched.
type RetrieveCandidatesInput struct {
	// ChangeAnalysis is the change record (deterministic, cacheable by blob SHAs).
	ChangeAnalysis schemas.ChangeAnalysis
	// Graph is the composed document graph (MUST be error-free — see RetrieveCandidates).
	Graph graph.DocumentGraph
	// Claims are stored claims (optional; only claims WITH a concept contribute at level 5).
	Claims []schemas.Claim
	// Manifest is the manifest allowlist — the doc universe (D7); graph mappings compose from it.
	Manifest schemas.Manifest
	// SemanticIndex is the injected lexical index (P1-T14); nil → no level-6 recall at all.
	SemanticIndex *SemanticIndex
	// DocContentHashes maps current doc content hashes (path → sha256 hex); enables level-6 staleness flags.
	DocContentHashes map[string]string
	// Caps holds per-level caps (levels 1–6); omitted = uncapped. Negative caps are refused.
	Caps RetrievalCaps
	// ApiDelta is the level-4 evidence: classified OpenAPI delta (locations); omitted/empty = no API evidence.
	ApiDelta *ApiDelta
	// ConfigDiff is the level-4 evidence: key-level config diffs (keys); omitted/empty = no config evidence.
	ConfigDiff []ConfigKeyDiff
}

// accumulator collects one candidate's provenance entries (first-insertion order
// preserved, deduplicated by provenanceKey) and its hit level set.
type accumulator struct {
	provenance   map[string]RetrievalProvenance
	provenanceID []string
	levels       map[RetrievalLevel]struct{}
}

func newAccumulator() *accumulator {
	return &accumulator{provenance: map[string]RetrievalProvenance{}, levels: map[RetrievalLevel]struct{}{}}
}

func (a *accumulator) mark(entry RetrievalProvenance) {
	key := provenanceKey(entry)
	if _, seen := a.provenance[key]; seen {
		return
	}
	a.provenance[key] = entry
	a.provenanceID = append(a.provenanceID, key)
	a.levels[entry.Level] = struct{}{}
}

func (a *accumulator) entries() []RetrievalProvenance {
	out := make([]RetrievalProvenance, 0, len(a.provenanceID))
	for _, key := range a.provenanceID {
		out = append(out, a.provenance[key])
	}
	return out
}

func (a *accumulator) sortedLevels() []RetrievalLevel {
	out := make([]RetrievalLevel, 0, len(a.levels))
	for level := range a.levels {
		out = append(out, level)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

/* ------------------------------------------------------- level 3–4 helpers */

// mentions is the identifier/path-boundary mention test (TS `mentions`, whose
// lookaround regexp `(?<![A-Za-z0-9_$])…(?![A-Za-z0-9_$])` has no RE2
// equivalent): the matched text must not be flanked by identifier characters (so
// `port` never matches inside `poolSize`); the leading `/` of `/payments/{id}`
// and trailing path punctuation are legal neighbors (not identifier characters).
// The needle is matched LITERALLY (the TS version escaped every regexp
// metacharacter first).
func mentions(text, needle string, caseInsensitive bool) bool {
	if needle == "" {
		return false
	}
	textRunes := []rune(text)
	needleRunes := []rune(needle)
	limit := len(textRunes) - len(needleRunes)
	for i := 0; i <= limit; i++ {
		if !runeEqual(textRunes[i], needleRunes[0], caseInsensitive) {
			continue
		}
		matched := true
		for j := 1; j < len(needleRunes); j++ {
			if !runeEqual(textRunes[i+j], needleRunes[j], caseInsensitive) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if i > 0 && isIdentifierRune(textRunes[i-1]) {
			continue
		}
		if i+len(needleRunes) < len(textRunes) && isIdentifierRune(textRunes[i+len(needleRunes)]) {
			continue
		}
		return true
	}
	return false
}

// runeEqual compares two runes, optionally case-insensitively (TS regexp `i` flag).
func runeEqual(a, b rune, caseInsensitive bool) bool {
	if a == b {
		return true
	}
	if !caseInsensitive {
		return false
	}
	return unicode.ToLower(a) == unicode.ToLower(b)
}

// isIdentifierRune reports whether r is a JavaScript identifier character
// (TS [A-Za-z0-9_$]).
func isIdentifierRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '$'
}

// unescapePointerToken is a JSON-pointer token unescape (RFC 6901): `~1` → `/`,
// then `~0` → `~` (TS unescapePointerToken).
func unescapePointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	return strings.ReplaceAll(token, "~0", "~")
}

// apiExpressionsFor returns the API path/schema expressions a document might
// reference, derived from the classifier's delta locations:
// `/paths/<token>/...` → the unescaped path (e.g.
// `/paths/~1payments~1{id}/get/...` → `payments/{id}`),
// `/components/schemas/<name>/...` → the unescaped schema name. First
// occurrence dedupe; other locations contribute nothing.
func apiExpressionsFor(location string) []string {
	segments := strings.Split(location, "/")
	if len(segments) > 2 && segments[1] == "paths" {
		return []string{unescapePointerToken(segments[2])}
	}
	if len(segments) > 3 && segments[1] == "components" && segments[2] == "schemas" {
		return []string{unescapePointerToken(segments[3])}
	}
	return nil
}

// collectChangedSymbols returns the changed symbol names from `symbols_detail`
// (P11-T03), in file order and added→removed→changed order within a file, first
// occurrence dedupe. Formatting-only files carry no content signal (same
// convention as level 6).
func collectChangedSymbols(files []schemas.ChangedFile) []string {
	symbols := []string{}
	for i := range files {
		file := files[i]
		if isFormattingOnly(file) || file.SymbolsDetail == nil {
			continue
		}
		for _, name := range file.SymbolsDetail.Added {
			if !containsString(symbols, name) {
				symbols = append(symbols, name)
			}
		}
		for _, name := range file.SymbolsDetail.Removed {
			if !containsString(symbols, name) {
				symbols = append(symbols, name)
			}
		}
		for _, change := range file.SymbolsDetail.Changed {
			if !containsString(symbols, change.Name) {
				symbols = append(symbols, change.Name)
			}
		}
	}
	return symbols
}

// symbolEvidencePresent reports whether symbol evidence is present: any
// non-formatting-only file carries `symbols_detail`.
func symbolEvidencePresent(files []schemas.ChangedFile) bool {
	for i := range files {
		if !isFormattingOnly(files[i]) && files[i].SymbolsDetail != nil {
			return true
		}
	}
	return false
}

// isFormattingOnly mirrors the TS `formatting_only === true` test (absent flag ≠ formatting-only).
func isFormattingOnly(file schemas.ChangedFile) bool {
	return file.FormattingOnly != nil && *file.FormattingOnly
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// mentionItem is one mention evidence item for levels 3–4: the needle text to
// match, its matching mode, the provenance source prefix for index-mention
// hits, and the MANDATORY structured per-level evidence.
type mentionItem struct {
	// level is the retrieval level this evidence item belongs to (3 or 4).
	level RetrievalLevel
	// needle is the text to match.
	needle string
	// caseInsensitive selects case-insensitive matching.
	caseInsensitive bool
	// indexSourcePrefix is the provenance source prefix for index-mention hits
	// (`<prefix>:<needle>`).
	indexSourcePrefix string
	// evidence is the MANDATORY structured per-level evidence payload.
	evidence RetrievalLevelEvidence
}

// markMentionHits runs both deterministic mention strategies for the given
// evidence items:
//   - (a) claim-text mentions — `claim:<id>` sources, DETERMINISTIC evidence
//     (committed, content-hash-anchored claim state);
//   - (b) index doc-content mentions — `<prefix>:<needle>` sources,
//     RECALL-grade evidence (the injected index is caller-supplied cache data,
//     D7): a doc shard is hit when it contains ALL tokens of the needle.
//
// Both strategies are allowlist-filtered (D7) and iterate in deterministic
// order (claims in input order; shards path-ascending, matching the TS
// insertion order).
func markMentionHits(
	mark func(string, RetrievalProvenance),
	claims []schemas.Claim,
	recordsByDoc map[string]graph.MappingRecord,
	semanticIndex *SemanticIndex,
	items []mentionItem,
) {
	for _, item := range items {
		for _, claim := range claims {
			if !mentions(claim.Text, item.needle, item.caseInsensitive) {
				continue
			}
			if _, allowed := recordsByDoc[claim.Anchor.File]; !allowed {
				continue // allowlist (D7)
			}
			mark(claim.Anchor.File, RetrievalProvenance{
				Level:    item.level,
				Source:   "claim:" + claim.ID,
				Evidence: item.evidence,
			})
		}
		if semanticIndex == nil {
			continue
		}
		tokens := SemanticTokens(item.needle)
		if len(tokens) == 0 {
			continue
		}
		for _, path := range sortedKeys(semanticIndex.Shards) {
			if _, allowed := recordsByDoc[path]; !allowed {
				continue // allowlist (D7)
			}
			if !shardContainsAllTokens(semanticIndex.Shards[path], tokens) {
				continue
			}
			mark(path, RetrievalProvenance{
				Level:    item.level,
				Source:   item.indexSourcePrefix + ":" + item.needle,
				Evidence: item.evidence,
			})
		}
	}
}

// shardContainsAllTokens reports whether the shard holds every query token
// (TS `tokens.every((token) => shard.tokens.includes(token))`).
func shardContainsAllTokens(shard *SemanticShard, tokens []string) bool {
	for _, token := range tokens {
		if _, ok := shard.Counts[token]; !ok {
			return false
		}
	}
	return true
}

// validateCaps asserts caps are sane (fail loud on programming errors, never
// clamped silently). Iteration is in level order so the reported error is
// deterministic.
func validateCaps(caps RetrievalCaps) error {
	if caps == nil {
		return nil
	}
	for _, level := range LevelOrder {
		cap, ok := caps[level]
		if !ok {
			continue
		}
		if cap < 0 {
			return NewRetrievalConfigError(fmt.Sprintf(
				"invalid retrieval cap for level %d: %d (must be a non-negative integer)",
				level, cap))
		}
	}
	return nil
}

// ParseRetrievalCaps converts caps decoded from an untyped source (JSON/YAML
// numbers arrive as float64) into RetrievalCaps, refusing negative or
// non-integer values exactly as the TS Number.isInteger guard did.
func ParseRetrievalCaps(raw map[RetrievalLevel]float64) (RetrievalCaps, error) {
	caps := RetrievalCaps{}
	for _, level := range LevelOrder {
		value, ok := raw[level]
		if !ok {
			continue
		}
		if value != float64(int(value)) || value < 0 {
			return nil, NewRetrievalConfigError(fmt.Sprintf(
				"invalid retrieval cap for level %d: %s (must be a non-negative integer)",
				level, strconv.FormatFloat(value, 'g', -1, 64)))
		}
		caps[level] = int(value)
	}
	return caps, nil
}

// RetrieveCandidates retrieves candidate docs for the changed set. Returns the
// shared graph.GraphUnusableError when the document graph carries composition
// errors (a graph with errors is UNUSABLE — consumers must refuse to route on
// it, fail closed, D8) and RetrievalConfigError when caps are malformed.
func RetrieveCandidates(input RetrieveCandidatesInput) (RetrievalResult, error) {
	if err := validateCaps(input.Caps); err != nil {
		return RetrievalResult{}, err
	}
	if len(input.Graph.Errors) > 0 {
		return RetrievalResult{}, graph.NewGraphUnusableError(input.Graph.Errors)
	}

	// Doc universe: the manifest allowlist (D7). Graph mappings are looked up
	// per doc; a manifest doc without a mapping (shouldn't happen when the
	// graph was composed from this manifest) degrades to a bare record with no
	// sources/concepts — it can then only surface via level-6 recall.
	recordsByDoc := make(map[string]graph.MappingRecord, len(input.Manifest.Documents))
	recordOrder := make([]string, 0, len(input.Manifest.Documents))
	for _, entry := range input.Manifest.Documents {
		record, found := mappingFor(input.Graph, entry.Path)
		if !found {
			record = graph.MappingRecord{Doc: entry.Path, Sources: []string{}, Concepts: []string{}, Contracts: []string{}}
		}
		recordsByDoc[entry.Path] = record
		recordOrder = append(recordOrder, entry.Path)
	}

	accByDoc := map[string]*accumulator{}
	accOrder := []string{}
	mark := func(doc string, entry RetrievalProvenance) {
		acc, ok := accByDoc[doc]
		if !ok {
			acc = newAccumulator()
			accByDoc[doc] = acc
			accOrder = append(accOrder, doc)
		}
		acc.mark(entry)
	}

	// Levels 1–2: literal (exact equality) beats glob within one doc/path.
	for _, file := range input.ChangeAnalysis.Files {
		for _, doc := range recordOrder {
			record := recordsByDoc[doc]
			literal, glob := "", ""
			haveLiteral, haveGlob := false, false
			for _, source := range record.Sources {
				if graph.IsLiteralSourcePattern(source) {
					if !haveLiteral && source == file.Path {
						literal, haveLiteral = source, true
					}
				} else if !haveGlob {
					// The TS loop short-circuited on the first glob match, so a
					// later glob never even reached the glob engine.
					ok, err := graph.SourceMatchesPath(source, file.Path)
					if err != nil {
						return RetrievalResult{}, err
					}
					if ok {
						glob, haveGlob = source, true
					}
				}
			}
			if haveLiteral {
				mark(record.Doc, RetrievalProvenance{Level: RetrievalLevel1, Source: "manifest-source:" + literal})
			} else if haveGlob {
				mark(record.Doc, RetrievalProvenance{Level: RetrievalLevel2, Source: "manifest-source:" + glob})
			}
		}
	}

	// Level 3: symbol mapping (P11-T04; ARCH §4.5). Evidence: `symbols_detail`
	// on non-formatting-only changed files. Changed symbols map to docs via
	// claim-text mentions (deterministic, auto-eligible) and index doc-content
	// mentions (recall-grade, review-gated). Absent evidence → declared gap
	// row, never silent (D14/OD-4).
	symbols := collectChangedSymbols(input.ChangeAnalysis.Files)
	if len(symbols) > 0 {
		items := make([]mentionItem, 0, len(symbols))
		for _, name := range symbols {
			items = append(items, mentionItem{
				level:             RetrievalLevel3,
				needle:            name,
				caseInsensitive:   false,
				indexSourcePrefix: "symbol-mention",
				evidence:          RetrievalSymbolEvidence{Symbols: []string{name}},
			})
		}
		markMentionHits(mark, input.Claims, recordsByDoc, input.SemanticIndex, items)
	}

	// Level 4: API/schema mapping (P11-T04; ARCH §4.5). Evidence: the classified
	// OpenAPI delta (paths + component-schema names from delta locations) and/or
	// key-level config diffs; both map to docs via the same two mention
	// strategies as level 3. Absent evidence → declared gap row, never silent
	// (D14/OD-4).
	apiLocations := []ApiDeltaEntry{}
	if input.ApiDelta != nil {
		apiLocations = append(apiLocations, input.ApiDelta.Deltas...)
	}
	configKeys := []ConfigKeyDiff{}
	if input.ConfigDiff != nil {
		configKeys = append(configKeys, input.ConfigDiff...)
	}
	if len(apiLocations) > 0 || len(configKeys) > 0 {
		items := []mentionItem{}
		for _, delta := range apiLocations {
			for _, expression := range apiExpressionsFor(delta.Location) {
				items = append(items, mentionItem{
					level:             RetrievalLevel4,
					needle:            expression,
					caseInsensitive:   true,
					indexSourcePrefix: "api-path-mention",
					evidence:          RetrievalApiSchemaEvidence{Locations: []string{delta.Location}, Keys: []string{}},
				})
			}
		}
		for _, diff := range configKeys {
			items = append(items, mentionItem{
				level:             RetrievalLevel4,
				needle:            diff.Key,
				caseInsensitive:   true,
				indexSourcePrefix: "config-key-mention",
				evidence:          RetrievalApiSchemaEvidence{Locations: []string{}, Keys: []string{diff.Key}},
			})
		}
		markMentionHits(mark, input.Claims, recordsByDoc, input.SemanticIndex, items)
	}

	// Level 5 seeds: docs hit at any deterministic path-mapping level.
	seedDocs := []string{}
	for _, doc := range accOrder {
		acc := accByDoc[doc]
		_, has1 := acc.levels[RetrievalLevel1]
		_, has2 := acc.levels[RetrievalLevel2]
		if has1 || has2 {
			seedDocs = append(seedDocs, doc)
		}
	}
	seedConcepts := map[string]struct{}{}
	for _, seed := range seedDocs {
		for _, concept := range recordsByDoc[seed].Concepts {
			seedConcepts[concept] = struct{}{}
		}
	}

	// Level 5a: concept siblings (sorted concept order; self-sibling excluded).
	concepts := make([]string, 0, len(seedConcepts))
	for concept := range seedConcepts {
		concepts = append(concepts, concept)
	}
	sort.Strings(concepts)
	for _, concept := range concepts {
		seedHolders := []string{}
		for _, seed := range seedDocs {
			if containsString(recordsByDoc[seed].Concepts, concept) {
				seedHolders = append(seedHolders, seed)
			}
		}
		if len(seedHolders) == 0 {
			continue
		}
		for _, doc := range recordOrder {
			record := recordsByDoc[doc]
			if !containsString(record.Concepts, concept) {
				continue
			}
			if seedHoldersOnlySelf(seedHolders, record.Doc) {
				continue
			}
			mark(record.Doc, RetrievalProvenance{Level: RetrievalLevel5, Source: "concept:" + concept})
		}
	}

	// Level 5b: claims whose concept matches a seed concept anchor their doc.
	for _, claim := range input.Claims {
		if claim.Concept == nil {
			continue
		}
		if _, seeded := seedConcepts[*claim.Concept]; !seeded {
			continue
		}
		if _, allowed := recordsByDoc[claim.Anchor.File]; !allowed {
			continue // allowlist (D7)
		}
		mark(claim.Anchor.File, RetrievalProvenance{Level: RetrievalLevel5, Source: "claim:" + claim.ID})
	}

	// Level 6: lexical fallback recall over change-derived tokens (OD-R37).
	if input.SemanticIndex != nil {
		paths := []string{}
		for _, file := range input.ChangeAnalysis.Files {
			if isFormattingOnly(file) {
				continue
			}
			if file.OldPath != nil {
				paths = append(paths, file.Path+" "+*file.OldPath)
			} else {
				paths = append(paths, file.Path)
			}
		}
		queryText := strings.Join(paths, " ")
		hits := QueryIndex(input.SemanticIndex, SemanticTokens(queryText), &QueryIndexOptions{CurrentHashes: input.DocContentHashes})
		for _, hit := range hits {
			if _, allowed := recordsByDoc[hit.Path]; !allowed {
				continue // allowlist (D7)
			}
			entry := RetrievalProvenance{Level: RetrievalLevel6, Source: "semantic-index", Score: &hit.Score}
			if hit.Stale != nil && *hit.Stale {
				stale := true
				entry.Stale = &stale
			}
			mark(hit.Path, entry)
		}
	}

	// Assemble candidates: best level + all levels; semantic-only detection.
	assembled := make([]RetrievalCandidate, 0, len(accOrder))
	for _, doc := range accOrder {
		acc := accByDoc[doc]
		levels := acc.sortedLevels()
		if len(levels) == 0 {
			continue
		}
		provenance := acc.entries()
		// V-P1-8 (P11-T04 extension): recall-grade hits are level 6 AND index-
		// mention level 3/4 hits — a candidate with no deterministic provenance
		// (1/2/5, or a claim-anchored 3/4 hit) routes to REVIEW at best.
		semanticOnly := true
		for _, entry := range provenance {
			if isDeterministicProvenance(entry) {
				semanticOnly = false
				break
			}
		}
		candidate := RetrievalCandidate{
			Doc:          doc,
			Level:        levels[0],
			Levels:       levels,
			Provenance:   provenance,
			SemanticOnly: semanticOnly,
		}
		if semanticOnly {
			candidate.MaxAction = MaxActionReview
		} else {
			candidate.MaxAction = MaxActionAuto
		}
		assembled = append(assembled, candidate)
	}

	// Ranking: level asc → score desc (level 6 only) → path asc (total order).
	sort.SliceStable(assembled, func(i, j int) bool {
		if assembled[i].Level != assembled[j].Level {
			return assembled[i].Level < assembled[j].Level
		}
		scoreDiff := scoreOf(assembled[j]) - scoreOf(assembled[i])
		if scoreDiff != 0 {
			return scoreDiff < 0
		}
		return assembled[i].Doc < assembled[j].Doc
	})

	// Per-level caps: deterministic truncation (sorted order kept, overflow dropped).
	candidates := []RetrievalCandidate{}
	overflow := []RetrievalOverflow{}
	for _, level := range LevelOrder {
		group := []RetrievalCandidate{}
		for _, candidate := range assembled {
			if candidate.Level == level {
				group = append(group, candidate)
			}
		}
		cap, capped := input.Caps[level]
		if !capped {
			candidates = append(candidates, group...)
			continue
		}
		if cap > len(group) {
			cap = len(group)
		}
		candidates = append(candidates, group[:cap]...)
		dropped := len(group) - cap
		if dropped > 0 {
			overflow = append(overflow, RetrievalOverflow{Level: level, Dropped: dropped})
		}
	}

	// Coverage gaps (levels 3–4, P11-T04): each level's DECLARED gap row is
	// reported exactly when its evidence input was ABSENT this run — the level
	// is never silently skipped (D14/OD-4). With evidence present the level ran
	// (even when it elected no candidates) and no gap row appears.
	coverageGaps := []RetrievalCoverageGap{}
	if !symbolEvidencePresent(input.ChangeAnalysis.Files) {
		coverageGaps = append(coverageGaps, RetrievalCoverageGap{
			Level:  InactiveLevel3,
			Status: "inactive",
			Reason: InactiveLevelReason,
		})
	}
	if len(apiLocations) == 0 && len(configKeys) == 0 {
		coverageGaps = append(coverageGaps, RetrievalCoverageGap{
			Level:  InactiveLevel4,
			Status: "inactive",
			Reason: InactiveLevelReason,
		})
	}

	caps := input.Caps
	if caps == nil {
		caps = RetrievalCaps{}
	}
	return RetrievalResult{Candidates: candidates, CoverageGaps: coverageGaps, Overflow: overflow, Caps: caps}, nil
}

// mappingFor finds the mapping record for doc in the composed graph (TS
// `graph.mappings.find((mapping) => mapping.doc === doc)`).
func mappingFor(documentGraph graph.DocumentGraph, doc string) (graph.MappingRecord, bool) {
	for _, mapping := range documentGraph.Mappings {
		if mapping.Doc == doc {
			return mapping, true
		}
	}
	return graph.MappingRecord{}, false
}

// seedHoldersOnlySelf reports whether every seed declaring the concept is the
// holder itself (self-sibling excluded, TS
// `seedHolders.every((seed) => seed === record.doc)`).
func seedHoldersOnlySelf(seedHolders []string, doc string) bool {
	for _, seed := range seedHolders {
		if seed != doc {
			return false
		}
	}
	return true
}

// scoreOf is the ranking score: the level-6 provenance score for level-6
// candidates, absent (-1 ranking floor) otherwise (TS scoreOf).
func scoreOf(candidate RetrievalCandidate) int {
	if candidate.Level != RetrievalLevel6 {
		return -1
	}
	for _, entry := range candidate.Provenance {
		if entry.Level == RetrievalLevel6 && entry.Score != nil {
			return *entry.Score
		}
	}
	return -1
}
