// The DEV-1 parser-fidelity suite: the lightweight TypeScript/TSX scanner is
// exercised over the shapes the tree-sitter query documented as in scope
// (top-level declarations plus class/interface members) and out of scope /
// unmatched (re-exports, ambient declarations, namespaces, destructuring,
// computed and string-named members, `#private` fields, index and call
// signatures).
//
// These are NOT ports of a TS test file — the TS suites cover the extractor
// through the analyzer only. They pin the substitution contract itself
// (plan.md §6 DEV-1): same coverage model, same classification, never an
// invented symbol.
package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
)

// symbolSummary renders one symbol as `kind:name(line) exported[signature]`.
func symbolSummary(symbol tstool.ExtractedSymbol) string {
	return string(symbol.Kind) + ":" + symbol.Name + "(" +
		itoaTestHelper(symbol.Line) + ") exported=" +
		boolString(symbol.Exported) + " signature=`" + symbol.Signature + "`"
}

func itoaTestHelper(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func summarize(table tstool.SymbolTable) []string {
	out := make([]string, 0, len(table.Symbols))
	for _, symbol := range table.Symbols {
		out = append(out, symbolSummary(symbol))
	}
	return out
}

func TestParser_TopLevelDeclarationForms(t *testing.T) {
	table, err := tstool.ExtractSymbols(`// leading line comment
/* a block
   comment spanning lines */
import type { Helper } from './helper.js';

export function plain(): void {}

async function asyncFn(): Promise<void> {}

function* generatorFn(): Iterator<number> {}

export function overload(a: string): void;
export function overload(a: number): void {}

export function generic<T extends object>(value: T): T {
  return value;
}

export abstract class AbstractBase {
  abstract run(): void;
  protected readonly size: number = 1;
}

export class Box<T> extends Container<Box<T>> implements Wrapper<T> {
  private static instances: number = 0;

  constructor(readonly content: T) {}

  get value(): T {
    return this.content;
  }

  set value(next: T) {
    this.content = next;
  }

  static build<T>(input: T): Box<T> {
    return new Box(input);
  }

  [computedName](): void {}

  'quoted'(): void {}

  #privatelyOwned: number = 0;

  static {
    Box.instances = 1;
  }
}

const enum Levels {
  Low,
  High,
}

export default class DefaultClass {}

export const arrow = async (x: number): Promise<number> => x + 1;
export const fnExpr = function named(): void {};
export const valueOnly = [1, 2, 3];
const { alpha, beta } = valueOnly;

export interface Wide<T> extends Base<T>, Other {
  [key: string]: unknown;
  call(event: string): void;
  new (): Wide<T>;
  readonly id: string;
  run?(): void;
}

export type Union = string | number | null;
export type Conditional<T> = T extends string ? string[] : T[];
export type Functionish = (a: number, b: string) => void;

declare module 'ambient' {
  export function hidden(): void;
}

namespace Impl {
  export function alsoHidden(): void {}
}

export * from './reexports.js';
export { renamed as publicName } from './named.js';
export type { OnlyTypes } from './types.js';
export = Impl;
`)
	require.NoError(t, err)
	names := kindNameList(table)
	// ---- in-scope top-level declarations
	assert.Contains(t, names, "function:plain")
	assert.Contains(t, names, "function:asyncFn")
	assert.Contains(t, names, "function:generatorFn")
	assert.Contains(t, names, "function:overload") // both overload declarations
	assert.Contains(t, names, "function:generic")
	assert.Contains(t, names, "class:AbstractBase")
	assert.Contains(t, names, "class:Box")
	assert.Contains(t, names, "enum:Levels")
	assert.Contains(t, names, "class:DefaultClass")
	assert.Contains(t, names, "function:arrow")
	assert.Contains(t, names, "function:fnExpr")
	assert.Contains(t, names, "variable:valueOnly")
	assert.Contains(t, names, "interface:Wide")
	assert.Contains(t, names, "type_alias:Union")
	assert.Contains(t, names, "type_alias:Conditional")
	assert.Contains(t, names, "type_alias:Functionish")
	// ---- class members
	assert.Contains(t, names, "property:instances")
	assert.Contains(t, names, "method:run")
	assert.Contains(t, names, "property:size")
	assert.Contains(t, names, "method:build")
	assert.Contains(t, names, "method:value")
	// ---- interface members
	assert.Contains(t, names, "method:call")
	assert.Contains(t, names, "property:id")
	assert.Contains(t, names, "method:run")
	// ---- declared exclusions (must NOT appear)
	assert.NotContains(t, names, "function:hidden")
	assert.NotContains(t, names, "function:alsoHidden")
	assert.NotContains(t, names, "variable:alpha")
	assert.NotContains(t, names, "variable:beta")
	assert.NotContains(t, names, "function:computedName")
	assert.NotContains(t, names, "function:quoted")
	assert.NotContains(t, names, "property:#privatelyOwned")
	assert.NotContains(t, names, "property:key")

	// Signatures elide the body exactly where tree-sitter's body field starts.
	plain := findSymbolIn(t, table, "plain", tstool.SymbolKindFunction)
	assert.Equal(t, 6, plain.Line, "the line number ignores the leading comments")
	assert.Equal(t, "function plain(): void", plain.Signature)
	assert.True(t, plain.Exported)

	asyncFn := findSymbolIn(t, table, "asyncFn", tstool.SymbolKindFunction)
	assert.Equal(t, "async function asyncFn(): Promise<void>", asyncFn.Signature)
	assert.False(t, asyncFn.Exported)

	generated := findSymbolIn(t, table, "generatorFn", tstool.SymbolKindFunction)
	assert.Equal(t, "function* generatorFn(): Iterator<number>", generated.Signature)

	abstractBase := findSymbolIn(t, table, "AbstractBase", tstool.SymbolKindClass)
	assert.Equal(t, "abstract class AbstractBase", abstractBase.Signature)
	assert.True(t, abstractBase.Exported)

	box := findSymbolIn(t, table, "Box", tstool.SymbolKindClass)
	assert.Equal(t, "class Box<T> extends Container<Box<T>> implements Wrapper<T>", box.Signature)

	size := findSymbolIn(t, table, "size", tstool.SymbolKindProperty)
	assert.Equal(t, "protected readonly size: number = 1", size.Signature)
	assert.Equal(t, []string{": number"}, size.TypeAnnotations)

	run := findSymbolIn(t, table, "run", tstool.SymbolKindMethod)
	assert.Equal(t, "abstract run(): void", run.Signature)

	getter := findSymbolIn(t, table, "value", tstool.SymbolKindMethod)
	assert.Equal(t, "get value(): T", getter.Signature)

	build := findSymbolIn(t, table, "build", tstool.SymbolKindMethod)
	assert.Equal(t, "static build<T>(input: T): Box<T>", build.Signature)
	assert.Equal(t, []string{": Box<T>"}, build.TypeAnnotations)

	wide := findSymbolIn(t, table, "Wide", tstool.SymbolKindInterface)
	assert.Equal(t, "interface Wide<T> extends Base<T>, Other", wide.Signature)

	id := findSymbolIn(t, table, "id", tstool.SymbolKindProperty)
	assert.Equal(t, "readonly id: string", id.Signature)
	assert.Equal(t, []string{": string"}, id.TypeAnnotations)

	assert.Equal(t, "type Union = string | number | null;",
		findSymbolIn(t, table, "Union", tstool.SymbolKindTypeAlias).Signature)
	assert.Equal(t, "type Conditional<T> = T extends string ? string[] : T[];",
		findSymbolIn(t, table, "Conditional", tstool.SymbolKindTypeAlias).Signature)
	assert.Equal(t, "type Functionish = (a: number, b: string) => void;",
		findSymbolIn(t, table, "Functionish", tstool.SymbolKindTypeAlias).Signature)

	// The constructor body elides at the statement block, and the parameter
	// property inside its parameter list is NOT a class member (the TS query
	// only matches public_field_definition nodes).
	assert.Equal(t, "constructor(readonly content: T)",
		findSymbolIn(t, table, "constructor", tstool.SymbolKindMethod).Signature)
	assert.NotContains(t, names, "property:content")

	// `export = Impl;` (export-assignment) emits no symbol of its own.
	assert.NotContains(t, names, "variable:Impl")
}

func TestParser_DefaultExportVisibility(t *testing.T) {
	table, err := tstool.ExtractSymbols(`export default function main(): void {}
export default function (): void {}
export default class Named {}
export default 42;
export default class extends Base {}
`)
	require.NoError(t, err)
	// `export default X` is still an export statement → exported is true.
	main := findSymbolIn(t, table, "main", tstool.SymbolKindFunction)
	assert.True(t, main.Exported)
	assert.Equal(t, "function main(): void", main.Signature)

	named := findSymbolIn(t, table, "Named", tstool.SymbolKindClass)
	assert.True(t, named.Exported)
	assert.Equal(t, "class Named", named.Signature)

	// The anonymous default function and the anonymous default class carry no
	// name → no symbol; `export default 42` is not a declaration at all.
	assert.Len(t, table.Symbols, 2)
	_ = table
}

func TestParser_TsxAndLiteralSafety(t *testing.T) {
	table, err := tstool.ExtractSymbols(`export const App = (): JSX.Element => <div className="app">{value}</div>;

export function Component<T>(props: { value: T }): JSX.Element {
  return <li key={String(props.value)}>{props.value}</li>;
}

export const tpl = ` + "`text ${name} and ${count + 1} end`" + `;

export const pattern = /class\s+\w+\s*\{[^}]*\}/g;

export const quoted = "an interface Ghost { } in a string";
export const chained = build().map(fn).filter(Boolean);

export function spread(...args: string[]): number {
  return args.length;
}
`)
	require.NoError(t, err)
	names := kindNameList(table)
	assert.Equal(t, []string{
		"function:App", "function:Component", "variable:tpl", "variable:pattern",
		"variable:quoted", "variable:chained", "function:spread",
	}, names)

	// The arrow-valued declarator reports kind 'function' (the TS query
	// classifies by the initializer's node type).
	app := findSymbolIn(t, table, "App", tstool.SymbolKindFunction)
	assert.Equal(t, `const App = (): JSX.Element => <div className="app">{value}</div>;`,
		app.Signature)

	component := findSymbolIn(t, table, "Component", tstool.SymbolKindFunction)
	assert.Equal(t, "function Component<T>(props: { value: T }): JSX.Element", component.Signature)
	assert.Equal(t, []string{": JSX.Element"}, component.TypeAnnotations)

	// A regex literal containing braces never opens a block, and a string
	// containing an interface never reports a symbol.
	pattern := findSymbolIn(t, table, "pattern", tstool.SymbolKindVariable)
	assert.Equal(t, "const pattern = /class\\s+\\w+\\s*\\{[^}]*\\}/g;", pattern.Signature)
	assert.NotContains(t, names, "interface:Ghost")

	// A template literal with substitutions containing braces is ONE token.
	tpl := findSymbolIn(t, table, "tpl", tstool.SymbolKindVariable)
	assert.Equal(t, "const tpl = `text ${name} and ${count + 1} end`;", tpl.Signature)

	chained := findSymbolIn(t, table, "chained", tstool.SymbolKindVariable)
	assert.Equal(t, "const chained = build().map(fn).filter(Boolean);", chained.Signature)

	spread := findSymbolIn(t, table, "spread", tstool.SymbolKindFunction)
	assert.Equal(t, "function spread(...args: string[]): number", spread.Signature)
}

// TestParser_ArrowFunctionVariety pins the variable/function classification of
// declarator initializers.
func TestParser_ArrowFunctionVariety(t *testing.T) {
	table, err := tstool.ExtractSymbols(`const a = (x: number) => x + 1;
const b = x => x + 1;
const c = async (x: number) => x + 1;
const d = async function () {};
const e = function () {};
const f = new Foo(() => 1);
const g = { handler: () => 1 };
const h = await load();
const i = tagged` + "`template`" + `;
const j = <T,>(x: T) => x;
let k = 1, l = 2;
var m = (x: number) => x, n = 3;
const o: (x: number) => void = (x) => void x;
const p = connection((a) => a, { retries: 2 });
`)
	require.NoError(t, err)
	names := kindNameList(table)

	assert.Contains(t, names, "function:a")
	assert.Contains(t, names, "function:b")
	assert.Contains(t, names, "function:c")
	assert.Contains(t, names, "function:d")
	assert.Contains(t, names, "function:e")
	assert.Contains(t, names, "variable:f") // new-expression initializer
	assert.Contains(t, names, "variable:g") // object-literal initializer
	assert.Contains(t, names, "variable:h") // await initializer
	assert.Contains(t, names, "variable:i") // tagged-template initializer
	assert.Contains(t, names, "function:j") // generic arrow
	assert.Contains(t, names, "variable:k")
	assert.Contains(t, names, "variable:l")
	assert.Contains(t, names, "function:m")
	assert.Contains(t, names, "variable:n")
	assert.Contains(t, names, "function:o")
	assert.Contains(t, names, "variable:p")

	// Multi-declarator statements: one symbol per identifier declarator, and
	// EVERY symbol of a lexical_declaration carries the WHOLE declaration text
	// as its signature (the TS query's @decl capture).
	assert.Equal(t, "let k = 1, l = 2;",
		findSymbolIn(t, table, "k", tstool.SymbolKindVariable).Signature)
	assert.Equal(t, "let k = 1, l = 2;",
		findSymbolIn(t, table, "l", tstool.SymbolKindVariable).Signature)
	assert.Equal(t, "var m = (x: number) => x, n = 3;",
		findSymbolIn(t, table, "m", tstool.SymbolKindFunction).Signature)
	assert.Equal(t, "var m = (x: number) => x, n = 3;",
		findSymbolIn(t, table, "n", tstool.SymbolKindVariable).Signature)
	assert.Equal(t, 11, findSymbolIn(t, table, "k", tstool.SymbolKindVariable).Line)
	assert.Equal(t, 11, findSymbolIn(t, table, "l", tstool.SymbolKindVariable).Line)
}

// TestParser_ObjectAndFunctionTypeAnnotations pins the annotation capture for
// the shapes whose type expression is not a plain reference.
func TestParser_ObjectAndFunctionTypeAnnotations(t *testing.T) {
	table, err := tstool.ExtractSymbols(`class Handlers {
  onEvent: { (event: string): void } | undefined;
  pair: [number, string];
  lookup: Record<string, { id: string }>;
  handler: (a: number) => void = (a) => void a;
  optional?: string;
  asserted!: number;
  plain;
  handle(input: { id: string }): { ok: boolean } {
    return { ok: true };
  }
  overloaded(a: string): void;
  overloaded(a: number): void;
}
`)
	require.NoError(t, err)

	onEvent := findSymbolIn(t, table, "onEvent", tstool.SymbolKindProperty)
	assert.Equal(t, []string{": { (event: string): void } | undefined"}, onEvent.TypeAnnotations)
	assert.Equal(t, "onEvent: { (event: string): void } | undefined", onEvent.Signature)

	pair := findSymbolIn(t, table, "pair", tstool.SymbolKindProperty)
	assert.Equal(t, []string{": [number, string]"}, pair.TypeAnnotations)

	lookup := findSymbolIn(t, table, "lookup", tstool.SymbolKindProperty)
	assert.Equal(t, []string{": Record<string, { id: string }>"}, lookup.TypeAnnotations)

	handler := findSymbolIn(t, table, "handler", tstool.SymbolKindProperty)
	assert.Equal(t, []string{": (a: number) => void"}, handler.TypeAnnotations)
	assert.Equal(t, "handler: (a: number) => void = (a) => void a", handler.Signature)

	optional := findSymbolIn(t, table, "optional", tstool.SymbolKindProperty)
	assert.Equal(t, []string{": string"}, optional.TypeAnnotations)
	assert.Equal(t, "optional?: string", optional.Signature)

	asserted := findSymbolIn(t, table, "asserted", tstool.SymbolKindProperty)
	assert.Equal(t, "asserted!: number", asserted.Signature)

	plainField := findSymbolIn(t, table, "plain", tstool.SymbolKindProperty)
	assert.Equal(t, "plain", plainField.Signature)

	// An object type as a RETURN type: the `{` is the type, not the body.
	handle := findSymbolIn(t, table, "handle", tstool.SymbolKindMethod)
	assert.Equal(t, "handle(input: { id: string }): { ok: boolean }", handle.Signature)
	assert.Equal(t, []string{": { ok: boolean }"}, handle.TypeAnnotations)

	assert.Equal(t, "overloaded(a: string): void",
		findSymbolIn(t, table, "overloaded", tstool.SymbolKindMethod).Signature)
}

// TestParser_DecoratorsDoNotShiftTheSignature checks that a decorator never
// becomes signature evidence (a metadata-only change must not fire a semantic
// change), and that it never shifts the line numbering of the member.
func TestParser_DecoratorsDoNotShiftTheSignature(t *testing.T) {
	table, err := tstool.ExtractSymbols(`@Component({ selector: 'app' })
@Injectable()
export class Widget {}

class Holder {
  @Input() label: string;
  @Watch('value')
  onChange(): void {}
}
`)
	require.NoError(t, err)

	widget := findSymbolIn(t, table, "Widget", tstool.SymbolKindClass)
	assert.Equal(t, "class Widget", widget.Signature)
	assert.Equal(t, 3, widget.Line)

	label := findSymbolIn(t, table, "label", tstool.SymbolKindProperty)
	assert.Equal(t, "label: string", label.Signature)
	assert.Equal(t, []string{": string"}, label.TypeAnnotations)
	// The decorator occupies member start, but the member itself is on line 6.
	assert.Equal(t, 6, label.Line)

	onChange := findSymbolIn(t, table, "onChange", tstool.SymbolKindMethod)
	assert.Equal(t, "onChange(): void", onChange.Signature)

	// A decorator change is NOT a semantic change (D14: less automation, never
	// more): the same member with and without its decorator reports nothing.
	undecorated := mustExtract(t, "class Holder {\n  label: string;\n}\n")
	decorated := mustExtract(t, "class Holder {\n  @Input() label: string;\n}\n")
	assert.Empty(t, tstool.DiffSymbolTables(&undecorated, &decorated).Added)
	assert.Empty(t, tstool.DiffSymbolTables(&undecorated, &decorated).Removed)
	assert.Empty(t, tstool.DiffSymbolTables(&undecorated, &decorated).Changed)
}

// TestParser_UnbalancedFragmentsDoNotHang exercises the error recovery of the
// before/after reconstruction: source fragments (hunk windows) can be
// unbalanced.
func TestParser_UnbalancedFragmentsDoNotHang(t *testing.T) {
	for _, fragment := range []string{
		"}",
		"{\n  export function dangling(): void {",
		"export function partial(a: string",
		"export interface Broken {\n  half: string",
		"@\n@",
		"export const weird =",
		"export",
		"export {,",
		"type",
		"class",
		"interface",
		"enum",
		"namespace",
		"function",
		"/",
		"`",
		"'",
		"\x00\x01\x02",
		"export const unicode = 'héllo → wörld';",
		"const re = /[/]/g;\nexport const after = 1;",
		"export class A { [",
		"export class A { get ",
	} {
		table, err := tstool.ExtractSymbols(fragment)
		require.NoError(t, err, "fragment %q must never fail extraction", fragment)
		assert.NotNil(t, table.Symbols)
	}
}

// TestParser_LineNumbersAcrossCommentsAndBlanks pins the 1-based line
// accounting through comments, template literals and regex literals.
func TestParser_LineNumbersAcrossCommentsAndBlanks(t *testing.T) {
	table, err := tstool.ExtractSymbols("/* one\n   two\n */\n\nexport function after(): void {}\n\nexport const later = /multi\nline/;\n\nexport function last(): void {}\n")
	require.NoError(t, err)
	// 1: /* one / 2: two / 3: */ / 4: blank / 5: after / 6: blank /
	// 7: literal start / 8: literal end / 9: blank / 10: last
	assert.Equal(t, 5, findSymbolIn(t, table, "after", tstool.SymbolKindFunction).Line)
	assert.Equal(t, 10, findSymbolIn(t, table, "last", tstool.SymbolKindFunction).Line)
}

// TestParser_SameNameSameKindOrdering pins the (name, kind, line) ordering of the
// diff: same-name same-kind pairs are matched positionally in source order.
func TestParser_SameNameSameKindOrdering(t *testing.T) {
	before := mustExtract(t, `export function overload(a: string): void;
export function overload(a: number): void {}

export class Svc {
  route(a: string): void;
  route(a: number): void;
}
`)
	after := mustExtract(t, `export function overload(a: number): void {}

export class Svc {
  route(a: string): void;
  route(a: number): void;
  route(a: boolean): void;
}
`)
	diff := tstool.DiffSymbolTables(&before, &after)
	// The before side carries two `overload` declarations and the after side
	// one: the positional pair (line 1 → after) CHANGES, and the leftover
	// before-side declaration (line 2) is REMOVED.
	assert.Equal(t, []string{"overload"}, symbolNames(diff.Removed))
	require.Len(t, diff.Changed, 1)
	assert.Equal(t, "overload", diff.Changed[0].Name)
	assert.Equal(t, "function overload(a: number): void", diff.Changed[0].After.Signature)

	// The class gained a third `route` overload → reported as added; the two
	// paired `route` declarations are unchanged (positional pairing).
	assert.Equal(t, []string{"route"}, symbolNames(diff.Added))
	assert.Len(t, diff.Removed, 1)
}

// TestParser_DivisionVersusRegexLiteral pins the lexer's regex/division
// heuristic: `/` after an identifier is division, after an operator a regex.
func TestParser_DivisionVersusRegexLiteral(t *testing.T) {
	table, err := tstool.ExtractSymbols(`export const ratio = total / divisor;
export const found = /ab+c/gi.test(text);
export const index = 4 / 2 / 1;
`)
	require.NoError(t, err)

	assert.Equal(t, "const ratio = total / divisor;",
		findSymbolIn(t, table, "ratio", tstool.SymbolKindVariable).Signature)
	assert.Equal(t, "const found = /ab+c/gi.test(text);",
		findSymbolIn(t, table, "found", tstool.SymbolKindVariable).Signature)
	assert.Equal(t, "const index = 4 / 2 / 1;",
		findSymbolIn(t, table, "index", tstool.SymbolKindVariable).Signature)
}

// TestParser_ReexportAndAmbientShapesEmitNothing pins the documented query
// coverage gaps as executable expectations.
func TestParser_ReexportAndAmbientShapesEmitNothing(t *testing.T) {
	table, err := tstool.ExtractSymbols(`import foo from './foo.js';
import { a, b, type C } from './c.js';
import * as ns from './ns.js';
export * from './all.js';
export { a as alpha } from './a.js';
export type { C } from './c.js';
export * as everything from './everything.js';
export = ns;
declare const ambientValue: number;
declare function ambientFn(): void;
`)
	require.NoError(t, err)
	require.NotNil(t, table.Symbols)
	assert.Empty(t, table.Symbols)
}
