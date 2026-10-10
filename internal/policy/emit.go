// Ported from packages/policy/src/decision/emit.ts — decision-object emission
// (P2-T13; ARCH §5.4): assembles and validates the `docsync.decision/v1`
// object the decision-engine stage ships downstream.
//
// Emission owns exactly two hard gates before assembly, then one fail-closed
// schema parse:
//
//  1. Manifest gate (V-P2-7): every `candidates` entry must be a member of the
//     manifest-known document-path universe. Violation →
//     CandidateNotInManifestError. NEVER silently drops — the caller decides
//     filtering; emission REJECTS (the §5.4 mandatory property; the constraint
//     is deliberately not in the wire schema because the manifest is
//     per-repository runtime data, so it is enforced here and re-verified by
//     the orchestrator before any candidate reaches the patch layer).
//  2. Evidence gate (§5.4 "machine-resolvable anchors, not free text"): every
//     evidence `ref` must match the resolvable shape of its kind; free text →
//     EvidenceNotResolvableError. The `note` field is human explanation and is
//     never shape-checked — the REF is the anchor.
//  3. DECISION_SCHEMA validation on the assembled object — fail closed on
//     drift (DecisionSchemaDriftError); no partial decision is ever returned.
//
// V-P2-6 by construction: the emitted object carries `policy_version` (from
// the compiled workflow), `trace.policy_hash`, `trace.latency_ms`, and
// value+probability on every answer.
//
// Answer-contract validation against the compiled questions is NOT emission's
// job — it is the Jev runtime's single gate. Emission validates what it owns:
// candidates, evidence anchors, and wire-schema shape.
//
// The per-kind resolvable-shape rules are CANONICAL in internal/schemas
// (EvidenceRefIssue / IsResolvableEvidence, V-P3-10; single source per OD-2) —
// this file keeps only its typed error and message format, delegating the
// shape logic.
package policy

import (
	"math"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// decisionEmissionWhere is the default assertion site carried in the
// EvidenceNotResolvableError message (TS `where = 'decision emission'`).
const decisionEmissionWhere = "decision emission"

/* ---------------------------------------------------------------------- errors */

// CandidateNotInManifestError is the V-P2-7 failure: a candidate references a
// path outside the manifest allowlist. Emission rejects (never silently
// drops); the caller decides filtering.
type CandidateNotInManifestError struct {
	// Offending lists the candidate paths absent from the manifest path
	// universe, in input order.
	Offending []string
}

// CandidateNotInManifestErrorCode is the machine-readable failure code (TS
// `CandidateNotInManifestError.code`).
const CandidateNotInManifestErrorCode = "CANDIDATE_NOT_IN_MANIFEST"

// Code is the stable failure code (TS class code).
func (e *CandidateNotInManifestError) Code() string { return CandidateNotInManifestErrorCode }

// Error renders the preserved TS message.
func (e *CandidateNotInManifestError) Error() string {
	return "candidates must reference manifest-known paths only (ARCH §5.4; V-P2-7) — offending: " +
		strings.Join(e.Offending, ", ") + ". Emission rejects; filter upstream, never here"
}

// Unwrap maps the gate failure onto docutil's coded-error class so
// docutil.CodeOf reports EDOCSYNC_MANIFEST — the closest existing docutil code
// for a manifest-universe violation (the TS class has no docutil counterpart;
// see CONVENTIONS.md §Errors).
func (e *CandidateNotInManifestError) Unwrap() error {
	return docutil.New(docutil.CodeManifest, e.Error())
}

// UnresolvableEvidence is one evidence item whose ref is not a
// machine-resolvable anchor for its kind (TS UnresolvableEvidence). Kind is
// the taxonomy kind, or the literal '(unknown)' for schema-bypassing casts.
type UnresolvableEvidence struct {
	Index  int    `json:"index"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// EvidenceNotResolvableError is the §5.4 failure: evidence items are
// machine-resolvable anchors, not free text.
//
// Message format (V-P3-10; OD-2 single source): BYTE-IDENTICAL with the
// canonical internal/schemas EvidenceNotResolvableError for the same
// offenders at the same `where` —
// `${where}: evidence refs must be machine-resolvable anchors (ARCH §5.4;
// V-P3-10) — [i] kind 'ref': reason`. The per-item reasons always come from
// the canonical schemas.EvidenceRefIssue on both sides.
type EvidenceNotResolvableError struct {
	// Offending lists the offending items in input order, with per-item
	// canonical reasons.
	Offending []UnresolvableEvidence
	// Where names the assertion site (TS default 'decision emission').
	Where string
}

// Code is the stable failure code (TS `EvidenceNotResolvableError.code`) —
// reused from the canonical schemas constant (OD-2).
func (e *EvidenceNotResolvableError) Code() string {
	return schemas.EvidenceNotResolvableErrorCode
}

// Error renders the canonical failure message: `where` prefix + V-P3-10
// marker + offender list.
func (e *EvidenceNotResolvableError) Error() string {
	where := e.Where
	if where == "" {
		where = decisionEmissionWhere
	}
	parts := make([]string, len(e.Offending))
	for i, item := range e.Offending {
		parts[i] = "[" + strconv.Itoa(item.Index) + "] " + item.Kind + " '" + item.Ref + "': " + item.Reason
	}
	return where + ": evidence refs must be machine-resolvable anchors (ARCH §5.4; V-P3-10) — " +
		strings.Join(parts, "; ")
}

// Unwrap maps the gate failure onto docutil's coded-error class so
// docutil.CodeOf reports EDOCSYNC_SCHEMA (mirrors the canonical schemas
// EvidenceNotResolvableError mapping).
func (e *EvidenceNotResolvableError) Unwrap() error {
	return docutil.New(docutil.CodeSchema, e.Error())
}

// DecisionSchemaDriftError is the fail-closed failure: the assembled decision
// failed the DECISION_SCHEMA check — no partial decisions.
type DecisionSchemaDriftError struct {
	// Issues lists the schema issues in check order.
	Issues []string
}

// DecisionSchemaDriftErrorCode is the machine-readable failure code (TS
// `DecisionSchemaDriftError.code`).
const DecisionSchemaDriftErrorCode = "SCHEMA_DRIFT"

// Code is the stable failure code (TS class code).
func (e *DecisionSchemaDriftError) Code() string { return DecisionSchemaDriftErrorCode }

// Error renders the preserved TS message.
func (e *DecisionSchemaDriftError) Error() string {
	return "assembled decision failed DECISION_SCHEMA parse — fail closed on drift " +
		"(no partial decisions): " + strings.Join(e.Issues, "; ")
}

// Unwrap maps the drift failure onto docutil's coded-error class so
// docutil.CodeOf reports EDOCSYNC_SCHEMA.
func (e *DecisionSchemaDriftError) Unwrap() error {
	return docutil.New(docutil.CodeSchema, e.Error())
}

/* ------------------------------------------------------- evidence shape checks */

// AssertEvidenceResolvable validates EVERY evidence item as a
// machine-resolvable anchor (§5.4) — delegating the canonical per-kind shape
// rules to internal/schemas (V-P3-10), collecting every offender, returning
// one EvidenceNotResolvableError if any. Zero evidence is valid here
// (queue-level "evidence attached" is the abstain queue's stricter rule).
func AssertEvidenceResolvable(evidence []schemas.Evidence) error {
	var offending []UnresolvableEvidence
	for index, item := range evidence {
		reason := schemas.EvidenceRefIssue(string(item.Kind), item.Ref)
		if reason == "" {
			continue
		}
		kind := string(item.Kind)
		if !schemas.IsValidEvidenceKind(kind) {
			// The '(unknown)' label applies only to schema-bypassing casts (a
			// typed Evidence always carries a taxonomy kind).
			kind = "(unknown)"
		}
		offending = append(offending, UnresolvableEvidence{
			Index:  index,
			Kind:   kind,
			Ref:    item.Ref,
			Reason: reason,
		})
	}
	if len(offending) > 0 {
		return &EvidenceNotResolvableError{Offending: offending, Where: decisionEmissionWhere}
	}
	return nil
}

/* --------------------------------------------------------------------- emit */

// EmitDecisionBody is the §5.4 body fields the decision-engine role proposes
// (models propose).
type EmitDecisionBody struct {
	ChangeKind          schemas.ChangeKind
	DocumentationImpact schemas.DocumentationImpact
	Risk                schemas.RiskLevel
	Confidence          float64
	Action              schemas.Action
	Abstain             bool
	// AbstainReason is the Stage-1 abstain reason (null when not abstaining).
	AbstainReason *schemas.AbstainReason
}

// EmitDecisionInput is the emitDecision input (TS EmitDecisionInput).
type EmitDecisionInput struct {
	// Compiled is the compiled workflow — source of `policy_version` and
	// `trace.policy_hash`.
	Compiled *CompiledPolicyWorkflow
	// Change is the analyzed change the decision routes (commit, diff_ref,
	// change_analysis_ref).
	Change schemas.DecisionChange
	// Answers are the value+probability answers keyed by question key
	// (ANSWER_SCHEMA shape).
	Answers map[string]schemas.Answer
	// Body is the §5.4 body fields the decision-engine role proposes.
	Body EmitDecisionBody
	// Candidates are the candidate paths the model selected — validated ⊆
	// manifest at emission.
	Candidates []string
	// ManifestDocPaths is the manifest-known document-path universe for the
	// manifest gate.
	ManifestDocPaths []string
	// Evidence is the machine-resolvable evidence attached to the decision.
	Evidence []schemas.Evidence
	// RoutedModel is the ACTUALLY ROUTED model id for `trace.model`
	// (provenance truthfulness, §10.6).
	RoutedModel string
	// LatencyMs is the decision latency carried on `trace.latency_ms`
	// (TS `number`; the wire schema requires an integer).
	LatencyMs float64
	// DecisionID is an explicit id override; nil defaults to the deterministic
	// content hash.
	DecisionID *string
}

// ResolveDecisionID is the deterministic default decision id (TS
// resolveDecisionId): `d_<hex16>` over
// sha256(policyHash ⊕ NUL ⊕ commit ⊕ NUL ⊕ diff_ref ⊕ NUL ⊕ change_analysis_ref)
// — the same change under the same policy always hashes to the same id
// (mirrors the openrouter adapter's content-hash id scheme, hex16 per spec).
func ResolveDecisionID(compiled *CompiledPolicyWorkflow, change schemas.DecisionChange) string {
	digest := docutil.ContentHash(strings.Join([]string{
		compiled.PolicyHash,
		change.Commit,
		change.DiffRef,
		change.ChangeAnalysisRef,
	}, "\x00"))
	return "d_" + digest[:16]
}

// AssertCandidatesInManifest is the V-P2-7 gate: validates every candidate
// against the manifest path universe. Collects every offender (input order)
// and returns one CandidateNotInManifestError — emission REJECTS, never
// silently drops.
func AssertCandidatesInManifest(candidates, manifestDocPaths []string) error {
	allowed := make(map[string]struct{}, len(manifestDocPaths))
	for _, path := range manifestDocPaths {
		allowed[path] = struct{}{}
	}
	var offending []string
	for _, candidate := range candidates {
		if _, ok := allowed[candidate]; !ok {
			offending = append(offending, candidate)
		}
	}
	if len(offending) > 0 {
		return &CandidateNotInManifestError{Offending: offending}
	}
	return nil
}

// EmitDecision emits the `docsync.decision/v1` decision object (P2-T13):
// manifest gate → evidence gate → assembly → DECISION_SCHEMA validation (fail
// closed on drift). Deterministic: the same inputs (with the same explicit
// decisionId, or the same change under the same compiled policy) always emit
// an identical object.
func EmitDecision(input EmitDecisionInput) (*schemas.Decision, error) {
	// Gate 1 — manifest-only candidates (V-P2-7). Rejects; never silently drops.
	if err := AssertCandidatesInManifest(input.Candidates, input.ManifestDocPaths); err != nil {
		return nil, err
	}
	// Gate 2 — machine-resolvable evidence anchors (§5.4).
	if err := AssertEvidenceResolvable(input.Evidence); err != nil {
		return nil, err
	}
	decisionID := ""
	if input.DecisionID != nil {
		decisionID = *input.DecisionID
	} else {
		decisionID = ResolveDecisionID(input.Compiled, input.Change)
	}
	answers := make(map[string]schemas.Answer, len(input.Answers))
	for key, answer := range input.Answers {
		answers[key] = answer
	}
	decision := &schemas.Decision{
		Schema:              schemas.DecisionSchemaID,
		DecisionID:          decisionID,
		PolicyVersion:       input.Compiled.Policy.PolicyVersion,
		Change:              input.Change,
		ChangeKind:          input.Body.ChangeKind,
		DocumentationImpact: input.Body.DocumentationImpact,
		Risk:                input.Body.Risk,
		Confidence:          input.Body.Confidence,
		Answers:             answers,
		Candidates:          append([]string{}, input.Candidates...),
		Action:              input.Body.Action,
		Abstain:             input.Body.Abstain,
		AbstainReason:       input.Body.AbstainReason,
		Evidence:            append([]schemas.Evidence{}, input.Evidence...),
		Trace: schemas.DecisionTrace{
			Model:      input.RoutedModel,
			PolicyHash: input.Compiled.PolicyHash,
			LatencyMS:  int(input.LatencyMs),
		},
	}
	// Gate 3 — fail closed on wire-schema drift; no partial decisions. The TS
	// DECISION_SCHEMA declares `trace.latency_ms: z.number().int()`, so a
	// non-integer latency is a schema issue (the Go wire struct carries int).
	issues := []string{}
	if !isIntegralFloat(input.LatencyMs) {
		issues = append(issues, "trace.latency_ms: Expected integer, received float")
	}
	for _, issue := range decision.Validate() {
		path := strings.Join(issue.Path, ".")
		if path == "" {
			path = "(root)"
		}
		issues = append(issues, path+": "+issue.Message)
	}
	if len(issues) > 0 {
		return nil, &DecisionSchemaDriftError{Issues: issues}
	}
	return decision, nil
}

// isIntegralFloat mirrors z.number().int(): finite values with no fractional
// part (NaN/Inf are never integral).
func isIntegralFloat(value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	return value == math.Trunc(value)
}
