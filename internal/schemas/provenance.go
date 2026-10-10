package schemas

// Ported from packages/schemas/src/v1/provenance.ts (PROVENANCE_SCHEMA +
// PROVENANCE_SCHEMA_ID + PROVENANCE_RUN_ID_REGEX + GIT_SHA_REGEX +
// PROVENANCE_EXAMPLE).
//
// Provenance record — `docsync.provenance/v1` (P9-T01; PLAN §10.3/§10.4;
// ARCH §12.5 binding shape; D5 storage precedence). Every automated
// documentation change carries this structured evidence; a provenance-less
// PR is a bug, not a format option (invariant 10). Records reflect what
// actually ran: `detector.model`/`writer.model` carry the ACTUALLY-ROUTED
// OpenRouter model IDs — never a hardcoded brand — and fallback routing must
// be reflected truthfully (ARCH §10.6; P9-T02; V-P9-4). The caller owning
// the write passes routed ids; this schema only refuses empty ones.
//
// Shape notes (ARCH §12.5 / P9-T01): flat envelope with a literal `schema`
// tag (OD-R01 strict, OD-2 vocabulary reuse); `verification.status` reuses
// the verifier's canonical VERIFICATION_VERDICT vocabulary — import, never
// duplicate (OD-2); `source_commit` is the full 40-hex git sha; the run key
// elsewhere pins `manifest_version` as a number while this record carries it
// as a string (documented asymmetry); `run_id` is filename-safe because it
// keys the archival filename `prov-<run_id>.json` (OD-R44: no new state
// subdirs) — `[A-Za-z0-9]` first, then `[A-Za-z0-9._-]`, no `/`, no leading `.`.

// ProvenanceSchemaID is the literal schema tag `docsync.provenance/v1`
// (PROVENANCE_SCHEMA_ID).
const ProvenanceSchemaID = "docsync.provenance/v1"

// IsProvenanceRunID reports the filename-safe run id shape
// (PROVENANCE_RUN_ID_REGEX /^[A-Za-z0-9][A-Za-z0-9._-]*$/). ULIDs,
// `run_01J…`-style ids and ULID-shaped ids all pass; `/`, `\`, leading `.`,
// and empty ids are refused (fail closed).
func IsProvenanceRunID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			// Always allowed, including first position.
		case c == '.' || c == '_' || c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// IsGitSHA reports the full lowercase 40-hex git commit sha shape
// (GIT_SHA_REGEX /^[0-9a-f]{40}$/; ARCH §6.3 audit-replay pin).
func IsGitSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < 40; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ProvenanceDetector is the detector block: routed model + confidence.
type ProvenanceDetector struct {
	Model      string  `json:"model"`
	Confidence float64 `json:"confidence"`
}

// Validate checks the detector block.
func (d *ProvenanceDetector) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(d.Model, 1, []string{"model"}))
	if d.Confidence < 0 {
		Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be greater than or equal to 0"})
	}
	if d.Confidence > 1 {
		Collect(&issues, &Issue{Path: []string{"confidence"}, Message: "Number must be less than or equal to 1"})
	}
	return issues
}

// ProvenanceWriter is the writer block: the actually-routed model.
type ProvenanceWriter struct {
	Model string `json:"model"`
}

// Validate checks the writer block.
func (w *ProvenanceWriter) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(w.Model, 1, []string{"model"}))
	return issues
}

// ProvenanceVerification reuses the verifier's canonical verdict vocabulary
// (OD-2 — import, never duplicate).
type ProvenanceVerification struct {
	Status VerificationVerdict `json:"status"`
}

// Validate checks the verification block.
func (v *ProvenanceVerification) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(v.Status), stringsOf(VerificationVerdictValues), []string{"status"}))
	return issues
}

// Provenance is one `docsync.provenance/v1` record — written once per run,
// append-only (D5 archival; PROVENANCE_SCHEMA).
type Provenance struct {
	Schema          string                 `json:"schema"`
	RunID           string                 `json:"run_id"`
	SourceCommit    string                 `json:"source_commit"`
	SourceFiles     []string               `json:"source_files"`
	Detector        ProvenanceDetector     `json:"detector"`
	Writer          ProvenanceWriter       `json:"writer"`
	Verification    ProvenanceVerification `json:"verification"`
	GeneratedAt     string                 `json:"generated_at"`
	PolicyVersion   int                    `json:"policy_version"`
	ManifestVersion string                 `json:"manifest_version"`
}

// Validate checks the record against PROVENANCE_SCHEMA (strict),
// reproducing the zod issue paths.
func (p *Provenance) Validate() []Issue {
	var issues []Issue
	if p.Schema != ProvenanceSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.provenance/v1"`})
	}
	Collect(&issues, RequiredString(p.RunID, 1, []string{"run_id"}))
	if !IsProvenanceRunID(p.RunID) {
		Collect(&issues, &Issue{Path: []string{"run_id"}, Message: "Invalid"})
	}
	if !IsGitSHA(p.SourceCommit) {
		Collect(&issues, &Issue{Path: []string{"source_commit"}, Message: "Invalid"})
	}
	if len(p.SourceFiles) < 1 {
		Collect(&issues, &Issue{Path: []string{"source_files"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i, f := range p.SourceFiles {
		Collect(&issues, RequiredString(f, 1, []string{"source_files", itoa(i)}))
	}
	for _, iss := range p.Detector.Validate() {
		iss.Path = append([]string{"detector"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range p.Writer.Validate() {
		iss.Path = append([]string{"writer"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range p.Verification.Validate() {
		iss.Path = append([]string{"verification"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, datetimeIssue(p.GeneratedAt, []string{"generated_at"}))
	if p.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, RequiredString(p.ManifestVersion, 1, []string{"manifest_version"}))
	return issues
}

// ParseProvenance strictly decodes and validates `docsync.provenance/v1`
// bytes (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseProvenance(data []byte) (*Provenance, error) {
	var p Provenance
	if err := StrictDecode(data, &p); err != nil {
		return nil, err
	}
	if issues := p.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &p, nil
}

// ProvenanceExample mirrors PROVENANCE_EXAMPLE (registered as
// 'docsync.provenance' v1; golden data for byte-for-byte serializer checks).
func ProvenanceExample() Provenance {
	return Provenance{
		Schema:          "docsync.provenance/v1",
		RunID:           "run_01J9ZK6X8A",
		SourceCommit:    "3f9c2a7b1d4e5f60718293a4b5c6d7e8f9012a3b",
		SourceFiles:     []string{"src/payments/refund.ts", "src/payments/types.ts"},
		Detector:        ProvenanceDetector{Model: "z-ai/glm-4.7", Confidence: 0.97},
		Writer:          ProvenanceWriter{Model: "anthropic/claude-sonnet-4.5"},
		Verification:    ProvenanceVerification{Status: VerdictPass},
		GeneratedAt:     "2026-10-04T22:31:00Z",
		PolicyVersion:   17,
		ManifestVersion: "1",
	}
}
