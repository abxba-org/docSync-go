package memory

// Ported from packages/core/src/memory/layout.ts (P10-T20; ARCH §8.8, CTX §25,
// D1, PLAN §11.6): `.docsync/memory/` layout enforcement. The D1 memory
// layout is EXACTLY FOUR Markdown files — decisions, known-patterns,
// exceptions, failures — each holding appended, machine-parseable
// front-matter records (see records.go).
//
// Fail-closed discipline preserved from the TS module:
//   - EnforceMemoryLayout creates the directory and any MISSING file as an
//     empty scaffold (create-if-missing, V1) and then REPORTS the result;
//   - EXTRA files are REPORTED (D1 exactly-four) and NEVER auto-deleted —
//     deletion is a human decision, not an automation behavior;
//   - every I/O failure is a typed *MemoryLayoutError, never a silent
//     "assume empty" (D8).
//
// The atomic write helper mirrors state/cas.go (temp file in the same
// directory + rename): no reader ever observes a partial memory file.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// MemoryFile is one of the D1 EXACTLY-FOUR memory files, by stem
// (schema-validated value; schemas.MemoryFileValues).
type MemoryFile = schemas.MemoryFile

// MemoryFiles are the D1 EXACTLY-FOUR memory file names on disk — nothing
// else belongs in `memory/` (MEMORY_FILES). Order is the canonical D1 order.
var MemoryFiles = []string{
	"decisions.md",
	"known-patterns.md",
	"exceptions.md",
	"failures.md",
}

// MemoryFileName maps a stem to its on-disk file name inside `.docsync/memory/`.
func MemoryFileName(file MemoryFile) string {
	return string(file) + ".md"
}

// EmptyMemoryFileScaffold is the empty front-matter scaffold written for a
// missing memory file (create-if-missing, V1). It deliberately contains NO
// `---` fence lines, so the record parser sees zero records in a fresh
// scaffold.
func EmptyMemoryFileScaffold(file MemoryFile) string {
	return strings.Join([]string{
		"# docsync memory — " + string(file),
		"",
		"Appended, machine-parseable front-matter records only (D1 exactly-four",
		"layout; PLAN §11.6: Markdown with structured front-matter records).",
		"Evidence fields are mandatory on every record (PRIN-18); contradictions",
		"land in failures.md and decay the contradicted record (R12).",
		"",
	}, "\n")
}

/* --------------------------------------------------------------------- errors */

// EDOCSYNC_MEMORY_LAYOUT is the machine-readable code for
// *MemoryLayoutError (TS EDOCSYNC_MEMORY_LAYOUT). The TS class code was not
// listed in docutil/errors.go; following the layout unit's precedent the TS
// code string is preserved verbatim as the observable identity and carried
// through a docutil coded error.
const EDOCSYNC_MEMORY_LAYOUT = "EDOCSYNC_MEMORY_LAYOUT"

// MemoryLayoutError is the typed, fail-closed I/O error for the memory
// layout (D8). It renders as `EDOCSYNC_MEMORY_LAYOUT: <message>` and wraps
// the underlying fs cause (when any).
type MemoryLayoutError struct {
	inner *docutil.Error
}

// NewMemoryLayoutError builds the error, preserving the TS message text and
// attaching cause when non-nil (Unwrap exposes the whole chain).
func NewMemoryLayoutError(message string, cause error) *MemoryLayoutError {
	inner := docutil.New(EDOCSYNC_MEMORY_LAYOUT, message)
	if cause != nil {
		inner = inner.Wrap(cause)
	}
	return &MemoryLayoutError{inner: inner}
}

// Error renders the coded error (`EDOCSYNC_MEMORY_LAYOUT: <message>`).
func (e *MemoryLayoutError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_MEMORY_LAYOUT.
func (e *MemoryLayoutError) Code() string { return EDOCSYNC_MEMORY_LAYOUT }

// Message returns the bare TS error message.
func (e *MemoryLayoutError) Message() string { return e.inner.Msg }

// Unwrap exposes the docutil coded error (and through it the fs cause), so
// errors.As / docutil.CodeOf keep working.
func (e *MemoryLayoutError) Unwrap() error { return e.inner }

/* --------------------------------------------------------------- atomic write */

// AtomicWriteText writes raw to absPath through a temp file in the target
// directory + rename (mirrors state/cas.go): the rename is atomic on POSIX,
// so a concurrent reader never sees a partial memory file. The temp file is
// cleaned up on failure.
func AtomicWriteText(absPath, raw string) error {
	tmpPath := absPath + ".tmp-" + randomToken()
	if err := os.WriteFile(tmpPath, []byte(raw), 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// randomToken names the temp file; uniqueness only needs to hold for the
// lifetime of one write (same contract as state.randomToken).
func randomToken() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

/* ------------------------------------------------------------------ validation */

// MemoryLayoutReport is the result of ValidateMemoryLayout (D1 exactly-four
// accounting).
type MemoryLayoutReport struct {
	// OK is true iff every expected file is present AND no extra entries exist.
	OK bool
	// Missing lists expected memory files that are missing (created by
	// EnforceMemoryLayout), in canonical D1 order.
	Missing []string
	// Extra lists entries REPORTED but never auto-deleted (D1 exactly-four),
	// sorted. Anything that is not exactly one of the four file names counts,
	// including directories and dotfiles.
	Extra []string
}

// ValidateMemoryLayout is pure accounting over the on-disk directory: which
// of the four expected files are missing, and which entries are extra. Extra
// files are reported, never removed here.
func ValidateMemoryLayout(memoryDir string) (*MemoryLayoutReport, error) {
	entries, err := os.ReadDir(memoryDir)
	if err != nil {
		return nil, NewMemoryLayoutError(
			fmt.Sprintf("failed to list memory directory %q", memoryDir), err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	expected := make(map[string]bool, len(MemoryFiles))
	for _, name := range MemoryFiles {
		expected[name] = true
	}
	present := make(map[string]bool, len(names))
	for _, name := range names {
		present[name] = true
	}

	report := &MemoryLayoutReport{
		Missing: make([]string, 0, len(MemoryFiles)),
		Extra:   make([]string, 0),
	}
	for _, name := range MemoryFiles {
		if !present[name] {
			report.Missing = append(report.Missing, name)
		}
	}
	for _, name := range names {
		if !expected[name] {
			report.Extra = append(report.Extra, name)
		}
	}
	report.OK = len(report.Missing) == 0 && len(report.Extra) == 0
	return report, nil
}

// EnforceMemoryLayout enforces the D1 layout: ensure the directory exists,
// create each missing file as an empty scaffold (atomic write), then return
// the validation report. Extra files are returned in Extra and LEFT ON DISK
// (D1: report, never auto-delete).
func EnforceMemoryLayout(memoryDir string) (*MemoryLayoutReport, error) {
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return nil, NewMemoryLayoutError(
			fmt.Sprintf("failed to create memory directory %q", memoryDir), err)
	}
	initial, err := ValidateMemoryLayout(memoryDir)
	if err != nil {
		return nil, err
	}
	for _, missing := range initial.Missing {
		stem := MemoryFile(strings.TrimSuffix(missing, ".md"))
		scaffold := EmptyMemoryFileScaffold(stem)
		if err := AtomicWriteText(filepath.Join(memoryDir, missing), scaffold); err != nil {
			return nil, NewMemoryLayoutError(
				fmt.Sprintf("failed to scaffold memory file %q", missing), err)
		}
	}
	return ValidateMemoryLayout(memoryDir)
}

// ReadMemoryTextFile reads one memory file's raw text. Fail-closed: a MISSING
// file is a typed layout error, never a silent "empty" (D8) — the caller was
// expected to have enforced the layout first.
func ReadMemoryTextFile(memoryDir string, file MemoryFile) (string, error) {
	name := MemoryFileName(file)
	absPath := filepath.Join(memoryDir, name)
	raw, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", NewMemoryLayoutError(
				fmt.Sprintf("memory file %q missing under %q — run enforceMemoryLayout before reading (fail closed, D8)", name, memoryDir),
				err)
		}
		return "", NewMemoryLayoutError(
			fmt.Sprintf("failed to read memory file %q", name), err)
	}
	return string(raw), nil
}
