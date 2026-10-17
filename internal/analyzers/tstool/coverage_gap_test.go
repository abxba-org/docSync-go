package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// TestCOV_TSTOOL_AnalyzerExtractionFailureDegradesToStub covers the fail-closed
// before-side / after-side extraction failure paths (analyzer.go:326, :335).
func TestCOV_TSTOOL_AnalyzerExtractionFailureDegradesToStub(t *testing.T) {
	failingExtract := func() (tstool.TypeScriptParser, error) {
		return &failingParser{}, nil
	}
	analyzer := tstool.CreateTypeScriptAnalyzer(&tstool.TypeScriptAnalyzerOptions{
		CreateParser: failingExtract,
	})

	const addedFilePatch = `diff --git a/src/orders/order.ts b/src/orders/order.ts
new file mode 100644
--- /dev/null
+++ b/src/orders/order.ts
@@ -0,0 +1,2 @@
+export const gone = 1;
+function helper(): void {}
`
	result, err := analyzer.AnalyzeDetailed(analyzerInputCOV([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusAdded, Patch: strPtrCOV(addedFilePatch)},
	}))
	require.NoError(t, err)
	assert.Equal(t, tstool.TypeScriptStubNote, *result.Coverage.Note)
	assert.Empty(t, result.Files)

	const modifiedFilePatch = `diff --git a/src/orders/order.ts b/src/orders/order.ts
--- a/src/orders/order.ts
+++ b/src/orders/order.ts
@@ -1,2 +1,2 @@
 export const gone = 1;
-function helper(): void {}
+function helper(): void {}
`
	result2, err := analyzer.AnalyzeDetailed(analyzerInputCOV([]analysis.AnalyzerInputFile{
		{Path: "src/orders/order.ts", Status: schemas.FileStatusModified, Patch: strPtrCOV(modifiedFilePatch)},
	}))
	require.NoError(t, err)
	assert.Equal(t, tstool.TypeScriptStubNote, *result2.Coverage.Note)
	assert.Empty(t, result2.Files)
}

type failingParser struct{}

func (failingParser) Extract(source string) (tstool.SymbolTable, error) {
	return tstool.SymbolTable{}, &tstool.TypeScriptParserError{Msg: "extraction failed (test injection)"}
}

// TestCOV_TSTOOL_DiffSymbolTablesNilAfter covers afterTableOf(nil) (analyzer.go:697).
func TestCOV_TSTOOL_DiffSymbolTablesNilAfter(t *testing.T) {
	before := mustExtractCOV("export const gone = 1;\n")
	diff := tstool.DiffSymbolTables(&before, nil)
	assert.Empty(t, diff.Added)
	assert.Equal(t, []string{"gone"}, symbolNamesCOV(diff.Removed))
	assert.Empty(t, diff.Changed)
}

// TestCOV_TSTOOL_TypeAnnotationsEqualLengthMismatch covers typeAnnotationsEqual's
// length-mismatch branch (analyzer.go:636).
func TestCOV_TSTOOL_TypeAnnotationsEqualLengthMismatch(t *testing.T) {
	before := mustExtractCOV("function serve(input: string): number { return 1; }\n")
	after := mustExtractCOV("function serve(input: string) { return 1; }\n")
	diff := tstool.DiffSymbolTables(&before, &after)
	require.Len(t, diff.Changed, 1)
	assert.Contains(t, diff.Changed[0].ChangeKinds, schemas.SymbolChangeSignature)
	assert.Contains(t, diff.Changed[0].ChangeKinds, schemas.SymbolChangeType)
}

// TestCOV_TSTOOL_SymbolLessLineComparison covers symbolLess's line comparison
// (analyzer.go:666-669).
func TestCOV_TSTOOL_SymbolLessLineComparison(t *testing.T) {
	before := mustExtractCOV("export function overload(a: string): void;\nexport function overload(a: number): void {}\n")
	after := mustExtractCOV("export function overload(a: number): void {}\n")
	diff := tstool.DiffSymbolTables(&before, &after)
	assert.Equal(t, []string{"overload"}, symbolNamesCOV(diff.Removed))
	assert.Empty(t, diff.Added)
}

// TestCOV_TSTOOL_LexerNewlinesInStrings covers scanString's newline handling
// (symbols.go:1324) and escaped quotes (symbols.go:1329).
func TestCOV_TSTOOL_LexerNewlinesInStrings(t *testing.T) {
	table, err := tstool.ExtractSymbols(`export const msg = "line one\nline two";
export const escaped = "she said \"hi\"";`)
	require.NoError(t, err)
	names := kindNameListCOV(table)
	assert.Contains(t, names, "variable:msg")
	assert.Contains(t, names, "variable:escaped")
}

// TestCOV_TSTOOL_LexerTemplateNewlinesAndSubstitutions covers scanTemplate's
// newline and substitution handling and skipTemplateSubstitution branches.
func TestCOV_TSTOOL_LexerTemplateNewlinesAndSubstitutions(t *testing.T) {
	table, err := tstool.ExtractSymbols("export const tpl = `a ${1 + 2} b ${x.y} c`;\nexport const multi = `first\nsecond`;\n")
	require.NoError(t, err)
	names := kindNameListCOV(table)
	assert.Contains(t, names, "variable:tpl")
	assert.Contains(t, names, "variable:multi")
}

// TestCOV_TSTOOL_LexerNumbers covers scanNumber branches: hex, bigint, exponent
// and isHexDigit.
func TestCOV_TSTOOL_LexerNumbers(t *testing.T) {
	table, err := tstool.ExtractSymbols(`export const hex = 0x1f;
export const bin = 0b1010;
export const big = 123n;
export const exp = 1.5e10;
export const dec = 42;`)
	require.NoError(t, err)
	names := kindNameListCOV(table)
	assert.Contains(t, names, "variable:hex")
	assert.Contains(t, names, "variable:bin")
	assert.Contains(t, names, "variable:big")
	assert.Contains(t, names, "variable:exp")
	assert.Contains(t, names, "variable:dec")
}

// TestCOV_TSTOOL_ParserBranches covers statement-scanner branches: export =,
// async-without-function, no-ident name, class heritage, interface heritage,
// type-alias no-name, variable non-name keyword, interface readonly,
// interface get/set.
func TestCOV_TSTOOL_ParserBranches(t *testing.T) {
	table, err := tstool.ExtractSymbols(`export = ns;\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	table, err = tstool.ExtractSymbols(`export async;\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	table, err = tstool.ExtractSymbols(`export function() {};\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	table, err = tstool.ExtractSymbols(`export class Box<T> extends Base implements Wrapper {}\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListCOV(table), "class:Box")

	table, err = tstool.ExtractSymbols(`export interface Wide<T> extends Base {}\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListCOV(table), "interface:Wide")

	table, err = tstool.ExtractSymbols(`type;\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	table, err = tstool.ExtractSymbols(`const extends = 1;\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	table, err = tstool.ExtractSymbols(`export interface Wide { readonly id: string; get value(): number; set value(n: number); }\n`)
	require.NoError(t, err)
	names := kindNameListCOV(table)
	assert.Contains(t, names, "property:id")
	assert.Contains(t, names, "method:value")
}

// TestCOV_TSTOOL_DivisionVersusRegex covers regexAllowed's punct false-branch
// and scanRegex's newline-termination.
func TestCOV_TSTOOL_DivisionVersusRegex(t *testing.T) {
	table, err := tstool.ExtractSymbols(`export const ratio = total / divisor;
export const found = /ab+c/
export const after = 1;`)
	require.NoError(t, err)
	names := kindNameListCOV(table)
	assert.Contains(t, names, "variable:ratio")
	assert.Contains(t, names, "variable:after")
}

func analyzerInputCOV(files []analysis.AnalyzerInputFile) analysis.AnalyzerInput {
	return analysis.AnalyzerInput{Files: files, DeepAllowed: true}
}

func strPtrCOV(s string) *string { return &s }

func mustExtractCOV(source string) tstool.SymbolTable {
	table, err := tstool.ExtractSymbols(source)
	if err != nil {
		panic(err)
	}
	return table
}

func symbolNamesCOV(symbols []tstool.ExtractedSymbol) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, s.Name)
	}
	return out
}

func kindNameListCOV(table tstool.SymbolTable) []string {
	out := make([]string, 0, len(table.Symbols))
	for _, s := range table.Symbols {
		out = append(out, string(s.Kind)+":"+s.Name)
	}
	return out
}
