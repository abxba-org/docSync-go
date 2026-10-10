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

func main() {
	os.Exit(int(cli.RunCli(os.Args[1:], cli.RunCliOptions{Io: cli.DefaultIo()})))
}
