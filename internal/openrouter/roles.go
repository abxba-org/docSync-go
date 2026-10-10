package openrouter

// roles.go — Go port of packages/openrouter/src/roles.ts (P2-T09; ARCH §10.2,
// D13): logical roles — not models — are the stable seam. Concrete adapters
// live in adapters/ (owned by other delegates) and implement the role
// interfaces declared here.
//
// DIVERGENCE (surface only): Phase-0 REQUIRED_MODEL_ROLES
// (internal/schemas, docsync.models/v1) names `writer`; ARCH §10.3 binds
// `patch-writer` (plus `policy-architect`, and V2's `claim-extractor` /
// `drift-auditor`). These types model the ARCH §10.3 names; the schemas tuple
// stays Phase-0 registry data read via internal/schemas, never edited here.
// `models.yml` role keys follow whichever naming the repository adopts —
// ResolveRoleBinding resolves whatever key is asked for.
//
// D13 (binding here): the judge is an independent model instance — never the
// patch-writer instance; orchestration enforces the instance constraint. Model
// output is untrusted data: every result is schema-validated typed data, and no
// role grants permissions, file selection, or Git credentials.
//
// External imports limited to internal/schemas. RoleTelemetry comes from the
// sibling client.go barrel (same package); NotYetImplementedRoleError from the
// sibling errors.go barrel. OD-R41's structured-output mode vocabulary
// (OD-R41) is mirrored from the client-side structured seam.

import (
	"encoding/json"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ArchModelRole names the ARCH §10.2 roles the model-access layer binds against.
type ArchModelRole string

// ARCH_MODEL_ROLES — ARCH §10.2 role names, the binding the model-access layer
// is written against (order preserved).
const (
	ArchRoleDecisionEngine  ArchModelRole = "decision-engine"
	ArchRolePatchWriter     ArchModelRole = "patch-writer"
	ArchRoleJudge           ArchModelRole = "judge"
	ArchRolePolicyArchitect ArchModelRole = "policy-architect"
	ArchRoleClaimExtractor  ArchModelRole = "claim-extractor"
	ArchRoleDriftAuditor    ArchModelRole = "drift-auditor"
)

// ARCHModelRoles is the ARCH §10.2 role vocabulary (mirrors ARCH_MODEL_ROLES).
var ARCHModelRoles = []ArchModelRole{
	ArchRoleDecisionEngine,
	ArchRolePatchWriter,
	ArchRoleJudge,
	ArchRolePolicyArchitect,
	ArchRoleClaimExtractor,
	ArchRoleDriftAuditor,
}

// IsArchModelRole reports whether role is a known ARCH §10.2 role.
func IsArchModelRole(role string) bool {
	for _, r := range ARCHModelRoles {
		if string(r) == role {
			return true
		}
	}
	return false
}

// StructuredOutputMode is the per-binding structured-output mode (OD-R41).
type StructuredOutputMode string

const (
	// StructuredOutputNative sends a response_format JSON-schema constraint AND
	// keeps the caller's schema authoritative (strict re-validation).
	StructuredOutputNative StructuredOutputMode = "native"
	// StructuredOutputPrompt downgrades to constrained prompting + strict
	// schema validation only (no response_format).
	StructuredOutputPrompt StructuredOutputMode = "prompt"
)

// LegalStructuredOutputModes is the OD-R41 mode vocabulary (mirrors the client
// structured-output seam; TS LEGAL_STRUCTURED_OUTPUT_MODES).
var LegalStructuredOutputModes = []StructuredOutputMode{
	StructuredOutputNative,
	StructuredOutputPrompt,
}

// IsLegalStructuredOutputMode reports whether mode is a legal OD-R41 mode.
func IsLegalStructuredOutputMode(mode string) bool {
	for _, m := range LegalStructuredOutputModes {
		if string(m) == mode {
			return true
		}
	}
	return false
}

// RoleInvocation is the shared per-call invocation the role adapters translate
// into prompts. Optional fields are pointers (nil == TS undefined).
type RoleInvocation struct {
	System              string
	User                string
	UntrustedContent    []string
	TemperatureOverride *float64
	CachePrefix         *bool
}

// DecisionAnswer mirrors schemas.Answer shape used inside a decision ground:
// value + probability (both mandatory, ARCH §5.4).
type DecisionAnswer struct {
	Value       json.RawMessage
	Probability float64
}

/* -------------------------------------------------------- decision-engine */

// DecisionAnalysisContext is the grounding context from deterministic analysis
// handed to the model as data.
type DecisionAnalysisContext struct {
	ChangeKind          schemas.ChangeKind
	DocumentationImpact schemas.DocumentationImpact
	Risk                schemas.RiskLevel
	Confidence          float64
	Answers             map[string]DecisionAnswer
	Candidates          []string
	Evidence            []schemas.Evidence
}

// DecisionChangeRefs are the anchored change references for a decision.
type DecisionChangeRefs struct {
	Commit            string
	DiffRef           string
	ChangeAnalysisRef string
}

// DecisionEngineInput is the decision-engine role input.
type DecisionEngineInput struct {
	AnalysisContext     DecisionAnalysisContext
	Change              DecisionChangeRefs
	UntrustedContent    []string
	TemperatureOverride *float64
	CachePrefix         *bool
}

// Decision-engine completion kinds.
const (
	DecisionEngineKindDecision = "decision"
	DecisionEngineKindAbstain  = "abstain"
)

// DecisionEngineResult is the decision-engine role result.
//
// Decision-level abstention (e.g. insufficient_confidence) is a SUCCESSFUL
// completion carrying a Decision with abstain=true; it flows through
// kind 'decision'. kind 'abstain' is operational degradation only
// (schema_violation from the bounded-retry loop, etc.).
type DecisionEngineResult struct {
	Kind      string
	Decision  *schemas.Decision
	Reason    *schemas.AbstainReason
	Telemetry RoleTelemetry
}

// IsOperationalDecisionAbstain reports whether an abstain reason is an
// operational (kind 'abstain') reason rather than a decision-level abstention
// (schema_violation, model_unavailable, cap_exceeded — modeled after the
// TS OperationalAbstainReasons).
func IsOperationalDecisionAbstain(reason schemas.AbstainReason) bool {
	switch reason {
	case "schema_violation", "model_unavailable", "cap_exceeded":
		return true
	default:
		return false
	}
}

// DecisionEngineRole is the decision-engine role interface.
type DecisionEngineRole interface {
	Decide(input DecisionEngineInput) (DecisionEngineResult, error)
}

/* ----------------------------------------------------------- patch-writer */

// Patch-writer completion kinds.
const (
	PatchWriterKindPatch   = "patch"
	PatchWriterKindAbstain = "abstain"
)

// PatchWriterResult is the patch-writer role result.
type PatchWriterResult struct {
	Kind      string
	Patch     *schemas.DocPatch
	Reason    *schemas.AbstainReason
	Telemetry RoleTelemetry
}

// PatchWriterRole is the Stage-2 patch-writer role (Phase 3): emits
// docsync.doc-patch/v1 — anchored hunks, minimal diffs.
type PatchWriterRole interface {
	WritePatch(invocation RoleInvocation) (PatchWriterResult, error)
}

/* ------------------------------------------------------------------ judge */

// JudgeVerdict is the Tier-3 judge verdict vocabulary.
type JudgeVerdict string

const (
	JudgeVerdictPass    JudgeVerdict = "pass"
	JudgeVerdictPReview JudgeVerdict = "review"
	JudgeVerdictReject  JudgeVerdict = "reject"
)

// JudgeVerdicts is the judge verdict vocabulary.
var JudgeVerdicts = []JudgeVerdict{JudgeVerdictPass, JudgeVerdictPReview, JudgeVerdictReject}

// IsJudgeVerdict reports whether v is a known judge verdict.
func IsJudgeVerdict(v string) bool {
	for _, x := range JudgeVerdicts {
		if string(x) == v {
			return true
		}
	}
	return false
}

// JudgeQuestionAnswer is one typed Tier-3 judge answer (docsync.judge-verdict/v1
// answer shape): value + probability.
type JudgeQuestionAnswer struct {
	Value       string
	Probability float64
}

// Judge completion kinds.
const (
	JudgeKindVerdict = "verdict"
	JudgeKindAbstain = "abstain"
)

// JudgeResult is the judge role result. The verdict arm carries the validated
// body verbatim (reasons — mandatory for reject — and typed-question answers);
// the adapter passes them through verbatim and never drops them. The abstain
// arm is operational degradation only — no body exists.
type JudgeResult struct {
	Kind      string
	Verdict   JudgeVerdict
	Reasons   []string
	Questions map[string]JudgeQuestionAnswer
	Reason    *schemas.AbstainReason
	Telemetry RoleTelemetry
}

// JudgeRole is the Stage-3 Tier-3 judge role (Phase 4): independent instance,
// never the writer (D13).
type JudgeRole interface {
	Judge(invocation RoleInvocation) (JudgeResult, error)
}

/* -------------------------------------------------------- policy-architect */

// PolicyProposalArtifact (ARCH §8.1) — deliberately loose in v1: proposals are
// human-promoted (never auto-applied), so the artifact is a record the
// policy-engine wave will schema-pin. Untyped values on purpose.
type PolicyProposalArtifact = map[string]any

// Policy-architect completion kinds.
const (
	PolicyArchitectKindProposal = "proposal"
	PolicyArchitectKindAbstain  = "abstain"
)

// PolicyArchitectResult is the policy-architect role result.
type PolicyArchitectResult struct {
	Kind      string
	Artifact  PolicyProposalArtifact
	Reason    *schemas.AbstainReason
	Telemetry RoleTelemetry
}

// PolicyArchitectRole is the policy-architect role interface.
type PolicyArchitectRole interface {
	Propose(invocation RoleInvocation) (PolicyArchitectResult, error)
}

/* ----------------------------------------------- V2 forward declarations */

// Claim-extractor completion kinds.
const (
	ClaimExtractorKindClaims  = "claims"
	ClaimExtractorKindAbstain = "abstain"
)

// ClaimExtractorResult is the V2 claim-extractor role result.
type ClaimExtractorResult struct {
	Kind      string
	Claims    []any
	Reason    *schemas.AbstainReason
	Telemetry RoleTelemetry
}

// ClaimExtractorRole is V2 (claim extraction, ARCH §4.3) — forward-declared.
type ClaimExtractorRole interface {
	ExtractClaims(invocation RoleInvocation) (ClaimExtractorResult, error)
}

// Drift-auditor completion kinds.
const (
	DriftAuditorKindFindings = "findings"
	DriftAuditorKindAbstain  = "abstain"
)

// DriftAuditResult is the V2/V3 drift-auditor role result.
type DriftAuditResult struct {
	Kind      string
	Findings  []any
	Reason    *schemas.AbstainReason
	Telemetry RoleTelemetry
}

// DriftAuditorRole is V2/V3 (Loop B background sweeps, ARCH §7.2) —
// forward-declared.
type DriftAuditorRole interface {
	AuditDrift(invocation RoleInvocation) (DriftAuditResult, error)
}

// notImplementedRoleStub is the shared body of the forward-declared V2 role
// stubs: invoking one before its phase is a typed NotYetImplementedRoleError.
type notImplementedRoleStub struct{}

func (notImplementedRoleStub) ExtractClaims(RoleInvocation) (ClaimExtractorResult, error) {
	return ClaimExtractorResult{}, NewNotYetImplementedRoleError("claim-extractor")
}

func (notImplementedRoleStub) AuditDrift(RoleInvocation) (DriftAuditResult, error) {
	return DriftAuditResult{}, NewNotYetImplementedRoleError("drift-auditor")
}

type claimExtractorRoleStub struct{ notImplementedRoleStub }
type driftAuditorRoleStub struct{ notImplementedRoleStub }

// ClaimExtractorRoleNotImplemented is the forward-declared claim-extractor stub
// (mirrors CLAIM_EXTRACTOR_ROLE): it has no implementation yet.
var ClaimExtractorRoleNotImplemented ClaimExtractorRole = claimExtractorRoleStub{}

// DriftAuditorRoleNotImplemented is the forward-declared drift-auditor stub
// (mirrors DRIFT_AUDITOR_ROLE): it has no implementation yet.
var DriftAuditorRoleNotImplemented DriftAuditorRole = driftAuditorRoleStub{}
