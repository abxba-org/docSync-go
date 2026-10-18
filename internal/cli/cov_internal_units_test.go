package cli

// Coverage-completion suite (assignment: internal/cli statement coverage 68.7%
// → >= 95%). This file is an INTERNAL test (package cli) so the unexported
// helpers the verb surfaces compose can be exercised directly; the exported
// behaviour lanes live in cov_cli_ext_test.go (package cli_test) and the
// flow-runtime lanes in cov_internal_flow_test.go.
//
// Every test asserts an OBSERVABLE outcome (returned error/exit code/rendered
// line/value) — no implementation poking, no network, no wall clock.

import (
	"context"
	"encoding/json"
	"errors"

	"fmt"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/validate"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* covRecorder is a deterministic in-memory CliIo. */
type covRecorder struct {
	stdout []string
	stderr []string
}

func (c *covRecorder) Stdout(line string) { c.stdout = append(c.stdout, line) }
func (c *covRecorder) Stderr(line string) { c.stderr = append(c.stderr, line) }

func (c *covRecorder) out() string { return strings.Join(c.stdout, "\n") }
func (c *covRecorder) err() string { return strings.Join(c.stderr, "\n") }

func covStrPtr(s string) *string { return &s }

func covIntPtr(n int) *int { return &n }

func covFloatPtr(f float64) *float64 { return &f }

/* ------------------------------------------------------------- test fixtures */

// covBootstrapPolicy compiles the documented bootstrap policy v1.
func covBootstrapPolicy(t *testing.T) *policy.CompiledPolicyWorkflow {
	t.Helper()
	compiled, err := policy.CompileBootstrapPolicyV1()
	require.NoError(t, err)
	require.NotNil(t, compiled)
	return compiled
}

// covModelsConfig parses a complete Phase-0 role binding set.
func covModelsConfig(t *testing.T) *openrouter.ExtendedModelsConfig {
	t.Helper()
	parsed, err := loadModelsConfigYAML(`
schema: docsync.models/v1
version: 1
roles:
  decision-engine:
    model: openai/gpt-4o-mini
  judge:
    model: openai/gpt-4o-mini
  writer:
    model: openai/gpt-4o-mini
`, "cov models.yml")
	require.NoError(t, err)
	require.NotNil(t, parsed)
	return parsed
}

// covValidCanonicalReport builds a schema-valid canonical report (the same
// artifact the fix flow renders).
func covValidCanonicalReport(t *testing.T) *schemas.CanonicalReport {
	t.Helper()
	confidence := 0.95
	outcome := &schemas.RunOutcome{
		Schema: schemas.RunOutcomeSchemaID,
		RunID:  "cov-run#a..b",
		Key: schemas.RunKey{
			Repo: "local/repo", BaseSHA: "a", HeadSHA: "b", ManifestVersion: 1, PolicyVersion: 1,
		},
		Status: schemas.RunStatusCompleted,
		Stages: []schemas.PipelineStage{{Stage: "analyze", Status: schemas.RunStageOk}},
		Documents: []schemas.RunDocument{{
			DocPath:       "docs/api.md",
			PolicyOutcome: schemas.PipelineOutcomeSuggest,
			Confidence:    &confidence,
		}},
		StartedAt:   covStrPtr("2026-01-01T00:00:00Z"),
		FinishedAt:  covStrPtr("2026-01-01T00:00:01Z"),
		Provenance:  schemas.RunProvenance{PolicyVersion: 1, ModelBindings: []schemas.RunModelBinding{}},
		EarlySignal: schemas.EarlySignal{},
		StateChurn:  schemas.StateChurn{},
	}
	report, err := orchestrator.BuildCanonicalReport(outcome)
	require.NoError(t, err)
	require.NotNil(t, report)
	require.Empty(t, report.Validate())
	return report
}

/* ------------------------------------------------------------------- args.go */

func TestCOV_CLI_001_LooksLikeFlagValueClassifiesTokens(t *testing.T) {
	// A flag value is any non-empty token that does not start with '-'.
	assert.True(t, looksLikeFlagValue("value"))
	assert.True(t, looksLikeFlagValue("docs/a.md"))
	assert.False(t, looksLikeFlagValue(""), "the empty token is not a value")
	assert.False(t, looksLikeFlagValue("--flag"), "a long flag is not a value")
	assert.False(t, looksLikeFlagValue("-x"), "a short flag is not a value")
	assert.False(t, looksLikeFlagValue("-"), "a lone dash is not a value")
}

func TestCOV_CLI_002_ParseIntLenientMirrorsParseInt(t *testing.T) {
	n, ok := parseIntLenient("42")
	assert.True(t, ok)
	assert.Equal(t, 42, n)

	n, ok = parseIntLenient("+7")
	assert.True(t, ok)
	assert.Equal(t, 7, n)

	n, ok = parseIntLenient("  -9 ")
	assert.True(t, ok)
	assert.Equal(t, -9, n)

	// Number.parseInt('12abc', 10) === 12 — trailing junk is ignored.
	n, ok = parseIntLenient("12abc")
	assert.True(t, ok)
	assert.Equal(t, 12, n)

	_, ok = parseIntLenient("")
	assert.False(t, ok, "the empty string is NaN")
	_, ok = parseIntLenient("abc")
	assert.False(t, ok, "no digit parsed is NaN")
	_, ok = parseIntLenient("99999999999999999999")
	assert.False(t, ok, "an out-of-range value fails closed")
}

func TestCOV_CLI_003_IsDigitsAcceptsAsciiDigitsOnly(t *testing.T) {
	assert.True(t, isDigits("0"))
	assert.True(t, isDigits("1234567890"))
	assert.False(t, isDigits(""), "the empty string is not /^\\d+$/")
	assert.False(t, isDigits("12a"))
	assert.False(t, isDigits("1 2"))
}

/* --------------------------------------------------------- compose.go errors */

func TestCOV_CLI_004_CliConfigErrorCarriesMessageDiagnosticsAndCode(t *testing.T) {
	cfg := newCliConfigError("docsync: broken", "line one", "line two")
	assert.Equal(t, "docsync: broken", cfg.Error())
	assert.Equal(t, docutil.CodeConfig, cfg.Code())
	assert.Equal(t, []string{"line one", "line two"}, cfg.Diagnostics)

	var target *CliConfigError
	assert.True(t, asCliConfigError(error(cfg), &target))
	assert.Same(t, cfg, target)
	assert.False(t, asCliConfigError(errors.New("plain"), &target))
}

func TestCOV_CLI_005_StripModelEnvKeepsOnlyOpenRouterVariables(t *testing.T) {
	env := map[string]string{
		"OPENROUTER_API_KEY": "sk-or-v1-test",
		"OPENROUTER_SITE":    "docsync",
		"GITHUB_TOKEN":       "ghp-secret",
		"PATH":               "/usr/bin",
	}
	stripped := stripModelEnv(env)
	assert.Equal(t, map[string]string{
		"OPENROUTER_API_KEY": "sk-or-v1-test",
		"OPENROUTER_SITE":    "docsync",
	}, stripped, "INV-8: only OPENROUTER_* variables reach the model layer")
	assert.NotContains(t, stripped, "GITHUB_TOKEN")
	assert.Empty(t, stripModelEnv(nil))
}

func TestCOV_CLI_006_IsOutageOutcomeRecognizesOutageSignatures(t *testing.T) {
	for _, kind := range []schemas.DegradationKind{
		schemas.DegradationAnalyzerInconclusive,
		schemas.DegradationJevUnavailable,
		schemas.DegradationProviderOutage,
	} {
		outcome := &schemas.RunOutcome{
			Degradations: []schemas.Degradation{{Stage: "jev", Kind: kind, Detail: "down"}},
		}
		assert.True(t, isOutageOutcome(outcome), "degradation kind %s is an outage", kind)
		assert.Equal(t, "down", outageReason(outcome))
	}
	withAbstain := &schemas.RunOutcome{
		Abstains: []schemas.RunAbstain{{Reason: string(schemas.AbstainReasonModelUnavailable)}},
	}
	assert.True(t, isOutageOutcome(withAbstain), "a model_unavailable abstain is an outage")
	assert.Equal(t, "", outageReason(withAbstain), "no outage degradation renders as no reason")

	clean := &schemas.RunOutcome{
		Degradations: []schemas.Degradation{{Stage: "other", Kind: schemas.DegradationKind("irrelevant")}},
		Abstains:     []schemas.RunAbstain{{Reason: string(schemas.AbstainReasonOther)}},
	}
	assert.False(t, isOutageOutcome(clean))
	assert.Equal(t, "", outageReason(clean))
	assert.False(t, isOutageOutcome(&schemas.RunOutcome{}))
}

func TestCOV_CLI_007_RenderIssuesJoinsPathAndMessage(t *testing.T) {
	assert.Equal(t, "", renderIssues(nil))
	assert.Equal(t, "", renderIssues([]schemas.Issue{}))
	single := renderIssues([]schemas.Issue{{Path: []string{"rows", "0"}, Message: "bad"}})
	assert.Equal(t, "rows.0: bad", single)
	many := renderIssues([]schemas.Issue{
		{Path: []string{"a"}, Message: "one"},
		{Path: []string{"b", "c"}, Message: "two"},
	})
	assert.Equal(t, "a: one; b.c: two", many)
}

func TestCOV_CLI_008_DescribeErrRendersNilEmptyAndNormalErrors(t *testing.T) {
	assert.Equal(t, "undefined", describeErr(nil))
	assert.Equal(t, "Error", describeErr(covEmptyErr{}))
	assert.Equal(t, "boom", describeErr(errors.New("boom")))
}

type covEmptyErr struct{}

func (covEmptyErr) Error() string { return "   " } // a blank message renders the literal fallback

func TestCOV_CLI_009_ExtractCompiledPolicyVersionReadsTheStructuralField(t *testing.T) {
	assert.Equal(t, 0, extractCompiledPolicyVersion(nil))
	// The bootstrap policy v1 pins policy_version 1.
	assert.Equal(t, 1, extractCompiledPolicyVersion(covBootstrapPolicy(t)))
}

func TestCOV_CLI_010_DegradationIfInconclusiveOnlyForInconclusiveAnalysis(t *testing.T) {
	assert.Nil(t, degradationIfInconclusive(analysis.AnalysisOutcome{
		Kind: analysis.AnalysisOutcomeKindAnalysis, Record: &schemas.ChangeAnalysis{},
	}))
	withReason := degradationIfInconclusive(analysis.AnalysisOutcome{
		Kind: analysis.AnalysisOutcomeKindInconclusive, Reason: "analyzer exploded",
	})
	require.Len(t, withReason, 1)
	assert.Equal(t, schemas.DegradationAnalyzerInconclusive, withReason[0].Kind)
	assert.Equal(t, "analyzer exploded", withReason[0].Detail)
	blank := degradationIfInconclusive(analysis.AnalysisOutcome{Kind: analysis.AnalysisOutcomeKindInconclusive})
	require.Len(t, blank, 1)
	assert.Equal(t, "inconclusive", blank[0].Detail, "a blank reason falls back to 'inconclusive'")
}

func TestCOV_CLI_011_ParseYAMLValueRejectsBrokenYAMLAsConfig(t *testing.T) {
	value, err := parseYAMLValue("a: 1\n", ".docsync/x.yml")
	require.NoError(t, err)
	assert.NotNil(t, value)
	_, err = parseYAMLValue("a: [1,\n", ".docsync/x.yml")
	require.Error(t, err)
	assert.True(t, isConfigClass(err))
	assert.Contains(t, err.Error(), ".docsync/x.yml")
}

func TestCOV_CLI_012_ReadOptionalFileTreatsAbsenceAndDirectoriesAsAbsent(t *testing.T) {
	dir := t.TempDir()
	source, ok, err := readOptionalFile(filepath.Join(dir, "absent.yml"))
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "", source)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "present.yml"), []byte("hello"), 0o644))
	source, ok, err = readOptionalFile(filepath.Join(dir, "present.yml"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "hello", source)

	// A directory read failure is "not configured", never a fail-closed error.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "adir"), 0o755))
	_, ok, err = readOptionalFile(filepath.Join(dir, "adir"))
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCOV_CLI_013_LoadCompiledPolicyFallsBackToBootstrapAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	compiled, err := loadCompiledPolicy(dir)
	require.NoError(t, err)
	require.NotNil(t, compiled, "no policy.yml → the documented bootstrap policy v1")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".docsync"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".docsync", "policy.yml"), []byte("a: [1,\n"), 0o644))
	_, err = loadCompiledPolicy(dir)
	require.Error(t, err)
	assert.True(t, isConfigClass(err))
	assert.Contains(t, err.Error(), "policy.yml")

	// A structurally invalid policy renders its diagnostics and fails closed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".docsync", "policy.yml"), []byte("schema: docsync.jev-policy/v1\n"), 0o644))
	_, err = loadCompiledPolicy(dir)
	require.Error(t, err)
	assert.True(t, isConfigClass(err))
	assert.Contains(t, err.Error(), "failed policy compilation")
}

func TestCOV_CLI_014_PolicyDiagnosticRenderWithAndWithoutPath(t *testing.T) {
	assert.Equal(t, "bare message", policyDiagnosticRender(policy.PolicyDiagnostic{Message: "bare message"}))
	assert.Equal(t, "x.y: scoped message", policyDiagnosticRender(policy.PolicyDiagnostic{Path: "x.y", Message: "scoped message"}))
}

func TestCOV_CLI_015_LoadModelsConfigLanesFailClosed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".docsync"), 0o755))
	// Absent models.yml is a configuration defect (never a silent empty binding).
	_, err := loadModelsConfig(dir, "")
	require.Error(t, err)
	assert.True(t, isConfigClass(err))
	assert.Contains(t, err.Error(), "not found")

	// An explicit path overrides the default.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "elsewhere.yml"), []byte("schema: docsync.models/v1\n"), 0o644))
	_, err = loadModelsConfig(dir, filepath.Join(dir, "elsewhere.yml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "models-config validation")
}

func TestCOV_CLI_016_LoadModelsConfigYAMLLanes(t *testing.T) {
	_, err := loadModelsConfigYAML("a: [1,\n", "label.yml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid YAML")

	// Missing required roles fail closed with the exact role names.
	_, err = loadModelsConfigYAML("schema: docsync.models/v1\nversion: 1\nroles:\n  decision-engine:\n    model: m\n", "models.yml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required role bindings")
}

func TestCOV_CLI_017_OfflineAbstainIdIsDeterministicAndScoped(t *testing.T) {
	docPath := "docs/api.md"
	first := offlineAbstainId("run#a..b", &docPath, "model_unavailable")
	second := offlineAbstainId("run#a..b", &docPath, "model_unavailable")
	assert.Equal(t, first, second, "the same inputs derive the same abstain id")
	assert.True(t, strings.HasPrefix(first, "ab-"), first)
	assert.Len(t, first, len("ab-")+16)
	assert.NotEqual(t, first, offlineAbstainId("run#a..b", nil, "model_unavailable"),
		"the run-level abstain is scoped separately")
}

/* ------------------------------------------------------------ exit_codes.go */

func TestCOV_CLI_018_IsRuntimeClassAndIsConfigClassLanes(t *testing.T) {
	runErr := orchestrator.NewPipelineRunError("setup: cannot resolve base/head refs", "", errors.New("unknown revision"))
	assert.True(t, isRuntimeClass(runErr), "a pipeline run failure is the D14 lane")
	assert.False(t, isConfigClass(runErr))

	cfg := newCliConfigError("docsync: broken")
	assert.False(t, isRuntimeClass(cfg), "a CliConfigError is never a runtime failure")
	assert.True(t, isConfigClass(cfg))

	configErr := orchestrator.NewPipelineConfigError("no policy version pinned", "", nil)
	assert.True(t, isConfigClass(configErr))
	assert.False(t, isRuntimeClass(configErr))

	plain := errors.New("plain")
	assert.False(t, isRuntimeClass(plain))
	assert.False(t, isConfigClass(plain))
}

func TestCOV_CLI_019_DescriptionForExitCodeUnknownCodeIsHonest(t *testing.T) {
	assert.Equal(t, "unknown exit code", DescriptionForExitCode(DocsyncExitCode(9)))
	assert.Equal(t, "no drift detected", DescriptionForExitCode(ExitOK))
}

/* -------------------------------------------------------------------- io.go */

func TestCOV_CLI_020_NewCliIoAppendsOneNewlinePerLine(t *testing.T) {
	var stdout, stderr strings.Builder
	io := NewCliIo(&stdout, &stderr)
	io.Stdout("line one")
	io.Stdout("line two")
	io.Stderr("problem")
	assert.Equal(t, "line one\nline two\n", stdout.String())
	assert.Equal(t, "problem\n", stderr.String())
}

/* -------------------------------------------------------- commands_check.go */

func TestCOV_CLI_021_RenderCheckReportFailureFailsClosedToRuntime(t *testing.T) {
	rec := &covRecorder{}
	exit := renderCheckReportFailure(errors.New("schema drift"), rec)
	assert.Equal(t, ExitRuntime, exit)
	assert.Contains(t, rec.err(), "docsync check:")
	assert.Contains(t, rec.err(), "canonical report failed schema validation")
	assert.Contains(t, rec.err(), "failing closed, nothing emitted (D14)")
	assert.Empty(t, rec.out(), "a failed report is never emitted on stdout")
}

func TestCOV_CLI_022_VerboseStageLineRendersLatencyOrNull(t *testing.T) {
	assert.Equal(t, "stage=analyze status=ok latency_ms=null",
		verboseStageLine(schemas.PipelineStage{Stage: "analyze", Status: schemas.RunStageOk}))
	latency := 17
	assert.Equal(t, "stage=jev status=ok latency_ms=17",
		verboseStageLine(schemas.PipelineStage{Stage: "jev", Status: schemas.RunStageOk, LatencyMS: &latency}))
}

func TestCOV_CLI_023_JsonStringFallsBackToEmptyObjectOnMarshalError(t *testing.T) {
	assert.Equal(t, `{"ok":true}`, jsonString(map[string]any{"ok": true}))
	assert.Equal(t, "{}", jsonString(make(chan int)), "an unmarshalable value degrades to {}")
}

func TestCOV_CLI_024_GlyphSetOfMapsFallbackAndUnicode(t *testing.T) {
	assert.Equal(t, orchestrator.GlyphsFallback, glyphSetOf(GlyphsFallback))
	assert.Equal(t, orchestrator.GlyphsUnicode, glyphSetOf(GlyphsUnicode))
	assert.Equal(t, orchestrator.GlyphsUnicode, glyphSetOf(GlyphMode("anything-else")))
}

func TestCOV_CLI_025_RenderCheckComposeFailureMapsConfigAndRuntimeLanes(t *testing.T) {
	cfgRec := &covRecorder{}
	exit := renderCheckComposeFailure(newCliConfigError("docsync: broken", "diag line"), cfgRec)
	assert.Equal(t, ExitConfig, exit)
	assert.Contains(t, cfgRec.err(), "docsync: broken")
	assert.Contains(t, cfgRec.err(), "diag line")

	runRec := &covRecorder{}
	exit = renderCheckComposeFailure(errors.New("boom"), runRec)
	assert.Equal(t, ExitRuntime, exit)
	assert.Contains(t, runRec.err(), "docsync check: boom")

	cfgClassRec := &covRecorder{}
	exit = renderCheckComposeFailure(orchestrator.NewPipelineConfigError("no policy", "", nil), cfgClassRec)
	assert.Equal(t, ExitConfig, exit)
}

func TestCOV_CLI_026_RenderCheckRunFailureMapsBothLanes(t *testing.T) {
	cfgRec := &covRecorder{}
	assert.Equal(t, ExitConfig,
		renderCheckRunFailure(orchestrator.NewPipelineConfigError("no policy", "", nil), cfgRec))
	assert.Contains(t, cfgRec.err(), "docsync check:")

	runRec := &covRecorder{}
	assert.Equal(t, ExitRuntime,
		renderCheckRunFailure(orchestrator.NewPipelineRunError("cannot resolve", "", errors.New("x")), runRec))
	assert.Contains(t, runRec.err(), "cannot resolve")
}

/* --------------------------------------------------------- commands_fix.go */

func TestCOV_CLI_027_NoopIncidentSinkRecordsSilently(t *testing.T) {
	var sink noopIncident
	assert.NoError(t, sink.Record("at", "run", "rule", "path", "detail", "doc"))
}

func TestCOV_CLI_028_RevalidateFixReportLanes(t *testing.T) {
	rec := &covRecorder{}
	report, ok := revalidateFixReport(FixFlowResult{}, rec)
	assert.False(t, ok)
	assert.Nil(t, report)
	assert.Contains(t, rec.err(), "nil report")

	rec = &covRecorder{}
	report, ok = revalidateFixReport(FixFlowResult{Report: &schemas.CanonicalReport{}}, rec)
	assert.False(t, ok)
	assert.Nil(t, report)
	assert.Contains(t, rec.err(), "schema validation")

	rec = &covRecorder{}
	valid := covValidCanonicalReport(t)
	report, ok = revalidateFixReport(FixFlowResult{Report: valid}, rec)
	assert.True(t, ok)
	assert.Same(t, valid, report)
	assert.Empty(t, rec.err())
}

func TestCOV_CLI_029_RenderFixComposeFailureMapsBothLanes(t *testing.T) {
	cfgRec := &covRecorder{}
	assert.Equal(t, ExitConfig, renderFixComposeFailure(newCliConfigError("docsync fix: broken", "diag"), cfgRec))
	assert.Contains(t, cfgRec.err(), "diag")

	cfgClassRec := &covRecorder{}
	assert.Equal(t, ExitConfig, renderFixComposeFailure(orchestrator.NewPipelineConfigError("no policy", "", nil), cfgClassRec))

	runRec := &covRecorder{}
	assert.Equal(t, ExitRuntime, renderFixComposeFailure(errors.New("boom"), runRec))
	assert.Contains(t, runRec.err(), "docsync fix: boom")
}

func TestCOV_CLI_030_RunIDOfEchoesOnlyAnExplicitRunId(t *testing.T) {
	assert.Equal(t, "", runIDOf(&ParsedInvocation{}))
	runID := "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"
	assert.Equal(t, runID, runIDOf(&ParsedInvocation{RunID: &runID}))
}

func TestCOV_CLI_031_FixRunRecordStoreWriteFailureSurfaces(t *testing.T) {
	// The state dir path is occupied by a regular file → MkdirAll fails loud.
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o644))
	store := NewFixRunRecordStore(blocker)
	runID := "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"
	err := store.Write(FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: runID})
	require.Error(t, err)
	assert.False(t, store.Has(runID))
}

func TestCOV_CLI_032_PatchCaptureResolveLanes(t *testing.T) {
	capture := newPatchCapture()
	patch, err := capture.resolve("docs/api.md", nil)
	require.NoError(t, err)
	assert.Nil(t, patch, "no captured patch → null (the flow reports not-a-candidate)")

	capture.add(nil) // a nil add is ignored
	first := &schemas.DocPatch{Schema: "docsync.doc-patch/v1", RunID: "r", DocPath: "docs/api.md"}
	capture.add(first)
	patch, err = capture.resolve("docs/api.md", nil)
	require.NoError(t, err)
	require.NotNil(t, patch)
	assert.Equal(t, *first, *patch, "a single captured patch resolves without a ref")

	second := &schemas.DocPatch{Schema: "docsync.doc-patch/v1", RunID: "r", DocPath: "docs/api.md"}
	capture.add(second)
	patch, err = capture.resolve("docs/api.md", nil)
	require.NoError(t, err)
	assert.Nil(t, patch, "an ambiguous capture without a ref resolves to null")

	ref, err := verifier.ComputePatchRef(second)
	require.NoError(t, err)
	patch, err = capture.resolve("docs/api.md", &ref)
	require.NoError(t, err)
	require.NotNil(t, patch)
	assert.Equal(t, *second, *patch, "the patch ref pins the exact captured patch")

	missing := "state/docs/docs__api.md#patch-0000000000000000"
	patch, err = capture.resolve("docs/api.md", &missing)
	require.NoError(t, err)
	assert.Nil(t, patch, "an unresolvable patch ref is null, never a guess")

	patch, err = capture.resolve("docs/other.md", nil)
	require.NoError(t, err)
	assert.Nil(t, patch)
}

func TestCOV_CLI_033_EffectiveModeResolvesEntryThenDefaultsThenSuggest(t *testing.T) {
	entryMode := schemas.DocMode("auto")
	assert.Equal(t, "auto", effectiveMode(schemas.ManifestEntry{Mode: &entryMode}, nil))
	defaultsMode := schemas.DocMode("locked")
	assert.Equal(t, "locked", effectiveMode(schemas.ManifestEntry{}, &schemas.ManifestDefaults{Mode: &defaultsMode}))
	assert.Equal(t, "suggest", effectiveMode(schemas.ManifestEntry{}, nil))
}

func TestCOV_CLI_034_MatchesSelectorMatchesExactAndPrefix(t *testing.T) {
	assert.True(t, matchesSelector("docs/api.md", []string{"docs/api.md"}))
	assert.True(t, matchesSelector("docs/api.md", []string{"docs"}))
	assert.True(t, matchesSelector("docs/api.md", []string{"other", "docs"}))
	assert.False(t, matchesSelector("docs/api.md", []string{"docs/api.md.bak"}))
	assert.False(t, matchesSelector("src/api.md", []string{"docs"}))
}

func TestCOV_CLI_035_ManifestConfigDetailRendersAbsentAndDiagnostics(t *testing.T) {
	assert.Contains(t, manifestConfigDetail(nil), "manifest.yml not found")
	assert.Contains(t, manifestConfigDetail(nil), "D7/D8")
	assert.Contains(t, manifestConfigDetail([]validate.Diagnostic{
		{Code: "E_MANIFEST_SCHEMA", Message: "version must be positive"},
	}), "E_MANIFEST_SCHEMA: version must be positive")
}

func TestCOV_CLI_036_ReadWorktreeFileAndWriteDocFileLanes(t *testing.T) {
	root := t.TempDir()
	assert.Nil(t, readWorktreeFile(root, "missing.md"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs.md"), []byte("body"), 0o644))
	content := readWorktreeFile(root, "docs.md")
	require.NotNil(t, content)
	assert.Equal(t, "body", *content)

	// The happy path writes the file.
	writeDocFile(root, "docs/api.md", "patched\n")
	onDisk, err := os.ReadFile(filepath.Join(root, "docs", "api.md"))
	require.NoError(t, err)
	assert.Equal(t, "patched\n", string(onDisk))

	// The parent path is a FILE → the write is skipped (never a panic).
	require.NoError(t, os.WriteFile(filepath.Join(root, "blocker"), []byte("x"), 0o644))
	writeDocFile(root, "blocker/api.md", "never written\n")
	_, err = os.Stat(filepath.Join(root, "blocker", "api.md"))
	require.Error(t, err, "the blocked write never lands on disk")
}

func TestCOV_CLI_037_RenderFixFlowResultLanes(t *testing.T) {
	args := &ParsedInvocation{}
	rec := &covRecorder{}
	assert.Equal(t, ExitConfig, renderFixFlowResult(FixFlowResult{Kind: FixResultConfigError, Detail: "bad config"}, args, rec, nil))
	assert.Contains(t, rec.err(), "bad config")

	rec = &covRecorder{}
	assert.Equal(t, ExitRuntime, renderFixFlowResult(FixFlowResult{Kind: FixResultRuntimeErr, Detail: "boom"}, args, rec, nil))
	assert.Contains(t, rec.err(), "boom")

	// The noop lane: text (default) and --json.
	runID := "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"
	rec = &covRecorder{}
	assert.Equal(t, ExitOK, renderFixFlowResult(FixFlowResult{Kind: FixResultNoop, Reason: "same-run-skip"},
		&ParsedInvocation{RunID: &runID}, rec, nil))
	assert.Contains(t, rec.out(), "no-op: run-id "+runID+" already recorded")

	rec = &covRecorder{}
	assert.Equal(t, ExitOK, renderFixFlowResult(FixFlowResult{Kind: FixResultNoop, Reason: "same-run-skip"},
		&ParsedInvocation{RunID: &runID, JSON: true}, rec, nil))
	assert.JSONEq(t, `{"report":null,"applied":[],"skipped":[],"refused":[],"run_id":"`+runID+`"}`+"", rec.out())

	// The completed lane: a valid report renders, applied rows map to drift.
	report := covValidCanonicalReport(t)
	rec = &covRecorder{}
	assert.Equal(t, ExitDrift, renderFixFlowResult(FixFlowResult{
		Kind:    FixResultCompleted,
		Report:  report,
		Applied: []FixFlowApplied{{DocPath: "docs/api.md", PatchRef: "state/docs/docs__api.md#patch-aaaaaaaaaaaaaaaa"}},
	}, args, rec, nil))
	assert.Contains(t, rec.out(), "Documentation consistency")
	assert.Contains(t, rec.out(), "✓ applied docs/api.md")

	// A completed run with nothing applied and nothing refused is a clean exit.
	rec = &covRecorder{}
	assert.Equal(t, ExitOK, renderFixFlowResult(FixFlowResult{Kind: FixResultCompleted, Report: report}, args, rec, nil))

	// An invalid report fails closed through the --json re-validation lane.
	rec = &covRecorder{}
	assert.Equal(t, ExitRuntime, renderFixFlowResult(FixFlowResult{
		Kind: FixResultCompleted, Report: &schemas.CanonicalReport{},
	}, &ParsedInvocation{JSON: true}, rec, nil))
	assert.Contains(t, rec.err(), "schema validation")
}

func TestCOV_CLI_038_BoolYesNoRendersTheStablePair(t *testing.T) {
	assert.Equal(t, "yes", boolYesNo(true))
	assert.Equal(t, "no", boolYesNo(false))
}

func TestCOV_CLI_039_JoinCommaJoinsWithSpace(t *testing.T) {
	assert.Equal(t, "", joinComma(nil))
	assert.Equal(t, "a", joinComma([]string{"a"}))
	assert.Equal(t, "a, b, c", joinComma([]string{"a", "b", "c"}))
}

/* --------------------------------------------------------- commands_init.go */

func TestCOV_CLI_040_InitSkeletonGateAndWriteFailureLanes(t *testing.T) {
	// The .docsync path is occupied by a regular file → the mkdir fails loud
	// and init reports a runtime failure (never a silent success).
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".docsync"), []byte("not a dir"), 0o644))
	result := RunInit(InitOptions{RepoRoot: root, Force: true})
	assert.Equal(t, ExitRuntime, result.ExitCode)
	assert.Contains(t, strings.Join(result.Messages, "\n"), "failed to write")

	// A config file path occupied by a directory → the write fails loud.
	root2 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root2, ".docsync", "manifest.yml"), 0o755))
	result2 := RunInit(InitOptions{RepoRoot: root2, Force: true})
	assert.Equal(t, ExitRuntime, result2.ExitCode)
	assert.Contains(t, strings.Join(result2.Messages, "\n"), "failed to write")
}

/* ------------------------------------------------------- commands_audit.go */

func TestCOV_CLI_041_ParseIntFlagLanes(t *testing.T) {
	assert.Equal(t, 4, parseIntFlag(nil, 4), "an absent flag keeps the fallback")
	assert.Equal(t, 7, parseIntFlag(covStrPtr("7"), 4))
	assert.Equal(t, 4, parseIntFlag(covStrPtr("0"), 4), "a non-positive value falls back")
	assert.Equal(t, 4, parseIntFlag(covStrPtr("not-a-number"), 4), "an unparsable value falls back")
	assert.Equal(t, 4, parseIntFlag(covStrPtr("2147483648"), 4), "beyond int32 falls back")
}

func TestCOV_CLI_042_RenderAuditResultLanes(t *testing.T) {
	// Noop: text + json.
	rec := &covRecorder{}
	assert.Equal(t, ExitOK, renderAuditResult(&ParsedInvocation{}, rec,
		AuditRunResult{Kind: AuditRunNoop, Reason: "trigger-* is not checked out"}))
	assert.Contains(t, rec.out(), "trigger not eligible")
	assert.Contains(t, rec.out(), "nothing ran, nothing wrote")

	rec = &covRecorder{}
	assert.Equal(t, ExitOK, renderAuditResult(&ParsedInvocation{JSON: true}, rec,
		AuditRunResult{Kind: AuditRunNoop, Reason: "not-eligible"}))
	assert.JSONEq(t, `{"kind":"noop","reason":"not-eligible"}`, rec.out())

	// Abstained: stderr + exit 3 (json stays silent).
	rec = &covRecorder{}
	assert.Equal(t, ExitRuntime, renderAuditResult(&ParsedInvocation{JSON: true}, rec,
		AuditRunResult{Kind: AuditRunAbstained, Reason: "corpus empty"}))
	assert.JSONEq(t, `{"kind":"abstained","detail":"corpus empty"}`, rec.out())
	assert.Contains(t, rec.err(), "abstained (fail closed)")

	// Ran with a degraded status: the detail lanes (present + defaulted).
	rec = &covRecorder{}
	assert.Equal(t, ExitRuntime, renderAuditResult(&ParsedInvocation{}, rec,
		AuditRunResult{Kind: AuditRunRan, Status: "failed", RunID: "run-1", Detail: "analyzer outage"}))
	assert.Contains(t, rec.err(), `run run-1 ended status "failed"`)
	assert.Contains(t, rec.err(), "analyzer outage")

	rec = &covRecorder{}
	assert.Equal(t, ExitRuntime, renderAuditResult(&ParsedInvocation{}, rec,
		AuditRunResult{Kind: AuditRunRan, Status: "failed", RunID: "run-2"}))
	assert.Contains(t, rec.err(), "no detail")

	// Ran completed, text mode: verbose renders the issue markdown; the org
	// report renders as data.
	rec = &covRecorder{}
	exit := renderAuditResult(&ParsedInvocation{Verbose: true}, rec, AuditRunResult{
		Kind: AuditRunRan, Status: "completed", Trigger: "nightly", RunID: "run-3",
		FindingCount: 3, AppendedCount: 2, IssueMarkdown: "ISSUE BODY", OrgMarkdown: "ORG REPORT",
	})
	assert.Equal(t, ExitDrift, exit)
	assert.Contains(t, rec.out(), "3 finding(s)")
	assert.Contains(t, rec.out(), "2 newly appended")
	assert.Contains(t, rec.out(), "ISSUE BODY")
	assert.Contains(t, rec.out(), "ORG REPORT")

	// Ran completed, --json: the machine-readable payload omits an absent org report.
	rec = &covRecorder{}
	exit = renderAuditResult(&ParsedInvocation{JSON: true}, rec, AuditRunResult{
		Kind: AuditRunRan, Status: "completed", Trigger: "nightly", RunID: "run-4", FindingCount: 0,
	})
	assert.Equal(t, ExitOK, exit)
	assert.JSONEq(t, `{"ok":true,"trigger":"nightly","runId":"run-4","status":"completed","findings":0,"appended":0,"advisory":true}`, rec.out())

	// An unknown kind is an honest no-op.
	rec = &covRecorder{}
	assert.Equal(t, ExitOK, renderAuditResult(&ParsedInvocation{}, rec, AuditRunResult{Kind: AuditRunKind("mystery")}))
}

/* ------------------------------------------------------------------ cmd.go */

func TestCOV_CLI_043_EmitInitLanes(t *testing.T) {
	rec := &covRecorder{}
	emitInit(InitResult{ExitCode: ExitOK, Messages: []string{"one", "two"}}, false, rec)
	assert.Equal(t, "one\ntwo", rec.out())

	rec = &covRecorder{}
	emitInit(InitResult{ExitCode: ExitOK, Messages: []string{"one", "two"}}, true, rec)
	assert.JSONEq(t, `{"ok":true,"messages":["one","two"]}`, rec.out())

	rec = &covRecorder{}
	emitInit(InitResult{ExitCode: ExitConfig}, true, rec)
	assert.JSONEq(t, `{"ok":false,"messages":null}`, rec.out())
}

/* --------------------------------------------------------- commands_pr.go */

func TestCOV_CLI_044_PrSkipReasonMapsEveryKnownReason(t *testing.T) {
	assert.Contains(t, prSkipReason("after_verification_identical"), "re-verification proved the branch content unchanged")
	assert.Contains(t, prSkipReason("no_proposed_changes"), "nothing to open")
	assert.Contains(t, prSkipReason("nothing_new"), "not new")
	assert.Contains(t, prSkipReason("up_to_date"), "already up to date")
	assert.Equal(t, "not attempted yet — unexpected", prSkipReason("mystery"))
}

func TestCOV_CLI_045_MapStringFallsBackOnEmptyResult(t *testing.T) {
	assert.Equal(t, "fallback", mapString(func(string) string { return "" }, "x", "fallback"))
	assert.Equal(t, "value", mapString(func(string) string { return "value" }, "x", "fallback"))
}

func TestCOV_CLI_046_PrIgnoredJSONAndJsonBoolAndPlural(t *testing.T) {
	assert.Equal(t, map[string]any{"docPath": "docs/x.md", "reason": "not-a-candidate", "detail": "review"},
		prIgnoredJSON(PrIssueRef{DocPath: "docs/x.md", Reason: "not-a-candidate", Detail: "review"}))
	assert.Equal(t, "true", jsonBool(true))
	assert.Equal(t, "false", jsonBool(false))
	assert.Equal(t, "y", plural(1))
	assert.Equal(t, "ies", plural(0))
	assert.Equal(t, "ies", plural(2))
}

func TestCOV_CLI_047_ItoaRendersZeroPositiveAndNegative(t *testing.T) {
	assert.Equal(t, "0", itoa(0))
	assert.Equal(t, "42", itoa(42))
	assert.Equal(t, "-42", itoa(-42))
	assert.Equal(t, "2147483647", itoa(2147483647))
}

func TestCOV_CLI_048_AppliedDocPathsProjectsTheDocPaths(t *testing.T) {
	assert.Equal(t, []string{}, appliedDocPaths(nil))
	assert.Equal(t, []string{"docs/a.md", "docs/b.md"}, appliedDocPaths([]FixFlowApplied{
		{DocPath: "docs/a.md"}, {DocPath: "docs/b.md"},
	}))
}

func TestCOV_CLI_049_JSONResultOfLanes(t *testing.T) {
	msg := "boom"
	// Not opened, with a run error → the run_failed unknown.
	out := JSONResultOf(PrPayload{RunError: &msg})
	assert.Equal(t, false, out["ok"])
	assert.Equal(t, false, out["pr_opened"])
	unknowns, ok := out["unknown"].([]any)
	require.True(t, ok)
	require.Len(t, unknowns, 1)
	first, _ := unknowns[0].(map[string]any)
	assert.Equal(t, "run_failed_no_opened_pr", first["kind"])

	// Not opened, without a run error → the no_opened_pr unknown; ignored rows
	// render and the fix summary rides along.
	ignored := JSONResultOf(PrPayload{
		FixSummaryLines: []string{"✓ applied docs/a.md"},
		Ignored:         []PrIssueRef{{DocPath: "docs/x.md", Reason: "not-a-candidate", Detail: "review"}},
	})
	unknowns, ok = ignored["unknown"].([]any)
	require.True(t, ok)
	require.Len(t, unknowns, 1)
	first, _ = unknowns[0].(map[string]any)
	assert.Equal(t, "no_opened_pr_without_error", first["kind"])
	assert.Equal(t, []string{"✓ applied docs/a.md"}, ignored["fix_summary"])
	ignoredRows, ok := ignored["ignored"].([]any)
	require.True(t, ok)
	require.Len(t, ignoredRows, 1)
	row, _ := ignoredRows[0].(map[string]any)
	assert.Equal(t, "docs/x.md", row["docPath"])

	// Opened: pr number, replies, and the verdict pair.
	opened := JSONResultOf(PrPayload{
		Applied:         []FixFlowApplied{{DocPath: "docs/a.md"}},
		PrOpened:        true,
		PrNumber:        covIntPtr(9),
		RecordedReplies: 2,
		PrResult:        &prResultVerdict{Verdict: "dry_run", Reason: "no-op"},
	})
	assert.Equal(t, true, opened["ok"])
	assert.Equal(t, true, opened["pr_opened"])
	assert.Equal(t, 9, opened["pr_number"])
	assert.Equal(t, 2, opened["recorded_replies"])
	result, ok := opened["pr_result"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "dry_run", result["verdict"])
	assert.Equal(t, "no-op", result["reason"])
	assert.Equal(t, []string{"docs/a.md"}, opened["fix_outcome"].(map[string]any)["applied"],
		"JSONResultOf projects the applied rows through appliedDocPaths")
	_, hasUnknown := opened["unknown"]
	assert.False(t, hasUnknown, "an opened PR never reports unknowns")
}

func TestCOV_CLI_050_RenderPrOutcomeLanes(t *testing.T) {
	msg := "pipeline broke"
	push := "credential rejected"
	payload := PrPayload{
		Applied:         []FixFlowApplied{{DocPath: "docs/api.md", PatchRef: "sha256:abc"}},
		Ignored:         []PrIssueRef{{DocPath: "docs/x.md", Reason: "not-a-candidate", Detail: "review"}},
		RunError:        &msg,
		PushFailed:      &push,
		PrOpened:        true,
		PrNumber:        covIntPtr(7),
		RecordedReplies: 1,
		PrResult:        &prResultVerdict{Verdict: "skipped", Reason: "up_to_date"},
	}
	joined := strings.Join(RenderPrOutcome(payload, GlyphsUnicode), "\n")
	assert.Contains(t, joined, "Applied 1 verified minimal patch(es)")
	assert.Contains(t, joined, "✓ docs/api.md (sha256:abc)")
	assert.Contains(t, joined, "⏭ docs/x.md (not-a-candidate — review)")
	assert.Contains(t, joined, "Pipeline run error (P7-T13 message): pipeline broke")
	assert.Contains(t, joined, "Local branch push failed")
	assert.Contains(t, joined, "Opened documentation PR #7 (1 reviewer reply recorded")
	assert.Contains(t, joined, "PR skipped: the docs are already up to date")
	assert.Contains(t, joined, "ok: false")

	// A dry-run verdict, no PR number, and the ASCII glyph set.
	dry := strings.Join(RenderPrOutcome(PrPayload{
		PrOpened: true,
		PrResult: &prResultVerdict{Verdict: "dry_run"},
	}, GlyphsFallback), "\n")
	assert.Contains(t, dry, "Dry-run: prepared a documentation branch but opened no PR")
	assert.Contains(t, dry, "Opened documentation PR #?")
	assert.Contains(t, dry, "(0 reviewer repl")

	// An unknown skip reason falls back to the mapString default.
	unknown := strings.Join(RenderPrOutcome(PrPayload{
		PrResult: &prResultVerdict{Verdict: "skipped", Reason: "mystery"},
	}, GlyphsUnicode), "\n")
	assert.Contains(t, unknown, "PR skipped: not attempted yet — unexpected")
	assert.Contains(t, unknown, "ok: true")
}

func TestCOV_CLI_051_PrSourcePullRecognizesTheSentinel(t *testing.T) {
	assert.True(t, prSourcePull(ErrPrSourcePull))
	assert.True(t, prSourcePull(fmt.Errorf("docsync pr: %w", ErrPrSourcePull)))
	assert.False(t, prSourcePull(errors.New("some other failure")))
	assert.False(t, prSourcePull(nil))
}

func TestCOV_CLI_052_RenderPrFailuresMapConfigAndRuntimeLanes(t *testing.T) {
	cfgRec := &covRecorder{}
	assert.Equal(t, ExitConfig, renderPrComposeFailure(newCliConfigError("docsync pr: broken"), cfgRec))
	assert.Contains(t, cfgRec.err(), "docsync pr: broken")

	runRec := &covRecorder{}
	assert.Equal(t, ExitRuntime, renderPrComposeFailure(errors.New("git exploded"), runRec))
	assert.Contains(t, runRec.err(), "git exploded")

	cfgRunRec := &covRecorder{}
	assert.Equal(t, ExitConfig, renderPrRunFailure(orchestrator.NewPipelineConfigError("no policy", "", nil), cfgRunRec))

	runRunRec := &covRecorder{}
	assert.Equal(t, ExitRuntime, renderPrRunFailure(orchestrator.NewPipelineRunError("cannot resolve", "", errors.New("x")), runRunRec))
	assert.Contains(t, runRunRec.err(), "cannot resolve")
}

/* ---------------------------------------------------------- model_ports.go */

func TestCOV_CLI_053_MatchGlobsSeamMatchesAndFailsClosed(t *testing.T) {
	assert.True(t, matchGlobs("src/payments/refund.ts", "src/**"))
	assert.False(t, matchGlobs("docs/api.md", "src/**"))
	assert.False(t, matchGlobs("anything", "[unclosed"), "an invalid glob never matches")
}

func TestCOV_CLI_054_BuildModelPortsFailsClosedOnMissingInputs(t *testing.T) {
	ctx := t.Context()
	_, err := BuildModelPorts(ctx, ModelPortsOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compiled policy workflow is required")

	bootstrap := covBootstrapPolicy(t)
	_, err = BuildModelPorts(ctx, ModelPortsOptions{Compiled: bootstrap})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "models configuration is required")

	models := covModelsConfig(t)
	_, err = BuildModelPorts(ctx, ModelPortsOptions{Compiled: bootstrap, Models: models})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENROUTER_API_KEY is required")

	_, err = BuildModelPorts(ctx, ModelPortsOptions{
		Compiled: bootstrap, Models: models, APIKey: "k", Transport: covNotTransport{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not satisfy openrouter.OpenRouterTransport")
}

type covNotTransport struct{}

func TestCOV_CLI_055_PatchWriterTelemetryFromProjectsProviderAndUsage(t *testing.T) {
	provider := "openai"
	total := 120
	cost := 0.25
	telemetry := patchWriterTelemetryFrom(openrouter.RoleTelemetry{
		Role: "patch-writer", RequestedModel: "anthropic/x", RoutedModel: "anthropic/y",
		LatencyMS: 42, Attempt: 2, FallbackUsed: true,
		Provider: &provider,
		Usage:    &openrouter.RoleUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: &total, Cost: &cost},
	})
	assert.Equal(t, "patch-writer", telemetry.Role)
	assert.Equal(t, "anthropic/y", telemetry.RoutedModel)
	assert.Equal(t, 42, telemetry.LatencyMS)
	assert.Equal(t, 2, telemetry.Attempt)
	assert.True(t, telemetry.FallbackUsed)
	assert.Equal(t, provider, telemetry.Provider)
	require.NotNil(t, telemetry.Usage)
	assert.Equal(t, 100, telemetry.Usage.PromptTokens)
	assert.Equal(t, 20, telemetry.Usage.CompletionTokens)
	assert.Equal(t, 120, telemetry.Usage.TotalTokens)
	assert.InDelta(t, 0.25, telemetry.Usage.Cost, 1e-9)

	// The absent halves project to zero values, never a nil dereference.
	bare := patchWriterTelemetryFrom(openrouter.RoleTelemetry{Role: "patch-writer"})
	assert.Equal(t, "", bare.Provider)
	assert.Nil(t, bare.Usage)
}

func TestCOV_CLI_056_JevTelemetryFromProjectsProviderAndUsage(t *testing.T) {
	provider := "z-ai"
	usage := jevTelemetryFrom(openrouter.RoleTelemetry{
		Role: "decision-engine", RequestedModel: "a", RoutedModel: "b", LatencyMS: 5, Attempt: 1,
		Provider: &provider,
		Usage:    &openrouter.RoleUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: covIntPtr(3), Cost: covFloatPtr(0.5)},
	})
	assert.Equal(t, "decision-engine", usage.Role)
	assert.Equal(t, "b", usage.RoutedModel)
	require.NotNil(t, usage.Provider)
	assert.Equal(t, provider, *usage.Provider)
	require.NotNil(t, usage.Usage)
	require.NotNil(t, usage.Usage.TotalTokens)
	assert.Equal(t, 3, *usage.Usage.TotalTokens)
	require.NotNil(t, usage.Usage.Cost)
	assert.InDelta(t, 0.5, *usage.Usage.Cost, 1e-9)
	assert.Nil(t, jevTelemetryFrom(openrouter.RoleTelemetry{}).Usage)
}

func TestCOV_CLI_057_AbstainReasonValueProjectsTheOptionalReason(t *testing.T) {
	assert.Equal(t, schemas.AbstainReason(""), abstainReasonValue(nil))
	reason := schemas.AbstainReasonModelUnavailable
	assert.Equal(t, reason, abstainReasonValue(&reason))
}

func TestCOV_CLI_058_CliContainsStringFindsMembers(t *testing.T) {
	assert.True(t, cliContainsString([]string{"a", "b"}, "b"))
	assert.False(t, cliContainsString([]string{"a", "b"}, "c"))
	assert.False(t, cliContainsString(nil, "c"))
}

func TestCOV_CLI_059_WriterGitPortNilClientFailsClosed(t *testing.T) {
	port := writerGitPort{}
	_, err := port.CatFile("HEAD", "docs/api.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a git client")
	_, err = port.DiffFiles("base", "head")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a git client")
}

func TestCOV_CLI_060_RenderFixSummaryFallbackGlyphs(t *testing.T) {
	lines := RenderFixSummary(FixFlowResult{
		Kind:    FixResultCompleted,
		Applied: []FixFlowApplied{{DocPath: "docs/a.md", PatchRef: "ref"}},
		Skipped: []FixFlowSkipped{{DocPath: "docs/b.md", Reason: "dirty", Detail: "uncommitted"}},
		Refused: []FixFlowRefused{{DocPath: "docs/c.md", Reason: "stale-patch", Detail: "race", ReDetected: false}},
	}, GlyphsFallback)
	require.Len(t, lines, 3)
	assert.Contains(t, lines[0], "[ok] applied docs/a.md (ref)")
	assert.Contains(t, lines[1], "[skip] skipped docs/b.md (dirty — uncommitted)")
	assert.Contains(t, lines[2], "[!] refused docs/c.md (stale-patch — race; re-detected: no)")
}

/* ------------------------------------------------- compose.go remaining lanes */

func TestCOV_CLI_061_ResolveBaseRefFailsOnNewGitClientError(t *testing.T) {
	rec := &covRecorder{}
	base, ok := resolveBaseRef(context.Background(), "/dev/null/nonexistent", &ParsedInvocation{}, rec)
	assert.False(t, ok)
	assert.Equal(t, "", base)
	assert.Contains(t, rec.err(), "docsync check:")
	assert.Contains(t, rec.err(), "no --base given")
}

func TestCOV_CLI_062_ResolveBaseRefWithExplicitBaseReturnsImmediately(t *testing.T) {
	base, ok := resolveBaseRef(context.Background(), "/invalid/path", &ParsedInvocation{Base: covStrPtr("HEAD")}, &covRecorder{})
	assert.True(t, ok)
	assert.Equal(t, "HEAD", base)
}

func TestCOV_CLI_063_ExtractCompiledPolicyVersionNilPolicyVersion(t *testing.T) {
	compiled, err := policy.CompileBootstrapPolicyV1()
	require.NoError(t, err)
	raw, err := json.Marshal(compiled)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	policyMap, ok := decoded["policy"].(map[string]any)
	require.True(t, ok)
	delete(policyMap, "policy_version")
	reEncoded, err := json.Marshal(decoded)
	require.NoError(t, err)
	var mutated policy.CompiledPolicyWorkflow
	require.NoError(t, json.Unmarshal(reEncoded, &mutated))
	assert.Equal(t, 0, extractCompiledPolicyVersion(&mutated))
}

func TestCOV_CLI_064_ReadOptionalFileDirectoryPathIsAbsent(t *testing.T) {
	dir := t.TempDir()
	_, ok, err := readOptionalFile(dir)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCOV_CLI_065_FileIncidentSinkRecordHappyPath(t *testing.T) {
	sink := newFileIncidentSink(t.TempDir())
	err := sink.Record("2026-01-01T00:00:00Z", "run-1", "out_of_allowlist_path", "src/x.ts", "blocked", "docs/api.md")
	require.NoError(t, err)
}

func TestCOV_CLI_066_FixRunRecordStoreReadAllHandlesCorruptLines(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, ".docsync", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	recordPath := filepath.Join(stateDir, "fix-run-records.jsonl")
	require.NoError(t, os.WriteFile(recordPath, []byte("not-json\n"), 0o644))
	store := NewFixRunRecordStore(stateDir)
	assert.False(t, store.Has("any-run-id"))
	require.NoError(t, os.WriteFile(recordPath, []byte(""), 0o644))
	assert.False(t, store.Has("any-run-id"))
	require.NoError(t, store.Write(FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: "run-1"}))
	assert.True(t, store.Has("run-1"))
}

func TestCOV_CLI_067_FileIncidentSinkRecordMkdirFailureSurfaceable(t *testing.T) {
	blocker := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(blocker, "blocker"), []byte("x"), 0o644))
	sink := newFileIncidentSink(filepath.Join(blocker, "blocker", "incidents.jsonl"))
	err := sink.Record("at", "run", "rule", "path", "detail", "doc")
	require.Error(t, err)
}

/* ------------------------------------------------- commands_init.go remaining lanes */

func TestCOV_CLI_068_InitExistingInvalidConfigWithoutForceRefuses(t *testing.T) {
	root := t.TempDir()
	docsyncDir := filepath.Join(root, ".docsync")
	require.NoError(t, os.MkdirAll(docsyncDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(docsyncDir, "manifest.yml"), []byte("version: [1,\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(docsyncDir, "models.yml"), []byte("schema: docsync.models/v1\nversion: 1\nroles:\n  decision-engine:\n    model: m\n  judge:\n    model: m\n  writer:\n    model: m\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(docsyncDir, "instructions.md"), []byte("# stub\n"), 0o644))
	result := RunInit(InitOptions{RepoRoot: root})
	assert.Equal(t, ExitConfig, result.ExitCode)
	messages := strings.Join(result.Messages, "\n")
	assert.Contains(t, messages, "refusing to overwrite existing")
	assert.Contains(t, messages, ".docsync/manifest.yml")
}

func TestCOV_CLI_069_InitPostWriteHappyPath(t *testing.T) {
	root := t.TempDir()
	result := RunInit(InitOptions{RepoRoot: root})
	assert.Equal(t, ExitOK, result.ExitCode)
}

/* ------------------------------------------------- commands_fix.go remaining lanes */

func TestCOV_CLI_070_IsWritableModeStrAndIsProtectedModeStrLanes(t *testing.T) {
	assert.True(t, isWritableModeStr("suggest"))
	assert.True(t, isWritableModeStr("auto"))
	assert.False(t, isWritableModeStr("locked"))
	assert.False(t, isWritableModeStr("mystery"))
	assert.True(t, isProtectedModeStr("locked"))
	assert.True(t, isProtectedModeStr("generated"))
	assert.True(t, isProtectedModeStr("external"))
	assert.False(t, isProtectedModeStr("suggest"))
	assert.False(t, isProtectedModeStr("mystery"))
}

/* ------------------------------------------------- commands_pr.go remaining lanes */

func TestCOV_CLI_071_ResolveChangePrSourcePrUnresolvableEmitsError(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	arg42 := 42
	rec := &covRecorder{}
	sha, ok := resolveChangePr(context.Background(), git, &ParsedInvocation{SourcePr: &arg42}, rec)
	assert.False(t, ok)
	assert.Equal(t, "", sha)
	assert.Contains(t, rec.err(), "could not resolve pull/42/head:")
}

func TestCOV_CLI_072_ResolveChangePrDefaultBranchResolvesByOriginHead(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	rec := &covRecorder{}
	sha, ok := resolveChangePr(context.Background(), git, &ParsedInvocation{}, rec)
	assert.True(t, ok)
	assert.Empty(t, rec.err())
	assert.Equal(t, "", sha, "no origin/HEAD → empty fallback (the core flow treats as dry-run)")
}

func TestCOV_CLI_073_ResolveChangePrDefaultBranchFlagUsesIt(t *testing.T) {
	repo := covSeedRepo(t)
	git, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	branch := "main"
	rec := &covRecorder{}
	sha, ok := resolveChangePr(context.Background(), git, &ParsedInvocation{DefaultBranch: &branch}, rec)
	assert.True(t, ok)
	assert.Empty(t, rec.err())
	assert.NotEmpty(t, sha)
}

/* ------------------------------------------------- resolveOfflineExitCode lanes */

func TestCOV_CLI_074_ResolveOfflineExitCodeAnalysisInconclusiveIsDrift(t *testing.T) {
	assert.Equal(t, ExitOK, resolveOfflineExitCode(&schemas.RunOutcome{}))
	assert.Equal(t, ExitDrift, resolveOfflineExitCode(&schemas.RunOutcome{Documents: []schemas.RunDocument{{DocPath: "x"}}}))
	assert.Equal(t, ExitDrift, resolveOfflineExitCode(&schemas.RunOutcome{
		Degradations: []schemas.Degradation{{Kind: schemas.DegradationAnalyzerInconclusive}}}))
}

/* ------------------------------------------------- flow_runtime.go remaining lanes */

func TestCOV_CLI_075_NightlyWorkflowPresentFalseForMissingDir(t *testing.T) {
	assert.False(t, nightlyWorkflowPresent(t.TempDir()))
}

/* ------------------------------------------------------- additional error lanes */

func TestCOV_CLI_076_ReadOptionalFilePermissionErrorFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o000))
	_, _, err := readOptionalFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read")
}

func TestCOV_CLI_077_FixRunRecordStoreWriteFileAppendError(t *testing.T) {
	stateDir := t.TempDir()
	store := NewFixRunRecordStore(stateDir)
	// Make the state dir read-only so OpenFile for append fails.
	require.NoError(t, os.Chmod(stateDir, 0o444))
	err := store.Write(FixRunRecord{Schema: "docsync.fix-run-record/v1", RunID: "run-1"})
	require.Error(t, err)
	os.Chmod(stateDir, 0o755)
}
