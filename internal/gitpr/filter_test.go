package gitpr_test

// Black-box port of packages/core/test/gitpr-filter.test.ts (P6-T10 + P6-T11):
// per-document partial-pass PR assembly (D4 drop-and-report) and the PR-side
// safety filter (lock enforcement layer 4 — INV-2/3 manifest-derived allowlist
// re-diff, INV-4 whole-patch rejection, OD-R12 state carriage).
//
// The TS suite drove REAL system git in fresh `os.tmpdir()` repos with a pinned
// env and local identity (the gitpr.test.ts fixture pattern; fixture commit
// dates pinned, no clocks in the units under test) — this port keeps that
// fixture (a real local git subprocess, no network) because the filter's own
// seam is the injected `gitx.GitClient`, which speaks the raw `git diff --raw
// -z` dialect the production code parses; a recorded executor double would
// re-encode that dialect instead of exercising it. The platform side of the
// write path stays fully faked (InMemoryPrPlatform lives in client_test.go).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gpFilterDocBranch is the doc branch about to become the PR (TS DOC_BRANCH).
const gpFilterDocBranch = "docsync/pr-481-abc1234"

// gpFilterEnv is the fixed identity + dates + config isolation for FIXTURE git
// ops (TS GIT_ENV): pinned dates and no user/system config.
func gpFilterEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"TERM=dumb",
	}
}

// gpGitRepo is a throwaway local repository (TS TestGitRepo). t.TempDir()
// provides the fresh root and its cleanup (the TS afterEach rim).
type gpGitRepo struct {
	root string
}

func (r *gpGitRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	cmd.Env = gpFilterEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return string(out)
}

func (r *gpGitRepo) write(t *testing.T, relPath, content string) {
	t.Helper()
	target := filepath.Join(r.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func (r *gpGitRepo) commitAll(t *testing.T, message string) string {
	t.Helper()
	r.git(t, "add", "-A")
	r.git(t, "commit", "-q", "-m", message)
	return strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
}

func (r *gpGitRepo) client(t *testing.T) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: r.root})
	require.NoError(t, err)
	return client
}

// gpNewFilterRepo inits the fixture repo with a repo-local line-ending policy
// pinned LF before any file is written (the DX-P11-3 port: a machine-level
// core.autocrlf=true would rewrite fresh files to CRLF and surface them as
// modified in the worktree).
func gpNewFilterRepo(t *testing.T) *gpGitRepo {
	t.Helper()
	repo := &gpGitRepo{root: t.TempDir()}
	repo.git(t, "init", "-b", "main")
	repo.git(t, "config", "core.autocrlf", "false")
	repo.git(t, "config", "core.eol", "lf")
	repo.git(t, "config", "user.name", "docsync-test")
	repo.git(t, "config", "user.email", "docsync-test@invalid")
	return repo
}

// gpRepoWithBranch commits the base files on `main`, then the given writes on
// the doc branch — the diff under test is main..docsync branch (TS
// repoWithBranch).
func gpRepoWithBranch(t *testing.T, baseFiles, branchFiles [][2]string) *gitx.GitClient {
	t.Helper()
	repo := gpNewFilterRepo(t)
	for _, file := range baseFiles {
		repo.write(t, file[0], file[1])
	}
	repo.commitAll(t, "base: docs + code")
	repo.git(t, "checkout", "-q", "-b", gpFilterDocBranch)
	for _, file := range branchFiles {
		repo.write(t, file[0], file[1])
	}
	repo.commitAll(t, "docsync: candidate doc PR head")
	return repo.client(t)
}

// gpModePtr is the optional-mode pointer helper.
func gpModePtr(mode schemas.DocMode) *schemas.DocMode { return &mode }

// gpPaymentsOnlyManifest is the TS PAYMENTS_ONLY_MANIFEST.
func gpPaymentsOnlyManifest() *schemas.Manifest {
	return &schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/payments.md", Mode: gpModePtr(schemas.DocModeAuto), Sources: []string{"src/payments/**"}},
		},
	}
}

// gpFilterReport is the TS reportFor: a verification-report fixture for one doc
// path (evidence shape, verdict pass).
func gpFilterReport(docPath, patchRef string) *schemas.VerificationReport {
	return &schemas.VerificationReport{
		Schema:        "docsync.verification-report/v1",
		RunID:         "run-under-test",
		DocPath:       docPath,
		PatchRef:      patchRef,
		Verdict:       schemas.VerdictPass,
		AutoEligible:  true,
		PolicyVersion: 17,
	}
}

// gpSortedKeys renders a path set as its sorted slice (the TS
// `[...set].sort()`).
func gpSortedKeys(paths map[string]bool) []string {
	keys := make([]string, 0, len(paths))
	for path := range paths {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	return keys
}

// gpFilterBranchDiff runs the layer-4 filter over main..doc branch against the
// manifest (the TS filterBranchDiff call shape).
func gpFilterBranchDiff(t *testing.T, git *gitx.GitClient, manifest *schemas.Manifest) gitpr.PrFilterResult {
	t.Helper()
	filter, err := gitpr.CreatePrFilter(gitpr.PrFilterDeps{Git: git})
	require.NoError(t, err)
	result, err := filter.FilterBranchDiff(context.Background(), gitpr.FilterBranchDiffInput{
		RepoRoot: git.Root(),
		Base:     "main",
		Branch:   gpFilterDocBranch,
		Manifest: manifest,
	})
	require.NoError(t, err)
	return result
}

func TestPrFilter_INV23OutOfAllowlistHunkAbortsPrCreation(t *testing.T) {
	git := gpRepoWithBranch(t,
		[][2]string{
			{"docs/payments.md", "# Payments\n\nRefunds are automatic.\n"},
			{"src/payments/refund.ts", "export const refund = () => 1;\n"},
		},
		// The branch diff touches a SOURCE file — invariant 2: code is
		// analysis-only; the allowlist (INV-3) never admits it.
		[][2]string{{"src/payments/refund.ts", "export const refund = () => 2;\n"}},
	)
	result := gpFilterBranchDiff(t, git, gpPaymentsOnlyManifest())

	require.False(t, result.OK)
	require.Len(t, result.Violations, 1)
	violation := result.Violations[0]
	assert.Equal(t, "src/payments/refund.ts", violation.Path)
	assert.Equal(t, gitpr.PrFilterRuleOutOfAllowlist, violation.Rule)
	assert.Contains(t, violation.Detail, "computed from the manifest itself")
}

func TestPrFilter_INV4ProtectedDocHunkRejectsWholePatch(t *testing.T) {
	manifest := &schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/payments.md", Mode: gpModePtr(schemas.DocModeAuto), Sources: []string{"src/payments/**"}},
			// A LOCKED manifest doc: terminal D2 floor — never writable at any
			// confidence; its hunks must reject the WHOLE PR (whole-patch), even
			// though the payments hunks in the same diff are perfectly clean.
			{Path: "docs/security.md", Mode: gpModePtr(schemas.DocModeLocked), Sources: []string{"src/auth/**"}},
		},
	}
	git := gpRepoWithBranch(t,
		[][2]string{
			{"docs/payments.md", "# Payments\n\nRefunds are automatic.\n"},
			{"docs/security.md", "# Security\n\nDo not edit (locked).\n"},
		},
		[][2]string{
			{"docs/security.md", "# Security\n\nDo not edit (locked) — EDITED.\n"},
			{"docs/payments.md", "# Payments\n\nRefunds are automatic within 5 days.\n"},
		},
	)
	result := gpFilterBranchDiff(t, git, manifest)

	// WHOLE-patch rejection: ok is false — the clean payments hunk does NOT
	// survive a partial filter-through.
	require.False(t, result.OK)
	protected := make([]gitpr.PrFilterViolation, 0, len(result.Violations))
	for _, violation := range result.Violations {
		if violation.Rule == gitpr.PrFilterRuleProtectedPath {
			protected = append(protected, violation)
		}
	}
	require.Len(t, protected, 1)
	assert.Equal(t, "docs/security.md", protected[0].Path)
	assert.Contains(t, protected[0].Detail, "protected")
	// The clean writable doc is not itself a violation (rejection is whole,
	// not per-path).
	for _, violation := range result.Violations {
		assert.NotEqual(t, "docs/payments.md", violation.Path)
	}
}

func TestPrFilter_StateCarriagePathsAlwaysAllowed(t *testing.T) {
	git := gpRepoWithBranch(t,
		[][2]string{{"docs/payments.md", "# Payments\n\nRefunds are automatic.\n"}},
		[][2]string{
			{"docs/payments.md", "# Payments\n\nRefunds are automatic within 5 days.\n"},
			// The OD-R12 carriage: state updates ride the doc PR — ALWAYS
			// allowed by layer 4, independent of the manifest's document list.
			{gitpr.StateCarriagePrefix + "docs/payments.json", "{\"doc\":\"docs/payments.md\"}\n"},
		},
	)
	result := gpFilterBranchDiff(t, git, gpPaymentsOnlyManifest())

	assert.True(t, result.OK)
	assert.Empty(t, result.Violations)
}

func TestPrFilter_P6T10FailingDocDroppedWithReportOthersMergeWorthy(t *testing.T) {
	candidates := []gitpr.PartialPassCandidate{
		{
			DocPath:      "docs/payments.md",
			Verified:     true,
			Verification: gpFilterReport("docs/payments.md", "state/docs/payments.md#proposal-d_01J"),
		},
		{
			DocPath:      "docs/billing.md",
			Verified:     true,
			Verification: gpFilterReport("docs/billing.md", "state/docs/billing.md#proposal-d_01K"),
		},
		{
			DocPath:  "docs/draft.md",
			Verified: false,
			Detail:   "verifier abstained: unanchored claims",
		},
	}
	result, err := gitpr.AssemblePartialPass(candidates)
	require.NoError(t, err)

	require.Len(t, result.Included, 2)
	require.Len(t, result.Dropped, 1)
	// Included docs unaffected, input order preserved.
	assert.Equal(t, "docs/payments.md", result.Included[0].DocPath)
	assert.Equal(t, "docs/billing.md", result.Included[1].DocPath)
	require.NotNil(t, result.Included[0].PatchRef)
	assert.Equal(t, "state/docs/payments.md#proposal-d_01J", *result.Included[0].PatchRef)
	// The drop is a REPORT entry: non-empty reason AND detail, never silent.
	dropped := result.Dropped[0]
	assert.Equal(t, "docs/draft.md", dropped.DocPath)
	assert.NotEmpty(t, dropped.Reason)
	assert.NotEmpty(t, dropped.Detail)
	assert.Contains(t, dropped.Detail, "unanchored claims")
	// The report lists the drop (and the survivors) deterministically.
	report := gitpr.RenderPartialPassReport(result)
	assert.Contains(t, report, "docs/draft.md")
	assert.Contains(t, report, "unanchored claims")
	assert.Contains(t, report, "docs/payments.md")
	assert.Contains(t, report, "docs/billing.md")
	assert.Contains(t, report, "reported, never silently")
}

func TestPrFilter_FilterIsDerivedFromManifestNotModelOutput(t *testing.T) {
	// The manifest's ONLY writable doc is payments; generated-report is
	// manifest-LISTED but generated-mode (PROTECTED_MODES) — the allowlist
	// is read from this structure, never from what a pipeline claimed.
	manifest := &schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/payments.md", Mode: gpModePtr(schemas.DocModeAuto), Sources: []string{"src/payments/**"}},
			{Path: "docs/generated-report.md", Mode: gpModePtr(schemas.DocModeGenerated), Sources: []string{"src/report/**"}},
		},
	}
	// The allowlist computation reads the manifest structure directly.
	derived := gitpr.DerivePrPathAllowlist(manifest)
	assert.Equal(t, []string{"docs/payments.md"}, gpSortedKeys(derived.WritablePaths))
	assert.Equal(t, []string{"docs/generated-report.md"}, gpSortedKeys(derived.ProtectedPaths))

	git := gpRepoWithBranch(t,
		[][2]string{
			{"docs/payments.md", "# Payments\n"},
			{"docs/generated-report.md", "AUTO-GENERATED — do not edit.\n"},
		},
		// The DIFFERENT manifest-listed-but-generated doc changed → violates
		// (even though it IS in the manifest — mode, not listing, decides).
		[][2]string{{"docs/generated-report.md", "AUTO-GENERATED — do not edit (touched).\n"}},
	)
	result := gpFilterBranchDiff(t, git, manifest)

	require.False(t, result.OK)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, "docs/generated-report.md", result.Violations[0].Path)
	assert.Equal(t, gitpr.PrFilterRuleProtectedPath, result.Violations[0].Rule)
}
