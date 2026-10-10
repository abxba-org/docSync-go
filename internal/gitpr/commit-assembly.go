package gitpr

import (
	"context"
	"reflect"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Ported from core/src/gitpr/commit-assembly.ts — doc-commit assembly + write
// path (P6-T02; INV-10 provenance gate is the P6-T25 early form; invariants
// 2/3/10; ARCH §12.2).
//
// Two stages, both fail-closed:
//
//  1. AssembleDocCommit — the PROVENANCE GATE fires FIRST (before ANY commit
//     assembly): a null/undefined/empty provenance record (or an empty run id)
//     is refused — "provenance or it didn't happen" (invariant 10) is a
//     PRECONDITION of assembly, not a formatting option. Plan-level
//     validation: at least one patch OR state update (an empty commit is never
//     assembled); state relPaths must stay under `.docsync/state/` (D1
//     committed-state namespace — nothing else rides in the state channel).
//     Path-level allowlisting is the PR filter layer's job (P6-T11), NOT this
//     module's. Emits the deterministic commit PLAN: conventional message
//     (`docs: synchronize documentation` + doc paths + `Provenance: <run-id>`),
//     the sorted deduped union of patch doc paths + state relPaths (each entry
//     carrying the content to write), and stats.
//  2. WriteDocCommit — deterministic write+commit via git plumbing. Writes
//     EXACTLY the planned files through the injected disk seam, stages EXACTLY
//     the planned paths (`git add -- <paths...>`, never a sweep), then the
//     post-index scope assert: `git diff --cached --name-only` must be a subset
//     of the plan — ANY foreign staged content (a rogue writer, a leftover
//     index from a crashed prior run) aborts with a *CommitScopeError BEFORE
//     the commit. An empty staged set (all writes were content no-ops) refuses
//     to commit (typed, fail closed). The commit itself is a plain
//     `git commit -m` — NO hook bypass; hooks run for the machine exactly as
//     they run for a human.
//
// The machine NEVER touches anything outside plan.Files: the disk seam is the
// only writer, the stage list is the plan, and the post-index assert is the
// commit-time proof.

/* -------------------------------------------------------------------- errors */

// CodeProvenanceMissing preserves the TS EDOCSYNC_PROVENANCE_MISSING code
// verbatim (INV-10 / P6-T25). docutil carries no such code; per the
// globs/statemachine precedent the TS code is kept through docutil.New
// instead of inventing a new one (CONVENTIONS.md §Errors).
const CodeProvenanceMissing = "EDOCSYNC_PROVENANCE_MISSING"

// CodeCommitPlan preserves the TS EDOCSYNC_COMMIT_PLAN code verbatim.
const CodeCommitPlan = "EDOCSYNC_COMMIT_PLAN"

// CodeCommitScope preserves the TS EDOCSYNC_COMMIT_SCOPE code verbatim.
const CodeCommitScope = "EDOCSYNC_COMMIT_SCOPE"

// CodeEmptyCommit preserves the TS EDOCSYNC_EMPTY_COMMIT code verbatim.
const CodeEmptyCommit = "EDOCSYNC_EMPTY_COMMIT"

// ProvenanceMissingError is the INV-10 (P6-T25 early form) refusal: the
// provenance record is null/undefined/empty (or the run id is empty) — NO
// commit is assembled.
type ProvenanceMissingError struct {
	coded *docutil.Error
}

// Code reports the machine-readable code (EDOCSYNC_PROVENANCE_MISSING).
func (e *ProvenanceMissingError) Code() string { return CodeProvenanceMissing }

// Error renders the coded error in docutil house style.
func (e *ProvenanceMissingError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *ProvenanceMissingError) Unwrap() error { return e.coded }

// CommitAssemblyError is a commit-plan assembly refusal (empty input, stray
// state path, missing content, …).
type CommitAssemblyError struct {
	coded *docutil.Error
}

// Code reports the machine-readable code (EDOCSYNC_COMMIT_PLAN).
func (e *CommitAssemblyError) Code() string { return CodeCommitPlan }

// Error renders the coded error in docutil house style.
func (e *CommitAssemblyError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *CommitAssemblyError) Unwrap() error { return e.coded }

// CommitScopeError is the post-index scope violation: the index holds paths
// outside the commit plan (ForeignPaths lists the evidence). The commit is
// ABORTED, never repaired (fail closed — a human investigates the foreign
// content).
type CommitScopeError struct {
	// ForeignPaths are the staged paths not present in the commit plan.
	ForeignPaths []string

	coded *docutil.Error
}

// Code reports the machine-readable code (EDOCSYNC_COMMIT_SCOPE).
func (e *CommitScopeError) Code() string { return CodeCommitScope }

// Error renders the coded error in docutil house style.
func (e *CommitScopeError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *CommitScopeError) Unwrap() error { return e.coded }

// EmptyCommitError means all planned writes were content no-ops — nothing
// staged, no commit.
type EmptyCommitError struct {
	coded *docutil.Error
}

// Code reports the machine-readable code (EDOCSYNC_EMPTY_COMMIT).
func (e *EmptyCommitError) Code() string { return CodeEmptyCommit }

// Error renders the coded error in docutil house style.
func (e *EmptyCommitError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *EmptyCommitError) Unwrap() error { return e.coded }

// newProvenanceMissingError builds the INV-10 refusal with the exact TS text.
func newProvenanceMissingError() *ProvenanceMissingError {
	return &ProvenanceMissingError{coded: docutil.New(CodeProvenanceMissing,
		"provenance record missing or empty — INV-10: every automated change carries its "+
			"evidence record; no commit is assembled without provenance (P6-T25 gate)")}
}

// newCommitAssemblyError builds a commit-plan assembly refusal.
func newCommitAssemblyError(message string) *CommitAssemblyError {
	return &CommitAssemblyError{coded: docutil.New(CodeCommitPlan, message)}
}

// newCommitScopeError builds the post-index scope-abort refusal.
func newCommitScopeError(foreignPaths []string) *CommitScopeError {
	return &CommitScopeError{
		ForeignPaths: foreignPaths,
		coded: docutil.New(CodeCommitScope,
			"post-index scope assert failed: staged content outside the commit plan ["+
				strings.Join(foreignPaths, ", ")+"] — commit aborted (machine never touches "+
				"anything outside plan.files)"),
	}
}

// newEmptyCommitError builds the nothing-staged refusal.
func newEmptyCommitError() *EmptyCommitError {
	return &EmptyCommitError{coded: docutil.New(CodeEmptyCommit,
		"refusing to commit: nothing staged — every planned write was a content no-op "+
			"(an empty commit is never created)")}
}

/* --------------------------------------------------------------------- plan */

// statePrefix is the committed-state channel (D1, OD-3): state relPaths must
// stay under it.
const statePrefix = ".docsync/state/"

// commitSubject is the conventional-commit subject.
const commitSubject = "docs: synchronize documentation"

// CommitStateUpdate is one state update riding in the committed-state channel.
type CommitStateUpdate struct {
	// RelPath is the repo-relative path; it MUST start with `.docsync/state/`.
	RelPath string
	// Content is the file content to write.
	Content string
}

// CommitPlanFile is one file of the commit plan: its repo-relative path + the
// content to write.
type CommitPlanFile struct {
	Path    string
	Content string
}

// CommitPlanStats is the aggregated plan stats (deterministic derivation from
// the plan input).
type CommitPlanStats struct {
	FileCount      int
	DocPathCount   int
	StatePathCount int
	// AddedLines is the sum of the patches' measured minimality additions.
	AddedLines int
	// RemovedLines is the sum of the patches' measured minimality removals.
	RemovedLines int
}

// CommitPlan is the deterministic commit PLAN — the only thing WriteDocCommit
// may touch.
type CommitPlan struct {
	// Message is the conventional message: subject + doc path list +
	// `Provenance: <run-id>`.
	Message string
	// Files is the sorted (path asc) deduped union of patch doc paths + state
	// relPaths.
	Files []CommitPlanFile
	Stats CommitPlanStats
}

// ProvenanceRef is the run's provenance: Record is the provenance record
// object (shape validated downstream; HERE only its PRESENCE gates assembly —
// INV-10) and RunID the run id the `Provenance:` ref line carries.
type ProvenanceRef struct {
	Record any
	RunID  string
}

// AssembleDocCommitInput is the input of AssembleDocCommit (the three spec
// fields + applied doc content).
type AssembleDocCommitInput struct {
	// Patches are the verified doc patches to carry.
	Patches []schemas.DocPatch
	// StateUpdates are the state updates riding the committed-state channel.
	StateUpdates []CommitStateUpdate
	// Provenance is the run's provenance record + run id (INV-10 gate input).
	Provenance ProvenanceRef
	// DocContents is the applied doc content per patch doc_path (the
	// verification stage's resultContent). DocPatch carries HUNKS, not
	// whole-file content, so the assembler cannot derive it; a patch without
	// an entry here is refused (fail closed). Nil == TS undefined: state-only
	// assemblies omit it.
	DocContents map[string]string
}

// isPresentRecord reports whether record is a non-null, non-array object with
// own keys — the TS isPresentRecord predicate, including its exclusion of
// arrays and primitives.
func isPresentRecord(record any) bool {
	if record == nil {
		return false
	}
	value := reflect.ValueOf(record)
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Map:
		return value.Len() > 0
	case reflect.Struct:
		return value.NumField() > 0
	default:
		// Slices/arrays are excluded by the TS Array.isArray check; strings,
		// numbers and booleans are not objects at all.
		return false
	}
}

// AssertProvenancePresent is the INV-10 provenance gate (P6-T25 early form):
// the record must be PRESENT (non-nil, non-undefined, non-empty object) and
// the run id non-empty — the `Provenance:` ref line would be meaningless
// otherwise. It fires BEFORE any other assembly step.
func AssertProvenancePresent(provenance ProvenanceRef) error {
	if !isPresentRecord(provenance.Record) {
		return newProvenanceMissingError()
	}
	if len(provenance.RunID) == 0 {
		return newProvenanceMissingError()
	}
	return nil
}

// assertSingleLine refuses commit-message inputs that are empty or carry a
// newline/NUL (they would corrupt the message).
func assertSingleLine(value, label string) error {
	if len(value) == 0 || strings.ContainsAny(value, "\n\r\x00") {
		return newCommitAssemblyError(
			"commit message input '" + label + "' must be nonempty, newline-free and NUL-free")
	}
	return nil
}

// AssembleDocCommit builds the commit plan (P6-T02). Deterministic and total
// over valid input; every refusal is a typed error (or the INV-10 gate).
func AssembleDocCommit(input AssembleDocCommitInput) (CommitPlan, error) {
	// INV-10 (P6-T25 early form) — the FIRST step, before ANY commit assembly.
	if err := AssertProvenancePresent(input.Provenance); err != nil {
		return CommitPlan{}, err
	}

	if len(input.Patches) == 0 && len(input.StateUpdates) == 0 {
		return CommitPlan{}, newCommitAssemblyError(
			"commit assembly refused: no patches and no state updates — an empty commit is " +
				"never assembled (fail closed)")
	}

	// Merged content map: dedupe by path; conflicting content for one path is
	// a fail-closed refusal (ambiguous plan), identical content collapses.
	contents := make(map[string]string)
	var docPaths, statePaths []string
	put := func(path, content, label string) error {
		if existing, ok := contents[path]; ok && existing != content {
			return newCommitAssemblyError(
				"commit assembly refused: path '" + path + "' appears twice with differing content (" +
					label + ")")
		}
		contents[path] = content
		return nil
	}

	for _, patch := range input.Patches {
		if err := assertSingleLine(patch.DocPath, "patch doc_path '"+patch.DocPath+"'"); err != nil {
			return CommitPlan{}, err
		}
		content, ok := input.DocContents[patch.DocPath]
		if !ok {
			return CommitPlan{}, newCommitAssemblyError(
				"commit assembly refused: patch doc_path '" + patch.DocPath + "' has no applied content " +
					"in docContents (hunks are not whole-file content — fail closed)")
		}
		if !containsStringValue(docPaths, patch.DocPath) {
			docPaths = append(docPaths, patch.DocPath)
		}
		if err := put(patch.DocPath, content, "patch"); err != nil {
			return CommitPlan{}, err
		}
	}
	for _, update := range input.StateUpdates {
		if err := assertSingleLine(update.RelPath, "state relPath '"+update.RelPath+"'"); err != nil {
			return CommitPlan{}, err
		}
		if !strings.HasPrefix(update.RelPath, statePrefix) {
			return CommitPlan{}, newCommitAssemblyError(
				"commit assembly refused: state relPath '" + update.RelPath + "' does not start with '" +
					statePrefix + "' (the committed-state channel carries state paths only)")
		}
		if !containsStringValue(statePaths, update.RelPath) {
			statePaths = append(statePaths, update.RelPath)
		}
		if err := put(update.RelPath, update.Content, "state update"); err != nil {
			return CommitPlan{}, err
		}
	}

	// Deterministic sort (path asc) over the deduped union.
	files := make([]CommitPlanFile, 0, len(contents))
	for path, content := range contents {
		files = append(files, CommitPlanFile{Path: path, Content: content})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	message, err := buildCommitMessage(input.Provenance.RunID, docPaths, statePaths)
	if err != nil {
		return CommitPlan{}, err
	}

	stats := CommitPlanStats{
		FileCount:      len(files),
		DocPathCount:   len(docPaths),
		StatePathCount: len(statePaths),
	}
	for _, patch := range input.Patches {
		stats.AddedLines += patch.Minimality.AddedLines
		stats.RemovedLines += patch.Minimality.RemovedLines
	}
	return CommitPlan{Message: message, Files: files, Stats: stats}, nil
}

// buildCommitMessage builds the conventional commit message:
// `docs: synchronize documentation` + sorted "Documentation updates" list +
// sorted "State updates" list + the provenance ref line
// `Provenance: <run-id>`.
func buildCommitMessage(runID string, docPaths, statePaths []string) (string, error) {
	if err := assertSingleLine(runID, "provenance runId"); err != nil {
		return "", err
	}
	lines := []string{commitSubject, ""}
	if len(docPaths) > 0 {
		lines = append(lines, "Documentation updates:", "")
		for _, path := range sortedStringsCopy(docPaths) {
			lines = append(lines, "- "+path)
		}
		lines = append(lines, "")
	}
	if len(statePaths) > 0 {
		lines = append(lines, "State updates:", "")
		for _, path := range sortedStringsCopy(statePaths) {
			lines = append(lines, "- "+path)
		}
		lines = append(lines, "")
	}
	lines = append(lines, "Provenance: "+runID)
	return strings.Join(lines, "\n"), nil
}

/* --------------------------------------------------------------- write path */

// DocFileApplier is the injected disk seam — the ONLY sanctioned writer
// (caller-supplied).
type DocFileApplier func(path, content string) error

// WriteDocCommitResult is the result of WriteDocCommit.
type WriteDocCommitResult struct {
	// Sha is the new HEAD commit sha.
	Sha string
	// Staged is the post-index staged paths (a subset of the plan — proven by
	// the assert).
	Staged []string
}

// WriteDocCommit is the deterministic write+commit (P6-T02):
//
//  1. write EXACTLY the planned files, in plan order, through the seam;
//  2. stage EXACTLY the planned paths (`git add -- <paths...>`);
//  3. post-index scope assert — `git diff --cached --name-only` ⊆ plan.Files,
//     else a *CommitScopeError (abort BEFORE the commit);
//  4. empty-staged guard — nothing staged → a *EmptyCommitError;
//  5. plain `git commit -m <plan.Message>` (no hook bypass).
func WriteDocCommit(
	ctx context.Context,
	git *gitx.GitClient,
	plan CommitPlan,
	apply DocFileApplier,
) (WriteDocCommitResult, error) {
	if apply == nil {
		return WriteDocCommitResult{}, newCommitAssemblyError(
			"writeDocCommit requires an apply disk seam ((path, content) => Promise<void>)")
	}
	if len(plan.Files) == 0 {
		return WriteDocCommitResult{}, newCommitAssemblyError(
			"writeDocCommit refused: the commit plan files list is empty")
	}
	planned := make(map[string]bool, len(plan.Files))
	for _, file := range plan.Files {
		if planned[file.Path] {
			return WriteDocCommitResult{}, newCommitAssemblyError(
				"writeDocCommit refused: duplicate plan file '" + file.Path + "'")
		}
		planned[file.Path] = true
	}

	for _, file := range plan.Files {
		if err := apply(file.Path, file.Content); err != nil {
			return WriteDocCommitResult{}, err
		}
	}

	paths := make([]string, 0, len(plan.Files))
	for _, file := range plan.Files {
		paths = append(paths, file.Path)
	}
	if err := git.StagePaths(ctx, paths); err != nil {
		return WriteDocCommitResult{}, err
	}

	staged, err := git.CachedFilePaths(ctx)
	if err != nil {
		return WriteDocCommitResult{}, err
	}
	var foreign []string
	for _, path := range staged {
		if !planned[path] {
			foreign = append(foreign, path)
		}
	}
	if len(foreign) > 0 {
		return WriteDocCommitResult{}, newCommitScopeError(foreign)
	}
	if len(staged) == 0 {
		return WriteDocCommitResult{}, newEmptyCommitError()
	}

	sha, err := git.Commit(ctx, plan.Message)
	if err != nil {
		return WriteDocCommitResult{}, err
	}
	return WriteDocCommitResult{Sha: sha, Staged: staged}, nil
}

/* ----------------------------------------------------------------- utilities */

// containsString reports membership (the TS Array.prototype.includes).
func containsStringValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// sortedCopy returns an ascending sorted copy (the TS `[...paths].sort()`).
func sortedStringsCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
