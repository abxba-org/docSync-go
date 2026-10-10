// Ported from packages/analyzer-generic/test/analyzer-generic.test.ts.
package generic_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/analyzers/generic"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// pathOnlyFiles is the port of the TS `pathOnlyFiles` fixture set: three paths,
// INCLUDING '.d.ts' (TypeScript declaration files — deliberately NOT reduced to
// the 'typescript' family, so the generic analyzer's input stays pure paths).
func pathOnlyFiles() []analysis.AnalyzerInputFile {
	return []analysis.AnalyzerInputFile{
		{Path: "src/index.ts", Status: schemas.FileStatusAdded},
		{Path: "src/lib/util.ts", Status: schemas.FileStatusModified},
		{Path: "yarn.lock", Status: schemas.FileStatusModified},
	}
}

// fixedTypescriptOutputPlugin is the minimal "preceding typescript plugin"
// from the TS composition test: it reports the first two paths with
// symbols_changed and api_diff true.
type fixedTypescriptOutputPlugin struct{}

func (fixedTypescriptOutputPlugin) Name() schemas.AnalyzerName {
	return schemas.AnalyzerNameTypeScript
}

func (fixedTypescriptOutputPlugin) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	note := "typescript fixture: symbols reported for the first two paths"
	files := make([]analysis.PerFileFinding, 0, len(input.Files))
	for i, file := range input.Files {
		if i >= 2 {
			break
		}
		files = append(files, analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: true,
			APIDiff:        true,
			ConfigDiff:     false,
		})
	}
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusOK,
			Note:     &note,
		},
		Files: files,
	}, nil
}

func TestCreateGenericAnalyzer_DeclaresCoverageAndDeepDeclinesEveryFile(t *testing.T) {
	output, err := generic.CreateGenericAnalyzer().Analyze(analysis.AnalyzerInput{Files: pathOnlyFiles()})
	require.NoError(t, err)

	assert.Equal(t, schemas.AnalyzerNameGeneric, output.Coverage.Analyzer)
	assert.Equal(t, schemas.AnalyzerStatusOK, output.Coverage.Status)
	require.NotNil(t, output.Coverage.Note)
	assert.Equal(t, generic.GenericCoverageNote, *output.Coverage.Note)

	// Every input file → exactly one finding, in input order.
	require.Len(t, output.Files, 3)
	for i, finding := range output.Files {
		assert.Equal(t, pathOnlyFiles()[i].Path, finding.Path)
		// The coverage gap is DECLARED per file (never a silent negative).
		assert.True(t, finding.DeepDeclined)
		assert.False(t, finding.SymbolsChanged)
		assert.False(t, finding.APIDiff)
		assert.False(t, finding.ConfigDiff)
		require.Len(t, finding.Notes, 1)
		assert.Equal(t, generic.GenericPathLevelNote, finding.Notes[0])
	}
}

func TestCreateGenericAnalyzer_EmptyInput(t *testing.T) {
	output, err := generic.CreateGenericAnalyzer().Analyze(analysis.AnalyzerInput{Files: []analysis.AnalyzerInputFile{}})
	require.NoError(t, err)
	// Coverage is 'ok' even for the empty input; the findings list is empty.
	assert.Equal(t, schemas.AnalyzerNameGeneric, output.Coverage.Analyzer)
	assert.Equal(t, schemas.AnalyzerStatusOK, output.Coverage.Status)
	assert.NotNil(t, output.Files)
	assert.Empty(t, output.Files)
}

func TestCreateGenericAnalyzer_Deterministic(t *testing.T) {
	analyzer := generic.CreateGenericAnalyzer()
	first, err := analyzer.Analyze(analysis.AnalyzerInput{Files: pathOnlyFiles()})
	require.NoError(t, err)
	second, err := analyzer.Analyze(analysis.AnalyzerInput{Files: pathOnlyFiles()})
	require.NoError(t, err)
	assert.Equal(t, first, second)
	// Findings preserve input order (same paths, same order).
	require.Len(t, second.Files, 3)
	assert.Equal(t, "src/index.ts", second.Files[0].Path)
	assert.Equal(t, "src/lib/util.ts", second.Files[1].Path)
	assert.Equal(t, "yarn.lock", second.Files[2].Path)
}

// TestGenericAnalyzer_DeepDeclineIsIndependentOfDeepAllowed pins the declared
// V1 semantics: path-level findings are identical whether deep analysis is
// permitted or not.
func TestGenericAnalyzer_DeepDeclineIsIndependentOfDeepAllowed(t *testing.T) {
	allowed, err := generic.GenericAnalyzer.Analyze(analysis.AnalyzerInput{Files: pathOnlyFiles(), DeepAllowed: true})
	require.NoError(t, err)
	forbidden, err := generic.GenericAnalyzer.Analyze(analysis.AnalyzerInput{Files: pathOnlyFiles(), DeepAllowed: false})
	require.NoError(t, err)
	assert.Equal(t, allowed, forbidden)
}

// TestGenericAnalyzer_InCompositionNeverClobbersEarlierFindings is the port of
// the composition-compat test (OD-4: 'typescript' first, 'generic' last).
func TestGenericAnalyzer_InCompositionNeverClobbersEarlierFindings(t *testing.T) {
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		fixedTypescriptOutputPlugin{},
		generic.GenericAnalyzer,
	}, nil)
	require.NoError(t, err)
	files := pathOnlyFiles()[:2]
	run := composed.RunAnalyzers(analysis.AnalyzerInput{Files: files})

	// Coverage: both entries, OD-4 order ('typescript' first).
	require.Len(t, run.Coverage, 2)
	assert.Equal(t, schemas.AnalyzerNameTypeScript, run.Coverage[0].Analyzer)
	assert.Equal(t, schemas.AnalyzerNameGeneric, run.Coverage[1].Analyzer)
	assert.Equal(t, schemas.AnalyzerStatusOK, run.Coverage[1].Status)

	// The generic analyzer never contradicts the earlier plugin (all-false +
	// deep_declined), so its findings are logically clobbered on the two paths
	// the preceding plugin already reported — the OR-merge keeps those booleans,
	// and the generic note rides the merged finding.
	require.Len(t, run.Files, 2)
	for i, finding := range run.Files {
		assert.Equal(t, files[i].Path, finding.Path)
		assert.True(t, finding.SymbolsChanged)
		assert.True(t, finding.APIDiff)
		assert.Equal(t, []string{generic.GenericPathLevelNote}, finding.Notes)
	}
}

// TestGenericAnalyzer_NotesUnionMergeDocumented pins the Go merge behavior when
// BOTH plugins carry notes and documents its divergence from the TS suite.
//
// DEVIATION (observed, owned by internal/analysis — NOT changed here): the TS
// merge made the LATER plugin's notes win verbatim, while the Go port
// (analysis.mergeFindings → mergeStrings, internal/analysis/plugin.go:322)
// unions the note lists in first-seen order. Notes are plugin-layer evidence
// only (they are not part of the ChangeAnalysis wire record), so no wire format
// changes; the union keeps MORE declared information rather than dropping the
// earlier plugin's note. Pinned here so the behavior cannot drift silently —
// the divergence is documented, not absorbed.
func TestGenericAnalyzer_NotesUnionMergeDocumented(t *testing.T) {
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		notedTypescriptOutputPlugin{},
		generic.GenericAnalyzer,
	}, nil)
	require.NoError(t, err)
	run := composed.RunAnalyzers(analysis.AnalyzerInput{Files: pathOnlyFiles()[:1]})

	require.Len(t, run.Files, 1)
	assert.Equal(t, []string{
		"typescript fixture: symbols reported for the first two paths",
		generic.GenericPathLevelNote,
	}, run.Files[0].Notes)
}

// notedTypescriptOutputPlugin is the preceding plugin WITH a per-file note (the
// note-merge divergence fixture).
type notedTypescriptOutputPlugin struct{}

func (notedTypescriptOutputPlugin) Name() schemas.AnalyzerName {
	return schemas.AnalyzerNameTypeScript
}

func (notedTypescriptOutputPlugin) Analyze(input analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	note := "typescript fixture: symbols reported for the first two paths"
	files := make([]analysis.PerFileFinding, 0, len(input.Files))
	for i, file := range input.Files {
		if i >= 2 {
			break
		}
		files = append(files, analysis.PerFileFinding{
			Path:           file.Path,
			SymbolsChanged: true,
			APIDiff:        true,
			ConfigDiff:     false,
			Notes:          []string{note},
		})
	}
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusOK,
			Note:     &note,
		},
		Files: files,
	}, nil
}

// TestGenericAnalyzer_NameIsStable pins the registry identity (OD-4 depends on
// it — a rename would break composition ordering).
func TestGenericAnalyzer_NameIsStable(t *testing.T) {
	assert.Equal(t, schemas.AnalyzerNameGeneric, generic.GenericAnalyzer.Name())
	assert.Equal(t, schemas.AnalyzerNameGeneric, generic.CreateGenericAnalyzer().Name())
	var _ analysis.AnalyzerPlugin = generic.GenericAnalyzer
}

// TestGenericAnalyzer_IsLastInPluginOrder pins the OD-4 contract from this
// package's side: 'typescript' runs first, 'generic' is ALWAYS present last.
func TestGenericAnalyzer_IsLastInPluginOrder(t *testing.T) {
	require.Len(t, analysis.AnalyzerPluginOrder, 2)
	assert.Equal(t, schemas.AnalyzerNameTypeScript, analysis.AnalyzerPluginOrder[0])
	assert.Equal(t, schemas.AnalyzerNameGeneric, analysis.AnalyzerPluginOrder[len(analysis.AnalyzerPluginOrder)-1])
}
