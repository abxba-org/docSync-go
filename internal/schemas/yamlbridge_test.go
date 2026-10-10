package schemas_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestYAMLBridgeRoundTripClaimSource(t *testing.T) {
	doc := []byte("schema: docsync.concepts/v1\ndocs:\n  - path: docs/payments.md\n    claims:\n      - id: claim_1\n        text: Refunds require a key.\n        anchor:\n          file: docs/payments.md\n          start_line: 3\n          end_line: 3\n          content_hash: abc\n        source:\n          file: src/payments/refund.ts\n          blob_sha: e0d1fa5\n")
	parsed, err := schemas.ParseConcepts(doc)
	require.NoError(t, err)
	require.NotEmpty(t, parsed.Docs)
	require.NotEmpty(t, parsed.Docs[0].Claims)
	assert.Equal(t, "e0d1fa5", parsed.Docs[0].Claims[0].Source.BlobSHA)
	assert.Equal(t, "src/payments/refund.ts", parsed.Docs[0].Claims[0].Source.File)

	out, err := schemas.MarshalJSONKeysYAML(parsed)
	require.NoError(t, err)
	reparsed, err := schemas.ParseConcepts(out)
	require.NoError(t, err)
	assert.Equal(t, parsed.Docs[0].Claims[0].Source.BlobSHA, reparsed.Docs[0].Claims[0].Source.BlobSHA)
}
