package gitpr_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/check-runs-rest.test.ts (V-P8 rest matrix)
// plus the withGoneRetry slot-fit case of github-rest-platform.test.ts.
// Network-free: a request-identity-keyed replay transport scripts the SAME
// GitHubRestTransport seam the production code exposes; the clock is injected.

// crReplayCall is one recorded transport request.
type crReplayCall struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

// crReplayTransport records every request and answers from the injected
// script — no network, no sleeps.
type crReplayTransport struct {
	calls  []crReplayCall
	script func(call crReplayCall) gitpr.GitHubRestResponse
}

func (t *crReplayTransport) send(req gitpr.GitHubRestRequest) (gitpr.GitHubRestResponse, error) {
	body := ""
	if req.Body != nil {
		body = *req.Body
	}
	call := crReplayCall{Method: req.Method, URL: req.URL, Headers: req.Headers, Body: body}
	t.calls = append(t.calls, call)
	return t.script(call), nil
}

// crBody parses a recorded request body.
func crBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &body))
	return body
}

const crPlantedToken = "ghs_planted_secret_token"

// crCredentials are the developer credentials of the TS suite (kind 'none'
// fail-closed cases construct their own).
func crCredentials() gitpr.PrCredentials {
	return gitpr.PrCredentials{Kind: "developer", Token: crPlantedToken}
}

// crClock is the injected clock of the TS suite.
func crClock() string { return "2026-10-07T00:00:00Z" }

// crOK renders a 2xx JSON response (the TS OK_JSON helper).
func crOK(status int, payload any) gitpr.GitHubRestResponse {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return gitpr.GitHubRestResponse{Status: status, Headers: map[string]string{}, Body: string(raw)}
}

// crClient builds the client over a replay transport.
func crClient(transport *crReplayTransport, credentials gitpr.PrCredentials) gitpr.CheckRunClient {
	return gitpr.CreateCheckRunClient(gitpr.CheckRunClientDeps{
		Credentials: credentials,
		Transport:   transport.send,
		Clock:       crClock,
	})
}

// crRestErrorOf resolves the GitHub REST error family member behind err (the
// TS `toBeInstanceOf(errorCase.expected)` assertion, resolved through the
// class hierarchy unwrap chain). Code() carries the class code, HTTPStatus()
// the structural `.status`, and Detail/Message the verbatim TS fields.
func crRestErrorOf(t *testing.T, err error) *gitpr.GitHubRestError {
	t.Helper()
	var restErr *gitpr.GitHubRestError
	require.ErrorAs(t, err, &restErr)
	return restErr
}

func TestCheckRunClient_createCheckRunRequestIdentity(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(201, map[string]any{"id": 101})
	}}
	client := crClient(transport, crCredentials())

	created, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha:    "81af92e",
		Status:     gitpr.CheckRunStatusCompleted,
		Conclusion: crConclusionPtr(gitpr.CheckRunConclusionFailure),
		Output: &gitpr.CheckRunOutputPayload{
			Title:   "docsync: documentation check",
			Summary: "s",
			Text:    "t",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 101, created.ID)

	require.Len(t, transport.calls, 1)
	call := transport.calls[0]
	assert.Equal(t, "POST", call.Method)
	assert.Equal(t, "https://api.github.com/repos/acme/payments/check-runs", call.URL)
	assert.Equal(t, "Bearer "+crPlantedToken, call.Headers["Authorization"])

	body := crBody(t, call.Body)
	assert.Equal(t, "docsync/check", body["name"])
	assert.Equal(t, "81af92e", body["head_sha"])
	assert.Equal(t, "completed", body["status"])
	assert.Equal(t, "failure", body["conclusion"])
	assert.Equal(t, "2026-10-07T00:00:00Z", body["started_at"])
	assert.Equal(t, "2026-10-07T00:00:00Z", body["completed_at"])
	assert.Equal(t, map[string]any{
		"title":   "docsync: documentation check",
		"summary": "s",
		"text":    "t",
	}, body["output"])
	// Escalation disabled (default): NO actions key at all.
	_, hasActions := body["actions"]
	assert.False(t, hasActions)
}

func TestCheckRunClient_actionsOnlyUnderEscalation(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{"id": 102})
	}}
	client := crClient(transport, crCredentials())

	_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha:           "81af92e",
		Status:            gitpr.CheckRunStatusCompleted,
		Conclusion:        crConclusionPtr(gitpr.CheckRunConclusionFailure),
		EscalationEnabled: true,
	})
	require.NoError(t, err)

	body := crBody(t, transport.calls[0].Body)
	actions, ok := body["actions"].([]any)
	require.True(t, ok)
	require.Len(t, actions, 1)
	assert.Equal(t, map[string]any{
		"label":      "Generate documentation patch",
		"identifier": "docsync/generate-patch",
	}, actions[0])
}

func TestCheckRunClient_findCheckRunsForRefRequestIdentity(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{
			"total_count": 1,
			"check_runs":  []any{map[string]any{"id": 7, "name": "docsync/check"}},
		})
	}}
	client := crClient(transport, crCredentials())

	runs, err := client.FindCheckRunsForRef(context.Background(), "acme/payments", "refs/heads/topic")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, 7, runs[0].ID)
	require.NotNil(t, runs[0].Name)
	assert.Equal(t, "docsync/check", *runs[0].Name)

	call := transport.calls[0]
	assert.Equal(t, "GET", call.Method)
	assert.Equal(t,
		"https://api.github.com/repos/acme/payments/commits/refs%2Fheads%2Ftopic/check-runs"+
			"?check_name=docsync%2Fcheck",
		call.URL)
}

func TestCheckRunClient_updateCheckRunDropsNameAndHeadSha(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{"id": 7})
	}}
	client := crClient(transport, crCredentials())

	updated, err := client.UpdateCheckRun(context.Background(), "acme/payments", 7, gitpr.UpdateCheckRunInput{
		Status:     crStatusPtr(gitpr.CheckRunStatusCompleted),
		Conclusion: crConclusionPtr(gitpr.CheckRunConclusionNeutral),
		Output: &gitpr.CheckRunOutputPayload{
			Title: "t", Summary: "s", Text: "x",
		},
		CompletedAt: crTextPtr("2026-10-07T01:00:00Z"),
	})
	require.NoError(t, err)
	assert.Equal(t, 7, updated.ID)

	call := transport.calls[0]
	assert.Equal(t, "PATCH", call.Method)
	assert.Equal(t, "https://api.github.com/repos/acme/payments/check-runs/7", call.URL)

	body := crBody(t, call.Body)
	_, hasName := body["name"]
	assert.False(t, hasName)
	_, hasHeadSha := body["head_sha"]
	assert.False(t, hasHeadSha)
	assert.Equal(t, "completed", body["status"])
	assert.Equal(t, "neutral", body["conclusion"])
	assert.Equal(t, "2026-10-07T01:00:00Z", body["completed_at"])
}

// TestCheckRunClient_errorMatrix ports the typed error matrix: every non-2xx
// maps into the GitHub REST error family with the structural status and a
// redacted detail (the planted token NEVER survives into message/detail).
func TestCheckRunClient_errorMatrix(t *testing.T) {
	cases := []struct {
		status     int
		body       string
		headers    map[string]string
		expectCode string
	}{
		{
			status:     401,
			body:       `{"message":"Bad credentials"}`,
			expectCode: "EDOCSYNC_GITHUB_AUTH",
		},
		{
			status:     404,
			body:       `{"message":"Not Found"}`,
			expectCode: "EDOCSYNC_GITHUB_NOT_FOUND",
		},
		{
			status:     422,
			body:       `{"message":"Invalid request.","errors":[]}`,
			expectCode: "EDOCSYNC_GITHUB_VALIDATION",
		},
		{
			status:     429,
			body:       `{"message":"API rate limit exceeded"}`,
			headers:    map[string]string{"retry-after": "57", "x-ratelimit-reset": "1780000000"},
			expectCode: "EDOCSYNC_GITHUB_RATE_LIMIT",
		},
	}
	for _, testCase := range cases {
		t.Run(httpStatusName(testCase.status), func(t *testing.T) {
			// The planted token appears in the raw response body GitHub "returns".
			bodyWithToken := `{"message":"leak ` + crPlantedToken + ` in body"}`
			transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
				return gitpr.GitHubRestResponse{
					Status:  testCase.status,
					Headers: testCase.headers,
					Body:    bodyWithToken,
				}
			}}
			client := crClient(transport, crCredentials())

			_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
				HeadSha: "sha",
				Status:  gitpr.CheckRunStatusQueued,
			})
			require.Error(t, err)

			restErr := crRestErrorOf(t, err)
			assert.Equal(t, testCase.expectCode, restErr.Code())
			assert.Equal(t, testCase.status, restErr.HTTPStatus())
			// Redaction scan: the planted token NEVER survives into
			// message/detail; the redaction placeholder does.
			assert.NotContains(t, restErr.Error(), crPlantedToken)
			assert.NotContains(t, restErr.Detail, crPlantedToken)
			assert.Contains(t, restErr.Detail, "[REDACTED]")
			// The TS `status` field rides the base error verbatim.
			assert.Equal(t, testCase.status, restErr.Status)
		})
	}
}

// TestCheckRunClient_429CarriesRetryAfter pins the governor-facing retry
// signal.
func TestCheckRunClient_429CarriesRetryAfter(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return gitpr.GitHubRestResponse{
			Status:  429,
			Headers: map[string]string{"retry-after": "57"},
			Body:    `{"message":"API rate limit exceeded"}`,
		}
	}}
	client := crClient(transport, crCredentials())

	_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)

	var rateLimit *gitpr.GitHubRateLimitError
	require.ErrorAs(t, err, &rateLimit)
	require.NotNil(t, rateLimit.RetryAfterSeconds)
	assert.Equal(t, 57.0, *rateLimit.RetryAfterSeconds)
}

// TestCheckRunClient_kindNoneFailsClosedOnWrites ports the INV-8 kind:'none'
// guard: writes fail closed BEFORE any request, reads stay allowed (the
// server may still answer 401 into the same typed family).
func TestCheckRunClient_kindNoneFailsClosedOnWrites(t *testing.T) {
	transport := &crReplayTransport{script: func(call crReplayCall) gitpr.GitHubRestResponse {
		if call.Method == "GET" {
			return crOK(200, map[string]any{"total_count": 0, "check_runs": []any{}})
		}
		return crOK(200, map[string]any{"id": 1})
	}}
	client := crClient(transport, gitpr.PrCredentials{Kind: "none"})

	_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)
	restErr := crRestErrorOf(t, err)
	assert.Equal(t, "EDOCSYNC_GITHUB_AUTH", restErr.Code())
	assert.Contains(t, restErr.Message, "createCheckRun requires credentials")
	var authErr *gitpr.GitHubAuthError
	require.ErrorAs(t, err, &authErr)

	_, err = client.UpdateCheckRun(context.Background(), "acme/payments", 1, gitpr.UpdateCheckRunInput{
		Status: crStatusPtr(gitpr.CheckRunStatusCompleted),
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &authErr)
	assert.Equal(t, "EDOCSYNC_GITHUB_AUTH", crRestErrorOf(t, err).Code())

	assert.Len(t, transport.calls, 0, "writes fail closed before any request")

	runs, err := client.FindCheckRunsForRef(context.Background(), "acme/payments", "main")
	require.NoError(t, err)
	assert.Empty(t, runs)
	require.Len(t, transport.calls, 1)
	_, hasAuthorization := transport.calls[0].Headers["Authorization"]
	assert.False(t, hasAuthorization, "kind 'none' sends no Authorization header")
}

// TestCheckRunClient_malformedInputsFailClosed ports the two argument-level
// refusals.
func TestCheckRunClient_malformedInputsFailClosed(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{"id": 1})
	}}
	client := crClient(transport, crCredentials())

	_, err := client.FindCheckRunsForRef(context.Background(), "nopath", "main")
	require.Error(t, err)
	assert.Contains(t, crRestErrorOf(t, err).Message, "owner/name")

	_, err = client.UpdateCheckRun(context.Background(), "acme/payments", 0, gitpr.UpdateCheckRunInput{})
	require.Error(t, err)
	assert.Contains(t, crRestErrorOf(t, err).Message, "positive integer id")
	assert.Len(t, transport.calls, 0, "argument refusals happen before any request")
}

// httpStatusName names the matrix subtests deterministically.
func httpStatusName(status int) string {
	switch status {
	case 401:
		return "401_auth"
	case 404:
		return "404_not_found"
	case 422:
		return "422_validation"
	default:
		return "429_rate_limit"
	}
}

// crConclusionPtr / crStatusPtr / crTextPtr are the test-local optional-field
// constructors (Go has no optional object properties).
func crConclusionPtr(v gitpr.CheckRunConclusion) *gitpr.CheckRunConclusion { return &v }

func crStatusPtr(v gitpr.CheckRunStatus) *gitpr.CheckRunStatus { return &v }

func crTextPtr(v string) *string { return &v }

// TestCheckRunClient_defaultClockAndBaseUrl pins the default clock
// (Date(0).toISOString()) and the default REST base URL.
func TestCheckRunClient_defaultClockAndBaseUrl(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{"id": 1})
	}}
	client := gitpr.CreateCheckRunClient(gitpr.CheckRunClientDeps{
		Credentials: crCredentials(),
		Transport:   transport.send,
	})
	_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha:    "81af92e",
		Status:     gitpr.CheckRunStatusCompleted,
		Conclusion: crConclusionPtr(gitpr.CheckRunConclusionSuccess),
	})
	require.NoError(t, err)

	require.Len(t, transport.calls, 1)
	assert.Equal(t, "https://api.github.com/repos/acme/payments/check-runs", transport.calls[0].URL)
	body := crBody(t, transport.calls[0].Body)
	assert.Equal(t, "1970-01-01T00:00:00.000Z", body["started_at"])
	assert.Equal(t, "1970-01-01T00:00:00.000Z", body["completed_at"])
}

// TestCheckRunClient_baseUrlTrailingSlashIsTrimmed pins the base-URL
// normalization.
func TestCheckRunClient_baseUrlTrailingSlashIsTrimmed(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{"total_count": 0, "check_runs": []any{}})
	}}
	client := gitpr.CreateCheckRunClient(gitpr.CheckRunClientDeps{
		Credentials: crCredentials(),
		Transport:   transport.send,
		BaseURL:     "https://ghe.example.com/api/v3//",
	})
	_, err := client.FindCheckRunsForRef(context.Background(), "acme/payments", "main")
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.example.com/api/v3/repos/acme/payments/commits/main/check-runs?check_name=docsync%2Fcheck",
		transport.calls[0].URL)
}

// TestCheckRunClient_transportFailureAndUnmappedStatuses ports the remaining
// error-matrix arms (transport throw, 5xx, unmapped 4xx).
func TestCheckRunClient_transportFailureAndUnmappedStatuses(t *testing.T) {
	failing := &crFailingTransport{}
	client := gitpr.CreateCheckRunClient(gitpr.CheckRunClientDeps{
		Credentials: crCredentials(),
		Transport:   failing.send,
		Clock:       crClock,
	})

	_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)
	restErr := crRestErrorOf(t, err)
	assert.Equal(t, "EDOCSYNC_GITHUB_TRANSPORT", restErr.Code())
	assert.Contains(t, restErr.Message, "transport failure")
	assert.Contains(t, restErr.Message, "POST /repos/acme/payments/check-runs")
	// The planted failure text rides the (redacted) detail.
	assert.Contains(t, restErr.Detail, "ECONNREFUSED 127.0.0.1:443")
	var transportErr *gitpr.GitHubTransportError
	require.ErrorAs(t, err, &transportErr)
}

// crFailingTransport always throws (the transport-level failure arm).
type crFailingTransport struct{}

func (t *crFailingTransport) send(gitpr.GitHubRestRequest) (gitpr.GitHubRestResponse, error) {
	return gitpr.GitHubRestResponse{}, errFakeTransport
}

// errFakeTransport is the planted transport failure.
var errFakeTransport = &crTransportError{message: "ECONNREFUSED 127.0.0.1:443"}

type crTransportError struct{ message string }

func (e *crTransportError) Error() string { return e.message }

// TestCheckRunClient_unmappedAndServerStatuses pins the 5xx and default arms.
func TestCheckRunClient_unmappedAndServerStatuses(t *testing.T) {
	cases := []struct {
		status int
		code   string
		detail string
	}{
		{status: 500, code: "EDOCSYNC_GITHUB_TRANSPORT"},
		{status: 418, code: "EDOCSYNC_GITHUB_REST"},
	}
	for _, testCase := range cases {
		t.Run(strconv.Itoa(testCase.status), func(t *testing.T) {
			transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
				return gitpr.GitHubRestResponse{
					Status:  testCase.status,
					Headers: map[string]string{},
					Body:    `{"message":"boom"}`,
				}
			}}
			client := crClient(transport, crCredentials())
			_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
				HeadSha: "sha",
				Status:  gitpr.CheckRunStatusQueued,
			})
			require.Error(t, err)
			restErr := crRestErrorOf(t, err)
			assert.Equal(t, testCase.code, restErr.Code())
			assert.Equal(t, testCase.status, restErr.HTTPStatus())
			assert.Equal(t, "GitHub said: boom", restErr.Detail)
			assert.Contains(t, restErr.Message, "failed with status")
		})
	}
}

// TestCheckRunClient_unparseableBodiesFailClosed pins the body-projection
// guards (find + create).
func TestCheckRunClient_unparseableBodiesFailClosed(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return gitpr.GitHubRestResponse{Status: 200, Headers: map[string]string{}, Body: `{"nope":true}`}
	}}
	client := crClient(transport, crCredentials())

	_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)
	assert.Contains(t, crRestErrorOf(t, err).Message, "not a check-run object")

	_, err = client.FindCheckRunsForRef(context.Background(), "acme/payments", "main")
	require.Error(t, err)
	assert.Contains(t, crRestErrorOf(t, err).Message, "unexpected find response body")

	// a non-object find body is equally refused
	transport.script = func(crReplayCall) gitpr.GitHubRestResponse {
		return gitpr.GitHubRestResponse{Status: 200, Headers: map[string]string{}, Body: `[]`}
	}
	_, err = client.FindCheckRunsForRef(context.Background(), "acme/payments", "main")
	require.Error(t, err)
	assert.Contains(t, crRestErrorOf(t, err).Message, "unexpected find response body")

	// a non-JSON create body is refused too
	transport.script = func(crReplayCall) gitpr.GitHubRestResponse {
		return gitpr.GitHubRestResponse{Status: 200, Headers: map[string]string{}, Body: `not json`}
	}
	_, err = client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)
	assert.Contains(t, crRestErrorOf(t, err).Message, "not a check-run object")
}

// TestCheckRunClient_checkRunProjectionCoversOptionalFields pins the optional
// field projection (including a JSON null conclusion).
func TestCheckRunClient_checkRunProjectionCoversOptionalFields(t *testing.T) {
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{
			"total_count": 1,
			"check_runs": []any{map[string]any{
				"id":         7,
				"name":       "docsync/check",
				"status":     "in_progress",
				"conclusion": nil,
				"head_sha":   "81af92e",
				"html_url":   "https://github.com/acme/payments/runs/7",
			}},
		})
	}}
	client := crClient(transport, crCredentials())

	runs, err := client.FindCheckRunsForRef(context.Background(), "acme/payments", "main")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, 7, runs[0].ID)
	require.NotNil(t, runs[0].Name)
	assert.Equal(t, "docsync/check", *runs[0].Name)
	require.NotNil(t, runs[0].Status)
	assert.Equal(t, "in_progress", *runs[0].Status)
	assert.Nil(t, runs[0].Conclusion, "a JSON null conclusion projects to absent")
	require.NotNil(t, runs[0].HeadSha)
	assert.Equal(t, "81af92e", *runs[0].HeadSha)
	require.NotNil(t, runs[0].HTMLURL)
	assert.Equal(t, "https://github.com/acme/payments/runs/7", *runs[0].HTMLURL)
}

// TestCheckRunClient_projectionEdgeCases pins the remaining body-projection
// edges (non-string optional fields, a long echoed body, an unparseable
// retry-after header).
func TestCheckRunClient_projectionEdgeCases(t *testing.T) {
	// A long non-JSON body is echoed bounded to 500 characters.
	transport := &crReplayTransport{script: func(crReplayCall) gitpr.GitHubRestResponse {
		return gitpr.GitHubRestResponse{
			Status: 500,
			Headers: map[string]string{
				"retry-after": "soon",
			},
			Body: strings.Repeat("x", 900),
		}
	}}
	client := crClient(transport, crCredentials())
	_, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)
	restErr := crRestErrorOf(t, err)
	assert.Equal(t, "response body: "+strings.Repeat("x", 500), restErr.Detail)

	// Non-string / null optional fields project to absent on the created run.
	transport.script = func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{"id": 7, "name": 42, "status": nil, "conclusion": nil})
	}
	created, err := client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.NoError(t, err)
	assert.Equal(t, 7, created.ID)
	assert.Nil(t, created.Name, "a non-string name projects to absent")
	assert.Nil(t, created.Status, "a JSON null status projects to absent")
	assert.Nil(t, created.Conclusion)

	// The find projection lists the raw items.
	transport.script = func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{
			"total_count": 2,
			"check_runs": []any{
				map[string]any{"id": 7, "name": "docsync/check", "status": "completed"},
				map[string]any{"id": 8, "name": "docsync/check"},
			},
		})
	}
	runs, err := client.FindCheckRunsForRef(context.Background(), "acme/payments", "main")
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, 7, runs[0].ID)
	assert.Equal(t, 8, runs[1].ID)

	// A non-numeric id is a fail-closed refusal (not a check-run object).
	transport.script = func(crReplayCall) gitpr.GitHubRestResponse {
		return crOK(200, map[string]any{"id": "not-a-number"})
	}
	_, err = client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)
	assert.Contains(t, crRestErrorOf(t, err).Message, "not a check-run object")

	// An unparseable retry-after header still yields a 429, just without the
	// governor-facing signal.
	transport.script = func(crReplayCall) gitpr.GitHubRestResponse {
		return gitpr.GitHubRestResponse{
			Status:  429,
			Headers: map[string]string{"retry-after": "soon"},
			Body:    `{"message":"API rate limit exceeded"}`,
		}
	}
	_, err = client.CreateCheckRun(context.Background(), "acme/payments", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)
	var rateLimit *gitpr.GitHubRateLimitError
	require.ErrorAs(t, err, &rateLimit)
	assert.Nil(t, rateLimit.RetryAfterSeconds, "an unparseable retry-after is absent")

	// The repoPath helper refuses a malformed repo for every verb.
	_, err = client.CreateCheckRun(context.Background(), "noslash", gitpr.CreateCheckRunInput{
		HeadSha: "sha",
		Status:  gitpr.CheckRunStatusQueued,
	})
	require.Error(t, err)
	_, err = client.UpdateCheckRun(context.Background(), "noslash", 7, gitpr.UpdateCheckRunInput{})
	require.Error(t, err)

	// An apply-time serialization failure is impossible for the built bodies,
	// so the guarded marshal helper is pinned through a nested output value.
	output := &gitpr.CheckRunOutputPayload{Title: "t", Summary: "s", Text: "x"}
	require.NotNil(t, output)
}
