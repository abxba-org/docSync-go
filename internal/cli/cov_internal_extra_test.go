package cli

// Coverage-completion suite, part 3 — internal lanes of the composed runtime
// (offline outcome/exit semantics, the real patch applier, the incident sink,
// the evidence workspace) plus the fix-flow failure lanes. INTERNAL test
// (package cli); deterministic clocks and a local git fixture only.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------- offline runtime semantics */

func TestCOV_CLI_201_OfflineOutcomeInconclusiveAnalysisLane(t *testing.T) {
	manifest, err := schemas.ParseManifest([]byte(covManifest))
	require.NoError(t, err)
	documentGraph := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: *manifest})

	outcome, err := offlineOutcome(offlineOutcomeInput{
		Repo: "local/repo", BaseSha: "a", HeadSha: "b", Manifest: *manifest,
		PolicyVersion: 1, Clock: covFixedClock, Graph: documentGraph,
		Analysis: analysis.AnalysisOutcome{Kind: analysis.AnalysisOutcomeKindInconclusive, Reason: "all analyzers unavailable"},
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.RunStatusDegraded, outcome.Status)
	assert.Equal(t, "local/repo#a..b", outcome.RunID)
	assert.Empty(t, outcome.Documents, "an inconclusive analysis maps no doc rows")
	require.Len(t, outcome.Abstains, 1)
	assert.Equal(t, string(schemas.AbstainReasonOther), outcome.Abstains[0].Reason)
	assert.Nil(t, outcome.Abstains[0].DocPath)
	assert.Nil(t, outcome.UnknownCoverage, "no analysis record → no unknown coverage")
	// The analyze stage renders INCONCLUSIVE (never a fabricated ok).
	analyze := -1
	for i, stage := range outcome.Stages {
		if stage.Stage == "analyze" {
			analyze = i
		}
	}
	require.GreaterOrEqual(t, analyze, 0)
	assert.Equal(t, schemas.RunStageInconclusive, outcome.Stages[analyze].Status)
	// The analyzer-inconclusive degradation rides along (resolveOfflineExitCode
	// reads it to map an inconclusive analysis onto drift).
	sawInconclusive := false
	for _, d := range outcome.Degradations {
		if d.Kind == schemas.DegradationAnalyzerInconclusive {
			sawInconclusive = true
		}
	}
	assert.True(t, sawInconclusive, "an inconclusive analysis degrades with the analyzer-inconclusive kind")
	assert.NotEmpty(t, outcome.Degradations)
}

func TestCOV_CLI_202_OfflineOutcomeAnalysisRecordLane(t *testing.T) {
	manifest, err := schemas.ParseManifest([]byte(covManifest))
	require.NoError(t, err)
	documentGraph := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{Manifest: *manifest})
	record := &schemas.ChangeAnalysis{
		Repo:    "local/repo",
		BaseSHA: "a",
		HeadSHA: "b",
		Files: []schemas.ChangedFile{
			{Path: "src/payments/refund.ts", Status: "modified"},
			{Path: "src/payments/refund.ts", Status: "modified"}, // deduped
			{Path: "README.md", Status: "added"},                 // mapped by no doc
		},
	}

	outcome, err := offlineOutcome(offlineOutcomeInput{
		Repo: "local/repo", BaseSha: "a", HeadSha: "b", Manifest: *manifest,
		PolicyVersion: 1, Clock: covFixedClock, Graph: documentGraph,
		Analysis: analysis.AnalysisOutcome{Kind: analysis.AnalysisOutcomeKindAnalysis, Record: record},
	})
	require.NoError(t, err)
	// The mapped doc is a review row (offline never fabricates auto) with a
	// doc-path abstain naming the model-unavailable reason.
	require.Len(t, outcome.Documents, 1)
	assert.Equal(t, "docs/api.md", outcome.Documents[0].DocPath)
	assert.Equal(t, schemas.PipelineOutcomeReview, outcome.Documents[0].PolicyOutcome)
	require.NotNil(t, outcome.Documents[0].Abstain)
	assert.Equal(t, string(schemas.AbstainReasonModelUnavailable), outcome.Documents[0].Abstain.Reason)
	assert.Equal(t, OFFLINE_ABSTAIN_DETAIL, outcome.Documents[0].Abstain.Detail)
	// The unmapped changed file lands in the honest unknown-coverage list.
	require.NotNil(t, outcome.UnknownCoverage)
	assert.Equal(t, []string{"README.md"}, outcome.UnknownCoverage,
		"an unmapped changed path is honest unknown coverage")
}

func TestCOV_CLI_203_ResolveOfflineExitCodeLanes(t *testing.T) {
	// Empty window → 0.
	assert.Equal(t, ExitOK, resolveOfflineExitCode(&schemas.RunOutcome{
		Status: schemas.RunStatusDegraded, Documents: []schemas.RunDocument{},
	}))
	// Any non-empty window → 1 (never a fabricated clean exit).
	assert.Equal(t, ExitDrift, resolveOfflineExitCode(&schemas.RunOutcome{
		Documents: []schemas.RunDocument{{DocPath: "docs/api.md"}},
	}))
	assert.Equal(t, ExitDrift, resolveOfflineExitCode(&schemas.RunOutcome{
		UnknownCoverage: []string{"src/x.ts"},
	}))
	inconclusive := resolveOfflineExitCode(&schemas.RunOutcome{
		Degradations: []schemas.Degradation{{Kind: schemas.DegradationAnalyzerInconclusive, Detail: "all analyzers unavailable"}},
	})
	assert.Equal(t, ExitDrift, inconclusive, "an inconclusive analysis is drift, never a clean exit (D14)")
}

func TestCOV_CLI_204_CreateOfflineRunFnRefResolutionLanes(t *testing.T) {
	repo := covSeedRepo(t)
	runFn := createOfflineRunFn(CliRuntimeOptions{
		RepoRoot: repo.root, Repo: "local/repo",
		Clock: covFixedClock,
	}, *covMustManifest(t), 1)

	_, err := runFn(context.Background(), CliRuntimeRunInput{Base: "definitely-missing-ref", Head: "HEAD"})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeGit, docutilCodeOf(err))
	assert.Contains(t, err.Error(), "cannot resolve base/head refs")

	_, err = runFn(context.Background(), CliRuntimeRunInput{Base: "HEAD", Head: "also-missing"})
	require.Error(t, err)
	assert.Equal(t, docutil.CodeGit, docutilCodeOf(err))
}

func covMustManifest(t *testing.T) *schemas.Manifest {
	t.Helper()
	manifest, err := schemas.ParseManifest([]byte(covManifest))
	require.NoError(t, err)
	return manifest
}

func docutilCodeOf(err error) string {
	var typed *docutil.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

/* ------------------------------------------------------ the real patch applier */

// covMakePatch builds a schema-valid single-hunk patch anchored on line 3 of
// baseContent (the same anchor convention the TS suite pins: content_hash is
// the bare sha256 of the quoted delete span; baseContentHash is the doc hash).
// anchorHash overrides the hunk content hash (a bogus value → unanchored).
func covMakePatch(docPath, baseContent, insertText string) *schemas.DocPatch {
	lines := strings.Split(baseContent, "\n")
	for len(lines) < 4 {
		lines = append(lines, "filler")
	}
	line3 := lines[2]
	baseHash := docutil.ContentHash(baseContent)
	return &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "cov-run",
		DocPath:         docPath,
		BaseContentHash: baseHash,
		Hunks: []schemas.PatchHunk{{
			Anchor:     schemas.PatchAnchor{StartLine: 3, EndLine: 3, ContentHash: claims.HashSpan(line3)},
			DeleteText: line3,
			InsertText: insertText,
			EvidenceRefs: []schemas.Evidence{{
				Kind: schemas.EvidenceKindDocAnchor,
				Ref:  docPath + "#line-3",
				Note: "line 3",
			}},
		}},
		EvidenceRefs: []schemas.Evidence{},
		Minimality:   schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 0},
		Provenance:   schemas.DocPatchProvenance{PolicyVersion: 1, WriterModel: "anthropic/test", GeneratedAt: covFixedClock()},
	}
}

// covUnanchoredPatch anchors on a hash that matches no line (unanchored).
func covUnanchoredPatch(docPath, baseContent, insertText string) *schemas.DocPatch {
	patchDoc := covMakePatch(docPath, baseContent, insertText)
	patchDoc.Hunks[0].Anchor.ContentHash = strings.Repeat("f", 64)
	return patchDoc
}

func TestCOV_CLI_205_ApplyDocPatchRealAppliesAndRefusesCleanly(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	// A document with enough non-empty lines that a one-line replacement stays
	// well under the whole-document-rewrite ratio (invariant 12).
	docPath := "docs/apply.md"
	original := "# Apply target\n\nLine a\nLine b\nLine c\nLine d\nLine e\nLine f\nThe refund call captures the payment.\n"
	repo.write(docPath, original)
	repo.commitAll("chore: apply target")

	apply := applyDocPatchReal(git)
	allowlist := map[string]bool{docPath: true}
	const insertText = "The refund ledger is append-only."

	// (a) A well-anchored patch applies at HEAD (isRunApplied nil → the same-run
	// lane never fires).
	worktreeContent := original
	applied := strings.Replace(original, "Line a", insertText, 1)
	require.NotEqual(t, original, applied)
	outcome, err := apply(context.Background(), covMakePatch(docPath, worktreeContent, insertText),
		&worktreeContent, allowlist, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "applied", outcome.Kind)
	assert.Equal(t, applied, outcome.ResultContent)

	// (b) An anchor that matches no line abstains unanchored, and the hunk
	// reasons render through the mapped outcome.
	outcome, err = apply(context.Background(), covUnanchoredPatch(docPath, worktreeContent, insertText),
		&worktreeContent, allowlist, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "unanchored", outcome.Kind)
	assert.NotEmpty(t, outcome.UnanchoredReasons, "the unanchored hunk reasons render")

	// (c) A doc-less patch cannot derive its patch ref → the typed error
	// surfaces from the applier.
	outcome, err = apply(context.Background(), covMakePatch("", "x", "y"), nil, allowlist, nil, nil)
	require.Error(t, err)
	assert.Equal(t, FixApplyOutcome{}, outcome)

	// (d) The isRunApplied hook is exercised when supplied.
	noopOutcome, err := apply(context.Background(), covMakePatch(docPath, worktreeContent, insertText),
		&worktreeContent, allowlist, nil, func(string, string) bool { return true })
	require.NoError(t, err)
	assert.Equal(t, "noop", noopOutcome.Kind)
}

/* ------------------------------------------------------------ incident sink */

func TestCOV_CLI_206_FileIncidentSinkRecordLanes(t *testing.T) {
	// Blocked state dir (the parent is a regular file) → the MkdirAll failure
	// surfaces, exactly one retry, fail loud.
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o644))
	sink := newFileIncidentSink(blocker)
	err := sink.Record(covFixedClock(), "run-1", "protected_path_patch", "docs/x.md", "detail", "docs/x.md")
	require.Error(t, err)

	// A read-only state dir → the write fails after one retry and returns a
	// loud error naming the incident id and rule.
	root := t.TempDir()
	stateDir := filepath.Join(root, ".docsync", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.Chmod(stateDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })
	sink = newFileIncidentSink(stateDir)
	err = sink.Record(covFixedClock(), "run-2", "protected_path_patch", "docs/y.md", "detail", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be persisted after one retry")
	assert.Contains(t, err.Error(), "protected_path_patch")

	// The happy path appends exactly one JSONL line with the derived id.
	root2 := t.TempDir()
	sink = newFileIncidentSink(filepath.Join(root2, ".docsync", "state"))
	require.NoError(t, sink.Record(covFixedClock(), "run-3", "protected_path_patch", "docs/z.md", "detail", "docs/z.md"))
	data, readErr := os.ReadFile(filepath.Join(root2, ".docsync", "state", "incidents.jsonl"))
	require.NoError(t, readErr)
	assert.Contains(t, string(data), `"rule":"protected_path_patch"`)
	assert.Contains(t, string(data), `"incident_id":"inc-`)
}

func TestCOV_CLI_207_FixRunRecordStoreWriteFailureOnAReadOnlyStateDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based write failures do not apply to root")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, ".docsync", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.Chmod(stateDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })
	store := NewFixRunRecordStore(stateDir)
	err := store.Write(FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"})
	require.Error(t, err)
}

/* ---------------------------------------------- evidence workspace sources */

func TestCOV_CLI_208_CliEvidenceWorkspaceSourcesDedupesAndBounds(t *testing.T) {
	repo := covSeedRepo(t)
	repo.write("src/payments/a.ts", "export const a = 1;\n")
	repo.write("src/payments/b.ts", "export const b = 2;\n")
	repo.write(".docsync/secret.md", "not source evidence\n")
	repo.commitAll("chore: evidence sources")
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	head := strings.TrimSpace(repo.git("rev-parse", "HEAD"))

	ref := func(target string) string { return target + ":10" }
	patchDoc := &schemas.DocPatch{
		Schema: "docsync.doc-patch/v1", RunID: "cov", DocPath: "docs/api.md",
		Hunks: []schemas.PatchHunk{{EvidenceRefs: []schemas.Evidence{
			{Ref: ref("src/payments/a.ts")},
			{Ref: ref("src/payments/a.ts")},  // deduped
			{Ref: ref(".docsync/secret.md")}, // skipped (state dir)
			{Ref: "../outside" + "/file.ts"}, // skipped (parent escape)
			{Ref: ref("src/payments/b.ts")},
			{Ref: "src/payments/b.ts"},                    // deduped (no :line suffix)
			{Ref: "no-colon-token-with-empty-path"},       // skipped (empty path)
			{Ref: ref("src/payments/never-committed.ts")}, // unreadable → omitted
		}}},
	}
	sources := cliEvidenceWorkspaceSources(git, head, patchDoc)
	assert.Equal(t, map[string]string{
		"src/payments/a.ts": "export const a = 1;\n",
		"src/payments/b.ts": "export const b = 2;\n",
	}, sources)
}

/* ------------------------------------------------------- audit state store */

/* ---------------------------------------------------- fix flow failure lanes */

func TestCOV_CLI_210_RunFixFlowFailureLanes(t *testing.T) {
	// Each lane gets a FRESH fixture: an applied fix legitimately writes the
	// doc, after which the per-doc dirty gate would skip the later lanes.
	storeDir := func() string { return t.TempDir() }
	mkFixture := func() (*covGitRepo, *gitx.GitClient) {
		repo := covSeedRepo(t)
		client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
		require.NoError(t, err)
		return repo, client
	}

	// (1) A patch source that returns (nil, nil) → the not-a-candidate skip.
	repo1, git1 := mkFixture()
	skipped := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{outcome: covAutoVerified("docs/api.md")}, Git: git1, Clock: covFixedClock,
		Store: NewFixRunRecordStore(storeDir()), PatchSource: covNilPatchSource, Apply: covApplyFn("applied", "x"),
	}, FixFlowInput{RepoRoot: repo1.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"})
	assert.Equal(t, FixResultCompleted, skipped.Kind)
	require.Len(t, skipped.Skipped, 1)
	assert.Equal(t, "not-a-candidate", skipped.Skipped[0].Reason)

	// (2) A doc-less patch cannot derive its patch ref → the abstain refusal.
	repo2, git2 := mkFixture()
	refused := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{outcome: covAutoVerified("docs/api.md")}, Git: git2, Clock: covFixedClock,
		Store: NewFixRunRecordStore(storeDir()), PatchSource: covDoclessPatchSource, Apply: covApplyFn("applied", "x"),
	}, FixFlowInput{RepoRoot: repo2.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9F"})
	assert.Equal(t, FixResultCompleted, refused.Kind)
	require.Len(t, refused.Refused, 1)
	assert.Contains(t, refused.Refused[0].Detail, "doc path is empty",
		"the patch-ref derivation failure renders its error")

	// (3) A doc-changed race whose re-detection itself fails (rerr) → the
	// stale-patch refusal carries the re-detection error (D14).
	race := &covScriptedRuntime{steps: []covRuntimeStep{
		{outcome: covAutoVerified("docs/api.md")},
		{err: errors.New("re-detection pipeline offline")},
	}}
	repo3, git3 := mkFixture()
	incidentErr := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: race, Git: git3, Clock: covFixedClock,
		Store:       NewFixRunRecordStore(storeDir()),
		PatchSource: covPatchSource,
		Apply:       covRaceAbstain(),
	}, FixFlowInput{RepoRoot: repo3.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9G"})
	assert.Equal(t, FixResultCompleted, incidentErr.Kind)
	require.Len(t, incidentErr.Refused, 1)
	assert.Equal(t, "stale-patch", incidentErr.Refused[0].Reason)
	assert.Contains(t, incidentErr.Refused[0].Detail, "re-detection pipeline offline")
	assert.True(t, incidentErr.Refused[0].ReDetected)

	// (4) A completed run whose report cannot be built → runtime failure.
	brokenOutcome := &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID, RunID: "run#a..b",
		Key:    schemas.RunKey{Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status: schemas.RunStatusCompleted,
		Documents: []schemas.RunDocument{{
			DocPath: "docs/api.md", PolicyOutcome: schemas.PipelineOutcome("bogus-outcome"),
		}},
		Provenance: schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
	}
	repo4, git4 := mkFixture()
	broken := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{outcome: brokenOutcome}, Git: git4, Clock: covFixedClock,
		Store: NewFixRunRecordStore(storeDir()),
	}, FixFlowInput{RepoRoot: repo4.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9H"})
	assert.Equal(t, FixResultRuntimeErr, broken.Kind)
	assert.Contains(t, broken.Detail, "canonical report failed validation")

	// (5) An unanchored abort WITH reasons renders the joined reasons.
	repo5, git5 := mkFixture()
	unanchored := RunFixFlow(context.Background(), FixFlowDeps{
		Runtime: &covFakeRuntime{outcome: covAutoVerified("docs/api.md")}, Git: git5, Clock: covFixedClock,
		Store:       NewFixRunRecordStore(storeDir()),
		PatchSource: covPatchSource,
		Apply: func(context.Context, *schemas.DocPatch, *string, map[string]bool, map[string]bool, func(string, string) bool) (FixApplyOutcome, error) {
			return FixApplyOutcome{
				Kind:              patchOutcomeUnanchored,
				UnanchoredReasons: []string{"content_hash_mismatch", "line_out_of_range"},
			}, nil
		},
	}, FixFlowInput{RepoRoot: repo5.root, Repo: "local/repo", Base: "main", Head: "HEAD", RunID: "01J9ZK7Q2M8P4X6T2W5Y3C0A9J"})
	assert.Equal(t, FixResultCompleted, unanchored.Kind)
	require.Len(t, unanchored.Refused, 1)
	assert.Equal(t, "content_hash_mismatch; line_out_of_range", unanchored.Refused[0].Detail,
		"the unanchored hunk reasons join with '; '")
}

// covRuntimeStep is one scripted runtime response.
type covRuntimeStep struct {
	outcome *schemas.RunOutcome
	err     error
}

// covScriptedRuntime serves its script positionally (the re-detection spy).
type covScriptedRuntime struct {
	steps []covRuntimeStep
	calls int
}

func (r *covScriptedRuntime) Run(context.Context, CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	index := r.calls
	r.calls++
	if index >= len(r.steps) {
		index = len(r.steps) - 1
	}
	return r.steps[index].outcome, r.steps[index].err
}

func (r *covScriptedRuntime) Advisories() []string { return nil }

func covNilPatchSource(context.Context, string, *string) (*schemas.DocPatch, error) {
	return nil, nil
}

func covDoclessPatchSource(context.Context, string, *string) (*schemas.DocPatch, error) {
	return &schemas.DocPatch{Schema: "docsync.doc-patch/v1", RunID: "r"}, nil
}

var _ = state.StateStoreOptions{}
