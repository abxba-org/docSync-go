// Ported from packages/policy/src/engine/outcome.ts — outcome mapping
// (P2-T19; ARCH §5.2, §5.3; PLAN §3.3 lines 545-549): the abstain-first stage
// that turns a pending Stage-1 decision into the policy outcome set
// `ignore | suggest | auto | blocked`, with `review` resolved as the
// WORKFLOW-STATE outcome (§5.2: something a human is expected to act on,
// carried on the outcome union with ReviewRequired: true), never as a silent
// fall-through.
//
// §5.2 hard rules, binding:
//  1. Uncertain cases are never forced into update/ignore.
//  2. Abstention is first-class and named, never a silent fall-through.
//
// Abstain-first precedence (the order below is binding): an upstream Stage-1
// abstain passes through unchanged (never re-derived over an explicit verdict);
// then the caller-asserted `policy_gap` / `conflicting_evidence` /
// `verifier_uncertain` seams; then the calibration no-man's-zone check on the
// DECIDING answer's probability; only then the deterministic ignore
// short-circuit and the matrix. Every abstained case maps to outcome `review`
// with ReviewRequired: true and a populated abstain block — NEVER `auto` or
// `ignore` (§5.2 rule 1).
//
// The pending-decision seam: this module consumes a STRUCTURAL pending shape
// (PendingDecision), not the finalized `docsync.decision/v1` object — Phase 5
// (orchestrator) wires the validated decision object in; the structural shape
// keeps this layer pure and dependency-free.
package policy

import (
	"strconv"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// documentationImpactAnswerKey is the question key the §5.2 abstention seam
// keys on (compiler enforces its presence, P2-T16).
const documentationImpactAnswerKey = "documentation_impact"

// PolicyOutcomeValues are the policy outcome labels (ARCH §5.2). The four broad
// outcomes are `ignore | suggest | auto | blocked`; `review` is ALSO carried as
// an outcome value because §5.2's practical mapping lists "human review" as a
// destination and BOTH the abstain path and the matrix's diagonal resolve
// there — `review` is the WORKFLOW-STATE outcome (something a human is
// expected to act on, ReviewRequired: true), distinct from `suggest` (a
// proposal that needs no review workflow) and from `blocked` (a hard stop, not
// a work item).
var PolicyOutcomeValues = []PolicyOutcomeLabel{
	PolicyOutcomeIgnore, PolicyOutcomeSuggest, PolicyOutcomeAuto, PolicyOutcomeBlocked, PolicyOutcomeReview,
}

// PolicyOutcomeLabel is one policy outcome label (TS PolicyOutcomeLabel).
type PolicyOutcomeLabel string

// PolicyOutcomeLabel values.
const (
	PolicyOutcomeIgnore  PolicyOutcomeLabel = "ignore"
	PolicyOutcomeSuggest PolicyOutcomeLabel = "suggest"
	PolicyOutcomeAuto    PolicyOutcomeLabel = "auto"
	PolicyOutcomeBlocked PolicyOutcomeLabel = "blocked"
	PolicyOutcomeReview  PolicyOutcomeLabel = "review"
)

// PendingDecisionAnswer is one structural answer slot of the pending decision
// — the deciding-answer probability seam (ARCH §5.4: every answer carries a
// value AND a probability; policy thresholds consume the probability). Loose
// by design: the validated `docsync.decision/v1` answer replaces this at the
// Phase-5 seam.
type PendingDecisionAnswer struct {
	// Value is the raw JSON answer value (TS `unknown`).
	Value []byte `json:"value,omitempty"`
	// Probability is the confidence threshold input, in [0, 1].
	Probability float64 `json:"probability"`
}

// PendingDecision is the pending Stage-1 decision (structural seam — see
// module doc). Abstain fields pass an already-decided Stage-1 abstain through
// (§5.2 decision-level reasons); the orchestrator-computed fields (PolicyGap /
// VerifierUncertain / ConflictingEvidence) live on PolicyOutcomeInput's, not
// here.
type PendingDecision struct {
	ChangeKind          schemas.ChangeKind          `json:"change_kind"`
	DocumentationImpact schemas.DocumentationImpact `json:"documentation_impact"`
	// Risk is Jev's `risk` choice — the conservative-max input of the risk
	// bucket (ARCH §5.3).
	Risk schemas.RiskLevel `json:"risk"`
	// Confidence is the overall decision confidence (the §5.4 confidence
	// score, distinct from answer probabilities).
	Confidence float64 `json:"confidence"`
	// Answers are the answer probabilities keyed by question key (the
	// `documentation_impact` answer keys the §5.2 band).
	Answers map[string]PendingDecisionAnswer `json:"answers,omitempty"`
	// DecidingProbability is the explicit deciding-answer probability override
	// — used when the caller has already extracted the §5.2 deciding
	// probability. Treated AS the `documentation_impact` deciding answer.
	DecidingProbability *float64 `json:"decidingProbability,omitempty"`
	// Abstain is the Stage-1 decision-level abstain, passed through verbatim
	// (never re-derived).
	Abstain bool `json:"abstain,omitempty"`
	// AbstainReason is the Stage-1 abstain reason (§5.2 two-tier taxonomy);
	// must name a reason when Abstain is true.
	AbstainReason *schemas.AbstainReason `json:"abstain_reason,omitempty"`
}

// PolicyOutcomeInput is the mapOutcome input (TS PolicyOutcomeInput).
type PolicyOutcomeInput struct {
	// Decision is the pending Stage-1 decision (structural seam).
	Decision PendingDecision
	// Compiled is the compiled Jev policy workflow whose calibrated data this
	// run consumes (D11).
	Compiled *CompiledPolicyWorkflow
	// DocMode is the effective document mutability mode — the D2 floor +
	// auto-permission input (§11.2).
	DocMode OwnershipFloorMode
	// ManifestRisk is the pre-resolved manifest-declared risk class
	// (conservative-max input, D2/D11).
	ManifestRisk *schemas.RiskLevel
	// RiskClassOverrides is the raw per-doc-glob risk classes from the policy
	// (conservative-max input, D2/D11).
	RiskClassOverrides map[string]schemas.RiskLevel
	// DocPath is the document path the overrides match against (required iff
	// overrides present).
	DocPath *string
	// MatchGlobs is the injected glob matcher (required iff overrides
	// present) — see matrix.go.
	MatchGlobs func(path, glob string) bool
	// PolicyGap is the Phase-5 seam (§5.2): true when no question/rule covers
	// the change kind — the orchestrator asserts this against the composed
	// question set; a Phase-5 wiring input, not derivable here.
	PolicyGap bool
	// VerifierUncertain is the Phase-4/5 seam (§5.2): Stage-3 verifier
	// returned `review`. The Stage-3 wiring lands in Phase 4/5; until then
	// this flag is the documented seam.
	VerifierUncertain bool
	// ConflictingEvidence is the Phase-5 seam (§5.2): question answers
	// disagree with deterministic analysis. Phase 5 computes the
	// disagreement; until then this flag is the documented seam.
	ConflictingEvidence bool
}

// AbstainRecord is the first-class abstain record (§5.2): named reason +
// human-readable detail.
type AbstainRecord struct {
	Reason schemas.AbstainReason `json:"reason"`
	Detail string                `json:"detail"`
}

// PolicyOutcome is the policy outcome (§5.2/§5.3). ReviewRequired is true iff
// Outcome is `review` — the workflow state IS the review queue; `blocked` is
// surfaced differently (a hard stop with provenance, not a human work item),
// `suggest` needs no review workflow, and `ignore` needs nothing. Provenance
// fields: RiskBucket/ConfidenceBucket always populated; Cell only when the
// matrix actually ran (nil on abstain and ignore paths — no matrix evaluation
// happened there).
type PolicyOutcome struct {
	Outcome          PolicyOutcomeLabel `json:"outcome"`
	ReviewRequired   bool               `json:"reviewRequired"`
	Abstain          *AbstainRecord     `json:"abstain,omitempty"`
	Cell             *ResolvedCell      `json:"cell,omitempty"`
	RiskBucket       RiskBucket         `json:"riskBucket"`
	ConfidenceBucket ConfidenceBucket   `json:"confidenceBucket"`
}

// ignorableChangeKinds are the change kinds with deterministic no-impact
// routing — the Phase-1 R10 semantics (documented short-circuit).
var ignorableChangeKinds = []schemas.ChangeKind{
	schemas.ChangeKindFormattingOnly,
	schemas.ChangeKindTestOnly,
}

// decidingSource is where the deciding probability came from — decides whether
// the per-answer floor band applies. TS: DecidingSource.
type decidingSource string

const (
	decidingSourceDocumentationImpactAnswer decidingSource = "documentation_impact_answer"
	decidingSourceExplicit                  decidingSource = "explicit"
	decidingSourceOverallConfidence         decidingSource = "overall_confidence"
)

// decidingProbabilityInfo is the resolved deciding probability with its
// source. TS: DecidingProbability.
type decidingProbabilityInfo struct {
	probability float64
	source      decidingSource
}

// resolveDecidingProbability is the deciding-answer probability (ARCH
// §5.2/§5.4; TS decidingProbability): the `documentation_impact` answer
// probability when present, else the explicit DecidingProbability override,
// else the overall confidence. The documentation_impact question is the §5.2
// abstention seam (compiler enforces its presence, P2-T16).
func resolveDecidingProbability(decision PendingDecision) decidingProbabilityInfo {
	if answer, ok := decision.Answers[documentationImpactAnswerKey]; ok {
		return decidingProbabilityInfo{
			probability: answer.Probability,
			source:      decidingSourceDocumentationImpactAnswer,
		}
	}
	if decision.DecidingProbability != nil {
		return decidingProbabilityInfo{probability: *decision.DecidingProbability, source: decidingSourceExplicit}
	}
	return decidingProbabilityInfo{probability: decision.Confidence, source: decidingSourceOverallConfidence}
}

// calibrationBandInfo is the calibration no-man's-zone band (§5.2). TS:
// CalibrationBand.
type calibrationBandInfo struct {
	probability float64
	lower       float64
	upper       float64
}

// resolveCalibrationBand is the calibration no-man's-zone band (§5.2;
// schema.ts routing doc; TS calibrationBand): a deciding probability STRICTLY
// INSIDE the open interval `(review, floorUpper)` abstains with reason
// `calibration_floor`. When the deciding probability IS the
// `documentation_impact` answer (or the explicit override standing in for it)
// and the policy declares DocumentationImpactAutoFloor, that per-answer floor
// takes precedence over the global band for this answer (schema precedence
// rule); otherwise the global band upper bound is CalibrationFloor, defaulting
// to HighConfidence. A probability exactly at `review` or exactly at the floor
// is NOT inside the open interval; the region [floorUpper, high_confidence) is
// not abstained and lands in the medium confidence bucket, which has no `auto`
// cell (matrix.go doc).
func resolveCalibrationBand(routing Routing, deciding decidingProbabilityInfo) (calibrationBandInfo, bool) {
	lower := routing.Review
	perAnswerFloorApplies := deciding.source != decidingSourceOverallConfidence &&
		routing.DocumentationImpactAutoFloor != nil
	upper := routing.HighConfidence
	if routing.CalibrationFloor != nil {
		upper = *routing.CalibrationFloor
	}
	if perAnswerFloorApplies {
		upper = *routing.DocumentationImpactAutoFloor
	}
	if deciding.probability > lower && deciding.probability < upper {
		return calibrationBandInfo{probability: deciding.probability, lower: lower, upper: upper}, true
	}
	return calibrationBandInfo{}, false
}

// calibrationDetail is the §5.2 detail wording — mirrors the canonical
// example's shape, naming probability + both thresholds + policy version.
func calibrationDetail(band calibrationBandInfo, policyVersion int) string {
	return "deciding probability " + formatJSNumber(band.probability) + " falls between auto floor (" +
		formatJSNumber(band.upper) + ") and review threshold (" + formatJSNumber(band.lower) + ") " +
		"under policy v" + strconv.Itoa(policyVersion)
}

// MapOutcome maps a pending Stage-1 decision to the policy outcome (P2-T19).
// Pure and deterministic; abstain-first (see module doc); the D2 ownership
// floor and the mode-permission gate (V-P2-8 groundwork: the `auto` cell fires
// only in manifest mode `auto` — every other writable mode degrades `auto` to
// `suggest`) are applied after the matrix. The deterministic ignore
// short-circuit mirrors Phase 1's R10 semantics: `formatting_only`/`test_only`
// changes with `documentation_impact: 'none'` are routed to `ignore` BEFORE
// the matrix (they carry no documentation surface to route), but AFTER every
// abstain check (abstain-first: an uncertain deciding probability still
// abstains even on an ignorable-looking change kind).
func MapOutcome(input PolicyOutcomeInput) (PolicyOutcome, error) {
	decision := input.Decision
	routing := input.Compiled.Routing
	policyVersion := input.Compiled.Policy.PolicyVersion

	bucketRisk, err := ResolveRiskBucket(RiskBucketInput{
		JevRisk:            decision.Risk,
		ManifestRisk:       input.ManifestRisk,
		RiskClassOverrides: input.RiskClassOverrides,
		DocPath:            input.DocPath,
		MatchGlobs:         input.MatchGlobs,
	})
	if err != nil {
		return PolicyOutcome{}, err
	}
	bucketConfidence, err := ResolveConfidenceBucket(decision.Confidence, routing)
	if err != nil {
		return PolicyOutcome{}, err
	}

	abstainOutcome := func(reason schemas.AbstainReason, detail string) PolicyOutcome {
		return PolicyOutcome{
			Outcome:          PolicyOutcomeReview,
			ReviewRequired:   true,
			Abstain:          &AbstainRecord{Reason: reason, Detail: detail},
			RiskBucket:       bucketRisk,
			ConfidenceBucket: bucketConfidence,
		}
	}

	// §5.2 rule 1+2 — abstain-first. An explicit Stage-1 verdict is never
	// re-derived over (including over the seams below).
	if decision.Abstain {
		reason := schemas.AbstainReasonInsufficientConfidence
		if decision.AbstainReason != nil {
			reason = *decision.AbstainReason
		}
		return abstainOutcome(reason,
			"stage-1 decision abstained with reason '"+string(reason)+"' under policy v"+
				strconv.Itoa(policyVersion)), nil
	}
	if input.PolicyGap {
		return abstainOutcome(schemas.AbstainReasonPolicyGap,
			"no question/rule covers change kind '"+string(decision.ChangeKind)+"' under policy v"+
				strconv.Itoa(policyVersion)), nil
	}
	if input.ConflictingEvidence {
		return abstainOutcome(schemas.AbstainReasonConflictingEvidence,
			"question answers disagree with deterministic analysis under policy v"+
				strconv.Itoa(policyVersion)), nil
	}
	if input.VerifierUncertain {
		return abstainOutcome(schemas.AbstainReasonVerifierUncertain,
			"verifier uncertainty flagged for this decision under policy v"+
				strconv.Itoa(policyVersion)), nil
	}

	// Calibration no-man's zone (§5.2 example: documentation_impact
	// probability 0.83 between auto floor 0.94 and review threshold 0.72 →
	// abstain).
	deciding := resolveDecidingProbability(decision)
	if band, ok := resolveCalibrationBand(routing, deciding); ok {
		return abstainOutcome(schemas.AbstainReasonCalibrationFloor, calibrationDetail(band, policyVersion)), nil
	}

	// Deterministic ignore short-circuit (Phase-1 R10 semantics), before the
	// matrix.
	if isIgnorableChangeKind(decision.ChangeKind) &&
		decision.DocumentationImpact == schemas.DocumentationImpactNone {
		return PolicyOutcome{
			Outcome:          PolicyOutcomeIgnore,
			ReviewRequired:   false,
			RiskBucket:       bucketRisk,
			ConfidenceBucket: bucketConfidence,
		}, nil
	}

	// Matrix path: fixed shape (matrix.go) + calibrated boundaries (D11) →
	// ownership floor (D2) → mode-permission gate (V-P2-8 groundwork).
	cell, err := LookupMatrixCell(bucketRisk, bucketConfidence)
	if err != nil {
		return PolicyOutcome{}, err
	}
	resolved := ApplyOwnershipFloor(cell, input.DocMode)
	withCell := func(outcome PolicyOutcomeLabel, reviewRequired bool) PolicyOutcome {
		resolvedCell := resolved
		return PolicyOutcome{
			Outcome:          outcome,
			ReviewRequired:   reviewRequired,
			Cell:             &resolvedCell,
			RiskBucket:       bucketRisk,
			ConfidenceBucket: bucketConfidence,
		}
	}

	switch resolved {
	case ResolvedCellBlocked:
		return withCell(PolicyOutcomeBlocked, false), nil
	case ResolvedCellReview:
		return withCell(PolicyOutcomeReview, true), nil
	case ResolvedCellSuggest:
		return withCell(PolicyOutcomeSuggest, false), nil
	case ResolvedCellAuto:
		// Auto only when the manifest mode permits it; any other writable mode
		// degrades the auto cell to a suggestion (ARCH §11.2: `auto` mode
		// means "automatic patching is permitted within policy").
		if input.DocMode == OwnershipFloorAuto {
			return withCell(PolicyOutcomeAuto, false), nil
		}
		return withCell(PolicyOutcomeSuggest, false), nil
	}
	// Unreachable: ApplyOwnershipFloor returns only the four ResolvedCell
	// values. Fail closed rather than returning a zero outcome (D8).
	return PolicyOutcome{}, newEngineRangeError(
		"mapOutcome resolved an unknown matrix cell '" + string(resolved) + "' — fail closed",
	)
}

func isIgnorableChangeKind(changeKind schemas.ChangeKind) bool {
	for _, candidate := range ignorableChangeKinds {
		if candidate == changeKind {
			return true
		}
	}
	return false
}
