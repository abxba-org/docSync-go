package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// TestCOV_TSTOOL_AfterSideExtractionFailure covers the after-side extraction
// failure path (analyzer.go:335): before succeeds, after fails → stub shape.
// before and after differ in content so both call Extract (no cache hit).
func TestCOV_TSTOOL_AfterSideExtractionFailure(t *testing.T) {
	callCount := 0
	extract := func() (tstool.TypeScriptParser, error) {
		return &conditionalFailingParser{failAfter: &callCount}, nil
	}
	analyzer := tstool.CreateTypeScriptAnalyzer(&tstool.TypeScriptAnalyzerOptions{
		CreateParser: extract,
	})
	// after side contains the __FAIL__ marker; before side does not.
	const modifiedFilePatch = `diff --git a/src/orders/order.ts b/src/orders/order.ts
--- a/src/orders/order.ts
+++ b/src/orders/order.ts
@@ -1,2 +1,2 @@
 export const gone = 1;
-function helper(): void {}
+function helper(): void {}; // __FAIL__
`
	result, err := analyzer.AnalyzeDetailed(analyzerInputLex([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusModified, Patch: strPtrLex(modifiedFilePatch)},
	}))
	require.NoError(t, err)
	assert.Equal(t, tstool.TypeScriptStubNote, *result.Coverage.Note)
	assert.Empty(t, result.Files)
}

type conditionalFailingParser struct {
	failAfter *int
}

func (p *conditionalFailingParser) Extract(source string) (tstool.SymbolTable, error) {
	if p.failAfter != nil {
		*p.failAfter++
		if *p.failAfter >= 2 {
			return tstool.SymbolTable{}, &tstool.TypeScriptParserError{Msg: "after-side extraction failed"}
		}
	}
	return tstool.ExtractSymbols(source)
}

// TestCOV_TSTOOL_SymbolLessKindComparison covers symbolLess's kind comparison
// (analyzer.go:666): same name, different kind, sorted via DiffSymbolTables.
func TestCOV_TSTOOL_SymbolLessKindComparison(t *testing.T) {
	before := mustExtractLex("export const item = 1;\nexport function item(): void {}\n")
	after := mustExtractLex("")
	diff := tstool.DiffSymbolTables(&before, &after)
	// Both symbols are removed; sortSymbols exercised symbolLess with equal
	// name and differing kind (variable vs function).
	assert.Equal(t, []string{"item", "item"}, symbolNamesLex(diff.Removed))
}

// TestCOV_TSTOOL_LexerStress hits many lexer branches at once: string newline
// + escape, template substitution + newline, regex vs division, hex/bigint/
// exponent numbers, get/set accessors, generator method, modifier rewind,
// computed members, interface readonly/computed.
func TestCOV_TSTOOL_LexerStress(t *testing.T) {
	src := `export const hex = 0x1f;
export const big = 123n;
export const exp = 1.5e10;
export const msg = "line one\nline two \"escaped\"";
export const tpl = ` + "`a ${1 + 2} b`" + `;
export const re = /ab+c/;
export const ratio = total / divisor;
export const item = 1;
export function item(): void {};
class C {
  static *gen(): void {}
  private static = 1;
  get x(): number { return 1; }
  set x(v: number) {}
  [computed]: string;
}
interface I {
  readonly id: string;
  get v(): number;
  set v(n: number);
  [key: string]: unknown;
}
`
	table, err := tstool.ExtractSymbols(src)
	require.NoError(t, err)
	names := kindNameListLex(table)
	assert.Contains(t, names, "variable:hex")
	assert.Contains(t, names, "variable:big")
	assert.Contains(t, names, "variable:exp")
	assert.Contains(t, names, "variable:msg")
	assert.Contains(t, names, "variable:tpl")
	assert.Contains(t, names, "variable:re")
	assert.Contains(t, names, "variable:ratio")
	assert.Contains(t, names, "variable:item")
	assert.Contains(t, names, "function:item")
	assert.Contains(t, names, "method:gen")
	assert.Contains(t, names, "method:x")
	assert.Contains(t, names, "property:id")
	assert.Contains(t, names, "method:v")
	assert.NotContains(t, names, "property:computed")
	assert.NotContains(t, names, "property:key")
}

// ---------------------------------------------------------------------------
// local helpers (distinct names)

func analyzerInputLex(files []analysis.AnalyzerInputFile) analysis.AnalyzerInput {
	return analysis.AnalyzerInput{Files: files, DeepAllowed: true}
}

func strPtrLex(s string) *string { return &s }

func mustExtractLex(source string) tstool.SymbolTable {
	table, err := tstool.ExtractSymbols(source)
	if err != nil {
		panic(err)
	}
	return table
}

func kindNameListLex(table tstool.SymbolTable) []string {
	out := make([]string, 0, len(table.Symbols))
	for _, s := range table.Symbols {
		out = append(out, string(s.Kind)+":"+s.Name)
	}
	return out
}

func symbolNamesLex(symbols []tstool.ExtractedSymbol) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, s.Name)
	}
	return out
}
