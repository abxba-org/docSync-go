// Ported from core/src/../../test/coverage-audit.test.ts (P11-T22; ARCH
// §16.1 A5; D14 fail-closed corpus conventions). Fixture-based, pinned `at`
// instant, mkdtemp filesystem, no network.
package coverageaudit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/coverageaudit"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pinnedAt = "2026-10-09T00:00:00Z"

// caseOf builds one audit case input (the TS caseOf helper).
func caseOf(caseID, language string, truth schemas.BehavioralProxyTruth, firedProxies []string) coverageaudit.BehavioralProxyCoverageCaseInput {
	return coverageaudit.BehavioralProxyCoverageCaseInput{
		CaseID:       caseID,
		Language:     language,
		Truth:        truth,
		FiredProxies: firedProxies,
	}
}

func findLanguage(report *schemas.BehavioralProxyCoverageReport, language string) *schemas.AuditLanguageRow {
	for i := range report.Languages {
		if report.Languages[i].Language == language {
			return &report.Languages[i]
		}
	}
	return nil
}

func findCase(report *schemas.BehavioralProxyCoverageReport, caseID string) *schemas.AuditCaseRecord {
	for i := range report.Cases {
		if report.Cases[i].CaseID == caseID {
			return &report.Cases[i]
		}
	}
	return nil
}

func TestRunBehavioralProxyCoverageAudit(t *testing.T) {
	t.Run("computes per-language recall, false-positive rate and the overall summary deterministically", func(t *testing.T) {
		report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 0.5,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				caseOf("hit", "typescript", schemas.ProxyTruthBehavioral, []string{"signature"}),
				caseOf("miss", "typescript", schemas.ProxyTruthBehavioral, []string{}),
				caseOf("fp", "typescript", schemas.ProxyTruthNonBehavioral, []string{"type"}),
				caseOf("clean", "typescript", schemas.ProxyTruthNonBehavioral, []string{}),
				caseOf("config-key", "yaml", schemas.ProxyTruthBehavioral, []string{"config"}),
			},
		})
		require.NoError(t, err)

		assert.Equal(t, "docsync.behavioral-proxy-coverage-report/v1", report.Schema)
		ts := findLanguage(report, "typescript")
		require.NotNil(t, ts)
		assert.Equal(t, 4, ts.AnalyzedCases)
		assert.Equal(t, 2, ts.BehaviorRelevantCases)
		assert.Equal(t, 1, ts.ProxyHits)
		assert.InDelta(t, 0.5, ts.Recall, 1e-12)
		assert.Equal(t, 1, ts.FalsePositives)
		assert.InDelta(t, 0.25, ts.FalsePositiveRate, 1e-12)
		yaml := findLanguage(report, "yaml")
		require.NotNil(t, yaml)
		assert.Equal(t, 1.0, yaml.Recall)
		assert.Equal(t, schemas.AuditSummary{
			Cases:            5,
			BehaviorRelevant: 3,
			ProxyHits:        2,
			FalsePositives:   1,
			DeclaredGaps:     1,
			ReviewRouted:     0,
		}, report.Summary)
		// Shape guarantee: the engine fail-closed validates its own output
		// against docsync.behavioral-proxy-coverage-report/v1 before returning
		// (any shape drift above would have errored instead).
	})

	t.Run("P11-T22: uncertain proxy cases route REVIEW in the report (D14) and are excluded from recall math", func(t *testing.T) {
		report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 0.5,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				caseOf("hit", "typescript", schemas.ProxyTruthBehavioral, []string{"signature"}),
				caseOf("rename", "typescript", schemas.ProxyTruthUncertain, []string{"api-surface"}),
			},
		})
		require.NoError(t, err)

		record := findCase(report, "rename")
		require.NotNil(t, record)
		assert.Equal(t, schemas.AuditOutcomeReview, record.Outcome)
		assert.True(t, record.ReviewRouted)
		ts := findLanguage(report, "typescript")
		require.NotNil(t, ts)
		assert.Equal(t, []string{"rename"}, ts.ReviewRouted)
		// The uncertain case is NOT a behaviorally-relevant denominator entry:
		// recall stays 1/1, and it is neither a hit nor a false positive.
		assert.Equal(t, 1, ts.BehaviorRelevantCases)
		assert.Equal(t, 1, ts.ProxyHits)
		assert.Equal(t, 1.0, ts.Recall)
		assert.Equal(t, 1, report.Summary.ReviewRouted)
	})

	t.Run("P11-T22: recall gaps are DECLARED per language, never silent — a missed behavioral case always appears in declared_gaps", func(t *testing.T) {
		report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 0.5,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				caseOf("hit", "typescript", schemas.ProxyTruthBehavioral, []string{"signature"}),
				caseOf("body-only", "typescript", schemas.ProxyTruthBehavioral, []string{}),
			},
		})
		require.NoError(t, err)
		ts := findLanguage(report, "typescript")
		require.NotNil(t, ts)
		gapIDs := make([]string, 0, len(ts.DeclaredGaps))
		for _, gap := range ts.DeclaredGaps {
			gapIDs = append(gapIDs, gap.CaseID)
		}
		assert.Equal(t, []string{"body-only"}, gapIDs)
		require.Len(t, ts.DeclaredGaps, 1)
		assert.Contains(t, ts.DeclaredGaps[0].Detail, "declared recall gap")
		assert.Equal(t, schemas.AuditVerdictPassWithDeclaredGaps, report.Verdict)
		assert.Contains(t, report.VerdictReason, "declared recall gap")
	})

	t.Run("a language below the caller-supplied recall floor fails the audit even with declared gaps", func(t *testing.T) {
		report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 0.9,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				caseOf("hit", "typescript", schemas.ProxyTruthBehavioral, []string{"signature"}),
				caseOf("miss", "typescript", schemas.ProxyTruthBehavioral, []string{}),
			},
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.AuditVerdictFail, report.Verdict)
		assert.Contains(t, report.VerdictReason, "typescript=0.5")
	})

	t.Run("a language with no behaviorally-relevant cases has vacuous recall 1 and no gaps", func(t *testing.T) {
		report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 1,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				caseOf("comments", "markdown", schemas.ProxyTruthNonBehavioral, []string{}),
			},
		})
		require.NoError(t, err)
		row := findLanguage(report, "markdown")
		require.NotNil(t, row)
		assert.Equal(t, 1, row.AnalyzedCases)
		assert.Equal(t, 0, row.BehaviorRelevantCases)
		assert.Equal(t, 1.0, row.Recall)
		assert.Equal(t, []schemas.AuditDeclaredGap{}, row.DeclaredGaps)
		assert.Equal(t, schemas.AuditVerdictPass, report.Verdict)
	})

	t.Run("is deterministic: identical inputs yield deep-equal reports, rows and cases code-unit sorted", func(t *testing.T) {
		input := coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 0.5,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				caseOf("b-hit", "typescript", schemas.ProxyTruthBehavioral, []string{"visibility", "signature"}),
				caseOf("a-miss", "typescript", schemas.ProxyTruthBehavioral, []string{}),
				caseOf("c-config", "yaml", schemas.ProxyTruthBehavioral, []string{"config"}),
			},
		}
		first, err := coverageaudit.RunBehavioralProxyCoverageAudit(input)
		require.NoError(t, err)
		second, err := coverageaudit.RunBehavioralProxyCoverageAudit(input)
		require.NoError(t, err)
		assert.Equal(t, first, second)
		caseIDs := make([]string, 0, len(first.Cases))
		for _, record := range first.Cases {
			caseIDs = append(caseIDs, record.CaseID)
		}
		assert.Equal(t, []string{"a-miss", "b-hit", "c-config"}, caseIDs)
		languages := make([]string, 0, len(first.Languages))
		for _, row := range first.Languages {
			languages = append(languages, row.Language)
		}
		assert.Equal(t, []string{"typescript", "yaml"}, languages)
		bHit := findCase(first, "b-hit")
		require.NotNil(t, bHit)
		assert.Equal(t, []schemas.ProxyKind{schemas.ProxyKindSignature, schemas.ProxyKindVisibility}, bHit.FiredProxies)
	})

	t.Run("fails closed on an unknown proxy kind or an out-of-range recall floor", func(t *testing.T) {
		_, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 0.5,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				caseOf("x", "typescript", schemas.ProxyTruthBehavioral, []string{"vibes"}),
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fired unknown proxy kind 'vibes'")

		_, err = coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 1.5,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				caseOf("x", "typescript", schemas.ProxyTruthBehavioral, []string{"signature"}),
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "recallFloor must be within [0, 1], got 1.5")
	})

	t.Run("an empty corpus vacuously passes with vacuous per-language recall", func(t *testing.T) {
		report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 0.5,
			Cases:       nil,
		})
		require.NoError(t, err)
		assert.Empty(t, report.Languages)
		assert.Empty(t, report.Cases)
		assert.Equal(t, schemas.AuditVerdictPass, report.Verdict)
		assert.Contains(t, report.VerdictReason, "min 1")
	})

	t.Run("the repository data source and a note are carried onto the report and the declared gap", func(t *testing.T) {
		repository := schemas.AuditDataSourceRepository
		note := "rename heuristic fired late"
		report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
			CorpusID:    "synthetic",
			At:          pinnedAt,
			RecallFloor: 0.5,
			DataSource:  &repository,
			Cases: []coverageaudit.BehavioralProxyCoverageCaseInput{
				{
					CaseID:       "body-only",
					Language:     "typescript",
					Truth:        schemas.ProxyTruthBehavioral,
					FiredProxies: []string{},
					Note:         &note,
				},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, schemas.AuditDataSourceRepository, report.DataSource)
		ts := findLanguage(report, "typescript")
		require.NotNil(t, ts)
		require.Len(t, ts.DeclaredGaps, 1)
		assert.Contains(t, ts.DeclaredGaps[0].Detail, note)
	})
}

func TestLoadCoverageCorpus(t *testing.T) {
	t.Run("loads truth.json plus sources/proxies per case, code-unit sorted", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "ok")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "case-a"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "case-a", "truth.json"),
			[]byte(`{"caseId":"case-a","language":"typescript","truth":"behavioral","note":"n"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "case-a", "before.ts"),
			[]byte("export function f(): void {}\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "case-a", "after.ts"),
			[]byte("export function f(x: number): void {}\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "case-b"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "case-b", "truth.json"),
			[]byte(`{"caseId":"case-b","language":"yaml","truth":"behavioral"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "case-b", "proxies.json"),
			[]byte(`{"firedProxies":["config"]}`), 0o644))

		cases, err := coverageaudit.LoadCoverageCorpus(dir)
		require.NoError(t, err)
		require.Len(t, cases, 2)
		assert.Equal(t, "case-a", cases[0].CaseID)
		assert.Equal(t, "case-b", cases[1].CaseID)
		require.NotNil(t, cases[0].BeforeSource)
		assert.Contains(t, *cases[0].BeforeSource, "export function f()")
		require.NotNil(t, cases[0].AfterSource)
		assert.Contains(t, *cases[0].AfterSource, "x: number")
		require.NotNil(t, cases[0].Note)
		assert.Equal(t, "n", *cases[0].Note)
		assert.Equal(t, []string{"config"}, cases[1].FiredProxies)
		assert.Nil(t, cases[1].Note)
		assert.Nil(t, cases[1].BeforeSource)
	})

	t.Run("fails closed on a missing truth.json, an unknown truth label, a case-id mismatch, or an unknown proxy kind", func(t *testing.T) {
		root := t.TempDir()

		noTruth := filepath.Join(root, "no-truth")
		require.NoError(t, os.MkdirAll(noTruth, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(noTruth, "before.ts"), []byte("x\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(noTruth, "after.ts"), []byte("y\n"), 0o644))
		_, err := coverageaudit.LoadCoverageCorpus(noTruth)
		require.Error(t, err)
		var corpusErr *coverageaudit.CoverageCorpusError
		require.ErrorAs(t, err, &corpusErr)
		assert.Equal(t, coverageaudit.EDOCSYNC_COVERAGE_CORPUS, corpusErr.Code())

		_, err = coverageaudit.LoadCoverageCorpus(filepath.Join(root, "missing-dir"))
		require.Error(t, err)
		require.ErrorAs(t, err, &corpusErr)
		assert.Equal(t, coverageaudit.EDOCSYNC_COVERAGE_CORPUS, corpusErr.Code())

		mismatch := filepath.Join(root, "mismatch")
		require.NoError(t, os.MkdirAll(filepath.Join(mismatch, "real-id"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(mismatch, "real-id", "truth.json"),
			[]byte(`{"caseId":"other-id","language":"typescript","truth":"behavioral"}`), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(mismatch)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "directory/truth mismatch")

		badLabel := filepath.Join(root, "bad-label")
		require.NoError(t, os.MkdirAll(filepath.Join(badLabel, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(badLabel, "case", "truth.json"),
			[]byte(`{"caseId":"case","language":"typescript","truth":"maybe"}`), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(badLabel)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a known truth label")

		badProxy := filepath.Join(root, "bad-proxy")
		require.NoError(t, os.MkdirAll(filepath.Join(badProxy, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(badProxy, "case", "truth.json"),
			[]byte(`{"caseId":"case","language":"yaml","truth":"behavioral"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(badProxy, "case", "proxies.json"),
			[]byte(`{"firedProxies":["star-gaze"]}`), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(badProxy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a known proxy kind")
	})

	t.Run("fails closed on an unpaired before/after pair, invalid JSON, a non-string note and a missing caseId", func(t *testing.T) {
		root := t.TempDir()

		unpaired := filepath.Join(root, "unpaired")
		require.NoError(t, os.MkdirAll(filepath.Join(unpaired, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(unpaired, "case", "truth.json"),
			[]byte(`{"caseId":"case","language":"typescript","truth":"behavioral"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(unpaired, "case", "before.ts"), []byte("x\n"), 0o644))
		_, err := coverageaudit.LoadCoverageCorpus(unpaired)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "without an after.<ext> pair")

		unpairedAfter := filepath.Join(root, "unpaired-after")
		require.NoError(t, os.MkdirAll(filepath.Join(unpairedAfter, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(unpairedAfter, "case", "truth.json"),
			[]byte(`{"caseId":"case","language":"typescript","truth":"behavioral"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(unpairedAfter, "case", "after.ts"), []byte("y\n"), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(unpairedAfter)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "without a before.<ext> pair")

		badJSON := filepath.Join(root, "bad-json")
		require.NoError(t, os.MkdirAll(filepath.Join(badJSON, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(badJSON, "case", "truth.json"), []byte("{not json"), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(badJSON)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "truth.json is not valid JSON")

		notObject := filepath.Join(root, "not-object")
		require.NoError(t, os.MkdirAll(filepath.Join(notObject, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(notObject, "case", "truth.json"), []byte("[]"), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(notObject)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "truth.json is not an object")

		badNote := filepath.Join(root, "bad-note")
		require.NoError(t, os.MkdirAll(filepath.Join(badNote, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(badNote, "case", "truth.json"),
			[]byte(`{"caseId":"case","language":"typescript","truth":"behavioral","note":7}`), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(badNote)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "note is not a string")

		noCaseID := filepath.Join(root, "no-case-id")
		require.NoError(t, os.MkdirAll(filepath.Join(noCaseID, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(noCaseID, "case", "truth.json"),
			[]byte(`{"language":"typescript","truth":"behavioral"}`), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(noCaseID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lacks a nonempty caseId")

		badProxiesJSON := filepath.Join(root, "bad-proxies-json")
		require.NoError(t, os.MkdirAll(filepath.Join(badProxiesJSON, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(badProxiesJSON, "case", "truth.json"),
			[]byte(`{"caseId":"case","language":"yaml","truth":"behavioral"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(badProxiesJSON, "case", "proxies.json"),
			[]byte(`{"firedProxies":"config"}`), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(badProxiesJSON)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lacks a firedProxies string array")

		badProxyKind := filepath.Join(root, "bad-proxy-kind")
		require.NoError(t, os.MkdirAll(filepath.Join(badProxyKind, "case"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(badProxyKind, "case", "truth.json"),
			[]byte(`{"caseId":"case","language":"yaml","truth":"uncertain"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(badProxyKind, "case", "proxies.json"),
			[]byte(`{"firedProxies":["config",42]}`), 0o644))
		_, err = coverageaudit.LoadCoverageCorpus(badProxyKind)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lacks a firedProxies string array")
	})
}

// TestLoadedCorpusFeedsTheAudit wires the two surfaces the TS index barrel
// exported together: a loaded corpus feeds the audit engine unchanged.
func TestLoadedCorpusFeedsTheAudit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "corpus")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "case-a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "case-a", "truth.json"),
		[]byte(`{"caseId":"case-a","language":"typescript","truth":"behavioral"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "case-a", "before.ts"), []byte("export function f(): void {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "case-a", "after.ts"), []byte("export function f(x: number): void {}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "case-b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "case-b", "truth.json"),
		[]byte(`{"caseId":"case-b","language":"yaml","truth":"behavioral","note":"config key"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "case-b", "proxies.json"),
		[]byte(`{"firedProxies":["config"]}`), 0o644))

	cases, err := coverageaudit.LoadCoverageCorpus(dir)
	require.NoError(t, err)
	require.Len(t, cases, 2)

	inputs := make([]coverageaudit.BehavioralProxyCoverageCaseInput, 0, len(cases))
	for _, c := range cases {
		input := coverageaudit.BehavioralProxyCoverageCaseInput{
			CaseID:       c.CaseID,
			Language:     c.Language,
			Truth:        c.Truth,
			FiredProxies: c.FiredProxies,
			Note:         c.Note,
		}
		inputs = append(inputs, input)
	}
	report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
		CorpusID:    dir,
		At:          pinnedAt,
		RecallFloor: 0.5,
		Cases:       inputs,
	})
	require.NoError(t, err)
	// case-a is behavioral with NO precomputed proxies (its measurement is the
	// analyzer study runner's job), so the typescript row declares its gap and
	// the corpus fails the 0.5 floor; case-b (config proxy) is a hit.
	assert.Equal(t, schemas.AuditVerdictFail, report.Verdict)
	assert.Equal(t, 2, report.Summary.Cases)
	assert.Equal(t, 1, report.Summary.ProxyHits)
	assert.Equal(t, 1, report.Summary.DeclaredGaps)

	// The report round-trips its own schema bytes (fail-closed self-check).
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	parsed, err := schemas.ParseBehavioralProxyCoverageReport(raw)
	require.NoError(t, err)
	assert.Equal(t, report.CorpusID, parsed.CorpusID)
}
