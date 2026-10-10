package verifier

// Ported from packages/verifier/src/replay.ts (Phase 9, P9-T20/P9-T21;
// PLAN §10.7): audit replay — from a COMMITTED state alone (V1 files-only,
// OD-3 — `.docsync/state/` is committed to Git), reconstruct why a
// documented claim was changed for a run: the decision (fix-run) record, the
// provenance record, the verification verdicts from the per-doc ledgers, and
// the safety incidents. No git, no network, no GitHub — the committed state
// is the entire evidence surface.
//
// Read posture: evidence is rendered VERBATIM. The tool indexes records by
// their `schema` tag and `run_id` field but does NOT re-validate them — the
// committed bytes ARE the evidence; validation happened at write time,
// fail-closed (INCIDENT_SCHEMA/PROVENANCE_SCHEMA/VerificationRecordStore).
// Missing evidence is a DECLARED GAP in the trail, never fabricated (§3
// rule 10). Deterministic output; bounded scans fail loud past the cap.
//
// Porting notes:
//
//   - The TS async file reads become plain sequential os reads; there is no
//     concurrency in the original, so none is introduced here.
//   - JSON.parse/JSON.stringify parity: decision, provenance and each
//     ledger entry's report ride as `json.RawMessage` — the committed bytes
//     themselves — so the renderer emits them verbatim (key order and number
//     formatting preserved) exactly as JSON.stringify did after parse.
//   - TS `record['x']` property reads over `Record<string, unknown>` become
//     guarded map reads with the same typeof fallbacks (non-string values
//     read as absent).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/state"
)

// AuditScanCap is the bounded scan cap — a state dir past this many entries
// fails loud (TS AUDIT_SCAN_CAP).
const AuditScanCap = 5000

// The schema tags the replay indexes records by (verbatim evidence is never
// re-validated, so these are match keys, not parsers).
const (
	auditFixRunRecordSchema = "docsync.fix-run-record/v1"
	auditProvenanceSchema   = "docsync.provenance/v1"
)

// AuditTrailVerification is one verification-ledger verdict for the run.
type AuditTrailVerification struct {
	DocPath string
	// Report is the ledger entry's embedded report, VERBATIM (the committed
	// bytes are the evidence).
	Report json.RawMessage
	At     string
}

// AuditTrailIncident is one safety incident recorded for the run.
type AuditTrailIncident struct {
	IncidentID    string
	At            string
	Rule          string
	OffendingPath string
	Detail        string
}

// AuditTrail is the reconstructed evidence surface for one run. Decision and
// Provenance are the verbatim record bytes, or nil when the evidence is
// absent — a declared gap, never fabricated.
type AuditTrail struct {
	RunID         string
	Decision      json.RawMessage
	Provenance    json.RawMessage
	Verifications []AuditTrailVerification
	Incidents     []AuditTrailIncident
	Gaps          []string
}

// AuditInputError is the bad-input class port (TS AuditInputError): the
// docsync-audit CLI classifies it as a config error (exit 2), never a
// runtime failure. The message is the TS constructor's text, verbatim.
type AuditInputError struct {
	Message string
	coded   *docutil.Error
}

func newAuditInputError(message string) *AuditInputError {
	return &AuditInputError{Message: message, coded: docutil.New(docutil.CodeConfig, message)}
}

func (e *AuditInputError) Error() string { return e.Message }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *AuditInputError) Unwrap() error { return e.coded }

// Code returns the routing code for this refusal (a config error).
func (e *AuditInputError) Code() string { return docutil.CodeConfig }

// AuditReplayOptions configures an audit replay (the TS createAuditReplay
// options).
type AuditReplayOptions struct {
	// StateDir is the committed `.docsync` directory.
	StateDir string
}

// AuditReplayInput is the replayRun input; DocPath is the optional
// verification-ledger scoping (the TS `docPath?`).
type AuditReplayInput struct {
	RunID   string
	DocPath *string
}

// AuditReplay reconstructs audit trails from committed state alone.
type AuditReplay struct {
	runsDir       string
	docsDir       string
	incidentsFile string
}

// NewAuditReplay builds a replay over the committed state dir (the TS
// createAuditReplay factory).
func NewAuditReplay(options AuditReplayOptions) *AuditReplay {
	stateDir := options.StateDir
	return &AuditReplay{
		runsDir:       filepath.Join(stateDir, "state", "runs"),
		docsDir:       filepath.Join(stateDir, "state", "docs"),
		incidentsFile: filepath.Join(stateDir, "state", "incidents.jsonl"),
	}
}

// ReplayRun reconstructs the audit trail for one run. Absent evidence is a
// declared gap; a bad run id and a scan past the cap fail loud (typed
// *AuditInputError).
func (a *AuditReplay) ReplayRun(input AuditReplayInput) (AuditTrail, error) {
	if input.RunID == "" {
		return AuditTrail{}, newAuditInputError("run id is required (--run <id>)")
	}
	runID := input.RunID
	var gaps []string

	/* -- decision (fix-run record) ------------------------------------ */
	var decision json.RawMessage
	runNames, err := listAuditJSONFiles(a.runsDir)
	if err != nil {
		return AuditTrail{}, err
	}
	for _, name := range runNames {
		record, ok := readAuditRecord(filepath.Join(a.runsDir, name))
		if !ok {
			continue
		}
		if auditStringField(record.value, "schema") == auditFixRunRecordSchema && auditRunIDOf(record.value) == runID {
			decision = record.raw
			break
		}
	}
	if decision == nil {
		gaps = append(gaps, "no fix-run (decision) record for run "+runID+" under state/runs")
	}

	/* -- provenance ---------------------------------------------------- */
	var provenance json.RawMessage
	if record, ok := readAuditRecord(filepath.Join(a.runsDir, "prov-"+runID+".json")); ok {
		provenance = record.raw
	} else {
		for _, name := range runNames {
			record, ok := readAuditRecord(filepath.Join(a.runsDir, name))
			if !ok {
				continue
			}
			if auditStringField(record.value, "schema") == auditProvenanceSchema && auditRunIDOf(record.value) == runID {
				provenance = record.raw
				break
			}
		}
	}
	if provenance == nil {
		gaps = append(gaps, "no provenance record for run "+runID+
			" (pre-Phase-9 run, or the run never shipped a change)")
	}

	/* -- verification ledgers ------------------------------------------ */
	var verifications []AuditTrailVerification
	docFiles, err := a.auditDocFiles(input.DocPath)
	if err != nil {
		return AuditTrail{}, err
	}
	for _, docFile := range docFiles {
		docState, ok := readAuditRecord(filepath.Join(a.docsDir, docFile.name))
		if !ok {
			continue
		}
		ledger, isArray := docState.value["verifications"].([]any)
		if !isArray {
			continue
		}
		verbatim := auditLedgerVerbatim(docState.raw)
		for i, entry := range ledger {
			typed, isObject := entry.(map[string]any)
			if !isObject {
				continue
			}
			if auditRunIDOf(typed) != runID {
				continue
			}
			docPath := docFile.docPath
			if !docFile.decoded {
				// An undecodable stem is not a doc-state file; the entry's
				// own run id stands in (the TS docPath ?? String(run_id)).
				docPath = "unknown"
				if i < len(verbatim) {
					docPath = stringifyVerbatim(verbatim[i].RunID)
				}
			}
			var report json.RawMessage
			if i < len(verbatim) {
				report = verbatim[i].Report
			}
			verifications = append(verifications, AuditTrailVerification{
				DocPath: docPath,
				Report:  report,
				At:      auditStringField(typed, "at"),
			})
		}
	}
	if len(verifications) == 0 {
		gaps = append(gaps, "no verification ledger entries for run "+runID+
			" in the doc-state ledgers")
	}

	/* -- incidents ----------------------------------------------------- */
	var incidents []AuditTrailIncident
	incidentsBytes, readErr := os.ReadFile(a.incidentsFile)
	if readErr != nil {
		gaps = append(gaps, "no incidents.jsonl under <state-dir>/state (no incident was ever recorded)")
	} else {
		for _, line := range strings.Split(string(incidentsBytes), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue // a malformed line is skipped; incident ids make dupes detectable
			}
			if auditRunIDOf(record) != runID {
				continue
			}
			incidents = append(incidents, AuditTrailIncident{
				IncidentID:    auditStringFieldOr(record, "incident_id", "unknown"),
				At:            auditStringField(record, "at"),
				Rule:          auditStringFieldOr(record, "rule", "unknown"),
				OffendingPath: auditStringFieldOr(record, "offending_path", "unknown"),
				Detail:        auditStringField(record, "detail"),
			})
		}
	}
	if len(incidents) == 0 && !hasIncidentsFileGap(gaps) {
		gaps = append(gaps, "no incidents recorded for run "+runID+" (clean run)")
	}

	return AuditTrail{
		RunID:         runID,
		Decision:      decision,
		Provenance:    provenance,
		Verifications: verifications,
		Incidents:     incidents,
		Gaps:          gaps,
	}, nil
}

// RenderAuditTrail is the deterministic markdown rendering of an audit trail
// (TS renderAuditTrail). Verbatim JSON blocks keep the committed bytes' key
// order (JSON.stringify parity): pretty-printed for the decision and the
// provenance, compact for the per-verdict reports.
func RenderAuditTrail(trail AuditTrail) string {
	var lines []string
	lines = append(lines, "# docsync audit trail — run "+trail.RunID)
	lines = append(lines, "")
	lines = append(lines, "## Decision (fix-run record)")
	if trail.Decision == nil {
		lines = append(lines, "_declared gap — see below_")
	} else {
		lines = append(lines, "```json")
		lines = append(lines, auditPrettyJSON(trail.Decision))
		lines = append(lines, "```")
	}
	lines = append(lines, "")
	lines = append(lines, "## Provenance")
	if trail.Provenance == nil {
		lines = append(lines, "_declared gap — see below_")
	} else {
		lines = append(lines, "```json")
		lines = append(lines, auditPrettyJSON(trail.Provenance))
		lines = append(lines, "```")
	}
	lines = append(lines, fmt.Sprintf("## Verification verdicts (%d)", len(trail.Verifications)))
	for _, verification := range trail.Verifications {
		lines = append(lines, fmt.Sprintf("- %s @ %s: %s",
			verification.DocPath, verification.At, auditCompactJSON(verification.Report)))
	}
	lines = append(lines, fmt.Sprintf("## Safety incidents (%d)", len(trail.Incidents)))
	for _, incident := range trail.Incidents {
		lines = append(lines, fmt.Sprintf("- %s %s %s %s: %s",
			incident.IncidentID, incident.At, incident.Rule, incident.OffendingPath, incident.Detail))
	}
	lines = append(lines, "")
	lines = append(lines, "## Declared gaps")
	for _, gap := range trail.Gaps {
		lines = append(lines, "- "+gap)
	}
	return strings.Join(lines, "\n")
}

/* ------------------------------------------------------------- internals */

// auditDocFile is one doc-state evidence file: its filename under state/docs
// and, when the stem decoded, its repository doc path (the TS
// `{name, docPath: decoded | null}`).
type auditDocFile struct {
	name    string
	docPath string
	decoded bool
}

// auditDocFiles builds the doc-state file list: the one requested doc path,
// or a sorted scan of state/docs (undecodable stems are kept — the file is
// still read, only its docPath stands as unknown).
func (a *AuditReplay) auditDocFiles(docPath *string) ([]auditDocFile, error) {
	if docPath != nil {
		encoded, err := state.EncodeStateDocPath(*docPath)
		if err != nil {
			return nil, err
		}
		return []auditDocFile{{name: encoded + ".json", docPath: *docPath, decoded: true}}, nil
	}
	names, err := listAuditJSONFiles(a.docsDir)
	if err != nil {
		return nil, err
	}
	docFiles := make([]auditDocFile, 0, len(names))
	for _, name := range names {
		docPath, err := state.DecodeStateDocPath(strings.TrimSuffix(name, ".json"))
		if err != nil {
			docFiles = append(docFiles, auditDocFile{name: name}) // undecodable stem ⇒ docPath unknown
			continue
		}
		docFiles = append(docFiles, auditDocFile{name: name, docPath: docPath, decoded: true})
	}
	return docFiles, nil
}

// auditRecord is one verbatim evidence record: the decoded generic value the
// replay indexes by schema/run_id (never re-validated) plus the exact bytes
// the renderer emits.
type auditRecord struct {
	value map[string]any
	raw   json.RawMessage
}

// readAuditRecord ports the TS readJson: unreadable or corrupt evidence is a
// null record — a declared gap downstream, never fabricated.
func readAuditRecord(file string) (auditRecord, bool) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return auditRecord{}, false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return auditRecord{}, false
	}
	object, isObject := value.(map[string]any)
	if !isObject {
		return auditRecord{}, false
	}
	return auditRecord{value: object, raw: json.RawMessage(raw)}, true
}

// auditLedgerEntry is the verbatim projection of one doc-state verification
// ledger entry: every field rides as raw bytes so the report renders from
// the committed bytes exactly (JSON.stringify parity).
type auditLedgerEntry struct {
	Report json.RawMessage `json:"report"`
	RunID  json.RawMessage `json:"run_id"`
}

// auditLedgerVerbatim extracts the ledger entries' verbatim bytes from one
// doc-state file's raw bytes. A non-array (or otherwise unreadable) ledger
// yields nothing — the generic decode already skipped those files — while a
// decoded array aligns index-for-index with the generic ledger.
func auditLedgerVerbatim(docStateRaw json.RawMessage) []auditLedgerEntry {
	var shape struct {
		Verifications []auditLedgerEntry `json:"verifications"`
	}
	if err := json.Unmarshal(docStateRaw, &shape); err != nil {
		return nil
	}
	return shape.Verifications
}

// listAuditJSONFiles ports the TS listJsonFiles: a missing directory is an
// empty evidence surface (declared gaps), and past the scan cap the scan
// fails loud with the TS message text.
func listAuditJSONFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // missing directory ⇒ empty evidence surface (declared gaps)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".json") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > AuditScanCap {
		return nil, newAuditInputError(fmt.Sprintf(
			"scan cap exceeded: %d files under %s > %d", len(names), dir, AuditScanCap))
	}
	return names, nil
}

// auditRunIDOf ports the TS runIdOf: the record's run_id when it is a
// non-empty string; everything else reads as "no run id".
func auditRunIDOf(record map[string]any) string {
	candidate, ok := record["run_id"].(string)
	if !ok || candidate == "" {
		return ""
	}
	return candidate
}

// auditStringField reads a string field, or "" when absent or of another type
// (the TS `typeof x === 'string'` guard).
func auditStringField(record map[string]any, key string) string {
	value, ok := record[key].(string)
	if !ok {
		return ""
	}
	return value
}

// auditStringFieldOr is auditStringField with the fallback the TS used for
// the incident fields (`typeof x === 'string' ? x : 'unknown'`).
func auditStringFieldOr(record map[string]any, key, fallback string) string {
	if value, ok := record[key].(string); ok {
		return value
	}
	return fallback
}

// stringifyVerbatim ports String(typed['run_id'] ?? 'unknown') over raw
// bytes: an absent or null value reads as "unknown", a JSON string as its
// text, anything else as its compact JSON text.
func stringifyVerbatim(raw json.RawMessage) string {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return "unknown"
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(bytes.TrimSpace(raw))
}

// hasIncidentsFileGap reports whether a declared gap already covers the
// absent incidents.jsonl (the TS gaps.every(!startsWith('no incidents.jsonl')).
func hasIncidentsFileGap(gaps []string) bool {
	for _, gap := range gaps {
		if strings.HasPrefix(gap, "no incidents.jsonl") {
			return true
		}
	}
	return false
}

// auditPrettyJSON renders raw evidence the way JSON.stringify(value, null, 2)
// did: the verbatim bytes, indented with two spaces (key order preserved).
func auditPrettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw) // unreachable: raw was valid JSON when read
	}
	return buf.String()
}

// auditCompactJSON renders raw evidence the way JSON.stringify(value) did:
// the verbatim bytes with insignificant whitespace elided; an absent report
// renders as null.
func auditCompactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw) // unreachable: raw was valid JSON when read
	}
	return buf.String()
}
