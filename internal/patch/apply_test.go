package patch_test

// Black-box port of test/patch-apply.test.ts (P3-T08..T12, P3-T16; ARCH §4.3,
// §4.9, §11.4 layer 3, §12.1/§12.3, A9; invariants 2/3/12/13), plus:
//   - test/patch-claim-refs.test.ts (V-P11-T15: verified_claim_refs survival),
//   - test/patch-purity.test.ts (static import audit → Go import audit): the
//     engine's own source imports nothing write-capable/network/model.
//
// Named verification gates:
//   - V-P3-2: a full-document-replacement patch is rejected structurally before
//     application (rewrite-rejected abstain, not an error);
//   - V-P3-3: unresolvable/ambiguous anchor → abstain UNANCHORED, never a silent
//     re-anchor (invariant 13);
//   - V-P3-4: doc-changed race → abstain without a re-verified merge; with a
//     reVerify pass → applied (ThreeWayUsed); adversarial Markdown fixtures;
//   - V-P3-5: idempotency — a second apply of an identical patch is a no-op
//     ('already-applied'); run-id port skip;
//   - V-P3-7: guard refusals — code-file, .docsync/-internal, locked-doc and
//     generated/external targets each refused (invariants 2/3), the allowlist
//     computed from the MANIFEST param, never from the patch.

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

var (
	writable                 = map[string]bool{"docs/payments.md": true}
	lockedProtected          = map[string]bool{"docs/security.md": true}
	generatedExternalProtect = map[string]bool{"docs/generated-api.md": true, "docs/unlisted.md": true}
)

// baseApplyInput builds the default engine input (git reads the worktree doc;
// allowlist from the manifest; injected clock). Callers mutate fields (e.g.
// WorktreeContent) before calling ApplyDocPatch.
func baseApplyInput(p *schemas.DocPatch) patch.ApplyDocPatchInput {
	return patch.ApplyDocPatchInput{
		Patch:             p,
		Git:               newCatFileDouble(doc, nil),
		Rev:               "HEAD",
		ManifestAllowlist: writable,
		Now:               NOW,
	}
}

func mustApply(t *testing.T, in patch.ApplyDocPatchInput) patch.ApplyOutcome {
	t.Helper()
	o, err := patch.ApplyDocPatch(context.Background(), in)
	require.NoError(t, err)
	return o
}

func requirePatchEngineError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var pe *patch.PatchEngineError
	require.True(t, errors.As(err, &pe), "expected *PatchEngineError, got %T: %v", err, err)
}

/* ------------------------------------------------------------ V-P3-2 gate */

func TestApplyV32FullDocumentRewriteStructurallyRejected(t *testing.T) {
	bodyLines := make([]string, 10)
	for i := range bodyLines {
		bodyLines[i] = "Body line " + strconv.Itoa(i+1) + "."
	}
	bodyDoc := strings.Join(bodyLines, "\n") + "\n"

	t.Run("a writer double returning a body-length delete_text trips the structural rule → abstain, not error", func(t *testing.T) {
		// 9 of 10 body lines inside one delete_text (>= 0.9 of non-empty lines).
		p := makeDocPatch(docPatchOpts{
			baseContent: bodyDoc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 2, deleteText: strings.Join(bodyLines[1:], "\n"), insert: "Fresh body."}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(bodyDoc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Equal(t, patch.AbstainRewriteRejected, o.AbstainReason)
		require.Contains(t, o.Detail, "deletion_span")
		require.Contains(t, o.Detail, "0.9")
	})

	t.Run("the abstain detail names the rule and measured ratio; a borderline patch (below the ratio) applies", func(t *testing.T) {
		rewriting := makeDocPatch(docPatchOpts{
			baseContent: bodyDoc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 2, deleteText: strings.Join(bodyLines[1:], "\n"), insert: "Fresh body."}),
			},
		})
		in := baseApplyInput(rewriting)
		in.WorktreeContent = strp(bodyDoc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Equal(t, patch.AbstainRewriteRejected, o.AbstainReason)
		require.Contains(t, o.Detail, "deletion_span")
		require.Contains(t, o.Detail, "0.9")

		// 2 of 10 lines replaced (0.4 < 0.9; structural diff 0.4 < 0.9) applies.
		small := makeDocPatch(docPatchOpts{
			baseContent: bodyDoc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 2, deleteText: bodyLines[1], insert: "Body line 2 (updated)."}),
				hunk(hunkOpts{start: 5, deleteText: bodyLines[4], insert: "Body line 5 (updated)."}),
			},
		})
		smallIn := baseApplyInput(small)
		smallIn.WorktreeContent = strp(bodyDoc)
		require.Equal(t, patch.OutcomeApplied, mustApply(t, smallIn).Kind)
	})
}

/* ------------------------------------------------------------ V-P3-3 gate */

func TestApplyV33UnanchoredAbstainsNeverSilent(t *testing.T) {
	t.Run("content matching nowhere, ambiguity, out-of-bounds lines and out-of-bounds insertion each abstain", func(t *testing.T) {
		nowhere := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 3, deleteText: "This sentence is not in the document at all."})},
		})
		nowhereIn := baseApplyInput(nowhere)
		nowhereIn.WorktreeContent = strp(doc)
		nowhereOut := mustApply(t, nowhereIn)
		require.Equal(t, patch.OutcomeUnanchored, nowhereOut.Kind)
		require.Equal(t, patch.DocStateSignalUnanchored, nowhereOut.DocStateSignal)
		require.Equal(t, []patch.UnanchoredHunk{
			{Index: 0, Reason: "content hash matches nowhere — doc edited or delete_text misquoted"},
		}, nowhereOut.UnanchoredHunks)

		duplicatedDoc := strings.Repeat("# Payments\n\nRefunds require an idempotency key.\n", 2)
		ambiguous := makeDocPatch(docPatchOpts{
			baseContent: duplicatedDoc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 3, deleteText: "Refunds require an idempotency key."})},
		})
		ambiguousIn := baseApplyInput(ambiguous)
		ambiguousIn.WorktreeContent = strp(duplicatedDoc)
		ambiguousOut := mustApply(t, ambiguousIn)
		require.Equal(t, patch.OutcomeUnanchored, ambiguousOut.Kind)
		require.Contains(t, ambiguousOut.UnanchoredHunks[0].Reason, "ambiguous — 2 identical spans")

		outOfBounds := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 99, deleteText: docLines[2]})},
		})
		oobIn := baseApplyInput(outOfBounds)
		oobIn.WorktreeContent = strp(doc)
		oobOut := mustApply(t, oobIn)
		require.Equal(t, patch.OutcomeUnanchored, oobOut.Kind)
		require.Contains(t, oobOut.UnanchoredHunks[0].Reason, "outside the document bounds")

		insertion := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 8, deleteText: "", insert: "Too far down."})},
		})
		insIn := baseApplyInput(insertion)
		insIn.WorktreeContent = strp(doc)
		insOut := mustApply(t, insIn)
		require.Equal(t, patch.OutcomeUnanchored, insOut.Kind)
		require.Contains(t, insOut.UnanchoredHunks[0].Reason, "insertion point 8")
	})

	t.Run("one unanchored hunk abstains the WHOLE patch — no partial application even when other hunks resolve", func(t *testing.T) {
		p := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: docLines[2], insert: "Refunds require an idempotency key above 10 USD."}),
				hunk(hunkOpts{start: 99, deleteText: docLines[5]}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(doc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeUnanchored, o.Kind)
		require.Equal(t, []patch.UnanchoredHunk{
			{
				Index:  1,
				Reason: "recorded anchor lines 99-99 are outside the document bounds (1-" + strconv.Itoa(len(docLines)) + ")",
			},
		}, o.UnanchoredHunks)
		require.Equal(t, patch.DocStateSignalUnanchored, o.DocStateSignal)
	})
}

/* ------------------------------------------------------------ V-P3-4 gate */

func TestApplyV34DocChangedRace(t *testing.T) {
	mkRaceDoc := func() string {
		lines := append([]string(nil), docLines...)
		lines[5] = "See docs/architecture.md for the refund flow."
		return strings.Join(lines, "\n") + "\n"
	}
	mkRacePatch := func() *schemas.DocPatch {
		return makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: docLines[2], insert: "Refunds require an idempotency key above 10 USD."}),
			},
		})
	}

	t.Run("race with no base snapshot → abstain (fail closed)", func(t *testing.T) {
		in := baseApplyInput(mkRacePatch())
		in.WorktreeContent = strp(mkRaceDoc())
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Equal(t, patch.AbstainDocChangedRaceUnverified, o.AbstainReason)
		require.Contains(t, o.Detail, "no base snapshot was provided")
	})

	t.Run("race with a base snapshot but no reVerify port → abstain (merge ships only on a re-verified pass)", func(t *testing.T) {
		in := baseApplyInput(mkRacePatch())
		in.WorktreeContent = strp(mkRaceDoc())
		in.BaseContent = strp(doc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Contains(t, o.Detail, "no reVerify port was provided")
	})

	t.Run("race with a failing reVerify → abstain carrying the verifier detail", func(t *testing.T) {
		in := baseApplyInput(mkRacePatch())
		in.WorktreeContent = strp(mkRaceDoc())
		in.BaseContent = strp(doc)
		in.ReVerify = func(_ string, _ *schemas.DocPatch) patch.ReVerifyResult {
			return patch.ReVerifyResult{Pass: false, Detail: "Tier-2 evidence check failed"}
		}
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Contains(t, o.Detail, "Tier-2 evidence check failed")
	})

	t.Run("race with a passing reVerify → applied with threeWayUsed: true and the merged content carries BOTH changes", func(t *testing.T) {
		in := baseApplyInput(mkRacePatch())
		in.WorktreeContent = strp(mkRaceDoc())
		in.BaseContent = strp(doc)
		in.ReVerify = func(_ string, _ *schemas.DocPatch) patch.ReVerifyResult { return patch.ReVerifyResult{Pass: true} }
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.True(t, o.ThreeWayUsed)
		require.Equal(t, "2026-10-06T10:00:00.000Z", o.AppliedAt)
		require.Equal(t,
			strings.Join([]string{
				"# Payments",
				"",
				"Refunds require an idempotency key above 10 USD.",
				"Amounts above 10 USD are held for review.",
				"",
				"See docs/architecture.md for the refund flow.",
			}, "\n")+"\n",
			o.ResultContent)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 1}, o.Minimality.Minimality)
	})

	t.Run("race whose merge conflicts (both sides changed the same line differently) → abstain, never an unresolved merge", func(t *testing.T) {
		lines := append([]string(nil), docLines...)
		lines[2] = "Refunds require an idempotency key for all amounts."
		conflictingDoc := strings.Join(lines, "\n") + "\n"
		in := baseApplyInput(mkRacePatch())
		in.WorktreeContent = strp(conflictingDoc)
		in.BaseContent = strp(doc)
		in.ReVerify = func(_ string, _ *schemas.DocPatch) patch.ReVerifyResult { return patch.ReVerifyResult{Pass: true} }
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Equal(t, patch.AbstainDocChangedRaceUnverified, o.AbstainReason)
		require.Contains(t, o.Detail, "three-way merge")
		require.Contains(t, o.Detail, "conflict")
	})

	t.Run("A9 adversarial fixture: front-matter document merges a body patch into a front-matter race", func(t *testing.T) {
		base := "---\ntitle: Payments\n---\n\nRefunds require an idempotency key.\n\nAmounts above 10 USD are held for review.\n"
		ours := rep(base, "title: Payments", "title: Payments v2")
		p := makeDocPatch(docPatchOpts{
			baseContent: base,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 7, deleteText: "Amounts above 10 USD are held for review.", insert: "Amounts above 25 USD are held for review."}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(ours)
		in.BaseContent = strp(base)
		in.ReVerify = func(_ string, _ *schemas.DocPatch) patch.ReVerifyResult { return patch.ReVerifyResult{Pass: true} }
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.True(t, o.ThreeWayUsed)
		require.Contains(t, o.ResultContent, "title: Payments v2")
		require.Contains(t, o.ResultContent, "Amounts above 25 USD are held for review.")
		require.Contains(t, o.ResultContent, "Refunds require an idempotency key.")
	})

	t.Run("A9 adversarial fixture: fenced code block — ours edits inside the fence, the patch edits after it", func(t *testing.T) {
		base := "# Runbook\n\n```bash\ndocsync check --strict\n```\n\nEscalate to the on-call rotation.\n"
		ours := rep(base, "docsync check --strict", "docsync check --strict --json")
		p := makeDocPatch(docPatchOpts{
			baseContent: base,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 7, deleteText: "Escalate to the on-call rotation.", insert: "Escalate to the secondary rotation."}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(ours)
		in.BaseContent = strp(base)
		in.ReVerify = func(_ string, _ *schemas.DocPatch) patch.ReVerifyResult { return patch.ReVerifyResult{Pass: true} }
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.Contains(t, o.ResultContent, "docsync check --strict --json")
		require.Contains(t, o.ResultContent, "Escalate to the secondary rotation.")
		require.Contains(t, o.ResultContent, "```bash")
	})

	t.Run("A9 adversarial fixture: nested lists — ours edits a sibling item, the patch edits a nested item", func(t *testing.T) {
		base := "- Top item A\n  - Nested item 1\n  - Nested item 2\n- Top item B\n"
		ours := rep(base, "- Top item B", "- Top item C")
		p := makeDocPatch(docPatchOpts{
			baseContent: base,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: "  - Nested item 2", insert: "  - Nested item 2 (renamed)"}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(ours)
		in.BaseContent = strp(base)
		in.ReVerify = func(_ string, _ *schemas.DocPatch) patch.ReVerifyResult { return patch.ReVerifyResult{Pass: true} }
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.Equal(t, "- Top item A\n  - Nested item 1\n  - Nested item 2 (renamed)\n- Top item C\n", o.ResultContent)
	})

	t.Run("a doc deleted at rev (missing blob) is a race outcome → abstain; a broken git run still throws", func(t *testing.T) {
		missing := newCatFileErr(gitx.NewGitExecError(gitx.KindMissingRev, "path does not exist in rev", &gitx.GitExecErrorOptions{Command: "rev-parse"}))
		in := baseApplyInput(mkRacePatch())
		in.Git = missing
		in.WorktreeContent = nil
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Equal(t, patch.AbstainDocChangedRaceUnverified, o.AbstainReason)
		require.Contains(t, o.Detail, "no longer exists at rev")

		broken := newCatFileErr(gitx.NewGitExecError(gitx.KindExecFailure, "git died", &gitx.GitExecErrorOptions{Command: "cat-file"}))
		brokenIn := baseApplyInput(mkRacePatch())
		brokenIn.Git = broken
		brokenIn.WorktreeContent = nil
		_, err := patch.ApplyDocPatch(context.Background(), brokenIn)
		require.Error(t, err)
		var ge *gitx.GitExecError
		require.True(t, errors.As(err, &ge), "expected *gitx.GitExecError, got %T", err)
		require.Equal(t, gitx.KindExecFailure, ge.Kind)
	})
}

/* ------------------------------------------------------------ V-P3-5 gate */

func TestApplyV35Idempotency(t *testing.T) {
	mkPatch := func() *schemas.DocPatch {
		return makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: docLines[2], insert: "Refunds require an idempotency key above 10 USD."}),
			},
		})
	}

	t.Run("apply → result; applying the SAME patch over the result is a no-op (content-level idempotency)", func(t *testing.T) {
		firstIn := baseApplyInput(mkPatch())
		firstIn.WorktreeContent = strp(doc)
		first := mustApply(t, firstIn)
		require.Equal(t, patch.OutcomeApplied, first.Kind)
		resultContent := first.ResultContent

		// Second apply: the doc now carries the patch (race vs the recorded
		// base), the writer snapshot is supplied — the engine detects its effect.
		secondIn := baseApplyInput(mkPatch())
		secondIn.WorktreeContent = strp(resultContent)
		secondIn.BaseContent = strp(doc)
		secondIn.ReVerify = func(_ string, _ *schemas.DocPatch) patch.ReVerifyResult { return patch.ReVerifyResult{Pass: true} }
		second := mustApply(t, secondIn)
		require.Equal(t, patch.OutcomeNoop, second.Kind)
		require.Equal(t, patch.NoopAlreadyApplied, second.NoopReason)
	})

	t.Run("a zero-effect patch (delete_text == insert_text) over an unchanged doc is a no-op", func(t *testing.T) {
		noop := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 3, deleteText: docLines[2], insert: docLines[2]})},
		})
		in := baseApplyInput(noop)
		in.WorktreeContent = strp(doc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeNoop, o.Kind)
		require.Equal(t, patch.NoopAlreadyApplied, o.NoopReason)
	})

	t.Run("the injected run-state port skips before any content work (run-id-skip)", func(t *testing.T) {
		in := baseApplyInput(mkPatch())
		in.WorktreeContent = strp(doc)
		in.IsRunApplied = func(_, _ string) bool { return true }
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeNoop, o.Kind)
		require.Equal(t, patch.NoopRunIDSkip, o.NoopReason)
	})

	t.Run("run-id skip precedes the race abstention (P3-T11 before P3-T10 in the outcome order)", func(t *testing.T) {
		lines := append([]string(nil), docLines...)
		lines[2] = "Totally rewritten line."
		raced := strings.Join(lines, "\n") + "\n"
		in := baseApplyInput(mkPatch())
		in.WorktreeContent = strp(raced)
		in.IsRunApplied = func(_, _ string) bool { return true }
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeNoop, o.Kind)
		require.Equal(t, patch.NoopRunIDSkip, o.NoopReason)
	})
}

/* ------------------------------------------------------------ V-P3-7 gate */

func TestApplyV37GuardRefusals(t *testing.T) {
	targets := []string{"src/payments/refund.ts", ".docsync/state/manifest.yml", ".docsync/state/runs/run-1.json"}
	for _, target := range targets {
		t.Run("a patch targeting "+target+" is refused: not in the manifest-derived writable allowlist", func(t *testing.T) {
			p := makeDocPatch(docPatchOpts{
				docPath:     target,
				baseContent: "anything the writer claims",
				hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 1, deleteText: "x", insert: "y"})},
			})
			in := baseApplyInput(p)
			in.WorktreeContent = strp(doc)
			o := mustApply(t, in)
			require.Equal(t, patch.OutcomeAbstain, o.Kind)
			require.Equal(t, patch.AbstainGuardRefused, o.AbstainReason)
			require.Contains(t, o.Detail, "manifest-derived writable allowlist")
			require.Contains(t, o.Detail, "invariant 3")
		})
	}

	protectedCases := []struct {
		target         string
		protectedPaths map[string]bool
		floor          string
	}{
		{"docs/security.md", lockedProtected, "locked"},
		{"docs/generated-api.md", generatedExternalProtect, "generated"},
		{"docs/unlisted.md", generatedExternalProtect, "external"},
	}
	for _, tc := range protectedCases {
		t.Run("a "+tc.target+"-doc target in protectedPaths is refused WHOLE even if a mis-derived allowlist admits it", func(t *testing.T) {
			p := makeDocPatch(docPatchOpts{
				docPath:     tc.target,
				baseContent: "content",
				hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 1, deleteText: "x", insert: "y"})},
			})
			in := baseApplyInput(p)
			in.WorktreeContent = strp("content\n")
			in.ManifestAllowlist = map[string]bool{tc.target: true} // mis-derived allowlist admits it
			in.ProtectedPaths = tc.protectedPaths
			o := mustApply(t, in)
			require.Equal(t, patch.OutcomeAbstain, o.Kind)
			require.Equal(t, patch.AbstainGuardRefused, o.AbstainReason)
			require.Contains(t, o.Detail, "protected")
			require.Contains(t, o.Detail, tc.floor)
			require.Contains(t, o.Detail, "rejected WHOLE")
		})
	}

	t.Run("the allowlist comes from the MANIFEST param, never from the patch: the guard runs FIRST", func(t *testing.T) {
		bodyLines := make([]string, 10)
		for i := range bodyLines {
			bodyLines[i] = "Body line " + strconv.Itoa(i+1) + "."
		}
		bodyDoc := strings.Join(bodyLines, "\n") + "\n"

		// Guard beats rewrite-precheck.
		rewritePatch := makeDocPatch(docPatchOpts{
			docPath:     "src/payments/refund.ts",
			baseContent: bodyDoc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 2, deleteText: strings.Join(bodyLines[1:], "\n"), insert: "Fresh body."})},
		})
		guardFirstIn := baseApplyInput(rewritePatch)
		guardFirstIn.WorktreeContent = strp(bodyDoc)
		guardFirst := mustApply(t, guardFirstIn)
		require.Equal(t, patch.OutcomeAbstain, guardFirst.Kind)
		require.Equal(t, patch.AbstainGuardRefused, guardFirst.AbstainReason)

		// Guard beats the run-id port.
		unlisted := makeDocPatch(docPatchOpts{
			docPath:     "docs/not-in-manifest.md",
			baseContent: doc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 3, deleteText: docLines[2]})},
		})
		unlistedIn := baseApplyInput(unlisted)
		unlistedIn.WorktreeContent = strp(doc)
		unlistedIn.IsRunApplied = func(_, _ string) bool { return true }
		unlistedOut := mustApply(t, unlistedIn)
		require.Equal(t, patch.OutcomeAbstain, unlistedOut.Kind)
		require.Equal(t, patch.AbstainGuardRefused, unlistedOut.AbstainReason)

		// Rewrite-precheck beats the run-id port (order 2 < 3).
		rewriteAllowed := makeDocPatch(docPatchOpts{
			docPath:     "docs/payments.md",
			baseContent: bodyDoc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 2, deleteText: strings.Join(bodyLines[1:], "\n"), insert: "Fresh body."})},
		})
		rewriteIn := baseApplyInput(rewriteAllowed)
		rewriteIn.WorktreeContent = strp(bodyDoc)
		rewriteIn.IsRunApplied = func(_, _ string) bool { return true }
		rewriteOut := mustApply(t, rewriteIn)
		require.Equal(t, patch.OutcomeAbstain, rewriteOut.Kind)
		require.Equal(t, patch.AbstainRewriteRejected, rewriteOut.AbstainReason)
	})
}

/* ------------------------------------------- hash-verified application */

func TestApplyHashVerifiedApplication(t *testing.T) {
	t.Run("stale recorded lines resolve at the ACTUAL lines of the hashed span (content wins over lines)", func(t *testing.T) {
		p := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 2, deleteText: docLines[2], insert: "Refunds require an idempotency key above 10 USD."}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(doc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.False(t, o.ThreeWayUsed)
		require.Equal(t, []patch.AppliedHunk{{Index: 0, StartLine: 3, EndLine: 3, ReAnchored: true}}, o.AppliedHunks)
		require.Equal(t,
			"# Payments\n\nRefunds require an idempotency key above 10 USD.\nAmounts above 10 USD are held for review.\n\nSee docs/architecture.md for the flow.\n",
			o.ResultContent)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 1}, o.Minimality.Minimality)
	})

	t.Run("misquoted delete_text is unanchored even though the recorded lines match real lines", func(t *testing.T) {
		p := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 4, deleteText: "Amounts above 25 USD are held for review."})},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(doc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeUnanchored, o.Kind)
		require.Contains(t, o.UnanchoredHunks[0].Reason, "content hash matches nowhere")
	})

	t.Run("multi-hunk patches splice bottom-up over hash-verified anchors", func(t *testing.T) {
		p := makeDocPatch(docPatchOpts{
			baseContent: richDoc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: richDocLines[2], insert: "Refunds require an idempotency key above 10 USD."}),
				hunk(hunkOpts{start: 6, deleteText: richDocLines[5], insert: "See docs/architecture.md for the refund flow."}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(richDoc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.Equal(t, []patch.AppliedHunk{
			{Index: 0, StartLine: 3, EndLine: 3, ReAnchored: false},
			{Index: 1, StartLine: 6, EndLine: 6, ReAnchored: false},
		}, o.AppliedHunks)
		require.Equal(t,
			strings.Join([]string{
				"# Payments",
				"",
				"Refunds require an idempotency key above 10 USD.",
				"Amounts above 10 USD are held for review.",
				"",
				"See docs/architecture.md for the refund flow.",
				"Escalation contacts live in docs/oncall.md.",
				"The review window is 48 hours.",
				"Refund disputes go to docs/disputes.md.",
			}, "\n")+"\n",
			o.ResultContent)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 2, RemovedLines: 2}, o.Minimality.Minimality)
	})

	t.Run("pure insertions splice before the anchor line (and append at the end-of-document gap)", func(t *testing.T) {
		p := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 2, deleteText: "", insert: "> Reviewed against the refund policy."}),
				hunk(hunkOpts{start: 7, deleteText: "", insert: "Managed by docSync run acme/payments#8a73f2c.."}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(doc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.Equal(t,
			strings.Join([]string{
				"# Payments",
				"> Reviewed against the refund policy.",
				"",
				"Refunds require an idempotency key.",
				"Amounts above 10 USD are held for review.",
				"",
				"See docs/architecture.md for the flow.",
				"Managed by docSync run acme/payments#8a73f2c..",
			}, "\n")+"\n",
			o.ResultContent)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 2, RemovedLines: 0}, o.Minimality.Minimality)
	})

	t.Run("reads the current content via git.CatFile when worktreeContent is absent", func(t *testing.T) {
		var calls []gitCall
		p := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: docLines[2], insert: "Refunds need an idempotency key."}),
			},
		})
		in := baseApplyInput(p)
		in.Git = newCatFileDouble(doc, &calls)
		in.WorktreeContent = nil
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.Equal(t, []gitCall{{rev: "HEAD", path: "docs/payments.md"}}, calls)
	})

	t.Run("overlapping resolved ranges (schema-skipped patch object) → whole-patch overlap abstain, no partial application", func(t *testing.T) {
		overlapPatch := &schemas.DocPatch{
			Schema:          "docsync.doc-patch/v1",
			RunID:           "run-overlap",
			DocPath:         "docs/payments.md",
			BaseContentHash: docutil.ContentHash(doc),
			Hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: docLines[2] + "\n" + docLines[3]}),
				hunk(hunkOpts{start: 4, deleteText: docLines[3] + "\n" + docLines[4]}),
			},
			EvidenceRefs: []schemas.Evidence{},
			Minimality:   schemas.DocPatchMinimality{Measured: true},
			Provenance:   schemas.DocPatchProvenance{PolicyVersion: 17, WriterModel: "double", GeneratedAt: NOW()},
		}
		in := baseApplyInput(overlapPatch)
		in.WorktreeContent = strp(doc)
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Equal(t, patch.AbstainOverlap, o.AbstainReason)
		require.Contains(t, o.Detail, "resolved hunk ranges overlap")
		require.Contains(t, o.Detail, "no partial application")
	})

	t.Run("the minimality ratio is carried for the verifier when triggering stats are supplied (P3-T12 exposure)", func(t *testing.T) {
		p := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: docLines[2], insert: "Refunds require an idempotency key above 10 USD."}),
			},
		})
		in := baseApplyInput(p)
		in.WorktreeContent = strp(doc)
		in.TriggeringDiffStats = &patch.DiffStats{Added: 1, Removed: 0}
		maxRatio := 1.5
		in.MaxRatio = &maxRatio
		o := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.NotNil(t, o.Minimality.Ratio)
		require.InDelta(t, 2.0, *o.Minimality.Ratio, 1e-12)
		require.True(t, o.Minimality.Oversized)
	})
}

/* --------------------------------------------- configuration contract */

func TestApplyConfigErrorsThrowTypedPatchEngineError(t *testing.T) {
	mkPatch := func() *schemas.DocPatch {
		return makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 3, deleteText: docLines[2]})},
		})
	}

	t.Run("missing/typed-wrong guard inputs throw a typed *PatchEngineError; they never become semantic outcomes", func(t *testing.T) {
		noAllow := baseApplyInput(mkPatch())
		noAllow.ManifestAllowlist = nil
		_, err := patch.ApplyDocPatch(context.Background(), noAllow)
		requirePatchEngineError(t, err)

		noNow := baseApplyInput(mkPatch())
		noNow.Now = nil
		_, err = patch.ApplyDocPatch(context.Background(), noNow)
		requirePatchEngineError(t, err)

		noGit := baseApplyInput(mkPatch())
		noGit.Git = nil
		_, err = patch.ApplyDocPatch(context.Background(), noGit)
		requirePatchEngineError(t, err)

		notDocPatch := baseApplyInput(&schemas.DocPatch{Schema: "docsync.doc-patch/v1"})
		notDocPatch.WorktreeContent = strp(doc)
		_, err = patch.ApplyDocPatch(context.Background(), notDocPatch)
		requirePatchEngineError(t, err)
	})

	t.Run("an injected base snapshot that does not hash to base_content_hash is a caller contract violation", func(t *testing.T) {
		in := baseApplyInput(mkPatch())
		in.WorktreeContent = strp("changed doc\n")
		in.BaseContent = strp("unrelated content\n")
		_, err := patch.ApplyDocPatch(context.Background(), in)
		requirePatchEngineError(t, err)
	})

	t.Run("an empty hunks array is a protocol violation — defensive PatchEngineError even for schema-skipped objects", func(t *testing.T) {
		empty := &schemas.DocPatch{
			Schema:          "docsync.doc-patch/v1",
			RunID:           "run-empty",
			DocPath:         "docs/payments.md",
			BaseContentHash: docutil.ContentHash(doc),
			Hunks:           []schemas.PatchHunk{},
			EvidenceRefs:    []schemas.Evidence{},
			Minimality:      schemas.DocPatchMinimality{Measured: true},
			Provenance:      schemas.DocPatchProvenance{PolicyVersion: 17, WriterModel: "double", GeneratedAt: NOW()},
		}
		in := baseApplyInput(empty)
		in.WorktreeContent = strp(doc)
		_, err := patch.ApplyDocPatch(context.Background(), in)
		requirePatchEngineError(t, err)
		require.Contains(t, err.Error(), "empty patches are protocol violations")
	})

	t.Run("the claims port is accepted-and-unused in V1 (threaded only — Phase 4 consumes it)", func(t *testing.T) {
		claimsPatch := makeDocPatch(docPatchOpts{
			baseContent: doc,
			hunks: []schemas.PatchHunk{
				hunk(hunkOpts{start: 3, deleteText: docLines[2], insert: "Refunds require an idempotency key above 10 USD."}),
			},
		})
		in := baseApplyInput(claimsPatch)
		in.WorktreeContent = strp(doc)
		in.Claims = func() {} // any value; must not change the outcome
		withClaims := mustApply(t, in)
		require.Equal(t, patch.OutcomeApplied, withClaims.Kind)
		require.Equal(t, strings.Replace(doc, docLines[2], "Refunds require an idempotency key above 10 USD.", 1), withClaims.ResultContent)
	})
}

/* ---------------------------------------------- V-P11-T15: claim refs */

func TestApplyClaimRefsSurvive(t *testing.T) {
	p := makeDocPatch(docPatchOpts{
		baseContent: doc,
		hunks: []schemas.PatchHunk{
			hunk(hunkOpts{
				start:             3,
				deleteText:        "Refunds require an idempotency key.",
				insert:            "Refunds require an idempotency key for amounts above 10 USD.",
				claimIDs:          []string{"claim_182"},
				verifiedClaimRefs: []string{"claim_182"},
			}),
		},
		verifiedClaims: []string{"claim_182"},
	})
	in := baseApplyInput(p)
	in.WorktreeContent = strp(doc)
	o := mustApply(t, in)

	require.Equal(t, patch.OutcomeApplied, o.Kind)
	require.NotNil(t, o.Patch)
	require.Equal(t, []string{"claim_182"}, o.Patch.VerifiedClaims)
	require.Equal(t, []string{"claim_182"}, o.Patch.Hunks[0].VerifiedClaimRefs)
	require.Equal(t, []string{"claim_182"}, o.Patch.Hunks[0].ClaimIDs)
	require.Equal(t,
		strings.Join([]string{
			"# Payments",
			"",
			"Refunds require an idempotency key for amounts above 10 USD.",
			"Amounts above 10 USD are held for review.",
			"",
			"See docs/architecture.md for the flow.",
			"",
		}, "\n"),
		o.ResultContent)
}

/* ---------------------------------------------- purity: import audit */

func TestPatchModulesImportNothingWriteCapable(t *testing.T) {
	// Port of test/patch-purity.test.ts: every patch source file imports nothing
	// from node builtins/fs/network/model-adapter surfaces. In Go the engine is
	// pure over injected inputs — its own imports must not reach a write-capable,
	// network, or model surface (git READ access reaches it only via the injected
	// CatFilePort; persistence is Phase 5/6's job).
	forbidden := regexp.MustCompile(`^(net|net/http|os|os/exec|syscall|unsafe|io/ioutil|io|archive/tar|plugin|github.com/DUDE616/docSync-go/internal/openrouter|github.com/DUDE616/docSync-go/internal/analysis)($|/)`)

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var srcFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			srcFiles = append(srcFiles, name)
		}
	}
	require.NotEmpty(t, srcFiles)

	// Audit sanity: the ported modules are actually among the audited files.
	require.Subset(t, srcFiles, []string{"apply.go", "anchor.go", "merge.go", "minimality.go", "export-claims.go", "types.go"})

	fset := token.NewFileSet()
	for _, name := range srcFiles {
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imp := range file.Imports {
			spec := strings.Trim(imp.Path.Value, `"`)
			require.False(t, forbidden.MatchString(spec),
				"%s imports %q — patch modules must be pure over injected inputs", name, spec)
		}
	}
}
