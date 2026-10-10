// Ported from packages/policy/src/bootstrap/policy-v1.ts.

package policy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// BootstrapPolicyV1 is the bootstrap default Jev policy v1 — the canonical
// universal set with abstain-heavy conservative routing
// (BOOTSTRAP_POLICY_V1): exactly the canonical UNIVERSAL_QUESTIONS of
// `docsync.decision/v1` (in canonical order), the conservative calibrated
// routing thresholds (high_confidence 0.94, review 0.72, calibration_floor
// 0.94, documentation_impact_auto_floor 0.94 — the §5.2 calibrated-data
// example), and `unsafe_auto_updates` disabled (the §8.4 promotion gate).
// `policy_version` is 1: the first entry of every repository's policy history.
//
// The object is a fixed, clock-free, deterministic constant: `created_at` is a
// shape-only ISO-8601 string (the bootstrap authoring timestamp, never a live
// clock), so the compiled artifact — and its hash — is byte-stable across
// runs. Choice data is drawn from the canonical value arrays of
// internal/schemas (OD-2), so schema drift is impossible by construction.
var BootstrapPolicyV1 = Policy{
	Schema:        PolicySchemaID,
	PolicyVersion: 1,
	CreatedBy:     "bootstrap",
	Reason: "Bootstrap default Jev policy v1: the canonical universal questions with conservative abstain-safe " +
		"routing (high_confidence 0.94, review 0.72, calibration_floor 0.94, documentation_impact_auto_floor " +
		"0.94); unsafe auto updates disabled (ARCH §8.4 promotion gate).",
	CreatedAt: "2026-01-01T00:00:00.000Z",
	Questions: []Question{
		{
			Key:    "public_behavior_changed",
			Type:   QuestionTypeBoolean,
			Prompt: "Did the change alter runtime behavior visible to users of this repository?",
		},
		{
			Key:    "api_contract_changed",
			Type:   QuestionTypeBoolean,
			Prompt: "Did the change alter the public contract (exported API surface)?",
		},
		{
			Key:     "affected_doc_count",
			Type:    QuestionTypeScore,
			Prompt:  "How many known documentation candidates does this change affect?",
			Scoring: &QuestionScoring{Min: 0, Max: 50},
		},
		{
			Key:     "documentation_impact",
			Type:    QuestionTypeChoice,
			Prompt:  "What is the documentation impact of this change?",
			Choices: choiceSetStrings(schemas.DocumentationImpactValues),
		},
	},
	Routing: Routing{
		HighConfidence:               0.94,
		Review:                       0.72,
		CalibrationFloor:             float64Ptr(0.94),
		DocumentationImpactAutoFloor: float64Ptr(0.94),
	},
	Universal:         true,
	TaxonomyVersion:   schemas.TriggerTaxonomyVersion,
	UnsafeAutoUpdates: &UnsafeAutoUpdates{Enabled: false},
}

// BootstrapPolicyCompileError is thrown only if the bootstrap policy ever
// fails compilation — an invariant breach of the shipped default (it must
// always compile clean), never a user-facing path. Fail-closed: no workflow
// is produced (BootstrapPolicyCompileError).
type BootstrapPolicyCompileError struct {
	Diagnostics []PolicyDiagnostic
}

// Error renders the invariant-breach message with every diagnostic named.
func (e *BootstrapPolicyCompileError) Error() string {
	parts := make([]string, 0, len(e.Diagnostics))
	for _, diagnostic := range e.Diagnostics {
		parts = append(parts, fmt.Sprintf("%s at %s: %s", diagnostic.Kind, diagnostic.Path, diagnostic.Message))
	}
	return "bootstrap policy v1 failed compilation (invariant breach — it must always compile clean): " +
		strings.Join(parts, "; ")
}

// Code maps the invariant breach onto the coded-error class (docutil).
func (e *BootstrapPolicyCompileError) Code() string { return docutil.CodeSchema }

// Unwrap exposes the coded error so docutil.CodeOf reports the code.
func (e *BootstrapPolicyCompileError) Unwrap() error {
	return docutil.New(docutil.CodeSchema, e.Error())
}

// CompileBootstrapPolicyV1 compiles the bootstrap policy v1 into the typed Jev
// workflow — the CI validity test artifact (V-P2-12 groundwork): compilation
// MUST succeed; a diagnostic would be a bootstrap-data invariant breach and
// fails closed by returning the error. Deterministic: the same constant always
// compiles to an identical workflow (same policyHash).
func CompileBootstrapPolicyV1() (*CompiledPolicyWorkflow, error) {
	wire, err := bootstrapPolicyV1Wire()
	if err != nil {
		return nil, &BootstrapPolicyCompileError{Diagnostics: []PolicyDiagnostic{{
			Kind:    "schema_violation",
			Path:    "(root)",
			Message: err.Error(),
		}}}
	}
	result := CompilePolicy(wire)
	if !result.OK {
		return nil, &BootstrapPolicyCompileError{Diagnostics: result.Diagnostics}
	}
	return result.Compiled, nil
}

// bootstrapPolicyV1Wire renders the bootstrap policy in its untrusted-input
// wire form for the compiler seam (the compiler validates the parsed
// YAML/JSON policy object, exactly as the architect runner hands it the
// model-supplied artifact map).
func bootstrapPolicyV1Wire() (map[string]any, error) {
	raw, err := json.Marshal(BootstrapPolicyV1)
	if err != nil {
		return nil, docutil.New(docutil.CodeInternal,
			"bootstrap policy v1 could not be serialized to its wire form: "+err.Error())
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, docutil.New(docutil.CodeInternal,
			"bootstrap policy v1 wire form could not be decoded: "+err.Error())
	}
	return wire, nil
}

// float64Ptr returns a pointer to v (package-local pointer helper — the
// pointer helpers of neighboring packages are unexported and not importable).
func float64Ptr(v float64) *float64 { return &v }
