package cli

// The production model-port binding: composes the OpenRouter role adapters
// (decision-engine, patch-writer, judge) with the policy decision stage, the
// openrouter writer stage, the verifier verification stage, and the policy
// outcome stage into the orchestrator's PipelinePorts.
//
// This is the Go form of the TS CLI composition layer (packages/cli/src
// command wiring @docsync/openrouter adapters onto the core stage-graph
// ports): production orchestration stays deterministic code — the adapters
// are bounded leaf stages with schema-validated I/O (ARCH §13).
//
// Fail-closed discipline (D8/D14): every constructor defect (unbound role,
// judge-writer session collision) surfaces as an error BEFORE the pipeline
// runs; nothing here guesses.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

func init() {
	ModelPortsBuilderImpl = BuildModelPorts
}

// BuildModelPorts builds the model-backed PipelinePorts for an online run.
// The declared cli seam (ModelPortsBuilderImpl) binds here at init.
func BuildModelPorts(ctx context.Context, opts ModelPortsOptions) (ModelPorts, error) {
	_ = ctx
	if opts.Compiled == nil {
		return ModelPorts{}, errors.New("docsync: compiled policy workflow is required for the model ports")
	}
	if opts.Models == nil {
		return ModelPorts{}, errors.New("docsync: models configuration is required for the model ports")
	}
	if opts.APIKey == "" {
		return ModelPorts{}, errors.New("docsync: OPENROUTER_API_KEY is required for the model ports (env-only, ARCH §10.7)")
	}

	transport := opts.Transport
	if transport == nil {
		transport = openrouter.CreateFetchTransport(nil)
	}
	httpTransport, ok := transport.(openrouter.OpenRouterTransport)
	if !ok {
		return ModelPorts{}, fmt.Errorf("docsync: model transport %T does not satisfy openrouter.OpenRouterTransport", transport)
	}

	client, err := openrouter.NewOpenRouterClient(openrouter.OpenRouterClientOptions{
		Transport: httpTransport,
		APIKey:    opts.APIKey,
		Models:    opts.Models,
	})
	if err != nil {
		return ModelPorts{}, err
	}

	/* -- Stage 1: Jev (decision-engine role) -------------------------------- */
	decisionRole, err := openrouter.CreateDecisionEngineAdapter(openrouter.DecisionEngineAdapterOptions{
		Client:   client,
		Compiled: compiledToOpenRouter(opts.Compiled),
	})
	if err != nil {
		return ModelPorts{}, err
	}
	decisionStage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
		Port:     decisionRolePort{role: decisionRole},
		Workflow: *opts.Compiled,
	})
	bridge := policy.NewBridgeCoreDecisionStage(decisionStage)

	/* -- Stage 2: Sonnet (patch-writer role, V1 stub) ----------------------- */
	writerAdapter, err := openrouter.CreatePatchWriterAdapter(openrouter.PatchWriterAdapterOptions{
		Client: patchWriterClientPort{client: client},
	})
	if err != nil {
		return ModelPorts{}, err
	}
	writerStage, err := openrouter.CreateWriterStagePort(openrouter.WriterStageDeps{
		Writer: writerAdapter,
		Git:    writerGitPort{git: opts.Git},
	})
	if err != nil {
		return ModelPorts{}, err
	}

	/* -- Stage 3: the independent judge (D13) ------------------------------- */
	judgeRole, err := openrouter.CreateJudgeAdapter(openrouter.JudgeAdapterOptions{
		Client: client,
	})
	if err != nil {
		return ModelPorts{}, err
	}
	verificationStage, err := verifier.CreateVerificationStagePort(verifier.VerificationStageDeps{
		Judge:          judgeRole,
		WriterIdentity: verifier.SessionIdentity{Role: "patch-writer", SessionID: "docsync-pipeline-writer"},
		JudgeIdentity:  verifier.SessionIdentity{Role: "judge", SessionID: "docsync-pipeline-judge"},
	})
	if err != nil {
		return ModelPorts{}, err
	}

	/* -- Stage 4: the policy outcome engine (D11) --------------------------- */
	policyStage := policy.CreatePolicyStagePort(policy.PolicyStageDeps{
		Workflow:   *opts.Compiled,
		MatchGlobs: matchGlobs,
	})

	return ModelPorts{
		Decision:     orchestratorDecisionPort{bridge: bridge},
		Writer:       orchestratorWriterPort{port: writerStage},
		Verification: orchestratorVerificationPort{port: verificationStage},
		Policy:       orchestratorPolicyPort{port: policyStage},
	}, nil
}

// matchGlobs is the policy risk_class_overrides glob seam (the globs engine).
func matchGlobs(path, glob string) bool {
	compiled, err := globs.CompileGlob(glob)
	if err != nil {
		return false
	}
	return compiled.Match(path)
}

/* ------------------------------------------------- decision-stage adapter */

type orchestratorDecisionPort struct {
	bridge policy.BridgeCoreDecisionStage
}

func (p orchestratorDecisionPort) Decide(input orchestrator.DecisionStageInput) (orchestrator.DecisionStageResult, error) {
	result, err := p.bridge.Decide(policy.BridgeCoreDecisionStageInput{Analysis: input.Analysis})
	if err != nil {
		return orchestrator.DecisionStageResult{}, err
	}
	return orchestrator.DecisionStageResult{
		Decision:      result.Decision,
		Abstained:     result.Abstained,
		AbstainReason: result.AbstainReason,
	}, nil
}

/* ------------------------------------------------ decision-engine role bridge */

// decisionRolePort adapts the openrouter decision-engine role onto the
// policy DecisionEnginePort shape (policy never imports openrouter).
type decisionRolePort struct {
	role openrouter.DecisionEngineRole
}

func (p decisionRolePort) Decide(input policy.JevPortInput) (policy.JevPortResult, error) {
	result, err := p.role.Decide(openrouter.DecisionEngineInput{
		AnalysisContext: openrouter.DecisionAnalysisContext{
			ChangeKind:          input.AnalysisContext.ChangeKind,
			DocumentationImpact: input.AnalysisContext.DocumentationImpact,
			Risk:                input.AnalysisContext.Risk,
			Confidence:          input.AnalysisContext.Confidence,
			Answers:             jevAnswersToOpenRouter(input.AnalysisContext.Answers),
			Candidates:          input.AnalysisContext.Candidates,
			Evidence:            input.AnalysisContext.Evidence,
		},
		Change: openrouter.DecisionChangeRefs{
			Commit:            input.Change.Commit,
			DiffRef:           input.Change.DiffRef,
			ChangeAnalysisRef: input.Change.ChangeAnalysisRef,
		},
		UntrustedContent:    input.UntrustedContent,
		TemperatureOverride: input.TemperatureOverride,
		CachePrefix:         input.CachePrefix,
	})
	if err != nil {
		return policy.JevPortResult{}, err
	}
	return policy.JevPortResult{
		Kind:      policy.JevPortResultKind(result.Kind),
		Decision:  result.Decision,
		Reason:    abstainReasonValue(result.Reason),
		Telemetry: jevTelemetryFrom(result.Telemetry),
	}, nil
}

func jevAnswersToOpenRouter(answers policy.JevAnswers) map[string]openrouter.DecisionAnswer {
	out := make(map[string]openrouter.DecisionAnswer, len(answers))
	for key, answer := range answers {
		converted := openrouter.DecisionAnswer{Probability: answer.Probability}
		if len(answer.Value) > 0 {
			converted.Value = json.RawMessage(append([]byte(nil), answer.Value...))
		}
		out[key] = converted
	}
	return out
}

func abstainReasonValue(reason *schemas.AbstainReason) schemas.AbstainReason {
	if reason == nil {
		return ""
	}
	return *reason
}

func jevTelemetryFrom(telemetry openrouter.RoleTelemetry) policy.JevTelemetry {
	out := policy.JevTelemetry{
		Role:           telemetry.Role,
		RequestedModel: telemetry.RequestedModel,
		RoutedModel:    telemetry.RoutedModel,
		LatencyMS:      telemetry.LatencyMS,
		Attempt:        telemetry.Attempt,
		FallbackUsed:   telemetry.FallbackUsed,
	}
	if telemetry.Provider != nil {
		out.Provider = telemetry.Provider
	}
	if telemetry.Usage != nil {
		out.Usage = &policy.JevUsage{
			PromptTokens:     telemetry.Usage.PromptTokens,
			CompletionTokens: telemetry.Usage.CompletionTokens,
			TotalTokens:      telemetry.Usage.TotalTokens,
			Cost:             telemetry.Usage.Cost,
		}
	}
	return out
}

/* ------------------------------------------------- patch-writer client bridge */

// patchWriterClientPort adapts the landed *OpenRouterClient onto the
// patch-writer adapter's client seam (pointer→value binding projection and
// the writer-side request/response shapes).
type patchWriterClientPort struct {
	client *openrouter.OpenRouterClient
}

func (p patchWriterClientPort) BindingFor(role string) (openrouter.PatchWriterRoleBinding, error) {
	binding, err := p.client.BindingFor(role)
	if err != nil {
		return openrouter.PatchWriterRoleBinding{}, err
	}
	if binding == nil {
		return openrouter.PatchWriterRoleBinding{}, nil
	}
	return openrouter.PatchWriterRoleBinding{StructuredOutput: binding.Params.StructuredOutput}, nil
}

func (p patchWriterClientPort) Complete(role string, request openrouter.PatchWriterCompletionRequest) (openrouter.PatchWriterRoleCompletion, error) {
	var temperature *float64
	if request.TemperatureOverride != 0 {
		value := request.TemperatureOverride
		temperature = &value
	}
	cache := request.CachePrefix
	policyVersion := request.PolicyVersion
	completion, err := p.client.Complete(role, openrouter.CompletionRequest{
		System:              request.System,
		User:                request.User,
		UntrustedContent:    request.UntrustedContent,
		JSONSchema:          request.JSONSchema,
		SchemaName:          request.SchemaName,
		TemperatureOverride: temperature,
		CachePrefix:         &cache,
		PolicyVersion:       &policyVersion,
	})
	if err != nil {
		return openrouter.PatchWriterRoleCompletion{}, err
	}
	out := openrouter.PatchWriterRoleCompletion{
		Kind:       completion.Kind,
		Text:       completion.Text,
		Structured: completion.Structured,
		Reason:     completion.Reason,
	}
	for _, violation := range completion.Attempts {
		out.Attempts = append(out.Attempts, openrouter.PatchWriterSchemaViolation{
			Attempt: violation.Attempt,
			Issues:  violation.Issues,
			RawText: violation.RawText,
		})
	}
	out.Telemetry = patchWriterTelemetryFrom(completion.Telemetry)
	return out, nil
}

func patchWriterTelemetryFrom(telemetry openrouter.RoleTelemetry) openrouter.PatchWriterRoleTelemetry {
	out := openrouter.PatchWriterRoleTelemetry{
		Role:           telemetry.Role,
		RequestedModel: telemetry.RequestedModel,
		RoutedModel:    telemetry.RoutedModel,
		LatencyMS:      telemetry.LatencyMS,
		Attempt:        telemetry.Attempt,
		FallbackUsed:   telemetry.FallbackUsed,
	}
	if telemetry.Provider != nil {
		out.Provider = *telemetry.Provider
	}
	if telemetry.Usage != nil {
		total := 0
		if telemetry.Usage.TotalTokens != nil {
			total = *telemetry.Usage.TotalTokens
		}
		cost := 0.0
		if telemetry.Usage.Cost != nil {
			cost = *telemetry.Usage.Cost
		}
		out.Usage = &openrouter.PatchWriterUsage{
			PromptTokens:     telemetry.Usage.PromptTokens,
			CompletionTokens: telemetry.Usage.CompletionTokens,
			TotalTokens:      total,
			Cost:             cost,
		}
	}
	return out
}

/* ---------------------------------------------------- writer-stage adapter */

type orchestratorWriterPort struct {
	port openrouter.WriterStagePort
}

func (p orchestratorWriterPort) Write(input orchestrator.WriterStageInput) (orchestrator.WriterStageResult, error) {
	result, err := p.port.Write(openrouter.WriterStageInput{
		Ctx: openrouter.WriterStageRunContext{
			Repo:     input.Ctx.Repo,
			RepoRoot: input.Ctx.RepoRoot,
			Base:     input.Ctx.Base,
			Head:     input.Ctx.Head,
			RunID:    input.Ctx.RunID,
			Manifest: input.Ctx.Manifest,
			Graph:    input.Ctx.Graph,
			Clock:    input.Ctx.Clock,
		},
		Doc: openrouter.WriterStageDocTarget{
			DocPath: input.Doc.DocPath,
			Mode:    input.Doc.Mode,
		},
		Analysis:                  input.Analysis,
		Decision:                  input.Decision,
		Relevance:                 toWriterRelevance(input.Relevance),
		ExpectedBaseContentHashes: input.ExpectedBaseContentHashes,
	})
	if err != nil {
		return orchestrator.WriterStageResult{}, err
	}
	out := orchestrator.WriterStageResult{
		Kind:   orchestrator.WriterStageResultKind(result.Kind),
		Patch:  result.Patch,
		Reason: result.Reason,
		Detail: result.Detail,
	}
	if result.Finding != nil {
		out.Finding = &orchestrator.WriterStageFinding{
			DocPath: result.Finding.DocPath,
			Reason:  result.Finding.Reason,
			Detail:  result.Finding.Detail,
		}
	}
	return out, nil
}

func toWriterRelevance(ctx orchestrator.RelevanceContext) openrouter.WriterStageRelevanceContext {
	docs := make([]openrouter.WriterStageRelevanceDoc, 0, len(ctx.Docs))
	for _, doc := range ctx.Docs {
		vias := make([]openrouter.WriterStageRelevanceVia, 0, len(doc.Via))
		for _, via := range doc.Via {
			vias = append(vias, openrouter.WriterStageRelevanceVia{
				Kind:   string(via.Kind),
				Detail: via.Detail,
			})
		}
		docs = append(docs, openrouter.WriterStageRelevanceDoc{
			DocPath:   doc.DocPath,
			Mode:      doc.Mode,
			Via:       vias,
			Contracts: doc.Contracts,
		})
	}
	return openrouter.WriterStageRelevanceContext{
		Docs:     docs,
		Concepts: ctx.Concepts,
		Caps:     openrouter.WriterStageRelevanceCaps{MaxDocs: ctx.Caps.MaxDocs},
	}
}

// writerGitPort adapts the read-only gitx client onto the writer-stage port
// (the writer stage's git seam carries no context — the client thread is
// process-scoped).
type writerGitPort struct {
	git *gitx.GitClient
}

func (p writerGitPort) CatFile(rev, path string) (gitx.CatFileResult, error) {
	if p.git == nil {
		return gitx.CatFileResult{}, fmt.Errorf("docsync: writer stage requires a git client")
	}
	return p.git.CatFile(context.Background(), rev, path)
}

func (p writerGitPort) DiffFiles(base, head string) ([]gitx.DiffFileRecord, error) {
	if p.git == nil {
		return nil, fmt.Errorf("docsync: writer stage requires a git client")
	}
	return p.git.DiffFiles(context.Background(), base, head)
}

/* --------------------------------------------- verification-stage adapter */

type orchestratorVerificationPort struct {
	port verifier.VerificationStagePort
}

func (p orchestratorVerificationPort) Verify(input orchestrator.VerificationStageInput) (orchestrator.VerificationStageResult, error) {
	patch := input.Patch
	result, err := p.port.Verify(verifier.VerificationStageInput{
		Ctx: verifier.RunContext{
			Repo:     input.Ctx.Repo,
			RepoRoot: input.Ctx.RepoRoot,
			Base:     input.Ctx.Base,
			Head:     input.Ctx.Head,
			RunID:    input.Ctx.RunID,
			Key:      input.Ctx.Key,
			Manifest: input.Ctx.Manifest,
			Graph:    input.Ctx.Graph,
			Clock:    input.Ctx.Clock,
		},
		Patch:      &patch,
		DocContent: input.DocContent,
	})
	if err != nil {
		return orchestrator.VerificationStageResult{}, err
	}
	return orchestrator.VerificationStageResult{Report: result.Report}, nil
}

/* ------------------------------------------------ policy-stage adapter */

type orchestratorPolicyPort struct {
	port policy.PolicyStagePort
}

func (p orchestratorPolicyPort) Resolve(input orchestrator.PolicyStageInput) (orchestrator.PolicyStageResult, error) {
	result, err := p.port.Resolve(policy.PolicyStageResolveInput{
		Ctx:          policy.PolicyStageRunContext{Manifest: input.Ctx.Manifest},
		DocPath:      input.DocPath,
		Decision:     input.Decision,
		Verification: input.Verification,
	})
	if err != nil {
		return orchestrator.PolicyStageResult{}, err
	}
	out := orchestrator.PolicyStageResult{
		Outcome:        result.Outcome,
		ReviewRequired: result.ReviewRequired,
	}
	if result.Abstain != nil {
		out.Abstain = &orchestrator.AbstainMarker{
			Reason: result.Abstain.Reason,
			Detail: result.Abstain.Detail,
		}
	}
	if result.QueueEventID != nil {
		out.QueueEventID = result.QueueEventID
	}
	return out, nil
}

/* ------------------------------------------------------------------ helpers */

// compiledToOpenRouter mirrors the compiled workflow into the openrouter
// adapter's structural input (the TS CLI passed the compiled policy
// verbatim; the Go adapters consume the same calibrated data).
func compiledToOpenRouter(compiled *policy.CompiledPolicyWorkflow) openrouter.CompiledPolicyWorkflow {
	questions := make([]openrouter.CompiledQuestion, 0, len(compiled.Questions))
	for _, question := range compiled.Questions {
		converted := openrouter.CompiledQuestion{
			Key:     question.Key,
			Type:    string(question.Type),
			Prompt:  question.Prompt,
			Choices: question.Choices,
		}
		if question.Scoring != nil {
			converted.Scoring = &openrouter.CompiledQuestionRange{
				Min: question.Scoring.Min,
				Max: question.Scoring.Max,
			}
		}
		questions = append(questions, converted)
	}
	return openrouter.CompiledPolicyWorkflow{
		Policy: openrouter.CompiledPolicy{
			PolicyVersion: compiled.Policy.PolicyVersion,
		},
		Questions:             questions,
		PolicyHash:            compiled.PolicyHash,
		DecisionSchemaVersion: compiled.DecisionSchemaVersion,
	}
}

var _ = workspace.WorkspaceMode("") // the writer doc mode is the workspace union
