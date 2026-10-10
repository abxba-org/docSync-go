package gitpr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Ported from core/src/gitpr/check-runs.ts — GitHub check-run REST primitives
// (P8-T08..T11 prep; ARCH §11.3; PLAN §8.3; D10/D8/D14 conclusion mapping lives
// in `orchestrator/check-conclusion.ts`).
//
// Thin REST layer over the SAME transport seam and typed error family as
// github-rest-platform.go — error classes and redactGitHubToken are REUSED,
// never forked. Endpoints:
//
//   - FindCheckRunsForRef → GET /repos/{repo}/commits/{ref}/check-runs
//     filtered with `check_name=docsync/check` (idempotent-lookup seam)
//   - CreateCheckRun → POST /repos/{repo}/check-runs
//   - UpdateCheckRun → PATCH /repos/{repo}/check-runs/{id}
//     (never sends `name`/`head_sha` — GitHub rejects them on PATCH)
//
// Deliberately MINIMAL (governors own backoff; the service layer owns the
// idempotent upsert `find → create-or-update` and the PR rate-limit gates):
// NO retries, NO pagination, NO flood logic here. `kind:'none'` credentials
// fail closed with an auth error on writes (INV-8 — reads stay allowed and let
// the server answer 401 into the same typed family).
//
// SEAM: the transport (GitHubRestTransport, declared in
// github-rest-platform.go) is an injectable function — production wires the
// fetch transport, tests wire fakes. No unit test performs network I/O.

// DOCSYNCCheckName is the stable check-run name every docsync CI surface
// publishes under.
const DOCSYNCCheckName = "docsync/check"

// CheckRunAction is the escalation action shape attached to
// enforcement-failure check runs (D10).
type CheckRunAction struct {
	Label      string `json:"label"`
	Identifier string `json:"identifier"`
}

// DOCSYNCGeneratePatchAction is the single escalation action docsync may
// attach (P8 prep; D10).
var DOCSYNCGeneratePatchAction = CheckRunAction{
	Label:      "Generate documentation patch",
	Identifier: "docsync/generate-patch",
}

// CheckRunStatus is the GitHub check-run status union.
type CheckRunStatus string

// The three check-run statuses.
const (
	CheckRunStatusQueued     CheckRunStatus = "queued"
	CheckRunStatusInProgress CheckRunStatus = "in_progress"
	CheckRunStatusCompleted  CheckRunStatus = "completed"
)

// CheckRunConclusion mirrors the TS CheckRunConclusion union of
// orchestrator/check-conclusion.ts — the GitHub check-run conclusions this
// engine may emit. D10 default: advisory mode reports drift as `neutral`
// (never failing CI); `failure` only under enforcement.
//
// Swap point: internal/orchestrator's port of check-conclusion.ts is the
// eventual canonical home of this vocabulary; the local declaration keeps this
// package free of an L5→L5 import until that package lands.
type CheckRunConclusion string

// The three check-run conclusions.
const (
	CheckRunConclusionSuccess CheckRunConclusion = "success"
	CheckRunConclusionNeutral CheckRunConclusion = "neutral"
	CheckRunConclusionFailure CheckRunConclusion = "failure"
)

// CheckRunOutputPayload is the check-run `output` payload (title/summary/text
// — see buildCheckPayload of the service layer).
type CheckRunOutputPayload struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

// CreateCheckRunInput is the input of CheckRunClient.CreateCheckRun.
type CreateCheckRunInput struct {
	// HeadSha is the commit SHA the check run attaches to (required by GitHub).
	HeadSha string
	// Status is queued | in_progress | completed.
	Status CheckRunStatus
	// Conclusion is required when status is completed (mapped by the
	// orchestrator's resolveCheckConclusion).
	Conclusion *CheckRunConclusion
	// Output is the rendered payload.
	Output *CheckRunOutputPayload
	// EscalationEnabled attaches the escalation action
	// (DOCSYNCGeneratePatchAction) — only ever under enforcement failure (D10).
	// Absent/false → the request body carries NO `actions` key at all.
	EscalationEnabled bool
	// StartedAt overrides started_at; it defaults to the injected clock.
	StartedAt *string
	// CompletedAt overrides completed_at; it defaults to the clock when
	// completed.
	CompletedAt *string
}

// UpdateCheckRunInput is the input of CheckRunClient.UpdateCheckRun — no
// name/head_sha.
type UpdateCheckRunInput struct {
	Status            *CheckRunStatus
	Conclusion        *CheckRunConclusion
	Output            *CheckRunOutputPayload
	EscalationEnabled bool
	CompletedAt       *string
}

// GitHubCheckRun is the minimal projection of a GitHub check-run object this
// client returns.
type GitHubCheckRun struct {
	ID         int     `json:"id"`
	Name       *string `json:"name,omitempty"`
	Status     *string `json:"status,omitempty"`
	Conclusion *string `json:"conclusion,omitempty"`
	HeadSha    *string `json:"head_sha,omitempty"`
	HTMLURL    *string `json:"html_url,omitempty"`
}

// CheckRunClientDeps is everything injected (no ambient state, P19).
type CheckRunClientDeps struct {
	// Credentials is the sealed credential union (INV-8); `kind:'none'` fails
	// closed on writes.
	Credentials PrCredentials
	// Transport is the seam (default: global fetch via
	// CreateFetchGitHubRestTransport).
	Transport GitHubRestTransport
	// BaseURL is the REST base URL (default GITHUB_API_BASE_URL).
	BaseURL string
	// Clock is the injected ISO-8601 clock for started_at/completed_at.
	Clock func() string
}

// CheckRunClient is the check-run REST client (see the module doc for the
// endpoint map).
type CheckRunClient interface {
	// FindCheckRunsForRef lists docsync/check runs attached to ref (the upsert
	// lookup seam).
	FindCheckRunsForRef(ctx context.Context, repo, ref string) ([]GitHubCheckRun, error)
	// CreateCheckRun creates a check run on repo for the input's HeadSha.
	CreateCheckRun(ctx context.Context, repo string, input CreateCheckRunInput) (GitHubCheckRun, error)
	// UpdateCheckRun updates an existing check run (never name/head_sha).
	UpdateCheckRun(ctx context.Context, repo string, id int, input UpdateCheckRunInput) (GitHubCheckRun, error)
}

// prCredentialsKindNone is the credential-union kind that carries no token
// (INV-8: writes fail closed, reads stay allowed).
const prCredentialsKindNone = "none"

// checkRunEpochClock is the TS default clock — new Date(0).toISOString().
func checkRunEpochClock() string { return time.Unix(0, 0).UTC().Format("2006-01-02T15:04:05.000Z") }

// checkRunClient is the constructed client over the injected transport.
type checkRunClient struct {
	credentials PrCredentials
	transport   GitHubRestTransport
	baseURL     string
	clock       func() string
}

// CreateCheckRunClient creates the check-run REST client. No retries inside —
// the service layer owns the idempotent upsert (`find → create-or-update`) and
// governors own backoff.
func CreateCheckRunClient(deps CheckRunClientDeps) CheckRunClient {
	transport := deps.Transport
	if transport == nil {
		transport = CreateFetchGitHubRestTransport(&FetchTransportOptions{BaseUrl: deps.BaseURL})
	}
	baseURL := deps.BaseURL
	if baseURL == "" {
		baseURL = GITHUB_API_BASE_URL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	clock := deps.Clock
	if clock == nil {
		clock = checkRunEpochClock
	}
	return &checkRunClient{
		credentials: deps.Credentials,
		transport:   transport,
		baseURL:     baseURL,
		clock:       clock,
	}
}

// repoPath validates and encodes the owner/name argument (fail closed on a
// malformed repo).
func checkRunRepoPath(repo string) (string, error) {
	if len(repo) == 0 || !strings.Contains(repo, "/") {
		return "", NewGitHubRestError("check-runs: repo must be a nonempty owner/name string",
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
func (c *checkRunClient) requireCredentials(operation string) error {
	if c.credentials.Kind == prCredentialsKindNone {
		return NewGitHubAuthError("check-runs: "+operation+" requires credentials",
			GitHubRestErrorInit{Detail: "no credentials configured (kind: none) — writes fail closed (INV-8)"})
	}
	return nil
}

// redact routes text through the shared token redactor (the token NEVER
// survives into an error detail).
func (c *checkRunClient) redact(text string) string {
	return RedactGitHubToken(text, c.credentials)
}

// send performs one REST call through the injected seam and maps every
// non-2xx into the typed error family (details always redacted).
func (c *checkRunClient) send(ctx context.Context, method, path string, bodyJSON *string) (GitHubRestResponse, error) {
	headers := map[string]string{
		"Accept":               GITHUB_ACCEPT,
		"X-GitHub-Api-Version": GITHUB_API_VERSION,
		"User-Agent":           DOCSYNC_USER_AGENT,
	}
	if c.credentials.Kind != prCredentialsKindNone {
		headers["Authorization"] = "Bearer " + c.credentials.Token
	}
	if bodyJSON != nil {
		headers["Content-Type"] = "application/json"
	}
	request := GitHubRestRequest{
		Method:  method,
		URL:     c.baseURL + path,
		Headers: headers,
		Body:    bodyJSON,
	}
	response, err := c.transport(request)
	if err != nil {
		cause := err.Error()
		if cause == "" {
			cause = "unknown transport failure"
		}
		return GitHubRestResponse{}, NewGitHubTransportError(
			"check-runs: transport failure for "+method+" "+path,
			GitHubRestErrorInit{Detail: c.redact(cause)})
	}
	if response.Status >= 200 && response.Status < 300 {
		return response, nil
	}
	return GitHubRestResponse{}, c.checkRunErrorFor(method, path, response)
}

// checkRunErrorFor is the typed error mapping for every non-2xx (the detail is
// always redacted).
func (c *checkRunClient) checkRunErrorFor(method, path string, response GitHubRestResponse) error {
	status := response.Status
	message := fmt.Sprintf("check-runs: %s %s failed with status %d", method, path, status)
	var githubMessage *string
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response.Body), &parsed); err == nil {
		if raw, ok := parsed["message"]; ok {
			githubMessage = rawTextPointer(raw)
		}
	}
	restError := func(detail string) error {
		s := status
		return NewGitHubRestError(message, GitHubRestErrorInit{Detail: detail, Status: &s})
	}
	if status == 422 {
		// TS `githubMessage ?? response.body` — an empty string message does
		// NOT fall back, so the pointer form is kept verbatim.
		detail := response.Body
		if githubMessage != nil {
			detail = *githubMessage
		}
		s := status
		return NewGitHubValidationError(message, GitHubRestErrorInit{Detail: c.redact(detail), Status: &s})
	}
	contextText := "response body: " + checkRunBodyExcerpt(response.Body)
	if githubMessage != nil {
		contextText = "GitHub said: " + *githubMessage
	}
	detail := c.redact(contextText)
	switch {
	case status == 401 || status == 403:
		return NewGitHubAuthError(message, GitHubRestErrorInit{Detail: detail, Status: intPointer(status)})
	case status == 404:
		return NewGitHubNotFoundError(message, GitHubRestErrorInit{Detail: detail, Status: intPointer(status)})
	case status == 429:
		init := GitHubRateLimitErrorInit{Detail: detail, Status: intPointer(status)}
		if retryAfter, ok := finiteHeader(response.Headers, "retry-after"); ok {
			init.RetryAfterSeconds = floatPointer(retryAfter)
		}
		if reset, ok := finiteHeader(response.Headers, "x-ratelimit-reset"); ok {
			init.RatelimitResetEpochSeconds = floatPointer(reset)
		}
		return NewGitHubRateLimitError(message, init)
	case status >= 500:
		return NewGitHubTransportError(message, GitHubRestErrorInit{Detail: detail, Status: intPointer(status)})
	default:
		return restError(detail)
	}
}

// parseCheckRun projects a response body into a GitHubCheckRun (fail closed
// when the body is not a check-run object carrying a numeric id).
func (c *checkRunClient) parseCheckRun(response GitHubRestResponse) (GitHubCheckRun, error) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response.Body), &parsed); err == nil {
		if raw, ok := parsed["id"]; ok {
			var id float64
			if err := json.Unmarshal(raw, &id); err == nil {
				run := GitHubCheckRun{ID: int(id)}
				if value, ok := parsed["name"]; ok {
					run.Name = rawTextPointer(value)
				}
				if value, ok := parsed["status"]; ok {
					run.Status = rawTextPointer(value)
				}
				if value, ok := parsed["conclusion"]; ok {
					run.Conclusion = rawTextPointer(value)
				}
				if value, ok := parsed["head_sha"]; ok {
					run.HeadSha = rawTextPointer(value)
				}
				if value, ok := parsed["html_url"]; ok {
					run.HTMLURL = rawTextPointer(value)
				}
				return run, nil
			}
		}
	}
	return GitHubCheckRun{}, NewGitHubRestError("check-runs: response body is not a check-run object",
		GitHubRestErrorInit{Detail: c.redact(checkRunBodyExcerpt(response.Body)), Status: intPointer(response.Status)})
}

// FindCheckRunsForRef lists the docsync/check runs attached to ref.
func (c *checkRunClient) FindCheckRunsForRef(ctx context.Context, repo, ref string) ([]GitHubCheckRun, error) {
	path, err := checkRunRepoPath(repo)
	if err != nil {
		return nil, err
	}
	path = "/repos/" + path + "/commits/" + url.PathEscape(ref) +
		"/check-runs?check_name=" + url.PathEscape(DOCSYNCCheckName)
	response, err := c.send(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response.Body), &parsed); err == nil {
		if raw, ok := parsed["check_runs"]; ok {
			var runs []GitHubCheckRun
			if err := json.Unmarshal(raw, &runs); err == nil {
				return runs, nil
			}
		}
	}
	return nil, NewGitHubRestError("check-runs: unexpected find response body",
		GitHubRestErrorInit{Detail: c.redact(checkRunBodyExcerpt(response.Body)), Status: intPointer(response.Status)})
}

// CreateCheckRun creates a check run on repo for the input's head sha.
func (c *checkRunClient) CreateCheckRun(ctx context.Context, repo string, input CreateCheckRunInput) (GitHubCheckRun, error) {
	if err := c.requireCredentials("createCheckRun"); err != nil {
		return GitHubCheckRun{}, err
	}
	path, err := checkRunRepoPath(repo)
	if err != nil {
		return GitHubCheckRun{}, err
	}
	body := map[string]any{
		"name":     DOCSYNCCheckName,
		"head_sha": input.HeadSha,
		"status":   input.Status,
	}
	if input.Conclusion != nil {
		body["conclusion"] = *input.Conclusion
	}
	if input.Output != nil {
		body["output"] = map[string]any{
			"title":   input.Output.Title,
			"summary": input.Output.Summary,
			"text":    input.Output.Text,
		}
	}
	if input.EscalationEnabled {
		body["actions"] = []CheckRunAction{DOCSYNCGeneratePatchAction}
	}
	startedAt := input.StartedAt
	if startedAt == nil {
		startedAt = textPointer(c.clock())
	}
	body["started_at"] = *startedAt
	completedAt := input.CompletedAt
	if completedAt == nil && input.Status == CheckRunStatusCompleted {
		completedAt = textPointer(c.clock())
	}
	if completedAt != nil {
		body["completed_at"] = *completedAt
	}
	bodyJSON, err := marshalCheckRunBody(body)
	if err != nil {
		return GitHubCheckRun{}, err
	}
	response, err := c.send(ctx, "POST", "/repos/"+path+"/check-runs", bodyJSON)
	if err != nil {
		return GitHubCheckRun{}, err
	}
	return c.parseCheckRun(response)
}

// UpdateCheckRun updates an existing check run — the body never carries
// `name`/`head_sha` (GitHub rejects them on PATCH).
func (c *checkRunClient) UpdateCheckRun(ctx context.Context, repo string, id int, input UpdateCheckRunInput) (GitHubCheckRun, error) {
	if err := c.requireCredentials("updateCheckRun"); err != nil {
		return GitHubCheckRun{}, err
	}
	if id <= 0 {
		return GitHubCheckRun{}, NewGitHubRestError("check-runs: updateCheckRun requires a positive integer id",
			GitHubRestErrorInit{Detail: "malformed check-run id argument"})
	}
	path, err := checkRunRepoPath(repo)
	if err != nil {
		return GitHubCheckRun{}, err
	}
	body := map[string]any{}
	if input.Status != nil {
		body["status"] = *input.Status
	}
	if input.Conclusion != nil {
		body["conclusion"] = *input.Conclusion
	}
	if input.Output != nil {
		body["output"] = map[string]any{
			"title":   input.Output.Title,
			"summary": input.Output.Summary,
			"text":    input.Output.Text,
		}
	}
	if input.EscalationEnabled {
		body["actions"] = []CheckRunAction{DOCSYNCGeneratePatchAction}
	}
	if input.CompletedAt != nil {
		body["completed_at"] = *input.CompletedAt
	}
	// NOTE: no `name` / `head_sha` — GitHub rejects them on PATCH.
	bodyJSON, err := marshalCheckRunBody(body)
	if err != nil {
		return GitHubCheckRun{}, err
	}
	response, err := c.send(ctx, "PATCH", "/repos/"+path+"/check-runs/"+strconv.Itoa(id), bodyJSON)
	if err != nil {
		return GitHubCheckRun{}, err
	}
	return c.parseCheckRun(response)
}

/* ----------------------------------------------------------------- utilities */

// marshalCheckRunBody renders a request body (the TS JSON.stringify step).
func marshalCheckRunBody(body map[string]any) (*string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		// Fail closed: a body we cannot render is never sent.
		return nil, NewGitHubTransportError("check-runs: request body could not be serialized",
			GitHubRestErrorInit{Detail: err.Error()})
	}
	text := string(raw)
	return &text, nil
}

// firstRunes500 bounds an echoed response body at 500 characters (the TS
// slice(0, 500) on every error detail).
func checkRunBodyExcerpt(s string) string {
	runes := []rune(s)
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return s
}

// finiteHeader parses a response header into a finite number (the TS
// `Number.isFinite(Number(value))` gate); missing/unparseable ⇒ absent.
func finiteHeader(headers map[string]string, name string) (float64, bool) {
	value, ok := headers[name]
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// rawTextPointer decodes an optional JSON string field; absent, a JSON null
// or a non-string value map to nil (the TS `?: string | null` projection).
func rawTextPointer(raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil
	}
	return &text
}

func textPointer(s string) *string { return &s }

func intPointer(i int) *int { return &i }

func floatPointer(f float64) *float64 { return &f }
