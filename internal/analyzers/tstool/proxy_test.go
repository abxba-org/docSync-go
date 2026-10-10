// Ported from packages/analyzer-typescript/test/proxy-measurement.test.ts
// (P11-T22; ARCH §16.1 A5; D14) and from the composition test in
// composed.test.ts (E2: the real plugin inside the analyzer composition).
package tstool_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func TestMeasureBehavioralProxies_SignatureHit(t *testing.T) {
	measurement, err := tstool.MeasureBehavioralProxies(
		"export function charge(amount: number): string {\n  return \"x\";\n}\n",
		"export function charge(amount: number, memo: string): string {\n  return \"x\";\n}\n",
	)
	require.NoError(t, err)
	assert.True(t, measurement.ProxyFired)
	assert.Contains(t, measurement.FiredProxies, "signature")
}

func TestMeasureBehavioralProxies_TypeHit(t *testing.T) {
	measurement, err := tstool.MeasureBehavioralProxies(
		"export interface Order {\n  idempotencyKey: string;\n}\n",
		"export interface Order {\n  idempotencyKey: string | null;\n}\n",
	)
	require.NoError(t, err)
	assert.True(t, measurement.ProxyFired)
	// The property's declaration text includes the annotation, so the
	// signature proxy fires alongside the type proxy.
	assert.Equal(t, []string{"signature", "type"}, measurement.FiredProxies)
}

func TestMeasureBehavioralProxies_VisibilityHit(t *testing.T) {
	measurement, err := tstool.MeasureBehavioralProxies(
		"function helper(): void {}\n",
		"export function helper(): void {}\n",
	)
	require.NoError(t, err)
	assert.True(t, measurement.ProxyFired)
	assert.Equal(t, []string{"visibility"}, measurement.FiredProxies)
}

func TestMeasureBehavioralProxies_ApiSurfaceHitOnAddAndRemove(t *testing.T) {
	added, err := tstool.MeasureBehavioralProxies(
		"export function charge(): void {}\n",
		"export function charge(): void {}\nexport function auditLog(): void {}\n",
	)
	require.NoError(t, err)
	assert.Contains(t, added.FiredProxies, "api-surface")

	removed, err := tstool.MeasureBehavioralProxies(
		"export function charge(): void {}\nexport function auditLog(): void {}\n",
		"export function charge(): void {}\n",
	)
	require.NoError(t, err)
	assert.Contains(t, removed.FiredProxies, "api-surface")
}

// TestMeasureBehavioralProxies_ApiSurfaceMissForNonExported pins that internal
// add/remove is not public surface and not a paired signature change either.
func TestMeasureBehavioralProxies_ApiSurfaceMissForNonExported(t *testing.T) {
	measurement, err := tstool.MeasureBehavioralProxies(
		"export function charge(): void {}\n",
		"export function charge(): void {}\nfunction internalHelper(): void {}\n",
	)
	require.NoError(t, err)
	assert.False(t, measurement.ProxyFired)
	assert.Empty(t, measurement.FiredProxies)
}

// TestMeasureBehavioralProxies_BodyOnlyAndCommentMiss pins the declared A5
// recall gap: a pure body-only change and a comment-only change fire NOTHING.
func TestMeasureBehavioralProxies_BodyOnlyAndCommentMiss(t *testing.T) {
	body, err := tstool.MeasureBehavioralProxies(
		"export function total(amount: number): number {\n  return amount + 1;\n}\n",
		"export function total(amount: number): number {\n  return amount + 2;\n}\n",
	)
	require.NoError(t, err)
	assert.False(t, body.ProxyFired)
	assert.Empty(t, body.FiredProxies)
	assert.Empty(t, body.Diff.Added)
	assert.Empty(t, body.Diff.Removed)
	assert.Empty(t, body.Diff.Changed)

	comment, err := tstool.MeasureBehavioralProxies(
		"export function total(amount: number): number {\n  return amount + 1;\n}\n",
		"export function total(amount: number): number {\n  // note\n  return amount + 1;\n}\n",
	)
	require.NoError(t, err)
	assert.False(t, comment.ProxyFired)
	assert.Empty(t, comment.FiredProxies)
}

func TestMeasureBehavioralProxies_AddedFileAndDeterminism(t *testing.T) {
	added, err := tstool.MeasureBehavioralProxies("", "export function charge(): void {}\n")
	require.NoError(t, err)
	require.Len(t, added.Diff.Added, 1)
	assert.Equal(t, []string{"api-surface"}, added.FiredProxies)

	before := "export function charge(amount: number): void {}\n"
	after := "export function charge(amount: number): void {}\nexport function z(): void {}\n"
	first, err := tstool.MeasureBehavioralProxies(before, after)
	require.NoError(t, err)
	second, err := tstool.MeasureBehavioralProxies(before, after)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	sorted := append([]string(nil), first.FiredProxies...)
	sort.Strings(sorted)
	assert.Equal(t, sorted, first.FiredProxies)
	assert.Equal(t, []string{"api-surface"}, first.FiredProxies)

	assert.Contains(t, tstool.BehavioralProxyMeasurementNote, "recall gap")
}

/* ------------------------------------------------- composition (OD-4, E2) */

// leadingPlugin is the fixed 'typescript' plugin at the head of the OD-4 order.
const composedModifiedFilePatch = `diff --git a/src/payments/refund.ts b/src/payments/refund.ts
--- a/src/payments/refund.ts
+++ b/src/payments/refund.ts
@@ -1,5 +1,5 @@
 export interface RefundRequest {
   readonly amount: number;
-  readonly idempotencyKey: string;
+  readonly idempotencyKey: string | null;
 }
`

// genericEcho is the minimal generic-shaped plugin from the TS composition test
// (TS GenericEchoPath — the fixture plugin echoed by the first plugin).
type genericEcho struct{ path string }

func (p genericEcho) Name() schemas.AnalyzerName { return schemas.AnalyzerNameGeneric }

func (p genericEcho) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	noteOK := ""
	files := make([]analysis.PerFileFinding, 0, len(input.Files))
	for _, file := range input.Files {
		if file.Path != p.path {
			continue
		}
		files = append(files, analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: false,
			APIDiff:        false,
			ConfigDiff:     false,
			Notes:          []string{"generic echo: path-level analysis only"},
		})
	}
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameGeneric,
			Status:   schemas.AnalyzerStatusOK,
			Note:     &noteOK,
		},
		Files: files,
	}, nil
}

// TestComposeAnalyzers_WithTheRealTypeScriptAnalyzer ports the E2 composed-path
// test: the schema-level projection survives the field-by-field merge.
func TestComposeAnalyzers_WithTheRealTypeScriptAnalyzer(t *testing.T) {
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		tstool.CreateTypeScriptAnalyzer(),
		genericEcho{path: "src/payments/refund.ts"},
	}, nil)
	require.NoError(t, err)
	run := composed.RunAnalyzers(analysis.AnalyzerInput{
		Files: []analysis.AnalyzerInputFile{{
			Path:   "src/payments/refund.ts",
			Status: schemas.FileStatusModified,
			Patch:  patch(composedModifiedFilePatch),
		}},
		DeepAllowed: true,
	})

	require.Len(t, run.Coverage, 2)
	assert.Equal(t, schemas.AnalyzerNameTypeScript, run.Coverage[0].Analyzer)
	assert.Equal(t, schemas.AnalyzerNameGeneric, run.Coverage[1].Analyzer)
	assert.Equal(t, schemas.AnalyzerStatusOK, run.Coverage[0].Status)
	require.NotNil(t, run.Coverage[0].Note)
	assert.Contains(t, *run.Coverage[0].Note, "symbol-capable")

	finding, ok := findingForPath(run.Files, "src/payments/refund.ts")
	require.True(t, ok)
	assert.True(t, finding.SymbolsChanged)
	assert.Equal(t, &schemas.SymbolsDetail{
		Added:   []string{},
		Removed: []string{},
		Changed: []schemas.SymbolChangeDetail{{
			Name:        "idempotencyKey",
			ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeSignature, schemas.SymbolChangeType},
		}},
	}, finding.SymbolsDetail)
}

// TestComposeAnalyzers_SymbolEvidenceSurvivesAndAbsenceStaysAbsent ports the
// second E2 composition test.
func TestComposeAnalyzers_SymbolEvidenceSurvivesAndAbsenceStaysAbsent(t *testing.T) {
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		tstool.CreateTypeScriptAnalyzer(),
		genericEcho{path: "src/other.md"},
	}, nil)
	require.NoError(t, err)
	run := composed.RunAnalyzers(analysis.AnalyzerInput{
		Files: []analysis.AnalyzerInputFile{
			{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified, Patch: patch(composedModifiedFilePatch)},
			{Path: "src/other.md", Status: schemas.FileStatusModified, Patch: patch("not analyzed")},
		},
		DeepAllowed: true,
	})

	tsFinding, ok := findingForPath(run.Files, "src/payments/refund.ts")
	require.True(t, ok)
	assert.NotNil(t, tsFinding.SymbolsDetail)

	mdFinding, ok := findingForPath(run.Files, "src/other.md")
	require.True(t, ok)
	assert.Nil(t, mdFinding.SymbolsDetail)
	assert.False(t, mdFinding.SymbolsChanged)
}

func findingForPath(findings []analysis.PerFileFinding, path string) (analysis.PerFileFinding, bool) {
	for _, finding := range findings {
		if finding.Path == path {
			return finding, true
		}
	}
	return analysis.PerFileFinding{}, false
}
