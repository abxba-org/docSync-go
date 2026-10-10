// Ported from core/src/audit/incremental.ts (P11-T19; ARCH §7.2 + §6.3).
// READ-MOSTLY orchestration: chunked per-document drift audit over the
// manifest allowlist (D7 — manifest-scoped, NO blanket scanning), incremental
// since `state/audit-cursor`, findings deduplicated by claim ID, cursor
// advanced ONLY after a chunk completes fully — crash-safe (a crash between
// chunks leaves the cursor at the last completed chunk; the unfinished chunk
// is re-audited on the next run and repeated claim-ID findings collapse, so
// the re-audit is idempotent).
//
// The engine performs NO document writes and NO patch application ever
// (ARCH §7.2): findings are advisory DATA. Fail closed: a cursor read,
// manifest or position port failure degrades to a typed `abstained` report
// with detail — never a silent skip. The cursor is written CAS-guarded through
// AuditCursorPort.WriteAuditCursor; a CAS loser is reported as
// `re-run-required` and NEVER blind-writes (R15, ARCH §6.3).
//
// Chunking + concurrency mirror the orchestrator's bounded in-process worker
// pool (P5-T12/OD-R10 pattern in orchestrator/run.ts — that pool is not
// exported, so this module mirrors it minimally): one chunk = one document,
// workers capped at AuditRunOptions.Concurrency, deterministic chunk order
// (sorted manifest doc list), and the cursor advances over the contiguous
// prefix of completed chunks only. Cursor advancement is serialized through a
// single FIFO advance goroutine — the Go twin of the TS promise chain — so a
// concurrent-writer CAS loser is reported exactly once, deterministically.
//
// Injectable ports (wiring is a W5b2 surface — nobody implements them yet):
// manifest doc list, current position, per-document drift predicates, and the
// audit-cursor store (satisfied by the real StateStore's
// ReadAuditCursor/WriteAuditCursor pair).
package audit

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* -------------------------------------------------- cursor position codec */

// PositionPrefix is the owner prefix of the audit-cursor `position` format
// this module defines (TS POSITION_PREFIX).
const PositionPrefix = "loopb:v1"

// ParsedAuditPosition is the decoded cursor position.
type ParsedAuditPosition struct {
	// Since is the repository position this run's audit is anchored "since".
	Since string
	// Done is the number of manifest docs (in sorted chunk order) fully
	// audited.
	Done int
	// ListHash is the short hash of the audited doc list — a changed manifest
	// restarts at 0.
	ListHash string
}

// uriComponentUnreserved is the character set JS `encodeURIComponent` leaves
// untouched (RFC 2396 unreserved plus the reserved-but-legal mark set).
const uriComponentUnreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"

const upperHex = "0123456789ABCDEF"

// encodeURIComponent ports JS `encodeURIComponent`: every byte outside the
// unreserved set is percent-encoded with UPPERCASE hex (multi-byte UTF-8
// sequences are byte-escaped, exactly as JS does after UTF-8 encoding).
func encodeURIComponent(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if strings.IndexByte(uriComponentUnreserved, c) >= 0 {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(upperHex[c>>4])
		out.WriteByte(upperHex[c&0x0f])
	}
	return out.String()
}

// decodeURIComponent ports JS `decodeURIComponent` (%XX sequences, UTF-8
// multi-byte aware, '+' left literal). Malformed escapes are an error — the
// caller refuses the position, exactly as the TS `catch { return null }` did.
func decodeURIComponent(value string) (string, error) {
	return url.PathUnescape(value)
}

// EncodeAuditPosition encodes the cursor position for the registered
// `docsync.audit-cursor` schema (`position` is a free nonempty string).
// `since` is percent-encoded so the `;`/`=` delimiters stay unambiguous.
func EncodeAuditPosition(since string, done int, listHash string) string {
	return fmt.Sprintf("%s;since=%s;done=%d;list=%s",
		PositionPrefix, encodeURIComponent(since), done, listHash)
}

// jsInteger ports the TS `Number(value)` acceptance used by the `done`
// segment: an empty/blank string parses as 0, a non-integral or malformed
// value is refused.
func jsInteger(value string) (int, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, true
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err == nil {
		return int(parsed), true
	}
	// JS Number() additionally accepts decimal/exponent forms; the encoder
	// only ever writes decimal integers, so non-integral floats are refused.
	floated, floatErr := strconv.ParseFloat(trimmed, 64)
	if floatErr != nil || floated != math.Trunc(floated) {
		return 0, false
	}
	return int(floated), true
}

// ParseAuditPosition parses EncodeAuditPosition output; nil for foreign
// formats (TS: `null`).
func ParseAuditPosition(position string) *ParsedAuditPosition {
	segments := strings.Split(position, ";")
	if segments[0] != PositionPrefix {
		return nil
	}
	var since *string
	var done *int
	var listHash *string
	for _, segment := range segments[1:] {
		eq := strings.IndexByte(segment, '=')
		if eq < 0 {
			return nil
		}
		key := segment[:eq]
		value := segment[eq+1:]
		switch key {
		case "since":
			decoded, err := decodeURIComponent(value)
			if err != nil {
				return nil
			}
			since = &decoded
		case "done":
			parsed, ok := jsInteger(value)
			if !ok || parsed < 0 {
				return nil
			}
			done = &parsed
		case "list":
			listHash = &value
		default:
			return nil
		}
	}
	if since == nil || done == nil || listHash == nil || *listHash == "" {
		return nil
	}
	return &ParsedAuditPosition{Since: *since, Done: *done, ListHash: *listHash}
}

// ChunkListHash is the stable short hash of the sorted chunk list — resume
// only matches identical lists (TS `contentHash(docs.join('\n')).slice(0,12)`).
func ChunkListHash(docs []string) string {
	joined := strings.Join(docs, "\n")
	hash := docutil.ContentHash(joined)
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

/* ------------------------------------------------------------- the engine */

// PortFindingInvalidError is the typed chunk failure raised when the audit
// port returns a DriftFinding that fails the local fail-closed schema
// (types.ts DRIFT_FINDING_SCHEMA): schema drift is a CHUNK FAILURE, never a
// silently-dropped record. The message preserves the TS text exactly; it is
// folded into the run result's typed detail and never escapes the package.
type PortFindingInvalidError struct {
	Message string
}

func (e *PortFindingInvalidError) Error() string { return e.Message }

// describeError renders a port error's message (TS `error instanceof Error ?
// error.message : String(error)`).
func describeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// chunkOutcome is one chunk's terminal state. `done` distinguishes a
// completed chunk (the TS `Array.isArray(results[i])` test) from a chunk that
// was never started; `failed` marks the chunk-failure variant
// (TS `{ error }`).
type chunkOutcome struct {
	done     bool
	failed   bool
	errMsg   string
	findings []DriftFinding
}

// casFailure is the recorded cursor-write failure (a store error or a CAS
// conflict) that stops advancement and reports re-run-required.
type casFailure struct {
	expectedHash *string
	actualHash   *string
	detail       string
}

// validatePortFindings validates every port-produced finding against the
// local fail-closed schema; the first failure refuses the whole chunk.
func validatePortFindings(docPath string, produced []DriftFinding) ([]DriftFinding, error) {
	validated := make([]DriftFinding, 0, len(produced))
	for index, finding := range produced {
		issues := ValidateFinding(finding)
		if len(issues) > 0 {
			return nil, &PortFindingInvalidError{Message: fmt.Sprintf(
				"audit port returned an invalid DriftFinding for %q (index %d): %s — refusing (fail closed)",
				docPath, index, firstIssueMessage(issues))}
		}
		validated = append(validated, finding)
	}
	return validated, nil
}

// abstain is the fail-closed typed degradation (never a silent skip).
func abstain(detail string) AuditRunResult {
	return AuditRunResult{
		Status:      AuditRunAbstained,
		Findings:    []DriftFinding{},
		AuditedDocs: []string{},
		SkippedDocs: []string{},
		Cursor:      AuditCursorState{Advanced: false, Position: nil},
		Detail:      &detail,
	}
}

// RunIncrementalAudit runs one incremental Loop B audit pass. See the module
// doc for the crash-safety, dedup and CAS semantics; see AuditRunResult for
// outcomes. The engine never throws: port failures are typed results.
func RunIncrementalAudit(ctx context.Context, deps IncrementalAuditDeps, options AuditRunOptions) AuditRunResult {
	/* Fail-closed reads: any port failure degrades to a typed abstain report. */
	previousRead, err := deps.Cursor.ReadAuditCursor()
	if err != nil {
		return abstain("audit-cursor read failed — refusing to audit (fail closed): " + describeError(err))
	}
	manifestDocs, err := deps.ManifestDocs(ctx)
	if err != nil {
		return abstain("manifest scope read failed — refusing to audit (fail closed, D7): " + describeError(err))
	}
	docs := uniqueSorted(manifestDocs)
	if len(docs) == 0 {
		return abstain("manifest doc list is empty — nothing is in the D7 allowlist scope to audit")
	}
	position, err := deps.Position(ctx)
	if err != nil {
		return abstain("audit position resolution failed — refusing to audit (fail closed): " + describeError(err))
	}

	/* Resume state: a matching cursor (same chunk list) resumes past completed
	 * chunks; a foreign-format or changed-manifest cursor restarts at 0 (still
	 * idempotent — claim-ID dedup collapses repeats). */
	listHash := ChunkListHash(docs)
	var prevCursor *schemas.AuditCursor
	if previousRead.Exists {
		prevCursor = previousRead.Content
	}
	var parsed *ParsedAuditPosition
	if prevCursor != nil {
		parsed = ParseAuditPosition(prevCursor.Position)
	}
	resumeDone := 0
	if parsed != nil && parsed.ListHash == listHash {
		resumeDone = parsed.Done
		if resumeDone < 0 {
			resumeDone = 0
		}
		if resumeDone > len(docs) {
			resumeDone = len(docs)
		}
	}
	sinceUsed := position
	if parsed != nil {
		sinceUsed = parsed.Since
	} else if prevCursor != nil {
		sinceUsed = prevCursor.Position
	}

	work := docs[resumeDone:]
	skipped := docs[:resumeDone]

	/* Chunk state (one chunk = one document), guarded by one mutex: the
	 * results slice, the shared index counter, the first-failure index, the
	 * CAS-failure holder and the cursor write bookkeeping. */
	var mu sync.Mutex
	results := make([]chunkOutcome, len(work))
	firstFailure := -1
	var casState *casFailure
	writtenDone := resumeDone
	// A missing cursor file has no hash to expect (the TS StateRead.hash was
	// null there); the first write therefore expects ABSENCE (create).
	var lastHash *string
	if previousRead.Exists {
		hash := previousRead.Hash
		lastHash = &hash
	}

	/* Cursor advancement: serialized (single FIFO advance goroutine),
	 * CAS-guarded, only over the contiguous completed-chunk prefix. A CAS
	 * conflict OR a store-write failure both stop advancement and report
	 * re-run-required — the loser re-runs, never blind-writes (R15, ARCH
	 * §6.3). */
	advanceStep := func() {
		mu.Lock()
		defer mu.Unlock()
		if casState != nil {
			return
		}
		prefix := 0
		for prefix < len(work) && results[prefix].done && !results[prefix].failed {
			prefix++
		}
		done := resumeDone + prefix
		if done <= writtenDone {
			return
		}
		write, writeErr := deps.Cursor.WriteAuditCursor(schemas.AuditCursor{
			Position:  EncodeAuditPosition(sinceUsed, done, listHash),
			UpdatedAt: options.Now,
		}, lastHash)
		if writeErr != nil {
			casState = &casFailure{
				expectedHash: lastHash,
				actualHash:   nil,
				detail: fmt.Sprintf(
					"audit-cursor write failed (store error, fail closed): %s — the cursor state is uncertain; re-run, never blind-write (R15, ARCH §6.3)",
					describeError(writeErr)),
			}
			return
		}
		if write.OK {
			hash := write.Hash
			lastHash = &hash
			writtenDone = done
			return
		}
		casState = &casFailure{
			expectedHash: write.ExpectedHash,
			actualHash:   write.ActualHash,
			detail: fmt.Sprintf(
				"audit-cursor CAS conflict (expected %s, actual %s) — the loser re-runs, never blind-writes (R15, ARCH §6.3)",
				jsonString(write.ExpectedHash), jsonString(write.ActualHash)),
		}
	}

	advanceRequests := make(chan struct{}, len(work)+2)
	advanceDone := make(chan struct{})
	go func() {
		defer close(advanceDone)
		for range advanceRequests {
			advanceStep()
		}
	}()

	/* Bounded in-process worker pool (P5-T12/OD-R10 pattern, mirrored). */
	concurrency := resolveConcurrency(options.Concurrency)
	workerCount := concurrency
	if workerCount > len(work) {
		workerCount = len(work)
	}
	next := 0
	var waitGroup sync.WaitGroup
	worker := func() {
		defer waitGroup.Done()
		for {
			mu.Lock()
			if firstFailure >= 0 || casState != nil {
				mu.Unlock()
				return
			}
			index := next
			next++
			mu.Unlock()
			if index >= len(work) {
				return
			}
			docPath := work[index]
			produced, portErr := deps.AuditDoc(ctx, docPath, prevCursor)
			if portErr == nil {
				validated, validationErr := validatePortFindings(docPath, produced)
				if validationErr != nil {
					portErr = validationErr
				} else {
					produced = validated
				}
			}
			mu.Lock()
			if portErr != nil {
				results[index] = chunkOutcome{done: true, failed: true, errMsg: describeError(portErr)}
				if firstFailure < 0 || index < firstFailure {
					firstFailure = index
				}
			} else {
				results[index] = chunkOutcome{done: true, findings: produced}
			}
			mu.Unlock()
			if portErr == nil {
				advanceRequests <- struct{}{}
			}
		}
	}
	for i := 0; i < workerCount; i++ {
		waitGroup.Add(1)
		go worker()
	}
	waitGroup.Wait()
	// The final advance pass (TS `await scheduleAdvance(); await advanceChain;`)
	// flushes any prefix completed by the last chunks.
	advanceRequests <- struct{}{}
	close(advanceRequests)
	<-advanceDone

	/* Findings are data: every successful chunk's findings are reported (even
	 * ones completed after a later chunk failed — the re-audit dedupes them). */
	rawFindings := []DriftFinding{}
	auditedDocs := []string{}
	for index, outcome := range results {
		if outcome.done && !outcome.failed {
			rawFindings = append(rawFindings, outcome.findings...)
			auditedDocs = append(auditedDocs, work[index])
		}
	}
	findings := DedupeFindings(rawFindings)
	sort.Strings(auditedDocs)
	var finalPosition *string
	if writtenDone > resumeDone {
		encoded := EncodeAuditPosition(sinceUsed, writtenDone, listHash)
		finalPosition = &encoded
	} else if prevCursor != nil {
		existing := prevCursor.Position
		finalPosition = &existing
	}
	cursor := AuditCursorState{Advanced: writtenDone > resumeDone, Position: finalPosition}

	if casState != nil {
		return AuditRunResult{
			Status:      AuditRunReRunRequired,
			Findings:    findings,
			AuditedDocs: auditedDocs,
			SkippedDocs: skipped,
			Cursor:      cursor,
			Detail:      &casState.detail,
		}
	}
	if firstFailure >= 0 {
		detail := fmt.Sprintf(
			"chunk failure at manifest doc %q: %s — the cursor persisted only the last fully completed chunk (done=%d); the unfinished chunk is re-audited on the next run, idempotent via claim-ID dedup (ARCH §7.2)",
			work[firstFailure], results[firstFailure].errMsg, writtenDone)
		return AuditRunResult{
			Status:      AuditRunPartial,
			Findings:    findings,
			AuditedDocs: auditedDocs,
			SkippedDocs: skipped,
			Cursor:      cursor,
			Detail:      &detail,
		}
	}
	return AuditRunResult{
		Status:      AuditRunCompleted,
		Findings:    findings,
		AuditedDocs: auditedDocs,
		SkippedDocs: skipped,
		Cursor:      cursor,
		Detail:      nil,
	}
}

// jsonString mirrors the JSON.stringify the TS CAS-conflict detail used for
// the expected/actual hash pair ("null" for absent, quoted for present).
func jsonString(value *string) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *value)
}
