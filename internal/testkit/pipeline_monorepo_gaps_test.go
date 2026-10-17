package testkit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCOV_TESTKIT_Internal_BuildMonorepoPipelineSizedInvalidManifest exercises
// the manifest-parse error branch of BuildMonorepoPipelineSized
// (pipeline-fixtures.go:993-996). A corrupt manifest on disk has no effect:
// the constructor rewrites the manifest via WriteMonorepoFixtureTree before
// reading and parsing it, so ParseManifest always sees the fixture's own valid
// manifest and the parse-error branch is unreachable through this constructor.
// This test pins that behavior (corruption is overwritten, build succeeds).
func TestCOV_TESTKIT_Internal_BuildMonorepoPipelineSizedInvalidManifest(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, WriteMonorepoFixtureTree(root, 2, 2, 2))

	// Corrupt the manifest on disk: invalid YAML that ParseManifest would
	// reject ... if the constructor ever read it. It does not — the
	// constructor rewrites manifest.yml first.
	corrupt := "version: 1\ndocuments:\n  - [unclosed\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".docsync", "manifest.yml"), []byte(corrupt), 0o644))

	pipeline, err := BuildMonorepoPipelineSized(root, 2, 2, 2)
	require.NoError(t, err)
	require.NotNil(t, pipeline)
	assert.Equal(t, 2, pipeline.PackageCount)
}

// TestCOV_TESTKIT_Internal_BuildMonorepoPipelineSizedGraphErrors pins the
// graph-error branch of BuildMonorepoPipelineSized
// (pipeline-fixtures.go:997-1001). Monorepo fixtures carry no concepts.yml, so
// graph.BuildDocumentGraph is invoked with Concepts == nil and can never
// accumulate GraphConfigErrors (graph/mapping.go only reports errors for
// concept declarations). The graph-error branch is therefore provably
// unreachable through this constructor; this test pins that a valid build
// yields an error-free graph.
func TestCOV_TESTKIT_Internal_BuildMonorepoPipelineSizedGraphErrors(t *testing.T) {
	root := t.TempDir()
	pipeline, err := BuildMonorepoPipelineSized(root, 2, 2, 2)
	require.NoError(t, err)
	require.NotNil(t, pipeline)
	assert.Empty(t, pipeline.Graph.Errors)
	assert.Equal(t, 2, pipeline.PackageCount)
	assert.Equal(t, 2, pipeline.AreaCount)
	assert.Equal(t, 2, pipeline.FileCount)
}

// TestCOV_TESTKIT_Internal_BuildMonorepoPipelineSizedNewFixtureRepoError pins
// the NewFixtureRepo error branch of BuildMonorepoPipelineSized
// (pipeline-fixtures.go:1002-1005). NewFixtureRepo only binds the git client
// and opens the state store over an existing root, so it never fails here; the
// error branch is unreachable through this constructor. This test pins the
// success path instead.
func TestCOV_TESTKIT_Internal_BuildMonorepoPipelineSizedNewFixtureRepoError(t *testing.T) {
	root := t.TempDir()
	pipeline, err := BuildMonorepoPipelineSized(root, 1, 1, 1)
	require.NoError(t, err)
	require.NotNil(t, pipeline)
	assert.NotNil(t, pipeline.Store)
	assert.NotNil(t, pipeline.Git)
	assert.Equal(t, 1, pipeline.PackageCount)
}
