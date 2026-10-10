package openrouter

// Ported from packages/openrouter/src/adapters/policy-architect-adapter.ts
// (P10-T14; ARCH §8.1) — the model-facing Policy Architect adapter: the AI
// SEAM between the LLM and the runner's ten validated inputs.
//
// Its whole job is AGENCY BOUNDARY ENFORCEMENT (ARCH §8.1; §3 rule 7): the
// architect PROPOSES, it NEVER writes policy. The adapter composes a
// CONSTRAINED prompt (POLICY LEARNING, NOT PROMPT OPTIMIZATION), executes
// schema-validated structured completion, and returns a DRAFT. It CANNOT
// compile policy, CANNOT promote it, and CANNOT execute it — the runner owns
// those steps (the draft is validated against `docsync.policy-proposal/v1`
// there, not here).
//
// The output is the structured draft only — `type`, `counted_evidence` and
// `artifact` — which is exactly what the runner validates and queues for human
// promotion. Nothing here may bypass the runner.
//
// MODEL ABSTAINS → the adapter THROWS. A refusal can never masquerade as a
// proposal (ARCH §8.1: "the runner NEVER lets the port write policy").

import (
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ArchitectMaxInputBytes is the size bound enforced BEFORE any model call (TS
// ARCHITECT_MAX_INPUT_BYTES).
const ArchitectMaxInputBytes = 200_000

// ArchitectSystemPrompt is the system prompt (TS ARCHITECT_SYSTEM_PROMPT; the
// array values joined with ' ').
const ArchitectSystemPrompt = "You are the docSync Policy Architect. You PROPOSE policy changes as structured drafts for human promotion — you NEVER write policy yourself. " +
	"You analyze documentation drift patterns, classifier behavior, routing outcomes, and reviewer corrections. " +
	"You emit ONLY a JSON proposal matching the provided schema, always including counted evidence with numerator/denominator (e.g. \"17/23 reviewer overrides targeted generated-API routing\"). " +
	"Never emit a raw policy YAML; never claim you applied anything."

// architectRole is the ARCH §8.1 role this adapter implements (the client's
// binding key).
const architectRole = "policy-architect"

/* ----------------------------------------------------------- typed failures */

// ArchitectAbstainError ports the TS ArchitectAbstainError class
// (adapters/policy-architect-adapter.ts) — the architect ABSTAINED. The reason
// is surfaced verbatim; the adapter must never fabricate a proposal.
type ArchitectAbstainError struct {
	// Reason is the abstain reason ('schema_violation' for a completion that
	// could not satisfy the proposal contract within the bounded
	// corrective-retry budget).
	Reason string
	// Message preserves the TS message text verbatim
	// (`architect abstained: ${reason}`).
	Message string
}

// Error renders the TS message text verbatim.
func (e *ArchitectAbstainError) Error() string { return e.Message }

func newArchitectAbstainError(reason string) *ArchitectAbstainError {
	return &ArchitectAbstainError{Reason: reason, Message: "architect abstained: " + reason}
}

/* ------------------------------------------------------------- client seam */

// The architect consumes the SAME minimal client seam as the claim extractor
// and the drift auditor (adapterCompletionPort in adapter-claim-extractor.go —
// one definition per unit, shared by this unit's role adapters; the landed
// *OpenRouterClient satisfies it structurally).

/* -------------------------------------------------------------- adapter API */

// PolicyArchitectProposalType is one of the eight proposal types
// (TS PolicyProposalType; POLICY_PROPOSAL_TYPE_VALUES is the closed set).
type PolicyArchitectProposalType = schemas.PolicyProposalType

// PolicyArchitectCountedEvidence is one counted-evidence row (TS
// PolicyArchitectDraft['counted_evidence'][number]) — the mandatory
// "17/23 style" ratio: assertions without counts are rejected downstream
// (ARCH §8.1).
type PolicyArchitectCountedEvidence = schemas.CountedEvidence

// PolicyArchitectArtifact is the policy artifact the draft carries (TS
// Record<string, unknown>).
type PolicyArchitectArtifact = map[string]any

// PolicyArchitectPriorFailedAttempt is the prior failed attempt fed back with
// diagnostics (TS PriorFailedAttempt).
type PolicyArchitectPriorFailedAttempt struct {
	Type        string         `json:"type"`
	Artifact    map[string]any `json:"artifact"`
	Diagnostics []string       `json:"diagnostics"`
}

// PolicyArchitectAdapterInput is the ten-input assembly handed to the model
// (TS PolicyArchitectAdapterInput).
type PolicyArchitectAdapterInput struct {
	RepositoryProfile   map[string]any   `json:"repositoryProfile"`
	CurrentPolicy       map[string]any   `json:"currentPolicy"`
	Manifest            map[string]any   `json:"manifest"`
	Concepts            map[string]any   `json:"concepts"`
	HistoricalPrs       []map[string]any `json:"historicalPrs"`
	PriorDecisions      []map[string]any `json:"priorDecisions"`
	FalsePositives      []map[string]any `json:"falsePositives"`
	FalseNegatives      []map[string]any `json:"falseNegatives"`
	ReviewerCorrections []map[string]any `json:"reviewerCorrections"`
	KnownExceptions     []map[string]any `json:"knownExceptions"`
	// PriorFailedAttempt rides along when the runner retries (ARCH §8.2 —
	// proposals return to the architect with diagnostics).
	PriorFailedAttempt *PolicyArchitectPriorFailedAttempt `json:"priorFailedAttempt,omitempty"`
}

// PolicyArchitectDraft is what the port may propose (TS PolicyArchitectDraft):
// a proposal type, its counted evidence and the policy artifact it carries. It
// is a DRAFT — never policy.
type PolicyArchitectDraft struct {
	Type            PolicyArchitectProposalType      `json:"type"`
	CountedEvidence []PolicyArchitectCountedEvidence `json:"counted_evidence"`
	Artifact        PolicyArchitectArtifact          `json:"artifact"`
}

// PolicyArchitectAdapterOptions configures the architect (TS
// PolicyArchitectAdapterDeps).
type PolicyArchitectAdapterOptions struct {
	// Client is the model-access seam.
	Client adapterCompletionPort
	// Models is the parsed models.yml — the role must be bound before the
	// architect can run (ARCH §8.1; the models schema roles record is open by
	// design, so the binding needs no schema change).
	Models *ExtendedModelsConfig
}

// PolicyArchitectAdapter is the architect adapter interface (TS
// PolicyArchitectAdapter).
type PolicyArchitectAdapter interface {
	Propose(input PolicyArchitectAdapterInput) (PolicyArchitectDraft, error)
}

// policyArchitectAdapter is the concrete architect.
type policyArchitectAdapter struct {
	client adapterCompletionPort
	// draftSchema is the strict draft contract (the Go form of the TS
	// zodSchema — the schema source of truth is internal/schemas).
	draftSchema StructuredSchema
}

// CreatePolicyArchitectAdapter wires the architect (TS
// createPolicyArchitectAdapter): an unbound role fails closed at construction
// with the TS message verbatim.
func CreatePolicyArchitectAdapter(options PolicyArchitectAdapterOptions) (PolicyArchitectAdapter, error) {
	if _, bound := options.Models.Roles[architectRole]; !bound {
		return nil, NewGatewayError(CodeRoleBindingMissing,
			architectRole+" role is not bound in models.yml (ARCH §8.1) — bind it before running the policy architect")
	}
	return &policyArchitectAdapter{
		client:      options.Client,
		draftSchema: ParseStrictSchema(parsePolicyArchitectDraft),
	}, nil
}

// Propose proposes one policy change. Fail-closed on every boundary: the size
// bound is checked BEFORE any model call; a schema-abstain completion raises
// ArchitectAbstainError (never a fabricated proposal); the structured draft is
// re-validated strictly before it is returned.
func (a *policyArchitectAdapter) Propose(input PolicyArchitectAdapterInput) (PolicyArchitectDraft, error) {
	serialized, err := marshalSeamInput(input)
	if err != nil {
		return PolicyArchitectDraft{}, newArchitectAbstainError(
			"architect inputs could not be serialized: " + err.Error())
	}
	if len(serialized) > ArchitectMaxInputBytes {
		return PolicyArchitectDraft{}, newArchitectAbstainError(
			"serialized architect inputs exceed the size bound (" + strconv.Itoa(len(serialized)) +
				" > " + strconv.Itoa(ArchitectMaxInputBytes) + " bytes) — refuse before any model call")
	}
	completion, err := a.client.Complete(architectRole, CompletionRequest{
		System:     ArchitectSystemPrompt,
		User:       string(serialized),
		ZodSchema:  a.draftSchema,
		SchemaName: "policy_architect_draft",
	})
	if err != nil {
		return PolicyArchitectDraft{}, err
	}
	if completion.Kind == RoleCompletionKindSchemaAbstain {
		return PolicyArchitectDraft{}, newArchitectAbstainError(completion.Reason)
	}
	if completion.Structured == nil {
		return PolicyArchitectDraft{}, newArchitectAbstainError(
			"completion carried no structured draft — no proposal is fabricated")
	}
	var draft policyArchitectDraftWire
	if _, err := parsePolicyArchitectDraftStrict(completion.Structured, &draft); err != nil {
		return PolicyArchitectDraft{}, newArchitectAbstainError(
			"structured completion failed the policy-architect draft contract: " + err.Error())
	}
	return draft.toPublic(), nil
}

/* ------------------------------------------------------- the draft contract */

// policyArchitectDraftWire is the strict draft shape (TS
// POLICY_ARCHITECT_DRAFT_SCHEMA, z.strictObject).
type policyArchitectDraftWire struct {
	Type            PolicyArchitectProposalType      `json:"type"`
	CountedEvidence []PolicyArchitectCountedEvidence `json:"counted_evidence"`
	Artifact        PolicyArchitectArtifact          `json:"artifact"`
}

// toPublic projects the wire draft onto the public draft (the TS adapter
// returns the parsed draft verbatim).
func (d policyArchitectDraftWire) toPublic() PolicyArchitectDraft {
	return PolicyArchitectDraft{
		Type:            d.Type,
		CountedEvidence: d.CountedEvidence,
		Artifact:        d.Artifact,
	}
}

// parsePolicyArchitectDraft is the StructuredSchema adapter over the strict
// draft parse (see ParseStrictSchema).
func parsePolicyArchitectDraft(data []byte) (any, error) {
	var draft policyArchitectDraftWire
	if err := schemas.StrictDecode(data, &draft); err != nil {
		return nil, err
	}
	return draft, validatePolicyArchitectDraft(draft)
}

// parsePolicyArchitectDraftStrict re-decodes the client's structured value into
// the adapter's draft shape under the same strict contract.
func parsePolicyArchitectDraftStrict(value any, draft *policyArchitectDraftWire) (any, error) {
	canonical, err := docutil.CanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	wire, err := parsePolicyArchitectDraft([]byte(canonical))
	if err != nil {
		return nil, err
	}
	decoded, ok := wire.(policyArchitectDraftWire)
	if !ok {
		return nil, NewGatewayError(CodeStructuredOutputViolation,
			"policy-architect draft decoded to an unexpected value")
	}
	*draft = decoded
	return draft, nil
}

// validatePolicyArchitectDraft applies the TS draft constraints: a known
// proposal type, MANDATORY counted evidence (assertions without counts are
// rejected — ARCH §8.1) and a policy artifact object. The runner owns the
// `docsync.policy-proposal/v1` validation; this is the adapter-side contract
// that keeps a malformed model output from ever reaching it.
func validatePolicyArchitectDraft(draft policyArchitectDraftWire) error {
	var issues []schemas.Issue
	if !schemas.IsValidPolicyProposalType(string(draft.Type)) {
		issues = append(issues, schemas.Issue{
			Path:    []string{"type"},
			Message: "unknown policy proposal type: " + jsonString(string(draft.Type)),
		})
	}
	if len(draft.CountedEvidence) == 0 {
		issues = append(issues, schemas.Issue{
			Path:    []string{"counted_evidence"},
			Message: "policy learning requires counted evidence (ARCH §8.1)",
		})
	}
	for i, evidence := range draft.CountedEvidence {
		for _, evidenceIssue := range evidence.Validate() {
			evidenceIssue.Path = append([]string{"counted_evidence", strconv.Itoa(i)}, evidenceIssue.Path...)
			issues = append(issues, evidenceIssue)
		}
	}
	if draft.Artifact == nil {
		issues = append(issues, schemas.Issue{
			Path:    []string{"artifact"},
			Message: "Expected object, received null",
		})
	}
	if len(issues) > 0 {
		return schemas.NewValidationError(issues...)
	}
	return nil
}
