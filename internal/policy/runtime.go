// Ported from packages/policy/src/jev/runtime.ts (P2-T10, P2-T11; ARCH §4.6,
// §8.3.1, §10.2) — the typed Stage-1 decision layer around the
// decision-engine role.
//
// Responsibilities (exactly these — nothing more):
//  1. Fail-closed configuration gate: the COMPOSED question set of the compiled
//     workflow (universal always runs; repository questions are additive and
//     already composed by the compiler — P2-T11 via composeQuestions) must
//     cover every UNIVERSAL_QUESTIONS key. A workflow missing a universal key
//     is a JevConfigError returned BEFORE any model call — never degraded into
//     a runtime abstention, because it is a configuration defect (exit-code 2
//     territory), not a model failure.
//  2. ONE call to the decision-engine port, then a SINGLE answer-contract
//     validation gate: every returned answer must be keyed exactly by the
//     asked question keys (hallucinated/missing keys are contract violations),
//     carry a value matching the question type (boolean → boolean, choice →
//     one of the question's choices, score → number within the question's
//     scoring bounds) and a probability in [0, 1]. Violation → operational
//     abstain (schema_violation). The gateway's bounded retry already ran
//     (§10.4); the runtime does NOT retry — one validation gate, then abstain.
//  3. Deterministic Stage-1 routing: decision.action is OVERWRITTEN with the
//     three-way routing verdict from JEV_ROUTING_TABLE — the model's proposed
//     action is carried as ProposedAction provenance only (models propose; the
//     application decides). The table is threshold-free by design: thresholds
//     (high_confidence / review / calibration floors) gate the policy-ENGINE
//     outcome mapping and the §5.2 abstention bands in the NEXT wave
//     (P2-T18/T19), never the Stage-1 verdict.
//  4. Final DECISION_SCHEMA validation of the emitted decision — fail closed
//     on drift (operational schema_violation), so nothing invalid flows
//     downstream.
//
// NO prose generation — the port contract is structured answers only; the
// runtime never builds prompts and never sees prompt text.
//
// DecisionEnginePort seam (D9/D13; ARCH §10.2): a STRUCTURAL port mirroring
// @docsync/openrouter's DecisionEngineRole/DecisionEngineResult/RoleTelemetry/
// DecisionEngineInput/DecisionAnalysisContext shapes. The openrouter adapter
// satisfies these types structurally with zero import — internal/policy must
// NOT depend on internal/openrouter (the dependency runs the other way:
// openrouter deps policy). The orchestrator (Phase 5) wires the concrete
// adapter into RunJev.
//
// Cross-unit note: the Jev decision-stage seam (decision-stage.go) consumes
// this unit's RunJevInput/Compiled/JevOutcome/JevUsage/Provider shapes and its
// *JevMemoCache satisfies its JevMemoCachePick interface directly.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------ DecisionEnginePort (structural seam) */

// JevAnalysisContext is a structural mirror of @docsync/openrouter's
// DecisionAnalysisContext (roles.ts).
type JevAnalysisContext struct {
	ChangeKind          schemas.ChangeKind
	DocumentationImpact schemas.DocumentationImpact
	Risk                schemas.RiskLevel
	Confidence          float64
	// Answers are analysis-derived grounding answers (NOT the model's returned
	// answers).
	Answers JevAnswers
	// Candidates are manifest-known candidate paths the model selects from.
	Candidates []string
	Evidence   []schemas.Evidence
}

// JevChangeRefs is a structural mirror of @docsync/openrouter's
// DecisionChangeRefs.
type JevChangeRefs struct {
	Commit            string
	DiffRef           string
	ChangeAnalysisRef string
}

// JevPortInput is a structural mirror of @docsync/openrouter's
// DecisionEngineInput.
type JevPortInput struct {
	AnalysisContext JevAnalysisContext
	Change          JevChangeRefs
	// UntrustedContent is optional (TS untrustedContent?: readonly string[]).
	UntrustedContent []string
	// TemperatureOverride is optional (TS temperatureOverride?: number).
	TemperatureOverride *float64
	// CachePrefix is optional (TS cachePrefix?: boolean).
	CachePrefix *bool
}

// JevTelemetryUsage is the usage block of @docsync/openrouter's RoleTelemetry
// (TS RoleTelemetry.usage; the Go name JevUsage matches the stage seam's
// written expectation).
type JevUsage struct {
	PromptTokens     int
	CompletionTokens int
	// TotalTokens is optional (TS total_tokens?: number).
	TotalTokens *int
	// Cost is optional (TS cost?: number).
	Cost *float64
}

// JevTelemetry is a structural mirror of @docsync/openrouter's RoleTelemetry —
// provenance fields carried through outcomes so degraded runs stay
// attributable (§10.6).
type JevTelemetry struct {
	Role           string
	RequestedModel string
	RoutedModel    string
	// Provider is optional (TS provider?: string); nil == absent.
	Provider *string
	// Usage is optional (TS usage?).
	Usage         *JevUsage
	LatencyMS     int
	Attempt       int
	FallbackUsed  bool
	Cache         *JevTelemetryCache
	PolicyVersion *int
}

// JevTelemetryCache mirrors the cache block of RoleTelemetry.
type JevTelemetryCache struct {
	PrefixKey string
	PrefixHit bool
}

// JevPortResultKind is the tagged-union kind of JevPortResult.
type JevPortResultKind string

// JevPortResultKind values (TS JevPortResult union).
const (
	JevPortResultKindDecision JevPortResultKind = "decision"
	JevPortResultKindAbstain  JevPortResultKind = "abstain"
)

// JevPortResult is a structural mirror of @docsync/openrouter's
// DecisionEngineResult: decision-level abstention (abstain: true on the
// Decision) is a SUCCESSFUL kind "decision" completion; kind "abstain" is
// operational degradation only (bounded-retry exhaustion etc.).
//
// Invariant: Decision is non-nil iff Kind is JevPortResultKindDecision; Reason
// is only read when Kind is JevPortResultKindAbstain.
type JevPortResult struct {
	Kind      JevPortResultKind
	Decision  *schemas.Decision
	Reason    schemas.AbstainReason
	Telemetry JevTelemetry
}

// DecisionEnginePort is the Stage-1 structural port.
// @docsync/openrouter's DecisionEngineRole satisfies this interface
// structurally (identical member shapes); policy never imports openrouter.
// The orchestrator (Phase 5) wires the concrete adapter created by
// createDecisionEngineAdapter.
//
// The error return is the Go form of the TS promise rejection: RunJev
// propagates it verbatim (the decision-stage seam maps it to an operational
// abstain, exactly like the TS orchestrator catch).
type DecisionEnginePort interface {
	Decide(input JevPortInput) (JevPortResult, error)
}

/* ---------------------------------------------------------------------- errors */

// JevConfigErrorCode is the stable machine-readable failure code
// (TS JevConfigError.code = 'JEV_CONFIG').
const JevConfigErrorCode = "JEV_CONFIG"

// JevConfigError is the fail-closed configuration error: returned by RunJev
// BEFORE any model call when the compiled workflow's composed question set
// does not cover the canonical UNIVERSAL_QUESTIONS. A configuration defect
// (exit-code 2 territory) — never coerced into a runtime abstention.
type JevConfigError struct {
	// MissingUniversalKeys lists the canonical universal keys the composed set
	// misses, in UNIVERSAL_QUESTIONS order.
	MissingUniversalKeys []string
	// Message preserves the TS message text exactly.
	Message string
}

func newJevConfigError(missingUniversalKeys []string) *JevConfigError {
	return &JevConfigError{
		MissingUniversalKeys: append([]string(nil), missingUniversalKeys...),
		Message: "compiled workflow questions miss canonical universal question(s): " +
			strings.Join(missingUniversalKeys, ", ") +
			" (UNIVERSAL_QUESTIONS of docsync.decision/v1) — " +
			"the universal set always runs (ARCH §8.3.1); fail closed before any model call",
	}
}

func (e *JevConfigError) Error() string { return e.Message }

// Code returns the stable failure code (TS class code).
func (e *JevConfigError) Code() string { return JevConfigErrorCode }

// Unwrap maps the class onto the docutil coded-error family (a configuration
// defect — exit-code 2 territory, docutil.CodeConfig).
func (e *JevConfigError) Unwrap() error {
	return docutil.New(docutil.CodeConfig, e.Message)
}

// AnswerContractErrorCode is the stable machine-readable failure code
// (TS AnswerContractError.code = 'ANSWER_CONTRACT').
const AnswerContractErrorCode = "ANSWER_CONTRACT"

// AnswerContractError is the answer-contract violation between the port's
// returned answers and the compiled workflow's typed questions (P2-T10). It
// is returned by AssertAnswerContract; RunJev catches it at its single
// validation gate and maps it to the operational abstain schema_violation
// (the gateway's bounded retry already ran — the runtime never retries).
type AnswerContractError struct {
	// Violations holds one machine-readable violation per offending answer, in
	// asked-key order (hallucinated keys first, then per asked question).
	Violations []string
	// Message preserves the TS message text exactly.
	Message string
}

func newAnswerContractError(violations []string) *AnswerContractError {
	return &AnswerContractError{
		Violations: append([]string(nil), violations...),
		Message: fmt.Sprintf("answer contract violation (%d): %s", len(violations), strings.Join(violations, "; ")) +
			" — operational abstain schema_violation; the runtime does not retry",
	}
}

func (e *AnswerContractError) Error() string { return e.Message }

// Code returns the stable failure code (TS class code).
func (e *AnswerContractError) Code() string { return AnswerContractErrorCode }

// Unwrap maps the class onto the docutil coded-error family (an answer-contract
// violation is a schema violation — docutil.CodeSchema).
func (e *AnswerContractError) Unwrap() error {
	return docutil.New(docutil.CodeSchema, e.Message)
}

/* ------------------------------------------------------- answer-contract gate */

// decodeJSONValue decodes a raw JSON value for shape checks; ok is false when
// the raw bytes are absent or not valid JSON (TS would have failed at parse).
func decodeJSONValue(raw json.RawMessage) (value any, ok bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return v, true
}

// jsTypeof renders the JS typeof of the JSON value ("undefined" when the value
// is absent — the TS templates interpolate ${typeof value}).
func jsTypeof(raw json.RawMessage) string {
	v, ok := decodeJSONValue(raw)
	if !ok {
		return "undefined"
	}
	switch v.(type) {
	case nil:
		return "object" // typeof null === 'object'
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	default:
		return "object"
	}
}

// jsStringOfValue renders a decoded JSON value the way JS template
// interpolation (String(value)) renders it.
func jsStringOfValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return formatJSNumber(t)
	case string:
		return t
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = jsStringOfValue(item)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// jsStringOf renders the JSON value the way JS template interpolation
// (String(value)) renders it ("undefined" when absent).
func jsStringOf(raw json.RawMessage) string {
	if v, ok := decodeJSONValue(raw); ok {
		return jsStringOfValue(v)
	}
	if len(raw) == 0 {
		return "undefined"
	}
	return string(raw)
}

// AssertAnswerContract is the single validation gate for the port's returned
// answers against the composed typed questions (P2-T10; ARCH §4.6).
// Deterministic; collects EVERY violation (never fail-fast — the degradation
// record needs the full picture) and returns one *AnswerContractError if any.
// Zero violations → returns nil.
//
// Contract (binding):
//   - answers must be keyed EXACTLY by the asked question keys — a
//     hallucinated (extra) key or a missing key is a violation (no guessing).
//   - value presence and type per question type: boolean → boolean; choice →
//     one of the question's choices; score → finite number within
//     [scoring.min, scoring.max].
//   - probability → finite number in [0, 1].
//
// Deviation from TS: the gate takes the typed answer map of a
// *schemas.Decision rather than `unknown`. The TS "answers must be an object"
// / "answer must be an object {value, probability}" guards exist because the
// TS port seam is structurally untyped; the Go port is typed
// (*schemas.Decision), so those guards are unreachable by construction and the
// gate checks everything they guarded: key set, value presence and type, and
// probability range.
func AssertAnswerContract(questions []Question, answers map[string]schemas.Answer) error {
	violations := make([]string, 0)
	asked := make(map[string]bool, len(questions))
	for _, question := range questions {
		asked[question.Key] = true
	}
	// TS iterates Object.keys(answers) (wire order); Go maps are unordered, so
	// iterate the sorted keys — deterministic violation order.
	for _, key := range jevSortedKeys(jevAnswersOf(answers)) {
		if !asked[key] {
			violations = append(violations, fmt.Sprintf("hallucinated answer key '%s' — not in the asked composed question set", key))
		}
	}
	for _, question := range questions {
		raw, present := answers[question.Key]
		if !present {
			violations = append(violations, fmt.Sprintf("missing answer for asked question key '%s'", question.Key))
			continue
		}
		if len(raw.Value) == 0 {
			violations = append(violations, fmt.Sprintf("answer '%s' must carry a value (ARCH §5.4)", question.Key))
		}
		if math.IsNaN(raw.Probability) || math.IsInf(raw.Probability, 0) {
			violations = append(violations, fmt.Sprintf("answer '%s' probability must be a finite number (got %s)", question.Key, formatJSNumber(raw.Probability)))
		} else if raw.Probability < 0 || raw.Probability > 1 {
			violations = append(violations, fmt.Sprintf("answer '%s' probability must lie in [0, 1] (got %s)", question.Key, formatJSNumber(raw.Probability)))
		}
		switch question.Type {
		case QuestionTypeBoolean:
			if value, ok := decodeJSONValue(raw.Value); !ok || !isJSONBool(value) {
				violations = append(violations, fmt.Sprintf("answer '%s' is a boolean question — value must be true|false (got %s)", question.Key, jsTypeof(raw.Value)))
			}
		case QuestionTypeChoice:
			choices := question.Choices
			if value, ok := decodeJSONValue(raw.Value); !ok || !isJSONStringIn(value, choices) {
				violations = append(violations, fmt.Sprintf("answer '%s' is a choice question — value must be one of [%s] (got %s)", question.Key, strings.Join(choices, ", "), jsStringOf(raw.Value)))
			}
		case QuestionTypeScore:
			value, ok := decodeJSONValue(raw.Value)
			number, isNumber := value.(float64)
			if !ok || !isNumber || math.IsNaN(number) || math.IsInf(number, 0) {
				violations = append(violations, fmt.Sprintf("answer '%s' is a score question — value must be a finite number (got %s)", question.Key, jsStringOf(raw.Value)))
			} else if question.Scoring != nil && (number < question.Scoring.Min || number > question.Scoring.Max) {
				violations = append(violations, fmt.Sprintf("answer '%s' score %s outside the question scoring bounds [%s, %s]", question.Key, formatJSNumber(number), formatJSNumber(question.Scoring.Min), formatJSNumber(question.Scoring.Max)))
			}
		}
	}
	if len(violations) > 0 {
		return newAnswerContractError(violations)
	}
	return nil
}

// jevAnswersOf adapts a plain answer map to JevAnswers for the shared sorted
// key helper (both are map[string]schemas.Answer).
func jevAnswersOf(answers map[string]schemas.Answer) JevAnswers {
	return JevAnswers(answers)
}

// isJSONBool reports whether the decoded value is a JSON boolean.
func isJSONBool(v any) bool {
	_, ok := v.(bool)
	return ok
}

// isJSONStringIn reports whether the decoded value is a JSON string member of
// choices.
func isJSONStringIn(v any, choices []string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, choice := range choices {
		if choice == s {
			return true
		}
	}
	return false
}

/* ----------------------------------------------------------- Stage-1 routing */

// JevStage1Verdict is the three-way Stage-1 routing outcome
// (ARCH §4.6: IGNORE | REVIEW | CONTINUE).
type JevStage1Verdict string

// JevStage1Verdict values.
const (
	// JevStage1VerdictIgnore is the §4.6 IGNORE leg.
	JevStage1VerdictIgnore JevStage1Verdict = "ignore"
	// JevStage1VerdictReview is the §4.6 REVIEW leg.
	JevStage1VerdictReview JevStage1Verdict = "review"
	// JevStage1VerdictInvestigate is the §4.6 CONTINUE leg (route onward to the
	// writer stage).
	JevStage1VerdictInvestigate JevStage1Verdict = "investigate"
)

// JevStage1VerdictValues is the closed Stage-1 verdict set
// (TS JEV_STAGE1_VERDICT_VALUES).
var JevStage1VerdictValues = []JevStage1Verdict{
	JevStage1VerdictIgnore,
	JevStage1VerdictReview,
	JevStage1VerdictInvestigate,
}

// JevRoutingSignals are the routing inputs (TS JevRoutingSignals).
type JevRoutingSignals struct {
	ChangeKind schemas.ChangeKind
	// AnalysisImpact is the documentation impact from the DETERMINISTIC
	// analysis context.
	AnalysisImpact schemas.DocumentationImpact
	// AnswerImpact is the documentation impact ANSWER value from the model
	// (typed answer). nil only when the answer is absent — the conflict guard
	// then cannot fire (the answer-contract gate makes this unreachable for
	// the universal set).
	AnswerImpact *schemas.DocumentationImpact
	// Risk is from the deterministic analysis context (conservative-max vs the
	// manifest is ENGINE work, §5.3).
	Risk schemas.RiskLevel
	// Abstained is the port's decision-level abstention flag.
	Abstained bool
}

// JevRoutingTableEntry is one routing-table entry — the documented guard and
// its deterministic verdict (TS JevRoutingTableEntry).
type JevRoutingTableEntry struct {
	ID      JevRoutingRuleId
	Guard   string
	Verdict JevStage1Verdict
}

// JevRoutingRuleId names one routing-table entry.
type JevRoutingRuleId string

// JevRoutingRuleIDs is the fixed evaluation order of the routing table
// (first match wins; the last entry is the total-space catch-all).
var JevRoutingRuleIDs = []JevRoutingRuleId{
	"abstained_review",
	"conflicting_signals",
	"definite_high_risk",
	"definite_investigate",
	"none_safe_trivial",
	"possible_review",
	"unknown_review",
	"none_conservative_default",
}

// JevRoutingTable is the EXACT Stage-1 routing table the runtime implements —
// deterministic, threshold-free, fixed evaluation order = JevRoutingRuleIDs
// (first match wins; the last entry is the total-space catch-all). No
// thresholds live here: high_confidence/review/calibration-floor gating
// belongs to the policy ENGINE's outcome mapping + §5.2 abstention (next wave,
// P2-T18/T19), never to the Stage-1 verdict.
//
// "Conflicting signals" is precisely: the documentation_impact ANSWER value
// disagrees with the analysis context's deterministic documentationImpact
// (the §5.2 conflicting_evidence definition — answers vs deterministic
// analysis). In v1 the analysis context overlaps the answers on exactly this
// one field; more overlap → new table entries via a policy/compiler change,
// never prompt prose (ARCH §8.3).
var JevRoutingTable = map[JevRoutingRuleId]JevRoutingTableEntry{
	"abstained_review": {
		ID:      "abstained_review",
		Guard:   "the decision abstained (decision.abstain true) → human review, always (§5.2 hard rule)",
		Verdict: JevStage1VerdictReview,
	},
	"conflicting_signals": {
		ID:      "conflicting_signals",
		Guard:   "documentation_impact answer value ≠ analysis-context documentationImpact → conflicting evidence (§5.2)",
		Verdict: JevStage1VerdictReview,
	},
	"definite_high_risk": {
		ID:      "definite_high_risk",
		Guard:   "documentation impact definite AND risk high → too risky to route onward",
		Verdict: JevStage1VerdictReview,
	},
	"definite_investigate": {
		ID:      "definite_investigate",
		Guard:   "documentation impact definite AND risk low|medium → continue to the writer stage",
		Verdict: JevStage1VerdictInvestigate,
	},
	"none_safe_trivial": {
		ID:      "none_safe_trivial",
		Guard:   "documentation impact none AND risk low AND change_kind formatting_only|test_only → nothing to do",
		Verdict: JevStage1VerdictIgnore,
	},
	"possible_review": {
		ID:      "possible_review",
		Guard:   "documentation impact possible → needs human triage",
		Verdict: JevStage1VerdictReview,
	},
	"unknown_review": {
		ID:      "unknown_review",
		Guard:   "documentation impact unknown → needs human triage",
		Verdict: JevStage1VerdictReview,
	},
	"none_conservative_default": {
		ID:      "none_conservative_default",
		Guard:   "documentation impact none BUT risk elevated or a substantive change kind → do not auto-ignore (fail-safe default)",
		Verdict: JevStage1VerdictReview,
	},
}

// JevRoutingOutcome is the resolved Stage-1 verdict (TS JevRoutingOutcome).
type JevRoutingOutcome struct {
	// Verdict is the Stage-1 verdict written into decision.action.
	Verdict JevStage1Verdict
	// Rule is the JevRoutingTable entry id that produced the verdict.
	Rule JevRoutingRuleId
}

// jevTrivialChangeKinds is the TRIVIAL_CHANGE_KINDS set of the
// none_safe_trivial guard (TS: new Set(['formatting_only', 'test_only'])).
var jevTrivialChangeKinds = map[schemas.ChangeKind]bool{
	schemas.ChangeKindFormattingOnly: true,
	schemas.ChangeKindTestOnly:       true,
}

// ResolveStage1Verdict resolves the deterministic Stage-1 verdict — pure,
// total over the signal space (pinned by a cross-product totality test), no
// thresholds, no clocks. First matching JevRoutingRuleIDs entry wins.
//
// The unreachable tail (the TS `throw new Error('JEV_ROUTING_TABLE is not
// total …')`) returns a docutil.CodeInternal-coded error: fail closed on
// internal drift rather than guess a verdict.
func ResolveStage1Verdict(signals JevRoutingSignals) (JevRoutingOutcome, error) {
	conflict := signals.AnswerImpact != nil && *signals.AnswerImpact != signals.AnalysisImpact
	for _, id := range JevRoutingRuleIDs {
		switch id {
		case "abstained_review":
			if signals.Abstained {
				return JevRoutingOutcome{Verdict: JevStage1VerdictReview, Rule: id}, nil
			}
		case "conflicting_signals":
			if conflict {
				return JevRoutingOutcome{Verdict: JevStage1VerdictReview, Rule: id}, nil
			}
		case "definite_high_risk":
			if signals.AnalysisImpact == schemas.DocumentationImpactDefinite && signals.Risk == schemas.RiskLevelHigh {
				return JevRoutingOutcome{Verdict: JevStage1VerdictReview, Rule: id}, nil
			}
		case "definite_investigate":
			if signals.AnalysisImpact == schemas.DocumentationImpactDefinite {
				return JevRoutingOutcome{Verdict: JevStage1VerdictInvestigate, Rule: id}, nil
			}
		case "none_safe_trivial":
			if signals.AnalysisImpact == schemas.DocumentationImpactNone &&
				signals.Risk == schemas.RiskLevelLow &&
				jevTrivialChangeKinds[signals.ChangeKind] {
				return JevRoutingOutcome{Verdict: JevStage1VerdictIgnore, Rule: id}, nil
			}
		case "possible_review":
			if signals.AnalysisImpact == schemas.DocumentationImpactPossible {
				return JevRoutingOutcome{Verdict: JevStage1VerdictReview, Rule: id}, nil
			}
		case "unknown_review":
			if signals.AnalysisImpact == schemas.DocumentationImpactUnknown {
				return JevRoutingOutcome{Verdict: JevStage1VerdictReview, Rule: id}, nil
			}
		case "none_conservative_default":
			// Catch-all guard of the last entry: signal 'none' with elevated
			// risk or a substantive change kind. The entries above already
			// consumed definite/possible/unknown and the safe-trivial none
			// cell.
			if signals.AnalysisImpact == schemas.DocumentationImpactNone {
				return JevRoutingOutcome{Verdict: JevStage1VerdictReview, Rule: id}, nil
			}
		}
	}
	// Unreachable: none_conservative_default + possible/unknown cover the whole
	// impact domain. Fail closed on internal drift rather than guess a verdict.
	return JevRoutingOutcome{}, docutil.New(docutil.CodeInternal, fmt.Sprintf(
		"JEV_ROUTING_TABLE is not total for signals %s — internal invariant violation",
		jevSignalsJSON(signals),
	))
}

// jevSignalsJSON renders the signals the way the TS unreachable-branch message
// does (JSON.stringify of the signals object).
func jevSignalsJSON(signals JevRoutingSignals) string {
	payload := struct {
		ChangeKind     string  `json:"changeKind"`
		AnalysisImpact string  `json:"analysisImpact"`
		AnswerImpact   *string `json:"answerImpact,omitempty"`
		Risk           string  `json:"risk"`
		Abstained      bool    `json:"abstained"`
	}{
		ChangeKind:     string(signals.ChangeKind),
		AnalysisImpact: string(signals.AnalysisImpact),
		Risk:           string(signals.Risk),
		Abstained:      signals.Abstained,
	}
	if signals.AnswerImpact != nil {
		impact := string(*signals.AnswerImpact)
		payload.AnswerImpact = &impact
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(data)
}

/* ------------------------------------------------------------------- runJev */

// RunJevInput is the RunJev input (TS RunJevInput).
type RunJevInput struct {
	// Compiled is the compiled workflow — the answer-contract gate and the
	// universal-coverage gate read its composed questions (TS:
	// input.compiled).
	Compiled CompiledPolicyWorkflow
	// Port is the structural decision-engine port (openrouter adapter wired by
	// Phase 5).
	Port DecisionEnginePort
	// Change is forwarded to the port verbatim.
	Change JevChangeRefs
	// AnalysisContext is forwarded to the port verbatim.
	AnalysisContext JevAnalysisContext
	// UntrustedContent is optional (TS untrustedContent?: readonly string[]).
	UntrustedContent []string
	// TemperatureOverride is optional (TS temperatureOverride?: number).
	TemperatureOverride *float64
	// CachePrefix is optional (TS cachePrefix?: boolean).
	CachePrefix *bool
}

// JevOutcomeKind is the tagged-union kind of JevOutcome.
type JevOutcomeKind string

// JevOutcomeKind values (TS JevOutcome union).
const (
	JevOutcomeDecision JevOutcomeKind = "decision"
	JevOutcomeAbstain  JevOutcomeKind = "abstain"
)

// JevOutcome is the RunJev outcome (TS JevOutcome union).
//
// Invariant: Kind == JevOutcomeDecision carries Decision (schema-valid,
// action = the Stage-1 routing verdict), Routing and ProposedAction; Kind ==
// JevOutcomeAbstain carries Reason and Detail. Telemetry is always the
// port's telemetry.
type JevOutcome struct {
	Kind           JevOutcomeKind
	Decision       *schemas.Decision
	Routing        *JevRoutingOutcome
	ProposedAction schemas.Action
	Reason         schemas.AbstainReason
	Detail         string
	Telemetry      JevTelemetry
}

// RunJev runs the Jev Stage-1 decision: one port call, one answer-contract
// gate, deterministic routing, fail-closed final schema parse. Never retries,
// never hand-patches, never produces prose. See the module doc for the full
// contract.
//
// The only returned Go error is the fail-closed configuration gate
// (*JevConfigError — exit-code 2 territory) or an unexpected propagation from
// the port (the TS promise rejection); every other degradation is a kind
// "abstain" outcome, exactly as in the TS implementation.
func RunJev(input RunJevInput) (JevOutcome, error) {
	// 1. Fail-closed configuration gate (P2-T11): the composed set must cover
	//    the universal keys — BEFORE any model call.
	composed := make(map[string]bool, len(input.Compiled.Questions))
	for _, question := range input.Compiled.Questions {
		composed[question.Key] = true
	}
	var missing []string
	for _, key := range schemas.UniversalQuestions {
		if !composed[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return JevOutcome{}, newJevConfigError(missing)
	}

	// 2. ONE port call — the gateway's bounded retry already ran; no retry here.
	result, err := input.Port.Decide(JevPortInput{
		AnalysisContext:     input.AnalysisContext,
		Change:              input.Change,
		UntrustedContent:    input.UntrustedContent,
		TemperatureOverride: input.TemperatureOverride,
		CachePrefix:         input.CachePrefix,
	})
	if err != nil {
		return JevOutcome{}, err
	}
	if result.Kind == JevPortResultKindAbstain {
		return JevOutcome{
			Kind:      JevOutcomeAbstain,
			Reason:    result.Reason,
			Detail:    fmt.Sprintf("decision-engine port degraded with operational abstain '%s' — passed through without retry", result.Reason),
			Telemetry: result.Telemetry,
		}, nil
	}
	// Go-only defensive guard: the TS union forbids kind 'decision' without a
	// decision; a misbehaving port fails closed here.
	if result.Decision == nil {
		return JevOutcome{
			Kind:      JevOutcomeAbstain,
			Reason:    schemas.AbstainReasonSchemaViolation,
			Detail:    "decision-engine port returned kind 'decision' without a decision — fail closed (answer contract violation)",
			Telemetry: result.Telemetry,
		}, nil
	}

	// 3. Single answer-contract validation gate → operational schema_violation.
	if contractErr := AssertAnswerContract(input.Compiled.Questions, result.Decision.Answers); contractErr != nil {
		var typed *AnswerContractError
		if errors.As(contractErr, &typed) {
			return JevOutcome{
				Kind:      JevOutcomeAbstain,
				Reason:    schemas.AbstainReasonSchemaViolation,
				Detail:    typed.Error(),
				Telemetry: result.Telemetry,
			}, nil
		}
		return JevOutcome{}, contractErr
	}

	// 4. Deterministic Stage-1 routing (threshold-free; engine owns thresholds).
	answers := result.Decision.Answers
	var answerImpact *schemas.DocumentationImpact
	if raw, ok := answers[DecidingAnswerKey]; ok {
		var value string
		if err := json.Unmarshal(raw.Value, &value); err == nil && schemas.IsValidDocumentationImpact(value) {
			impact := schemas.DocumentationImpact(value)
			answerImpact = &impact
		}
	}
	routing, err := ResolveStage1Verdict(JevRoutingSignals{
		ChangeKind:     input.AnalysisContext.ChangeKind,
		AnalysisImpact: input.AnalysisContext.DocumentationImpact,
		AnswerImpact:   answerImpact,
		Risk:           input.AnalysisContext.Risk,
		Abstained:      result.Decision.Abstain,
	})
	if err != nil {
		return JevOutcome{}, err
	}

	// 5. Application-owned action + fail-closed final DECISION_SCHEMA parse.
	rebuilt := *result.Decision
	rebuilt.Action = schemas.Action(routing.Verdict)
	if issues := rebuilt.Validate(); len(issues) > 0 {
		parts := make([]string, len(issues))
		for i, issue := range issues {
			path := "(root)"
			if len(issue.Path) > 0 {
				path = strings.Join(issue.Path, ".")
			}
			parts[i] = fmt.Sprintf("%s: %s", path, issue.Message)
		}
		return JevOutcome{
			Kind:      JevOutcomeAbstain,
			Reason:    schemas.AbstainReasonSchemaViolation,
			Detail:    "rebuilt decision failed DECISION_SCHEMA parse — fail closed on drift: " + strings.Join(parts, "; "),
			Telemetry: result.Telemetry,
		}, nil
	}
	return JevOutcome{
		Kind:           JevOutcomeDecision,
		Decision:       &rebuilt,
		Routing:        &routing,
		ProposedAction: result.Decision.Action,
		Telemetry:      result.Telemetry,
	}, nil
}
