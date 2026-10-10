package lint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// LintSeverity is always warning (lint never halts the run, D8).
type LintSeverity string

// LintSeverityWarning is the only severity a lint finding carries.
const LintSeverityWarning LintSeverity = "warning"

// LintFindingCode enumerates the warning finding kinds.
type LintFindingCode string

const (
	// LintZeroMatchGlob: a sources glob matches zero repo files today (V-P0-15).
	LintZeroMatchGlob LintFindingCode = "zero_match_glob"
	// LintNonexistentDocPath: the entry's document path is absent from repoFiles.
	LintNonexistentDocPath LintFindingCode = "nonexistent_doc_path"
	// LintDeadEntry: ALL sources are zero-match AND the document path is absent.
	LintDeadEntry LintFindingCode = "dead_entry"
	// LintUnmappedDocDir: a doc file under docs/ absent from every entry path.
	LintUnmappedDocDir LintFindingCode = "unmapped_doc_dir"
)

// LintFinding is one warnings-only lint finding.
type LintFinding struct {
	Code     LintFindingCode `json:"code"`
	Severity LintSeverity    `json:"severity"`
	// Path is the manifest entry path (or, for unmapped_doc_dir, the doc file) flagged.
	Path   string `json:"path"`
	Detail string `json:"detail"`
	// Target is the concrete offending artifact, e.g. the zero-matching glob
	// pattern (empty when there is none — mirrors the TS optional field).
	Target string `json:"target,omitempty"`
}

// LintReport is warnings-only — OK is always true (warnings never halt the run).
type LintReport struct {
	Findings []LintFinding `json:"findings"`
	OK       bool          `json:"ok"`
}

// CodeLintInvalid is the distinguishable error code for lint handed an invalid
// manifest (D8). Preserves the observable TS code EDOCSYNC_LINT_INVALID.
const CodeLintInvalid = "EDOCSYNC_LINT_INVALID"

// ManifestLintError is the typed error returned when the manifest fails schema
// validation before linting. Configuration-error semantics: lint runs on valid
// manifests only; the hard-error validator owns the full diagnostic surface.
type ManifestLintError struct {
	// Issues are the rendered `${path}: ${message}` diagnostics (manifest root = "(manifest)").
	Issues []string
	inner  *docutil.Error
}

func newManifestLintError(message string, issues []string) *ManifestLintError {
	return &ManifestLintError{Issues: issues, inner: docutil.New(CodeLintInvalid, message)}
}

// Error renders the coded error (`EDOCSYNC_LINT_INVALID: <message>`).
func (e *ManifestLintError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_LINT_INVALID.
func (e *ManifestLintError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ManifestLintError) Unwrap() error { return e.inner }

// LintManifestParams mirrors the TS lintManifest params object.
type LintManifestParams struct {
	Manifest schemas.Manifest
	// RepoFiles are all tracked-ish file paths in the repo (POSIX, relative).
	RepoFiles []string
	// DocFiles are doc files found in the repo (POSIX, relative). A nil pointer
	// skips the coverage scan entirely (mirrors TS docFiles === undefined); a
	// pointer to an empty slice scans with no doc files.
	DocFiles *[]string
}

// LintManifest lints a manifest against the repository's current files.
//
// Findings:
//   - zero_match_glob: a sources glob matches zero repo files today (V-P0-15).
//   - nonexistent_doc_path: the entry's document path is absent from repoFiles.
//   - dead_entry: ALL sources are zero-match AND the document path is absent —
//     the entry has zero signal; emitted once per entry (its per-glob
//     zero_match_glob warnings are still emitted).
//   - unmapped_doc_dir: a doc file under docs/ absent from every entry path —
//     unknown coverage per D7. Only computed when DocFiles is provided.
//
// Findings are sorted deterministically by (path, code, target, detail) and OK
// is always true. A hard input (schema-invalid manifest → *ManifestLintError,
// escaping glob → propagated *globs.GlobGuardError) is returned as an error
// rather than a report (fail closed, D8).
func LintManifest(params LintManifestParams) (LintReport, error) {
	if issues := params.Manifest.Validate(); len(issues) > 0 {
		rendered := make([]string, 0, len(issues))
		for _, iss := range issues {
			path := strings.Join(iss.Path, ".")
			if path == "" {
				path = "(manifest)"
			}
			rendered = append(rendered, path+": "+iss.Message)
		}
		message := "manifest failed schema validation — lint runs on valid manifests only; hard errors are the fail-closed validator's job (D8): " +
			strings.Join(rendered, "; ")
		return LintReport{}, newManifestLintError(message, rendered)
	}

	repoFileSet := make(map[string]bool, len(params.RepoFiles))
	for _, f := range params.RepoFiles {
		repoFileSet[f] = true
	}

	findings := []LintFinding{}
	for i := range params.Manifest.Documents {
		entry := &params.Manifest.Documents[i]
		zeroMatchGlobs := []string{}
		for _, glob := range entry.Sources {
			matcher, err := globs.CompileGlob(glob)
			if err != nil {
				// A guard-failing glob throws GlobGuardError (never downgraded).
				return LintReport{}, err
			}
			hasMatch := false
			for _, file := range params.RepoFiles {
				if matcher.Match(file) {
					hasMatch = true
					break
				}
			}
			if !hasMatch {
				zeroMatchGlobs = append(zeroMatchGlobs, glob)
				findings = append(findings, LintFinding{
					Code:     LintZeroMatchGlob,
					Severity: LintSeverityWarning,
					Path:     entry.Path,
					Detail:   fmt.Sprintf(`sources glob "%s" matches zero repository files today (V-P0-15: warning — the run proceeds, annotated)`, glob),
					Target:   glob,
				})
			}
		}
		docPresent := repoFileSet[entry.Path]
		if !docPresent {
			findings = append(findings, LintFinding{
				Code:     LintNonexistentDocPath,
				Severity: LintSeverityWarning,
				Path:     entry.Path,
				Detail:   fmt.Sprintf(`document path "%s" does not exist in the repository today (V-P0-15: warning — the run proceeds, annotated)`, entry.Path),
			})
		}
		if len(zeroMatchGlobs) == len(entry.Sources) && !docPresent {
			findings = append(findings, LintFinding{
				Code:     LintDeadEntry,
				Severity: LintSeverityWarning,
				Path:     entry.Path,
				Detail:   fmt.Sprintf("manifest entry has zero signal: all %d source glob(s) match nothing and the document path does not exist — a removal candidate (V-P0-15: warning — the run proceeds, annotated)", len(zeroMatchGlobs)),
			})
		}
	}

	if params.DocFiles != nil {
		entryPaths := make(map[string]bool, len(params.Manifest.Documents))
		for i := range params.Manifest.Documents {
			entryPaths[params.Manifest.Documents[i].Path] = true
		}
		for _, doc := range *params.DocFiles {
			if entryPaths[doc] {
				continue
			}
			if !strings.HasPrefix(doc, "docs/") {
				continue
			}
			findings = append(findings, LintFinding{
				Code:     LintUnmappedDocDir,
				Severity: LintSeverityWarning,
				Path:     doc,
				Detail:   "documentation under docs/ is absent from the manifest allowlist — unknown coverage (D7): docs outside the manifest are never scanned or patched (V-P0-15: warning — the run proceeds, annotated)",
			})
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		return findingLess(findings[i], findings[j])
	})
	return LintReport{Findings: findings, OK: true}, nil
}

// findingLess orders findings by (path, code, target, detail), mirroring the TS
// compareFindings chain (target/empty as "").
func findingLess(a, b LintFinding) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	if a.Target != b.Target {
		return a.Target < b.Target
	}
	return a.Detail < b.Detail
}
