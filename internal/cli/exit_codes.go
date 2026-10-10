// Ported from packages/cli/src/exit-codes.ts (P7-T06/T07) — the exit-code
// surface. The single source is internal/orchestrator (the ARCH §12.1 exit
// table); the CLI only re-exports the constants and adds human descriptions for
// the usage text. Never re-encode the numbers here.
//
//	0 — no drift detected.
//	1 — drift detected (candidates found).
//	2 — configuration/manifest error (fail-closed, D8): detected BEFORE any AI
//	    stage — config errors always win over everything else.
//	3 — runtime failure (API/model/verifier error — also fail-closed, D14).
package cli

import "github.com/DUDE616/docSync-go/internal/orchestrator"

// DocsyncExitCode is the closed CLI exit-code union (ARCH §12.1 exit table) —
// aliased to the orchestrator's single source.
type DocsyncExitCode = orchestrator.DocsyncExitCode

// The closed exit-code union (re-exported from the orchestrator; never re-typed).
const (
	ExitOK      = orchestrator.ExitOK
	ExitDrift   = orchestrator.ExitDrift
	ExitConfig  = orchestrator.ExitConfig
	ExitRuntime = orchestrator.ExitRuntime
)

// isConfigClass reports the configuration lane (exit 2, D8): the CLI's own
// CliConfigError OR the orchestrator's structural classification (typed
// pipeline config defects, layout/I/O failures). TS isConfigError + the
// instanceof CliConfigError check folded into one predicate.
func isConfigClass(err error) bool {
	var cfg *CliConfigError
	if asCliConfigError(err, &cfg) {
		return true
	}
	return orchestrator.IsConfigError(err)
}

// isRuntimeClass reports the runtime lane (exit 3, D14): the orchestrator's
// structural classification (pipeline run failures, corrupted state, git exec
// failures). A CliConfigError is never a runtime failure.
func isRuntimeClass(err error) bool {
	var cfg *CliConfigError
	if asCliConfigError(err, &cfg) {
		return false
	}
	return orchestrator.IsRuntimeFailure(err)
}

// DescriptionForExitCode returns the one-line human meaning for an exit code
// (mirrors usageText).
func DescriptionForExitCode(code DocsyncExitCode) string {
	switch code {
	case ExitOK:
		return "no drift detected"
	case ExitDrift:
		return "drift detected (candidates found)"
	case ExitConfig:
		return "configuration/manifest error (fail-closed before any AI stage, D8)"
	case ExitRuntime:
		return "runtime failure (fail-closed, D14)"
	default:
		return "unknown exit code"
	}
}
