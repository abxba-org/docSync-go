// Ported from packages/cli/src/compose.ts (P7-T01/T02, P7 wave 2) — the
// application-owned wiring that turns `docsync check`/`fix`/`pr` into a pipeline
// run over the Phase-5 composed stage adapters (internal/orchestrator owns the
// stage machine). The CLI owns composition ONLY: every stage behavior stays the
// leaf port; the deterministic core orchestrator decides what is permitted (D11).
//
// Fail-closed order (D8): manifest → policy → (offline? runtime) →
// models → credentials, ALL BEFORE any AI stage and any model-client
// construction. A configuration defect returns a typed CliConfigError (exit-2
// lane); the deterministic offline runtime (OD-R16) never constructs a model
// port. Exactly ONE manifest read per composition (its advisories ride the
// returned runtime — P12-T13; V-P12-12).
//
// DECLARED SEAM (orchestrator to rebind): the model-backed PipelinePorts
// (OpenRouter decision/writer adapters, policy bridge, three-tier verifier port)
// are built through ModelPortsBuilderImpl. While it is nil — the concurrent
// adapter wiring is not yet landed — an ONLINE run fails closed (D14) with an
// honest typed error; the offline path and every config/exit lane work without
// it. TODO(model-adapters).
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/analyzers/generic"
	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/validate"
)

/* ------------------------------------------------------------------ errors */

// CliConfigError is a configuration defect on the CLI composition path (exit-2
// lane, D8). It carries deterministic, human-rendered diagnostic lines
// (line-precise where the Phase-0 loader provides positions). Never produced
// after any AI stage ran.
type CliConfigError struct {
	// Diagnostics are the rendered diagnostic lines, deterministic order.
	Diagnostics []string
	msg         string
}

// Error renders the config-error message (the msg only; diagnostics render on
// separate lines by the caller).
func (e *CliConfigError) Error() string { return e.msg }

// Code returns the stable config error code (documenting the exit-2 lane).
func (e *CliConfigError) Code() string { return docutil.CodeConfig }

func newCliConfigError(msg string, diagnostics ...string) *CliConfigError {
	return &CliConfigError{msg: msg, Diagnostics: diagnostics}
}

// asCliConfigError reports whether err is (or wraps) a *CliConfigError.
func asCliConfigError(err error, target **CliConfigError) bool {
	return errors.As(err, target)
}

/* ------------------------------------------------------------------ shapes */

// CliRuntimeOptions configures createCliPipelineRuntime.
type CliRuntimeOptions struct {
	// RepoRoot is the absolute repository root.
	RepoRoot string
	// Repo is the repository run-key identity (`org/name`).
	Repo string
	// Env is the environment map (shape of process.env) — key source ONLY.
	Env map[string]string
	// Clock is the injected ISO-8601 clock.
	Clock func() string
	// Offline (OD-R16): deterministic-only runtime; skips credential resolution
	// and ALL model-client construction.
	Offline bool
	// ModelsPath overrides the default `<repoRoot>/.docsync/models.yml`.
	ModelsPath string
	// Transport is the model transport injection seam (recorded doubles for
	// tests, OD-2). Consumed by the model-ports builder; nil otherwise.
	Transport any
	// OnPatch is the patch-capture seam (P7-T08): invoked with each verified
	// patch object as it crosses the verification boundary — the single honest
	// capture point the fix flow resolves its patches through.
	OnPatch func(patch *schemas.DocPatch, docPath string)
}

// CliRuntimeRunInput is the input of CliRuntime.Run.
type CliRuntimeRunInput struct {
	// Base is the base ref/sha of the change window.
	Base string
	// Head is the head ref/sha of the change window (the CLI uses `HEAD`).
	Head string
	// PR is the PR number context when running against a pull request.
	PR *int
	// Budgets are overrides merged over the runner defaults.
	Budgets *orchestrator.PipelineBudgetOverrides
}

// CliRuntime is the composed runtime a check/fix/pr verb runs against.
type CliRuntime interface {
	// Run runs the pipeline once and returns the schema-validated outcome.
	Run(ctx context.Context, input CliRuntimeRunInput) (*schemas.RunOutcome, error)
	// Advisories surfaces the report-only org-policy advisories from the ONE
	// manifest read that gated this run (P12-T13; V-P12-12).
	Advisories() []string
}

// cliRuntime is the concrete CliRuntime implementation.
type cliRuntime struct {
	runFn      func(context.Context, CliRuntimeRunInput) (*schemas.RunOutcome, error)
	advisories []string
}

func (r *cliRuntime) Run(ctx context.Context, input CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	return r.runFn(ctx, input)
}

func (r *cliRuntime) Advisories() []string { return r.advisories }

/* ---------------------------------------------------------- analyzer lineup */

// CLI_ANALYZER_LINEUP is the REAL analyzer lineup injected into the pipeline
// (P7-T02; the m8 injection seam): TypeScript (tree-sitter stub — V1) first,
// generic path-level second (OD-4). It IS the TS CLI_ANALYZER_LINEUP, now wired
// to the landed analyzer packages (internal/analyzers/tstool +
// internal/analyzers/generic). The lineup is NEVER empty and never a silent
// stub: TypeScript resolves when its requirements are present, otherwise it
// degrades to path-level ANALYSIS in the run records — never silent.
func CLI_ANALYZER_LINEUP() []analysis.AnalyzerPlugin {
	return []analysis.AnalyzerPlugin{
		tstool.CreateTypeScriptAnalyzerStub(),
		generic.CreateGenericAnalyzer(),
	}
}

/* --------------------------------------------------------------- constants */

// OFFLINE_ABSTAIN_DETAIL is the exact offline abstain detail (binding wording).
const OFFLINE_ABSTAIN_DETAIL = "decision unavailable (offline)"

// MAX_EVIDENCE_SOURCES caps evidence workspace sources per verify call.
const MAX_EVIDENCE_SOURCES = 8

// GROUNDING_PROBABILITY_FALLBACK is the grounding fallback probability for a
// deterministic 'definite' upgrade of a path-level 'none' documentation impact.
const GROUNDING_PROBABILITY_FALLBACK = 0.6

// OUTAGE_DEGRADATION_KINDS are the outage-signature degradation kinds → the D14
// inconclusive lane (V-P8-5).
var outageDegradationKinds = map[schemas.DegradationKind]struct{}{
	schemas.DegradationAnalyzerInconclusive: {},
	schemas.DegradationJevUnavailable:       {},
	schemas.DegradationProviderOutage:       {},
}

/* --------------------------------------------------------------- config I/O */

// readOptionalFile reads a `.docsync/` file; the ok=false result means absent
// (ENOENT/ENOTDIR) — the caller treats absence as "not configured". Any other
// read failure is a CliConfigError (fail closed, never "assume empty").
func readOptionalFile(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "is a directory") ||
			strings.Contains(err.Error(), "not a directory") {
			return "", false, nil
		}
		return "", false, newCliConfigError(
			"docsync: failed to read " + path + ": " + describeErr(err))
	}
	return string(data), true, nil
}

// parseYAMLValue parses YAML text into an `any`; a parse failure is a
// CliConfigError (fail closed, D8).
func parseYAMLValue(source, label string) (any, error) {
	var value any
	if err := yaml.Unmarshal([]byte(source), &value); err != nil {
		return nil, newCliConfigError(
			"docsync: " + label + " is not valid YAML: " + describeErr(err))
	}
	return value, nil
}

// loadCompiledPolicy loads + compiles the policy workflow (P7-T02):
// `.docsync/policy.yml` when present (compiled with policy.CompilePolicy —
// invalid input fails closed with per-diagnostic lines), else the bootstrap
// policy v1 (the documented default `docsync init` does not scaffold).
func loadCompiledPolicy(repoRoot string) (*policy.CompiledPolicyWorkflow, error) {
	path := filepath.Join(repoRoot, schemas.DocSyncDir, "policy.yml")
	source, ok, err := readOptionalFile(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return policy.CompileBootstrapPolicyV1()
	}
	parsed, err := parseYAMLValue(source, ".docsync/policy.yml")
	if err != nil {
		return nil, err
	}
	result := policy.CompilePolicy(parsed)
	if !result.OK {
		diagnostics := make([]string, 0, len(result.Diagnostics))
		for _, d := range result.Diagnostics {
			diagnostics = append(diagnostics, policyDiagnosticRender(d))
		}
		return nil, newCliConfigError(
			"docsync: .docsync/policy.yml failed policy compilation (fail closed, D8)",
			diagnostics...)
	}
	return result.Compiled, nil
}

// policyDiagnosticRender renders one PolicyDiagnostic line (path: message).
func policyDiagnosticRender(d policy.PolicyDiagnostic) string {
	if d.Path == "" {
		return d.Message
	}
	return d.Path + ": " + d.Message
}

// loadModelsConfig loads + parses `.docsync/models.yml` (docsync.models/v1
// bindings). Missing or invalid bindings fail closed (D8): the gateway cannot
// run without its role bindings.
func loadModelsConfig(repoRoot, modelsPath string) (*openrouter.ExtendedModelsConfig, error) {
	path := modelsPath
	if path == "" {
		path = filepath.Join(repoRoot, schemas.DocSyncDir, "models.yml")
	}
	source, ok, err := readOptionalFile(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newCliConfigError(
			"docsync: " + path + " not found — model role bindings are required for a model-backed run " +
				"(fail closed, D8; run `docsync init` to scaffold)")
	}
	return loadModelsConfigYAML(source, ".docsync/models.yml")
}

// loadModelsConfigYAML parses a YAML models.yml source into the extended
// binding config: YAML is decoded to a generic value, re-encoded to canonical
// JSON, and validated by the gateway's JSON StrictDecode + extended-shape
// check. Missing required roles fail closed (D8). label names the file in
// diagnostics.
func loadModelsConfigYAML(source, label string) (*openrouter.ExtendedModelsConfig, error) {
	var raw any
	if err := yaml.Unmarshal([]byte(source), &raw); err != nil {
		return nil, newCliConfigError("docsync: " + label + " is not valid YAML: " + describeErr(err))
	}
	canonical, err := json.Marshal(raw)
	if err != nil {
		return nil, newCliConfigError("docsync: " + label + " could not be normalized: " + describeErr(err))
	}
	parsed, err := openrouter.ParseModelsConfig(canonical)
	if err != nil {
		return nil, newCliConfigError(
			"docsync: " + label + " failed models-config validation (fail closed, D8): " + describeErr(err))
	}
	missing := openrouter.MissingRequiredRoles(&parsed.Models)
	if len(missing) > 0 {
		return nil, newCliConfigError(
			"docsync: " + label + " is missing required role bindings: " + strings.Join(missing, ", ") +
				" (required: decision-engine, judge, writer)")
	}
	return &parsed.Models, nil
}

/* ------------------------------------------------------------- the runtime */

// ModelPorts are the model-backed pipeline ports an online run uses (the
// cross-boundary adapter surface the orchestrator rebinds).
type ModelPorts struct {
	Decision     orchestrator.DecisionStagePort
	Writer       orchestrator.WriterStagePort
	Verification orchestrator.VerificationStagePort
	Policy       orchestrator.PolicyStagePort
}

// ModelPortsOptions carries the inputs the model-ports builder needs.
type ModelPortsOptions struct {
	RepoRoot  string
	Repo      string
	Clock     func() string
	Git       *gitx.GitClient
	Compiled  *policy.CompiledPolicyWorkflow
	Models    *openrouter.ExtendedModelsConfig
	APIKey    string
	Env       map[string]string
	Transport any
	// OnPatch is the patch-capture seam forwarded to the verification port
	// (the fix composition's capture; nil in the check/pr compositions).
	OnPatch func(patch *schemas.DocPatch, docPath string)
}

// ModelPortsBuilderImpl builds the model-backed PipelinePorts for an online
// run. DECLARED SEAM: nil until the OpenRouter/policy/verifier adapter wiring
// lands (orchestrator rebinds). While nil an online run fails closed (D14).
var ModelPortsBuilderImpl func(ctx context.Context, opts ModelPortsOptions) (ModelPorts, error)

// createCliPipelineRuntime composes the CLI pipeline runtime (P7-T01/T02).
// Fail-closed order: manifest → policy → (offline? runtime) → models →
// credentials. Any configuration defect returns a CliConfigError BEFORE any
// model client exists (assertable, V-P7-4). Exactly ONE manifest read: the run's
// gate and the returned advisories come from the same read (P12-T13; V-P12-12).
func createCliPipelineRuntime(options CliRuntimeOptions) (CliRuntime, error) {
	/* 1a. Configuration (D8) — ValidateRepositoryConfig runs the FULL
	 * fail-closed gate (canonical layout → manifest → globs → environment) and
	 * renders every hard diagnostic line-precise; it NEVER throws for
	 * configuration problems. A hard error is exit-2 BEFORE any AI stage. */
	configOutcome, err := validate.ValidateRepositoryConfig(options.RepoRoot, validate.ValidateOptions{})
	if err != nil {
		// A loader/IO surprise propagates (a failed run must never look valid).
		return nil, err
	}
	if !configOutcome.OK {
		return nil, newCliConfigError(
			"docsync: .docsync/ configuration failed validation (fail closed, D8)",
			configOutcome.Rendered...)
	}
	manifest := configOutcome.Manifest

	// The report-only advisories of the SAME read (P12-T13; V-P12-12).
	advisories := advisoryMessages(configOutcome.Advisories)

	/* 1b. Policy (before models/credentials — a policy defect never needs the key). */
	compiled, err := loadCompiledPolicy(options.RepoRoot)
	if err != nil {
		return nil, err
	}
	policyVersion := extractCompiledPolicyVersion(compiled)

	/* 1c. Offline mode (OD-R16): deterministic-only runtime, NO model ports. */
	if options.Offline {
		return &cliRuntime{
			runFn:      createOfflineRunFn(options, *manifest, policyVersion),
			advisories: advisories,
		}, nil
	}

	/* 1d. Models bindings (D8). */
	modelsConfig, err := loadModelsConfig(options.RepoRoot, options.ModelsPath)
	if err != nil {
		return nil, err
	}

	/* 1e. Credentials (env-only, §3 rule 8) — fail closed BEFORE any AI stage. */
	credentials := openrouter.ResolveOpenRouterApiKey(options.Env)
	if !credentials.OK {
		return nil, newCliConfigError("docsync: " + credentials.Reason)
	}

	/* 2. The online runtime (the model ports come from the declared seam). */
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: options.RepoRoot})
	if err != nil {
		return nil, err
	}
	clock := options.Clock
	repoRoot := options.RepoRoot
	repo := options.Repo

	runFn := func(ctx context.Context, input CliRuntimeRunInput) (*schemas.RunOutcome, error) {
		// Resolve the change window first (a bad ref is a RUNTIME failure,
		// exit-3 lane — the honest "cannot resolve base/head refs" message).
		if _, err := git.ResolveRev(ctx, input.Base); err != nil {
			return nil, orchestrator.NewPipelineRunError(
				"setup: cannot resolve base/head refs ('"+input.Base+"', '"+input.Head+"'): "+describeErr(err),
				"", err)
		}
		if _, err := git.ResolveRev(ctx, input.Head); err != nil {
			return nil, orchestrator.NewPipelineRunError(
				"setup: cannot resolve base/head refs ('"+input.Base+"', '"+input.Head+"'): "+describeErr(err),
				"", err)
		}
		// The model-ports builder is bound at init (model_ports.go); a nil
		// binding is a programming defect — fail closed (D14).
		if ModelPortsBuilderImpl == nil {
			return nil, docutil.New(docutil.CodeModel,
				"model adapter ports are not bound (internal wiring defect, fail closed D14) "+
					"— run `docsync check --offline` for the deterministic path")
		}
		ports, err := ModelPortsBuilderImpl(ctx, ModelPortsOptions{
			RepoRoot:  repoRoot,
			Repo:      repo,
			Clock:     clock,
			Git:       git,
			Compiled:  compiled,
			Models:    modelsConfig,
			APIKey:    credentials.Key,
			Env:       options.Env,
			Transport: options.Transport,
			OnPatch:   options.OnPatch,
		})
		if err != nil {
			return nil, err
		}
		var prCtx *orchestrator.PRContext
		if input.PR != nil {
			prCtx = &orchestrator.PRContext{Number: *input.PR}
		}
		return orchestrator.RunPipeline(ctx, orchestrator.PipelineInput{
			Repo:      repo,
			RepoRoot:  repoRoot,
			Base:      input.Base,
			Head:      input.Head,
			Manifest:  *manifest,
			Policy:    compiled,
			Analyzers: CLI_ANALYZER_LINEUP(),
			Clock:     clock,
			PR:        prCtx,
			Budgets:   input.Budgets,
		}, orchestrator.PipelinePorts{
			Decision:     ports.Decision,
			Writer:       ports.Writer,
			Verification: ports.Verification,
			Policy:       ports.Policy,
		})
	}

	return &cliRuntime{runFn: runFn, advisories: advisories}, nil
}

/* ------------------------------------------------------- offline run outcome */

// wallClock is the CLI's own wall clock (ISO-8601), used by the verb runners.
func wallClock() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// offlineAbstainId is the deterministic offline abstain id (`ab-<sha256[:16]>`,
// mirrors the core recipe).
func offlineAbstainId(runID string, docPath *string, reason string) string {
	docPart := "(run)"
	if docPath != nil {
		docPart = *docPath
	}
	sum := sha256.Sum256([]byte(runID + "|" + docPart + "|" + reason))
	return "ab-" + hex.EncodeToString(sum[:])[:16]
}

// offlineOutcome builds the honest offline RunOutcome (OD-R16): one
// review/abstain row per affected mapped doc; unknown_coverage (D7) only when
// the analysis was conclusive; status `degraded`; a `jev-unavailable`
// degradation. It is schema-validated at the end (fail closed — an invalid
// outcome is never returned).
// offlineOutcomeInput is the offlineOutcome input bundle.
type offlineOutcomeInput struct {
	Repo          string
	BaseSha       string
	HeadSha       string
	Manifest      schemas.Manifest
	PolicyVersion int
	Clock         func() string
	Graph         graph.DocumentGraph
	Analysis      analysis.AnalysisOutcome
}

func offlineOutcome(input offlineOutcomeInput) (*schemas.RunOutcome, error) {
	runID := input.Repo + "#" + input.BaseSha + ".." + input.HeadSha
	var analysisRecord *schemas.ChangeAnalysis
	if input.Analysis.Kind == analysis.AnalysisOutcomeKindAnalysis {
		analysisRecord = input.Analysis.Record
	}

	var changedPaths []string
	if analysisRecord != nil {
		seen := map[string]struct{}{}
		for _, file := range analysisRecord.Files {
			if _, dup := seen[file.Path]; dup {
				continue
			}
			seen[file.Path] = struct{}{}
			changedPaths = append(changedPaths, file.Path)
		}
	}

	reverse, err := graph.BuildReverseIndex(input.Graph, changedPaths)
	if err != nil {
		return nil, err
	}
	affectedDocs := make([]string, 0, len(reverse.AffectedDocs))
	for docPath := range reverse.AffectedDocs {
		affectedDocs = append(affectedDocs, docPath)
	}
	sort.Slice(affectedDocs, func(i, j int) bool { return affectedDocs[i] < affectedDocs[j] })

	var unknownCoverage []string
	if analysisRecord != nil {
		unknownCoverage, err = orchestrator.ComputeUnknownCoverage(analysisRecord, &input.Graph)
		if err != nil {
			return nil, err
		}
	}

	documents := make([]schemas.RunDocument, 0, len(affectedDocs))
	abstains := make([]schemas.RunAbstain, 0, len(affectedDocs)+1)
	for _, docPath := range affectedDocs {
		dp := docPath
		documents = append(documents, schemas.RunDocument{
			DocPath:         docPath,
			PolicyOutcome:   schemas.PipelineOutcomeReview,
			PartialCoverage: false,
			Abstain: &schemas.RunDocumentAbstain{
				Reason: string(schemas.AbstainReasonModelUnavailable),
				Detail: OFFLINE_ABSTAIN_DETAIL,
			},
		})
		abstains = append(abstains, schemas.RunAbstain{
			AbstainID: offlineAbstainId(runID, &dp, string(schemas.AbstainReasonModelUnavailable)),
			DocPath:   &dp,
			Reason:    string(schemas.AbstainReasonModelUnavailable),
			Detail:    OFFLINE_ABSTAIN_DETAIL,
		})
	}
	if analysisRecord == nil {
		abstains = append(abstains, schemas.RunAbstain{
			AbstainID: offlineAbstainId(runID, nil, string(schemas.AbstainReasonOther)),
			DocPath:   nil,
			Reason:    string(schemas.AbstainReasonOther),
			Detail:    "analysis inconclusive (offline) — no deterministic coverage of the change window",
		})
	}

	stages := make([]schemas.PipelineStage, 0, len(orchestrator.PipelineStages))
	for _, stage := range orchestrator.PipelineStages {
		stageName := string(stage)
		if stage == orchestrator.StageAnalyze {
			status := schemas.RunStageOk
			if analysisRecord == nil {
				status = schemas.RunStageInconclusive
			}
			stages = append(stages, schemas.PipelineStage{Stage: stageName, Status: status})
			continue
		}
		note := "skipped: offline mode (OD-R16) — deterministic-only run"
		stages = append(stages, schemas.PipelineStage{
			Stage:  stageName,
			Status: schemas.RunStageSkipped,
			Note:   &note,
		})
	}

	startedAt := input.Clock()
	finishedAt := input.Clock()
	degradations := []schemas.Degradation{
		{
			Stage:  "jev",
			Kind:   schemas.DegradationJevUnavailable,
			Detail: OFFLINE_ABSTAIN_DETAIL + " — no model stages run in offline mode (OD-R16)",
		},
	}
	degradations = append(degradations, degradationIfInconclusive(input.Analysis)...)
	outcome := &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID,
		RunID:  runID,
		Key: schemas.RunKey{
			Repo:            input.Repo,
			BaseSHA:         input.BaseSha,
			HeadSHA:         input.HeadSha,
			ManifestVersion: input.Manifest.Version,
			PolicyVersion:   input.PolicyVersion,
		},
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		Status:     schemas.RunStatusDegraded,
		Stages:     stages,
		Documents:  documents,
		Abstains:   abstains,
		Candidates: []schemas.RunCandidate{},
		Provenance: schemas.RunProvenance{
			PolicyVersion: input.PolicyVersion,
			ModelBindings: []schemas.RunModelBinding{},
		},
		Degradations: degradations,
		Findings:     []schemas.RunFinding{},
		EarlySignal:  schemas.EarlySignal{},
		StateChurn:   schemas.StateChurn{},
	}
	if len(unknownCoverage) > 0 {
		outcome.UnknownCoverage = unknownCoverage
	}

	if issues := outcome.Validate(); len(issues) > 0 {
		return nil, docutil.New(docutil.CodeSchema,
			"offline run outcome failed schema validation (fail closed, D14): "+renderIssues(issues))
	}
	return outcome, nil
}

// degradationIfInconclusive returns the analyzer-inconclusive degradation when
// the analysis was inconclusive (empty otherwise).
func degradationIfInconclusive(analysisOutcome analysis.AnalysisOutcome) []schemas.Degradation {
	if analysisOutcome.Kind != analysis.AnalysisOutcomeKindInconclusive {
		return nil
	}
	detail := analysisOutcome.Reason
	if detail == "" {
		detail = "inconclusive"
	}
	return []schemas.Degradation{{
		Stage:  "analyze",
		Kind:   schemas.DegradationAnalyzerInconclusive,
		Detail: detail,
	}}
}

// createOfflineRunFn builds the offline runtime's run closure (OD-R16): resolve
// window shas → real analyzer analysis → graph → honest abstain outcome. No
// model port is ever built.
func createOfflineRunFn(options CliRuntimeOptions, manifest schemas.Manifest, policyVersion int) func(context.Context, CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	return func(ctx context.Context, input CliRuntimeRunInput) (*schemas.RunOutcome, error) {
		git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: options.RepoRoot})
		if err != nil {
			return nil, err
		}
		baseSha, err := git.ResolveRev(ctx, input.Base)
		if err != nil {
			return nil, docutil.New(docutil.CodeGit,
				"offline run: cannot resolve base/head refs ('"+input.Base+"', '"+input.Head+"'): "+describeErr(err))
		}
		headSha, err := git.ResolveRev(ctx, input.Head)
		if err != nil {
			return nil, docutil.New(docutil.CodeGit,
				"offline run: cannot resolve base/head refs ('"+input.Base+"', '"+input.Head+"'): "+describeErr(err))
		}
		analysisOutcome, err := analysis.BuildChangeAnalysis(ctx, analysis.BuildChangeAnalysisInput{
			Repo:     options.Repo,
			RepoRoot: options.RepoRoot,
			Git:      git,
			Base:     baseSha,
			Head:     headSha,
			Plugins:  CLI_ANALYZER_LINEUP(),
		})
		if err != nil {
			return nil, err
		}
		documentGraph := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: manifest})
		if len(documentGraph.Errors) > 0 {
			// A concepts.yml-level graph defect is configuration (D8): fail closed.
			diagnostics := make([]string, 0, len(documentGraph.Errors))
			for _, e := range documentGraph.Errors {
				diagnostics = append(diagnostics, string(e.Kind)+" at "+e.Path+": "+e.Message)
			}
			return nil, newCliConfigError(
				"docsync: document graph configuration errors (fail closed, D8)",
				diagnostics...)
		}
		return offlineOutcome(offlineOutcomeInput{
			Repo:          options.Repo,
			BaseSha:       baseSha,
			HeadSha:       headSha,
			Manifest:      manifest,
			PolicyVersion: policyVersion,
			Clock:         options.Clock,
			Graph:         documentGraph,
			Analysis:      analysisOutcome,
		})
	}
}

// resolveOfflineExitCode is the OFFLINE exit semantics (binding, see module
// doc): 0 only for an EMPTY change window; 1 for any non-empty window (abstained
// mapped docs, unknown coverage, or an inconclusive analysis — never a
// fabricated clean exit, D14).
func resolveOfflineExitCode(outcome *schemas.RunOutcome) DocsyncExitCode {
	analysisInconclusive := false
	for _, d := range outcome.Degradations {
		if d.Kind == schemas.DegradationAnalyzerInconclusive {
			analysisInconclusive = true
			break
		}
	}
	windowEmpty := len(outcome.Documents) == 0 &&
		len(outcome.UnknownCoverage) == 0 &&
		!analysisInconclusive
	if windowEmpty {
		return ExitOK
	}
	return ExitDrift
}

/* ------------------------------------------------------- shared utilities */

// stripModelEnv strips the model-layer env down to `OPENROUTER_*` variables
// (INV-8, ARCH §13): GitHub tokens and every other process variable never reach
// the composed model runtime.
func stripModelEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range env {
		if strings.HasPrefix(key, "OPENROUTER_") {
			out[key] = value
		}
	}
	return out
}

// isOutageOutcome reports whether the outcome carries an outage/degradation
// signature (D14): an outage-kind degradation OR a run-level `model_unavailable`
// abstain.
func isOutageOutcome(outcome *schemas.RunOutcome) bool {
	for _, d := range outcome.Degradations {
		if _, ok := outageDegradationKinds[d.Kind]; ok {
			return true
		}
	}
	for _, a := range outcome.Abstains {
		if a.Reason == string(schemas.AbstainReasonModelUnavailable) {
			return true
		}
	}
	return false
}

// outageReason returns the first outage-signature degradation's detail, or ""
// when the outcome carries no outage signature.
func outageReason(outcome *schemas.RunOutcome) string {
	for _, d := range outcome.Degradations {
		if _, ok := outageDegradationKinds[d.Kind]; ok {
			return d.Detail
		}
	}
	return ""
}

// advisoryMessages projects the report-only floor advisories to their messages.
func advisoryMessages(advisories []validate.FloorCheckAdvisory) []string {
	if len(advisories) == 0 {
		return nil
	}
	messages := make([]string, 0, len(advisories))
	for _, a := range advisories {
		messages = append(messages, a.Message)
	}
	return messages
}

// extractCompiledPolicyVersion reads the compiled policy's policy_version
// structurally (P5-T08 seam parity): compiled.Policy.policy_version.
func extractCompiledPolicyVersion(compiled *policy.CompiledPolicyWorkflow) int {
	if compiled == nil {
		return 0
	}
	raw, err := json.Marshal(compiled)
	if err != nil {
		return 0
	}
	var decoded struct {
		Policy struct {
			PolicyVersion *int `json:"policy_version"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return 0
	}
	if decoded.Policy.PolicyVersion == nil {
		return 0
	}
	return *decoded.Policy.PolicyVersion
}

// describeErr renders an error's message text (TS detailOf).
func describeErr(err error) string {
	if err == nil {
		return "undefined"
	}
	if strings.TrimSpace(err.Error()) != "" {
		return err.Error()
	}
	return "Error"
}

// osEnv snapshots the process environment into a map (shape of process.env).
func osEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			env[kv[:eq]] = kv[eq+1:]
		}
	}
	return env
}

// renderIssues renders schema Issues to a single string (diagnostics).
func renderIssues(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, strings.Join(issue.Path, ".")+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}
