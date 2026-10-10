package workspace

// Ported from core/src/workspace/assemble.ts (P3-T02; ARCH §4.7, §3.3, §3.7, D2)
// with D2 protected-path marking (P3-T04; ARCH §11.4 lock layer 2).
//
// CONTRACT (binding):
//   - Purity: the assembler NEVER does fs/git I/O — SourceFiles/Documents
//     contents are caller-fetched (e.g. via GitClient.CatFile); the clock is
//     injected; same inputs → byte-identical RenderWorkspace output
//     (render.go golden surface).
//   - Fail closed on assembly (D8): a missing/malformed required input returns
//     a *WorkspaceAssemblyError naming the component — a partial workspace is
//     never assembled silently. Present-but-empty (or explicitly null) inputs
//     assemble with an explicit empty/none marker + reason.
//   - Exactly the seven components (ARCH §4.7), always all present; the git
//     diff itself carries no size cap (it is already bounded upstream by the
//     analysis caps, ARCH §3.7); per-entry/per-count/total caps bind on the
//     source_files + relevant_documents components.
//   - Cap enforcement (§3.7 — report, never silently drop): entries are
//     byte-truncated to their per-entry cap, then count-capped
//     (MaxSourceFiles/MaxDocs), then the combined source+document budget
//     (MaxTotalBytes) is filled in the deterministic order (path ascending,
//     then post-truncation bytes ascending, then kind source-file before
//     document). Every truncation/omission is reported in the section body
//     (escape-protected note/entry marker lines) and via the section's
//     Truncated flag + TruncationReason.
//   - D2 protected paths (lock layer 2): among the SUPPLIED input paths,
//     document paths whose manifest mode is in ProtectedModes
//     (locked/generated) and paths absent from the manifest (the external
//     pseudo-mode, D7 fail-closed) are marked read-only with a per-path
//     reason; ALL source-file paths are marked read-only (invariant 2: code
//     files are analysis-only). Protection is about ownership, not visibility
//     — but an unsupplied path needs no marking (the workspace is the writer's
//     entire universe: anything not in it cannot be patched).
//     WritableAllowlist = manifest mode in WritableModes (auto | suggest |
//     human) ONLY — and only docs ADMITTED to the workspace (a cap-omitted doc
//     is not visible to the writer, so it is not writable here; §11.4 layer 3
//     re-derives the authoritative allowlist from the manifest itself). A
//     zero-writable workspace is legal (analysis-only session) and protected
//     candidates still appear — investigation, never patch emission.
//   - "human" note (HUMAN-MODE RULING, Phase 3): NOT protected — ARCH
//     §11.2/modes/resolve.ts and the P2 policy engine treat "human" as
//     review-gated patch-eligible; writability derives from the centralized
//     WritableModes (types.go), so human-mode patches route through human
//     review (policy outcome suggest/review), never auto-apply.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* -------------------------------------------------------------------- input */

// WorkspaceSourceFile is one caller-fetched source file (the assembler never
// touches fs/git).
type WorkspaceSourceFile struct {
	Path    string
	Content string
}

// WorkspaceDocument is one caller-fetched candidate document.
type WorkspaceDocument struct {
	Path    string
	Content string
}

// AssembleWorkspaceInput is the assembler input. All seven components' data
// must be present (see types.go); a nil pointer/slice field means the input
// was MISSING and fails closed, while a present-but-empty value assembles an
// explicit empty/none component.
type AssembleWorkspaceInput struct {
	// GitDiff is the raw `git diff` text (already bounded upstream by the
	// analysis caps). Nil → WorkspaceAssemblyError (component 'git_diff').
	GitDiff *string
	// ChangeAnalysis is the structured change analysis (ARCH §4.1).
	// Nil → WorkspaceAssemblyError (component 'change_analysis').
	ChangeAnalysis *schemas.ChangeAnalysis
	// Decision is the Jev decision (ARCH §5.4) — explicitly nullable: a nil
	// pointer renders the none-marker component (analysis-only session).
	Decision *schemas.Decision
	// Manifest is the manifest allowlist — classification + writableAllowlist
	// authority (D7). Nil → WorkspaceAssemblyError (component 'manifest_policy').
	Manifest *schemas.Manifest
	// PolicySummary is the compiled policy's routing data as deterministic
	// text — system-rendered by the caller, NO secrets. Optional: nil →
	// none-marker inside the manifest_policy section.
	PolicySummary *string
	// SourceFiles holds the relevant source files (caller-fetched contents).
	// Nil → WorkspaceAssemblyError (component 'source_files'); an empty slice
	// is present-but-empty.
	SourceFiles []WorkspaceSourceFile
	// Documents holds the relevant documents (caller-fetched contents).
	// Nil → WorkspaceAssemblyError (component 'relevant_documents'); an empty
	// slice is present-but-empty.
	Documents []WorkspaceDocument
	// Instructions is the project instructions (repo-owned free text).
	// Nil → WorkspaceAssemblyError (component 'project_instructions').
	Instructions *string
	// Clock is the INJECTED clock — required; AssembledAt never comes from a
	// local clock. Nil → WorkspaceAssemblyError (component '(workspace)').
	Clock func() string
	// Caps holds cap overrides merged over DefaultWorkspaceCaps (nil → the
	// defaults).
	Caps *WorkspaceCapsOverrides
	// Graph is the document-graph context guard: when present it MUST be
	// usable (graph.Errors empty — fail closed, D8). Structural grouping
	// happens in BatchRelatedDocs (P3-T03); assembly itself consumes no graph
	// data.
	Graph *graph.DocumentGraph
}

/* ------------------------------------------------------------------ helpers */

func byteLength(text string) int {
	return len(text)
}

// cutToByteCap cuts content to at most cap bytes at a code-point (rune)
// boundary. Deterministic for a given input (left-to-right rune
// accumulation). Go strings are UTF-8, so a rune boundary never splits a
// multi-byte sequence — the byte-accounting equivalent of the TS
// UTF-16-unit walk (a cut inside a multi-byte sequence is impossible here by
// construction, and the TS surrogate-pair units cover the same characters).
func cutToByteCap(content string, cap int) string {
	if byteLength(content) <= cap {
		return content
	}
	bytes := 0
	keptBytes := 0
	for index := 0; index < len(content); {
		// DecodeRuneInString reports the ACTUAL byte width of the decoded
		// unit (1 for an invalid byte, which the walk then accounts as one
		// byte), so keptBytes always lands on a rune boundary.
		_, size := utf8.DecodeRuneInString(content[index:])
		if size < 1 {
			size = 1
		}
		if bytes+size > cap {
			break
		}
		bytes += size
		index += size
		keptBytes = index
	}
	return content[:keptBytes]
}

// entryKind is one capped entry's kind. The rank pins the deterministic total
// order's last tiebreaker (source-file before document).
type entryKind string

const (
	kindSourceFile entryKind = "source-file"
	kindDocument   entryKind = "document"
)

// kindRank mirrors the TS KIND_RANK map.
func kindRank(kind entryKind) int {
	switch kind {
	case kindSourceFile:
		return 0
	case kindDocument:
		return 1
	default:
		return 2
	}
}

// capEntry is one entry under cap enforcement (TS CapEntry).
type capEntry struct {
	kind      entryKind
	path      string
	mode      *WorkspaceMode
	writable  bool
	readonly  bool
	reason    string
	content   string
	bytes     int
	truncated bool
	// truncationReason is nil until a byte cap binds.
	truncationReason *string
	omitted          bool
	// omissionReason is nil until a count/total cap omits the entry.
	omissionReason *string
}

// entryOrder is the deterministic total order: path asc, then
// post-truncation bytes asc, then kind (TS entryOrder).
func entryOrder(a, b *capEntry) int {
	if a.path != b.path {
		if a.path < b.path {
			return -1
		}
		return 1
	}
	if a.bytes != b.bytes {
		return a.bytes - b.bytes
	}
	return kindRank(a.kind) - kindRank(b.kind)
}

// documentClassification is one document's manifest classification (TS
// classifyDocument's return).
type documentClassification struct {
	mode     WorkspaceMode
	writable bool
	reason   string
}

// classifyDocument classifies a document path against the manifest (TS
// classifyDocument): an unlisted path is the `external` pseudo-mode (D7
// fail-closed); precedence is ARCH §11.3 — per-document entry > manifest
// defaults > built-in 'suggest'.
func classifyDocument(path string, manifest *schemas.Manifest) documentClassification {
	var entry *schemas.ManifestEntry
	for i := range manifest.Documents {
		if manifest.Documents[i].Path == path {
			entry = &manifest.Documents[i]
			break
		}
	}
	if entry == nil {
		return documentClassification{
			mode:     WorkspaceModeExternal,
			writable: false,
			reason:   "not in the manifest allowlist (D7) — fail-closed: treated as externally owned; patch emission forbidden",
		}
	}
	// Manifest entries carry DocMode only; the 'external' pseudo-mode is
	// produced by the not-in-manifest branch above.
	mode := schemas.DocModeSuggest
	if entry.Mode != nil {
		mode = *entry.Mode
	} else if manifest.Defaults != nil && manifest.Defaults.Mode != nil {
		mode = *manifest.Defaults.Mode
	}
	// Writability derives from the centralized WritableModes (one flip point —
	// the HUMAN-MODE RULING reversed `human` from protected to review-gated
	// writable there; this switch must not hard-code a second opinion).
	writable := IsWritableMode(string(mode))
	reason := ""
	switch mode {
	case schemas.DocModeAuto:
		reason = "manifest mode 'auto' — patch emission permitted (verification and the policy engine still gate it)"
	case schemas.DocModeSuggest:
		reason = "manifest mode 'suggest' — proposal-only: patch emission permitted, auto-apply never"
	case schemas.DocModeLocked:
		reason = "manifest mode 'locked' — terminal D2 floor: read-only at any confidence, patch emission forbidden"
	case schemas.DocModeGenerated:
		reason = "manifest mode 'generated' — excluded from patch candidates: the AI never fights another generator"
	case schemas.DocModeHuman:
		reason = "manifest mode 'human' — review-gated patch-eligible (ARCH §11.2): patches route through human review (policy outcome suggest/review), never auto-apply"
	}
	return documentClassification{
		mode:     WorkspaceMode(string(mode)),
		writable: writable,
		reason:   reason,
	}
}

// makeEntry builds one capped entry (TS makeEntry). The caller has already
// validated the entry path (nonempty, unduplicated). Escape FIRST, then
// truncate: the cap binds on the bytes actually shipped and a cut inside an
// escape sequence can never resurrect a raw marker.
func makeEntry(
	kind entryKind,
	path string,
	content string,
	byteCap int,
	byteCapName string,
	mode *WorkspaceMode,
	writable bool,
	readonly bool,
	reason string,
) *capEntry {
	escaped := EscapeWorkspaceContent(content)
	escapedBytes := byteLength(escaped)
	kept := cutToByteCap(escaped, byteCap)
	truncated := kept != escaped
	entry := &capEntry{
		kind:      kind,
		path:      path,
		mode:      mode,
		writable:  writable,
		readonly:  readonly,
		reason:    reason,
		content:   kept,
		bytes:     byteLength(kept),
		truncated: truncated,
	}
	if truncated {
		reasonText := fmt.Sprintf("cap %s (%d bytes): content truncated from %d to %d bytes",
			byteCapName, byteCap, escapedBytes, byteLength(kept))
		entry.truncationReason = &reasonText
	}
	return entry
}

// renderEntry renders one entry block (TS renderEntry).
func renderEntry(entry *capEntry) string {
	fields := []string{"kind: " + string(entry.kind), "path: " + entry.path}
	if entry.kind == kindDocument {
		mode := ""
		if entry.mode != nil {
			mode = string(*entry.mode)
		}
		fields = append(fields, "mode: "+mode)
	}
	fields = append(fields,
		"writable: "+boolText(entry.writable),
		"readonly: "+boolText(entry.readonly),
		"reason: "+entry.reason,
	)
	if entry.truncated && entry.truncationReason != nil {
		fields = append(fields, "truncated: "+*entry.truncationReason)
	}
	if entry.omitted && entry.omissionReason != nil {
		fields = append(fields, "omitted: "+*entry.omissionReason)
	}
	return RenderWorkspaceEntry(fields, entry.content)
}

// boolText renders a boolean exactly as the TS template interpolation does.
func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// joinReasons returns the unique reasons in first-occurrence order, joined
// with "; " — deterministic summary text (TS joinReasons). Returns nil when
// there is nothing to report.
func joinReasons(reasons []*string) *string {
	unique := make([]string, 0, len(reasons))
	seen := make(map[string]struct{}, len(reasons))
	for _, reason := range reasons {
		if reason == nil {
			continue
		}
		if _, dup := seen[*reason]; dup {
			continue
		}
		seen[*reason] = struct{}{}
		unique = append(unique, *reason)
	}
	if len(unique) == 0 {
		return nil
	}
	joined := strings.Join(unique, "; ")
	return &joined
}

// capReportNote renders the cap-report note for a component's omitted entries
// (TS capReportNote). Returns nil when nothing was omitted.
func capReportNote(component WorkspaceComponentName, omitted []*capEntry) *string {
	if len(omitted) == 0 {
		return nil
	}
	lines := []string{"kind: cap-report", "component: " + string(component)}
	for index, entry := range omitted {
		reason := ""
		if entry.omissionReason != nil {
			reason = *entry.omissionReason
		}
		lines = append(lines, fmt.Sprintf("omitted[%d]: %s — %s", index, entry.path, reason))
	}
	note := RenderWorkspaceNote(lines)
	return &note
}

// sectionFor assembles one capped component's section (TS sectionFor): the
// cap report first, then the admitted entries in deterministic order. A
// component with no entries at all is 'empty' (present-but-empty input) and
// carries the explicit reason — never omitted.
func sectionFor(
	name WorkspaceComponentName,
	entries []*capEntry,
	emptyReason string,
) WorkspaceSection {
	omitted := make([]*capEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.omitted {
			omitted = append(omitted, entry)
		}
	}
	bodyParts := make([]string, 0, len(entries)+1)
	note := capReportNote(name, omitted)
	if note != nil {
		bodyParts = append(bodyParts, *note)
	}
	for _, entry := range entries {
		if !entry.omitted {
			bodyParts = append(bodyParts, renderEntry(entry))
		}
	}
	body := strings.Join(bodyParts, "\n")
	truncated := false
	for _, entry := range entries {
		if entry.truncated || entry.omitted {
			truncated = true
			break
		}
	}
	reasons := make([]*string, 0, len(entries)+len(omitted))
	for _, entry := range entries {
		reasons = append(reasons, entry.truncationReason)
	}
	for _, entry := range omitted {
		reasons = append(reasons, entry.omissionReason)
	}
	section := WorkspaceSection{
		Name:             name,
		Status:           StatusContent,
		Reason:           "",
		Body:             body,
		Truncated:        truncated,
		TruncationReason: joinReasons(reasons),
	}
	if len(entries) == 0 {
		section.Status = StatusEmpty
		section.Reason = emptyReason
	}
	return section
}

/* ------------------------------------------------------------- validation */

// canonicalComponentJSON serializes one structured component (TS
// canonicalJsonOrThrow). The record's STRING VALUES are escape-neutralized
// BEFORE serialization (render.go EscapeStructuredComponentValue): the
// records are schema-validated, but their free-text fields (e.g. the Jev
// decision's evidence note — model-authored) are untrusted DATA and must not
// carry live containment tags or docsync frame markers into the rendered JSON.
// Idempotent — a no-op for marker-free values.
//
// Numbers survive the generic-tree round-trip as json.Number, so the canonical
// writer emits the exact literal the schema produced (740, 0.91) — the same
// bytes JSON.stringify produced in TypeScript.
func canonicalComponentJSON(value any, component WorkspaceComponentName, key string) (string, error) {
	encoded, err := marshalJSONNoHTMLEscape(value)
	if err != nil {
		return "", NewWorkspaceAssemblyError(string(component),
			fmt.Sprintf("input '%s' is not JSON-representable: %s", key, err.Error()))
	}
	tree, err := decodeGenericTree(encoded)
	if err != nil {
		return "", NewWorkspaceAssemblyError(string(component),
			fmt.Sprintf("input '%s' is not JSON-representable: %s", key, err.Error()))
	}
	canonical, err := docutil.CanonicalJSON(EscapeStructuredComponentValue(tree))
	if err != nil {
		return "", NewWorkspaceAssemblyError(string(component),
			fmt.Sprintf("input '%s' is not JSON-representable: %s", key, err.Error()))
	}
	return canonical, nil
}

// marshalJSONNoHTMLEscape marshals value with HTML escaping DISABLED, so the
// bytes match JSON.stringify (which does not escape `<`, `>` or `&`) — the
// canonical writer escapes only what JSON requires, and the workspace's
// escape pass runs on the decoded string values instead.
func marshalJSONNoHTMLEscape(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// decodeGenericTree decodes JSON bytes into the generic tree the canonical
// writer and the escape walk consume (numbers as json.Number).
func decodeGenericTree(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return nil, err
	}
	return tree, nil
}

/* ---------------------------------------------------------------- assembler */

// AssembleWorkspace assembles the constrained workspace (P3-T02).
// Deterministic and pure: same inputs (incl. the injected clock value) →
// byte-identical RenderWorkspace output. See the file header for the binding
// contract.
//
// It returns a *WorkspaceAssemblyError (fail closed, D8) for a
// missing/malformed/duplicated required input, and never assembles a partial
// workspace.
func AssembleWorkspace(input AssembleWorkspaceInput) (*ConstrainedWorkspace, error) {
	if input.Clock == nil {
		return nil, NewWorkspaceAssemblyError("(workspace)",
			"clock is required and must be a function (injected clock — never a local clock)")
	}
	assembledAt := input.Clock()
	if assembledAt == "" {
		return nil, NewWorkspaceAssemblyError("(workspace)",
			"clock must return a nonempty string")
	}

	caps := WithOverrides(input.Caps)
	// 0 is a legal degenerate budget (admit nothing / truncate to nothing) —
	// every binding cap is still REPORTED, never silent. Go ints are always
	// integral, so only the non-negative bound is checkable here.
	for _, c := range []struct {
		name  string
		value int
	}{
		{"maxSourceFileBytes", caps.MaxSourceFileBytes},
		{"maxDocBytes", caps.MaxDocBytes},
		{"maxTotalBytes", caps.MaxTotalBytes},
		{"maxSourceFiles", caps.MaxSourceFiles},
		{"maxDocs", caps.MaxDocs},
	} {
		if c.value < 0 {
			return nil, NewWorkspaceAssemblyError("(workspace)",
				fmt.Sprintf("cap '%s' must be a non-negative integer, got %d", c.name, c.value))
		}
	}

	if input.GitDiff == nil {
		return nil, NewWorkspaceAssemblyError(string(ComponentGitDiff),
			"required input 'gitDiff' is missing or not a string")
	}
	gitDiff := *input.GitDiff
	if input.ChangeAnalysis == nil {
		return nil, NewWorkspaceAssemblyError(string(ComponentChangeAnalysis),
			"required input 'changeAnalysis' is missing or not an object")
	}
	if input.Manifest == nil {
		return nil, NewWorkspaceAssemblyError(string(ComponentManifestPolicy),
			"required input 'manifest' is missing or not an object")
	}
	// Decision is the nullable input: a nil pointer is explicit null (legal,
	// none-marker component).
	if input.SourceFiles == nil {
		return nil, NewWorkspaceAssemblyError(string(ComponentSourceFiles),
			"required input 'sourceFiles' is missing or not an array")
	}
	if input.Documents == nil {
		return nil, NewWorkspaceAssemblyError(string(ComponentRelevantDocuments),
			"required input 'documents' is missing or not an array")
	}
	if input.Instructions == nil {
		return nil, NewWorkspaceAssemblyError(string(ComponentProjectInstruction),
			"required input 'instructions' is missing or not a string")
	}
	instructions := *input.Instructions
	if input.Graph != nil && len(input.Graph.Errors) > 0 {
		return nil, NewWorkspaceAssemblyError("(workspace)",
			fmt.Sprintf("optional input 'graph' is unusable (%d composition error(s)) — fail closed (D8)",
				len(input.Graph.Errors)))
	}

	/* -- entries: build + per-entry byte caps -------------------------------- */

	sourceEntries := make([]*capEntry, 0, len(input.SourceFiles))
	seenSourcePaths := make(map[string]struct{}, len(input.SourceFiles))
	for _, file := range input.SourceFiles {
		if file.Path == "" {
			return nil, NewWorkspaceAssemblyError(string(ComponentSourceFiles),
				"entry path must be a nonempty string")
		}
		if _, dup := seenSourcePaths[file.Path]; dup {
			return nil, NewWorkspaceAssemblyError(string(ComponentSourceFiles),
				"duplicate source-file path '"+file.Path+"'")
		}
		seenSourcePaths[file.Path] = struct{}{}
		sourceEntries = append(sourceEntries, makeEntry(
			kindSourceFile,
			file.Path,
			file.Content,
			caps.MaxSourceFileBytes,
			"maxSourceFileBytes",
			nil,
			false,
			true,
			"source file — analysis only: code files are never modified (invariant 2)",
		))
	}

	docEntries := make([]*capEntry, 0, len(input.Documents))
	seenDocPaths := make(map[string]struct{}, len(input.Documents))
	for _, doc := range input.Documents {
		if doc.Path == "" {
			return nil, NewWorkspaceAssemblyError(string(ComponentRelevantDocuments),
				"entry path must be a nonempty string")
		}
		if _, dup := seenDocPaths[doc.Path]; dup {
			return nil, NewWorkspaceAssemblyError(string(ComponentRelevantDocuments),
				"duplicate document path '"+doc.Path+"'")
		}
		seenDocPaths[doc.Path] = struct{}{}
		classification := classifyDocument(doc.Path, input.Manifest)
		mode := classification.mode
		docEntries = append(docEntries, makeEntry(
			kindDocument,
			doc.Path,
			doc.Content,
			caps.MaxDocBytes,
			"maxDocBytes",
			&mode,
			classification.writable,
			!classification.writable,
			classification.reason,
		))
	}

	/* -- count caps (sorted by path, then size) ------------------------------ */

	sourceByOrder := append([]*capEntry(nil), sourceEntries...)
	docByOrder := append([]*capEntry(nil), docEntries...)
	sort.SliceStable(sourceByOrder, func(i, j int) bool { return entryOrder(sourceByOrder[i], sourceByOrder[j]) < 0 })
	sort.SliceStable(docByOrder, func(i, j int) bool { return entryOrder(docByOrder[i], docByOrder[j]) < 0 })
	for _, entry := range sourceByOrder[min(caps.MaxSourceFiles, len(sourceByOrder)):] {
		entry.omitted = true
		entry.content = ""
		entry.bytes = 0
		entry.omissionReason = strPtr(fmt.Sprintf(
			"cap maxSourceFiles (%d): omitted — entry over the count cap (sorted by path, then size)",
			caps.MaxSourceFiles))
	}
	for _, entry := range docByOrder[min(caps.MaxDocs, len(docByOrder)):] {
		entry.omitted = true
		entry.content = ""
		entry.bytes = 0
		entry.omissionReason = strPtr(fmt.Sprintf(
			"cap maxDocs (%d): omitted — entry over the count cap (sorted by path, then size)",
			caps.MaxDocs))
	}

	/* -- combined total budget across source + document entries -------------- */

	combined := make([]*capEntry, 0, len(sourceEntries)+len(docEntries))
	for _, entry := range append(append([]*capEntry(nil), sourceEntries...), docEntries...) {
		if !entry.omitted {
			combined = append(combined, entry)
		}
	}
	sort.SliceStable(combined, func(i, j int) bool { return entryOrder(combined[i], combined[j]) < 0 })
	totalBytes := 0
	for _, entry := range combined {
		if totalBytes+entry.bytes > caps.MaxTotalBytes {
			entry.omitted = true
			entry.content = ""
			entry.bytes = 0
			entry.omissionReason = strPtr(fmt.Sprintf(
				"cap maxTotalBytes (%d bytes): omitted — combined source+document budget exhausted after %d bytes",
				caps.MaxTotalBytes, totalBytes))
		} else {
			totalBytes += entry.bytes
		}
	}

	/* -- section bodies ------------------------------------------------------ */

	gitDiffSection := WorkspaceSection{
		Name:      ComponentGitDiff,
		Status:    StatusContent,
		Reason:    "",
		Body:      EscapeWorkspaceContent(gitDiff),
		Truncated: false,
	}
	if gitDiff == "" {
		gitDiffSection.Status = StatusEmpty
		gitDiffSection.Reason = "provided but empty — no diff content"
		gitDiffSection.Body = ""
	}

	changeAnalysisBody, err := canonicalComponentJSON(input.ChangeAnalysis, ComponentChangeAnalysis, "changeAnalysis")
	if err != nil {
		return nil, err
	}

	jevDecisionSection := WorkspaceSection{
		Name:      ComponentJevDecision,
		Status:    StatusNone,
		Reason:    "no Jev decision supplied — decision is a nullable input (analysis-only session)",
		Body:      "",
		Truncated: false,
	}
	if input.Decision != nil {
		decisionBody, err := canonicalComponentJSON(input.Decision, ComponentJevDecision, "decision")
		if err != nil {
			return nil, err
		}
		jevDecisionSection.Status = StatusContent
		jevDecisionSection.Reason = ""
		jevDecisionSection.Body = decisionBody
	}

	manifestBody, err := canonicalComponentJSON(input.Manifest, ComponentManifestPolicy, "manifest")
	if err != nil {
		return nil, err
	}
	policyBlock := "[policy: none — policySummary is an optional workspace input; no policy summary supplied]"
	if input.PolicySummary != nil {
		if *input.PolicySummary == "" {
			policyBlock = "[policy: empty — policySummary supplied but empty]"
		} else {
			policyBlock = *input.PolicySummary
		}
	}
	manifestPolicySection := WorkspaceSection{
		Name:      ComponentManifestPolicy,
		Status:    StatusContent,
		Reason:    "",
		Body:      strings.Join([]string{"[manifest]", manifestBody, "[policy]", policyBlock}, "\n"),
		Truncated: false,
	}

	sourceFilesSection := sectionFor(ComponentSourceFiles, sourceByOrder,
		"provided but empty — no source files supplied")
	documentsSection := sectionFor(ComponentRelevantDocuments, docByOrder,
		"provided but empty — no documents supplied")

	instructionsSection := WorkspaceSection{
		Name:      ComponentProjectInstruction,
		Status:    StatusContent,
		Reason:    "",
		Body:      EscapeWorkspaceContent(instructions),
		Truncated: false,
	}
	if instructions == "" {
		instructionsSection.Status = StatusEmpty
		instructionsSection.Reason = "provided but empty — no instructions supplied"
		instructionsSection.Body = ""
	}

	components := map[WorkspaceComponentName]WorkspaceSection{
		ComponentGitDiff:            gitDiffSection,
		ComponentChangeAnalysis:     {Name: ComponentChangeAnalysis, Status: StatusContent, Reason: "", Body: changeAnalysisBody},
		ComponentJevDecision:        jevDecisionSection,
		ComponentManifestPolicy:     manifestPolicySection,
		ComponentSourceFiles:        sourceFilesSection,
		ComponentRelevantDocuments:  documentsSection,
		ComponentProjectInstruction: instructionsSection,
	}

	/* -- D2 path marking ----------------------------------------------------- */

	// Protection is about OWNERSHIP, not workspace visibility: omitted
	// protected docs stay protected; omitted writable docs are NOT writable
	// (the writer cannot patch what it cannot see — fail closed).
	protectedPaths := make(map[string]bool, len(sourceEntries)+len(docEntries))
	writableAllowlist := make(map[string]bool, len(docEntries))
	for _, entry := range append(append([]*capEntry(nil), sourceEntries...), docEntries...) {
		if !entry.writable {
			protectedPaths[entry.path] = true
		}
	}
	for _, entry := range docEntries {
		if entry.writable && !entry.omitted {
			writableAllowlist[entry.path] = true
		}
	}

	return &ConstrainedWorkspace{
		Components:        components,
		ProtectedPaths:    protectedPaths,
		WritableAllowlist: writableAllowlist,
		Caps:              caps,
		AssembledAt:       assembledAt,
	}, nil
}

// strPtr returns a pointer to s (tiny helper for the optional reason fields).
func strPtr(s string) *string { return &s }
