// Ported from packages/schemas/src/v1/safety-incident.ts: safety incident —
// `docsync.safety-incident/v1` (P6-T12; OD-R50 additive extension).
//
// The record shape for a tripped safety invariant: an operation the system
// refused (or must refuse, when the incident is recorded as part of a
// fail-closed block) because it violated one of the binding safety
// invariants — out-of-allowlist paths, protected-path patches, machine-mode
// transitions, manifest/policy edits, default-branch references and missing
// provenance. Incidents are append-only evidence: they are surfaced, never
// silenced, and they feed the Phase 9 invariant tests and Phase 10 metrics.
//
// OD-R50 (Phase 9; PLAN §10.3 fixed six-class taxonomy): the PLAN's six
// incident classes map onto this closed union — lock_violation →
// protected_path_patch, out_of_allowlist_write → out_of_allowlist_path,
// policy_path_violation → manifest_or_policy_edit, provenance_missing →
// provenance_missing (1:1) — plus two classes the shipped union lacked,
// carried verbatim: model_influence_on_authority and injection_contained.
// Additive extension of the v1 enum — no /v2 migration.
//
// The union is closed, every timestamp is a field supplied by the emitter —
// never generated here (no wall-clock reads anywhere).
package schemas

import "time"

// SafetyIncidentRule is the frozen safety-rule union — the incident taxonomy
// of the binding invariants.
type SafetyIncidentRule string

// SafetyIncidentRuleValues is the frozen eight-rule taxonomy (P6-T12 +
// OD-R50 additive extension: model_influence_on_authority, injection_contained
// appended, never renumbered).
var SafetyIncidentRuleValues = []SafetyIncidentRule{
	SafetyIncidentRuleOutOfAllowlistPath,
	SafetyIncidentRuleProtectedPathPatch,
	SafetyIncidentRuleMachineModeTransition,
	SafetyIncidentRuleManifestOrPolicyEdit,
	SafetyIncidentRuleDefaultBranchRef,
	SafetyIncidentRuleProvenanceMissing,
	SafetyIncidentRuleModelInfluenceOnAuthority,
	SafetyIncidentRuleInjectionContained,
}

// The eight safety-rule values in canonical order.
const (
	SafetyIncidentRuleOutOfAllowlistPath        SafetyIncidentRule = "out_of_allowlist_path"
	SafetyIncidentRuleProtectedPathPatch        SafetyIncidentRule = "protected_path_patch"
	SafetyIncidentRuleMachineModeTransition     SafetyIncidentRule = "machine_mode_transition"
	SafetyIncidentRuleManifestOrPolicyEdit      SafetyIncidentRule = "manifest_or_policy_edit"
	SafetyIncidentRuleDefaultBranchRef          SafetyIncidentRule = "default_branch_ref"
	SafetyIncidentRuleProvenanceMissing         SafetyIncidentRule = "provenance_missing"
	SafetyIncidentRuleModelInfluenceOnAuthority SafetyIncidentRule = "model_influence_on_authority"
	SafetyIncidentRuleInjectionContained        SafetyIncidentRule = "injection_contained"
)

// IsValidSafetyIncidentRule reports membership (SAFETY_INCIDENT_RULE).
func IsValidSafetyIncidentRule(r string) bool {
	for _, v := range SafetyIncidentRuleValues {
		if string(v) == r {
			return true
		}
	}
	return false
}

// SafetyIncident is the tripped-invariant record (SAFETY_INCIDENT_SCHEMA).
type SafetyIncident struct {
	Schema         string             `json:"schema" yaml:"schema"`
	IncidentID     string             `json:"incident_id" yaml:"incident_id"`
	At             string             `json:"at" yaml:"at"`
	RunID          string             `json:"run_id" yaml:"run_id"`
	Rule           SafetyIncidentRule `json:"rule" yaml:"rule"`
	OffendingPath  string             `json:"offending_path" yaml:"offending_path"`
	Detail         string             `json:"detail" yaml:"detail"`
	DocPath        *string            `json:"doc_path,omitempty" yaml:"doc_path,omitempty"`
	SourcePRNumber *int               `json:"source_pr_number,omitempty" yaml:"source_pr_number,omitempty"`
}

// safetyIncidentAllowedRootKeys is the strict root key set.
var safetyIncidentAllowedRootKeys = []string{
	"schema", "incident_id", "at", "run_id", "rule", "offending_path", "detail",
	"doc_path", "source_pr_number",
}

// Validate checks the incident against the schema (strict).
func (s *SafetyIncident) Validate() []Issue {
	var issues []Issue
	if s.Schema != "docsync.safety-incident/v1" {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: "Invalid literal value, expected \"docsync.safety-incident/v1\""})
	}
	Collect(&issues, RequiredString(s.IncidentID, 1, []string{"incident_id"}))
	if !isZuluDateTime(s.At) {
		Collect(&issues, &Issue{Path: []string{"at"}, Message: "Invalid datetime"})
	}
	Collect(&issues, RequiredString(s.RunID, 1, []string{"run_id"}))
	if !IsValidSafetyIncidentRule(string(s.Rule)) {
		Collect(&issues, &Issue{
			Path: []string{"rule"},
			Message: "Invalid enum value. Expected 'out_of_allowlist_path' | 'protected_path_patch' | " +
				"'machine_mode_transition' | 'manifest_or_policy_edit' | 'default_branch_ref' | 'provenance_missing' | " +
				"'model_influence_on_authority' | 'injection_contained', received '" + string(s.Rule) + "'",
		})
	}
	Collect(&issues, RequiredString(s.OffendingPath, 1, []string{"offending_path"}))
	Collect(&issues, RequiredString(s.Detail, 1, []string{"detail"}))
	if s.DocPath != nil {
		Collect(&issues, RequiredString(*s.DocPath, 1, []string{"doc_path"}))
	}
	if s.SourcePRNumber != nil && *s.SourcePRNumber < 1 {
		Collect(&issues, &Issue{Path: []string{"source_pr_number"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// ParseSafetyIncident strictly parses and validates YAML/JSON incident bytes.
// Unknown keys are rejected at the root (zod strictObject semantics).
func ParseSafetyIncident(data []byte) (*SafetyIncident, error) {
	var s SafetyIncident
	if isJSONish(data) {
		if err := decodeJSONStrict(data, &s); err != nil {
			return nil, err
		}
	} else {
		if err := decodeStrictYAMLOrJSON(data, &s, safetyIncidentAllowedRootKeys); err != nil {
			return nil, err
		}
	}
	if issues := s.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &s, nil
}

// SafetyIncidentExample returns the canonical example incident
// (SAFETY_INCIDENT_EXAMPLE).
func SafetyIncidentExample() SafetyIncident {
	return SafetyIncident{
		Schema:         "docsync.safety-incident/v1",
		IncidentID:     "inc_01J9ZM3X9B",
		At:             "2026-10-05T09:15:12Z",
		RunID:          "run_01J9ZK6X8A",
		Rule:           SafetyIncidentRuleOutOfAllowlistPath,
		OffendingPath:  "src/handlers/payments.ts",
		Detail:         "patch attempt touched a path outside the manifest allowlist — blocked, fail-closed (invariant 2)",
		DocPath:        new("docs/payments.md"),
		SourcePRNumber: new(42),
	}
}

// isZuluDateTime reports whether s is an RFC 3339 timestamp restricted to
// the UTC 'Z' designator (zod z.string().datetime({ offset: false })). All
// emitter-supplied timestamps in this schema are UTC. Semantically valid
// calendar dates are enforced on top of the shape (stricter than zod's
// regex, fail closed).
func isZuluDateTime(s string) bool {
	if !isZuluDateTimeShape(s) {
		return false
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return false
	}
	return true
}

// isZuluDateTimeShape checks the literal RFC 3339 Zulu shape without
// calendar validation.
func isZuluDateTimeShape(s string) bool {
	if len(s) < 20 {
		return false
	}
	for _, i := range [...]int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	if s[4] != '-' || s[7] != '-' {
		return false
	}
	if s[10] != 'T' || s[13] != ':' || s[16] != ':' {
		return false
	}
	// No fraction: seconds end at 18 and 'Z' follows at 19.
	if s[19] == 'Z' {
		return len(s) == 20
	}
	if s[19] != '.' {
		return false
	}
	// Fraction: '.' + 1-9 digits + 'Z'.
	if len(s) < 22 || s[len(s)-1] != 'Z' {
		return false
	}
	digits := 0
	for i := 20; i < len(s)-1; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		digits++
	}
	return digits >= 1 && digits <= 9
}

// isOffsetDateTime reports whether s is an RFC 3339 timestamp with a UTC
// offset designator, numeric offsets included (zod z.string().datetime()
// default).
func isOffsetDateTime(s string) bool {
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
		return false
	}
	return delimiterStrict(s)
}

// delimiterStrict rejects lowercase 't'/'z' separators, which RFC3339Nano
// accepts but zod does not.
func delimiterStrict(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 't' || s[i] == 'z' {
			return false
		}
	}
	return true
}
