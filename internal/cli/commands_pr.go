// Ported from packages/cli/src/commands/pr.ts (+ pr/flow.ts; P7-T13..T17) — the
// SYNCHESIS verb: run the composed pipeline, apply ONLY VERIFIED minimal patches
// (or report an abort), re-verify on the branch, and open a documentation PR
// when drift remained. NOT applied to the checked-out branch: a LOCAL branch,
// verified, minor, then pushed — the "propose, never silently push" synthesis
// contract (V-P7-1).
//
// Fail-closed order (D8): same as fix up to changePr resolution (offline
// refused; base `--base`/`origin/HEAD`; default-branch + `--default-branch`
// refused when it would overwrite the default branch). Then `docsync pr`:
//  1. resolve changePr (the target branch; offline → no target;
//     `--source-pr` → `pull/<n>/head`);
//  2. run the composed fix flow + core doc-PR flow on changePr (seam-bound);
//  3. render: fix summary lines, the pr outcome payload, the optional fix
//     report, then the JSON `{ok, drift_detected?}` — ONE order, text wraps,
//     JSON closes;
//  4. exit: run failure OR an opened PR → 1 (drift that persists past the
//     patcher); an abort with no open PR → 0.
package cli

import (
	"context"
	"errors"

	"github.com/DUDE616/docSync-go/internal/gitx"
)

// prPrefix is the stderr line prefix for pr diagnostics.
const prPrefix = "docsync pr:"

// PrOptions are the runPr options.
type PrOptions struct {
	// Cwd is the repository root; defaults to the process cwd.
	Cwd string
	// Env is the environment map (key source only); defaults to os.Environ().
	Env map[string]string
	// Clock is the injected ISO-8601 clock; defaults to wallClock.
	Clock func() string
	// Transport is the model transport injection seam (recorded doubles, OD-2).
	Transport any
	// Flow (tests) injects the pr flow runner directly, bypassing the core
	// doc-PR flow (bound in flow_runtime.go).
	Flow func(ctx context.Context, input *PrFlowInput) (*PrPayload, error)
}

// PrFlowInput is the resolved flow state handed to the runner seam.
type PrFlowInput struct {
	ChangePr string
	Base     string
	SourcePr *int
}

// PrIssueRef is one ignored/refused issue.
type PrIssueRef struct {
	DocPath string `json:"docPath"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail"`
}

// PrResultVerdict is the doc-PR dry-run/skip verdict pair.
type prResultVerdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// PrPayload is the readable pr outcome a runPrFlow produces (limited CLI-shaped
// mirror — P7-T13; the full flow-state never leaves the verb).
type PrPayload struct {
	Applied         []FixFlowApplied `json:"_applied"`
	FixSummaryLines []string         `json:"_fixSummaryLines"`
	RunError        *string          `json:"_runError"`
	Ignored         []PrIssueRef     `json:"_ignored"`
	PrOpened        bool             `json:"_prOpened"`
	PrNumber        *int             `json:"_prNumber"`
	PushFailed      *string          `json:"_pushFailed"`
	PrSkipped       string           `json:"_prSkipped"`
	RecordedReplies int              `json:"_recordedReplies"`
	PrResult        *prResultVerdict `json:"_prResult"`
}

// RenderPrOutcome renders the human-readable pr lines (P7-T13/T14): summary,
// ignored, push failure, opened PR, dry-run/skip, then the `ok:` tail. Pure —
// the same lines text and json close with.
func RenderPrOutcome(payload PrPayload, glyphs GlyphMode) []string {
	glyph := fixGlyphSet(glyphs)
	lines := []string{}
	if len(payload.Applied) > 0 {
		lines = append(lines, "Applied "+itoa(len(payload.Applied))+" verified minimal patch(es) to local docs "+
			"(output is informational; patches commit on the pr branch, not here)")
		for _, entry := range payload.Applied {
			lines = append(lines, "  "+glyph.applied+" "+entry.DocPath+" ("+entry.PatchRef+")")
		}
	}
	for _, ref := range payload.Ignored {
		lines = append(lines, "  "+glyph.skipped+" "+ref.DocPath+" ("+ref.Reason+" — "+ref.Detail+")")
	}
	if payload.RunError != nil {
		lines = append(lines, "Pipeline run error (P7-T13 message): "+*payload.RunError)
	}
	if payload.PushFailed != nil {
		lines = append(lines, "Local branch push failed (fix the credential / repo grants and re-run; "+
			"local docs already patched): reason="+*payload.PushFailed)
	}
	if payload.PrOpened {
		number := "?"
		if payload.PrNumber != nil {
			number = itoa(*payload.PrNumber)
		}
		lines = append(lines, "Opened documentation PR #"+number+" ("+itoa(payload.RecordedReplies)+
			" reviewer repl"+plural(payload.RecordedReplies)+" recorded; docs verified on the branch, not here)")
	}
	if payload.PrResult != nil {
		verdict := payload.PrResult.Verdict
		reason := payload.PrResult.Reason
		switch verdict {
		case "dry_run":
			lines = append(lines, "Dry-run: prepared a documentation branch but opened no PR (dry-run)")
		case "skipped":
			lines = append(lines, "PR skipped: "+
				mapString(prSkipReason, reason, "not attempted yet — unexpected"))
		}
	}
	lines = append(lines, "ok: "+jsonBool(payload.RunError == nil))
	return lines
}

// jsonBool renders a bool as JSON `true`/`false` (TS String(Boolean)).
func jsonBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func prSkipReason(reason string) string {
	switch reason {
	case "after_verification_identical":
		return "re-verification proved the branch content unchanged — the doc PR is redundant, skipping"
	case "no_proposed_changes":
		return "no proposed doc changes on the branch — nothing to open"
	case "nothing_new":
		return "the remaining doc changes are not new"
	case "up_to_date":
		return "the docs are already up to date"
	default:
		return "not attempted yet — unexpected"
	}
}

func mapString(fn func(string) string, v, fallback string) string {
	out := fn(v)
	if out == "" {
		return fallback
	}
	return out
}

// itoa is a small int → decimal helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// JSONResultOf is the machine-readable pr outcome: a flat object
// mixing the POSTED side (ok, fix_outcome, ignored, push_failed) with the
// open-PR side (pr_opened/pr_number/recorded_replies/pr_result) and a `unknown`
// [] mirroring the text render's visible cases. Tests pin the EXACT keys
// (`unknown` never leaks).
func JSONResultOf(payload PrPayload) map[string]any {
	result := map[string]any{
		"ok": payload.RunError == nil,
	}
	if len(payload.FixSummaryLines) > 0 {
		result["fix_summary"] = payload.FixSummaryLines
	}
	fixOutcome := map[string]any{
		"applied": appliedDocPaths(payload.Applied),
	}
	result["fix_outcome"] = fixOutcome
	for _, ref := range payload.Ignored {
		ignored, _ := result["ignored"].([]any)
		result["ignored"] = append(ignored, prIgnoredJSON(ref))
	}

	if !payload.PrOpened {
		result["pr_opened"] = false
		var unknowns []any
		if payload.RunError != nil {
			unknowns = append(unknowns, prUnknown(prUnknownRunFailedNoOpenedPr))
		} else {
			unknowns = append(unknowns, prUnknown(prUnknownNoOpenedPrWithoutError))
		}
		result["unknown"] = unknowns
		return result
	}

	result["pr_opened"] = true
	if payload.PrNumber != nil {
		result["pr_number"] = *payload.PrNumber
	}
	result["recorded_replies"] = payload.RecordedReplies
	if payload.PrResult != nil {
		result["pr_result"] = map[string]any{
			"verdict": payload.PrResult.Verdict,
			"reason":  payload.PrResult.Reason,
		}
	}
	return result
}

const (
	prUnknownRunFailedNoOpenedPr    = "run_failed_no_opened_pr"
	prUnknownNoOpenedPrWithoutError = "no_opened_pr_without_error"
)

func prUnknown(kind string) map[string]any {
	return map[string]any{
		"kind":   kind,
		"detail": ": " + kind,
	}
}

func prIgnoredJSON(ref PrIssueRef) map[string]any {
	return map[string]any{
		"docPath": ref.DocPath,
		"reason":  ref.Reason,
		"detail":  ref.Detail,
	}
}

func appliedDocPaths(applied []FixFlowApplied) []string {
	paths := make([]string, 0, len(applied))
	for _, entry := range applied {
		paths = append(paths, entry.DocPath)
	}
	return paths
}

/* --------------------------------------------------------------- the runner */

// ErrPrSourcePull marks the composed source-pr fallback failure (the runner is
// reached without an extracted PullSnapshot).
var ErrPrSourcePull = errors.New("source-pr fallback requested with no extracted pull snapshot")

// PrFlowResult is the (renderable) pr flow state.
type prFlowState struct {
	Payload *PrPayload
}

// RunPr runs the `pr` verb (P7-T13..T17).
func RunPr(args *ParsedInvocation, io CliIo, options PrOptions) DocsyncExitCode {
	ctx := context.Background()
	repoRoot := options.Cwd
	if repoRoot == "" {
		repoRoot = "."
	}
	repo := "local/repo"
	if args.Repo != nil {
		repo = *args.Repo
	}
	env := options.Env
	if env == nil {
		env = osEnv()
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock
	}

	/* 1. Offline refusal: pr is a model-backed synthesis verb (OD-R16). */
	if args.Offline {
		io.Stderr(prPrefix + " --offline is not supported for pr — pr runs the full engine " +
			"(decision/writer/verifier) and opens a pull request (OD-R16: offline is check-only)")
		return ExitConfig
	}

	/* 2. Compose — ALL config validation happens here, before any AI stage. */
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repoRoot})
	if err != nil {
		return renderPrComposeFailure(err, io)
	}
	runtime, err := createCliPipelineRuntime(CliRuntimeOptions{
		RepoRoot:  repoRoot,
		Repo:      repo,
		Env:       env,
		Clock:     clock,
		Transport: options.Transport,
	})
	if err != nil {
		var cfg *CliConfigError
		if errors.As(err, &cfg) {
			io.Stderr(cfg.Error())
			for _, line := range cfg.Diagnostics {
				io.Stderr(line)
			}
			return ExitConfig
		}
		return renderPrRunFailure(err, io)
	}
	for _, advisory := range runtime.Advisories() {
		io.Stderr(prPrefix + " " + advisory)
	}

	/* 3. The change window. */
	base, ok := resolveBaseRef(ctx, repoRoot, args, io)
	if !ok {
		return ExitConfig
	}

	/* 4. changePr: `--source-pr` → `pull/<n>/head` sha; else the default branch
	 * (`--default-branch` names it, else origin/HEAD deref). The synthesis
	 * targets `docsync/default`; a run whose changePr equals the repo default
	 * (would overwrite it) is refused by the core flow. Offline → "" (dry-run). */
	changePr := ""
	changePr, ok = resolveChangePr(ctx, git, args, io)
	if !ok {
		return ExitConfig
	}

	/* 5. Run the composed fix + doc-PR flow (bound in flow_runtime.go). */
	runner := options.Flow
	if runner == nil {
		if PrFlowRunnerImpl == nil {
			io.Stderr(prPrefix + " the core doc-PR flow is not bound (internal wiring defect, fail closed D14)")
			return ExitRuntime
		}
		runner = PrFlowRunnerImpl
	}
	payload, err := runner(ctx, &PrFlowInput{ChangePr: changePr, Base: base, SourcePr: args.SourcePr})
	if err != nil {
		if prSourcePull(err) {
			// The documented source-pr refusal lane (test-visible behavior).
			io.Stderr(prPrefix + " " + describeErr(err))
			return ExitConfig
		}
		return renderPrRunFailure(err, io)
	}

	/* 6. Render (text wraps, json closes — ONE order). */
	if !args.JSON {
		for _, line := range payload.FixSummaryLines {
			io.Stdout(line)
		}
		if payload.PrOpened || payload.PrResult != nil {
			for _, line := range RenderPrOutcome(*payload, args.Glyphs) {
				io.Stdout(line)
			}
		}
	}

	/* 7. JSON close — one object; then the exit. */
	if args.JSON {
		io.Stdout(jsonString(JSONResultOf(*payload)))
	}

	if payload.RunError != nil || payload.PrOpened {
		return ExitDrift
	}
	return ExitOK
}

// schemasCanonicalReportNil is a placeholder to keep the optional fix-report
// seam compile without importing schemas purely for a nil pointer here.
type schemasCanonicalReportNil struct{}

// resolveChangePr resolves the changePr target: `--source-pr` → `pull/<n>/head`
// sha; else the default branch (`--default-branch` names it, else origin/HEAD
// deref). ok=false means the caller must exit (message already emitted).
func resolveChangePr(ctx context.Context, git *gitx.GitClient, args *ParsedInvocation, io CliIo) (string, bool) {
	if args.SourcePr != nil {
		ref := "pull/" + itoa(*args.SourcePr) + "/head"
		sha, err := git.ResolveRev(ctx, ref)
		if err != nil {
			io.Stderr(prPrefix + " could not resolve " + ref + ": " + describeErr(err))
			return "", false
		}
		return sha, true
	}
	if args.DefaultBranch != nil && *args.DefaultBranch != "" {
		if sha, err := git.ResolveRev(ctx, *args.DefaultBranch); err == nil {
			return sha, true
		}
	}
	sha, err := git.ResolveRev(ctx, "origin/HEAD")
	if err != nil {
		// No default branch resolvable: the synthesis target is empty, which the
		// core flow treats as dry-run when it cannot map to a branch.
		return "", true
	}
	return sha, true
}

func prSourcePull(err error) bool {
	return errors.Is(err, ErrPrSourcePull)
}

func renderPrComposeFailure(err error, io CliIo) DocsyncExitCode {
	io.Stderr(prPrefix + " " + describeErr(err))
	if isConfigClass(err) {
		return ExitConfig
	}
	return ExitRuntime
}

func renderPrRunFailure(err error, io CliIo) DocsyncExitCode {
	io.Stderr(prPrefix + " " + describeErr(err))
	if isConfigClass(err) {
		return ExitConfig
	}
	return ExitRuntime
}

// PrFlowRunnerImpl is the production pr flow runner (fix flow + core doc-PR
// flow); bound at init in flow_runtime.go.
var PrFlowRunnerImpl func(ctx context.Context, input *PrFlowInput) (*PrPayload, error)
