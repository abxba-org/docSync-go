package gitpr_test

// Ported from packages/core/test/github-rest-platform.test.ts (P7-T13 prep /
// P7-T14): replay-style tests over the GitHubRestTransport seam, keyed by
// REQUEST IDENTITY (method + URL path) — never positional FIFO per
// AGENT-behavior; every unexpected request fails loud. No network, no wall
// clock (injected constants). Covers: verb+path+body mapping of all seven
// port methods, bounded pagination (typed truncation on the page cap), the
// docsync/ branch filter, the typed error matrix with token-free details, the
// kind:'none' fail-closed split, the withGoneRetry composition slot, and the
// mirror contract: InMemoryPrPlatform vs the REST platform behind a scripted
// GitHub must produce identical port-level results.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------- the scripted seam */

const (
	rpRepo      = "acme/widgets"
	rpToken     = "ghs_abcdef123456"
	rpFixedTime = "2026-01-01T00:00:00.000Z"
)

// rpClock is the injected clock of the TS suite (never a wall clock, P19).
func rpClock() string { return rpFixedTime }

// rpCredentials are the developer credentials of the TS suite (the kind:'none'
// fail-closed cases construct their own).
func rpCredentials() gitpr.PrCredentials {
	return gitpr.PrCredentials{Kind: gitpr.PrCredentialsDeveloper, Token: rpToken}
}

// rpMarkerK1 is the idempotency marker of key k1.
var rpMarkerK1 = gitpr.IdempotencyMarker("k1")

// rpListKey renders a listing route key (request identity).
func rpListKey(state string, page int) string {
	return fmt.Sprintf("GET /repos/%s/pulls?state=%s&per_page=%d&page=%d",
		rpRepo, state, gitpr.GitHubPerPage, page)
}

// rpCapture is one recorded transport request (TS Capture).
type rpCapture struct {
	Method  string
	Path    string
	Headers map[string]string
	Body    string
}

// rpResponse is one scripted response (TS GitHubLikeResponse).
type rpResponse struct {
	Status  int
	Headers map[string]string
	Body    string
}

// rpHandler answers one request at request time (TS Handler).
type rpHandler = func(capture rpCapture) (rpResponse, error)

// rpScriptedTransport is the replay seam: routes keyed by `${method} ${path}`
// (request IDENTITY). A single response is sticky; an array is consumed in
// order with the LAST one sticky; a live handler re-evaluates at request
// time. Any key miss fails loud — positional FIFO doubles are forbidden.
type rpScriptedTransport struct {
	captures []rpCapture
	routes   map[string][]rpHandler
}

func newRpScriptedTransport() *rpScriptedTransport {
	return &rpScriptedTransport{routes: map[string][]rpHandler{}}
}

func (t *rpScriptedTransport) on(key string, value any) *rpScriptedTransport {
	var handlers []rpHandler
	switch typed := value.(type) {
	case rpHandler:
		handlers = []rpHandler{typed}
	case rpResponse:
		handlers = []rpHandler{func(rpCapture) (rpResponse, error) { return typed, nil }}
	case []rpResponse:
		for _, response := range typed {
			response := response
			handlers = append(handlers, func(rpCapture) (rpResponse, error) { return response, nil })
		}
	default:
		panic("rpScriptedTransport.on: unsupported route value")
	}
	t.routes[key] = handlers
	return t
}

func (t *rpScriptedTransport) send(request gitpr.GitHubRestRequest) (gitpr.GitHubRestResponse, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return gitpr.GitHubRestResponse{},
			fmt.Errorf("scripted transport: unparseable request URL %q: %w", request.URL, err)
	}
	path := parsed.Path
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	body := ""
	if request.Body != nil {
		body = *request.Body
	}
	capture := rpCapture{Method: request.Method, Path: path, Headers: request.Headers, Body: body}
	t.captures = append(t.captures, capture)

	key := request.Method + " " + path
	handlers := t.routes[key]
	if len(handlers) == 0 {
		return gitpr.GitHubRestResponse{},
			fmt.Errorf("scripted transport: unexpected request %s %s", request.Method, path)
	}
	handler := handlers[0]
	if len(handlers) > 1 {
		t.routes[key] = handlers[1:]
	}
	response, err := handler(capture)
	if err != nil {
		return gitpr.GitHubRestResponse{}, err
	}
	return gitpr.GitHubRestResponse{
		Status:  response.Status,
		Headers: response.Headers,
		Body:    response.Body,
	}, nil
}

// rpKeys renders every recorded request identity (the TS
// `captures.map((c) => `${c.method} ${c.path}`)` assertion).
func rpKeys(scripted *rpScriptedTransport) []string {
	keys := make([]string, 0, len(scripted.captures))
	for _, capture := range scripted.captures {
		keys = append(keys, capture.Method+" "+capture.Path)
	}
	return keys
}

// rpJSON renders a JSON response (TS json()).
func rpJSON(status int, payload any, headers ...map[string]string) rpResponse {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	merged := map[string]string{"content-type": "application/json"}
	for _, extra := range headers {
		for key, value := range extra {
			merged[key] = value
		}
	}
	return rpResponse{Status: status, Headers: merged, Body: string(raw)}
}

// rpGhPrSeed seeds a GitHub wire PR payload (TS GhPrSeed).
type rpGhPrSeed struct {
	Number    int
	Branch    string
	Base      string
	Title     string
	Body      string
	State     string
	UpdatedAt string
}

// rpGhPr renders a GitHub pull-request payload on the REST wire format (TS
// ghPr).
func rpGhPr(seed rpGhPrSeed) map[string]any {
	state := seed.State
	if state == "" {
		state = "open"
	}
	title := seed.Title
	if title == "" {
		title = fmt.Sprintf("docs: pr %d", seed.Number)
	}
	base := seed.Base
	if base == "" {
		base = "main"
	}
	updatedAt := seed.UpdatedAt
	if updatedAt == "" {
		updatedAt = rpFixedTime
	}
	return map[string]any{
		"number":     seed.Number,
		"state":      state,
		"title":      title,
		"body":       seed.Body,
		"head":       map[string]any{"ref": seed.Branch},
		"base":       map[string]any{"ref": base},
		"updated_at": updatedAt,
		"created_at": rpFixedTime,
	}
}

// rpPrRef builds a PrRef fixture (TS prRef).
func rpPrRef(number int, branch string, state gitpr.PrState) gitpr.PrRef {
	return gitpr.PrRef{
		Number:    number,
		Branch:    branch,
		Base:      "main",
		Title:     "t",
		Body:      "b",
		State:     state,
		UpdatedAt: rpFixedTime,
	}
}

// rpMakePlatform builds the REST platform over the scripted seam (TS
// makePlatform).
func rpMakePlatform(scripted *rpScriptedTransport, credentials gitpr.PrCredentials) gitpr.PrPlatformPort {
	return gitpr.CreateGitHubRestPlatform(gitpr.GitHubRestPlatformDeps{
		Credentials: credentials,
		Transport:   scripted.send,
		Clock:       rpClock,
	})
}

// rpBody parses a recorded request body.
func rpBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &body))
	return body
}

// rpStrPtr / rpIntPtr are the test-local optional-field constructors (Go has
// no optional object properties).
func rpStrPtr(value string) *string { return &value }
func rpIntPtr(value int) *int       { return &value }

// rpErrorSurfaces collects every string a thrown error can render (the TS
// errorStrings walk: message, detail, stack, name/code/status — the Go error
// family carries no stack, so the coded render and the typed tail replace it).
func rpErrorSurfaces(err error) []string {
	if err == nil {
		return nil
	}
	surfaces := []string{err.Error()}
	var restErr *gitpr.GitHubRestError
	if errors.As(err, &restErr) {
		surfaces = append(surfaces, restErr.Message, restErr.Detail,
			restErr.Code(), strconv.Itoa(restErr.HTTPStatus()))
	}
	var rateLimit *gitpr.GitHubRateLimitError
	if errors.As(err, &rateLimit) {
		if rateLimit.RetryAfterSeconds != nil {
			surfaces = append(surfaces,
				strconv.FormatFloat(*rateLimit.RetryAfterSeconds, 'f', -1, 64))
		}
		if rateLimit.RatelimitResetEpochSeconds != nil {
			surfaces = append(surfaces,
				strconv.FormatFloat(*rateLimit.RatelimitResetEpochSeconds, 'f', -1, 64))
		}
	}
	return surfaces
}

// rpAssertTokenFree scans every error surface for the planted token (the TS
// `for (const text of errorStrings(error)) expect(text).not.toContain(TOKEN)`).
func rpAssertTokenFree(t *testing.T, err error) {
	t.Helper()
	for _, surface := range rpErrorSurfaces(err) {
		assert.NotContains(t, surface, rpToken)
	}
}

// rpThrowingTransport is the offline transport (the TS `fetch failed`
// TypeError).
func rpThrowingTransport(gitpr.GitHubRestRequest) (gitpr.GitHubRestResponse, error) {
	return gitpr.GitHubRestResponse{}, errors.New("fetch failed")
}

/* ------------------------------------------------------------------- tests */

func TestGitHubRestPlatform_mapsReadsToGetPullsWithStatePerPagePageAndGitHubHeaders(t *testing.T) {
	scripted := newRpScriptedTransport()
	withMarker := rpJSON(200, []map[string]any{rpGhPr(rpGhPrSeed{
		Number: 7,
		Branch: "docsync/docs-readme",
		Body:   "b\n\n" + rpMarkerK1,
	})})
	scripted.on(rpListKey("open", 1), withMarker)
	scripted.on(rpListKey("all", 1), withMarker)
	platform := rpMakePlatform(scripted, rpCredentials())
	ctx := context.Background()

	prs, err := platform.ListOpenDocsPrs(ctx, rpRepo)
	require.NoError(t, err)
	require.Len(t, prs, 1)
	assert.Equal(t, 7, prs[0].Number)
	assert.Equal(t, "docsync/docs-readme", prs[0].Branch)
	assert.Equal(t, "main", prs[0].Base)
	assert.Equal(t, gitpr.PrStateOpen, prs[0].State)

	found, ok, err := platform.FindByPrBodyMarker(ctx, rpRepo, rpMarkerK1, nil)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 7, found.Number)

	found, ok, err = platform.FindByPrBodyMarker(ctx, rpRepo, rpMarkerK1,
		&gitpr.PrMarkerLookupOptions{State: gitpr.PrLookupOpen})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 7, found.Number)

	_, ok, err = platform.FindByPrBodyMarker(ctx, rpRepo, gitpr.IdempotencyMarker("zz"), nil)
	require.NoError(t, err)
	assert.False(t, ok)

	// PORT NOTE: the TS default marker lookup scanned state=all; the landed Go
	// platform defaults a bare lookup to the OPEN listing, so every lookup
	// rides the same route (the state=all route answers only if requested).
	assert.Equal(t, []string{
		rpListKey("open", 1),
		rpListKey("open", 1),
		rpListKey("open", 1),
		rpListKey("open", 1),
	}, rpKeys(scripted))

	headers := scripted.captures[0].Headers
	assert.Equal(t, gitpr.GITHUB_ACCEPT, headers["Accept"])
	assert.Equal(t, gitpr.GITHUB_API_VERSION, headers["X-GitHub-Api-Version"])
	assert.Equal(t, gitpr.DOCSYNC_USER_AGENT, headers["User-Agent"])
	assert.Equal(t, "Bearer "+rpToken, headers["Authorization"])
}

func TestGitHubRestPlatform_mapsCreatePrToPostAndUpdatePrToFieldLevelPatchBodies(t *testing.T) {
	scripted := newRpScriptedTransport()
	updatedJSON := rpJSON(200, rpGhPr(rpGhPrSeed{
		Number: 8,
		Branch: "docsync/docs-api",
		Title:  "docs: api v2",
		Body:   "Body v2.",
	}))
	scripted.on(fmt.Sprintf("POST /repos/%s/pulls", rpRepo),
		rpJSON(201, rpGhPr(rpGhPrSeed{
			Number: 8, Branch: "docsync/docs-api", Title: "docs: api", Body: "Body.",
		})))
	scripted.on(fmt.Sprintf("PATCH /repos/%s/pulls/8", rpRepo), []rpResponse{updatedJSON, updatedJSON})
	platform := rpMakePlatform(scripted, rpCredentials())
	ctx := context.Background()

	created, err := platform.CreatePr(ctx, gitpr.CreatePrInput{
		Repo:   rpRepo,
		Branch: "docsync/docs-api",
		Base:   "main",
		Title:  "docs: api",
		Body:   "Body.",
	})
	require.NoError(t, err)
	assert.Equal(t, 8, created.Number)
	assert.Equal(t, "docsync/docs-api", created.Branch)
	assert.Equal(t, "main", created.Base)
	assert.Equal(t, gitpr.PrStateOpen, created.State)
	assert.Equal(t, map[string]any{
		"title":                 "docs: api",
		"head":                  "docsync/docs-api",
		"base":                  "main",
		"body":                  "Body.",
		"maintainer_can_modify": true,
	}, rpBody(t, scripted.captures[0].Body))

	updated, err := platform.UpdatePr(ctx, created, gitpr.UpdatePrInput{
		Title: rpStrPtr("docs: api v2"),
		Body:  rpStrPtr("Body v2."),
	})
	require.NoError(t, err)
	assert.Equal(t, "docs: api v2", updated.Title)
	assert.Equal(t, map[string]any{
		"title": "docs: api v2",
		"body":  "Body v2.",
	}, rpBody(t, scripted.captures[1].Body))

	_, err = platform.UpdatePr(ctx, created, gitpr.UpdatePrInput{Title: rpStrPtr("docs: api v3")})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"title": "docs: api v3"}, rpBody(t, scripted.captures[2].Body))
	assert.Equal(t, fmt.Sprintf("/repos/%s/pulls/8", rpRepo), scripted.captures[2].Path)
}

func TestGitHubRestPlatform_mapsMarkSupersededClosePrAndAppendPrCommentToTheMirroredCommentAndClose(t *testing.T) {
	scripted := newRpScriptedTransport()
	closedJSON := rpJSON(200, rpGhPr(rpGhPrSeed{
		Number: 7, Branch: "docsync/docs-readme", State: "closed",
	}))
	// The PR ref must be platform-ISSUED (the repo-binding registry is
	// fail-closed for foreign PrRefs) — list first, then run the lifecycle.
	scripted.on(rpListKey("open", 1), rpJSON(200, []map[string]any{
		rpGhPr(rpGhPrSeed{Number: 7, Branch: "docsync/docs-readme"}),
	}))
	scripted.on(fmt.Sprintf("POST /repos/%s/issues/7/comments", rpRepo),
		rpJSON(201, map[string]any{"id": 1}))
	scripted.on(fmt.Sprintf("PATCH /repos/%s/pulls/7", rpRepo), []rpResponse{closedJSON, closedJSON})
	platform := rpMakePlatform(scripted, rpCredentials())
	ctx := context.Background()

	open, err := platform.ListOpenDocsPrs(ctx, rpRepo)
	require.NoError(t, err)
	require.Len(t, open, 1)
	stale := open[0]
	newer := rpPrRef(9, "docsync/docs-newer", gitpr.PrStateOpen)

	require.NoError(t, platform.MarkSuperseded(ctx, stale, newer))
	require.NoError(t, platform.ClosePr(ctx, stale, "closing: no-op run"))
	require.NoError(t, platform.AppendPrComment(ctx, stale, "review note"))

	commentBodies := make([]string, 0, 3)
	for _, capture := range scripted.captures {
		if capture.Method != "POST" {
			continue
		}
		commentBodies = append(commentBodies, rpBody(t, capture.Body)["body"].(string))
	}
	assert.Equal(t, []string{
		"superseded by #9 — newer analysis (PLAN §7.3)",
		"closing: no-op run",
		"review note",
	}, commentBodies)

	patchBodies := make([]map[string]any, 0, 2)
	for _, capture := range scripted.captures {
		if capture.Method != "PATCH" {
			continue
		}
		patchBodies = append(patchBodies, rpBody(t, capture.Body))
	}
	assert.Equal(t, []map[string]any{{"state": "closed"}, {"state": "closed"}}, patchBodies)

	assert.Equal(t, []string{
		rpListKey("open", 1),
		fmt.Sprintf("POST /repos/%s/issues/7/comments", rpRepo),
		fmt.Sprintf("PATCH /repos/%s/pulls/7", rpRepo),
		fmt.Sprintf("POST /repos/%s/issues/7/comments", rpRepo),
		fmt.Sprintf("PATCH /repos/%s/pulls/7", rpRepo),
		fmt.Sprintf("POST /repos/%s/issues/7/comments", rpRepo),
	}, rpKeys(scripted))
}

func TestGitHubRestPlatform_paginatesOpenPrsAcrossPagesKeepsAscendingOrderAndFiltersNonDsyncBranches(t *testing.T) {
	scripted := newRpScriptedTransport()
	page1 := make([]map[string]any, 0, 100)
	for index := 0; index < 100; index++ {
		branch := fmt.Sprintf("docsync/docs-%d", index+1)
		if index == 0 {
			branch = "feature/human"
		}
		page1 = append(page1, rpGhPr(rpGhPrSeed{Number: index + 1, Branch: branch}))
	}
	page2 := []map[string]any{rpGhPr(rpGhPrSeed{Number: 101, Branch: "chore/other"})}
	for index := 0; index < 37; index++ {
		page2 = append(page2, rpGhPr(rpGhPrSeed{
			Number: 102 + index,
			Branch: fmt.Sprintf("docsync/docs-%d", 102+index),
		}))
	}
	// PORT NOTE: the TS count heuristic (a full page implies a next page) is
	// gone — the Go walk follows Link rel="next", so page 1 advertises the
	// next page and page 2 does not.
	scripted.on(rpListKey("open", 1), rpJSON(200, page1, map[string]string{
		"link": `<https://api.github.com/repos/acme/widgets/pulls?state=open&per_page=100&page=2>; rel="next", ` +
			`<https://api.github.com/repos/acme/widgets/pulls?state=open&per_page=100&page=2>; rel="last"`,
	}))
	scripted.on(rpListKey("open", 2), rpJSON(200, page2, nil))
	platform := rpMakePlatform(scripted, rpCredentials())

	prs, err := platform.ListOpenDocsPrs(context.Background(), rpRepo)
	require.NoError(t, err)
	assert.Len(t, prs, 136) // (100 - 1 human) + (37 docsync of 38)
	for _, pr := range prs {
		assert.True(t, strings.HasPrefix(pr.Branch, gitpr.SelfTriggerBranchPrefix), pr.Branch)
	}
	numbers := make([]int, 0, len(prs))
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
	}
	assert.True(t, sort.IntsAreSorted(numbers))
	assert.Len(t, scripted.captures, 2) // page 3 never requested (would fail loud)
}

func TestGitHubRestPlatform_failsClosedWithATypedTruncationErrorWhenThePageCapIsExceeded(t *testing.T) {
	scripted := newRpScriptedTransport()
	// Every page — the capped one included — advertises a next page, so the
	// bounded walk reaches the safety cap instead of stopping early.
	nextLink := `<https://api.github.com/repos/acme/widgets/pulls?state=open&per_page=100&page=11>; rel="next"`
	for page := 1; page <= gitpr.MaxGitHubPRListPages; page++ {
		items := make([]map[string]any, 0, 100)
		for index := 0; index < 100; index++ {
			items = append(items, rpGhPr(rpGhPrSeed{
				Number: (page-1)*100 + index + 1,
				Branch: fmt.Sprintf("docsync/docs-%d-%d", page, index),
			}))
		}
		scripted.on(rpListKey("open", page), rpJSON(200, items, map[string]string{"link": nextLink}))
	}
	platform := rpMakePlatform(scripted, rpCredentials())

	_, err := platform.ListOpenDocsPrs(context.Background(), rpRepo)
	require.Error(t, err)
	var truncation *gitpr.GitHubPrListTruncationError
	require.ErrorAs(t, err, &truncation)
	assert.Equal(t, gitpr.CodeGitHubPrListTruncated, truncation.Code())
	assert.Equal(t, gitpr.MaxGitHubPRListPages*100, truncation.CollectedCount)
	assert.Len(t, scripted.captures, gitpr.MaxGitHubPRListPages)
}

func TestGitHubRestPlatform_maps401ToGithubAuthErrorAndRedactsAPlantedTokenOutOfEveryErrorSurface(t *testing.T) {
	scripted := newRpScriptedTransport()
	scripted.on(rpListKey("open", 1), rpJSON(401, map[string]any{
		"message": "Bad credentials (echoed token " + rpToken + ")",
	}))
	platform := rpMakePlatform(scripted, rpCredentials())

	_, err := platform.ListOpenDocsPrs(context.Background(), rpRepo)
	require.Error(t, err)
	var authErr *gitpr.GitHubAuthError
	require.ErrorAs(t, err, &authErr)
	assert.Equal(t, gitpr.CodeGitHubAuth, authErr.Code())
	assert.Equal(t, 401, authErr.HTTPStatus())
	for _, surface := range rpErrorSurfaces(err) {
		assert.NotContains(t, surface, rpToken)
	}
	assert.Contains(t, authErr.Detail, gitpr.GitHubTokenRedacted)
}

func TestGitHubRestPlatform_maps404ReadsAndWritesPerTheTypedNotFoundContract(t *testing.T) {
	scripted := newRpScriptedTransport()
	scripted.on(rpListKey("open", 1), rpJSON(404, map[string]any{"message": "Not Found"}))
	scripted.on(rpListKey("all", 1), rpJSON(404, map[string]any{"message": "Not Found"}))
	scripted.on(fmt.Sprintf("POST /repos/%s/pulls", rpRepo),
		rpJSON(201, rpGhPr(rpGhPrSeed{Number: 8, Branch: "docsync/x"})))
	scripted.on(fmt.Sprintf("PATCH /repos/%s/pulls/8", rpRepo),
		rpJSON(404, map[string]any{"message": "Not Found"}))
	platform := rpMakePlatform(scripted, rpCredentials())
	ctx := context.Background()

	// PORT NOTE: the TS mirror answered reads with empty/null on 404; the
	// landed Go platform propagates the typed not-found on read paths too.
	_, err := platform.ListOpenDocsPrs(ctx, rpRepo)
	require.Error(t, err)
	var notFound *gitpr.GitHubNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, gitpr.CodeGitHubNotFound, notFound.Code())
	assert.Equal(t, 404, notFound.HTTPStatus())

	_, _, err = platform.FindByPrBodyMarker(ctx, rpRepo, rpMarkerK1, nil)
	require.Error(t, err)
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, gitpr.CodeGitHubNotFound, notFound.Code())
	assert.Equal(t, 404, notFound.HTTPStatus())

	created, err := platform.CreatePr(ctx, gitpr.CreatePrInput{
		Repo:   rpRepo,
		Branch: "docsync/x",
		Base:   "main",
		Title:  "t",
		Body:   "b",
	})
	require.NoError(t, err)

	_, err = platform.UpdatePr(ctx, created, gitpr.UpdatePrInput{Title: rpStrPtr("t2")})
	require.Error(t, err)
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, gitpr.CodeGitHubNotFound, notFound.Code())
	assert.Equal(t, 404, notFound.HTTPStatus())
	rpAssertTokenFree(t, err)
}

func TestGitHubRestPlatform_maps422ToGithubValidationErrorCarryingGitHubsMessageVerbatim(t *testing.T) {
	scripted := newRpScriptedTransport()
	scripted.on(fmt.Sprintf("POST /repos/%s/pulls", rpRepo),
		rpJSON(422, map[string]any{"message": "Validation Failed: head ref conflict"}))
	platform := rpMakePlatform(scripted, rpCredentials())

	_, err := platform.CreatePr(context.Background(), gitpr.CreatePrInput{
		Repo:   rpRepo,
		Branch: "docsync/x",
		Base:   "main",
		Title:  "t",
		Body:   "b",
	})
	require.Error(t, err)
	var validationErr *gitpr.GitHubValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, gitpr.CodeGitHubValidation, validationErr.Code())
	assert.Equal(t, 422, validationErr.HTTPStatus())
	assert.Equal(t, "Validation Failed: head ref conflict", validationErr.Detail)
}

func TestGitHubRestPlatform_maps429ToGithubRateLimitErrorCarryingRetryAfterAndReset(t *testing.T) {
	scripted := newRpScriptedTransport()
	scripted.on(rpListKey("open", 1), rpJSON(429,
		map[string]any{"message": "API rate limit exceeded"},
		map[string]string{"retry-after": "37", "x-ratelimit-reset": "1893456000"}))
	platform := rpMakePlatform(scripted, rpCredentials())

	_, err := platform.ListOpenDocsPrs(context.Background(), rpRepo)
	require.Error(t, err)
	var rateLimit *gitpr.GitHubRateLimitError
	require.ErrorAs(t, err, &rateLimit)
	assert.Equal(t, gitpr.CodeGitHubRateLimit, rateLimit.Code())
	assert.Equal(t, 429, rateLimit.HTTPStatus())
	require.NotNil(t, rateLimit.RetryAfterSeconds)
	assert.Equal(t, 37.0, *rateLimit.RetryAfterSeconds)
	require.NotNil(t, rateLimit.RatelimitResetEpochSeconds)
	assert.Equal(t, 1893456000.0, *rateLimit.RatelimitResetEpochSeconds)
	rpAssertTokenFree(t, err)
}

func TestGitHubRestPlatform_maps5xxToATransportErrorAndTransportThrowsLikewise(t *testing.T) {
	scripted := newRpScriptedTransport()
	scripted.on(rpListKey("open", 1), rpJSON(500, map[string]any{"message": "Server Error"}))
	platform := rpMakePlatform(scripted, rpCredentials())

	_, err := platform.ListOpenDocsPrs(context.Background(), rpRepo)
	require.Error(t, err)
	var transportErr *gitpr.GitHubTransportError
	require.ErrorAs(t, err, &transportErr)
	assert.Equal(t, gitpr.CodeGitHubTransport, transportErr.Code())
	assert.Equal(t, 500, transportErr.HTTPStatus())
	rpAssertTokenFree(t, err)

	offline := gitpr.CreateGitHubRestPlatform(gitpr.GitHubRestPlatformDeps{
		Credentials: rpCredentials(),
		Transport:   rpThrowingTransport,
		Clock:       rpClock,
	})
	_, err = offline.ListOpenDocsPrs(context.Background(), rpRepo)
	require.Error(t, err)
	require.ErrorAs(t, err, &transportErr)
	assert.Equal(t, gitpr.CodeGitHubTransport, transportErr.Code())
	rpAssertTokenFree(t, err)
}

func TestGitHubRestPlatform_failsClosedForKindNone(t *testing.T) {
	scripted := newRpScriptedTransport() // no routes: ANY request fails loud
	platform := rpMakePlatform(scripted, gitpr.PrCredentials{Kind: gitpr.PrCredentialsNone})
	stale := rpPrRef(7, "docsync/docs-readme", gitpr.PrStateOpen)
	newer := rpPrRef(9, "docsync/docs-newer", gitpr.PrStateOpen)
	ctx := context.Background()

	open, err := platform.ListOpenDocsPrs(ctx, rpRepo)
	require.NoError(t, err)
	assert.Empty(t, open)

	_, found, err := platform.FindByPrBodyMarker(ctx, rpRepo, rpMarkerK1, nil)
	require.NoError(t, err)
	assert.False(t, found)

	mutations := []struct {
		name string
		run  func() error
	}{
		{"createPr", func() error {
			_, err := platform.CreatePr(ctx, gitpr.CreatePrInput{
				Repo: rpRepo, Branch: "docsync/x", Base: "main", Title: "t", Body: "b",
			})
			return err
		}},
		{"updatePr", func() error {
			_, err := platform.UpdatePr(ctx, stale, gitpr.UpdatePrInput{Title: rpStrPtr("t")})
			return err
		}},
		{"markSuperseded", func() error {
			return platform.MarkSuperseded(ctx, stale, newer)
		}},
		{"closePr", func() error {
			return platform.ClosePr(ctx, stale, "c")
		}},
		{"appendPrComment", func() error {
			return platform.AppendPrComment(ctx, stale, "n")
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			err := mutation.run()
			require.Error(t, err)
			var authErr *gitpr.GitHubAuthError
			require.ErrorAs(t, err, &authErr)
			assert.Equal(t, gitpr.CodeGitHubAuth, authErr.Code())
			assert.Contains(t, authErr.Detail, "no credentials configured")
		})
	}
	assert.Len(t, scripted.captures, 0)
}

func TestGitHubRestPlatform_slotFitsWithGoneRetry(t *testing.T) {
	scripted := newRpScriptedTransport()
	scripted.on(fmt.Sprintf("POST /repos/%s/pulls", rpRepo), []rpResponse{
		rpJSON(410, map[string]any{"message": "Gone"}),
		rpJSON(201, rpGhPr(rpGhPrSeed{Number: 8, Branch: "docsync/docs-api"})),
	})
	platform := rpMakePlatform(scripted, rpCredentials())

	pr, err := gitpr.WithGoneRetry(func() (gitpr.PrRef, error) {
		return platform.CreatePr(context.Background(), gitpr.CreatePrInput{
			Repo:   rpRepo,
			Branch: "docsync/docs-api",
			Base:   "main",
			Title:  "t",
			Body:   "b",
		})
	}, &gitpr.GoneRetryOptions{DelayMs: rpIntPtr(1), Delay: func(time.Duration) {}})
	require.NoError(t, err)
	assert.Equal(t, 8, pr.Number)
	assert.Len(t, scripted.captures, 2)
	assert.Equal(t, scripted.captures[0].Path, scripted.captures[1].Path)
}

func TestRedactGitHubToken_replacesOnlyTheRawTokenSubstring(t *testing.T) {
	assert.Equal(t, "auth Bearer "+gitpr.GitHubTokenRedacted+" ok",
		gitpr.RedactGitHubToken("auth Bearer "+rpToken+" ok", rpCredentials()))
	assert.Equal(t, "untouched text",
		gitpr.RedactGitHubToken("untouched text", gitpr.PrCredentials{Kind: gitpr.PrCredentialsNone}))
	assert.Equal(t, "untouched text",
		gitpr.RedactGitHubToken("untouched text", rpCredentials()))
}

func TestCreateFetchGitHubRestTransport_buildsAUsableTransportFunction(t *testing.T) {
	assert.NotNil(t, gitpr.CreateFetchGitHubRestTransport(nil))
	assert.NotNil(t, gitpr.CreateFetchGitHubRestTransport(&gitpr.FetchTransportOptions{
		BaseUrl: "https://enterprise.example/api/",
	}))
}

/* --------------------------------------------------- the mirror contract */

// rpMirrorSimPr is one simulated GitHub PR record.
type rpMirrorSimPr struct {
	number    int
	branch    string
	base      string
	title     string
	body      string
	state     string
	updatedAt string
}

// rpMirrorSim is the scripted GitHub of the mirror test: LIVE handlers keyed
// by request identity (they read the sim state at request time), never FIFO.
type rpMirrorSim struct {
	mu         sync.Mutex
	prs        []rpMirrorSimPr
	comments   map[int][]string
	nextNumber int
}

func newRpMirrorSim() *rpMirrorSim {
	return &rpMirrorSim{comments: map[int][]string{}, nextNumber: 1}
}

// rpSimStamp mirrors the Go double's deterministic create stamp
// (inMemoryTimestamp) so the mirror comparison stays byte-exact: the double
// stamps number-derived on create and the injected clock on update.
func rpSimStamp(prNumber int) string {
	return time.UnixMilli(int64(prNumber-1) * 1000).UTC().Format("2006-01-02T15:04:05.000Z")
}

// wire renders a sim PR on the GitHub REST wire format.
func (s *rpMirrorSim) wire(pr rpMirrorSimPr) map[string]any {
	return map[string]any{
		"number":     pr.number,
		"state":      pr.state,
		"title":      pr.title,
		"body":       pr.body,
		"head":       map[string]any{"ref": pr.branch},
		"base":       map[string]any{"ref": pr.base},
		"updated_at": pr.updatedAt,
		"created_at": pr.updatedAt,
	}
}

// create appends a PR (ascending number assignment, like GitHub).
func (s *rpMirrorSim) create(payload map[string]any) rpMirrorSimPr {
	pr := rpMirrorSimPr{
		number:    s.nextNumber,
		branch:    rpMirrorString(payload, "head"),
		base:      rpMirrorString(payload, "base"),
		title:     rpMirrorString(payload, "title"),
		body:      rpMirrorString(payload, "body"),
		state:     "open",
		updatedAt: rpSimStamp(s.nextNumber),
	}
	s.nextNumber++
	s.prs = append(s.prs, pr)
	return pr
}

// patch applies a field-level update and stamps the injected clock.
func (s *rpMirrorSim) patch(number int, payload map[string]any) (rpMirrorSimPr, bool) {
	for index := range s.prs {
		if s.prs[index].number != number {
			continue
		}
		if title := rpMirrorString(payload, "title"); title != "" {
			s.prs[index].title = title
		}
		if body, present := payload["body"]; present {
			s.prs[index].body, _ = body.(string)
		}
		if state := rpMirrorString(payload, "state"); state != "" {
			s.prs[index].state = state
		}
		s.prs[index].updatedAt = rpFixedTime
		return s.prs[index], true
	}
	return rpMirrorSimPr{}, false
}

// appendComment records one comment on the PR's issue thread.
func (s *rpMirrorSim) appendComment(number int, body string) {
	s.comments[number] = append(s.comments[number], body)
}

// commentsFor returns the sim thread of a PR.
func (s *rpMirrorSim) commentsFor(number int) []string {
	return append([]string(nil), s.comments[number]...)
}

// rpMirrorString projects an optional JSON string field.
func rpMirrorString(record map[string]any, key string) string {
	text, _ := record[key].(string)
	return text
}

// rpMirrorBody parses a mirror-sim request body.
func rpMirrorBody(raw string) (map[string]any, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, fmt.Errorf("mirror sim: unparseable request body %q: %w", raw, err)
	}
	return body, nil
}

// rpMirrorRoutes wires the sim's live handlers, keyed by request identity.
func rpMirrorRoutes(scripted *rpScriptedTransport, sim *rpMirrorSim) {
	// The Go platform preserves the wire order (the TS platform re-sorted
	// ascending), so the sim answers ascending by number.
	scripted.on(rpListKey("open", 1), func(rpCapture) (rpResponse, error) {
		sim.mu.Lock()
		defer sim.mu.Unlock()
		items := make([]map[string]any, 0, len(sim.prs))
		for _, pr := range sim.prs {
			if pr.state != "open" {
				continue
			}
			items = append(items, sim.wire(pr))
		}
		return rpJSON(200, items, nil), nil
	})

	scripted.on(fmt.Sprintf("POST /repos/%s/pulls", rpRepo), func(capture rpCapture) (rpResponse, error) {
		payload, err := rpMirrorBody(capture.Body)
		if err != nil {
			return rpResponse{}, err
		}
		sim.mu.Lock()
		defer sim.mu.Unlock()
		return rpJSON(201, sim.wire(sim.create(payload)), nil), nil
	})

	// Identity-keyed live routes for the fixed PR numbers the scenario uses
	// (seeds #1/#2, then #3) and their comment threads (#1, #2, #3).
	for _, number := range []int{1, 2, 3} {
		scripted.on(fmt.Sprintf("PATCH /repos/%s/pulls/%d", rpRepo, number),
			func(capture rpCapture) (rpResponse, error) {
				payload, err := rpMirrorBody(capture.Body)
				if err != nil {
					return rpResponse{}, err
				}
				sim.mu.Lock()
				defer sim.mu.Unlock()
				pr, ok := sim.patch(number, payload)
				if !ok {
					return rpResponse{}, fmt.Errorf("mirror sim: no PR #%d", number)
				}
				return rpJSON(200, sim.wire(pr), nil), nil
			})
		scripted.on(fmt.Sprintf("POST /repos/%s/issues/%d/comments", rpRepo, number),
			func(capture rpCapture) (rpResponse, error) {
				payload, err := rpMirrorBody(capture.Body)
				if err != nil {
					return rpResponse{}, err
				}
				sim.mu.Lock()
				defer sim.mu.Unlock()
				sim.appendComment(number, rpMirrorString(payload, "body"))
				return rpJSON(201, map[string]any{
					"id": number*100 + len(sim.comments[number]),
				}, nil), nil
			})
	}
}

// rpMirrorResult is the port-level surface the two platforms must agree on.
type rpMirrorResult struct {
	openBefore  []gitpr.PrRef
	k1Open      gitpr.PrRef
	k1OpenFound bool
	k1MissFound bool
	created     gitpr.PrRef
	updated     gitpr.PrRef
	openAfter   []gitpr.PrRef
	k1OpenAfter bool
	k1MissAfter bool
	k2MissAfter bool
}

func TestGitHubRestPlatform_mirrorContractMatchesInMemoryPrPlatform(t *testing.T) {
	ctx := context.Background()
	sim := newRpMirrorSim()
	scripted := newRpScriptedTransport()
	rpMirrorRoutes(scripted, sim)
	platform := rpMakePlatform(scripted, rpCredentials())
	memory := gitpr.NewInMemoryPrPlatform(&gitpr.InMemoryPrPlatformOptions{Clock: rpClock})

	// Seed both platforms identically through the port itself.
	seeds := []gitpr.CreatePrInput{
		{Repo: rpRepo, Branch: "feature/human", Base: "main", Title: "human pr", Body: "Human work."},
		{
			Repo: rpRepo, Branch: "docsync/one", Base: "main", Title: "docs: one",
			Body: "Doc one.\n\n" + rpMarkerK1,
		},
	}
	for _, seed := range seeds {
		_, err := memory.CreatePr(ctx, seed)
		require.NoError(t, err)
		_, err = platform.CreatePr(ctx, seed)
		require.NoError(t, err)
	}

	// The Go marker lookups are OPEN-scoped (the TS default was any-state), so
	// the scenario states its lookup options explicitly on both platforms.
	lookupOpen := &gitpr.PrMarkerLookupOptions{State: gitpr.PrLookupOpen}
	normalize := func(prs []gitpr.PrRef) []gitpr.PrRef {
		if len(prs) == 0 {
			return []gitpr.PrRef{}
		}
		return prs
	}
	scenario := func(target gitpr.PrPlatformPort) rpMirrorResult {
		openBefore, err := target.ListOpenDocsPrs(ctx, rpRepo)
		require.NoError(t, err)
		k1Open, k1OpenFound, err := target.FindByPrBodyMarker(ctx, rpRepo, rpMarkerK1, lookupOpen)
		require.NoError(t, err)
		_, k1MissFound, err := target.FindByPrBodyMarker(ctx, rpRepo,
			gitpr.IdempotencyMarker("zz"), lookupOpen)
		require.NoError(t, err)
		created, err := target.CreatePr(ctx, gitpr.CreatePrInput{
			Repo: rpRepo, Branch: "docsync/two", Base: "main", Title: "docs: two",
			Body: "Doc two.\n\n" + gitpr.IdempotencyMarker("k2"),
		})
		require.NoError(t, err)
		updated, err := target.UpdatePr(ctx, created, gitpr.UpdatePrInput{
			Title: rpStrPtr("docs: two v2"),
			Body:  rpStrPtr("Doc two v2.\n\n" + gitpr.IdempotencyMarker("k2")),
		})
		require.NoError(t, err)
		require.NoError(t, target.MarkSuperseded(ctx, k1Open, created))
		require.NoError(t, target.ClosePr(ctx, created, "closing: no-op run"))
		require.NoError(t, target.AppendPrComment(ctx, k1Open, "review note"))
		openAfter, err := target.ListOpenDocsPrs(ctx, rpRepo)
		require.NoError(t, err)
		_, k1OpenAfter, err := target.FindByPrBodyMarker(ctx, rpRepo, rpMarkerK1, lookupOpen)
		require.NoError(t, err)
		_, k1MissAfter, err := target.FindByPrBodyMarker(ctx, rpRepo,
			gitpr.IdempotencyMarker("zz"), lookupOpen)
		require.NoError(t, err)
		_, k2MissAfter, err := target.FindByPrBodyMarker(ctx, rpRepo,
			gitpr.IdempotencyMarker("k2"), lookupOpen)
		require.NoError(t, err)
		return rpMirrorResult{
			openBefore:  normalize(openBefore),
			k1Open:      k1Open,
			k1OpenFound: k1OpenFound,
			k1MissFound: k1MissFound,
			created:     created,
			updated:     updated,
			openAfter:   normalize(openAfter),
			k1OpenAfter: k1OpenAfter,
			k1MissAfter: k1MissAfter,
			k2MissAfter: k2MissAfter,
		}
	}

	fromRest := scenario(platform)
	fromMemory := scenario(memory)
	assert.Equal(t, fromMemory, fromRest)

	// PORT NOTE: the TS mirror compared the comment threads directly. The Go
	// double additionally stamps a create comment and does not record close
	// comments, so each surface is pinned verbatim and the scenario comments
	// (the supersede note, the review note, the close note) ride both.
	assert.Equal(t, []string{"superseded by #3 — newer analysis (PLAN §7.3)", "review note"},
		sim.commentsFor(2))
	assert.Equal(t, []string{"closing: no-op run"}, sim.commentsFor(3))
	assert.Equal(t, []string{
		"1000-docsync-pr-created", "superseded by #3 — newer analysis (PLAN §7.3)", "review note",
	}, memory.CommentsFor(2))
	assert.Equal(t, []string{"1000-docsync-pr-created"}, memory.CommentsFor(3))
}
