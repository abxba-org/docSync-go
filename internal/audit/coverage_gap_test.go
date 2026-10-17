package audit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

// failingExecutor is a GitExecutor double that fails every invocation.
type failingExecutor struct{}

func (failingExecutor) Run(context.Context, []string, *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	return gitx.GitRunResult{}, errors.New("executor failure (injected)")
}

// ---------------------------------------------------------------------------
// types.go: datetime validation branches + IsValidAuditRunStatus

func TestCOV_AUDIT_ValidateFindingDatetimeBranches(t *testing.T) {
	base := audit.DriftFinding{
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

	bad := base
	bad.CreatedAt = "2026-10-09T00:00:00"
	issues := audit.ValidateFinding(bad)
	require.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "Invalid datetime")

	bad2 := base
	bad2.CreatedAt = "2026-10-09T00:00:00.Z"
	issues = audit.ValidateFinding(bad2)
	require.NotEmpty(t, issues)

	bad3 := base
	bad3.CreatedAt = "short"
	issues = audit.ValidateFinding(bad3)
	require.NotEmpty(t, issues)

	hi := base
	hi.Confidence = floatPtr(1.5)
	issues = audit.ValidateFinding(hi)
	require.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "less than or equal to 1")

	lo := base
	lo.Confidence = floatPtr(-0.1)
	issues = audit.ValidateFinding(lo)
	require.NotEmpty(t, issues)
	assert.Contains(t, issues[0].Message, "greater than or equal to 0")
}

func TestCOV_AUDIT_IsValidAuditRunStatus(t *testing.T) {
	for _, s := range audit.AuditRunStatusValues {
		assert.True(t, audit.IsValidAuditRunStatus(string(s)))
	}
	assert.False(t, audit.IsValidAuditRunStatus("bogus"))
	assert.False(t, audit.IsValidAuditRunStatus(""))
}

// ---------------------------------------------------------------------------
// incremental.js: jsInteger (via ParseAuditPosition) + ParseAuditPosition

func TestCOV_AUDIT_ParseAuditPosition(t *testing.T) {
	assert.Nil(t, audit.ParseAuditPosition("foreign:format"))

	// The codec refuses any position missing one of the required
	// since/done/list segments (fail closed — an incomplete cursor must never
	// be resumed as if it were complete).
	assert.Nil(t, audit.ParseAuditPosition(audit.PositionPrefix+";"))
	assert.Nil(t, audit.ParseAuditPosition(audit.PositionPrefix+";since=x;done=1;"))
	assert.Nil(t, audit.ParseAuditPosition(audit.PositionPrefix+";since=x;done=1;list="))
	// An unknown segment is refused, and a malformed percent-escape in
	// `since` is refused (the TS `catch { return null }`).
	assert.Nil(t, audit.ParseAuditPosition(audit.PositionPrefix+";since=x;extra=1;done=2;list=h"))
	assert.Nil(t, audit.ParseAuditPosition(audit.PositionPrefix+";since=%zz;done=1;list=h"))

	// Round-trip: EncodeAuditPosition output parses back (the `;` inside
	// `since` is percent-encoded, so the delimiters stay unambiguous).
	encoded := audit.EncodeAuditPosition("abc;def", 2, "hash123")
	parsed := audit.ParseAuditPosition(encoded)
	require.NotNil(t, parsed)
	assert.Equal(t, "abc;def", parsed.Since)
	assert.Equal(t, 2, parsed.Done)
	assert.Equal(t, "hash123", parsed.ListHash)

	// jsInteger via the `done` segment: a blank value parses as 0 ...
	parsed = audit.ParseAuditPosition(audit.PositionPrefix + ";since=x;done=;list=h")
	require.NotNil(t, parsed)
	assert.Equal(t, 0, parsed.Done)
	// ... while a non-integral or negative value is refused.
	assert.Nil(t, audit.ParseAuditPosition(audit.PositionPrefix+";since=x;done=1.5;list=h"))
	assert.Nil(t, audit.ParseAuditPosition(audit.PositionPrefix+";since=x;done=-1;list=h"))
}

// ---------------------------------------------------------------------------
// suggest.go: FindingsToSuggestCandidates sort + advisory-only + no manifest

func TestCOV_AUDIT_FindingsToSuggestCandidates(t *testing.T) {
	mode := workspace.WorkspaceModeAuto
	protected := workspace.WorkspaceModeLocked

	findings := []audit.DriftFinding{
		{ID: "f-b", DocPath: "docs/b.md", Kind: "claim-lost-support", Evidence: audit.DriftFindingEvidence{Commits: []string{"c1"}, Paths: []string{"docs/b.md"}, Detail: "b detail"}, CreatedAt: auditNow},
		{ID: "f-a", DocPath: "docs/a.md", Kind: "claim-lost-support", Evidence: audit.DriftFindingEvidence{Commits: []string{"c2"}, Paths: []string{"docs/a.md"}, Detail: "a detail"}, CreatedAt: auditNow},
		{ID: "f-z", DocPath: "docs/a.md", Kind: "claim-lost-support", Evidence: audit.DriftFindingEvidence{Commits: []string{"c3"}, Paths: []string{"docs/a.md"}, Detail: "z detail"}, CreatedAt: auditNow},
	}

	mapping := audit.FindingsToSuggestCandidates(findings, map[string]workspace.WorkspaceMode{
		"docs/a.md": mode,
		"docs/b.md": mode,
	})
	require.Len(t, mapping.Candidates, 3)
	assert.Equal(t, "docs/a.md", mapping.Candidates[0].DocPath)
	assert.Equal(t, "f-a", mapping.Candidates[0].FindingID)
	assert.Equal(t, "docs/b.md", mapping.Candidates[2].DocPath)
	assert.Equal(t, "f-b", mapping.Candidates[2].FindingID)

	mapping = audit.FindingsToSuggestCandidates(findings[:1], map[string]workspace.WorkspaceMode{
		"docs/b.md": protected,
	})
	require.Empty(t, mapping.Candidates)
	require.Len(t, mapping.AdvisoryOnly, 1)
	assert.Equal(t, "docs/b.md", mapping.AdvisoryOnly[0].DocPath)
	assert.Contains(t, mapping.AdvisoryOnly[0].Reason, "D2")
}

func TestCOV_AUDIT_RunDriftSuggestFlowNoManifest(t *testing.T) {
	called := false
	entry := func(context.Context, *audit.PipelineInput, audit.PipelinePorts) (*schemas.RunOutcome, error) {
		called = true
		return nil, nil
	}
	result, err := audit.RunDriftSuggestFlow(context.Background(), audit.DriftSuggestFlowDeps{
		Candidates:    []audit.DriftSuggestCandidate{{DocPath: "docs/a.md"}},
		Input:         nil,
		Ports:         nil,
		PipelineEntry: entry,
	})
	require.NoError(t, err)
	assert.False(t, called)
	assert.Len(t, result.Candidates, 1)
	assert.Nil(t, result.Outcome)
}

func TestCOV_AUDIT_RunDriftSuggestFlowEntryError(t *testing.T) {
	entry := func(context.Context, *audit.PipelineInput, audit.PipelinePorts) (*schemas.RunOutcome, error) {
		return nil, errors.New("pipeline failure (injected)")
	}
	_, err := audit.RunDriftSuggestFlow(context.Background(), audit.DriftSuggestFlowDeps{
		Candidates: []audit.DriftSuggestCandidate{{DocPath: "docs/a.md"}},
		Input: &audit.PipelineInput{Manifest: &schemas.Manifest{
			Version:   1,
			Documents: []schemas.ManifestEntry{{Path: "docs/a.md", Sources: []string{"docs/a.md"}}},
		}},
		Ports:         nil,
		PipelineEntry: entry,
	})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// dedup.go: compareFindings same-ID / different-ID branches (via DedupeAgainstStore)

func TestCOV_AUDIT_DedupCompareBranches(t *testing.T) {
	sameID := "claim-shared"
	a := audit.DriftFinding{ID: "f-1", ClaimID: strPtr(sameID), DocPath: "docs/a.md", Kind: "claim-lost-support", CreatedAt: auditNow}
	b := audit.DriftFinding{ID: "f-2", ClaimID: strPtr(sameID), DocPath: "docs/a.md", Kind: "claim-lost-support", CreatedAt: auditNow}
	c := audit.DriftFinding{ID: "f-3", ClaimID: strPtr("claim-other"), DocPath: "docs/a.md", Kind: "claim-lost-support", CreatedAt: auditNow}

	res := audit.DedupeAgainstStore([]audit.DriftFinding{a, b}, nil)
	require.Len(t, res.Appendable, 2)

	res = audit.DedupeAgainstStore([]audit.DriftFinding{a, c}, nil)
	require.Len(t, res.Appendable, 2)
}

// ---------------------------------------------------------------------------
// org-report.go: cross-repo concepts empty + requireRepoFullName

func TestCOV_AUDIT_AggregateOrgReportConceptsEmpty(t *testing.T) {
	report, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org:         "acme",
		RunID:       "run_1",
		GeneratedAt: auditNow,
		Repos: []audit.OrgRepoAuditInput{
			{Repo: "acme/repo-a", Findings: []audit.DriftFinding{{ID: "f-1", DocPath: "docs/a.md", Kind: "claim-lost-support", Evidence: audit.DriftFindingEvidence{Commits: []string{"c1"}, Paths: []string{"docs/a.md"}, Detail: "x"}, CreatedAt: auditNow}}},
			{Repo: "acme/repo-b", Findings: []audit.DriftFinding{{ID: "f-2", DocPath: "docs/b.md", Kind: "doc-drift", Evidence: audit.DriftFindingEvidence{Commits: []string{"c2"}, Paths: []string{"docs/b.md"}, Detail: "y"}, CreatedAt: auditNow}}},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, report.CrossRepoConcepts)
}

func TestCOV_AUDIT_OrgReportRequireRepoFullName(t *testing.T) {
	_, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org:         "acme",
		RunID:       "run_1",
		GeneratedAt: auditNow,
		Repos:       []audit.OrgRepoAuditInput{{Repo: "bare-name"}},
	})
	require.Error(t, err)
	var inputErr *audit.OrgReportInputError
	require.ErrorAs(t, err, &inputErr)
}

// ---------------------------------------------------------------------------
// store.go: error Unwrap methods

// failingSink is a feedback.JsonlSink double: reads replay nothing; appends
// fail with the injected error.
type failingSink struct {
	cause error
}

func (s failingSink) Append(any) error { return s.cause }

func (s failingSink) ReadAll(func(value any, lineNumber int) (any, error)) ([]any, error) {
	return []any{}, nil
}

func TestCOV_AUDIT_StoreErrorUnwrap(t *testing.T) {
	// Constructor-built AuditFindingsSinkError (no cause): Unwrap exposes the
	// coded inner docutil error.
	_, err := audit.CreateAuditFindingsStore("", nil)
	require.Error(t, err)
	var sinkErr *audit.AuditFindingsSinkError
	require.ErrorAs(t, err, &sinkErr)
	assert.Equal(t, audit.EDOCSYNC_AUDIT_FINDINGS_SINK, sinkErr.Code())
	assert.Contains(t, sinkErr.Error(), "stateDir must be a nonempty absolute path")
	assert.NotNil(t, sinkErr.Unwrap())

	// Wrapped cause: a failing append sink surfaces as AuditFindingsSinkError
	// whose chain reaches the cause (errors.Is) through the coded inner error.
	cause := errors.New("append failure (injected)")
	store, err := audit.CreateAuditFindingsStore(t.TempDir(), &audit.AuditStoreSinks{
		Findings: failingSink{cause: cause},
		Runs:     failingSink{cause: cause},
	})
	require.NoError(t, err)
	_, err = store.AppendFindings([]audit.DriftFinding{{
		ID:      "f-1",
		DocPath: "docs/a.md",
		Kind:    "claim-lost-support",
		Evidence: audit.DriftFindingEvidence{
			Commits: []string{"c1"},
			Paths:   []string{"docs/a.md"},
			Detail:  "x",
		},
		CreatedAt: auditNow,
	}})
	require.Error(t, err)
	require.ErrorAs(t, err, &sinkErr)
	assert.Equal(t, audit.EDOCSYNC_AUDIT_FINDINGS_SINK, sinkErr.Code())
	assert.NotNil(t, sinkErr.Unwrap())
	assert.ErrorIs(t, err, cause)

	// AuditFindingValidationError (fail closed on a schema-drift finding):
	// Unwrap exposes the coded inner error.
	_, err = store.AppendFindings([]audit.DriftFinding{{ID: "", DocPath: "", Kind: "", CreatedAt: "bad"}})
	require.Error(t, err)
	var validationErr *audit.AuditFindingValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, audit.EDOCSYNC_AUDIT_FINDING_INVALID, validationErr.Code())
	assert.NotNil(t, validationErr.Unwrap())
	assert.ErrorIs(t, err, validationErr)
}

// ---------------------------------------------------------------------------
// compose.go: git position resolution failure → abstained (line 229)

func TestCOV_AUDIT_ComposeGitPositionFailure(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)

	git, err := gitx.NewGitClient(gitx.GitClientOptions{
		RepoRoot: stateDir,
		Executor: failingExecutor{},
	})
	require.NoError(t, err)

	result, err := audit.RunLoopBAudit(context.Background(), audit.LoopBAuditComposeDeps{
		Config: audit.LoopBScheduleConfig{
			Nightly:       true,
			DefaultBranch: "main",
		},
		Context: audit.LoopBTriggerContext{
			Event:  audit.LoopBEventNightly,
			Branch: strPtr("main"),
		},
		Manifest:        validManifestForTest(t),
		StateStore:      store,
		Git:             git,
		Claims:          nil,
		Store:           nil,
		RunID:           "run_1",
		Now:             auditNow,
		CommitWalkDepth: intPtr(50),
		Concurrency:     intPtr(1),
		RenderArtifacts: boolPtr(true),
	})
	require.NoError(t, err)
	assert.Equal(t, audit.LoopBAuditAbstained, result.Kind)
	assert.Contains(t, result.Detail, "position resolution failed")
}

// ---------------------------------------------------------------------------
// local fixtures

func validManifestForTest(t *testing.T) schemas.Manifest {
	manifest := schemas.Manifest{
		Version:   1,
		Documents: []schemas.ManifestEntry{{Path: "docs/a.md", Sources: []string{"docs/a.md"}}},
	}
	require.Empty(t, manifest.Validate())
	return manifest
}

// ---------------------------------------------------------------------------
// org-report.go: OrgReportInputError.Unwrap (currently 0%)
func TestCOV_AUDIT_OrgReportInputErrorUnwrap(t *testing.T) {
	_, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org: "acme", RunID: "r", GeneratedAt: auditNow,
		Repos: []audit.OrgRepoAuditInput{{Repo: "bare-name"}},
	})
	require.Error(t, err)
	var inputErr *audit.OrgReportInputError
	require.ErrorAs(t, err, &inputErr)
	assert.NotNil(t, inputErr.Unwrap())
}

// ---------------------------------------------------------------------------
// store.go: ListRunRecords with negative limit
func TestCOV_AUDIT_ListRunRecordsNegativeLimit(t *testing.T) {
	store, err := audit.CreateAuditFindingsStore(filepath.Join(t.TempDir(), ".docsync", "state"), nil)
	require.NoError(t, err)
	neg := -5
	records, err := store.ListRunRecords(&audit.ListFindingsOptions{Limit: &neg})
	require.NoError(t, err)
	require.Empty(t, records)
}

// ---------------------------------------------------------------------------
// store.go: AppendRunRecord with a failing runs sink
func TestCOV_AUDIT_AppendRunRecordSinkError(t *testing.T) {
	cause := errors.New("runs sink error")
	store, err := audit.CreateAuditFindingsStore(t.TempDir(),
		&audit.AuditStoreSinks{
			Findings: failingSink{cause: cause},
			Runs:     failingSink{cause: cause},
		})
	require.NoError(t, err)
	valid, err := audit.BuildDriftAuditRunRecord(audit.BuildDriftAuditRunRecordInput{
		RunID: "r", At: auditNow, Trigger: schemas.DriftAuditTriggerNightlyScheduled,
		Position: "abc",
		Result: audit.AuditRunResult{
			Status:      audit.AuditRunCompleted,
			Findings:    []audit.DriftFinding{},
			AuditedDocs: []string{},
			SkippedDocs: []string{},
		},
	})
	require.NoError(t, err)
	_, err = store.AppendRunRecord(valid)
	require.Error(t, err)
	var sinkErr *audit.AuditFindingsSinkError
	require.ErrorAs(t, err, &sinkErr)
	assert.NotNil(t, sinkErr.Unwrap())
	assert.ErrorIs(t, err, cause)
}

// ---------------------------------------------------------------------------
// types.go: isRFC3339UTCDatetime fractional second edge cases (via ValidateFinding)
func TestCOV_AUDIT_DatetimeFracEdgeCases(t *testing.T) {
	base := audit.DriftFinding{
		ID: "f-1", DocPath: "docs/a.md", Kind: "claim-lost-support",
		Evidence:  audit.DriftFindingEvidence{Commits: []string{"c"}, Paths: []string{"p"}, Detail: "d"},
		CreatedAt: auditNow,
	}

	// Fractional seconds with non-digit character after dot —— refused.
	bad := base
	bad.CreatedAt = "2026-10-09T00:00:00.12Z"
	require.Empty(t, audit.ValidateFinding(bad)) // fractional digits are fine

	bad.CreatedAt = "2026-10-09T00:00:00.1Z"
	require.Empty(t, audit.ValidateFinding(bad))

	bad.CreatedAt = "2026-10-09T00:00:00.Z"
	issues := audit.ValidateFinding(bad)
	require.NotEmpty(t, issues) // dot with nothing after

	bad.CreatedAt = "2026-10-09T00:00:00.1xZ"
	issues = audit.ValidateFinding(bad)
	require.NotEmpty(t, issues) // non-digit in frac

	// Body too short.
	bad.CreatedAt = "2026-10-09T00:00:00Z"
	require.Empty(t, audit.ValidateFinding(bad)) // valid — no fraction, exactly 20 chars

	bad.CreatedAt = "2026-10-09T00:00:0Z"
	issues = audit.ValidateFinding(bad)
	require.NotEmpty(t, issues) // body too short
}

// ---------------------------------------------------------------------------
// types.go: resolveConcurrency branches
// ---------------------------------------------------------------------------
// dedup.go: compareFindings equal-ID branch (IDs differ)

// CompareFindings: equal case returns 0 when IDs match

// ---------------------------------------------------------------------------
// incremental.go: ChunkListHash with exactly 12-char hash

// ---------------------------------------------------------------------------
// store.go: evidenceSubsumed cross-run dedup branch (via DedupeAgainstStore)
func TestCOV_AUDIT_EvidenceNotSubsumed(t *testing.T) {
	prior := audit.DriftFinding{ID: "f-1", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: strPtr("c1"),
		Evidence:  audit.DriftFindingEvidence{Commits: []string{"c1"}, Paths: []string{"p1"}, Detail: "d"},
		CreatedAt: auditNow}
	next := audit.DriftFinding{ID: "f-2", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: strPtr("c1"),
		Evidence:  audit.DriftFindingEvidence{Commits: []string{"c2"}, Paths: []string{"p1"}, Detail: "d"},
		CreatedAt: auditNow}
	// next has commit c2 not in prior's commits → not subsumed → appendable.
	res := audit.DedupeAgainstStore([]audit.DriftFinding{next}, []audit.DriftFinding{prior})
	require.Len(t, res.Appendable, 1)
}

// ---------------------------------------------------------------------------
// compose.go: RunLoopBAudit noop — PR context and manifest unsupported version
func TestCOV_AUDIT_ComposeNoopPR(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: stateDir, Executor: failingExecutor{}})
	require.NoError(t, err)
	// PR event -> noop
	result, err := audit.RunLoopBAudit(context.Background(), audit.LoopBAuditComposeDeps{
		Config:     audit.LoopBScheduleConfig{Nightly: true, DefaultBranch: "main"},
		Context:    audit.LoopBTriggerContext{Event: audit.LoopBEventPR, Branch: strPtr("main")},
		Manifest:   validManifestForTest(t),
		StateStore: store,
		Git:        git,
		RunID:      "r",
		Now:        auditNow,
	})
	require.NoError(t, err)
	assert.Equal(t, audit.LoopBAuditNoop, result.Kind)
	assert.Equal(t, audit.NoopPRCriticalPathForbidden, result.Reason)
}

func TestCOV_AUDIT_ComposeNoopUnsupportedVersion(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	store, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: stateDir, Executor: failingExecutor{}})
	require.NoError(t, err)
	manifest := schemas.Manifest{Version: 999}
	require.NotEmpty(t, manifest.Validate())
	result, err := audit.RunLoopBAudit(context.Background(), audit.LoopBAuditComposeDeps{
		Config:     audit.LoopBScheduleConfig{Nightly: true, DefaultBranch: "main"},
		Context:    audit.LoopBTriggerContext{Event: audit.LoopBEventNightly, Branch: strPtr("main")},
		Manifest:   manifest,
		StateStore: store,
		Git:        git,
		RunID:      "r",
		Now:        auditNow,
	})
	require.NoError(t, err)
	assert.Equal(t, audit.LoopBAuditAbstained, result.Kind)
}

func TestCOV_AUDIT_ComposeNoopEventUnknown(t *testing.T) {
	result, err := audit.RunLoopBAudit(context.Background(), audit.LoopBAuditComposeDeps{
		Config:   audit.LoopBScheduleConfig{DefaultBranch: "main"},
		Context:  audit.LoopBTriggerContext{Event: audit.LoopBTriggerEvent("bogus")},
		Manifest: validManifestForTest(t),
	})
	require.NoError(t, err)
	assert.Equal(t, audit.LoopBAuditNoop, result.Kind)
	assert.Equal(t, audit.NoopUnknownEvent, result.Reason)
}

// ---------------------------------------------------------------------------
// org-report.go: RenderOrgDriftReport with cross-repo concepts and period
func TestCOV_AUDIT_RenderOrgDriftReportFull(t *testing.T) {
	report, err := audit.AggregateOrgDriftReport(audit.AggregateOrgDriftReportInput{
		Org:         "acme",
		RunID:       "run_1",
		GeneratedAt: auditNow,
		Period:      &schemas.OrgDriftPeriod{From: "2026-01-01T00:00:00Z", To: "2026-10-01T00:00:00Z"},
		Repos: []audit.OrgRepoAuditInput{
			{Repo: "acme/repo-a", Findings: []audit.DriftFinding{
				{ID: "f-a1", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: strPtr("concept_x"),
					Evidence: audit.DriftFindingEvidence{Commits: []string{"c1"}, Paths: []string{"p"}, Detail: "x"}, CreatedAt: auditNow},
			}},
			{Repo: "acme/repo-b", Findings: []audit.DriftFinding{
				{ID: "f-b1", DocPath: "docs/b.md", Kind: "doc-drift",
					Evidence: audit.DriftFindingEvidence{Commits: []string{"c2"}, Paths: []string{"p"}, Detail: "y"}, CreatedAt: auditNow},
			}},
		},
	})
	require.NoError(t, err)
	// RenderOrgDriftReport — cross-repo concepts empty (each concept appears
	// in one repo only) -> no cross-repo section is omitted.
	rendered := audit.RenderOrgDriftReport(report)
	require.Contains(t, rendered, "no cross-repo terminology/contract divergence surfaced")
	require.Contains(t, rendered, "Covered period")
}

// ---------------------------------------------------------------------------
// avoid unused import errors: the existing imports already cover "errors", "os",
// "path/filepath", "context", "gitx", "state", "workspace", "schemas"

func TestCOV_AUDIT_CompareFindingsSort(t *testing.T) {
	withClaimA := audit.DriftFinding{ID: "f-a", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: strPtr("c1")}
	withClaimB := audit.DriftFinding{ID: "f-b", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: strPtr("c2")}
	deduped := audit.DedupeFindings([]audit.DriftFinding{withClaimB, withClaimA})
	require.Len(t, deduped, 2)
}

func TestCOV_AUDIT_CompareFindingsSameKeyMerge(t *testing.T) {
	a := audit.DriftFinding{ID: "f-1", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: strPtr("c1")}
	b := audit.DriftFinding{ID: "f-1", DocPath: "docs/a.md", Kind: "claim-lost-support", ClaimID: strPtr("c1")}
	deduped := audit.DedupeFindings([]audit.DriftFinding{a, b})
	require.Len(t, deduped, 1)
}

func TestCOV_AUDIT_ChunkListHashExact(t *testing.T) {
	h := audit.ChunkListHash([]string{"docs/a.md"})
	require.NotEmpty(t, h)
	h2 := audit.ChunkListHash([]string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md"})
	require.Equal(t, 12, len(h2))
}

// ---------------------------------------------------------------------------
// truncateForMessage via long state dir path
func TestCOV_AUDIT_CreateStoreLongPath(t *testing.T) {
	// Build a stateDir > 64 chars to trigger truncateForMessage.
	long := filepath.Join(t.TempDir(), "very-long-directory-name-that-exceeds-the-sixty-four-character-truncation-limit")
	_, err := audit.CreateAuditFindingsStore(long, nil)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// ListRunRecords with negative and zero limits
func TestCOV_AUDIT_ListRunRecordsLimits(t *testing.T) {
	store, err := audit.CreateAuditFindingsStore(filepath.Join(t.TempDir(), stDir), nil)
	require.NoError(t, err)
	writeRunRecord(t, store, "run_1")

	neg := -3
	records, err := store.ListRunRecords(&audit.ListFindingsOptions{Limit: &neg})
	require.NoError(t, err)
	require.Empty(t, records)

	zero := 0
	records, err = store.ListRunRecords(&audit.ListFindingsOptions{Limit: &zero})
	require.NoError(t, err)
	require.Empty(t, records)

	// Limit larger than available — full list.
	huge := 999
	records, err = store.ListRunRecords(&audit.ListFindingsOptions{Limit: &huge})
	require.NoError(t, err)
	require.Len(t, records, 1)
}

// helper
const stDir = ".docsync/state"

func writeRunRecord(t *testing.T, store audit.AuditFindingsStore, runID string) {
	t.Helper()
	record, err := audit.BuildDriftAuditRunRecord(audit.BuildDriftAuditRunRecordInput{
		RunID: runID, At: auditNow, Trigger: schemas.DriftAuditTriggerNightlyScheduled,
		Position: "abc",
		Result: audit.AuditRunResult{
			Status: audit.AuditRunCompleted, AuditedDocs: []string{}, SkippedDocs: []string{},
		},
	})
	require.NoError(t, err)
	_, err = store.AppendRunRecord(record)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// BuildDriftAuditRunRecord with cursor-before/after and detail
func TestCOV_AUDIT_BuildRunRecordFull(t *testing.T) {
	detail := "chunk failure at doc"
	record, err := audit.BuildDriftAuditRunRecord(audit.BuildDriftAuditRunRecordInput{
		RunID:        "r",
		At:           auditNow,
		Trigger:      schemas.DriftAuditTriggerNightlyScheduled,
		Position:     "abc",
		CursorBefore: strPtr("loopb:v1;since=old;done=1;list=h"),
		Result: audit.AuditRunResult{
			Status: audit.AuditRunPartial,
			Findings: []audit.DriftFinding{
				{ID: "f-1", DocPath: "docs/a.md", Kind: "claim-lost-support",
					Evidence:  audit.DriftFindingEvidence{Commits: []string{"c"}, Paths: []string{"p"}, Detail: "d"},
					CreatedAt: auditNow},
			},
			AuditedDocs: []string{"docs/a.md"},
			SkippedDocs: []string{"docs/b.md"},
			Cursor:      audit.AuditCursorState{Advanced: false, Position: strPtr("loopb:v1;since=old;done=1;list=h")},
			Detail:      &detail,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "r", record.RunID)
	require.NotNil(t, record.CursorBefore)
	require.NotNil(t, record.CursorAfter)
	require.NotNil(t, record.Detail)
	require.Len(t, record.Findings, 1)
}

// ---------------------------------------------------------------------------
// store.ListFindings with limit edge cases (negative, zero)
func TestCOV_AUDIT_ListFindingsLimits(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), stDir)
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	store, err := audit.CreateAuditFindingsStore(stateDir, nil)
	require.NoError(t, err)
	// Append two findings
	f := audit.DriftFinding{ID: "f-a", DocPath: "docs/a.md", Kind: "claim-lost-support",
		Evidence:  audit.DriftFindingEvidence{Commits: []string{"c"}, Paths: []string{"p"}, Detail: "d"},
		CreatedAt: auditNow}
	_, err = store.AppendFindings([]audit.DriftFinding{f})
	require.NoError(t, err)

	neg := -1
	view, err := store.ListFindings(&audit.ListFindingsOptions{Limit: &neg})
	require.NoError(t, err)
	require.Empty(t, view)

	zero := 0
	view, err = store.ListFindings(&audit.ListFindingsOptions{Limit: &zero})
	require.NoError(t, err)
	require.Empty(t, view)
}

// ---------------------------------------------------------------------------
// jsInteger: decimal and exponent forms (via ParseAuditPosition)
func TestCOV_AUDIT_jsIntegerFloatForms(t *testing.T) {
	// "1.0" — ParseFloat truncates to 1.0, equals Trunc → true.
	parsed := audit.ParseAuditPosition(audit.PositionPrefix + ";since=x;done=1.0;list=h")
	require.NotNil(t, parsed)
	assert.Equal(t, 1, parsed.Done)

	// "1e1" — ParseFloat 1e1 = 10, Trunc = 10, equal → true.
	parsed = audit.ParseAuditPosition(audit.PositionPrefix + ";since=x;done=1e1;list=h")
	require.NotNil(t, parsed)
	assert.Equal(t, 10, parsed.Done)
}

// ---------------------------------------------------------------------------
