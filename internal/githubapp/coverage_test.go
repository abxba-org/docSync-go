package githubapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/githubapp"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* ======================================================= routes (HTTP admission seam) */

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

func TestCOV_GITHUBAPP_Routes(t *testing.T) {
	t.Run("ServeWebhook mirrors handled result and status", func(t *testing.T) {
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret:      "s",
			AppIdentity: "bot",
			Handlers: githubapp.WebhookHandlers{
				PullRequest: func(ctx githubapp.WebhookHandlerContext) error { return nil },
			},
		})
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 1},
			"pull_request": map[string]any{
				"number": 1, "head": map[string]any{"sha": "abc", "ref": "feat"},
			},
		})
		req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "d1")
		req.Header.Set("X-Hub-Signature-256", signBody(body, "s"))
		w := httptest.NewRecorder()
		githubapp.ServeWebhook(receiver)(w, req)
		assert.Equal(t, 200, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		var res githubapp.WebhookResult
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
		assert.Equal(t, githubapp.WebhookResultHandled, res.Kind)
		assert.Equal(t, "pull_request", res.Event)
	})

	t.Run("ServeWebhook invalid signature → 401", func(t *testing.T) {
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: "s", AppIdentity: "bot",
			Handlers: githubapp.WebhookHandlers{
				PullRequest: func(ctx githubapp.WebhookHandlerContext) error { return nil },
			},
		})
		body := `{"action":"opened"}`
		req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "d1")
		req.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
		w := httptest.NewRecorder()
		githubapp.ServeWebhook(receiver)(w, req)
		assert.Equal(t, 401, w.Code)
	})

	t.Run("ServeWebhook body read error → 400", func(t *testing.T) {
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: "s", AppIdentity: "bot",
			Handlers: githubapp.WebhookHandlers{
				PullRequest: func(ctx githubapp.WebhookHandlerContext) error { return nil },
			},
		})
		req := httptest.NewRequest("POST", "/webhook", errReader{})
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "d1")
		req.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
		w := httptest.NewRecorder()
		githubapp.ServeWebhook(receiver)(w, req)
		assert.Equal(t, 400, w.Code)
	})

	t.Run("ServeWebhook handler error → 500", func(t *testing.T) {
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: "s", AppIdentity: "bot",
			Handlers: githubapp.WebhookHandlers{
				PullRequest: func(ctx githubapp.WebhookHandlerContext) error {
					return errors.New("handler boom")
				},
			},
		})
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 1},
			"pull_request": map[string]any{
				"number": 1, "head": map[string]any{"sha": "abc", "ref": "feat"},
			},
		})
		req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "d1")
		req.Header.Set("X-Hub-Signature-256", signBody(body, "s"))
		w := httptest.NewRecorder()
		githubapp.ServeWebhook(receiver)(w, req)
		assert.Equal(t, 500, w.Code)
	})

	t.Run("ServeAppWebhook mirrors handled result", func(t *testing.T) {
		app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
			AppCheckRuntime: &stubCheckRuntime{}, AppIdentity: appWebhookIdentity,
			Secret: appWebhookSecret, Logger: loggedApp(t, &[]string{}),
		})
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": appTestRepoID},
			"installation": map[string]any{"id": 42},
			"pull_request": map[string]any{
				"number": 7, "head": map[string]any{"sha": "head456", "ref": "feature/docs"},
			},
		})
		req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "d1")
		req.Header.Set("X-Hub-Signature-256", signBody(body, appWebhookSecret))
		w := httptest.NewRecorder()
		githubapp.ServeAppWebhook(app)(w, req)
		assert.Equal(t, 200, w.Code)
	})

	t.Run("ServeWebhook unexpected event → 200 ACK, no handler", func(t *testing.T) {
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: "s", AppIdentity: "bot",
			Handlers: githubapp.WebhookHandlers{},
		})
		body := `{"action":"published"}`
		req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "release")
		req.Header.Set("X-GitHub-Delivery", "d1")
		req.Header.Set("X-Hub-Signature-256", signBody(body, "s"))
		w := httptest.NewRecorder()
		githubapp.ServeWebhook(receiver)(w, req)
		assert.Equal(t, 200, w.Code)
	})
}

/* ======================================================= app wiring (app.go) */

type wrapDuplicate struct {
	err error
}

func (e wrapDuplicate) Error() string { return e.err.Error() }
func (e wrapDuplicate) Unwrap() error { return e.err }

func TestCOV_GITHUBAPP_AppWiring(t *testing.T) {
	t.Run("nil logger defaults to no-op logger", func(t *testing.T) {
		app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
			AppCheckRuntime: &stubCheckRuntime{}, AppIdentity: appWebhookIdentity,
			Secret: appWebhookSecret,
		})
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": appTestRepoID},
			"installation": map[string]any{"id": 42},
			"pull_request": map[string]any{
				"number": 7, "head": map[string]any{"sha": "head456", "ref": "feature/docs"},
			},
		})
		result, err := app.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "delivery-1", Signature: signBody(body, appWebhookSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
	})

	t.Run("nil clock falls back to epochClock via escalation logs", func(t *testing.T) {
		calls := &[]githubapp.RunPatchPortInput{}
		runPatch := func(in githubapp.RunPatchPortInput) (githubapp.RunPatchPortResult, error) {
			*calls = append(*calls, in)
			return githubapp.RunPatchPortResult{Kind: githubapp.PatchShipped, Detail: "ok"}, nil
		}
		app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
			AppCheckRuntime: &stubCheckRuntime{}, AppIdentity: appWebhookIdentity, Secret: appWebhookSecret,
			Escalation: &githubapp.AppEscalation{RunPatch: runPatch},
		})
		body := mustJSON(t, map[string]any{
			"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
			"repository": map[string]any{"full_name": appTestRepoID}, "installation": map[string]any{"id": 42},
			"requested_action": map[string]any{"identifier": gitpr.DOCSYNCGeneratePatchAction.Identifier},
			"check_run": map[string]any{"id": 99, "pull_requests": []any{
				map[string]any{"number": 7, "head": map[string]any{"sha": strings.Repeat("b", 40)}},
			}},
		})
		result, err := app.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "delivery-e1", Signature: signBody(body, appWebhookSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.Len(t, *calls, 1)
	})

	t.Run("escalation plain error → fail-closed redacted log", func(t *testing.T) {
		calls := &[]githubapp.RunPatchPortInput{}
		runPatch := func(in githubapp.RunPatchPortInput) (githubapp.RunPatchPortResult, error) {
			*calls = append(*calls, in)
			return githubapp.RunPatchPortResult{}, errors.New("boom")
		}
		logs := &[]string{}
		app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
			AppCheckRuntime: &stubCheckRuntime{}, AppIdentity: appWebhookIdentity, Secret: appWebhookSecret,
			Logger:     loggedApp(t, logs),
			Escalation: &githubapp.AppEscalation{RunPatch: runPatch},
			Clock:      func() string { return appWebhookClockTime },
		})
		body := mustJSON(t, map[string]any{
			"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
			"repository": map[string]any{"full_name": appTestRepoID}, "installation": map[string]any{"id": 42},
			"requested_action": map[string]any{"identifier": gitpr.DOCSYNCGeneratePatchAction.Identifier},
			"check_run": map[string]any{"id": 99, "pull_requests": []any{
				map[string]any{"number": 7, "head": map[string]any{"sha": strings.Repeat("b", 40)}},
			}},
		})
		result, err := app.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "delivery-e2", Signature: signBody(body, appWebhookSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.Len(t, *calls, 1)
		assert.True(t, containsSubstr(*logs, "escalation failed (fail closed)"))
	})

	t.Run("escalation wrapped duplicate → errorsAsDuplicate unwrap path", func(t *testing.T) {
		calls := &[]githubapp.RunPatchPortInput{}
		dup := &githubapp.DuplicateEscalationError{
			IdempotencyKey: "escalation-delivery-e3",
		}
		runPatch := func(in githubapp.RunPatchPortInput) (githubapp.RunPatchPortResult, error) {
			*calls = append(*calls, in)
			return githubapp.RunPatchPortResult{}, wrapDuplicate{err: dup}
		}
		logs := &[]string{}
		app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
			AppCheckRuntime: &stubCheckRuntime{}, AppIdentity: appWebhookIdentity, Secret: appWebhookSecret,
			Logger:     loggedApp(t, logs),
			Escalation: &githubapp.AppEscalation{RunPatch: runPatch},
			Clock:      func() string { return appWebhookClockTime },
		})
		body := mustJSON(t, map[string]any{
			"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
			"repository": map[string]any{"full_name": appTestRepoID}, "installation": map[string]any{"id": 42},
			"requested_action": map[string]any{"identifier": gitpr.DOCSYNCGeneratePatchAction.Identifier},
			"check_run": map[string]any{"id": 99, "pull_requests": []any{
				map[string]any{"number": 7, "head": map[string]any{"sha": strings.Repeat("b", 40)}},
			}},
		})
		result, err := app.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "delivery-e3", Signature: signBody(body, appWebhookSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.Len(t, *calls, 1)
		assert.True(t, containsSubstr(*logs, "escalation duplicate rejected"))
	})
}

/* ======================================================= check-run service error/nil paths */

// errFindClient always fails the find lookup.
type errFindClient struct{}

func (errFindClient) FindCheckRunsForRef(context.Context, string, string) ([]gitpr.GitHubCheckRun, error) {
	return nil, errors.New("find down")
}
func (errFindClient) CreateCheckRun(context.Context, string, gitpr.CreateCheckRunInput) (gitpr.GitHubCheckRun, error) {
	return gitpr.GitHubCheckRun{}, nil
}
func (errFindClient) UpdateCheckRun(context.Context, string, int, gitpr.UpdateCheckRunInput) (gitpr.GitHubCheckRun, error) {
	return gitpr.GitHubCheckRun{}, nil
}

// errUpdateClient finds an existing run then fails the update.
type errUpdateClient struct {
	found bool
}

func (c errUpdateClient) FindCheckRunsForRef(context.Context, string, string) ([]gitpr.GitHubCheckRun, error) {
	if c.found {
		return []gitpr.GitHubCheckRun{ghRun(101, "in_progress", gitpr.DOCSYNCCheckName)}, nil
	}
	return nil, nil
}
func (errUpdateClient) CreateCheckRun(context.Context, string, gitpr.CreateCheckRunInput) (gitpr.GitHubCheckRun, error) {
	return gitpr.GitHubCheckRun{}, nil
}
func (errUpdateClient) UpdateCheckRun(context.Context, string, int, gitpr.UpdateCheckRunInput) (gitpr.GitHubCheckRun, error) {
	return gitpr.GitHubCheckRun{}, errors.New("update down")
}

// errCreateClient finds nothing then fails the create with a non-422 error.
type errCreateClient struct{}

func (errCreateClient) FindCheckRunsForRef(context.Context, string, string) ([]gitpr.GitHubCheckRun, error) {
	return nil, nil
}
func (errCreateClient) CreateCheckRun(context.Context, string, gitpr.CreateCheckRunInput) (gitpr.GitHubCheckRun, error) {
	return gitpr.GitHubCheckRun{}, errors.New("create down")
}
func (errCreateClient) UpdateCheckRun(context.Context, string, int, gitpr.UpdateCheckRunInput) (gitpr.GitHubCheckRun, error) {
	return gitpr.GitHubCheckRun{}, nil
}

func TestCOV_GITHUBAPP_ChecksErrorPaths(t *testing.T) {
	newSvc := func(c gitpr.CheckRunClient, logger func(string)) githubapp.UpsertService {
		return githubapp.CreateCheckRunService(githubapp.CheckRunServiceDeps{
			Client: c, Clock: func() string { return "2026-10-07T00:00:00Z" }, Logger: logger,
		})
	}

	t.Run("findExistingRun propagate find error → no create", func(t *testing.T) {
		_, err := newSvc(errFindClient{}, nil).UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Status: githubapp.UpsertInProgress, Partial: true,
		})
		assert.NoError(t, err)
	})

	t.Run("updateExisting propagate update error", func(t *testing.T) {
		_, err := newSvc(errUpdateClient{found: true}, nil).UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: githubapp.UpsertCompleted,
		})
		assert.Error(t, err)
	})

	t.Run("create non-422 error → returned directly (not duplicate)", func(t *testing.T) {
		_, err := newSvc(errCreateClient{}, nil).UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Status: githubapp.UpsertInProgress, Partial: true,
		})
		assert.Error(t, err)
	})

	t.Run("nil logger → logLine/logf guards (no panic)", func(t *testing.T) {
		client := &fakeCheckClient{findResults: [][]gitpr.GitHubCheckRun{{}}}
		_, err := newSvc(client, nil).UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Status: githubapp.UpsertInProgress, Partial: true,
		})
		assert.NoError(t, err)
	})

	t.Run("updateExisting with nil conclusion → conclusionText(nil) branch", func(t *testing.T) {
		client := &fakeCheckClient{
			findResults: [][]gitpr.GitHubCheckRun{{ghRun(101, "in_progress", gitpr.DOCSYNCCheckName)}},
		}
		_, err := newSvc(client, nil).UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Conclusion: nil, Status: githubapp.UpsertInProgress,
		})
		assert.NoError(t, err)
		require.Len(t, client.updatedCalls, 1)
		assert.Equal(t, 101, client.updatedCalls[0].id)
	})
}

/* ======================================================= pipeline handler nil-logger */

func TestCOV_GITHUBAPP_HandlerNilLogger(t *testing.T) {
	svc := &svcDouble{}
	runCheck := githubapp.RunCheckPort(func(in githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
		_ = in.EarlySignal.Post(partialPayload("stage-1"))
		return githubapp.RunCheckPortResult{Kind: githubapp.RunCheckPortResultSuccess, Report: handlerReport(nil)}, nil
	})
	handler := githubapp.CreatePullRequestCheckHandler(githubapp.PullRequestCheckHandlerDeps{
		Service: svc, RunCheck: runCheck, Logger: func(string) {},
	})
	_, err := handler.HandlePullRequest(githubapp.PullRequestCheckInput{
		Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
	})
	require.NoError(t, err)
	assert.Len(t, svc.upserts, 2)
}

/* ======================================================= webhook receiver body/parse/error edges */

func TestCOV_GITHUBAPP_ReceiverEdges(t *testing.T) {
	t.Run("malformed JSON payload → 200 rejected", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		body := `{not json`
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		assert.Len(t, *calls, 0)
		assert.True(t, containsSubstr(*logs, "malformed payload"))
	})

	t.Run("JSON null payload → rejected (not a JSON object)", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		result, err := receiver.HandleWebhook("null", githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody("null", recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("delivery with no installation → fail-closed reject", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository": map[string]any{"full_name": "acme/w"},
			"pull_request": map[string]any{
				"number": 1, "head": map[string]any{"sha": "abc", "ref": "feat"},
			},
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})

	t.Run("installation not an object → not present", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": "acme/w"},
			"installation": "not-an-object",
			"pull_request": map[string]any{
				"number": 1, "head": map[string]any{"sha": "abc", "ref": "feat"},
			},
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("pull_request malformed (missing head sha) → 200 rejected", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 42},
			"pull_request": map[string]any{"number": 1},
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})

	t.Run("pull_request non-whole number → rejected", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 42},
			"pull_request": map[string]any{
				"number": 1.5, "head": map[string]any{"sha": "abc", "ref": "feat"},
			},
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("pull_request handler error propagates", func(t *testing.T) {
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: recvSecret, AppIdentity: recvAppIdentity,
			Handlers: githubapp.WebhookHandlers{
				PullRequest: func(ctx githubapp.WebhookHandlerContext) error {
					return errors.New("handler boom")
				},
			},
			Logger: &githubapp.WebhookLogger{Info: func(string) {}, Warn: func(string) {}},
		})
		body := recvPRBody("opened", "feature/docs", "human-author")
		_, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		assert.Error(t, err)
	})

	t.Run("check_run requested_action with pull_requests PR context", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
			"repository":   map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 42},
			"check_run": map[string]any{
				"id":            99,
				"pull_requests": []any{map[string]any{"number": 3, "head": map[string]any{"sha": "shax"}}},
			},
			"requested_action": map[string]any{"identifier": "docsync/generate-patch"},
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		require.Len(t, *calls, 1)
		require.NotNil(t, (*calls)[0].CheckRun)
		require.NotNil(t, (*calls)[0].CheckRun.PR)
		assert.Equal(t, 3, (*calls)[0].CheckRun.PR.Number)
		assert.Equal(t, "shax", (*calls)[0].CheckRun.PR.HeadSha)
	})

	t.Run("check_run malformed (no check_run object) → 200 rejected", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := recvNamedBody()
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})

	t.Run("push malformed (missing after) → 200 rejected", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"sender": map[string]any{"login": "human"}, "repository": map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 42}, "ref": "refs/heads/main",
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "push", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})

	t.Run("push handler error propagates", func(t *testing.T) {
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: recvSecret, AppIdentity: recvAppIdentity,
			Handlers: githubapp.WebhookHandlers{
				Push: func(ctx githubapp.WebhookHandlerContext) error {
					return errors.New("push boom")
				},
			},
			Logger: &githubapp.WebhookLogger{Info: func(string) {}, Warn: func(string) {}},
		})
		body := recvPushBody("main", "human-author")
		_, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "push", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		assert.Error(t, err)
	})
}

/* ======================================================= webhook receiver signature & self-trigger branches */

func TestCOV_GITHUBAPP_ReceiverBranches(t *testing.T) {
	const maxBody = 1024 * 1024

	t.Run("body over 1MiB is truncated before signature check", func(t *testing.T) {
		payload := map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 42},
			"pull_request": map[string]any{
				"number": 1, "head": map[string]any{"sha": "abc", "ref": "feat"},
			},
			"padding": strings.Repeat("x", maxBody+10),
		}
		body := mustJSON(t, payload)
		assert.True(t, len(body) > maxBody)
		sig := signBody(body[:maxBody], recvSecret)
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: sig,
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.Len(t, *calls, 1)
	})

	t.Run("signature hex decode / length mismatch branches", func(t *testing.T) {
		body := `{"action":"opened"}`
		// odd-length hex → isHexString false
		assert.False(t, githubapp.VerifyWebhookSignature(body, "sha256=abc", recvSecret))
		// even-length hex that decodes but wrong bytes → length mismatch path
		good := signBody(body, recvSecret)
		assert.False(t, githubapp.VerifyWebhookSignature(body, strings.Repeat("f", len(good)-7), recvSecret))
	})

	t.Run("self-trigger via receiver: bot identity suppresses", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := recvPRBody("opened", "feature/docs", recvAppIdentity)
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultSuppressed, result.Kind)
		assert.Equal(t, 202, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})

	t.Run("self-trigger via receiver: branch prefix suppresses", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := recvPRBody("opened", "docsync/feat", "human-author")
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultSuppressed, result.Kind)
		assert.Len(t, *calls, 0)
	})
}

/* ======================================================= errors: Error()/Message() */

func TestCOV_GITHUBAPP_Errors(t *testing.T) {
	err := githubapp.AssertNoForbiddenPermission(map[string]string{"administration": "read&write"})
	require.Error(t, err)
	var forbidden *githubapp.ForbiddenPermissionError
	require.ErrorAs(t, err, &forbidden)
	assert.Equal(t, "app permissions must be the exact allowlist {contents, pull_requests, checks} each read&write — a cardinality mismatch is a forbidden grant (D16)", err.Error())
	assert.Equal(t, err.Error(), forbidden.Message())
}

/* ======================================================= stringOrNil via delivery header */

func TestCOV_GITHUBAPP_StringOrNil(t *testing.T) {
	calls, _ := newCtx()
	receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
	body := recvPRBody("opened", "feature/docs", "human-author")
	// empty delivery → nil delivery key
	_, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
		Event: "pull_request", Delivery: "", Signature: signBody(body, recvSecret),
	})
	require.NoError(t, err)
	assert.Len(t, *calls, 1)
}

/* ======================================================= errorsAsDuplicate: unwrap-to-nil path */

type nilUnwrap struct{}

func (nilUnwrap) Error() string { return "nil unwrap" }
func (nilUnwrap) Unwrap() error { return nil }

func TestCOV_GITHUBAPP_ErrorsAsDuplicateUnwrapNil(t *testing.T) {
	calls := &[]githubapp.RunPatchPortInput{}
	runPatch := func(in githubapp.RunPatchPortInput) (githubapp.RunPatchPortResult, error) {
		*calls = append(*calls, in)
		return githubapp.RunPatchPortResult{}, nilUnwrap{}
	}
	logs := &[]string{}
	app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
		AppCheckRuntime: &stubCheckRuntime{}, AppIdentity: appWebhookIdentity, Secret: appWebhookSecret,
		Logger:     loggedApp(t, logs),
		Escalation: &githubapp.AppEscalation{RunPatch: runPatch},
		Clock:      func() string { return appWebhookClockTime },
	})
	body := mustJSON(t, map[string]any{
		"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
		"repository": map[string]any{"full_name": appTestRepoID}, "installation": map[string]any{"id": 42},
		"requested_action": map[string]any{"identifier": gitpr.DOCSYNCGeneratePatchAction.Identifier},
		"check_run": map[string]any{"id": 99, "pull_requests": []any{
			map[string]any{"number": 7, "head": map[string]any{"sha": strings.Repeat("b", 40)}},
		}},
	})
	result, err := app.HandleWebhook(body, githubapp.WebhookHeaders{
		Event: "check_run", Delivery: "delivery-e4", Signature: signBody(body, appWebhookSecret),
	})
	require.NoError(t, err)
	assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
	assert.True(t, containsSubstr(*logs, "escalation failed (fail closed)"))
}

/* ======================================================= pipeline handler error lanes */

type failSvc struct {
	failOn int
	calls  int
}

func (s *failSvc) UpsertCheckRun(in githubapp.CheckRunUpsertInput) (githubapp.CheckRunUpsertResult, error) {
	s.calls++
	if s.failOn > 0 && s.calls == s.failOn {
		return githubapp.CheckRunUpsertResult{}, errors.New("upsert fail")
	}
	return githubapp.CheckRunUpsertResult{CheckRunID: 9, Created: true}, nil
}

func TestCOV_GITHUBAPP_HandlerErrorLanes(t *testing.T) {
	t.Run("config-error upsert fails → handler error", func(t *testing.T) {
		svc := &failSvc{failOn: 1}
		runCheck := githubapp.RunCheckPort(func(githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
			return githubapp.RunCheckPortResult{
				Kind:        githubapp.RunCheckPortResultConfigError,
				Diagnostics: []orchestrator.ConfigErrorDiagnostic{{File: "x.yml", Message: "bad"}},
			}, nil
		})
		handler := githubapp.CreatePullRequestCheckHandler(githubapp.PullRequestCheckHandlerDeps{Service: svc, RunCheck: runCheck})
		_, err := handler.HandlePullRequest(githubapp.PullRequestCheckInput{
			Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
		})
		assert.Error(t, err)
	})

	t.Run("runtime-inconclusive upsert fails → handler error", func(t *testing.T) {
		svc := &failSvc{failOn: 1}
		runCheck := githubapp.RunCheckPort(func(githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
			return githubapp.RunCheckPortResult{Kind: githubapp.RunCheckPortResultRuntimeInconclusive, Reason: "outage"}, nil
		})
		handler := githubapp.CreatePullRequestCheckHandler(githubapp.PullRequestCheckHandlerDeps{Service: svc, RunCheck: runCheck})
		_, err := handler.HandlePullRequest(githubapp.PullRequestCheckInput{
			Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
		})
		assert.Error(t, err)
	})

	t.Run("success final upsert fails → handler error", func(t *testing.T) {
		svc := &failSvc{failOn: 1}
		runCheck := githubapp.RunCheckPort(func(githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
			return githubapp.RunCheckPortResult{Kind: githubapp.RunCheckPortResultSuccess, Report: handlerReport([]schemas.CanonicalReportRow{handlerRow()})}, nil
		})
		handler := githubapp.CreatePullRequestCheckHandler(githubapp.PullRequestCheckHandlerDeps{Service: svc, RunCheck: runCheck})
		_, err := handler.HandlePullRequest(githubapp.PullRequestCheckInput{
			Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
		})
		assert.Error(t, err)
	})

	t.Run("early signal upsert fails → handler error", func(t *testing.T) {
		svc := &failSvc{failOn: 1}
		runCheck := githubapp.RunCheckPort(func(in githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
			_ = in.EarlySignal.Post(partialPayload("stage-1"))
			return githubapp.RunCheckPortResult{Kind: githubapp.RunCheckPortResultSuccess, Report: handlerReport(nil)}, nil
		})
		handler := githubapp.CreatePullRequestCheckHandler(githubapp.PullRequestCheckHandlerDeps{Service: svc, RunCheck: runCheck})
		_, err := handler.HandlePullRequest(githubapp.PullRequestCheckInput{
			Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
		})
		assert.NoError(t, err)        // early-signal errors are discarded by the injected port
		assert.Equal(t, 2, svc.calls) // early-signal attempt + final upsert attempted
	})
}

/* ======================================================= receiver: nil handlers & installation id shapes */

func TestCOV_GITHUBAPP_ReceiverNilHandlers(t *testing.T) {
	t.Run("check_run with no CheckRun handler → skipped", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: recvSecret, AppIdentity: recvAppIdentity,
			Handlers: githubapp.WebhookHandlers{
				PullRequest: func(ctx githubapp.WebhookHandlerContext) error { return nil },
			},
			Logger: &githubapp.WebhookLogger{Info: func(string) {}, Warn: func(string) {}},
		})
		body := recvRequestedActionBody()
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultSkipped, result.Kind)
		assert.Equal(t, 202, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})

	t.Run("check_run requested_action handler error propagates", func(t *testing.T) {
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: recvSecret, AppIdentity: recvAppIdentity,
			Handlers: githubapp.WebhookHandlers{
				CheckRun: func(ctx githubapp.WebhookHandlerContext) error {
					return errors.New("checkrun boom")
				},
			},
			Logger: &githubapp.WebhookLogger{Info: func(string) {}, Warn: func(string) {}},
		})
		body := recvRequestedActionBody()
		_, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		assert.Error(t, err)
	})

	t.Run("push with no Push handler → skipped", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: recvSecret, AppIdentity: recvAppIdentity,
			Handlers: githubapp.WebhookHandlers{
				PullRequest: func(ctx githubapp.WebhookHandlerContext) error { return nil },
			},
			Logger: &githubapp.WebhookLogger{Info: func(string) {}, Warn: func(string) {}},
		})
		body := recvPushBody("main", "human-author")
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "push", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultSkipped, result.Kind)
		assert.Equal(t, 202, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})
}

func TestCOV_GITHUBAPP_InstallationIDShapes(t *testing.T) {
	cases := []struct {
		name string
		id   any
		want any
	}{
		{"float64 non-whole", 42.5, float64(42.5)},
		{"string empty", "", nil},
		{"string non-empty", "abc", "abc"},
		{"bool (default)", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls, _ := newCtx()
			receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
			body := mustJSON(t, map[string]any{
				"action": "opened", "sender": map[string]any{"login": "human"},
				"repository":   map[string]any{"full_name": "acme/w"},
				"installation": map[string]any{"id": c.id},
				"pull_request": map[string]any{
					"number": 1, "head": map[string]any{"sha": "abc", "ref": "feat"},
				},
			})
			_, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
				Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
			})
			require.NoError(t, err)
			require.Len(t, *calls, 1)
			assert.Equal(t, c.want, (*calls)[0].InstallationID)
		})
	}
}

/* ======================================================= check_run id string / pr number string */

func TestCOV_GITHUBAPP_ParseEdges(t *testing.T) {
	t.Run("check_run id non-numeric string → id 0", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
			"repository":       map[string]any{"full_name": "acme/w"},
			"installation":     map[string]any{"id": 42},
			"check_run":        map[string]any{"id": "notanum"},
			"requested_action": map[string]any{"identifier": "docsync/generate-patch"},
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		require.Len(t, *calls, 1)
		require.NotNil(t, (*calls)[0].CheckRun)
		assert.Equal(t, int64(0), (*calls)[0].CheckRun.ID)
	})

	t.Run("pull_request number as string → rejected", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human"},
			"repository":   map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 42},
			"pull_request": map[string]any{
				"number": "seven", "head": map[string]any{"sha": "abc", "ref": "feat"},
			},
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Len(t, *calls, 0)
	})
}

/* ======================================================= final gap-closers via exported API */

func TestCOV_GITHUBAPP_FinalGaps(t *testing.T) {
	t.Run("routeCheckRun requested_action with no check_run object → rejected", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		body := mustJSON(t, map[string]any{
			"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
			"repository":       map[string]any{"full_name": "acme/w"},
			"installation":     map[string]any{"id": 42},
			"requested_action": map[string]any{"identifier": "docsync/generate-patch"},
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		assert.Len(t, *calls, 0)
		assert.True(t, containsSubstr(*logs, "malformed payload"))
	})

	t.Run("routePush with ref lacking refs/heads prefix → rejected", func(t *testing.T) {
		calls, _ := newCtx()
		receiver := makeReceiver(recvSecret, calls, &[]string{}, nil)
		body := mustJSON(t, map[string]any{
			"sender": map[string]any{"login": "human"}, "repository": map[string]any{"full_name": "acme/w"},
			"installation": map[string]any{"id": 42}, "after": "abc", "ref": "main",
		})
		result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "push", Delivery: "d1", Signature: signBody(body, recvSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("config-error lane with non-nil logger → logLine body", func(t *testing.T) {
		svc := &svcDouble{}
		logs := &[]string{}
		runCheck := githubapp.RunCheckPort(func(githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
			return githubapp.RunCheckPortResult{
				Kind:        githubapp.RunCheckPortResultConfigError,
				Diagnostics: []orchestrator.ConfigErrorDiagnostic{{File: "x.yml", Message: "bad"}},
			}, nil
		})
		handler := githubapp.CreatePullRequestCheckHandler(githubapp.PullRequestCheckHandlerDeps{
			Service: svc, RunCheck: runCheck, Logger: func(line string) { *logs = append(*logs, line) },
		})
		_, err := handler.HandlePullRequest(githubapp.PullRequestCheckInput{
			Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
		})
		assert.NoError(t, err) // config-error surface upsert succeeds → handler returns nil
		assert.True(t, containsSubstr(*logs, "config-error surface upserted"))
	})

	t.Run("AssertNoForbiddenPermission loop body: wrong permission with 3 keys → forbidden", func(t *testing.T) {
		err := githubapp.AssertNoForbiddenPermission(map[string]string{
			"contents": "read&write", "pull_requests": "read&write", "checks": "admin",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is forbidden")
	})
}
