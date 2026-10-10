package openrouter

// Ported from packages/openrouter/src/adapters/drift-auditor-adapter.ts
// (P11-T21; ARCH §7.2) — the V2/V3 background differential-sweep (Loop B)
// auditor: the SECOND read-only model adapter of the loop, and the ARCH §10.1
// posture point for P11-T21.
//
// It runs on BACKGROUND sweeps (Loop B, ARCH §7.2), not the write path — its
// output is an ANALYSIS (a summary plus an ARRAY of drift-finding proposals,
// each stamped with the model that ACTUALLY served the audit — ARCH §10.6
// routed-model honesty). Loop B NEVER writes repository content, so the output
// carries write-decision vocabulary NOWHERE: no "apply", no "hunks", no
// "action". A rewriting decision is the WRITE path's job (a different loop,
// different owner) — this adapter's findings are INPUTS to it, never
// instructions.
//
// MODEL ABSTAINS → the adapter THROWS. A refused model call can never
// masquerade as a finding set (the loop degrades, it does not fabricate).

import (
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DriftAuditorMaxInputBytes is the size bound enforced BEFORE any model call
// (TS DRIFT_AUDITOR_MAX_INPUT_BYTES).
const DriftAuditorMaxInputBytes = 200_000

// DriftAuditorMaxFindings caps the emitted finding count (TS
// DRIFT_AUDITOR_MAX_FINDINGS) — a draft promising more is a contract violation,
// not an excuse to exceed the bound.
const DriftAuditorMaxFindings = 40

// DriftAuditorSystemPrompt is the system prompt (TS DRIFT_AUDITOR_SYSTEM_PROMPT;
// array values joined with ' ').
const DriftAuditorSystemPrompt = "You are the docSync drift auditor: a read-only differential-sweep analyzer. " +
	"You receive differential SIGNALS (docs that changed, code that changed without doc changes, mappings, DUE sweeps). " +
	"You may ALSO receive prior verdicts or fallback/override reasons as INPUT SIGNALS. " +
	"You do not decide writes and you do not apply changes. " +
	"Analyze and report drift as JSON only: { \"summary\": string, \"proposed_findings\": [{ \"id\": string, \"claim_id\"?: string, \"doc_path\": string, \"kind\": \"stale\" | \"contradicted\" | \"orphaned\", \"evidence\": { \"commits\": string[], \"paths\": string[], \"detail\": string }, \"confidence\"?: number, }] }. " +
	"Express STRUCTURED drift facts and citations only — no procedures, no fixes, no instructions."

// driftAuditorRole is the ARCH §10.2 role this adapter implements (the client's
// binding key).
const driftAuditorRole = "drift-auditor"

/* ----------------------------------------------------------- typed failures */

// DriftAuditorAbstainError ports the TS DriftAuditorAbstainError class
// (adapters/drift-auditor-adapter.ts) — the auditor ABSTAINED. The reason is
// surfaced verbatim; the adapter must never fabricate findings.
type DriftAuditorAbstainError struct {
	// Reason is the abstain reason ('schema_violation' for a completion that
	// could not satisfy the auditor contract within the bounded
	// corrective-retry budget).
	Reason string
	// Message preserves the TS message text verbatim
	// (`drift auditor abstained: ${reason}`).
	Message string
}

// Error renders the TS message text verbatim.
func (e *DriftAuditorAbstainError) Error() string { return e.Message }

func newDriftAuditorAbstainError(reason string) *DriftAuditorAbstainError {
	return &DriftAuditorAbstainError{Reason: reason, Message: "drift auditor abstained: " + reason}
}

/* ------------------------------------------------------------- client seam */

// The drift auditor consumes the SAME minimal client seam as the claim
// extractor (adapterCompletionPort in adapter-claim-extractor.go — one
// definition per unit, shared by this unit's V2 role adapters; the landed
// *OpenRouterClient satisfies it structurally).

/* -------------------------------------------------------------- adapter API */

// DriftAuditorAdapterInput is the auditor input assembly (TS
// DriftAuditorAdapterInput).
type DriftAuditorAdapterInput struct {
	RunID       string   `json:"runId"`
	Trigger     string   `json:"trigger"`
	AuditedDocs []string `json:"auditedDocs"`
	SkippedDocs []string `json:"skippedDocs"`
	// Findings are the differential signals — opaque records from earlier
	// stages, handed to the model as data.
	Findings []map[string]any `json:"findings"`
}

// DriftAuditorProposedFinding is one drift-finding PROPOSAL: a
// `docsync.drift-finding/v1` BODY (identity fields — schema, record id,
// created_at — belong to the runner) plus the audit model that proposed it
// (TS DriftAuditorProposedFinding).
type DriftAuditorProposedFinding struct {
	ID         string                       `json:"id"`
	ClaimID    *string                      `json:"claim_id,omitempty"`
	DocPath    string                       `json:"doc_path"`
	Kind       schemas.DriftFindingKind     `json:"kind"`
	Evidence   schemas.DriftFindingEvidence `json:"evidence"`
	Confidence *float64                     `json:"confidence,omitempty"`
	// AuditModel is the model that ACTUALLY served the audit (stamped from the
	// completion telemetry — ARCH §10.6).
	AuditModel string `json:"audit_model"`
}

// DriftAuditorAnalysis is the auditor output (TS DriftAuditorAnalysis): a
// summary plus the drift-finding proposals and the per-call telemetry. It is
// write-decision-free by construction (Loop B never writes).
type DriftAuditorAnalysis struct {
	Summary          string                        `json:"summary"`
	ProposedFindings []DriftAuditorProposedFinding `json:"proposed_findings"`
	Telemetry        RoleTelemetry                 `json:"telemetry"`
}

// DriftAuditorAdapterOptions configures the auditor (TS
// DriftAuditorAdapterDeps).
type DriftAuditorAdapterOptions struct {
	// Client is the model-access seam.
	Client adapterCompletionPort
	// Models is the parsed models.yml — the role must be bound before the
	// auditor can run (ARCH §10.2).
	Models *ExtendedModelsConfig
}

// DriftAuditorAdapter is the auditor adapter interface (TS
// DriftAuditorAdapter).
type DriftAuditorAdapter interface {
	AuditDrift(input DriftAuditorAdapterInput) (DriftAuditorAnalysis, error)
}

// driftAuditorAdapter is the concrete auditor.
type driftAuditorAdapter struct {
	client adapterCompletionPort
	// draftSchema is the strict draft contract (the Go form of the TS
	// zodSchema — the schema source of truth is internal/schemas).
	draftSchema StructuredSchema
}

// CreateDriftAuditorAdapter wires the auditor (TS
// createDriftAuditorAdapter): an unbound role fails closed at construction
// with the TS message verbatim.
func CreateDriftAuditorAdapter(options DriftAuditorAdapterOptions) (DriftAuditorAdapter, error) {
	if _, bound := options.Models.Roles[driftAuditorRole]; !bound {
		return nil, NewGatewayError(CodeRoleBindingMissing,
			driftAuditorRole+" role is not bound in models.yml (ARCH §10.2) — bind it before running the drift auditor")
	}
	return &driftAuditorAdapter{
		client:      options.Client,
		draftSchema: ParseStrictSchema(parseDriftAuditorDraft),
	}, nil
}

// AuditDrift audits ONE differential sweep. Fail-closed on every boundary: the
// size bound is checked BEFORE any model call; a schema-abstain completion
// raises DriftAuditorAbstainError (never a fabricated finding); the structured
// draft is re-validated strictly before it is returned.
func (a *driftAuditorAdapter) AuditDrift(input DriftAuditorAdapterInput) (DriftAuditorAnalysis, error) {
	serialized, err := marshalSeamInput(input)
	if err != nil {
		return DriftAuditorAnalysis{}, newDriftAuditorAbstainError(
			"auditor inputs could not be serialized: " + err.Error())
	}
	if len(serialized) > DriftAuditorMaxInputBytes {
		return DriftAuditorAnalysis{}, newDriftAuditorAbstainError(
			"serialized auditor inputs exceed the size bound (" + strconv.Itoa(len(serialized)) +
				" > " + strconv.Itoa(DriftAuditorMaxInputBytes) + " bytes) — refuse before any model call")
	}
	completion, err := a.client.Complete(driftAuditorRole, CompletionRequest{
		System:     DriftAuditorSystemPrompt,
		User:       string(serialized),
		ZodSchema:  a.draftSchema,
		SchemaName: "drift_auditor_draft",
	})
	if err != nil {
		return DriftAuditorAnalysis{}, err
	}
	if completion.Kind == RoleCompletionKindSchemaAbstain {
		return DriftAuditorAnalysis{}, newDriftAuditorAbstainError(completion.Reason)
	}
	if completion.Structured == nil {
		return DriftAuditorAnalysis{}, newDriftAuditorAbstainError(
			"completion carried no structured draft — no analysis is fabricated")
	}
	var draft driftAuditorDraftWire
	if _, err := parseDriftAuditorDraftStrict(completion.Structured, &draft); err != nil {
		return DriftAuditorAnalysis{}, newDriftAuditorAbstainError(
			"structured completion failed the drift-auditor draft contract: " + err.Error())
	}
	// P11-T21: every finding carries the model that ACTUALLY served the audit
	// — routed-model honesty (ARCH §10.6).
	for i := range draft.ProposedFindings {
		draft.ProposedFindings[i].AuditModel = completion.Telemetry.RoutedModel
	}
	return DriftAuditorAnalysis{
		Summary:          draft.Summary,
		ProposedFindings: draft.ProposedFindings,
		Telemetry:        completion.Telemetry,
	}, nil
}

/* ------------------------------------------------------- the draft contract */

// driftAuditorDraftWire is the strict draft shape (TS
// DRIFT_AUDITOR_DRAFT_SCHEMA, z.strictObject).
type driftAuditorDraftWire struct {
	Summary          string                        `json:"summary"`
	ProposedFindings []DriftAuditorProposedFinding `json:"proposed_findings"`
}

// parseDriftAuditorDraft is the StructuredSchema adapter over the strict draft
// parse (see ParseStrictSchema).
func parseDriftAuditorDraft(data []byte) (any, error) {
	var draft driftAuditorDraftWire
	if err := schemas.StrictDecode(data, &draft); err != nil {
		return nil, err
	}
	return draft, validateDriftAuditorDraft(draft)
}

// parseDriftAuditorDraftStrict re-decodes the client's structured value into
// the adapter's draft shape under the same strict contract.
func parseDriftAuditorDraftStrict(value any, draft *driftAuditorDraftWire) (any, error) {
	canonical, err := docutil.CanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	wire, err := parseDriftAuditorDraft([]byte(canonical))
	if err != nil {
		return nil, err
	}
	decoded, ok := wire.(driftAuditorDraftWire)
	if !ok {
		return nil, NewGatewayError(CodeStructuredOutputViolation,
			"drift-auditor draft decoded to an unexpected value")
	}
	*draft = decoded
	return draft, nil
}

// validateDriftAuditorDraft applies the TS draft constraints: a non-empty
// summary, at most DriftAuditorMaxFindings findings, and — per finding — the
// `docsync.drift-finding/v1` BODY contract (id/doc_path/kind/evidence) the
// runner assembles into the registered record.
func validateDriftAuditorDraft(draft driftAuditorDraftWire) error {
	var issues []schemas.Issue
	schemas.Collect(&issues, schemas.RequiredString(draft.Summary, 1, []string{"summary"}))
	if len(draft.ProposedFindings) > DriftAuditorMaxFindings {
		issues = append(issues, schemas.Issue{
			Path:    []string{"proposed_findings"},
			Message: "Array must contain at most " + strconv.Itoa(DriftAuditorMaxFindings) + " element(s)",
		})
	}
	for i, finding := range draft.ProposedFindings {
		path := []string{"proposed_findings", strconv.Itoa(i)}
		schemas.Collect(&issues, schemas.RequiredString(finding.ID, 1, appendIssuePath(path, "id")))
		if finding.ClaimID != nil {
			schemas.Collect(&issues, schemas.RequiredString(*finding.ClaimID, 1, appendIssuePath(path, "claim_id")))
		}
		schemas.Collect(&issues, schemas.RequiredString(finding.DocPath, 1, appendIssuePath(path, "doc_path")))
		schemas.Collect(&issues, schemas.CheckEnum(string(finding.Kind), driftFindingKindNames(), appendIssuePath(path, "kind")))
		for _, evidenceIssue := range finding.Evidence.Validate() {
			evidenceIssue.Path = append(appendIssuePath(path, "evidence"), evidenceIssue.Path...)
			issues = append(issues, evidenceIssue)
		}
		if finding.Confidence != nil && (*finding.Confidence < 0 || *finding.Confidence > 1) {
			issues = append(issues, schemas.Issue{
				Path:    appendIssuePath(path, "confidence"),
				Message: "confidence must be a number in [0, 1]",
			})
		}
	}
	if len(issues) > 0 {
		return schemas.NewValidationError(issues...)
	}
	return nil
}

// driftFindingKindNames renders the registered drift-finding kind values for
// the enum check (TS DRIFT_FINDING_KIND_VALUES).
func driftFindingKindNames() []string {
	names := make([]string, 0, len(schemas.DriftFindingKindValues))
	for _, kind := range schemas.DriftFindingKindValues {
		names = append(names, string(kind))
	}
	return names
}
