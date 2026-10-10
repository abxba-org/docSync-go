package analysis_test

// Ported from packages/core/test/pipeline-caps.test.ts (P1-T20; V-P1-4; ARCH
// §3.7). Cap breach on the monorepo-scale fixture is a DOWNGRADE, never an
// inconclusive run (D14): the record stays valid, deep_analysis is skipped with
// the exact cap-breach reason, the classification hints carry the REVIEW-routing
// `other` flag, analyzer coverage is still declared, per-file rows are
// path-level only, and the structural SUSPECT driver still routes the affected
// doc VERIFIED→SUSPECT deterministically with ZERO model invocations.
//
// Contrast: generous caps keep deep_allowed (the seam echoed by a recording
// double) and deep skips for the DECLARED V1 reason with NO REVIEW hint.
//
// Finally the structural zero-model audit: a static import audit proving the
// pipeline-stage Go packages import no network or model surface.

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
)

const (
	// monorepoPackageADoc is the package-a doc of the monorepo-scale fixture.
	monorepoPackageADoc = "docs/packages/a.md"

	pipelineCapsAt       = "2026-10-05T12:00:00Z"
	pipelineCapsHintNote = "deep_analysis skipped (cap breach) — route REVIEW"
)

// newMonorepoPipelineRepo materializes the monorepo-scale fixture in a fresh
// caller-owned git repo (TS createMonorepoPipelineRepo).
func newMonorepoPipelineRepo(t *testing.T) *testkit.MonorepoPipeline {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))
	pipeline, err := testkit.BuildMonorepoPipeline(root)
	require.NoError(t, err)
	return pipeline
}

// touchPackageASources appends ONE deterministic line to the first count
// package-a sources (TS touchPackageASources).
func touchPackageASources(t *testing.T, pipeline *testkit.MonorepoPipeline, count int) []string {
	t.Helper()
	candidates, err := pipeline.Files("packages/a/src")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(candidates), count)
	changed := candidates[:count]
	for _, relPath := range changed {
		content, err := pipeline.Read(relPath)
		require.NoError(t, err)
		require.NoError(t, pipeline.Write(relPath, content+"// pipeline cap test: deterministic content change\n"))
	}
	return changed
}

// pipelineCapsRecordingPlugin is the recording generic double (TS
// recordingGenericAnalyzerDouble): it records the deep_allowed seam the builder
// exposes to analyzers.
type pipelineCapsRecordingPlugin struct {
	seen []bool
}

func (p *pipelineCapsRecordingPlugin) Name() schemas.AnalyzerName { return schemas.AnalyzerNameGeneric }

func (p *pipelineCapsRecordingPlugin) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	p.seen = append(p.seen, input.DeepAllowed)
	files := make([]analysis.PerFileFinding, 0, len(input.Files))
	for _, file := range input.Files {
		files = append(files, analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: false,
			APIDiff:        false,
			ConfigDiff:     false,
			DeepDeclined:   !input.DeepAllowed,
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

// TestV_P1_4_CapBreachOnMonorepoFixtureDowngradesDeterministically ports the
// headline cap case.
func TestV_P1_4_CapBreachOnMonorepoFixtureDowngradesDeterministically(t *testing.T) {
	pipeline := newMonorepoPipelineRepo(t)
	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)
	require.NoError(t, pipeline.SeedVerifiedDoc(monorepoPackageADoc, base))

	// 6 > maxFiles 5 → breach.
	changed := touchPackageASources(t, pipeline, 6)
	head, err := pipeline.CommitPaths(changed, "feat: touch six package-a sources")
	require.NoError(t, err)

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:     testkit.MonorepoRepoID,
		RepoRoot: pipeline.Root,
		Git:      pipeline.Git,
		Base:     base,
		Head:     head,
		Plugins:  pipelinePluginLineup(),
		Caps:     &analysis.AnalysisCaps{MaxFiles: 5, MaxDiffSize: 200},
	})
	require.NoError(t, err)
	record := assertPipelinedAnalysis(t, outcome)

	// The record is VALID — a downgrade, never an inconclusive run (D14).
	require.Len(t, record.Files, 6)
	for _, row := range record.Files {
		assert.Equal(t, schemas.FileStatusModified, row.Status)
		// Path-level downgrade: ASSESSED booleans, all false — declared per
		// row, never silent.
		require.NotNil(t, row.SymbolsChanged)
		assert.False(t, *row.SymbolsChanged)
		require.NotNil(t, row.APIDiff)
		assert.False(t, *row.APIDiff)
		require.NotNil(t, row.ConfigDiff)
		assert.False(t, *row.ConfigDiff)
	}
	assert.False(t, record.DependenciesChanged)

	// deep_analysis skipped with the exact cap-breach reason + REVIEW flag.
	assert.Equal(t, schemas.DeepAnalysisSkipped, record.DeepAnalysis.Status)
	require.NotNil(t, record.DeepAnalysis.Reason)
	assert.Equal(t, "cap breach (6 files / 6 bytes > caps)", *record.DeepAnalysis.Reason)
	assert.Equal(t, []schemas.ClassificationHint{
		{Kind: schemas.ClassificationHintOther, Note: pipelineCapsHintNote},
	}, record.ClassificationHints)
	// Coverage still declared: the stub gap + the generic analyzer ok.
	requirePipelineCoverage(t, record)

	// Structural routing under the breach: the doc still flips
	// VERIFIED→SUSPECT deterministically, with ZERO model invocations (the
	// REVIEW flag defers the DECISION stage, not structural routing).
	report := runSuspectDriver(t, &pipeline.FixtureRepo, pipeline.Graph, record, head)
	assert.Zero(t, report.ModelInvocations)
	assert.Equal(t, 1, report.TransitionedCount)
	entry, found := pipelineReportEntry(report, monorepoPackageADoc)
	require.True(t, found)
	require.NotNil(t, entry.From)
	require.NotNil(t, entry.To)
	assert.Equal(t, schemas.DocStateVerified, *entry.From)
	assert.Equal(t, schemas.DocStateSuspect, *entry.To)
	assert.True(t, entry.Transitioned)

	// §6.2 intersection from BOTH evidence bases: 6 + 6 deduped reasons, all
	// naming the package-a source tree.
	require.Len(t, entry.Reasons, 12)
	for _, reason := range entry.Reasons {
		assert.Equal(t, baselines.ReasonSourceChanged, reason.Kind)
		assert.Contains(t, reason.Detail, "packages/a/src/")
		assert.True(t,
			strings.Contains(reason.Detail, `source "packages/a/src/**" matches`) ||
				strings.HasPrefix(reason.Detail, `source "packages/a/src/`),
			"reason wording: %s", reason.Detail)
	}
	// The mapping (glob) wording is present exactly once per changed file.
	globWorded := 0
	for _, reason := range entry.Reasons {
		if strings.Contains(reason.Detail, `source "packages/a/src/**" matches`) {
			globWorded++
		}
	}
	assert.Equal(t, 6, globWorded)

	// The seven untouched docs have no state record: reported, never routed.
	fromNil := 0
	for _, candidate := range report.Entries {
		if candidate.From == nil {
			fromNil++
		}
	}
	assert.Equal(t, 7, fromNil)
}

// TestPipelineCapsContrastGenerousCapsKeepDeepAllowed ports the contrast case.
func TestPipelineCapsContrastGenerousCapsKeepDeepAllowed(t *testing.T) {
	pipeline := newMonorepoPipelineRepo(t)
	base, err := pipeline.CommitAll("base")
	require.NoError(t, err)

	changed := touchPackageASources(t, pipeline, 6)
	head, err := pipeline.CommitPaths(changed, "feat: touch six package-a sources")
	require.NoError(t, err)

	recorder := &pipelineCapsRecordingPlugin{}
	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:     testkit.MonorepoRepoID,
		RepoRoot: pipeline.Root,
		Git:      pipeline.Git,
		Base:     base,
		Head:     head,
		Plugins:  []analysis.AnalyzerPlugin{pipelineTSStubPlugin{}, recorder},
		Caps:     &analysis.AnalysisCaps{MaxFiles: 100, MaxDiffSize: 100_000},
	})
	require.NoError(t, err)
	record := assertPipelinedAnalysis(t, outcome)

	// Deep analysis is still SKIPPED — but for the declared V1 coverage gap,
	// NOT a cap breach; no REVIEW-routing hint is emitted.
	requireDeepSkippedV1(t, record)
	assert.Empty(t, record.ClassificationHints)
	// The seam echoes deep_allowed; no cap notes nonsense.
	assert.Equal(t, []bool{true}, recorder.seen)
}

// TestStructuralZeroModelAudit_PipelineStagesImportNoNetworkOrModelSurface
// ports the static import audit: zero model calls is a STRUCTURAL property,
// not just a counter.
func TestStructuralZeroModelAudit_PipelineStagesImportNoNetworkOrModelSurface(t *testing.T) {
	// The Go import-specifier equivalents of the TS FORBIDDEN list: the
	// network and model surfaces.
	forbidden := []string{"net", "net/http", "openrouter"}

	dirs := []string{
		"../graph", "../claims", "../contracts", "../cache",
		"../retrieval", "../baselines", "../statemachine", ".",
	}

	audited := 0
	var offenders []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			audited++
			path := filepath.Join(dir, name)
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("audited %s failed to parse: %v", path, err)
			}
			for _, spec := range parsed.Imports {
				specifier := spec.Path.Value
				specifier = strings.Trim(specifier, `"`)
				for _, candidate := range forbidden {
					if strings.Contains(specifier, candidate) {
						offenders = append(offenders, path+": "+specifier)
					}
				}
			}
		}
	}
	assert.Positive(t, audited, "the audit must actually look at the pipeline sources")
	assert.Empty(t, offenders, "pipeline stages must import no network or model surface")
}
