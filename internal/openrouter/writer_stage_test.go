package openrouter_test

// Ported from packages/openrouter/test/orchestrator-writer-stage.test.ts
// (P5-T06) — black-box (package openrouter_test) with testify, recorded
// doubles keyed by request identity (no network, OD-2).

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

/* --------------------------------------------------------------- fixtures */

const (
	baseSha       = "b0e2f6c"
	headSha       = "81af92e"
	stageRunID    = "acme/payments#b0e2f6c..81af92e"
	docPath       = "docs/payments.md"
	sourcePath    = "src/payments/refund.ts"
	clockValue    = "2026-05-01T10:00:00.000Z"
	targetLine    = "The idempotency key is required."
	insertText    = "The idempotency key is required when the refund total exceeds 10 USD."
	instructions  = "Keep patches minimal and evidence-backed.\n"
	policySummary = "policy v17: update >= 0.94, review >= 0.72 (calibrated data)"
)

// Long deterministic literals kept close to their use (determinism through
// canonicality).
const (
	docContentText  = "# Payments\n\nRefunds are issued immediately.\nThe idempotency key is required.\n"
	refundHeadText  = "export function refund(amount: number) { /* reviewed */ }\n"
	refundBaseText  = "export function refund(amount: number) {}\n"
	diffSectionText = "diff --git a/src/payments/refund.ts b/src/payments/refund.ts\n--- a/src/payments/refund.ts\n+++ b/src/payments/refund.ts\n@@ -1 +1,4 @@\n-export function refund(amount: number) {}\n+export function refund(amount: number) {\n+  // introduced here: the idempotency key is optional above 3,000 EUR\n+}\n"
)

func manifestFixture() schemas.Manifest {
	// DocModeAuto is a typed constant — a local variable is addressed so the
	// entry carries the manifest `mode: auto` binding.
	mode := schemas.DocModeAuto
	return schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: docPath, Mode: &mode, Sources: []string{"src/payments/**"}},
		},
	}
}

func graphFixture() graph.DocumentGraph {
	return graph.DocumentGraph{
		Mappings:      []graph.MappingRecord{{Doc: docPath, Sources: []string{"src/payments/**"}}},
		ContractKinds: map[string]schemas.ContractKind{},
	}
}

func runCtxFixture() openrouter.WriterStageRunContext {
	return openrouter.WriterStageRunContext{
		Repo:     "acme/payments",
		RepoRoot: "/tmp/kilo/writer-stage-fake-root",
		Base:     baseSha,
		Head:     headSha,
		RunID:    stageRunID,
		Manifest: manifestFixture(),
		Graph:    graphFixture(),
		Clock:    func() string { return clockValue },
	}
}

// analysisFixture spreads CHANGE_ANALYSIS_EXAMPLE and overrides the fixture
// fields (TS: the recorded P1 ChangeAnalysis fixture).
func analysisFixture() *schemas.ChangeAnalysis {
	analysis := schemas.ChangeAnalysisExample
	analysis.RunID = stageRunID
	analysis.Repo = "acme/payments"
	analysis.BaseSHA = baseSha
	analysis.HeadSHA = headSha
	analysis.Files = []schemas.ChangedFile{
		{Path: sourcePath, Status: schemas.FileStatusModified},
	}
	analysis.ClassificationHints = nil
	analysis.DeepAnalysis = schemas.DeepAnalysis{
		Status: schemas.DeepAnalysisSkipped,
		Reason: strPtr("test fixture: no analyzer coverage"),
	}
	return &analysis
}

func decisionFixture() *schemas.Decision {
	decision := schemas.DecisionExample
	decision.PolicyVersion = 17
	decision.Candidates = []string{docPath}
	decision.Change = schemas.DecisionChange{
		Commit:            headSha,
		DiffRef:           "git:" + baseSha + ".." + headSha,
		ChangeAnalysisRef: stageRunID,
	}
	return &decision
}

func relevanceFixture() openrouter.WriterStageRelevanceContext {
	return openrouter.WriterStageRelevanceContext{
		Docs: []openrouter.WriterStageRelevanceDoc{
			{
				DocPath: docPath,
				Mode:    workspace.WorkspaceModeAuto,
				Via:     []openrouter.WriterStageRelevanceVia{{Kind: "mapping-source", Detail: "src/payments/**"}},
			},
		},
		Caps: openrouter.WriterStageRelevanceCaps{MaxDocs: 10},
	}
}

// fakeGit is the recorded read-only git double (keyed by ref + path).
type fakeGit struct {
	head map[string]string
	base map[string]string
	diff []gitx.DiffFileRecord
	// catErrPath / catErr inject one scripted CatFile failure for the given
	// path (any ref); diffErr injects one scripted DiffFiles failure.
	catErrPath string
	catErr     error
	diffErr    error
}

func defaultGit() *fakeGit {
	return &fakeGit{
		head: map[string]string{
			sourcePath:                 refundHeadText,
			docPath:                    docContentText,
			".docsync/instructions.md": instructions,
		},
		base: map[string]string{sourcePath: refundBaseText},
		diff: []gitx.DiffFileRecord{
			{Path: sourcePath, Status: gitx.DiffStatusModified, Patch: diffSectionText},
		},
	}
}

func (g *fakeGit) CatFile(rev, path string) (gitx.CatFileResult, error) {
	if g.catErr != nil && path == g.catErrPath {
		return gitx.CatFileResult{}, g.catErr
	}
	files := g.head
	if rev == baseSha {
		files = g.base
	}
	content, ok := files[path]
	if !ok {
		return gitx.CatFileResult{}, gitx.NewGitExecError(gitx.KindMissingRev,
			"git cat-file blob "+rev+":"+path+": no such path", &gitx.GitExecErrorOptions{Command: "cat-file"})
	}
	return gitx.CatFileResult{BlobSha: docutil.ContentHash(content), Content: content}, nil
}

func (g *fakeGit) DiffFiles(base, head string) ([]gitx.DiffFileRecord, error) {
	if g.diffErr != nil {
		return nil, g.diffErr
	}
	return g.diff, nil
}

// fakeWriter is the recorded patch-writer double (keyed by the outgoing
// request identity — appended in call order).
type fakeWriter struct {
	requests []openrouter.PatchWriterRequest
	result   openrouter.PatchWriterWriteResult
	err      error
}

func (w *fakeWriter) Write(request openrouter.PatchWriterRequest) (openrouter.PatchWriterWriteResult, error) {
	w.requests = append(w.requests, request)
	return w.result, w.err
}

// fakePatch builds a schema-valid, anchored DocPatch for the target doc (TS:
// fakePatch).
func fakePatch(baseContent, target string) schemas.DocPatch {
	lines := strings.Split(docContentText, "\n")
	lineIndex := -1
	for i, line := range lines {
		if line == targetLine {
			lineIndex = i
			break
		}
	}
	if lineIndex < 0 {
		panic("target line not found in fixture doc content")
	}
	evidence := schemas.Evidence{
		Kind: schemas.EvidenceKindSymbolDiff,
		Ref:  sourcePath + ":1",
		Note: "idempotencyKey became optional",
	}
	return schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           stageRunID,
		DocPath:         target,
		BaseContentHash: docutil.ContentHash(baseContent),
		Hunks: []schemas.PatchHunk{{
			Anchor: schemas.PatchAnchor{
				StartLine:   lineIndex + 1,
				EndLine:     lineIndex + 1,
				ContentHash: docutil.ContentHash(targetLine),
			},
			DeleteText:   targetLine,
			InsertText:   insertText,
			EvidenceRefs: []schemas.Evidence{evidence},
		}},
		EvidenceRefs: []schemas.Evidence{evidence},
		Minimality:   schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 1},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: 17,
			WriterModel:   "fake/writer-routed",
			GeneratedAt:   clockValue,
		},
	}
}

func stagePatch() *schemas.DocPatch {
	patch := fakePatch(docContentText, docPath)
	return &patch
}

func writeStageInput() openrouter.WriterStageInput {
	return openrouter.WriterStageInput{
		Ctx:                       runCtxFixture(),
		Doc:                       openrouter.WriterStageDocTarget{DocPath: docPath, Mode: workspace.WorkspaceModeAuto},
		Analysis:                  analysisFixture(),
		Decision:                  decisionFixture(),
		Relevance:                 relevanceFixture(),
		ExpectedBaseContentHashes: map[string]string{docPath: docutil.ContentHash(docContentText)},
	}
}

func newPort(t *testing.T, writer *fakeWriter, mutate func(*openrouter.WriterStageDeps)) openrouter.WriterStagePort {
	t.Helper()
	deps := openrouter.WriterStageDeps{
		Writer:        writer,
		Git:           defaultGit(),
		PolicySummary: strPtr(policySummary),
		// A deliberately tiny doc cap so the truncated-document branch is
		// reachable from the port (the ONLY cap exercised: irrelevant for
		// determinism — the full caps table is the workspace package's own
		// test concern).
		WorkspaceCaps: &workspace.WorkspaceCapsOverrides{MaxDocBytes: pwIntPtr(24)},
	}
	if mutate != nil {
		mutate(&deps)
	}
	port, err := openrouter.CreateWriterStagePort(deps)
	require.NoError(t, err)
	return port
}

/* ------------------------------------------------------------------ tests */

// The port assembles the EXACT constrained workspace and returns the adapter's
// patch (V-P3-8 invariants 3/8, 10: base_content_hash + run_id verbatim).
func TestWriterStagePort_assembles_exact_workspace_and_returns_patch(t *testing.T) {
	writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{
		Kind:      openrouter.PatchWriterResultPatch,
		Patch:     stagePatch(),
		Telemetry: openrouter.PatchWriterRoleTelemetry{RoutedModel: "fake/writer-routed"},
	}}
	port := newPort(t, writer, nil)

	outcome, err := port.Write(writeStageInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultPatch, outcome.Kind)
	require.NotNil(t, outcome.Patch)
	// base_content_hash + run_id ride through VERBATIM from the
	// orchestrator-supplied identity (invariant 10: provenance or it didn't
	// happen).
	assert.Equal(t, docutil.ContentHash(docContentText), outcome.Patch.BaseContentHash)
	assert.Equal(t, stageRunID, outcome.Patch.RunID)

	require.Len(t, writer.requests, 1)
	request := writer.requests[0]

	// Workspace frame + every assembled section (exact render contract).
	assert.True(t, strings.HasPrefix(request.Workspace,
		workspace.WorkspaceOpenFrame+" v1 assembledAt="+clockValue+">>>"),
		"workspace must open with the canonical frame")
	assert.True(t, strings.HasSuffix(request.Workspace, workspace.WorkspaceCloseFrame))
	assert.Contains(t, request.Workspace, "<<<docsync-section git_diff>>>")
	assert.Contains(t, request.Workspace, diffSectionText)
	assert.Contains(t, request.Workspace, "<<<docsync-section change_analysis>>>")
	assert.Contains(t, request.Workspace, `"schema":"docsync.change-analysis/v1"`)
	assert.Contains(t, request.Workspace, "<<<docsync-section jev_decision>>>")
	assert.Contains(t, request.Workspace, `"decision_id":"d_01J9ZK7Q2M"`)
	assert.Contains(t, request.Workspace, "<<<docsync-section manifest_policy>>>")
	assert.Contains(t, request.Workspace, "[manifest]")
	assert.Contains(t, request.Workspace, "[policy]\n"+policySummary)
	assert.Contains(t, request.Workspace, "<<<docsync-section source_files>>>")
	assert.Contains(t, request.Workspace, sourcePath)
	assert.Contains(t, request.Workspace, refundHeadText)
	assert.Contains(t, request.Workspace, "<<<docsync-section relevant_documents>>>")
	assert.Contains(t, request.Workspace, docPath)
	assert.Contains(t, request.Workspace, "mode: auto")
	assert.Contains(t, request.Workspace, "writable: true")
	assert.Contains(t, request.Workspace, "readonly: true")
	assert.Contains(t, request.Workspace, "<<<docsync-section project_instructions>>>")
	assert.Contains(t, request.Workspace, "Keep patches minimal and evidence-backed.")
	assert.Contains(t, request.Workspace, "truncated: cap maxDocBytes (24 bytes)")

	// Identity forwarding (orchestrator-supplied, never invented).
	assert.Equal(t, stageRunID, request.RunID)
	assert.Equal(t, 17, request.PolicyVersion)
	require.NotNil(t, request.SystemPrompt)
	assert.Contains(t, *request.SystemPrompt, "Your five duties, in order:")
	require.Len(t, request.DocTargets, 1)
	assert.Equal(t, docPath, request.DocTargets[0].Path)
	assert.Equal(t, "auto", request.DocTargets[0].Mode)
	require.NotNil(t, request.DocTargets[0].CurrentContentHash)
	assert.Equal(t, docutil.ContentHash(docContentText), *request.DocTargets[0].CurrentContentHash)
	assert.Equal(t, map[string]string{docPath: docutil.ContentHash(docContentText)},
		request.ExpectedBaseContentHashes)
	// The clock is forwarded, not replaced (the invoked value is the injected
	// clock's).
	require.NotNil(t, request.Clock)
	assert.Equal(t, clockValue, request.Clock())
}

// A writer timeout degrades to finding-without-patch, never a partial patch
// (V-P3-8; the same write NAME means the patch content is INVALIDATED — the
// run must re-run).
func TestWriterStagePort_maps_timeout_to_finding_without_partial_patch(t *testing.T) {
	writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{
		Kind: openrouter.PatchWriterResultFinding,
		Finding: &openrouter.PatchWriterFinding{
			DocPaths: []string{docPath},
			Reason:   openrouter.PatchWriterTimeoutReason,
			Detail:   "writer wall-clock budget of 40000ms elapsed before the model completed — no partial patch is produced (P3-T14 groundwork; ARCH §3.4)",
		},
	}}
	port := newPort(t, writer, nil)

	outcome, err := port.Write(writeStageInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultFinding, outcome.Kind)
	require.NotNil(t, outcome.Finding)
	assert.Equal(t, docPath, outcome.Finding.DocPath)
	assert.Equal(t, openrouter.WriterStageFindingTimeoutReason, outcome.Finding.Reason)
	assert.NotEmpty(t, outcome.Finding.Detail)
	assert.Nil(t, outcome.Patch)
}

// Schema-abstain never gets hand-patched into compliance (P3-T01).
func TestWriterStagePort_maps_schema_abstain_never_fabricates_patch(t *testing.T) {
	writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{
		Kind:      openrouter.PatchWriterResultAbstain,
		Reason:    schemas.AbstainReasonSchemaViolation,
		Telemetry: openrouter.PatchWriterRoleTelemetry{RoutedModel: "fake/writer-routed"},
	}}
	port := newPort(t, writer, nil)

	outcome, err := port.Write(writeStageInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonSchemaViolation, outcome.Reason)
	assert.Equal(t, "patch-writer abstained (reason 'schema_violation')", outcome.Detail)
	assert.Nil(t, outcome.Patch)
}

// The assembled workspace is INDEPENDENT of model output (V-P5-14) — two ports
// with different adapter results receive the SAME assembled workspace and the
// same write request.
func TestWriterStagePort_workspace_assembly_independent_of_model_output(t *testing.T) {
	firstWriter := &fakeWriter{result: openrouter.PatchWriterWriteResult{
		Kind:  openrouter.PatchWriterResultPatch,
		Patch: stagePatch(),
	}}
	secondWriter := &fakeWriter{result: openrouter.PatchWriterWriteResult{
		Kind:   openrouter.PatchWriterResultAbstain,
		Reason: schemas.AbstainReasonModelUnavailable,
	}}
	firstPort := newPort(t, firstWriter, nil)
	secondPort := newPort(t, secondWriter, nil)
	sharedInput := writeStageInput()

	_, err := firstPort.Write(sharedInput)
	require.NoError(t, err)
	_, err = secondPort.Write(sharedInput)
	require.NoError(t, err)

	require.Len(t, firstWriter.requests, 1)
	require.Len(t, secondWriter.requests, 1)
	firstRequest, secondRequest := firstWriter.requests[0], secondWriter.requests[0]
	assert.Equal(t, firstRequest.Workspace, secondRequest.Workspace)
	assert.Equal(t, firstRequest.SystemPrompt, secondRequest.SystemPrompt)
	assert.Equal(t, firstRequest.PolicyVersion, secondRequest.PolicyVersion)
	assert.Equal(t, firstRequest.RunID, secondRequest.RunID)
	assert.Equal(t, firstRequest.DocTargets, secondRequest.DocTargets)
	assert.Equal(t, firstRequest.ExpectedBaseContentHashes, secondRequest.ExpectedBaseContentHashes)
	require.NotNil(t, firstRequest.Clock)
	require.NotNil(t, secondRequest.Clock)
	assert.Equal(t, firstRequest.Clock(), secondRequest.Clock())
}

// Preconditions that are NOT evaluable BEFORE the writer runs → abstain
// `other` (never `model_unavailable` + never a noop): negative/positive
// analysis-level status must not masquerade as evidence absence.
func TestWriterStagePort_abstains_when_analysis_or_decision_unavailable(t *testing.T) {
	writer := &fakeWriter{}
	port := newPort(t, writer, nil)

	noAnalysis := writeStageInput()
	noAnalysis.Analysis = nil
	outcome, err := port.Write(noAnalysis)
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonOther, outcome.Reason)
	assert.Contains(t, outcome.Detail, "ChangeAnalysis")

	noDecision := writeStageInput()
	noDecision.Decision = nil
	outcome, err = port.Write(noDecision)
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonOther, outcome.Reason)
	assert.Contains(t, outcome.Detail, "CONTINUE")
	assert.Empty(t, writer.requests)
}

// No mapped source file changed → the writer is NEVER invoked → noop (no
// hallucination surface).
func TestWriterStagePort_noop_when_no_mapped_source_changed(t *testing.T) {
	writer := &fakeWriter{}
	port := newPort(t, writer, nil)
	input := writeStageInput()
	input.Analysis.Files = []schemas.ChangedFile{{Path: "src/auth/token.ts", Status: schemas.FileStatusModified}}

	outcome, err := port.Write(input)
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultNoop, outcome.Kind)
	assert.Contains(t, outcome.Detail, docPath)
	assert.Empty(t, writer.requests)
}

// A rejected adapter call degrades to the canonical model_unavailable abstain
// (the stage machine never crashes; the runner owns exit mapping).
func TestWriterStagePort_degrades_rejected_adapter_call_to_model_unavailable(t *testing.T) {
	writer := &fakeWriter{err: openrouter.NewPatchWriterTotalInferenceFailure(
		"all model targets exhausted for role patch-writer", "patch-writer", nil)}
	port := newPort(t, writer, nil)

	outcome, err := port.Write(writeStageInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonModelUnavailable, outcome.Reason)
	assert.Contains(t, outcome.Detail, "all model targets exhausted")
	assert.Len(t, writer.requests, 1)
}

// Model misbehavior caught at adapter assembly degrades to `other` (not
// model_unavailable — the model was reachable, its output was not usable).
func TestWriterStagePort_assembly_violation_degrades_to_other_abstain(t *testing.T) {
	writer := &fakeWriter{err: docutil.New("patch_assembly_violation", "hostile model output")}
	port := newPort(t, writer, nil)

	outcome, err := port.Write(writeStageInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultAbstain, outcome.Kind)
	assert.Equal(t, schemas.AbstainReasonOther, outcome.Reason)
	assert.Contains(t, outcome.Detail, "hostile model output")
	assert.Nil(t, outcome.Patch)
}

// A stage-config defect (invalid_patch_writer_input) propagates verbatim —
// exit-2 territory, never masked as an operational abstain.
func TestWriterStagePort_invalid_writer_input_propagates_verbatim(t *testing.T) {
	writer := &fakeWriter{err: docutil.New("invalid_patch_writer_input", "policyVersion must be a positive integer")}
	port := newPort(t, writer, nil)

	_, err := port.Write(writeStageInput())
	require.Error(t, err)
	assert.Equal(t, "invalid_patch_writer_input", docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "policyVersion must be a positive integer")
}

// A missing expectedBaseContentHashes entry for the target is a typed
// invalid_writer_stage_input (caller contract defect).
func TestWriterStagePort_missing_base_hash_is_typed_stage_input_error(t *testing.T) {
	writer := &fakeWriter{}
	port := newPort(t, writer, nil)
	input := writeStageInput()
	input.ExpectedBaseContentHashes = map[string]string{}

	_, err := port.Write(input)
	require.Error(t, err)
	assert.Equal(t, "invalid_writer_stage_input", docutil.CodeOf(err))
	assert.Contains(t, err.Error(), docPath)
	assert.Empty(t, writer.requests)
}

// Explicit instructions (pointer, including the explicit-empty case) and the
// absent-instructions fallback are both honored deterministically: ” renders
// as the workspace's explicit empty marker note (never invented instructions).
func TestWriterStagePort_instructions_resolution_variants(t *testing.T) {
	// Explicit instructions ride verbatim.
	writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{Kind: openrouter.PatchWriterResultPatch, Patch: stagePatch()}}
	port := newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		d.Instructions = strPtr("explicit project instructions")
	})
	_, err := port.Write(writeStageInput())
	require.NoError(t, err)
	assert.Contains(t, writer.requests[0].Workspace, "explicit project instructions")

	// Explicit null (pointer to "") yields ''.
	writer = &fakeWriter{result: openrouter.PatchWriterWriteResult{Kind: openrouter.PatchWriterResultPatch, Patch: stagePatch()}}
	port = newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		d.Instructions = strPtr("")
	})
	_, err = port.Write(writeStageInput())
	require.NoError(t, err)
	assert.Contains(t, writer.requests[0].Workspace, "status: empty")
	assert.NotContains(t, writer.requests[0].Workspace, "Keep patches minimal and evidence-backed.")

	// Absent at head → '' (the head fixture without the instructions path).
	writer = &fakeWriter{result: openrouter.PatchWriterWriteResult{Kind: openrouter.PatchWriterResultPatch, Patch: stagePatch()}}
	port = newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		git := defaultGit()
		delete(git.head, ".docsync/instructions.md")
		d.Git = git
	})
	_, err = port.Write(writeStageInput())
	require.NoError(t, err)
	assert.Contains(t, writer.requests[0].Workspace, "status: empty")
	assert.NotContains(t, writer.requests[0].Workspace, "Keep patches minimal and evidence-backed.")
}

// The default source selector: rename old-path candidates match, both halves
// are diff-restricted, and result order stays analysis order.
func TestWriterStagePort_default_source_selector_rename_halves(t *testing.T) {
	writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{Kind: openrouter.PatchWriterResultPatch, Patch: stagePatch()}}
	port := newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		git := defaultGit()
		git.diff = []gitx.DiffFileRecord{
			{Path: "src/payments/refunds.ts", OldPath: strPtr(sourcePath), Status: gitx.DiffStatusRenamed, Patch: diffSectionText},
		}
		d.Git = git
	})
	input := writeStageInput()
	input.Analysis.Files = []schemas.ChangedFile{{Path: sourcePath, Status: schemas.FileStatusRenamed}}

	outcome, err := port.Write(input)
	require.NoError(t, err)
	assert.Equal(t, openrouter.WriterStageResultPatch, outcome.Kind)
	require.Len(t, writer.requests, 1)
	assert.Contains(t, writer.requests[0].Workspace, diffSectionText)
}

/* ------------------------------------------------- coverage completions */

// An unmapped target doc grounds nothing → noop, the writer is never invoked.
func TestWriterStagePort_unmapped_doc_yields_noop(t *testing.T) {
	writer := &fakeWriter{}
	port := newPort(t, writer, nil)
	input := writeStageInput()
	input.Doc.DocPath = "docs/not-in-graph.md"
	input.ExpectedBaseContentHashes = map[string]string{"docs/not-in-graph.md": docutil.ContentHash(docContentText)}

	outcome, err := port.Write(input)
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultNoop, outcome.Kind)
	assert.Contains(t, outcome.Detail, "docs/not-in-graph.md")
	assert.Empty(t, writer.requests)
}

// A doc deleted within the change window falls back to its BASE content (the
// writer grounds hunks on existing text).
func TestWriterStagePort_doc_deleted_in_window_falls_back_to_base(t *testing.T) {
	writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{
		Kind:  openrouter.PatchWriterResultPatch,
		Patch: stagePatch(),
	}}
	port := newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		git := defaultGit()
		git.head = map[string]string{sourcePath: refundHeadText, ".docsync/instructions.md": instructions}
		git.base[docPath] = docContentText
		d.Git = git
	})

	outcome, err := port.Write(writeStageInput())
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultPatch, outcome.Kind)
	require.Len(t, writer.requests, 1)
	assert.Contains(t, writer.requests[0].Workspace, "# Payments")
}

// A doc missing at BOTH refs (and any non-missing-rev git failure) rethrows —
// fail closed (D14).
func TestWriterStagePort_doc_missing_at_both_refs_rethrows(t *testing.T) {
	writer := &fakeWriter{}
	port := newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		git := defaultGit()
		delete(git.head, docPath)
		d.Git = git
	})
	_, err := port.Write(writeStageInput())
	require.Error(t, err)
	var execErr *gitx.GitExecError
	assert.True(t, errors.As(err, &execErr))
	assert.Equal(t, gitx.KindMissingRev, execErr.Kind)
	assert.Empty(t, writer.requests)
}

// Non-missing-rev git failures (diff walk, doc read) propagate as typed
// failures — never silently noop'd.
func TestWriterStagePort_git_failures_propagate(t *testing.T) {
	writer := &fakeWriter{}
	port := newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		git := defaultGit()
		git.diffErr = gitx.NewGitExecError(gitx.KindExecFailure, "scripted diff failure",
			&gitx.GitExecErrorOptions{Command: "diff"})
		d.Git = git
	})
	_, err := port.Write(writeStageInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scripted diff failure")

	port = newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		git := defaultGit()
		git.catErrPath = docPath
		git.catErr = gitx.NewGitExecError(gitx.KindExecFailure, "scripted cat-file failure",
			&gitx.GitExecErrorOptions{Command: "cat-file"})
		d.Git = git
	})
	_, err = port.Write(writeStageInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scripted cat-file failure")
}

// A failing instructions fetch rethrows (absent ≠ broken).
func TestWriterStagePort_instructions_fetch_failure_propagates(t *testing.T) {
	writer := &fakeWriter{}
	port := newPort(t, writer, func(d *openrouter.WriterStageDeps) {
		git := defaultGit()
		git.catErrPath = ".docsync/instructions.md"
		git.catErr = gitx.NewGitExecError(gitx.KindExecFailure, "scripted instructions failure",
			&gitx.GitExecErrorOptions{Command: "cat-file"})
		d.Git = git
	})
	_, err := port.Write(writeStageInput())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scripted instructions failure")
	assert.Empty(t, writer.requests)
}

// A manifest source that fails its §11.3 glob guard aborts the run (fail
// closed, D8) — never a silently skipped mapping.
func TestWriterStagePort_glob_guard_failure_propagates(t *testing.T) {
	writer := &fakeWriter{}
	port := newPort(t, writer, nil)
	input := writeStageInput()
	input.Ctx.Graph = graph.DocumentGraph{
		Mappings:      []graph.MappingRecord{{Doc: docPath, Sources: []string{"**/.docsync/**"}}},
		ContractKinds: map[string]schemas.ContractKind{},
	}
	_, err := port.Write(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docsync_internal")
	assert.Empty(t, writer.requests)
}

// Deleted source files have no head blob and are skipped (their diff still
// grounds the writer).
func TestWriterStagePort_deleted_source_file_skipped(t *testing.T) {
	writer := &fakeWriter{result: openrouter.PatchWriterWriteResult{
		Kind:  openrouter.PatchWriterResultPatch,
		Patch: stagePatch(),
	}}
	port := newPort(t, writer, nil)
	input := writeStageInput()
	input.Analysis.Files = []schemas.ChangedFile{{Path: sourcePath, Status: schemas.FileStatusDeleted}}

	outcome, err := port.Write(input)
	require.NoError(t, err)
	require.Equal(t, openrouter.WriterStageResultPatch, outcome.Kind)
	require.Len(t, writer.requests, 1)
	assert.Contains(t, writer.requests[0].Workspace, diffSectionText)
	assert.NotContains(t, writer.requests[0].Workspace, refundHeadText)
}
