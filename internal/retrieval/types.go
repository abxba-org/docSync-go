package retrieval

import (
	"encoding/json"
	"strconv"
)

// Retrieval types (P1-T13; ARCH §4.5). Candidate retrieval walks the strictly
// ordered level hierarchy 1–6; each candidate carries its LEVEL as provenance.
// Phase 11 (F11-2, P11-T04): ALL six levels are ACTIVE. Levels 3 (symbol
// mapping) and 4 (API/schema mapping) run only when their evidence inputs are
// present — changed `symbols_detail`, a classified OpenAPI delta, key-level
// config diffs; ABSENT evidence keeps the level's DECLARED gap row in
// `coverageGaps`, never silent (D14/OD-4). Per-level provenance carries a
// structured `evidence` payload (MANDATORY at levels 3–4, ARCH §4.5).
//
// Level semantics (V1):
//   - 1 explicit manifest: the doc's manifest entry declares the changed path
//     as a LITERAL source (exact path equality — strongest evidence);
//   - 2 path mapping: the doc matched via a glob source (core glob engine);
//   - 3 symbol mapping (P11-T04): changed symbols (`symbols_detail`) → docs
//     claiming them, via claim-text mentions (deterministic) and lexical-index
//     doc-content mentions (recall-grade, review-gated like level 6);
//   - 4 API/schema mapping (P11-T04): OpenAPI delta locations + config-diff
//     keys → docs referencing those API paths / schema names / config keys,
//     via the same two mention strategies as level 3;
//   - 5 concept/document graph: sibling docs reached through concept nodes
//     from level-1/2 seed docs (one hop, no transitive closure) — precise
//     traversal is documented on RetrieveCandidates;
//   - 6 semantic retrieval: lexical-index fallback recall (OD-R37 — a
//     deterministic token inverted index; embeddings are a Phase 2+ data swap
//     behind the identical interface). Level 6 is FALLBACK RECALL ONLY, never
//     the source of truth: a semantic-only candidate cannot drive any
//     automatic action — it routes to REVIEW at best (V-P1-8 guard:
//     CanDriveAutomaticAction). Phase 2 policy MUST honor this.

// RetrievalLevel is one active retrieval level (TS RetrievalLevel = 1|2|3|4|5|6).
type RetrievalLevel int

// The active levels, in level order (LEVEL_ORDER).
const (
	RetrievalLevel1 RetrievalLevel = 1
	RetrievalLevel2 RetrievalLevel = 2
	RetrievalLevel3 RetrievalLevel = 3
	RetrievalLevel4 RetrievalLevel = 4
	RetrievalLevel5 RetrievalLevel = 5
	RetrievalLevel6 RetrievalLevel = 6
)

// LevelOrder is the strictly ordered level hierarchy (TS LEVEL_ORDER).
var LevelOrder = []RetrievalLevel{
	RetrievalLevel1, RetrievalLevel2, RetrievalLevel3,
	RetrievalLevel4, RetrievalLevel5, RetrievalLevel6,
}

// InactiveRetrievalLevel is a level whose gap row is EVIDENCE-CONDITIONAL
// (3 symbol mapping, 4 API/schema mapping): its gap entry appears only when the
// run carries no evidence input for the level, never otherwise.
type InactiveRetrievalLevel int

// The evidence-conditional levels (TS InactiveRetrievalLevel = 3 | 4).
const (
	InactiveLevel3 InactiveRetrievalLevel = 3
	InactiveLevel4 InactiveRetrievalLevel = 4
)

// InactiveLevelReason is the fixed reason text for an evidence-absent coverage
// gap at levels 3–4 (Phase 11 P11-T04: the levels are ACTIVE; the gap row now
// means "no evidence input this run", never silence — D14/OD-4). Referencing
// it in run records keeps the gap HONEST and greppable. The text is verbatim
// INACTIVE_LEVEL_REASON from types.ts.
const InactiveLevelReason = "symbol/API-schema retrieval levels 3–4 ran with no evidence input this run — " +
	"Phase 11 (F11-2) activation; absent evidence keeps the level declared inactive, never silent"

// InactiveLevels lists the evidence-conditional levels, in level order (each
// gaps only when its evidence is absent) — TS INACTIVE_LEVELS = [3, 4].
var InactiveLevels = []InactiveRetrievalLevel{InactiveLevel3, InactiveLevel4}

// RetrievalSymbolEvidence is the structured per-level evidence payload for
// level 3 (MANDATORY at level 3; ARCH §4.5).
type RetrievalSymbolEvidence struct {
	// Symbols are the changed symbol names (verbatim symbols_detail names) that
	// produced the hit.
	Symbols []string `json:"symbols"`
}

// RetrievalApiSchemaEvidence is the structured per-level evidence payload for
// level 4 (MANDATORY at level 4; ARCH §4.5).
type RetrievalApiSchemaEvidence struct {
	// Locations are the matched OpenAPI delta locations (verbatim classifier
	// output; empty for key-only hits).
	Locations []string `json:"locations"`
	// Keys are the matched config keys (verbatim config-diff keys; empty for
	// location-only hits).
	Keys []string `json:"keys"`
}

// RetrievalLevelEvidence is the per-level structured evidence carried on
// levels 3–4 provenance entries (TS RetrievalLevelEvidence union).
type RetrievalLevelEvidence interface {
	retrievalLevelEvidence()
}

// retrievalLevelEvidence seals the union: only the two structured payloads
// above implement it (compile-time equivalent of the TS union members).
func (RetrievalSymbolEvidence) retrievalLevelEvidence()    {}
func (RetrievalApiSchemaEvidence) retrievalLevelEvidence() {}

// RetrievalMaxAction is the highest action automation a candidate may drive
// (TS 'review' | 'auto').
type RetrievalMaxAction string

// The max-action values.
const (
	MaxActionReview RetrievalMaxAction = "review"
	MaxActionAuto   RetrievalMaxAction = "auto"
)

// RetrievalProvenance is one evidence line for a candidate hit. `Score`/`Stale`
// apply to level 6 only; `Evidence` is MANDATORY on level 3/4 entries
// (per-level provenance, ARCH §4.5) and absent on the other levels.
type RetrievalProvenance struct {
	Level RetrievalLevel `json:"level"`
	// Source is where the hit came from, e.g. `manifest-source:src/x.ts`,
	// `concept:refunds`, `claim:claim_1`, `semantic-index`,
	// `symbol-mention:idempotencyKey`.
	Source string `json:"source"`
	// Score is the lexical-index score (integer, tenths-of-token-weight units) —
	// level 6 only.
	Score *int `json:"score,omitempty"`
	// Stale is true when the queried shard was stale at query time — level 6 only.
	Stale *bool `json:"stale,omitempty"`
	// Evidence is the structured per-level evidence — levels 3–4 only (P11-T04).
	Evidence RetrievalLevelEvidence `json:"evidence,omitempty"`
}

// provenanceKey renders a provenance entry's dedupe identity, byte-compatible
// with the TS provenanceKey join (level, source, score, stale flag and the
// JSON.stringify of the structured evidence).
func provenanceKey(entry RetrievalProvenance) string {
	key := []byte{}
	key = append(key, strconv.Itoa(int(entry.Level))...)
	key = append(key, 0)
	key = append(key, entry.Source...)
	key = append(key, 0)
	if entry.Score != nil {
		key = append(key, strconv.Itoa(*entry.Score)...)
	}
	key = append(key, 0)
	if entry.Stale != nil && *entry.Stale {
		key = append(key, "stale"...)
	}
	key = append(key, 0)
	if entry.Evidence != nil {
		// The TS entry hashed JSON.stringify(entry.evidence); the struct tags
		// above carry the identical wire keys, so the bytes match.
		encoded, err := json.Marshal(entry.Evidence)
		if err == nil {
			key = append(key, encoded...)
		}
	}
	return string(key)
}

// RetrievalCandidate is one candidate DOC selected for investigation
// (retrieval selects documents, not patches).
type RetrievalCandidate struct {
	// Doc is the manifest document path.
	Doc string `json:"doc"`
	// Level is the BEST (lowest) level hit — the primary ranking level.
	Level RetrievalLevel `json:"level"`
	// Levels holds all levels hit, ascending (best level first).
	Levels []RetrievalLevel `json:"levels"`
	// Provenance holds every hit's evidence, deterministic order, deduplicated.
	Provenance []RetrievalProvenance `json:"provenance"`
	// SemanticOnly is true when the candidate carries NO deterministic
	// confirmation — its only hits are recall-grade: level 6, or an
	// index-mention hit at level 3/4 (lexical doc-content matching;
	// claim-anchored level 3/4 hits ARE deterministic). Such a candidate is
	// semantic recall without deterministic confirmation and routes to REVIEW
	// at best (V-P1-8).
	SemanticOnly bool `json:"semanticOnly"`
	// MaxAction is the highest action automation this candidate may drive:
	// MaxActionReview when semantic-only (V-P1-8 — Phase 2 policy MUST route
	// these to REVIEW), MaxActionAuto when corroborated at any deterministic
	// level (1/2/5, or a claim-anchored level 3/4 hit — committed,
	// content-hash-anchored state).
	MaxAction RetrievalMaxAction `json:"maxAction"`
}

// RetrievalCoverageGap is a declared gap entry (levels 3–4; reported per level
// exactly when that level's evidence inputs are absent — never silent).
type RetrievalCoverageGap struct {
	Level  InactiveRetrievalLevel `json:"level"`
	Status string                 `json:"status"`
	Reason string                 `json:"reason"`
}

// RetrievalOverflow is a deterministic truncation report: candidates dropped by
// a per-level cap.
type RetrievalOverflow struct {
	Level   RetrievalLevel `json:"level"`
	Dropped int            `json:"dropped"`
}

// RetrievalResult is the retrieval outcome. Deterministic for identical inputs.
type RetrievalResult struct {
	// Candidates are ranked: level ascending, then score descending (semantic
	// only), then path ascending.
	Candidates []RetrievalCandidate `json:"candidates"`
	// CoverageGaps: per level 3/4, present exactly when that level had NO
	// evidence input this run (declared gap, never silent).
	CoverageGaps []RetrievalCoverageGap `json:"coverageGaps"`
	// Overflow: per-level dropped counts (level order); empty when nothing was truncated.
	Overflow []RetrievalOverflow `json:"overflow"`
	// Caps are the effective per-level caps (echoed input; empty when uncapped).
	Caps RetrievalCaps `json:"caps"`
}

// RetrievalCaps holds the per-level caps (levels 1–6). Absent level = uncapped
// (TS Partial<Record<RetrievalLevel, number>> with omitted entries).
type RetrievalCaps map[RetrievalLevel]int

// CanDriveAutomaticAction is the V-P1-8 guard: a semantic-only candidate (no
// deterministic corroboration — levels 1/2/5, or a claim-anchored level 3/4
// hit) can NEVER drive an automatic action — Phase 2 policy must route it to
// REVIEW. Lexical recall (level 6 and index-mention level 3/4) is fallback
// only; every candidate that drives automation is traceable to deterministic
// evidence.
func CanDriveAutomaticAction(candidate RetrievalCandidate) bool {
	return !candidate.SemanticOnly
}

// isDeterministicProvenance reports whether the provenance entry is
// deterministic evidence (V-P1-8 classification). Claim-anchored level 3/4
// mentions count: the claim store is committed, content-hash-anchored state —
// deterministic, unlike the lexical index.
func isDeterministicProvenance(entry RetrievalProvenance) bool {
	switch entry.Level {
	case RetrievalLevel1, RetrievalLevel2, RetrievalLevel5:
		return true
	case RetrievalLevel3, RetrievalLevel4:
		return hasClaimSourcePrefix(entry.Source)
	default:
		return false
	}
}

const claimSourcePrefix = "claim:"

// hasClaimSourcePrefix reports whether the provenance source is a claim anchor
// (TS entry.source.startsWith('claim:')).
func hasClaimSourcePrefix(source string) bool {
	return len(source) >= len(claimSourcePrefix) && source[:len(claimSourcePrefix)] == claimSourcePrefix
}
