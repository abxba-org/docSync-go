// Append-only JSONL sinks for Phase 10 feedback rows (P10-T02; ARCH §8.7;
// OD-R22).
//
// Ported from core/src/feedback/sink.ts. Feedback RAW rows (events) and
// hygiene labels are system-written append-only rows that live under
// `.docsync/state/feedback/` — the same deliberate exception to the
// registry-validated StateStore CAS path as the safety-incident log: the CAS
// write path is read → validate → REWRITE-whole-file, which is incompatible
// with append-only semantics and would lose feedback exactly when the failure
// is in state infrastructure itself. OD-R22 (adopted recommendation): RAW
// system-written rows under `state/feedback/`, curated artifacts under
// `evaluation/`.
//
// Validation is NOT waived: every row is parsed against its registered schema
// BEFORE the write (fail closed — malformed evidence is refused, not
// persisted), and every row is re-validated on read (fail closed — a corrupt
// line is a hard error, never "skip it"). Appends are O_APPEND — the file is
// never truncated or rewritten; a sink failure is retried exactly once and
// then fails LOUD via FeedbackSinkError (a feedback row that cannot be
// recorded is itself a lost-evaluation-data incident and is never dropped
// silently).
//
// No wall clock, no ULID generation here — ids and timestamps are supplied by
// the callers (injected clock; never new Date()).

package feedback

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
)

// EDOCSYNC_FEEDBACK_SINK is the machine-readable code of FeedbackSinkError
// (TS FeedbackSinkError.code). docutil/errors.go has no feedback-specific
// code; following the layout/globs/statemachine precedent the TS code string
// is preserved verbatim as the observable identity and carried through a
// docutil coded error (the closest listed generic, docutil.CodeIO, would drop
// the observable EDOCSYNC_FEEDBACK_SINK code).
const EDOCSYNC_FEEDBACK_SINK = "EDOCSYNC_FEEDBACK_SINK"

// EDOCSYNC_FEEDBACK_INVALID is the machine-readable code of
// FeedbackValidationError (TS FeedbackValidationError.code) — the fail-closed
// refusal to persist or read a row that fails schema validation.
const EDOCSYNC_FEEDBACK_INVALID = "EDOCSYNC_FEEDBACK_INVALID"

// FeedbackSinkError means the feedback sink could not persist a validated row
// even after one retry — fail loud: feedback that cannot be recorded must
// surface, never vanish (TS FeedbackSinkError).
type FeedbackSinkError struct {
	// Message is the exact TS message text.
	Message string
	// Cause is the underlying failure (TS `cause`), nil when none.
	Cause error

	inner *docutil.Error
}

func newFeedbackSinkError(message string, cause error) *FeedbackSinkError {
	inner := docutil.New(EDOCSYNC_FEEDBACK_SINK, message)
	if cause != nil {
		inner = inner.Wrap(cause)
	}
	return &FeedbackSinkError{Message: message, Cause: cause, inner: inner}
}

// Error renders the coded error (`EDOCSYNC_FEEDBACK_SINK: <message>`).
func (e *FeedbackSinkError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_FEEDBACK_SINK.
func (e *FeedbackSinkError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error (and,
// through it, the wrapped cause).
func (e *FeedbackSinkError) Unwrap() error { return e.inner }

// FeedbackValidationError is the fail-closed refusal to persist or read a row
// that fails schema validation (TS FeedbackValidationError).
type FeedbackValidationError struct {
	// Message is the exact TS message text.
	Message string

	inner *docutil.Error
}

func newFeedbackValidationError(message string) *FeedbackValidationError {
	return &FeedbackValidationError{Message: message, inner: docutil.New(EDOCSYNC_FEEDBACK_INVALID, message)}
}

// Error renders the coded error (`EDOCSYNC_FEEDBACK_INVALID: <message>`).
func (e *FeedbackValidationError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_FEEDBACK_INVALID.
func (e *FeedbackValidationError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *FeedbackValidationError) Unwrap() error { return e.inner }

// FEEDBACK_DIR_NAME is the subdirectory under the state dir holding RAW
// feedback rows (OD-R22; TS FEEDBACK_DIR_NAME).
const FEEDBACK_DIR_NAME = "feedback"

// FEEDBACK_EVENTS_FILENAME is the RAW feedback events path, relative to the
// state dir (OD-R22; TS FEEDBACK_EVENTS_FILENAME):
// `<stateDir>/feedback/events.jsonl`.
const FEEDBACK_EVENTS_FILENAME = "feedback/events.jsonl"

// FEEDBACK_LABELS_FILENAME is the hygiene labels path, relative to the state
// dir (OD-R22; TS FEEDBACK_LABELS_FILENAME): `<stateDir>/feedback/labels.jsonl`.
const FEEDBACK_LABELS_FILENAME = "feedback/labels.jsonl"

// JsonlSink is the port: append-only persistence + fail-closed replay for one
// JSONL file (TS JsonlSink).
type JsonlSink interface {
	// Append writes one already-validated value as a JSON line.
	Append(value any) error
	// ReadAll replays every row, validating each line with the injected parse
	// function. A missing file is the soft "nothing recorded yet" outcome
	// ([]); a present-but-malformed line fails (fail closed).
	ReadAll(parse func(value any, lineNumber int) (any, error)) ([]any, error)
}

// RowParser validates one decoded JSONL row into T (the TS `parse` callback of
// JsonlSink.readAll<T>).
type RowParser[T any] func(value any, lineNumber int) (T, error)

// ReadAllRows replays every row of sink into a typed slice — the Go form of
// the TS generic `sink.readAll<T>(parse)`, since Go interface methods cannot
// be generic.
func ReadAllRows[T any](sink JsonlSink, parse RowParser[T]) ([]T, error) {
	rows, err := sink.ReadAll(func(value any, lineNumber int) (any, error) {
		return parse(value, lineNumber)
	})
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		typed, ok := row.(T)
		if !ok {
			return nil, docutil.Newf(docutil.CodeInternal,
				"feedback sink row parser returned %T; expected %T", row, *new(T))
		}
		out = append(out, typed)
	}
	return out, nil
}

// jsonlFileSink is the append-only JSONL file sink under
// `<stateDir>/<filename>` (TS createJsonlSink).
type jsonlFileSink struct {
	stateDir string
	filename string
	file     string
}

// CreateJsonlSink builds the append-only JSONL file sink under
// `<stateDir>/<filename>`; it fails closed on a non-absolute state dir or an
// escaping filename (TS createJsonlSink's assertAbsoluteStateDir /
// toFileAbsPath).
func CreateJsonlSink(stateDir, filename string) (JsonlSink, error) {
	if len(stateDir) == 0 || !filepath.IsAbs(stateDir) {
		return nil, newFeedbackSinkError(fmt.Sprintf(
			"createJsonlSink: stateDir must be an absolute path (got %s)",
			strconv.Quote(truncateForMessage(stateDir, 64))), nil)
	}
	if len(filename) == 0 || filepath.IsAbs(filename) || strings.Contains(filename, "..") {
		return nil, newFeedbackSinkError(fmt.Sprintf(
			"feedback sink filename must be a state-dir-relative path (got %s)",
			strconv.Quote(truncateForMessage(filename, 64))), nil)
	}
	return &jsonlFileSink{
		stateDir: stateDir,
		filename: filename,
		file:     filepath.Join(stateDir, filename),
	}, nil
}

// Append writes one already-validated value as a JSON line (TS
// JsonlSink.append). The parent directory is created on first write
// (idempotent mkdir); the file is only ever opened for append — never
// truncated, never rewritten. One retry on failure, then fail LOUD
// (unrecordable feedback is lost evaluation data and must surface, same
// discipline as incidents).
func (s *jsonlFileSink) Append(value any) error {
	line, err := marshalRow(value)
	if err != nil {
		return newFeedbackSinkError(fmt.Sprintf("feedback row could not be serialized: %v", err), err)
	}
	// First attempt: mkdir (idempotent) + append.
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err == nil {
		if appendErr := appendLine(s.file, line); appendErr == nil {
			return nil
		}
	}
	// Exactly one retry (append only), then fail LOUD.
	if appendErr := appendLine(s.file, line); appendErr != nil {
		return newFeedbackSinkError(fmt.Sprintf(
			"feedback row could not be appended to %q after one retry: %v", s.filename, appendErr), appendErr)
	}
	return nil
}

// ReadAll replays every row, validating each line (fail closed — a corrupt
// line is a hard error, never skipped; TS JsonlSink.readAll).
func (s *jsonlFileSink) ReadAll(parse func(value any, lineNumber int) (any, error)) ([]any, error) {
	raw, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []any{}, nil
		}
		return nil, newFeedbackSinkError(fmt.Sprintf("feedback sink %q could not be read", s.filename), err)
	}
	rows := []any{}
	for index, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return nil, newFeedbackValidationError(fmt.Sprintf(
				"feedback sink %q line %d is not valid JSON — refusing to read (fail closed)", s.filename, index+1))
		}
		row, err := parse(parsed, index+1)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
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

// marshalRow renders one value as the JSON line the sink stores
// (TS `JSON.stringify(value)` + "\n"). Rows are written in the declared field
// order — exactly the TS object-literal insertion order — so the byte stream
// stays identical to the TypeScript implementation's; HTML escaping is
// disabled for the same reason. Canonical (sorted-key) serialization is
// deliberately NOT used here: the file is append-only wire data, not a CAS
// state file.
func marshalRow(value any) (string, error) {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

// idGenerator is the package-level canonical-ULID source for the default ids
// this module mints (`label_id`, `proposal_id`; TS module-global
// `generateUlid`). docutil.ULIDGenerator is mutex-guarded and monotonic within
// a millisecond, so concurrent callers stay ordered.
var idGenerator = docutil.NewULIDGenerator()

// generateID mints one default id of the named kind (TS generateUlid).
func generateID(kind string) (string, error) {
	id, err := idGenerator.Generate()
	if err != nil {
		return "", docutil.Newf(docutil.CodeInternal, "%s id generation failed: %v", kind, err)
	}
	return id, nil
}

// marshalRowCompact is marshalRow without the trailing newline — the
// JSON.stringify form callers hand back to a strict schema parser.
func marshalRowCompact(value any) ([]byte, error) {
	line, err := marshalRow(value)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(line, "\n")), nil
}

// truncateForMessage ports the TS `String(x).slice(0, 64)` message excerpt
// (character-based, as the TS string slice is).
func truncateForMessage(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
