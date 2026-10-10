package patch

// Ported from core/src/patch/apply.ts — the content-anchored hunk application
// engine (P3-T08; ARCH §4.3, §4.9, §11.4 layer 3, §12.1/§12.3, A9; invariants
// 2/3/12/13; V-P3-2..V-P3-7).
//
// DETERMINISTIC OUTCOME ORDER (each step sees only outcomes of prior steps):
//
//  1. GUARD (P3-T16, lock layer 3): patch.doc_path must be in the
//     MANIFEST-DERIVED writable allowlist and must NOT be in the protected set —
//     the allowlist is computed from the manifest, NEVER from model output; a
//     patch touching a protected/unlisted path is rejected WHOLE (no partial
//     application; invariants 2/3). Configuration errors of the guard itself
//     (missing/typed-wrong inputs) surface as a *PatchEngineError — a
//     misconfigured guard cannot even decide.
//  2. Whole-document-rewrite precheck (P3-T07, invariant 12, V-P3-2):
//     schemas.AssertNotWholeDocumentRewrite against the CURRENT content — a
//     structural rejection BEFORE any application; surfaces as abstain
//     'rewrite-rejected', never an error.
//  3. Idempotency (P3-T11, §12.1): run-id skip via the injected IsRunApplied
//     port (persisted state is Phase 5's wiring), plus the content-level no-op:
//     when the engine's prospective result equals the current content
//     byte-for-byte, the outcome is noop 'already-applied' (the engine is pure
//     over content; the check fires after the prospective result exists — see
//     steps 4-6).
//  4. Base-content race check (P3-T10, A9) — BEFORE anchor resolution (step 5):
//     docutil.ContentHash(current) must equal patch.base_content_hash. On
//     mismatch (the doc changed since the writer read it) the ONLY paths are:
//     abstain 'doc-changed-race-unverified' (no base snapshot available, or no
//     ReVerify port — fail closed), a deterministic 3-way merge (merge.go) whose
//     result MUST pass the injected ReVerify port before it can be applied
//     (ThreeWayUsed: true), or noop 'already-applied' when the patch is already
//     in the document. A merge with conflicts abstains — never shipped
//     unresolved.
//  5. Anchors (P3-T08/T09): EVERY hunk's anchor resolves content-hash-first
//     (anchor.go); ANY unanchored hunk → the WHOLE patch abstains 'unanchored'
//     with per-hunk reasons and docStateSignal UNANCHORED — no partial
//     application, never a silent re-anchor (invariant 13). Post-resolution
//     backstops: overlapping resolved ranges → abstain 'overlap'; a resolved
//     range outside the document → abstain 'out-of-bounds' (defensive in depth
//     against DocPatch objects that skipped schema validation).
//  6. Apply hunks (bottom-up splice over hash-verified anchors) → ResultContent;
//     minimality is MEASURED (P3-T12) and carried on the applied outcome for the
//     verifier. The engine NEVER writes to disk or git itself — persistence is
//     the Phase 5/6 write path's job.
//
// The engine returns an error only for engine-configuration failures
// (*PatchEngineError) — every decision above is a typed ApplyOutcome. The Claims
// field is accepted-but-unused (Phase 4's over-deletion guard consumes the claim
// store; Phase 3 only threads it — see ApplyDocPatchInput): the pure engine v1
// does not mutate the claim store.

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CatFilePort is the narrow git READ port the engine consumes (TS:
// Pick<GitClient, 'catFile'>). *gitx.GitClient satisfies it directly; tests
// inject a fake. The engine writes nothing to git itself.
type CatFilePort interface {
	CatFile(ctx context.Context, rev, path string) (gitx.CatFileResult, error)
}

// ReVerifyResult is the re-verification port's verdict (P3-T10).
type ReVerifyResult struct {
	Pass   bool
	Detail string
}

// ReVerifyPort (TS: ReVerifyPort) is the verification engine's pass, injected.
type ReVerifyPort func(patchedContent string, racePatch *schemas.DocPatch) ReVerifyResult

// IsRunAppliedPort (TS: IsRunAppliedPort) is the persisted run-state port
// (P3-T11): Phase 5 wires the state store in.
type IsRunAppliedPort func(runID, docPath string) bool

// ApplyDocPatchInput (TS: ApplyDocPatchInput) carries the engine's injected
// inputs.
type ApplyDocPatchInput struct {
	// Patch is the patch to apply (schema-valid DocPatch).
	Patch *schemas.DocPatch
	// Git is read access — used ONLY when WorktreeContent is absent.
	Git CatFilePort
	// Rev is the rev whose tree holds the current document content.
	Rev string
	// WorktreeContent is pre-read current document content (e.g. the worktree
	// file). When absent the engine reads via Git.CatFile(Rev, Patch.DocPath).
	WorktreeContent *string
	// ManifestAllowlist is the MANIFEST-DERIVED writable-path allowlist (P3-T16,
	// lock layer 3) — NEVER computed from model output. The engine refuses any
	// doc_path outside it. A nil allowlist is a configuration error (fail closed).
	ManifestAllowlist map[string]bool
	// ProtectedPaths is the protected set (D2 — locked/generated/external +
	// source files). Optional; a nil map means none.
	ProtectedPaths map[string]bool
	// BaseContent is the ORIGINAL base content the writer read (its snapshot).
	// Required for the race merge path; its hash must equal
	// Patch.BaseContentHash.
	BaseContent *string
	// Claims is the claim-store port — ACCEPTED BUT UNUSED by the pure engine v1
	// (see module doc): Phase 4 consumes it; Phase 3 only threads it so callers
	// can already pass it without a signature break later.
	Claims any
	// Now is the INJECTED clock (provenance AppliedAt; never a local clock).
	Now func() string
	// ReVerify is the re-verification port (P3-T10); nil → races always abstain
	// (fail closed).
	ReVerify ReVerifyPort
	// IsRunApplied is the persisted run-state port (P3-T11); nil → content-level
	// idempotency only.
	IsRunApplied IsRunAppliedPort
	// TriggeringDiffStats is the triggering code-diff size for the minimality
	// ratio (P3-T12); optional.
	TriggeringDiffStats *DiffStats
	// MaxRatio overrides the oversized flag bound; nil uses
	// DefaultMinimalityMaxRatio.
	MaxRatio *float64
}

// resolvedHunk is one hunk resolved to its actual base-relative lines (internal
// to apply; TS interface ResolvedHunk).
type resolvedHunk struct {
	Index       int
	StartLine   int
	EndLine     int
	ReAnchored  bool
	IsInsertion bool
}

// noopOutcome / abstainOutcome build the non-applied variants.
func noopOutcome(reason NoopReason) ApplyOutcome {
	return ApplyOutcome{Kind: OutcomeNoop, NoopReason: reason}
}

func abstainOutcome(reason ApplyAbstainReason, detail string) ApplyOutcome {
	return ApplyOutcome{Kind: OutcomeAbstain, AbstainReason: reason, Detail: detail}
}

// spliceHunks is the bottom-up splice of resolved hunks into lines (matches the
// schema's applyHunksStructurally reference: bottom-up so earlier indices do not
// shift). Replacements/deletions remove the INCLUSIVE anchor range; pure
// insertions (zero-width anchors) remove NOTHING — they insert before the anchor
// line. Deterministic.
func spliceHunks(lines []string, resolved []resolvedHunk, patch *schemas.DocPatch) []string {
	applied := make([]string, len(lines))
	copy(applied, lines)
	bottomUp := make([]resolvedHunk, len(resolved))
	copy(bottomUp, resolved)
	sort.SliceStable(bottomUp, func(i, j int) bool {
		return bottomUp[i].StartLine > bottomUp[j].StartLine
	})
	for _, hunk := range bottomUp {
		source := &patch.Hunks[hunk.Index]
		var replacement []string
		if len(source.InsertText) != 0 {
			replacement = strings.Split(source.InsertText, "\n")
		}
		removeCount := 0
		if !hunk.IsInsertion {
			removeCount = hunk.EndLine - hunk.StartLine + 1
		}
		applied = spliceLines(applied, hunk.StartLine-1, removeCount, replacement)
	}
	return applied
}

// spliceLines mirrors JavaScript Array.splice(start, deleteCount, ...items) with
// native clamping — used by the bottom-up splice above.
func spliceLines(a []string, start, removeCount int, replacement []string) []string {
	if start < 0 {
		start = 0
	}
	if start > len(a) {
		start = len(a)
	}
	end := start + removeCount
	if end > len(a) {
		end = len(a)
	}
	out := make([]string, 0, start+len(replacement)+(len(a)-end))
	out = append(out, a[:start]...)
	out = append(out, replacement...)
	out = append(out, a[end:]...)
	return out
}

// joinLikeBasis reassembles spliced lines with the basis content's
// trailing-newline convention.
func joinLikeBasis(lines []string, basisContent string) string {
	if len(lines) == 0 {
		return ""
	}
	joined := strings.Join(lines, "\n")
	if strings.HasSuffix(basisContent, "\n") {
		return joined + "\n"
	}
	return joined
}

// ApplyDocPatch (TS: applyDocPatch) is the application engine (see module doc
// for the deterministic outcome order). It returns an error only for
// engine-configuration failures (*PatchEngineError); every semantic decision is
// a typed ApplyOutcome value.
func ApplyDocPatch(ctx context.Context, input ApplyDocPatchInput) (ApplyOutcome, error) {
	/* -- configuration (typed errors; a misconfigured guard cannot decide) --- */
	if input.Now == nil {
		return ApplyOutcome{}, newPatchEngineError(
			"applyDocPatch config error: 'now' must be a function (injected clock)")
	}
	if input.ManifestAllowlist == nil {
		return ApplyOutcome{}, newPatchEngineError(
			"applyDocPatch config error: 'manifestAllowlist' must be a ReadonlySet<string> (manifest-derived, P3-T16)")
	}
	if input.Rev == "" {
		return ApplyOutcome{}, newPatchEngineError(
			"applyDocPatch config error: 'rev' must be a nonempty string")
	}
	if input.Git == nil {
		return ApplyOutcome{}, newPatchEngineError(
			"applyDocPatch config error: 'git' must expose catFile")
	}
	// protectedPaths is type-safe in Go (a nil map means "none"); the TS
	// typed-wrong-input check is unreachable here.
	patch := input.Patch
	if patch == nil ||
		patch.DocPath == "" ||
		patch.BaseContentHash == "" ||
		patch.RunID == "" ||
		patch.Hunks == nil {
		return ApplyOutcome{}, newPatchEngineError(
			"applyDocPatch config error: patch must be a DocPatch (doc_path/run_id/base_content_hash/hunks present)")
	}
	// Defensive in depth (schema parity): an EMPTY hunks slice is a protocol
	// violation the wire schema rejects (hunks min(1) — writers abstain upstream);
	// a DocPatch object that skipped schema validation gets the same fail-closed
	// treatment here instead of a meaningless "application".
	if len(patch.Hunks) == 0 {
		return ApplyOutcome{}, newPatchEngineError(
			"applyDocPatch config error: patch.hunks must be a non-empty array (empty patches are protocol violations — writers abstain upstream)")
	}

	/* -- 1. GUARD (P3-T16, lock layer 3; invariants 2/3) --------------------- */
	if !input.ManifestAllowlist[patch.DocPath] {
		return abstainOutcome(AbstainGuardRefused,
			"doc_path '"+patch.DocPath+"' is not in the manifest-derived writable allowlist "+
				"(invariant 3: the allowlist is computed from the manifest, never from model output; "+
				"docsync never patches unlisted paths — code files and .docsync/ internals included)"), nil
	}
	if input.ProtectedPaths[patch.DocPath] {
		return abstainOutcome(AbstainGuardRefused,
			"doc_path '"+patch.DocPath+"' is protected (D2: locked/generated/external ownership "+
				"floors resolve protected; invariants 2/3 — a patch touching a protected path is "+
				"rejected WHOLE, no partial application)"), nil
	}

	/* -- current content (worktree pre-read, else git read) ------------------ */
	var currentContent string
	if input.WorktreeContent != nil {
		currentContent = *input.WorktreeContent
	} else {
		blob, err := input.Git.CatFile(ctx, input.Rev, patch.DocPath)
		if err != nil {
			// A doc missing at rev = the doc changed since the writer read it — a
			// race outcome (abstain). Any other git failure is a runtime failure
			// and is returned (fail closed; exit-code 3 semantics).
			if gitErr, ok := gitx.AsGitExecError(err); ok && gitErr.Kind == gitx.KindMissingRev {
				return abstainOutcome(AbstainDocChangedRaceUnverified,
					"doc '"+patch.DocPath+"' no longer exists at rev '"+input.Rev+"' — the document "+
						"changed since the writer read it (A9: races abstain to REVIEW)"), nil
			}
			return ApplyOutcome{}, err
		}
		currentContent = blob.Content
	}

	/* -- 2. whole-document-rewrite precheck (invariant 12, V-P3-2) ----------- */
	if err := schemas.AssertNotWholeDocumentRewrite(patch, currentContent); err != nil {
		var rewriteErr *schemas.WholeDocumentRewriteError
		if errors.As(err, &rewriteErr) {
			return abstainOutcome(AbstainRewriteRejected, rewriteErr.Error()), nil
		}
		return ApplyOutcome{}, err
	}

	/* -- 3. idempotency: persisted run-state port (P3-T11) ------------------- */
	if input.IsRunApplied != nil && input.IsRunApplied(patch.RunID, patch.DocPath) {
		return noopOutcome(NoopRunIDSkip), nil
	}

	/* -- 4. base-content race detection (P3-T10, A9; before anchors) --------- */
	race := docutil.ContentHash(currentContent) != patch.BaseContentHash
	var basisContent string
	if race {
		if input.BaseContent == nil {
			return abstainOutcome(AbstainDocChangedRaceUnverified,
				"doc '"+patch.DocPath+"' changed since the writer read it (base_content_hash mismatch) "+
					"and no base snapshot was provided — fail closed (A9: races abstain to REVIEW; a "+
					"3-way merge ships only on a re-verified pass)"), nil
		}
		if docutil.ContentHash(*input.BaseContent) != patch.BaseContentHash {
			return ApplyOutcome{}, newPatchEngineError(
				"applyDocPatch config error: injected 'baseContent' does not hash to the patch's " +
					"base_content_hash — caller contract violation")
		}
		basisContent = *input.BaseContent
	} else {
		basisContent = currentContent
	}

	/* -- 5. anchors: resolve EVERY hunk content-hash-first (invariant 13) ----- */
	basisLines := claims.SplitDocLines(basisContent)
	var resolved []resolvedHunk
	var unanchoredHunks []UnanchoredHunk
	for index := range patch.Hunks {
		hunk := &patch.Hunks[index]
		resolution := ResolveHunkAnchor(hunk, basisContent)
		if resolution.Status == AnchorUnanchored {
			unanchoredHunks = append(unanchoredHunks, UnanchoredHunk{
				Index:  index,
				Reason: resolution.Reason,
			})
			continue
		}
		resolved = append(resolved, resolvedHunk{
			Index:       index,
			StartLine:   resolution.LineRange.StartLine,
			EndLine:     resolution.LineRange.EndLine,
			ReAnchored:  resolution.ReAnchored,
			IsInsertion: len(hunk.DeleteText) == 0,
		})
	}
	if len(unanchoredHunks) > 0 {
		return ApplyOutcome{
			Kind:            OutcomeUnanchored,
			UnanchoredHunks: unanchoredHunks,
			DocStateSignal:  DocStateSignalUnanchored,
		}, nil
	}

	// Backstop: overlapping resolved ranges → the whole patch abstains (no
	// partial application; defends against DocPatch objects that skipped the
	// schema's non-overlap refinement).
	byStart := make([]resolvedHunk, len(resolved))
	copy(byStart, resolved)
	sort.SliceStable(byStart, func(i, j int) bool {
		return byStart[i].StartLine < byStart[j].StartLine
	})
	for i := 1; i < len(byStart); i++ {
		previous := byStart[i-1]
		current := byStart[i]
		if current.StartLine <= previous.EndLine {
			return abstainOutcome(AbstainOverlap,
				"resolved hunk ranges overlap: hunk "+strconv.Itoa(previous.Index)+
					" ["+strconv.Itoa(previous.StartLine)+", "+strconv.Itoa(previous.EndLine)+"] and hunk "+
					strconv.Itoa(current.Index)+" ["+strconv.Itoa(current.StartLine)+", "+
					strconv.Itoa(current.EndLine)+"] — no partial application"), nil
		}
	}
	// Backstop: a resolved range outside the document (unreachable for
	// hash-verified anchors and bounds-checked insertions; defense in depth).
	for _, hunk := range resolved {
		maxPoint := len(basisLines)
		if hunk.IsInsertion {
			maxPoint = len(basisLines) + 1
		}
		if hunk.StartLine < 1 || hunk.EndLine > maxPoint {
			return abstainOutcome(AbstainOutOfBounds,
				"resolved hunk "+strconv.Itoa(hunk.Index)+" range ["+strconv.Itoa(hunk.StartLine)+", "+
					strconv.Itoa(hunk.EndLine)+"] is outside the document bounds (1-"+strconv.Itoa(maxPoint)+
					") — no partial application"), nil
		}
	}

	/* -- 6. apply hunks (bottom-up splice over hash-verified anchors) --------- */
	prospectiveLines := spliceHunks(basisLines, resolved, patch)
	appliedHunks := make([]AppliedHunk, len(byStart))
	for i, hunk := range byStart {
		appliedHunks[i] = AppliedHunk{
			Index:      hunk.Index,
			StartLine:  hunk.StartLine,
			EndLine:    hunk.EndLine,
			ReAnchored: hunk.ReAnchored,
		}
	}

	finishApplied := func(resultContent string, threeWayUsed bool) (ApplyOutcome, error) {
		measurement, err := MeasureMinimality(MeasureMinimalityInput{
			Patch:               patch,
			CurrentContent:      currentContent,
			PatchedContent:      resultContent,
			TriggeringDiffStats: input.TriggeringDiffStats,
			MaxRatio:            input.MaxRatio,
		})
		if err != nil {
			return ApplyOutcome{}, err
		}
		return ApplyOutcome{
			Kind:          OutcomeApplied,
			Patch:         patch,
			ResultContent: resultContent,
			AppliedHunks:  appliedHunks,
			ThreeWayUsed:  threeWayUsed,
			AppliedAt:     input.Now(),
			Minimality:    measurement,
		}, nil
	}

	/* -- 3b. content-level idempotency (P3-T11): already applied? ------------- */
	if !race {
		resultContent := joinLikeBasis(prospectiveLines, basisContent)
		if resultContent == currentContent {
			return noopOutcome(NoopAlreadyApplied), nil
		}
		return finishApplied(resultContent, false)
	}

	// Race path: theirs = the writer's intended result (patch applied to ITS OWN
	// base).
	theirs := joinLikeBasis(prospectiveLines, basisContent)
	if theirs == currentContent {
		// The document already carries the patch's full effect — nothing to do
		// (§12.1: the second apply of an identical patch is a no-op).
		return noopOutcome(NoopAlreadyApplied), nil
	}

	merged := ThreeWayMerge(ThreeWayMergeInput{Base: basisContent, Ours: currentContent, Theirs: theirs})
	if !merged.Ok {
		return abstainOutcome(AbstainDocChangedRaceUnverified,
			"three-way merge of the writer patch into the changed document conflicts ("+
				strconv.Itoa(len(merged.Conflicts))+" region(s), first at base line "+
				strconv.Itoa(merged.Conflicts[0].Line)+") — refusing to ship an unresolved merge "+
				"(A9: races abstain to REVIEW)"), nil
	}
	if merged.Merged == currentContent {
		return noopOutcome(NoopAlreadyApplied), nil
	}
	if input.ReVerify == nil {
		return abstainOutcome(AbstainDocChangedRaceUnverified,
			"doc '"+patch.DocPath+"' changed since the writer read it and no reVerify port was "+
				"provided — fail closed (A9: a 3-way merge ships only on a re-verified pass)"), nil
	}
	verdict := input.ReVerify(merged.Merged, patch)
	if !verdict.Pass {
		detail := verdict.Detail
		if detail == "" {
			detail = "no detail provided"
		}
		return abstainOutcome(AbstainDocChangedRaceUnverified,
			"re-verification of the merged result failed: "+detail+" — "+
				"the race merge is not applied (A9)"), nil
	}
	return finishApplied(merged.Merged, true)
}
