package main

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/action"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sameRepoPayloadCmd() map[string]any {
	return map[string]any{
		"repository": map[string]any{"full_name": "acme/payments"},
		"pull_request": map[string]any{
			"number": 7,
			"head": map[string]any{"sha": "abc123", "ref": "feature/x",
				"repo": map[string]any{"full_name": "acme/payments"}},
			"base": map[string]any{"ref": "main"},
		},
	}
}

func cleanReportCmd() *schemas.CanonicalReport {
	generatedAt := "2026-10-07T00:00:00Z"
	confidence := 0.5
	return &schemas.CanonicalReport{
		RunID:         "run_test",
		Key:           schemas.RunKey{Repo: "acme/payments", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1},
		Status:        schemas.RunStatusCompleted,
		GeneratedAt:   &generatedAt,
		PolicyVersion: 1,
		Rows: []schemas.CanonicalReportRow{{
			DocPath: "docs/a.md", Glyph: schemas.ReportGlyphOk, Outcome: schemas.PipelineOutcomeIgnore,
			Confidence: &confidence,
		}},
	}
}

func eventJSONCmd() string {
	b, err := json.Marshal(sameRepoPayloadCmd())
	if err != nil {
		panic(err)
	}
	return string(b)
}

var _ = require.NoError

func TestRunActionExitCodes(t *testing.T) {
	eventJSON := eventJSONCmd()

	t.Run("no port configured -> config error exit 2", func(t *testing.T) {
		assert.Equal(t, 2, run(action.MainDeps{}))
	})

	t.Run("default port composition failure -> config error exit 2", func(t *testing.T) {
		code := run(action.MainDeps{
			NewDefaultPort: func() (action.CheckRunPort, error) {
				return nil, assert.AnError
			},
		})
		assert.Equal(t, 2, code)
	})

	t.Run("port runtime inconclusive -> exit 3", func(t *testing.T) {
		code := run(action.MainDeps{
			Check: func(in action.CheckRunPortInput) (action.CheckRunPortResult, error) {
				return action.CheckRunPortResult{Kind: action.CheckRunPortResultRuntimeInconclusive, Reason: "outage"}, nil
			},
			FS: &action.MainFS{ReadFile: func(string) (string, error) { return eventJSON, nil }},
			IO: &action.MainIO{Stderr: func(string) {}, SetExitCode: func(int) {}},
			Env: map[string]string{
				"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json",
			},
		})
		assert.Equal(t, 3, code)
	})

	t.Run("port clean report -> exit 0", func(t *testing.T) {
		code := run(action.MainDeps{
			Check: func(in action.CheckRunPortInput) (action.CheckRunPortResult, error) {
				return action.CheckRunPortResult{Kind: action.CheckRunPortResultReport, Report: cleanReportCmd()}, nil
			},
			FS: &action.MainFS{ReadFile: func(string) (string, error) { return eventJSON, nil }},
			IO: &action.MainIO{Stderr: func(string) {}, SetExitCode: func(int) {}},
			Env: map[string]string{
				"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": "event.json",
			},
		})
		assert.Equal(t, 0, code)
	})
}

func TestMainPropagatesExitCode(t *testing.T) {
	var captured int
	previous := osExit
	osExit = func(code int) { captured = code }
	t.Cleanup(func() { osExit = previous })

	main()
	assert.Equal(t, 2, captured, "no port configured -> main exits 2")
}
