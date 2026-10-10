package policy

// Ported from packages/policy/src/orchestrator/decision-stage.ts (P5-T03;
// PLAN §6.4 `jev` stage; ARCH §3.5, §13) — the policy-side port the
// orchestrator wires into the deterministic stage machine. It wraps the
// Phase-2 Jev runtime (RunJev: fail-closed universal-question gate, ONE port
// call, single answer-contract gate, JEV_ROUTING_TABLE routing, fail-closed
// DECISION_SCHEMA parse) and maps its outcome onto the core
// `DecisionStageResult` shape (@docsync/core src/orchestrator/types.ts).
//
// The decision stage is a DETERMINISTIC stage-graph node around the
// decision-engine role: production orchestration is deterministic code and
// LLMs are bounded leaf stages with schema-validated I/O (the application
// decides, never the model). The V-P5-4 integration edge: this module is the
// seam the recorded-double openrouter suite drives end-to-end —
// schema-violating model output burns the gateway's bounded corrective
// retries, the adapter abstains (`schema_violation`), and THIS stage maps
// that to an abstained result with the canonical reason. NOTHING is ever
// hand-patched into a decision.
//
// Mapping contract (binding):
//   - A decision outcome → decision PASSTHROUGH (the schema-valid decision
//     the runtime emitted — the stage never rebuilds it) + telemetry from the
//     port's JevTelemetry: D3/DX-P2-2 provenance truthfulness — `model` is
//     the ACTUALLY ROUTED model half (`routed_model`, never the configured
//     default) and `provider` is the actually-routed provider half when the
//     route carried one. `usage` (P5-T17; ARCH §10.6) is the port's
//     token/cost usage block VERBATIM when the route carried one (nothing
//     routed → no usage: the degraded/abstained paths attribute wall-time
//     only).
//   - An abstain outcome → {decision: null, abstained: true, abstainReason}
//     with the canonical operational reason passed through verbatim.
//   - A port that REJECTS (transport/gateway failure, or any non-config error
//     escaping the runtime) degrades to the operational abstain
//     `model_unavailable` — stage-level fail-closed degradation, not a crash:
//     run-level failure mapping (exit 3) stays the runner's job (P5-T11).
//     Stage wall-time on this degraded path is attributed from the INJECTED
//     clock; nothing was routed, so no model/provider half exists to report.
//   - JevConfigError PROPAGATES verbatim: a compiled-workflow defect is a
//     configuration error (exit-code 2 territory) thrown BEFORE any model
//     call — never masked as review noise or an abstention.
//
// Memoization (P5-T10; ARCH §7.1 item 4, D6, OD-R11): when deps.Memo is
// wired, Decide consults the Phase-2 JevMemoCache BEFORE the port call,
// keying through the cache's OWN API (`KeyFor`) on the stage input's stable
// change fingerprint (JevStageMemoDiffSubset — exactly the JevChangeRefs
// triple), `deps.Workflow.Policy.PolicyVersion`, the optional model-binding
// identity (deps.MemoModelBinding, P12-T03/D3), and the namespace passthrough
// (deps.MemoNamespace, default production). HIT → the cached result returns
// verbatim (the additive `CacheHit` marker; the original telemetry is
// preserved via a store-time result sidecar; the port is skipped). MISS →
// the port runs and a successful decision is stored under the key. Mirroring
// the cache's own semantics, ONLY schema-valid Decisions are cached: an
// operational abstain carries no Decision and is NEVER memoized (every
// abstain re-runs the port); a decision-level abstention IS a valid Decision
// and is cached like any decision. Shadow-key isolation (D6) is INHERITED
// from the cache — the namespace is salted into the key AND storage is
// partitioned per namespace — so a shadow-stored result never returns for a
// production key and vice versa; an invalid namespace fails closed (returns
// the error before the port call, the same vocabulary as JevConfigError)
// instead of silently missing, while ANY other memo failure degrades to the
// uncached path — the memo never breaks the stage. In-memory V1 via the
// Phase-2 primitive (OD-R11); file-backed cross-run reuse is revisited at
// Phase 10.
//
// Structural seam (mirrors the Phase-2 DecisionEnginePort pattern):
// `@docsync/policy` must NOT import `@docsync/core` (OD-1), so the result and
// stage shapes are defined here structurally identical to core's
// `DecisionStageResult` / `DecisionStagePort`.
//
// Pure logic only: the only async is the port call; no wall clock — time is
// the injected clock (nil → the deterministic zero clock).

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------------ shapes */

// JevDecisionStageInput is the stage input (TS: JevDecisionStageInput):
// RunJev's input minus the two members this stage wires itself (the compiled
// workflow and the port). Nothing is invented — every member forwards to
// RunJev verbatim.
type JevDecisionStageInput struct {
	// Change is the change refs forwarded to the port verbatim.
	Change JevChangeRefs
	// AnalysisContext is the deterministic analysis-derived grounding context.
	AnalysisContext JevAnalysisContext
	// UntrustedContent is the untrusted repository content blocks; nil == absent.
	UntrustedContent []string
	// TemperatureOverride tunes the call, not the change identity; nil == absent.
	TemperatureOverride *float64
	// CachePrefix tunes the call, not the change identity; nil == absent.
	CachePrefix *bool
}

// JevDecisionStageTelemetry is the stage telemetry (TS:
// JevDecisionStageTelemetry) — structurally identical to core's
// DecisionStageResult telemetry member. `Model`/`Provider` carry the
// ACTUALLY ROUTED halves (D3/DX-P2-2), never the configured defaults.
// `Usage` (P5-T17; ARCH §10.6) is the port's JevTelemetry.Usage block
// VERBATIM, present only when the route actually reached the port — additive
// + optional, so existing consumers are unaffected.
type JevDecisionStageTelemetry struct {
	LatencyMS int
	Model     *string
	Provider  *string
	Usage     *JevUsage
}

// JevDecisionStageResult is the stage result (TS: JevDecisionStageResult) —
// structurally identical to core's `DecisionStageResult` (the orchestrator
// consumes this shape directly; P5-T06). `Decision` is nil exactly when the
// stage abstained; `AbstainReason` is the canonical operational reason
// (`AbstainReason` of docsync.decision/v1).
type JevDecisionStageResult struct {
	Decision      *schemas.Decision
	Abstained     bool
	AbstainReason *schemas.AbstainReason
	Telemetry     *JevDecisionStageTelemetry
	// CacheHit is the additive memo marker (P5-T10): true exactly when the
	// result came from a memo cache HIT — the decision AND telemetry are the
	// original store-time values returned verbatim and the port was never
	// invoked. False on a fresh (port-computed) result, so core-shape
	// consumers are unaffected.
	CacheHit bool
	// Detail is the additive bridge detail (review M1): populated ONLY by
	// BridgeCoreDecisionStage on the no-analysis abstain path — the
	// evidence-backed abstention detail for richer consumers (abstain
	// bookkeeping). Additive optional member: core's DecisionStageResult has
	// no detail field.
	Detail *string
}

// JevMemoCachePick is the structural memo seam (P5-T10; TS:
// JevMemoCachePick = Pick<JevMemoCache, 'keyFor' | 'get' | 'set'>): the
// public slice of the Phase-2 JevMemoCache this stage consumes.
// `*JevMemoCache` instances satisfy it directly and test doubles satisfy it
// structurally (the same seam pattern as the decision-engine port). Key
// derivation, lookup and store all flow through the cache's OWN
// namespace-validating API, so D6 shadow-key isolation is inherited, never
// re-implemented here.
type JevMemoCachePick interface {
	KeyFor(input JevMemoKeyInput) (string, error)
	Get(namespace, key string) (*schemas.Decision, error)
	Set(namespace, key string, decision *schemas.Decision) error
}

// JevDecisionStageDeps is the stage dependencies (TS: JevDecisionStageDeps)
// — the Phase-2 seams plus the injected clock.
type JevDecisionStageDeps struct {
	// Port is the structural decision-engine port (openrouter adapter wired
	// in; see DecisionEnginePort).
	Port DecisionEnginePort
	// Workflow is the compiled workflow — exactly the `compiled` parameter
	// RunJev takes (its composed questions gate the universal-coverage check).
	Workflow CompiledPolicyWorkflow
	// Clock is the injected clock (monotonic counter or epoch ms — only
	// differences are used). Attributed as stage wall-time on the DEGRADED
	// path (port rejection); everywhere else the port's JevTelemetry carries
	// the latency. Nil → the deterministic zero clock (never the wall clock).
	Clock func() int
	// Memo is the optional memo cache (P5-T10; OD-R11) — the Phase-2
	// JevMemoCache primitive, consumed through its OWN keying/isolation API
	// (JevMemoCachePick). Nil → the stage behaves exactly as without
	// memoization.
	Memo JevMemoCachePick
	// MemoNamespace is the memo namespace passthrough (D6 shadow-key
	// isolation) — which partition of the memo cache this stage reads and
	// writes. Empty → ProductionNamespace; a shadow run passes
	// 'shadow:<binding>'. Isolation itself is INHERITED from the cache
	// (namespace salted into the key + per-namespace storage partitions), so
	// a shadow-stored result never returns for a production key and vice
	// versa. An invalid namespace fails closed: the error returns before the
	// port call rather than silently missing.
	MemoNamespace string
	// MemoModelBinding is the optional model-binding identity salted into the
	// memo key (P12-T03; ARCH §3.5, §7.1 item 4; D3; PLAN §13.6 decision
	// "shadow decision caches keyed `policy_version` (+ model binding)"). The
	// canonical D3 binding identity of the decision-engine role (e.g.
	// `${model}#${provider}`) — injected by the caller so derivation stays
	// deterministic and test-pinnable. The same diff-subset under a
	// DIFFERENT binding MISSES: swapping the decision-engine model
	// invalidates memoized decisions exactly like a policy-version bump. Nil
	// → keys derive exactly as before P12-T03 (additive: existing wirings and
	// their pinned key shapes are unaffected).
	MemoModelBinding *string
}

// DecisionEnginePort (declared ONCE per package, in runtime.go — the port of
// packages/policy/src/jev/runtime.ts, exactly like the TS import graph: the
// decision stage imports the port from the runtime module rather than
// redeclaring it) is the Stage-1 structural port this stage consumes. The
// @docsync/openrouter `DecisionEngineRole` adapter satisfies it structurally
// (identical member shapes); policy never imports openrouter — the dependency
// runs the other way (OD-1).
//
// TODO(policy-orchestrator-stages): the concrete openrouter decision-engine
// adapter (internal/openrouter — port of packages/openrouter's
// DecisionEngineRole) is wired into CreateJevDecisionStage's deps.Port below
// by the orchestrator. Do NOT import internal/openrouter from this package:
// the adapter satisfies the interface structurally with zero import.

// JevDecisionStage is the Jev decision-stage port (TS: JevDecisionStage) —
// the structural counterpart of core's `DecisionStagePort`. The input side
// differs on purpose: the orchestrator adapter (P5-T06) translates the core
// `{ ctx, relevance }` input into this Jev domain input; the RESULT side is
// the pinned structural edge. Errors surface the fail-closed vocabulary:
// JevConfigError (compiled-workflow defect, exit-2 territory) and
// InvalidMemoNamespaceError (D6) propagate; everything else degrades to an
// abstained result.
type JevDecisionStage interface {
	Decide(input JevDecisionStageInput) (JevDecisionStageResult, error)
}

/* -------------------------------------------------------------- the stage */

// JevStageMemoDiffSubset is the orchestrator-owned reduction of the stage
// input into the memo's diff subset (P5-T10; TS: jevStageMemoDiffSubset;
// jev-memo.ts assigns the ChangeAnalysis → diff-subset reduction to the
// ORCHESTRATOR — this is that reduction). The stage keys the memo on the
// STABLE CHANGE FINGERPRINT of its input: exactly the JevChangeRefs triple
// (commit, diff_ref, change_analysis_ref), reduced losslessly into a single
// MemoDiffFile row (path = commit, status = diff_ref, flags =
// [change_analysis_ref]) — deterministic, order-free, and free of invented
// vocabulary. The remaining input members are deliberately NOT keyed: the
// analysis context and untrusted content are deterministic functions of the
// referenced change analysis (`change_analysis_ref`), and
// `temperatureOverride`/`cachePrefix` tune the call, not the change identity.
// Canonicalization, the policy version and the namespace salting happen
// inside the cache's own KeyFor (D6 layers).
func JevStageMemoDiffSubset(change JevChangeRefs) MemoDiffSubset {
	return MemoDiffSubset{
		Files: []MemoDiffFile{
			{
				Path:   change.Commit,
				Status: change.DiffRef,
				Flags:  []string{change.ChangeAnalysisRef},
			},
		},
	}
}

// zeroClock is the deterministic default clock (TS: ZERO_CLOCK) —
// degraded-path wall-time is simply not attributed when the runner injects
// none.
func zeroClock() int { return 0 }

// jevTelemetryOf maps the port telemetry onto the stage telemetry (TS:
// telemetryOf — routed halves verbatim). `Usage` (P5-T17) threads VERBATIM
// when the port carried it — the degraded paths build their telemetry
// literals without usage (nothing was routed).
func jevTelemetryOf(telemetry JevTelemetry) *JevDecisionStageTelemetry {
	out := &JevDecisionStageTelemetry{
		LatencyMS: telemetry.LatencyMS,
		Model:     jevStageStringPtr(telemetry.RoutedModel),
	}
	if telemetry.Provider != nil {
		provider := *telemetry.Provider
		out.Provider = &provider
	}
	if telemetry.Usage != nil {
		out.Usage = telemetry.Usage
	}
	return out
}

// CreateJevDecisionStage creates the Jev decision stage (P5-T03; TS:
// createJevDecisionStage). One RunJev call per Decide; the bounded-retry
// gateway already ran inside the port (V-P5-4) — the stage never retries,
// never hand-patches, and never fabricates a decision.
func CreateJevDecisionStage(deps JevDecisionStageDeps) JevDecisionStage {
	clock := deps.Clock
	if clock == nil {
		clock = zeroClock
	}
	namespace := deps.MemoNamespace
	if namespace == "" {
		namespace = ProductionNamespace
	}
	return &jevDecisionStage{
		deps:          deps,
		clock:         clock,
		memoNamespace: namespace,
		// Store-time result sidecar (P5-T10): memo key → the ORIGINAL stage
		// result, written only after the cache store succeeded, so
		// same-stage hits return the original result — telemetry preserved —
		// verbatim. The memo decision remains the durable D6-isolated store;
		// a cross-instance hit (a cache shared beyond this stage instance)
		// falls back to truthful this-call attribution (nothing was routed
		// here → no model/provider half).
		storedResults: make(map[string]JevDecisionStageResult),
	}
}

type jevDecisionStage struct {
	deps          JevDecisionStageDeps
	clock         func() int
	memoNamespace string

	mu            sync.Mutex
	storedResults map[string]JevDecisionStageResult
}

func (s *jevDecisionStage) Decide(input JevDecisionStageInput) (JevDecisionStageResult, error) {
	startedAt := s.clock()

	// Memo gate (P5-T10): derive the key through the cache's OWN API BEFORE
	// the port call; the namespace is validated here (D6 fail closed). A
	// namespace violation returns the error (never a silent miss); ANY other
	// memo failure degrades to the uncached path — the port runs and the
	// stage is unaffected.
	memoKey := ""
	memoKeyed := false
	var memoHit *schemas.Decision
	if s.deps.Memo != nil {
		key, err := s.deps.Memo.KeyFor(JevMemoKeyInput{
			DiffSubset:    JevStageMemoDiffSubset(input.Change),
			PolicyVersion: s.deps.Workflow.Policy.PolicyVersion,
			Namespace:     s.memoNamespace,
			ModelBinding:  s.deps.MemoModelBinding,
		})
		if err != nil {
			if isInvalidMemoNamespace(err) {
				return JevDecisionStageResult{}, err
			}
			// ANY other keyFor failure degrades to the uncached path (the TS
			// catch resets both memoKey and memoHit).
		} else {
			memoKey, memoKeyed = key, true
			hit, getErr := s.deps.Memo.Get(s.memoNamespace, key)
			if getErr != nil {
				if isInvalidMemoNamespace(getErr) {
					return JevDecisionStageResult{}, getErr
				}
				// A failed lookup is a miss AND the key is abandoned — the
				// uncached path with no store (the TS catch resets both).
				memoKey, memoKeyed = "", false
			} else if hit != nil {
				memoHit = hit
			}
		}
	}
	if memoHit != nil && memoKeyed {
		// HIT → the cached result verbatim, port skipped. Prefer the
		// original result (original telemetry preserved); a cross-instance
		// hit falls back to this-call wall-time from the injected clock —
		// nothing was routed, so no model/provider half exists to report.
		s.mu.Lock()
		original, ok := s.storedResults[memoKey]
		s.mu.Unlock()
		if ok {
			hit := original
			hit.CacheHit = true
			return hit, nil
		}
		return JevDecisionStageResult{
			Decision:  memoHit,
			Abstained: false,
			Telemetry: &JevDecisionStageTelemetry{LatencyMS: s.clock() - startedAt},
			CacheHit:  true,
		}, nil
	}

	outcome, err := RunJev(RunJevInput{
		Compiled:            s.deps.Workflow,
		Port:                s.deps.Port,
		Change:              input.Change,
		AnalysisContext:     input.AnalysisContext,
		UntrustedContent:    input.UntrustedContent,
		TemperatureOverride: input.TemperatureOverride,
		CachePrefix:         input.CachePrefix,
	})
	if err != nil {
		// Configuration defect (exit-2 territory): propagate verbatim —
		// NEVER masked into an abstention or review noise.
		var cfgErr *JevConfigError
		if errors.As(err, &cfgErr) {
			return JevDecisionStageResult{}, err
		}
		// Operational degradation: the port call failed (transport, gateway,
		// or an unexpected stage error). Degrade to the operational abstain
		// `model_unavailable` — fail closed at the stage boundary; the runner
		// (P5-T11) owns run-level failure mapping. Nothing was routed, so no
		// model/provider half exists to report.
		return JevDecisionStageResult{
			Decision:      nil,
			Abstained:     true,
			AbstainReason: jevStageAbstainReason(schemas.AbstainReasonModelUnavailable),
			Telemetry:     &JevDecisionStageTelemetry{LatencyMS: s.clock() - startedAt},
		}, nil
	}
	if outcome.Kind == JevOutcomeDecision {
		result := JevDecisionStageResult{
			Decision:  outcome.Decision,
			Abstained: false,
			Telemetry: jevTelemetryOf(outcome.Telemetry),
		}
		// MISS → store the decision under the key (P5-T10), best-effort: a
		// memo store failure never breaks the stage — the port result is
		// returned as-is and simply not memoized. A namespace violation
		// still fails closed (D6) rather than being swallowed.
		if s.deps.Memo != nil && memoKeyed {
			if setErr := s.deps.Memo.Set(s.memoNamespace, memoKey, outcome.Decision); setErr != nil {
				if isInvalidMemoNamespace(setErr) {
					return JevDecisionStageResult{}, setErr
				}
			} else {
				s.mu.Lock()
				s.storedResults[memoKey] = result
				s.mu.Unlock()
			}
		}
		return result, nil
	}
	// Operational abstain passthrough: the canonical reason verbatim; the
	// decision stays nil (never fabricated out of an abstention). NOT
	// memoized (P5-T10): JevMemoCache stores schema-valid Decision objects
	// only, so an operational abstain re-runs the port every time.
	return JevDecisionStageResult{
		Decision:      nil,
		Abstained:     true,
		AbstainReason: jevStageAbstainReason(outcome.Reason),
		Telemetry:     jevTelemetryOf(outcome.Telemetry),
	}, nil
}

/* -------------------------------------------------- core bridge (review M1) */

// groundingConfidencePerformed / groundingConfidenceSkipped are the declared
// V1 grounding confidence for the deterministic analysis-derived context
// (BuildJevStageInput) — the certainty of a PATH-LEVEL V1 analysis record:
// deep analysis performed vs skipped (TS: GROUNDING_CONFIDENCE_PERFORMED /
// GROUNDING_CONFIDENCE_SKIPPED). DECLARED CONSTANTS, not calibrated
// thresholds: D11 keeps policy thresholds (0.94/0.72) as per-policy-version
// data; nothing here gates on them.
const (
	groundingConfidencePerformed = 0.6
	groundingConfidenceSkipped   = 0.3
)

// BuildJevStageInputExtra holds the caller-supplied extras for
// BuildJevStageInput (TS: BuildJevStageInputExtra). `UntrustedContent`
// REPLACES the derived default when provided: the orchestrator may hold the
// REAL diff excerpts (strictly richer than the record's flag surface), and
// duplicated content blocks would only burn tokens. Containment tagging is
// the gateway's job (openrouter client `composeUserMessage`) — never done
// here.
type BuildJevStageInputExtra struct {
	UntrustedContent []string
}

// BuildJevStageInput builds the JevDecisionStageInput from a schema
// ChangeAnalysis record (review M1; TS: buildJevStageInput) — the pure
// ChangeAnalysis → Jev-stage mapping the core orchestrator adapter reuses.
// Deterministic, total, no clocks, no I/O.
//
// Mapping provenance (copied logic, cited):
//   - Change refs triple: `commit ← head_sha`, `diff_ref ← git:<base>..<head>`,
//     `change_analysis_ref ← run_id` — the P5-T06 decision-fixture pattern
//     (packages/openrouter/test/orchestrator-writer-stage.test.ts
//     `decisionFixture`; the core orchestrator tests' `fakeDecision`,
//     packages/core/test/orchestrator-e2e.test.ts) — the same stable change
//     fingerprint the memo keys on (JevStageMemoDiffSubset).
//   - Analysis context: the Phase-2 Stage-1 construction pattern
//     (packages/openrouter/test/integration-stage1.test.ts
//     `ANALYSIS_CONTEXT`, P2-T24) — changeKind / documentationImpact / risk /
//     confidence / universal-keyed grounding answers / candidates / evidence
//     — here derived from the record's OWN signal surface (file flags,
//     `dependencies_changed`, `deep_analysis`), conservative and fail-closed
//     (D14):
//     changeKind: trivial classifications first (`formatting_only` /
//     `test_only`), then `dependencies_changed` → `dependency`, api_diff →
//     `api_contract`, config_diff → `configuration`, `behavioral` as the
//     honest residual; documentationImpact: an EMPTY file set → `unknown`
//     (never a fabricated `none`), public surface (api_diff|symbols_changed)
//     or config_diff → `possible`, never `definite` (V1 path-level analysis
//     has no definite-impact knowledge — Phase 11 owns deeper understanding);
//     risk: `dependencies_changed` → `high`, surface/config signals →
//     `medium`, else `low`; confidence: the declared grounding constants
//     (performed vs skipped deep analysis); answers: grounding answers keyed
//     to the universal questions (NOT the model's returned answers), every
//     probability = the declared grounding confidence, `affected_doc_count`
//     honestly 0; candidates: [] (manifest knowledge is not derivable from a
//     bare record — D14); evidence: one machine-resolvable row per changed
//     file (ref = the repo-relative path; kind `api_diff` when the file
//     carries the api_diff signal, else `other`).
//   - untrustedContent: the record's own diff-bearing content surface — the
//     serialized ChangeAnalysis JSON as ONE block, per the Phase-2 pattern of
//     passing repository-content strings
//     (integration-stage1.test.ts); the gateway (openrouter client) owns
//     containment wrapping/escaping.
func BuildJevStageInput(analysis *schemas.ChangeAnalysis, extra *BuildJevStageInputExtra) JevDecisionStageInput {
	files := analysis.Files
	anyApiDiff := false
	anyConfigDiff := false
	anySymbolsChanged := false
	allFormattingOnly := len(files) > 0
	allTestOnly := len(files) > 0
	for i := range files {
		file := &files[i]
		if file.APIDiff != nil && *file.APIDiff {
			anyApiDiff = true
		}
		if file.ConfigDiff != nil && *file.ConfigDiff {
			anyConfigDiff = true
		}
		if file.SymbolsChanged != nil && *file.SymbolsChanged {
			anySymbolsChanged = true
		}
		if file.FormattingOnly == nil || !*file.FormattingOnly {
			allFormattingOnly = false
		}
		if file.TestOnly == nil || !*file.TestOnly {
			allTestOnly = false
		}
	}
	publicSurfaceChanged := anyApiDiff || anySymbolsChanged

	var changeKind schemas.ChangeKind
	switch {
	case allFormattingOnly:
		changeKind = schemas.ChangeKindFormattingOnly
	case allTestOnly:
		changeKind = schemas.ChangeKindTestOnly
	case analysis.DependenciesChanged:
		changeKind = schemas.ChangeKindDependency
	case anyApiDiff:
		changeKind = schemas.ChangeKindAPIContract
	case anyConfigDiff:
		changeKind = schemas.ChangeKindConfiguration
	default:
		changeKind = schemas.ChangeKindBehavioral
	}

	var documentationImpact schemas.DocumentationImpact
	switch {
	case len(files) == 0:
		documentationImpact = schemas.DocumentationImpactUnknown
	case publicSurfaceChanged || anyConfigDiff:
		documentationImpact = schemas.DocumentationImpactPossible
	default:
		documentationImpact = schemas.DocumentationImpactNone
	}

	var risk schemas.RiskLevel
	switch {
	case analysis.DependenciesChanged:
		risk = schemas.RiskLevelHigh
	case publicSurfaceChanged || anyConfigDiff:
		risk = schemas.RiskLevelMedium
	default:
		risk = schemas.RiskLevelLow
	}

	confidence := groundingConfidenceSkipped
	if analysis.DeepAnalysis.Status == schemas.DeepAnalysisPerformed {
		confidence = groundingConfidencePerformed
	}

	evidence := make([]schemas.Evidence, 0, len(files))
	for i := range files {
		file := &files[i]
		signals := make([]string, 0, 6)
		if file.APIDiff != nil && *file.APIDiff {
			signals = append(signals, "api_diff")
		}
		if file.ConfigDiff != nil && *file.ConfigDiff {
			signals = append(signals, "config_diff")
		}
		if file.SymbolsChanged != nil && *file.SymbolsChanged {
			signals = append(signals, "symbols_changed")
		}
		if file.TestOnly != nil && *file.TestOnly {
			signals = append(signals, "test_only")
		}
		if file.FormattingOnly != nil && *file.FormattingOnly {
			signals = append(signals, "formatting_only")
		}
		if file.Generated != nil && *file.Generated {
			signals = append(signals, "generated")
		}
		kind := schemas.EvidenceKindOther
		if file.APIDiff != nil && *file.APIDiff {
			kind = schemas.EvidenceKindAPIDiff
		}
		signalList := "none"
		if len(signals) > 0 {
			signalList = strings.Join(signals, ", ")
		}
		evidence = append(evidence, schemas.Evidence{
			Kind: kind,
			Ref:  file.Path,
			Note: "change-analysis signal(s): " + signalList +
				" — path-level ref (anchored refs are a declared V1 coverage gap)",
		})
	}

	analysisContext := JevAnalysisContext{
		ChangeKind:          changeKind,
		DocumentationImpact: documentationImpact,
		Risk:                risk,
		Confidence:          confidence,
		Answers: map[string]schemas.Answer{
			"public_behavior_changed": {Value: jevStageJSONBool(publicSurfaceChanged), Probability: confidence},
			"api_contract_changed":    {Value: jevStageJSONBool(anyApiDiff), Probability: confidence},
			"affected_doc_count":      {Value: jevStageJSONInt(0), Probability: confidence},
			"documentation_impact":    {Value: jevStageJSONString(string(documentationImpact)), Probability: confidence},
		},
		Candidates: []string{},
		Evidence:   evidence,
	}

	// untrustedContent: caller-supplied content REPLACES the derived default
	// (the orchestrator's real diff excerpts are strictly richer).
	untrustedContent := []string{jevStageJSONStringify(analysis)}
	if extra != nil && extra.UntrustedContent != nil {
		untrustedContent = extra.UntrustedContent
	}

	return JevDecisionStageInput{
		Change: JevChangeRefs{
			Commit:            analysis.HeadSHA,
			DiffRef:           "git:" + analysis.BaseSHA + ".." + analysis.HeadSHA,
			ChangeAnalysisRef: analysis.RunID,
		},
		AnalysisContext:  analysisContext,
		UntrustedContent: untrustedContent,
	}
}

// BridgeCoreDecisionStageInput is the structural input slice the bridge
// reads from the core DecisionStagePort input (`{ ctx, relevance, analysis }`
// — the P5-T06 consumer shape with the parallel `analysis: ChangeAnalysis |
// null` member) (TS: BridgeCoreDecisionStageInput). ONLY `Analysis` is read;
// every other member is ignored — policy never imports core (OD-1), so the
// seam is structural, not nominal. A nil Analysis == TS `analysis: null`.
type BridgeCoreDecisionStageInput struct {
	Analysis *schemas.ChangeAnalysis
}

// BridgeCoreDecisionStage is the bridge's decide surface (TS:
// BridgeCoreDecisionStage) — structurally ASSIGNABLE to core's
// `DecisionStagePort.decide` (a narrower input requirement is assignable
// under contravariance; the result side is the pinned
// JevDecisionStageResult edge).
type BridgeCoreDecisionStage interface {
	Decide(input BridgeCoreDecisionStageInput) (JevDecisionStageResult, error)
}

// NoAnalysisAbstainReason is the canonical no-analysis abstention reason
// (review M1; TS: NO_ANALYSIS_ABSTAIN_REASON): Jev cannot ground a decision
// without the change record — abstain, don't guess (invariant 6). `other` is
// the canonical abstain-reason vocabulary for an unattributable abstention;
// the runner ALSO independently downgrades a CONTINUE-without-analysis route,
// so this path never produces a fabricated decision and never reaches the
// port.
const NoAnalysisAbstainReason = schemas.AbstainReasonOther

// NoAnalysisAbstainDetail is the canonical no-analysis abstention detail
// (review M1; TS: NO_ANALYSIS_ABSTAIN_DETAIL).
const NoAnalysisAbstainDetail = "no change record — Jev cannot ground a decision"

// NewBridgeCoreDecisionStage bridges the Jev decision stage onto the
// core-shaped DecisionStagePort input (review M1; TS: bridgeCoreDecisionStage,
// an unexported factory — Go has no barrels, so the constructor is exported
// here for the P5-T06 orchestrator adapter that wires the bridge): a
// structural adapter taking `{ analysis }` (the rest of the core input is
// ignored) and calling BuildJevStageInput when a change record exists.
// `analysis: nil` → the canonical abstained result WITHOUT a port call — fail
// to review, never a guessed decision.
func NewBridgeCoreDecisionStage(stage JevDecisionStage) BridgeCoreDecisionStage {
	return &bridgeCoreDecisionStage{stage: stage}
}

type bridgeCoreDecisionStage struct {
	stage JevDecisionStage
}

func (b *bridgeCoreDecisionStage) Decide(input BridgeCoreDecisionStageInput) (JevDecisionStageResult, error) {
	if input.Analysis == nil {
		return JevDecisionStageResult{
			Decision:      nil,
			Abstained:     true,
			AbstainReason: jevStageAbstainReason(NoAnalysisAbstainReason),
			Detail:        jevStageStringPtr(NoAnalysisAbstainDetail),
		}, nil
	}
	return b.stage.Decide(BuildJevStageInput(input.Analysis, nil))
}

/* ------------------------------------------------------------------ helpers */

// isInvalidMemoNamespace reports whether err is a memo-namespace violation
// (D6 fail closed): the only memo error that propagates instead of degrading
// to the uncached path.
func isInvalidMemoNamespace(err error) bool {
	var nsErr *InvalidMemoNamespaceError
	return errors.As(err, &nsErr)
}

func jevStageAbstainReason(reason schemas.AbstainReason) *schemas.AbstainReason {
	return &reason
}

func jevStageStringPtr(s string) *string { return &s }

// jevStageJSONBool / jevStageJSONInt / jevStageJSONString render answer
// values as the raw JSON the wire format carries (the TS Answer `value` is
// `unknown` serialized by zod).
func jevStageJSONBool(b bool) json.RawMessage {
	if b {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

func jevStageJSONInt(n int) json.RawMessage {
	return json.RawMessage(strconv.Itoa(n))
}

func jevStageJSONString(s string) json.RawMessage {
	quoted, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return json.RawMessage(quoted)
}

// jevStageJSONStringify renders v exactly like the TypeScript JSON.stringify
// the bridge default uses (packages/policy/src/orchestrator/decision-stage.ts
// `buildJevStageInput`): struct field order (the schema declaration order),
// no HTML escaping. Not the canonical form — canonical JSON is
// docutil.CanonicalJSON's job; this is the untrusted-content block's
// JSON.stringify-compatible rendering.
func jevStageJSONStringify(v any) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// ChangeAnalysis is a plain data struct — unreachable.
		return ""
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
