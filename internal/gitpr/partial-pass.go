package gitpr

// Ported from core/src/gitpr/partial-pass.ts (P6-T10; PLAN §7.3 line 1143; D4;
// ARCH §12.2): per-document partial-pass PR assembly.
//
// A single failing document's verification must NOT block the others'
// merge-worthiness — the failing doc is DROPPED from the PR and REPORTED, never
// silently.
//
// This module is the PR-ASSEMBLY VIEW of that rule. It is deliberately pure and
// evidence-consuming: the pipeline (Phase 5 orchestrator) has already recorded
// per-document verification evidence in the RunOutcome; the RunOutcome is NOT
// mutated here. What this module produces is (a) which candidates ride into the
// doc PR (included) and (b) which are dropped with a non-empty reason + detail
// so the drop surfaces in the PR body report (RenderPartialPassReport) — a
// dropped doc is a reported doc.
//
// Determinism (ARCH §13): input order is preserved for BOTH lists; same input →
// identical result and byte-identical rendered report. No clocks, no I/O, no
// model calls (P19).

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// PartialPassCandidate is one PR-assembly candidate: a document the pipeline
// proposed and verified. Verified is the GATE (the policy stage's verdict,
// already recorded in the RunOutcome); Verification is the evidence report when
// one exists (nil when the verifier never produced one — e.g. it failed or
// abstained upstream). Detail is optional caller-provided context carried
// VERBATIM into the drop report when the candidate is dropped.
type PartialPassCandidate struct {
	// DocPath is the repo-relative manifest document path.
	DocPath string `json:"doc_path"`
	// Verified is the verification gate: false ⇒ the doc is dropped from the PR.
	Verified bool `json:"verified"`
	// Verification is the verification report when one exists (evidence, never
	// re-derived). Absent ⇒ nil (TS: `VerificationReport | null`).
	Verification *schemas.VerificationReport `json:"-"`
	// Detail is optional caller context, carried verbatim into the drop detail.
	Detail string `json:"detail,omitempty"`
}

// PartialPassIncludedDoc is one document that rides into the PR (verification
// gate passed).
type PartialPassIncludedDoc struct {
	DocPath string `json:"doc_path"`
	// PatchRef is the verified patch's reference, when the evidence report
	// carries one (nil ⇒ absent; evidence-carried, never invented).
	PatchRef *string `json:"patch_ref,omitempty"`
}

// PartialPassDroppedDoc is one document DROPPED from the PR — always with
// non-empty reason + detail.
type PartialPassDroppedDoc struct {
	DocPath string `json:"doc_path"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail"`
}

// PartialPassResult is the result of AssemblePartialPass (the PR-assembly view,
// pure data).
type PartialPassResult struct {
	// Included is the gate-passing documents, in input order (deterministic).
	Included []PartialPassIncludedDoc `json:"included"`
	// Dropped is the gate-failing documents, in input order, each with non-empty
	// reason/detail.
	Dropped []PartialPassDroppedDoc `json:"dropped"`
}

// PartialPassDropReason is the stable machine-readable drop reason for a failed
// verification gate.
const PartialPassDropReason = "verification_not_passed"

// Non-empty fallback drop details when the candidate carries no detail.
const (
	dropDetailNoReport  = "no verification evidence — dropped from the PR (partial pass, D4: reported, never silently)"
	dropDetailNotPassed = "verification did not pass — dropped from the PR (partial pass, D4: reported, never silently)"
)

// partialPassCandidateError reports a malformed candidate. The TS threw a bare
// TypeError (the duties.ts precedent); per CONVENTIONS.md §Errors the closest
// registered docutil code carries it while the message text is preserved.
func partialPassCandidateError(msg string) error {
	return docutil.New(docutil.CodeValidation, msg)
}

// assertPartialPassCandidate is the fail-closed input validation (D8): a
// malformed candidate is a bug, never a silently-skippable entry. The TS also
// rejected a non-object/array candidate and a non-boolean `verified`; Go's
// struct types make both unrepresentable, so docPath is the only checkable
// shape here.
func assertPartialPassCandidate(candidate PartialPassCandidate, index int) error {
	if candidate.DocPath == "" {
		return partialPassCandidateError(
			"candidates[" + strconv.Itoa(index) + "].docPath must be a nonempty string")
	}
	return nil
}

// AssemblePartialPass assembles the partial-pass view (P6-T10). Pure and
// deterministic: input order is preserved for both lists; a `verified: false`
// candidate is DROPPED with a non-empty reason + detail (the report entry),
// included docs are unaffected, and no RunOutcome is touched here — the
// pipeline already recorded the evidence; this is the PR-assembly projection of
// it.
//
// `patchRef` for an included doc comes from its verification report's
// `patch_ref` when the report exists (evidence-carried, never invented); a
// verified doc without a report is included WITHOUT a patchRef.
func AssemblePartialPass(candidates []PartialPassCandidate) (PartialPassResult, error) {
	result := PartialPassResult{
		Included: []PartialPassIncludedDoc{},
		Dropped:  []PartialPassDroppedDoc{},
	}
	for index, candidate := range candidates {
		if err := assertPartialPassCandidate(candidate, index); err != nil {
			return PartialPassResult{}, err
		}

		if candidate.Verified {
			included := PartialPassIncludedDoc{DocPath: candidate.DocPath}
			if candidate.Verification != nil && candidate.Verification.PatchRef != "" {
				patchRef := candidate.Verification.PatchRef
				included.PatchRef = &patchRef
			}
			result.Included = append(result.Included, included)
			continue
		}

		detail := strings.TrimSpace(candidate.Detail)
		if detail == "" {
			if candidate.Verification != nil {
				detail = "verification verdict '" + string(candidate.Verification.Verdict) +
					"': " + dropDetailNotPassed
			} else {
				detail = dropDetailNoReport
			}
		}
		result.Dropped = append(result.Dropped, PartialPassDroppedDoc{
			DocPath: candidate.DocPath,
			Reason:  PartialPassDropReason,
			Detail:  detail,
		})
	}
	return result, nil
}

// partialPassLineBreaks matches one run of CR/LF characters (TS: /[\r\n]+/g).
var partialPassLineBreaks = regexp.MustCompile(`[\r\n]+`)

// partialPassCell is the markdown-table cell renderer: escape pipes + collapse
// line breaks (pr-body.ts rule).
func partialPassCell(value string) string {
	escaped := strings.ReplaceAll(value, "|", "\\|")
	return partialPassLineBreaks.ReplaceAllString(escaped, " ")
}

// Partial-pass section headings and table headers (kept verbatim from the TS
// renderer so the composed PR body is byte-identical).
const (
	partialPassIncludedHeading = "Included documents (verification passed — merge-worthy):"
	partialPassDroppedHeading  = "Dropped documents (verification not passed — reported, never silently):"
	partialPassIncludedTable   = "| Document | Patch ref |\n| --- | --- |"
	partialPassDroppedTable    = "| Document | Reason | Detail |\n| --- | --- | --- |"
	partialPassNone            = "None."
)

// RenderPartialPassReport renders the deterministic partial-pass markdown
// section for the doc-PR body (P6-T03 composition): BOTH lists are always
// rendered, explicit 'None.' when empty — a drop is REPORTED, never silently
// swallowed (PLAN §7.3; D4). Same input → byte-identical output (P19, no
// clocks/IO).
func RenderPartialPassReport(result PartialPassResult) string {
	lines := []string{"## Partial pass (D4)", "", partialPassIncludedHeading, ""}
	if len(result.Included) == 0 {
		lines = append(lines, partialPassNone, "")
	} else {
		lines = append(lines, strings.Split(partialPassIncludedTable, "\n")...)
		for _, doc := range result.Included {
			patchRef := "—"
			if doc.PatchRef != nil {
				patchRef = "`" + partialPassCell(*doc.PatchRef) + "`"
			}
			lines = append(lines, "| `"+partialPassCell(doc.DocPath)+"` | "+patchRef+" |")
		}
		lines = append(lines, "")
	}
	lines = append(lines, partialPassDroppedHeading, "")
	if len(result.Dropped) == 0 {
		lines = append(lines, partialPassNone, "")
	} else {
		lines = append(lines, strings.Split(partialPassDroppedTable, "\n")...)
		for _, doc := range result.Dropped {
			lines = append(lines, "| `"+partialPassCell(doc.DocPath)+"` | "+
				partialPassCell(doc.Reason)+" | "+partialPassCell(doc.Detail)+" |")
		}
		lines = append(lines, "")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), " \t\r\n") + "\n"
}
