package patch_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func TestMeasuredMinimality_Convenience(t *testing.T) {
	m := patch.MeasuredMinimality("alpha\nbeta\n\nbeta\n", "gamma\nalpha\ngamma\n")
	require.Equal(t, 2, m.AddedLines)
	require.Equal(t, 2, m.RemovedLines)
	require.True(t, m.Measured)
}

func TestMeasuredMinimality_Identical(t *testing.T) {
	m := patch.MeasuredMinimality("same\ncontent\n", "same\ncontent\n")
	require.Equal(t, 0, m.AddedLines)
	require.Equal(t, 0, m.RemovedLines)
}

func TestPatchEngineError_CodeAndUnwrap(t *testing.T) {
	p := &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "test-pe",
		DocPath:         "docs/payments.md",
		BaseContentHash: docutil.ContentHash(doc),
		Hunks:           nil,
		EvidenceRefs:    defaultDocPatchEvidence(),
		Minimality:      schemas.DocPatchMinimality{Measured: true},
		Provenance:      schemas.DocPatchProvenance{PolicyVersion: 17, WriterModel: "double", GeneratedAt: NOW()},
	}
	in := baseApplyInput(p)
	in.WorktreeContent = strp(doc)
	_, err := patch.ApplyDocPatch(context.Background(), in)
	require.Error(t, err)
	var pe *patch.PatchEngineError
	require.True(t, errors.As(err, &pe))
	require.Equal(t, "EDOCSYNC_CONFIG", pe.Code())
	require.Error(t, pe.Unwrap())
}

func TestApplyDocPatch_EmptyRev(t *testing.T) {
	p := makeDocPatch(docPatchOpts{
		baseContent: doc,
		hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 3, deleteText: docLines[2]})},
	})
	in := baseApplyInput(p)
	in.Rev = ""
	_, err := patch.ApplyDocPatch(context.Background(), in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be a nonempty string")
}

func TestApplyDocPatch_NilPatch(t *testing.T) {
	in := baseApplyInput(nil)
	_, err := patch.ApplyDocPatch(context.Background(), in)
	require.Error(t, err)
}

func TestApplyDocPatch_EmptyDocPath(t *testing.T) {
	p := &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           "test-empty-docpath",
		DocPath:         "",
		BaseContentHash: docutil.ContentHash(doc),
		Hunks:           []schemas.PatchHunk{hunk(hunkOpts{start: 3, deleteText: docLines[2]})},
		EvidenceRefs:    defaultDocPatchEvidence(),
		Minimality:      schemas.DocPatchMinimality{Measured: true},
		Provenance:      schemas.DocPatchProvenance{PolicyVersion: 17, WriterModel: "double", GeneratedAt: NOW()},
	}
	in := baseApplyInput(p)
	in.WorktreeContent = strp(doc)
	_, err := patch.ApplyDocPatch(context.Background(), in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "doc_path")
}

func TestApplyDocPatch_BaseContentMismatch(t *testing.T) {
	largeDocLines := make([]string, 10)
	for i := range largeDocLines {
		largeDocLines[i] = "line-" + docutil.ContentHash(string(rune(i)))[:8]
	}
	largeDoc := strings.Join(largeDocLines, "\n") + "\n"
	changedLines := make([]string, 10)
	copy(changedLines, largeDocLines)
	changedLines[0] = "CHANGED-" + docutil.ContentHash(string(rune(0)))[:8]
	changedDoc := strings.Join(changedLines, "\n") + "\n"
	p := makeDocPatch(docPatchOpts{
		baseContent: largeDoc,
		hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 3, deleteText: largeDocLines[2], insert: "modified-line-3"})},
	})
	in := baseApplyInput(p)
	in.WorktreeContent = strp(changedDoc)
	in.BaseContent = strp("totally unrelated base content\n")
	_, err := patch.ApplyDocPatch(context.Background(), in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "caller contract violation")
}

func TestApplyDocPatch_DeleteOneLine(t *testing.T) {
	largeDocLines := make([]string, 10)
	for i := range largeDocLines {
		largeDocLines[i] = "line-" + docutil.ContentHash(string(rune(i)))[:8]
	}
	largeDoc := strings.Join(largeDocLines, "\n") + "\n"
	p := makeDocPatch(docPatchOpts{
		baseContent: largeDoc,
		hunks:       []schemas.PatchHunk{hunk(hunkOpts{start: 1, deleteText: largeDocLines[0], insert: ""})},
		runID:       "delete-one",
	})
	in := baseApplyInput(p)
	in.ManifestAllowlist = map[string]bool{"docs/payments.md": true}
	in.WorktreeContent = strp(largeDoc)
	o := mustApply(t, in)
	require.Equal(t, patch.OutcomeApplied, o.Kind)
	require.NotContains(t, o.ResultContent, largeDocLines[0])
}

func TestThreeWayMerge_BoundaryInsertionAndReplacement(t *testing.T) {
	// Ours: insertion at a boundary, Theirs: replacement covering that boundary
	// The insertion should go first, then the replacement
	base := withTrailing("line1\nline2\nline3\nline4\n")
	ours := withTrailing("LINE0\nline1\nline2\nline3\nline4\n") // insertion at line 1
	theirs := withTrailing("line1\nEDITED2\nEDITED3\nline4\n")  // replacement of lines 2-3
	result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
	require.True(t, result.Ok, "boundary insertion+replacement should merge cleanly")
	require.Equal(t, withTrailing("LINE0\nline1\nEDITED2\nEDITED3\nline4\n"), result.Merged)
}

func TestThreeWayMerge_ViceVersa(t *testing.T) {
	// Theirs: insertion at a boundary, Ours: replacement covering that boundary
	base := withTrailing("line1\nline2\nline3\nline4\n")
	theirs := withTrailing("LINE0\nline1\nline2\nline3\nline4\n") // insertion at line 1
	ours := withTrailing("line1\nEDITED2\nEDITED3\nline4\n")      // replacement of lines 2-3
	result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
	require.True(t, result.Ok, "boundary insertion+replacement should merge cleanly in either order")
	require.Equal(t, withTrailing("LINE0\nline1\nEDITED2\nEDITED3\nline4\n"), result.Merged)
}

func TestThreeWayMerge_OneSideOnlyChanges(t *testing.T) {
	base := withTrailing("line1\nline2\nline3\n")
	ours := withTrailing("MODIFIED1\nline2\nline3\n") // only ours changes line 1
	theirs := base                                    // theirs has nothing
	result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
	require.True(t, result.Ok)
	require.Equal(t, ours, result.Merged)

	// Only theirs changes
	theirs2 := withTrailing("THEIR_MODIFIED1\nline2\nline3\n")
	ours2 := base
	result2 := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours2, Theirs: theirs2})
	require.True(t, result2.Ok)
	require.Equal(t, theirs2, result2.Merged)
}

func TestThreeWayMerge_BothSidesSameInsertionAtEnd(t *testing.T) {
	base := withTrailing("line1\n")
	appended := base + "APPENDED\n"
	result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: appended, Theirs: appended})
	require.True(t, result.Ok)
	require.Equal(t, appended, result.Merged)
}

func TestThreeWayMerge_FullAppendInsertionDiffEdges(t *testing.T) {
	// Pure end insertion (side longer than base) with no open run
	base := "line1\nline2\n"
	ours := "line1\nline2\nappended\n"
	theirs := "line1\nline2\nappended\n"
	result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: ours, Theirs: theirs})
	require.True(t, result.Ok)
	require.Equal(t, ours, result.Merged)
}

func TestThreeWayMerge_IdenticalMultiLineContentConflictAvoidance(t *testing.T) {
	// Both sides change the same lines to identical multi-line content
	base := withTrailing("original1\noriginal2\noriginal3\n")
	changed := withTrailing("changed1\nchanged2\noriginal3\n")
	result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: changed, Theirs: changed})
	require.True(t, result.Ok)
	require.Equal(t, changed, result.Merged)
}

func TestThreeWayMerge_BothSidesDeleteSameLines(t *testing.T) {
	base := withTrailing("keep1\ndelete_me\nkeep2\n")
	deleted := withTrailing("keep1\nkeep2\n")
	result := patch.ThreeWayMerge(patch.ThreeWayMergeInput{Base: base, Ours: deleted, Theirs: deleted})
	require.True(t, result.Ok)
	require.Equal(t, deleted, result.Merged)
}

func TestApplyDocPatch_NoTrailingNewline(t *testing.T) {
	docNoTrailing := strings.Join(docLines, "\n") // no trailing \n
	p := makeDocPatch(docPatchOpts{
		baseContent: docNoTrailing,
		hunks: []schemas.PatchHunk{
			hunk(hunkOpts{start: 3, deleteText: docLines[2], insert: "Refunds require an idempotency key above 10 USD."}),
		},
	})
	in := baseApplyInput(p)
	in.WorktreeContent = strp(docNoTrailing)
	o := mustApply(t, in)
	require.Equal(t, patch.OutcomeApplied, o.Kind)
	require.False(t, strings.HasSuffix(o.ResultContent, "\n"))
}
