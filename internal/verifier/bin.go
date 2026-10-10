// Ported (minimally) from packages/verifier/src/bin.ts (84 lines).
//
// docsync-audit — the standalone audit-replay binary (Phase 9, P9-T20/P9-T21;
// PLAN §10.7; decision 3-4: the tool lives in @docsync/verifier and is the
// monorepo's first package `bin`). Reads a COMMITTED `.docsync/state/`
// directory alone — no git, no network, no GitHub. Exit codes mirror the CLI
// contract: 0 success · 2 config error (missing flags / state dir) · 3
// runtime failure.
//
// Kept minimal by design: it becomes cmd/docsync-verifier later (the
// orchestrator wires the entrypoint). The only effects are the injected
// writers and the replay port's reads; the binary never writes state.
package verifier

import (
	"errors"
	"os"
	"strings"
)

// AuditReplayCLIIo is the binary's output seam (stdout/stderr line writers).
type AuditReplayCLIIo struct {
	Stdout func(line string)
	Stderr func(line string)
}

// AuditReplayCLIUsage is the help text (TS USAGE, verbatim).
var AuditReplayCLIUsage = strings.Join([]string{
	"docsync-audit — reconstruct the audit trail for a run from committed state alone",
	"",
	"Usage: docsync-audit --state-dir <path> --run <ulid> [--doc <docPath>]",
	"",
	"  --state-dir  the committed `.docsync` directory (required)",
	"  --run        the run id to replay (required)",
	"  --doc        restrict verification ledgers to one doc path (optional)",
}, "\n")

// Audit CLI exit codes (the CLI contract: 0 success · 2 config error · 3
// runtime failure).
const (
	AuditCLIExitOK      = 0
	AuditCLIExitConfig  = 2
	AuditCLIExitRuntime = 3
)

// RunAuditReplayCLI runs the docsync-audit CLI over args and returns the
// process exit code (TS runAuditReplayCli). Argument parsing is fail-closed:
// an unknown argument or a missing required flag is a config error (2); the
// replay itself failing (bad run id, unreadable state) is a runtime
// failure (3) with the typed *AuditInputError message preserved verbatim.
func RunAuditReplayCLI(args []string, io AuditReplayCLIIo) int {
	stateDir := ""
	runID := ""
	docPath := ""
	docPathSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--state-dir":
			if index+1 < len(args) {
				stateDir = args[index+1]
			}
			index++
		case "--run":
			if index+1 < len(args) {
				runID = args[index+1]
			}
			index++
		case "--doc":
			if index+1 < len(args) {
				docPath = args[index+1]
				docPathSet = true
			}
			index++
		case "--help", "-h":
			io.Stdout(AuditReplayCLIUsage)
			return AuditCLIExitOK
		default:
			io.Stderr("docsync-audit: unknown argument " + arg)
			return AuditCLIExitConfig
		}
	}
	if stateDir == "" || runID == "" {
		io.Stderr("docsync-audit: --state-dir and --run are required")
		return AuditCLIExitConfig
	}
	if _, err := os.Stat(stateDir); err != nil {
		io.Stderr("docsync-audit: state dir does not exist: " + stateDir)
		return AuditCLIExitConfig
	}

	replay := NewAuditReplay(AuditReplayOptions{StateDir: stateDir})
	input := AuditReplayInput{RunID: runID}
	if docPathSet {
		scope := docPath
		input.DocPath = &scope
	}
	trail, err := replay.ReplayRun(input)
	if err != nil {
		message := err.Error()
		var auditInput *AuditInputError
		if errors.As(err, &auditInput) {
			message = auditInput.Error()
		}
		io.Stderr("docsync-audit: replay failed: " + message)
		return AuditCLIExitRuntime
	}
	io.Stdout(RenderAuditTrail(trail))
	return AuditCLIExitOK
}
