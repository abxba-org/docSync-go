// Ported from packages/core/src/analysis/plugin.ts (P1-T06; ARCH §4.1/§15.2;
// OD-4).
//
// This file is the ONLY seam between the deterministic orchestrator and the
// analyzer packages: the TypeScript analyzer (tree-sitter) first, the generic
// path-level analyzer always present. Plugins are pure by construction — they
// receive everything they need in AnalyzerInput and must not touch fs,
// network, git, or the clock, so ChangeAnalysis stays deterministic and
// cacheable by blob SHAs (ARCH §4.1).
//
// Failure semantics (D14 — analyzer failure is inconclusive, never negative):
//   - a plugin that fails is captured as coverage `{ status: 'unavailable',
//     note }` and composition CONTINUES with the remaining plugins; degradation
//     is declared in the run record, never silent and never fatal (the Go
//     spelling of the TS throw is an error return from Analyze);
//   - a stub plugin (Phase 1 TypeScript analyzer: no AST until Phase 11 F11-1)
//     declares `{ status: 'stub', note }` and emits no symbol/type-level
//     findings — its coverage entry says so;
//   - if every plugin fails or is unavailable, the composed run still carries a
//     coverage entry per plugin and simply reports no findings; the
//     ChangeAnalysis builder (build.go) decides 'inconclusive' from that.
//
// Merge semantics across plugins (documented contract):
//   - booleans (symbols_changed, api_diff, config_diff, deep_declined)
//     OR-merge: a later plugin can never clobber a `true` an earlier plugin
//     established (under-reporting is not allowed to erase evidence);
//   - symbols concatenate with first-seen-order dedupe; notes concatenate in
//     plugin order;
//   - symbols_detail (P11-T03/E2 structured symbol evidence) is a first-seen
//     field: the OD-4-first symbol-capable plugin owns it and a later plugin
//     without one never erases it (same under-reporting rule as the booleans);
//   - a path reported by NO plugin yields no finding: absence of a finding plus
//     the declared coverage status is how "unanalyzed" is represented — never
//     an all-false row that would read as a negative result (D14).

package analysis

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AnalyzerInputFile is a single changed file handed to every plugin (purity:
// no self-sourced I/O).
type AnalyzerInputFile struct {
	Path   string
	Status schemas.FileStatus
	// OldPath is the previous path when Status is 'renamed'.
	OldPath *string
	// Patch is the unified diff when available; plugins must treat absence as
	// unknown.
	Patch *string
}

// AnalyzerInput is everything a plugin needs — plugins must NOT do
// fs/network/git themselves.
type AnalyzerInput struct {
	Files []AnalyzerInputFile
	// DeepAllowed is whether deep (AST/API/schema) analysis is permitted for
	// this run (ARCH §3.7); beyond caps the run downgrades to path-level and
	// the run record declares `deep_analysis: skipped`.
	DeepAllowed bool
	// Notes are orchestrator context notes (e.g. why deep analysis was capped).
	Notes []string
}

// PerFileFinding is one file's findings from ONE plugin. SymbolsChanged /
// APIDiff / ConfigDiff are required booleans so a plugin can never half-report
// a file it did analyze.
type PerFileFinding struct {
	Path           string
	SymbolsChanged bool
	Symbols        []string
	APIDiff        bool
	ConfigDiff     bool
	// DeepDeclined is the plugin's own decline of deep analysis for this file
	// by its own judgment/coverage (e.g. language without tooling) — declared,
	// never silent (D14).
	DeepDeclined bool
	Notes        []string
	// SymbolsDetail is the structured symbol evidence (P11-T03, E2): the
	// schema-level name diff projection derived by a symbol-capable analyzer
	// (e.g. the tree-sitter TypeScript analyzer). Absent = no symbol evidence
	// for this file.
	SymbolsDetail *schemas.SymbolsDetail
}

// AnalyzerOutput is one plugin run's result: exactly one coverage entry plus
// per-file findings.
type AnalyzerOutput struct {
	Coverage schemas.AnalyzerCoverage
	Files    []PerFileFinding
}

// AnalyzerPlugin is the analyzer plugin contract.
//
// PURITY CONTRACT: an analyzer plugin must not perform filesystem, network,
// git, or clock access, and must be deterministic for identical input — all
// facts it consumes arrive in AnalyzerInput; everything it produces is
// returned in AnalyzerOutput. This keeps analysis runs reproducible and safe
// to re-run/cache by blob SHAs (ARCH §4.1).
type AnalyzerPlugin interface {
	// Name is the registry name of this analyzer (`ANALYZER_NAME`); unique per
	// composition.
	Name() schemas.AnalyzerName
	// Analyze runs the plugin. A returned error is the Go spelling of the TS
	// throw: composition captures it as an 'unavailable' coverage entry with an
	// explanatory note and continues with the remaining plugins (D14).
	Analyze(input AnalyzerInput) (AnalyzerOutput, error)
}

// AnalyzerPluginOrder is the OD-4 fixed plugin precedence: the TypeScript
// analyzer runs FIRST; the generic path-level analyzer is ALWAYS present after
// it. Composition in any other order is rejected (fail closed), never silently
// reordered.
var AnalyzerPluginOrder = []schemas.AnalyzerName{
	schemas.AnalyzerNameTypeScript,
	schemas.AnalyzerNameGeneric,
}

// TypeScriptStubNote is the byte-stable stub note — verbatim
// TYPESCRIPT_STUB_NOTE from packages/analyzer-typescript/src/index.ts (the
// declared V1 coverage gap itself, V-P1-13 groundwork). It lives at the
// plugin seam because the seam is where analyzer coverage is declared: the
// schemas package carries its own copy inside ChangeAnalysisExample (it cannot
// import the analyzer: dependency direction), and every analyzer package
// re-declares it verbatim. Analyzer/production code should reference this
// constant rather than re-typing the literal.
const TypeScriptStubNote = "stub: no AST/symbol extraction in V1 — tree-sitter symbol/type-level analysis lands in Phase 11 (F11-1); per-file symbol diffs are a declared V1 coverage gap"

// ComposeAnalyzersOptions configures ComposeAnalyzers.
type ComposeAnalyzersOptions struct {
	// Order is an explicit evaluation order. Must be a complete permutation of
	// the known analyzer names (every `ANALYZER_NAME` exactly once) with
	// 'typescript' before 'generic' (OD-4) — anything else is an error
	// (fail closed).
	Order []schemas.AnalyzerName
}

// ComposedAnalyzerRun is the findings and coverage from one composed run over
// all plugins.
type ComposedAnalyzerRun struct {
	// Files are the merged per-file findings (first-seen path order; no finding
	// = unanalyzed).
	Files []PerFileFinding
	// Coverage is one coverage entry per composed plugin, in enforced
	// evaluation order.
	Coverage []schemas.AnalyzerCoverage
}

// ComposedAnalyzers is a composed, OD-4-ordered analyzer lineup ready to run.
type ComposedAnalyzers struct {
	order   []schemas.AnalyzerName
	plugins []AnalyzerPlugin
}

// Order is the enforced evaluation order (OD-4: typescript before generic).
func (c *ComposedAnalyzers) Order() []schemas.AnalyzerName {
	return append([]schemas.AnalyzerName(nil), c.order...)
}

// RunAnalyzers runs every composed plugin sequentially in order, merging
// findings. It resolves even when plugins fail — each failure becomes an
// 'unavailable' coverage entry with an explanatory note (D14).
func (c *ComposedAnalyzers) RunAnalyzers(input AnalyzerInput) ComposedAnalyzerRun {
	byPath := make(map[string]PerFileFinding)
	paths := make([]string, 0, len(input.Files))
	coverage := make([]schemas.AnalyzerCoverage, 0, len(c.plugins))
	for _, plugin := range c.plugins {
		output, err := plugin.Analyze(input)
		if err != nil {
			coverage = append(coverage, schemas.AnalyzerCoverage{
				Analyzer: plugin.Name(),
				Status:   schemas.AnalyzerStatusUnavailable,
				Note:     strPtr(describePluginFailure(plugin.Name(), err)),
			})
			continue
		}
		coverage = append(coverage, output.Coverage)
		for _, finding := range output.Files {
			existing, ok := byPath[finding.Path]
			if !ok {
				byPath[finding.Path] = finding
				paths = append(paths, finding.Path)
				continue
			}
			byPath[finding.Path] = mergeFindings(existing, finding)
		}
	}
	files := make([]PerFileFinding, 0, len(paths))
	for _, path := range paths {
		files = append(files, byPath[path])
	}
	return ComposedAnalyzerRun{Files: files, Coverage: coverage}
}

// failCompose builds the composition error (TS `failCompose` throws a
// TypeError with this exact text).
func failCompose(message string) error {
	return docutil.InternalError("composeAnalyzers: %s", message)
}

// knownAnalyzerName reports whether name is a known analyzer.
func knownAnalyzerName(name schemas.AnalyzerName) bool {
	for _, known := range AnalyzerPluginOrder {
		if known == name {
			return true
		}
	}
	return false
}

// containsAnalyzerName reports whether names contains name.
func containsAnalyzerName(names []schemas.AnalyzerName, name schemas.AnalyzerName) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// indexOfAnalyzerName is the index of name in names, or -1.
func indexOfAnalyzerName(names []schemas.AnalyzerName, name schemas.AnalyzerName) int {
	for i, candidate := range names {
		if candidate == name {
			return i
		}
	}
	return -1
}

// joinAnalyzerNames renders names comma-separated (TS `KNOWN_ANALYZER_NAMES.join(', ')`).
func joinAnalyzerNames(names []schemas.AnalyzerName) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, string(name))
	}
	return strings.Join(parts, ", ")
}

// quoteJoinAnalyzerNames renders names singly-quoted and comma-separated
// (TS `['${order.join("', '")}']`).
func quoteJoinAnalyzerNames(names []schemas.AnalyzerName) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, "'"+string(name)+"'")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// validateOrder is the TS validateOrder: an explicit order must be a complete,
// duplicate-free permutation of the known analyzer names with 'typescript'
// before 'generic' (OD-4).
func validateOrder(order []schemas.AnalyzerName) error {
	if len(order) == 0 {
		return failCompose("explicit order is empty — a composition must declare a full analyzer order")
	}
	seen := make(map[schemas.AnalyzerName]bool, len(order))
	for _, name := range order {
		if !knownAnalyzerName(name) {
			return failCompose(fmt.Sprintf(
				"unknown analyzer name '%s' in order — known names: %s",
				name, joinAnalyzerNames(AnalyzerPluginOrder)))
		}
		if seen[name] {
			return failCompose(fmt.Sprintf("analyzer '%s' appears more than once in order", name))
		}
		seen[name] = true
	}
	for _, required := range AnalyzerPluginOrder {
		if !seen[required] {
			return failCompose(fmt.Sprintf(
				"order is missing required analyzer '%s' — order must be a complete permutation of: %s",
				required, joinAnalyzerNames(AnalyzerPluginOrder)))
		}
	}
	if indexOfAnalyzerName(order, schemas.AnalyzerNameTypeScript) >
		indexOfAnalyzerName(order, schemas.AnalyzerNameGeneric) {
		return failCompose(fmt.Sprintf(
			"order %s violates OD-4: 'typescript' must run before 'generic' (never silently reordered)",
			quoteJoinAnalyzerNames(order)))
	}
	return nil
}

// validatePlugins is the TS validatePlugins: a non-empty, duplicate-free plugin
// set fully covered by the declared order.
func validatePlugins(plugins []AnalyzerPlugin, order []schemas.AnalyzerName) error {
	if len(plugins) == 0 {
		return failCompose("no analyzer plugins given — an empty composition would emit no coverage entries (fail closed)")
	}
	seen := make(map[schemas.AnalyzerName]bool, len(plugins))
	for _, plugin := range plugins {
		if seen[plugin.Name()] {
			return failCompose(fmt.Sprintf("duplicate analyzer plugin name '%s'", plugin.Name()))
		}
		seen[plugin.Name()] = true
	}
	// Iterate plugins (not the seen set) so the first uncovered name in plugin
	// order wins — deterministic, matching the TS insertion-ordered Set walk.
	for _, plugin := range plugins {
		if !containsAnalyzerName(order, plugin.Name()) {
			return failCompose(fmt.Sprintf(
				"analyzer plugin '%s' is not covered by the declared order — it would be silently skipped (fail closed)",
				plugin.Name()))
		}
	}
	return nil
}

// mergeStrings is the first-seen-order union of two optional string lists
// (used for symbols/notes).
func mergeStrings(base, incoming []string) []string {
	if base == nil && incoming == nil {
		return nil
	}
	merged := make([]string, 0, len(base)+len(incoming))
	seen := make(map[string]bool, len(base)+len(incoming))
	for _, value := range base {
		if !seen[value] {
			seen[value] = true
			merged = append(merged, value)
		}
	}
	for _, value := range incoming {
		if !seen[value] {
			seen[value] = true
			merged = append(merged, value)
		}
	}
	return merged
}

// mergeFindings OR-merges a later finding into an earlier one for the same
// path.
func mergeFindings(base, incoming PerFileFinding) PerFileFinding {
	merged := PerFileFinding{
		Path:           base.Path,
		SymbolsChanged: base.SymbolsChanged || incoming.SymbolsChanged,
		APIDiff:        base.APIDiff || incoming.APIDiff,
		ConfigDiff:     base.ConfigDiff || incoming.ConfigDiff,
	}
	if symbols := mergeStrings(base.Symbols, incoming.Symbols); symbols != nil {
		merged.Symbols = symbols
	}
	if notes := mergeStrings(base.Notes, incoming.Notes); notes != nil {
		merged.Notes = notes
	}
	if base.DeepDeclined || incoming.DeepDeclined {
		merged.DeepDeclined = true
	}
	// First-seen wins (OD-4: the symbol-capable TypeScript plugin runs first);
	// a later plugin without symbol evidence never erases an earlier one's.
	if base.SymbolsDetail != nil {
		merged.SymbolsDetail = base.SymbolsDetail
	} else if incoming.SymbolsDetail != nil {
		merged.SymbolsDetail = incoming.SymbolsDetail
	}
	return merged
}

// describePluginFailure renders the declared coverage-gap note for a failed
// plugin (TS describePluginFailure).
func describePluginFailure(name schemas.AnalyzerName, err error) string {
	detail := "no error detail available"
	if err != nil {
		if msg := strings.TrimSpace(err.Error()); msg != "" {
			detail = msg
		}
	}
	return fmt.Sprintf(
		"analyzer '%s' failed: %s — analysis continued without it (declared coverage gap, D14)",
		name, detail)
}

// ComposeAnalyzers composes analyzer plugins into a single OD-4-ordered runner.
//
// Fail closed (an error whose message is the TS TypeError text):
//   - plugins is empty (an empty composition would emit no coverage entries);
//   - duplicate plugin names;
//   - an explicit order that is not a complete permutation of the known
//     analyzer names, or in which 'typescript' does not precede 'generic'
//     (OD-4) — out-of-order composition is NEVER silently reordered;
//   - a plugin whose name the order does not cover (it would be silently
//     skipped).
func ComposeAnalyzers(plugins []AnalyzerPlugin, opts *ComposeAnalyzersOptions) (*ComposedAnalyzers, error) {
	order := AnalyzerPluginOrder
	if opts != nil && opts.Order != nil {
		order = opts.Order
	}
	if err := validateOrder(order); err != nil {
		return nil, err
	}
	if err := validatePlugins(plugins, order); err != nil {
		return nil, err
	}

	byName := make(map[schemas.AnalyzerName]AnalyzerPlugin, len(plugins))
	for _, plugin := range plugins {
		byName[plugin.Name()] = plugin
	}
	ordered := make([]AnalyzerPlugin, 0, len(order))
	for _, name := range order {
		if plugin, ok := byName[name]; ok {
			ordered = append(ordered, plugin)
		}
	}
	return &ComposedAnalyzers{
		order:   append([]schemas.AnalyzerName(nil), order...),
		plugins: ordered,
	}, nil
}
