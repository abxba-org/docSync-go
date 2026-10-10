package verifier_test

// Black-box port of packages/verifier/test/replay.test.ts, the
// createAuditReplay/renderAuditTrail half (Phase 9, P9-T20/P9-T21;
// V-P9-21): the tool reconstructs the trail from a committed state ALONE,
// and absent evidence is a declared gap, never fabricated.
//
// The bin/CLI half of the TS file (runAuditReplayCli exit codes) and the
// barrel re-export assertion belong to the CLI unit's ported files; the
// replay-level refusals they cover (bad input ⇒ config error) are pinned
// here against AuditInputError directly.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	auditRunID = "01JAUDITRUN0000000000"
	auditNow   = "2026-10-08T12:00:00.000Z"
)

// seedState ports the TS seedState: a committed `.docsync` state dir holding
// a fix-run record, a provenance record, a doc-state verification ledger and
// one safety incident — the entire evidence surface.
func seedState(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runs := filepath.Join(root, "state", "runs")
	docs := filepath.Join(root, "state", "docs")
	require.NoError(t, os.MkdirAll(runs, 0o755))
	require.NoError(t, os.MkdirAll(docs, 0o755))
	writeJSON(t, filepath.Join(runs, auditRunID+".json"), map[string]any{
		"schema": "docsync.fix-run-record/v1",
		"run_id": auditRunID,
		"at":     auditNow,
		"status": "applied",
		"docs":   []string{"docs/payments.md"},
	})
	writeJSON(t, filepath.Join(runs, "prov-"+auditRunID+".json"), map[string]any{
		"schema":        "docsync.provenance/v1",
		"run_id":        auditRunID,
		"source_commit": strings.Repeat("a", 40),
		"detector":      map[string]any{"model": "z-ai/glm-4.7", "confidence": 0.96},
		"writer":        map[string]any{"model": "anthropic/claude-sonnet-4.5"},
		"verification":  map[string]any{"status": "pass"},
		"generated_at":  auditNow,
	})
	writeJSON(t, filepath.Join(docs, "docs__payments.md.json"), map[string]any{
		"schema": "docsync.doc-state/v1",
		"path":   "docs/payments.md",
		"verifications": []any{
			map[string]any{
				"report_hash": strings.Repeat("b", 64),
				"report": map[string]any{
					"doc_path": "docs/payments.md",
					"run_id":   auditRunID,
					"verdict":  "pass",
				},
				"at":     auditNow,
				"run_id": auditRunID,
			},
		},
	})
	// One incident per line; the file is the only incidents evidence.
	incident := mustJSON(t, map[string]any{
		"schema":         "docsync.safety-incident/v1",
		"incident_id":    "inc-" + strings.Repeat("c", 16),
		"at":             auditNow,
		"run_id":         auditRunID,
		"rule":           "injection_contained",
		"offending_path": "docs/payments.md",
		"detail":         "directive detected and contained",
	})
	require.NoError(t, os.WriteFile(filepath.Join(root, "state", "incidents.jsonl"),
		[]byte(incident+"\n"), 0o644))
	return root
}

func TestV_P9_21_ReconstructsDecisionProvenanceVerificationsIncidents(t *testing.T) {
	root := seedState(t)
	trail, err := verifier.NewAuditReplay(verifier.AuditReplayOptions{StateDir: root}).
		ReplayRun(verifier.AuditReplayInput{RunID: auditRunID})
	require.NoError(t, err)

	assert.Equal(t, auditRunID, trail.RunID)
	require.NotNil(t, trail.Decision)
	assert.Equal(t, "docsync.fix-run-record/v1", decodeObject(t, trail.Decision)["schema"])
	require.NotNil(t, trail.Provenance)
	assert.Equal(t, "docsync.provenance/v1", decodeObject(t, trail.Provenance)["schema"])
	require.Len(t, trail.Verifications, 1)
	assert.Equal(t, "docs/payments.md", trail.Verifications[0].DocPath)
	assert.Equal(t, auditNow, trail.Verifications[0].At)
	require.Len(t, trail.Incidents, 1)
	assert.Equal(t, "injection_contained", trail.Incidents[0].Rule)
	assert.Equal(t, "docs/payments.md", trail.Incidents[0].OffendingPath)
	assert.Equal(t, "directive detected and contained", trail.Incidents[0].Detail)
	assert.Equal(t, "inc-"+strings.Repeat("c", 16), trail.Incidents[0].IncidentID)
	assert.Empty(t, trail.Gaps)

	rendered := verifier.RenderAuditTrail(trail)
	assert.Contains(t, rendered, "# docsync audit trail — run "+auditRunID)
	assert.Contains(t, rendered, "## Provenance")
	assert.Contains(t, rendered, "## Declared gaps")
	// The decision/provenance blocks render the committed bytes verbatim.
	assert.Contains(t, rendered, `"schema": "docsync.fix-run-record/v1"`)
	assert.Contains(t, rendered, `"schema": "docsync.provenance/v1"`)
	// The per-verdict line renders the embedded report compactly.
	assert.Contains(t, rendered, `- docs/payments.md @ `+auditNow+
		`: {"doc_path":"docs/payments.md","run_id":"`+auditRunID+`","verdict":"pass"}`)
	assert.Contains(t, rendered, "- inc-"+strings.Repeat("c", 16)+" "+auditNow+
		" injection_contained docs/payments.md: directive detected and contained")
}

func TestV_P9_21_AbsentEvidenceIsADeclaredGap(t *testing.T) {
	// A committed state dir with no runs/, docs/ or incidents.jsonl at all.
	root := t.TempDir()
	trail, err := verifier.NewAuditReplay(verifier.AuditReplayOptions{StateDir: root}).
		ReplayRun(verifier.AuditReplayInput{RunID: auditRunID})
	require.NoError(t, err)

	assert.Nil(t, trail.Decision)
	assert.Nil(t, trail.Provenance)
	assert.Empty(t, trail.Verifications)
	assert.Empty(t, trail.Incidents)
	assert.GreaterOrEqual(t, len(trail.Gaps), 3)
	assert.Contains(t, strings.Join(trail.Gaps, "\n"),
		"no fix-run (decision) record for run "+auditRunID+" under state/runs")
	assert.Contains(t, strings.Join(trail.Gaps, "\n"), "no provenance record")
	assert.Contains(t, strings.Join(trail.Gaps, "\n"),
		"no verification ledger entries for run "+auditRunID)
	assert.Contains(t, verifier.RenderAuditTrail(trail), "- no fix-run (decision) record")
}

func TestV_P9_21_ReplayRunRefusesBadInput(t *testing.T) {
	replay := verifier.NewAuditReplay(verifier.AuditReplayOptions{StateDir: t.TempDir()})

	// The bin maps AuditInputError to exit 2 (config error), never 3.
	_, err := replay.ReplayRun(verifier.AuditReplayInput{})
	require.Error(t, err)
	var inputErr *verifier.AuditInputError
	require.True(t, errors.As(err, &inputErr), "want *verifier.AuditInputError, got %T", err)
	assert.Equal(t, "run id is required (--run <id>)", err.Error())
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
	assert.Equal(t, docutil.CodeConfig, inputErr.Code())
}

func TestV_P9_21_ReplayRunScopesToTheRequestedDocPath(t *testing.T) {
	root := seedState(t)
	replay := verifier.NewAuditReplay(verifier.AuditReplayOptions{StateDir: root})
	docPath := "docs/payments.md"

	trail, err := replay.ReplayRun(verifier.AuditReplayInput{RunID: auditRunID, DocPath: &docPath})
	require.NoError(t, err)
	require.Len(t, trail.Verifications, 1)
	assert.Equal(t, docPath, trail.Verifications[0].DocPath)
	assert.Empty(t, trail.Gaps)

	other := "docs/missing.md"
	trail, err = replay.ReplayRun(verifier.AuditReplayInput{RunID: auditRunID, DocPath: &other})
	require.NoError(t, err)
	assert.Empty(t, trail.Verifications)
	assert.Contains(t, strings.Join(trail.Gaps, "\n"),
		"no verification ledger entries for run "+auditRunID)
}

// An undecodable state/docs stem is not a doc-state file: it is still read
// (evidence is never skipped silently), and the entry's own run id stands in
// for the unknown doc path (the TS docPath ?? String(run_id) fallback).
func TestV_P9_21_UndecodableDocStemFallsBackToTheEntryRunID(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "state", "docs")
	require.NoError(t, os.MkdirAll(docs, 0o755))
	writeJSON(t, filepath.Join(docs, "a_x.json"), map[string]any{
		"verifications": []any{
			map[string]any{
				"report_hash": strings.Repeat("b", 64),
				"report":      map[string]any{"verdict": "pass"},
				"at":          auditNow,
				"run_id":      auditRunID,
			},
		},
	})
	trail, err := verifier.NewAuditReplay(verifier.AuditReplayOptions{StateDir: root}).
		ReplayRun(verifier.AuditReplayInput{RunID: auditRunID})
	require.NoError(t, err)
	require.Len(t, trail.Verifications, 1)
	assert.Equal(t, auditRunID, trail.Verifications[0].DocPath)
	assert.Contains(t, verifier.RenderAuditTrail(trail), "- "+auditRunID+" @ "+auditNow+": {\"verdict\":\"pass\"}")
}

func TestV_P9_21_ReplayRunFailsLoudPastTheScanCap(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "state", "runs")
	require.NoError(t, os.MkdirAll(runs, 0o755))
	for i := 0; i <= verifier.AuditScanCap; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(runs, "entry-"+strings.Repeat("0", 6-len(itoa(i)))+itoa(i)+".json"),
			[]byte("{}"), 0o644))
	}
	replay := verifier.NewAuditReplay(verifier.AuditReplayOptions{StateDir: root})

	_, err := replay.ReplayRun(verifier.AuditReplayInput{RunID: auditRunID})
	require.Error(t, err)
	var inputErr *verifier.AuditInputError
	require.True(t, errors.As(err, &inputErr), "want *verifier.AuditInputError, got %T", err)
	assert.Contains(t, err.Error(), "scan cap exceeded: ")
	assert.Contains(t, err.Error(), "files under "+filepath.Join(root, "state", "runs"))
}

/* --------------------------------------------------------------- helpers */

// writeJSON writes value as JSON bytes (the TS writeFile(JSON.stringify)).
func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(mustJSON(t, value)), 0o644))
}

// mustJSON marshals value or fails the test.
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

// decodeObject decodes a verbatim evidence record for assertions (the TS
// `(trail.decision as Record<string, unknown>)['schema']`).
func decodeObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}

// itoa renders a non-negative int (the scan-cap filename padding helper).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
