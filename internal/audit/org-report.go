// Ported from core/src/audit/org-report.ts (P12-T15; ARCH §15.3; CTX §42;
// OD-R32; V-P12-14) — org-wide drift report: aggregation + advisory renderers.
//
// The cross-repository consistency surface. docSync NEVER writes outside a
// repository's own docsync/* branch → PR → human-review path, so an org-level
// artifact can only ever be ADVISORY: this module is pure (no IO, no clocks,
// no models) and produces DATA plus markdown payloads. The scheduled report
// rides the OD-R32 recommendation (a scheduled report on the org policy
// repository; per-repo issues only for actionable findings).
//
// Cross-repo consistency (CTX §42 "shared-concept/terminology drift") is
// COMPUTED: findings sharing a concept identity (claim id preferred, the
// reserved `claimless:<kind>` identity for claimless findings) across >= 2
// repositories surface as cross-repo concepts — the evidence that terminology
// or contracts diverged between repositories. The output never names an edit,
// never ranks blame, and never blocks anything.
//
// INV-11 (no flood): rendering caps per-repo finding rows and per-repo issues
// are produced only for repos whose findings are actionable (non-empty), with
// the cap declared inside the artifact itself — never a silent truncation.
package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ORG_REPORT_RENDER_CAP is the max finding rows rendered per repository inside
// the org report (INV-11).
const ORG_REPORT_RENDER_CAP = 50

// CROSS_REPO_MIN_REPOS is the min repositories a concept must appear in to
// count as cross-repo (CTX §42).
const CROSS_REPO_MIN_REPOS = 2

// EDOCSYNC_ORG_REPORT_INPUT is the machine-readable code of
// OrgReportInputError (TS OrgReportInputError.code).
const EDOCSYNC_ORG_REPORT_INPUT = "EDOCSYNC_ORG_REPORT_INPUT"

// OrgReportInputError is the fail-closed refusal to build an org drift report
// (nothing is emitted): the caller supplied input the artifact cannot
// represent honestly — a repo name that is not a full `org/name` (an empty org
// segment would publish `org: ”`, violating the registered schema's
// `org: min(1)`), or an assembled report that itself fails
// `docsync.org-drift-report/v1` validation.
type OrgReportInputError struct {
	// Message is the exact TS message text.
	Message string

	inner *docutil.Error
}

func newOrgReportInputError(message string) *OrgReportInputError {
	return &OrgReportInputError{Message: message, inner: docutil.New(EDOCSYNC_ORG_REPORT_INPUT, message)}
}

// Error renders the coded error.
func (e *OrgReportInputError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_ORG_REPORT_INPUT.
func (e *OrgReportInputError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is.
func (e *OrgReportInputError) Unwrap() error { return e.inner }

// orgAdvisoryBanner is the org-report advisory banner (verbatim).
const orgAdvisoryBanner = "> **ADVISORY — org-wide drift report (ARCH §15.3; CTX §42).** " +
	"No document in ANY repository was modified by this report. Findings are " +
	"evidence only; remediation routes through each repository\u2019s own SUGGEST " +
	"path (docsync/* branch → PR → human review) — never auto-applied."

// claimlessFallbackPrefix is the reserved namespace prefix for the CLAIMLESS
// concept fallback (display identity only).
const claimlessFallbackPrefix = "claimless:"

// OrgRepoAuditInput is one repository's aggregated audit contribution (input
// of the aggregator).
type OrgRepoAuditInput struct {
	// Repo is the repository full name (`org/name`).
	Repo string
	// Findings are the deduplicated background-audit findings for this
	// repository.
	Findings []DriftFinding
	// RunRecord is the repository's audit run record (provenance; D5; nil
	// when the repo contributed no run record).
	RunRecord *schemas.DriftAuditRunRecord
}

// AggregateOrgDriftReportInput is the input of AggregateOrgDriftReport.
type AggregateOrgDriftReportInput struct {
	// Org is the organization login the report covers.
	Org string
	// RunID is this report's own run id (the scheduled org-report run).
	RunID string
	// GeneratedAt is the generation timestamp (caller-owned clock; ISO-8601).
	GeneratedAt string
	// Period is the covered period for the scheduled report (optional).
	Period *schemas.OrgDriftPeriod
	// Repos are the per-repository contributions (any order; output is
	// deterministic).
	Repos []OrgRepoAuditInput
}

// conceptOf is the concept identity of a finding (CTX §42): the claim id when
// present, else the reserved `claimless:<kind>` fallback namespace.
//
// `claim_id` is a free non-empty string at the schema level (human-authored
// in `.docsync/concepts.yml`), so NO claim id is refused here — the fallback
// namespace is a DISPLAY identity only, and a claim id that coincides with one
// merely shares an advisory row instead of hard-refusing the whole verb over a
// naming quirk.
func conceptOf(finding DriftFinding) string {
	if finding.ClaimID != nil {
		return *finding.ClaimID
	}
	return claimlessFallbackPrefix + finding.Kind
}

// requireRepoFullName refuses a repo name that is not `org/name` with a
// non-empty org segment: `/x` (empty org) would publish a report whose `org`
// is `”` — violating the registered schema's `org: min(1)` — and a bare name
// has no org segment at all.
func requireRepoFullName(repo string) (string, error) {
	slash := strings.IndexByte(repo, '/')
	org := ""
	name := ""
	if slash >= 0 {
		org = repo[:slash]
		name = repo[slash+1:]
	}
	if org == "" || name == "" {
		return "", newOrgReportInputError(fmt.Sprintf(
			"aggregateOrgDriftReport: repo %s is not a full name (org/name) — "+
				"a repository whose org segment is empty is refused (caller input, fail closed)",
			jsonQuote(repo)))
	}
	return repo, nil
}

// conceptRecord is the per-concept aggregation state.
type conceptRecord struct {
	kind        string
	repos       map[string]struct{}
	occurrences int
}

// AggregateOrgDriftReport aggregates per-repository audit findings into the org
// drift report artifact. Pure and deterministic: repos sorted by name,
// cross-repo concepts sorted by (concept, repos). A repo contributing no
// findings still appears (zero counts) so coverage is visible — an absent
// repo is not evidence of clean docs.
//
// FAIL CLOSED AT CONSTRUCTION: the assembled artifact is validated against the
// registered schemas.OrgDriftReportSchemaID before it is returned, so a report
// that would violate the registry schema (e.g. an empty `org`) is never
// emitted; a repo name that is not a full `org/name` is refused as a caller
// fault (see requireRepoFullName). Claim ids are NEVER refused.
//
// DIVERGENCE RULE (documented choice): when the occurrences of ONE concept
// identity disagree on `kind`, the LEXICOGRAPHICALLY SMALLEST kind is recorded
// — deterministic under any input order, unlike first-seen-wins.
func AggregateOrgDriftReport(input AggregateOrgDriftReportInput) (schemas.OrgDriftReport, error) {
	repos := append([]OrgRepoAuditInput{}, input.Repos...)
	sort.SliceStable(repos, func(i, j int) bool {
		return compareStrings(repos[i].Repo, repos[j].Repo) < 0
	})

	repoRows := make([]schemas.OrgDriftRepo, 0, len(repos))
	totalFindings := 0
	for _, entry := range repos {
		repo, err := requireRepoFullName(entry.Repo)
		if err != nil {
			return schemas.OrgDriftReport{}, err
		}
		docs := make(map[string]struct{}, len(entry.Findings))
		for _, finding := range entry.Findings {
			docs[finding.DocPath] = struct{}{}
		}
		runID := fmt.Sprintf("missing-run-record:%s", repo)
		at := input.GeneratedAt
		if entry.RunRecord != nil {
			runID = entry.RunRecord.RunID
			at = entry.RunRecord.At
		}
		repoRows = append(repoRows, schemas.OrgDriftRepo{
			Repo:          repo,
			FindingsCount: len(entry.Findings),
			DocsAffected:  len(docs),
			RunID:         runID,
			At:            at,
		})
		totalFindings += len(entry.Findings)
	}

	// Cross-repo concepts: concept identity → { kind, repos, occurrences }.
	// `kind` carries the smallest kind observed for the concept (divergence
	// rule above) — never the first-seen one.
	concepts := map[string]*conceptRecord{}
	conceptOrder := []string{}
	for _, entry := range repos {
		for _, finding := range entry.Findings {
			concept := conceptOf(finding)
			record, ok := concepts[concept]
			if !ok {
				record = &conceptRecord{kind: finding.Kind, repos: map[string]struct{}{}}
				conceptOrder = append(conceptOrder, concept)
				concepts[concept] = record
			}
			if compareStrings(finding.Kind, record.kind) < 0 {
				record.kind = finding.Kind
			}
			record.repos[entry.Repo] = struct{}{}
			record.occurrences++
		}
	}
	crossRepoConcepts := []schemas.OrgCrossRepoConcept{}
	for _, concept := range conceptOrder {
		record := concepts[concept]
		if len(record.repos) < CROSS_REPO_MIN_REPOS {
			continue
		}
		reposOfConcept := make([]string, 0, len(record.repos))
		for repo := range record.repos {
			reposOfConcept = append(reposOfConcept, repo)
		}
		sort.Strings(reposOfConcept)
		crossRepoConcepts = append(crossRepoConcepts, schemas.OrgCrossRepoConcept{
			Concept:     concept,
			Kind:        record.kind,
			Repos:       reposOfConcept,
			Occurrences: record.occurrences,
		})
	}
	sort.SliceStable(crossRepoConcepts, func(i, j int) bool {
		return compareStrings(crossRepoConcepts[i].Concept, crossRepoConcepts[j].Concept) < 0
	})

	report := schemas.OrgDriftReport{
		Schema:            schemas.OrgDriftReportSchemaID,
		RunID:             input.RunID,
		Org:               input.Org,
		GeneratedAt:       input.GeneratedAt,
		Period:            input.Period,
		Repos:             repoRows,
		CrossRepoConcepts: crossRepoConcepts,
		Summary: schemas.OrgDriftSummary{
			Repositories:      len(repoRows),
			Findings:          totalFindings,
			CrossRepoConcepts: len(crossRepoConcepts),
		},
		Advisory: true,
	}

	// Validate AT CONSTRUCTION against the registered schema (the same
	// fail-closed posture as replacement.ts / store.ts): an artifact that
	// cannot satisfy docsync.org-drift-report/v1 is REFUSED, never returned.
	raw, err := canonicalJSONBytes(report)
	if err != nil {
		return schemas.OrgDriftReport{}, newOrgReportInputError(fmt.Sprintf(
			"aggregateOrgDriftReport: assembled report could not be serialized for %s validation — refusing to emit (fail closed): %v",
			schemas.OrgDriftReportSchemaID, err))
	}
	parsed, err := schemas.ParseOrgDriftReport([]byte(raw))
	if err != nil {
		return schemas.OrgDriftReport{}, newOrgReportInputError(fmt.Sprintf(
			"aggregateOrgDriftReport: assembled report failed %s validation — refusing to emit an artifact that violates the registry schema (fail closed): %s",
			schemas.OrgDriftReportSchemaID, registryMessage(err)))
	}
	return *parsed, nil
}

// canonicalJSONBytes renders a value as canonical JSON bytes — the exact
// serialization the registry schema parses (keys sorted, arrays in order,
// JSON.stringify-compatible escaping; fail closed on non-JSON values). The
// value round-trips through generic JSON form first (docutil.CanonicalJSON
// operates on decoded values) with UseNumber so integers stay integral.
func canonicalJSONBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	canonical, err := docutil.CanonicalJSON(generic)
	if err != nil {
		return nil, err
	}
	return []byte(canonical), nil
}

// cell is the markdown table-cell escaping (pipes and newlines would break
// the table).
func cell(value string) string {
	escaped := strings.ReplaceAll(value, "|", "\\|")
	return strings.ReplaceAll(strings.ReplaceAll(escaped, "\r\n", " "), "\n", " ")
}

// RenderOrgDriftReport renders the org drift report as markdown (the OD-R32
// scheduled-report body). Deterministic ordering; the INV-11 cap is declared
// inside the artifact.
func RenderOrgDriftReport(report schemas.OrgDriftReport) string {
	lines := []string{
		"## Org-wide drift report — " + report.Org + " (advisory)",
		"",
		orgAdvisoryBanner,
		"",
		"**" + strconv.Itoa(report.Summary.Findings) + " finding(s)** across **" +
			strconv.Itoa(report.Summary.Repositories) + " repository(ies)**" +
			" · **" + strconv.Itoa(report.Summary.CrossRepoConcepts) +
			" cross-repo concept(s)** (CTX §42)",
		"",
		"### Repositories",
		"",
		"| Repository | Findings | Docs affected | Audit run | Audited at |",
		"| --- | --- | --- | --- | --- |",
	}
	for _, repo := range report.Repos {
		lines = append(lines, "| `"+cell(repo.Repo)+"` | "+strconv.Itoa(repo.FindingsCount)+" | "+
			strconv.Itoa(repo.DocsAffected)+" | `"+cell(repo.RunID)+"` | "+repo.At+" |")
	}

	lines = append(lines, "", "### Cross-repo shared concepts", "")
	if len(report.CrossRepoConcepts) == 0 {
		lines = append(lines,
			"_No concept appeared in more than one repository — no cross-repo terminology/contract divergence surfaced (CTX §42)._")
	} else {
		lines = append(lines, "| Concept | Kind | Repositories | Occurrences |", "| --- | --- | --- | --- |")
		for _, concept := range report.CrossRepoConcepts {
			reposOfConcept := make([]string, 0, len(concept.Repos))
			for _, repo := range concept.Repos {
				reposOfConcept = append(reposOfConcept, "`"+cell(repo)+"`")
			}
			lines = append(lines, "| `"+cell(concept.Concept)+"` | "+cell(concept.Kind)+" | "+
				strings.Join(reposOfConcept, ", ")+" | "+strconv.Itoa(concept.Occurrences)+" |")
		}
	}

	if report.Period != nil {
		lines = append(lines, "", "_Covered period: "+report.Period.From+" → "+report.Period.To+"._")
	}
	lines = append(lines,
		"",
		"_Provenance: report run `"+report.RunID+"` · org `"+report.Org+"` · generated "+
			report.GeneratedAt+" (P12-T15; ARCH §15.3; OD-R32)._")
	return strings.Join(lines, "\n")
}

// RenderPerRepoAdvisoryIssue renders one repository's per-repo advisory issue
// body (OD-R32: per-repo issues ONLY for actionable findings — an empty
// finding set returns nil, so the caller opens nothing). Same advisory banner
// and INV-11 cap discipline.
func RenderPerRepoAdvisoryIssue(input OrgRepoAuditInput) *string {
	if len(input.Findings) == 0 {
		return nil
	}
	capped := input.Findings
	if len(capped) > ORG_REPORT_RENDER_CAP {
		capped = capped[:ORG_REPORT_RENDER_CAP]
	}
	overflow := len(input.Findings) - len(capped)

	docs := make(map[string]struct{}, len(input.Findings))
	for _, finding := range input.Findings {
		docs[finding.DocPath] = struct{}{}
	}

	lines := []string{
		"## Background drift audit — " + input.Repo + " (advisory)",
		"",
		orgAdvisoryBanner,
		"",
		"**" + strconv.Itoa(len(input.Findings)) + " finding(s)** across " +
			strconv.Itoa(len(docs)) + " document(s)" +
			" (flood cap " + strconv.Itoa(ORG_REPORT_RENDER_CAP) + " — INV-11):",
		"",
		"| Claim | Document | Kind | Confidence | Evidence commits |",
		"| --- | --- | --- | --- | --- |",
	}
	for _, finding := range capped {
		claim := "—"
		if finding.ClaimID != nil {
			claim = *finding.ClaimID
		}
		lines = append(lines, "| `"+cell(claim)+"` | `"+cell(finding.DocPath)+"` | "+
			cell(finding.Kind)+" | "+formatConfidence(finding.Confidence)+" | "+
			strconv.Itoa(len(finding.Evidence.Commits))+" |")
	}
	if overflow > 0 {
		lines = append(lines, "_… "+strconv.Itoa(overflow)+
			" more finding(s) omitted (flood cap "+strconv.Itoa(ORG_REPORT_RENDER_CAP)+
			", INV-11) — full set in the archival store._")
	}
	lines = append(lines, "")
	if input.RunRecord == nil {
		lines = append(lines,
			"_Provenance: no run record supplied (D5 — the archival store remains the source of truth)._")
	} else {
		lines = append(lines,
			"_Provenance: run `"+input.RunRecord.RunID+"` · trigger `"+
				string(input.RunRecord.Trigger)+"` · position `"+input.RunRecord.Position+
				"` · status `"+string(input.RunRecord.Status)+"` · "+input.RunRecord.At+"._")
	}
	rendered := strings.Join(lines, "\n")
	return &rendered
}
