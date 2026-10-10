// Black-box port of packages/core/test/audit-store-artifacts.test.ts
// (P11-T20; ARCH §7.2 Loop B; PLAN §12.5 V-P11-9) — the findings store half.
//
// Pins:
//   - V-P11-9 structural seam: the store's ONLY write targets are the audit
//     JSONL files under `.docsync/state/` (never a document path).
//   - P11-T20 cross-run dedup: an identical re-run yields ZERO duplicate
//     findings for the same claim ID against the persisted store; evidence
//     unions merge across runs (claim-id key, latest-wins body — the same
//     pinned rule as the within-run dedup).
//   - P11-T20 fail-closed store: schema-invalid records are refused before
//     ANY write, write failures surface typed, corrupt lines fail reads.
//
// Fixtures: temp dirs only (no git — findings are synthetic data), pinned
// dates, no network.

package audit_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/feedback"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// storeFindingInput is the P11-T20 finding factory input (TS `finding({...})`).
type storeFindingInput struct {
	ClaimID    *string
	DocPath    string
	Kind       string
	Commits    []string
	Detail     string
	Confidence *float64
}

// storeFinding is the deterministic finding factory of the store/artifact
// suites (pinned dates; claim-scoped evidence).
func storeFinding(in storeFindingInput) audit.DriftFinding {
	claimID := ""
	if in.ClaimID != nil {
		claimID = *in.ClaimID
	}
	commits := in.Commits
	if commits == nil {
		commits = []string{"c-" + claimID}
		if claimID == "" {
			commits = []string{"c-" + in.DocPath}
		}
	}
	detail := in.Detail
	if detail == "" {
		label := claimID
		if label == "" {
			label = in.DocPath
		}
		detail = label + " lost support"
	}
	return audit.DriftFinding{
		ID:      "f-" + claimID + "-" + in.DocPath,
		ClaimID: in.ClaimID,
		DocPath: in.DocPath,
		Kind:    in.Kind,
		Evidence: audit.DriftFindingEvidence{
			Commits: commits,
			Paths:   []string{"src/x.ts"},
			Detail:  detail,
		},
		Confidence: in.Confidence,
		CreatedAt:  auditNow,
	}
}

// createStateDir builds a temp `.docsync/state` dir (the TS createStateDir).
func createStateDir(t *testing.T) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), schemas.DocSyncDir, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	return stateDir
}

// jsonlLineCount counts the non-empty lines of a JSONL file (the TS
// jsonlLineCount).
func jsonlLineCount(t *testing.T, file string) int {
	t.Helper()
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// countByKey counts the findings per dedup key (the TS countByKey).
func countByKey(findings []audit.DriftFinding) map[string]int {
	counts := map[string]int{}
	for _, finding := range findings {
		key := "unclaimed:" + finding.DocPath + "|" + finding.Kind
		if finding.ClaimID != nil {
			key = *finding.ClaimID
		}
		counts[key]++
	}
	return counts
}

func TestVP11_9_FindingsCarryEvidenceAndRenderAdvisoryOnly(t *testing.T) {
	stateDir := createStateDir(t)
	store, err := audit.CreateAuditFindingsStore(stateDir, nil)
	require.NoError(t, err)

	// Structural seam: the store's ONLY write targets are the audit JSONL
	// files under `.docsync/state/` (never a document path).
	require.Equal(t, filepath.Join(stateDir, "audit-findings.jsonl"), store.FindingsFile())
	require.Contains(t, store.FindingsFile(), schemas.DocSyncDir)
	require.Equal(t, filepath.Join(stateDir, "audit-runs.jsonl"), store.RunsFile())

	findings := []audit.DriftFinding{
		storeFinding(storeFindingInput{
			ClaimID: strPtr("claim_182"), DocPath: "docs/payments.md",
			Kind: "claim-lost-support", Commits: []string{"c1"}, Confidence: floatPtr(0.9)}),
		storeFinding(storeFindingInput{
			ClaimID: strPtr("claim_190"), DocPath: "docs/api.md",
			Kind: "claim-lost-support", Commits: []string{"c2"}, Confidence: floatPtr(0.72)}),
		storeFinding(storeFindingInput{
			DocPath: "docs/legacy.md", Kind: "doc-drift", Confidence: floatPtr(0.5)}),
	}
	appended, err := store.AppendFindings(findings)
	require.NoError(t, err)
	require.Len(t, appended.Appended, 3)

	listed, err := store.ListFindings(nil)
	require.NoError(t, err)
	require.Len(t, listed, 3)
	for _, item := range listed {
		require.NotEmpty(t, item.Evidence.Commits)
	}
	// Canonical view order: the pinned dedup sort (doc_path, kind, claim, id).
	require.Equal(t, []*string{strPtr("claim_190"), nil, strPtr("claim_182")}, claimIDsOf(listed))

	// Additive compatibility: the registered wire record minus its `schema`
	// tag validates against the LOCAL engine schema (the promotion is a
	// strict refinement, never a fork).
	registeredExample, ok := schemas.GetSchema("docsync.drift-finding").Example.(schemas.DriftFindingRecord)
	require.True(t, ok, "drift-finding registry example")
	raw, err := json.Marshal(registeredExample)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &wire))
	delete(wire, "schema")
	untagged, err := json.Marshal(wire)
	require.NoError(t, err)
	var local audit.DriftFinding
	require.NoError(t, json.Unmarshal(untagged, &local))
	require.Empty(t, audit.ValidateFinding(local))

	// Advisory check-run payload: carries the evidence, never a patch.
	runRecord := makeRunRecord(t, findings)
	payload := audit.RenderDriftCheckRun(findings, &runRecord)
	require.Equal(t, "docsync: background drift audit (advisory)", payload.Title)
	require.Contains(t, payload.Summary, "advisory only")
	require.Contains(t, payload.Summary, "no document was modified")
	require.Contains(t, payload.Text, "No document was modified")
	require.Contains(t, payload.Text, "SUGGEST")
	require.Contains(t, payload.Text, "`claim_182`")
	require.Contains(t, payload.Text, "c1")
	require.Contains(t, payload.Text, "0.90")
	require.Contains(t, payload.Text, "audit_run_01J9ZK7Q2M")
	require.NotContains(t, payload.Text, "```diff")
	require.NotContains(t, payload.Text, "--- a/")

	// Issue + dashboard rows: same advisory discipline.
	issue := audit.RenderDriftIssue(findings, &runRecord)
	require.Contains(t, issue, "## Background drift audit — advisory findings")
	require.Contains(t, issue, "No document was modified")
	require.NotContains(t, issue, "```diff")
	row := audit.RenderDriftDashboardRow(findings[0], &runRecord)
	require.Contains(t, row, "docs/payments.md")
	require.Contains(t, row, "claim-lost-support")
	require.Contains(t, row, "claim_182")

	// Grep-level structural assertion (the TS read the module sources): the
	// renderers have NO fs surface and the store's persistence is the
	// append-only JSONL transport to the store's own files.
	artifactsSource, err := os.ReadFile("artifacts.go")
	require.NoError(t, err)
	require.NotContains(t, string(artifactsSource), "node:fs")
	require.NotContains(t, string(artifactsSource), "os.ReadFile")
	require.NotContains(t, string(artifactsSource), "os.WriteFile")
	require.NotContains(t, string(artifactsSource), "appendFile")
	storeSource, err := os.ReadFile("store.go")
	require.NoError(t, err)
	require.NotContains(t, string(storeSource), "writeFile")
	require.Contains(t, string(storeSource), "audit-findings.jsonl")
}

func TestP11T20_CrossRunDedupYieldsZeroDuplicateFindingsForTheSameClaimID(t *testing.T) {
	stateDir := createStateDir(t)
	store, err := audit.CreateAuditFindingsStore(stateDir, nil)
	require.NoError(t, err)

	run1 := []audit.DriftFinding{
		storeFinding(storeFindingInput{
			ClaimID: strPtr("claim_A"), DocPath: "docs/a.md",
			Kind: "claim-lost-support", Commits: []string{"c1"}, Confidence: floatPtr(0.9)}),
		storeFinding(storeFindingInput{
			ClaimID: strPtr("claim_B"), DocPath: "docs/b.md",
			Kind: "claim-lost-support", Commits: []string{"c2"}, Confidence: floatPtr(0.8)}),
		storeFinding(storeFindingInput{DocPath: "docs/legacy.md", Kind: "doc-drift"}),
	}
	first, err := store.AppendFindings(run1)
	require.NoError(t, err)
	require.Len(t, first.Appended, 3)
	require.Empty(t, first.Duplicates)
	require.Equal(t, 3, jsonlLineCount(t, store.FindingsFile()))

	// Identical re-run: every occurrence is fully subsumed by the stored view
	// — ZERO new lines, zero duplicate findings in the list view.
	second, err := store.AppendFindings(run1)
	require.NoError(t, err)
	require.Empty(t, second.Appended)
	require.Len(t, second.Duplicates, 3)
	require.Equal(t, 3, jsonlLineCount(t, store.FindingsFile()))
	viewAfterRerun, err := store.ListFindings(nil)
	require.NoError(t, err)
	counts := countByKey(viewAfterRerun)
	require.Equal(t, 1, counts["claim_A"])
	require.Equal(t, 1, counts["claim_B"])
	require.Equal(t, 1, counts["unclaimed:docs/legacy.md|doc-drift"])

	// A later run re-detects claim_A with NEW commit evidence: the raw
	// occurrence is appended (append-only) and the view unions the evidence
	// across runs with a latest-wins body (the pinned rule).
	run3 := []audit.DriftFinding{
		storeFinding(storeFindingInput{
			ClaimID: strPtr("claim_A"), DocPath: "docs/a.md", Kind: "claim-lost-support",
			Commits: []string{"c3"}, Detail: "claim_A lost support (re-detected)",
			Confidence: floatPtr(0.95)}),
	}
	third, err := store.AppendFindings(run3)
	require.NoError(t, err)
	require.Len(t, third.Appended, 1)
	require.Empty(t, third.Duplicates)
	require.Equal(t, 4, jsonlLineCount(t, store.FindingsFile()))

	view, err := store.ListFindings(nil)
	require.NoError(t, err)
	require.Equal(t, 1, countByKey(view)["claim_A"])
	mergedA := findingWithClaim(view, "claim_A")
	require.NotNil(t, mergedA)
	require.Equal(t, []string{"c1", "c3"}, mergedA.Evidence.Commits)
	require.Equal(t, "claim_A lost support (re-detected)", mergedA.Evidence.Detail)
	require.NotNil(t, mergedA.Confidence)
	require.InDelta(t, 0.95, *mergedA.Confidence, 1e-9)

	// Pure cross-run dedup surface (same rule, no IO): a newer body is NOT
	// subsumed (latest-wins) — it is appendable and the merged view unions.
	pure := audit.DedupeAgainstStore(run3, run1)
	require.Len(t, pure.Appendable, 1)
	require.Empty(t, pure.Duplicates)
	require.Equal(t, []string{"c1", "c3"},
		findingWithClaim(pure.Merged, "claim_A").Evidence.Commits)
	// Identical occurrences ARE fully subsumed (the re-run zero-append pin).
	identical := audit.DedupeAgainstStore(run1, run1)
	require.Empty(t, identical.Appendable)
	require.Len(t, identical.Duplicates, 3)
}

func TestP11T20_StoreIsFailClosed(t *testing.T) {
	stateDir := createStateDir(t)
	store, err := audit.CreateAuditFindingsStore(stateDir, nil)
	require.NoError(t, err)

	// Invalid finding in a batch with valid ones: the WHOLE batch is
	// validated before ANY write — nothing partially persisted.
	invalid := storeFinding(storeFindingInput{
		ClaimID: strPtr("claim_bad"), DocPath: "docs/a.md",
		Kind: "claim-lost-support", Confidence: floatPtr(1.5)})
	valid := storeFinding(storeFindingInput{
		ClaimID: strPtr("claim_ok"), DocPath: "docs/b.md", Kind: "claim-lost-support"})
	_, err = store.AppendFindings([]audit.DriftFinding{valid, invalid})
	require.Error(t, err)
	var validationErr *audit.AuditFindingValidationError
	require.True(t, errors.As(err, &validationErr))
	require.Equal(t, audit.EDOCSYNC_AUDIT_FINDING_INVALID, validationErr.Code())
	require.Contains(t, validationErr.Error(), "EDOCSYNC_AUDIT_FINDING_INVALID: ")
	require.Contains(t, validationErr.Error(), "fail closed, nothing written")
	_, err = store.AppendFindings([]audit.DriftFinding{valid, invalid})
	require.Error(t, err)
	listed, err := store.ListFindings(nil)
	require.NoError(t, err)
	require.Empty(t, listed)

	// Write failure surfaces typed (single transport retry, then fail loud).
	failingSink := failingJsonlSink{}
	failingStore, err := audit.CreateAuditFindingsStore(stateDir, &audit.AuditStoreSinks{Findings: failingSink})
	require.NoError(t, err)
	_, err = failingStore.AppendFindings([]audit.DriftFinding{valid})
	require.Error(t, err)
	var sinkErr *audit.AuditFindingsSinkError
	require.True(t, errors.As(err, &sinkErr))
	require.Equal(t, audit.EDOCSYNC_AUDIT_FINDINGS_SINK, sinkErr.Code())
	require.Error(t, sinkErr.Cause)
	require.Contains(t, sinkErr.Error(), "EDOCSYNC_AUDIT_FINDINGS_SINK: ")
	require.Contains(t, sinkErr.Error(), "could not be appended")
	require.ErrorIs(t, sinkErr, sinkErr.Cause)

	// Corrupt persisted line — valid JSON failing the registered schema:
	// reads fail closed (hard error, never "skip the unreadable row").
	healthy, err := audit.CreateAuditFindingsStore(stateDir, nil)
	require.NoError(t, err)
	_, err = healthy.AppendFindings([]audit.DriftFinding{valid})
	require.NoError(t, err)
	appendLine(t, healthy.FindingsFile(), `{"schema":"docsync.drift-finding/v1"}`)
	_, err = healthy.ListFindings(nil)
	require.Error(t, err)
	require.True(t, errors.As(err, &validationErr))

	// Non-JSON garbage: a typed transport-level failure, also fail closed.
	garbageDir := createStateDir(t)
	garbageStore, err := audit.CreateAuditFindingsStore(garbageDir, nil)
	require.NoError(t, err)
	_, err = garbageStore.AppendFindings([]audit.DriftFinding{valid})
	require.NoError(t, err)
	appendLine(t, garbageStore.FindingsFile(), "not-json")
	_, err = garbageStore.ListFindings(nil)
	require.Error(t, err)
	require.True(t, errors.As(err, &sinkErr))

	// Invalid run record: refused before any write.
	_, err = healthy.AppendRunRecord(schemas.DriftAuditRunRecord{
		Schema:   "docsync.drift-audit-run-record/v1",
		RunID:    "audit_run_01J9ZK7Q2M",
		At:       auditNow,
		Trigger:  schemas.DriftAuditTriggerNightlyScheduled,
		Position: auditHead,
		Status:   schemas.DriftAuditRunStatus("exploded"),
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &validationErr))
}

func TestP11T20_StateDirMustBeAbsolute(t *testing.T) {
	_, err := audit.CreateAuditFindingsStore("relative/state", nil)
	require.Error(t, err)
	var sinkErr *audit.AuditFindingsSinkError
	require.True(t, errors.As(err, &sinkErr))
	require.Equal(t, audit.EDOCSYNC_AUDIT_FINDINGS_SINK, sinkErr.Code())
	require.Contains(t, sinkErr.Message, "stateDir must be a nonempty absolute path")

	stateDir := createStateDir(t)
	store, err := audit.CreateAuditFindingsStore(stateDir, nil)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(stateDir, "audit-findings.jsonl"), store.FindingsFile())
}

func TestP11T20_ListRunRecordsIsBoundedAndNewestLast(t *testing.T) {
	stateDir := createStateDir(t)
	store, err := audit.CreateAuditFindingsStore(stateDir, nil)
	require.NoError(t, err)

	for _, runID := range []string{"run_1", "run_2", "run_3"} {
		record, err := audit.BuildDriftAuditRunRecord(audit.BuildDriftAuditRunRecordInput{
			RunID:    runID,
			At:       auditNow,
			Trigger:  schemas.DriftAuditTriggerNightlyScheduled,
			Position: auditHead,
			Result: audit.AuditRunResult{
				Status:      audit.AuditRunCompleted,
				Findings:    []audit.DriftFinding{},
				AuditedDocs: []string{"docs/a.md"},
				SkippedDocs: []string{},
			},
		})
		require.NoError(t, err)
		persisted, err := store.AppendRunRecord(record)
		require.NoError(t, err)
		require.Equal(t, runID, persisted.RunID)
	}

	records, err := store.ListRunRecords(nil)
	require.NoError(t, err)
	require.Len(t, records, 3)
	require.Equal(t, []string{"run_1", "run_2", "run_3"},
		[]string{records[0].RunID, records[1].RunID, records[2].RunID})

	// The bounded read keeps the NEWEST rows (file order tail).
	bounded, err := store.ListRunRecords(&audit.ListFindingsOptions{Limit: intPtr(2)})
	require.NoError(t, err)
	require.Len(t, bounded, 2)
	require.Equal(t, "run_3", bounded[1].RunID)

	// The bounded findings view keeps the alphabetically-first dedup keys.
	_, err = store.ListFindings(&audit.ListFindingsOptions{Limit: intPtr(1)})
	require.NoError(t, err)
}

/* ----------------------------------------------------------------- doubles */

// failingJsonlSink is the TS failing sink double: every append fails, every
// read is empty.
type failingJsonlSink struct{}

func (failingJsonlSink) Append(value any) error { return errors.New("disk full") }

func (failingJsonlSink) ReadAll(parse func(value any, lineNumber int) (any, error)) ([]any, error) {
	return []any{}, nil
}

// compile-time proof the double satisfies the injectable transport.
var _ feedback.JsonlSink = failingJsonlSink{}

// appendLine appends one raw line to a JSONL file (the TS appendFile).
func appendLine(t *testing.T, file, line string) {
	t.Helper()
	handle, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = handle.WriteString(line + "\n")
	require.NoError(t, err)
	require.NoError(t, handle.Close())
}

// findingWithClaim selects the first finding carrying the given claim id.
func findingWithClaim(findings []audit.DriftFinding, claimID string) *audit.DriftFinding {
	for i := range findings {
		if findings[i].ClaimID != nil && *findings[i].ClaimID == claimID {
			return &findings[i]
		}
	}
	return nil
}
