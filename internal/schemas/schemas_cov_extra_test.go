package schemas_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCOV_SCHEMA_049_YAMLBridgeRejectsNonJSONValues covers the
// json.Marshal-boundary failure in both YAML bridges: a YAML `.nan` scalar
// resolves to a float64 NaN, which JSON cannot represent, so the generic →
// JSON bridge fails closed before any schema check runs.
func TestCOV_SCHEMA_049_YAMLBridgeRejectsNonJSONValues(t *testing.T) {
	nanManifest := []byte("version: .nan\ndocuments: []\n")
	_, err := schemas.ParseManifest(nanManifest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported value")

	nanProposal := []byte("schema: docsync.policy-proposal/v1\nproposal_id: x\npolicy_version_basis: .nan\n")
	_, err = schemas.ParsePolicyProposal(nanProposal)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported value")
}

// TestCOV_SCHEMA_050_ShadowRunRecordSampledRules covers the shadow
// run-record blocks: the sampling guards, the policy-version guards and the
// sampled/unsampled superRefine.
func TestCOV_SCHEMA_050_ShadowRunRecordSampledRules(t *testing.T) {
	example := schemas.ShadowRunRecordExample()
	assert.Empty(t, example.Validate())

	// The sampled/unsampled superRefine: a recorded decision requires sampling.
	unsampled := schemas.ShadowRunRecordExample()
	unsampled.Sampled = false
	issues := unsampled.Validate()
	require.NotNil(t, covIssueAt(issues, "decision"))
	assert.Equal(t, "an unsampled shadow record must not carry a decision",
		covIssueAt(issues, "decision").Message)

	// An unsampled record must carry a skip_reason and NO decision or input
	// hashes (fail-closed laziness of the shadow draw).
	cleanUnsampled := schemas.ShadowRunRecordExample()
	cleanUnsampled.Sampled = false
	cleanUnsampled.Decision = nil
	cleanUnsampled.InputHashes = nil
	cleanUnsampled.SkipReason = new("not sampled this run")
	assert.Empty(t, cleanUnsampled.Validate())

	// A missing skip_reason on an unsampled record is reported.
	unexplained := cleanUnsampled
	unexplained.SkipReason = nil
	issues = unexplained.Validate()
	require.NotNil(t, covIssueAt(issues, "skip_reason"))
	assert.Contains(t, covIssueAt(issues, "skip_reason").Message,
		"an unsampled shadow record must name its skip reason")

	// Input hashes on an unsampled record are equally forbidden.
	unhashable := cleanUnsampled
	unhashable.InputHashes = &schemas.ShadowRecordedInputHashes{}
	require.NotNil(t, covIssueAt(unhashable.Validate(), "input_hashes"))

	samplingBroken := schemas.ShadowRunRecordExample()
	samplingBroken.Sampling = schemas.ShadowSampling{Sampler: "nope", Fraction: -0.5}
	issues = samplingBroken.Validate()
	require.NotNil(t, covIssueAt(issues, "sampling", "sampler"))
	assert.Contains(t, covIssueAt(issues, "sampling", "sampler").Message, "Invalid enum value. Expected")
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "sampling", "fraction").Message)

	policyBroken := schemas.ShadowRunRecordExample()
	policyBroken.Policy.ProductionVersion = 0
	policyBroken.Policy.CandidateVersion = 0
	issues = policyBroken.Validate()
	require.NotNil(t, covIssueAt(issues, "policy", "production_version"))
	assert.Equal(t, "Number must be greater than 0", covIssueAt(issues, "policy", "production_version").Message)
	assert.Equal(t, "Number must be greater than 0", covIssueAt(issues, "policy", "candidate_version").Message)

	assert.False(t, schemas.IsValidShadowSampler("nope"))
	assert.True(t, schemas.IsValidShadowSampler("sha256-hex-threshold/v1"))

	// The root guards and the strictness of the record itself.
	bare := schemas.ShadowRunRecord{}
	issues = bare.Validate()
	assert.Equal(t, "Invalid literal value, expected \"docsync.shadow-run-record/v1\"", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "record_id"))
	require.NotNil(t, covIssueAt(issues, "run_id"))
	require.NotNil(t, covIssueAt(issues, "pr_id"))
	require.NotNil(t, covIssueAt(issues, "repo"))
	assert.Equal(t, "Invalid literal value, expected true", covIssueAt(issues, "shadow_only").Message)
}

// TestCOV_SCHEMA_051_SafetyIncidentValidators covers the safety-incident
// record guards and the Zulu-datetime strictness (lowercase 't'/'z' and
// offsets are rejected, microns and fractional seconds are accepted).
func TestCOV_SCHEMA_051_SafetyIncidentValidators(t *testing.T) {
	exampleIncident := schemas.SafetyIncidentExample()
	assert.Empty(t, (&exampleIncident).Validate())

	badRule := schemas.SafetyIncident{Rule: "anything-goes", Schema: "", IncidentID: "", At: "", RunID: ""}
	issues := badRule.Validate()
	require.NotNil(t, covIssueAt(issues, "rule"))
	assert.Contains(t, covIssueAt(issues, "rule").Message, "Invalid enum value")
	assert.Contains(t, covIssueAt(issues, "rule").Message,
		"Expected 'out_of_allowlist_path' | 'protected_path_patch' | 'machine_mode_transition'")
	require.NotNil(t, covIssueAt(issues, "schema"))
	require.NotNil(t, covIssueAt(issues, "incident_id"))
	require.NotNil(t, covIssueAt(issues, "run_id"))
	assert.Contains(t, covIssueAt(issues, "at").Message, "Invalid datetime")

	base := schemas.SafetyIncident{
		Schema:     "docsync.safety-incident/v1",
		IncidentID: "inc_1",
		RunID:      "run_01J9ZK6X8A",
		Rule:       schemas.SafetyIncidentRuleProvenanceMissing,
	}

	// A lowercase separator is the RFC3339Nano-tolerant shape zod rejects.
	lowercase := base
	lowercase.At = "2026-10-09t00:00:00Z"
	require.NotNil(t, covIssueAt((&lowercase).Validate(), "at"))

	// A lowercase trailing 'z' is equally rejected.
	lowercaseZ := base
	lowercaseZ.At = "2026-10-09T00:00:00z"
	require.NotNil(t, covIssueAt((&lowercaseZ).Validate(), "at"))

	// An offset datetime is rejected (Zulu only).
	offset := base
	offset.At = "2026-10-09T00:00:00+02:00"
	require.NotNil(t, covIssueAt((&offset).Validate(), "at"))

	// A valid Zulu datetime with a fractional part is accepted.
	fractional := base
	fractional.At = "2026-10-09T00:00:00.000Z"
	fractional.OffendingPath = "docs/a.md"
	fractional.Detail = "provenance missing"
	assert.Empty(t, (&fractional).Validate())

	// A blank rule violates the allow-list.
	blankRule := base
	blankRule.Rule = ""
	blankRule.At = "2026-10-09T00:00:00Z"
	require.NotNil(t, covIssueAt((&blankRule).Validate(), "rule"))

	assert.False(t, schemas.IsValidSafetyIncidentRule("nope"))
	assert.True(t, schemas.IsValidSafetyIncidentRule(string(schemas.SafetyIncidentRuleManifestOrPolicyEdit)))

	// The strict root key set is enforced on the YAML bridge.
	_, err := schemas.ParseSafetyIncident([]byte("schema: docsync.safety-incident/v1\nnope: 1\n"))
	require.Error(t, err)
}

// TestCOV_SCHEMA_052_RunRecordLeafValidators covers the run-record leaf
// blocks (run key, stages, state churn) and the root guards.
func TestCOV_SCHEMA_052_RunRecordLeafValidators(t *testing.T) {
	example := schemas.RunRecordExample()
	assert.Empty(t, example.Validate())

	validKey := schemas.RunKey{
		Repo:            "acme/payments",
		BaseSHA:         "8a73f2c",
		HeadSHA:         "81af92e",
		ManifestVersion: 1,
		PolicyVersion:   17,
	}
	assert.Empty(t, validKey.Validate())

	badKey := schemas.RunKey{Repo: "", BaseSHA: "", HeadSHA: "", ManifestVersion: 0, PolicyVersion: 0}
	issues := badKey.Validate()
	require.NotNil(t, covIssueAt(issues, "repo"))
	require.NotNil(t, covIssueAt(issues, "base_sha"))
	require.NotNil(t, covIssueAt(issues, "head_sha"))
	assert.Equal(t, "Number must be greater than 0", covIssueAt(issues, "manifest_version").Message)
	assert.Equal(t, "Number must be greater than 0", covIssueAt(issues, "policy_version").Message)

	badChurn := schemas.StateChurn{Added: -1, Updated: -1, Unchanged: -1}
	issues = badChurn.Validate()
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "added").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "updated").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "unchanged").Message)

	broken := schemas.RunRecord{
		Schema:     "",
		RunID:      "",
		StartedAt:  "nope",
		Key:        schemas.RunKey{},
		Stages:     []schemas.RunStage{{}},
		StateChurn: schemas.StateChurn{Added: -1},
	}
	issues = broken.Validate()
	assert.Equal(t, "Invalid literal value, expected \"docsync.run/v1\"", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "run_id"))
	assert.Contains(t, covIssueAt(issues, "started_at").Message, "Invalid datetime")
	require.NotNil(t, covIssueAt(issues, "key", "repo"))
	require.NotNil(t, covIssueAt(issues, "stages", "0", "stage"))

	// A blank stage list is valid (z.empty()).
	minimal := schemas.RunRecord{
		Schema: schemas.RunRecordSchemaID, RunID: "r", StartedAt: "2026-10-04T22:30:47Z",
		Key: validKey, Stages: []schemas.RunStage{},
	}
	assert.Empty(t, minimal.Validate())
}
