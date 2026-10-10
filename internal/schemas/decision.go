// Ported from packages/schemas/src/v1/decision.ts
package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// DecisionSchemaID is the wire schema literal (`docsync.decision/v1`).
const DecisionSchemaID = "docsync.decision/v1"

// ChangeKind is the change taxonomy (ARCH §5.4).
type ChangeKind string

// ChangeKind values.
const (
	ChangeKindBehavioral     ChangeKind = "behavioral"
	ChangeKindAPIContract    ChangeKind = "api_contract"
	ChangeKindConfiguration  ChangeKind = "configuration"
	ChangeKindArchitecture   ChangeKind = "architecture"
	ChangeKindDependency     ChangeKind = "dependency"
	ChangeKindTestOnly       ChangeKind = "test_only"
	ChangeKindFormattingOnly ChangeKind = "formatting_only"
	ChangeKindOther          ChangeKind = "other"
)

// ChangeKindValues lists all kinds in canonical order.
var ChangeKindValues = []ChangeKind{
	ChangeKindBehavioral, ChangeKindAPIContract, ChangeKindConfiguration,
	ChangeKindArchitecture, ChangeKindDependency, ChangeKindTestOnly,
	ChangeKindFormattingOnly, ChangeKindOther,
}

// IsValidChangeKind reports membership.
func IsValidChangeKind(s string) bool {
	for _, v := range ChangeKindValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// DocumentationImpact is the documentation-impact answer value.
type DocumentationImpact string

// DocumentationImpact values (ARCH §5.4).
const (
	DocumentationImpactNone     DocumentationImpact = "none"
	DocumentationImpactPossible DocumentationImpact = "possible"
	DocumentationImpactDefinite DocumentationImpact = "definite"
	DocumentationImpactUnknown  DocumentationImpact = "unknown"
)

// DocumentationImpactValues lists all impacts in canonical order.
var DocumentationImpactValues = []DocumentationImpact{
	DocumentationImpactNone, DocumentationImpactPossible,
	DocumentationImpactDefinite, DocumentationImpactUnknown,
}

// IsValidDocumentationImpact reports membership.
func IsValidDocumentationImpact(s string) bool {
	for _, v := range DocumentationImpactValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// RiskLevel is the risk classification.
type RiskLevel string

// RiskLevel values (ARCH §5.4).
const (
	RiskLevelLow    RiskLevel = "low"
	RiskLevelMedium RiskLevel = "medium"
	RiskLevelHigh   RiskLevel = "high"
)

// RiskLevelValues lists all risk levels in canonical order.
var RiskLevelValues = []RiskLevel{RiskLevelLow, RiskLevelMedium, RiskLevelHigh}

// IsValidRiskLevel reports membership.
func IsValidRiskLevel(s string) bool {
	for _, v := range RiskLevelValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// Action is the Stage-1 routing outcome (ARCH §5.4).
type Action string

// Action values (ARCH §5.4): the Stage-1 routing verdict, not the policy
// engine's final outcome.
const (
	ActionIgnore      Action = "ignore"
	ActionReview      Action = "review"
	ActionInvestigate Action = "investigate"
	ActionPatch       Action = "patch"
)

// ActionValues lists all actions in canonical order.
var ActionValues = []Action{ActionIgnore, ActionReview, ActionInvestigate, ActionPatch}

// IsValidAction reports membership.
func IsValidAction(s string) bool {
	for _, v := range ActionValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// AbstainReason is one abstain taxonomy member.
type AbstainReason string

// AbstainReason values — Tier 1: the decision-level canonical five (ARCH §5.2,
// PLAN §3.3). Each lands in the review queue with evidence and becomes a
// labeled data point once resolved; abstain rate is a monitored
// per-policy-version metric (§5.2).
const (
	AbstainReasonInsufficientConfidence AbstainReason = "insufficient_confidence"
	AbstainReasonConflictingEvidence    AbstainReason = "conflicting_evidence"
	AbstainReasonPolicyGap              AbstainReason = "policy_gap"
	AbstainReasonCalibrationFloor       AbstainReason = "calibration_floor"
	AbstainReasonVerifierUncertain      AbstainReason = "verifier_uncertain"
)

// DecisionLevelAbstainReasons lists the Tier-1 reasons in canonical order.
var DecisionLevelAbstainReasons = []AbstainReason{
	AbstainReasonInsufficientConfidence, AbstainReasonConflictingEvidence,
	AbstainReasonPolicyGap, AbstainReasonCalibrationFloor, AbstainReasonVerifierUncertain,
}

// AbstainReason values — Tier 2: operational gateway-degradation reasons
// (schema_violation per PLAN §3.3's bounded-retry-then-abstain rule). These
// record degraded runs; they are never consumed as labeled policy-learning
// data.
const (
	AbstainReasonSchemaViolation  AbstainReason = "schema_violation"
	AbstainReasonModelUnavailable AbstainReason = "model_unavailable"
	AbstainReasonCapExceeded      AbstainReason = "cap_exceeded"
	AbstainReasonPolicyMissing    AbstainReason = "policy_missing"
	AbstainReasonOther            AbstainReason = "other"
)

// OperationalAbstainReasons lists the Tier-2 reasons in canonical order.
var OperationalAbstainReasons = []AbstainReason{
	AbstainReasonSchemaViolation, AbstainReasonModelUnavailable,
	AbstainReasonCapExceeded, AbstainReasonPolicyMissing, AbstainReasonOther,
}

// AbstainReasonValues is the stable-order union: five decision-level reasons
// first, operational extras, `other` last.
var AbstainReasonValues = []AbstainReason{
	AbstainReasonInsufficientConfidence, AbstainReasonConflictingEvidence,
	AbstainReasonPolicyGap, AbstainReasonCalibrationFloor,
	AbstainReasonVerifierUncertain,
	AbstainReasonSchemaViolation, AbstainReasonModelUnavailable,
	AbstainReasonCapExceeded, AbstainReasonPolicyMissing, AbstainReasonOther,
}

// IsValidAbstainReason reports membership.
func IsValidAbstainReason(s string) bool {
	for _, v := range AbstainReasonValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// EvidenceKind is the machine-resolvable evidence anchor taxonomy.
type EvidenceKind string

// EvidenceKind values (ARCH §5.4).
const (
	EvidenceKindSymbolDiff     EvidenceKind = "symbol_diff"
	EvidenceKindAPIDiff        EvidenceKind = "api_diff"
	EvidenceKindConfigDiff     EvidenceKind = "config_diff"
	EvidenceKindDependencyDiff EvidenceKind = "dependency_diff"
	EvidenceKindDocAnchor      EvidenceKind = "doc_anchor"
	EvidenceKindOther          EvidenceKind = "other"
)

// EvidenceKindValues lists all kinds in canonical order.
var EvidenceKindValues = []EvidenceKind{
	EvidenceKindSymbolDiff, EvidenceKindAPIDiff, EvidenceKindConfigDiff,
	EvidenceKindDependencyDiff, EvidenceKindDocAnchor, EvidenceKindOther,
}

// IsValidEvidenceKind reports membership.
func IsValidEvidenceKind(s string) bool {
	for _, v := range EvidenceKindValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// Evidence is a machine-resolvable evidence anchor — never free text
// (ARCH §5.4).
type Evidence struct {
	Kind EvidenceKind `json:"kind"`
	Ref  string       `json:"ref"`
	Note string       `json:"note"`
}

// Validate checks the evidence item against EVIDENCE_SCHEMA.
func (e *Evidence) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(e.Kind), stringsOf(EvidenceKindValues), []string{"kind"}))
	Collect(&issues, RequiredString(e.Ref, 1, []string{"ref"}))
	return issues
}

// Answer is one policy answer: branching uses `value`, policy thresholds use
// `probability`. Both are mandatory (ARCH §5.4) — `value` presence is
// enforced on decode and in Validate because a plain "any" field cannot
// express "required unknown" structurally.
type Answer struct {
	// Value is the raw JSON value; a nil/empty Value means the key was absent.
	Value json.RawMessage `json:"value"`
	// Probability is the confidence threshold input, in [0, 1].
	Probability float64 `json:"probability"`
}

// answerBothFieldsMessage mirrors the ANSWER_SCHEMA superRefine message.
const answerBothFieldsMessage = "every answer must carry both a value and a probability (ARCH §5.4)"

// UnmarshalJSON enforces the structural half of ANSWER_SCHEMA: zod's
// `z.unknown()` accepts a missing key, so TS pins presence with a
// superRefine. Decoding here fails closed on a missing `value` or
// `probability` key, and on unknown keys (strictObject semantics), so the
// decoder and Validate agree.
func (a *Answer) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key := range raw {
		if key != "value" && key != "probability" {
			return fmt.Errorf("json: unknown field %q", key)
		}
	}
	value, hasValue := raw["value"]
	probability, hasProbability := raw["probability"]
	if !hasValue || !hasProbability {
		return errors.New(answerBothFieldsMessage)
	}
	var p float64
	if err := json.Unmarshal(probability, &p); err != nil {
		return err
	}
	a.Value = value
	a.Probability = p
	return nil
}

// Validate checks the answer refinements: value presence and probability
// range.
func (a *Answer) Validate() []Issue {
	var issues []Issue
	if len(a.Value) == 0 {
		Collect(&issues, &Issue{Path: []string{"value"}, Message: answerBothFieldsMessage})
	}
	if a.Probability < 0 {
		Collect(&issues, &Issue{Path: []string{"probability"}, Message: "Number must be greater than or equal to 0"})
	} else if a.Probability > 1 {
		Collect(&issues, &Issue{Path: []string{"probability"}, Message: "Number must be less than or equal to 1"})
	}
	return issues
}

// UniversalQuestions are the universal question keys the bootstrap policy v1
// always runs (ARCH §8.3.1 / PLAN §3.3: the universal set always runs;
// repository-specific questions are additive and refine routing/risk).
// Bootstrap data — this slice is the seam the Jev runtime and the policy
// compiler (P2-T11/P2-T21/P2-T22) consume.
//
// Derivation: `public_behavior_changed`, `api_contract_changed`,
// `affected_doc_count` come from the ARCH §8.3 bootstrap policy example that
// the §5.4 wire example instantiates; `documentation_impact` is the answer
// probability ARCH §5.2's abstention seam keys on ("documentation_impact
// probability 0.83 falls between auto floor 0.94 and review threshold 0.72").
// The §8.3.1 prose names (`behavior_changed?`, `public_contract_changed?`,
// `documentation_impacted?`, `risk_level?`) are illustrative and map onto
// these keys; `risk_level` is deliberately NOT an answer key — risk is a
// top-level decision field derived as the conservative max of the manifest
// risk class and the Jev risk choice (ARCH §5.3). Additive repository
// questions extend this set per policy version; they never replace it.
var UniversalQuestions = []string{
	"public_behavior_changed",
	"api_contract_changed",
	"affected_doc_count",
	"documentation_impact",
}

// DecisionCandidatesConstraintNote documents why `candidates` membership is
// not encoded in the schema: the manifest is per-repository runtime data a
// repository-independent schema cannot reference, so structural enforcement
// would wrongly couple the wire format to one repository's allowlist
// (ARCH §5.4 mandatory property; enforced at decision emission (P2-T13) and
// re-verified by the orchestrator before any candidate reaches the patch
// layer).
const DecisionCandidatesConstraintNote = "candidates must reference manifest-known paths only (ARCH §5.4); enforced at " +
	"emission (P2-T13), not by this schema, because the manifest is per-repository " +
	"runtime data a static schema cannot reference"

// DecisionChange pins the analyzed change the decision routes (ARCH §5.4).
type DecisionChange struct {
	Commit            string `json:"commit"`
	DiffRef           string `json:"diff_ref"`
	ChangeAnalysisRef string `json:"change_analysis_ref"`
}

// Validate checks the change block.
func (c *DecisionChange) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.Commit, 1, []string{"commit"}))
	Collect(&issues, RequiredString(c.DiffRef, 1, []string{"diff_ref"}))
	Collect(&issues, RequiredString(c.ChangeAnalysisRef, 1, []string{"change_analysis_ref"}))
	return issues
}

// DecisionTrace is the provenance trace of one decision (ARCH §5.4).
type DecisionTrace struct {
	Model      string `json:"model"`
	PolicyHash string `json:"policy_hash"`
	LatencyMS  int    `json:"latency_ms"`
}

// Validate checks the trace block.
func (t *DecisionTrace) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(t.Model, 1, []string{"model"}))
	Collect(&issues, RequiredString(t.PolicyHash, 1, []string{"policy_hash"}))
	if t.LatencyMS < 0 {
		Collect(&issues, &Issue{Path: []string{"latency_ms"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// Decision is the decision object on the wire — `docsync.decision/v1`
// (ARCH §5.4, binding shape; finalized P2-T12). Produced by the
// decision-engine role; consumed by the orchestrator. Every answer carries
// BOTH a value and a probability (ARCH §5.4); abstention is first-class.
//
// `action` is the Stage-1 routing verdict — NOT the policy engine's final
// outcome: the confidence × risk matrix (ARCH §5.3) resolves to a policy
// `outcome` of ignore | suggest | auto | blocked under the versioned policy.
// The two live on different objects and must not be conflated.
type Decision struct {
	Schema              string              `json:"schema"`
	DecisionID          string              `json:"decision_id"`
	PolicyVersion       int                 `json:"policy_version"`
	Change              DecisionChange      `json:"change"`
	ChangeKind          ChangeKind          `json:"change_kind"`
	DocumentationImpact DocumentationImpact `json:"documentation_impact"`
	Risk                RiskLevel           `json:"risk"`
	Confidence          float64             `json:"confidence"`
	Answers             map[string]Answer   `json:"answers"`
	Candidates          []string            `json:"candidates"`
	Action              Action              `json:"action"`
	Abstain             bool                `json:"abstain"`
	AbstainReason       *AbstainReason      `json:"abstain_reason"`
	Evidence            []Evidence          `json:"evidence"`
	Trace               DecisionTrace       `json:"trace"`
}

// Validate checks the decision against DECISION_SCHEMA, including the
// abstain/abstain_reason pairing refinement.
func (d *Decision) Validate() []Issue {
	var issues []Issue
	if d.Schema != DecisionSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: "Invalid literal value, expected \"" + DecisionSchemaID + "\""})
	}
	Collect(&issues, RequiredString(d.DecisionID, 1, []string{"decision_id"}))
	if d.PolicyVersion <= 0 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	for _, iss := range d.Change.Validate() {
		iss.Path = append([]string{"change"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, CheckEnum(string(d.ChangeKind), stringsOf(ChangeKindValues), []string{"change_kind"}))
	Collect(&issues, CheckEnum(string(d.DocumentationImpact), stringsOf(DocumentationImpactValues), []string{"documentation_impact"}))
	Collect(&issues, CheckEnum(string(d.Risk), stringsOf(RiskLevelValues), []string{"risk"}))
	if d.Confidence < 0 {
		Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be greater than or equal to 0"})
	} else if d.Confidence > 1 {
		Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be less than or equal to 1"})
	}
	for _, key := range sortedKeys(d.Answers) {
		answer := d.Answers[key]
		Collect(&issues, RequiredString(key, 1, []string{"answers", key}))
		for _, iss := range answer.Validate() {
			iss.Path = append([]string{"answers", key}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i, candidate := range d.Candidates {
		Collect(&issues, RequiredString(candidate, 1, []string{"candidates", itoa(i)}))
	}
	Collect(&issues, CheckEnum(string(d.Action), stringsOf(ActionValues), []string{"action"}))
	if d.Abstain && d.AbstainReason == nil {
		Collect(&issues, &Issue{Path: []string{"abstain_reason"}, Message: "abstain_reason must name a reason when abstain is true"})
	}
	if !d.Abstain && d.AbstainReason != nil {
		Collect(&issues, &Issue{Path: []string{"abstain_reason"}, Message: "abstain_reason must be null when abstain is false"})
	}
	if d.AbstainReason != nil {
		Collect(&issues, CheckEnum(string(*d.AbstainReason), stringsOf(AbstainReasonValues), []string{"abstain_reason"}))
	}
	for i := range d.Evidence {
		for _, iss := range d.Evidence[i].Validate() {
			iss.Path = append([]string{"evidence", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range d.Trace.Validate() {
		iss.Path = append([]string{"trace"}, iss.Path...)
		issues = append(issues, iss)
	}
	return issues
}

// ParseDecision strictly parses and validates JSON decision bytes (strict
// decode rejects unknown keys, matching z.strictObject).
func ParseDecision(data []byte) (*Decision, error) {
	var d Decision
	if err := StrictDecode(data, &d); err != nil {
		return nil, err
	}
	if issues := d.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &d, nil
}

// DecisionExample is the ARCH §5.4 wire example (golden data).
var DecisionExample = Decision{
	Schema:              DecisionSchemaID,
	DecisionID:          "d_01J9ZK7Q2M",
	PolicyVersion:       17,
	Change:              DecisionChange{Commit: "81af92e", DiffRef: "git:81af92e", ChangeAnalysisRef: "analysis/01J9ZK6X8A"},
	ChangeKind:          ChangeKindBehavioral,
	DocumentationImpact: DocumentationImpactPossible,
	Risk:                RiskLevelMedium,
	Confidence:          0.91,
	Answers: map[string]Answer{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.87},
		"api_contract_changed":    {Value: json.RawMessage("false"), Probability: 0.12},
		"affected_doc_count":      {Value: json.RawMessage("2"), Probability: 0.91},
	},
	Candidates:    []string{"docs/payments.md", "docs/api.md"},
	Action:        ActionReview,
	Abstain:       false,
	AbstainReason: nil,
	Evidence: []Evidence{
		{Kind: EvidenceKindSymbolDiff, Ref: "src/payments/refund.ts:42", Note: "idempotencyKey became optional"},
	},
	Trace: DecisionTrace{
		Model:      "jev",
		PolicyHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		LatencyMS:  740,
	},
}

// DecisionAbstainExample is the first-class abstain example (ARCH §5.2): the
// deciding `documentation_impact` answer probability 0.66 sits below the
// review threshold (0.72) under policy v17 → Stage-1 abstains
// `insufficient_confidence` and routes to review — never forced into
// update/ignore. The overall `confidence` equals the deciding answer's
// probability (0.66), rule-consistent with the documented confidence
// aggregation. Answers carry all four universal question keys ARCH §5.2 keys
// abstention on.
var DecisionAbstainExample = Decision{
	Schema:              DecisionSchemaID,
	DecisionID:          "d_01J9ZK7Q2N",
	PolicyVersion:       17,
	Change:              DecisionChange{Commit: "81af92e", DiffRef: "git:81af92e", ChangeAnalysisRef: "analysis/01J9ZK6X8A"},
	ChangeKind:          ChangeKindBehavioral,
	DocumentationImpact: DocumentationImpactUnknown,
	Risk:                RiskLevelMedium,
	Confidence:          0.66,
	Answers: map[string]Answer{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.61},
		"api_contract_changed":    {Value: json.RawMessage("false"), Probability: 0.3},
		"affected_doc_count":      {Value: json.RawMessage("1"), Probability: 0.66},
		"documentation_impact":    {Value: json.RawMessage("\"unknown\""), Probability: 0.66},
	},
	Candidates:    []string{"docs/payments.md"},
	Action:        ActionReview,
	Abstain:       true,
	AbstainReason: abstainReasonPtr(AbstainReasonInsufficientConfidence),
	Evidence: []Evidence{
		{Kind: EvidenceKindSymbolDiff, Ref: "src/payments/refund.ts:42", Note: "idempotencyKey became optional"},
	},
	Trace: DecisionTrace{
		Model:      "jev",
		PolicyHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		LatencyMS:  810,
	},
}

func abstainReasonPtr(r AbstainReason) *AbstainReason { return &r }

/* ------------------------- canonical evidence-resolvability (V-P3-10) */

// jsSpaceClass is the ECMAScript \s class (WhiteSpace + LineTerminator),
// spelled out because Go's \s differs (no \v, no Unicode spaces).
const jsSpaceClass = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	// pathForbiddenRe is JS [\s\\:\0]: whitespace, backslash, colon (the
	// anchor separator), NUL.
	pathForbiddenRe = regexp.MustCompile("[" + jsSpaceClass + `\\:\x00]`)
	// lineNumberRe is a positive 1-based line number without leading zeros.
	lineNumberRe = regexp.MustCompile(`^[1-9][0-9]*$`)
	// symbolAnchorRe is an identifier-ish symbol anchor (tree-sitter style,
	// dot-qualified allowed).
	symbolAnchorRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_.$]*$`)
	// configKeyAnchorRe is a dotted config key anchor, e.g. `server.port`
	// (JS ^[^\s:]+$).
	configKeyAnchorRe = regexp.MustCompile("^[^" + jsSpaceClass + ":]+$")
	// dependencyNameRe is an npm package name shape (optional `@scope/`
	// prefix).
	dependencyNameRe = regexp.MustCompile(`^(@[A-Za-z0-9._-]+/)?[A-Za-z0-9._-]+$`)
)

// isPathish: repo-relative POSIX-path-like shape for evidence path components
// — non-empty, no whitespace, no NUL, no backslash, no colon (the anchor
// separator), no leading '/', no '.'/'..' segments.
func isPathish(path string) bool {
	if len(path) == 0 {
		return false
	}
	if pathForbiddenRe.MatchString(path) {
		return false
	}
	if strings.HasPrefix(path, "/") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if len(segment) == 0 || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// isLineNumber: positive 1-based line number without leading zeros.
func isLineNumber(anchor string) bool {
	return lineNumberRe.MatchString(anchor)
}

// isSymbolAnchor: identifier-ish symbol anchor (tree-sitter style,
// dot-qualified allowed).
func isSymbolAnchor(anchor string) bool {
	return symbolAnchorRe.MatchString(anchor)
}

// isConfigKeyAnchor: dotted config key anchor, e.g. `server.port`.
func isConfigKeyAnchor(anchor string) bool {
	return configKeyAnchorRe.MatchString(anchor)
}

// isDependencyName: npm package name shape (optional `@scope/` prefix),
// never `.`/`..` alone.
func isDependencyName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return dependencyNameRe.MatchString(name)
}

// splitPathAnchor splits `path:anchor` on the first colon; ok is false when
// no separator exists (or it is at either edge).
func splitPathAnchor(ref string) (path, anchor string, ok bool) {
	separator := strings.Index(ref, ":")
	if separator <= 0 || separator == len(ref)-1 {
		return "", "", false
	}
	return ref[:separator], ref[separator+1:], true
}

// EvidenceRefIssue returns the canonical per-kind resolvable-shape rejection
// reason for an evidence ref, or "" when resolvable (V-P3-10; ARCH §5.4
// "machine-resolvable anchors, not free text"). Deterministic regex/shape
// checks, single source per OD-2: consumed by decision emission AND by patch
// evidence handling. Per kind:
//
//   - `symbol_diff`: `path:line` | `path:SymbolName` (last segment
//     identifier-ish, e.g. `src/a.ts:42`, `src/a.ts:refundOrder`)
//   - `api_diff`: a non-empty repo-relative path (no anchor)
//   - `config_diff`: `file:key` (e.g. `config/app.yml:server.port`)
//   - `dependency_diff`: bare npm package name, no leading path separators
//     (e.g. `lodash`, `@scope/pkg`)
//   - `doc_anchor`: `path:line` (line form only)
//   - `other`: any non-empty ref
//   - unknown kinds are never resolvable (the wire schema also rejects them)
//
// The `note` field is human explanation and is never shape-checked — the REF
// is the anchor.
func EvidenceRefIssue(kind, ref string) string {
	if kind == string(EvidenceKindOther) {
		if len(ref) > 0 {
			return ""
		}
		return "other evidence ref must be non-empty"
	}
	if !IsValidEvidenceKind(kind) {
		return unknownEvidenceKindMessage(kind)
	}
	switch EvidenceKind(kind) {
	case EvidenceKindSymbolDiff:
		splitPath, splitAnchor, splitOK := splitPathAnchor(ref)
		if !splitOK {
			return "symbol_diff ref must be 'path:line' or 'path:symbolName' (got '" + ref + "')"
		}
		if !isPathish(splitPath) {
			return "symbol_diff path component '" + splitPath + "' is not a repo-relative path"
		}
		if !isLineNumber(splitAnchor) && !isSymbolAnchor(splitAnchor) {
			return "symbol_diff anchor '" + splitAnchor + "' is neither a 1-based line number nor a symbol name"
		}
		return ""
	case EvidenceKindAPIDiff:
		if !isPathish(ref) {
			return "api_diff ref must be a repo-relative path without an anchor (got '" + ref + "')"
		}
		return ""
	case EvidenceKindConfigDiff:
		splitPath, splitAnchor, splitOK := splitPathAnchor(ref)
		if !splitOK {
			return "config_diff ref must be 'file:key' (got '" + ref + "')"
		}
		if !isPathish(splitPath) {
			return "config_diff file component '" + splitPath + "' is not a repo-relative path"
		}
		if !isConfigKeyAnchor(splitAnchor) {
			return "config_diff key component '" + splitAnchor + "' is empty or malformed"
		}
		return ""
	case EvidenceKindDependencyDiff:
		if !isDependencyName(ref) {
			return "dependency_diff ref must be an npm package name (got '" + ref + "')"
		}
		return ""
	case EvidenceKindDocAnchor:
		splitPath, splitAnchor, splitOK := splitPathAnchor(ref)
		if !splitOK {
			return "doc_anchor ref must be 'path:line' (got '" + ref + "')"
		}
		if !isPathish(splitPath) {
			return "doc_anchor path component '" + splitPath + "' is not a repo-relative path"
		}
		if !isLineNumber(splitAnchor) {
			return "doc_anchor anchor '" + splitAnchor + "' is not a 1-based line number"
		}
		return ""
	default:
		return unknownEvidenceKindMessage(kind)
	}
}

func unknownEvidenceKindMessage(kind string) string {
	return "unknown evidence kind '" + kind + "' — not a machine-resolvable anchor taxonomy member"
}

// IsResolvableEvidence is the pure predicate form of the canonical V-P3-10
// rules (EvidenceRefIssue): true iff the ref is a machine-resolvable anchor
// for its kind. Consumed by decision emission AND patch evidence. The TS form
// took an evidence object; the `note` is accepted for parity but never
// consulted (the REF is the anchor).
func IsResolvableEvidence(kind, ref string) bool {
	return EvidenceRefIssue(kind, ref) == ""
}

// EvidenceRef is the shape the resolvable-shape rules consume (kind + ref).
// Note is carried by callers but never shape-checked.
type EvidenceRef struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}

// UnresolvableEvidenceItem is one evidence item that failed the canonical
// resolvable-shape rules.
type UnresolvableEvidenceItem struct {
	Index  int    `json:"index"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// EvidenceNotResolvableErrorCode is the stable machine-readable failure code
// (TS EvidenceNotResolvableError.code) consumers can switch on.
const EvidenceNotResolvableErrorCode = "EVIDENCE_NOT_RESOLVABLE"

// EvidenceNotResolvableError is the canonical V-P3-10 gate failure, mapped
// from the TS class (docutil.CodeSchema).
type EvidenceNotResolvableError struct {
	// Offending lists the offending items in input order, with per-item
	// canonical reasons.
	Offending []UnresolvableEvidenceItem
	// Where names the assertion site (e.g. 'decision emission').
	Where string
}

// Code is the stable failure code (TS class code).
func (e *EvidenceNotResolvableError) Code() string { return EvidenceNotResolvableErrorCode }

// Unwrap maps the gate failure onto the docutil coded-error class.
func (e *EvidenceNotResolvableError) Unwrap() error {
	return docutil.New(docutil.CodeSchema, e.Error())
}

// Error renders the canonical failure message: `where` prefix + V-P3-10
// marker + offender list.
func (e *EvidenceNotResolvableError) Error() string {
	where := e.Where
	if where == "" {
		where = "evidence"
	}
	parts := make([]string, len(e.Offending))
	for i, item := range e.Offending {
		parts[i] = "[" + itoa(item.Index) + "] " + item.Kind + " '" + item.Ref + "': " + item.Reason
	}
	return where + ": evidence refs must be machine-resolvable anchors (ARCH §5.4; V-P3-10) — " + strings.Join(parts, "; ")
}

// AssertResolvableEvidence is the canonical V-P3-10 gate: validates EVERY
// evidence item against the per-kind resolvable-shape rules, collects every
// offender, and returns one EvidenceNotResolvableError if any. Zero items
// pass (emptiness is the callers' stricter rule where it applies).
// Deterministic; where names the assertion site for the error message
// ("" defaults to 'evidence').
func AssertResolvableEvidence(items []EvidenceRef, where string) error {
	var offending []UnresolvableEvidenceItem
	for index, item := range items {
		if reason := EvidenceRefIssue(item.Kind, item.Ref); reason != "" {
			offending = append(offending, UnresolvableEvidenceItem{
				Index:  index,
				Kind:   item.Kind,
				Ref:    item.Ref,
				Reason: reason,
			})
		}
	}
	if len(offending) > 0 {
		return &EvidenceNotResolvableError{Offending: offending, Where: where}
	}
	return nil
}

func sortedKeys(m map[string]Answer) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
