// Ported from packages/analyzer-typescript/test/analyzer.test.ts (the real
// analyzer, E1b/E2), analyzer-typescript-stub.test.ts (P1-T06, V-P1-13) and
// smoke.test.ts. Black-box over the exported plugin.
package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ANCHOR: the ported TS fixtures (byte-identical patch texts).

const addedFilePatch = `diff --git a/src/orders/order.ts b/src/orders/order.ts
new file mode 100644
--- /dev/null
+++ b/src/orders/order.ts
@@ -0,0 +1,4 @@
+export interface Order {
+  idempotencyKey: string;
+  total(): number;
+}
`

const modifiedFilePatch = `diff --git a/src/orders/order.ts b/src/orders/order.ts
--- a/src/orders/order.ts
+++ b/src/orders/order.ts
@@ -1,4 +1,4 @@
 export interface Order {
-  idempotencyKey: string;
+  idempotencyKey: string | null;
   total(): number;
 }
`

const deletedFilePatch = `diff --git a/src/gone.ts b/src/gone.ts
deleted file mode 100644
--- a/src/gone.ts
+++ /dev/null
@@ -1,2 +0,0 @@
-export const gone = 1;
-function helper(): void {}
`

// analyzerInput is the port of the test's input() helper.
func analyzerInput(files []analysis.AnalyzerInputFile, deepAllowed ...bool) analysis.AnalyzerInput {
	allowed := true
	if len(deepAllowed) > 0 {
		allowed = deepAllowed[0]
	}
	return analysis.AnalyzerInput{Files: files, DeepAllowed: allowed}
}

func patch(patch string) *string { return &patch }

func symbolsDetail(
	added []string,
	removed []string,
	changed []schemas.SymbolChangeDetail,
) *schemas.SymbolsDetail {
	if added == nil {
		added = []string{}
	}
	if removed == nil {
		removed = []string{}
	}
	if changed == nil {
		changed = []schemas.SymbolChangeDetail{}
	}
	return &schemas.SymbolsDetail{Added: added, Removed: removed, Changed: changed}
}

func TestCreateTypeScriptAnalyzer_RealAnalyzerAndDeclaredFallback(t *testing.T) {
	analyzer := tstool.CreateTypeScriptAnalyzer()
	result, err := analyzer.AnalyzeDetailed(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusAdded, Patch: patch(addedFilePatch)},
		{Path: "src/orders/readme.md", Status: schemas.FileStatusModified, Patch: patch("not analyzed")},
	}))
	require.NoError(t, err)

	// The schema's analyzed status — the ANALYZER_STATUS enum has no 'analyzed'.
	assert.Equal(t, schemas.AnalyzerNameTypeScript, result.Coverage.Analyzer)
	assert.Equal(t, schemas.AnalyzerStatusOK, result.Coverage.Status)
	require.NotNil(t, result.Coverage.Note)
	assert.Contains(t, *result.Coverage.Note,
		"analyzed: tree-sitter symbol extraction over 1 .ts/.tsx file(s)")
	// P11-T05: the coverage row DECLARES the analyzer's symbol capability.
	assert.Contains(t, *result.Coverage.Note, "symbol-capable")

	require.Len(t, result.Files, 1)
	finding := result.Files[0]
	assert.Equal(t, "src/orders/order.ts", finding.Path)
	assert.True(t, finding.SymbolsChanged)
	assert.True(t, finding.APIDiff)
	assert.False(t, finding.ConfigDiff)
	assert.False(t, finding.DeepDeclined)
	assert.Equal(t, []string{"Order", "idempotencyKey", "total"}, finding.Symbols)
	// Added file: the before side is empty → every symbol reported as added.
	require.NotNil(t, finding.SymbolDiff)
	assert.Equal(t, []string{"Order", "idempotencyKey", "total"}, symbolNames(finding.SymbolDiff.Added))
	assert.Empty(t, finding.SymbolDiff.Removed)
	assert.Empty(t, finding.SymbolDiff.Changed)
	assert.Equal(t, symbolsDetail([]string{"Order", "idempotencyKey", "total"}, nil, nil), finding.SymbolsDetail)
	require.Len(t, finding.Notes, 1)
	assert.Equal(t,
		"full after-side reconstructed from the patch (added file); the before side is empty — every extracted symbol is reported as added",
		finding.Notes[0])

	// The plugin-shape projection keeps the same evidence (the rich diff stays
	// available through AnalyzeDetailed — see the module doc).
	output, err := analyzer.Analyze(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusAdded, Patch: patch(addedFilePatch)},
	}))
	require.NoError(t, err)
	require.Len(t, output.Files, 1)
	assert.Equal(t, []string{"Order", "idempotencyKey", "total"}, output.Files[0].Symbols)
	assert.Equal(t, symbolsDetail([]string{"Order", "idempotencyKey", "total"}, nil, nil), output.Files[0].SymbolsDetail)

	// Fallback: an injected parser-gate failure degrades to the EXACT stub
	// shape — declared via coverage, never silent (D14).
	failing := tstool.CreateTypeScriptAnalyzer(&tstool.TypeScriptAnalyzerOptions{
		CreateParser: func() (tstool.TypeScriptParser, error) {
			return nil, tstool.NewTypeScriptParserError("wasm unavailable (test injection)")
		},
	})
	degraded, err := failing.AnalyzeDetailed(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusAdded, Patch: patch(addedFilePatch)},
	}))
	require.NoError(t, err)
	stub := tstool.CreateTypeScriptAnalyzerStub()
	stubOutput, err := stub.Analyze(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusAdded, Patch: patch(addedFilePatch)},
	}))
	require.NoError(t, err)
	assert.Equal(t, stubOutput.Coverage, degraded.Coverage)
	assert.Equal(t, schemas.AnalyzerStatusStub, degraded.Coverage.Status)
	require.NotNil(t, degraded.Coverage.Note)
	assert.Equal(t, tstool.TypeScriptStubNote, *degraded.Coverage.Note)
	assert.Empty(t, degraded.Files)
}

// TestV_P11_1_ModifiedFilePatchYieldsFullSymbolDiff ports the V-P11-1
// groundwork test: a modified-file patch flipping a type annotation yields a
// FULL symbol diff with changeKind type on the changed symbol — zero LLM calls.
func TestV_P11_1_ModifiedFilePatchYieldsFullSymbolDiff(t *testing.T) {
	analyzer := tstool.CreateTypeScriptAnalyzer()
	result, err := analyzer.AnalyzeDetailed(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusModified, Patch: patch(modifiedFilePatch)},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.AnalyzerStatusOK, result.Coverage.Status)
	require.Len(t, result.Files, 1)
	finding := result.Files[0]
	assert.True(t, finding.SymbolsChanged)
	require.NotNil(t, finding.SymbolDiff)
	assert.Empty(t, finding.SymbolDiff.Added)
	assert.Empty(t, finding.SymbolDiff.Removed)
	require.Len(t, finding.SymbolDiff.Changed, 1)
	change := finding.SymbolDiff.Changed[0]
	assert.Equal(t, "idempotencyKey", change.Name)
	assert.Equal(t, tstool.SymbolKindProperty, change.Kind)
	// The signature text spans the annotation, so the widening also surfaces as
	// a signature delta; the semantic point is the 'type' changeKind.
	assert.Contains(t, change.ChangeKinds, schemas.SymbolChangeType)
	assert.Equal(t, []string{": string"}, change.Before.TypeAnnotations)
	assert.Equal(t, []string{": string | null"}, change.After.TypeAnnotations)
	assert.Equal(t, symbolsDetail(nil, nil, []schemas.SymbolChangeDetail{{
		Name:        "idempotencyKey",
		ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeSignature, schemas.SymbolChangeType},
	}}), finding.SymbolsDetail)
	require.Len(t, finding.Notes, 1)
	assert.Equal(t,
		"both sides reconstructed from the unified diff (added/removed/context lines) — the symbol diff covers declarations fully contained in the hunk windows; declarations partially outside the diff context are invisible to both sides equally (declared, D14)",
		finding.Notes[0])

	// A whole new declaration added to a modified file is reported as ADDED.
	const withNewFunction = `diff --git a/src/orders/order.ts b/src/orders/order.ts
--- a/src/orders/order.ts
+++ b/src/orders/order.ts
@@ -3,2 +3,6 @@
 }
+
+export function cancel(orderId: string): void {
+  void orderId;
+}
`
	result2, err := analyzer.AnalyzeDetailed(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusModified, Patch: patch(withNewFunction)},
	}))
	require.NoError(t, err)
	finding2 := result2.Files[0]
	assert.True(t, finding2.SymbolsChanged)
	assert.Equal(t, []string{"cancel"}, symbolNames(finding2.SymbolDiff.Added))
	assert.Empty(t, finding2.SymbolDiff.Removed)
	assert.True(t, finding2.APIDiff)
	require.NotNil(t, finding2.SymbolsDetail)
	assert.Equal(t, []string{"cancel"}, finding2.SymbolsDetail.Added)
}

// TestDeletedFilesAndUnderivablePatches ports the deleted-file / no-hunks /
// no-patch test (D14: a fabricated empty negative is never emitted).
func TestDeletedFilesAndUnderivablePatches(t *testing.T) {
	analyzer := tstool.CreateTypeScriptAnalyzer()
	result, err := analyzer.AnalyzeDetailed(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/gone.ts", Status: schemas.FileStatusDeleted, Patch: patch(deletedFilePatch)},
		{Path: "src/broken.ts", Status: schemas.FileStatusDeleted, Patch: patch("some deleted-content patch")},
		{Path: "src/mystery.ts", Status: schemas.FileStatusModified},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.AnalyzerStatusOK, result.Coverage.Status)
	require.Len(t, result.Files, 3)

	deleted := result.Files[0]
	assert.False(t, deleted.DeepDeclined)
	assert.True(t, deleted.SymbolsChanged)
	assert.True(t, deleted.APIDiff) // the removed `gone` was exported
	assert.Empty(t, deleted.SymbolDiff.Added)
	assert.Equal(t, []string{"gone", "helper"}, symbolNames(deleted.SymbolDiff.Removed))
	assert.Empty(t, deleted.SymbolDiff.Changed)
	require.NotNil(t, deleted.SymbolsDetail)
	assert.Equal(t, []string{"gone", "helper"}, deleted.SymbolsDetail.Removed)
	require.Len(t, deleted.Notes, 1)
	assert.Contains(t, deleted.Notes[0], "file deleted")

	// No hunks in the patch → NOTHING is derivable from either side; the
	// finding declines deep analysis instead of emitting a fabricated empty
	// negative (D14).
	broken := result.Files[1]
	assert.True(t, broken.DeepDeclined)
	assert.False(t, broken.SymbolsChanged)
	assert.Nil(t, broken.SymbolDiff)
	assert.Nil(t, broken.SymbolsDetail)
	assert.True(t, len(broken.Notes) > 0)
	assert.Contains(t, broken.Notes[0], "no hunks")

	noPatch := result.Files[2]
	assert.True(t, noPatch.DeepDeclined)
	assert.True(t, len(noPatch.Notes) > 0)
	assert.Contains(t, noPatch.Notes[0], "no unified diff provided")
}

// TestDeepNotPermittedDowngradesToStub ports the ARCH §3.7 downgrade test.
func TestDeepNotPermittedDowngradesToStub(t *testing.T) {
	analyzer := tstool.CreateTypeScriptAnalyzer()
	result, err := analyzer.AnalyzeDetailed(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusAdded, Patch: patch(addedFilePatch)},
	}, false))
	require.NoError(t, err)

	assert.Equal(t, schemas.AnalyzerStatusStub, result.Coverage.Status)
	require.NotNil(t, result.Coverage.Note)
	assert.Equal(t, tstool.TypeScriptDeepNotPermittedNote, *result.Coverage.Note)
	assert.Empty(t, result.Files)

	// The plugin shape agrees.
	output, err := analyzer.Analyze(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusAdded, Patch: patch(addedFilePatch)},
	}, false))
	require.NoError(t, err)
	assert.Equal(t, schemas.AnalyzerStatusStub, output.Coverage.Status)
	assert.Empty(t, output.Files)
}

// TestCachesBothSideSymbolTablesByContentSha256 ports the cache/determinism
// test: purity forbids fs/git, so the cache key is a content hash.
func TestCachesBothSideSymbolTablesByContentSha256(t *testing.T) {
	analyzer := tstool.CreateTypeScriptAnalyzer()
	files := []analysis.AnalyzerInputFile{
		{Path: "src/a.ts", Status: schemas.FileStatusAdded, Patch: patch(addedFilePatch)},
		{Path: "src/b.ts", Status: schemas.FileStatusAdded, Patch: patch(addedFilePatch)}, // same content, other path
	}
	first, err := analyzer.Analyze(analyzerInput(files))
	require.NoError(t, err)
	// Two extractions per unique content (empty before side + after side).
	assert.Equal(t, tstool.SymbolIndexCacheStats{Builds: 2, CacheHits: 2}, analyzer.Stats())

	second, err := analyzer.Analyze(analyzerInput(files))
	require.NoError(t, err)
	assert.Equal(t, tstool.SymbolIndexCacheStats{Builds: 2, CacheHits: 6}, analyzer.Stats())
	assert.Equal(t, first, second)
	assert.Equal(t, first.Files[0].Symbols, second.Files[0].Symbols)
}

// TestReconstructsBothSidesFromHunks ports the afterTextFromPatch test.
func TestReconstructsBothSidesFromHunks(t *testing.T) {
	assert.Equal(t, "export interface Order {\n  idempotencyKey: string;\n  total(): number;\n}",
		tstool.AfterTextFromPatch(addedFilePatch))
	assert.Equal(t, "export interface Order {\n  idempotencyKey: string | null;\n  total(): number;\n}",
		tstool.AfterTextFromPatch(modifiedFilePatch))
	assert.Equal(t, "", tstool.AfterTextFromPatch("no hunks here"))

	// The before side is the exact mirror (removed + context lines).
	assert.Equal(t, "export interface Order {\n  idempotencyKey: string;\n  total(): number;\n}",
		tstool.BeforeTextFromPatch(modifiedFilePatch))
	assert.Equal(t, "", tstool.BeforeTextFromPatch(addedFilePatch))
}

/* ---------------------------------------------- the stub (P1-T06, V-P1-13) */

func stubSampleInput() analysis.AnalyzerInput {
	return analysis.AnalyzerInput{
		Files: []analysis.AnalyzerInputFile{
			{
				Path:   "src/payments/refund.ts",
				Status: schemas.FileStatusModified,
				Patch:  patch("@@ -1,3 +1,4 @@\n export function refundInvoice(): boolean {\n+  return true;\n+}\n"),
			},
			{Path: "src/payments/refund.test.ts", Status: schemas.FileStatusAdded},
		},
		DeepAllowed: true,
		Notes:       []string{"sample run for stub contract tests"},
	}
}

func TestTypeScriptStub_SatisfiesPluginContract(t *testing.T) {
	assert.Equal(t, schemas.AnalyzerNameTypeScript, tstool.TypeScriptAnalyzerStub.Name())

	output, err := tstool.TypeScriptAnalyzerStub.Analyze(stubSampleInput())
	require.NoError(t, err)
	assert.Empty(t, output.Files)
	assert.Equal(t, schemas.AnalyzerNameTypeScript, output.Coverage.Analyzer)
	assert.Equal(t, schemas.AnalyzerStatusStub, output.Coverage.Status)
	require.NotNil(t, output.Coverage.Note)
	assert.NotEmpty(t, *output.Coverage.Note)

	// Interface compliance is compile-time; the registry name is the contract.
	var _ analysis.AnalyzerPlugin = tstool.TypeScriptStubPlugin{}
	var _ analysis.AnalyzerPlugin = tstool.CreateTypeScriptAnalyzer()
}

func TestTypeScriptStub_Deterministic(t *testing.T) {
	first, err := tstool.CreateTypeScriptAnalyzerStub().Analyze(stubSampleInput())
	require.NoError(t, err)
	second, err := tstool.CreateTypeScriptAnalyzerStub().Analyze(stubSampleInput())
	require.NoError(t, err)
	assert.Equal(t, first, second)

	// Purity: a structurally identical input yields the same output.
	same, err := tstool.TypeScriptAnalyzerStub.Analyze(stubSampleInput())
	require.NoError(t, err)
	assert.Equal(t, first, same)

	// deepAllowed is irrelevant for the declared stub shape.
	notAllowed := stubSampleInput()
	notAllowed.DeepAllowed = false
	other, err := tstool.TypeScriptAnalyzerStub.Analyze(notAllowed)
	require.NoError(t, err)
	assert.Equal(t, first, other)
}

func TestTypeScriptStub_DeclaredCoverage(t *testing.T) {
	output, err := tstool.CreateTypeScriptAnalyzerStub().Analyze(stubSampleInput())
	require.NoError(t, err)
	require.NotNil(t, output.Coverage.Note)
	note := *output.Coverage.Note
	assert.Equal(t, tstool.TypeScriptStubNote, note)
	// The note must name the Phase 11 upgrade path so the gap is actionable.
	assert.Contains(t, note, "Phase 11")
	assert.Contains(t, note, "F11-1")
}

// TestTypeScriptStubNote_CrossCopyDriftGuard keeps the three byte-equal copies
// of the stub note in sync (the analyzer package's source, the plugin seam and
// the schema example — the analysis-level guard extends to this literal).
func TestTypeScriptStubNote_CrossCopyDriftGuard(t *testing.T) {
	assert.Equal(t, analysis.TypeScriptStubNote, tstool.TypeScriptStubNote)
	for _, entry := range schemas.ChangeAnalysisExample.AnalyzerCoverage {
		if entry.Analyzer == schemas.AnalyzerNameTypeScript && entry.Status == schemas.AnalyzerStatusStub {
			require.NotNil(t, entry.Note)
			assert.Equal(t, tstool.TypeScriptStubNote, *entry.Note)
			return
		}
	}
	t.Fatal("the schema example carries no 'typescript' stub coverage entry")
}

// TestDeviationNoteDeclared pins the DEV-1 declaration (plan.md §6): the Go
// port substitutes an equivalent lightweight parser for web-tree-sitter and says
// so, never silently.
func TestDeviationNoteDeclared(t *testing.T) {
	assert.Contains(t, tstool.LightweightParserDeviationNote, "DEV-1")
	assert.Contains(t, tstool.LightweightParserDeviationNote, "lightweight TypeScript/TSX parser")
}

// TestExposesAVersion ports the smoke suite.
func TestExposesAVersion(t *testing.T) {
	assert.Equal(t, "0.0.0", tstool.AnalyzerTypeScriptPackageVersion)
}

// TestNoTypeScriptFiles_DeclaredCoverageNote ports the "nothing to extract"
// coverage note branch of analyzedCoverageNote.
func TestNoTypeScriptFiles_DeclaredCoverageNote(t *testing.T) {
	analyzer := tstool.CreateTypeScriptAnalyzer()
	result, err := analyzer.AnalyzeDetailed(analyzerInput([]analysis.AnalyzerInputFile{
		{Path: "yarn.lock", Status: schemas.FileStatusModified, Patch: patch("diff --git a/yarn.lock b/yarn.lock")},
		{Path: "src/readme.md", Status: schemas.FileStatusModified, Patch: patch("not analyzed")},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.AnalyzerStatusOK, result.Coverage.Status)
	require.NotNil(t, result.Coverage.Note)
	assert.Contains(t, *result.Coverage.Note, "analyzed: no .ts/.tsx files in the input")
	assert.Empty(t, result.Files)
}

// TestTypeScriptParserErrorMessagePreserved pins the injected parser-gate error
// (the TS assertion observed the message of the rejected parser promise).
func TestTypeScriptParserErrorMessagePreserved(t *testing.T) {
	err := tstool.NewTypeScriptParserError("wasm unavailable (test injection)")
	assert.Equal(t, "wasm unavailable (test injection)", err.Error())

	var parserErr *tstool.TypeScriptParserError
	require.ErrorAs(t, err, &parserErr)
}

// TestSymbolDetailProjectionOfNilDiff pins the nil-diff projection (an analyzer
// never emits a nil projection, so the schema shape stays valid).
func TestSymbolDetailProjectionOfNilDiff(t *testing.T) {
	detail := tstool.SymbolsDetailOf(nil)
	require.NotNil(t, detail)
	assert.NotNil(t, detail.Added)
	assert.NotNil(t, detail.Removed)
	assert.NotNil(t, detail.Changed)
	assert.Empty(t, detail.Added)
	assert.Empty(t, detail.Removed)
	assert.Empty(t, detail.Changed)
	assert.Empty(t, detail.Validate())
}

// symbolNames maps symbols to their names (test helper mirroring the TS
// `.map((s) => s.name)` assertions).
func symbolNames(symbols []tstool.ExtractedSymbol) []string {
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		out = append(out, symbol.Name)
	}
	return out
}
