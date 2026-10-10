// Ported from packages/core/test/analysis-plugin.test.ts (P1-T06, OD-4;
// P11-T03/E2) — package analysis_test (black-box, testify), per
// CONVENTIONS.md.
//
// Helper names are deliberately distinct from the sibling suites sharing this
// test package (the detect/semantics/caps doubles): the TS suite kept per-file
// module scope, Go shares one package scope.
package analysis_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// pluginBaseInput mirrors BASE_INPUT from analysis-plugin.test.ts.
func pluginBaseInput() analysis.AnalyzerInput {
	return analysis.AnalyzerInput{
		Files: []analysis.AnalyzerInputFile{
			{Path: "src/payments/refund.ts", Status: schemas.FileStatusModified},
			{Path: "src/payments/refund.test.ts", Status: schemas.FileStatusAdded},
		},
		DeepAllowed: true,
	}
}

// testStrPtr returns a pointer to s (test-only optional-string helper).
func testStrPtr(s string) *string { return &s }

// fixedOutputPlugin mirrors staticPlugin: a plugin returning a fixed output,
// or a fixed error (the Go spelling of the TS throw).
type fixedOutputPlugin struct {
	name   schemas.AnalyzerName
	output analysis.AnalyzerOutput
	err    error
}

func (p fixedOutputPlugin) Name() schemas.AnalyzerName { return p.name }

func (p fixedOutputPlugin) Analyze(analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	return p.output, p.err
}

// throwingAnalyzerPlugin mirrors throwingPlugin.
func throwingAnalyzerPlugin(name schemas.AnalyzerName, err error) analysis.AnalyzerPlugin {
	return fixedOutputPlugin{name: name, err: err}
}

// perFileFinding mirrors the finding() helper: the three required booleans
// default to false so a plugin can never half-report a file it did analyze.
func perFileFinding(path string, modifiers ...func(*analysis.PerFileFinding)) analysis.PerFileFinding {
	out := analysis.PerFileFinding{Path: path}
	for _, modify := range modifiers {
		modify(&out)
	}
	return out
}

// tsStubAnalyzerOutput mirrors tsStubOutput: the V1 stub declares its gap and
// emits no findings.
func tsStubAnalyzerOutput() analysis.AnalyzerOutput {
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameTypeScript,
			Status:   schemas.AnalyzerStatusStub,
			Note:     testStrPtr(analysis.TypeScriptStubNote),
		},
		Files: nil,
	}
}

// genericAnalyzerOutput mirrors genericOutput: one path-level finding with
// config_diff true.
func genericAnalyzerOutput() analysis.AnalyzerOutput {
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{
			Analyzer: schemas.AnalyzerNameGeneric,
			Status:   schemas.AnalyzerStatusOK,
		},
		Files: []analysis.PerFileFinding{
			perFileFinding("src/payments/refund.ts", func(f *analysis.PerFileFinding) {
				f.ConfigDiff = true
			}),
		},
	}
}

// callOrderPlugin records its invocation order (mirrors the closure-capturing
// TS doubles).
type callOrderPlugin struct {
	name  schemas.AnalyzerName
	order *[]schemas.AnalyzerName
	files []analysis.PerFileFinding
}

func (p *callOrderPlugin) Name() schemas.AnalyzerName { return p.name }

func (p *callOrderPlugin) Analyze(analysis.AnalyzerInput) (analysis.AnalyzerOutput, error) {
	*p.order = append(*p.order, p.name)
	return analysis.AnalyzerOutput{
		Coverage: schemas.AnalyzerCoverage{Analyzer: p.name, Status: schemas.AnalyzerStatusOK},
		Files:    p.files,
	}, nil
}

func TestComposeAnalyzers_OD4DefaultOrderExposesTypeScriptBeforeGeneric(t *testing.T) {
	assert.Equal(t,
		[]schemas.AnalyzerName{schemas.AnalyzerNameTypeScript, schemas.AnalyzerNameGeneric},
		analysis.AnalyzerPluginOrder)
}

func TestComposeAnalyzers_RejectsUnknownOrderAndDuplicatePluginNames(t *testing.T) {
	ts := fixedOutputPlugin{name: schemas.AnalyzerNameTypeScript, output: tsStubAnalyzerOutput()}
	generic := fixedOutputPlugin{name: schemas.AnalyzerNameGeneric, output: genericAnalyzerOutput()}
	lineup := []analysis.AnalyzerPlugin{ts, generic}

	// Unknown analyzer name in the explicit order.
	_, err := analysis.ComposeAnalyzers(lineup, &analysis.ComposeAnalyzersOptions{
		Order: []schemas.AnalyzerName{schemas.AnalyzerNameTypeScript, schemas.AnalyzerName("mystery")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "composeAnalyzers: ")

	// Out-of-order composition (OD-4) is never silently reordered.
	_, err = analysis.ComposeAnalyzers(lineup, &analysis.ComposeAnalyzersOptions{
		Order: []schemas.AnalyzerName{schemas.AnalyzerNameGeneric, schemas.AnalyzerNameTypeScript},
	})
	require.Error(t, err)

	// Duplicate entries in the order.
	_, err = analysis.ComposeAnalyzers(lineup, &analysis.ComposeAnalyzersOptions{
		Order: []schemas.AnalyzerName{
			schemas.AnalyzerNameTypeScript, schemas.AnalyzerNameTypeScript, schemas.AnalyzerNameGeneric,
		},
	})
	require.Error(t, err)

	// Incomplete order (missing a known analyzer).
	_, err = analysis.ComposeAnalyzers(lineup, &analysis.ComposeAnalyzersOptions{
		Order: []schemas.AnalyzerName{schemas.AnalyzerNameTypeScript},
	})
	require.Error(t, err)

	// Explicit but EMPTY order: a composition must declare a full analyzer
	// order (the Go spelling of the TS nullish-vs-empty-array distinction —
	// an empty slice is an explicit order, not "unset").
	_, err = analysis.ComposeAnalyzers(lineup, &analysis.ComposeAnalyzersOptions{
		Order: []schemas.AnalyzerName{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "explicit order is empty")

	// Duplicate plugin names.
	_, err = analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		ts,
		fixedOutputPlugin{name: schemas.AnalyzerNameTypeScript, output: tsStubAnalyzerOutput()},
	}, nil)
	require.Error(t, err)

	// A plugin whose name the order does not cover would be silently skipped.
	_, err = analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		fixedOutputPlugin{name: schemas.AnalyzerName("mystery"), output: tsStubAnalyzerOutput()},
		generic,
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not covered by the declared order")

	// Empty composition would emit no coverage entries — fail closed.
	_, err = analysis.ComposeAnalyzers(nil, nil)
	require.Error(t, err)
}

func TestComposeAnalyzers_TypeScriptRunsFirstAndFindingsORMergeWithoutClobbering(t *testing.T) {
	callOrder := []schemas.AnalyzerName{}
	tsPlugin := &callOrderPlugin{
		name:  schemas.AnalyzerNameTypeScript,
		order: &callOrder,
		files: []analysis.PerFileFinding{
			perFileFinding("src/payments/refund.ts", func(f *analysis.PerFileFinding) {
				f.SymbolsChanged = true
				f.Symbols = []string{"refundInvoice"}
				f.Notes = []string{"ts: symbol delta"}
			}),
		},
	}
	genericPlugin := &callOrderPlugin{
		name:  schemas.AnalyzerNameGeneric,
		order: &callOrder,
		files: []analysis.PerFileFinding{
			perFileFinding("src/payments/refund.ts", func(f *analysis.PerFileFinding) {
				f.APIDiff = true
				f.ConfigDiff = true
				f.Symbols = []string{"refundInvoice", "chargeCard"}
				f.Notes = []string{"generic: path delta"}
			}),
			perFileFinding("src/payments/refund.test.ts", func(f *analysis.PerFileFinding) {
				f.DeepDeclined = true
			}),
		},
	}

	// Composition array order is irrelevant: OD-4 order is enforced.
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{genericPlugin, tsPlugin}, nil)
	require.NoError(t, err)
	assert.Equal(t,
		[]schemas.AnalyzerName{schemas.AnalyzerNameTypeScript, schemas.AnalyzerNameGeneric},
		composed.Order())
	result := composed.RunAnalyzers(pluginBaseInput())

	assert.Equal(t,
		[]schemas.AnalyzerName{schemas.AnalyzerNameTypeScript, schemas.AnalyzerNameGeneric},
		callOrder)
	assert.Equal(t, []schemas.AnalyzerCoverage{
		{Analyzer: schemas.AnalyzerNameTypeScript, Status: schemas.AnalyzerStatusOK},
		{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK},
	}, result.Coverage)

	var refund *analysis.PerFileFinding
	var testFile *analysis.PerFileFinding
	for i := range result.Files {
		switch result.Files[i].Path {
		case "src/payments/refund.ts":
			refund = &result.Files[i]
		case "src/payments/refund.test.ts":
			testFile = &result.Files[i]
		}
	}
	require.NotNil(t, refund)
	// OR-merge: generic's false must not clobber typescript's true.
	assert.Equal(t, analysis.PerFileFinding{
		Path:           "src/payments/refund.ts",
		SymbolsChanged: true,
		APIDiff:        true,
		ConfigDiff:     true,
		Symbols:        []string{"refundInvoice", "chargeCard"},
		Notes:          []string{"ts: symbol delta", "generic: path delta"},
	}, *refund)
	require.NotNil(t, testFile)
	assert.True(t, testFile.DeepDeclined)
}

func TestComposeAnalyzers_ThrowingPluginIsDeclaredUnavailableAndAnalysisContinues(t *testing.T) {
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		throwingAnalyzerPlugin(schemas.AnalyzerNameTypeScript, errors.New("tree-sitter exploded")),
		fixedOutputPlugin{name: schemas.AnalyzerNameGeneric, output: genericAnalyzerOutput()},
	}, nil)
	require.NoError(t, err)
	result := composed.RunAnalyzers(pluginBaseInput())

	// One coverage entry per plugin run: typescript (failed) + generic (ok).
	require.Len(t, result.Coverage, 2)
	tsCoverage := result.Coverage[0]
	assert.Equal(t, schemas.AnalyzerNameTypeScript, tsCoverage.Analyzer)
	assert.Equal(t, schemas.AnalyzerStatusUnavailable, tsCoverage.Status)
	// D14: never silent — the note must be present and explain the failure.
	require.NotNil(t, tsCoverage.Note)
	assert.Contains(t, *tsCoverage.Note, "failed")
	assert.Contains(t, *tsCoverage.Note, "tree-sitter exploded")
	assert.Equal(t, schemas.AnalyzerCoverage{
		Analyzer: schemas.AnalyzerNameGeneric,
		Status:   schemas.AnalyzerStatusOK,
	}, result.Coverage[1])
	// Analysis CONTINUED with the remaining plugin.
	assert.Equal(t, []analysis.PerFileFinding{
		perFileFinding("src/payments/refund.ts", func(f *analysis.PerFileFinding) {
			f.ConfigDiff = true
		}),
	}, result.Files)
}

func TestComposeAnalyzers_NonErrorFailureIsStillDeclaredInTheCoverageNote(t *testing.T) {
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		throwingAnalyzerPlugin(schemas.AnalyzerNameTypeScript, errors.New("string boom")),
		fixedOutputPlugin{name: schemas.AnalyzerNameGeneric, output: genericAnalyzerOutput()},
	}, nil)
	require.NoError(t, err)
	result := composed.RunAnalyzers(pluginBaseInput())
	assert.Equal(t, schemas.AnalyzerStatusUnavailable, result.Coverage[0].Status)
	require.NotNil(t, result.Coverage[0].Note)
	assert.Contains(t, *result.Coverage[0].Note, "string boom")
}

func TestComposeAnalyzers_AllPluginsUnavailableLeavesEveryFileUnanalyzed(t *testing.T) {
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		throwingAnalyzerPlugin(schemas.AnalyzerNameTypeScript, errors.New("ts down")),
		throwingAnalyzerPlugin(schemas.AnalyzerNameGeneric, errors.New("generic down")),
	}, nil)
	require.NoError(t, err)
	result := composed.RunAnalyzers(pluginBaseInput())

	// No findings at all: unanalyzed, never an all-false "no changes" row.
	assert.Empty(t, result.Files)
	analyzedPaths := make(map[string]bool, len(result.Files))
	for _, finding := range result.Files {
		analyzedPaths[finding.Path] = true
	}
	for _, file := range pluginBaseInput().Files {
		assert.False(t, analyzedPaths[file.Path])
	}
	// Coverage is still declared per plugin (V-P1-13 groundwork).
	require.Len(t, result.Coverage, 2)
	for _, entry := range result.Coverage {
		assert.Equal(t, schemas.AnalyzerStatusUnavailable, entry.Status)
		require.NotNil(t, entry.Note)
		assert.NotEmpty(t, *entry.Note)
	}
	assert.Equal(t, schemas.AnalyzerNameTypeScript, result.Coverage[0].Analyzer)
	assert.Equal(t, schemas.AnalyzerNameGeneric, result.Coverage[1].Analyzer)
}

func TestComposeAnalyzers_StubPluginStillYieldsItsDeclaredCoverageEntry(t *testing.T) {
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		fixedOutputPlugin{name: schemas.AnalyzerNameTypeScript, output: tsStubAnalyzerOutput()},
		fixedOutputPlugin{name: schemas.AnalyzerNameGeneric, output: genericAnalyzerOutput()},
	}, nil)
	require.NoError(t, err)
	result := composed.RunAnalyzers(pluginBaseInput())

	// The stub's coverage entry passes through verbatim, first in OD-4 order.
	assert.Equal(t, schemas.AnalyzerCoverage{
		Analyzer: schemas.AnalyzerNameTypeScript,
		Status:   schemas.AnalyzerStatusStub,
		Note:     testStrPtr(analysis.TypeScriptStubNote),
	}, result.Coverage[0])
	assert.Len(t, result.Coverage, 2)
	// The stub emitted no findings; the generic analyzer's finding survives.
	assert.Equal(t, []analysis.PerFileFinding{
		perFileFinding("src/payments/refund.ts", func(f *analysis.PerFileFinding) {
			f.ConfigDiff = true
		}),
	}, result.Files)
}

func TestTypeScriptStubNote_CrossCopyDriftGuard(t *testing.T) {
	// Copy 1: the analyzer package's source on disk (the declared authority)
	// — awaited: packages/analyzer-typescript ports to
	// internal/analyzers/tstool (wave W7); once it lands, this guard extends to
	// parsing its TypeScriptStubNote literal out of that source, exactly as the
	// TS suite reads '../../analyzer-typescript/src/index.ts'.
	//
	// Copy 2: the canonical schema example (imported, not parsed from disk) —
	// the 'typescript' stub coverage entry's note.
	var stubNote *string
	for i := range schemas.ChangeAnalysisExample.AnalyzerCoverage {
		entry := schemas.ChangeAnalysisExample.AnalyzerCoverage[i]
		if entry.Analyzer == schemas.AnalyzerNameTypeScript && entry.Status == schemas.AnalyzerStatusStub {
			stubNote = entry.Note
			break
		}
	}
	require.NotNil(t, stubNote)

	// Copy 3 is the plugin-seam constant itself: the example copy must
	// byte-equal it (string identity — the strictest equality for strings).
	assert.Equal(t, analysis.TypeScriptStubNote, *stubNote)
}

func TestSymbolsDetailMerge_FirstSeenWinsAndLaterPluginNeverErases(t *testing.T) {
	detail := &schemas.SymbolsDetail{
		Added:   []string{"fresh"},
		Removed: []string{"gone"},
		Changed: []schemas.SymbolChangeDetail{{
			Name:        "idempotencyKey",
			ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeSignature, schemas.SymbolChangeType},
		}},
	}
	ts := fixedOutputPlugin{
		name: schemas.AnalyzerNameTypeScript,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{Analyzer: schemas.AnalyzerNameTypeScript, Status: schemas.AnalyzerStatusOK},
			Files: []analysis.PerFileFinding{
				perFileFinding("src/a.ts", func(f *analysis.PerFileFinding) {
					f.SymbolsChanged = true
					f.SymbolsDetail = detail
				}),
			},
		},
	}
	generic := fixedOutputPlugin{
		name: schemas.AnalyzerNameGeneric,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK},
			Files: []analysis.PerFileFinding{
				perFileFinding("src/a.ts", func(f *analysis.PerFileFinding) {
					f.ConfigDiff = true
				}),
			},
		},
	}
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{ts, generic}, nil)
	require.NoError(t, err)
	result := composed.RunAnalyzers(pluginBaseInput())
	require.Len(t, result.Files, 1)
	assert.Equal(t, detail, result.Files[0].SymbolsDetail)
}

func TestSymbolsDetailMerge_LaterPluginMaySupplyDetailAndAbsentStaysAbsent(t *testing.T) {
	detail := &schemas.SymbolsDetail{
		Added:   []string{"fresh"},
		Removed: []string{"gone"},
		Changed: []schemas.SymbolChangeDetail{{
			Name:        "idempotencyKey",
			ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeSignature, schemas.SymbolChangeType},
		}},
	}
	ts := fixedOutputPlugin{
		name: schemas.AnalyzerNameTypeScript,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{Analyzer: schemas.AnalyzerNameTypeScript, Status: schemas.AnalyzerStatusStub},
			Files:    nil,
		},
	}
	genericWithDetail := fixedOutputPlugin{
		name: schemas.AnalyzerNameGeneric,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK},
			Files: []analysis.PerFileFinding{
				perFileFinding("src/payments/refund.ts", func(f *analysis.PerFileFinding) {
					f.SymbolsDetail = detail
				}),
			},
		},
	}
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{ts, genericWithDetail}, nil)
	require.NoError(t, err)
	result := composed.RunAnalyzers(pluginBaseInput())
	require.Len(t, result.Files, 1)
	assert.Equal(t, detail, result.Files[0].SymbolsDetail)

	composedPlain, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{
		ts,
		fixedOutputPlugin{name: schemas.AnalyzerNameGeneric, output: genericAnalyzerOutput()},
	}, nil)
	require.NoError(t, err)
	plain := composedPlain.RunAnalyzers(pluginBaseInput())
	require.Len(t, plain.Files, 1)
	assert.Nil(t, plain.Files[0].SymbolsDetail)
}

func TestMergeFindings_DeepDeclinedORsAndLaterPluginMaySupplySymbolsDetail(t *testing.T) {
	ts := fixedOutputPlugin{
		name: schemas.AnalyzerNameTypeScript,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{
				Analyzer: schemas.AnalyzerNameTypeScript,
				Status:   schemas.AnalyzerStatusOK,
			},
			Files: []analysis.PerFileFinding{
				perFileFinding("src/a.ts", func(f *analysis.PerFileFinding) {
					f.Symbols = []string{"a"}
					f.Notes = []string{"ts note"}
				}),
			},
		},
	}
	detail := &schemas.SymbolsDetail{
		Added:   []string{"fresh"},
		Removed: []string{},
		Changed: []schemas.SymbolChangeDetail{{
			Name:        "internal",
			ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeVisibility},
		}},
	}
	generic := fixedOutputPlugin{
		name: schemas.AnalyzerNameGeneric,
		output: analysis.AnalyzerOutput{
			Coverage: schemas.AnalyzerCoverage{
				Analyzer: schemas.AnalyzerNameGeneric,
				Status:   schemas.AnalyzerStatusOK,
			},
			Files: []analysis.PerFileFinding{
				perFileFinding("src/a.ts", func(f *analysis.PerFileFinding) {
					f.APIDiff = true
					f.DeepDeclined = true
					f.SymbolsDetail = detail
				}),
			},
		},
	}
	composed, err := analysis.ComposeAnalyzers([]analysis.AnalyzerPlugin{ts, generic}, nil)
	require.NoError(t, err)
	result := composed.RunAnalyzers(pluginBaseInput())

	require.Len(t, result.Files, 1)
	merged := result.Files[0]
	assert.True(t, merged.DeepDeclined)           // OR-merge: never clobbered away
	assert.Equal(t, detail, merged.SymbolsDetail) // first-seen absent → later plugin may supply
	assert.True(t, merged.APIDiff)
	assert.Equal(t, []string{"a"}, merged.Symbols)
	assert.Equal(t, []string{"ts note"}, merged.Notes)
}
