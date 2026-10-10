// Black-box port of packages/core/test/audit-compose.test.ts (P11-T21; ARCH
// §7.2/§10.2; PLAN §12.5 V-P11-8/V-P11-9 follow-through), plus the composed
// nightly-audit half of packages/core/test/audit-suggest.test.ts (V-P11-10,
// the finding → SUGGEST mapping over the real audit output).
//
// Pins:
//   - PR-context triggers are refused END-TO-END in the composed runtime —
//     the P11-T18 structural refusal propagates as a typed no-op with NO
//     audit, NO store writes (findings/runs JSONL absent, audit cursor
//     absent).
//   - the composed audit is READ-MOSTLY — no document bytes are modified
//     (byte-identical snapshots + a clean worktree over tracked files).
//   - ineligible triggers produce typed no-op results without touching the
//     findings store.
//   - an invalid caller-supplied manifest abstains fail-closed — never a
//     partial scan.
//
// Fixtures: real system git in a deterministic temp repo with pinned
// identity/dates and repo-local core.autocrlf=false + core.eol=lf (the TS
// shared drift fixture, ported inline); the REAL StateStore claim store, the
// REAL claim-support sweep over that repo, and the RNA-seq findings store.
// Zero LLM: every drift verdict is a deterministic git blob-identity/anchor
// fact.

package audit_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/workspace"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------ fixture constants */

const (
	composeDocPath    = "docs/payments.md"
	composeRefundPath = "src/payments/refund.ts"
	composeRetryPath  = "src/api/retry.ts"
	composeLedgerPath = "src/payments/ledger.ts"
	composeUnrelPath  = "src/payments/unrelated.ts"
	composeExtraPath  = "src/payments/extra.ts"
	composeClaimA     = "claim_a"
	composeClaimB     = "claim_b"
	composeClaimC     = "claim_c"
	composeRunID      = "run_audit_p11t21"
)

var composeDocLines = []string{
	"# Payments",
	"",
	"Refunds require an idempotency key.",
	"",
	"Retries back off exponentially.",
	"",
	"Payments are stored in the ledger table.",
	"",
}

var (
	composeRefundV1 = strings.Join([]string{
		"export function refundPayment(idempotencyKey: string): void {}",
		"",
		"function removedHelper(): void {}",
		"",
	}, "\n")
	composeRefundV2 = strings.Join([]string{
		"export function refundPayment(request: { idempotencyKey: string }): void {}",
		"",
	}, "\n")
	composeRetrySrc = strings.Join([]string{"export function retryWithBackoff(attempt: number): void {}", ""}, "\n")
	composeLedgerS  = strings.Join([]string{"export function ledgerEntry(amount: number): void {}", ""}, "\n")
)

// composeManifest is the audit's manifest scope: the drift-fixture doc,
// suggest mode, D7 globs (TS MANIFEST).
func composeManifest() schemas.Manifest {
	mode := schemas.DocModeSuggest
	return schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: composeDocPath, Mode: &mode, Sources: []string{"src/payments/**"}},
		},
	}
}

// composeStores are the claim store + RNA-seq findings store over the
// fixture's own `.docsync/state` dir (the TS single stateDir — so the
// read-mostly assertion sees exactly what the run wrote).
type composeStores struct {
	stateDir string
	claims   *claims.ClaimStore
	store    audit.AuditFindingsStore
}

// newComposeStores wires the stores over the fixture state store/dir.
func newComposeStores(t *testing.T, stateStore *state.StateStore, stateDir string) composeStores {
	t.Helper()
	findingsStore, err := audit.CreateAuditFindingsStore(stateDir, nil)
	require.NoError(t, err)
	return composeStores{
		stateDir: stateDir,
		claims:   claims.NewClaimStore(stateStore),
		store:    findingsStore,
	}
}

// upsertClaims installs the fixture claims A and B (the TS nightly-audit
// setup).
func (s composeStores) upsertClaims(t *testing.T, fixture *composeFixture) {
	t.Helper()
	for _, id := range []string{composeClaimA, composeClaimB} {
		write, err := s.claims.Upsert(fixture.claimsFor(t, id), &claims.ClaimUpsertOptions{ExpectedHash: nil})
		require.NoError(t, err)
		require.True(t, write.OK)
	}
}

// baseDeps is the composed-audit deps of the default nightly scenario.
func (s composeStores) baseDeps(t *testing.T, fixture *composeFixture) audit.LoopBAuditComposeDeps {
	t.Helper()
	return audit.LoopBAuditComposeDeps{
		Config: audit.LoopBScheduleConfig{Nightly: true, PostMerge: false, DefaultBranch: "main"},
		Context: audit.LoopBTriggerContext{
			Event: audit.LoopBEventNightly, Branch: strPtr("main"), PRNumber: nil,
		},
		Manifest:        composeManifest(),
		StateStore:      fixture.stateStore,
		Git:             fixture.client(t),
		Claims:          s.claims,
		Store:           s.store,
		RunID:           composeRunID,
		Now:             auditNow,
		CommitWalkDepth: intPtr(5),
	}
}

// scenario materializes the fixture + claims + the composed deps (the TS
// makeScenario).
func makeScenario(t *testing.T) (*composeFixture, composeStores, audit.LoopBAuditComposeDeps, string) {
	t.Helper()
	fixture := createComposeFixture(t)
	stores := newComposeStores(t, fixture.stateStore, fixture.stateDir)
	stores.upsertClaims(t, fixture)
	return fixture, stores, stores.baseDeps(t, fixture), stores.stateDir
}

/* ------------------------------------------------------------------- tests */

func TestP11T21_PRContextTriggersAreRefusedEndToEnd(t *testing.T) {
	_, _, deps, stateDir := makeScenario(t)

	// A PR event — and ANY pr number in the context, regardless of the
	// declared event — is the developer's PR critical path: refused.
	prEvent, err := audit.RunLoopBAudit(context.Background(), withContext(deps,
		audit.LoopBTriggerContext{Event: audit.LoopBEventPR, Branch: strPtr("main"), PRNumber: nil}))
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditNoop, prEvent.Kind)
	require.Equal(t, audit.NoopPRCriticalPathForbidden, prEvent.Reason)

	prNumber, err := audit.RunLoopBAudit(context.Background(), withContext(deps,
		audit.LoopBTriggerContext{Event: audit.LoopBEventPostMerge, Branch: strPtr("main"), PRNumber: intPtr(7)}))
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditNoop, prNumber.Kind)
	require.Equal(t, audit.NoopPRCriticalPathForbidden, prNumber.Reason)

	// End-to-end refusal: NO store writes at all — the findings/runs JSONL
	// files were never created and the audit cursor was never written.
	requireNoFile(t, filepath.Join(stateDir, "audit-findings.jsonl"))
	requireNoFile(t, filepath.Join(stateDir, "audit-runs.jsonl"))
	requireNoFile(t, filepath.Join(stateDir, "audit-cursor"))
	cursor, err := deps.StateStore.ReadAuditCursor()
	require.NoError(t, err)
	require.False(t, cursor.Exists)

	// No artifacts were rendered for a refused trigger.
	require.Nil(t, prEvent.Artifacts)
	require.Nil(t, prEvent.RunRecord)
	require.Nil(t, prEvent.Result)
}

func TestP11T21_ComposedAuditIsReadMostly(t *testing.T) {
	fixture, stores, deps, _ := makeScenario(t)

	// Byte snapshots of EVERY document + source file in the repo.
	tracked := []string{
		composeDocPath, composeRefundPath, composeRetryPath,
		composeLedgerPath, composeUnrelPath, composeExtraPath,
	}
	before := snapshotTracked(t, fixture, tracked)

	out, err := audit.RunLoopBAudit(context.Background(), deps)
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditRan, out.Kind)
	require.Equal(t, audit.LoopBTriggerNightlyScheduled, out.Trigger)
	require.Equal(t, audit.AuditRunCompleted, out.Result.Status)
	require.Equal(t, []string{composeDocPath}, out.Result.AuditedDocs)

	// The headline finding: claim_a lost support (its blob moved at head);
	// claim_b is supported — no finding. Closed vocabulary + evidence.
	require.Len(t, out.Findings, 1)
	finding := out.Findings[0]
	require.Equal(t, "claim-lost-support", finding.Kind)
	require.Equal(t, strPtr(composeClaimA), finding.ClaimID)
	require.Equal(t, composeDocPath, finding.DocPath)
	require.Equal(t, "loopb:claim-lost-support:"+composeClaimA, finding.ID)
	require.Equal(t, []string{fixture.headSha}, finding.Evidence.Commits)
	require.Equal(t, []string{composeDocPath, composeRefundPath}, finding.Evidence.Paths)
	require.Contains(t, finding.Evidence.Detail, "source blob moved at head")
	require.Equal(t, auditNow, finding.CreatedAt)

	// Byte-identical: every document/source file is untouched.
	after := snapshotTracked(t, fixture, tracked)
	for index, path := range tracked {
		require.Equal(t, before[index], after[index], "tracked file %s was modified", path)
	}
	// The worktree is clean over every TRACKED file (only untracked state).
	for _, line := range gitStatus(t, fixture.root) {
		require.True(t, strings.HasPrefix(line, "??"), "unexpected worktree change: %q", line)
	}

	// Provenance + advisory artifacts (P11-T20 store + renderers).
	require.NotNil(t, out.RunRecord)
	require.Equal(t, schemas.DriftAuditTriggerNightlyScheduled, out.RunRecord.Trigger)
	require.Equal(t, schemas.DriftAuditRunStatus(audit.AuditRunCompleted), out.RunRecord.Status)
	require.Len(t, out.RunRecord.Findings, 1)
	require.Nil(t, out.RunRecord.CursorBefore)
	require.NotNil(t, out.RunRecord.CursorAfter)
	require.Contains(t, *out.RunRecord.CursorAfter, "loopb:v1;")
	require.NotNil(t, out.Artifacts)
	require.Equal(t, "docsync: background drift audit (advisory)", out.Artifacts.CheckRun.Title)
	require.Contains(t, out.Artifacts.Issue, "No document was modified")
	require.NotContains(t, out.Artifacts.Issue, "```diff")
	require.Equal(t, 1, jsonlLineCount(t, stores.store.FindingsFile()))
	require.Equal(t, 1, jsonlLineCount(t, stores.store.RunsFile()))

	// V-P11-10 shared floor: the finding maps to a SUGGEST candidate.
	mapping := audit.FindingsToSuggestCandidates(out.Findings,
		map[string]workspace.WorkspaceMode{composeDocPath: workspace.WorkspaceModeSuggest})
	require.Empty(t, mapping.AdvisoryOnly)
	require.Len(t, mapping.Candidates, 1)
	require.Equal(t, composeDocPath, mapping.Candidates[0].DocPath)
	require.Equal(t, workspace.WorkspaceModeSuggest, mapping.Candidates[0].Mode)
	require.Equal(t, strPtr(composeClaimA), mapping.Candidates[0].ClaimID)
	require.Equal(t, "claim-lost-support", mapping.Candidates[0].Kind)
	require.Equal(t, audit.DriftSuggestTriggerLoopB, mapping.Candidates[0].Trigger)
	require.Equal(t, finding.Evidence.Detail, mapping.Candidates[0].Reason)

	// Second run: the cursor resumes past the completed chunk — zero new
	// findings, zero appended lines, and STILL no document writes.
	second, err := audit.RunLoopBAudit(context.Background(), deps)
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditRan, second.Kind)
	require.Equal(t, audit.AuditRunCompleted, second.Result.Status)
	require.Equal(t, []string{composeDocPath}, second.Result.SkippedDocs)
	require.Empty(t, second.Findings)
	// No chunk was re-audited, so no findings were offered to the store at
	// all: zero appends (not "appends suppressed by dedup").
	require.Empty(t, second.Appended.Appended)
	require.Empty(t, second.Appended.Duplicates)
	require.Equal(t, 1, jsonlLineCount(t, stores.store.FindingsFile()))
	require.Equal(t, 2, jsonlLineCount(t, stores.store.RunsFile()))
	require.Equal(t, before[0], trackRead(t, fixture, composeDocPath))
}

func TestP11T21_IneligibleTriggerProducesTypedNoopWithoutStoreWrites(t *testing.T) {
	// Unconfigured nightly schedule: noop, no store writes.
	unconfigured := createComposeFixture(t)
	unconfiguredStores := newComposeStores(t, unconfigured.stateStore, unconfigured.stateDir)
	unconfiguredStores.upsertClaims(t, unconfigured)
	unconfiguredDeps := unconfiguredStores.baseDeps(t, unconfigured)
	unconfiguredDeps.Config = audit.LoopBScheduleConfig{
		Nightly: false, PostMerge: false, DefaultBranch: "main",
	}
	out, err := audit.RunLoopBAudit(context.Background(), unconfiguredDeps)
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditNoop, out.Kind)
	require.Equal(t, audit.NoopNightlyNotConfigured, out.Reason)
	requireNoFile(t, filepath.Join(unconfiguredStores.stateDir, "audit-findings.jsonl"))
	requireNoFile(t, filepath.Join(unconfiguredStores.stateDir, "audit-runs.jsonl"))

	// Nightly on the wrong branch: noop, no store writes, no cursor.
	wrongBranch := createComposeFixture(t)
	wrongBranchStores := newComposeStores(t, wrongBranch.stateStore, wrongBranch.stateDir)
	wrongBranchStores.upsertClaims(t, wrongBranch)
	wrongBranchDeps := wrongBranchStores.baseDeps(t, wrongBranch)
	wrongBranchDeps.Context = audit.LoopBTriggerContext{
		Event: audit.LoopBEventNightly, Branch: strPtr("feature/other"), PRNumber: nil,
	}
	outBranch, err := audit.RunLoopBAudit(context.Background(), wrongBranchDeps)
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditNoop, outBranch.Kind)
	require.Equal(t, audit.NoopNotDefaultBranch, outBranch.Reason)
	requireNoFile(t, filepath.Join(wrongBranchStores.stateDir, "audit-findings.jsonl"))
	requireNoFile(t, filepath.Join(wrongBranchStores.stateDir, "audit-runs.jsonl"))
	cursor, err := wrongBranchDeps.StateStore.ReadAuditCursor()
	require.NoError(t, err)
	require.False(t, cursor.Exists)
}

func TestP11T21_InvalidManifestAbstainsFailClosed(t *testing.T) {
	fixture, _, deps, stateDir := makeScenario(t)

	// Malformed manifest: schema-invalid (sources must be a nonempty array).
	invalid := deps.Manifest
	invalid.Documents[0].Sources = []string{}
	deps.Manifest = invalid

	out, err := audit.RunLoopBAudit(context.Background(), deps)
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditAbstained, out.Kind)
	require.Contains(t, out.Detail, "fail closed")
	// Nothing ran, nothing wrote, nothing rendered.
	requireNoFile(t, filepath.Join(stateDir, "audit-findings.jsonl"))
	requireNoFile(t, filepath.Join(stateDir, "audit-runs.jsonl"))
	require.Nil(t, out.RunRecord)
	require.Nil(t, out.Artifacts)
	require.Empty(t, out.Findings)
	require.Nil(t, out.Result)
	// The engine never ran: the manifest gate refuses BEFORE any git read.
	requireNoFile(t, filepath.Join(stateDir, "audit-cursor"))
	cursor, err := fixture.stateStore.ReadAuditCursor()
	require.NoError(t, err)
	require.False(t, cursor.Exists)
}

func TestP11T21_UnsupportedManifestVersionAbstains(t *testing.T) {
	_, _, deps, stateDir := makeScenario(t)
	deps.Manifest.Version = 99
	out, err := audit.RunLoopBAudit(context.Background(), deps)
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditAbstained, out.Kind)
	require.Contains(t, out.Detail, "not supported")
	requireNoFile(t, filepath.Join(stateDir, "audit-findings.jsonl"))
	require.Nil(t, out.RunRecord)
}

func TestP11T21_ArtifactsAreSuppressibleByFlag(t *testing.T) {
	_, stores, deps, _ := makeScenario(t)

	// Default (nil flag): artifacts rendered.
	out, err := audit.RunLoopBAudit(context.Background(), deps)
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditRan, out.Kind)
	require.NotNil(t, out.Artifacts)
	require.Contains(t, out.Artifacts.CheckRun.Title, "advisory")
	require.NotEmpty(t, out.Artifacts.Issue)

	// A fresh fixture for the suppressed run (the cursor is per-repo state).
	suppressedFixture := createComposeFixture(t)
	suppressedStores := newComposeStores(t, suppressedFixture.stateStore, suppressedFixture.stateDir)
	suppressedStores.upsertClaims(t, suppressedFixture)
	suppressedDeps := suppressedStores.baseDeps(t, suppressedFixture)
	suppressedDeps.RenderArtifacts = boolPtr(false)
	suppressed, err := audit.RunLoopBAudit(context.Background(), suppressedDeps)
	require.NoError(t, err)
	require.Equal(t, audit.LoopBAuditRan, suppressed.Kind)
	require.Nil(t, suppressed.Artifacts)
	require.Equal(t, 1, jsonlLineCount(t, stores.store.FindingsFile()))
}

/* --------------------------------------------------------------- helpers */

// withContext returns deps with the trigger context replaced (the TS
// `{ ...deps, context }` spread).
func withContext(deps audit.LoopBAuditComposeDeps, ctx audit.LoopBTriggerContext) audit.LoopBAuditComposeDeps {
	deps.Context = ctx
	return deps
}

// boolPtr returns a pointer to b.
func boolPtr(b bool) *bool { return &b }

// snapshotTracked snapshots the tracked files' bytes (the TS readFile walk).
func snapshotTracked(t *testing.T, fixture *composeFixture, paths []string) []string {
	t.Helper()
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, trackRead(t, fixture, path))
	}
	return out
}

// trackRead reads one worktree file's bytes as a string.
func trackRead(t *testing.T, fixture *composeFixture, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixture.root, path))
	require.NoError(t, err)
	return string(raw)
}

// requireNoFile asserts the path does not exist (the TS existsSync === false).
func requireNoFile(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "expected %s to be absent (stat: %v)", path, err)
}

/* ------------------------------------------------------------- the fixture */

type composeFixture struct {
	root        string
	baselineSha string
	headSha     string
	stateDir    string
	stateStore  *state.StateStore
	claims      map[string]schemas.Claim
}

// createComposeFixture builds the deterministic two-commit repository (the TS
// createDriftFixture, ported inline): commit 1 holds the doc plus three claim
// sources, one of which also carries a helper that vanishes in commit 2;
// commit 2 touches refund.ts (claim A's backing source — its blob moves),
// adds extra.ts and modifies unrelated.ts, leaving claims B and C's sources
// untouched.
func createComposeFixture(t *testing.T) *composeFixture {
	t.Helper()
	fixture := &composeFixture{root: t.TempDir()}
	fixture.git(t, "init", "-b", "main")
	// Windows platform-port (DX-P11-3): pin LF repo-locally BEFORE any write.
	fixture.git(t, "config", "core.autocrlf", "false")
	fixture.git(t, "config", "core.eol", "lf")

	fixture.write(t, composeDocPath, strings.Join(composeDocLines, "\n"))
	fixture.write(t, composeRefundPath, composeRefundV1)
	fixture.write(t, composeRetryPath, composeRetrySrc)
	fixture.write(t, composeLedgerPath, composeLedgerS)
	fixture.write(t, composeUnrelPath, "export const unrelatedOne = 1;\n")
	fixture.baselineSha = fixture.commitAll(t, "baseline")

	fixture.write(t, composeRefundPath, composeRefundV2)
	fixture.write(t, composeUnrelPath, "export const unrelatedOne = 2;\n")
	fixture.write(t, composeExtraPath, "export const extraThing = true;\n")
	fixture.headSha = fixture.commitAll(t, "drift")

	refundBlobV1 := fixture.blobSha(t, fixture.baselineSha, composeRefundPath)
	retryBlob := fixture.blobSha(t, fixture.baselineSha, composeRetryPath)
	ledgerBlob := fixture.blobSha(t, fixture.baselineSha, composeLedgerPath)

	// Anchors are computed from the committed doc content (content-hash-first,
	// ARCH §4.3): lines are 1-addressable with the trailing newline absorbed.
	docContent := strings.Join(composeDocLines, "\n")
	anchorFor := func(line int) schemas.ClaimAnchor {
		lines := claims.SplitDocLines(docContent)
		return schemas.ClaimAnchor{
			File:        composeDocPath,
			StartLine:   line,
			EndLine:     line,
			ContentHash: claims.HashSpan(lines[line-1]),
		}
	}
	line := intPtr(1)
	at := "2026-01-02T00:00:00.000Z"
	history := func() []schemas.ClaimHistoryEvent {
		return []schemas.ClaimHistoryEvent{
			{At: at, RunID: composeRunID, Event: "intake: hand-anchored claim (audit fixture)"},
		}
	}
	fixture.claims = map[string]schemas.Claim{
		composeClaimA: {
			ID: composeClaimA, Text: composeDocLines[2], Anchor: anchorFor(3),
			Source: schemas.ClaimSource{File: composeRefundPath, Line: line, BlobSHA: refundBlobV1},
			Status: schemas.ClaimStatusVerified, History: history(),
		},
		composeClaimB: {
			ID: composeClaimB, Text: composeDocLines[4], Anchor: anchorFor(5),
			Source: schemas.ClaimSource{File: composeRetryPath, Line: line, BlobSHA: retryBlob},
			Status: schemas.ClaimStatusVerified, History: history(),
		},
		composeClaimC: {
			ID: composeClaimC, Text: composeDocLines[6], Anchor: anchorFor(7),
			Source: schemas.ClaimSource{File: composeLedgerPath, Line: line, BlobSHA: ledgerBlob},
			Status: schemas.ClaimStatusVerified, History: history(),
		},
	}

	stateDir := filepath.Join(fixture.root, schemas.DocSyncDir, "state")
	stateStore, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)
	fixture.stateStore = stateStore
	fixture.stateDir = stateDir
	return fixture
}

// claimsFor returns one fixture claim by id.
func (f *composeFixture) claimsFor(t *testing.T, id string) schemas.Claim {
	t.Helper()
	claim, ok := f.claims[id]
	require.True(t, ok, "fixture claim %s", id)
	return claim
}

// client builds the typed git client over the fixture root.
func (f *composeFixture) client(t *testing.T) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: f.root})
	require.NoError(t, err)
	return client
}

func (f *composeFixture) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.root}, args...)...)
	cmd.Env = fixtureGitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
}

func (f *composeFixture) write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(f.root, path)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, path), []byte(content), 0o644))
}

func (f *composeFixture) commitAll(t *testing.T, message string) string {
	t.Helper()
	f.git(t, "add", "-A")
	cmd := exec.Command("git", append([]string{"-C", f.root}, append(gitIdentityArgs,
		"commit", "-q", "-m", message, "--allow-empty")...)...)
	cmd.Env = fixtureGitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", out)
	return strings.TrimSpace(f.revParse(t, "HEAD"))
}

func (f *composeFixture) blobSha(t *testing.T, rev, path string) string {
	t.Helper()
	return strings.TrimSpace(f.revParse(t, rev+":"+path))
}

func (f *composeFixture) revParse(t *testing.T, rev string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.root}, "rev-parse", rev)...)
	cmd.Env = fixtureGitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git rev-parse %s failed: %s", rev, out)
	return string(out)
}

// compile-time proof the fixture's state store satisfies the claim-store
// backend (the TS ClaimStore over StateStore).
var _ claims.ClaimStoreBackend = (*state.StateStore)(nil)
