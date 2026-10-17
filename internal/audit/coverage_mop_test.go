package audit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/workspace"
	"github.com/stretchr/testify/require"
)

// COVERAGE tests for the remaining public-API statements (engine resume
// branches, cursor-write failure, store fail-closed paths, compose config
// error, dedup/suggest sort edges). Deterministic, no wall clock.

/* ------------------------------------------------------ compose config error */

func TestCOV_AUDIT_ComposeConfigError(t *testing.T) {
	// An empty DefaultBranch is a typed SchedulerConfigError surfaced from
	// ResolveLoopBTriggers BEFORE anything else runs.
	_, err := audit.RunLoopBAudit(context.Background(), audit.LoopBAuditComposeDeps{
		Config:  audit.LoopBScheduleConfig{},
		Context: audit.LoopBTriggerContext{Event: audit.LoopBEventNightly, Branch: strPtr("main")},
	})
	require.Error(t, err)
	var configErr *audit.SchedulerConfigError
	require.ErrorAs(t, err, &configErr)
}

/* ---------------------------------------------------- engine resume branches */

func TestCOV_AUDIT_CursorDoneBeyondDocCountClamped(t *testing.T) {
	// A cursor whose done=5 exceeds the 2-doc manifest is clamped to len(docs)
	// — the shrunken-manifest resize branch of the resume logic.
	store := newStateStore(t)
	docs := []string{"docs/a.md", "docs/b.md"}
	_, err := store.WriteAuditCursor(schemas.AuditCursor{
		Position:  audit.EncodeAuditPosition("old-head", 5, audit.ChunkListHash(docs)),
		UpdatedAt: auditNow,
	}, nil)
	require.NoError(t, err)

	double := makeAuditPort(func(docPath string) []audit.DriftFinding { return nil })
	result := runAudit(t, buildDeps(store, docs, double.port), 1)
	require.Equal(t, audit.AuditRunCompleted, result.Status)
	require.Empty(t, result.AuditedDocs)
	require.Equal(t, docs, result.SkippedDocs)
	require.Empty(t, double.docPaths())
	require.False(t, result.Cursor.Advanced)
}

func TestCOV_AUDIT_CursorForeignFormatSinceUsed(t *testing.T) {
	// A cursor whose position is NOT parseable (foreign format) still anchors
	// `since` on the stored position text (the prevCursor fallback branch);
	// the run restarts at done=0 and completes.
	store := newStateStore(t)
	_, err := store.WriteAuditCursor(schemas.AuditCursor{
		Position:  "foreign:format;irrelevant",
		UpdatedAt: auditNow,
	}, nil)
	require.NoError(t, err)

	double := makeAuditPort(func(docPath string) []audit.DriftFinding {
		return []audit.DriftFinding{loopBFinding("claim-"+docPath, docPath, "c-"+docPath)}
	})
	result := runAudit(t, buildDeps(store, loopBDocs, double.port), 1)
	require.Equal(t, audit.AuditRunCompleted, result.Status)
	require.Len(t, result.AuditedDocs, len(loopBDocs))
	require.Empty(t, result.SkippedDocs)
	require.True(t, result.Cursor.Advanced)
}

// writeFailingPort is an AuditCursorPort double whose write always fails (the
// store-error cursor-write branch: re-run-required, never a blind write).
type writeFailingPort struct {
	store *state.StateStore
}

func (p writeFailingPort) ReadAuditCursor() (state.StateRead[*schemas.AuditCursor], error) {
	return p.store.ReadAuditCursor()
}

func (p writeFailingPort) WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error) {
	return state.CasWriteResult{}, errors.New("store offline on write (injected)")
}

func TestCOV_AUDIT_CursorWriteFailureIsReRunRequired(t *testing.T) {
	store := newStateStore(t)
	double := makeAuditPort(func(docPath string) []audit.DriftFinding {
		return []audit.DriftFinding{loopBFinding("claim-"+docPath, docPath, "c-"+docPath)}
	})
	deps := buildDeps(store, loopBDocs, double.port)
	deps.Cursor = writeFailingPort{store: store}

	result := runAudit(t, deps, 1)
	require.Equal(t, audit.AuditRunReRunRequired, result.Status)
	require.Contains(t, *result.Detail, "audit-cursor write failed")
	// The first completed chunk is always audited; once the CAS write fails
	// the single worker aborts (a non-deterministic prefix of the rest), and
	// the findings are still reported as data.
	require.NotEmpty(t, result.AuditedDocs)
	require.NotEmpty(t, result.Findings)
	require.Len(t, result.AuditedDocs, len(result.Findings))
	require.False(t, result.Cursor.Advanced)
}

/* ----------------------------------------------------- run record fail-closed */

func TestCOV_AUDIT_BuildRunRecordRejectsEmptyRunID(t *testing.T) {
	// An empty run_id fails the registered run-record schema at construction —
	// the provenance record is refused, never composed loosely.
	_, err := audit.BuildDriftAuditRunRecord(audit.BuildDriftAuditRunRecordInput{
		RunID:    "",
		At:       auditNow,
		Trigger:  schemas.DriftAuditTriggerNightlyScheduled,
		Position: "abc",
		Result: audit.AuditRunResult{
			Status:      audit.AuditRunCompleted,
			AuditedDocs: []string{},
			SkippedDocs: []string{},
		},
	})
	require.Error(t, err)
	var validationErr *audit.AuditFindingValidationError
	require.ErrorAs(t, err, &validationErr)
}

/* ------------------------------------------------ store construction fail-closed */

/* --------------------------------------------------- append-read fail-closed */

// replayRow is a JsonlSink double that replays exactly one raw row (used to
// drive the fail-closed read paths of the store).
type replayRow struct {
	row any
}

func (s replayRow) Append(any) error { return nil }

func (s replayRow) ReadAll(parse func(value any, lineNumber int) (any, error)) ([]any, error) {
	if _, err := parse(s.row, 0); err != nil {
		return nil, err
	}
	return []any{s.row}, nil
}

// sinkReadError is a JsonlSink double whose replay fails at the transport
// level (a non-validation read failure).
type sinkReadError struct{}

func (sinkReadError) Append(any) error { return nil }

func (sinkReadError) ReadAll(parse func(value any, lineNumber int) (any, error)) ([]any, error) {
	return nil, errors.New("findings file unreadable (injected)")
}

func validFindingForStore() audit.DriftFinding {
	return audit.DriftFinding{
		ID:      "f-1",
		DocPath: "docs/a.md",
		Kind:    "claim-lost-support",
		Evidence: audit.DriftFindingEvidence{
			Commits: []string{"c1"},
			Paths:   []string{"docs/a.md"},
			Detail:  "x",
		},
		CreatedAt: auditNow,
	}
}

func TestCOV_AUDIT_AppendFindingsPriorReadValidationError(t *testing.T) {
	// The prior read fails schema validation → the whole append refuses with
	// the validation error (fail closed): nothing is appended.
	store, err := audit.CreateAuditFindingsStore(t.TempDir(), &audit.AuditStoreSinks{
		Findings: replayRow{row: map[string]any{"bogus": true}},
		Runs:     replayRow{row: map[string]any{"bogus": true}},
	})
	require.NoError(t, err)

	_, err = store.AppendFindings([]audit.DriftFinding{validFindingForStore()})
	require.Error(t, err)
	var validationErr *audit.AuditFindingValidationError
	require.ErrorAs(t, err, &validationErr)
}

func TestCOV_AUDIT_AppendFindingsPriorReadSinkError(t *testing.T) {
	// The prior read fails at the transport level → the append surfaces as a
	// typed AuditFindingsSinkError (never a silent skip).
	store, err := audit.CreateAuditFindingsStore(t.TempDir(), &audit.AuditStoreSinks{
		Findings: sinkReadError{},
		Runs:     sinkReadError{},
	})
	require.NoError(t, err)

	_, err = store.AppendFindings([]audit.DriftFinding{validFindingForStore()})
	require.Error(t, err)
	var sinkErr *audit.AuditFindingsSinkError
	require.ErrorAs(t, err, &sinkErr)
}

func TestCOV_AUDIT_ListRunRecordsReadFailures(t *testing.T) {
	// A schema-invalid runs row refuses the whole read (validation error).
	store, err := audit.CreateAuditFindingsStore(t.TempDir(), &audit.AuditStoreSinks{
		Findings: sinkReadError{},
		Runs:     replayRow{row: map[string]any{"bogus": true}},
	})
	require.NoError(t, err)
	_, err = store.ListRunRecords(nil)
	require.Error(t, err)
	var validationErr *audit.AuditFindingValidationError
	require.ErrorAs(t, err, &validationErr)

	// A transport-level runs read failure surfaces typed and loud.
	store, err = audit.CreateAuditFindingsStore(t.TempDir(), &audit.AuditStoreSinks{
		Findings: sinkReadError{},
		Runs:     sinkReadError{},
	})
	require.NoError(t, err)
	_, err = store.ListRunRecords(nil)
	require.Error(t, err)
	var sinkErr *audit.AuditFindingsSinkError
	require.ErrorAs(t, err, &sinkErr)
}

/* --------------------------------------------------------- dedup + suggest */

func TestCOV_AUDIT_EvidencePathNotSubsumed(t *testing.T) {
	// next adds a path absent from the stored view → NOT subsumed → appendable
	// (the per-path subset check returns false).
	claim := "concept-c"
	prior := audit.DriftFinding{
		ID: "f-old", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: &claim,
		Evidence:  audit.DriftFindingEvidence{Commits: []string{"c1"}, Paths: []string{"p1"}, Detail: "d"},
		CreatedAt: auditNow,
	}
	next := audit.DriftFinding{
		ID: "f-new", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: &claim,
		Evidence:  audit.DriftFindingEvidence{Commits: []string{"c1"}, Paths: []string{"p2"}, Detail: "d"},
		CreatedAt: auditNow,
	}
	res := audit.DedupeAgainstStore([]audit.DriftFinding{next}, []audit.DriftFinding{prior})
	require.Len(t, res.Appendable, 1)
}

func TestCOV_AUDIT_SuggestAdvisoryOnlySortsByFindingID(t *testing.T) {
	// Two advisory-only findings share a doc path: the deterministic sort
	// falls back to findingId (the comparator's second key).
	mapping := audit.FindingsToSuggestCandidates([]audit.DriftFinding{
		{ID: "f-b", DocPath: "docs/a.md", Kind: "k", CreatedAt: auditNow},
		{ID: "f-a", DocPath: "docs/a.md", Kind: "k", CreatedAt: auditNow},
	}, map[string]workspace.WorkspaceMode{"docs/a.md": workspace.WorkspaceModeLocked})
	require.Len(t, mapping.AdvisoryOnly, 2)
	require.Equal(t, "f-a", mapping.AdvisoryOnly[0].FindingID)
	require.Equal(t, "f-b", mapping.AdvisoryOnly[1].FindingID)
}

func TestCOV_AUDIT_CreateStoreLongRelativePath(t *testing.T) {
	// A non-absolute stateDir > 64 runes hits the truncateForMessage excerpt
	// (the fail-closed construction error message is bounded).
	long := strings.Repeat("a", 100)
	_, err := audit.CreateAuditFindingsStore(long, nil)
	require.Error(t, err)
	var sinkErr *audit.AuditFindingsSinkError
	require.ErrorAs(t, err, &sinkErr)
}
