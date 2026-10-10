// Ported from packages/core/src/orchestrator/run.ts (P5-T01 part 3; PLAN
// §6.4; ARCH §3.4, §3.5, §7.1, §12.2; D14; V-P5-1/5-8/5-11/5-12 groundwork;
// P5-T14/T15 seams).
//
// RunPipeline walks DeclareStageGraph as DATA (the deterministic stage machine
// of ARCH §13): core-internal stages are direct calls to the real engines
// (analysis.BuildChangeAnalysis, graph.BuildDocumentGraph,
// statemachine.MarkSuspectsFromAnalysis, retrieval.RetrieveCandidates), while
// every LLM stage is a bounded leaf call through the PipelinePorts bundle —
// the application, never the model, decides what is permitted (ARCH §13).
//
// Honest-degradation discipline (D14, V-P5-8): an inconclusive analysis is a
// DECLARED analyzer-inconclusive degradation and the run CONTINUES with an
// honest empty candidate set — it never reports "no docs affected"; a failed
// decision stage degrades to REVIEW routing (fail to review, never auto).
//
// Seams and choices (ported verbatim from the TS module doc):
//   - LATENCY: stage latency_ms is wall-clock MEASUREMENT, not a generated
//     timestamp; every timestamp field still comes from the injected
//     input.Clock() (no time.Now() value is ever recorded as a field).
//   - RUN ID: the ARCH §4.1 convention — `repo#<base_sha>..<head_sha>` keyed
//     by the RESOLVED full SHAs, identical to analysis/build.go.
//   - POLICY VERSION (P5-T08 seam): input.Policy is opaque until the policy
//     stage lands, so policy_version is read STRUCTURALLY at runtime
//     (`policy.policy_version`, the CompiledPolicyWorkflow shape), fail-closed
//     when absent. Core must not import internal/policy (OD-1).
//   - ANALYZER LINEUP (P5-T02 seam): core cannot import the analyzer packages
//     (they depend on core), so DefaultAnalyzerLineup mirrors the real
//     packages' V1 behavior verbatim (stub + path-level); the P5-T02 runner
//     injects the real lineup via input.Analyzers.
//   - EDGE CONTRACTS (P5-T02): every payload received from an UNTRUSTED port
//     (decision / patch / verification) is schema-validated at the edge
//     (contracts.go) BEFORE any use; violations degrade onto the canonical
//     defaults (jev abstention / writer abstain / verifier_uncertain), each
//     with an edge-naming degradation record. EXCEPTION: JEV config defects
//     (code 'JEV_CONFIG') are NOT ablations — they fail the run typed
//     (PipelineConfigError; ARCH §11.3, exit-lane mapping in P5-T11).
//   - ABSTAIN REASONS: free-form port reasons map onto the canonical
//     AbstainReason taxonomy (exact vocabulary match, else other);
//     operational failures use model_unavailable, verification failures
//     verifier_uncertain, review-path policy downgrades policy_gap.
//   - ABSTAIN IDS: deterministic hex ab-<sha256[:16]> over
//     <decision_id|runId>|<docPath or '(run)'>|<reason> — stable for replay.
//   - RUN CAPS: input.Caps omitted → the default run caps bootstrap budget
//     (labeled budget data, not threshold calibration).
//   - PERSIST: the StateStore at <repoRoot>/.docsync/state (absolute, D1) with
//     an integrated churn hook; UpsertRunRecord conflicts get ONE graceful
//     retry, a second conflict is a typed PipelineRunError (V-P5-12).
//   - REVIEW PATH (ARCH §7.1): the policy stage resolves per-doc outcomes with
//     verification: nil; a patch-path outcome (auto/suggest/blocked) without
//     verification evidence downgrades to review + a policy_gap abstain — fail
//     to less automation. The writer is NEVER invoked on the IGNORE or REVIEW
//     paths (V-P5-1; stage-before-decision abort).
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/retrieval"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/statemachine"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

/* --------------------------------------------------------- small utilities */

// hrNow / hrMS: the TypeScript `process.hrtime.bigint()` latency helpers.
// Wall-clock MEASUREMENT only — no timestamp field is ever derived from them.
func hrNow() time.Time { return time.Now() }

func hrMS(started time.Time) int { return int(time.Since(started).Milliseconds()) }

// describeError renders an error's message text (TS `describeError`).
func describeError(err error) string {
	if err == nil {
		return "undefined"
	}
	if strings.TrimSpace(err.Error()) != "" {
		return err.Error()
	}
	return "Error"
}

// jevConfigErrorCode is the stable JEV config-defect code (TS
// JevConfigError.code = 'JEV_CONFIG').
const jevConfigErrorCode = "JEV_CONFIG"

// isJevConfigError is the structural JEV-config check (P5-T02; ARCH §11.3):
// core cannot import internal/policy or internal/openrouter (OD-1), so a
// config defect is recognized by its stable code — never by instanceof.
// JEV CONFIG ERRORS ARE NOT ABLATIONS: a match fails the run typed
// (PipelineConfigError), it never degrades to REVIEW.
func isJevConfigError(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ Code() string }
	return errors.As(err, &coded) && coded.Code() == jevConfigErrorCode
}

// toAbstainReason maps a free-form port reason onto the canonical abstain
// vocabulary (exact match else 'other'); an absent reason is the operational
// model_unavailable.
func toAbstainReason(raw *schemas.AbstainReason) schemas.AbstainReason {
	if raw == nil {
		return schemas.AbstainReasonModelUnavailable
	}
	if schemas.IsValidAbstainReason(string(*raw)) {
		return *raw
	}
	return schemas.AbstainReasonOther
}

// abstainReasonSeed is the TS `result.abstainReason ?? decision?.abstain_reason ?? undefined`.
func abstainReasonSeed(abstainReason *schemas.AbstainReason, decision *schemas.Decision) *schemas.AbstainReason {
	if abstainReason != nil {
		return abstainReason
	}
	if decision != nil {
		return decision.AbstainReason
	}
	return nil
}

// abstainIdFor is the deterministic abstain id: ab-<sha256[:16]> over the
// replay-stable seed (TS abstainIdFor).
func abstainIdFor(decisionID *string, runID string, docPath *string, reason schemas.AbstainReason) string {
	decisionPart := runID
	if decisionID != nil {
		decisionPart = *decisionID
	}
	docPart := "(run)"
	if docPath != nil {
		docPart = *docPath
	}
	seed := decisionPart + "|" + docPart + "|" + string(reason)
	return "ab-" + docutil.ContentHash(seed)[:16]
}

// compareText is the TS total order `a < b ? -1 : a > b ? 1 : 0`.
func compareText(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

/* ---------------------------------------------------- exported pure helpers */

// ConcurrencyGroupFor is the ARCH §12.2 concurrency-group surface: one stable
// group per repo × PR, exact `docsync-<repo>-<pr>`.
func ConcurrencyGroupFor(repo string, pr int) string {
	return fmt.Sprintf("docsync-%s-%d", repo, pr)
}

// RoutingResolution is the resolved routing plus the abstain marker the
// abstention carries (TS resolveRouting's return shape).
type RoutingResolution struct {
	Routing Routing
	Abstain *AbstainMarker
}

// ResolveRouting is the pure JEV → routing extraction (unit-testable):
// decision.action maps onto the routing union, and an abstention
// (abstained / null decision / decision.abstain) carries the canonical
// abstain reason plus the abstain detail.
func ResolveRouting(result DecisionStageResult) RoutingResolution {
	decision := result.Decision
	if result.Abstained || decision == nil || decision.Abstain {
		var detail string
		if result.AbstainReason != nil {
			detail = fmt.Sprintf("JEV abstained: %s — routed to review (abstain, don't guess)", *result.AbstainReason)
		} else {
			detail = "JEV abstained — routed to review (abstain, don't guess)"
		}
		return RoutingResolution{
			Routing: RoutingReview,
			Abstain: &AbstainMarker{
				Reason: toAbstainReason(abstainReasonSeed(result.AbstainReason, decision)),
				Detail: detail,
			},
		}
	}
	switch decision.Action {
	case schemas.ActionIgnore:
		return RoutingResolution{Routing: RoutingIgnore}
	case schemas.ActionReview:
		return RoutingResolution{Routing: RoutingReview}
	case schemas.ActionInvestigate, schemas.ActionPatch:
		return RoutingResolution{Routing: RoutingContinue}
	default:
		// Unreachable for a schema-validated decision (the closed action
		// union); fail closed to review.
		return RoutingResolution{Routing: RoutingReview}
	}
}

// extractPolicyVersion reads policy_version structurally from the opaque
// policy payload (P5-T08 seam): the CompiledPolicyWorkflow nested
// policy.policy_version first, else a bare policy object's policy_version;
// fail closed (typed PipelineRunError, unscoped) when absent or not a positive
// integer. The payload round-trips through JSON so map- and struct-shaped
// payloads read identically.
func extractPolicyVersion(policy any) (int, error) {
	fail := func() (int, error) {
		return 0, NewPipelineRunError(
			"pipeline input policy carries no positive integer policy_version — the P5-T08 "+
				"policy-stage integration must supply a CompiledPolicyWorkflow-shaped payload (fail closed)",
			"", nil)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return fail()
	}
	var decoded struct {
		PolicyVersion *json.Number `json:"policy_version"`
		Policy        *struct {
			PolicyVersion *json.Number `json:"policy_version"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fail()
	}
	value := decoded.PolicyVersion
	if decoded.Policy != nil && decoded.Policy.PolicyVersion != nil {
		value = decoded.Policy.PolicyVersion
	}
	if value == nil {
		return fail()
	}
	asFloat, err := value.Float64()
	if err != nil || asFloat != math.Trunc(asFloat) || asFloat <= 0 || asFloat > float64(math.MaxInt32) {
		return fail()
	}
	return int(asFloat), nil
}

// manifestModeOf is the manifest mode for a doc: entry mode, else defaults
// mode, else 'suggest' (review-gated) — the TS `entry?.mode ??
// manifest.defaults?.mode ?? 'suggest'`.
func manifestModeOf(manifest schemas.Manifest, docPath string) workspace.WorkspaceMode {
	for _, entry := range manifest.Documents {
		if entry.Path == docPath {
			if entry.Mode != nil {
				return workspace.WorkspaceMode(string(*entry.Mode))
			}
			break
		}
	}
	if manifest.Defaults != nil && manifest.Defaults.Mode != nil {
		return workspace.WorkspaceMode(string(*manifest.Defaults.Mode))
	}
	return workspace.WorkspaceModeSuggest
}

/* ---------------------------------------- V1 bootstrap analyzer lineup seam */

// The byte-stable V1 coverage notes, verbatim from the real analyzer packages:
// the TS stub note is REUSED from the analysis package (the seam where
// analyzer coverage is declared) rather than re-typed.
const (
	genericCoverageNote    = "path-level analyzer, always present (OD-4); symbol/type-level analysis lands Phase 11 (F11-1/F11-2)"
	genericPathLevelNoteV1 = "generic path-level analyzer (V1): no symbol/AST extraction — declared coverage gap"
)

// typescriptAnalyzerStub is the Phase-1 mirror of
// packages/analyzer-typescript/src/index.ts: a STUB with no AST emission that
// declares its coverage gap (V-P1-13 groundwork).
type typescriptAnalyzerStub struct{}

func (typescriptAnalyzerStub) Name() schemas.AnalyzerName {
	return schemas.AnalyzerNameTypeScript
}

func (typescriptAnalyzerStub) Analyze(analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	note := analysis.TypeScriptStubNote
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusStub,
			Note:     &note,
		},
		Files: []analysis.PerFileFinding{},
	}, nil
}

// genericPathLevelAnalyzer is the Phase-1 mirror of
// packages/analyzer-generic/src/index.ts: per-file path-level coverage with
// deep_declined declared — never a guessed negative.
type genericPathLevelAnalyzer struct{}

func (genericPathLevelAnalyzer) Name() schemas.AnalyzerName {
	return schemas.AnalyzerNameGeneric
}

func (genericPathLevelAnalyzer) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	note := genericCoverageNote
	files := make([]analysis.PerFileFinding, 0, len(input.Files))
	for _, file := range input.Files {
		pathNote := genericPathLevelNoteV1
		files = append(files, analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: false,
			APIDiff:        false,
			ConfigDiff:     false,
			DeepDeclined:   true,
			Notes:          []string{pathNote},
		})
	}
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameGeneric,
			Status:   schemas.AnalyzerStatusOK,
			Note:     &note,
		},
		Files: files,
	}, nil
}

// DefaultAnalyzerLineup is the V1 bootstrap mirror of the real TypeScript
// stub + generic path-level lineup (TS defaultAnalyzerLineup). The P5-T02
// runner injects the real analyzer packages via input.Analyzers; the fallback
// exists ONLY so callers can wire a run before importing them.
func DefaultAnalyzerLineup() []analysis.AnalyzerPlugin {
	return []analysis.AnalyzerPlugin{typescriptAnalyzerStub{}, genericPathLevelAnalyzer{}}
}

// defaultRunCaps is the DEFAULT_RUN_CAPS bootstrap budget (TS
// `{ maxFiles: 200, maxDiffSize: 1_000_000 } as const`).
var defaultRunCaps = analysis.AnalysisCaps{
	MaxFiles:    200,
	MaxDiffSize: 1_000_000,
}

/* ------------------------------------------ retrieval evidence (P11-T04) */

// RetrievalEvidence is the level-3/4/5b evidence inputs the run computed from
// git-backed content before retrieval (TS `RetrievalEvidence &
// {unparseable: number}`).
type RetrievalEvidence struct {
	ApiDelta    *analysis.OpenApiDeltaClassification
	ConfigDiff  []analysis.ConfigKeyDiff
	Unparseable int
}

// mergeApiDeltas merges the per-file OpenAPI delta classifications from the
// recorded-double `AnalyzeApiDelta` calls: concatenated, re-sorted by
// location/kind/detail (deterministic, not per-file order), recursive tier
// summary, first file's rules_version (TS mergeApiDeltas).
func mergeApiDeltas(classifications []analysis.OpenApiDeltaClassification) *analysis.OpenApiDeltaClassification {
	if len(classifications) == 0 {
		return nil
	}
	deltas := make([]analysis.OpenApiDeltaEntry, 0)
	for _, classification := range classifications {
		deltas = append(deltas, classification.Deltas...)
	}
	sort.SliceStable(deltas, func(i, j int) bool {
		return compareText(
			deltas[i].Location+"\x00"+string(deltas[i].Kind)+"\x00"+deltas[i].Detail,
			deltas[j].Location+"\x00"+string(deltas[j].Kind)+"\x00"+deltas[j].Detail,
		) < 0
	})
	var breaking, behaviorAnnotating, nonBreaking int
	for _, delta := range deltas {
		switch delta.Tier {
		case analysis.OpenApiTierBreaking:
			breaking++
		case analysis.OpenApiTierBehaviorAnnotating:
			behaviorAnnotating++
		default:
			nonBreaking++
		}
	}
	worstTier := analysis.OpenApiTierNonBreaking
	if breaking > 0 {
		worstTier = analysis.OpenApiTierBreaking
	} else if behaviorAnnotating > 0 {
		worstTier = analysis.OpenApiTierBehaviorAnnotating
	}
	return &analysis.OpenApiDeltaClassification{
		Deltas: deltas,
		Summary: analysis.OpenApiDeltaSummary{
			Breaking:           breaking,
			BehaviorAnnotating: behaviorAnnotating,
			NonBreaking:        nonBreaking,
			WorstTier:          worstTier,
		},
		RulesVersion: classifications[0].RulesVersion,
	}
}

// parseStructuredContent parses structured content: JSON first, then YAML —
// the two structured formats the config/openapi families carry (TS
// parseStructuredContent).
func parseStructuredContent(text string) (any, error) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err == nil {
		return value, nil
	}
	if err := yaml.Unmarshal([]byte(text), &value); err != nil {
		return nil, err
	}
	return value, nil
}

// deriveRetrievalEvidence derives the level-3/4/5b retrieval evidence inputs
// from the MODIFIED openapi/config-family files: both sides are fetched at
// head and base, API/config deltas are computed with the REAL Phase 1 engines
// (world logic import, not a stub), and any unparseable side increments
// unparseable — the orchestrator turns that into the declared cap-exceeded
// degradation (never a silent zero). Never throws.
func deriveRetrievalEvidence(
	ctx context.Context,
	git *gitx.GitClient,
	baseSha, headSha string,
	files []schemas.ChangedFile,
) RetrievalEvidence {
	evidence := RetrievalEvidence{}
	var apiClassifications []analysis.OpenApiDeltaClassification
	for _, file := range files {
		if file.Status != schemas.FileStatusModified {
			continue
		}
		family := analysis.ClassifyPathFamily(file.Path)
		if family != analysis.PathFamilyOpenAPI && family != analysis.PathFamilyConfig {
			continue
		}
		before, err := git.CatFile(ctx, baseSha, file.Path)
		if err != nil {
			evidence.Unparseable++
			continue
		}
		after, err := git.CatFile(ctx, headSha, file.Path)
		if err != nil {
			evidence.Unparseable++
			continue
		}
		beforeValue, err := parseStructuredContent(before.Content)
		if err != nil {
			evidence.Unparseable++
			continue
		}
		afterValue, err := parseStructuredContent(after.Content)
		if err != nil {
			evidence.Unparseable++
			continue
		}
		if family == analysis.PathFamilyOpenAPI {
			classification, err := analysis.ClassifyOpenApiDelta(beforeValue, afterValue)
			if err != nil {
				evidence.Unparseable++
				continue
			}
			apiClassifications = append(apiClassifications, classification)
			continue
		}
		diffs, err := analysis.DiffConfigKeys(beforeValue, afterValue)
		if err != nil {
			evidence.Unparseable++
			continue
		}
		evidence.ConfigDiff = append(evidence.ConfigDiff, diffs...)
	}
	evidence.ApiDelta = mergeApiDeltas(apiClassifications)
	return evidence
}

/* ------------------------------------------------------------ doc grounding */

// loadStoredClaims returns all stored claims, deterministic order (the
// level-3/4/5b mention strategies).
func loadStoredClaims(store *state.StateStore) ([]schemas.Claim, error) {
	return claims.NewClaimStore(store).List()
}

/* ------------------------------------------ unknown coverage (D7, P5-T15) */

// ComputeUnknownCoverage is the D7 unknown coverage (P5-T15; ARCH §4.2):
// analysis changed paths matched by NO manifest mapping, deterministic sorted.
// A failed reverse-index build propagates (fail closed) — a coverage answer is
// never guessed.
func ComputeUnknownCoverage(
	changeAnalysis *schemas.ChangeAnalysis,
	documentGraph *graph.DocumentGraph,
) ([]string, error) {
	if changeAnalysis == nil || documentGraph == nil {
		return []string{}, nil
	}
	changedPaths := make([]string, 0, len(changeAnalysis.Files))
	seen := make(map[string]struct{}, len(changeAnalysis.Files))
	for _, file := range changeAnalysis.Files {
		if _, duplicate := seen[file.Path]; duplicate {
			continue
		}
		seen[file.Path] = struct{}{}
		changedPaths = append(changedPaths, file.Path)
	}
	index, err := graph.BuildReverseIndex(*documentGraph, changedPaths)
	if err != nil {
		return nil, err
	}
	unknown := make([]string, 0, len(changedPaths))
	for _, path := range changedPaths {
		if len(index.DocsByChangedPath[path]) == 0 {
			unknown = append(unknown, path)
		}
	}
	sort.SliceStable(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	return unknown, nil
}

/* ------------------------------------ baseline short-circuit (P5-T14) */

// BaselineShortCircuit is the P5-T14 baseline short-circuit verdict (TS
// evaluateBaselineShortCircuitee's return shape): a re-verified doc with a
// BASELINE_VALID ladder resolution short-circuits the writer; the declining
// paths carry the ladder run verbatim for review evidence.
type BaselineShortCircuit struct {
	ShortCircuit bool
	Run          schemas.BaselineRun
	// Inconclusive marks a HELD (not expired) baseline resolution that could
	// not be judged — the failure detail rides in InconclusiveDetail.
	Inconclusive       bool
	InconclusiveDetail string
}

// EvaluateBaselineShortCircuit is the pre-writer baseline short-circuit gate
// (TS evaluateBaselineShortCircuit). A doc with no stored state, any state but
// VERIFIED, or no baseline entry NEVER short-circuits (run 'none'); a held
// baseline resolves through the ladder every time it can be judged. Baseline
// RESOLUTION failure is inconclusive, never a silent negative (D14): the
// writer continues normally and the degradation declares it.
//
// Deviation: the TS signature leads with an unused RunContext member; Go takes
// the context the git/ladder calls need instead.
func EvaluateBaselineShortCircuit(
	ctx context.Context,
	docPath string,
	store *state.StateStore,
	git *gitx.GitClient,
	headSha string,
) BaselineShortCircuit {
	inconclusive := func(err error) BaselineShortCircuit {
		return BaselineShortCircuit{
			ShortCircuit:       false,
			Run:                schemas.BaselineRunNone,
			Inconclusive:       true,
			InconclusiveDetail: describeError(err),
		}
	}
	docRead, err := store.ReadDocState(docPath)
	if err != nil {
		return inconclusive(err)
	}
	if !docRead.Exists || docRead.Content == nil {
		return BaselineShortCircuit{Run: schemas.BaselineRunNone}
	}
	if docRead.Content.State != schemas.DocStateVerified {
		return BaselineShortCircuit{Run: schemas.BaselineRunNone}
	}
	baseline, err := baselines.NewBaselineStore(store).Get(docPath)
	if err != nil {
		return inconclusive(err)
	}
	if baseline == nil {
		return BaselineShortCircuit{Run: schemas.BaselineRunNone}
	}
	resolution, err := baselines.ResolveBaseline(ctx, baselines.ResolveBaselineInput{
		Baseline: baseline,
		Head:     headSha,
		Git:      git,
	})
	if err != nil {
		return inconclusive(err)
	}
	return BaselineShortCircuit{
		ShortCircuit: resolution.BaselineValid,
		Run:          schemas.BaselineRun(string(resolution.Run)),
	}
}

/* --------------------------------------------------------- the run records */

// stageTrace is one stage's runtime record (TS the traces Map value): the
// status plus the wall-clock latency measurement and the optional note.
type stageTrace struct {
	status    schemas.RunStageStatus
	latencyMS *int
	note      *string
}

// stageFailure is the first terminal failure (TS `failure`).
type stageFailure struct {
	stage StageName
	err   error
}

// pendingPatch is one schema-validated writer patch awaiting verification (TS
// pendingPatches entry).
type pendingPatch struct {
	docPath string
	patch   schemas.DocPatch
}

// verifiedPatch is one verified patch (TS patchVerified entry).
type verifiedPatch struct {
	docPath      string
	verification schemas.VerificationReport
}

// bufferedWriterCall is one queued writer call (TS bufferedWriterCalls entry).
type bufferedWriterCall struct {
	candidate retrieval.RetrievalCandidate
	input     WriterStageInput
}

// settledWriterCall is one settled pool result (TS settledResults entry): the
// closed worker union is a Go error when the port rejected transport-wide,
// which is applied through the writer-error path (transport/generic ablation).
type settledWriterCall struct {
	result *WriterStageResult
	err    error
}

// docBase is the ensureDoc `Partial<RunDocument>` argument (TS's spread shape):
// only the three fields any caller sets are represented.
type docBase struct {
	policyOutcome   *schemas.PipelineOutcome
	partialCoverage *bool
	baselineRun     *schemas.BaselineRun
}

// docPatch is the updateDoc `Partial<RunDocument>` argument: the mutable
// document fields the stages write after dom creation.
type docPatch struct {
	policyOutcome   *schemas.PipelineOutcome
	partialCoverage *bool
	abstain         *schemas.RunDocumentAbstain
	verification    *schemas.RunDocumentVerification
}

func strPtr(s string) *string     { return &s }
func floatPtr(f float64) *float64 { return &f }

func applyDocBase(doc *schemas.RunDocument, base docBase) {
	if base.policyOutcome != nil {
		doc.PolicyOutcome = *base.policyOutcome
	}
	if base.partialCoverage != nil {
		doc.PartialCoverage = *base.partialCoverage
	}
	if base.baselineRun != nil {
		doc.BaselineRun = base.baselineRun
	}
}

func applyDocPatch(doc *schemas.RunDocument, patch docPatch) {
	if patch.policyOutcome != nil {
		doc.PolicyOutcome = *patch.policyOutcome
	}
	if patch.partialCoverage != nil {
		doc.PartialCoverage = *patch.partialCoverage
	}
	if patch.abstain != nil {
		doc.Abstain = patch.abstain
	}
	if patch.verification != nil {
		doc.Verification = patch.verification
	}
}

// pipelineRun carries the state the TypeScript closures shared across the nine
// stages. It is NOT concurrent: the writer pool's goroutines only call the
// injected port and write to their own settled slot; every shared field is
// touched by the stage methods on the pipeline's own goroutine.
type pipelineRun struct {
	ctx   context.Context
	input PipelineInput
	ports PipelinePorts

	git   *gitx.GitClient
	store *state.StateStore
	churn *state.ChurnCounter

	startedAt     string
	baseSha       string
	headSha       string
	policyVersion int
	key           schemas.RunKey
	runID         string
	effectiveCaps analysis.AnalysisCaps
	budgets       PipelineBudgets

	traces       map[StageName]*stageTrace
	degradations []schemas.Degradation
	findings     []schemas.RunFinding
	abstains     []schemas.RunAbstain
	candidates   []schemas.RunCandidate
	docsByPath   map[string]*schemas.RunDocument
	docsOrder    []string

	failure        *stageFailure
	routing        Routing
	decisionResult *DecisionStageResult
	relevance      *RelevanceContext
	analysis       *schemas.ChangeAnalysis
	runCtx         *RunContext

	retrievalCandidates []retrieval.RetrievalCandidate
	pendingPatches      []pendingPatch
	patchVerified       []verifiedPatch

	postedPayload *EarlySignalPayload
	postedAt      *string
	updatedAt     *string
}

/* -------------------------------------------------------------- the runner */

// RunPipeline is the ARCH §3.5 entry point: one deterministic run of the
// nine-stage graph (P5-T01 part 3). Core-internal stages call the real
// engines directly; every LLM stage is a bounded leaf call through ports;
// stage selection walks DeclareStageGraph as data with terminal edges as
// values. An outcome is ALWAYS produced when the run itself was sound —
// per-stage failures are declared degradations, never a thrown pipeline
// outcome; only a setup / internal-invariant failure returns an error (D14).
func RunPipeline(ctx context.Context, input PipelineInput, ports PipelinePorts) (*schemas.RunOutcome, error) {
	// The TS `new GitClient({repoRoot})` sits OUTSIDE the try: an unusable
	// repo root fails the run before any stage runs.
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: input.RepoRoot})
	if err != nil {
		return nil, err
	}

	resolvedBase, err := git.ResolveRev(ctx, input.Base)
	if err != nil {
		return nil, NewPipelineRunError(
			fmt.Sprintf("setup: cannot resolve base/head refs ('%s', '%s'): %s", input.Base, input.Head, describeError(err)),
			"", err)
	}
	resolvedHead, err := git.ResolveRev(ctx, input.Head)
	if err != nil {
		return nil, NewPipelineRunError(
			fmt.Sprintf("setup: cannot resolve base/head refs ('%s', '%s'): %s", input.Base, input.Head, describeError(err)),
			"", err)
	}

	policyVersion, err := extractPolicyVersion(input.Policy)
	if err != nil {
		return nil, err
	}

	effectiveCaps := defaultRunCaps
	if input.Caps != nil {
		effectiveCaps = *input.Caps
	}

	// The churn counter is wired into the StateStore itself (the TS
	// `new StateStore({stateDir, churn})`), so the state_churn observable in
	// the run record is a direct emission from every landed write.
	churn := state.CreateStateChurnCounter()
	stateDir := filepath.Join(input.RepoRoot, ".docsync", "state")
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir, Churn: churn})
	if err != nil {
		return nil, err
	}

	startedAt := input.Clock()
	key := schemas.RunKey{
		Repo:            input.Repo,
		BaseSHA:         resolvedBase,
		HeadSHA:         resolvedHead,
		ManifestVersion: input.Manifest.Version,
		PolicyVersion:   policyVersion,
	}
	runID := fmt.Sprintf("%s#%s..%s", input.Repo, resolvedBase, resolvedHead)

	traces := make(map[StageName]*stageTrace, len(PipelineStages))
	for _, stage := range PipelineStages {
		traces[stage] = &stageTrace{
			status: schemas.RunStageSkipped,
			note:   strPtr("not reached"),
		}
	}

	run := &pipelineRun{
		ctx:           ctx,
		input:         input,
		ports:         ports,
		git:           git,
		store:         store,
		churn:         churn,
		startedAt:     startedAt,
		baseSha:       resolvedBase,
		headSha:       resolvedHead,
		policyVersion: policyVersion,
		key:           key,
		runID:         runID,
		effectiveCaps: effectiveCaps,
		traces:        traces,
		degradations:  []schemas.Degradation{},
		findings:      []schemas.RunFinding{},
		abstains:      []schemas.RunAbstain{},
		candidates:    []schemas.RunCandidate{},
		docsByPath:    map[string]*schemas.RunDocument{},
		routing:       RoutingContinue,
		budgets:       MergePipelineBudgets(input.Budgets),
	}

	current := StageAnalyze
	for current != "" && run.failure == nil {
		// An internal-invariant throw escapes the walk exactly as the TS
		// throw did: no persist, no outcome (it can never happen for a
		// sound run; it fails loud rather than degrading).
		if err := run.executeStage(current); err != nil {
			return nil, err
		}
		if run.failure != nil {
			break
		}
		current = NextStage(current, run.routing)
	}

	if run.failure != nil && traces[StagePersist].status == schemas.RunStageSkipped {
		run.persistStage()
	}

	if run.failure != nil {
		// A typed PipelineConfigError keeps its identity (exit-lane mapping
		// lives in the runner); every other failure is rethrown typed.
		if IsPipelineConfigError(run.failure.err) {
			return nil, run.failure.err
		}
		return nil, NewPipelineRunError(describeError(run.failure.err), run.failure.stage, run.failure.err)
	}

	outcome, err := run.assembleOutcome()
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

/* ------------------------------------------------------------ run plumbing */

// outcomeForRecord is the record's outcome: the run-level outcome, or 'failed'
// when a stage failed terminally (TS `failure === null ? runLevelOutcome() : 'failed'`).
func (r *pipelineRun) outcomeForRecord() string {
	if r.failure == nil {
		return string(r.runLevelOutcome())
	}
	return "failed"
}

// stagesForRecord is the record's stage list (status + optional note; the run
// record carries no latency).
func (r *pipelineRun) stagesForRecord() []schemas.RunStage {
	stages := make([]schemas.RunStage, 0, len(PipelineStages))
	for _, stage := range PipelineStages {
		trace := r.traces[stage]
		stages = append(stages, schemas.RunStage{
			Stage:  string(stage),
			Status: trace.status,
			Note:   trace.note,
		})
	}
	return stages
}

// complete finalizes a stage's trace (TS `complete`): records the measured
// latency and the budget breach as a DECLARED degradation — a stage running
// past its declared budget is never silent (D14).
func (r *pipelineRun) complete(stage StageName, status schemas.RunStageStatus, started time.Time, note ...string) {
	latencyMs := hrMS(started)
	trace := r.traces[stage]
	trace.latencyMS = &latencyMs
	trace.status = status
	trace.note = nil
	if len(note) > 0 {
		noteValue := note[0]
		trace.note = &noteValue
	}
	if budget := r.budgets.StageBudgetMS[stage]; budget != nil && latencyMs > *budget {
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage:  string(stage),
			Kind:   schemas.DegradationBudgetExceeded,
			Detail: fmt.Sprintf("stage '%s' took %dms > budget %dms (P5-T11)", stage, latencyMs, *budget),
		})
		text := fmt.Sprintf("budget exceeded (P5-T11): %dms > %dms", latencyMs, *budget)
		if trace.note != nil {
			text = *trace.note + " — " + text
		}
		trace.note = &text
	}
}

// ensureDoc invents or returns the review-default document entry for a doc
// (TS ensureDoc): artifacts touched by a degraded stage are ALWAYS assumed
// reviewed-and-pending — they never lack a row.
func (r *pipelineRun) ensureDoc(docPath string, base docBase) *schemas.RunDocument {
	if existing, ok := r.docsByPath[docPath]; ok {
		applyDocBase(existing, base)
		return existing
	}
	doc := &schemas.RunDocument{
		DocPath:         docPath,
		PolicyOutcome:   schemas.PipelineOutcomeReview,
		PartialCoverage: false,
	}
	routing := r.routing
	doc.Routing = &routing
	if r.decisionResult != nil && r.decisionResult.Decision != nil {
		doc.DecisionID = strPtr(r.decisionResult.Decision.DecisionID)
		doc.Confidence = floatPtr(r.decisionResult.Decision.Confidence)
	}
	applyDocBase(doc, base)
	r.docsByPath[docPath] = doc
	r.docsOrder = append(r.docsOrder, docPath)
	return doc
}

// updateDoc merges a partial document update into an existing entry (TS
// updateDoc).
func (r *pipelineRun) updateDoc(docPath string, patch docPatch) {
	if doc, ok := r.docsByPath[docPath]; ok {
		applyDocPatch(doc, patch)
	}
}

// pushAbstain records one run-level abstain record (TS pushAbstain): the
// deterministic id, the optional doc path, the canonical reason, the
// evidence detail, and the review-queue event linkage when the policy
// adapter surfaced one.
func (r *pipelineRun) pushAbstain(docPath *string, reason schemas.AbstainReason, detail string, queueEventID string) {
	var decisionID *string
	if r.decisionResult != nil && r.decisionResult.Decision != nil {
		decisionID = strPtr(r.decisionResult.Decision.DecisionID)
	}
	abstain := schemas.RunAbstain{
		AbstainID: abstainIdFor(decisionID, r.runID, docPath, reason),
		DocPath:   docPath,
		Reason:    string(reason),
		Detail:    detail,
	}
	if decisionID != nil {
		abstain.DecisionID = decisionID
	}
	if queueEventID != "" {
		abstain.QueueEventID = strPtr(queueEventID)
	}
	r.abstains = append(r.abstains, abstain)
}

// runLevelOutcome derives the run-level pipeline outcome from the per-document
// policy outcomes (TS runLevelOutcome): any unresolved doc means the run is
// review; else any suggest means suggest; else any auto means auto; else
// ignore.
func (r *pipelineRun) runLevelOutcome() schemas.PipelineOutcome {
	if r.routing == RoutingIgnore {
		return schemas.PipelineOutcomeIgnore
	}
	if r.routing == RoutingReview {
		return schemas.PipelineOutcomeReview
	}
	var anyReview, anySuggest, anyAuto bool
	for _, doc := range r.docsByPath {
		switch doc.PolicyOutcome {
		case schemas.PipelineOutcomeReview, schemas.PipelineOutcomeBlocked:
			anyReview = true
		case schemas.PipelineOutcomeSuggest:
			anySuggest = true
		case schemas.PipelineOutcomeAuto:
			anyAuto = true
		}
	}
	if anyReview {
		return schemas.PipelineOutcomeReview
	}
	if anySuggest {
		return schemas.PipelineOutcomeSuggest
	}
	if anyAuto {
		return schemas.PipelineOutcomeAuto
	}
	return schemas.PipelineOutcomeIgnore
}

// persistStage is the ALWAYS-RUNS persist stage (TS persistStage): one
// writeRunRecord against the run key (idempotent) with ONE graceful CAS
// retry, the optional final early-signal update, and a declared degradation
// when the retry was needed (V-P5-12). A failed persist DOES NOT throw an
// outcome — the failure rides the run record itself (D14).
func (r *pipelineRun) persistStage() {
	started := hrNow()
	finishedAt := r.input.Clock()
	record := schemas.RunRecord{
		Schema:     schemas.RunRecordSchemaID,
		RunID:      r.runID,
		Key:        r.key,
		StartedAt:  r.startedAt,
		FinishedAt: &finishedAt,
		Outcome:    strPtr(r.outcomeForRecord()),
		StateChurn: state.ChurnToRecordFields(r.churn.Snapshot()),
		Stages:     r.stagesForRecord(),
	}
	upsert, err := r.store.UpsertRunRecord(r.key, record)
	retried := false
	if err == nil && !upsert.OK {
		upsert, err = r.store.UpsertRunRecord(r.key, record)
		retried = true
	}
	if err != nil {
		if r.failure == nil {
			r.failure = &stageFailure{stage: StagePersist, err: err}
		}
		r.complete(StagePersist, schemas.RunStageFailed, started, describeError(err))
		return
	}
	if !upsert.OK {
		conflict := NewPipelineRunError("run record CAS conflict persisted after one graceful retry (V-P5-12)", StagePersist, nil)
		if r.failure == nil {
			r.failure = &stageFailure{stage: StagePersist, err: conflict}
		}
		r.complete(StagePersist, schemas.RunStageFailed, started, describeError(conflict))
		return
	}
	if retried {
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage:  string(StagePersist),
			Kind:   "cas-conflict-retry",
			Detail: "run record upsert hit a CAS conflict once — re-upserted after the graceful re-read (V-P5-12)",
		})
	}
	if r.ports.EarlySignal != nil && r.postedPayload != nil && r.postedAt != nil {
		updatedAt := r.input.Clock()
		updateErr := r.ports.EarlySignal.Update(EarlySignalPayload{
			RunID:           r.postedPayload.RunID,
			Key:             r.postedPayload.Key,
			DecisionSummary: r.postedPayload.DecisionSummary,
			At:              updatedAt,
			Final:           true,
		})
		if updateErr != nil {
			// The signal payload is already emitted; a failed update never
			// blocks the run — the signal stays partial.
			r.updatedAt = nil
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage:  string(StagePersist),
				Kind:   schemas.DegradationSignalSinkFailed,
				Detail: "early signal update failed: " + describeError(updateErr),
			})
			if r.failure == nil {
				r.failure = &stageFailure{stage: StagePersist, err: updateErr}
			}
			r.complete(StagePersist, schemas.RunStageFailed, started, describeError(updateErr))
			return
		}
		r.updatedAt = &updatedAt
	}
	r.complete(StagePersist, schemas.RunStageOk, started)
}

/* -------------------------------------------------------- the stage bodies */

// executeStage runs one stage of the walked graph (TS executeStage). A returned
// error escapes the walk (internal invariant breaks) — it never degrades.
func (r *pipelineRun) executeStage(stage StageName) error {
	// Every stage except analysis and mapping needs the run context; reaching
	// one without it is an internal invariant violation, never a stage
	// degradation.
	if stage != StageAnalyze && stage != StageMap && r.runCtx == nil {
		return NewPipelineRunError(
			fmt.Sprintf("internal: stage '%s' reached before the run context existed", stage), stage, nil)
	}

	switch stage {
	case StageAnalyze:
		r.analyzeStage()
	case StageMap:
		r.mapStage()
	case StageInvalidate:
		r.invalidateStage()
	case StageJev:
		r.jevStage()
	case StageRetrieval:
		r.retrievalStage()
	case StageWriter:
		return r.writerStage()
	case StageVerification:
		r.verificationStage()
	case StagePolicy:
		r.policyStage()
	case StagePersist:
		r.persistStage()
	}
	return nil
}

func (r *pipelineRun) analyzeStage() {
	started := hrNow()
	plugins := r.input.Analyzers
	if plugins == nil {
		plugins = DefaultAnalyzerLineup()
	}
	outcome, err := analysis.BuildChangeAnalysis(r.ctx, analysis.BuildChangeAnalysisInput{
		Repo:     r.input.Repo,
		RepoRoot: r.input.RepoRoot,
		Git:      r.git,
		Base:     r.input.Base,
		Head:     r.input.Head,
		Plugins:  plugins,
		Caps:     &r.effectiveCaps,
	})
	if err != nil {
		r.failure = &stageFailure{stage: StageAnalyze, err: err}
		r.complete(StageAnalyze, schemas.RunStageFailed, started, describeError(err))
		return
	}
	if outcome.Kind == analysis.AnalysisOutcomeKindInconclusive {
		// D14: analyzer failure is inconclusive — never "no docs affected".
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage:  string(StageAnalyze),
			Kind:   schemas.DegradationAnalyzerInconclusive,
			Detail: outcome.Reason,
		})
		r.complete(StageAnalyze, schemas.RunStageInconclusive, started, outcome.Reason)
		return
	}
	r.analysis = outcome.Record
	r.complete(StageAnalyze, schemas.RunStageOk, started)
}

func (r *pipelineRun) mapStage() {
	started := hrNow()
	documentGraph := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest: r.input.Manifest,
	})
	if len(documentGraph.Errors) > 0 {
		// Graph composition errors are DATA (not a throw): a graph with
		// unresolved mappings never drives invalidation — fail closed, D8.
		details := make([]string, 0, len(documentGraph.Errors))
		for _, entry := range documentGraph.Errors {
			details = append(details, fmt.Sprintf("%s at %s", entry.Kind, entry.Path))
		}
		r.failure = &stageFailure{
			stage: StageMap,
			err: NewPipelineRunError(
				"document graph unusable: "+strings.Join(details, "; ")+" (fail closed, D8)", StageMap, nil),
		}
		r.complete(StageMap, schemas.RunStageFailed, started, "graph composition errors — fail closed (D8)")
		return
	}
	r.runCtx = &RunContext{
		Repo:     r.input.Repo,
		RepoRoot: r.input.RepoRoot,
		Base:     r.input.Base,
		Head:     r.input.Head,
		RunID:    r.runID,
		Key:      r.key,
		Manifest: r.input.Manifest,
		Graph:    documentGraph,
		Caps:     r.effectiveCaps,
		Budgets:  r.budgets,
		Clock:    r.input.Clock,
		PR:       r.input.PR,
	}
	r.complete(StageMap, schemas.RunStageOk, started)
}

func (r *pipelineRun) invalidateStage() {
	started := hrNow()
	if r.analysis == nil || r.runCtx == nil {
		// Only reached when analysis was inconclusive.
		r.complete(StageInvalidate, schemas.RunStageSkipped, started,
			"analyzer inconclusive — no change record to invalidate against (D14)")
		return
	}
	_, err := statemachine.MarkSuspectsFromAnalysis(r.ctx, statemachine.SuspectDriverInput{
		Store:         r.store,
		Graph:         r.runCtx.Graph,
		Analysis:      *r.analysis,
		Git:           r.git,
		Head:          r.input.Head,
		RunID:         r.runID,
		PolicyVersion: r.policyVersion,
		At:            r.input.Clock(),
	})
	if err != nil {
		r.failure = &stageFailure{stage: StageInvalidate, err: err}
		r.complete(StageInvalidate, schemas.RunStageFailed, started, describeError(err))
		return
	}
	r.complete(StageInvalidate, schemas.RunStageOk, started)
}

func (r *pipelineRun) jevStage() {
	started := hrNow()
	active := r.runCtx

	// TS parity: `ports.decision.decide(...)` on an UNWIRED engine throws a
	// TypeError on the property read, which the stage's own catch absorbs into
	// the generic degradation path (REVIEW routing, never a panic, never the
	// IGNORE route — an unwired engine is not a routing verdict).
	if r.ports.Decision == nil {
		r.jevFailure(started, false, errors.New("cannot read properties of undefined (reading 'decide')"))
		r.jevPostProcess()
		return
	}

	// Bounded relevance context (ARCH §3.3, M3): the CHANGE-TOUCHED mapped
	// docs — derived the same way retrieval is, the reverse index over
	// analysis.files (the ComputeUnknownCoverage seam's BuildReverseIndex)
	// intersected with the manifest mappings (per changed path → mapped docs)
	// — NOT all graph.mappings. Deduped and sorted by docPath for
	// determinism; empty when the analysis was inconclusive (honest empty
	// candidate set — never a fabricated "no docs affected", D14/V-P5-8).
	// Bounded by maxRelevanceDocs (labeled budget data), deliberately distinct
	// from the writer-stage maxDocsInvestigated so the writer overflow branch
	// stays reachable.
	var relevancePaths []RelevanceDocTarget
	if r.analysis != nil {
		changedPaths := make([]string, 0, len(r.analysis.Files))
		seen := make(map[string]struct{}, len(r.analysis.Files))
		for _, file := range r.analysis.Files {
			if _, duplicate := seen[file.Path]; duplicate {
				continue
			}
			seen[file.Path] = struct{}{}
			changedPaths = append(changedPaths, file.Path)
		}
		index, err := graph.BuildReverseIndex(active.Graph, changedPaths)
		if err != nil {
			r.jevFailure(started, false, err)
			r.jevPostProcess()
			return
		}
		affected := make([]string, 0, len(index.AffectedDocs))
		for docPath := range index.AffectedDocs {
			affected = append(affected, docPath)
		}
		sort.SliceStable(affected, func(i, j int) bool { return affected[i] < affected[j] })
		relevancePaths = make([]RelevanceDocTarget, 0, len(affected))
		for _, docPath := range affected {
			relevancePaths = append(relevancePaths, RelevanceDocTarget{
				DocPath: docPath,
				Mode:    manifestModeOf(active.Manifest, docPath),
			})
		}
	}
	relevance, err := BuildRelevanceContext(active.Graph, relevancePaths, active.Budgets.MaxRelevanceDocs)
	if err != nil {
		r.jevFailure(started, true, err)
		r.jevPostProcess()
		return
	}
	r.relevance = &relevance

	raw, err := r.ports.Decision.Decide(DecisionStageInput{
		Ctx:       *active,
		Relevance: relevance,
		Analysis:  r.analysis,
	})
	if err != nil {
		if isJevConfigError(err) {
			// P5-T02 (ARCH §11.3): JEV CONFIG ERRORS ARE NOT ABLATIONS — a
			// configuration defect must SURFACE as a distinct typed failure,
			// never degrade to REVIEW. The walk still persists the failed run
			// record (provenance for failures) before the typed rethrow.
			r.jevConfigFailure(started, err)
			return
		}
		r.jevFailure(started, false, err)
		r.jevPostProcess()
		return
	}

	// P5-T02 inter-stage contract (ARCH §3.3 JEV → orchestrator edge): the
	// decision payload from the UNTRUSTED decision port is schema-validated
	// BEFORE any use — the application validates every edge (ARCH §13).
	var validated *schemas.Decision
	if raw.Decision != nil {
		parsed, parseErr := ParseDecisionValue(raw.Decision)
		if parseErr != nil {
			r.jevContractViolation(started, parseErr)
			r.jevPostProcess()
			return
		}
		validated = parsed
	}
	result := DecisionStageResult{
		Decision:      validated,
		Abstained:     raw.Abstained,
		AbstainReason: raw.AbstainReason,
		Telemetry:     raw.Telemetry,
	}
	r.decisionResult = &result
	resolved := ResolveRouting(result)
	r.routing = resolved.Routing
	if resolved.Abstain != nil {
		r.pushAbstain(nil, resolved.Abstain.Reason, resolved.Abstain.Detail, "")
	}
	r.complete(StageJev, schemas.RunStageOk, started)
	r.jevPostProcess()
}

// jevPostProcess is the post-catch code every non-config JEV path runs: the
// CONTINUE readiness gate (a CONTINUE routing is only executable against an
// analyzed change — without one the run degrades to REVIEW honestly, D14) and
// the one-shot early signal (P5-T08/T11).
func (r *pipelineRun) jevPostProcess() {
	if r.routing == RoutingContinue && r.analysis == nil {
		r.routing = RoutingReview
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage:  string(StageRetrieval),
			Kind:   schemas.DegradationAnalyzerInconclusive,
			Detail: "CONTINUE is not executable without a change record (analyzer inconclusive) — routed to REVIEW (D14)",
		})
	}
	// Early signal (P5-T08/T11): post once after the decision, final:false.
	if r.ports.EarlySignal != nil {
		at := r.input.Clock()
		summary := DecisionSummary{Routing: r.routing}
		if r.decisionResult != nil && r.decisionResult.Decision != nil {
			summary.Confidence = floatPtr(r.decisionResult.Decision.Confidence)
			summary.CandidateCount = len(r.decisionResult.Decision.Candidates)
		}
		r.postedPayload = &EarlySignalPayload{
			RunID:           r.runID,
			Key:             r.key,
			DecisionSummary: summary,
			At:              at,
			Final:           false,
		}
		postErr := r.ports.EarlySignal.Post(*r.postedPayload)
		if postErr != nil {
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage:  string(StageJev),
				Kind:   schemas.DegradationSignalSinkFailed,
				Detail: "early signal post failed: " + describeError(postErr),
			})
		} else {
			posted := at
			r.postedAt = &posted
		}
	}
}

// jevFailure is the JEV-stage catch body (TS the catch's non-config edges):
// the bounding violation degrades with the canonical cap_exceeded reason, a
// port failure degrades with model_unavailable — BOTH onto REVIEW routing
// (abstain, don't guess). JEV config defects are NOT ablations; they surface
// in jevConfigFailure.
func (r *pipelineRun) jevFailure(started time.Time, overflow bool, err error) {
	reason := schemas.AbstainReasonModelUnavailable
	if overflow {
		reason = schemas.AbstainReasonCapExceeded
	}
	detail := fmt.Sprintf("decision stage failed: %s — routed to REVIEW (abstain, don't guess)", describeError(err))
	if overflow {
		detail = fmt.Sprintf("%s — routed to REVIEW (fail to review, never auto)", err.Error())
	}
	r.decisionResult = &DecisionStageResult{Abstained: true, AbstainReason: &reason}
	r.routing = RoutingReview
	r.pushAbstain(nil, reason, detail, "")
	kind := schemas.DegradationJevUnavailable
	if overflow {
		kind = schemas.DegradationCapOverflow
	}
	r.degradations = append(r.degradations, schemas.Degradation{
		Stage:  string(StageJev),
		Kind:   kind,
		Detail: detail,
	})
	note := describeError(err)
	if overflow {
		note = err.Error()
	}
	r.complete(StageJev, schemas.RunStageFailed, started, note)
}

// jevContractViolation is the JEV-stage catch body for a decision-edge
// contract violation (P5-T02; ARCH §3.4): it degrades EXACTLY like a JEV
// abstention — REVIEW routing + abstain record + jev-unavailable degradation,
// each naming the decision edge — never a crash, never hand-patched.
func (r *pipelineRun) jevContractViolation(started time.Time, err error) {
	detail := contractEdgeDetail(err)
	result := &DecisionStageResult{
		Abstained:     true,
		AbstainReason: abstainReasonPtr(schemas.AbstainReasonSchemaViolation),
	}
	r.decisionResult = result
	r.routing = RoutingReview
	r.pushAbstain(nil, schemas.AbstainReasonSchemaViolation,
		fmt.Sprintf("decision-edge contract violation (docsync.decision/v1): %s — routed to review (abstain, don't guess)", detail), "")
	r.degradations = append(r.degradations, schemas.Degradation{
		Stage: string(StageJev),
		Kind:  schemas.DegradationJevUnavailable,
		Detail: fmt.Sprintf("decision-edge contract violation (docsync.decision/v1): %s — routed to REVIEW (abstain, don't guess)",
			detail),
	})
	r.complete(StageJev, schemas.RunStageFailed, started,
		fmt.Sprintf("decision-edge contract violation: %s", detail))
}

// jevConfigFailure is the typed JEV-config failure (P5-T02; ARCH §11.3): a
// configuration defect must SURFACE as the distinct typed failure, never
// degrade to REVIEW. The walk still persists the failed run record (provenance
// for failures) before the typed rethrow (the PipelineConfigError keeps its
// identity through the final failure rethrow).
func (r *pipelineRun) jevConfigFailure(started time.Time, err error) {
	configError := NewPipelineConfigError(
		fmt.Sprintf("decision stage rejected its configuration (JEV_CONFIG): %s — "+
			"config defects surface, they never degrade to review (ARCH §11.3)", describeError(err)),
		StageJev, err)
	r.failure = &stageFailure{stage: StageJev, err: configError}
	r.complete(StageJev, schemas.RunStageFailed, started, describeError(configError))
}

// contractEdgeDetail renders a StageContractError's message (the TS
// `error.message` — the edge-validating detail, without the coded wrapper).
func contractEdgeDetail(err error) string {
	if _, ok := StageContractEdgeOf(err); ok {
		var contractErr *StageContractError
		if asStageContractError(err, &contractErr) {
			return contractErr.Detail()
		}
	}
	return describeError(err)
}

func abstainReasonPtr(reason schemas.AbstainReason) *schemas.AbstainReason { return &reason }

func (r *pipelineRun) retrievalStage() {
	started := hrNow()
	active := r.runCtx

	// m2 (V1 deviation, declared vs ARCH §3.3): the retrieval edge does not
	// carry the JevDecision — the engine input has no such member; V1 retrieval
	// is deterministic and does not consume the decision. Revisit Phase 11.
	//
	// M3c (read + reconciled): the outcome candidate list is bounded by
	// threading the run's maxDocsInvestigated as the engine cap. The engine
	// caps PER LEVEL (no global-cap seam in V1) and truncates deterministically
	// in ranked order — never silently: dropped candidates are reported in
	// result.overflow and surfaced below as a cap-overflow degradation. Because
	// the cap is per-level, candidates beyond the run cap still arrive as
	// retrieval output, so the writer stage remains the bounded-loop
	// enforcement point (V-P5-11).
	if r.analysis == nil {
		// TS: `(analysis as ChangeAnalysis).files` throws on the null
		// inconclusive path — caught by the stage's own catch below.
		err := fmt.Errorf("cannot read properties of null (reading 'files')")
		r.failure = &stageFailure{stage: StageRetrieval, err: err}
		r.complete(StageRetrieval, schemas.RunStageFailed, started, describeError(err))
		return
	}
	retrievalCap := active.Budgets.MaxDocsInvestigated

	// P11-T04 completion (V-P11-4): ALL six levels are active. Levels 3–4 run
	// when the run computed their evidence inputs — the symbols_detail rows
	// ride the analysis (level 3), the OpenAPI delta and key-level config
	// diffs are derived here from the changed API/config files (level 4), and
	// the stored claims feed the deterministic claim-mention strategies.
	evidence := deriveRetrievalEvidence(r.ctx, r.git, r.baseSha, r.headSha, r.analysis.Files)
	if evidence.Unparseable > 0 {
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage: string(StageRetrieval),
			Kind:  schemas.DegradationContentUnavailable,
			Detail: fmt.Sprintf("%d changed API/config file(s) could not be read/parsed "+
				"for level-4 retrieval evidence — declared gap, never silent (D14)", evidence.Unparseable),
		})
	}
	storedClaims, err := loadStoredClaims(r.store)
	if err != nil {
		r.failure = &stageFailure{stage: StageRetrieval, err: err}
		r.complete(StageRetrieval, schemas.RunStageFailed, started, describeError(err))
		return
	}
	retrievalCaps := retrieval.RetrievalCaps{
		retrieval.RetrievalLevel1: retrievalCap,
		retrieval.RetrievalLevel2: retrievalCap,
		retrieval.RetrievalLevel3: retrievalCap,
		retrieval.RetrievalLevel4: retrievalCap,
		retrieval.RetrievalLevel5: retrievalCap,
		retrieval.RetrievalLevel6: retrievalCap,
	}
	var apiDelta *retrieval.ApiDelta
	if evidence.ApiDelta != nil {
		apiDelta = &retrieval.ApiDelta{Deltas: make([]retrieval.ApiDeltaEntry, 0, len(evidence.ApiDelta.Deltas))}
		for _, delta := range evidence.ApiDelta.Deltas {
			apiDelta.Deltas = append(apiDelta.Deltas, retrieval.ApiDeltaEntry{Location: delta.Location})
		}
	}
	var configDiff []retrieval.ConfigKeyDiff
	for _, diff := range evidence.ConfigDiff {
		configDiff = append(configDiff, retrieval.ConfigKeyDiff{
			Key:          diff.Key,
			Change:       retrieval.ConfigKeyDiffChange(diff.Change),
			SecretMasked: diff.SecretMasked,
		})
	}
	result, err := retrieval.RetrieveCandidates(retrieval.RetrieveCandidatesInput{
		ChangeAnalysis: *r.analysis,
		Graph:          active.Graph,
		Manifest:       active.Manifest,
		Claims:         storedClaims,
		Caps:           retrievalCaps,
		ApiDelta:       apiDelta,
		ConfigDiff:     configDiff,
	})
	if err != nil {
		r.failure = &stageFailure{stage: StageRetrieval, err: err}
		r.complete(StageRetrieval, schemas.RunStageFailed, started, describeError(err))
		return
	}
	for _, dropped := range result.Overflow {
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage: string(StageRetrieval),
			Kind:  schemas.DegradationCapOverflow,
			Detail: fmt.Sprintf("retrieval level %d capped at maxDocsInvestigated=%d — "+
				"%d candidate(s) beyond the cap dropped deterministically in ranked order "+
				"(reported, never silent — D14)", dropped.Level, retrievalCap, dropped.Dropped),
		})
	}
	r.retrievalCandidates = result.Candidates
	for _, candidate := range result.Candidates {
		levels := make([]int, 0, len(candidate.Levels))
		for _, level := range candidate.Levels {
			levels = append(levels, int(level))
		}
		provenance := make([]schemas.RunCandidateProvenance, 0, len(candidate.Provenance))
		for _, entry := range candidate.Provenance {
			wire := schemas.RunCandidateProvenance{
				Level:  int(entry.Level),
				Source: entry.Source,
			}
			if entry.Score != nil {
				wire.Score = floatPtr(float64(*entry.Score))
			}
			if entry.Stale != nil {
				wire.Stale = entry.Stale
			}
			// P11-T04: structured per-level evidence rides the run record
			// (MANDATORY at levels 3–4, ARCH §4.5; additive optional schema
			// field).
			switch evidence := entry.Evidence.(type) {
			case retrieval.RetrievalSymbolEvidence:
				wire.Evidence = &schemas.RunCandidateLevelEvidence{
					Symbols: append([]string{}, evidence.Symbols...),
				}
			case retrieval.RetrievalApiSchemaEvidence:
				wire.Evidence = &schemas.RunCandidateLevelEvidence{
					Locations: append([]string{}, evidence.Locations...),
					Keys:      append([]string{}, evidence.Keys...),
				}
			}
			provenance = append(provenance, wire)
		}
		r.candidates = append(r.candidates, schemas.RunCandidate{
			DocPath:      candidate.Doc,
			Levels:       levels,
			Provenance:   provenance,
			SemanticOnly: candidate.SemanticOnly,
			MaxAction:    schemas.PipelineMaxAction(string(candidate.MaxAction)),
		})
	}
	r.complete(StageRetrieval, schemas.RunStageOk, started)
}

// writerStage is the P5-T12 (OD-R10) bounded-concurrency writer stage: parallel
// per-doc writer investigations behind an IN-PROCESS pool (no worker threads —
// worker isolation is reserved for A10 memory pressure) capped at
// budgets.maxConcurrentDocs concurrent calls. Outcome equivalence with the
// sequential loop is structural: the per-candidate PRE-CALL gates (baseline
// short-circuit seam, patch-budget gate, relevance guard, head-content read)
// stay SEQUENTIAL and in candidate order, each call's input snapshots the hash
// map at ITS gate time, buffered calls flush in chunks bounded by the remaining
// patch room, and settled results are applied STRICTLY in candidate order —
// every observable outcome depends on candidate order only, never on
// completion order.
func (r *pipelineRun) writerStage() error {
	started := hrNow()
	active := r.runCtx

	inBudget := r.retrievalCandidates
	if len(inBudget) > active.Budgets.MaxDocsInvestigated {
		inBudget = inBudget[:active.Budgets.MaxDocsInvestigated]
	}
	var overflowDocs []retrieval.RetrievalCandidate
	if len(r.retrievalCandidates) > active.Budgets.MaxDocsInvestigated {
		overflowDocs = r.retrievalCandidates[active.Budgets.MaxDocsInvestigated:]
	}
	for _, candidate := range overflowDocs {
		r.ensureDoc(candidate.Doc, docBase{
			policyOutcome:   pipelineOutcomePtr(schemas.PipelineOutcomeReview),
			partialCoverage: boolPtr(true),
		})
	}
	if len(overflowDocs) > 0 {
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage: string(StageWriter),
			Kind:  schemas.DegradationCapOverflow,
			Detail: fmt.Sprintf("candidate fan-out capped at maxDocsInvestigated=%d — %d doc(s) beyond the cap carry partial_coverage (V-P5-11)",
				active.Budgets.MaxDocsInvestigated, len(overflowDocs)),
		})
	}

	patchCount := 0
	writerCalls := 0
	writerFailures := 0
	patchBudgetOverflow := false
	expectedBaseContentHashes := map[string]string{}

	// Application logic first (P5-T12): the per-result switch, VERBATIM from
	// the sequential loop (success switch + catch mapping), reused for every
	// settled result so application is order-stable.
	applyWriterError := func(docPath string, err error) {
		writerFailures++
		if IsStageContractError(err) {
			detail := contractEdgeDetail(err)
			// P5-T02 (ARCH §3.4): a schema-violating patch is treated as a
			// writer abstain (canonical schema_violation reason) — it NEVER
			// enters pendingPatches/verification (fail to less automation).
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage: string(StageWriter),
				Kind:  schemas.DegradationProviderOutage,
				Detail: fmt.Sprintf("writer patch-edge contract violation (docsync.doc-patch/v1) for %s: %s — treated as a writer abstain; the patch never enters verification (fail to less automation)",
					docPath, detail),
			})
			r.updateDoc(docPath, docPatch{
				policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview),
				abstain: &schemas.RunDocumentAbstain{
					Reason: string(schemas.AbstainReasonSchemaViolation),
					Detail: fmt.Sprintf("writer patch-edge contract violation: %s — routed to review (abstain, don't guess)", detail),
				},
			})
			r.pushAbstain(&docPath, schemas.AbstainReasonSchemaViolation,
				fmt.Sprintf("writer patch-edge contract violation: %s", detail), "")
			return
		}
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage: string(StageWriter),
			Kind:  schemas.DegradationProviderOutage,
			Detail: fmt.Sprintf("writer port failed for %s: %s — routed to review (fail to less automation)",
				docPath, describeError(err)),
		})
		r.updateDoc(docPath, docPatch{
			policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview),
			abstain: &schemas.RunDocumentAbstain{
				Reason: string(schemas.AbstainReasonModelUnavailable),
				Detail: fmt.Sprintf("writer port failed: %s — routed to review (fail to less automation)",
					describeError(err)),
			},
		})
		r.pushAbstain(&docPath, schemas.AbstainReasonModelUnavailable,
			fmt.Sprintf("writer port failed: %s", describeError(err)), "")
	}

	applyWriterResult := func(candidate retrieval.RetrievalCandidate, settled settledWriterCall) {
		docPath := candidate.Doc
		if settled.err != nil {
			applyWriterError(docPath, settled.err)
			return
		}
		result := settled.result
		switch result.Kind {
		case WriterResultPatch:
			patchCount++
			r.pendingPatches = append(r.pendingPatches, pendingPatch{docPath: docPath, patch: *result.Patch})
		case WriterResultFinding:
			r.findings = append(r.findings, schemas.RunFinding{
				DocPath: &docPath,
				Kind:    "potentially-stale",
				Detail:  result.Finding.Detail,
			})
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage: string(StageWriter),
				Kind:  schemas.DegradationWriterTimeout,
				Detail: fmt.Sprintf("writer timeout for %s: %s — potentially-stale finding emitted without a patch",
					docPath, result.Finding.Detail),
			})
			r.updateDoc(docPath, docPatch{policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview)})
		case WriterResultAbstain:
			reason := toAbstainReason(&result.Reason)
			r.updateDoc(docPath, docPatch{
				policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview),
				abstain: &schemas.RunDocumentAbstain{
					Reason: string(reason),
					Detail: result.Detail,
				},
			})
			r.pushAbstain(&docPath, reason, result.Detail, "")
		case WriterResultNoop:
			// m3: the noop rationale is intentionally NOT carried into the
			// outcome — RunDocument is a closed schema, and the 'ignore'
			// policy_outcome plus the writer-stage skip (no patch, no
			// verification, no abstain) IS the record of the noop.
			r.updateDoc(docPath, docPatch{policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeIgnore)})
		}
	}

	// Bounded in-process flush (OD-R10; no worker threads): the buffered calls
	// run through a worker pool capped at maxConcurrentDocs. The pool only
	// READS shared state: every call's input carries a call-time snapshot of
	// the hash map, and the shared context is not mutated while calls are in
	// flight. A degenerate override (non-finite / < 1) would deadlock the
	// pool, so the cap clamps to >= 1.
	concurrencyCap := active.Budgets.MaxConcurrentDocs
	if concurrencyCap < 1 {
		concurrencyCap = 1
	}
	var buffered []bufferedWriterCall
	flush := func() error {
		if len(buffered) == 0 {
			return nil
		}
		calls := buffered
		buffered = nil
		settled := make([]settledWriterCall, len(calls))
		var next int64
		var waitGroup sync.WaitGroup
		worker := func() {
			defer waitGroup.Done()
			for {
				index := int(atomic.AddInt64(&next, 1) - 1)
				if index >= len(calls) {
					return
				}
				raw, err := r.ports.Writer.Write(calls[index].input)
				if err != nil {
					settled[index] = settledWriterCall{err: err}
					continue
				}
				// P5-T02 inter-stage contract (ARCH §3.3 SONNET AGENT →
				// VERIFICATION edge): the patch payload from the UNTRUSTED
				// writer port is schema-validated BEFORE it can enter
				// pendingPatches.
				if raw.Kind == WriterResultPatch {
					parsed, parseErr := ParsePatchValue(raw.Patch)
					if parseErr != nil {
						settled[index] = settledWriterCall{err: parseErr}
						continue
					}
					raw.Patch = parsed
				}
				result := raw
				settled[index] = settledWriterCall{result: &result}
			}
		}
		workers := concurrencyCap
		if workers > len(calls) {
			workers = len(calls)
		}
		waitGroup.Add(workers)
		for worker_ := 0; worker_ < workers; worker_++ {
			go worker()
		}
		waitGroup.Wait()
		// Deterministic application order: candidate order, never completion
		// order. Every index is settled exactly once before the pool drains —
		// the guard only fails loud on an internal invariant break.
		for index := range calls {
			if settled[index].result == nil && settled[index].err == nil {
				return NewPipelineRunError(
					fmt.Sprintf("internal: writer pool lost the settled result for call index %d", index),
					StageWriter, nil)
			}
			applyWriterResult(calls[index].candidate, settled[index])
		}
		return nil
	}

	// Remaining patch room for the current chunk: flushing once the buffer
	// reaches this bound guarantees no application inside the chunk can exceed
	// the patch budget, so the pre-call budget gate below decides the
	// writer-call set EXACTLY like the sequential loop.
	patchRoom := func() int {
		room := active.Budgets.MaxPatchesPerRun - patchCount
		if room < 0 {
			return 0
		}
		return room
	}

	for _, candidate := range inBudget {
		docPath := candidate.Doc
		shortCircuit := EvaluateBaselineShortCircuit(r.ctx, docPath, r.store, r.git, r.headSha)
		r.ensureDoc(docPath, docBase{baselineRun: &shortCircuit.Run})
		// D14 fail-closed: a failed baseline resolution is DECLARED, never
		// swallowed — the candidate proceeds to the writer (fail to more
		// investigation) with an honest degradation record.
		if shortCircuit.Inconclusive {
			detail := shortCircuit.InconclusiveDetail
			if detail == "" {
				detail = "unknown error"
			}
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage: string(StageWriter),
				Kind:  schemas.DegradationAnalyzerInconclusive,
				Detail: fmt.Sprintf("baseline resolution failed — no short-circuit, writer investigating (fail to more investigation): %s",
					detail),
			})
		}
		if shortCircuit.ShortCircuit {
			r.updateDoc(docPath, docPatch{policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeIgnore)})
			continue
		}
		// Patch-budget gate, pre-call, in candidate order.
		if patchCount >= active.Budgets.MaxPatchesPerRun {
			r.updateDoc(docPath, docPatch{
				policyOutcome:   pipelineOutcomePtr(schemas.PipelineOutcomeReview),
				partialCoverage: boolPtr(true),
			})
			patchBudgetOverflow = true
			continue
		}
		if r.relevance == nil {
			r.updateDoc(docPath, docPatch{
				policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview),
				abstain: &schemas.RunDocumentAbstain{
					Reason: string(schemas.AbstainReasonOther),
					Detail: "relevance context absent",
				},
			})
			r.pushAbstain(&docPath, schemas.AbstainReasonOther, "relevance context absent", "")
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage:  string(StageWriter),
				Kind:   schemas.DegradationContentUnavailable,
				Detail: "relevance context absent — writer skipped, routed to review with evidence",
			})
			continue
		}
		content, err := r.git.CatFile(r.ctx, r.headSha, docPath)
		if err != nil {
			detail := fmt.Sprintf("doc content unavailable at head: %s", describeError(err))
			r.updateDoc(docPath, docPatch{
				policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview),
				abstain: &schemas.RunDocumentAbstain{
					Reason: string(schemas.AbstainReasonOther),
					Detail: detail,
				},
			})
			r.pushAbstain(&docPath, schemas.AbstainReasonOther, detail, "")
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage: string(StageWriter),
				Kind:  schemas.DegradationContentUnavailable,
				Detail: fmt.Sprintf("target doc content unreadable at head — writer skipped, routed to review with evidence (%s)",
					describeError(err)),
			})
			continue
		}
		expectedBaseContentHashes[docPath] = docutil.ContentHash(content.Content)
		writerCalls++
		// Call-time snapshot (P5-T12): the writer sees exactly the hash entries
		// the sequential loop had accumulated when it issued THIS call — never
		// entries added by later candidates' gates.
		hashes := make(map[string]string, len(expectedBaseContentHashes))
		for key, value := range expectedBaseContentHashes {
			hashes[key] = value
		}
		buffered = append(buffered, bufferedWriterCall{
			candidate: candidate,
			input: WriterStageInput{
				Ctx:                       *active,
				Doc:                       WorkspaceDocTarget{DocPath: docPath, Mode: manifestModeOf(active.Manifest, docPath)},
				Analysis:                  r.analysis,
				Decision:                  r.decisionDecision(),
				Relevance:                 *r.relevance,
				ExpectedBaseContentHashes: hashes,
			},
		})
		if len(buffered) >= patchRoom() {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}

	if patchBudgetOverflow {
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage: string(StageWriter),
			Kind:  schemas.DegradationCapOverflow,
			Detail: fmt.Sprintf("patch budget capped at maxPatchesPerRun=%d — doc(s) beyond the cap carry partial_coverage (V-P5-11)",
				active.Budgets.MaxPatchesPerRun),
		})
	}
	writerStatus := schemas.RunStageSkipped
	switch {
	case writerFailures > 0:
		writerStatus = schemas.RunStageFailed
	case writerCalls > 0:
		writerStatus = schemas.RunStageOk
	}
	if writerCalls == 0 {
		r.complete(StageWriter, writerStatus, started, "no in-budget candidates accepted a writer session")
		return nil
	}
	r.complete(StageWriter, writerStatus, started)
	return nil
}

// decisionDecision is the TS `decisionResult?.decision ?? null` read.
func (r *pipelineRun) decisionDecision() *schemas.Decision {
	if r.decisionResult == nil {
		return nil
	}
	return r.decisionResult.Decision
}

func (r *pipelineRun) verificationStage() {
	started := hrNow()
	active := r.runCtx
	verifyCalls := 0
	verifyFailures := 0
	for _, pending := range r.pendingPatches {
		verifyCalls++
		result, err := r.ports.Verification.Verify(VerificationStageInput{
			Ctx:        *active,
			Patch:      pending.patch,
			DocContent: nil,
		})
		var parsedReport *schemas.VerificationReport
		if err == nil {
			if result.Report == nil {
				err = errors.New("verification port returned no report")
			} else {
				// P5-T02 inter-stage contract (ARCH §3.3 VERIFICATION → POLICY
				// edge): the report from the UNTRUSTED verifier port is
				// schema-validated BEFORE any use.
				parsedReport, err = ParseVerificationValue(result.Report)
			}
		}
		if err != nil {
			verifyFailures++
			if IsStageContractError(err) {
				detail := contractEdgeDetail(err)
				// P5-T02 (ARCH §3.4): a schema-violating report takes EXACTLY
				// the verification-port-failure path — verifier_uncertain
				// abstain + review — plus an edge-naming degradation record.
				r.degradations = append(r.degradations, schemas.Degradation{
					Stage: string(StageVerification),
					Kind:  schemas.DegradationVerificationFailed,
					Detail: fmt.Sprintf("verification-edge contract violation (docsync.verification-report/v1) for %s: %s — routed to review with evidence detail (never silent, D13/D14)",
						pending.docPath, detail),
				})
				r.updateDoc(pending.docPath, docPatch{
					policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview),
					abstain: &schemas.RunDocumentAbstain{
						Reason: string(schemas.AbstainReasonVerifierUncertain),
						Detail: fmt.Sprintf("verification-edge contract violation: %s — routed to review (fail to less automation)", detail),
					},
				})
				r.pushAbstain(&pending.docPath, schemas.AbstainReasonVerifierUncertain,
					fmt.Sprintf("verification-edge contract violation: %s", detail), "")
				continue
			}
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage: string(StageVerification),
				Kind:  schemas.DegradationVerificationFailed,
				Detail: fmt.Sprintf("verification failed for %s: %s — routed to review with evidence detail (never silent, D13/D14)",
					pending.docPath, describeError(err)),
			})
			r.updateDoc(pending.docPath, docPatch{
				policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview),
				abstain: &schemas.RunDocumentAbstain{
					Reason: string(schemas.AbstainReasonVerifierUncertain),
					Detail: fmt.Sprintf("verification port failed: %s — routed to review (fail to less automation)",
						describeError(err)),
				},
			})
			r.pushAbstain(&pending.docPath, schemas.AbstainReasonVerifierUncertain,
				fmt.Sprintf("verification port failed: %s", describeError(err)), "")
			continue
		}
		r.patchVerified = append(r.patchVerified, verifiedPatch{docPath: pending.docPath, verification: *parsedReport})
		r.updateDoc(pending.docPath, docPatch{verification: &schemas.RunDocumentVerification{
			Verdict:          parsedReport.Verdict,
			AutoEligible:     parsedReport.AutoEligible,
			AutoBlockReasons: append([]string{}, parsedReport.AutoBlockReasons...),
			PatchRef:         strPtr(parsedReport.PatchRef),
		}})
	}
	verifyStatus := schemas.RunStageSkipped
	switch {
	case verifyFailures > 0:
		verifyStatus = schemas.RunStageFailed
	case verifyCalls > 0:
		verifyStatus = schemas.RunStageOk
	}
	if verifyCalls == 0 {
		r.complete(StageVerification, verifyStatus, started, "no patch to verify")
		return
	}
	r.complete(StageVerification, verifyStatus, started)
}

// policyTarget is one policy-stage resolution target (TS the targets array).
type policyTarget struct {
	docPath      string
	verification *schemas.VerificationReport
}

func (r *pipelineRun) policyStage() {
	started := hrNow()
	active := r.runCtx
	decision := r.decisionDecision()
	var targets []policyTarget
	if r.routing == RoutingReview {
		if decision != nil {
			for _, candidate := range decision.Candidates {
				if !manifestContains(active.Manifest, candidate) {
					continue
				}
				targets = append(targets, policyTarget{docPath: candidate, verification: nil})
			}
		}
	} else {
		for _, resolved := range r.patchVerified {
			verification := resolved.verification
			targets = append(targets, policyTarget{docPath: resolved.docPath, verification: &verification})
		}
	}
	if len(targets) == 0 {
		r.complete(StagePolicy, schemas.RunStageSkipped, started,
			"no documents to resolve through the policy stage")
		return
	}
	policyFailures := 0
	for _, target := range targets {
		r.ensureDoc(target.docPath, docBase{})
		outcome, abstain, queueEventID, resolveErr := r.resolvePolicyTarget(active, target, decision)
		if resolveErr != nil {
			policyFailures++
			r.degradations = append(r.degradations, schemas.Degradation{
				Stage: string(StagePolicy),
				Kind:  schemas.DegradationProviderOutage,
				Detail: fmt.Sprintf("policy port failed for %s: %s — routed to review (fail to less automation)",
					target.docPath, describeError(resolveErr)),
			})
			r.updateDoc(target.docPath, docPatch{
				policyOutcome: pipelineOutcomePtr(schemas.PipelineOutcomeReview),
				abstain: &schemas.RunDocumentAbstain{
					Reason: string(schemas.AbstainReasonModelUnavailable),
					Detail: fmt.Sprintf("policy port failed: %s — routed to review (fail to less automation)",
						describeError(resolveErr)),
				},
			})
			r.pushAbstain(&target.docPath, schemas.AbstainReasonModelUnavailable,
				fmt.Sprintf("policy port failed: %s", describeError(resolveErr)), "")
			continue
		}
		r.updateDoc(target.docPath, docPatch{
			policyOutcome: pipelineOutcomePtr(outcome),
			abstain:       abstain,
		})
		// M2 (ARCH §5.2): the policy adapter's review-queue linkage rides onto
		// the run-level abstain record as queue_event_id when the abstain
		// landed in the review queue.
		if abstain != nil {
			r.pushAbstain(&target.docPath, schemas.AbstainReason(abstain.Reason), abstain.Detail, queueEventID)
		}
	}
	if policyFailures > 0 {
		r.complete(StagePolicy, schemas.RunStageFailed, started)
		return
	}
	r.complete(StagePolicy, schemas.RunStageOk, started)
}

// resolvePolicyTarget is one policy-stage resolution: the port call, the
// review-path downgrade (ARCH §7.1), the port-abstain mapping and the V-P5-6
// verification gate (P5-T09). The port call itself is extracted so the caller
// can distinguish a port FAILURE (degradation path) from a resolved outcome.
// queueEventID is the adapter's review-queue linkage (empty when none).
func (r *pipelineRun) resolvePolicyTarget(
	active *RunContext,
	target policyTarget,
	decision *schemas.Decision,
) (schemas.PipelineOutcome, *schemas.RunDocumentAbstain, string, error) {
	resolved, err := r.ports.Policy.Resolve(PolicyStageInput{
		Ctx:          *active,
		DocPath:      target.docPath,
		Decision:     decision,
		Verification: target.verification,
	})
	if err != nil {
		return "", nil, "", err
	}
	queueEventID := ""
	if resolved.QueueEventID != nil {
		queueEventID = *resolved.QueueEventID
	}
	outcome := resolved.Outcome
	var abstain *schemas.RunDocumentAbstain
	if target.verification == nil &&
		(outcome == schemas.PipelineOutcomeAuto ||
			outcome == schemas.PipelineOutcomeSuggest ||
			outcome == schemas.PipelineOutcomeBlocked) {
		abstain = &schemas.RunDocumentAbstain{
			Reason: string(schemas.AbstainReasonPolicyGap),
			Detail: fmt.Sprintf("policy resolved '%s' on the review path without verification evidence — downgraded to review (fail to less automation)",
				outcome),
		}
		outcome = schemas.PipelineOutcomeReview
	}
	if resolved.Abstain != nil {
		abstain = &schemas.RunDocumentAbstain{
			Reason: string(toAbstainReason(&resolved.Abstain.Reason)),
			Detail: resolved.Abstain.Detail,
		}
	}
	// V-P5-6 (P5-T09): verification gate on the CONTINUE/patch path — a
	// resolved 'auto'/'suggest' outcome for a target that HAS verification
	// evidence may not survive a rejecting or non-auto-eligible report. Runs on
	// the RESOLVED outcome, after the port-abstain mapping above: a port abstain
	// that already reached 'review' makes this a no-op. 'blocked' is NEVER
	// loosened — the verification block already rides the doc record, evidence
	// attached, never silent (D13/D14).
	if target.verification != nil &&
		(outcome == schemas.PipelineOutcomeAuto || outcome == schemas.PipelineOutcomeSuggest) &&
		(target.verification.Verdict == schemas.VerdictReject ||
			!target.verification.AutoEligible) {
		blockReasons := "none"
		if len(target.verification.AutoBlockReasons) > 0 {
			blockReasons = strings.Join(target.verification.AutoBlockReasons, ", ")
		}
		gateDetail := fmt.Sprintf("verification gate downgraded '%s' to review — verdict '%s', block reasons: %s (V-P5-6; fail to less automation)",
			outcome, target.verification.Verdict, blockReasons)
		outcome = schemas.PipelineOutcomeReview
		abstain = &schemas.RunDocumentAbstain{
			Reason: string(schemas.AbstainReasonVerifierUncertain),
			Detail: gateDetail,
		}
		r.degradations = append(r.degradations, schemas.Degradation{
			Stage:  string(StagePolicy),
			Kind:   schemas.DegradationVerificationFailed,
			Detail: gateDetail,
		})
	}
	return outcome, abstain, queueEventID, nil
}

// manifestContains reports whether the manifest allowlists docPath (D7).
func manifestContains(manifest schemas.Manifest, docPath string) bool {
	for _, entry := range manifest.Documents {
		if entry.Path == docPath {
			return true
		}
	}
	return false
}

func pipelineOutcomePtr(outcome schemas.PipelineOutcome) *schemas.PipelineOutcome { return &outcome }

func boolPtr(value bool) *bool { return &value }

/* --------------------------------------------- outcome assembly + gate */

// assembleOutcome maps the completed run onto the frozen RunOutcome (V-P5-16)
// and SELF-VALIDATES the result against RUN_OUTCOME_SCHEMA (fail closed): the
// orchestrator never returns an outcome its own schema would reject.
func (r *pipelineRun) assembleOutcome() (*schemas.RunOutcome, error) {
	modelBindings := []schemas.RunModelBinding{}
	var telemetry *StageTelemetry
	if r.decisionResult != nil {
		telemetry = r.decisionResult.Telemetry
	}
	if telemetry != nil && telemetry.Model != nil {
		binding := schemas.RunModelBinding{Role: "decision-engine", Model: *telemetry.Model}
		if telemetry.Provider != nil {
			binding.Provider = telemetry.Provider
		}
		modelBindings = append(modelBindings, binding)
	}
	// m4 (D3 full run-level bindings): the writer model comes from the FIRST
	// applied patch's provenance (DocPatch provenance pins writer_model — the
	// actually-routed writer model, never the role's configured default), the
	// judge model from the FIRST verification report's judge pin (the routed
	// model per D3; an abstaining judge carries no judge block). Both are
	// optional: skipped when their evidence is absent. The writer/judge session
	// ids are NOT derivable at run level — the composition layer pins them.
	if len(r.pendingPatches) > 0 && r.pendingPatches[0].patch.Provenance.WriterModel != "" {
		modelBindings = append(modelBindings, schemas.RunModelBinding{
			Role:  "writer",
			Model: r.pendingPatches[0].patch.Provenance.WriterModel,
		})
	}
	if len(r.patchVerified) > 0 {
		if judge := r.patchVerified[0].verification.Judge; judge != nil && judge.Model != "" {
			modelBindings = append(modelBindings, schemas.RunModelBinding{Role: "judge", Model: judge.Model})
		}
	}
	// D7 (P5-T15): changed paths matched by no manifest mapping — computed from
	// the analysis record + the map-stage graph. Empty (or analysis
	// inconclusive) → the field stays absent; never a fabricated claim.
	var documentGraph *graph.DocumentGraph
	if r.runCtx != nil {
		documentGraph = &r.runCtx.Graph
	}
	unknownCoverage, err := ComputeUnknownCoverage(r.analysis, documentGraph)
	if err != nil {
		return nil, err
	}
	// P5-T17 (ARCH §10.6): usage attribution from the decision telemetry,
	// pinned to the producing policy version; absent when nothing was routed.
	var usage *schemas.RunUsage
	if telemetry != nil && telemetry.Usage != nil {
		usage = &schemas.RunUsage{
			PromptTokens:     telemetry.Usage.PromptTokens,
			CompletionTokens: telemetry.Usage.CompletionTokens,
			TotalTokens:      telemetry.Usage.TotalTokens,
			Cost:             telemetry.Usage.Cost,
			PolicyVersion:    r.policyVersion,
		}
	}
	status := schemas.RunStatusCompleted
	if len(r.degradations) > 0 {
		status = schemas.RunStatusDegraded
	}
	documents := make([]schemas.RunDocument, 0, len(r.docsOrder))
	for _, docPath := range r.docsOrder {
		documents = append(documents, *r.docsByPath[docPath])
	}
	stages := make([]schemas.PipelineStage, 0, len(PipelineStages))
	for _, stage := range PipelineStages {
		trace := r.traces[stage]
		stages = append(stages, schemas.PipelineStage{
			Stage:     string(stage),
			Status:    trace.status,
			LatencyMS: trace.latencyMS,
			Note:      trace.note,
		})
	}
	var concurrencyGroup *string
	if r.input.PR != nil {
		group := ConcurrencyGroupFor(r.input.Repo, r.input.PR.Number)
		concurrencyGroup = &group
	}
	finishedAt := r.input.Clock()
	finishedAtPtr := &finishedAt
	outcome := &schemas.RunOutcome{
		Schema:           schemas.RunOutcomeSchemaID,
		RunID:            r.runID,
		Key:              r.key,
		StartedAt:        &r.startedAt,
		FinishedAt:       finishedAtPtr,
		Status:           status,
		Stages:           stages,
		Documents:        documents,
		Abstains:         r.abstains,
		Candidates:       r.candidates,
		Provenance:       schemas.RunProvenance{PolicyVersion: r.policyVersion, ModelBindings: modelBindings},
		Usage:            usage,
		Degradations:     r.degradations,
		Findings:         r.findings,
		EarlySignal:      schemas.EarlySignal{PostedAt: r.postedAt, UpdatedAt: r.updatedAt},
		ConcurrencyGroup: concurrencyGroup,
		StateChurn:       state.ChurnToRecordFields(r.churn.Snapshot()),
	}
	if len(unknownCoverage) > 0 {
		outcome.UnknownCoverage = unknownCoverage
	}
	raw, marshalErr := json.Marshal(outcome)
	if marshalErr == nil {
		var parsed *schemas.RunOutcome
		parsed, marshalErr = schemas.ParseRunOutcome(raw)
		if marshalErr == nil {
			return parsed, nil
		}
	}
	return nil, NewPipelineRunError(
		fmt.Sprintf("run outcome failed self-validation (RUN_OUTCOME_SCHEMA) — refusing to return an invalid outcome (fail closed): %s",
			describeError(marshalErr)),
		StagePersist, marshalErr)
}
