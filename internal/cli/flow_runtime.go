package cli

// The production flow runtimes for the `pr` and `audit` verbs — the Go form of
// the TS CLI composition layer wiring `internal/gitpr` (the doc-PR write path)
// and `internal/audit` (the Loop-B engine) onto the verb seams.
//
// Both bindings compose the SAME fail-closed ladder as the offline verbs: all
// config validation happens BEFORE any AI stage (D8), a platform/credential
// problem surfaces as a typed error, and a missing capability degrades to less
// automation (D14). Nothing here guesses.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

func init() {
	AuditRunnerImpl = runAuditFlow
	PrFlowRunnerImpl = runPrFlow
}

/* ------------------------------------------------------------------ pr flow */

// runPrFlow runs the fix flow followed by the core doc-PR flow (the TS
// `docsync pr` wiring): every model stage is composed behind the pipeline
// runtime, the applied patches feed the doc-PR flow, and the platform is the
// GitHub REST port built from env credentials (INV-8: env-only, never stored).
func runPrFlow(ctx context.Context, input *PrFlowInput) (*PrPayload, error) {
	if input == nil {
		return nil, newCliConfigError("docsync pr: flow input is required")
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		repoRoot = "."
	}

	/* Config validation first (D8): manifest + layout + policy before any AI. */
	manifest, err := loadManifestForFlow(repoRoot)
	if err != nil {
		return nil, err
	}
	if issues := manifest.Validate(); len(issues) > 0 {
		return nil, newCliConfigError("docsync pr: manifest failed re-validation (fail closed, D8): " + issues[0].Message)
	}

	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repoRoot})
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	clock := func() string { return now }
	runID := newULID()

	stateDir := filepath.Join(repoRoot, schemas.DocSyncDir, "state")
	store := NewFixRunRecordStore(stateDir)
	incidents := newFileIncidentSink(stateDir)
	repo := prFlowRepoName(repoRoot)

	capture := newPatchCapture()
	runtime, err := createCliPipelineRuntime(CliRuntimeOptions{
		RepoRoot: repoRoot,
		Repo:     repo,
		Env:      osEnv(),
		Clock:    clock,
		OnPatch:  func(patch *schemas.DocPatch, _ string) { capture.add(patch) },
	})
	if err != nil {
		var cfg *CliConfigError
		if errors.As(err, &cfg) {
			return nil, newCliConfigError(cfg.Error())
		}
		return nil, err
	}

	/* The fix flow (the same ports as `docsync fix`). */
	fixResult := RunFixFlow(ctx, FixFlowDeps{
		Runtime:   runtime,
		Git:       git,
		Store:     store,
		Incidents: incidents,
		Clock:     clock,
		PatchSource: func(ctx context.Context, docPath string, patchRef *string) (*schemas.DocPatch, error) {
			return capture.resolve(docPath, patchRef)
		},
		Apply: applyDocPatchReal(git),
	}, FixFlowInput{
		RepoRoot: repoRoot,
		Repo:     repo,
		RunID:    runID,
	})
	if fixResult.Kind != FixResultCompleted {
		if fixResult.Kind == FixResultConfigError {
			return nil, newCliConfigError("docsync pr: " + fixResult.Detail)
		}
		return nil, docutilRuntimeError("docsync pr: fix flow ended " + fixFlowKindName(fixResult.Kind))
	}
	if len(fixResult.Applied) == 0 {
		return nil, docutilRuntimeError(
			"docsync pr: no documentation patches to open a PR for (nothing applied)")
	}

	/* The frozen run outcome the flow consumes as evidence. */
	report := fixResult.Report
	if report == nil {
		return nil, docutilRuntimeError("docsync pr: fix flow completed without a canonical report")
	}
	outcome := runOutcomeFromReport(*report)
	patches := make([]schemas.DocPatch, 0, len(capture.patches))
	patches = append(patches, capture.patches...)
	headSha, headErr := git.ResolveRev(ctx, "HEAD")
	if headErr != nil || headSha == "" {
		headSha = "0000000000000000000000000000000000000000"
	}
	detectorModel, writerModel := "decision-engine", "patch-writer"
	for _, binding := range report.ModelBindings {
		switch binding.Role {
		case "decision-engine":
			detectorModel = binding.Model
		case "patch-writer", "writer":
			writerModel = binding.Model
		}
	}
	provenance := &schemas.Provenance{
		Schema:          "docsync.provenance/v1",
		RunID:           report.RunID,
		SourceCommit:    headSha,
		SourceFiles:     sourceFilesOf(manifest, *report),
		Detector:        schemas.ProvenanceDetector{Model: detectorModel, Confidence: 1},
		Writer:          schemas.ProvenanceWriter{Model: writerModel},
		Verification:    schemas.ProvenanceVerification{Status: schemas.VerdictPass},
		GeneratedAt:     now,
		PolicyVersion:   report.PolicyVersion,
		ManifestVersion: strconv.Itoa(manifest.Version),
	}

	/* The core doc-PR flow over the landed gitpr engine. */
	docPrFlow, flowErr := createDocPrFlow(repoRoot, git, clock)
	if flowErr != nil {
		return nil, flowErr
	}
	var sourcePr *gitpr.FlowSourcePr
	if input.SourcePr != nil {
		sourcePr = &gitpr.FlowSourcePr{Number: *input.SourcePr, HeadSha: input.ChangePr}
	}
	flowResult, err := docPrFlow.Run(ctx, gitpr.DocPrFlowInput{
		RunID:            report.RunID,
		SourcePr:         sourcePr,
		Patches:          patches,
		Provenance:       &gitpr.FlowProvenance{RunID: report.RunID, PolicyVersion: report.PolicyVersion},
		ProvenanceRecord: provenance,
		Outcome:          outcome,
		Canonical:        *report,
		Manifest:         manifest,
		RunHeadSha:       &headSha,
	})
	if err != nil {
		return nil, err
	}

	payloadRunError := fixRunErrorOf(fixResult)
	prOpened := flowResult.PR != nil &&
		(flowResult.Kind == gitpr.PrFlowKindCreated || flowResult.Kind == gitpr.PrFlowKindUpdated)
	return &PrPayload{
		FixSummaryLines: fixSummaryLinesFor(fixResult),
		PrOpened:        prOpened,
		PrResult:        prResultVerdictOf(flowResult),
		RunError:        payloadRunError,
	}, nil
}

func prFlowRepoName(repoRoot string) string {
	if slug := rawGitTrimmed(repoRoot, "remote", "get-url", "origin"); slug != "" {
		if idx := strings.Index(slug, "github.com"); idx >= 0 {
			slug = strings.TrimSuffix(strings.TrimPrefix(slug[idx+len("github.com"):], "/"), ".git")
			slug = strings.TrimSpace(strings.TrimSuffix(slug, "/"))
		}
		if slug != "" {
			return slug
		}
	}
	return "local/repo"
}

// rawGitTrimmed runs a read-only git command, returning trimmed stdout (empty
// when it fails — the caller falls back).
func rawGitTrimmed(repoRoot string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func loadManifestForFlow(repoRoot string) (*schemas.Manifest, error) {
	source, err := os.ReadFile(filepath.Join(repoRoot, schemas.DocSyncDir, schemas.ConfigFiles[0]))
	if err != nil {
		return nil, newCliConfigError("docsync: manifest is not readable: " + describeErr(err))
	}
	return schemas.ParseManifest(source)
}

// createDocPrFlow builds the gitpr doc-PR flow with the platform port derived
// from env credentials. With no credentials the flow runs in dry-run posture
// (kind='none' credentials are a legal, fail-closed configuration).
func createDocPrFlow(repoRoot string, git *gitx.GitClient, clock func() string) (gitpr.DocPrFlow, error) {
	stateDir := filepath.Join(repoRoot, schemas.DocSyncDir, "state")
	stateStore, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	if err != nil {
		return nil, err
	}

	credentials := gitpr.PrCredentials{Kind: gitpr.PrCredentialsNone}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		credentials = gitpr.PrCredentials{Kind: gitpr.PrCredentialsDeveloper, Token: token}
	}
	platform := gitpr.CreateGitHubRestPlatform(gitpr.GitHubRestPlatformDeps{
		Credentials: credentials,
		Clock:       clock,
	})

	incidentSink, sinkErr := gitpr.FileIncidentSink(stateDir)
	if sinkErr != nil {
		return nil, sinkErr
	}
	flow, flowErr := gitpr.CreateDocPrFlow(gitpr.DocPrFlowDeps{
		Git:           git,
		Platform:      platform,
		Credentials:   credentials,
		Repo:          prFlowRepoName(repoRoot),
		DefaultBranch: defaultBranchOf(git),
		Store:         stateStore,
		Incidents:     gitpr.CreateIncidentLog(incidentSink),
		Clock:         clock,
	})
	if flowErr != nil {
		return nil, flowErr
	}
	return flow, nil
}

func defaultBranchOf(git *gitx.GitClient) string {
	if branch := rawGitTrimmed(git.Root(), "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); branch != "" {
		if idx := strings.Index(branch, "/"); idx >= 0 {
			branch = branch[idx+1:]
		}
		if branch != "" {
			return branch
		}
	}
	return "main"
}

func runOutcomeFromReport(report schemas.CanonicalReport) schemas.RunOutcome {
	outcome := schemas.RunOutcome{
		Schema:       "docsync.run-outcome/v1",
		RunID:        report.RunID,
		Key:          report.Key,
		Status:       report.Status,
		Documents:    []schemas.RunDocument{},
		Abstains:     report.Abstains,
		Candidates:   []schemas.RunCandidate{},
		Stages:       []schemas.PipelineStage{},
		Degradations: report.Degradations,
		Findings:     report.Findings,
	}
	if report.GeneratedAt != nil {
		started := *report.GeneratedAt
		outcome.StartedAt = &started
	}
	return outcome
}

func fixSummaryLinesFor(result FixFlowResult) []string {
	return RenderFixSummary(result, GlyphsFallback)
}

func fixRunErrorOf(result FixFlowResult) *string {
	if result.Kind == FixResultCompleted {
		return nil
	}
	detail := result.Detail
	if detail == "" {
		detail = "fix flow ended with kind " + fixFlowKindName(result.Kind)
	}
	return &detail
}

func fixFlowKindName(kind FixFlowResultKind) string {
	switch kind {
	case FixResultNoop:
		return "noop"
	case FixResultConfigError:
		return "config-error"
	case FixResultRuntimeErr:
		return "runtime-error"
	default:
		return "unknown"
	}
}

func prResultVerdictOf(result gitpr.PrFlowResult) *prResultVerdict {
	return &prResultVerdict{Verdict: string(result.Kind), Reason: result.Reason}
}

/* --------------------------------------------------------------- audit flow */

// runAuditFlow runs the Loop-B audit engine over the landed internal/audit
// compose (the TS loop-b-nightly wiring). Every config piece is validated
// BEFORE the engine runs (D8); a scheduler noop or an engine abstention is
// DATA, never a failure.
func runAuditFlow(ctx context.Context, input AuditRunnerInput) (AuditRunResult, error) {
	repoRoot := input.RepoRoot
	manifest, err := loadManifestForFlow(repoRoot)
	if err != nil {
		return AuditRunResult{}, err
	}

	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repoRoot})
	if err != nil {
		return AuditRunResult{}, err
	}

	stateDir := filepath.Join(repoRoot, schemas.DocSyncDir, "state")
	stateStore, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	if err != nil {
		return AuditRunResult{}, err
	}
	claimsStore := claims.NewClaimStore(stateStore)
	findingsStore, err := audit.CreateAuditFindingsStore(stateDir, nil)
	if err != nil {
		return AuditRunResult{}, err
	}

	defaultBranch := defaultBranchOf(git)
	branch := strings.TrimSpace(input.Branch)
	schedule := audit.LoopBScheduleConfig{
		Nightly:       nightlyWorkflowPresent(repoRoot),
		PostMerge:     input.PostMerge,
		DefaultBranch: defaultBranch,
	}
	triggerContext := audit.LoopBTriggerContext{
		Event:  auditTriggerEventOf(input.Trigger),
		Branch: &branch,
	}
	commitWalk := input.CommitWalk
	if commitWalk <= 0 {
		commitWalk = 50
	}
	concurrency := input.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}

	composed, err := audit.RunLoopBAudit(ctx, audit.LoopBAuditComposeDeps{
		Config:          schedule,
		Context:         triggerContext,
		Manifest:        *manifest,
		StateStore:      stateStore,
		Git:             git,
		Claims:          claimsStore,
		Store:           findingsStore,
		RunID:           input.RunID,
		Now:             input.Now,
		CommitWalkDepth: &commitWalk,
		Concurrency:     &concurrency,
	})
	if err != nil {
		return AuditRunResult{}, err
	}

	switch composed.Kind {
	case audit.LoopBAuditNoop:
		return AuditRunResult{Kind: AuditRunNoop, Reason: string(composed.Reason)}, nil
	case audit.LoopBAuditAbstained:
		return AuditRunResult{Kind: AuditRunAbstained, Reason: composed.Detail}, nil
	}

	result := composed.Result
	if result == nil {
		return AuditRunResult{}, docutilRuntimeError("docsync audit: engine returned no run record")
	}
	out := AuditRunResult{
		Kind:         AuditRunRan,
		Status:       string(result.Status),
		Detail:       auditRunDetail(result),
		FindingCount: len(composed.Findings),
		Trigger:      string(composed.Trigger),
		RunID:        input.RunID,
	}
	if composed.Appended != nil {
		out.AppendedCount = len(composed.Appended.Appended)
	}
	// The org-report advisories are advisory DATA surfaced by the verb's
	// caller through input.Advisories (the verb composes them); nothing posts.
	return out, nil
}

func auditTriggerEventOf(trigger string) audit.LoopBTriggerEvent {
	switch strings.TrimSpace(trigger) {
	case "nightly-scheduled", "schedule", "nightly":
		return audit.LoopBEventNightly
	case "post-merge":
		return audit.LoopBEventPostMerge
	case "pull_request", "pull-request":
		return audit.LoopBEventPR
	default:
		return ""
	}
}

// auditRunDetail reads the engine run's optional detail (a *string field).
func auditRunDetail(result *audit.AuditRunResult) string {
	if result == nil || result.Detail == nil {
		return ""
	}
	return *result.Detail
}

func nightlyWorkflowPresent(repoRoot string) bool {
	_, err := os.Stat(filepath.Join(repoRoot, ".github", "workflows", "docsync-loop-b-nightly.yml"))
	return err == nil
}

func docutilRuntimeError(msg string) error {
	return docutil.New(docutil.CodeInternal, msg)
}

// sourceFilesOf derives the provenance source-file set: the manifest sources of
// every applied document (the TS pipeline threads the analysis sources; the
// fix flow's report rows name the documents — their manifest sources are the
// same evidence set the analysis consumed).
func sourceFilesOf(manifest *schemas.Manifest, report schemas.CanonicalReport) []string {
	applied := map[string]bool{}
	for _, row := range report.Rows {
		applied[row.DocPath] = true
	}
	files := []string{}
	for _, entry := range manifest.Documents {
		if !applied[entry.Path] {
			continue
		}
		files = append(files, entry.Sources...)
	}
	if len(files) == 0 {
		for _, entry := range manifest.Documents {
			files = append(files, entry.Sources...)
		}
	}
	return files
}
