package gitpr

// Ported from packages/core/src/gitpr/github-rest-platform.ts (P6-T11; P8;
// ARCH §12.5; D1-D5 platform seam): the real GitHub REST implementation of
// PrPlatformPort over net/http, plus the typed error family the whole package
// shares (check-runs.go reuses it — never forks it).
//
// The transport is an INJECTABLE FUNCTION SEAM (GitHubRestTransport):
// production wires fetchTransport (CreateFetchGitHubRestTransport), tests wire a
// double keyed by request identity — NO unit test performs network I/O (P19).
//
// INV-8 credential discipline with the kind:'none' nuance preserved: READS
// mirror an unauthenticated session (the server answers with empty results),
// WRITES fail closed with a typed auth error BEFORE any request is sent.
//
// Every error detail is REDACTED (RedactGitHubToken) — a token never survives
// into an error message. The error family preserves the TS class hierarchy
// (GitHubRestError ← Auth/NotFound/Validation/RateLimit/Transport/
// PrListTruncation) and every class answers HTTPStatus() — the ONE structural
// probe client.go's structuralStatus (withGoneRetry, backoff) reads.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

/* ------------------------------------------------------------------ constants */

// REST endpoint constants (TS GITHUB_API_BASE_URL/GITHUB_API_VERSION/GITHUB_ACCEPT/DOCSYNC_USER_AGENT).
const (
	GITHUB_API_BASE_URL  = "https://api.github.com"
	GITHUB_API_VERSION   = "2022-11-28"
	GITHUB_ACCEPT        = "application/vnd.github+json"
	DOCSYNC_USER_AGENT   = "docsync/1.0"
	GitHubPerPage        = 100
	MaxGitHubPRListPages = 10
	// GitHubTokenRedacted is the redaction placeholder.
	GitHubTokenRedacted = "[REDACTED]"
)

// The TS error-class codes, preserved verbatim on the Go error types
// (CONVENTIONS.md §Errors — the codes are not in internal/docutil, so they ride
// the Go error types instead of inventing new docutil codes; docutil.CodeOf
// resolves them because they are what the coded error carries).
const (
	CodeGitHubRest            = "EDOCSYNC_GITHUB_REST"
	CodeGitHubAuth            = "EDOCSYNC_GITHUB_AUTH"
	CodeGitHubNotFound        = "EDOCSYNC_GITHUB_NOT_FOUND"
	CodeGitHubValidation      = "EDOCSYNC_GITHUB_VALIDATION"
	CodeGitHubRateLimit       = "EDOCSYNC_GITHUB_RATE_LIMIT"
	CodeGitHubTransport       = "EDOCSYNC_GITHUB_TRANSPORT"
	CodeGitHubPrListTruncated = "EDOCSYNC_GITHUB_PR_LIST_TRUNCATED"
)

/* ------------------------------------------------------------- error family */

// GitHubRestErrorInit carries the typed-error tail (TS init opts).
type GitHubRestErrorInit struct {
	Detail string
	Status *int
}

// GitHubRateLimitErrorInit carries the rate-limit error tail.
type GitHubRateLimitErrorInit struct {
	Detail                     string
	Status                     *int
	RetryAfterSeconds          *float64
	RatelimitResetEpochSeconds *float64
}

// GitHubRestError is the common base (TS abstract GitHubRestError): message,
// redacted detail, the structural HTTP status (absent ⇒ 0), and the coded error
// (so docutil.CodeOf resolves the class code). The TS rendered `message` is
// `init.detail || message` — reproduced verbatim by the coded error.
type GitHubRestError struct {
	Message string
	Detail  string
	Status  int

	coded *docutil.Error
}

// Error renders the coded error in docutil house style.
func (e *GitHubRestError) Error() string { return e.coded.Error() }

// Code reports the machine-readable class code (EDOCSYNC_GITHUB_*).
func (e *GitHubRestError) Code() string { return e.coded.Code }

// Unwrap exposes the coded error so docutil.CodeOf resolves the class code.
func (e *GitHubRestError) Unwrap() error { return e.coded }

// HTTPStatus is the structural `.status` probe client.go's structuralStatus
// reads (the TS rule: only the numeric status matters, never the class).
func (e *GitHubRestError) HTTPStatus() int { return e.Status }

// newGitHubError builds one typed error of the given class code.
func newGitHubError(classCode, message string, init GitHubRestErrorInit) *GitHubRestError {
	err := &GitHubRestError{Message: message, Detail: init.Detail}
	if init.Status != nil {
		err.Status = *init.Status
	}
	// TS: `this.message = init.detail || message` (the detail, when present, is
	// the rendered message).
	rendered := message
	if init.Detail != "" {
		rendered = init.Detail
	}
	err.coded = docutil.New(classCode, rendered)
	return err
}

// NewGitHubRestError builds the base typed error (TS GitHubRestError).
func NewGitHubRestError(message string, init GitHubRestErrorInit) *GitHubRestError {
	return newGitHubError(CodeGitHubRest, message, init)
}

// GitHubAuthError is the credential-class failure (TS GitHubAuthError).
type GitHubAuthError struct{ *GitHubRestError }

// Unwrap exposes the base error (class-hierarchy walk for errors.As).
func (e *GitHubAuthError) Unwrap() error { return e.GitHubRestError }

// NewGitHubAuthError builds the auth refusal (TS code EDOCSYNC_GITHUB_AUTH).
func NewGitHubAuthError(message string, init GitHubRestErrorInit) *GitHubAuthError {
	return &GitHubAuthError{newGitHubError(CodeGitHubAuth, message, init)}
}

// GitHubNotFoundError is the 404-class failure (TS GitHubNotFoundError).
type GitHubNotFoundError struct{ *GitHubRestError }

// Unwrap exposes the base error.
func (e *GitHubNotFoundError) Unwrap() error { return e.GitHubRestError }

// NewGitHubNotFoundError builds the not-found refusal.
func NewGitHubNotFoundError(message string, init GitHubRestErrorInit) *GitHubNotFoundError {
	return &GitHubNotFoundError{newGitHubError(CodeGitHubNotFound, message, init)}
}

// GitHubValidationError is the 422-class failure (TS GitHubValidationError).
type GitHubValidationError struct{ *GitHubRestError }

// Unwrap exposes the base error.
func (e *GitHubValidationError) Unwrap() error { return e.GitHubRestError }

// NewGitHubValidationError builds the validation refusal.
func NewGitHubValidationError(message string, init GitHubRestErrorInit) *GitHubValidationError {
	return &GitHubValidationError{newGitHubError(CodeGitHubValidation, message, init)}
}

// GitHubRateLimitError is the 429-class failure carrying the rate-limit
// retry hints (TS GitHubRateLimitError).
type GitHubRateLimitError struct {
	*GitHubRestError
	RetryAfterSeconds          *float64
	RatelimitResetEpochSeconds *float64
}

// Unwrap exposes the base error.
func (e *GitHubRateLimitError) Unwrap() error { return e.GitHubRestError }

// NewGitHubRateLimitError builds the rate-limit refusal.
func NewGitHubRateLimitError(message string, init GitHubRateLimitErrorInit) *GitHubRateLimitError {
	base := newGitHubError(CodeGitHubRateLimit, message, GitHubRestErrorInit{Detail: init.Detail, Status: init.Status})
	return &GitHubRateLimitError{
		GitHubRestError:            base,
		RetryAfterSeconds:          init.RetryAfterSeconds,
		RatelimitResetEpochSeconds: init.RatelimitResetEpochSeconds,
	}
}

// GitHubTransportError is the transport-class failure (TS GitHubTransportError):
// the fetch itself threw, or the server answered 5xx. `cause` mirrors the TS
// `override readonly cause`.
type GitHubTransportError struct {
	*GitHubRestError
	Cause error
}

// Unwrap exposes the base error (class-hierarchy walk).
func (e *GitHubTransportError) Unwrap() error { return e.GitHubRestError }

// NewGitHubTransportError builds the transport refusal.
func NewGitHubTransportError(message string, init GitHubRestErrorInit) *GitHubTransportError {
	return &GitHubTransportError{GitHubRestError: newGitHubError(CodeGitHubTransport, message, init)}
}

// GitHubPrListTruncationError is the fail-closed listing-cap failure (TS
// GitHubPrListTruncationError): the 10-page safety cap was reached — failing
// closed instead of silently dropping results.
type GitHubPrListTruncationError struct {
	*GitHubRestError
	CollectedCount int
	Repo           string
}

// Unwrap exposes the base error.
func (e *GitHubPrListTruncationError) Unwrap() error { return e.GitHubRestError }

// newGitHubPrListTruncationError builds the truncation refusal.
func newGitHubPrListTruncationError(collectedCount int, repo string) *GitHubPrListTruncationError {
	return &GitHubPrListTruncationError{
		GitHubRestError: newGitHubError(CodeGitHubPrListTruncated,
			"GitHub REST platform: PR listing exceeded the "+strconv.Itoa(MaxGitHubPRListPages)+
				"-page safety cap — failing closed instead of silently dropping results",
			GitHubRestErrorInit{
				Detail: "collected " + strconv.Itoa(collectedCount) + " PRs for " + repo +
					" before truncation",
			}),
		CollectedCount: collectedCount,
		Repo:           repo,
	}
}

// RedactGitHubToken replaces every occurrence of the caller's credentials with
// GitHubTokenRedacted (TS redactGitHubToken / RedactGitHubToken).
func RedactGitHubToken(text string, credentials PrCredentials) string {
	out := text
	for _, secret := range []string{credentials.Token, credentials.InstallationID} {
		if len(secret) > 0 && strings.Contains(out, secret) {
			out = strings.ReplaceAll(out, secret, GitHubTokenRedacted)
		}
	}
	return out
}

/* ------------------------------------------------------------------ the port */

// GitHubRestRequest is one injected transport request (TS
// FetchTransportRequestBody): a nil Body means no request body.
type GitHubRestRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    *string
}

// GitHubRestResponse is the injected transport's answer. Header keys are
// lowercased (the TS Headers.forEach contract).
type GitHubRestResponse struct {
	Status  int
	Headers map[string]string
	Body    string
}

// GitHubRestTransport is the transport seam (TS FetchTransport): an injectable
// function — production wires the fetch transport, tests wire doubles. It
// carries no ctx (the seam's call shape is request-only, as the check-run
// client's calls show); the fetch transport applies its own deadline.
type GitHubRestTransport func(request GitHubRestRequest) (GitHubRestResponse, error)

// FetchTransportOptions are the production transport's options.
type FetchTransportOptions struct {
	// BaseUrl is the REST base URL (default GITHUB_API_BASE_URL).
	BaseUrl string
	// TimeoutMs is the per-request deadline (default 10_000).
	TimeoutMs int
}

// CreateFetchGitHubRestTransport builds the net/http transport (TS
// fetchTransport(): a real fetch over the REST seam).
func CreateFetchGitHubRestTransport(opts *FetchTransportOptions) GitHubRestTransport {
	baseUrl := GITHUB_API_BASE_URL
	timeoutMs := 10_000
	if opts != nil {
		if opts.BaseUrl != "" {
			baseUrl = opts.BaseUrl
		}
		if opts.TimeoutMs > 0 {
			timeoutMs = opts.TimeoutMs
		}
	}
	client := &http.Client{}
	return func(request GitHubRestRequest) (GitHubRestResponse, error) {
		requestURL := request.URL
		if !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			requestURL = baseUrl + requestURL
		}
		var bodyReader io.Reader
		if request.Body != nil {
			bodyReader = bytes.NewReader([]byte(*request.Body))
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()
		httpRequest, err := http.NewRequestWithContext(ctx, request.Method, requestURL, bodyReader)
		if err != nil {
			return GitHubRestResponse{}, err
		}
		for key, value := range request.Headers {
			httpRequest.Header.Set(key, value)
		}
		response, err := client.Do(httpRequest)
		if err != nil {
			return GitHubRestResponse{}, err
		}
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if err != nil {
			return GitHubRestResponse{}, err
		}
		headers := make(map[string]string, len(response.Header))
		for key, values := range response.Header {
			if len(values) > 0 {
				headers[strings.ToLower(key)] = values[0]
			}
		}
		return GitHubRestResponse{
			Status:  response.StatusCode,
			Headers: headers,
			Body:    string(raw),
		}, nil
	}
}

/* ------------------------------------------------------------ the platform */

// GitHubRestPlatformDeps is everything injected (TS
// GitHubRestPlatformDependencies): credentials, the transport seam, the base
// URL, and the clock (stamps nothing reachable today, carried for parity).
type GitHubRestPlatformDeps struct {
	Credentials PrCredentials
	Transport   GitHubRestTransport
	BaseUrl     string
	Clock       func() string
}

// githubRestEpochClock is the TS default clock — new Date(0).toISOString().
func githubRestEpochClock() string {
	return time.Unix(0, 0).UTC().Format("2006-01-02T15:04:05.000Z")
}

// repoMemory remembers a PR's repo + branch (TS remember/repoFor) so the
// idempotent comment helpers never trust a caller-chosen ref.
type repoMemory struct {
	repo   string
	branch string
}

// githubRestPlatform is the constructed platform (TS `platform` record).
type githubRestPlatform struct {
	credentials PrCredentials
	transport   GitHubRestTransport
	baseUrl     string
	clock       func() string

	mu       sync.Mutex
	repoByPr map[int]repoMemory
}

// CreateGitHubRestPlatform builds the GitHub REST platform (TS
// createGitHubRestPlatform).
func CreateGitHubRestPlatform(deps GitHubRestPlatformDeps) PrPlatformPort {
	transport := deps.Transport
	if transport == nil {
		transport = CreateFetchGitHubRestTransport(&FetchTransportOptions{BaseUrl: deps.BaseUrl})
	}
	baseUrl := deps.BaseUrl
	if baseUrl == "" {
		baseUrl = GITHUB_API_BASE_URL
	}
	baseUrl = strings.TrimRight(baseUrl, "/")
	clock := deps.Clock
	if clock == nil {
		clock = githubRestEpochClock
	}
	return &githubRestPlatform{
		credentials: deps.Credentials,
		transport:   transport,
		baseUrl:     baseUrl,
		clock:       clock,
		repoByPr:    make(map[int]repoMemory),
	}
}

// repoPath validates and encodes the owner/name argument (fail closed).
func (p *githubRestPlatform) repoPath(repo string) (string, error) {
	if len(repo) == 0 || !strings.Contains(repo, "/") {
		return "", NewGitHubRestError("GitHub REST platform: repo must be a nonempty owner/name string",
			GitHubRestErrorInit{Detail: "malformed repo argument"})
	}
	segments := strings.Split(repo, "/")
	encoded := make([]string, 0, len(segments))
	for _, segment := range segments {
		encoded = append(encoded, url.PathEscape(segment))
	}
	return strings.Join(encoded, "/"), nil
}

// requireCredentials is the INV-8 write guard: kind 'none' fails closed BEFORE
// any request is sent.
func (p *githubRestPlatform) requireCredentials(operation string) error {
	if p.credentials.Kind == PrCredentialsNone {
		return NewGitHubAuthError("GitHub REST platform: "+operation+" requires credentials",
			GitHubRestErrorInit{Detail: "no credentials configured (kind: none) — writes fail closed (INV-8)"})
	}
	return nil
}

// redact routes text through the shared token redactor (the token NEVER
// survives into an error detail).
func (p *githubRestPlatform) redact(text string) string {
	return RedactGitHubToken(text, p.credentials)
}

// remember binds a PR number to its repo+branch so the idempotent comment
// helpers never trust a caller-chosen ref.
func (p *githubRestPlatform) remember(pr PrRef, repo string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.repoByPr[pr.Number] = repoMemory{repo: repo, branch: pr.Branch}
}

// repoFor resolves the remembered repo of a PR.
func (p *githubRestPlatform) repoFor(pr PrRef) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.repoByPr[pr.Number].repo
}

// send performs one REST call through the injected seam and maps every non-2xx
// into the typed error family (details always redacted).
func (p *githubRestPlatform) send(ctx context.Context, method, path string, body *string) (GitHubRestResponse, error) {
	headers := map[string]string{
		"Accept":               GITHUB_ACCEPT,
		"X-GitHub-Api-Version": GITHUB_API_VERSION,
		"User-Agent":           DOCSYNC_USER_AGENT,
	}
	if p.credentials.Kind != PrCredentialsNone {
		headers["Authorization"] = "Bearer " + p.credentials.Token
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	response, err := p.transport(GitHubRestRequest{
		Method:  method,
		URL:     p.baseUrl + path,
		Headers: headers,
		Body:    body,
	})
	if err != nil {
		cause := err.Error()
		if cause == "" {
			cause = "unknown transport failure"
		}
		return GitHubRestResponse{}, NewGitHubTransportError(
			"GitHub REST transport failure for "+method+" "+path,
			GitHubRestErrorInit{Detail: p.redact(cause)})
	}
	if response.Status >= 200 && response.Status < 300 {
		return response, nil
	}
	return GitHubRestResponse{}, p.restErrorFor(method, path, response)
}

// restErrorFor is the typed error mapping for every non-2xx (TS restErrorFor):
// 422 → validation, 401/403 → auth, 404 → not-found, 429 → rate limit (with the
// retry hints), 5xx → transport, else the base error. Details are ALWAYS
// redacted.
func (p *githubRestPlatform) restErrorFor(method, path string, response GitHubRestResponse) error {
	status := response.Status
	message := fmt.Sprintf("GitHub REST %s %s failed with status %d", method, path, status)
	githubMessage := githubMessageOf(response.Body)
	if status == 422 {
		// TS `githubMessage ?? response.body` — an empty string message does
		// NOT fall back, so the pointer semantics are preserved.
		detail := response.Body
		if githubMessage != nil {
			detail = *githubMessage
		}
		statusValue := status
		return NewGitHubValidationError(message, GitHubRestErrorInit{Detail: p.redact(detail), Status: &statusValue})
	}
	contextText := "response body: " + githubBodyExcerpt(response.Body)
	if githubMessage != nil {
		contextText = "GitHub said: " + *githubMessage
	}
	detail := p.redact(contextText)
	statusValue := status
	switch {
	case status == 401 || status == 403:
		return NewGitHubAuthError(message, GitHubRestErrorInit{Detail: detail, Status: &statusValue})
	case status == 404:
		return NewGitHubNotFoundError(message, GitHubRestErrorInit{Detail: detail, Status: &statusValue})
	case status == 429:
		init := GitHubRateLimitErrorInit{Detail: detail, Status: &statusValue}
		if retryAfter, ok := githubNumericHeader(response.Headers, "retry-after"); ok {
			init.RetryAfterSeconds = &retryAfter
		}
		if reset, ok := githubNumericHeader(response.Headers, "x-ratelimit-reset"); ok {
			init.RatelimitResetEpochSeconds = &reset
		}
		return NewGitHubRateLimitError(message, init)
	case status >= 500:
		return NewGitHubTransportError(message, GitHubRestErrorInit{Detail: detail, Status: &statusValue})
	default:
		return NewGitHubRestError(message, GitHubRestErrorInit{Detail: detail, Status: &statusValue})
	}
}

// parseSuccessJson parses a 2xx JSON body (TS parseSuccessJson): a non-JSON
// body is a TRANSPORT-class failure (fail closed), never a guessed projection.
func (p *githubRestPlatform) parseSuccessJson(response GitHubRestResponse) (map[string]any, error) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(response.Body), &parsed); err != nil {
		return nil, NewGitHubTransportError("GitHub REST response was not valid JSON",
			GitHubRestErrorInit{Detail: p.redact(githubBodyExcerpt(response.Body))})
	}
	return parsed, nil
}

// ListOpenDocsPrs lists the open docsync PRs (TS listOpenDocsPrs) — used for the
// open-PR cap governor.
func (p *githubRestPlatform) ListOpenDocsPrs(ctx context.Context, repo string) ([]PrRef, error) {
	if p.credentials.Kind == PrCredentialsNone {
		// The kind:'none' nuance — an unauthenticated session mirrors what the
		// server would return; the platform never guesses.
		return []PrRef{}, nil
	}
	pulls, err := p.listAllPulls(ctx, repo, "open")
	if err != nil {
		return nil, err
	}
	open := make([]PrRef, 0, len(pulls))
	for _, pull := range pulls {
		if !strings.HasPrefix(pull.Branch, SelfTriggerBranchPrefix) {
			continue
		}
		open = append(open, pull)
		p.remember(pull, repo)
	}
	return open, nil
}

// FindByPrBodyMarker finds the marker-keyed PR (TS findByPrBodyMarker): the
// idempotency seam — a hidden comment on the PR body is the key.
func (p *githubRestPlatform) FindByPrBodyMarker(
	ctx context.Context,
	repo string,
	marker string,
	opts *PrMarkerLookupOptions,
) (PrRef, bool, error) {
	if len(marker) == 0 {
		return PrRef{}, false, NewGitHubRestError(
			"GitHub REST platform: marker must be a nonempty string",
			GitHubRestErrorInit{Detail: "malformed marker argument"})
	}
	if p.credentials.Kind == PrCredentialsNone {
		return PrRef{}, false, nil
	}
	state := "open"
	if opts != nil && opts.State != "" {
		state = string(opts.State)
	}
	pulls, err := p.listAllPulls(ctx, repo, state)
	if err != nil {
		return PrRef{}, false, err
	}
	for _, pull := range pulls {
		if !strings.Contains(pull.Body, marker) {
			continue
		}
		p.remember(pull, repo)
		return pull, true, nil
	}
	return PrRef{}, false, nil
}

// CreatePr opens a PR (TS createPr).
func (p *githubRestPlatform) CreatePr(ctx context.Context, input CreatePrInput) (PrRef, error) {
	if err := p.requireCredentials("createPr"); err != nil {
		return PrRef{}, err
	}
	if len(input.Title) == 0 {
		return PrRef{}, NewGitHubValidationError(
			"GitHub REST platform: createPr requires a nonempty title",
			GitHubRestErrorInit{Detail: "malformed title argument"})
	}
	if len(input.Branch) == 0 {
		return PrRef{}, NewGitHubValidationError(
			"GitHub REST platform: createPr requires a nonempty branch",
			GitHubRestErrorInit{Detail: "malformed branch argument"})
	}
	if len(input.Base) == 0 {
		return PrRef{}, NewGitHubValidationError(
			"GitHub REST platform: createPr requires a nonempty base",
			GitHubRestErrorInit{Detail: "malformed base argument"})
	}
	path, err := p.repoPath(input.Repo)
	if err != nil {
		return PrRef{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"title":                 input.Title,
		"head":                  input.Branch,
		"base":                  input.Base,
		"body":                  input.Body,
		"maintainer_can_modify": true,
	})
	if err != nil {
		return PrRef{}, NewGitHubTransportError("GitHub REST platform: request could not be serialized",
			GitHubRestErrorInit{Detail: err.Error()})
	}
	body := string(payload)
	response, err := p.send(ctx, "POST", "/repos/"+path+"/pulls", &body)
	if err != nil {
		return PrRef{}, err
	}
	parsed, err := p.parseSuccessJson(response)
	if err != nil {
		return PrRef{}, err
	}
	pr, err := p.parsePr(parsed)
	if err != nil {
		return PrRef{}, err
	}
	p.remember(pr, input.Repo)
	return pr, nil
}

// UpdatePr updates an existing PR (TS updatePr).
func (p *githubRestPlatform) UpdatePr(ctx context.Context, pr PrRef, input UpdatePrInput) (PrRef, error) {
	if err := p.requireCredentials("updatePr"); err != nil {
		return PrRef{}, err
	}
	if input.Title == nil && input.Body == nil {
		return PrRef{}, NewGitHubValidationError(
			"GitHub REST platform: updatePr requires title or body (malformed input)",
			GitHubRestErrorInit{Detail: "neither title nor body supplied"})
	}
	if !strings.HasPrefix(pr.Branch, SelfTriggerBranchPrefix) {
		return PrRef{}, NewGitHubRestError(
			"GitHub REST platform: refusing to update a non-docsync branch (invariant 1: rule 1)",
			GitHubRestErrorInit{Detail: "branch '" + pr.Branch + "' is not docsync-owned"})
	}
	repo := p.repoFor(pr)
	path, err := p.repoPath(repo)
	if err != nil {
		return PrRef{}, err
	}
	payload := map[string]any{}
	if input.Title != nil {
		payload["title"] = *input.Title
	}
	if input.Body != nil {
		payload["body"] = *input.Body
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return PrRef{}, NewGitHubTransportError("GitHub REST platform: request could not be serialized",
			GitHubRestErrorInit{Detail: err.Error()})
	}
	body := string(raw)
	response, err := p.send(ctx, "PATCH", "/repos/"+path+"/pulls/"+strconv.Itoa(pr.Number), &body)
	if err != nil {
		return PrRef{}, err
	}
	parsed, err := p.parseSuccessJson(response)
	if err != nil {
		return PrRef{}, err
	}
	updated, err := p.parsePr(parsed)
	if err != nil {
		// Fail closed: a malformed update response must not surface as a
		// zero PrRef with a nil error (the TS platform refuses it).
		return PrRef{}, err
	}
	p.remember(updated, repo)
	return updated, nil
}

// MarkSuperseded runs the lifecycle transition (TS markSuperseded): a comment
// quoting the newer PR (or a caller note), then the close state.
func (p *githubRestPlatform) MarkSuperseded(ctx context.Context, pr, newerPr PrRef) error {
	if err := p.requireCredentials("markSuperseded"); err != nil {
		return err
	}
	if pr.Number < 1 || newerPr.Number < 1 {
		return NewGitHubRestError(
			"GitHub REST platform: markSuperseded requires positive PR numbers",
			GitHubRestErrorInit{Detail: "malformed PR reference"})
	}
	repo := p.repoFor(pr)
	path, err := p.repoPath(repo)
	if err != nil {
		return err
	}
	note := fmt.Sprintf("superseded by #%d — newer analysis (PLAN §7.3)", newerPr.Number)
	if err := p.appendComment(ctx, path, pr.Number, note); err != nil {
		return err
	}
	body := `{"state":"closed"}`
	response, err := p.send(ctx, "PATCH", "/repos/"+path+"/pulls/"+strconv.Itoa(pr.Number), &body)
	if err != nil {
		return err
	}
	if _, err := p.parseSuccessJson(response); err != nil {
		return err
	}
	return nil
}

// ClosePr closes a PR with a comment (TS closePr).
func (p *githubRestPlatform) ClosePr(ctx context.Context, pr PrRef, comment string) error {
	if err := p.requireCredentials("closePr"); err != nil {
		return err
	}
	if pr.Number < 1 {
		return NewGitHubRestError("GitHub REST platform: closePr requires a positive PR number",
			GitHubRestErrorInit{Detail: "malformed PR reference"})
	}
	repo := p.repoFor(pr)
	path, err := p.repoPath(repo)
	if err != nil {
		return err
	}
	if len(comment) > 0 {
		if err := p.appendComment(ctx, path, pr.Number, comment); err != nil {
			return err
		}
	}
	body := `{"state":"closed"}`
	response, err := p.send(ctx, "PATCH", "/repos/"+path+"/pulls/"+strconv.Itoa(pr.Number), &body)
	if err != nil {
		return err
	}
	if _, err := p.parseSuccessJson(response); err != nil {
		return err
	}
	return nil
}

// AppendPrComment appends a comment (TS appendPrComment): INVALID-AUTH
// incidents and marker comments ride here.
func (p *githubRestPlatform) AppendPrComment(ctx context.Context, pr PrRef, body string) error {
	if err := p.requireCredentials("appendPrComment"); err != nil {
		return err
	}
	if pr.Number < 1 {
		return NewGitHubRestError("GitHub REST platform: appendPrComment requires a positive PR number",
			GitHubRestErrorInit{Detail: "malformed PR reference"})
	}
	repo := p.repoFor(pr)
	path, err := p.repoPath(repo)
	if err != nil {
		return err
	}
	return p.appendComment(ctx, path, pr.Number, body)
}

// appendComment posts one comment to the issue-thread endpoint.
func (p *githubRestPlatform) appendComment(ctx context.Context, path string, number int, text string) error {
	if text == "" {
		return NewGitHubRestError("GitHub REST platform: refusing to append an empty comment",
			GitHubRestErrorInit{Detail: "malformed comment body"})
	}
	payload, err := json.Marshal(map[string]any{"body": text})
	if err != nil {
		return NewGitHubTransportError("GitHub REST platform: request could not be serialized",
			GitHubRestErrorInit{Detail: err.Error()})
	}
	requestBody := string(payload)
	response, err := p.send(ctx, "POST", "/repos/"+path+"/issues/"+strconv.Itoa(number)+"/comments", &requestBody)
	if err != nil {
		return err
	}
	if _, err := p.parseSuccessJson(response); err != nil {
		return err
	}
	return nil
}

/* -------------------------------------------------------------- PR listing */

// listPullsPage lists one page (TS listPullsPage).
func (p *githubRestPlatform) listPullsPage(ctx context.Context, repo, state string, page int) ([]PrRef, string, error) {
	path, err := p.repoPath(repo)
	if err != nil {
		return nil, "", err
	}
	listPath := "/repos/" + path + "/pulls?state=" + url.QueryEscape(state) +
		"&per_page=" + strconv.Itoa(GitHubPerPage) + "&page=" + strconv.Itoa(page)
	response, err := p.send(ctx, "GET", listPath, nil)
	if err != nil {
		return nil, "", err
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(response.Body), &entries); err != nil {
		return nil, "", NewGitHubTransportError("GitHub REST response was not valid JSON",
			GitHubRestErrorInit{Detail: p.redact(githubBodyExcerpt(response.Body))})
	}
	pulls := make([]PrRef, 0, len(entries))
	for _, entry := range entries {
		pr, err := p.parsePr(entry)
		if err != nil {
		}
		pulls = append(pulls, pr)
	}
	return pulls, response.Headers["link"], nil
}

// linkHeaderHasNext reports whether a Link header advertises a next page (TS
// linkHeaderHasNext).
func linkHeaderHasNext(header string) bool {
	if header == "" {
		return false
	}
	segments := strings.Split(header, ",")
	for _, raw := range segments {
		segment := strings.TrimSpace(raw)
		if segment == "" {
			continue
		}
		if !strings.Contains(segment, "rel=") {
			continue
		}
		if !strings.Contains(segment, "next") {
			continue
		}
		if !strings.HasPrefix(segment, "<http") {
			continue
		}
		return true
	}
	return false
}

// listAllPulls follows Link rel="next" pages up to the safety cap (TS
// listAllPulls): silent storage is worse than a loud failure, so the cap is
// fail-closed.
func (p *githubRestPlatform) listAllPulls(ctx context.Context, repo, state string) ([]PrRef, error) {
	collected := []PrRef{}
	for page := 1; page <= MaxGitHubPRListPages; page++ {
		pulls, link, err := p.listPullsPage(ctx, repo, state, page)
		if err != nil {
			return nil, err
		}
		collected = append(collected, pulls...)
		if !linkHeaderHasNext(link) {
			return collected, nil
		}
	}
	return nil, newGitHubPrListTruncationError(len(collected), repo)
}

/* ------------------------------------------------------------------ helpers */

// githubMessageOf extracts the GitHub `message` field (nil when absent).
func githubMessageOf(body string) *string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil
	}
	message, ok := parsed["message"]
	if !ok {
		return nil
	}
	text, ok := message.(string)
	if !ok {
		return nil
	}
	return &text
}

// githubNumericHeader parses a numeric response header (TS numericHeader): the
// canonical name and the underscore form both resolve; missing/unparseable ⇒
// absent.
func githubNumericHeader(headers map[string]string, name string) (float64, bool) {
	underscore := strings.ReplaceAll(name, "-", "_")
	raw, ok := headers[name]
	if !ok {
		raw, ok = headers[underscore]
	}
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// githubBodyExcerpt bounds an echoed response body at 500 characters.
func githubBodyExcerpt(body string) string {
	runes := []rune(body)
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return body
}

// githubStringField projects an optional JSON string field (top level).
func githubStringField(record map[string]any, key string) string {
	value, ok := record[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// githubNestedString projects a one-level nested `parent.child` string field
// (the PR payload's head.ref / base.ref).
func githubNestedString(record map[string]any, parent, child string) string {
	container, ok := record[parent].(map[string]any)
	if !ok {
		return ""
	}
	return githubStringField(container, child)
}

// parsePr projects a GitHub pull-request payload into PrRef (TS parsePr): a
// malformed payload is a fail-closed typed error whose detail carries the
// REDACTED, bounded evidence (the token can never survive).
func (p *githubRestPlatform) parsePr(record map[string]any) (PrRef, error) {
	malformed := func() error {
		return NewGitHubRestError("GitHub REST platform: malformed PR payload",
			GitHubRestErrorInit{
				Detail: p.redact(githubBodyExcerpt(githubStringifyRecord(record))),
			})
	}
	numberValue, ok := record["number"]
	if !ok {
		return PrRef{}, malformed()
	}
	number, ok := githubFiniteInt(numberValue)
	if !ok {
		return PrRef{}, malformed()
	}
	state := githubStringField(record, "state")
	if state == "" {
		return PrRef{}, malformed()
	}
	return PrRef{
		Number:    number,
		Branch:    githubNestedString(record, "head", "ref"),
		Base:      githubNestedString(record, "base", "ref"),
		Title:     githubStringField(record, "title"),
		Body:      githubStringField(record, "body"),
		State:     PrState(state),
		UpdatedAt: githubStringField(record, "updated_at"),
	}, nil
}

// githubStringifyRecord renders a record for the bounded error evidence (TS
// JSON.stringify(record).slice(0, 500)).
func githubStringifyRecord(record map[string]any) string {
	raw, err := json.Marshal(record)
	if err != nil {
		return "<unserializable>"
	}
	return string(raw)
}

// githubFiniteInt projects a JSON number into a positive int (TS
// Number.isInteger(value) && value >= 1).
func githubFiniteInt(value any) (int, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if number != float64(int(number)) || int(number) < 1 {
		return 0, false
	}
	return int(number), true
}
