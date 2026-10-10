package workspace

// Ported from core/src/workspace/batch.ts (P3-T03; ARCH §7.1 item 6, §3.7).
// Deterministic, pure grouping of retrieval candidates into writer sessions
// within budget:
//
//  1. Shared-concept siblings (graph mappings) batch together FIRST — the R16
//     groundwork: one session over concept-connected docs reduces
//     cross-patch inconsistency between documents describing the same
//     concept.
//  2. Then level-1/2 candidates group by SHARED SOURCE-GLOB OVERLAP — exact
//     pattern-string intersection between the docs' manifest `sources` (NOT
//     semantic glob matching: two docs whose globs happen to match the same
//     file through different patterns do NOT batch — conservative + cheap +
//     deterministic).
//  3. Remaining candidates become singleton sessions (ARCH §7.1 item 3:
//     independent Sonnet sessions when unrelated).
//  4. Groups larger than MaxDocsPerSession split into consecutive chunks;
//     sessions beyond MaxSessionCount overflow → Unbatched, reported — Phase 5
//     routes overflow to REVIEW, never silently dropped (mirrors the retrieval
//     engine's honest overflow semantics, §3.7).
//
// Determinism: candidates keep their input order (the retrieval engine's
// deterministic rank order); groups are identified by union-find over
// deterministic iteration orders (input order for members, mapping order for
// patterns, graph order for concepts) and ordered by their smallest member
// input index; session reasons are composed from sorted name sets. Same
// inputs → deep-equal plan.
//
// An UNUSABLE graph (composition errors) returns the SAME shared
// *graph.GraphUnusableError as every other routing consumer (fail closed, D8).

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/retrieval"
)

// BatchSession is one writer session's doc assignment + deterministic grouping
// reason (TS BatchSession).
type BatchSession struct {
	Docs   []string
	Reason string
}

// BatchPlan is the deterministic batch plan; Unbatched docs overflow the
// session budget (TS BatchPlan).
type BatchPlan struct {
	Sessions  []BatchSession
	Unbatched []string
}

// BatchRelatedDocsInput is the batching input (TS BatchRelatedDocsInput).
type BatchRelatedDocsInput struct {
	// Candidates holds the retrieval candidates in their deterministic rank
	// order (input order is semantic).
	Candidates []retrieval.RetrievalCandidate
	// Graph is the composed document graph — MUST be error-free (fail closed,
	// D8).
	Graph graph.DocumentGraph
	// MaxDocsPerSession is the maximum docs per session (positive integer).
	MaxDocsPerSession int
	// MaxSessionCount is the maximum number of sessions per run (positive
	// integer); overflow → unbatched.
	MaxSessionCount int
}

// CodeBatchConfig is the coded-error code carried by BatchConfigError. The TS
// BatchConfigError was a plain Error subclass (no EDOCSYNC code), so per the
// migration conventions the closest listed code is used: docutil.CodeConfig
// (EDOCSYNC_CONFIG — a batching configuration problem, fail closed).
const CodeBatchConfig = docutil.CodeConfig

// BatchConfigError mirrors the TS BatchConfigError: a batching-specific
// configuration problem (malformed caps). An UNUSABLE graph (composition
// errors) is NOT batching-specific: it returns the SAME shared
// *graph.GraphUnusableError as every other routing consumer (fail closed, D8).
type BatchConfigError struct {
	// Message is the TS message text, preserved exactly.
	Message string
	inner   *docutil.Error
}

// NewBatchConfigError builds the coded error.
func NewBatchConfigError(message string) *BatchConfigError {
	return &BatchConfigError{Message: message, inner: docutil.New(CodeBatchConfig, message)}
}

// Error renders the coded error (`EDOCSYNC_CONFIG: <message>`).
func (e *BatchConfigError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_CONFIG.
func (e *BatchConfigError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *BatchConfigError) Unwrap() error { return e.inner }

/* ---------------------------------------------------------------- union-find */

// disjointSet is the path-compressed union-find used to group candidates
// (TS DisjointSet). The representative is always the SMALLEST index, which
// keeps the grouping deterministic regardless of union order.
type disjointSet struct {
	parent map[int]int
}

func newDisjointSet() *disjointSet {
	return &disjointSet{parent: map[int]int{}}
}

func (d *disjointSet) find(x int) int {
	root := x
	for {
		p, ok := d.parent[root]
		if !ok || p == root {
			break
		}
		root = p
	}
	current := x
	for current != root {
		next := d.parent[current]
		d.parent[current] = root
		current = next
	}
	return root
}

func (d *disjointSet) union(a, b int) {
	if _, ok := d.parent[a]; !ok {
		d.parent[a] = a
	}
	if _, ok := d.parent[b]; !ok {
		d.parent[b] = b
	}
	rootA := d.find(a)
	rootB := d.find(b)
	if rootA == rootB {
		return
	}
	// Deterministic representative: the smaller index always wins.
	if rootA < rootB {
		d.parent[rootB] = rootA
	} else {
		d.parent[rootA] = rootB
	}
}

func (d *disjointSet) has(x int) bool {
	_, ok := d.parent[x]
	return ok
}

/* ------------------------------------------------------------------ planner */

// assertPositiveInt mirrors the TS assertPositiveInt (a BatchConfigError).
func assertPositiveInt(value int, name string) error {
	if value <= 0 {
		return NewBatchConfigError(fmt.Sprintf("%s must be a positive integer, got %d", name, value))
	}
	return nil
}

// orderedHits records <key, candidate input indexes> pairs in FIRST-OCCURRENCE
// order (the TS Map insertion order) plus an index lookup, so iteration over
// the collected keys is deterministic in Go (map iteration is randomized).
type orderedHits struct {
	keys  []string
	byKey map[string][]int
}

func newOrderedHits() *orderedHits {
	return &orderedHits{byKey: map[string][]int{}}
}

func (o *orderedHits) add(key string, index int) {
	if _, ok := o.byKey[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.byKey[key] = append(o.byKey[key], index)
}

func (o *orderedHits) indexes(key string) []int { return o.byKey[key] }

// sortedKeys returns the keys in ascending order (the TS
// [...hits.entries()].sort(([a], [b]) => (a < b ? -1 : 1))).
func (o *orderedHits) sortedKeys() []string {
	keys := append([]string(nil), o.keys...)
	sort.Strings(keys)
	return keys
}

// rawGroup is one pre-session group (TS RawGroup).
type rawGroup struct {
	kind     string // 'concept' | 'pattern' | 'singleton'
	members  []int
	evidence []string
}

// batchGroupKind mirrors the TS RawGroup kind literals.
const (
	groupKindConcept   = "concept"
	groupKindPattern   = "pattern"
	groupKindSingleton = "singleton"
)

// BatchRelatedDocs plans the writer sessions (P3-T03). Pure; same inputs →
// deep-equal plan. See the file header for the binding grouping semantics.
//
// It returns a *BatchConfigError for malformed caps and the shared
// *graph.GraphUnusableError for an unusable graph (fail closed, D8).
func BatchRelatedDocs(input BatchRelatedDocsInput) (*BatchPlan, error) {
	if err := assertPositiveInt(input.MaxDocsPerSession, "maxDocsPerSession"); err != nil {
		return nil, err
	}
	if err := assertPositiveInt(input.MaxSessionCount, "maxSessionCount"); err != nil {
		return nil, err
	}
	if len(input.Graph.Errors) > 0 {
		return nil, graph.NewGraphUnusableError(input.Graph.Errors)
	}

	candidates := input.Candidates
	indexOfDoc := make(map[string]int, len(candidates))
	for index, candidate := range candidates {
		indexOfDoc[candidate.Doc] = index
	}

	sets := newDisjointSet()

	/* -- Phase A: shared-concept siblings (graph mappings) batch first ------- */

	// Concept → candidate input indexes, in graph (manifest) mapping order.
	conceptHits := newOrderedHits()
	for _, mapping := range input.Graph.Mappings {
		ownerIndex, ok := indexOfDoc[mapping.Doc]
		if !ok {
			continue
		}
		for _, concept := range mapping.Concepts {
			conceptHits.add(concept, ownerIndex)
		}
	}
	for _, key := range conceptHits.keys {
		indexes := conceptHits.indexes(key)
		for i := 1; i < len(indexes); i++ {
			sets.union(indexes[0], indexes[i])
		}
	}
	groupedByConcept := make(map[int]struct{}, len(candidates))
	for _, key := range conceptHits.keys {
		indexes := conceptHits.indexes(key)
		if len(indexes) >= 2 {
			for _, index := range indexes {
				groupedByConcept[index] = struct{}{}
			}
		}
	}

	/* -- Phase B: level-1/2 candidates grouped by shared source pattern ------ */

	isLevel12 := func(candidate retrieval.RetrievalCandidate) bool {
		return candidate.Level == retrieval.RetrievalLevel1 || candidate.Level == retrieval.RetrievalLevel2
	}

	patternHits := newOrderedHits()
	for index, candidate := range candidates {
		if !isLevel12(candidate) {
			continue
		}
		if _, grouped := groupedByConcept[index]; grouped {
			continue
		}
		mapping, ok := findMapping(input.Graph.Mappings, candidate.Doc)
		if !ok {
			continue // no mapping record → no source-overlap evidence
		}
		for _, pattern := range mapping.Sources {
			patternHits.add(pattern, index)
		}
	}
	for _, key := range patternHits.keys {
		indexes := patternHits.indexes(key)
		for i := 1; i < len(indexes); i++ {
			sets.union(indexes[0], indexes[i])
		}
	}
	groupedByPattern := make(map[int]struct{}, len(candidates))
	for _, key := range patternHits.keys {
		indexes := patternHits.indexes(key)
		if len(indexes) >= 2 {
			for _, index := range indexes {
				groupedByPattern[index] = struct{}{}
			}
		}
	}

	/* -- Phase C: remaining candidates are singleton groups ------------------ */

	for index := range candidates {
		if !sets.has(index) {
			sets.union(index, index)
		}
	}

	/* -- Groups → ordered member lists (smallest input index first) ---------- */

	membersByRoot := map[int][]int{}
	for index := range candidates {
		root := sets.find(index)
		members, ok := membersByRoot[root]
		if !ok {
			membersByRoot[root] = []int{index}
			continue
		}
		membersByRoot[root] = append(members, index)
	}

	// Group order: the map keys are the roots (each root IS the smallest
	// member index of its group); groups are then ordered by their smallest
	// member input index (a redundant, deterministic total order).
	roots := make([]int, 0, len(membersByRoot))
	for root := range membersByRoot {
		roots = append(roots, root)
	}
	sort.Ints(roots)

	groups := make([]rawGroup, 0, len(membersByRoot))
	for _, root := range roots {
		members := append([]int(nil), membersByRoot[root]...)
		sort.Ints(members)

		sharedConcepts := map[string]struct{}{}
		if allGrouped(members, groupedByConcept) {
			for _, concept := range conceptHits.sortedKeys() {
				if containsAll(members, conceptHits.indexes(concept)) {
					sharedConcepts[concept] = struct{}{}
				}
			}
		}
		if len(sharedConcepts) > 0 {
			groups = append(groups, rawGroup{
				kind:     groupKindConcept,
				members:  members,
				evidence: sortedSetKeys(sharedConcepts),
			})
			continue
		}

		sharedPatterns := map[string]struct{}{}
		if allGrouped(members, groupedByPattern) {
			for _, pattern := range patternHits.sortedKeys() {
				if containsAll(members, patternHits.indexes(pattern)) {
					sharedPatterns[pattern] = struct{}{}
				}
			}
		}
		if len(sharedPatterns) > 0 {
			groups = append(groups, rawGroup{
				kind:     groupKindPattern,
				members:  members,
				evidence: sortedSetKeys(sharedPatterns),
			})
			continue
		}

		groups = append(groups, rawGroup{
			kind:     groupKindSingleton,
			members:  members,
			evidence: []string{},
		})
	}

	// Groups are kept in root order (the map keys above ARE the smallest
	// member index of each group) and then ordered by smallest member input
	// index, giving a deterministic total order over the plan.
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].members[0] < groups[j].members[0]
	})

	/* -- Sessions: chunk oversized groups; overflow → unbatched -------------- */

	sessions := []BatchSession{}
	unbatched := []string{}

	for _, group := range groups {
		var chunks [][]int
		for start := 0; start < len(group.members); start += input.MaxDocsPerSession {
			end := start + input.MaxDocsPerSession
			if end > len(group.members) {
				end = len(group.members)
			}
			chunks = append(chunks, append([]int(nil), group.members[start:end]...))
		}
		for chunkIndex, memberChunk := range chunks {
			docs := make([]string, 0, len(memberChunk))
			for _, index := range memberChunk {
				docs = append(docs, candidates[index].Doc)
			}
			partSuffix := ""
			if len(chunks) > 1 {
				partSuffix = fmt.Sprintf(" (batch part %d/%d)", chunkIndex+1, len(chunks))
			}
			reason := ""
			switch group.kind {
			case groupKindConcept:
				reason = "shared concept(s): " + joinSorted(group.evidence) + partSuffix
			case groupKindPattern:
				reason = "shared source pattern(s): " + joinSorted(group.evidence) + partSuffix
			default:
				reason = "no shared concept or source-pattern overlap with other candidates — isolated session"
			}
			sessions = append(sessions, BatchSession{Docs: docs, Reason: reason})
		}
	}

	for index := input.MaxSessionCount; index < len(sessions); index++ {
		for _, doc := range sessions[index].Docs {
			unbatched = append(unbatched, doc)
		}
	}
	keptSessions := sessions
	if len(sessions) > input.MaxSessionCount {
		keptSessions = sessions[:input.MaxSessionCount]
	}

	return &BatchPlan{Sessions: keptSessions, Unbatched: unbatched}, nil
}

/* ------------------------------------------------------------------ helpers */

// findMapping returns the FIRST graph mapping record for doc (the TS
// mappings.find(record => record.doc === doc)).
func findMapping(mappings []graph.MappingRecord, doc string) (graph.MappingRecord, bool) {
	for _, mapping := range mappings {
		if mapping.Doc == doc {
			return mapping, true
		}
	}
	return graph.MappingRecord{}, false
}

// allGrouped reports whether every member index is in the grouped set (TS
// sorted.every(index => grouped.has(index))).
func allGrouped(members []int, grouped map[int]struct{}) bool {
	for _, index := range members {
		if _, ok := grouped[index]; !ok {
			return false
		}
	}
	return true
}

// containsAll reports whether every member of members is present in values (TS
// sorted.every(index => indexes.includes(index))).
func containsAll(members []int, values []int) bool {
	for _, member := range members {
		found := false
		for _, value := range values {
			if value == member {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// sortedSetKeys returns a set's keys in ascending order (TS [...set].sort()).
func sortedSetKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// joinSorted joins an already-sorted evidence list with ", " (TS
// evidence.join(', ')).
func joinSorted(evidence []string) string {
	return strings.Join(evidence, ", ")
}
