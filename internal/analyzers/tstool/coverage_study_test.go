// Ported from packages/analyzer-typescript/test/coverage-study.test.ts
// (P11-T22; ARCH §16.1 A5; V-P11-11) — the A5 behavioral-proxy coverage STUDY
// RUNNER over the real (lightweight-parser) analyzer, with the RECORDED verdict
// pinned exactly as the TS suite did.
//
// Dependency direction (recorded): `@docsync/analyzer-typescript` depends on
// `@docsync/core`, never the reverse — so core's audit engine
// (runBehavioralProxyCoverageAudit) takes proxy measurements as an injectable
// narrow port, and the REAL-analyzer study runner lives HERE: it loads the
// labeled fixture corpus through core's loadCoverageCorpus convention
// (`<corpus>/case/truth.json` + `before./after.` sources or precomputed
// `proxies.json`), measures each TypeScript case through the real analyzer
// (MeasureBehavioralProxies), and computes the report.
//
// Ground truth is DETERMINISTIC: every case directory DECLARES its truth label
// in `truth.json` (no model judgment). The verdict provenance follows the A4
// trial convention: `data_source: 'fixture'` — a pilot study, recorded in the
// pinned a5VerdictRecord below and asserted to match the computed report (the
// verdict is RECORDED, not recomputed ad hoc). `docs/evaluation/` does not exist
// in this repository (Phase 10 shipped `docs/evaluation.md` instead), so this
// test is the verdict record of note. The report shape is schema-guaranteed: the
// audit engine fail-closed parses its own output against
// `docsync.behavioral-proxy-coverage-report/v1` before returning.
package tstool_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
	"github.com/DUDE616/docSync-go/internal/coverageaudit"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// corpusDir is the ported fixture corpus (byte-identical to the TS fixture).
var corpusDir = filepath.Join("testdata", "a5-corpus")

const (
	a5PinnedAt    = "2026-10-09T00:00:00Z"
	a5RecallFloor = 0.5
)

// a5VerdictRecord is the RECORDED A5 verdict over this corpus (data_source
// 'fixture' — pilot evidence, per the A4 trial convention; a repository-scale
// corpus re-run is required before treating the verdict as repository evidence).
var a5VerdictRecord = struct {
	Gate                      string
	Item                      string
	SchemaID                  string
	Corpus                    string
	At                        string
	DataSource                schemas.AuditDataSource
	RecallFloor               float64
	Verdict                   schemas.AuditVerdict
	TypeScriptAnalyzedCases   int
	TypeScriptBehavioralCases int
	TypeScriptProxyHits       int
	TypeScriptRecall          float64
	YamlAnalyzedCases         int
	YamlBehavioralCases       int
	YamlProxyHits             int
	YamlRecall                float64
	DeclaredGaps              []string
	ReviewRouted              []string
}{
	Gate:                      "A5",
	Item:                      "P11-T22",
	SchemaID:                  "docsync.behavioral-proxy-coverage-report/v1",
	Corpus:                    "packages/analyzer-typescript/test/fixtures/a5-corpus",
	At:                        a5PinnedAt,
	DataSource:                schemas.AuditDataSourceFixture,
	RecallFloor:               a5RecallFloor,
	Verdict:                   "pass-with-declared-gaps",
	TypeScriptAnalyzedCases:   7,
	TypeScriptBehavioralCases: 5,
	TypeScriptProxyHits:       4,
	TypeScriptRecall:          0.8,
	YamlAnalyzedCases:         1,
	YamlBehavioralCases:       1,
	YamlProxyHits:             1,
	YamlRecall:                1,
	DeclaredGaps:              []string{"body-only-change"},
	ReviewRouted:              []string{"rename-exported"},
}

// measureCase measures one corpus case: live TS measurement, or the precomputed
// proxies record (fail closed on neither, exactly as the TS runner).
func measureCase(entry coverageaudit.CoverageCorpusCase) (coverageaudit.BehavioralProxyCoverageCaseInput, error) {
	if entry.BeforeSource != nil && entry.AfterSource != nil {
		measurement, err := tstool.MeasureBehavioralProxies(*entry.BeforeSource, *entry.AfterSource)
		if err != nil {
			return coverageaudit.BehavioralProxyCoverageCaseInput{}, err
		}
		return coverageaudit.BehavioralProxyCoverageCaseInput{
			CaseID:       entry.CaseID,
			Language:     entry.Language,
			Truth:        entry.Truth,
			FiredProxies: measurement.FiredProxies,
			Note:         entry.Note,
		}, nil
	}
	// Precomputed-measurement path (non-AST languages, e.g. config cases).
	if entry.FiredProxies != nil {
		return coverageaudit.BehavioralProxyCoverageCaseInput{
			CaseID:       entry.CaseID,
			Language:     entry.Language,
			Truth:        entry.Truth,
			FiredProxies: entry.FiredProxies,
			Note:         entry.Note,
		}, nil
	}
	return coverageaudit.BehavioralProxyCoverageCaseInput{},
		&coverageCorpusCaseError{caseID: entry.CaseID}
}

// coverageCorpusCaseError is the fail-closed error of the study runner.
type coverageCorpusCaseError struct{ caseID string }

func (e *coverageCorpusCaseError) Error() string {
	return "coverage study: case '" + e.caseID + "' carries neither a before/after source pair nor a precomputed proxies record (fail closed, D14)"
}

func runStudy(t *testing.T) *schemas.BehavioralProxyCoverageReport {
	t.Helper()
	corpus, err := coverageaudit.LoadCoverageCorpus(corpusDir)
	require.NoError(t, err)
	cases := make([]coverageaudit.BehavioralProxyCoverageCaseInput, 0, len(corpus))
	for _, entry := range corpus {
		measured, err := measureCase(entry)
		require.NoError(t, err)
		cases = append(cases, measured)
	}
	report, err := coverageaudit.RunBehavioralProxyCoverageAudit(coverageaudit.BehavioralProxyCoverageAuditInput{
		CorpusID:    "packages/analyzer-typescript/test/fixtures/a5-corpus",
		At:          a5PinnedAt,
		RecallFloor: a5RecallFloor,
		DataSource:  &a5VerdictRecord.DataSource,
		Cases:       cases,
	})
	require.NoError(t, err)
	return report
}

func languageRow(t *testing.T, report *schemas.BehavioralProxyCoverageReport, language string) schemas.AuditLanguageRow {
	t.Helper()
	for _, row := range report.Languages {
		if row.Language == language {
			return row
		}
	}
	require.Failf(t, "language row missing", "expected a row for language '%s'", language)
	return schemas.AuditLanguageRow{}
}

func caseRecord(t *testing.T, report *schemas.BehavioralProxyCoverageReport, caseID string) schemas.AuditCaseRecord {
	t.Helper()
	for _, entry := range report.Cases {
		if entry.CaseID == caseID {
			return entry
		}
	}
	require.Failf(t, "case record missing", "expected a record for case '%s'", caseID)
	return schemas.AuditCaseRecord{}
}

// TestV_P11_11_A5CoverageReportPortsTheRecordedVerdict is the port of the A5
// coverage-report test: per-language rows over the real corpus, matching the
// RECORDED verdict exactly.
func TestV_P11_11_A5CoverageReportPortsTheRecordedVerdict(t *testing.T) {
	report := runStudy(t)

	// The artifact exists under the registered schema id (the engine has already
	// fail-closed parsed it against its schema).
	assert.Equal(t, a5VerdictRecord.SchemaID, report.Schema)

	// Per-language AST coverage numbers (the A5 gate's "per-language rows").
	typescript := languageRow(t, report, "typescript")
	assert.Equal(t, a5VerdictRecord.TypeScriptAnalyzedCases, typescript.AnalyzedCases)
	assert.Equal(t, a5VerdictRecord.TypeScriptBehavioralCases, typescript.BehaviorRelevantCases)
	assert.Equal(t, a5VerdictRecord.TypeScriptProxyHits, typescript.ProxyHits)
	assert.Equal(t, a5VerdictRecord.TypeScriptRecall, typescript.Recall)
	yaml := languageRow(t, report, "yaml")
	assert.Equal(t, a5VerdictRecord.YamlAnalyzedCases, yaml.AnalyzedCases)
	assert.Equal(t, a5VerdictRecord.YamlRecall, yaml.Recall)

	// The verdict is RECORDED (A4-trial convention): the computed report must
	// match the pinned record exactly — verdict, data source, gaps, routing.
	assert.Equal(t, a5VerdictRecord.Verdict, report.Verdict)
	assert.Equal(t, a5VerdictRecord.DataSource, report.DataSource)
	assert.Equal(t, a5VerdictRecord.At, report.At)
	assert.Equal(t, a5VerdictRecord.Corpus, report.CorpusID)
	assert.Equal(t, a5VerdictRecord.RecallFloor, report.RecallFloor)

	gapCaseIDs := make([]string, 0, len(typescript.DeclaredGaps))
	for _, gap := range typescript.DeclaredGaps {
		gapCaseIDs = append(gapCaseIDs, gap.CaseID)
	}
	assert.Equal(t, a5VerdictRecord.DeclaredGaps, gapCaseIDs)
	assert.Equal(t, a5VerdictRecord.ReviewRouted, typescript.ReviewRouted)
}

// TestV_P11_11_RecallGapsDeclaredNeverSilent is the port of the declared-gap
// test: the missed behavioral case appears in declaredGaps with its detail and
// the verdict is pass-with-declared-gaps.
func TestV_P11_11_RecallGapsDeclaredNeverSilent(t *testing.T) {
	report := runStudy(t)
	typescript := languageRow(t, report, "typescript")

	var gap *schemas.AuditDeclaredGap
	for i := range typescript.DeclaredGaps {
		if typescript.DeclaredGaps[i].CaseID == "body-only-change" {
			gap = &typescript.DeclaredGaps[i]
			break
		}
	}
	require.NotNil(t, gap, "expected a declared gap for 'body-only-change'")
	assert.Contains(t, gap.Detail, "declared recall gap")
	assert.Equal(t, 1, report.Summary.DeclaredGaps)
	assert.Equal(t, "pass-with-declared-gaps", string(report.Verdict))
	assert.Contains(t, report.VerdictReason, "declared recall gap")

	// Cross-check the measurement itself: the case fires nothing.
	bodyOnly := caseRecord(t, report, "body-only-change")
	assert.Equal(t, schemas.AuditOutcomeProxyMiss, bodyOnly.Outcome)
	assert.Empty(t, bodyOnly.FiredProxies)
}

// TestV_P11_11_UncertainProxyCasesRouteReview is the port of the REVIEW-routing
// test (D14: ambiguity degrades toward less automation, never more).
func TestV_P11_11_UncertainProxyCasesRouteReview(t *testing.T) {
	report := runStudy(t)
	record := caseRecord(t, report, "rename-exported")
	assert.Equal(t, schemas.ProxyTruthUncertain, record.Truth)
	assert.Equal(t, schemas.AuditOutcomeReview, record.Outcome)
	assert.True(t, record.ReviewRouted)
	assert.Contains(t, languageRow(t, report, "typescript").ReviewRouted, "rename-exported")
	assert.Equal(t, 1, report.Summary.ReviewRouted)
}

// TestV_P11_11_StudyDeterministic is the port of the determinism test.
func TestV_P11_11_StudyDeterministic(t *testing.T) {
	first := runStudy(t)
	second := runStudy(t)
	assert.Equal(t, first, second)
}
