package verifier

// Ported from packages/verifier/src/tier1/apply-check.ts (P4-T02; PLAN §5.4
// item 2; ARCH §4.9): the Tier-1 DETERMINISTIC application simulation. Runs
// patch.ApplyDocPatch over the injected document content — the verifier
// computes nothing about permissions itself; the allowlist arrives
// application-owned.
//
// Provenance: the record's `inputs` pin the consumed evidence — the git rev
// (when supplied), the pre-patch content blob (`blob:<sha>`), and, when the
// apply produced result content, the PATCHED content blob
// (`patched:<hash16>` — first 16 hex of the sha256 over the simulated
// result; core's contentHash), so the check is replayable evidence
// (invariant 10 — provenance or it didn't happen).
//
// Outcome mapping (fail closed; ARCH §4.8 invariant 5 — every failing check
// carries evidence):
//   - `applied`                ⇒ pass (resultContent carried forward);
//   - `noop` `already-applied` ⇒ pass (the patch's effect is already in the
//     document — byte-for-byte, §12.1);
//   - `noop` `run-id-skip`     ⇒ 'skipped' (idempotent skip is not a failure,
//     but the tier cannot attest application);
//   - any `abstain` / `unanchored` ⇒ fail with the abstain reason (hunk
//     indices included for UNANCHORED).
//
// The engine is pure over content — no git writes — so the simulation is a
// safe verification step, and the engine's own `catFile` port is used only
// when the caller supplied `git`/`rev` (otherwise a fail-closed stub keeps
// the check working from injected content alone: the stub READS NOTHING —
// any attempt to read fails loudly instead of fabricating content).

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ApplyCheckResult is the apply check's record plus what later tiers consume.
type ApplyCheckResult struct {
	Check schemas.VerificationCheck
	// Outcome is the engine outcome (carried for later tiers).
	Outcome patch.ApplyOutcome
	// PatchedContent is the content later checks verify: the engine's result
	// for `applied`, the unchanged document for `already-applied` (its
	// content IS the patch's result), nil when no simulation produced
	// content.
	PatchedContent *string
}

// injectedContentOnlyRev is the stand-in rev used when the caller supplied no
// git port (TS: input.rev ?? 'tier1-injected-content-only').
const injectedContentOnlyRev = "tier1-injected-content-only"

// contentOnlyGit is a git port that refuses every read — injected content is
// the only source (TS: CONTENT_ONLY_GIT). It satisfies patch.CatFilePort.
type contentOnlyGit struct{}

// CatFile refuses the read with the typed structural defect (fail closed).
func (contentOnlyGit) CatFile(_ context.Context, _, _ string) (gitx.CatFileResult, error) {
	return gitx.CatFileResult{}, &StructuralDefect{
		Message: "tier1 apply-check attempted a git read although no git port was supplied — " +
			"injected content is the only source (fail closed)",
	}
}

// CheckPatchAppliesCleanly runs the deterministic application simulation.
// The returned error is reserved for non-engine runtime failures (the TS
// tier rethrows them instead of deciding): engine-configuration failures are
// mapped to an abstain outcome with the typed error as evidence (fail
// closed, never a crashed tier).
func CheckPatchAppliesCleanly(input Tier1Input) (ApplyCheckResult, error) {
	if input.DocContent == nil {
		return ApplyCheckResult{
			Check: schemas.VerificationCheck{
				ID:     "patch-applies-cleanly",
				Status: schemas.CheckFail,
				Detail: tier1DetailPtr(
					"doc_path '" + input.Patch.DocPath + "' has no current content (document absent) — " +
						"the application cannot be verified (fail closed; A9: a patch on an absent " +
						"document is a writer misquote, routed to REVIEW)",
				),
			},
			Outcome: patch.ApplyOutcome{
				Kind:          patch.OutcomeAbstain,
				AbstainReason: patch.AbstainOutOfBounds,
				Detail:        "document content absent (docContent null) — not simulated",
			},
			PatchedContent: nil,
		}, nil
	}

	rev := injectedContentOnlyRev
	if input.Rev != nil {
		rev = *input.Rev
	}
	gitPort := input.Git
	if gitPort == nil {
		gitPort = contentOnlyGit{}
	}

	worktreeContent := *input.DocContent
	outcome, err := patch.ApplyDocPatch(context.Background(), patch.ApplyDocPatchInput{
		Patch:               input.Patch,
		Git:                 gitPort,
		Rev:                 rev,
		WorktreeContent:     &worktreeContent,
		ManifestAllowlist:   input.ManifestAllowlist,
		BaseContent:         input.BaseContent,
		IsRunApplied:        input.IsRunApplied,
		Now:                 input.Now,
		TriggeringDiffStats: input.TriggeringDiffStats,
		MaxRatio:            input.MinRatio,
	})
	if err != nil {
		var engineErr *patch.PatchEngineError
		if !errors.As(err, &engineErr) {
			// A misconfigured engine input cannot even decide — fail the check
			// with the typed error as evidence (fail closed), never crash the
			// tier. Anything else is a runtime failure and propagates.
			return ApplyCheckResult{}, err
		}
		outcome = patch.ApplyOutcome{
			Kind:          patch.OutcomeAbstain,
			AbstainReason: patch.AbstainOutOfBounds,
			Detail:        "application simulation could not decide (PatchEngineError): " + engineErr.Error(),
		}
	}

	inputs := []string{"blob:" + docutil.ContentHash(worktreeContent)}
	if input.Rev != nil {
		inputs = append([]string{"git:" + *input.Rev}, inputs...)
	}

	switch outcome.Kind {
	case patch.OutcomeApplied:
		appliedInputs := make([]string, 0, len(inputs)+1)
		appliedInputs = append(appliedInputs, inputs...)
		appliedInputs = append(appliedInputs, "patched:"+docutil.ContentHash(outcome.ResultContent)[:16])
		detail := strconv.Itoa(len(outcome.AppliedHunks)) + " hunk(s) applied"
		if outcome.ThreeWayUsed {
			detail += " (three-way race merge, re-verified)"
		}
		result := outcome.ResultContent
		return ApplyCheckResult{
			Check: schemas.VerificationCheck{
				ID:     "patch-applies-cleanly",
				Status: schemas.CheckPass,
				Inputs: appliedInputs,
				Detail: tier1DetailPtr(detail),
			},
			Outcome:        outcome,
			PatchedContent: &result,
		}, nil

	case patch.OutcomeNoop:
		if outcome.NoopReason == patch.NoopAlreadyApplied {
			unchanged := worktreeContent
			return ApplyCheckResult{
				Check: schemas.VerificationCheck{
					ID:     "patch-applies-cleanly",
					Status: schemas.CheckPass,
					Inputs: inputs,
					Detail: tier1DetailPtr(
						"noop: already-applied — the patch's effect is already in the document " +
							"(idempotent no-op, §12.1)",
					),
				},
				Outcome:        outcome,
				PatchedContent: &unchanged,
			}, nil
		}
		return ApplyCheckResult{
			Check: schemas.VerificationCheck{
				ID:     "patch-applies-cleanly",
				Status: schemas.CheckSkipped,
				Inputs: inputs,
				Detail: tier1DetailPtr(
					"run-id skip (idempotency port reported run '" + input.Patch.RunID + "' applied) — " +
						"not a failure, but this tier cannot attest application",
				),
			},
			Outcome:        outcome,
			PatchedContent: nil,
		}, nil

	case patch.OutcomeUnanchored:
		indices := make([]string, 0, len(outcome.UnanchoredHunks))
		for _, hunk := range outcome.UnanchoredHunks {
			indices = append(indices, "hunk "+strconv.Itoa(hunk.Index)+" ("+hunk.Reason+")")
		}
		return ApplyCheckResult{
			Check: schemas.VerificationCheck{
				ID:     "patch-applies-cleanly",
				Status: schemas.CheckFail,
				Inputs: inputs,
				Detail: tier1DetailPtr(
					"patch is UNANCHORED — no application attempted (unanchored ⇒ suggest, never auto — " +
						"PLAN §5.6/ARCH §4.8): " + strings.Join(indices, "; "),
				),
			},
			Outcome:        outcome,
			PatchedContent: nil,
		}, nil

	default: // abstain
		return ApplyCheckResult{
			Check: schemas.VerificationCheck{
				ID:     "patch-applies-cleanly",
				Status: schemas.CheckFail,
				Inputs: inputs,
				Detail: tier1DetailPtr(
					"patch application abstained (" + string(outcome.AbstainReason) + "): " + outcome.Detail,
				),
			},
			Outcome:        outcome,
			PatchedContent: nil,
		}, nil
	}
}
