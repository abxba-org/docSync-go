// Ported from packages/schemas/src/v1/org-drift-report.ts: org-wide drift
// report — `docsync.org-drift-report/v1` (P12-T15; ARCH §15.3; CTX §42;
// OD-R32).
//
// The org-level advisory artifact of the cross-repository consistency
// surface: a scheduled report aggregating background drift findings across
// the org's repositories (OD-R32 recommends a scheduled report on the org
// policy repository; per-repo issues only for actionable findings). It is
// QUERY data — `advisory` is a schema-level `true` literal so no producer can
// ever emit a non-advisory variant of this artifact (INV-11: findings
// surface, never auto-write).
//
// Cross-repo consistency (CTX §42 "shared-concept/terminology drift") is
// computed, not asserted: a concept (claim id or stable drift kind) appearing
// in >= 2 repositories is surfaced as a `cross_repo_concepts` entry naming the
// affected repositories — evidence for terminology/contract divergence, never
// an instruction to edit anything.
package schemas

// OrgDriftReportSchemaID is the wire literal carried by the `schema` field
// (ORG_DRIFT_REPORT_SCHEMA_ID, z.literal('docsync.org-drift-report/v1')).
const OrgDriftReportSchemaID = "docsync.org-drift-report/v1"

// OrgDriftRepo is one repository's contribution to the org report
// (ORG_DRIFT_REPO_SCHEMA) — counts + provenance.
type OrgDriftRepo struct {
	Repo          string `json:"repo"`
	FindingsCount int    `json:"findings_count"`
	DocsAffected  int    `json:"docs_affected"`
	RunID         string `json:"run_id"`
	At            string `json:"at"`
}

// Validate checks the entry against ORG_DRIFT_REPO_SCHEMA (strict).
func (r *OrgDriftRepo) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(r.Repo, 1, []string{"repo"}))
	if r.FindingsCount < 0 {
		Collect(&issues, &Issue{Path: []string{"findings_count"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.DocsAffected < 0 {
		Collect(&issues, &Issue{Path: []string{"docs_affected"}, Message: "Number must be greater than or equal to 0"})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	return issues
}

// OrgDriftPeriod is the optional covered period of the scheduled report
// (the inline strictObject of ORG_DRIFT_REPORT_SCHEMA).
type OrgDriftPeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Validate checks the period against the inline strictObject (strict).
func (p *OrgDriftPeriod) Validate() []Issue {
	var issues []Issue
	Collect(&issues, datetimeIssue(p.From, []string{"from"}))
	Collect(&issues, datetimeIssue(p.To, []string{"to"}))
	return issues
}

// OrgCrossRepoConcept is one cross-repository shared concept (CTX §42): the
// same claim id (or, when claims are absent, the same stable drift kind)
// surfaced in >= 2 repositories (ORG_CROSS_REPO_CONCEPT_SCHEMA).
type OrgCrossRepoConcept struct {
	Concept     string   `json:"concept"`
	Kind        string   `json:"kind"`
	Repos       []string `json:"repos"`
	Occurrences int      `json:"occurrences"`
}

// Validate checks the concept against ORG_CROSS_REPO_CONCEPT_SCHEMA (strict),
// including the min(2) guards: a cross-repo concept spans at least two
// repositories and occurred at least twice.
func (c *OrgCrossRepoConcept) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.Concept, 1, []string{"concept"}))
	Collect(&issues, RequiredString(c.Kind, 1, []string{"kind"}))
	if len(c.Repos) < 2 {
		Collect(&issues, &Issue{Path: []string{"repos"}, Message: "Array must contain at least 2 element(s)"})
	}
	for i, repo := range c.Repos {
		Collect(&issues, RequiredString(repo, 1, []string{"repos", itoa(i)}))
	}
	if c.Occurrences < 2 {
		Collect(&issues, &Issue{Path: []string{"occurrences"}, Message: "Number must be greater than or equal to 2"})
	}
	return issues
}

// OrgDriftSummary is the report-wide counters (ORG_DRIFT_SUMMARY_SCHEMA).
type OrgDriftSummary struct {
	Repositories      int `json:"repositories"`
	Findings          int `json:"findings"`
	CrossRepoConcepts int `json:"cross_repo_concepts"`
}

// Validate checks the summary against ORG_DRIFT_SUMMARY_SCHEMA (strict).
func (s *OrgDriftSummary) Validate() []Issue {
	var issues []Issue
	if s.Repositories < 0 {
		Collect(&issues, &Issue{Path: []string{"repositories"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.Findings < 0 {
		Collect(&issues, &Issue{Path: []string{"findings"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.CrossRepoConcepts < 0 {
		Collect(&issues, &Issue{Path: []string{"cross_repo_concepts"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// OrgDriftReport is the `docsync.org-drift-report/v1` advisory artifact
// (ORG_DRIFT_REPORT_SCHEMA).
type OrgDriftReport struct {
	Schema            string                `json:"schema"`
	RunID             string                `json:"run_id"`
	Org               string                `json:"org"`
	GeneratedAt       string                `json:"generated_at"`
	Period            *OrgDriftPeriod       `json:"period,omitempty"`
	Repos             []OrgDriftRepo        `json:"repos"`
	CrossRepoConcepts []OrgCrossRepoConcept `json:"cross_repo_concepts"`
	Summary           OrgDriftSummary       `json:"summary"`
	Advisory          bool                  `json:"advisory"`
}

// Validate checks the report against ORG_DRIFT_REPORT_SCHEMA (strict),
// reproducing the zod issue paths — including the advisory-only invariant:
// `advisory` is a schema-level literal `true` (INV-11), so any non-true value
// is rejected.
func (r *OrgDriftReport) Validate() []Issue {
	var issues []Issue
	if r.Schema != OrgDriftReportSchemaID {
		msg := `Invalid literal value, expected "` + OrgDriftReportSchemaID + `"`
		if r.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	Collect(&issues, RequiredString(r.Org, 1, []string{"org"}))
	Collect(&issues, datetimeIssue(r.GeneratedAt, []string{"generated_at"}))
	if r.Period != nil {
		for _, iss := range r.Period.Validate() {
			iss.Path = append([]string{"period"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Repos {
		for _, iss := range r.Repos[i].Validate() {
			iss.Path = append([]string{"repos", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.CrossRepoConcepts {
		for _, iss := range r.CrossRepoConcepts[i].Validate() {
			iss.Path = append([]string{"cross_repo_concepts", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range r.Summary.Validate() {
		iss.Path = append([]string{"summary"}, iss.Path...)
		issues = append(issues, iss)
	}
	if !r.Advisory {
		Collect(&issues, &Issue{Path: []string{"advisory"}, Message: "Invalid literal value, expected true"})
	}
	return issues
}

// ParseOrgDriftReport strictly parses and validates a
// `docsync.org-drift-report/v1` document, mirroring the zod strictObject
// contract (unknown keys rejected; absent required root keys rejected with
// zod's "Required").
func ParseOrgDriftReport(data []byte) (*OrgDriftReport, error) {
	var r OrgDriftReport
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := requireRootKeys(data,
		"schema", "run_id", "org", "generated_at", "repos", "cross_repo_concepts",
		"summary", "advisory"); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// OrgDriftReportExample is the deterministic example
// (ORG_DRIFT_REPORT_EXAMPLE).
var OrgDriftReportExample = OrgDriftReport{
	Schema:      OrgDriftReportSchemaID,
	RunID:       "run_org_01J9ZMAP",
	Org:         "acme",
	GeneratedAt: "2026-10-09T03:00:00Z",
	Period: &OrgDriftPeriod{
		From: "2026-10-02T03:00:00Z",
		To:   "2026-10-09T03:00:00Z",
	},
	Repos: []OrgDriftRepo{
		{
			Repo:          "acme/widgets",
			FindingsCount: 2,
			DocsAffected:  2,
			RunID:         "run_audit_01J9ZLXM",
			At:            "2026-10-08T03:00:00Z",
		},
		{
			Repo:          "acme/payments",
			FindingsCount: 1,
			DocsAffected:  1,
			RunID:         "run_audit_01J9ZMAP2",
			At:            "2026-10-08T03:00:00Z",
		},
	},
	CrossRepoConcepts: []OrgCrossRepoConcept{
		{
			Concept:     "claim_182",
			Kind:        "support-moved",
			Repos:       []string{"acme/payments", "acme/widgets"},
			Occurrences: 3,
		},
	},
	Summary:  OrgDriftSummary{Repositories: 2, Findings: 3, CrossRepoConcepts: 1},
	Advisory: true,
}
