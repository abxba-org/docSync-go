// FN/FP capture flows (P10-T04; ARCH §8.7; CTX §22; PLAN §11.3 F10-3).
//
// Ported from core/src/feedback/fnfp.ts.
//
// FALSE NEGATIVE: Jev returned `docs_affected = false` (no docs affected) but
// the developer later edits a doc in the same PR — the decision missed a real
// documentation impact. Recorded as a hygiene label `true_negative` (the ARCH
// §8.7 negative-example marker for the evaluation dataset) bound to the
// originating `decision_id`, source signal `post_merge_edit`, rule
// `post_merge_edit⇒true_negative`.
//
// FALSE POSITIVE: a patch was rejected because the documentation was already
// correct (ARCH §8.7). Recorded as `false_positive` with rule
// `reject_reason:doc_already_correct⇒false_positive`, linked to the decision
// AND the rejected patch; any other reject reason is a style rejection
// (`reject_reason:other⇒style_rejected`), never reinterpreted as an FP.
//
// Both flows emit ordinary `docsync.hygiene-label/v1` rows (same schema, same
// append-only store) so the evaluation loop consumes ONE label vocabulary.
// Identity/time are injected (label ids default to canonical ULIDs; `at` is a
// required injected clock value — never new Date()).

package feedback

import "github.com/DUDE616/docSync-go/internal/schemas"

// DecisionRecord is a routed decision whose documentation impact Jev assessed
// (PLAN §11.3 F10-3; TS DecisionRecord).
type DecisionRecord struct {
	DecisionID string
	// DocsAffected is Jev's documentation-impact verdict — false is the FN
	// precondition.
	DocsAffected bool
	// PRNumber is the source PR the decision ran against (join key for later
	// doc edits).
	PRNumber int
	// PolicyVersion is the production policy version the decision was made
	// under (label field).
	PolicyVersion int
}

// LaterDocEdit is a developer edit to a documentation file observed after the
// decision ran (TS LaterDocEdit).
type LaterDocEdit struct {
	PRNumber int
	DocPath  string
	SHA      string
	// At is the ISO-8601 instant of the edit (injected clock value from the
	// capture layer).
	At string
}

// DetectFalseNegativesInput is the input of DetectFalseNegatives (TS
// DetectFalseNegativesInput).
type DetectFalseNegativesInput struct {
	Decisions     []DecisionRecord
	LaterDocEdits []LaterDocEdit
	// At is the ISO-8601 instant for `normalized_at` (injected clock value).
	At string
	// LabelID is an optional label-id factory; defaults to canonical ULIDs
	// (one per row).
	LabelID func() string
}

// DetectFalseNegatives detects false negatives: every `docs_affected = false`
// decision that has a later doc edit in the same PR yields ONE `true_negative`
// hygiene-label row per (decision, edited doc path), bound to the originating
// decision. Decisions with `docs_affected = true` never match (their doc edits
// are the ordinary corrected/accepted flow, not missed impact).
//
// Rows are returned unvalidated, exactly like the TypeScript flow: the
// HygieneLabelStore validates and persists them (fail closed at the write).
func DetectFalseNegatives(input DetectFalseNegativesInput) ([]schemas.HygieneLabel, error) {
	rows := []schemas.HygieneLabel{}
	for _, decision := range input.Decisions {
		if decision.DocsAffected {
			continue
		}
		editedPaths := map[string]bool{}
		for _, edit := range input.LaterDocEdits {
			if edit.PRNumber != decision.PRNumber {
				continue
			}
			if editedPaths[edit.DocPath] {
				continue
			}
			editedPaths[edit.DocPath] = true
			labelID := ""
			if input.LabelID != nil {
				labelID = input.LabelID()
			} else {
				generated, err := generateID("hygiene label")
				if err != nil {
					return nil, err
				}
				labelID = generated
			}
			rows = append(rows, schemas.HygieneLabel{
				Schema:        hygieneLabelSchemaID,
				LabelID:       labelID,
				DecisionID:    decision.DecisionID,
				PolicyVersion: decision.PolicyVersion,
				Label:         schemas.HygieneLabelTrueNegative,
				SourceSignal:  schemas.HygieneSourcePostMergeEdit,
				HygieneRule:   hygieneRulePostMergeEdit,
				NormalizedAt:  input.At,
			})
		}
	}
	return rows, nil
}

// RejectedPatchContext is a routed decision + rejected patch, for
// CaptureFalsePositive (TS RejectedPatchContext).
type RejectedPatchContext struct {
	DecisionID string
	// PolicyVersion is the production policy version the decision was made
	// under (label field).
	PolicyVersion int
}

// FalsePositiveCapture is the result of CaptureFalsePositive: the label plus
// its patch linkage (TS FalsePositiveCapture).
type FalsePositiveCapture struct {
	// Label is the normalized hygiene-label row (persist via the hygiene-label
	// store).
	Label schemas.HygieneLabel
	// PatchID is the rejected patch the label is linked to (schema-external
	// linkage).
	PatchID string
}

// CaptureFalsePositiveOptions is the input of CaptureFalsePositive beyond the
// decision/patch pair (TS CaptureFalsePositiveOptions).
type CaptureFalsePositiveOptions struct {
	// At is the ISO-8601 instant for `normalized_at` (injected clock value).
	At string
	// LabelID is the optional pre-assigned `label_id`; defaults to a canonical
	// ULID.
	LabelID *string
}

// CaptureFalsePositive captures a false positive from a patch rejection: when
// the stated reject reason IS "doc already correct" the row is
// `false_positive` (`reject_reason:doc_already_correct⇒false_positive`); ANY
// other reason is a style rejection (`reject_reason:other⇒style_rejected`).
// The returned capture links the label to the rejected patch id; the
// HygieneLabel schema itself carries the decision linkage (`decision_id`), so
// the patch linkage rides the caller's records alongside the persisted label
// row.
func CaptureFalsePositive(decision RejectedPatchContext, patchID, rejectReason string, options CaptureFalsePositiveOptions) (FalsePositiveCapture, error) {
	isFalsePositive := IsDocAlreadyCorrect(rejectReason)
	labelID := ""
	if options.LabelID != nil {
		labelID = *options.LabelID
	} else {
		generated, err := generateID("hygiene label")
		if err != nil {
			return FalsePositiveCapture{}, err
		}
		labelID = generated
	}
	label := schemas.HygieneLabel{
		Schema:        hygieneLabelSchemaID,
		LabelID:       labelID,
		DecisionID:    decision.DecisionID,
		PolicyVersion: decision.PolicyVersion,
		SourceSignal:  schemas.HygieneSourceReject,
		NormalizedAt:  options.At,
	}
	if isFalsePositive {
		label.Label = schemas.HygieneLabelFalsePositive
		label.HygieneRule = hygieneRuleRejectDocCorrect
	} else {
		label.Label = schemas.HygieneLabelStyleRejected
		label.HygieneRule = hygieneRuleRejectOther
	}
	return FalsePositiveCapture{Label: label, PatchID: patchID}, nil
}
