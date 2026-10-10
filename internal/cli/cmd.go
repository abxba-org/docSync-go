// Ported from packages/cli/src/bin.ts (P7-T01; exit-code surface P7-T06/T07) —
// the command registry + dispatcher. RunCli is the testable entrypoint: it takes
// argv, writes through an injected CliIo, and RETURNS the DocsyncExitCode
// instead of calling os.Exit (library code never exits the process).
//
// Exit-code discipline: usage/config-class errors → 2 (D8 lane, fail-closed
// before any AI stage); runtime failures → 3 (D14 lane).
package cli

// RunCliOptions configures RunCli.
type RunCliOptions struct {
	// Io is the output sink; defaults to DefaultIo (the real process streams).
	Io CliIo
	// Cwd is the repository root for verbs that need one.
	Cwd string
}

// RunCli dispatches one CLI invocation and returns the process exit code.
func RunCli(argv []string, options RunCliOptions) DocsyncExitCode {
	io := options.Io
	if io == nil {
		io = DefaultIo()
	}
	invocation := ParseCliArgs(argv)

	if invocation.Help {
		io.Stdout(UsageText())
		return ExitOK
	}

	// Usage/config-class errors exit 2 (D8): unknown verb/flag/value or no verb.
	if invocation.Error != "" || invocation.Verb == "" {
		errText := invocation.Error
		if errText == "" {
			errText = "no verb given"
		}
		io.Stderr("docsync: " + errText)
		io.Stderr(UsageText())
		return ExitConfig
	}

	switch invocation.Verb {
	case VerbInit:
		result := RunInit(InitOptions{
			RepoRoot: options.Cwd,
			Force:    invocation.Force,
		})
		emitInit(result, invocation.JSON, io)
		return result.ExitCode
	case VerbCheck:
		return RunCheck(&invocation, io, CheckOptions{Cwd: options.Cwd})
	case VerbFix:
		return RunFix(&invocation, io, FixOptions{Cwd: options.Cwd})
	case VerbPr:
		return RunPr(&invocation, io, PrOptions{Cwd: options.Cwd})
	case VerbAudit:
		return RunAudit(&invocation, io, AuditOptions{Cwd: options.Cwd})
	default:
		io.Stderr("docsync: unknown verb: " + string(invocation.Verb))
		io.Stderr(UsageText())
		return ExitConfig
	}
}

// emitInit renders init output: one JSON line with --json, human lines otherwise.
func emitInit(result InitResult, json bool, io CliIo) {
	if json {
		io.Stdout(jsonString(initEmitPayload{
			OK:       result.ExitCode == ExitOK,
			Messages: result.Messages,
		}))
		return
	}
	for _, message := range result.Messages {
		io.Stdout(message)
	}
}

// initEmitPayload is the `docsync init --json` object shape ({ok, messages}).
type initEmitPayload struct {
	OK       bool     `json:"ok"`
	Messages []string `json:"messages"`
}
