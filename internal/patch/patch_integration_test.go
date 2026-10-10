package patch_test

// Patch-layer E2E integration test over a REAL temp git repository — port of
// test/patch-integration.test.ts (P3-T18; BACKLOG V-P3 evidence; ARCH §4.3,
// §4.7, §4.9, §11.4, §12.1/§12.3, A9; invariants 2/3/12/13), restricted to the
// gates that exercise the PATCH layer (this package) end-to-end against a real
// git repo and the real *gitx.GitClient read path.
//
// SCOPE NOTE (exact next step): the TS integration test also carries V-P3-1
// (the assembled seven-component workspace + deterministic render golden) and
// the src/workspace half of the no-network proof. Those depend on
// internal/workspace (assembleWorkspace/renderWorkspace — a later migration
// wave, NOT yet ported) and the internal/testkit expectGolden helper, neither
// of which exists in this workspace, so they CANNOT be ported here. The
// manifest-derived writableAllowlist / protectedPaths are therefore hand-written
// for this fixture below, mirroring the TS V-P3-1 expectation sets exactly, so
// the engine still receives the §11.4 layer-3 derivation it would get from the
// workspace layer. The src/patch half of the no-network proof is already covered
// by TestPatchModulesImportNothingWriteCapable in apply_test.go.
//
// The writer is a local double (core cannot import @docsync/openrouter): it
// hand-builds the minimal patch fixing one claim line, fills the adapter-owned
// identity fields, and re-parses through schemas.ParseDocPatch (the adapter's
// fail-closed assembly step; core BARE-hex content hashes).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/validate"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- constants */

const (
	integrationRunID       = "acme/payments#8a73f2c..81af92e"
	integrationPolicyVer   = 17
	integrationWriterModel = "anthropic/claude-sonnet-4.5"
	integrationGeneratedAt = "2026-10-06T11:30:00.000Z"
	integrationNow         = "2026-10-06T11:45:00.000Z"
	integrationClaimLine   = "Refunds require an idempotency key."
	integrationFixedLine   = "Refunds require an idempotency key above 10 USD."
	integrationDuplicated  = "The review window is 48 hours."
	integrationFlowLine    = "Refunds flow through docs/architecture.md for the full diagram."
	integrationFlowRaced   = "Refunds flow through docs/architecture.md for the full refund flow."
)

var integrationEvidence = schemas.Evidence{
	Kind: schemas.EvidenceKindSymbolDiff,
	Ref:  "src/payments/refund.ts:3",
	Note: "idempotencyKey became optional for amounts under 10 USD",
}

/* ----------------------------------------------------------- fixture files */

var (
	paymentsLines = []string{
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
	paymentsDoc = strings.Join(paymentsLines, "\n") + "\n"

	handbookDoc = strings.Join([]string{
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

	lockedDoc = strings.Join([]string{
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

	refundSource = strings.Join([]string{
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

	refundSourceChanged = strings.Replace(
		strings.Replace(refundSource, "  readonly idempotencyKey: string;", "  readonly idempotencyKey?: string;", 1),
		"  return `refunded:${request.amount}:${request.idempotencyKey}`;",
		"  return `refunded:${request.amount}:${request.idempotencyKey ?? 'auto'}`;", 1,
	)

	manifestYAML = strings.Join([]string{
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

	stateCursor = "0\n"
)

func integrationStateBaselineJSON(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"doc_path":       "docs/payments.md",
		"content_hash":   docutil.ContentHash(paymentsDoc),
		"policy_version": integrationPolicyVer,
	})
	require.NoError(t, err)
	return string(b) + "\n"
}

/* ------------------------------------------------------------ fixture repo */

type integrationFixture struct {
	root              string
	client            *gitx.GitClient
	baseRev           string
	rev               string
	manifest          *schemas.Manifest
	payments          string
	handbook          string
	locked            string
	refundSource      string
	writableAllowlist map[string]bool
	protectedPaths    map[string]bool
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
}

func gitIn(t *testing.T, root string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-C", root,
		"-c", "user.name=docsync-test",
		"-c", "user.email=docsync-test@invalid",
	}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("git %v failed: %v\nstderr: %s", args, err, ee.Stderr)
		}
		t.Fatalf("git %v failed: %v", args, err)
	}
	return string(out)
}

func writeFixtureFile(t *testing.T, root, rel, content string) {
	t.Helper()
	target := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func commitAll(t *testing.T, root, msg string) string {
	t.Helper()
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", msg)
	return strings.TrimSpace(gitIn(t, root, "rev-parse", "HEAD"))
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	// Repo-local line-ending policy (mirror git-client.test.ts): pin LF before
	// any file is written so assertions never see CRLF.
	gitIn(t, root, "config", "core.autocrlf", "false")
	gitIn(t, root, "config", "core.eol", "lf")
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)

	writeFixtureFile(t, root, ".docsync/manifest.yml", manifestYAML)
	writeFixtureFile(t, root, ".docsync/state/audit-cursor", stateCursor)
	writeFixtureFile(t, root, ".docsync/state/baselines/docs__payments.md.json", integrationStateBaselineJSON(t))
	writeFixtureFile(t, root, "docs/payments.md", paymentsDoc)
	writeFixtureFile(t, root, "docs/handbook.md", handbookDoc)
	writeFixtureFile(t, root, "docs/locked.md", lockedDoc)
	writeFixtureFile(t, root, "src/payments/refund.ts", refundSource)
	baseRev := commitAll(t, root, "docs: initial payments docs, manifest, state")

	writeFixtureFile(t, root, "src/payments/refund.ts", refundSourceChanged)
	rev := commitAll(t, root, "feat(payments): make the refund idempotency key optional")

	// The on-disk manifest through the real fail-closed loader (fixture sanity).
	result, err := validate.LoadAndValidateManifest(root, validate.ManifestValidateOptions{})
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.NotNil(t, result.Manifest)

	ctx := context.Background()
	read := func(path string) string {
		blob, err := client.CatFile(ctx, rev, path)
		require.NoError(t, err)
		return blob.Content
	}

	f := &integrationFixture{
		root:         root,
		client:       client,
		baseRev:      baseRev,
		rev:          rev,
		manifest:     result.Manifest,
		payments:     read("docs/payments.md"),
		handbook:     read("docs/handbook.md"),
		locked:       read("docs/locked.md"),
		refundSource: read("src/payments/refund.ts"),
	}
	// §11.4 layer-3 derivation, hand-written for this fixture (internal/workspace
	// is a later wave). Mirrors the TS V-P3-1 expectation sets exactly: the
	// locked doc AND the source file are read-only; the human-mode handbook is
	// review-gated WRITABLE.
	f.writableAllowlist = map[string]bool{"docs/payments.md": true, "docs/handbook.md": true}
	f.protectedPaths = map[string]bool{"docs/locked.md": true, "src/payments/refund.ts": true}
	require.Equal(t, paymentsDoc, f.payments) // the real git read matches the fixture
	return f
}

func integrationApplyInput(f *integrationFixture, p *schemas.DocPatch) patch.ApplyDocPatchInput {
	return patch.ApplyDocPatchInput{
		Patch:             p,
		Git:               f.client,
		Rev:               f.rev,
		ManifestAllowlist: f.writableAllowlist,
		ProtectedPaths:    f.protectedPaths,
		Now:               func() string { return integrationNow },
	}
}

/* ----------------------------------------------------------- writer double */

// reparseDocPatch mirrors DOC_PATCH_SCHEMA.parse (the adapter's fail-closed
// assembly step): round-trip a candidate through the real strict parser.
func reparseDocPatch(t *testing.T, p *schemas.DocPatch) *schemas.DocPatch {
	t.Helper()
	data, err := json.Marshal(p)
	require.NoError(t, err)
	parsed, err := schemas.ParseDocPatch(data)
	require.NoError(t, err)
	return parsed
}

func buildMinimalDocPatch(t *testing.T, baseContent, deleteLine, insertText, docPath string, claimIDs []string) *schemas.DocPatch {
	t.Helper()
	lines := strings.Split(baseContent, "\n")
	lineIndex := slices.Index(lines, deleteLine)
	require.GreaterOrEqual(t, lineIndex, 0, "writer double: line not found in fixture content: %q", deleteLine)
	target := docPath
	if target == "" {
		target = "docs/payments.md"
	}
	h := schemas.PatchHunk{
		Anchor: schemas.PatchAnchor{
			StartLine:   lineIndex + 1,
			EndLine:     lineIndex + 1,
			ContentHash: docutil.ContentHash(deleteLine),
		},
		DeleteText:   deleteLine,
		InsertText:   insertText,
		EvidenceRefs: []schemas.Evidence{integrationEvidence},
	}
	if claimIDs != nil {
		h.ClaimIDs = claimIDs
	}
	return reparseDocPatch(t, &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           integrationRunID,
		DocPath:         target,
		BaseContentHash: docutil.ContentHash(baseContent),
		Hunks:           []schemas.PatchHunk{h},
		EvidenceRefs:    []schemas.Evidence{integrationEvidence},
		Minimality:      schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 1},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: integrationPolicyVer, WriterModel: integrationWriterModel, GeneratedAt: integrationGeneratedAt,
		},
	})
}

func writerDouble(t *testing.T, baseContent string) *schemas.DocPatch {
	return buildMinimalDocPatch(t, baseContent, integrationClaimLine, integrationFixedLine, "docs/payments.md", nil)
}

func rewriteDouble(t *testing.T, baseContent string) *schemas.DocPatch {
	t.Helper()
	bodyLines := strings.Split(baseContent, "\n")
	if bodyLines[len(bodyLines)-1] == "" {
		bodyLines = bodyLines[:len(bodyLines)-1]
	}
	body := strings.Join(bodyLines, "\n")
	return reparseDocPatch(t, &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           integrationRunID,
		DocPath:         "docs/payments.md",
		BaseContentHash: docutil.ContentHash(baseContent),
		Hunks: []schemas.PatchHunk{{
			Anchor: schemas.PatchAnchor{
				StartLine:   1,
				EndLine:     len(bodyLines),
				ContentHash: docutil.ContentHash(body),
			},
			DeleteText:   body,
			InsertText:   "The payments policy has been rewritten from scratch.",
			EvidenceRefs: []schemas.Evidence{integrationEvidence},
		}},
		EvidenceRefs: []schemas.Evidence{integrationEvidence},
		Minimality:   schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: len(bodyLines)},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: integrationPolicyVer, WriterModel: integrationWriterModel, GeneratedAt: integrationGeneratedAt,
		},
	})
}

func ambiguousDouble(t *testing.T, baseContent string) *schemas.DocPatch {
	return buildMinimalDocPatch(t, baseContent, integrationDuplicated, "The review window is 72 hours for disputed refunds.", "docs/payments.md", nil)
}

type multiKindEdit struct {
	deleteLine string
	insert     string
	kind       schemas.EvidenceKind
	ref        string
	note       string
}

var multiKindEdits = []multiKindEdit{
	{integrationClaimLine, integrationFixedLine, schemas.EvidenceKindSymbolDiff, "src/payments/refund.ts:3", "idempotencyKey became optional for small refunds"},
	{"Amounts above 10 USD are held for review.", "Amounts above 10 USD are held for review after sign-off.", schemas.EvidenceKindAPIDiff, "src/payments/refund.ts", "the refund API surface changed"},
	{integrationFlowLine, "Refunds flow through docs/architecture.md for the complete diagram.", schemas.EvidenceKindDocAnchor, "docs/payments.md:5", "the review-window claim line"},
	{"Dispute handling follows docs/disputes.md.", "Dispute handling follows docs/disputes.md and the escalation ladder.", schemas.EvidenceKindConfigDiff, ".docsync/manifest.yml:version", "manifest schema version anchors the config surface"},
	{"Escalation contacts live in docs/oncall.md.", "Escalation contacts live in docs/oncall.md during business hours.", schemas.EvidenceKindDependencyDiff, "pnpm", "workspace package manager dependency surface"},
}

func multiKindWriterDouble(t *testing.T, baseContent string) *schemas.DocPatch {
	t.Helper()
	lines := strings.Split(baseContent, "\n")
	hunks := make([]schemas.PatchHunk, len(multiKindEdits))
	allEvidence := make([]schemas.Evidence, len(multiKindEdits))
	for i, edit := range multiKindEdits {
		lineIndex := slices.Index(lines, edit.deleteLine)
		require.GreaterOrEqual(t, lineIndex, 0, "multi-kind writer double: line not found: %q", edit.deleteLine)
		ev := schemas.Evidence{Kind: edit.kind, Ref: edit.ref, Note: edit.note}
		allEvidence[i] = ev
		hunks[i] = schemas.PatchHunk{
			Anchor: schemas.PatchAnchor{
				StartLine:   lineIndex + 1,
				EndLine:     lineIndex + 1,
				ContentHash: docutil.ContentHash(edit.deleteLine),
			},
			DeleteText:   edit.deleteLine,
			InsertText:   edit.insert,
			EvidenceRefs: []schemas.Evidence{ev},
		}
	}
	return reparseDocPatch(t, &schemas.DocPatch{
		Schema:          "docsync.doc-patch/v1",
		RunID:           integrationRunID,
		DocPath:         "docs/payments.md",
		BaseContentHash: docutil.ContentHash(baseContent),
		Hunks:           hunks,
		EvidenceRefs:    allEvidence,
		Minimality:      schemas.DocPatchMinimality{Measured: true, AddedLines: 5, RemovedLines: 5},
		Provenance: schemas.DocPatchProvenance{
			PolicyVersion: integrationPolicyVer, WriterModel: integrationWriterModel, GeneratedAt: integrationGeneratedAt,
		},
	})
}

/* ------------------------------------------------------------------- gates */

func TestPatchIntegrationE2E(t *testing.T) {
	t.Run("writer double E2E: the minimal claim fix applies over the fixture repo (single hunk, no race)", func(t *testing.T) {
		f := newIntegrationFixture(t)
		p := writerDouble(t, f.payments)
		o := mustApply(t, integrationApplyInput(f, p))
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.False(t, o.ThreeWayUsed)
		require.Equal(t, integrationNow, o.AppliedAt)
		require.Equal(t, []patch.AppliedHunk{{Index: 0, StartLine: 3, EndLine: 3, ReAnchored: false}}, o.AppliedHunks)
		require.Equal(t, strings.Replace(paymentsDoc, integrationClaimLine, integrationFixedLine, 1), o.ResultContent)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 1}, o.Minimality.Minimality)
	})

	t.Run("V-P3-2 E2E: a full-document replacement is rejected structurally before application", func(t *testing.T) {
		f := newIntegrationFixture(t)
		p := rewriteDouble(t, f.payments)
		o := mustApply(t, integrationApplyInput(f, p))
		require.Equal(t, patch.OutcomeAbstain, o.Kind)
		require.Equal(t, patch.AbstainRewriteRejected, o.AbstainReason)
		require.Contains(t, o.Detail, "deletion_span")
		require.Contains(t, o.Detail, "0.9")
	})

	t.Run("V-P3-3 E2E: an ambiguous anchor abstains UNANCHORED, never re-anchored silently", func(t *testing.T) {
		f := newIntegrationFixture(t)
		p := ambiguousDouble(t, f.payments)
		o := mustApply(t, integrationApplyInput(f, p))
		require.Equal(t, patch.OutcomeUnanchored, o.Kind)
		require.Equal(t, patch.DocStateSignalUnanchored, o.DocStateSignal)
		require.Equal(t, []patch.UnanchoredHunk{
			{Index: 0, Reason: "ambiguous — 2 identical spans (never re-anchor on ambiguity, invariant 13)"},
		}, o.UnanchoredHunks)
	})

	t.Run("V-P3-4 E2E: race — abstain without reVerify; applied with a passing reVerify carrying both changes", func(t *testing.T) {
		f := newIntegrationFixture(t)
		base := f.payments
		p := writerDouble(t, base)

		// The doc changes after the writer read it: a concurrent commit edits the
		// flow line, then HEAD carries it.
		writeFixtureFile(t, f.root, "docs/payments.md", strings.Replace(paymentsDoc, integrationFlowLine, integrationFlowRaced, 1))
		raceRev := commitAll(t, f.root, "docs: clarify the refund flow")

		// Race with nothing to merge from → abstain (fail closed).
		noSnapshot := integrationApplyInput(f, p)
		noSnapshot.Rev = raceRev
		out1 := mustApply(t, noSnapshot)
		require.Equal(t, patch.OutcomeAbstain, out1.Kind)
		require.Equal(t, patch.AbstainDocChangedRaceUnverified, out1.AbstainReason)
		require.Contains(t, out1.Detail, "no base snapshot was provided")

		// Race with the writer snapshot but NO reVerify port → abstain (A9).
		noReVerify := integrationApplyInput(f, p)
		noReVerify.Rev = raceRev
		noReVerify.BaseContent = strp(base)
		out2 := mustApply(t, noReVerify)
		require.Equal(t, patch.OutcomeAbstain, out2.Kind)
		require.Contains(t, out2.Detail, "no reVerify port was provided")

		// Race with a PASSING reVerify → applied via a deterministic 3-way merge
		// carrying BOTH changes.
		applied := integrationApplyInput(f, p)
		applied.Rev = raceRev
		applied.BaseContent = strp(base)
		applied.ReVerify = func(_ string, _ *schemas.DocPatch) patch.ReVerifyResult { return patch.ReVerifyResult{Pass: true} }
		out3 := mustApply(t, applied)
		require.Equal(t, patch.OutcomeApplied, out3.Kind)
		require.True(t, out3.ThreeWayUsed)
		require.Equal(t,
			strings.Replace(
				strings.Replace(paymentsDoc, integrationFlowLine, integrationFlowRaced, 1),
				integrationClaimLine, integrationFixedLine, 1),
			out3.ResultContent)
		require.Equal(t, []patch.AppliedHunk{{Index: 0, StartLine: 3, EndLine: 3, ReAnchored: false}}, out3.AppliedHunks)
	})

	t.Run("V-P3-5 E2E: idempotency through the real git read path + isRunApplied run-id skip", func(t *testing.T) {
		f := newIntegrationFixture(t)
		p := writerDouble(t, f.payments)

		first := mustApply(t, integrationApplyInput(f, p))
		require.Equal(t, patch.OutcomeApplied, first.Kind)

		// Persist the patched result and commit, so HEAD carries the full effect.
		writeFixtureFile(t, f.root, "docs/payments.md", first.ResultContent)
		appliedRev := commitAll(t, f.root, "docs: record the idempotency-key fix")

		// Re-applying the SAME patch over the applied doc → content-level no-op,
		// read through the REAL git path (catFile at the new HEAD).
		second := integrationApplyInput(f, p)
		second.Rev = appliedRev
		second.BaseContent = strp(f.payments)
		out2 := mustApply(t, second)
		require.Equal(t, patch.OutcomeNoop, out2.Kind)
		require.Equal(t, patch.NoopAlreadyApplied, out2.NoopReason)

		// The persisted run-state port skips before any content work, called with
		// the patch's own run identity.
		var seen [][2]string
		skip := integrationApplyInput(f, p)
		skip.IsRunApplied = func(runID, docPath string) bool {
			seen = append(seen, [2]string{runID, docPath})
			return true
		}
		out3 := mustApply(t, skip)
		require.Equal(t, patch.OutcomeNoop, out3.Kind)
		require.Equal(t, patch.NoopRunIDSkip, out3.NoopReason)
		require.Equal(t, [][2]string{{p.RunID, p.DocPath}}, seen)
	})

	t.Run("V-P3-6: minimality flag — oversized is flagged while apply still returns applied", func(t *testing.T) {
		f := newIntegrationFixture(t)
		p := writerDouble(t, f.payments)

		flagged := integrationApplyInput(f, p)
		flagged.TriggeringDiffStats = &patch.DiffStats{Added: 1, Removed: 0}
		maxRatio := 1.5
		flagged.MaxRatio = &maxRatio
		flaggedOut := mustApply(t, flagged)
		require.Equal(t, patch.OutcomeApplied, flaggedOut.Kind)
		require.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 1, RemovedLines: 1}, flaggedOut.Minimality.Minimality)
		require.NotNil(t, flaggedOut.Minimality.Ratio)
		require.InDelta(t, 2.0, *flaggedOut.Minimality.Ratio, 1e-12)
		require.True(t, flaggedOut.Minimality.Oversized)

		prop := integrationApplyInput(f, p)
		prop.TriggeringDiffStats = &patch.DiffStats{Added: 1, Removed: 1}
		prop.MaxRatio = &maxRatio
		propOut := mustApply(t, prop)
		require.Equal(t, patch.OutcomeApplied, propOut.Kind)
		require.NotNil(t, propOut.Minimality.Ratio)
		require.InDelta(t, 1.0, *propOut.Minimality.Ratio, 1e-12)
		require.False(t, propOut.Minimality.Oversized)
	})

	t.Run("V-P3-7 E2E: guard refusals — code, .docsync/-internal and locked-doc targets each refused whole", func(t *testing.T) {
		f := newIntegrationFixture(t)
		targets := []struct {
			path          string
			base          string
			deleteLine    string
			insert        string
			alsoProtected bool
		}{
			{"src/payments/refund.ts", f.refundSource, "export interface RefundRequest {", "export interface RefundRequestV2 {", true},
			{".docsync/state/audit-cursor", stateCursor, "0", "1", false},
			{"docs/locked.md", f.locked, "The AI never fights another generator in this repository.", "The AI may rewrite the generated compliance text.", true},
		}
		for _, target := range targets {
			// The allowlist is manifest-derived (layer-3 derivation), so none of
			// the three targets is patchable; the locked doc + code file are ALSO
			// protected (layer 2 agrees with layer 3).
			require.False(t, f.writableAllowlist[target.path])
			require.Equal(t, target.alsoProtected, f.protectedPaths[target.path])

			p := buildMinimalDocPatch(t, target.base, target.deleteLine, target.insert, target.path, nil)
			o := mustApply(t, integrationApplyInput(f, p))
			require.Equal(t, patch.OutcomeAbstain, o.Kind, "target %s", target.path)
			require.Equal(t, patch.AbstainGuardRefused, o.AbstainReason, "target %s", target.path)
			require.Contains(t, o.Detail, "'"+target.path+"'")
			require.Contains(t, o.Detail, "manifest-derived writable allowlist")
			require.Contains(t, o.Detail, "invariant 3")
		}
	})

	t.Run("V-P3-9: evidence refs on every applied patch + provenance record", func(t *testing.T) {
		f := newIntegrationFixture(t)
		p := writerDouble(t, f.payments)
		o := mustApply(t, integrationApplyInput(f, p))
		require.Equal(t, patch.OutcomeApplied, o.Kind)
		require.NotNil(t, o.Patch)

		// Schema-enforced minimum (OD-R08): every hunk carries >= 1 evidence ref.
		require.NotEmpty(t, o.Patch.Hunks)
		for _, hunk := range o.Patch.Hunks {
			require.GreaterOrEqual(t, len(hunk.EvidenceRefs), 1)
		}
		require.NoError(t, reparseCheck(o.Patch))

		require.Equal(t, integrationPolicyVer, o.Patch.Provenance.PolicyVersion)
		require.NotEmpty(t, o.Patch.Provenance.WriterModel)
		require.Equal(t, integrationGeneratedAt, o.Patch.Provenance.GeneratedAt)
	})

	t.Run("V-P3-9: writer provenance carried through application unchanged", func(t *testing.T) {
		f := newIntegrationFixture(t)
		p := writerDouble(t, f.payments)
		o := mustApply(t, integrationApplyInput(f, p))
		require.Equal(t, patch.OutcomeApplied, o.Kind)

		// The routed-model fixture string survives UNCHANGED.
		require.Regexp(t, `^[a-zA-Z0-9.-]+/[a-zA-Z0-9.-]+$`, integrationWriterModel)
		require.NotContains(t, strings.ToLower(integrationWriterModel), "placeholder")
		require.Equal(t, integrationWriterModel, o.Patch.Provenance.WriterModel)
		require.Equal(t, integrationPolicyVer, o.Patch.Provenance.PolicyVersion)
		require.Equal(t, integrationGeneratedAt, o.Patch.Provenance.GeneratedAt)
		require.Equal(t, p, o.Patch) // the same patch object carried through unchanged
	})

	t.Run("V-P3-10: per-hunk evidence refs resolve against fixture anchors (all five kinds)", func(t *testing.T) {
		f := newIntegrationFixture(t)
		p := multiKindWriterDouble(t, f.payments)

		kinds := make([]schemas.EvidenceKind, len(p.Hunks))
		for i, hunk := range p.Hunks {
			kinds[i] = hunk.EvidenceRefs[0].Kind
		}
		require.Equal(t, []schemas.EvidenceKind{
			schemas.EvidenceKindSymbolDiff,
			schemas.EvidenceKindAPIDiff,
			schemas.EvidenceKindDocAnchor,
			schemas.EvidenceKindConfigDiff,
			schemas.EvidenceKindDependencyDiff,
		}, kinds)

		o := mustApply(t, integrationApplyInput(f, p))
		require.Equal(t, patch.OutcomeApplied, o.Kind)

		for _, hunk := range o.Patch.Hunks {
			require.GreaterOrEqual(t, len(hunk.EvidenceRefs), 1)
			require.NoError(t, schemas.AssertResolvableEvidence(toEvidenceRefs(hunk.EvidenceRefs),
				"applied patch hunk evidence ("+string(hunk.EvidenceRefs[0].Kind)+")"))
			for _, item := range hunk.EvidenceRefs {
				require.True(t, schemas.IsResolvableEvidence(string(item.Kind), item.Ref))
			}
		}
		require.NoError(t, schemas.AssertResolvableEvidence(toEvidenceRefs(o.Patch.EvidenceRefs), "applied patch aggregate evidence"))

		// Negative case: a note-only FREE-TEXT ref is not a machine-resolvable
		// anchor — presented as a structured kind, the canonical shape rejects it.
		noteOnly := []schemas.EvidenceRef{{
			Kind: string(schemas.EvidenceKindSymbolDiff),
			Ref:  "the idempotency key became optional for small refunds, see the refund flow",
			Note: "free-text rationale without an anchor",
		}}
		require.False(t, schemas.IsResolvableEvidence(string(schemas.EvidenceKindSymbolDiff), noteOnly[0].Ref))
		negErr := schemas.AssertResolvableEvidence(noteOnly, "negative case")
		require.Error(t, negErr)
		var notResolvable *schemas.EvidenceNotResolvableError
		require.True(t, errors.As(negErr, &notResolvable))
	})

	t.Run("V-P3-11: cross-patch claim-value conflicts are detectable across two patches", func(t *testing.T) {
		f := newIntegrationFixture(t)
		patchA := buildMinimalDocPatch(t, f.payments, integrationClaimLine, integrationFixedLine, "docs/payments.md", []string{"claim_idempotency"})
		patchB := buildMinimalDocPatch(t, f.payments, integrationClaimLine, "Refunds require an idempotency key for every request.", "docs/payments.md", []string{"claim_idempotency"})

		exportsA := patch.ExportPatchClaimValues(patchA)
		require.Equal(t, []patch.ExportedClaimValue{
			{ClaimID: "claim_idempotency", DocPath: "docs/payments.md", HunkIndex: 0, Value: integrationFixedLine, EvidenceRefKinds: []string{"symbol_diff"}},
		}, exportsA)

		exportsB := patch.ExportPatchClaimValues(patchB)
		require.Equal(t, "Refunds require an idempotency key for every request.", exportsB[0].Value)

		conflicts := patch.DetectClaimValueConflicts(append(append([]patch.ExportedClaimValue{}, exportsA...), exportsB...))
		require.Len(t, conflicts, 1)
		require.Equal(t, "claim_idempotency", conflicts[0].ClaimID)
		require.Len(t, conflicts[0].Conflicts, 2)
		values := make([]string, 0)
		for _, group := range conflicts[0].Conflicts {
			if len(group) > 0 {
				values = append(values, group[0].Value)
			}
		}
		require.Contains(t, values, integrationFixedLine)
		require.Contains(t, values, "Refunds require an idempotency key for every request.")
	})
}

// reparseCheck mirrors DOC_PATCH_SCHEMA.safeParse(...).success: marshals and
// re-parses (returns the parse error, nil when valid).
func reparseCheck(p *schemas.DocPatch) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = schemas.ParseDocPatch(data)
	return err
}

// toEvidenceRefs converts hunk Evidence into the canonical EvidenceRef shape the
// resolvable-shape gate consumes.
func toEvidenceRefs(items []schemas.Evidence) []schemas.EvidenceRef {
	out := make([]schemas.EvidenceRef, len(items))
	for i, it := range items {
		out[i] = schemas.EvidenceRef{Kind: string(it.Kind), Ref: it.Ref, Note: it.Note}
	}
	return out
}
