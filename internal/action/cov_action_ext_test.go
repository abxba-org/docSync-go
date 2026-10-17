package action_test

import (
	"encoding/json"
	"errors"
	"github.com/DUDE616/docSync-go/internal/action"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
)

/* ================================================================ event */

func TestParseActionEventNilAndDefaultPayloads(t *testing.T) {
	var eventErr *action.ActionEventError

	// nil payload -> parseActionPayload nil lane
	_, err := action.ParseActionEvent(nil, eventEnv())
	require.ErrorAs(t, err, &eventErr)

	// non-string/non-map/non-nil payload -> default lane
	_, err = action.ParseActionEvent(42, eventEnv())
	require.ErrorAs(t, err, &eventErr)
}

func TestParseActionEventNumberShapes(t *testing.T) {
	var eventErr *action.ActionEventError
	base := map[string]any{
		"repository": map[string]any{"full_name": "acme/payments"},
		"pull_request": map[string]any{
			"head": map[string]any{
				"sha": "abc", "ref": "feature/x",
				"repo": map[string]any{"full_name": "acme/payments"},
			},
			"base": map[string]any{"ref": "main"},
		},
	}

	setNumber := func(p map[string]any, n any) {
		p["pull_request"].(map[string]any)["number"] = n
	}

	// number as Go int (int branch)
	p := cloneMap(base)
	setNumber(p, 7)
	ctx, err := action.ParseActionEvent(p, eventEnv())
	require.NoError(t, err)
	assert.Equal(t, 7, ctx.PR.Number)

	// number as int64 (int64 branch)
	p = cloneMap(base)
	setNumber(p, int64(11))
	ctx, err = action.ParseActionEvent(p, eventEnv())
	require.NoError(t, err)
	assert.Equal(t, 11, ctx.PR.Number)

	// number as json.Number (json.Number branch)
	p = cloneMap(base)
	setNumber(p, json.Number("13"))
	ctx, err = action.ParseActionEvent(p, eventEnv())
	require.NoError(t, err)
	assert.Equal(t, 13, ctx.PR.Number)

	// number as string -> default lane (error)
	p = cloneMap(base)
	setNumber(p, "seven")
	_, err = action.ParseActionEvent(p, eventEnv())
	require.ErrorAs(t, err, &eventErr)
}

func TestParseActionEventPushTagRef(t *testing.T) {
	// push with a tags ref exercises stripRefsHeads non-prefix lane
	ctx, err := action.ParseActionEvent(map[string]any{"ref": "refs/tags/v1.2", "after": "cafe"},
		&action.RunnerEnv{GithubEventName: "push"})
	require.NoError(t, err)
	assert.Equal(t, "refs/tags/v1.2", ctx.Push.Branch)
}

/* ================================================================ main */

func TestOSSeams(t *testing.T) {
	assert.NotEmpty(t, action.OSEnv())
	assert.NotNil(t, action.OSFS())
	assert.NotNil(t, action.OSIO())
}

func TestMapActionInputsToRunnerEnvGithubPassthrough(t *testing.T) {
	env := action.MapActionInputsToRunnerEnv(map[string]string{
		"GITHUB_REF":        "refs/heads/main",
		"GITHUB_HEAD_REF":   "feature/x",
		"GITHUB_BASE_REF":   "main",
		"GITHUB_SHA":        "abc123",
		"GITHUB_REPOSITORY": "acme/payments",
		"GITHUB_TOKEN":      "ghs_tok",
	})
	assert.Equal(t, "refs/heads/main", env.GithubRef)
	assert.Equal(t, "feature/x", env.GithubHeadRef)
	assert.Equal(t, "main", env.GithubBaseRef)
	assert.Equal(t, "abc123", env.GithubSha)
	assert.Equal(t, "acme/payments", env.GithubRepository)
	assert.Equal(t, "ghs_tok", env.GithubToken)
}

func TestWriteOutputsMissingAndError(t *testing.T) {
	eventJSON := mustMarshal(sameRepoPayload())

	// missing GITHUB_OUTPUT -> silent no-op
	capture := makeCapture(
		map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json"},
		map[string]string{"event.json": eventJSON},
	)
	delete(capture.env, "GITHUB_OUTPUT")
	_ = action.RunMain(capture.deps())
	assert.Len(t, capture.outputs, 0)

	// unwritable GITHUB_OUTPUT -> logged, not a crash (failing AppendFile fs)
	failingFS := &action.MainFS{
		ReadFile: func(path string) (string, error) {
			if path == "event.json" {
				return eventJSON, nil
			}
			return "", nil
		},
		AppendFile: func(path, data string) error { return errors.New("ENOSPC") },
	}
	var stderr []string
	var exitCodes []int
	io := &action.MainIO{
		Stderr:      func(line string) { stderr = append(stderr, line) },
		SetExitCode: func(code int) { exitCodes = append(exitCodes, code) },
	}
	res := action.RunMain(action.MainDeps{
		Env: map[string]string{
			"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json",
			"GITHUB_OUTPUT": "/etc/passwd/output",
		},
		FS: failingFS, IO: io, Check: capture.check,
	})
	assert.Contains(t, strings.Join(stderr, "\n"), "GITHUB_OUTPUT write skipped")
	assert.Equal(t, []int{3}, exitCodes)
	assert.Equal(t, orchestrator.ExitRuntime, res.ExitCode)
}

func TestRunMainOfflineInputWarn(t *testing.T) {
	eventJSON := mustMarshal(sameRepoPayload())
	capture := makeCapture(
		map[string]string{
			"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json",
			"INPUT_OFFLINE": "true",
		},
		map[string]string{"event.json": eventJSON},
	)
	res := action.RunMain(capture.deps())
	assert.Contains(t, strings.Join(capture.stderr, "\n"), "input 'offline: true' is not wired")
	assert.Equal(t, orchestrator.ExitRuntime, res.ExitCode)
}

func TestRunMainDefaultPortErrorConfig(t *testing.T) {
	eventJSON := mustMarshal(sameRepoPayload())
	d := makeCapture(
		map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json"},
		map[string]string{"event.json": eventJSON},
	).deps()
	d.Check = nil
	d.NewDefaultPort = func() (action.CheckRunPort, error) {
		return nil, errors.New("composition broken")
	}
	res := action.RunMain(d)
	assert.Equal(t, orchestrator.ExitConfig, res.ExitCode)
	assert.Contains(t, *res.Text, "default check-run port composition failed")
}

func TestRunMainNoPortConfiguredConfig(t *testing.T) {
	eventJSON := mustMarshal(sameRepoPayload())
	d := makeCapture(
		map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json"},
		map[string]string{"event.json": eventJSON},
	).deps()
	d.Check = nil
	d.NewDefaultPort = nil
	res := action.RunMain(d)
	assert.Equal(t, orchestrator.ExitConfig, res.ExitCode)
	assert.Contains(t, *res.Text, "no check-run port configured")
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func cloneMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

/* ================================================================ seams */

func TestOSFSAppendFileRoundTrip(t *testing.T) {
	path := t.TempDir() + "/out.txt"
	require.NoError(t, action.OSFS().AppendFile(path, "line one\n"))
	require.NoError(t, action.OSFS().AppendFile(path, "line two\n"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "line one\nline two\n", string(data))
}

func TestOSIOStderrAndSetExitCode(t *testing.T) {
	io := action.OSIO()
	io.Stderr("a summary line")
	io.SetExitCode(3)
}

func TestRunActionRunnerEnvNilAndHeadless(t *testing.T) {
	noopLogger := &action.ActionLogger{Info: func(string) {}, Warn: func(string) {}}
	double := staticDouble(action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport})

	// Env nil -> defaults to empty RunnerEnv
	res := action.RunActionRunner(action.RunActionRunnerDeps{Check: double.port, Env: nil, Payload: sameRepoPayload(), Logger: noopLogger})
	assert.Equal(t, orchestrator.ExitConfig, res.ExitCode)

	// headless event (no PR, no push, no GITHUB_SHA) -> inconclusive, never no-drift
	res = action.RunActionRunner(action.RunActionRunnerDeps{Check: double.port,
		Env: &action.RunnerEnv{GithubEventName: "release"}, Payload: map[string]any{}, Logger: noopLogger})
	assert.Equal(t, orchestrator.ExitRuntime, res.ExitCode)
	assert.True(t, resultInconclusive(res))
}

func resultInconclusive(res action.ActionRunResult) bool { return res.Inconclusive }

func TestOSFSReadFile(t *testing.T) {
	path := t.TempDir() + "/in.txt"
	require.NoError(t, os.WriteFile(path, []byte("hello\n"), 0o644))
	data, err := action.OSFS().ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello\n", data)
}

func TestRunMainDefaultSeams(t *testing.T) {
	// nil IO/FS fall back to OSIO/OSFS defaults; event read from a real temp file
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/event.json", []byte(mustMarshal(sameRepoPayload())), 0o644))
	res := action.RunMain(action.MainDeps{
		Env: map[string]string{
			"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": dir + "/event.json",
		},
		FS: nil,
		IO: nil,
		Check: func(in action.CheckRunPortInput) (action.CheckRunPortResult, error) {
			return action.CheckRunPortResult{Kind: action.CheckRunPortResultRuntimeInconclusive, Reason: "lane"}, nil
		},
	})
	assert.Equal(t, orchestrator.ExitRuntime, res.ExitCode)
}

func captureCheck() action.CheckRunPort {
	return func(in action.CheckRunPortInput) (action.CheckRunPortResult, error) {
		return action.CheckRunPortResult{Kind: action.CheckRunPortResultRuntimeInconclusive, Reason: "lane"}, nil
	}
}

func TestParseActionEventInvalidJsonNumber(t *testing.T) {
	var eventErr *action.ActionEventError
	base := map[string]any{
		"repository": map[string]any{"full_name": "acme/payments"},
		"pull_request": map[string]any{
			"number": json.Number("not-a-number"),
			"head": map[string]any{
				"sha": "abc", "ref": "feature/x",
				"repo": map[string]any{"full_name": "acme/payments"},
			},
			"base": map[string]any{"ref": "main"},
		},
	}
	_, err := action.ParseActionEvent(base, eventEnv())
	require.ErrorAs(t, err, &eventErr)
}

func TestRunActionRunnerHeadFromEnvSha(t *testing.T) {
	noopLogger := &action.ActionLogger{Info: func(string) {}, Warn: func(string) {}}
	var seen *action.CheckRunPortInput
	double := selectingDouble(func(in action.CheckRunPortInput) action.CheckRunPortResult {
		seen = &in
		return action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport}
	})

	// release event with GITHUB_SHA set: head comes from env (not payload)
	result := action.RunActionRunner(action.RunActionRunnerDeps{
		Check:   double.port,
		Env:     &action.RunnerEnv{GithubEventName: "release", GithubSha: "abc123"},
		Payload: map[string]any{},
		Logger:  noopLogger,
	})
	require.NotNil(t, seen)
	assert.Equal(t, "abc123", seen.Head)
	assert.Equal(t, orchestrator.ExitOK, result.ExitCode)
}

func TestRunActionRunnerBaseAndRepoFromEnv(t *testing.T) {
	noopLogger := &action.ActionLogger{Info: func(string) {}, Warn: func(string) {}}
	var seen *action.CheckRunPortInput
	double := selectingDouble(func(in action.CheckRunPortInput) action.CheckRunPortResult {
		seen = &in
		return action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReport}
	})

	result := action.RunActionRunner(action.RunActionRunnerDeps{
		Check: double.port,
		Env: &action.RunnerEnv{
			GithubEventName:  "release",
			GithubSha:        "abc123",
			GithubBaseRef:    "main",
			GithubRepository: "acme/repo",
		},
		Payload: map[string]any{},
		Logger:  noopLogger,
	})
	require.NotNil(t, seen)
	assert.Equal(t, "main", seen.Base, "base falls back to GITHUB_BASE_REF")
	assert.Equal(t, "acme/repo", seen.Repo, "repo falls back to GITHUB_REPOSITORY")
	assert.Equal(t, orchestrator.ExitOK, result.ExitCode)
}
