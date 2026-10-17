package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
)

// bt is a single backtick character (raw string literal).
const bt = "`"

// TestCOV_TSTOOL_SkipTemplateSubstitutionBranches hits skipTemplateSubstitution
// branches: escape, newline, string, line/block comment, brace depth
// (symbols.go:1369-1393). The line-comment case is placed last so its comment
// (which runs to end of line) does not bleed into subsequent declarations.
func TestCOV_TSTOOL_SkipTemplateSubstitutionBranches(t *testing.T) {
	src := "export const t1 = " + bt + "${'str'}" + bt + ";\n" +
		"export const t2 = " + bt + "${" + "\n" + "}" + bt + ";\n" +
		"export const t3 = " + bt + "${/* c */}" + bt + ";\n" +
		"export const t4 = " + bt + "${// c" + "\n" + "}" + bt + ";\n"
	table, err := tstool.ExtractSymbols(src)
	require.NoError(t, err)
	names := kindNameListLexer2(table)
	assert.Contains(t, names, "variable:t1")
	assert.Contains(t, names, "variable:t2")
	assert.Contains(t, names, "variable:t3")
	assert.Contains(t, names, "variable:t4")
}

// TestCOV_TSTOOL_ArrowAheadBranches hits arrowAhead's depth/terminator branches
// (symbols.go:1145-1162): '=>' found (arrow), and ',' at depth 0 (not an arrow).
func TestCOV_TSTOOL_ArrowAheadBranches(t *testing.T) {
	src := "export const a = <T,>(x: T) => x;\nexport const c = (x) => x;\nexport const d = (x, y);\n"
	table, err := tstool.ExtractSymbols(src)
	require.NoError(t, err)
	names := kindNameListLexer2(table)
	assert.Contains(t, names, "function:a")
	assert.Contains(t, names, "function:c")
	assert.Contains(t, names, "variable:d")
}

// TestCOV_TSTOOL_RegexAllowedPunct hits regexAllowed punct branches
// (symbols.go:1288-1293): '/' after ')' is division; after '(' allows regex.
func TestCOV_TSTOOL_RegexAllowedPunct(t *testing.T) {
	src := "export const a = foo() / 2;\nexport const b = (/x/);\n"
	table, err := tstool.ExtractSymbols(src)
	require.NoError(t, err)
	names := kindNameListLexer2(table)
	assert.Contains(t, names, "variable:a")
	assert.Contains(t, names, "variable:b")
}

// TestCOV_TSTOOL_ScanStringBranches hits scanString escape + newline
// (symbols.go:1324-1332).
func TestCOV_TSTOOL_ScanStringBranches(t *testing.T) {
	src := "export const s1 = \"a\\\\b\";\nexport const s2 = \"a\\nb\";\n"
	table, err := tstool.ExtractSymbols(src)
	require.NoError(t, err)
	names := kindNameListLexer2(table)
	assert.Contains(t, names, "variable:s1")
	assert.Contains(t, names, "variable:s2")
}

// TestCOV_TSTOOL_SkipAngleBranches hits skipAngle's '{' and ';'-depth0 branches
// (symbols.go:1004-1009).
func TestCOV_TSTOOL_SkipAngleBranches(t *testing.T) {
	src := "export function f<T extends { a: string }>(x: T): void {}\n"
	table, err := tstool.ExtractSymbols(src)
	require.NoError(t, err)
	names := kindNameListLexer2(table)
	assert.Contains(t, names, "function:f")
}

// ---------------------------------------------------------------------------
func kindNameListLexer2(table tstool.SymbolTable) []string {
	out := make([]string, 0, len(table.Symbols))
	for _, s := range table.Symbols {
		out = append(out, string(s.Kind)+":"+s.Name)
	}
	return out
}
