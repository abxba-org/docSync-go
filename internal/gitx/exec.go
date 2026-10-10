package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// EDOCSYNCGitExec mirrors the TypeScript `EDOCSYNC_GIT_EXEC` constant.
// docutil exposes exactly one git code, and CONVENTIONS.md ("Errors") forbids
// inventing a new one, so this alias points at docutil.CodeGit.
const EDOCSYNCGitExec = docutil.CodeGit

// GitExecErrorKind is the failure vocabulary in routing-relevant terms:
//   - KindMissingRev: a resolvable object/path does not exist (missing blob at
//     a rev, missing rev:path, unreadable object).
//   - KindUnknownRef: a ref/revision NAME cannot be resolved (bad rev, typo).
//   - KindExecFailure: git ran and failed for any other reason (or could not
//     be spawned at all — a missing binary is an exec failure, not "no changes").
//   - KindTimeout: the invocation was killed by the executor's timeout.
//   - KindInvalidArg: option-injection guard rejected an argument pre-exec.
type GitExecErrorKind string

const (
	KindMissingRev  GitExecErrorKind = "missing-rev"
	KindUnknownRef  GitExecErrorKind = "unknown-ref"
	KindExecFailure GitExecErrorKind = "exec-failure"
	KindTimeout     GitExecErrorKind = "timeout"
	KindInvalidArg  GitExecErrorKind = "invalid-arg"
)

// GitExecError is the typed error every git access failure becomes (D14
// groundwork, V-P1-10): a docutil coded error whose message text is identical
// to the TypeScript GitExecError's `message`, plus the kind, the failing
// subcommand and a bounded stderr excerpt.
type GitExecError struct {
	Kind GitExecErrorKind
	// Command is the git subcommand that failed (e.g. "rev-parse", "diff").
	Command string
	// StderrExcerpt is the parsed stderr excerpt (never a full dump).
	StderrExcerpt string
	// Msg is the exact message text of the TypeScript error.
	Msg string
	// Cause is the underlying failure, when there is one.
	Cause error

	coded *docutil.Error
}

// Code reports the docutil error code carried by this error.
func (e *GitExecError) Code() string { return e.coded.Code }

// Error renders the coded error in docutil house style ("EDOCSYNC_GIT: <msg>").
func (e *GitExecError) Error() string { return e.coded.Error() }

// Unwrap exposes both the coded error (so docutil.CodeOf resolves it) and the
// underlying cause.
func (e *GitExecError) Unwrap() []error {
	if e.Cause == nil {
		return []error{e.coded}
	}
	return []error{e.coded, e.Cause}
}

// newGitExecError builds a typed git failure.
func newGitExecError(kind GitExecErrorKind, msg, command, stderrExcerpt string, cause error) *GitExecError {
	coded := docutil.New(docutil.CodeGit, msg)
	if cause != nil {
		coded = coded.Wrap(cause)
	}
	return &GitExecError{
		Kind:          kind,
		Command:       command,
		StderrExcerpt: stderrExcerpt,
		Msg:           msg,
		Cause:         cause,
		coded:         coded,
	}
}

// GitExecErrorOptions carries the optional context of a git failure.
type GitExecErrorOptions struct {
	Command       string
	StderrExcerpt string
	Cause         error
}

// NewGitExecError builds a typed git failure from options (command defaults to
// "git" when unset, mirroring the TS constructor default).
func NewGitExecError(kind GitExecErrorKind, msg string, opts *GitExecErrorOptions) *GitExecError {
	command := "git"
	excerpt := ""
	var cause error
	if opts != nil {
		if opts.Command != "" {
			command = opts.Command
		}
		excerpt = opts.StderrExcerpt
		cause = opts.Cause
	}
	return newGitExecError(kind, msg, command, excerpt, cause)
}

// AsGitExecError extracts a typed git failure from err.
func AsGitExecError(err error) (*GitExecError, bool) {
	var e *GitExecError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// GitRunResult is the raw report of one git invocation. A non-zero exit is a
// VALUE here, never a thrown error: classification happens in exactly one
// place (ClassifyGitFailure) so callers can never mistake a broken run for
// "no changes".
type GitRunResult struct {
	// ExitCode is nil when the process was killed by a signal or never spawned.
	ExitCode *int
	// Signal is the fatal signal name, or "" when none.
	Signal string
	// TimedOut is true when the executor's timeout killed the process.
	TimedOut bool
	// Stdout / Stderr are binary-safe captures (decoded to text only at API
	// boundaries).
	Stdout []byte
	Stderr []byte
}

// Failed reports whether the run did not exit cleanly.
func (r GitRunResult) Failed() bool { return r.ExitCode == nil || *r.ExitCode != 0 }

// StdoutText decodes captured stdout as UTF-8 (API boundary only).
func (r GitRunResult) StdoutText() string { return string(r.Stdout) }

// StderrText decodes captured stderr as UTF-8 (API boundary only).
func (r GitRunResult) StderrText() string { return string(r.Stderr) }

// GitRunOptions carries per-invocation executor settings.
type GitRunOptions struct {
	// TimeoutMs kills the git process after this many ms (classified as
	// timeout). Zero means "use the executor default".
	TimeoutMs int
	// Stdin is an optional stdin payload (used for `git patch-id` piping).
	// Nil means no stdin; an empty non-nil slice still routes through the
	// stdin variant, mirroring the TypeScript `stdin !== undefined` check.
	Stdin []byte
}

// GitExecutor is the injection seam (V-P1-10): tests substitute executors that
// return scripted failures, and the client must treat them exactly like real
// git failures. The executor receives the FULL argv (including `-C <root>`)
// WITHOUT the leading `git` binary name and MUST NOT use a shell.
//
// Errors are reserved for executor seams (e.g. an exhausted script); git
// failures are reported inside GitRunResult.
type GitExecutor interface {
	Run(ctx context.Context, args []string, opts *GitRunOptions) (GitRunResult, error)
}

// SystemGitExecutorOptions tunes the default executor.
type SystemGitExecutorOptions struct {
	// MaxBufferBytes caps captured stdout/stderr (default 64 MiB).
	MaxBufferBytes int
	// DefaultTimeoutMs is the default per-invocation timeout (default 60s).
	DefaultTimeoutMs int
}

const (
	// DefaultMaxBufferBytes is the TS DEFAULT_MAX_BUFFER_BYTES.
	DefaultMaxBufferBytes = 64 * 1024 * 1024
	// DefaultTimeoutMs is the TS DEFAULT_TIMEOUT_MS.
	DefaultTimeoutMs = 60_000
	// gitWaitDelay bounds how long Wait may block on I/O copying after the
	// process was signaled (an abandoned subprocess may hold the pipes open).
	gitWaitDelay = 500 * time.Millisecond
)

// errGitTimeout is the cause used to mark a self-inflicted timeout.
var errGitTimeout = errors.New("gitx: invocation timed out")

// SystemGitExecutor runs the system `git` binary via os/exec (args array, no
// shell). Non-zero exits and spawn failures are reported as GitRunResult
// values (never thrown), so classification stays centralized.
type SystemGitExecutor struct {
	maxBufferBytes   int
	defaultTimeoutMs int
}

// NewSystemGitExecutor builds the default executor (nil opts = defaults).
func NewSystemGitExecutor(opts *SystemGitExecutorOptions) *SystemGitExecutor {
	maxBuffer := DefaultMaxBufferBytes
	timeout := DefaultTimeoutMs
	if opts != nil {
		if opts.MaxBufferBytes > 0 {
			maxBuffer = opts.MaxBufferBytes
		}
		if opts.DefaultTimeoutMs > 0 {
			timeout = opts.DefaultTimeoutMs
		}
	}
	return &SystemGitExecutor{maxBufferBytes: maxBuffer, defaultTimeoutMs: timeout}
}

// CreateSystemGitExecutor is the TS-parity factory for the default executor.
func CreateSystemGitExecutor(opts *SystemGitExecutorOptions) *SystemGitExecutor {
	return NewSystemGitExecutor(opts)
}

// Run executes `git <args>` with the repository root already spliced into the
// argv by the caller. stdout/stderr are captured as binary-safe buffers; a
// timeout kills the process with SIGTERM (escalating to SIGKILL) and is
// reported as TimedOut.
func (e *SystemGitExecutor) Run(ctx context.Context, args []string, opts *GitRunOptions) (GitRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := e.defaultTimeoutMs
	var stdin []byte
	if opts != nil {
		if opts.TimeoutMs > 0 {
			timeout = opts.TimeoutMs
		}
		stdin = opts.Stdin
	}
	if timeout <= 0 {
		timeout = DefaultTimeoutMs
	}

	runCtx, cancel := context.WithTimeoutCause(ctx, time.Duration(timeout)*time.Millisecond, errGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Env = isolatedEnv()
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(interruptSignal())
	}
	cmd.WaitDelay = gitWaitDelay

	stdout := &cappedBuffer{max: e.maxBufferBytes}
	stderr := &cappedBuffer{max: e.maxBufferBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	err := cmd.Run()
	timedOut := runCtx.Err() != nil && errors.Is(context.Cause(runCtx), errGitTimeout)

	stdoutBytes, stdoutOverflow := stdout.bytes()
	stderrBytes, stderrOverflow := stderr.bytes()
	if stdoutOverflow || stderrOverflow {
		// execFile's maxBuffer error is reported as a never-clean result with
		// the failure detail in stderr (D14: never "no changes").
		which := "stdout"
		if stderrOverflow && !stdoutOverflow {
			which = "stderr"
		}
		if len(stderrBytes) == 0 {
			stderrBytes = []byte(fmt.Sprintf("git exec failure: %s maxBuffer length exceeded", which))
		}
		return GitRunResult{
			ExitCode: nil,
			Signal:   "",
			TimedOut: timedOut,
			Stdout:   stdoutBytes,
			Stderr:   stderrBytes,
		}, nil
	}

	if err == nil {
		zero := 0
		return GitRunResult{ExitCode: &zero, Signal: "", TimedOut: false, Stdout: stdoutBytes, Stderr: stderrBytes}, nil
	}

	// Never spawned, signaled, or killed: exit code is null, the captured
	// stderr (or the failure detail) is preserved for classification.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if state := exitErr.ProcessState; state != nil {
			if sig, signaled := terminatedBy(state); signaled {
				return GitRunResult{
					ExitCode: nil,
					Signal:   sig,
					TimedOut: timedOut,
					Stdout:   stdoutBytes,
					Stderr:   stderrBytes,
				}, nil
			}
			code := state.ExitCode()
			return GitRunResult{ExitCode: &code, Signal: "", TimedOut: timedOut, Stdout: stdoutBytes, Stderr: stderrBytes}, nil
		}
	}

	detail := err.Error()
	if len(stderrBytes) == 0 {
		stderrBytes = []byte(fmt.Sprintf("git exec failure: %s", detail))
	}
	return GitRunResult{
		ExitCode: nil,
		Signal:   "",
		TimedOut: timedOut,
		Stdout:   stdoutBytes,
		Stderr:   stderrBytes,
	}, nil
}

/* ------------------------------------------------------------------ helpers */

// cappedBuffer keeps the first max bytes of a stream and flags overflow, so an
// unbounded `git log` cannot exhaust the process.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.max - c.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			c.buf.Write(p[:remaining])
			c.overflow = true
			return len(p), nil
		}
		c.buf.Write(p)
	} else if len(p) > 0 {
		c.overflow = true
	}
	return len(p), nil
}

func (c *cappedBuffer) bytes() ([]byte, bool) { return c.buf.Bytes(), c.overflow }

// isolatedEnv is the parent env minus vars that could hijack `-C` to another
// repository.
var hijackEnvVars = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"}

func isolatedEnv() []string {
	environ := os.Environ()
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		hijacked := false
		for _, bad := range hijackEnvVars {
			if key == bad {
				hijacked = true
				break
			}
		}
		if !hijacked {
			out = append(out, entry)
		}
	}
	return out
}

// interruptSignal is the graceful kill used on timeout (TS used SIGTERM).
func interruptSignal() os.Signal {
	if runtime.GOOS == "windows" {
		return os.Interrupt
	}
	return syscall.SIGTERM
}

// terminatedBy reports the fatal signal name of a signaled process.
func terminatedBy(state *os.ProcessState) (string, bool) {
	if state == nil || runtime.GOOS == "windows" {
		return "", false
	}
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return "", false
	}
	return signalName(ws.Signal()), true
}

// signalName renders a signal in the TS `NodeJS.Signals` vocabulary.
func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	default:
		return sig.String()
	}
}

/* ------------------------------------------------------------------- errors */

// ValidateRefArg is the option-injection guard (pre-exec, BEFORE the binary is
// ever invoked): it rejects empty values, values starting with `-`, and values
// containing NUL/newline/carriage-return. Returns the value unchanged.
func ValidateRefArg(value, label string) (string, error) {
	return validateGitArg(value, label)
}

// ValidatePathArg is the same guard, spelled for path arguments (conservative:
// leading `-` rejected too).
func ValidatePathArg(value, label string) (string, error) {
	return validateGitArg(value, label)
}

func validateGitArg(value, label string) (string, error) {
	invalid := func(reason string) error {
		return NewGitExecError(KindInvalidArg, fmt.Sprintf("git %s rejected: %s", label, reason), &GitExecErrorOptions{
			Command:       label,
			StderrExcerpt: tsJSONString(firstRunes(value, 64)),
		})
	}
	if len(value) == 0 {
		return "", invalid("empty argument")
	}
	if strings.HasPrefix(value, "-") {
		return "", invalid(`option-like argument (leading "-")`)
	}
	if strings.Contains(value, "\x00") {
		return "", invalid("NUL byte")
	}
	if strings.ContainsAny(value, "\n\r") {
		return "", invalid("newline byte")
	}
	return value, nil
}

// ValidateRepoRoot validates a repository root for `-C <absRoot>` (absolute
// path, injectable-arg safe). Returns the value unchanged.
func ValidateRepoRoot(repoRoot string) (string, error) {
	if !strings.HasPrefix(repoRoot, "/") {
		return "", NewGitExecError(KindInvalidArg, "repoRoot must be an absolute path", &GitExecErrorOptions{
			Command:       "git",
			StderrExcerpt: tsJSONString(firstRunes(repoRoot, 64)),
		})
	}
	return validateGitArg(repoRoot, "repoRoot")
}

// StderrExcerpt renders a human-bounded stderr excerpt for error records.
func StderrExcerpt(stderr []byte, limit int) string {
	if len(stderr) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = 400
	}
	text := strings.Join(strings.Fields(string(stderr)), " ")
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}

// stderrExcerpt is StderrExcerpt with the TS default limit (400).
func stderrExcerpt(stderr []byte) string { return StderrExcerpt(stderr, 400) }

// ClassifyGitFailure classifies a failed run into a *GitExecError. Deterministic
// stderr pattern matching, first match wins; a per-call kindHint overrides for
// commands whose semantics are unambiguous. A timeout or a never-spawned
// process always wins over patterns. Unknown stderr → exec-failure (fail closed).
func ClassifyGitFailure(command string, result GitRunResult, kindHint ...GitExecErrorKind) *GitExecError {
	excerpt := stderrExcerpt(result.Stderr)
	excerptLower := strings.ToLower(excerpt)
	inference := fmt.Sprintf("exit=%s", formatExitCode(result.ExitCode))
	if result.TimedOut {
		inference = "timeout"
	}
	msg := fmt.Sprintf("git %s failed (kind inference: %s): %s", command, inference, excerptOrNone(excerpt))

	kind := KindExecFailure
	switch {
	case result.TimedOut:
		kind = KindTimeout
	case len(kindHint) > 0 && kindHint[0] != "":
		kind = kindHint[0]
	case matchesAny(excerptLower, unknownRefPatterns):
		kind = KindUnknownRef
	case matchesAny(excerptLower, missingRevPatterns):
		kind = KindMissingRev
	}
	return newGitExecError(kind, msg, command, excerpt, gitRunResultCause(result))
}

// gitRunResultCause exposes the raw run as the error cause, exactly like the TS
// `cause: result` option.
func gitRunResultCause(result GitRunResult) error {
	return &gitRunFailure{result: result}
}

type gitRunFailure struct{ result GitRunResult }

func (f *gitRunFailure) Error() string {
	return fmt.Sprintf("git run failed (kind inference: %s)", formatExitCode(f.result.ExitCode))
}

func formatExitCode(code *int) string {
	if code == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *code)
}

func excerptOrNone(excerpt string) string {
	if excerpt == "" {
		return "no stderr"
	}
	return excerpt
}

func matchesAny(excerptLower string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(excerptLower, pattern) {
			return true
		}
	}
	return false
}

var unknownRefPatterns = []string{
	"unknown revision",
	"bad revision",
	"needed a single revision",
	"ambiguous argument",
	"unknown ref",
	"not a valid object name",
}

var missingRevPatterns = []string{
	"does not exist in",
	"did not match any file",
	"no such path",
}

// firstRunes slices s to at most n runes (UTF-16 code units in the TS original).
func firstRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n])
	}
	return s
}

// tsJSONString reproduces `JSON.stringify` semantics for the strings and string
// arrays used in parser error messages (raw non-ASCII, \b\f\n\r\t escapes,
// \u00xx for the remaining control characters).
func tsJSONString(s string) string {
	var b strings.Builder
	writeJSONString(&b, s)
	return b.String()
}

func tsJSONStringArray(values []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, v)
	}
	b.WriteByte(']')
	return b.String()
}

func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

/* ------------------------------------------------- determinism flag plumbing */

// GITQuotePathArgs is the global determinism flag set on EVERY invocation: it
// disables C-style octal quoting of non-ASCII paths in raw/diff/numstat output
// so parsers see the real bytes.
var GITQuotePathArgs = []string{"-c", "core.quotepath=false"}

// GITDiffConfigPins neutralizes per-machine `diff.*` settings for
// diff-producing commands ONLY. Later `-c` args win over earlier ones, which is
// exactly how the pins are proven.
var GITDiffConfigPins = []string{
	"-c", "diff.noprefix=false",
	"-c", "diff.mnemonicPrefix=false",
	"-c", "diff.algorithm=myers",
	"-c", "diff.indentHeuristic=true",
	"-c", "diff.context=3",
	"-c", "diff.interHunkContext=0",
	"-c", "diff.srcPrefix=a/",
	"-c", "diff.dstPrefix=b/",
}

// GITDiffOutputArgs are the fixed determinism flags for any textual diff.
var GITDiffOutputArgs = []string{"--no-color", "--no-ext-diff"}
