// Ported from core/src/audit/store.ts (P11-T20; ARCH §7.2 Loop B; D1/D5).
//
// Append-only background-findings store. Persists validated
// `docsync.drift-finding/v1` records (and their audit run-provenance records)
// as append-only JSONL under `.docsync/state/` — `audit-findings.jsonl` and
// `audit-runs.jsonl`. This is the same deliberate exception to the
// registry-validated StateStore CAS write path as the safety-incident log and
// the Phase-10 feedback sinks: the CAS path is read → validate →
// REWRITE-whole-file, which is incompatible with append-only semantics and
// would lose findings exactly when the failure is in state infrastructure
// itself. Validation is NOT waived: every record is composed against the
// registered schema BEFORE the write (fail closed — malformed evidence is
// refused, never persisted) and re-validated on read (a corrupt line is a hard
// error, never "skip it").
//
// CROSS-RUN DEDUP (this closes the gap flagged against P11-T19, whose dedup
// was within-run only): DedupeAgainstStore applies the SAME pinned rule as the
// within-run dedup (dedup.go) across run boundaries — findings with a claim_id
// dedupe on `claim:<claim_id>`, claimless findings on
// `doc:<doc_path>|kind:<kind>`; latest-wins body, evidence commits/paths
// union. AuditFindingsStore.AppendFindings appends ONLY non-subsumed
// occurrences: an occurrence whose dedup key exists in the store AND whose
// evidence and body are fully subsumed by the stored view is skipped
// (identical re-runs append ZERO duplicate lines); an occurrence that adds
// evidence or a newer body is appended raw, and the read view (ListFindings)
// re-applies the pinned dedup rule across all persisted rows — so evidence
// unions accumulate across runs while every claim stays represented exactly
// once in the view (ARCH §7.2 cost control: nightly runs must not spam).
//
// Fail-closed on write errors: a record that cannot be persisted surfaces as a
// typed AuditFindingsSinkError after the transport's single retry — findings
// are never dropped silently. No wall clock, no id generation here: ids and
// timestamps arrive from the caller (injected clock; never `new Date()`).
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/feedback"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------------- errors */

// EDOCSYNC_AUDIT_FINDINGS_SINK is the machine-readable code of
// AuditFindingsSinkError (TS AuditFindingsSinkError.code).
const EDOCSYNC_AUDIT_FINDINGS_SINK = "EDOCSYNC_AUDIT_FINDINGS_SINK"

// EDOCSYNC_AUDIT_FINDING_INVALID is the machine-readable code of
// AuditFindingValidationError (TS AuditFindingValidationError.code).
const EDOCSYNC_AUDIT_FINDING_INVALID = "EDOCSYNC_AUDIT_FINDING_INVALID"

// AuditFindingsSinkError means a validated record could not be persisted (or
// the store could not be constructed / read) even after the transport's one
// retry — fail loud: an unrecordable finding must surface, never vanish.
type AuditFindingsSinkError struct {
	// Message is the exact TS message text.
	Message string
	// Cause is the underlying failure (TS `cause`), nil when none.
	Cause error

	inner *docutil.Error
}

func newAuditFindingsSinkError(message string, cause error) *AuditFindingsSinkError {
	inner := docutil.New(EDOCSYNC_AUDIT_FINDINGS_SINK, message)
	if cause != nil {
		inner = inner.Wrap(cause)
	}
	return &AuditFindingsSinkError{Message: message, Cause: cause, inner: inner}
}

// Error renders the coded error (`EDOCSYNC_AUDIT_FINDINGS_SINK: <message>`).
func (e *AuditFindingsSinkError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_AUDIT_FINDINGS_SINK.
func (e *AuditFindingsSinkError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is (and the wrapped cause).
func (e *AuditFindingsSinkError) Unwrap() error { return e.inner }

// AuditFindingValidationError is the fail-closed refusal to persist or read a
// record that fails schema validation (TS AuditFindingValidationError).
type AuditFindingValidationError struct {
	// Message is the exact TS message text.
	Message string

	inner *docutil.Error
}

func newAuditFindingValidationError(message string) *AuditFindingValidationError {
	return &AuditFindingValidationError{
		Message: message,
		inner:   docutil.New(EDOCSYNC_AUDIT_FINDING_INVALID, message),
	}
}

// Error renders the coded error.
func (e *AuditFindingValidationError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_AUDIT_FINDING_INVALID.
func (e *AuditFindingValidationError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is.
func (e *AuditFindingValidationError) Unwrap() error { return e.inner }

/* -------------------------------------------------------------- shape moves */

// AUDIT_FINDINGS_FILENAME is the findings JSONL file inside `.docsync/state/`
// (one record per line).
const AUDIT_FINDINGS_FILENAME = "audit-findings.jsonl"

// AUDIT_RUNS_FILENAME is the audit run-provenance JSONL file inside
// `.docsync/state/`.
const AUDIT_RUNS_FILENAME = "audit-runs.jsonl"

// The registered schema ids (the TS strings imported from @docsync/schemas;
// the Go constants are unexported there, so the wire literals are pinned
// here — the registry lookup below fails closed on any drift).
const (
	driftFindingSchemaID   = "docsync.drift-finding/v1"
	driftRunRecordSchemaID = "docsync.drift-audit-run-record/v1"
)

// ToFindingRecord composes a registered `docsync.drift-finding/v1` record
// from a local engine finding (stamps the `schema` tag; fixed key order →
// deterministic line bytes). Pure.
func ToFindingRecord(finding DriftFinding) schemas.DriftFindingRecord {
	return schemas.DriftFindingRecord{
		Schema:     driftFindingSchemaID,
		ID:         finding.ID,
		ClaimID:    finding.ClaimID,
		DocPath:    finding.DocPath,
		Kind:       schemas.DriftFindingKind(finding.Kind),
		Evidence:   toRecordEvidence(finding.Evidence),
		Confidence: finding.Confidence,
		CreatedAt:  finding.CreatedAt,
	}
}

// ToLocalFinding is the inverse of ToFindingRecord: the engine-facing local
// shape.
func ToLocalFinding(record *schemas.DriftFindingRecord) DriftFinding {
	return DriftFinding{
		ID:      record.ID,
		ClaimID: record.ClaimID,
		DocPath: record.DocPath,
		Kind:    string(record.Kind),
		Evidence: DriftFindingEvidence{
			Commits: copyStrings(record.Evidence.Commits),
			Paths:   copyStrings(record.Evidence.Paths),
			Detail:  record.Evidence.Detail,
		},
		Confidence: record.Confidence,
		CreatedAt:  record.CreatedAt,
	}
}

// toRecordEvidence copies the evidence triple into the record shape (arrays
// stay non-nil so the wire form is an array, never null).
func toRecordEvidence(evidence DriftFindingEvidence) schemas.DriftFindingEvidence {
	return schemas.DriftFindingEvidence{
		Commits: copyStrings(evidence.Commits),
		Paths:   copyStrings(evidence.Paths),
		Detail:  evidence.Detail,
	}
}

// copyStrings returns a non-nil copy (the TS spread on an empty array).
func copyStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

// parseRegisteredRecord validates one value against a registered schema and
// returns the typed record (the TS `SCHEMA.safeParse(value)` +
// `result.data`). Wire bytes are the validation surface, so the value is
// marshaled first and parsed strictly (unknown keys rejected).
func parseRegisteredRecord(schemaID string, value any) (any, error) {
	entry := schemas.GetSchema(schemaID)
	if entry == nil {
		return nil, docutil.Newf(docutil.CodeInternal,
			"schema %q is missing from the registry — refusing (fail closed)", schemaID)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return entry.ParseTyped(raw)
}

// parseFindingRecord validates one persisted findings JSONL row (the TS
// parseFindingRecord callback).
func parseFindingRecord(value any, lineNumber int) (*schemas.DriftFindingRecord, error) {
	parsed, err := parseRegisteredRecord("docsync.drift-finding", value)
	if err != nil {
		return nil, newAuditFindingValidationError(fmt.Sprintf(
			"audit-findings line %d failed %s validation — refusing to read (fail closed): %s",
			lineNumber, driftFindingSchemaID, registryMessage(err)))
	}
	record, ok := parsed.(*schemas.DriftFindingRecord)
	if !ok {
		return nil, docutil.Newf(docutil.CodeInternal,
			"registry parse for %q returned %T, want *schemas.DriftFindingRecord",
			"docsync.drift-finding", parsed)
	}
	return record, nil
}

// parseRunRecord validates one persisted run-record JSONL row (the TS
// parseRunRecord callback).
func parseRunRecord(value any, lineNumber int) (*schemas.DriftAuditRunRecord, error) {
	parsed, err := parseRegisteredRecord("docsync.drift-audit-run-record", value)
	if err != nil {
		return nil, newAuditFindingValidationError(fmt.Sprintf(
			"audit-runs line %d failed %s validation — refusing to read (fail closed): %s",
			lineNumber, driftRunRecordSchemaID, registryMessage(err)))
	}
	record, ok := parsed.(*schemas.DriftAuditRunRecord)
	if !ok {
		return nil, docutil.Newf(docutil.CodeInternal,
			"registry parse for %q returned %T, want *schemas.DriftAuditRunRecord",
			"docsync.drift-audit-run-record", parsed)
	}
	return record, nil
}

// registryMessage renders the first zod-equivalent issue of a parse failure
// (TS `result.error.issues[0]?.message ?? 'schema drift'`).
func registryMessage(err error) string {
	var validationErr *schemas.ValidationError
	if errors.As(err, &validationErr) && len(validationErr.Issues) > 0 {
		return validationErr.Issues[0].Message
	}
	if err != nil {
		return err.Error()
	}
	return "schema drift"
}

/* ---------------------------------------------------------- cross-run dedup */

// CrossRunDedupResult is the result of DedupeAgainstStore.
//   - Appendable — occurrences worth persisting (fresh keys, or key-existing
//     occurrences that add evidence / carry a newer body).
//   - Duplicates — key-existing occurrences fully subsumed by the stored view
//     (identical re-runs); nothing is appended for these.
//   - Merged — the canonical deduplicated store view after this batch
//     (pinned rule: claim-id key, latest-wins body, evidence union).
type CrossRunDedupResult struct {
	Appendable []DriftFinding
	Duplicates []DriftFinding
	Merged     []DriftFinding
}

// evidenceSubsumed is the evidence subset check (commits and paths of `next`
// all present in `prior`).
func evidenceSubsumed(prior, next DriftFinding) bool {
	priorCommits := make(map[string]struct{}, len(prior.Evidence.Commits))
	for _, commit := range prior.Evidence.Commits {
		priorCommits[commit] = struct{}{}
	}
	priorPaths := make(map[string]struct{}, len(prior.Evidence.Paths))
	for _, path := range prior.Evidence.Paths {
		priorPaths[path] = struct{}{}
	}
	for _, commit := range next.Evidence.Commits {
		if _, ok := priorCommits[commit]; !ok {
			return false
		}
	}
	for _, path := range next.Evidence.Paths {
		if _, ok := priorPaths[path]; !ok {
			return false
		}
	}
	return true
}

// sameConfidence compares the optional confidences (TS `===`, nil == nil).
func sameConfidence(prior, next *float64) bool {
	if prior == nil || next == nil {
		return prior == nil && next == nil
	}
	return *prior == *next
}

// isSubsumedBy reports whether `next` adds nothing the stored `prior`
// occurrence lacks.
func isSubsumedBy(prior, next DriftFinding) bool {
	return prior.Kind == next.Kind &&
		prior.DocPath == next.DocPath &&
		sameConfidence(prior.Confidence, next.Confidence) &&
		prior.Evidence.Detail == next.Evidence.Detail &&
		evidenceSubsumed(prior, next)
}

// DedupeAgainstStore is the cross-RUN dedup against the persisted store
// (P11-T20; ARCH §7.2 "findings deduplicated by claim ID so nightly runs
// don't spam"). PURE. Same pinned rule as the within-run dedup
// (FindingDedupKey): claim-id key with the `doc_path|kind` fallback; see the
// module doc for the subsumption semantics that decide appendable vs
// duplicate.
func DedupeAgainstStore(newFindings, priorFindings []DriftFinding) CrossRunDedupResult {
	priorView := DedupeFindings(priorFindings)
	priorByKey := make(map[string]DriftFinding, len(priorView))
	for _, prior := range priorView {
		priorByKey[FindingDedupKey(prior)] = prior
	}
	appendable := []DriftFinding{}
	duplicates := []DriftFinding{}
	for _, next := range newFindings {
		prior, ok := priorByKey[FindingDedupKey(next)]
		if ok && isSubsumedBy(prior, next) {
			duplicates = append(duplicates, next)
			continue
		}
		appendable = append(appendable, next)
	}
	merged := DedupeFindings(append(append([]DriftFinding{}, priorFindings...), newFindings...))
	return CrossRunDedupResult{Appendable: appendable, Duplicates: duplicates, Merged: merged}
}

/* -------------------------------------------------------------- run record */

// BuildDriftAuditRunRecordInput is the input of BuildDriftAuditRunRecord —
// everything a run knows.
type BuildDriftAuditRunRecordInput struct {
	// RunID is the caller-assigned run id (deterministic caller; never
	// generated here).
	RunID string
	// At is the ISO-8601 timestamp from the caller's injected clock.
	At string
	// Trigger is what triggered the run (scheduler verdict trigger, or
	// `manual`).
	Trigger schemas.DriftAuditTrigger
	// Position is the repository position the audit ran against (e.g. HEAD
	// sha).
	Position string
	// CursorBefore is the encoded audit-cursor position before the run (nil
	// when none).
	CursorBefore *string
	// Result is the engine's run result.
	Result AuditRunResult
}

// BuildDriftAuditRunRecord composes and validates one
// `docsync.drift-audit-run-record/v1` provenance record from an engine result.
// Fail closed: a record that cannot validate returns an
// AuditFindingValidationError — provenance is never composed loosely and
// persisted anyway.
func BuildDriftAuditRunRecord(input BuildDriftAuditRunRecordInput) (schemas.DriftAuditRunRecord, error) {
	findings := make([]schemas.DriftFindingRecord, 0, len(input.Result.Findings))
	for _, finding := range input.Result.Findings {
		findings = append(findings, ToFindingRecord(finding))
	}
	record := schemas.DriftAuditRunRecord{
		Schema:       driftRunRecordSchemaID,
		RunID:        input.RunID,
		At:           input.At,
		Trigger:      input.Trigger,
		Position:     input.Position,
		CursorBefore: input.CursorBefore,
		CursorAfter:  input.Result.Cursor.Position,
		AuditedDocs:  copyStrings(input.Result.AuditedDocs),
		SkippedDocs:  copyStrings(input.Result.SkippedDocs),
		Status:       schemas.DriftAuditRunStatus(input.Result.Status),
		Detail:       input.Result.Detail,
		Findings:     findings,
	}
	parsed, err := parseRegisteredRecord("docsync.drift-audit-run-record", record)
	if err != nil {
		return schemas.DriftAuditRunRecord{}, newAuditFindingValidationError(fmt.Sprintf(
			"drift-audit-run-record refused (fail closed, nothing written): %s", registryMessage(err)))
	}
	validated, ok := parsed.(*schemas.DriftAuditRunRecord)
	if !ok {
		return schemas.DriftAuditRunRecord{}, docutil.Newf(docutil.CodeInternal,
			"registry parse for %q returned %T, want *schemas.DriftAuditRunRecord",
			"docsync.drift-audit-run-record", parsed)
	}
	return *validated, nil
}

/* -------------------------------------------------------------- the store */

// DEFAULT_FINDINGS_LIST_LIMIT is the default cap on the findings
// ListFindings returns (INV-11 flood safety).
const DEFAULT_FINDINGS_LIST_LIMIT = 500

// ListFindingsOptions are the options for the bounded list reads.
type ListFindingsOptions struct {
	// Limit caps the returned view (the canonical deduplicated view,
	// deterministic order). Nil keeps DEFAULT_FINDINGS_LIST_LIMIT.
	Limit *int
}

// AuditFindingsAppendResult is the result of AuditFindingsStore.AppendFindings.
type AuditFindingsAppendResult struct {
	// Appended are the records actually appended this call (validated,
	// schema-tagged).
	Appended []schemas.DriftFindingRecord
	// Duplicates are the occurrences skipped as fully subsumed duplicates
	// (nothing was written).
	Duplicates []DriftFinding
}

// AuditFindingsStore is the findings/run-record persistence surface (see the
// module doc).
type AuditFindingsStore interface {
	// FindingsFile is the absolute path of the findings JSONL — the ONLY file
	// findings touch.
	FindingsFile() string
	// RunsFile is the absolute path of the run-record JSONL.
	RunsFile() string
	// AppendFindings validates then appends findings, cross-run deduped
	// against the persisted store (see the module doc). Fail closed: the
	// WHOLE batch is validated BEFORE any line is written — one invalid
	// finding appends nothing.
	AppendFindings(findings []DriftFinding) (AuditFindingsAppendResult, error)
	// ListFindings is the bounded deduplicated read view: rows are
	// deduplicated by DedupeFindings (returned sorted by doc_path, kind,
	// claim_id, id) then capped by limit — alphabetically-first dedup keys
	// survive the cap. KNOWN LIMITATION (documented cost-control tradeoff,
	// INV-11-adjacent): cross-run dedup (DedupeAgainstStore) runs against
	// this bounded view, so stores exceeding ~500 distinct dedup keys
	// (DEFAULT_FINDINGS_LIST_LIMIT) may re-append older claims. Within-run
	// and <=500-key behavior is correct and test-pinned.
	ListFindings(options *ListFindingsOptions) ([]DriftFinding, error)
	// AppendRunRecord validates then appends one run-provenance record (fail
	// closed).
	AppendRunRecord(record schemas.DriftAuditRunRecord) (schemas.DriftAuditRunRecord, error)
	// ListRunRecords is the bounded run-record read, newest-last (file order).
	ListRunRecords(options *ListFindingsOptions) ([]schemas.DriftAuditRunRecord, error)
}

// AuditStoreSinks carries the injectable JSONL transports (the TS optional
// `sinks` argument); a nil field keeps the real append-only file sink.
type AuditStoreSinks struct {
	Findings feedback.JsonlSink
	Runs     feedback.JsonlSink
}

// auditFindingsStore is the default AuditFindingsStore implementation over a
// state dir.
type auditFindingsStore struct {
	findingsFile string
	runsFile     string
	findingsSink feedback.JsonlSink
	runsSink     feedback.JsonlSink
}

// CreateAuditFindingsStore creates the append-only findings/run-record store
// over `<stateDir>` (`.docsync/state` in production). Sinks are injectable for
// tests; the defaults are the shared append-only JSONL transport
// (feedback-sink pattern: single retry on append, fail-closed reads).
func CreateAuditFindingsStore(stateDir string, sinks *AuditStoreSinks) (AuditFindingsStore, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return nil, newAuditFindingsSinkError(fmt.Sprintf(
			"createAuditFindingsStore: stateDir must be a nonempty absolute path (got %s)",
			jsonQuote(truncateForMessage(stateDir, 64))), nil)
	}
	findingsSink := feedback.JsonlSink(nil)
	runsSink := feedback.JsonlSink(nil)
	if sinks != nil {
		findingsSink = sinks.Findings
		runsSink = sinks.Runs
	}
	if findingsSink == nil {
		created, err := feedback.CreateJsonlSink(stateDir, AUDIT_FINDINGS_FILENAME)
		if err != nil {
			return nil, newAuditFindingsSinkError(
				"audit findings store: findings sink could not be created: "+err.Error(), err)
		}
		findingsSink = created
	}
	if runsSink == nil {
		created, err := feedback.CreateJsonlSink(stateDir, AUDIT_RUNS_FILENAME)
		if err != nil {
			return nil, newAuditFindingsSinkError(
				"audit findings store: runs sink could not be created: "+err.Error(), err)
		}
		runsSink = created
	}
	return &auditFindingsStore{
		findingsFile: filepath.Join(stateDir, AUDIT_FINDINGS_FILENAME),
		runsFile:     filepath.Join(stateDir, AUDIT_RUNS_FILENAME),
		findingsSink: findingsSink,
		runsSink:     runsSink,
	}, nil
}

// FindingsFile returns the absolute findings JSONL path.
func (s *auditFindingsStore) FindingsFile() string { return s.findingsFile }

// RunsFile returns the absolute run-record JSONL path.
func (s *auditFindingsStore) RunsFile() string { return s.runsFile }

// listFindingsLocal is the bounded, deduplicated read view.
func (s *auditFindingsStore) listFindingsLocal(options *ListFindingsOptions) ([]DriftFinding, error) {
	rows, err := feedback.ReadAllRows(s.findingsSink, parseFindingRecord)
	if err != nil {
		var validationErr *AuditFindingValidationError
		if errors.As(err, &validationErr) {
			return nil, validationErr
		}
		// Transport-level read failure (unreadable file, non-JSON line):
		// typed and loud — fail closed, never "skip the unreadable row".
		return nil, newAuditFindingsSinkError(fmt.Sprintf(
			"audit findings store: %q could not be read: %s", AUDIT_FINDINGS_FILENAME, err), err)
	}
	local := make([]DriftFinding, 0, len(rows))
	for _, row := range rows {
		local = append(local, ToLocalFinding(row))
	}
	view := DedupeFindings(local)
	limit := DEFAULT_FINDINGS_LIST_LIMIT
	if options != nil && options.Limit != nil {
		limit = *options.Limit
	}
	if limit < 0 {
		limit = 0
	}
	if limit > len(view) {
		limit = len(view)
	}
	return view[:limit], nil
}

// AppendFindings validates the whole batch before any write, then appends only
// the cross-run non-subsumed occurrences.
func (s *auditFindingsStore) AppendFindings(findings []DriftFinding) (AuditFindingsAppendResult, error) {
	// Validate the WHOLE batch before ANY write (fail closed — nothing
	// partially persisted).
	validated := make([]DriftFinding, 0, len(findings))
	for index, finding := range findings {
		record := ToFindingRecord(finding)
		parsed, err := parseRegisteredRecord("docsync.drift-finding", record)
		if err != nil {
			id := finding.ID
			if id == "" {
				id = "<no id>"
			}
			return AuditFindingsAppendResult{}, newAuditFindingValidationError(fmt.Sprintf(
				"finding index %d (%s) refused by %s (fail closed, nothing written): %s",
				index, id, driftFindingSchemaID, registryMessage(err)))
		}
		record2, ok := parsed.(*schemas.DriftFindingRecord)
		if !ok {
			return AuditFindingsAppendResult{}, docutil.Newf(docutil.CodeInternal,
				"registry parse for %q returned %T, want *schemas.DriftFindingRecord",
				"docsync.drift-finding", parsed)
		}
		validated = append(validated, ToLocalFinding(record2))
	}
	prior, err := s.listFindingsLocal(nil)
	if err != nil {
		var validationErr *AuditFindingValidationError
		if errors.As(err, &validationErr) {
			return AuditFindingsAppendResult{}, validationErr
		}
		return AuditFindingsAppendResult{}, newAuditFindingsSinkError(
			"audit findings store: prior findings could not be read before append: "+err.Error(), err)
	}
	dedup := DedupeAgainstStore(validated, prior)
	appended := make([]schemas.DriftFindingRecord, 0, len(dedup.Appendable))
	for _, finding := range dedup.Appendable {
		record := ToFindingRecord(finding)
		if err := s.findingsSink.Append(record); err != nil {
			return AuditFindingsAppendResult{}, newAuditFindingsSinkError(fmt.Sprintf(
				"audit findings store: record could not be appended to %q after one retry: %s",
				AUDIT_FINDINGS_FILENAME, err), err)
		}
		appended = append(appended, record)
	}
	return AuditFindingsAppendResult{Appended: appended, Duplicates: dedup.Duplicates}, nil
}

// ListFindings is the bounded deduplicated read view.
func (s *auditFindingsStore) ListFindings(options *ListFindingsOptions) ([]DriftFinding, error) {
	return s.listFindingsLocal(options)
}

// AppendRunRecord validates then appends one run-provenance record (fail
// closed).
func (s *auditFindingsStore) AppendRunRecord(record schemas.DriftAuditRunRecord) (schemas.DriftAuditRunRecord, error) {
	parsed, err := parseRegisteredRecord("docsync.drift-audit-run-record", record)
	if err != nil {
		return schemas.DriftAuditRunRecord{}, newAuditFindingValidationError(fmt.Sprintf(
			"drift-audit-run-record refused (fail closed, nothing written): %s", registryMessage(err)))
	}
	validated, ok := parsed.(*schemas.DriftAuditRunRecord)
	if !ok {
		return schemas.DriftAuditRunRecord{}, docutil.Newf(docutil.CodeInternal,
			"registry parse for %q returned %T, want *schemas.DriftAuditRunRecord",
			"docsync.drift-audit-run-record", parsed)
	}
	if err := s.runsSink.Append(*validated); err != nil {
		return schemas.DriftAuditRunRecord{}, newAuditFindingsSinkError(fmt.Sprintf(
			"audit findings store: record could not be appended to %q after one retry: %s",
			AUDIT_RUNS_FILENAME, err), err)
	}
	return *validated, nil
}

// ListRunRecords is the bounded run-record read, newest-last (file order).
func (s *auditFindingsStore) ListRunRecords(options *ListFindingsOptions) ([]schemas.DriftAuditRunRecord, error) {
	rows, err := feedback.ReadAllRows(s.runsSink, parseRunRecord)
	if err != nil {
		var validationErr *AuditFindingValidationError
		if errors.As(err, &validationErr) {
			return nil, validationErr
		}
		return nil, newAuditFindingsSinkError(fmt.Sprintf(
			"audit findings store: %q could not be read: %s", AUDIT_RUNS_FILENAME, err), err)
	}
	limit := DEFAULT_FINDINGS_LIST_LIMIT
	if options != nil && options.Limit != nil {
		limit = *options.Limit
	}
	if limit < 0 {
		limit = 0
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	tail := rows[len(rows)-limit:]
	out := make([]schemas.DriftAuditRunRecord, 0, len(tail))
	for _, row := range tail {
		out = append(out, *row)
	}
	return out, nil
}

/* ----------------------------------------------------------------- helpers */

// jsonQuote mirrors JSON.stringify for the message excerpt (ASCII-safe
// paths).
func jsonQuote(value string) string {
	return strconv.Quote(value)
}

// truncateForMessage ports the TS `String(x).slice(0, 64)` message excerpt
// (rune-based, as the TS string slice is).
func truncateForMessage(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
