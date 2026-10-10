// Ported from:
//   - packages/analyzer-typescript/src/index.ts      — the barrel/type identity,
//     the package version and the declared-degradation stub (TYPESCRIPT_STUB_NOTE)
//   - packages/analyzer-typescript/src/analyzer.ts   — the REAL analyzer plugin
//     (F11-1/F11-3, E1b + E2 / P11-T03; ARCH §3.7, §4.1)
//   - packages/analyzer-typescript/src/diff.ts       — the pure symbol-table diff
//     and both sides of the unified-diff reconstruction (F11-3/E2)
//
// The real plugin implements the SAME analysis.AnalyzerPlugin contract as the V1
// stub, registry name unchanged ('typescript'), so the OD-4 order and every
// ComposeAnalyzers call site are transparent to the swap. The stub
// (CreateTypeScriptAnalyzerStub) remains the declared-degradation fallback of
// this plugin (see the analyze failure paths) and stays exported for composition
// sites that need the pure stub.
//
// INPUT REALITY (verified against internal/analysis/plugin.go): the analyzer
// input carries per-file { path, status, old_path?, patch? } — NO file content
// and NO blob SHAs. Consequences (all declared, never silent — D14):
//
//   - A unified diff carries BOTH sides of every hunk: the before-side source is
//     reconstructed from the `-`/context lines (BeforeTextFromPatch) and the
//     after-side source from the `+`/context lines (AfterTextFromPatch). A FULL
//     SymbolDiff (added/removed/changed) is therefore derived for EVERY
//     patch-bearing file — added files (before side empty), modified/renamed
//     files (both sides from the hunk windows), and deleted files (after side
//     empty).
//   - Declared partiality (both sides equally): the diff context is truncated to
//     the hunk windows, so the reconstruction covers declarations fully
//     contained in those windows; declarations partially outside the context are
//     invisible to BOTH sides and cannot produce phantom diffs.
//   - A deleted file with a patch is ANALYZED (the before side is the full
//     removed content; every symbol is reported as removed). A file without any
//     patch has no content at all and gets a deep_declined finding with a
//     declared note (never an all-false row without the decline marker).
//   - The schema-level projection `symbols_detail` (schemas.SymbolsDetail) is
//     attached to every analyzed finding so the structured evidence SURVIVES
//     companion analysis (P11-T03).
//
// CACHE KEY (documented decision): the symbol index cache is keyed by blobSha
// (ARCH §3.7), but the real analyzer input carries no blob SHAs and plugin
// purity FORBIDS fs/git access, so the key is docutil.ContentHash of the
// analyzed content — the same sha256 hex digest as the TS `contentSha256`
// content hash pins the exact extraction input just like a blob SHA would.
//
// PARSER FACTORY OPTION (kept from the TS unit sketch): the factory takes
// CreateParser (default CreateTypeScriptParser) as the availability gate;
// injecting a failing factory forces the declared stub fallback — exactly the
// test seam this unit requires.
//
// FAIL-CLOSED (D14): any parser-gate or extraction failure degrades the WHOLE
// analyze call to the stub coverage shape (status 'stub', TypeScriptStubNote,
// files: []) — declared, never a silent partial or a negative result. A run
// with deepAllowed false downgrades the same way with its own declared note
// (ARCH §3.7: beyond caps → path-level).
//
// STRUCTURAL NOTE (Go): analysis.PerFileFinding has no field for the rich
// analyzer-layer evidence, so the TS finding's `symbolDiff` extra property is
// surfaced through TypeScriptFileFinding and AnalyzeDetailed (the pure result
// Analyze projects into the plugin contract) instead of riding the plugin
// output. The schema-level `symbols_detail` projection rides every finding, so
// nothing is lost for the ChangeAnalysis rows or for companion analysis.
//
// PURITY: no fs/network/git/clock; deterministic for identical input — the same
// input yields a deep-equal, byte-stable output.

package tstool

import (
	"fmt"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AnalyzerTypeScriptPackageVersion is the package version (0.0.0 until first
// release) — the port of ANALYZER_TYPESCRIPT_PACKAGE_VERSION.
const AnalyzerTypeScriptPackageVersion = "0.0.0"

// TypeScriptStubNote is the byte-stable stub note — the declared V1 coverage
// gap itself (V-P1-13 groundwork). A constant so every declaration of the gap is
// identical. Verbatim TYPESCRIPT_STUB_NOTE, byte-equal to
// analysis.TypeScriptStubNote (the seam constant) and to the schema example's
// copy.
const TypeScriptStubNote = "stub: no AST/symbol extraction in V1 — tree-sitter symbol/type-level analysis lands in Phase 11 (F11-1); per-file symbol diffs are a declared V1 coverage gap"

// LightweightParserDeviationNote is the declared DEV-1 note (plan.md §6): the
// Go port replaces web-tree-sitter with an equivalent lightweight
// TypeScript/TSX parser because Go has no tree-sitter binding without CGO. The
// classification output and coverage model are unchanged, and the substitution
// is DECLARED here — never silently absorbed.
const LightweightParserDeviationNote = "Go port deviation (plan.md §6 DEV-1): symbol extraction runs on an equivalent lightweight TypeScript/TSX parser instead of web-tree-sitter (Go has no tree-sitter binding without CGO) — the coverage model and the classification output are unchanged"

// TypeScriptDeepNotPermittedNote is the declared note for a run where deep
// analysis is not permitted (ARCH §3.7): the plugin downgrades to the stub
// coverage shape instead of silently emitting nothing. Byte-stable constant
// (determinism). Verbatim TYPESCRIPT_DEEP_NOT_PERMITTED_NOTE.
const TypeScriptDeepNotPermittedNote = "deep analysis not permitted for this run (deepAllowed=false) — TypeScript analyzer skipped AST/symbol extraction and reports the stub coverage shape (declared downgrade, ARCH §3.7; D14)"

// Byte-stable per-file decline notes (determinism; declared, never silent).
const (
	noPatchNote     = "no unified diff provided for this file — content is unavailable to a pure plugin; symbol extraction declined (declared, D14)"
	noHunksNote     = "unified diff carries no hunks — no content is derivable from either side; symbol extraction declined (declared, D14)"
	addedFileNote   = "full after-side reconstructed from the patch (added file); the before side is empty — every extracted symbol is reported as added"
	deletedFileNote = "file deleted — the after side is empty (status deleted); every before-side symbol is reported as removed"
	bothSidesNote   = "both sides reconstructed from the unified diff (added/removed/context lines) — the symbol diff covers declarations fully contained in the hunk windows; declarations partially outside the diff context are invisible to both sides equally (declared, D14)"
)

/* ------------------------------------------------- the parser seam (DEV-1) */

// TypeScriptParser is the symbol-extraction engine behind the plugin: the
// availability gate of the deep path (the port of the web-tree-sitter parser
// handle). The default implementation is the lightweight parser in symbols.go.
type TypeScriptParser interface {
	// Extract returns the symbol table for one source blob.
	Extract(source string) (SymbolTable, error)
}

// CreateTypeScriptParser is the default parser factory — always available in the
// Go port (no WASM runtime to bootstrap). Fail-closed injectors replace it to
// force the declared stub fallback.
func CreateTypeScriptParser() (TypeScriptParser, error) { return lightweightParser{}, nil }

// lightweightParser is the DEV-1 parser: lex + scan, no external runtime.
type lightweightParser struct{}

// Extract extracts the symbol table for one source blob.
func (lightweightParser) Extract(source string) (SymbolTable, error) { return ExtractSymbols(source) }

/* --------------------------------------------------------- the stub plugin */

// TypeScriptStubPlugin is the V1 TypeScript analyzer stub (the
// declared-degradation fallback of TypeScriptPlugin).
type TypeScriptStubPlugin struct{}

// CreateTypeScriptAnalyzerStub creates the stub plugin: registry name
// 'typescript', coverage `{ status: 'stub', note }` and an empty findings list,
// regardless of input (including .ts files carrying patches and deepAllowed
// being true) — the gap is declared via coverage, and absence of findings plus
// that status is how "unanalyzed" is represented (D14), never an all-false row.
func CreateTypeScriptAnalyzerStub() TypeScriptStubPlugin { return TypeScriptStubPlugin{} }

// Name is the registry name — 'typescript' for both the stub and the real
// analyzer.
func (TypeScriptStubPlugin) Name() schemas.AnalyzerName { return schemas.AnalyzerNameTypeScript }

// Analyze returns the declared stub coverage shape.
func (TypeScriptStubPlugin) Analyze(analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	return stubOutput(), nil
}

// TypeScriptAnalyzerStub is the shared singleton instance of the V1 TypeScript
// analyzer stub (port of typescriptAnalyzerStub).
var TypeScriptAnalyzerStub analysis.AnalyzerPlugin = TypeScriptStubPlugin{}

// Compile-time contract assertions: both plugins satisfy the plugin seam.
var (
	_ analysis.AnalyzerPlugin = TypeScriptStubPlugin{}
	_ analysis.AnalyzerPlugin = (*TypeScriptPlugin)(nil)
)

// stubOutput is the exact stub mirror used by every declared degradation path.
func stubOutput() analysis.AnalyzerOutput {
	note := TypeScriptStubNote
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusStub,
			Note:     &note,
		},
		Files: []analysis.PerFileFinding{},
	}
}

/* -------------------------------------------------------- the real plugin */

// TypeScriptAnalyzerOptions configures CreateTypeScriptAnalyzer (port of
// TypeScriptAnalyzerOptions).
type TypeScriptAnalyzerOptions struct {
	// CreateParser is the parser factory override — the deep-analysis
	// availability gate. Defaults to CreateTypeScriptParser. A factory that
	// fails (wasm unavailable in TS; a test injection here) makes every analyze
	// call return the declared stub coverage shape (D14).
	CreateParser func() (TypeScriptParser, error)
}

// TypeScriptPlugin is the real TypeScript analyzer plugin: the
// analysis.AnalyzerPlugin contract plus Stats for cache observability (builds/
// hits over the instance lifetime). Port of TypeScriptAnalyzer.
type TypeScriptPlugin struct {
	createParser func() (TypeScriptParser, error)
	cache        *SymbolIndexCache[SymbolTable]

	// The parser gate never fails loudly: a factory failure is RECORDED
	// (parserErr) so every analyze call observes it and degrades to the declared
	// stub shape (D14). Resolution is lazy and idempotent — the TS promise is
	// created at factory time and awaited by the first analyze call.
	mu           sync.Mutex
	gateResolved bool
	parser       TypeScriptParser
	parserErr    error
}

// TypeScriptFileFinding is the per-file finding for one .ts/.tsx file: the
// core PerFileFinding plus the RICH structured symbol diff (full before/after
// symbol evidence — signatures, lines, type annotations).
type TypeScriptFileFinding struct {
	analysis.PerFileFinding
	// SymbolDiff is the full structured symbol diff — present for every analyzed
	// file.
	SymbolDiff *SymbolDiff
}

// AnalyzeResult is the rich result of one analyzer run: exactly one coverage
// entry plus the per-file findings carrying their full symbol diff.
type AnalyzeResult struct {
	Coverage schemas.AnalyzerCoverage
	Files    []TypeScriptFileFinding
}

// CreateTypeScriptAnalyzer creates the real TypeScript analyzer. See the module
// doc for the input reality, the cache-key decision, and the fail-closed
// degradation paths. The options parameter is optional (the TS `options?`
// spelling); the first non-nil option set wins.
func CreateTypeScriptAnalyzer(options ...*TypeScriptAnalyzerOptions) *TypeScriptPlugin {
	var opts *TypeScriptAnalyzerOptions
	for _, candidate := range options {
		if candidate != nil {
			opts = candidate
			break
		}
	}
	createParser := CreateTypeScriptParser
	if opts != nil && opts.CreateParser != nil {
		createParser = opts.CreateParser
	}
	return &TypeScriptPlugin{
		createParser: createParser,
		cache:        CreateSymbolIndexCache[SymbolTable](),
	}
}

// Name is the registry name — 'typescript' (OD-4: first plugin).
func (p *TypeScriptPlugin) Name() schemas.AnalyzerName { return schemas.AnalyzerNameTypeScript }

// Stats is the symbol-index cache observability over this instance's lifetime.
func (p *TypeScriptPlugin) Stats() SymbolIndexCacheStats { return p.cache.Stats() }

// resolveParser resolves the parser gate exactly once (idempotent, never
// panicking): failures are recorded, not thrown.
func (p *TypeScriptPlugin) resolveParser() (TypeScriptParser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.gateResolved {
		p.gateResolved = true
		p.parser, p.parserErr = p.createParser()
	}
	return p.parser, p.parserErr
}

// Analyze projects one analyzer run into the plugin contract. The rich symbol
// diff of every analyzed file is available through AnalyzeDetailed.
func (p *TypeScriptPlugin) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	result, err := p.AnalyzeDetailed(input)
	if err != nil {
		return analysis.AnalyzerOutput{}, err
	}
	files := make([]analysis.PerFileFinding, 0, len(result.Files))
	for _, finding := range result.Files {
		files = append(files, finding.PerFileFinding)
	}
	return analysis.AnalyzerOutput{Coverage: result.Coverage, Files: files}, nil
}

// AnalyzeDetailed runs the plugin and returns the rich per-file findings (the
// port of the analyzer's internal findings, which carry the full SymbolDiff).
func (p *TypeScriptPlugin) AnalyzeDetailed(input analysis.AnalyzerInput) (AnalyzeResult, error) {
	if !input.DeepAllowed {
		note := TypeScriptDeepNotPermittedNote
		return AnalyzeResult{
			Coverage: schemas.AnalyzerCoverage{
				Analyzer: schemas.AnalyzerNameTypeScript,
				Status:   schemas.AnalyzerStatusStub,
				Note:     &note,
			},
			Files: []TypeScriptFileFinding{},
		}, nil
	}
	parser, err := p.resolveParser()
	if err != nil || parser == nil {
		// Fail-closed: the parser gate failed → the EXACT stub shape.
		return AnalyzeResult{Coverage: stubOutput().Coverage, Files: []TypeScriptFileFinding{}}, nil
	}

	findings := make([]TypeScriptFileFinding, 0, len(input.Files))
	tsFiles := 0
	extracted := 0
	declined := 0
	for _, file := range input.Files {
		if !isTypeScriptPath(file.Path) {
			continue
		}
		tsFiles++
		if file.Patch == nil {
			declined++
			findings = append(findings, declinedFinding(file, noPatchNote))
			continue
		}
		// Both sides from the SAME unified diff: the before side from
		// removed/context lines, the after side from added/context lines.
		beforeText := BeforeTextFromPatch(*file.Patch)
		afterText := AfterTextFromPatch(*file.Patch)
		if beforeText == "" && afterText == "" {
			declined++
			findings = append(findings, declinedFinding(file, noHunksNote))
			continue
		}
		// Cache keys: content sha256 — the input carries no git blob sha and
		// the purity contract forbids fs/git access (see module doc).
		beforeTable, beforeErr := p.cache.GetOrBuild(
			SymbolIndexCacheRequest{BlobSha: docutil.ContentHash(beforeText), FilePath: file.Path},
			func() (SymbolTable, error) { return parser.Extract(beforeText) },
		)
		if beforeErr != nil {
			// Fail-closed: an extraction failure degrades the WHOLE call to the
			// declared stub shape — never a silent partial result (D14).
			return AnalyzeResult{Coverage: stubOutput().Coverage, Files: []TypeScriptFileFinding{}}, nil
		}
		afterTable, afterErr := p.cache.GetOrBuild(
			SymbolIndexCacheRequest{BlobSha: docutil.ContentHash(afterText), FilePath: file.Path},
			func() (SymbolTable, error) { return parser.Extract(afterText) },
		)
		if afterErr != nil {
			return AnalyzeResult{Coverage: stubOutput().Coverage, Files: []TypeScriptFileFinding{}}, nil
		}
		extracted++
		findings = append(findings, analyzedFinding(file, beforeTable, afterTable))
	}

	note := analyzedCoverageNote(tsFiles, extracted, declined)
	return AnalyzeResult{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusOK,
			Note:     &note,
		},
		Files: findings,
	}, nil
}

func isTypeScriptPath(path string) bool {
	return strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")
}

// BeforeTextFromPatch reconstructs the BEFORE-side source from a unified diff:
// the REMOVED lines and CONTEXT lines (leading `-`/` ` prefix stripped) in hunk
// order — the exact mirror of AfterTextFromPatch (ADDED + context). For a
// status-'added' file there are no `-`/context lines and this yields ” (the
// file did not exist). '\' lines ("no newline at end of file" markers) carry no
// source text and are skipped. Pure and deterministic.
func BeforeTextFromPatch(patch string) string {
	var removed []string
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			continue
		}
		if !inHunk {
			continue // diff --git / index / --- / +++ headers before the first hunk
		}
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") {
			removed = append(removed, line[1:])
		}
		// '+', '\' (no-newline marker) and blank separators are skipped.
	}
	return strings.Join(removed, "\n")
}

// AfterTextFromPatch reconstructs the AFTER-side source from a unified diff: the
// ADDED lines AND CONTEXT lines (leading `+`/` ` stripped) in hunk order — the
// mirror of BeforeTextFromPatch. For an 'added' file this is the whole file; for
// a 'deleted' file it is ”. Pure and deterministic.
func AfterTextFromPatch(patch string) string {
	var added []string
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, " ") {
			added = append(added, line[1:])
		}
		// '-', '\' (no-newline marker) and blank separators are skipped.
	}
	return strings.Join(added, "\n")
}

func declinedFinding(file analysis.AnalyzerInputFile, note string) TypeScriptFileFinding {
	return TypeScriptFileFinding{
		PerFileFinding: analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: false,
			APIDiff:        false,
			ConfigDiff:     false,
			DeepDeclined:   true,
			Notes:          []string{note},
		},
	}
}

// SymbolsDetailOf projects a rich SymbolDiff into the schema-level
// `symbols_detail` shape (name-level evidence; the full before/after annotations
// stay in the analyzer layer). Pure and deterministic.
func SymbolsDetailOf(diff *SymbolDiff) *schemas.SymbolsDetail {
	detail := &schemas.SymbolsDetail{
		Added:   []string{},
		Removed: []string{},
		Changed: []schemas.SymbolChangeDetail{},
	}
	if diff == nil {
		return detail
	}
	for _, symbol := range diff.Added {
		detail.Added = append(detail.Added, symbol.Name)
	}
	for _, symbol := range diff.Removed {
		detail.Removed = append(detail.Removed, symbol.Name)
	}
	for _, change := range diff.Changed {
		detail.Changed = append(detail.Changed, schemas.SymbolChangeDetail{
			Name:        change.Name,
			ChangeKinds: append([]schemas.SymbolChangeKind(nil), change.ChangeKinds...),
		})
	}
	return detail
}

func analyzedFinding(
	file analysis.AnalyzerInputFile,
	beforeTable SymbolTable,
	afterTable SymbolTable,
) TypeScriptFileFinding {
	names := make([]string, 0, len(beforeTable.Symbols)+len(afterTable.Symbols))
	seen := make(map[string]bool, len(beforeTable.Symbols)+len(afterTable.Symbols))
	for _, symbol := range beforeTable.Symbols {
		if !seen[symbol.Name] {
			seen[symbol.Name] = true
			names = append(names, symbol.Name)
		}
	}
	for _, symbol := range afterTable.Symbols {
		if !seen[symbol.Name] {
			seen[symbol.Name] = true
			names = append(names, symbol.Name)
		}
	}
	symbolDiff := DiffSymbolTables(&beforeTable, &afterTable)
	note := bothSidesNote
	switch file.Status {
	case schemas.FileStatusAdded:
		note = addedFileNote
	case schemas.FileStatusDeleted:
		note = deletedFileNote
	}
	// Public surface affected: an exported symbol exists on EITHER side (a
	// deleted exported symbol is as much an API-surface change as an added one).
	apiDiff := false
	for _, symbol := range beforeTable.Symbols {
		if symbol.Exported {
			apiDiff = true
			break
		}
	}
	if !apiDiff {
		for _, symbol := range afterTable.Symbols {
			if symbol.Exported {
				apiDiff = true
				break
			}
		}
	}
	finding := TypeScriptFileFinding{
		PerFileFinding: analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: !symbolDiff.Empty(),
			APIDiff:        apiDiff,
			ConfigDiff:     false,
			Notes:          []string{note},
		},
		SymbolDiff: &symbolDiff,
	}
	if len(names) > 0 {
		finding.Symbols = names
	}
	finding.SymbolsDetail = SymbolsDetailOf(&symbolDiff)
	return finding
}

// analyzedCoverageNote is the coverage note of a fully analyzed run (the port of
// analyzedCoverageNote).
func analyzedCoverageNote(tsFiles int, extracted int, declined int) string {
	if tsFiles == 0 {
		return "analyzed: no .ts/.tsx files in the input — nothing to extract"
	}
	return fmt.Sprintf(
		"analyzed: tree-sitter symbol extraction over %d .ts/.tsx file(s) — %d extracted, %d declined (declared per file); "+
			"symbol-capable: full per-file symbol diffs (added/removed/changed) derive from both-side unified-diff reconstruction; "+
			"non-TS languages remain on the generic path-level analyzer — their absence of symbol capability is the declared coverage gap (D14)",
		tsFiles, extracted, declined)
}

/* ------------------------------------------------------- the symbol diff */

// SymbolChange is one paired symbol that changed semantically (before/after kept
// as evidence). Port of SymbolChange; the change-kind type is reused from
// schemas.SymbolChangeKind (the single source of truth for the wire enum).
type SymbolChange struct {
	Name        string
	Kind        SymbolKind
	ChangeKinds []schemas.SymbolChangeKind
	Before      ExtractedSymbol
	After       ExtractedSymbol
}

// SymbolDiff is the semantic delta between two symbol tables (added/removed/
// changed only). Port of SymbolDiff.
type SymbolDiff struct {
	Added   []ExtractedSymbol
	Removed []ExtractedSymbol
	Changed []SymbolChange
}

// Empty reports whether the diff carries no semantic change.
func (d *SymbolDiff) Empty() bool {
	return d == nil || (len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0)
}

// DiffSymbolTables diffs two symbol tables. A nil `before` means the file did
// not exist before (added file): every `after` symbol is reported as added, and
// removed/changed are empty. Pure and deterministic.
//
// Report ONLY semantic changes:
//   - added   — present in after, absent from before;
//   - removed — present in before, absent from after;
//   - changed — paired symbols (same name AND kind) that differ in one or more
//     semantic dimensions: 'signature' (the signature text changed), 'type'
//     (the written annotations added/removed/changed), 'visibility' (the
//     exported flag flipped) — always in the fixed order signature → type →
//     visibility.
//
// A symbol that only MOVED (line differs) but is otherwise identical is NOT a
// semantic change. Matching keys pair (name, kind) positionally in source
// order, so a name that changes kind (`const Foo` → `function Foo`) is reported
// as removed + added rather than as a fake in-place change. All result lists are
// ordered by (name, kind, line) using code-unit string comparison (never
// locale-dependent comparison).
func DiffSymbolTables(before *SymbolTable, after *SymbolTable) SymbolDiff {
	afterSorted := sortSymbols(afterTableOf(after).Symbols)
	if before == nil {
		return SymbolDiff{Added: afterSorted, Removed: []ExtractedSymbol{}, Changed: []SymbolChange{}}
	}
	beforeSorted := sortSymbols(beforeTableOf(before).Symbols)
	beforeByKey := groupByNameAndKind(beforeSorted)
	afterByKey := groupByNameAndKind(afterSorted)
	keys := make([]string, 0, len(beforeByKey)+len(afterByKey))
	for key := range beforeByKey {
		keys = append(keys, key)
	}
	for key := range afterByKey {
		if _, ok := beforeByKey[key]; !ok {
			keys = append(keys, key)
		}
	}
	sortStringsCodeUnit(keys)
	added := []ExtractedSymbol{}
	removed := []ExtractedSymbol{}
	changed := []SymbolChange{}
	for _, key := range keys {
		beforeList := beforeByKey[key]
		afterList := afterByKey[key]
		pairs := len(beforeList)
		if len(afterList) > pairs {
			pairs = len(afterList)
		}
		for index := 0; index < pairs; index++ {
			if index >= len(beforeList) {
				added = append(added, afterList[index])
				continue
			}
			if index >= len(afterList) {
				removed = append(removed, beforeList[index])
				continue
			}
			changeKinds := changeKindsOf(beforeList[index], afterList[index])
			if len(changeKinds) == 0 {
				continue
			}
			changed = append(changed, SymbolChange{
				Name:        beforeList[index].Name,
				Kind:        beforeList[index].Kind,
				ChangeKinds: changeKinds,
				Before:      beforeList[index],
				After:       afterList[index],
			})
		}
	}
	return SymbolDiff{Added: added, Removed: removed, Changed: changed}
}

// changeKindsOf is the fixed emission order for changeKinds (determinism).
func changeKindsOf(before ExtractedSymbol, after ExtractedSymbol) []schemas.SymbolChangeKind {
	var kinds []schemas.SymbolChangeKind
	if before.Signature != after.Signature {
		kinds = append(kinds, schemas.SymbolChangeSignature)
	}
	if !typeAnnotationsEqual(before.TypeAnnotations, after.TypeAnnotations) {
		kinds = append(kinds, schemas.SymbolChangeType)
	}
	if before.Exported != after.Exported {
		kinds = append(kinds, schemas.SymbolChangeVisibility)
	}
	return kinds
}

func typeAnnotationsEqual(before []string, after []string) bool {
	if len(before) == 0 && len(after) == 0 {
		return true
	}
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i] != after[i] {
			return false
		}
	}
	return true
}

// sortSymbols sorts by (name, kind, line) using code-unit comparison.
func sortSymbols(symbols []ExtractedSymbol) []ExtractedSymbol {
	sorted := append([]ExtractedSymbol(nil), symbols...)
	sortStrings := func(list []ExtractedSymbol) {
		// insertion-free, allocation-light sort: (name, kind, line)
		for i := 1; i < len(list); i++ {
			for j := i; j > 0 && symbolLess(list[j], list[j-1]); j-- {
				list[j], list[j-1] = list[j-1], list[j]
			}
		}
	}
	sortStrings(sorted)
	return sorted
}

func symbolLess(a ExtractedSymbol, b ExtractedSymbol) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Line < b.Line
}

func groupByNameAndKind(symbols []ExtractedSymbol) map[string][]ExtractedSymbol {
	groups := make(map[string][]ExtractedSymbol)
	for _, symbol := range symbols {
		key := symbol.Name + "\u0000" + string(symbol.Kind)
		groups[key] = append(groups[key], symbol)
	}
	return groups
}

func sortStringsCodeUnit(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func beforeTableOf(before *SymbolTable) SymbolTable {
	if before == nil {
		return SymbolTable{}
	}
	return *before
}

func afterTableOf(after *SymbolTable) SymbolTable {
	if after == nil {
		return SymbolTable{}
	}
	return *after
}
