package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
)

// TestCOV_TSTOOL_LexerFinalBranches hits the last coverable lexer branches:
// skipTemplateSubstitution nested braces, arrowAhead's unmatched-closing and
// ';' depth-0 false paths, skipAngle's ';' depth-0 return, and
// isFunctionInitializer's generic-arrow-not-arrow path.
func TestCOV_TSTOOL_LexerFinalBranches(t *testing.T) {
	// nested braces inside a template substitution: depth goes 1->2->1->0.
	src := "export const t = " + bt + "${" + bt + "a" + bt + "}" + bt + ";\n"
	table, err := tstool.ExtractSymbols(src)
	require.NoError(t, err)
	assert.Contains(t, kindNameListLexer4(table), "variable:t")

	// arrowAhead: '(' then unmatched ')' at depth 0 → not an arrow → variable.
	table, err = tstool.ExtractSymbols(`export const v = (x, y);\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListLexer4(table), "variable:v")

	// skipAngle ';' at depth 0 inside a type-parameter list.
	table, err = tstool.ExtractSymbols(`export function g<T;>(x: T): void {}\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListLexer4(table), "function:g")

	// isFunctionInitializer generic arrow that isn't actually an arrow.
	table, err = tstool.ExtractSymbols(`export const w = <T>(x);\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListLexer4(table), "variable:w")
}

// ---------------------------------------------------------------------------
func kindNameListLexer4(table tstool.SymbolTable) []string {
	out := make([]string, 0, len(table.Symbols))
	for _, s := range table.Symbols {
		out = append(out, string(s.Kind)+":"+s.Name)
	}
	return out
}
