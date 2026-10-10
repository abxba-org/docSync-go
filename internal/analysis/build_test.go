// Ported from packages/core/test/analysis-builder.test.ts (P1-T02, P1-T08,
// P1-T18; ARCH §4.1; D12/D14; V-P1-1/V-P1-4/V-P1-10 groundwork) and
// packages/core/test/analysis-caps.test.ts (P1-T08; V-P1-4/V-P11-3) —
// package analysis_test (black-box, testify), per CONVENTIONS.md.
//
// Real system git in fresh temp repos with pinned identity/dates so commit
// SHAs — and therefore the committed golden snapshot — are stable across runs
// and machines.
package analysis_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------- repo helper */

const gitPinnedDate = "2026-01-01T00:00:00+00:00"

// builderRepo is a fresh temp repository with pinned identity/dates.
type builderRepo struct {
	root string
}

// newBuilderRepo creates the repo (TS createBuilderRepo).
func newBuilderRepo(t *testing.T) *builderRepo {
	t.Helper()
	root := t.TempDir()
	gitInRepo(t, root, "init", "-b", "main")
	// Repo-local line-ending policy pinned before any file is written (DX-P11-3).
	gitInRepo(t, root, "config", "core.autocrlf", "false")
	gitInRepo(t, root, "config", "core.eol", "lf")
	return &builderRepo{root: root}
}

// gitInRepo runs git in dir with the pinned environment (TS GIT_ENV).
func gitInRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+gitPinnedDate,
		"GIT_COMMITTER_DATE="+gitPinnedDate,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func (r *builderRepo) write(t *testing.T, relPath, content string) {
	t.Helper()
	target := filepath.Join(r.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func (r *builderRepo) commitAll(t *testing.T, message string) string {
	t.Helper()
	identity := []string{
		"-c", "user.name=docsync-test",
		"-c", "user.email=docsync-test@invalid",
	}
	addArgs := append(append([]string(nil), identity...), "add", "-A")
	gitInRepo(t, r.root, addArgs...)
	commitArgs := append(append([]string(nil), identity...), "commit", "-q", "-m", message)
	gitInRepo(t, r.root, commitArgs...)
	return gitInRepo(t, r.root, "rev-parse", "HEAD")
}

func (r *builderRepo) gitClient(t *testing.T) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: r.root})
	require.NoError(t, err)
	return client
}

/* ------------------------------------------------------------ plugin stubs */

// tsStubAnalyzer mirrors tsStubPlugin.
func tsStubAnalyzer() analysis.AnalyzerPlugin {
	return fixedOutputPlugin{name: schemas.AnalyzerNameTypeScript, output: tsStubAnalyzerOutput()}
}

// genericOKAnalyzer mirrors genericOkPlugin.
func genericOKAnalyzer(findings ...analysis.PerFileFinding) analysis.AnalyzerPlugin {
	return fixedOutputPlugin{
		name: schemas.AnalyzerNameGeneric,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{
				Analyzer: schemas.AnalyzerNameGeneric,
				Status:   schemas.AnalyzerStatusOK,
			},
			Files: findings,
		},
	}
}

// failingAnalyzer mirrors throwingPlugin.
func failingAnalyzer(name schemas.AnalyzerName, err error) analysis.AnalyzerPlugin {
	return fixedOutputPlugin{name: name, err: err}
}

// tsOKAnalyzer is the symbol-capable-but-silent typescript double used by the
// skipped-reason honesty test.
func tsOKAnalyzer(note string) analysis.AnalyzerPlugin {
	return fixedOutputPlugin{
		name: schemas.AnalyzerNameTypeScript,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{
				Analyzer: schemas.AnalyzerNameTypeScript,
				Status:   schemas.AnalyzerStatusOK,
				Note:     testStrPtr(note),
			},
			Files: nil,
		},
	}
}

/* ------------------------------------------------------- assertions/golden */

const deepSkippedV1Reason = "no type-aware analyzer coverage in V1 (typescript stub; tree-sitter lands Phase 11)"

const deepSkippedNoSymbolChangesReason = "symbol-capable analyzer ran but reported no symbol-level changes for this diff"

const capBreachHintNote = "deep_analysis skipped (cap breach) — route REVIEW"

// analysisBuilderPurityGolden is the checked-in golden
// (test/goldens/analysis-builder-purity.golden.json) carried as a Go literal;
// the comparison canonicalizes BOTH sides (TS expectGolden compares
// canonicalJson(actual) with canonicalJson(JSON.parse(file))).
const analysisBuilderPurityGolden = `{
  "analyzer_coverage": [
    {
      "analyzer": "typescript",
      "note": "stub: no AST/symbol extraction in V1 — tree-sitter symbol/type-level analysis lands in Phase 11 (F11-1); per-file symbol diffs are a declared V1 coverage gap",
      "status": "stub"
    },
    { "analyzer": "generic", "status": "ok" }
  ],
  "base_sha": "4ca5c90ef81767793d777bbf1a395cc2a961eca9",
  "classification_hints": [],
  "deep_analysis": {
    "reason": "no type-aware analyzer coverage in V1 (typescript stub; tree-sitter lands Phase 11)",
    "status": "skipped"
  },
  "dependencies_changed": true,
  "files": [
    { "config_diff": true, "path": ".env", "status": "modified" },
    { "path": "docs/added.md", "status": "added" },
    { "path": "package.json", "status": "modified" },
    { "path": "src/payments/refund.test.ts", "status": "modified", "test_only": true },
    { "path": "src/payments/refund.ts", "status": "modified" }
  ],
  "head_sha": "e3d4611d09e3645a3b73df432796219eca85b9b6",
  "repo": "fixtures/payments",
  "run_id": "fixtures/payments#4ca5c90ef81767793d777bbf1a395cc2a961eca9..e3d4611d09e3645a3b73df432796219eca85b9b6",
  "schema": "docsync.change-analysis/v1"
}`

// requireAnalysisOutcome mirrors assertAnalysis (TS).
func requireAnalysisOutcome(t *testing.T, outcome analysis.AnalysisOutcome) *schemas.ChangeAnalysis {
	t.Helper()
	require.Equalf(t, analysis.AnalysisOutcomeKindAnalysis, outcome.Kind,
		"expected analysis outcome, got: %+v", outcome)
	require.NotNil(t, outcome.Record)
	return outcome.Record
}

// canonicalRecordJSON is the wire-bytes view of a record through the same
// canonical serialization as committed state (TS canonicalJson).
func canonicalRecordJSON(t *testing.T, record *schemas.ChangeAnalysis) string {
	t.Helper()
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	var generic any
	require.NoError(t, json.Unmarshal(raw, &generic))
	canonical, err := docutil.CanonicalJSON(generic)
	require.NoError(t, err)
	return canonical
}

// findChangedFile locates one row by path (TS record.files.find).
func findChangedFile(record *schemas.ChangeAnalysis, path string) *schemas.ChangedFile {
	for i := range record.Files {
		if record.Files[i].Path == path {
			return &record.Files[i]
		}
	}
	return nil
}

/* ------------------------------------------------------------- the fixture */

// commitPaymentsFixture is the shared fixture: source + test + env-config +
// manifest changes, one doc added.
func commitPaymentsFixture(t *testing.T, repo *builderRepo) (base, head string) {
	t.Helper()
	repo.write(t, "src/payments/refund.ts",
		"export function refundInvoice(amount: number): number {\n  return amount;\n}\n")
	repo.write(t, "src/payments/refund.test.ts",
		"import { refundInvoice } from \"./refund.js\";\n\nit(\"refunds\", () => {\n  expect(refundInvoice(1)).toBe(1);\n});\n")
	repo.write(t, ".env", "API_URL=https://api.example\nDEBUG=false\n")
	repo.write(t, "package.json", "{\n  \"name\": \"payments\",\n  \"version\": \"1.0.0\"\n}\n")
	base = repo.commitAll(t, "base")

	repo.write(t, "src/payments/refund.ts",
		"export function refundInvoice(amount: number, fee: number): number {\n  return amount + fee;\n}\n")
	repo.write(t, "src/payments/refund.test.ts",
		"import { refundInvoice } from \"./refund.js\";\n\nit(\"refunds with fee\", () => {\n  expect(refundInvoice(1, 0)).toBe(1);\n});\n")
	repo.write(t, ".env", "API_URL=https://api2.example\nDEBUG=false\n")
	repo.write(t, "package.json",
		"{\n  \"name\": \"payments\",\n  \"version\": \"1.1.0\",\n  \"dependencies\": {\n    \"left-pad\": \"^1.3.0\"\n  }\n}\n")
	repo.write(t, "docs/added.md", "payments overview\n")
	head = repo.commitAll(t, "head")
	return base, head
}

/* ------------------------------------------------------------------ tests */

func TestV_P1_1_PuritySameInputsBuildByteIdenticalChangeAnalysis(t *testing.T) {
	repo := newBuilderRepo(t)
	base, head := commitPaymentsFixture(t, repo)

	plugins := []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()}
	build := func() analysis.AnalysisOutcome {
		outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
			Repo:     "fixtures/payments",
			RepoRoot: repo.root,
			Git:      repo.gitClient(t),
			Base:     base,
			Head:     head,
			Plugins:  plugins,
		})
		require.NoError(t, err)
		return outcome
	}
	a := requireAnalysisOutcome(t, build())
	b := requireAnalysisOutcome(t, build())

	// Byte-identity via the SAME canonical serialization as committed state.
	assert.Equal(t, canonicalRecordJSON(t, a), canonicalRecordJSON(t, b))

	// Cross-process stability: the checked-in golden must match exactly.
	var expectedGolden any
	require.NoError(t, json.Unmarshal([]byte(analysisBuilderPurityGolden), &expectedGolden))
	expectedCanonical, err := docutil.CanonicalJSON(expectedGolden)
	require.NoError(t, err)
	assert.Equal(t, expectedCanonical, canonicalRecordJSON(t, a))

	// Structural sanity on the merged rows (diff order is git's, stable).
	assert.Equal(t, schemas.ChangeAnalysisSchemaID, a.Schema)
	assert.Equal(t, fmt.Sprintf("fixtures/payments#%s..%s", base, head), a.RunID)
	assert.Equal(t, base, a.BaseSHA)
	assert.Equal(t, head, a.HeadSHA)
	assert.True(t, a.DependenciesChanged)

	envRow := findChangedFile(a, ".env")
	require.NotNil(t, envRow)
	require.NotNil(t, envRow.ConfigDiff)
	assert.True(t, *envRow.ConfigDiff)
	assert.Nil(t, envRow.SymbolsChanged)

	testRow := findChangedFile(a, "src/payments/refund.test.ts")
	require.NotNil(t, testRow)
	require.NotNil(t, testRow.TestOnly)
	assert.True(t, *testRow.TestOnly)
	assert.Nil(t, testRow.FormattingOnly)

	docRow := findChangedFile(a, "docs/added.md")
	require.NotNil(t, docRow)
	assert.Equal(t, schemas.FileStatusAdded, docRow.Status)
	assert.Nil(t, docRow.Generated)
	assert.Nil(t, docRow.SymbolsChanged)

	// No plugin findings in this lineup → nothing symbol-assessed.
	for i := range a.Files {
		assert.Nil(t, a.Files[i].SymbolsChanged)
	}
	assert.Equal(t, schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: testStrPtr(deepSkippedV1Reason),
	}, a.DeepAnalysis)
	assert.Empty(t, a.ClassificationHints) // mixed change → decision stage decides
	assert.Equal(t, []schemas.AnalyzerCoverage{
		{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusStub,
			Note:     testStrPtr(analysis.TypeScriptStubNote),
		},
		{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK},
	}, a.AnalyzerCoverage)
}

func TestV_P1_10_InjectedGitFailureYieldsInconclusiveNeverEmptyAnalysis(t *testing.T) {
	repo := newBuilderRepo(t)
	base, head := commitPaymentsFixture(t, repo)

	client, err := gitx.NewGitClient(gitx.GitClientOptions{
		RepoRoot: repo.root,
		Executor: failingGitExecutor{},
	})
	require.NoError(t, err)

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/payments",
		Git:     client,
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
	})
	require.NoError(t, err)
	require.Equal(t, analysis.AnalysisOutcomeKindInconclusive, outcome.Kind)
	assert.Regexp(t, "^git failure: ", outcome.Reason)
	assert.Contains(t, outcome.Reason, "simulated git outage")
	// Git failed BEFORE analysis: no coverage entries exist yet.
	assert.Empty(t, outcome.Coverage)
	assert.Nil(t, outcome.Record)
}

// failingGitExecutor is the injected git outage (TS failing GitExecutor).
type failingGitExecutor struct{}

func (failingGitExecutor) Run(_ context.Context, _ []string, _ *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	exitCode := 128
	return gitx.GitRunResult{
		ExitCode: &exitCode,
		Signal:   "",
		TimedOut: false,
		Stdout:   nil,
		Stderr:   []byte("fatal: simulated git outage\n"),
	}, nil
}

// catFileFailingExecutor fails every cat-file invocation (mid-run: AFTER
// analysis ran and coverage was gathered) and delegates everything else to the
// real system executor.
type catFileFailingExecutor struct {
	inner *gitx.SystemGitExecutor
}

func (e catFileFailingExecutor) Run(ctx context.Context, args []string, opts *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	for _, arg := range args {
		if arg == "cat-file" {
			exitCode := 128
			return gitx.GitRunResult{
				ExitCode: &exitCode,
				Signal:   "",
				TimedOut: false,
				Stdout:   nil,
				Stderr:   []byte("fatal: simulated cat-file outage\n"),
			}, nil
		}
	}
	return e.inner.Run(ctx, args, opts)
}

func TestD14_AllAnalyzersUnavailableWithChangedFilesIsInconclusive(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/a.ts", "x\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "y\n")
	head := repo.commitAll(t, "head")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/one-file",
		Git:  repo.gitClient(t),
		Base: base,
		Head: head,
		Plugins: []analysis.AnalyzerPlugin{
			failingAnalyzer(schemas.AnalyzerNameTypeScript, errors.New("ts down")),
			failingAnalyzer(schemas.AnalyzerNameGeneric, errors.New("generic down")),
		},
	})
	require.NoError(t, err)
	require.Equal(t, analysis.AnalysisOutcomeKindInconclusive, outcome.Kind)
	assert.Contains(t, outcome.Reason, "all analyzers unavailable with 1 changed file(s)")
	assert.Contains(t, outcome.Reason, "inconclusive, never negative")
	assert.Equal(t, []schemas.AnalyzerCoverage{
		{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusUnavailable,
			Note: testStrPtr(fmt.Sprintf(
				"analyzer '%s' failed: %s — analysis continued without it (declared coverage gap, D14)",
				"typescript", "ts down")),
		},
		{
			Analyzer: schemas.AnalyzerNameGeneric,
			Status:   schemas.AnalyzerStatusUnavailable,
			Note: testStrPtr(fmt.Sprintf(
				"analyzer '%s' failed: %s — analysis continued without it (declared coverage gap, D14)",
				"generic", "generic down")),
		},
	}, outcome.Coverage)
}

func TestEmptyDiffYieldsValidEmptyAnalysisNotInconclusive(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "docs/only.md", "stable\n")
	sha := repo.commitAll(t, "only commit")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/empty",
		Git:  repo.gitClient(t),
		Base: sha,
		Head: sha, // identical revs → genuinely empty diff
		Plugins: []analysis.AnalyzerPlugin{
			tsStubAnalyzer(),
			genericOKAnalyzer(),
		},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	assert.Empty(t, record.Files)
	// Wire-format fidelity: an empty record serializes `"files": []`, never null.
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"files":[]`)
	assert.False(t, record.DependenciesChanged)
	assert.Empty(t, record.ClassificationHints)
	assert.Equal(t, schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: testStrPtr(deepSkippedV1Reason),
	}, record.DeepAnalysis)
	assert.Len(t, record.AnalyzerCoverage, 2)
}

func TestSymbolsChangedFindingFlipsDeepAnalysisToPerformed(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/a.ts", "x\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "y\n")
	head := repo.commitAll(t, "head")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/deep",
		Git:  repo.gitClient(t),
		Base: base,
		Head: head,
		Plugins: []analysis.AnalyzerPlugin{
			tsStubAnalyzer(),
			genericOKAnalyzer(analysis.PerFileFinding{
				Path:           "src/a.ts",
				SymbolsChanged: true,
			}),
		},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	assert.Equal(t, schemas.DeepAnalysis{Status: schemas.DeepAnalysisPerformed}, record.DeepAnalysis)
	row := findChangedFile(record, "src/a.ts")
	require.NotNil(t, row)
	require.NotNil(t, row.SymbolsChanged)
	assert.True(t, *row.SymbolsChanged)
}

func TestGeneratedContentMarkersRequireFetchContentForGeneratedHints(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/client.ts", "plain hand-written code\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/client.ts", "// DO NOT EDIT — generated by codegen\nexport const x = 1;\n")
	head := repo.commitAll(t, "head")

	withoutFetch, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/gen",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
	})
	require.NoError(t, err)
	noFetch := requireAnalysisOutcome(t, withoutFetch)
	require.Len(t, noFetch.Files, 1)
	assert.Nil(t, noFetch.Files[0].Generated) // path check alone found nothing
	assert.Empty(t, noFetch.ClassificationHints)

	withFetch, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:                          "fixtures/gen",
		Git:                           repo.gitClient(t),
		Base:                          base,
		Head:                          head,
		Plugins:                       []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
		FetchContentForGeneratedHints: true,
	})
	require.NoError(t, err)
	fetched := requireAnalysisOutcome(t, withFetch)
	require.Len(t, fetched.Files, 1)
	require.NotNil(t, fetched.Files[0].Generated)
	assert.True(t, *fetched.Files[0].Generated)
	require.Len(t, fetched.ClassificationHints, 1)
	assert.Equal(t, schemas.ClassificationHintGenerated, fetched.ClassificationHints[0].Kind)
	assert.Contains(t, fetched.ClassificationHints[0].Note, "inspect_generated_output")
}

func TestV_P11_1_SymbolsDetailRidesTheChangeAnalysisRowUnchanged(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/a.ts", "x\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "y\n")
	head := repo.commitAll(t, "head")

	detail := &schemas.SymbolsDetail{
		Added:   []string{},
		Removed: []string{},
		Changed: []schemas.SymbolChangeDetail{{
			Name:        "idempotencyKey",
			ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeType},
		}},
	}
	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/detail",
		Git:  repo.gitClient(t),
		Base: base,
		Head: head,
		Plugins: []analysis.AnalyzerPlugin{
			tsStubAnalyzer(),
			genericOKAnalyzer(analysis.PerFileFinding{
				Path:           "src/a.ts",
				SymbolsChanged: true,
				SymbolsDetail:  detail,
			}),
		},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	row := findChangedFile(record, "src/a.ts")
	require.NotNil(t, row)
	require.NotNil(t, row.SymbolsChanged)
	assert.True(t, *row.SymbolsChanged)
	assert.Equal(t, detail, row.SymbolsDetail)
}

func TestDeepAnalysisSkippedReasonHonesty_OkTypeScriptCoverageIsNotTheStubGap(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/a.ts", "x\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "y\n")
	head := repo.commitAll(t, "head")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/reason",
		Git:  repo.gitClient(t),
		Base: base,
		Head: head,
		Plugins: []analysis.AnalyzerPlugin{
			tsOKAnalyzer("analyzed: symbol-capable; no symbol-level changes in this diff"),
			genericOKAnalyzer(),
		},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	assert.Equal(t, schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: testStrPtr(deepSkippedNoSymbolChangesReason),
	}, record.DeepAnalysis)

	// The stub lineup keeps the historical stub-gap reason (V1 declaration).
	stubOutcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/reason",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
	})
	require.NoError(t, err)
	stubRecord := requireAnalysisOutcome(t, stubOutcome)
	assert.Equal(t, schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: testStrPtr(deepSkippedV1Reason),
	}, stubRecord.DeepAnalysis)
}

/* --------------------------------------------- deep-analysis caps (P1-T08) */

// capsCapture mirrors CapturedInput from analysis-caps.test.ts.
type capsCapture struct {
	deepAllowed bool
	notes       []string
}

// recordingGenericAnalyzer mirrors recordingGenericPlugin.
type recordingGenericAnalyzer struct {
	captured *capsCapture
}

func (p recordingGenericAnalyzer) Name() schemas.AnalyzerName {
	return schemas.AnalyzerNameGeneric
}

func (p recordingGenericAnalyzer) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	p.captured.deepAllowed = input.DeepAllowed
	if input.Notes == nil {
		p.captured.notes = nil
	} else {
		p.captured.notes = append([]string(nil), input.Notes...)
	}
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameGeneric,
			Status:   schemas.AnalyzerStatusOK,
		},
		Files: nil,
	}, nil
}

// commitTwoFileFixture is the caps fixture. Diff accounting (text files →
// additions+deletions): src/a.ts: 1 del + 1 add = 2 bytes; src/b.ts: 1 del +
// 2 add = 3 bytes → 5.
func commitTwoFileFixture(t *testing.T, repo *builderRepo) (base, head string) {
	t.Helper()
	repo.write(t, "src/a.ts", "one\n")
	repo.write(t, "src/b.ts", "x\n")
	base = repo.commitAll(t, "base")

	repo.write(t, "src/a.ts", "ONE\n")
	repo.write(t, "src/b.ts", "y\nz\n")
	head = repo.commitAll(t, "head")
	return base, head
}

func TestDeepAnalysisCaps_V_P1_4_CapBreachDowngradesWithReviewRoutingHint(t *testing.T) {
	repo := newBuilderRepo(t)
	base, head := commitTwoFileFixture(t, repo)
	captured := &capsCapture{}

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/caps",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), recordingGenericAnalyzer{captured: captured}},
		Caps:    &analysis.AnalysisCaps{MaxFiles: 1, MaxDiffSize: 1000}, // breached by file COUNT (2 > 1)
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome) // a downgrade, NOT an inconclusive run
	assert.Equal(t, schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: testStrPtr("cap breach (2 files / 5 bytes > caps)"),
	}, record.DeepAnalysis)
	assert.Equal(t, []schemas.ClassificationHint{
		{Kind: schemas.ClassificationHintOther, Note: capBreachHintNote},
	}, record.ClassificationHints)
	assert.False(t, captured.deepAllowed)
	assert.Equal(t, []string{"cap breach (2 files / 5 bytes > caps)"}, captured.notes)
	// The downgrade is declared in the run record, never silent: rows stay bare.
	for i := range record.Files {
		assert.Nil(t, record.Files[i].SymbolsChanged)
	}
}

func TestDeepAnalysisCaps_ByteBudgetBreachDowngrades(t *testing.T) {
	repo := newBuilderRepo(t)
	base, head := commitTwoFileFixture(t, repo)
	captured := &capsCapture{}

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/caps",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), recordingGenericAnalyzer{captured: captured}},
		Caps:    &analysis.AnalysisCaps{MaxFiles: 100, MaxDiffSize: 4}, // 5 accounted bytes > 4
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	assert.Equal(t, schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: testStrPtr("cap breach (2 files / 5 bytes > caps)"),
	}, record.DeepAnalysis)
	assert.False(t, captured.deepAllowed)
}

func TestDeepAnalysisCaps_NoBreachKeepsDeepAllowedAndDefaultStubReason(t *testing.T) {
	repo := newBuilderRepo(t)
	base, head := commitTwoFileFixture(t, repo)
	captured := &capsCapture{}

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/caps",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), recordingGenericAnalyzer{captured: captured}},
		Caps:    &analysis.AnalysisCaps{MaxFiles: 10, MaxDiffSize: 100},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	assert.Equal(t, schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: testStrPtr(deepSkippedV1Reason),
	}, record.DeepAnalysis)
	assert.Empty(t, record.ClassificationHints)
	assert.True(t, captured.deepAllowed)
	assert.Nil(t, captured.notes)
}

func TestDeepAnalysisCaps_ExactBoundaryValuesDoNotBreach(t *testing.T) {
	repo := newBuilderRepo(t)
	base, head := commitTwoFileFixture(t, repo)
	captured := &capsCapture{}

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/caps",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), recordingGenericAnalyzer{captured: captured}},
		Caps:    &analysis.AnalysisCaps{MaxFiles: 2, MaxDiffSize: 5}, // exactly the fixture's counts
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	assert.Empty(t, record.ClassificationHints)
	assert.Equal(t, schemas.DeepAnalysisSkipped, record.DeepAnalysis.Status)
	require.NotNil(t, record.DeepAnalysis.Reason)
	assert.NotContains(t, *record.DeepAnalysis.Reason, "cap breach")
	assert.True(t, captured.deepAllowed)
}

func TestV_P11_3_UnsupportedLanguageFixtureDeclaresTheCoverageGapAndRoutesReview(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/x.py", "one\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/x.py", "ONE\n")
	head := repo.commitAll(t, "head")
	captured := &capsCapture{}

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/unsupported-language",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), recordingGenericAnalyzer{captured: captured}},
		Caps:    &analysis.AnalysisCaps{MaxFiles: 10, MaxDiffSize: 1}, // breached by byte budget (2 > 1)
	})
	// A VALID record — declared downgrade, never inconclusive (V-P11-3/D14).
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	// The declared coverage gap lives IN the run record (never silent).
	assert.Equal(t, schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: testStrPtr("cap breach (1 files / 2 bytes > caps)"),
	}, record.DeepAnalysis)
	// REVIEW routing: the record carries the route-REVIEW hint.
	assert.Equal(t, []schemas.ClassificationHint{
		{Kind: schemas.ClassificationHintOther, Note: capBreachHintNote},
	}, record.ClassificationHints)
	// Declared-gap notes: the TS stub gap + the generic path-level analyzer ok.
	assert.Equal(t, []schemas.AnalyzerCoverage{
		{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusStub,
			Note:     testStrPtr(analysis.TypeScriptStubNote),
		},
		{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK},
	}, record.AnalyzerCoverage)
	// Path-level only: the unsupported-language row stays bare (no symbol
	// evidence was fabricated for a language no analyzer covers).
	paths := make([]string, 0, len(record.Files))
	for i := range record.Files {
		paths = append(paths, record.Files[i].Path)
		assert.Nil(t, record.Files[i].SymbolsChanged)
	}
	assert.Equal(t, []string{"src/x.py"}, paths)
	assert.False(t, captured.deepAllowed)
	assert.Equal(t, []string{"cap breach (1 files / 2 bytes > caps)"}, captured.notes)
}

func TestMidRunGitFailureAfterAnalysisIsInconclusiveWithCoverageRetained(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, ".env", "A=1\n")
	base := repo.commitAll(t, "base")
	repo.write(t, ".env", "A=2\n")
	head := repo.commitAll(t, "head")

	// A modified config-family file makes mergeRows fetch base+head content
	// (cat-file): failing there is a mid-run git failure, not an analyzer one.
	client, err := gitx.NewGitClient(gitx.GitClientOptions{
		RepoRoot: repo.root,
		Executor: catFileFailingExecutor{inner: gitx.NewSystemGitExecutor(nil)},
	})
	require.NoError(t, err)

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/midrun",
		Git:     client,
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
	})
	require.NoError(t, err)
	require.Equal(t, analysis.AnalysisOutcomeKindInconclusive, outcome.Kind)
	assert.Regexp(t, "^git failure: ", outcome.Reason)
	assert.Contains(t, outcome.Reason, "simulated cat-file outage")
	// Git failed AFTER analysis: the coverage gathered before the failure
	// rides along — the run record can still show what analyzed and what did not.
	assert.Equal(t, []schemas.AnalyzerCoverage{
		{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusStub,
			Note:     testStrPtr(analysis.TypeScriptStubNote),
		},
		{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK},
	}, outcome.Coverage)
}

func TestRenamedFileRowCarriesOldPathAndRenamedStatus(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/old-name.ts", "export const value = 1;\n")
	base := repo.commitAll(t, "base")
	// Pure rename (identical content → 100% similarity): git reports R100 and
	// the builder must carry the old path onto the row.
	require.NoError(t, os.Rename(
		filepath.Join(repo.root, "src/old-name.ts"),
		filepath.Join(repo.root, "src/new-name.ts")))
	head := repo.commitAll(t, "head")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/rename",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	require.Len(t, record.Files, 1)
	row := record.Files[0]
	assert.Equal(t, "src/new-name.ts", row.Path)
	assert.Equal(t, schemas.FileStatusRenamed, row.Status)
	require.NotNil(t, row.OldPath)
	assert.Equal(t, "src/old-name.ts", *row.OldPath)
	// A pure rename is not a config/API/symbol assessment.
	assert.Nil(t, row.ConfigDiff)
	assert.Nil(t, row.APIDiff)
	assert.Nil(t, row.SymbolsChanged)
}

func TestModifiedConfigFileWithUnchangedKeysIsAssessedConfigDiffFalse(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "config/settings.json", "{\n  \"a\": 1,\n  \"b\": 2\n}\n")
	base := repo.commitAll(t, "base")
	// Same keys/values, reordered text: a real file modification whose key set
	// did NOT change — the differ assessed it and found no change.
	repo.write(t, "config/settings.json", "{\n  \"b\": 2,\n  \"a\": 1\n}\n")
	head := repo.commitAll(t, "head")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/config-unchanged",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	require.Len(t, record.Files, 1)
	row := record.Files[0]
	assert.Equal(t, "config/settings.json", row.Path)
	require.NotNil(t, row.ConfigDiff)
	assert.False(t, *row.ConfigDiff) // assessed and unchanged — never absent
}

func TestSchemaDriftFailsLoudNeverCoercedToInconclusive(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/a.ts", "x\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "y\n")
	head := repo.commitAll(t, "head")

	// A coverage entry no schema version knows: the assembled record cannot
	// validate. That is schema DRIFT — a loud error, never an inconclusive
	// verdict and never an empty analysis.
	drifting := fixedOutputPlugin{
		name: schemas.AnalyzerNameGeneric,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{
				Analyzer: schemas.AnalyzerName("mystery"),
				Status:   schemas.AnalyzerStatusOK,
			},
			Files: nil,
		},
	}
	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/drift",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), drifting},
	})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeSchema, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "CHANGE_ANALYSIS_SCHEMA")
	// Never coerced: no outcome kind, no record, no coverage passthrough.
	assert.Empty(t, outcome.Kind)
	assert.Nil(t, outcome.Record)
	assert.Empty(t, outcome.Coverage)
}

// nthGitFailingExecutor fails the Nth git invocation and delegates everything
// else to the real system executor. It pins the P1-T18 "ANY stage" claim: a
// git failure at ANY point of the deterministic pipeline is conclusive as
// inconclusive — never an empty/zero-docs analysis.
type nthGitFailingExecutor struct {
	inner   *gitx.SystemGitExecutor
	failNth int // 1-based invocation index to fail; 0 = never
	seen    int
}

func (e *nthGitFailingExecutor) Run(ctx context.Context, args []string, opts *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	e.seen++
	if e.failNth > 0 && e.seen == e.failNth {
		exitCode := 128
		return gitx.GitRunResult{
			ExitCode: &exitCode,
			Signal:   "",
			TimedOut: false,
			Stdout:   nil,
			Stderr:   []byte("fatal: simulated git outage\n"),
		}, nil
	}
	return e.inner.Run(ctx, args, opts)
}

func TestEveryGitStageFailureIsInconclusiveNeverEmptyAnalysis(t *testing.T) {
	// Symbolic refs (not resolved SHAs) so the two rev-parse resolutions stay
	// in the invocation sequence. The pinned pipeline for a modified .env with
	// generated-hint fetching ON invokes git in this order:
	//   1 rev-parse base, 2 rev-parse head, 3 diff --raw, 4 diff (patch),
	//   5 diff --numstat, 6-8 head content fetch, 9-11 base content,
	//   12-14 head content (config diff new side).
	testCases := []struct {
		name        string
		failNth     int
		analysisRan bool // whether the analyzers ran before the failure
	}{
		{"head resolution", 2, false},
		{"diff listing raw pass", 3, false},
		{"diff stat numstat", 5, false},
		{"generated-hint content fetch", 8, true},
		{"config diff new-side content", 14, true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newBuilderRepo(t)
			repo.write(t, ".env", "A=1\n")
			repo.commitAll(t, "base")
			repo.write(t, ".env", "A=2\n")
			repo.commitAll(t, "head")

			client, err := gitx.NewGitClient(gitx.GitClientOptions{
				RepoRoot: repo.root,
				Executor: &nthGitFailingExecutor{
					inner:   gitx.NewSystemGitExecutor(nil),
					failNth: testCase.failNth,
				},
			})
			require.NoError(t, err)

			outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
				Repo:                          "fixtures/stages",
				Git:                           client,
				Base:                          "main~1",
				Head:                          "HEAD",
				Plugins:                       []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
				FetchContentForGeneratedHints: true,
			})
			require.NoError(t, err)
			require.Equal(t, analysis.AnalysisOutcomeKindInconclusive, outcome.Kind)
			assert.Regexp(t, "^git failure: ", outcome.Reason)
			assert.Contains(t, outcome.Reason, "simulated git outage")
			if testCase.analysisRan {
				// After analysis: the coverage gathered rides along.
				assert.Len(t, outcome.Coverage, 2)
			} else {
				// Before analysis: no coverage entries exist yet.
				assert.Empty(t, outcome.Coverage)
			}
			assert.Nil(t, outcome.Record)
		})
	}
}

func TestBadPluginLineupFailsLoudThroughTheBuilder(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "docs/only.md", "stable\n")
	sha := repo.commitAll(t, "only commit")

	// A duplicate plugin name is a lineup bug: compose rejects it BEFORE any
	// git call, and the builder must NOT coerce it into an environmental
	// inconclusive verdict.
	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo: "fixtures/bad-lineup",
		Git:  repo.gitClient(t),
		Base: sha,
		Head: sha,
		Plugins: []analysis.AnalyzerPlugin{
			fixedOutputPlugin{name: schemas.AnalyzerNameGeneric, output: genericAnalyzerOutput()},
			fixedOutputPlugin{name: schemas.AnalyzerNameGeneric, output: genericAnalyzerOutput()},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate analyzer plugin name")
	assert.Empty(t, outcome.Kind)
	assert.Nil(t, outcome.Record)
	assert.Empty(t, outcome.Coverage)
}

func TestFormattingOnlyChangeFlipsTheRowFlagAndEmitsTheSkipHint(t *testing.T) {
	repo := newBuilderRepo(t)
	repo.write(t, "src/a.ts", "const x = 1;\n")
	base := repo.commitAll(t, "base")
	repo.write(t, "src/a.ts", "const x = 1; \n") // trailing space: whitespace-equivalent
	head := repo.commitAll(t, "head")

	outcome, err := analysis.BuildChangeAnalysis(context.Background(), analysis.BuildChangeAnalysisInput{
		Repo:    "fixtures/formatting",
		Git:     repo.gitClient(t),
		Base:    base,
		Head:    head,
		Plugins: []analysis.AnalyzerPlugin{tsStubAnalyzer(), genericOKAnalyzer()},
	})
	require.NoError(t, err)
	record := requireAnalysisOutcome(t, outcome)
	require.Len(t, record.Files, 1)
	row := record.Files[0]
	require.NotNil(t, row.FormattingOnly)
	assert.True(t, *row.FormattingOnly)
	assert.Nil(t, row.TestOnly)
	// ALL files formatting-only → the change-level hint skips the decision stage.
	require.Len(t, record.ClassificationHints, 1)
	assert.Equal(t, schemas.ClassificationHintFormattingOnly, record.ClassificationHints[0].Kind)
	assert.Contains(t, record.ClassificationHints[0].Note, "skip Jev entirely")
}
