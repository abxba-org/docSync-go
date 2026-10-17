package tstool

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTSCov_extract_symbols(t *testing.T) {
	table, err := ExtractSymbols("export function add(a: number, b: number): number { return a + b; }")
	require.NoError(t, err)
	assert.NotEmpty(t, table.Symbols)
}

func TestTSCov_exportedHere(t *testing.T) {
	table, err := ExtractSymbols("export function f(): void {}; export const x = 1;")
	require.NoError(t, err)
	require.NotEmpty(t, table.Symbols)
	assert.True(t, table.Symbols[0].Exported)
}

func TestTSCov_lastEnd(t *testing.T) {
	parser := newTSParser("const x = 1; const y = 2;")
	assert.GreaterOrEqual(t, parser.lastEnd(), 0)
}

func TestTSCov_skipDecorator_complex(t *testing.T) {
	_, err := ExtractSymbols("@Component({ selector: 'x', inputs: ['a', 'b'] }) export class Widget {}")
	require.NoError(t, err)
}

func TestTSCov_parseExport_default(t *testing.T) {
	_, err := ExtractSymbols("export default class MainComp {}")
	require.NoError(t, err)
}

func TestTSCov_parseExport_namespace(t *testing.T) {
	_, err := ExtractSymbols("export * from './mod'; export { a, b as c } from './mod2';")
	require.NoError(t, err)
}

func TestTSCov_parseFunctionDecl_async(t *testing.T) {
	_, err := ExtractSymbols("export async function* gen(): AsyncIterableIterator<string> { yield 'x'; }")
	require.NoError(t, err)
}

func TestTSCov_parseInterface_decl(t *testing.T) {
	_, err := ExtractSymbols("export interface Config { url: string; port?: number; readonly retries: number }")
	require.NoError(t, err)
}

func TestTSCov_parseTypeAlias_generics(t *testing.T) {
	_, err := ExtractSymbols("type Deep<T, U extends T> = { [K in keyof T]: T[K] };")
	require.NoError(t, err)
}

func TestTSCov_parseEnum_assigned(t *testing.T) {
	_, err := ExtractSymbols("enum Color { Red = 1, Green = 2, Blue = 4 }")
	require.NoError(t, err)
	_, err = ExtractSymbols("const enum Level { A = 'a', B = 'b' }")
	require.NoError(t, err)
}

func TestTSCov_parseVariable_destructure(t *testing.T) {
	_, err := ExtractSymbols("const { a, b } = obj; const [x, y] = arr;")
	require.NoError(t, err)
}

func TestTSCov_parseVariable_arrow(t *testing.T) {
	_, err := ExtractSymbols("const handler = (event: Event): void => { event.preventDefault(); };")
	require.NoError(t, err)
}

func TestTSCov_parseClass_members(t *testing.T) {
	_, err := ExtractSymbols("class Service { private cache = new Map(); #privateField = 1; static create(): Service { return new Service(); } }")
	require.NoError(t, err)
}

func TestTSCov_typeAnnotation_union(t *testing.T) {
	_, err := ExtractSymbols("let value: string | number | null = null;")
	require.NoError(t, err)
}

func TestTSCov_skipTypeAtom(t *testing.T) {
	_, err := ExtractSymbols("function f(x: Array<Map<string, number>>): void {}")
	require.NoError(t, err)
}

func TestTSCov_arrowAhead(t *testing.T) {
	_, err := ExtractSymbols("if (x) { y = (a, b) => a + b; }")
	require.NoError(t, err)
}

func TestTSCov_scanStrings(t *testing.T) {
	_, err := ExtractSymbols("const s1 = 'it\\'s'; const s2 = \"double\\\"quote\"; const escape = `template ${x}`;")
	require.NoError(t, err)
}

func TestTSCov_scanRegex(t *testing.T) {
	_, err := ExtractSymbols("const re = /[a-z]+/gi; const re2 = /\\/escaped\\//;")
	require.NoError(t, err)
}

func TestTSCov_scanNumbers(t *testing.T) {
	_, err := ExtractSymbols("const n1 = 0xFF; const n2 = 0b1010; const n3 = 1.5e10; const n4 = 123n;")
	require.NoError(t, err)
}

func TestTSCov_MeasureBehavioralProxies(t *testing.T) {
	result, err := MeasureBehavioralProxies(`function add(a: number, b: number): number { return a + b; }`, `function add(a: number, b: number): number { return a * b; }`)
	require.NoError(t, err)
	require.NotNil(t, result)

	_, err2 := MeasureBehavioralProxies("", "")
	require.NoError(t, err2)
}

func TestTSCov_beforeTableOf(t *testing.T) {
	before, err := ExtractSymbols("function f(): void {}")
	require.NoError(t, err)
	table := beforeTableOf(&before)
	assert.NotEmpty(t, table.Symbols)
}
