package cli_test

// Ported from packages/cli/test/fix-composed.test.ts (V-P7-6, V-P7-7,
// V-P7-9, V-P7-10, V-P7-15) — the fix verb end-to-end over the REAL composed
// pipeline (decision chain, patch-writer, judge, policy engine) and the REAL
// patch engine (`applyDocPatchReal`): applied patches write a minimal
// working-tree diff; the provenance record lands and the same run id is the
// A9 no-op that never invokes the pipeline; reject verdicts, dirty docs and
// locked targets never write; the --doc selector lane is out-of-scope; and
// the planted key never reaches any output stream.
//
// Replay-order honesty (AGENT-behavior replay rule): bare-response fixtures
// are consumed positionally; the writer pool is capped at maxConcurrentDocs
// 1 (the TS SEQUENTIAL_POOL) so the FIFO script is consumed in a
// deterministic order, exactly as the TS suite documented.
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cliFixRunKey   = "sk-or-v1-test-run-key-9f2c"
	cliFixApiDoc   = "docs/api.md"
	cliFixLocked   = "docs/locked.md"
	cliFixApiLine3 = "The refund call captures the payment."
	cliFixLockLine = "The refund ledger is append-only."
)

// cliFixComposedRepo is the composed fix fixture: TS-shaped docs, the
// 4-role models binding, and a behavioral source change (the `amountMinor`
// token rides the changed source so the insert claims anchor).
func cliFixComposedRepo(t *testing.T) (*gitFixture, string) {
	t.Helper()
	repo := newGitFixture(t)
	repo.seedBase()
	repo.write(".docsync/models.yml", cliComposedModels)
	repo.write("docs/api.md", cliComposedApiDoc)
	repo.write("docs/locked.md", cliComposedLockedDoc)
	repo.commitPaths([]string{".docsync/models.yml", "docs/api.md", "docs/locked.md"},
		"chore: composed model bindings and doc bodies")
	repo.write("src/payments/refund.ts",
		"export interface RefundInput {\n  amountMinor: number;\n  idempotencyKey?: string;\n}\n\n"+
			"export function refund(input: RefundInput): void {\n  void input;\n}\n")
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	repo.commitPaths([]string{"src/payments/refund.ts"}, "feat: refund idempotency key optional")
	return repo, base
}

func cliFixComposedArgs(t *testing.T, extra ...string) *cli.ParsedInvocation {
	t.Helper()
	argv := append([]string{"fix", "--no-quiet", "--repo", cliComposedRepo}, extra...)
	return cliArgs(t, argv...)
}

func TestFixComposedAppliedWritesMinimalDiffAndSecondRunIsNoop(t *testing.T) {
	repo, base := cliFixComposedRepo(t)

	/* Run 1: apply the verified patch to docs/api.md (locked blocked). */
	first := captureIo()
	exit1 := cli.RunFix(cliFixComposedArgs(t, "--base", base), first, cli.FixOptions{
		Cwd:       repo.root,
		Env:       map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateReplayTransport(cliDriftReplay("pass")).Transport,
		Budgets:   cliSequentialBudgets(),
	})

	assert.Equal(t, cli.ExitDrift, exit1)
	patched := cliReadDoc(t, repo.root, "docs/api.md")
	assert.Equal(t,
		strings.Replace(cliComposedApiDoc, cliFixApiLine3, cliComposedApiInsert, 1), patched)
	assert.Contains(t, first.Out(), "✓ applied docs/api.md (")

	// MINIMAL diff: exactly one line changed in exactly one doc.
	numstat := strings.TrimSpace(repo.git("diff", "--numstat"))
	assert.Equal(t, "1\t1\tdocs/api.md", numstat)

	/* The human commits the fix as their own work. */
	repo.commitPaths([]string{"docs/api.md"}, "docs: refund idempotency key optional")

	/* Run 2 (fresh run id): the committed tree has NO candidates → exit 0. */
	second := captureIo()
	exit2 := cli.RunFix(cliFixComposedArgs(t, "--base", base), second, cli.FixOptions{
		Cwd: repo.root,
		Env: map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateReplayTransport([]openrouter.ReplayFixture{
			cliBareCompletion(cliComposedDecisionModel, cliIgnoreDecisionBody()),
		}).Transport,
		Budgets: cliSequentialBudgets(),
	})

	assert.Equal(t, cli.ExitOK, exit2)
	assert.NotContains(t, second.Out(), "✓ applied")
	assert.Empty(t, second.Err())
	// The doc was not touched again.
	assert.Equal(t, patched, cliReadDoc(t, repo.root, "docs/api.md"))
	// P8-T24 secrets-absence: nothing under .docsync/ carries a credential.
	cliAssertNoSecretsUnderDocsync(t, repo.root, plantedKey, cliFixRunKey)
}

func TestFixComposedProvenanceRecordAndSameRunSkipSkipsPipeline(t *testing.T) {
	repo, base := cliFixComposedRepo(t)
	runID := genULID(t)

	first := captureIo()
	exit1 := cli.RunFix(cliFixComposedArgs(t, "--base", base, "--run-id", runID), first, cli.FixOptions{
		Cwd:       repo.root,
		Env:       map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateReplayTransport(cliDriftReplay("pass")).Transport,
		Budgets:   cliSequentialBudgets(),
	})
	assert.Equal(t, cli.ExitDrift, exit1)

	/* The provenance record exists (schema-valid; run id + applied row). */
	recordData, err := os.ReadFile(filepath.Join(repo.root, ".docsync", "state", "fix-run-records.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(recordData), runID)
	assert.Contains(t, string(recordData), cliFixApiDoc)

	/* Same run-id again: A9 same-run skip with a FAILING transport — any
	 * pipeline invocation would throw (the transport is never reached). */
	second := captureIo()
	exit2 := cli.RunFix(cliFixComposedArgs(t, "--base", base, "--run-id", runID), second, cli.FixOptions{
		Cwd: repo.root,
		Env: map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateFailingTransport([]openrouter.TransportFailure{{
			Kind: openrouter.TransportFailureNetwork, Message: "pipeline must not run",
		}}).Transport,
		Budgets: cliSequentialBudgets(),
	})

	assert.Equal(t, cli.ExitOK, exit2)
	assert.Contains(t, second.Out(), "no-op: run-id "+runID+" already recorded")
}

func TestFixComposedJudgeRejectLeavesTheFileUntouched(t *testing.T) {
	repo, base := cliFixComposedRepo(t)
	captured := captureIo()

	exit := cli.RunFix(cliFixComposedArgs(t, "--base", base), captured, cli.FixOptions{
		Cwd:       repo.root,
		Env:       map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateReplayTransport(cliDriftReplay("reject")).Transport,
		Budgets:   cliSequentialBudgets(),
	})

	// A reject verdict is never applied: the policy downgrades suggest → review
	// and the flow skips it (advisory posture, exit 0).
	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "⏭ skipped docs/api.md (not-a-candidate")
	assert.Contains(t, captured.Out(), "verification gate downgraded 'suggest' to review")
	assert.Equal(t, cliComposedApiDoc, cliReadDoc(t, repo.root, "docs/api.md"))
}

func TestFixComposedDirtyDocSkippedUntouched(t *testing.T) {
	repo, base := cliFixComposedRepo(t)
	dirty := strings.Replace(cliComposedApiDoc, "captures the payment", "captures the payment (draft)", 1)
	require.NoError(t, os.WriteFile(filepath.Join(repo.root, "docs/api.md"), []byte(dirty), 0o644))
	captured := captureIo()

	exit := cli.RunFix(cliFixComposedArgs(t, "--base", base), captured, cli.FixOptions{
		Cwd:       repo.root,
		Env:       map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateReplayTransport(cliDriftReplay("pass")).Transport,
		Budgets:   cliSequentialBudgets(),
	})

	assert.Equal(t, cli.ExitOK, exit)
	assert.Contains(t, captured.Out(), "⏭ skipped docs/api.md (dirty")
	assert.Equal(t, dirty, cliReadDoc(t, repo.root, "docs/api.md"))
}

func TestFixComposedLockedBlockedWithIncidentAndDocSelectorOutOfScope(t *testing.T) {
	repo, base := cliFixComposedRepo(t)
	captured := captureIo()

	exit := cli.RunFix(cliFixComposedArgs(t, "--base", base), captured, cli.FixOptions{
		Cwd:       repo.root,
		Env:       map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
		Transport: openrouter.CreateReplayTransport(cliDriftReplay("pass")).Transport,
		Budgets:   cliSequentialBudgets(),
	})

	// api applied (exit 1); locked blocked at ANY confidence (D2 floor).
	assert.Equal(t, cli.ExitDrift, exit)
	assert.Contains(t, captured.Out(), "✓ applied docs/api.md (")
	assert.Contains(t, captured.Out(), "⏭ skipped docs/locked.md (locked-or-protected")
	assert.Equal(t, cliComposedLockedDoc, cliReadDoc(t, repo.root, "docs/locked.md"))

	// The write-scope violation is a recorded safety incident (protected_path_patch).
	incidents := &cliFileIncidents{path: filepath.Join(repo.root, ".docsync", "state", "incidents.jsonl")}
	lines := incidents.lines(t)
	require.Len(t, lines, 1)
	assert.Equal(t, "protected_path_patch", lines[0]["rule"])
	assert.Equal(t, "docs/locked.md", lines[0]["offending_path"])

	// Selector lane (command-level out-of-scope): a fresh run restricted to
	// the source prefix excludes BOTH doc candidates.
	selected := captureIo()
	exitSelected := cli.RunFix(cliFixComposedArgs(t, "--base", base, "--doc", "src/payments"),
		selected, cli.FixOptions{
			Cwd:       repo.root,
			Env:       map[string]string{"OPENROUTER_API_KEY": cliFixRunKey},
			Transport: openrouter.CreateReplayTransport(cliDriftReplay("pass")).Transport,
			Budgets:   cliSequentialBudgets(),
		})

	assert.Equal(t, cli.ExitOK, exitSelected)
	assert.Contains(t, selected.Out(), "⏭ skipped docs/api.md (out-of-scope")
	assert.Contains(t, selected.Out(), "⏭ skipped docs/locked.md (out-of-scope")
}

func TestFixComposedRedactionPlantedKeyNeverReachesOutput(t *testing.T) {
	repo, base := cliFixComposedRepo(t)
	captured := captureIo()

	_ = cli.RunFix(cliFixComposedArgs(t, "--base", base), captured, cli.FixOptions{
		Cwd: repo.root,
		// The planted key rides the env; nothing in the CLI ever renders env.
		Env:       map[string]string{"OPENROUTER_API_KEY": plantedKey},
		Transport: openrouter.CreateReplayTransport(cliDriftReplay("pass")).Transport,
		Budgets:   cliSequentialBudgets(),
	})

	everything := captured.Out() + "\n" + captured.Err()
	assert.NotContains(t, everything, plantedKey)
	assert.NotContains(t, everything, "sk-or-v1")
}

// cliIgnoreDecisionBody is the ignore-route decision (run 2 after the human
// commit: trivial → no candidates).
func cliIgnoreDecisionBody() map[string]any {
	return map[string]any{
		"change_kind":          "formatting_only",
		"documentation_impact": "none",
		"risk":                 "low",
		"confidence":           0.9,
		"answers": map[string]any{
			"public_behavior_changed": map[string]any{"value": false, "probability": 0.9},
			"api_contract_changed":    map[string]any{"value": false, "probability": 0.9},
			"affected_doc_count":      map[string]any{"value": 0, "probability": 0.9},
			"documentation_impact":    map[string]any{"value": "none", "probability": 0.9},
		},
		"candidates":     []string{},
		"action":         "ignore",
		"abstain":        false,
		"abstain_reason": nil,
		"evidence":       []map[string]any{},
	}
}

// cliAssertNoSecretsUnderDocsync is the P8-T24 secrets-absence scan: nothing
// under .docsync/ (state included) ever carries a credential.
func cliAssertNoSecretsUnderDocsync(t *testing.T, root string, secrets ...string) {
	t.Helper()
	err := filepath.Walk(filepath.Join(root, ".docsync"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, secret := range secrets {
			assert.NotContains(t, string(data), secret,
				"a credential leaked into "+path+" (§3 rule 8)")
		}
		return nil
	})
	require.NoError(t, err)
}
