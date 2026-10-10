// Ported from packages/schemas/src/v1/merge-ledger.ts: merge-order ledger —
// `docsync.merge-ledger/v1` (P6-T18).
//
// Per-source-key record of doc-PR lifecycle states so the Phase 6 PR write
// path can order merges, supersede stale doc PRs and stay idempotent under
// concurrency (no-flood invariant 11): one ledger per `source_key` (the
// source PR number or run id that produced the doc PRs), append-updated
// entries, `updated_at` stamped by the emitter — never generated here.
//
// Fail-closed refinement: `doc_pr_number` is unique within the ledger — two
// entries claiming the same doc PR would make supersede/merge ordering
// ambiguous. The state union is closed, every timestamp is a field supplied
// by the emitter (no wall-clock reads anywhere).
package schemas

// MergeLedgerState is the frozen doc-PR lifecycle union.
type MergeLedgerState string

// The five lifecycle states in canonical order.
const (
	MergeLedgerStateOpen       MergeLedgerState = "open"
	MergeLedgerStateSuperseded MergeLedgerState = "superseded"
	MergeLedgerStateMerged     MergeLedgerState = "merged"
	MergeLedgerStateConflicted MergeLedgerState = "conflicted"
	MergeLedgerStateDropped    MergeLedgerState = "dropped"
)

// MergeLedgerStateValues is the frozen five-state lifecycle union
// (MERGE_LEDGER_STATE_VALUES).
var MergeLedgerStateValues = []MergeLedgerState{
	MergeLedgerStateOpen, MergeLedgerStateSuperseded, MergeLedgerStateMerged,
	MergeLedgerStateConflicted, MergeLedgerStateDropped,
}

// IsValidMergeLedgerState reports membership (MERGE_LEDGER_STATE).
func IsValidMergeLedgerState(s string) bool {
	for _, v := range MergeLedgerStateValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// LedgerEntry is one doc-PR lifecycle record (LEDGER_ENTRY_SCHEMA).
type LedgerEntry struct {
	DocPRNumber    int              `json:"doc_pr_number" yaml:"doc_pr_number"`
	State          MergeLedgerState `json:"state" yaml:"state"`
	IdempotencyKey string           `json:"idempotency_key" yaml:"idempotency_key"`
	At             string           `json:"at" yaml:"at"`
	Detail         string           `json:"detail" yaml:"detail"`
}

// Validate checks the entry against the schema (strict).
func (e *LedgerEntry) Validate() []Issue {
	var issues []Issue
	if e.DocPRNumber < 1 {
		Collect(&issues, &Issue{Path: []string{"doc_pr_number"}, Message: "Number must be greater than 0"})
	}
	if !IsValidMergeLedgerState(string(e.State)) {
		Collect(&issues, &Issue{
			Path: []string{"state"},
			Message: "Invalid enum value. Expected 'open' | 'superseded' | 'merged' | 'conflicted' | 'dropped', " +
				"received '" + string(e.State) + "'",
		})
	}
	Collect(&issues, RequiredString(e.IdempotencyKey, 1, []string{"idempotency_key"}))
	if !isOffsetDateTime(e.At) {
		Collect(&issues, &Issue{Path: []string{"at"}, Message: "Invalid datetime"})
	}
	Collect(&issues, RequiredString(e.Detail, 1, []string{"detail"}))
	return issues
}

// MergeLedger is the per-source-key ledger (MERGE_LEDGER_SCHEMA).
type MergeLedger struct {
	Schema    string        `json:"schema" yaml:"schema"`
	SourceKey string        `json:"source_key" yaml:"source_key"`
	UpdatedAt string        `json:"updated_at" yaml:"updated_at"`
	Entries   []LedgerEntry `json:"entries" yaml:"entries"`
}

// mergeLedgerAllowedRootKeys is the strict root key set.
var mergeLedgerAllowedRootKeys = []string{"schema", "source_key", "updated_at", "entries"}

// Validate checks the ledger, including the fail-closed uniqueness
// refinement: doc_pr_number is unique within the ledger — a repeated number
// would make supersede/merge ordering ambiguous.
func (l *MergeLedger) Validate() []Issue {
	var issues []Issue
	if l.Schema != "docsync.merge-ledger/v1" {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: "Invalid literal value, expected \"docsync.merge-ledger/v1\""})
	}
	Collect(&issues, RequiredString(l.SourceKey, 1, []string{"source_key"}))
	if !isOffsetDateTime(l.UpdatedAt) {
		Collect(&issues, &Issue{Path: []string{"updated_at"}, Message: "Invalid datetime"})
	}
	seen := map[int]bool{}
	for index := range l.Entries {
		for _, iss := range l.Entries[index].Validate() {
			iss.Path = append([]string{"entries", itoa(index)}, iss.Path...)
			issues = append(issues, iss)
		}
		if seen[l.Entries[index].DocPRNumber] {
			Collect(&issues, &Issue{
				Path: []string{"entries", itoa(index)},
				Message: "doc_pr_number " + itoa(l.Entries[index].DocPRNumber) +
					" appears more than once in the ledger (merge-order ambiguity)",
			})
		}
		seen[l.Entries[index].DocPRNumber] = true
	}
	return issues
}

// ParseMergeLedger strictly parses and validates YAML/JSON ledger bytes.
// Unknown keys are rejected at the root (zod strictObject semantics).
func ParseMergeLedger(data []byte) (*MergeLedger, error) {
	var l MergeLedger
	if isJSONish(data) {
		if err := decodeJSONStrict(data, &l); err != nil {
			return nil, err
		}
	} else {
		if err := decodeStrictYAMLOrJSON(data, &l, mergeLedgerAllowedRootKeys); err != nil {
			return nil, err
		}
	}
	if issues := l.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &l, nil
}

// MergeLedgerExample returns the canonical example ledger
// (MERGE_LEDGER_EXAMPLE).
func MergeLedgerExample() MergeLedger {
	return MergeLedger{
		Schema:    "docsync.merge-ledger/v1",
		SourceKey: "source-pr-42",
		UpdatedAt: "2026-10-05T09:20:00Z",
		Entries: []LedgerEntry{
			{
				DocPRNumber:    84,
				State:          MergeLedgerStateMerged,
				IdempotencyKey: "docsync-acme-payments-84",
				At:             "2026-10-05T09:18:41Z",
				Detail:         "merged as the freshest proposal for docs/payments.md",
			},
			{
				DocPRNumber:    83,
				State:          MergeLedgerStateSuperseded,
				IdempotencyKey: "docsync-acme-payments-83",
				At:             "2026-10-05T09:17:02Z",
				Detail:         "superseded by #84 before merge (same doc, newer run)",
			},
		},
	}
}
