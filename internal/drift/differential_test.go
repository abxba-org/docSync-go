package drift_test

// Black-box port of packages/core/test/drift-differential.test.ts and
// test/drift-fixture.ts (P11-T16; ARCH §6.1; PRIN-2): the differential covers
// ONLY the baseline's recorded scope (no blanket scanning), and two runs on
// identical inputs are byte-identical (canonicalJson). Real system git in a
// deterministic temp fixture (createDriftFixture); zero LLM.
//
// The TS suite used the shared `drift-fixture.ts` builder; it is ported
// inline here (the drift test files share this package, so sweep_test.go
// reuses the same fixture, exactly as the TS tests did).

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/drift"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------- fixture constants */

const (
	docPath        = "docs/payments.md"
	refundPath     = "src/payments/refund.ts"
	retryPath      = "src/api/retry.ts"
	ledgerPath     = "src/payments/ledger.ts"
	unrelatedPath  = "src/payments/unrelated.ts"
	extraPath      = "src/payments/extra.ts"
	claimAID       = "claim_a"
	claimBID       = "claim_b"
	claimCID       = "claim_c"
	driftRunID     = "run_drift_fixture"
	driftAt        = "2026-01-02T00:00:00.000Z"
	driftDocHash   = "sha256:drift-fixture-doc"
	driftIntakeEvt = "intake: hand-anchored claim (drift fixture)"
)

var (
	docLines = []string{
		"# Payments",
		"",
		"Refunds require an idempotency key.",
		"",
		"Retries back off exponentially.",
		"",
		"Payments are stored in the ledger table.",
		"",
	}
	spanRefunds = docLines[2]
	spanRetries = docLines[4]
	spanLedger  = docLines[6]

	refundV1 = strings.Join([]string{
		"export function refundPayment(idempotencyKey: string): void {}",
		"",
		"function removedHelper(): void {}",
		"",
	}, "\n")
	refundV2 = strings.Join([]string{
		"export function refundPayment(request: { idempotencyKey: string }): void {}",
		"",
	}, "\n")
	retrySource = strings.Join([]string{"export function retryWithBackoff(attempt: number): void {}", ""}, "\n")
	ledgerSrc   = strings.Join([]string{"export function ledgerEntry(amount: number): void {}", ""}, "\n")
	unrelatedV1 = strings.Join([]string{"export const unrelatedOne = 1;", ""}, "\n")
	unrelatedV2 = strings.Join([]string{"export const unrelatedOne = 2;", ""}, "\n")
	extraSource = strings.Join([]string{"export const extraThing = true;", ""}, "\n")
)

/* ------------------------------------------------------- fixture plumbing */

// identityArgs pins the commit identity per invocation (the fixture env points
// GIT_CONFIG_GLOBAL at /dev/null, so no user config exists).
var identityArgs = []string{"-c", "user.name=docsync-test", "-c", "user.email=docsync-test@invalid"}

// runGit runs a raw git command outside the typed wrapper (fixtures only).
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = fixtureEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return string(out)
}

// fixtureEnv is a deterministic, isolated git environment: no user or system
// config, fixed identity and dates (TS DRIFT_GIT_ENV).
func fixtureEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"TERM=dumb",
	}
}

// gitIdentity runs a fixture git command with the pinned commit identity.
func gitIdentity(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runGit(t, dir, append(append([]string(nil), identityArgs...), args...)...)
}

// driftFixture is the shared deterministic two-commit repository (the TS
// DriftFixture): commit 1 (`baselineSha`) holds the doc plus three claim
// sources, one of which also carries a helper that vanishes in commit 2;
// commit 2 (`headSha`) touches refund.ts (claim A's backing source — its blob
// moves), adds extra.ts and modifies unrelated.ts, leaving claims B and C's
// sources untouched.
type driftFixture struct {
	root        string
	baselineSha string
	headSha     string
	claims      []schemas.Claim
	baseline    schemas.Baseline
}

func (f *driftFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	return runGit(t, f.root, args...)
}

func (f *driftFixture) gitIdentity(t *testing.T, args ...string) {
	t.Helper()
	gitIdentity(t, f.root, args...)
}

func (f *driftFixture) client(t *testing.T) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: f.root})
	require.NoError(t, err)
	return client
}

func (f *driftFixture) write(t *testing.T, relPath, content string) {
	t.Helper()
	target := filepath.Join(f.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func (f *driftFixture) blobSha(t *testing.T, commit, path string) string {
	t.Helper()
	return strings.TrimSpace(f.git(t, "rev-parse", commit+":"+path))
}

func (f *driftFixture) revParseHead(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(f.git(t, "rev-parse", "HEAD"))
}

func (f *driftFixture) commitAll(t *testing.T, message string) string {
	t.Helper()
	f.gitIdentity(t, "add", "-A")
	f.gitIdentity(t, "commit", "-q", "-m", message)
	return f.revParseHead(t)
}

// createDriftFixture builds the deterministic temp repository.
func createDriftFixture(t *testing.T) *driftFixture {
	t.Helper()
	fixture := &driftFixture{root: t.TempDir()}
	fixture.git(t, "init", "-b", "main")
	// Windows platform-port (DX-P11-3): pin LF repo-locally BEFORE any write —
	// a machine-level core.autocrlf=true would rewrite fixture files to CRLF.
	fixture.git(t, "config", "core.autocrlf", "false")
	fixture.git(t, "config", "core.eol", "lf")

	fixture.write(t, docPath, strings.Join(docLines, "\n"))
	fixture.write(t, refundPath, refundV1)
	fixture.write(t, retryPath, retrySource)
	fixture.write(t, ledgerPath, ledgerSrc)
	fixture.write(t, unrelatedPath, unrelatedV1)
	fixture.baselineSha = fixture.commitAll(t, "baseline")

	fixture.write(t, refundPath, refundV2)
	fixture.write(t, unrelatedPath, unrelatedV2)
	fixture.write(t, extraPath, extraSource)
	fixture.headSha = fixture.commitAll(t, "drift")

	refundBlobV1 := fixture.blobSha(t, fixture.baselineSha, refundPath)
	retryBlob := fixture.blobSha(t, fixture.baselineSha, retryPath)
	ledgerBlob := fixture.blobSha(t, fixture.baselineSha, ledgerPath)

	// Anchors are computed from the committed doc content (content-hash-first,
	// ARCH §4.3): lines are 1-addressable with the trailing newline absorbed.
	docContent := strings.Join(docLines, "\n")
	anchorFor := func(line int) schemas.ClaimAnchor {
		return schemas.ClaimAnchor{
			File:        docPath,
			StartLine:   line,
			EndLine:     line,
			ContentHash: claims.HashSpan(claims.SplitDocLines(docContent)[line-1]),
		}
	}

	fixture.claims = []schemas.Claim{
		{
			ID:     claimAID,
			Text:   spanRefunds,
			Anchor: anchorFor(3),
			Source: schemas.ClaimSource{File: refundPath, Line: intptr(1), BlobSHA: refundBlobV1},
			Status: schemas.ClaimStatusVerified,
			History: []schemas.ClaimHistoryEvent{
				{At: driftAt, RunID: driftRunID, Event: driftIntakeEvt},
			},
		},
		{
			ID:     claimBID,
			Text:   spanRetries,
			Anchor: anchorFor(5),
			Source: schemas.ClaimSource{File: retryPath, Line: intptr(1), BlobSHA: retryBlob},
			Status: schemas.ClaimStatusVerified,
			History: []schemas.ClaimHistoryEvent{
				{At: driftAt, RunID: driftRunID, Event: driftIntakeEvt},
			},
		},
		{
			ID:     claimCID,
			Text:   spanLedger,
			Anchor: anchorFor(7),
			Source: schemas.ClaimSource{File: ledgerPath, Line: intptr(1), BlobSHA: ledgerBlob},
			Status: schemas.ClaimStatusVerified,
			History: []schemas.ClaimHistoryEvent{
				{At: driftAt, RunID: driftRunID, Event: driftIntakeEvt},
			},
		},
	}

	fixture.baseline = schemas.Baseline{
		DocPath:        docPath,
		DocContentHash: driftDocHash,
		BaselineCommit: fixture.baselineSha,
		// Recorded scope deliberately EXCLUDES ledger.ts (claim C's source) and
		// unrelated.ts/extra.ts — the intersection assertions lean on this.
		SourceFiles:   []string{refundPath, retryPath},
		SourceSymbols: []string{"refundPayment", "removedHelper", "retryWithBackoff"},
		ClaimIDs:      []string{claimAID, claimBID},
		VerifiedAt:    driftAt,
		VerifiedByRun: driftRunID,
		PolicyVersion: 17,
	}
	return fixture
}

/* ------------------------------------------------------------- assertions */

func intptr(i int) *int { return &i }

func strptr(s string) *string { return &s }

// canonicalJSONOf renders a value as canonical JSON (the exact bytes CAS
// hashes and state files carry) so two reports compare byte-for-byte.
func canonicalJSONOf(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	require.NoError(t, dec.Decode(&generic))
	out, err := docutil.CanonicalJSON(generic)
	require.NoError(t, err)
	return out
}

func analyzeDrift(t *testing.T, fixture *driftFixture, head string) *schemas.DriftReport {
	t.Helper()
	report, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &fixture.baseline,
		Head:     head,
		Git:      fixture.client(t),
		Claims:   fixture.claims,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.NoError(t, err)
	return report
}

func factByPath(facts []schemas.DriftSourceFact, path string) *schemas.DriftSourceFact {
	for i := range facts {
		if facts[i].Path == path {
			return &facts[i]
		}
	}
	return nil
}

func claimFactByID(facts []schemas.DriftClaimFact, claimID string) *schemas.DriftClaimFact {
	for i := range facts {
		if facts[i].ClaimID == claimID {
			return &facts[i]
		}
	}
	return nil
}

/* ------------------------------------------------------------------ tests */

func TestP11T16_DifferentialIntersectsBaselineScope(t *testing.T) {
	fixture := createDriftFixture(t)
	report := analyzeDrift(t, fixture, fixture.headSha)

	// Sources: exactly the recorded scope, one modified + one unchanged
	// (code-unit sorted: 'src/api/retry.ts' < 'src/payments/refund.ts').
	require.Equal(t, []schemas.DriftSourceFact{
		{Path: retryPath, Status: schemas.DriftSourceStatusUnchanged},
		{Path: refundPath, Status: schemas.DriftSourceStatusModified, Additions: intptr(1), Deletions: intptr(3)},
	}, report.Sources)

	// Out-of-scope changes in the same range (unrelated.ts modified,
	// extra.ts added, ledger.ts untouched) NEVER appear.
	reportedPaths := make([]string, 0, len(report.Sources))
	for _, fact := range report.Sources {
		reportedPaths = append(reportedPaths, fact.Path)
	}
	require.NotContains(t, reportedPaths, unrelatedPath)
	require.NotContains(t, reportedPaths, extraPath)
	require.NotContains(t, reportedPaths, ledgerPath)

	// Symbols: only recorded names, only against CHANGED recorded files.
	// `refundPayment` survived (present), `removedHelper` vanished (absent),
	// `retryWithBackoff` lives in an unchanged file → no fact at all.
	require.Equal(t, []schemas.DriftSymbolFact{
		{Name: "refundPayment", Path: refundPath, Kind: schemas.DriftSymbolPresent},
		{Name: "removedHelper", Path: refundPath, Kind: schemas.DriftSymbolAbsent},
	}, report.Symbols)

	// Claims: exactly the recorded claim_ids (claim C is out of scope).
	claimIDs := make([]string, 0, len(report.Claims))
	for _, fact := range report.Claims {
		claimIDs = append(claimIDs, fact.ClaimID)
	}
	require.Equal(t, []string{claimAID, claimBID}, claimIDs)
	claimA := report.Claims[0]
	require.Equal(t, schemas.DriftClaimSupportMoved, claimA.Verdict)
	require.Equal(t, strptr(schemas.ClaimStatusSuspect), claimA.ProposedStatus)
	require.NotNil(t, claimA.SourceBlobSHAAtHead)
	require.NotEqual(t, claimA.SourceBlobSHA, *claimA.SourceBlobSHAAtHead)
	claimB := report.Claims[1]
	require.Equal(t, schemas.DriftClaimUnchanged, claimB.Verdict)
	require.Nil(t, claimB.ProposedStatus)

	// Summary + ladder context (D12 ladder reused verbatim: ancestor rung).
	require.Equal(t, schemas.DriftReportSummary{SourcesChanged: 1, SymbolsDrifted: 1, ClaimsDrifted: 1}, report.Summary)
	require.Equal(t, schemas.LadderRunAncestor, report.LadderRun)
	require.Equal(t, fixture.headSha, report.Head)
	require.Equal(t, fixture.baselineSha, report.BaselineCommit)
	require.Equal(t, docPath, report.DocPath)
}

func TestP11T16_DifferentialIsDeterministicAcrossTwoRuns(t *testing.T) {
	fixture := createDriftFixture(t)
	first := analyzeDrift(t, fixture, fixture.headSha)
	second := analyzeDrift(t, fixture, fixture.headSha)
	require.Equal(t, canonicalJSONOf(t, first), canonicalJSONOf(t, second))
}

func TestP11T16_ExactShaHeadYieldsAllUnchangedDifferential(t *testing.T) {
	fixture := createDriftFixture(t)
	report := analyzeDrift(t, fixture, fixture.baselineSha)

	require.Equal(t, schemas.LadderRunExactSHA, report.LadderRun)
	require.Equal(t, schemas.DriftReportSummary{SourcesChanged: 0, SymbolsDrifted: 0, ClaimsDrifted: 0}, report.Summary)
	for _, fact := range report.Claims {
		require.Equal(t, schemas.DriftClaimUnchanged, fact.Verdict)
		require.Nil(t, fact.ProposedStatus)
	}
}

func TestP11T16_DeletedRecordedSourceReportedMissing(t *testing.T) {
	fixture := createDriftFixture(t)
	// Dedicated micro-fixture: delete a recorded source after the baseline.
	fixture.git(t, "rm", "-q", retryPath)
	fixture.gitIdentity(t, "commit", "-q", "-m", "delete recorded source")
	headSha := fixture.revParseHead(t)

	report := analyzeDrift(t, fixture, headSha)

	retry := factByPath(report.Sources, retryPath)
	require.NotNil(t, retry)
	require.Equal(t, schemas.DriftSourceStatusDeleted, retry.Status)
	claimB := claimFactByID(report.Claims, claimBID)
	require.NotNil(t, claimB)
	require.Equal(t, schemas.DriftClaimSourceMissing, claimB.Verdict)
	require.Equal(t, strptr(schemas.ClaimStatusSuspect), claimB.ProposedStatus)
	require.Nil(t, claimB.SourceBlobSHAAtHead)
}
func TestP11T16_NilBaselineAndGitAreRefusedClosed(t *testing.T) {
	fixture := createDriftFixture(t)
	client := fixture.client(t)

	_, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: nil,
		Head:     fixture.headSha,
		Git:      client,
		Claims:   fixture.claims,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.EqualError(t, err,
		"EDOCSYNC_INTERNAL: analyzeDrift: baseline is nil — refusing to analyze an absent record (fail closed)")
	require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))

	_, err = drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &fixture.baseline,
		Head:     fixture.headSha,
		Git:      nil,
		Claims:   fixture.claims,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.EqualError(t, err,
		"EDOCSYNC_INTERNAL: analyzeDrift: git client is nil — refusing to analyze without git (fail closed)")
	require.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
}

func TestP11T16_BinaryChangedSourceCarriesNoCounts(t *testing.T) {
	// A changed binary file is a recorded source with no numstat counts:
	// git reports `-`/`-`, so the fact carries neither additions nor deletions
	// (never a panic, never a fabricated zero).
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "core.autocrlf", "false")
	runGit(t, root, "config", "core.eol", "lf")

	binPath := filepath.Join("assets", "logo.bin")
	writeFileBytes(t, filepath.Join(root, binPath), []byte{0x00, 0x01, 0x02, 0x03})
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "baseline")
	baselineSha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	writeFileBytes(t, filepath.Join(root, binPath), []byte{0x00, 0x01, 0x09})
	gitIdentity(t, root, "add", "-A")
	gitIdentity(t, root, "commit", "-q", "-m", "binary drift")

	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	baseline := schemas.Baseline{
		DocPath:        docPath,
		DocContentHash: driftDocHash,
		BaselineCommit: baselineSha,
		SourceFiles:    []string{binPath},
		SourceSymbols:  []string{},
		ClaimIDs:       []string{},
		VerifiedAt:     driftAt,
		VerifiedByRun:  driftRunID,
		PolicyVersion:  17,
	}
	report, err := drift.AnalyzeDrift(t.Context(), drift.AnalyzeDriftInput{
		Baseline: &baseline,
		Head:     "HEAD",
		Git:      client,
		Claims:   nil,
		RunID:    driftRunID,
		At:       driftAt,
	})
	require.NoError(t, err)
	require.Equal(t, []schemas.DriftSourceFact{
		{Path: filepath.ToSlash(binPath), Status: schemas.DriftSourceStatusModified},
	}, report.Sources)
	require.Equal(t, schemas.DriftReportSummary{SourcesChanged: 1, SymbolsDrifted: 0, ClaimsDrifted: 0}, report.Summary)
}

func writeFileBytes(t *testing.T, target string, content []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, content, 0o644))
}
