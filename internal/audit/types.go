// Ported from core/src/audit/types.ts (P11-T19; ARCH §7.2 Loop B — Background
// drift audit, §6.3 audit-cursor).
//
// The audit engine is READ-MOSTLY orchestration: it computes drift findings
// over repository state and emits them as DATA — it performs NO document
// writes and NO patch application ever (ARCH §7.2: findings surface as
// advisory artifacts, never auto-writes).
//
// `DriftFinding` is defined LOCALLY here (plain struct + local fail-closed
// validation, validated on every port result): the concrete drift predicates
// arrive from the drift module, and the findings store is a W5b2 surface. The
// local shape is deliberately compatible in spirit with the registered
// `docsync.drift-finding/v1` record's `doc_path`/`kind`/`detail` triple —
// ARCH §7.2 evidence-carrying findings: claim IDs, commits, confidence.
//
// Ports are injectable by design: the engine owns chunking, the audit-cursor,
// dedup, idempotency and trigger discipline; the manifest doc list, the
// current audit position and the per-document drift predicates are wired by
// the caller (W5b2 wiring wave).
package audit

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

/* --------------------------------------------------------------- findings */

// DriftFinding is one evidence-carrying drift finding (ARCH §7.2: claim IDs,
// commits, confidence; advisory only — this record is DATA, never a write
// instruction).
type DriftFinding struct {
	// ID is the stable finding id (port-owned; deterministic ports pin it).
	ID string `json:"id"`
	// ClaimID is the dedup identity (ARCH §7.2: findings deduplicated by
	// claim ID).
	ClaimID *string `json:"claim_id,omitempty"`
	// DocPath is the manifest-scoped document the finding is about (D7: no
	// blanket scanning).
	DocPath string `json:"doc_path"`
	// Kind is the stable kebab-case drift-kind tag (free string at the
	// schema level; the closed vocabulary is enforced by the registered
	// record schema in the store).
	Kind string `json:"kind"`
	// Evidence is the evidence triple every finding carries.
	Evidence DriftFindingEvidence `json:"evidence"`
	// Confidence is the optional 0..1 confidence (caller-calibrated; never
	// hard-coded thresholds).
	Confidence *float64 `json:"confidence,omitempty"`
	// CreatedAt is the ISO-8601 creation timestamp (caller-owned clock; no
	// hidden clock).
	CreatedAt string `json:"created_at"`
}

// DriftFindingEvidence is the finding's evidence block (commits, paths,
// detail).
type DriftFindingEvidence struct {
	Commits []string `json:"commits"`
	Paths   []string `json:"paths"`
	Detail  string   `json:"detail"`
}

// ValidateFinding is the local fail-closed validation for port-produced
// findings (TS DRIFT_FINDING_SCHEMA — the module-LOCAL schema of types.ts,
// NOT the registered record schema): a drift predicate returning schema drift
// is a CHUNK FAILURE, never a silently-dropped record.
//
// The zod messages are reproduced via the schemas issue helpers
// (`String must contain at least 1 character(s)`, `Invalid datetime`, the
// numeric bounds). Because a Go port returns structs rather than parsed JSON,
// an ABSENT field is indistinguishable from an empty one, so absent strings
// report the min-1 message rather than zod's `Required` — the only observable
// difference of this port, and it is confined to failure messages.
func ValidateFinding(finding DriftFinding) []schemas.Issue {
	var issues []schemas.Issue
	schemas.Collect(&issues, schemas.RequiredString(finding.ID, 1, []string{"id"}))
	if finding.ClaimID != nil {
		schemas.Collect(&issues, schemas.RequiredString(*finding.ClaimID, 1, []string{"claim_id"}))
	}
	schemas.Collect(&issues, schemas.RequiredString(finding.DocPath, 1, []string{"doc_path"}))
	schemas.Collect(&issues, schemas.RequiredString(finding.Kind, 1, []string{"kind"}))
	for i, commit := range finding.Evidence.Commits {
		schemas.Collect(&issues, schemas.RequiredString(commit, 1, []string{"evidence", "commits", itoa(i)}))
	}
	for i, path := range finding.Evidence.Paths {
		schemas.Collect(&issues, schemas.RequiredString(path, 1, []string{"evidence", "paths", itoa(i)}))
	}
	schemas.Collect(&issues, schemas.RequiredString(finding.Evidence.Detail, 1, []string{"evidence", "detail"}))
	if finding.Confidence != nil {
		if *finding.Confidence < 0 {
			schemas.Collect(&issues, &schemas.Issue{
				Path:    []string{"confidence"},
				Message: "Number must be greater than or equal to 0"})
		}
		if *finding.Confidence > 1 {
			schemas.Collect(&issues, &schemas.Issue{
				Path:    []string{"confidence"},
				Message: "Number must be less than or equal to 1"})
		}
	}
	schemas.Collect(&issues, datetimeIssue(finding.CreatedAt, []string{"created_at"}))
	return issues
}

// firstIssueMessage renders the first validation issue's message (TS
// `result.error.issues[0]?.message ?? 'schema drift'`).
func firstIssueMessage(issues []schemas.Issue) string {
	if len(issues) == 0 {
		return "schema drift"
	}
	return issues[0].Message
}

// datetimeIssue is the local port of the schemas package's unexported
// helper (z.string().datetime() with the registry's RFC-3339 UTC acceptance
// rule): the same "Invalid datetime" message, so error text stays identical.
func datetimeIssue(value string, path []string) *schemas.Issue {
	if !isRFC3339UTCDatetime(value) {
		return &schemas.Issue{Path: path, Message: "Invalid datetime"}
	}
	return nil
}

// isRFC3339UTCDatetime mirrors schemas' internal acceptance rule (an explicit
// UTC 'Z' timestamp, optional fractional seconds).
func isRFC3339UTCDatetime(value string) bool {
	if len(value) < 20 || !strings.HasSuffix(value, "Z") {
		return false
	}
	body := value[:len(value)-1]
	if dot := strings.IndexByte(body, '.'); dot >= 0 {
		frac := body[dot+1:]
		if frac == "" {
			return false
		}
		for i := 0; i < len(frac); i++ {
			if frac[i] < '0' || frac[i] > '9' {
				return false
			}
		}
		body = body[:dot]
	}
	if len(body) != len("2006-01-02T15:04:05") {
		return false
	}
	parsed, err := time.Parse("2006-01-02T15:04:05", body)
	if err != nil {
		return false
	}
	return parsed.Format("2006-01-02T15:04:05") == body
}

// itoa is the decimal rendering used in issue paths (zod array indices).
func itoa(i int) string {
	return strconv.Itoa(i)
}

/* ------------------------------------------------------------------ ports */

// AuditDocPort is the per-document drift predicate port (P11-T19): audits ONE
// manifest document against the given previous audit cursor (the "since"
// position) and returns its findings. The concrete predicates arrive from the
// drift module — the engine is the orchestration around this port. A returned
// error is a CHUNK FAILURE (typed partial result), never a silent skip.
type AuditDocPort func(ctx context.Context, docPath string, cursor *schemas.AuditCursor) ([]DriftFinding, error)

// AuditCursorPort is the audit-cursor port (ARCH §6.3 `state/audit-cursor`):
// exactly the StateStore surface for the registered `docsync.audit-cursor`
// document — CAS-guarded reads and writes (a stale expectedHash loses and the
// caller re-runs, never blind-writes, R15). Injectable so fakes and
// concurrent-writer wrappers can be substituted in tests.
type AuditCursorPort interface {
	ReadAuditCursor() (state.StateRead[*schemas.AuditCursor], error)
	WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error)
}

// AuditManifestPort is the manifest-scope port (D7): the allowlisted document
// paths to audit — NOTHING else.
type AuditManifestPort func(ctx context.Context) ([]string, error)

// AuditPositionPort is the current audit position port: where the repository
// stands now (e.g. HEAD sha).
type AuditPositionPort func(ctx context.Context) (string, error)

// IncrementalAuditDeps is the injectable engine dependencies (see the package
// doc: wiring is a W5b2 surface).
type IncrementalAuditDeps struct {
	Cursor       AuditCursorPort
	ManifestDocs AuditManifestPort
	Position     AuditPositionPort
	AuditDoc     AuditDocPort
}

/* ----------------------------------------------------------------- results */

// AuditRunStatus is the closed run-status vocabulary (TS AuditRunStatus).
type AuditRunStatus string

// The run statuses in canonical order.
const (
	// AuditRunCompleted — every chunk finished and the cursor advanced fully.
	AuditRunCompleted AuditRunStatus = "completed"
	// AuditRunPartial — a chunk failed (port error or schema-drift finding);
	// the cursor persisted only the fully-completed chunk prefix
	// (crash-safe: the unfinished chunk is re-audited on the next run,
	// idempotent via claim-ID dedup). ARCH §7.2 read-mostly: findings are
	// data.
	AuditRunPartial AuditRunStatus = "partial"
	// AuditRunReRunRequired — the cursor CAS write lost to a concurrent
	// writer (or the write failed): the loser re-runs, never blind-writes
	// (R15, ARCH §6.3).
	AuditRunReRunRequired AuditRunStatus = "re-run-required"
	// AuditRunAbstained — fail-closed degradation (cursor read / manifest /
	// position port failure): a typed report with detail, never a silent
	// skip.
	AuditRunAbstained AuditRunStatus = "abstained"
)

// AuditRunStatusValues lists every run status in canonical order.
var AuditRunStatusValues = []AuditRunStatus{
	AuditRunCompleted, AuditRunPartial, AuditRunReRunRequired, AuditRunAbstained,
}

// IsValidAuditRunStatus reports membership in the run-status vocabulary.
func IsValidAuditRunStatus(s string) bool {
	for _, v := range AuditRunStatusValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// AuditCursorState is the run's cursor outcome.
type AuditCursorState struct {
	// Advanced is true when the cursor position advanced during this run.
	Advanced bool
	// Position is the cursor position after the run (nil when never
	// advanced).
	Position *string
}

// AuditRunResult is one incremental audit run's outcome.
type AuditRunResult struct {
	Status AuditRunStatus
	// Findings are the deduplicated findings gathered during the run
	// (advisory data).
	Findings []DriftFinding
	// AuditedDocs are the manifest docs whose port completed successfully
	// this run (sorted).
	AuditedDocs []string
	// SkippedDocs are the manifest docs skipped because the resumed cursor
	// already covered them.
	SkippedDocs []string
	// Cursor is the run's cursor outcome.
	Cursor AuditCursorState
	// Detail is the human-readable typed detail for
	// partial/re-run-required/abstained runs.
	Detail *string
}

// AuditRunOptions configures one engine run.
type AuditRunOptions struct {
	// Now is the ISO-8601 timestamp stamped into every cursor write
	// (caller-owned clock).
	Now string
	// Concurrency is the bounded in-process pool cap (P5-T12 parity; clamped
	// to >= 1). Nil keeps the default of 4.
	Concurrency *int
}

// DefaultAuditConcurrency is the engine's default worker-pool cap
// (TS `Math.max(1, ... : 4)`).
const DefaultAuditConcurrency = 4

// resolveConcurrency ports the TS clamp: `Math.max(1, Number.isFinite(cap) ?
// Math.floor(cap) : 4)` — a nil/absurd cap falls back to the default, and the
// result is always >= 1.
func resolveConcurrency(requested *int) int {
	if requested == nil {
		return DefaultAuditConcurrency
	}
	if *requested < 1 {
		return 1
	}
	return *requested
}
