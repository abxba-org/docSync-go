package memory

// Ported from packages/core/src/memory/records.ts (P10-T21; ARCH §8.8, CTX §25,
// D1, PLAN §11.6, R12): the read/write core of the EXACTLY-FOUR
// `.docsync/memory/` files.
//
// File format (PLAN §11.6 decision: "Markdown with structured front-matter
// records, machine-parseable"): each record is an APPENDED block shaped as
//
//	---
//	<YAML front-matter: every structured field, incl. observation + action>
//	---
//	<observation prose>
//
//	<action prose>
//
// The front-matter is the SINGLE SOURCE OF TRUTH (parsed and schema-validated
// on every read); the prose body after the closing fence is a human-readable
// projection rendered at write time and never parsed back. Round-trip
// guarantee: ParseMemoryRecords of a store-written file equals the appended
// record (deep equality), verified in memory_test.go.
//
// R12 — memory cannot ossify wrong beliefs: Contradict appends a failure
// record (to failures.md, with `contradiction_of`) AND DECAYS the
// contradicted record's confidence by MemoryDecayFactor = 0.5, floored at
// MemoryConfidenceFloor = 0.05 (0.5 is an exact halving in binary floating
// point, so decay never manufactures decimals).
//
// INV-7 — memory never mutates policy: this module writes ONLY the four
// memory files; the feed surface (feeds.go) projects data outward.
//
// Write discipline: read-modify-write of a human-readable file (explicitly
// acceptable for memory), made atomic via AtomicWriteText (temp + rename,
// mirroring state/cas.go). Schema violations FAIL CLOSED (typed error,
// nothing written, D6/D8).
//
// Serialization: the YAML front-matter is emitted to be byte-identical to the
// TypeScript `yaml` package's `stringify(fields, { lineWidth: 0 })`
// (eemeli/yaml v2) over the record schema's value domain. The emitter below
// ports that package's string-style decisions: plain scalars unless an
// indicator character / `: ` / trailing-space / non-string-resolving value
// forces quoting; literal block scalars (`|-`, `|`, `|+`, explicit indent
// indicators) for multi-line prose; single-vs-double quoting per the same
// quote preference; and JS number-to-string formatting for numbers.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------- decay constants */

// MemoryConfidenceFloor: no memory record ever falls below this (a fully dead
// belief stays visible as a corpse, not a ghost — retrieval can still see it
// to avoid re-learning a refuted pattern).
const MemoryConfidenceFloor = 0.05

// MemoryDecayFactor (R12): a contradiction halves the contradicted record's
// confidence. Exact in binary floating point (halving), so repeated decay
// chains produce no float garbage. Exported + versioned here on purpose —
// never hard-code these numbers elsewhere (D11 discipline).
const MemoryDecayFactor = 0.5

/* --------------------------------------------------------------------- errors */

// EDOCSYNC_MEMORY_RECORD is the machine-readable code for
// *MemoryRecordError (TS EDOCSYNC_MEMORY_RECORD). The TS class code was not
// listed in docutil/errors.go; following the layout unit's precedent the TS
// code string is preserved verbatim as the observable identity and carried
// through a docutil coded error.
const EDOCSYNC_MEMORY_RECORD = "EDOCSYNC_MEMORY_RECORD"

// MemoryRecordError is the fail-closed refusal: a record that violates
// MEMORY_RECORD_SCHEMA, an unknown record_id in Contradict, an unparseable
// front-matter block, or prose that would corrupt the block framing (a line
// of exactly `---`).
type MemoryRecordError struct {
	inner *docutil.Error
}

// NewMemoryRecordError builds the error, preserving the TS message text.
func NewMemoryRecordError(message string) *MemoryRecordError {
	return &MemoryRecordError{inner: docutil.New(EDOCSYNC_MEMORY_RECORD, message)}
}

// Error renders the coded error (`EDOCSYNC_MEMORY_RECORD: <message>`).
func (e *MemoryRecordError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_MEMORY_RECORD.
func (e *MemoryRecordError) Code() string { return EDOCSYNC_MEMORY_RECORD }

// Message returns the bare TS error message.
func (e *MemoryRecordError) Message() string { return e.inner.Msg }

// Unwrap exposes the docutil coded error, so errors.As / docutil.CodeOf keep
// working.
func (e *MemoryRecordError) Unwrap() error { return e.inner }

/* ---------------------------------------------------------------- serialization */

// SerializeMemoryRecord serializes one record as an appended front-matter
// block: structured YAML (lineWidth 0 — no folding, so values round-trip
// byte-exactly) followed by the observation + action prose projection.
// Deterministic: same record in, same bytes out (required because decay
// rewrites re-serialize whole files). Fail-closed: prose containing a line of
// exactly `---` is refused — it would break the framing the parser keys on.
func SerializeMemoryRecord(record *schemas.MemoryRecord) (string, error) {
	for _, field := range []struct {
		name string
		text string
	}{
		{"observation", record.Observation},
		{"action", record.Action},
	} {
		for _, line := range strings.Split(field.text, "\n") {
			if line == "---" {
				return "", NewMemoryRecordError(
					field.name + " prose contains a line of exactly '---' — it would break the " +
						"front-matter block framing (fail closed, D6)")
			}
		}
	}

	var b strings.Builder
	b.WriteString("---\n")
	// Field order is the TS literal's insertion order (schema, record_id,
	// file, pattern_name, evidence_prs, confidence, unverifiable,
	// [contradiction_of], created_at, observation, action).
	emitYAMLStringPair(&b, "schema", record.Schema)
	emitYAMLStringPair(&b, "record_id", record.RecordID)
	emitYAMLStringPair(&b, "file", string(record.File))
	emitYAMLStringPair(&b, "pattern_name", record.PatternName)
	emitYAMLStringSeq(&b, "evidence_prs", record.EvidencePRs)
	b.WriteString("confidence: " + yamlNumberString(record.Confidence) + "\n")
	b.WriteString("unverifiable: " + yamlBoolString(record.Unverifiable) + "\n")
	if record.ContradictionOf != nil {
		emitYAMLStringPair(&b, "contradiction_of", *record.ContradictionOf)
	}
	emitYAMLStringPair(&b, "created_at", record.CreatedAt)
	emitYAMLStringPair(&b, "observation", record.Observation)
	emitYAMLStringPair(&b, "action", record.Action)
	b.WriteString("---\n")
	b.WriteString(record.Observation + "\n\n" + record.Action + "\n\n")
	return b.String(), nil
}

// ParseMemoryRecords parses every front-matter record from a memory file's
// raw text. The parser keys on `---` fence lines (scaffolds contain none →
// zero records). Every parsed block is schema-validated; ANY violation
// returns a *MemoryRecordError (corrupted memory fails closed — D6/D8 — it is
// never silently skipped).
func ParseMemoryRecords(raw string) ([]schemas.MemoryRecord, error) {
	lines := strings.Split(raw, "\n")
	var records []schemas.MemoryRecord
	index := 0
	for index < len(lines) {
		if lines[index] != "---" {
			index++
			continue
		}
		start := index
		end := -1
		for probe := start + 1; probe < len(lines); probe++ {
			if lines[probe] == "---" {
				end = probe
				break
			}
		}
		if end == -1 {
			return nil, NewMemoryRecordError(
				fmt.Sprintf("unterminated front-matter block starting at line %d (fail closed, D6)", start+1))
		}
		block := strings.Join(lines[start+1:end], "\n")
		// The reference parser (eemeli/yaml) treats end-of-input as ending
		// the current line, so a front-matter block that does not end in a
		// line break still yields a terminated final scalar: a `|` (clip)
		// block keeps its trailing newline and a `|+` (keep) block does not
		// gain a phantom one. yaml.v3 requires the line break to be present,
		// so the block is terminated before parsing.
		parseText := block
		if !strings.HasSuffix(parseText, "\n") {
			parseText += "\n"
		}

		// The TS module parses the YAML first (parse errors and schema
		// refusals carry distinct messages) and validates second; the same
		// two steps are kept here, with schemas.ParseMemoryRecord as the
		// strict YAML/JSON schema parser (reused, not re-declared).
		var probe any
		if err := yaml.Unmarshal([]byte(parseText), &probe); err != nil {
			return nil, NewMemoryRecordError(
				fmt.Sprintf("unparseable front-matter block at line %d: %s", start+1, err.Error()))
		}
		record, err := schemas.ParseMemoryRecord([]byte(parseText))
		if err != nil {
			return nil, NewMemoryRecordError(
				fmt.Sprintf("memory record refused (fail closed, D6) at line %d: %s", start+1, renderMemoryIssues(err)))
		}
		records = append(records, *record)
		index = end + 1
	}
	return records, nil
}

// renderMemoryIssues renders zod-style issue summaries exactly like the TS
// call sites: `path.to.field: message` joined by "; ", with `<root>` for the
// empty path.
func renderMemoryIssues(err error) string {
	var verr *schemas.ValidationError
	if errors.As(err, &verr) {
		parts := make([]string, 0, len(verr.Issues))
		for _, issue := range verr.Issues {
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

/* --------------------------------------------------------------------- shapes */

// MemoryRecordInput is a record to append; record_id and created_at are
// minted by the store.
type MemoryRecordInput struct {
	// File selects which of the EXACTLY-FOUR files the record belongs in (D1).
	File        MemoryFile
	PatternName string
	// EvidencePRs is mandatory — at least one source PR (PRIN-18).
	EvidencePRs  []string
	Observation  string
	Action       string
	Confidence   float64
	Unverifiable bool
	// ContradictionOf is set when the record contradicts an existing record (R12).
	ContradictionOf *string
}

// ParsedMemory holds one file's parsed records, keyed by the D1 stem.
type ParsedMemory struct {
	Decisions     []schemas.MemoryRecord
	KnownPatterns []schemas.MemoryRecord
	Exceptions    []schemas.MemoryRecord
	Failures      []schemas.MemoryRecord
}

// recordsFor returns the records of one file (internal; the exported fields
// carry the same data for callers).
func (p ParsedMemory) recordsFor(file MemoryFile) []schemas.MemoryRecord {
	switch file {
	case schemas.MemoryFileDecisions:
		return p.Decisions
	case schemas.MemoryFileKnownPatterns:
		return p.KnownPatterns
	case schemas.MemoryFileExceptions:
		return p.Exceptions
	case schemas.MemoryFileFailures:
		return p.Failures
	default:
		return nil
	}
}

// ContradictionInput is the R12 contradiction request.
type ContradictionInput struct {
	RecordID    string
	Observation string
	// Evidence is the evidence PRs for the contradiction (>= 1, schema-mandatory).
	Evidence []string
}

// ContradictionResult is the result of Contradict.
type ContradictionResult struct {
	// FailureRecord is the failure record appended to failures.md (with contradiction_of).
	FailureRecord *schemas.MemoryRecord
	// ContradictedFile is the file that held the contradicted record.
	ContradictedFile MemoryFile
	// PreviousConfidence is the confidence before decay.
	PreviousConfidence float64
	// DecayedConfidence is the confidence after decay: max(floor, previous * MemoryDecayFactor).
	DecayedConfidence float64
}

// MemoryStoreConfig configures a MemoryStore: where the four files live plus
// the caller's clock.
type MemoryStoreConfig struct {
	// MemoryDir is the absolute path of `.docsync/memory/`.
	MemoryDir string
	// Clock supplies timestamps — timestamps are ALWAYS caller-supplied (D6).
	Clock func() string
	// ULIDs mints record ids; nil defaults to docutil.NewULIDGenerator() (the
	// wall-clock reader — inject docutil.NewULIDGeneratorWith for tests).
	ULIDs *docutil.ULIDGenerator
}

// MemoryStore is the memory store surface (INV-7: writes ONLY the four memory
// files).
type MemoryStore struct {
	memoryDir string
	clock     func() string
	ulids     *docutil.ULIDGenerator
}

// NewMemoryStore creates the memory store. It enforces the D1 layout up front
// (create-if-missing scaffolds) and refuses to operate while extra files are
// reported (fail closed, D1); extra files are never auto-deleted.
func NewMemoryStore(config MemoryStoreConfig) (*MemoryStore, error) {
	if config.Clock == nil {
		return nil, docutil.ConfigError(
			"memory store clock is required — timestamps are always caller-supplied (D6)")
	}
	if config.MemoryDir == "" {
		return nil, docutil.ConfigError("memory store memoryDir is required")
	}
	layout, err := EnforceMemoryLayout(config.MemoryDir)
	if err != nil {
		return nil, err
	}
	if !layout.OK {
		return nil, NewMemoryLayoutError(
			fmt.Sprintf("memory layout refuses to operate under %q — extra entries reported (never auto-deleted, D1): %s",
				config.MemoryDir, strings.Join(layout.Extra, ", ")), nil)
	}
	ulids := config.ULIDs
	if ulids == nil {
		ulids = docutil.NewULIDGenerator()
	}
	return &MemoryStore{memoryDir: config.MemoryDir, clock: config.Clock, ulids: ulids}, nil
}

// MemoryDir returns the store's memory directory.
func (s *MemoryStore) MemoryDir() string { return s.memoryDir }

// AppendRecord validates + schema-parses + atomically appends one record to
// its file, returning the stored record.
func (s *MemoryStore) AppendRecord(input MemoryRecordInput) (*schemas.MemoryRecord, error) {
	recordID, err := s.ulids.Generate()
	if err != nil {
		return nil, docutil.Newf(docutil.CodeInternal,
			"memory store failed to mint a record id: %v", err)
	}
	record, err := s.appendValidated(MemoryRecordInput{
		File:            input.File,
		PatternName:     input.PatternName,
		EvidencePRs:     input.EvidencePRs,
		Observation:     input.Observation,
		Action:          input.Action,
		Confidence:      input.Confidence,
		Unverifiable:    input.Unverifiable,
		ContradictionOf: input.ContradictionOf,
	}, recordID)
	if err != nil {
		return nil, err
	}
	return record, nil
}

// appendValidated is the shared append path (also used by Contradict for the
// failure record): mint the id, schema-validate the candidate (nothing is
// written on refusal), then read-modify-write the target file atomically.
func (s *MemoryStore) appendValidated(input MemoryRecordInput, recordID string) (*schemas.MemoryRecord, error) {
	var contradictionOf *string
	if input.ContradictionOf != nil {
		value := *input.ContradictionOf
		contradictionOf = &value
	}
	candidate := schemas.MemoryRecord{
		Schema:          schemas.MemoryRecordSchemaID,
		RecordID:        recordID,
		File:            input.File,
		PatternName:     input.PatternName,
		EvidencePRs:     input.EvidencePRs,
		Observation:     input.Observation,
		Action:          input.Action,
		Confidence:      input.Confidence,
		Unverifiable:    input.Unverifiable,
		ContradictionOf: contradictionOf,
		CreatedAt:       s.clock(),
	}
	// MEMORY_RECORD_SCHEMA.safeParse: strict validation of the candidate (the
	// Go registry parser is reused; JSON is the wire form).
	wire, err := json.Marshal(&candidate)
	if err != nil {
		return nil, NewMemoryRecordError(
			"memory record refused (fail closed, nothing written, D6): " + err.Error())
	}
	parsed, err := schemas.ParseMemoryRecord(wire)
	if err != nil {
		return nil, NewMemoryRecordError(
			"memory record refused (fail closed, nothing written, D6): " + renderMemoryIssues(err))
	}

	existing, err := ReadMemoryTextFile(s.memoryDir, parsed.File)
	if err != nil {
		return nil, err
	}
	block, err := SerializeMemoryRecord(parsed)
	if err != nil {
		return nil, err
	}
	if err := AtomicWriteText(filepath.Join(s.memoryDir, MemoryFileName(parsed.File)), existing+block); err != nil {
		return nil, NewMemoryLayoutError(
			fmt.Sprintf("failed to write memory file %q", MemoryFileName(parsed.File)), err)
	}
	return parsed, nil
}

// ParseFile parses one memory file (fail-closed on a missing file or a bad
// record).
func (s *MemoryStore) ParseFile(file MemoryFile) ([]schemas.MemoryRecord, error) {
	raw, err := ReadMemoryTextFile(s.memoryDir, file)
	if err != nil {
		return nil, err
	}
	return ParseMemoryRecords(raw)
}

// ParseAll parses all EXACTLY-FOUR files, in canonical order.
func (s *MemoryStore) ParseAll() (ParsedMemory, error) {
	var out ParsedMemory
	var err error
	if out.Decisions, err = s.ParseFile(schemas.MemoryFileDecisions); err != nil {
		return ParsedMemory{}, err
	}
	if out.KnownPatterns, err = s.ParseFile(schemas.MemoryFileKnownPatterns); err != nil {
		return ParsedMemory{}, err
	}
	if out.Exceptions, err = s.ParseFile(schemas.MemoryFileExceptions); err != nil {
		return ParsedMemory{}, err
	}
	if out.Failures, err = s.ParseFile(schemas.MemoryFileFailures); err != nil {
		return ParsedMemory{}, err
	}
	return out, nil
}

// Contradict is R12: record a contradiction — append a failure record to
// failures.md AND decay the contradicted record's confidence (rewrite its
// source file). An unknown record_id fails closed.
//
// Decay happens FIRST (rewrite the source file), then the failure record is
// appended: if the append fails, the decay already took effect; if the decay
// write fails, nothing was mutated.
func (s *MemoryStore) Contradict(input ContradictionInput) (*ContradictionResult, error) {
	all, err := s.ParseAll()
	if err != nil {
		return nil, err
	}
	var source *schemas.MemoryRecord
	sourceFile := schemas.MemoryFileDecisions
	// Deterministic iteration order over the EXACTLY-FOUR files
	// (schemas.MemoryFileValues is the TS MEMORY_FILE_ORDER).
	for _, file := range schemas.MemoryFileValues {
		records := all.recordsFor(file)
		for i := range records {
			if records[i].RecordID == input.RecordID {
				source = &records[i]
				sourceFile = file
				break
			}
		}
		if source != nil {
			break
		}
	}
	if source == nil {
		return nil, NewMemoryRecordError(
			fmt.Sprintf("contradict refuses unknown record_id %q (fail closed — memory never guesses what it decays, D6)",
				input.RecordID))
	}

	previousConfidence := source.Confidence
	decayedConfidence := math.Max(MemoryConfidenceFloor, previousConfidence*MemoryDecayFactor)

	sourceRaw, err := ReadMemoryTextFile(s.memoryDir, sourceFile)
	if err != nil {
		return nil, err
	}
	existing, err := ParseMemoryRecords(sourceRaw)
	if err != nil {
		return nil, err
	}
	var rewritten strings.Builder
	for i := range existing {
		record := existing[i]
		if record.RecordID == source.RecordID {
			record.Confidence = decayedConfidence
		}
		block, err := SerializeMemoryRecord(&record)
		if err != nil {
			return nil, err
		}
		rewritten.WriteString(block)
	}
	if err := AtomicWriteText(filepath.Join(s.memoryDir, MemoryFileName(sourceFile)), rewritten.String()); err != nil {
		return nil, NewMemoryLayoutError(
			fmt.Sprintf("failed to rewrite memory file %q", MemoryFileName(sourceFile)), err)
	}

	failureID, err := s.ulids.Generate()
	if err != nil {
		return nil, docutil.Newf(docutil.CodeInternal,
			"memory store failed to mint a record id: %v", err)
	}
	failureRecord, err := s.appendValidated(MemoryRecordInput{
		File:            schemas.MemoryFileFailures,
		PatternName:     source.PatternName,
		EvidencePRs:     input.Evidence,
		Observation:     input.Observation,
		Action:          decayActionText(source.RecordID, previousConfidence, decayedConfidence),
		Confidence:      1,
		Unverifiable:    false,
		ContradictionOf: &source.RecordID,
	}, failureID)
	if err != nil {
		return nil, err
	}
	return &ContradictionResult{
		FailureRecord:      failureRecord,
		ContradictedFile:   sourceFile,
		PreviousConfidence: previousConfidence,
		DecayedConfidence:  decayedConfidence,
	}, nil
}

// decayActionText renders the R12 decay note exactly like the TS template
// literal (JS number-to-string for every interpolated number).
func decayActionText(recordID string, previous, decayed float64) string {
	return fmt.Sprintf(
		"decayed confidence of record %s from %s to %s (R12: memory cannot ossify wrong beliefs; decay factor %s, floor %s)",
		recordID, yamlNumberString(previous), yamlNumberString(decayed),
		yamlNumberString(MemoryDecayFactor), yamlNumberString(MemoryConfidenceFloor))
}

/* -------------------------------------------------- YAML emitter (lineWidth 0) */

// yamlScalarCtx carries the eemeli stringify context bits that matter for the
// emitter: indent is the indentation continuation lines of a scalar carry
// (ctx.indent — for map values it is the key indent plus one step), and
// implicitKey marks key position.
type yamlScalarCtx struct {
	indent      string
	implicitKey bool
}

// emitYAMLStringPair writes `key: <scalar>` for a top-level map entry (the
// value indent is the root map's one indent step).
func emitYAMLStringPair(b *strings.Builder, key, value string) {
	b.WriteString(key + ": " + yamlString(value, yamlScalarCtx{indent: "  "}) + "\n")
}

// emitYAMLStringSeq writes the block sequence form (`key:` then `  - item`
// lines); an empty sequence takes the flow form eemeli emits for empty
// collections.
func emitYAMLStringSeq(b *strings.Builder, key string, items []string) {
	if len(items) == 0 {
		b.WriteString(key + ": []\n")
		return
	}
	b.WriteString(key + ":\n")
	for _, item := range items {
		b.WriteString("  - " + yamlString(item, yamlScalarCtx{indent: "    "}) + "\n")
	}
}

// yamlBoolString renders a boolean (trueStr/falseStr).
func yamlBoolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// yamlNumberString renders a float64 exactly like eemeli's stringifyNumber,
// which is JSON.stringify (JS Number::toString) with the -0 special case.
func yamlNumberString(v float64) string {
	if v == 0 {
		if math.Signbit(v) {
			return "-0"
		}
		return "0"
	}
	negative := math.Signbit(v)
	digits, exp := shortestDigits(v)
	n := exp + 1 // value = 0.digits × 10^n
	k := len(digits)
	var out string
	switch {
	case k <= n && n <= 21:
		out = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		out = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		out = "0." + strings.Repeat("0", -n) + digits
	default:
		e := n - 1
		sign := "+"
		if e < 0 {
			sign = "-"
			e = -e
		}
		mantissa := digits[:1]
		if k > 1 {
			mantissa += "." + digits[1:]
		}
		out = mantissa + "e" + sign + strconv.Itoa(e)
	}
	if negative {
		return "-" + out
	}
	return out
}

// shortestDigits returns the shortest round-trip decimal digits of v (no
// sign, no dot) and the base-10 exponent e with v = 0.digits × 10^e.
func shortestDigits(v float64) (string, int) {
	es := strconv.FormatFloat(v, 'e', -1, 64)
	e := strings.IndexByte(es, 'e')
	mantissa := es[:e]
	exp, _ := strconv.Atoi(es[e+1:])
	mantissa = strings.Replace(mantissa, ".", "", 1)
	mantissa = strings.TrimPrefix(mantissa, "-")
	return mantissa, exp
}

// yamlString ports stringifyString for our inputs (plain JS strings, no
// explicit scalar type): control characters force double quotes, everything
// else takes the PLAIN default path.
func yamlString(value string, ctx yamlScalarCtx) string {
	if yamlHasControlChars(value) {
		return yamlDoubleQuoted(value, ctx)
	}
	return yamlPlainString(value, ctx)
}

// yamlPlainString ports plainString. The inFlow branches of the reference
// (implicitKey-with-newline is kept; inFlow never applies — this emitter only
// writes block-context map values and block sequence items, never flow) are
// omitted accordingly.
func yamlPlainString(value string, ctx yamlScalarCtx) string {
	if ctx.implicitKey && strings.Contains(value, "\n") {
		return yamlQuotedString(value, ctx)
	}
	if yamlPlainForbiddenRe.MatchString(value) {
		if ctx.implicitKey || !strings.Contains(value, "\n") {
			return yamlQuotedString(value, ctx)
		}
		return yamlBlockString(value, ctx)
	}
	if !ctx.implicitKey && strings.Contains(value, "\n") {
		// Where allowed & type not set explicitly, prefer block style for
		// multiline strings.
		return yamlBlockString(value, ctx)
	}
	if yamlContainsDocumentMarker(value) {
		if ctx.indent == "" {
			// Root-level document markers force an indented block so the
			// emitted document stays single-document.
			return yamlBlockString(value, yamlScalarCtx{indent: "  "})
		}
		// (implicitKey && indent === indentStep → quoted: unreachable for
		// map values, which always carry a non-empty indent here.)
	}
	str := yamlIndentNewlineRunsOpen(value, ctx.indent)
	// Verify the output parses back as a string: values that a YAML 1.2 core
	// schema resolves as null/bool/int/float must be quoted.
	if yamlResolvesToNonString(str) {
		return yamlQuotedString(value, ctx)
	}
	return str
}

// yamlQuotedString ports quotedString (singleQuote unset ⇒ double by default;
// a value with only double quotes prefers single quotes and vice versa).
func yamlQuotedString(value string, ctx yamlScalarCtx) string {
	hasDouble := strings.Contains(value, "\"")
	hasSingle := strings.Contains(value, "'")
	if hasDouble && !hasSingle {
		return yamlSingleQuoted(value, ctx)
	}
	if hasSingle && !hasDouble {
		return yamlDoubleQuoted(value, ctx)
	}
	return yamlDoubleQuoted(value, ctx)
}

// yamlSingleQuoted ports singleQuotedString.
func yamlSingleQuoted(value string, ctx yamlScalarCtx) string {
	if yamlWhitespaceAroundNewlineRe.MatchString(value) {
		// A single-quoted string can't have leading or trailing whitespace
		// around a newline.
		return yamlDoubleQuoted(value, ctx)
	}
	indent := ctx.indent
	if indent == "" && yamlContainsDocumentMarker(value) {
		indent = "  "
	}
	escaped := strings.ReplaceAll(value, "'", "''")
	return "'" + yamlIndentNewlineRunsOpen(escaped, indent) + "'"
}

// yamlDoubleQuoted ports doubleQuotedString: JSON.stringify's output with
// eemeli's escape rewrites and (only for values ≥ 40 chars) newline folding
// into real newlines. foldFlowLines is a no-op at lineWidth 0.
func yamlDoubleQuoted(value string, ctx yamlScalarCtx) string {
	json := jsonStringifyString(value)
	const minMultiLineLength = 40
	indent := ctx.indent
	if indent == "" && yamlContainsDocumentMarker(value) {
		indent = "  "
	}
	var b strings.Builder
	start := 0
	for i := 0; i < len(json); i++ {
		ch := json[i]
		if ch == ' ' && i+2 < len(json) && json[i+1] == '\\' && json[i+2] == 'n' {
			// A space before a newline needs to be escaped to not be folded.
			b.WriteString(json[start:i])
			b.WriteString(`\ `)
			i++
			start = i
			ch = json[i]
		}
		if ch != '\\' {
			continue
		}
		switch json[i+1] {
		case 'u':
			b.WriteString(json[start:i])
			code := json[i+2 : i+6]
			switch code {
			case "0000":
				b.WriteString(`\0`)
			case "0007":
				b.WriteString(`\a`)
			case "000b":
				b.WriteString(`\v`)
			case "001b":
				b.WriteString(`\e`)
			case "0085":
				b.WriteString(`\N`)
			case "00a0":
				b.WriteString(`\_`)
			case "2028":
				b.WriteString(`\L`)
			case "2029":
				b.WriteString(`\P`)
			default:
				if strings.HasPrefix(code, "00") {
					b.WriteString(`\x` + code[2:])
				} else {
					b.WriteString(json[i : i+6])
				}
			}
			i += 5
			start = i + 1
		case 'n':
			if ctx.implicitKey || (i+2 < len(json) && json[i+2] == '"') || len(json) < minMultiLineLength {
				i++
				continue
			}
			// Folding will eat the first newline.
			b.WriteString(json[start:i])
			b.WriteString("\n\n")
			for i+2 < len(json) && json[i+2] == '\\' && json[i+3] == 'n' &&
				!(i+4 < len(json) && json[i+4] == '"') {
				b.WriteString("\n")
				i += 2
			}
			b.WriteString(indent)
			// A space after the newline needs to be escaped to not be folded.
			if i+2 < len(json) && json[i+2] == ' ' {
				b.WriteString(`\`)
			}
			i++
			start = i + 1
		default:
			i++
		}
	}
	b.WriteString(json[start:])
	return b.String()
}

// jsonStringifyString renders a Go string exactly like JavaScript's
// JSON.stringify: short escapes for the named controls, \u00XX for the rest,
// and raw UTF-8 for everything else (no HTML escaping, no U+2028/U+2029
// escaping).
func jsonStringifyString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(`\u`)
				const hexDigits = "0123456789abcdef"
				b.WriteByte(hexDigits[(r>>12)&0xf])
				b.WriteByte(hexDigits[(r>>8)&0xf])
				b.WriteByte(hexDigits[(r>>4)&0xf])
				b.WriteByte(hexDigits[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// yamlBlockString ports blockString for the literal case (blockQuote is on
// and lineWidth is 0, so the folded branch and its literal fallback never
// apply).
func yamlBlockString(value string, ctx yamlScalarCtx) string {
	if yamlEndsWithNewlineWhitespace(value) {
		// A block can't end in whitespace unless the last line is non-empty.
		return yamlQuotedString(value, ctx)
	}
	indent := ctx.indent
	if indent == "" && (yamlContainsDocumentMarker(value)) {
		indent = "  "
	}

	// Chomping from the whitespace at the value end.
	endStart := len(value)
	for endStart > 0 {
		ch := value[endStart-1]
		if ch != '\n' && ch != '\t' && ch != ' ' {
			break
		}
		endStart--
	}
	end := value[endStart:]
	chomp := "-"
	endNlPos := strings.IndexByte(end, '\n')
	switch {
	case endNlPos == -1:
		chomp = "-" // strip
	case len(value) == len(end) || endNlPos != len(end)-1:
		chomp = "+" // keep
	default:
		chomp = "" // clip
	}
	if end != "" {
		value = value[:len(value)-len(end)]
		if end[len(end)-1] == '\n' {
			end = end[:len(end)-1]
		}
		end = yamlIndentTrailingNewlineRuns(end, indent)
	}

	// Indent indicator from the whitespace at the value start.
	startWithSpace := false
	startNlPos := -1
	startEnd := 0
	for startEnd = 0; startEnd < len(value); startEnd++ {
		ch := value[startEnd]
		if ch == ' ' {
			startWithSpace = true
		} else if ch == '\n' {
			startNlPos = startEnd
		} else {
			break
		}
	}
	startLen := startEnd
	if startNlPos < startEnd {
		startLen = startNlPos + 1
	}
	start := value[:startLen]
	if start != "" {
		value = value[len(start):]
		start = yamlIndentNewlineRuns(start, indent)
	}
	indentSize := "1"
	if indent != "" {
		indentSize = "2"
	}
	header := ""
	if startWithSpace {
		header += indentSize
	}
	header += chomp
	value = yamlIndentNewlineRuns(value, indent)
	return "|" + header + "\n" + indent + start + value + end
}

// yamlIndentNewlineRuns mirrors value.replace(/\n+/g, `$&${indent}`): every
// maximal newline run is followed by the indent (block-scalar bodies and the
// leading newline run).
func yamlIndentNewlineRuns(s, indent string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(indent))
	for i := 0; i < len(s); {
		if s[i] != '\n' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '\n' {
			j++
		}
		b.WriteString(s[i:j])
		b.WriteString(indent)
		i = j
	}
	return b.String()
}

// yamlIndentNewlineRunsOpen mirrors value.replace(/\n+/g, `$&\n${indent}`):
// every maximal newline run is followed by a newline plus the indent (plain
// and single-quoted scalars fold that newline back to a space on parse).
func yamlIndentNewlineRunsOpen(s, indent string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(indent))
	for i := 0; i < len(s); {
		if s[i] != '\n' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '\n' {
			j++
		}
		b.WriteString(s[i:j])
		b.WriteByte('\n')
		b.WriteString(indent)
		i = j
	}
	return b.String()
}

// yamlIndentTrailingNewlineRuns mirrors the blockEndNewlines replacement
// ((^|(?<!\n))\n+(?!\n|$)): maximal newline runs NOT at end-of-string are
// followed by the indent.
func yamlIndentTrailingNewlineRuns(s, indent string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(indent))
	for i := 0; i < len(s); {
		if s[i] != '\n' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '\n' {
			j++
		}
		b.WriteString(s[i:j])
		if j < len(s) {
			b.WriteString(indent)
		}
		i = j
	}
	return b.String()
}

var (
	// yamlControlChars mirrors /[\x00-\x08\x0b-\x1f\x7f-\x9f]/u (lone
	// surrogates cannot appear in valid Go strings).
	yamlControlChars = regexp.MustCompile(`[\x00-\x08\x0b-\x1f\x7f-\x9f]`)
	// yamlPlainForbidden mirrors the plainString indicator test:
	//   ^[\n\t ,[\]{}#&*!|>'"%@`]  start with an indicator character
	//   ^[?-]$                      exactly '-' or '?'
	//   ^[?-][ \t]                  start with '-'/'?' followed by space/tab
	//   [\n:][ \t]                  '\n '/': ' anywhere
	//   [ \t]\n                     space/tab before a newline
	//   [\n\t ]#                    '#' not preceded by a non-space char
	//   [\n\t :]$                   end with ' ', '\t', '\n' or ':'
	yamlPlainForbiddenRe = regexp.MustCompile("^[\n\t ,\\[\\]{}#&*!|>'\"%@`]|^[-?]$|^[-?][ \t]|[\n:][ \t]|[ \t]\n|[\n\t ]#|[\n\t :]$")
	// yamlFlowIndicator mirrors the inFlow test /[[\]{},]/ (never inFlow for
	// map values and block sequences, kept for parity of the code path).
	yamlFlowIndicator = regexp.MustCompile(`[[\]{},]`)
	// yamlDocumentMarker mirrors /^(%|---|\.\.\.)/m.
	yamlDocumentMarker = regexp.MustCompile(`(?m)^(%|---|\.\.\.)`)
	// yamlNewlineWhitespaceEnd mirrors /\n[\t ]+$/.
	yamlNewlineWhitespaceEnd = regexp.MustCompile(`\n[\t ]+$`)
	// yamlWhitespaceAroundNewline mirrors /[ \t]\n|\n[ \t]/.
	yamlWhitespaceAroundNewlineRe = regexp.MustCompile(`[ \t]\n|\n[ \t]`)

	// The core-schema scalar tests (yaml 1.2): a plain string that any of
	// these matches would be parsed as null/bool/int/float, so it must be
	// quoted instead.
	yamlNullTest  = regexp.MustCompile(`^(?:~|[Nn]ull|NULL)?$`)
	yamlBoolTest  = regexp.MustCompile(`^(?:[Tt]rue|TRUE|[Ff]alse|FALSE)$`)
	yamlIntOct    = regexp.MustCompile(`^0o[0-7]+$`)
	yamlIntTest   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	yamlIntHex    = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	yamlFloatNaN  = regexp.MustCompile(`^(?:[-+]?\.(?:inf|Inf|INF)|\.nan|\.NaN|\.NAN)$`)
	yamlFloatExp  = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)[eE][-+]?[0-9]+$`)
	yamlFloatTest = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+\.[0-9]*)$`)
)

func yamlHasControlChars(v string) bool { return yamlControlChars.MatchString(v) }

func yamlContainsDocumentMarker(v string) bool { return yamlDocumentMarker.MatchString(v) }

func yamlEndsWithNewlineWhitespace(v string) bool { return yamlNewlineWhitespaceEnd.MatchString(v) }

func yamlWhitespaceAroundNewlineValue(v string) bool {
	return yamlWhitespaceAroundNewlineRe.MatchString(v)
}

// yamlResolvesToNonString reports whether a plain scalar would resolve to a
// non-string type under the YAML 1.2 core schema (the actualString check).
func yamlResolvesToNonString(str string) bool {
	return yamlNullTest.MatchString(str) ||
		yamlBoolTest.MatchString(str) ||
		yamlIntOct.MatchString(str) ||
		yamlIntTest.MatchString(str) ||
		yamlIntHex.MatchString(str) ||
		yamlFloatNaN.MatchString(str) ||
		yamlFloatExp.MatchString(str) ||
		yamlFloatTest.MatchString(str)
}
