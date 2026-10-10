package schemas

// Baseline pins the verified state of a document (ARCH §6.1; D12 resolution:
// exact SHA → same tree hash → patch-id/merge-base → invalidated).
type Baseline struct {
	DocPath        string   `json:"doc_path"`
	DocContentHash string   `json:"doc_content_hash"`
	BaselineCommit string   `json:"baseline_commit"`
	SourceFiles    []string `json:"source_files"`
	SourceSymbols  []string `json:"source_symbols"`
	ClaimIDs       []string `json:"claim_ids"`
	VerifiedAt     string   `json:"verified_at"`
	VerifiedByRun  string   `json:"verified_by_run"`
	PolicyVersion  int      `json:"policy_version"`
}

// ClaimStatus values.
const (
	ClaimStatusVerified   = "verified"
	ClaimStatusSuspect    = "suspect"
	ClaimStatusUnanchored = "unanchored"
	ClaimStatusStale      = "stale"
	ClaimStatusClosed     = "closed"
)

// ClaimStatusValues lists all claim statuses.
var ClaimStatusValues = []string{
	ClaimStatusVerified, ClaimStatusSuspect, ClaimStatusUnanchored, ClaimStatusStale, ClaimStatusClosed,
}

// IsValidClaimStatus reports membership.
func IsValidClaimStatus(s string) bool {
	for _, v := range ClaimStatusValues {
		if v == s {
			return true
		}
	}
	return false
}

// ClaimAnchor is content-hash-first anchoring; ambiguous re-anchoring routes
// to REVIEW (ARCH §4.3).
type ClaimAnchor struct {
	File        string `json:"file"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	ContentHash string `json:"content_hash"`
}

// Validate checks the anchor, including end_line >= start_line.
func (a *ClaimAnchor) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(a.File, 1, []string{"file"}))
	Collect(&issues, RequiredString(a.ContentHash, 1, []string{"content_hash"}))
	if a.StartLine < 1 {
		Collect(&issues, &Issue{Path: []string{"start_line"}, Message: "Number must be greater than or equal to 1"})
	}
	if a.EndLine < 1 {
		Collect(&issues, &Issue{Path: []string{"end_line"}, Message: "Number must be greater than or equal to 1"})
	}
	if a.EndLine < a.StartLine {
		Collect(&issues, &Issue{Path: []string{"end_line"}, Message: "end_line must be greater than or equal to start_line"})
	}
	return issues
}

// ClaimSource identifies the code symbol a claim came from.
type ClaimSource struct {
	File    string `json:"file"`
	Line    *int   `json:"line,omitempty"`
	BlobSHA string `json:"blob_sha"`
}

// ClaimHistoryEvent is one append-only claim history entry.
type ClaimHistoryEvent struct {
	At    string `json:"at"`
	RunID string `json:"run_id"`
	Event string `json:"event"`
}

// Claim is a documentation claim with anchor and status (ARCH §4.3).
type Claim struct {
	ID      string              `json:"id"`
	Text    string              `json:"text"`
	Anchor  ClaimAnchor         `json:"anchor"`
	Source  ClaimSource         `json:"source"`
	Concept *string             `json:"concept,omitempty"`
	Status  string              `json:"status"`
	History []ClaimHistoryEvent `json:"history"`
}

// DocStateName is the doc lifecycle state.
type DocStateName string

// DocStateNames values (ARCH §6.2).
const (
	DocStateVerified DocStateName = "VERIFIED"
	DocStateSuspect  DocStateName = "SUSPECT"
	DocStateClear    DocStateName = "CLEAR"
	DocStateReview   DocStateName = "REVIEW"
	DocStateStale    DocStateName = "STALE"
	DocStateProposed DocStateName = "PROPOSED"
	DocStateRejected DocStateName = "REJECTED"
	DocStateClosed   DocStateName = "CLOSED"
)

// DocStateNameValues lists all state names in canonical order.
var DocStateNameValues = []DocStateName{
	DocStateVerified, DocStateSuspect, DocStateClear, DocStateReview,
	DocStateStale, DocStateProposed, DocStateRejected, DocStateClosed,
}

// IsValidDocStateName reports membership.
func IsValidDocStateName(s string) bool {
	for _, v := range DocStateNameValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// DocStateTransition is one append-only transition entry.
type DocStateTransition struct {
	State  DocStateName `json:"state"`
	At     string       `json:"at"`
	RunID  string       `json:"run_id"`
	Reason string       `json:"reason"`
}

// VerificationRecordEntry embeds one emitted verification report,
// content-addressed by report_hash (P4-T20; OD-R44 bare-digest convention).
type VerificationRecordEntry struct {
	ReportHash string             `json:"report_hash"`
	Report     VerificationReport `json:"report"`
	At         string             `json:"at"`
	RunID      string             `json:"run_id"`
}

// DisagreementEntry is a human-vs-verifier disagreement log entry (P4-T20).
type DisagreementEntry struct {
	At              string              `json:"at"`
	RunID           string              `json:"run_id"`
	VerifierVerdict VerificationVerdict `json:"verifier_verdict"`
	HumanOutcome    string              `json:"human_outcome"`
	Reason          string              `json:"reason"`
	PatchRef        *string             `json:"patch_ref,omitempty"`
}

// Human outcome taxonomy (closed set).
var humanOutcomeValues = []string{"accepted", "edited", "rejected", "intentional", "ignored"}

// IsValidHumanOutcome reports membership.
func IsValidHumanOutcome(s string) bool {
	for _, v := range humanOutcomeValues {
		if v == s {
			return true
		}
	}
	return false
}

// DocState is the per-document lifecycle record (ARCH §6.3).
type DocState struct {
	Path          string                    `json:"path"`
	State         DocStateName              `json:"state"`
	Since         string                    `json:"since"`
	LastRun       *string                   `json:"last_run,omitempty"`
	PolicyVersion int                       `json:"policy_version"`
	EvidenceRefs  []string                  `json:"evidence_refs"`
	Transitions   []DocStateTransition      `json:"transitions"`
	Verifications []VerificationRecordEntry `json:"verifications,omitempty"`
	Disagreements []DisagreementEntry       `json:"disagreements,omitempty"`
}

// AuditCursor marks the Loop-B audit position.
type AuditCursor struct {
	Position  string `json:"position"`
	UpdatedAt string `json:"updated_at"`
}

// PolicyPointer is the live `policy.yml` pointer into policy-history/ (OD-R04).
type PolicyPointer struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	Pointer string `json:"pointer"`
	Hash    string `json:"hash,omitempty"`
}

// RequiredModelRoles must be bound for the engine to run (D9).
var RequiredModelRoles = []string{"decision-engine", "judge", "writer"}

// ModelRoleBinding is a role → OpenRouter binding (D9).
type ModelRoleBinding struct {
	Model     string   `json:"model"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// ModelsConfig is the versioned models.yml (D9).
type ModelsConfig struct {
	Schema  string                      `json:"schema"`
	Version int                         `json:"version"`
	Roles   map[string]ModelRoleBinding `json:"roles"`
}
