package gitpr

// Ported from core/src/gitpr/rebase-gate.ts (P6-T14; PLAN §7.3 "Merge-order
// conflicts & PR lifecycle" lines 1159-1160; ARCH §12.3; A9).
//
// Before a doc PR is pushed, the doc branch is rebased onto the current default
// branch; if the target document changed since the writer read it, the 3-way
// merge ships ONLY when the merged result RE-PASSES verification — a failed
// merge/re-verification ABSTAINS to REVIEW, never force-applies (A9; invariant:
// the working tree is left with no staged/leftover changes).
//
// Flow (fail-closed ordering — every read and validation runs BEFORE the first
// mutating git op):
//
//  1. Clean-tree pre-flight: `git status --porcelain` must be empty — a dirty
//     tree is never rebased (abstain 'dirty-worktree').
//  2. Per-patch pre-reads at the CURRENT refs: the doc content at the
//     default-branch head (the rebase target's view) and, for the race path,
//     the writer's base snapshot recovered from the merge-base of the branch
//     and the default branch (where the pipeline read the document). The
//     snapshot must hash to the patch's `base_content_hash` — anything else is
//     an unrecoverable baseline (abstain 'base-content-unavailable').
//  3. Rebase via the git client's RebaseBranch; a conflict is aborted
//     internally by the client (restoring the branch), the tree is asserted
//     clean again, and the gate abstains 'rebase-conflict' — NEVER a forced
//     apply (A9). The branch ref is byte-identical to its pre-gate state.
//  4. Per patch, against the post-rebase world:
//     - default-head content hash = the expected base hash → the document did
//     not change upstream → the patch rides as-is;
//     - different → upstream changed: the 3-way runs THROUGH the Phase-3
//     engine's race path (patch.ApplyDocPatch with the writer's base snapshot),
//     whose injected ReVerify port is this gate's RebaseGateDeps.Reverify — the
//     merged content ships only on `verdict: 'pass'` AND `auto_eligible` (A9). A
//     passing merged result is re-applied to the branch (write → stage exact
//     path → scope assert → commit); a byte-identical branch state skips the
//     commit (the rebase itself already produced the merged content).
//  5. A final clean-tree assert closes the run: an abstaining or finishing gate
//     leaves the working tree untouched — no staged leftovers (an assertion
//     failure is a bug class → typed RebaseGateError, fail loud).
//
// The gate is deterministic orchestrator code: no model calls here — the only
// intelligence is the injected reverify seam (the verification engine's pass).
// The clock is injected (P19) and only stamps the engine's provenance field; no
// wall clock is read.
//
// The 'clean' outcome means the branch already contains the default-branch head
// (merge-base = default head): there is nothing to rebase and the pipeline
// produced its patches against that state — the gate does nothing.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ------------------------------------------------------------------- errors */

// EDOCSYNCRebaseGate preserves the TS static `RebaseGateError.code`
// (EDOCSYNC_REBASE_GATE), which internal/docutil does not list. Per
// CONVENTIONS.md §Errors the code string is preserved on the Go error type
// instead of inventing a new docutil code.
const EDOCSYNCRebaseGate = "EDOCSYNC_REBASE_GATE"

// RebaseGateError is the fail-closed refusal of a malformed gate
// configuration/input, or an internal-invariant violation (e.g. the post-abort
// tree is not clean). Semantic outcomes are RebaseGateResult values, never this
// error.
type RebaseGateError struct {
	Msg string
}

// Error reproduces the TS message text.
func (e *RebaseGateError) Error() string { return e.Msg }

// Code exposes the machine-readable failure class (TS: static/instance `code`).
func (e *RebaseGateError) Code() string { return EDOCSYNCRebaseGate }

// gateConfigError is a malformed configuration/input refusal (TS: the same
// RebaseGateError class; the patch engine maps its own config errors to
// docutil.CodeConfig, so the gate does too for its input validation).
func gateConfigError(msg string) *RebaseGateError { return &RebaseGateError{Msg: msg} }

/* -------------------------------------------------------------------- types */

// RebaseGateAbstainReason is why the gate abstained (A9 — every reason routes
// to REVIEW, never force).
type RebaseGateAbstainReason string

// The five abstain reasons (TS: the closed RebaseGateAbstainReason union).
const (
	RebaseGateAbstainRebaseConflict         RebaseGateAbstainReason = "rebase-conflict"
	RebaseGateAbstainReverifyFailed         RebaseGateAbstainReason = "reverify-failed"
	RebaseGateAbstainRaceUnresolved         RebaseGateAbstainReason = "race-unresolved"
	RebaseGateAbstainBaseContentUnavailable RebaseGateAbstainReason = "base-content-unavailable"
	RebaseGateAbstainDirtyWorktree          RebaseGateAbstainReason = "dirty-worktree"
)

// RebaseGateOutcome is the tag of the closed RebaseGateResult union.
type RebaseGateOutcome string

// The three gate outcomes (TS: RebaseGateResult['outcome']).
const (
	RebaseGateOutcomeClean             RebaseGateOutcome = "clean"
	RebaseGateOutcomeRebasedReverified RebaseGateOutcome = "rebased-reverified"
	RebaseGateOutcomeAbstained         RebaseGateOutcome = "abstained"
)

// RebaseGateResult is the result of RebaseGate.Run (closed union rendered as
// one tagged struct — the repo's discriminated-union convention).
type RebaseGateResult struct {
	Outcome RebaseGateOutcome
	// Patches is populated only for RebaseGateOutcomeRebasedReverified: the
	// surviving candidate patches (no-op drops are not carried).
	Patches []*schemas.DocPatch
	// Reason is populated only for RebaseGateOutcomeAbstained.
	Reason RebaseGateAbstainReason
	// Detail is populated only for RebaseGateOutcomeAbstained (evidence).
	Detail string
}

// ReverifyPort is the verification seam: the verification engine's re-pass over
// merged content. A nil return is a port bug and is treated as a failing
// verdict (fail closed).
type ReverifyPort func(patch *schemas.DocPatch, headContent string) *schemas.VerificationReport

// RebaseGateGitClient is the typed git access the gate consumes (TS:
// GitClient). *gitx.GitClient satisfies it directly; tests inject a double.
type RebaseGateGitClient interface {
	Root() string
	CatFile(ctx context.Context, rev, path string) (gitx.CatFileResult, error)
	RebaseBranch(ctx context.Context, branch, onto string) (gitx.RebaseBranchResult, error)
	StatusPorcelain(ctx context.Context) ([]string, error)
	StagePaths(ctx context.Context, paths []string) error
	Commit(ctx context.Context, message string) (string, error)
	MergeBase(ctx context.Context, a, b string) (string, error)
	ResolveRev(ctx context.Context, ref string) (string, error)
	CachedFilePaths(ctx context.Context) ([]string, error)
}

// RebaseGateDeps are the deps of CreateRebaseGate.
type RebaseGateDeps struct {
	// Git is typed git access (reads + the scoped rebase/status write plumbing).
	Git RebaseGateGitClient
	// Reverify is the re-verification pass: given the raced patch and the MERGED
	// head content, returns the verification report the merge must satisfy. A
	// merged result re-passes ONLY with `verdict: 'pass'` AND `auto_eligible` —
	// anything else abstains (A9).
	Reverify ReverifyPort
	// ManifestAllowlist is the MANIFEST-derived writable-path allowlist handed
	// to the Phase-3 engine for the race path (defense in depth). OPTIONAL: the
	// patches reaching this gate were already allowlist-gated upstream, so the
	// default is a self-scoped set over the candidates' own doc paths — it can
	// never WIDEN anything (the gate invents no new paths and a guard-refused
	// outcome still abstains, fail closed). Pass the manifest-derived set when
	// available.
	ManifestAllowlist map[string]bool
	// Apply is the disk seam for re-applying merged content (defaults to writing
	// the repo file through the OS). Same contract as commit-assembly's seam.
	Apply DocFileApplier
	// Now is the INJECTED clock feeding the engine's provenance stamp (P19 —
	// never a local clock). Defaults to a fixed epoch: the gate's outcomes are
	// not persisted; the stamp exists to satisfy the engine contract.
	Now func() string
}

// RebaseGateRunInput is the input of RebaseGate.Run.
type RebaseGateRunInput struct {
	// Branch is the doc branch to rebase (the PR write path's branch).
	Branch string `json:"branch"`
	// DefaultBranch is the default branch to rebase ONTO.
	DefaultBranch string `json:"default_branch"`
	// Patches are the verified candidate patches riding the branch.
	Patches []*schemas.DocPatch `json:"patches"`
	// ExpectedBaseContentHashes: per doc path, sha256 (docutil.ContentHash) of
	// the document content the writer read — the baseline the rebase is measured
	// against. In healthy pipelines this equals the patch's
	// `base_content_hash`.
	ExpectedBaseContentHashes map[string]string `json:"expected_base_content_hashes"`
}

// RebaseGate is the P6-T14 rebase gate surface.
type RebaseGate struct {
	deps RebaseGateDeps
	now  func() string
}

// gateConstantNow is the fixed epoch for the default clock (P19: no wall clock
// in the gate).
func gateConstantNow() string {
	return "1970-01-01T00:00:00.000Z"
}

// gateDefaultFileApplier is the default disk seam: write the repo-relative file
// (parents created).
func gateDefaultFileApplier(repoRoot string) DocFileApplier {
	return func(relPath, content string) error {
		target := filepath.Join(repoRoot, relPath)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(content), 0o644)
	}
}

// gateReApplyCommitMessage is the deterministic re-apply commit message
// (INV-10 spirit: provenance line).
func gateReApplyCommitMessage(patch *schemas.DocPatch) string {
	return strings.Join([]string{
		"docs: re-apply documentation after rebase",
		"",
		"The documentation branch was rebased onto the default branch and this",
		"document changed upstream since the writer read it. The merged result",
		"re-passed verification (A9; P6-T14 rebase gate).",
		"",
		"Documentation updates:",
		"- " + patch.DocPath,
		"",
		"Provenance: " + patch.RunID,
	}, "\n")
}

// gateReverifyVerdictDetail renders the verdict evidence the abstain detail
// carries (A9).
func gateReverifyVerdictDetail(report *schemas.VerificationReport) string {
	autoEligible := "false"
	if report.AutoEligible {
		autoEligible = "true"
	}
	return "verdict '" + string(report.Verdict) + "', auto_eligible=" + autoEligible +
		", auto_block_reasons=[" + strings.Join(report.AutoBlockReasons, ", ") + "]"
}

// gateReverifyPass is the A9 ship rule: `verdict: 'pass'` AND `auto_eligible`.
func gateReverifyPass(report *schemas.VerificationReport) bool {
	return report != nil && report.Verdict == schemas.VerdictPass && report.AutoEligible
}

/* --------------------------------------------------------------- the gate */

// CreateRebaseGate builds the P6-T14 rebase gate. Typed, fail-closed validation
// of the deps; every semantic outcome is a RebaseGateResult (the gate never
// errors for rebase/race/verification outcomes — only a misconfiguration or a
// broken internal invariant errors, typed RebaseGateError).
func CreateRebaseGate(deps RebaseGateDeps) (*RebaseGate, error) {
	if deps.Git == nil {
		return nil, gateConfigError(
			"createRebaseGate: deps.git must be a GitClient (catFile/rebaseBranch/statusPorcelain/stagePaths/commit/mergeBase/resolveRev)")
	}
	if deps.Reverify == nil {
		return nil, gateConfigError("createRebaseGate: deps.reverify must be a function")
	}
	if deps.Now == nil {
		deps.Now = gateConstantNow
	}
	return &RebaseGate{deps: deps, now: deps.Now}, nil
}

// gatePreflight is the per-patch pre-read pair the race path needs.
type gatePreflight struct {
	docPatch    *schemas.DocPatch
	headContent string
	baseContent string
}

// Run runs the gate (see the module doc for the fail-closed ordering).
func (g *RebaseGate) Run(ctx context.Context, input RebaseGateRunInput) (RebaseGateResult, error) {
	if input.Branch == "" {
		return RebaseGateResult{}, gateConfigError(
			"rebase gate run: branch must be a nonempty string")
	}
	if input.DefaultBranch == "" {
		return RebaseGateResult{}, gateConfigError(
			"rebase gate run: defaultBranch must be a nonempty string")
	}
	if len(input.Patches) == 0 {
		return RebaseGateResult{}, gateConfigError(
			"rebase gate run: patches must be non-empty (the gate exists to gate patches — an empty run is a caller bug, fail closed)")
	}
	for _, candidate := range input.Patches {
		if candidate == nil || candidate.DocPath == "" {
			return RebaseGateResult{}, gateConfigError(
				"rebase gate run: every patch must carry a doc_path")
		}
	}

	git := g.deps.Git
	expected := input.ExpectedBaseContentHashes

	/* -- 1. clean-tree pre-flight (a dirty tree is never rebased) ---------- */
	dirty, err := git.StatusPorcelain(ctx)
	if err != nil {
		return RebaseGateResult{}, err
	}
	if len(dirty) > 0 {
		return RebaseGateResult{
			Outcome: RebaseGateOutcomeAbstained,
			Reason:  RebaseGateAbstainDirtyWorktree,
			Detail: "working tree is not clean (" + strconv.Itoa(len(dirty)) +
				" status entr(y/ies), first: " + gateJSONString(dirty[0]) +
				") — refusing to rebase a dirty tree (fail closed)",
		}, nil
	}

	/* -- 2. pre-reads + validations, ALL before the first mutating op ------ */
	defaultHeadSha, err := git.ResolveRev(ctx, input.DefaultBranch)
	if err != nil {
		return RebaseGateResult{}, err
	}
	mergeBaseSha, err := git.MergeBase(ctx, input.Branch, input.DefaultBranch)
	if err != nil {
		return RebaseGateResult{}, err
	}
	if mergeBaseSha == "" {
		return RebaseGateResult{
			Outcome: RebaseGateOutcomeAbstained,
			Reason:  RebaseGateAbstainBaseContentUnavailable,
			Detail: "branch '" + input.Branch + "' and '" + input.DefaultBranch +
				"' have no common ancestor (unrelated histories) — a rebase cannot be reasoned about, abstaining",
		}, nil
	}
	if mergeBaseSha == defaultHeadSha {
		// The branch already contains the default-branch head: nothing to
		// rebase, the pipeline produced its patches against this state.
		return RebaseGateResult{Outcome: RebaseGateOutcomeClean}, nil
	}

	preflights := map[string]gatePreflight{}
	racedDocPaths := map[string]bool{}
	for _, candidate := range input.Patches {
		expectedHash, ok := expected[candidate.DocPath]
		if !ok {
			return RebaseGateResult{
				Outcome: RebaseGateOutcomeAbstained,
				Reason:  RebaseGateAbstainBaseContentUnavailable,
				Detail: "no expected base content hash for doc '" + candidate.DocPath +
					"' in expectedBaseContentHashes — the upstream-change race cannot be classified (fail closed)",
			}, nil
		}
		if candidate.BaseContentHash != expectedHash {
			return RebaseGateResult{
				Outcome: RebaseGateOutcomeAbstained,
				Reason:  RebaseGateAbstainBaseContentUnavailable,
				Detail: "patch base_content_hash for doc '" + candidate.DocPath +
					"' disagrees with the expected base content hash entry — inconsistent baseline, refusing to classify the race (fail closed)",
			}, nil
		}
		headContent, err := git.CatFile(ctx, input.DefaultBranch, candidate.DocPath)
		if err != nil {
			if isMissingRev(err) {
				return RebaseGateResult{
					Outcome: RebaseGateOutcomeAbstained,
					Reason:  RebaseGateAbstainRaceUnresolved,
					Detail: "doc '" + candidate.DocPath + "' no longer exists at the default-branch head '" +
						input.DefaultBranch + "' — the document was changed (deleted) upstream; the 3-way cannot run (A9: abstains to REVIEW, never force-applies)",
				}, nil
			}
			return RebaseGateResult{}, err
		}
		if docutil.ContentHash(headContent.Content) == expectedHash {
			// The document did not change upstream: the patch rides as-is.
			continue
		}
		// Upstream changed the document since the writer read it: recover the
		// writer's base snapshot from the merge-base (where the pipeline read it).
		if racedDocPaths[candidate.DocPath] {
			return RebaseGateResult{
				Outcome: RebaseGateOutcomeAbstained,
				Reason:  RebaseGateAbstainRaceUnresolved,
				Detail: "two racing patches target doc '" + candidate.DocPath +
					"' — one patch per document per run (fail closed: the second merge could not be grounded)",
			}, nil
		}
		racedDocPaths[candidate.DocPath] = true
		baseContent, err := git.CatFile(ctx, mergeBaseSha, candidate.DocPath)
		if err != nil {
			if isMissingRev(err) {
				return RebaseGateResult{
					Outcome: RebaseGateOutcomeAbstained,
					Reason:  RebaseGateAbstainBaseContentUnavailable,
					Detail: "the writer's base snapshot for doc '" + candidate.DocPath +
						"' is not readable at the merge-base of '" + input.Branch + "' and '" +
						input.DefaultBranch + "' — the race cannot be grounded (fail closed)",
				}, nil
			}
			return RebaseGateResult{}, err
		}
		if docutil.ContentHash(baseContent.Content) != candidate.BaseContentHash {
			return RebaseGateResult{
				Outcome: RebaseGateOutcomeAbstained,
				Reason:  RebaseGateAbstainBaseContentUnavailable,
				Detail: "content at the merge-base of '" + input.Branch + "' and '" + input.DefaultBranch +
					"' does not hash to the patch's base_content_hash for doc '" + candidate.DocPath +
					"' — the writer's base snapshot is unrecoverable (fail closed; A9: abstains to REVIEW)",
			}, nil
		}
		preflights[candidate.DocPath] = gatePreflight{
			docPatch:    candidate,
			headContent: headContent.Content,
			baseContent: baseContent.Content,
		}
	}

	/* -- 3. rebase (conflict → aborted + asserted clean + abstain) --------- */
	rebase, err := git.RebaseBranch(ctx, input.Branch, input.DefaultBranch)
	if err != nil {
		return RebaseGateResult{}, err
	}
	if !rebase.Ok {
		if err := g.assertTreeClean(ctx, "git rebase --abort"); err != nil {
			return RebaseGateResult{}, err
		}
		return RebaseGateResult{
			Outcome: RebaseGateOutcomeAbstained,
			Reason:  RebaseGateAbstainRebaseConflict,
			Detail: "rebasing '" + input.Branch + "' onto '" + input.DefaultBranch +
				"' conflicts — the rebase was aborted (branch restored byte-identical), nothing is force-applied (A9: abstains to REVIEW)",
		}, nil
	}

	/* -- 4. per raced patch: engine 3-way + reverify + re-apply ------------ */
	apply := g.deps.Apply
	if apply == nil {
		apply = gateDefaultFileApplier(git.Root())
	}
	survivors := make([]*schemas.DocPatch, 0, len(input.Patches))
	for _, candidate := range input.Patches {
		preflight, raced := preflights[candidate.DocPath]
		if !raced {
			survivors = append(survivors, candidate)
			continue
		}

		var lastReport *schemas.VerificationReport
		reVerify := func(mergedContent string, racePatch *schemas.DocPatch) patch.ReVerifyResult {
			report := g.deps.Reverify(racePatch, mergedContent)
			lastReport = report
			pass := gateReverifyPass(report)
			detail := ""
			if pass {
				detail = "merged result re-passed verification (verdict pass, auto_eligible)"
			} else {
				detail = "merged result did NOT re-pass verification (" +
					gateReverifyVerdictDetail(gateReportOrNil(report)) + ")"
			}
			return patch.ReVerifyResult{Pass: pass, Detail: detail}
		}
		allowlist := g.deps.ManifestAllowlist
		if allowlist == nil {
			allowlist = map[string]bool{candidate.DocPath: true}
		}
		outcome, err := patch.ApplyDocPatch(ctx, patch.ApplyDocPatchInput{
			Patch:             candidate,
			Git:               git,
			Rev:               input.DefaultBranch,
			WorktreeContent:   &preflight.headContent,
			ManifestAllowlist: allowlist,
			BaseContent:       &preflight.baseContent,
			Now:               g.now,
			ReVerify:          reVerify,
		})
		if err != nil {
			return RebaseGateResult{}, err
		}

		if outcome.Kind == patch.OutcomeApplied {
			merged := outcome.ResultContent
			branchContent, err := git.CatFile(ctx, input.Branch, candidate.DocPath)
			if err != nil {
				return RebaseGateResult{}, err
			}
			if branchContent.Content != merged {
				// The rebase's own merge did not land the engine-verified
				// content — re-apply it exactly: write, stage the EXACT path,
				// scope-assert, commit (no sweep, no force).
				if err := apply(candidate.DocPath, merged); err != nil {
					return RebaseGateResult{}, err
				}
				if err := git.StagePaths(ctx, []string{candidate.DocPath}); err != nil {
					return RebaseGateResult{}, err
				}
				staged, err := git.CachedFilePaths(ctx)
				if err != nil {
					return RebaseGateResult{}, err
				}
				if len(staged) != 1 || staged[0] != candidate.DocPath {
					return RebaseGateResult{}, gateConfigError(
						"re-apply scope assert failed: staged [" + strings.Join(staged, ", ") +
							"] instead of exactly '" + candidate.DocPath +
							"' — commit aborted (foreign or missing staged content)")
				}
				if _, err := git.Commit(ctx, gateReApplyCommitMessage(candidate)); err != nil {
					return RebaseGateResult{}, err
				}
			}
			survivors = append(survivors, candidate)
			continue
		}
		if outcome.Kind == patch.OutcomeNoop {
			// The drifted document already carries the patch's full effect — the
			// drift is already fixed upstream; the patch contributes nothing.
			// Dropped from the result (the P6-T15 evaluator is the reporting
			// surface for the no-op reason).
			continue
		}

		if err := g.assertTreeClean(ctx, "the abstaining race path"); err != nil {
			return RebaseGateResult{}, err
		}
		engineDetail := ""
		if outcome.Kind == patch.OutcomeUnanchored {
			reasons := make([]string, 0, len(outcome.UnanchoredHunks))
			for _, hunk := range outcome.UnanchoredHunks {
				reasons = append(reasons, "hunk "+strconv.Itoa(hunk.Index)+": "+hunk.Reason)
			}
			engineDetail = "anchors do not resolve against the merged basis: " + strings.Join(reasons, "; ")
		} else {
			engineDetail = string(outcome.AbstainReason) + ": " + outcome.Detail
		}
		// The TS reads the report THROUGH the closure helper: a report is
		// present only when the engine actually ran the reVerify seam. An engine
		// abstention that never reached reVerify (e.g. a conflicting 3-way) is a
		// RACE outcome, not a verification failure.
		if lastReport != nil && !gateReverifyPass(lastReport) {
			return RebaseGateResult{
				Outcome: RebaseGateOutcomeAbstained,
				Reason:  RebaseGateAbstainReverifyFailed,
				Detail: "the merged result of doc '" + candidate.DocPath +
					"' did not re-pass verification (" + gateReverifyVerdictDetail(gateReportOrNil(lastReport)) +
					") — A9: abstains to REVIEW, never force-applies; working tree untouched",
			}, nil
		}
		return RebaseGateResult{
			Outcome: RebaseGateOutcomeAbstained,
			Reason:  RebaseGateAbstainRaceUnresolved,
			Detail: "the upstream-change race for doc '" + candidate.DocPath +
				"' could not be resolved safely (" + engineDetail +
				") — A9: abstains to REVIEW, never force-applies; working tree untouched",
		}, nil
	}

	/* -- 5. closing clean-tree assert + result ------------------------------ */
	if err := g.assertTreeClean(ctx, "the finished rebase gate"); err != nil {
		return RebaseGateResult{}, err
	}
	return RebaseGateResult{
		Outcome: RebaseGateOutcomeRebasedReverified,
		Patches: survivors,
	}, nil
}

// assertTreeClean is the invariant that closes every path of the gate: an
// abstaining or finishing gate leaves no staged/leftover changes.
func (g *RebaseGate) assertTreeClean(ctx context.Context, when string) error {
	entries, err := g.deps.Git.StatusPorcelain(ctx)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return gateConfigError(
			"rebase gate internal invariant violated: " + when + " left " +
				strconv.Itoa(len(entries)) +
				" status entr(y/ies) behind — no staged/leftover changes are ever acceptable (first: " +
				gateJSONString(entries[0]) + ")")
	}
	return nil
}

// gateReportOrNil keeps the verdict-detail renderer total: a nil report (the
// port returned nothing — a Go-only port bug) renders as an empty verdict
// instead of panicking.
func gateReportOrNil(report *schemas.VerificationReport) *schemas.VerificationReport {
	if report == nil {
		return &schemas.VerificationReport{}
	}
	return report
}

// gateJSONString mirrors the JSON.stringify the TS used for status entries.
func gateJSONString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "\"" + value + "\""
	}
	return string(raw)
}

// isMissingRev reports whether a git failure is the typed missing-rev kind
// (TS: `error instanceof GitExecError && error.kind === 'missing-rev'`).
func isMissingRev(err error) bool {
	gitErr, ok := gitx.AsGitExecError(err)
	return ok && gitErr.Kind == gitx.KindMissingRev
}
