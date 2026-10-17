// Command docsync is the Go port of the docSync CLI (TS packages/cli/src/bin.ts).
//
// It is a thin wrapper: all logic (argument parsing, verb dispatch, exit-code
// mapping) lives in the composable library internal/cli, so the behavior is
// testable and reusable. This binary only maps the returned exit code onto the
// process exit (programs should return codes, not panic/exit in libraries).
package main

import (
	"os"

	"github.com/DUDE616/docSync-go/internal/cli"
)

// osExit is the process-exit seam; main() routes through it so the entrypoint
// logic can be exercised in-process without terminating the test binary.
var osExit = os.Exit

// run executes the CLI entrypoint logic and returns the process exit code.
// main() exists only to translate that into os.Exit so the logic can be tested
// in-process without terminating the test binary.
//
// NOTE: the os.Getwd failure branch (exit 3) is not reachable in-process on
// Linux (getcwd reports the cwd path even when it has been unlinked), so that
// single branch remains uncovered by in-process tests.
func run(argv []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		return int(cli.ExitRuntime)
	}
	return int(cli.RunCli(argv, cli.RunCliOptions{Io: cli.DefaultIo(), Cwd: cwd}))
}

func main() {
	osExit(run(os.Args[1:]))
}
