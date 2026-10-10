package schemas

// Ported from packages/schemas/src/v1/run-outcome.ts (RUN_OUTCOME_SCHEMA —
// the FROZEN RunOutcome API — plus all closed vocabularies, record shapes,
// both examples, and the superRefine refinement rules).
//
// Run outcome — `docsync.run-outcome/v1` (P5-T15; PLAN §6.3; ARCH
// §12.1/§10.6). FROZEN single-run pipeline API: the Phase 7 CLI
// (check/fix/pr, exit codes 0/1/2/3) and the Phase 8 CI surfaces consume
// THIS shape — frozen interface, changes require a version bump (V-P5-16).
// Every timestamp/clock is a field supplied by the orchestrator, never
// generated here; z.strictObject everywhere and every union is closed.
//
// Fail-closed degradation is visible at the top level: `status` is the
// closed RunStatus union — `degraded` is the honest middle state and an
// analyzer failure is `inconclusive`, never "no docs affected" (invariant
// 9). `degradations` carries the ARCH §3.4 degradation-matrix vocabulary
// (DegradationKindValues, incl. bounded-loop `cap-overflow` overflow and the
// A10 analyzer→path-level downgrade as `analyzer-inconclusive`).
//
// superRefine rules (kept FEW, precise, test-covered — V-P5-16):
//  1. `status === 'completed'` ⇒ `finished_at` non-null.
//  2. `status === 'completed'` ⇒ every per-document record is accounted
//     for: a `verification` block, an `abstain` record, a matching
//     run-level `findings` entry (matched by `doc_path`), or a terminal
//     `policy_outcome` of `ignore`/`review` — a completed run never leaves
//     a document in limbo.
//  3. any document with `partial_coverage: true` ⇒ at least one
//     `degradations` entry with `kind: 'cap-overflow'`.
//
// Vocabulary reuse (OD-2 — one taxonomy across artifacts; import, never
// duplicate): `key`/`state_churn` reuse the v1/run-record.go shapes;
// `RunStage.Status` reuses RunStageStatus; `policy_outcome` pins the exact
// five-value policy-outcome vocabulary of the Phase-2 policy engine (the
// values are pinned equal by tests); the per-document `verification` block
// reuses the verification-report verdict and auto-block-reason
// vocabularies (verification-report.go); abstain reasons reuse the
// canonical AbstainReasonValues taxonomy of `docsync.decision/v1`
// (decision.go); model-binding roles reuse RequiredModelRoles (state.go);
// `baseline_run` mirrors the core baseline-resolution ladder plus `none`.

// RunStatus is the frozen run-status union — fail-closed degradation
// visible at the top level (RUN_STATUS).
type RunStatus string

// RunStatusValues is the frozen completed/degraded/failed union.
const (
	RunStatusCompleted RunStatus = "completed"
	RunStatusDegraded  RunStatus = "degraded"
	RunStatusFailed    RunStatus = "failed"
)

// RunStatusValues lists all run statuses in canonical order (RUN_STATUS).
var RunStatusValues = []RunStatus{RunStatusCompleted, RunStatusDegraded, RunStatusFailed}

// IsValidRunStatus reports membership in RUN_STATUS.
func IsValidRunStatus(s string) bool {
	for _, v := range RunStatusValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// PipelineOutcome is the frozen pipeline outcome union — the exact
// five-value policy-outcome vocabulary (`ignore | suggest | auto | blocked
// | review`) the Phase-2 policy engine emits. Mirrored here as the
// wire-format single source: schemas cannot import policy (policy depends
// on schemas); the values are pinned equal by tests.
type PipelineOutcome string

// PipelineOutcomeValues is the frozen five-value policy-outcome vocabulary.
const (
	PipelineOutcomeIgnore  PipelineOutcome = "ignore"
	PipelineOutcomeSuggest PipelineOutcome = "suggest"
	PipelineOutcomeAuto    PipelineOutcome = "auto"
	PipelineOutcomeBlocked PipelineOutcome = "blocked"
	PipelineOutcomeReview  PipelineOutcome = "review"
)

// PipelineOutcomeValues lists all pipeline outcomes in canonical order
// (PIPELINE_OUTCOME_VALUES).
var PipelineOutcomeValues = []PipelineOutcome{
	PipelineOutcomeIgnore, PipelineOutcomeSuggest, PipelineOutcomeAuto,
	PipelineOutcomeBlocked, PipelineOutcomeReview,
}

// IsValidPipelineOutcome reports membership in PIPELINE_OUTCOME_VALUES.
func IsValidPipelineOutcome(s string) bool {
	for _, v := range PipelineOutcomeValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// PipelineRouting is the frozen Jev Stage-1 routing vocabulary carried
// through to the run outcome (PIPELINE_ROUTING).
type PipelineRouting string

// PipelineRoutingValues is the frozen Stage-1 routing vocabulary.
const (
	PipelineRoutingIgnore   PipelineRouting = "IGNORE"
	PipelineRoutingReview   PipelineRouting = "REVIEW"
	PipelineRoutingContinue PipelineRouting = "CONTINUE"
)

// PipelineRoutingValues lists all routing values in canonical order
// (PIPELINE_ROUTING_VALUES).
var PipelineRoutingValues = []PipelineRouting{
	PipelineRoutingIgnore, PipelineRoutingReview, PipelineRoutingContinue,
}

// IsValidPipelineRouting reports membership in PIPELINE_ROUTING_VALUES.
func IsValidPipelineRouting(s string) bool {
	for _, v := range PipelineRoutingValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// PipelineMaxAction is the frozen per-candidate ceiling — a semantic-only
// candidate can never route to auto (PIPELINE_MAX_ACTION).
type PipelineMaxAction string

// PipelineMaxActionValues is the frozen per-candidate ceiling vocabulary.
const (
	PipelineMaxActionReview PipelineMaxAction = "review"
	PipelineMaxActionAuto   PipelineMaxAction = "auto"
)

// PipelineMaxActionValues lists all max-action values in canonical order
// (PIPELINE_MAX_ACTION_VALUES).
var PipelineMaxActionValues = []PipelineMaxAction{PipelineMaxActionReview, PipelineMaxActionAuto}

// IsValidPipelineMaxAction reports membership in PIPELINE_MAX_ACTION_VALUES.
func IsValidPipelineMaxAction(s string) bool {
	for _, v := range PipelineMaxActionValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// BaselineRun is the frozen baseline-resolution vocabulary: the core
// baseline ladder (`exact-sha | same-tree | ancestor | patch-id-match |
// orphaned-suspect`) plus `none` for documents without a usable baseline
// (BASELINE_RUN).
type BaselineRun string

// BaselineRunValues is the frozen baseline-resolution ladder vocabulary.
const (
	BaselineRunExactSHA        BaselineRun = "exact-sha"
	BaselineRunSameTree        BaselineRun = "same-tree"
	BaselineRunAncestor        BaselineRun = "ancestor"
	BaselineRunPatchIDMatch    BaselineRun = "patch-id-match"
	BaselineRunOrphanedSuspect BaselineRun = "orphaned-suspect"
	BaselineRunNone            BaselineRun = "none"
)

// BaselineRunValues lists all baseline rungs in canonical order
// (BASELINE_RUN_VALUES).
var BaselineRunValues = []BaselineRun{
	BaselineRunExactSHA, BaselineRunSameTree, BaselineRunAncestor,
	BaselineRunPatchIDMatch, BaselineRunOrphanedSuspect, BaselineRunNone,
}

// IsValidBaselineRun reports membership in BASELINE_RUN_VALUES.
func IsValidBaselineRun(s string) bool {
	for _, v := range BaselineRunValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// DegradationKind is one ARCH §3.4 degradation-matrix entry plus the
// bounded-loop overflow and the A10 analyzer→path-level downgrade
// (DEGRADATION_KIND). `content-unavailable` = target doc content
// unreadable at the run's head — the writer is skipped, review + evidence.
// `signal-sink-failed` = early-signal/review-sink write failure — not a
// model outage (m6).
type DegradationKind string

// DegradationKindValues is the frozen degradation matrix.
const (
	DegradationAnalyzerInconclusive DegradationKind = "analyzer-inconclusive"
	DegradationJevUnavailable       DegradationKind = "jev-unavailable"
	DegradationWriterTimeout        DegradationKind = "writer-timeout"
	DegradationContentUnavailable   DegradationKind = "content-unavailable"
	DegradationVerificationFailed   DegradationKind = "verification-failed"
	DegradationProviderOutage       DegradationKind = "provider-outage"
	DegradationSignalSinkFailed     DegradationKind = "signal-sink-failed"
	DegradationCapOverflow          DegradationKind = "cap-overflow"
	DegradationBudgetExceeded       DegradationKind = "budget-exceeded"
	DegradationCASConflictRetry     DegradationKind = "cas-conflict-retry"
)

// DegradationKindValues lists all degradation kinds in canonical order
// (DEGRADATION_KINDS).
var DegradationKindValues = []DegradationKind{
	DegradationAnalyzerInconclusive, DegradationJevUnavailable, DegradationWriterTimeout,
	DegradationContentUnavailable, DegradationVerificationFailed, DegradationProviderOutage,
	DegradationSignalSinkFailed, DegradationCapOverflow, DegradationBudgetExceeded,
	DegradationCASConflictRetry,
}

// IsValidDegradationKind reports membership in DEGRADATION_KINDS.
func IsValidDegradationKind(s string) bool {
	for _, v := range DegradationKindValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// PipelineStage is one pipeline stage record: the closed RunStageStatus
// vocabulary of `docsync.run/v1` plus per-stage latency (P5-T17 telemetry).
// `latency_ms` is null when the stage never measurably ran (e.g. `skipped`).
type PipelineStage struct {
	Stage     string         `json:"stage"`
	Status    RunStageStatus `json:"status"`
	LatencyMS *int           `json:"latency_ms"`
	Note      *string        `json:"note,omitempty"`
}

// Validate checks the stage against PIPELINE_STAGE_SCHEMA (strict).
func (s *PipelineStage) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(s.Stage, 1, []string{"stage"}))
	Collect(&issues, CheckEnum(string(s.Status), stringsOf(RunStageStatusValues), []string{"status"}))
	if s.LatencyMS != nil && *s.LatencyMS < 0 {
		Collect(&issues, &Issue{Path: []string{"latency_ms"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.Note != nil {
		Collect(&issues, RequiredString(*s.Note, 1, []string{"note"}))
	}
	return issues
}

// RunDocumentVerification is the per-document verification outcome —
// mirrors the registered `docsync.verification-report/v1` verdict and
// auto-eligibility fields the orchestrator copies out of the report
// (OD-R09 gate + D3-pinned routing); `patch_ref` pins the
// content-addressed proposal when one exists.
type RunDocumentVerification struct {
	Verdict          VerificationVerdict `json:"verdict"`
	AutoEligible     bool                `json:"auto_eligible"`
	AutoBlockReasons []string            `json:"auto_block_reasons"`
	PatchRef         *string             `json:"patch_ref,omitempty"`
}

// Validate checks the block against RUN_DOCUMENT_VERIFICATION_SCHEMA
// (strict).
func (v *RunDocumentVerification) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(v.Verdict), stringsOf(VerificationVerdictValues), []string{"verdict"}))
	for i, r := range v.AutoBlockReasons {
		Collect(&issues, CheckEnum(r, AutoBlockReasonValues, []string{"auto_block_reasons", itoa(i)}))
	}
	if v.PatchRef != nil {
		Collect(&issues, RequiredString(*v.PatchRef, 1, []string{"patch_ref"}))
	}
	return issues
}

// RunDocumentAbstain is the per-document abstain record — canonical
// decision abstain taxonomy (ARCH §5.2; AbstainReasonValues).
type RunDocumentAbstain struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// Validate checks the record against RUN_DOCUMENT_ABSTAIN_SCHEMA (strict).
func (a *RunDocumentAbstain) Validate() []Issue {
	var issues []Issue
	// The canonical full union — decision-level AND operational reasons,
	// since run-level abstains also carry gateway-degradation records.
	Collect(&issues, CheckEnum(a.Reason, stringsOf(AbstainReasonValues), []string{"reason"}))
	Collect(&issues, RequiredString(a.Detail, 1, []string{"detail"}))
	return issues
}

// RunDocument is the per-document outcome record. `policy_outcome` is the
// policy engine's final outcome (never the Stage-1 routing action);
// `abstain` carries the evidence-backed abstention when the run refused to
// guess (abstain, don't guess — invariant 6); `partial_coverage` marks
// capped candidate fan-out (refinement rule 3); `baseline_run` records the
// baseline-ladder rung the document's baseline resolved to (or `none`).
type RunDocument struct {
	DocPath         string                   `json:"doc_path"`
	PolicyOutcome   PipelineOutcome          `json:"policy_outcome"`
	DecisionID      *string                  `json:"decision_id,omitempty"`
	Confidence      *float64                 `json:"confidence,omitempty"`
	Routing         *PipelineRouting         `json:"routing,omitempty"`
	Verification    *RunDocumentVerification `json:"verification,omitempty"`
	Abstain         *RunDocumentAbstain      `json:"abstain,omitempty"`
	PartialCoverage bool                     `json:"partial_coverage"`
	BaselineRun     *BaselineRun             `json:"baseline_run,omitempty"`
}

// Validate checks the record against RUN_DOCUMENT_SCHEMA (strict).
func (d *RunDocument) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(d.DocPath, 1, []string{"doc_path"}))
	Collect(&issues, CheckEnum(string(d.PolicyOutcome), stringsOf(PipelineOutcomeValues), []string{"policy_outcome"}))
	if d.DecisionID != nil {
		Collect(&issues, RequiredString(*d.DecisionID, 1, []string{"decision_id"}))
	}
	if d.Confidence != nil {
		if *d.Confidence < 0 {
			Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be greater than or equal to 0"})
		}
		if *d.Confidence > 1 {
			Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be less than or equal to 1"})
		}
	}
	if d.Routing != nil {
		Collect(&issues, CheckEnum(string(*d.Routing), stringsOf(PipelineRoutingValues), []string{"routing"}))
	}
	if d.Verification != nil {
		for _, iss := range d.Verification.Validate() {
			iss.Path = append([]string{"verification"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if d.Abstain != nil {
		for _, iss := range d.Abstain.Validate() {
			iss.Path = append([]string{"abstain"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if d.BaselineRun != nil {
		Collect(&issues, CheckEnum(string(*d.BaselineRun), stringsOf(BaselineRunValues), []string{"baseline_run"}))
	}
	return issues
}

// RunAbstain is the run-level abstain record (first-class, ARCH §5.2 /
// ARCH §10.6): the abstain queue integration keys on `abstain_id`;
// `queue_event_id` links the enqueued review event; `doc_path` is null for
// run-scoped abstentions that cannot be attributed to a single document.
type RunAbstain struct {
	AbstainID    string  `json:"abstain_id"`
	DocPath      *string `json:"doc_path"`
	Reason       string  `json:"reason"`
	Detail       string  `json:"detail"`
	DecisionID   *string `json:"decision_id,omitempty"`
	QueueEventID *string `json:"queue_event_id,omitempty"`
}

// Validate checks the record against RUN_ABSTAIN_SCHEMA (strict).
func (a *RunAbstain) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(a.AbstainID, 1, []string{"abstain_id"}))
	if a.DocPath != nil {
		Collect(&issues, RequiredString(*a.DocPath, 1, []string{"doc_path"}))
	}
	Collect(&issues, CheckEnum(a.Reason, stringsOf(AbstainReasonValues), []string{"reason"}))
	Collect(&issues, RequiredString(a.Detail, 1, []string{"detail"}))
	if a.DecisionID != nil {
		Collect(&issues, RequiredString(*a.DecisionID, 1, []string{"decision_id"}))
	}
	if a.QueueEventID != nil {
		Collect(&issues, RequiredString(*a.QueueEventID, 1, []string{"queue_event_id"}))
	}
	return issues
}

// RunCandidateLevelEvidence is the closed structured per-level retrieval
// evidence union (P11-T04, ARCH §4.5): MANDATORY on levels 3–4 provenance
// entries, absent on the other levels. Symbol evidence carries the changed
// symbol names; API/schema evidence carries the matched OpenAPI delta
// locations and/or config-diff keys (verbatim engine outputs). The two
// branches are mutually exclusive and both are strict — unknown shapes
// are rejected (fail closed).
type RunCandidateLevelEvidence struct {
	Symbols   []string `json:"symbols,omitempty"`
	Locations []string `json:"locations,omitempty"`
	Keys      []string `json:"keys,omitempty"`
}

// Validate checks the evidence against
// RUN_CANDIDATE_LEVEL_EVIDENCE_SCHEMA (the closed union, strict).
func (e *RunCandidateLevelEvidence) Validate() []Issue {
	var issues []Issue
	if e.Symbols != nil {
		// Branch 1: symbol evidence — nonempty symbol list, no other keys.
		if len(e.Symbols) < 1 {
			Collect(&issues, &Issue{Path: []string{"symbols"}, Message: "Array must contain at least 1 element(s)"})
		}
		for i, s := range e.Symbols {
			Collect(&issues, RequiredString(s, 1, []string{"symbols", itoa(i)}))
		}
		if e.Locations != nil || e.Keys != nil {
			Collect(&issues, &Issue{Path: []string{}, Message: "Invalid input"})
		}
		return issues
	}
	// Branch 2: locations + keys — both keys present (arrays may be empty).
	if e.Locations == nil || e.Keys == nil {
		Collect(&issues, &Issue{Path: []string{}, Message: "Invalid input"})
		return issues
	}
	for i, s := range e.Locations {
		Collect(&issues, RequiredString(s, 1, []string{"locations", itoa(i)}))
	}
	for i, s := range e.Keys {
		Collect(&issues, RequiredString(s, 1, []string{"keys", itoa(i)}))
	}
	return issues
}

// RunCandidateProvenance is one retrieval-provenance entry for a candidate
// document: which retrieval level produced it, the named source of the
// hit, the retrieval score and whether the anchor was stale at retrieval
// time (mirrors the @docsync/core retrieval provenance semantics; levels
// align with the V2/V3 retrieval tiers).
type RunCandidateProvenance struct {
	Level    int                        `json:"level"`
	Source   string                     `json:"source"`
	Score    *float64                   `json:"score,omitempty"`
	Stale    *bool                      `json:"stale,omitempty"`
	Evidence *RunCandidateLevelEvidence `json:"evidence,omitempty"`
}

// Validate checks the entry against RUN_CANDIDATE_PROVENANCE_SCHEMA
// (strict).
func (p *RunCandidateProvenance) Validate() []Issue {
	var issues []Issue
	if p.Level < 0 {
		Collect(&issues, &Issue{Path: []string{"level"}, Message: "Number must be greater than or equal to 0"})
	}
	Collect(&issues, RequiredString(p.Source, 1, []string{"source"}))
	if p.Evidence != nil {
		for _, iss := range p.Evidence.Validate() {
			iss.Path = append([]string{"evidence"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// RunCandidate is the candidate mapping: retrieval levels + provenance for
// one candidate document.
type RunCandidate struct {
	DocPath      string                   `json:"doc_path"`
	Levels       []int                    `json:"levels"`
	Provenance   []RunCandidateProvenance `json:"provenance"`
	SemanticOnly bool                     `json:"semantic_only"`
	MaxAction    PipelineMaxAction        `json:"max_action"`
}

// Validate checks the candidate against RUN_CANDIDATE_SCHEMA (strict).
func (c *RunCandidate) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.DocPath, 1, []string{"doc_path"}))
	for i, l := range c.Levels {
		if l < 0 {
			Collect(&issues, &Issue{Path: []string{"levels", itoa(i)}, Message: "Number must be greater than or equal to 0"})
		}
	}
	for i := range c.Provenance {
		for _, iss := range c.Provenance[i].Validate() {
			iss.Path = append([]string{"provenance", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	Collect(&issues, CheckEnum(string(c.MaxAction), stringsOf(PipelineMaxActionValues), []string{"max_action"}))
	return issues
}

// RunModelBinding is one role binding pin (D3 + DX-P2-2): the
// actually-routed `model` — never the role's configured default — and the
// routed `provider` when OpenRouter telemetry carries it. Roles reuse
// RequiredModelRoles (state.go).
type RunModelBinding struct {
	Role     string  `json:"role"`
	Model    string  `json:"model"`
	Provider *string `json:"provider,omitempty"`
}

// Validate checks the binding against RUN_MODEL_BINDING_SCHEMA (strict).
func (b *RunModelBinding) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(b.Role, RequiredModelRoles, []string{"role"}))
	Collect(&issues, RequiredString(b.Model, 1, []string{"model"}))
	if b.Provider != nil {
		Collect(&issues, RequiredString(*b.Provider, 1, []string{"provider"}))
	}
	return issues
}

// RunProvenance is the run provenance: policy version, FULL per-role model
// bindings (D3 + DX-P2-2) and the writer/judge session ids for replay —
// session ids are pinned when the composition layer supplies them
// (Phase 7/8); the run-level orchestrator leaves them unset (m4).
type RunProvenance struct {
	PolicyVersion   int               `json:"policy_version"`
	ModelBindings   []RunModelBinding `json:"model_bindings"`
	WriterSessionID *string           `json:"writer_session_id,omitempty"`
	JudgeSessionID  *string           `json:"judge_session_id,omitempty"`
}

// Validate checks the block against RUN_PROVENANCE_SCHEMA (strict).
func (p *RunProvenance) Validate() []Issue {
	var issues []Issue
	if p.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	for i := range p.ModelBindings {
		for _, iss := range p.ModelBindings[i].Validate() {
			iss.Path = append([]string{"model_bindings", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if p.WriterSessionID != nil {
		Collect(&issues, RequiredString(*p.WriterSessionID, 1, []string{"writer_session_id"}))
	}
	if p.JudgeSessionID != nil {
		Collect(&issues, RequiredString(*p.JudgeSessionID, 1, []string{"judge_session_id"}))
	}
	return issues
}

// Degradation is one degradation record (fail-closed bookkeeping for
// degraded runs).
type Degradation struct {
	Stage  string          `json:"stage"`
	Kind   DegradationKind `json:"kind"`
	Detail string          `json:"detail"`
}

// Validate checks the record against DEGRADATION_SCHEMA (strict).
func (d *Degradation) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(d.Stage, 1, []string{"stage"}))
	Collect(&issues, CheckEnum(string(d.Kind), stringsOf(DegradationKindValues), []string{"kind"}))
	Collect(&issues, RequiredString(d.Detail, 1, []string{"detail"}))
	return issues
}

// RunFinding is one run-level finding: an outcome that is neither a patch
// nor an abstention — e.g. the writer-timeout "potentially stale" record
// without a patch. `doc_path` is null for run-scoped findings; `kind` is a
// stable kebab-case tag (free string at the schema level, pinned by
// emitter tests).
type RunFinding struct {
	DocPath  *string `json:"doc_path"`
	Kind     string  `json:"kind"`
	Detail   string  `json:"detail"`
	PatchRef *string `json:"patch_ref,omitempty"`
}

// Validate checks the record against RUN_FINDING_SCHEMA (strict).
func (f *RunFinding) Validate() []Issue {
	var issues []Issue
	if f.DocPath != nil {
		Collect(&issues, RequiredString(*f.DocPath, 1, []string{"doc_path"}))
	}
	Collect(&issues, RequiredString(f.Kind, 1, []string{"kind"}))
	Collect(&issues, RequiredString(f.Detail, 1, []string{"detail"}))
	if f.PatchRef != nil {
		Collect(&issues, RequiredString(*f.PatchRef, 1, []string{"patch_ref"}))
	}
	return issues
}

// EarlySignal carries the early-signal lifecycle timestamps (P5-T11) —
// fields, never generated.
type EarlySignal struct {
	PostedAt  *string `json:"posted_at"`
	UpdatedAt *string `json:"updated_at"`
}

// Validate checks the block against EARLY_SIGNAL_SCHEMA (strict).
func (e *EarlySignal) Validate() []Issue {
	var issues []Issue
	Collect(&issues, optionalDatetimeIssue(e.PostedAt, []string{"posted_at"}))
	Collect(&issues, optionalDatetimeIssue(e.UpdatedAt, []string{"updated_at"}))
	return issues
}

// RunUsage is the per-run usage attribution (P5-T17; ARCH §10.6): the
// decision-stage token usage threaded through the decision telemetry,
// pinned to the `policy_version` that produced it — usage/cost provenance,
// never a guess. `total_tokens`/`cost` are optional because the gateway
// may omit them.
type RunUsage struct {
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	TotalTokens      *int     `json:"total_tokens,omitempty"`
	Cost             *float64 `json:"cost,omitempty"`
	PolicyVersion    int      `json:"policy_version"`
}

// Validate checks the block against RUN_USAGE_SCHEMA (strict).
func (u *RunUsage) Validate() []Issue {
	var issues []Issue
	if u.PromptTokens < 0 {
		Collect(&issues, &Issue{Path: []string{"prompt_tokens"}, Message: "Number must be greater than or equal to 0"})
	}
	if u.CompletionTokens < 0 {
		Collect(&issues, &Issue{Path: []string{"completion_tokens"}, Message: "Number must be greater than or equal to 0"})
	}
	if u.TotalTokens != nil && *u.TotalTokens < 0 {
		Collect(&issues, &Issue{Path: []string{"total_tokens"}, Message: "Number must be greater than or equal to 0"})
	}
	if u.Cost != nil && *u.Cost < 0 {
		Collect(&issues, &Issue{Path: []string{"cost"}, Message: "Number must be greater than or equal to 0"})
	}
	if u.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// RunOutcomeSchemaID is the literal schema tag `docsync.run-outcome/v1`.
const RunOutcomeSchemaID = "docsync.run-outcome/v1"

// RunOutcome is the FROZEN `docsync.run-outcome/v1` single-run pipeline
// API (RUN_OUTCOME_SCHEMA). Field order matches the TS declaration order
// so serialized bytes stay byte-stable with the TypeScript emitter.
type RunOutcome struct {
	Schema           string          `json:"schema"`
	RunID            string          `json:"run_id"`
	Key              RunKey          `json:"key"`
	StartedAt        *string         `json:"started_at"`
	FinishedAt       *string         `json:"finished_at"`
	Status           RunStatus       `json:"status"`
	Stages           []PipelineStage `json:"stages"`
	Documents        []RunDocument   `json:"documents"`
	Abstains         []RunAbstain    `json:"abstains"`
	Candidates       []RunCandidate  `json:"candidates"`
	UnknownCoverage  []string        `json:"unknown_coverage,omitempty"`
	Provenance       RunProvenance   `json:"provenance"`
	Usage            *RunUsage       `json:"usage,omitempty"`
	Degradations     []Degradation   `json:"degradations"`
	Findings         []RunFinding    `json:"findings"`
	EarlySignal      EarlySignal     `json:"early_signal"`
	ConcurrencyGroup *string         `json:"concurrency_group"`
	StateChurn       StateChurn      `json:"state_churn"`
}

// Validate checks the run against RUN_OUTCOME_SCHEMA (strict) and applies
// the superRefine consistency rules 1–3 (V-P5-16). The refinement rules
// run only when the field-level checks pass, mirroring zod, where
// refinements do not run on an aborted (already invalid) parse.
func (r *RunOutcome) Validate() []Issue {
	var issues []Issue
	if r.Schema != RunOutcomeSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.run-outcome/v1"`})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	for _, iss := range r.Key.Validate() {
		iss.Path = append([]string{"key"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, optionalDatetimeIssue(r.StartedAt, []string{"started_at"}))
	Collect(&issues, optionalDatetimeIssue(r.FinishedAt, []string{"finished_at"}))
	Collect(&issues, CheckEnum(string(r.Status), stringsOf(RunStatusValues), []string{"status"}))
	for i := range r.Stages {
		for _, iss := range r.Stages[i].Validate() {
			iss.Path = append([]string{"stages", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Documents {
		for _, iss := range r.Documents[i].Validate() {
			iss.Path = append([]string{"documents", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Abstains {
		for _, iss := range r.Abstains[i].Validate() {
			iss.Path = append([]string{"abstains", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Candidates {
		for _, iss := range r.Candidates[i].Validate() {
			iss.Path = append([]string{"candidates", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i, p := range r.UnknownCoverage {
		Collect(&issues, RequiredString(p, 1, []string{"unknown_coverage", itoa(i)}))
	}
	for _, iss := range r.Provenance.Validate() {
		iss.Path = append([]string{"provenance"}, iss.Path...)
		issues = append(issues, iss)
	}
	if r.Usage != nil {
		for _, iss := range r.Usage.Validate() {
			iss.Path = append([]string{"usage"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Degradations {
		for _, iss := range r.Degradations[i].Validate() {
			iss.Path = append([]string{"degradations", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Findings {
		for _, iss := range r.Findings[i].Validate() {
			iss.Path = append([]string{"findings", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range r.EarlySignal.Validate() {
		iss.Path = append([]string{"early_signal"}, iss.Path...)
		issues = append(issues, iss)
	}
	if r.ConcurrencyGroup != nil {
		Collect(&issues, RequiredString(*r.ConcurrencyGroup, 1, []string{"concurrency_group"}))
	}
	for _, iss := range r.StateChurn.Validate() {
		iss.Path = append([]string{"state_churn"}, iss.Path...)
		issues = append(issues, iss)
	}

	if len(issues) > 0 {
		return issues
	}

	// Rule 1 (V-P5-16): a completed run IS finished — finished_at must be set.
	if r.Status == RunStatusCompleted && r.FinishedAt == nil {
		Collect(&issues, &Issue{
			Path:    []string{"finished_at"},
			Message: "status 'completed' requires a non-null finished_at (a completed run is finished; V-P5-16)",
		})
	}

	// Rule 2 (V-P5-16): a completed run never leaves a document in limbo —
	// every document record must be accounted for by a verification block,
	// an abstain record, a matching finding, or a terminal policy_outcome.
	if r.Status == RunStatusCompleted {
		for i := range r.Documents {
			doc := &r.Documents[i]
			hasFinding := false
			for fi := range r.Findings {
				if r.Findings[fi].DocPath != nil && *r.Findings[fi].DocPath == doc.DocPath {
					hasFinding = true
					break
				}
			}
			accounted := doc.Verification != nil ||
				doc.Abstain != nil ||
				hasFinding ||
				doc.PolicyOutcome == PipelineOutcomeIgnore ||
				doc.PolicyOutcome == PipelineOutcomeReview
			if !accounted {
				Collect(&issues, &Issue{
					Path: []string{"documents", itoa(i)},
					Message: "completed run: document '" + doc.DocPath + "' needs a verification block, an abstain " +
						"record, a matching finding, or policy_outcome 'ignore'/'review' (V-P5-16)",
				})
			}
		}
	}

	// Rule 3 (V-P5-16): capped coverage is a degradation, never a silent pass.
	partial := false
	for i := range r.Documents {
		if r.Documents[i].PartialCoverage {
			partial = true
			break
		}
	}
	if partial {
		hasCap := false
		for i := range r.Degradations {
			if r.Degradations[i].Kind == DegradationCapOverflow {
				hasCap = true
				break
			}
		}
		if !hasCap {
			Collect(&issues, &Issue{
				Path:    []string{"degradations"},
				Message: "partial_coverage document requires a degradations entry with kind 'cap-overflow' (capped coverage is a degradation, never a silent full pass; V-P5-16)",
			})
		}
	}

	return issues
}

// ParseRunOutcome strictly decodes and validates `docsync.run-outcome/v1`
// bytes (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseRunOutcome(data []byte) (*RunOutcome, error) {
	var r RunOutcome
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// RunOutcomeExample mirrors RUN_OUTCOME_EXAMPLE (registered as
// 'docsync.run-outcome' v1; golden data for byte-for-byte serializer
// checks — the FROZEN wire format).
func RunOutcomeExample() RunOutcome {
	startedAt := "2026-10-04T22:30:41Z"
	finishedAt := "2026-10-04T22:30:47Z"
	decisionID := "d_01J9ZK7Q2M"
	confidence := 0.96
	routingContinue := PipelineRoutingContinue
	verification := RunDocumentVerification{
		Verdict:          VerdictPass,
		AutoEligible:     true,
		AutoBlockReasons: []string{},
		PatchRef:         strPtr("state/docs/payments.md#proposal-d_01J9ZK7Q2M"),
	}
	exactSHA := BaselineRunExactSHA
	apiConfidence := 0.31
	routingIgnore := PipelineRoutingIgnore
	sameTree := BaselineRunSameTree
	cliDecisionID := "d_01J9ZK7Q2N"
	cliConfidence := 0.66
	routingReview := PipelineRoutingReview
	cliAbstain := RunDocumentAbstain{
		Reason: "insufficient_confidence",
		Detail: "documentation_impact probability 0.66 sits below the review threshold (0.72)",
	}
	patchIDMatch := BaselineRunPatchIDMatch
	queueEventID := "q_01J9ZK8A0P"
	writerSessionID := "sess_writer_01J9ZK7Q2M"
	judgeSessionID := "sess_judge_01J9ZK7Q2M"
	postedAt := "2026-10-04T22:30:44Z"
	updatedAt := "2026-10-04T22:30:47Z"
	concurrencyGroup := "docsync-acme/payments-42"
	return RunOutcome{
		Schema:     "docsync.run-outcome/v1",
		RunID:      "run_01J9ZK6X8A",
		Key:        RunKeyExample(),
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		Status:     RunStatusCompleted,
		Stages: []PipelineStage{
			{Stage: "change-analysis", Status: RunStageOk, LatencyMS: intPtr(310)},
			{Stage: "decision", Status: RunStageOk, LatencyMS: intPtr(740)},
			{Stage: "writer", Status: RunStageOk, LatencyMS: intPtr(910)},
			{Stage: "verification", Status: RunStageOk, LatencyMS: intPtr(1420)},
		},
		Documents: []RunDocument{
			{
				DocPath:         "docs/payments.md",
				PolicyOutcome:   PipelineOutcomeAuto,
				DecisionID:      &decisionID,
				Confidence:      &confidence,
				Routing:         &routingContinue,
				Verification:    &verification,
				PartialCoverage: false,
				BaselineRun:     &exactSHA,
			},
			{
				DocPath:         "docs/api.md",
				PolicyOutcome:   PipelineOutcomeIgnore,
				Confidence:      &apiConfidence,
				Routing:         &routingIgnore,
				PartialCoverage: false,
				BaselineRun:     &sameTree,
			},
			{
				DocPath:         "docs/cli.md",
				PolicyOutcome:   PipelineOutcomeReview,
				DecisionID:      &cliDecisionID,
				Confidence:      &cliConfidence,
				Routing:         &routingReview,
				Abstain:         &cliAbstain,
				PartialCoverage: false,
				BaselineRun:     &patchIDMatch,
			},
		},
		Abstains: []RunAbstain{
			{
				AbstainID:    "ab_01J9ZK7Q2N",
				DocPath:      strPtr("docs/cli.md"),
				Reason:       "insufficient_confidence",
				Detail:       "routed to the human-review queue with evidence attached (ARCH §5.2)",
				DecisionID:   &cliDecisionID,
				QueueEventID: &queueEventID,
			},
		},
		Candidates: []RunCandidate{
			{
				DocPath: "docs/payments.md",
				Levels:  []int{1, 2},
				Provenance: []RunCandidateProvenance{
					{Level: 1, Source: "path-level", Score: floatPtr(0.92)},
					{Level: 2, Source: "symbol", Score: floatPtr(0.88), Stale: boolPtr(false)},
				},
				SemanticOnly: false,
				MaxAction:    PipelineMaxActionAuto,
			},
		},
		Provenance: RunProvenance{
			PolicyVersion: 17,
			ModelBindings: []RunModelBinding{
				{Role: "decision-engine", Model: "z-ai/glm-4.7", Provider: strPtr("z-ai")},
				{Role: "judge", Model: "z-ai/glm-4.7", Provider: strPtr("z-ai")},
				{Role: "writer", Model: "anthropic/claude-sonnet-4.5", Provider: strPtr("anthropic")},
			},
			WriterSessionID: &writerSessionID,
			JudgeSessionID:  &judgeSessionID,
		},
		Degradations: []Degradation{},
		Findings:     []RunFinding{},
		EarlySignal: EarlySignal{
			PostedAt:  &postedAt,
			UpdatedAt: &updatedAt,
		},
		ConcurrencyGroup: &concurrencyGroup,
		StateChurn:       StateChurn{Added: 1, Updated: 2, Unchanged: 19},
	}
}

// RunOutcomeDegradedExample mirrors RUN_OUTCOME_DEGRADED_EXAMPLE: the
// tree-sitter analyzer failed (declared `analyzer-inconclusive` —
// degradation is declared, never silent), the candidate fan-out capped
// (`cap-overflow` ⇔ the partial_coverage document, refinement rule 3) and
// the writer timed out (`writer-timeout` ⇒ the "potentially stale" finding
// without a patch); verification skipped with null latency. Every closed
// vocabulary stays intact under degradation.
func RunOutcomeDegradedExample() RunOutcome {
	startedAt := "2026-10-04T22:31:12Z"
	finishedAt := "2026-10-04T22:31:19Z"
	confidence := 0.41
	routingReview := PipelineRoutingReview
	ancestor := BaselineRunAncestor
	stale := true
	postedAt := "2026-10-04T22:31:16Z"
	updatedAt := "2026-10-04T22:31:19Z"
	concurrencyGroup := "docsync-acme/payments-42"
	return RunOutcome{
		Schema:     "docsync.run-outcome/v1",
		RunID:      "run_01J9ZK6X8B",
		Key:        RunKeyExample(),
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		Status:     RunStatusDegraded,
		Stages: []PipelineStage{
			{
				Stage:     "change-analysis",
				Status:    RunStageInconclusive,
				LatencyMS: intPtr(480),
				Note:      strPtr("tree-sitter analyzer failed — path-level degradation declared (never silent)"),
			},
			{Stage: "decision", Status: RunStageOk, LatencyMS: intPtr(810)},
			{
				Stage:     "writer",
				Status:    RunStageFailed,
				LatencyMS: intPtr(30000),
				Note:      strPtr("writer exceeded its 30s cap — finding emitted, no patch proposed"),
			},
			{
				Stage:     "verification",
				Status:    RunStageSkipped,
				LatencyMS: nil,
				Note:      strPtr("no patch to verify"),
			},
		},
		Documents: []RunDocument{
			{
				DocPath:         "docs/deploy.md",
				PolicyOutcome:   PipelineOutcomeReview,
				Confidence:      &confidence,
				Routing:         &routingReview,
				PartialCoverage: true,
				BaselineRun:     &ancestor,
			},
		},
		Abstains: []RunAbstain{},
		Candidates: []RunCandidate{
			{
				DocPath: "docs/deploy.md",
				Levels:  []int{0, 1},
				Provenance: []RunCandidateProvenance{
					{Level: 0, Source: "semantic", Score: floatPtr(0.71), Stale: &stale},
					{Level: 1, Source: "path-level", Score: floatPtr(0.64)},
				},
				SemanticOnly: false,
				MaxAction:    PipelineMaxActionReview,
			},
		},
		Provenance: RunProvenance{
			PolicyVersion: 17,
			ModelBindings: []RunModelBinding{
				{Role: "decision-engine", Model: "z-ai/glm-4.7", Provider: strPtr("z-ai")},
				{Role: "writer", Model: "anthropic/claude-sonnet-4.5", Provider: strPtr("anthropic")},
			},
		},
		Degradations: []Degradation{
			{
				Stage:  "change-analysis",
				Kind:   DegradationAnalyzerInconclusive,
				Detail: "tree-sitter analyzer failed — degraded to path-level matching (A10 downgrade)",
			},
			{
				Stage:  "decision",
				Kind:   DegradationCapOverflow,
				Detail: "candidate fan-out capped; docs/deploy.md evaluated partially (partial_coverage)",
			},
			{
				Stage:  "writer",
				Kind:   DegradationWriterTimeout,
				Detail: "writer exceeded its 30s cap — potentially-stale finding emitted without a patch",
			},
		},
		Findings: []RunFinding{
			{
				DocPath: strPtr("docs/deploy.md"),
				Kind:    "potentially-stale",
				Detail:  "writer timed out after the drift signal — the document is potentially stale; no patch proposed",
			},
		},
		EarlySignal: EarlySignal{
			PostedAt:  &postedAt,
			UpdatedAt: &updatedAt,
		},
		ConcurrencyGroup: &concurrencyGroup,
		StateChurn:       StateChurn{Added: 0, Updated: 1, Unchanged: 21},
	}
}

// RunKeyExample is the shared run key of the run-outcome examples
// (identical to the RUN_RECORD_EXAMPLE key: acme/payments, 8a73f2c → 81af92e,
// manifest 1, policy 17).
func RunKeyExample() RunKey {
	return RunKey{
		Repo:            "acme/payments",
		BaseSHA:         "8a73f2c",
		HeadSHA:         "81af92e",
		ManifestVersion: 1,
		PolicyVersion:   17,
	}
}

// intPtr / floatPtr / boolPtr return pointers to literals (example helpers).
func intPtr(i int) *int           { return &i }
func floatPtr(f float64) *float64 { return &f }
func boolPtr(b bool) *bool        { return &b }
