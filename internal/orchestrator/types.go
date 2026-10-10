// Ported from packages/core/src/orchestrator/types.ts (P5-T01 part 1; PLAN
// §6.4; ARCH §3.5, §13).
//
// The orchestrator is a DETERMINISTIC stage machine (ARCH §13): it owns the
// control flow, the budgets, and the persistence boundary, while every LLM
// stage is a bounded leaf port. The ports below are STRUCTURAL (Go interfaces
// with typed payloads) so internal/policy, internal/openrouter and
// internal/verifier satisfy them externally through thin wave-end adapters —
// this package imports neither (OD-1 package topology).
//
// Everything here is pure data + pure helpers: no fs, no git, no network, no
// `new Date()` — time is always the injected `clock` (run.go consumes this
// contract; provenance records the stage telemetry verbatim).

package orchestrator

import (
	"errors"
	"fmt"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

/* -------------------------------------------------------------- stage names */

// PipelineStages is the exact ordered pipeline stages (PLAN §6.4): analyze →
// map → invalidate → jev → retrieval → writer → verification → policy →
// persist. Frozen: no stage can be appended/removed at runtime.
var PipelineStages = []StageName{
	StageAnalyze,
	StageMap,
	StageInvalidate,
	StageJev,
	StageRetrieval,
	StageWriter,
	StageVerification,
	StagePolicy,
	StagePersist,
}

// StageName is one pipeline stage name (element of the frozen PipelineStages).
type StageName string

// The frozen stage names, in canonical order.
const (
	StageAnalyze      StageName = "analyze"
	StageMap          StageName = "map"
	StageInvalidate   StageName = "invalidate"
	StageJev          StageName = "jev"
	StageRetrieval    StageName = "retrieval"
	StageWriter       StageName = "writer"
	StageVerification StageName = "verification"
	StagePolicy       StageName = "policy"
	StagePersist      StageName = "persist"
)

/* ------------------------------------------------------------------ routing */

// Routing is the JEV routing verdict carried through the run (P5-T06
// consumer). The wire vocabulary is owned by schemas (PipelineRouting — the
// run outcome carries it); the alias keeps OD-2 (one taxonomy, never
// duplicated).
type Routing = schemas.PipelineRouting

// The three routing values.
const (
	RoutingIgnore   = schemas.PipelineRoutingIgnore
	RoutingReview   = schemas.PipelineRoutingReview
	RoutingContinue = schemas.PipelineRoutingContinue
)

/* ------------------------------------------------------------- run context */

// PRContext is the PR context when running in the GitHub integration
// (P5-T08).
type PRContext struct {
	Number int `json:"number"`
}

// RunContext is the immutable per-run context handed to every stage (the
// runner builds it once; stages receive it read-only). Key is the run-record
// identity (RunKey) used for state-store addressing (ARCH §6.3); RunID is the
// human-readable `repo#base..head` run_id (ARCH §4.1). Clock is INJECTED — no
// stage may call a local time source.
type RunContext struct {
	// Repo is the repository identity (e.g. `org/name`).
	Repo string
	// RepoRoot is the absolute repository root.
	RepoRoot string
	// Base is the base ref/sha of the change window.
	Base string
	// Head is the head ref/sha of the change window.
	Head string
	// RunID is the human-readable run id (`repo#base..head`, ARCH §4.1).
	RunID string
	// Key is the run-record identity (state-store key, ARCH §6.3).
	Key schemas.RunKey
	// Manifest is the manifest allowlist for the run (D7).
	Manifest schemas.Manifest
	// Graph is the composed document graph (P1-T09).
	Graph graph.DocumentGraph
	// Caps are the deep-analysis caps in force (P1-T08).
	Caps analysis.AnalysisCaps
	// Budgets are the per-run pipeline budgets (this package).
	Budgets PipelineBudgets
	// Clock is the injected clock: returns the current ISO-8601 instant.
	Clock func() string
	// PR is the PR context when running in the GitHub integration (P5-T08).
	PR *PRContext
}

/* ----------------------------------------------------------------- budgets */

// PipelineBudgets are the per-run pipeline budgets (PLAN §6.4; ARCH §3.7
// bounding). Bootstrap data, labeled: BUDGET numbers, not decision thresholds
// (D11 calibration stays in the versioned policy). A nil StageBudgetMS entry
// (or an absent key) means an UNBOUNDED stage.
type PipelineBudgets struct {
	// MaxDocsInvestigated is the maximum number of candidate documents
	// investigated in one run.
	MaxDocsInvestigated int
	// MaxRelevanceDocs is the maximum number of change-touched mapped docs
	// admitted to the JEV relevance context (ARCH §3.3 bound; M3). Labeled
	// budget data, NOT D11 threshold calibration. Deliberately DISTINCT from
	// MaxDocsInvestigated: the relevance bound governs the graph→JEV edge
	// only, so the writer-stage doc-cap overflow branch stays reachable.
	MaxRelevanceDocs int
	// MaxPatchesPerRun is the maximum number of patches emitted in one run.
	MaxPatchesPerRun int
	// MaxConcurrentDocs is the in-process writer-pool cap (OD-R10; P5-T12):
	// the maximum number of writer calls the writer stage executes
	// concurrently. The pool is IN-PROCESS (no worker threads) — worker
	// isolation is reserved for A10 memory pressure — and results are applied
	// in candidate order, so this bounds concurrency only, never outcome
	// ordering.
	MaxConcurrentDocs int
	// StageBudgetMS is the per-stage wall-time budget in ms; a nil entry (or
	// an absent key) means unbounded.
	StageBudgetMS map[StageName]*int
}

// DefaultPipelineBudgets are the default budgets (PLAN §6.4): 20 docs
// investigated, 10 patches per run, writer pool capped at 4 concurrent
// in-process doc investigations (OD-R10; P5-T12), 100 relevance docs (M3: the
// graph→JEV change-touched-doc bound, separate from the writer DOC cap),
// analyze 5000ms, jev 1000ms, retrieval 1000ms; every other stage unbounded.
func DefaultPipelineBudgets() PipelineBudgets {
	return PipelineBudgets{
		MaxDocsInvestigated: 20,
		MaxRelevanceDocs:    100,
		MaxPatchesPerRun:    10,
		MaxConcurrentDocs:   4,
		StageBudgetMS: map[StageName]*int{
			StageAnalyze:      intPtr(5000),
			StageMap:          nil,
			StageInvalidate:   nil,
			StageJev:          intPtr(1000),
			StageRetrieval:    intPtr(1000),
			StageWriter:       nil,
			StageVerification: nil,
			StagePolicy:       nil,
			StagePersist:      nil,
		},
	}
}

// PipelineBudgetOverrides mirrors the TS `Partial<PipelineBudgets>` runner
// input: a nil member leaves the default in force (a zero int cannot express
// "unset" in Go, hence the pointers). StageBudgetMS keys behave like the TS
// spread: an ABSENT key keeps the default; a nil value sets that stage
// unbounded; a non-nil value overrides the budget.
type PipelineBudgetOverrides struct {
	MaxDocsInvestigated *int
	MaxRelevanceDocs    *int
	MaxPatchesPerRun    *int
	MaxConcurrentDocs   *int
	StageBudgetMS       map[StageName]*int
}

// MergePipelineBudgets merges the overrides over DEFAULT_PIPELINE_BUDGETS —
// the TS `{...DEFAULT_PIPELINE_BUDGETS, ...input.budgets}` with the per-stage
// map merge (absent key = keep default).
func MergePipelineBudgets(overrides *PipelineBudgetOverrides) PipelineBudgets {
	budgets := DefaultPipelineBudgets()
	if overrides == nil {
		return budgets
	}
	if overrides.MaxDocsInvestigated != nil {
		budgets.MaxDocsInvestigated = *overrides.MaxDocsInvestigated
	}
	if overrides.MaxRelevanceDocs != nil {
		budgets.MaxRelevanceDocs = *overrides.MaxRelevanceDocs
	}
	if overrides.MaxPatchesPerRun != nil {
		budgets.MaxPatchesPerRun = *overrides.MaxPatchesPerRun
	}
	if overrides.MaxConcurrentDocs != nil {
		budgets.MaxConcurrentDocs = *overrides.MaxConcurrentDocs
	}
	for stage, budget := range overrides.StageBudgetMS {
		budgets.StageBudgetMS[stage] = budget
	}
	return budgets
}

/* ----------------------------------------------------------- pipeline input */

// PipelineInput is the top-level orchestrator input (the P5-T11 runner
// input). Policy is intentionally `any` here: the versioned policy payload is
// parsed/validated by the policy-backed PolicyStagePort (P5-T08), not by this
// package — core must never interpret policy internals (D11, OD-1). Caps and
// Budgets are merged over the defaults by the runner.
type PipelineInput struct {
	// Repo is the repository identity (e.g. `org/name`).
	Repo string
	// RepoRoot is the absolute repository root.
	RepoRoot string
	// Base is the base ref/sha of the change window.
	Base string
	// Head is the head ref/sha of the change window.
	Head string
	// Manifest is the manifest allowlist for the run (D7).
	Manifest schemas.Manifest
	// Policy is the versioned policy payload (opaque to core; consumed by the
	// policy stage).
	Policy any
	// PR is the PR context when running in the GitHub integration (P5-T08).
	PR *PRContext
	// Caps are the deep-analysis caps; nil = the runner's default caps.
	Caps *analysis.AnalysisCaps
	// Budgets are the budget overrides merged over DEFAULT_PIPELINE_BUDGETS.
	Budgets *PipelineBudgetOverrides
	// Analyzers is the composed analyzer lineup injection (m8; the P5-T02
	// seam): the runner composes the real analyzer plugins here; nil → the
	// core bootstrap lineup (DefaultAnalyzerLineup).
	Analyzers []analysis.AnalyzerPlugin
	// Clock is the injected clock: returns the current ISO-8601 instant.
	Clock func() string
}

/* -------------------------------------------------------------------- ports */

// DecisionStagePort is the JEV decision-stage port (P5-T03; ARCH §3.5 —
// bounded leaf stage). The implementation (OpenRouter-backed) lives outside
// this package; this package only calls it.
type DecisionStagePort interface {
	Decide(input DecisionStageInput) (DecisionStageResult, error)
}

// DecisionStageInput is the decision-stage port input — the §3.3 DOCUMENT
// GRAPH → JEV edge payload (the run context plus the bounded relevance
// context). Analysis is read by the wave-end policy bridge adapter
// (internal/policy BridgeCoreDecisionStageInput) and is nil exactly when the
// analyzer was inconclusive.
type DecisionStageInput struct {
	Ctx       RunContext
	Relevance RelevanceContext
	Analysis  *schemas.ChangeAnalysis
}

// DecisionUsageSeed is the P5-T17 usage-attribution seed (OD-1 structural
// read): the decision-stage telemetry MAY carry the port's token usage. The
// mapped shape is validated by the RUN_OUTCOME_SCHEMA usage gate at
// self-validation (fail closed).
type DecisionUsageSeed struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      *int
	Cost             *float64
}

// StageTelemetry is the stage telemetry (P5-T15 provenance). Model/Provider
// carry the ACTUALLY ROUTED halves (D3/DX-P2-2), never configured defaults.
type StageTelemetry struct {
	LatencyMS int
	Model     *string
	Provider  *string
	// Usage is the port's usage block verbatim, present only when the route
	// actually reached the port (P5-T17).
	Usage *DecisionUsageSeed
}

// DecisionStageResult is the result of the JEV decision stage. Decision is
// nil exactly when the stage abstained (or routed IGNORE).
type DecisionStageResult struct {
	Decision      *schemas.Decision
	Abstained     bool
	AbstainReason *schemas.AbstainReason
	Telemetry     *StageTelemetry
}

// WorkspaceDocTarget is one document the writer stage may be asked to
// investigate (workspace target).
type WorkspaceDocTarget struct {
	// DocPath is the manifest document path.
	DocPath string
	// Mode is the mirrored ownership-floor mode (workspace/types.go).
	Mode workspace.WorkspaceMode
}

// WriterStagePort is the writer-stage port (P5-T06; ARCH §3.5, §4.7 — Sonnet
// patch-writer leaf). Closed result union: exactly one of patch / finding /
// abstain / noop. The input carries the §3.3 RETRIEVAL→SONNET workspace
// ingredients (application-assembled, V-P5-14).
type WriterStagePort interface {
	Write(input WriterStageInput) (WriterStageResult, error)
}

// WriterStageInput is the writer-stage port input. Analysis is null only when
// the analyzer was inconclusive; Decision is the Stage-1 Jev decision (the
// writer runs only on the CONTINUE route); Relevance is the SAME bounded
// RelevanceContext instance Jev saw (ARCH §3.3 edge); ExpectedBaseContentHashes
// maps docPath → bare-hex sha256 of the doc's content at head —
// orchestrator-computed from Git, never model-supplied (P5-T06; V-P5-14:
// workspace assembly is application-owned).
type WriterStageInput struct {
	Ctx                       RunContext
	Doc                       WorkspaceDocTarget
	Analysis                  *schemas.ChangeAnalysis
	Decision                  *schemas.Decision
	Relevance                 RelevanceContext
	ExpectedBaseContentHashes map[string]string
}

// WriterStageResultKind is the tag of the closed writer-stage result union.
type WriterStageResultKind string

// The closed union arms (TS WriterStageResult kinds).
const (
	WriterResultPatch   WriterStageResultKind = "patch"
	WriterResultFinding WriterStageResultKind = "finding"
	WriterResultAbstain WriterStageResultKind = "abstain"
	WriterResultNoop    WriterStageResultKind = "noop"
)

// WriterStageFindingReasonTimeout is the finding reason literal (TS
// WriterStageResult finding union reason 'writer-timeout').
const WriterStageFindingReasonTimeout = "writer-timeout"

// WriterStageFinding is the V-P3-8 timeout finding (TS WriterStageResult
// finding member).
type WriterStageFinding struct {
	DocPath string
	Reason  string
	Detail  string
}

// WriterStageResult is the closed union of writer-stage outcomes: exactly one
// arm is populated, selected by Kind.
type WriterStageResult struct {
	Kind WriterStageResultKind
	// Patch is the schema-valid DocPatch (Kind == WriterResultPatch).
	Patch *schemas.DocPatch
	// Finding is the timeout finding (Kind == WriterResultFinding).
	Finding *WriterStageFinding
	// Reason is the canonical abstain reason (Kind == WriterResultAbstain).
	Reason schemas.AbstainReason
	// Detail is the deterministic outcome detail (finding / abstain / noop).
	Detail string
}

// VerificationStagePort is the verification-stage port (P5-T07; ARCH §3.5 —
// three-tier verifier leaf). The verifier is JUDGE-INDEPENDENT (ARCH §13): it
// receives the patch and the original document content and re-derives the
// verdict.
//
// SEAM: internal/verifier is NOT landed yet — this interface is the declared
// seam; the wave-end composition layer binds the concrete three-tier verifier
// (TODO(verifier): no dependency from this package — only the interface).
type VerificationStagePort interface {
	Verify(input VerificationStageInput) (VerificationStageResult, error)
}

// VerificationStageInput is the verification-stage port input. DocContent is
// the original document content the judge re-derives the verdict from (nil
// when the runner could not read it — the verifier decides).
type VerificationStageInput struct {
	Ctx        RunContext
	Patch      schemas.DocPatch
	DocContent *string
}

// VerificationStageResult carries the verified report. Report is nil when the
// port failed (the runner degrades on a transport failure without an
// already-parsed report).
type VerificationStageResult struct {
	Report *schemas.VerificationReport
}

// PolicyStagePort is the policy-stage port (P5-T08; ARCH §3.5 — versioned
// policy engine leaf). Maps a (decision, verification) pair onto the pipeline
// outcome; the application — not the model — decides what is permitted
// (D11, ARCH §13).
type PolicyStagePort interface {
	Resolve(input PolicyStageInput) (PolicyStageResult, error)
}

// PolicyStageInput is the policy-stage port input. Verification is nil on the
// REVIEW path (no patch was produced).
type PolicyStageInput struct {
	Ctx          RunContext
	DocPath      string
	Decision     *schemas.Decision
	Verification *schemas.VerificationReport
}

// AbstainMarker is the canonical abstain {reason, detail} evidence pair.
type AbstainMarker struct {
	Reason schemas.AbstainReason
	Detail string
}

// PolicyStageResult is the result of the policy stage (P5-T08).
type PolicyStageResult struct {
	// Outcome is the pipeline outcome (run-outcome union).
	Outcome schemas.PipelineOutcome
	// ReviewRequired is true when the outcome requires human review routing.
	ReviewRequired bool
	// Abstain is the abstain marker when the stage refused to resolve
	// (fail closed).
	Abstain *AbstainMarker
	// QueueEventID is the review-queue linkage (M2; ARCH §5.2): set by the
	// policy adapter when the abstain landed in the review queue — the runner
	// carries it onto the run-level abstain record as `queue_event_id`
	// (RUN_ABSTAIN_SCHEMA.queue_event_id).
	QueueEventID *string
}

// DecisionSummary is the JEV decision summary of the early-signal payload
// (routing + confidence + candidate count).
type DecisionSummary struct {
	Routing        Routing
	Confidence     *float64
	CandidateCount int
}

// EarlySignalPayload is the early-signal payload (P5-T11 consumer): the
// post-decision snapshot emitted before the full run completes. Final marks
// the last update for this run.
type EarlySignalPayload struct {
	// RunID is the human-readable run id.
	RunID string
	// Key is the run-record identity.
	Key schemas.RunKey
	// DecisionSummary is the JEV decision summary.
	DecisionSummary DecisionSummary
	// At is the ISO-8601 instant (injected clock value, never a local Date).
	At string
	// Final is true when this is the final update for the run.
	Final bool
}

// EarlySignalPort is the early-signal sink port (P5-T11): Post once, then
// Update as state changes.
type EarlySignalPort interface {
	Post(payload EarlySignalPayload) error
	Update(payload EarlySignalPayload) error
}

// PipelinePorts is the port bundle the orchestrator runs against (P5-T11).
type PipelinePorts struct {
	Decision     DecisionStagePort
	Writer       WriterStagePort
	Verification VerificationStagePort
	Policy       PolicyStagePort
	EarlySignal  EarlySignalPort
}

/* --------------------------------------------- relevance context (graph → jev) */

// RelevanceViaKind is one closed `via` provenance kind of the DOCUMENT GRAPH
// → JEV edge (ARCH §3.3).
type RelevanceViaKind string

// The closed via-kind union (TS VIA_KIND_VALUES).
const (
	ViaKindMappingSource RelevanceViaKind = "mapping-source"
	ViaKindConcept       RelevanceViaKind = "concept"
	ViaKindContract      RelevanceViaKind = "contract"
)

// RelevanceVia is one bounded `via` provenance trail entry.
type RelevanceVia struct {
	Kind   RelevanceViaKind
	Detail string
}

// RelevanceDoc is one candidate doc entry of the relevance context.
type RelevanceDoc struct {
	DocPath   string
	Mode      workspace.WorkspaceMode
	Via       []RelevanceVia
	Contracts []string
}

// RelevanceCaps is the bounded caps of the relevance context.
type RelevanceCaps struct {
	MaxDocs int
}

// RelevanceContext is the bounded relevance context handed to the JEV
// decision stage (P5-T06; ARCH §3.3 DOCUMENT GRAPH → JEV edge). Readonly,
// closed shape: the graph edge exposes ONLY what Jev needs — candidate docs,
// their modes, bounded `via` provenance trails, contract names, and concept
// names.
type RelevanceContext struct {
	Docs     []RelevanceDoc
	Concepts []string
	Caps     RelevanceCaps
}

// BuildRelevanceContext builds the bounded relevance context from the
// document graph (P5-T01 part 1; pure — no I/O). Each admitted doc receives
// ≥1 `via` entry: one `mapping-source` entry per declared manifest source
// pattern for the doc's mapping record (the derivable matched-source-pattern
// set), plus one `concept` entry per concept that includes the doc; when no
// mapping record (or no derivable source pattern) exists, the single `via`
// entry is `{mapping-source, 'unmapped-candidate'}`. Order is deterministic:
// input order for docs, declaration order within records.
//
// BOUNDING (ARCH §3.3/§3.7): docPaths.length > maxDocs returns a
// RelevanceContextOverflowError — overflow is REPORTED (fail loud), never
// silently truncated.
func BuildRelevanceContext(
	documentGraph graph.DocumentGraph,
	docPaths []RelevanceDocTarget,
	maxDocs int,
) (RelevanceContext, error) {
	if len(docPaths) > maxDocs {
		return RelevanceContext{}, newRelevanceContextOverflowError(fmt.Sprintf(
			"relevance context overflow: %d candidate docs exceed maxDocs=%d",
			len(docPaths), maxDocs), nil)
	}

	mappingsByDoc := make(map[string]graph.MappingRecord, len(documentGraph.Mappings))
	for _, record := range documentGraph.Mappings {
		mappingsByDoc[record.Doc] = record
	}

	concepts := []string{}
	seenConcepts := make(map[string]struct{})

	docs := make([]RelevanceDoc, 0, len(docPaths))
	for _, candidate := range docPaths {
		record, hasRecord := mappingsByDoc[candidate.DocPath]

		via := []RelevanceVia{}
		if hasRecord && len(record.Sources) > 0 {
			for _, pattern := range record.Sources {
				via = append(via, RelevanceVia{Kind: ViaKindMappingSource, Detail: pattern})
			}
		} else {
			via = append(via, RelevanceVia{Kind: ViaKindMappingSource, Detail: "unmapped-candidate"})
		}

		if hasRecord {
			for _, concept := range record.Concepts {
				via = append(via, RelevanceVia{Kind: ViaKindConcept, Detail: concept})
				if _, seen := seenConcepts[concept]; !seen {
					seenConcepts[concept] = struct{}{}
					concepts = append(concepts, concept)
				}
			}
		}

		doc := RelevanceDoc{
			DocPath: candidate.DocPath,
			Mode:    candidate.Mode,
			Via:     via,
		}
		if hasRecord && len(record.Contracts) > 0 {
			doc.Contracts = append([]string(nil), record.Contracts...)
		} else {
			doc.Contracts = []string{}
		}
		docs = append(docs, doc)
	}

	return RelevanceContext{
		Docs:     docs,
		Concepts: concepts,
		Caps:     RelevanceCaps{MaxDocs: maxDocs},
	}, nil
}

// RelevanceDocTarget is one candidate doc handed to BuildRelevanceContext
// (TS `{ docPath, mode }`).
type RelevanceDocTarget struct {
	DocPath string
	Mode    workspace.WorkspaceMode
}

/* ------------------------------------------------------------------- errors */

// The closed union of typed pipeline error codes (provenance + CLI
// diagnostics): the base run failure plus the two typed refinements — the
// inter-stage contract violation (EDOCSYNC_STAGE_CONTRACT, contracts.ts,
// P5-T02) and the pipeline config defect (EDOCSYNC_PIPELINE_CONFIG,
// ARCH §11.3). The TS class hierarchy maps onto one Go error type whose code
// selects the refinement (CONVENTIONS.md: Go idiom is error values; the codes
// are new to docutil/errors.go, so they are carried verbatim as local
// constants through docutil.New — the globs/layout precedent — and reported
// in the unit report).
const (
	CodePipeline       = "EDOCSYNC_PIPELINE"
	CodePipelineConfig = "EDOCSYNC_PIPELINE_CONFIG"
	CodeStageContract  = "EDOCSYNC_STAGE_CONTRACT"
)

// PipelineError is the typed pipeline-run failure (P5-T01; ARCH §3.5). Stage
// names the stage that failed when the failure is stage-scoped; Detail
// carries the deterministic reason text (never a stack-only failure).
// Exit-code mapping to 3 (runtime failure, fail closed) lives in the runner
// (P5-T11); the config refinement (CodePipelineConfig) maps to exit 2.
type PipelineError struct {
	code   string
	stage  StageName
	detail string
	cause  error
}

// Error renders the TS message text: `stage '<stage>' failed: <detail>` when
// stage-scoped, else the bare detail (prefixed with the coded identity).
func (e *PipelineError) Error() string {
	inner := e.inner()
	if e.stage == "" {
		return inner.Error()
	}
	return fmt.Sprintf("%s: stage '%s' failed: %s", inner.Code, e.stage, e.detail)
}

// inner builds the coded error carrying the observable TS code.
func (e *PipelineError) inner() *docutil.Error {
	return docutil.New(e.code, e.detail)
}

// Code returns the stable error code (provenance + CLI diagnostics).
func (e *PipelineError) Code() string { return e.code }

// Stage returns the stage the failure is scoped to ("" when unscoped).
func (e *PipelineError) Stage() StageName { return e.stage }

// Detail returns the deterministic reason text.
func (e *PipelineError) Detail() string { return e.detail }

// Unwrap exposes the wrapped cause (nil when absent).
func (e *PipelineError) Unwrap() error { return e.cause }

// NewPipelineRunError builds the base pipeline-run failure (TS
// PipelineRunError).
func NewPipelineRunError(detail string, stage StageName, cause error) *PipelineError {
	return &PipelineError{code: CodePipeline, stage: stage, detail: detail, cause: cause}
}

// NewPipelineConfigError builds the typed pipeline CONFIG defect (P5-T02;
// ARCH §11.3; TS PipelineConfigError): a configuration error (e.g. the JEV
// stage rejecting its own configuration) is NOT an ablation — it must SURFACE
// as this distinct typed failure, never degrade to REVIEW. The runner maps
// the code onto its config-error exit lane (P5-T11).
func NewPipelineConfigError(detail string, stage StageName, cause error) *PipelineError {
	return &PipelineError{code: CodePipelineConfig, stage: stage, detail: detail, cause: cause}
}

// newRelevanceContextOverflowError builds the relevance-context bounding
// violation (P5-T01; ARCH §3.3/§3.7; TS RelevanceContextOverflowError):
// scoped to the retrieval stage (the bounding edge on the graph → JEV path).
func newRelevanceContextOverflowError(detail string, cause error) *PipelineError {
	return &PipelineError{code: CodePipeline, stage: StageRetrieval, detail: detail, cause: cause}
}

// IsPipelineConfigError reports whether err is (or wraps) the typed pipeline
// config defect (the exit-2 lane).
func IsPipelineConfigError(err error) bool {
	var pipelineErr *PipelineError
	return errors.As(err, &pipelineErr) && pipelineErr.code == CodePipelineConfig
}

// AsPipelineError extracts the typed pipeline error from err.
func AsPipelineError(err error) (*PipelineError, bool) {
	var pipelineErr *PipelineError
	if errors.As(err, &pipelineErr) {
		return pipelineErr, true
	}
	return nil, false
}

// intPtr returns a pointer to i, for optional integer members.
func intPtr(i int) *int { return &i }
