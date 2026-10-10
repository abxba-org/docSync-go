package cli_test

// Ported from packages/cli/test/fix-flow.test.ts (P7-T08..T13 flow core) —
// flow-level lanes over a FAKE runtime + REAL fixture repo: same-run skip
// without pipeline invocation; unverified patch never applied (V-P7-15);
// out-of-scope/locked candidates blocked with incidents (V-P7-10); dirty
// gating (V-P7-9); stale-patch refuse-and-rerun with EXACTLY ONE
// re-detection and no force-apply (V-P7-8); applied writes + record;
// config-error lane; credential redaction (§3 rule 8).
//
// DEVIATION NOTES: the Go flow carries no env surface at all (FixFlowDeps has
// no credentials member), so the redaction lane is pinned by scanning every
// captured line — structurally stronger than the TS env-carrying deps. The
// Go fix-run record persists only the applied rows (+ their patch refs); the
// TS record's skipped/refused rows are asserted here through the flow result
// instead of the record.
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cliFlowNow   = "2026-10-07T18:00:00Z"
	cliFlowRepo  = "acme/cli-fixture"
	cliApiInsert = "The refund call captures and voids the payment."
)

// cliFlowOutcome is the TS makeOutcome: a completed run proposing documents.
func cliFlowOutcome(documents []schemas.RunDocument) *schemas.RunOutcome {
	return &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID,
		RunID:  "run_01J9ZK6X8A",
		Key: schemas.RunKey{
			Repo: cliFlowRepo, BaseSHA: "base123", HeadSHA: "head456",
			ManifestVersion: 1, PolicyVersion: 1,
		},
		StartedAt:   strPtrValue(cliFlowNow),
		FinishedAt:  strPtrValue(cliFlowNow),
		Status:      schemas.RunStatusCompleted,
		Stages:      []schemas.PipelineStage{{Stage: "analyze", Status: schemas.RunStageOk}},
		Documents:   documents,
		Provenance:  schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
		EarlySignal: schemas.EarlySignal{},
		StateChurn:  schemas.StateChurn{},
	}
}

// cliVerifiedDoc is the TS verifiedDoc: an auto/suggest doc with a passing,
// auto-eligible verification block.
func cliVerifiedDoc(docPath string, outcome schemas.PipelineOutcome) schemas.RunDocument {
	return schemas.RunDocument{
		DocPath:         docPath,
		PolicyOutcome:   outcome,
		PartialCoverage: false,
		Confidence:      floatPtr(0.95),
		Verification: &schemas.RunDocumentVerification{
			Verdict:      schemas.VerdictPass,
			AutoEligible: true,
			PatchRef:     strPtrValue("state/docs/docs__api.md#patch-fake"),
		},
	}
}

func floatPtr(f float64) *float64 { return &f }

// cliMakePatch is the TS makePatch: a single-hunk line-3 replacement over
// baseContent (the anchor convention: content_hash = hashSpan over the
// quoted delete_text span); stale swaps in a bogus base_content_hash.
func cliMakePatch(docPath, baseContent string, stale bool) *schemas.DocPatch {
	lines := strings.Split(baseContent, "\n")
	line3 := lines[2]
	baseHash := docutil.ContentHash(baseContent)
	if stale {
		baseHash = strings.Repeat("f", 64)
	}
	return &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "run_01J9ZK6X8A",
		DocPath:         docPath,
		BaseContentHash: baseHash,
		Hunks: []schemas.PatchHunk{{
			Anchor: schemas.PatchAnchor{
				StartLine:   3,
				EndLine:     3,
				ContentHash: claims.HashSpan(line3),
			},
			DeleteText: line3,
			InsertText: cliApiInsert,
			EvidenceRefs: []schemas.Evidence{{
				Kind: schemas.EvidenceKindDocAnchor,
				Ref:  docPath + "#line-3",
				Note: "line 3",
			}},
		}},
		EvidenceRefs: []schemas.Evidence{},
		Minimality:   schemas.DocPatchMinimality{Measured: false, AddedLines: 1, RemovedLines: 0},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: 1,
			WriterModel:   "anthropic/claude-sonnet-4.5",
			GeneratedAt:   cliFlowNow,
		},
	}
}

/* ------------------------------------------------------- test doubles */

// cliCountingRuntime records every Run call (the TS runCalls spy).
type cliCountingRuntime struct {
	mu        sync.Mutex
	calls     int
	outcomeFn func(call int) *schemas.RunOutcome
}

func (r *cliCountingRuntime) Run(_ context.Context, _ cli.CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	return r.outcomeFn(call), nil
}

func (r *cliCountingRuntime) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *cliCountingRuntime) Advisories() []string { return nil }

// cliFileIncidents appends safety-incident JSONL lines (the TS
// createIncidentLog(fileIncidentSink(stateDir))).
type cliFileIncidents struct {
	path string
	mu   sync.Mutex
}

func (s *cliFileIncidents) Record(at, runID, rule, offendingPath, detail, docPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(map[string]string{
		"at": at, "run_id": runID, "rule": rule,
		"offending_path": offendingPath, "detail": detail, "doc_path": docPath,
	})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}

func (s *cliFileIncidents) lines(t *testing.T) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(s.path)
	require.NoError(t, err)
	var out []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var incident map[string]string
		require.NoError(t, json.Unmarshal([]byte(line), &incident))
		out = append(out, incident)
	}
	return out
}

// cliApplyFn builds a fixed-outcome apply fake (the TS apply double).
func cliApplyFn(outcome cli.FixApplyOutcome) cli.FixApplyFn {
	return func(context.Context, *schemas.DocPatch, *string, map[string]bool, map[string]bool, func(string, string) bool) (cli.FixApplyOutcome, error) {
		return outcome, nil
	}
}

// cliFlowDeps assembles the injected deps over a real fixture repo.
func cliFlowDeps(t *testing.T, repo *gitFixture, runtime cli.CliRuntime, io cli.CliIo) (cli.FixFlowDeps, *cli.FixRunRecordStore, *cliFileIncidents) {
	t.Helper()
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	store := cli.NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state"))
	incidents := &cliFileIncidents{path: filepath.Join(repo.root, ".docsync", "state", "incidents.jsonl")}
	return cli.FixFlowDeps{
		Runtime:   runtime,
		Git:       git,
		Store:     store,
		Incidents: incidents,
		Io:        io,
		Clock:     func() string { return cliFlowNow },
	}, store, incidents
}

func cliFlowInput(repoRoot, runID string) cli.FixFlowInput {
	return cli.FixFlowInput{
		RepoRoot: repoRoot,
		Repo:     cliFlowRepo,
		Base:     "main",
		Head:     "HEAD",
		RunID:    runID,
	}
}

/* ------------------------------------------------------------------ suites */

func TestFixFlowSameRunSkipShortCircuitsBeforePipeline(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	runID := genULID(t)
	store := cli.NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state"))
	require.NoError(t, store.Write(cli.FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: runID}))

	captured := captureIo()
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	failing := &cliFixRunPipelineSpy{}
	deps := cli.FixFlowDeps{Runtime: failing, Git: git, Store: store, Io: captured}

	result := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, runID))

	assert.Equal(t, cli.FixResultNoop, result.Kind)
	assert.Equal(t, "same-run-skip", result.Reason)
	assert.Equal(t, 0, failing.calls, "pipeline must NEVER run for a same-run skip")
}

func TestFixFlowFailingPatchIsNeverApplied(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	docs := []schemas.RunDocument{{
		DocPath:         "docs/api.md",
		PolicyOutcome:   schemas.PipelineOutcomeAuto,
		PartialCoverage: false,
		Verification: &schemas.RunDocumentVerification{
			Verdict: schemas.VerdictReject, AutoEligible: false,
		},
	}}
	runtime := &cliCountingRuntime{outcomeFn: func(int) *schemas.RunOutcome { return cliFlowOutcome(docs) }}
	deps, _, _ := cliFlowDeps(t, repo, runtime, captured)
	patchRequested := 0
	deps.PatchSource = func(context.Context, string, *string) (*schemas.DocPatch, error) {
		patchRequested++
		return nil, nil
	}

	result := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, genULID(t)))

	assert.Equal(t, cli.FixResultCompleted, result.Kind)
	assert.Empty(t, result.Applied)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "docs/api.md", result.Skipped[0].DocPath)
	assert.Equal(t, "unverified", result.Skipped[0].Reason)
	assert.Equal(t, 0, patchRequested)
	onDisk, err := os.ReadFile(filepath.Join(repo.root, "docs/api.md"))
	require.NoError(t, err)
	assert.Equal(t, "# API reference\n\nThe refund call captures the payment.\n", string(onDisk))
}

func TestFixFlowOutOfScopeAndLockedCandidatesBlockedWithIncidents(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	docs := []schemas.RunDocument{
		cliVerifiedDoc("src/payments/refund.ts", schemas.PipelineOutcomeAuto),
		cliVerifiedDoc("docs/locked.md", schemas.PipelineOutcomeAuto),
	}
	runtime := &cliCountingRuntime{outcomeFn: func(int) *schemas.RunOutcome { return cliFlowOutcome(docs) }}
	deps, _, incidents := cliFlowDeps(t, repo, runtime, captured)

	result := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, genULID(t)))

	assert.Equal(t, cli.FixResultCompleted, result.Kind)
	assert.Empty(t, result.Applied)
	reasons := map[string]string{}
	for _, entry := range result.Skipped {
		reasons[entry.DocPath] = entry.Reason
	}
	assert.Equal(t, "out-of-scope", reasons["src/payments/refund.ts"])
	assert.Equal(t, "locked-or-protected", reasons["docs/locked.md"])

	// incidents.jsonl — one incident per blocked lane.
	lines := incidents.lines(t)
	require.Len(t, lines, 2)
	rules := []string{lines[0]["rule"], lines[1]["rule"]}
	assert.ElementsMatch(t, []string{"out_of_allowlist_path", "protected_path_patch"}, rules)

	// Neither file was touched.
	onDisk, err := os.ReadFile(filepath.Join(repo.root, "docs/api.md"))
	require.NoError(t, err)
	assert.Equal(t, "# API reference\n\nThe refund call captures the payment.\n", string(onDisk))
}

func TestFixFlowDirtyDocSkippedWithoutAllowDirtyAppliedWithIt(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	base := "# API reference\n\nThe refund call captures the payment.\n"
	dirty := "# API reference\n\nThe refund call captures the payment (draft).\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo.root, "docs/api.md"), []byte(dirty), 0o644))
	captured := captureIo()
	runtime := &cliCountingRuntime{
		outcomeFn: func(int) *schemas.RunOutcome {
			return cliFlowOutcome([]schemas.RunDocument{cliVerifiedDoc("docs/api.md", schemas.PipelineOutcomeAuto)})
		},
	}
	deps, _, _ := cliFlowDeps(t, repo, runtime, captured)

	// Without allowDirty: skipped 'dirty', file untouched.
	skippedResult := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, genULID(t)))
	assert.Equal(t, cli.FixResultCompleted, skippedResult.Kind)
	require.Len(t, skippedResult.Skipped, 1)
	assert.Equal(t, "docs/api.md", skippedResult.Skipped[0].DocPath)
	assert.Equal(t, "dirty", skippedResult.Skipped[0].Reason)
	assert.Empty(t, skippedResult.Applied)
	onDisk, err := os.ReadFile(filepath.Join(repo.root, "docs/api.md"))
	require.NoError(t, err)
	assert.Equal(t, dirty, string(onDisk))

	// With allowDirty: the worktree content is current — the patch applies.
	deps.PatchSource = func(context.Context, string, *string) (*schemas.DocPatch, error) {
		return cliMakePatch("docs/api.md", dirty, false), nil
	}
	deps.Apply = cliApplyFn(cli.FixApplyOutcome{
		Kind:          "applied",
		ResultContent: strings.Replace(dirty, "The refund call captures the payment (draft).", cliApiInsert, 1),
		ThreeWayUsed:  false,
	})
	input := cliFlowInput(repo.root, genULID(t))
	input.AllowDirty = true
	appliedResult := cli.RunFixFlow(context.Background(), deps, input)
	assert.Equal(t, cli.FixResultCompleted, appliedResult.Kind)
	require.Len(t, appliedResult.Applied, 1)
	onDisk, err = os.ReadFile(filepath.Join(repo.root, "docs/api.md"))
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(dirty, "The refund call captures the payment (draft).", cliApiInsert, 1), string(onDisk))
	_ = base
}

func TestFixFlowStalePatchReDetectsExactlyOnceAndNeverForceApplies(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	runtime := &cliCountingRuntime{
		outcomeFn: func(int) *schemas.RunOutcome {
			return cliFlowOutcome([]schemas.RunDocument{cliVerifiedDoc("docs/api.md", schemas.PipelineOutcomeAuto)})
		},
	}
	deps, _, _ := cliFlowDeps(t, repo, runtime, captured)
	stalePatch := cliMakePatch("docs/api.md", "# API reference\n\nThe refund call captures the payment.\n", true)
	deps.PatchSource = func(context.Context, string, *string) (*schemas.DocPatch, error) {
		return stalePatch, nil
	}
	// reverify intentionally ABSENT → the race abstains (fail closed).
	deps.Apply = cliApplyFn(cli.FixApplyOutcome{
		Kind:          "abstain",
		AbstainReason: string(patch.AbstainDocChangedRaceUnverified),
		Detail:        "patch engine abstained doc-changed-race-unverified",
	})

	result := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, genULID(t)))

	assert.Equal(t, cli.FixResultCompleted, result.Kind)
	// Initial run + EXACTLY ONE re-detection.
	assert.Equal(t, 2, runtime.callCount())
	assert.Empty(t, result.Applied)
	require.Len(t, result.Refused, 1)
	assert.Equal(t, "docs/api.md", result.Refused[0].DocPath)
	assert.Equal(t, "stale-patch", result.Refused[0].Reason)
	assert.True(t, result.Refused[0].ReDetected)
	onDisk, err := os.ReadFile(filepath.Join(repo.root, "docs/api.md"))
	require.NoError(t, err)
	assert.Equal(t, "# API reference\n\nThe refund call captures the payment.\n", string(onDisk))
}

func TestFixFlowAppliedWritesResultContentAndPersistsRecord(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	runtime := &cliCountingRuntime{
		outcomeFn: func(int) *schemas.RunOutcome {
			return cliFlowOutcome([]schemas.RunDocument{cliVerifiedDoc("docs/api.md", schemas.PipelineOutcomeSuggest)})
		},
	}
	deps, store, _ := cliFlowDeps(t, repo, runtime, captured)
	docContent := "# API reference\n\nThe refund call captures the payment.\n"
	docPatch := cliMakePatch("docs/api.md", docContent, false)
	patchRef, err := verifier.ComputePatchRef(docPatch)
	require.NoError(t, err)
	deps.PatchSource = func(context.Context, string, *string) (*schemas.DocPatch, error) {
		return docPatch, nil
	}
	deps.Apply = cliApplyFn(cli.FixApplyOutcome{
		Kind:          "applied",
		ResultContent: strings.Replace(docContent, "The refund call captures the payment.", cliApiInsert, 1),
		ThreeWayUsed:  false,
	})
	runID := genULID(t)

	result := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, runID))

	assert.Equal(t, cli.FixResultCompleted, result.Kind)
	require.Len(t, result.Applied, 1)
	assert.Equal(t, cli.FixFlowApplied{DocPath: "docs/api.md", PatchRef: patchRef, ThreeWayUsed: false}, result.Applied[0])
	assert.Regexp(t, regexp.MustCompile(`^state/docs/.+#patch-[0-9a-f]{16}$`), result.Applied[0].PatchRef)
	onDisk, err := os.ReadFile(filepath.Join(repo.root, "docs/api.md"))
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(docContent, "The refund call captures the payment.", cliApiInsert, 1), string(onDisk))

	// The fix-run record is persisted under the state dir (provenance).
	assert.True(t, store.Has(runID))
	assert.True(t, store.HasPatchRef(patchRef))

	// A re-run of the same run id is the A9 same-run no-op.
	rerun := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, runID))
	assert.Equal(t, cli.FixResultNoop, rerun.Kind)
	assert.Equal(t, "same-run-skip", rerun.Reason)
}

func TestFixFlowNeverLeaksPlantedKeyThroughCapturedIo(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	runtime := &cliCountingRuntime{
		outcomeFn: func(int) *schemas.RunOutcome {
			return cliFlowOutcome([]schemas.RunDocument{cliVerifiedDoc("docs/api.md", schemas.PipelineOutcomeAuto)})
		},
	}
	deps, _, _ := cliFlowDeps(t, repo, runtime, captured)
	docContent := "# API reference\n\nThe refund call captures the payment.\n"
	deps.PatchSource = func(context.Context, string, *string) (*schemas.DocPatch, error) {
		return cliMakePatch("docs/api.md", docContent, false), nil
	}
	deps.Apply = cliApplyFn(cli.FixApplyOutcome{
		Kind:          "applied",
		ResultContent: strings.Replace(docContent, "The refund call captures the payment.", cliApiInsert, 1),
	})

	_ = cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, genULID(t)))

	rendered := captured.Out() + "\n" + captured.Err()
	assert.NotContains(t, rendered, plantedKey)
}

func TestFixFlowConfigErrorLane(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	failing := &cliConfigErrorRuntime{}
	deps, _, _ := cliFlowDeps(t, repo, failing, captured)

	result := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, genULID(t)))

	assert.Equal(t, cli.FixResultConfigError, result.Kind)
}

// cliFixRunPipelineSpy fails the test if the pipeline is ever invoked (the TS
// failing-pipeline spy).
type cliFixRunPipelineSpy struct{ calls int }

func (s *cliFixRunPipelineSpy) Run(context.Context, cli.CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	s.calls++
	return nil, assertAnError("pipeline must NEVER run for a same-run skip")
}

func (s *cliFixRunPipelineSpy) Advisories() []string { return nil }

// cliConfigErrorRuntime throws the CliConfigError-style failure (the TS
// error.name = 'CliConfigError' lane).
type cliConfigErrorRuntime struct{}

func (cliConfigErrorRuntime) Run(context.Context, cli.CliRuntimeRunInput) (*schemas.RunOutcome, error) {
	return nil, orchestrator.NewPipelineConfigError("no policy version pinned", "", nil)
}

func (cliConfigErrorRuntime) Advisories() []string { return nil }

func assertAnError(msg string) error { return &stringError{msg} }
