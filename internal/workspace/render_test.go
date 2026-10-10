package workspace_test

// Ported from packages/core/test/patch-integration.test.ts — the V-P3-1 gate
// (seven-component workspace assembly + the deterministic render golden) and
// the render.ts surface (frames, containment escape, marker blocks).
//
// The TS gate assembles the workspace over a REAL temp git repository (manifest
// allowlist through the fail-closed loader, real documents via GitClient)
// and pins the render with test/goldens/patch-integration-workspace.golden.json
// (expectGolden compares canonicalJson(actual) with
// canonicalJson(JSON.parse(file))). Both are mirrored here: the fixture repo is
// built in-process with real git, and the golden document is carried as a Go
// literal, canonicalized on BOTH sides exactly as expectGolden did.
//
// The TS `no-network proof` (no network/adapter imports under src/workspace)
// has no Go equivalent to port — this package imports no net-capable surface at
// all, which `go list -deps` verifies below.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/validate"
	"github.com/DUDE616/docSync-go/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------- render primitives */

func TestRenderWorkspace_FrameConstantsAreSharedAndStable(t *testing.T) {
	// All frames share the `<<<docsync` prefix so a single escape rule
	// neutralizes every forgeable frame marker (render.ts header contract).
	assert.Equal(t, "<<<docsync-workspace", workspace.WorkspaceOpenFrame)
	assert.Equal(t, "<<<docsync-workspace-end>>>", workspace.WorkspaceCloseFrame)
	assert.Equal(t, "<<<docsync-section", workspace.SectionOpenFrame)
	assert.Equal(t, "<<<docsync-section-end", workspace.SectionCloseFrame)
	assert.Equal(t, "<<<docsync-entry", workspace.EntryOpenFrame)
	assert.Equal(t, "<<<docsync-entry-end>>>", workspace.EntryCloseFrame)
	assert.Equal(t, "<<<docsync-note", workspace.NoteOpenFrame)
	assert.Equal(t, "<<<docsync-note-end>>>", workspace.NoteCloseFrame)
	assert.Equal(t, "<untrusted-repository-content>", workspace.UntrustedContentOpenTag)
	assert.Equal(t, "</untrusted-repository-content>", workspace.UntrustedContentCloseTag)
	for _, frame := range []string{
		workspace.WorkspaceOpenFrame, workspace.SectionOpenFrame,
		workspace.EntryOpenFrame, workspace.NoteOpenFrame,
	} {
		assert.True(t, strings.HasPrefix(frame, "<<<docsync"), frame)
	}
}

func TestRenderWorkspaceEntry_RendersHeaderFrameThenContentNoTrailingNewline(t *testing.T) {
	rendered := workspace.RenderWorkspaceEntry(
		[]string{"kind: document", "path: docs/a.md", "mode: auto", "writable: true", "readonly: false"},
		"body")
	assert.Equal(t, strings.Join([]string{
		"<<<docsync-entry",
		"kind: document",
		"path: docs/a.md",
		"mode: auto",
		"writable: true",
		"readonly: false",
		"<<<docsync-entry-end>>>",
		"body",
	}, "\n"), rendered)
	assert.False(t, strings.HasSuffix(rendered, "\n"))
}

func TestRenderWorkspaceNote_RendersKeyValueBlockNoTrailingNewline(t *testing.T) {
	rendered := workspace.RenderWorkspaceNote([]string{"status: empty", "reason: provided but empty — no diff content"})
	assert.Equal(t, strings.Join([]string{
		"<<<docsync-note",
		"status: empty",
		"reason: provided but empty — no diff content",
		"<<<docsync-note-end>>>",
	}, "\n"), rendered)
	assert.False(t, strings.HasSuffix(rendered, "\n"))
}

func TestRenderWorkspace_MarkerBlocksForEmptyAndNoneComponents(t *testing.T) {
	ws := requireAssembled(t, func() workspace.AssembleWorkspaceInput {
		input := assembleBaseInput()
		input.GitDiff = strPtr("")
		input.Decision = nil
		input.Instructions = strPtr("")
		input.Documents = []workspace.WorkspaceDocument{}
		return input
	}())
	rendered := workspace.RenderWorkspace(ws)

	assert.Equal(t, workspace.StatusEmpty, ws.Components[workspace.ComponentGitDiff].Status)
	assert.Equal(t, workspace.StatusNone, ws.Components[workspace.ComponentJevDecision].Status)
	assert.Equal(t, workspace.StatusEmpty, ws.Components[workspace.ComponentProjectInstruction].Status)
	assert.Equal(t, workspace.StatusEmpty, ws.Components[workspace.ComponentRelevantDocuments].Status)

	// Explicit marker blocks render instead of silent drops (§3.7).
	assert.Contains(t, rendered, "status: empty")
	assert.Contains(t, rendered, "status: none")
	assert.Contains(t, rendered, "reason: provided but empty — no diff content")
	assert.Contains(t, rendered, "reason: no Jev decision supplied — decision is a nullable input (analysis-only session)")
	assert.Contains(t, rendered, "reason: provided but empty — no documents supplied")
	// Every component is still framed: the workspace never drops a section.
	for _, name := range workspace.WorkspaceComponents {
		assert.Contains(t, rendered, "<<<docsync-section "+string(name)+">>>")
		assert.Contains(t, rendered, "<<<docsync-section-end "+string(name)+">>>")
	}
}

func TestRenderWorkspace_TruncationReportAndBatchContextNotes(t *testing.T) {
	ws := requireAssembled(t, func() workspace.AssembleWorkspaceInput {
		input := assembleBaseInput()
		input.Caps = &workspace.WorkspaceCapsOverrides{MaxDocs: intPtr(0), MaxSourceFiles: intPtr(0)}
		return input
	}())
	// The assembler never attaches batch context (P3-T03) — the orchestrator does.
	require.Nil(t, ws.Batch)
	ws.Batch = &workspace.BatchInfo{
		SessionIndex: 2,
		SessionCount: 3,
		Docs:         []string{"docs/payments.md", "docs/api.md"},
		Reason:       "shared concept(s): refunds",
	}

	rendered := workspace.RenderWorkspace(ws)
	lines := strings.Split(rendered, "\n")

	// The batch note renders right after the workspace open frame.
	assert.Equal(t, workspace.WorkspaceOpenFrame+" v1 assembledAt="+ws.AssembledAt+">>>", lines[0])
	assert.Equal(t, "<<<docsync-note", lines[1])
	assert.Equal(t, "kind: batch-context", lines[2])
	assert.Equal(t, "session: 2/3", lines[3])
	assert.Equal(t, "docs: docs/payments.md, docs/api.md", lines[4])
	assert.Equal(t, "reason: shared concept(s): refunds", lines[5])
	assert.Equal(t, "<<<docsync-note-end>>>", lines[6])

	// Truncated components carry an explicit truncation-report note naming the
	// component and the deterministic reason (§3.7 — report, never drop).
	sourceSection := ws.Components[workspace.ComponentSourceFiles]
	require.True(t, sourceSection.Truncated)
	require.NotNil(t, sourceSection.TruncationReason)
	assert.Contains(t, rendered, "kind: truncation-report")
	assert.Contains(t, rendered, "component: source_files")
	assert.Contains(t, rendered, "reason: "+*sourceSection.TruncationReason)
}

func TestRenderWorkspace_IsByteDeterministicAcrossAssemblies(t *testing.T) {
	first := requireAssembled(t, assembleBaseInput())
	second := requireAssembled(t, assembleBaseInput())
	third := requireAssembled(t, assembleBaseInput())
	assert.Equal(t, workspace.RenderWorkspace(first), workspace.RenderWorkspace(second))
	assert.Equal(t, workspace.RenderWorkspace(first), workspace.RenderWorkspace(third))

	// The workspace opens with the injected clock value, never a local one.
	assert.Contains(t, workspace.RenderWorkspace(first), "assembledAt="+assembleClock+">>>")
	assert.Equal(t, assembleClock, first.AssembledAt)
}

/* --------------------------------------------------- the seven parts (order) */

func TestRenderWorkspace_CanonicalComponentOrder(t *testing.T) {
	ws := requireAssembled(t, assembleBaseInput())
	rendered := workspace.RenderWorkspace(ws)

	order := make([]string, 0, len(workspace.WorkspaceComponents))
	for _, name := range workspace.WorkspaceComponents {
		order = append(order, string(name))
	}
	assert.Equal(t, []string{
		"git_diff", "change_analysis", "jev_decision", "manifest_policy",
		"source_files", "relevant_documents", "project_instructions",
	}, order)

	// The render visits the components in exactly that canonical order.
	cursor := 0
	for _, name := range order {
		openIndex := strings.Index(rendered[cursor:], "<<<docsync-section "+name+">>>")
		require.GreaterOrEqual(t, openIndex, 0, "component %s must be present", name)
		closeIndex := strings.Index(rendered[cursor:], "<<<docsync-section-end "+name+">>>")
		require.GreaterOrEqual(t, closeIndex, 0, "component %s must close", name)
		require.Less(t, openIndex, closeIndex)
		cursor += openIndex + 1
	}
	assert.True(t, strings.HasSuffix(rendered, workspace.WorkspaceCloseFrame))
}

/* ------------------------------------------------------------ golden fixture */

const (
	goldenClock         = "2026-10-06T11:15:00.000Z"
	goldenPolicySummary = "policy v17: update >= 0.94, review >= 0.72, abstain below (calibrated data)"
	goldenInstructions  = "Fix documentation drift with minimal, evidence-backed patches."
)

var (
	goldenPaymentsLines = []string{
		"# Payments",
		"",
		"Refunds require an idempotency key.",
		"Amounts above 10 USD are held for review.",
		"The review window is 48 hours.",
		"",
		"## Flow",
		"",
		"Refunds flow through docs/architecture.md for the full diagram.",
		"The review window is 48 hours.",
		"",
		"## Disputes",
		"",
		"Dispute handling follows docs/disputes.md.",
		"Escalation contacts live in docs/oncall.md.",
		"",
		"## Evidence",
		"",
		"Idempotency keys are scoped per run in src/payments/refund.ts.",
		"Amounts are compared against the triggering code diff.",
		"The reviewer checks evidence refs before approval.",
		"Patches must remain minimal; whole-document rewrites are rejected.",
		"Evidence refs are machine-resolvable anchors, never free text.",
		"Claim values must stay consistent across patches in one run.",
		"The policy engine gates automation on calibrated thresholds.",
		"Uncertain routes abstain to REVIEW instead of guessing.",
		"Generated documents are excluded from patch candidates.",
		"Locked documents stay read-only at any confidence.",
		"Human-authored documents route through human review.",
		"Anchors are content hashes; line numbers are only locators.",
		"A duplicated identical span is ambiguous and must abstain.",
		"Races abstain unless a re-verified merge passes.",
		"Application is deterministic and pure over content.",
	}
	goldenPaymentsDoc = strings.Join(goldenPaymentsLines, "\n") + "\n"

	goldenHandbookDoc = strings.Join([]string{
		"# Team Handbook",
		"",
		"This handbook is human-authored; every patch routes through human review.",
		"",
		"## Voice",
		"",
		"Write plainly and prefer active voice with concrete examples.",
		"Jargon is defined on first use; acronyms expand on first mention.",
		"Sentences stay short; paragraphs carry one idea each.",
		"",
		"## Reviews",
		"",
		"Documentation changes are reviewed by the docs owners before merge.",
		"Automated proposals arrive as suggestions, never as direct edits.",
		"Reviewers check evidence refs and minimality before approving.",
		"Disagreements are recorded on the proposal, not in the document.",
		"",
		"## Ownership",
		"",
		"Each document has a named owner recorded in the docs index.",
		"Ownership transfers go through a pull request, never silently.",
		"Orphaned documents are re-owned within one sprint.",
		"",
		"## Changes",
		"",
		"Every change carries its run id and provenance record.",
		"Reverted changes keep their history for audit purposes.",
	}, "\n") + "\n"

	goldenLockedDoc = strings.Join([]string{
		"# Locked Document",
		"",
		"This document is locked: read-only at any confidence (D2 floor).",
		"",
		"## Compliance Statement",
		"",
		"The compliance text is hand-curated and mirrored from the policy office.",
		"No automated process may modify this file under any circumstance.",
		"Drift detected here is reported to the compliance owner as a finding.",
		"The compliance owner applies corrections through a reviewed manual edit.",
		"Every drift report carries the triggering commit and evidence refs.",
		"External auditors receive this file verbatim on request.",
		"Questions about this text go to the compliance office.",
		"",
		"## History",
		"",
		"2026-09: wording aligned with the updated compliance handbook.",
		"2026-06: initial compliance statement recorded.",
		"Each history line is signed off by the compliance owner.",
		"Lock mode exists precisely so this history cannot be rewritten.",
		"A patch touching this file is refused whole at the application layer.",
		"The refusal names the ownership floor, never a partial application.",
		"Protected paths are ownership floors, not confidence gates.",
		"Locked resolves blocked at ANY confidence by design.",
		"Generated documents are a separate floor for a different reason.",
		"The AI never fights another generator in this repository.",
	}, "\n") + "\n"

	goldenRefundSource = strings.Join([]string{
		"export interface RefundRequest {",
		"  readonly amount: number;",
		"  readonly idempotencyKey: string;",
		"}",
		"",
		"export function refund(request: RefundRequest): string {",
		"  if (request.amount <= 0) {",
		"    throw new Error('amount must be positive');",
		"  }",
		"  return `refunded:${request.amount}:${request.idempotencyKey}`;",
		"}",
	}, "\n") + "\n"

	goldenRefundChanged = strings.Replace(
		strings.Replace(goldenRefundSource, "  readonly idempotencyKey: string;", "  readonly idempotencyKey?: string;", 1),
		"  return `refunded:${request.amount}:${request.idempotencyKey}`;",
		"  return `refunded:${request.amount}:${request.idempotencyKey ?? 'auto'}`;", 1,
	)

	goldenManifestYAML = strings.Join([]string{
		"version: 1",
		"documents:",
		"  - path: docs/payments.md",
		"    mode: auto",
		"    sources:",
		"      - src/payments/**",
		"  - path: docs/handbook.md",
		"    mode: human",
		"    sources:",
		"      - src/handbook/**",
		"  - path: docs/locked.md",
		"    mode: locked",
		"    sources:",
		"      - src/auth/**",
	}, "\n") + "\n"
)

// goldenWorkspaceFixture is the assembled workspace over the real temp repo.
type goldenWorkspaceFixture struct {
	root      string
	client    *gitx.GitClient
	baseRev   string
	rev       string
	gitDiff   string
	manifest  *schemas.Manifest
	payments  string
	handbook  string
	locked    string
	refundSrc string
}

func goldenGitIn(t *testing.T, root string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-C", root,
		"-c", "user.name=docsync-test",
		"-c", "user.email=docsync-test@invalid",
	}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("git %v failed: %v\nstderr: %s", args, err, exitErr.Stderr)
		}
		t.Fatalf("git %v failed: %v", args, err)
	}
	return string(out)
}

func goldenWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	target := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func goldenCommitAll(t *testing.T, root, message string) string {
	t.Helper()
	goldenGitIn(t, root, "add", "-A")
	goldenGitIn(t, root, "commit", "-q", "-m", message)
	return strings.TrimSpace(goldenGitIn(t, root, "rev-parse", "HEAD"))
}

// newGoldenWorkspaceFixture builds the temp git repo and assembles the
// workspace over it (port of the TS buildFixture's workspace half).
func newGoldenWorkspaceFixture(t *testing.T) (*goldenWorkspaceFixture, workspace.AssembleWorkspaceInput) {
	t.Helper()
	root := t.TempDir()
	goldenGitIn(t, root, "init", "-q", "-b", "main")
	// Repo-local line-ending policy (mirror git-client.test.ts): pin LF before
	// any file is written so the diff never sees CRLF.
	goldenGitIn(t, root, "config", "core.autocrlf", "false")
	goldenGitIn(t, root, "config", "core.eol", "lf")
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)

	goldenWrite(t, root, ".docsync/manifest.yml", goldenManifestYAML)
	goldenWrite(t, root, ".docsync/state/audit-cursor", "0\n")
	goldenWrite(t, root, "docs/payments.md", goldenPaymentsDoc)
	goldenWrite(t, root, "docs/handbook.md", goldenHandbookDoc)
	goldenWrite(t, root, "docs/locked.md", goldenLockedDoc)
	goldenWrite(t, root, "src/payments/refund.ts", goldenRefundSource)
	baseRev := goldenCommitAll(t, root, "docs: initial payments docs, manifest, state")

	goldenWrite(t, root, "src/payments/refund.ts", goldenRefundChanged)
	rev := goldenCommitAll(t, root, "feat(payments): make the refund idempotency key optional")

	gitDiff := goldenGitIn(t, root, "diff", baseRev, rev)

	// The on-disk manifest through the real fail-closed loader (P0-T08 surface).
	loaded, err := validate.LoadAndValidateManifest(root, validate.ManifestValidateOptions{})
	require.NoError(t, err)
	require.Empty(t, loaded.Diagnostics)
	require.NotNil(t, loaded.Manifest)

	ctx := context.Background()
	read := func(path string) string {
		blob, err := client.CatFile(ctx, rev, path)
		require.NoError(t, err)
		return blob.Content
	}

	fixture := &goldenWorkspaceFixture{
		root:      root,
		client:    client,
		baseRev:   baseRev,
		rev:       rev,
		gitDiff:   gitDiff,
		manifest:  loaded.Manifest,
		payments:  read("docs/payments.md"),
		handbook:  read("docs/handbook.md"),
		locked:    read("docs/locked.md"),
		refundSrc: read("src/payments/refund.ts"),
	}
	require.Equal(t, goldenPaymentsDoc, fixture.payments) // the git read matches the fixture
	require.Equal(t, goldenRefundChanged, fixture.refundSrc)

	analysis := schemas.ChangeAnalysisExample
	decision := schemas.DecisionExample
	input := workspace.AssembleWorkspaceInput{
		GitDiff:        strPtr(fixture.gitDiff),
		ChangeAnalysis: &analysis,
		Decision:       &decision,
		Manifest:       fixture.manifest,
		PolicySummary:  strPtr(goldenPolicySummary),
		SourceFiles:    []workspace.WorkspaceSourceFile{{Path: "src/payments/refund.ts", Content: fixture.refundSrc}},
		Documents: []workspace.WorkspaceDocument{
			{Path: "docs/payments.md", Content: fixture.payments},
			{Path: "docs/handbook.md", Content: fixture.handbook},
			{Path: "docs/locked.md", Content: fixture.locked},
		},
		Instructions: strPtr(goldenInstructions),
		Clock:        func() string { return goldenClock },
	}
	return fixture, input
}

// goldenWorkspaceDocument mirrors the TS golden's JSON shape
// (test/goldens/patch-integration-workspace.golden.json).
type goldenWorkspaceDocument struct {
	AssembledAt       string   `json:"assembledAt"`
	ProtectedPaths    []string `json:"protectedPaths"`
	Rendered          string   `json:"rendered"`
	WritableAllowlist []string `json:"writableAllowlist"`
}

// wantGoldenWorkspace is the checked-in TS golden carried as a Go literal
// (patch-integration-workspace.golden.json).
var wantGoldenWorkspace = goldenWorkspaceDocument{
	AssembledAt:    "2026-10-06T11:15:00.000Z",
	ProtectedPaths: []string{"docs/locked.md", "src/payments/refund.ts"},
	Rendered: "<<<docsync-workspace v1 assembledAt=2026-10-06T11:15:00.000Z>>>\n<<<docsync-section git_" +
		"diff>>>\ndiff --git a/src/payments/refund.ts b/src/payments/refund.ts\nindex 0e4e961..66" +
		"5b6e2 100644\n--- a/src/payments/refund.ts\n+++ b/src/payments/refund.ts\n@@ -1,11 +1,11" +
		" @@\n export interface RefundRequest {\n   readonly amount: number;\n-  readonly idempot" +
		"encyKey: string;\n+  readonly idempotencyKey?: string;\n }\n \n export function refund(r" +
		"equest: RefundRequest): string {\n   if (request.amount <= 0) {\n     throw new Error('a" +
		"mount must be positive');\n   }\n-  return `refunded:${request.amount}:${request.idempot" +
		"encyKey}`;\n+  return `refunded:${request.amount}:${request.idempotencyKey ?? 'auto'}`;" +
		"\n }\n\n<<<docsync-section-end git_diff>>>\n<<<docsync-section change_analysis>>>\n{\"an" +
		"alyzer_coverage\":[{\"analyzer\":\"typescript\",\"note\":\"stub: no AST/symbol extractio" +
		"n in V1 — tree-sitter symbol/type-level analysis lands in Phase 11 (F11-1); per-file sym" +
		"bol diffs are a declared V1 coverage gap\",\"status\":\"stub\"},{\"analyzer\":\"generic" +
		"\",\"status\":\"ok\"}],\"base_sha\":\"8a73f2c\",\"classification_hints\":[{\"kind\":\"te" +
		"st_only\",\"note\":\"all changed paths match src/**/*.test.ts\"}],\"deep_analysis\":{\"r" +
		"eason\":\"no type-aware analyzer coverage for this language\",\"status\":\"skipped\"},\"" +
		"dependencies_changed\":false,\"files\":[{\"api_diff\":false,\"config_diff\":false,\"form" +
		"atting_only\":false,\"generated\":false,\"path\":\"src/payments/refund.ts\",\"status\":" +
		"\"modified\",\"symbols_changed\":true,\"symbols_detail\":{\"added\":[],\"changed\":[{\"c" +
		"hangeKinds\":[\"type\"],\"name\":\"idempotencyKey\"}],\"removed\":[]},\"test_only\":fals" +
		"e},{\"path\":\"src/payments/refund.test.ts\",\"status\":\"added\",\"test_only\":true}]," +
		"\"head_sha\":\"81af92e\",\"repo\":\"acme/payments\",\"run_id\":\"acme/payments#8a73f2c.." +
		"81af92e\",\"schema\":\"docsync.change-analysis/v1\"}\n<<<docsync-section-end change_anal" +
		"ysis>>>\n<<<docsync-section jev_decision>>>\n{\"abstain\":false,\"abstain_reason\":null," +
		"\"action\":\"review\",\"answers\":{\"affected_doc_count\":{\"probability\":0.91,\"value" +
		"\":2},\"api_contract_changed\":{\"probability\":0.12,\"value\":false},\"public_behavior_" +
		"changed\":{\"probability\":0.87,\"value\":true}},\"candidates\":[\"docs/payments.md\",\"" +
		"docs/api.md\"],\"change\":{\"change_analysis_ref\":\"analysis/01J9ZK6X8A\",\"commit\":\"" +
		"81af92e\",\"diff_ref\":\"git:81af92e\"},\"change_kind\":\"behavioral\",\"confidence\":0." +
		"91,\"decision_id\":\"d_01J9ZK7Q2M\",\"documentation_impact\":\"possible\",\"evidence\":[" +
		"{\"kind\":\"symbol_diff\",\"note\":\"idempotencyKey became optional\",\"ref\":\"src/paym" +
		"ents/refund.ts:42\"}],\"policy_version\":17,\"risk\":\"medium\",\"schema\":\"docsync.dec" +
		"ision/v1\",\"trace\":{\"latency_ms\":740,\"model\":\"jev\",\"policy_hash\":\"sha256:e3b0" +
		"c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\"}}\n<<<docsync-section-end" +
		" jev_decision>>>\n<<<docsync-section manifest_policy>>>\n[manifest]\n{\"documents\":[{\"" +
		"mode\":\"auto\",\"path\":\"docs/payments.md\",\"sources\":[\"src/payments/**\"]},{\"mode" +
		"\":\"human\",\"path\":\"docs/handbook.md\",\"sources\":[\"src/handbook/**\"]},{\"mode\":" +
		"\"locked\",\"path\":\"docs/locked.md\",\"sources\":[\"src/auth/**\"]}],\"version\":1}\n[" +
		"policy]\npolicy v17: update >= 0.94, review >= 0.72, abstain below (calibrated data)\n<<" +
		"<docsync-section-end manifest_policy>>>\n<<<docsync-section source_files>>>\n<<<docsync-" +
		"entry\nkind: source-file\npath: src/payments/refund.ts\nwritable: false\nreadonly: true" +
		"\nreason: source file — analysis only: code files are never modified (invariant 2)\n<<<d" +
		"ocsync-entry-end>>>\nexport interface RefundRequest {\n  readonly amount: number;\n  rea" +
		"donly idempotencyKey?: string;\n}\n\nexport function refund(request: RefundRequest): str" +
		"ing {\n  if (request.amount <= 0) {\n    throw new Error('amount must be positive');\n  " +
		"}\n  return `refunded:${request.amount}:${request.idempotencyKey ?? 'auto'}`;\n}\n\n<<<d" +
		"ocsync-section-end source_files>>>\n<<<docsync-section relevant_documents>>>\n<<<docsync" +
		"-entry\nkind: document\npath: docs/handbook.md\nmode: human\nwritable: true\nreadonly: f" +
		"alse\nreason: manifest mode 'human' — review-gated patch-eligible (ARCH §11.2): patches " +
		"route through human review (policy outcome suggest/review), never auto-apply\n<<<docsync" +
		"-entry-end>>>\n# Team Handbook\n\nThis handbook is human-authored; every patch routes th" +
		"rough human review.\n\n## Voice\n\nWrite plainly and prefer active voice with concrete e" +
		"xamples.\nJargon is defined on first use; acronyms expand on first mention.\nSentences s" +
		"tay short; paragraphs carry one idea each.\n\n## Reviews\n\nDocumentation changes are re" +
		"viewed by the docs owners before merge.\nAutomated proposals arrive as suggestions, neve" +
		"r as direct edits.\nReviewers check evidence refs and minimality before approving.\nDisa" +
		"greements are recorded on the proposal, not in the document.\n\n## Ownership\n\nEach doc" +
		"ument has a named owner recorded in the docs index.\nOwnership transfers go through a pu" +
		"ll request, never silently.\nOrphaned documents are re-owned within one sprint.\n\n## Ch" +
		"anges\n\nEvery change carries its run id and provenance record.\nReverted changes keep t" +
		"heir history for audit purposes.\n\n<<<docsync-entry\nkind: document\npath: docs/locked." +
		"md\nmode: locked\nwritable: false\nreadonly: true\nreason: manifest mode 'locked' — term" +
		"inal D2 floor: read-only at any confidence, patch emission forbidden\n<<<docsync-entry-e" +
		"nd>>>\n# Locked Document\n\nThis document is locked: read-only at any confidence (D2 flo" +
		"or).\n\n## Compliance Statement\n\nThe compliance text is hand-curated and mirrored from" +
		" the policy office.\nNo automated process may modify this file under any circumstance.\n" +
		"Drift detected here is reported to the compliance owner as a finding.\nThe compliance ow" +
		"ner applies corrections through a reviewed manual edit.\nEvery drift report carries the " +
		"triggering commit and evidence refs.\nExternal auditors receive this file verbatim on re" +
		"quest.\nQuestions about this text go to the compliance office.\n\n## History\n\n2026-09:" +
		" wording aligned with the updated compliance handbook.\n2026-06: initial compliance stat" +
		"ement recorded.\nEach history line is signed off by the compliance owner.\nLock mode exi" +
		"sts precisely so this history cannot be rewritten.\nA patch touching this file is refuse" +
		"d whole at the application layer.\nThe refusal names the ownership floor, never a partia" +
		"l application.\nProtected paths are ownership floors, not confidence gates.\nLocked reso" +
		"lves blocked at ANY confidence by design.\nGenerated documents are a separate floor for " +
		"a different reason.\nThe AI never fights another generator in this repository.\n\n<<<doc" +
		"sync-entry\nkind: document\npath: docs/payments.md\nmode: auto\nwritable: true\nreadonly" +
		": false\nreason: manifest mode 'auto' — patch emission permitted (verification and the p" +
		"olicy engine still gate it)\n<<<docsync-entry-end>>>\n# Payments\n\nRefunds require an i" +
		"dempotency key.\nAmounts above 10 USD are held for review.\nThe review window is 48 hour" +
		"s.\n\n## Flow\n\nRefunds flow through docs/architecture.md for the full diagram.\nThe re" +
		"view window is 48 hours.\n\n## Disputes\n\nDispute handling follows docs/disputes.md.\nE" +
		"scalation contacts live in docs/oncall.md.\n\n## Evidence\n\nIdempotency keys are scoped" +
		" per run in src/payments/refund.ts.\nAmounts are compared against the triggering code di" +
		"ff.\nThe reviewer checks evidence refs before approval.\nPatches must remain minimal; wh" +
		"ole-document rewrites are rejected.\nEvidence refs are machine-resolvable anchors, never" +
		" free text.\nClaim values must stay consistent across patches in one run.\nThe policy en" +
		"gine gates automation on calibrated thresholds.\nUncertain routes abstain to REVIEW inst" +
		"ead of guessing.\nGenerated documents are excluded from patch candidates.\nLocked docume" +
		"nts stay read-only at any confidence.\nHuman-authored documents route through human revi" +
		"ew.\nAnchors are content hashes; line numbers are only locators.\nA duplicated identical" +
		" span is ambiguous and must abstain.\nRaces abstain unless a re-verified merge passes.\n" +
		"Application is deterministic and pure over content.\n\n<<<docsync-section-end relevant_d" +
		"ocuments>>>\n<<<docsync-section project_instructions>>>\nFix documentation drift with mi" +
		"nimal, evidence-backed patches.\n<<<docsync-section-end project_instructions>>>\n<<<docs" +
		"ync-workspace-end>>>",
	WritableAllowlist: []string{"docs/handbook.md", "docs/payments.md"},
}

func TestRenderWorkspace_VP3_1_SevenComponentsAndDeterministicRenderGolden(t *testing.T) {
	_, input := newGoldenWorkspaceFixture(t)
	ws := requireAssembled(t, input)

	// Exactly the seven components — the closed record carries no other keys,
	// and every one assembled as content (all inputs present and non-empty).
	want := make([]string, 0, len(workspace.WorkspaceComponents))
	for _, name := range workspace.WorkspaceComponents {
		want = append(want, string(name))
	}
	sort.Strings(want)
	assert.Equal(t, want, sortedComponentNames(t, ws))
	for _, name := range workspace.WorkspaceComponents {
		assert.Equal(t, workspace.StatusContent, ws.Components[name].Status, "component %s", name)
		assert.Equal(t, "", ws.Components[name].Reason, "component %s", name)
	}

	// D2 post-ruling marking: the locked doc AND the source file are read-only;
	// the human-mode handbook is review-gated WRITABLE (never protected).
	assert.Equal(t, []string{"docs/locked.md", "src/payments/refund.ts"}, sortedKeys(ws.ProtectedPaths))
	assert.Equal(t, []string{"docs/handbook.md", "docs/payments.md"}, sortedKeys(ws.WritableAllowlist))

	rendered := workspace.RenderWorkspace(ws)
	assert.Contains(t, rendered, "path: docs/locked.md")
	assert.Contains(t, rendered, "reason: manifest mode 'locked' — terminal D2 floor")
	assert.Contains(t, rendered, "reason: manifest mode 'human' — review-gated patch-eligible (ARCH §11.2)")
	assert.Contains(t, rendered, "source file — analysis only: code files are never modified (invariant 2)")

	// Byte-stable render across three independent assemblies (injected clock).
	assert.Equal(t, rendered, workspace.RenderWorkspace(requireAssembled(t, input)))
	assert.Equal(t, rendered, workspace.RenderWorkspace(requireAssembled(t, input)))

	// The golden: canonicalJson(actual) vs canonicalJson(golden document),
	// exactly as the TS expectGolden helper compared them.
	assert.Equal(t, canonicalGoldenJSON(t, wantGoldenWorkspace), canonicalGoldenJSON(t, goldenWorkspaceDocument{
		AssembledAt:       ws.AssembledAt,
		ProtectedPaths:    sortedKeys(ws.ProtectedPaths),
		Rendered:          rendered,
		WritableAllowlist: sortedKeys(ws.WritableAllowlist),
	}))
}

func canonicalGoldenJSON(t *testing.T, document goldenWorkspaceDocument) string {
	t.Helper()
	encoded, err := json.Marshal(document)
	require.NoError(t, err)
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var tree any
	require.NoError(t, decoder.Decode(&tree))
	canonical, err := docutil.CanonicalJSON(tree)
	require.NoError(t, err)
	return canonical
}
