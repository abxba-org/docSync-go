// Ported from core test suites (P0-T17; P1-T20): coverage tests targeting
// internal/testkit statements and branches not exercised by the existing
// testkit_test.go. Black-box, exported surface only, deterministic, no network.
package testkit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCOV_TESTKIT_GitPlatformHeadSHA(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))
	require.NoError(t, testkit.WriteRepoFile(root, "a.txt", "hello\n"))
	_, err := testkit.GitCommitAll(root, "initial")
	require.NoError(t, err)
	sha, err := testkit.GitHeadSHA(root)
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{40}$`, sha)
}

func TestCOV_TESTKIT_GitPlatformRemoveAndStateDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.WriteRepoFile(root, "a.txt", "hello\n"))
	require.NoError(t, testkit.RemoveRepoFile(root, "a.txt"))
	_, err := os.ReadFile(filepath.Join(root, "a.txt"))
	require.Error(t, err)

	pipeline, err := testkit.BuildPaymentsPipeline(root)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".docsync", "state"), pipeline.StateDir())

	require.NoError(t, pipeline.Write("b.txt", "b\n"))
	require.NoError(t, pipeline.Remove("b.txt"))
	_, err = os.ReadFile(filepath.Join(root, "b.txt"))
	require.Error(t, err)
}

func TestCOV_TESTKIT_MonorepoRunPipeline(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, testkit.InitGitRepo(root))
	pipeline, err := testkit.BuildMonorepoPipeline(root)
	require.NoError(t, err)
	commit, err := pipeline.CommitAll("chore: initial fixture")
	require.NoError(t, err)
	require.NoError(t, pipeline.SeedVerifiedDoc("docs/packages/a.md", commit))

	fakes := testkit.NewPipelineFakes()
	decision := schemas.DecisionExample
	decision.Action = schemas.ActionInvestigate
	decision.Candidates = []string{"docs/packages/a.md"}
	fakes.Decision.OnDefault(orchestrator.DecisionStageResult{Decision: &decision})
	fakes.Writer.OnDefault(orchestrator.WriterStageResult{Kind: orchestrator.WriterResultNoop, Detail: "noop"})

	runID := testkit.MonorepoRepoID + "#" + commit + ".." + commit
	outcome, err := pipeline.RunPipeline(context.Background(), fakes, commit, commit)
	require.NoError(t, err)
	require.NotNil(t, outcome)
	assert.Equal(t, runID, outcome.RunID)
}

func TestCOV_TESTKIT_StageFakeIdentitiesAndMethods(t *testing.T) {
	fakes := testkit.NewPipelineFakes()

	// DecisionStageFake: OnRun scripts by run id; DefaultDecisionStageIdentity.
	fakes.Decision.OnRun("run_1", orchestrator.DecisionStageResult{Abstained: false})
	got, err := fakes.Decision.Decide(orchestrator.DecisionStageInput{Ctx: orchestrator.RunContext{RunID: "run_1"}})
	require.NoError(t, err)
	assert.False(t, got.Abstained)
	assert.Equal(t, "run_1", testkit.DefaultDecisionStageIdentity(orchestrator.DecisionStageInput{Ctx: orchestrator.RunContext{RunID: "run_1"}}))
	_ = fakes.Decision.Identities()

	// VerificationStageFake: OnDoc + Dispatch via identity (doc path).
	fakes.Verification.OnDoc("docs/payments.md", orchestrator.VerificationStageResult{})
	gotV, err := fakes.Verification.Verify(orchestrator.VerificationStageInput{
		Patch: schemas.DocPatchExample,
	})
	require.NoError(t, err)
	_ = gotV
	assert.Equal(t, "docs/payments.md", testkit.DefaultVerificationStageIdentity(orchestrator.VerificationStageInput{
		Patch: schemas.DocPatchExample,
	}))

	// PolicyStageFake: OnDoc + Resolve + DefaultPolicyStageIdentity.
	fakes.Policy.OnDoc("docs/a.md", orchestrator.PolicyStageResult{Outcome: schemas.PipelineOutcomeIgnore})
	gotP, err := fakes.Policy.Resolve(orchestrator.PolicyStageInput{DocPath: "docs/a.md"})
	require.NoError(t, err)
	_ = gotP
	assert.Equal(t, "docs/a.md", testkit.DefaultPolicyStageIdentity(orchestrator.PolicyStageInput{DocPath: "docs/a.md"}))
}
