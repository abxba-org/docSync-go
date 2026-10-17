package cli

// Coverage-completion suite, part 2 — the production flow runtimes
// (flow_runtime.go: runPrFlow / runAuditFlow and their helpers) plus the fix
// flow lanes the exported-surface suites do not reach. INTERNAL test (package
// cli); all runs are deterministic: a local git fixture (pinned identity,
// no network) and the REAL internal/audit engine (no model stages).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------ git fixture */

type covGitRepo struct {
	t    *testing.T
	root string
}

func covNewGitRepo(t *testing.T) *covGitRepo {
	t.Helper()
	repo := &covGitRepo{t: t, root: t.TempDir()}
	repo.git("init", "-b", "main", "-q")
	repo.git("config", "core.autocrlf", "false")
	repo.git("config", "core.eol", "lf")
	repo.git("config", "user.name", "docsync-test")
	repo.git("config", "user.email", "docsync-test@invalid")
	return repo
}

func (r *covGitRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *covGitRepo) write(relPath, content string) {
	r.t.Helper()
	target := filepath.Join(r.root, relPath)
	require.NoError(r.t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(r.t, os.WriteFile(target, []byte(content), 0o644))
}

func (r *covGitRepo) commitAll(message string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", message)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

const covManifest = `version: 1
documents:
  - path: docs/api.md
    mode: suggest
    sources:
      - "src/payments/**"
    triggers:
      - public_behavior
`

const covModels = `schema: docsync.models/v1
version: 1
roles:
  decision-engine:
    model: openai/gpt-4o-mini
  judge:
    model: openai/gpt-4o-mini
  writer:
    model: openai/gpt-4o-mini
`

// covSeedRepo writes a complete valid .docsync/ configuration plus one mapped
// doc and one source file, committed (clean worktree).
func covSeedRepo(t *testing.T) *covGitRepo {
	t.Helper()
	repo := covNewGitRepo(t)
	repo.write(".docsync/manifest.yml", covManifest)
	repo.write(".docsync/models.yml", covModels)
	repo.write(".docsync/instructions.md", "# instructions\n- keep diffs minimal\n")
	repo.write("docs/api.md", "# API reference\n\nThe refund call captures the payment.\n")
	repo.write("src/payments/refund.ts", "export function refund(): void {}\n")
	repo.commitAll("chore: fixture base tree")
	return repo
}

/* ----------------------------------------------------- flow_runtime helpers */

func TestCOV_CLI_101_RawGitTrimmedReadsAndFailsQuietly(t *testing.T) {
	repo := covSeedRepo(t)
	head := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	assert.Equal(t, head, rawGitTrimmed(repo.root, "rev-parse", "HEAD"))
	assert.Equal(t, "", rawGitTrimmed(repo.root, "definitely-not-a-git-command"),
		"a failing git read is empty (the caller falls back), never a panic")
}

func TestCOV_CLI_102_PrFlowRepoNameDerivesTheSlugOrFallsBack(t *testing.T) {
	repo := covSeedRepo(t)
	assert.Equal(t, "local/repo", prFlowRepoName(repo.root), "no origin remote → the local fallback")

	repoWithRemote := covSeedRepo(t)
	repoWithRemote.git("remote", "add", "origin", "https://github.com/acme/widgets.git")
	assert.Equal(t, "acme/widgets", prFlowRepoName(repoWithRemote.root))

	// SUSPECTED MINOR BUG (report-only): an scp-style remote
	// (git@github.com:org/repo.git) keeps its leading ':' — the slug splitter
	// only trims a leading '/', so the derived run-key repo id is
	// ':dupe/six-one'. The observable behaviour is pinned here.
	repoWithTrailing := covSeedRepo(t)
	repoWithTrailing.git("remote", "add", "origin", "git@github.com:dupe/six-one.git")
	slug := prFlowRepoName(repoWithTrailing.root)
	assert.Equal(t, ":dupe/six-one", slug)

	// A non-GitHub remote is used verbatim (still a nonempty slug).
	repoOther := covSeedRepo(t)
	repoOther.git("remote", "add", "origin", "https://gitlab.com/acme/widgets.git")
	assert.Equal(t, "https://gitlab.com/acme/widgets.git", prFlowRepoName(repoOther.root),
		"a non-GitHub remote is used verbatim (the .git suffix is only trimmed for github.com)")
}

func TestCOV_CLI_103_LoadManifestForFlowLanes(t *testing.T) {
	repo := covSeedRepo(t)
	manifest, err := loadManifestForFlow(repo.root)
	require.NoError(t, err)
	require.NotNil(t, manifest)
	assert.Equal(t, 1, manifest.Version)

	// An unreadable manifest is a config-class failure.
	_, err = loadManifestForFlow(t.TempDir())
	require.Error(t, err)
	assert.True(t, isConfigClass(err))
	assert.Contains(t, err.Error(), "manifest is not readable")

	// A directory where the manifest belongs is the same fail-closed lane.
	dirAsManifest := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dirAsManifest, ".docsync", "manifest.yml"), 0o755))
	_, err = loadManifestForFlow(dirAsManifest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest is not readable")

	// A parse failure surfaces through ParseManifest.
	broken := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(broken, ".docsync"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, ".docsync", "manifest.yml"), []byte("version: [1,\n"), 0o644))
	_, err = loadManifestForFlow(broken)
	require.Error(t, err)
}

func TestCOV_CLI_104_CreateDocPrFlowCompositionLanes(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	clock := func() string { return "2026-01-01T00:00:00.000Z" }

	// No GITHUB_TOKEN → the dry-run credential lane (INV-8). The deps are
	// composed (state store, incident sink, platform, repo slug, default
	// branch) and then the flow validation FAILS CLOSED with a config-class
	// defect. See the SUSPECTED BUG note in the file header: the CLI
	// composition omits gitpr.DocPrFlowDeps.Apply, which the flow requires, so
	// createDocPrFlow can never return a constructed flow today.
	t.Setenv("GITHUB_TOKEN", "")
	flow, err := createDocPrFlow(repo.root, git, clock)
	require.Error(t, err)
	assert.Nil(t, flow)
	// The defect is fail-closed and typed (a gitpr config-class error), even
	// though the CLI's isConfigClass predicate does not recognize it — the flow
	// still refuses to construct, which is the safety-relevant property.
	assert.Contains(t, err.Error(), "deps.apply must be a disk seam")

	// With a token the developer credential lane composes the same way.
	t.Setenv("GITHUB_TOKEN", "ghp-test-token")
	flow, err = createDocPrFlow(repo.root, git, clock)
	require.Error(t, err)
	assert.Nil(t, flow)
	assert.Contains(t, err.Error(), "deps.apply must be a disk seam")
}

func TestCOV_CLI_105_DefaultBranchOfFallsBackToMain(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	assert.Equal(t, "main", defaultBranchOf(git), "no origin/HEAD → the documented main fallback")

	repo.git("remote", "add", "origin", "https://github.com/acme/widgets.git")
	// Pin origin/HEAD without any fetch (no network): the remote-tracking ref
	// and the symbolic ref are written directly.
	repo.git("update-ref", "refs/remotes/origin/main", "main")
	repo.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git, err = gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	assert.Equal(t, "main", defaultBranchOf(git), "origin/HEAD dereferences to the branch name")
}

func TestCOV_CLI_106_RunOutcomeFromReportMirrorsTheFrozenOutcome(t *testing.T) {
	outcome := runOutcomeFromReport(schemas.CanonicalReport{
		RunID:  "run-1",
		Key:    schemas.RunKey{Repo: "acme/widgets", BaseSHA: "a", HeadSHA: "b"},
		Status: "completed",
	})
	assert.Equal(t, "docsync.run-outcome/v1", outcome.Schema)
	assert.Equal(t, "run-1", outcome.RunID)
	assert.Equal(t, "acme/widgets", outcome.Key.Repo)
	assert.Equal(t, schemas.RunStatus("completed"), outcome.Status)
	assert.Nil(t, outcome.StartedAt, "a report without GeneratedAt carries no started_at")
	assert.Empty(t, outcome.Documents)
	assert.Empty(t, outcome.Candidates)
	assert.Empty(t, outcome.Stages)

	generatedAt := "2026-01-01T00:00:00Z"
	withTime := runOutcomeFromReport(schemas.CanonicalReport{RunID: "run-2", GeneratedAt: &generatedAt})
	require.NotNil(t, withTime.StartedAt)
	assert.Equal(t, generatedAt, *withTime.StartedAt)
}

func TestCOV_CLI_107_FixRunErrorOfAndFixFlowKindName(t *testing.T) {
	assert.Nil(t, fixRunErrorOf(FixFlowResult{Kind: FixResultCompleted}))
	withDetail := fixRunErrorOf(FixFlowResult{Kind: FixResultRuntimeErr, Detail: "boom"})
	require.NotNil(t, withDetail)
	assert.Equal(t, "boom", *withDetail)
	defaulted := fixRunErrorOf(FixFlowResult{Kind: FixResultConfigError})
	require.NotNil(t, defaulted)
	assert.Equal(t, "fix flow ended with kind config-error", *defaulted)

	assert.Equal(t, "noop", fixFlowKindName(FixResultNoop))
	assert.Equal(t, "config-error", fixFlowKindName(FixResultConfigError))
	assert.Equal(t, "runtime-error", fixFlowKindName(FixResultRuntimeErr))
	assert.Equal(t, "unknown", fixFlowKindName(FixFlowResultKind("mystery")))
}

func TestCOV_CLI_108_FixSummaryLinesForAndPrResultVerdictOf(t *testing.T) {
	lines := fixSummaryLinesFor(FixFlowResult{
		Kind:    FixResultCompleted,
		Applied: []FixFlowApplied{{DocPath: "docs/api.md", PatchRef: "state/docs/docs__api.md#patch-aaaaaaaaaaaaaaaa"}},
	})
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "[ok] applied docs/api.md", "the pr surface renders ASCII glyphs")
	assert.Empty(t, fixSummaryLinesFor(FixFlowResult{Kind: FixResultCompleted}))

	verdict := prResultVerdictOf(gitpr.PrFlowResult{Kind: gitpr.PrFlowKindAborted, Reason: "up_to_date"})
	require.NotNil(t, verdict)
	assert.Equal(t, "aborted", verdict.Verdict)
	assert.Equal(t, "up_to_date", verdict.Reason)
}

func TestCOV_CLI_109_AuditTriggerEventOfMapsTheContextVocabulary(t *testing.T) {
	assert.Equal(t, audit.LoopBEventNightly, auditTriggerEventOf("nightly"))
	assert.Equal(t, audit.LoopBEventNightly, auditTriggerEventOf(" nightly-scheduled "))
	assert.Equal(t, audit.LoopBEventNightly, auditTriggerEventOf("schedule"))
	assert.Equal(t, audit.LoopBEventPostMerge, auditTriggerEventOf("post-merge"))
	assert.Equal(t, audit.LoopBEventPR, auditTriggerEventOf("pull_request"))
	assert.Equal(t, audit.LoopBEventPR, auditTriggerEventOf("pull-request"))
	assert.Equal(t, audit.LoopBTriggerEvent(""), auditTriggerEventOf("mystery"))
}

func TestCOV_CLI_110_AuditRunDetailAndNightlyWorkflowPresent(t *testing.T) {
	assert.Equal(t, "", auditRunDetail(nil))
	assert.Equal(t, "", auditRunDetail(&audit.AuditRunResult{}))
	detail := "partial run"
	assert.Equal(t, detail, auditRunDetail(&audit.AuditRunResult{Detail: &detail}))

	dir := t.TempDir()
	assert.False(t, nightlyWorkflowPresent(dir))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "workflows", "docsync-loop-b-nightly.yml"), []byte("name: nightly\n"), 0o644))
	assert.True(t, nightlyWorkflowPresent(dir))
}

func TestCOV_CLI_111_DocutilRuntimeErrorCarriesTheInternalCode(t *testing.T) {
	err := docutilRuntimeError("docsync pr: boom")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docsync pr: boom")
	var typed *docutil.Error
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, docutil.CodeInternal, typed.Code)
}

func TestCOV_CLI_112_SourceFilesOfDerivesTheEvidenceSet(t *testing.T) {
	manifest := &schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/api.md", Sources: []string{"src/payments/refund.ts"}},
			{Path: "docs/locked.md", Sources: []string{"src/payments/ledger.ts"}},
		},
	}
	// The report names one applied doc → its manifest sources are the evidence.
	files := sourceFilesOf(manifest, schemas.CanonicalReport{
		Rows: []schemas.CanonicalReportRow{{DocPath: "docs/api.md"}},
	})
	assert.Equal(t, []string{"src/payments/refund.ts"}, files)

	// No applied rows → every manifest source (never an empty evidence set).
	all := sourceFilesOf(manifest, schemas.CanonicalReport{})
	assert.ElementsMatch(t, []string{"src/payments/refund.ts", "src/payments/ledger.ts"}, all)

	// A report naming an absent doc falls back the same way.
	absent := sourceFilesOf(manifest, schemas.CanonicalReport{
		Rows: []schemas.CanonicalReportRow{{DocPath: "docs/missing.md"}},
	})
	assert.ElementsMatch(t, []string{"src/payments/refund.ts", "src/payments/ledger.ts"}, absent)
}

/* ------------------------------------------------------------------ pr flow */

func TestCOV_CLI_113_RunPrFlowRefusesNilInput(t *testing.T) {
	payload, err := runPrFlow(context.Background(), nil)
	require.Error(t, err)
	assert.Nil(t, payload)
	assert.True(t, isConfigClass(err), "a missing flow input is a config-class defect")
	assert.Contains(t, err.Error(), "flow input is required")
}

func TestCOV_CLI_114_RunPrFlowFailsClosedOnUnreadableManifest(t *testing.T) {
	// runPrFlow resolves its repo root from the process cwd.
	t.Chdir(t.TempDir())
	payload, err := runPrFlow(context.Background(), &PrFlowInput{})
	require.Error(t, err)
	assert.Nil(t, payload)
	assert.Contains(t, err.Error(), "manifest is not readable")
}

func TestCOV_CLI_115_RunPrFlowFailsClosedOnAnInvalidManifest(t *testing.T) {
	repo := covNewGitRepo(t)
	// Parses as YAML but fails schema validation (documents min(1)) — the flow
	// refuses BEFORE any stage. (schemas.ParseManifest already validates, so the
	// subsequent Manifest.Validate() re-validation inside runPrFlow is a
	// documented DEFENSIVE lane: no parsed manifest can reach it invalid.)
	repo.write(".docsync/manifest.yml", "version: 1\ndocuments: []\n")
	t.Chdir(repo.root)
	payload, err := runPrFlow(context.Background(), &PrFlowInput{})
	require.Error(t, err)
	assert.Nil(t, payload)
	// SUSPECTED MINOR BUG (report-only): an UNPARSEABLE manifest reaches the
	// caller as the raw schemas.ValidationError, so it fails closed on the
	// RUNTIME lane (exit 3) instead of the CONFIG lane (exit 2) — unlike the
	// unreadable-manifest lane, which is a proper CliConfigError. The flow still
	// refuses before any stage, so the safety property holds.
	assert.False(t, isConfigClass(err), "the parse lane is not config-class (see the bug note)")
	assert.Contains(t, err.Error(), "documents:")
}

func TestCOV_CLI_116_RunPrFlowSurfacesAConfigCompositionDefect(t *testing.T) {
	// A valid manifest but NO models.yml → the composition fails closed with a
	// CliConfigError, which the flow re-wraps without ever running a stage.
	repo := covNewGitRepo(t)
	repo.write(".docsync/manifest.yml", covManifest)
	repo.write(".docsync/instructions.md", "# instructions\n")
	t.Chdir(repo.root)
	payload, err := runPrFlow(context.Background(), &PrFlowInput{})
	require.Error(t, err)
	assert.Nil(t, payload)
	assert.True(t, isConfigClass(err))
	assert.Contains(t, err.Error(), "models.yml not found",
		"the composition defect is re-wrapped verbatim (a CliConfigError, D8)")
}

func TestCOV_CLI_117_RunPrFlowFixFlowRuntimeErrorSurfacesAsTypedError(t *testing.T) {
	// Fully configured repo → the composition gates pass and the fix flow runs.
	// The flow input carries NO Base/Head (the verb never threads them), so the
	// composed runtime's ref resolution fails closed and the fix flow ends as a
	// RUNTIME error — which runPrFlow surfaces through the docutil runtime error
	// helper (fail closed, D14). See the suspected-bug note in the file header.
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-test-run-key")
	repo := covSeedRepo(t)
	t.Chdir(repo.root)
	payload, err := runPrFlow(context.Background(), &PrFlowInput{ChangePr: "main", Base: "main"})
	require.Error(t, err)
	assert.Nil(t, payload)
	assert.False(t, isConfigClass(err), "a fix-flow runtime failure is the D14 lane, never config")
	assert.Contains(t, err.Error(), "docsync pr: fix flow ended runtime-error")
}

/* ---------------------------------------------------------------- audit flow */

func TestCOV_CLI_118_RunAuditFlowFailsClosedOnUnreadableManifest(t *testing.T) {
	result, err := runAuditFlow(context.Background(), AuditRunnerInput{RepoRoot: t.TempDir()})
	require.Error(t, err)
	assert.Equal(t, AuditRunResult{}, result)
	assert.True(t, isConfigClass(err))
}

func TestCOV_CLI_119_RunAuditFlowFailsClosedOnAnInvalidGitRoot(t *testing.T) {
	repo := covSeedRepo(t)
	result, err := runAuditFlow(context.Background(), AuditRunnerInput{RepoRoot: "relative/path/" + repo.root})
	require.Error(t, err)
	assert.Equal(t, AuditRunResult{}, result)
}

func TestCOV_CLI_120_RunAuditFlowRunsTheRealEngineAdvisoryNoop(t *testing.T) {
	// No nightly workflow file → the scheduler verdict is a typed NO-OP: a
	// noop is DATA, never a failure (exit 0), and nothing is written.
	repo := covSeedRepo(t)
	result, err := runAuditFlow(context.Background(), AuditRunnerInput{
		RepoRoot: repo.root,
		RunID:    "nightly:local/repo",
		Now:      "2026-01-01T00:00:00Z",
		Trigger:  "nightly",
		Branch:   "main",
	})
	require.NoError(t, err)
	assert.Equal(t, AuditRunNoop, result.Kind)
	assert.Equal(t, "nightly-not-configured", result.Reason)
}

func TestCOV_CLI_121_RunAuditFlowRunsTheRealEngineToCompletion(t *testing.T) {
	// The nightly workflow present + the default branch → the engine runs the
	// claim-support sweep over the fixture history (empty corpus → zero
	// findings, a clean advisory run).
	repo := covSeedRepo(t)
	repo.write(".github/workflows/docsync-loop-b-nightly.yml", "name: docsync-loop-b-nightly\n")
	repo.commitAll("chore: nightly loop-b workflow")

	result, err := runAuditFlow(context.Background(), AuditRunnerInput{
		RepoRoot:    repo.root,
		RunID:       "nightly:local/repo",
		Now:         "2026-01-01T00:00:00Z",
		Trigger:     "nightly",
		Branch:      "main",
		PostMerge:   false,
		Concurrency: 0, // <= 0 → the documented default of 4
		CommitWalk:  0, // <= 0 → the documented default of 50
	})
	require.NoError(t, err)
	assert.Equal(t, AuditRunRan, result.Kind)
	assert.Equal(t, "completed", result.Status)
	assert.Equal(t, "nightly:local/repo", result.RunID)
	assert.Equal(t, "nightly-scheduled", result.Trigger)
	assert.Equal(t, 0, result.FindingCount, "an empty claim corpus yields zero findings")
	assert.Equal(t, 0, result.AppendedCount)
	assert.Equal(t, "", result.Detail, "a completed run carries no failure detail")
}

func TestCOV_CLI_122_RunAuditFlowAbstainsOnAnUnsupportedManifestVersion(t *testing.T) {
	// A manifest the loader parses but the engine re-validation refuses →
	// abstained (fail closed), which the verb maps to exit 3.
	repo := covNewGitRepo(t)
	repo.write(".docsync/manifest.yml", "version: 2\ndocuments:\n  - path: docs/api.md\n    sources:\n      - \"src/**\"\n")
	repo.write(".github/workflows/docsync-loop-b-nightly.yml", "name: docsync-loop-b-nightly\n")
	repo.commitAll("chore: nightly workflow with an unsupported manifest version")
	result, err := runAuditFlow(context.Background(), AuditRunnerInput{
		RepoRoot: repo.root, RunID: "nightly:local/repo", Now: "2026-01-01T00:00:00Z",
		Trigger: "nightly", Branch: "main",
	})
	require.NoError(t, err)
	if result.Kind == AuditRunAbstained {
		assert.Contains(t, result.Reason, "version")
		return
	}
	// The loader may refuse the shape outright (a config-class error is the
	// other honest fail-closed lane).
	require.Error(t, err)
	assert.True(t, isConfigClass(err))
}

/* --------------------------------------------------------------- fix flow */

// covFakeRuntime is a deterministic CliRuntime double.
type covFakeRuntime struct {
	outcome *schemas.RunOutcome
	err     error
	calls   int
}

func (f *covFakeRuntime) Run(context.Context, CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	f.calls++
	return f.outcome, f.err
}

func (f *covFakeRuntime) Advisories() []string { return nil }

// covIncidentRecorder records the incident-sink calls.
type covIncidentRecorder struct {
	calls []string
}

func (r *covIncidentRecorder) Record(_, _, rule, offendingPath, _, _ string) error {
	r.calls = append(r.calls, rule+":"+offendingPath)
	return nil
}

func TestCOV_CLI_123_RunFixFlowDefaultsIoAndApplyPort(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	runID := "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"

	// A same-run skip returns before any Io use — the DefaultIo default is
	// exercised without writing to the real process streams.
	store := NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state"))
	require.NoError(t, store.Write(FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: runID}))
	skipped := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{}, Git: git, Store: store, Clock: covFixedClock,
	}, FixFlowInput{RepoRoot: repo.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: runID})
	assert.Equal(t, FixResultNoop, skipped.Kind)

	// An unbound apply port refuses (fail closed) instead of guessing.
	confidence := 0.95
	runtime := &covFakeRuntime{outcome: &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID, RunID: "run#a..b",
		Key:    schemas.RunKey{Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status: schemas.RunStatusCompleted,
		Documents: []schemas.RunDocument{{
			DocPath: "docs/api.md", PolicyOutcome: schemas.PipelineOutcomeAuto, Confidence: &confidence,
			Verification: &schemas.RunDocumentVerification{Verdict: schemas.VerdictPass, AutoEligible: true},
		}},
		Provenance: schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
	}}
	incidents := &covIncidentRecorder{}
	result := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: runtime, Git: git, Clock: covFixedClock,
		Store:       NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state")),
		Incidents:   incidents,
		PatchSource: covPatchSource,
	}, FixFlowInput{RepoRoot: repo.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9F"})
	assert.Equal(t, FixResultCompleted, result.Kind)
	require.Len(t, result.Refused, 1)
	assert.Equal(t, "abstain", result.Refused[0].Reason)
	assert.Contains(t, result.Refused[0].Detail, "fix apply port is not bound")
}

func TestCOV_CLI_124_RunFixFlowConfigAndManifestDiagnosticLanes(t *testing.T) {
	repo := covNewGitRepo(t)
	repo.write(".docsync/manifest.yml", "version: 0\ndocuments:\n  - path: docs/api.md\n    sources:\n      - \"src/**\"\n")
	result := RunFixFlow(context.Background(), FixFlowDeps{Clock: covFixedClock},
		FixFlowInput{RepoRoot: repo.root, RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"})
	assert.Equal(t, FixResultConfigError, result.Kind)
	assert.Contains(t, result.Detail, "E_MANIFEST_SCHEMA")

	// A missing manifest renders the absent-manifest detail.
	result = RunFixFlow(context.Background(), FixFlowDeps{Clock: covFixedClock},
		FixFlowInput{RepoRoot: t.TempDir(), RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"})
	assert.Equal(t, FixResultConfigError, result.Kind)
	assert.Contains(t, result.Detail, "manifest.yml not found")
}

func TestCOV_CLI_125_RunFixFlowStoreWriteFailureIsARuntimeError(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	// The state dir is blocked by a regular file → the CAS write fails loud.
	blocker := filepath.Join(repo.root, ".docsync", "state")
	require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o644))
	result := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{outcome: covEmptyOutcome()},
		Git:     git,
		Store:   NewFixRunRecordStore(blocker),
		Clock:   covFixedClock,
	}, FixFlowInput{RepoRoot: repo.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"})
	assert.Equal(t, FixResultRuntimeErr, result.Kind)
	assert.Contains(t, result.Detail, "not a dir")
}

func TestCOV_CLI_126_RunFixFlowSkipAndRefuseLanes(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)

	// One candidate outside the manifest allowlist (invariants 2/3) and one
	// inside it with a rejected verification (never applied).
	confidence := 0.95
	runtime := &covFakeRuntime{outcome: &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID, RunID: "run#a..b",
		Key:    schemas.RunKey{Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status: schemas.RunStatusCompleted,
		Documents: []schemas.RunDocument{
			{DocPath: "src/payments/refund.ts", PolicyOutcome: schemas.PipelineOutcomeAuto},
			{
				DocPath: "docs/api.md", PolicyOutcome: schemas.PipelineOutcomeAuto, Confidence: &confidence,
				Verification: &schemas.RunDocumentVerification{Verdict: schemas.VerdictReject},
			},
		},
		Provenance: schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
	}}
	incidents := &covIncidentRecorder{}
	result := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: runtime, Git: git, Incidents: incidents, Clock: covFixedClock,
		Store: NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state")),
	}, FixFlowInput{RepoRoot: repo.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"})
	assert.Equal(t, FixResultCompleted, result.Kind)
	assert.Empty(t, result.Applied)
	reasons := map[string]string{}
	for _, entry := range result.Skipped {
		reasons[entry.DocPath] = entry.Reason
	}
	assert.Equal(t, "out-of-scope", reasons["src/payments/refund.ts"])
	assert.Equal(t, "unverified", reasons["docs/api.md"])
	assert.Equal(t, []string{"out_of_allowlist_path:src/payments/refund.ts"}, incidents.calls)

	// A patch-source error and a noop apply outcome each map to their own lane.
	patchSourceErr := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{outcome: covAutoVerified("docs/api.md")},
		Git:     git, Clock: covFixedClock,
		Store: NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state")),
		PatchSource: func(context.Context, string, *string) (*schemas.DocPatch, error) {
			return nil, errors.New("patch store offline")
		},
		Apply: covApplyFn("applied", "x"),
	}, FixFlowInput{RepoRoot: repo.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9F"})
	assert.Equal(t, FixResultCompleted, patchSourceErr.Kind)
	require.Len(t, patchSourceErr.Refused, 1)
	assert.Equal(t, "abstain", patchSourceErr.Refused[0].Reason)
	assert.Contains(t, patchSourceErr.Refused[0].Detail, "patch store offline")

	noop := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{outcome: covAutoVerified("docs/api.md")},
		Git:     git, Clock: covFixedClock,
		Store: NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state")),
		PatchSource: func(context.Context, string, *string) (*schemas.DocPatch, error) {
			return &schemas.DocPatch{Schema: "docsync.doc-patch/v1", RunID: "r", DocPath: "docs/api.md"}, nil
		},
		Apply: covApplyFn("noop", ""),
	}, FixFlowInput{RepoRoot: repo.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9G"})
	assert.Equal(t, FixResultCompleted, noop.Kind)
	require.Len(t, noop.Skipped, 1)
	assert.Equal(t, "same-run-skip", noop.Skipped[0].Reason)

	// An unanchored outcome with no reasons falls back to the stable detail.
	unanchored := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{outcome: covAutoVerified("docs/api.md")},
		Git:     git, Clock: covFixedClock,
		Store: NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state")),
		PatchSource: func(context.Context, string, *string) (*schemas.DocPatch, error) {
			return &schemas.DocPatch{Schema: "docsync.doc-patch/v1", RunID: "r", DocPath: "docs/api.md"}, nil
		},
		Apply: covApplyFn("unanchored", ""),
	}, FixFlowInput{RepoRoot: repo.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9H"})
	assert.Equal(t, FixResultCompleted, unanchored.Kind)
	require.Len(t, unanchored.Refused, 1)
	assert.Equal(t, "unanchored", unanchored.Refused[0].Reason)
	assert.Equal(t, "anchors did not resolve content-hash-first", unanchored.Refused[0].Detail)
}

func TestCOV_CLI_127_RunFixFlowStalePatchRedetectionWithoutCandidateRefuses(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	// The re-detection run returns an outcome WITHOUT the doc → the race
	// refuses as stale-patch rather than force-applying.
	runtime := &covSequencedRuntime{outcomes: []*schemas.RunOutcome{
		covAutoVerified("docs/api.md"),
		covEmptyOutcome(),
	}}
	result := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: runtime, Git: git, Clock: covFixedClock,
		Store: NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state")),
		PatchSource: func(context.Context, string, *string) (*schemas.DocPatch, error) {
			return &schemas.DocPatch{Schema: "docsync.doc-patch/v1", RunID: "r", DocPath: "docs/api.md"}, nil
		},
		Apply: covRaceAbstain(),
	}, FixFlowInput{RepoRoot: repo.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"})
	assert.Equal(t, FixResultCompleted, result.Kind)
	require.Len(t, result.Refused, 1)
	assert.Equal(t, "stale-patch", result.Refused[0].Reason)
	assert.Contains(t, result.Refused[0].Detail, "re-detection produced no candidate")
	assert.True(t, result.Refused[0].ReDetected)
	assert.Equal(t, 2, runtime.calls, "exactly one re-detection")
}

// covPatchSource is a deterministic patch-store double.
func covPatchSource(context.Context, string, *string) (*schemas.DocPatch, error) {
	return &schemas.DocPatch{Schema: "docsync.doc-patch/v1", RunID: "r", DocPath: "docs/api.md"}, nil
}

// covFixedClock is the injected deterministic clock for the flow deps.
func covFixedClock() string { return "2026-01-01T00:00:00Z" }

func covEmptyOutcome() *schemas.RunOutcome {
	return &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID, RunID: "run#a..b",
		Key:    schemas.RunKey{Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status: schemas.RunStatusCompleted, Documents: []schemas.RunDocument{},
		Provenance: schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
	}
}

func covAutoVerified(docPath string) *schemas.RunOutcome {
	confidence := 0.95
	return &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID, RunID: "run#a..b",
		Key:    schemas.RunKey{Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status: schemas.RunStatusCompleted,
		Documents: []schemas.RunDocument{{
			DocPath: docPath, PolicyOutcome: schemas.PipelineOutcomeAuto, Confidence: &confidence,
			Verification: &schemas.RunDocumentVerification{Verdict: schemas.VerdictPass, AutoEligible: true},
		}},
		Provenance: schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
	}
}

func covApplyFn(kind, resultContent string) FixApplyFn {
	return func(context.Context, *schemas.DocPatch, *string, map[string]bool, map[string]bool, func(string, string) bool) (FixApplyOutcome, error) {
		return FixApplyOutcome{Kind: kind, ResultContent: resultContent}, nil
	}
}

func covRaceAbstain() FixApplyFn {
	return func(context.Context, *schemas.DocPatch, *string, map[string]bool, map[string]bool, func(string, string) bool) (FixApplyOutcome, error) {
		return FixApplyOutcome{
			Kind: "abstain", AbstainReason: string(patch.AbstainDocChangedRaceUnverified), Detail: "doc-changed race unverified",
		}, nil
	}
}

// covSequencedRuntime returns a fixed script of outcomes (the re-detection spy).
type covSequencedRuntime struct {
	outcomes []*schemas.RunOutcome
	calls    int
}

func (r *covSequencedRuntime) Run(context.Context, CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	index := r.calls
	r.calls++
	if index >= len(r.outcomes) {
		index = len(r.outcomes) - 1
	}
	return r.outcomes[index], nil
}

func (r *covSequencedRuntime) Advisories() []string { return nil }
