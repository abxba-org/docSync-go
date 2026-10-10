package policy

// Ported from packages/policy/src/extractor/{runner,trial,port,index}.ts —
// Claim Extractor (P11-T12/T13; ARCH §4.3, §8.1 posture, PRIN-3; V-P11-5;
// P11-T14 A4): extract via the injected port, validate the draft against
// `docsync.claim-proposal/v1`, then run the PROPOSAL → TRUTH GATE — each
// claim's source_evidence.quote must appear VERBATIM in the doc content
// (whitespace-collapsed substring match — deterministic, zero-LLM) AND its
// candidate_anchor must resolve against the doc structure. Verified claims
// become verified_claims rows ('source_confirmed'); ANY unverifiable claim
// leaves the proposal at status 'validated' with the unverified claims
// EXPLICITLY flagged — an unverified proposal never drives an automatic
// action (V-P11-5). ADDITIONS ONLY: the runner never touches a claim store;
// hand-anchored / manifest-declared claims remain authoritative. The schema
// retry loop mirrors the architect (invalid → retry with diagnostics,
// bounded, then status invalid — fail closed).
//
// extractor/index.ts is the module barrel (export * from port/runner); in Go a
// package needs no barrel — the exported identifiers below ARE the barrel.

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// claimProposalWireSchemaID is the `docsync.claim-proposal/v1` literal
// (TS: CLAIM_PROPOSAL_SCHEMA_ID). Declared here because internal/schemas keeps
// the identifier unexported (claim-proposal.go); this is the single package
// policy-side copy — do not redefine it in sibling files.
const claimProposalWireSchemaID = "docsync.claim-proposal/v1"

// ExtractorMaxAttemptsDefault is the default bounded attempt count
// (TS: EXTRACTOR_MAX_ATTEMPTS_DEFAULT = 2).
const ExtractorMaxAttemptsDefault = 2

// ClaimExtractorError ports the TS ClaimExtractorError class.
type ClaimExtractorError struct {
	coded *docutil.Error
}

// Error renders the coded message.
func (e *ClaimExtractorError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf reports the code.
func (e *ClaimExtractorError) Unwrap() error { return e.coded }

func newClaimExtractorError(message string) *ClaimExtractorError {
	return &ClaimExtractorError{coded: docutil.New(docutil.CodeValidation, message)}
}

/* ---------------------------------------------------------------- the port */

// ClaimExtractorDocStructure is the doc structure the extractor reasons over
// (TS: ClaimExtractorDocStructure).
type ClaimExtractorDocStructure struct {
	SectionHeadings []string
	LineCount       int
}

// ExistingClaimReference is context only — existing claims are never rewritten;
// they bound extraction (TS: ExistingClaimReference).
type ExistingClaimReference struct {
	ClaimText string
	Anchor    map[string]any
}

// ClaimExtractorInputs is the input assembly (TS: ClaimExtractorInputs).
type ClaimExtractorInputs struct {
	DocPath        string
	DocContent     string
	DocStructure   ClaimExtractorDocStructure
	ExistingClaims []ExistingClaimReference
	PolicyVersion  int
}

// ClaimSourceEvidenceDraft is the mandatory QUOTE: a claim that does not quote
// the doc is not evidence (TS: ExtractedClaimDraft['source_evidence']).
type ClaimSourceEvidenceDraft struct {
	Quote   string
	DocPath string
}

// ExtractedClaimDraft is one proposed claim (TS: ExtractedClaimDraft). The
// candidate anchor reuses the wire anchor shape: the draft carries exactly the
// three optional forms the `docsync.claim-proposal/v1` schema declares.
type ExtractedClaimDraft struct {
	ClaimText       string
	CandidateAnchor schemas.ClaimCandidateAnchor
	SourceEvidence  ClaimSourceEvidenceDraft
	ExtractionModel string
}

// ClaimExtractorDraft is what the port may propose
// (TS: ClaimExtractorDraft). An honest extractor may extract nothing — the
// empty claim set is valid.
type ClaimExtractorDraft struct {
	Claims []ExtractedClaimDraft
}

// PriorExtractionAttempt is the prior failed attempt fed back to the extractor
// with diagnostics (TS: PriorExtractionAttempt).
type PriorExtractionAttempt struct {
	Claims      []any
	Diagnostics []string
}

// ClaimExtractorLoopInputs is the loop input: the inputs plus the optional
// prior failed attempt (TS: ClaimExtractorLoopInputs).
type ClaimExtractorLoopInputs struct {
	ClaimExtractorInputs
	PriorFailedAttempt *PriorExtractionAttempt
}

// ClaimExtractorPort is the structural port the LLM adapter implements
// (TS: ClaimExtractorPort): DOC → claims, never code → claims. The runner
// NEVER lets the port write to the claim store — extraction proposes ADDITIONS
// only. The Go port returns an error where the TS port rejected.
type ClaimExtractorPort interface {
	Extract(input ClaimExtractorLoopInputs) (ClaimExtractorDraft, error)
}

/* --------------------------------------------------------------- the runner */

// ExtractorNewId is the id generator type (TS: ExtractorNewId =
// ArchitectNewId — the shared ULID family).
type ExtractorNewId = ArchitectNewId

// CreateExtractorUlidFactory is the default id generator: the shared
// monotonic ULID factory (architect family) (TS: createExtractorUlidFactory).
func CreateExtractorUlidFactory() ExtractorNewId {
	return CreateArchitectUlidFactory()
}

// ClaimProposalOutcome is the discriminated union of
// `{ ok: true, proposal }` and `{ ok: false, proposal, reason, diagnostics }`
// (TS: ClaimProposalOutcome). Reason is non-empty exactly when OK is false.
type ClaimProposalOutcome struct {
	OK          bool
	Proposal    schemas.ClaimProposal
	Reason      string
	Diagnostics []string
}

// ClaimExtractorRejectReasonSchema is the only rejection reason the extractor
// emits (TS: ClaimProposalOutcome reason union — 'schema').
const ClaimExtractorRejectReasonSchema = "schema"

// ClaimRejectReasonQuoteNotFound / ClaimRejectReasonAnchorUnresolved are the
// unverified-claim reasons (TS: UnverifiedClaimRow reason union).
const (
	ClaimRejectReasonQuoteNotFound    = "quote_not_found"
	ClaimRejectReasonAnchorUnresolved = "anchor_unresolved"
)

// VerifiedClaimRow is a verified claim row (TS: VerifiedClaimRow extends
// VerifiedClaim) — the wire shape from internal/schemas.
type VerifiedClaimRow = schemas.VerifiedClaim

// UnverifiedClaimRow is an EXPLICITLY flagged unverified claim — it can never
// drive an automatic action (TS: UnverifiedClaimRow).
type UnverifiedClaimRow struct {
	ClaimID     string
	Reason      string
	Diagnostics []string
}

// ClaimVerificationResult is the truth-gate outcome
// (TS: ClaimVerificationResult). The proposal status STAYS 'validated' —
// verification never flips a proposal to a fully-trusted state while any claim
// remains unverified (V-P11-5).
type ClaimVerificationResult struct {
	Proposal       schemas.ClaimProposal
	VerifiedClaims []VerifiedClaimRow
	Unverified     []UnverifiedClaimRow
}

// ClaimExtractorProposeInput is the proposeAndValidate argument
// (TS: { port, inputs }).
type ClaimExtractorProposeInput struct {
	Port   ClaimExtractorPort
	Inputs ClaimExtractorInputs
}

// ClaimVerifyInput is the verifyProposals argument
// (TS: { proposal, docContent, docStructure }).
type ClaimVerifyInput struct {
	Proposal     schemas.ClaimProposal
	DocContent   string
	DocStructure ClaimExtractorDocStructure
}

// ClaimExtractorRunner ports the TS ClaimExtractorRunner interface. NOTE the
// surface is extraction + verification ONLY (V-P11-5): there is deliberately
// no claim-store access, so nothing can overwrite a hand-anchored or
// manifest-declared claim row.
type ClaimExtractorRunner interface {
	ProposeAndValidate(input ClaimExtractorProposeInput) (ClaimProposalOutcome, error)
	VerifyProposals(input ClaimVerifyInput) (ClaimVerificationResult, error)
}

// ClaimExtractorRunnerOptions ports ClaimExtractorRunnerOptions. Clock is
// REQUIRED (TS threw without it).
type ClaimExtractorRunnerOptions struct {
	Clock       func() string
	NewID       ExtractorNewId
	MaxAttempts int
}

// jsWhitespace is the ECMAScript `\s` class (WhiteSpace + LineTerminator:
// TAB, VT, FF, SP, NBSP, ZWNBSP, the Zs separators and LS/PS) — the
// whitespace-collapse canon must match the TS implementation byte-for-byte.
var jsWhitespace = regexp.MustCompile("[\t\n\v\f\r \u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff]+")

// CollapseWhitespace is the whitespace-collapsed text — the deterministic
// evidence-matching canon (TS: collapseWhitespace: value.replace(/\s+/g,'
// ').trim()).
func CollapseWhitespace(value string) string {
	return strings.Trim(jsWhitespace.ReplaceAllString(value, " "), " \t\n\v\f\r\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

// CollapsedSha256 is the bare 64-hex sha256 over the whitespace-collapsed text
// (OD-R42 convention) (TS: collapsedSha256).
func CollapsedSha256(value string) string {
	return docutil.ContentHash(CollapseWhitespace(value))
}

// CreateClaimExtractorRunner ports createClaimExtractorRunner.
func CreateClaimExtractorRunner(
	options ClaimExtractorRunnerOptions,
) (ClaimExtractorRunner, error) {
	if options.Clock == nil {
		return nil, docutil.New(docutil.CodeInternal,
			"claim extractor runner requires an injected clock (no wall-clock defaults)")
	}
	clock := options.Clock
	newID := options.NewID
	if newID == nil {
		newID = CreateExtractorUlidFactory()
	}
	maxAttempts := options.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = ExtractorMaxAttemptsDefault
	}
	if maxAttempts < 1 {
		return nil, docutil.New(docutil.CodeInternal,
			"claim extractor maxAttempts must be a positive integer")
	}
	return &claimExtractorRunner{clock: clock, newID: newID, maxAttempts: maxAttempts}, nil
}

type claimExtractorRunner struct {
	clock       func() string
	newID       ExtractorNewId
	maxAttempts int
}

// mintID mints one id, mapping the generator failure to a coded error.
func (r *claimExtractorRunner) mintID() (string, error) {
	id, err := r.newID()
	if err != nil {
		return "", docutil.New(docutil.CodeInternal,
			"claim extractor runner could not mint an id: "+err.Error())
	}
	return id, nil
}

// ProposeAndValidate ports proposeAndValidate: the bounded schema-retry loop —
// an invalid draft returns to the extractor with diagnostics, never to a
// fabricated one.
func (r *claimExtractorRunner) ProposeAndValidate(
	input ClaimExtractorProposeInput,
) (ClaimProposalOutcome, error) {
	if input.Port == nil {
		return ClaimProposalOutcome{}, docutil.New(docutil.CodeInternal,
			"claim extractor runner requires a port (models propose; the port is the only model seam)")
	}
	var priorFailedAttempt *PriorExtractionAttempt
	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		loopInputs := ClaimExtractorLoopInputs{
			ClaimExtractorInputs: input.Inputs,
		}
		if priorFailedAttempt != nil {
			loopInputs.PriorFailedAttempt = priorFailedAttempt
		}
		draft, err := input.Port.Extract(loopInputs)
		if err != nil {
			return ClaimProposalOutcome{}, err
		}
		proposalID, err := r.mintID()
		if err != nil {
			return ClaimProposalOutcome{}, err
		}
		candidate := schemas.ClaimProposal{
			Schema:     claimProposalWireSchemaID,
			ProposalID: proposalID,
			CreatedAt:  r.clock(),
			DocPath:    input.Inputs.DocPath,
			Status:     "draft",
		}
		candidate.Claims, err = extractorClaimRows(r, draft)
		if err != nil {
			return ClaimProposalOutcome{}, err
		}
		candidateWire, marshalErr := marshalWire(candidate)
		if marshalErr != nil {
			return ClaimProposalOutcome{}, marshalErr
		}
		parsed, parseErr := schemas.ParseClaimProposal(candidateWire)
		if parseErr != nil {
			diagnostics := claimSchemaDiagnostics(parseErr)
			if attempt == r.maxAttempts {
				// The failure marker must itself be schema-valid: an EMPTY claim
				// set with diagnostics (truthful — no extracted claim survived
				// schema validation, so nothing fabricated is shipped).
				fallbackID, mintErr := r.mintID()
				if mintErr != nil {
					return ClaimProposalOutcome{}, mintErr
				}
				fallback := schemas.ClaimProposal{
					Schema:      claimProposalWireSchemaID,
					ProposalID:  fallbackID,
					CreatedAt:   r.clock(),
					DocPath:     input.Inputs.DocPath,
					Claims:      []schemas.ExtractedClaim{},
					Status:      "invalid",
					Diagnostics: diagnostics,
				}
				fallbackWire, fallbackMarshalErr := marshalWire(fallback)
				if fallbackMarshalErr != nil {
					return ClaimProposalOutcome{}, fallbackMarshalErr
				}
				fallbackParsed, fallbackErr := schemas.ParseClaimProposal(fallbackWire)
				if fallbackErr != nil {
					return ClaimProposalOutcome{}, newClaimExtractorError(
						"fallback invalid claim proposal failed schema validation — fail closed")
				}
				return ClaimProposalOutcome{
					OK:          false,
					Proposal:    *fallbackParsed,
					Reason:      ClaimExtractorRejectReasonSchema,
					Diagnostics: diagnostics,
				}, nil
			}
			priorFailedAttempt = &PriorExtractionAttempt{
				Claims:      extractorRawClaims(draft),
				Diagnostics: diagnostics,
			}
			continue
		}
		parsed.Status = "validated"
		return ClaimProposalOutcome{OK: true, Proposal: *parsed}, nil
	}
	return ClaimProposalOutcome{}, newClaimExtractorError(
		"unreachable: extractor loop exhausted without returning")
}

// extractorClaimRows maps the draft claims onto wire rows, minting a claim_id
// per claim (TS: draft.claims.map((claim) => ({ claim_id: newId(), … }))).
// A claim that cannot be identified can never enter the pipeline — the minting
// failure FAILS CLOSED (an error) rather than silently dropping a claim.
func extractorClaimRows(
	r *claimExtractorRunner,
	draft ClaimExtractorDraft,
) ([]schemas.ExtractedClaim, error) {
	rows := make([]schemas.ExtractedClaim, 0, len(draft.Claims))
	for _, claim := range draft.Claims {
		claimID, err := r.mintID()
		if err != nil {
			return nil, err
		}
		rows = append(rows, schemas.ExtractedClaim{
			ClaimID:         claimID,
			ClaimText:       claim.ClaimText,
			CandidateAnchor: claim.CandidateAnchor,
			SourceEvidence: schemas.ClaimSourceEvidence{
				Quote:   claim.SourceEvidence.Quote,
				DocPath: claim.SourceEvidence.DocPath,
			},
			ExtractionModel: claim.ExtractionModel,
		})
	}
	return rows, nil
}

// extractorRawClaims maps the draft claims into the untyped shape the prior
// failed attempt rides along in (TS: { claims: draft.claims, diagnostics }).
func extractorRawClaims(draft ClaimExtractorDraft) []any {
	rows := make([]any, 0, len(draft.Claims))
	for _, claim := range draft.Claims {
		rows = append(rows, claim)
	}
	return rows
}

// claimSchemaDiagnostics ports schemaDiagnostics: zod issue MESSAGES prefixed
// `claim proposal schema: `.
func claimSchemaDiagnostics(err error) []string {
	var validationErr *schemas.ValidationError
	if errors.As(err, &validationErr) && len(validationErr.Issues) > 0 {
		diagnostics := make([]string, 0, len(validationErr.Issues))
		for _, issue := range validationErr.Issues {
			diagnostics = append(diagnostics, "claim proposal schema: "+issue.Message)
		}
		return diagnostics
	}
	return []string{"claim proposal schema: validation failed"}
}

// VerifyProposals ports verifyProposals: the deterministic zero-LLM truth gate
// over a VALIDATED proposal. A draft or invalid proposal is refused (fail
// closed).
func (r *claimExtractorRunner) VerifyProposals(
	input ClaimVerifyInput,
) (ClaimVerificationResult, error) {
	if input.Proposal.Status != "validated" {
		return ClaimVerificationResult{}, newClaimExtractorError(
			"truth gate consumes validated proposals only — refuse to verify a draft or invalid proposal")
	}
	haystack := CollapseWhitespace(input.DocContent)
	headingSet := make(map[string]struct{}, len(input.DocStructure.SectionHeadings))
	digestSet := make(map[string]struct{}, len(input.DocStructure.SectionHeadings)+16)
	for _, heading := range input.DocStructure.SectionHeadings {
		collapsed := CollapseWhitespace(heading)
		headingSet[collapsed] = struct{}{}
		digestSet[CollapsedSha256(heading)] = struct{}{}
	}
	for _, line := range strings.Split(input.DocContent, "\n") {
		digestSet[CollapsedSha256(line)] = struct{}{}
	}
	verifiedAt := r.clock()
	previouslyVerified := make(map[string]struct{}, len(input.Proposal.VerifiedClaims))
	verifiedRows := make([]VerifiedClaimRow, 0, len(input.Proposal.VerifiedClaims)+len(input.Proposal.Claims))
	verifiedRows = append(verifiedRows, input.Proposal.VerifiedClaims...)
	for _, row := range input.Proposal.VerifiedClaims {
		previouslyVerified[row.ClaimID] = struct{}{}
	}
	unverified := make([]UnverifiedClaimRow, 0, len(input.Proposal.Claims))
	for _, claim := range input.Proposal.Claims {
		if _, done := previouslyVerified[claim.ClaimID]; done {
			continue
		}
		diagnostics := []string{}
		quote := CollapseWhitespace(claim.SourceEvidence.Quote)
		if len(quote) == 0 || !strings.Contains(haystack, quote) {
			diagnostics = append(diagnostics, "source_evidence.quote not found verbatim "+
				"(whitespace-collapsed) in "+input.Proposal.DocPath)
		}
		anchor := claim.CandidateAnchor
		// ANY of the three forms may resolve (TS `||` chain — not first-wins).
		anchorResolved := false
		if anchor.SectionHeading != nil {
			if _, ok := headingSet[CollapseWhitespace(*anchor.SectionHeading)]; ok {
				anchorResolved = true
			}
		}
		if !anchorResolved && anchor.LineHint != nil {
			if *anchor.LineHint >= 1 && *anchor.LineHint <= input.DocStructure.LineCount {
				anchorResolved = true
			}
		}
		if !anchorResolved && anchor.ContentHash != nil {
			if _, ok := digestSet[*anchor.ContentHash]; ok {
				anchorResolved = true
			}
		}
		if !anchorResolved {
			diagnostics = append(diagnostics,
				"candidate_anchor unresolved: section_heading not a doc heading, line_hint out of range, "+
					"and content_hash matches no heading/line digest")
		}
		if len(diagnostics) == 0 {
			verifiedRows = append(verifiedRows, VerifiedClaimRow{
				ClaimID:      claim.ClaimID,
				VerifiedAt:   verifiedAt,
				Verification: "source_confirmed",
			})
			continue
		}
		reason := ClaimRejectReasonAnchorUnresolved
		if strings.HasPrefix(diagnostics[0], "source_evidence.quote") {
			reason = ClaimRejectReasonQuoteNotFound
		}
		unverified = append(unverified, UnverifiedClaimRow{
			ClaimID:     claim.ClaimID,
			Reason:      reason,
			Diagnostics: diagnostics,
		})
	}
	result := input.Proposal
	// verified_claims is set ONLY when at least one claim is confirmed — an
	// empty row list is omitted (the field marks truth, never emptiness).
	if len(verifiedRows) > 0 {
		result.VerifiedClaims = verifiedRows
	}
	return ClaimVerificationResult{
		Proposal:       result,
		VerifiedClaims: verifiedRows,
		Unverified:     unverified,
	}, nil
}

/* ---------------------------------------------------- the A4 quality trial */

// ClaimVerificationRateFloor is the recommended A4 floor: the aggregate
// claim-verification rate at or above which the extractor-quality trial passes
// (TS: CLAIM_VERIFICATION_RATE_FLOOR). D11-adjacent: versioned trial DATA, not
// an engine threshold — per-policy calibration data is never hard-wired into a
// behavior path; the orchestrator consumes it as recorded output.
const ClaimVerificationRateFloor = 0.8

// ProseHeavyFallbackNote is the run-record declaration seam for the Tier-2
// fallback (TS: PROSE_HEAVY_FALLBACK_NOTE): the orchestrator attaches this note
// to the run record whenever a prose-heavy doc routes to section-level
// checking — the wiring itself is a later unit's concern (declared here, per
// ARCH §16.1 A4 "declared, never silent").
const ProseHeavyFallbackNote = "prose-heavy doc: Tier-2 degrades to section-level checking " +
	"(declared A4 fallback — hand-anchored claims remain truth)"

// ProseHeavyHeadingDensityDivisor is the heading-density divisor of the
// prose-heavy heuristic (lines per heading) (TS:
// PROSE_HEAVY_HEADING_DENSITY_DIVISOR).
const ProseHeavyHeadingDensityDivisor = 80

// ProseHeavyMinLines is the minimum doc length before the prose-heavy
// heuristic may fire at all (TS: PROSE_HEAVY_MIN_LINES).
const ProseHeavyMinLines = 40

// ExtractorTrialDoc is one representative doc the trial runs the extractor
// over (TS: ExtractorTrialDoc).
type ExtractorTrialDoc struct {
	DocPath         string
	Content         string
	SectionHeadings []string
	LineCount       int
}

// ExtractorProseHeavyResult is the prose-heavy heuristic outcome
// (TS: ProseHeavyResult).
type ExtractorProseHeavyResult struct {
	ProseHeavy bool
	Reason     string
}

// ExtractorTrialDocStat is the per-doc verification record
// (TS: ExtractorTrialDocStat).
type ExtractorTrialDocStat struct {
	DocPath          string  `json:"docPath"`
	ClaimsExtracted  int     `json:"claimsExtracted"`
	ClaimsVerified   int     `json:"claimsVerified"`
	ClaimsUnverified int     `json:"claimsUnverified"`
	VerificationRate float64 `json:"verificationRate"`
	QuoteIntegrity   string  `json:"quoteIntegrity"`
	ProseHeavy       bool    `json:"proseHeavy"`
	ProseHeavyReason string  `json:"proseHeavyReason"`
}

// ExtractorTrialFallbackDeclaration is the declared Tier-2 fallback for one
// doc (TS: ExtractorTrialFallbackDeclaration).
type ExtractorTrialFallbackDeclaration struct {
	DocPath string `json:"docPath"`
	Note    string `json:"note"`
}

// ExtractorTrialVerdict is the A4 trial verdict (TS: ExtractorTrialVerdict). It
// always carries its data_source: 'fixture' means the verdict was computed on
// fixture data — a pilot trial (real repository docs) is REQUIRED before the
// verdict is treated as repository evidence.
type ExtractorTrialVerdict struct {
	Verdict              string                              `json:"verdict"`
	Threshold            float64                             `json:"threshold"`
	DataSource           string                              `json:"data_source"`
	PerDoc               []ExtractorTrialDocStat             `json:"perDoc"`
	ClaimsExtracted      int                                 `json:"claimsExtracted"`
	ClaimsVerified       int                                 `json:"claimsVerified"`
	VerificationRate     float64                             `json:"verificationRate"`
	ProseHeavyDocs       []string                            `json:"proseHeavyDocs"`
	FallbackDeclarations []ExtractorTrialFallbackDeclaration `json:"fallbackDeclarations"`
	TrialAt              string                              `json:"trial_at"`
}

// Trial verdict / data-source / quote-integrity values (TS unions).
const (
	ExtractorTrialVerdictPass = "pass"
	ExtractorTrialVerdictFail = "fail"
	ExtractorTrialDataFixture = "fixture"
	ExtractorTrialDataPilot   = "pilot"
	ExtractorQuoteAllVerbatim = "all-verbatim"
	ExtractorQuoteTampered    = "tampered"
)

// ExtractorQualityTrialInput ports ExtractorQualityTrialInput.
type ExtractorQualityTrialInput struct {
	Runner        ClaimExtractorRunner
	Port          ClaimExtractorPort
	Docs          []ExtractorTrialDoc
	Clock         func() string
	DataSource    string
	PolicyVersion int
}

// DetectProseHeavyDoc ports detectProseHeavyDoc: a long document with FEW
// section headings relative to its length (headings < lineCount /
// PROSE_HEAVY_HEADING_DENSITY_DIVISOR, and at least PROSE_HEAVY_MIN_LINES
// long) AND no extractable claims found is prose-heavy — claim-level
// extraction has no foothold there, so Tier-2 routes the doc to section-level
// checking (the declared fallback). When claimsExtracted is nil the structural
// test alone is reported; the trial always supplies it.
func DetectProseHeavyDoc(
	docContent string,
	sectionHeadings []string,
	claimsExtracted *int,
) ExtractorProseHeavyResult {
	lineCount := 0
	if len(docContent) > 0 {
		lineCount = strings.Count(docContent, "\n") + 1
	}
	densityFloor := float64(lineCount) / float64(ProseHeavyHeadingDensityDivisor)
	// The TS comparison is against the FLOAT density floor
	// (`sectionHeadings.length < densityFloor`) — never a truncated integer.
	structurallySparse := lineCount >= ProseHeavyMinLines &&
		float64(len(sectionHeadings)) < densityFloor
	if !structurallySparse {
		return ExtractorProseHeavyResult{
			ProseHeavy: false,
			Reason: "not prose-heavy: " + strconv.Itoa(len(sectionHeadings)) +
				" heading(s) for " + strconv.Itoa(lineCount) +
				" line(s) meets the density floor (" + FixedTwo(densityFloor) + ")",
		}
	}
	if claimsExtracted != nil && *claimsExtracted > 0 {
		return ExtractorProseHeavyResult{
			ProseHeavy: false,
			Reason: "not prose-heavy: sparse headings (" + strconv.Itoa(len(sectionHeadings)) +
				" for " + strconv.Itoa(lineCount) + " lines) but " + strconv.Itoa(*claimsExtracted) +
				" extractable claim(s) anchor the content",
		}
	}
	claimPart := "claim yield not supplied (structural test only)"
	if claimsExtracted != nil {
		claimPart = strconv.Itoa(*claimsExtracted) + " extractable claim(s)"
	}
	return ExtractorProseHeavyResult{
		ProseHeavy: true,
		Reason: "prose-heavy: " + strconv.Itoa(len(sectionHeadings)) + " heading(s) for " +
			strconv.Itoa(lineCount) + " line(s) is below the density floor (" +
			FixedTwo(densityFloor) + ") with " + claimPart +
			" — routes to section-level checking",
	}
}

// FixedTwo renders a float the way the TS trial reports it (toFixed(2)).
func FixedTwo(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

// RunExtractorQualityTrial ports runExtractorQualityTrial: per doc,
// proposeAndValidate (the bounded schema-retry loop) then verifyProposals (the
// deterministic truth gate). A schema-exhausted doc contributes ZERO claims
// (the runner's fallback is an empty invalid proposal) — it can never inflate
// the verification rate. Aggregate verdict: pass only when at least one claim
// was extracted AND the aggregate verification rate is at or above
// CLAIM_VERIFICATION_RATE_FLOOR; a trial that extracts nothing demonstrates
// nothing and FAILS CLOSED (the declared Tier-2 degradation is the
// conservative outcome).
//
// The A4 gate is a DEGRADATION gate, not an automation gate: if the trial
// fails, Tier-2 degrades to section-level checking — the degradation is
// DECLARED in run records (proseHeavyDocs + fallbackDeclarations), never
// silent. Hand-anchored / manifest-declared claims remain truth regardless of
// the verdict.
func RunExtractorQualityTrial(
	input ExtractorQualityTrialInput,
) (ExtractorTrialVerdict, error) {
	if input.Clock == nil {
		return ExtractorTrialVerdict{}, docutil.New(docutil.CodeInternal,
			"extractor quality trial requires an injected clock (no wall-clock defaults)")
	}
	dataSource := input.DataSource
	if dataSource == "" {
		dataSource = ExtractorTrialDataFixture
	}
	policyVersion := input.PolicyVersion
	if policyVersion == 0 {
		policyVersion = 1
	}
	perDoc := make([]ExtractorTrialDocStat, 0, len(input.Docs))
	totalExtracted := 0
	totalVerified := 0
	proseHeavyDocs := []string{}
	fallbackDeclarations := []ExtractorTrialFallbackDeclaration{}

	for _, doc := range input.Docs {
		docStructure := ClaimExtractorDocStructure{
			SectionHeadings: doc.SectionHeadings,
			LineCount:       doc.LineCount,
		}
		outcome, err := input.Runner.ProposeAndValidate(ClaimExtractorProposeInput{
			Port: input.Port,
			Inputs: ClaimExtractorInputs{
				DocPath:        doc.DocPath,
				DocContent:     doc.Content,
				DocStructure:   docStructure,
				ExistingClaims: []ExistingClaimReference{},
				PolicyVersion:  policyVersion,
			},
		})
		if err != nil {
			return ExtractorTrialVerdict{}, err
		}
		claimsExtracted := len(outcome.Proposal.Claims)
		verifiedCount := 0
		unverifiedCount := 0
		quoteIntegrity := ExtractorQuoteAllVerbatim
		if outcome.OK {
			gate, verifyErr := input.Runner.VerifyProposals(ClaimVerifyInput{
				Proposal:     outcome.Proposal,
				DocContent:   doc.Content,
				DocStructure: docStructure,
			})
			if verifyErr != nil {
				return ExtractorTrialVerdict{}, verifyErr
			}
			verifiedCount = len(gate.VerifiedClaims)
			unverifiedCount = len(gate.Unverified)
			for _, row := range gate.Unverified {
				if row.Reason == ClaimRejectReasonQuoteNotFound {
					quoteIntegrity = ExtractorQuoteTampered
					break
				}
			}
		}
		verificationRate := 1.0
		if claimsExtracted > 0 {
			verificationRate = float64(verifiedCount) / float64(claimsExtracted)
		}
		prose := DetectProseHeavyDoc(doc.Content, doc.SectionHeadings, &claimsExtracted)
		if prose.ProseHeavy {
			proseHeavyDocs = append(proseHeavyDocs, doc.DocPath)
			fallbackDeclarations = append(fallbackDeclarations, ExtractorTrialFallbackDeclaration{
				DocPath: doc.DocPath,
				Note:    ProseHeavyFallbackNote,
			})
		}
		if claimsExtracted > 0 {
			totalExtracted += claimsExtracted
			totalVerified += verifiedCount
		}
		perDoc = append(perDoc, ExtractorTrialDocStat{
			DocPath:          doc.DocPath,
			ClaimsExtracted:  claimsExtracted,
			ClaimsVerified:   verifiedCount,
			ClaimsUnverified: unverifiedCount,
			VerificationRate: verificationRate,
			QuoteIntegrity:   quoteIntegrity,
			ProseHeavy:       prose.ProseHeavy,
			ProseHeavyReason: prose.Reason,
		})
	}

	verificationRate := 0.0
	if totalExtracted > 0 {
		verificationRate = float64(totalVerified) / float64(totalExtracted)
	}
	verdict := ExtractorTrialVerdictFail
	if totalExtracted > 0 && verificationRate >= ClaimVerificationRateFloor {
		verdict = ExtractorTrialVerdictPass
	}
	return ExtractorTrialVerdict{
		Verdict:              verdict,
		Threshold:            ClaimVerificationRateFloor,
		DataSource:           dataSource,
		PerDoc:               perDoc,
		ClaimsExtracted:      totalExtracted,
		ClaimsVerified:       totalVerified,
		VerificationRate:     verificationRate,
		ProseHeavyDocs:       proseHeavyDocs,
		FallbackDeclarations: fallbackDeclarations,
		TrialAt:              input.Clock(),
	}, nil
}
