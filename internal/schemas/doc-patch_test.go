package schemas_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Ported from packages/schemas/test/doc-patch.test.ts — DocPatch schema tests
// for the claim-level edit objects (P11-T15, OD-R26): the OPTIONAL per-hunk
// `verified_claim_refs` and patch-level `verified_claims` fields parse on
// additive patches, every legacy patch (fields absent) parses unchanged
// (DX-P11-2), and the superRefine subset rule — every hunk's
// verified_claim_refs must be a subset of the patch-level verified_claims —
// fails closed naming the field and the offending ref.

func baseHunkMap() map[string]any {
	return map[string]any{
		"anchor": map[string]any{
			"start_line":   3,
			"end_line":     3,
			"content_hash": "67df5f2fc5a81b8885e723bab5e54435436ef2177c4144031511c7342c32b506",
		},
		"delete_text": "Refunds require an idempotency key.",
		"insert_text": "Refunds require an idempotency key for amounts above 10 USD.",
		"evidence_refs": []any{
			map[string]any{
				"kind": "symbol_diff",
				"ref":  "src/payments/refund.ts:42",
				"note": "idempotencyKey became optional",
			},
		},
	}
}

// patchWith mirrors the TS `patchWith(fields: Partial<DocPatch>)` helper.
func patchWith(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	base := map[string]any{
		"schema":            "docsync.doc-patch/v1",
		"run_id":            "acme/payments#8a73f2c..81af92e",
		"doc_path":          "docs/payments.md",
		"base_content_hash": "cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64",
		"hunks":             []any{baseHunkMap()},
		"evidence_refs":     []any{map[string]any{"kind": "symbol_diff", "ref": "src/payments/refund.ts:42", "note": "idempotencyKey became optional"}},
		"minimality":        map[string]any{"measured": true, "added_lines": 1, "removed_lines": 1},
		"provenance":        map[string]any{"policy_version": 17, "writer_model": "anthropic/claude-sonnet-4.5", "generated_at": "2026-10-06T09:40:00.000Z"},
	}
	for k, v := range fields {
		base[k] = v
	}
	data, err := json.Marshal(base)
	require.NoError(t, err)
	return data
}

func TestV_P11_T15_Positive_HunkRefInsidePatchLevelSetParses(t *testing.T) {
	// a hunk ref inside the patch-level set parses
	hunk := baseHunkMap()
	hunk["verified_claim_refs"] = []any{"claim_182"}
	additive := patchWith(t, map[string]any{
		"verified_claims": []any{"claim_182", "claim_9x"},
		"hunks":           []any{hunk},
	})
	parsed, err := schemas.ParseDocPatch(additive)
	require.NoError(t, err)
	assert.Equal(t, []string{"claim_182", "claim_9x"}, parsed.VerifiedClaims)
	assert.Equal(t, []string{"claim_182"}, parsed.Hunks[0].VerifiedClaimRefs)

	// fields present but empty arrays are legitimate (a hunk may carry the
	// field with an empty ref list)
	emptyHunk := baseHunkMap()
	emptyHunk["verified_claim_refs"] = []any{}
	empty := patchWith(t, map[string]any{
		"verified_claims": []any{},
		"hunks":           []any{emptyHunk},
	})
	_, err = schemas.ParseDocPatch(empty)
	require.NoError(t, err)
}

func TestV_P11_T15_Negative_HunkRefOutsidePatchLevelSetFails(t *testing.T) {
	hunk := baseHunkMap()
	hunk["verified_claim_refs"] = []any{"claim_182", "claim_ghost"}
	violated := patchWith(t, map[string]any{
		"verified_claims": []any{"claim_182"},
		"hunks":           []any{hunk},
	})
	_, err := schemas.ParseDocPatch(violated)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)

	var issue *schemas.Issue
	for i := range verr.Issues {
		if strings.Contains(verr.Issues[i].Message, "verified_claim_refs") {
			issue = &verr.Issues[i]
			break
		}
	}
	require.NotNil(t, issue, "expected an issue naming verified_claim_refs")
	assert.Contains(t, issue.Message, "verified_claim_refs contains 'claim_ghost'")
	assert.Contains(t, issue.Message, "patch-level verified_claims")
	assert.Contains(t, issue.Path, "verified_claim_refs")
}

func TestV_P11_T15_Negative_HunkRefsWithNoPatchLevelSetFail(t *testing.T) {
	hunk := baseHunkMap()
	hunk["verified_claim_refs"] = []any{"claim_182"}
	violated := patchWith(t, map[string]any{"hunks": []any{hunk}})
	_, err := schemas.ParseDocPatch(violated)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)

	found := false
	for _, iss := range verr.Issues {
		if strings.Contains(iss.Message, "'claim_182'") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected an issue naming the offending ref 'claim_182'")
}

func TestDocPatchExampleValidatesAndSerializes(t *testing.T) {
	// Registry example contract (V-P0-2): the shipped example validates.
	raw, err := json.Marshal(schemas.DocPatchExample)
	require.NoError(t, err)

	const expected = `{"schema":"docsync.doc-patch/v1","run_id":"acme/payments#8a73f2c..81af92e","doc_path":"docs/payments.md","base_content_hash":"cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64","hunks":[{"anchor":{"start_line":42,"end_line":42,"content_hash":"67df5f2fc5a81b8885e723bab5e54435436ef2177c4144031511c7342c32b506"},"delete_text":"Refunds require an idempotency key.","insert_text":"Refunds require an idempotency key for amounts above 10 USD.","evidence_refs":[{"kind":"symbol_diff","ref":"src/payments/refund.ts:42","note":"idempotencyKey became optional"}],"claim_ids":["claim_182"]}],"evidence_refs":[{"kind":"symbol_diff","ref":"src/payments/refund.ts:42","note":"idempotencyKey became optional"}],"minimality":{"measured":true,"added_lines":1,"removed_lines":1},"provenance":{"policy_version":17,"writer_model":"anthropic/claude-sonnet-4.5","generated_at":"2026-10-04T22:31:00Z"}}`
	assert.Equal(t, expected, string(raw))

	parsed, err := schemas.ParseDocPatch(raw)
	require.NoError(t, err)
	assert.Equal(t, schemas.DocPatchExample, *parsed)
}

func TestDocPatchHunkRangesMustBeStrictlyNonOverlapping(t *testing.T) {
	overlappingHunk := baseHunkMap()
	overlappingHunk["anchor"] = map[string]any{
		"start_line":   3,
		"end_line":     5,
		"content_hash": "67df5f2fc5a81b8885e723bab5e54435436ef2177c4144031511c7342c32b506",
	}
	violated := patchWith(t, map[string]any{"hunks": []any{baseHunkMap(), overlappingHunk}})
	_, err := schemas.ParseDocPatch(violated)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)

	var issue *schemas.Issue
	for i := range verr.Issues {
		if strings.Contains(verr.Issues[i].Message, "overlap in line ranges") {
			issue = &verr.Issues[i]
			break
		}
	}
	require.NotNil(t, issue)
	assert.Contains(t, issue.Message, "hunks 0 and 1 overlap in line ranges [3, 3] and [3, 5]")
	assert.Contains(t, issue.Message, "hunk line ranges must be strictly non-overlapping")
	assert.Equal(t, []string{"hunks"}, issue.Path)
}

func TestDocPatchBaseContentHashMustBeBare64Hex(t *testing.T) {
	violated := patchWith(t, map[string]any{
		"base_content_hash": "sha256:cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64",
	})
	_, err := schemas.ParseDocPatch(violated)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	found := false
	for _, iss := range verr.Issues {
		if strings.Contains(iss.Message, "base_content_hash must be bare 64-hex sha256 (no sha256: prefix)") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected the bare-hex refinement message")
}

func TestDocPatchEmptyHunksIsAProtocolViolation(t *testing.T) {
	violated := patchWith(t, map[string]any{"hunks": []any{}})
	_, err := schemas.ParseDocPatch(violated)
	require.Error(t, err)
	var verr *schemas.ValidationError
	require.ErrorAs(t, err, &verr)
	var issue *schemas.Issue
	for i := range verr.Issues {
		if len(verr.Issues[i].Path) == 1 && verr.Issues[i].Path[0] == "hunks" &&
			strings.Contains(verr.Issues[i].Message, "Array must contain at least 1 element(s)") {
			issue = &verr.Issues[i]
			break
		}
	}
	require.NotNil(t, issue, "an EMPTY patch is a protocol violation — a writer that confirms no drift ABSTAINS upstream")
}

func TestDocPatchUnknownKeysRefused(t *testing.T) {
	violated := patchWith(t, map[string]any{"extra": true})
	_, err := schemas.ParseDocPatch(violated)
	require.Error(t, err)
}

func TestDocPatchProvenanceGeneratedAtMustBeUTCDatetime(t *testing.T) {
	// z.string().datetime(): 'Z' suffix only — offsets are rejected
	// (provenance.test.ts pins "rejects non-UTC datetimes without offset
	// support"); the message is zod's default "Invalid datetime".
	for _, generatedAt := range []string{
		"2026-10-04T22:31:00+02:00",
		"not-a-datetime",
	} {
		violated := patchWith(t, map[string]any{
			"provenance": map[string]any{
				"policy_version": 17,
				"writer_model":   "anthropic/claude-sonnet-4.5",
				"generated_at":   generatedAt,
			},
		})
		_, err := schemas.ParseDocPatch(violated)
		require.Error(t, err, generatedAt)
		var verr *schemas.ValidationError
		require.ErrorAs(t, err, &verr)
		found := false
		for _, iss := range verr.Issues {
			if len(iss.Path) == 2 && iss.Path[0] == "provenance" && iss.Path[1] == "generated_at" &&
				iss.Message == "Invalid datetime" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected provenance.generated_at datetime issue for %q", generatedAt)
	}

	// the example's generated_at parses (Z, no fractional seconds)
	assert.Empty(t, schemas.DocPatchExample.Provenance.Validate())
}

/* --------------------------------- whole-document-rewrite rejection (P3-T07, invariant 12) */

const (
	rewriteBaseHash = "cf8a87e7256892cff86b6013ae8324a39ad497a017b5780da437740e1082dd64"
	rewriteHunkHash = "67df5f2fc5a81b8885e723bab5e54435436ef2177c4144031511c7342c32b506"
)

func evidence() schemas.Evidence {
	return schemas.Evidence{Kind: "symbol_diff", Ref: "src/payments/refund.ts:42", Note: "rewrite fixture"}
}

func docOfLines(n int) string {
	lines := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		lines = append(lines, "line-"+strconv.Itoa(i))
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}

func docPatchWithHunks(hunks ...schemas.PatchHunk) *schemas.DocPatch {
	hunkEvidence := evidence()
	for i := range hunks {
		hunks[i].EvidenceRefs = []schemas.Evidence{hunkEvidence}
	}
	return &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "rewrite-test",
		DocPath:         "docs/payments.md",
		BaseContentHash: rewriteBaseHash,
		Hunks:           hunks,
		EvidenceRefs:    []schemas.Evidence{hunkEvidence},
		Minimality:      schemas.DocPatchMinimality{Measured: true},
		Provenance:      schemas.DocPatchProvenance{PolicyVersion: 17, WriterModel: "anthropic/claude-sonnet-4.5", GeneratedAt: "2026-10-06T12:00:00.000Z"},
	}
}

func TestIsWholeDocumentRewriteRuleADeletionSpan(t *testing.T) {
	// 10 non-empty lines; a deletion hunk covering 10 of them ⇒ ratio 1.0.
	doc := docOfLines(10)
	body := strings.Join(strings.Split(doc, "\n")[:10], "\n")
	patch := docPatchWithHunks(schemas.PatchHunk{
		Anchor:     schemas.PatchAnchor{StartLine: 1, EndLine: 10, ContentHash: rewriteHunkHash},
		DeleteText: body,
		InsertText: "",
	})
	assert.True(t, schemas.IsWholeDocumentRewrite(patch, doc))
	assert.False(t, schemas.IsWholeDocumentRewrite(patch, "")) // empty document: nothing to rewrite

	err := schemas.AssertNotWholeDocumentRewrite(patch, doc)
	require.Error(t, err)
	var werr *schemas.WholeDocumentRewriteError
	require.True(t, errors.As(err, &werr))
	assert.Equal(t, schemas.WholeDocumentRewriteDeletionSpan, werr.Reason)
	assert.Equal(t, 1.0, werr.MeasuredRatio) // 10 of 10 non-empty lines covered
	assert.Equal(t, schemas.WholeDocumentRewriteErrorCode, werr.Code())
	assert.Equal(t,
		"whole-document rewrite rejected structurally (deletion_span): measured ratio 1 >= WHOLE_DOC_REWRITE_RATIO 0.9",
		werr.Error())

	require.NoError(t, schemas.AssertNotWholeDocumentRewrite(patch, ""))
}

func TestIsWholeDocumentRewriteRuleBStructuralDiff(t *testing.T) {
	// Rule A stays below the ratio (5 of 10 non-empty lines deleted); rule B
	// fires: replacing 5 lines with 5 fresh lines contributes 2*5 = 10 delta
	// over 10 non-empty lines ⇒ ratio 1.0.
	doc := docOfLines(10)
	lines := strings.Split(doc, "\n")
	deleted := strings.Join(lines[4:9], "\n")
	inserted := strings.Join([]string{"fresh-0", "fresh-1", "fresh-2", "fresh-3", "fresh-4"}, "\n")
	patch := docPatchWithHunks(schemas.PatchHunk{
		Anchor:     schemas.PatchAnchor{StartLine: 5, EndLine: 9, ContentHash: rewriteHunkHash},
		DeleteText: deleted,
		InsertText: inserted,
	})
	assert.True(t, schemas.IsWholeDocumentRewrite(patch, doc))

	err := schemas.AssertNotWholeDocumentRewrite(patch, doc)
	require.Error(t, err)
	var werr *schemas.WholeDocumentRewriteError
	require.True(t, errors.As(err, &werr))
	assert.Equal(t, schemas.WholeDocumentRewriteStructuralDiff, werr.Reason)
	assert.InDelta(t, 1.0, werr.MeasuredRatio, 1e-12)
}

func TestMeasureStructuralDiffRatioInsertionBoundary(t *testing.T) {
	// Reviewer case (patch-rule-b-parity.test.ts): a 10-line document with an
	// 8-line pure insertion measures 0.8 — not a rewrite; a 9-line insertion
	// measures 0.9 — a rewrite. Zero-width anchors are pure insertions
	// (removeCount 0).
	doc := docOfLines(10)

	patch8 := docPatchWithHunks(schemas.PatchHunk{
		Anchor:     schemas.PatchAnchor{StartLine: 1, EndLine: 1, ContentHash: rewriteHunkHash},
		DeleteText: "",
		InsertText: strings.Join([]string{"fresh-0", "fresh-1", "fresh-2", "fresh-3", "fresh-4", "fresh-5", "fresh-6", "fresh-7"}, "\n"),
	})
	assert.InDelta(t, 0.8, schemas.MeasureStructuralDiffRatio(patch8, doc), 1e-12)
	assert.False(t, schemas.IsWholeDocumentRewrite(patch8, doc))
	require.NoError(t, schemas.AssertNotWholeDocumentRewrite(patch8, doc))

	patch9 := docPatchWithHunks(schemas.PatchHunk{
		Anchor:     schemas.PatchAnchor{StartLine: 1, EndLine: 1, ContentHash: rewriteHunkHash},
		DeleteText: "",
		InsertText: strings.Join([]string{"fresh-0", "fresh-1", "fresh-2", "fresh-3", "fresh-4", "fresh-5", "fresh-6", "fresh-7", "fresh-8"}, "\n"),
	})
	assert.InDelta(t, 0.9, schemas.MeasureStructuralDiffRatio(patch9, doc), 1e-12)
	assert.True(t, schemas.IsWholeDocumentRewrite(patch9, doc))
}

func TestRewriteRejectionSchemaNoteAndRatioAreExportedConstants(t *testing.T) {
	assert.Equal(t, 0.9, schemas.WholeDocumentRewriteRatio)
	assert.Equal(t, "WHOLE_DOCUMENT_REWRITE", schemas.WholeDocumentRewriteErrorCode)
	assert.Contains(t, schemas.RewriteRejectionSchemaNote, "whole-document-rewrite rejection needs the document body")
	assert.Contains(t, schemas.RewriteRejectionSchemaNote, "assertNotWholeDocumentRewrite")
}
