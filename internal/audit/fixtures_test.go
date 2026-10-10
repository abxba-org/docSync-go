// Shared deterministic fixtures for the Loop B audit test suites (the TS
// suites shared one fixture block per file; here they live in one place so
// every ported suite asserts against the SAME pinned values).
//
// No wall clock is read anywhere: auditNow is the pinned ISO-8601 stamp the
// suites hand to the engine/store/compose as the caller-owned clock.

package audit_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// auditNow is the deterministic pinned clock for every suite.
const auditNow = "2026-10-09T00:00:00.000Z"

// auditHead is the deterministic repository position (HEAD sha) of the
// engine suites.
const auditHead = "head-sha-0001"

// floatPtr returns a pointer to v (optional-field helper).
func floatPtr(v float64) *float64 { return &v }

// intPtr returns a pointer to v (optional-field helper).
func intPtr(v int) *int { return &v }

// strPtr returns a pointer to v (optional-field helper).
func strPtr(v string) *string { return &v }

// claimIDsOf extracts the claim ids (nil for claimless findings) in order —
// the TS `findings.map((f) => f.claim_id)` assertion shape.
func claimIDsOf(findings []audit.DriftFinding) []*string {
	out := make([]*string, 0, len(findings))
	for _, finding := range findings {
		out = append(out, finding.ClaimID)
	}
	return out
}

// requireKinds asserts the findings' kinds in order.
func requireKinds(t *testing.T, findings []audit.DriftFinding, want []string) {
	t.Helper()
	got := make([]string, 0, len(findings))
	for _, finding := range findings {
		got = append(got, finding.Kind)
	}
	require.Equal(t, want, got)
}

// makeRunRecord composes the deterministic P11-T20 run-provenance record the
// artifact suites render against (the TS makeRunRecord helper).
func makeRunRecord(t *testing.T, findings []audit.DriftFinding) schemas.DriftAuditRunRecord {
	t.Helper()
	record, err := audit.BuildDriftAuditRunRecord(audit.BuildDriftAuditRunRecordInput{
		RunID:    "audit_run_01J9ZK7Q2M",
		At:       auditNow,
		Trigger:  schemas.DriftAuditTriggerNightlyScheduled,
		Position: auditHead,
		Result: audit.AuditRunResult{
			Status:      audit.AuditRunCompleted,
			Findings:    findings,
			AuditedDocs: []string{"docs/a.md", "docs/b.md"},
			SkippedDocs: []string{},
			Cursor: audit.AuditCursorState{
				Advanced: true,
				Position: strPtr("loopb:v1;since=" + auditHead + ";done=2;list=abc123def456"),
			},
		},
	})
	require.NoError(t, err)
	return record
}
