package schemas_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// covPtr returns a pointer to a literal (test-only helper; the package-local
// strPtr/intPtr/floatPtr/boolPtr are unexported and not reachable from the
// external test package).
func covPtr[T any](v T) *T { return &v }

// covIssueAt returns the first issue carrying exactly the given path, or nil.
func covIssueAt(issues []schemas.Issue, path ...string) *schemas.Issue {
	for i := range issues {
		if len(issues[i].Path) != len(path) {
			continue
		}
		match := true
		for j := range path {
			if issues[i].Path[j] != path[j] {
				match = false
				break
			}
		}
		if match {
			return &issues[i]
		}
	}
	return nil
}

// covHasMessage reports whether any issue carries the exact message.
func covHasMessage(issues []schemas.Issue, message string) bool {
	for i := range issues {
		if issues[i].Message == message {
			return true
		}
	}
	return false
}

// TestCOV_SCHEMA_010_EnumPredicatesReportMembershipAndRejection exercises the
// closed-vocabulary membership predicates, both the accept and the reject path
// of every loop (a nil result proves the loop ran to completion).
func TestCOV_SCHEMA_010_EnumPredicatesReportMembershipAndRejection(t *testing.T) {
	for _, v := range schemas.ReportGlyphValues {
		assert.True(t, schemas.IsValidReportGlyph(string(v)), "report glyph %q", v)
	}
	assert.False(t, schemas.IsValidReportGlyph("critical"))
	assert.False(t, schemas.IsValidReportGlyph(""))

	for _, v := range schemas.LearningModeValues {
		assert.True(t, schemas.IsValidLearningMode(string(v)), "learning mode %q", v)
	}
	assert.False(t, schemas.IsValidLearningMode("sentient"))

	for _, v := range schemas.RiskBuckets {
		assert.True(t, schemas.IsValidRiskBucket(string(v)), "risk bucket %q", v)
	}
	assert.False(t, schemas.IsValidRiskBucket("extreme"))

	for _, v := range schemas.RoutingPolicyStates {
		assert.True(t, schemas.IsValidRoutingPolicyActivationState(string(v)), "activation state %q", v)
	}
	assert.False(t, schemas.IsValidRoutingPolicyActivationState("quarantined"))

	for _, v := range schemas.ClaimStatusValues {
		assert.True(t, schemas.IsValidClaimStatus(v), "claim status %q", v)
	}
	assert.False(t, schemas.IsValidClaimStatus("unknown"))

	for _, v := range schemas.DocStateNameValues {
		assert.True(t, schemas.IsValidDocStateName(string(v)), "doc state %q", v)
	}
	assert.False(t, schemas.IsValidDocStateName("VERIFIED "))

	for _, v := range []string{"accepted", "edited", "rejected", "intentional", "ignored"} {
		assert.True(t, schemas.IsValidHumanOutcome(v), "human outcome %q", v)
	}
	assert.False(t, schemas.IsValidHumanOutcome("maybe"))

	for _, v := range schemas.VerificationVerdictValues {
		assert.True(t, schemas.IsValidVerificationVerdict(string(v)), "verdict %q", v)
	}
	assert.False(t, schemas.IsValidVerificationVerdict("unknown"))

	for _, v := range schemas.DocModeValues {
		assert.True(t, schemas.IsValidDocMode(string(v)), "doc mode %q", v)
	}
	assert.False(t, schemas.IsValidDocMode("hybrid"))
}

// TestCOV_SCHEMA_011_IsValidManifestPathRejectsEscapingPaths covers the
// absolute / backslash / NUL / empty-segment guards of the manifest path
// predicate.
func TestCOV_SCHEMA_011_IsValidManifestPathRejectsEscapingPaths(t *testing.T) {
	assert.True(t, schemas.IsValidManifestPath("docs/payments.md"))
	assert.True(t, schemas.IsValidManifestPath("docs"))

	assert.False(t, schemas.IsValidManifestPath(""))
	assert.False(t, schemas.IsValidManifestPath("/etc/passwd"))
	assert.False(t, schemas.IsValidManifestPath("docs\\payments.md"))
	assert.False(t, schemas.IsValidManifestPath("docs/\x00payments.md"))
	assert.False(t, schemas.IsValidManifestPath("docs//payments.md"))
	assert.False(t, schemas.IsValidManifestPath("docs/./payments.md"))
	assert.False(t, schemas.IsValidManifestPath("docs/../payments.md"))
	assert.False(t, schemas.IsValidManifestPath(("../payments.md")))
}

// TestCOV_SCHEMA_012_HexAndRunPredicates covers the evidence-digest and
// provenance-identifier predicates, including the prefix/length guards.
func TestCOV_SCHEMA_012_HexAndRunPredicates(t *testing.T) {
	const bareSHA = "cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64"
	assert.True(t, schemas.IsBareSHA256Hex(bareSHA))
	assert.False(t, schemas.IsBareSHA256Hex("sha256:"+bareSHA))
	assert.False(t, schemas.IsBareSHA256Hex(bareSHA[:63]))
	assert.False(t, schemas.IsBareSHA256Hex("CF8A87E7256892CFF86B6013AE8324A39AD497A017B5780DA437740E1082DD64"))

	assert.True(t, schemas.IsGitSHA("8a73f2c1d4e5f60718293a4b5c6d7e8f90123456"))
	assert.False(t, schemas.IsGitSHA("8a73f2c1d4e5f60718293a4b5c6d7e8f9012345"))
	assert.False(t, schemas.IsGitSHA("8a73f2c1d4e5f60718293A4b5c6d7e8f90123456"))

	assert.True(t, schemas.IsProvenanceRunID("run_01J9ZK6X8A"))
	assert.True(t, schemas.IsProvenanceRunID("01J9ZK6X8A"))
	assert.False(t, schemas.IsProvenanceRunID(""))
	assert.False(t, schemas.IsProvenanceRunID("run 01J9ZK6X8A"))

	digest := "sha256:82b30f0886a7a8558d2bf448fcdd056c9d3e04e9a09c4aae64f443db5484a418"
	assert.True(t, schemas.IsSHA256HashDigest(digest))
	assert.False(t, schemas.IsSHA256HashDigest(digest[7:]))
	assert.False(t, schemas.IsSHA256HashDigest("sha512"+digest[6:]))
}

// TestCOV_SCHEMA_013_DecodeHelpersCoverLenientAndRequiredString covers
// DecodeLenient (unknown keys stripped, not rejected), StrictDecode's
// trailing-content guard and RequiredString's non-one minimum message.
func TestCOV_SCHEMA_013_DecodeHelpersCoverLenientAndRequiredString(t *testing.T) {
	type covLenientTarget struct {
		Keep string `json:"keep"`
	}
	var lenientTarget covLenientTarget
	require.NoError(t, schemas.DecodeLenient([]byte(`{"keep":"a","extra":1}`), &lenientTarget))
	assert.Equal(t, "a", lenientTarget.Keep)

	var strictTarget struct {
		Keep string `json:"keep"`
	}
	err := schemas.StrictDecode([]byte(`{"keep":"a"}{"keep":"b"}`), &strictTarget)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected trailing content after JSON value")

	short := schemas.RequiredString("a", 2, []string{"field"})
	require.NotNil(t, short)
	assert.Equal(t, "field", short.Path[0])
	assert.Equal(t, "String must contain at least 2 character(s)", short.Message)

	longEnough := schemas.RequiredString("ab", 2, []string{"field"})
	assert.Nil(t, longEnough)
}

// TestCOV_SCHEMA_014_IssueAndValidationErrorRendering covers the
// path-less/path-carrying renderings of a single Issue and a collected
// ValidationError.
func TestCOV_SCHEMA_014_IssueAndValidationErrorRendering(t *testing.T) {
	pathless := schemas.Issue{Message: "root problem"}
	assert.Equal(t, "root problem", pathless.Error())

	nested := schemas.Issue{Path: []string{"docs", "2", "path"}, Message: "Invalid"}
	assert.Equal(t, "docs.2.path: Invalid", nested.Error())

	collected := schemas.NewValidationError(
		schemas.Issue{Path: []string{"version"}, Message: "Number must be greater than 0"},
		schemas.Issue{Message: "root problem"},
	)
	assert.Contains(t, collected.Error(), "validation failed: version: Number must be greater than 0; root problem")
}

// TestCOV_SCHEMA_015_RegistryLookupAndExportFailClosed covers the registry
// lookup semantics: an unknown id, a version mismatch, and the fail-closed
// export when no entry matches.
func TestCOV_SCHEMA_015_RegistryLookupAndExportFailClosed(t *testing.T) {
	manifest := schemas.GetSchema("docsync.manifest")
	require.NotNil(t, manifest)
	assert.Equal(t, 1, manifest.Version)

	assert.Nil(t, schemas.GetSchema("docsync.nope"))
	assert.Nil(t, schemas.GetSchema("docsync.manifest", 99))

	exported, err := schemas.ExportJsonSchema("docsync.manifest")
	require.NoError(t, err)
	assert.Equal(t, "docsync.manifest/v1", exported["$id"])
	assert.Equal(t, "docsync.manifest/v1", exported["title"])
	assert.Equal(t, "http://json-schema.org/draft-07/schema#", exported["$schema"])

	byVersion, err := schemas.ExportJsonSchema("docsync.manifest", 1)
	require.NoError(t, err)
	assert.Equal(t, "docsync.manifest/v1", byVersion["$id"])

	missing, err := schemas.ExportJsonSchema("docsync.nope")
	require.Error(t, err)
	assert.Nil(t, missing)
	assert.Contains(t, err.Error(), "unknown schema id/version in registry: docsync.nope/v1")

	missingVersion, err := schemas.ExportJsonSchema("docsync.manifest", 42)
	require.Error(t, err)
	assert.Nil(t, missingVersion)
	assert.Contains(t, err.Error(), "docsync.manifest/v42")
}

// TestCOV_SCHEMA_016_SupportedManifestVersions covers the manifest version
// acceptance predicate (fail-closed outside the closed set).
func TestCOV_SCHEMA_016_SupportedManifestVersions(t *testing.T) {
	assert.True(t, schemas.IsSupportedManifestVersion(1))
	assert.False(t, schemas.IsSupportedManifestVersion(2))
	assert.False(t, schemas.IsSupportedManifestVersion(0))
}

// TestCOV_SCHEMA_017_ManifestEntryValidateCoversEveryGuard drives each guard
// of ManifestEntry.Validate directly with a valid base record mutated one
// field at a time.
func TestCOV_SCHEMA_017_ManifestEntryValidateCoversEveryGuard(t *testing.T) {
	valid := schemas.ManifestEntry{
		Path:     "docs/payments.md",
		Mode:     covPtr(schemas.DocModeAuto),
		Sources:  []string{"src/payments/refund.ts"},
		Triggers: []string{"src/payments/types.ts"},
		Ignore:   []string{"docs/payments.md#generated"},
	}
	assert.Empty(t, valid.Validate())

	badPath := valid
	badPath.Path = "/etc/passwd"
	issues := badPath.Validate()
	require.NotNil(t, covIssueAt(issues, "path"))
	assert.Equal(t,
		"document path must be a nonempty relative POSIX path without '..' segments or backslashes",
		covIssueAt(issues, "path").Message)

	badMode := valid
	badMode.Mode = covPtr(schemas.DocMode("hybrid"))
	issues = badMode.Validate()
	require.NotNil(t, covIssueAt(issues, "mode"))
	assert.Equal(t,
		"Invalid enum value. Expected 'auto' | 'suggest' | 'locked' | 'generated' | 'human', received 'hybrid'",
		covIssueAt(issues, "mode").Message)

	noSources := valid
	noSources.Sources = nil
	issues = noSources.Validate()
	require.NotNil(t, covIssueAt(issues, "sources"))
	assert.Equal(t, "Array must contain at least 1 element(s)", covIssueAt(issues, "sources").Message)

	emptySource := valid
	emptySource.Sources = []string{""}
	require.NotNil(t, covIssueAt(emptySource.Validate(), "sources", "0"))

	emptyTrigger := valid
	emptyTrigger.Triggers = []string{""}
	require.NotNil(t, covIssueAt(emptyTrigger.Validate(), "triggers", "0"))

	emptyIgnore := valid
	emptyIgnore.Ignore = []string{""}
	require.NotNil(t, covIssueAt(emptyIgnore.Validate(), "ignore", "0"))
}

// TestCOV_SCHEMA_018_ManifestDefaultsAndRootValidate covers the defaults-mode
// guard and the root manifest guards (version, documents, defaults wiring).
func TestCOV_SCHEMA_018_ManifestDefaultsAndRootValidate(t *testing.T) {
	validDefaults := schemas.ManifestDefaults{Mode: covPtr(schemas.DocModeSuggest)}
	assert.Empty(t, validDefaults.Validate())

	nilDefaults := schemas.ManifestDefaults{}
	assert.Empty(t, nilDefaults.Validate())

	badDefaults := schemas.ManifestDefaults{Mode: covPtr(schemas.DocMode("sentient"))}
	issues := badDefaults.Validate()
	require.NotNil(t, covIssueAt(issues, "mode"))
	assert.Equal(t, "Invalid enum value for defaults.mode", covIssueAt(issues, "mode").Message)

	validRoot := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/payments.md", Sources: []string{"src/payments/refund.ts"}},
		},
		Defaults: &schemas.ManifestDefaults{Mode: covPtr(schemas.DocModeHuman)},
	}
	assert.Empty(t, validRoot.Validate())

	badVersion := validRoot
	badVersion.Version = 0
	require.NotNil(t, covIssueAt(badVersion.Validate(), "version"))

	noDocs := validRoot
	noDocs.Documents = nil
	require.NotNil(t, covIssueAt(noDocs.Validate(), "documents"))

	badDefaultsRoot := validRoot
	badDefaultsRoot.Defaults = &schemas.ManifestDefaults{Mode: covPtr(schemas.DocMode("sentient"))}
	nested := badDefaultsRoot.Validate()
	require.NotNil(t, covIssueAt(nested, "defaults", "mode"))
	assert.Equal(t, "Invalid enum value for defaults.mode", covIssueAt(nested, "defaults", "mode").Message)

	badDoc := validRoot
	badDoc.Documents = []schemas.ManifestEntry{{Path: "/abs", Sources: nil}}
	nested = badDoc.Validate()
	require.NotNil(t, covIssueAt(nested, "documents", "0", "path"))
	require.NotNil(t, covIssueAt(nested, "documents", "0", "sources"))
}

// TestCOV_SCHEMA_019_ParseManifestYAMLAndStrictness covers ParseManifest's
// YAML bridge (accepting YAML wire bytes) and its two rejections.
func TestCOV_SCHEMA_019_ParseManifestYAMLAndStrictness(t *testing.T) {
	validYAML := []byte("version: 1\ndocuments:\n  - path: docs/payments.md\n    sources:\n      - src/payments/refund.ts\n")
	parsed, err := schemas.ParseManifest(validYAML)
	require.NoError(t, err)
	assert.Equal(t, 1, parsed.Version)
	require.Len(t, parsed.Documents, 1)
	assert.Equal(t, "docs/payments.md", parsed.Documents[0].Path)

	brokenYAML := []byte("version: 1\ndocuments: [oops\n")
	_, err = schemas.ParseManifest(brokenYAML)
	require.Error(t, err)

	invalidSemantics := []byte("version: 0\ndocuments:\n  - path: docs/payments.md\n    sources:\n      - src/payments/refund.ts\n")
	_, err = schemas.ParseManifest(invalidSemantics)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version: Number must be greater than 0")
}

// TestCOV_SCHEMA_020_StateParsersDriveUnexportedValidators drives the state
// artifact parsers so their unexported validators run every guard.
func TestCOV_SCHEMA_020_StateParsersDriveUnexportedValidators(t *testing.T) {
	baseline := []byte(`{"doc_path":"docs/payments.md","doc_content_hash":"sha256:abc","baseline_commit":"8a73f2c",` +
		`"source_files":["src/payments/refund.ts"],"source_symbols":["refundRequest"],"claim_ids":["claim_182"],` +
		`"verified_at":"2026-10-04T22:31:00Z","verified_by_run":"run_01J9ZK6X8A","policy_version":17}`)
	parsedBaseline, err := schemas.ParseBaseline(baseline)
	require.NoError(t, err)
	assert.Equal(t, "docs/payments.md", parsedBaseline.DocPath)

	badBaseline := []byte(`{"doc_path":"","doc_content_hash":"","baseline_commit":"8a73f2c",` +
		`"source_files":[""],"source_symbols":[],"claim_ids":[],` +
		`"verified_at":"nope","verified_by_run":"","policy_version":0}`)
	_, err = schemas.ParseBaseline(badBaseline)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "doc_path: String must contain at least 1 character(s)")
	assert.Contains(t, err.Error(), "verified_by_run: String must contain at least 1 character(s)")
	assert.Contains(t, err.Error(), "verified_at: Invalid datetime")
	assert.Contains(t, err.Error(), "policy_version: Number must be greater than 0")

	claim := []byte(`{"id":"claim_182","text":"Refunds require an idempotency key.",` +
		`"anchor":{"file":"docs/payments.md","start_line":42,"end_line":42,"content_hash":"1f2e3d"},` +
		`"source":{"file":"src/payments/refund.ts","line":42,"blob_sha":"e0d1fa5"},` +
		`"concept":"refunds","status":"verified","history":[{"at":"2026-10-04T22:31:00Z","run_id":"run_1","event":"verified"}]}`)
	parsedClaim, err := schemas.ParseClaim(claim)
	require.NoError(t, err)
	assert.Equal(t, "claim_182", parsedClaim.ID)

	badClaim := []byte(`{"id":"","text":"",` +
		`"anchor":{"file":"","start_line":0,"end_line":0,"content_hash":""},` +
		`"source":{"file":"","line":0,"blob_sha":""},"concept":"","status":"nope",` +
		`"history":[{"at":"nope","run_id":"","event":""}]}`)
	_, err = schemas.ParseClaim(badClaim)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status: Invalid enum value. Expected")
	assert.Contains(t, err.Error(), "anchor.file: String must contain at least 1 character(s)")
	assert.Contains(t, err.Error(), "source.blob_sha: String must contain at least 1 character(s)")
	assert.Contains(t, err.Error(), "history.0.at: Invalid datetime")
	assert.Contains(t, err.Error(), "history.0.run_id: String must contain at least 1 character(s)")
	assert.Contains(t, err.Error(), "anchor.start_line: Number must be greater than or equal to 1")

	anchorIssue := badClaim
	_ = anchorIssue

	cursor := []byte(`{"position":"git:81af92e","updated_at":"2026-10-04T22:31:00Z"}`)
	parsedCursor, err := schemas.ParseAuditCursor(cursor)
	require.NoError(t, err)
	assert.Equal(t, "git:81af92e", parsedCursor.Position)

	badCursor := []byte(`{"position":"","updated_at":"nope"}`)
	_, err = schemas.ParseAuditCursor(badCursor)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updated_at: Invalid datetime")

	policy := []byte(`{"schema":"docsync.policy/v1","version":17,"pointer":"policy-history/v17.yml","hash":"sha256:e3b0c"}`)
	parsedPolicy, err := schemas.ParsePolicyPointer(policy)
	require.NoError(t, err)
	assert.Equal(t, 17, parsedPolicy.Version)

	badPolicy := []byte(`{"schema":"docsync.policy/v2","version":0,"pointer":""}`)
	_, err = schemas.ParsePolicyPointer(badPolicy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema: Invalid literal value, expected \"docsync.policy/v1\"")
	assert.Contains(t, err.Error(), "version: Number must be greater than 0")

	models := []byte(`{"schema":"docsync.models/v1","version":1,"roles":{"judge":{"model":"z-ai/glm-4.7","fallbacks":["openai/gpt-4.1-mini"]}}}`)
	parsedModels, err := schemas.ParseModelsConfig(models)
	require.NoError(t, err)
	assert.Len(t, parsedModels.Roles, 1)

	badModels := []byte(`{"schema":"docsync.models/v2","version":0,"roles":{"":{"model":"","fallbacks":[""]}}}`)
	_, err = schemas.ParseModelsConfig(badModels)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema: Invalid literal value, expected \"docsync.models/v1\"")
	assert.Contains(t, err.Error(), "version: Number must be greater than 0")
	assert.Contains(t, err.Error(), "model: String must contain at least 1 character(s)")
}

// TestCOV_SCHEMA_021_DocStateParsersDriveVerificationLedger drives
// validateDocState through ParseDocState, including the append-only
// verification ledger and the disagreement log.
func TestCOV_SCHEMA_021_DocStateParsersDriveVerificationLedger(t *testing.T) {
	valid := []byte(`{"path":"docs/payments.md","state":"SUSPECT","since":"2026-10-04T22:30:44Z",` +
		`"policy_version":17,"evidence_refs":["analysis/01"],` +
		`"transitions":[{"state":"SUSPECT","at":"2026-10-04T22:30:44Z","run_id":"run_1","reason":"mapped sources changed"}],` +
		`"verifications":[{"report_hash":"cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64",` +
		`"at":"2026-10-04T22:30:47Z","run_id":"run_1",` +
		`"report":{"schema":"docsync.verification-report/v1","run_id":"run_1","doc_path":"docs/payments.md","patch_ref":"state/docs/payments.md#p1",` +
		`"tiers":[{"tier":1,"status":"pass","checks":[]},{"tier":2,"status":"pass","checks":[]},{"tier":3,"status":"pass","checks":[]}],` +
		`"claims":[],"verdict":"pass","auto_eligible":true,"auto_block_reasons":[],"policy_version":17}}],` +
		`"disagreements":[{"at":"2026-10-04T22:31:00Z","run_id":"run_1","verifier_verdict":"reject","human_outcome":"accepted","reason":"human judged the doc correct"}]}`)
	parsed, err := schemas.ParseDocState(valid)
	require.NoError(t, err)
	assert.Equal(t, schemas.DocStateSuspect, parsed.State)

	badVerification := []byte(`{"path":"docs/payments.md","state":"NOPE","since":"nope","last_run":"",` +
		`"policy_version":0,"evidence_refs":[""],` +
		`"transitions":[{"state":"NOPE","at":"nope","run_id":"","reason":""}],` +
		`"verifications":[{"report_hash":"sha256:cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64",` +
		`"at":"nope","run_id":"",` +
		`"report":{"schema":"docsync.verification-report/v1","run_id":"run_1","doc_path":"docs/payments.md","patch_ref":"p1","tiers":[],` +
		`"claims":[],"verdict":"pass","auto_eligible":true,"auto_block_reasons":[],"policy_version":17}}],` +
		`"disagreements":[{"at":"nope","run_id":"","verifier_verdict":"nope","human_outcome":"nope","reason":"","patch_ref":""}]}`)
	_, err = schemas.ParseDocState(badVerification)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state: Invalid enum value. Expected")
	assert.Contains(t, err.Error(), "report_hash: report_hash must be bare 64-hex sha256 (no sha256: prefix)")
	assert.Contains(t, err.Error(), "tiers: tiers must contain exactly three entries")
	assert.Contains(t, err.Error(), "human_outcome: Invalid enum value. Expected 'accepted' | 'edited' | 'rejected' | 'intentional' | 'ignored', received 'nope'")
	assert.Contains(t, err.Error(), "transitions.0.at: Invalid datetime")
}
