package analysis

// Ported from packages/core/src/analysis/generated.ts (P11-T23; ARCH §4.1,
// §8.7, §8.8; PLAN §12.3 F11-12): the memory pattern `generated-sdk`
// ("changes under src/api/generated may alter public documentation
// indirectly") is wired to the analyzer action `inspect_generated_output`,
// and analyzer MISPREDICTIONS feed closed-loop learning (§8.7) through the
// EXISTING Phase-10 feedback recorder.
//
// Two pieces, both thin over existing machinery:
//
//  1. InspectGeneratedOutput — given the caller's changed-file rows and
//     candidate doc list, REUSES the detection from detect.go (DetectFileFlags
//     — the heuristic is never duplicated here) and produces a typed, PURE,
//     data-only routing record: pattern `generated-sdk`, analyzer action
//     `inspect_generated_output`, the generated-path evidence (paths +
//     caller-supplied blob SHAs), and the affected docs selected from the
//     CALLER-SUPPLIED list (those whose content mentions the generated file's
//     path stem). The record carries a policy-routing SUGGESTION only — the
//     policy engine decides the outcome. The decision-action vocabulary
//     (schemas.ActionValues: ignore/review/investigate/patch) is CLOSED and
//     deliberately excludes `inspect_generated_output`: the analyzer action is
//     context data the policy engine consumes, never a decision the analyzer
//     makes (models propose / analyzers propose; the application decides,
//     ARCH §8.3; the Jev Stage-1 routing table owns every `decision.action`).
//
//  2. RecordAnalyzerMisprediction — when the proxy prediction is wrong
//     (caller supplies predicted vs actual, paths, evidence), emits ONE
//     feedback event through the feedback recorder
//     (`docsync.feedback-event/v1`, append-only JSONL under
//     `<stateDir>/feedback/events.jsonl`, OD-R22). The feedback-event signal
//     vocabulary is CLOSED at the five reviewer signals (ARCH §8.7;
//     schemas.FeedbackEventValues) and cannot be extended here (schemas are
//     outside this unit's ownership), so the misprediction is modeled as the
//     CLOSEST existing kind — `reject` (the prediction / routing was rejected
//     as wrong) — with the reject reason carrying the full misprediction
//     facts deterministically (predicted, actual, paths, evidence).
//     DEVIATION RECORDED (P11-T23): a dedicated misprediction signal kind
//     would be the cleaner vocabulary, but `feedback-event/v1` is closed; the
//     facts survive in the reason verbatim. Fail closed: a recorder rejection
//     (validation or sink I/O) propagates as a typed docutil error — a
//     misprediction that cannot be recorded is surfaced, never dropped
//     silently.
//
// PORTING NOTE — recorder wiring: the TypeScript module calls
// `createFeedbackRecorder` from core/src/feedback/recorder.ts, which has no
// Go counterpart yet (internal/feedback is a later migration wave). The
// recorder surface recordAnalyzerMisprediction depends on (validate → append
// one JSONL row; fail-closed replay) is therefore ported faithfully inside
// this file (RecordFeedbackEvent / ListFeedbackEvents over the same
// `<stateDir>/feedback/events.jsonl` sink semantics), so the behavior and its
// tests are preserved. When internal/feedback lands, these two functions are
// the swap point — the misprediction wiring above them is unchanged.
//
// Pure where it can be: InspectGeneratedOutput has no I/O, no clock, and
// returns plain data (Go has no Object.freeze; immutability is by convention —
// nothing in the record can reach a write path or the policy engine's
// decision vocabulary). Time and identity in the feedback path are injected
// (Clock, optional EventID/At) — never generated here.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// GeneratedSdkPattern is the D1 memory-pattern name this module wires
// (ARCH §8.8 record example; the structured `known-patterns` record carries
// the long form `generated-sdk-docs-follow-api-changes` in the schema
// example; TS GENERATED_SDK_PATTERN).
const GeneratedSdkPattern = "generated-sdk"

// FeedbackEventsFilename is the RAW feedback-events sink path, relative to the
// state dir (OD-R22; TS FEEDBACK_EVENTS_FILENAME): `<stateDir>/feedback/events.jsonl`.
const FeedbackEventsFilename = "feedback/events.jsonl"

// FeedbackDirName is the subdirectory under the state dir holding RAW
// feedback rows (OD-R22; TS FEEDBACK_DIR_NAME).
const FeedbackDirName = "feedback"

// GeneratedRoutingEvidence is one changed generated file in the routing
// record's evidence surface (TS GeneratedRoutingEvidence).
type GeneratedRoutingEvidence struct {
	Path string `json:"path"`
	// BlobSha is the HEAD blob SHA when the caller supplied one (provenance —
	// evidence or it didn't happen); nil when absent/empty.
	BlobSha *string `json:"blob_sha,omitempty"`
	// Status is the caller-supplied change status (modified/added/deleted/
	// renamed) when known; nil when absent/empty.
	Status *string `json:"status,omitempty"`
}

// GeneratedRoutingDoc is one caller-supplied candidate doc the affected-doc
// selection filters (TS GeneratedRoutingDoc).
type GeneratedRoutingDoc struct {
	Path    string
	Content string
}

// GeneratedRoutingFile is the detector shape plus the optional evidence
// extras the detectors ignore (TS `DetectorFile & { blobSha?; status? }`).
type GeneratedRoutingFile struct {
	DetectorFile
	BlobSha string
	Status  string
}

// GeneratedRoutingInput is the input of InspectGeneratedOutput
// (TS GeneratedRoutingInput).
type GeneratedRoutingInput struct {
	// RunID is the run identity carried verbatim into the record (provenance).
	RunID   string
	BaseSha string
	HeadSha string
	// Files are the changed-file rows in the DetectorFile detector shape;
	// blobSha and status are optional evidence extras the detectors ignore.
	Files []GeneratedRoutingFile
	// Docs is the caller-supplied candidate doc list (manifest/graph-known
	// docs). The affected-doc selection NEVER invents docs: only these
	// candidates are eligible, and only those whose content mentions a
	// generated file's path stem are selected. Nil when the caller supplied
	// none.
	Docs []GeneratedRoutingDoc
}

// GeneratedRoutingSuggestion is the policy-routing suggestion — DATA
// describing what to route, never a decision (TS GeneratedRoutingSuggestion).
type GeneratedRoutingSuggestion struct {
	// AnalyzerAction is the stable analyzer action (ARCH §4.1/§8.8).
	AnalyzerAction string `json:"analyzer_action"`
	// Pattern is the memory pattern the suggestion is grounded in
	// (ARCH §8.8).
	Pattern string `json:"pattern"`
	// Basis is the deterministic basis note carried for the policy engine's
	// context.
	Basis string `json:"basis"`
}

// GeneratedRoutingRecord is the typed routing record. Deliberately contains
// NO `outcome`/`decision` field: the outcome (ignore/review/investigate/
// patch) belongs to the policy engine's closed decision vocabulary and is
// decided there, never here (TS GeneratedRoutingRecord).
type GeneratedRoutingRecord struct {
	Pattern string `json:"pattern"`
	// Action is the analyzer action the pattern routes through (ARCH §8.8).
	Action    string                     `json:"action"`
	RunID     string                     `json:"run_id"`
	BaseSha   string                     `json:"base_sha"`
	HeadSha   string                     `json:"head_sha"`
	Generated []GeneratedRoutingEvidence `json:"generated"`
	// AffectedDocs lists the caller-supplied candidate docs whose content
	// mentions a generated path stem.
	AffectedDocs []string                   `json:"affected_docs"`
	Suggestion   GeneratedRoutingSuggestion `json:"suggestion"`
}

// SuggestionBasis is the deterministic basis note (TS SUGGESTION_BASIS).
const SuggestionBasis = "memory pattern generated-sdk (ARCH §8.8): changes under src/api/generated may alter " +
	"public documentation indirectly — route per policy; the policy engine decides the outcome"

// PathStem returns the filename stem used for the affected-doc mention match:
// the basename without its extension (TS pathStem).
func PathStem(path string) string {
	normalized := strings.ReplaceAll(path, "\\", "/")
	base := normalized
	if index := strings.LastIndex(normalized, "/"); index >= 0 {
		base = normalized[index+1:]
	}
	dot := strings.LastIndex(base, ".")
	// `dot <= 0`: no dot at all, or a leading dot (dotfile — the stem is the
	// whole basename).
	if dot <= 0 {
		return base
	}
	return base[:dot]
}

// InspectGeneratedOutput inspects the caller's change for generated files and
// — when ANY changed file is generated — produces the data-only routing record
// for the `generated-sdk` pattern (ARCH §8.8). Detection is REUSED from
// detect.go (DetectFileFlags: path conventions always, content markers when
// the caller supplied contentAtHead); nothing is re-implemented here.
//
// Returns nil when NO changed file is generated — the action never fires on a
// non-generated change (negative contract, V-P11-13).
//
// Affected docs: among the CALLER-SUPPLIED candidates, a doc is affected iff
// its content mentions the stem (basename without extension,
// case-insensitive) of at least one generated changed file. Deterministic and
// conservative: no candidate list ⇒ no affected docs; a missed mention only
// narrows context, an invented doc would fabricate relevance.
//
// Pure: no fs/network/clock; the returned record is data with no function
// members — nothing in it can reach a write path or the policy engine's
// decision vocabulary (the outcome stays the engine's).
func InspectGeneratedOutput(input GeneratedRoutingInput) *GeneratedRoutingRecord {
	generatedEvidence := []GeneratedRoutingEvidence{}
	for _, file := range input.Files {
		flags := DetectFileFlags(file.DetectorFile)
		if !flags.Generated {
			continue
		}
		evidence := GeneratedRoutingEvidence{Path: file.Path}
		if len(file.BlobSha) > 0 {
			evidence.BlobSha = &file.BlobSha
		}
		if len(file.Status) > 0 {
			evidence.Status = &file.Status
		}
		generatedEvidence = append(generatedEvidence, evidence)
	}
	if len(generatedEvidence) == 0 {
		return nil
	}

	stems := make([]string, 0, len(generatedEvidence))
	for _, evidence := range generatedEvidence {
		stems = append(stems, strings.ToLower(PathStem(evidence.Path)))
	}
	affected := []string{}
	for _, doc := range input.Docs {
		content := strings.ToLower(doc.Content)
		mentioned := false
		for _, stem := range stems {
			if len(stem) > 0 && strings.Contains(content, stem) {
				mentioned = true
				break
			}
		}
		if mentioned && !containsString(affected, doc.Path) {
			affected = append(affected, doc.Path)
		}
	}

	return &GeneratedRoutingRecord{
		Pattern:      GeneratedSdkPattern,
		Action:       InspectGeneratedOutputAction,
		RunID:        input.RunID,
		BaseSha:      input.BaseSha,
		HeadSha:      input.HeadSha,
		Generated:    generatedEvidence,
		AffectedDocs: affected,
		Suggestion: GeneratedRoutingSuggestion{
			AnalyzerAction: InspectGeneratedOutputAction,
			Pattern:        GeneratedSdkPattern,
			Basis:          SuggestionBasis,
		},
	}
}

/* ----------------------------------------------------- misprediction feedback */

// AnalyzerMispredictionSignal is the closest EXISTING closed-vocabulary signal
// kind for an analyzer misprediction (P11-T23 deviation note in the module
// header): `reject` — the prediction/routing was wrong and is being rejected,
// with the misprediction facts carried in the reject reason (never a new
// signal kind: `docsync.feedback-event/v1` is closed at the five ARCH §8.7
// reviewer signals and schemas are outside this unit's ownership;
// TS ANALYZER_MISPREDICTION_SIGNAL).
const AnalyzerMispredictionSignal = "reject"

// AnalyzerMispredictionInputCode is the code carried by input-fact refusals.
// DEVIATION (recorded per the migration conventions): the TS class
// AnalyzerMispredictionInputError carried 'EDOCSYNC_MISPREDICTION_INPUT',
// which is not in docutil's code list; the closest existing code
// (EDOCSYNC_VALIDATION — a fail-closed input refusal) is used and the exact
// TS message text is preserved.
const AnalyzerMispredictionInputCode = docutil.CodeValidation

// NewAnalyzerMispredictionInputError builds the fail-closed refusal to record
// a misprediction whose FACTS are empty: an event without predicted/actual/
// paths is an assertion, not evidence (provenance or it didn't happen).
// Typed — never a silent drop (TS AnalyzerMispredictionInputError).
func NewAnalyzerMispredictionInputError(message string) error {
	return docutil.New(AnalyzerMispredictionInputCode, message)
}

// AnalyzerMispredictionInput is the caller-supplied misprediction facts
// (TS AnalyzerMispredictionInput).
type AnalyzerMispredictionInput struct {
	// DecisionID is the decision the mispredicted prediction/routing came
	// from (ARCH §5.4 identity).
	DecisionID    string
	PolicyVersion int
	// Pattern is the memory pattern whose prediction misfired (default:
	// `generated-sdk`); nil means defaulted.
	Pattern *string
	// Predicted is what the analyzer/policy proxy predicted (caller-supplied,
	// non-empty).
	Predicted string
	// Actual is what actually happened (caller-supplied, non-empty).
	Actual string
	// Paths are the generated paths the prediction covered (≥ 1).
	Paths []string
	// Evidence holds optional evidence refs (blob SHAs, commit refs) carried
	// in the reason; nil/empty omits them.
	Evidence []string
	RunID    *string
	DocPath  *string
	Repo     *string
	// EventID is the injected event id (defaults to a canonical ULID in the
	// recorder); nil means defaulted.
	EventID *string
	// At is the injected instant (defaults to the injected clock) — never
	// generated here; nil means defaulted.
	At *string
}

// FeedbackRecorderOptions is the recorder wiring for
// RecordAnalyzerMisprediction — mirrors the TypeScript
// FeedbackRecorderOptions (TS AnalyzerMispredictionOptions).
type FeedbackRecorderOptions struct {
	// StateDir is the ABSOLUTE path to `.docsync/state` (D1; OD-R22 rows under
	// `state/feedback/`).
	StateDir string
	// Clock is the injected clock: returns the current ISO-8601 instant
	// (never a wall-clock read inside logic).
	Clock func() string
}

// AnalyzerMispredictionReason deterministically serializes the misprediction
// facts into the reject-reason payload (the closed event schema has no
// free-form payload member; the reason IS the detail carrier).
// Caller-supplied order is preserved (deterministic for identical input);
// evidence is omitted when absent (TS analyzerMispredictionReason).
func AnalyzerMispredictionReason(input AnalyzerMispredictionInput) string {
	pattern := GeneratedSdkPattern
	if input.Pattern != nil {
		pattern = *input.Pattern
	}
	parts := []string{
		fmt.Sprintf("analyzer-misprediction (%s; ARCH §8.7)", pattern),
		"predicted=" + input.Predicted,
		"actual=" + input.Actual,
		"paths=" + strings.Join(input.Paths, "|"),
	}
	if len(input.Evidence) > 0 {
		parts = append(parts, "evidence="+strings.Join(input.Evidence, "|"))
	}
	return strings.Join(parts, "; ")
}

// AssertMispredictionFacts is the fail-closed fact check: a misprediction
// without its facts is an assertion, not evidence (TS assertMispredictionFacts).
func AssertMispredictionFacts(input AnalyzerMispredictionInput) error {
	facts := []string{}
	if len(input.Predicted) == 0 {
		facts = append(facts, "predicted")
	}
	if len(input.Actual) == 0 {
		facts = append(facts, "actual")
	}
	if len(input.Paths) == 0 {
		facts = append(facts, "paths")
	}
	if len(facts) > 0 {
		return NewAnalyzerMispredictionInputError(
			"analyzer misprediction refused (fail closed): empty fact field(s) " + strings.Join(facts, ", ") +
				" — a misprediction without its facts is an assertion, not evidence")
	}
	return nil
}

// RecordAnalyzerMisprediction emits ONE analyzer-misprediction feedback event
// through the feedback recorder (ARCH §8.7 closed-loop learning; P11-T23):
// signal AnalyzerMispredictionSignal (`reject`), reject reason
// AnalyzerMispredictionReason (the full predicted/actual/paths/evidence
// facts), persisted append-only at `<stateDir>/feedback/events.jsonl` (OD-R22).
//
// Fail closed: input-fact gaps return an input error (nothing persisted);
// recorder validation returns a validation error; sink I/O returns an I/O
// error. NOTHING is ever dropped silently — a misprediction that cannot be
// recorded surfaces as a typed error (TS recordAnalyzerMisprediction).
func RecordAnalyzerMisprediction(input AnalyzerMispredictionInput, options FeedbackRecorderOptions) (*schemas.FeedbackEvent, error) {
	if err := AssertMispredictionFacts(input); err != nil {
		return nil, err
	}
	reason := AnalyzerMispredictionReason(input)
	return RecordFeedbackEvent(options, FeedbackEventInput{
		DecisionID:    input.DecisionID,
		PolicyVersion: input.PolicyVersion,
		Signal:        AnalyzerMispredictionSignal,
		RejectReason:  &reason,
		RunID:         input.RunID,
		DocPath:       input.DocPath,
		Repo:          input.Repo,
		EventID:       input.EventID,
		At:            input.At,
	})
}

/* --------------------------------------------- feedback recorder + JSONL sink
Ported from core/src/feedback/recorder.ts and core/src/feedback/sink.ts
(P10-T02; ARCH §8.7; OD-R22) — the surface recordAnalyzerMisprediction
depends on, until internal/feedback lands (see the module-header porting
note). The recorder is the ONE write path for the five reviewer signals; each
signal is an append-only `docsync.feedback-event/v1` row under
`<stateDir>/feedback/events.jsonl`, validated BEFORE persistence (fail
closed: malformed evidence is refused, never stored) and re-validated on read
(a corrupt line is a hard error, never skipped). */

// FeedbackEventInput is the recorder input: a feedback event minus the
// generated fields (TS FeedbackEventInput). Nil pointers mean "absent"
// (omitted from the event, exactly like the TS spread).
type FeedbackEventInput struct {
	EventID       *string
	At            *string
	DecisionID    string
	PolicyVersion int
	Signal        string
	RejectReason  *string
	EditDiff      *string
	RunID         *string
	DocPath       *string
	Repo          *string
}

const feedbackEventSchemaID = "docsync.feedback-event/v1"

// RecordFeedbackEvent validates and appends one feedback event; returns the
// stored row (TS FeedbackRecorder.record).
func RecordFeedbackEvent(options FeedbackRecorderOptions, input FeedbackEventInput) (*schemas.FeedbackEvent, error) {
	sink, err := newJSONLSink(options.StateDir, FeedbackEventsFilename)
	if err != nil {
		return nil, err
	}
	event := schemas.FeedbackEvent{
		Schema:        feedbackEventSchemaID,
		DecisionID:    input.DecisionID,
		PolicyVersion: input.PolicyVersion,
		Signal:        schemas.FeedbackEventSignal(input.Signal),
		RejectReason:  input.RejectReason,
		EditDiff:      input.EditDiff,
		RunID:         input.RunID,
		DocPath:       input.DocPath,
		Repo:          input.Repo,
	}
	// Identity & time are injected, never generated behind the caller's back
	// (TS: `input.event_id ?? generateUlid()`, `input.at ?? options.clock()`).
	if input.EventID != nil {
		event.EventID = *input.EventID
	} else {
		generated, err := docutil.NewULIDGenerator().Generate()
		if err != nil {
			return nil, docutil.Newf(docutil.CodeInternal, "feedback event id generation failed: %v", err)
		}
		event.EventID = generated
	}
	if input.At != nil {
		event.At = *input.At
	} else if options.Clock != nil {
		event.At = options.Clock()
	}
	validated, err := parseFeedbackEventRow(&event, "recorder.record")
	if err != nil {
		return nil, err
	}
	if err := sink.append(validated); err != nil {
		return nil, err
	}
	return validated, nil
}

// ListFeedbackEvents replays every stored feedback event (TS
// FeedbackRecorder.list). A missing file replays as empty; a malformed line
// errors (fail closed).
func ListFeedbackEvents(options FeedbackRecorderOptions) ([]schemas.FeedbackEvent, error) {
	sink, err := newJSONLSink(options.StateDir, FeedbackEventsFilename)
	if err != nil {
		return nil, err
	}
	return sink.readAll()
}

// parseFeedbackEventRow validates one feedback-event row; the failure is the
// Go form of the TS FeedbackValidationError (TS parseFeedbackEvent).
func parseFeedbackEventRow(event *schemas.FeedbackEvent, origin string) (*schemas.FeedbackEvent, error) {
	if issues := event.Validate(); len(issues) > 0 {
		return nil, docutil.Newf(docutil.CodeValidation,
			"feedback event refused at %s (fail closed): %s", origin, schemas.NewValidationError(issues...).Error())
	}
	return event, nil
}

// jsonlSink is the append-only persistence + fail-closed replay surface for
// one JSONL file (TS JsonlSink).
type jsonlSink struct {
	stateDir string
	filename string
	file     string
}

// newJSONLSink builds the sink over `<stateDir>/<filename>`, fail closed on a
// non-absolute state dir or an escaping filename (TS createJsonlSink).
func newJSONLSink(stateDir, filename string) (*jsonlSink, error) {
	if len(stateDir) == 0 || !filepath.IsAbs(stateDir) {
		return nil, docutil.Newf(docutil.CodeIO,
			"createJsonlSink: stateDir must be an absolute path (got %s)",
			strconv.Quote(truncateForMessage(stateDir, 64)))
	}
	if len(filename) == 0 || filepath.IsAbs(filename) || strings.Contains(filename, "..") {
		return nil, docutil.Newf(docutil.CodeIO,
			"feedback sink filename must be a state-dir-relative path (got %s)",
			strconv.Quote(truncateForMessage(filename, 64)))
	}
	return &jsonlSink{stateDir: stateDir, filename: filename, file: filepath.Join(stateDir, filename)}, nil
}

// append writes one already-validated value as a JSON line (TS JsonlSink.append).
// The parent directory is created on first write (idempotent mkdir); the file
// is only ever opened for append — never truncated, never rewritten. One
// retry on failure, then fail LOUD (unrecordable feedback is lost evaluation
// data and must surface, same discipline as incidents).
func (s *jsonlSink) append(event *schemas.FeedbackEvent) error {
	line, err := marshalJSONLine(event)
	if err != nil {
		return docutil.Newf(docutil.CodeIO, "feedback row could not be serialized: %v", err)
	}
	// First attempt: mkdir (idempotent) + append.
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err == nil {
		if appendErr := appendLine(s.file, line); appendErr == nil {
			return nil
		}
	}
	// Exactly one retry (append only), then fail LOUD.
	if appendErr := appendLine(s.file, line); appendErr != nil {
		return docutil.Newf(docutil.CodeIO, "feedback row could not be appended to %q after one retry: %v",
			s.filename, appendErr)
	}
	return nil
}

// readAll replays every row, validating each line (fail closed — a corrupt
// line is a hard error, never skipped; TS JsonlSink.readAll).
func (s *jsonlSink) readAll() ([]schemas.FeedbackEvent, error) {
	raw, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []schemas.FeedbackEvent{}, nil
		}
		return nil, docutil.Newf(docutil.CodeIO, "feedback sink %q could not be read", s.filename)
	}
	rows := []schemas.FeedbackEvent{}
	for index, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		event, err := schemas.ParseFeedbackEvent([]byte(trimmed))
		if err != nil {
			if _, ok := err.(*schemas.ValidationError); ok {
				return nil, docutil.Newf(docutil.CodeValidation,
					"feedback event refused at recorder.list line %d (fail closed): %s", index+1, err)
			}
			return nil, docutil.Newf(docutil.CodeValidation,
				"feedback sink %q line %d is not valid JSON — refusing to read (fail closed)", s.filename, index+1)
		}
		rows = append(rows, *event)
	}
	return rows, nil
}

// appendLine appends one line to the file with O_APPEND semantics (TS
// fs.appendFile): the file is only ever opened for append.
func appendLine(file, line string) error {
	handle, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := handle.WriteString(line); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

// marshalJSONLine renders one event as the JSON line the sink stores.
// HTML escaping is disabled so the bytes match the TS `JSON.stringify`
// output for identical content.
func marshalJSONLine(event *schemas.FeedbackEvent) (string, error) {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(event); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

// truncateForMessage ports the TS `String(x).slice(0, 64)` message excerpt.
func truncateForMessage(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

// containsString reports whether values contains value (TS Array.includes).
func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
