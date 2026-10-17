package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func TestBuildPaymentsPipeline_InvalidManifest(t *testing.T) {
	root := t.TempDir()

	err := WritePaymentsFixtureTree(root)
	require.NoError(t, err)
	err = WritePaymentsConceptsFile(root)
	require.NoError(t, err)
	err = AssertPaymentsAnchors(root)
	require.NoError(t, err)

	manifestPath := filepath.Join(root, ".docsync", "manifest.yml")
	err = os.WriteFile(manifestPath, []byte("invalid: yaml: [unclosed"), 0o644)
	require.NoError(t, err)

	manifestRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "manifest.yml"))
	require.NoError(t, err)
	_, err = schemas.ParseManifest([]byte(manifestRaw))
	require.Error(t, err)
	wrapped := fmt.Errorf("payments fixture manifest is invalid: %w", err)
	assert.Contains(t, wrapped.Error(), "manifest is invalid")
}

func TestBuildPaymentsPipeline_InvalidManifestSchema(t *testing.T) {
	root := t.TempDir()

	err := WritePaymentsFixtureTree(root)
	require.NoError(t, err)
	err = WritePaymentsConceptsFile(root)
	require.NoError(t, err)
	err = AssertPaymentsAnchors(root)
	require.NoError(t, err)

	manifestPath := filepath.Join(root, ".docsync", "manifest.yml")
	badManifest := `version: 1
documents: []
`
	err = os.WriteFile(manifestPath, []byte(badManifest), 0o644)
	require.NoError(t, err)

	manifestRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "manifest.yml"))
	require.NoError(t, err)
	_, err = schemas.ParseManifest([]byte(manifestRaw))
	require.Error(t, err)
	wrapped := fmt.Errorf("payments fixture manifest is invalid: %w", err)
	assert.Contains(t, wrapped.Error(), "manifest is invalid")
}

func TestBuildPaymentsPipeline_InvalidConcepts(t *testing.T) {
	root := t.TempDir()

	err := WritePaymentsFixtureTree(root)
	require.NoError(t, err)
	err = WritePaymentsConceptsFile(root)
	require.NoError(t, err)
	err = AssertPaymentsAnchors(root)
	require.NoError(t, err)

	conceptsPath := filepath.Join(root, ".docsync", "concepts.yml")
	err = os.WriteFile(conceptsPath, []byte("invalid: concepts: yaml: {{{"), 0o644)
	require.NoError(t, err)

	conceptsRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "concepts.yml"))
	require.NoError(t, err)
	_, err = schemas.ParseConcepts([]byte(conceptsRaw))
	require.Error(t, err)
	wrapped := fmt.Errorf("payments fixture concepts are invalid: %w", err)
	assert.Contains(t, wrapped.Error(), "concepts are invalid")
}

func TestBuildPaymentsPipeline_InvalidConceptsSchema(t *testing.T) {
	root := t.TempDir()

	err := WritePaymentsFixtureTree(root)
	require.NoError(t, err)
	err = WritePaymentsConceptsFile(root)
	require.NoError(t, err)
	err = AssertPaymentsAnchors(root)
	require.NoError(t, err)

	conceptsPath := filepath.Join(root, ".docsync", "concepts.yml")
	badConcepts := `schema: "docsync.concepts/v1"
docs:
  - path: docs/payments.md
    claims: "not-an-array"
`
	err = os.WriteFile(conceptsPath, []byte(badConcepts), 0o644)
	require.NoError(t, err)

	conceptsRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "concepts.yml"))
	require.NoError(t, err)
	_, err = schemas.ParseConcepts([]byte(conceptsRaw))
	require.Error(t, err)
	wrapped := fmt.Errorf("payments fixture concepts are invalid: %w", err)
	assert.Contains(t, wrapped.Error(), "concepts are invalid")
}

func TestBuildPaymentsPipeline_GraphErrors(t *testing.T) {
	root := t.TempDir()

	err := WritePaymentsFixtureTree(root)
	require.NoError(t, err)
	err = WritePaymentsConceptsFile(root)
	require.NoError(t, err)
	err = AssertPaymentsAnchors(root)
	require.NoError(t, err)

	// Create graph configuration errors by having concepts reference a doc not in manifest
	conceptsPath := filepath.Join(root, ".docsync", "concepts.yml")
	badConcepts := `{
  "schema": "docsync.concepts/v1",
  "docs": [
    {
      "path": "docs/payments.md",
      "claims": [
        {
          "id": "claim_refund_idempotency",
          "text": "Refund requests require an ` + "`idempotencyKey`" + `.",
          "anchor": {"file": "docs/payments.md", "start_line": 8, "end_line": 8},
          "source": {"file": "src/payments/refund.ts", "line": 10, "blob_sha": "fake"}
        }
      ]
    },
    {
      "path": "docs/nonexistent.md",
      "claims": [
        {
          "id": "claim_bad",
          "text": "bad",
          "anchor": {"file": "docs/nonexistent.md", "start_line": 1, "end_line": 1},
          "source": {"file": "src/payments/refund.ts", "line": 1, "blob_sha": "fake"}
        }
      ]
    }
  ]
}
`
	err = os.WriteFile(conceptsPath, []byte(badConcepts), 0o644)
	require.NoError(t, err)

	manifestRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "manifest.yml"))
	require.NoError(t, err)
	manifest, err := schemas.ParseManifest([]byte(manifestRaw))
	require.NoError(t, err)

	conceptsRaw, err := ReadRepoFile(root, filepath.Join(schemas.DocSyncDir, "concepts.yml"))
	require.NoError(t, err)
	concepts, err := schemas.ParseConcepts([]byte(conceptsRaw))
	require.NoError(t, err)

	documentGraph := graph.BuildDocumentGraph(graph.BuildDocumentGraphInput{
		Manifest:    *manifest,
		Concepts:    concepts,
		ConceptsRaw: conceptsRaw,
	})
	require.NotEmpty(t, documentGraph.Errors)
	err = fmt.Errorf("payments fixture graph has configuration errors: %s", renderGraphErrors(documentGraph.Errors))
	assert.Contains(t, err.Error(), "graph has configuration errors")
}
