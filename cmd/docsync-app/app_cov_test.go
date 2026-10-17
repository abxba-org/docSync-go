package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/githubapp"
)

func TestCOV_CMD_APP_FailClosedCheckRuntime(t *testing.T) {
	res, err := failClosedCheckRuntime{}.HandlePullRequest(githubapp.AppCheckInput{})
	require.Error(t, err)
	assert.ErrorIs(t, err, errCheckRuntimeUnwired)
	assert.Empty(t, res)
}

// portString is the no-op formatting seam used by cov_cmd_test.go.
func portString(s string) string { return s }

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	require.NoError(t, ln.Close())
	return port
}

func httpGetRetry(t *testing.T, url string) error {
	t.Helper()
	var last error
	for i := 0; i < 50; i++ {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		last = err
		time.Sleep(time.Millisecond)
	}
	return last
}

func appSignBody(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestCOV_CMD_APP_BuildWebhookMuxRoutes(t *testing.T) {
	t.Setenv("APP_WEBHOOK_SECRET", "appwhsec_test")
	t.Setenv("APP_IDENTITY", "docsync-bot[bot]")
	mux := buildWebhookMux()
	require.NotNil(t, mux)

	t.Run("missing signature → 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{"action":"opened"}`))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "d1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, 401, rec.Code)
	})

	t.Run("valid signature + pull_request → fail-closed 500 (runtime unwired)", func(t *testing.T) {
		body := `{"action":"opened","sender":{"login":"human"},"repository":{"full_name":"acme/w"},"installation":{"id":42},"pull_request":{"number":7,"head":{"sha":"abc123","ref":"feature/x"}}}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "d2")
		req.Header.Set("X-Hub-Signature-256", appSignBody(body, "appwhsec_test"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, 500, rec.Code)
	})

	t.Run("valid signature + check_run requested_action → 200 (escalation unwired skip)", func(t *testing.T) {
		body := `{"action":"requested_action","sender":{"login":"maintainer"},"repository":{"full_name":"acme/w"},"installation":{"id":42},"requested_action":{"identifier":"docsync/generate-patch"},"check_run":{"id":99,"pull_requests":[{"number":3,"head":{"sha":"shax"}}]}}`
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "check_run")
		req.Header.Set("X-GitHub-Delivery", "d3")
		req.Header.Set("X-Hub-Signature-256", appSignBody(body, "appwhsec_test"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, 200, rec.Code)
	})

	t.Run("unknown path → 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/nope", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, 404, rec.Code)
	})
}
