// Ported from packages/github-app/test/{webhook-verify,webhook-receiver,
// permissions,escalation-handler,upsert-service,pipeline-check-handler,
// fallback-guidance,app-webhooks,smoke}.test.ts. Black-box (package
// githubapp_test); network-free (OD-2): HMAC signing is recreated locally, the
// check-run REST surface is a fake client, the composed runtimes are injected
// doubles, and NO network is reached. The app-webhooks composed-pipeline
// deliveries (which need internal/cli) are exercised with a stub check runtime
// so the WIRING — not the pipeline — is what's under test.
package githubapp_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/githubapp"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func conclusionPtr(c orchestrator.CheckRunConclusion) *orchestrator.CheckRunConclusion {
	return &c
}

func signBody(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func strPtr(s string) *string { return &s }

func intPtr(i int) *int { return &i }

/* ===================================================== webhook signature verify */

func TestVerifyWebhookSignature(t *testing.T) {
	const secret = "whsec_test_secret"
	const body = `{"action":"opened"}`

	t.Run("accepts a valid signature", func(t *testing.T) {
		assert.True(t, githubapp.VerifyWebhookSignature(body, signBody(body, secret), secret))
	})
	t.Run("rejects a tampered body", func(t *testing.T) {
		signature := signBody(body, secret)
		assert.False(t, githubapp.VerifyWebhookSignature(`{"action":"closed"}`, signature, secret))
	})
	t.Run("rejects a tampered signature (wrong secret)", func(t *testing.T) {
		assert.False(t, githubapp.VerifyWebhookSignature(body, signBody(body, "other-secret"), secret))
	})
	t.Run("rejects a missing signature header", func(t *testing.T) {
		assert.False(t, githubapp.VerifyWebhookSignature(body, "", secret))
	})
	t.Run("rejects malformed signatures without throwing", func(t *testing.T) {
		assert.False(t, githubapp.VerifyWebhookSignature(body, "sha1=abc", secret))
		assert.False(t, githubapp.VerifyWebhookSignature(body, "sha256=", secret))
		assert.False(t, githubapp.VerifyWebhookSignature(body, "sha256=zzzz", secret))
		assert.False(t, githubapp.VerifyWebhookSignature(body, "garbage", secret))
	})
}

/* ============================================================ permissions/visual */

func TestAppPermissions(t *testing.T) {
	t.Run("grants EXACTLY contents, pull_requests, checks with read&write", func(t *testing.T) {
		perms := githubapp.AppPermissions()
		keys := make([]string, 0, len(perms))
		for scope := range perms {
			keys = append(keys, scope)
		}
		assert.ElementsMatch(t, []string{"contents", "pull_requests", "checks"}, keys)
		assert.Equal(t, "read&write", perms["contents"])
		assert.Equal(t, "read&write", perms["pull_requests"])
		assert.Equal(t, "read&write", perms["checks"])
	})

	t.Run("asserts the full forbidden list", func(t *testing.T) {
		assert.ElementsMatch(t, []string{
			"administration", "deployments", "issues", "members", "secrets", "webhooks", "workflows",
		}, githubapp.AppNotGrantedPermissions())
	})

	t.Run("rejects forbidden grants (never request write hooks)", func(t *testing.T) {
		for _, permission := range githubapp.AppNotGrantedPermissions() {
			err := githubapp.AssertNoForbiddenPermission(map[string]string{permission: "read&write"})
			var forbidden *githubapp.ForbiddenPermissionError
			require.ErrorAs(t, err, &forbidden)
		}
	})

	t.Run("rejects permissions outside the exact allowlist", func(t *testing.T) {
		var forbidden *githubapp.ForbiddenPermissionError
		require.ErrorAs(t, githubapp.AssertNoForbiddenPermission(map[string]string{"meta": "read"}), &forbidden)
	})

	t.Run("accepts the exact grant table", func(t *testing.T) {
		assert.NoError(t, githubapp.AssertNoForbiddenPermission(githubapp.AppPermissions()))
	})

	t.Run("assertRepoScopedInstallation: only repository_selection=selected passes", func(t *testing.T) {
		assert.NoError(t, githubapp.AssertRepoScopedInstallation(map[string]any{"repository_selection": "selected"}))
		var repoErr *githubapp.RepoScopeInstallationError
		require.ErrorAs(t, githubapp.AssertRepoScopedInstallation(map[string]any{}), &repoErr)
		require.ErrorAs(t, githubapp.AssertRepoScopedInstallation(map[string]any{"repository_selection": "all"}), &repoErr)
		require.ErrorAs(t, githubapp.AssertRepoScopedInstallation(map[string]any{"repository_selection": "none"}), &repoErr)
	})
}

func TestBuildAppManifest(t *testing.T) {
	t.Run("builds a manifest with the exact permissions and events", func(t *testing.T) {
		manifest := githubapp.BuildAppManifest(githubapp.AppManifestInput{
			Name: "docSync", URL: "https://example.com", WebhookURL: "https://example.com/webhook",
		})
		assert.Equal(t, githubapp.AppPermissions(), manifest.Permissions)
		assert.Equal(t, []string{"pull_request", "check_run", "push"}, manifest.Events)
		assert.Equal(t, "https://example.com/webhook", manifest.WebhookURL)
	})

	t.Run("APP_WEBHOOK_EVENTS is exactly pull_request, check_run, push", func(t *testing.T) {
		assert.Equal(t, []string{"pull_request", "check_run", "push"}, githubapp.AppWebhookEvents())
	})
}

/* ============================================================ fallback guidance */

func TestRenderFallbackGuidance(t *testing.T) {
	conversational := `(?i)(\?|please|sorry|thank|let me|feel free|happy to|would you|could you)`

	t.Run("carries the deterministic fallback line verbatim", func(t *testing.T) {
		text := githubapp.RenderFallbackGuidance(false)
		assert.Contains(t, text, githubapp.FallbackGuidanceLine)
		assert.Equal(t, "run 'docsync fix' locally to apply verified patches", githubapp.FallbackGuidanceLine)
	})

	t.Run("is mechanical: no questions, no AI/chatbot phrasing", func(t *testing.T) {
		for _, text := range []string{
			githubapp.RenderFallbackGuidance(false),
			githubapp.RenderFallbackGuidance(true),
		} {
			assert.NotRegexp(t, conversational, text)
			assert.True(t, strings.HasSuffix(text, "."))
		}
	})

	t.Run("patch-ready note appears only when a patch is ready", func(t *testing.T) {
		assert.Contains(t, githubapp.RenderFallbackGuidance(true), "patch is ready")
		assert.NotContains(t, githubapp.RenderFallbackGuidance(false), "patch is ready")
	})

	t.Run("always states patch generation requires explicit human action", func(t *testing.T) {
		assert.Contains(t, githubapp.RenderFallbackGuidance(false), "explicit human action")
		assert.Contains(t, githubapp.RenderFallbackGuidance(true), "explicit human action")
	})
}

/* ============================================================ upsert service */

type updatedCall struct {
	id    int
	input gitpr.UpdateCheckRunInput
}

type fakeCheckClient struct {
	findResults   [][]gitpr.GitHubCheckRun
	onCreate      func(call int) (gitpr.GitHubCheckRun, error)
	findCount     int
	createCount   int
	updateCount   int
	createdInputs []gitpr.CreateCheckRunInput
	updatedCalls  []updatedCall
}

func (c *fakeCheckClient) FindCheckRunsForRef(_ context.Context, _, _ string) ([]gitpr.GitHubCheckRun, error) {
	idx := c.findCount
	c.findCount++
	if idx < len(c.findResults) {
		return c.findResults[idx], nil
	}
	return []gitpr.GitHubCheckRun{}, nil
}

func (c *fakeCheckClient) CreateCheckRun(_ context.Context, _ string, input gitpr.CreateCheckRunInput) (gitpr.GitHubCheckRun, error) {
	c.createCount++
	c.createdInputs = append(c.createdInputs, input)
	if c.onCreate != nil {
		return c.onCreate(c.createCount)
	}
	name := gitpr.DOCSYNCCheckName
	return gitpr.GitHubCheckRun{ID: 100 + c.createCount, Name: &name}, nil
}

func (c *fakeCheckClient) UpdateCheckRun(_ context.Context, _ string, id int, input gitpr.UpdateCheckRunInput) (gitpr.GitHubCheckRun, error) {
	c.updateCount++
	c.updatedCalls = append(c.updatedCalls, updatedCall{id: id, input: input})
	name := gitpr.DOCSYNCCheckName
	return gitpr.GitHubCheckRun{ID: id, Name: &name}, nil
}

func ghRun(id int, status string, name string) gitpr.GitHubCheckRun {
	runName := name
	runStatus := status
	return gitpr.GitHubCheckRun{ID: id, Name: &runName, Status: &runStatus}
}

func upsertPayload() orchestrator.CheckSummaryPayload {
	return orchestrator.CheckSummaryPayload{Title: "t", Summary: "s", Text: "x"}
}

func TestCheckRunUpsertService(t *testing.T) {
	newSvc := func(c *fakeCheckClient, logs *[]string) githubapp.UpsertService {
		logger := func(line string) { *logs = append(*logs, line) }
		return githubapp.CreateCheckRunService(githubapp.CheckRunServiceDeps{
			Client: c, Clock: func() string { return "2026-10-07T00:00:00Z" }, Logger: logger,
		})
	}
	var logs []string

	t.Run("first delivery: find-empty → create (one run)", func(t *testing.T) {
		client := &fakeCheckClient{findResults: [][]gitpr.GitHubCheckRun{{}}}
		result, err := newSvc(client, &logs).UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: githubapp.UpsertCompleted,
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.CheckRunUpsertResult{CheckRunID: 101, Created: true}, result)
		assert.Equal(t, 1, client.findCount)
		assert.Equal(t, 1, client.createCount)
		assert.Equal(t, 0, client.updateCount)
	})

	t.Run("second delivery: find-existing → update by id, no second create", func(t *testing.T) {
		client := &fakeCheckClient{findResults: [][]gitpr.GitHubCheckRun{{}, {ghRun(101, "in_progress", gitpr.DOCSYNCCheckName)}}}
		svc := newSvc(client, &logs)
		first, err := svc.UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Status: githubapp.UpsertInProgress, Partial: true,
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.CheckRunUpsertResult{CheckRunID: 101, Created: true}, first)
		second, err := svc.UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: githubapp.UpsertCompleted,
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.CheckRunUpsertResult{CheckRunID: 101, Created: false}, second)
		assert.Equal(t, 1, client.createCount)
		assert.Equal(t, 1, client.updateCount)
		require.Len(t, client.updatedCalls, 1)
		assert.Equal(t, 101, client.updatedCalls[0].id)
	})

	t.Run("duplicate-create 422 → re-find path updates the raced run (still one run)", func(t *testing.T) {
		client := &fakeCheckClient{
			findResults: [][]gitpr.GitHubCheckRun{{}, {}, {ghRun(201, "in_progress", gitpr.DOCSYNCCheckName)}},
			onCreate: func(call int) (gitpr.GitHubCheckRun, error) {
				if call == 1 {
					return ghRun(201, "in_progress", gitpr.DOCSYNCCheckName), nil
				}
				return gitpr.GitHubCheckRun{}, gitpr.NewGitHubValidationError("already_exists", gitpr.GitHubRestErrorInit{Status: intPtr(422)})
			},
		}
		svc := newSvc(client, &logs)
		first, err := svc.UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(), Status: githubapp.UpsertInProgress, Partial: true,
		})
		require.NoError(t, err)
		assert.Equal(t, 201, first.CheckRunID)
		raced, err := svc.UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: githubapp.UpsertCompleted,
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.CheckRunUpsertResult{CheckRunID: 201, Created: false}, raced)
		assert.Equal(t, 3, client.findCount)
		assert.Equal(t, 1, client.updateCount)
	})

	t.Run("duplicate-create 422 with re-find still empty → typed CheckRunUpsertError code duplicate", func(t *testing.T) {
		client := &fakeCheckClient{
			findResults: [][]gitpr.GitHubCheckRun{{}, {}},
			onCreate: func(call int) (gitpr.GitHubCheckRun, error) {
				if call == 1 {
					return ghRun(301, "in_progress", gitpr.DOCSYNCCheckName), nil
				}
				return gitpr.GitHubCheckRun{}, gitpr.NewGitHubValidationError("already_exists", gitpr.GitHubRestErrorInit{Status: intPtr(422)})
			},
		}
		svc := newSvc(client, &logs)
		_, err := svc.UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(), Status: githubapp.UpsertInProgress,
		})
		require.NoError(t, err)
		_, err = svc.UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
			Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: githubapp.UpsertCompleted,
		})
		var upErr *githubapp.CheckRunUpsertError
		require.ErrorAs(t, err, &upErr)
		assert.Equal(t, githubapp.UpsertErrorDuplicate, upErr.Code)
	})

	t.Run("fail-closed contract: status completed without a conclusion is rejected BEFORE any REST call", func(t *testing.T) {
		client := &fakeCheckClient{findResults: [][]gitpr.GitHubCheckRun{{}}}
		_, err := newSvc(client, &logs).UpsertCheckRun(githubapp.CheckRunUpsertInput{
			Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(), Status: githubapp.UpsertCompleted,
		})
		var upErr *githubapp.CheckRunUpsertError
		require.ErrorAs(t, err, &upErr)
		assert.Equal(t, githubapp.UpsertErrorCompletedRequiresConclusion, upErr.Code)
		assert.Equal(t, 0, client.findCount)
		assert.Equal(t, 0, client.createCount)
		assert.Equal(t, 0, client.updateCount)
	})

	t.Run("upsert find preference", func(t *testing.T) {
		t.Run("prefers the live in-progress run over a completed one", func(t *testing.T) {
			client := &fakeCheckClient{findResults: [][]gitpr.GitHubCheckRun{{ghRun(11, "completed", gitpr.DOCSYNCCheckName), ghRun(22, "in_progress", gitpr.DOCSYNCCheckName)}}}
			result, err := newSvc(client, &logs).UpsertCheckRun(githubapp.CheckRunUpsertInput{
				Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
				Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: githubapp.UpsertCompleted,
			})
			require.NoError(t, err)
			assert.Equal(t, githubapp.CheckRunUpsertResult{CheckRunID: 22, Created: false}, result)
		})
		t.Run("falls back to the latest completed run when no live run exists", func(t *testing.T) {
			client := &fakeCheckClient{findResults: [][]gitpr.GitHubCheckRun{{ghRun(11, "completed", gitpr.DOCSYNCCheckName), ghRun(22, "completed", gitpr.DOCSYNCCheckName)}}}
			result, err := newSvc(client, &logs).UpsertCheckRun(githubapp.CheckRunUpsertInput{
				Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
				Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: githubapp.UpsertCompleted,
			})
			require.NoError(t, err)
			assert.Equal(t, 22, result.CheckRunID)
		})
		t.Run("escalationEnabled reaches the client only when requested (D10)", func(t *testing.T) {
			client := &fakeCheckClient{findResults: [][]gitpr.GitHubCheckRun{{ghRun(41, "in_progress", gitpr.DOCSYNCCheckName)}}}
			_, err := newSvc(client, &logs).UpsertCheckRun(githubapp.CheckRunUpsertInput{
				Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
				Conclusion: conclusionPtr(orchestrator.ConclusionFailure), Status: githubapp.UpsertCompleted, EscalationEnabled: true,
			})
			require.NoError(t, err)
			require.Len(t, client.updatedCalls, 1)
			assert.True(t, client.updatedCalls[0].input.EscalationEnabled)

			client2 := &fakeCheckClient{findResults: [][]gitpr.GitHubCheckRun{{ghRun(42, "in_progress", gitpr.DOCSYNCCheckName)}}}
			_, err = newSvc(client2, &logs).UpsertCheckRun(githubapp.CheckRunUpsertInput{
				Repo: "acme/widgets", HeadSha: "sha1", Payload: upsertPayload(),
				Conclusion: conclusionPtr(orchestrator.ConclusionNeutral), Status: githubapp.UpsertCompleted,
			})
			require.NoError(t, err)
			require.Len(t, client2.updatedCalls, 1)
			assert.False(t, client2.updatedCalls[0].input.EscalationEnabled)
		})
	})
}

/* ======================================================= pipeline check handler */

type svcDouble struct {
	upserts []githubapp.CheckRunUpsertInput
}

func (s *svcDouble) UpsertCheckRun(in githubapp.CheckRunUpsertInput) (githubapp.CheckRunUpsertResult, error) {
	s.upserts = append(s.upserts, in)
	return githubapp.CheckRunUpsertResult{CheckRunID: 500 + len(s.upserts), Created: len(s.upserts) == 1}, nil
}

func handlerRow() schemas.CanonicalReportRow {
	confidence := 0.5
	confidenceDisplay := "50% (policy v1, model z-ai/glm-4.7)"
	return schemas.CanonicalReportRow{
		DocPath: "docs/readme.md", Glyph: schemas.ReportGlyphWarn, Outcome: schemas.PipelineOutcomeSuggest,
		Confidence: &confidence, ConfidenceDisplay: &confidenceDisplay, Evidence: []string{},
	}
}

func handlerReport(rows []schemas.CanonicalReportRow) *schemas.CanonicalReport {
	generatedAt := "2026-10-07T00:00:00Z"
	return &schemas.CanonicalReport{
		RunID:  "run_test",
		Key:    schemas.RunKey{Repo: "acme/widgets", BaseSHA: "basesha", HeadSHA: "headsha", ManifestVersion: 1, PolicyVersion: 1},
		Status: schemas.RunStatusCompleted, GeneratedAt: &generatedAt, PolicyVersion: 1,
		ModelBindings: []schemas.RunModelBinding{}, Rows: rows,
		Abstains: []schemas.RunAbstain{}, Findings: []schemas.RunFinding{},
		Degradations: []schemas.Degradation{}, UnknownCoverage: []string{},
	}
}

func partialPayload(suffix string) orchestrator.CheckSummaryPayload {
	return orchestrator.CheckSummaryPayload{
		Title:   "docsync: documentation check (early signal)",
		Summary: "stage 1 decision pinned " + suffix,
		Text:    "stage 1 decision pinned " + suffix,
	}
}

func TestPullRequestCheckHandler(t *testing.T) {
	newHandler := func(
		result githubapp.RunCheckPortResult, deps githubapp.PullRequestCheckHandlerDeps,
	) (githubapp.PullRequestCheckHandler, *svcDouble, githubapp.PullRequestCheckInput) {
		svc := &svcDouble{}
		runCheck := githubapp.RunCheckPort(func(githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
			return result, nil
		})
		deps.Service = svc
		deps.RunCheck = runCheck
		return githubapp.CreatePullRequestCheckHandler(deps), svc, githubapp.PullRequestCheckInput{
			Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
		}
	}

	t.Run("config-error → failure check with offending lines, one upsert", func(t *testing.T) {
		handler, svc, input := newHandler(githubapp.RunCheckPortResult{
			Kind:        githubapp.RunCheckPortResultConfigError,
			Diagnostics: []orchestrator.ConfigErrorDiagnostic{{File: ".docsync/manifest.yml", Message: "unknown mode", Line: intPtr(12)}},
		}, githubapp.PullRequestCheckHandlerDeps{})
		result, err := handler.HandlePullRequest(input)
		require.NoError(t, err)
		assert.Equal(t, "config-error", result.Kind)
		require.NotNil(t, result.Conclusion)
		assert.Equal(t, orchestrator.ConclusionFailure, *result.Conclusion)
		require.Len(t, svc.upserts, 1)
		up := svc.upserts[0]
		assert.Equal(t, githubapp.UpsertCompleted, up.Status)
		require.NotNil(t, up.Conclusion)
		assert.Equal(t, orchestrator.ConclusionFailure, *up.Conclusion)
		assert.Equal(t, "docsync: configuration error", up.Payload.Title)
		assert.Contains(t, up.Payload.Text, "`.docsync/manifest.yml:12`: unknown mode")
		assert.False(t, up.EscalationEnabled)
	})

	t.Run("runtime-inconclusive → neutral inconclusive, never 'no docs affected'", func(t *testing.T) {
		handler, svc, input := newHandler(githubapp.RunCheckPortResult{
			Kind: githubapp.RunCheckPortResultRuntimeInconclusive, Reason: "analyzer outage",
		}, githubapp.PullRequestCheckHandlerDeps{})
		result, err := handler.HandlePullRequest(input)
		require.NoError(t, err)
		assert.Equal(t, "runtime-inconclusive", result.Kind)
		require.NotNil(t, result.Conclusion)
		assert.Equal(t, orchestrator.ConclusionNeutral, *result.Conclusion)
		require.Len(t, svc.upserts, 1)
		up := svc.upserts[0]
		require.NotNil(t, up.Conclusion)
		assert.Equal(t, orchestrator.ConclusionNeutral, *up.Conclusion)
		assert.Contains(t, up.Payload.Title, "inconclusive")
		assert.Contains(t, up.Payload.Summary, "Inconclusive")
		assert.NotContains(t, up.Payload.Summary, "no docs affected")
		assert.Contains(t, up.Payload.Summary, "analyzer outage")
	})

	t.Run("early signals stay in_progress; only the final upsert completes with a conclusion", func(t *testing.T) {
		finalReport := handlerReport([]schemas.CanonicalReportRow{handlerRow()})
		svc := &svcDouble{}
		runCheck := githubapp.RunCheckPort(func(in githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
			require.NotNil(t, in.EarlySignal)
			_ = in.EarlySignal.Post(partialPayload("stage-1"))
			_ = in.EarlySignal.Post(partialPayload("stage-1"))
			_ = in.EarlySignal.Update(partialPayload("verification rows"))
			return githubapp.RunCheckPortResult{Kind: githubapp.RunCheckPortResultSuccess, Report: finalReport}, nil
		})
		handler := githubapp.CreatePullRequestCheckHandler(githubapp.PullRequestCheckHandlerDeps{Service: svc, RunCheck: runCheck})
		result, err := handler.HandlePullRequest(githubapp.PullRequestCheckInput{
			Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
		})
		require.NoError(t, err)
		assert.Equal(t, "completed", result.Kind)
		require.NotNil(t, result.Conclusion)
		assert.Equal(t, orchestrator.ConclusionNeutral, *result.Conclusion)
		require.Len(t, svc.upserts, 3)
		first, second, final := svc.upserts[0], svc.upserts[1], svc.upserts[2]
		assert.Equal(t, githubapp.UpsertInProgress, first.Status)
		assert.Nil(t, first.Conclusion)
		assert.True(t, first.Partial)
		assert.Equal(t, githubapp.UpsertInProgress, second.Status)
		assert.Equal(t, githubapp.UpsertCompleted, final.Status)
		require.NotNil(t, final.Conclusion)
		assert.Equal(t, orchestrator.ConclusionNeutral, *final.Conclusion)
		assert.False(t, final.Partial)
		expected, buildErr := orchestrator.BuildCheckPayload(finalReport, nil)
		require.NoError(t, buildErr)
		assert.Equal(t, expected.Title, final.Payload.Title)
	})

	t.Run("dedupes consecutive identical signals by hash (≤1 update per stage transition)", func(t *testing.T) {
		svc := &svcDouble{}
		runCheck := githubapp.RunCheckPort(func(in githubapp.RunCheckPortInput) (githubapp.RunCheckPortResult, error) {
			_ = in.EarlySignal.Post(partialPayload("stage-1"))
			_ = in.EarlySignal.Post(partialPayload("stage-1"))
			_ = in.EarlySignal.Update(partialPayload("stage-2/3"))
			_ = in.EarlySignal.Update(partialPayload("stage-2/3"))
			return githubapp.RunCheckPortResult{Kind: githubapp.RunCheckPortResultSuccess, Report: handlerReport(nil)}, nil
		})
		handler := githubapp.CreatePullRequestCheckHandler(githubapp.PullRequestCheckHandlerDeps{Service: svc, RunCheck: runCheck})
		_, err := handler.HandlePullRequest(githubapp.PullRequestCheckInput{
			Repo: "acme/widgets", Base: "main", Head: "headsha", PR: githubapp.RunCheckPortPR{Number: 7},
		})
		require.NoError(t, err)
		require.Len(t, svc.upserts, 3)
		assert.Equal(t, githubapp.UpsertInProgress, svc.upserts[0].Status)
		assert.Contains(t, svc.upserts[0].Payload.Summary, "stage-1")
		assert.Equal(t, githubapp.UpsertInProgress, svc.upserts[1].Status)
		assert.Contains(t, svc.upserts[1].Payload.Summary, "stage-2/3")
		assert.Equal(t, githubapp.UpsertCompleted, svc.upserts[2].Status)
		require.NotNil(t, svc.upserts[2].Conclusion)
		assert.Equal(t, orchestrator.ConclusionSuccess, *svc.upserts[2].Conclusion)
	})

	t.Run("advisory default: drift reported, check stays neutral, no escalation action (D10)", func(t *testing.T) {
		handler, svc, input := newHandler(githubapp.RunCheckPortResult{
			Kind: githubapp.RunCheckPortResultSuccess, Report: handlerReport([]schemas.CanonicalReportRow{handlerRow()}),
		}, githubapp.PullRequestCheckHandlerDeps{})
		result, err := handler.HandlePullRequest(input)
		require.NoError(t, err)
		assert.Equal(t, "completed", result.Kind)
		require.NotNil(t, result.Conclusion)
		assert.Equal(t, orchestrator.ConclusionNeutral, *result.Conclusion)
		assert.False(t, svc.upserts[0].EscalationEnabled)
	})

	t.Run("strict: enforcement engaged, check fails and attaches the escalation action (D10)", func(t *testing.T) {
		handler, svc, input := newHandler(githubapp.RunCheckPortResult{
			Kind: githubapp.RunCheckPortResultSuccess, Report: handlerReport([]schemas.CanonicalReportRow{handlerRow()}),
		}, githubapp.PullRequestCheckHandlerDeps{Strict: true})
		result, err := handler.HandlePullRequest(input)
		require.NoError(t, err)
		require.NotNil(t, result.Conclusion)
		assert.Equal(t, orchestrator.ConclusionFailure, *result.Conclusion)
		require.Len(t, svc.upserts, 1)
		require.NotNil(t, svc.upserts[0].Conclusion)
		assert.Equal(t, orchestrator.ConclusionFailure, *svc.upserts[0].Conclusion)
		assert.True(t, svc.upserts[0].EscalationEnabled)
	})
}

/* ============================================================ escalation handler */

func TestEscalationHandler(t *testing.T) {
	const clock = "2026-10-07T00:00:00Z"

	patchDouble := func() (githubapp.RunPatchPort, *[]githubapp.RunPatchPortInput) {
		calls := &[]githubapp.RunPatchPortInput{}
		port := func(in githubapp.RunPatchPortInput) (githubapp.RunPatchPortResult, error) {
			*calls = append(*calls, in)
			return githubapp.RunPatchPortResult{Kind: githubapp.PatchShipped, Detail: "doc PR opened"}, nil
		}
		return port, calls
	}
	request := func() githubapp.GeneratePatchRequest {
		return githubapp.GeneratePatchRequest{
			Repo: "acme/widgets", PR: githubapp.RunPatchPortPRNumber{Number: 7, HeadSha: "headsha"},
			ActionIdentifier: strPtr(gitpr.DOCSYNCGeneratePatchAction.Identifier), IdempotencyKey: "delivery-1",
		}
	}
	newHandler := func(patch githubapp.RunPatchPort, auto *githubapp.AutoProposal) githubapp.GeneratePatchHandler {
		return githubapp.CreateGeneratePatchHandler(githubapp.GeneratePatchHandlerDeps{
			RunPatch: patch, Clock: func() string { return clock }, AutoProposal: auto,
		})
	}

	t.Run("runs exactly once per idempotency key; a repeat raises DuplicateEscalationError", func(t *testing.T) {
		patch, calls := patchDouble()
		handler := newHandler(patch, nil)
		result, err := handler.HandleRequestedAction(request())
		require.NoError(t, err)
		assert.Equal(t, githubapp.EscalationShipped, result.Kind)
		assert.Equal(t, "doc PR opened", result.Detail)
		assert.Len(t, *calls, 1)
		_, err = handler.HandleRequestedAction(request())
		var dup *githubapp.DuplicateEscalationError
		require.ErrorAs(t, err, &dup)
		assert.Equal(t, "delivery-1", dup.IdempotencyKey)
		assert.Len(t, *calls, 1)
	})

	t.Run("unknown action identifier → typed no-op, port never invoked", func(t *testing.T) {
		patch, calls := patchDouble()
		handler := newHandler(patch, nil)
		req := request()
		req.ActionIdentifier = strPtr("other/action")
		result, err := handler.HandleRequestedAction(req)
		require.NoError(t, err)
		assert.Equal(t, githubapp.EscalationIgnored, result.Kind)
		reqNone := request()
		reqNone.ActionIdentifier = nil
		resultNone, err := handler.HandleRequestedAction(reqNone)
		require.NoError(t, err)
		assert.Equal(t, githubapp.EscalationIgnored, resultNone.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("auto-proposal default OFF: no click, no opt-in → no patch generation", func(t *testing.T) {
		patch, calls := patchDouble()
		handler := newHandler(patch, nil)
		result, err := handler.RunAutoProposal(request())
		require.NoError(t, err)
		assert.Equal(t, githubapp.EscalationIgnored, result.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("auto-proposal explicitly enabled → runs through the same run-once registry", func(t *testing.T) {
		patch, calls := patchDouble()
		handler := newHandler(patch, &githubapp.AutoProposal{Enabled: true})
		result, err := handler.RunAutoProposal(request())
		require.NoError(t, err)
		assert.Equal(t, githubapp.EscalationShipped, result.Kind)
		assert.Len(t, *calls, 1)
		_, err = handler.RunAutoProposal(request())
		var dup *githubapp.DuplicateEscalationError
		require.ErrorAs(t, err, &dup)
		assert.Len(t, *calls, 1)
	})
}

/* ============================================================ smoke */

func TestGitHubAppSmoke(t *testing.T) {
	assert.NotEmpty(t, githubapp.AppPackageVersion)
}

/* ============================================================ webhook receiver */

const recvSecret = "whsec_test"
const recvAppIdentity = "docsync-bot[bot]"

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := jsonMarshal(v)
	require.NoError(t, err)
	return string(raw)
}

func recvPRBody(action string, headBranch, sender string) string {
	logs := &[]string{}
	_ = logs
	return mustJSONStr(map[string]any{
		"action":       action,
		"sender":       map[string]any{"login": sender},
		"repository":   map[string]any{"full_name": "acme/widgets"},
		"installation": map[string]any{"id": 42},
		"pull_request": map[string]any{
			"number": 7,
			"head":   map[string]any{"sha": "abc123", "ref": headBranch},
		},
	})
}

func recvPushBody(branch, sender string) string {
	return mustJSONStr(map[string]any{
		"sender":       map[string]any{"login": sender},
		"repository":   map[string]any{"full_name": "acme/widgets"},
		"installation": map[string]any{"id": 42},
		"ref":          "refs/heads/" + branch,
		"after":        "deadbeef",
	})
}

func recvRequestedActionBody() string {
	return mustJSONStr(map[string]any{
		"action":       "requested_action",
		"sender":       map[string]any{"login": "maintainer"},
		"repository":   map[string]any{"full_name": "acme/widgets"},
		"installation": map[string]any{"id": 42},
		"check_run":    map[string]any{"id": 99},
	})
}

func recvNamedBody() string {
	return mustJSONStr(map[string]any{
		"action":       "created",
		"sender":       map[string]any{"login": "someone"},
		"repository":   map[string]any{"full_name": "acme/widgets"},
		"installation": map[string]any{"id": 42},
	})
}

func mustJSONStr(v any) string {
	raw, err := jsonMarshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

type receiverDouble struct {
	receiver *githubapp.WebhookReceiver
	calls    []githubapp.WebhookHandlerContext
}

func makeReceiver(secret string, onCalls *[]githubapp.WebhookHandlerContext, logs *[]string, credentialsFor githubapp.CredentialsForFunc) *githubapp.WebhookReceiver {
	record := func(ctx githubapp.WebhookHandlerContext) error {
		*onCalls = append(*onCalls, ctx)
		return nil
	}
	return githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
		Secret:      secret,
		AppIdentity: recvAppIdentity,
		Handlers: githubapp.WebhookHandlers{
			PullRequest: record,
			CheckRun:    record,
			Push:        record,
		},
		Logger: &githubapp.WebhookLogger{
			Info: func(m string) { *logs = append(*logs, m) },
			Warn: func(m string) { *logs = append(*logs, m) },
		},
		CredentialsFor: credentialsFor,
	})
}

func deliver(t *testing.T, receiver *githubapp.WebhookReceiver, event, body string) githubapp.WebhookResult {
	t.Helper()
	result, err := receiver.HandleWebhook(body, githubapp.WebhookHeaders{
		Event: event, Delivery: "delivery-1", Signature: signBody(body, recvSecret),
	})
	require.NoError(t, err)
	return result
}

func TestWebhookReceiver(t *testing.T) {

	t.Run("dispatches pull_request opened", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "pull_request", recvPRBody("opened", "feature/docs", "human-author"))
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		assert.Len(t, *calls, 1)
	})

	t.Run("dispatches pull_request synchronize with normalized pr ctx", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "pull_request", recvPRBody("synchronize", "feature/docs", "human-author"))
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		require.Len(t, *calls, 1)
		ctx := (*calls)[0]
		require.NotNil(t, ctx.PR)
		assert.Equal(t, 7, ctx.PR.Number)
		assert.Equal(t, "abc123", ctx.PR.HeadSha)
		assert.Equal(t, "feature/docs", ctx.PR.HeadBranch)
		assert.Equal(t, "synchronize", ctx.PR.Action)
		assert.Nil(t, ctx.PR.BaseBranch)
		assert.Equal(t, int64(42), ctx.InstallationID)
		require.NotNil(t, ctx.Repo)
		assert.Equal(t, "acme/widgets", *ctx.Repo)
		require.NotNil(t, ctx.Actor)
		assert.Equal(t, "human-author", *ctx.Actor)
		require.NotNil(t, ctx.DeliveryID)
		assert.Equal(t, "delivery-1", *ctx.DeliveryID)
	})

	t.Run("logged no-op for pull_request closed", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "pull_request", recvPRBody("closed", "feature/docs", "human-author"))
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		assert.Len(t, *calls, 0)
		assert.True(t, containsSubstr(*logs, "action=closed ignored (not in webhook surface)"))
	})

	t.Run("dispatches check_run requested_action", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "check_run", recvRequestedActionBody())
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.Len(t, *calls, 1)
	})

	t.Run("logged no-op for check_run with other actions", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "check_run", recvNamedBody())
		assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("dispatches push with normalized push ctx", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "push", recvPushBody("main", "human-author"))
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		require.Len(t, *calls, 1)
		ctx := (*calls)[0]
		require.NotNil(t, ctx.Push)
		assert.Equal(t, "main", ctx.Push.Branch)
		assert.Equal(t, "deadbeef", ctx.Push.After)
	})

	for _, event := range []string{"issues", "star", "deployment"} {
		t.Run("logged no-op rejection for unlisted event "+event, func(t *testing.T) {
			calls, logs := newCtx()
			receiver := makeReceiver(recvSecret, calls, logs, nil)
			result := deliver(t, receiver, event, recvNamedBody())
			assert.Equal(t, githubapp.WebhookResultRejected, result.Kind)
			assert.Equal(t, event, result.Event)
			assert.Equal(t, 200, result.HTTPStatus)
			assert.Len(t, *calls, 0)
			assert.True(t, containsSubstr(*logs, "event="+event) && containsSubstr(*logs, "ignored (not in webhook surface)"))
		})
	}

	t.Run("invalid signature → 401-class result and no handler invocation", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result, err := receiver.HandleWebhook(recvPRBody("opened", "feature/docs", "human-author"), githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "d", Signature: "sha256=" + strings.Repeat("0", 64),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultSignatureError, result.Kind)
		assert.Equal(t, 401, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})

	t.Run("missing handler → accepted-but-skipped", func(t *testing.T) {
		logs := &[]string{}
		receiver := githubapp.NewWebhookReceiver(githubapp.WebhookReceiverOptions{
			Secret: recvSecret, AppIdentity: recvAppIdentity,
			Handlers: githubapp.WebhookHandlers{CheckRun: func(githubapp.WebhookHandlerContext) error { return nil }},
			Logger:   &githubapp.WebhookLogger{Info: func(m string) { *logs = append(*logs, m) }, Warn: func(m string) { *logs = append(*logs, m) }},
		})
		result := deliver(t, receiver, "pull_request", recvPRBody("opened", "feature/docs", "human-author"))
		assert.Equal(t, githubapp.WebhookResultSkipped, result.Kind)
		assert.Equal(t, 202, result.HTTPStatus)
		assert.True(t, containsSubstr(*logs, "no handler registered"))
	})

	t.Run("credentialsFor absent → {kind:none}; present → injected", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		deliver(t, receiver, "pull_request", recvPRBody("opened", "feature/docs", "human-author"))
		require.Len(t, *calls, 1)
		assert.Equal(t, gitpr.PrCredentialsNone, (*calls)[0].Credentials.Kind)

		calls2, logs2 := newCtx()
		injected := makeReceiver(recvSecret, calls2, logs2, func(githubapp.CredentialsInput) *gitpr.PrCredentials {
			return &gitpr.PrCredentials{Kind: gitpr.PrCredentialsAppInstallation, InstallationID: "42", Token: "ghs_x"}
		})
		deliver(t, injected, "pull_request", recvPRBody("opened", "feature/docs", "human-author"))
		require.Len(t, *calls2, 1)
		assert.Equal(t, gitpr.PrCredentialsAppInstallation, (*calls2)[0].Credentials.Kind)
		assert.Equal(t, "42", (*calls2)[0].Credentials.InstallationID)
		assert.Equal(t, "ghs_x", (*calls2)[0].Credentials.Token)
	})
}

func TestWebhookReceiverSelfTrigger(t *testing.T) {
	t.Run("identity + branch prefix → suppressed, handler not invoked", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "pull_request", recvPRBody("opened", "docsync/update-readme", recvAppIdentity))
		assert.Equal(t, githubapp.WebhookResultSuppressed, result.Kind)
		assert.Equal(t, 202, result.HTTPStatus)
		assert.Len(t, *calls, 0)
	})

	t.Run("branch prefix only → suppressed", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "pull_request", recvPRBody("opened", "docsync/human-branch", "human-author"))
		assert.Equal(t, githubapp.WebhookResultSuppressed, result.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("identity only → suppressed", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "pull_request", recvPRBody("opened", "feature/other", recvAppIdentity))
		assert.Equal(t, githubapp.WebhookResultSuppressed, result.Kind)
		assert.Len(t, *calls, 0)
	})

	t.Run("neither → dispatched", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		result := deliver(t, receiver, "pull_request", recvPRBody("opened", "feature/docs", "human-author"))
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.Len(t, *calls, 1)
	})

	t.Run("push deliveries suppressed by the same checks", func(t *testing.T) {
		calls, logs := newCtx()
		receiver := makeReceiver(recvSecret, calls, logs, nil)
		branchSuppressed := deliver(t, receiver, "push", recvPushBody("docsync/auto-update", "human-author"))
		assert.Equal(t, githubapp.WebhookResultSuppressed, branchSuppressed.Kind)
		botPush := deliver(t, receiver, "push", recvPushBody("main", recvAppIdentity))
		assert.Equal(t, githubapp.WebhookResultSuppressed, botPush.Kind)
		dispatched := deliver(t, receiver, "push", recvPushBody("main", "human-author"))
		assert.Equal(t, githubapp.WebhookResultHandled, dispatched.Kind)
		assert.Len(t, *calls, 1)
	})
}

/* ===================================================== app webhook wiring */

const (
	appWebhookSecret    = "whsec_p8e1a"
	appWebhookIdentity  = "docsync-bot[bot]"
	appTestRepoID       = "acme/action-fixture"
	appWebhookClockTime = "2026-10-07T12:00:00.000Z"
)

type stubCheckRuntime struct {
	calls []githubapp.AppCheckInput
}

func (s *stubCheckRuntime) HandlePullRequest(in githubapp.AppCheckInput) (githubapp.AppCheckResult, error) {
	s.calls = append(s.calls, in)
	return githubapp.AppCheckResult{Kind: githubapp.AppCheckResultPosted}, nil
}

func loggedApp(t *testing.T, logs *[]string) *githubapp.WebhookLogger {
	return &githubapp.WebhookLogger{
		Info: func(m string) { *logs = append(*logs, m) },
		Warn: func(m string) { *logs = append(*logs, m) },
	}
}

func TestAppWebhookHandler(t *testing.T) {
	t.Run("pull_request opened runs the check runtime through the receiver surface", func(t *testing.T) {
		check := &stubCheckRuntime{}
		logs := &[]string{}
		app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
			AppCheckRuntime: check, AppIdentity: appWebhookIdentity, Secret: appWebhookSecret,
			Logger: loggedApp(t, logs), Clock: func() string { return appWebhookClockTime },
		})
		body := mustJSON(t, map[string]any{
			"action": "opened", "sender": map[string]any{"login": "human-author"},
			"repository": map[string]any{"full_name": appTestRepoID}, "installation": map[string]any{"id": 42},
			"pull_request": map[string]any{
				"number": 7,
				"head":   map[string]any{"sha": "head456", "ref": "feature/docs"},
				"base":   map[string]any{"ref": "base123"},
			},
		})
		result, err := app.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "pull_request", Delivery: "delivery-1", Signature: signBody(body, appWebhookSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.Equal(t, 200, result.HTTPStatus)
		require.Len(t, check.calls, 1)
		assert.Equal(t, 7, check.calls[0].PR.Number)
		assert.Equal(t, "head456", check.calls[0].PR.HeadSha)
		assert.Equal(t, "feature/docs", check.calls[0].PR.HeadBranch)
		assert.Equal(t, "base123", check.calls[0].PR.BaseBranch)
	})

	t.Run("check_run.requested_action stays an UNWIRED escalation seam (logged skip, no patch path)", func(t *testing.T) {
		check := &stubCheckRuntime{}
		logs := &[]string{}
		app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
			AppCheckRuntime: check, AppIdentity: appWebhookIdentity, Secret: appWebhookSecret,
			Logger: loggedApp(t, logs),
		})
		body := mustJSON(t, map[string]any{
			"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
			"repository": map[string]any{"full_name": appTestRepoID}, "installation": map[string]any{"id": 42},
			"check_run": map[string]any{"id": 99},
		})
		result, err := app.HandleWebhook(body, githubapp.WebhookHeaders{
			Event: "check_run", Delivery: "delivery-2", Signature: signBody(body, appWebhookSecret),
		})
		require.NoError(t, err)
		assert.Equal(t, githubapp.WebhookResultHandled, result.Kind)
		assert.True(t, containsSubstr(*logs, "escalation: composed handler not configured"))
		assert.Len(t, check.calls, 0)
	})

	t.Run("wired escalation: click runs runPatch once; redelivery is a typed duplicate; unknown identifier is a no-op", func(t *testing.T) {
		calls := &[]githubapp.RunPatchPortInput{}
		runPatch := func(in githubapp.RunPatchPortInput) (githubapp.RunPatchPortResult, error) {
			*calls = append(*calls, in)
			return githubapp.RunPatchPortResult{Kind: githubapp.PatchShipped, Detail: "doc PR #12 created on branch docsync/pr-7-abc1234"}, nil
		}
		logs := &[]string{}
		app := githubapp.CreateAppWebhookHandler(githubapp.AppWebhookHandlerDeps{
			AppCheckRuntime: &stubCheckRuntime{}, AppIdentity: appWebhookIdentity, Secret: appWebhookSecret,
			Logger:     loggedApp(t, logs),
			Escalation: &githubapp.AppEscalation{RunPatch: runPatch},
			Clock:      func() string { return appWebhookClockTime },
		})

		clickBody := func(delivery, identifier string) string {
			return mustJSON(t, map[string]any{
				"action": "requested_action", "sender": map[string]any{"login": "maintainer"},
				"repository": map[string]any{"full_name": appTestRepoID}, "installation": map[string]any{"id": 42},
				"requested_action": map[string]any{"identifier": identifier},
				"check_run":        map[string]any{"id": 99, "pull_requests": []any{map[string]any{"number": 7, "head": map[string]any{"sha": strings.Repeat("b", 40)}}}},
			})
		}
		send := func(body, delivery string) githubapp.WebhookResult {
			result, err := app.HandleWebhook(body, githubapp.WebhookHeaders{
				Event: "check_run", Delivery: delivery, Signature: signBody(body, appWebhookSecret),
			})
			require.NoError(t, err)
			return result
		}

		first := send(clickBody("delivery-3", gitpr.DOCSYNCGeneratePatchAction.Identifier), "delivery-3")
		assert.Equal(t, githubapp.WebhookResultHandled, first.Kind)
		assert.Len(t, *calls, 1)
		assert.Equal(t, appTestRepoID, (*calls)[0].Repo)
		assert.Equal(t, 7, (*calls)[0].PR.Number)
		assert.Equal(t, "escalation-delivery-3", (*calls)[0].IdempotencyKey)

		send(clickBody("delivery-4", gitpr.DOCSYNCGeneratePatchAction.Identifier), "delivery-4")
		assert.Len(t, *calls, 2)
		assert.Equal(t, "escalation-delivery-4", (*calls)[1].IdempotencyKey)

		send(clickBody("delivery-3", gitpr.DOCSYNCGeneratePatchAction.Identifier), "delivery-3")
		assert.Len(t, *calls, 2)
		assert.True(t, containsSubstr(*logs, "escalation duplicate rejected (key=escalation-delivery-3)"))

		ignored := send(clickBody("delivery-5", "some.other.button"), "delivery-5")
		assert.Equal(t, githubapp.WebhookResultHandled, ignored.Kind)
		assert.Len(t, *calls, 2)
		assert.True(t, containsSubstrBoth(*logs, "escalation ignored", "some.other.button"))
	})
}

/* ------------------------------------------------------------- test utils */

func newCtx() (*[]githubapp.WebhookHandlerContext, *[]string) {
	return &[]githubapp.WebhookHandlerContext{}, &[]string{}
}

func containsSubstr(lines []string, sub string) bool {
	for _, line := range lines {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

func containsSubstrBoth(lines []string, a, b string) bool {
	for _, line := range lines {
		if strings.Contains(line, a) && strings.Contains(line, b) {
			return true
		}
	}
	return false
}
