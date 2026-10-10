package workspace

import "github.com/DUDE616/docSync-go/internal/docutil"

// Ported from core/src/workspace/types.ts (P3-T02/T04; ARCH §4.7, §3.3, §3.7,
// §11.4; D2).
//
// The workspace is the writer's ENTIRE universe (ARCH §4.7: Sonnet receives a
// constrained workspace, not the whole repository). Anything not in it cannot
// be patched — invariants 2 (never code files) and 3 (never outside the
// manifest allowlist) are enforced structurally by what the assembler admits,
// and lock layer 2 (§11.4) by the readonly markers + the writer prompt's
// protected-target prohibition (duties.go).
//
// Purity contract (binding for the whole module):
//   - the assembler NEVER performs fs/git I/O — source/doc contents are
//     caller-fetched ({path, content} arrays, e.g. via GitClient.CatFile);
//   - the clock is INJECTED (clock func() string) — AssembledAt never comes
//     from a local time call, so identical inputs (incl. the clock value)
//     yield a byte-identical rendered payload (V-P3-1 determinism);
//   - size caps are bootstrap data (DefaultWorkspaceCaps), enforced BY the
//     assembler and REPORTED (truncated/omitted entries carry reasons) — never
//     silently dropped; they are per-run workspace budget data, not policy
//     calibration (D11 thresholds are a different class of data and stay in
//     the versioned policy).
//
// Absent-data discipline: a MISSING required input (nil pointer / nil slice)
// is a *WorkspaceAssemblyError naming the component (fail closed, never a
// partial assembly); an input that is PRESENT but semantically empty
// ('' diff, empty arrays, '' instructions) or explicitly null (a nil
// decision) still assembles the component with an explicit empty/none marker
// and reason. The assembler NEVER omits a component.

/* ---------------------------------------------------------- the seven parts */

// WorkspaceComponentName is one component's name (element of the seven-tuple).
type WorkspaceComponentName string

// The exact seven workspace components (ARCH §4.7), in canonical component
// order — the ordering used everywhere (assembly record, section rendering).
// No eighth component can appear without changing this tuple; the workspace
// contains exactly these.
const (
	ComponentGitDiff            WorkspaceComponentName = "git_diff"
	ComponentChangeAnalysis     WorkspaceComponentName = "change_analysis"
	ComponentJevDecision        WorkspaceComponentName = "jev_decision"
	ComponentManifestPolicy     WorkspaceComponentName = "manifest_policy"
	ComponentSourceFiles        WorkspaceComponentName = "source_files"
	ComponentRelevantDocuments  WorkspaceComponentName = "relevant_documents"
	ComponentProjectInstruction WorkspaceComponentName = "project_instructions"
)

// WorkspaceComponents is the canonical component ordering (TS
// WORKSPACE_COMPONENTS — a closed readonly tuple).
var WorkspaceComponents = []WorkspaceComponentName{
	ComponentGitDiff,
	ComponentChangeAnalysis,
	ComponentJevDecision,
	ComponentManifestPolicy,
	ComponentSourceFiles,
	ComponentRelevantDocuments,
	ComponentProjectInstruction,
}

// workspaceComponentSet is the closed membership set of the seven names.
var workspaceComponentSet = func() map[WorkspaceComponentName]struct{} {
	set := make(map[WorkspaceComponentName]struct{}, len(WorkspaceComponents))
	for _, name := range WorkspaceComponents {
		set[name] = struct{}{}
	}
	return set
}()

// IsWorkspaceComponentName reports whether name is one of the exact seven
// component names (TS isWorkspaceComponentName runtime guard).
func IsWorkspaceComponentName(name string) bool {
	_, ok := workspaceComponentSet[WorkspaceComponentName(name)]
	return ok
}

// ProtectedModes is the D2 lock-layer-2 mode set (P3-T04): the manifest modes
// whose document paths the writer must NEVER patch, as a closed tuple.
// Membership (fail closed, always a superset of what ARCH §11.4 layer 3
// rejects):
//   - "locked" — terminal D2 floor: read-only at ANY confidence;
//   - "generated" — the AI never fights another generator;
//   - "external" — paths ABSENT from the manifest (D7: the manifest is an
//     allowlist — unlisted docs classify as the external pseudo-mode, never
//     writable).
//
// HUMAN-MODE RULING (Phase 3; supersedes the P3-T04 extra-conservative
// marking): "human" is NOT protected. ARCH §11.2 and the P2 policy engine
// treat "human" as review-gated patch-eligible (MODE_LADDER.human ==
// MODE_LADDER.suggest); blocking it would break the docsync fix path.
// Human-mode patches route through human review (policy outcome suggest or
// review) — they are writable in the workspace, never auto-applied.
//
// ALL source-file paths are protected by invariant 2 regardless of mode (code
// files are analysis-only) — that guard lives in the assembler, not in this
// mode list.
//
// DRIFT GUARD: both mode sets are pinned to the shared committed fixture
// packages/schemas/test/fixtures/modes.fixture.json — the core AND
// @docsync/policy (OWNERSHIP_FLOOR_MODES) test suites assert set equality
// with the fixture, so neither side can drift.
var ProtectedModes = []string{"locked", "generated", "external"}

// WritableModes lists the manifest modes that admit patch emission in the
// writer workspace: "auto", "suggest", and "human" (review-gated: human-mode
// patches route through human review — policy outcome suggest/review, never
// auto-apply). Pinned to the shared modes fixture (see ProtectedModes).
var WritableModes = []string{"auto", "suggest", "human"}

// writableModeSet is the centralized WRITABLE_MODES membership set — the
// single authority the assembler's per-document classification consumes (one
// flip point).
var writableModeSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(WritableModes))
	for _, mode := range WritableModes {
		set[mode] = struct{}{}
	}
	return set
}()

// IsWritableMode reports whether mode admits patch emission per
// WritableModes (TS isWritableMode).
func IsWritableMode(mode string) bool {
	_, ok := writableModeSet[mode]
	return ok
}

// WorkspaceMode mirrors the policy package's OwnershipFloorMode union
// (=@docsync/schemas DocMode | "external"). Core depends on schemas only and
// cannot import the policy package, so the union is mirrored here with a
// cross-copy consistency note (Phase 5 goldens catch drift). "external" is the
// §11.2 ownership category surfaced as a floor-carrying pseudo-mode.
type WorkspaceMode string

// WorkspaceModeValues is the mirrored OwnershipFloorMode union, in canonical
// order (TS WORKSPACE_MODE_VALUES).
const (
	WorkspaceModeAuto      WorkspaceMode = "auto"
	WorkspaceModeSuggest   WorkspaceMode = "suggest"
	WorkspaceModeLocked    WorkspaceMode = "locked"
	WorkspaceModeGenerated WorkspaceMode = "generated"
	WorkspaceModeHuman     WorkspaceMode = "human"
	WorkspaceModeExternal  WorkspaceMode = "external"
)

var WorkspaceModeValues = []WorkspaceMode{
	WorkspaceModeAuto,
	WorkspaceModeSuggest,
	WorkspaceModeLocked,
	WorkspaceModeGenerated,
	WorkspaceModeHuman,
	WorkspaceModeExternal,
}

// workspaceModeSet is the mirrored-union membership set.
var workspaceModeSet = func() map[WorkspaceMode]struct{} {
	set := make(map[WorkspaceMode]struct{}, len(WorkspaceModeValues))
	for _, mode := range WorkspaceModeValues {
		set[mode] = struct{}{}
	}
	return set
}()

// IsWorkspaceMode reports whether value is a mirrored OwnershipFloorMode
// value (TS isWorkspaceMode runtime guard).
func IsWorkspaceMode(value string) bool {
	_, ok := workspaceModeSet[WorkspaceMode(value)]
	return ok
}

/* -------------------------------------------------------------- components */

// WorkspaceSectionStatus is one component's status:
//   - StatusContent — the component carries a rendered body;
//   - StatusEmpty   — input PRESENT but semantically empty (” diff, empty
//     array, ” instructions) — explicit marker + reason, never omitted;
//   - StatusNone    — nullable/optional input absent (a nil decision, no
//     policySummary) — explicit none marker + reason.
//
// The assembler NEVER omits a component: every one of the seven is present.
type WorkspaceSectionStatus string

const (
	StatusContent WorkspaceSectionStatus = "content"
	StatusEmpty   WorkspaceSectionStatus = "empty"
	StatusNone    WorkspaceSectionStatus = "none"
)

// WorkspaceSection is one workspace component's assembled record (TS
// WorkspaceSection). Body is FINAL at assembly time: repo-derived content is
// already escape-neutralized (render.go EscapeWorkspaceContent) and caps are
// already applied. render.go adds only deterministic framing around it.
type WorkspaceSection struct {
	Name             WorkspaceComponentName `json:"name"`
	Status           WorkspaceSectionStatus `json:"status"`
	Reason           string                 `json:"reason"`
	Body             string                 `json:"body"`
	Truncated        bool                   `json:"truncated"`
	TruncationReason *string                `json:"truncation_reason,omitempty"`
}

/* -------------------------------------------------------------------- caps */

// WorkspaceCaps holds the workspace size caps (ARCH §4.7 bounded workspace +
// §3.7 bounding; P3-T02). The assembler enforces them with a DETERMINISTIC
// truncation order (entries sorted by path, then size); every binding cap is
// reported via the component's Truncated flag + reasons and per-entry marker
// lines in the body — never a silent drop (§3.7: overflow is reported, never
// dropped).
type WorkspaceCaps struct {
	// MaxSourceFileBytes is the per-entry byte cap for one source file's content.
	MaxSourceFileBytes int `json:"maxSourceFileBytes"`
	// MaxDocBytes is the per-entry byte cap for one document's content.
	MaxDocBytes int `json:"maxDocBytes"`
	// MaxTotalBytes is the combined byte cap across the source_files +
	// relevant_documents bodies.
	MaxTotalBytes int `json:"maxTotalBytes"`
	// MaxSourceFiles is the maximum number of source files admitted.
	MaxSourceFiles int `json:"maxSourceFiles"`
	// MaxDocs is the maximum number of documents admitted.
	MaxDocs int `json:"maxDocs"`
}

// DefaultWorkspaceCaps holds the bootstrap cap defaults (P3-T02). LABELED
// bootstrap data: workspace BUDGET numbers, not decision thresholds — nothing
// here gates automation (D11's 0.94/0.72 calibration lives in the versioned
// policy, never here).
var DefaultWorkspaceCaps = WorkspaceCaps{
	MaxSourceFileBytes: 16384,
	MaxDocBytes:        65536,
	MaxTotalBytes:      262144,
	MaxSourceFiles:     20,
	MaxDocs:            10,
}

// WorkspaceCapsOverrides is the caller's partial cap override set (TS
// `caps?: Partial<WorkspaceCaps>`): each non-nil field replaces the default.
// A nil pointer means "use the default"; a pointer to 0 is a legal degenerate
// budget (admit nothing / truncate to nothing) — every binding cap is still
// REPORTED, never silent.
type WorkspaceCapsOverrides struct {
	MaxSourceFileBytes *int
	MaxDocBytes        *int
	MaxTotalBytes      *int
	MaxSourceFiles     *int
	MaxDocs            *int
}

// WithOverrides merges the override set over DefaultWorkspaceCaps (TS
// `{ ...DEFAULT_WORKSPACE_CAPS, ...input.caps }`), preserving every unset
// default.
func WithOverrides(overrides *WorkspaceCapsOverrides) WorkspaceCaps {
	caps := DefaultWorkspaceCaps
	if overrides == nil {
		return caps
	}
	if overrides.MaxSourceFileBytes != nil {
		caps.MaxSourceFileBytes = *overrides.MaxSourceFileBytes
	}
	if overrides.MaxDocBytes != nil {
		caps.MaxDocBytes = *overrides.MaxDocBytes
	}
	if overrides.MaxTotalBytes != nil {
		caps.MaxTotalBytes = *overrides.MaxTotalBytes
	}
	if overrides.MaxSourceFiles != nil {
		caps.MaxSourceFiles = *overrides.MaxSourceFiles
	}
	if overrides.MaxDocs != nil {
		caps.MaxDocs = *overrides.MaxDocs
	}
	return caps
}

/* ----------------------------------------------------------------- batching */

// BatchInfo is the batch context attached to a workspace AFTER assembly
// (P3-T03; ARCH §7.1 item 6: related candidate docs sharing context are
// batched into one session). The assembler leaves Batch unset; the
// orchestrator runs BatchRelatedDocs over the retrieval candidates and
// attaches the matching BatchInfo to each session's workspace before
// rendering.
type BatchInfo struct {
	// SessionIndex is the 1-based index of this session within the batch plan
	// (deterministic order).
	SessionIndex int `json:"sessionIndex"`
	// SessionCount is the total number of sessions in the plan.
	SessionCount int `json:"sessionCount"`
	// Docs holds the doc paths assigned to THIS session (plan order).
	Docs []string `json:"docs"`
	// Reason is the deterministic grouping reason for this session.
	Reason string `json:"reason"`
}

/* ------------------------------------------------------------- the assembly */

// ConstrainedWorkspace is the constrained workspace (ARCH §4.7, §3.3
// RETRIEVAL ENGINE → SONNET edge).
//
// ProtectedPaths (D2, lock layer 2): every path the writer must never patch —
// protected-mode docs (ProtectedModes), manifest-unlisted docs (external), and
// ALL source files. WritableAllowlist: documents whose manifest mode is in
// WritableModes (auto | suggest | human; human is review-gated) and that were
// ADMITTED to the workspace — the single positive authority downstream layers
// validate against (§11.4 layer 3 re-derives the authoritative allowlist from
// the manifest itself, never from model output).
//
// A workspace with ZERO writable documents is LEGAL (analysis-only session):
// protected candidates may still be investigated/reported, the writer simply
// has nothing it may patch (V-P3-1).
type ConstrainedWorkspace struct {
	// Components carries exactly the seven components (a closed record — no
	// other keys can exist).
	Components map[WorkspaceComponentName]WorkspaceSection
	// ProtectedPaths marks paths read-only (see ProtectedModes membership note)
	// — lock layer 2.
	ProtectedPaths map[string]bool
	// WritableAllowlist lists the documents the writer may patch (WritableModes:
	// auto | suggest | human).
	WritableAllowlist map[string]bool
	// Caps holds the effective caps (merged over DefaultWorkspaceCaps).
	Caps WorkspaceCaps
	// Batch is the batch context; unset by the assembler — attached by the
	// orchestrator (P3-T03).
	Batch *BatchInfo
	// AssembledAt is the injected clock value (never a local clock).
	AssembledAt string
}

// AssembledWorkspace is the alias used in P3-T02 wording; identical type to
// ConstrainedWorkspace (TS AssembledWorkspace).
type AssembledWorkspace = ConstrainedWorkspace

/* ------------------------------------------------------------------- errors */

// CodeWorkspaceAssembly is the coded-error code carried by
// WorkspaceAssemblyError. The TS WorkspaceAssemblyError was a plain Error
// subclass (no EDOCSYNC code), so per the migration conventions the closest
// listed code is used: docutil.CodeConfig (EDOCSYNC_CONFIG — a malformed
// assembly INPUT is a configuration problem that fails closed before any AI
// stage runs, exit-code-2 semantics).
const CodeWorkspaceAssembly = docutil.CodeConfig

// WorkspaceAssemblyError is the typed assembly failure (fail closed, D8): a
// required input is missing, malformed, or duplicated — naming the affected
// workspace component. A partial workspace is NEVER assembled silently.
//
// Error() preserves the exact TS message text:
//
//	workspace assembly failed (component '<component>'): <message>
//
// Component is the component name the failure concerns, or "(workspace)" for
// run-level input problems (e.g. the clock).
type WorkspaceAssemblyError struct {
	Component string
	inner     *docutil.Error
}

// NewWorkspaceAssemblyError builds the error, rendering the exact TS message
// text.
func NewWorkspaceAssemblyError(component, message string) *WorkspaceAssemblyError {
	msg := "workspace assembly failed (component '" + component + "'): " + message
	return &WorkspaceAssemblyError{Component: component, inner: docutil.New(CodeWorkspaceAssembly, msg)}
}

// Error renders the coded error (`EDOCSYNC_CONFIG: <message>`).
func (e *WorkspaceAssemblyError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_CONFIG (see the type doc).
func (e *WorkspaceAssemblyError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *WorkspaceAssemblyError) Unwrap() error { return e.inner }
