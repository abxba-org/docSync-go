package gitpr_test

// Black-box port of test/gitpr-rebase.test.ts (P6-T14/T15; PLAN §7.3 lines
// 1159-1161; ARCH §12.3; A9; invariants 2/3/12/13) for the rebase gate and
// no-op drop evaluators this unit owns.
//
// The TS suite drove REAL system git in fresh `os.tmpdir()` repos. AGENTS.md
// §4 forbids network-dependent unit tests and asks for recorded doubles, so
// this port drives the same flows through a recorded `RebaseGateGitClient`
// double (deterministic, no subprocess): every git read the gate performs is
// served from a blob map, every write is recorded. The no-op evaluator is pure
// content simulation and needs no repository at all (same as the TS).
//
// Named gates:
//   - A9/P6-T14 upstream-change-reverified-pass-merges: the merged content the
//     reverify seam SAW is exactly what the branch carries, and it re-applied
//     as a single scoped commit.
//   - A9/P6-T14 reverify-fail-abstains-tree-untouched: abstained
//     'reverify-failed', the verdict + block reasons in the detail, exactly one
//     reverify call, and NO commit was added by the gate.
//   - P6-T14 rebase-conflict-abstains-never-force-applies: abstained
//     'rebase-conflict', the tree asserted clean after the abort, zero reverify
//     calls.
//   - P6-T15 already-fixed-drift-drops-noop / genuine-drift-is-not-a-noop:
//     the strict content rule and its rebase-gate boundary.

import (
	"context"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ fixtures */

const gateTestDoc = "docs/payments.md"
const gateTestBranch = "docsync/pr-481-abc1234"
const gateTestDefault = "main"
const gateTestHeadSha = "1111111111111111111111111111111111111111"
const gateTestBaseSha = "0000000000000000000000000000000000000000"

// The writer's base: the stale document the patch was grounded in.
var gateBaseDocVar = `# Payments

Refunds are automatic.

## Limits

Daily limit is 100 USD.

## Support

Contact payments support for questions.

## History

- 2026-01: refunds documented
- 2026-02: limits documented
`

// The patched document (the writer's intended effect).
var gatePatchedDoc = strings.Replace(gateBaseDocVar, "Refunds are automatic.", "Refunds are automatic within 5 days.", 1)

// Upstream evolved ELSEWHERE in the document — rebase-mergeable.
var gateUpstreamDoc = strings.Replace(gateBaseDocVar, "Daily limit is 100 USD.", "Daily limit is 150 USD.", 1)

// Upstream rewrote the SAME line the patch rewrites — a hard rebase conflict.
var gateConflictingUpstreamDoc = strings.Replace(gateBaseDocVar, "Refunds are automatic.", "Refunds are automatic after review.", 1)

// gateTestPatch builds the candidate patch: replace the stale refund line in
// gateBaseDocVar (the TS `patchFor()` fixture).
func gateTestPatch(docPath string) *schemas.DocPatch {
	hunk := schemas.DocPatchExample.Hunks[0]
	return &schemas.DocPatch{
		Schema:          schemas.DocPatchExample.Schema,
		RunID:           "acme/payments#8a73f2c..81af92e",
		DocPath:         docPath,
		BaseContentHash: docutil.ContentHash(gateBaseDocVar),
		Hunks: []schemas.PatchHunk{{
			Anchor: schemas.PatchAnchor{
				StartLine:   3,
				EndLine:     3,
				ContentHash: claims.HashSpan("Refunds are automatic."),
			},
			DeleteText:   "Refunds are automatic.",
			InsertText:   "Refunds are automatic within 5 days.",
			EvidenceRefs: hunk.EvidenceRefs,
			ClaimIDs:     hunk.ClaimIDs,
		}},
		EvidenceRefs: schemas.DocPatchExample.EvidenceRefs,
		Minimality:   schemas.DocPatchExample.Minimality,
		Provenance:   schemas.DocPatchExample.Provenance,
	}
}

func gatePassReport(docPath string) *schemas.VerificationReport {
	return &schemas.VerificationReport{
		Schema:           "docsync.verification-report/v1",
		RunID:            "acme/payments#8a73f2c..81af92e",
		DocPath:          docPath,
		PatchRef:         "patch-ref",
		Verdict:          schemas.VerdictPass,
		AutoEligible:     true,
		AutoBlockReasons: []string{},
		PolicyVersion:    17,
	}
}

func gateRejectReport(docPath string) *schemas.VerificationReport {
	return &schemas.VerificationReport{
		Schema:           "docsync.verification-report/v1",
		RunID:            "acme/payments#8a73f2c..81af92e",
		DocPath:          docPath,
		PatchRef:         "patch-ref",
		Verdict:          schemas.VerdictReject,
		AutoEligible:     false,
		AutoBlockReasons: []string{schemas.AutoBlockUnanchoredClaims},
		PolicyVersion:    17,
	}
}

/* ------------------------------------------------------------ the git double */

// gateFakeGit is the recorded GitClient double: every read is served from blobs
// (`<rev>:<path>` → content), every write is recorded. A blob key absent from
// the map — or present in `missing` — is the typed missing-rev failure.
type gateFakeGit struct {
	root        string
	blobs       map[string]string
	missing     map[string]bool
	status      []string
	statusFn    func() []string
	rebaseOK    bool
	mergeBaseFn func(context.Context, string, string) string
	staged      []string
	commits     []string
	calls       []string
	applies     []string
	applyFn     func(relPath, content string) error
}

func newGateFakeGit() *gateFakeGit {
	return &gateFakeGit{
		root:     "/repo",
		blobs:    map[string]string{},
		missing:  map[string]bool{},
		rebaseOK: true,
	}
}

func (f *gateFakeGit) Root() string { return f.root }

func (f *gateFakeGit) CatFile(_ context.Context, rev, path string) (gitx.CatFileResult, error) {
	f.calls = append(f.calls, "catFile "+rev+":"+path)
	key := rev + ":" + path
	if f.missing[key] {
		return gitx.CatFileResult{}, gitx.NewGitExecError(gitx.KindMissingRev,
			"path '"+path+"' does not exist in '"+rev+"'", nil)
	}
	content, ok := f.blobs[key]
	if !ok {
		return gitx.CatFileResult{}, gitx.NewGitExecError(gitx.KindMissingRev,
			"path '"+path+"' does not exist in '"+rev+"'", nil)
	}
	return gitx.CatFileResult{BlobSha: docutil.ContentHash(content), Content: content}, nil
}

func (f *gateFakeGit) RebaseBranch(_ context.Context, branch, onto string) (gitx.RebaseBranchResult, error) {
	f.calls = append(f.calls, "rebaseBranch "+branch+" onto "+onto)
	if f.rebaseOK {
		return gitx.RebaseOK(), nil
	}
	return gitx.RebaseConflicted(), nil
}

func (f *gateFakeGit) StatusPorcelain(_ context.Context) ([]string, error) {
	f.calls = append(f.calls, "statusPorcelain")
	if f.statusFn != nil {
		return f.statusFn(), nil
	}
	return f.status, nil
}

func (f *gateFakeGit) StagePaths(_ context.Context, paths []string) error {
	f.calls = append(f.calls, "stagePaths")
	f.staged = append(f.staged, paths...)
	return nil
}

func (f *gateFakeGit) CachedFilePaths(_ context.Context) ([]string, error) {
	f.calls = append(f.calls, "cachedFilePaths")
	return f.staged, nil
}

func (f *gateFakeGit) Commit(_ context.Context, message string) (string, error) {
	f.calls = append(f.calls, "commit")
	f.commits = append(f.commits, message)
	return "2222222222222222222222222222222222222222", nil
}

func (f *gateFakeGit) MergeBase(_ context.Context, a, b string) (string, error) {
	f.calls = append(f.calls, "mergeBase "+a+" "+b)
	if f.mergeBaseFn != nil {
		return f.mergeBaseFn(nil, a, b), nil
	}
	return gateTestBaseSha, nil
}

func (f *gateFakeGit) ResolveRev(_ context.Context, ref string) (string, error) {
	f.calls = append(f.calls, "resolveRev "+ref)
	return gateTestHeadSha, nil
}

func (f *gateFakeGit) set(rev, path, content string) {
	f.blobs[rev+":"+path] = content
}

// gateApply returns a DocFileApplier recording every re-apply.
func (f *gateFakeGit) applySeam() gitpr.DocFileApplier {
	return func(relPath, content string) error {
		f.applies = append(f.applies, relPath)
		f.set(gateTestBranch, relPath, content)
		if f.applyFn != nil {
			return f.applyFn(relPath, content)
		}
		return nil
	}
}

/* -------------------------------------------------------------- gate helpers */

// gateFixture wires a diverged repo: the stale doc committed on both refs, the
// patch head on the branch, and `upstreamContent` on the default branch.
func gateFixture(t *testing.T, upstreamContent string) (*gateFakeGit, *schemas.DocPatch) {
	t.Helper()
	git := newGateFakeGit()
	git.set(gateTestBaseSha, gateTestDoc, gateBaseDocVar)
	git.set(gateTestHeadSha, gateTestDoc, upstreamContent)
	git.set(gateTestDefault, gateTestDoc, upstreamContent)
	git.set(gateTestBranch, gateTestDoc, gatePatchedDoc)
	return git, gateTestPatch(gateTestDoc)
}

func gateRunInput(patches ...*schemas.DocPatch) gitpr.RebaseGateRunInput {
	return gitpr.RebaseGateRunInput{
		Branch:                    gateTestBranch,
		DefaultBranch:             gateTestDefault,
		Patches:                   patches,
		ExpectedBaseContentHashes: map[string]string{gateTestDoc: docutil.ContentHash(gateBaseDocVar)},
	}
}

func gateExpectAbstained(t *testing.T, result gitpr.RebaseGateResult, reason gitpr.RebaseGateAbstainReason) {
	t.Helper()
	require.Equal(t, gitpr.RebaseGateOutcomeAbstained, result.Outcome)
	require.Equal(t, reason, result.Reason)
	require.NotEmpty(t, result.Detail)
}

/* -------------------------------------------------------------------- tests */

func TestP6T14_UpstreamChangeReverifiedPassMerges(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	var seenHeadContents []string
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git: git,
		Reverify: func(racedPatch *schemas.DocPatch, headContent string) *schemas.VerificationReport {
			require.Equal(t, gateTestDoc, racedPatch.DocPath)
			seenHeadContents = append(seenHeadContents, headContent)
			return gatePassReport(gateTestDoc)
		},
		Apply: git.applySeam(),
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	require.Equal(t, gitpr.RebaseGateOutcomeRebasedReverified, result.Outcome)
	require.Len(t, result.Patches, 1)
	require.Same(t, candidate, result.Patches[0])

	// The re-verified merged content is what the seam saw AND what the branch
	// carries: both the patch's fix and the upstream evolution survive.
	require.Len(t, seenHeadContents, 1)
	branchContent, err := git.CatFile(context.Background(), gateTestBranch, gateTestDoc)
	require.NoError(t, err)
	require.Equal(t, seenHeadContents[0], branchContent.Content)
	require.Contains(t, branchContent.Content, "Refunds are automatic within 5 days.")
	require.Contains(t, branchContent.Content, "Daily limit is 150 USD.")

	// The re-apply landed as EXACTLY one scoped commit (write → stage → scope
	// assert → commit), with the deterministic re-apply message.
	require.Len(t, git.applies, 1)
	require.Equal(t, gateTestDoc, git.applies[0])
	require.Len(t, git.commits, 1)
	require.Contains(t, git.commits[0], "docs: re-apply documentation after rebase")
	require.Contains(t, git.commits[0], "Provenance: acme/payments#8a73f2c..81af92e")
	require.Equal(t, []string{gateTestDoc}, git.staged)
	// No staged/leftover changes — the tree stayed clean through the run.
	require.Empty(t, git.status)
}

func TestP6T14_ReverifyFailAbstainsTreeUntouched(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	calls := 0
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git: git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport {
			calls++
			return gateRejectReport(gateTestDoc)
		},
		Apply: git.applySeam(),
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainReverifyFailed)
	// The detail carries the verdict and the block reasons (evidence, A9).
	require.Contains(t, result.Detail, "reject")
	require.Contains(t, result.Detail, "unanchored-claims")
	require.Equal(t, 1, calls)

	// Working tree untouched: nothing was applied, staged or committed.
	require.Empty(t, git.applies)
	require.Empty(t, git.staged)
	require.Empty(t, git.commits)
}

func TestP6T14_RebaseConflictAbstainsNeverForceApplies(t *testing.T) {
	git, candidate := gateFixture(t, gateConflictingUpstreamDoc)
	git.rebaseOK = false
	reverifyCalls := 0
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git: git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport {
			reverifyCalls++
			return gatePassReport(gateTestDoc)
		},
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainRebaseConflict)
	require.Contains(t, result.Detail, "aborted")
	require.Equal(t, 0, reverifyCalls)
	// Nothing force-applied: no writes of any kind.
	require.Empty(t, git.applies)
	require.Empty(t, git.commits)
}

func TestP6T14_CleanWhenBranchAlreadyContainsDefaultHead(t *testing.T) {
	git, candidate := gateFixture(t, gateBaseDocVar)
	// merge-base == default head ⇒ nothing to rebase.
	git.mergeBaseFn = func(context.Context, string, string) string { return gateTestHeadSha }
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	require.Equal(t, gitpr.RebaseGateOutcomeClean, result.Outcome)
	for _, call := range git.calls {
		require.False(t, strings.HasPrefix(call, "rebaseBranch"), "the gate must not rebase a clean branch")
	}
}

func TestP6T14_DirtyWorktreeAbstains(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	git.status = []string{" M docs/payments.md"}
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainDirtyWorktree)
	require.Contains(t, result.Detail, "refusing to rebase a dirty tree")
	for _, call := range git.calls {
		require.False(t, strings.HasPrefix(call, "rebaseBranch"), "a dirty tree is never rebased")
	}
}

func TestP6T14_UnrelatedHistoriesAbstain(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	git.mergeBaseFn = func(context.Context, string, string) string { return "" }
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainBaseContentUnavailable)
	require.Contains(t, result.Detail, "unrelated histories")
}

func TestP6T14_MissingExpectedBaseHashAbstains(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
	})
	require.NoError(t, err)

	input := gateRunInput(candidate)
	input.ExpectedBaseContentHashes = map[string]string{}
	result, err := gate.Run(context.Background(), input)
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainBaseContentUnavailable)
	require.Contains(t, result.Detail, "no expected base content hash for doc '"+gateTestDoc+"'")
}

func TestP6T14_BaseHashDisagreementAbstains(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	candidate.BaseContentHash = docutil.ContentHash(gateUpstreamDoc)
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainBaseContentUnavailable)
	require.Contains(t, result.Detail, "inconsistent baseline")
}

func TestP6T14_DocDeletedAtHeadAbstainsRaceUnresolved(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	git.missing[gateTestHeadSha+":"+gateTestDoc] = true
	git.missing[gateTestDefault+":"+gateTestDoc] = true
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainRaceUnresolved)
	require.Contains(t, result.Detail, "changed (deleted) upstream")
}

func TestP6T14_BaseSnapshotUnrecoverableAbstains(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	// The merge-base content no longer hashes to the patch's baseline.
	git.set(gateTestBaseSha, gateTestDoc, gateUpstreamDoc)
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainBaseContentUnavailable)
	require.Contains(t, result.Detail, "writer's base snapshot is unrecoverable")
}

func TestP6T14_EngineConflictAbstainsRaceUnresolved(t *testing.T) {
	// The upstream evolution conflicts with the writer's change INSIDE the
	// engine's 3-way (no rebase conflict at the git level): the engine abstains
	// without ever calling reVerify, so the gate routes a RACE abstention, not a
	// verification failure.
	git, candidate := gateFixture(t, gateConflictingUpstreamDoc)
	// Keep the git rebase itself clean so the engine sees the race.
	git.rebaseOK = true
	reverifyCalls := 0
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git: git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport {
			reverifyCalls++
			return gatePassReport(gateTestDoc)
		},
		Apply: git.applySeam(),
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	gateExpectAbstained(t, result, gitpr.RebaseGateAbstainRaceUnresolved)
	require.Equal(t, 0, reverifyCalls)
	require.Empty(t, git.commits)
}

func TestP6T14_NoopPatchDroppedFromSurvivors(t *testing.T) {
	// The head content already carries the EXACT fix the patch proposes (a prior
	// doc PR merged it upstream): the engine's simulated application is
	// byte-identical → the drifted doc is dropped from the result.
	git, candidate := gateFixture(t, gatePatchedDoc)
	reverifyCalls := 0
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git: git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport {
			reverifyCalls++
			return gatePassReport(gateTestDoc)
		},
		Apply: git.applySeam(),
	})
	require.NoError(t, err)

	result, err := gate.Run(context.Background(), gateRunInput(candidate))
	require.NoError(t, err)
	require.Equal(t, gitpr.RebaseGateOutcomeRebasedReverified, result.Outcome)
	require.Empty(t, result.Patches, "the already-fixed doc is dropped, never shipped")
	require.Equal(t, 0, reverifyCalls, "a byte-identical merge never reaches reVerify")
	require.Empty(t, git.commits)
}

func TestP6T14_ReApplyScopeAssertAbortsCommit(t *testing.T) {
	git, candidate := gateFixture(t, gateUpstreamDoc)
	git.staged = []string{gateTestDoc, "src/foreign.ts"}
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
		Apply:    git.applySeam(),
	})
	require.NoError(t, err)

	_, err = gate.Run(context.Background(), gateRunInput(candidate))
	require.Error(t, err)
	var gateErr *gitpr.RebaseGateError
	require.ErrorAs(t, err, &gateErr)
	require.Equal(t, gitpr.EDOCSYNCRebaseGate, gateErr.Code())
	require.Contains(t, err.Error(), "re-apply scope assert failed")
	require.Contains(t, err.Error(), "foreign or missing staged content")
	require.Empty(t, git.commits, "a scope-assert failure aborts BEFORE the commit")
}

func TestP6T14_InternalInvariantViolationFailsLoud(t *testing.T) {
	// A leftover status entry after the rebase is a bug class, not a semantic
	// outcome: the closing clean-tree assert throws, typed.
	git, candidate := gateFixture(t, gateUpstreamDoc)
	statusCalls := 0
	git.statusFn = func() []string {
		statusCalls++
		// Clean at the pre-flight, dirty afterwards: a leftover entry after the
		// rebase is a bug class, not a semantic outcome.
		if statusCalls == 1 {
			return nil
		}
		return []string{" M docs/payments.md"}
	}
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      git,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return gatePassReport(gateTestDoc) },
		Apply:    git.applySeam(),
	})
	require.NoError(t, err)

	_, err = gate.Run(context.Background(), gateRunInput(candidate))
	require.Error(t, err)
	var gateErr *gitpr.RebaseGateError
	require.ErrorAs(t, err, &gateErr)
	require.Contains(t, err.Error(), "rebase gate internal invariant violated")
	require.Contains(t, err.Error(), "no staged/leftover changes are ever acceptable")
}

func TestP6T14_InputValidationIsFailClosed(t *testing.T) {
	gate, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Git:      newGateFakeGit(),
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return nil },
	})
	require.NoError(t, err)

	_, err = gate.Run(context.Background(), gitpr.RebaseGateRunInput{Branch: "", DefaultBranch: gateTestDefault, Patches: []*schemas.DocPatch{gateTestPatch(gateTestDoc)}, ExpectedBaseContentHashes: map[string]string{gateTestDoc: docutil.ContentHash(gateBaseDocVar)}})
	require.ErrorContains(t, err, "rebase gate run: branch must be a nonempty string")

	_, err = gate.Run(context.Background(), gitpr.RebaseGateRunInput{Branch: gateTestBranch, DefaultBranch: "", Patches: []*schemas.DocPatch{gateTestPatch(gateTestDoc)}, ExpectedBaseContentHashes: map[string]string{gateTestDoc: docutil.ContentHash(gateBaseDocVar)}})
	require.ErrorContains(t, err, "rebase gate run: defaultBranch must be a nonempty string")

	_, err = gate.Run(context.Background(), gitpr.RebaseGateRunInput{Branch: gateTestBranch, DefaultBranch: gateTestDefault, ExpectedBaseContentHashes: map[string]string{}})
	require.ErrorContains(t, err, "rebase gate run: patches must be non-empty")

	badPatch := gateTestPatch("")
	_, err = gate.Run(context.Background(), gitpr.RebaseGateRunInput{Branch: gateTestBranch, DefaultBranch: gateTestDefault, Patches: []*schemas.DocPatch{badPatch}, ExpectedBaseContentHashes: map[string]string{}})
	require.ErrorContains(t, err, "rebase gate run: every patch must carry a doc_path")
}

func TestP6T14_DepsValidationIsFailClosed(t *testing.T) {
	_, err := gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport { return nil },
	})
	require.ErrorContains(t, err, "createRebaseGate: deps.git must be a GitClient")

	_, err = gitpr.CreateRebaseGate(gitpr.RebaseGateDeps{Git: newGateFakeGit()})
	require.ErrorContains(t, err, "createRebaseGate: deps.reverify must be a function")
}

/* ------------------------------------------------------------- no-op drop (P6-T15) */

func TestP6T15_AlreadyFixedDriftDropsNoop(t *testing.T) {
	// The head content already carries the EXACT fix the patch proposes (a
	// prior doc PR merged it upstream): the engine's simulated application is
	// byte-identical → strict no-op drop.
	result, err := gitpr.EvaluateNoOpDrop(context.Background(), gitpr.EvaluateNoOpDropInput{
		Patch:       gateTestPatch(gateTestDoc),
		HeadContent: gatePatchedDoc,
		BaseContent: &gateBaseDocVar,
	})
	require.NoError(t, err)
	require.True(t, result.Noop)
	require.Equal(t, gitpr.NoopDropDetail, result.Detail)
	require.Contains(t, result.Detail, "PLAN §12.3")
}

func TestP6T15_GenuineDriftIsNotANoop(t *testing.T) {
	// Real stale content: the patch APPLIES and changes the head → not a no-op.
	stale, err := gitpr.EvaluateNoOpDrop(context.Background(), gitpr.EvaluateNoOpDropInput{
		Patch:       gateTestPatch(gateTestDoc),
		HeadContent: gateBaseDocVar,
	})
	require.NoError(t, err)
	require.False(t, stale.Noop)
	require.Contains(t, stale.Detail, "genuine drift")

	// Boundary (strict rule): the head content drifted elsewhere (raced, no
	// base snapshot supplied) — the apply is INCONCLUSIVE, never guessed into an
	// "already fixed" no-op; that classification belongs to the rebase gate's
	// 3-way + re-verification path.
	raced, err := gitpr.EvaluateNoOpDrop(context.Background(), gitpr.EvaluateNoOpDropInput{
		Patch:       gateTestPatch(gateTestDoc),
		HeadContent: gateUpstreamDoc,
	})
	require.NoError(t, err)
	require.False(t, raced.Noop)
	require.Contains(t, raced.Detail, "NOT treated as already-fixed")
	require.Contains(t, raced.Detail, "rebase-gate territory")

	// The same raced head WITH the base snapshot is the rebase gate's 3-way:
	// without a reVerify port the engine cannot ship it, so the simulation
	// abstains — still not a no-op (fail closed).
	racedWithBase, err := gitpr.EvaluateNoOpDrop(context.Background(), gitpr.EvaluateNoOpDropInput{
		Patch:       gateTestPatch(gateTestDoc),
		HeadContent: gateUpstreamDoc,
		BaseContent: &gateBaseDocVar,
	})
	require.NoError(t, err)
	require.False(t, racedWithBase.Noop)
	require.Contains(t, racedWithBase.Detail, "NOT treated as already-fixed")
	require.Contains(t, racedWithBase.Detail, "rebase-gate territory")
}

func TestP6T15_NoopDropInputValidation(t *testing.T) {
	_, err := gitpr.EvaluateNoOpDrop(context.Background(), gitpr.EvaluateNoOpDropInput{
		HeadContent: gateBaseDocVar,
	})
	require.ErrorContains(t, err, "evaluateNoOpDrop input.patch must be a DocPatch (doc_path present)")

	// The head content is the ONLY input the simulation reads: no repository, no
	// git client and no reVerify port is accepted, so the engine can never fire
	// a git read (the NEVER_READ_GIT tripwire stays untriggered).
	result, err := gitpr.EvaluateNoOpDrop(context.Background(), gitpr.EvaluateNoOpDropInput{
		Patch:       gateTestPatch(gateTestDoc),
		HeadContent: gatePatchedDoc,
		BaseContent: &gateBaseDocVar,
	})
	require.NoError(t, err)
	require.True(t, result.Noop)
}

func TestP6T15_EngineConfigErrorsPropagate(t *testing.T) {
	// A patch without a baseline hash cannot be reasoned about: the engine's
	// typed configuration error propagates (fail closed), no outcome is guessed.
	broken := gateTestPatch(gateTestDoc)
	broken.BaseContentHash = ""
	_, err := gitpr.EvaluateNoOpDrop(context.Background(), gitpr.EvaluateNoOpDropInput{
		Patch:       broken,
		HeadContent: gateBaseDocVar,
	})
	require.Error(t, err)
	var engineErr *patch.PatchEngineError
	require.ErrorAs(t, err, &engineErr)
	assert.Equal(t, docutil.CodeConfig, engineErr.Code())
}
