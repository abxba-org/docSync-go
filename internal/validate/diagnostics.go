package validate

import (
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/layout"
)

// DiagnosticSeverity is 'error' (hard, halts the run — D8) or 'warning'
// (annotates, never halts — V-P0-15).
type DiagnosticSeverity string

// Severities (TS union, values preserved verbatim).
const (
	SeverityError   DiagnosticSeverity = "error"
	SeverityWarning DiagnosticSeverity = "warning"
)

// Diagnostic is one line-precise configuration diagnostic, rendered as
// `file:line:col: [CODE] message — hint`. Line and Column are 1-based; 0
// means "no known position" (file-level), which renders omitted and sorts
// first within its file — the TS optional-field semantics.
type Diagnostic struct {
	Severity DiagnosticSeverity `json:"severity"`
	Code     string             `json:"code"`
	// File is a repo-relative file (or a pseudo-file such as `environment`).
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// ValidationErrorCode is the complete, enumerable code set exported for the
// Phase 7 CLI and the docs wave.
type ValidationErrorCode = string

// VALIDATION_ERROR_CODES lists the hard-error codes, grouped by stage:
// layout → manifest → globs → environment (values preserved verbatim).
var VALIDATION_ERROR_CODES = []string{
	"E_LAYOUT_MISPLACED_FILE",
	"E_LAYOUT_UNKNOWN_ENTRY",
	"E_LAYOUT_MEMORY_EXTRA_FILE",
	"E_LAYOUT_STATE_UNKNOWN_ENTRY",
	"E_LAYOUT_MISSING_REQUIRED_FILE",
	"E_LAYOUT_MISSING_DOCSYNC_DIR",
	"E_LAYOUT_DIR_EXPECTED",
	"E_LAYOUT_FILE_EXPECTED",
	"E_LAYOUT_IO",
	"E_MANIFEST_MISSING",
	"E_MANIFEST_IO",
	"E_MANIFEST_YAML",
	"E_MANIFEST_SCHEMA",
	"E_MANIFEST_UNSUPPORTED_VERSION",
	"E_MANIFEST_UNKNOWN_MODE",
	"E_MANIFEST_MISSING_PATH",
	"E_MANIFEST_DUPLICATE_PATH",
	"E_MANIFEST_UNKNOWN_TRIGGER",
	"E_MANIFEST_FLOOR_CONFLICT",
	// P12-T13: a PRESENT but invalid `.docsync/org-policy.yml` — a hard error
	// (exit 2), never a silent drop that would un-apply the org's floors.
	"E_MANIFEST_ORG_POLICY_INVALID",
	"E_GLOB_ESCAPE",
	"E_GLOB_DOCSYNC_INTERNAL",
	"E_GLOB_WORKFLOWS",
	"E_GLOB_ABSOLUTE",
	"E_ENV_POLICY",
}

// VALIDATION_WARNING_CODES lists the warning codes — annotate, never halt.
var VALIDATION_WARNING_CODES = []string{
	"W_ZERO_MATCH_GLOB",
	"W_NONEXISTENT_DOC_PATH",
	"W_COVERAGE_LINT",
}

// LAYOUT_VIOLATION_CODES maps the layout violation kinds (P0-T03) onto the
// diagnostic taxonomy. Every kind is a hard configuration error (D8).
var LAYOUT_VIOLATION_CODES = map[layout.LayoutViolationKind]string{
	layout.ViolationMisplacedFile:       "E_LAYOUT_MISPLACED_FILE",
	layout.ViolationUnknownEntry:        "E_LAYOUT_UNKNOWN_ENTRY",
	layout.ViolationMemoryExtraFile:     "E_LAYOUT_MEMORY_EXTRA_FILE",
	layout.ViolationStateUnknownEntry:   "E_LAYOUT_STATE_UNKNOWN_ENTRY",
	layout.ViolationMissingRequiredFile: "E_LAYOUT_MISSING_REQUIRED_FILE",
	layout.ViolationMissingDocsyncDir:   "E_LAYOUT_MISSING_DOCSYNC_DIR",
	layout.ViolationDirExpected:         "E_LAYOUT_DIR_EXPECTED",
	layout.ViolationFileExpected:        "E_LAYOUT_FILE_EXPECTED",
}

// RenderDiagnostic renders one diagnostic:
// `file:line:col: [CODE] message — hint`, dropping the positional parts
// (and the trailing separators) that are absent.
func RenderDiagnostic(d Diagnostic) string {
	location := d.File
	if d.Line != 0 {
		location += ":" + strconv.Itoa(d.Line)
		if d.Column != 0 {
			location += ":" + strconv.Itoa(d.Column)
		}
	}
	text := location + ": [" + d.Code + "] " + d.Message
	if d.Hint != "" {
		text += " — " + d.Hint
	}
	return text
}

// CompareDiagnostics is the deterministic total order for diagnostics:
// (file, line, column, code, message); absent positions sort first within
// their file (file-level diagnostics lead). Used to order both errors and
// warnings.
func CompareDiagnostics(a, b Diagnostic) int {
	if a.File != b.File {
		return strings.Compare(a.File, b.File)
	}
	if a.Line != b.Line {
		return a.Line - b.Line
	}
	if a.Column != b.Column {
		return a.Column - b.Column
	}
	if a.Code != b.Code {
		return strings.Compare(a.Code, b.Code)
	}
	return strings.Compare(a.Message, b.Message)
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		return CompareDiagnostics(diagnostics[i], diagnostics[j]) < 0
	})
}
