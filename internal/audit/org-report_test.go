// Black-box port of packages/core/test/org-drift-report-p12-t15.test.ts
// (P12-T15; ARCH §15.3; CTX §42; OD-R32; V-P12-14).
//
// An org report aggregates across >= 2 fixture repositories; findings are
// advisory only; no write path exists outside advisory artifacts (the module
// is pure — data + markdown strings, nothing else).

package audit_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// generatedAt is the deterministic generation timestamp of the org suite.
const generatedAt = "2026-10-09T03:00:00Z"

// orgMakeFinding is the deterministic finding factory of the org suite (TS
// makeFinding).
func orgMakeFinding(id, docPath, kind string, claimID ...string) audit.DriftFinding {
	finding := audit.DriftFinding{
		ID:      id,
		DocPath: docPath,
		Kind:    kind,
		Evidence: audit.DriftFindingEvidence{
			Commits: []string{"81af92e"},
			Paths:   []string{"src/x.ts"},
			Detail:  "fixture evidence",
		},
		CreatedAt: "2026-10-08T03:00:00Z",
	}
	if len(claimID) > 0 && claimID[0] != "" {
		id := claimID[0]
		finding.ClaimID = &id
	}
	return finding
}

// orgMakeRunRecord is the deterministic run-record factory (TS makeRunRecord).
func orgMakeRunRecord(runID, position string) schemas.DriftAuditRunRecord {
	return schemas.DriftAuditRunRecord{
		Schema:       "docsync.drift-audit-run-record/v1",
		RunID:        runID,
		Trigger:      schemas.DriftAuditTriggerNightlyScheduled,
		At:           "2026-10-08T03:00:00Z",
		Position:     position,
		CursorBefore: nil,
		CursorAfter:  strPtr(position),
		AuditedDocs:  []string{"docs/payments.md"},
		SkippedDocs:  []string{},
		Status:       schemas.DriftAuditRunCompleted,
		Detail:       nil,
		Findings:     []schemas.DriftFindingRecord{},
	}
}

var (
	// widgets and payments are the two fixture repositories (TS WIDGETS /
	// PAYMENTS).
	widgets = audit.OrgRepoAuditInput{
		Repo: "acme/widgets",
		Findings: []audit.DriftFinding{
			orgMakeFinding("f-1", "docs/payments.md", "support-moved", "claim_182"),
			orgMakeFinding("f-2", "docs/api.md", "stale-example"),
		},
		RunRecord: runRecordPtr(orgMakeRunRecord("run_audit_widgets", "81af92e")),
	}
	payments = audit.OrgRepoAuditInput{
		Repo: "acme/payments",
		Findings: []audit.DriftFinding{
			orgMakeFinding("f-3", "docs/refunds.md", "support-moved", "claim_182"),
		},
		RunRecord: runRecordPtr(orgMakeRunRecord("run_audit_payments", "b7c2d3e")),
	}
)

// runRecordPtr returns a pointer to a run record.
func runRecordPtr(record schemas.DriftAuditRunRecord) *schemas.DriftAuditRunRecord {
	return &record
}

// aggregateOrg aggregates the fixture repos (TS `aggregate`).
func aggregateOrg(t *testing.T, repos []audit.OrgRepoAuditInput, org string) schemas.OrgDriftReport {
	t.Helper()
	report, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org:         org,
		RunID:       "run_org_01",
		GeneratedAt: generatedAt,
		Repos:       repos,
	})
	require.NoError(t, err)
	return report
}

// conceptNames extracts the concept ids in order.
func conceptNames(report schemas.OrgDriftReport) []string {
	out := make([]string, 0, len(report.CrossRepoConcepts))
	for _, concept := range report.CrossRepoConcepts {
		out = append(out, concept.Concept)
	}
	return out
}

func TestVP12_14_AggregatesAcrossTwoFixtureRepositories(t *testing.T) {
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{widgets, payments}, "acme")
	require.Equal(t, schemas.OrgDriftReportSchemaID, report.Schema)
	require.Equal(t, 2, report.Summary.Repositories)
	require.Equal(t, 3, report.Summary.Findings)
	// Sorted by repository name (deterministic).
	require.Equal(t, []string{"acme/payments", "acme/widgets"}, repoNames(report))
}

func TestVP12_14_DetectsCrossRepoSharedConcepts(t *testing.T) {
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{widgets, payments}, "acme")
	require.Equal(t, 1, report.Summary.CrossRepoConcepts)
	require.Len(t, report.CrossRepoConcepts, 1)
	concept := report.CrossRepoConcepts[0]
	require.Equal(t, "claim_182", concept.Concept)
	require.Equal(t, "support-moved", concept.Kind)
	require.Equal(t, []string{"acme/payments", "acme/widgets"}, concept.Repos)
	require.Equal(t, 2, concept.Occurrences) // one per repository
}

func TestVP12_14_SingleRepoConceptIsNotCrossRepo(t *testing.T) {
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{widgets, payments}, "acme")
	require.Equal(t, 2, audit.CROSS_REPO_MIN_REPOS)
	require.NotContains(t, conceptNames(report), "claimless:stale-example")
}

func TestVP12_14_ClaimlessFallbackKeepsItsOwnNamespace(t *testing.T) {
	a := audit.OrgRepoAuditInput{
		Repo:     "acme/one",
		Findings: []audit.DriftFinding{orgMakeFinding("f-1", "docs/a.md", "stale-example")},
	}
	b := audit.OrgRepoAuditInput{
		Repo:     "acme/two",
		Findings: []audit.DriftFinding{orgMakeFinding("f-2", "docs/b.md", "stale-example")},
	}
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{a, b}, "acme")
	require.Equal(t, []string{"claimless:stale-example"}, conceptNames(report))
	require.Contains(t, report.Repos[0].RunID, "missing-run-record")
}

func TestVP12_14_ReposWithZeroFindingsStillAppear(t *testing.T) {
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{
		widgets,
		{Repo: "acme/empty", Findings: nil},
	}, "acme")
	var empty *schemas.OrgDriftRepo
	for i := range report.Repos {
		if report.Repos[i].Repo == "acme/empty" {
			empty = &report.Repos[i]
		}
	}
	require.NotNil(t, empty)
	require.Equal(t, 0, empty.FindingsCount)
	require.Equal(t, 0, empty.DocsAffected)
}

func TestVP12_14_ArtifactValidatesAgainstTheRegisteredSchema(t *testing.T) {
	report, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org:         "acme",
		RunID:       "run_org_01",
		GeneratedAt: generatedAt,
		Repos:       []audit.OrgRepoAuditInput{widgets, payments},
		Period: &schemas.OrgDriftPeriod{
			From: "2026-10-02T03:00:00Z",
			To:   generatedAt,
		},
	})
	require.NoError(t, err)
	// The returned artifact parses against the registered schema (period
	// included).
	parsed, parseErr := parseOrgReport(t, report)
	require.NoError(t, parseErr)
	require.Equal(t, report, *parsed)
}

func TestVP12_14_AdvisoryOnlyIsASchemaLevelInvariant(t *testing.T) {
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{widgets, payments}, "acme")
	// A non-advisory variant can never validate.
	forged := report
	forged.Advisory = false
	_, err := parseOrgReport(t, forged)
	require.Error(t, err)
	var validationErr *schemas.ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.True(t, hasIssuePath(validationErr, []string{"advisory"}))
	require.True(t, schemas.OrgDriftReportExample.Advisory)

	// The registry example round-trips through the schema.
	example, err := parseOrgReport(t, schemas.OrgDriftReportExample)
	require.NoError(t, err)
	require.Equal(t, schemas.OrgDriftReportExample, *example)
}

func TestVP12_14_RenderEmitsTheAdvisoryBannerAndProvenance(t *testing.T) {
	report, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org:         "acme",
		RunID:       "run_org_01",
		GeneratedAt: generatedAt,
		Repos:       []audit.OrgRepoAuditInput{widgets, payments},
		Period:      &schemas.OrgDriftPeriod{From: "2026-10-02T03:00:00Z", To: generatedAt},
	})
	require.NoError(t, err)
	markdown := audit.RenderOrgDriftReport(report)
	require.Contains(t, markdown, "ADVISORY")
	require.Contains(t, markdown, "No document in ANY repository was modified")
	require.Contains(t, markdown, "acme/widgets")
	require.Contains(t, markdown, "acme/payments")
	require.Contains(t, markdown, "claim_182")
	require.Contains(t, markdown, "Provenance: report run `run_org_01`")
	require.Contains(t, markdown, "## Org-wide drift report — acme (advisory)")
	require.Contains(t, markdown, "| Repository | Findings | Docs affected | Audit run | Audited at |")
	require.Contains(t, markdown, "| Concept | Kind | Repositories | Occurrences |")
	require.Contains(t, markdown, "_Covered period: 2026-10-02T03:00:00Z → 2026-10-09T03:00:00Z._")
	require.Contains(t, markdown, "generated 2026-10-09T03:00:00Z (P12-T15; ARCH §15.3; OD-R32).")
	// Never a write instruction.
	lower := strings.ToLower(markdown)
	require.NotContains(t, lower, "auto-apply")
	require.NotContains(t, lower, "patch applied")
}

func TestVP12_14_PerRepoIssuesRenderOnlyForActionableFindings(t *testing.T) {
	require.Contains(t, issueText(t, widgets), "ADVISORY")
	// A repo with no findings yields no issue — the caller opens nothing.
	require.Nil(t, audit.RenderPerRepoAdvisoryIssue(audit.OrgRepoAuditInput{
		Repo: "acme/empty", Findings: nil,
	}))
}

func TestVP12_14_FloodCapIsDeclaredInsideTheArtifact(t *testing.T) {
	require.Equal(t, 50, audit.ORG_REPORT_RENDER_CAP)
	many := make([]audit.DriftFinding, 0, audit.ORG_REPORT_RENDER_CAP+3)
	for index := 0; index < audit.ORG_REPORT_RENDER_CAP+3; index++ {
		many = append(many, orgMakeFinding(
			"f-"+itoaTest(index), "docs/doc-"+itoaTest(index)+".md", "stale-example"))
	}
	issue := issueText(t, audit.OrgRepoAuditInput{Repo: "acme/many", Findings: many})
	require.Contains(t, issue, "flood cap 50")
	require.Contains(t, issue, "3 more finding(s) omitted")
}

func TestVP12_14_AggregationIsDeterministicUnderInputOrder(t *testing.T) {
	a := jsonString(t, aggregateOrg(t, []audit.OrgRepoAuditInput{widgets, payments}, "acme"))
	b := jsonString(t, aggregateOrg(t, []audit.OrgRepoAuditInput{payments, widgets}, "acme"))
	require.Equal(t, a, b)
}

func TestVP12_14_FindingsAreDeduplicatedBeforeAggregation(t *testing.T) {
	duplicated := audit.OrgRepoAuditInput{
		Repo: "acme/dupes",
		Findings: []audit.DriftFinding{
			orgMakeFinding("f-1", "docs/a.md", "support-moved", "claim_9"),
			orgMakeFinding("f-2", "docs/a.md", "support-moved", "claim_9"),
		},
	}
	view := audit.DedupeFindings(duplicated.Findings)
	require.Len(t, view, 1)
	// Raw counts are reported; dedup is the engine's job
	// (DedupeFindings/DedupeAgainstStore, not the aggregator's).
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{duplicated}, "acme")
	require.Equal(t, 2, report.Repos[0].FindingsCount)
}

/* ------------------------------------------ fail-closed construction (F4-3) */

func TestF4_3_RefusesNonFullRepoNames(t *testing.T) {
	// The `--repo /x` shape: a leading slash leaves an empty org segment, so
	// the artifact would publish org: '' — violating the registered
	// org: min(1).
	_, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org: "acme", RunID: "run_org_01", GeneratedAt: generatedAt,
		Repos: []audit.OrgRepoAuditInput{{Repo: "/x"}},
	})
	require.Error(t, err)
	var inputErr *audit.OrgReportInputError
	require.ErrorAs(t, err, &inputErr)
	require.Equal(t, audit.EDOCSYNC_ORG_REPORT_INPUT, inputErr.Code())
	require.Contains(t, inputErr.Error(), "EDOCSYNC_ORG_REPORT_INPUT: ")
	require.Contains(t, inputErr.Error(), "is not a full name")

	_, err = audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org: "acme", RunID: "run_org_01", GeneratedAt: generatedAt,
		Repos: []audit.OrgRepoAuditInput{{Repo: "bare"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not a full name")

	_, err = audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org: "acme", RunID: "run_org_01", GeneratedAt: generatedAt,
		Repos: []audit.OrgRepoAuditInput{{Repo: "acme/"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not a full name")

	// A full org/name is still accepted (no false refusal).
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{widgets}, "acme")
	require.Equal(t, "acme/widgets", report.Repos[0].Repo)
}

func TestF4_3_AssembledReportIsValidatedBeforeReturning(t *testing.T) {
	// An org the caller cannot fill honestly never becomes an artifact.
	_, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org: "", RunID: "run_org_01", GeneratedAt: generatedAt,
		Repos: []audit.OrgRepoAuditInput{widgets, payments},
	})
	require.Error(t, err)
	var inputErr *audit.OrgReportInputError
	require.ErrorAs(t, err, &inputErr)
	require.Contains(t, err.Error(), `failed docsync.org-drift-report/v1 validation`)

	// Every emitted artifact validates against the registered schema.
	_, err = parseOrgReport(t, aggregateOrg(t, []audit.OrgRepoAuditInput{widgets, payments}, "acme"))
	require.NoError(t, err)
}

/* ------------------------- deterministic kind + collision-free claimless */

func TestF4_4_KindDivergenceRecordsTheSmallestKind(t *testing.T) {
	// DOCUMENTED RULE: kind divergence resolves to the LEXICOGRAPHICALLY
	// SMALLEST kind (deterministic under any input order — never
	// first-seen-wins).
	unanchored := audit.OrgRepoAuditInput{
		Repo:     "acme/one",
		Findings: []audit.DriftFinding{orgMakeFinding("f-1", "docs/a.md", "claim-unanchored", "claim_182")},
	}
	lostSupport := audit.OrgRepoAuditInput{
		Repo:     "acme/two",
		Findings: []audit.DriftFinding{orgMakeFinding("f-2", "docs/b.md", "claim-lost-support", "claim_182")},
	}
	forward := aggregateOrg(t, []audit.OrgRepoAuditInput{unanchored, lostSupport}, "acme")
	reversed := aggregateOrg(t, []audit.OrgRepoAuditInput{lostSupport, unanchored}, "acme")
	// Input order must not decide the recorded kind.
	require.Equal(t, forward, reversed)
	require.Equal(t, []schemas.OrgCrossRepoConcept{{
		Concept:     "claim_182",
		Kind:        "claim-lost-support", // smallest of the two kinds
		Repos:       []string{"acme/one", "acme/two"},
		Occurrences: 2,
	}}, forward.CrossRepoConcepts)
}

func TestF4_4_AClaimIdNamedKindIsLegalAndNeverCollides(t *testing.T) {
	// G3-2: production claim ids come from `.docsync/concepts.yml`, where
	// `id: kind:refund` is legal human-authored data. The claimless fallback
	// lives in its OWN `claimless:` namespace, so the two identities coexist
	// as DISTINCT concepts and no legal claim id can collide with the
	// fallback.
	named := audit.OrgRepoAuditInput{
		Repo: "acme/one",
		Findings: []audit.DriftFinding{
			orgMakeFinding("f-1", "docs/a.md", "stale-example", "kind:stale-example"),
			orgMakeFinding("f-2", "docs/a.md", "stale-example"),
		},
	}
	namedAgain := audit.OrgRepoAuditInput{
		Repo: "acme/two",
		Findings: []audit.DriftFinding{
			orgMakeFinding("f-3", "docs/b.md", "stale-example", "kind:stale-example"),
			orgMakeFinding("f-4", "docs/b.md", "stale-example"),
		},
	}
	// ACCEPTED: no OrgReportInputError, no refusal — the run completes.
	report := aggregateOrg(t, []audit.OrgRepoAuditInput{named, namedAgain}, "acme")
	require.Equal(t, []string{"claimless:stale-example", "kind:stale-example"}, conceptNames(report))
	for _, concept := range report.CrossRepoConcepts {
		require.Equal(t, 2, concept.Occurrences)
		require.Equal(t, []string{"acme/one", "acme/two"}, concept.Repos)
		require.Equal(t, "stale-example", concept.Kind)
	}

	// The claimless fallback keeps its own namespace, unchanged in behavior.
	three := namedAgain
	three.Repo = "acme/three"
	fallback := aggregateOrg(t, []audit.OrgRepoAuditInput{named, three}, "acme")
	require.Equal(t, []string{"claimless:stale-example", "kind:stale-example"}, conceptNames(fallback))
}

/* ----------------------------------------------------------------- helpers */

// repoNames extracts the repository full names in order.
func repoNames(report schemas.OrgDriftReport) []string {
	out := make([]string, 0, len(report.Repos))
	for _, repo := range report.Repos {
		out = append(out, repo.Repo)
	}
	return out
}

// issueText renders one repo's issue and dereferences it (the TS
// renderPerRepoAdvisoryIssue(...)!.contains).
func issueText(t *testing.T, input audit.OrgRepoAuditInput) string {
	t.Helper()
	issue := audit.RenderPerRepoAdvisoryIssue(input)
	require.NotNil(t, issue)
	return *issue
}

// parseOrgReport validates one report against the registered
// `docsync.org-drift-report/v1` schema (the TS safeParse).
func parseOrgReport(t *testing.T, report schemas.OrgDriftReport) (*schemas.OrgDriftReport, error) {
	t.Helper()
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	return schemas.ParseOrgDriftReport(raw)
}

// jsonString renders one value as JSON (the TS JSON.stringify determinism
// comparison).
func jsonString(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

// itoaTest is the decimal rendering used in fixture ids.
func itoaTest(i int) string {
	return strconv.Itoa(i)
}

// hasIssuePath reports whether a validation error carries an issue at path.
func hasIssuePath(err *schemas.ValidationError, path []string) bool {
	for _, issue := range err.Issues {
		if strings.Join(issue.Path, ".") == strings.Join(path, ".") {
			return true
		}
	}
	return false
}
