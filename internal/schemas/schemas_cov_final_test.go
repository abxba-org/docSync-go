package schemas_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCOV_SCHEMA_045_BehavioralProxyCoverageValidators covers every guard of
// the A5 coverage report chain: case records, language rows, declared gaps,
// the summary counters and the root record.
func TestCOV_SCHEMA_045_BehavioralProxyCoverageValidators(t *testing.T) {
	report := schemas.BehavioralProxyCoverageReportExample
	assert.Empty(t, report.Validate())

	validCase := schemas.AuditCaseRecord{
		CaseID:       "case_signature",
		Language:     "typescript",
		Truth:        schemas.ProxyTruthBehavioral,
		ProxyFired:   true,
		FiredProxies: []schemas.ProxyKind{schemas.ProxyKindSignature, schemas.ProxyKindConfig},
		Outcome:      schemas.AuditOutcomeProxyHit,
	}
	assert.Empty(t, validCase.Validate())

	badCase := schemas.AuditCaseRecord{
		CaseID:       "",
		Language:     "",
		Truth:        schemas.BehavioralProxyTruth("semantic"),
		FiredProxies: []schemas.ProxyKind{"telemetry"},
		Outcome:      schemas.AuditOutcome("skipped"),
	}
	issues := badCase.Validate()
	require.NotNil(t, covIssueAt(issues, "case_id"))
	require.NotNil(t, covIssueAt(issues, "language"))
	assert.Contains(t, covIssueAt(issues, "truth").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "fired_proxies", "0"))
	assert.Contains(t, covIssueAt(issues, "outcome").Message, "Invalid enum value. Expected 'proxy-hit' | 'proxy-miss' | 'false-positive' | 'true-non-behavioral' | 'review'")

	noProxies := validCase
	noProxies.FiredProxies = nil
	assert.Empty(t, noProxies.Validate())

	validGap := schemas.AuditDeclaredGap{CaseID: "case_1", Detail: "no signature proxy covers this error path"}
	assert.Empty(t, validGap.Validate())

	badGap := schemas.AuditDeclaredGap{CaseID: "", Detail: ""}
	issues = badGap.Validate()
	require.NotNil(t, covIssueAt(issues, "case_id"))
	require.NotNil(t, covIssueAt(issues, "detail"))

	validRow := schemas.AuditLanguageRow{
		Language:              "typescript",
		AnalyzedCases:         3,
		BehaviorRelevantCases: 2,
		ProxyHits:             2,
		Recall:                1,
		FalsePositiveRate:     0.5,
		DeclaredGaps:          []schemas.AuditDeclaredGap{{CaseID: "case_1", Detail: "gap"}},
		ReviewRouted:          []string{"case_2"},
	}
	assert.Empty(t, validRow.Validate())

	badRow := schemas.AuditLanguageRow{
		Language:              "",
		AnalyzedCases:         -1,
		BehaviorRelevantCases: -2,
		ProxyHits:             -3,
		Recall:                -0.1,
		FalsePositives:        -1,
		FalsePositiveRate:     -0.5,
		DeclaredGaps:          []schemas.AuditDeclaredGap{{}},
		ReviewRouted:          []string{""},
	}
	issues = badRow.Validate()
	require.NotNil(t, covIssueAt(issues, "language"))
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "analyzed_cases").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "behavior_relevant_cases").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "proxy_hits").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "recall").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "false_positives").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "false_positive_rate").Message)
	require.NotNil(t, covIssueAt(issues, "declared_gaps", "0", "case_id"))
	require.NotNil(t, covIssueAt(issues, "review_routed", "0"))

	highRow := validRow
	highRow.Recall = 1.5
	highRow.FalsePositiveRate = 1.5
	issues = highRow.Validate()
	assert.Equal(t, "Number must be less than or equal to 1", covIssueAt(issues, "recall").Message)
	assert.Equal(t, "Number must be less than or equal to 1", covIssueAt(issues, "false_positive_rate").Message)

	badSummary := schemas.AuditSummary{Cases: -1, BehaviorRelevant: -1, ProxyHits: -1, FalsePositives: -1, DeclaredGaps: -1, ReviewRouted: -1}
	issues = badSummary.Validate()
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "cases").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "behavior_relevant").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "proxy_hits").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "false_positives").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "declared_gaps").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "review_routed").Message)

	brokenRoot := schemas.BehavioralProxyCoverageReport{
		Schema:        "",
		CorpusID:      "",
		At:            "nope",
		RecallFloor:   -0.1,
		DataSource:    schemas.AuditDataSource("audit-log"),
		Languages:     []schemas.AuditLanguageRow{{}},
		Cases:         []schemas.AuditCaseRecord{{}},
		Summary:       schemas.AuditSummary{},
		Verdict:       schemas.AuditVerdict("pass-with-notes"),
		VerdictReason: "",
	}
	issues = brokenRoot.Validate()
	assert.Equal(t, "Required", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "corpus_id"))
	assert.Contains(t, covIssueAt(issues, "at").Message, "Invalid datetime")
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "recall_floor").Message)
	require.NotNil(t, covIssueAt(issues, "data_source"))
	assert.Contains(t, covIssueAt(issues, "data_source").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "languages", "0", "language"))
	require.NotNil(t, covIssueAt(issues, "cases", "0", "case_id"))
	require.NotNil(t, covIssueAt(issues, "verdict"))
	assert.Contains(t, covIssueAt(issues, "verdict").Message,
		"Invalid enum value. Expected 'pass' | 'pass-with-declared-gaps' | 'fail'")
	require.NotNil(t, covIssueAt(issues, "verdict_reason"))

	highFloor := report
	highFloor.RecallFloor = 1.5
	assert.Equal(t, "Number must be less than or equal to 1",
		covIssueAt(highFloor.Validate(), "recall_floor").Message)

	assert.False(t, schemas.IsValidProxyKind("nope"))
	assert.True(t, schemas.IsValidProxyKind(string(schemas.ProxyKindConfig)))
	assert.False(t, schemas.IsValidBehavioralProxyTruth("nope"))
	assert.True(t, schemas.IsValidBehavioralProxyTruth(string(schemas.ProxyTruthNonBehavioral)))
	assert.False(t, schemas.IsValidAuditOutcome("nope"))
	assert.True(t, schemas.IsValidAuditOutcome(string(schemas.AuditOutcomeFalsePositive)))
	assert.False(t, schemas.IsValidAuditVerdict("nope"))
	assert.True(t, schemas.IsValidAuditVerdict(string(schemas.AuditVerdictFail)))
	assert.False(t, schemas.IsValidAuditDataSource("nope"))
	assert.True(t, schemas.IsValidAuditDataSource(string(schemas.AuditDataSourceRepository)))
}

// TestCOV_SCHEMA_046_ReplacementEvaluationRefinements covers the root guards
// and the fail-closed invariants of the replacement-model shadow evaluation.
func TestCOV_SCHEMA_046_ReplacementEvaluationRefinements(t *testing.T) {
	assert.Empty(t, schemas.ReplacementEvaluationReportExample.Validate())

	broken := schemas.ReplacementEvaluationReportExample
	broken.Schema = ""
	broken.Kind = ""
	broken.Role = ""
	broken.Repo = ""
	broken.RunID = ""
	broken.Cases = nil
	broken.MinAgreementRate = -0.1
	broken.Verdict = "unknown"
	broken.PortID = ""
	broken.CreatedAt = "nope"
	broken.ExpiresAt = "nope"
	issues := broken.Validate()
	assert.Equal(t, "Required", covIssueAt(issues, "schema").Message)
	assert.Equal(t, "Required", covIssueAt(issues, "kind").Message)
	require.NotNil(t, covIssueAt(issues, "role"))
	require.NotNil(t, covIssueAt(issues, "repo"))
	require.NotNil(t, covIssueAt(issues, "run_id"))
	assert.Equal(t, "Array must contain at least 1 element(s)", covIssueAt(issues, "cases").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "min_agreement_rate").Message)
	require.NotNil(t, covIssueAt(issues, "verdict"))
	assert.Contains(t, covIssueAt(issues, "verdict").Message,
		"Invalid enum value. Expected 'pass' | 'fail'")
	require.NotNil(t, covIssueAt(issues, "port_id"))
	assert.Contains(t, covIssueAt(issues, "created_at").Message, "Invalid datetime")
	assert.Contains(t, covIssueAt(issues, "expires_at").Message, "Invalid datetime")

	tooHighRate := schemas.ReplacementEvaluationReportExample
	tooHighRate.MinAgreementRate = 1.5
	assert.Equal(t, "Number must be less than or equal to 1",
		covIssueAt(tooHighRate.Validate(), "min_agreement_rate").Message)

	// Both bindings must differ: primaries and policy versions.
	samePrimaries := schemas.ReplacementEvaluationReportExample
	samePrimaries.Bindings.New.Primary = samePrimaries.Bindings.Old.Primary
	assert.True(t, covHasMessage(samePrimaries.Validate(),
		"a replacement evaluation compares TWO bindings — the old and new primaries must differ"))

	sameVersions := schemas.ReplacementEvaluationReportExample
	sameVersions.Policy.CandidateVersion = sameVersions.Policy.ProductionVersion
	assert.True(t, covHasMessage(sameVersions.Validate(),
		"a replacement evaluation compares TWO policy versions — production and candidate must differ (R2)"))

	// The agreement block must be internally consistent and reconcile the cases.
	bindingBroken := schemas.ReplacementEvaluationReportExample
	bindingBroken.Bindings.New.Primary = ""
	issues = bindingBroken.Validate()
	require.NotNil(t, covIssueAt(issues, "bindings", "new", "primary"))

	badFallback := schemas.ReplacementEvaluationReportExample
	badFallback.Bindings.Old.Fallbacks = []string{""}
	require.NotNil(t, covIssueAt(badFallback.Validate(), "bindings", "old", "fallbacks", "0"))

	// Refinement only runs on a structurally valid record.
	inconsistent := schemas.ReplacementEvaluationReportExample
	inconsistent.Agreement.Disagreed = 1
	inconsistent.Agreement.Total = 5
	issues = inconsistent.Validate()
	assert.True(t, covHasMessage(issues,
		"the agreement block must be internally consistent — agreed + disagreed === total"))
	assert.True(t, covHasMessage(issues,
		"the agreement total must equal the number of recorded cases"))

	// NOTE: the example is a package-level var, so its Cases slice is shared.
	// Rebuild the slice (never mutate an element in place) to keep the
	// example pristine for the rest of the package's tests.
	caseBroken := schemas.ReplacementEvaluationReportExample
	caseBroken.Cases = append([]schemas.ReplacementEvalCase(nil), caseBroken.Cases...)
	caseBroken.Cases[0].CaseID = ""
	caseBroken.Cases[0].RiskBucket = "extreme"
	caseBroken.Cases[0].OldOutcome.AbstainReason = new(schemas.AbstainReason("nope"))
	caseBroken.Cases[0].InputHashes.ChangeAnalysis = "xyz"
	issues = caseBroken.Validate()
	require.NotNil(t, covIssueAt(issues, "cases", "0", "caseId"))
	require.NotNil(t, covIssueAt(issues, "cases", "0", "riskBucket"))
	assert.Contains(t, covIssueAt(issues, "cases", "0", "riskBucket").Message, "Invalid enum value. Expected")
	require.NotNil(t, covIssueAt(issues, "cases", "0", "old_outcome", "abstain_reason"))
	require.NotNil(t, covIssueAt(issues, "cases", "0", "input_hashes", "change_analysis"))
	assert.Equal(t, "Invalid", covIssueAt(issues, "cases", "0", "input_hashes", "change_analysis").Message)

	shadowDecisionBroken := schemas.ReplacementEvaluationReportExample
	shadowDecisionBroken.Cases = append([]schemas.ReplacementEvalCase(nil), shadowDecisionBroken.Cases...)
	shadowDecisionBroken.Cases[0].NewOutcome = schemas.ShadowDecision{Action: "nope"}
	require.NotNil(t, covIssueAt(shadowDecisionBroken.Validate(), "cases", "0", "new_outcome", "action"))

	badInputHashes := schemas.ShadowRecordedInputHashes{
		ChangeAnalysis: "not-hex",
		CandidateSet:   "not-hex",
		DiffSubset:     "not-hex",
	}
	issues = badInputHashes.Validate()
	assert.Equal(t, "Invalid", covIssueAt(issues, "change_analysis").Message)
	require.NotNil(t, covIssueAt(issues, "change_analysis"))
	require.NotNil(t, covIssueAt(issues, "candidate_set"))
	require.NotNil(t, covIssueAt(issues, "diff_subset"))

	negativePolicy := schemas.ReplacementEvaluationReportExample
	negativePolicy.Policy.ProductionVersion = 0
	negativePolicy.Policy.CandidateVersion = 0
	issues = negativePolicy.Validate()
	require.NotNil(t, covIssueAt(issues, "policy", "production_version"))
	require.NotNil(t, covIssueAt(issues, "policy", "candidate_version"))

	negativeAgreement := schemas.ReplacementEvaluationReportExample
	negativeAgreement.Agreement.Total = -1
	negativeAgreement.Agreement.Agreed = -1
	negativeAgreement.Agreement.Disagreed = -1
	negativeAgreement.Agreement.Rate = -0.5
	issues = negativeAgreement.Validate()
	require.NotNil(t, covIssueAt(issues, "agreement", "total"))
	require.NotNil(t, covIssueAt(issues, "agreement", "agreed"))
	require.NotNil(t, covIssueAt(issues, "agreement", "disagreed"))
	require.NotNil(t, covIssueAt(issues, "agreement", "rate"))

	highAgreementRate := schemas.ReplacementEvaluationReportExample
	highAgreementRate.Agreement.Rate = 1.5
	assert.Equal(t, "Number must be less than or equal to 1",
		covIssueAt(highAgreementRate.Validate(), "agreement", "rate").Message)

	// A 'pass' verdict requires the rate to clear the minimum.
	failingPass := schemas.ReplacementEvaluationReportExample
	failingPass.Agreement.Rate = 0.85
	failingPass.MinAgreementRate = 0.9
	issues = failingPass.Validate()
	assert.True(t, covHasMessage(issues, "a 'pass' verdict requires an agreement rate at or above the minimum"))

	// A 'fail' verdict requires the rate to sit below the minimum.
	failingFail := schemas.ReplacementEvaluationReportExample
	failingFail.Verdict = "fail"
	failingFail.MinAgreementRate = 0.5
	assert.True(t, covHasMessage(failingFail.Validate(),
		"a 'fail' verdict requires an agreement rate below the minimum"))

	// expires_at must be strictly AFTER created_at.
	expired := schemas.ReplacementEvaluationReportExample
	expired.ExpiresAt = expired.CreatedAt
	assert.True(t, covHasMessage(expired.Validate(),
		"expires_at must be strictly after created_at (time-boxed canary)"))

	unparsableWindow := expired
	unparsableWindow.CreatedAt = "not-a-datetime"
	unparsableWindow.ExpiresAt = "also-not"
	assert.False(t, covHasMessage(unparsableWindow.Validate(),
		"expires_at must be strictly after created_at (time-boxed canary)"))
}

// TestCOV_SCHEMA_047_OrgDriftReportValidators covers the org-level advisory
// report chain: the repo row, the period, the cross-repo concept, the summary
// and the root guards.
func TestCOV_SCHEMA_047_OrgDriftReportValidators(t *testing.T) {
	report := schemas.OrgDriftReportExample
	assert.Empty(t, report.Validate())

	badRepo := schemas.OrgDriftRepo{Repo: "", FindingsCount: -1, DocsAffected: -1, RunID: "", At: "nope"}
	issues := badRepo.Validate()
	require.NotNil(t, covIssueAt(issues, "repo"))
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "findings_count").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "docs_affected").Message)
	require.NotNil(t, covIssueAt(issues, "run_id"))
	assert.Contains(t, covIssueAt(issues, "at").Message, "Invalid datetime")

	badPeriod := schemas.OrgDriftPeriod{From: "nope", To: "nope"}
	issues = badPeriod.Validate()
	require.NotNil(t, covIssueAt(issues, "from"))
	require.NotNil(t, covIssueAt(issues, "to"))

	singleRepo := schemas.OrgCrossRepoConcept{Concept: "c", Kind: "support-moved", Repos: []string{"acme/widgets"}, Occurrences: 5}
	issues = singleRepo.Validate()
	assert.Equal(t, "Array must contain at least 2 element(s)", covIssueAt(issues, "repos").Message)

	badConcept := schemas.OrgCrossRepoConcept{Concept: "", Kind: "", Repos: []string{"acme/widgets", ""}, Occurrences: 1}
	issues = badConcept.Validate()
	require.NotNil(t, covIssueAt(issues, "concept"))
	require.NotNil(t, covIssueAt(issues, "kind"))
	require.NotNil(t, covIssueAt(issues, "repos", "1"))
	assert.Equal(t, "Number must be greater than or equal to 2", covIssueAt(issues, "occurrences").Message)

	badSummary := schemas.OrgDriftSummary{Repositories: -1, Findings: -1, CrossRepoConcepts: -1}
	issues = badSummary.Validate()
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "repositories").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "findings").Message)
	assert.Equal(t, "Number must be greater than or equal to 0", covIssueAt(issues, "cross_repo_concepts").Message)

	brokenRoot := schemas.OrgDriftReport{
		Repos:             []schemas.OrgDriftRepo{{}},
		CrossRepoConcepts: []schemas.OrgCrossRepoConcept{{}},
		Period:            &schemas.OrgDriftPeriod{},
		Advisory:          false,
	}
	issues = brokenRoot.Validate()
	assert.Equal(t, "Required", covIssueAt(issues, "schema").Message)
	require.NotNil(t, covIssueAt(issues, "run_id"))
	require.NotNil(t, covIssueAt(issues, "org"))
	assert.Contains(t, covIssueAt(issues, "generated_at").Message, "Invalid datetime")
	require.NotNil(t, covIssueAt(issues, "period", "from"))
	require.NotNil(t, covIssueAt(issues, "repos", "0", "repo"))
	require.NotNil(t, covIssueAt(issues, "cross_repo_concepts", "0", "concept"))
	assert.Equal(t, "Invalid literal value, expected true", covIssueAt(issues, "advisory").Message)

	noPeriod := schemas.OrgDriftReport{
		Schema: schemas.OrgDriftReportSchemaID, RunID: "r", Org: "acme",
		GeneratedAt: "2026-10-09T03:00:00Z", Advisory: true,
	}
	assert.Empty(t, noPeriod.Validate())

	_, err := schemas.ParseOrgDriftReport([]byte(`{"nope":1}`))
	require.Error(t, err)
}

// TestCOV_SCHEMA_048_YAMLBridgeErrorSurface covers the YAML bridge's failure
// branches: malformed YAML, empty documents, invalid scalar→struct coercions
// and YAML anchors/aliases.
func TestCOV_SCHEMA_048_YAMLBridgeErrorSurface(t *testing.T) {
	// A malformed YAML document fails at the generic decode boundary.
	_, err := schemas.ParseManifest([]byte("version: 1\ndocuments: [oops\n"))
	require.Error(t, err)

	// An empty document yields a zero manifest, which fails validation.
	_, err = schemas.ParseManifest([]byte(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version: Number must be greater than 0")

	// A YAML scalar where the target field wants an integer is a decode error.
	_, err = schemas.ParseManifest([]byte("version: not-a-number\ndocuments: []\n"))
	require.Error(t, err)

	// Anchors and aliases must round-trip through the generic bridge.
	withAnchor := []byte("version: 1\ndocuments:\n  - &shared\n    path: docs/a.md\n    sources:\n      - src/a.ts\n  - *shared\n")
	parsed, err := schemas.ParseManifest(withAnchor)
	require.NoError(t, err)
	require.Len(t, parsed.Documents, 2)
	assert.Equal(t, "docs/a.md", parsed.Documents[1].Path)

	// The strict YAML bridge shares the same failure surface.
	_, err = schemas.ParsePolicyProposal([]byte("schema: docsync.policy-proposal/v1\ncounted_evidence: [oops\n"))
	require.Error(t, err)

	_, err = schemas.ParsePolicyProposal([]byte(""))
	require.Error(t, err)

	// MarshalJSONKeysYAML fails closed on an unmarshalable value.
	_, err = schemas.MarshalJSONKeysYAML(make(chan int))
	require.Error(t, err)

	// Numbers become int64 when they have no fractional part.
	payload := struct {
		Prompt  int     `json:"prompt"`
		Cost    float64 `json:"cost"`
		Enabled bool    `json:"enabled"`
	}{Prompt: 12, Cost: 0.25, Enabled: true}
	out, err := schemas.MarshalJSONKeysYAML(payload)
	require.NoError(t, err)
	var round struct {
		Prompt  int64   `yaml:"prompt"`
		Cost    float64 `yaml:"cost"`
		Enabled bool    `yaml:"enabled"`
	}
	require.NoError(t, yamlUnmarshalForTest(out, &round))
	assert.Equal(t, int64(12), round.Prompt)
	assert.InDelta(t, 0.25, round.Cost, 0.0001)
	assert.True(t, round.Enabled)
}

// yamlUnmarshalForTest decodes the YAML-bridge output (test-only helper).
func yamlUnmarshalForTest(data []byte, out any) error {
	return yaml.Unmarshal(data, out)
}
