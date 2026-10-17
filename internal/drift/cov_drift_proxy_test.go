package drift_test

// Coverage for the D14 git-failure propagation branches of AnalyzeDrift and
// the sweep: a proxy GitExecutor (V-P1-10 seam) delegates real git runs but
// returns a scripted, non-git EXECUTOR error for one precise invocation. The
// client treats an executor error exactly like a broken git run — a typed
// *gitx.GitExecError for run failures, and the raw error for seam failures —
// and drift must propagate it (never swallow it into a verdict).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/drift"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// failingExecutor runs git normally except for invocations matched by fail.
type failingExecutor struct {
	inner gitx.GitExecutor
	fail  func(args []string) bool
}

func (f *failingExecutor) Run(ctx context.Context, args []string, opts *gitx.GitRunOptions) (gitx.GitRunResult, error) {
	if f.fail(args) {
		return gitx.GitRunResult{}, errors.New("scripted executor failure")
	}
	return f.inner.Run(ctx, args, opts)
}

// driftArgvSubcommand extracts the git subcommand from the spliced argv. The
// layout is `-C <root> -c core.quotepath=false ... <subcommand> <argv>` with a
// variable-length set of `-c key=value` diff determinism flags before the
// subcommand, so the first plain token (non-flag, non-assignment, non-path)
// is the subcommand.
func driftArgvSubcommand(args []string) string {
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") || strings.Contains(a, "=") || strings.HasPrefix(a, "/") {
			continue
		}
		return a
	}
	return ""
}

func driftClientWithFail(t *testing.T, root string, fail func(args []string) bool) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{
		RepoRoot: root,
		Executor: &failingExecutor{inner: gitx.CreateSystemGitExecutor(nil), fail: fail},
	})
	require.NoError(t, err)
	return client
}

// TestCOV_DRIFT_AnalyzeDriftLadderErrorPropagates: a failure inside the D12
// ladder (the merge-base call feeding the ancestor rung) is a typed git
// failure propagated from AnalyzeDrift — never a report without ladder
// context.
func TestCOV_DRIFT_AnalyzeDriftLadderErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	client := driftClientWithFail(t, fixture.root, func(args []string) bool {
		return driftArgvSubcommand(args) == "merge-base"
	})
	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &fixture.baseline,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_AnalyzeDriftDiffFilesErrorPropagates: the raw-status diff run
// failing surfaces the error (D14); no partial report.
func TestCOV_DRIFT_AnalyzeDriftDiffFilesErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	client := driftClientWithFail(t, fixture.root, func(args []string) bool {
		return driftArgvSubcommand(args) == "diff" && sliceContains(args, "--raw")
	})
	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &fixture.baseline,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_AnalyzeDriftDiffStatErrorPropagates: the numstat run failing
// surfaces the error.
func TestCOV_DRIFT_AnalyzeDriftDiffStatErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	client := driftClientWithFail(t, fixture.root, func(args []string) bool {
		return driftArgvSubcommand(args) == "diff" && sliceContains(args, "--numstat")
	})
	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &fixture.baseline,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.Error(t, err)
}

// ctxMatch builds the rev:path matcher (CatFile's inner rev-parse).
func ctxMatch(revOrPath string) func(args []string) bool {
	return func(args []string) bool {
		if driftArgvSubcommand(args) != "rev-parse" {
			return false
		}
		for _, a := range args {
			if strings.Contains(a, revOrPath) {
				return true
			}
		}
		return false
	}
}

// TestCOV_DRIFT_SymbolBaselineBlobErrorPropagates: reading the BASELINE blob
// of a changed recorded source fails → the symbols loop propagates (D14).
func TestCOV_DRIFT_SymbolBaselineBlobErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	// Only the baseline-side sha:path read fails (head-side and everything
	// else runs normally).
	client := driftClientWithFail(t, fixture.root, ctxMatch(fixture.baselineSha+":"))
	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &fixture.baseline,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_SymbolHeadBlobErrorPropagates: the HEAD-side blob read in the
// symbols loop fails (baseline read succeeded) → propagates.
func TestCOV_DRIFT_SymbolHeadBlobErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	client := driftClientWithFail(t, fixture.root, func(args []string) bool {
		if driftArgvSubcommand(args) != "rev-parse" {
			return false
		}
		for _, a := range args {
			if strings.Contains(a, "refund.ts") && strings.HasPrefix(a, fixture.headSha+":") {
				return true
			}
		}
		return false
	})
	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &fixture.baseline,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_ClaimHeadBlobErrorPropagates: no recorded symbols, so the
// first blob read is the claim loop's head read → propagates.
func TestCOV_DRIFT_ClaimHeadBlobErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	baseline := fixture.baseline
	baseline.SourceSymbols = []string{}
	client := driftClientWithFail(t, fixture.root, ctxMatch(fixture.headSha+":"))
	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &baseline,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   fixture.claims,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.Error(t, err)
}

/* ------------------------------------------------------------ sweep paths */

// TestCOV_DRIFT_SweepDocReadErrorPropagates: the anchor doc cannot be read at
// head (D14) → the sweep propagates the git failure rather than guessing an
// unanchored verdict.
func TestCOV_DRIFT_SweepDocReadErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	client := driftClientWithFail(t, fixture.root, ctxMatch(docPath))
	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: fixture.claims,
		Git:    client,
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_SweepSourceReadError_Anchored: the anchor resolves but the
// source blob read fails → propagates.
func TestCOV_DRIFT_SweepSourceReadError_Anchored(t *testing.T) {
	fixture := createDriftFixture(t)
	client := driftClientWithFail(t, fixture.root, ctxMatch(refundPath))
	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: fixture.claims,
		Git:    client,
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_SweepSourceReadError_MissingDoc: the anchor doc is absent at
// head (declared absence) but the source blob read fails → propagates.
func TestCOV_DRIFT_SweepSourceReadError_MissingDoc(t *testing.T) {
	fixture := createDriftFixture(t)
	claimMissingDoc := fixture.claims[0]
	claimMissingDoc.Anchor.File = "docs/never-existed.md"
	client := driftClientWithFail(t, fixture.root, ctxMatch(refundPath))
	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: []schemas.Claim{claimMissingDoc},
		Git:    client,
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_SweepSourceReadError_Unanchored: the anchor fails to resolve
// (nowhere match) and the subsequent source blob read fails → propagates.
func TestCOV_DRIFT_SweepSourceReadError_Unanchored(t *testing.T) {
	fixture := createDriftFixture(t)
	claimBrokenAnchor := fixture.claims[0]
	claimBrokenAnchor.Anchor = schemas.ClaimAnchor{
		File:        docPath,
		StartLine:   1,
		EndLine:     1,
		ContentHash: "sha256:not-in-the-doc",
	}
	client := driftClientWithFail(t, fixture.root, ctxMatch(refundPath))
	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: []schemas.Claim{claimBrokenAnchor},
		Git:    client,
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_SweepCommitWalkErrorPropagates: the per-path commit walk
// fails for a lost-support claim → propagates (D14), never omitted evidence.
func TestCOV_DRIFT_SweepCommitWalkErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	client := driftClientWithFail(t, fixture.root, func(args []string) bool {
		return driftArgvSubcommand(args) == "log"
	})
	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: fixture.claims,
		Git:    client,
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})
	require.Error(t, err)
}

// TestCOV_DRIFT_SweepPatchIdErrorPropagates: `git patch-id` failing during the
// commit walk propagates.
func TestCOV_DRIFT_SweepPatchIdErrorPropagates(t *testing.T) {
	fixture := createDriftFixture(t)
	client := driftClientWithFail(t, fixture.root, func(args []string) bool {
		return driftArgvSubcommand(args) == "patch-id"
	})
	_, err := drift.RunClaimSupportSweep(t.Context(), drift.ClaimSupportSweepInput{
		Claims: fixture.claims,
		Git:    client,
		Head:   fixture.headSha,
		RunID:  driftRunID,
		At:     driftAt,
	})
	require.Error(t, err)
}

func sliceContains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
