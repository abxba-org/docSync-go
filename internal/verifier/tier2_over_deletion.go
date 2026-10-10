package verifier

// Tier-2 over-deletion guard (port of tier2/over-deletion.ts; P4-T11; PLAN
// §5.4 item 11; ARCH §4.8 Tier-2): a patch may not DELETE a documentation
// region that still anchors a live claim whose source support is still present
// — deleting the claim text while the code it documents survives is
// over-deletion, not a legitimate removal.
//
// Evidence model: the STORE claims (`input.claims`, orchestrator-owned
// claim-store snapshot) are the guard's subjects — they are NOT decomposed
// claims, so the per-claim tracker is never used to grade them; the check
// collects violations instead. Each store claim's anchor is resolved against
// `input.docContent` with the core content-hash-first resolver
// (claims.ResolveAnchor): 'unchanged'/'re-anchored' give a resolved span;
// 'unanchored' claims are SKIPPED with a recorded note — never attributed a
// deletion, never silently re-anchored (invariant 13).
//
// Overlap test per (claim, hunk): the resolved span's lines intersect the
// hunk's deleted lines (delete_text lines mapped onto
// `start_line..start_line+deleted.length-1`) AND the hunk does not re-insert
// the claim's text (`insert_text` lacks the claim text verbatim). A violated
// claim whose `source.file` still exists in `input.workspaceSources` is
// over-deletion ⇒ the check FAILS naming claim id, text and source file
// (V-P4-7); support absent ⇒ a legitimate removal, recorded in detail, no
// failure. Decomposed claims linked to a violated store claim via
// `existing_claim_id` are graded 'over-deleted' on the tracker (the report's
// per-claim surface).
//
// Pure and deterministic (module doc): no fs, no git, no network, no clock of
// its own — the same inputs always yield deep-equal records.

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// overDeletionViolation is one store claim whose anchored span a deletion hunk
// removes.
type overDeletionViolation struct {
	claimID    string
	claimText  string
	spanStart  int
	spanEnd    int
	sourceFile string
}

// deletedSpan is a deleted line set for one hunk: 1-based document line numbers
// + insert text.
type deletedSpan struct {
	lines      map[int]bool
	insertText string
}

// docPathUnavailable is the docPath prefix used in tracker details naming the
// deleted span.
const docPathUnavailable = "<doc-path-unavailable>"

/* ------------------------------------------------------------------ helpers */

// tier2DeletedLinesFor is the deleted lines of one hunk: `delete_text` split
// into lines (the core document line convention) mapped onto the anchor range
// `start_line..start_line+deleted.length-1`.
func tier2DeletedLinesFor(hunkDeleteText string, hunkStartLine int) map[int]bool {
	deleted := claims.SplitDocLines(hunkDeleteText)
	lines := make(map[int]bool, len(deleted))
	for offset := range deleted {
		lines[hunkStartLine+offset] = true
	}
	return lines
}

// tier2ReinsertsClaimText is whether the hunk re-inserts the claim's text
// verbatim — the "equivalent claim text in the same hunk" test: the claim's
// stored text (its exact span text) appears in `insert_text`.
func tier2ReinsertsClaimText(insertText string, claimText string) bool {
	return claimText != "" && strings.Contains(insertText, claimText)
}

// tier2DocPathRender is the doc-path renderer shared by the tracker and check
// details (TS: `docPath === ” ? DOC_PATH_UNAVAILABLE : docPath`).
func tier2DocPathRender(docPath string) string {
	if docPath == "" {
		return docPathUnavailable
	}
	return docPath
}

/* ---------------------------------------------------------------- exports */

// CheckOverDeletionGuard runs the over-deletion guard over the store-claim
// snapshot. See the module doc for the evidence model. All outcomes emit one
// record; violations fail the check AND grade the linked decomposed claims
// 'over-deleted' on the tracker. Pure and deterministic (module doc).
//
// The tracker's fail-closed validation (unknown index / invalid status) is
// surfaced as an error instead of the TS throw.
func CheckOverDeletionGuard(
	input Tier2Input,
	tracker *ClaimStatusTracker,
) (schemas.VerificationCheck, error) {
	if input.Claims == nil {
		return schemas.VerificationCheck{
			ID:     "over-deletion-guard",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr("no claim-store snapshot supplied (orchestrator-owned data)"),
		}, nil
	}

	// Deletion hunks in patch-hunks array order (the TS Map's insertion order);
	// hunks are schema-enforced non-overlapping, so at most one hunk can
	// intersect a given claim span — the order is load-bearing only for the
	// detail text's stability.
	var deletedByHunk []deletedSpan
	if input.Patch != nil {
		for hunkIndex := range input.Patch.Hunks {
			hunk := &input.Patch.Hunks[hunkIndex]
			if hunk.DeleteText == "" {
				continue
			}
			deletedByHunk = append(deletedByHunk, deletedSpan{
				lines:      tier2DeletedLinesFor(hunk.DeleteText, hunk.Anchor.StartLine),
				insertText: hunk.InsertText,
			})
		}
	}
	if len(deletedByHunk) == 0 {
		return schemas.VerificationCheck{
			ID:     "over-deletion-guard",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr("patch deletes nothing"),
		}, nil
	}

	if input.DocContent == nil {
		return schemas.VerificationCheck{
			ID:     "over-deletion-guard",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr("document content absent — claim anchors unresolvable (fail-closed)"),
		}, nil
	}

	workspaceSources := input.WorkspaceSources
	docPath := ""
	if input.Patch != nil {
		docPath = input.Patch.DocPath
	}
	var violations []overDeletionViolation
	var legitimateRemovals []string
	var unresolvableNotes []string

	for _, claim := range input.Claims {
		resolution := claims.ResolveAnchor(claim.Anchor, *input.DocContent)
		if resolution.Status == claims.AnchorUnanchored {
			// Invariant 13: never attribute a deletion to an unresolvable
			// claim, never silently re-anchor — the claim is skipped with a
			// recorded note.
			unresolvableNotes = append(unresolvableNotes, fmt.Sprintf(
				"claim %s anchor unresolvable (%s) — no deletion attributed (invariant 13)",
				claim.ID, resolution.Reason))
			continue
		}

		spanLines := make(map[int]bool, resolution.EndLine-resolution.StartLine+1)
		for line := resolution.StartLine; line <= resolution.EndLine; line++ {
			spanLines[line] = true
		}

		for _, deleted := range deletedByHunk {
			intersects := false
			for line := range spanLines {
				if deleted.lines[line] {
					intersects = true
					break
				}
			}
			if !intersects {
				continue
			}
			if tier2ReinsertsClaimText(deleted.insertText, claim.Text) {
				break
			}
			if _, supportPresent := workspaceSources[claim.Source.File]; supportPresent {
				violations = append(violations, overDeletionViolation{
					claimID:    claim.ID,
					claimText:  claim.Text,
					spanStart:  resolution.StartLine,
					spanEnd:    resolution.EndLine,
					sourceFile: claim.Source.File,
				})
			} else {
				legitimateRemovals = append(legitimateRemovals, claim.ID)
			}
			break // one decisive overlap per claim; hunks are non-overlapping
		}
	}

	// Grade the decomposed claims linked to violated store claims (the report's
	// per-claim surface): existing_claim_id matches a violated store claim id.
	if len(violations) > 0 {
		decomposed := DecomposePatchClaims(input.Patch)
		violatedIDs := make(map[string]bool, len(violations))
		for _, violation := range violations {
			violatedIDs[violation.claimID] = true
		}
		for _, claim := range decomposed {
			if claim.ExistingClaimID == "" || !violatedIDs[claim.ExistingClaimID] {
				continue
			}
			var violation overDeletionViolation
			for _, entry := range violations {
				if entry.claimID == claim.ExistingClaimID {
					violation = entry
					break
				}
			}
			detail := fmt.Sprintf(
				"store claim %s over-deleted: deleted span %s %d-%d but support still present in %s",
				violation.claimID, tier2DocPathRender(docPath), violation.spanStart,
				violation.spanEnd, violation.sourceFile)
			if err := tracker.Set(claim.Index, schemas.ClaimVerdictOverDeleted, detail); err != nil {
				return schemas.VerificationCheck{}, err
			}
		}
	}

	notesSuffix := ""
	if len(unresolvableNotes) > 0 {
		notesSuffix = "; " + strings.Join(unresolvableNotes, "; ")
	}

	if len(violations) > 0 {
		violationDetails := make([]string, 0, len(violations))
		for _, violation := range violations {
			violationDetails = append(violationDetails, fmt.Sprintf(
				"claim %s (\"%s\") deleted at %s:%d-%d but support still present in %s",
				violation.claimID, violation.claimText, tier2DocPathRender(docPath),
				violation.spanStart, violation.spanEnd, violation.sourceFile))
		}
		return schemas.VerificationCheck{
			ID:     "over-deletion-guard",
			Status: schemas.CheckFail,
			Detail: tier2StringPtr(fmt.Sprintf("%d over-deleted claim(s): %s",
				len(violations), strings.Join(violationDetails, "; ")) + notesSuffix),
		}, nil
	}

	legitimateSuffix := ""
	if len(legitimateRemovals) > 0 {
		legitimateSuffix = fmt.Sprintf(", %d legitimate removal(s): %s (support absent)",
			len(legitimateRemovals), strings.Join(legitimateRemovals, ", "))
	}
	return schemas.VerificationCheck{
		ID:     "over-deletion-guard",
		Status: schemas.CheckPass,
		Detail: tier2StringPtr(fmt.Sprintf("no over-deletion: %d store claim(s) examined%s, %d unresolvable anchor note(s)%s",
			len(input.Claims), legitimateSuffix, len(unresolvableNotes), notesSuffix)),
	}, nil
}
