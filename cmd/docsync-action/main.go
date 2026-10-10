// Command docsync-action is the GitHub Action entrypoint: a thin main that
// calls action.RunMain over the real process env / fs / io and applies the
// resulting CLI exit code via os.Exit (the entry exception that turns the
// runner's exit-code table into the workflow step outcome).
//
// PORT seam (internal/cli): the default CheckRunPort composition (the pipeline
// runtime + check-run REST) is bound through MainDeps.NewDefaultPort once
// internal/cli lands (TODO(docsync-action/cli)). Until then an unbound port
// fails CLOSED — the run reports a configuration error (exit 2), never a false
// "no drift" pass (D8/D14).
package main

import (
	"os"

	"github.com/DUDE616/docSync-go/internal/action"
)

func main() {
	// Env (INPUT_*/GITHUB_*), fs, and io default to the real process (OSEnv /
	// OSFS / OSIO). NewDefaultPort stays unbound until the internal/cli
	// composition wave lands.
	result := action.RunMain(action.MainDeps{})
	os.Exit(int(result.ExitCode))
}
