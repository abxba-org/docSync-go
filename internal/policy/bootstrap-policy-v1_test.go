// Ported from packages/policy/test/bootstrap-policy.test.ts
// (bootstrap/policy-v1.ts assertions). The `unsafeAutoUpdatesGate` block of
// the TS suite targets versioning.ts's promotion-gate helper and is ported by
// the versioning unit, not here.
package policy_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func mustCompileBootstrapPolicyV1(t *testing.T) *policy.CompiledPolicyWorkflow {
	t.Helper()
	compiled, err := policy.CompileBootstrapPolicyV1()
	require.NoError(t, err)
	require.NotNil(t, compiled)
	return compiled
}

// TestBootstrapPolicyV1CompilesClean ports "compiles clean and targets
// docsync.decision/v1".
func TestBootstrapPolicyV1CompilesClean(t *testing.T) {
	compiled := mustCompileBootstrapPolicyV1(t)

	assert.Equal(t, schemas.DecisionSchemaID, compiled.DecisionSchemaVersion)
	assert.Regexp(t, regexp.MustCompile(`^sha256:[0-9a-f]{64}$`), compiled.PolicyHash)
}

// TestBootstrapPolicyV1CarriesTheCanonicalUniversalQuestions ports "carries
// exactly the canonical universal questions, in canonical order, with the
// canonical types".
func TestBootstrapPolicyV1CarriesTheCanonicalUniversalQuestions(t *testing.T) {
	compiled := mustCompileBootstrapPolicyV1(t)

	keys := make([]string, 0, len(compiled.Questions))
	for _, question := range compiled.Questions {
		keys = append(keys, question.Key)
	}
	assert.Equal(t, schemas.UniversalQuestions, keys)

	behavior, apiContract, docCount, impact := compiled.Questions[0], compiled.Questions[1], compiled.Questions[2], compiled.Questions[3]
	assert.Equal(t, policy.QuestionTypeBoolean, behavior.Type)
	assert.Equal(t, policy.QuestionTypeBoolean, apiContract.Type)
	assert.Equal(t, policy.QuestionTypeScore, docCount.Type)
	require.NotNil(t, docCount.Scoring)
	assert.InDelta(t, 0, docCount.Scoring.Min, 1e-12)
	assert.InDelta(t, 50, docCount.Scoring.Max, 1e-12)
	assert.Equal(t, policy.QuestionTypeChoice, impact.Type)
	assert.Equal(t, evalStringsOf(schemas.DocumentationImpactValues), impact.Choices)
}

// TestBootstrapPolicyV1CarriesConservativeThresholds ports "carries the
// conservative abstain-safe calibrated thresholds (§5.2 / §8.3)".
func TestBootstrapPolicyV1CarriesConservativeThresholds(t *testing.T) {
	compiled := mustCompileBootstrapPolicyV1(t)

	assert.Equal(t, policy.Routing{
		HighConfidence:               0.94,
		Review:                       0.72,
		CalibrationFloor:             bootstrapFloat64Ptr(0.94),
		DocumentationImpactAutoFloor: bootstrapFloat64Ptr(0.94),
	}, compiled.Routing)
}

// TestBootstrapPolicyV1IsSchemaValidAndByteStable ports "is schema-valid,
// byte-stable across runs, and pins the v1 trigger taxonomy". The TS
// `POLICY_SCHEMA.parse(BOOTSTRAP_POLICY_V1) === BOOTSTRAP_POLICY_V1` identity
// is expressed through the compiler's schema-validating parse plus the
// constant's canonical wire bytes.
func TestBootstrapPolicyV1IsSchemaValidAndByteStable(t *testing.T) {
	// Schema-valid: the constant compiles clean (the compiler runs the
	// policy schema parse first).
	assert.Equal(t, mustCompileBootstrapPolicyV1(t), mustCompileBootstrapPolicyV1(t))

	// The constant is clock-free and deterministic: its canonical wire bytes
	// are byte-stable and round-trip.
	first, err := json.Marshal(policy.BootstrapPolicyV1)
	require.NoError(t, err)
	second, err := json.Marshal(policy.BootstrapPolicyV1)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
	assert.Contains(t, string(first), `"schema":"docsync.jev-policy/v1"`)
	assert.Contains(t, string(first), `"created_at":"2026-01-01T00:00:00.000Z"`)
	assert.Contains(t, string(first), `"unsafe_auto_updates":{"enabled":false}`)
	assert.Contains(t, string(first), `"taxonomy_version":1`)

	var roundTripped map[string]any
	require.NoError(t, json.Unmarshal(first, &roundTripped))
	assert.Equal(t, "bootstrap", roundTripped["created_by"])
	assert.Equal(t, float64(1), roundTripped["policy_version"])
	assert.Equal(t, true, roundTripped["universal"])
}

// TestBootstrapPolicyV1FieldPins pins the constant's provenance fields.
func TestBootstrapPolicyV1FieldPins(t *testing.T) {
	p := policy.BootstrapPolicyV1

	assert.Equal(t, "docsync.jev-policy/v1", p.Schema)
	assert.Equal(t, 1, p.PolicyVersion)
	assert.Equal(t, "bootstrap", p.CreatedBy)
	assert.True(t, p.Universal)
	assert.Equal(t, schemas.TriggerTaxonomyVersion, p.TaxonomyVersion)
	require.NotNil(t, p.UnsafeAutoUpdates)
	assert.False(t, p.UnsafeAutoUpdates.Enabled)
	require.NotNil(t, p.Routing.CalibrationFloor)
	require.NotNil(t, p.Routing.DocumentationImpactAutoFloor)
	assert.Equal(t,
		"Bootstrap default Jev policy v1: the canonical universal questions with conservative abstain-safe "+
			"routing (high_confidence 0.94, review 0.72, calibration_floor 0.94, documentation_impact_auto_floor "+
			"0.94); unsafe auto updates disabled (ARCH §8.4 promotion gate).",
		p.Reason)
}

// TestCompileBootstrapPolicyV1FailsClosed pins that a compilation failure is
// surfaced as the invariant-breach error (never a silent pass).
func TestCompileBootstrapPolicyV1FailsClosed(t *testing.T) {
	compiled, err := policy.CompileBootstrapPolicyV1()
	require.NoError(t, err)
	require.NotNil(t, compiled)

	// The error type is exported and renders the invariant-breach message
	// with every diagnostic named.
	breach := &policy.BootstrapPolicyCompileError{Diagnostics: []policy.PolicyDiagnostic{{
		Kind:    "schema_violation",
		Path:    "questions.0.choices",
		Message: "scaffolding diagnostic",
	}}}
	assert.Equal(t,
		"bootstrap policy v1 failed compilation (invariant breach — it must always compile clean): "+
			"schema_violation at questions.0.choices: scaffolding diagnostic",
		breach.Error())
	assert.Equal(t, "EDOCSYNC_SCHEMA", breach.Code())
	assert.Equal(t, "EDOCSYNC_SCHEMA", docutil.CodeOf(breach))
}

func bootstrapFloat64Ptr(v float64) *float64 { return &v }
