// Ported from packages/cli/src/io.ts (P7-T01) — the injected process I/O seam.
// runCli and the verb runners write through a CliIo instead of touching
// os.Stdout directly, so tests capture output and library code never calls
// os.Exit — the exit code is a returned DocsyncExitCode (P7-T06/T07).
//
// Both methods append a single newline per line (TS `process.stdout.write(line +
// "\n")`). The concrete writer-based implementation lets tests capture output
// through an injected io.Writer (bytes.Buffer).
package cli

import (
	"io"
	"os"
)

// CliIo is the injected stdout/stderr seam.
type CliIo interface {
	// Stdout writes one line to standard output (a trailing newline is added).
	Stdout(line string)
	// Stderr writes one line to standard error (a trailing newline is added).
	Stderr(line string)
}

// writerIo is the default CliIo over two writers.
type writerIo struct {
	stdout io.Writer
	stderr io.Writer
}

// NewCliIo builds a CliIo that writes each line (with a trailing newline) to the
// given stdout/stderr writers.
func NewCliIo(stdout, stderr io.Writer) CliIo {
	return &writerIo{stdout: stdout, stderr: stderr}
}

func (w *writerIo) Stdout(line string) {
	_, _ = io.WriteString(w.stdout, line+"\n")
}

func (w *writerIo) Stderr(line string) {
	_, _ = io.WriteString(w.stderr, line+"\n")
}

// DefaultIo returns the real-process writers (os.Stdout / os.Stderr).
func DefaultIo() CliIo {
	return NewCliIo(os.Stdout, os.Stderr)
}
