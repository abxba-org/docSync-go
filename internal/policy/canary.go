// Ported from packages/policy/src/promotion/canary.ts (P12-T05; ARCH §8.6,
// CTX §21): time-boxed canaries and AUTO-EXPIRY of stale shadows.
//
//   - {@link EvaluateCanaryLifecycle} classifies W1 shadow-run records
//     (`docsync.shadow-run-record/v1`, which carry `created_at`/`expires_at`)
//     against an INJECTED `now`: a record at or past its `expires_at` is
//     expired — a TYPED lifecycle status (`expired`), never consulted again.
//   - {@link FilterConsultableRecords} is the "never consulted again" seam:
//     expired records are dropped from the consultable set.
//   - {@link CanaryLifecycleStore} persists canary status in
//     `.docsync/policy-history/` (the existing record layer — the promotion
//     event log lives in the same directory) as `canaries.jsonl`, following
//     the promotion-store pattern: append-only `docsync.canary-record/v1`
//     transition rows, latest row per `canary_id` is the current status,
//     injected clock/ids, schema-validated on every read (fail closed).
//
// An expired canary is NEVER promotable: {@link CanaryLifecycleStore.Promote}
// refuses any transition out of `expired` (fail closed, INV-9). A successful
// canary conclusion hands its evidence to the promotion pipeline — this store
// never promotes policy by itself.
//
// The expiry probe itself (`now >= expires_at`, TS isShadowRecordExpired in
// shadow/live.ts) is mirrored by the unexported shadowExpiredAt helper until
// the shadow-evaluation unit lands its own port; the swap point is documented
// on the helper.
package policy

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// CanaryLogFilename is the append-only canary event log inside
// `.docsync/policy-history/` (CANARY_LOG_FILENAME).
const CanaryLogFilename = "canaries.jsonl"

// canaryRecordSchemaID is the literal schema tag every row carries
// (CANARY_RECORD_SCHEMA_ID).
const canaryRecordSchemaID = "docsync.canary-record/v1"

// canaryOpenedReason is the default reason recorded when a canary opens
// (TS: `input.reason ?? 'canary opened: shadow run active within its
// time-box (ARCH §8.6)'`).
const canaryOpenedReason = "canary opened: shadow run active within its time-box (ARCH §8.6)"

// CanaryLifecycleError ports the TS CanaryLifecycleError class: a
// discriminator code ('not_found' | 'invalid_transition' | 'schema' | 'io')
// plus the verbatim message, wrapped in a docutil coded error so
// docutil.CodeOf keeps working (matched with errors.As).
type CanaryLifecycleError struct {
	// Code is the TS discriminator: 'not_found' | 'invalid_transition' |
	// 'schema' | 'io'.
	Code string
	// coded carries the message and the docutil code.
	coded *docutil.Error
}

// Error renders the coded message.
func (e *CanaryLifecycleError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error, so errors.As / docutil.CodeOf keep working
// through the wrapper.
func (e *CanaryLifecycleError) Unwrap() error { return e.coded }

// newCanaryLifecycleError builds a coded canary error (message text preserved
// verbatim from the TS class). The code → docutil-code mapping mirrors the
// promotion store (CONVENTIONS: closest existing code, never a new one).
func newCanaryLifecycleError(code, format string, args ...any) *CanaryLifecycleError {
	docutilCode := docutil.CodeState
	switch code {
	case "schema":
		docutilCode = docutil.CodeSchema
	case "io":
		docutilCode = docutil.CodeIO
	}
	return &CanaryLifecycleError{
		Code:  code,
		coded: docutil.Newf(docutilCode, format, args...),
	}
}

// CanaryLifecycleStatus is the typed lifecycle verdict status for one shadow
// record (P12-T05): a record at or past its `expires_at` is `expired` — a
// typed terminal outcome, never consulted again.
type CanaryLifecycleStatus string

// The lifecycle verdict statuses (a canary is either active or stale).
const (
	CanaryLifecycleActive  CanaryLifecycleStatus = "active"
	CanaryLifecycleExpired CanaryLifecycleStatus = "expired"
)

// CanaryLifecycleVerdict is the typed lifecycle verdict for one shadow record
// (CanaryLifecycleVerdict).
type CanaryLifecycleVerdict struct {
	RecordID                string                `json:"record_id"`
	RunID                   string                `json:"run_id"`
	CandidatePolicyVersion  int                   `json:"candidate_policy_version"`
	ProductionPolicyVersion int                   `json:"production_policy_version"`
	CreatedAt               string                `json:"created_at"`
	ExpiresAt               string                `json:"expires_at"`
	Status                  CanaryLifecycleStatus `json:"status"`
}

// CanaryLifecycleResult is the deterministic classification outcome
// (CanaryLifecycleResult): one verdict per record, sorted by record id.
type CanaryLifecycleResult struct {
	Now          string                   `json:"now"`
	Verdicts     []CanaryLifecycleVerdict `json:"verdicts"`
	ActiveCount  int                      `json:"active_count"`
	ExpiredCount int                      `json:"expired_count"`
}

// parseInstant parses one schema-validated instant. The TS used the lenient
// `Date.parse`; the wire format is RFC 3339 (the `z.string().datetime()`
// shape `internal/schemas` validates), so a strictly-RFC3339 instant is
// required here — anything else fails closed instead of yielding NaN.
func parseInstant(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}

// shadowExpiredAt mirrors isShadowRecordExpired (shadow/live.ts): `now` at or
// after `expires_at` means the shadow is stale (ARCH §8.6). Enforcement
// (auto-expiry) lives in this unit — this is the lifecycle data seam.
//
// SWAP POINT: when the shadow-evaluation unit lands its own port, replace the
// body with a call to policy.IsShadowRecordExpired(record, now) and delete
// this helper (one definition per behavior).
func shadowExpiredAt(now, expiresAt string) (bool, error) {
	nowMs, err := parseInstant(now)
	if err != nil {
		return false, err
	}
	expiresMs, err := parseInstant(expiresAt)
	if err != nil {
		return false, err
	}
	return !nowMs.Before(expiresMs), nil
}

// EvaluateCanaryLifecycle classifies shadow records against the injected
// `now` (P12-T05; ARCH §8.6 "time-box canaries; auto-expire stale shadows").
// Pure and deterministic: expiry comes solely from the records' `expires_at`
// vs `now` — no clocks, no I/O. Each record is schema-validated first (fail
// closed on drift).
func EvaluateCanaryLifecycle(records []schemas.ShadowRunRecord, now string) (CanaryLifecycleResult, error) {
	if _, err := parseInstant(now); err != nil {
		return CanaryLifecycleResult{}, newCanaryLifecycleError("schema",
			"evaluateCanaryLifecycle: now is not a parseable instant: %s", now)
	}
	verdicts := make([]CanaryLifecycleVerdict, 0, len(records))
	for _, record := range records {
		if issues := record.Validate(); len(issues) > 0 {
			return CanaryLifecycleResult{}, newCanaryLifecycleError("schema",
				"evaluateCanaryLifecycle: shadow record %s failed docsync.shadow-run-record/v1 validation: %s",
				record.RecordID, schemas.NewValidationError(issues...).Error())
		}
		expired, err := shadowExpiredAt(now, record.ExpiresAt)
		if err != nil {
			return CanaryLifecycleResult{}, newCanaryLifecycleError("schema",
				"evaluateCanaryLifecycle: record %s has an unparseable expires_at (%s)",
				record.RecordID, record.ExpiresAt)
		}
		status := CanaryLifecycleActive
		if expired {
			status = CanaryLifecycleExpired
		}
		verdicts = append(verdicts, CanaryLifecycleVerdict{
			RecordID:                record.RecordID,
			RunID:                   record.RunID,
			CandidatePolicyVersion:  record.Policy.CandidateVersion,
			ProductionPolicyVersion: record.Policy.ProductionVersion,
			CreatedAt:               record.CreatedAt,
			ExpiresAt:               record.ExpiresAt,
			Status:                  status,
		})
	}
	sort.SliceStable(verdicts, func(i, j int) bool {
		return verdicts[i].RecordID < verdicts[j].RecordID
	})
	activeCount := 0
	expiredCount := 0
	for _, verdict := range verdicts {
		if verdict.Status == CanaryLifecycleActive {
			activeCount++
		} else {
			expiredCount++
		}
	}
	return CanaryLifecycleResult{
		Now:          now,
		Verdicts:     verdicts,
		ActiveCount:  activeCount,
		ExpiredCount: expiredCount,
	}, nil
}

// FilterConsultableRecords is the consultable set (TS
// filterConsultableRecords): stale shadows are auto-expired and NEVER
// consulted again (ARCH §8.6) — expired records are dropped, active records
// pass through untouched, input order preserved.
func FilterConsultableRecords(records []schemas.ShadowRunRecord, now string) ([]schemas.ShadowRunRecord, error) {
	result, err := EvaluateCanaryLifecycle(records, now)
	if err != nil {
		return nil, err
	}
	expired := make(map[string]struct{}, len(result.Verdicts))
	for _, verdict := range result.Verdicts {
		if verdict.Status == CanaryLifecycleExpired {
			expired[verdict.RecordID] = struct{}{}
		}
	}
	out := make([]schemas.ShadowRunRecord, 0, len(records))
	for _, record := range records {
		if _, isExpired := expired[record.RecordID]; isExpired {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

// CanaryOpenInput is the open() input (CanaryOpenInput): the time-box data of
// a shadow run plus its versions. A nil Reason means "absent" — the default
// reason is recorded (the TS `input.reason ?? default`).
type CanaryOpenInput struct {
	RunID                   string
	Repo                    string
	CandidatePolicyVersion  int
	ProductionPolicyVersion int
	CreatedAt               string
	ExpiresAt               string
	Reason                  *string
}

// CanaryLifecycleStore is the canary lifecycle store (P12-T05): the
// policy-side record store for canary status, following the promotion-store
// pattern (`promotions.jsonl` → `canaries.jsonl` in the same
// `.docsync/policy-history/` directory). Append-only transition rows; the
// latest row per canary is current.
type CanaryLifecycleStore struct {
	historyDir string
	clock      func() string
	newID      func() string
	path       string

	// mu serializes the read-modify-write append so concurrent appends can
	// never lose a row.
	mu sync.Mutex
}

// NewCanaryLifecycleStore builds a store over an ABSOLUTE historyDir. The
// clock and id injections are required (fail closed, determinism).
func NewCanaryLifecycleStore(options CanaryLifecycleStoreOptions) (*CanaryLifecycleStore, error) {
	if options.HistoryDir == "" || !filepath.IsAbs(options.HistoryDir) {
		return nil, docutil.Newf(docutil.CodeConfig,
			"canary lifecycle store: historyDir must be an absolute path (got %q)", options.HistoryDir)
	}
	if options.Clock == nil {
		return nil, docutil.New(docutil.CodeConfig,
			"canary lifecycle store: clock must be injected (the store never reads the wall clock)")
	}
	if options.NewID == nil {
		return nil, docutil.New(docutil.CodeConfig,
			"canary lifecycle store: newId must be injected (the store never generates ids itself)")
	}
	return &CanaryLifecycleStore{
		historyDir: options.HistoryDir,
		clock:      options.Clock,
		newID:      options.NewID,
		path:       filepath.Join(options.HistoryDir, CanaryLogFilename),
	}, nil
}

// CanaryLifecycleStoreOptions configures a CanaryLifecycleStore
// (CanaryLifecycleStoreOptions).
type CanaryLifecycleStoreOptions struct {
	// HistoryDir is the `.docsync/policy-history` directory
	// (POLICY_HISTORY_SUBDIR).
	HistoryDir string
	// Clock supplies the `at` instant of every appended row.
	Clock func() string
	// NewID supplies the canary id of every opened row.
	NewID func() string
}

// LogPath is the absolute path of the append-only canary event log.
func (s *CanaryLifecycleStore) LogPath() string { return s.path }

// ReadAll replays every canary row in file order (TS
// CanaryLifecycleStore.readAll). A missing log is the declared soft case (an
// empty slice); any other read failure fails LOUD — the lifecycle surface
// must never silently vanish. Every row is schema-validated (fail closed).
func (s *CanaryLifecycleStore) ReadAll() ([]schemas.CanaryRecord, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []schemas.CanaryRecord{}, nil // no log yet — the declared soft case
		}
		return nil, newCanaryLifecycleError("io",
			`canary log %q could not be read — the lifecycle surface must never silently vanish`,
			CanaryLogFilename)
	}
	rows := make([]schemas.CanaryRecord, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record, parseErr := schemas.ParseCanaryRecord([]byte(line))
		if parseErr != nil {
			return nil, newCanaryLifecycleError("schema",
				"canary log row failed schema validation: %s", parseErr.Error())
		}
		rows = append(rows, *record)
	}
	return rows, nil
}

// append writes one row as a JSON line (TS CanaryLifecycleStore.append), the
// same compare-and-swap discipline as the promotion store.
func (s *CanaryLifecycleStore) append(row schemas.CanaryRecord) error {
	line, err := json.Marshal(row)
	if err != nil {
		return newCanaryLifecycleError("schema", "canary row could not be serialized: %s", err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return newCanaryLifecycleError("io",
			`canary log %q could not be read for append — the lifecycle surface must never silently vanish`,
			CanaryLogFilename)
	}
	next := string(current) + string(line) + "\n"
	var expectedHash *string
	if err == nil {
		hash := docutil.ContentHashBytes(current)
		expectedHash = &hash
	}
	write, err := state.CasWriteFileDetailed(s.path, next, expectedHash)
	if err != nil {
		return newCanaryLifecycleError("io",
			`canary log %q could not be appended: %s`, CanaryLogFilename, err.Error())
	}
	if !write.OK {
		return newCanaryLifecycleError("io",
			`canary log %q changed concurrently — refusing to append (the lifecycle surface is append-only, fail closed)`,
			CanaryLogFilename)
	}
	return nil
}

// validateCanaryRow reproduces CANARY_RECORD_SCHEMA.parse on a row about to
// be appended: the strict record parser is the single source of truth for the
// wire format.
func validateCanaryRow(row schemas.CanaryRecord) error {
	if issues := row.Validate(); len(issues) > 0 {
		return newCanaryLifecycleError("schema", "canary row failed schema validation: %s",
			schemas.NewValidationError(issues...).Error())
	}
	return nil
}

// ByID returns the latest row for a canary, or nil when absent (TS
// CanaryLifecycleStore.byId — `CanaryRecord | null`).
func (s *CanaryLifecycleStore) ByID(canaryID string) (*schemas.CanaryRecord, error) {
	rows, err := s.ReadAll()
	if err != nil {
		return nil, err
	}
	var latest *schemas.CanaryRecord
	for i := range rows {
		if rows[i].CanaryID == canaryID {
			latest = &rows[i]
		}
	}
	return latest, nil
}

// Open opens a canary from a shadow run's time-box data — status `active`
// (TS CanaryLifecycleStore.open).
func (s *CanaryLifecycleStore) Open(input CanaryOpenInput) (*schemas.CanaryRecord, error) {
	reason := canaryOpenedReason
	if input.Reason != nil {
		reason = *input.Reason
	}
	row := schemas.CanaryRecord{
		Schema:                  canaryRecordSchemaID,
		CanaryID:                s.newID(),
		RunID:                   input.RunID,
		Repo:                    input.Repo,
		CandidatePolicyVersion:  input.CandidatePolicyVersion,
		ProductionPolicyVersion: input.ProductionPolicyVersion,
		Status:                  schemas.CanaryStatusActive,
		Reason:                  reason,
		CreatedAt:               input.CreatedAt,
		ExpiresAt:               input.ExpiresAt,
		At:                      s.clock(),
	}
	if err := validateCanaryRow(row); err != nil {
		return nil, err
	}
	if err := s.append(row); err != nil {
		return nil, err
	}
	return &row, nil
}

// ExpireStale is the AUTO-EXPIRY pass (ARCH §8.6; TS
// CanaryLifecycleStore.expireStale): every canary whose `expires_at` is at or
// before the injected `now` transitions to the TYPED `expired` status. Expired
// canaries are never consulted again and never promotable. The pass is
// idempotent — an already-expired canary is not re-appended.
func (s *CanaryLifecycleStore) ExpireStale(now string) ([]schemas.CanaryRecord, error) {
	nowMs, err := parseInstant(now)
	if err != nil {
		return nil, newCanaryLifecycleError("schema",
			"expireStale: now is not a parseable instant: %s", now)
	}
	rows, err := s.ReadAll()
	if err != nil {
		return nil, err
	}
	// The latest row per canary_id, iterated in first-appearance order (the
	// JS Map's insertion order).
	latestIndex := make(map[string]int, len(rows))
	order := make([]string, 0, len(rows))
	for i, row := range rows {
		if _, seen := latestIndex[row.CanaryID]; !seen {
			order = append(order, row.CanaryID)
		}
		latestIndex[row.CanaryID] = i
	}
	expired := make([]schemas.CanaryRecord, 0, len(order))
	for _, canaryID := range order {
		row := rows[latestIndex[canaryID]]
		if row.Status != schemas.CanaryStatusActive {
			continue
		}
		expiresMs, err := parseInstant(row.ExpiresAt)
		if err != nil || nowMs.Before(expiresMs) {
			continue
		}
		next := row
		next.Status = schemas.CanaryStatusExpired
		next.Reason = "auto-expired: now " + now + " is at or after expires_at " + row.ExpiresAt +
			" (time-boxed canary, ARCH §8.6) — never consulted again, never promotable"
		next.At = s.clock()
		if err := validateCanaryRow(next); err != nil {
			return nil, err
		}
		if err := s.append(next); err != nil {
			return nil, err
		}
		expired = append(expired, next)
	}
	sort.SliceStable(expired, func(i, j int) bool {
		return expired[i].CanaryID < expired[j].CanaryID
	})
	return expired, nil
}

// Promote concludes an ACTIVE canary as promoted (TS
// CanaryLifecycleStore.promote): its evidence moves to the promotion pipeline,
// which owns the actual policy promotion — INV-7. An EXPIRED canary is never
// promotable (fail closed).
func (s *CanaryLifecycleStore) Promote(canaryID, reason string) (*schemas.CanaryRecord, error) {
	return s.transition(canaryID, schemas.CanaryStatusPromoted, reason,
		"only an active canary can be promoted")
}

// Reject concludes an ACTIVE canary as rejected (TS
// CanaryLifecycleStore.reject): e.g. its evaluation failed.
func (s *CanaryLifecycleStore) Reject(canaryID, reason string) (*schemas.CanaryRecord, error) {
	return s.transition(canaryID, schemas.CanaryStatusRejected, reason,
		"only an active canary can be rejected")
}

// transition is the shared conclude path (TS CanaryLifecycleStore.transition):
// a reason is required, the latest row decides, and an EXPIRED canary never
// transitions again (fail closed, INV-9).
func (s *CanaryLifecycleStore) transition(canaryID string, status schemas.CanaryStatus, reason, onlyActive string) (*schemas.CanaryRecord, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, newCanaryLifecycleError("invalid_transition", "concluding a canary requires a reason")
	}
	prior, err := s.ByID(canaryID)
	if err != nil {
		return nil, err
	}
	if prior == nil {
		return nil, newCanaryLifecycleError("not_found", "canary %s not found", canaryID)
	}
	if prior.Status == schemas.CanaryStatusExpired {
		return nil, newCanaryLifecycleError("invalid_transition",
			"canary %s is EXPIRED — stale shadows auto-expire and expired canaries are "+
				"never consulted again and never promotable (ARCH §8.6; INV-9)", canaryID)
	}
	if prior.Status != schemas.CanaryStatusActive {
		return nil, newCanaryLifecycleError("invalid_transition",
			"canary %s is %s — %s", canaryID, prior.Status, onlyActive)
	}
	row := *prior
	row.Status = status
	row.Reason = reason
	row.At = s.clock()
	if err := validateCanaryRow(row); err != nil {
		return nil, err
	}
	if err := s.append(row); err != nil {
		return nil, err
	}
	return &row, nil
}
