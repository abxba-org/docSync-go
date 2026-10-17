package gitpr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	gitx "github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// covGateGit is the rebase gate's git double (P19): every read is scripted,
// every write records, and a flagged key reports a MISSING REV so the gate's
// own classification is exercised (deleted upstream / unreadable base).
type covGateGit struct {
	mu sync.Mutex

	root     string
	blobs    map[string]string // "rev:path" → content
	missing  map[string]bool   // keys CatFile reports as a missing rev
	statuses [][]string

	rebaseErr    error
	catFileErrs  map[string]error
	resolveErr   error
	mergeBaseErr error
	rebaseOK     bool
	commitSha    string
	baseSha      string
	headSha      string
	cachedPaths  []string
	cachedStaged bool

	catFileCalls  []string
	stageCalls    [][]string
	commitCalls   []string
	rebaseCalls   [][2]string
	mergeBaseCall []string
	resolveCalls  []string
}

func newCovGateGit(root string) *covGateGit {
	return &covGateGit{
		root:      root,
		blobs:     map[string]string{},
		missing:   map[string]bool{},
		statuses:  [][]string{{}},
		rebaseOK:  true,
		commitSha: "c0ffee11c0ffee11c0ffee11c0ffee11c0ffee11",
		baseSha:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		headSha:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func (g *covGateGit) Root() string { return g.root }

func (g *covGateGit) CatFile(_ context.Context, rev, path string) (gitx.CatFileResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.catFileCalls = append(g.catFileCalls, rev+":"+path)
	key := rev + ":" + path
	if g.missing[key] {
		return gitx.CatFileResult{}, gitx.NewGitExecError(gitx.KindMissingRev,
			"could not resolve rev", &gitx.GitExecErrorOptions{StderrExcerpt: "no such rev"})
	}
	content, ok := g.blobs[key]
	if !ok {
		if keyErr := g.catFileErrs[key]; keyErr != nil {
			return gitx.CatFileResult{}, keyErr
		}
		return gitx.CatFileResult{}, gitx.NewGitExecError(gitx.KindUnknownRef,
			"path not found", &gitx.GitExecErrorOptions{StderrExcerpt: "path does not exist"})
	}
	return gitx.CatFileResult{BlobSha: "blob-sha-for-" + key, Content: content}, nil
}

func (g *covGateGit) RebaseBranch(_ context.Context, branch, onto string) (gitx.RebaseBranchResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rebaseCalls = append(g.rebaseCalls, [2]string{branch, onto})
	if g.rebaseErr != nil {
		return gitx.RebaseBranchResult{}, g.rebaseErr
	}
	if !g.rebaseOK {
		return gitx.RebaseConflicted(), nil
	}
	return gitx.RebaseOK(), nil
}

func (g *covGateGit) StatusPorcelain(_ context.Context) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.catFileCalls) + len(g.rebaseCalls) + len(g.stageCalls) + len(g.commitCalls)
	if n >= len(g.statuses) {
		return g.statuses[len(g.statuses)-1], nil
	}
	return g.statuses[n], nil
}

func (g *covGateGit) StagePaths(_ context.Context, paths []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stageCalls = append(g.stageCalls, paths)
	return nil
}

func (g *covGateGit) Commit(_ context.Context, message string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commitCalls = append(g.commitCalls, message)
	return g.commitSha, nil
}

func (g *covGateGit) MergeBase(_ context.Context, a, b string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mergeBaseCall = append(g.mergeBaseCall, a+".."+b)
	if g.mergeBaseErr != nil {
		return "", g.mergeBaseErr
	}
	return g.baseSha, nil
}

func (g *covGateGit) ResolveRev(_ context.Context, ref string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resolveCalls = append(g.resolveCalls, ref)
	if g.resolveErr != nil {
		return "", g.resolveErr
	}
	return g.headSha, nil
}

func (g *covGateGit) CachedFilePaths(_ context.Context) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cachedStaged {
		return []string{"foreign.txt"}, nil
	}
	return g.cachedPaths, nil
}

// covGatePatch is one verified candidate patch over the doc-path fixture.
func covGatePatch(t *testing.T, docPath, baseHash string) *schemas.DocPatch {
	t.Helper()
	patchValue := schemas.DocPatchExample
	patchValue.DocPath = docPath
	patchValue.RunID = "run_gate"
	patchValue.BaseContentHash = baseHash
	return &patchValue
}

// covGateDeps assembles a gate over the double with a permissive reverify.
func covGateDeps(g *covGateGit, reverify ReverifyPort) RebaseGateDeps {
	return RebaseGateDeps{
		Git:      g,
		Reverify: reverify,
		Now:      noopDropConstantNow,
	}
}

// TestCOV_GITPR_027_GateConfigRefusals pins the gate's fail-closed input
// surface (every malformed deps/input member).
func TestCOV_GITPR_027_GateConfigRefusals(t *testing.T) {
	patchValue := covGatePatch(t, "docs/doc.md", "h")

	_, err := CreateRebaseGate(RebaseGateDeps{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deps.git must be a GitClient")

	_, err = CreateRebaseGate(RebaseGateDeps{Git: newCovGateGit(t.TempDir()), Reverify: nil})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reverify")

	gate, err := CreateRebaseGate(RebaseGateDeps{
		Git:      newCovGateGit(t.TempDir()),
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return nil },
		Now:      covFixedTime,
	})
	require.NoError(t, err)

	// The three run-input refusals (branch / defaultBranch / patches).
	_, err = gate.Run(context.Background(), RebaseGateRunInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch must be a nonempty string")
	_, err = gate.Run(context.Background(), RebaseGateRunInput{Branch: "b", Patches: []*schemas.DocPatch{patchValue}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defaultBranch must be a nonempty string")
	_, err = gate.Run(context.Background(), RebaseGateRunInput{Branch: "b", DefaultBranch: "main"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "patches must be non-empty")
	// A nil/empty patch entry.
	_, err = gate.Run(context.Background(), RebaseGateRunInput{
		Branch: "b", DefaultBranch: "main", Patches: []*schemas.DocPatch{nil},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "every patch must carry a doc_path")
	empty := covGatePatch(t, "", "h")
	_, err = gate.Run(context.Background(), RebaseGateRunInput{
		Branch: "b", DefaultBranch: "main", Patches: []*schemas.DocPatch{empty},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "every patch must carry a doc_path")

	// A nil Now falls back to the deterministic default clock (P19).
	defaulted, err := CreateRebaseGate(RebaseGateDeps{
		Git:      newCovGateGit(t.TempDir()),
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return nil },
	})
	require.NoError(t, err)
	require.NotNil(t, defaulted.deps.Now)
	assert.Equal(t, "1970-01-01T00:00:00.000Z", defaulted.deps.Now())

	// gateConfigError / gateReverifyPass boundaries are pinned in the helper test
	// below; the config error code resolves through docutil.
	assert.Equal(t, "EDOCSYNC_REBASE_GATE", gateConfigError("boom").Code())
}

// TestCOV_GITPR_028_GateDefaultHelpers pins the gate's pure helpers.
func TestCOV_GITPR_028_GateDefaultHelpers(t *testing.T) {
	// gateDefaultFileApplier writes the repo file through the OS seam.
	root := t.TempDir()
	applier := gateDefaultFileApplier(root)
	require.NotNil(t, applier)
	require.NoError(t, applier("docs/deep/doc.md", "# merged content\n"))
	written, err := os.ReadFile(filepath.Join(root, "docs/deep/doc.md"))
	require.NoError(t, err)
	assert.Equal(t, "# merged content\n", string(written))

	// gateJSONString / gateReportOrNil / gateReverifyVerdictDetail.
	assert.Equal(t, `"x"`, gateJSONString("x"))
	assert.Equal(t, `"a\nb"`, gateJSONString("a\nb"))
	report := gateReportOrNil(nil)
	require.NotNil(t, report)
	assert.Empty(t, report.Verdict)
	assert.Equal(t, "verdict '', auto_eligible=false, auto_block_reasons=[]",
		gateReverifyVerdictDetail(report))
	full := &schemas.VerificationReport{
		Verdict: schemas.VerdictReview, AutoEligible: true, AutoBlockReasons: []string{"R1", "R2"},
	}
	assert.Same(t, full, gateReportOrNil(full))
	assert.Contains(t, gateReverifyVerdictDetail(full), "auto_eligible=true")
	assert.Contains(t, gateReverifyVerdictDetail(full), "R1, R2")
	assert.False(t, gateReverifyPass(nil))
	assert.False(t, gateReverifyPass(&schemas.VerificationReport{Verdict: schemas.VerdictPass}))
	assert.True(t, gateReverifyPass(&schemas.VerificationReport{
		Verdict: schemas.VerdictPass, AutoEligible: true,
	}))
}

// TestCOV_GITPR_029_GateSemanticOutcomes pins every abstain path plus the clean
// and rebased-reverified outcomes (A9: the gate never force-applies).
func TestCOV_GITPR_029_GateSemanticOutcomes(t *testing.T) {
	const (
		branch = "docsync/run_gate"
		base   = "main"
		doc    = "docs/doc.md"
	)
	ctx := context.Background()

	passReverify := func(*schemas.DocPatch, string) *schemas.VerificationReport {
		return &schemas.VerificationReport{Verdict: schemas.VerdictPass, AutoEligible: true}
	}

	// (a) dirty worktree → abstained before ANY ref work.
	dirty := newCovGateGit(t.TempDir())
	dirty.statuses = [][]string{{"M some/file"}}
	gate, err := CreateRebaseGate(covGateDeps(dirty, passReverify))
	require.NoError(t, err)
	result, err := gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateOutcomeAbstained, result.Outcome)
	assert.Equal(t, RebaseGateAbstainDirtyWorktree, result.Reason)
	assert.Contains(t, result.Detail, "not clean")
	assert.Empty(t, dirty.rebaseCalls, "no rebase is attempted on a dirty tree")

	// (b) unrelated histories → abstained (no common ancestor).
	unrelated := newCovGateGit(t.TempDir())
	unrelated.baseSha = ""
	gate, err = CreateRebaseGate(covGateDeps(unrelated, passReverify))
	require.NoError(t, err)
	result, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateAbstainBaseContentUnavailable, result.Reason)
	assert.Contains(t, result.Detail, "no common ancestor")

	// (c) the branch already carries the default head → clean, no rebase.
	fresh := newCovGateGit(t.TempDir())
	fresh.baseSha = fresh.headSha
	gate, err = CreateRebaseGate(covGateDeps(fresh, passReverify))
	require.NoError(t, err)
	result, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateOutcomeClean, result.Outcome)
	assert.Empty(t, fresh.rebaseCalls)

	// (d) no expected base content hash entry → abstained (cannot classify).
	noHash := newCovGateGit(t.TempDir())
	gate, err = CreateRebaseGate(covGateDeps(noHash, passReverify))
	require.NoError(t, err)
	result, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateAbstainBaseContentUnavailable, result.Reason)
	assert.Contains(t, result.Detail, "no expected base content hash")

	// (e) a disagreeing patch base_content_hash → abstained.
	mismatch := newCovGateGit(t.TempDir())
	gate, err = CreateRebaseGate(covGateDeps(mismatch, passReverify))
	require.NoError(t, err)
	result, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "patch-hash")},
		ExpectedBaseContentHashes: map[string]string{doc: "expected-hash"},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateAbstainBaseContentUnavailable, result.Reason)
	assert.Contains(t, result.Detail, "disagrees with the expected base content hash")

	// (f) the doc is DELETED upstream → race unresolved (A9).
	deleted := newCovGateGit(t.TempDir())
	deleted.missing[base+":"+doc] = true
	gate, err = CreateRebaseGate(covGateDeps(deleted, passReverify))
	require.NoError(t, err)
	result, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateAbstainRaceUnresolved, result.Reason)
	assert.Contains(t, result.Detail, "no longer exists")

	// (g) the writer's base snapshot is unreadable → base content unavailable.
	noBase := newCovGateGit(t.TempDir())
	noBase.blobs[base+":"+doc] = "# drifted upstream content\n"
	noBase.missing[noBase.baseSha+":"+doc] = true
	gate, err = CreateRebaseGate(covGateDeps(noBase, passReverify))
	require.NoError(t, err)
	result, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateAbstainBaseContentUnavailable, result.Reason)
	assert.Contains(t, result.Detail, "not readable at the merge-base")

	// (h) the merge-base content does not hash to the patch baseline.
	shifted := newCovGateGit(t.TempDir())
	shifted.blobs[base+":"+doc] = "# drifted upstream content\n"
	shifted.blobs[shifted.baseSha+":"+doc] = "# different content\n"
	gate, err = CreateRebaseGate(covGateDeps(shifted, passReverify))
	require.NoError(t, err)
	result, err = gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   []*schemas.DocPatch{covGatePatch(t, doc, "h")},
		ExpectedBaseContentHashes: map[string]string{doc: "h"},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateAbstainBaseContentUnavailable, result.Reason)
	assert.Contains(t, result.Detail, "does not hash to the patch's base_content_hash")
}

// TestCOV_GITPR_030_GateRaceResolution pins the raced-document resolution: a
// no-op drop is removed from the survivors, the re-apply seam commits EXACTLY
// the merged content, and a foreign staged path is a typed scope refusal.
func TestCOV_GITPR_030_GateRaceResolution(t *testing.T) {
	const (
		branch = "docsync/run_gate"
		base   = "main"
		doc    = "docs/doc.md"
	)
	ctx := context.Background()

	passReverify := func(*schemas.DocPatch, string) *schemas.VerificationReport {
		return &schemas.VerificationReport{Verdict: schemas.VerdictPass, AutoEligible: true}
	}

	// covGateDocs builds the fixture's documents: 41 filler lines plus the
	// writer's line-42 (the example patch's deleted line) and its patched form.
	docs := func(patchedLine string) string {
		lines := make([]string, 0, 42)
		for i := 1; i <= 41; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		lines = append(lines, patchedLine)
		return strings.Join(lines, "\n") + "\n"
	}
	baseContent := docs("Refunds require an idempotency key.")
	alreadyApplied := docs("Refunds require an idempotency key for amounts above 10 USD.")
	baseHash := docutil.ContentHash(baseContent)
	patches := []*schemas.DocPatch{covGatePatch(t, doc, baseHash)}

	// (1) Upstream already carries the patch's effect ⇒ a no-op drop.
	noop := newCovGateGit(t.TempDir())
	noop.blobs[base+":"+doc] = alreadyApplied
	noop.blobs[noop.baseSha+":"+doc] = baseContent
	noop.blobs[branch+":"+doc] = baseContent
	noop.cachedPaths = []string{doc}
	gate, err := CreateRebaseGate(covGateDeps(noop, passReverify))
	require.NoError(t, err)
	result, err := gate.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   patches,
		ExpectedBaseContentHashes: map[string]string{doc: baseHash},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateOutcomeRebasedReverified, result.Outcome)
	assert.Empty(t, result.Patches, "the drifted no-op patch is dropped")
	require.Len(t, noop.rebaseCalls, 1)
	assert.Equal(t, [2]string{branch, base}, noop.rebaseCalls[0])
	assert.Empty(t, noop.commitCalls, "a dropped patch commits nothing")

	// (2) Up drifted further: the engine re-applies the merged content through
	// the injected seam and commits EXACTLY one scoped path.
	drifted := newCovGateGit(t.TempDir())
	drifted.blobs[base+":"+doc] = "# new upstream note\n" + baseContent
	drifted.blobs[drifted.baseSha+":"+doc] = baseContent
	drifted.blobs[branch+":"+doc] = baseContent
	drifted.cachedPaths = []string{doc}
	var appliedPaths []string
	deps := covGateDeps(drifted, passReverify)
	deps.Apply = func(path, content string) error {
		appliedPaths = append(appliedPaths, path)
		return nil
	}
	gate2, err := CreateRebaseGate(deps)
	require.NoError(t, err)
	result, err = gate2.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   patches,
		ExpectedBaseContentHashes: map[string]string{doc: baseHash},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateOutcomeRebasedReverified, result.Outcome)
	require.Len(t, result.Patches, 1, "the resolved patch survives")
	assert.Equal(t, doc, result.Patches[0].DocPath)
	require.NotEmpty(t, drifted.commitCalls, "the merged result is committed")
	assert.Contains(t, drifted.commitCalls[0], "docs:")
	assert.Contains(t, drifted.commitCalls[0], "Provenance: run_gate")

	// (3) A FOREIGN staged path is a typed scope refusal — the commit aborts.
	foreign := newCovGateGit(t.TempDir())
	foreign.blobs[base+":"+doc] = "# new upstream note\n" + baseContent
	foreign.blobs[foreign.baseSha+":"+doc] = baseContent
	foreign.blobs[branch+":"+doc] = baseContent
	foreign.cachedStaged = true
	gate3, err := CreateRebaseGate(covGateDeps(foreign, passReverify))
	require.NoError(t, err)
	_, err = gate3.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   patches,
		ExpectedBaseContentHashes: map[string]string{doc: baseHash},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope assert failed")
	assert.Empty(t, foreign.commitCalls, "no commit survives a scope violation")

	// (4) A rebase CONFLICT abstains (never force-applies) and the tree is
	// asserted clean afterwards.
	conflicted := newCovGateGit(t.TempDir())
	conflicted.blobs[base+":"+doc] = alreadyApplied
	conflicted.blobs[conflicted.baseSha+":"+doc] = baseContent
	conflicted.rebaseOK = false
	conflicted.cachedPaths = []string{}
	gate4, err := CreateRebaseGate(covGateDeps(conflicted, passReverify))
	require.NoError(t, err)
	result, err = gate4.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   patches,
		ExpectedBaseContentHashes: map[string]string{doc: baseHash},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateOutcomeAbstained, result.Outcome)
	assert.Equal(t, RebaseGateAbstainRebaseConflict, result.Reason)
	assert.Contains(t, result.Detail, "conflicts")
	assert.Contains(t, result.Detail, "nothing is force-applied")

	// (5) A REBASED result that fails to re-verify abstains (A9).
	unoozed := newCovGateGit(t.TempDir())
	unoozed.blobs[base+":"+doc] = "# new upstream note\n" + baseContent
	unoozed.blobs[unoozed.baseSha+":"+doc] = baseContent
	unoozed.blobs[branch+":"+doc] = baseContent
	unoozed.cachedPaths = []string{doc}
	failReverify := func(*schemas.DocPatch, string) *schemas.VerificationReport {
		return &schemas.VerificationReport{
			Verdict: schemas.VerdictReject, AutoEligible: false, AutoBlockReasons: []string{"claim-not-verified"},
		}
	}
	gate5, err := CreateRebaseGate(covGateDeps(unoozed, failReverify))
	require.NoError(t, err)
	result, err = gate5.Run(ctx, RebaseGateRunInput{
		Branch: branch, DefaultBranch: base,
		Patches:                   patches,
		ExpectedBaseContentHashes: map[string]string{doc: baseHash},
	})
	require.NoError(t, err)
	assert.Equal(t, RebaseGateOutcomeAbstained, result.Outcome)
	assert.Equal(t, RebaseGateAbstainReverifyFailed, result.Reason)
	assert.Contains(t, result.Detail, "did not re-pass verification")
	assert.Contains(t, result.Detail, "claim-not-verified")
	assert.Contains(t, result.Detail, "never force-applies")
}
