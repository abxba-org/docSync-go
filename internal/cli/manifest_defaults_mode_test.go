package cli_test

// Ported from packages/cli/test/manifest-defaults-mode-p12-t16.test.ts
// (F5-2; INV-4 defense in depth) — the write-path flows must resolve per-doc
// modes through `manifest.defaults.mode` (`entry.mode → defaults.mode →
// suggest`), never a hard-coded 'auto': with `defaults: {mode: locked}` and
// mode-less entries, the protected docs are skipped-and-reported, never
// collected as writable.
//
// Ported here: the FIX flow half (the mode-less doc inherits
// defaults.mode: locked — skipped + incident, never applied — while an entry
// with an EXPLICIT mode still resolves through the entry layer).
//
// SKIPPED: the TS pr-flow half (the mode-less doc lands in protectedPaths and
// the shipped PR never touches it). The Go CLI's pr flow is a DECLARED SEAM
// (PrFlowRunnerImpl is nil until the doc-pr-flow rebind) and the Go fix-flow
// style protectedPaths derivation lives in internal/gitpr (pr-filter.go,
// covered by internal/gitpr tests) — there is no CLI-level surface to
// exercise it through, so the lane is pinned at the fix flow instead.
import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readFileString reads one fixture file (the TS readFile helper).
func readFileString(root, relPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, relPath))
	return string(data), err
}

// cliDefaultsLockedManifest: docs/api.md keeps its explicit mode;
// docs/locked.md is mode-less and must inherit defaults.mode: locked.
const cliDefaultsLockedManifest = `version: 1
defaults:
  mode: locked
documents:
  - path: docs/api.md
    mode: suggest
    sources:
      - "src/payments/**"
  - path: docs/locked.md
    sources:
      - "src/payments/**"
`

func TestManifestDefaultsModeFixFlowResolvesThroughDefaults(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	repo.write(".docsync/manifest.yml", cliDefaultsLockedManifest)
	repo.commitPaths([]string{".docsync/manifest.yml"}, "chore: manifest defaults mode locked")

	captured := captureIo()
	runtime := &cliCountingRuntime{outcomeFn: func(int) *schemas.RunOutcome {
		return cliFlowOutcome([]schemas.RunDocument{
			cliVerifiedDoc("docs/api.md", schemas.PipelineOutcomeAuto),
			cliVerifiedDoc("docs/locked.md", schemas.PipelineOutcomeAuto),
		})
	}}
	deps, store, incidents := cliFlowDeps(t, repo, runtime, captured)
	apiContent := "# API reference\n\nThe refund call captures the payment.\n"
	lockedContent := "# Operations runbook (locked)\n\nThe refund ledger is append-only.\n"
	apiPatch := cliMakePatch("docs/api.md", apiContent, false)
	patchRef, err := verifier.ComputePatchRef(apiPatch)
	require.NoError(t, err)
	deps.PatchSource = func(_ context.Context, docPath string, _ *string) (*schemas.DocPatch, error) {
		if docPath == "docs/api.md" {
			return apiPatch, nil
		}
		return cliMakePatch(docPath, lockedContent, false), nil
	}
	deps.Apply = cliApplyFn(cli.FixApplyOutcome{
		Kind:          "applied",
		ResultContent: strings.Replace(apiContent, "The refund call captures the payment.", cliApiInsert, 1),
	})
	runID := genULID(t)

	result := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, runID))

	assert.Equal(t, cli.FixResultCompleted, result.Kind)
	// The mode-less doc resolves to locked via defaults → skip-and-report.
	require.Len(t, result.Applied, 1)
	assert.Equal(t, "docs/api.md", result.Applied[0].DocPath)
	assert.Equal(t, patchRef, result.Applied[0].PatchRef)
	reasons := map[string]string{}
	for _, entry := range result.Skipped {
		reasons[entry.DocPath] = entry.Reason
	}
	assert.Equal(t, "locked-or-protected", reasons["docs/locked.md"])

	// On disk: only the explicitly-suggest doc was written.
	onDiskAPI, err := readFileString(repo.root, "docs/api.md")
	require.NoError(t, err)
	assert.Contains(t, onDiskAPI, cliApiInsert)
	onDiskLocked, err := readFileString(repo.root, "docs/locked.md")
	require.NoError(t, err)
	assert.Equal(t, lockedContent, onDiskLocked)

	// The skip is a recorded safety incident (protected_path_patch, D2 floor).
	lines := incidents.lines(t)
	require.Len(t, lines, 1)
	assert.Equal(t, "protected_path_patch", lines[0]["rule"])
	assert.Equal(t, "docs/locked.md", lines[0]["offending_path"])

	// The fix-run record carries the applied row (Go parity: the record
	// persists the applied rows; the skip rows are surfaced in the flow
	// result above — the TS record's skipped rows have no Go counterpart).
	assert.True(t, store.Has(runID))
	assert.True(t, store.HasPatchRef(patchRef))
}
