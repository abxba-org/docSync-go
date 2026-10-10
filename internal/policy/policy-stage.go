package policy

// Ported from packages/policy/src/orchestrator/policy-stage.ts (P5-T08;
// PLAN §6.4 `policy` stage; ARCH §3.5, §13) — the adapter that wires the
// Phase-2 outcome engine (MapOutcome) into the core `PolicyStagePort` shape.
// THE APPLICATION DECIDES whether a patch is permitted (D11): the adapter
// never re-derives an outcome — it derives the `PolicyOutcomeInput` the
// engine defines and maps MapOutcome's verdict onto the stage result
// VERBATIM.
//
// Input derivation (all application-owned, never invented):
//   - `decision` — the schema-valid `docsync.decision/v1` object is narrowed
//     to the structural PendingDecision seam field-by-field (answers carry
//     value+probability; Stage-1 abstain passes through verbatim — MapOutcome
//     never re-derives over an explicit verdict).
//   - `docMode` — the doc's effective mutability mode per §11.3 precedence
//     (entry mode → defaults mode → built-in `suggest`); a doc path ABSENT
//     from the manifest resolves `external` (D7: the manifest is an allowlist
//     — unlisted is never writable; the D2 ownership floor then blocks at any
//     confidence).
//   - `manifestRisk` — NOT derived: the v1 `Manifest` schema carries no
//     per-document risk class (checked: path/mode/sources/triggers/ignore
//     only), so the conservative-max bucket runs on Jev's risk choice and the
//     policy's own `risk_class_overrides` (below). When the manifest grows
//     risk data, this is the single derivation point to extend.
//   - `riskClassOverrides` / `docPath` / `matchGlobs` — the compiled policy's
//     own `risk_class_overrides` table (D11 data) matched against the doc
//     path through the INJECTED glob matcher (PolicyStageDeps.MatchGlobs; the
//     glob engine is owned by @docsync/core and policy cannot import it —
//     OD-1). Overrides present without a matcher make riskBucket throw its
//     typed EngineRangeError (fail loud — override resolution is never
//     silently skipped).
//   - `verifierUncertain` — the Phase-4/5 seam (§5.2): true exactly when the
//     Stage-3 verifier returned `review`. `verification: nil` passes the
//     flag's null-equivalent (absent) — the outcome stays the engine's own
//     verdict; the runner owns the review-path downgrade for missing
//     verification evidence.
//   - `policyGap` / `conflictingEvidence` — NOT derived: v1 composed
//     questions carry no change-kind scoping and the resolve input carries no
//     analysis to disagree with; a derived-looking flag here would be
//     fabrication. Documented seams, set only by a composition that owns the
//     data.
//
// Null decision (Jev abstained or routed IGNORE): the engine cannot run —
// `PolicyOutcomeInput.decision` is mandatory. Fail closed to a first-class
// abstain result (`review`, reviewRequired) with the operational reason
// `other` and a factual detail; per the §5.2 two-tier discipline an
// operational reason NEVER enqueues.
//
// Review-queue writes (first-class abstain, §5.2): when MapOutcome yields an
// abstain AND the queue is wired AND the source decision itself abstained
// with the SAME decision-level reason (the queue's own contract — it stores
// the decision verbatim and rejects operational reasons and non-abstained
// decisions), the adapter enqueues exactly one record: the decision verbatim
// + the reason + evidence whose machine-resolvable anchor is the doc path
// (`other` kind) with the abstain detail riding verbatim as the note. The
// deterministic `event_id` is surfaced as the adapter-level extension field
// `queueEventId` — the fixed core `PolicyStageResult` shape has no such
// member, and core must not be edited for it: the RUN-LEVEL abstain record
// (`docsync.run-outcome/v1` `abstains[].queue_event_id`) is where queue
// linkage lands; a runner copies it from there. A queue rejection is a
// programming error under this guard — it propagates (fail loud; the runner
// owns degradation).
//
// Structural seam (mirrors the Phase-2 decision-stage pattern): policy must
// NOT import @docsync/core (OD-1), so the ctx/result shapes are defined here
// structurally and pinned against core-sketch literals by the type-level
// probes (ported as Go interface assignability pins in
// policy_stage_test.go).
//
// Pure logic only: the only effect is the optional injected queue write; no
// fs, no network, no wall clock — never a clock input.

import "github.com/DUDE616/docSync-go/internal/schemas"

/* ------------------------------------------------------------------ shapes */

// PolicyStageRunContext is the run-context members the policy stage consumes
// (TS: PolicyStageRunContext) — a structural subset of the core `RunContext`
// (@docsync/policy must not import @docsync/core, OD-1; the full core
// context is assignable to this shape).
type PolicyStageRunContext struct {
	// Manifest is the manifest allowlist for the run (D7) — the doc-mode
	// derivation source.
	Manifest schemas.Manifest
}

// PolicyStageResolveInput is the resolve input (TS: PolicyStageResolveInput)
// — structurally identical to the core `PolicyStagePort` input with the ctx
// subset above.
type PolicyStageResolveInput struct {
	Ctx PolicyStageRunContext
	// DocPath is the repo-relative path of the document being resolved.
	DocPath string
	// Decision is the Stage-1 Jev decision; nil when Jev abstained or routed
	// IGNORE.
	Decision *schemas.Decision
	// Verification is the verification report when a patch was verified; nil
	// otherwise.
	Verification *schemas.VerificationReport
}

// PolicyStageAbstain is the abstain marker when the stage refused to resolve
// (fail closed) (TS: PolicyStageResultWithQueue['abstain']).
type PolicyStageAbstain struct {
	Reason schemas.AbstainReason
	Detail string
}

// PolicyStageResultWithQueue is the stage result (TS:
// PolicyStageResultWithQueue) — the core `PolicyStageResult` shape PLUS the
// adapter-level `queueEventId` extension (core's fixed shape has no such
// member; the run-level abstain record is where queue linkage lands — see
// the module doc).
type PolicyStageResultWithQueue struct {
	// Outcome is the pipeline outcome (run-outcome union; the engine's
	// verdict verbatim).
	Outcome schemas.PipelineOutcome
	// ReviewRequired is true when the outcome requires human review routing.
	ReviewRequired bool
	// Abstain is the abstain marker when the stage refused to resolve (fail
	// closed).
	Abstain *PolicyStageAbstain
	// QueueEventID is the adapter extension: the deterministic review-queue
	// event id (`a_<hex16>`) when the abstain was enqueued via
	// PolicyStageDeps.Queue.
	QueueEventID *string
}

// AbstainQueuePick is the policy stage's structural slice of the Phase-2
// abstain queue (TS: PolicyStageDeps['queue'] = Pick<AbstainQueue,
// 'recordAbstain'>): only the enqueue half is consumed. The Phase-2
// InMemoryAbstainQueue satisfies it directly and test doubles satisfy it
// structurally (the same seam pattern as the decision-engine port). Unwired
// ⇒ abstains ride the result without a queue write.
type AbstainQueuePick interface {
	RecordAbstain(input RecordAbstainInput) (AbstainEvent, error)
}

// PolicyStageDeps is the stage dependencies (TS: PolicyStageDeps) — the
// compiled workflow plus the optional Phase-2 seams.
type PolicyStageDeps struct {
	// Workflow is the compiled Jev policy workflow whose calibrated data this
	// run consumes (D11).
	Workflow CompiledPolicyWorkflow
	// Queue is the optional Phase-2 abstain queue (e.g.
	// InMemoryAbstainQueue) — only the RecordAbstain half is consumed.
	Queue AbstainQueuePick
	// MatchGlobs is the injected glob matcher (path, glob) → match for the
	// policy's `risk_class_overrides` (the glob engine is @docsync/core's;
	// OD-1). Required when the compiled policy declares overrides —
	// riskBucket throws its typed range error otherwise (fail loud).
	MatchGlobs func(path, glob string) bool
}

/* ----------------------------------------------------------------- the port */

// docModeOf resolves the effective §11.3 mode precedence (TS: docModeOf):
// unlisted paths are the D2 `external` floor — the manifest is an allowlist
// (D7), so an unlisted document is never writable.
func docModeOf(manifest schemas.Manifest, docPath string) OwnershipFloorMode {
	for _, entry := range manifest.Documents {
		if entry.Path != docPath {
			continue
		}
		if entry.Mode != nil {
			return OwnershipFloorMode(*entry.Mode)
		}
		if manifest.Defaults != nil && manifest.Defaults.Mode != nil {
			return OwnershipFloorMode(*manifest.Defaults.Mode)
		}
		return OwnershipFloorSuggest
	}
	return OwnershipFloorExternal // D7: unlisted = external, never writable
}

// pendingFromDecision narrows the schema-valid decision to the structural
// PendingDecision seam (TS: pendingFromDecision) — field-for-field, nothing
// derived. The answer block converts the wire `Answer` (raw JSON value) into
// the engine's `PendingDecisionAnswer` seam; every other field rides through.
func pendingFromDecision(decision schemas.Decision) PendingDecision {
	pending := PendingDecision{
		ChangeKind:          decision.ChangeKind,
		DocumentationImpact: decision.DocumentationImpact,
		Risk:                decision.Risk,
		Confidence:          decision.Confidence,
		Abstain:             decision.Abstain,
	}
	if len(decision.Answers) > 0 {
		answers := make(map[string]PendingDecisionAnswer, len(decision.Answers))
		for key, answer := range decision.Answers {
			answers[key] = PendingDecisionAnswer{
				Value:       []byte(answer.Value),
				Probability: answer.Probability,
			}
		}
		pending.Answers = answers
	}
	if decision.AbstainReason != nil {
		reason := *decision.AbstainReason
		pending.AbstainReason = &reason
	}
	return pending
}

// PolicyStagePort is the core-shaped stage port (TS: the object literal
// createPolicyStagePort returns — `{ resolve }`). Resolve returns the error
// when the engine's fail-closed guards fire (an EngineRangeError from
// riskBucket/confidenceBucket/matrixCell) or the queue rejects the record —
// fail loud, never coerced into a fabricated outcome.
type PolicyStagePort interface {
	Resolve(input PolicyStageResolveInput) (PolicyStageResultWithQueue, error)
}

// CreatePolicyStagePort creates the policy-stage port (P5-T08; TS:
// createPolicyStagePort). One MapOutcome call per resolve; the engine's
// semantics are kept verbatim — the adapter adds derivation and queue
// writes, never re-derivation.
func CreatePolicyStagePort(deps PolicyStageDeps) PolicyStagePort {
	return &policyStagePort{deps: deps}
}

type policyStagePort struct {
	deps PolicyStageDeps
}

func (p *policyStagePort) Resolve(input PolicyStageResolveInput) (PolicyStageResultWithQueue, error) {
	// Null decision: the engine cannot run without a pending decision —
	// fail closed to a first-class abstain (never forced into a guess).
	// Operational reason ⇒ bypasses the review queue (§5.2 two-tier).
	if input.Decision == nil {
		return PolicyStageResultWithQueue{
			Outcome:        schemas.PipelineOutcomeReview,
			ReviewRequired: true,
			Abstain: &PolicyStageAbstain{
				Reason: schemas.AbstainReasonOther,
				Detail: `policy stage received no Stage-1 decision for "` + input.DocPath +
					`" (Jev abstained or routed IGNORE) — no pending decision to map; ` +
					`routed to review (fail closed; the run-level abstain record carries the linkage)`,
			},
		}, nil
	}

	mapInput := PolicyOutcomeInput{
		Decision: pendingFromDecision(*input.Decision),
		Compiled: &p.deps.Workflow,
		DocMode:  docModeOf(input.Ctx.Manifest, input.DocPath),
	}
	if overrides := p.deps.Workflow.Policy.RiskClassOverrides; overrides != nil {
		// The compiled policy's overrides are validated data (the compiler
		// rejects a non-low/medium/high value at compile time); the wire map
		// renders onto the engine's typed input here — the adapter's single
		// derivation point.
		riskClassOverrides := make(map[string]schemas.RiskLevel, len(overrides))
		for glob, level := range overrides {
			riskClassOverrides[glob] = schemas.RiskLevel(level)
		}
		mapInput.RiskClassOverrides = riskClassOverrides
		docPath := input.DocPath
		mapInput.DocPath = &docPath
		if p.deps.MatchGlobs != nil {
			mapInput.MatchGlobs = p.deps.MatchGlobs
		}
	}
	if input.Verification != nil && input.Verification.Verdict == schemas.VerdictReview {
		mapInput.VerifierUncertain = true
	}

	outcome, err := MapOutcome(mapInput)
	if err != nil {
		return PolicyStageResultWithQueue{}, err
	}

	// First-class abstain → review-queue write (§5.2), only when the
	// decision itself abstained with the SAME decision-level reason — the
	// queue's own contract (it stores the decision verbatim; operational
	// reasons and engine-derived seams like the calibration band on a
	// non-abstaining decision are not queueable and would be rejected). A
	// queue rejection would be a programming error under this guard — it
	// propagates (fail loud; the runner owns degradation).
	var queueEventID *string
	if p.deps.Queue != nil &&
		outcome.Abstain != nil &&
		input.Decision.Abstain &&
		input.Decision.AbstainReason != nil &&
		*input.Decision.AbstainReason == outcome.Abstain.Reason &&
		policyStageIsDecisionLevelReason(outcome.Abstain.Reason) {
		event, recordErr := p.deps.Queue.RecordAbstain(RecordAbstainInput{
			Decision: input.Decision,
			Reason:   outcome.Abstain.Reason,
			Evidence: []schemas.Evidence{
				{
					Kind: schemas.EvidenceKindOther,
					Ref:  input.DocPath,
					Note: outcome.Abstain.Detail,
				},
			},
		})
		if recordErr != nil {
			return PolicyStageResultWithQueue{}, recordErr
		}
		eventID := event.EventID
		queueEventID = &eventID
	}

	result := PolicyStageResultWithQueue{
		Outcome:        schemas.PipelineOutcome(outcome.Outcome),
		ReviewRequired: outcome.ReviewRequired,
	}
	if outcome.Abstain != nil {
		abstain := PolicyStageAbstain{Reason: outcome.Abstain.Reason, Detail: outcome.Abstain.Detail}
		result.Abstain = &abstain
	}
	if queueEventID != nil {
		result.QueueEventID = queueEventID
	}
	return result, nil
}

/* ------------------------------------------------------------------ helpers */

// policyStageIsDecisionLevelReason reports membership in the Tier-1
// queueable abstain reasons (TS: `(DECISION_LEVEL_ABSTAIN_REASONS as
// readonly string[]).includes(...)`) — operational reasons bypass the queue.
func policyStageIsDecisionLevelReason(reason schemas.AbstainReason) bool {
	for _, candidate := range schemas.DecisionLevelAbstainReasons {
		if candidate == reason {
			return true
		}
	}
	return false
}
