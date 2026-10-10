// Ported from packages/analyzer-typescript/test/symbols.test.ts (F11-1 / P11;
// V-P11-2 groundwork). Black-box over the exported extractor.

package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
)

// fixture is the TS FIXTURE verbatim (the same source the tree-sitter-backed
// extractor was asserted against).
const fixture = `import { Logger } from './logger.js';

export const VERSION: string = '1.0.0';

export function processOrder(orderId: string): Promise<void> {
  return Promise.resolve(orderId);
}

function internalHelper(input: number): number {
  return input * 2;
}

const normalize = (value: string) => value.trim();

export class OrderService {
  private idempotencyKey: string;
  attempts: number = 0;

  constructor(idempotencyKey: string) {
    this.idempotencyKey = idempotencyKey;
  }

  async submit(order: { id: string }): Promise<void> {
    this.attempts += 1;
  }

  private deliver(): void {}
}

export interface Order {
  idempotencyKey: string;
  total(): number;
}

export type OrderState = 'draft' | 'submitted';

export enum OrderStatus {
  Draft,
  Submitted,
}

function nestedInsideFunction(): void {
  function shadowed(): void {}
}
`

// findSymbol is the port of the test's find() helper.
func findSymbol(
	t *testing.T,
	table tstool.SymbolTable,
	name string,
	kind tstool.SymbolKind,
) tstool.ExtractedSymbol {
	t.Helper()
	for _, symbol := range table.Symbols {
		if symbol.Name == name && symbol.Kind == kind {
			return symbol
		}
	}
	require.Failf(t, "symbol not found", "expected a %s named '%s' in: %+v", kind, name, table.Symbols)
	return tstool.ExtractedSymbol{}
}

func kindNameList(table tstool.SymbolTable) []string {
	out := make([]string, 0, len(table.Symbols))
	for _, symbol := range table.Symbols {
		out = append(out, string(symbol.Kind)+":"+symbol.Name)
	}
	return out
}

func TestExtractSymbols(t *testing.T) {
	table, err := tstool.ExtractSymbols(fixture)
	require.NoError(t, err)
	names := kindNameList(table)

	assert.Contains(t, names, "variable:VERSION")
	assert.Contains(t, names, "function:processOrder")
	assert.Contains(t, names, "function:internalHelper")
	assert.Contains(t, names, "function:normalize")
	assert.Contains(t, names, "class:OrderService")
	assert.Contains(t, names, "property:idempotencyKey")
	assert.Contains(t, names, "property:attempts")
	assert.Contains(t, names, "method:submit")
	assert.Contains(t, names, "method:deliver")
	assert.Contains(t, names, "method:total")
	assert.Contains(t, names, "interface:Order")
	assert.Contains(t, names, "type_alias:OrderState")
	assert.Contains(t, names, "enum:OrderStatus")
	// Out of scope: a function nested inside another function body.
	assert.NotContains(t, names, "function:shadowed")

	version := findSymbol(t, table, "VERSION", tstool.SymbolKindVariable)
	assert.True(t, version.Exported)
	assert.Equal(t, 3, version.Line)
	assert.Equal(t, []string{": string"}, version.TypeAnnotations)

	processOrder := findSymbol(t, table, "processOrder", tstool.SymbolKindFunction)
	assert.True(t, processOrder.Exported)
	assert.Equal(t, "function processOrder(orderId: string): Promise<void>", processOrder.Signature)
	assert.Equal(t, []string{": Promise<void>"}, processOrder.TypeAnnotations)

	normalize := findSymbol(t, table, "normalize", tstool.SymbolKindFunction)
	assert.False(t, normalize.Exported)
	assert.Equal(t, "const normalize = (value: string) => value.trim();", normalize.Signature)

	service := findSymbol(t, table, "OrderService", tstool.SymbolKindClass)
	assert.True(t, service.Exported)
	assert.Equal(t, "class OrderService", service.Signature)
	assert.Nil(t, service.TypeAnnotations)

	// A class member: not exported in its own right (the exporting symbol is the
	// enclosing `export class OrderService` declaration itself).
	key := findSymbol(t, table, "idempotencyKey", tstool.SymbolKindProperty)
	assert.False(t, key.Exported)
	assert.Equal(t, []string{": string"}, key.TypeAnnotations)
	assert.Equal(t, "private idempotencyKey: string", key.Signature)

	submit := findSymbol(t, table, "submit", tstool.SymbolKindMethod)
	assert.False(t, submit.Exported)
	assert.Equal(t, "async submit(order: { id: string }): Promise<void>", submit.Signature)
	assert.Equal(t, []string{": Promise<void>"}, submit.TypeAnnotations)

	total := findSymbol(t, table, "total", tstool.SymbolKindMethod)
	assert.Equal(t, []string{": number"}, total.TypeAnnotations)

	state := findSymbol(t, table, "OrderState", tstool.SymbolKindTypeAlias)
	assert.True(t, state.Exported)
	assert.Equal(t, "type OrderState = 'draft' | 'submitted';", state.Signature)

	status := findSymbol(t, table, "OrderStatus", tstool.SymbolKindEnum)
	assert.True(t, status.Exported)

	internal := findSymbol(t, table, "internalHelper", tstool.SymbolKindFunction)
	assert.False(t, internal.Exported)
	assert.Equal(t, "function internalHelper(input: number): number", internal.Signature)
}

func TestExtractSymbolsDeterministic(t *testing.T) {
	first, err := tstool.ExtractSymbols(fixture)
	require.NoError(t, err)
	second, err := tstool.ExtractSymbols(fixture)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.True(t, assert.ObjectsAreEqual(first.Symbols, second.Symbols))
}

// TestExtractSymbolsDeclaredCoverageModel pins the extractor's coverage model
// (the same one the tree-sitter query had): top-level declarations plus
// class/interface members only — everything nested deeper is out of scope and
// must be reported as NOTHING, never invented.
func TestExtractSymbolsDeclaredCoverageModel(t *testing.T) {
	table, err := tstool.ExtractSymbols(`function outer(): void {
  function shadowed(): void {}
  if (true) {
    function deeper(): void {}
  }
}

namespace inner {
  export function hidden(): void {}
}

declare module 'ambient' {
  export function ambient(): void;
}

export const destructured = { a: 1, b: 2 };
const { a, b } = destructured;
export const arrow = (x: number) => x + 1;
export const plain = 7;
`)
	require.NoError(t, err)

	names := kindNameList(table)
	assert.Contains(t, names, "function:outer")
	// Nested declarations, namespace members, ambient declarations and
	// destructuring targets are all out of scope / unmatched.
	assert.NotContains(t, names, "function:shadowed")
	assert.NotContains(t, names, "function:deeper")
	assert.NotContains(t, names, "function:hidden")
	assert.NotContains(t, names, "function:ambient")
	assert.NotContains(t, names, "variable:a")
	assert.NotContains(t, names, "variable:b")
	// Arrow-valued declarators report kind 'function'; plain values 'variable'.
	assert.Contains(t, names, "function:arrow")
	assert.Contains(t, names, "variable:plain")
	assert.Contains(t, names, "variable:destructured")
}

// TestExtractSymbolsEmptySource pins the empty-source result: a table with no
// symbols (never a nil slice, so callers can serialize it deterministically).
func TestExtractSymbolsEmptySource(t *testing.T) {
	table, err := tstool.ExtractSymbols("")
	require.NoError(t, err)
	require.NotNil(t, table.Symbols)
	assert.Empty(t, table.Symbols)
}
