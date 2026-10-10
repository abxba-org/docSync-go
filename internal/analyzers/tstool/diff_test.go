// Ported from packages/analyzer-typescript/test/diff.test.ts (E1a + E2 /
// P11-T03; V-P11-1 groundwork). Black-box over the exported diff and the
// unified-diff reconstruction.
package tstool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analyzers/tstool"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

const (
	beforeSource = `export interface Order {
  idempotencyKey: string;
  total(): number;
}
`
	afterTypeWidened = `export interface Order {
  idempotencyKey: string | null;
  total(): number;
}
`
	afterOptional = `export interface Order {
  idempotencyKey?: string;
  total(): number;
}
`
)

func mustExtract(t *testing.T, source string) tstool.SymbolTable {
	t.Helper()
	table, err := tstool.ExtractSymbols(source)
	require.NoError(t, err)
	return table
}

func changeKindNames(change tstool.SymbolChange) []string {
	out := make([]string, 0, len(change.ChangeKinds))
	for _, kind := range change.ChangeKinds {
		out = append(out, string(kind))
	}
	return out
}

func TestDiffSymbolTables_TypeChange(t *testing.T) {
	before := mustExtract(t, beforeSource)
	after := mustExtract(t, afterTypeWidened)

	diff := tstool.DiffSymbolTables(&before, &after)
	assert.Empty(t, diff.Added)
	assert.Empty(t, diff.Removed)
	require.Len(t, diff.Changed, 1)

	change := diff.Changed[0]
	assert.Equal(t, "idempotencyKey", change.Name)
	assert.Equal(t, tstool.SymbolKindProperty, change.Kind)
	assert.Contains(t, changeKindNames(change), string(schemas.SymbolChangeType))
	assert.Equal(t, []string{": string"}, change.Before.TypeAnnotations)
	assert.Equal(t, []string{": string | null"}, change.After.TypeAnnotations)
}

func TestDiffSymbolTables_SignatureOnlyChangeOptionality(t *testing.T) {
	before := mustExtract(t, beforeSource)
	after := mustExtract(t, afterOptional)

	diff := tstool.DiffSymbolTables(&before, &after)
	require.Len(t, diff.Changed, 1)
	assert.Equal(t, "idempotencyKey", diff.Changed[0].Name)
	assert.Equal(t, []string{string(schemas.SymbolChangeSignature)}, changeKindNames(diff.Changed[0]))
	// The annotation text is unchanged — the `?` sits on the property, not
	// inside the annotation the extractor captures.
	assert.Equal(t, []string{": string"}, diff.Changed[0].Before.TypeAnnotations)
	assert.Equal(t, []string{": string"}, diff.Changed[0].After.TypeAnnotations)
}

func TestDiffSymbolTables_VisibilityFlip(t *testing.T) {
	before := mustExtract(t, "function helper(): void {}\n")
	after := mustExtract(t, "export function helper(): void {}\n")

	diff := tstool.DiffSymbolTables(&before, &after)
	require.Len(t, diff.Changed, 1)
	assert.Equal(t, "helper", diff.Changed[0].Name)
	assert.Equal(t, []string{string(schemas.SymbolChangeVisibility)}, changeKindNames(diff.Changed[0]))
	assert.Empty(t, diff.Added)
	assert.Empty(t, diff.Removed)
}

func TestDiffSymbolTables_ExcludesLineOnlyMoves(t *testing.T) {
	before := mustExtract(t, "function alpha(): void {}\n\nfunction beta(): void {}\n")
	after := mustExtract(t, "// a new comment line\n// another one\n\nfunction alpha(): void {}\n\nfunction beta(): void {}\n")

	diff := tstool.DiffSymbolTables(&before, &after)
	assert.Empty(t, diff.Added)
	assert.Empty(t, diff.Removed)
	assert.Empty(t, diff.Changed)
	// The move is real: alpha shifted from line 1 to line 4.
	assert.Equal(t, 1, findSymbolIn(t, before, "alpha", tstool.SymbolKindFunction).Line)
	assert.Equal(t, 4, findSymbolIn(t, after, "alpha", tstool.SymbolKindFunction).Line)
}

func TestDiffSymbolTables_AddedRemovedAndKindChange(t *testing.T) {
	before := mustExtract(t, "export const gone = 1;\n")
	after := mustExtract(t, "export const fresh = 2;\nexport const fresher = 3;\n")

	diff := tstool.DiffSymbolTables(&before, &after)
	assert.Equal(t, []string{"gone"}, symbolNames(diff.Removed))
	assert.Equal(t, []string{"fresh", "fresher"}, symbolNames(diff.Added))
	assert.Empty(t, diff.Changed)

	// A kind change is removed + added, never a fake in-place change.
	itemBefore := mustExtract(t, "export const item = 1;\n")
	itemAfter := mustExtract(t, "export function item(): void {}\n")
	kindFlip := tstool.DiffSymbolTables(&itemBefore, &itemAfter)
	assert.Equal(t, []string{"variable:item"}, kindNamesOf(kindFlip.Removed))
	assert.Equal(t, []string{"function:item"}, kindNamesOf(kindFlip.Added))
	assert.Empty(t, kindFlip.Changed)
}

func TestDiffSymbolTables_BeforeNilIsAddedFile(t *testing.T) {
	after := mustExtract(t, beforeSource)
	diff := tstool.DiffSymbolTables(nil, &after)
	assert.Empty(t, diff.Removed)
	assert.Empty(t, diff.Changed)
	assert.Equal(t, []string{"interface:Order", "property:idempotencyKey", "method:total"}, kindNamesOf(diff.Added))
}

func TestDiffSymbolTables_DeterministicOrdering(t *testing.T) {
	before := mustExtract(t, beforeSource)
	after := mustExtract(t, afterTypeWidened)

	first := tstool.DiffSymbolTables(&before, &after)
	second := tstool.DiffSymbolTables(&before, &after)
	assert.Equal(t, first, second)

	orderBefore := mustExtract(t, "export const zebra = 1;\nexport const apple = 2;\n")
	orderAfter := mustExtract(t, "export const mango = 3;\n")
	order := tstool.DiffSymbolTables(&orderBefore, &orderAfter)
	assert.Equal(t, []string{"apple", "zebra"}, symbolNames(order.Removed))
	assert.Equal(t, []string{"mango"}, symbolNames(order.Added))
}

func TestDiffSymbolTables_CombinedChangeKindsInFixedOrder(t *testing.T) {
	before := mustExtract(t, "function serve(input: string): number { return 1; }\n")
	after := mustExtract(t, "export function serve(input: string | null): string { return \"\"; }\n")

	diff := tstool.DiffSymbolTables(&before, &after)
	require.Len(t, diff.Changed, 1)
	assert.Equal(t,
		[]string{string(schemas.SymbolChangeSignature), string(schemas.SymbolChangeType), string(schemas.SymbolChangeVisibility)},
		changeKindNames(diff.Changed[0]))
}

func TestBeforeTextFromPatch(t *testing.T) {
	const modified = `diff --git a/src/orders/order.ts b/src/orders/order.ts
--- a/src/orders/order.ts
+++ b/src/orders/order.ts
@@ -1,4 +1,4 @@
 export interface Order {
-  idempotencyKey: string;
+  idempotencyKey: string | null;
   total(): number;
 }
`
	assert.Equal(t, "export interface Order {\n  idempotencyKey: string;\n  total(): number;\n}",
		tstool.BeforeTextFromPatch(modified))

	const added = `diff --git a/src/new.ts b/src/new.ts
new file mode 100644
--- /dev/null
+++ b/src/new.ts
@@ -0,0 +1,1 @@
+export const fresh = 1;
`
	assert.Equal(t, "", tstool.BeforeTextFromPatch(added))
	assert.Equal(t, "", tstool.BeforeTextFromPatch("no hunks here"))

	const deleted = `diff --git a/src/gone.ts b/src/gone.ts
deleted file mode 100644
--- a/src/gone.ts
+++ /dev/null
@@ -1,2 +0,0 @@
-export const gone = 1;
-function helper(): void {}
`
	assert.Equal(t, "export const gone = 1;\nfunction helper(): void {}",
		tstool.BeforeTextFromPatch(deleted))

	// Determinism: identical patches yield identical reconstructions.
	assert.Equal(t, tstool.BeforeTextFromPatch(modified), tstool.BeforeTextFromPatch(modified))
}

// findSymbolIn is the table-scoped lookup helper.
func findSymbolIn(t *testing.T, table tstool.SymbolTable, name string, kind tstool.SymbolKind) tstool.ExtractedSymbol {
	t.Helper()
	for _, symbol := range table.Symbols {
		if symbol.Name == name && symbol.Kind == kind {
			return symbol
		}
	}
	require.Failf(t, "symbol not found", "expected a %s named '%s'", kind, name)
	return tstool.ExtractedSymbol{}
}

// kindNamesOf renders `kind:name` pairs (the TS `.map((s) => `${s.kind}:${s.name}`)`
// assertions).
func kindNamesOf(symbols []tstool.ExtractedSymbol) []string {
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		out = append(out, string(symbol.Kind)+":"+symbol.Name)
	}
	return out
}
