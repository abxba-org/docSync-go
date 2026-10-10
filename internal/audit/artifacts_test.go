// Black-box port of the artifact half of packages/core/test/
// audit-store-artifacts.test.ts (P11-T20; ARCH §7.2; INV-11 flood safety).
//
// Pins:
//   - deterministic renderers: same input ⇒ byte-identical payloads (the
//     reversed-input order must not matter — the pinned dedup sort owns the
//     order);
//   - the flood cap is DECLARED inside the artifact (60 findings ⇒ 50 rows +
//     an overflow line), never a silent truncation;
//   - the D5 provenance footer is run-record bound (and says so when no run
//     record is supplied).

package audit_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/stretchr/testify/require"
)

// floodFindings builds the deterministic 60-finding corpus of the flood-cap
// test (TS `Array.from({length: 60}, ...)`).
func floodFindings() []audit.DriftFinding {
	findings := make([]audit.DriftFinding, 0, 60)
	for index := 0; index < 60; index++ {
		docPath := "docs/b.md"
		if index%2 == 0 {
			docPath = "docs/a.md"
		}
		findings = append(findings, storeFinding(storeFindingInput{
			ClaimID:    strPtr(fmt.Sprintf("claim_%03d", index)),
			DocPath:    docPath,
			Kind:       "claim-lost-support",
			Commits:    []string{fmt.Sprintf("c%d", index)},
			Confidence: floatPtr(float64(index) / 100),
		}))
	}
	return findings
}

// renderedRows counts the finding table rows (the TS `rows` helper: lines
// starting with '| `claim_').
func renderedRows(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "| `claim_") {
			count++
		}
	}
	return count
}

func TestP11T20_ArtifactsAreDeterministicAndFloodCapped(t *testing.T) {
	findings := floodFindings()
	runRecord := makeRunRecord(t, findings)

	// Same input ⇒ byte-identical payloads (all three renderers).
	check1 := audit.RenderDriftCheckRun(findings, &runRecord)
	reversed := make([]audit.DriftFinding, 0, len(findings))
	for i := len(findings) - 1; i >= 0; i-- {
		reversed = append(reversed, findings[i])
	}
	check2 := audit.RenderDriftCheckRun(reversed, &runRecord)
	require.Equal(t, check1, check2)
	check1JSON, err := json.Marshal(check1)
	require.NoError(t, err)
	check2JSON, err := json.Marshal(check2)
	require.NoError(t, err)
	require.JSONEq(t, string(check1JSON), string(check2JSON))
	issue1 := audit.RenderDriftIssue(findings, &runRecord)
	issue2 := audit.RenderDriftIssue(reversed, &runRecord)
	require.Equal(t, issue1, issue2)

	// The dashboard row is deterministic for the same finding.
	require.Equal(t,
		audit.RenderDriftDashboardRow(findings[0], &runRecord),
		audit.RenderDriftDashboardRow(findings[0], &runRecord))

	// Flood cap (INV-11): 60 findings ⇒ 50 rendered rows + overflow line.
	require.Equal(t, 50, audit.DRIFT_ARTIFACT_RENDER_CAP)
	require.Equal(t, 50, renderedRows(check1.Text))
	require.Contains(t, check1.Text,
		"_… 10 more finding(s) omitted (flood cap 50, INV-11) — full set in the archival store.")
	require.Equal(t, 50, renderedRows(issue1))
	require.Contains(t, issue1,
		"_… 10 more finding(s) omitted (flood cap 50, INV-11) — full set in the archival store.")

	// The rendered rows are the dedup-sorted prefix (docs/a.md first).
	firstRow := firstTableRow(check1.Text)
	require.Contains(t, firstRow, "docs/a.md")
	require.Contains(t, firstRow, "claim_000")

	// Small runs render everything (no overflow line).
	small := audit.RenderDriftCheckRun(findings[:3], &runRecord)
	require.Equal(t, 3, renderedRows(small.Text))
	require.NotContains(t, small.Text, "omitted (flood cap")

	// Zero findings: clean advisory payload, no table rows.
	empty := audit.RenderDriftCheckRun(nil, &runRecord)
	require.Equal(t, 0, renderedRows(empty.Text))
	require.Contains(t, empty.Text, "No drift findings in this audit run.")
	require.Contains(t, empty.Summary, "no findings")

	// Provenance footer is present and run-record-bound (D5).
	require.Contains(t, check1.Text, "run `audit_run_01J9ZK7Q2M`")
	require.Contains(t, check1.Text, "trigger `nightly-scheduled`")
	require.Contains(t, check1.Text, "status `completed`")
	require.Contains(t, check1.Text, auditNow)
	// No run record: the footer DECLARES the absence (never a silent skip).
	require.Contains(t, audit.RenderDriftCheckRun(findings, nil).Text, "no run record supplied")
	require.Contains(t, audit.RenderDriftIssue(findings, nil), "no run record supplied")
	require.Contains(t, audit.RenderDriftDashboardRow(findings[0], nil), "—")
}

func TestP11T20_EvidenceFormattingIsPinned(t *testing.T) {
	finding := storeFinding(storeFindingInput{
		ClaimID:    strPtr("claim_182"),
		DocPath:    "docs/payments.md",
		Kind:       "claim-lost-support",
		Commits:    []string{"c1", "c2", "c3", "c4"},
		Confidence: floatPtr(0.9),
	})
	runRecord := makeRunRecord(t, []audit.DriftFinding{finding})
	row := audit.RenderDriftDashboardRow(finding, &runRecord)
	// Four commits render head(3) + the overflow count.
	require.Contains(t, row, "| 4 | audit_run_01J9ZK7Q2M |")
	require.Contains(t, audit.RenderDriftCheckRun([]audit.DriftFinding{finding}, &runRecord).Text,
		"c1, c2, c3 (+1 more)")

	// No commits and no confidence render the em dash.
	bare := storeFinding(storeFindingInput{DocPath: "docs/x.md", Kind: "doc-drift"})
	bare.Evidence.Commits = nil
	payload := audit.RenderDriftCheckRun([]audit.DriftFinding{bare}, nil)
	require.Contains(t, payload.Text, "| `—` | `docs/x.md` | doc-drift | — | — |")
}

// firstTableRow returns the first finding row of the rendered table.
func firstTableRow(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "| `claim_") {
			return line
		}
	}
	return ""
}
