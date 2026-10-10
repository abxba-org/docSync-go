// Ported from packages/core/test/pipeline-invalidation.test.ts (P1-T20;
// ARCH §4.1/§4.3/§6.1/§6.2; V-P1-2/V-P1-6) with the pipeline plugin doubles
// from pipeline-plugins.ts — package analysis_test (black-box, testify).
//
// The TypeScript workspace could not import the real analyzer packages from a
// core suite (dependency cycle), so the TS suite stood up local doubles
// mirroring the real packages' exact note strings. The Go DAG (plan.md §3)
// keeps internal/analyzers/* above internal/analysis, so this port keeps the
// same discipline: the doubles below are the pipeline lineup, and the stub
// note is taken from the plugin-seam constant itself
// (analysis.TypeScriptStubNote — the same string the real stub carries).
package analysis_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/statemachine"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

/* ------------------------------------------- pipeline plugin doubles (TS pipeline-plugins.ts) */

// The fixed verdict texts (TS module constants).
const (
	pipelineDeepSkippedV1Reason = "no type-aware analyzer coverage in V1 (typescript stub; tree-sitter lands Phase 11)"
	// Verbatim from internal/analyzers/generic/analyzer.go (GENERIC_COVERAGE_NOTE).
	pipelineGenericCoverageNote = "path-level analyzer, always present (OD-4); symbol/type-level analysis lands Phase 11 (F11-1/F11-2)"
	// Verbatim from internal/analyzers/generic/analyzer.go (GENERIC_PATH_LEVEL_NOTE).
	pipelineGenericPathLevelNote = "generic path-level analyzer (V1): no symbol/AST extraction — declared coverage gap"
)

const (
	pipelineInvalidationRun = "run_p1t20_suspect"
	pipelineSuspectAt       = "2026-10-05T12:00:00Z"
)

// pipelineStrPtr returns a pointer to s (test-only optional-string helper;
// distinct from the sibling suites' helpers in this shared test package).
func pipelineStrPtr(s string) *string { return &s }

// pipelineTSStubPlugin mirrors typescriptAnalyzerStubDouble: the V1 stub
// declares its gap and emits no findings.
type pipelineTSStubPlugin struct{}

func (pipelineTSStubPlugin) Name() schemas.AnalyzerName { return schemas.AnalyzerNameTypeScript }

func (pipelineTSStubPlugin) Analyze(analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusStub,
			Note:     pipelineStrPtr(analysis.TypeScriptStubNote),
		},
		Files: nil,
	}, nil
}

// pipelineGenericPlugin mirrors genericAnalyzerDouble: the always-present
// path-level analyzer declares its gap for EVERY input file (deleted and
// renamed included, in input order).
type pipelineGenericPlugin struct{}

func (pipelineGenericPlugin) Name() schemas.AnalyzerName { return schemas.AnalyzerNameGeneric }

func (pipelineGenericPlugin) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	files := make([]analysis.PerFileFinding, 0, len(input.Files))
	for _, file := range input.Files {
		files = append(files, analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: false,
			APIDiff:        false,
			ConfigDiff:     false,
			DeepDeclined:   true,
			Notes:          []string{pipelineGenericPathLevelNote},
		})
	}
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameGeneric,
			Status:   schemas.AnalyzerStatusOK,
			Note:     pipelineStrPtr(pipelineGenericCoverageNote),
		},
		Files: files,
	}, nil
}

// pipelinePluginLineup mirrors the TS helper: TypeScript stub FIRST, generic
// path-level second (OD-4: 'typescript' before 'generic', both always present).
func pipelinePluginLineup() []analysis.AnalyzerPlugin {
	return []analysis.AnalyzerPlugin{pipelineTSStubPlugin{}, pipelineGenericPlugin{}}
}

/* ------------------------------------------------------------- repo helpers */

// newPaymentsPipelineRepo materializes the payments fixture in a fresh
// caller-owned git repo (TS createPaymentsPipelineRepo).
func newPaymentsPipelineRepo(t *testing.T) *testkit.PaymentsPipeline {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))
	pipeline, err := testkit.BuildPaymentsPipeline(root)
	require.NoError(t, err)
	return pipeline
}

// assertPipelinedAnalysis unwraps the builder outcome (TS assertAnalysis).
func assertPipelinedAnalysis(t *testing.T, outcome analysis.AnalysisOutcome) *schemas.ChangeAnalysis {
	t.Helper()
	require.Equal(t, analysis.AnalysisOutcomeKindAnalysis, outcome.Kind,
		"expected analysis outcome, got %+v", outcome)
	require.NotNil(t, outcome.Record)
	return outcome.Record
}

// pipelineCanonicalJSON serializes a record through the same canonical path
// the committed state bytes take (TS canonicalJson).
func pipelineCanonicalJSON(t *testing.T, value any) string {
	t.Helper()
	wire, err := json.Marshal(value)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.UseNumber()
	var generic any
	require.NoError(t, decoder.Decode(&generic))
	out, err := docutil.CanonicalJSON(generic)
	require.NoError(t, err)
	return out
}

// pipelineClaims maps the store's claims onto the driver's claim shapes
// (TS `await repo.claims.list()`). A nil store yields no claim evidence.
func pipelineClaims(t *testing.T, store *claims.ClaimStore) []baselines.InvalidationClaim {
	t.Helper()
	if store == nil {
		return nil
	}
	stored, err := store.List()
	require.NoError(t, err)
	out := make([]baselines.InvalidationClaim, 0, len(stored))
	for _, claim := range stored {
		out = append(out, baselines.InvalidationClaim{
			ID:     claim.ID,
			Source: baselines.InvalidationClaimSource{File: claim.Source.File},
		})
	}
	return out
}

// buildPipelineAnalysis runs the builder with the pipeline lineup (TS the
// shared buildChangeAnalysis call).
func buildPipelineAnalysis(
	t *testing.T,
	pipeline *testkit.PaymentsPipeline,
	base, head string,
	caps *analysis.AnalysisCaps,
) *schemas.ChangeAnalysis {
	t.Helper()
	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:     testkit.PaymentsRepoID,
		RepoRoot: pipeline.Root,
		Git:      pipeline.Git,
		Base:     base,
		Head:     head,
		Plugins:  pipelinePluginLineup(),
		Caps:     caps,
	})
	require.NoError(t, err)
	return assertPipelinedAnalysis(t, outcome)
}

// runSuspectDriver drives the deterministic SUSPECT pass (TS the shared
// markSuspectsFromAnalysis call) over one pipeline fixture's repo + graph.
func runSuspectDriver(
	t *testing.T,
	repo *testkit.FixtureRepo,
	docGraph graph.DocumentGraph,
	record *schemas.ChangeAnalysis,
	head string,
) statemachine.SuspectReport {
	t.Helper()
	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), statemachine.SuspectDriverInput{
		Store:         repo.Store,
		Graph:         docGraph,
		Analysis:      *record,
		Git:           repo.Git,
		Head:          head,
		RunID:         pipelineInvalidationRun,
		PolicyVersion: testkit.PipelinePolicyVersion,
		At:            pipelineSuspectAt,
		Claims:        nil,
	})
	require.NoError(t, err)
	return report
}

// runPaymentsSuspectDriver drives the SUSPECT pass with the payments claims
// evidence attached (the payments fixture carries hand-anchored claims).
func runPaymentsSuspectDriver(
	t *testing.T,
	pipeline *testkit.PaymentsPipeline,
	record *schemas.ChangeAnalysis,
	head string,
) statemachine.SuspectReport {
	t.Helper()
	report, err := statemachine.MarkSuspectsFromAnalysis(context.Background(), statemachine.SuspectDriverInput{
		Store:         pipeline.Store,
		Graph:         pipeline.Graph,
		Analysis:      *record,
		Git:           pipeline.Git,
		Head:          head,
		RunID:         pipelineInvalidationRun,
		PolicyVersion: testkit.PipelinePolicyVersion,
		At:            pipelineSuspectAt,
		Claims:        pipelineClaims(t, pipeline.Claims),
	})
	require.NoError(t, err)
	return report
}

// applyIdempotencyTypeChange applies the V-P1-2 type-level change
// (`idempotencyKey: string -> idempotencyKey?: string`).
func applyIdempotencyTypeChange(t *testing.T, pipeline *testkit.PaymentsPipeline) {
	t.Helper()
	const refundFile = "src/payments/refund.ts"
	before, err := pipeline.Read(refundFile)
	require.NoError(t, err)
	after := strings.Replace(before, "\tidempotencyKey: string;", "\tidempotencyKey?: string;", 1)
	require.NotEqual(t, before, after, "type-level change did not apply — payments fixture drifted")
	require.NoError(t, pipeline.Write(refundFile, after))
}

// requirePipelineReasons asserts the exact ordered reason list of one entry.
func requirePipelineReasons(t *testing.T, entry statemachine.SuspectReportEntry, want []baselines.InvalidationReason) {
	t.Helper()
	assert.Equal(t, want, entry.Reasons)
}

// requirePipelineCoverage asserts the pipeline lineup's declared coverage.
func requirePipelineCoverage(t *testing.T, record *schemas.ChangeAnalysis) {
	t.Helper()
	assert.Equal(t, []schemas.AnalyzerCoverage{
		{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusStub,
			Note:     pipelineStrPtr(analysis.TypeScriptStubNote),
		},
		{
			Analyzer: schemas.AnalyzerNameGeneric,
			Status:   schemas.AnalyzerStatusOK,
			Note:     pipelineStrPtr(pipelineGenericCoverageNote),
		},
	}, record.AnalyzerCoverage)
}

// requireDeepSkippedV1 asserts the declared V1 deep-analysis skip.
func requireDeepSkippedV1(t *testing.T, record *schemas.ChangeAnalysis) {
	t.Helper()
	assert.Equal(t, schemas.DeepAnalysisSkipped, record.DeepAnalysis.Status)
	require.NotNil(t, record.DeepAnalysis.Reason)
	assert.Equal(t, pipelineDeepSkippedV1Reason, *record.DeepAnalysis.Reason)
}

// requirePathLevelRow asserts one path-level row: assessed booleans, all
// false (the declared path-level downgrade).
func requirePathLevelRow(t *testing.T, record *schemas.ChangeAnalysis, path, status string) {
	t.Helper()
	require.Len(t, record.Files, 1)
	row := record.Files[0]
	assert.Equal(t, path, row.Path)
	assert.Equal(t, schemas.FileStatus(status), row.Status)
	require.NotNil(t, row.SymbolsChanged)
	assert.False(t, *row.SymbolsChanged)
	require.NotNil(t, row.APIDiff)
	assert.False(t, *row.APIDiff)
	require.NotNil(t, row.ConfigDiff)
	assert.False(t, *row.ConfigDiff)
}

/* --------------------------------------------------------------- V-P1-2 */

// TestV_P1_2_MappedSourceChangeFlipsBaselineRoutesSuspectWithZeroModelCalls
// ports the headline invalidation case: a mapped source change flips the
// baseline and routes the doc VERIFIED→SUSPECT deterministically with ZERO
// model calls; the type-level case is the DECLARED V1 gap (deep_analysis
// skipped) — support loss is detected at MAPPING level, never at type level.
func TestV_P1_2_MappedSourceChangeFlipsBaselineRoutesSuspectWithZeroModelCalls(t *testing.T) {
	pipeline := newPaymentsPipelineRepo(t)

	// Materialization: commit B (fixture + concepts), claims intake, doc
	// state VERIFIED + baseline recorded against B.
	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)
	_, err = pipeline.IntakeHandAnchoredClaims()
	require.NoError(t, err)
	require.NoError(t, pipeline.SeedVerifiedDoc(base))
	claimBefore, err := pipeline.Claims.Get(testkit.ClaimIdempotency)
	require.NoError(t, err)
	require.NotNil(t, claimBefore)
	assert.Equal(t, schemas.ClaimStatusVerified, claimBefore.Status)

	// Step A — commit the type-level source change and build the analysis.
	applyIdempotencyTypeChange(t, pipeline)
	head, err := pipeline.CommitPaths([]string{"src/payments/refund.ts"},
		"feat: make refund idempotencyKey optional (string -> string?)")
	require.NoError(t, err)
	record := buildPipelineAnalysis(t, pipeline, base, head, nil)

	// DECLARED V1 gap (never silent): deep analysis skipped with the Phase-11
	// reason; analyzer coverage declares the typescript stub.
	requireDeepSkippedV1(t, record)
	requirePipelineCoverage(t, record)
	// Support loss is detected at MAPPING level: a plain modified row whose
	// booleans are PATH-LEVEL assessments (all false).
	requirePathLevelRow(t, record, "src/payments/refund.ts", "modified")
	assert.False(t, record.DependenciesChanged)
	assert.Empty(t, record.ClassificationHints)

	// Step B — the deterministic SUSPECT driver (the NAMED test's contract).
	report := runPaymentsSuspectDriver(t, pipeline, record, head)
	assert.Zero(t, report.ModelInvocations) // binding: zero model calls
	assert.Equal(t, 1, report.TransitionedCount)

	entry, found := pipelineReportEntry(report, testkit.PaymentsDoc)
	require.True(t, found, "payments doc missing from the suspect report")
	require.NotNil(t, entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	// Reasons name the evidence from BOTH §6.2 bases: the graph mapping's
	// declarative glob (intersected INDEPENDENT of the baseline) and the
	// baseline's resolved literal source, plus the intersecting claim id.
	requirePipelineReasons(t, entry, []baselines.InvalidationReason{
		{
			Kind:   baselines.ReasonClaimSourceChanged,
			Detail: `claims [claim_refund_idempotency] reference changed file "src/payments/refund.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/**" matches changed file "src/payments/refund.ts" (modified)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/refund.ts" matches changed file "src/payments/refund.ts" (modified)`,
		},
	})
	require.NotNil(t, entry.LadderRun)
	assert.Equal(t, baselines.RunAncestor, *entry.LadderRun) // baseline B is an ancestor of head C

	// The transition log is appended with run_id + an evidence-naming reason.
	docState, err := statemachine.NewDocStateMachine(pipeline.Store).GetState(testkit.PaymentsDoc)
	require.NoError(t, err)
	require.NotNil(t, docState)
	assert.Equal(t, schemas.DocStateSuspect, docState.State)
	require.Len(t, docState.Transitions, 2)
	appended := docState.Transitions[1]
	assert.Equal(t, schemas.DocStateSuspect, appended.State)
	assert.Equal(t, pipelineSuspectAt, appended.At)
	assert.Equal(t, pipelineInvalidationRun, appended.RunID)
	assert.Contains(t, appended.Reason,
		`source-changed: source "src/payments/refund.ts" matches changed file "src/payments/refund.ts" (modified)`)
	assert.Contains(t, docState.EvidenceRefs, record.RunID)

	// Step C — claim-level: the claim whose source.file === the changed file
	// transitions to `suspect` via the claims API (LoseSupport); the history
	// event carries the run id ("which claims lost support" groundwork).
	lost, err := claims.LoseSupport(pipeline.Claims, testkit.ClaimIdempotency, claims.ClaimTransitionOptions{
		RunID:  pipelineInvalidationRun,
		Reason: "mapped source src/payments/refund.ts changed (idempotencyKey: string -> string?)",
		At:     pipelineSuspectAt,
	})
	require.NoError(t, err)
	assert.True(t, lost.OK)
	claimAfter, err := pipeline.Claims.Get(testkit.ClaimIdempotency)
	require.NoError(t, err)
	require.NotNil(t, claimAfter)
	assert.Equal(t, schemas.ClaimStatusSuspect, claimAfter.Status)
	require.Len(t, claimAfter.History, 2)
	assert.Equal(t, schemas.ClaimHistoryEvent{
		At:    pipelineSuspectAt,
		RunID: pipelineInvalidationRun,
		Event: "support lost: mapped source src/payments/refund.ts changed (idempotencyKey: string -> string?)",
	}, claimAfter.History[1])

	// Contrast: the retry-window claim's source (client.ts) did NOT change —
	// it stays verified with its intake-only history.
	windowClaim, err := pipeline.Claims.Get(testkit.ClaimRetryWindow)
	require.NoError(t, err)
	require.NotNil(t, windowClaim)
	assert.Equal(t, schemas.ClaimStatusVerified, windowClaim.Status)
	assert.Len(t, windowClaim.History, 1)
}

/* --------------------------------------------------------------- V-P1-6 */

// TestV_P1_6_DeletedMappedSourceOrphansDocRoutesSuspectNeverDropped ports
// the orphan completion: a deleted mapped source yields the orphaned-edge
// reasons and the doc enters SUSPECT, never silently dropped.
func TestV_P1_6_DeletedMappedSourceOrphansDocRoutesSuspectNeverDropped(t *testing.T) {
	pipeline := newPaymentsPipelineRepo(t)

	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)
	_, err = pipeline.IntakeHandAnchoredClaims()
	require.NoError(t, err)
	require.NoError(t, pipeline.SeedVerifiedDoc(base))

	require.NoError(t, pipeline.Remove(testkit.PaymentsTypesSource))
	head, err := pipeline.CommitPaths([]string{testkit.PaymentsTypesSource}, "chore: delete types.ts")
	require.NoError(t, err)

	record := buildPipelineAnalysis(t, pipeline, base, head, nil)
	requirePathLevelRow(t, record, testkit.PaymentsTypesSource, "deleted")

	report := runPaymentsSuspectDriver(t, pipeline, record, head)
	assert.Zero(t, report.ModelInvocations)
	entry, found := pipelineReportEntry(report, testkit.PaymentsDoc)
	require.True(t, found)
	require.NotNil(t, entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)
	// Orphaned, never silently dropped: the deleted literal source (resolved
	// into the baseline at verification time) yields the intersection (glob
	// + literal wording) and the orphan reason. Manifest globs pin nothing,
	// so the graph contributes no edge here — the baseline identity speaks
	// for the orphan signal.
	requirePipelineReasons(t, entry, []baselines.InvalidationReason{
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/**" matches changed file "src/payments/types.ts" (deleted)`,
		},
		{
			Kind:   baselines.ReasonSourceChanged,
			Detail: `source "src/payments/types.ts" matches changed file "src/payments/types.ts" (deleted)`,
		},
		{
			Kind:   baselines.ReasonSourceOrphaned,
			Detail: `source "src/payments/types.ts" deleted`,
		},
	})

	docState, err := statemachine.NewDocStateMachine(pipeline.Store).GetState(testkit.PaymentsDoc)
	require.NoError(t, err)
	require.NotNil(t, docState)
	assert.Equal(t, schemas.DocStateSuspect, docState.State)
}

/* --------------------------------------------------------- pipeline purity */

// TestPipelinePurity_IdenticalMaterializationByteIdenticalAnalysisAndDecisions
// ports the purity contract: identical materializations (pinned git identity
// → identical SHAs) produce byte-identical ChangeAnalysis records and
// identical SuspectReport decisions.
func TestPipelinePurity_IdenticalMaterializationByteIdenticalAnalysisAndDecisions(t *testing.T) {
	runPipeline := func(t *testing.T) (*schemas.ChangeAnalysis, statemachine.SuspectReport) {
		t.Helper()
		pipeline := newPaymentsPipelineRepo(t)
		base, err := pipeline.CommitAll("base")
		require.NoError(t, err)
		_, err = pipeline.IntakeHandAnchoredClaims()
		require.NoError(t, err)
		require.NoError(t, pipeline.SeedVerifiedDoc(base))
		applyIdempotencyTypeChange(t, pipeline)
		head, err := pipeline.CommitPaths([]string{"src/payments/refund.ts"},
			"feat: make refund idempotencyKey optional (string -> string?)")
		require.NoError(t, err)
		record := buildPipelineAnalysis(t, pipeline, base, head, nil)
		report := runPaymentsSuspectDriver(t, pipeline, record, head)
		return record, report
	}

	firstRecord, firstReport := runPipeline(t)
	secondRecord, secondReport := runPipeline(t)

	// Identical materializations (pinned git identity/dates) → identical SHAs.
	assert.Equal(t, firstRecord.BaseSHA, secondRecord.BaseSHA)
	assert.Equal(t, firstRecord.HeadSHA, secondRecord.HeadSHA)

	// Byte-identical ChangeAnalysis (the SAME canonical serialization as
	// committed state) — stability across 2+ runs.
	assert.Equal(t, pipelineCanonicalJSON(t, firstRecord), pipelineCanonicalJSON(t, secondRecord))

	// Identical SuspectReport decisions (fixed injected `at`; deterministic
	// graph order, reason ordering, ladder walk).
	assert.Equal(t, firstReport.Entries, secondReport.Entries)
	assert.Equal(t, firstReport.TransitionedCount, secondReport.TransitionedCount)
	assert.Zero(t, firstReport.ModelInvocations)
	assert.Zero(t, secondReport.ModelInvocations)
}

/* ----------------------------------------------------------------- helpers */

// pipelineReportEntry finds one doc's entry in the report (TS the
// entries.find call).
func pipelineReportEntry(report statemachine.SuspectReport, doc string) (statemachine.SuspectReportEntry, bool) {
	for _, entry := range report.Entries {
		if entry.Doc == doc {
			return entry, true
		}
	}
	return statemachine.SuspectReportEntry{}, false
}
