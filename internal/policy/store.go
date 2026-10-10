// Ported from packages/policy/src/promotion/store.ts (P10-T18/T19; ARCH
// §8.4/§8.5, CTX §20): the versioned, human-gated promotion pipeline's durable
// surface. `docsync.promotion-record/v1` rows ARE the self-contained versioned
// artifacts (previous pointer, reason, evaluation block incl.
// precision/recall/unsafe_auto_updates, status) — the event log
// (`promotions.jsonl`, append-only) carries every promotion event with full
// evaluation evidence. MODELS PROPOSE; HUMANS PROMOTE (INV-7):
// approved/promoted requires approved_by. Rollback = promoting the prior
// version, a one-operation metadata act (ARCH §8.4). P12-T07: the promote
// checklist is structurally cross-checked against its own gate rows (V-P12-9)
// so a fabricated checklist cannot promote.
//
// Persistence: the log lives at `<historyDir>/promotions.jsonl` where
// historyDir is `.docsync/policy-history/` (POLICY_HISTORY_SUBDIR) — the same
// record layer as the canary log. Appends go through internal/state's
// compare-and-swap write (state.CasWriteFileDetailed: atomic temp-file +
// rename, never a truncating direct write), guarded by a per-store mutex so a
// read-modify-write append can never lose a row within one process. Reads
// replay every line through schemas.ParsePromotionRecord — the strict
// `docsync.promotion-record/v1` parser (unknown JSON keys rejected, zod
// validation messages reproduced) — so a schema-drifted row fails LOUD instead
// of silently vanishing from the audit surface (fail closed).
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// PromotionLogFilename is the append-only promotion event log inside
// `.docsync/policy-history/` (PROMOTION_LOG_FILENAME).
const PromotionLogFilename = "promotions.jsonl"

// promotionRecordSchemaID is the literal schema tag every row carries
// (PROMOTION_RECORD_SCHEMA_ID).
const promotionRecordSchemaID = "docsync.promotion-record/v1"

// PromotionStatus is the closed promotion-status vocabulary
// (PromotionRecord['status']); the values live in internal/schemas.
type PromotionStatus = schemas.PromotionStatus

// PromotionStoreError ports the TS PromotionStoreError class: a discriminator
// code ('not_found' | 'invalid_transition' | 'schema' | 'io') plus the verbatim
// message, wrapped in a docutil coded error so docutil.CodeOf keeps working
// (Go returns values; the class identity is matched with errors.As).
type PromotionStoreError struct {
	// Code is the TS discriminator: 'not_found' | 'invalid_transition' |
	// 'schema' | 'io'.
	Code string
	// coded carries the message and the docutil code.
	coded *docutil.Error
}

// Error renders the coded message.
func (e *PromotionStoreError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error, so errors.As / docutil.CodeOf keep working
// through the wrapper.
func (e *PromotionStoreError) Unwrap() error { return e.coded }

// promotionDocutilCode maps the TS discriminator onto the closest existing
// docutil code (CONVENTIONS: pick the closest code, never invent one).
func promotionDocutilCode(code string) string {
	switch code {
	case "schema":
		return docutil.CodeSchema
	case "io":
		return docutil.CodeIO
	default: // not_found | invalid_transition
		return docutil.CodeState
	}
}

// newPromotionStoreError builds a coded promotion-store error (message text
// preserved verbatim from the TS class).
func newPromotionStoreError(code, format string, args ...any) *PromotionStoreError {
	return &PromotionStoreError{
		Code:  code,
		coded: docutil.Newf(promotionDocutilCode(code), format, args...),
	}
}

// PromotionStoreOptions configures a PromotionStore (PromotionStoreOptions).
// Clock and NewID are injected — the store never reads the wall clock or
// generates an id behind the caller's back (CONVENTIONS: determinism).
type PromotionStoreOptions struct {
	// HistoryDir is the `.docsync/policy-history` directory
	// (POLICY_HISTORY_SUBDIR).
	HistoryDir string
	// Clock supplies the `at` instant of every appended row.
	Clock func() string
	// NewID supplies the promotion id of every proposed row.
	NewID func() string
}

// PromotionEvaluationEvidence is the embedded evaluation evidence
// (PromotionEvaluationEvidence) — full report + the two gate inputs.
type PromotionEvaluationEvidence struct {
	Report            map[string]any `json:"report"`
	UnsafeAutoUpdates int            `json:"unsafe_auto_updates"`
	SampleSize        int            `json:"sample_size"`
}

// PromotionGateChecklistRow is one claimed gate row of a presented checklist
// (the store reads only gate + passed; the TS structural type
// `{readonly gate: string; readonly passed: boolean}`).
type PromotionGateChecklistRow struct {
	Gate   string `json:"gate"`
	Passed bool   `json:"passed"`
}

// PromotionGateChecklist is the gate checklist re-presented to the store at
// promote time (P12-T07, V-P12-9). When Rows is non-nil the store structurally
// vetoes a fabricated checklist: the claimed passed/failedGates must agree
// with the rows themselves and every mandatory gate must be present — a
// caller cannot assert `passed: true` over rows that say otherwise.
type PromotionGateChecklist struct {
	Passed      bool
	FailedGates []string
	Rows        []PromotionGateChecklistRow
}

// PromotionProposal is the propose input (PromotionProposalInput): a nil
// PreviousVersion means "absent" (the TS optional field).
type PromotionProposal struct {
	PolicyVersion   int
	PreviousVersion *int
	Reason          string
	Evaluation      PromotionEvaluationEvidence
}

// PromotionStore is the durable promotion surface.
type PromotionStore struct {
	historyDir string
	clock      func() string
	newID      func() string
	path       string

	// mu serializes the read-modify-write append so concurrent appends can
	// never lose a row (the TS process had the same single-writer property
	// through appendFile's O_APPEND).
	mu sync.Mutex
}

// NewPromotionStore builds a store over an ABSOLUTE historyDir. The clock and
// id injections are required — the store refuses to construct without them
// (fail closed, determinism: no wall clock and no id generation behind the
// caller's back).
func NewPromotionStore(options PromotionStoreOptions) (*PromotionStore, error) {
	if options.HistoryDir == "" || !filepath.IsAbs(options.HistoryDir) {
		return nil, docutil.Newf(docutil.CodeConfig,
			"promotion store: historyDir must be an absolute path (got %q)", options.HistoryDir)
	}
	if options.Clock == nil {
		return nil, docutil.New(docutil.CodeConfig,
			"promotion store: clock must be injected (the store never reads the wall clock)")
	}
	if options.NewID == nil {
		return nil, docutil.New(docutil.CodeConfig,
			"promotion store: newId must be injected (the store never generates ids itself)")
	}
	return &PromotionStore{
		historyDir: options.HistoryDir,
		clock:      options.Clock,
		newID:      options.NewID,
		path:       filepath.Join(options.HistoryDir, PromotionLogFilename),
	}, nil
}

// LogPath is the absolute path of the append-only promotion event log.
func (s *PromotionStore) LogPath() string { return s.path }

// ReadAll replays every promotion row in file order (TS PromotionStore.readAll).
// A missing log is the declared soft case (an empty slice); any other read
// failure fails LOUD — the audit surface must never silently vanish. Every row
// is schema-validated: a drifted row is a hard error, never skipped.
func (s *PromotionStore) ReadAll() ([]schemas.PromotionRecord, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []schemas.PromotionRecord{}, nil // no log yet — the declared soft case
		}
		return nil, newPromotionStoreError("io",
			`promotion log %q could not be read — the audit surface must never silently vanish`,
			PromotionLogFilename)
	}
	rows := make([]schemas.PromotionRecord, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record, parseErr := schemas.ParsePromotionRecord([]byte(line))
		if parseErr != nil {
			return nil, newPromotionStoreError("schema",
				"promotion log row failed schema validation: %s", parseErr.Error())
		}
		rows = append(rows, *record)
	}
	return rows, nil
}

// append writes one row as a JSON line (TS PromotionStore.append). The append
// is a compare-and-swap over the whole log: read current bytes, append the
// line, write back atomically expecting the bytes just read. A concurrent
// change between read and write surfaces as an io error (fail closed) rather
// than a silently lost row.
func (s *PromotionStore) append(row schemas.PromotionRecord) error {
	line, err := json.Marshal(row)
	if err != nil {
		return newPromotionStoreError("schema", "promotion row could not be serialized: %s", err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return newPromotionStoreError("io",
			`promotion log %q could not be read for append — the audit surface must never silently vanish`,
			PromotionLogFilename)
	}
	next := string(current) + string(line) + "\n"
	var expectedHash *string
	if err == nil {
		hash := docutil.ContentHashBytes(current)
		expectedHash = &hash
	}
	write, err := state.CasWriteFileDetailed(s.path, next, expectedHash)
	if err != nil {
		return newPromotionStoreError("io",
			`promotion log %q could not be appended: %s`, PromotionLogFilename, err.Error())
	}
	if !write.OK {
		return newPromotionStoreError("io",
			`promotion log %q changed concurrently — refusing to append (the audit surface is append-only, fail closed)`,
			PromotionLogFilename)
	}
	return nil
}

// validateRow reproduces PROMOTION_RECORD_SCHEMA.parse on a row about to be
// appended: the strict record parser is the single source of truth for the
// wire format, so a malformed row never reaches the log.
func validatePromotionRow(row schemas.PromotionRecord) error {
	if issues := row.Validate(); len(issues) > 0 {
		return newPromotionStoreError("schema", "promotion row failed schema validation: %s",
			schemas.NewValidationError(issues...).Error())
	}
	return nil
}

// ByID returns the LATEST row for a promotion id, or nil when absent
// (TS PromotionStore.byId — `PromotionRecord | null`).
func (s *PromotionStore) ByID(promotionID string) (*schemas.PromotionRecord, error) {
	rows, err := s.ReadAll()
	if err != nil {
		return nil, err
	}
	var latest *schemas.PromotionRecord
	for i := range rows {
		if rows[i].PromotionID == promotionID {
			latest = &rows[i]
		}
	}
	return latest, nil
}

// Propose appends a `proposed` row and returns it (TS PromotionStore.propose).
// Models propose: the row carries no approver — approved_by is a HUMAN act.
func (s *PromotionStore) Propose(input PromotionProposal) (*schemas.PromotionRecord, error) {
	row := schemas.PromotionRecord{
		Schema:          promotionRecordSchemaID,
		PromotionID:     s.newID(),
		PolicyVersion:   input.PolicyVersion,
		PreviousVersion: input.PreviousVersion,
		Reason:          input.Reason,
		Evaluation: schemas.PromotionEvaluation{
			Report:            input.Evaluation.Report,
			UnsafeAutoUpdates: input.Evaluation.UnsafeAutoUpdates,
			SampleSize:        input.Evaluation.SampleSize,
		},
		Status: schemas.PromotionStatusProposed,
		At:     s.clock(),
	}
	if err := validatePromotionRow(row); err != nil {
		return nil, err
	}
	if err := s.append(row); err != nil {
		return nil, err
	}
	return &row, nil
}

// Approve appends an `approved` row (TS PromotionStore.approve). INV-7: a human
// approves — approved_by must be present and non-empty.
func (s *PromotionStore) Approve(promotionID, approvedBy string) (*schemas.PromotionRecord, error) {
	if strings.TrimSpace(approvedBy) == "" {
		return nil, newPromotionStoreError("invalid_transition",
			"approve requires a non-empty approved_by (INV-7: models propose, humans promote)")
	}
	prior, err := s.ByID(promotionID)
	if err != nil {
		return nil, err
	}
	if prior == nil {
		return nil, newPromotionStoreError("not_found", "promotion %s not found", promotionID)
	}
	if prior.Status != schemas.PromotionStatusProposed {
		return nil, newPromotionStoreError("invalid_transition",
			"promotion %s is %s, only proposed can be approved", promotionID, prior.Status)
	}
	row := *prior
	row.Status = schemas.PromotionStatusApproved
	approver := approvedBy
	row.ApprovedBy = &approver
	if err := validatePromotionRow(row); err != nil {
		return nil, err
	}
	if err := s.append(row); err != nil {
		return nil, err
	}
	return &row, nil
}

// Promote appends the `promoted` row (TS PromotionStore.promote). The gates
// cannot be bypassed: a checklist that does not PASS is refused with the
// failing gates named, and — when the checklist carries rows — the store
// cross-checks the claim against those rows and the mandatory gate coverage
// (V-P12-9 structural veto).
func (s *PromotionStore) Promote(promotionID string, checklist PromotionGateChecklist) (*schemas.PromotionRecord, error) {
	if !checklist.Passed {
		failed := strings.Join(checklist.FailedGates, ", ")
		if failed == "" {
			failed = "none reported"
		}
		return nil, newPromotionStoreError("invalid_transition",
			"promote requires a PASSED gate checklist (INV-9) — failing gates: %s", failed)
	}
	// V-P12-9 structural veto: when the checklist carries gate rows, the store
	// cross-checks the claim against the rows themselves — a fabricated
	// checklist cannot assert a pass its own evidence contradicts, and a
	// checklist missing a mandatory gate is not a promotion checklist.
	if checklist.Rows != nil {
		derivedFailed := make([]string, 0, len(checklist.Rows))
		for _, row := range checklist.Rows {
			if !row.Passed {
				derivedFailed = append(derivedFailed, row.Gate)
			}
		}
		claimedFailed := append([]string(nil), checklist.FailedGates...)
		sort.Strings(claimedFailed)
		derivedSorted := append([]string(nil), derivedFailed...)
		sort.Strings(derivedSorted)
		consistent := checklist.Passed == (len(derivedFailed) == 0) &&
			len(claimedFailed) == len(derivedSorted)
		if consistent {
			for i := range claimedFailed {
				if claimedFailed[i] != derivedSorted[i] {
					consistent = false
					break
				}
			}
		}
		covered := true
		for _, mandatory := range MandatoryGateNames {
			present := false
			for _, row := range checklist.Rows {
				if row.Gate == mandatory {
					present = true
					break
				}
			}
			if !present {
				covered = false
				break
			}
		}
		if !consistent || !covered {
			return nil, newPromotionStoreError("invalid_transition",
				"gate checklist rejected: the claimed checklist disagrees with its own gate rows or "+
					"omits a mandatory gate (%s) — a fabricated checklist cannot promote (V-P12-9, INV-9)",
				strings.Join(MandatoryGateNames, ", "))
		}
	}
	prior, err := s.ByID(promotionID)
	if err != nil {
		return nil, err
	}
	if prior == nil {
		return nil, newPromotionStoreError("not_found", "promotion %s not found", promotionID)
	}
	if prior.Status != schemas.PromotionStatusApproved {
		return nil, newPromotionStoreError("invalid_transition",
			"promotion %s is %s — only approved promotes (INV-9: fail closed)", promotionID, prior.Status)
	}
	if prior.Evaluation.UnsafeAutoUpdates != 0 {
		return nil, newPromotionStoreError("invalid_transition",
			"promotion %s carries unsafe_auto_updates = %d — the gate metric is not promotable away (INV-9)",
			promotionID, prior.Evaluation.UnsafeAutoUpdates)
	}
	row := *prior
	row.Status = schemas.PromotionStatusPromoted
	if err := validatePromotionRow(row); err != nil {
		return nil, err
	}
	if err := s.append(row); err != nil {
		return nil, err
	}
	return &row, nil
}

// Rollback is the one-op rollback (TS PromotionStore.rollback): a new promoted
// row restoring the PRIOR version. Only a *promoted* row rolls back, and the
// original human approver attribution carries over verbatim (INV-7 — no
// synthetic approver). 'rolled_back' remains a reserved status value (rollback
// currently emits a 'promoted' restore row).
func (s *PromotionStore) Rollback(promotionID string) (*schemas.PromotionRecord, error) {
	prior, err := s.ByID(promotionID)
	if err != nil {
		return nil, err
	}
	if prior == nil {
		return nil, newPromotionStoreError("not_found", "promotion %s not found", promotionID)
	}
	if prior.Status != schemas.PromotionStatusPromoted {
		return nil, newPromotionStoreError("invalid_transition",
			"promotion %s is %s — only a promoted row can be rolled back", promotionID, prior.Status)
	}
	if prior.ApprovedBy == nil {
		return nil, newPromotionStoreError("invalid_transition",
			"rollback requires the original human approval attribution (INV-7) — a promoted row without approved_by cannot exist via the pipeline")
	}
	if prior.PreviousVersion == nil {
		return nil, newPromotionStoreError("invalid_transition",
			"promotion %s has no previous version to roll back to", promotionID)
	}
	previousVersion := *prior.PreviousVersion
	approver := *prior.ApprovedBy
	row := schemas.PromotionRecord{
		Schema:          promotionRecordSchemaID,
		PromotionID:     s.newID(),
		PolicyVersion:   previousVersion,
		PreviousVersion: &prior.PolicyVersion,
		Reason:          fmt.Sprintf("rollback of promotion %s", promotionID),
		Evaluation:      prior.Evaluation,
		Status:          schemas.PromotionStatusPromoted,
		ApprovedBy:      &approver,
		At:              s.clock(),
	}
	if err := validatePromotionRow(row); err != nil {
		return nil, err
	}
	if err := s.append(row); err != nil {
		return nil, err
	}
	return &row, nil
}

// LatestStatusFor returns the latest status recorded for a policy version
// (TS PromotionStore.latestStatusFor — `PromotionStatus | null`). The boolean
// reports presence (false = no row for that version).
func (s *PromotionStore) LatestStatusFor(policyVersion int) (schemas.PromotionStatus, bool, error) {
	rows, err := s.ReadAll()
	if err != nil {
		return "", false, err
	}
	var latest *schemas.PromotionRecord
	for i := range rows {
		if rows[i].PolicyVersion == policyVersion {
			latest = &rows[i]
		}
	}
	if latest == nil {
		return "", false, nil
	}
	return latest.Status, true, nil
}
