// Ported from packages/policy/src/compiler.ts — the deterministic policy
// compiler (P2-T16; ARCH §8.2).
//
// Validates a Jev policy (schema conformance, question types, threshold sanity,
// choice-set closure, trigger-taxonomy mapping) and compiles it into the typed
// Jev workflow the decision-engine runtime consumes. Failing policies are
// returned to the architect with machine-readable diagnostics — NO partial
// policies ever reach production: compilation is all-or-nothing (OK=false
// carries diagnostics and nothing else; OK=true carries a fully validated
// CompiledPolicyWorkflow and nothing else).
//
// Determinism contract: the compiler is pure (no clocks, no randomness, no
// I/O); the same input always yields the same result, and policyHash is a
// sha256 over the CANONICAL JSON serialization of the parsed policy — that
// serialization is delegated to docutil.CanonicalJSON (and the digest to
// docutil.ContentHash), so Go-produced hashes are byte-identical to the
// TypeScript implementation's.
//
// Trigger-taxonomy seam (R17; V-P2-2 groundwork): the v1 policy schema declares
// NO trigger field, so a policy carries no in-policy trigger vocabulary to
// resolve — the manifest's user-facing `triggers:`/`ignore:` vocabulary is
// resolved by core's versioned mapping layer (mapTriggers). This module keeps
// the compiler-side seam alive: a provided taxonomy document is validated
// against `docsync.trigger-map/v1` (internal/schemas' TriggerMap), a
// policy/taxonomy version mismatch is a named diagnostic, and
// ValidateTriggerVocabulary produces the same version-naming diagnostics for a
// policy vocabulary — ready for the policy version that grows a trigger field.
// Every taxonomy/trigger diagnostic names the EXPECTED schema version verbatim.
package policy

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DecisionSchemaVersion is the engine decision-schema version the compiled
// workflow's answers target (ARCH §5.4) — re-exported from the single schema
// source of truth (OD-2: internal/schemas owns the literal).
const DecisionSchemaVersion = schemas.DecisionSchemaID

// TriggerTaxonomySchemaID is the registry id of the versioned trigger-taxonomy
// mapping table (R17; mirrors core).
const TriggerTaxonomySchemaID = "docsync.trigger-map"

// triggerTaxonomyVersion mirrors the universal trigger-taxonomy version
// (TRIGGER_TAXONOMY_VERSION). The schemas package keeps it unexported; the
// trigger-map schema itself enforces the literal (schemas.TriggerMap.Validate),
// this constant is only the v1 default for the vocabulary seam below.
const triggerTaxonomyVersion = 1

// TriggerMapSchemaRef renders the fully versioned trigger-taxonomy reference,
// e.g. `docsync.trigger-map/v1` — named in every R17 error
// (triggerMapSchemaRef).
func TriggerMapSchemaRef(taxonomyVersion int) string {
	return TriggerTaxonomySchemaID + "/v" + strconv.Itoa(taxonomyVersion)
}

// PolicyDiagnosticKind names one machine-readable diagnostic kind. It is a
// string ALIAS (not a defined type) because every Go call site concatenates
// and compares kinds as plain strings (the architect runner formats
// `kind: message`; the bootstrap invariant-breach error does the same).
type PolicyDiagnosticKind = string

// POLICY_DIAGNOSTIC_KIND_VALUES — ONE taxonomy for the whole policy engine:
// the compiler kinds (this module) and the policy-history kinds
// (versioning.go) share the PolicyDiagnostic shape so the architect-feedback
// loop consumes a single machine-readable contract.
var PolicyDiagnosticKindValues = []PolicyDiagnosticKind{
	// compiler kinds
	PolicyDiagnosticKindSchemaViolation,
	PolicyDiagnosticKindQuestionTypeViolation,
	PolicyDiagnosticKindChoiceSetViolation,
	PolicyDiagnosticKindDuplicateQuestionKey,
	PolicyDiagnosticKindRoutingThresholdViolation,
	PolicyDiagnosticKindDocumentationImpactMissing,
	PolicyDiagnosticKindUniversalSetViolation,
	PolicyDiagnosticKindRiskClassOverrideViolation,
	PolicyDiagnosticKindAdditiveRefinesUniversal,
	PolicyDiagnosticKindTaxonomyInvalid,
	PolicyDiagnosticKindTaxonomyVersionMismatch,
	PolicyDiagnosticKindUnknownTrigger,
	// policy-history kinds (versioning.go)
	PolicyDiagnosticKindRecordInvalid,
	PolicyDiagnosticKindVersionOrderViolation,
	PolicyDiagnosticKindDuplicateVersion,
	PolicyDiagnosticKindPreviousPointerViolation,
	PolicyDiagnosticKindVersionNotFound,
}

// The diagnostic kinds.
const (
	PolicyDiagnosticKindSchemaViolation            PolicyDiagnosticKind = "schema_violation"
	PolicyDiagnosticKindQuestionTypeViolation      PolicyDiagnosticKind = "question_type_violation"
	PolicyDiagnosticKindChoiceSetViolation         PolicyDiagnosticKind = "choice_set_violation"
	PolicyDiagnosticKindDuplicateQuestionKey       PolicyDiagnosticKind = "duplicate_question_key"
	PolicyDiagnosticKindRoutingThresholdViolation  PolicyDiagnosticKind = "routing_threshold_violation"
	PolicyDiagnosticKindDocumentationImpactMissing PolicyDiagnosticKind = "documentation_impact_missing"
	PolicyDiagnosticKindUniversalSetViolation      PolicyDiagnosticKind = "universal_set_violation"
	PolicyDiagnosticKindRiskClassOverrideViolation PolicyDiagnosticKind = "risk_class_override_violation"
	PolicyDiagnosticKindAdditiveRefinesUniversal   PolicyDiagnosticKind = "additive_refines_universal"
	PolicyDiagnosticKindTaxonomyInvalid            PolicyDiagnosticKind = "taxonomy_invalid"
	PolicyDiagnosticKindTaxonomyVersionMismatch    PolicyDiagnosticKind = "taxonomy_version_mismatch"
	PolicyDiagnosticKindUnknownTrigger             PolicyDiagnosticKind = "unknown_trigger"
	PolicyDiagnosticKindRecordInvalid              PolicyDiagnosticKind = "record_invalid"
	PolicyDiagnosticKindVersionOrderViolation      PolicyDiagnosticKind = "version_order_violation"
	PolicyDiagnosticKindDuplicateVersion           PolicyDiagnosticKind = "duplicate_version"
	PolicyDiagnosticKindPreviousPointerViolation   PolicyDiagnosticKind = "previous_pointer_violation"
	PolicyDiagnosticKindVersionNotFound            PolicyDiagnosticKind = "version_not_found"
)

// PolicyDiagnostic is one machine-readable compiler/history diagnostic.
type PolicyDiagnostic struct {
	// Kind is the machine-readable diagnostic kind.
	Kind PolicyDiagnosticKind `json:"kind"`
	// Path is the dotted path into the offending input, e.g.
	// `routing.high_confidence`, `questions.0.choices`,
	// `additivePolicies.0.questions.1.key`; `(root)` at top level. Array
	// indices are numeric path segments.
	Path string `json:"path"`
	// Message is the human-readable, stable diagnostic text.
	Message string `json:"message"`
	// ExpectedSchemaVersion is the versioned schema reference the error
	// expected (R17 — taxonomy/trigger diagnostics only). Empty otherwise.
	ExpectedSchemaVersion string `json:"expectedSchemaVersion,omitempty"`
}

// TriggerTaxonomyMap is the permissive caller-side shape of the versioned
// trigger-taxonomy mapping document. Untrusted input: validated at runtime
// against `docsync.trigger-map/v1` (internal/schemas' TriggerMap — literal
// taxonomy version, enum-constrained mapping keys, strict object).
type TriggerTaxonomyMap struct {
	TaxonomyVersion int               `json:"taxonomyVersion"`
	Mappings        map[string]string `json:"mappings"`
}

// CompiledPolicyWorkflow is the typed seam the Jev runtime consumes — produced
// only on successful compilation, never partially.
type CompiledPolicyWorkflow struct {
	// Policy is the parsed, schema-valid policy — compiled data, never the raw
	// input.
	Policy Policy `json:"policy"`
	// Questions are the composed questions: universal order preserved,
	// additive appended (or refinement-blocked) per §8.3.1.
	Questions []Question `json:"questions"`
	// Routing carries the routing/calibration thresholds the fixed
	// confidence × risk matrix consumes (D11 data).
	Routing Routing `json:"routing"`
	// PolicyHash is `sha256:<hex>` over the canonical JSON of the parsed
	// policy; feeds decision trace `policy_hash`.
	PolicyHash string `json:"policyHash"`
	// DecisionSchemaVersion is the engine decision-schema version this
	// workflow's answers target.
	DecisionSchemaVersion string `json:"decisionSchemaVersion"`
	// TaxonomyVersion is the effective trigger-taxonomy version (the provided
	// map's version, else the policy's declared version).
	TaxonomyVersion int `json:"taxonomyVersion"`
}

// CompilePolicyResult is the TS discriminated union rendered as a struct:
// Compiled is non-nil exactly when OK is true; Diagnostics is non-empty
// exactly when OK is false.
type CompilePolicyResult struct {
	OK          bool
	Compiled    *CompiledPolicyWorkflow
	Diagnostics []PolicyDiagnostic
}

// CompilePolicyOption configures one optional compile input. The TS original
// took a single input object { policy, taxonomy, additivePolicies }; Go's
// idiomatic translation is the policy value plus these functional options,
// which existing in-package call sites (the architect runner, the bootstrap
// compiler) use with the bare artifact map.
type CompilePolicyOption func(*compilePolicyOptions)

type compilePolicyOptions struct {
	taxonomy         *TriggerTaxonomyMap
	additivePolicies []any
}

// WithTriggerTaxonomy attaches the trigger-taxonomy mapping document
// (TS: input.taxonomy), validated against `docsync.trigger-map/v1` (R17).
func WithTriggerTaxonomy(taxonomy *TriggerTaxonomyMap) CompilePolicyOption {
	return func(o *compilePolicyOptions) { o.taxonomy = taxonomy }
}

// WithAdditivePolicies attaches additive repository policies
// (TS: input.additivePolicies; ARCH §8.3.1); each is validated like the base
// policy.
func WithAdditivePolicies(policies ...any) CompilePolicyOption {
	return func(o *compilePolicyOptions) { o.additivePolicies = append(o.additivePolicies, policies...) }
}

// TriggerDirection is which manifest vocabulary slot a trigger value came from
// (TriggerDirection).
type TriggerDirection string

// The directions.
const (
	TriggerDirectionTriggers TriggerDirection = "triggers"
	TriggerDirectionIgnore   TriggerDirection = "ignore"
)

// TriggerVocabularyValidation is the ValidateTriggerVocabulary result
// (TriggerVocabularyValidation).
type TriggerVocabularyValidation struct {
	// Mapped are the values the taxonomy accepted (v1: identity — the same
	// strings).
	Mapped []string `json:"mapped"`
	// UnknownValues are the values absent from the taxonomy, deduplicated, in
	// input order.
	UnknownValues []string `json:"unknownValues"`
	// Diagnostics carries one unknown_trigger diagnostic per unknown value,
	// naming the expected schema version (R17).
	Diagnostics []PolicyDiagnostic `json:"diagnostics"`
}

// ValidateTriggerVocabularyOptions configures ValidateTriggerVocabulary
// (the TS options object; nil fields take the v1 defaults).
type ValidateTriggerVocabularyOptions struct {
	Direction       TriggerDirection
	TaxonomyVersion *int
	KnownValues     []string
}

// ValidateTriggerVocabulary is the compiler-side seam for the manifest trigger
// vocabulary (R17; V-P2-2 groundwork) — mirrors core's
// mapTriggers/formatUnknownTriggers: v1 resolves the universal vocabulary by
// identity; unknown values are never guessed and never clamped (D6/D8) — they
// are reported with the expected taxonomy version and the versioned decision
// schema named verbatim. Never panics and never fails closed by coercion:
// unknown values are returned to the caller as data. (The v1 policy schema
// declares no trigger field, so CompilePolicy itself has no in-policy
// vocabulary to resolve — documented skip; this function is the seam for the
// policy version that grows a trigger field.)
func ValidateTriggerVocabulary(values []string, options ValidateTriggerVocabularyOptions) TriggerVocabularyValidation {
	taxonomyVersion := triggerTaxonomyVersion
	if options.TaxonomyVersion != nil {
		taxonomyVersion = *options.TaxonomyVersion
	}
	knownValues := universalTriggerValueStrings()
	if options.KnownValues != nil {
		knownValues = options.KnownValues
	}
	known := make(map[string]struct{}, len(knownValues))
	for _, value := range knownValues {
		known[value] = struct{}{}
	}

	result := TriggerVocabularyValidation{
		Mapped:        make([]string, 0, len(values)),
		UnknownValues: make([]string, 0, len(values)),
		Diagnostics:   make([]PolicyDiagnostic, 0, len(values)),
	}
	seenUnknown := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := known[value]; ok {
			result.Mapped = append(result.Mapped, value)
			continue
		}
		if _, duplicate := seenUnknown[value]; duplicate {
			continue
		}
		seenUnknown[value] = struct{}{}
		result.UnknownValues = append(result.UnknownValues, value)
		result.Diagnostics = append(result.Diagnostics, PolicyDiagnostic{
			Kind:                  PolicyDiagnosticKindUnknownTrigger,
			Path:                  string(options.Direction),
			ExpectedSchemaVersion: TriggerMapSchemaRef(taxonomyVersion),
			Message: fmt.Sprintf("unknown trigger value %s for %s; expected trigger-taxonomy version %d "+
				"(decision schema %s; engine decision schema %s); supported values: %s",
				strconv.Quote(value), options.Direction, taxonomyVersion,
				TriggerMapSchemaRef(taxonomyVersion), DecisionSchemaVersion,
				strings.Join(knownValues, ", ")),
		})
	}
	return result
}

// CompilePolicy compiles a Jev policy into the typed workflow (ARCH §8.2).
// All-or-nothing. Diagnostic-collection semantics, precisely scoped:
//
//   - A base-schema parse failure (step 1) SHORT-CIRCUITS: it returns the base
//     parse diagnostics alone and skips the additive, taxonomy and semantic
//     passes, whose inputs are not parseable policy data (additive diagnostics
//     are deliberately skipped there).
//   - A failed ADDITIVE parse contributes its own diagnostics and the
//     remaining passes still run over the base policy.
//   - The semantic checks COLLECT EVERY diagnostic — never fail-fast, because
//     the architect loop needs the full picture.
//
// Only on zero diagnostics is the compiled workflow produced.
func CompilePolicy(policy any, options ...CompilePolicyOption) CompilePolicyResult {
	settings := compilePolicyOptions{}
	for _, option := range options {
		option(&settings)
	}
	diagnostics := make([]PolicyDiagnostic, 0)

	// 1. Schema conformance first (schema issues → diagnostics with path
	//    precision).
	parsed, issues := parsePolicy(policy)
	if len(issues) > 0 {
		parseDiagnostics := make([]PolicyDiagnostic, 0, len(issues))
		for _, issue := range issues {
			kind, path, message := classifyIssue(issue)
			parseDiagnostics = append(parseDiagnostics, toDiagnostic(kind, path, message))
		}
		return CompilePolicyResult{OK: false, Diagnostics: parseDiagnostics}
	}

	// 2. Additive policies: each validated like the base policy (§8.3.1).
	parsedAdditives := make([]*Policy, 0, len(settings.additivePolicies))
	for index, additiveInput := range settings.additivePolicies {
		additive, additiveIssues := parsePolicy(additiveInput)
		if len(additiveIssues) > 0 {
			for _, issue := range additiveIssues {
				kind, path, message := classifyIssue(issue)
				diagnostics = append(diagnostics,
					withPathPrefix(fmt.Sprintf("additivePolicies.%d", index), toDiagnostic(kind, path, message)))
			}
			continue
		}
		parsedAdditives = append(parsedAdditives, additive)
	}

	// 3. Trigger-taxonomy document validation + policy/taxonomy version pin
	//    (R17).
	var taxonomyMap *schemas.TriggerMap
	if settings.taxonomy != nil {
		candidate := schemas.TriggerMap{
			TaxonomyVersion: settings.taxonomy.TaxonomyVersion,
			Mappings:        settings.taxonomy.Mappings,
		}
		if taxonomyIssues := candidate.Validate(); len(taxonomyIssues) == 0 {
			taxonomyMap = &candidate
			if taxonomyMap.TaxonomyVersion != parsed.TaxonomyVersion {
				diagnostics = append(diagnostics, PolicyDiagnostic{
					Kind:                  PolicyDiagnosticKindTaxonomyVersionMismatch,
					Path:                  "taxonomy_version",
					ExpectedSchemaVersion: TriggerMapSchemaRef(parsed.TaxonomyVersion),
					Message: fmt.Sprintf("policy declares taxonomy_version %d but the provided trigger taxonomy "+
						"is version %d (expected %s; engine decision schema %s) — R17",
						parsed.TaxonomyVersion, taxonomyMap.TaxonomyVersion,
						TriggerMapSchemaRef(parsed.TaxonomyVersion), DecisionSchemaVersion),
				})
			}
		} else {
			diagnostics = append(diagnostics, PolicyDiagnostic{
				Kind:                  PolicyDiagnosticKindTaxonomyInvalid,
				Path:                  "taxonomy",
				ExpectedSchemaVersion: TriggerMapSchemaRef(parsed.TaxonomyVersion),
				Message: fmt.Sprintf("taxonomy is not a valid %s document (engine decision schema %s): %s",
					TriggerMapSchemaRef(parsed.TaxonomyVersion), DecisionSchemaVersion,
					formatIssueText(taxonomyIssues)),
			})
		}
	}

	// 4. Composition: universal order preserved, additive appended/refined
	//    (§8.3.1).
	composedQuestions, conflicts := composeWithAdditives(parsed, parsedAdditives)
	for _, conflict := range conflicts {
		diagnostics = append(diagnostics, PolicyDiagnostic{
			Kind: PolicyDiagnosticKindAdditiveRefinesUniversal,
			Path: "questions." + conflict.Key,
			Message: fmt.Sprintf("%s — the v1 compiler cannot verify refinement intent: use a distinct question "+
				"key (additive questions are new keys; same-key refinement requires explicit promotion)",
				conflict.Message),
		})
	}

	// 5. Deterministic semantic checks over the composed set.
	diagnostics = append(diagnostics, checkQuestionKeyUniqueness(composedQuestions)...)
	diagnostics = append(diagnostics, checkChoiceClosure(composedQuestions)...)
	diagnostics = append(diagnostics, checkRoutingThresholds(parsed.Routing)...)
	diagnostics = append(diagnostics, checkDocumentationImpactSeam(composedQuestions)...)
	if parsed.Universal {
		diagnostics = append(diagnostics, checkUniversalSet(composedQuestions)...)
	}
	diagnostics = append(diagnostics, checkRiskClassOverrides(parsed)...)

	// 6. Trigger-vocabulary resolution — documented skip (R17): the v1 policy
	//    schema declares no trigger field, so there is no in-policy
	//    `triggers:`/`ignore:` vocabulary to resolve here; the manifest
	//    vocabulary is resolved by core's mapping layer, and
	//    ValidateTriggerVocabulary is the compiler-side seam (V-P2-2
	//    groundwork).

	if len(diagnostics) > 0 {
		return CompilePolicyResult{OK: false, Diagnostics: diagnostics}
	}

	hash, err := PolicyHash(*parsed)
	if err != nil {
		// A parsed policy is JSON-safe data by construction; a serialization
		// failure here is a fail-closed invariant breach, never a panic.
		return CompilePolicyResult{OK: false, Diagnostics: []PolicyDiagnostic{{
			Kind:    PolicyDiagnosticKindSchemaViolation,
			Path:    "(root)",
			Message: "policy could not be canonically serialized for hashing: " + err.Error(),
		}}}
	}

	taxonomyVersion := parsed.TaxonomyVersion
	if taxonomyMap != nil {
		taxonomyVersion = taxonomyMap.TaxonomyVersion
	}
	return CompilePolicyResult{
		OK: true,
		Compiled: &CompiledPolicyWorkflow{
			Policy:                *parsed,
			Questions:             composedQuestions,
			Routing:               parsed.Routing,
			PolicyHash:            hash,
			DecisionSchemaVersion: DecisionSchemaVersion,
			TaxonomyVersion:       taxonomyVersion,
		},
	}
}

func composeWithAdditives(policy *Policy, additives []*Policy) ([]Question, []CompositionConflict) {
	questions := make([]Question, 0, len(policy.Questions))
	questions = append(questions, policy.Questions...)
	conflicts := make([]CompositionConflict, 0)
	for _, additive := range additives {
		result := ComposeQuestions(ComposeQuestionsInput{
			UniversalPolicy: QuestionSource{Questions: questions},
			AdditivePolicy:  &QuestionSource{Questions: additive.Questions},
		})
		conflicts = append(conflicts, result.Conflicts...)
		questions = result.Questions
	}
	return questions, conflicts
}

// classifyIssue classifies a schema issue into a named diagnostic kind,
// deterministically, by path shape and this package's own stable schema
// messages (pinned by tests). Everything unmatched stays the coarse
// schema_violation.
func classifyIssue(issue schemas.Issue) (PolicyDiagnosticKind, string, string) {
	path := formatPathSegments(issue.Path)
	message := issue.Message
	kind := PolicyDiagnosticKindSchemaViolation
	if len(issue.Path) > 0 && issue.Path[0] == "routing" {
		kind = PolicyDiagnosticKindRoutingThresholdViolation
	} else if len(issue.Path) > 0 && issue.Path[0] == "risk_class_overrides" {
		kind = PolicyDiagnosticKindRiskClassOverrideViolation
	} else if strings.Contains(message, "duplicate key") {
		kind = PolicyDiagnosticKindDuplicateQuestionKey
	} else if strings.Contains(message, "closed policy choice sets") ||
		strings.Contains(message, "choices must be unique") {
		kind = PolicyDiagnosticKindChoiceSetViolation
	} else if (len(issue.Path) > 2 && (issue.Path[2] == "type" || issue.Path[2] == "choices")) ||
		strings.Contains(message, "type 'choice'") ||
		strings.Contains(message, "type 'score'") {
		kind = PolicyDiagnosticKindQuestionTypeViolation
	}
	return kind, path, message
}

// toDiagnostic maps a classified issue onto a diagnostic (`(root)` at top
// level).
func toDiagnostic(kind PolicyDiagnosticKind, path, message string) PolicyDiagnostic {
	if path == "" {
		path = "(root)"
	}
	return PolicyDiagnostic{Kind: kind, Path: path, Message: message}
}

// withPathPrefix namespaces a diagnostic under an input location (used for
// additive policies).
func withPathPrefix(prefix string, diagnostic PolicyDiagnostic) PolicyDiagnostic {
	if diagnostic.Path == "" {
		diagnostic.Path = prefix
	} else {
		diagnostic.Path = prefix + "." + diagnostic.Path
	}
	return diagnostic
}

// formatPathSegments renders an issue path the way the TS formatPath did.
func formatPathSegments(path []string) string { return strings.Join(path, ".") }

// formatIssueText renders validation issues as `path: message` joined by
// "; " (the TS taxonomy diagnostic body).
func formatIssueText(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, formatPathSegments(issue.Path)+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

// formatIssueList renders a validation error's issues the same way (used by
// versioning.go's rollback seam).
func formatIssueList(err error) string {
	if err == nil {
		return ""
	}
	var validationErr *schemas.ValidationError
	if errors.As(err, &validationErr) {
		return formatIssueText(validationErr.Issues)
	}
	return err.Error()
}

// checkQuestionKeyUniqueness re-checks key uniqueness across the COMPOSED set
// (the parse-time root refinement covers one policy; composition across
// policies can still collide when an additive introduces an existing key).
func checkQuestionKeyUniqueness(questions []Question) []PolicyDiagnostic {
	var diagnostics []PolicyDiagnostic
	seen := make(map[string]struct{}, len(questions))
	for _, question := range questions {
		if _, duplicate := seen[question.Key]; duplicate {
			diagnostics = append(diagnostics, PolicyDiagnostic{
				Kind:    PolicyDiagnosticKindDuplicateQuestionKey,
				Path:    "questions." + question.Key,
				Message: fmt.Sprintf("question keys must be unique across the composed set — duplicate key '%s'", question.Key),
			})
		}
		seen[question.Key] = struct{}{}
	}
	return diagnostics
}

// checkChoiceClosure re-checks choice-set closure after composition
// (defense-in-depth over the parse-time enforcement).
func checkChoiceClosure(questions []Question) []PolicyDiagnostic {
	var diagnostics []PolicyDiagnostic
	for _, question := range questions {
		if question.Type != QuestionTypeChoice || question.Choices == nil {
			continue
		}
		for _, choice := range question.Choices {
			if IsClosedPolicyChoice(choice) {
				continue
			}
			diagnostics = append(diagnostics, PolicyDiagnostic{
				Kind:    PolicyDiagnosticKindChoiceSetViolation,
				Path:    "questions." + question.Key + ".choices",
				Message: fmt.Sprintf("question choice '%s' is not a member of the closed policy choice sets (choice-sets.ts: change_kind/documentation_impact/risk/action)", choice),
			})
		}
	}
	return diagnostics
}

// checkRoutingThresholds re-checks threshold sanity after composition
// (defense-in-depth over the parse-time enforcement; D11).
func checkRoutingThresholds(routing Routing) []PolicyDiagnostic {
	var diagnostics []PolicyDiagnostic
	inUnitRange := func(value float64) bool { return value >= 0 && value <= 1 }
	if !inUnitRange(routing.HighConfidence) || !inUnitRange(routing.Review) {
		diagnostics = append(diagnostics, PolicyDiagnostic{
			Kind: PolicyDiagnosticKindRoutingThresholdViolation,
			Path: "routing",
			Message: fmt.Sprintf("routing thresholds must lie within [0, 1] (got high_confidence %s, review %s)",
				renderNumber(routing.HighConfidence), renderNumber(routing.Review)),
		})
	}
	if routing.Review > routing.HighConfidence {
		diagnostics = append(diagnostics, PolicyDiagnostic{
			Kind: PolicyDiagnosticKindRoutingThresholdViolation,
			Path: "routing.review",
			Message: fmt.Sprintf("routing thresholds must satisfy review ≤ high_confidence (got review %s > high_confidence %s)",
				renderNumber(routing.Review), renderNumber(routing.HighConfidence)),
		})
	}
	if routing.CalibrationFloor != nil {
		floor := *routing.CalibrationFloor
		if floor < routing.Review || floor > routing.HighConfidence {
			diagnostics = append(diagnostics, PolicyDiagnostic{
				Kind: PolicyDiagnosticKindRoutingThresholdViolation,
				Path: "routing.calibration_floor",
				Message: fmt.Sprintf("calibration_floor must lie within [review, high_confidence] (got %s outside [%s, %s])",
					renderNumber(floor), renderNumber(routing.Review), renderNumber(routing.HighConfidence)),
			})
		}
	}
	if routing.DocumentationImpactAutoFloor != nil {
		floor := *routing.DocumentationImpactAutoFloor
		if !inUnitRange(floor) {
			diagnostics = append(diagnostics, PolicyDiagnostic{
				Kind: PolicyDiagnosticKindRoutingThresholdViolation,
				Path: "routing.documentation_impact_auto_floor",
				Message: fmt.Sprintf("documentation_impact_auto_floor must lie within [0, 1] (got %s)",
					renderNumber(floor)),
			})
		}
		if floor < routing.Review {
			diagnostics = append(diagnostics, PolicyDiagnostic{
				Kind: PolicyDiagnosticKindRoutingThresholdViolation,
				Path: "routing.documentation_impact_auto_floor",
				Message: fmt.Sprintf("documentation_impact_auto_floor must be ≥ review (got %s < %s)",
					renderNumber(floor), renderNumber(routing.Review)),
			})
		}
	}
	return diagnostics
}

// checkDocumentationImpactSeam is the §5.2 abstention seam: a
// `documentation_impact` choice question must exist in the composed set.
func checkDocumentationImpactSeam(questions []Question) []PolicyDiagnostic {
	var question *Question
	for index := range questions {
		if questions[index].Key == "documentation_impact" {
			question = &questions[index]
			break
		}
	}
	if question == nil {
		return []PolicyDiagnostic{{
			Kind:    PolicyDiagnosticKindDocumentationImpactMissing,
			Path:    "questions",
			Message: "the 'documentation_impact' question is required — it is the §5.2 abstention seam (its answer probability keys calibration-floor abstention); universal policies always carry it",
		}}
	}
	if question.Type != QuestionTypeChoice {
		return []PolicyDiagnostic{{
			Kind:    PolicyDiagnosticKindQuestionTypeViolation,
			Path:    "questions.documentation_impact.type",
			Message: fmt.Sprintf("the 'documentation_impact' question must be type 'choice' for the §5.2 abstention seam (got '%s')", question.Type),
		}}
	}
	return nil
}

// checkUniversalSet: a universal policy must carry the complete canonical
// universal set (ARCH §8.3.1 / docsync.decision/v1).
func checkUniversalSet(questions []Question) []PolicyDiagnostic {
	keys := make(map[string]struct{}, len(questions))
	for _, question := range questions {
		keys[question.Key] = struct{}{}
	}
	var missing []string
	for _, key := range schemas.UniversalQuestions {
		if _, ok := keys[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []PolicyDiagnostic{{
		Kind: PolicyDiagnosticKindUniversalSetViolation,
		Path: "questions",
		Message: fmt.Sprintf("universal policy is missing canonical universal question(s): %s (UNIVERSAL_QUESTIONS of %s)",
			strings.Join(missing, ", "), DecisionSchemaVersion),
	}}
}

// checkRiskClassOverrides: matrix reference data must stay in the closed risk
// set (conservative-max input data, D2/D11).
func checkRiskClassOverrides(policy *Policy) []PolicyDiagnostic {
	if policy.RiskClassOverrides == nil {
		return nil
	}
	riskLevels := riskLevelValueStrings()
	var diagnostics []PolicyDiagnostic
	for _, pattern := range sortedOverridePatterns(policy.RiskClassOverrides) {
		risk := policy.RiskClassOverrides[pattern]
		if containsValue(riskLevels, string(risk)) {
			continue
		}
		diagnostics = append(diagnostics, PolicyDiagnostic{
			Kind: PolicyDiagnosticKindRiskClassOverrideViolation,
			Path: "risk_class_overrides." + pattern,
			Message: fmt.Sprintf("risk_class_overrides['%s'] = '%s' is not a member of the closed risk set [%s] — matrix input data (D2/D11)",
				pattern, risk, strings.Join(riskLevels, ", ")),
		})
	}
	return diagnostics
}

// sortedOverridePatterns keeps the risk-class override diagnostics
// deterministic (Go map iteration is randomized; the TS Object.entries order
// is insertion order — sorting is the stable equivalent).
func sortedOverridePatterns(overrides map[string]schemas.RiskLevel) []string {
	patterns := make([]string, 0, len(overrides))
	for pattern := range overrides {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	return patterns
}

// universalTriggerValueStrings is the universal trigger vocabulary in
// canonical order (UNIVERSAL_TRIGGER_VALUES).
func universalTriggerValueStrings() []string {
	return choiceSetStrings(schemas.UniversalTriggerValues)
}

// riskLevelValueStrings is the closed risk level set in canonical order
// (RISK_LEVEL_VALUES).
func riskLevelValueStrings() []string {
	return choiceSetStrings(schemas.RiskLevelValues)
}

func containsValue(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// CanonicalJSON serializes a value to canonical JSON (sorted object keys,
// arrays in order, null-safe) — the byte-stable form policy hashes are taken
// over. Delegated to docutil.CanonicalJSON so every package produces the same
// bytes (TS: canonicalJson).
func CanonicalJSON(value any) (string, error) {
	return docutil.CanonicalJSON(value)
}

// PolicyHash is `sha256:<hex>` over the canonical JSON of the parsed policy —
// the format the decision trace's `policy_hash` field carries (ARCH §5.4).
// Built from docutil.CanonicalJSON + docutil.ContentHash so Go-built hashes
// match the TypeScript implementation byte-for-byte.
func PolicyHash(policy Policy) (string, error) {
	canonical, err := docutil.CanonicalJSON(policy.canonicalValue())
	if err != nil {
		return "", err
	}
	return "sha256:" + docutil.ContentHash(canonical), nil
}

// canonicalValue renders the policy as the generic value canonical JSON
// consumes. Optional fields appear exactly when present — including an
// explicitly empty `risk_class_overrides: {}` / `evaluation.metrics: {}`,
// which the TS canonicalJson keeps and an omitempty struct marshal would drop.
func (p *Policy) canonicalValue() map[string]any {
	questions := make([]any, 0, len(p.Questions))
	for index := range p.Questions {
		questions = append(questions, p.Questions[index].canonicalValue())
	}
	out := map[string]any{
		"schema":           p.Schema,
		"policy_version":   p.PolicyVersion,
		"created_by":       p.CreatedBy,
		"reason":           p.Reason,
		"created_at":       p.CreatedAt,
		"questions":        questions,
		"routing":          p.Routing.canonicalValue(),
		"universal":        p.Universal,
		"taxonomy_version": p.TaxonomyVersion,
	}
	if p.Extends != nil {
		out["extends"] = *p.Extends
	}
	if p.RiskClassOverrides != nil {
		overrides := make(map[string]any, len(p.RiskClassOverrides))
		for pattern, risk := range p.RiskClassOverrides {
			overrides[pattern] = string(risk)
		}
		out["risk_class_overrides"] = overrides
	}
	if p.Evaluation != nil {
		out["evaluation"] = p.Evaluation.canonicalValue()
	}
	if p.UnsafeAutoUpdates != nil {
		out["unsafe_auto_updates"] = map[string]any{"enabled": p.UnsafeAutoUpdates.Enabled}
	}
	return out
}

func (q *Question) canonicalValue() map[string]any {
	out := map[string]any{
		"key":    q.Key,
		"type":   string(q.Type),
		"prompt": q.Prompt,
	}
	if q.Choices != nil {
		choices := make([]any, 0, len(q.Choices))
		for _, choice := range q.Choices {
			choices = append(choices, choice)
		}
		out["choices"] = choices
	}
	if q.Scoring != nil {
		out["scoring"] = map[string]any{"min": q.Scoring.Min, "max": q.Scoring.Max}
	}
	return out
}

func (r *Routing) canonicalValue() map[string]any {
	out := map[string]any{
		"high_confidence": r.HighConfidence,
		"review":          r.Review,
	}
	if r.CalibrationFloor != nil {
		out["calibration_floor"] = *r.CalibrationFloor
	}
	if r.DocumentationImpactAutoFloor != nil {
		out["documentation_impact_auto_floor"] = *r.DocumentationImpactAutoFloor
	}
	return out
}

func (e *PolicyEvaluation) canonicalValue() map[string]any {
	metrics := make(map[string]any, len(e.Metrics))
	for metric, value := range e.Metrics {
		metrics[metric] = value
	}
	return map[string]any{
		"split":   e.Split,
		"metrics": metrics,
	}
}
