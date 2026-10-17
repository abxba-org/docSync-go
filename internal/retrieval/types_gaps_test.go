package retrieval

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRetrievalLevelEvidenceContracts(t *testing.T) {
	// RetrievalSymbolEvidence implements RetrievalLevelEvidence.
	var sym RetrievalSymbolEvidence
	_, symImplsInterface := any(sym).(RetrievalLevelEvidence)
	assert.True(t, symImplsInterface, "RetrievalSymbolEvidence must implement RetrievalLevelEvidence")

	// RetrievalApiSchemaEvidence implements RetrievalLevelEvidence.
	var api RetrievalApiSchemaEvidence
	_, apiImplsInterface := any(api).(RetrievalLevelEvidence)
	assert.True(t, apiImplsInterface, "RetrievalApiSchemaEvidence must implement RetrievalLevelEvidence")

	// Calling retrievalLevelEvidence() on both is a no-op and does not panic.
	assert.NotPanics(t, func() {
		RetrievalSymbolEvidence{}.retrievalLevelEvidence()
	})
	assert.NotPanics(t, func() {
		RetrievalApiSchemaEvidence{}.retrievalLevelEvidence()
	})
}
