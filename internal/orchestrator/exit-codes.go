// Ported from packages/core/src/orchestrator/exit-codes.ts (P7-T06/T07; ARCH
// §12.1 exit table; D8/D14/D10/D2).
//
//   - 0 — no drift detected.
//   - 1 — drift detected (candidates found).
//   - 2 — configuration/manifest error (fail-closed, D8): detected BEFORE any
//     AI stage — config errors always win over everything else.
//   - 3 — runtime failure (API/model/verifier error — also fail-closed, D14).
//
// Strictness (D10): `suggest`-only (advisory) runs exit 0 without `--strict`
// and exit 1 with `--strict`; `review`-family rows (the run refused to guess)
// are promoted the same way. Blocking outcomes are NOT advisory: `auto` (a
// verified auto-eligible candidate — "candidates found") and `blocked`/locked
// rows (the ownership floor, D2) ALWAYS count as drift regardless of strict.
// Unknown coverage alone (D7) is surfaced, never a drift exit —
// driftDetected does not count it.
//
// All helpers are pure; error classification is structural (errors.As over
// the typed error classes this and the neighbouring packages own).

package orchestrator

import (
	"errors"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/layout"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// DocsyncExitCode is the closed CLI exit-code union (ARCH §12.1 exit table).
type DocsyncExitCode int

// The closed exit-code union.
const (
	// ExitOK — no drift detected.
	ExitOK DocsyncExitCode = 0
	// ExitDrift — drift detected (candidates found).
	ExitDrift DocsyncExitCode = 1
	// ExitConfig — configuration/manifest error — fail-closed before any AI
	// stage (D8).
	ExitConfig DocsyncExitCode = 2
	// ExitRuntime — runtime failure — also fail-closed (D14).
	ExitRuntime DocsyncExitCode = 3
)

// IsConfigError reports the configuration lane (exit 2, D8): pipeline config
// defects (EDOCSYNC_PIPELINE_CONFIG), invalid `.docsync/` layout and layout
// I/O failures (the fs adapter could not read `.docsync/` — "assume empty" is
// never an option).
func IsConfigError(err error) bool {
	if IsPipelineConfigError(err) {
		return true
	}
	var layoutErr *layout.DocsyncLayoutError
	if errors.As(err, &layoutErr) {
		return true
	}
	var layoutIoErr *layout.LayoutIoError
	return errors.As(err, &layoutIoErr)
}

// IsRuntimeFailure reports the runtime lane (exit 3, D14): pipeline run
// failures (EDOCSYNC_PIPELINE, excluding the config subclass), corrupted
// state files and the git exec error family (option-injection guards, failed
// subprocesses, patch-id infidelity).
func IsRuntimeFailure(err error) bool {
	if IsPipelineConfigError(err) {
		return false
	}
	if _, ok := AsPipelineError(err); ok {
		return true
	}
	var stateCorrupt *docutil.Error
	if errors.As(err, &stateCorrupt) && stateCorrupt.Code == state.CodeStateCorrupt {
		return true
	}
	var gitErr *gitx.GitExecError
	return errors.As(err, &gitErr)
}

// DriftDetected is the drift predicate: true iff any row's policy outcome is
// NOT `ignore` — i.e. `suggest | auto | blocked | review` (the glyph is not
// `ok`). Unknown coverage alone (D7) never counts: it is not a row.
func DriftDetected(report *schemas.CanonicalReport) bool {
	for i := range report.Rows {
		if report.Rows[i].Outcome != schemas.PipelineOutcomeIgnore {
			return true
		}
	}
	return false
}

// isBlockingDrift reports the blocking outcomes: always drift, with or
// without `--strict` (D2 floor).
func isBlockingDrift(row schemas.CanonicalReportRow) bool {
	return row.Outcome == schemas.PipelineOutcomeAuto || row.Outcome == schemas.PipelineOutcomeBlocked
}

// CheckExitOptions are the call-site flags of ResolveCheckExitCode.
type CheckExitOptions struct {
	ConfigError  bool
	RuntimeError bool
	Strict       bool
}

// ResolveCheckExitCode resolves the `docsync check` exit code. Precedence:
// configuration error → 2 (D8 — before any AI stage, wins even when drift is
// present); runtime failure → 3 (D14); blocking drift (`auto`/`blocked`/
// locked) → 1; strict mode (`--strict`, D10) promotes any warn/review-family
// row to 1 — an advisory-only run is exit 0 without and exit 1 with
// `--strict`; else 0.
func ResolveCheckExitCode(report *schemas.CanonicalReport, opts *CheckExitOptions) DocsyncExitCode {
	if opts != nil && opts.ConfigError {
		return ExitConfig
	}
	if opts != nil && opts.RuntimeError {
		return ExitRuntime
	}
	for i := range report.Rows {
		if isBlockingDrift(report.Rows[i]) {
			return ExitDrift
		}
	}
	if opts != nil && opts.Strict {
		for i := range report.Rows {
			if report.Rows[i].Glyph != schemas.ReportGlyphOk {
				return ExitDrift
			}
		}
	}
	return ExitOK
}
