package workspace

// Ported from core/src/workspace/render.ts (P3-T02; ARCH §4.7) + injection
// containment (§10.7 conventions, mirrored from @docsync/openrouter).
//
// ═══ UNTRUSTED-CONTENT DIVISION — read before wiring the writer adapter ═══
//
// There are TWO layers of containment and each owns exactly one job so nothing
// is double-wrapped or double-escaped:
//
//  1. THIS render (core): the workspace is plain DATA inside deterministic
//     `<<<docsync-…>>>` frames. It NEVER emits
//     `<untrusted-repository-content>` tags and never adds the containment
//     instruction. Repo-derived free text (git diff, source-file contents,
//     document contents, project instructions) is escape-NEUTRALIZED at
//     assembly time (see EscapeWorkspaceContent): embedded containment tags
//     and docsync frame markers get a backslash after the leading `<` so repo
//     content can neither forge workspace frames nor carry parseable
//     containment markers. Structured components (change_analysis /
//     jev_decision / manifest_policy) render as canonical JSON of
//     schema-validated records whose STRING VALUES are escape-neutralized
//     first via EscapeStructuredComponentValue — model-authored free text
//     inside those records (e.g. the Jev decision's evidence Note) must not
//     be able to forge frames or containment markers either; see the shared
//     drift-guard fixture containment-escape.fixture.json.
//
//  2. The writer ADAPTER (next wave, W6): composes the user message,
//     wrapping the WHOLE rendered workspace string as one
//     untrusted-repository-content block with the containment instruction. It
//     therefore passes repo content as untrustedContent ONCE, at the outer
//     layer only.
//
//     SAFETY against double-escaping: the mirrored escape below is IDEMPOTENT
//     — the backslash-inserted forms `<\untrusted-repository-content`,
//     `<\/untrusted-repository-content>` and `<\<<docsync…` no longer match
//     the escape patterns, so when the adapter's own escape pass runs over
//     the already-neutralized workspace it is a no-op and the final composed
//     bytes are IDENTICAL whether this render escaped first or not (covered
//     by the V-P3-1 determinism/escape tests). W6 must NOT pre-escape or
//     re-wrap anything on top of RenderWorkspace output.
//
// ═══ CROSS-COPY CONSISTENCY NOTE (core ↔ openrouter) ═══
//
// EscapeUntrustedContentTags below mirrors packages/openrouter/src/client.ts
// `escapeContainmentTags` (composeUserMessage UNTRUSTED_CONTENT_* conventions)
// character-for-character, including case-insensitivity and the
// `(?=[\s/>]|$)` opener guard (Go has no lookahead, so the delimiter
// character is captured and re-emitted — the emitted bytes are identical).
// Core depends on @docsync/schemas only and cannot import @docsync/openrouter,
// so the logic is intentionally duplicated; Phase 5 golden run-records + the
// escape tests on both sides catch drift. If you change one, change BOTH and
// note it in SESSIONS.md.
//
// All frames (workspace/section/entry/note) share the `<<<docsync` prefix so a
// single escape rule neutralizes every forgeable frame marker.

import (
	"regexp"
	"strconv"
	"strings"
)

/* ------------------------------------------------------------------ frames */

// Workspace-level frame markers (all frames share the `<<<docsync` prefix).
const (
	WorkspaceOpenFrame  = "<<<docsync-workspace"
	WorkspaceCloseFrame = "<<<docsync-workspace-end>>>"
	SectionOpenFrame    = "<<<docsync-section"
	SectionCloseFrame   = "<<<docsync-section-end"
	EntryOpenFrame      = "<<<docsync-entry"
	EntryCloseFrame     = "<<<docsync-entry-end>>>"
	NoteOpenFrame       = "<<<docsync-note"
	NoteCloseFrame      = "<<<docsync-note-end>>>"
)

// Mirror of openrouter's UNTRUSTED_CONTENT tags (see cross-copy note).
const (
	UntrustedContentOpenTag  = "<untrusted-repository-content>"
	UntrustedContentCloseTag = "</untrusted-repository-content>"
)

// The two containment-tag patterns, mirrored from the TS
// escapeUntrustedContentTags / openrouter escapeContainmentTags (same regexes,
// same insertion rule: a backslash after the leading `<`; idempotent because
// the escaped forms no longer match).
//
// closeTagRE mirrors /<\/untrusted-repository-content>/gi and openTagRE
// mirrors /<untrusted-repository-content(?=[\s/>]|$)/gi. Go's RE2 has no
// lookahead, so the opening-tag guard character is CAPTURED and re-emitted by
// the replacement; the produced bytes are identical to the TS pass, and the
// non-consuming lookahead cannot change the result (a match never overlaps
// the delimiter it guards).
//
// jsSpaceClass spells out the ECMAScript \s class (WhiteSpace + LineTerminator
// — including \v and the Unicode spaces) because Go's \s is ASCII-only.
var (
	closeTagRE = regexp.MustCompile(`(?i)</untrusted-repository-content>`)
	openTagRE  = regexp.MustCompile(`(?i)<untrusted-repository-content([\s/>\x{000b}\x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]|$)`)
	frameRE    = regexp.MustCompile(`<<<docsync`)
)

// EscapeUntrustedContentTags mirrors @docsync/openrouter escapeContainmentTags
// (client.ts) — same regexes, same insertion rule (backslash after the leading
// `<`), idempotent. See the cross-copy consistency note in the file header.
// DRIFT GUARD: pinned to the shared committed fixture
// packages/schemas/test/fixtures/containment-escape.fixture.json (both suites
// assert identical outputs for the same inputs).
func EscapeUntrustedContentTags(content string) string {
	content = closeTagRE.ReplaceAllString(content, `<\/untrusted-repository-content>`)
	return openTagRE.ReplaceAllString(content, `<\untrusted-repository-content$1`)
}

// EscapeWorkspaceContent neutralizes, in repo-derived content: (1) the
// mirrored containment tags and (2) every docsync frame marker. Idempotent
// (escaped forms never re-match). DRIFT GUARD: pinned to the shared committed
// fixture packages/schemas/test/fixtures/containment-escape.fixture.json
// together with @docsync/openrouter's escapeContainmentTags (both suites
// assert identical outputs for the same inputs).
func EscapeWorkspaceContent(content string) string {
	return frameRE.ReplaceAllString(EscapeUntrustedContentTags(content), `<\<<docsync`)
}

// EscapeStructuredComponentValue deep-walks a structured record (plain
// objects/arrays/scalars) and escape-neutralizes every STRING value with
// EscapeWorkspaceContent BEFORE canonical-JSON serialization (see the
// untrusted-content division note). Rationale: structured components
// (change_analysis / jev_decision / manifest_policy) are schema-validated, but
// their free-text fields — the Jev decision's evidence note is
// model-authored — are still untrusted DATA; a hostile note must not carry
// live containment tags or docsync frame markers into the rendered JSON.
// Idempotence is preserved: EscapeWorkspaceContent is idempotent, so
// re-running the neutralization (or routing the rendered JSON through the
// adapter's escape pass) changes nothing. Non-JSON leaves are left untouched
// — the canonical serializer rejects them fail-closed at assembly.
//
// Operates on the generic tree (map[string]any / []any / string / bool / nil /
// json.Number) the assembler decodes the component into.
func EscapeStructuredComponentValue(value any) any {
	switch v := value.(type) {
	case string:
		return EscapeWorkspaceContent(v)
	case []any:
		escaped := make([]any, len(v))
		for i, item := range v {
			escaped[i] = EscapeStructuredComponentValue(item)
		}
		return escaped
	case map[string]any:
		escaped := make(map[string]any, len(v))
		for key, item := range v {
			escaped[key] = EscapeStructuredComponentValue(item)
		}
		return escaped
	default:
		return value
	}
}

/* ------------------------------------------------------------- block render */

// kvBlock renders one deterministic key/value header line block shared by
// entries and notes (TS kvBlock).
func kvBlock(open string, fields []string, close string) string {
	lines := make([]string, 0, len(fields)+2)
	lines = append(lines, open)
	lines = append(lines, fields...)
	lines = append(lines, close)
	return strings.Join(lines, "\n")
}

// RenderWorkspaceEntry renders one admitted (or omitted) source/document
// entry: a key/value header frame and the escaped content (empty when the
// entry was omitted by a cap). No trailing newline — callers join entries
// with '\n'. `readonly`/`reason` are the per-path markers lock layer 2
// consumes (P3-T04). Deterministic.
func RenderWorkspaceEntry(fields []string, content string) string {
	return kvBlock(EntryOpenFrame, fields, EntryCloseFrame) + "\n" + content
}

// RenderWorkspaceNote renders a system note inside a section: empty/none
// markers, cap-binding reports. No trailing newline — callers join with '\n'.
// Every note is escape-protected by the shared frame prefix. Deterministic.
func RenderWorkspaceNote(fields []string) string {
	return kvBlock(NoteOpenFrame, fields, NoteCloseFrame)
}

/* ------------------------------------------------------------- full render */

// RenderWorkspace renders the byte-deterministic plain-text workspace body the
// writer adapter passes as untrustedContent (see the division note in the file
// header — this function adds FRAMING only; section bodies are final at
// assembly time).
//
// Canonical component ordering = WorkspaceComponents order. Batch context
// (orchestrator-attached, P3-T03) renders as a note right after the open frame
// when present. Truncated/empty/none components render an explicit marker
// block instead of a body — the workspace never silently drops anything (§3.7).
func RenderWorkspace(ws *ConstrainedWorkspace) string {
	lines := []string{WorkspaceOpenFrame + " v1 assembledAt=" + ws.AssembledAt + ">>>"}

	if ws.Batch != nil {
		lines = append(lines, RenderWorkspaceNote([]string{
			"kind: batch-context",
			"session: " + strconv.Itoa(ws.Batch.SessionIndex) + "/" + strconv.Itoa(ws.Batch.SessionCount),
			"docs: " + strings.Join(ws.Batch.Docs, ", "),
			"reason: " + ws.Batch.Reason,
		}))
	}

	for _, name := range WorkspaceComponents {
		section := ws.Components[name]
		lines = append(lines, SectionOpenFrame+" "+string(name)+">>>")
		if section.Truncated {
			reason := ""
			if section.TruncationReason != nil {
				reason = *section.TruncationReason
			}
			lines = append(lines, RenderWorkspaceNote([]string{
				"kind: truncation-report",
				"component: " + string(name),
				"reason: " + reason,
			}))
		}
		if section.Status == StatusContent {
			lines = append(lines, section.Body)
		} else {
			lines = append(lines, RenderWorkspaceNote([]string{
				"status: " + string(section.Status),
				"reason: " + section.Reason,
			}))
		}
		lines = append(lines, SectionCloseFrame+" "+string(name)+">>>")
	}

	lines = append(lines, WorkspaceCloseFrame)
	return strings.Join(lines, "\n")
}
