// Ported from packages/schemas/src/v1/memory-record.ts: memory record
// (P10-T01; ARCH §8.8, CTX §25, D1) — evidence-backed entries in the EXACTLY
// FOUR .docsync/memory/ files.
//
// Memory is PRUNABLE — a contradiction lands in failures.md and the
// pattern's confidence decays (R12: memory cannot ossify wrong beliefs); an
// unverifiable entry is stored with confidence NEAR THE FLOOR (an anecdote
// is a hypothesis, not a fact). record_id is a ULID, created_at is
// emitter-supplied (never a wall-clock read here).
package schemas

import "github.com/DUDE616/docSync-go/internal/docutil"

// MemoryRecordSchemaID is the literal schema id (MEMORY_RECORD_SCHEMA_ID).
const MemoryRecordSchemaID = "docsync.memory-record/v1"

// MemoryFile is one of the D1 four-file layout — exactly these, nothing else.
type MemoryFile string

// The four memory files in canonical order.
const (
	MemoryFileDecisions     MemoryFile = "decisions"
	MemoryFileKnownPatterns MemoryFile = "known-patterns"
	MemoryFileExceptions    MemoryFile = "exceptions"
	MemoryFileFailures      MemoryFile = "failures"
)

// MemoryFileValues is the D1 four-file layout — exactly these, nothing else
// (MEMORY_FILE_VALUES).
var MemoryFileValues = []MemoryFile{
	MemoryFileDecisions, MemoryFileKnownPatterns, MemoryFileExceptions, MemoryFileFailures,
}

// IsValidMemoryFile reports membership.
func IsValidMemoryFile(f string) bool {
	for _, v := range MemoryFileValues {
		if string(v) == f {
			return true
		}
	}
	return false
}

// MemoryRecord is one evidence-backed memory entry (MEMORY_RECORD_SCHEMA).
type MemoryRecord struct {
	Schema          string     `json:"schema" yaml:"schema"`
	RecordID        string     `json:"record_id" yaml:"record_id"`
	File            MemoryFile `json:"file" yaml:"file"`
	PatternName     string     `json:"pattern_name" yaml:"pattern_name"`
	EvidencePRs     []string   `json:"evidence_prs" yaml:"evidence_prs"`
	Observation     string     `json:"observation" yaml:"observation"`
	Action          string     `json:"action" yaml:"action"`
	Confidence      float64    `json:"confidence" yaml:"confidence"`
	Unverifiable    bool       `json:"unverifiable" yaml:"unverifiable"`
	ContradictionOf *string    `json:"contradiction_of,omitempty" yaml:"contradiction_of,omitempty"`
	CreatedAt       string     `json:"created_at" yaml:"created_at"`
}

// memoryRecordAllowedRootKeys is the strict root key set.
var memoryRecordAllowedRootKeys = []string{
	"schema", "record_id", "file", "pattern_name", "evidence_prs", "observation",
	"action", "confidence", "unverifiable", "contradiction_of", "created_at",
}

// Validate checks the record, including the near-floor confidence
// refinement: an unverifiable entry must be stored with confidence <= 0.3
// (ARCH §8.8, R12).
func (r *MemoryRecord) Validate() []Issue {
	var issues []Issue
	if r.Schema != MemoryRecordSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: "Invalid literal value, expected \"docsync.memory-record/v1\""})
	}
	if !docutil.IsValidULID(r.RecordID) {
		Collect(&issues, &Issue{Path: []string{"record_id"}, Message: "Invalid"})
	}
	if !IsValidMemoryFile(string(r.File)) {
		Collect(&issues, &Issue{
			Path: []string{"file"},
			Message: "Invalid enum value. Expected 'decisions' | 'known-patterns' | 'exceptions' | 'failures', " +
				"received '" + string(r.File) + "'",
		})
	}
	Collect(&issues, RequiredString(r.PatternName, 1, []string{"pattern_name"}))
	if len(r.EvidencePRs) < 1 {
		Collect(&issues, &Issue{Path: []string{"evidence_prs"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i := range r.EvidencePRs {
		Collect(&issues, RequiredString(r.EvidencePRs[i], 1, []string{"evidence_prs", itoa(i)}))
	}
	Collect(&issues, RequiredString(r.Observation, 1, []string{"observation"}))
	Collect(&issues, RequiredString(r.Action, 1, []string{"action"}))
	if r.Confidence < 0 {
		Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.Confidence > 1 {
		Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be less than or equal to 1"})
	}
	if r.Unverifiable && r.Confidence > 0.3 {
		Collect(&issues, &Issue{
			Message: "an unverifiable entry must be stored with confidence near the floor (<= 0.3) — an anecdote is a hypothesis, not a fact (ARCH §8.8, R12)",
		})
	}
	if r.ContradictionOf != nil && !docutil.IsValidULID(*r.ContradictionOf) {
		Collect(&issues, &Issue{Path: []string{"contradiction_of"}, Message: "Invalid"})
	}
	if !isZuluDateTime(r.CreatedAt) {
		Collect(&issues, &Issue{Path: []string{"created_at"}, Message: "Invalid datetime"})
	}
	return issues
}

// ParseMemoryRecord strictly parses and validates YAML/JSON memory bytes.
// Unknown keys are rejected at the root (zod strictObject semantics).
func ParseMemoryRecord(data []byte) (*MemoryRecord, error) {
	var r MemoryRecord
	if isJSONish(data) {
		if err := decodeJSONStrict(data, &r); err != nil {
			return nil, err
		}
	} else {
		if err := decodeStrictYAMLOrJSON(data, &r, memoryRecordAllowedRootKeys); err != nil {
			return nil, err
		}
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// MemoryRecordExample returns the canonical example record
// (MEMORY_RECORD_EXAMPLE).
func MemoryRecordExample() MemoryRecord {
	return MemoryRecord{
		Schema:       MemoryRecordSchemaID,
		RecordID:     "01JMEMCRY1EXAMP1E000000000",
		File:         MemoryFileKnownPatterns,
		PatternName:  "generated-sdk-docs-follow-api-changes",
		EvidencePRs:  []string{"acme/widgets#481", "acme/widgets#502", "acme/widgets#519"},
		Observation:  "generated SDK docs drift whenever the API client changes",
		Action:       "route generated-sdk/** doc claims through the generated-docs lane",
		Confidence:   0.72,
		Unverifiable: false,
		CreatedAt:    "2026-10-08T12:00:00.000Z",
	}
}
