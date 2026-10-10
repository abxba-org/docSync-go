// Ported from core/src/audit/artifacts.ts (P11-T20; ARCH §7.2 Loop B; D5/D10).
//
// Advisory drift-artifact renderers. PURE, LLM-FREE, DETERMINISTIC: findings
// in, markdown payloads out — no IO, no clocks, no AI. These renderers EMIT
// the payload shapes the consumer surfaces post (a default-branch check run
// via the Phase-8 upsert service, a drift issue, a dashboard row) — they POST
// nothing and, like the whole audit engine, they have NO document write path
// (ARCH §7.2: findings are advisory artifacts, never auto-writes; remediation
// routes ONLY via the SUGGEST path, P11-T21).
//
// Flood safety (INV-11): every rendered artifact caps the rendered findings
// at DRIFT_ARTIFACT_RENDER_CAP with an explicit overflow line naming the
// archival store — findings are never silently truncated (the cap is declared
// in the artifact itself).
//
// Provenance footer (D5): every artifact carries the run record's provenance
// (run id, trigger, audited position, cursor movement, doc counts, status,
// timestamp) — "provenance or it didn't happen". The check-run metadata is an
// ephemeral VIEW; `.docsync/state/audit-findings.jsonl` stays the archive.
package audit

import (
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DRIFT_ARTIFACT_RENDER_CAP is the max findings rendered per artifact before
// the overflow line (INV-11).
const DRIFT_ARTIFACT_RENDER_CAP = 50

// advisoryBanner is the shared advisory banner (verbatim from the TS module).
const advisoryBanner = "> **ADVISORY — background drift audit (Loop B, ARCH §7.2).** " +
	"No document was modified by this audit. Findings are evidence only; " +
	"remediation routes via the SUGGEST path (the same verification + policy " +
	"engine as the PR loop) — never auto-applied."

// remediationLine is the SUGGEST-path remediation note (verbatim).
const remediationLine = "Remediation: findings warranting patches become SUGGEST items processed by " +
	"the same verification + policy engine as Loop A (ARCH §7.2, one code path, " +
	"two triggers). This artifact contains no patch suggestions."

// archivePointer names the archival store (verbatim).
const archivePointer = "Archival store: `.docsync/state/audit-findings.jsonl` (D5)."

// checkRunTitle is the check-run payload title (verbatim).
const checkRunTitle = "docsync: background drift audit (advisory)"

// formatConfidence renders a finding's confidence (TS toFixed(2); an absent
// confidence renders the em dash).
func formatConfidence(confidence *float64) string {
	if confidence == nil {
		return "—"
	}
	return strconv.FormatFloat(*confidence, 'f', 2, 64)
}

// formatCommits renders up to max evidence commits with an overflow count.
func formatCommits(commits []string, max int) string {
	if len(commits) == 0 {
		return "—"
	}
	head := commits
	if len(head) > max {
		head = head[:max]
	}
	joined := strings.Join(head, ", ")
	if len(commits) > max {
		return joined + " (+" + strconv.Itoa(len(commits)-max) + " more)"
	}
	return joined
}

// provenanceFooter is the D5 provenance footer shared by every artifact
// (deterministic).
func provenanceFooter(runRecord *schemas.DriftAuditRunRecord) string {
	if runRecord == nil {
		return "_Provenance: no run record supplied (D5 — the archival store remains the source of truth)._"
	}
	cursorBefore := "∅"
	if runRecord.CursorBefore != nil {
		cursorBefore = *runRecord.CursorBefore
	}
	cursorAfter := "∅"
	if runRecord.CursorAfter != nil {
		cursorAfter = *runRecord.CursorAfter
	}
	return "_Provenance: run `" + runRecord.RunID + "` · trigger `" + string(runRecord.Trigger) + "` · " +
		"position `" + runRecord.Position + "` · cursor " + cursorBefore + " → " + cursorAfter + " · " +
		"audited " + strconv.Itoa(len(runRecord.AuditedDocs)) + " / " +
		"skipped " + strconv.Itoa(len(runRecord.SkippedDocs)) + " docs · status `" +
		string(runRecord.Status) + "` · " + runRecord.At + " (P11-T20, ARCH §7.2)._"
}

// findingsTable renders the findings table (header + rows), capped with the
// INV-11 overflow line. Findings arrive already normalized (deduplicated and
// deterministically ordered).
func findingsTable(findings []DriftFinding) string {
	lines := []string{
		"| Claim | Document | Kind | Confidence | Evidence commits |",
		"| --- | --- | --- | --- | --- |",
	}
	capped := findings
	if len(capped) > DRIFT_ARTIFACT_RENDER_CAP {
		capped = capped[:DRIFT_ARTIFACT_RENDER_CAP]
	}
	for _, finding := range capped {
		claim := "—"
		if finding.ClaimID != nil {
			claim = *finding.ClaimID
		}
		lines = append(lines, "| `"+claim+"` | `"+finding.DocPath+"` | "+finding.Kind+" | "+
			formatConfidence(finding.Confidence)+" | "+formatCommits(finding.Evidence.Commits, 3)+" |")
	}
	overflow := len(findings) - len(capped)
	if overflow > 0 {
		lines = append(lines, "_… "+strconv.Itoa(overflow)+
			" more finding(s) omitted (flood cap "+strconv.Itoa(DRIFT_ARTIFACT_RENDER_CAP)+
			", INV-11) — full set in the archival store._")
	}
	return strings.Join(lines, "\n")
}

// normalize is the deterministic finding ordering for every renderer (pinned
// dedup sort).
func normalize(findings []DriftFinding) []DriftFinding {
	return DedupeFindings(findings)
}

// findingCountLine is the shared "N finding(s)" line (empty-run variant
// included).
func findingCountLine(count int) string {
	if count == 0 {
		return "No drift findings in this audit run."
	}
	return "**" + strconv.Itoa(count) + " finding(s)** (deduplicated by claim ID; flood cap " +
		strconv.Itoa(DRIFT_ARTIFACT_RENDER_CAP) + " — INV-11):"
}

// RenderDriftCheckRun renders the default-branch check-run payload a check-run
// service would post (P11-T20; ARCH §7.2). The payload shape matches the
// Phase-8 CheckSummaryPayload (title/summary/text — see seams.go) so W5b2b
// wiring can feed it straight into the upsert seam; the conclusion mapping
// stays a caller-owned policy decision (D10 advisory default), not a renderer
// one.
func RenderDriftCheckRun(findings []DriftFinding, runRecord *schemas.DriftAuditRunRecord) CheckSummaryPayload {
	view := normalize(findings)
	docs := make(map[string]struct{}, len(view))
	for _, finding := range view {
		docs[finding.DocPath] = struct{}{}
	}
	summary := "Background drift audit: no findings (advisory, ARCH §7.2). No document was modified."
	if len(view) > 0 {
		summary = "Background drift audit: " + strconv.Itoa(len(view)) + " finding(s) across " +
			strconv.Itoa(len(docs)) + " document(s) — advisory only (ARCH §7.2); no document was modified."
	}
	text := strings.Join([]string{
		advisoryBanner,
		"",
		findingCountLine(len(view)),
		"",
		findingsTable(view),
		"",
		remediationLine,
		"",
		archivePointer,
		"",
		provenanceFooter(runRecord),
	}, "\n")
	return CheckSummaryPayload{Title: checkRunTitle, Summary: summary, Text: text}
}

// RenderDriftIssue renders the drift issue body (P11-T20; ARCH §7.2 "drift
// dashboard or issue"). Pure markdown; same cap + provenance discipline as the
// check run.
func RenderDriftIssue(findings []DriftFinding, runRecord *schemas.DriftAuditRunRecord) string {
	view := normalize(findings)
	return strings.Join([]string{
		"## Background drift audit — advisory findings",
		"",
		advisoryBanner,
		"",
		findingCountLine(len(view)),
		"",
		findingsTable(view),
		"",
		remediationLine,
		"",
		archivePointer,
		"",
		provenanceFooter(runRecord),
	}, "\n")
}

// RenderDriftDashboardRow renders one compact dashboard row for a finding
// (P11-T20). Deterministic; the dashboard aggregates rows it is handed — this
// module renders, never posts.
func RenderDriftDashboardRow(finding DriftFinding, runRecord *schemas.DriftAuditRunRecord) string {
	claim := "—"
	if finding.ClaimID != nil {
		claim = *finding.ClaimID
	}
	run := "—"
	if runRecord != nil {
		run = runRecord.RunID
	}
	return "| `" + finding.DocPath + "` | " + finding.Kind + " | `" + claim + "` | " +
		formatConfidence(finding.Confidence) + " | " +
		strconv.Itoa(len(finding.Evidence.Commits)) + " | " + run + " |"
}
