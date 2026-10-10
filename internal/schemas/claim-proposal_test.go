package schemas_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Ported from packages/schemas/test/claim-proposal.test.ts —
// docsync.claim-proposal/v1 (P11-T12/T13; ARCH §4.3, §8.1 posture): quoted
// source evidence mandatory; at least one candidate-anchor form; the empty
// claim set is valid (an honest extractor may extract nothing); invalid
// proposals carry diagnostics; verified_claims carries source_confirmed only.

// claimProposalBase mirrors the TS `const BASE = { ...CLAIM_PROPOSAL_EXAMPLE }`
// mutable-copy pattern (a fresh map per test so mutations stay local).
func claimProposalBase(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(schemas.ClaimProposalExample)
	require.NoError(t, err)
	var base map[string]any
	require.NoError(t, json.Unmarshal(raw, &base))
	return base
}

func claimsWith(t *testing.T, base map[string]any, mutate func(claim map[string]any)) map[string]any {
	t.Helper()
	claims, ok := base["claims"].([]any)
	require.True(t, ok, "example claims must be a JSON array")
	claim, ok := claims[0].(map[string]any)
	require.True(t, ok, "example claim must be a JSON object")
	mutate(claim)
	return base
}

func parseClaimProposal(t *testing.T, base map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(base)
	require.NoError(t, err)
	_, err = schemas.ParseClaimProposal(raw)
	return err
}

func TestV_P0_2_ShippedExampleValidates(t *testing.T) {
	raw, err := json.Marshal(schemas.ClaimProposalExample)
	require.NoError(t, err)
	parsed, err := schemas.ParseClaimProposal(raw)
	require.NoError(t, err)
	assert.Equal(t, schemas.ClaimProposalExample, *parsed)
}

func TestP11_T12_T13_EmptyClaimSetIsValid(t *testing.T) {
	base := claimProposalBase(t)
	base["claims"] = []any{}
	require.NoError(t, parseClaimProposal(t, base))
}

func TestP11_T12_T13_SourceEvidenceIsMandatory(t *testing.T) {
	// quote must be non-empty
	emptyQuote := claimProposalBase(t)
	claimsWith(t, emptyQuote, func(claim map[string]any) {
		claim["source_evidence"] = map[string]any{"quote": "", "doc_path": "docs/payments.md"}
	})
	require.Error(t, parseClaimProposal(t, emptyQuote))

	// quote must be present
	missingQuote := claimProposalBase(t)
	claimsWith(t, missingQuote, func(claim map[string]any) {
		claim["source_evidence"] = map[string]any{"doc_path": "docs/payments.md"}
	})
	require.Error(t, parseClaimProposal(t, missingQuote))
}

func TestP11_T12_T13_CandidateAnchorRequiresAtLeastOneForm(t *testing.T) {
	noForm := claimProposalBase(t)
	claimsWith(t, noForm, func(claim map[string]any) {
		claim["candidate_anchor"] = map[string]any{}
	})
	err := parseClaimProposal(t, noForm)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	found := false
	for _, iss := range verr.Issues {
		if iss.Message == "candidate_anchor requires at least one anchor form: section_heading, line_hint, or content_hash (ARCH §4.3)" {
			found = true
			assert.Equal(t, []string{"claims", "0", "candidate_anchor"}, iss.Path)
		}
	}
	assert.True(t, found, "expected the at-least-one-anchor-form refinement issue")

	for _, anchor := range []map[string]any{
		{"section_heading": "Refunds"},
		{"line_hint": 3},
		{"content_hash": "1f2e3d4c5b6a7988071627345a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7"},
	} {
		base := claimProposalBase(t)
		claimsWith(t, base, func(claim map[string]any) {
			claim["candidate_anchor"] = anchor
		})
		require.NoError(t, parseClaimProposal(t, base))
	}
}

func TestP11_T12_T13_ContentHashMustBeBare64HexSha256(t *testing.T) {
	prefixed := claimProposalBase(t)
	claimsWith(t, prefixed, func(claim map[string]any) {
		claim["candidate_anchor"] = map[string]any{
			"content_hash": "sha256:1f2e3d4c5b6a7988071627345a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7",
		}
	})
	require.Error(t, parseClaimProposal(t, prefixed))
}

func TestP11_T12_T13_ExtractionModelProvenanceIsMandatory(t *testing.T) {
	base := claimProposalBase(t)
	claimsWith(t, base, func(claim map[string]any) {
		delete(claim, "extraction_model")
	})
	require.Error(t, parseClaimProposal(t, base))
}

func TestP11_T12_T13_StatusInvalidRequiresDiagnostics(t *testing.T) {
	// status invalid without diagnostics fails
	noDiagnostics := claimProposalBase(t)
	noDiagnostics["status"] = "invalid"
	err := parseClaimProposal(t, noDiagnostics)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	found := false
	for _, iss := range verr.Issues {
		if iss.Message == "status invalid requires diagnostics — no unverifiable claim proposal ever reaches truth (P11-T13)" {
			found = true
			assert.Empty(t, iss.Path)
		}
	}
	assert.True(t, found, "expected the status-invalid-requires-diagnostics refinement issue")

	// status invalid with diagnostics parses
	withDiagnostics := claimProposalBase(t)
	withDiagnostics["status"] = "invalid"
	withDiagnostics["diagnostics"] = []any{"claim 1: quote not found in doc"}
	require.NoError(t, parseClaimProposal(t, withDiagnostics))

	// verified_claims carries source_confirmed only (hand_written is rejected)
	handWritten := claimProposalBase(t)
	handWritten["verified_claims"] = []any{
		map[string]any{
			"claim_id":     "01HTXSCHEMA1EXAMP1ES000001",
			"verified_at":  "2026-10-08T12:00:00.000Z",
			"verification": "hand_written",
		},
	}
	require.Error(t, parseClaimProposal(t, handWritten))

	// source_confirmed parses (set ONLY by the deterministic gate, P11-T13)
	sourceConfirmed := claimProposalBase(t)
	sourceConfirmed["verified_claims"] = []any{
		map[string]any{
			"claim_id":     "01HTXSCHEMA1EXAMP1ES000001",
			"verified_at":  "2026-10-08T12:00:00.000Z",
			"verification": "source_confirmed",
		},
	}
	require.NoError(t, parseClaimProposal(t, sourceConfirmed))
}

func TestP11_T12_T13_UnknownKeysRefusedAndIDsMustBeULIDs(t *testing.T) {
	unknownKey := claimProposalBase(t)
	unknownKey["extra"] = true
	require.Error(t, parseClaimProposal(t, unknownKey))

	notULID := claimProposalBase(t)
	notULID["proposal_id"] = "not-a-ulid"
	require.Error(t, parseClaimProposal(t, notULID))
}

func TestP11_T12_T13_DatetimesMustBeUTCDatetimeNoOffset(t *testing.T) {
	// z.string().datetime({ offset: false }): 'Z' suffix only — offsets like
	// +02:00 are rejected; the message is zod's default "Invalid datetime".
	offsetCreatedAt := claimProposalBase(t)
	offsetCreatedAt["created_at"] = "2026-10-08T12:00:00.000+02:00"
	err := parseClaimProposal(t, offsetCreatedAt)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	found := false
	for _, iss := range verr.Issues {
		if len(iss.Path) == 1 && iss.Path[0] == "created_at" && iss.Message == "Invalid datetime" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected created_at datetime issue")

	badVerifiedAt := claimProposalBase(t)
	badVerifiedAt["verified_claims"] = []any{
		map[string]any{
			"claim_id":     "01HTXSCHEMA1EXAMP1ES000001",
			"verified_at":  "nope",
			"verification": "source_confirmed",
		},
	}
	require.Error(t, parseClaimProposal(t, badVerifiedAt))
}
