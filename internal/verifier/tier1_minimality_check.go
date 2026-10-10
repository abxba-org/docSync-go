package verifier

// Ported from packages/verifier/src/tier1/minimality-check.ts (P4-T04; PLAN
// §5.4 item 4; ARCH §4.9 minimality + invariant 12). SEPARATE check records
// by design so V-P4-2 (oversized) and V-P4-3 (rewrite) each get their own
// tier-1 evidence:
//
//   - minimality-bound: when a patched content EXISTS (application simulated),
//     the verifier MEASURES (never trusts the writer's self-report) with
//     patch.MeasureMinimality and FAILS when the measured ratio crosses the
//     bound — this is the hard rejection Phase 3 deliberately deferred to Tier 1
//     (patch/minimality.go deferral note). Detail quotes measured added/removed
//     lines, ratio, and the bound. No patched content ⇒ 'skipped' — the
//     simulation was impossible and the clean-apply failure already rejects.
//   - whole-document-rewrite: PURE over (patch, docContent) — the same
//     structural predicate the applier prechecked — and therefore runs
//     INDEPENDENT of application success whenever the document content is
//     available: a rewrite that also abstained the apply still gets its own
//     V-P4-3 fail record with reason + measured ratio (two failure records for
//     one rewrite patch: the apply abstention AND the structural rule). This
//     is the resolution of the spec's "skipped when apply failed" clause for
//     this check: the rewrite predicate needs no simulation, and V-P4-3
//     requires the fail record regardless.

import (
	"errors"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// MinimalityCheckResult carries the two separate records plus the measurement
// (present when the bound check ran, not skipped).
type MinimalityCheckResult struct {
	Bound       schemas.VerificationCheck
	Rewrite     schemas.VerificationCheck
	Measurement *patch.MinimalityMeasurement
}

// CheckMinimalityAndRewrite runs the minimality-bound and
// whole-document-rewrite checks. The error return is reserved for the
// unexpected non-rewrite failure the TS tier rethrows (the structural
// predicate only ever produces a *schemas.WholeDocumentRewriteError).
func CheckMinimalityAndRewrite(input Tier1Input, patchedContent *string) (MinimalityCheckResult, error) {
	var docContent *string
	if input.DocContent != nil {
		content := *input.DocContent
		docContent = &content
	}

	/* -- whole-document-rewrite: pure over (patch, docContent) ---------------- */
	var rewrite schemas.VerificationCheck
	if docContent == nil {
		rewrite = schemas.VerificationCheck{
			ID:     "whole-document-rewrite",
			Status: schemas.CheckSkipped,
			Detail: tier1DetailPtr(
				"document content absent (docContent null) — the structural rewrite predicate cannot run",
			),
		}
	} else {
		check, err := rewriteCheck(input.Patch, *docContent)
		if err != nil {
			return MinimalityCheckResult{}, err
		}
		rewrite = check
	}

	/* -- minimality-bound: needs the simulated patched content ---------------- */
	if patchedContent == nil || docContent == nil {
		return MinimalityCheckResult{
			Bound: schemas.VerificationCheck{
				ID:     "minimality-bound",
				Status: schemas.CheckSkipped,
				Detail: tier1DetailPtr(
					"patch application was not simulated successfully — the actual content change cannot be " +
						"measured (fail-closed: the clean-apply failure already rejects the patch; no bound " +
						"measurement is fabricated)",
				),
			},
			Rewrite:     rewrite,
			Measurement: nil,
		}, nil
	}

	measurement, err := patch.MeasureMinimality(patch.MeasureMinimalityInput{
		Patch:               input.Patch,
		CurrentContent:      *docContent,
		PatchedContent:      *patchedContent,
		TriggeringDiffStats: input.TriggeringDiffStats,
		MaxRatio:            input.MinRatio,
	})
	if err != nil {
		// Malformed measurement inputs (non-finite bound, negative diff stats)
		// cannot even be measured — fail the check with the reason as evidence
		// instead of crashing the tier (fail closed, invariant 9 posture).
		// The message text is preserved verbatim (the docutil code prefix the
		// coded error adds is not part of the TS message).
		message := tier1ErrorMessage(err)
		return MinimalityCheckResult{
			Bound: schemas.VerificationCheck{
				ID:     "minimality-bound",
				Status: schemas.CheckFail,
				Detail: tier1DetailPtr("minimality measurement could not run: " + message),
			},
			Rewrite:     rewrite,
			Measurement: nil,
		}, nil
	}

	var bound schemas.VerificationCheck
	if measurement.Oversized {
		triggerAdded, triggerRemoved := 0, 0
		if input.TriggeringDiffStats != nil {
			triggerAdded = input.TriggeringDiffStats.Added
			triggerRemoved = input.TriggeringDiffStats.Removed
		}
		bound = schemas.VerificationCheck{
			ID:     "minimality-bound",
			Status: schemas.CheckFail,
			Detail: tier1DetailPtr(
				"measured patch change is oversized: added " + strconv.Itoa(measurement.Minimality.AddedLines) +
					" / removed " + strconv.Itoa(measurement.Minimality.RemovedLines) + " lines against triggering diff " +
					strconv.Itoa(triggerAdded) + "+/ " + strconv.Itoa(triggerRemoved) + "- — measured ratio " +
					tier1FormatRatio(measurement.Ratio) + " exceeds the minimality bound " +
					tier1FormatBound(input.MinRatio) +
					" (V-P4-2: hard rejection at tier 1, deferred from the application layer)",
			),
		}
	} else {
		bound = schemas.VerificationCheck{ID: "minimality-bound", Status: schemas.CheckPass}
	}

	return MinimalityCheckResult{Bound: bound, Rewrite: rewrite, Measurement: &measurement}, nil
}

// rewriteCheck is the structural rewrite verdict with evidence: reason
// ('deletion_span' | 'structural_diff') + measured ratio in the detail
// (V-P4-3).
func rewriteCheck(patchArg *schemas.DocPatch, docContent string) (schemas.VerificationCheck, error) {
	err := schemas.AssertNotWholeDocumentRewrite(patchArg, docContent)
	if err == nil {
		return schemas.VerificationCheck{ID: "whole-document-rewrite", Status: schemas.CheckPass}, nil
	}
	var rewriteErr *schemas.WholeDocumentRewriteError
	if errors.As(err, &rewriteErr) {
		return schemas.VerificationCheck{
			ID:     "whole-document-rewrite",
			Status: schemas.CheckFail,
			Detail: tier1DetailPtr(
				"whole-document rewrite rejected outright (" + string(rewriteErr.Reason) + "): measured ratio " +
					tier1NumberString(rewriteErr.MeasuredRatio) + " >= WHOLE_DOC_REWRITE_RATIO (invariant 12: " +
					"minimal diffs only — content quality is irrelevant, a document-replacing patch is never a " +
					"minimal patch)",
			),
		}, nil
	}
	return schemas.VerificationCheck{}, err
}

// tier1FormatRatio renders the measured ratio the way TS String(ratio) did
// (null ⇒ not measurable).
func tier1FormatRatio(ratio *float64) string {
	if ratio == nil {
		return "(no triggering diff — not measurable)"
	}
	return tier1NumberString(*ratio)
}

// tier1FormatBound renders the minimality bound (TS String(minRatio ??
// DEFAULT_MINIMALITY_MAX_RATIO)).
func tier1FormatBound(minRatio *float64) string {
	if minRatio == nil {
		return tier1NumberString(patch.DefaultMinimalityMaxRatio)
	}
	return tier1NumberString(*minRatio)
}

// tier1NumberString renders a float exactly like the TS String() of a number
// (shortest round-trip form) so check details stay byte-identical.
func tier1NumberString(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// tier1ErrorMessage extracts the TS `error.message` text from a coded error: the
// docutil code prefix the coded error adds is not part of the message.
func tier1ErrorMessage(err error) string {
	var coded *docutil.Error
	if errors.As(err, &coded) {
		return coded.Msg
	}
	return err.Error()
}
