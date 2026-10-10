// Ported from packages/policy/src/schema.ts — the Jev policy document schema
// `docsync.jev-policy/v1` (P2-T15; ARCH §8.3, §8.3.1, §8.4; D11; OD-R40).
//
// A policy document is the per-policy-version CONTRACT for the decision-engine
// role: typed questions, closed choice sets, and routing/calibration
// thresholds. D11: every threshold is per-policy-version calibrated DATA — the
// confidence × risk matrix shape is fixed code and is NOT part of this schema;
// the policy carries only the calibration the fixed matrix consumes. Models
// propose policies; humans promote them (§8.5).
//
// OD-R40: `docsync.jev-policy/v1` is the Jev policy DOCUMENT id, deliberately
// distinct from the registered `docsync.policy/v1` artifact in internal/schemas
// (OD-R04), which is the `policy.yml` live POINTER into `policy-history/`.
//
// Port shape: zod's strictObject/min/max/enum/literal checks are reproduced by
// parse-time validators that emit schemas.Issue values with zod's exact message
// text and dotted paths; the superRefine cross-field checks run only when every
// field-level check passes (zod refinement semantics). Unknown keys are
// rejected (OD-R01). Untrusted input in — this parser never panics.
package policy

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// PolicySchemaID is the wire id of the Jev policy DOCUMENT (OD-R40). The
// internal/schemas-registered docsync.policy/v1 id remains the policy.yml
// live-pointer artifact (OD-R04).
const PolicySchemaID = "docsync.jev-policy/v1"

// QuestionTypeValues lists the question types in canonical order
// (QUESTION_TYPE_VALUES).
var QuestionTypeValues = []QuestionType{
	QuestionTypeBoolean, QuestionTypeChoice, QuestionTypeScore,
}

// QuestionType is one typed question definition kind (QuestionType).
type QuestionType string

// The question types.
const (
	// QuestionTypeBoolean — answer value is true/false.
	QuestionTypeBoolean QuestionType = "boolean"
	// QuestionTypeChoice — answer value is one of choices.
	QuestionTypeChoice QuestionType = "choice"
	// QuestionTypeScore — answer value is a number in [scoring.min, scoring.max].
	QuestionTypeScore QuestionType = "score"
)

// QuestionScoring is the score range of a `score` question
// (QUESTION_SCHEMA.scoring) — both bounds are finite numbers.
type QuestionScoring struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// Question is one typed question definition — the CONTRACT for one answer the
// model returns (Question).
//
//   - type 'boolean' — answer value is true/false.
//   - type 'choice' — answer value is one of Choices (required iff type is
//     choice; ≥ 2 unique strings, each a member of the closed policy choice
//     sets).
//   - type 'score' — answer value is a number in [Scoring.Min, Scoring.Max]
//     (scoring is required iff type is score).
//
// Prompt is the question text shown to the model; it is data, never prompt
// prose smuggled into routing (ARCH §8.3).
type Question struct {
	// Key is the answer key in `docsync.decision/v1.answers`; unique within
	// the policy (root refinement).
	Key string `json:"key"`
	// Type is the question type.
	Type QuestionType `json:"type"`
	// Prompt is the question text — at least one character.
	Prompt string `json:"prompt"`
	// Choices is the closed choice set — required iff Type is 'choice',
	// forbidden otherwise.
	Choices []string `json:"choices,omitempty"`
	// Scoring is the score range — required iff Type is 'score', forbidden
	// otherwise.
	Scoring *QuestionScoring `json:"scoring,omitempty"`
}

// Routing carries the routing and calibration thresholds the fixed confidence ×
// risk matrix consumes (Routing; ARCH §5.2–§5.3; D11).
//
// Semantics, binding for the matrix wave:
//   - HighConfidence — minimum overall decision confidence for the matrix's
//     `auto` cell; `auto` requires confidence ≥ high_confidence. (Matrix SHAPE
//     is fixed code; high-risk rows never produce `auto` regardless of
//     confidence.)
//   - Review — lower threshold of the confidence axis: confidence < review
//     falls into the low bucket. Ordering invariant: review ≤ high_confidence.
//   - CalibrationFloor (optional) — upper bound of the calibration no-man's
//     zone, defaulting to HighConfidence: a DECIDING answer's probability in
//     the open interval (review, calibration_floor) abstains with reason
//     `calibration_floor` (§5.2 example: documentation_impact probability 0.83
//     with auto floor 0.94 and review threshold 0.72 → abstain).
//   - DocumentationImpactAutoFloor (optional) — per-answer refinement of the
//     same rule for the documentation_impact answer only; when present it takes
//     precedence over the global band for documentation_impact. NOT capped at
//     high_confidence (only calibration_floor is).
type Routing struct {
	HighConfidence               float64  `json:"high_confidence"`
	Review                       float64  `json:"review"`
	CalibrationFloor             *float64 `json:"calibration_floor,omitempty"`
	DocumentationImpactAutoFloor *float64 `json:"documentation_impact_auto_floor,omitempty"`
}

// PolicyEvaluation is the per-version evaluation metadata (R4; §8.4) on the
// calibration split.
type PolicyEvaluation struct {
	// Split is the evaluation split the metrics were measured on.
	Split string `json:"split"`
	// Metrics are named metrics; `unsafe_auto_updates = 0` is the promotion
	// gate metric (§8.4).
	Metrics map[string]float64 `json:"metrics"`
}

// UnsafeAutoUpdates is the §8.4 promotion-gate acknowledgment. The gate struct
// can only ever record Enabled == false — the schema cannot represent a policy
// enabling unsafe auto updates.
type UnsafeAutoUpdates struct {
	Enabled bool `json:"enabled"`
}

// Policy is the Jev policy document — self-contained per policy version
// (ARCH §8.3, §8.4). Unknown keys are rejected (OD-R01, strict throughout).
// CreatedAt is an ISO-8601 string by shape only — clocks are injected by
// callers, never owned by the schema.
type Policy struct {
	Schema          string     `json:"schema"`
	PolicyVersion   int        `json:"policy_version"`
	CreatedBy       string     `json:"created_by"`
	Reason          string     `json:"reason"`
	CreatedAt       string     `json:"created_at"`
	Questions       []Question `json:"questions"`
	Routing         Routing    `json:"routing"`
	Universal       bool       `json:"universal"`
	Extends         *string    `json:"extends,omitempty"`
	TaxonomyVersion int        `json:"taxonomy_version"`
	// RiskClassOverrides are per-doc-pattern risk classes —
	// conservative-max INPUT data (D2/D11), consumed by the matrix. Values
	// are the closed schemas.RiskLevel vocabulary.
	RiskClassOverrides map[string]schemas.RiskLevel `json:"risk_class_overrides,omitempty"`
	// Evaluation is the per-version evaluation metadata on the calibration
	// split.
	Evaluation *PolicyEvaluation `json:"evaluation,omitempty"`
	// UnsafeAutoUpdates is the promotion-gate acknowledgment (§8.4).
	UnsafeAutoUpdates *UnsafeAutoUpdates `json:"unsafe_auto_updates,omitempty"`
}

// PolicyExample mirrors POLICY_EXAMPLE: the canonical universal question set
// with the calibrated §5.2 thresholds (high_confidence 0.94 / review 0.72) and
// the calibration no-man's zone floors. Diff-stable (no clocks, no
// randomness).
var PolicyExample = newPolicyExample()

// newPolicyExample builds the example; the optional floor fields need
// addressable locals (self-contained — no package-level pointer helper).
func newPolicyExample() Policy {
	calibrationFloor, documentationImpactAutoFloor := 0.94, 0.94
	return Policy{
		Schema:        PolicySchemaID,
		PolicyVersion: 17,
		CreatedBy:     "claude-sonnet (policy-architect)",
		Reason: "Bootstrap universal policy v17: canonical universal questions with conservative calibrated routing " +
			"(high_confidence 0.94, review 0.72) and a calibration no-man\u2019s zone; abstain-safe defaults.",
		CreatedAt: "2026-01-15T00:00:00.000Z",
		Questions: []Question{
			{
				Key:    "public_behavior_changed",
				Type:   QuestionTypeBoolean,
				Prompt: "Did the change alter runtime behavior visible to users of this repository?",
			},
			{
				Key:    "api_contract_changed",
				Type:   QuestionTypeBoolean,
				Prompt: "Did the change alter the public contract (exported API surface)?",
			},
			{
				Key:     "affected_doc_count",
				Type:    QuestionTypeScore,
				Prompt:  "How many known documentation candidates does this change affect?",
				Scoring: &QuestionScoring{Min: 0, Max: 50},
			},
			{
				Key:     "documentation_impact",
				Type:    QuestionTypeChoice,
				Prompt:  "What is the documentation impact of this change?",
				Choices: choiceSetStrings(schemas.DocumentationImpactValues),
			},
		},
		Routing: Routing{
			HighConfidence:               0.94,
			Review:                       0.72,
			CalibrationFloor:             &calibrationFloor,
			DocumentationImpactAutoFloor: &documentationImpactAutoFloor,
		},
		Universal:       true,
		TaxonomyVersion: 1,
		Evaluation: &PolicyEvaluation{
			Split: "evaluation/universal-bootstrap-v17",
			Metrics: map[string]float64{
				"precision":           0.96,
				"recall":              0.93,
				"unsafe_auto_updates": 0,
			},
		},
		UnsafeAutoUpdates: &UnsafeAutoUpdates{Enabled: false},
	}
}

/* ------------------------------------------------------------- strict parse */

// policyFieldNames lists POLICY_SCHEMA's known keys (zod strictObject).
var policyFieldNames = []string{
	"schema", "policy_version", "created_by", "reason", "created_at",
	"questions", "routing", "universal", "extends", "taxonomy_version",
	"risk_class_overrides", "evaluation", "unsafe_auto_updates",
}

// questionFieldNames lists QUESTION_SCHEMA's known keys.
var questionFieldNames = []string{"key", "type", "prompt", "choices", "scoring"}

// routingFieldNames lists ROUTING_SCHEMA's known keys.
var routingFieldNames = []string{"high_confidence", "review", "calibration_floor", "documentation_impact_auto_floor"}

// ParsePolicy strictly validates an untrusted policy value (a decoded
// YAML/JSON object) against POLICY_SCHEMA (`docsync.jev-policy/v1`) — the Go
// form of POLICY_SCHEMA.safeParse. All-or-nothing: on any issue the returned
// error is a *schemas.ValidationError carrying every issue with zod's message
// text and dotted path; a nil error means the policy is fully valid and the
// returned *Policy is the parsed, schema-valid document.
func ParsePolicy(input any) (*Policy, error) {
	policy, issues := parsePolicy(input)
	if len(issues) > 0 {
		return nil, schemas.NewValidationError(issues...)
	}
	return policy, nil
}

// parsePolicy is the internal issue-collecting form of ParsePolicy (the
// compiler classifies every issue, so it needs the list, not just the error).
func parsePolicy(input any) (*Policy, []schemas.Issue) {
	var issues []schemas.Issue
	policy := parsePolicyObject(input, &issues)
	if len(issues) > 0 {
		return nil, issues
	}
	return policy, nil
}

func parsePolicyObject(input any, issues *[]schemas.Issue) *Policy {
	policy := &Policy{}
	obj, ok := asStringMap(input)
	if !ok {
		parseIssue(issues, nil, "Expected object, received "+zodReceivedKind(input))
		return policy
	}
	collectUnrecognizedKeys(obj, policyFieldNames, nil, issues)

	policy.Schema = literalStringField(obj, "schema", PolicySchemaID, nil, issues)
	policy.PolicyVersion = positiveIntField(obj, "policy_version", pathChild(nil, "policy_version"), issues)
	policy.CreatedBy = requiredStringField(obj, "created_by", pathChild(nil, "created_by"), issues)
	policy.Reason = requiredStringField(obj, "reason", pathChild(nil, "reason"), issues)
	policy.CreatedAt = requiredStringField(obj, "created_at", pathChild(nil, "created_at"), issues)
	policy.Questions = questionsField(obj, issues)
	policy.Routing = routingValueField(obj, pathChild(nil, "routing"), issues)
	policy.Universal = booleanField(obj, "universal", pathChild(nil, "universal"), issues)
	policy.Extends = optionalStringField(obj, "extends", pathChild(nil, "extends"), issues)
	policy.TaxonomyVersion = positiveIntField(obj, "taxonomy_version", pathChild(nil, "taxonomy_version"), issues)
	policy.RiskClassOverrides = riskClassOverridesField(obj, issues)
	policy.Evaluation = evaluationField(obj, issues)
	policy.UnsafeAutoUpdates = unsafeAutoUpdatesField(obj, issues)

	// POLICY_SCHEMA.superRefine: duplicate question keys. zod runs an
	// object's refinement even when field-level checks FAILED (the object
	// parse is DIRTY, not aborted — only a non-object input aborts), which
	// the compiler's "collects every diagnostic for one broken policy" test
	// pins: a routing threshold violation AND the root duplicate-key
	// diagnostic are both reported for one policy. The refinement therefore
	// runs over the partially-assembled value here.
	refineQuestionKeys(policy, issues)
	return policy
}

// refineQuestionKeys ports the POLICY_SCHEMA superRefine: one issue per
// duplicate occurrence (a third occurrence yields a second issue).
func refineQuestionKeys(policy *Policy, issues *[]schemas.Issue) {
	seen := make(map[string]struct{}, len(policy.Questions))
	for _, question := range policy.Questions {
		if _, duplicate := seen[question.Key]; duplicate {
			parseIssue(issues, []string{"questions"},
				fmt.Sprintf("question keys must be unique — duplicate key '%s'", question.Key))
		}
		seen[question.Key] = struct{}{}
	}
}

func questionsField(obj map[string]any, issues *[]schemas.Issue) []Question {
	value, present := obj["questions"]
	if !present {
		parseIssue(issues, []string{"questions"}, "Required")
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		parseIssue(issues, []string{"questions"}, "Expected array, received "+zodReceivedKind(value))
		return nil
	}
	if len(items) < 1 {
		parseIssue(issues, []string{"questions"}, "Array must contain at least 1 element(s)")
		return nil
	}
	questions := make([]Question, 0, len(items))
	for index, item := range items {
		path := pathChild([]string{"questions"}, strconv.Itoa(index))
		question, valid := parseQuestion(item, path, issues)
		if valid {
			questions = append(questions, question)
		}
	}
	return questions
}

// parseQuestion validates one array element against QUESTION_SCHEMA. The
// returned bool is false when field-level issues were emitted for this element
// (the question-level superRefine is then skipped, mirroring zod).
func parseQuestion(value any, path []string, issues *[]schemas.Issue) (Question, bool) {
	question := Question{}
	obj, ok := asStringMap(value)
	if !ok {
		parseIssue(issues, path, "Expected object, received "+zodReceivedKind(value))
		return question, false
	}
	before := len(*issues)
	collectUnrecognizedKeys(obj, questionFieldNames, path, issues)

	question.Key = requiredStringField(obj, "key", pathChild(path, "key"), issues)
	question.Type = QuestionType(enumField(obj, "type", choiceSetStrings(QuestionTypeValues), pathChild(path, "type"), issues))
	question.Prompt = requiredStringField(obj, "prompt", pathChild(path, "prompt"), issues)

	if raw, present := obj["choices"]; present {
		choices, ok := parseChoices(raw, pathChild(path, "choices"), issues)
		if ok {
			question.Choices = choices
		}
	}
	if raw, present := obj["scoring"]; present {
		scoring, ok := parseScoring(raw, pathChild(path, "scoring"), issues)
		if ok {
			question.Scoring = scoring
		}
	}

	if len(*issues) == before {
		refineQuestion(&question, path, issues)
		return question, true
	}
	return question, false
}

// parseChoices applies z.array(z.string().min(1)).min(2).optional().
func parseChoices(value any, path []string, issues *[]schemas.Issue) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		parseIssue(issues, path, "Expected array, received "+zodReceivedKind(value))
		return nil, false
	}
	if len(items) < 2 {
		parseIssue(issues, path, "Array must contain at least 2 element(s)")
	}
	choices := make([]string, 0, len(items))
	for index, item := range items {
		choice, ok := item.(string)
		if !ok {
			parseIssue(issues, pathChild(path, strconv.Itoa(index)), "Expected string, received "+zodReceivedKind(item))
			continue
		}
		if len(choice) < 1 {
			parseIssue(issues, pathChild(path, strconv.Itoa(index)), "String must contain at least 1 character(s)")
			continue
		}
		choices = append(choices, choice)
	}
	return choices, true
}

// parseScoring applies the strict scoring object {min, max} (finite numbers).
func parseScoring(value any, path []string, issues *[]schemas.Issue) (*QuestionScoring, bool) {
	obj, ok := asStringMap(value)
	if !ok {
		parseIssue(issues, path, "Expected object, received "+zodReceivedKind(value))
		return nil, false
	}
	collectUnrecognizedKeys(obj, []string{"min", "max"}, path, issues)
	scoring := &QuestionScoring{}
	scoring.Min = finiteNumberField(obj, "min", pathChild(path, "min"), issues)
	scoring.Max = finiteNumberField(obj, "max", pathChild(path, "max"), issues)
	return scoring, true
}

// refineQuestion ports the QUESTION_SCHEMA superRefine (exact TS messages).
func refineQuestion(question *Question, path []string, issues *[]schemas.Issue) {
	if question.Type == QuestionTypeChoice && question.Choices == nil {
		parseIssue(issues, pathChild(path, "choices"),
			"a question with type 'choice' must declare choices (required iff type 'choice')")
	}
	if question.Type != QuestionTypeChoice && question.Choices != nil {
		parseIssue(issues, pathChild(path, "choices"),
			fmt.Sprintf("choices are only allowed on questions with type 'choice' (got '%s')", question.Type))
	}
	if question.Type == QuestionTypeScore && question.Scoring == nil {
		parseIssue(issues, pathChild(path, "scoring"),
			"a question with type 'score' must declare scoring {min, max} (required iff type 'score')")
	}
	if question.Type != QuestionTypeScore && question.Scoring != nil {
		parseIssue(issues, pathChild(path, "scoring"),
			fmt.Sprintf("scoring is only allowed on questions with type 'score' (got '%s')", question.Type))
	}
	if question.Choices != nil {
		if !hasNoDuplicates(question.Choices) {
			parseIssue(issues, pathChild(path, "choices"),
				"question choices must be unique — closed sets contain no duplicates")
		}
		for _, choice := range question.Choices {
			if !IsClosedPolicyChoice(choice) {
				parseIssue(issues, pathChild(path, "choices"),
					fmt.Sprintf("question choice '%s' is not a member of the closed policy choice sets (choice-sets.ts)", choice))
			}
		}
	}
	if question.Scoring != nil && question.Scoring.Max < question.Scoring.Min {
		parseIssue(issues, pathChild(path, "scoring", "max"), "scoring.max must be ≥ scoring.min")
	}
}

func routingValueField(obj map[string]any, path []string, issues *[]schemas.Issue) Routing {
	value, present := obj["routing"]
	if !present {
		parseIssue(issues, path, "Required")
		return Routing{}
	}
	return parseRouting(value, path, issues)
}

// parseRouting validates ROUTING_SCHEMA (strict + unit-interval bounds +
// superRefine). The refine runs only when the object's own field checks pass.
func parseRouting(value any, path []string, issues *[]schemas.Issue) Routing {
	routing := Routing{}
	obj, ok := asStringMap(value)
	if !ok {
		parseIssue(issues, path, "Expected object, received "+zodReceivedKind(value))
		return routing
	}
	before := len(*issues)
	collectUnrecognizedKeys(obj, routingFieldNames, path, issues)

	routing.HighConfidence = unitIntervalField(obj, "high_confidence", pathChild(path, "high_confidence"), issues)
	routing.Review = unitIntervalField(obj, "review", pathChild(path, "review"), issues)
	routing.CalibrationFloor = optionalUnitIntervalField(obj, "calibration_floor", pathChild(path, "calibration_floor"), issues)
	routing.DocumentationImpactAutoFloor = optionalUnitIntervalField(obj, "documentation_impact_auto_floor", pathChild(path, "documentation_impact_auto_floor"), issues)

	if len(*issues) == before {
		refineRouting(&routing, path, issues)
	}
	return routing
}

// refineRouting ports the ROUTING_SCHEMA superRefine (exact TS messages).
func refineRouting(routing *Routing, path []string, issues *[]schemas.Issue) {
	if routing.Review > routing.HighConfidence {
		parseIssue(issues, pathChild(path, "review"),
			fmt.Sprintf("routing thresholds must satisfy review ≤ high_confidence (got review %s > high_confidence %s)",
				renderNumber(routing.Review), renderNumber(routing.HighConfidence)))
	}
	if routing.CalibrationFloor != nil {
		floor := *routing.CalibrationFloor
		if floor < routing.Review || floor > routing.HighConfidence {
			parseIssue(issues, pathChild(path, "calibration_floor"),
				fmt.Sprintf("calibration_floor must lie within [review, high_confidence] (got %s outside [%s, %s])",
					renderNumber(floor), renderNumber(routing.Review), renderNumber(routing.HighConfidence)))
		}
	}
	if routing.DocumentationImpactAutoFloor != nil {
		floor := *routing.DocumentationImpactAutoFloor
		if floor < routing.Review {
			parseIssue(issues, pathChild(path, "documentation_impact_auto_floor"),
				fmt.Sprintf("documentation_impact_auto_floor must be ≥ review (got %s < %s)",
					renderNumber(floor), renderNumber(routing.Review)))
		}
	}
}

func riskClassOverridesField(obj map[string]any, issues *[]schemas.Issue) map[string]schemas.RiskLevel {
	value, present := obj["risk_class_overrides"]
	if !present {
		return nil
	}
	overrides, ok := asStringMap(value)
	if !ok {
		parseIssue(issues, []string{"risk_class_overrides"}, "Expected object, received "+zodReceivedKind(value))
		return nil
	}
	riskLevels := choiceSetStrings(schemas.RiskLevelValues)
	out := make(map[string]schemas.RiskLevel, len(overrides))
	for _, pattern := range sortedFieldNames(overrides) {
		if len(pattern) < 1 {
			parseIssue(issues, []string{"risk_class_overrides", pattern}, "String must contain at least 1 character(s)")
		}
		path := []string{"risk_class_overrides", pattern}
		risk, ok := overrides[pattern].(string)
		if !ok {
			parseIssue(issues, path, "Expected "+strings.Join(quoteEach(riskLevels), " | ")+", received "+zodReceivedKind(overrides[pattern]))
			continue
		}
		if issue := schemas.CheckEnum(risk, riskLevels, path); issue != nil {
			*issues = append(*issues, *issue)
			continue
		}
		out[pattern] = schemas.RiskLevel(risk)
	}
	return out
}

func evaluationField(obj map[string]any, issues *[]schemas.Issue) *PolicyEvaluation {
	value, present := obj["evaluation"]
	if !present {
		return nil
	}
	eval, ok := asStringMap(value)
	if !ok {
		parseIssue(issues, []string{"evaluation"}, "Expected object, received "+zodReceivedKind(value))
		return nil
	}
	collectUnrecognizedKeys(eval, []string{"split", "metrics"}, []string{"evaluation"}, issues)
	out := &PolicyEvaluation{}
	out.Split = requiredStringField(eval, "split", []string{"evaluation", "split"}, issues)
	if metricsRaw, present := eval["metrics"]; present {
		metrics, ok := asStringMap(metricsRaw)
		if !ok {
			parseIssue(issues, []string{"evaluation", "metrics"}, "Expected object, received "+zodReceivedKind(metricsRaw))
		} else {
			out.Metrics = make(map[string]float64, len(metrics))
			for _, metric := range sortedFieldNames(metrics) {
				path := []string{"evaluation", "metrics", metric}
				if len(metric) < 1 {
					parseIssue(issues, path, "String must contain at least 1 character(s)")
				}
				number, ok := numericValue(metrics[metric])
				if !ok || math.IsNaN(number) {
					parseIssue(issues, path, "Expected number, received "+receivedName(metrics[metric]))
					continue
				}
				if math.IsInf(number, 0) {
					parseIssue(issues, path, "Number must be finite")
					continue
				}
				out.Metrics[metric] = number
			}
		}
	} else {
		parseIssue(issues, []string{"evaluation", "metrics"}, "Required")
	}
	return out
}

func unsafeAutoUpdatesField(obj map[string]any, issues *[]schemas.Issue) *UnsafeAutoUpdates {
	value, present := obj["unsafe_auto_updates"]
	if !present {
		return nil
	}
	gate, ok := asStringMap(value)
	if !ok {
		parseIssue(issues, []string{"unsafe_auto_updates"}, "Expected object, received "+zodReceivedKind(value))
		return nil
	}
	collectUnrecognizedKeys(gate, []string{"enabled"}, []string{"unsafe_auto_updates"}, issues)
	enabled, present := gate["enabled"]
	if !present {
		parseIssue(issues, []string{"unsafe_auto_updates", "enabled"}, "Required")
		return nil
	}
	flag, ok := enabled.(bool)
	if !ok {
		parseIssue(issues, []string{"unsafe_auto_updates", "enabled"}, "Expected boolean, received "+zodReceivedKind(enabled))
		return nil
	}
	if flag {
		// z.literal(false) — JSON.stringify(false) renders unquoted.
		parseIssue(issues, []string{"unsafe_auto_updates", "enabled"}, "Invalid literal value, expected false")
		return nil
	}
	return &UnsafeAutoUpdates{Enabled: false}
}

/* ------------------------------------------------------- field-level helpers */

// parseIssue appends a path-carrying issue.
func parseIssue(issues *[]schemas.Issue, path []string, message string) {
	*issues = append(*issues, schemas.Issue{Path: path, Message: message})
}

// collectUnrecognizedKeys mirrors z.strictObject's unrecognized_keys issue.
func collectUnrecognizedKeys(obj map[string]any, allowed []string, path []string, issues *[]schemas.Issue) {
	unknown := make([]string, 0, len(obj))
	for key := range obj {
		if !keyIsAllowed(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return
	}
	sort.Strings(unknown)
	quoted := make([]string, 0, len(unknown))
	for _, key := range unknown {
		quoted = append(quoted, "'"+key+"'")
	}
	parseIssue(issues, path, "Unrecognized key(s) in object: "+strings.Join(quoted, ", "))
}

func keyIsAllowed(allowed []string, key string) bool {
	for _, candidate := range allowed {
		if candidate == key {
			return true
		}
	}
	return false
}

// literalStringField applies z.literal(id).
func literalStringField(obj map[string]any, key, literal string, path []string, issues *[]schemas.Issue) string {
	value, present := obj[key]
	if !present {
		parseIssue(issues, path, "Required")
		return ""
	}
	text, ok := value.(string)
	if !ok {
		parseIssue(issues, path, "Expected string, received "+zodReceivedKind(value))
		return ""
	}
	if text != literal {
		parseIssue(issues, path, `Invalid literal value, expected "`+literal+`"`)
	}
	return text
}

// requiredStringField applies z.string().min(1).
func requiredStringField(obj map[string]any, key string, path []string, issues *[]schemas.Issue) string {
	value, present := obj[key]
	if !present {
		parseIssue(issues, path, "Required")
		return ""
	}
	text, ok := value.(string)
	if !ok {
		parseIssue(issues, path, "Expected string, received "+zodReceivedKind(value))
		return ""
	}
	if len(text) < 1 {
		parseIssue(issues, path, "String must contain at least 1 character(s)")
		return ""
	}
	return text
}

// optionalStringField applies z.string().min(1).optional().
func optionalStringField(obj map[string]any, key string, path []string, issues *[]schemas.Issue) *string {
	value, present := obj[key]
	if !present {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		parseIssue(issues, path, "Expected string, received "+zodReceivedKind(value))
		return nil
	}
	if len(text) < 1 {
		parseIssue(issues, path, "String must contain at least 1 character(s)")
		return nil
	}
	return &text
}

// positiveIntField applies z.number().int().positive().
func positiveIntField(obj map[string]any, key string, path []string, issues *[]schemas.Issue) int {
	value, present := obj[key]
	if !present {
		parseIssue(issues, path, "Required")
		return 0
	}
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) {
		parseIssue(issues, path, "Expected number, received "+receivedName(value))
		return 0
	}
	if number != math.Trunc(number) {
		parseIssue(issues, path, "Expected int, received "+renderNumber(number))
		return 0
	}
	if number <= 0 {
		parseIssue(issues, path, "Number must be greater than 0")
		return 0
	}
	return int(number)
}

// unitIntervalField applies z.number().min(0).max(1).
func unitIntervalField(obj map[string]any, key string, path []string, issues *[]schemas.Issue) float64 {
	value, present := obj[key]
	if !present {
		parseIssue(issues, path, "Required")
		return 0
	}
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) {
		parseIssue(issues, path, "Expected number, received "+receivedName(value))
		return 0
	}
	return checkUnitInterval(number, path, issues)
}

// optionalUnitIntervalField applies z.number().min(0).max(1).optional().
func optionalUnitIntervalField(obj map[string]any, key string, path []string, issues *[]schemas.Issue) *float64 {
	value, present := obj[key]
	if !present {
		return nil
	}
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) {
		parseIssue(issues, path, "Expected number, received "+receivedName(value))
		return nil
	}
	checked := checkUnitInterval(number, path, issues)
	return &checked
}

func checkUnitInterval(number float64, path []string, issues *[]schemas.Issue) float64 {
	if number < 0 {
		parseIssue(issues, path, "Number must be greater than or equal to 0")
		return 0
	}
	if number > 1 {
		parseIssue(issues, path, "Number must be less than or equal to 1")
		return 0
	}
	return number
}

// finiteNumberField applies z.number().finite().
func finiteNumberField(obj map[string]any, key string, path []string, issues *[]schemas.Issue) float64 {
	value, present := obj[key]
	if !present {
		parseIssue(issues, path, "Required")
		return 0
	}
	number, ok := numericValue(value)
	if !ok || math.IsNaN(number) {
		parseIssue(issues, path, "Expected number, received "+receivedName(value))
		return 0
	}
	if math.IsInf(number, 0) {
		parseIssue(issues, path, "Number must be finite")
		return 0
	}
	return number
}

// enumField applies z.enum([...]) — absent keys are "Required".
func enumField(obj map[string]any, key string, allowed []string, path []string, issues *[]schemas.Issue) string {
	value, present := obj[key]
	if !present {
		parseIssue(issues, path, "Required")
		return ""
	}
	text, ok := value.(string)
	if !ok {
		parseIssue(issues, path, "Expected "+strings.Join(quoteEach(allowed), " | ")+", received "+zodReceivedKind(value))
		return ""
	}
	if issue := schemas.CheckEnum(text, allowed, path); issue != nil {
		*issues = append(*issues, *issue)
	}
	return text
}

// booleanField applies z.boolean().
func booleanField(obj map[string]any, key string, path []string, issues *[]schemas.Issue) bool {
	value, present := obj[key]
	if !present {
		parseIssue(issues, path, "Required")
		return false
	}
	flag, ok := value.(bool)
	if !ok {
		parseIssue(issues, path, "Expected boolean, received "+zodReceivedKind(value))
		return false
	}
	return flag
}

/* ------------------------------------------------------- generic value utils */

// asStringMap accepts both map shapes decoders produce (JSON/yaml.v3 string
// keys; yaml.v3 non-string keys), normalizing to map[string]any.
func asStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			text, ok := key.(string)
			if !ok {
				text = fmt.Sprint(key)
			}
			out[text] = item
		}
		return out, true
	}
	return nil, false
}

// numericValue extracts the float behind any decoded numeric form (JSON
// numbers decode as json.Number, YAML numbers as int or float64).
func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return number, true
	}
	return 0, false
}

// zodReceivedKind renders a decoded value the way zod's `received` token does.
func zodReceivedKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, float32, int, int64, json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any, map[any]any:
		return "object"
	default:
		return "unknown"
	}
}

// receivedName renders the received token for a numeric field: z.number()
// rejects NaN as a TYPE failure ("Expected number, received nan"), while ±Inf
// passes the type check and fails the bound checks.
func receivedName(value any) string {
	switch number := value.(type) {
	case float64:
		if math.IsNaN(number) {
			return "nan"
		}
	case float32:
		if math.IsNaN(float64(number)) {
			return "nan"
		}
	}
	return zodReceivedKind(value)
}

// renderNumber formats a number the way JS template interpolation does.
func renderNumber(number float64) string {
	switch {
	case math.IsNaN(number):
		return "NaN"
	case math.IsInf(number, 1):
		return "Infinity"
	case math.IsInf(number, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(number, 'g', -1, 64)
}

// pathChild clones path plus the extra segments (never appends in place —
// issue paths outlive the loop that built them).
func pathChild(path []string, segments ...string) []string {
	out := make([]string, 0, len(path)+len(segments))
	out = append(out, path...)
	return append(out, segments...)
}

// sortedFieldNames returns the map's keys in deterministic order (Go map
// iteration is randomized; the TS Object.entries order is insertion order —
// sorting keeps diagnostics deterministic).
func sortedFieldNames(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func hasNoDuplicates(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func quoteEach(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, "'"+value+"'")
	}
	return out
}
