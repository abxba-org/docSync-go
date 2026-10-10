package provenance

// Ported from core/src/provenance/record.ts (P9-T01; ARCH §12.5; D5).
//
// The record builder is PURE: it assembles a `docsync.provenance/v1` record
// from explicit inputs and refuses — with a typed ProvenanceBuildError naming
// the offending field — whenever a required input is missing or invalid (fail
// closed; a half-record is never built, §3 rule 9).
//
// Routed-model honesty (ARCH §10.6; V-P9-4): RoutedModels takes whatever the
// caller passes — the caller is responsible for passing the ACTUALLY-ROUTED
// model IDs. Reflecting OpenRouter fallback outcomes is the transport layer's
// job; this module has no fallback plumbing and invents none. Where telemetry
// is absent, the configured model is what exists and is what gets recorded —
// never a fabricated fallback.
//
// Fields, never generated: `now` is an injected ISO-8601 UTC instant (rendered
// verbatim as `generated_at`); no clock, no randomness.

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CodeProvenanceBuild is the closest registered docutil code for a refused
// builder input (CONVENTIONS.md §Errors: the TS class code
// EDOCSYNC_PROVENANCE_BUILD is not in the code list; class identity is
// preserved by the ProvenanceBuildError TYPE).
const CodeProvenanceBuild = docutil.CodeValidation

// ProvenanceBuildError is the Go port of the TS ProvenanceBuildError class: a
// refused builder input carrying the offending field (snake_case as in the
// record). It is a returned value, never a panic.
type ProvenanceBuildError struct {
	// Field is the offending input field, exactly as the TS constructor took it.
	Field string
	// Message preserves the TS message text verbatim.
	Message string
	coded   *docutil.Error
}

func (e *ProvenanceBuildError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf / errors.As keep working.
func (e *ProvenanceBuildError) Unwrap() error { return e.coded }

// Code returns the coded-error code for this refusal (EDOCSYNC_VALIDATION).
func (e *ProvenanceBuildError) Code() string { return CodeProvenanceBuild }

// RoutedModels are the actually-routed model ids for the two LLM roles.
type RoutedModels struct {
	// DetectorModel is the actually-routed decision-engine (Jev) model id.
	DetectorModel string `json:"detector_model"`
	// WriterModel is the actually-routed writer model id.
	WriterModel string `json:"writer_model"`
}

// BuildProvenanceInput is the builder input (P9-T01 spec shape).
type BuildProvenanceInput struct {
	RunID              string       `json:"run_id"`
	SourceCommit       string       `json:"source_commit"`
	SourceFiles        []string     `json:"source_files"`
	RoutedModels       RoutedModels `json:"routed_models"`
	DetectorConfidence float64      `json:"detector_confidence"`
	// VerificationStatus is the verifier verdict (`pass | review | reject` —
	// the canonical vocabulary).
	VerificationStatus string `json:"verification_status"`
	PolicyVersion      int    `json:"policy_version"`
	// ManifestVersion is the manifest version identifier as a string.
	ManifestVersion string `json:"manifest_version"`
	// Now is the injected clock instant (ISO-8601 UTC) — a field, never generated.
	Now string `json:"now"`
}

// ISO_UTC_REGEX: an ISO-8601 UTC instant, rendered verbatim as generated_at.
var isoUTCRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)

// BuildProvenanceRecord builds a validated provenance record. Every input is
// checked explicitly (a typed error naming the field), then the assembled
// record is parsed against PROVENANCE_SCHEMA as the final floor — belt and
// suspenders, so the builder can NEVER emit schema drift (OD-R03 posture).
func BuildProvenanceRecord(input BuildProvenanceInput) (*schemas.Provenance, error) {
	runID, err := requireNonEmpty(input.RunID, "run_id")
	if err != nil {
		return nil, err
	}
	if !schemas.IsProvenanceRunID(runID) {
		return nil, buildError(
			`run_id "`+runID+`" is not filename-safe ([A-Za-z0-9] first, then [A-Za-z0-9._-])`,
			"run_id")
	}
	sourceCommit, err := requireNonEmpty(input.SourceCommit, "source_commit")
	if err != nil {
		return nil, err
	}
	if !schemas.IsGitSHA(sourceCommit) {
		return nil, buildError(`source_commit "`+sourceCommit+`" is not a 40-hex git sha`, "source_commit")
	}
	sourceFiles := input.SourceFiles
	if len(sourceFiles) == 0 {
		return nil, buildError("source_files must be a non-empty array", "source_files")
	}
	for _, file := range sourceFiles {
		if file == "" {
			return nil, buildError("every source_files entry must be a non-empty string", "source_files")
		}
	}
	detectorModel, err := requireNonEmpty(input.RoutedModels.DetectorModel, "detector.model")
	if err != nil {
		return nil, err
	}
	writerModel, err := requireNonEmpty(input.RoutedModels.WriterModel, "writer.model")
	if err != nil {
		return nil, err
	}
	confidence := input.DetectorConfidence
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		return nil, buildError(
			"detector.confidence must be a finite number in [0, 1] (got "+jsNumber(confidence)+")",
			"detector.confidence")
	}
	verificationStatus, err := requireNonEmpty(input.VerificationStatus, "verification.status")
	if err != nil {
		return nil, err
	}
	if !isVerificationVerdict(verificationStatus) {
		return nil, buildError(
			`verification.status "`+verificationStatus+`" is outside the verifier verdict vocabulary (pass | review | reject)`,
			"verification.status")
	}
	policyVersion := input.PolicyVersion
	if policyVersion <= 0 {
		return nil, buildError(
			"policy_version must be a positive integer (got "+strconv.Itoa(policyVersion)+")",
			"policy_version")
	}
	manifestVersion, err := requireNonEmpty(input.ManifestVersion, "manifest_version")
	if err != nil {
		return nil, err
	}
	generatedAt, err := requireNonEmpty(input.Now, "generated_at")
	if err != nil {
		return nil, err
	}
	if !isoUTCRegex.MatchString(generatedAt) {
		return nil, buildError(
			`generated_at "`+generatedAt+`" is not an ISO-8601 UTC instant (…Z)`,
			"generated_at")
	}

	record := &schemas.Provenance{
		Schema:          schemas.ProvenanceSchemaID,
		RunID:           runID,
		SourceCommit:    sourceCommit,
		SourceFiles:     append([]string(nil), sourceFiles...),
		Detector:        schemas.ProvenanceDetector{Model: detectorModel, Confidence: confidence},
		Writer:          schemas.ProvenanceWriter{Model: writerModel},
		Verification:    schemas.ProvenanceVerification{Status: schemas.VerificationVerdict(verificationStatus)},
		GeneratedAt:     generatedAt,
		PolicyVersion:   policyVersion,
		ManifestVersion: manifestVersion,
	}
	parsed, err := parseProvenanceRecord(record)
	if err != nil {
		return nil, buildError(
			"assembled record failed the docsync.provenance/v1 schema (fail closed): "+err.Error(),
			"<record>")
	}
	return parsed, nil
}

// parseProvenanceRecord is the builder's final floor: the assembled record is
// serialized and STRICTLY parsed against the registered docsync.provenance/v1
// (unknown keys rejected, mirroring z.strictObject), so the parsed value is
// exactly what an archival write would accept.
func parseProvenanceRecord(record *schemas.Provenance) (*schemas.Provenance, error) {
	wire, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return schemas.ParseProvenance(wire)
}

// jsNumber renders a float the way JS `String(value)` did in the TS refusal
// messages (NaN / ±Infinity spelled out, shortest round-trip otherwise).
func jsNumber(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	default:
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
}

// requireNonEmpty ports the TS requireNonEmpty: a non-empty string, or a typed
// refusal naming the field.
func requireNonEmpty(value, field string) (string, error) {
	if value == "" {
		return "", buildError(field+" must be a non-empty string", field)
	}
	return value, nil
}

// isVerificationVerdict reports whether status is in the canonical verifier
// verdict vocabulary (OD-2 — import, never duplicate).
func isVerificationVerdict(status string) bool {
	switch schemas.VerificationVerdict(status) {
	case schemas.VerdictPass, schemas.VerdictReview, schemas.VerdictReject:
		return true
	default:
		return false
	}
}

// buildError builds the typed refusal with the TS message text preserved.
func buildError(message, field string) *ProvenanceBuildError {
	return &ProvenanceBuildError{
		Field:   field,
		Message: message,
		coded:   docutil.New(CodeProvenanceBuild, message),
	}
}
