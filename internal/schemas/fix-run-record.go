package schemas

import (
	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Ported from packages/schemas/src/v1/fix-run-record.ts
// (FIX_RUN_RECORD_SCHEMA + FIX_APPLIED_ENTRY_SCHEMA + FIX_SKIPPED_ENTRY_SCHEMA
// + FIX_REFUSED_ENTRY_SCHEMA + FIX_SKIP_REASON + FIX_REFUSE_REASON +
// FIX_RUN_RECORD_EXAMPLE).
//
// Fix-run record — `docsync.fix-run-record/v1` (P7-T09; ARCH §6.3 parity with
// the run record; D3/D7/D10). Persisted by the Phase 7 `fix` flow at
// `<stateDir>/runs/fix-<run_id>.json` through the CAS store, this artifact is
// the idempotency + audit memory of an interactive repair run:
//
//   - `applied` rows carry `patch_ref` + `three_way_used` so provenance review
//     can replay exactly what landed (provenance-or-it-didn't-happen, §3 rule 10).
//   - `skipped` rows are the fail-closed lanes: a patch that failed
//     verification (`unverified`), a dirty working tree (`dirty`), a
//     locked/generated or protected-mode doc (`locked-or-protected`), a path
//     outside the manifest allowlist or a non-candidate outcome
//     (`out-of-scope` / `not-a-candidate`), and the A9 same-run replay skip
//     (`same-run-skip`).
//   - `refused` rows are the P7-T10 refuse-and-rerun lanes: `stale-patch`
//     (doc-changed race → re-detect exactly once), `unanchored` (ambiguous
//     re-anchoring → REVIEW, §3 rule 13), `abstain` (writer abstained — never
//     guessed). `re_detected` pins whether a fresh detection pass already ran.
//
// Like every docSync artifact this schema is closed (z.strictObject) and the
// record is written only AFTER the flow completes, via CAS — a concurrent
// writer conflicts and the losing run re-runs instead of overwriting
// (V-P7-13). Field naming mirrors `docsync.run-outcome/v1` (snake_case
// document rows) while the envelope stays strict (OD-2: import, never
// duplicate).
//
// ULID shape: the TS file declares ULID_REGEX (26-char Crockford base32,
// matching the core `generateUlid()` producer); the Go port reuses the single
// canonical predicate docutil.IsValidULID (same [0-9A-HJKMNP-TV-Z]{26}
// semantics) instead of duplicating the pattern, mirroring claim-proposal.go.

// FixRunRecordSchemaID is the wire schema literal (TS:
// FIX_RUN_RECORD_SCHEMA_ID, `z.literal('docsync.fix-run-record/v1')`).
const FixRunRecordSchemaID = "docsync.fix-run-record/v1"

// FixSkipReason is why a candidate document was not patched in this fix run
// (TS: FIX_SKIP_REASON_VALUES, z.infer FixSkipReason).
type FixSkipReason string

// FixSkipReasonValues lists all skip reasons in canonical order (TS:
// FIX_SKIP_REASON_VALUES).
const (
	FixSkipUnverified        FixSkipReason = "unverified"
	FixSkipDirty             FixSkipReason = "dirty"
	FixSkipLockedOrProtected FixSkipReason = "locked-or-protected"
	FixSkipOutOfScope        FixSkipReason = "out-of-scope"
	FixSkipNotACandidate     FixSkipReason = "not-a-candidate"
	FixSkipSameRunSkip       FixSkipReason = "same-run-skip"
)

// FixSkipReasonValues lists all skip reasons in canonical order.
var FixSkipReasonValues = []FixSkipReason{
	FixSkipUnverified, FixSkipDirty, FixSkipLockedOrProtected,
	FixSkipOutOfScope, FixSkipNotACandidate, FixSkipSameRunSkip,
}

// IsValidFixSkipReason reports membership in FIX_SKIP_REASON.
func IsValidFixSkipReason(s string) bool {
	for _, v := range FixSkipReasonValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// FixRefuseReason is why the flow refused to act on a candidate even after
// selection (TS: FIX_REFUSE_REASON_VALUES, z.infer FixRefuseReason).
type FixRefuseReason string

// FixRefuseReasonValues lists all refuse reasons in canonical order (TS:
// FIX_REFUSE_REASON_VALUES).
const (
	FixRefuseStalePatch FixRefuseReason = "stale-patch"
	FixRefuseUnanchored FixRefuseReason = "unanchored"
	FixRefuseAbstain    FixRefuseReason = "abstain"
)

// FixRefuseReasonValues lists all refuse reasons in canonical order.
var FixRefuseReasonValues = []FixRefuseReason{
	FixRefuseStalePatch, FixRefuseUnanchored, FixRefuseAbstain,
}

// IsValidFixRefuseReason reports membership in FIX_REFUSE_REASON.
func IsValidFixRefuseReason(s string) bool {
	for _, v := range FixRefuseReasonValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// FixAppliedEntry is one successfully applied patch (P7-T08; provenance §3
// rule 10; TS: FIX_APPLIED_ENTRY_SCHEMA, z.infer FixAppliedEntry).
type FixAppliedEntry struct {
	DocPath      string `json:"doc_path"`
	PatchRef     string `json:"patch_ref"`
	ThreeWayUsed bool   `json:"three_way_used"`
}

// Validate checks the entry against FIX_APPLIED_ENTRY_SCHEMA (strict).
func (e *FixAppliedEntry) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(e.DocPath, 1, []string{"doc_path"}))
	Collect(&issues, RequiredString(e.PatchRef, 1, []string{"patch_ref"}))
	return issues
}

// FixSkippedEntry is one candidate the flow skipped without touching the file
// (TS: FIX_SKIPPED_ENTRY_SCHEMA, z.infer FixSkippedEntry).
type FixSkippedEntry struct {
	DocPath string        `json:"doc_path"`
	Reason  FixSkipReason `json:"reason"`
	Detail  string        `json:"detail"`
}

// Validate checks the entry against FIX_SKIPPED_ENTRY_SCHEMA (strict).
func (e *FixSkippedEntry) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(e.DocPath, 1, []string{"doc_path"}))
	Collect(&issues, CheckEnum(string(e.Reason), stringsOf(FixSkipReasonValues), []string{"reason"}))
	Collect(&issues, RequiredString(e.Detail, 1, []string{"detail"}))
	return issues
}

// FixRefusedEntry is one candidate the flow refused to patch (P7-T10 lanes;
// TS: FIX_REFUSED_ENTRY_SCHEMA, z.infer FixRefusedEntry). `re_detected` pins
// whether a fresh detection pass already ran.
type FixRefusedEntry struct {
	DocPath    string          `json:"doc_path"`
	Reason     FixRefuseReason `json:"reason"`
	Detail     string          `json:"detail"`
	ReDetected bool            `json:"re_detected"`
}

// Validate checks the entry against FIX_REFUSED_ENTRY_SCHEMA (strict).
func (e *FixRefusedEntry) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(e.DocPath, 1, []string{"doc_path"}))
	Collect(&issues, CheckEnum(string(e.Reason), stringsOf(FixRefuseReasonValues), []string{"reason"}))
	Collect(&issues, RequiredString(e.Detail, 1, []string{"detail"}))
	return issues
}

// FixRunRecord is the `docsync.fix-run-record/v1` fix-run record envelope —
// written once per run via CAS (TS: FIX_RUN_RECORD_SCHEMA, z.infer
// FixRunRecord). Field order matches the TS declaration order so serialized
// bytes stay byte-stable with the TypeScript emitter.
type FixRunRecord struct {
	Schema        string            `json:"schema"`
	RunID         string            `json:"run_id"`
	At            string            `json:"at"`
	Repo          string            `json:"repo"`
	Base          string            `json:"base"`
	Head          string            `json:"head"`
	PolicyVersion int               `json:"policy_version"`
	Applied       []FixAppliedEntry `json:"applied"`
	Skipped       []FixSkippedEntry `json:"skipped"`
	Refused       []FixRefusedEntry `json:"refused"`
}

// Validate checks the record against FIX_RUN_RECORD_SCHEMA (strict),
// reproducing the zod issue paths.
func (r *FixRunRecord) Validate() []Issue {
	var issues []Issue
	if r.Schema != FixRunRecordSchemaID {
		msg := `Invalid literal value, expected "docsync.fix-run-record/v1"`
		if r.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	// TS: z.string().regex(ULID_REGEX) — a Crockford base32 ULID matching the
	// core generateUlid() producer.
	if !docutil.IsValidULID(r.RunID) {
		Collect(&issues, &Issue{Path: []string{"run_id"}, Message: "run_id must be a ULID (26-char Crockford base32)"})
	}
	// z.string().datetime() — 'Z' suffix only (no offset support), pinned by
	// the shared run-record.go helper.
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	Collect(&issues, RequiredString(r.Repo, 1, []string{"repo"}))
	Collect(&issues, RequiredString(r.Base, 1, []string{"base"}))
	Collect(&issues, RequiredString(r.Head, 1, []string{"head"}))
	if r.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	for i := range r.Applied {
		for _, iss := range r.Applied[i].Validate() {
			iss.Path = append([]string{"applied", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Skipped {
		for _, iss := range r.Skipped[i].Validate() {
			iss.Path = append([]string{"skipped", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Refused {
		for _, iss := range r.Refused[i].Validate() {
			iss.Path = append([]string{"refused", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// ParseFixRunRecord strictly decodes and validates
// `docsync.fix-run-record/v1` bytes (unknown JSON keys are rejected, mirroring
// z.strictObject).
func ParseFixRunRecord(data []byte) (*FixRunRecord, error) {
	var r FixRunRecord
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// FixRunRecordExample mirrors FIX_RUN_RECORD_EXAMPLE (validated example
// mirroring the Phase 7 fix flow outputs; golden data for byte-for-byte
// serializer checks).
func FixRunRecordExample() FixRunRecord {
	return FixRunRecord{
		Schema:        FixRunRecordSchemaID,
		RunID:         "01J9ZK7Q2M8P4X6T2W5Y3C0A9E",
		At:            "2026-10-07T18:00:00Z",
		Repo:          "acme/payments",
		Base:          "8a73f2c",
		Head:          "81af92e",
		PolicyVersion: 17,
		Applied: []FixAppliedEntry{
			{
				DocPath:      "docs/payments.md",
				PatchRef:     "patch_01J9ZK7Q2M8P4X6T2W5Y3C0A9E_docs-payments",
				ThreeWayUsed: false,
			},
		},
		Skipped: []FixSkippedEntry{
			{
				DocPath: "docs/api.md",
				Reason:  FixSkipUnverified,
				Detail:  "verification verdict fail — patch never applied (V-P7-15)",
			},
			{
				DocPath: "docs/locked.md",
				Reason:  FixSkipLockedOrProtected,
				Detail:  "locked document: detected and reported, never modified",
			},
			{
				DocPath: "docs/cli.md",
				Reason:  FixSkipNotACandidate,
				Detail:  "outcome review is not auto/suggest — routed to human review",
			},
		},
		Refused: []FixRefusedEntry{
			{
				DocPath:    "docs/race.md",
				Reason:     FixRefuseStalePatch,
				Detail:     "doc-changed-race-unverified — re-detection ran once, fresh patch refused",
				ReDetected: true,
			},
		},
	}
}
