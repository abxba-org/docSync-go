package orchestrator

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrchCov_resolveOptions(t *testing.T) {
	resolved := resolveOptions(nil)
	assert.True(t, resolved.unicode)
	assert.True(t, resolved.quiet)
	assert.Equal(t, 5, resolved.maxAbstainRows)

	resolved = resolveOptions(&CheckRenderOptions{
		Glyphs: GlyphsFallback, Quiet: boolp(false), MaxAbstainRows: intp(-3), IncludeUsage: true,
	})
	assert.False(t, resolved.unicode)
	assert.False(t, resolved.quiet)
	assert.Equal(t, 0, resolved.maxAbstainRows)
	assert.True(t, resolved.includeUsage)

	resolved = resolveOptions(&CheckRenderOptions{Quiet: boolp(true), MaxAbstainRows: intp(10)})
	assert.True(t, resolved.quiet)
	assert.Equal(t, 10, resolved.maxAbstainRows)
}

func TestOrchCov_GlyphForOutcome(t *testing.T) {
	assert.Equal(t, schemas.ReportGlyphOk, GlyphForOutcome(schemas.PipelineOutcomeIgnore))
	assert.Equal(t, schemas.ReportGlyphReview, GlyphForOutcome(schemas.PipelineOutcomeReview))
	assert.Equal(t, schemas.ReportGlyphWarn, GlyphForOutcome(schemas.PipelineOutcomeSuggest))
	assert.Equal(t, schemas.ReportGlyphWarn, GlyphForOutcome(schemas.PipelineOutcomeAuto))
	assert.Equal(t, schemas.ReportGlyphLocked, GlyphForOutcome(schemas.PipelineOutcomeBlocked))
	assert.Equal(t, schemas.ReportGlyphReview, GlyphForOutcome(schemas.PipelineOutcome("bogus")))
}

func TestOrchCov_describeError(t *testing.T) {
	assert.Equal(t, "undefined", describeError(nil))
	assert.Equal(t, "boom", describeError(errors.New("boom")))
	assert.Equal(t, "Error", describeError(&emptyError{}))
}

func TestOrchCov_isJevConfigError(t *testing.T) {
	assert.False(t, isJevConfigError(nil))
	assert.False(t, isJevConfigError(errors.New("other")))
	assert.True(t, isJevConfigError(&codedError{code: "JEV_CONFIG"}))
	assert.False(t, isJevConfigError(&codedError{code: "OTHER"}))
}

func TestOrchCov_compareText(t *testing.T) {
	assert.Equal(t, -1, compareText("a", "b"))
	assert.Equal(t, 1, compareText("b", "a"))
	assert.Equal(t, 0, compareText("a", "a"))
	assert.Equal(t, 0, compareText("", ""))
}

func TestOrchCov_NextStage(t *testing.T) {
	g := DeclareStageGraph()
	assert.Len(t, g, 10)
	assert.Equal(t, StageMap, NextStage(StageAnalyze, RoutingContinue))
	assert.Equal(t, StagePersist, NextStage(StageJev, RoutingIgnore))
	assert.Equal(t, StagePolicy, NextStage(StageJev, RoutingReview))
	assert.Equal(t, StageRetrieval, NextStage(StageJev, RoutingContinue))
	assert.Equal(t, StagePolicy, NextStage(StageVerification, RoutingContinue))
	assert.Equal(t, StagePersist, NextStage(StagePolicy, RoutingContinue))
	assert.Equal(t, StageName(""), NextStage(StageName("bogus"), RoutingContinue))
}

func TestOrchCov_manifestModeOf(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/a.md", Mode: modePtr(schemas.DocModeLocked)},
			{Path: "docs/b.md", Sources: []string{"src/b/**"}},
		},
		Defaults: &schemas.ManifestDefaults{Mode: modePtr(schemas.DocModeSuggest)},
	}
	require.Equal(t, "locked", string(manifestModeOf(manifest, "docs/a.md")))
	assert.Equal(t, "suggest", string(manifestModeOf(manifest, "docs/b.md")))
	assert.Equal(t, "suggest", string(manifestModeOf(manifest, "docs/unknown.md")))
	assert.Equal(t, "suggest", string(manifestModeOf(schemas.Manifest{Version: 1}, "docs/x.md")))
}

func TestOrchCov_applyDocBase(t *testing.T) {
	doc := schemas.RunDocument{DocPath: "docs/a.md"}
	applyDocBase(&doc, docBase{policyOutcome: ptr(schemas.PipelineOutcomeAuto), partialCoverage: boolp(true)})
	assert.Equal(t, schemas.PipelineOutcomeAuto, doc.PolicyOutcome)
	assert.True(t, doc.PartialCoverage)
}

func boolp(v bool) *bool                                     { return &v }
func intp(v int) *int                                        { return &v }
func modePtr(m schemas.DocMode) *schemas.DocMode             { return &m }
func ptr(t schemas.PipelineOutcome) *schemas.PipelineOutcome { return &t }

type emptyError struct{}

func (e *emptyError) Error() string { return "" }

type codedError struct{ code string }

func (e *codedError) Error() string { return "coded" }
func (e *codedError) Code() string  { return e.code }
