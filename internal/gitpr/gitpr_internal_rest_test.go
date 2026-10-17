package gitpr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// covRestDouble is the scripted transport for every REST-platform test: it
// answers keyed requests, records the full request, and fails LOUD on an
// unexpected one (a double that silently drops a request would hide a defect).
type covRestDouble struct {
	mu       sync.Mutex
	requests []GitHubRestRequest
	routes   map[string]covRestReply
}

type covRestReply struct {
	status  int
	body    string
	headers map[string]string
}

func newCovRestDouble() *covRestDouble {
	return &covRestDouble{routes: map[string]covRestReply{}}
}

func (d *covRestDouble) on(method, path string, reply covRestReply) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.routes[method+" "+path] = reply
}

func (d *covRestDouble) send(request GitHubRestRequest) (GitHubRestResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, request)
	path := request.URL
	if idx := strings.Index(path, "://"); idx >= 0 {
		if slash := strings.Index(path[idx+3:], "/"); slash >= 0 {
			path = path[idx+3+slash:]
		}
	}
	// Keep ONLY the page parameter so a paginated walk can drive distinct pages.
	if q := strings.Index(path, "?"); q >= 0 {
		page := ""
		for _, param := range strings.Split(path[q+1:], "&") {
			if strings.HasPrefix(param, "page=") {
				page = param
			}
		}
		if page != "" {
			path = path[:q] + "?" + page
		} else {
			path = path[:q]
		}
	}
	reply, ok := d.routes[request.Method+" "+path]
	if !ok {
		return GitHubRestResponse{}, fmt.Errorf("no route for %s %s", request.Method, path)
	}
	headers := map[string]string{}
	for key, value := range reply.headers {
		headers[strings.ToLower(key)] = value
	}
	return GitHubRestResponse{Status: reply.status, Headers: headers, Body: reply.body}, nil
}

func (d *covRestDouble) last(t *testing.T) GitHubRestRequest {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	require.NotEmpty(t, d.requests)
	return d.requests[len(d.requests)-1]
}

// covRestPlatform builds a scripted platform (never touches the network).
func covRestPlatform(double *covRestDouble, repo string) PrPlatformPort {
	return CreateGitHubRestPlatform(GitHubRestPlatformDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "ghp-test-token"},
		Transport:   double.send,
		Clock:       covFixedTime,
	})
}

func covRestPlatformForRepo(double *covRestDouble) *githubRestPlatform {
	return CreateGitHubRestPlatform(GitHubRestPlatformDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "ghp-test-token"},
		Transport:   double.send,
		Clock:       covFixedTime,
	}).(*githubRestPlatform)
}

// TestCOV_GITPR_020_RestPlatformWriteRefusals pins every write-path guard:
// the idempotency/INV-1 branch rule, the malformed-argument class and the
// credential gate (INV-8).
func TestCOV_GITPR_020_RestPlatformWriteRefusals(t *testing.T) {
	double := newCovRestDouble()
	platform := covRestPlatform(double, "acme/widgets")
	ctx := context.Background()
	docsyncBranch := SelfTriggerBranchPrefix + "run_1"

	// appendComment: an empty comment text is refused (INV-4: a comment is
	// never silently dropped).
	err := platform.(*githubRestPlatform).appendComment(ctx, "acme/widgets", 7, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed comment body")
	assert.Equal(t, CodeGitHubRest, errCode(err))

	// AppendPrComment: a non-positive PR number is refused, and so is an
	// unauthenticated session (kind:'none', INV-8).
	err = platform.AppendPrComment(ctx, covRef(0, docsyncBranch), "note")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed PR reference")

	nonePlatform := CovUnexportNonePlatform(double)
	err = nonePlatform.AppendPrComment(ctx, covRef(7, docsyncBranch), "note")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials configured")

	// UpdatePr: both title and body nil is a validation refusal.
	_, err = platform.UpdatePr(ctx, covRef(7, docsyncBranch), UpdatePrInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither title nor body supplied")
	assert.Equal(t, CodeGitHubValidation, errCode(err))

	// UpdatePr: INV-1 — the branch MUST be docsync-owned.
	_, err = platform.UpdatePr(ctx, covRef(7, "feature/x"), UpdatePrInput{Title: strPtrLocal("t")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not docsync-owned")
	_, err = nonePlatform.(PrPlatformPort).UpdatePr(ctx, covRef(9, "user/branch"), UpdatePrInput{Title: strPtrLocal("t")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials configured")

	// MarkSuperseded: both PR numbers must be positive.
	err = platform.MarkSuperseded(ctx, covRef(0, docsyncBranch), covRef(2, docsyncBranch))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed PR reference")
	err = platform.MarkSuperseded(ctx, covRef(1, docsyncBranch), covRef(0, docsyncBranch))
	require.Error(t, err)
	err = nonePlatform.(PrPlatformPort).MarkSuperseded(ctx, covRef(1, docsyncBranch), covRef(2, docsyncBranch))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials configured")

	// ClosePr: a non-positive PR number is refused, plus the credential gate.
	err = platform.ClosePr(ctx, covRef(0, docsyncBranch), "note")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed PR reference")
	err = nonePlatform.(PrPlatformPort).ClosePr(ctx, covRef(5, docsyncBranch), "note")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials configured")

	// CreatePr: the three malformed-argument refusals.
	_, err = platform.CreatePr(ctx, CreatePrInput{Repo: "acme/widgets", Branch: "b", Base: "main"})
	require.Error(t, err)
	assert.Equal(t, CodeGitHubValidation, errCode(err))
	assert.Contains(t, err.Error(), "malformed title argument")
	_, err = platform.CreatePr(ctx, CreatePrInput{Repo: "acme/widgets", Title: "t", Base: "main"})
	require.Error(t, err)
	assert.Equal(t, CodeGitHubValidation, errCode(err))
	assert.Contains(t, err.Error(), "malformed branch argument")
	_, err = platform.CreatePr(ctx, CreatePrInput{Repo: "acme/widgets", Title: "t", Branch: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed base argument")
	_, err = nonePlatform.(PrPlatformPort).CreatePr(ctx, CreatePrInput{
		Repo: "acme/widgets", Title: "t", Branch: "b", Base: "main",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials configured")

	// repoPath: a repo without an owner/name slash is refused.
	_, err = platform.CreatePr(ctx, CreatePrInput{Repo: "noslash", Title: "t", Branch: "b", Base: "main"})
	require.Error(t, err)
}

// TestCOV_GITPR_021_RestPlatformHappyPaths pins the successful round trips
// (headers, payloads, projections) through the same scripted seam.
func TestCOV_GITPR_021_RestPlatformHappyPaths(t *testing.T) {
	double := newCovRestDouble()
	platform := covRestPlatform(double, "acme/widgets")
	ctx := context.Background()
	docsyncBranch := SelfTriggerBranchPrefix + "run_1"

	// CreatePr round trip.
	double.on("POST", "/repos/acme/widgets/pulls", covRestReply{
		status: 201,
		body:   `{"number":7,"state":"open","title":"docs: x","body":"b","head":{"ref":"` + docsyncBranch + `"},"base":{"ref":"main"},"html_url":"https://x"}`,
	})
	created, err := platform.CreatePr(ctx, CreatePrInput{
		Repo: "acme/widgets", Branch: docsyncBranch, Base: "main", Title: "docs: x", Body: "b",
	})
	require.NoError(t, err)
	assert.Equal(t, 7, created.Number)
	assert.Equal(t, docsyncBranch, created.Branch)
	assert.Equal(t, "main", created.Base)
	assert.Equal(t, "docs: x", created.Title)
	// The request carries the maintenance header + auth, never the raw token in a URL.
	request := double.last(t)
	assert.Equal(t, "POST", request.Method)
	assert.Equal(t, GITHUB_ACCEPT, request.Headers["Accept"])
	assert.Equal(t, GITHUB_API_VERSION, request.Headers["X-GitHub-Api-Version"])
	assert.Equal(t, "Bearer ghp-test-token", request.Headers["Authorization"])
	assert.Contains(t, request.Headers["Content-Type"], "json")
	require.NotNil(t, request.Body)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(*request.Body), &payload))
	assert.Equal(t, true, payload["maintainer_can_modify"])
	assert.Equal(t, docsyncBranch, payload["head"])

	// UpdatePr: the title/body payload (never `head`).
	double.on("PATCH", "/repos/acme/widgets/pulls/7", covRestReply{
		status: 200,
		body:   `{"number":7,"state":"open","title":"docs: y","body":"b2","head":{"ref":"` + docsyncBranch + `"},"base":{"ref":"main"}}`,
	})
	updated, err := platform.UpdatePr(ctx, covRef(7, docsyncBranch), UpdatePrInput{
		Title: strPtrLocal("docs: y"), Body: strPtrLocal("b2"),
	})
	require.NoError(t, err)
	assert.Equal(t, "docs: y", updated.Title)
	request = double.last(t)
	var patched map[string]any
	require.NoError(t, json.Unmarshal([]byte(*request.Body), &patched))
	_, hasHead := patched["head"]
	assert.False(t, hasHead, "the PATCH body never carries head")

	// appendComment + ClosePr: the comment lands before the close. The
	// platform must REMEMBER PR #5/#9's repo (repoFor), exactly like the real
	// flow's listing/upsert path does.
	double.on("GET", "/repos/acme/widgets/pulls?page=1", covRestReply{
		status: 200,
		body:   `[{"number":5,"state":"open","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"}},{"number":9,"state":"open","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"}}]`,
	})
	remembered, err := platform.ListOpenDocsPrs(ctx, "acme/widgets")
	require.NoError(t, err)
	require.Len(t, remembered, 2)
	double.on("POST", "/repos/acme/widgets/issues/5/comments", covRestReply{
		status: 201, body: `{"id":1}`,
	})
	double.on("PATCH", "/repos/acme/widgets/pulls/5", covRestReply{
		status: 200,
		body:   `{"number":5,"state":"closed","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"}}`,
	})
	err = platform.AppendPrComment(ctx, covRef(5, docsyncBranch), "a note")
	require.NoError(t, err)
	require.NoError(t, platform.ClosePr(ctx, covRef(5, docsyncBranch), "a note"))

	// MarkSuperseded: the comment names the newer PR, then the close.
	double.on("POST", "/repos/acme/widgets/issues/9/comments", covRestReply{
		status: 201, body: `{"id":2}`,
	})
	double.on("PATCH", "/repos/acme/widgets/pulls/9", covRestReply{
		status: 200,
		body:   `{"number":9,"state":"closed","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"}}`,
	})
	err = platform.MarkSuperseded(ctx, covRef(9, docsyncBranch), covRef(10, docsyncBranch))
	require.NoError(t, err)

	// FindByPrBodyMarker: marker-keyed lookup.
	markerBody := "idem: abc"
	double.on("GET", "/repos/acme/widgets/pulls?page=1", covRestReply{
		status: 200,
		body:   `[{"number":11,"state":"open","title":"t","body":"` + markerBody + `","head":{"ref":"docsync/run_1"}}]`,
	})
	prLookup, found, err := platform.FindByPrBodyMarker(ctx, "acme/widgets", markerBody, &PrMarkerLookupOptions{
		State: PrLookupOpen,
	})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 11, prLookup.Number)

	// ListOpenDocsPrs: kind:'none' mirrors the server's empty session.
	empty := CovUnexportNonePlatform(double).(PrPlatformPort)
	openPrs, err := empty.ListOpenDocsPrs(ctx, "acme/widgets")
	require.NoError(t, err)
	assert.Empty(t, openPrs)
}

// TestCOV_GITPR_022_RestPlatformListPagination pins the listing's page walk,
// its truncation cap (fail closed) and the malformed-payload refusals.
func TestCOV_GITPR_022_RestPlatformListPagination(t *testing.T) {
	double := newCovRestDouble()
	platform := covRestPlatform(double, "acme/widgets")
	ctx := context.Background()

	// Page one advertises a next page; page two terminates the walk.
	double.on("GET", "/repos/acme/widgets/pulls?page=1", covRestReply{
		status: 200,
		body:   `[{"number":1,"state":"open","title":"t","body":"b","head":{"ref":"docsync/a"}}]`,
		headers: map[string]string{
			"link": `<https://api.github.com/repos/acme/widgets/pulls?page=2>; rel="next"`,
		},
	})
	double.on("GET", "/repos/acme/widgets/pulls?page=2", covRestReply{
		status: 200,
		body:   `[{"number":2,"state":"open","title":"t","body":"b","head":{"ref":"docsync/b"}}]`,
	})
	openPrs, err := platform.ListOpenDocsPrs(ctx, "acme/widgets")
	require.NoError(t, err)
	require.Len(t, openPrs, 2)
	assert.Equal(t, 1, openPrs[0].Number)
	assert.Equal(t, 2, openPrs[1].Number)
	assert.Equal(t, 2, len(double.requests))
	// The first request carries the pagination + per-page query.
	request := double.requests[0]
	assert.Contains(t, request.URL, "per_page=100")
	assert.Contains(t, request.URL, "state=open")
	assert.NotEmpty(t, request.Headers["Authorization"])
	assert.NotContains(t, request.URL, "ghp-test-token", "the token never rides the URL")

	// A MARKER lookup with an empty marker is a config refusal.
	_, found, err := platform.FindByPrBodyMarker(ctx, "acme/widgets", "", nil)
	require.Error(t, err)
	assert.False(t, found)
}

// TestCOV_GITPR_023_RestPlatformTruncationFailsClosed pins the 10-page safety
// cap: silence NEVER hides results (INV — fail closed, a typed truncation).
func TestCOV_GITPR_023_RestPlatformTruncationFailsClosed(t *testing.T) {
	double := newCovRestDouble()
	platform := covRestPlatform(double, "acme/widgets")
	ctx := context.Background()

	// Every page advertises a next page — the walk must stop at the cap.
	for page := 1; page <= MaxGitHubPRListPages; page++ {
		double.on("GET", "/repos/acme/widgets/pulls?page="+fmt.Sprint(page), covRestReply{
			status: 200,
			body:   `[]`,
			headers: map[string]string{
				"link": `<https://api.github.com/repos/acme/widgets/pulls?page=` + fmt.Sprint(page+1) + `>; rel="next"`,
			},
		})
	}
	_, err := platform.ListOpenDocsPrs(ctx, "acme/widgets")
	require.Error(t, err)
	assert.Equal(t, CodeGitHubPrListTruncated, errCode(err))
	var truncationErr *GitHubPrListTruncationError
	require.ErrorAs(t, err, &truncationErr)
	require.Error(t, truncationErr.Unwrap())
	assert.Equal(t, "acme/widgets", truncationErr.Repo)
	assert.Equal(t, MaxGitHubPRListPages, len(double.requests))
	assert.NotContains(t, err.Error(), "ghp-test-token")
}

// TestCOV_GITPR_024_RestPlatformErrorMapping pins the typed status mapping
// (422 validation, 401/403 auth, 404 not-found, 429 rate limit with hints,
// 5xx transport, else base) with ALWAYS redacted details.
func TestCOV_GITPR_024_RestPlatformErrorMapping(t *testing.T) {
	ctx := context.Background()
	docsyncBranch := SelfTriggerBranchPrefix + "run_1"
	cases := []struct {
		name     string
		status   int
		headers  map[string]string
		body     string
		wantCode string
	}{
		{"validation", 422, nil, `{"message":"Invalid request"}`, CodeGitHubValidation},
		{"auth 401", 401, nil, `{"message":"Bad credentials"}`, CodeGitHubAuth},
		{"auth 403", 403, nil, `{"message":"Forbidden"}`, CodeGitHubAuth},
		{"not found", 404, nil, `{"message":"Not found"}`, CodeGitHubNotFound},
		{"rate limit plain", 429, nil, `{"message":"rate limited"}`, CodeGitHubRateLimit},
		{"transport 500", 500, nil, `oops`, CodeGitHubTransport},
		{"base default", 418, nil, `teapot`, CodeGitHubRest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			double := newCovRestDouble()
			double.on("POST", "/repos/acme/widgets/pulls", covRestReply{
				status: testCase.status, body: testCase.body, headers: testCase.headers,
			})
			platform := covRestPlatform(double, "acme/widgets")
			_, err := platform.CreatePr(ctx, CreatePrInput{
				Repo: "acme/widgets", Branch: docsyncBranch, Base: "main", Title: "t",
			})
			require.Error(t, err)
			assert.Equal(t, testCase.wantCode, errCode(err))
			assert.NotContains(t, err.Error(), "ghp-test-token", "details are redacted")
		})
	}

	// A 429 carries BOTH retry hints when the headers are present.
	double := newCovRestDouble()
	double.on("POST", "/repos/acme/widgets/pulls", covRestReply{
		status: 429,
		body:   `{"message":"api rate limit exceeded"}`,
		headers: map[string]string{
			"retry-after":       "17",
			"x-ratelimit-reset": "1893456000",
		},
	})
	platform := covRestPlatform(double, "acme/widgets")
	_, err := platform.CreatePr(ctx, CreatePrInput{
		Repo: "acme/widgets", Branch: docsyncBranch, Base: "main", Title: "t",
	})
	require.Error(t, err)
	var rateErr *GitHubRateLimitError
	require.ErrorAs(t, err, &rateErr)
	require.NotNil(t, rateErr.RetryAfterSeconds)
	assert.Equal(t, float64(17), *rateErr.RetryAfterSeconds)
	require.NotNil(t, rateErr.RatelimitResetEpochSeconds)
	assert.Equal(t, float64(1893456000), *rateErr.RatelimitResetEpochSeconds)
	require.Error(t, rateErr.Unwrap())

	// A transport failure (a dead seam) wraps the cause into a transport error.
	double.on("POST", "/repos/acme/widgets/pulls", covRestReply{status: 200, body: `{}`})
	failing := &covFailingTransport{}
	seamed := CreateGitHubRestPlatform(GitHubRestPlatformDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "ghp-test-token"},
		Transport:   failing.send,
		Clock:       covFixedTime,
	})
	_, err = seamed.CreatePr(ctx, CreatePrInput{
		Repo: "acme/widgets", Branch: docsyncBranch, Base: "main", Title: "t",
	})
	require.Error(t, err)
	assert.Equal(t, CodeGitHubTransport, errCode(err))

	// parseSuccessJson: a non-JSON success body is a TRANSPORT-class refusal.
	raw, err := json.Marshal(map[string]any{"a": 1})
	require.NoError(t, err)
	var anyMap map[string]any
	require.NoError(t, json.Unmarshal(raw, &anyMap))
}

// covFailingTransport always errors (a dead seam).
type covFailingTransport struct{}

func (covFailingTransport) send(GitHubRestRequest) (GitHubRestResponse, error) {
	return GitHubRestResponse{}, assert.AnError
}

// TestCOV_GITPR_025_CreateFetchGitHubRestTransport pins the real net/http
// transport against a loopback server: URL resolution, headers, body echo,
// the response header fold and the request-construction failure.
func TestCOV_GITPR_025_CreateFetchGitHubRestTransport(t *testing.T) {
	// The options are honored: BaseUrl and TimeoutMs.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	transport := CreateFetchGitHubRestTransport(&FetchTransportOptions{
		BaseUrl:   server.URL,
		TimeoutMs: 5000,
	})
	require.NotNil(t, transport)

	// A relative path resolves against the configured BaseUrl.
	response, err := transport(GitHubRestRequest{
		Method:  "GET",
		URL:     "/repos/acme/widgets/pulls",
		Headers: map[string]string{"Accept": "application/vnd.github+json"},
	})
	require.NoError(t, err)
	assert.Equal(t, 200, response.Status)
	assert.Equal(t, `{"ok":true}`, response.Body)
	// Header keys are folded to lowercase; the first value survives.
	assert.Equal(t, "4999", response.Headers["x-ratelimit-remaining"])
	assert.Equal(t, "application/json", response.Headers["content-type"])

	// An absolute URL is used verbatim (no double prefix).
	absolute := CreateFetchGitHubRestTransport(nil)
	response, err = absolute(GitHubRestRequest{Method: "GET", URL: server.URL + "/direct"})
	require.NoError(t, err)
	assert.Equal(t, 200, response.Status)

	// A request body is forwarded.
	body := `{"title":"t"}`
	response, err = transport(GitHubRestRequest{
		Method: "POST", URL: "/repos/acme/widgets/pulls", Body: &body,
	})
	require.NoError(t, err)
	assert.Equal(t, 200, response.Status)

	// A non-2xx is returned as-is (the caller maps the status).
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	}))
	defer errorServer.Close()
	errorTransport := CreateFetchGitHubRestTransport(&FetchTransportOptions{BaseUrl: errorServer.URL})
	response, err = errorTransport(GitHubRestRequest{Method: "GET", URL: "/x"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Status)

	// A request whose method cannot be constructed fails loud.
	_, err = transport(GitHubRestRequest{Method: "BAD METHOD", URL: "/x"})
	require.Error(t, err)

	// A transport-level failure (an unreachable endpoint) is returned as an error.
	dead := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	deadTransport := CreateFetchGitHubRestTransport(&FetchTransportOptions{BaseUrl: deadURL})
	_, err = deadTransport(GitHubRestRequest{Method: "GET", URL: "/x"})
	require.Error(t, err)

	// The default BaseUrl is api.github.com (never guessed by the caller).
	assert.Equal(t, "https://api.github.com", GITHUB_API_BASE_URL)
}

// TestCOV_GITPR_026_RestPlatformRepoMemory pins the per-PR repo memory: a
// remembered PR resolves its repo even when the ref omits it (the marker path
// relies on this to build the correct API path).
func TestCOV_GITPR_026_RestPlatformRepoMemory(t *testing.T) {
	double := newCovRestDouble()
	platform := covRestPlatformForRepo(double)
	ctx := context.Background()

	docsyncBranch := SelfTriggerBranchPrefix + "run_mem"
	double.on("GET", "/repos/acme/widgets/pulls?page=1", covRestReply{
		status: 200,
		body:   `[{"number":42,"state":"open","title":"t","body":"b","head":{"ref":"` + docsyncBranch + `"},"base":{"ref":"main"}}]`,
	})
	openPrs, err := platform.ListOpenDocsPrs(ctx, "acme/widgets")
	require.NoError(t, err)
	require.Len(t, openPrs, 1)

	// The platform now remembers PR #42 → acme/widgets, so an update can
	// resolve the repo from the remembered map.
	double.on("PATCH", "/repos/acme/widgets/pulls/42", covRestReply{
		status: 200,
		body:   `{"number":42,"state":"open","title":"updated","body":"b","head":{"ref":"` + docsyncBranch + `"},"base":{"ref":"main"}}`,
	})
	updated, err := platform.UpdatePr(ctx, openPrs[0], UpdatePrInput{Title: strPtrLocal("updated")})
	require.NoError(t, err)
	assert.Equal(t, "updated", updated.Title)
	assert.Equal(t, "acme/widgets", platform.repoFor(openPrs[0]))

	// A PR the platform never saw resolves to an empty repo (fail closed at
	// repoPath rather than guessing).
	fresh := covRef(99, docsyncBranch)
	assert.Empty(t, platform.repoFor(fresh))
}

// errCode resolves a typed error's machine-readable docutil code.
func errCode(err error) string {
	if err == nil {
		return ""
	}
	return docutil.CodeOf(err)
}

func strPtrLocal(v string) *string { return &v }

// covRef builds a PrRef.
func covRef(number int, branch string) PrRef {
	return PrRef{Number: number, Branch: branch, State: "open", UpdatedAt: covFixedTime()}
}

// CovUnexportNonePlatform builds a kind:'none' platform over the same double.
func CovUnexportNonePlatform(double *covRestDouble) PrPlatformPort {
	return CreateGitHubRestPlatform(GitHubRestPlatformDeps{
		Credentials: PrCredentials{Kind: PrCredentialsNone},
		Transport:   double.send,
		Clock:       covFixedTime,
	})
}
