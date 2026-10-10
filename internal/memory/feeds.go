package memory

// Ported from packages/core/src/memory/feeds.ts (P10-T21; ARCH §8.8, CTX §25,
// INV-7): the outward projection of parsed memory for the architect/Jev and
// the evaluation pipeline.
//
// INV-7 — memory never mutates policy: ProjectMemoryFeeds is a PURE
// projection. It takes parsed records in and returns plain data out; it has
// no I/O, no write path, no policy handle, and nothing in its return type can
// reach the policy engine's write side. The feed SURFACES rows for (a) Jev's
// routing context, (b) exception candidates for the NEXT policy version, and
// (c) negative examples for eval labeling — policy refinement consumes them
// only through the human-promoted pipeline (models propose; humans promote,
// D14/ARCH §11.4).
//
// The TS module returns Object.freeze'd structures; Go has no equivalent, so
// the projection returns freshly built slices/values that no caller can use
// to reach the store (the round-trip guarantee lives in records.go, and the
// pure-shape contract is asserted in memory_test.go: only string/number data
// comes back, and mutating the result cannot affect the store's records).

// JevContextRow is one known-pattern surfaced as a Jev routing-context row.
type JevContextRow struct {
	Pattern     string
	Observation string
	Confidence  float64
}

// PolicyExceptionCandidate is one exception candidate for the NEXT policy
// version (never auto-applied).
type PolicyExceptionCandidate struct {
	Pattern string
	Action  string
}

// NegativeExample is one negative example for eval-label inputs (R12/R11
// loop, eval side).
type NegativeExample struct {
	// ContradictionOf is the contradicted record id; nil when the failure is
	// not a contradiction.
	ContradictionOf *string
	Observation     string
}

// MemoryFeeds is the feed payload (INV-7: read-only data, no handles).
type MemoryFeeds struct {
	// JevContextRows: from known-patterns, what Jev should see as routing
	// context.
	JevContextRows []JevContextRow
	// PolicyExceptions: from exceptions, candidates for policy refinement,
	// human-promoted.
	PolicyExceptions []PolicyExceptionCandidate
	// NegativeExamples: from failures, negative examples for the evaluation
	// pipeline.
	NegativeExamples []NegativeExample
}

// ProjectMemoryFeeds projects parsed memory into the three feeds. Pure: same
// input, same output; never writes anything (INV-7 — asserted in
// memory_test.go by the pure-shape contract: the result contains only data,
// no functions that could reach policy, and the input records are untouched).
func ProjectMemoryFeeds(parsed ParsedMemory) MemoryFeeds {
	jevContextRows := make([]JevContextRow, 0, len(parsed.KnownPatterns))
	for _, record := range parsed.KnownPatterns {
		jevContextRows = append(jevContextRows, JevContextRow{
			Pattern:     record.PatternName,
			Observation: record.Observation,
			Confidence:  record.Confidence,
		})
	}
	policyExceptions := make([]PolicyExceptionCandidate, 0, len(parsed.Exceptions))
	for _, record := range parsed.Exceptions {
		policyExceptions = append(policyExceptions, PolicyExceptionCandidate{
			Pattern: record.PatternName,
			Action:  record.Action,
		})
	}
	negativeExamples := make([]NegativeExample, 0, len(parsed.Failures))
	for _, record := range parsed.Failures {
		var contradictionOf *string
		if record.ContradictionOf != nil {
			value := *record.ContradictionOf
			contradictionOf = &value
		}
		negativeExamples = append(negativeExamples, NegativeExample{
			ContradictionOf: contradictionOf,
			Observation:     record.Observation,
		})
	}
	return MemoryFeeds{
		JevContextRows:   jevContextRows,
		PolicyExceptions: policyExceptions,
		NegativeExamples: negativeExamples,
	}
}
