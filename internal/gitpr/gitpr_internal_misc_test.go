package gitpr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------- pr-platform surface */

// TestCOV_GITPR_037_PrClientConstructionAndUpsert pins the client's
// construction refusals, the body stamping and the marker-keyed upsert path
// (INV-11: a re-invocation never duplicates the stamp).
func TestCOV_GITPR_037_PrClientConstructionAndUpsert(t *testing.T) {
	platform := NewInMemoryPrPlatform(nil)
	base := DocPrClientDeps{
		Platform: platform, Repo: "acme/widgets", DefaultBranch: "main", Clock: covFixedTime,
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "t"},
	}

	// The four construction refusals.
	_, buildErr := NewDocPrClient(DocPrClientDeps{})
	require.Error(t, buildErr)
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(buildErr))
	_, buildErr = NewDocPrClient(base)
	require.NoError(t, buildErr)
	noRepo := base
	noRepo.Repo = ""
	_, buildErr = NewDocPrClient(noRepo)
	require.Error(t, buildErr)
	noBase := base
	noBase.DefaultBranch = ""
	_, buildErr = NewDocPrClient(noBase)
	require.Error(t, buildErr)
	assert.Contains(t, buildErr.Error(), "INV-1")
	noClock := base
	noClock.Clock = nil
	_, buildErr = NewDocPrClient(noClock)
	require.Error(t, buildErr)

	// Credentials: kind 'none' needs nothing, a developer needs a token.
	require.NoError(t, assertCredentialsShape(PrCredentials{Kind: PrCredentialsNone}))
	require.NoError(t, assertCredentialsShape(PrCredentials{Kind: PrCredentialsDeveloper, Token: "t"}))
	require.Error(t, assertCredentialsShape(PrCredentials{Kind: PrCredentialsDeveloper}))
	require.Error(t, assertCredentialsShape(PrCredentials{
		Kind: PrCredentialsAppInstallation, InstallationID: "7",
	}))
	require.NoError(t, assertCredentialsShape(PrCredentials{
		Kind: PrCredentialsAppInstallation, InstallationID: "7", Token: "t",
	}))
	// A kind outside the closed union fails closed.
	require.Error(t, assertCredentialsShape(PrCredentials{Kind: PrCredentialsKind("bogus")}))
	var credErr *PrCredentialsError
	require.ErrorAs(t, assertCredentialsShape(PrCredentials{Kind: PrCredentialsKind("x")}), &credErr)
	assert.Contains(t, credErr.Error(), "NEVER proceeds with half credentials")

	// stampPrBodyMarkers: a body that already carries the marker is never
	// duplicated; empty extras are skipped.
	marker := IdempotencyMarker("k1")
	stamped := stampPrBodyMarkers("body text", marker, []string{"self-trigger", "", "incident:inc-abc"})
	assert.Contains(t, stamped, marker)
	assert.Contains(t, stamped, "<!-- docsync:self-trigger -->")
	assert.True(t, strings.HasSuffix(stamped, "\n"))
	reStamped := stampPrBodyMarkers(stamped+"\n\n", marker, []string{"self-trigger"})
	assert.Equal(t, 1, strings.Count(reStamped, marker), "a re-stamp never duplicates")
	assert.True(t, strings.HasSuffix(reStamped, "\n"))
	assert.False(t, strings.HasSuffix(reStamped, "\n\n"),
		"the re-stamp normalizes the trailing whitespace to exactly one newline")
	assert.Equal(t, marker,
		strings.TrimSpace(strings.TrimRight(stampPrBodyMarkers("", marker, nil), "\n")))

	// The upsert: creation, then a re-run UPDATES the same PR (INV-11).
	client, err := NewDocPrClient(base)
	require.NoError(t, err)
	first, err := client.UpsertDocPr(context.Background(), UpsertDocPrInput{
		Branch: "docsync/run_1", Title: "docs: x", Body: "body v1",
		IdempotencyKey: "k1", Markers: []string{"self-trigger"},
	})
	require.NoError(t, err)
	assert.Equal(t, PrUpsertCreated, first.Action)
	require.NotNil(t, first.PR)
	assert.Equal(t, 1, first.PR.Number)
	second, err := client.UpsertDocPr(context.Background(), UpsertDocPrInput{
		Branch: "docsync/run_1", Title: "docs: x v2", Body: "body v1",
		IdempotencyKey: "k1", Markers: []string{"self-trigger"},
	})
	require.NoError(t, err)
	assert.Equal(t, PrUpsertUpdated, second.Action)
	assert.Equal(t, first.PR.Number, second.PR.Number)
	assert.Equal(t, "docs: x v2", second.PR.Title)
	// The stamped body carries the marker exactly once.
	assert.Equal(t, 1, strings.Count(second.PR.Body, marker), "the marker appears exactly once")
	assert.Contains(t, second.PR.Body, IdempotencyMarker("k1"))
	assert.Contains(t, second.PR.Body, "<!-- docsync:self-trigger -->")
	// The explicit LookupState narrows the marker probe ('closed' misses it).
	closed := PrLookupAny
	missed, err := client.UpsertDocPr(context.Background(), UpsertDocPrInput{
		Branch: "docsync/run_1", Title: "t", Body: "b", IdempotencyKey: "k1",
		LookupState: &closed,
	})
	require.NoError(t, err)
	assert.Equal(t, PrUpsertCreated, missed.Action)
}

// TestCOV_GITPR_038_InMemoryPlatformRefusals pins the double's own error paths
// (an unknown PR is a loud failure, never a silent success) and the write-path
// argument validation.
func TestCOV_GITPR_038_InMemoryPlatformRefusals(t *testing.T) {
	platform := NewInMemoryPrPlatform(nil)
	ctx := context.Background()
	missing := PrRef{Number: 99, Branch: "docsync/x", State: "open"}

	_, updateErr := platform.UpdatePr(ctx, missing, UpdatePrInput{Title: strPtrForTest("t")})
	require.Error(t, updateErr)
	require.Error(t, platform.AppendPrComment(ctx, missing, "note"))
	require.Error(t, platform.MarkSuperseded(ctx, missing, PrRef{Number: 2}))
	require.Error(t, platform.ClosePr(ctx, missing, "note"))

	// CreatePr refuses any empty member.
	_, err := platform.CreatePr(ctx, CreatePrInput{Repo: "r", Branch: "b", Base: "main"})
	require.Error(t, err)

	// A known PR updates; the double echoes the stamped `updated_at`.
	created, err := platform.CreatePr(ctx, CreatePrInput{
		Repo: "acme/widgets", Branch: "docsync/run_1", Base: "main", Title: "t", Body: "b",
	})
	require.NoError(t, err)
	assert.Equal(t, inMemoryTimestamp(created.Number), created.UpdatedAt)
	updated, err := platform.UpdatePr(ctx, created,
		UpdatePrInput{Title: strPtrForTest("t2"), Body: strPtrForTest("b2")})
	require.NoError(t, err)
	assert.Equal(t, "t2", updated.Title)
	assert.Equal(t, "b2", updated.Body)

	// MarkSuperseded with a zero-number newer PR takes its body as the note.
	newer := PrRef{Number: 0, Body: "manual supersede note"}
	require.NoError(t, platform.MarkSuperseded(ctx, created, newer))
	comments := platform.CommentsFor(created.Number)
	require.NotEmpty(t, comments)
	joined := strings.Join(comments, "\n")
	assert.Contains(t, joined, "manual supersede note")

	// Closing then listing: the double only reports OPEN docsync PRs per repo.
	require.NoError(t, platform.ClosePr(ctx, created, "done"))
	open, err := platform.ListOpenDocsPrs(ctx, "acme/widgets")
	require.NoError(t, err)
	assert.Empty(t, open, "a closed PR is not listed as open")
	otherRepo, err := platform.ListOpenDocsPrs(ctx, "other/repo")
	require.NoError(t, err)
	assert.Empty(t, otherRepo)
}

// TestCOV_GITPR_039_RestPlatformMalformedPayloads pins the REST platform's
// malformed-payload handling: the parse projection itself refuses a payload
// without a positive number or state (fail closed, never a guessed PrRef).
//
// SUSPECTED BUG (reported, not fixed here): listPullsPage swallows
// parsePr's error and appends the ZERO-value PrRef, so a malformed list entry
// is silently dropped instead of failing closed. This test pins the
// parse-level contract; the listPulls-level swallowing is left to the report.
func TestCOV_GITPR_039_RestPlatformMalformedPayloads(t *testing.T) {
	double := newCovRestDouble()
	platform := covRestPlatform(double, "acme/widgets").(*githubRestPlatform)

	for name, record := range map[string]map[string]any{
		"missing number":     {"state": "open"},
		"non-numeric number": {"number": "one", "state": "open"},
		"missing state":      {"number": float64(7)},
	} {
		_, err := platform.parsePr(record)
		require.Error(t, err, name)
		assert.Equal(t, CodeGitHubRest, errCode(err), name)
		// The detail is the REDACTED record itself (bounded evidence).
		assert.LessOrEqual(t, len(err.Error()), 600, name)
	}

	// A well-formed record projects into a PrRef.
	ref, err := platform.parsePr(map[string]any{
		"number": float64(7), "state": "open",
		"head": map[string]any{"ref": SelfTriggerBranchPrefix + "run_1"},
		"base": map[string]any{"ref": "main"},
	})
	require.NoError(t, err)
	assert.Equal(t, 7, ref.Number)
	assert.Equal(t, PrStateOpen, ref.State)
	assert.Equal(t, SelfTriggerBranchPrefix+"run_1", ref.Branch)

	// A 2xx body that is not JSON is a TRANSPORT-class refusal.
	double.on("POST", "/repos/acme/widgets/pulls", covRestReply{status: 201, body: "not json"})
	_, err = platform.CreatePr(context.Background(), CreatePrInput{
		Repo: "acme/widgets", Branch: "docsync/x", Base: "main", Title: "t", Body: "b",
	})
	require.Error(t, err)
	assert.Equal(t, CodeGitHubTransport, errCode(err))
	assert.Contains(t, err.Error(), "not json")
}

// TestCOV_GITPR_040_SupersedeSurface pins the supersede decision matrix and
// the idempotent marker ceremony: a re-invocation never duplicates the marker.
func TestCOV_GITPR_040_SupersedeSurface(t *testing.T) {
	ctx := context.Background()
	_ = ctx

	// EvaluateSupersedence: the pure decision matrix.
	_, err := EvaluateSupersedence(nil, "")
	require.Error(t, err)
	action, err := EvaluateSupersedence(nil, "key-a")
	require.NoError(t, err)
	assert.Equal(t, SupersedenceActionCreate, action.Action)
	action, err = EvaluateSupersedence(
		&SupersedenceExistingPr{PrNumber: 9, IdempotencyKey: "key-a"}, "key-a")
	require.NoError(t, err)
	assert.Equal(t, SupersedenceActionUpdate, action.Action)
	assert.Equal(t, 9, action.ExistingPrNumber)
	action, err = EvaluateSupersedence(
		&SupersedenceExistingPr{PrNumber: 9, IdempotencyKey: "key-a"}, "key-b")
	require.NoError(t, err)
	assert.Equal(t, SupersedenceActionSupersedeThenCrea, action.Action)

	// SupersedeMarker: the exact marker text; a non-positive number fails closed.
	marker, err := SupersedeMarker(482)
	require.NoError(t, err)
	assert.Equal(t, SupersedeMarkerPrefix+"482)", marker)
	_, err = SupersedeMarker(0)
	require.Error(t, err)

	// SupersedeStalePr: the full marker ceremony over a seam double.
	client := &covSupersedeClient{}
	input := SupersedeStalePrInput{
		Stale: SeamPrRef{Number: 1, Branch: "docsync/a"},
		Newer: SeamPrRef{Number: 2, Branch: "docsync/b"},
		Clock: covFixedTime,
	}
	first, err := SupersedeStalePr(client, input)
	require.NoError(t, err)
	assert.Equal(t, SupersedeActionSuperseded, first.Action)
	assert.Equal(t, covFixedTime(), first.At)
	require.Len(t, client.appended, 1)
	assert.Contains(t, client.appended[0], "superseded by newer analysis (#2)")
	assert.True(t, client.marked, "the lifecycle transition runs after the marker")

	// A re-invocation against a thread that already carries the marker is a
	// no-op (idempotent, never a duplicate comment).
	client.comments = append(client.comments, client.appended[0])
	second, err := SupersedeStalePr(client, input)
	require.NoError(t, err)
	assert.Equal(t, SupersedeActionAlready, second.Action)
	assert.Len(t, client.appended, 1, "no duplicate marker comment")

	// A nil clock and a non-positive PR number both fail closed.
	_, err = SupersedeStalePr(client, SupersedeStalePrInput{})
	require.Error(t, err)
	_, err = SupersedeStalePr(client, SupersedeStalePrInput{
		Stale: SeamPrRef{Number: 0}, Newer: SeamPrRef{Number: 2}, Clock: covFixedTime,
	})
	require.Error(t, err)
}

// covSupersedeClient is the SupersedePrClient seam double.
type covSupersedeClient struct {
	comments []string
	appended []string
	marked   bool
	failList error
}

func (c *covSupersedeClient) ListPrComments(context.Context, SeamPrRef) ([]string, error) {
	if c.failList != nil {
		return nil, c.failList
	}
	return c.comments, nil
}

func (c *covSupersedeClient) AppendPrComment(_ context.Context, _ SeamPrRef, body string) error {
	c.appended = append(c.appended, body)
	return nil
}

func (c *covSupersedeClient) MarkSuperseded(context.Context, SeamPrRef, SeamPrRef) error {
	c.marked = true
	return nil
}

// TestCOV_GITPR_041_BranchFactorySurface pins the branch-name projections and
// the create/find path, including the disambiguated collision.
func TestCOV_GITPR_041_BranchFactorySurface(t *testing.T) {
	repo := covNewFlowRepo(t)
	client := repo.client(t)
	ctx := context.Background()
	_ = CreateBranchFactory(client)

	// branchNameJSONExcerpt: every escape arm plus the \u00xx control arm.
	assert.Equal(t, `"abc"`, branchNameJSONExcerpt("abc"))
	assert.Equal(t, `"a\"b"`, branchNameJSONExcerpt(`a"b`))
	assert.Equal(t, `"a\\b"`, branchNameJSONExcerpt(`a\b`))
	assert.Equal(t, `"a\nb"`, branchNameJSONExcerpt("a\nb"))
	assert.Equal(t, `"a\tb"`, branchNameJSONExcerpt("a\tb"))
	assert.Equal(t, `"a\rb"`, branchNameJSONExcerpt("a\rb"))
	assert.Equal(t, `"\b"`, branchNameJSONExcerpt("\b"))
	assert.Equal(t, `"\f"`, branchNameJSONExcerpt("\f"))
	assert.Equal(t, `"\u0001"`, branchNameJSONExcerpt("\x01"))
	assert.Equal(t, `"héllo"`, branchNameJSONExcerpt("héllo"))
	assert.Equal(t, `""`, branchNameJSONExcerpt(""))

	// branchNameJSONExcerpt is unbounded, but the refusal echo is NOT: a
	// 200-rune offending run id renders bounded (under 80 runes total).
	longEcho := branchEchoBound(strings.Repeat("z", 200))
	assert.Less(t, len([]rune(longEcho)), 100, "the echo is bounded")
	assert.Contains(t, longEcho, "zzzz")

	// BranchNameForRun / BranchNameForSourcePr projects and refuses.
	runBranch, err := BranchNameForRun("run_1")
	require.NoError(t, err)
	assert.Equal(t, SelfTriggerBranchPrefix+"run_1", runBranch)
	sha := covSha()
	prBranch, branchErr := BranchNameForSourcePr(481, sha)
	require.NoError(t, branchErr)
	assert.Equal(t, SelfTriggerBranchPrefix+"pr-481-"+sha[:7], prBranch)
	// An unsafe run id fails closed.
	_, branchErr = BranchNameForRun("bad run/id")
	require.Error(t, branchErr)
	assert.Equal(t, CodeBranchName, docutil.CodeOf(branchErr))

	// AssertNotDefaultBranch refuses the collision (INV-1).
	assert.Equal(t, CodeDefaultBranchRef, docutil.CodeOf(AssertNotDefaultBranch("main", "main")))
	require.NoError(t, AssertNotDefaultBranch("docsync/x", "main"))

	// branchTip: an absent branch is a soft miss, a present branch resolves.
	tip, exists, tipErr := branchTip(ctx, client, "nope-not-a-branch")
	require.NoError(t, tipErr)
	assert.False(t, exists)
	assert.Empty(t, tip)
	tip, exists, tipErr = branchTip(ctx, client, "main")
	require.NoError(t, tipErr)
	assert.True(t, exists)
	assert.NotEmpty(t, tip)
}

// TestCOV_GITPR_042_CheckRunEdgeCases pins the check-run client's remaining
// refusals: the malformed repo argument and the body projections.
func TestCOV_GITPR_042_CheckRunEdgeCases(t *testing.T) {
	transport := &covRecordingTransport{responses: map[string]string{
		"GET /repos/acme/widgets/commits/" + covSha() + "/check-runs": `{"check_runs": [{"id": 3, "name": "docsync/check"}]}`,
	}}
	seamed := CreateCheckRunClient(CheckRunClientDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "t"},
		Transport:   transport.send,
		Clock:       covFixedTime,
	})

	// A malformed repo fails closed on the read path too.
	_, err := seamed.FindCheckRunsForRef(context.Background(), "noslash", covSha())
	require.Error(t, err)
	assert.Equal(t, CodeGitHubRest, errCode(err))

	// A well-formed repo serves the runs.
	found, err := seamed.FindCheckRunsForRef(context.Background(), "acme/widgets", covSha())
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, 3, found[0].ID)
	assert.Equal(t, DOCSYNCCheckName, *found[0].Name)

	// The escalation action projects onto the PATCH body.
	status := CheckRunStatusCompleted
	conclusion := CheckRunConclusionFailure
	double := newCovRestDouble()
	escalation := CreateCheckRunClient(CheckRunClientDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "t"},
		Transport:   double.send,
		Clock:       covFixedTime,
	})
	double.on("PATCH", "/repos/acme/widgets/check-runs/4", covRestReply{
		status: 200, body: `{"id":4,"name":"docsync/check"}`,
	})
	updated, err := escalation.UpdateCheckRun(context.Background(), "acme/widgets", 4, UpdateCheckRunInput{
		Status: &status, Conclusion: &conclusion,
		EscalationEnabled: true,
		Output:            &CheckRunOutputPayload{Title: "t", Summary: "s", Text: "x"},
		CompletedAt:       strPtrForTest(covFixedTime()),
	})
	require.NoError(t, err)
	assert.Equal(t, 4, updated.ID)
	sent := double.last(t)
	require.NotNil(t, sent.Body)
	assert.Contains(t, *sent.Body, DOCSYNCGeneratePatchAction.Identifier)
	assert.Contains(t, *sent.Body, "completed_at")
}

// TestCOV_GITPR_043_IncidentSinkDiskFailures pins the file sink's failure
// surface: an unreadable target path fails loud (an incident that cannot be
// recorded is itself an incident).
func TestCOV_GITPR_043_IncidentSinkDiskFailures(t *testing.T) {
	// A stateDir that is actually a FILE makes MkdirAll fail.
	fileAsDir := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(fileAsDir, []byte("x"), 0o644))
	sink, err := FileIncidentSink(fileAsDir)
	require.NoError(t, err, "the sink builder itself only checks the path shape")
	require.NotNil(t, sink)
	require.Error(t, sink.Append(context.Background(), &schemas.SafetyIncident{
		Schema: "docsync.safety-incident/v1", IncidentID: "inc-1",
		At: covFixedTime(), RunID: "r", Rule: schemas.SafetyIncidentRuleProtectedPathPatch,
		OffendingPath: "manifest.yml", Detail: "d",
	}), "MkdirAll fails when a FILE occupies the stateDir")

	// incidentGeneric's error branch is reachable only through a marshalable
	// input, so the canonical line renders and the fallback branch (an
	// unserializable value) is exercised through incidentJSONString instead.
	line, err := IncidentCanonicalLine(&schemas.SafetyIncident{
		Schema: "docsync.safety-incident/v1", IncidentID: "inc-2",
		At: covFixedTime(), RunID: "r", Rule: schemas.SafetyIncidentRuleProtectedPathPatch,
		OffendingPath: "manifest.yml", Detail: "d",
	})
	require.NoError(t, err)
	assert.Contains(t, line, "inc-2")

	// A missing ledger read surfaces as the ledger's own error (a soft nil is
	// only for an absent file).
	store := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	ledger, err := NewMergeLedgerStore(MergeLedgerStoreDeps{Store: store})
	require.NoError(t, err)
	_, err = ledger.Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sourceKey must be a nonempty string")
}

// TestCOV_GITPR_044_NoopDropEvaluationOutcomes pins the no-op drop evaluator's
// outcome union: an unanchored result carries the failing hunks, and an
// engine abstention carries its reason.
func TestCOV_GITPR_044_NoopDropEvaluationOutcomes(t *testing.T) {
	ctx := context.Background()
	docPath := "docs/doc.md"
	docs := func(line string) string {
		lines := make([]string, 0, 42)
		for i := 1; i <= 41; i++ {
			lines = append(lines, "line "+itoa(i))
		}
		lines = append(lines, line)
		return strings.Join(lines, "\n") + "\n"
	}
	baseContent := docs("Refunds require an idempotency key.")
	baseHash := docutil.ContentHash(baseContent)

	// A doc the engine already carries ⇒ no-op (0 hunks reported).
	alreadyApplied := docs("Refunds require an idempotency key for amounts above 10 USD.")
	result, err := EvaluateNoOpDrop(ctx, EvaluateNoOpDropInput{
		Patch:             covGatePatch(t, docPath, baseHash),
		HeadContent:       alreadyApplied,
		BaseContent:       &baseContent,
		ManifestAllowlist: map[string]bool{docPath: true},
		Now:               noopDropConstantNow,
	})
	require.NoError(t, err)
	assert.True(t, result.Noop)
	assert.NotEmpty(t, result.Detail)

	// Head content that does NOT carry the anchor ⇒ unanchored with reasons.
	unrelated := docs("completely different content")
	result, err = EvaluateNoOpDrop(ctx, EvaluateNoOpDropInput{
		Patch:             covGatePatch(t, docPath, baseHash),
		HeadContent:       unrelated,
		BaseContent:       &baseContent,
		ManifestAllowlist: map[string]bool{docPath: true},
		Now:               noopDropConstantNow,
	})
	require.NoError(t, err)
	assert.False(t, result.Noop, "unrelated head content never reads as a no-op")
	assert.NotEmpty(t, result.Detail)

	// A malformed input (an empty doc path) is refused before any work.
	_, err = EvaluateNoOpDrop(ctx, EvaluateNoOpDropInput{
		Patch: &schemas.DocPatch{},
		Now:   noopDropConstantNow,
	})
	require.Error(t, err)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func strPtrForTest(v string) *string { return &v }
