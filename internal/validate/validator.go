package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/DUDE616/docSync-go/internal/layout"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AiStageProbe is a probe over hypothetical AI-stage invocations. The
// validator only READS it; a real AI stage would record into it — so a
// count > 0 after validation is a contract violation (V-P0-8).
type AiStageProbe interface {
	Invoked() int
}

// ValidateOptions configures ValidateRepositoryConfig.
type ValidateOptions struct {
	// Floors are org floors (from policy/config), checked per manifest entry.
	Floors []schemas.OrgFloor
	// ModelStub is a no-op model stub proving zero AI invocations (V-P0-8).
	ModelStub AiStageProbe
}

// EffectiveModeSummary is one entry's resolved configuration (mode
// resolution semantics: entry mode → defaults → built-in "suggest"; org
// floors are hard errors, never clamps — V-P0-6).
type EffectiveModeSummary struct {
	Path           string          `json:"path"`
	Mode           schemas.DocMode `json:"mode"`
	PatchCandidate bool            `json:"patchCandidate"`
	AutoApply      bool            `json:"autoApply"`
}

// ValidationOutcome is the full validator result. Hard errors short-circuit
// only the stages that depend on them; otherwise ALL error classes are
// collected into one full diagnostic list (never first-error). Warnings
// annotate, never halt (V-P0-15).
type ValidationOutcome struct {
	// OK is false iff any hard error — the run must not proceed (D8).
	OK bool
	// Errors holds every hard diagnostic, deterministically ordered.
	Errors []Diagnostic
	// Warnings holds every warning diagnostic, deterministically ordered.
	Warnings []Diagnostic
	// Manifest is the parsed manifest on a fully valid configuration, else nil.
	Manifest *schemas.Manifest
	// EffectiveModes is the per-entry effective configuration; nil when the
	// manifest failed to parse or a floor conflict was found (a violated
	// floor is an error, never a clamp — V-P0-6).
	EffectiveModes []EffectiveModeSummary
	// LearningModeDefault is the resolved org learning-mode default (P12-T13;
	// OD-R30): the loaded org policy's learning.defaultMode when declared,
	// else the built-in 'propose'. It never changes a mode, reportOnly
	// (invocation-flags only) or learningMode (V1 hardcoded 'propose').
	LearningModeDefault schemas.LearningMode
	// Advisories are report-only advisories from org policy floors
	// (non-blocking, V-P12-12).
	Advisories []FloorCheckAdvisory
	// AIStageInvocations is always 0 for this validator; asserted via the
	// injected probe (V-P0-8).
	AIStageInvocations int
	// Rendered holds the human-rendered diagnostics, deterministic order
	// (errors, then warnings).
	Rendered []string
}

// ExitCode maps the outcome onto the config-error exit-code table (D8):
// 2 when any hard error is present (fail-closed BEFORE any AI stage), 0
// otherwise. Drift detection (exit 1) and runtime failures (exit 3) are
// owned by later stages, never by this gate.
func (o ValidationOutcome) ExitCode() int {
	if !o.OK {
		return 2
	}
	return 0
}

// layoutViolationFile normalizes a layout violation path
// (`.docsync/`-relative by the layout adapter's contract) onto the
// repo-rooted file reference every diagnostic stage uses. Already-rooted
// paths (`.docsync` itself) pass through.
func layoutViolationFile(path string) string {
	if path == schemas.DocSyncDir || strings.HasPrefix(path, schemas.DocSyncDir+"/") {
		return path
	}
	return schemas.DocSyncDir + "/" + path
}

// ValidateRepositoryConfig validates a repository's docSync configuration,
// fail-closed (D8). Never throws for configuration problems — everything
// becomes diagnostics. Runtime surprises (fs read failures the loader
// refuses to tolerate) propagate: a failed run must never look like a valid
// one.
func ValidateRepositoryConfig(repoRoot string, options ValidateOptions) (ValidationOutcome, error) {
	var errorsList []Diagnostic
	var warningsList []Diagnostic

	// Stage 1 — canonical .docsync/ layout (D1; V-P0-3).
	layoutResult, err := layout.LoadDocsyncLayout(repoRoot)
	if err != nil {
		var ioErr *layout.LayoutIoError
		if errors.As(err, &ioErr) {
			errorsList = append(errorsList, Diagnostic{
				Severity: SeverityError,
				Code:     "E_LAYOUT_IO",
				File:     schemas.DocSyncDir,
				Message:  ioErr.Message(),
			})
		} else {
			return ValidationOutcome{}, err
		}
	}
	for _, violation := range layoutResult.Violations {
		code, ok := LAYOUT_VIOLATION_CODES[violation.Kind]
		if !ok {
			// Fail closed on an unmapped violation kind — never drop it.
			code = "E_LAYOUT_UNKNOWN_ENTRY"
		}
		errorsList = append(errorsList, Diagnostic{
			Severity: SeverityError,
			Code:     code,
			// The layout adapter reports .docsync/-relative paths; diagnostics
			// are repo-rooted like every other stage's file references.
			File:    layoutViolationFile(violation.Path),
			Message: violation.Message,
			Hint:    "expected " + violation.Expected,
		})
	}

	// Stage 2 — manifest content (schema/semantics/floors). Runs even when
	// the layout reported violations, so the caller gets the full error list
	// in one pass; a missing manifest yields its own precise diagnostic
	// inside LoadAndValidateManifest.
	manifestResult, err := LoadAndValidateManifest(repoRoot, ManifestValidateOptions{
		Floors: options.Floors,
	})
	if err != nil {
		return ValidationOutcome{}, err
	}
	for _, diagnostic := range manifestResult.Diagnostics {
		if diagnostic.Severity == SeverityError {
			errorsList = append(errorsList, diagnostic)
		} else {
			warningsList = append(warningsList, diagnostic)
		}
	}
	manifest := manifestResult.Manifest

	// Effective configuration per entry (mode resolution semantics) — only
	// when the manifest is valid AND floors are clean (a violated floor is
	// an error, never a clamp; V-P0-6).
	var effectiveModes []EffectiveModeSummary
	floorConflict := false
	for _, diagnostic := range errorsList {
		if diagnostic.Code == "E_MANIFEST_FLOOR_CONFLICT" {
			floorConflict = true
			break
		}
	}
	if manifest != nil && !floorConflict {
		for _, entry := range manifest.Documents {
			mode := ResolveEntryMode(entry, manifest.Defaults)
			effectiveModes = append(effectiveModes, EffectiveModeSummary{
				Path:           entry.Path,
				Mode:           mode,
				PatchCandidate: IsPatchCandidateMode(mode),
				// The V1 posture is report-only (D10): even "auto" cannot
				// auto-apply without an explicit, reviewed flags override,
				// and no such override exists in this gate.
				AutoApply: false,
			})
		}
	}

	// Deterministic order: errors first (file, line, column, code, message),
	// then warnings — rendered once, stable across runs.
	sortDiagnostics(errorsList)
	sortDiagnostics(warningsList)
	rendered := make([]string, 0, len(errorsList)+len(warningsList))
	for _, diagnostic := range errorsList {
		rendered = append(rendered, RenderDiagnostic(diagnostic))
	}
	for _, diagnostic := range warningsList {
		rendered = append(rendered, RenderDiagnostic(diagnostic))
	}

	// Structural D8 proof: no AI stage exists; the probe (if provided) must
	// have observed zero invocations.
	aiStageInvocations := 0
	if options.ModelStub != nil {
		aiStageInvocations = options.ModelStub.Invoked()
	}

	// The org learning default, resolved exactly as the precedence engine
	// resolves it (org policy learning.defaultMode, else built-in 'propose').
	learningModeDefault := schemas.LearningModePropose
	if manifestResult.OrgPolicy != nil && manifestResult.OrgPolicy.Learning != nil &&
		manifestResult.OrgPolicy.Learning.DefaultMode != nil {
		learningModeDefault = *manifestResult.OrgPolicy.Learning.DefaultMode
	}

	return ValidationOutcome{
		OK:                  len(errorsList) == 0,
		Errors:              errorsList,
		Warnings:            warningsList,
		Manifest:            manifest,
		EffectiveModes:      effectiveModes,
		LearningModeDefault: learningModeDefault,
		Advisories:          manifestResult.Advisories,
		AIStageInvocations:  aiStageInvocations,
		Rendered:            rendered,
	}, nil
}

// ------------------------------------------------------- fs + yaml helpers

// isNotExist reports an ENOENT-style absence.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// isNotExistOrNotDir reports ENOENT or ENOTDIR — both mean "no manifest at
// this location" (a path component is a file).
func isNotExistOrNotDir(err error) bool {
	return isNotExist(err) || errors.Is(err, syscall.ENOTDIR)
}

// firstSemicolonPart mirrors the TS `message.split(';')[0]` extraction: the
// first ';'-separated part of a yaml error message.
func firstSemicolonPart(message string) string {
	if idx := strings.Index(message, ";"); idx >= 0 {
		return message[:idx]
	}
	return message
}

// yamlLinePattern matches yaml.v3's `yaml: line N: …` error prefix (also
// used by yaml.TypeError's per-error lines).
var yamlLinePattern = regexp.MustCompile(`line (\d+)`)

// lineFromYamlError extracts the 1-based line from a yaml.v3 error message;
// ok is false when no position could be recovered (the caller then uses the
// file-level fallback with the "position unavailable" hint).
func lineFromYamlError(err error) (int, bool) {
	match := yamlLinePattern.FindStringSubmatch(err.Error())
	if match == nil {
		return 1, false
	}
	line, convErr := strconv.Atoi(match[1])
	if convErr != nil {
		return 1, false
	}
	return line, true
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

func decodeStrictJSON(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

func asValidationError(err error, target **schemas.ValidationError) bool {
	return errors.As(err, target)
}
