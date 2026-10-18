package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/action"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func cleanCanonicalReport() *schemas.CanonicalReport {
	generatedAt := "2026-10-07T00:00:00Z"
	return &schemas.CanonicalReport{
		RunID:           "run_action_coverage",
		Key:             schemas.RunKey{Repo: "acme/payments", BaseSHA: "8a73f2c", HeadSHA: "81af92e", ManifestVersion: 1, PolicyVersion: 17},
		Status:          schemas.RunStatusCompleted,
		GeneratedAt:     &generatedAt,
		PolicyVersion:   17,
		ModelBindings:   []schemas.RunModelBinding{},
		Rows:            []schemas.CanonicalReportRow{{DocPath: "docs/a.md", Glyph: schemas.ReportGlyphOk, Outcome: schemas.PipelineOutcomeIgnore, Confidence: ptrFloat64(0.5), Evidence: []string{}}},
		Abstains:        []schemas.RunAbstain{},
		Findings:        []schemas.RunFinding{},
		Degradations:    []schemas.Degradation{},
		UnknownCoverage: []string{},
	}
}

func ptrFloat64(v float64) *float64 { return &v }

func prEventPayload() string {
	return `{"repository":{"full_name":"acme/payments"},"pull_request":{"number":7,"head":{"sha":"abc123","ref":"feature/x","repo":{"full_name":"acme/payments"}},"base":{"ref":"main"}}}`
}

func writeEventFile(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "event.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o640))
	return path
}

func TestCOV_CMD_ACTION_Run(t *testing.T) {
	t.Run("no port wired → fail-closed configuration error (exit 2)", func(t *testing.T) {
		dir := t.TempDir()
		eventPath := writeEventFile(t, dir, prEventPayload())
		code := run(action.MainDeps{
			Env: map[string]string{
				"GITHUB_EVENT_NAME": "pull_request",
				"GITHUB_EVENT_PATH": eventPath,
			},
			FS: &action.MainFS{
				ReadFile:   func(p string) (string, error) { return prEventPayload(), nil },
				AppendFile: func(string, string) error { return nil },
			},
			IO: &action.MainIO{
				Stderr:      func(string) {},
				SetExitCode: func(int) {},
			},
		})
		assert.Equal(t, 2, code)
	})

	t.Run("runtime-inconclusive port → exit 3", func(t *testing.T) {
		dir := t.TempDir()
		writeEventFile(t, dir, prEventPayload())
		code := run(action.MainDeps{
			Env: map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": filepath.Join(dir, "event.json")},
			Check: func(action.CheckRunPortInput) (action.CheckRunPortResult, error) {
				return action.CheckRunPortResult{Kind: action.CheckRunPortResultRuntimeInconclusive, Reason: "outage"}, nil
			},
			FS: &action.MainFS{
				ReadFile:   func(p string) (string, error) { return prEventPayload(), nil },
				AppendFile: func(string, string) error { return nil },
			},
			IO: &action.MainIO{Stderr: func(string) {}, SetExitCode: func(int) {}},
		})
		assert.Equal(t, 3, code)
	})

	t.Run("clean report port → exit 0", func(t *testing.T) {
		dir := t.TempDir()
		writeEventFile(t, dir, prEventPayload())
		code := run(action.MainDeps{
			Env: map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": filepath.Join(dir, "event.json")},
			Check: func(action.CheckRunPortInput) (action.CheckRunPortResult, error) {
				return action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanCanonicalReport()}, nil
			},
			FS: &action.MainFS{
				ReadFile:   func(p string) (string, error) { return prEventPayload(), nil },
				AppendFile: func(string, string) error { return nil },
			},
			IO: &action.MainIO{Stderr: func(string) {}, SetExitCode: func(int) {}},
		})
		assert.Equal(t, 0, code)
	})
}

func TestCOV_CMD_ACTION_MainSubprocess(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "docsync-action")
	require.NoError(t, os.MkdirAll(filepath.Dir(bin), 0o755))
	cmd := exec.Command("go", "build", "-o", bin, "github.com/DUDE616/docSync-go/cmd/docsync-action")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", string(out))

	env := append(os.Environ(), "GITHUB_EVENT_NAME=pull_request", "GITHUB_EVENT_PATH=/nonexistent/event.json")

	t.Run("main exits 2 when the event path is missing (fail-closed)", func(t *testing.T) {
		c := exec.Command(bin)
		c.Env = env
		c.Dir = t.TempDir()
		err := c.Run()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 2, exitErr.ExitCode())
	})

	t.Run("main exits 2 with a valid event but no port wired (fail-closed)", func(t *testing.T) {
		dir := t.TempDir()
		eventPath := writeEventFile(t, dir, `{"action":"opened","pull_request":{}}`)
		e2 := append(env, "GITHUB_EVENT_PATH="+eventPath)
		c := exec.Command(bin)
		c.Env = e2
		c.Dir = dir
		err := c.Run()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 2, exitErr.ExitCode())
	})

	t.Run("main writes a non-zero exit code to stderr", func(t *testing.T) {
		c := exec.Command(bin)
		c.Env = env
		c.Dir = t.TempDir()
		errBuf := &strings.Builder{}
		c.Stderr = errBuf
		err := c.Run()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 2, exitErr.ExitCode())
		assert.Contains(t, errBuf.String(), "Configuration error")
	})
}
