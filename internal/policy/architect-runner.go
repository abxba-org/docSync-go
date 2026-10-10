package policy

// Ported from packages/policy/src/architect/runner.ts — Policy Architect
// runner (P10-T14/T15; ARCH §8.1/§8.2): assemble the ten inputs, propose via
// the injected port, validate the draft against `docsync.policy-proposal/v1`,
// then run the proposal→Policy-Compiler loop — compiler-invalid drafts RETURN
// to the architect with diagnostics (bounded retries); nothing partial ever
// reaches production (INV-7/INV-9: models propose, humans promote; fail
// closed).

import (
	"encoding/json"
	"errors"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ArchitectMaxAttemptsDefault is the default bounded attempt count
// (TS: ARCHITECT_MAX_ATTEMPTS_DEFAULT = 2).
const ArchitectMaxAttemptsDefault = 2

// PolicyArchitectError ports the TS PolicyArchitectError class: the runner
// could not produce a schema-valid outcome and therefore FAILS CLOSED.
type PolicyArchitectError struct {
	coded *docutil.Error
}

// Error renders the coded message.
func (e *PolicyArchitectError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf reports the code.
func (e *PolicyArchitectError) Unwrap() error { return e.coded }

func newPolicyArchitectError(message string) *PolicyArchitectError {
	return &PolicyArchitectError{coded: docutil.New(docutil.CodeValidation, message)}
}

// ArchitectNewId generates proposal ids (TS: ArchitectNewId = () => string).
// The Go signature returns an error because the ULID generator can fail
// (crypto/rand exhaustion, clock out of the 48-bit range) — a failure to mint
// an id is a failure to propose, never a silently malformed id.
type ArchitectNewId func() (string, error)

// CreateArchitectUlidFactory is the default id generator: a monotonic
// Crockford-base32 ULID factory (TS: createArchitectUlidFactory, backed by
// docutil.NewULIDGenerator).
func CreateArchitectUlidFactory() ArchitectNewId {
	generator := docutil.NewULIDGenerator()
	return generator.Generate
}

// Architect rejection reasons (TS: ProposalValidationOutcome reason union).
const (
	ArchitectRejectReasonSchema   = "schema"
	ArchitectRejectReasonCompiler = "compiler"
)

// ProposalValidationOutcome is the discriminated union of
// `{ ok: true, proposal }` and `{ ok: false, proposal, reason, diagnostics }`
// (TS: ProposalValidationOutcome). Go has no unions: OK carries the verdict,
// Reason is non-empty exactly when OK is false.
type ProposalValidationOutcome struct {
	OK          bool
	Proposal    schemas.PolicyProposal
	Reason      string
	Diagnostics []string
}

// PolicyArchitectProposeInput is the proposeAndValidate argument
// (TS: { port, inputs, policyVersionBasis }).
type PolicyArchitectProposeInput struct {
	Port               PolicyArchitectPort
	Inputs             ArchitectLoopInputs
	PolicyVersionBasis int
}

// PolicyArchitectRunner ports the TS PolicyArchitectRunner interface. Go
// returns errors where the TS runner threw; the surface is otherwise identical
// (assemble → propose/validate → queue).
type PolicyArchitectRunner interface {
	AssembleInputs(raw PolicyArchitectInputs) (PolicyArchitectInputs, error)
	ProposeAndValidate(input PolicyArchitectProposeInput) (ProposalValidationOutcome, error)
	QueueProposal(proposal schemas.PolicyProposal) schemas.PolicyProposal
}

// PolicyArchitectRunnerOptions ports PolicyArchitectRunnerOptions. Clock is
// REQUIRED (TS threw without it — no wall-clock reads inside logic).
type PolicyArchitectRunnerOptions struct {
	Clock       func() string
	NewID       ArchitectNewId
	MaxAttempts int
}

// CreatePolicyArchitectRunner ports createPolicyArchitectRunner.
func CreatePolicyArchitectRunner(options PolicyArchitectRunnerOptions) (PolicyArchitectRunner, error) {
	if options.Clock == nil {
		return nil, docutil.New(docutil.CodeInternal,
			"architect runner requires an injected clock (no wall-clock defaults)")
	}
	clock := options.Clock
	newID := options.NewID
	if newID == nil {
		newID = CreateArchitectUlidFactory()
	}
	maxAttempts := options.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = ArchitectMaxAttemptsDefault
	}
	if maxAttempts < 1 {
		return nil, docutil.New(docutil.CodeInternal,
			"architect maxAttempts must be a positive integer")
	}
	return &policyArchitectRunner{clock: clock, newID: newID, maxAttempts: maxAttempts}, nil
}

type policyArchitectRunner struct {
	clock       func() string
	newID       ArchitectNewId
	maxAttempts int
}

// mintID mints one proposal id, mapping the generator failure to a coded error.
func (r *policyArchitectRunner) mintID() (string, error) {
	id, err := r.newID()
	if err != nil {
		return "", docutil.New(docutil.CodeInternal,
			"architect runner could not mint a proposal id: "+err.Error())
	}
	return id, nil
}

// AssembleInputs ports assembleInputs: currentPolicy and manifest are required
// (ARCH §8.1) — an empty assembly is a fail-closed input error, never a
// silently accepted one.
func (r *policyArchitectRunner) AssembleInputs(raw PolicyArchitectInputs) (PolicyArchitectInputs, error) {
	if len(raw.CurrentPolicy) == 0 {
		return raw, docutil.New(docutil.CodeValidation,
			"architect inputs: currentPolicy is required (ARCH §8.1)")
	}
	if len(raw.Manifest) == 0 {
		return raw, docutil.New(docutil.CodeValidation,
			"architect inputs: manifest is required (ARCH §8.1)")
	}
	return raw, nil
}

// ProposeAndValidate ports proposeAndValidate: the bounded loop — propose via
// the port, schema-validate, compile, and on failure RETURN to the architect
// with diagnostics (bounded by maxAttempts).
func (r *policyArchitectRunner) ProposeAndValidate(
	input PolicyArchitectProposeInput,
) (ProposalValidationOutcome, error) {
	if input.Port == nil {
		return ProposalValidationOutcome{}, docutil.New(docutil.CodeInternal,
			"architect runner requires a port (models propose; the port is the only model seam)")
	}
	var priorFailedAttempt *PriorFailedAttempt
	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		loopInputs := input.Inputs
		if priorFailedAttempt != nil {
			loopInputs.PriorFailedAttempt = priorFailedAttempt
		}
		draft, err := input.Port.Propose(loopInputs)
		if err != nil {
			return ProposalValidationOutcome{}, err
		}
		proposalID, err := r.mintID()
		if err != nil {
			return ProposalValidationOutcome{}, err
		}
		candidate := schemas.PolicyProposal{
			Schema:             schemas.PolicyProposalSchemaID,
			ProposalID:         proposalID,
			CreatedAt:          r.clock(),
			PolicyVersionBasis: input.PolicyVersionBasis,
			Type:               schemas.PolicyProposalType(draft.Type),
			CountedEvidence:    architectCountedEvidence(draft.CountedEvidence),
			Artifact:           draft.Artifact,
			Status:             schemas.PolicyProposalStatusDraft,
		}
		candidateWire, marshalErr := marshalWire(candidate)
		if marshalErr != nil {
			return ProposalValidationOutcome{}, marshalErr
		}
		parsed, parseErr := schemas.ParsePolicyProposal(candidateWire)
		if parseErr != nil {
			diagnostics := proposalSchemaDiagnostics(parseErr)
			if attempt == r.maxAttempts {
				// The failure marker must itself be schema-valid: 1/1 = one failed
				// attempt of one made (truthful — a 0-count row would violate the
				// counted-evidence schema itself, so the fallback fails closed).
				fallbackID, mintErr := r.mintID()
				if mintErr != nil {
					return ProposalValidationOutcome{}, mintErr
				}
				fallback := schemas.PolicyProposal{
					Schema:             schemas.PolicyProposalSchemaID,
					ProposalID:         fallbackID,
					CreatedAt:          r.clock(),
					PolicyVersionBasis: input.PolicyVersionBasis,
					Type:               schemas.PolicyProposalTypeNewException,
					CountedEvidence: []schemas.CountedEvidence{
						{
							Statement:    "architect draft failed proposal-schema validation",
							SupportCount: 1,
							TotalCount:   1,
						},
					},
					Artifact:    draft.Artifact,
					Status:      schemas.PolicyProposalStatusInvalid,
					Diagnostics: diagnostics,
				}
				fallbackWire, fallbackMarshalErr := marshalWire(fallback)
				if fallbackMarshalErr != nil {
					return ProposalValidationOutcome{}, fallbackMarshalErr
				}
				fallbackParsed, fallbackErr := schemas.ParsePolicyProposal(fallbackWire)
				if fallbackErr != nil {
					return ProposalValidationOutcome{}, newPolicyArchitectError(
						"fallback invalid proposal failed schema validation — fail closed")
				}
				return ProposalValidationOutcome{
					OK:          false,
					Proposal:    *fallbackParsed,
					Reason:      ArchitectRejectReasonSchema,
					Diagnostics: diagnostics,
				}, nil
			}
			priorFailedAttempt = &PriorFailedAttempt{
				Type:        draft.Type,
				Artifact:    draft.Artifact,
				Diagnostics: diagnostics,
			}
			continue
		}
		compiled, compilerDiagnostics := compileArchitectArtifact(draft.Artifact)
		if compiled {
			parsed.Status = schemas.PolicyProposalStatusValidated
			return ProposalValidationOutcome{OK: true, Proposal: *parsed}, nil
		}
		if attempt == r.maxAttempts {
			parsed.Status = schemas.PolicyProposalStatusInvalid
			parsed.Diagnostics = compilerDiagnostics
			return ProposalValidationOutcome{
				OK:          false,
				Proposal:    *parsed,
				Reason:      ArchitectRejectReasonCompiler,
				Diagnostics: compilerDiagnostics,
			}, nil
		}
		priorFailedAttempt = &PriorFailedAttempt{
			Type:        draft.Type,
			Artifact:    draft.Artifact,
			Diagnostics: compilerDiagnostics,
		}
	}
	return ProposalValidationOutcome{}, newPolicyArchitectError(
		"unreachable: architect loop exhausted without returning")
}

// QueueProposal ports queueProposal: the queued lane is EXPLICIT — a validated
// proposal is never auto-promoted (models propose, humans promote).
func (r *policyArchitectRunner) QueueProposal(
	proposal schemas.PolicyProposal,
) schemas.PolicyProposal {
	queued := proposal
	queued.Status = schemas.PolicyProposalStatusQueued
	return queued
}

// architectCountedEvidence maps the draft evidence onto the wire rows.
func architectCountedEvidence(draft []ArchitectCountedEvidence) []schemas.CountedEvidence {
	if draft == nil {
		return nil
	}
	rows := make([]schemas.CountedEvidence, 0, len(draft))
	for _, row := range draft {
		rows = append(rows, schemas.CountedEvidence{
			Statement:    row.Statement,
			SupportCount: row.SupportCount,
			TotalCount:   row.TotalCount,
		})
	}
	return rows
}

// proposalSchemaDiagnostics ports schemaDiagnostics: zod's issue MESSAGES
// (path-free, exactly as the TS mapped them), prefixed `proposal schema: `.
func proposalSchemaDiagnostics(err error) []string {
	var validationErr *schemas.ValidationError
	if errors.As(err, &validationErr) && len(validationErr.Issues) > 0 {
		diagnostics := make([]string, 0, len(validationErr.Issues))
		for _, issue := range validationErr.Issues {
			diagnostics = append(diagnostics, "proposal schema: "+issue.Message)
		}
		return diagnostics
	}
	return []string{"proposal schema: validation failed"}
}

// compileArchitectArtifact is the SINGLE compiler seam of this runner: it runs
// the deterministic policy compiler (compiler.ts compilePolicy) over the draft
// artifact and shapes the machine-readable diagnostics the way the architect
// loop consumes them (`policy compiler: <kind>: <message>`). Untrusted input in
// — all-or-nothing out (ARCH §8.2).
func compileArchitectArtifact(artifact map[string]any) (bool, []string) {
	result := CompilePolicy(artifact)
	if result.OK {
		return true, nil
	}
	diagnostics := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		diagnostics = append(diagnostics, "policy compiler: "+diagnostic.Kind+": "+diagnostic.Message)
	}
	return false, diagnostics
}

// marshalWire renders a wire value for the strict parsers. The runner's own
// structs are plain data, but the model-supplied artifact map is NOT trusted:
// a non-JSON value (a func, a channel, a cyclic map) is a coded error here,
// never a panic across a package boundary.
func marshalWire(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, docutil.New(docutil.CodeSchema,
			"policy runner could not serialize the proposal wire form: "+err.Error())
	}
	return raw, nil
}
