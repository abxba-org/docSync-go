// Ported from packages/action/src/main.ts (P8-T15/T17 wiring): the module the
// GitHub Actions runtime loads as `runs.main`. Responsibilities:
//
//  1. Map the declared action.yml inputs (INPUT_*) onto the RunnerEnv keys the
//     runner consumes (MapActionInputsToRunnerEnv): INPUT_BASE → GITHUB_BASE_REF,
//     INPUT_STRICT → DOCSYNC_STRICT, INPUT_REPORT_ONLY → DOCSYNC_REPORT_ONLY.
//     INPUT_CHECK_NAME / INPUT_OFFLINE are declared but NOT consumed by the V1
//     runner/port — an `offline: true` run is logged as a warn so the gap is
//     observable, never silent (fail-closed honesty).
//  2. Read the event payload from GITHUB_EVENT_PATH (a missing or unreadable
//     path fails CLOSED to the configuration lane, exit 2 — never "no drift",
//     D8). The raw payload string is handed to the runner, whose typed parser
//     maps malformed JSON to the same config lane.
//  3. Compose the default port (the DefaultCheckRunPortFunc seam, backed by
//     internal/cli — TODO(docsync-action/cli)) OR use the injected Check seam,
//     then call RunActionRunner.
//  4. Write the outputs protocol (exit-code / conclusion lines appended to
//     GITHUB_OUTPUT, best-effort — never a crash), one summary line to stderr,
//     and apply the process exit code (the caller applies os.Exit with the
//     returned result — the entry exception that turns the runner's exit-code
//     table into the workflow step outcome).
//
// The env map, the fs seam, and the io seam are all injected so tests are
// network-free (OD-2); the real process wiring lives in cmd/docsync-action.

package action

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
)

// ActionPackageVersion mirrors the TS ACTION_PACKAGE_VERSION barrel constant
// (index.ts). The library barrel surface stays stable across the migration.
const ActionPackageVersion = "0.0.0"

// MainFS is the fs seam for the event-payload read and the outputs append.
type MainFS struct {
	ReadFile   func(path string) (string, error)
	AppendFile func(path, data string) error
}

// MainIO is the io seam for the summary line and the process exit code.
type MainIO struct {
	Stderr      func(line string)
	SetExitCode func(code int)
}

// DefaultCheckRunPortFunc is the seam that composes the default CheckRunPort
// (port.go; the composition binds to the internal/cli pipeline runtime —
// TODO(docsync-action/cli)). Production wiring supplies it; when Check is also
// nil, RunMain fails closed (config-error) rather than silently producing a
// run without a port.
type DefaultCheckRunPortFunc func() (CheckRunPort, error)

// MainDeps are the injectable deps of RunMain (defaults = the real process,
// supplied by cmd/docsync-action via OSFS / OSIO).
type MainDeps struct {
	// Env is the process env shape (GITHUB_*/INPUT_*/…); nil ⇒ the real env
	// (cmd builds it via OSEnv).
	Env map[string]string
	// FS is the fs seam; nil ⇒ OSFS().
	FS *MainFS
	// IO is the io seam; nil ⇒ OSIO().
	IO *MainIO
	// Check is the injected check port (tests): bypasses the default port.
	Check CheckRunPort
	// NewDefaultPort composes the default port when Check is nil (production).
	NewDefaultPort DefaultCheckRunPortFunc
	// Clock is the ISO-8601 clock forwarded to the default port; nil ⇒ the real
	// wall clock (cmd supplies a deterministic one when needed).
	Clock func() string
}

// OSEnv snapshots the process environment into the env map shape.
func OSEnv() map[string]string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			env[entry[:idx]] = entry[idx+1:]
		}
	}
	return env
}

// OSFS is the real fs seam (node:fs/promises readFile / appendFile).
func OSFS() *MainFS {
	return &MainFS{
		ReadFile: func(path string) (string, error) {
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
		AppendFile: func(path, data string) error {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = file.WriteString(data)
			return err
		},
	}
}

// OSIO is the real io seam. SetExitCode is a no-op here — the caller
// (cmd/docsync-action) applies the process exit via os.Exit(result.ExitCode)
// after RunMain returns (single exit point).
func OSIO() *MainIO {
	return &MainIO{
		Stderr:      func(line string) { fmt.Fprintln(os.Stderr, line) },
		SetExitCode: func(int) {},
	}
}

// MapActionInputsToRunnerEnv maps the GitHub Actions input env (INPUT_*) onto
// the RunnerEnv keys the runner consumes. Pure; only the runner-consumed keys
// are mapped (faithful wiring); INPUT_CHECK_NAME / INPUT_OFFLINE have no runner
// key in V1 and are surfaced by the caller.
func MapActionInputsToRunnerEnv(env map[string]string) RunnerEnv {
	runnerEnv := RunnerEnv{}

	// GITHUB_* passthrough: the full shape parseActionEvent / RunnerEnv consume.
	for _, key := range []string{
		"GITHUB_EVENT_NAME",
		"GITHUB_REF",
		"GITHUB_HEAD_REF",
		"GITHUB_BASE_REF",
		"GITHUB_SHA",
		"GITHUB_REPOSITORY",
		"GITHUB_TOKEN",
	} {
		if value, ok := env[key]; ok && len(value) > 0 {
			switch key {
			case "GITHUB_EVENT_NAME":
				runnerEnv.GithubEventName = value
			case "GITHUB_REF":
				runnerEnv.GithubRef = value
			case "GITHUB_HEAD_REF":
				runnerEnv.GithubHeadRef = value
			case "GITHUB_BASE_REF":
				runnerEnv.GithubBaseRef = value
			case "GITHUB_SHA":
				runnerEnv.GithubSha = value
			case "GITHUB_REPOSITORY":
				runnerEnv.GithubRepository = value
			case "GITHUB_TOKEN":
				runnerEnv.GithubToken = value
			}
		}
	}

	// INPUT_BASE → the base ref the port diffs against (overrides the event's
	// GITHUB_BASE_REF when the workflow pins one explicitly).
	if base := env["INPUT_BASE"]; len(base) > 0 {
		runnerEnv.GithubBaseRef = base
	}

	// Enforcement flags (D10): strict and report-only are exact-'true' booleans.
	runnerEnv.DocsyncStrict = exactTrueFlag(env["INPUT_STRICT"])
	runnerEnv.DocsyncReportOnly = exactTrueFlag(env["INPUT_REPORT_ONLY"])

	return runnerEnv
}

// exactTrueFlag is the TS `x === 'true' ? 'true' : 'false'` honest default.
func exactTrueFlag(value string) string {
	if value == "true" {
		return "true"
	}
	return "false"
}

// configEventFailure is the entry-side config lane for an unreadable event
// payload (D8): a missing/invalid payload is a CONFIGURATION error (exit 2),
// never "no drift" (mirrors the runner's exitConfigError shape).
func configEventFailure(diagnostics []orchestrator.ConfigErrorDiagnostic) *ActionRunResult {
	result := exitConfigError(diagnostics)
	return &result
}

// readEventPayloadReadResult carries either the raw payload string or a
// fail-closed config-event failure.
type readEventPayloadResult struct {
	payload string
	failure *ActionRunResult
}

// readEventPayload reads the raw event payload; a missing/unreadable path is a
// config lane (D8).
func readEventPayload(env map[string]string, fs *MainFS) readEventPayloadResult {
	eventPath := env["GITHUB_EVENT_PATH"]
	if len(eventPath) == 0 {
		return readEventPayloadResult{failure: configEventFailure([]orchestrator.ConfigErrorDiagnostic{
			{
				File:    "(action event)",
				Message: "GITHUB_EVENT_PATH is missing (fail-closed, D8): the event payload cannot be read",
			},
		})}
	}
	content, err := fs.ReadFile(eventPath)
	if err != nil {
		return readEventPayloadResult{failure: configEventFailure([]orchestrator.ConfigErrorDiagnostic{
			{
				File:    "(action event)",
				Message: "GITHUB_EVENT_PATH is unreadable (fail-closed, D8): " + err.Error(),
			},
		})}
	}
	return readEventPayloadResult{payload: content}
}

// writeOutputs appends the outputs-protocol lines; best-effort, never a crash
// (D8). A missing GITHUB_OUTPUT is a silent no-op; an unwritable file is logged.
func writeOutputs(env map[string]string, fs *MainFS, io *MainIO, result ActionRunResult) {
	outputPath := env["GITHUB_OUTPUT"]
	if len(outputPath) == 0 {
		return
	}
	lines := []string{fmt.Sprintf("exit-code=%s", strconv.Itoa(int(result.ExitCode)))}
	if result.Conclusion != nil {
		lines = append(lines, "conclusion="+string(*result.Conclusion))
	}
	data := strings.Join(lines, "\n") + "\n"
	if err := fs.AppendFile(outputPath, data); err != nil {
		io.Stderr("docsync action: GITHUB_OUTPUT write skipped (outputs unavailable): " + err.Error())
	}
}

// summaryLine renders the one-line entry transcript (`conclusion none` when a
// lane carried no conclusion).
func summaryLine(result ActionRunResult) string {
	conclusion := "none"
	if result.Conclusion != nil {
		conclusion = string(*result.Conclusion)
	}
	return fmt.Sprintf("docsync action: exit %s, conclusion %s — %s",
		strconv.Itoa(int(result.ExitCode)), conclusion, result.Summary)
}

// RunMain runs the Action end-to-end (inputs → event → runner → outputs → exit
// code). Returns the runner result; the process outcome is expressed through
// GITHUB_OUTPUT + the returned exit code (the caller applies os.Exit).
func RunMain(deps MainDeps) ActionRunResult {
	env := deps.Env
	if env == nil {
		env = OSEnv()
	}
	fs := deps.FS
	if fs == nil {
		fs = OSFS()
	}
	io := deps.IO
	if io == nil {
		io = OSIO()
	}
	logger := &ActionLogger{Info: io.Stderr, Warn: io.Stderr}

	// Declared-but-unconsumed inputs: observable, never silent.
	if env["INPUT_OFFLINE"] == "true" {
		io.Stderr(
			"docsync action: input 'offline: true' is not wired by the V1 runner/port — " +
				"the run is NOT offline (see the package README, declared-input gaps)")
	}

	runnerEnv := MapActionInputsToRunnerEnv(env)

	read := readEventPayload(env, fs)
	if read.failure != nil {
		// Missing/unreadable GITHUB_EVENT_PATH → the config lane (exit 2, D8).
		result := *read.failure
		writeOutputs(env, fs, io, result)
		io.Stderr(summaryLine(result))
		io.SetExitCode(int(result.ExitCode))
		return result
	}

	check := deps.Check
	if check == nil && deps.NewDefaultPort != nil {
		composed, err := deps.NewDefaultPort()
		if err != nil {
			result := *configEventFailure([]orchestrator.ConfigErrorDiagnostic{
				{File: "(action default port)", Message: "default check-run port composition failed: " + err.Error()},
			})
			writeOutputs(env, fs, io, result)
			io.Stderr(summaryLine(result))
			io.SetExitCode(int(result.ExitCode))
			return result
		}
		check = composed
	}
	if check == nil {
		// No port configured and no default composition seam bound — fail
		// closed (D8/D14) rather than silently reporting a run without a check.
		result := *configEventFailure([]orchestrator.ConfigErrorDiagnostic{
			{
				File: "(action default port)",
				Message: "no check-run port configured and the default port seam (internal/cli) is unbound " +
					"— failing closed (TODO(docsync-action/cli))",
			},
		})
		writeOutputs(env, fs, io, result)
		io.Stderr(summaryLine(result))
		io.SetExitCode(int(result.ExitCode))
		return result
	}

	result := RunActionRunner(RunActionRunnerDeps{
		Check:   check,
		Env:     &runnerEnv,
		Payload: read.payload,
		Logger:  logger,
	})

	writeOutputs(env, fs, io, result)
	io.Stderr(summaryLine(result))
	io.SetExitCode(int(result.ExitCode))
	return result
}
