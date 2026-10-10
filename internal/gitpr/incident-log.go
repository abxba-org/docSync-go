package gitpr

// Ported from core/src/gitpr/incident-log.ts (P6-T12; PLAN §7.3 "PR-side
// safety"; ARCH §11.4): safety-incident log.
//
// Out-of-allowlist or lock-violating changes abort PR creation and are recorded
// as SAFETY INCIDENTS — an append-only, alert-worthy bug class that is NEVER
// silently swallowed (PLAN §7.3; the alerting surface itself lands in Phase 9).
// Three pieces:
//
//  1. IncidentSink — the port: one Append method. The only side effect surface;
//     tests inject in-memory doubles, production uses FileIncidentSink.
//  2. FileIncidentSink — an append-only JSONL sink at
//     `<stateDir>/incidents.jsonl` (i.e. `.docsync/state/incidents.jsonl`),
//     one validated `docsync.safety-incident/v1` record per line, created on
//     first write. DELIBERATE EXCEPTION to the registry-validated store: the
//     internal/state CAS write path is read → validate → REWRITE-whole-file,
//     which is incompatible with append-only file semantics AND loses the
//     incident exactly when the thing that broke is the registry/store itself
//     (schema drift, corrupt state) — incidents must survive that failure.
//     Validation is NOT waived: every record is parsed against
//     `docsync.safety-incident/v1` BEFORE the write (fail closed, refuse to
//     persist malformed evidence); the exception is the persistence mechanism
//     only. The state directory is created on first write (idempotent mkdir);
//     the file itself is only ever opened for append — never truncated, never
//     rewritten. A retried append MAY duplicate a line (evidence duplication,
//     never loss; `incident_id` makes duplicates detectable downstream).
//  3. CreateIncidentLog / RecordIncident — the record builder: composes the
//     incident, assigns the deterministic `incident_id`
//     (`inc-<first 16 hex of sha256 over the canonical incident minus the
//     id>`), validates via the schema (malformed input REFUSES to write — fail
//     closed) and appends. A sink failure fails LOUD: exactly one retry, then a
//     typed IncidentSinkError — an incident that cannot be recorded is itself
//     an incident and is never dropped on the floor.
//
// No wall clock: `at` is an input field supplied by the caller's injected clock
// (no `new Date()` anywhere; P19).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// EDOCSYNCIncidentInvalid preserves the TS static
// `IncidentValidationError.code` (EDOCSYNC_INCIDENT_INVALID), which
// internal/docutil does not list. Per CONVENTIONS.md §Errors the code string is
// preserved on the Go error type instead of inventing a new docutil code.
const EDOCSYNCIncidentInvalid = "EDOCSYNC_INCIDENT_INVALID"

// EDOCSYNCIncidentSink preserves the TS static `IncidentSinkError.code`
// (EDOCSYNC_INCIDENT_SINK) for the same reason.
const EDOCSYNCIncidentSink = "EDOCSYNC_INCIDENT_SINK"

// IncidentValidationError is the fail-closed refusal to record a malformed
// incident (nothing was written).
type IncidentValidationError struct {
	Msg string
}

// Error reproduces the TS message text.
func (e *IncidentValidationError) Error() string { return e.Msg }

// Code exposes the machine-readable failure class (TS: static/instance `code`).
func (e *IncidentValidationError) Code() string { return EDOCSYNCIncidentInvalid }

// IncidentSinkError reports a sink that could not persist a validated incident
// even after one retry — fail loud: an unrecordable safety incident must
// surface, never vanish.
type IncidentSinkError struct {
	Msg   string
	Cause error
}

// Error reproduces the TS message text.
func (e *IncidentSinkError) Error() string { return e.Msg }

// Code exposes the machine-readable failure class (TS: static/instance `code`).
func (e *IncidentSinkError) Code() string { return EDOCSYNCIncidentSink }

// Unwrap exposes the underlying sink failure evidence (the retried attempt's
// error; TS: `override readonly cause`).
func (e *IncidentSinkError) Unwrap() error { return e.Cause }

// IncidentLogFilename is the incident log filename, inside `.docsync/state/`
// (JSONL, one record per line).
const IncidentLogFilename = "incidents.jsonl"

// IncidentSink is the port: append-only persistence for validated incidents.
type IncidentSink interface {
	Append(ctx context.Context, incident *schemas.SafetyIncident) error
}

// FileIncidentSink builds the append-only JSONL file sink at
// `<stateDir>/incidents.jsonl`. See the module doc for why this bypasses the
// registry-validated store.
func FileIncidentSink(stateDir string) (IncidentSink, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return nil, &IncidentSinkError{
			Msg: "fileIncidentSink: stateDir must be an absolute path (got " +
				incidentJSONString(incidentTruncate(stateDir, 64)) + ")",
			Cause: nil,
		}
	}
	file := filepath.Join(stateDir, IncidentLogFilename)
	return &fileIncidentSink{stateDir: stateDir, file: file}, nil
}

// fileIncidentSink is the O_APPEND JSONL sink (TS `fs.appendFile`).
type fileIncidentSink struct {
	stateDir string
	file     string
}

// Append writes exactly one canonical-JSON line, creating the state directory
// on first write. The file is never truncated or rewritten.
func (s *fileIncidentSink) Append(_ context.Context, incident *schemas.SafetyIncident) error {
	line, err := IncidentCanonicalLine(incident)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.stateDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// IncidentCanonicalLine renders one incident as the canonical JSON the sink
// appends (keys sorted recursively, arrays in order — docutil.CanonicalJSON,
// the same bytes the state store hashes), so a re-record produces an identical
// line and duplicates stay detectable by `incident_id`.
func IncidentCanonicalLine(incident *schemas.SafetyIncident) (string, error) {
	generic, err := incidentGeneric(incident)
	if err != nil {
		return "", err
	}
	return docutil.CanonicalJSON(generic)
}

// incidentGeneric round-trips the record through generic JSON form (UseNumber
// so integers stay integral) so CanonicalJSON sees decoded values.
func incidentGeneric(incident *schemas.SafetyIncident) (map[string]any, error) {
	raw, err := json.Marshal(incident)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var generic map[string]any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return generic, nil
}

// incidentJSONString mirrors the JSON.stringify used in the TS sink message.
func incidentJSONString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "\"" + value + "\""
	}
	return string(raw)
}

// incidentTruncate mirrors String(stateDir).slice(0, 64) (code points).
func incidentTruncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

/* ------------------------------------------------------------- the incident */

// RecordIncidentInput is everything a tripped invariant knows.
type RecordIncidentInput struct {
	// At is the ISO-8601 timestamp from the caller's injected clock (never
	// generated here).
	At string
	// RunID is the run that tripped the invariant.
	RunID string
	// Rule is which binding safety rule fired (closed taxonomy,
	// schemas.SafetyIncidentRuleValues).
	Rule schemas.SafetyIncidentRule
	// OffendingPath is the path the refused operation touched.
	OffendingPath string
	// Detail is human-readable evidence — why the operation was refused.
	Detail string
	// DocPath is the documentation path the run was processing, when known.
	DocPath *string
	// SourcePRNumber is the source PR the run documents, when known.
	SourcePRNumber *int
}

// IncidentLog is the incident log surface: a sink bound to the record builder.
type IncidentLog struct {
	Sink IncidentSink
}

// CreateIncidentLog builds an IncidentLog over the injected sink.
func CreateIncidentLog(sink IncidentSink) *IncidentLog {
	return &IncidentLog{Sink: sink}
}

// Record composes, validates and appends one incident (fail closed on a
// malformed input; fail loud after exactly one sink retry).
func (l *IncidentLog) Record(ctx context.Context, input RecordIncidentInput) (*schemas.SafetyIncident, error) {
	incident, err := l.compose(input)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(incident)
	if err != nil {
		return nil, err
	}
	// Validation is NOT waived: every record is parsed against the registry
	// schema BEFORE the write (fail closed, refuse to persist malformed
	// evidence).
	validated, err := schemas.ParseSafetyIncident(raw)
	if err != nil {
		return nil, &IncidentValidationError{
			Msg: "safety-incident refused (fail closed, nothing written): " + incidentIssueSummary(err),
		}
	}
	if err := l.Sink.Append(ctx, validated); err != nil {
		// Exactly one retry, then fail LOUD: an incident that cannot be recorded
		// is itself an incident — never silently swallowed (PLAN §7.3).
		if retryErr := l.Sink.Append(ctx, validated); retryErr != nil {
			return nil, &IncidentSinkError{
				Msg: "incident " + validated.IncidentID + " could not be persisted after one retry (rule=" +
					string(validated.Rule) + ", path=" + validated.OffendingPath + "): " + retryErr.Error(),
				Cause: retryErr,
			}
		}
	}
	return validated, nil
}

// RecordIncident is the standalone form: `RecordIncident(log, input) ===
// log.Record(input)`.
func RecordIncident(ctx context.Context, log *IncidentLog, input RecordIncidentInput) (*schemas.SafetyIncident, error) {
	return log.Record(ctx, input)
}

// compose builds the incident with its deterministic id.
func (l *IncidentLog) compose(input RecordIncidentInput) (*schemas.SafetyIncident, error) {
	body := map[string]any{
		"schema":         "docsync.safety-incident/v1",
		"at":             input.At,
		"run_id":         input.RunID,
		"rule":           string(input.Rule),
		"offending_path": input.OffendingPath,
		"detail":         input.Detail,
	}
	if input.DocPath != nil {
		body["doc_path"] = *input.DocPath
	}
	if input.SourcePRNumber != nil {
		body["source_pr_number"] = *input.SourcePRNumber
	}
	canonical, err := docutil.CanonicalJSON(body)
	if err != nil {
		return nil, err
	}
	return &schemas.SafetyIncident{
		Schema:         "docsync.safety-incident/v1",
		IncidentID:     incidentID(canonical),
		At:             input.At,
		RunID:          input.RunID,
		Rule:           input.Rule,
		OffendingPath:  input.OffendingPath,
		Detail:         input.Detail,
		DocPath:        input.DocPath,
		SourcePRNumber: input.SourcePRNumber,
	}, nil
}

// incidentID is the deterministic incident id: `inc-<sha16>` where sha16 is the
// first 16 hex chars of the sha256 over the canonical JSON of the incident
// WITHOUT the id. Same inputs (including the injected `at`) always produce the
// same id.
func incidentID(canonicalBody string) string {
	return "inc-" + docutil.ContentHash(canonicalBody)[:16]
}

// incidentIssueSummary renders a schema-validation failure the way the TS
// mapped zod issues — `<path>.<to>.<issue>: <message>`, `<root>: <message>`
// when the issue has no path — so the fail-closed message stays auditable.
func incidentIssueSummary(err error) string {
	var validationErr *schemas.ValidationError
	if errors.As(err, &validationErr) {
		parts := make([]string, 0, len(validationErr.Issues))
		for _, issue := range validationErr.Issues {
			path := strings.Join(issue.Path, ".")
			if path == "" {
				path = "<root>"
			}
			parts = append(parts, path+": "+issue.Message)
		}
		return strings.Join(parts, "; ")
	}
	return err.Error()
}

/* ------------------------------------------------ OD-R50 emit points (Phase 9) */

// EmitInjectionContainedIncident is a documented emit-point (OD-R50; PLAN
// §10.3): an untrusted-content directive that was DETECTED AND CONTAINED at the
// gateway/containment layer. Core has escape-neutralization but no directive
// DETECTION — the detecting layer (gateway guard, workspace containment) calls
// this emit point when it contains a directive. The record is the structured
// evidence (fail loud via the sink — never silently swallowed).
func EmitInjectionContainedIncident(ctx context.Context, log *IncidentLog, input IncidentEmitInput) (*schemas.SafetyIncident, error) {
	return log.Record(ctx, RecordIncidentInput{
		At:             input.At,
		RunID:          input.RunID,
		Rule:           schemas.SafetyIncidentRuleInjectionContained,
		OffendingPath:  input.OffendingPath,
		Detail:         input.Detail,
		DocPath:        input.DocPath,
		SourcePRNumber: input.SourcePRNumber,
	})
}

// EmitAuthorityInfluenceIncident is a documented emit-point (OD-R50; PLAN
// §10.3): model output attempted to influence permissions, file selection, or
// credentials — emitted by the refusal seams when the refused path matches an
// authority-config surface (see IsAuthorityConfigSurface).
func EmitAuthorityInfluenceIncident(ctx context.Context, log *IncidentLog, input IncidentEmitInput) (*schemas.SafetyIncident, error) {
	return log.Record(ctx, RecordIncidentInput{
		At:             input.At,
		RunID:          input.RunID,
		Rule:           schemas.SafetyIncidentRuleModelInfluenceOnAuthority,
		OffendingPath:  input.OffendingPath,
		Detail:         input.Detail,
		DocPath:        input.DocPath,
		SourcePRNumber: input.SourcePRNumber,
	})
}

// IncidentEmitInput is the shared input of the OD-R50 emit points.
type IncidentEmitInput struct {
	At             string
	RunID          string
	OffendingPath  string
	Detail         string
	DocPath        *string
	SourcePRNumber *int
}
