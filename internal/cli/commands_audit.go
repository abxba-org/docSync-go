// Ported from packages/cli/src/commands/audit.ts (P12-T16; ARCH §7.2 Loop B) —
// the Loop B background-audit verb. Runs the composed, read-mostly, ADVISORY
// audit from the CLI and renders its artifacts locally.
//
// Posture (binding): advisory ONLY — NO document writes, NO patch application;
// NEVER on the PR critical path; cadence/cost tuning (`--trigger`,
// `--concurrency`, `--commit-walk`) via explicit flags; `--org-report` renders
// the org-shaped drift report for THIS repository (V1: no cross-repository
// credential surface).
//
// Exit codes: manifest/config defects → 2; runtime failures (including a
// fail-closed abstained audit) → 3; a completed audit with findings → 1 (drift);
// a completed audit without findings, and every typed no-op → 0.
//
// DECLARED SEAM (orchestrator to rebind): the loop-B engine run + org-report
// aggregation/render are wired through AuditRunnerImpl. While it is nil, an
// otherwise-valid audit run fails closed (D14, exit 3) with an honest message.
// The deterministic arg-resolution, org-report refusal, manifest gate and exit
// mapping work without the engine (covered by tests). TODO(loop-b-engine).
package cli

import (
	"context"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/validate"
)

// auditPrefix is the stderr line prefix for audit diagnostics.
const auditPrefix = "docsync audit:"

// AuditDefaultConcurrency is the engine worker-pool default (P5-T12 parity).
const AuditDefaultConcurrency = 4

// AuditDefaultCommitWalk is the sweep commit-walk default.
const AuditDefaultCommitWalk = 50

// AuditOptions configures RunAudit.
type AuditOptions struct {
	// Cwd is the repository root; defaults to the process cwd.
	Cwd string
	// RunID is an injected run id (tests).
	RunID string
	// Clock is an injected clock (tests); defaults to wallClock.
	Clock func() string
}

// AuditRunResult is the closed loop-B run result the engine seam returns.
type AuditRunResult struct {
	// Kind is the typed run kind (noop / abstained / ran).
	Kind AuditRunKind
	// Reason is the noop reason or abstained detail.
	Reason string
	// Status is the run status for a `ran` result ("completed" ⇒ exit from findings).
	Status string
	// Detail is the failure detail for a non-completed `ran` result.
	Detail string
	// FindingCount is this run's new finding count.
	FindingCount int
	// AppendedCount is the newly-appended finding count.
	AppendedCount int
	// RunID is the run-record id.
	RunID string
	// Trigger is the trigger that ran.
	Trigger string
	// IssueMarkdown is the rendered artifacts.issue (verbose).
	IssueMarkdown string
	// OrgMarkdown is the rendered org drift report (present only with --org-report).
	OrgMarkdown string
}

// AuditRunKind is the typed loop-B run kind.
type AuditRunKind string

// The loop-B run kinds.
const (
	AuditRunNoop      AuditRunKind = "noop"
	AuditRunAbstained AuditRunKind = "abstained"
	AuditRunRan       AuditRunKind = "ran"
)

// AuditRunnerInput carries the resolved inputs the loop-B engine seam consumes.
type AuditRunnerInput struct {
	RepoRoot    string
	Repo        string
	RunID       string
	Now         string
	Trigger     string
	Branch      string
	PostMerge   bool
	Concurrency int
	CommitWalk  int
	OrgReport   bool
	Advisories  []string
}

// AuditRunnerImpl runs the loop-B audit engine + org-report; bound at init in
// flow_runtime.go. While nil a valid audit run fails closed (D14, exit 3).
var AuditRunnerImpl func(ctx context.Context, input AuditRunnerInput) (AuditRunResult, error)

// parseIntFlag parses a `*string` flag to a positive int, falling back when
// absent/invalid (TS parseIntFlag).
func parseIntFlag(value *string, fallback int) int {
	if value == nil {
		return fallback
	}
	n, ok := parseIntLenient(*value)
	if !ok || n <= 0 || n > 1<<31-1 {
		return fallback
	}
	return n
}

// isRepoFullName reports whether repo is a full `org/name` with BOTH segments
// non-empty (F4/L1): `/x` or a bare `widgets` would publish a report the
// registry schema refuses (`org: min(1)`).
func isRepoFullName(repo string) bool {
	slash := indexByte(repo, '/')
	if slash == -1 {
		return false
	}
	return len(repo[:slash]) > 0 && len(repo[slash+1:]) > 0
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// RunAudit runs the `audit` verb (P12-T16). Never writes documents; the only
// files it touches are the core-owned advisory state stores under
// `.docsync/state/`.
func RunAudit(args *ParsedInvocation, io CliIo, options AuditOptions) DocsyncExitCode {
	ctx := context.Background()
	repoRoot := options.Cwd
	if repoRoot == "" {
		repoRoot = "."
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock
	}

	// Cadence/cost knobs (P12-T16): explicit flags, clamped >= 1.
	trigger := AuditTriggerNightly
	if args.AuditTrigger != nil {
		trigger = *args.AuditTrigger
	}
	branch := "main"
	if args.AuditBranch != nil {
		branch = *args.AuditBranch
	}
	concurrency := parseIntFlag(args.AuditConcurrency, AuditDefaultConcurrency)
	// L2: the walk depth is clamped to the documented ceiling.
	commitWalk := parseIntFlag(args.AuditCommitWalk, AuditDefaultCommitWalk)
	if commitWalk > AuditCommitWalkCeiling {
		commitWalk = AuditCommitWalkCeiling
	}
	postMerge := trigger == string(AuditTriggerPostMerge) || args.AuditPostMerge
	repoName := "local/repo"
	if args.Repo != nil {
		repoName = *args.Repo
	}

	/* 0. Usage gate (L1, F4) — `--org-report` aggregates a repository
	 * identified by a FULL `org/name`; a malformed `--repo` is refused BEFORE
	 * the engine runs (exit-2 lane, D8). */
	if args.AuditOrgReport && !isRepoFullName(repoName) {
		io.Stderr(auditPrefix + " invalid --repo value: " + jsonStringLiteral(repoName) +
			" (expected a full org/name — an empty org segment is refused, fail closed)")
		return ExitConfig
	}

	/* 1. Manifest gate (D7/D8) — the loader renders every diagnostic; an
	 * invalid manifest is exit 2 BEFORE anything runs. Prose-only on stderr
	 * (even under --json, G2-2). */
	manifestResult, err := validate.LoadAndValidateManifest(repoRoot, validate.ManifestValidateOptions{})
	if err != nil {
		io.Stderr(auditPrefix + " " + describeErr(err))
		return ExitConfig
	}
	if manifestResult.Manifest == nil {
		for _, diagnostic := range manifestResult.Diagnostics {
			io.Stderr(auditPrefix + " " + diagnostic.Code + ": " + diagnostic.Message)
		}
		return ExitConfig
	}

	/* 1b. Report-only advisories rendered on stderr BEFORE anything runs. */
	advisories := advisoryMessages(manifestResult.Advisories)
	for _, message := range advisories {
		io.Stderr(auditPrefix + " " + message)
	}

	// F1-4: ONE clock stamp per invocation.
	runStamp := clock()
	runID := options.RunID
	if runID == "" {
		runID = string(AuditTriggerNightly) + ":" + repoName
	}

	/* 2. The loop-B engine run (bound in flow_runtime.go). */
	if AuditRunnerImpl == nil {
		io.Stderr(auditPrefix + " loop-B audit engine is not bound (internal wiring defect, fail closed D14)")
		return ExitRuntime
	}
	result, err := AuditRunnerImpl(ctx, AuditRunnerInput{
		RepoRoot:    repoRoot,
		Repo:        repoName,
		RunID:       runID,
		Now:         runStamp,
		Trigger:     trigger,
		Branch:      branch,
		PostMerge:   postMerge,
		Concurrency: concurrency,
		CommitWalk:  commitWalk,
		OrgReport:   args.AuditOrgReport,
		Advisories:  advisories,
	})
	if err != nil {
		io.Stderr(auditPrefix + " runtime failure (fail closed, D14): " + describeErr(err))
		return ExitRuntime
	}

	/* 3. Render + exit mapping (advisory output only). */
	return renderAuditResult(args, io, result)
}

// renderAuditResult renders the loop-B result and maps it onto the exit code.
func renderAuditResult(args *ParsedInvocation, io CliIo, result AuditRunResult) DocsyncExitCode {
	switch result.Kind {
	case AuditRunNoop:
		if args.JSON {
			io.Stdout(jsonString(auditNoopPayload{Kind: "noop", Reason: result.Reason}))
		} else {
			io.Stdout(auditPrefix + " trigger not eligible (" + result.Reason + ") — nothing ran, nothing wrote " +
				"(advisory; ARCH §7.2, V-P11-8)")
		}
		return ExitOK

	case AuditRunAbstained:
		if args.JSON {
			io.Stdout(jsonString(auditAbstainedPayload{Kind: "abstained", Detail: result.Reason}))
		}
		io.Stderr(auditPrefix + " abstained (fail closed): " + result.Reason)
		return ExitRuntime

	case AuditRunRan:
		// F1-1: a degraded engine run is a fail-closed RUNTIME failure (D14).
		if result.Status != AuditStatusCompleted {
			detail := result.Detail
			if detail == "" {
				detail = "no detail"
			}
			io.Stderr(auditPrefix + " run " + result.RunID + " ended status \"" + result.Status + "\" — " +
				"failing closed (D14): " + detail)
			return ExitRuntime
		}

		if args.JSON {
			io.Stdout(jsonString(auditRanPayload{
				OK:        true,
				Trigger:   result.Trigger,
				RunID:     result.RunID,
				Status:    result.Status,
				Findings:  result.FindingCount,
				Appended:  result.AppendedCount,
				Advisory:  true,
				OrgReport: result.OrgMarkdown,
			}))
			return auditRanExit(result)
		}

		io.Stdout(auditPrefix + " " + result.Trigger + " run " + result.RunID + " — status " +
			result.Status + ", " + strconv.Itoa(result.FindingCount) + " finding(s) " +
			"(" + strconv.Itoa(result.AppendedCount) + " newly appended; advisory only, no writes)")
		if result.IssueMarkdown != "" && args.Verbose {
			io.Stdout(result.IssueMarkdown)
		}
		if result.OrgMarkdown != "" {
			io.Stdout(result.OrgMarkdown)
		}
		return auditRanExit(result)
	}
	return ExitOK
}

// auditRanExit maps a completed run onto 1 (drift) or 0 (clean) by findings.
func auditRanExit(result AuditRunResult) DocsyncExitCode {
	if result.FindingCount > 0 {
		return ExitDrift
	}
	return ExitOK
}

// Audit trigger-context values.
const (
	AuditTriggerNightly   = "nightly"
	AuditTriggerPostMerge = "post-merge"
)

const AuditStatusCompleted = "completed"

// JSON payload shapes (F1-2: one parseable object per --json surface).
type auditNoopPayload struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type auditAbstainedPayload struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

type auditRanPayload struct {
	OK        bool   `json:"ok"`
	Trigger   string `json:"trigger"`
	RunID     string `json:"runId"`
	Status    string `json:"status"`
	Findings  int    `json:"findings"`
	Appended  int    `json:"appended"`
	Advisory  bool   `json:"advisory"`
	OrgReport string `json:"orgReport,omitempty"`
}
