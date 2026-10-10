package schemas

import (
	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Ported from packages/schemas/src/v1/claim-proposal.ts — Claim proposal
// (P11-T12/T13; ARCH §4.3, §8.1 posture): the structured output of the
// claim-extractor role — a PROPOSAL artifact of the same species as
// docsync.policy-proposal/v1 (draft → validated → invalid via source-evidence
// verification). Every extracted claim MUST quote the document it came from
// (source_evidence — the quote is mandatory) and carry at least one
// candidate-anchor form. This is a PROPOSAL, never truth: claims cross into
// truth only through the deterministic zero-LLM verification gate (P11-T13,
// PRIN-3), verified_claims is set ONLY by that gate, hand-anchored /
// manifest-declared claims remain authoritative, and extraction proposes
// ADDITIONS only — existing claim-store rows are never overwritten (V-P11-5).
//
// ULID shape: TS claims-imports ULID_REGEX from feedback-event.ts; the Go port
// reuses the single canonical Crockford-base32 predicate docutil.IsValidULID
// (internal/docutil/ulid.ts — same 26-char [0-9A-HJKMNP-TV-Z] semantics)
// instead of duplicating the pattern in this package.

// ClaimProposalSchemaID is the wire schema literal (TS:
// CLAIM_PROPOSAL_SCHEMA_ID, `z.literal('docsync.claim-proposal/v1')`).
const ClaimProposalSchemaID = "docsync.claim-proposal/v1"

// ClaimProposalStatusValues lists the proposal statuses in canonical order
// (TS: CLAIM_PROPOSAL_STATUS_VALUES — 'draft' | 'validated' | 'invalid').
var ClaimProposalStatusValues = []string{"draft", "validated", "invalid"}

// ClaimCandidateAnchor is a candidate anchor (TS:
// CLAIM_CANDIDATE_ANCHOR_SCHEMA, z.infer ClaimCandidateAnchor). At least ONE
// anchor form is required (Validate — the TS superRefine): a claim the
// extractor cannot even propose an anchor for is unanchored by construction and
// must never enter the pipeline (INV-13 — ambiguous anchoring routes to
// REVIEW). `content_hash` is a BARE 64-hex lowercase sha256 digest (OD-R42
// coherence, same convention as doc-patch anchors).
type ClaimCandidateAnchor struct {
	SectionHeading *string `json:"section_heading,omitempty"`
	LineHint       *int    `json:"line_hint,omitempty"`
	ContentHash    *string `json:"content_hash,omitempty"`
}

// Validate checks the anchor against CLAIM_CANDIDATE_ANCHOR_SCHEMA, including
// the superRefine requiring at least one anchor form.
func (a *ClaimCandidateAnchor) Validate() []Issue {
	var issues []Issue
	if a.SectionHeading != nil {
		Collect(&issues, RequiredString(*a.SectionHeading, 1, []string{"section_heading"}))
	}
	if a.LineHint != nil && *a.LineHint < 1 {
		Collect(&issues, &Issue{Path: []string{"line_hint"}, Message: "Number must be greater than or equal to 1"})
	}
	if a.ContentHash != nil && !IsBareSHA256Hex(*a.ContentHash) {
		Collect(&issues, &Issue{Path: []string{"content_hash"}, Message: "content_hash must be bare 64-hex sha256 (no sha256: prefix)"})
	}
	if a.SectionHeading == nil && a.LineHint == nil && a.ContentHash == nil {
		Collect(&issues, &Issue{
			Message: "candidate_anchor requires at least one anchor form: section_heading, line_hint, or content_hash (ARCH §4.3)",
		})
	}
	return issues
}

// ClaimSourceEvidence is the mandatory QUOTE (TS: CLAIM_SOURCE_EVIDENCE_SCHEMA,
// z.infer ClaimSourceEvidence) — a claim that does not quote the doc is not
// evidence.
type ClaimSourceEvidence struct {
	Quote   string `json:"quote"`
	DocPath string `json:"doc_path"`
}

// Validate checks the source evidence block.
func (e *ClaimSourceEvidence) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(e.Quote, 1, []string{"quote"}))
	Collect(&issues, RequiredString(e.DocPath, 1, []string{"doc_path"}))
	return issues
}

// ExtractedClaim is one proposed claim (TS: EXTRACTED_CLAIM_SCHEMA, z.infer
// ExtractedClaim). `extraction_model` is the ACTUALLY ROUTED model id that
// produced this claim.
type ExtractedClaim struct {
	ClaimID         string               `json:"claim_id"`
	ClaimText       string               `json:"claim_text"`
	CandidateAnchor ClaimCandidateAnchor `json:"candidate_anchor"`
	SourceEvidence  ClaimSourceEvidence  `json:"source_evidence"`
	ExtractionModel string               `json:"extraction_model"`
}

// Validate checks the claim against EXTRACTED_CLAIM_SCHEMA.
func (c *ExtractedClaim) Validate() []Issue {
	var issues []Issue
	if !docutil.IsValidULID(c.ClaimID) {
		Collect(&issues, &Issue{Path: []string{"claim_id"}, Message: "claim_id must be a ULID (26-char Crockford base32)"})
	}
	Collect(&issues, RequiredString(c.ClaimText, 1, []string{"claim_text"}))
	for _, iss := range c.CandidateAnchor.Validate() {
		iss.Path = append([]string{"candidate_anchor"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range c.SourceEvidence.Validate() {
		iss.Path = append([]string{"source_evidence"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, RequiredString(c.ExtractionModel, 1, []string{"extraction_model"}))
	return issues
}

// VerifiedClaim is set ONLY by the deterministic verification gate (P11-T13) —
// never by the model (TS: VERIFIED_CLAIM_SCHEMA, z.infer VerifiedClaim).
type VerifiedClaim struct {
	ClaimID      string `json:"claim_id"`
	VerifiedAt   string `json:"verified_at"`
	Verification string `json:"verification"`
}

// Validate checks the verified claim against VERIFIED_CLAIM_SCHEMA.
func (v *VerifiedClaim) Validate() []Issue {
	var issues []Issue
	if !docutil.IsValidULID(v.ClaimID) {
		Collect(&issues, &Issue{Path: []string{"claim_id"}, Message: "claim_id must be a ULID (26-char Crockford base32)"})
	}
	// z.string().datetime({ offset: false }) — 'Z' suffix only (no offset
	// support), pinned by the shared run-record.go helper.
	Collect(&issues, datetimeIssue(v.VerifiedAt, []string{"verified_at"}))
	if v.Verification != "source_confirmed" {
		msg := `Invalid literal value, expected "source_confirmed"`
		if v.Verification == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"verification"}, Message: msg})
	}
	return issues
}

// ClaimProposal is the `docsync.claim-proposal/v1` wire object (TS:
// CLAIM_PROPOSAL_SCHEMA, z.infer ClaimProposal).
type ClaimProposal struct {
	Schema     string `json:"schema"`
	ProposalID string `json:"proposal_id"`
	CreatedAt  string `json:"created_at"`
	DocPath    string `json:"doc_path"`
	// Claims — an honest extractor may extract nothing: the empty claim set is
	// valid (no min).
	Claims      []ExtractedClaim `json:"claims"`
	Status      string           `json:"status"`
	Diagnostics []string         `json:"diagnostics,omitempty"`
	// VerifiedClaims carries source_confirmed entries only — set by the
	// deterministic verification gate (P11-T13), never by the model.
	VerifiedClaims []VerifiedClaim `json:"verified_claims,omitempty"`
}

// Validate checks the proposal against CLAIM_PROPOSAL_SCHEMA, including the
// superRefine: status 'invalid' requires diagnostics — no unverifiable claim
// proposal ever reaches truth (P11-T13).
func (p *ClaimProposal) Validate() []Issue {
	var issues []Issue
	if p.Schema != ClaimProposalSchemaID {
		msg := `Invalid literal value, expected "` + ClaimProposalSchemaID + `"`
		if p.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	if !docutil.IsValidULID(p.ProposalID) {
		Collect(&issues, &Issue{Path: []string{"proposal_id"}, Message: "proposal_id must be a ULID (26-char Crockford base32)"})
	}
	// z.string().datetime({ offset: false }) — 'Z' suffix only (no offset
	// support), pinned by the shared run-record.go helper.
	Collect(&issues, datetimeIssue(p.CreatedAt, []string{"created_at"}))
	Collect(&issues, RequiredString(p.DocPath, 1, []string{"doc_path"}))
	for i := range p.Claims {
		for _, iss := range p.Claims[i].Validate() {
			iss.Path = append([]string{"claims", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	Collect(&issues, CheckEnum(p.Status, ClaimProposalStatusValues, []string{"status"}))
	for i, diagnostic := range p.Diagnostics {
		Collect(&issues, RequiredString(diagnostic, 1, []string{"diagnostics", itoa(i)}))
	}
	for i := range p.VerifiedClaims {
		for _, iss := range p.VerifiedClaims[i].Validate() {
			iss.Path = append([]string{"verified_claims", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if p.Status == "invalid" && len(p.Diagnostics) == 0 {
		Collect(&issues, &Issue{
			Message: "status invalid requires diagnostics — no unverifiable claim proposal ever reaches truth (P11-T13)",
		})
	}
	return issues
}

// ParseClaimProposal strictly parses and validates `docsync.claim-proposal/v1`
// JSON bytes (TS: CLAIM_PROPOSAL_SCHEMA.parse; unknown keys are rejected —
// strictObject, OD-R01).
func ParseClaimProposal(data []byte) (*ClaimProposal, error) {
	var p ClaimProposal
	if err := StrictDecode(data, &p); err != nil {
		return nil, err
	}
	if issues := p.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &p, nil
}

// ClaimProposalExample is the shipped example (TS: CLAIM_PROPOSAL_EXAMPLE).
func claimProposalExample() ClaimProposal {
	heading := "Refunds"
	lineHint := 3
	return ClaimProposal{
		Schema:     ClaimProposalSchemaID,
		ProposalID: "01HTXSCHEMA1EXAMP1ES000000",
		CreatedAt:  "2026-10-08T12:00:00.000Z",
		DocPath:    "docs/payments.md",
		Claims: []ExtractedClaim{
			{
				ClaimID:         "01HTXSCHEMA1EXAMP1ES000001",
				ClaimText:       "Refunds require an idempotency key.",
				CandidateAnchor: ClaimCandidateAnchor{SectionHeading: &heading, LineHint: &lineHint},
				SourceEvidence: ClaimSourceEvidence{
					Quote:   "Refunds require an idempotency key.",
					DocPath: "docs/payments.md",
				},
				ExtractionModel: "z-ai/glm-4.7",
			},
		},
		Status: "draft",
	}
}

// ClaimProposalExample is the registry example contract value (V-P0-2).
var ClaimProposalExample = claimProposalExample()
