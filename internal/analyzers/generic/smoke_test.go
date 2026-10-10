// Ported from packages/analyzer-generic/test/smoke.test.ts.
package generic_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DUDE616/docSync-go/internal/analyzers/generic"
)

func TestExposesAVersion(t *testing.T) {
	assert.Equal(t, "0.0.0", generic.AnalyzerGenericPackageVersion)
	assert.NotEmpty(t, generic.AnalyzerGenericPackageVersion)
}
