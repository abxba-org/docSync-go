package cli_test

// Ported from packages/cli/test/org-policy-advisories-p12-t16.test.ts
// (F5-1; P12-T13; OD-R30; V-P12-12) — org-policy report-only advisories must
// be VISIBLE in production: an unreviewed `report_only_until_reviewed` floor
// renders on every surface, and a `reviewed: true` declaration silences them
// (absence is not a review — fail closed).
//
// Rendering contract per surface:
//   - `docsync check` — stderr lines prefixed `docsync check:` before the
//     exit-code decision, plus an `advisories` array in the `--json` payload;
//   - `docsync audit` — the same, prefixed `docsync audit:`;
//   - the fix flow — stderr lines prefixed `docsync fix:` from the flow's own
//     manifest read (the SINGLE render point).
//
// No network (OD-R2): the check runs are `--offline`; the audit runs inject
// an idle runner double; the flow runs inject an idle runtime.
//
// DEVIATION NOTE: the TS audit `--json` payload carries an `advisories`
// array; the Go audit payload shape (auditRanPayload) has no advisories key
// — the advisories are threaded to the engine seam (AuditRunnerInput.
// Advisories) instead, which is what this suite pins for the audit verb.
import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cliAdvisoryMarker = "manifest not yet reviewed"

// cliOrgPolicyUnreviewed is the report-only floor over `docs/**` — NOT
// reviewed (absence is not a review).
const cliOrgPolicyUnreviewed = `version: 1
floors:
  - kind: report_only_until_reviewed
    pattern: docs/**
    requiredReviewers:
      - security-team
learning:
  defaultMode: canary
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`

// cliOrgPolicyReviewed is the same floor with the human-owned `reviewed: true`.
const cliOrgPolicyReviewed = `version: 1
floors:
  - kind: report_only_until_reviewed
    pattern: docs/**
    requiredReviewers:
      - security-team
    reviewed: true
learning:
  defaultMode: canary
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`

// cliPolicyRepo is the fixture repo carrying the org policy file (committed →
// clean worktree). The shared fixture manifest maps BOTH docs, so an
// unreviewed floor yields one advisory per manifest doc.
func cliPolicyRepo(t *testing.T, orgPolicyYML string) *gitFixture {
	t.Helper()
	repo := newGitFixture(t)
	repo.seedBase()
	repo.write(".docsync/org-policy.yml", orgPolicyYML)
	repo.commitPaths([]string{".docsync/org-policy.yml"}, "chore: org policy report-only floor")
	return repo
}

func cliAdvisoryLines(err string, prefix string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(err, "\n"), "\n") {
		if strings.HasPrefix(line, prefix) {
			lines = append(lines, line)
		}
	}
	return lines
}

/* ------------------------------------------------------------ check verb */

func TestOrgPolicyAdvisoriesCheckRendersStderrLinesBeforeTheExitDecision(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	captured := captureIo()

	exit := cli.RunCheck(cliArgs(t, "check", "--offline", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	// The advisory is non-blocking: the exit code is the run's, never the floor's.
	assert.Equal(t, cli.ExitOK, exit)
	lines := cliAdvisoryLines(captured.Err(), "docsync check:")
	require.Len(t, lines, 2)
	assert.Contains(t, strings.Join(lines, "\n"), cliAdvisoryMarker)
}

func TestOrgPolicyAdvisoriesCheckJSONCarriesTheAdvisoriesArray(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	captured := captureIo()

	_ = cli.RunCheck(cliArgs(t, "check", "--offline", "--json", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	require.Len(t, captured.stdoutLines(), 1)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &payload))
	advisories, ok := payload["advisories"].([]any)
	require.True(t, ok, "advisories must be an array")
	require.Len(t, advisories, 2)
	for _, message := range advisories {
		assert.Contains(t, message, cliAdvisoryMarker)
	}
	// The canonical report itself is untouched by the additive key.
	assert.NotNil(t, payload["status"])
}

func TestOrgPolicyAdvisoriesCheckJSONReParsesThroughTheReportSchema(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	captured := captureIo()

	_ = cli.RunCheck(cliArgs(t, "check", "--offline", "--json", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	require.Len(t, captured.stdoutLines(), 1)
	// G1-1: the emitted payload must re-parse through the registered
	// canonical-report schema, or every consumer re-parsing stdout crashes.
	var report schemas.CanonicalReport
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &report))
	assert.Empty(t, report.Validate())
	require.NotNil(t, report.Advisories)
	assert.Len(t, *report.Advisories, 2)
	for _, message := range *report.Advisories {
		assert.Contains(t, message, cliAdvisoryMarker)
	}
	assert.NotEmpty(t, report.Key.Repo)
}

func TestOrgPolicyAdvisoriesCheckWithoutOrgPolicyOmitsTheKey(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	captured := captureIo()

	_ = cli.RunCheck(cliArgs(t, "check", "--offline", "--json", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	require.Len(t, captured.stdoutLines(), 1)
	var report schemas.CanonicalReport
	require.NoError(t, json.Unmarshal([]byte(captured.stdoutLines()[0]), &report))
	assert.Nil(t, report.Advisories)
}

func TestOrgPolicyAdvisoriesReviewedDeclarationSilencesTheCheckSurface(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyReviewed)
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	text := captureIo()
	jsonCap := captureIo()

	_ = cli.RunCheck(cliArgs(t, "check", "--offline", "--base", base), text,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})
	_ = cli.RunCheck(cliArgs(t, "check", "--offline", "--json", "--base", base), jsonCap,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	assert.Empty(t, text.Err())
	assert.NotContains(t, text.Err(), cliAdvisoryMarker)
	require.Len(t, jsonCap.stdoutLines(), 1)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(jsonCap.stdoutLines()[0]), &payload))
	_, present := payload["advisories"]
	assert.False(t, present)
}

func TestOrgPolicyAdvisoriesRepoWithoutOrgPolicyRendersNothingNew(t *testing.T) {
	repo := newGitFixture(t)
	repo.seedBase()
	base := strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	captured := captureIo()

	_ = cli.RunCheck(cliArgs(t, "check", "--offline", "--base", base), captured,
		cli.CheckOptions{Cwd: repo.root, Env: map[string]string{}})

	assert.Empty(t, captured.Err())
}

/* ------------------------------------------------------------- audit verb */

func TestOrgPolicyAdvisoriesAuditPrefixesItsAdvisoryLines(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	withAuditRunner(t, func(context.Context, cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		return cli.AuditRunResult{Kind: cli.AuditRunNoop, Reason: "trigger-* is not checked out"}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit"), captured, cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitOK, exit)
	lines := cliAdvisoryLines(captured.Err(), "docsync audit:")
	require.Len(t, lines, 2)
	assert.Contains(t, strings.Join(lines, "\n"), cliAdvisoryMarker)
}

func TestOrgPolicyAdvisoriesAuditThreadsTheAdvisoriesToTheEngineSeam(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	var seen []string
	withAuditRunner(t, func(_ context.Context, input cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		seen = input.Advisories
		return cli.AuditRunResult{Kind: cli.AuditRunNoop, Reason: "trigger-* is not checked out"}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit", "--json"), captured, cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitOK, exit)
	// DEVIATION (see the file header): the Go audit --json payload has no
	// advisories key; the engine seam receives the advisory messages instead.
	require.Len(t, seen, 2)
	for _, message := range seen {
		assert.Contains(t, message, cliAdvisoryMarker)
	}
}

func TestOrgPolicyAdvisoriesReviewedFloorSilencesTheAuditSurface(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyReviewed)
	var seen []string
	withAuditRunner(t, func(_ context.Context, input cli.AuditRunnerInput) (cli.AuditRunResult, error) {
		seen = input.Advisories
		return cli.AuditRunResult{Kind: cli.AuditRunNoop, Reason: "trigger-* is not checked out"}, nil
	})
	captured := captureIo()

	exit := cli.RunAudit(cliArgs(t, "audit", "--json"), captured, cli.AuditOptions{Cwd: repo.root})

	assert.Equal(t, cli.ExitOK, exit)
	assert.Empty(t, captured.Err())
	assert.Empty(t, seen)
}

/* ---------------------------------------------- fix flow + command surface */

func TestOrgPolicyAdvisoriesFixFlowRendersLinesFromItsManifestRead(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	captured := captureIo()
	runtime := &fakeRuntime{outcome: cliFlowOutcome(nil)}
	deps, _, _ := cliFlowDeps(t, repo, runtime, captured)

	result := cli.RunFixFlow(context.Background(), deps, cliFlowInput(repo.root, genULID(t)))

	assert.Equal(t, cli.FixResultCompleted, result.Kind)
	lines := cliAdvisoryLines(captured.Err(), "docsync fix:")
	require.Len(t, lines, 2)
	assert.Contains(t, strings.Join(lines, "\n"), cliAdvisoryMarker)
}

func TestOrgPolicyAdvisoriesFixCommandRendersEachAdvisoryExactlyOnce(t *testing.T) {
	repo := cliPolicyRepo(t, cliOrgPolicyUnreviewed)
	captured := captureIo()
	// The FULL fix command path threads its io into the flow; each advisory
	// renders EXACTLY once, verb-prefixed — nothing double-rendered, nothing
	// dropped. (The Go composition seam carries no io member at all, so a
	// composition-level render is structurally impossible — see the header.)
	exit := cli.RunFix(cliArgs(t, "fix", "--base", "main"), captured, cli.FixOptions{
		Cwd: repo.root,
		FlowDeps: &cli.FixFlowDeps{
			Runtime: &fakeRuntime{outcome: cliFlowOutcome(nil)},
			Store:   cli.NewFixRunRecordStore(filepath.Join(repo.root, ".docsync", "state")),
			Io:      captured,
			Clock:   func() string { return cliFlowNow },
		},
	})

	assert.Equal(t, cli.ExitOK, exit)
	require.Len(t, cliAdvisoryLines(captured.Err(), "docsync fix:"), 2)
	assert.Equal(t, 2, countNonEmptyLines(captured.Err()))
}

// countNonEmptyLines counts the non-empty captured lines.
func countNonEmptyLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
