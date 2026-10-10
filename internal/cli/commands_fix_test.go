package cli_test

import (
	"context"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
)

func TestRenderFixSummaryLines(t *testing.T) {
	result := cli.FixFlowResult{
		Kind:    cli.FixResultCompleted,
		Applied: []cli.FixFlowApplied{{DocPath: "docs/api.md", PatchRef: "sha256:abc12345"}},
		Skipped: []cli.FixFlowSkipped{{DocPath: "docs/x.md", Reason: "not-a-candidate", Detail: "policy_outcome 'review'"}},
		Refused: []cli.FixFlowRefused{{DocPath: "docs/y.md", Reason: "unanchored", Detail: "anchors did not resolve content-hash-first", ReDetected: true}},
	}
	lines := cli.RenderFixSummary(result, cli.GlyphsUnicode)
	requireLen(t, len(lines), 3)
	assert.Contains(t, lines[0], "✓ applied docs/api.md")
	assert.Contains(t, lines[0], "sha256:abc12345")
	assert.Contains(t, lines[1], "⏭ skipped docs/x.md")
	assert.Contains(t, lines[1], "not-a-candidate")
	assert.Contains(t, lines[2], "⚠ refused docs/y.md")
	assert.Contains(t, lines[2], "re-detected: yes")
}

func TestRenderFixSummaryEmpty(t *testing.T) {
	assert.Empty(t, cli.RenderFixSummary(cli.FixFlowResult{Kind: cli.FixResultCompleted}, cli.GlyphsFallback))
}

func fixAutoOutcome(docPath string) *schemas.RunOutcome {
	confidence := 0.95
	return &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID,
		RunID:  "run#a..b",
		Key:    schemas.RunKey{Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status: schemas.RunStatusCompleted,
		Stages: []schemas.PipelineStage{{Stage: "analyze", Status: schemas.RunStageOk}},
		Documents: []schemas.RunDocument{{
			DocPath:       docPath,
			PolicyOutcome: schemas.PipelineOutcomeAuto,
			Confidence:    &confidence,
			Verification: &schemas.RunDocumentVerification{
				Verdict:      schemas.VerdictPass,
				AutoEligible: true,
			},
		}},
		Provenance:  schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
		EarlySignal: schemas.EarlySignal{},
		StateChurn:  schemas.StateChurn{},
	}
}

func fakeApply(kind, resultContent string) cli.FixApplyFn {
	return func(context.Context, *schemas.DocPatch, *string, map[string]bool, map[string]bool, func(string, string) bool) (cli.FixApplyOutcome, error) {
		return cli.FixApplyOutcome{Kind: kind, ResultContent: resultContent, ThreeWayUsed: kind == "applied"}, nil
	}
}

func alwaysPatch(_ context.Context, _ string, _ *string) (*schemas.DocPatch, error) {
	return &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "run",
		DocPath:         "docs/api.md",
		BaseContentHash: "sha256:0000",
		Hunks:           []schemas.PatchHunk{},
	}, nil
}

func TestFixSameRunNoopExits0(t *testing.T) {
	runID := genULID(t)
	store := cli.NewFixRunRecordStore(t.TempDir())
	require1(t, store.Write(cli.FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: runID}))

	captured := captureIo()
	args := cliArgs(t, "fix", "--run-id", runID)
	exit := cli.RunFix(args, captured, cli.FixOptions{
		Cwd:      t.TempDir(),
		FlowDeps: &cli.FixFlowDeps{Runtime: &fakeRuntime{}, Store: store, Apply: fakeApply("applied", "x")},
	})
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "no-op:")
}

func TestFixConfigErrorExits2(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "fix")
	exit := cli.RunFix(args, captured, cli.FixOptions{
		Cwd:      t.TempDir(), // no .docsync/ → config error
		FlowDeps: &cli.FixFlowDeps{},
	})
	assert.Equal(t, cli.ExitConfig, exit)
}

func TestFixRefusedCandidateExits1(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	args := cliArgs(t, "fix")
	exit := cli.RunFix(args, captured, cli.FixOptions{
		Cwd: repo.root,
		FlowDeps: &cli.FixFlowDeps{
			Runtime:     &fakeRuntime{outcome: fixAutoOutcome("docs/api.md")},
			Store:       cli.NewFixRunRecordStore(repo.root + "/.docsync/state"),
			Apply:       fakeApply("unanchored", ""),
			PatchSource: alwaysPatch,
		},
	})
	assert.Equal(t, cli.ExitDrift, exit)
	assert.Contains(t, captured.Out(), "refused docs/api.md")
}

func TestFixAppliedWritesAndExits1(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	captured := captureIo()
	args := cliArgs(t, "fix")
	exit := cli.RunFix(args, captured, cli.FixOptions{
		Cwd: repo.root,
		FlowDeps: &cli.FixFlowDeps{
			Runtime:     &fakeRuntime{outcome: fixAutoOutcome("docs/api.md")},
			Store:       cli.NewFixRunRecordStore(repo.root + "/.docsync/state"),
			Apply:       fakeApply("applied", "patched body\n"),
			PatchSource: alwaysPatch,
		},
	})
	assert.Equal(t, cli.ExitDrift, exit)
	assert.Contains(t, captured.Out(), "docs/api.md")
}

func TestFixOfflineRefused(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "fix", "--offline")
	exit := cli.RunFix(args, captured, cli.FixOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "--offline")
}

func TestFixInvalidRunId(t *testing.T) {
	captured := captureIo()
	args := cliArgs(t, "fix", "--run-id", "not-a-ulid")
	exit := cli.RunFix(args, captured, cli.FixOptions{Cwd: t.TempDir()})
	assert.Equal(t, cli.ExitConfig, exit)
	assert.Contains(t, captured.Err(), "--run-id")
}

/* helpers */

func genULID(t *testing.T) string {
	t.Helper()
	id, err := docutil.NewULIDGenerator().Generate()
	require1(t, err)
	return id
}

func require1(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func requireLen(t *testing.T, n, want int) {
	t.Helper()
	if n != want {
		t.Fatalf("expected %d lines, got %d", want, n)
	}
}
