package openrouter

// Ported from packages/openrouter/src/adapters/claim-extractor-adapter.ts
// (P11-T12; ARCH §4.3, §8.1 posture, PRIN-3) — the claim-extractor model
// adapter: DOC → claims, never code → claims.
//
// Seam contract (ARCH §4.3): the adapter reads UNTRUSTED doc content as data
// and emits a CONTRACT-GOVERNED, bound proposal (claims + anchors), never a
// free-text completion; it NEVER writes to the claim store, and it checks the
// input SIZE BOUND before any model call. Every claim must quote the doc
// verbatim and carry a candidate anchor — the runner's truth gate then
// verifies each claim; an UNVERIFIED claim never drives an automatic action
// (the runner's job, not this adapter's).
//
// Claims propose ADDITIONS only: a model output can never overwrite or delete
// a hand-anchored or manifest-declared claim (the runner adds, never rewrites).
//
// MODEL ABSTAINS → the adapter THROWS. It never fabricates a claim (zero-claim
// output is an honest result of a SUCCESSFUL structured completion; a refused
// model call can never masquerade as one).

import (
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ClaimExtractorMaxInputBytes is the size bound enforced BEFORE any model call
// (TS CLAIM_EXTRACTOR_MAX_INPUT_BYTES).
const ClaimExtractorMaxInputBytes = 200_000

// ClaimExtractorMaxClaims caps the emitted claim count (TS
// CLAIM_EXTRACTOR_MAX_CLAIMS) — a draft promising more is a contract
// violation, not an excuse to exceed the bound.
const ClaimExtractorMaxClaims = 40

// ClaimExtractorSystemPrompt is the system prompt (TS
// CLAIM_EXTRACTOR_SYSTEM_PROMPT; array values joined with ' ').
const ClaimExtractorSystemPrompt = "You are the docSync claim extractor. " +
	"Extract EXPLICIT factual claims the document makes about behavior, configuration, or API surface — DOC → claims, never code → claims. " +
	"Every claim MUST quote the document VERBATIM as source evidence and carry at least one candidate anchor: section_heading (an existing section heading), line_hint (1-based line number), or content_hash. " +
	`Output exactly one JSON object: { "claims": [{ "claim_text": string, "candidate_anchor": { "section_heading"?: string, "line_hint"?: number, "content_hash"?: string }, "source_evidence": { "quote": string, "doc_path": string } }] }. ` +
	"Extract nothing rather than invent: an empty claims array is a valid, honest result."

// claimExtractorRole is the ARCH §10.2 role this adapter implements (the
// client's binding key).
const claimExtractorRole = "claim-extractor"

/* ----------------------------------------------------------- typed failures */

// ClaimExtractorAbstainError ports the TS ClaimExtractorAbstainError class
// (adapters/claim-extractor-adapter.ts) — the extractor ABSTAINED. The reason
// is surfaced verbatim; the adapter must never fabricate claims, so the
// failure is a returned typed error (Go has no throw).
type ClaimExtractorAbstainError struct {
	// Reason is the abstain reason ('schema_violation' for a completion that
	// could not satisfy the claim-extractor contract within the bounded
	// corrective-retry budget).
	Reason string
	// Message preserves the TS message text verbatim
	// (`claim extractor abstained: ${reason}`).
	Message string
}

// Error renders the TS message text verbatim.
func (e *ClaimExtractorAbstainError) Error() string { return e.Message }

func newClaimExtractorAbstainError(reason string) *ClaimExtractorAbstainError {
	return &ClaimExtractorAbstainError{Reason: reason, Message: "claim extractor abstained: " + reason}
}

/* ------------------------------------------------------------- client seam */

// adapterCompletionPort is the minimal client seam the V2 role adapters
// consume (TS OpenRouterClient): resolve a role binding once at construction
// (fail-closed typed error when the role is unbound) and run ONE bounded role
// completion (model walk + bounded corrective retries + telemetry). The landed
// *OpenRouterClient satisfies this interface structurally (verified by a
// compile-time assertion during the port).
//
// TODO(client.go): converge onto the shared adapter port the sibling adapter
// units declare (chatCompletionPort in adapter-decision-engine.go — the same
// method set over the real CompletionRequest/RoleCompletion/ResolvedRoleBinding
// types) when the wave-end reconciliation lands a single adapter seam. Until
// then this is the local seam so this unit compiles independently.
type adapterCompletionPort interface {
	// BindingFor resolves the role binding (fail-closed typed error when the
	// role is unbound; TS client.bindingFor → resolveRoleBinding).
	BindingFor(role string) (*ResolvedRoleBinding, error)
	// Complete runs one bounded role completion (TS client.complete).
	Complete(role string, request CompletionRequest) (RoleCompletion, error)
}

/* -------------------------------------------------------------- adapter API */

// ClaimExtractorDocStructure is the doc structure handed to the model (TS
// ClaimExtractorDocStructure).
type ClaimExtractorDocStructure struct {
	SectionHeadings []string `json:"sectionHeadings"`
	LineCount       int      `json:"lineCount"`
}

// ClaimExtractorExistingClaim is context only — existing claims are never
// rewritten; they bound extraction (TS ClaimExtractorExistingClaim).
type ClaimExtractorExistingClaim struct {
	ClaimText string         `json:"claimText"`
	Anchor    map[string]any `json:"anchor"`
}

// ClaimExtractorAdapterInput is the extractor input assembly (TS
// ClaimExtractorAdapterInput).
type ClaimExtractorAdapterInput struct {
	DocPath        string                        `json:"docPath"`
	DocContent     string                        `json:"docContent"`
	DocStructure   ClaimExtractorDocStructure    `json:"docStructure"`
	ExistingClaims []ClaimExtractorExistingClaim `json:"existingClaims"`
	PolicyVersion  int                           `json:"policyVersion"`
	// PriorFailedAttempt is the prior failed attempt fed back with
	// diagnostics by the runner's bounded retry.
	PriorFailedAttempt *ClaimExtractorPriorFailedAttempt `json:"priorFailedAttempt,omitempty"`
}

// ClaimExtractorPriorFailedAttempt is the prior failed attempt (TS
// PriorFailedAttempt).
type ClaimExtractorPriorFailedAttempt struct {
	Claims      []any    `json:"claims"`
	Diagnostics []string `json:"diagnostics"`
}

// ClaimExtractorClaim is one proposed claim, stamped with the model that
// actually served the extraction (TS ClaimExtractorClaim).
type ClaimExtractorClaim struct {
	ClaimText       string                       `json:"claim_text"`
	CandidateAnchor schemas.ClaimCandidateAnchor `json:"candidate_anchor"`
	SourceEvidence  schemas.ClaimSourceEvidence  `json:"source_evidence"`
	ExtractionModel string                       `json:"extraction_model"`
}

// ClaimExtractorDraft is what the port may propose (TS ClaimExtractorDraft).
// An honest extractor may extract nothing — the empty claim set is valid.
type ClaimExtractorDraft struct {
	Claims []ClaimExtractorClaim `json:"claims"`
}

// ClaimExtractorAdapterOptions configures the extractor (TS
// ClaimExtractorAdapterDeps).
type ClaimExtractorAdapterOptions struct {
	// Client is the model-access seam.
	Client adapterCompletionPort
	// Models is the parsed models.yml — the role must be bound before the
	// extractor can run (ARCH §10.2).
	Models *ExtendedModelsConfig
}

// ClaimExtractorAdapter is the extractor adapter interface (TS
// ClaimExtractorAdapter).
type ClaimExtractorAdapter interface {
	Extract(input ClaimExtractorAdapterInput) (ClaimExtractorDraft, error)
}

// claimExtractorAdapter is the concrete extractor.
type claimExtractorAdapter struct {
	client adapterCompletionPort
	// draftSchema is the strict draft contract (the Go form of the TS
	// zodSchema — the schema source of truth is internal/schemas).
	draftSchema StructuredSchema
}

// CreateClaimExtractorAdapter wires the extractor (TS
// createClaimExtractorAdapter): an unbound role fails closed at construction
// with the TS message verbatim.
func CreateClaimExtractorAdapter(options ClaimExtractorAdapterOptions) (ClaimExtractorAdapter, error) {
	if _, bound := options.Models.Roles[claimExtractorRole]; !bound {
		return nil, NewGatewayError(CodeRoleBindingMissing,
			claimExtractorRole+" role is not bound in models.yml (ARCH §10.2) — bind it before running the extractor")
	}
	return &claimExtractorAdapter{
		client:      options.Client,
		draftSchema: ParseStrictSchema(parseClaimExtractorDraft),
	}, nil
}

// Extract extracts claims from ONE document. Fail-closed on every boundary:
// the size bound is checked BEFORE any model call; a schema-abstain completion
// raises ClaimExtractorAbstainError (never a fabricated claim); the structured
// draft is re-validated strictly (the TS zod contract) before it is returned.
func (a *claimExtractorAdapter) Extract(input ClaimExtractorAdapterInput) (ClaimExtractorDraft, error) {
	serialized, err := marshalSeamInput(input)
	if err != nil {
		return ClaimExtractorDraft{}, newClaimExtractorAbstainError(
			"extractor inputs could not be serialized: " + err.Error())
	}
	if len(serialized) > ClaimExtractorMaxInputBytes {
		return ClaimExtractorDraft{}, newClaimExtractorAbstainError(
			"serialized extractor inputs exceed the size bound (" + strconv.Itoa(len(serialized)) +
				" > " + strconv.Itoa(ClaimExtractorMaxInputBytes) + " bytes) — refuse before any model call")
	}
	completion, err := a.client.Complete(claimExtractorRole, CompletionRequest{
		System:     ClaimExtractorSystemPrompt,
		User:       string(serialized),
		ZodSchema:  a.draftSchema,
		SchemaName: "claim_extractor_draft",
	})
	if err != nil {
		return ClaimExtractorDraft{}, err
	}
	if completion.Kind == RoleCompletionKindSchemaAbstain {
		return ClaimExtractorDraft{}, newClaimExtractorAbstainError(completion.Reason)
	}
	if completion.Structured == nil {
		return ClaimExtractorDraft{}, newClaimExtractorAbstainError(
			"completion carried no structured draft — no proposal is fabricated")
	}
	// Strict local re-validation of the draft (the TS caller-supplied zod
	// schema): unknown keys, oversized claim counts and missing quotes/anchors
	// are rejected fail-closed even if a lenient client let them through.
	var draft ClaimExtractorDraft
	if _, err := parseClaimExtractorDraftStrict(completion.Structured, &draft); err != nil {
		return ClaimExtractorDraft{}, newClaimExtractorAbstainError(
			"structured completion failed the claim-extractor draft contract: " + err.Error())
	}
	// P11-T12: every claim carries the model that ACTUALLY served the
	// extraction — provenance names the real model (ARCH §10.6), so a stale
	// id can never be attributed to a claim.
	for i := range draft.Claims {
		draft.Claims[i].ExtractionModel = completion.Telemetry.RoutedModel
	}
	return draft, nil
}

/* ------------------------------------------------------- the draft contract */

// claimExtractorDraftWire is the strict draft shape (TS
// CLAIM_EXTRACTOR_DRAFT_SCHEMA, z.strictObject).
type claimExtractorDraftWire struct {
	Claims []claimExtractorClaimWire `json:"claims"`
}

type claimExtractorClaimWire struct {
	ClaimText       string                       `json:"claim_text"`
	CandidateAnchor schemas.ClaimCandidateAnchor `json:"candidate_anchor"`
	SourceEvidence  schemas.ClaimSourceEvidence  `json:"source_evidence"`
}

// parseClaimExtractorDraft is the StructuredSchema adapter over the strict
// draft parse (see ParseStrictSchema).
func parseClaimExtractorDraft(data []byte) (any, error) {
	var draft claimExtractorDraftWire
	if err := schemas.StrictDecode(data, &draft); err != nil {
		return nil, err
	}
	return draft, validateClaimExtractorDraft(draft)
}

// parseClaimExtractorDraftStrict re-decodes the client's structured value into
// the adapter's public draft shape under the same strict contract.
func parseClaimExtractorDraftStrict(value any, draft *ClaimExtractorDraft) (any, error) {
	canonical, err := docutil.CanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	wire, err := parseClaimExtractorDraft([]byte(canonical))
	if err != nil {
		return nil, err
	}
	decoded, ok := wire.(claimExtractorDraftWire)
	if !ok {
		return nil, NewGatewayError(CodeStructuredOutputViolation,
			"claim-extractor draft decoded to an unexpected value")
	}
	claims := make([]ClaimExtractorClaim, 0, len(decoded.Claims))
	for _, claim := range decoded.Claims {
		claims = append(claims, ClaimExtractorClaim{
			ClaimText:       claim.ClaimText,
			CandidateAnchor: claim.CandidateAnchor,
			SourceEvidence:  claim.SourceEvidence,
		})
	}
	draft.Claims = claims
	return draft, nil
}

// validateClaimExtractorDraft applies the TS draft constraints: at most
// ClaimExtractorMaxClaims claims, a non-empty claim text, and the registered
// candidate-anchor / source-evidence contracts (an anchor form and a verbatim
// quote — "a claim that does not quote the doc is not evidence").
func validateClaimExtractorDraft(draft claimExtractorDraftWire) error {
	var issues []schemas.Issue
	if len(draft.Claims) > ClaimExtractorMaxClaims {
		issues = append(issues, schemas.Issue{
			Path:    []string{"claims"},
			Message: "Array must contain at most " + strconv.Itoa(ClaimExtractorMaxClaims) + " element(s)",
		})
	}
	for i, claim := range draft.Claims {
		path := []string{"claims", strconv.Itoa(i)}
		schemas.Collect(&issues, schemas.RequiredString(claim.ClaimText, 1, appendIssuePath(path, "claim_text")))
		for _, anchorIssue := range claim.CandidateAnchor.Validate() {
			anchorIssue.Path = append(appendIssuePath(path, "candidate_anchor"), anchorIssue.Path...)
			issues = append(issues, anchorIssue)
		}
		for _, evidenceIssue := range claim.SourceEvidence.Validate() {
			evidenceIssue.Path = append(appendIssuePath(path, "source_evidence"), evidenceIssue.Path...)
			issues = append(issues, evidenceIssue)
		}
	}
	if len(issues) > 0 {
		return schemas.NewValidationError(issues...)
	}
	return nil
}

// appendIssuePath appends one path element to a copy of prefix.
func appendIssuePath(prefix []string, element string) []string {
	return append(append([]string{}, prefix...), element)
}
