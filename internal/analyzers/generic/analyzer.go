// Ported from packages/analyzer-generic/src/index.ts (P1-T07; ARCH §3.7/§4.1;
// OD-4) — the ALWAYS-PRESENT path-level analyzer.
//
// Deterministic and pure: identical input yields deep-equal output; findings
// preserve input file order. Every input file gets exactly one finding carrying
// `deep_declined: true` plus the stable per-file note, so a file this plugin
// did not (and cannot) analyze is DECLARED as a coverage gap rather than
// reported as an assessed negative (D14, ARCH §3.7, V-P1-13).
package generic

import (
	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AnalyzerGenericPackageVersion is the package version (0.0.0 until first
// release) — the port of ANALYZER_GENERIC_PACKAGE_VERSION.
const AnalyzerGenericPackageVersion = "0.0.0"

// GenericPathLevelNote is the stable per-file note (part of the
// ChangeAnalysis evidence surface — treat as golden-stable; changes here are
// policy-relevant, not cosmetic). Verbatim GENERIC_PATH_LEVEL_NOTE.
const GenericPathLevelNote = "generic path-level analyzer (V1): no symbol/AST extraction — declared coverage gap"

// GenericCoverageNote is the stable coverage note declared in every run record
// (V-P1-13 groundwork). Verbatim GENERIC_COVERAGE_NOTE.
const GenericCoverageNote = "path-level analyzer, always present (OD-4); symbol/type-level analysis lands Phase 11 (F11-1/F11-2)"

// PathLevelAnalyzer is the generic path-level analyzer plugin (the port of
// `createGenericAnalyzer`). The zero-size value type is immutability by
// construction: the TS `Object.freeze` had nothing left to protect.
type PathLevelAnalyzer struct{}

// Name is the registry name of this analyzer — 'generic', always present LAST
// in the OD-4 order.
func (PathLevelAnalyzer) Name() schemas.AnalyzerName { return schemas.AnalyzerNameGeneric }

// Analyze declares the path-level coverage gap for every input file. It does
// not claim symbols, never contradicts an earlier plugin (composition
// OR-merges booleans, and all of these are false), and never returns an error
// — the plugin is always available.
func (PathLevelAnalyzer) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	files := make([]analysis.PerFileFinding, 0, len(input.Files))
	for _, file := range input.Files {
		files = append(files, analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: false,
			APIDiff:        false,
			ConfigDiff:     false,
			DeepDeclined:   true,
			Notes:          []string{GenericPathLevelNote},
		})
	}
	note := GenericCoverageNote
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameGeneric,
			Status:   schemas.AnalyzerStatusOK,
			Note:     &note,
		},
		Files: files,
	}, nil
}

// CreateGenericAnalyzer creates the generic path-level analyzer plugin (OD-4:
// always the last plugin in the composition). The returned value is stateless,
// so callers never need a shared instance — {@link GenericAnalyzer} exists for
// symmetry with the TS singleton.
func CreateGenericAnalyzer() PathLevelAnalyzer { return PathLevelAnalyzer{} }

// GenericAnalyzer is the shared stateless instance — safe to reuse across runs
// (pure plugin). The port of GENERIC_ANALYZER.
var GenericAnalyzer analysis.AnalyzerPlugin = PathLevelAnalyzer{}

// Compile-time contract assertions: the plugin satisfies the plugin seam.
var (
	_ analysis.AnalyzerPlugin = PathLevelAnalyzer{}
	_ analysis.AnalyzerPlugin = GenericAnalyzer
)
