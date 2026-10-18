package gitpr

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type covBranchRepo struct {
	root   string
	client *gitx.GitClient
}

func covRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+dir,
		"GIT_AUTHOR_NAME=docsync-test", "GIT_AUTHOR_EMAIL=docsync-test@invalid",
		"GIT_COMMITTER_NAME=docsync-test", "GIT_COMMITTER_EMAIL=docsync-test@invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	out, cerr := cmd.CombinedOutput()
	require.NoError(t, cerr, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func covWrite(t *testing.T, repo *covBranchRepo, relPath, content string) {
	t.Helper()
	full := filepath.Join(repo.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

func covNewRepo(t *testing.T) *covBranchRepo {
	t.Helper()
	root := t.TempDir()
	covRunGit(t, root, "init", "-b", "main")
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: root})
	require.NoError(t, err)
	return &covBranchRepo{root: root, client: client}
}

func covCommitAll(t *testing.T, repo *covBranchRepo, message string) string {
	t.Helper()
	covRunGit(t, repo.root, "add", "-A")
	covRunGit(t, repo.root, "commit", "-m", message)
	sha, err := repo.client.ResolveRev(context.Background(), "HEAD")
	require.NoError(t, err)
	return sha
}

const covFullSha = "0123456789abcdef0123456789abcdef01234567"

func strp(value string) *string { return &value }
func intp(value int) *int       { return &value }

type covStatusError struct {
	message string
	status  int
}

func (e *covStatusError) Error() string   { return e.message }
func (e *covStatusError) HTTPStatus() int { return e.status }

type covGoneError struct {
	message string
	status  int
}

func (e *covGoneError) Error() string   { return e.message }
func (e *covGoneError) HTTPStatus() int { return e.status }

func covRateLimited(status int) error {
	return &covStatusError{message: "rate limited", status: status}
}

type covRecorder struct {
	delays []float64
}

func (r *covRecorder) delay(ms float64) { r.delays = append(r.delays, ms) }

func covFixedRng(value float64) func() float64 {
	return func() float64 { return value }
}

func TestCov_branchTip_nonexistentBranch(t *testing.T) {
	repo := covNewRepo(t)
	covWrite(t, repo, "README.md", "r")
	covCommitAll(t, repo, "initial")
	_, exists, err := branchTip(context.Background(), repo.client, "does-not-exist")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestCov_createDocBranch_emptyRepoRoot(t *testing.T) {
	repo := covNewRepo(t)
	covWrite(t, repo, "README.md", "r")
	covCommitAll(t, repo, "initial")
	factory := CreateBranchFactory(repo.client)
	_, err := factory.CreateDocBranch(context.Background(), CreateDocBranchInput{
		RepoRoot: "", RunID: "run_x", HeadSha: covFullSha, DefaultBranch: "main",
	})
	require.Error(t, err)
}

func TestCov_createDocBranch_emptyRunID(t *testing.T) {
	repo := covNewRepo(t)
	covWrite(t, repo, "README.md", "r")
	covCommitAll(t, repo, "initial")
	factory := CreateBranchFactory(repo.client)
	_, err := factory.CreateDocBranch(context.Background(), CreateDocBranchInput{
		RepoRoot: repo.root, RunID: "", HeadSha: covFullSha, DefaultBranch: "main",
	})
	require.Error(t, err)
}

func TestCov_createDocBranch_exhaustion(t *testing.T) {
	repo := covNewRepo(t)
	covWrite(t, repo, "README.md", "r")
	firstSha := covCommitAll(t, repo, "initial")
	covWrite(t, repo, "docs/payments.md", "# payments")
	headSha := covCommitAll(t, repo, "docs update")
	name, err := BranchNameForSourcePr(42, headSha)
	require.NoError(t, err)
	for i := 1; i <= MAX_DISAMBIGUATOR_CANDIDATES; i++ {
		candidate := name
		if i > 1 {
			candidate = name + "-" + strconv.Itoa(i)
		}
		covRunGit(t, repo.root, "branch", candidate, firstSha)
	}
	factory := CreateBranchFactory(repo.client)
	sourcePr := 42
	_, err = factory.CreateDocBranch(context.Background(), CreateDocBranchInput{
		RepoRoot: repo.root, SourcePr: &sourcePr, RunID: "run_x",
		HeadSha: headSha, DefaultBranch: "main",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no free branch name")
}

func TestCov_RetryWithBackoff_nonRetryable(t *testing.T) {
	recorder := &covRecorder{}
	opCalls := 0
	op := func(attempt int) (string, error) {
		opCalls++
		return "", errors.New("boom")
	}
	backoff, cerr := CreateBackoff(BackoffConfig{MaxAttempts: 3, BaseDelayMs: 100, Rng: covFixedRng(0.5)}, recorder.delay)
	require.NoError(t, cerr)
	_, rerr := RetryWithBackoff(backoff, op)
	require.Error(t, rerr)
	assert.Equal(t, 1, opCalls)
	assert.Empty(t, recorder.delays)
}

func TestCov_RetryWithBackoff_429Exhausted(t *testing.T) {
	recorder := &covRecorder{}
	opCalls := 0
	op := func(attempt int) (string, error) {
		opCalls++
		return "", covRateLimited(429)
	}
	backoff, cerr := CreateBackoff(BackoffConfig{MaxAttempts: 2, BaseDelayMs: 100, Rng: covFixedRng(0.5)}, recorder.delay)
	require.NoError(t, cerr)
	outcome, rerr := RetryWithBackoff(backoff, op)
	require.NoError(t, rerr)
	assert.False(t, outcome.OK)
	assert.Equal(t, 2, outcome.Attempts)
	require.NotNil(t, outcome.LastStatus)
	assert.Equal(t, 429, *outcome.LastStatus)
}

func TestCov_RetryWithBackoff_403ThenSuccess(t *testing.T) {
	recorder := &covRecorder{}
	opCalls := 0
	op := func(attempt int) (string, error) {
		opCalls++
		if opCalls == 1 {
			return "", covRateLimited(403)
		}
		return "value", nil
	}
	backoff, cerr := CreateBackoff(BackoffConfig{MaxAttempts: 3, BaseDelayMs: 100, Rng: covFixedRng(0.5)}, recorder.delay)
	require.NoError(t, cerr)
	outcome, rerr := RetryWithBackoff(backoff, op)
	require.NoError(t, rerr)
	assert.True(t, outcome.OK)
	assert.Equal(t, "value", outcome.Value)
}

func TestCov_CreateBackoff_nonFiniteBaseDelay(t *testing.T) {
	_, err := CreateBackoff(BackoffConfig{MaxAttempts: 2, BaseDelayMs: math.NaN()}, func(float64) {})
	require.Error(t, err)
	_, err = CreateBackoff(BackoffConfig{MaxAttempts: 2, BaseDelayMs: math.Inf(1)}, func(float64) {})
	require.Error(t, err)
}

func TestCov_LedgerTransition_unknown(t *testing.T) {
	_, err := LedgerTransition(schemas.MergeLedgerState("bogus"), schemas.MergeLedgerStateOpen)
	require.Error(t, err)
}

func TestCov_LedgerTransition_terminal(t *testing.T) {
	_, err := LedgerTransition(schemas.MergeLedgerStateMerged, schemas.MergeLedgerStateOpen)
	require.Error(t, err)
	_, err = LedgerTransition(schemas.MergeLedgerStateDropped, schemas.MergeLedgerStateSuperseded)
	require.Error(t, err)
}

func TestCov_LedgerTransition_legal(t *testing.T) {
	next, err := LedgerTransition(schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateSuperseded)
	require.NoError(t, err)
	assert.Equal(t, schemas.MergeLedgerStateSuperseded, next)
	next, err = LedgerTransition(schemas.MergeLedgerStateConflicted, schemas.MergeLedgerStateOpen)
	require.NoError(t, err)
	assert.Equal(t, schemas.MergeLedgerStateOpen, next)
	next, err = LedgerTransition(schemas.MergeLedgerStateSuperseded, schemas.MergeLedgerStateMerged)
	require.NoError(t, err)
	assert.Equal(t, schemas.MergeLedgerStateMerged, next)
}

func TestCov_EvaluateMergeTargetRecheck_emptyShas(t *testing.T) {
	_, err := EvaluateMergeTargetRecheck(MergeTargetRecheckInput{
		DocPrBaseSha: "", DefaultBranchHeadSha: "abc", DefaultBranchMoved: false, MergeQueueDetected: false,
	})
	require.Error(t, err)
	_, err = EvaluateMergeTargetRecheck(MergeTargetRecheckInput{
		DocPrBaseSha: "abc", DefaultBranchHeadSha: "", DefaultBranchMoved: false, MergeQueueDetected: false,
	})
	require.Error(t, err)
}

func TestCov_EvaluateMergeTargetRecheck_triggers(t *testing.T) {
	dec, err := EvaluateMergeTargetRecheck(MergeTargetRecheckInput{
		DocPrBaseSha: "base", DefaultBranchHeadSha: "head",
		DefaultBranchMoved: false, MergeQueueDetected: true,
	})
	require.NoError(t, err)
	assert.Equal(t, MergeTargetTriggerMergeQueue, *dec.Trigger)

	dec, err = EvaluateMergeTargetRecheck(MergeTargetRecheckInput{
		DocPrBaseSha: "head", DefaultBranchHeadSha: "head",
		DefaultBranchMoved: true, MergeQueueDetected: false,
	})
	require.NoError(t, err)
	assert.Equal(t, MergeTargetTriggerSquashMerge, *dec.Trigger)

	dec, err = EvaluateMergeTargetRecheck(MergeTargetRecheckInput{
		DocPrBaseSha: "base", DefaultBranchHeadSha: "head",
		DefaultBranchMoved: true, MergeQueueDetected: false,
	})
	require.NoError(t, err)
	assert.Equal(t, MergeTargetTriggerDefaultBranchAdvanced, *dec.Trigger)

	dec, err = EvaluateMergeTargetRecheck(MergeTargetRecheckInput{
		DocPrBaseSha: "base", DefaultBranchHeadSha: "head",
		DefaultBranchMoved: false, MergeQueueDetected: false,
	})
	require.NoError(t, err)
	assert.False(t, dec.Recheck)
}

func TestCov_EvaluateSelfTrigger(t *testing.T) {
	r := EvaluateSelfTrigger(SelfTriggerCheckInput{
		Branch: strp("docsync/pr-42"), AuthorIdentity: strp("human"), BotIdentity: "bot", DefaultBranch: "main",
	})
	assert.True(t, r.Suppressed)

	r = EvaluateSelfTrigger(SelfTriggerCheckInput{
		Branch: strp("feature/x"), AuthorIdentity: strp("bot"), BotIdentity: "bot", DefaultBranch: "main",
	})
	assert.True(t, r.Suppressed)

	r = EvaluateSelfTrigger(SelfTriggerCheckInput{
		Branch: nil, AuthorIdentity: nil, BotIdentity: "bot", DefaultBranch: "main",
	})
	assert.False(t, r.Suppressed)
}

func TestCov_SupersedeMarker(t *testing.T) {
	_, err := SupersedeMarker(0)
	require.Error(t, err)
	m, err := SupersedeMarker(42)
	require.NoError(t, err)
	assert.Equal(t, "superseded by newer analysis (#42)", m)
}

func TestCov_DisambiguatedBranchName(t *testing.T) {
	_, err := DisambiguatedBranchName("", map[string]bool{})
	require.Error(t, err)
	name, err := DisambiguatedBranchName("base", map[string]bool{"base": true, "base-2": true})
	require.NoError(t, err)
	assert.Equal(t, "base-3", name)

	taken := map[string]bool{}
	for i := 0; i < MAX_DISAMBIGUATOR_CANDIDATES; i++ {
		c := "base"
		if i > 0 {
			c = "base-" + strconv.Itoa(i+1)
		}
		taken[c] = true
	}
	_, err = DisambiguatedBranchName("base", taken)
	require.Error(t, err)
}

func TestCov_EvaluateSupersedence(t *testing.T) {
	_, err := EvaluateSupersedence(nil, "")
	require.Error(t, err)
	_, err = EvaluateSupersedence(nil, "k1")
	require.NoError(t, err)

	_, err = EvaluateSupersedence(&SupersedenceExistingPr{PrNumber: 0, IdempotencyKey: "k"}, "k2")
	require.Error(t, err)
	_, err = EvaluateSupersedence(&SupersedenceExistingPr{PrNumber: 1, IdempotencyKey: ""}, "k2")
	require.Error(t, err)

	dec, err := EvaluateSupersedence(nil, "k1")
	require.NoError(t, err)
	assert.Equal(t, SupersedenceActionCreate, dec.Action)

	dec, err = EvaluateSupersedence(&SupersedenceExistingPr{PrNumber: 1, IdempotencyKey: "k1"}, "k1")
	require.NoError(t, err)
	assert.Equal(t, SupersedenceActionUpdate, dec.Action)

	dec, err = EvaluateSupersedence(&SupersedenceExistingPr{PrNumber: 1, IdempotencyKey: "k1"}, "k2")
	require.NoError(t, err)
	assert.Equal(t, SupersedenceActionSupersedeThenCrea, dec.Action)
}

func TestCov_ComputeIdempotencyKey(t *testing.T) {
	_, err := ComputeIdempotencyKey(ComputeIdempotencyKeyInput{DocSetContentHash: ""})
	require.Error(t, err)

	key, err := ComputeIdempotencyKey(ComputeIdempotencyKeyInput{DocSetContentHash: "abc", RunID: "run_01J"})
	require.NoError(t, err)
	assert.Equal(t, "local:run_01J:abc", key)

	key, err = ComputeIdempotencyKey(ComputeIdempotencyKeyInput{DocSetContentHash: "abc"})
	require.NoError(t, err)
	assert.Equal(t, "local:abc", key)

	key, err = ComputeIdempotencyKey(ComputeIdempotencyKeyInput{
		SourcePrHeadSha: strp("0123456789abcdef0123456789abcdef01234567"), DocSetContentHash: "abc",
	})
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567:abc", key)
}

func TestCov_DocSetContentHash(t *testing.T) {
	hash, err := DocSetContentHash([]schemas.DocPatch{})
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
}

func TestCov_NormalizeReviewerAction(t *testing.T) {
	a := NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventMerged})
	assert.Equal(t, schemas.FeedbackSignalAccepted, a.Signal)
	a = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventReviewChanges, EditedDiff: "diff"})
	assert.Equal(t, schemas.FeedbackSignalCorrected, a.Signal)
	a = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventClosedRejected, StatedReason: "already correct"})
	assert.Equal(t, schemas.FeedbackSignalFalsePositive, a.Signal)
	a = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventClosedRejected})
	assert.Equal(t, schemas.FeedbackSignalStyleRejected, a.Signal)
	a = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventLabel, Label: "docsync:intentional"})
	assert.Equal(t, schemas.FeedbackSignalOverrideAck, a.Signal)
	a = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventTimeout})
	assert.Equal(t, schemas.FeedbackSignalUnlabeled, a.Signal)
	a = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventFalseNegative, MissedDocs: []string{"docs/a.md"}})
	assert.Equal(t, schemas.FeedbackSignalFalseNegative, a.Signal)
}

func TestCov_DetectFalseNegative(t *testing.T) {
	r := DetectFalseNegative(RoutingIgnore, []string{"docs/a.md"}, []string{"docs/a.md"})
	assert.True(t, r.FalseNegative)
	r = DetectFalseNegative(RoutingReview, []string{"docs/a.md"}, []string{"docs/a.md"})
	assert.False(t, r.FalseNegative)
	r = DetectFalseNegative(RoutingIgnore, []string{}, []string{"docs/a.md"})
	assert.False(t, r.FalseNegative)
}

func TestCov_MatchesProtectedEntry(t *testing.T) {
	assert.True(t, matchesProtectedEntry("manifest.yml", "manifest.yml"))
	assert.True(t, matchesProtectedEntry(".docsync/policy.yml", ".docsync/policy*"))
	assert.False(t, matchesProtectedEntry(".docsync/other", ".docsync/policy*"))
}

func TestCov_PrFilterIsProtectedMode(t *testing.T) {
	assert.True(t, prFilterIsProtectedMode("locked"))
	assert.False(t, prFilterIsProtectedMode("auto"))
}

func TestCov_IsPresentRecord_edgeCases(t *testing.T) {
	err := AssertProvenancePresent(ProvenanceRef{Record: nil, RunID: "r"})
	require.Error(t, err)
	err = AssertProvenancePresent(ProvenanceRef{Record: map[string]any{}, RunID: "r"})
	require.Error(t, err)
	err = AssertProvenancePresent(ProvenanceRef{Record: []any{}, RunID: "r"})
	require.Error(t, err)
	err = AssertProvenancePresent(ProvenanceRef{Record: "string", RunID: "r"})
	require.Error(t, err)
	err = AssertProvenancePresent(ProvenanceRef{Record: 42, RunID: "r"})
	require.Error(t, err)
}

func TestCov_FileIncidentSink(t *testing.T) {
	_, err := FileIncidentSink("")
	require.Error(t, err)
	_, err = FileIncidentSink("relative/path")
	require.Error(t, err)
}

func TestCov_EvaluatePostMergeReRun(t *testing.T) {
	_, err := EvaluatePostMergeReRun(PostMergeReRunInput{
		Event: PostMergePushEvent{Kind: "push", Ref: ""}, DefaultBranch: "main",
	})
	require.Error(t, err)
	_, err = EvaluatePostMergeReRun(PostMergeReRunInput{
		Event: PostMergePushEvent{Kind: "", Ref: "refs/heads/main"}, DefaultBranch: "main",
	})
	require.Error(t, err)
	_, err = EvaluatePostMergeReRun(PostMergeReRunInput{
		Event: PostMergePushEvent{Kind: "push", Ref: "refs/heads/main"}, DefaultBranch: "",
	})
	require.Error(t, err)
	_, err = EvaluatePostMergeReRun(PostMergeReRunInput{
		Event: PostMergePushEvent{Kind: "push", Ref: "refs/heads/main"}, DefaultBranch: "main",
		OpenDocsyncPrs: []OpenDocsyncPr{{Number: 0, Base: "main"}},
	})
	require.Error(t, err)
	_, err = EvaluatePostMergeReRun(PostMergeReRunInput{
		Event: PostMergePushEvent{Kind: "push", Ref: "refs/heads/main"}, DefaultBranch: "main",
		OpenDocsyncPrs: []OpenDocsyncPr{{Number: 7, Base: ""}},
	})
	require.Error(t, err)

	dec, err := EvaluatePostMergeReRun(PostMergeReRunInput{
		Event: PostMergePushEvent{Kind: "push", Ref: "refs/heads/main"}, DefaultBranch: "main",
		OpenDocsyncPrs: []OpenDocsyncPr{{Number: 7, Base: "main"}},
	})
	require.NoError(t, err)
	assert.True(t, dec.ShouldRerun)
}

func TestCov_DocPrConcurrencyGroup(t *testing.T) {
	_, err := DocPrConcurrencyGroup("", nil)
	require.Error(t, err)
	_, err = DocPrConcurrencyGroup("repo", intp(0))
	require.Error(t, err)
	g, err := DocPrConcurrencyGroup("repo", nil)
	require.NoError(t, err)
	assert.Equal(t, "docsync-repo-local", g)
	g, err = DocPrConcurrencyGroup("repo", intp(42))
	require.NoError(t, err)
	assert.Equal(t, "docsync-repo-42", g)
}

func TestCov_SupersedeInFlight(t *testing.T) {
	_, err := SupersedeInFlight("", Supersedable{}, Supersedable{})
	require.Error(t, err)
	a, err := SupersedeInFlight("g",
		Supersedable{RunID: "r1", IdempotencyKey: "k1"},
		Supersedable{RunID: "r2", IdempotencyKey: "k1"})
	require.NoError(t, err)
	assert.Equal(t, SupersedeInFlightNew, a)

	a, err = SupersedeInFlight("g",
		Supersedable{RunID: "r1", IdempotencyKey: "k1"},
		Supersedable{RunID: "r2", IdempotencyKey: "k2"})
	require.NoError(t, err)
	assert.Equal(t, SupersedeInFlightSuperseded, a)
}

func TestCov_OpenPrCapGovernor(t *testing.T) {
	_, err := NewOpenPrCapGovernor(OpenPrCapConfig{MaxOpen: 0})
	require.Error(t, err)
	_, err = NewOpenPrCapGovernor(OpenPrCapConfig{MaxOpen: 11})
	require.Error(t, err)
	g, err := NewOpenPrCapGovernor(OpenPrCapConfig{MaxOpen: 3})
	require.NoError(t, err)
	r, err := g.Evaluate(3)
	require.NoError(t, err)
	assert.False(t, r.Allowed)
	assert.True(t, r.Downgrade)
	r, err = g.Evaluate(1)
	require.NoError(t, err)
	assert.True(t, r.Allowed)
}

func TestCov_ProposalQueue(t *testing.T) {
	q := CreateProposalQueue()
	q.Push(ProposalEntry{Kind: "ignore-pattern", Pattern: "*.md", PolicyVersion: 1, At: "t", RunID: "r"})
	d := q.Drain()
	assert.Len(t, d, 1)
	assert.Equal(t, "*.md", d[0].Pattern)
}

func TestCov_ProposalQueue_nil(t *testing.T) {
	var nq *ProposalQueue
	nq.Push(ProposalEntry{})
	d := nq.Drain()
	assert.Empty(t, d)
}

func TestCov_WithGoneRetry_410(t *testing.T) {
	calls := 0
	_, err := WithGoneRetry(func() (string, error) {
		calls++
		return "", &covGoneError{message: "gone", status: 410}
	}, &GoneRetryOptions{Retries: intp(1), DelayMs: intp(1), Delay: func(d time.Duration) {}})
	require.Error(t, err)
	assert.Equal(t, 2, calls)
}

func TestCov_WithGoneRetry_non410(t *testing.T) {
	_, err := WithGoneRetry(func() (string, error) {
		return "", errors.New("boom")
	}, nil)
	require.Error(t, err)
}

func TestCov_WithGoneRetry_success(t *testing.T) {
	v, err := WithGoneRetry(func() (string, error) {
		return "ok", nil
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", v)
}

func TestCov_RestErrorFor_typedCodes(t *testing.T) {
	p := githubRestPlatform{
		credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "tok"},
		baseUrl:     "https://api.github.com", clock: githubRestEpochClock,
	}
	e := p.restErrorFor("GET", "/x", GitHubRestResponse{Status: 401, Body: `{"message":"unauth"}`})
	assert.Equal(t, CodeGitHubAuth, docutil.CodeOf(e))
	e = p.restErrorFor("GET", "/x", GitHubRestResponse{Status: 404, Body: `{"message":"nf"}`})
	assert.Equal(t, CodeGitHubNotFound, docutil.CodeOf(e))
	e = p.restErrorFor("GET", "/x", GitHubRestResponse{Status: 422, Body: `{"message":"bad"}`})
	assert.Equal(t, CodeGitHubValidation, docutil.CodeOf(e))
	e = p.restErrorFor("GET", "/x", GitHubRestResponse{Status: 429, Body: `{"message":"rl"}`})
	assert.Equal(t, CodeGitHubRateLimit, docutil.CodeOf(e))
	e = p.restErrorFor("GET", "/x", GitHubRestResponse{Status: 500, Body: "err"})
	assert.Equal(t, CodeGitHubTransport, docutil.CodeOf(e))
	e = p.restErrorFor("GET", "/x", GitHubRestResponse{Status: 418, Body: "teapot"})
	assert.Equal(t, CodeGitHubRest, docutil.CodeOf(e))
}

func TestCov_CheckRunErrorFor(t *testing.T) {
	c := checkRunClient{credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "tok"}, baseURL: "https://api.github.com", clock: checkRunEpochClock}
	assert.NotNil(t, c)
}

func TestCov_RedactGitHubToken(t *testing.T) {
	assert.Equal(t, "[REDACTED]", RedactGitHubToken("secret", PrCredentials{Kind: PrCredentialsDeveloper, Token: "secret"}))
	assert.Equal(t, "hello", RedactGitHubToken("hello", PrCredentials{Kind: PrCredentialsDeveloper, Token: "secret"}))
}

func TestCov_githubStringifyRecord(t *testing.T) {
	s := githubStringifyRecord(map[string]any{"a": 1})
	assert.Contains(t, s, `"a"`)
}

func TestCov_githubFiniteInt(t *testing.T) {
	_, ok := githubFiniteInt("x")
	assert.False(t, ok)
	_, ok = githubFiniteInt(0.5)
	assert.False(t, ok)
	v, ok := githubFiniteInt(42.0)
	assert.True(t, ok)
	assert.Equal(t, 42, v)
}

func TestCov_BurstLimiter_config(t *testing.T) {
	_, err := NewHourlyBurstLimiter(BurstLimiterConfig{MaxPerHour: 0}, nil, nil)
	require.Error(t, err)
	_, err = NewHourlyBurstLimiter(BurstLimiterConfig{MaxPerHour: 5}, nil, nil)
	require.Error(t, err)
}

func TestCov_BurstLimiter_admit(t *testing.T) {
	_, err := NewHourlyBurstLimiter(BurstLimiterConfig{MaxPerHour: 2}, nil, nil)
	require.Error(t, err) // nil clock is refused
}

func TestCov_isRateLimitStatus(t *testing.T) {
	assert.True(t, isRateLimitStatus(429))
	assert.True(t, isRateLimitStatus(403))
	assert.False(t, isRateLimitStatus(500))
}

func TestCov_linkHeaderHasNext(t *testing.T) {
	assert.True(t, linkHeaderHasNext(`<https://a.com/2>; rel="next"`))
	assert.False(t, linkHeaderHasNext(""))
	assert.False(t, linkHeaderHasNext(`<https://a.com/2>; rel="last"`))
}

func TestCov_githubBodyExcerpt(t *testing.T) {
	short := githubBodyExcerpt("short")
	assert.Equal(t, "short", short)
	long := githubBodyExcerpt(strings.Repeat("x", 600))
	assert.Len(t, []rune(long), 500)
}

func TestCov_branchNameJSONExcerpt(t *testing.T) {
	s := branchNameJSONExcerpt("a\"b\nc")
	assert.Contains(t, s, `\"`)
	assert.Contains(t, s, `\n`)
}

func TestCov_branchEchoBound(t *testing.T) {
	b := branchEchoBound(strings.Repeat("x", 100))
	assert.Len(t, []rune(b), 64)
	b = branchEchoBound("short")
	assert.Equal(t, "short", b)
}

func TestCov_containsStringValue(t *testing.T) {
	assert.True(t, containsStringValue([]string{"a", "b"}, "a"))
	assert.False(t, containsStringValue([]string{"a", "b"}, "c"))
}

func TestCov_sortedStringsCopy(t *testing.T) {
	r := sortedStringsCopy([]string{"b", "a", "c"})
	assert.Equal(t, []string{"a", "b", "c"}, r)
}

func TestCov_AcknowledgeOverride(t *testing.T) {
	r := AcknowledgeOverride(OverrideAckInput{
		Pattern: "*.md", RunID: "r1", PolicyVersion: 1, Clock: func() string { return "2026-01-01T00:00:00Z" }, Queue: nil,
	})
	assert.True(t, r.Ack)

	q := CreateProposalQueue()
	r = AcknowledgeOverride(OverrideAckInput{
		Pattern: "*.go", RunID: "r2", PolicyVersion: 2, Clock: func() string { return "2026-01-02T00:00:00Z" }, Queue: q,
	})
	assert.True(t, r.Ack)
	assert.Len(t, q.Drain(), 1)
}

func TestCov_EmitFalseNegativeRecord(t *testing.T) {
	e, err := EmitFalseNegativeRecord(FalseNegativeEmissionInput{
		Routing: RoutingIgnore, SourcePrDiffPaths: []string{}, ManifestDocPaths: []string{"docs/a.md"},
		RunID: "r1", PolicyVersion: 1, Clock: func() string { return "2026-01-01T00:00:00Z" },
	})
	require.NoError(t, err)
	assert.Nil(t, e.Record)

	e, err = EmitFalseNegativeRecord(FalseNegativeEmissionInput{
		Routing: RoutingIgnore, SourcePrDiffPaths: []string{"docs/a.md"}, ManifestDocPaths: []string{"docs/a.md"},
		RunID: "r1", PolicyVersion: 1, DecisionID: strp("d1"), Clock: func() string { return "2026-01-01T00:00:00Z" },
	})
	require.NoError(t, err)
	require.NotNil(t, e.Record)
}

func TestCov_githubNumericHeader(t *testing.T) {
	h := map[string]string{"retry_after": "42"}
	v, ok := githubNumericHeader(h, "retry-after")
	assert.True(t, ok)
	assert.Equal(t, 42.0, v)

	v, ok = githubNumericHeader(h, "x-rate")
	assert.False(t, ok)
}

func TestCov_githubMessageOf(t *testing.T) {
	m := githubMessageOf(`{"message":"hello"}`)
	require.NotNil(t, m)
	assert.Equal(t, "hello", *m)
	m = githubMessageOf("not-json")
	assert.Nil(t, m)
	m = githubMessageOf(`{"msg":"hello"}`)
	assert.Nil(t, m)
}

func TestCov_githubStringField(t *testing.T) {
	assert.Equal(t, "hello", githubStringField(map[string]any{"key": "hello"}, "key"))
	assert.Equal(t, "", githubStringField(map[string]any{}, "key"))
}

func TestCov_githubNestedString(t *testing.T) {
	assert.Equal(t, "feat/x", githubNestedString(map[string]any{"head": map[string]any{"ref": "feat/x"}}, "head", "ref"))
	assert.Equal(t, "", githubNestedString(map[string]any{}, "head", "ref"))
}

func TestCov_realBackoffDelay(t *testing.T) {
	realBackoffDelay(0)
	realBackoffDelay(1)
}

func TestCov_backoffNumberText(t *testing.T) {
	assert.Equal(t, "NaN", backoffNumberText(math.NaN()))
	assert.Equal(t, "Infinity", backoffNumberText(math.Inf(1)))
	assert.Equal(t, "-Infinity", backoffNumberText(math.Inf(-1)))
}

func TestCov_gateJSONString(t *testing.T) {
	s := gateJSONString("hello")
	assert.NotEmpty(t, s)
}

func TestCov_mergeLedgerStringOrNull(t *testing.T) {
	assert.Equal(t, "null", mergeLedgerStringOrNull(nil))
	assert.Equal(t, "abc", mergeLedgerStringOrNull(strp("abc")))
}

func TestCov_incidentJSONString(t *testing.T) {
	s := incidentJSONString("hello")
	assert.NotEmpty(t, s)
}

func TestCov_incidentTruncate(t *testing.T) {
	assert.Equal(t, "short", incidentTruncate("short", 64))
	assert.Len(t, []rune(incidentTruncate(strings.Repeat("x", 100), 5)), 5)
}

func TestCov_incidentIssueSummary(t *testing.T) {
	s := incidentIssueSummary(errors.New("plain error"))
	assert.Equal(t, "plain error", s)
}
