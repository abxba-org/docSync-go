package verifier_test

// Black-box port of the verifier TS test helpers (P4-T02..T09 tests):
// test/helpers/patch-builder.ts + test/helpers/tier1-fixtures.ts.
//
// A DETERMINISTIC DocPatch factory over injected document content, following
// the core hashSpan convention — anchors are content-hash-first
// (claims.HashSpan(delete_text) for the span hash, docutil.ContentHash for
// the base), hunks are located BY LINE TEXT so tests never hand-count lines,
// and the assembled patch re-validates through schemas.DocPatch.Validate
// (the adapter's fail-closed assembly step; identity-fill contract) — plus
// the per-concern document fixtures and the `t1Input` assembler that
// mirrors the application edge (allowlist + target mode + injected clock —
// the application owns permission data, the verifier consumes it).
//
// Test-only helpers — shared by every *_test.go file in this package (Go
// test files in one package share top-level declarations, so the fixtures
// live here instead of in any single suite).

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
)

/* ------------------------------------------------- patch builder (patch-builder.ts) */

// The evidence ref every test hunk carries (machine-resolvable shape).
func t1Evidence() schemas.Evidence {
	return schemas.Evidence{
		Kind: schemas.EvidenceKindSymbolDiff,
		Ref:  "src/payments/refund.ts:3",
		Note: "idempotencyKey became optional for amounts under 10 USD",
	}
}

const (
	t1RunID         = "acme/payments#verify-p4"
	t1PolicyVersion = 17
	t1WriterModel   = "anthropic/claude-sonnet-4.5"
	t1GeneratedAt   = "2026-10-06T12:00:00.000Z"
)

// t1InjectedClock is the fixed injected clock — Tier-1 provenance is caller-owned
// (no hidden Date) (TS: T1_CLOCK).
func t1InjectedClock() string { return "2026-10-06T12:30:00.000Z" }

// t1LineEdit is one line edit: replaces the FIRST occurrence of Line with
// Replacement.
type t1LineEdit struct {
	line        string
	replacement string
	claimIDs    []string
}

// t1LineEdits builds hunks from line edits over baseContent: each edit anchors
// on the FIRST occurrence of its line (unique per fixture docs — duplicated
// lines would make the anchor ambiguous and abstain, which is engine
// behavior covered by patch tests, not a builder feature).
func t1LineEdits(baseContent string, edits []t1LineEdit) []schemas.PatchHunk {
	lines := claims.SplitDocLines(baseContent)
	hunks := make([]schemas.PatchHunk, 0, len(edits))
	for _, edit := range edits {
		lineIndex := -1
		for index, line := range lines {
			if line == edit.line {
				lineIndex = index
				break
			}
		}
		if lineIndex < 0 {
			panic(fmt.Sprintf("patch builder: line not found in base content: %q", edit.line))
		}
		hunk := schemas.PatchHunk{
			Anchor: schemas.PatchAnchor{
				StartLine:   lineIndex + 1,
				EndLine:     lineIndex + 1,
				ContentHash: claims.HashSpan(edit.line),
			},
			DeleteText:   edit.line,
			InsertText:   edit.replacement,
			EvidenceRefs: []schemas.Evidence{t1Evidence()},
		}
		if edit.claimIDs != nil {
			hunk.ClaimIDs = edit.claimIDs
		}
		hunks = append(hunks, hunk)
	}
	return hunks
}

// t1SpanReplace is a deletion hunk over a literal span (anchor = the span's
// FIRST-occurrence location in baseContent), with an optional insertion —
// the writer-shaped "replace this quoted region" hunk.
func t1SpanReplace(baseContent, deleteText, insertText string) schemas.PatchHunk {
	startLine := t1FirstLineOf(baseContent, deleteText)
	return schemas.PatchHunk{
		Anchor: schemas.PatchAnchor{
			StartLine:   startLine,
			EndLine:     startLine + len(claims.SplitDocLines(deleteText)) - 1,
			ContentHash: claims.HashSpan(deleteText),
		},
		DeleteText:   deleteText,
		InsertText:   insertText,
		EvidenceRefs: []schemas.Evidence{t1Evidence()},
	}
}

// t1InsertBefore is a pure-insertion hunk (zero-width anchor at the gap BEFORE
// beforeLine).
func t1InsertBefore(baseContent string, beforeLine int, insertText string) schemas.PatchHunk {
	lineCount := len(claims.SplitDocLines(baseContent))
	if beforeLine < 1 || beforeLine > lineCount+1 {
		panic(fmt.Sprintf("patch builder: insertion point %d outside 1-%d", beforeLine, lineCount+1))
	}
	return schemas.PatchHunk{
		Anchor: schemas.PatchAnchor{
			StartLine:   beforeLine,
			EndLine:     beforeLine,
			ContentHash: claims.HashSpan(""),
		},
		DeleteText:   "",
		InsertText:   insertText,
		EvidenceRefs: []schemas.Evidence{t1Evidence()},
	}
}

// t1BuildPatchOpts mirrors the TS t1BuildPatch() options struct.
type t1BuildPatchOpts struct {
	baseContent string
	hunks       []schemas.PatchHunk
	docPath     string
	runID       string
}

// t1BuildPatch assembles a complete DocPatch (schema-valid; fails closed at
// validation on bad input).
func t1BuildPatch(opts t1BuildPatchOpts) *schemas.DocPatch {
	docPath := opts.docPath
	if docPath == "" {
		docPath = "docs/payments.md"
	}
	runID := opts.runID
	if runID == "" {
		runID = t1RunID
	}
	candidate := &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           runID,
		DocPath:         docPath,
		BaseContentHash: docutil.ContentHash(opts.baseContent),
		Hunks:           opts.hunks,
		EvidenceRefs:    []schemas.Evidence{t1Evidence()},
		Minimality:      schemas.DocPatchMinimality{Measured: false, AddedLines: 0, RemovedLines: 0},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: t1PolicyVersion,
			WriterModel:   t1WriterModel,
			GeneratedAt:   t1GeneratedAt,
		},
	}
	if issues := candidate.Validate(); len(issues) > 0 {
		panic(fmt.Sprintf("patch builder: assembled patch is schema-invalid: %v", issues))
	}
	return candidate
}

// t1FirstLineOf is the 1-based line where a span (newline-joined) first occurs —
// fail loud otherwise.
func t1FirstLineOf(content, span string) int {
	lines := claims.SplitDocLines(content)
	spanLines := claims.SplitDocLines(span)
	for start := 0; start+len(spanLines) <= len(lines); start++ {
		matches := true
		for offset, spanLine := range spanLines {
			if lines[start+offset] != spanLine {
				matches = false
				break
			}
		}
		if matches {
			return start + 1
		}
	}
	panic(fmt.Sprintf("patch builder: span not found in base content: %q…", t1TruncateSpan(span)))
}

// t1TruncateSpan mirrors the TS t1FirstLineOf failure preview (first 60 chars).
func t1TruncateSpan(span string) string {
	if len(span) > 60 {
		return span[:60]
	}
	return span
}

/* --------------------------------------------- tier1 fixtures (tier1-fixtures.ts) */

// t1PaymentsClaimLine is the payments fixture claim line the writer patches
// (pinned by the fixture; fixtures/payments/docs/payments.md line 8).
const t1PaymentsClaimLine = "Refund requests require an `idempotencyKey`."

// t1ProseDocLines is the plain-prose document — no fences, no links, no
// front-matter.
var t1ProseDocLines = []string{
	"# Payments",
	"",
	t1PaymentsClaimLine,
	"Retries are safe within a 24-hour idempotency window.",
	"",
	"## Refunds",
	"",
	"The same idempotency key always returns the same refund result.",
	"",
	"## Captures",
	"",
	"Captures are recorded with the exact minor-unit amount that was approved.",
}

// t1ProseDoc is the joined plain-prose fixture (TS PROSE_DOC).
var t1ProseDoc = strings.Join(t1ProseDocLines, "\n") + "\n"

// t1PaymentsFixtureDoc is the checked-in payments fixture document
// (fixtures/payments/docs/payments.md) — the V-P4-1 golden-patch surface.
var t1PaymentsFixtureDoc = strings.Join([]string{
	"# Payments",
	"",
	"Behavior of the payments service. Mapped by `.docsync/manifest.yml` in `auto`",
	"mode from `src/payments/**` and `src/api/payments/**`.",
	"",
	"## Refunds",
	"",
	t1PaymentsClaimLine,
	"",
	"- The same idempotency key always returns the same refund result; retries never",
	"  create a second refund.",
	"- Retries are safe within a 24-hour idempotency window.",
	"  <!-- DRIFT-PRONE CLAIM (P0-T17): the 24-hour window exists so later-phase tests",
	"       can change refund.ts and observe docs/payments.md becoming stale. -->",
	"- A refund whose `idempotencyKey` is missing is rejected with a validation error",
	"  before any money moves.",
	"",
	"## Captures",
	"",
	"Captures are recorded with the exact minor-unit amount that was approved.",
	"Partial captures are rejected; use refunds for partial returns.",
}, "\n") + "\n"

// t1FenceDocLines is the fence-carrying document — one valid yaml fence + one
// valid json fence.
var t1FenceDocLines = []string{
	"# Payments",
	"",
	t1PaymentsClaimLine,
	"",
	"## Configuration",
	"",
	"```yaml",
	"retries:",
	"  window: 24h",
	"```",
	"",
	"## Examples",
	"",
	"```json",
	`{"amount": 1000}`,
	"```",
	"",
	"Escalation contacts live in docs/oncall.md.",
}

// t1FenceDoc is the joined fence-carrying fixture (TS FENCE_DOC).
var t1FenceDoc = strings.Join(t1FenceDocLines, "\n") + "\n"

// t1RewriteDocLines is a tiny document whose whole body a rewrite patch deletes
// (V-P4-3 bait).
var t1RewriteDocLines = []string{
	"# Payments",
	"Body line 1.",
	"Body line 2.",
	"Body line 3.",
	"Body line 4.",
	"Body line 5.",
	"Body line 6.",
	"Body line 7.",
	"Body line 8.",
	"Body line 9.",
	"Body line 10.",
	"Body line 11.",
}

// t1RewriteDoc is the joined rewrite-bait fixture (TS REWRITE_DOC).
var t1RewriteDoc = strings.Join(t1RewriteDocLines, "\n") + "\n"

// t1LinksDoc builds the link-carrying document; `linksLine` variants swap the
// link targets per case.
func t1LinksDoc(linksLine string) string {
	lines := []string{"# Payments", "", linksLine, "", "## Refunds", "", t1PaymentsClaimLine}
	return strings.Join(lines, "\n") + "\n"
}

// t1ExamplesDocLines is the examples-carrying document: json + yaml + ts + a
// language-less prose fence.
var t1ExamplesDocLines = []string{
	"# Payments",
	"",
	t1PaymentsClaimLine,
	"",
	"```json",
	`{"amount": 1000}`,
	"```",
	"",
	"```yaml",
	"window: 24h",
	"```",
	"",
	"```ts",
	"const x = 1;",
	"```",
	"",
	"A fence with no language tag is prose and ignored:",
	"",
	"```",
	"not code, ignored",
	"```",
}

// t1ExamplesDoc is the joined examples fixture (TS EXAMPLES_DOC).
var t1ExamplesDoc = strings.Join(t1ExamplesDocLines, "\n") + "\n"

// t1KnownDocs is the known-docs snapshot for link-resolution fixtures
// (repo-relative paths).
func t1KnownDocs() map[string]string {
	return map[string]string{
		"docs/refunds.md": "# Refunds\n\n## Refund flow\n\nRefunds are issued once per idempotency key.\n",
		"guide.md":        "# Guide\n\n## Setup\n\nInstall docsync from the workspace.\n",
	}
}

// t1SpecURL is the allowlisted external URL shared by the link fixtures.
const t1SpecURL = "https://example.com/spec"

// t1ExternalAllowlist is a stable external-URL allowlist for the link fixtures.
func t1ExternalAllowlist() map[string]bool {
	return map[string]bool{t1SpecURL: true}
}

// t1LinkInputs is the shared link-check input set (TS LINK_INPUTS).
var t1LinkInputs = verifier.LinkCheckInputs{
	KnownDocs:         t1KnownDocs(),
	ExternalAllowlist: t1ExternalAllowlist(),
}

// t1FixedClaimLine is the writer's corrected claim line.
const t1FixedClaimLine = "Refund requests require an optional `idempotencyKey`."

// t1ClaimFixPatch builds the golden one-line claim fix over docContent.
func t1ClaimFixPatch(docContent string) *schemas.DocPatch {
	return t1BuildPatch(t1BuildPatchOpts{
		baseContent: docContent,
		hunks: t1LineEdits(docContent, []t1LineEdit{
			{line: t1PaymentsClaimLine, replacement: t1FixedClaimLine},
		}),
	})
}

// t1Override carries the optional Tier1Input fields a test overrides (the
// TS `Partial<Tier1Input>` spread). Non-nil fields win over the defaults.
type t1Override struct {
	manifestAllowlist   map[string]bool
	targetMode          *schemas.DocMode
	triggeringDiffStats *patch.DiffStats
	minRatio            *float64
	t1LinkInputs        *verifier.LinkCheckInputs
	exampleChecker      verifier.ExampleCheckerPort
	isRunApplied        patch.IsRunAppliedPort
	baseContent         *string
}

// t1Input assembles a Tier-1 input the way the application edge would.
func t1Input(p *schemas.DocPatch, docContent *string, overrides ...t1Override) verifier.Tier1Input {
	input := verifier.Tier1Input{
		Patch:             p,
		DocContent:        docContent,
		ManifestAllowlist: map[string]bool{"docs/payments.md": true},
		TargetMode:        schemas.DocModeAuto,
		Now:               t1InjectedClock,
	}
	for _, override := range overrides {
		if override.manifestAllowlist != nil {
			input.ManifestAllowlist = override.manifestAllowlist
		}
		if override.targetMode != nil {
			input.TargetMode = *override.targetMode
		}
		if override.triggeringDiffStats != nil {
			input.TriggeringDiffStats = override.triggeringDiffStats
		}
		if override.minRatio != nil {
			input.MinRatio = override.minRatio
		}
		if override.t1LinkInputs != nil {
			input.LinkInputs = override.t1LinkInputs
		}
		if override.exampleChecker != nil {
			input.ExampleChecker = override.exampleChecker
		}
		if override.isRunApplied != nil {
			input.IsRunApplied = override.isRunApplied
		}
		if override.baseContent != nil {
			input.BaseContent = override.baseContent
		}
	}
	return input
}

// t1CheckOrder is the canonical tier-1 check-id order (runTier1 contract).
var t1CheckOrder = []string{
	"patch-applies-cleanly",
	"manifest-allowlist-only",
	"ownership-floor",
	"minimality-bound",
	"whole-document-rewrite",
	"structural-integrity",
	"link-resolution",
	"examples-parse",
}

// t1CheckOf returns the one record for a closed check id (tier 1 always emits
// all eight).
func t1CheckOf(t testing.TB, result verifier.Tier1Result, id string) schemas.VerificationCheck {
	t.Helper()
	for _, check := range result.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("tier-1 result is missing the '%s' check record", id)
	return schemas.VerificationCheck{}
}

// t1CheckIDs lists the emitted check ids in order.
func t1CheckIDs(result verifier.Tier1Result) []string {
	ids := make([]string, 0, len(result.Checks))
	for _, check := range result.Checks {
		ids = append(ids, check.ID)
	}
	return ids
}

// t1FailedIDs lists the failed check ids in emitted order.
func t1FailedIDs(result verifier.Tier1Result) []string {
	ids := make([]string, 0, len(result.Checks))
	for _, check := range result.Checks {
		if check.Status == schemas.CheckFail {
			ids = append(ids, check.ID)
		}
	}
	return ids
}

// t1AssertDetailContains asserts the check detail contains want (a failing or
// skipped record must carry evidence — invariant 5/10).
func t1AssertDetailContains(t testing.TB, check schemas.VerificationCheck, want string) {
	t.Helper()
	if check.Detail == nil {
		t.Fatalf("check '%s' carries no detail (want it to contain %q)", check.ID, want)
	}
	if !strings.Contains(*check.Detail, want) {
		t.Fatalf("check '%s' detail %q does not contain %q", check.ID, *check.Detail, want)
	}
}

// t1Float64p returns a pointer to f (optional float64 fields).
func t1Float64p(f float64) *float64 { return &f }

// t1StrPtr returns a pointer to s (optional string fields).
func t1StrPtr(s string) *string { return &s }

// t1ModePtr returns a pointer to a DocMode (optional mode overrides).
func t1ModePtr(m schemas.DocMode) *schemas.DocMode { return &m }
