package openrouter

// Ported from packages/openrouter/src/orchestrator/writer-stage.ts (P5-T06;
// ARCH §3.5, §4.7, §13; PLAN §6.4 `writer` stage; 376 TS lines) — the
// @docsync/openrouter integration the deterministic orchestrator wires into
// the stage machine's `writer` leaf. Per target document it fetches the
// application-owned §3.3 RETRIEVAL→SONNET workspace ingredients from Git,
// assembles + renders the constrained workspace with the canonical machinery
// (internal/workspace), and hands it to the patch-writer adapter, mapping the
// adapter's closed result union onto core's `WriterStageResult` (mirrored
// structurally below — the same cross-copy pattern internal/policy uses for
// its stage port; the W6 orchestrator owns the canonical core shapes).
//
// V-P5-14 (binding): the workspace is assembled from APPLICATION-owned inputs
// ONLY — ctx (manifest/graph/clock), the resolved P1 ChangeAnalysis, the
// Stage-1 decision, and caller-fetched Git contents. Nothing the model
// produced ever feeds the assembly: model output enters ONLY through the
// result mapping below (pinned by the
// 'workspace-assembly-independent-of-model-output' test). The port NEVER
// writes to disk and NEVER calls a git mutating op — CatFile/DiffFiles are
// read-only.
//
// Default source selection: the changed-file paths (analysis.files, both
// rename halves) that match the target doc's manifest mapping sources via
// graph.ForwardLookup + graph.SourceMatchesPath (the §11.3 glob guards apply —
// a manifest glob that fails its guard returns a GlobGuardError: a config
// error, fail closed, D8). ZERO mapped changed sources → the writer is never
// invoked and the port answers `{ kind: 'noop' }` (nothing grounded to write —
// no wasted inference, no hallucination surface). deps.SourceSelector
// overrides this deterministically; an empty override result means the same
// noop.
//
// Workspace documents: exactly the ONE target doc (one patch per adapter
// call; sibling candidates never enter the workspace, so the model can never
// patch a non-target — related-doc batching is the orchestrator's job).
// Instructions: deps.Instructions verbatim when set, empty when explicitly
// null (a pointer to ""), else fetched from `.docsync/instructions.md` at head
// (absent → '' — the assembler's explicit empty marker). A target doc missing
// at head falls back to its base content (deleted within the window); missing
// at BOTH refs rethrows (fail closed — the runner already skips the writer for
// unreadable docs, P5-T06 groundwork).
//
// Result mapping — exhaustive over the adapter union (no `default:` arm; the
// default arm is a typed internal error so a new arm can never be silently
// swallowed):
//   - `kind: 'patch'` → `{ kind: 'patch', patch }` verbatim (the adapter
//     already re-parsed through the registered DOC_PATCH_SCHEMA — fail closed);
//   - `kind: 'abstain'` → `{ kind: 'abstain', reason, detail }` with the
//     canonical AbstainReason passthrough (`schema_violation`) — NEVER
//     hand-patched into a patch;
//   - `kind: 'finding-without-patch'` → `{ kind: 'finding', finding: { docPath,
//     reason: 'writer-timeout', detail } }` (V-P3-8: timeout →
//     finding-without-patch, never a partial patch).
//
// Port precondition gates (fail closed, BEFORE any model call; reasons stay
// inside the canonical ABSTAIN_REASON taxonomy — run records pin abstain
// reasons to that closed union):
//   - `analysis === nil` (analyzer inconclusive, D14) → abstain `other` —
//     inconclusive is never negative, so this is deliberately NOT a noop;
//   - `decision === nil` (the writer runs only on the CONTINUE route) →
//     abstain `other`;
//   - no `expectedBaseContentHashes` entry for the target → typed
//     `invalid_writer_stage_input` (caller contract defect; V-P5-14 input).
//
// A REJECTED adapter call degrades at the stage boundary (P5-T03 precedent):
// transport/gateway failures → operational abstain `model_unavailable`; a
// `patch_assembly_violation` (model misbehavior caught at adapter assembly)
// → `other`; `invalid_patch_writer_input` (stage-config defect) propagates
// verbatim — exit-2 territory, never masked. Run-level failure mapping stays
// the runner's job (P5-T11).
//
// Pure logic + injected I/O only: no `new Date()` (time is the injected ctx
// clock), no network, no fs — git access is the injected read-only GitPort.
// The TS Promise.all fan-out (diffRecords / docContent / instructions) is
// plain sequential Go: the fetched values are order-independent inputs and the
// assembly stays byte-deterministic either way.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

// instructionsPath is the repo-owned project-instructions path fetched when
// deps.Instructions is unset (TS: INSTRUCTIONS_PATH).
const instructionsPath = ".docsync/instructions.md"

// writerStageInternalCode is the typed code for an unreachable result arm
// (TS: GatewayError('writer_stage_internal', …)).
const writerStageInternalCode = "writer_stage_internal"

// invalidWriterStageInputCode is the typed code for a caller-contract defect
// (TS: GatewayError('invalid_writer_stage_input', …)).
const invalidWriterStageInputCode = "invalid_writer_stage_input"

/* ------------------------------------------------- structural core mirrors */

// TODO(orchestrator, W6): the RunContext / WriterStagePort / WriterStageResult
// / RelevanceContext / WorkspaceDocTarget shapes below are STRUCTURAL MIRRORS
// of the core orchestrator types (core/src/orchestrator/types.ts) — the same
// cross-copy pattern internal/policy uses for its stage port. When the W6
// orchestrator package lands, converge these onto the canonical shapes.

// WriterStageRunContext is the run-context members the writer stage consumes
// (TS: RunContext subset). `key`, `caps` (AnalysisCaps), `budgets`
// (PipelineBudgets) and `pr` are consumed by OTHER stages and are deliberately
// not mirrored here — the full core context is assignable to this shape.
type WriterStageRunContext struct {
	// Repo is the repository identity (e.g. `org/name`).
	Repo string
	// RepoRoot is the absolute repository root.
	RepoRoot string
	// Base / Head are the change-window refs.
	Base string
	Head string
	// RunID is the human-readable run id (`repo#base..head`, ARCH §4.1).
	RunID string
	// Manifest is the manifest allowlist for the run (D7).
	Manifest schemas.Manifest
	// Graph is the composed document graph (P1-T09); an unusable graph
	// (composition errors) fails the assembly closed (D8).
	Graph graph.DocumentGraph
	// Clock is the INJECTED clock — no stage may call a local time source.
	Clock func() string
}

// WriterStageDocTarget is one document the writer stage may be asked to
// investigate (TS: WorkspaceDocTarget).
type WriterStageDocTarget struct {
	// DocPath is the manifest document path.
	DocPath string
	// Mode is the mirrored ownership-floor mode.
	Mode workspace.WorkspaceMode
}

// WriterStageRelevanceVia is one relevance provenance hop (TS:
// RelevanceContext['docs'][number]['via'][number]).
type WriterStageRelevanceVia struct {
	Kind   string
	Detail string
}

// WriterStageRelevanceDoc is one relevance doc entry (TS: RelevanceContext
// docs member).
type WriterStageRelevanceDoc struct {
	DocPath   string
	Mode      workspace.WorkspaceMode
	Via       []WriterStageRelevanceVia
	Contracts []string
}

// WriterStageRelevanceCaps is the relevance bound (TS: RelevanceContext caps).
type WriterStageRelevanceCaps struct {
	MaxDocs int
}

// WriterStageRelevanceContext is the SAME bounded RelevanceContext instance
// Jev saw (ARCH §3.3 edge). It rides the input verbatim: the writer stage
// grounds its workspace from the application inputs above, never from the
// relevance context's model-adjacent data.
type WriterStageRelevanceContext struct {
	Docs     []WriterStageRelevanceDoc
	Concepts []string
	Caps     WriterStageRelevanceCaps
}

// WriterStageResultKind is the tag of the closed writer-stage result union
// (TS: WriterStageResult kinds).
type WriterStageResultKind string

const (
	// WriterStageResultPatch is a schema-valid DocPatch.
	WriterStageResultPatch WriterStageResultKind = "patch"
	// WriterStageResultFinding is the timeout finding-without-patch.
	WriterStageResultFinding WriterStageResultKind = "finding"
	// WriterStageResultAbstain is an operational abstention.
	WriterStageResultAbstain WriterStageResultKind = "abstain"
	// WriterStageResultNoop is the nothing-grounded outcome (the writer was
	// never invoked).
	WriterStageResultNoop WriterStageResultKind = "noop"
)

// WriterStageFindingTimeoutReason is the finding reason literal (TS:
// WriterStageResult finding union reason 'writer-timeout').
const WriterStageFindingTimeoutReason = "writer-timeout"

// WriterStageFinding is the V-P3-8 timeout finding (TS: WriterStageResult
// finding member).
type WriterStageFinding struct {
	DocPath string
	Reason  string
	Detail  string
}

// WriterStageResult is the closed union of writer-stage outcomes (TS:
// WriterStageResult). Exactly one arm is populated, selected by Kind.
type WriterStageResult struct {
	Kind WriterStageResultKind
	// Patch is the schema-valid DocPatch (Kind == patch).
	Patch *schemas.DocPatch
	// Finding is the timeout finding (Kind == finding).
	Finding *WriterStageFinding
	// Reason is the canonical abstain reason (Kind == abstain).
	Reason schemas.AbstainReason
	// Detail is the deterministic outcome detail (finding / abstain / noop).
	Detail string
}

// WriterStageInput is the writer-stage port input (TS: the
// WriterStagePort.write parameter — an object literal in TS).
type WriterStageInput struct {
	Ctx WriterStageRunContext
	Doc WriterStageDocTarget
	// Analysis is the resolved P1 change analysis record; nil ONLY when the
	// analyzer was inconclusive.
	Analysis *schemas.ChangeAnalysis
	// Decision is the Stage-1 Jev decision — the writer runs only on the
	// CONTINUE route. Nil = no decision was supplied.
	Decision *schemas.Decision
	// Relevance is the SAME bounded RelevanceContext instance Jev saw.
	Relevance WriterStageRelevanceContext
	// ExpectedBaseContentHashes maps docPath → bare-hex sha256 of the doc's
	// content at head — orchestrator-computed from Git, never model-supplied
	// (V-P5-14).
	ExpectedBaseContentHashes map[string]string
}

// WriterStagePort is the core-shaped writer-stage port (TS: WriterStagePort).
type WriterStagePort interface {
	Write(input WriterStageInput) (WriterStageResult, error)
}

/* -------------------------------------------------------------------- deps */

// WriterStageSelectionInput is the deterministic changed-source selection
// input (TS: WriterStageSelectionInput) — the §3.3 application data a selector
// may ground its choice on (never model output).
type WriterStageSelectionInput struct {
	Ctx      WriterStageRunContext
	Doc      WriterStageDocTarget
	Analysis *schemas.ChangeAnalysis
}

// WriterStageSourceSelector selects the changed source paths that ground ONE
// writer session (TS: WriterStageSourceSelector). Deterministic over its
// input; an empty result means "nothing grounded" and the port answers noop
// without invoking the model. An error is a fail-closed selection defect
// (e.g. a manifest glob that fails its §11.3 guard — D8).
type WriterStageSourceSelector func(input WriterStageSelectionInput) ([]string, error)

// WriterStageGitPort is the typed READ-ONLY git seam the stage consumes (TS:
// the core GitClient slice catFile/diffFiles — never mutating ops). The W6
// orchestrator adapts *gitx.GitClient onto it (threading its own context).
type WriterStageGitPort interface {
	// CatFile reads the blob at `<rev>:<path>`.
	CatFile(rev, path string) (gitx.CatFileResult, error)
	// DiffFiles lists the rename-aware per-file diff records with patch text.
	DiffFiles(base, head string) ([]gitx.DiffFileRecord, error)
}

// WriterStageWriterPort is the patch-writer adapter's `write` seam (TS:
// Pick<PatchWriterAdapter, 'write'>) — only this method is consumed.
type WriterStageWriterPort interface {
	Write(request PatchWriterRequest) (PatchWriterWriteResult, error)
}

// WriterStageDeps is the port dependencies (TS: WriterStageDeps — see the
// module header for per-member semantics).
type WriterStageDeps struct {
	// Writer is the concrete patch-writer adapter — only its `write` seam is
	// consumed.
	Writer WriterStageWriterPort
	// Git is the typed read-only git client (CatFile/DiffFiles only).
	Git WriterStageGitPort
	// Instructions: non-nil → the verbatim project instructions (a pointer to
	// "" is the explicit-empty case); nil → fetch
	// `.docsync/instructions.md` at head (absent → '').
	Instructions *string
	// PolicySummary is the compiled-policy routing summary rendered into
	// manifest_policy (optional).
	PolicySummary *string
	// WorkspaceCaps holds cap overrides merged over
	// workspace.DefaultWorkspaceCaps by the assembler.
	WorkspaceCaps *workspace.WorkspaceCapsOverrides
	// SourceSelector is the deterministic changed-source selection override
	// (default: the mapped filter).
	SourceSelector WriterStageSourceSelector
}

/* ----------------------------------------------------------------- helpers */

// stageAbstain is the canonical abstain helper — reasons stay in the closed
// ABSTAIN_REASON union (TS: stageAbstain).
func stageAbstain(reason schemas.AbstainReason, detail string) WriterStageResult {
	return WriterStageResult{Kind: WriterStageResultAbstain, Reason: reason, Detail: detail}
}

// defaultSourceSelector is the default selector: changed-file paths from the
// analysis whose path (or rename old-path) matches the target doc's manifest
// mapping sources, in analysis order, deduplicated (TS:
// defaultSourceSelector). Unmapped docs (no mapping record) ground nothing →
// empty selection. A glob guard failure propagates (fail closed, D8).
func defaultSourceSelector(input WriterStageSelectionInput) ([]string, error) {
	record := graph.ForwardLookup(input.Ctx.Graph, input.Doc.DocPath)
	if record == nil {
		return nil, nil
	}
	selected := make([]string, 0, len(input.Analysis.Files))
	seen := make(map[string]struct{}, len(input.Analysis.Files))
	for _, file := range input.Analysis.Files {
		candidates := []string{file.Path}
		if file.OldPath != nil {
			candidates = []string{file.Path, *file.OldPath}
		}
		matched := false
		for _, path := range candidates {
			for _, source := range record.Sources {
				ok, err := graph.SourceMatchesPath(source, path)
				if err != nil {
					return nil, err
				}
				if ok {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if matched {
			if _, dup := seen[file.Path]; !dup {
				seen[file.Path] = struct{}{}
				selected = append(selected, file.Path)
			}
		}
	}
	return selected, nil
}

// isMissingRev reports whether err is the ported core GitExecError
// missing-rev kind (TS: error instanceof GitExecError && error.kind ===
// 'missing-rev').
func isMissingRev(err error) bool {
	var execErr *gitx.GitExecError
	return errors.As(err, &execErr) && execErr.Kind == gitx.KindMissingRev
}

// fetchDocContent returns the doc content at head; a doc deleted within the
// change window falls back to its last known (base) content — the writer
// grounds hunks on existing text. Missing at both refs rethrows (fail closed,
// D14) (TS: fetchDocContent).
func fetchDocContent(git WriterStageGitPort, ctx WriterStageRunContext, docPath string) (string, error) {
	blob, err := git.CatFile(ctx.Head, docPath)
	if err != nil {
		if isMissingRev(err) {
			baseBlob, baseErr := git.CatFile(ctx.Base, docPath)
			if baseErr != nil {
				return "", baseErr
			}
			return baseBlob.Content, nil
		}
		return "", err
	}
	return blob.Content, nil
}

// resolveInstructions resolves the project instructions (see
// WriterStageDeps.Instructions) (TS: resolveInstructions).
func resolveInstructions(deps WriterStageDeps, ctx WriterStageRunContext) (string, error) {
	if deps.Instructions != nil {
		return *deps.Instructions, nil
	}
	blob, err := deps.Git.CatFile(ctx.Head, instructionsPath)
	if err != nil {
		if isMissingRev(err) {
			return "", nil
		}
		return "", err
	}
	return blob.Content, nil
}

// assembleGitDiff is the deterministic git diff text: the per-file patch
// sections of DiffFiles(base, head) restricted to the selected paths (both
// rename halves), joined with '\n' in git's canonical diff order; empty when
// no selected file carries a diff section (TS: assembleGitDiff).
func assembleGitDiff(records []gitx.DiffFileRecord, selected map[string]struct{}) string {
	parts := make([]string, 0, len(records))
	for _, record := range records {
		paths := []string{record.Path}
		if record.OldPath != nil {
			paths = []string{*record.OldPath, record.Path}
		}
		for _, path := range paths {
			if _, ok := selected[path]; ok {
				parts = append(parts, record.Patch)
				break
			}
		}
	}
	return strings.Join(parts, "\n")
}

// fetchSourceFiles returns the head contents of the selected source files
// (analysis order) (TS: fetchSourceFiles). Deleted files have no head blob and
// are excluded (their diff still grounds the writer); any other missing blob is
// an anomalous tree and rethrows (fail closed).
func fetchSourceFiles(
	git WriterStageGitPort,
	ctx WriterStageRunContext,
	selectedPaths []string,
	analysis *schemas.ChangeAnalysis,
) ([]workspace.WorkspaceSourceFile, error) {
	statusByPath := make(map[string]schemas.FileStatus, len(analysis.Files))
	for _, file := range analysis.Files {
		statusByPath[file.Path] = file.Status
	}
	files := make([]workspace.WorkspaceSourceFile, 0, len(selectedPaths))
	for _, path := range selectedPaths {
		if statusByPath[path] == schemas.FileStatusDeleted {
			continue
		}
		blob, err := git.CatFile(ctx.Head, path)
		if err != nil {
			return nil, err
		}
		files = append(files, workspace.WorkspaceSourceFile{Path: path, Content: blob.Content})
	}
	return files, nil
}

// mapWriterResult is the exhaustive adapter-union mapping (TS:
// mapWriterResult). The default arm is a typed internal error so an unmapped
// future arm can never be silently swallowed (the TS `never` assertion made it
// a compile-time error).
func mapWriterResult(result PatchWriterWriteResult, docPath string) (WriterStageResult, error) {
	switch result.Kind {
	case PatchWriterResultPatch:
		return WriterStageResult{Kind: WriterStageResultPatch, Patch: result.Patch}, nil
	case PatchWriterResultAbstain:
		return WriterStageResult{
			Kind:   WriterStageResultAbstain,
			Reason: result.Reason,
			Detail: fmt.Sprintf("patch-writer abstained (reason '%s')", result.Reason),
		}, nil
	case PatchWriterResultFinding:
		if result.Finding == nil {
			return WriterStageResult{}, docutil.New(writerStageInternalCode,
				"unreachable writer-port result arm: finding-without-patch carried no finding")
		}
		return WriterStageResult{
			Kind: WriterStageResultFinding,
			Finding: &WriterStageFinding{
				DocPath: docPath,
				Reason:  result.Finding.Reason,
				Detail:  result.Finding.Detail,
			},
		}, nil
	default:
		return WriterStageResult{}, docutil.New(writerStageInternalCode,
			fmt.Sprintf("unreachable writer-port result arm: %s", string(result.Kind)))
	}
}

/* -------------------------------------------------------------- the port */

// writerStagePort is the concrete port (TS: the object literal
// createWriterStagePort returns).
type writerStagePort struct {
	deps          WriterStageDeps
	systemPrompt  string
	selectSources WriterStageSourceSelector
}

// CreateWriterStagePort creates the writer-stage port (P5-T06; TS:
// createWriterStagePort): one assembled workspace + ONE adapter call per
// Write; the canonical five-duty system prompt comes from
// workspace.BuildWriterSystemPrompt (one canonical prompt source). Same
// application inputs + same clock → byte-identical workspace handed to the
// writer, regardless of what the model answers (V-P5-14).
func CreateWriterStagePort(deps WriterStageDeps) (WriterStagePort, error) {
	systemPrompt, err := workspace.BuildWriterSystemPrompt(workspace.BuildWriterSystemPromptInput{})
	if err != nil {
		return nil, err
	}
	selectSources := deps.SourceSelector
	if selectSources == nil {
		selectSources = defaultSourceSelector
	}
	return &writerStagePort{deps: deps, systemPrompt: systemPrompt, selectSources: selectSources}, nil
}

// Write runs ONE writer session for ONE target document (TS: the returned
// port's `write`).
func (p *writerStagePort) Write(input WriterStageInput) (WriterStageResult, error) {
	ctx := input.Ctx
	doc := input.Doc

	/* -- precondition gates (fail closed, before any model call) ---------- */

	if input.Analysis == nil {
		return stageAbstain(schemas.AbstainReasonOther,
			"no resolved ChangeAnalysis record for this run (analyzer inconclusive) — "+
				"nothing to ground a patch on (inconclusive is never negative, D14)"), nil
	}
	if input.Decision == nil {
		return stageAbstain(schemas.AbstainReasonOther,
			fmt.Sprintf("the writer stage runs only on the CONTINUE route: no Stage-1 Jev decision was "+
				"supplied for '%s'", doc.DocPath)), nil
	}
	baseHash, ok := input.ExpectedBaseContentHashes[doc.DocPath]
	if !ok {
		return WriterStageResult{}, docutil.New(invalidWriterStageInputCode,
			fmt.Sprintf("expectedBaseContentHashes carries no entry for the target doc '%s' — the "+
				"orchestrator-computed hash map is authoritative (V-P5-14) and the adapter needs "+
				"base_content_hash for it (invariant 10)", doc.DocPath))
	}

	/* -- deterministic grounding selection -------------------------------- */

	selectedPaths, err := p.selectSources(WriterStageSelectionInput{
		Ctx:      ctx,
		Doc:      doc,
		Analysis: input.Analysis,
	})
	if err != nil {
		return WriterStageResult{}, err
	}
	if len(selectedPaths) == 0 {
		return WriterStageResult{
			Kind: WriterStageResultNoop,
			Detail: fmt.Sprintf("no changed source file matches the mapping sources of '%s' — "+
				"nothing grounded to write for this target", doc.DocPath),
		}, nil
	}
	selected := make(map[string]struct{}, len(selectedPaths))
	for _, path := range selectedPaths {
		selected[path] = struct{}{}
	}

	/* -- application-owned workspace assembly (V-P5-14) ------------------- */

	diffRecords, err := p.deps.Git.DiffFiles(ctx.Base, ctx.Head)
	if err != nil {
		return WriterStageResult{}, err
	}
	docContent, err := fetchDocContent(p.deps.Git, ctx, doc.DocPath)
	if err != nil {
		return WriterStageResult{}, err
	}
	instructions, err := resolveInstructions(p.deps, ctx)
	if err != nil {
		return WriterStageResult{}, err
	}
	sourceFiles, err := fetchSourceFiles(p.deps.Git, ctx, selectedPaths, input.Analysis)
	if err != nil {
		return WriterStageResult{}, err
	}
	assembled, err := workspace.AssembleWorkspace(workspace.AssembleWorkspaceInput{
		GitDiff:        strPtr(assembleGitDiff(diffRecords, selected)),
		ChangeAnalysis: input.Analysis,
		Decision:       input.Decision,
		Manifest:       &ctx.Manifest,
		PolicySummary:  p.deps.PolicySummary,
		SourceFiles:    sourceFiles,
		Documents:      []workspace.WorkspaceDocument{{Path: doc.DocPath, Content: docContent}},
		Instructions:   strPtr(instructions),
		Clock:          ctx.Clock,
		Caps:           p.deps.WorkspaceCaps,
		// Defense in depth: an unusable graph (composition errors) fails the
		// assembly closed (D8) — the runner must not route writes on it.
		Graph: &ctx.Graph,
	})
	if err != nil {
		return WriterStageResult{}, err
	}

	/* -- one adapter call -------------------------------------------------- */

	request := PatchWriterRequest{
		Workspace:                 workspace.RenderWorkspace(assembled),
		SystemPrompt:              strPtr(p.systemPrompt),
		PolicyVersion:             input.Decision.PolicyVersion,
		RunID:                     ctx.RunID,
		DocTargets:                []PatchWriterDocTarget{{Path: doc.DocPath, Mode: string(doc.Mode), CurrentContentHash: strPtr(baseHash)}},
		Clock:                     ctx.Clock,
		ExpectedBaseContentHashes: copyHashes(input.ExpectedBaseContentHashes),
	}

	var result PatchWriterWriteResult
	func() {
		defer func() {
			// A panic inside the adapter is a programming error — never a
			// silent success. Recovered here so the stage machine degrades
			// through the typed path below instead of crashing the process.
			if recovered := recover(); recovered != nil {
				result = PatchWriterWriteResult{}
				err = fmt.Errorf("patch-writer panicked: %v", recovered)
			}
		}()
		result, err = p.deps.Writer.Write(request)
	}()
	if err != nil {
		// Stage-config defect (exit-2 territory): propagate verbatim, never mask.
		if docutil.CodeOf(err) == pwCodeInvalidPatchWriterInput {
			return WriterStageResult{}, err
		}
		// Operational degradation (P5-T03 precedent): a rejected writer call
		// yields NO patch — degrade to the canonical operational abstain
		// instead of crashing the stage machine; the runner owns exit mapping.
		reason := schemas.AbstainReasonModelUnavailable
		if docutil.CodeOf(err) == pwCodePatchAssemblyViolation {
			reason = schemas.AbstainReasonOther
		}
		return stageAbstain(reason, fmt.Sprintf("patch-writer failed: %s", err.Error())), nil
	}
	return mapWriterResult(result, doc.DocPath)
}

// strPtr returns a pointer to s.
func strPtr(s string) *string { return &s }

// copyHashes copies the caller's hash map (the adapter must never mutate the
// orchestrator's map, and the port owns no other reference to it).
func copyHashes(hashes map[string]string) map[string]string {
	if hashes == nil {
		return nil
	}
	out := make(map[string]string, len(hashes))
	for path, hash := range hashes {
		out[path] = hash
	}
	return out
}
