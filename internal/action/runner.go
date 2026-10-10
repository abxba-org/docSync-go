// Ported from packages/action/src/runner.ts (P8-T16): wraps the docSync check
// through an INJECTED CheckRunPort port and maps the result to the CLI
// exit-code table plus a GitHub check-run conclusion (ARCH §12.2). PURE
// orchestration — the port is the composition seam; the default wiring
// (port.go) binds it to the composed pipeline runtime + check-run REST surface.
// No I/O, no AI here.
//
// Exit-code mapping (internal/orchestrator/exit-codes.go — P7-T06/T07):
// 0 → success (no drift) · 1 → drift · 2 → failure configuration error (D8) ·
// 3 → neutral inconclusive (D14). Neutral-default posture (D10): without an
// enforcement opt-in, advisory findings conclude neutral — builds never break
// in V1.
//
// Fork report-only mode (A7, docs/spikes/A7-github-token-forks.md; P8-T16): a
// fork PR forces report-only. Report-only results assert what IS real about
// the result — no patch-generation indicator (writerInvoked), no PR creation,
// and — when the report carries any abstain — that the report-only downgrade
// marker (ReportOnlyAbstainDetail) is present. A violating port fails closed to
// exit 3 / neutral-inconclusive. On forks a drift-only failure is downgraded to
// neutral (advisory on forks — the check never fails CI from drift alone
// there), while configuration errors still conclude failure on any repository.

package action

import (
	"errors"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ReportOnlyAbstainDetail is the exact report-only abstain detail (binding
// wording, P8-T16): the default port's writer stub abstains with this detail
// and the runner's fork-guard asserts a fork report-only result's abstains
// carry it. Owned HERE (the runner owns the fork contract).
const ReportOnlyAbstainDetail = "report-only mode (fork PR): no patch generation"

// RunnerEnv is the environment consumed by the runner (action.yml inputs are
// mapped onto these keys by Main). An empty string field means "absent" (the
// TS optional `?` fields), matching the mapActionInputsToRunnerEnv copy rule
// (value.length > 0).
type RunnerEnv struct {
	// ActionEventEnv (superset) — the GITHUB_* fields parseActionEvent consumes.
	GithubEventName string
	GithubRef       string
	GithubHeadRef   string
	GithubBaseRef   string
	GithubSha       string

	// DocsyncStrict is the `--strict` enforcement flag (D10) — input `strict`.
	DocsyncStrict string
	// DocsyncEnforcementOptIn is the repository enforcement opt-in (D10) —
	// policy-derived, not an action input.
	DocsyncEnforcementOptIn string
	// DocsyncReportOnly is "true" forces report-only regardless of fork status
	// (input `report-only`).
	DocsyncReportOnly string
	// GithubRepository is the standard Actions repository identity (owner/name):
	// the repo fallback when the event payload omits repository.full_name.
	GithubRepository string

	// GithubToken is NEVER read by the runner logic and NEVER written to any
	// output; declared so tests can plant a token and assert no output ever
	// carries it.
	GithubToken string
}

// ActionLogger is the minimal logging seam (stderr / local runner transcript).
// A nil field (or a nil *ActionLogger) is a no-op — matches the TS optional
// info/warn members.
type ActionLogger struct {
	Info func(message string)
	Warn func(message string)
}

func (l *ActionLogger) info(message string) {
	if l != nil && l.Info != nil {
		l.Info(message)
	}
}

func (l *ActionLogger) warn(message string) {
	if l != nil && l.Warn != nil {
		l.Warn(message)
	}
}

// CheckRunPortInput is the input handed to the injected check port (the
// composition seam).
type CheckRunPortInput struct {
	// Repo is the base repository full name (owner/name).
	Repo string
	// Base is the base ref (PR base branch, or the push parent ref).
	Base string
	// Head is the head commit SHA to report on.
	Head string
	// PR is present on pull_request events only.
	PR *CheckRunPortPR
	// ForkReportOnly (A7): forced true for fork PRs — the port must not write
	// anything.
	ForkReportOnly bool
}

// CheckRunPortPR is the PR identity carried on the port input.
type CheckRunPortPR struct {
	Number     int
	HeadBranch string
	BaseBranch string
}

// CheckRunPortResultKind is the tag of the closed port-result union.
type CheckRunPortResultKind string

// The closed union arms (TS CheckRunPortResult kinds).
const (
	CheckRunPortResultReport              CheckRunPortResultKind = "report"
	CheckRunPortResultConfigError         CheckRunPortResultKind = "config-error"
	CheckRunPortResultRuntimeInconclusive CheckRunPortResultKind = "runtime-inconclusive"
)

// CheckRunPortResult is the result of the injected port — a flattened
// discriminated union selected by Kind. report results may carry
// composition-seam indicators (WriterInvoked/PRCreated); the runner asserts
// BOTH are false whenever ForkReportOnly was set (no patch generation, no PR
// creation).
type CheckRunPortResult struct {
	Kind CheckRunPortResultKind
	// Report is the canonical report (Kind == CheckRunPortResultReport).
	Report *schemas.CanonicalReport
	// Diagnostics is the config-error diagnostic list
	// (Kind == CheckRunPortResultConfigError).
	Diagnostics []orchestrator.ConfigErrorDiagnostic
	// Reason is the runtime-inconclusive reason
	// (Kind == CheckRunPortResultRuntimeInconclusive).
	Reason string
	// WriterInvoked is true iff the port invoked the patch writer (contract flag
	// for the runner assert).
	WriterInvoked bool
	// PRCreated is true iff the port created/updated a PR (contract flag for the
	// runner assert).
	PRCreated bool
}

// CheckRunPort is the injected seam: runs the check pipeline and reports. A
// returned error models the TS `throw` — the runner maps it to the runtime
// inconclusive lane (D14).
type CheckRunPort func(input CheckRunPortInput) (CheckRunPortResult, error)

// ActionRunResult is the runner outcome: the CLI exit code (semantics
// preserved for scripts) plus the check conclusion. Conclusion is nil only
// when a lane did not resolve one (never in practice for these lanes); Title
// and Text carry the lane payload when present.
type ActionRunResult struct {
	ExitCode     orchestrator.DocsyncExitCode
	Conclusion   *orchestrator.CheckRunConclusion
	Summary      string
	Title        *string
	Text         *string
	Inconclusive bool
}

// RunActionRunnerDeps are the injected dependencies of RunActionRunner.
type RunActionRunnerDeps struct {
	Check   CheckRunPort
	Env     *RunnerEnv
	Payload any
	Logger  *ActionLogger
}

func conclusionPtr(conclusion orchestrator.CheckRunConclusion) *orchestrator.CheckRunConclusion {
	return &conclusion
}

// exitInconclusive maps a D14 runtime lane onto the neutral/inconclusive result.
func exitInconclusive(reason string) ActionRunResult {
	payload := orchestrator.BuildInconclusiveSummary(reason)
	title := payload.Title
	return ActionRunResult{
		ExitCode:     orchestrator.ExitRuntime,
		Conclusion:   conclusionPtr(orchestrator.ConclusionNeutral),
		Summary:      payload.Summary,
		Title:        &title,
		Inconclusive: true,
	}
}

// exitConfigError maps a D8 configuration lane onto the failure result.
func exitConfigError(diagnostics []orchestrator.ConfigErrorDiagnostic) ActionRunResult {
	payload := orchestrator.BuildConfigErrorSummary(diagnostics)
	title := payload.Title
	text := payload.Text
	return ActionRunResult{
		ExitCode:   orchestrator.ExitConfig,
		Conclusion: conclusionPtr(orchestrator.ConclusionFailure),
		Summary:    payload.Summary,
		Title:      &title,
		Text:       &text,
	}
}

// RunActionRunner runs the docSync check through the injected port and maps the
// result to {exitCode, conclusion, summary}. Never throws for pipeline lanes —
// failures are surfaced fail-closed as exit 2/3 results.
func RunActionRunner(deps RunActionRunnerDeps) ActionRunResult {
	env := deps.Env
	if env == nil {
		env = &RunnerEnv{}
	}
	logger := deps.Logger

	// 1. Parse the event (fail-closed, D8): malformed payload/environment → the
	// config-error lane.
	context, err := ParseActionEvent(deps.Payload, env)
	if err != nil {
		var eventErr *ActionEventError
		if errors.As(err, &eventErr) {
			logger.warn("docsync action: " + eventErr.Message)
			return exitConfigError([]orchestrator.ConfigErrorDiagnostic{
				{File: "(action event)", Message: eventErr.Message},
			})
		}
		logger.warn("docsync action: unexpected event-parse failure: " + err.Error())
		return exitInconclusive("unexpected event-parse failure")
	}

	// 2. Resolve head/base/repo — indeterminate degrades to inconclusive (D14),
	// never "no drift".
	head := ""
	switch {
	case context.PR != nil:
		head = context.PR.HeadSha
	case context.Push != nil:
		head = context.Push.After
	default:
		head = env.GithubSha
	}
	base := ""
	switch {
	case context.PR != nil:
		base = context.PR.BaseBranch
	case len(env.GithubBaseRef) > 0:
		base = env.GithubBaseRef
	case context.Push != nil:
		base = context.Push.Branch + "~1"
	}
	if len(head) == 0 {
		logger.warn("docsync action: cannot determine the head commit for this event")
		return exitInconclusive(
			"cannot determine the head commit of this event (no pull_request head sha, no push after, no GITHUB_SHA)")
	}

	forkReportOnly := (context.PR != nil && context.PR.IsFork) || env.DocsyncReportOnly == "true"
	if forkReportOnly {
		logger.info(
			"docsync action: fork PR — report-only mode forced (A7): no patch generation, no PR creation")
	}

	repo := ""
	if context.Repo != nil {
		repo = *context.Repo
	} else if len(env.GithubRepository) > 0 {
		repo = env.GithubRepository
	}

	portInput := CheckRunPortInput{
		Repo:           repo,
		Base:           base,
		Head:           head,
		ForkReportOnly: forkReportOnly,
	}
	if context.PR != nil {
		portInput.PR = &CheckRunPortPR{
			Number:     context.PR.Number,
			HeadBranch: context.PR.HeadBranch,
			BaseBranch: context.PR.BaseBranch,
		}
	}

	// 3. Run the port (injected seam) — a throwing port is a runtime lane (D14).
	result, err := deps.Check(portInput)
	if err != nil {
		reason := "check pipeline failure: " + err.Error()
		logger.warn("docsync action: " + reason)
		return exitInconclusive(reason)
	}

	// 4. Map the lane.
	if result.Kind == CheckRunPortResultConfigError {
		logger.info("docsync action: configuration error — failing closed (D8)")
		return exitConfigError(result.Diagnostics)
	}
	if result.Kind == CheckRunPortResultRuntimeInconclusive {
		logger.warn("docsync action: runtime inconclusive — " + result.Reason)
		return exitInconclusive(result.Reason)
	}

	// 4a. Fork report-only contract assert (A7/P8-T16): the guard asserts what
	// IS real about the result — no patch generation, no PR creation, and the
	// report-only downgrade marker on any report that carries abstains.
	if forkReportOnly {
		reasons := make([]string, 0, 3)
		if result.WriterInvoked {
			reasons = append(reasons, "invoked the patch writer")
		}
		if result.PRCreated {
			reasons = append(reasons, "created a PR")
		}
		if hasAbstainsWithoutMarker(result.Report) {
			reasons = append(reasons, "carries abstains without the report-only downgrade marker")
		}
		if len(reasons) > 0 {
			violation := "check port contract violation: report-only fork run " +
				strings.Join(reasons, " and ") + " (A7)"
			logger.warn("docsync action: " + violation)
			return exitInconclusive(violation)
		}
	}

	// 4b. Report lane: exit code from the CLI table (semantics preserved for
	// scripts); conclusion from the core mapping (D8/D10/D14 precedence).
	strict := env.DocsyncStrict == "true"
	enforcementOptIn := env.DocsyncEnforcementOptIn == "true"
	exitCode := orchestrator.ResolveCheckExitCode(result.Report, &orchestrator.CheckExitOptions{Strict: strict})
	resolved := orchestrator.ResolveCheckConclusion(result.Report, &orchestrator.ResolveCheckConclusionOptions{
		Strict:           strict,
		EnforcementOptIn: enforcementOptIn,
	})

	// 4c. Advisory on forks (A7): a fork report-only run NEVER concludes failure
	// from drift alone — downgrade to neutral; config errors (lane above) still
	// fail on any repository.
	if forkReportOnly && resolved.Conclusion == orchestrator.ConclusionFailure {
		logger.info(
			"docsync action: fork PR — drift-only failure downgraded to neutral (advisory on forks, A7)")
		summary := "advisory on fork PRs (A7): documentation drift is reported without failing the check — " +
			"fork head repos are unwritable with GITHUB_TOKEN, so docSync runs report-only. " + resolved.Reason
		return ActionRunResult{
			ExitCode:   exitCode,
			Conclusion: conclusionPtr(orchestrator.ConclusionNeutral),
			Summary:    summary,
		}
	}

	logger.info("docsync action: exit " + strconv.Itoa(int(exitCode)) + ", conclusion " +
		string(resolved.Conclusion) + " — " + resolved.Reason)
	return ActionRunResult{
		ExitCode:   exitCode,
		Conclusion: conclusionPtr(resolved.Conclusion),
		Summary:    resolved.Reason,
	}
}

// hasAbstainsWithoutMarker reports a report that carries abstains but NONE
// carries the report-only downgrade marker (the fork-guard abstain check).
func hasAbstainsWithoutMarker(report *schemas.CanonicalReport) bool {
	if report == nil || len(report.Abstains) == 0 {
		return false
	}
	for i := range report.Abstains {
		if report.Abstains[i].Detail == ReportOnlyAbstainDetail {
			return false
		}
	}
	return true
}
