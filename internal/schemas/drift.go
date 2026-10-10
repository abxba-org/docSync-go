// Ported from packages/schemas/src/v1/drift-report.ts AND
// packages/schemas/src/v1/drift-finding.ts (P11-T16/P11-T17/P11-T20;
// ARCH §4.3, §6.1, §7.2).
//
// The two halves of F11-9 (PLAN §12.3 "Drift & Loop B"):
//
//   - `docsync.drift-report/v1` (DRIFT_REPORT_SCHEMA) — the baseline
//     differential (P11-T16): the `baseline_commit..head` diff INTERSECTED
//     with the baseline's recorded sources/symbols/claims (ARCH §6.1 — "the
//     differential between current HEAD and the baseline commit is precisely
//     the evidence set that must be re-evaluated"; PRIN-2 — never blanket
//     scanning). Produced deterministically, zero LLM.
//   - `docsync.claim-support-report/v1` (CLAIM_SUPPORT_REPORT_SCHEMA) — the
//     claim-support sweep (P11-T17), the headline query: "Which documented
//     claims lost support in the source of truth?" (ARCH §4.3). Per-claim
//     verdicts plus, when the caller explicitly opts into state flips, the
//     transitions applied through the claim state machine's CAS functions and
//     every CAS conflict surfaced (never a silent write; R15).
//
// Both are QUERY artifacts — advisory evidence, never state-machine records;
// the state machine's own artifacts (claims, doc-states) are the only status
// truth. This module shapes data; core enforces the CAS/append-only semantics.
//
// Loop B background-drift-finding records (P11-T20):
//
//   - `docsync.drift-finding/v1` (DRIFT_FINDING_SCHEMA) — evidence-carrying
//     ADVISORY DATA (claim IDs, commits, confidence; ARCH §7.2) — never a
//     write instruction: the audit engine performs NO document writes and NO
//     patch application ever.
//   - `docsync.drift-audit-run-record/v1` (DRIFT_AUDIT_RUN_RECORD_SCHEMA) —
//     one audit run's provenance record ("provenance or it didn't happen"):
//     what triggered the run, the cursor position before/after, which manifest
//     docs were audited vs skipped (D7 scope), the run status, and the exact
//     deduplicated findings it produced.
//
// Wire keys stay snake_case exactly as zod declared them; z.strictObject ⇒
// unknown JSON keys are rejected at parse (StrictDecode), and every zod
// validation message is reproduced in Validate() via Issue paths (see
// decode.go for the zod→Go mapping rules). The D12 ladder rung vocabulary
// and the audit-trigger/status vocabularies are mirrored verbatim from core
// (this package cannot import core — dependency direction; drift is caught by
// the registry example validation).
package schemas

import "encoding/json"

/* ---------------------------------------------- drift report (P11-T16) */

// DriftReportSchemaID is the wire literal carried by the `schema` field
// (DRIFT_REPORT_SCHEMA_ID, z.literal('docsync.drift-report/v1')).
const DriftReportSchemaID = "docsync.drift-report/v1"

// LadderRun is the D12 ladder rung vocabulary (LADDER_RUN_VALUES).
type LadderRun string

// The ladder rungs in canonical order (LADDER_RUN_VALUES).
const (
	LadderRunExactSHA        LadderRun = "exact-sha"
	LadderRunSameTree        LadderRun = "same-tree"
	LadderRunAncestor        LadderRun = "ancestor"
	LadderRunPatchIDMatch    LadderRun = "patch-id-match"
	LadderRunOrphanedSuspect LadderRun = "orphaned-suspect"
)

// LadderRunValues lists every ladder rung in canonical order
// (LADDER_RUN_VALUES).
var LadderRunValues = []LadderRun{
	LadderRunExactSHA, LadderRunSameTree, LadderRunAncestor,
	LadderRunPatchIDMatch, LadderRunOrphanedSuspect,
}

// IsValidLadderRun reports membership in LADDER_RUN.
func IsValidLadderRun(r string) bool {
	for _, v := range LadderRunValues {
		if string(v) == r {
			return true
		}
	}
	return false
}

// DriftSourceStatus is one per-source drift fact — `unchanged` plus the diff
// statuses (DRIFT_SOURCE_STATUS_VALUES; the file statuses come from
// change-analysis.ts).
type DriftSourceStatus string

// The drift source statuses in canonical order (DRIFT_SOURCE_STATUS_VALUES —
// 'unchanged' first, then FILE_STATUS_VALUES).
const (
	DriftSourceStatusUnchanged DriftSourceStatus = "unchanged"
	DriftSourceStatusAdded     DriftSourceStatus = "added"
	DriftSourceStatusModified  DriftSourceStatus = "modified"
	DriftSourceStatusDeleted   DriftSourceStatus = "deleted"
	DriftSourceStatusRenamed   DriftSourceStatus = "renamed"
)

// DriftSourceStatusValues lists every drift source status in canonical order
// (DRIFT_SOURCE_STATUS_VALUES) — 'unchanged' followed by the file statuses,
// reusing FileStatusValues so the two vocabularies cannot drift apart.
var DriftSourceStatusValues = driftSourceStatusValues()

func driftSourceStatusValues() []DriftSourceStatus {
	out := make([]DriftSourceStatus, 0, len(FileStatusValues)+1)
	out = append(out, DriftSourceStatusUnchanged)
	for _, s := range FileStatusValues {
		out = append(out, DriftSourceStatus(s))
	}
	return out
}

// IsValidDriftSourceStatus reports membership in DRIFT_SOURCE_STATUS.
func IsValidDriftSourceStatus(s string) bool {
	for _, v := range DriftSourceStatusValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// DriftSourceFact is one baseline-recorded source file's drift fact
// (DRIFT_SOURCE_FACT_SCHEMA). Only files RECORDED in the baseline appear (the
// intersection is enforced upstream by the producer); `additions`/`deletions`
// are the numstat counts for a changed file (null for binary content) and are
// absent for `unchanged` files.
type DriftSourceFact struct {
	Path      string            `json:"path"`
	Status    DriftSourceStatus `json:"status"`
	OldPath   *string           `json:"old_path,omitempty"`
	Additions *int              `json:"additions,omitempty"`
	Deletions *int              `json:"deletions,omitempty"`
}

// Validate checks the fact against DRIFT_SOURCE_FACT_SCHEMA (strict).
func (f *DriftSourceFact) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(f.Path, 1, []string{"path"}))
	Collect(&issues, CheckEnum(string(f.Status), stringsOf(DriftSourceStatusValues), []string{"status"}))
	if f.OldPath != nil {
		Collect(&issues, RequiredString(*f.OldPath, 1, []string{"old_path"}))
	}
	if f.Additions != nil && *f.Additions < 0 {
		Collect(&issues, &Issue{Path: []string{"additions"}, Message: "Number must be greater than or equal to 0"})
	}
	if f.Deletions != nil && *f.Deletions < 0 {
		Collect(&issues, &Issue{Path: []string{"deletions"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// DriftSymbolFactKind is the symbol drift outcome (the inline
// z.enum(['symbol-present', 'symbol-absent']) of
// DRIFT_SYMBOL_FACT_SCHEMA): `symbol-absent` = the name no longer appears in
// the head blob of the file it lived in at baseline (removed/renamed);
// `symbol-present` = the file changed but the symbol name is still there
// (text-level drift only).
type DriftSymbolFactKind string

// The symbol fact kinds in canonical order.
const (
	DriftSymbolPresent DriftSymbolFactKind = "symbol-present"
	DriftSymbolAbsent  DriftSymbolFactKind = "symbol-absent"
)

// DriftSymbolFactKindValues lists every symbol fact kind in canonical order.
var DriftSymbolFactKindValues = []DriftSymbolFactKind{DriftSymbolPresent, DriftSymbolAbsent}

// IsValidDriftSymbolFactKind reports membership in the symbol-fact enum.
func IsValidDriftSymbolFactKind(k string) bool {
	for _, v := range DriftSymbolFactKindValues {
		if string(v) == k {
			return true
		}
	}
	return false
}

// DriftSymbolFact is a symbol-name drift fact: a baseline-recorded symbol
// name checked against a CHANGED recorded source file's head content
// (DRIFT_SYMBOL_FACT_SCHEMA).
type DriftSymbolFact struct {
	Name string              `json:"name"`
	Path string              `json:"path"`
	Kind DriftSymbolFactKind `json:"kind"`
}

// Validate checks the fact against DRIFT_SYMBOL_FACT_SCHEMA (strict).
func (f *DriftSymbolFact) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(f.Name, 1, []string{"name"}))
	Collect(&issues, RequiredString(f.Path, 1, []string{"path"}))
	Collect(&issues, CheckEnum(string(f.Kind), stringsOf(DriftSymbolFactKindValues), []string{"kind"}))
	return issues
}

// DriftClaimVerdict is the per-claim drift verdict (DRIFT_CLAIM_VERDICT_VALUES):
//   - `unchanged`      — the claim's source blob identity is intact at head.
//   - `source-changed` — the source file changed but the recorded backing blob
//     sha is STILL the blob at head (git content identity — the claim's own
//     bytes survive; support is not yet refuted).
//   - `support-moved`  — the source file's head blob differs from the recorded
//     `source.blob_sha`: the recorded backing bytes no longer exist at head
//     (ARCH §4.3 direct-invalidation posture → proposed status `suspect`).
//   - `source-missing` — the source file does not exist at head (deleted).
type DriftClaimVerdict string

// The claim verdicts in canonical order (DRIFT_CLAIM_VERDICT_VALUES).
const (
	DriftClaimUnchanged     DriftClaimVerdict = "unchanged"
	DriftClaimSourceChanged DriftClaimVerdict = "source-changed"
	DriftClaimSupportMoved  DriftClaimVerdict = "support-moved"
	DriftClaimSourceMissing DriftClaimVerdict = "source-missing"
)

// DriftClaimVerdictValues lists every claim verdict in canonical order
// (DRIFT_CLAIM_VERDICT_VALUES).
var DriftClaimVerdictValues = []DriftClaimVerdict{
	DriftClaimUnchanged, DriftClaimSourceChanged, DriftClaimSupportMoved, DriftClaimSourceMissing,
}

// IsValidDriftClaimVerdict reports membership in DRIFT_CLAIM_VERDICT.
func IsValidDriftClaimVerdict(v string) bool {
	for _, x := range DriftClaimVerdictValues {
		if string(x) == v {
			return true
		}
	}
	return false
}

// DriftClaimFact is one per-claim drift verdict — deterministic,
// evidence-backed (DRIFT_CLAIM_FACT_SCHEMA).
type DriftClaimFact struct {
	ClaimID             string            `json:"claim_id"`
	Verdict             DriftClaimVerdict `json:"verdict"`
	PreviousStatus      string            `json:"previous_status"`
	ProposedStatus      *string           `json:"proposed_status,omitempty"`
	SourceBlobSHA       string            `json:"source_blob_sha"`
	SourceBlobSHAAtHead *string           `json:"source_blob_sha_at_head"`
	EvidencePaths       []string          `json:"evidence_paths"`
}

// Validate checks the fact against DRIFT_CLAIM_FACT_SCHEMA (strict).
func (f *DriftClaimFact) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(f.ClaimID, 1, []string{"claim_id"}))
	Collect(&issues, CheckEnum(string(f.Verdict), stringsOf(DriftClaimVerdictValues), []string{"verdict"}))
	Collect(&issues, CheckEnum(f.PreviousStatus, ClaimStatusValues, []string{"previous_status"}))
	if f.ProposedStatus != nil {
		Collect(&issues, CheckEnum(*f.ProposedStatus, ClaimStatusValues, []string{"proposed_status"}))
	}
	Collect(&issues, RequiredString(f.SourceBlobSHA, 1, []string{"source_blob_sha"}))
	for i, p := range f.EvidencePaths {
		Collect(&issues, RequiredString(p, 1, []string{"evidence_paths", itoa(i)}))
	}
	return issues
}

// DriftReportSummary is the differential's counters (DRIFT_REPORT_SUMMARY_SCHEMA).
type DriftReportSummary struct {
	SourcesChanged int `json:"sources_changed"`
	SymbolsDrifted int `json:"symbols_drifted"`
	ClaimsDrifted  int `json:"claims_drifted"`
}

// Validate checks the counters against DRIFT_REPORT_SUMMARY_SCHEMA (strict).
func (s *DriftReportSummary) Validate() []Issue {
	var issues []Issue
	if s.SourcesChanged < 0 {
		Collect(&issues, &Issue{Path: []string{"sources_changed"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.SymbolsDrifted < 0 {
		Collect(&issues, &Issue{Path: []string{"symbols_drifted"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.ClaimsDrifted < 0 {
		Collect(&issues, &Issue{Path: []string{"claims_drifted"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// DriftReport is the `docsync.drift-report/v1` baseline differential
// (DRIFT_REPORT_SCHEMA).
type DriftReport struct {
	Schema         string             `json:"schema"`
	RunID          string             `json:"run_id"`
	At             string             `json:"at"`
	Head           string             `json:"head"`
	BaselineCommit string             `json:"baseline_commit"`
	LadderRun      LadderRun          `json:"ladder_run"`
	LadderEvidence string             `json:"ladder_evidence"`
	DocPath        string             `json:"doc_path"`
	Sources        []DriftSourceFact  `json:"sources"`
	Symbols        []DriftSymbolFact  `json:"symbols"`
	Claims         []DriftClaimFact   `json:"claims"`
	Summary        DriftReportSummary `json:"summary"`
}

// Validate checks the report against DRIFT_REPORT_SCHEMA (strict),
// reproducing the zod issue paths.
func (r *DriftReport) Validate() []Issue {
	var issues []Issue
	if r.Schema != DriftReportSchemaID {
		msg := `Invalid literal value, expected "` + DriftReportSchemaID + `"`
		if r.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	Collect(&issues, RequiredString(r.Head, 1, []string{"head"}))
	Collect(&issues, RequiredString(r.BaselineCommit, 1, []string{"baseline_commit"}))
	Collect(&issues, CheckEnum(string(r.LadderRun), stringsOf(LadderRunValues), []string{"ladder_run"}))
	Collect(&issues, RequiredString(r.LadderEvidence, 1, []string{"ladder_evidence"}))
	Collect(&issues, RequiredString(r.DocPath, 1, []string{"doc_path"}))
	for i := range r.Sources {
		for _, iss := range r.Sources[i].Validate() {
			iss.Path = append([]string{"sources", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Symbols {
		for _, iss := range r.Symbols[i].Validate() {
			iss.Path = append([]string{"symbols", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Claims {
		for _, iss := range r.Claims[i].Validate() {
			iss.Path = append([]string{"claims", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range r.Summary.Validate() {
		iss.Path = append([]string{"summary"}, iss.Path...)
		issues = append(issues, iss)
	}
	return issues
}

// ParseDriftReport strictly parses and validates a `docsync.drift-report/v1`
// document, mirroring the zod strictObject contract (unknown keys rejected;
// absent required root keys rejected with zod's "Required").
func ParseDriftReport(data []byte) (*DriftReport, error) {
	var r DriftReport
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := requireRootKeys(data,
		"schema", "run_id", "at", "head", "baseline_commit", "ladder_run", "ladder_evidence",
		"doc_path", "sources", "symbols", "claims", "summary"); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// DriftReportExample is the deterministic example (DRIFT_REPORT_EXAMPLE).
var DriftReportExample = DriftReport{
	Schema:         DriftReportSchemaID,
	RunID:          "run_01J9ZK6X8A",
	At:             "2026-10-08T22:31:00Z",
	Head:           "81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5",
	BaselineCommit: "8a73f2c1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7",
	LadderRun:      LadderRunAncestor,
	LadderEvidence: "merge-base 8a73f2c1b2c3 equals the baseline commit 8a73f2c1b2c3",
	DocPath:        "docs/payments.md",
	Sources: []DriftSourceFact{
		{
			Path:      "src/payments/refund.ts",
			Status:    DriftSourceStatusModified,
			Additions: intPtr(2),
			Deletions: intPtr(1),
		},
		{Path: "src/api/payments/client.ts", Status: DriftSourceStatusUnchanged},
	},
	Symbols: []DriftSymbolFact{
		{Name: "refundRequest", Path: "src/payments/refund.ts", Kind: DriftSymbolPresent},
	},
	Claims: []DriftClaimFact{
		{
			ClaimID:             "claim_182",
			Verdict:             DriftClaimSupportMoved,
			PreviousStatus:      ClaimStatusVerified,
			ProposedStatus:      strPtr(ClaimStatusSuspect),
			SourceBlobSHA:       "e0d1fa5",
			SourceBlobSHAAtHead: strPtr("b7c2d3e"),
			EvidencePaths:       []string{"src/payments/refund.ts"},
		},
	},
	Summary: DriftReportSummary{SourcesChanged: 1, SymbolsDrifted: 0, ClaimsDrifted: 1},
}

/* --------------------------------------------------- claim-support report */

// ClaimSupportReportSchemaID is the wire literal carried by the `schema`
// field (CLAIM_SUPPORT_REPORT_SCHEMA_ID,
// z.literal('docsync.claim-support-report/v1')).
const ClaimSupportReportSchemaID = "docsync.claim-support-report/v1"

// ClaimSupportVerdict is the sweep verdict (CLAIM_SUPPORT_VERDICT_VALUES):
// `lost-support` covers both refuted support (backing blob moved / source
// missing) and rule-table-refuted symbol evidence; `unresolvable-anchor`
// covers anchor resolution failures (missing doc, content hash
// nowhere/ambiguous — routes REVIEW per invariant 13, never a silent
// re-anchor).
type ClaimSupportVerdict string

// The sweep verdicts in canonical order (CLAIM_SUPPORT_VERDICT_VALUES).
const (
	ClaimSupportSupported          ClaimSupportVerdict = "supported"
	ClaimSupportLostSupport        ClaimSupportVerdict = "lost-support"
	ClaimSupportUnresolvableAnchor ClaimSupportVerdict = "unresolvable-anchor"
)

// ClaimSupportVerdictValues lists every sweep verdict in canonical order
// (CLAIM_SUPPORT_VERDICT_VALUES).
var ClaimSupportVerdictValues = []ClaimSupportVerdict{
	ClaimSupportSupported, ClaimSupportLostSupport, ClaimSupportUnresolvableAnchor,
}

// IsValidClaimSupportVerdict reports membership in CLAIM_SUPPORT_VERDICT.
func IsValidClaimSupportVerdict(v string) bool {
	for _, x := range ClaimSupportVerdictValues {
		if string(x) == v {
			return true
		}
	}
	return false
}

// ClaimSupportAnchorResolution is the anchor resolution outcome
// (CLAIM_SUPPORT_ANCHOR_RESOLUTION_VALUES), mirrored from core's
// claims/anchor.ts.
type ClaimSupportAnchorResolution string

// The anchor resolutions in canonical order
// (CLAIM_SUPPORT_ANCHOR_RESOLUTION_VALUES).
const (
	ClaimSupportAnchorUnchanged  ClaimSupportAnchorResolution = "unchanged"
	ClaimSupportAnchorReAnchored ClaimSupportAnchorResolution = "re-anchored"
	ClaimSupportAnchorUnanchored ClaimSupportAnchorResolution = "unanchored"
)

// ClaimSupportAnchorResolutionValues lists every anchor resolution in
// canonical order (CLAIM_SUPPORT_ANCHOR_RESOLUTION_VALUES).
var ClaimSupportAnchorResolutionValues = []ClaimSupportAnchorResolution{
	ClaimSupportAnchorUnchanged, ClaimSupportAnchorReAnchored, ClaimSupportAnchorUnanchored,
}

// IsValidClaimSupportAnchorResolution reports membership in the anchor
// resolution vocabulary.
func IsValidClaimSupportAnchorResolution(r string) bool {
	for _, v := range ClaimSupportAnchorResolutionValues {
		if string(v) == r {
			return true
		}
	}
	return false
}

// ClaimSupportEvidence is one entry's evidence block
// (CLAIM_SUPPORT_EVIDENCE_SCHEMA).
type ClaimSupportEvidence struct {
	ClaimID             string                       `json:"claim_id"`
	AnchorFile          string                       `json:"anchor_file"`
	AnchorStartLine     int                          `json:"anchor_start_line"`
	AnchorEndLine       int                          `json:"anchor_end_line"`
	AnchorResolution    ClaimSupportAnchorResolution `json:"anchor_resolution"`
	SourceFile          string                       `json:"source_file"`
	SourceLine          *int                         `json:"source_line,omitempty"`
	SourceBlobSHA       string                       `json:"source_blob_sha"`
	SourceBlobSHAAtHead *string                      `json:"source_blob_sha_at_head"`
	Commits             []string                     `json:"commits"`
	Detail              string                       `json:"detail"`
}

// Validate checks the evidence against CLAIM_SUPPORT_EVIDENCE_SCHEMA (strict).
func (e *ClaimSupportEvidence) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(e.ClaimID, 1, []string{"claim_id"}))
	Collect(&issues, RequiredString(e.AnchorFile, 1, []string{"anchor_file"}))
	if e.AnchorStartLine < 1 {
		Collect(&issues, &Issue{Path: []string{"anchor_start_line"}, Message: "Number must be greater than or equal to 1"})
	}
	if e.AnchorEndLine < 1 {
		Collect(&issues, &Issue{Path: []string{"anchor_end_line"}, Message: "Number must be greater than or equal to 1"})
	}
	Collect(&issues, CheckEnum(string(e.AnchorResolution), stringsOf(ClaimSupportAnchorResolutionValues), []string{"anchor_resolution"}))
	Collect(&issues, RequiredString(e.SourceFile, 1, []string{"source_file"}))
	if e.SourceLine != nil && *e.SourceLine < 1 {
		Collect(&issues, &Issue{Path: []string{"source_line"}, Message: "Number must be greater than or equal to 1"})
	}
	Collect(&issues, RequiredString(e.SourceBlobSHA, 1, []string{"source_blob_sha"}))
	for i, c := range e.Commits {
		Collect(&issues, RequiredString(c, 1, []string{"commits", itoa(i)}))
	}
	Collect(&issues, RequiredString(e.Detail, 1, []string{"detail"}))
	return issues
}

// ClaimSupportEntry is one per-claim sweep verdict
// (CLAIM_SUPPORT_ENTRY_SCHEMA).
type ClaimSupportEntry struct {
	ClaimID      string               `json:"claim_id"`
	Verdict      ClaimSupportVerdict  `json:"verdict"`
	StatusBefore string               `json:"status_before"`
	Reason       string               `json:"reason"`
	Evidence     ClaimSupportEvidence `json:"evidence"`
}

// Validate checks the entry against CLAIM_SUPPORT_ENTRY_SCHEMA (strict).
func (e *ClaimSupportEntry) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(e.ClaimID, 1, []string{"claim_id"}))
	Collect(&issues, CheckEnum(string(e.Verdict), stringsOf(ClaimSupportVerdictValues), []string{"verdict"}))
	Collect(&issues, CheckEnum(e.StatusBefore, ClaimStatusValues, []string{"status_before"}))
	Collect(&issues, RequiredString(e.Reason, 1, []string{"reason"}))
	for _, iss := range e.Evidence.Validate() {
		iss.Path = append([]string{"evidence"}, iss.Path...)
		issues = append(issues, iss)
	}
	return issues
}

// ClaimSupportTransition is the state-flip transition vocabulary
// (CLAIM_SUPPORT_TRANSITION_VALUES) — always via the claim state machine's
// own CAS transition functions (claims/status.ts), never a direct store write.
type ClaimSupportTransition string

// The flip transitions in canonical order (CLAIM_SUPPORT_TRANSITION_VALUES).
const (
	ClaimSupportTransitionLoseSupport    ClaimSupportTransition = "loseSupport"
	ClaimSupportTransitionMarkUnanchored ClaimSupportTransition = "markUnanchored"
)

// ClaimSupportTransitionValues lists every flip transition in canonical order
// (CLAIM_SUPPORT_TRANSITION_VALUES).
var ClaimSupportTransitionValues = []ClaimSupportTransition{
	ClaimSupportTransitionLoseSupport, ClaimSupportTransitionMarkUnanchored,
}

// IsValidClaimSupportTransition reports membership in the transition
// vocabulary.
func IsValidClaimSupportTransition(t string) bool {
	for _, v := range ClaimSupportTransitionValues {
		if string(v) == t {
			return true
		}
	}
	return false
}

// ClaimSupportFlip is one applied state flip (CLAIM_SUPPORT_FLIP_SCHEMA).
type ClaimSupportFlip struct {
	ClaimID    string                 `json:"claim_id"`
	From       string                 `json:"from"`
	To         string                 `json:"to"`
	Transition ClaimSupportTransition `json:"transition"`
	Event      string                 `json:"event"`
	Hash       string                 `json:"hash"`
}

// Validate checks the flip against CLAIM_SUPPORT_FLIP_SCHEMA (strict).
func (f *ClaimSupportFlip) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(f.ClaimID, 1, []string{"claim_id"}))
	Collect(&issues, CheckEnum(f.From, ClaimStatusValues, []string{"from"}))
	Collect(&issues, CheckEnum(f.To, ClaimStatusValues, []string{"to"}))
	Collect(&issues, CheckEnum(string(f.Transition), stringsOf(ClaimSupportTransitionValues), []string{"transition"}))
	Collect(&issues, RequiredString(f.Event, 1, []string{"event"}))
	Collect(&issues, RequiredString(f.Hash, 1, []string{"hash"}))
	return issues
}

// ClaimSupportConflict is a CAS conflict surfaced by the machine's own
// conflict shape — never a crash (CLAIM_SUPPORT_CONFLICT_SCHEMA; R15).
type ClaimSupportConflict struct {
	ClaimID      string                 `json:"claim_id"`
	Transition   ClaimSupportTransition `json:"transition"`
	ExpectedHash *string                `json:"expected_hash"`
	ActualHash   *string                `json:"actual_hash"`
}

// Validate checks the conflict against CLAIM_SUPPORT_CONFLICT_SCHEMA (strict).
func (c *ClaimSupportConflict) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.ClaimID, 1, []string{"claim_id"}))
	Collect(&issues, CheckEnum(string(c.Transition), stringsOf(ClaimSupportTransitionValues), []string{"transition"}))
	return issues
}

// ClaimSupportSummary is the sweep's counters (CLAIM_SUPPORT_SUMMARY_SCHEMA).
type ClaimSupportSummary struct {
	Total              int `json:"total"`
	Supported          int `json:"supported"`
	LostSupport        int `json:"lost_support"`
	UnresolvableAnchor int `json:"unresolvable_anchor"`
	Flipped            int `json:"flipped"`
}

// Validate checks the counters against CLAIM_SUPPORT_SUMMARY_SCHEMA (strict).
func (s *ClaimSupportSummary) Validate() []Issue {
	var issues []Issue
	if s.Total < 0 {
		Collect(&issues, &Issue{Path: []string{"total"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.Supported < 0 {
		Collect(&issues, &Issue{Path: []string{"supported"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.LostSupport < 0 {
		Collect(&issues, &Issue{Path: []string{"lost_support"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.UnresolvableAnchor < 0 {
		Collect(&issues, &Issue{Path: []string{"unresolvable_anchor"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.Flipped < 0 {
		Collect(&issues, &Issue{Path: []string{"flipped"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// ClaimSupportReport is the `docsync.claim-support-report/v1` sweep artifact
// (CLAIM_SUPPORT_REPORT_SCHEMA).
type ClaimSupportReport struct {
	Schema    string                 `json:"schema"`
	RunID     string                 `json:"run_id"`
	At        string                 `json:"at"`
	Head      string                 `json:"head"`
	Applied   bool                   `json:"applied"`
	Claims    []ClaimSupportEntry    `json:"claims"`
	Flips     []ClaimSupportFlip     `json:"flips"`
	Conflicts []ClaimSupportConflict `json:"conflicts"`
	Summary   ClaimSupportSummary    `json:"summary"`
}

// Validate checks the report against CLAIM_SUPPORT_REPORT_SCHEMA (strict),
// reproducing the zod issue paths.
func (r *ClaimSupportReport) Validate() []Issue {
	var issues []Issue
	if r.Schema != ClaimSupportReportSchemaID {
		msg := `Invalid literal value, expected "` + ClaimSupportReportSchemaID + `"`
		if r.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	Collect(&issues, RequiredString(r.Head, 1, []string{"head"}))
	for i := range r.Claims {
		for _, iss := range r.Claims[i].Validate() {
			iss.Path = append([]string{"claims", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Flips {
		for _, iss := range r.Flips[i].Validate() {
			iss.Path = append([]string{"flips", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Conflicts {
		for _, iss := range r.Conflicts[i].Validate() {
			iss.Path = append([]string{"conflicts", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range r.Summary.Validate() {
		iss.Path = append([]string{"summary"}, iss.Path...)
		issues = append(issues, iss)
	}
	return issues
}

// ParseClaimSupportReport strictly parses and validates a
// `docsync.claim-support-report/v1` document, mirroring the zod strictObject
// contract (unknown keys rejected; absent required root keys rejected with
// zod's "Required").
func ParseClaimSupportReport(data []byte) (*ClaimSupportReport, error) {
	var r ClaimSupportReport
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := requireRootKeys(data,
		"schema", "run_id", "at", "head", "applied", "claims", "flips", "conflicts", "summary"); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// ClaimSupportReportExample is the deterministic example
// (CLAIM_SUPPORT_REPORT_EXAMPLE).
var ClaimSupportReportExample = ClaimSupportReport{
	Schema:  ClaimSupportReportSchemaID,
	RunID:   "run_01J9ZK6X8A",
	At:      "2026-10-08T22:31:00Z",
	Head:    "81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5",
	Applied: false,
	Claims: []ClaimSupportEntry{
		{
			ClaimID:      "claim_182",
			Verdict:      ClaimSupportLostSupport,
			StatusBefore: ClaimStatusVerified,
			Reason:       "source blob moved at head (recorded e0d1fa5, head b7c2d3e)",
			Evidence: ClaimSupportEvidence{
				ClaimID:             "claim_182",
				AnchorFile:          "docs/payments.md",
				AnchorStartLine:     3,
				AnchorEndLine:       3,
				AnchorResolution:    ClaimSupportAnchorUnchanged,
				SourceFile:          "src/payments/refund.ts",
				SourceLine:          intPtr(42),
				SourceBlobSHA:       "e0d1fa5",
				SourceBlobSHAAtHead: strPtr("b7c2d3e"),
				Commits:             []string{"81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5"},
				Detail:              "recorded backing bytes no longer exist at head (ARCH §4.3)",
			},
		},
		{
			ClaimID:      "claim_190",
			Verdict:      ClaimSupportSupported,
			StatusBefore: ClaimStatusVerified,
			Reason:       "source blob identity intact at head",
			Evidence: ClaimSupportEvidence{
				ClaimID:             "claim_190",
				AnchorFile:          "docs/payments.md",
				AnchorStartLine:     5,
				AnchorEndLine:       5,
				AnchorResolution:    ClaimSupportAnchorUnchanged,
				SourceFile:          "src/api/payments/client.ts",
				SourceLine:          intPtr(7),
				SourceBlobSHA:       "c0ffee1",
				SourceBlobSHAAtHead: strPtr("c0ffee1"),
				Commits:             []string{},
				Detail:              "blob identity matches the recorded source anchor",
			},
		},
	},
	Flips:     []ClaimSupportFlip{},
	Conflicts: []ClaimSupportConflict{},
	Summary:   ClaimSupportSummary{Total: 2, Supported: 1, LostSupport: 1, UnresolvableAnchor: 0, Flipped: 0},
}

/* ------------------------------------------------------- drift finding */

// DriftFindingSchemaID is the wire literal carried by the `schema` field
// (DRIFT_FINDING_SCHEMA_ID, z.literal('docsync.drift-finding/v1')).
const DriftFindingSchemaID = "docsync.drift-finding/v1"

// DriftFindingKind is the closed drift-kind vocabulary
// (DRIFT_FINDING_KIND_VALUES). Grounded in the deterministic drift producers
// that exist today and extensible additively:
//   - `claim-lost-support` — a documented claim's backing source evidence
//     moved or disappeared (P11-T16 differential / P11-T17 sweep verdicts
//     `support-moved` / `source-missing` / `lost-support`).
//   - `claim-unanchored` — anchor resolution failed (content hash nowhere or
//     ambiguous); routes REVIEW, never a silent re-anchor (invariant 13).
//   - `doc-drift` — document-level baseline differential drift (P11-T16).
//   - `symbol-absent` — a baseline-recorded symbol name is gone at head
//     (P11-T16 `symbol-absent` fact).
type DriftFindingKind string

// The drift kinds in canonical order (DRIFT_FINDING_KIND_VALUES).
const (
	DriftFindingClaimLostSupport DriftFindingKind = "claim-lost-support"
	DriftFindingClaimUnanchored  DriftFindingKind = "claim-unanchored"
	DriftFindingDocDrift         DriftFindingKind = "doc-drift"
	DriftFindingSymbolAbsent     DriftFindingKind = "symbol-absent"
)

// DriftFindingKindValues lists every drift kind in canonical order
// (DRIFT_FINDING_KIND_VALUES).
var DriftFindingKindValues = []DriftFindingKind{
	DriftFindingClaimLostSupport, DriftFindingClaimUnanchored, DriftFindingDocDrift, DriftFindingSymbolAbsent,
}

// IsValidDriftFindingKind reports membership in DRIFT_FINDING_KIND.
func IsValidDriftFindingKind(k string) bool {
	for _, v := range DriftFindingKindValues {
		if string(v) == k {
			return true
		}
	}
	return false
}

// DriftFindingEvidence is the evidence triple every finding carries
// (DRIFT_FINDING_EVIDENCE_SCHEMA; ARCH §7.2: commits, paths, detail).
type DriftFindingEvidence struct {
	Commits []string `json:"commits"`
	Paths   []string `json:"paths"`
	Detail  string   `json:"detail"`
}

// Validate checks the evidence against DRIFT_FINDING_EVIDENCE_SCHEMA (strict).
func (e *DriftFindingEvidence) Validate() []Issue {
	var issues []Issue
	for i, c := range e.Commits {
		Collect(&issues, RequiredString(c, 1, []string{"commits", itoa(i)}))
	}
	for i, p := range e.Paths {
		Collect(&issues, RequiredString(p, 1, []string{"paths", itoa(i)}))
	}
	Collect(&issues, RequiredString(e.Detail, 1, []string{"detail"}))
	return issues
}

// DriftFindingRecord is the `docsync.drift-finding/v1` record
// (DRIFT_FINDING_SCHEMA) — evidence-carrying ADVISORY DATA, never a write
// instruction.
type DriftFindingRecord struct {
	Schema     string               `json:"schema"`
	ID         string               `json:"id"`
	ClaimID    *string              `json:"claim_id,omitempty"`
	DocPath    string               `json:"doc_path"`
	Kind       DriftFindingKind     `json:"kind"`
	Evidence   DriftFindingEvidence `json:"evidence"`
	Confidence *float64             `json:"confidence,omitempty"`
	CreatedAt  string               `json:"created_at"`
}

// Validate checks the record against DRIFT_FINDING_SCHEMA (strict).
func (f *DriftFindingRecord) Validate() []Issue {
	var issues []Issue
	if f.Schema != DriftFindingSchemaID {
		msg := `Invalid literal value, expected "` + DriftFindingSchemaID + `"`
		if f.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	Collect(&issues, RequiredString(f.ID, 1, []string{"id"}))
	if f.ClaimID != nil {
		Collect(&issues, RequiredString(*f.ClaimID, 1, []string{"claim_id"}))
	}
	Collect(&issues, RequiredString(f.DocPath, 1, []string{"doc_path"}))
	Collect(&issues, CheckEnum(string(f.Kind), stringsOf(DriftFindingKindValues), []string{"kind"}))
	for _, iss := range f.Evidence.Validate() {
		iss.Path = append([]string{"evidence"}, iss.Path...)
		issues = append(issues, iss)
	}
	if f.Confidence != nil {
		if *f.Confidence < 0 {
			Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be greater than or equal to 0"})
		}
		if *f.Confidence > 1 {
			Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be less than or equal to 1"})
		}
	}
	Collect(&issues, datetimeIssue(f.CreatedAt, []string{"created_at"}))
	return issues
}

// ParseDriftFindingRecord strictly parses and validates a
// `docsync.drift-finding/v1` document, mirroring the zod strictObject
// contract (unknown keys rejected; absent required root keys rejected with
// zod's "Required").
func ParseDriftFindingRecord(data []byte) (*DriftFindingRecord, error) {
	var f DriftFindingRecord
	if err := StrictDecode(data, &f); err != nil {
		return nil, err
	}
	if issues := requireRootKeys(data,
		"schema", "id", "doc_path", "kind", "evidence", "created_at"); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	if issues := f.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &f, nil
}

// DriftFindingExample is the deterministic example (DRIFT_FINDING_EXAMPLE).
var DriftFindingExample = DriftFindingRecord{
	Schema:  DriftFindingSchemaID,
	ID:      "finding_claim_182_docs_payments_md",
	ClaimID: strPtr("claim_182"),
	DocPath: "docs/payments.md",
	Kind:    DriftFindingClaimLostSupport,
	Evidence: DriftFindingEvidence{
		Commits: []string{"81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5"},
		Paths:   []string{"src/payments/refund.ts"},
		Detail:  "recorded backing bytes no longer exist at head (ARCH §4.3)",
	},
	Confidence: floatPtr(0.9),
	CreatedAt:  "2026-10-09T00:00:00.000Z",
}

/* -------------------------------------------------------- audit run record */

// DriftAuditRunRecordSchemaID is the wire literal carried by the `schema`
// field (DRIFT_AUDIT_RUN_RECORD_SCHEMA_ID,
// z.literal('docsync.drift-audit-run-record/v1')).
const DriftAuditRunRecordSchemaID = "docsync.drift-audit-run-record/v1"

// DriftAuditTrigger is what may trigger a Loop B audit run
// (DRIFT_AUDIT_TRIGGER_VALUES) — mirrors core's scheduler verdicts (P11-T18),
// plus the explicit local `manual` invocation. PR-critical-path triggers are
// structurally impossible and therefore absent by construction.
type DriftAuditTrigger string

// The audit triggers in canonical order (DRIFT_AUDIT_TRIGGER_VALUES).
const (
	DriftAuditTriggerNightlyScheduled       DriftAuditTrigger = "nightly-scheduled"
	DriftAuditTriggerPostMergeDefaultBranch DriftAuditTrigger = "post-merge-default-branch"
	DriftAuditTriggerManual                 DriftAuditTrigger = "manual"
)

// DriftAuditTriggerValues lists every audit trigger in canonical order
// (DRIFT_AUDIT_TRIGGER_VALUES).
var DriftAuditTriggerValues = []DriftAuditTrigger{
	DriftAuditTriggerNightlyScheduled, DriftAuditTriggerPostMergeDefaultBranch, DriftAuditTriggerManual,
}

// IsValidDriftAuditTrigger reports membership in DRIFT_AUDIT_TRIGGER.
func IsValidDriftAuditTrigger(t string) bool {
	for _, v := range DriftAuditTriggerValues {
		if string(v) == t {
			return true
		}
	}
	return false
}

// DriftAuditRunStatus is the run status vocabulary
// (DRIFT_AUDIT_RUN_STATUS_VALUES), mirrored verbatim from core's
// AuditRunStatus.
type DriftAuditRunStatus string

// The run statuses in canonical order (DRIFT_AUDIT_RUN_STATUS_VALUES).
const (
	DriftAuditRunCompleted     DriftAuditRunStatus = "completed"
	DriftAuditRunPartial       DriftAuditRunStatus = "partial"
	DriftAuditRunReRunRequired DriftAuditRunStatus = "re-run-required"
	DriftAuditRunAbstained     DriftAuditRunStatus = "abstained"
)

// DriftAuditRunStatusValues lists every run status in canonical order
// (DRIFT_AUDIT_RUN_STATUS_VALUES).
var DriftAuditRunStatusValues = []DriftAuditRunStatus{
	DriftAuditRunCompleted, DriftAuditRunPartial, DriftAuditRunReRunRequired, DriftAuditRunAbstained,
}

// IsValidDriftAuditRunStatus reports membership in DRIFT_AUDIT_RUN_STATUS.
func IsValidDriftAuditRunStatus(s string) bool {
	for _, v := range DriftAuditRunStatusValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// DriftAuditRunRecord is one audit run's provenance record
// (DRIFT_AUDIT_RUN_RECORD_SCHEMA; D5: archival, append-only under
// `.docsync/state/`). Cursor positions are the encoded
// `docsync.audit-cursor` position strings (or null when the cursor never
// existed / never advanced).
type DriftAuditRunRecord struct {
	Schema       string               `json:"schema"`
	RunID        string               `json:"run_id"`
	At           string               `json:"at"`
	Trigger      DriftAuditTrigger    `json:"trigger"`
	Position     string               `json:"position"`
	CursorBefore *string              `json:"cursor_before"`
	CursorAfter  *string              `json:"cursor_after"`
	AuditedDocs  []string             `json:"audited_docs"`
	SkippedDocs  []string             `json:"skipped_docs"`
	Status       DriftAuditRunStatus  `json:"status"`
	Detail       *string              `json:"detail"`
	Findings     []DriftFindingRecord `json:"findings"`
}

// Validate checks the record against DRIFT_AUDIT_RUN_RECORD_SCHEMA (strict),
// reproducing the zod issue paths.
func (r *DriftAuditRunRecord) Validate() []Issue {
	var issues []Issue
	if r.Schema != DriftAuditRunRecordSchemaID {
		msg := `Invalid literal value, expected "` + DriftAuditRunRecordSchemaID + `"`
		if r.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	Collect(&issues, CheckEnum(string(r.Trigger), stringsOf(DriftAuditTriggerValues), []string{"trigger"}))
	Collect(&issues, RequiredString(r.Position, 1, []string{"position"}))
	for i, doc := range r.AuditedDocs {
		Collect(&issues, RequiredString(doc, 1, []string{"audited_docs", itoa(i)}))
	}
	for i, doc := range r.SkippedDocs {
		Collect(&issues, RequiredString(doc, 1, []string{"skipped_docs", itoa(i)}))
	}
	Collect(&issues, CheckEnum(string(r.Status), stringsOf(DriftAuditRunStatusValues), []string{"status"}))
	for i := range r.Findings {
		for _, iss := range r.Findings[i].Validate() {
			iss.Path = append([]string{"findings", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// ParseDriftAuditRunRecord strictly parses and validates a
// `docsync.drift-audit-run-record/v1` document, mirroring the zod
// strictObject contract (unknown keys rejected; absent required root keys
// rejected with zod's "Required").
func ParseDriftAuditRunRecord(data []byte) (*DriftAuditRunRecord, error) {
	var r DriftAuditRunRecord
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := requireRootKeys(data,
		"schema", "run_id", "at", "trigger", "position", "cursor_before", "cursor_after",
		"audited_docs", "skipped_docs", "status", "detail", "findings"); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// DriftAuditRunRecordExample is the deterministic example
// (DRIFT_AUDIT_RUN_RECORD_EXAMPLE).
var DriftAuditRunRecordExample = DriftAuditRunRecord{
	Schema:       DriftAuditRunRecordSchemaID,
	RunID:        "audit_run_01J9ZK7Q2M",
	At:           "2026-10-09T00:00:00.000Z",
	Trigger:      DriftAuditTriggerNightlyScheduled,
	Position:     "81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5",
	CursorBefore: nil,
	CursorAfter:  strPtr("loopb:v1;since=81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5;done=1;list=abc123def456"),
	AuditedDocs:  []string{"docs/payments.md"},
	SkippedDocs:  []string{},
	Status:       DriftAuditRunCompleted,
	Detail:       nil,
	Findings:     []DriftFindingRecord{DriftFindingExample},
}

/* ------------------------------------------------------ root key presence */

// requireRootKeys enforces zod's "Required" semantics for the root object's
// required keys: Go's zero values (false, 0, empty slices/objects) are
// indistinguishable from an absent key after decoding, so the presence check
// runs against the raw JSON object. Called after a successful StrictDecode,
// which guarantees data is a JSON object.
func requireRootKeys(data []byte, keys ...string) []Issue {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	var issues []Issue
	for _, k := range keys {
		if _, ok := obj[k]; !ok {
			issues = append(issues, Issue{Path: []string{k}, Message: "Required"})
		}
	}
	return issues
}
