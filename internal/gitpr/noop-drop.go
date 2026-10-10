package gitpr

// Ported from core/src/gitpr/noop-drop.ts (P6-T15; PLAN §7.3 line 1161;
// ARCH §12.3): no-op patch drop.
//
// If a prior doc PR already fixed the drift, the claims are re-evaluated
// against the new HEAD and the no-op patch is DROPPED — a patch whose
// application to the current head content is a byte-identical no-op contributes
// nothing.
//
// THE STRICT RULE (and its boundary with the rebase gate, P6-T14):
//
//   - noop === true IFF the Phase-3 engine's simulated application of the
//     patch against the head content concludes with the engine's own
//     content-level no-op (patch.OutcomeNoop): the prospective result is
//     byte-identical to the head content. That is the ONLY already-fixed-upstream
//     proof this module accepts — "the drift is already fixed" is a content
//     fact, never a guess.
//   - Apply "failures" (an unanchored outcome or an abstain outcome — anchors
//     that no longer resolve, a race the engine refuses to conclude, a guard
//     refusal) are NOT no-ops and NEVER treated as "upstream no longer matches
//     the patched claims → already fixed". An apply failure is INCONCLUSIVE: it
//     can equally mean legitimate rebase drift, which is the REBASE GATE's
//     territory (3-way via the engine's race path + re-verification, A9).
//     Failing closed here means: noop === false with a boundary-naming detail,
//     the patch stays in the pipeline, and the rebase gate decides.
//   - A genuine applied outcome (the patch changes the head content) is real
//     drift → noop === false, the patch proceeds.
//
// Pure SIMULATION: the engine is invoked with the head content pre-supplied
// (WorktreeContent), no ReVerify port (a race merge cannot ship here — fail
// closed), and no git read ever fires. No writes, no clock beyond the
// injected/constant stamp (P19).

import (
	"context"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// NoopDropDetail is the exact no-op detail (PLAN §12.3 wording).
const NoopDropDetail = "drift already fixed upstream — no-op patch dropped (PLAN §12.3)"

// noopDropSimulationRev is the sentinel rev the simulation runs against: the
// engine never reads the repository through it (WorktreeContent is always
// supplied), it only satisfies the engine's nonempty-rev contract.
const noopDropSimulationRev = "noop-drop-simulation"

// noopDropBoundary names the strict rule's boundary with the rebase gate.
const noopDropBoundary = "apply failures are rebase-gate territory — the 3-way + re-verification path decides; " +
	"a failed apply is never guessed into a no-op (P6-T15 strict rule)"

// NoOpDropResult is the result of EvaluateNoOpDrop.
type NoOpDropResult struct {
	// Noop is true ONLY when the simulated application is byte-identical
	// (strict rule).
	Noop bool `json:"noop"`
	// Detail is rule-naming detail (drop reports / run records).
	Detail string `json:"detail"`
}

// EvaluateNoOpDropInput is the input of EvaluateNoOpDrop.
type EvaluateNoOpDropInput struct {
	// Patch is the candidate patch to simulate against the head content.
	Patch *schemas.DocPatch
	// HeadContent is the document content at the current head (the
	// re-evaluated claims' target).
	HeadContent string
	// BaseContent is the writer's base snapshot (its hash must equal the
	// patch's base_content_hash). REQUIRED to reach the engine's race path:
	// when the head content already carries an IDENTICAL fix, the engine
	// detects the content race and concludes noop ('already-applied') exactly
	// when the patch's intended result is byte-identical to the head. Omitted →
	// a raced head (hash ≠ base_content_hash) abstains inside the engine →
	// noop === false (strict boundary — the rebase gate owns raced heads).
	BaseContent *string
	// ManifestAllowlist is the MANIFEST-derived writable-path allowlist handed
	// to the engine (defense in depth). OPTIONAL: the simulation performs NO
	// writes and invents no paths — the default is a self-scoped set over the
	// patch's own doc_path (the candidate was already allowlist-gated
	// upstream); a guard refusal still resolves to noop === false (fail
	// closed), never to a no-op.
	ManifestAllowlist map[string]bool
	// Now is the INJECTED engine clock (P19 — never a local clock); fixed epoch
	// by default.
	Now func() string
}

// noopDropFrozenEpoch is the fixed epoch of the default engine clock: the
// outcome carries no timestamp, so a constant stands in and no wall clock is
// ever read (P19 — the only `time`-free way to prove it).
const noopDropFrozenEpoch = "1970-01-01T00:00:00.000Z"

// noopDropConstantNow is the fixed epoch for the default engine clock
// (P19: no wall clock in the module).
func noopDropConstantNow() string {
	return noopDropFrozenEpoch
}

// noopDropNeverReadGit is a git stub that must never fire: the simulation
// always supplies the head content, so the engine never reads the repository.
// If it ever fires, the simulation is miswired — fail loud instead of
// fabricating content.
type noopDropNeverReadGit struct{}

// CatFile is the never-firing read port (patch.CatFilePort).
func (noopDropNeverReadGit) CatFile(context.Context, string, string) (gitx.CatFileResult, error) {
	return gitx.CatFileResult{}, docutil.New(docutil.CodeInternal,
		"noop-drop: the simulation always supplies head content — git must never be read")
}

// noopDropError reports a malformed input. The TS threw a bare TypeError (the
// duties.ts precedent); per CONVENTIONS.md §Errors the closest registered
// docutil code carries it while the message text is preserved.
func noopDropError(msg string) error {
	return docutil.New(docutil.CodeValidation, msg)
}

// EvaluateNoOpDrop evaluates the P6-T15 no-op drop (PLAN §7.3 line 1161):
// simulate the patch against the head content through the Phase-3 engine and
// conclude a no-op ONLY on a byte-identical apply result. See the module doc
// for the strict rule and the rebase-gate boundary.
func EvaluateNoOpDrop(ctx context.Context, input EvaluateNoOpDropInput) (NoOpDropResult, error) {
	if input.Patch == nil || input.Patch.DocPath == "" {
		return NoOpDropResult{}, noopDropError(
			"evaluateNoOpDrop input.patch must be a DocPatch (doc_path present)")
	}
	if input.Now == nil {
		input.Now = noopDropConstantNow
	}
	allowlist := input.ManifestAllowlist
	if allowlist == nil {
		allowlist = map[string]bool{input.Patch.DocPath: true}
	}

	outcome, err := patch.ApplyDocPatch(ctx, patch.ApplyDocPatchInput{
		Patch:             input.Patch,
		Git:               noopDropNeverReadGit{},
		Rev:               noopDropSimulationRev,
		WorktreeContent:   &input.HeadContent,
		ManifestAllowlist: allowlist,
		BaseContent:       input.BaseContent,
		Now:               input.Now,
		// ReVerify is deliberately nil: a race merge cannot ship here (fail
		// closed) — the rebase gate owns raced heads.
	})
	if err != nil {
		return NoOpDropResult{}, err
	}

	switch outcome.Kind {
	case patch.OutcomeNoop:
		// The ONLY already-fixed proof: the engine's content-level no-op — the
		// prospective result is byte-identical to the head content.
		return NoOpDropResult{Noop: true, Detail: NoopDropDetail}, nil
	case patch.OutcomeApplied:
		return NoOpDropResult{
			Noop: false,
			Detail: "genuine drift — the patch changes the head content (P6-T15: only a " +
				"byte-identical apply result is a no-op)",
		}, nil
	case patch.OutcomeUnanchored:
		reasons := make([]string, 0, len(outcome.UnanchoredHunks))
		for _, hunk := range outcome.UnanchoredHunks {
			reasons = append(reasons, "hunk "+strconv.Itoa(hunk.Index)+": "+hunk.Reason)
		}
		return NoOpDropResult{
			Noop: false,
			Detail: "patch anchors do not resolve against the head content (" +
				strings.Join(reasons, "; ") + ") — inconclusive, NOT treated as already-fixed (" +
				noopDropBoundary + ")",
		}, nil
	case patch.OutcomeAbstain:
		return NoOpDropResult{
			Noop: false,
			Detail: "apply simulation abstained (" + string(outcome.AbstainReason) + ": " +
				outcome.Detail + ") — inconclusive, NOT treated as already-fixed (" +
				noopDropBoundary + ")",
		}, nil
	default:
		return NoOpDropResult{}, docutil.New(docutil.CodeInternal,
			"noop-drop: unhandled apply outcome kind '"+string(outcome.Kind)+"' — fail closed")
	}
}
