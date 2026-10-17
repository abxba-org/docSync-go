package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
)

// TestCOV_TSTOOL_ParserAndLexerTail hits remaining coverable branches:
// skipAngle ';' depth-0 (symbols.go:1007), skipTypeAtom object '{' (1078),
// parseInterfaceDecl no-ident (568), parseTypeAlias no-name / no-= (603/611),
// parseEnum (627), scanNumber octal/exponent-sign/binary branches.
func TestCOV_TSTOOL_ParserAndLexerTail(t *testing.T) {
	// interface without a name → skipped (no symbol).
	table, err := tstool.ExtractSymbols(`interface {}\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	// type alias without a name → skipped.
	table, err = tstool.ExtractSymbols(`type = 1;\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	// type alias without '=' → skipped.
	table, err = tstool.ExtractSymbols(`type Foo;\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	// enum without a name → skipped.
	table, err = tstool.ExtractSymbols(`enum {}\n`)
	require.NoError(t, err)
	assert.Empty(t, table.Symbols)

	// type parameter list containing an object type literal '{...}'.
	table, err = tstool.ExtractSymbols(`export function f<T extends { a: string }>(x: T): void {}\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListLexer3(table), "function:f")

	// number forms (each in its own source to avoid lexer edge interactions).
	table, err = tstool.ExtractSymbols(`export const o = 0o77;\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListLexer3(table), "variable:o")
	table, err = tstool.ExtractSymbols(`export const e = 1e+5;\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListLexer3(table), "variable:e")
	table, err = tstool.ExtractSymbols(`export const n = 0b11;\n`)
	require.NoError(t, err)
	assert.Contains(t, kindNameListLexer3(table), "variable:n")
}

// ---------------------------------------------------------------------------
func kindNameListLexer3(table tstool.SymbolTable) []string {
	out := make([]string, 0, len(table.Symbols))
	for _, s := range table.Symbols {
		out = append(out, string(s.Kind)+":"+s.Name)
	}
	return out
}
