// Black-box port of the local-schema assertions in packages/core/test/
// audit-loop-b.test.ts (P11-T19, the module-local fail-closed validation the
// engine applies to every port result) and the additive-compatibility pin of
// audit-store-artifacts.test.ts (V-P11-9).
//
// The engine validates port-produced findings against a LOCAL schema
// (types.ts DRIFT_FINDING_SCHEMA): schema drift is a CHUNK FAILURE, never a
// silently-dropped record.

package audit_test

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/stretchr/testify/require"
)

func TestP11T19_ValidFindingPassesTheLocalSchema(t *testing.T) {
	require.Empty(t, audit.ValidateFinding(loopBFinding("claim_1", "docs/a.md", "c1")))

	// The optional members are optional (absent confidence, absent claim id,
	// empty evidence arrays) — no false refusals.
	minimal := audit.DriftFinding{
		ID:      "f-1",
		DocPath: "docs/a.md",
		Kind:    "claim-lost-support",
		Evidence: audit.DriftFindingEvidence{
			Commits: []string{}, Paths: []string{}, Detail: "detail",
		},
		CreatedAt: auditNow,
	}
	require.Empty(t, audit.ValidateFinding(minimal))
}

func TestP11T19_SchemaDriftIsRefusedByPath(t *testing.T) {
	base := loopBFinding("claim_1", "docs/a.md", "c1")

	cases := []struct {
		name   string
		mutate func(f *audit.DriftFinding)
		wantAt string
	}{
		{"missing id", func(f *audit.DriftFinding) { f.ID = "" }, "id"},
		{"missing doc path", func(f *audit.DriftFinding) { f.DocPath = "" }, "doc_path"},
		{"missing kind", func(f *audit.DriftFinding) { f.Kind = "" }, "kind"},
		{"missing detail", func(f *audit.DriftFinding) { f.Evidence.Detail = "" }, "evidence.detail"},
		{"missing commit", func(f *audit.DriftFinding) { f.Evidence.Commits = []string{""} }, "evidence.commits.0"},
		{"bad created_at", func(f *audit.DriftFinding) { f.CreatedAt = "not-a-datetime" }, "created_at"},
		{"confidence above range", func(f *audit.DriftFinding) { f.Confidence = floatPtr(1.5) }, "confidence"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			finding := base
			testCase.mutate(&finding)
			issues := audit.ValidateFinding(finding)
			require.NotEmpty(t, issues)
			require.Equal(t, testCase.wantAt, strings.Join(issues[0].Path, "."))
			require.NotEmpty(t, issues[0].Message)
		})
	}

	// Confidence is clamped-but-optional: 0 and 1 are both legal.
	lower := base
	lower.Confidence = floatPtr(0)
	require.Empty(t, audit.ValidateFinding(lower))
	upper := base
	upper.Confidence = floatPtr(1)
	require.Empty(t, audit.ValidateFinding(upper))

	// A claim id, when present, must be nonempty.
	empty := base
	empty.ClaimID = strPtr("")
	require.NotEmpty(t, audit.ValidateFinding(empty))
}
