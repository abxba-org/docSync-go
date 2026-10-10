// Ported from packages/cli/src/commands/check.ts (P7-T01/T02; exit codes
// P7-T06/T07; OD-R16 offline; OD-R15-adjacent config surfacing) — the READ-ONLY
// pipeline verb: run the composed Phase-5 stage adapters through core's
// deterministic stage machine, render the shared canonical report, resolve the
// exit code. NEVER writes: check produces no doc patch, no file write, no
// CLI-owned state (the core orchestrator persists its own run record under
// `.docsync/state/` — a core-owned provenance write, see compose.go).
//
// Order of operations (fail closed, D8):
//  1. compose the runtime — manifest/policy/models/credentials all validated
//     BEFORE any AI stage; a CliConfigError renders its diagnostics on stderr
//     and exits 2;
//     1b. report-only advisories (P12-T13; V-P12-12) rendered on stderr BEFORE the
//     exit-code decision;
//  2. resolve the change window: `--base <ref>` (default `origin/HEAD` when
//     resolvable), head = `HEAD`;
//  3. run; typed failures map to 2 (config) / 3 (runtime);
//  4. render: buildCanonicalReport (schema self-validating — an invalid report
//     fails closed to exit 3, never emitted); `--json` emits the canonical
//     report object (plus the optional advisories key); text mode renders via
//     the shared RenderCheckText;
//  5. offline marker line on stdout in text mode (OD-R16);
//  6. `--verbose` (cross-cutting): one line per outcome.stages entry;
//  7. exit: offline → resolveOfflineExitCode; online → resolveCheckExitCode.
//
// Credentials discipline (§3 rule 8): the API key never reaches this module — it
// lives inside the composed client; nothing here reads or renders env.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// checkPrefix is the stderr line prefix for check diagnostics.
const checkPrefix = "docsync check:"

// offlineMarker is the offline marker line (OD-R16) — text mode only.
const offlineMarker = "docsync check: offline mode (OD-R16) — deterministic-only run, no model stages"

// CheckOptions are the runCheck options (deps injected for tests; production
// callers omit Runtime/Transport/Clock).
type CheckOptions struct {
	// Cwd is the repository root; defaults to the process cwd.
	Cwd string
	// Env is the environment map (key source only); defaults to os.Environ().
	Env map[string]string
	// Clock is the injected ISO-8601 clock; defaults to wallClock.
	Clock func() string
	// Runtime is a pre-built runtime injection seam (tests inject a fake so the
	// exit mapping/render is exercised without a real model pipeline).
	Runtime CliRuntime
	// Transport is the model transport injection seam (recorded doubles, OD-2).
	Transport any
	// Budgets are the pipeline budget overrides (tests cap the writer pool).
	Budgets *orchestrator.PipelineBudgetOverrides
}

// RunCheck runs the `check` verb (P7-T01). Never calls os.Exit — the returned
// DocsyncExitCode is the process exit code (P7-T06/T07).
func RunCheck(args *ParsedInvocation, io CliIo, options CheckOptions) DocsyncExitCode {
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

	/* 1. Compose — ALL config validation happens here, before any AI stage. */
	var runtime CliRuntime
	if options.Runtime != nil {
		runtime = options.Runtime
	} else {
		composed, err := createCliPipelineRuntime(CliRuntimeOptions{
			RepoRoot:  repoRoot,
			Repo:      repo,
			Env:       env,
			Clock:     clock,
			Offline:   args.Offline,
			Transport: options.Transport,
		})
		if err != nil {
			return renderCheckComposeFailure(err, io)
		}
		runtime = composed
	}

	/* 1b. Report-only advisories — rendered on stderr BEFORE the exit decision. */
	for _, message := range runtime.Advisories() {
		io.Stderr(checkPrefix + " " + message)
	}

	/* 2. The change window: base (required, default origin/HEAD) .. HEAD. */
	base, ok := resolveBaseRef(ctx, repoRoot, args, io)
	if !ok {
		return ExitConfig
	}

	/* 3. Run. Typed failures map onto the exit lanes (D8/D14). */
	outcome, err := runtime.Run(ctx, CliRuntimeRunInput{
		Base:    base,
		Head:    "HEAD",
		Budgets: options.Budgets,
	})
	if err != nil {
		return renderCheckRunFailure(err, io)
	}

	/* 4. Render — the canonical report self-validates; schema drift fails
	 * closed to exit 3 and NEVER emits (P7-T01 renderer contract). */
	report, err := orchestrator.BuildCanonicalReport(outcome)
	if err != nil {
		return renderCheckReportFailure(err, io)
	}
	if args.JSON {
		if len(runtime.Advisories()) > 0 {
			advisories := runtime.Advisories()
			report.Advisories = &advisories
		}
		io.Stdout(jsonString(report))
	} else {
		if args.Offline {
			io.Stdout(offlineMarker)
		}
		quiet := args.Quiet
		io.Stdout(orchestrator.RenderCheckText(report, &orchestrator.CheckRenderOptions{
			Glyphs: glyphSetOf(args.Glyphs),
			Quiet:  &quiet,
		}))
	}

	/* 5. --verbose (cross-cutting): one line per recorded pipeline stage. */
	if args.Verbose {
		for _, stage := range outcome.Stages {
			io.Stdout(verboseStageLine(stage))
		}
	}

	/* 6. Exit code: offline has its own documented semantics (compose.go). */
	if args.Offline {
		return resolveOfflineExitCode(outcome)
	}
	return orchestrator.ResolveCheckExitCode(report, &orchestrator.CheckExitOptions{Strict: args.Strict})
}

// resolveBaseRef resolves the base ref: explicit `--base`, else `origin/HEAD`
// when the repo resolves it, else fail with a clear message (exit 2 — a
// required-flag config defect, D8). ok=false means the caller must exit 2.
func resolveBaseRef(ctx context.Context, repoRoot string, args *ParsedInvocation, io CliIo) (string, bool) {
	if args.Base != nil {
		return *args.Base, true
	}
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repoRoot})
	if err != nil {
		io.Stderr(checkPrefix + " " + checkBaseUnresolvable)
		return "", false
	}
	if _, err := git.ResolveRev(ctx, "origin/HEAD"); err != nil {
		io.Stderr(checkBaseUnresolvable)
		return "", false
	}
	return "origin/HEAD", true
}

// checkBaseUnresolvable is the unresolvable-base diagnostic (one line).
const checkBaseUnresolvable = checkPrefix +
	" no --base given and 'origin/HEAD' is not resolvable in this repository — " +
	"pass --base <ref> (the change window is <base>..HEAD)"

// renderCheckComposeFailure renders a composition error; returns the mapped exit.
func renderCheckComposeFailure(err error, io CliIo) DocsyncExitCode {
	var cfg *CliConfigError
	if errors.As(err, &cfg) {
		io.Stderr(cfg.Error())
		for _, line := range cfg.Diagnostics {
			io.Stderr(line)
		}
		return ExitConfig
	}
	return renderCheckRunFailure(err, io)
}

// renderCheckRunFailure renders one caught run error honestly; maps to the lane.
func renderCheckRunFailure(err error, io CliIo) DocsyncExitCode {
	io.Stderr(checkPrefix + " " + describeErr(err))
	if isConfigClass(err) {
		return ExitConfig
	}
	return ExitRuntime
}

// renderCheckReportFailure renders a canonical-report schema failure (fail
// closed, exit 3, nothing emitted).
func renderCheckReportFailure(err error, io CliIo) DocsyncExitCode {
	io.Stderr(checkPrefix +
		" canonical report failed schema validation — failing closed, nothing emitted (D14): " +
		describeErr(err))
	return ExitRuntime
}

// verboseStageLine renders one `--verbose` stage line (check/fix/pr parity).
func verboseStageLine(stage schemas.PipelineStage) string {
	latency := "null"
	if stage.LatencyMS != nil {
		latency = strconv.Itoa(*stage.LatencyMS)
	}
	return "stage=" + stage.Stage + " status=" + string(stage.Status) + " latency_ms=" + latency
}

// jsonString marshals a value to a compact JSON string (TS JSON.stringify).
func jsonString(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// glyphSetOf maps the parsed GlyphMode onto the renderer GlyphSet.
func glyphSetOf(mode GlyphMode) orchestrator.GlyphSet {
	if mode == GlyphsFallback {
		return orchestrator.GlyphsFallback
	}
	return orchestrator.GlyphsUnicode
}
